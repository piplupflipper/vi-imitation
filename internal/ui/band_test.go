package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// bandText は、"line 1" から "line n" までの n 行のバッファである。
func bandText(n int) *buffer.Buffer {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line %d\n", i+1)
	}
	return buffer.New(b.String())
}

// 帯を表示領域の上の端から描く。印の列と行番号の列は band.linenr で塗って行の番号を描き、本文の列は band で塗って範囲の色を重ね、
// 最後の行に band.bottom を重ねる。帯はカーソルの行を隠さないよう、上の行から削る（nvim-treesitter-context と同じ）。
//
// req: FR-237
func TestRenderBand(t *testing.T) {
	theme := Theme{
		StyleBand: {Bg: Indexed(8)}, StyleBandLineNr: {Fg: Indexed(11)}, StyleBandBottom: {Underline: true},
		StyleKeyword: {Fg: Indexed(3)},
	}
	band := &Band{Lines: []BandLine{
		{Text: "func outer() {", Line: 0, Spans: []BandSpan{{Start: 0, End: 4, Style: StyleKeyword}}},
		{Text: "\tif x {", Line: 1},
		{Text: "for {", Line: -1},
	}}
	render := func(cursor int, f *Frame) Screen {
		r := &Renderer{Width: 20, Height: 10, Theme: theme}
		f.Text, f.Cursor, f.Band = bandText(30), buffer.Position{Line: cursor}, band
		f.TabStop = 4
		f.LineNumbers = LineNumbers{Show: true, MinWidth: 4}
		return r.Render(f)
	}
	s := render(5, &Frame{})
	got := rowTexts(s, 5)
	want := []string{
		"  1 func outer() {",
		"  2     if x {",
		"    for {",
		"  4 line 4",
		"  5 line 5",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("画面 =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if a := s.Grid.At(0, 2).Attr; a != theme[StyleBandLineNr] {
		t.Errorf("行番号の列の色 = %+v", a)
	}
	if a := s.Grid.At(0, 4).Attr; a != theme[StyleBand].Over(theme[StyleKeyword]) {
		t.Errorf("範囲の色 = %+v", a)
	}
	if a := s.Grid.At(0, 10).Attr; a != theme[StyleBand] {
		t.Errorf("本文の列の色 = %+v", a)
	}
	if a := s.Grid.At(2, 19).Attr; a != theme[StyleBand].Over(theme[StyleBandBottom]) {
		t.Errorf("最後の行の右の端の色 = %+v", a)
	}
	if a := s.Grid.At(2, 0).Attr; a != theme[StyleBandLineNr].Over(theme[StyleBandBottom]) {
		t.Errorf("最後の行の印の列の色 = %+v", a)
	}

	// カーソルが表示領域の 2 行目なら、帯は 2 行で、上の行から削る。最初の行なら描かない。
	s = render(2, &Frame{})
	if got := rowTexts(s, 3); got[0] != "  2     if x {" || got[1] != "    for {" || got[2] != "  3 line 3" {
		t.Errorf("2 行に削った帯 = %q", got)
	}
	s = render(0, &Frame{})
	if got := rowTexts(s, 1); got[0] != "  1 line 1" {
		t.Errorf("カーソルが最初の行の帯 = %q", got)
	}

	// 行を折り返さない窓で横にスクロールしていれば、帯も同じ列から描く。
	r := &Renderer{Width: 20, Height: 10, Theme: theme}
	long := buffer.New(strings.Repeat("x", 60) + "\n" + strings.Repeat("y", 60) + "\n")
	s = r.Render(&Frame{Text: long, Cursor: buffer.Position{Line: 1, Byte: 50}, Band: &Band{Lines: []BandLine{{Text: "0123456789abcdefghijklmnopqrstuvwxyz0123456789", Line: 0}}}})
	if r.Left() == 0 {
		t.Fatal("横にスクロールしていない")
	}
	want0 := "0123456789abcdefghijklmnopqrstuvwxyz0123456789"[r.Left():]
	if got := rowTexts(s, 1)[0]; got != want0[:min(len(want0), 20)] {
		t.Errorf("横にスクロールした帯 = %q, want %q（left %d）", got, want0, r.Left())
	}
}

// band と band.linenr は、既定の対応にも配色にも無ければ、popup と linenr の今の色を引き継ぐ（nvim-treesitter-context の
// TreesitterContext と TreesitterContextLineNumber のリンクと同じ）。配色が popup と linenr だけを変えても、帯の色が付いてくる。
// 本体の既定の対応でも、帯の本文の列は本文と見分けられる背景を持つ。
//
// req: FR-237
func TestBandColorLinks(t *testing.T) {
	def := DefaultTheme()
	if _, ok := def[StyleBand]; ok {
		t.Fatal("既定の対応に band がある（popup を引き継がない）")
	}
	if got := def.attr(StyleBand); got != def[StylePopup] || got.Bg == ColorDefault {
		t.Errorf("既定の band = %+v, want popup の %+v（背景あり）", got, def[StylePopup])
	}
	if got := def.attr(StyleBandLineNr); got != def[StyleLineNr] {
		t.Errorf("既定の band.linenr = %+v, want linenr の %+v", got, def[StyleLineNr])
	}
	scheme := def.WithColors(map[string]Attr{StylePopup: {Bg: RGB(0x3d, 0x42, 0x5b)}, StyleLineNr: {Fg: RGB(0x44, 0x4b, 0x71)}})
	if got := scheme.attr(StyleBand); got != (Attr{Bg: RGB(0x3d, 0x42, 0x5b)}) {
		t.Errorf("配色の popup を引き継いだ band = %+v", got)
	}
	if got := scheme.attr(StyleBandLineNr); got != (Attr{Fg: RGB(0x44, 0x4b, 0x71)}) {
		t.Errorf("配色の linenr を引き継いだ band.linenr = %+v", got)
	}
	// band を書けば、引き継がずにその色を使う。
	if got := scheme.WithColors(map[string]Attr{StyleBand: {}}).attr(StyleBand); got != (Attr{}) {
		t.Errorf("書いた band = %+v", got)
	}
}

// 帯は、スクロールバーと進み具合の下に描き、ポップアップの窓の中の Frame には描かない（layers.go の表の 2.7）。
//
// req: FR-239
func TestBandLayers(t *testing.T) {
	theme := Theme{StyleBand: {Bg: Indexed(8)}, StyleScrollbar: {Reverse: true}}
	band := &Band{Lines: []BandLine{{Text: "AAAA", Line: 0}, {Text: "BBBB", Line: 1}, {Text: "CCCC", Line: 2}}}
	r := &Renderer{Width: 20, Height: 6, Theme: theme}
	// 表示領域は 4 行で、カーソルは 4 行目なので帯は 3 行。進み具合は下から 2 行（3 行目と 4 行目）に出る。
	s := r.Render(&Frame{
		Text: bandText(100), Cursor: buffer.Position{Line: 3}, Band: band, Scrollbar: &Scrollbar{},
		Progress: []Progress{{Title: "p1", Percentage: -1}, {Title: "p2", Percentage: -1}},
	})
	got := rowTexts(s, 4)
	// 帯の 3 行目の右に、進み具合が重なる。
	if !strings.HasPrefix(got[0], "AAAA") || !strings.HasPrefix(got[1], "BBBB") || got[2] != "CCCC         p1 ..." {
		t.Errorf("進み具合との重なり = %q", got)
	}
	if a := s.Grid.At(0, 19).Attr; !a.Reverse {
		t.Errorf("帯の上のスクロールバーのつまみの色 = %+v", a)
	}
	// ポップアップの窓の中の Frame の帯は描かない。
	r = &Renderer{Width: 30, Height: 10, Theme: theme}
	s = r.Render(&Frame{Text: bandText(5), Float: &Float{Center: true, View: &Frame{
		Text: buffer.New("one\ntwo\nthree\n"), Cursor: buffer.Position{Line: 2}, Band: band,
	}}})
	if strings.Contains(s.Grid.String(), "AAAA") {
		t.Errorf("ポップアップの中に帯を描いた:\n%s", s.Grid.String())
	}
}

// 帯の、バッファの行に当たる行を押すと HitBand で、帯の行と、帯の下の本文の位置を返す。行の無い帯の行、作った後に本文が変わった帯の行、
// 本文へ寄せて解くとき（clamp が HitText）は本文である。スクロールバーのつまみと浮かべた窓は帯の上に重なる。
//
// req: FR-238
func TestHitBand(t *testing.T) {
	r := &Renderer{Width: 20, Height: 10}
	f := &Frame{
		Text: bandText(100), Cursor: buffer.Position{Line: 50},
		Band: &Band{Lines: []BandLine{
			{Text: "func a() {", Line: 3},
			{Text: "sep", Line: -1},
			{Text: "if {", Line: 7, Stale: true},
		}},
		Scrollbar: &Scrollbar{},
	}
	r.Render(f)
	top := r.Top()
	if h := r.HitTest(0, 2, HitNone); h.Area != HitBand || h.BandLine != 3 || h.Line != top || h.Col != 2 {
		t.Errorf("帯の 1 行目 = %+v（最初の行 %d）", h, top)
	}
	if h := r.HitTest(1, 2, HitNone); h.Area != HitText || h.Line != top+1 {
		t.Errorf("行の無い帯の行 = %+v", h)
	}
	if h := r.HitTest(2, 2, HitNone); h.Area != HitText || h.Line != top+2 {
		t.Errorf("本文が変わった帯の行 = %+v", h)
	}
	if h := r.HitTest(0, 2, HitText); h.Area != HitText {
		t.Errorf("本文へ寄せて解く = %+v", h)
	}
	if h := r.HitTest(3, 2, HitNone); h.Area != HitText || h.Line != top+3 {
		t.Errorf("帯の下 = %+v", h)
	}
	// つまみは表示領域の真ん中にあるので、帯の行に重なるつまみを作るために、最初の行の近くへ動かす。
	f.Cursor = buffer.Position{Line: 5}
	r.SetScroll(0, 0)
	r.Render(f)
	if h := r.HitTest(0, 19, HitNone); h.Area != HitScrollbar {
		t.Errorf("帯の上のつまみ = %+v", h)
	}
	if h := r.HitTest(0, 2, HitNone); h.Area != HitBand || h.BandLine != 3 {
		t.Errorf("つまみの左の帯 = %+v", h)
	}
}
