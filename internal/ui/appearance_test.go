package ui

import (
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// 編集を禁止したバッファは、ステータス行の名前の後に [RO] を出す。未保存の印と並べるときは [+] の後に出す。
//
// req: FR-87
func TestStatusLineReadOnly(t *testing.T) {
	b := buffer.New("abc\n")
	r := newRenderer(40, 4)
	tests := []struct {
		name               string
		readOnly, modified bool
		want               string
	}{
		{"禁止していない", false, false, " NORMAL  a.go"},
		{"禁止", true, false, " NORMAL  a.go [RO]"},
		{"未保存と禁止", true, true, " NORMAL  a.go [+] [RO]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := r.Render(&Frame{Text: b, Name: "a.go", ReadOnly: tt.readOnly, Modified: tt.modified})
			got := s.Grid.RowText(2)
			if !strings.HasPrefix(got, tt.want+"   ") {
				t.Errorf("ステータス行 = %q, want 先頭が %q", got, tt.want)
			}
		})
	}
}

// 名前が長くて収まらなければ、Vim 9.1 と同じく名前の先頭を "<" で切り、[RO] と [+] を残す（PR #206 のレビューの入力。
// 100 列の端末で 94 文字のパス。ユーザー名は同じ長さの架空の名前に置き換えた。Vim 9.1 の `vim -Nu NONE -R` の 50 列の窓は `<4030-b80ff77dac4f/find.go [RO]` と出す）。
//
// req: FR-87
func TestStatusLineLongName(t *testing.T) {
	b := buffer.New("abc\n")
	r := newRenderer(100, 6)
	name := "/home/user01/go/pkg/mod/github.com/tailscale/hujson@v0.0.0-20260727124030-b80ff77dac4f/find.go"
	got := r.Render(&Frame{Text: b, Name: name, ReadOnly: true}).Grid.RowText(4)
	want := " NORMAL  <mod/github.com/tailscale/hujson@v0.0.0-20260727124030-b80ff77dac4f/find.go [RO]  1,1  All "
	if got != want {
		t.Errorf("ステータス行\n got %q\nwant %q", got, want)
	}
	got = r.Render(&Frame{Text: b, Name: name, ReadOnly: true, Modified: true}).Grid.RowText(4)
	if !strings.Contains(got, "find.go [+] [RO]  1,1") || !strings.HasPrefix(got, " NORMAL  <") {
		t.Errorf("未保存と禁止: %q", got)
	}
	// 名前を "<" だけにしても収まらない幅（PR #206 の再レビューの 24 列）では、名前を出さずに [RO] を残す。
	if got := newRenderer(24, 6).Render(&Frame{Text: b, Name: name, ReadOnly: true}).Grid.RowText(4); got != " NORMAL  [RO]  1,1  All " {
		t.Errorf("24 列: %q", got)
	}
	// 収まる名前は切らない。
	if got := r.Render(&Frame{Text: b, Name: "a.go", ReadOnly: true}).Grid.RowText(4); !strings.HasPrefix(got, " NORMAL  a.go [RO] ") {
		t.Errorf("短い名前: %q", got)
	}
}

// 部品の色が文字か背景の色を持てば、既定の statusline の反転を外す（反転すると、部品の文字の色と背景の色が入れ替わるため）。
// 飾りだけの部品の色（太字など）は、statusline の反転の上に重ねる。
//
// req: FR-91
func TestStatusLinePartReverse(t *testing.T) {
	b := buffer.New("abc\n")
	theme := DefaultTheme().WithColors(map[string]Attr{
		StyleStatusLineModeNormal: {Fg: Indexed(0), Bg: Indexed(1)},
		StyleStatusLineName:       {Italic: true},
	})
	r := &Renderer{Width: 30, Height: 4, Theme: theme}
	g := r.Render(&Frame{Text: b, Name: "a.go"}).Grid
	if got, want := g.At(2, 1).Attr, (Attr{Fg: Indexed(0), Bg: Indexed(1), Bold: true}); got != want {
		t.Errorf("モードの部品 = %+v, want %+v", got, want)
	}
	if got, want := g.At(2, 10).Attr, (Attr{Reverse: true, Bold: true, Italic: true}); got != want {
		t.Errorf("名前の部品 = %+v, want %+v", got, want)
	}
}

// ステータス行の部品は、設定の並び（StatusLine）の順に、左と右へ並べる。並べなかった部品は出さず、知らない ID は描かない。
// 部品の色（statusline.<ID>）は、ステータス行の色の上に重ねる。モードの部品は、モードごとの色も重ねる。
// 編集の禁止の印と未保存の印の文字は、記号の設定で変えられる。
//
// req: FR-87, FR-92, FR-93
func TestStatusLineLayout(t *testing.T) {
	b := buffer.New("abc\n")
	theme := testTheme()
	theme[StyleStatusLine] = Attr{Bold: true}
	theme[StyleStatusLineMode] = Attr{Fg: Indexed(2)}
	theme[StyleStatusLineModeInsert] = Attr{Fg: Indexed(3)}
	theme[StyleStatusLineReadOnly] = Attr{Fg: Indexed(1)}
	r := &Renderer{
		Width: 30, Height: 4, Theme: theme,
		StatusLine: &StatusLine{Left: []string{"readonly", "name", "unknown"}, Right: []string{"filetype", "mode"}},
		Symbols:    Symbols{SymbolReadOnly: "RO", SymbolModified: "+"},
	}
	golden(t, "statusline-layout", renderSteps(r, []step{
		{"並べた順に出す", Frame{Text: b, Name: "a.go", FileType: "go", ReadOnly: true, Modified: true}},
		{"インサートモードの色", Frame{Text: b, Name: "a.go", FileType: "go", Mode: ModeInsert}},
		{"中身の無い部品は出さない", Frame{Text: b, Name: "a.go"}},
	}))
}

// 印の列の印の文字は、記号の設定で変えられる。1 列の印は右に空白を置く。
//
// req: FR-93
func TestSignSymbols(t *testing.T) {
	b := buffer.New("l0\nl1\nl2\n")
	r := newRenderer(10, 5)
	r.Symbols = Symbols{SymbolSignBreakpoint: "●", SymbolSignExecutionPoint: "->", SymbolSignDiagnosticError: "全"}
	s := r.Render(&Frame{
		Text: b, Signs: []Sign{{Line: 0, Kind: SignBreakpoint}, {Line: 1, Kind: SignExecutionPoint}},
		Diagnostics: []Diagnostic{{Range: buffer.Range{Start: pos(2, 0), End: pos(2, 1)}, Severity: SeverityError}},
	})
	for i, want := range []string{"● l0", "->l1", "全l2"} {
		if got := s.Grid.RowText(i); !strings.HasPrefix(got, want) {
			t.Errorf("%d 行目 = %q, want 先頭が %q", i, got, want)
		}
	}
}

// 枠線の記号を設定すると、浮かべる窓（popup/show の本文と選択の一覧）の周りに枠線を描く。
// 窓の位置の決め方（下に置き、収まらなければ上、右の端で左へ寄せる）は枠線を含めた大きさで行う。
//
// req: FR-93
func TestGoldenBorder(t *testing.T) {
	b := buffer.New("line0\nline1\nline2\nline3\nline4\nline5\nline6\nline7\n")
	theme := testTheme()
	theme[StylePopup] = Attr{Reverse: true}
	theme[StylePopupBorder] = Attr{Fg: Indexed(2)}
	r := &Renderer{Width: 16, Height: 10, Theme: theme, Symbols: Symbols{SymbolPopupBorder: "┌─┐│┘─└│"}}
	golden(t, "border", renderSteps(r, []step{
		{"下に置く", Frame{Text: b, Cursor: pos(0, 2), Float: textFloat("func f()\nok")}},
		{"下に収まらなければ上に置く", Frame{Text: b, Cursor: pos(6, 0), Float: textFloat("a\nb")}},
		{"右の端で左へ寄せる", Frame{Text: b, Cursor: pos(1, 4), Float: textFloat("abcdefghijkl")}},
		{"見出しは上の枠線に重ねる", Frame{Text: b, Cursor: pos(0, 0), Float: &Float{View: &Frame{Text: buffer.New("x")}, Title: "T"}}},
		{"選択の一覧", Frame{Text: b, Cursor: pos(0, 0), List: &List{Title: "reg", Items: []string{"a", "b"}, Selected: 1}}},
	}))
}

// 枠線があっても、上と下のどちらに置くかを枠線を含めた大きさで決めるので、表示領域のどの行のカーソルでもホバーが出る
// （PR #206 のレビューの入力。表示領域 8 行で、下から 2〜3 行目のカーソルで出なかった）。
//
// req: FR-93
func TestBorderPopupPlacement(t *testing.T) {
	b := buffer.New("0\n1\n2\n3\n4\n5\n6\n7\n")
	r := newRenderer(40, 10)
	r.Symbols = Symbols{SymbolPopupBorder: "╭─╮│╯─╰│"}
	// カーソルの行から、本文の行（枠線の内側）への対応。下に 3 行（本文と枠線）が入らなければ上に置く。
	want := map[int]int{0: 2, 4: 6, 5: 3, 6: 4, 7: 5}
	for line, row := range want {
		g := r.Render(&Frame{Text: b, Cursor: pos(line, 0), Float: textFloat("hover")}).Grid
		if got := g.RowText(row); !strings.Contains(got, "│ hover │") {
			t.Errorf("カーソルの行 %d: %d 行目 = %q\n%s", line, row, got, g.String())
		}
	}
}

func TestCheckSymbol(t *testing.T) {
	tests := []struct {
		name, value string
		ok          bool
	}{
		{SymbolSignBreakpoint, "●", true},
		{SymbolSignBreakpoint, "=>", true},
		{SymbolSignBreakpoint, "全", true},
		{SymbolSignBreakpoint, "", false},
		{SymbolSignBreakpoint, "abc", false},
		{SymbolSignBreakpoint, "\t", false},
		{SymbolReadOnly, "", true},
		{SymbolReadOnly, "[読み取り専用]", true},
		{SymbolReadOnly, "a\x01", false},
		{SymbolPopupBorder, "", true},
		{SymbolPopupBorder, "┌─┐│┘─└│", true},
		{SymbolPopupBorder, "+-+|+-+|", true},
		{SymbolPopupBorder, "+-+|+-+", false},
		{SymbolPopupBorder, "+-+|+-+全", false},
		{"sign.unknown", "x", false},
		// 空白の記号（FR-170）は、Vim の 'listchars' と同じく幅 1 の文字だけにする。タブ文字の記号は 2 文字か 3 文字、ほかは 1 文字である。
		{SymbolWhitespaceTab, "", true},
		{SymbolWhitespaceTab, "→ ", true},
		{SymbolWhitespaceTab, "<->", true},
		{SymbolWhitespaceTab, ">", false},
		{SymbolWhitespaceTab, "<-->", false},
		{SymbolWhitespaceTab, "全 ", false},
		{SymbolWhitespaceTrail, "·", true},
		{SymbolWhitespaceTrail, "", true},
		{SymbolWhitespaceTrail, "--", false},
		{SymbolWhitespaceEOL, "↲", true},
		{SymbolWhitespaceEOL, "全", false},
		{SymbolWhitespaceSpace, "\t", false},
	}
	for _, tt := range tests {
		err := CheckSymbol(tt.name, tt.value)
		if (err == nil) != tt.ok {
			t.Errorf("CheckSymbol(%q, %q) = %v, want ok=%v", tt.name, tt.value, err, tt.ok)
		}
	}
	for _, n := range SymbolNames() {
		if err := CheckSymbol(n, DefaultSymbols()[n]); err != nil {
			t.Errorf("既定の記号 %s が検査を通らない: %v", n, err)
		}
	}
}

// 設定の colors は、既定の対応を名前ごとに置き換える。元の対応は変えない。
//
// req: FR-91
func TestThemeWithColors(t *testing.T) {
	base := DefaultTheme()
	got := base.WithColors(map[string]Attr{StyleKeyword: {Fg: RGB(0x84, 0xa0, 0xc6)}, StyleStatusLine: {Bg: Indexed(0)}})
	if got[StyleKeyword] != (Attr{Fg: RGB(0x84, 0xa0, 0xc6)}) {
		t.Errorf("keyword = %+v", got[StyleKeyword])
	}
	if got[StyleStatusLine] != (Attr{Bg: Indexed(0)}) {
		t.Errorf("statusline は既定と混ぜずに置き換える: %+v", got[StyleStatusLine])
	}
	if got[StyleComment] != base[StyleComment] || base[StyleKeyword] == got[StyleKeyword] {
		t.Error("上書きしない名前が変わったか、元の対応が変わった")
	}
	for _, n := range StyleNames() {
		if !IsStyleName(n) {
			t.Errorf("IsStyleName(%q) = false", n)
		}
	}
	for n := range DefaultTheme() {
		if !IsStyleName(n) {
			t.Errorf("既定の対応の名前 %q が StyleNames に無い", n)
		}
	}
}

// ブロックの範囲の罫線の色の名前は、設定の colors に書け、既定は hlchunk.nvim の chunk の style の既定と同じ色である。
//
// req: FR-267
func TestChunkStyles(t *testing.T) {
	def := DefaultTheme()
	if !IsStyleName(StyleChunk) || def[StyleChunk] != (Attr{Fg: RGB(0x80, 0x6d, 0x9c)}) {
		t.Errorf("chunk = %+v", def[StyleChunk])
	}
	if !IsStyleName(StyleChunkError) || def[StyleChunkError] != (Attr{Fg: RGB(0xc2, 0x1f, 0x30)}) {
		t.Errorf("chunk.error = %+v", def[StyleChunkError])
	}
	if got := def.WithColors(map[string]Attr{StyleChunk: {Fg: Indexed(5)}}); got.attr(StyleChunk) != (Attr{Fg: Indexed(5)}) {
		t.Errorf("colors で変えた chunk = %+v", got.attr(StyleChunk))
	}
}
