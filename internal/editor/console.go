package editor

import (
	"slices"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 書き足すテキストと入力の行の組（ext.ViewConsole。基本設計 1.4 の「書き足すテキストと入力の行の組」。FR-274）。
//
// 出力（floatState.out）と入力の行（floatState.bs）を、それぞれ本体のバッファにする。出力は編集を禁止したファイルでないバッファで、
// view/append（viewappend.go）で書き足す。入力の行は、ファインダーの入力の行と同じく編集できる 1 行のバッファである。
// キーは、フォーカスのある方のバッファの State が受け、どちらの KeyHook も popupStateKey を通る（consoleKey、consoleOutputKey）。
//
// 入力の行の編集は、インサートモードの State のキー（文字、<BS>、<Del>、<Left>、<Right>、<Home>、<End>）と、
// input/ask の問いの入力（inputask.go）と同じ <C-w>、<C-u> で行う。<BS>、<Del>、<Left>、<Right> は、input/ask と同じく
// 結合文字を前の文字と一緒に 1 文字として扱い（prevCharStart、nextCharEnd）、<C-w> の区切りも input/ask と同じ prevWordStart である
// （consoleKey）。横のスクロールは、入力の行を描く子の Renderer が行う（ui.drawConsole）。
//
// 切り替えの部品（FR-269）の子にしたときの規則（子を初めて出したときのモード、切り替え直したときにフォーカスとモードを戻すこと）は、
// 切り替えの部品の実装で足す。そのときは、フォーカスのある方（outFocus）を子ごとに覚え、consoleFocusBuffer で戻す先を決める。

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
	f := &floatState{kind: floatConsole, ext: extID, title: c.Title, scratch: true, input: c.Input, prompt: c.Prompt}
	h := c.History
	if d := len(h) - ext.ConsoleHistoryMax; d > 0 {
		h = h[d:]
	}
	f.history = slices.Clone(h)
	f.histPos = len(f.history)
	f.bs = e.newPopupBuffer(consoleInputName, c.Input, "", false)
	f.bs.state.SetCursor(buffer.Position{Byte: len(c.Input)})
	lines, _, decs := floatLines(c, nil, nil)
	f.out = e.newPopupBuffer(consoleOutputName, floatBufferText(lines), "", true)
	f.out.state.Viewport = consoleOutViewport{f: f, r: e.renderer}
	if len(decs) > 0 {
		e.setPopupDecorations(f.out, decs)
	}
	f.out.state.SetCursor(buffer.Position{Line: f.out.buf.LineCount() - 1})
	f.textEOL = len(c.Text) > 0 && c.Text[len(c.Text)-1] == '\n'
	return f
}

// setConsoleContent は、書き足すテキストと入力の行の組 f への view/update（部品 c）を当てる（FR-274）。
// 出力（Text、Highlights）と前置き（Prompt）だけを置き換え、入力の行の中身と履歴は読まない（利用者が打っているため）。
// 出力のカーソルは、テキストの部品と同じく、最後の行にあれば（c.Tail なら常に）新しい中身の最後の行へ置き、そうでなければ行の番号を保つ。
func (e *Editor) setConsoleContent(f *floatState, c ext.ViewComponent) {
	out := f.out
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
	f.prompt = c.Prompt
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
//
// req: FR-274
func (e *Editor) submitConsole(f *floatState) {
	e.notifyInputChange(f)
	text := f.bs.buf.Line(0)
	e.notifyView(f, ext.MethodViewSelect, ext.ViewSelectParams{View: ext.ViewID(f.id), Item: text})
	f.history = ext.AppendConsoleHistory(f.history, text)
	f.histPos, f.histDraft = len(f.history), ""
	e.setConsoleInput(f, "")
}

// consoleHistory は、書き足すテキストと入力の行の組 f の入力の行に、入力の履歴の d 件先（-1 は前、1 は後）の中身を入れる（FR-274）。
// たどり始めるときに打ちかけの中身を覚え、最後より後へ進むと打ちかけの中身に戻す。最初より前と、たどっていないときの後へは進まない。
// カーソルは中身の末尾に置く。
//
// req: FR-274
func (e *Editor) consoleHistory(f *floatState, d int) {
	n := len(f.history)
	pos := f.histPos + d
	if pos < 0 || pos > n || pos == f.histPos {
		return
	}
	if f.histPos >= n {
		f.histDraft = f.bs.buf.Line(0)
	}
	f.histPos = pos
	text := f.histDraft
	if pos < n {
		text = f.history[pos]
	}
	e.setConsoleInput(f, text)
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
	f.outFocus = !f.outFocus
	e.switchFocus(e.consoleFocusBuffer(f))
}

// consoleFocusBuffer は、書き足すテキストと入力の行の組 f で、キーを受ける方のバッファ（出力か入力の行）を返す。
func (e *Editor) consoleFocusBuffer(f *floatState) *bufferState {
	if f.outFocus && slices.Contains(e.bufs, f.out) {
		return f.out
	}
	return f.bs
}

// uiConsole は、書き足すテキストと入力の行の組 f の、入力の行のほかの描画の入力を作る（floatFrame）。
func (e *Editor) uiConsole(f *floatState) *ui.Console {
	out := f.out
	st := out.state
	view := &ui.Frame{
		Text:        out.buf,
		LineLayout:  e.lineLayout(out, false),
		Margins:     e.marginsFor(st),
		Cursor:      st.Cursor(),
		Decorations: out.decoView,
	}
	if f.focused && f.outFocus {
		view.Mode = uiMode(st.Mode())
		view.OperatorPending = st.Mode() == command.ModeNormal && st.Pending()
		if start, end, linewise, ok := st.Selection(); ok {
			view.Selection = ui.Selection{Active: true, Start: start, End: end, Linewise: linewise}
		}
	}
	return &ui.Console{Output: view, Prompt: f.prompt, OutputFocused: f.outFocus}
}

// consoleOutViewport は、書き足すテキストと入力の行の組の出力のバッファのページ単位のスクロール（command.State.Viewport）と、
// 書き足しで古い行を捨てたときの見ている行のずらし（appendFloatText）を、出力の表示領域につなぐ口である。
// まだ描いていなければ、1 行の表示領域として扱う。部品の client が描く部品（floatState.remote）では、その client の画面の窓を使う。
type consoleOutViewport struct {
	f *floatState
	r *ui.Renderer
}

// sub は、出力を描く子の Renderer を返す。まだ描いていなければ nil である。
func (v consoleOutViewport) sub() *ui.Renderer {
	if c := v.f.remote; c != nil {
		return c.r.ConsoleOutputRenderer()
	}
	return v.r.ConsoleOutputRenderer()
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
