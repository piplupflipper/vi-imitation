package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"testing"
)

// TestRuneWidthVim は、1 文字の表示幅を、Vim 9.1 の strdisplaywidth の値と全文字で比べる。
//
// testdata/vim-width.txt は、internal/widthgen（go generate）が Vim から書き出した値で、
// U+0001 から U+10FFFF までの各文字 c について、'a' の後に置いた幅（strdisplaywidth('a' .. c) - 1）と
// 単独の幅（strdisplaywidth(c)）の組が変わる位置で区切った区間である。
// 幅 -2 はサロゲートで、Go の文字列には現れないので比べない。
// タブは列 1 に置いた幅なので比べず、TestStringWidth で確かめる。
//
// Go の unicode パッケージと Vim 9.1 は、持っている Unicode の版が違う。
// このため、結合文字（Mn と Me）かどうかの判定が食い違う文字（Go が新しく Mn にした文字と、Mn から Mc に変わった U+1171E）は比べない。
// これらは internal/command/target と同じく Go の判定に従う。
func TestRuneWidthVim(t *testing.T) {
	data, err := os.ReadFile("testdata/vim-width.txt")
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	checked, skipped := 0, 0
	for sc.Scan() {
		var lo, hi rune
		var after, alone int
		if _, err := fmt.Sscanf(sc.Text(), "%x %x %d %d", &lo, &hi, &after, &alone); err != nil {
			t.Fatalf("%q: %v", sc.Text(), err)
		}
		if after < 0 {
			continue
		}
		for r := lo; r <= hi; r++ {
			if r == '\t' {
				continue
			}
			if isComposing(r) != (after == 0) {
				skipped++
				continue
			}
			gotAfter := StringWidth("a"+string(r), DefaultTabStop) - 1
			gotAlone := StringWidth(string(r), DefaultTabStop)
			if gotAfter != after || gotAlone != alone {
				t.Errorf("U+%04X: width after 'a' %d, alone %d; Vim %d, %d", r, gotAfter, gotAlone, after, alone)
			}
			checked++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if checked < 1_000_000 {
		t.Errorf("checked only %d characters", checked)
	}
	t.Logf("checked %d characters, skipped %d characters whose combining class differs", checked, skipped)
}

func TestStringWidth(t *testing.T) {
	tests := []struct {
		s       string
		tabStop int
		want    int
	}{
		{"", 8, 0},
		{"abc", 8, 3},
		{"あいう", 8, 6},
		{"aあb", 8, 4},
		{"\t", 8, 8},
		{"a\t", 8, 8},
		{"abcdefgh\t", 8, 16},
		{"あ\tx", 8, 9},
		{"a\tb", 4, 5},
		{"\t", 0, 8},
		{"\x01", 8, 2},     // ^A
		{"\x00", 8, 2},     // ^@
		{"\x7f", 8, 2},     // ^?
		{"\u0085", 8, 4},   // <85>
		{"a\x80b", 8, 6},   // <80>
		{"\xe3\x81", 8, 8}, // 途中で切れた UTF-8 は 1 バイトずつ <e3><81>
		{"\u200b", 8, 6},   // <200b>
		{"e\u0301", 8, 1},  // 結合文字は 0 列
		{"\u0301", 8, 1},   // 行頭の結合文字は空白に重ねて 1 列
		{"\u0301\u0302", 8, 1},
		{"\u3099", 8, 2}, // 行頭の全角の結合文字は 2 列
		{"\u180b", 8, 6}, // 行頭の U+180B は <180b>
		{"a\u180b", 8, 1},
		{"\t\u0301", 8, 8},
		{"😀", 8, 2},
		{"🌡", 8, 2}, // Vim は East Asian Width が N の絵文字も 2 列にする
		{" ", 8, 1},
		{"Ａ", 8, 2}, // 全角の A
	}
	for _, tt := range tests {
		if got := StringWidth(tt.s, tt.tabStop); got != tt.want {
			t.Errorf("StringWidth(%q, %d) = %d, want %d", tt.s, tt.tabStop, got, tt.want)
		}
	}
}

// TestColumns は、望みの列の数え方を確かめる。
// タブの上の列（最後の列）は、Vim 9.1 で "\tx" の 1 文字目から j を押すと下の行の 8 文字目へ移ることを確かめた。
func TestColumns(t *testing.T) {
	c := Columns{}
	columnTests := []struct {
		line string
		off  int
		want int
	}{
		{"", 0, 0},
		{"abc", 0, 0},
		{"abc", 2, 2},
		{"abc", 3, 3},
		{"\tx", 0, 7},
		{"\tx", 1, 8},
		{"a\tb", 1, 7},
		{"a\tb", 2, 8},
		{"あいう", 3, 2},
		{"あいう", 6, 4},
		{"e\u0301x", 1, 0}, // 結合文字の位置はその文字の列
		{"e\u0301x", 3, 1},
		{"\x01a", 1, 2},
		{"a\x80b", 2, 5},
	}
	for _, tt := range columnTests {
		if got := c.Column(0, tt.line, tt.off); got != tt.want {
			t.Errorf("Column(%q, %d) = %d, want %d", tt.line, tt.off, got, tt.want)
		}
	}

	byteTests := []struct {
		line string
		col  int
		want int
	}{
		{"", 5, 0},
		{"abc", 0, 0},
		{"abc", 2, 2},
		{"abc", 10, 2},
		{"abc", -1, 0},
		{"\tx", 0, 0},
		{"\tx", 7, 0},
		{"\tx", 8, 1},
		{"あいう", 1, 0},
		{"あいう", 2, 3},
		{"あいう", 5, 6},
		{"あいう", 100, 6},
		{"e\u0301x", 1, 3},
		{"\x01a", 1, 0},
		{"\x01a", 2, 1},
	}
	for _, tt := range byteTests {
		if got := c.ByteAt(0, tt.line, tt.col); got != tt.want {
			t.Errorf("ByteAt(%q, %d) = %d, want %d", tt.line, tt.col, got, tt.want)
		}
	}

	if got := (Columns{LineLayout: LineLayout{TabStop: 4}}).Column(0, "\tx", 1); got != 4 {
		t.Errorf("TabStop 4: Column = %d, want 4", got)
	}
}
