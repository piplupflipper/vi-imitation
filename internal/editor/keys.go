package editor

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// keyInput は、端末から届いた入力 1 件である。paste が空でなければ貼り付けで、key は読まない。
type keyInput struct {
	// c は、入力を受けた client である。
	c     *clientView
	key   editcmd.Key
	paste string
	// mouseRow と mouseCol は、key がマウスの出来事のときの、受けた端末の画面の位置である（FR-185。mouse.go）。
	mouseRow, mouseCol int
	// comp は、入力を受けた部品の client である（component.go）。nil なら c の端末で受けた。
	// 部品の client で受けた入力は、元の client c の操作として扱う（基本設計 1.2）。
	comp *component
}

// keyQueue は、端末から読んだキーと貼り付けの列である。読む goroutine が積み、イベントループが取り出す。
// 上限を持たないのは、イベントループが長い操作の途中でも、読む goroutine が <C-c> を読めるようにするためである。
type keyQueue struct {
	mu    sync.Mutex
	items []keyInput
	// ready は、列が空でないことを知らせる。容量 1 で、取り出した後も残っていれば知らせ直す。
	ready chan struct{}
}

func newKeyQueue() *keyQueue { return &keyQueue{ready: make(chan struct{}, 1)} }

func (q *keyQueue) push(in keyInput) {
	q.mu.Lock()
	q.items = append(q.items, in)
	q.mu.Unlock()
	signal(q.ready)
}

// pushInterrupt は、中断の印 flag と client の印 clientFlag を立て、中断のキー in を積む。3 つを錠の中で行うので、
// 印が立ったのを見てから dropThrough を呼べば、in はもう列にある。
// client の印は、錠の外から読まずに interrupted で読む（#294）。
func (q *keyQueue) pushInterrupt(in keyInput, flag, clientFlag *atomic.Bool) {
	q.mu.Lock()
	flag.Store(true)
	clientFlag.Store(true)
	q.items = append(q.items, in)
	q.mu.Unlock()
	signal(q.ready)
}

// interrupted は、client c の中断の印（c.intr）が立っているかを、列の錠の中で読む。
//
// 再生は、中断の印（Editor.interrupt）を錠の外から読んで止まる。その時点の読む goroutine は、pushInterrupt の錠の中で
// 中断の印だけを立て、client の印と <C-c> をまだ置いていないことがある。錠の外から client の印を読むと、再生は止まったのに
// 後始末（印を下ろして <C-c> までを捨てる）を飛ばし、残った <C-c> を後で <Esc> として処理して「中断しました」を消す（#294）。
// 錠の中で読めば、pushInterrupt が終わるのを待つので、中断の印を見た後なら client の印も <C-c> もそろっている。
func (q *keyQueue) interrupted(c *clientView) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return c.intr.Load()
}

// popFor は、列の中で allowed が true を返す client の最初の入力を取り出す。
// 取り出した後も列に入力が残っていれば、ready で知らせ直す。
func (q *keyQueue) popFor(allowed func(*clientView) bool) (keyInput, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, k := range q.items {
		if !allowed(k.c) {
			continue
		}
		q.items = slices.Delete(q.items, i, i+1)
		if len(q.items) > 0 {
			signal(q.ready)
		}
		return k, true
	}
	return keyInput{}, false
}

func (q *keyQueue) empty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) == 0
}

// any は、列に allowed が true を返す client の入力があるかを返す。
func (q *keyQueue) any(allowed func(*clientView) bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.ContainsFunc(q.items, func(k keyInput) bool { return allowed(k.c) })
}

// has は、列に client c の入力があるかを返す。
func (q *keyQueue) has(c *clientView) bool {
	return q.any(func(o *clientView) bool { return o == c })
}

// drop は、列から client c の入力をすべて捨てる。
func (q *keyQueue) drop(c *clientView) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = slices.DeleteFunc(q.items, func(k keyInput) bool { return k.c == c })
}

// dropThrough は、列の中の client c の入力のうち、先頭から k までのキー（k を含む）と、その間の貼り付けを捨てる。
// c の k が無ければ何もしない。他の client の入力は残す。
// 中断（<C-c>）の後に、中断の前に先打ちしたキーを実行しないために使う（Vim と同じ）。
func (q *keyQueue) dropThrough(k editcmd.Key, c *clientView) {
	q.mu.Lock()
	defer q.mu.Unlock()
	end := -1
	for i, x := range q.items {
		if x.c == c && x.paste == "" && x.key == k {
			end = i
			break
		}
	}
	if end < 0 {
		return
	}
	kept := q.items[:0:0]
	for i, x := range q.items {
		if i > end || x.c != c {
			kept = append(kept, x)
		}
	}
	q.items = kept
}

// handleKeys は、列からキーを 1 個取り出して処理する。残りがあれば ready が知らせ直すので、次の周回で処理する。
// 1 個ずつにするのは、キーの間に届いた拡張機能のイベントも、届いた順に近く処理するためである。
// 外部のコマンドの実行中は、それを打った client のキーを列に残して終わってから処理し、問いの間は、問いを出した client のキーだけを処理する（keyAllowed）。
// <C-c> による外部のコマンドの中断は intrCh で受ける（shell.go）。
func (e *Editor) handleKeys() {
	e.syncAsker()
	in, ok := e.keys.popFor(e.keyAllowed)
	if !ok || in.c.detached {
		return
	}
	// 利用者がどれかの client でキーを打ったら、ペインで選んだ後のフォーカスを戻す印を下ろす（focusAfterPane。PR #248 の再レビューの必須）。
	// ペインの部品の client のキーで選んだときは、このキーの処理の中で立て直す。
	clear(e.extFromPane)
	e.useClient(in.c)
	// この client の前の選択への editor/execute は、次のキーを打った時点で無効にする（list.go の revokeExecute）。
	// 部品の client（端末多重化ソフトのポップアップとペイン）で打ったキーも、元の client（in.c）のキーとして数える。
	e.revokeExecute(in.c)
	if in.comp == nil {
		// この client の端末でキーを受けたので、フォーカスはこの client のペインに戻っている（cursorhold.go の leaveFocus）。
		in.c.focusAway = false
	}
	if in.paste == "" && in.key.IsMouse() {
		// マウスの出来事の位置を、キーマップと操作の表を通った後の操作が解けるように覚える（mouse.go）。
		e.noteMouse(in.c, in.comp, in.key, in.mouseRow, in.mouseCol)
	}
	if in.comp != nil {
		e.componentInput(in)
		return
	}
	if in.paste != "" {
		e.handlePaste(in.paste)
	} else {
		e.handleKey(in.key)
	}
	e.afterKey()
}

// afterKey は、キーの処理の後に、中断の後始末と、曖昧なキーの列の時間切れの準備をする。
func (e *Editor) afterKey() {
	e.afterFloatKey()
	e.resumeFollow(e.float) // ビジュアルモードかキーの列を抜けたら、末尾への追従に戻す（viewappend.go。FR-272）
	if c := e.op; e.keys.interrupted(c) {
		// このキーで始めた外部のコマンドと再生中の拡張機能の待ちは、知らせ（intrCh）を Run が先に取っていても、ここで中断する。
		e.interruptShell()
		e.interruptReplay()
		e.interruptCompletion()
		// 処理の途中に <C-c> が押された。中断はもう効いたので印を下ろし、<C-c> までの先打ちを捨てる。
		e.interrupt.Store(false)
		c.intr.Store(false)
		e.keys.dropThrough(interruptKey, c)
	}
	if e.quit {
		// 最後のタブを閉じた client は、この後に外す（reapClients）。
		e.stopKeyTimer()
		e.stopHold(e.op)
		e.extRan, e.fromCmdline = false, false
		return
	}
	if e.remap.pendingKeys() || e.cur.state.Pending() {
		e.armKeyTimer()
	} else {
		e.stopKeyTimer()
	}
	// キーを打ったので、カーソルが止まった契機を待ち直す（cursorhold.go。FR-171）。
	// ペインの部品の client のキー（paneInput の途中）では待ち直さず、元の client のタイマーを止める（利用者はペインを見ている）。
	if e.paneActive != nil {
		e.stopHold(e.view)
	} else {
		e.armHold()
	}
	if e.extRan {
		// コマンドラインから実行した拡張機能のコマンドの後は、記録した外での変更の問いを出してよい時点である（askNotice）。
		// ノーマルモードのキーに割り当てたコマンドの後は出さない。続けて打つ l や o を答えに取らないためである（PR #149 の再レビュー）。
		// 出さなかった問い、打ちかけのキーや先読みのキーがあって出せなかった問いは、記録したまま次の安全な時点へ回る。
		e.extRan = false
		if e.fromCmdline {
			e.askNotice()
		}
	}
	e.fromCmdline = false
}

// armKeyTimer は、操作中の client の、曖昧なキーの列を確定させるタイマーを掛け直す。
// 時間が来たら、keyFireCh でイベントループへ知らせる（client ごとに、他の client のキーで止まらないように持つ）。
func (e *Editor) armKeyTimer() {
	c := e.op
	if c.keyTimer != nil {
		c.keyTimer.Stop()
	}
	c.keyGen++
	f := keyFire{c: c, gen: c.keyGen}
	stop := e.stop
	c.keyTimer = time.AfterFunc(e.keyTimeout(), func() {
		select {
		case e.keyFireCh <- f:
		case <-stop:
		}
	})
}

// stopKeyTimer は、操作中の client のタイマーを止める。止める前に送り始めた知らせは、世代が違うので捨てる。
func (e *Editor) stopKeyTimer() {
	c := e.op
	if c.keyTimer != nil {
		c.keyTimer.Stop()
	}
	c.keyGen++
}

// handleKey は、利用者のキーを 1 個処理する。
func (e *Editor) handleKey(k editcmd.Key) {
	if k == interruptKey {
		e.interrupt.Store(false)
		e.op.intr.Store(false)
		k = editcmd.Esc
	}
	if e.dismissIntro() {
		// :intro の案内を出していれば、このキーは案内を消すだけで、コマンドとしては使わない（intro.go。FR-207）。
		return
	}
	if k.IsMouse() && (e.confirm != nil || e.prompt != nil || e.activeChoice() != nil || e.regList != nil) {
		// 問いと一覧の窓を出している間のマウスの出来事（ホイールなど）は、答えにも一覧の操作にもせずに捨てる（FR-184）。
		// 答えにすると、問いの間にホイールを回しただけで y でない答え（n）になるためである。
		return
	}
	e.dismissFloat(k)
	if e.confirm != nil {
		// 答えは、問いを出したバッファの State へ渡す。問いの間は拡張機能のイベントを取っておくので、そのバッファと範囲は変わっていない。
		cf, bs := e.confirm, e.confirmBS
		e.confirm, e.confirmBS = nil, nil
		e.message = noMessage
		yes := k == editcmd.Rune('y')
		_ = e.handleResult(bs, bs.state.Confirm(cf, yes)) // 失敗は handleResult が知らせる
		return
	}
	if e.prompt != nil {
		e.promptKey(k)
		return
	}
	if e.activeChoice() != nil {
		e.choiceKey(k)
		return
	}
	if e.discardAfterWithdrawn(k) {
		// 拡張機能が取り下げた問いに向けて打ち続けたキーは、編集のコマンドにしない（inputask.go。PR #411 のレビューの必須 1）。
		return
	}
	e.endChoices()
	if e.regList != nil {
		e.message = noMessage
		e.regListKey(k)
		return
	}
	if e.hitEnter() && (k == editcmd.Special(editcmd.CodeEnter) || k == editcmd.Rune(' ')) {
		// Vim の hit-enter の問いと同じく、<CR> と <Space> はメッセージを消すだけで、コマンドとしては使わない。
		// 他のキーは、メッセージを消してからコマンドとして使う。
		e.message = noMessage
		return
	}
	e.message = noMessage
	// 利用者がコマンドラインで打ったキー（:acp.prompt ...<CR> の <CR>）かを覚える。afterKey が、拡張機能のコマンドの後に
	// 記録した問いを出してよいかを決めるのに使う。キーマップの展開で入ったコマンドラインは、利用者のキーがノーマルモードなので数えない。
	e.fromCmdline = e.cur.state.Mode() == command.ModeCommandLine
	e.feedMapped(e.remap.feed(e.mapMode(), k))
}

// feedMapped は、キーマップを通した後のキーの列を、1 個ずつ command.State へ渡す。キーが失敗したら残りを捨てる。
// 途中のキーで外部のコマンド（:{range}! と :r !）を始めたか、再生中の拡張機能の結果を待ち始めたら、残りのキーを待ちに持たせ、
// 終わった後に、再び展開せずに続ける（Vim はコマンドが終わってから展開の残りを実行する）。
func (e *Editor) feedMapped(keys []editcmd.Key) {
	for i, k := range keys {
		if e.dismissIntro() {
			// 展開の途中の :intro の案内は、展開の次のキーで消し、そのキーは使わない（Neovim の plain_vgetc が先行入力から読むのと同じ。intro.go）。
			continue
		}
		ok := e.feedKey(k)
		if h := e.suspension(); h != nil {
			h.mapped = append(h.mapped, keys[i+1:]...)
			return
		}
		if !ok {
			e.remap.reset()
			return
		}
	}
}

// handlePaste は、端末から貼り付けたテキストを、カーソルのあるバッファへ入れる（#114）。
// 貼り付けはキーマップを通さない（Vim と同じ）。キーマップの接頭辞を待っていれば、時間切れと同じく先に確定させて実行する。
// 確認の問いと choice/ask の問いを出している間の貼り付けは、答えにせずに捨てる。choice/ask の問いでは、捨てたことを数えて出す（#151）。
// input/ask の問いを出している間は、入力の中身に入れる（inputPaste。FR-275）。
func (e *Editor) handlePaste(text string) {
	// :intro の案内は消すが、貼り付けたテキストは捨てずに入れる（intro.go。FR-207）。
	e.dismissIntro()
	e.dismissFloat(editcmd.Key{})
	if e.asking() {
		return
	}
	if q := e.activeChoice(); q != nil {
		if q.input != nil {
			// 1 行の入力の問いには、貼り付けたテキストを入れる（FR-275）。
			e.inputPaste(text)
			return
		}
		e.dropChoiceInput()
		return
	}
	if w := e.withdrawn; w != nil && w.input && w.c == e.op {
		// 取り下げた入力の問いに向けた貼り付けも、編集にしない（discardAfterWithdrawn）。
		return
	}
	e.endChoices()
	if e.regList != nil {
		// レジスターの一覧の窓を出している間の貼り付けは、一覧の操作にならないので捨てる。
		return
	}
	e.message = noMessage
	e.feedMapped(e.remap.flush())
	if h := e.suspension(); h != nil {
		// 貼り付けの前に確定させたキーで外部のコマンドか再生中の拡張機能の待ちを始めたら、貼り付けは終わった後に入れる。
		h.paste = text
		return
	}
	bs := e.cur
	_ = e.handleResult(bs, bs.state.Paste(text)) // 失敗は handleResult が知らせる
}

// flushKeys は、曖昧なキーの列を、時間切れとして確定させて実行する。
func (e *Editor) flushKeys() {
	e.feedMapped(e.remap.flush())
	if e.suspension() == nil {
		_ = e.handleResult(e.cur, e.cur.state.Flush()) // 失敗は handleResult が知らせる
	}
	e.afterKey()
}

// feedKey は、キーを 1 個 command.State へ渡し、結果を処理する。操作が失敗したら false を返す。
// マクロの記録はこの Feed が行うので、コマンドラインのキーも記録される。
// コマンドラインの補完のキー（<Tab> など。wildKey）は State へ渡さず、補った文字を記録に入れる（cmdcomplete.go の wildSetWord）。
func (e *Editor) feedKey(k editcmd.Key) bool {
	if e.wildKey(k) {
		// コマンドラインの補完のキー（cmdcomplete.go。FR-195）。State へは渡さない。
		return true
	}
	bs := e.cur
	if e.noRecord {
		return e.handleResult(bs, bs.state.FeedUnrecorded(k)) == nil
	}
	return e.handleResult(bs, bs.state.Feed(k)) == nil
}

// mapMode は、利用者のキーマップを引くモードを返す。
// ノーマルモードとビジュアルモードでは、キーの列の解釈の途中（"a、d の後など）では引かない。
func (e *Editor) mapMode() mapMode {
	st := e.cur.state
	switch st.Mode() {
	case command.ModeNormal:
		if !st.Pending() {
			return mapNormal
		}
	case command.ModeVisual, command.ModeVisualLine:
		if !st.Pending() {
			return mapVisual
		}
	case command.ModeInsert:
		return mapInsert
	case command.ModeCommandLine:
		return mapCommandLine
	}
	return mapNone
}

// handleResult は、command.State の結果を処理し、操作の失敗（無ければ nil）を返す。
// 変更を exthost へ送り（FR-16）、表示のデータをずらし、Ex コマンドの動作を行い、失敗を知らせる。
//
// マクロの再生の途中でカーソルのあるバッファが移った（command.ErrBufferSwitched）なら、
// 再生の残りを、移った先のバッファで続ける（Vim と同じく、マクロのキーはカーソルのあるバッファへ入る）。
func (e *Editor) handleResult(bs *bufferState, res command.Result) error {
	// State を呼んだ入り口によらず、結果を扱い終えた時点で、履歴の先頭が変わっていればホストのクリップボードへ送る（基本設計 4.5）。
	// 動作（:e など）でバッファを切り替えても、レジスターは TakeOver で引き継ぐので、扱い終えた後のカーソルのあるバッファのものを読む。
	defer e.syncClipboard()
	// 再生の途中の :e が bs の State を作り直したなら、res は捨てた State のもので、変更は捨てたバッファのものである。
	// 作り直す前の変更は RunActions が送り終えているので、ここでは送らない（送ると、作り直したバッファの番号で複製がずれる）。
	replaced := bs.replacedInReplay
	bs.replacedInReplay = false
	if !replaced {
		for _, ch := range res.Changes {
			e.commitChange(bs, ch)
		}
		// ヤンクした範囲に一時的な色を付ける（FR-140）。
		e.flashYank(bs, res.Yanked)
	}
	// 再生の途中で拡張機能の結果を待つために止めた。誤りではないので知らせない（残りは replayAnswered が続ける）。
	queryPending := errors.Is(res.Err, command.ErrQueryPending)
	if queryPending {
		if err := e.holdReplay(res); err != nil {
			res.Err = err
			queryPending = false
		}
	} else {
		e.trackDeferred(bs, res.Deferred)
		e.dropOrphanReplay()
	}
	if res.Message.Text != "" {
		// 検索したパターンと、検索の折り返しの警告。後に続く動作と失敗の知らせで上書きされる。
		// 検索の一致の件数（FR-167）は、パターンと同じ行の右に寄せて出す。
		level := ui.MessageInfo
		if res.Message.Warning {
			level = ui.MessageWarning
		}
		e.message = ui.Message{Text: res.Message.Text, Level: level, Right: res.Message.Count}
	}
	switched := errors.Is(res.Err, command.ErrBufferSwitched)
	// 再生の途中の外部のコマンドで再生を止めた。誤りではないので知らせない（残りは shellFinished が続ける）。
	shellPending := errors.Is(res.Err, command.ErrShellPending)
	// Ex コマンドの動作（:w など）を先に行い、操作の失敗はその後に知らせる。
	// ex は誤りのコマンドで止まるので、動作はどれも誤りより左のコマンドのものである。
	// 先に知らせると、:w|foo の E492 が :w の「書き込みました」に上書きされて見えない（Vim は :w の後に E492 を出す）。
	// 動作が失敗したら、そこで止めてその誤りを知らせ、右のコマンドの誤りは知らせない（Vim は :w が失敗すると右を実行しない）。
	// :w が外での変更を見つけて問いを出したときも、失敗と同じく右のコマンドの問いと誤りは出さない（runActions）。
	actionFailed := !e.runActions(bs, res.Actions)
	switch {
	case res.Confirm != nil && !actionFailed:
		// Err は確認を求めた E493 なので知らせず、問いを出す（render）。
		// 左の動作が失敗したら問いを出さない（:w! dir|3,1d。Vim 9.1 は :w が失敗すると右のコマンドを実行しない）。
		e.confirm, e.confirmBS = res.Confirm, bs
	case res.Err != nil && !switched && !shellPending && !queryPending && !actionFailed:
		e.reportError(res.Err)
	}
	if res.Shell != nil && !actionFailed {
		// :{range}! と :r ! の外部のコマンドを、イベントループを止めずに動かす（FR-81。shell.go）。
		// 再生の途中なら、再生の残りを持たせ、終わった後に続ける。
		var rest []command.ReplaySegment
		if shellPending {
			rest = res.Remaining
		}
		e.startShell(bs, res.Shell, rest)
	}
	e.doPendingOpens()
	if switched {
		if len(res.Remaining) == 0 || (e.cur == bs && !replaced) {
			return nil
		}
		next := e.cur
		return e.handleResult(next, next.state.ContinueReplay(res.Remaining))
	}
	return res.Err
}

// MaxClipboardEncoded は、ホストのクリップボードへ送るテキストを base64 にした後の大きさの上限（バイト）である（仮。基本設計 4.5）。
// 元のテキストでおよそ 750,000 バイトに当たる。tmux 3.4 が base64 で 1 MiB を超える OSC 52 を黙って捨てた実測（PR #170 のレビュー）による。
// 送り出しはイベントループの上で同期に書くので、この上限は書き終わるまで入力と描画が止まる時間も抑える。
const MaxClipboardEncoded = 1_000_000

// syncClipboard は、カーソルのあるバッファのレジスターの履歴の先頭が、前に判定したときから変わっていれば、
// その内容を Terminal.SetClipboard でホストのクリップボードへ送る（FR-56、基本設計 4.5）。
// 変わったかは command.Registers.Pushes の値を clipPushes と比べて判定する。1 回の結果の中で何度変わっても、送るのは最後の先頭だけである。
// 行単位のテキストは、Vim と同じく最後に改行を付けて送る。
// 上限を超えた無名のレジスターに置いただけでは Pushes が変わらないので送らない。
// base64 にした大きさが MaxClipboardEncoded を超えるテキストは送らず、送らなかったことをメッセージの行に出す。
// Config.NoClipboard なら送らない（判定した値だけ進める）。
//
// 切り替えの前のバッファの State に保留した拡張機能の結果が書き込んだレジスターは、カーソルのあるバッファの履歴ではないので送らない。
//
// req: FR-56
func (e *Editor) syncClipboard() {
	if e.cur == nil {
		return
	}
	regs := e.cur.state.Registers()
	n := regs.Pushes()
	if n <= e.clipPushes {
		return
	}
	e.clipPushes = n
	if e.cfg.NoClipboard {
		return
	}
	reg, ok := regs.Head()
	if !ok {
		return
	}
	text := reg.Text
	if reg.Linewise {
		text += "\n"
	}
	if n := base64.StdEncoding.EncodedLen(len(text)); n > MaxClipboardEncoded {
		e.message = ui.Message{
			// 要点を先頭に置き、80 桁の端末でも切れない長さにする。
			Text:  fmt.Sprintf("クリップボードへ送りませんでした: 大きすぎます（%d バイト）", len(text)),
			Level: ui.MessageWarning,
		}
		return
	}
	if err := e.term.SetClipboard(text); err != nil {
		e.log.Error("クリップボードへ送れない", "err", err)
	}
}

// reportError は、操作の失敗を知らせる。
// Vim がビープを鳴らすだけの失敗（command.ErrFailed）は知らせない。
// 期限 0 の結果の破棄（command.ErrStale）は、どの操作の結果かを知る applyDeferred が showDiscarded で知らせるので、ここでは知らせない。
func (e *Editor) reportError(err error) {
	switch {
	case errors.Is(err, command.ErrFailed), errors.Is(err, command.ErrStale):
		e.log.Debug("操作の失敗", "err", err)
	case errors.Is(err, command.ErrInterrupted):
		e.message = infoMessage("中断しました")
	default:
		e.showError(err)
	}
}
