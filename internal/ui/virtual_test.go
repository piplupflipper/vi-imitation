package ui

import (
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// vt は、1 つの文字列の仮想テキストである。
func vt(line, b int, p VirtualPlacement, text string) VirtualText {
	return VirtualText{Position: pos(line, b), Placement: p, Chunks: []VirtualChunk{{Text: text, Attr: Attr{Fg: RGB(255, 0, 0)}}}}
}

// virtualScreen は、2026-11-22 に nvim 0.12.5（--clean）の 30 列の画面で nvim_buf_set_extmark を並べたテキストと仮想テキストである
// （virtual.go の doc。期待値は tmux の capture-pane で読んだ画面）。
func virtualScreen() (*buffer.Buffer, *VirtualTexts) {
	b := buffer.New(strings.Join([]string{
		"abcdef",
		"abcdefghij",
		"    indented line",
		"short",
		"0123456789012345678901234567890123",
		"wide全角end",
		"tail",
		"",
	}, "\n") + "\n")
	v := NewVirtualTexts([]VirtualText{
		vt(0, 2, VirtualInline, "XXXX"),
		{Position: pos(1, 10), Placement: VirtualEOL, Chunks: []VirtualChunk{{Text: "EOL", Style: StyleComment}, {Text: "2", Style: StyleKeyword}}},
		vt(2, 0, VirtualOverlay, "|"),
		vt(3, 3, VirtualOverlay, "OVERLAYLONG"),
		vt(4, 20, VirtualInline, "INLINEINLINE"),
		vt(4, 34, VirtualEOL, "ENDEND"),
		vt(5, 4, VirtualOverlay, "V"),
		vt(6, 4, VirtualInline, "AT"),
		vt(6, 4, VirtualInline, "BT"),
		vt(6, 4, VirtualEOL, "E1"),
		vt(6, 0, VirtualEOL, "E2"),
	}, b.LineCount())
	return b, v
}

// TestVirtualTextPlacement は、inline、eol、overlay の 3 つの置き方を、行を折り返すときと折り返さずに横にスクロールしたときで、
// nvim 0.12.5 の画面と比べる。
//
//   - inline は本文を右へずらし、折り返すときは本文と一緒に折り返す（4 行目）。同じ位置の inline は一覧の順に並べる（7 行目）
//   - eol は行末のセルの次から、位置のバイトの順に 1 列ずつ空けて並べ、右の端で切る（折り返さないときの 4 行目は画面の外）
//   - overlay は本文を右へずらさず、行末より右にもはみ出して重ねる（4 行目）。全角文字の左半分に重ねたら、右半分は空白にする（6 行目）
//   - 横にスクロールして行が左の端より短いときは、eol を左の端の 1 列の次から描く（Neovim と同じ）。左の端より左に出た overlay は描かない
//     （Neovim は左の端に寄せて描く。virtual.go の doc の決め）
//
// req: FR-231, FR-233
func TestVirtualTextPlacement(t *testing.T) {
	b, v := virtualScreen()
	r := newRenderer(30, 14)
	f := &Frame{Text: b, LineLayout: LineLayout{Virtual: v}, Wrap: true}
	checkRows(t, "折り返す", r.Render(f), []string{
		"abXXXXcdef",
		"abcdefghij EOL2",
		"|   indented line",
		"shoOVERLAYLONG",
		"01234567890123456789INLINEINLI",
		"NE01234567890123 ENDEND",
		"wideV 角end",
		"tailATBT E2 E1",
		"",
		"~",
	})
	r = newRenderer(30, 14)
	f = &Frame{Text: b, LineLayout: LineLayout{Virtual: v}, Cursor: pos(4, 33)}
	s := r.Render(f)
	checkRows(t, "折り返さず、5 行目の行末へ横にスクロール", s, []string{
		"",
		" EOL2",
		"",
		"",
		"NE01234567890123 ENDEND",
		"",
		" E2 E1",
	})
	// 横の位置は、仮想テキストの列も数えたカーソルの列（45）から決める（nvim のルーラーは 5,34-46 で、左の端は 30 列目）。
	if r.Left() != 30 || s.Cursor.Col != 15 {
		t.Errorf("Left = %d, cursor col = %d, want 30, 15", r.Left(), s.Cursor.Col)
	}
	r = newRenderer(30, 14)
	f.Cursor = pos(0, 0)
	checkRows(t, "折り返さず、左の端", r.Render(f), []string{
		"abXXXXcdef",
		"abcdefghij EOL2",
		"|   indented line",
		"shoOVERLAYLONG",
		"01234567890123456789INLINEINLI",
		"wideV 角end",
		"tailATBT E2 E1",
	})
}

// TestVirtualTextEdges は、行を折り返すときの右の端（eol と overlay は次の見た目の行へ続けない）と、横にスクロールしたときの overlay を、
// nvim 0.12.5 の 30 列の画面と比べる。
//
// req: FR-233
func TestVirtualTextEdges(t *testing.T) {
	b := buffer.New(strings.Join([]string{
		"0123456789012345678901234567890123456789",
		"abcdefghijklmnopqrstuvwxy",
		"abcdefghijklmnopqrstuvwxyz1234",
		"short",
		"abcdefghijklmnopqrstuvwxyz123",
	}, "\n") + "\n")
	v := NewVirtualTexts([]VirtualText{
		vt(0, 2, VirtualOverlay, "OV"),
		vt(0, 35, VirtualOverlay, "OW"),
		vt(1, 25, VirtualEOL, "EOLTEXTLONG"),
		vt(2, 28, VirtualOverlay, "OVERLAYLONG"),
		vt(3, 1, VirtualInline, "INL"),
		vt(4, 29, VirtualEOL, "E"),
	}, b.LineCount())
	r := newRenderer(30, 12)
	f := &Frame{Text: b, LineLayout: LineLayout{Virtual: v}, Wrap: true}
	checkRows(t, "折り返す", r.Render(f), []string{
		"01OV45678901234567890123456789",
		"01234OW789",
		"abcdefghijklmnopqrstuvwxy EOLT",
		"abcdefghijklmnopqrstuvwxyz12OV",
		"sINLhort",
		"abcdefghijklmnopqrstuvwxyz123",
		"~",
	})
	// nvim --clean の 'sidescroll' の既定は 1 で、1G$ は左の端を 10 列目にした。本体は Vim の既定の 0 に合わせて画面の中ほどへ送る
	// （scroll の doc）ので、左の端を 10 列目に置いてから描いて比べる。
	r = newRenderer(30, 12)
	r.SetScroll(0, 10)
	f = &Frame{Text: b, LineLayout: LineLayout{Virtual: v}, Cursor: pos(0, 39)}
	checkRows(t, "折り返さず、1 行目の行末", r.Render(f), []string{
		"0123456789012345678901234OW789",
		"klmnopqrstuvwxy EOLTEXTLONG",
		"klmnopqrstuvwxyz12OVERLAYLONG",
		"",
		"klmnopqrstuvwxyz123 E",
	})
}

// TestVirtualTextLayout は、タブ文字の幅、空白の記号の行末の記号、ちょうど本文の幅で終わる行、空の行、同じ位置の inline と overlay を、
// nvim 0.12.5 の 30 列の画面（:set list listchars=tab:>-,eol:$ と :set nolist）と比べる。
//
// req: FR-231, FR-233
func TestVirtualTextLayout(t *testing.T) {
	b := buffer.New(strings.Join([]string{
		"ab\tc",
		"abc",
		"abcdefghijklmnopqrstuvwxyz1",
		"abcdefghijklmnopqrstuvwxyz12",
		"abcdefghijklmnopqrstuvwxyz",
		"",
		"abcdef",
		"abcdef",
	}, "\n") + "\n")
	v := NewVirtualTexts([]VirtualText{
		vt(0, 0, VirtualInline, "XXX"),
		vt(1, 3, VirtualEOL, "EOL"),
		vt(2, 27, VirtualInline, "I"),
		vt(3, 28, VirtualInline, "IJ"),
		vt(4, 26, VirtualInline, "全角"),
		vt(5, 0, VirtualInline, "ABC"),
		vt(6, 2, VirtualInline, "INL"),
		vt(6, 2, VirtualOverlay, "O"),
		vt(7, 1, VirtualOverlay, "OVR"),
	}, b.LineCount())
	r := newRenderer(30, 16)
	r.Symbols = lcs(map[string]string{"tab": ">-", "eol": "$"})
	checkRows(t, "list", r.Render(&Frame{Text: b, LineLayout: LineLayout{Virtual: v, Whitespace: true}, Wrap: true}), []string{
		"XXXab>--c$",
		"abc$EOL",
		"abcdefghijklmnopqrstuvwxyz1I$",
		"abcdefghijklmnopqrstuvwxyz12IJ",
		"$",
		"abcdefghijklmnopqrstuvwxyz全角",
		"$",
		"ABC$",
		"abINLOdef$",
		"aOVRef$",
	})
	r = newRenderer(30, 16)
	checkRows(t, "nolist", r.Render(&Frame{Text: b, LineLayout: LineLayout{Virtual: v}, Wrap: true}), []string{
		"XXXab   c",
		"abc EOL",
		"abcdefghijklmnopqrstuvwxyz1I",
		"abcdefghijklmnopqrstuvwxyz12IJ",
		"abcdefghijklmnopqrstuvwxyz全角",
		"ABC",
		"abINLOdef",
		"aOVRef",
		"~",
	})
}

// TestVirtualTextCursor は、inline の仮想テキストのある行のカーソルの画面の列を、nvim 0.12.5 と比べる。
// ノーマルモードの位置の文字の上のカーソルは仮想テキストの後ろ、インサートモードのカーソルと行末のカーソル（空の行を含む）は前に置く。
// 折り返すときも、折り返さないときも同じにする。
//
// req: FR-232
func TestVirtualTextCursor(t *testing.T) {
	b := buffer.New("abcdef\nabcdefghijklmnopqrstuvwxyz12\n\naIbcZ\n")
	v := NewVirtualTexts([]VirtualText{
		vt(0, 2, VirtualInline, "XXXX"),
		vt(1, 28, VirtualInline, "IJ"),
		vt(2, 0, VirtualInline, "ABC"),
		vt(3, 5, VirtualInline, "Z"),
	}, b.LineCount())
	tests := []struct {
		name     string
		cur      buffer.Position
		mode     Mode
		row, col int
	}{
		{"ノーマルモードの c", pos(0, 2), ModeNormal, 0, 6},
		{"ノーマルモードの b", pos(0, 1), ModeNormal, 0, 1},
		{"ノーマルモードの e", pos(0, 4), ModeNormal, 0, 8},
		{"インサートモードの c の前", pos(0, 2), ModeInsert, 0, 2},
		{"インサートモードの d の前", pos(0, 3), ModeInsert, 0, 7},
		{"インサートモードの行末（4GA）", pos(1, 28), ModeInsert, 1, 28},
		{"空の行", pos(2, 0), ModeNormal, 2, 0},
		{"空の行のインサートモード", pos(2, 0), ModeInsert, 2, 0},
		{"行末の位置の仮想テキストの前の最後の文字（4G$）", pos(3, 4), ModeNormal, 3, 4},
	}
	for _, wrap := range []bool{false, true} {
		for _, tt := range tests {
			r := newRenderer(30, 8)
			s := r.Render(&Frame{Text: b, LineLayout: LineLayout{Virtual: v}, Wrap: wrap, Cursor: tt.cur, Mode: tt.mode})
			if s.Cursor.Row != tt.row || s.Cursor.Col != tt.col {
				t.Errorf("%s（wrap %v）: cursor = %d,%d, want %d,%d", tt.name, wrap, s.Cursor.Row, s.Cursor.Col, tt.row, tt.col)
			}
		}
	}
}

// TestVirtualTextWrapCursor は、行を折り返すときのカーソルの位置（wrapper.cursor）が、行の番号で引いた仮想テキストを数え、
// 見た目の行の右の端で折り返した inline の後ろの文字を次の見た目の行に置くことを確かめる。インサートモードでは仮想テキストの前に置く。
//
// req: FR-232, FR-233
func TestVirtualTextWrapCursor(t *testing.T) {
	v := NewVirtualTexts([]VirtualText{vt(1, 8, VirtualInline, "XXXX")}, 2)
	w := wrapper{width: 10, rule: lineRule{tabStop: 8, virt: v}}
	s := "abcdefghij"
	if p := w.cursor(1, s, 8, true); p != (wrapPos{row: 1, x: 2}) {
		t.Errorf("ノーマルモードの i = %+v, want 1,2", p)
	}
	if p := w.cursor(1, s, 8, false); p != (wrapPos{row: 0, x: 8}) {
		t.Errorf("インサートモードの i の前 = %+v, want 0,8", p)
	}
	if p := w.cursor(0, s, 8, true); p != (wrapPos{row: 0, x: 8}) {
		t.Errorf("仮想テキストの無い行の i = %+v, want 0,8", p)
	}
	if got := w.rows(1, s); got != 2 {
		t.Errorf("rows = %d, want 2", got)
	}
}

// TestVirtualTextColumns は、望みの列の数え方（Columns と WrapLines）が inline の仮想テキストの列も数え、列から文字を引くときは
// 仮想テキストの列を後ろの文字として引くことを確かめる。値は nvim 0.12.5 の virtcol() と、j と k の移り先と、マウスのクリックの移り先である
// （"abcdef" の 2 バイト目に "XXXX" のとき、e の virtcol は 9、下の行の 3〜6 列目からの k は c、仮想テキストの上のクリックは c）。
//
// req: FR-232
func TestVirtualTextColumns(t *testing.T) {
	b := buffer.New("abcdef\nab\tc\n")
	v := NewVirtualTexts([]VirtualText{vt(0, 2, VirtualInline, "XXXX"), vt(0, 6, VirtualInline, "EOL"), vt(1, 0, VirtualInline, "XXX")}, b.LineCount())
	r := newRenderer(30, 8)
	r.Render(&Frame{Text: b, LineLayout: LineLayout{Virtual: v}})
	counts := []struct {
		name string
		col  interface {
			Column(n int, line string, byteOff int) int
			ByteAt(n int, line string, col int) int
		}
	}{
		{"Columns", Columns{LineLayout: LineLayout{Virtual: v}}},
		{"WrapLines", r.WrapLines(LineLayout{Virtual: v})},
	}
	line := b.Line(0)
	for _, c := range counts {
		for _, tt := range []struct{ off, col int }{{0, 0}, {1, 1}, {2, 6}, {4, 8}, {5, 9}, {6, 10}} {
			if got := c.col.Column(0, line, tt.off); got != tt.col {
				t.Errorf("%s.Column(%d) = %d, want %d", c.name, tt.off, got, tt.col)
			}
		}
		// 行の先頭からの部分の文字列（インサートモードの望みの列）は、部分の後ろの位置の仮想テキストを数えない。
		if got := c.col.Column(0, line[:2], 2); got != 2 {
			t.Errorf("%s.Column(%q, 2) = %d, want 2", c.name, line[:2], got)
		}
		for _, tt := range []struct{ col, off int }{{1, 1}, {2, 2}, {5, 2}, {6, 2}, {8, 4}, {9, 5}, {10, 5}, {12, 5}, {99, 5}} {
			if got := c.col.ByteAt(0, line, tt.col); got != tt.off {
				t.Errorf("%s.ByteAt(%d) = %d, want %d", c.name, tt.col, got, tt.off)
			}
		}
		// タブ文字の幅は、前の仮想テキストの列も数えて決める（"XXXab" の後のタブは 3 列）。
		if got := c.col.Column(1, b.Line(1), 3); got != 8 {
			t.Errorf("%s.Column(1, c) = %d, want 8", c.name, got)
		}
		// 別の行の番号では、その行の仮想テキストを数えない。
		if got := c.col.Column(5, line, 4); got != 4 {
			t.Errorf("%s.Column(5, e) = %d, want 4", c.name, got)
		}
	}
	// 行を折り返すときの見た目の行の数は inline を数え、eol と overlay を数えない。
	w := r.WrapLines(LineLayout{Virtual: NewVirtualTexts([]VirtualText{
		vt(0, 6, VirtualEOL, strings.Repeat("E", 40)), vt(0, 0, VirtualOverlay, strings.Repeat("O", 40)), vt(1, 0, VirtualInline, strings.Repeat("I", 30)),
	}, 2)})
	if got := w.Rows(0, line); got != 1 {
		t.Errorf("Rows(eol と overlay) = %d, want 1", got)
	}
	if got := w.Rows(1, b.Line(1)); got != 2 {
		t.Errorf("Rows(inline) = %d, want 2", got)
	}
}

// TestVirtualTextAttr は、仮想テキストのセルを拡張機能が付けた色（名前の色の上に直の色）だけで描き、本文の構文の色、検索の一致、選択の色を
// 重ねないことを確かめる（Neovim の hl_mode の既定の replace。nvim 0.12.5 の v で、選択の中の inline の仮想テキストは選択の色にならない）。
//
// req: FR-234
func TestVirtualTextAttr(t *testing.T) {
	b := buffer.New("abcdef\n")
	v := NewVirtualTexts([]VirtualText{
		{Position: pos(0, 2), Placement: VirtualInline, Chunks: []VirtualChunk{{Text: "X", Style: StyleComment, Attr: Attr{Bold: true}}, {Text: "Y", Attr: Attr{Bg: RGB(1, 2, 3)}}}},
		{Position: pos(0, 4), Placement: VirtualOverlay, Chunks: []VirtualChunk{{Text: "O", Style: StyleKeyword}}},
	}, 1)
	r := newRenderer(20, 5)
	s := r.Render(&Frame{
		Text: b, LineLayout: LineLayout{Virtual: v}, Cursor: pos(0, 5), Mode: ModeVisual,
		Selection:   Selection{Active: true, Start: pos(0, 0), End: pos(0, 5)},
		Decorations: NewDecorations([]Highlight{{Range: buffer.Range{Start: pos(0, 0), End: pos(0, 6)}, Style: StyleString}}, 1),
	})
	if got := s.Grid.RowText(0); !strings.HasPrefix(got, "abXYcdOf") {
		t.Fatalf("row = %q", got)
	}
	want := []Attr{
		{Fg: Indexed(4), Reverse: true}, {Fg: Indexed(4), Reverse: true}, // ab: 構文の色と選択
		{Fg: Indexed(2), Bold: true}, {Bg: RGB(1, 2, 3)}, // XY: 仮想テキストの色だけ
		{Fg: Indexed(4), Reverse: true}, {Fg: Indexed(4), Reverse: true}, // cd
		{Fg: Indexed(3)}, // O: overlay の色だけ（下の e の選択と構文の色を重ねない）
	}
	for i, a := range want {
		if got := s.Grid.At(0, i).Attr; got != a {
			t.Errorf("col %d attr = %+v, want %+v", i, got, a)
		}
	}
}

// TestInlayHintColor は、インレイヒントの色の名前 inlayHint が、既定の対応にも配色にも無ければ nontext の今の色を引き継ぎ
// （Neovim 0.12.5 の LspInlayHint は NonText へのリンク）、書けばその色で描くことを、inline の仮想テキストの描画で確かめる。
// 前後の空白（paddingLeft と paddingRight）の文字列には色の名前を付けないので、本文の背景のまま描く。
//
// req: FR-258
func TestInlayHintColor(t *testing.T) {
	def := DefaultTheme()
	if _, ok := def[StyleInlayHint]; ok {
		t.Fatal("既定の対応に inlayHint がある（nontext を引き継がない）")
	}
	if got := def.attr(StyleInlayHint); got != def[StyleNonText] {
		t.Errorf("既定の inlayHint = %+v, want nontext の %+v", got, def[StyleNonText])
	}
	if !IsStyleName(StyleInlayHint) {
		t.Error("inlayHint が設定の colors に書ける名前でない")
	}
	b := buffer.New("x := f(1)\n")
	v := NewVirtualTexts([]VirtualText{{Position: pos(0, 1), Placement: VirtualInline, Chunks: []VirtualChunk{
		{Text: " "}, {Text: "int", Style: StyleInlayHint},
	}}}, 1)
	for _, tc := range []struct {
		name  string
		theme Theme
		want  Attr
	}{
		{"nontext を引き継ぐ", testTheme(), Attr{Fg: Indexed(5)}},
		{"書いた inlayHint", testTheme().WithColors(map[string]Attr{StyleInlayHint: {Fg: RGB(0x8b, 0x80, 0xa8)}}), Attr{Fg: RGB(0x8b, 0x80, 0xa8)}},
	} {
		r := &Renderer{Width: 20, Height: 3, Theme: tc.theme}
		s := r.Render(&Frame{Text: b, LineLayout: LineLayout{Virtual: v}, Cursor: pos(0, 0)})
		if got := s.Grid.RowText(0); !strings.HasPrefix(got, "x int := f(1)") {
			t.Fatalf("%s: row = %q", tc.name, got)
		}
		if got := s.Grid.At(0, 1).Attr; got != (Attr{}) {
			t.Errorf("%s: padding attr = %+v, want none", tc.name, got)
		}
		for col := 2; col < 5; col++ {
			if got := s.Grid.At(0, col).Attr; got != tc.want {
				t.Errorf("%s: col %d attr = %+v, want %+v", tc.name, col, got, tc.want)
			}
		}
	}
}

// TestDecorationAttr は、範囲に直に付けた色（Highlight.Attr。FR-230）を、名前の色の上に重ねて描くことと、名前の無い範囲も直の色で描くことを
// 確かめる。直の色の違う同じ名前の範囲は、別の組として描く。
//
// req: FR-230
func TestDecorationAttr(t *testing.T) {
	b := buffer.New("abcdef\nxyz\n")
	hs := []Highlight{
		{Range: buffer.Range{Start: pos(0, 0), End: pos(0, 2)}, Style: StyleComment},
		{Range: buffer.Range{Start: pos(0, 2), End: pos(0, 4)}, Style: StyleComment, Attr: &Attr{Bg: RGB(255, 0, 0), Italic: true}},
		{Range: buffer.Range{Start: pos(0, 4), End: pos(1, 1)}, Attr: &Attr{Fg: RGB(0, 0, 255)}},
	}
	r := newRenderer(20, 5)
	s := r.Render(&Frame{Text: b, Decorations: NewDecorations(hs, b.LineCount())})
	want := []struct {
		row, col int
		a        Attr
	}{
		{0, 0, Attr{Fg: Indexed(2)}},
		{0, 2, Attr{Fg: Indexed(2), Bg: RGB(255, 0, 0), Italic: true}},
		{0, 4, Attr{Fg: RGB(0, 0, 255)}},
		{1, 0, Attr{Fg: RGB(0, 0, 255)}},
		{1, 1, Attr{}},
	}
	for _, tt := range want {
		if got := s.Grid.At(tt.row, tt.col).Attr; got != tt.a {
			t.Errorf("(%d,%d) attr = %+v, want %+v", tt.row, tt.col, got, tt.a)
		}
	}
}

// TestVirtualTextHit は、マウスの位置を解いた表示の列を、望みの列の数え方で文字へ引くと、仮想テキストの上のクリックが後ろの文字になる
// ことを確かめる（nvim 0.12.5 で、"abXXXXcdef" の 3〜7 列目のクリックは、どれも c へ移った）。
//
// req: FR-232
func TestVirtualTextHit(t *testing.T) {
	b := buffer.New("abcdef\n")
	l := LineLayout{Virtual: NewVirtualTexts([]VirtualText{vt(0, 2, VirtualInline, "XXXX")}, 1)}
	for _, wrap := range []bool{false, true} {
		r := newRenderer(20, 5)
		r.Render(&Frame{Text: b, LineLayout: l, Wrap: wrap})
		for col := 2; col <= 6; col++ {
			h := r.HitTest(0, col, HitNone)
			var got int
			if wrap {
				got = r.WrapLines(l).ByteAt(h.Line, b.Line(h.Line), h.Col)
			} else {
				got = Columns{LineLayout: l}.ByteAt(h.Line, b.Line(h.Line), h.Col)
			}
			if got != 2 {
				t.Errorf("wrap %v: click at col %d = byte %d, want 2", wrap, col, got)
			}
		}
	}
}

// TestVirtualTextDrawOrder は、同じ位置の inline と overlay を一覧の順に描くことと、行末の overlay の上に eol を描くことを、
// Neovim 0.12.5 の画面と比べる（PR #378 のレビューの推奨 2、3。先に置いた overlay は inline の文字の上に重なり、後に置いた overlay は
// inline の後ろの文字に重なる。xyz の行末の overlay と eol は xyzOEOLTEXT になる）。
//
// req: FR-231
func TestVirtualTextDrawOrder(t *testing.T) {
	b := buffer.New("\n\nxyz\n")
	v := NewVirtualTexts([]VirtualText{
		vt(0, 0, VirtualOverlay, "    |"),
		vt(0, 0, VirtualInline, "INL"),
		vt(1, 0, VirtualInline, "INL"),
		vt(1, 0, VirtualOverlay, "    |"),
		vt(2, 3, VirtualOverlay, "OVERLAY"),
		vt(2, 3, VirtualEOL, "EOLTEXT"),
	}, b.LineCount())
	for _, wrap := range []bool{false, true} {
		r := newRenderer(30, 6)
		checkRows(t, "順", r.Render(&Frame{Text: b, LineLayout: LineLayout{Virtual: v}, Wrap: wrap}), []string{"    |", "INL    |", "xyzOEOLTEXT"})
	}
}

// TestVirtualLayerApplyEdit は、編集に合わせた仮想テキストのずらし方を、PR #378 のレビューの推奨 1 の表の Neovim 0.12.5 の画面と比べる。
// 編集は、J と gUU が作るもの（つなぐ箇所の改行と空白だけ、変わる文字だけを置き換える。internal/command の actJoin と caseEdits）である。
// 位置が編集の範囲の中か端にあれば入れたテキストの後ろへ、範囲より後ろにあれば行と列をずらす（Neovim の extmark の right_gravity）。
//
// req: FR-231
func TestVirtualLayerApplyEdit(t *testing.T) {
	text := "\tif x {\n\t\ty := f(a, b)\n"
	vts := []VirtualText{
		vt(1, 3, VirtualInline, ": int"),
		vt(1, 9, VirtualInline, "x: "),
		vt(1, 0, VirtualOverlay, "║"),
		vt(0, 0, VirtualOverlay, "╔══════▶"),
	}
	render := func(b *buffer.Buffer, v *VirtualTexts) []string {
		return rowTexts(newRenderer(60, 6).Render(&Frame{Text: b, LineLayout: LineLayout{Virtual: v}}), 2)
	}
	apply := func(eds ...buffer.Edit) (*buffer.Buffer, *VirtualTexts) {
		b := buffer.New(text)
		l := NewVirtualLayer(vts, b.LineCount())
		v := &VirtualTexts{}
		v.SetLayers([]*VirtualLayer{l})
		gen := v.Gen()
		if _, err := b.Apply(buffer.Transaction{BaseVersion: b.Version(), Edits: eds}); err != nil {
			t.Fatal(err)
		}
		for _, ed := range eds {
			l.ApplyEdit(ed, false)
		}
		v.Changed()
		if v.Gen() == gen {
			t.Error("Changed が世代の番号を進めない")
		}
		return b, v
	}
	b, v := apply(buffer.Edit{Range: buffer.Range{Start: pos(0, 7), End: pos(1, 2)}, Text: " "})
	if got, want := render(b, v)[0], "╔══════▶if x { ║: int := f(x: a, b)"; got != want {
		t.Errorf("J = %q, want %q", got, want)
	}
	b, v = apply(
		buffer.Edit{Range: buffer.Range{Start: pos(1, 12), End: pos(1, 13)}, Text: "B"},
		buffer.Edit{Range: buffer.Range{Start: pos(1, 9), End: pos(1, 10)}, Text: "A"},
		buffer.Edit{Range: buffer.Range{Start: pos(1, 7), End: pos(1, 8)}, Text: "F"},
		buffer.Edit{Range: buffer.Range{Start: pos(1, 2), End: pos(1, 3)}, Text: "Y"},
	)
	if got, want := render(b, v)[1], "║               Y: int := F(Ax: , B)"; got != want {
		t.Errorf("gUU = %q, want %q", got, want)
	}
	// 行の数の変わる編集は、後ろの行の番号だけを足す。前の行は動かさない。
	b, v = apply(buffer.Edit{Range: buffer.Range{Start: pos(0, 0), End: pos(0, 0)}, Text: "new\n"})
	if got := rowTexts(newRenderer(60, 6).Render(&Frame{Text: b, LineLayout: LineLayout{Virtual: v}}), 3); got[1] != "╔══════▶if x {" || got[2] != "║               y: int := f(x: a, b)" {
		t.Errorf("行を足した後 = %q", got)
	}
	// 行の中身だけを変える、仮想テキストの無い行の編集では、何も変えない。
	l := NewVirtualLayer(vts, 2)
	if l.ApplyEdit(buffer.Edit{Range: buffer.Range{Start: pos(5, 0), End: pos(5, 1)}, Text: "z"}, false) {
		t.Error("仮想テキストの無い行の編集で変えたと答えた")
	}
	// xyz と空の行の J（つなぐ箇所の改行を消すだけ）は、行末の inline を行末に残す（Neovim は xyz123）。
	b2 := buffer.New("xyz\n\n")
	l2 := NewVirtualLayer([]VirtualText{vt(0, 3, VirtualInline, "123")}, 2)
	ed := buffer.Edit{Range: buffer.Range{Start: pos(0, 3), End: pos(1, 0)}}
	if _, err := b2.Apply(buffer.Transaction{BaseVersion: b2.Version(), Edits: []buffer.Edit{ed}}); err != nil {
		t.Fatal(err)
	}
	l2.ApplyEdit(ed, false)
	v2 := &VirtualTexts{}
	v2.SetLayers([]*VirtualLayer{l2})
	if got := rowTexts(newRenderer(20, 4).Render(&Frame{Text: b2, LineLayout: LineLayout{Virtual: v2}}), 1)[0]; got != "xyz123" {
		t.Errorf("空の行の J = %q, want xyz123", got)
	}
}

// TestVirtualTextsLayers は、複数の拡張機能の仮想テキストを、並びの順（拡張機能の順、同じ拡張機能の中では一覧の順）に合わせて描き、
// 1 つの拡張機能の一覧を置き換えても、ほかの拡張機能の仮想テキストはそのまま描くことを確かめる。同じ値の直の色は 1 つの値を指す。
//
// req: FR-231
func TestVirtualTextsLayers(t *testing.T) {
	b := buffer.New("abc\ndef\n")
	a := NewVirtualLayer([]VirtualText{vt(0, 1, VirtualInline, "A1"), vt(0, 1, VirtualInline, "A2"), vt(1, 3, VirtualEOL, "ae")}, 2)
	z := NewVirtualLayer([]VirtualText{vt(0, 1, VirtualInline, "Z"), vt(1, 0, VirtualEOL, "ze")}, 2)
	v := &VirtualTexts{}
	v.SetLayers([]*VirtualLayer{a, z})
	render := func() []string {
		return rowTexts(newRenderer(30, 5).Render(&Frame{Text: b, LineLayout: LineLayout{Virtual: v}}), 2)
	}
	if got := render(); got[0] != "aA1A2Zbc" || got[1] != "def ze ae" {
		t.Errorf("合わせた行 = %q", got)
	}
	v.SetLayers([]*VirtualLayer{NewVirtualLayer([]VirtualText{vt(0, 0, VirtualInline, "N")}, 2), z})
	if got := render(); got[0] != "NaZbc" || got[1] != "def ze" {
		t.Errorf("置き換えた後 = %q, want NaZbc と def ze", got)
	}
	same := NewVirtualLayer([]VirtualText{vt(0, 0, VirtualInline, "x"), vt(0, 1, VirtualInline, "y")}, 1)
	lv := same.line(0, 0)
	if lv.inline[0].style.attr != lv.inline[1].style.attr {
		t.Error("同じ値の直の色が別の値を指す")
	}
}

// TestNewVirtualTexts は、行の外の仮想テキスト、空の文字列、改行とタブ文字の扱いを確かめる。
func TestNewVirtualTexts(t *testing.T) {
	if v := NewVirtualTexts([]VirtualText{vt(5, 0, VirtualInline, "x"), vt(-1, 0, VirtualInline, "x"), vt(0, 0, VirtualEOL, "")}, 2); v != nil {
		t.Errorf("NewVirtualTexts = %+v, want nil", v)
	}
	b := buffer.New("ab\n")
	v := NewVirtualTexts([]VirtualText{vt(0, 1, VirtualInline, "x\ty\nz"), vt(0, -3, VirtualInline, "S")}, 1)
	r := newRenderer(20, 4)
	checkRows(t, "改行とタブ文字は空白、負のバイトは行頭", r.Render(&Frame{Text: b, LineLayout: LineLayout{Virtual: v}}), []string{"Sax y zb"})
}
