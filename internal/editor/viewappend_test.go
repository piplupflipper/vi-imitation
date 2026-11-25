package editor

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
)

// openTestText は、拡張機能 fake が開いたテキストの部品として、text を中身にした中央のポップアップを開く。イベントループの goroutine で呼ぶ。
func openTestText(e *Editor, text string, hs []ext.Decoration) *floatState {
	f := e.newComponentFloat(ext.ViewComponent{Kind: ext.ViewText, Text: text, Highlights: hs}, "fake")
	f.center = true
	e.openFloat(f)
	return f
}

func appendTo(e *Editor, f *floatState, text string, hs ...ext.ViewSpan) {
	e.viewAppend(&exthost.ViewAppend{Extension: "fake", Params: ext.ViewAppendParams{View: ext.ViewID(f.id), Text: text, Highlights: hs}})
}

// popupDecs は、部品のバッファの色の範囲を "行:始め-終わり:色" の列にする。
func popupDecs(f *floatState) []string {
	var out []string
	for _, d := range f.bs.decorations[popupDecorationOwner].decorations {
		out = append(out, fmt.Sprintf("%d:%d-%d:%s", d.Range.Start.Line, d.Range.Start.Byte, d.Range.End.Byte, d.Style))
	}
	return out
}

// view/append は、テキストの部品の末尾へ書き足す。改行で終わっていない最後の行には続け、改行で終わっていれば新しい行から始める。
// カーソルが最後の行にあれば末尾に追従し、上の行にあればカーソルを動かさない。書き足しはアンドゥの履歴に積まない。
// 子を指す書き足し、テキストでない部品、知らない部品への書き足しは捨てる。
//
// req: FR-272
func TestViewAppend(t *testing.T) {
	h := start(t, Config{})
	h.e.do(func() {
		e := h.e
		f := openTestText(e, "a\nb", nil)
		appendTo(e, f, "c\nd\n", ext.ViewSpan{Start: 0, End: 3, Style: "comment"})
		if got := f.bs.buf.Text(); got != "a\nbc\nd" || !f.textEOL {
			t.Fatalf("after append: %q eol %v", got, f.textEOL)
		}
		if got, want := popupDecs(f), []string{"1:1-2:comment", "2:0-1:comment"}; !slices.Equal(got, want) {
			t.Errorf("decorations = %v, want %v", got, want)
		}
		// カーソルは先頭の行（最後の行より上）にあるので動かさない。
		if c := f.bs.state.Cursor(); c.Line != 0 {
			t.Errorf("cursor moved to %v", c)
		}
		// 最後の行へ動かすと、書き足すたびに最後の行へ追従する。
		f.bs.state.SetCursor(buffer.Position{Line: 2})
		appendTo(e, f, "e\nf")
		if got := f.bs.buf.Text(); got != "a\nbc\nd\ne\nf" || f.textEOL {
			t.Fatalf("after second append: %q", got)
		}
		if c := f.bs.state.Cursor(); c.Line != 4 {
			t.Errorf("cursor did not follow the tail: %v", c)
		}
		appendTo(e, f, "g")
		if got := f.bs.buf.Text(); got != "a\nbc\nd\ne\nfg" {
			t.Errorf("continued line: %q", got)
		}
		if f.bs.buf.CanUndo() {
			t.Errorf("view/append was recorded in the undo history")
		}
		// 捨てるもの。
		before := f.bs.buf.Text()
		e.viewAppend(&exthost.ViewAppend{Extension: "fake", Params: ext.ViewAppendParams{View: ext.ViewID(f.id), Child: "console", Text: "x"}})
		e.viewAppend(&exthost.ViewAppend{Extension: "other", Params: ext.ViewAppendParams{View: ext.ViewID(f.id), Text: "x"}})
		e.viewAppend(&exthost.ViewAppend{Extension: "fake", Params: ext.ViewAppendParams{View: ext.ViewID(f.id + 100), Text: "x"}})
		if got := f.bs.buf.Text(); got != before {
			t.Errorf("ignored appends changed the text: %q", got)
		}
		e.closeFloat(false)

		l := e.newComponentFloat(ext.ViewComponent{Kind: ext.ViewList, Items: []ext.ViewItem{{ID: "a", Label: "a"}}}, "fake")
		e.openFloat(l)
		appendTo(e, l, "x")
		if got := l.bs.buf.Text(); got != "a" {
			t.Errorf("append to a list: %q", got)
		}
		e.closeFloat(false)

		// 末尾の改行は 1 つだけを除くので、空の行で終わる中身にも、その後ろへ書き足す。
		g := openTestText(e, "a\n\n", nil)
		appendTo(e, g, "b")
		if got := g.bs.buf.Text(); got != "a\n\nb" {
			t.Errorf("append after an empty line: %q", got)
		}
		e.closeFloat(false)
	})
}

// 行数が上限を超えたら古い行から捨てる。上を見ているカーソルと窓の最初の行は、捨てた行数だけずらして同じ行を指し続け、
// カーソルの行を捨てたら最初の行に置く。色の範囲も同じだけずらす。1 行の上限を超えた行は文字の境目で次の行へ送る。
//
// req: FR-272
func TestViewAppendLimit(t *testing.T) {
	h := start(t, Config{})
	h.e.do(func() {
		e := h.e
		lines := make([]string, ext.ViewTextMaxLines)
		for i := range lines {
			lines[i] = fmt.Sprintf("l%d", i)
		}
		f := openTestText(e, strings.Join(lines, "\n")+"\n", []ext.Decoration{{Range: ext.Range{Start: ext.Position{Line: 6}, End: ext.Position{Line: 6, Byte: 2}}, Style: "comment"}})
		f.bs.state.SetCursor(buffer.Position{Line: 5, Byte: 1})
		appendTo(e, f, "x\ny\n")
		b := f.bs.buf
		if n := b.LineCount(); n != ext.ViewTextMaxLines || b.Line(0) != "l2" || b.Line(n-1) != "y" {
			t.Fatalf("after overflow: %d lines, first %q last %q", n, b.Line(0), b.Line(n-1))
		}
		if c := f.bs.state.Cursor(); c != (buffer.Position{Line: 3, Byte: 1}) {
			t.Errorf("cursor = %v, want line 3 (same line l5)", c)
		}
		if got := popupDecs(f); !slices.Equal(got, []string{"4:0-2:comment"}) {
			t.Errorf("decorations = %v", got)
		}
		f.bs.state.SetCursor(buffer.Position{Line: 1})
		appendTo(e, f, "z\nw\nv\n")
		if c := f.bs.state.Cursor(); c != (buffer.Position{}) {
			t.Errorf("cursor on a dropped line = %v, want the first line", c)
		}
		if got := popupDecs(f); !slices.Equal(got, []string{"1:0-2:comment"}) {
			t.Errorf("decorations = %v", got)
		}
		f.bs.state.SetCursor(buffer.Position{Line: b.LineCount() - 1})
		appendTo(e, f, strings.Repeat("あ", 2000)) // 6000 バイトの 1 行
		n := b.LineCount()
		if c := f.bs.state.Cursor(); c.Line != n-1 {
			t.Errorf("cursor did not follow: %v (lines %d)", c, n)
		}
		if l0, l1 := b.Line(n-2), b.Line(n-1); len(l0) != 4095 || len(l0)+len(l1) != 6000 {
			t.Errorf("long line split = %d + %d bytes", len(l0), len(l1))
		}
		e.closeFloat(false)
	})
}

// 書き足しをどう区切って送っても、本体の中身は、写し（ext/server の TextLog）と同じ手順（ext.AppendViewText と
// ext.ViewTextOverflow）で当てた中身と同じになる。閉じて開き直したときに、写しの中身が本体の出していた中身と食い違わない。
//
// req: FR-272, FR-273
func TestViewAppendMatchesModel(t *testing.T) {
	h := start(t, Config{})
	r := rand.New(rand.NewPCG(5, 6))
	pieces := []string{"a", "bc\n", "\n", "あい", strings.Repeat("x", 3000), "line\n"}
	h.e.do(func() {
		e := h.e
		f := openTestText(e, "", nil)
		lines, eol := ext.SplitViewText("")
		for range 4000 {
			var sb strings.Builder
			for range 1 + r.IntN(8) {
				sb.WriteString(pieces[r.IntN(len(pieces))])
			}
			if r.IntN(20) == 0 {
				sb.WriteString(strings.Repeat("y\n", 3000))
			}
			text := sb.String()
			appendTo(e, f, text)
			last := len(lines) - 1
			add, _, eolAfter := ext.AppendViewText(lines[last], nil, eol, text, nil)
			if !eol {
				lines = lines[:last]
			}
			lines = append(lines, add...)
			eol = eolAfter
			if d := ext.ViewTextOverflow(len(lines)); d > 0 {
				lines = lines[d:]
			}
		}
		if got, want := f.bs.buf.Text(), strings.Join(lines, "\n"); got != want || f.textEOL != eol {
			t.Errorf("editor and model differ: %d vs %d lines, eol %v vs %v", f.bs.buf.LineCount(), len(lines), f.textEOL, eol)
		}
		e.closeFloat(false)
	})
}

// 待ちの間に取っておく view/append は、上限で捨てる通知に数えず、同じ部品への続く書き足しを 1 件につなぐ。
// 間に同じ部品への view/update か view/close があれば、その後ろに新しい件を置く。
// つなげない書き足しだけで上限を超えたら、届いた書き足しを捨て、処理し終えたときに、その部品の末尾に捨てたことを表す行を足す。
//
// req: FR-272
func TestHoldViewAppend(t *testing.T) {
	h := start(t, Config{Host: exthost.Config{MaxQueuedEvents: 4}})
	h.e.do(func() {
		e := h.e
		app := func(view ext.ViewID, text string, hs ...ext.ViewSpan) *exthost.ViewAppend {
			return &exthost.ViewAppend{Extension: "fake", Params: ext.ViewAppendParams{View: view, Text: text, Highlights: hs}}
		}
		e.hold(app(1, "a"))
		e.hold(&exthost.MessageShow{Params: ext.MessageShowParams{Text: "m"}})
		e.hold(app(2, "x"))
		e.hold(app(1, "b\n", ext.ViewSpan{Start: 0, End: 1, Style: "s"})) // 間にほかの部品と通知があってもつなぐ
		e.hold(&exthost.ViewUpdate{Extension: "fake", Params: ext.ViewUpdateParams{View: 1, Component: ext.ViewComponent{Kind: ext.ViewText}}})
		e.hold(app(1, "c")) // view/update の後ろなので、新しい件。上限を超えたので最も古い通知（message/show）を捨てる
		var got []string
		for _, ev := range e.held {
			switch v := ev.(type) {
			case *exthost.ViewAppend:
				got = append(got, fmt.Sprintf("append %d %q %v", v.Params.View, v.Params.Text, v.Params.Highlights))
			default:
				got = append(got, fmt.Sprintf("%T", ev))
			}
		}
		want := []string{`append 1 "ab\n" [{1 2 s}]`, `append 2 "x" []`, "*exthost.ViewUpdate", `append 1 "c" []`}
		if !slices.Equal(got, want) {
			t.Errorf("held = %q\nwant %q", got, want)
		}
		e.held = nil

		// 捨てられる通知が無いまま、つなげない書き足しで上限を超えたら、届いた書き足しを捨てて覚える。
		f := openTestText(e, "start\n", nil)
		view := ext.ViewID(f.id)
		for i := range 3 {
			e.hold(&exthost.QueryResult{Tag: queryTag(90 + i)})
		}
		e.hold(app(view, "kept\n"))
		e.hold(app(view+1, "other"))
		if len(e.held) != 4 || !slices.Equal(e.appendLost, []appendKey{{ext: "fake", view: view + 1}}) {
			t.Fatalf("held = %d, lost = %v", len(e.held), e.appendLost)
		}
		e.hold(app(view, "more\n")) // つなげるので数えない
		e.held = e.held[3:]         // 問い合わせの結果は捨てて、書き足しだけを当てる
		for len(e.held) > 0 {
			ev := e.held[0]
			e.held = e.held[1:]
			e.handleEvent(ev)
		}
		e.appendLost = append(e.appendLost, appendKey{ext: "fake", view: view})
		e.flushAppendLost()
		if got := f.bs.buf.Text(); got != "start\nkept\nmore\n"+appendLostNote {
			t.Errorf("after flush: %q", got)
		}
		if e.appendLost != nil {
			t.Errorf("appendLost = %v", e.appendLost)
		}
		e.closeFloat(false)
	})
}
