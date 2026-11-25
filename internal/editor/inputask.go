package editor

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/target"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 拡張機能の問いの列（Editor.choices）と、1 行の入力を聞く問い（input/ask。基本設計 1.4 の「1 行の入力を聞くリクエスト」。FR-275）。
//
// choice/ask と input/ask は 1 本の列に届いた順に並べ、先頭の 1 つだけを、問いを出した client（asker）に出す。
// 問いを頼んだキーがペインの部品の client から来たなら、そのペインの画面に出す（questionPane）。
// 問いの間は、問いを出した client のキーと貼り付けをすべて問いへの答えにし、キーマップを通さず、command.State へも渡さない。
// このため答えのキーは、マクロにもドットリピートにも記録されない（choice/ask と同じ）。
// 他の client のキーは、確認の問いと同じく答えるまで待たせる（keyAllowed）。
// 問いを出した拡張機能が再起動するか問いを取り消したら、その問いの Done が閉じるので、列から外して取り下げる（activeChoice）。
// 取り下げたことはメッセージの行に知らせ、問いに向けて打ち続けたキーを編集のコマンドにしない（noteWithdrawn）。
// 利用者の応答を待つので、キー入力ごとの問い合わせの期限（FR-17）は当てない（応答の期限は exthost が持たない）。

// extQuestion は、拡張機能の問いの列の 1 件である。choice と input のどちらか一方だけを持つ。
type extQuestion struct {
	choice *exthost.ChoiceAskRequest
	input  *exthost.InputAskRequest
	// text は、input/ask の入力の今の中身である。初めは params の default である。
	text string
	// cursor は、input/ask の入力のカーソルのバイトの位置である（0 から len(text)）。初めは中身の末尾である。
	cursor int
	// pane は、問いを頼んだキーが来たペインの部品である。nil なら問いを出した client の画面に出す（questionPane）。
	pane *floatState
	// typed は、利用者がこの入力の問いにキーか貼り付けを 1 つでも打ったかである。取り下げの後のキーを捨てるかを決めるのに使う
	// （noteWithdrawn）。
	typed bool
}

// Done は、問いのリクエストが取り消されたか、拡張機能の接続が閉じたか、応答した後に閉じるチャネルを返す（request）。
func (q *extQuestion) Done() <-chan struct{} {
	if q.input != nil {
		return q.input.Done()
	}
	return q.choice.Done()
}

// ReplyError は、拡張機能へ失敗の応答を返す（request）。
func (q *extQuestion) ReplyError(err error) error {
	if q.input != nil {
		return q.input.ReplyError(err)
	}
	return q.choice.ReplyError(err)
}

// cancel は、問いを取り消したことを拡張機能へ返す。拡張機能が先に取り下げていれば届かない。
func (q *extQuestion) cancel() {
	if q.input != nil {
		_ = q.input.Reply(ext.InputAskResult{Cancelled: true})
		return
	}
	_ = q.choice.Reply(ext.ChoiceAskResult{Cancelled: true})
}

// title は、問いの文である。
func (q *extQuestion) title() string {
	if q.input != nil {
		return q.input.Params.Title
	}
	return q.choice.Params.Title
}

// enqueueQuestion は、拡張機能 extID から届いた問い q を列の末尾に足す。
// 問いを出していなければ、問いはその拡張機能へ問い合わせを始めた client に出す（基本設計 1.2。extView）。
// その問い合わせがペインの部品の client のキーから始まったなら、そのペインを覚えておき、ペインの画面に出す（questionPane）。
func (e *Editor) enqueueQuestion(extID string, q *extQuestion) {
	if e.asker == nil && !e.asking() && e.activeChoice() == nil {
		e.asker = e.extView(extID)
	}
	q.cursor = len(q.text)
	q.pane = e.extPane[extID]
	e.withdrawn = nil
	e.choices = append(e.choices, q)
	go e.watchQuestion(q)
}

// watchQuestion は、問い q の Done が閉じたら（答えたか、拡張機能が取り下げたか、接続が閉じた）、イベントループを起こす。
// 起こされたイベントループは activeChoice で取り下げに気づき、知らせてから描き直す。キーを待たずに問いを画面から消すためである
// （PR #411 の再レビューの推奨 2）。答えた問いで起こしても、activeChoice は何もしない。
func (e *Editor) watchQuestion(q *extQuestion) {
	defer e.recoverPanic()
	select {
	case <-q.Done():
		select {
		case e.questionGone <- struct{}{}:
		default: // 既に知らせてある
		}
	case <-e.finished:
	}
}

// notePane は、拡張機能 extID へのいまの問い合わせか知らせが、ペインの部品 p（nil なら、ペインの部品の client のキーでない）から
// 始まったことを覚える。端末多重化ソフトのペインに描く部品（remote）でなければ覚えない（その部品は元の client の画面の中にあるため）。
func (e *Editor) notePane(extID string, p *floatState) {
	if p != nil && p.pane && p.remote != nil {
		e.extPane[extID] = p
		return
	}
	delete(e.extPane, extID)
}

// questionPane は、表示中の問いを出すペインの部品を返す。問いを頼んだペインが無いか、閉じたか、その部品の client が終わっていれば、
// nil（問いを出した client の画面に出す）を返す。イベントループの goroutine で呼ぶ。
// オーナーの nvim の vim.fn.input の問いが、利用者がいまいる画面（窓）の下に出るのに合わせた（PR #411 のレビューの裁定要 1）。
func (e *Editor) questionPane() *floatState {
	q := e.activeChoice()
	if q == nil || q.pane == nil || q.pane.remote == nil {
		return nil
	}
	c := e.asker
	if c == nil {
		c = e.op
	}
	if c == nil || !slices.Contains(e.viewOf(c).panes, q.pane) {
		return nil
	}
	return q.pane
}

// withdrawnQuestion は、拡張機能が取り下げた問いと、その後に捨てるキーの扱いである（noteWithdrawn）。
type withdrawnQuestion struct {
	// c は、問いを出していた client である。この client のキーだけを捨てる。
	c *clientView
	// input は、取り下げた問いが 1 行の入力の問いかである。真なら <Esc> か <CR> までのキーを、偽（選択肢の問い）なら
	// 答えになるキー（番号と <Esc>）を 1 つだけ捨てる。
	input bool
	// until は、キーを捨てる期限である。取り下げの時点から withdrawnDiscardWindow の後で、それより後に処理するキーは捨てない。
	until time.Time
	// notice は、取り下げの知らせである。キーを捨てるたびにメッセージの行に出し直す（拡張機能の異常終了の知らせなど、
	// 後から届いたメッセージに上書きされていても、キーを捨てている理由が見えるようにするため）。
	notice string
}

// withdrawnDiscardWindow は、拡張機能が問いを取り下げた後に、問いに向けて打ち続けたキーとみなして捨てる間である（仮）。
// 打ち続けている途中の取り下げだけを守り、取り下げから時間が経った後のふつうの操作（dd、@a）は捨てない
// （PR #411 の再レビューの推奨 1 へのオーケストレータの決め）。
const withdrawnDiscardWindow = time.Second

// noteWithdrawn は、表示中の問い q を拡張機能が取り下げた（Done が閉じた）ことを知らせる（activeChoice から呼ぶ）。
// 待たせている問いが無く、入力の問いなら利用者がそこへ何かを打っていれば（選択肢の問いならいつでも）、問いを出していた client が
// 取り下げから withdrawnDiscardWindow の間に打つキーを捨てる（discardAfterWithdrawn）。
// 入力の問いに式やパスを続けて打っている途中で取り下げられると、残りの文字がノーマルモードのコマンドになってバッファを
// 書き換えるためである（PR #411 のレビューの必須 1）。
//
// req: FR-275
func (e *Editor) noteWithdrawn(q *extQuestion) {
	title := strings.TrimSpace(q.title())
	if len(e.choices) > 0 || (q.input != nil && !q.typed) {
		e.message = warnMessage(fmt.Sprintf("問い「%s」は拡張機能が取り下げました", title))
		return
	}
	c := e.asker
	if c == nil {
		c = e.op
	}
	what := "直後に打った番号か <Esc> は捨てます"
	if q.input != nil {
		what = "直後に打ったキーは、<Esc> か <CR> まで捨てます"
	}
	notice := fmt.Sprintf("問い「%s」は拡張機能が取り下げました（%s）", title, what)
	window := withdrawnDiscardWindow
	if e.withdrawWindow > 0 {
		window = e.withdrawWindow
	}
	e.withdrawn = &withdrawnQuestion{c: c, input: q.input != nil, until: time.Now().Add(window), notice: notice}
	e.message = warnMessage(notice)
}

// discardAfterWithdrawn は、取り下げた問いに向けて打ったキー k を捨てるかを決め、捨てるなら true を返す（handleKey から呼ぶ）。
// 問いを出していた client の、取り下げから withdrawnDiscardWindow の間に処理する、マウスでないキーだけを捨てる。
// 入力の問いなら <Esc> か <CR> まで（そのキーも含めて）、選択肢の問いなら答えになるキー（1〜9 と <Esc>）を 1 つだけ捨てる。
// ほかのキーが来たら、捨てるのをやめてそのキーを処理する。
func (e *Editor) discardAfterWithdrawn(k editcmd.Key) bool {
	w := e.withdrawn
	if w == nil {
		return false
	}
	if w.c != e.op || k.IsMouse() || time.Now().After(w.until) {
		e.withdrawn = nil
		return false
	}
	if !w.input {
		e.withdrawn = nil
		return k == editcmd.Esc || (k.Plain() && k.Rune >= '1' && k.Rune <= '9')
	}
	if k == editcmd.Esc || k == editcmd.Special(editcmd.CodeEnter) {
		e.withdrawn = nil
		e.message = infoMessage("取り下げた問いに向けて打ったキーを捨てました")
		return true
	}
	e.message = warnMessage(w.notice)
	return true
}

// inputKey は、表示中の 1 行の入力の問いへのキーを処理する（FR-275）。
//
// 文字はカーソルの位置に入れ、<Tab> はタブ文字として入れる（補完はしない）。ここまでと <BS> は : のコマンドラインと同じである。
// <BS> と <Del> は、カーソルの前と後の 1 文字を、結合文字ごと消す（Vim 9.1 のコマンドラインの既定と同じ）。
// <C-w> はカーソルの前の単語を、<C-u> はカーソルの前のすべてを消す（: のコマンドラインには無く、インサートモードのものに合わせた）。
// <C-w> の単語の区切りは、インサートモードの <C-w>（insert.delete-prev-word）と同じく、ノーマルモードの b と同じ区切りにする。
// <Left>、<Right> で 1 文字ずつ、<Home>、<End> で中身の先頭と末尾へカーソルを動かす（既定値の途中を直すため。PR #411 の裁定要 2）。
// 中身が空でも <BS> で取り消さない（: と違う。空の答えを返せるようにするため。基本設計 1.4）。
// <CR> で中身を確定し、<Esc> と <C-c>（handleKey が <Esc> に替える）で取り消す。
// ほかの修飾キー付きのキーと特殊キーは、何もしない（捨てた数は出さない）。
//
// req: FR-275
func (e *Editor) inputKey(k editcmd.Key) {
	q := e.choices[0]
	q.typed = true
	switch {
	case k == editcmd.Esc:
		_ = q.input.Reply(ext.InputAskResult{Cancelled: true})
		e.answered(e.questionName() + "を取り消しました")
	case k == editcmd.Special(editcmd.CodeEnter):
		_ = q.input.Reply(ext.InputAskResult{Value: q.text})
		if q.text == "" {
			e.answered(e.questionName() + "に空の文字列で答えました")
		} else {
			e.answered(fmt.Sprintf("%sに「%s」と答えました", e.questionName(), q.text))
		}
	case k == editcmd.Special(editcmd.CodeBackspace):
		q.deleteTo(prevCharStart(q.text, q.cursor))
	case k == editcmd.Special(editcmd.CodeDelete):
		q.deleteTo(nextCharEnd(q.text, q.cursor))
	case k == editcmd.Key{Code: editcmd.CodeRune, Rune: 'w', Mod: editcmd.ModCtrl}:
		q.deleteTo(prevWordStart(q.text[:q.cursor]))
	case k == editcmd.Key{Code: editcmd.CodeRune, Rune: 'u', Mod: editcmd.ModCtrl}:
		q.deleteTo(0)
	case k == editcmd.Special(editcmd.CodeLeft):
		q.cursor = prevCharStart(q.text, q.cursor)
	case k == editcmd.Special(editcmd.CodeRight):
		q.cursor = nextCharEnd(q.text, q.cursor)
	case k == editcmd.Special(editcmd.CodeHome):
		q.cursor = 0
	case k == editcmd.Special(editcmd.CodeEnd):
		q.cursor = len(q.text)
	case k == editcmd.Special(editcmd.CodeTab):
		q.insert("\t")
	case k.Plain():
		q.insert(string(k.Rune))
	}
}

// insert は、入力のカーソルの位置に s を入れ、カーソルを s の後へ動かす。
func (q *extQuestion) insert(s string) {
	q.text = q.text[:q.cursor] + s + q.text[q.cursor:]
	q.cursor += len(s)
}

// deleteTo は、入力のカーソルと at の間を消し、カーソルを消した範囲の始まりに置く。
func (q *extQuestion) deleteTo(at int) {
	from, to := min(at, q.cursor), max(at, q.cursor)
	q.text = q.text[:from] + q.text[to:]
	q.cursor = from
}

// isComposing は、r が前の文字に重ねて描く結合文字かを返す（Vim の utf_iscomposing に当たる。一般カテゴリーの Mn、Mc、Me）。
func isComposing(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me)
}

// prevCharStart は、text の at の前の 1 文字（後に続く結合文字を含む）の始まりのバイトの位置を返す。先頭なら 0 を返す。
func prevCharStart(text string, at int) int {
	for at > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:at])
		at -= size
		if !isComposing(r) {
			break
		}
	}
	return at
}

// nextCharEnd は、text の at からの 1 文字（後に続く結合文字を含む）の終わりのバイトの位置を返す。末尾なら at のまま返す。
func nextCharEnd(text string, at int) int {
	if at >= len(text) {
		return at
	}
	_, size := utf8.DecodeRuneInString(text[at:])
	at += size
	for at < len(text) {
		r, size := utf8.DecodeRuneInString(text[at:])
		if !isComposing(r) {
			break
		}
		at += size
	}
	return at
}

// inputPaste は、表示中の 1 行の入力の問いへ、端末から貼り付けたテキストをカーソルの位置に入れる（FR-275）。
// 改行は空白にし、タブ文字の他の制御文字は捨てる（答えを 1 行に保つため。: の貼り付けと同じく制御文字は入れない）。
func (e *Editor) inputPaste(text string) {
	q := e.choices[0]
	q.typed = true
	var b strings.Builder
	for _, r := range strings.ReplaceAll(text, "\r\n", "\n") {
		switch {
		case r == '\n' || r == '\r':
			b.WriteByte(' ')
		case r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	q.insert(b.String())
}

// prevWordStart は、1 行の text の末尾にカーソルを置いて <C-w> を打ったときに消し始める位置（バイトの位置）を返す。
// インサートモードの <C-w>（command.State の prevWordStart）と同じく、ノーマルモードの b（word-prev）で戻る位置で、
// カーソルの前の空白を飛ばし、その前の単語の先頭まで戻る。
func prevWordStart(text string) int {
	if text == "" {
		return 0
	}
	f, ok := target.LookupMotion("word-prev")
	if !ok {
		return 0
	}
	// b はカーソルを文字の上に置くので、末尾に空白を 1 つ足してその上から戻る（空白は b が飛ばすので、戻る位置は変わらない）。
	c := buffer.Position{Byte: len(text)}
	res, err := f(buffer.New(text+" "), target.Request{Cursor: c, Count: 1})
	if err != nil || res.Pos.Line != 0 || res.Pos.Byte > len(text) {
		return 0
	}
	return res.Pos.Byte
}

// uiInput は、表示中の 1 行の入力の問いを描画の入力にする。何件目かと、前の問いへの答えと待たせている問いの数の知らせは、
// 選択肢の問い（uiChoice）と同じく添える（#151）。
func (e *Editor) uiInput() *ui.Input {
	q := e.choices[0]
	return &ui.Input{
		Prompt: q.input.Params.Title,
		Text:   q.text,
		Cursor: q.cursor,
		Index:  e.questionIndex(),
		Note:   strings.Join(e.questionNotes(), "\n"),
	}
}
