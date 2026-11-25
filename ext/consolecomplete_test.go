package ext

import (
	"strings"
	"testing"
)

// 入力の行の補完の候補の検査（FR-297）。範囲は入力の行の中の文字の境目を指し、始まりはカーソルより後ろにない。
func TestConsoleCompletionItemValidate(t *testing.T) {
	const text = "あい.b" // あ(0-3) い(3-6) .(6) b(7)
	tests := []struct {
		name   string
		it     ConsoleCompletionItem
		cursor int
		want   string
	}{
		{"カーソルの前の語", ConsoleCompletionItem{Label: "bar", Start: 7, End: 8}, 8, ""},
		{"空の範囲", ConsoleCompletionItem{Label: "x", Start: 8, End: 8}, 8, ""},
		{"多バイト文字の境目", ConsoleCompletionItem{Label: "あいう", Start: 0, End: 6}, 6, ""},
		{"カーソルの後ろまで", ConsoleCompletionItem{Label: "い.b", Start: 3, End: 8}, 3, ""},
		{"文字の途中", ConsoleCompletionItem{Label: "x", Start: 1, End: 6}, 6, "文字の途中"},
		{"終わりが文字の途中", ConsoleCompletionItem{Label: "x", Start: 0, End: 4}, 0, "文字の途中"},
		{"外", ConsoleCompletionItem{Label: "x", Start: 7, End: 9}, 8, "外を指す"},
		{"負", ConsoleCompletionItem{Label: "x", Start: -1, End: 0}, 8, "外を指す"},
		{"逆", ConsoleCompletionItem{Label: "x", Start: 7, End: 6}, 8, "外を指す"},
		{"カーソルより後ろ", ConsoleCompletionItem{Label: "x", Start: 7, End: 8}, 6, "カーソル"},
		{"改行", ConsoleCompletionItem{Label: "a\nb", Start: 0, End: 0}, 0, "label が改行"},
		{"種類の改行", ConsoleCompletionItem{Label: "a", Kind: "f\r", Start: 0, End: 0}, 0, "kind が改行"},
	}
	for _, tt := range tests {
		err := tt.it.Validate(text, tt.cursor)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: %v", tt.name, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
}

// 入れる文字列は Text で、空なら Label である。
func TestConsoleCompletionItemInsert(t *testing.T) {
	if got := (&ConsoleCompletionItem{Label: "a", Text: "b"}).Insert(); got != "b" {
		t.Errorf("Insert = %q", got)
	}
	if got := (&ConsoleCompletionItem{Label: "a"}).Insert(); got != "a" {
		t.Errorf("Insert = %q", got)
	}
}
