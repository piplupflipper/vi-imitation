package editor

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// consoleInfo は、書き足すテキストと入力の行の組の状態をテストで比べる形にしたものである。
type consoleInfo struct {
	input, output string
	cursor        int // 入力の行のカーソルのバイトの位置
	mode          command.Mode
	outLine       int // 出力のカーソルの行
	outFocus      bool
	history       []string
}

// consoleOf は、組 f の状態を返す。イベントループの goroutine で呼ぶ。
func consoleOf(f *floatState) consoleInfo {
	return consoleInfo{
		input: f.bs.buf.Text(), output: f.out.buf.Text(), cursor: f.bs.state.Cursor().Byte, mode: f.bs.state.Mode(),
		outLine: f.out.state.Cursor().Line, outFocus: f.outFocus, history: f.history,
	}
}

// console は、読み込んでいる client の中央のポップアップの組の状態を返す。組を出していなければ ok が false である。
func (h *harness) console() (ci consoleInfo, ok bool) {
	h.e.do(func() {
		if f := h.e.float; f != nil && f.kind == floatConsole {
			ci, ok = consoleOf(f), true
		}
	})
	return ci, ok
}

// consoleOutput は、読み込んでいる client の中央のポップアップの組の出力の中身を返す。イベントループの goroutine で呼ぶ
// （eventually の中で使う）。
func consoleOutput(e *Editor) string {
	if f := e.float; f != nil && f.kind == floatConsole {
		return f.out.buf.Text()
	}
	return ""
}

// waitSeen は、substr を含むメッセージがメッセージの履歴に入るまで待つ。組の送信の後は、入力の行を空にした input/change の知らせが
// view/select の知らせを上書きするので、いまのメッセージだけを見る waitMessage では待てない。
func (h *harness) waitSeen(substr string) {
	h.t.Helper()
	h.eventually(substr, func() bool {
		return slices.ContainsFunc(h.e.msgHistory, func(m ui.Message) bool { return strings.Contains(m.Text, substr) })
	})
}

// openConsole は、:fake.popup console で中央のポップアップに組を開き、開いたことの知らせが届くまで待つ。
func openConsole(t *testing.T, h *harness) {
	t.Helper()
	h.keys(":fake.popup console<CR>")
	h.eventually("組", func() bool { return h.e.float != nil && h.e.float.kind == floatConsole })
	var view uint64
	h.e.do(func() { view = h.e.float.id })
	h.waitPopupOpened(view)
}

// 書き足すテキストと入力の行の組は、開いたら入力の行のインサートモードで始める。<CR> で中身を view/select で知らせ、
// 空でない中身を履歴に積んで入力の行を空にし、モードを変えない。空の中身の <CR> も知らせる（履歴には積まない）。
// インサートモードの <Up> と <Down> で履歴をたどり、最後より後へ進むと打ちかけの中身に戻す。
// インサートモードの <Esc> はノーマルモードへ戻るだけで閉じない。ノーマルモードの <CR> も送り、中央のポップアップはノーマルモードの <Esc> で閉じる。
//
// req: FR-274
func TestConsoleSubmitAndHistory(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t)})
	openConsole(t, h)
	first, _ := h.console()
	if first.mode != command.ModeInsert || first.input != "" || first.output != "welcome" || first.outFocus {
		t.Fatalf("開いたとき: %+v", first)
	}
	if fi, _ := h.float(); !fi.focused || !fi.center {
		t.Errorf("組のフォーカス: %+v", fi)
	}
	if scr := h.term.screen(); !strings.Contains(scr, "welcome") || !strings.Contains(scr, "> ") {
		t.Errorf("組の画面:\n%s", scr)
	}

	// 送った中身は拡張機能へ知らせ、拡張機能が出力へ書き足す。入力の行は空になり、インサートモードのまま続けて打てる。
	h.keys("say hi<CR>")
	h.waitSeen("kind=1 item=say hi key=")
	h.eventually("書き足した出力", func() bool { return consoleOutput(h.e) == "welcome\nhi" })
	if ci, _ := h.console(); ci.input != "" || ci.mode != command.ModeInsert || strings.Join(ci.history, ",") != "h1,say hi" {
		t.Errorf("送った後: %+v", ci)
	}

	// 空の中身も知らせるが、履歴には積まない。
	h.keys("<CR>")
	h.waitSeen("kind=1 item= key=")
	if ci, _ := h.console(); strings.Join(ci.history, ",") != "h1,say hi" {
		t.Errorf("空の送信の後の履歴: %q", ci.history)
	}

	// 打ちかけの中身から履歴をたどり、戻ると打ちかけの中身に戻る。最初より前へは進まない。
	h.keys("ab<Up>")
	h.settle()
	if ci, _ := h.console(); ci.input != "say hi" || ci.cursor != len("say hi") {
		t.Errorf("<Up>: %+v", ci)
	}
	h.keys("<Up><Up><Up>")
	h.settle()
	if ci, _ := h.console(); ci.input != "h1" {
		t.Errorf("<Up> の端: %+v", ci)
	}
	h.keys("<Down><Down>")
	h.settle()
	if ci, _ := h.console(); ci.input != "ab" {
		t.Errorf("<Down> で打ちかけの中身へ: %+v", ci)
	}
	h.keys("<Down>")
	h.settle()
	if ci, _ := h.console(); ci.input != "ab" {
		t.Errorf("たどっていないときの <Down>: %+v", ci)
	}

	// インサートモードの <Esc> は閉じない。ノーマルモードの <CR> も送る。
	h.keys("<Esc>")
	h.settle()
	if ci, ok := h.console(); !ok || ci.mode != command.ModeNormal {
		t.Fatalf("インサートモードの <Esc> の後: %+v %v", ci, ok)
	}
	h.keys("<CR>")
	h.waitSeen("kind=1 item=ab key=")
	if ci, _ := h.console(); ci.input != "" || ci.mode != command.ModeNormal {
		t.Errorf("ノーマルモードの <CR> の後: %+v", ci)
	}
	// 中央のポップアップは、ノーマルモードの <Esc> で閉じる。
	h.keys("<Esc>")
	h.waitSeen("kind=0 item= key=")
	if _, ok := h.console(); ok {
		t.Errorf("ノーマルモードの <Esc> で閉じない")
	}
}

// 出力に書き足している間も、入力の行の中身、カーソル、モードを変えない。出力のカーソルは最後の行にあれば末尾に追従する。
// 書き足しはアンドゥの履歴に積まない。
//
// req: FR-274, FR-272
func TestConsoleAppendWhileTyping(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t)})
	openConsole(t, h)
	h.keys("abcd<Left><Left>")
	h.settle()
	h.e.do(func() {
		e := h.e
		f := e.float
		for i := range 50 {
			appendTo(e, f, fmt.Sprintf("out %d\n", i))
		}
		ci := consoleOf(f)
		if ci.input != "abcd" || ci.cursor != 2 || ci.mode != command.ModeInsert {
			t.Errorf("書き足しの後の入力の行: %+v", ci)
		}
		if n := f.out.buf.LineCount(); ci.outLine != n-1 || f.out.buf.Line(n-1) != "out 49" {
			t.Errorf("出力の末尾の追従: line %d of %d (%q)", ci.outLine, n, f.out.buf.Line(n-1))
		}
		if f.out.buf.CanUndo() {
			t.Errorf("書き足しがアンドゥの履歴に入った")
		}
		if e.cur != f.bs {
			t.Errorf("キーを受けるバッファが入力の行でない")
		}
	})
	// 書き足した後も、打った文字は入力の行のカーソルの位置に入る。
	h.keys("X")
	h.settle()
	if ci, _ := h.console(); ci.input != "abXcd" {
		t.Errorf("書き足しの後に打った: %+v", ci)
	}
}

// <C-w>w で出力へフォーカスを移すと、ノーマルモードの移動、検索、ヤンクで読み返せる。出力を最後の行より上へ動かしている間は
// 書き足しで追従せず、最後の行へ戻すとまた追従する。もう一度 <C-w>w で入力の行へ戻り、モードは離れたときのままである。
// 出力は編集を禁止している。
//
// req: FR-274
func TestConsoleOutputFocus(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t)})
	openConsole(t, h)
	h.keys("say alpha<CR>say beta<CR>")
	h.eventually("書き足した出力", func() bool { return consoleOutput(h.e) == "welcome\nalpha\nbeta" })
	h.keys("<Esc><C-w>w")
	h.settle()
	var onOut bool
	h.e.do(func() { onOut = h.e.cur == h.e.float.out })
	if ci, _ := h.console(); !ci.outFocus || !onOut {
		t.Fatalf("<C-w>w で出力へ移らない: %+v %v", ci, onOut)
	}
	h.keys(`/alp<CR>"ayy`)
	h.settle()
	var yank string
	h.e.do(func() {
		r, _ := h.e.cur.state.Registers().Get('a')
		yank = r.Text
	})
	if yank != "alpha" {
		t.Errorf("出力の検索とヤンク: %q", yank)
	}
	h.keys("dd")
	h.settle()
	if msg := h.message(); !strings.Contains(msg, "E21") {
		t.Errorf("出力の編集: message %q", msg)
	}
	// 上の行を見ている間は追従しない。
	h.e.do(func() {
		f := h.e.float
		appendTo(h.e, f, "gamma\n")
		if c := f.out.state.Cursor(); c.Line != 1 {
			t.Errorf("上の行を見ている間に追従した: %v", c)
		}
	})
	h.keys("G")
	h.settle()
	h.e.do(func() {
		f := h.e.float
		appendTo(h.e, f, "delta\n")
		if c, n := f.out.state.Cursor(), f.out.buf.LineCount(); c.Line != n-1 {
			t.Errorf("最後の行へ戻した後に追従しない: %v of %d", c, n)
		}
	})
	if scr := h.term.screen(); !strings.Contains(scr, "[出力]") {
		t.Errorf("出力のステータス行:\n%s", scr)
	}
	h.keys("<C-w>w")
	h.settle()
	h.e.do(func() { onOut = h.e.cur == h.e.float.out })
	if ci, _ := h.console(); ci.outFocus || onOut || ci.mode != command.ModeNormal {
		t.Errorf("入力の行へ戻らない: %+v %v", ci, onOut)
	}
	// 出力のノーマルモードの <Esc> でも、中央のポップアップを閉じる。
	h.keys("<C-w>w<Esc>")
	h.waitSeen("kind=0 item= key=")
	var cur string
	h.e.do(func() { cur = h.e.bufferName(h.e.cur) })
	if _, ok := h.console(); ok || cur == consoleOutputName {
		t.Errorf("出力の <Esc> の後: open=%v cur=%q", ok, cur)
	}
}

// 入力の行のインサートモードの <BS>、<Del>、<Left>、<Right> は、結合文字を前の文字と一緒に 1 文字として扱う（input/ask と同じ）。
// 全角文字も 1 文字として消す。<C-w> と <C-u> はインサートモードのものである。
//
// req: FR-274
func TestConsoleMultibyte(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t)})
	openConsole(t, h)
	ga := "が" // 「か」と結合文字の濁点
	h.keys("あ" + ga + "い")
	h.settle()
	h.keys("<Left><Left>")
	h.settle()
	if ci, _ := h.console(); ci.cursor != len("あ") {
		t.Errorf("<Left> が結合文字の間で止まった: %+v", ci)
	}
	h.keys("<Right>")
	h.settle()
	if ci, _ := h.console(); ci.cursor != len("あ"+ga) {
		t.Errorf("<Right> が結合文字の間で止まった: %+v", ci)
	}
	h.keys("<BS>")
	h.settle()
	if ci, _ := h.console(); ci.input != "あい" || ci.cursor != len("あ") {
		t.Errorf("<BS> が結合文字だけを消した: %+v", ci)
	}
	h.keys(ga + "<Left><Del>")
	h.settle()
	if ci, _ := h.console(); ci.input != "あい" {
		t.Errorf("<Del> が結合文字だけを消した: %+v", ci)
	}
	h.keys("<End> foo bar<C-w>")
	h.settle()
	if ci, _ := h.console(); ci.input != "あい foo " {
		t.Errorf("<C-w>: %q", ci.input)
	}
	h.keys("<C-u>")
	h.settle()
	if ci, _ := h.console(); ci.input != "" {
		t.Errorf("<C-u>: %q", ci.input)
	}
	// 送った全角文字の中身は、そのまま知らせる。
	h.keys("say 全角" + ga + "<CR>")
	h.waitSeen("kind=1 item=say 全角" + ga)
	h.eventually("書き足した全角文字", func() bool { return strings.HasSuffix(consoleOutput(h.e), "全角"+ga) })
}

// view/update は出力と前置きだけを置き換え、入力の行の中身と履歴は読まない。部品の種類の違う view/update は捨てる。
// 開くときの履歴は、新しい方から上限の件数だけを持つ。
//
// req: FR-274
func TestConsoleUpdate(t *testing.T) {
	h := start(t, Config{})
	h.e.do(func() {
		e := h.e
		hist := make([]string, ext.ConsoleHistoryMax+3)
		for i := range hist {
			hist[i] = fmt.Sprint(i)
		}
		f := e.newComponentFloat(ext.ViewComponent{Kind: ext.ViewConsole, Text: "a\nb\n", Input: "typed", Prompt: "> ", History: hist}, "fake")
		f.center, f.focused = true, true
		e.openFloat(f)
		e.startConsoleInsert(f)
		if len(f.history) != ext.ConsoleHistoryMax || f.history[0] != "3" {
			t.Errorf("開いたときの履歴: %d 件, 最初 %q", len(f.history), f.history[0])
		}
		if ci := consoleOf(f); ci.output != "a\nb" || ci.outLine != 1 || ci.input != "typed" || ci.cursor != 5 || ci.mode != command.ModeInsert {
			t.Errorf("開いたとき: %+v", ci)
		}
		e.setFloatContent(f, ext.ViewComponent{Kind: ext.ViewConsole, Text: "x\ny\nz", Input: "ignored", Prompt: "$ ", History: []string{"q"}})
		if ci := consoleOf(f); ci.output != "x\ny\nz" || ci.outLine != 2 || ci.input != "typed" || len(ci.history) != ext.ConsoleHistoryMax || f.prompt != "$ " {
			t.Errorf("view/update の後: %+v prompt %q", ci, f.prompt)
		}
		// 上の行を見ていれば、行の番号を保つ。
		f.out.state.SetCursor(buffer.Position{Line: 0})
		e.setFloatContent(f, ext.ViewComponent{Kind: ext.ViewConsole, Text: "1\n2\n3\n4"})
		if c := f.out.state.Cursor(); c.Line != 0 {
			t.Errorf("上の行を見ていたときの view/update: %v", c)
		}
		// 部品の種類の違う view/update は捨てる。
		e.viewUpdate(&exthost.ViewUpdate{Extension: "fake", Params: ext.ViewUpdateParams{View: ext.ViewID(f.id), Component: ext.ViewComponent{Kind: ext.ViewText, Text: "t"}}})
		if got := f.out.buf.Text(); got != "1\n2\n3\n4" {
			t.Errorf("種類の違う view/update を当てた: %q", got)
		}
		e.closeFloat(false)
		if e.float != nil || slices.Contains(e.bufs, f.out) || slices.Contains(e.bufs, f.bs) {
			t.Errorf("閉じた後にバッファが残った")
		}
	})
}

// 分割したペインに出した組も、開いたら入力の行のインサートモードで始め、ペインで打ったキーで送れる。
// ペインの <Esc> は閉じない。毎秒数百行の出力が届いている間も、ペインで打った文字は入力の行に入る。
//
// req: FR-274
func TestConsolePane(t *testing.T) {
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t)}
	calls.config(&cfg)
	h := start(t, cfg)
	peer := h.attach(ClientInfo{ID: 2, Pane: "%9"})
	peer.keys(":fake.pane console<CR>")
	h.eventually("OpenPane", func() bool { _, ok := calls.last(true); return ok })
	call, _ := calls.last(true)
	h.waitPaneOpened(call.view)
	comp := h.attachComponent(call.view, true)
	comp.waitScreen("welcome")
	var pane *floatState
	h.e.do(func() { pane = h.e.viewOf(h.clientOf(peer.term)).panes[0] })
	paneConsole := func() (ci consoleInfo) {
		h.e.do(func() { ci = consoleOf(pane) })
		return ci
	}
	if ci := paneConsole(); ci.mode != command.ModeInsert {
		t.Fatalf("ペインの組のモード: %+v", ci)
	}
	comp.keys("say from pane<CR>")
	h.waitSeen("kind=1 item=say from pane key=")
	comp.waitScreen("from pane")

	// 出力を流している間に打つ。
	comp.keys("flood 400<CR>")
	h.waitSeen("kind=1 item=flood 400")
	comp.keys("typing while flooding")
	h.waitShown("flooded")
	h.settle()
	h.eventually("流した出力", func() bool { return strings.HasSuffix(pane.out.buf.Text(), "line 399") })
	if ci := paneConsole(); ci.input != "typing while flooding" || ci.mode != command.ModeInsert {
		t.Errorf("流している間に打った入力の行: %+v", ci)
	}
	comp.waitScreen("line 399")
	comp.keys("<Esc><Esc>")
	h.settle()
	var panes int
	h.e.do(func() { panes = len(h.e.viewOf(h.clientOf(peer.term)).panes) })
	if panes != 1 {
		t.Errorf("ペインの <Esc> で閉じた")
	}
	// ペインでも <C-w>w で出力へ移れ、ステータス行は出力のバッファのものになる。
	comp.keys("<C-w>w")
	h.settle()
	if ci := paneConsole(); !ci.outFocus {
		t.Errorf("ペインの <C-w>w: %+v", ci)
	}
	comp.waitScreen("[出力]")
	comp.keys("k")
	h.settle()
	if ci := paneConsole(); ci.outLine >= pane.out.buf.LineCount()-1 {
		t.Errorf("ペインの出力で k が効かない: %+v", ci)
	}
}
