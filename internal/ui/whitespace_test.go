package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// nbsp と nnbsp は、ノーブレークスペース（U+00A0）と狭いノーブレークスペース（U+202F）である。
var nbsp, nnbsp = string(rune(0x00a0)), string(rune(0x202f))

// nbspLine は、whitespaceText の 6 行目（ノーブレークスペースと、ノーブレークスペースの前の空白）である。
var nbspLine = "a" + nbsp + "b" + nnbsp + "c " + nbsp

// whitespaceText は、空白の記号の確かめに使うテキストである。2026-11-21 に同じテキストを nvim 0.12.5（--clean）と Vim 9.1（-Nu NONE）で
// tmux の画面に開き、:set list と 'listchars' を変えて capture-pane -e -p で読んだ画面を期待値にした（どの場合も nvim と Vim の画面は同じだった）。
//
//	1 行目: 行頭のタブ文字
//	2 行目: 全角の文字の後のタブ文字（タブ文字の幅が 6 列になる）
//	3 行目: 行頭、行の途中、行末の空白（20 文字。20 列の画面ではちょうど本文の幅で終わる）
//	4 行目: 空白だけの行
//	5 行目: 空の行
//	6 行目: ノーブレークスペース（U+00A0 と U+202F）と、ノーブレークスペースの前の空白（行末の空白でない）
//	7 行目: 行頭の空白とタブ文字、行の途中と行末のタブ文字と空白
//	8 行目: 40 文字の後のタブ文字（20 列の画面では見た目の行の頭から始まる）
//	9 行目: 19 文字の行、10 行目: 20 文字の行
func whitespaceText() *buffer.Buffer {
	return buffer.New(strings.Join([]string{
		"\tx",
		"あ\tx",
		"  lead  mid  trail  ",
		"    ",
		"",
		nbspLine,
		" \t x\t ",
		"abcdefghijklmnopqrstuvwxyzabcdefghijklmn\topq",
		"0123456789012345678",
		"01234567890123456789",
	}, "\n") + "\n")
}

// lcs は、Vim の 'listchars' の項目（tab、space、lead、trail、nbsp、eol）を、空白の記号の対応にする。書かない項目は空にする
// （'listchars' を書き直したときに、書かない項目が無くなるのと同じ）。
func lcs(items map[string]string) Symbols {
	s := Symbols{}
	for _, n := range []string{SymbolWhitespaceTab, SymbolWhitespaceSpace, SymbolWhitespaceLead, SymbolWhitespaceTrail, SymbolWhitespaceNbsp, SymbolWhitespaceEOL} {
		s[n] = items[strings.TrimPrefix(n, "whitespace.")]
	}
	return s
}

// list は、空白の記号を描く並べ方（タブ文字の幅は既定）である。
var list = LineLayout{Whitespace: true}

// checkRows は、画面の上から len(want) 行の文字（右の空白を除く。diff_test.go の rowTexts）が want と同じかを確かめる。
func checkRows(t *testing.T, name string, s Screen, want []string) {
	t.Helper()
	got := rowTexts(s, len(want))
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: %d 行目 = %q, want %q", name, i+1, got[i], want[i])
		}
	}
}

// 空白の記号の描き方を、nvim 0.12.5 と Vim 9.1 の :set list の画面と比べる（期待値の取り方は whitespaceText の doc）。
//
//   - 既定の記号（Neovim の 'listchars' の既定の tab:> ,trail:-,nbsp:+）。空白だけの行は行末の空白として描く
//   - タブ文字の記号が空なら ^I の 2 列で描く（行の幅が変わる）。行末の記号は空の行にも描く
//   - 空白の記号の優先（lead と trail は space より優先し、空なら space を使う。空白だけの行には lead を使わない）
//   - タブ文字の記号の 3 文字の形（tab:<->）と 2 文字の形（tab:xy）
//
// req: FR-170
func TestWhitespaceSymbols(t *testing.T) {
	b := whitespaceText()
	render := func(w, h int, sym Symbols) Screen {
		r := newRenderer(w, h)
		r.Symbols = sym
		return r.Render(&Frame{Text: b, LineLayout: list})
	}
	tests := []struct {
		name string
		w    int
		sym  Symbols
		want []string
	}{
		{"既定の記号", 30, nil, []string{
			">       x", "あ>     x", "  lead  mid  trail--", "----", "", "a+b+c +", " >       x>     -",
			"abcdefghijklmnopqrstuvwxyzabcd", "0123456789012345678", "01234567890123456789",
		}},
		{"eol:$", 30, lcs(map[string]string{"eol": "$"}), []string{
			"^Ix$", "あ^Ix$", "  lead  mid  trail  $", "    $", "$", nbspLine + "$", " ^I x^I $",
			"abcdefghijklmnopqrstuvwxyzabcd", "0123456789012345678$", "01234567890123456789$",
		}},
		{"space:S,lead:L,trail:T,nbsp:N", 40, lcs(map[string]string{"space": "S", "lead": "L", "trail": "T", "nbsp": "N"}), []string{
			"^Ix", "あ^Ix", "LLleadSSmidSStrailTT", "TTTT", "", "aNbNcSN", "L^ILx^IT",
		}},
		{"lead:L", 40, lcs(map[string]string{"lead": "L"}), []string{
			"^Ix", "あ^Ix", "LLlead  mid  trail", "", "", nbspLine, "L^ILx^I",
		}},
		{"space:S", 40, lcs(map[string]string{"space": "S"}), []string{
			"^Ix", "あ^Ix", "SSleadSSmidSStrailSS", "SSSS", "", "a" + nbsp + "b" + nnbsp + "cS" + nbsp, "S^ISx^IS",
		}},
		{"space:S,trail:T", 40, lcs(map[string]string{"space": "S", "trail": "T"}), []string{
			"^Ix", "あ^Ix", "SSleadSSmidSStrailTT", "TTTT", "", "a" + nbsp + "b" + nnbsp + "cS" + nbsp, "S^ISx^IT",
		}},
		{"tab:<->", 40, lcs(map[string]string{"tab": "<->"}), []string{
			"<------>x", "あ<---->x", "  lead  mid  trail", "", "", nbspLine, " <-----> x<---->",
		}},
		{"tab:xy", 40, lcs(map[string]string{"tab": "xy"}), []string{
			"xyyyyyyyx", "あxyyyyyx", "  lead  mid  trail", "", "", nbspLine, " xyyyyyy xxyyyyy",
		}},
	}
	for _, tt := range tests {
		checkRows(t, tt.name, render(tt.w, 14, tt.sym), tt.want)
	}
}

// 行を折り返すときの空白の記号を、nvim 0.12.5 と Vim 9.1 の :set list wrap の 20 列の画面と比べる。
//
//   - オーナーの nvim の値（tab:→ ,lead:·,trail:·,eol:↲）。ちょうど本文の幅で終わる行（3 行目と 10 行目）は、行末の記号を次の見た目の行に描き、
//     その行を見た目の行の数に数える
//   - 見た目の行の右の端をまたぐタブ文字（tab:<->）は、記号を続けて次の見た目の行に描く
//
// req: FR-170
func TestWhitespaceWrap(t *testing.T) {
	r := newRenderer(20, 22)
	r.Symbols = lcs(map[string]string{"tab": "→ ", "lead": "·", "trail": "·", "eol": "↲"})
	s := r.Render(&Frame{Text: whitespaceText(), LineLayout: list, Wrap: true})
	checkRows(t, "オーナーの値", s, []string{
		"→       x↲", "あ→     x↲", "··lead  mid  trail··", "↲", "····↲", "↲", nbspLine + "↲", "·→      ·x→     ·↲",
		"abcdefghijklmnopqrst", "uvwxyzabcdefghijklmn", "→       opq↲", "0123456789012345678↲", "01234567890123456789", "↲", "~",
	})
	if got := r.WrapLines(list).Rows(0, "01234567890123456789"); got != 2 {
		t.Errorf("行末の記号を描くときの 20 文字の行の見た目の行の数 = %d, want 2", got)
	}
	if got := r.WrapLines(LineLayout{}).Rows(0, "01234567890123456789"); got != 1 {
		t.Errorf("空白の記号を描かないときの 20 文字の行の見た目の行の数 = %d, want 1", got)
	}

	r = newRenderer(20, 12)
	r.Symbols = lcs(map[string]string{"tab": "<->"})
	b := buffer.New("\tabc\n0123456789\n0123456789012345\tX\nab\tc\n\t\n")
	checkRows(t, "tab:<->", r.Render(&Frame{Text: b, LineLayout: list, Wrap: true}), []string{
		"<------>abc", "0123456789", "0123456789012345<---", "--->X", "ab<---->c", "<------>",
	})
}

// 折り返さずに横にスクロールしたとき、画面の左の端より短い行（空の行を含む）の行末の記号は、画面の 0 列目に描く。
// 行末に掛かる選択の色も同じく 0 列目に描く（空白の記号を描かないときも）。
// 2026-11-21 に nvim 0.12.5 と Vim 9.1 で、60 文字の行と短い行のファイルを 30 列の tmux で開き、1 行目で $ を打って横にスクロールし、
// 'list' の有無の両方で、短い行の 0 列目に $ と選択の色が出ることを確かめた（PR #334 のレビューの必須 2）。
//
// req: FR-170
func TestWhitespaceEOLScrolled(t *testing.T) {
	b := buffer.New(strings.Repeat("0123456789", 6) + "\nx\n\nabc\n")
	r := newRenderer(30, 8)
	r.Symbols = lcs(map[string]string{"eol": "$"})
	s := r.Render(&Frame{Text: b, Cursor: pos(0, 59), LineLayout: list})
	if r.Left() == 0 {
		t.Fatalf("横にスクロールしていない")
	}
	checkRows(t, "行末の記号", s, []string{strings.Repeat("0123456789", 6)[r.Left():] + "$", "$", "$", "$"})

	r = newRenderer(30, 8)
	sel := Selection{Active: true, Start: pos(0, 59), End: pos(2, 0), Linewise: true}
	s = r.Render(&Frame{Text: b, Mode: ModeVisualLine, Cursor: pos(0, 59), Selection: sel})
	visual := r.Theme[StyleVisual]
	for row := 1; row <= 2; row++ {
		if got := s.Grid.At(row, 0).Attr; got != visual {
			t.Errorf("記号を描かないときの %d 行目の 0 列目の色 = %+v, want 選択の色", row+1, got)
		}
	}
	if got := s.Grid.At(3, 0).Attr; got != (Attr{}) {
		t.Errorf("選択の外の 4 行目の 0 列目の色 = %+v, want 色なし", got)
	}
}

// 空白の記号を描くときは、タブ文字の上のカーソルを、ノーマルモードでもタブの最初の列に置き、望みの列とルーラーの列もその列にする。
// nvim 0.12.5 と Vim 9.1 で、"\tabc" の 1 文字目のカーソルが画面の 0 列目に出て、j で下の行の 1 列目へ移ることを確かめた。
// タブ文字を ^I で描くときは、タブ文字を 2 列として数える（nvim のルーラーは "あ\tx" のタブ文字の上で 2,4-3）。
//
// req: FR-170
func TestWhitespaceCursor(t *testing.T) {
	b := whitespaceText()
	r := newRenderer(30, 14)
	for _, ws := range []bool{false, true} {
		for _, mode := range []Mode{ModeNormal, ModeInsert} {
			s := r.Render(&Frame{Text: b, Mode: mode, LineLayout: LineLayout{Whitespace: ws}})
			want := 0
			if !ws && mode == ModeNormal {
				want = 7
			}
			if s.Cursor.Col != want {
				t.Errorf("Whitespace=%v モード %v のタブ文字の上のカーソルの列 = %d, want %d", ws, mode, s.Cursor.Col, want)
			}
		}
	}

	r.Symbols = lcs(map[string]string{"eol": "$"})
	s := r.Render(&Frame{Text: b, Cursor: pos(1, 3), LineLayout: list})
	if s.Cursor.Col != 2 || !strings.Contains(s.Grid.RowText(r.Height-2), "2,4-3") {
		t.Errorf("^I の上のカーソル = 列 %d、ステータス行 %q, want 列 2 と 2,4-3", s.Cursor.Col, s.Grid.RowText(r.Height-2))
	}

	tests := []struct {
		cols       Columns
		line       string
		byteOff    int
		col, byteA int // Column の結果と、ByteAt(line, col+1) の結果
	}{
		{Columns{}, "\tabc", 0, 7, 1},
		{Columns{LineLayout: list}, "\tabc", 0, 0, 0},
		{Columns{LineLayout: list}, "\tabc", 1, 8, 2},
		{Columns{LineLayout: list, Symbols: lcs(nil)}, "ab\tc", 3, 4, 3},
		{Columns{LineLayout: list, Symbols: lcs(nil)}, "ab\tc", 2, 2, 2},
	}
	for _, tt := range tests {
		if got := tt.cols.Column(0, tt.line, tt.byteOff); got != tt.col {
			t.Errorf("%+v.Column(%q, %d) = %d, want %d", tt.cols, tt.line, tt.byteOff, got, tt.col)
		}
		if got := tt.cols.ByteAt(0, tt.line, tt.col+1); got != tt.byteA {
			t.Errorf("%+v.ByteAt(%q, %d) = %d, want %d", tt.cols, tt.line, tt.col+1, got, tt.byteA)
		}
	}
}

// 空白の記号の色は、^A などの表記と同じ「文字の表記の色」の層で、構文の色の上、検索の一致と選択の色の下に重ねる（layers.go）。
// 検索の一致の文字の色は、記号の色より上に出る（Vim 9.1 と同じ。nvim 0.12.5 は記号の色を一致の色の上に出す。docs/vim-differences.md）。
// 選択は背景の色だけを持つので、記号の文字の色の上に選択の背景の色が出る（nvim 0.12.5 と同じ）。行末の記号は nontext の色である。
//
// req: FR-170
func TestWhitespaceLayers(t *testing.T) {
	ws, nt := Attr{Fg: Indexed(1)}, Attr{Fg: Indexed(2)}
	search, visual := Attr{Fg: Indexed(0), Bg: Indexed(3)}, Attr{Bg: Indexed(4)}
	r := &Renderer{Width: 40, Height: 9, Theme: Theme{StyleWhitespace: ws, StyleNonText: nt, StyleSearch: search, StyleVisual: visual}}
	r.Symbols = lcs(map[string]string{"tab": ">-", "lead": "L", "trail": "T", "eol": "$"})
	b := whitespaceText()

	s := r.Render(&Frame{Text: b, LineLayout: list, Search: regexp.MustCompile(` +$|\t`)})
	at := func(row, col int) Attr { return s.Grid.At(row, col).Attr }
	checks := []struct {
		name     string
		row, col int
		want     Attr
	}{
		{"一致したタブ文字", 0, 0, search},
		{"一致した行末の空白", 2, 18, search},
		{"一致しない行頭の空白", 2, 0, ws},
		{"行末の記号", 2, 20, nt},
		{"ノーブレークスペースの前の空白（記号を描かない）", 5, 5, Attr{}},
	}
	for _, c := range checks {
		if got := at(c.row, c.col); got != c.want {
			t.Errorf("%s の色 = %+v, want %+v", c.name, got, c.want)
		}
	}

	sel := Selection{Active: true, Start: pos(2, 0), End: pos(4, 0), Linewise: true}
	s = r.Render(&Frame{Text: b, Mode: ModeVisualLine, Cursor: pos(4, 0), Selection: sel, LineLayout: list})
	checks = []struct {
		name     string
		row, col int
		want     Attr
	}{
		{"選択した行頭の空白", 2, 0, ws.Over(visual)},
		{"選択した行の行末の記号", 2, 20, nt.Over(visual)},
		{"選択した空白だけの行", 3, 1, ws.Over(visual)},
	}
	for _, c := range checks {
		if got := s.Grid.At(c.row, c.col).Attr; got != c.want {
			t.Errorf("%s の色 = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// 構文の色（tree-sitter の拡張機能が送る範囲の色）と重なった空白の記号と ^A は、構文の色でなく記号の色で描く（PR #334 のレビューの必須 1）。
// 記号でない文字は構文の色のまま、行末の記号は nontext の色である。
// 2026-11-21 に、同じ Go のテキストを nvim 0.12.5 と Vim 9.1 で syntax on にして開き（Whitespace と SpecialKey を 1 番、Comment を 4 番、
// String を 3 番、NonText を 2 番にした）、行末の空白、生の文字列の中の行頭の空白とタブ文字、ブロックコメントの中の行頭の空白の記号が
// どれも Whitespace の色で、行末の $ が NonText の色であることを tmux で確かめた。範囲は tree-sitter の Go の文法の
// comment と raw_string_literal の節（コメントは行末の空白を含む）に合わせた。
//
// req: FR-170
func TestWhitespaceOverSyntax(t *testing.T) {
	ws, nt, sk := Attr{Fg: Indexed(1)}, Attr{Fg: Indexed(2)}, Attr{Fg: Indexed(5)}
	comment, str := Attr{Fg: Indexed(4)}, Attr{Fg: Indexed(3)}
	r := &Renderer{Width: 40, Height: 14, Theme: Theme{
		StyleWhitespace: ws, StyleNonText: nt, StyleSpecialKey: sk, StyleComment: comment, StyleString: str,
		StyleDiagnosticError: {Undercurl: true, Sp: Indexed(9)},
	}}
	r.Symbols = lcs(map[string]string{"tab": ">-", "lead": ".", "trail": ".", "eol": "$"})
	lines := []string{
		"package p",
		"",
		"// comment   ",
		"var s = `raw",
		"    indented",
		"\ttabbed",
		"`",
		"",
		"/*",
		"    block",
		"*/ // a\x01b",
	}
	b := buffer.New(strings.Join(lines, "\n") + "\n")
	decos := NewDecorations([]Highlight{
		{Range: buffer.Range{Start: pos(2, 0), End: pos(2, 13)}, Style: StyleComment},
		{Range: buffer.Range{Start: pos(3, 8), End: pos(6, 1)}, Style: StyleString},
		{Range: buffer.Range{Start: pos(8, 0), End: pos(10, 2)}, Style: StyleComment},
		{Range: buffer.Range{Start: pos(10, 3), End: pos(10, 10)}, Style: StyleComment},
	}, b.LineCount())
	diag := []Diagnostic{{Range: buffer.Range{Start: pos(4, 0), End: pos(4, 2)}, Severity: SeverityError}}
	s := r.Render(&Frame{Text: b, Cursor: pos(0, 0), LineLayout: list, Decorations: decos, Diagnostics: diag})
	// 診断があるので、左の 2 列は印の列である（5 行目に診断の印 E を出す）。
	checkRows(t, "文字", s, []string{
		"  package p$", "  $", "  // comment...$", "  var s = `raw$", "E ....indented$", "  >-------tabbed$", "  `$", "  $", "  /*$",
		"  ....block$", "  */ // a^Ab$",
	})
	checks := []struct {
		name     string
		row, col int
		want     Attr
	}{
		{"コメントの文字", 2, 0, comment},
		{"コメントの行末の空白の記号", 2, 10, ws},
		{"コメントの行の行末の記号", 2, 13, nt},
		{"生の文字列の文字", 3, 8, str},
		{"生の文字列の中の行頭の空白の記号（診断の下線を上に重ねる）", 4, 0, ws.Over(Attr{Undercurl: true, Sp: Indexed(9)})},
		{"生の文字列の中の行頭の空白の記号", 4, 2, ws},
		{"生の文字列の中の文字", 4, 4, str},
		{"生の文字列の中のタブ文字の記号", 5, 0, ws},
		{"生の文字列の中のタブ文字の記号の続き", 5, 7, ws},
		{"ブロックコメントの中の行頭の空白の記号", 9, 0, ws},
		{"ブロックコメントの中の文字", 9, 4, comment},
		{"コメントの中の ^A", 10, 7, sk},
		{"コメントの中の ^A の後の文字", 10, 9, comment},
	}
	for _, c := range checks {
		if got := s.Grid.At(c.row, signColumnWidth+c.col).Attr; got != c.want {
			t.Errorf("%s の色 = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// 設定が偽（既定）なら、空白の記号を持つ前と同じに描く。:set list! で切り替えても、描くたびに Frame の LineLayout を読む。
//
// req: FR-170
func TestWhitespaceToggle(t *testing.T) {
	b := whitespaceText()
	r := newRenderer(30, 14)
	off := []string{"        x", "あ      x", "  lead  mid  trail", "", "", nbspLine, "         x"}
	checkRows(t, "設定が偽", r.Render(&Frame{Text: b}), off)
	checkRows(t, "設定が真", r.Render(&Frame{Text: b, LineLayout: list}), []string{">       x"})
	checkRows(t, "偽に戻す", r.Render(&Frame{Text: b}), off)
}
