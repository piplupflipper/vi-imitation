//go:build linux

package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、REPL の区画の入力の行の補完（panecomplete.go。FR-310）のテストである。

// complete は、ペイン view の REPL の区画の入力の行 text（カーソルはバイトの位置 cursor）への console/complete を送り、答えを返す。
func (h *host) complete(view ext.ViewID, child, text string, cursor int) ext.ConsoleCompleteResult {
	h.t.Helper()
	res, err := h.completeErr(h.ctx(), view, child, text, cursor)
	if err != nil {
		h.t.Fatalf("console/complete %q: %v", text, err)
	}
	return res
}

func (h *host) completeErr(ctx context.Context, view ext.ViewID, child, text string, cursor int) (ext.ConsoleCompleteResult, error) {
	var res ext.ConsoleCompleteResult
	err := h.conn.Call(ctx, ext.MethodConsoleComplete, ext.ConsoleCompleteParams{View: view, Child: child, Text: text, Cursor: cursor, Seq: 1}, &res)
	return res, err
}

// itemsString は、候補を「label|text|kind|start-end」の行にする。
func itemsString(items []ext.ConsoleCompletionItem) string {
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%s|%s|%s|%d-%d\n", it.Label, it.Text, it.Kind, it.Start, it.End)
	}
	return b.String()
}

func intp(n int) *int { return &n }

// TestReplCompletionItems は、completions の候補を本体の候補にするときの、並び、入れる文字列、種類、置き換える範囲（start と length の
// UTF-16 の符号単位からバイトへの換算、start も length も無い候補の語の範囲）を確かめる。
//
// req: FR-310
func TestReplCompletionItems(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		targets      []completionItem
		want         string
	}{
		{
			"start と length、sortText の並び", "obj.al",
			[]completionItem{
				{Label: "alpha", Type: "field", SortText: "b", Start: intp(4), Length: intp(2)},
				{Label: "also", Text: "also()", Type: "method", SortText: "a", Start: intp(4), Length: intp(2)},
				{Label: "alright", Start: intp(4), Length: intp(2)},
			},
			// 並べる鍵は a、b、alright（sortText が無ければ label）。
			"also|also()|method|4-6\nalright|alright||4-6\nalpha|alpha|field|4-6\n",
		},
		{
			// 😀 は UTF-8 で 4 バイト、UTF-16 で 2 符号単位。カーソルまでは 6 符号単位で、a は 5 番目（0 から）。
			"BMP の外の文字の後ろの start", "😀+o.a",
			[]completionItem{{Label: "abc", Start: intp(5), Length: intp(1)}},
			"abc|abc||7-8\n",
		},
		{
			"length だけ（カーソルから前へ数える）", "あlen",
			[]completionItem{{Label: "lengthy", Length: intp(3)}},
			"lengthy|lengthy||3-6\n",
		},
		{
			"start がカーソルより後ろの候補は捨てる", "ab",
			[]completionItem{{Label: "zz", Start: intp(3)}, {Label: "abc", Start: intp(0), Length: intp(2)}},
			"abc|abc||0-2\n",
		},
		{
			"start が文字の途中（サロゲートペアの間）なら文字の先頭に寄せる", "😀x",
			[]completionItem{{Label: "q", Start: intp(1)}},
			"q|q||0-5\n",
		},
		{
			"start も length も無い: 入力の行の先頭から候補に含まれれば先頭、ほかは語", "p.pr",
			[]completionItem{{Label: "p.prop"}, {Label: "prop2"}},
			"p.prop|p.prop||0-4\nprop2|prop2||2-4\n",
		},
		{
			"start も length も無い: 空白の手前までしか戻らない", "x = obj.at",
			[]completionItem{{Label: "obj.attr"}, {Label: "= obj.attr"}},
			"= obj.attr|= obj.attr||8-10\nobj.attr|obj.attr||4-10\n",
		},
		{
			"start も length も無い: 多バイト文字の語", "名前",
			[]completionItem{{Label: "名前空間"}},
			"名前空間|名前空間||0-6\n",
		},
		{
			"start も length も無い: 語の外（. の後ろ）はカーソルに挿す", "obj.",
			[]completionItem{{Label: "attr"}},
			"attr|attr||4-4\n",
		},
		{
			"入れる文字列と範囲の同じ候補は 1 つにする", "o.__c",
			[]completionItem{
				{Label: "__class__", Type: "class", Start: intp(2), Length: intp(3)},
				{Label: "__class__", Type: "class", Start: intp(2), Length: intp(3)},
				{Label: "__class__", Text: "__class__", Type: "field", Start: intp(2), Length: intp(3)},
				{Label: "__class__", Start: intp(4), Length: intp(1)},
			},
			"__class__|__class__|class|2-5\n__class__|__class__||4-5\n",
		},
		{
			"label の無い候補は捨て、label と type の改行は空白にする", "",
			[]completionItem{{Label: ""}, {Label: "a\nb", Type: "x\ny"}},
			"a b|a\nb|x y|0-0\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := replCompletionItems(tc.prefix, tc.targets)
			if got := itemsString(items); got != tc.want {
				t.Errorf("items = %q, want %q", got, tc.want)
			}
			for _, it := range items {
				if err := it.Validate(tc.prefix, len(tc.prefix)); err != nil {
					t.Errorf("%q: %v", it.Label, err)
				}
			}
		})
	}
}

// TestPaneReplComplete は、REPL の区画を補完ありで開き、止まっている間の console/complete に、選んでいるフレームに対する completions の
// 候補（カーソルまでの入力の行と UTF-16 の column で問い合わせる）で答え、デバッグしていないとき、ほかの部品、失敗したときは ok を
// false にすることを確かめる。
//
// req: FR-310
func TestPaneReplComplete(t *testing.T) {
	h := paneHost(t, []string{"-completions"}, nil, map[string]any{"defaultSection": "repl"})
	h.toggleUI()
	p := h.waitPane()
	if c := childOf(t, p.params.Component, secRepl); !c.Complete {
		t.Errorf("repl is not opened with complete: %+v", c)
	}
	if res := h.complete(p.id, secRepl, "obj.a", 5); res.OK {
		t.Errorf("before debugging = %+v", res)
	}
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, bp5, exec2)

	res := h.complete(p.id, secRepl, "obj.a", 5)
	if want := "apple|apple()|method|4-5\nalpha|alpha|field|4-5\n"; !res.OK || itemsString(res.Items) != want {
		t.Errorf("obj.a = %v %q, want %q", res.OK, itemsString(res.Items), want)
	}
	h.waitOutput("completions obj.a col=6 frame=1000")

	// カーソルの後ろは送らず、column は UTF-16 の符号単位で数える（😀 は 2）。
	text := "😀+o.b + rest"
	res = h.complete(p.id, secRepl, text, len("😀+o.b"))
	if want := "beta|beta|variable|7-8\n"; !res.OK || itemsString(res.Items) != want {
		t.Errorf("%q = %v %q, want %q", text, res.OK, itemsString(res.Items), want)
	}
	h.waitOutput("completions 😀+o.b col=7 frame=1000")

	// 候補が 0 件なら ok で空。失敗、ほかの子、ほかの部品、カーソルが文字の途中なら ok が false。
	if none := h.complete(p.id, secRepl, "nothing", 7); !none.OK || len(none.Items) != 0 {
		t.Errorf("no candidates = %+v", none)
	}
	for _, tc := range []struct {
		view          ext.ViewID
		child, text   string
		cursor        int
		wantAdapterIn string
	}{
		{p.id, secRepl, "fail", 4, "completions fail col=5"},
		{p.id, secWatches, "obj.a", 5, ""},
		{p.id + 100, secRepl, "obj.a", 5, ""},
		{p.id, secRepl, "あ", 1, ""},
		{p.id, secRepl, "a", 2, ""},
	} {
		if got := h.complete(tc.view, tc.child, tc.text, tc.cursor); got.OK {
			t.Errorf("%+v = %+v, want not ok", tc, got)
		}
		if tc.wantAdapterIn != "" {
			h.waitOutput(tc.wantAdapterIn)
		}
	}

	// 数千個の候補もすべて返す。
	res = h.complete(p.id, secRepl, "many", 4)
	if !res.OK || len(res.Items) != 5000 || res.Items[0].Label != "m0000" || res.Items[4999].Start != 0 {
		t.Errorf("many = %v %d", res.OK, len(res.Items))
	}
	h.stopAndWait()

	// 動いている間は送らない。
	h = paneHost(t, []string{"-completions"}, map[string]any{"idle": true}, map[string]any{"defaultSection": "repl"})
	h.toggleUI()
	p = h.waitPane()
	h.mustExec(cmdStart, 0)
	h.outputsUntil("hello from debuggee")
	if res := h.complete(p.id, secRepl, "obj.a", 5); res.OK {
		t.Errorf("running = %+v", res)
	}
	h.submit(p.id, "x")
	if out := h.outputsUntil("evaluate repl x frame=none"); strings.Contains(out, "completions") {
		t.Errorf("completions was sent while running: %q", out)
	}
	h.stopAndWait()
}

// TestPaneReplCompleteUnsupported は、アダプターが supportsCompletionsRequest を示さなければ、completions を送らずに ok を false にすることを確かめる。
//
// req: FR-310
func TestPaneReplCompleteUnsupported(t *testing.T) {
	h := paneHost(t, nil, nil, map[string]any{"defaultSection": "repl"})
	h.toggleUI()
	p := h.waitPane()
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, bp5, exec2)
	if res := h.complete(p.id, secRepl, "obj.a", 5); res.OK || len(res.Items) != 0 {
		t.Errorf("unsupported = %+v", res)
	}
	h.submit(p.id, "x")
	if out := h.outputsUntil("evaluate repl x frame=1000"); strings.Contains(out, "completions") {
		t.Errorf("completions was sent to an adapter without supportsCompletionsRequest: %q", out)
	}
	h.stopAndWait()
}

// TestPaneReplCompleteSlow は、遅い completions の応答を待つ間に届いた問い合わせのうち、最後のものだけをアダプターへ送り、間のものと、
// 応答の前に新しい問い合わせが来た古いものは ok を false にすること、本体が取り消した問い合わせは誤りで答えること、
// 応答の前にステップ実行して止まった状態が変わったら候補を捨てることを確かめる。
//
// req: FR-310
func TestPaneReplCompleteSlow(t *testing.T) {
	h := paneHost(t, []string{"-completions"}, map[string]any{"completionsDelayMs": 300}, map[string]any{"defaultSection": "repl"})
	h.toggleUI()
	p := h.waitPane()
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, bp5, exec2)

	type answer struct {
		res ext.ConsoleCompleteResult
		err error
	}
	send := func(ctx context.Context, text string) chan answer {
		ch := make(chan answer, 1)
		go func() {
			res, err := h.completeErr(ctx, p.id, secRepl, text, len(text))
			ch <- answer{res, err}
		}()
		return ch
	}
	recv := func(ch chan answer) answer {
		t.Helper()
		select {
		case a := <-ch:
			return a
		case <-time.After(testTimeout):
			t.Fatal("console/complete did not answer")
			return answer{}
		}
	}
	first := send(h.ctx(), "a")
	h.waitOutput("completions requested")
	// 送ってある間に届いた問い合わせは待つ。次が届けば、待っていたものは送らずに答える。
	cctx, cancel := context.WithCancel(context.Background())
	cancelled := send(cctx, "al")
	time.Sleep(50 * time.Millisecond)
	cancel()
	middle := send(h.ctx(), "alp")
	time.Sleep(50 * time.Millisecond)
	last := send(h.ctx(), "b")
	if a := recv(cancelled); a.err == nil {
		t.Errorf("cancelled = %+v, want an error", a)
	}
	if a := recv(middle); a.err != nil || a.res.OK {
		t.Errorf("middle = %+v, want not ok", a)
	}
	if a := recv(first); a.err != nil || a.res.OK {
		t.Errorf("first (superseded while waiting for the adapter) = %+v, want not ok", a)
	}
	a := recv(last)
	if want := "beta|beta|variable|0-1\n"; a.err != nil || !a.res.OK || itemsString(a.res.Items) != want {
		t.Errorf("last = %+v, want %q", a, want)
	}
	out := h.outputsUntil("completions b col=2")
	var sent []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "completions ") && !strings.HasSuffix(line, "requested") {
			sent = append(sent, strings.Fields(line)[1])
		}
	}
	if !slices.Equal(sent, []string{"b"}) {
		// first は waitOutput が読み捨てた後なので、ここには b だけが並ぶ。
		t.Errorf("completions sent after the first = %q, want [b]", sent)
	}

	// 応答の前にステップ実行して止まり直したら、前の停止の候補を出さない。
	stale := send(h.ctx(), "a")
	h.waitOutput("completions requested")
	h.mustExec(cmdNext, 0)
	if a := recv(stale); a.err != nil || a.res.OK {
		t.Errorf("stale = %+v, want not ok", a)
	}
	h.stopAndWait()
}
