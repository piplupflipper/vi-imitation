package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// sameLines は、同じ行を n 行並べたテキストである。大きなバッファの描画を、メモリを使わずに測るために使う。
type sameLines struct {
	line string
	n    int
}

func (s sameLines) LineCount() int  { return s.n }
func (s sameLines) Line(int) string { return s.line }

// perLine は、各行に、幅 step バイトごとに 1 つずつ、count 個の色の範囲を付けた一覧を作る。
func perLine(lines, count, step int) *Decorations {
	styles := []string{StyleKeyword, StyleString, StyleComment, StyleType}
	hs := make([]Highlight, 0, lines*count)
	for n := range lines {
		for i := range count {
			hs = append(hs, Highlight{
				Range: buffer.Range{Start: pos(n, i*step), End: pos(n, i*step+step/2)},
				Style: styles[i%len(styles)],
			})
		}
	}
	return NewDecorations(hs, lines)
}

// benchRender は、200 × 60 の画面で、カーソルを 1 行ずつ下げながら f を描く時間を測る。
func benchRender(b *testing.B, f Frame) {
	b.Helper()
	r := &Renderer{Width: 200, Height: 60}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		f.Cursor.Line = 5000 + i%50
		r.Render(&f)
	}
}

func BenchmarkRenderPlain(b *testing.B) {
	line := "\tfunc example(あいう int) string { return \"日本語\" } // comment"
	benchRender(b, Frame{Text: sameLines{line, 10000}})
}

func BenchmarkRenderLongLines(b *testing.B) {
	line := strings.Repeat("abcdefghij", 2200)
	benchRender(b, Frame{Text: sameLines{line, 10000}, Cursor: pos(0, 20000)})
}

// 行を折り返すとき（FR-156）の描画である。どの行も画面の幅より長いので、カーソルを 1 行下げるたびに、行の単位でスクロールする。
func BenchmarkRenderWrapLongLines(b *testing.B) {
	line := strings.Repeat("abcdefghij", 2200)
	benchRender(b, Frame{Text: sameLines{line, 10000}, Cursor: pos(0, 20000), Wrap: true})
}

// 行を折り返すとき、全角文字とタブを含む 20 万文字の 1 行の中で、カーソルを見た目の行で動かしながら描く（行の途中から描く）。
func BenchmarkRenderWrapHugeLine(b *testing.B) {
	line := strings.Repeat("abc\tあいうえお日本語 ", 20000)
	text := sameLines{line, 1}
	r := &Renderer{Width: 200, Height: 60}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		r.Render(&Frame{Text: text, Cursor: pos(0, (i*997)%len(line)/4*4), Wrap: true})
	}
}

// 行の最後の見た目の行からの gk の 1 回（target の displayVertical）が WrapLines に問い合わせる数え方を、
// BenchmarkRenderWrapHugeLine と同じ 1 行で測る。
func BenchmarkWrapLinesHugeLine(b *testing.B) {
	line := strings.Repeat("abc\tあいうえお日本語 ", 20000)
	r := &Renderer{Width: 200, Height: 60}
	r.Render(&Frame{Text: sameLines{line, 1}, Wrap: true})
	l := r.WrapLines(LineLayout{TabStop: DefaultTabStop})
	b.ResetTimer()
	n := l.Column(0, line, len(line))
	for i := range b.N {
		// 行の最後の見た目の行から gk を打つ。行の最後の列、望みの列の文字、着いた文字の列を数える。
		_ = l.Column(0, line, len(line))
		at := l.ByteAt(0, line, n-l.Width()-(i%l.Width()))
		_ = l.Column(0, line, at)
	}
}

func BenchmarkRenderManyDecorations(b *testing.B) {
	line := strings.Repeat("abcd ", 40)
	benchRender(b, Frame{Text: sameLines{line, 10000}, Decorations: perLine(10000, 60, 3)})
}

func BenchmarkRenderDenseDecorationsScrolled(b *testing.B) {
	line := strings.Repeat("ab", 1000)
	f := Frame{Text: sameLines{line, 1000}, Decorations: perLine(1000, 500, 4), Cursor: pos(0, 1500)}
	r := &Renderer{Width: 200, Height: 60}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		f.Cursor.Line = 500 + i%50
		r.Render(&f)
	}
}

// spanning は、perLine(lines, 8, 4) の一覧に、ranges 個の行をまたぐ範囲を足した一覧である。
// i 個目の範囲は、i 行目の途中から、末尾から i 行目の途中までにまたがる（ほぼ全体）。
func spanning(lines, ranges int) []Highlight {
	styles := []string{StyleKeyword, StyleString, StyleComment, StyleType}
	var hs []Highlight
	for n := range lines {
		for i := range 8 {
			hs = append(hs, Highlight{Range: buffer.Range{Start: pos(n, i*4), End: pos(n, i*4+2)}, Style: styles[i%len(styles)]})
		}
	}
	for i := range ranges {
		hs = append(hs, Highlight{Range: buffer.Range{Start: pos(i, 3), End: pos(lines-1-i, 5)}, Style: styles[i%len(styles)]})
	}
	return hs
}

// 1 行の中の範囲だけの一覧（行をまたぐ範囲が無い、ふつうの場合）。
func BenchmarkNewDecorationsSingleLine100k(b *testing.B) {
	hs := spanning(100_000, 0)
	b.ReportAllocs()
	for b.Loop() {
		NewDecorations(hs, 100_000)
	}
}

// ほぼ全体にまたがる範囲が 100 個ある一覧。前は行ごとに展開したので、表が 100 × 10 万要素になった（#80）。
func BenchmarkNewDecorationsMultiline100k(b *testing.B) {
	hs := spanning(100_000, 100)
	b.ReportAllocs()
	for b.Loop() {
		NewDecorations(hs, 100_000)
	}
}

// 一致の多い長い行（PR #276 のレビューの推奨 1 の、約 917 KB の行を 8 行と同じ形）を、80 × 16 の画面に描く時間を測る。
// 一致を探すのは表示の右の端までなので、一致の数（1 行に約 13 万）でなく、見えている列の範囲に比例する。
func BenchmarkRenderSearchLongLines(b *testing.B) {
	line := strings.Repeat("foo xy ", 131072)
	for _, tt := range []struct {
		name    string
		pattern string
		left    int
	}{
		{"o", "o", 0},
		{"y_foo", "y foo", 0},
		{"zzq", "zzq", 0},
		{"none", "", 0},
		{"o_scrolled", "o", 450000},
	} {
		b.Run(tt.name, func(b *testing.B) {
			f := Frame{Text: sameLines{line, 8}, Cursor: pos(0, tt.left)}
			if tt.pattern != "" {
				f.Search = regexp.MustCompile(tt.pattern)
			}
			r := &Renderer{Width: 80, Height: 16}
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				f.Cursor.Line = i % 8
				r.Render(&f)
			}
		})
	}
}

func BenchmarkRenderMultilineDecorations(b *testing.B) {
	line := strings.Repeat("abcd ", 40)
	benchRender(b, Frame{Text: sameLines{line, 10000}, Decorations: NewDecorations(spanning(10000, 100), 10000)})
}
