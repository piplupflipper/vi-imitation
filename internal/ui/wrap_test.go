package ui

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// wrapText は、折り返しの確かめに使うテキストである。2026-11-20 に nvim 0.12.5（--clean）で同じテキストを tmux の 41 列 9 行の画面に開き、
// 画面を tmux capture-pane で読んで期待値にした。
//
//	1 行目: 短い行
//	2 行目: 93 文字の半角の行（41 列なら 3 行、37 列なら 3 行）
//	3 行目: 35 文字の全角の行（行の最後の 1 列に全角文字が入らない）
//	4 行目: タブを 2 つ含む行
//	5 行目: 40 文字の後にタブ（41 列なら、タブが見た目の行の右の端で分かれる）
//	7 行目: 400 文字の行（表示領域より高い）
func wrapText() *buffer.Buffer {
	lines := []string{
		"line1 short",
		"0123456789abcdefghij0123456789ABCDEFGHIJ0123456789abcdefghij0123456789ABCDEFGHIJ0123456789xyz",
		"あいうえおかきくけこさしすせそたちつてとなにぬねのはひふへほまみむめも",
		"abcdefghijklmnopqrstuvwxyzabcdefghijkl\tTAB\tx",
		strings.Repeat("a", 40) + "\tX",
		"line6",
		strings.Repeat("0123456789", 40),
	}
	for i := 8; i <= 20; i++ {
		lines = append(lines, "line"+strconv.Itoa(i))
	}
	return buffer.New(strings.Join(lines, "\n") + "\n")
}

// 行番号の列を出して（本文は 37 列）、カーソルを下へ動かしたときの画面である。期待値は nvim 0.12.5 の :set number の画面と同じにした。
//
//   - 表示領域の最後に収まりきらない 4 行目は、入るところまで描き、右の端に "@@@" を出す
//   - 続きの見た目の行の行番号の列は空ける。全角文字が入らない最後の 1 列には ">" を出す
//   - 4 行目へ動くと、4 行目の全体が入るまで行の単位でスクロールする（1 行目を外す）。5 行目へ動くと 2 行目を外す
//   - 表示領域より高い 7 行目の先頭にカーソルを置くと、7 行目を最初の行にして先頭から描き、"@@@" は出さない
//   - 7 行目の行末（$）へ動くと、カーソルの見た目の行が最後の行に来るまで行の途中から描き、最初の行の左に "<<<" を出す
//
// req: FR-156
func TestGoldenWrapNumber(t *testing.T) {
	b := wrapText()
	r := newRenderer(41, 9)
	ln := LineNumbers{Show: true, MinWidth: 4}
	frame := func(line, byteOff int) Frame {
		return Frame{Text: b, Cursor: pos(line, byteOff), LineNumbers: ln, Wrap: true}
	}
	golden(t, "wrap_number", renderSteps(r, []step{
		{"1 行目", frame(0, 0)},
		{"jjj で 4 行目", frame(3, 0)},
		{"j で 5 行目", frame(4, 0)},
		{"jj で 7 行目", frame(6, 0)},
		{"$ で 7 行目の行末", frame(6, 399)},
	}))
}

// 行番号の列を出さず（本文は 41 列）に、表示領域より高い 7 行目の中で動いたときの画面である（nvim 0.12.5 の gj、gk と同じ）。
//
//   - 行の途中から描くのは、カーソルの見た目の行が隠れたときだけで、隠れなくなる最小の分だけ動かす
//   - 7 行目から 8 行目へ動くと、7 行目は途中からは描かず、8 行目を最初の行にする
//
// req: FR-156
func TestGoldenWrapSkip(t *testing.T) {
	b := wrapText()
	r := newRenderer(41, 9)
	frame := func(line, byteOff int) Frame {
		return Frame{Text: b, Cursor: pos(line, byteOff), Wrap: true}
	}
	golden(t, "wrap_skip", renderSteps(r, []step{
		{"7 行目の先頭", frame(6, 0)},
		{"7gj で 8 番目の見た目の行", frame(6, 287)},
		{"gj で 9 番目", frame(6, 328)},
		{"gj で 10 番目（最後）", frame(6, 369)},
		{"4gk で 6 番目", frame(6, 205)},
		{"3gk で 3 番目", frame(6, 82)},
		{"j で 8 行目", frame(7, 4)},
	}))
}

// 折り返したときの文字の置き方と色である。
//
//   - タブは見た目の行の右の端で分け、残りを次の見た目の行に描く（5 行目）
//   - 選択の色、検索の一致の色、範囲の色、診断の下線は、見た目の行をまたいで続けて付く
//   - 続きの見た目の行の記号（lineWrap.showBreak）は、続きの見た目の行の頭に nontext の色で描き、その右から文字を描く
//
// req: FR-156
func TestGoldenWrapLayers(t *testing.T) {
	b := wrapText()
	r := newRenderer(41, 12)
	r.Theme[StyleSearch] = Attr{Underline: true} // ゴールデンの飾りの '_'
	sel := Selection{Active: true, Start: pos(1, 35), End: pos(1, 45)}
	diag := []Diagnostic{{Range: buffer.Range{Start: pos(3, 36), End: pos(3, 41)}, Severity: SeverityError}}
	hl := []Highlight{{Range: buffer.Range{Start: pos(2, 54), End: pos(2, 66)}, Style: StyleDiagnosticWarning}}
	golden(t, "wrap_layers", renderSteps(r, []step{
		{"選択と診断と範囲の色", Frame{Text: b, Mode: ModeVisual, Cursor: pos(1, 45), Selection: sel, Diagnostics: diag, Highlights: hl, Wrap: true}},
		// 2 行目の IJ0123 の 1 つ目（38 バイト目から）は、1 つ目と 2 つ目の見た目の行にまたがる。
		{"検索の一致の色", Frame{Text: b, Cursor: pos(0, 0), Search: regexp.MustCompile("IJ0123"), Wrap: true}},
	}))

	r = newRenderer(41, 12)
	r.Symbols = Symbols{SymbolShowBreak: ">>"}
	golden(t, "wrap_showbreak", renderSteps(r, []step{
		{"続きの行の記号", Frame{Text: b, Cursor: pos(0, 0), Wrap: true}},
	}))
}

// 折り返さないとき（既定）は、Frame.Wrap を持つ前と同じに描く。横にスクロールし、"@@@" を出さない。
// 折り返したり戻したり（:set wrap!）しても、描くたびに Frame.Wrap を読む。
//
// req: FR-156
func TestWrapToggle(t *testing.T) {
	b := wrapText()
	r := newRenderer(41, 9)
	// 行末の z（92 列目）が画面の外に出るので、z を画面の幅の半分（20 列目）に置く位置（72 列目から）へ横にスクロールする。
	s := r.Render(&Frame{Text: b, Cursor: pos(1, 92)})
	if got := strings.TrimRight(s.Grid.RowText(1), " "); got != "CDEFGHIJ0123456789xyz" {
		t.Errorf("折り返さないときの 2 行目 = %q", got)
	}
	if got := strings.Join(rowTexts(s, 7), "|"); strings.Contains(got, lastLineMark) {
		t.Errorf("折り返さないときに %s を出した: %q", lastLineMark, got)
	}
	s = r.Render(&Frame{Text: b, Cursor: pos(1, 92), Wrap: true})
	if got := rowTexts(s, 4); got[1] != "0123456789abcdefghij0123456789ABCDEFGHIJ0" || got[3] != "23456789xyz" {
		t.Errorf("折り返したときの行 = %q", got)
	}
	if s.Cursor.Row != 3 || s.Cursor.Col != 10 {
		t.Errorf("折り返したときのカーソル = %d,%d, want 3,10", s.Cursor.Row, s.Cursor.Col)
	}
	s = r.Render(&Frame{Text: b, Cursor: pos(1, 92)})
	if got := strings.TrimRight(s.Grid.RowText(1), " "); got != "CDEFGHIJ0123456789xyz" {
		t.Errorf("戻したときの 2 行目 = %q", got)
	}
}

// wrapper の数え方である。半角、全角（行の最後の 1 列に入らない）、タブ（右の端で分かれる）、続きの行の記号の列を確かめる。
//
// req: FR-156, FR-157
func TestWrapper(t *testing.T) {
	w := wrapper{width: 10, rule: lineRule{tabStop: 8}}
	tests := []struct {
		s    string
		rows int
	}{
		{"", 1},
		{"0123456789", 1},
		{"0123456789a", 2},
		{"あいうえおか", 2},       // 10 列で 5 文字、6 文字目は次の行
		{"aあいうえおか", 2},      // 1 列目から 4 文字（9 列）、10 列目に ">"、残り 2 文字
		{"012345678\tx", 2}, // タブは 9 列目から 16 列目の手前まで。10 列目で分かれる
	}
	for _, tt := range tests {
		if got := w.rows(0, tt.s); got != tt.rows {
			t.Errorf("rows(%q) = %d, want %d", tt.s, got, tt.rows)
		}
	}
	s := "aあいうえおか"
	// か（10 バイト目からの 2 文字目の全角の後）: "a" と 4 文字で 9 列、"お" は送られて 2 行目の 0 列。
	if p := w.cursor(0, s, 13, false); p != (wrapPos{row: 1, x: 0}) {
		t.Errorf("cursor(お) = %+v, want 1,0", p)
	}
	// 表示の列は ">" の列（9 列目）も数えるので、お は 10 列目、か は 12 列目である。">" の列は、その後の お の列として数える。
	l := WrapLines{w: w}
	for _, tt := range []struct{ off, col int }{{0, 0}, {10, 7}, {13, 10}, {16, 12}, {len(s), 14}} {
		if c := l.Column(0, s, tt.off); c != tt.col {
			t.Errorf("Column(%d) = %d, want %d", tt.off, c, tt.col)
		}
	}
	for _, tt := range []struct{ col, off int }{{8, 10}, {9, 13}, {10, 13}, {11, 13}, {12, 16}, {100, 16}} {
		if b := l.ByteAt(0, s, tt.col); b != tt.off {
			t.Errorf("ByteAt(%d) = %d, want %d", tt.col, b, tt.off)
		}
	}
	// ">" の後のタブの幅も、">" の列を数えた列で決める（nvim 0.12.5 と同じ。PR #310 のレビューの必須 3）。
	// b は 12 列目、タブは 13 列目から 16 列目の手前までの 3 列で、c は 16 列目（2 行目の 6 列目）。
	padTab := "aあいうえおb\tc"
	if p := w.cursor(0, padTab, len(padTab)-1, false); p != (wrapPos{row: 1, x: 6}) {
		t.Errorf("cursor(c) = %+v, want 1,6", p)
	}
	tab := "012345678\tx"
	// タブの最後のセル（15 列目）は 2 行目の 5 列目。x は 16 列目で 2 行目の 6 列目。
	if p := w.cursor(0, tab, 9, true); p != (wrapPos{row: 1, x: 5}) {
		t.Errorf("cursor(タブ, 最後のセル) = %+v, want 1,5", p)
	}
	if p := w.cursor(0, tab, 9, false); p != (wrapPos{row: 0, x: 9}) {
		t.Errorf("cursor(タブ, 最初のセル) = %+v, want 0,9", p)
	}
	if c := l.Column(0, tab, 9); c != 15 {
		t.Errorf("Column(タブ) = %d, want 15（最後のセル）", c)
	}

	sb := wrapper{width: 10, sbw: 2, rule: lineRule{tabStop: 8}}
	// 続きの行は 2 列目から 8 列ずつ。表示の列は記号の列も数える。
	if got := sb.rows(0, "0123456789abcdefghij"); got != 3 {
		t.Errorf("記号ありの rows = %d, want 3", got)
	}
	if p := sb.cursor(0, "0123456789abcdefghij", 18, false); p != (wrapPos{row: 2, x: 2}) {
		t.Errorf("記号ありの cursor(18) = %+v, want 2,2", p)
	}
	if c := (WrapLines{w: sb}).Column(0, "0123456789abcdefghij", 18); c != 22 {
		t.Errorf("記号ありの Column(18) = %d, want 22", c)
	}
	// 記号の後のタブの幅も、記号の列を数えた列で決める（nvim 0.12.5 の showbreak=>> と同じ）。
	// 2 行目の a は 12 列目から、タブは 15 列目から 16 列目の手前までの 1 列、Y は 16 列目（2 行目の 6 列目）。
	if p := sb.cursor(0, "0123456789aaa\tY", 14, false); p != (wrapPos{row: 1, x: 6}) {
		t.Errorf("記号の後のタブ: cursor(Y) = %+v, want 1,6", p)
	}
}

// インサートモードで、ちょうど見た目の行の幅で終わる行の行末にいるカーソルは、次の見た目の行の左の端に置く（nvim 0.12.5 と同じ）。
//
// req: FR-156
func TestWrapCursorAtEOL(t *testing.T) {
	b := buffer.New(strings.Repeat("b", 10) + "\nnext\n")
	r := newRenderer(10, 5)
	s := r.Render(&Frame{Text: b, Mode: ModeInsert, Cursor: pos(0, 10), Wrap: true})
	if s.Cursor.Row != 1 || s.Cursor.Col != 0 {
		t.Errorf("カーソル = %d,%d, want 1,0", s.Cursor.Row, s.Cursor.Col)
	}
	if got := rowTexts(s, 2); got[1] != "next" {
		t.Errorf("行 = %q（行の見た目の行の数は増やさない）", got)
	}
}

// プレビュー（FR-149）の中身も、View.Wrap なら本文と同じく折り返して描き、色を付ける行（Line）の見た目の行のすべてに色を付ける。
//
// req: FR-156
func TestDrawPreviewWrap(t *testing.T) {
	theme := testTheme()
	lineAttr := Attr{Bg: RGB(1, 2, 3)}
	theme[StylePreviewLine] = lineAttr
	rule := PreviewRule{MinWindowWidth: 40, WidthPercent: 50, WindowWidthPercent: 100}
	text := buffer.New("short\n" + strings.Repeat("x", 60) + "\nend")
	r := &Renderer{Width: 60, Height: 14, Theme: theme}
	s := r.Render(&Frame{Text: buffer.New("x"), Float: &Float{
		ID: 1, View: &Frame{Text: buffer.New("one")}, Center: true, CursorLine: true, PreviewRule: rule,
		Preview: &Preview{ID: 1, Title: "t", View: &Frame{Text: text, Cursor: pos(1, 0), Wrap: true}, Line: 1},
	}})
	rows := strings.Split(s.Grid.String(), "\n")
	// プレビューは 30 列目から 30 列で、中身は 28 列。60 文字の行は 28、28、4 文字の 3 行になる。
	start := -1
	for i, row := range rows {
		if strings.Contains(row, "short") {
			start = i
		}
	}
	if start < 0 || !strings.Contains(rows[start+1], strings.Repeat("x", 28)) || !strings.Contains(rows[start+3], "xxxx ") ||
		!strings.Contains(rows[start+4], "end") {
		t.Fatalf("折り返したプレビュー:\n%s", s.Grid.String())
	}
	for i := start + 1; i <= start+3; i++ {
		if a := s.Grid.At(i, 58).Attr; a.Bg != lineAttr.Bg {
			t.Errorf("%d 行目（色を付ける行の見た目の行）の色 = %+v", i, a)
		}
	}
	for _, i := range []int{start, start + 4} {
		if a := s.Grid.At(i, 58).Attr; a.Bg == lineAttr.Bg {
			t.Errorf("%d 行目に色が付いた", i)
		}
	}
}
