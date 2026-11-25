//go:build linux

package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、デバッグの情報のペイン（pane.go、paneviews.go。FR-108、FR-277〜FR-278、FR-289〜FR-291、FR-295、FR-302〜FR-306）のテストである。

// paneHost は、テスト用のアダプター（args を付けて起動する）と、ファイルの種類 go の構成 1 つ（fake の項目が fake）と、
// 設定の ui（nil なら書かない）で拡張機能を初期化し、srcPath を開いて、2 行目と 5 行目にブレークポイントを置いた本体の役を返す。
func paneHost(t *testing.T, args []string, fake, ui map[string]any) *host {
	t.Helper()
	if fake == nil {
		fake = map[string]any{}
	}
	s := map[string]any{
		"adapters":       map[string]any{"fake": fakeDef(transportStdio, args...)},
		"configurations": map[string]any{"go": []any{map[string]any{"name": "parent", "type": "fake", "request": "launch", "fake": fake}}},
	}
	if ui != nil {
		s["ui"] = ui
	}
	h := newHost(t, mustJSON(t, s))
	h.openFT(srcPath, "go")
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdToggleBreakpoint, 5)
	h.waitSigns(bp5Signs...)
	return h
}

// toggleUI は、debug.toggleUI を呼ぶ。
func (h *host) toggleUI() {
	h.t.Helper()
	h.mustExec(ext.OpDebugToggleUI, 0)
}

// waitUpdate は、子 child を指す view/update のうち、部品が ok を満たすものが届くまで待ち、その部品を返す。途中のものは seen に足す。
func (h *host) waitUpdate(child string, ok func(ext.ViewComponent) bool, seen *[]ext.ViewComponent) ext.ViewComponent {
	h.t.Helper()
	deadline := time.After(testTimeout)
	var last ext.ViewComponent
	for {
		select {
		case u := <-h.updates:
			if u.Child != child {
				continue
			}
			if seen != nil {
				*seen = append(*seen, u.Component)
			}
			last = u.Component
			if ok(u.Component) {
				return u.Component
			}
		case <-deadline:
			h.t.Fatalf("view/update of %s did not arrive; last = %s", child, labels(last.Items))
			return ext.ViewComponent{}
		}
	}
}

// waitLabel は、子 child の木か一覧に、label を含む項目が出る view/update まで待つ。
func (h *host) waitLabel(child, label string) ext.ViewComponent {
	h.t.Helper()
	return h.waitUpdate(child, func(c ext.ViewComponent) bool { return strings.Contains(labels(c.Items), label) }, nil)
}

// labels は、項目の木を、深さを空白 2 つの字下げで表した行にする（開いていない項目の子は出さない）。
func labels(items []ext.ViewItem) string {
	var b strings.Builder
	var walk func([]ext.ViewItem, int)
	walk = func(items []ext.ViewItem, depth int) {
		for _, it := range items {
			b.WriteString(strings.Repeat("  ", depth) + it.Label + "\n")
			if it.Expanded == nil || *it.Expanded {
				walk(it.Children, depth+1)
			}
		}
	}
	walk(items, 0)
	return b.String()
}

// find は、木か一覧から ID が id の項目を探す。
func find(items []ext.ViewItem, id string) *ext.ViewItem {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
		if it := find(items[i].Children, id); it != nil {
			return it
		}
	}
	return nil
}

// childOf は、切り替えの部品 c の子 id の部品を返す。
func childOf(t *testing.T, c ext.ViewComponent, id string) ext.ViewComponent {
	t.Helper()
	i := c.ChildIndex(id)
	if i < 0 {
		t.Fatalf("no child %q", id)
	}
	return c.Children[i].Component
}

// notifyView は、本体が送る部品の知らせ method を送る。
func (h *host) notifyView(method string, params any) {
	h.t.Helper()
	if err := h.conn.Notify(method, params); err != nil {
		h.t.Fatalf("%s: %v", method, err)
	}
}

// key は、ペイン view の子 child のカーソルの項目 item で、popupKeys の id のキーを打ったことを知らせる。
func (h *host) key(view ext.ViewID, child, id, item string) {
	h.notifyView(ext.MethodViewKey, ext.ViewKeyParams{View: view, ID: id, Item: item, Child: child})
}

// TestPaneToggle は、debug.toggleUI で、7 つの区画を子にした切り替えの部品を、元のペインの下に 25% で開き（nvim-dap-view の既定）、
// 区画の名前とキーが nvim-dap-view と同じで、最初はウォッチ式を選び、もう一度で閉じることを確かめる。
// 設定の ui で、区画の並び、最初の区画、向き、大きさを変えられ、誤りは initialize を失敗させることも確かめる。
//
// req: FR-108, FR-277, FR-278
func TestPaneToggle(t *testing.T) {
	h := newHost(t, "")
	h.open(1, srcPath, srcText)
	h.toggleUI()
	p := h.waitPane()
	c := p.params.Component
	if c.Kind != ext.ViewSwitcher || c.Selected != secWatches || p.params.Direction != ext.PaneDown || p.params.Size != 25 || p.params.Focus {
		t.Errorf("pane/open = %+v", p.params)
	}
	var got []string
	for _, ch := range c.Children {
		got = append(got, ch.ID+":"+ch.Title+":"+ch.Key)
	}
	want := []string{"watches:Watches:W", "scopes:Scopes:S", "exceptions:Exceptions:E", "breakpoints:Breakpoints:B", "threads:Threads:T", "repl:REPL:R", "console:Console:C"}
	if !slices.Equal(got, want) {
		t.Errorf("children = %v, want %v", got, want)
	}
	if err := p.params.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
	if l := labels(childOf(t, c, secThreads).Items); !strings.Contains(l, "デバッグしていない") {
		t.Errorf("threads = %q", l)
	}
	h.toggleUI()
	select {
	case id := <-h.closes:
		if id != p.id {
			t.Errorf("view/close %d, want %d", id, p.id)
		}
	case <-time.After(testTimeout):
		t.Fatal("view/close did not arrive")
	}
	// 閉じた後のもう一度で、閉じたときの区画を選んで開き直す。
	h.toggleUI()
	if p2 := h.waitPane(); p2.params.Component.Selected != secWatches {
		t.Errorf("reopened selected = %q", p2.params.Component.Selected)
	}

	h2 := newHost(t, `{"ui": {"sections": ["console", "scopes"], "defaultSection": "scopes", "direction": "right", "size": 40}}`)
	h2.open(1, srcPath, srcText)
	h2.toggleUI()
	p = h2.waitPane()
	c = p.params.Component
	if len(c.Children) != 2 || c.Children[0].ID != secConsole || c.Selected != secScopes || p.params.Direction != ext.PaneRight || p.params.Size != 40 {
		t.Errorf("pane/open with ui = %+v", p.params)
	}
}

// TestParseUISettings は、設定の ui の誤りを確かめる。
//
// req: FR-278
func TestParseUISettings(t *testing.T) {
	for _, bad := range []string{
		`{"ui": {"sections": ["scopes", "nope"]}}`,
		`{"ui": {"sections": ["scopes", "scopes"]}}`,
		`{"ui": {"sections": []}}`,
		`{"ui": {"sections": ["scopes"], "defaultSection": "threads"}}`,
		`{"ui": {"direction": "up"}}`,
		`{"ui": {"size": 100}}`,
		`{"ui": {"autoOpen": "yes"}}`,
		`{"ui": {"unknown": 1}}`,
	} {
		if _, err := parseSettings([]byte(bad)); err == nil {
			t.Errorf("%s: nil error", bad)
		}
	}
	s, err := parseSettings([]byte(`{"ui": {"sections": ["threads", "console"], "autoOpen": true, "autoClose": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.ui.defaultSection != secThreads || !s.ui.autoOpen || !s.ui.autoClose {
		t.Errorf("ui = %+v", s.ui)
	}
}

// TestPaneScopes は、スコープの区画が、止まったフレームのスコープを最上位にし、expensive でないスコープを開いて変数を
// 「名前: 型 = 値」で出し、子を持つ変数は開いたときに読み（3000 の要素の列）、続けている間は「実行中」にし、止まり直すと新しい値を出すことを確かめる。
//
// req: FR-289, FR-302, FR-305
func TestPaneScopes(t *testing.T) {
	h := paneHost(t, nil, map[string]any{"stopDelayMs": 300}, map[string]any{"defaultSection": "scopes"})
	h.toggleUI()
	p := h.waitPane()
	h.mustExec(cmdStart, 0)
	c := h.waitLabel(secScopes, "x: int = 1")
	if l := labels(c.Items); !strings.Contains(l, "Locals\n") || !strings.Contains(l, "Globals\n") || strings.Contains(l, "g: int") || !strings.Contains(l, "big: []int = []int len: 3000") {
		t.Errorf("scopes = %q", l)
	}
	if g := find(c.Items, "Globals"); g == nil || !g.HasChildren || g.Expanded == nil || *g.Expanded {
		t.Errorf("Globals = %+v, want a closed expensive scope", g)
	}
	if s := find(c.Items, "Locals\x1fs"); s == nil || len([]rune(s.Label)) > maxLabelRunes+20 {
		t.Errorf("long string label = %v", s)
	}
	// 子を持つ変数を開くと、その子を読む。
	h.notifyView(ext.MethodViewExpand, ext.ViewExpandParams{View: p.id, Item: "Locals\x1fbig", Expanded: true, Child: secScopes})
	c = h.waitUpdate(secScopes, func(c ext.ViewComponent) bool {
		b := find(c.Items, "Locals\x1fbig")
		return b != nil && len(b.Children) == 3000
	}, nil)
	if b := find(c.Items, "Locals\x1fbig"); b.Children[2999].Label != "[2999]: int = 2999" {
		t.Errorf("big[2999] = %q", b.Children[2999].Label)
	}
	// ステップ実行で動いている間は実行中にし、止まり直すと新しい値を出す。開いた変数は開いたまま読み直す。
	h.mustExec(cmdNext, 0)
	h.waitLabel(secScopes, "実行中")
	c = h.waitLabel(secScopes, "x: int = 2")
	if b := find(c.Items, "Locals\x1fbig"); b == nil || len(b.Children) != 3000 {
		t.Error("the expanded variable was not read again after the stop")
	}
	h.stopAndWait()
	h.waitLabel(secScopes, "デバッグは終わった")
}

// TestPaneStaleVariables は、variables の応答を待つ間にステップ実行して止まり直したら、遅れて届いた前の停止の応答を表示に使わないことを確かめる。
//
// req: FR-305
func TestPaneStaleVariables(t *testing.T) {
	h := paneHost(t, nil, map[string]any{"variablesDelayMs": 500}, map[string]any{"defaultSection": "scopes"})
	h.toggleUI()
	h.waitPane()
	h.mustExec(cmdStart, 0)
	h.waitOutput("variables requested")
	h.mustExec(cmdNext, 0)
	var seen []ext.ViewComponent
	h.waitUpdate(secScopes, func(c ext.ViewComponent) bool { return strings.Contains(labels(c.Items), "x: int = 2") }, &seen)
	for _, c := range seen {
		if strings.Contains(labels(c.Items), "x: int = 1") {
			t.Errorf("the stale variables were shown: %q", labels(c.Items))
		}
	}
	h.stopAndWait()
}

// TestPaneSetVariable は、スコープの区画の s で、今の値を初めの中身にして値を聞き、setVariable（evaluateName のある変数で、
// アダプターが setExpression に対応すれば setExpression）で書き換えて出し直すこと、どちらにも対応しないアダプターでは
// request を送らずに知らせることを確かめる。
//
// req: FR-289, FR-304
func TestPaneSetVariable(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"setVariable", nil, "setVariable x=42 ref=1"},
		{"setExpression", []string{"-set-expression"}, "setExpression x=42 ref=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := paneHost(t, tc.args, nil, map[string]any{"defaultSection": "scopes"})
			h.toggleUI()
			p := h.waitPane()
			h.mustExec(cmdStart, 0)
			h.waitLabel(secScopes, "x: int = 1")
			h.key(p.id, secScopes, keyScopesSetValue, "Locals\x1fx")
			ask := <-h.inputAsks
			if ask.Title != "x = " || ask.Default != "1" {
				t.Errorf("input/ask = %+v", ask)
			}
			h.inputAnswers <- ext.InputAskResult{Value: "42"}
			h.waitOutput(tc.want)
			h.waitLabel(secScopes, "x: int = 42")
			h.stopAndWait()
		})
	}
	h := paneHost(t, []string{"-no-set-variable"}, nil, map[string]any{"defaultSection": "scopes"})
	h.toggleUI()
	p := h.waitPane()
	h.mustExec(cmdStart, 0)
	h.waitLabel(secScopes, "x: int = 1")
	h.key(p.id, secScopes, keyScopesSetValue, "Locals\x1fx")
	h.waitMessage("変数の値の書き換えに対応していない")
	select {
	case a := <-h.inputAsks:
		t.Errorf("input/ask %+v was sent", a)
	case <-time.After(100 * time.Millisecond):
	}
	h.stopAndWait()
}

// TestPaneThreads は、スレッドの区画が、止まったスレッドを開いてフレームを出し、subtle のフレームを t で出し、f の絞り込みと o の反転で
// フレームを絞り、フレームを選ぶと、そのフレームを選んで位置を開き、スコープの区画がそのフレームで出し直すことを確かめる。
//
// req: FR-290
func TestPaneThreads(t *testing.T) {
	h := paneHost(t, nil, nil, map[string]any{"defaultSection": "threads"})
	h.toggleUI()
	p := h.waitPane()
	h.mustExec(cmdStart, 0)
	c := h.waitLabel(secThreads, "main.caller")
	l := labels(c.Items)
	if !strings.Contains(l, "main（止まっている）\n  main.main  /src/app/main.go:3\n  main.caller  /src/app/main.go:3\n") || strings.Contains(l, "runtime.main") {
		t.Errorf("threads = %q", l)
	}
	if f := find(c.Items, "t1/1/1000"); f == nil || f.Highlights[0].Style != "search" || f.Preview == nil || *f.Preview.Line != 2 {
		t.Errorf("the selected frame = %+v", f)
	}
	h.key(p.id, secThreads, keyThreadsSubtle, "")
	h.waitLabel(secThreads, "runtime.main")
	h.key(p.id, secThreads, keyThreadsFilter, "")
	if ask := <-h.inputAsks; ask.Default != "" {
		t.Errorf("filter ask = %+v", ask)
	}
	h.inputAnswers <- ext.InputAskResult{Value: "caller"}
	c = h.waitLabel(secThreads, "絞り込み: caller")
	if l := labels(c.Items); strings.Contains(l, "main.main") || !strings.Contains(l, "main.caller") {
		t.Errorf("filtered = %q", l)
	}
	h.key(p.id, secThreads, keyThreadsInvert, "")
	c = h.waitLabel(secThreads, "絞り込み（含まないもの）: caller")
	if l := labels(c.Items); !strings.Contains(l, "main.main") || strings.Contains(l, "main.caller") {
		t.Errorf("inverted = %q", l)
	}
	h.drainOpens()
	h.notifyView(ext.MethodViewSelect, ext.ViewSelectParams{View: p.id, Item: "t1/1/1001", Child: secThreads})
	if o := h.waitOpen(); o.Position == nil || o.Position.Line != 0 {
		t.Errorf("editor/open = %+v", o)
	}
	if sel := h.d.currentSelection(); !sel.hasFrame || sel.frame != 1001 {
		t.Errorf("selection = %+v", sel)
	}
	// スコープの区画は、見えていない間は作らず、選んだときに選んだフレームで作る。
	h.notifyView(ext.MethodViewChildSelect, ext.ViewChildSelectParams{View: p.id, Child: secScopes})
	h.waitOutput("scopes 1001")
	h.stopAndWait()
}

// TestPaneSessions は、セッションが 2 つあるとき、スレッドの区画がセッションの項目を出し、セッションかスレッドを開くと
// そのセッションを今のセッションにし（FR-303）、出力の区画の ]s で出すセッションを替えられることを確かめる。
//
// req: FR-290, FR-291, FR-303
func TestPaneSessions(t *testing.T) {
	h := connectFromChild(t, map[string]any{"runForever": true}, map[string]any{"runForever": true})
	h.toggleUI()
	p := h.waitPane()
	h.mustExec(cmdStart, 0)
	ss := h.waitSessions(2)
	h.waitHalted(ss[0])
	h.waitHalted(ss[1])
	h.notifyView(ext.MethodViewChildSelect, ext.ViewChildSelectParams{View: p.id, Child: secThreads})
	h.waitUpdate(secThreads, func(c ext.ViewComponent) bool {
		l := labels(c.Items)
		return strings.Contains(l, "parent（止まっている）") && strings.Contains(l, "Subprocess 1（止まっている）")
	}, nil)
	other := ss[0]
	if h.current() == ss[0] {
		other = ss[1]
	}
	h.notifyView(ext.MethodViewExpand, ext.ViewExpandParams{View: p.id, Item: sessionItemID(other), Expanded: true, Child: secThreads})
	h.waitCurrent(other)
	back := ss[0]
	if other == ss[0] {
		back = ss[1]
	}
	h.notifyView(ext.MethodViewExpand, ext.ViewExpandParams{View: p.id, Item: threadItemID(back, 1), Expanded: true, Child: secThreads})
	h.waitCurrent(back)
	if sel := h.d.currentSelection(); !sel.hasThread || sel.thread != 1 || !sel.hasFrame || sel.frame != 1000 {
		t.Errorf("selection after opening the thread = %+v", sel)
	}
	h.key(p.id, secConsole, keyConsoleNext, "")
	h.waitMessage("出力の区画: ")
	h.stopAndWait()
}

// TestPaneBreakpoints は、ブレークポイントの区画が、すべてのブレークポイントをパスと行で出し、選ぶとその位置を開き、d で消すことを確かめる。
//
// req: FR-295
func TestPaneBreakpoints(t *testing.T) {
	h := paneHost(t, nil, map[string]any{"unverified": []int{6}}, map[string]any{"defaultSection": "breakpoints"})
	h.mustExec(cmdToggleBreakpoint, 7)
	h.invoke(1, ext.OpBreakpointConditional, 7, `{"text":"i > 3"}`)
	h.toggleUI()
	p := h.waitPane()
	c := childOf(t, p.params.Component, secBreakpoints)
	if l := labels(c.Items); l != "/src/app/main.go:3\n/src/app/main.go:6\n/src/app/main.go:8  if i > 3\n" {
		t.Errorf("breakpoints = %q", l)
	}
	if it := c.Items[0]; it.File == nil || it.File.Path != srcPath || it.Preview == nil || *it.Preview.Line != 2 {
		t.Errorf("item = %+v", it)
	}
	h.mustExec(cmdStart, 0)
	h.waitOpen() // 止まった位置へ移るもの
	h.waitLabel(secBreakpoints, "main.go:6  （確かめられていない）")
	h.drainOpens()
	h.notifyView(ext.MethodViewSelect, ext.ViewSelectParams{View: p.id, Item: c.Items[1].ID, Child: secBreakpoints})
	if o := h.waitOpen(); o.Position.Line != 5 {
		t.Errorf("editor/open = %+v", o)
	}
	h.key(p.id, secBreakpoints, keyBreakpointsDel, c.Items[0].ID)
	h.waitUpdate(secBreakpoints, func(c ext.ViewComponent) bool { return !strings.Contains(labels(c.Items), "main.go:3\n") }, nil)
	h.waitOutput("setBreakpoints main.go [6 8]")
	h.stopAndWait()
}

// TestPaneOutput は、出力の区画が、ペインを閉じている間の出力と停止も含めて、開き直したときに最新を出し（写しの中身）、
// \r\n と \r を \n にし、stderr に色を付け、important をメッセージの行にも出し、telemetry を出さず、
// デバッグが終わった後も、次に始めるまで読めることを確かめる。
//
// req: FR-108, FR-291
func TestPaneOutput(t *testing.T) {
	h := paneHost(t, nil, map[string]any{"extraOutput": true}, map[string]any{"defaultSection": "scopes"})
	h.toggleUI()
	p := h.waitPane()
	// 利用者がペインを閉じた。
	h.notifyView(ext.MethodViewClosed, ext.ViewClosedParams{View: p.id})
	h.mustExec(cmdStart, 0)
	h.waitMessage("important note")
	h.waitOutput("important note")
	h.waitSigns(bp2, bp5, exec2)
	h.toggleUI()
	p = h.waitPane()
	console := childOf(t, p.params.Component, secConsole)
	const want = "hello from debuggee\nerr line\nsplit\nprogress 1\nprogress 2\nimportant note\n"
	if !strings.Contains(console.Text, want) || strings.Contains(console.Text, "telemetry") || strings.Contains(console.Text, "\r") {
		t.Errorf("console = %q, want %q", console.Text, want)
	}
	hasErr := false
	for _, d := range console.Highlights {
		hasErr = hasErr || d.Style == "diagnostic.error"
	}
	if !hasErr || !console.Tail {
		t.Errorf("console highlights = %+v, tail = %v", console.Highlights, console.Tail)
	}
	// 閉じている間の停止も、開き直したら出す（選んでいた scopes を作る）。
	if l := labels(childOf(t, p.params.Component, secScopes).Items); !strings.Contains(l, "x: int = 1") {
		t.Errorf("scopes after reopening = %q", l)
	}
	h.stopAndWait()
	h.notifyView(ext.MethodViewClosed, ext.ViewClosedParams{View: p.id})
	h.toggleUI()
	p = h.waitPane()
	if console := childOf(t, p.params.Component, secConsole); !strings.Contains(console.Text, want) {
		t.Errorf("console after the end = %q", console.Text)
	}
	if l := labels(childOf(t, p.params.Component, secThreads).Items); !strings.Contains(l, "デバッグは終わった") {
		t.Errorf("threads after the end = %q", l)
	}
}

// TestPaneAutoOpenClose は、ui.autoOpen と ui.autoClose で、デバッグを始めたときにペインを開き、すべてのセッションが終わったら閉じることを確かめる。
//
// req: FR-277
func TestPaneAutoOpenClose(t *testing.T) {
	h := paneHost(t, nil, nil, map[string]any{"autoOpen": true, "autoClose": true})
	h.mustExec(cmdStart, 0)
	p := h.waitPane()
	h.waitSigns(bp2, bp5, exec2)
	h.stopAndWait()
	select {
	case id := <-h.closes:
		if id != p.id {
			t.Errorf("view/close %d, want %d", id, p.id)
		}
	case <-time.After(testTimeout):
		t.Fatal("view/close did not arrive")
	}
}
