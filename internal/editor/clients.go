package editor

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 複数の編集の client とタブ（基本設計 1.2、2.1、4.4 の段階 3 のタブとバッファ。FR-58、FR-62〜FR-65、FR-113、FR-114、FR-124）。
//
// # client ごとの状態
//
// イベントループは server に 1 つで、どの client のキーも、届いた順に 1 件ずつこの Editor が処理する（基本設計 2.1）。
// 画面に関わる状態（viewFields: 端末、描画の部品、カーソルのあるタブ、裏のタブ、ジャンプリスト、メッセージ、ホバー、
// レジスターの一覧の窓、出力の欄、キーマップの途中の状態）は client ごとに持つ。
// イベントループは、扱う client の viewFields を Editor に埋め込んだ欄へ読み込み（switchView）、他の client のものは
// clientView に退避する。段階 2 までのコードは、Editor の欄を読み書きしたまま、扱っている client の状態を扱える。
//
// キーを処理するときは、そのキーの client を読み込み（useClient）、操作中の client（op）にする。
// 拡張機能のイベント（editor/open など）は、操作中の client の状態で処理する。
// 描画は client ごとに読み込み直して行い（render）、終わったら操作中の client に戻す。
//
// # client の間で共有する状態
//
// レジスターとその履歴、直前の検索、直前の :s、直前の f、ドットリピートの値、@@ は server に 1 つで共有する（FR-58）。
// 段階 2 までと同じくバッファごとの command.State が持ち、キーを打つ client が替わるたびに、最後に持った State から
// その client のカーソルのあるタブの State へ引き継ぐ（command.State.TakeOverShared）。
// マクロの記録は引き継がない。記録は、記録を始めた client のキーだけを記録する（基本設計 10 章の仮の扱い）。
// 記録したレジスターは共有するので、client A で qa で記録したマクロを、client B で @a で再生できる。
//
// # 問いと外部のコマンド
//
// 確認の問い（:s///c、外で変わったファイルへの :w の問いなど）と、拡張機能の選択肢の問い（choice/ask）は server に 1 つで、
// 問いを出した時点の操作中の client（拡張機能の問いは、その拡張機能へ問い合わせを始めた client。asker）の画面にだけ出す。
// 答えるまで、他の client のキーは処理せずに列に残し、その client には待っていることを出す（仮）。
// Vim の問いと同じく、答えるまで他の処理をせず、問いを出したバッファと範囲を変えないためである。
// 問いを出した client が終わったら、問いは取り下げる（cancelQuestions）。
//
// # 拡張機能が後から届けるもの
//
// editor/open、message/show、choice/ask、期限 0 の問い合わせの結果は、その拡張機能へ最後に問い合わせを始めた client に出す（extView。仮）。
// ホバー（popup/show）は、そのバッファをカーソルのあるタブに持つ client に出す。
// 外部のコマンド（:{range}! と :r !）の実行中は、コマンドを打った client のキーだけを列に残し、他の client のキーは処理する（仮。基本設計 4.9）。
// 外部のコマンドを <C-c> で中断できるのは、コマンドを打った client の <C-c> だけである。
// マクロの再生と拡張機能の応答の待ちは、イベントループを止めるので、どの client の <C-c> でも中断する。
//
// # client の終わり
//
// client の最後のタブを閉じた（:q、:bd、:qa）client は終わる（FR-114）。最後の client が終われば Run が戻る（FR-61）。
// 端末の入力が閉じた（ペインを閉じた、client が異常終了した）client も終わる。そのとき、未保存の変更の無いタブのバッファは閉じ、
// 未保存の変更のあるバッファは、どのタブにもならないバッファとして残す（仮。:ls に出し、:b で開ける。変更を失わないため）。

// ClientInfo は、編集の client が server に知らせる、client ごとの情報である（基本設計 1.2。PR #207 の未確認事項）。
type ClientInfo struct {
	// ID は、server が接続ごとに割り当てた識別子である。:ls と、別の client のタブを知らせるメッセージに出す。
	ID ClientID
	// Pane は、client のペインの識別子（tmux の TMUX_PANE、herdr の HERDR_PANE_ID）である。FR-63 のフォーカスの移動に使う。
	// 端末多重化ソフトの外では空である。
	Pane string
	// WorkDir は、client の作業ディレクトリである。:e などの相対パスと、:ls のバッファの名前と、外部のコマンドの作業ディレクトリに使う。
	// 空なら Config.WorkDir を使う。
	WorkDir string
	// Env は、外部のコマンドの環境変数に、Config.ChildEnv の前に足す "名前=値" の列である（client のペインの TMUX_PANE など）。
	// server のプロセスの環境は、最初の client のものなので、client ごとに上書きする。
	Env []string
	// Files は、client の起動の引数のファイル（絶対パス）である。Attach が、その client のタブとして開く。
	Files []string
	// ReadOnly は、Files と Stdin を編集を禁止した状態で開くことを表す（-R。FR-119）。
	ReadOnly bool
	// Stdin は、client が起動の引数の - で標準入力から読んだ中身である（FR-158）。nil なら - を書いていない。
	// Attach が、Files より前に、名前の無いバッファのタブとして開く（newStdinBuffer）。開いた後は覚えておかない。
	Stdin *string
}

// viewFields は、編集の client ごとの画面の状態である。イベントループが扱う client のものを Editor に埋め込み、他は clientView に退避する。
type viewFields struct {
	term     Terminal
	renderer *ui.Renderer
	// remap は、利用者のキーマップの展開と、その途中の状態である。
	remap *remapper
	// cur は、カーソルのあるタブのバッファである。
	cur *bufferState
	// alt は、裏のタブ（直前にカーソルのあったタブ。<C-^> の移り先、:ls の #）のバッファである。無ければ nil。
	alt *bufferState
	// jumps と jumpIdx は、ジャンプリストと、<C-o> と <C-i> が次に読む位置である（buffers.go）。client ごとに持つ（基本設計 4.4 の段階 3 のジャンプリスト。仮）。
	// jumpIdx が len(jumps) なら、末尾の次（最後のジャンプの後に <C-o> をまだ打っていない）を指す。
	jumps   []jumpEntry
	jumpIdx int
	message ui.Message
	// extMessage は、最後に拡張機能の message/show で出したメッセージである。
	// message と違えば、メッセージの行のものは本体が出したものである（showExtMessage が本体の誤りを上書きしないために使う）。
	extMessage ui.Message
	// offscreenNote は、メッセージの行に出した、画面に出さないバッファを閉じた知らせである（offscreen.go）。
	offscreenNote offscreenNote
	// float は、出しているポップアップである（popup.go）。nil なら出していない。
	float *floatState
	// panes は、この client を元の client にして、拡張機能が pane/open で端末多重化ソフトのペインに開いた部品を、開いた順に並べたものである
	// （component.go。FR-115）。
	panes []*floatState
	// popupMark は、最後に拡張機能の操作（command/execute と provider/invoke）を問い合わせた State と、
	// その時点の command.State.KeysFed の値である（invokeResult）。showPopup が、問い合わせの後に次のキーを解釈したかを調べる。
	popupMark popupMark
	// regList は、:registers で出したレジスターの一覧の窓である（reglist.go）。nil なら出していない。
	regList *regListState
	// outView は、開いている出力の欄である。nil なら閉じている。
	outView *outputView
	// inc は、/ か ? でパターンを打っている間の、打つたびの検索の状態である（incsearch.go。FR-153）。打っていなければ nil。
	inc *incSearch
	// wild は、コマンドラインの補完の状態である（cmdcomplete.go。FR-195）。補っていなければ nil。
	wild *wildMenu
	// quit は、client の最後のタブを閉じたので、この client を終えることを表す（reapClients が外す）。
	quit bool
}

// clientView は、繋いでいる編集の client 1 つである。
type clientView struct {
	info ClientInfo
	// v は、この client がイベントループに読み込まれていない間の viewFields である。読み込まれている間は Editor の欄が正である。
	v viewFields
	// tabs は、この client のタブの並びである（FR-62）。各バッファの owner はこの client である。
	tabs []*bufferState
	// size は、端末から届いた最新の大きさ（[2]int）である。入力を読む goroutine が置き、イベントループが読む。
	size atomic.Value
	// intr は、この client の <C-c> を受けて、まだ処理していないことを表す。外部のコマンドの中断と、先打ちのキーを捨てるのに使う。
	intr atomic.Bool
	// inputClosed は、端末の入力が閉じたことを表す。残ったキーを処理し終えたら、reapClients が外す。
	inputClosed bool
	// detached は、Editor から外したことを表す。
	detached bool
	// keyTimer と keyGen は、曖昧なキーの列を確定させるタイマーと、その世代である（armKeyTimer）。
	keyTimer *time.Timer
	keyGen   uint64
	// holdStop と holdGen は、カーソルが止まった契機のタイマーを止める関数と、その世代である（cursorhold.go の armHold）。
	// 掛けていなければ holdStop は nil である。
	holdStop func() bool
	holdGen  uint64
	// focusAway は、端末多重化ソフトのフォーカスがこの client のペインから離れた（フォーカスを入れたペインの部品を開いた、
	// 別の client のペインへ移した）ことを表す（cursorhold.go の leaveFocus）。この client の端末で次のキーを受けたら下ろす（handleKeys）。
	// 立っている間は、カーソルが止まった契機を知らせない（holdReady）。
	focusAway bool
	// replay は、この client のマクロかドットリピートの再生が待っている拡張機能の問い合わせである。無ければ nil（replaywait.go）。
	replay *replayWait
	// completionJob は、この client の補完の候補をイベントループの外で集めている仕事である。無ければ nil（completion.go）。
	// ある間は、この client のキーを処理しない（keyAllowed）。
	completionJob *completionJob
	// mouseDown は、この client の端末でマウスのボタンを最後に押した位置の部品の種類である。ドラッグの位置をその部品へ寄せる（mouse.go）。
	mouseDown ui.HitArea
	// mouse は、この client（とその部品の client）が最後に受けたマウスの出来事の位置である（mouse.go）。
	mouse mouseEvent
}

// attached は、c が Editor に繋がっているかを返す。
func (c *clientView) attached() bool { return !c.detached }

// keyFire は、client c のタイマーの時間切れである。gen が c.keyGen と違えば、止めたタイマーのものなので捨てる。
type keyFire struct {
	c   *clientView
	gen uint64
}

// attachRequest は、Attach がイベントループへ渡す新しい client である。
type attachRequest struct {
	term Terminal
	info ClientInfo
	done chan error
}

// ErrStopped は、Run が終わった後に Attach を呼んだことを表す。
var ErrStopped = errors.New("editor: event loop stopped")

// newClientView は、端末 term の client を作る。キーマップの展開の部品 remap が nil なら作る。
func (e *Editor) newClientView(term Terminal, remap *remapper) *clientView {
	if remap == nil {
		// 設定の誤りは最初の client を作るときに知らせたので、ここでは捨てる。
		remap, _ = newRemapper(e.cfg.Keymap, e.leader)
	}
	c := &clientView{}
	c.v.term, c.v.renderer, c.v.remap = term, e.newRenderer(), remap
	return c
}

// SetClientInfo は、最初の編集の client（New に渡した端末）の情報を設定する。Run の前に、Run と同じ goroutine から呼ぶ。
// info.Files と info.Stdin は読まない（開くのは呼び出し側が OpenWith と OpenStdin で行う）。
func (e *Editor) SetClientInfo(info ClientInfo) {
	info.Stdin = nil
	e.clients[0].info = info
}

// Attach は、2 つ目からの編集の client を繋ぐ（FR-58、FR-62）。Run を動かしている間に、他の goroutine から呼ぶ。
// イベントループが、info.Files をその client のタブとして開き（無ければ名前の無い空のタブを作り）、term の入力を読み始め、
// 描き終えてから戻る。ファイルを開けなければ client を繋がずに、その誤りを返す。
// Run が終わっていれば ErrStopped を返す。term は、Run が戻るか、その client が終わるまで使う。
func (e *Editor) Attach(term Terminal, info ClientInfo) error {
	req := attachRequest{term: term, info: info, done: make(chan error, 1)}
	select {
	case e.attachCh <- req:
	case <-e.finished:
		return ErrStopped
	}
	select {
	case err := <-req.done:
		return err
	case <-e.finished:
		return ErrStopped
	}
}

// attach は、イベントループで、Attach の client を繋ぐ。
func (e *Editor) attach(req attachRequest) {
	c := e.newClientView(req.term, nil)
	c.info = req.info
	c.info.Stdin = nil // 中身はバッファが持つ。client の情報には残さない
	token := takeSplitToken(&c.info)
	e.clients = append(e.clients, c)
	prev := e.op
	e.useClient(c)
	if err := e.openClientFiles(req.info.Stdin, req.info.Files, req.info.ReadOnly); err != nil {
		// 開けたタブも閉じて、client を繋がずに戻す。
		for _, bs := range slices.Clone(c.tabs) {
			e.closeBufferState(bs)
		}
		e.removeClient(c)
		if prev != nil && prev.attached() {
			e.useClient(prev)
		}
		req.done <- err
		return
	}
	e.diffAdopt(token)
	e.startClient(c)
	e.diffAttached(c, token)
	req.done <- nil
}

// openClientFiles は、client の起動の引数のファイルを、引数の順にタブとして開き、最初のファイルのタブへ移る（Server.OpenArgs と同じ順）。
// 他の client のタブになっているファイルは、開かずに飛ばす（FR-62 のタブとバッファの 1 対 1 を保つため。フォーカスは移さない）。
// stdin が nil でなければ、その中身の名前の無いバッファ（FR-158）を、files より前のタブとして開く。
func (e *Editor) openClientFiles(stdin *string, files []string, readOnly bool) error {
	var firstTab *bufferState
	if stdin != nil {
		// req: FR-158
		firstTab = e.newStdinBuffer(*stdin, readOnly)
		e.enter(firstTab)
	}
	for _, f := range files {
		abs := e.absPath(f)
		if o := e.findPath(abs); o != nil && o.owner != nil && o.owner != e.view {
			e.message = infoMessage(fmt.Sprintf("%s は client %d のタブなので、このタブには開きません", e.bufferName(o), o.owner.info.ID))
			continue
		}
		bs, err := e.openFile(f, true)
		if err != nil {
			return fmt.Errorf("%s を開けない: %w", f, err)
		}
		if readOnly {
			bs.buf.SetReadOnly(true)
		}
		if firstTab == nil {
			firstTab = bs
		}
	}
	if firstTab != nil && e.cur != firstTab {
		e.switchTo(firstTab)
	}
	e.jumps, e.jumpIdx, e.alt = nil, 0, nil
	return nil
}

// startClient は、client c の最初のタブ（無ければ名前の無い空のタブ）を用意し、端末の大きさを読み、入力を読み始める。
func (e *Editor) startClient(c *clientView) {
	prev := e.view
	e.switchView(c)
	if e.cur == nil {
		e.enter(e.newUnnamedBuffer(""))
	}
	w, h := e.term.Size()
	e.setSize(w, h)
	term := e.term
	e.switchView(prev)
	go e.forwardInput(c, term, nil)
}

// forwardInput は、client c の端末 term の入力を読み、キーを列に積む。<C-c> を見たら中断の印を立てる。
// 入力が閉じたら、c を goneCh でイベントループへ送る。
// comp が nil でなければ、term は c を元の client にした部品の client の端末である（component.go）。
// キーは c が受けたものとして積み（基本設計 1.2）、大きさの変化は comp の画面の大きさにし、入力が閉じたら componentGone を呼ばせる。
func (e *Editor) forwardInput(c *clientView, term Terminal, comp *component) {
	defer func() {
		if comp != nil {
			select {
			case e.inspect <- func() { e.componentGone(c, comp) }:
			case <-e.stop:
			}
			return
		}
		select {
		case e.goneCh <- c:
		case <-e.stop:
		}
	}()
	defer e.recoverPanic()
	for ev := range term.Input() {
		if comp != nil && ev.Resize {
			comp.size.Store([2]int{ev.Width, ev.Height})
			signal(e.resizeCh)
			continue
		}
		if comp != nil && ev.Terminal != nil {
			continue // 部品の client には端末の受け渡しを頼まない
		}
		if ev.Terminal != nil {
			select {
			case e.termCh <- termResult{c: c, r: *ev.Terminal}:
			case <-e.stop:
				return
			}
			continue
		}
		if ev.Resize {
			c.size.Store([2]int{ev.Width, ev.Height})
			signal(e.resizeCh)
			continue
		}
		if ev.Paste != "" {
			e.keys.push(keyInput{c: c, paste: ev.Paste, comp: comp})
			e.forwarded.Add(1)
			continue
		}
		k := ev.Key.Normalize()
		if k == interruptKey {
			// 印と <C-c> のキーは、列の錠の中で一緒に置く。印を先に見た afterKey の dropThrough が、
			// まだ積んでいない <C-c> を捨て損ね、後でそれを <Esc> として処理して「中断しました」を消すのを防ぐ（#202）。
			e.keys.pushInterrupt(keyInput{c: c, key: k, comp: comp}, &e.interrupt, &c.intr)
			signal(e.intrCh)
			e.forwarded.Add(1)
			continue
		}
		e.keys.push(keyInput{c: c, key: k, comp: comp, mouseRow: ev.MouseRow, mouseCol: ev.MouseCol})
		e.forwarded.Add(1)
	}
}

func (e *Editor) setSize(w, h int) {
	e.renderer.Width, e.renderer.Height = max(w, 0), max(h, 0)
}

// applySize は、各 client の端末と、各 client の部品の client の端末から届いた最新の大きさを反映する。
func (e *Editor) applySize() {
	e.eachView(func(c *clientView) {
		if v, ok := c.size.Load().([2]int); ok {
			e.setSize(v[0], v[1])
		}
		e.applyComponentSizes()
	})
}

// switchView は、イベントループの扱う画面の状態を client c のものにする。c が nil か、既に読み込んでいれば何もしない。
// レジスターなどの共有する状態は引き継がない（キーを処理するときは useClient を使う）。
func (e *Editor) switchView(c *clientView) {
	if c == nil || c == e.view {
		return
	}
	if e.view != nil && e.view.attached() {
		e.view.v = e.viewFields
	}
	e.viewFields = c.v
	e.view = c
}

// useClient は、client c のキーを処理する前に、c を読み込んで操作中の client にし、共有する状態を c のカーソルのあるタブの State へ引き継ぐ。
func (e *Editor) useClient(c *clientView) {
	e.switchView(c)
	e.op, e.focused = c, c
	if e.cur != nil {
		if e.shared != nil && e.shared != e.cur.state {
			e.cur.state.TakeOverShared(e.shared)
		}
		e.shared = e.cur.state
	}
}

// eachView は、繋いでいるすべての client を順に読み込んで f を呼び、最後に元の client へ戻す。
func (e *Editor) eachView(f func(c *clientView)) {
	prev := e.view
	for _, c := range slices.Clone(e.clients) {
		if c.detached {
			continue
		}
		e.switchView(c)
		f(c)
	}
	if prev != nil && prev.attached() {
		e.switchView(prev)
	} else if e.op != nil && e.op.attached() {
		e.switchView(e.op)
	}
}

// viewOf は、client c の viewFields を指すポインターを返す。c が読み込まれていれば Editor の欄を指す。
func (e *Editor) viewOf(c *clientView) *viewFields {
	if c == e.view {
		return &e.viewFields
	}
	return &c.v
}

// reapClients は、終わる client を外す。:q などで最後のタブを閉じた client と、端末の入力が閉じて残りのキーを処理し終えた client である。
func (e *Editor) reapClients() {
	defer e.reapWaiters()
	for _, c := range slices.Clone(e.clients) {
		switch {
		case e.viewOf(c).quit:
			e.endErr = nil
			e.detachClient(c, "")
		case c.inputClosed && (!e.keys.has(c) || (e.shell != nil && e.shell.client == c)):
			e.endErr = ErrTerminalLost
			e.loseClient(c)
		}
	}
}

// ClientExiter は、Run の途中で終わる client の端末が実装してよい口である。
// client が :q で最後のタブを閉じたときに、Editor は Exit を呼ぶ（server は、終わりの知らせを送って接続を閉じる）。
// 最後の client が終わったときは呼ばず、Run が戻る（呼び出し側が端末を戻す）。
type ClientExiter interface {
	Exit(reason string)
}

// detachClient は、client c を外す。最後の client でなければ、c の端末に終わりを知らせる（ClientExiter）。
func (e *Editor) detachClient(c *clientView, reason string) {
	term := e.viewOf(c).term
	e.removeClient(c)
	if len(e.clients) > 0 {
		if x, ok := term.(ClientExiter); ok {
			x.Exit(reason)
		}
	}
}

// loseClient は、端末の入力が閉じた client c を外す。c のタブのうち、未保存の変更の無いバッファは閉じ、
// 未保存の変更のあるバッファは、どのタブにもならないバッファとして残して、他の client に知らせる（仮。パッケージの doc）。
func (e *Editor) loseClient(c *clientView) {
	var kept []string
	for _, bs := range slices.Clone(c.tabs) {
		if bs.buf.Modified() {
			c.tabs = slices.DeleteFunc(c.tabs, func(o *bufferState) bool { return o == bs })
			bs.owner = nil
			kept = append(kept, e.bufferName(bs))
			continue
		}
		e.closeBufferState(bs)
	}
	e.removeClient(c)
	if len(kept) > 0 {
		e.notifyAll(fmt.Sprintf("client %d が終わりました。未保存のバッファを残しました（:ls で確かめ、:b で開けます）: %s", c.info.ID, strings.Join(kept, "、")))
	}
	if x, ok := e.lostTerm(c).(ClientExiter); ok && len(e.clients) > 0 {
		// server が接続の後始末（送り待ちの置き場と一覧から外すこと）をできるように知らせる。相手はもう読んでいない。
		x.Exit("")
	}
}

// lostTerm は、外した client c の端末を返す。
func (e *Editor) lostTerm(c *clientView) Terminal { return c.v.term }

// notifyAll は、繋いでいるすべての client のメッセージの行に、警告 text を出す。
// 同じ処理の中で既に警告を出していれば、その後に続ける（client が終わったときの、問いの取り下げと残したバッファの知らせ）。
func (e *Editor) notifyAll(text string) {
	for _, c := range e.clients {
		v := e.viewOf(c)
		if v.message.Level == ui.MessageWarning && v.message.Text != "" && !strings.Contains(v.message.Text, text) {
			v.message = ui.Message{Text: v.message.Text + "。" + text, Level: ui.MessageWarning}
			continue
		}
		v.message = ui.Message{Text: text, Level: ui.MessageWarning}
	}
}

// cancelQuestions は、出している問いをすべて取り下げる。確認の問いは n、本体の問いは <Esc>（書き出さない、読み直さない）、
// 拡張機能の選択肢の問いは取り消し（Cancelled）で答えたものとする。
func (e *Editor) cancelQuestions() {
	if cf, bs := e.confirm, e.confirmBS; cf != nil {
		e.confirm, e.confirmBS = nil, nil
		if bs != nil && slices.Contains(e.bufs, bs) {
			_ = e.handleResult(bs, bs.state.Confirm(cf, false)) // 失敗は handleResult が知らせる
		}
	}
	if p := e.prompt; p != nil {
		e.prompt = nil
		p.answer(editcmd.Esc)
	}
	for _, ch := range e.choices {
		if !closed(ch) {
			ch.cancel() // 拡張機能が先に取り下げていれば届かない
		}
	}
	e.choices = nil
	e.endChoices()
}

// removeClient は、client c を一覧から外し、他の client を読み込む。c のタイマーを止める。
func (e *Editor) removeClient(c *clientView) {
	if v := e.viewOf(c); v.float != nil || len(v.panes) > 0 {
		// ポップアップを出す画面が無くなるので閉じ、拡張機能のポップアップなら持ち主へ知らせる。
		// この client を元の client にしたペインの部品も閉じる（部品の client を終わらせる。FR-61）。
		e.withView(c, func() {
			e.closeFloat(true)
			for _, p := range slices.Clone(e.panes) {
				e.closePane(p, true)
			}
		})
	}
	if c.keyTimer != nil {
		c.keyTimer.Stop()
	}
	c.keyGen++
	e.stopHold(c)
	// 窓の帯を消す。閉じた窓の知らせは、次の描画の後に送る（window.go の flushWindows）。
	delete(e.bands, windowIDOf(c))
	if c == e.view {
		c.v = e.viewFields
	}
	// 問いを出した client が終わったら、問いを他の client へ移さずに取り下げる（PR #223 のレビューの必須 2）。
	// 他の client の利用者は、問いがどのファイルのものかを知らないので、その答えで見ていないファイルを書き出さないためである。
	asked := e.asker == c || (e.asker == nil && e.op == c)
	c.detached = true
	e.clients = slices.DeleteFunc(e.clients, func(o *clientView) bool { return o == c })
	cancelled := asked && (e.asking() || e.activeChoice() != nil)
	if cancelled {
		e.cancelQuestions()
		// 問いの間に待たせていた他の client のキーの処理を再開する（PR #223 の再レビューの必須）。
		// 問いに答えて終わるときは、答えのキーを取り出した popFor が、列に残りがあれば知らせ直す。
		// 取り下げはキーを取り出さずに起きるので、ここで知らせ直さないと、その client が次のキーを打つまで止まる。
		if !e.keys.empty() {
			signal(e.keys.ready)
		}
	}
	defer func() {
		if cancelled {
			e.notifyAll(fmt.Sprintf("client %d が終わったので、その client の問いを取り下げました", c.info.ID))
		}
	}()
	if e.asker == c {
		e.asker = nil
	}
	for owner, o := range e.extClient {
		if o == c {
			delete(e.extClient, owner)
		}
	}
	if e.shell != nil && e.shell.client == c {
		// 外部のコマンドを打った client が終わった。出力を入れる先の画面が無いので止める（結果は shellFinished が捨てる）。
		// 端末を渡した実行（:! と権限を上げた保存）は、その client の端末で動くので、結果はもう届かない。忘れる。
		if e.shell.terminal {
			e.dropTerminalJob()
		} else {
			e.shell.cancel()
		}
	}
	if c.replay != nil {
		// 再生の続きを入れる先の画面が無いので、問い合わせを取り消す（結果は replayAnswered が捨てる）。
		c.replay.q.Cancel()
	}
	e.keys.drop(c)
	if len(e.clients) == 0 {
		return
	}
	if e.op == c {
		e.op = nil
	}
	if e.focused == c {
		e.focused = nil
	}
	if e.view == c {
		e.view = nil
		next := e.op
		if next == nil {
			next = e.clients[len(e.clients)-1]
		}
		e.viewFields = next.v
		e.view = next
	}
	if e.op == nil {
		e.useClient(e.view)
	}
}

// ownerName は、bs をタブに持つ client を、メッセージに出す形で返す。
func ownerName(bs *bufferState) string {
	return fmt.Sprintf("client %d", bs.owner.info.ID)
}

// workDir は、読み込んでいる client（キーを処理する間は操作中の client）の作業ディレクトリを返す。知らせていなければ Config.WorkDir である。
func (e *Editor) workDir() string {
	if c := e.view; c != nil && c.info.WorkDir != "" {
		return c.info.WorkDir
	}
	return e.cfg.WorkDir
}

// childEnv は、読み込んでいる client の外部のコマンドに足す環境変数を返す（ClientInfo.Env の後に Config.ChildEnv）。
func (e *Editor) childEnv() []string {
	var env []string
	if c := e.view; c != nil {
		env = append(env, c.info.Env...)
	}
	return append(env, e.cfg.ChildEnv...)
}

// syncAsker は、問い（確認の問いと選択肢の問い）を出している client を決め直す。
// 問いがあり、まだ決めていなければ操作中の client にし、問いが無ければ nil にする。
func (e *Editor) syncAsker() {
	if !e.asking() && e.activeChoice() == nil {
		e.asker = nil
		return
	}
	if e.asker == nil || e.asker.detached {
		e.asker = e.op
	}
}

// showsQuestion は、読み込んでいる client の画面に、問いを出すかを返す。
func (e *Editor) showsQuestion() bool {
	q := e.asker
	if q == nil {
		q = e.op
	}
	return e.view == q
}

// keyAllowed は、client c のキーを今処理してよいかを返す。
// 問いの間は問いを出した client のキーだけを、外部のコマンドの実行中はそれを打った client 以外のキーを処理する（パッケージの doc）。
// ただし、外部のコマンドの子孫が起動した待つだけの client のタブをその client に開いている間（reclaimed。FR-117）は、その client のキーも処理する。
// 再生中の拡張機能の結果を待っている client のキーも、続け終えるまで処理しない（replayWait）。
// 補完の候補を集めている client のキーも、集め終えて補うまで処理しない（completionJob。FR-199）。
func (e *Editor) keyAllowed(c *clientView) bool {
	if c.detached {
		return true // 外した client のキーは、取り出して捨てる
	}
	if q := e.asker; q != nil && q != c {
		return false
	}
	if g := e.askGap; g != nil && !g.c.detached {
		// 拡張機能の問いに答えた直後は、次の問いが届くまでどの client のキーも取っておく（inputask.go の startAskGap。#420）。
		return false
	}
	return (e.shell == nil || e.shell.client != c || e.shell.reclaimed) && c.replay == nil && c.completionJob == nil
}
