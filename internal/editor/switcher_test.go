package editor

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// switcherInfo は、切り替えの部品の状態をテストで比べる形にしたものである。
type switcherInfo struct {
	id       uint64
	sel      string
	kind     floatKind
	mode     command.Mode // キーを受けているバッファのモード
	curIsBs  bool         // キーを受けているのが選んでいる子の bs（組では入力の行）か
	curIsOut bool         // キーを受けているのが組の出力か
	line     int          // 選んでいる子の bs のカーソルの行
	text     string       // 選んでいる子の bs の中身（組では出力）
}

// switcher は、読み込んでいる client の中央のポップアップの切り替えの部品の状態を返す。出していなければ ok が false である。
func (h *harness) switcher() (si switcherInfo, ok bool) {
	h.e.do(func() {
		f := h.e.float
		if f == nil || f.sw == nil {
			return
		}
		si = switcherInfoOf(h.e, f)
		ok = true
	})
	return si, ok
}

func switcherInfoOf(e *Editor, f *floatState) switcherInfo {
	si := switcherInfo{id: f.id, sel: f.sw.cur().id, kind: f.kind, mode: e.cur.state.Mode(), curIsBs: e.cur == f.bs, line: f.bs.state.Cursor().Line, text: f.textBuf().buf.Text()}
	si.curIsOut = f.con != nil && e.cur == f.con.out
	return si
}

// childText は、切り替えの部品 f の子 id の中身（組なら出力）を返す。イベントループの goroutine で呼ぶ。
func childText(f *floatState, id string) string {
	i := f.sw.index(id)
	if i < 0 {
		return "<none>"
	}
	return f.sw.children[i].content.textBuf().buf.Text()
}

// openSwitcher は、:fake.popup kind で中央のポップアップに切り替えの部品を開き、開いたことの知らせが届くまで待つ。
func openSwitcher(t *testing.T, h *harness, kind string) uint64 {
	t.Helper()
	h.keys(":fake.popup " + kind + "<CR>")
	h.eventually("切り替えの部品", func() bool { return h.e.float != nil && h.e.float.sw != nil })
	var view uint64
	h.e.do(func() { view = h.e.float.id })
	h.waitPopupOpened(view)
	return view
}

// seen は、substr を含むメッセージがメッセージの履歴にあるかを、イベントループの goroutine で読んで返す。
func (h *harness) seen(substr string) (ok bool) {
	h.e.do(func() {
		ok = slices.ContainsFunc(h.e.msgHistory, func(m ui.Message) bool { return strings.Contains(m.Text, substr) })
	})
	return ok
}

func startSwitcher(t *testing.T) *harness {
	t.Helper()
	term := newFakeTerm()
	term.w, term.h = 80, 24
	return startTerm(t, term, Config{Extensions: fakeSpecs(t)})
}

// 切り替えの部品は、見出しの行に子の名前とキーを並べ、選んだ子だけを出す。]v、[v、]V、[V（本体の操作 view.child-*）で次、前、最後、最初の子へ
// 切り替え、次と前は回数の数だけ進んで端で折り返す。子のキー（T、L、R）でその子へ直に切り替える。利用者が切り替えたら view/childSelect で知らせる。
// 書き足すテキストと入力の行の組の子を初めて出すと入力の行のインサートモードにし、切り替え直すと最後にフォーカスのあったバッファとモードに戻す。
// 組の子では、出力と入力の行のどちらのノーマルモードでも切り替えのキーが効き、インサートモードの ]v は文字として入る。
//
// req: FR-269, FR-270, FR-301
func TestSwitcherKeys(t *testing.T) {
	h := startSwitcher(t)
	openSwitcher(t, h, "switcher")
	si, _ := h.switcher()
	if si.sel != "tree" || si.kind != floatTree || !si.curIsBs || si.mode != command.ModeNormal {
		t.Fatalf("開いたとき: %+v", si)
	}
	scr := h.term.screen()
	for _, want := range []string{" Tree [T] ", " List [L] ", " Out ", " Repl [R] ", "▸ dir"} {
		if !strings.Contains(scr, want) {
			t.Errorf("画面に %q が無い:\n%s", want, scr)
		}
	}
	if strings.Contains(scr, "first") {
		t.Errorf("見えていない子を描いた:\n%s", scr)
	}

	sel := func(keys, want string) {
		t.Helper()
		h.keys(keys)
		h.settle()
		if si, _ := h.switcher(); si.sel != want {
			t.Fatalf("%s の後の子 = %q, want %q", keys, si.sel, want)
		}
	}
	sel("]v", "list")
	h.waitSeen("kind=6 item= key= child=list")
	h.eventually("一覧の子を描く", func() bool { return strings.Contains(h.term.screen(), "first") })
	sel("[v", "tree")
	sel("]V", "repl")
	// 組の子を初めて出すと、入力の行のインサートモードにする（FR-301）。
	if si, _ := h.switcher(); !si.curIsBs || si.mode != command.ModeInsert {
		t.Fatalf("組の子を初めて出したとき: %+v", si)
	}
	// インサートモードの ]v は文字として入る。
	h.keys("]v<Esc>")
	h.settle()
	if si, _ := h.switcher(); si.sel != "repl" || si.mode != command.ModeNormal {
		t.Fatalf("インサートモードの ]v: %+v", si)
	}
	sel("[V", "tree")
	sel("2]v", "out")
	sel("]v", "repl")
	// 切り替え直すと、最後のモード（ノーマルモード）と入力の行の中身に戻す。
	var input string
	h.e.do(func() { input = h.e.float.bs.buf.Text() })
	if si, _ := h.switcher(); !si.curIsBs || si.mode != command.ModeNormal || input != "]v" {
		t.Fatalf("組の子へ戻ったとき: %+v input=%q", si, input)
	}
	sel("]v", "tree") // 端で折り返す
	sel("3[v", "list")
	sel("[v[v[v", "out")

	// 子のキーで直に切り替える。組の出力へフォーカスを移して離れ、戻ると出力に戻る。出力のノーマルモードでも切り替えのキーが効く。
	sel("R", "repl")
	h.keys("<C-w>w")
	h.settle()
	if si, _ := h.switcher(); !si.curIsOut {
		t.Fatalf("<C-w>w の後: %+v", si)
	}
	sel("T", "tree")
	sel("R", "repl")
	if si, _ := h.switcher(); !si.curIsOut || si.mode != command.ModeNormal {
		t.Fatalf("出力へ戻ったとき: %+v", si)
	}
	sel("]v", "tree")
	h.keys("<Esc>")
	h.settle()
	if _, ok := h.switcher(); ok {
		t.Fatal("<Esc> で閉じない")
	}
	// 切り替えの部品の外の ]v は、何もせず誤りも出さない。
	h.keys("]v")
	h.settle()
	if m := h.message(); m != "" && !strings.Contains(m, "kind=") {
		t.Errorf("部品の外の ]v のメッセージ: %q", m)
	}
}

// 子ごとに、カーソル、木の開閉、スクロールの位置を保つ。子の中の知らせ（view/select、view/expand、view/key）には子の ID を付け、
// popupKeys の children を並べた項目（d、zs）はその子でだけ効く。<Tab> と <S-Tab> を子を切り替える action にした popupKeys は、
// 拡張機能を呼ばずに子を切り替える。マクロに記録した切り替えは、再生でも同じく切り替える。
//
// req: FR-269, FR-270, FR-271
func TestSwitcherChildState(t *testing.T) {
	h := startSwitcher(t)
	openSwitcher(t, h, "switcher")
	h.keys("<CR>")
	h.waitSeen("kind=4 item=dir key= expanded=true child=tree")
	h.keys("jj")
	h.settle()
	// popupKeys の d は、一覧の子だけで効く。木の子の d は本体のキー（オペレーター）のままで、view/key を送らない。
	h.keys("L")
	h.settle()
	h.keys("j")
	h.keys("d")
	h.waitSeen("kind=3 item=close key=fake.del child=list")
	h.keys("k<CR>")
	h.waitSeen("kind=1 item=one key= child=list")
	h.keys("j")
	h.keys("T")
	h.settle()
	si, _ := h.switcher()
	if si.sel != "tree" || si.line != 2 || !strings.Contains(si.text, "▾ dir") {
		t.Fatalf("木の子へ戻ったとき: %+v", si)
	}
	h.keys("d<Esc>")
	h.settle()
	if h.seen("fake.del child=tree") {
		t.Error("木の子で d が view/key を送った")
	}
	h.keys("L")
	h.settle()
	if si, _ := h.switcher(); si.line != 1 {
		t.Errorf("一覧の子へ戻ったときのカーソル: %+v", si)
	}

	// <Tab> と <S-Tab>（popupKeys の子を切り替える action）。view/key は送らず、view/childSelect を送る。
	h.keys("<Tab>")
	h.waitSeen("kind=6 item= key= child=out")
	h.keys("<S-Tab><S-Tab>")
	h.settle()
	if si, _ := h.switcher(); si.sel != "tree" {
		t.Fatalf("<S-Tab> の後: %+v", si)
	}
	if h.seen("key=fake.next") {
		t.Error("子を切り替える action のキーが view/key を送った")
	}

	// マクロに記録した切り替えは、再生でも切り替える。
	h.keys("qa]vjq")
	h.settle()
	if si, _ := h.switcher(); si.sel != "list" {
		t.Fatalf("記録の後: %+v", si)
	}
	h.keys("T@a")
	h.settle()
	if si, _ := h.switcher(); si.sel != "list" || si.line != 2 {
		t.Fatalf("再生の後: %+v", si)
	}
}

// 見えていない子への view/update と view/append は、その場で子の中身に当て、切り替えたときに最新の中身を出す。末尾の追従も見えている子と
// 同じに当てる。子を指す更新は、種類の違う更新と知らない子への更新と書き足しを捨てる。部品の全体を置き換える更新は、残った子の状態を保ち、
// 無くなった子のバッファを閉じ、selected の子を選ぶ（view/childSelect は送らない）。
//
// req: FR-270
func TestSwitcherHiddenUpdates(t *testing.T) {
	h := startSwitcher(t)
	view := openSwitcher(t, h, "switcher")
	h.keys("R")
	h.keys("say hello<CR>")
	h.waitSeen("kind=1 item=say hello key= child=repl")
	h.eventually("見えていない子への書き足し", func() bool { return childText(h.e.float, "out") == "start\nhello" })
	h.keys("<Esc>[v")
	h.settle()
	si, _ := h.switcher()
	if si.sel != "out" || si.text != "start\nhello" || si.line != 1 {
		t.Fatalf("見えていない子へ書き足した後: %+v", si)
	}
	if !strings.Contains(h.term.screen(), "hello") {
		t.Errorf("書き足した中身を描かない:\n%s", h.term.screen())
	}

	// 一覧の子の update を選ぶと、その子と見えていない out の子を置き換える。
	h.keys("L")
	h.keys("G<CR>")
	h.waitSeen("kind=1 item=update key= child=list")
	h.eventually("見えていない子の置き換え", func() bool { return childText(h.e.float, "out") == "replaced" })
	if si, _ := h.switcher(); si.text != "updated" {
		t.Errorf("見えている子の置き換え: %+v", si)
	}

	// 知らない子、種類の違う更新、切り替えの部品でない部品の子を指す書き足しは捨てる。
	h.e.do(func() {
		h.e.viewUpdate(&exthost.ViewUpdate{Extension: "fake", Params: ext.ViewUpdateParams{View: ext.ViewID(view), Child: "nope", Component: ext.ViewComponent{Kind: ext.ViewText, Text: "x"}}})
		h.e.viewUpdate(&exthost.ViewUpdate{Extension: "fake", Params: ext.ViewUpdateParams{View: ext.ViewID(view), Child: "out", Component: ext.ViewComponent{Kind: ext.ViewList}}})
		h.e.viewAppend(&exthost.ViewAppend{Extension: "fake", Params: ext.ViewAppendParams{View: ext.ViewID(view), Child: "nope", Text: "x"}})
		h.e.viewAppend(&exthost.ViewAppend{Extension: "fake", Params: ext.ViewAppendParams{View: ext.ViewID(view), Child: "list", Text: "x"}})
		h.e.viewAppend(&exthost.ViewAppend{Extension: "fake", Params: ext.ViewAppendParams{View: ext.ViewID(view), Text: "x"}})
	})
	var outText, listText string
	h.e.do(func() { outText, listText = childText(h.e.float, "out"), childText(h.e.float, "list") })
	if outText != "replaced" || listText != "updated" {
		t.Errorf("捨てるべき更新を当てた: out=%q list=%q", outText, listText)
	}

	// 部品の全体を置き換える（木の子を捨て、out を先頭へ並べ直し、out を選ぶ）。一覧の子は状態を保つ。
	var treeBufs []*bufferState
	h.e.do(func() { treeBufs = h.e.float.sw.children[0].content.buffers() })
	h.keys("zs")
	h.eventually("部品の全体の置き換え", func() bool {
		f := h.e.float
		return f != nil && f.sw != nil && len(f.sw.children) == 3 && f.sw.cur().id == "out"
	})
	h.e.do(func() {
		f := h.e.float
		if got := []string{f.sw.children[0].id, f.sw.children[1].id, f.sw.children[2].id}; !slices.Equal(got, []string{"out", "list", "repl"}) {
			t.Errorf("子の並び = %q", got)
		}
		if childText(f, "out") != "swapped" || childText(f, "repl") != "repl" {
			t.Errorf("置き換えた中身: out=%q repl=%q", childText(f, "out"), childText(f, "repl"))
		}
		for _, bs := range treeBufs {
			if slices.Contains(h.e.bufs, bs) {
				t.Errorf("捨てた子のバッファ %d を閉じていない", bs.id)
			}
		}
		if h.e.cur != f.bs {
			t.Errorf("キーを受けるバッファが選んだ子でない")
		}
	})
	h.keys("L")
	h.settle()
	if si, _ := h.switcher(); si.sel != "list" || si.text != "first\nclose me\nupdate me" {
		t.Errorf("残った一覧の子: %+v", si)
	}
}

// 子が 1 つの切り替えの部品では、切り替えの操作は何もしない。閉じると、見えていない子を含むすべての子のバッファを閉じる。
//
// req: FR-269
func TestSwitcherOneChildAndClose(t *testing.T) {
	h := startSwitcher(t)
	openSwitcher(t, h, "switcher1")
	h.keys("]v[V<Tab>")
	h.settle()
	if si, _ := h.switcher(); si.sel != "tree" {
		t.Fatalf("子が 1 つ: %+v", si)
	}
	if h.seen("kind=6") {
		t.Error("子が 1 つの部品で view/childSelect を送った")
	}
	h.keys("<Esc>")
	h.settle()

	openSwitcher(t, h, "switcher")
	var bufs []*bufferState
	h.e.do(func() {
		for _, ct := range h.e.float.contents() {
			bufs = append(bufs, ct.buffers()...)
		}
	})
	h.keys("<Esc>")
	h.settle()
	h.e.do(func() {
		for _, bs := range bufs {
			if slices.Contains(h.e.bufs, bs) {
				t.Errorf("閉じた部品の子のバッファ %d が残っている", bs.id)
			}
		}
	})
}

// 見出しの行の子の名前を左のボタンで押すと、その子に切り替える（FR-185 の例外）。見出しの無い列を押しても何もしない。
//
// req: FR-269, FR-185
func TestSwitcherMouse(t *testing.T) {
	h := startSwitcher(t)
	openSwitcher(t, h, "switcher")
	row, col := -1, -1
	for i, l := range strings.Split(h.term.screen(), "\n") {
		if j := strings.Index(l, " List [L] "); j >= 0 {
			row, col = i, ui.StringWidth(l[:j], 8)+2
		}
	}
	if row < 0 {
		t.Fatalf("見出しの行が無い:\n%s", h.term.screen())
	}
	h.click(row, col)
	if si, _ := h.switcher(); si.sel != "list" {
		t.Fatalf("見出しを押した後: %+v", si)
	}
	h.waitSeen("kind=6 item= key= child=list")
	h.click(row, 78)
	if si, _ := h.switcher(); si.sel != "list" {
		t.Fatalf("見出しの無い列を押した後: %+v", si)
	}
}

// 見えていない子へ書き足し続けている間も、ほかの子を操作できる。書き足しは見えていない子の行数の上限で古い行から捨て、
// 末尾に追従したまま、切り替えたときに最後の行を出す。
//
// req: FR-270
func TestSwitcherFloodHidden(t *testing.T) {
	h := startSwitcher(t)
	openSwitcher(t, h, "switcher")
	h.keys("R")
	h.keys("flood 3000<CR><Esc>")
	h.keys("T")
	for range 20 {
		h.keys("jk")
	}
	h.settle()
	if si, _ := h.switcher(); si.sel != "tree" || si.line != 0 {
		t.Fatalf("書き足しの間の操作: %+v", si)
	}
	h.waitSeen("flooded")
	h.eventually("書き足しの最後の行", func() bool { return strings.HasSuffix(childText(h.e.float, "out"), "line 2999") })
	h.keys("[v[v")
	h.settle()
	si, _ := h.switcher()
	if si.sel != "out" {
		t.Fatalf("out へ: %+v", si)
	}
	var last int
	h.e.do(func() { last = h.e.float.bs.buf.LineCount() - 1 })
	if si.line != last {
		t.Errorf("末尾に追従していない: line=%d last=%d", si.line, last)
	}
	h.eventually("最後の行を描く", func() bool { return strings.Contains(h.term.screen(), "line 2999") })

	// スクロールの位置は子ごとに保つ。先頭へ動かして離れ、戻っても先頭を見ている。
	h.keys("gg")
	h.eventually("先頭を描く", func() bool { return strings.Contains(h.term.screen(), "start") })
	h.keys("T")
	h.eventually("木を描く", func() bool { return strings.Contains(h.term.screen(), "▸ dir") })
	h.keys("]v]v")
	h.eventually("戻った先頭を描く", func() bool { return strings.Contains(h.term.screen(), "start") })
	var top int
	h.e.do(func() { top = h.e.renderer.FloatRenderer().Top() })
	if top != 0 {
		t.Errorf("戻ったときの最初の行 = %d", top)
	}
}

// 分割したペインの切り替えの部品では、popupKeys で子を切り替える action にした <Tab> と <S-Tab> が、ペインの中では子を切り替え、
// 元の client の <Tab> と <S-Tab> はタブの移動（FR-98）のままである。組の子を初めて出すと入力の行のインサートモードにし、
// 離れて戻ると最後のモードに戻す（FR-301）。
//
// req: FR-269, FR-271, FR-301
func TestSwitcherPane(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "A\n", "b.txt": "B\n"})
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t), WorkDir: dir}
	calls.config(&cfg)
	h := start(t, cfg, "a.txt")
	h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })
	h.keys(":e b.txt<CR>")
	h.keys(":fake.pane switcher<CR>")
	h.eventually("OpenPane", func() bool { _, ok := calls.last(true); return ok })
	call, _ := calls.last(true)
	h.waitPaneOpened(call.view)
	comp := h.attachComponent(call.view, true)
	comp.waitScreen(" Tree [T] ")

	pane := func() (si switcherInfo) {
		h.settle()
		h.e.do(func() {
			p := h.e.panes[0]
			si = switcherInfo{sel: p.sw.cur().id, mode: p.bs.state.Mode(), line: p.bs.state.Cursor().Line}
		})
		return si
	}
	curTab := func() (name string) {
		h.e.do(func() { name = filepath.Base(h.e.cur.path) })
		return name
	}
	comp.keys("<Tab>")
	if si := pane(); si.sel != "list" {
		t.Fatalf("ペインの <Tab>: %+v", si)
	}
	if curTab() != "b.txt" {
		t.Errorf("ペインの <Tab> で元の client のタブが替わった: %s", curTab())
	}
	h.keys("<Tab>")
	h.settle()
	if curTab() != "a.txt" || pane().sel != "list" {
		t.Errorf("元の client の <Tab>: tab=%s pane=%+v", curTab(), pane())
	}
	comp.keys("<S-Tab><S-Tab>")
	if si := pane(); si.sel != "repl" || si.mode != command.ModeInsert {
		t.Fatalf("ペインの組の子を初めて出したとき: %+v", si)
	}
	comp.keys("ab<Esc>T")
	comp.waitScreen("▸ dir")
	comp.keys("R")
	if si := pane(); si.sel != "repl" || si.mode != command.ModeNormal {
		t.Fatalf("ペインの組の子へ戻ったとき: %+v", si)
	}
	comp.waitScreen("> ab")
}

// 子のキーは、読めない表記、2 つ以上のキー、本体の部品の操作と重なるキー、読んだ後に重なるキーを断る。
//
// req: FR-269
func TestSwitcherChildKeys(t *testing.T) {
	c := func(keys ...string) ext.ViewComponent {
		out := ext.ViewComponent{Kind: ext.ViewSwitcher}
		for i, k := range keys {
			out.Children = append(out.Children, ext.ViewChild{ID: string(rune('a' + i)), Key: k, Component: ext.ViewComponent{Kind: ext.ViewText}})
		}
		return out
	}
	for _, tt := range []struct {
		keys []string
		ok   bool
	}{
		{[]string{"S", "", "<C-x>"}, true},
		{[]string{"<CR>"}, false},
		{[]string{"<Esc>"}, false},
		{[]string{":"}, false},
		{[]string{"<C-w>"}, false},
		{[]string{"ab"}, false},
		{[]string{"<Bad"}, false},
		{[]string{"<C-x>", "<c-x>"}, false},
	} {
		if err := checkComponentKeys(c(tt.keys...)); (err == nil) != tt.ok {
			t.Errorf("%q: %v", tt.keys, err)
		}
	}
}
