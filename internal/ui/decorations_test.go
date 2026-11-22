package ui

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// spansOf は、d の n 行目の範囲を、[start end order style] の文字列にする。
func spansOf(d *Decorations, n int) []string {
	var out []string
	for _, sp := range d.line(n) {
		end := fmt.Sprint(sp.end)
		if sp.end == math.MaxInt {
			end = "$"
		}
		out = append(out, fmt.Sprintf("%d-%s #%d %s", sp.start, end, sp.order, d.style(sp)))
	}
	return out
}

func rng(sl, sb, el, eb int) buffer.Range { return buffer.Range{Start: pos(sl, sb), End: pos(el, eb)} }

// 行ごとの表は、並んでいない一覧も開始の順に並べ、同じ開始では元の順を保つ。行をまたぐ範囲は各行に分ける。
//
// req: FR-35
func TestNewDecorationsLines(t *testing.T) {
	d := NewDecorations([]Highlight{
		{Range: rng(1, 5, 1, 7), Style: StyleString},
		{Range: rng(1, 2, 1, 4), Style: StyleKeyword},
		{Range: rng(0, 3, 2, 1), Style: StyleComment}, // 行をまたぐ
		{Range: rng(1, 2, 1, 3), Style: StyleKeyword}, // 同じ開始は元の順
		{Range: rng(2, 4, 2, 2), Style: StyleType},    // 終わりが開始より前
		{Range: rng(2, 6, 2, 6), Style: StyleType},    // 空
		{Range: rng(2, 0, 2, 0), Style: StyleNumber},  // 空。表に名前を足さない
	}, 3)
	want := [][]string{
		{"3-$ #2 comment"},
		{"0-$ #2 comment", "2-4 #1 keyword", "2-3 #3 keyword", "5-7 #0 string"},
		{"0-1 #2 comment", "2-4 #4 type"},
	}
	for n, w := range want {
		if got := spansOf(d, n); !slices.Equal(got, w) {
			t.Errorf("line %d = %q, want %q", n, got, w)
		}
	}
	if d.line(3) != nil || d.line(-1) != nil {
		t.Error("範囲の外の行に範囲がある")
	}
	// 行をまたぐ範囲の間の行の名前を先に数える（1 回目の走査で区間を集めるため）。
	if !slices.Equal(d.styles, []decoStyle{{name: StyleComment}, {name: StyleString}, {name: StyleKeyword}, {name: StyleType}}) {
		t.Errorf("styles = %+v", d.styles)
	}
}

// 行番号がとても大きい範囲を受け取っても、表は行数の分しか作らない（NFR-03）。
// 前は終わりの行まで行ごとの表を伸ばしたので、終わりが 20 億行目の範囲 1 つで 20 億要素の確保を試みた（#75）。
//
// req: NFR-03
func TestNewDecorationsHugeLine(t *testing.T) {
	hs := []Highlight{
		{Range: rng(0, 1, 2_000_000_000, 0), Style: StyleString},
		{Range: rng(2_000_000_000, 0, 2_000_000_000, 5), Style: StyleKeyword},
		{Range: rng(math.MaxInt, 0, 1, 2), Style: StyleComment}, // 入れ替えると 1 行目から MaxInt 行目まで
		{Range: rng(math.MinInt, 0, math.MaxInt, 0), Style: StyleType},
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	d := NewDecorations(hs, 3)
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 64<<10 {
		t.Errorf("確保が %d バイト", n)
	}
	want := [][]string{
		{"0-$ #3 type", "1-$ #0 string"},
		{"0-$ #0 string", "0-$ #3 type", "2-$ #2 comment"},
		{"0-$ #0 string", "0-$ #2 comment", "0-$ #3 type"},
	}
	for n, w := range want {
		if got := spansOf(d, n); !slices.Equal(got, w) {
			t.Errorf("line %d = %q, want %q", n, got, w)
		}
	}
	if d := NewDecorations(hs, 0); d.line(0) != nil {
		t.Error("0 行のテキストに範囲がある")
	}
}

// 色の種類の名前が多くても、添字は名前ごとに 1 つで、名前を引き直せる。
func TestNewDecorationsManyStyles(t *testing.T) {
	var hs []Highlight
	for i := range 3 * smallStyles {
		hs = append(hs, Highlight{Range: rng(0, i, 0, i+1), Style: fmt.Sprint("s", i%(2*smallStyles))})
	}
	d := NewDecorations(hs, 1)
	if len(d.styles) != 2*smallStyles {
		t.Errorf("len(styles) = %d", len(d.styles))
	}
	for i, sp := range d.line(0) {
		if got, want := d.style(sp), fmt.Sprint("s", i%(2*smallStyles)); got != want {
			t.Errorf("%d: style %q, want %q", i, got, want)
		}
	}
}

// 全体にまたがる範囲が多数あっても、表は行数と範囲の数の和の大きさで済む（NFR-03）。
// 前は間の行にも 1 つずつ入れたので、この一覧で 100 億要素の表を確保しようとした（#80）。
//
// req: NFR-03
func TestNewDecorationsManyMultiline(t *testing.T) {
	const lines, ranges = 1_000_000, 10_000
	styles := []string{StyleKeyword, StyleString, StyleComment, StyleType}
	hs := make([]Highlight, ranges)
	for i := range hs {
		hs[i] = Highlight{Range: rng(i%7, 1, lines-1-i%5, 2), Style: styles[i%len(styles)]}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	d := NewDecorations(hs, lines)
	runtime.ReadMemStats(&after)
	// 行ごとの開始の位置（8 MB）と、範囲ごとの開始と終わりの行の部分と間の区間。
	if n := after.TotalAlloc - before.TotalAlloc; n > 16<<20 {
		t.Errorf("確保が %d バイト", n)
	}
	// 間の行では、色の種類ごとに最も後の範囲だけを返す。
	want := []string{"0-$ #9996 keyword", "0-$ #9997 string", "0-$ #9998 comment", "0-$ #9999 type"}
	if got := spansOf(d, lines/2); !slices.Equal(got, want) {
		t.Errorf("line %d = %q, want %q", lines/2, got, want)
	}
}

// 行の表から引いた範囲を重ねた属性は、範囲を各行に展開してすべて重ねた属性と、行のすべてのバイトで同じである。
// 展開は前の実装（#80 より前）と同じ方法で、テストの中で行う。
//
// req: FR-35
func TestNewDecorationsSameAsExpanded(t *testing.T) {
	theme := Theme{
		StyleKeyword: {Fg: Indexed(1)},
		StyleString:  {Fg: Indexed(2), Bold: true},
		StyleComment: {Bg: Indexed(3)},
		StyleType:    {Underline: true},
		StyleNumber:  {Fg: Indexed(4), Bg: Indexed(5), Italic: true},
	}
	names := []string{StyleKeyword, StyleString, StyleComment, StyleType, StyleNumber, "unknown"}
	const lines, width = 12, 10
	rnd := rand.New(rand.NewPCG(1, 80)) //nolint:gosec // テストの入力を作るだけで、暗号には使わない
	for trial := range 300 {
		hs := make([]Highlight, rnd.IntN(40))
		for i := range hs {
			hs[i] = Highlight{
				Range: rng(rnd.IntN(lines+4)-2, rnd.IntN(width+2)-1, rnd.IntN(lines+4)-2, rnd.IntN(width+2)-1),
				Style: names[rnd.IntN(len(names))],
			}
		}
		d := NewDecorations(hs, lines)
		for n := range lines {
			got := d.line(n)
			for b := range width + 1 {
				if g, w := layered(theme, d, got, b), expandedAttr(theme, hs, n, b); g != w {
					t.Fatalf("trial %d line %d byte %d: %+v, want %+v (spans %q)", trial, n, b, g, w, spansOf(d, n))
				}
			}
			if !slices.IsSortedFunc(got, compareStart) {
				t.Fatalf("trial %d line %d: 開始の順でない %q", trial, n, spansOf(d, n))
			}
		}
	}
}

// layered は、spans のうち byte b に掛かるものを、order の順に重ねた属性である。
func layered(theme Theme, d *Decorations, spans []decoSpan, b int) Attr {
	spans = slices.Clone(spans)
	slices.SortFunc(spans, func(x, y decoSpan) int { return cmp.Compare(x.order, y.order) })
	var a Attr
	for _, sp := range spans {
		if sp.start <= b && b < sp.end {
			a = a.Over(theme.attr(d.style(sp)))
		}
	}
	return a
}

// expandedAttr は、hs を n 行目に展開した範囲のうち byte b に掛かるものを、一覧の順に重ねた属性である。
func expandedAttr(theme Theme, hs []Highlight, n, b int) Attr {
	var a Attr
	for _, h := range hs {
		s, e := h.Range.Start, h.Range.End
		if e.Less(s) {
			s, e = e, s
		}
		if s == e || n < s.Line || n > e.Line {
			continue
		}
		start, end := 0, math.MaxInt
		if n == s.Line {
			start = max(s.Byte, 0)
		}
		if n == e.Line {
			end = e.Byte
		}
		if start <= b && b < end {
			a = a.Over(theme.attr(h.Style))
		}
	}
	return a
}
