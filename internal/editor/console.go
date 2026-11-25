package editor

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/command/ex"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 書き足すテキストと入力の行の組（ext.ViewConsole。基本設計 1.4 の「書き足すテキストと入力の行の組」。FR-274）。
//
// 出力（consoleState.out）と入力の行（floatState.bs）を、それぞれ本体のバッファにする。出力は編集を禁止したファイルでないバッファで、
// view/append（viewappend.go）で書き足す。入力の行は、ファインダーの入力の行と同じく編集できる 1 行のバッファである。
// キーは、フォーカスのある方のバッファの State が受け、どちらの KeyHook も popupStateKey を通る（consoleKey、consoleOutputKey）。
//
// 入力の行の編集は、インサートモードの State のキー（文字、<BS>、<Del>、<Left>、<Right>、<Home>、<End>）と、
// input/ask の問いの入力（inputask.go）と同じ <C-w>、<C-u> で行う。<BS>、<Del>、<Left>、<Right> は、input/ask と同じく
// 結合文字を前の文字と一緒に 1 文字として扱い（prevCharStart、nextCharEnd）、<C-w> の区切りも input/ask と同じ prevWordStart である
// （consoleKey）。横のスクロールは、入力の行を描く子の Renderer が行う（ui.drawConsole）。
//
// 切り替えの部品（FR-269）の子にしたときは、子ごとの中身（floatContent）が consoleState と tailState を持ち、子を初めて出したときに
// 入力の行のインサートモードにし、切り替え直したときはフォーカスのある方（outFocus）とそのバッファのモードに戻す（switcher.go。FR-301）。
//
// 入力の行の改行は持たない。貼り付けと、行を増やす編集（行単位の p、o）で入った改行は、consoleInputText で空白に替える
// （input/ask の貼り付けと同じ。PR #415 のレビューの必須 1）。ACP のプロンプトで改行を保つ形（記号で見せて送るときに戻す、
// 複数行の欄にする）にするときは、consoleInputText と submitConsole の 2 か所を替える。

// consoleState は、書き足すテキストと入力の行の組の、入力の行（floatState.bs）のほかの状態である（floatState.con）。
// 切り替えの部品（FR-269）では子ごとに持つので、floatState に直に並べずに 1 つにまとめた。
type consoleState struct {
	// out は、出力のバッファである（編集を禁止したファイルでないバッファ）。書き足し（view/append）と末尾の追従（tailState）は、
	// テキストの部品の bs の代わりに out に当てる（floatState.textBuf）。
	out *bufferState
	// outFocus は、フォーカスを出力へ移している（キーを out が受ける）ことを表す。
	outFocus bool
	// prompt は、入力の行の前置きである。
	prompt string
	// history は、入力の履歴（古い順）である。histPos はたどっている位置で、len(history) ならたどっていない（打ちかけの中身）。
	history []string
	histPos int
	// histEdits は、たどっている間に直した中身を、履歴の位置ごとに覚えたものである。len(history) の位置は打ちかけの中身である。
	// 送るまで保ち、送ったら捨てる（シェルの履歴と同じ。PR #415 のレビューの推奨 3）。履歴そのものは書き換えない。
	histEdits map[int]string
}

// consoleInputName と consoleOutputName は、書き足すテキストと入力の行の組の入力の行と出力のバッファの名前（ステータス行に出る）である（仮）。
// 見出しは view/update で変わりうるので、ファインダーと同じく決まった名前にする。
const (
	consoleInputName  = "[入力]"
	consoleOutputName = "[出力]"
)

// newConsoleFloat は、書き足すテキストと入力の行の組 c を中身にした部品を作る（FR-274）。開くのは呼び出し側である。
// 出力のカーソルは最後の行に置き、末尾から見せて追従させる。入力の行のカーソルは中身の末尾に置く。
// 履歴は c.History の新しい方から ext.ConsoleHistoryMax 件を持つ。
//
// req: FR-274
func (e *Editor) newConsoleFloat(c ext.ViewComponent, extID string) *floatState {
	f := &floatState{floatContent: &floatContent{kind: floatConsole, input: c.Input}, ext: extID, title: c.Title, scratch: true}
	h := c.History
	if d := len(h) - ext.ConsoleHistoryMax; d > 0 {
		h = h[d:]
	}
	con := &consoleState{prompt: c.Prompt, history: slices.Clone(h)}
	con.histPos = len(con.history)
	f.con = con
	f.bs = e.newPopupBuffer(consoleInputName, c.Input, "", false)
	f.bs.state.SetCursor(buffer.Position{Byte: len(c.Input)})
	lines, _, decs := floatLines(c, nil, nil)
	con.out = e.newPopupBuffer(consoleOutputName, floatBufferText(lines), "", true)
	con.out.state.Viewport = consoleOutViewport{f: f, r: e.renderer, ct: f.floatContent}
	if len(decs) > 0 {
		e.setPopupDecorations(con.out, decs)
	}
	con.out.state.SetCursor(buffer.Position{Line: con.out.buf.LineCount() - 1})
	f.textEOL = len(c.Text) > 0 && c.Text[len(c.Text)-1] == '\n'
	return f
}

// setConsoleContent は、書き足すテキストと入力の行の組 f への view/update（部品 c）を当てる（FR-274）。
// 出力（Text、Highlights）と前置き（Prompt）だけを置き換え、入力の行の中身と履歴は読まない（利用者が打っているため）。
// 出力のカーソルは、テキストの部品と同じく、最後の行にあれば（c.Tail なら常に）新しい中身の最後の行へ置き、そうでなければ行の番号を保つ。
func (e *Editor) setConsoleContent(f *floatState, c ext.ViewComponent) {
	out := f.con.out
	cursor := out.state.Cursor()
	follow := cursor.Line >= out.buf.LineCount()-1 || c.Tail
	lines, _, decs := floatLines(c, nil, nil)
	e.reload(out, buffer.New(floatBufferText(lines)))
	n := out.buf.LineCount()
	line := min(cursor.Line, n-1)
	if follow {
		line = n - 1
	}
	out.state.SetCursor(buffer.Position{Line: line, Byte: min(cursor.Byte, len(out.buf.Line(line)))})
	f.textEOL = len(c.Text) > 0 && c.Text[len(c.Text)-1] == '\n'
	f.decsOwned, f.followHeld = false, false
	e.setPopupDecorations(out, decs)
	f.con.prompt = c.Prompt
}

// startConsoleInsert は、開いた書き足すテキストと入力の行の組 f の入力の行を、行末から打てるインサートモードにする（FR-274）。
// 組でない部品と、既にインサートモードの入力の行では何もしない。利用者のキーではないので、マクロに記録しない（command.State.FeedUnrecorded）。
// 分割したペインの部品は、元の client のキーを受けるバッファではないので、入力の行の State に直に渡す（モードを入れるだけで、
// バッファもレジスターも変えないので、結果を処理しなくてよい）。
func (e *Editor) startConsoleInsert(f *floatState) {
	if f.kind != floatConsole || f.bs.state.Mode() == command.ModeInsert {
		return
	}
	if e.cur == f.bs {
		_ = e.handleResult(f.bs, f.bs.state.FeedUnrecorded(editcmd.Rune('A'))) // 失敗は handleResult が知らせる
		return
	}
	f.bs.state.FeedUnrecorded(editcmd.Rune('A'))
}

// 書き足すテキストと入力の行の組の入力の行で、本体が受けるキー。
var (
	consoleEnter = editcmd.Special(editcmd.CodeEnter)
	consoleUp    = editcmd.Special(editcmd.CodeUp)
	consoleDown  = editcmd.Special(editcmd.CodeDown)
	consoleBS    = editcmd.Special(editcmd.CodeBackspace)
	consoleDel   = editcmd.Special(editcmd.CodeDelete)
	consoleLeft  = editcmd.Special(editcmd.CodeLeft)
	consoleRight = editcmd.Special(editcmd.CodeRight)
	consoleCtrlW = editcmd.Key{Code: editcmd.CodeRune, Rune: 'w', Mod: editcmd.ModCtrl}
	consoleCtrlU = editcmd.Key{Code: editcmd.CodeRune, Rune: 'u', Mod: editcmd.ModCtrl}
)

// consoleKey は、書き足すテキストと入力の行の組 f の入力の行で受けたキー k を、組の操作として扱えれば扱い、true を返す（FR-274）。
//
//   - <CR>（ノーマルモードとインサートモード）: 入力の行の中身を送る（submitConsole）
//   - インサートモードの <Up>、<Down>: 入力の履歴の前と後の中身を入力の行に入れる（consoleHistory。nvim-dap の REPL の既定のキー）
//   - インサートモードの <BS>、<Del>、<Left>、<Right>: 結合文字を前の文字と一緒に 1 文字として扱う（input/ask の問いの入力と同じ）。
//     State は 1 つのキーで 1 つの符号位置を消すか動くので、結合文字の分だけ同じキーを held で前に足し、State に続けて解釈させる。
//     held は KeyHook の取り決めで、k と一緒にマクロに記録したキーではないので、再生でも同じく足す
//   - インサートモードの <C-w>、<C-u>: カーソルの前の単語（ノーマルモードの b と同じ区切り）と、カーソルの前のすべてを消す。
//     本体のインサートモードには既定のキーの割り当てが無い（insert.delete-prev-word は keymap で割り当てる操作）ので、
//     input/ask の問いの入力（inputask.go の inputKey）と同じく、組の入力の行には既定で持たせる
//
// インサートモードの <Esc> は扱わない（State がノーマルモードへ戻す。閉じない）。
//
// req: FR-274
func (e *Editor) consoleKey(f *floatState, k editcmd.Key, insert bool) (bool, []editcmd.Key) {
	switch {
	case k == consoleEnter:
		e.submitConsole(f)
		return true, nil
	case !insert:
		return false, nil
	case k == consoleUp:
		e.consoleHistory(f, -1)
		return true, nil
	case k == consoleDown:
		e.consoleHistory(f, 1)
		return true, nil
	}
	text, at := f.bs.buf.Line(0), f.bs.state.Cursor().Byte
	at = min(at, len(text))
	switch k {
	case consoleCtrlW:
		e.deleteConsoleInput(f, prevWordStart(text[:at]), at)
		return true, nil
	case consoleCtrlU:
		e.deleteConsoleInput(f, 0, at)
		return true, nil
	}
	var n int
	switch k {
	case consoleBS, consoleLeft:
		n = utf8.RuneCountInString(text[prevCharStart(text, at):at])
	case consoleDel, consoleRight:
		n = utf8.RuneCountInString(text[at:nextCharEnd(text, at)])
	}
	if n <= 1 {
		return false, nil
	}
	return false, slices.Repeat([]editcmd.Key{k}, n-1)
}

// consoleOutputKey は、書き足すテキストと入力の行の組 f の出力（フォーカスを移したとき）で受けたキー k を扱い、扱ったら true を返す。
// ノーマルモードの contributes.popupKeys のキー（view/key の Item は入力の行の中身）と、ノーマルモードの <Esc>（中央のポップアップなら閉じ、
// 分割したペインでは閉じない）だけを扱い、ほかのキー（移動、検索、ヤンク、ビジュアルモード）は出力のバッファの State が解釈する（FR-71）。
func (e *Editor) consoleOutputKey(f *floatState, k editcmd.Key, insert bool) (bool, []editcmd.Key) {
	if insert {
		return false, nil
	}
	if f.ext != "" {
		if done, held := e.popupKey(f, k); done || len(held) > 0 {
			return done, held
		}
	}
	if k == editcmd.Esc && !f.pane {
		e.closeFloat(true)
		return true, nil
	}
	return false, nil
}

// submitConsole は、書き足すテキストと入力の行の組 f の入力の行の中身を送る（FR-274）。
// 中身が変わっていれば先に input/change で知らせてから、view/select（Item は中身。空でも送る）を送る。
// 空でない中身を履歴の末尾に積み（直前と同じなら積まない。ext.AppendConsoleHistory）、履歴をたどる位置を戻し、入力の行を空にする。
// モードは変えない（インサートモードなら続けて打てる）。
// 出力のカーソルを最後の行へ戻して末尾への追従を続ける。出力を上へ動かして読み返した後に送っても、送った結果（返事）が見えるように
// するためである（PR #415 のレビューの裁定要 2 へのオーケストレータの決め。FR-272 の、利用者が上へ動かしている間は追従しない規則の例外）。
// 改行を持たない入力の行の中身をそのまま送る（consoleInputText の取り決め。改行を保つ形にするときは、ここで戻す）。
//
// req: FR-274
func (e *Editor) submitConsole(f *floatState) {
	e.notifyInputChange(f)
	text := f.bs.buf.Line(0)
	e.notifyView(f, ext.MethodViewSelect, ext.ViewSelectParams{View: ext.ViewID(f.id), Item: text, Child: f.child})
	con := f.con
	con.history = ext.AppendConsoleHistory(con.history, text)
	con.histPos, con.histEdits = len(con.history), nil
	e.setConsoleInput(f, "")
	out := con.out
	last := out.buf.LineCount() - 1
	out.state.SetCursor(buffer.Position{Line: last, Byte: min(out.state.Cursor().Byte, len(out.buf.Line(last)))})
	f.followHeld = false
}

// consoleHistory は、書き足すテキストと入力の行の組 f の入力の行に、入力の履歴の d 件先（-1 は前、1 は後）の中身を入れる（FR-274）。
// 離れる位置の今の中身を覚えておき（打ちかけの中身と、履歴から出して直した中身。consoleState.histEdits）、その位置へ戻ると覚えた中身を出す。
// 送るまで覚え、履歴そのものは書き換えない（シェルの履歴と同じ）。最初より前と、たどっていないときの後へは進まない。
// カーソルは中身の末尾に置く。
//
// req: FR-274
func (e *Editor) consoleHistory(f *floatState, d int) {
	con := f.con
	n := len(con.history)
	pos := con.histPos + d
	if pos < 0 || pos > n || pos == con.histPos {
		return
	}
	cur := f.bs.buf.Line(0)
	if con.histPos >= n || cur != con.history[con.histPos] {
		if con.histEdits == nil {
			con.histEdits = map[int]string{}
		}
		con.histEdits[con.histPos] = cur
	} else {
		delete(con.histEdits, con.histPos)
	}
	con.histPos = pos
	text, ok := con.histEdits[pos]
	if !ok && pos < n {
		text = con.history[pos]
	}
	e.setConsoleInput(f, text)
}

// consoleInputText は、書き足すテキストと入力の行の組の入力の行へ入れるテキスト s を、入力の行の中身の形（1 行）にする。
// 改行（\r\n、\r、\n）は空白に替え、タブ文字の他の制御文字は捨てる（input/ask の貼り付けの inputPaste と同じ。PR #415 のレビューの必須 1）。
// 貼り付け（handlePaste）と、行を増やす編集の後（afterFloatKey）の両方がここを通る。入力の行の改行の扱いを替えるとき
// （ACP のプロンプトで改行を保つ）は、ここと送る側（submitConsole）を替える。
func consoleInputText(s string) string {
	var b strings.Builder
	for _, r := range strings.ReplaceAll(s, "\r\n", "\n") {
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
	return b.String()
}

// consolePaste は、キーを受けているバッファ bs が、読み込んでいる client のフォーカスのある組の入力の行なら、貼り付けるテキスト text を
// 入力の行の中身の形にして返す（consoleInputText）。ほかのバッファなら text のまま返す。handlePaste から呼ぶ。
func (e *Editor) consolePaste(bs *bufferState, text string) string {
	if f := e.float; f != nil && f.focused && f.con != nil && bs == f.bs {
		return consoleInputText(text)
	}
	return text
}

// joinConsoleInput は、行を増やす編集（行単位の p、o）で 2 行以上になった組 f の入力の行を、行の間を空白にした 1 行に戻す。
// 2 行目から後ろを捨てずに残す（PR #415 のレビューの必須 1）。カーソルは、元の行と列から 1 行の中の位置へ移す。
func (e *Editor) joinConsoleInput(f *floatState) {
	buf := f.bs.buf
	c := f.bs.state.Cursor()
	var b strings.Builder
	at := 0
	for i := range buf.LineCount() {
		if i > 0 {
			b.WriteByte('\n')
		}
		line := buf.Line(i)
		if i == c.Line {
			at = b.Len() + min(c.Byte, len(line))
		}
		b.WriteString(line)
	}
	raw := b.String()
	text := consoleInputText(raw)
	e.reload(f.bs, buffer.New(text))
	f.bs.state.SetCursor(buffer.Position{Byte: min(len(consoleInputText(raw[:at])), len(text))})
}

// deleteConsoleInput は、書き足すテキストと入力の行の組 f の入力の行の [from, to) のバイトを消し、カーソルを from に置く。
func (e *Editor) deleteConsoleInput(f *floatState, from, to int) {
	text := f.bs.buf.Line(0)
	if from >= to || to > len(text) {
		return
	}
	e.reload(f.bs, buffer.New(text[:from]+text[to:]))
	f.bs.state.SetCursor(buffer.Position{Byte: from})
}

// errConsoleWrite と errConsoleEdit は、書き足すテキストと入力の行の組の中で、書き出しとファイルを開く Ex コマンドを断る誤りである。
var (
	errConsoleWrite = errors.New("出力と入力の行の組のバッファは書き出せません")
	errConsoleEdit  = errors.New("出力と入力の行の組の中からはファイルを開けません（:q で閉じてから開きます）")
)

// consoleFileAction は、書き足すテキストと入力の行の組 f の中で打った Ex コマンド a が、書き出し（:w、:w {ファイル}）か
// ファイルを開く操作（:e {ファイル}）なら、断る誤りを返す（doAction から呼ぶ）。組でなければ nil を返す。
// :w {ファイル} を通すと、入力の行のバッファがそのファイルの名前になり、ステータス行の名前が替わって、以後の中身も保存済みに見える
// （PR #415 のレビューの推奨 2）。:e {ファイル} は、ほかの部品のようにポップアップを閉じて開くと、打ちかけの入力と出力を黙って失うので断る。
func consoleFileAction(f *floatState, a ex.Action) error {
	if f.con == nil {
		return nil
	}
	switch {
	case a.Kind == ex.ActionWrite:
		return errConsoleWrite
	case a.Kind == ex.ActionEdit && a.File != "":
		return errConsoleEdit
	}
	return nil
}

// setConsoleInput は、書き足すテキストと入力の行の組 f の入力の行の中身を text にし、カーソルを末尾に置く。
// 中身を替えるのは、ファインダーの入力の行と同じく読み直しの入り口（reload）で行う。
func (e *Editor) setConsoleInput(f *floatState, text string) {
	if f.bs.buf.Line(0) != text || f.bs.buf.LineCount() != 1 {
		e.reload(f.bs, buffer.New(text))
	}
	f.bs.state.SetCursor(buffer.Position{Byte: len(text)})
}

// toggleConsoleFocus は、<C-w>w で、書き足すテキストと入力の行の組 f の出力と入力の行の間でフォーカスを移す（FR-274、FR-71）。
// 出力では、ノーマルモードの移動、検索、ヤンクで読み返せる。末尾の追従の規則は、フォーカスがどちらにあっても変えない。
//
// req: FR-274
func (e *Editor) toggleConsoleFocus(f *floatState) {
	f.con.outFocus = !f.con.outFocus
	e.switchFocus(e.consoleFocusBuffer(f))
}

// consoleFocusBuffer は、書き足すテキストと入力の行の組 f で、キーを受ける方のバッファ（出力か入力の行）を返す。
func (e *Editor) consoleFocusBuffer(f *floatState) *bufferState {
	if f.con.outFocus && slices.Contains(e.bufs, f.con.out) {
		return f.con.out
	}
	return f.bs
}

// uiConsole は、書き足すテキストと入力の行の組 f の、入力の行のほかの描画の入力を作る（floatFrame）。
func (e *Editor) uiConsole(f *floatState) *ui.Console {
	con := f.con
	out := con.out
	st := out.state
	view := &ui.Frame{
		Text:        out.buf,
		LineLayout:  e.lineLayout(out, false),
		Margins:     e.marginsFor(st),
		Cursor:      st.Cursor(),
		Decorations: out.decoView,
	}
	if f.focused && con.outFocus {
		view.Mode = uiMode(st.Mode())
		view.OperatorPending = st.Mode() == command.ModeNormal && st.Pending()
		if start, end, linewise, ok := st.Selection(); ok {
			view.Selection = ui.Selection{Active: true, Start: start, End: end, Linewise: linewise}
		}
	}
	return &ui.Console{Output: view, Prompt: con.prompt, OutputFocused: con.outFocus}
}

// consoleOutViewport は、書き足すテキストと入力の行の組の出力のバッファのページ単位のスクロール（command.State.Viewport）と、
// 書き足しで古い行を捨てたときの見ている行のずらし（appendFloatText）を、出力の表示領域につなぐ口である。
// まだ描いていなければ、1 行の表示領域として扱う。部品の client が描く部品（floatState.remote）では、その client の画面の窓を使う。
// ct は、その組の中身である（切り替えの部品の子なら、その子を描いた子の Renderer を使う。floatViewport と同じ）。
type consoleOutViewport struct {
	f  *floatState
	r  *ui.Renderer
	ct *floatContent
}

// sub は、出力を描く子の Renderer を返す。まだ描いていなければ nil である。
func (v consoleOutViewport) sub() *ui.Renderer {
	_, s := v.f.childRenderers(v.r, v.ct)
	return s
}

func (v consoleOutViewport) Top() int {
	if s := v.sub(); s != nil {
		return s.Top()
	}
	return 0
}

func (v consoleOutViewport) Rows() int {
	if s := v.sub(); s != nil {
		return max(s.TextRows(), 1)
	}
	return 1
}

func (v consoleOutViewport) Skip() int {
	if s := v.sub(); s != nil {
		return s.Skip()
	}
	return 0
}

func (v consoleOutViewport) SetView(top, skip, hold int) {
	if s := v.sub(); s != nil {
		s.SetScrollHeld(top, skip, hold)
	}
}
