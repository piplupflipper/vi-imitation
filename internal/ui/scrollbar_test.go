package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// scrollbarColumn は、画面の表示領域の rows 行の、右端の列の文字を上から並べた文字列を返す。
func scrollbarColumn(s Screen, rows int) string {
	var b strings.Builder
	x := s.Grid.Width() - 1
	for i := range rows {
		b.WriteString(s.Grid.At(i, x).Text)
	}
	return b.String()
}

// thumbRows は、表示領域の rows 行のうち、右端の列の属性につまみの背景の色 bg が付いた行を、"#" と "." で並べた文字列を返す。
func thumbRows(s Screen, rows int, bg Color) string {
	var b strings.Builder
	x := s.Grid.Width() - 1
	for i := range rows {
		if s.Grid.At(i, x).Attr.Bg == bg {
			b.WriteByte('#')
		} else {
			b.WriteByte('.')
		}
	}
	return b.String()
}

// スクロールバーは、表示領域に収まらないときだけ、右端の列に、見えている行の範囲を本文の行で割り付けたつまみを描く。
// つまみのセルは下の文字を残し、背景の色だけを替える。全角文字の右半分に当たるときは、その文字を空白にする。
// 本文が 1 列しかない画面と、Frame.Scrollbar が nil のときは描かない。行を折り返して画面より高い 1 行も、収まらないとして描く。
//
// req: FR-186
func TestRenderScrollbarThumb(t *testing.T) {
	theme := Theme{StyleScrollbar: {Bg: Indexed(8)}}
	bg := Indexed(8)
	var lines strings.Builder
	for range 100 {
		lines.WriteString("0123456789\n")
	}
	text := buffer.New(lines.String())
	r := &Renderer{Width: 10, Height: 12, Theme: theme}
	render := func(line int) Screen {
		return r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: line}, Scrollbar: &Scrollbar{}})
	}

	// 先頭の 10 行（100 行のうち）は、つまみが最初の 1 行になる。下の文字は残す。
	s := render(0)
	if got := thumbRows(s, 10, bg); got != "#........." {
		t.Errorf("先頭のつまみ = %s", got)
	}
	if c := s.Grid.At(0, 9); c.Text != "9" || c.Attr != theme[StyleScrollbar] {
		t.Errorf("つまみのセル = %+v", c)
	}
	// 50 行目へ跳ぶと、Vim と同じくカーソルの行を中ほどに置き、46 行目から 55 行目（0 から数える）を見る。
	// つまみは 46×10÷100 = 4 行目から、56×10÷100 の切り上げの 6 行目の手前まで。
	if got := thumbRows(render(50), 10, bg); got != "....##...." || r.Top() != 46 {
		t.Errorf("中ほどのつまみ = %s（最初の行 %d）", got, r.Top())
	}
	// 最後の行まで見えていれば、表示領域の最後の行まで伸ばす。
	if got := thumbRows(render(99), 10, bg); got != ".........#" {
		t.Errorf("最後のつまみ = %s", got)
	}
	// Scrollbar が nil なら描かない。
	s = r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: 99}})
	if got := thumbRows(s, 10, bg); got != ".........." {
		t.Errorf("nil のとき = %s", got)
	}

	// 表示領域に収まるバッファには描かない。
	short := &Renderer{Width: 10, Height: 12, Theme: theme}
	s = short.Render(&Frame{Text: buffer.New("a\nb\nc\n"), Scrollbar: &Scrollbar{}})
	if got := thumbRows(s, 10, bg); got != ".........." {
		t.Errorf("収まるバッファ = %s", got)
	}

	// 本文の幅が 1 列しかなければ描かない。
	narrow := &Renderer{Width: 1, Height: 12, Theme: theme}
	s = narrow.Render(&Frame{Text: text, Scrollbar: &Scrollbar{}})
	if got := thumbRows(s, 10, bg); got != ".........." {
		t.Errorf("1 列の本文 = %s", got)
	}

	// 全角文字の右半分に当たるつまみは、その文字を空白にする。
	wide := &Renderer{Width: 10, Height: 12, Theme: theme}
	s = wide.Render(&Frame{Text: buffer.New("あいうえお\n" + lines.String()), Scrollbar: &Scrollbar{}})
	if got := strings.TrimRight(s.Grid.RowText(0), " "); got != "あいうえ" {
		t.Errorf("全角の行 = %q", got)
	}
	if c := s.Grid.At(0, 9); c.Text != " " || c.Width != 1 || c.Attr.Bg != bg {
		t.Errorf("全角文字の右半分のつまみ = %+v", c)
	}

	// 行を折り返して表示領域より高い 1 行も、収まらないとして描く（最後の行を描ききれない）。
	tall := &Renderer{Width: 10, Height: 6, Theme: theme}
	s = tall.Render(&Frame{Text: buffer.New(strings.Repeat("x", 100) + "\n"), Wrap: true, Scrollbar: &Scrollbar{}})
	if got := thumbRows(s, 4, bg); got != "####" {
		t.Errorf("画面より高い 1 行 = %s", got)
	}
}

// スクロールバーの印の色と記号の名前は、本体の種類に加えて、"scrollbar." の後に scrollbar.marks に書ける形の名前が続くものを、
// 決まった表に頼らずに受け付ける（後に拡張機能が足す種類）。受け付けた名前の色と記号で、その種類の印を描ける。
// 本体の種類と同じか似た名前（編集距離が 2 以下）で本体の表に無い名前は、書き間違いとして受け付けない。
//
// req: FR-187
func TestScrollbarMarkNames(t *testing.T) {
	for name, want := range map[string]bool{
		"scrollbar.git": true, "scrollbar.diagnostic.error": true, "scrollbar": true, "scrollbar.": false,
		"scrollbar.a,b": false, "scrollbar.a b": false, "scrollbarx": false, "gitsigns.add": false,
		// 本体の種類（diagnostic、search）と同じか似た名前で、本体の表に無いものは書き間違いとして受け付けない。
		"scrollbar.diagnostics.error": false, "scrollbar.diagnostic.err": false, "scrollbar.serch": false, "scrollbar.saerch": false,
		"scrollbar.search.x": false, "scrollbar.todo": true, "scrollbar.mark": true, "scrollbar.stash": true,
	} {
		if got := IsStyleName(name); got != want {
			t.Errorf("IsStyleName(%q) = %v, want %v", name, got, want)
		}
	}
	if err := CheckSymbol("scrollbar.git", "+"); err != nil {
		t.Errorf("CheckSymbol(scrollbar.git) = %v", err)
	}
	if err := CheckSymbol("scrollbar.git", "++"); err == nil {
		t.Error("2 文字の記号を受け付けた")
	}
	if err := CheckSymbol("scrollbar.", "+"); err == nil {
		t.Error("種類の名前の無い記号を受け付けた")
	}
	if err := CheckSymbol("scrollbar.diagnostics.error", "X"); err == nil {
		t.Error("本体の種類の書き間違いの記号を受け付けた")
	}

	var lines strings.Builder
	for range 100 {
		lines.WriteString("0123456789\n")
	}
	theme := Theme{"scrollbar.git": {Fg: Indexed(2)}}
	r := &Renderer{Width: 10, Height: 12, Theme: theme, Symbols: Symbols{"scrollbar.git": "+"}}
	sb := &Scrollbar{Marks: []ScrollbarMarks{{Lines: []int{95}, Priority: 1, Style: "scrollbar.git", Symbol: "scrollbar.git"}}}
	s := r.Render(&Frame{Text: buffer.New(lines.String()), Scrollbar: sb})
	if c := s.Grid.At(9, 9); c.Text != "+" || c.Attr != theme["scrollbar.git"] {
		t.Errorf("拡張機能の種類の印 = %+v", c)
	}
}

// 描いたスクロールバーのつまみと印のセルは、HitTest が HitScrollbar として解き、そのセルに割り付けた行の範囲、つまみの上か、
// 描いた印の種類（優先で選んだもの）とその種類の範囲の中の行を返す。つまみも印も無いセルと、描いていないとき
// （Scrollbar が nil、表示領域に収まる）は、その列もテキストである。
//
// req: FR-189
func TestHitTestScrollbar(t *testing.T) {
	var lines strings.Builder
	for range 100 {
		lines.WriteString("0123456789\n")
	}
	text := buffer.New(lines.String())
	sb := &Scrollbar{Marks: []ScrollbarMarks{
		{Lines: []int{5, 52}, Priority: 1, Style: StyleScrollbarSearch, Symbol: SymbolScrollbarSearch},
		{Lines: []int{3, 7, 12}, Priority: 2, Style: StyleScrollbarDiagnosticError, Symbol: SymbolScrollbarDiagnosticError},
	}}
	r := &Renderer{Width: 10, Height: 12}
	r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: 50}, Scrollbar: sb}) // つまみは 4 行目と 5 行目
	h := r.HitTest(5, 9, HitNone)
	want := Hit{Area: HitScrollbar, Tab: -1, Line: 50, LineEnd: 60, Thumb: true, Mark: 0, MarkFirst: 52, MarkLast: 52, Row: 5, ThumbRow: 4}
	if h != want {
		t.Errorf("つまみの上の印 = %+v", h)
	}
	want = Hit{Area: HitScrollbar, Tab: -1, Line: 40, LineEnd: 50, Thumb: true, Mark: -1, MarkFirst: -1, MarkLast: -1, Row: 4, ThumbRow: 4}
	if h = r.HitTest(4, 9, HitNone); h != want {
		t.Errorf("印の無いつまみ = %+v", h)
	}
	// 0 行目は、優先の大きい error の印を描いたセルで、その種類の 0 行目から 9 行目の行を返す。
	want = Hit{Area: HitScrollbar, Tab: -1, Line: 0, LineEnd: 10, Mark: 1, MarkFirst: 3, MarkLast: 7, ThumbRow: 4}
	if h = r.HitTest(0, 9, HitNone); h != want {
		t.Errorf("つまみの外の印 = %+v", h)
	}
	// つまみも印も無いセルは、本文の最後の列である。
	if h = r.HitTest(7, 9, HitNone); h.Area != HitText || h.Line != 53 || h.Col != 9 {
		t.Errorf("何も描いていないセル = %+v", h)
	}
	if h = r.HitTest(5, 8, HitNone); h.Area != HitText || h.Line != 51 {
		t.Errorf("スクロールバーの左の列 = %+v", h)
	}
	// ドラッグ（テキストへ寄せて解く）の間は、スクロールバーの列もテキストである。
	if h = r.HitTest(5, 9, HitText); h.Area != HitText {
		t.Errorf("テキストへ寄せたとき = %+v", h)
	}
	// ステータス行はスクロールバーにしない。
	if h = r.HitTest(10, 9, HitNone); h.Area != HitStatusLine {
		t.Errorf("ステータス行 = %+v", h)
	}

	r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: 50}})
	if h = r.HitTest(5, 9, HitNone); h.Area != HitText {
		t.Errorf("描いていないとき = %+v", h)
	}
	short := &Renderer{Width: 10, Height: 12}
	short.Render(&Frame{Text: buffer.New("abc\n"), Scrollbar: &Scrollbar{}})
	if h = short.HitTest(0, 9, HitNone); h.Area != HitText {
		t.Errorf("収まるバッファ = %+v", h)
	}
}

// つまみのドラッグ（HitTest の clamp が HitScrollbar）の間は、列に依らずスクロールバーの行として解き、表示領域の外の行もそのまま返す。
// ScrollbarTop は、つまみの最初の行を置く表示領域の行から、nvim-scrollview の simple の数え方（round(i×(n−1)÷(h−1))）で
// 表示領域の最初の行を返し、範囲の外の行は端の行へ寄せる。ScrollbarMarkLines は、そのセルに描いた印の種類の、割り付けた範囲の行を返す。
//
// req: FR-210, FR-211
func TestScrollbarDragAndMarks(t *testing.T) {
	var lines strings.Builder
	for range 100 {
		lines.WriteString("0123456789\n")
	}
	text := buffer.New(lines.String())
	sb := &Scrollbar{Marks: []ScrollbarMarks{
		{Lines: []int{5, 52}, Priority: 1, Style: StyleScrollbarSearch, Symbol: SymbolScrollbarSearch},
		{Lines: []int{3, 7, 7, 12}, Priority: 2, Style: StyleScrollbarDiagnosticError, Symbol: SymbolScrollbarDiagnosticError},
	}}
	r := &Renderer{Width: 10, Height: 12}
	if _, _, ok := r.ScrollbarTop(0); ok {
		t.Errorf("描く前の ScrollbarTop が ok")
	}
	r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: 50}, Scrollbar: sb}) // つまみは 4 行目と 5 行目
	// 画面の外の位置は画面の端の行（HitText と同じ）にしてから、表示領域の外の行（10 行目、11 行目）もそのまま返す。
	for _, c := range []struct{ row, col, wantRow int }{{3, 2, 3}, {0, 9, 0}, {9, 0, 9}, {11, 9, 11}, {40, 9, 11}, {-5, 9, 0}} {
		h := r.HitTest(c.row, c.col, HitScrollbar)
		if h.Area != HitScrollbar || h.Row != c.wantRow || h.ThumbRow != 4 {
			t.Errorf("ドラッグの (%d,%d) = %+v, want Row %d", c.row, c.col, h, c.wantRow)
		}
	}
	// h は 10 行、n は 100 行。
	for _, c := range []struct{ i, wantRow, want int }{{-3, 0, 0}, {0, 0, 0}, {1, 1, 11}, {4, 4, 44}, {5, 5, 55}, {8, 8, 88}, {9, 9, 99}, {20, 9, 99}} {
		if row, got, ok := r.ScrollbarTop(c.i); !ok || row != c.wantRow || got != c.want {
			t.Errorf("ScrollbarTop(%d) = %d, %d, %v, want %d, %d", c.i, row, got, ok, c.wantRow, c.want)
		}
	}
	if got := fmt.Sprint(r.ScrollbarMarkLines(0)); got != "[3 7 7]" {
		t.Errorf("0 行目の印の行 = %s", got)
	}
	if got := fmt.Sprint(r.ScrollbarMarkLines(5)); got != "[52]" {
		t.Errorf("5 行目の印の行 = %s", got)
	}
	if got := r.ScrollbarMarkLines(4); got != nil {
		t.Errorf("印の無い 4 行目の印の行 = %v", got)
	}
	if got := r.ScrollbarMarkLines(10); got != nil {
		t.Errorf("範囲の外の行の印の行 = %v", got)
	}
	// 描いていなければ、ドラッグの位置は解かない。
	r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: 50}})
	if h := r.HitTest(3, 9, HitScrollbar); h.Area != HitNone {
		t.Errorf("描いていないときのドラッグ = %+v", h)
	}
	if _, _, ok := r.ScrollbarTop(3); ok {
		t.Errorf("描いていないときの ScrollbarTop が ok")
	}
	if got := r.ScrollbarMarkLines(0); got != nil {
		t.Errorf("描いていないときの印の行 = %v", got)
	}
}

// ScrollbarTop の最初の行で描き直すと、つまみがその行からずれることがある（40 行の表示領域、201 行で、1 行目の最初の行は 5 で、
// 元の数え方のつまみは 0 行目）。HoldScrollbarThumb の後は、最初の行がその行の間だけ、つまみを決めた行に描き、
// HitTest の ThumbRow もその行にする。最初の行が変われば元の数え方に戻る。
//
// req: FR-210
func TestScrollbarHoldThumb(t *testing.T) {
	var lines strings.Builder
	for range 201 {
		lines.WriteString("0123456789\n")
	}
	text := buffer.New(lines.String())
	sb := &Scrollbar{}
	bg := Indexed(8)
	r := &Renderer{Width: 10, Height: 42, Theme: Theme{StyleScrollbar: {Bg: bg}}} // 表示領域は 40 行
	r.Render(&Frame{Text: text, Scrollbar: sb})
	row, top, ok := r.ScrollbarTop(1)
	if !ok || row != 1 || top != 5 {
		t.Fatalf("ScrollbarTop(1) = %d, %d, %v", row, top, ok)
	}
	r.SetScrollHeld(top, 0, 5)
	s := r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: 5}, Scrollbar: sb})
	// 元の数え方では、つまみは 0 行目（5×40÷201 の切り捨て）から 9 行目（45×40÷201 の切り上げ）の手前までである。
	if got := thumbRows(s, 11, bg); got != "#########.." {
		t.Errorf("元の数え方のつまみ = %q", got)
	}
	r.HoldScrollbarThumb(row, top)
	s = r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: 5}, Scrollbar: sb})
	if got := thumbRows(s, 11, bg); got != ".#########." {
		t.Errorf("決めた行のつまみ = %q", got)
	}
	if h := r.HitTest(1, 9, HitNone); h.Area != HitScrollbar || !h.Thumb || h.ThumbRow != 1 {
		t.Errorf("決めた行のつまみの HitTest = %+v", h)
	}
	r.SetScrollHeld(4, 0, 4)
	s = r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: 4}, Scrollbar: sb})
	if got := thumbRows(s, 11, bg); got != "#########.." {
		t.Errorf("最初の行が変わった後のつまみ = %q", got)
	}
}

// 印は、行を表示領域の行へ floor(行×行数÷バッファの行数) で割り付け、同じセルでは優先の大きい種類を 1 つだけ出す。
// 優先が同じなら前の種類を出す。つまみの上の印は、つまみの色の上に印の色を重ねる。記号が空の種類は描かない。
// 印もつまみと同じく、表示領域に収まらないときだけ描く。
//
// req: FR-187
func TestRenderScrollbarMarks(t *testing.T) {
	theme := Theme{
		StyleScrollbar:                {Bg: Indexed(8)},
		StyleScrollbarDiagnosticError: {Fg: Indexed(9)},
		StyleScrollbarSearch:          {Fg: Indexed(12)},
	}
	var lines strings.Builder
	for range 100 {
		lines.WriteString("0123456789\n")
	}
	text := buffer.New(lines.String())
	marks := []ScrollbarMarks{
		{Lines: []int{5, 55, 55, 99}, Priority: 1, Style: StyleScrollbarSearch, Symbol: SymbolScrollbarSearch},
		{Lines: []int{50, 59}, Priority: 3, Style: StyleScrollbarDiagnosticError, Symbol: SymbolScrollbarDiagnosticError},
		// 優先が同じなら、前の種類（error）を出す。
		{Lines: []int{51}, Priority: 3, Style: StyleScrollbarDiagnosticWarning, Symbol: SymbolScrollbarDiagnosticWarning},
		{Lines: []int{70}, Priority: 2, Style: StyleScrollbarDiagnosticInfo, Symbol: SymbolScrollbarDiagnosticInfo},
	}
	r := &Renderer{Width: 10, Height: 12, Theme: theme}
	s := r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: 50}, Scrollbar: &Scrollbar{Marks: marks}})
	if got, want := scrollbarColumn(s, 10), "=9999E9I9="; got != want {
		t.Errorf("右端の列 = %q, want %q", got, want)
	}
	// 5 行目はつまみ（4 行目から 5 行目）の上なので、つまみの背景に印の文字の色を重ねる。7 行目はつまみの外なので、印の色だけである。
	if a := s.Grid.At(5, 9).Attr; a != (Attr{Fg: Indexed(9), Bg: Indexed(8)}) {
		t.Errorf("つまみの上の印の色 = %+v", a)
	}
	if a := s.Grid.At(7, 9).Attr; a != (Attr{}) {
		t.Errorf("info の印の色（theme に無い） = %+v", a)
	}
	if a := s.Grid.At(0, 9).Attr; a != theme[StyleScrollbarSearch] {
		t.Errorf("つまみの外の印の色 = %+v", a)
	}

	// 記号を空にした種類は描かず、次に優先の大きい種類が出る。
	r.Symbols = Symbols{SymbolScrollbarDiagnosticError: ""}
	s = r.Render(&Frame{Text: text, Cursor: buffer.Position{Line: 50}, Scrollbar: &Scrollbar{Marks: marks}})
	if got, want := scrollbarColumn(s, 10), "=9999W9I9="; got != want {
		t.Errorf("error の記号が空の右端の列 = %q, want %q", got, want)
	}

	// 表示領域に収まるバッファには、印も描かない。
	short := &Renderer{Width: 10, Height: 12, Theme: theme}
	s = short.Render(&Frame{Text: buffer.New("abc\n"), Scrollbar: &Scrollbar{Marks: marks}})
	if got := strings.TrimRight(s.Grid.RowText(0), " "); got != "abc" {
		t.Errorf("収まるバッファの 0 行目 = %q", got)
	}
}
