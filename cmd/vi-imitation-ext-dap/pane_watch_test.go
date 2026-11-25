//go:build linux

package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、ウォッチ式、REPL、例外の区画（panewatches.go、panerepl.go、exceptions.go。FR-292〜FR-294、FR-309）のテストである。

// addWatch は、ウォッチ式の区画の項目 item で、キー key（a か i か e）を打ち、問いに expr を答える。問いの初めの中身を返す。
func (h *host) addWatch(view ext.ViewID, key, item, expr string) string {
	h.t.Helper()
	h.key(view, secWatches, key, item)
	var ask ext.InputAskParams
	select {
	case ask = <-h.inputAsks:
	case <-time.After(testTimeout):
		h.t.Fatalf("input/ask for %s did not arrive", key)
	}
	if ask.Title != "式: " {
		h.t.Errorf("input/ask title = %q", ask.Title)
	}
	h.inputAnswers <- ext.InputAskResult{Value: expr}
	return ask.Default
}

// waitRepl は、REPL の区画への view/append を、つないだテキストが want を含むまで待ち、つないだテキストを返す。
func (h *host) waitRepl(want string) string {
	h.t.Helper()
	var all strings.Builder
	deadline := time.After(testTimeout)
	for {
		select {
		case a := <-h.appends:
			if a.Child != secRepl {
				continue
			}
			all.WriteString(a.Text)
			if strings.Contains(all.String(), want) {
				return all.String()
			}
		case <-deadline:
			h.t.Fatalf("view/append of %q to the REPL did not arrive; got %q", want, all.String())
			return ""
		}
	}
}

// submit は、REPL の区画の入力の行で text を送ったことを知らせる。
func (h *host) submit(view ext.ViewID, text string) {
	h.notifyView(ext.MethodViewSelect, ext.ViewSelectParams{View: view, Item: text, Child: secRepl})
}

// TestPaneWatches は、ウォッチ式の区画で、式を足し（a）、差し込み（i）、書き換え（e）、消し（d）、止まるたびと、フレームを選び直したときに
// 選んでいるフレームで evaluate（context は watch）し直し、構造を持つ結果を開いて子を 100 個ずつ読み、評価の誤りを式の行に出し、
// 動いている間は前の木を残し、式がデバッグをまたいで残ることを確かめる。
//
// req: FR-292
func TestPaneWatches(t *testing.T) {
	h := paneHost(t, nil, nil, nil)
	h.toggleUI()
	p := h.waitPane()
	if l := labels(childOf(t, p.params.Component, secWatches).Items); l != "式が無い（a で足す）\n" {
		t.Errorf("watches before adding = %q", l)
	}
	// デバッグの前でも式を足せる。
	h.addWatch(p.id, keyWatchesAppend, "none", "x")
	c := h.waitLabel(secWatches, "x  （デバッグしていない）")
	if c.Cursor != "w1" {
		t.Errorf("cursor after adding = %q", c.Cursor)
	}
	h.addWatch(p.id, keyWatchesAppend, "w1", "big")
	h.waitLabel(secWatches, "big  （デバッグしていない）")
	h.addWatch(p.id, keyWatchesAppend, "w2", "bad")
	h.waitLabel(secWatches, "bad  （デバッグしていない）")
	// i は、カーソルの式の前に差し込む（子の項目なら、その式の前）。
	h.addWatch(p.id, keyWatchesInsert, "w2\x1f[0]", "nested")
	c = h.waitLabel(secWatches, "nested  （デバッグしていない）")
	if l := labels(c.Items); l != "x  （デバッグしていない）\nnested  （デバッグしていない）\nbig  （デバッグしていない）\nbad  （デバッグしていない）\n" {
		t.Errorf("watches after inserting = %q", l)
	}
	// 同じ式は足さない。
	h.addWatch(p.id, keyWatchesAppend, "w1", "big")
	h.waitMessage("式 big は既にある")

	h.mustExec(cmdStart, 0)
	c = h.waitLabel(secWatches, "x = 1")
	l := labels(c.Items)
	if !strings.HasPrefix(l, "x = 1\nnested = {...}\n  next = depth 1\nbig = []int len: 3000\n  [0]: int = 0\n") ||
		!strings.Contains(l, "\n  more: 残り 2900 個\nbad = could not evaluate bad\n") {
		t.Errorf("watches after the stop = %q", l)
	}
	if b := find(c.Items, "w3"); b == nil || b.Highlights[len(b.Highlights)-1].Style != "diagnostic.error" {
		t.Errorf("the error item = %+v", b)
	}
	h.waitOutput("evaluate watch x frame=1000")
	// 開いた子の続き（more）は、スコープの区画と同じく 100 個ずつ読む。
	h.notifyView(ext.MethodViewExpand, ext.ViewExpandParams{View: p.id, Item: "w2\x1f!more100", Expanded: true, Child: secWatches})
	h.waitLabel(secWatches, "more: 残り 2800 個")
	// [[ で式の行へ移る。
	h.key(p.id, secWatches, keyWatchesParent, "w2\x1f[150]")
	h.waitUpdate(secWatches, func(c ext.ViewComponent) bool { return c.Cursor == "w2" }, nil)

	// ステップ実行で動いている間は前の木を残し、止まり直すと評価し直す。
	h.mustExec(cmdNext, 0)
	h.waitLabel(secWatches, "x = 2")

	// e は、今の式を初めの中身にして聞き、書き換える。子の項目では書き換えない。
	if def := h.addWatch(p.id, keyWatchesEdit, "w1", "multi"); def != "x" {
		t.Errorf("edit default = %q", def)
	}
	h.waitLabel(secWatches, "multi = line1 line2")
	h.key(p.id, secWatches, keyWatchesEdit, "w2\x1f[0]")
	h.waitMessage("カーソルの項目は式でない")
	// d は、式を消し、カーソルを次の式に置く。
	h.key(p.id, secWatches, keyWatchesDelete, "w2")
	c = h.waitUpdate(secWatches, func(c ext.ViewComponent) bool { return !strings.Contains(labels(c.Items), "big") }, nil)
	if c.Cursor != "w3" {
		t.Errorf("cursor after deleting = %q", c.Cursor)
	}

	// フレームを選び直すと、そのフレームで評価し直す（y は呼んだ側のフレームでだけ評価できる）。
	h.addWatch(p.id, keyWatchesAppend, "w4", "y")
	h.waitLabel(secWatches, "y = could not evaluate y")
	h.notifyView(ext.MethodViewChildSelect, ext.ViewChildSelectParams{View: p.id, Child: secThreads})
	h.waitLabel(secThreads, "main.caller")
	h.notifyView(ext.MethodViewSelect, ext.ViewSelectParams{View: p.id, Item: "t1/1/#2", Child: secThreads})
	h.notifyView(ext.MethodViewChildSelect, ext.ViewChildSelectParams{View: p.id, Child: secWatches})
	h.waitLabel(secWatches, "y = 7")
	h.waitOutput("evaluate watch y frame=1002")

	// 終わった後も式は残り、次のデバッグで評価する。
	h.stopAndWait()
	h.waitLabel(secWatches, "multi  （デバッグは終わった）")
	h.mustExec(cmdStart, 0)
	h.waitLabel(secWatches, "multi = line1 line2")
	h.stopAndWait()
}

// TestPaneWatchesRunning は、一度も止まっていないセッションでは式を評価せず、動いている間は evaluate を送らないことを確かめる。
//
// req: FR-292
func TestPaneWatchesRunning(t *testing.T) {
	h := paneHost(t, nil, map[string]any{"idle": true}, nil)
	h.toggleUI()
	p := h.waitPane()
	h.addWatch(p.id, keyWatchesAppend, "none", "x")
	h.waitLabel(secWatches, "x  （デバッグしていない）")
	h.mustExec(cmdStart, 0)
	c := h.waitLabel(secWatches, "x  （止まったら評価する）")
	if c.Items[0].ID != runningID {
		t.Errorf("watches while running = %q", labels(c.Items))
	}
	if all := h.outputsUntil("hello from debuggee"); strings.Contains(all, "evaluate") {
		t.Errorf("evaluate was sent while running: %q", all)
	}
	h.stopAndWait()
}

// TestPaneWatchesHidden は、ウォッチ式の区画が見えていない間は、止まっても evaluate を送らず（スレッドの区画も読まない）、
// 区画に切り替えた時点で最新の停止に対して評価することと、動いている間に切り替えたときは、前の停止の値を出さずに式だけを出し、
// 止まったら最新の停止で評価することを確かめる（#424 の裁定）。
//
// req: FR-292
func TestPaneWatchesHidden(t *testing.T) {
	h := paneHost(t, nil, map[string]any{"stopDelayMs": 600}, nil)
	h.toggleUI()
	p := h.waitPane()
	h.addWatch(p.id, keyWatchesAppend, "none", "x")
	h.waitLabel(secWatches, "x  （デバッグしていない）")
	h.notifyView(ext.MethodViewChildSelect, ext.ViewChildSelectParams{View: p.id, Child: secScopes})
	h.mustExec(cmdStart, 0)
	h.waitLabel(secScopes, "x: int = 1")
	h.mustExec(cmdNext, 0)
	h.waitLabel(secScopes, "x: int = 2")
	all := h.outputsUntil("stopped 2", "variables 1")
	if strings.Contains(all, "evaluate") || strings.Contains(all, "threads") || strings.Contains(all, "levels=200") {
		t.Errorf("a hidden section was read: %q", all)
	}
	// 止まっている間に切り替えると、まず読み直していることを出し（前の停止の値は出さない）、最新の停止（2 回目）で評価する。
	h.notifyView(ext.MethodViewChildSelect, ext.ViewChildSelectParams{View: p.id, Child: secWatches})
	var first []ext.ViewComponent
	h.waitUpdate(secWatches, func(c ext.ViewComponent) bool { return strings.Contains(labels(c.Items), "x = 2") }, &first)
	if l := labels(first[0].Items); !strings.Contains(l, "読み直しています") {
		t.Errorf("the first update after switching = %q", l)
	}
	// 見えていない間に止まり直してから、動いている間に切り替えると、前の停止の値（x = 2）を出さない。
	h.notifyView(ext.MethodViewChildSelect, ext.ViewChildSelectParams{View: p.id, Child: secScopes})
	h.mustExec(cmdNext, 0)
	h.waitLabel(secScopes, "x: int = 3")
	h.mustExec(cmdNext, 0)
	h.waitLabel(secScopes, runningLabel)
	h.notifyView(ext.MethodViewChildSelect, ext.ViewChildSelectParams{View: p.id, Child: secWatches})
	var seen []ext.ViewComponent
	h.waitUpdate(secWatches, func(c ext.ViewComponent) bool { return strings.Contains(labels(c.Items), "x = 4") }, &seen)
	for _, c := range seen {
		if l := labels(c.Items); strings.Contains(l, "x = 2") || strings.Contains(l, "x = 3") {
			t.Errorf("a value of an older stop was shown: %q", l)
		}
	}
	h.stopAndWait()
}

// TestPaneWatchesQuickStep は、動き出してから runningShowDelay の間に止まるステップ実行では、ウォッチ式とスコープの区画に
// 「動いている」の中身を送らず、止まった後の中身だけを送ることと、それより長く動くときは送ることを確かめる。
//
// req: FR-292
func TestPaneWatchesQuickStep(t *testing.T) {
	for _, tc := range []struct {
		delay   int
		running bool
	}{{30, false}, {600, true}} {
		h := paneHost(t, nil, map[string]any{"stopDelayMs": tc.delay}, nil)
		h.toggleUI()
		p := h.waitPane()
		h.addWatch(p.id, keyWatchesAppend, "none", "x")
		h.mustExec(cmdStart, 0)
		h.waitLabel(secWatches, "x = 1")
		h.mustExec(cmdNext, 0)
		var seen []ext.ViewComponent
		h.waitUpdate(secWatches, func(c ext.ViewComponent) bool { return strings.Contains(labels(c.Items), "x = 2") }, &seen)
		got := slices.ContainsFunc(seen, func(c ext.ViewComponent) bool { return strings.Contains(labels(c.Items), runningLabel) })
		if got != tc.running {
			t.Errorf("stop after %d ms: running shown = %v, want %v", tc.delay, got, tc.running)
		}
		h.stopAndWait()
	}
}

// TestPaneWatchesStale は、evaluate の応答を待つ間にステップ実行して止まり直したら、遅れて届いた前の停止の結果を表示に使わないことを確かめる。
//
// req: FR-305
func TestPaneWatchesStale(t *testing.T) {
	h := paneHost(t, nil, map[string]any{"evaluateDelayMs": 500}, nil)
	h.toggleUI()
	p := h.waitPane()
	h.addWatch(p.id, keyWatchesAppend, "none", "x")
	h.waitLabel(secWatches, "x  （デバッグしていない）")
	h.mustExec(cmdStart, 0)
	h.waitOutput("evaluate requested")
	h.mustExec(cmdNext, 0)
	var seen []ext.ViewComponent
	h.waitUpdate(secWatches, func(c ext.ViewComponent) bool { return strings.Contains(labels(c.Items), "x = 2") }, &seen)
	for _, c := range seen {
		if strings.Contains(labels(c.Items), "x = 1") {
			t.Errorf("the stale result was shown: %q", labels(c.Items))
		}
	}
	h.stopAndWait()
}

// TestPaneWatchesSetCopy は、ウォッチ式の区画の s で、式を setExpression で書き換え（対応しないアダプターでは request を送らずに知らせる）、
// c で、clipboard の context の評価の結果（対応しなければ表示している値）を、ヤンクできるようポップアップに出すことを確かめる。
//
// req: FR-292, FR-309
func TestPaneWatchesSetCopy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		set      bool
		copyWant string
	}{
		{"supported", []string{"-set-expression", "-clipboard"}, true, "clip:42"},
		{"unsupported", nil, false, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := paneHost(t, tc.args, nil, nil)
			h.toggleUI()
			p := h.waitPane()
			h.addWatch(p.id, keyWatchesAppend, "none", "x")
			h.waitLabel(secWatches, "x  （デバッグしていない）")
			// デバッグしていないときは、動いている間ではなく、デバッグしていないことを知らせる。
			h.key(p.id, secWatches, keyWatchesCopy, "w1")
			h.waitMessage("デバッグしていないので、値を読めない")
			h.key(p.id, secWatches, keyWatchesSet, "w1")
			h.waitMessage("デバッグしていないので、値を書き換えられない")
			h.addWatch(p.id, keyWatchesAppend, "w1", "bad")
			h.mustExec(cmdStart, 0)
			h.waitLabel(secWatches, "x = 1")
			// 評価の誤りの行の c は、誤りの全文を出す。
			h.key(p.id, secWatches, keyWatchesCopy, "w2")
			select {
			case pp := <-h.popups:
				if pp.Component.Text != "could not evaluate bad" || !strings.Contains(pp.Component.Title, "bad の評価の誤り") {
					t.Errorf("popup/open for the error = %+v", pp)
				}
			case <-time.After(testTimeout):
				t.Fatal("popup/open for the error did not arrive")
			}
			h.key(p.id, secWatches, keyWatchesSet, "w1")
			if tc.set {
				ask := <-h.inputAsks
				if ask.Title != "x = " || ask.Default != "1" {
					t.Errorf("input/ask = %+v", ask)
				}
				h.inputAnswers <- ext.InputAskResult{Value: "42"}
				h.waitOutput("setExpression x=42")
				h.waitLabel(secWatches, "x = 42")
			} else {
				h.waitMessage("式の値の書き換え（setExpression）に対応していない")
				select {
				case a := <-h.inputAsks:
					t.Errorf("input/ask %+v was sent", a)
				case <-time.After(100 * time.Millisecond):
				}
			}
			h.key(p.id, secWatches, keyWatchesCopy, "w1")
			select {
			case pp := <-h.popups:
				if pp.Component.Kind != ext.ViewText || pp.Component.Text != tc.copyWant || pp.Placement != ext.PopupCenter ||
					!strings.Contains(pp.Component.Title, "0y$ で文字単位にヤンク") {
					t.Errorf("popup/open = %+v", pp)
				}
			case <-time.After(testTimeout):
				t.Fatal("popup/open did not arrive")
			}
			h.stopAndWait()
		})
	}
}

// TestPaneRepl は、REPL の区画が、打った式を選んでいるフレームで evaluate（context は repl）し、式と結果（複数行、構造を持つ結果の子、
// 誤り）を書き足し、空の入力で前の式を評価し直し、デバッグしていないときはそのことを書き、止まっていないときはフレームを付けずに評価し、
// 開き直したときに出力と入力の履歴を渡すことを確かめる。
//
// req: FR-293
func TestPaneRepl(t *testing.T) {
	h := paneHost(t, nil, nil, map[string]any{"defaultSection": "repl"})
	h.toggleUI()
	p := h.waitPane()
	if c := childOf(t, p.params.Component, secRepl); c.Kind != ext.ViewConsole || c.Prompt != replPrompt {
		t.Errorf("repl = %+v", c)
	}
	h.submit(p.id, "x")
	h.waitRepl("> x\nデバッグしていない\n")
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, bp5, exec2)
	h.submit(p.id, "x")
	h.waitRepl("> x\n1\n")
	h.waitOutput("evaluate repl x frame=1000")
	h.submit(p.id, "multi")
	h.submit(p.id, "bad")
	h.submit(p.id, "big")
	all := h.waitRepl("…（ほかに 2900 個")
	if !strings.Contains(all, "> multi\nline1\nline2\n> bad\ncould not evaluate bad\n> big\n[]int len: 3000\n  [0]: int = 0\n") ||
		!strings.Contains(all, "  [99]: int = 99\n  …") {
		t.Errorf("repl output = %q", all)
	}
	// 代入と、空の入力での評価し直し。
	h.submit(p.id, "x = 5")
	h.submit(p.id, "x")
	h.submit(p.id, "")
	h.waitRepl("> x = 5\n5\n> x\n5\n> x\n5\n")
	// 閉じて開き直すと、出力と履歴が戻る。
	h.notifyView(ext.MethodViewClosed, ext.ViewClosedParams{View: p.id})
	h.toggleUI()
	p = h.waitPane()
	c := childOf(t, p.params.Component, secRepl)
	if !slices.Equal(c.History, []string{"x", "multi", "bad", "big", "x = 5", "x"}) || !strings.Contains(c.Text, "> x = 5\n5\n") {
		t.Errorf("reopened repl = history %q, text %q", c.History, c.Text)
	}
	h.stopAndWait()

	// 止まっていないときは、フレームを付けずに評価する。
	h = paneHost(t, nil, map[string]any{"idle": true}, map[string]any{"defaultSection": "repl"})
	h.toggleUI()
	p = h.waitPane()
	h.mustExec(cmdStart, 0)
	h.outputsUntil("hello from debuggee")
	h.submit(p.id, "x")
	h.waitOutput("evaluate repl x frame=none")
	h.stopAndWait()
}

// TestPaneReplStale は、前の評価の応答を待つ間に打った式を、打った時点の停止に対して評価し、その間にステップ実行して停止が無くなったら、
// 動いているデバッグ対象で評価せずに、そのことを書き足すことを確かめる（PR #425 のレビューの推奨 2）。
//
// req: FR-293, FR-305
func TestPaneReplStale(t *testing.T) {
	h := paneHost(t, nil, map[string]any{"evaluateDelayMs": 400, "evaluateDelayRepl": true, "stopDelayMs": 1500}, map[string]any{"defaultSection": "repl"})
	h.toggleUI()
	p := h.waitPane()
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, bp5, exec2)
	h.submit(p.id, "multi")
	h.submit(p.id, "x")
	h.waitOutput("evaluate requested")
	h.mustExec(cmdNext, 0)
	all := h.waitRepl("> x\n（止まっていない。打ったときの停止はもう無いので、評価しなかった）\n")
	if !strings.Contains(all, "> multi\n（止まった状態が変わったので、結果を出さない）\n") {
		t.Errorf("repl = %q", all)
	}
	if out := h.outputsUntil("stopped 2"); strings.Contains(out, "evaluate repl x") {
		t.Errorf("x was evaluated after the stop was gone: %q", out)
	}
	h.stopAndWait()
}

// TestPaneExceptions は、例外の区画が、アダプターの例外のフィルターを [x] と [ ] で出し、既定はアダプターの default に従って
// 開始の setExceptionBreakpoints で送り、選ぶと切り替えて送り、切り替えた状態を次のデバッグの開始でも送ることと、
// フィルターを示さないアダプターではそのことを出して送らないことを確かめる。
//
// req: FR-294, FR-309
func TestPaneExceptions(t *testing.T) {
	h := paneHost(t, nil, nil, map[string]any{"defaultSection": "exceptions"})
	h.toggleUI()
	p := h.waitPane()
	if l := labels(childOf(t, p.params.Component, secExceptions).Items); !strings.Contains(l, "デバッグを始めると") {
		t.Errorf("exceptions before the start = %q", l)
	}
	h.mustExec(cmdStart, 0)
	all := h.outputsUntil("hello from debuggee")
	if i, j := strings.Index(all, "setExceptionBreakpoints [panic]"), strings.Index(all, "setBreakpoints main.go"); i < 0 || i < j {
		t.Errorf("setExceptionBreakpoints was not sent after setBreakpoints: %q", all)
	}
	h.waitLabel(secExceptions, "[x] Unrecovered Panics\n[ ] All Exceptions  every throw\n")
	h.notifyView(ext.MethodViewSelect, ext.ViewSelectParams{View: p.id, Item: exceptionItemID("all"), Child: secExceptions})
	h.waitOutput("setExceptionBreakpoints [panic all]")
	h.waitLabel(secExceptions, "[x] All Exceptions")
	h.stopAndWait()
	// セッションが無い間も切り替えられ、次の開始で送る。
	h.waitLabel(secExceptions, "次のデバッグの開始で送る（fake）")
	h.notifyView(ext.MethodViewSelect, ext.ViewSelectParams{View: p.id, Item: exceptionItemID("panic"), Child: secExceptions})
	h.waitLabel(secExceptions, "[ ] Unrecovered Panics")
	h.mustExec(cmdStart, 0)
	h.waitOutput("setExceptionBreakpoints [all]")
	h.stopAndWait()

	// 動いている間に断られたら（dlv）、次に止まったときに送り直す。
	h = paneHost(t, nil, map[string]any{"stopDelayMs": 1500}, map[string]any{"defaultSection": "exceptions"})
	h.toggleUI()
	p = h.waitPane()
	h.mustExec(cmdStart, 0)
	h.waitLabel(secExceptions, "[x] Unrecovered Panics")
	h.waitSigns(bp2, bp5, exec2)
	h.mustExec(cmdNext, 0)
	h.notifyView(ext.MethodViewSelect, ext.ViewSelectParams{View: p.id, Item: exceptionItemID("all"), Child: secExceptions})
	h.waitMessage("動いている間は例外のフィルターを送れなかった（parent）。次に止まったときに送る")
	all = h.outputsUntil("stopped 2", "setExceptionBreakpoints [panic all]")
	if strings.Index(all, "setExceptionBreakpoints [panic all]") < strings.Index(all, "stopped 2") {
		t.Errorf("the filters were sent before the stop: %q", all)
	}
	h.stopAndWait()

	h = paneHost(t, []string{"-no-exceptions"}, nil, map[string]any{"defaultSection": "exceptions"})
	h.toggleUI()
	h.waitPane()
	h.mustExec(cmdStart, 0)
	h.waitLabel(secExceptions, "このアダプターに例外のフィルターはありません")
	if all := h.outputsUntil("hello from debuggee"); strings.Contains(all, "setExceptionBreakpoints") {
		t.Errorf("setExceptionBreakpoints was sent to an adapter without filters: %q", all)
	}
	h.stopAndWait()
}

// TestExceptionInfo は、例外で止まったとき、止まった位置を出した後に exceptionInfo を読み、メッセージの行に要旨を、REPL に型と
// メッセージとスタックトレースと内側の例外を出すことと、exceptionInfo に対応しないアダプターには送らず、stopped の text を出すことを確かめる。
//
// req: FR-309
func TestExceptionInfo(t *testing.T) {
	h := paneHost(t, nil, map[string]any{"exceptionOnContinue": true}, map[string]any{"defaultSection": "repl"})
	h.toggleUI()
	h.waitPane()
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, bp5, exec2)
	h.mustExec(cmdContinue, 0)
	h.waitMessage("例外で止まった（panic、unhandled: boom）")
	all := h.waitRepl("inner.Error: cause")
	if !strings.Contains(all, "例外で止まった（parent）: panic、unhandled: boom\n  runtime.Error: boom\n  main.main()\n  \tmain.go:6\n  内側の例外:\n    inner.Error: cause\n") {
		t.Errorf("repl = %q", all)
	}
	h.stopAndWait()

	h = paneHost(t, []string{"-no-exception-info"}, map[string]any{"exceptionOnContinue": true}, nil)
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, bp5, exec2)
	h.mustExec(cmdContinue, 0)
	h.waitMessage("例外で止まった（panic from the stopped event）")
	if all := h.outputsUntil("stopped 2"); strings.Contains(all, "exceptionInfo") {
		t.Errorf("exceptionInfo was sent: %q", all)
	}
	h.stopAndWait()
}
