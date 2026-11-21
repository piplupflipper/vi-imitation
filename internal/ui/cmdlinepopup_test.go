package ui

import (
	"fmt"
	"strings"
	"testing"
)

// paletteTheme は、画面の上のポップアップのコマンドラインの試験で使う色である。
var paletteTheme = Theme{
	StylePopup: {Bg: Indexed(1)}, StylePopupSelected: {Bg: Indexed(2)}, StylePopupMatch: {Bold: true},
	StylePopupScrollbar: {Bg: Indexed(3)}, StylePopupThumb: {Bg: Indexed(4)},
	StyleCommandLinePopup: {Bg: Indexed(5)}, StyleCommandLinePopupBorder: {Fg: Indexed(6)},
	StyleCommandLinePopupBorderSearch: {Fg: Indexed(7)},
}

// roundedBorder は、試験で使う枠線の記号（オーナーの設定の例と同じ rounded）である。
const roundedBorder = "╭─╮│╯─╰│"

// paletteRenderer は、width 列 height 行の画面を描く、枠線の記号を border にした Renderer を返す。
func paletteRenderer(width, height int, border string) *Renderer {
	return &Renderer{Width: width, Height: height, Theme: paletteTheme, Symbols: Symbols{SymbolPopupBorder: border}}
}

// renderPalette は、r で、種類 kind のコマンドライン line と一覧 m を、画面の上のポップアップに描く。
func renderPalette(r *Renderer, kind rune, line string, m *CompletionMenu) Screen {
	return r.Render(&Frame{Mode: ModeCommandLine, CommandLine: line, CommandLineType: kind, Completion: m, CommandLinePopup: true})
}

// boxRow は、バッファの最後の行より下の "~" の行の left 列から、左の文字 l、中の inner、右の文字 rr を並べた行の、
// TrimRight した文字列を返す（left は 1 以上）。
func boxRow(left int, l, inner, rr string) string {
	return strings.TrimRight("~"+strings.Repeat(" ", left-1)+l+inner+rr, " ")
}

// 入力の枠は、枠線を含めて 64 列の幅で画面の横の中央に置き、上の枠線を 2 行目（0 から数える）に、入力を 3 行目に描く。
// 題は上の枠線の中央に、前後に空白を付けて描く。候補の一覧は、入力の枠の下の枠線のすぐ下の 5 行目から、同じ 64 列の枠に描く
// （noice.nvim の command_palette の既定）。最後の行は空ける。カーソルは入力の末尾に置く。
//
// req: FR-215, FR-216, FR-217
func TestRenderCommandLinePopup(t *testing.T) {
	r := paletteRenderer(80, 24, roundedBorder)
	m := &CompletionMenu{ID: 1, Items: []string{"Alpha.txt", "alpine.md"}, Selected: 0, Start: 2, Match: "al"}
	s := renderPalette(r, ':', "e al", m)
	got := rowTexts(s, 24)
	edge := strings.Repeat("─", 62)
	title := " Cmdline "
	want := map[int]string{
		2: boxRow(8, "╭", edge[:26*len("─")]+title+edge[:(62-26-len(title))*len("─")], "╮"),
		3: boxRow(8, "│", " :e al"+strings.Repeat(" ", 56), "│"),
		4: boxRow(8, "╰", edge, "╯"),
		5: boxRow(8, "╭", edge, "╮"),
		6: boxRow(8, "│", " Alpha.txt"+strings.Repeat(" ", 52), "│"),
		7: boxRow(8, "│", " alpine.md"+strings.Repeat(" ", 52), "│"),
		8: boxRow(8, "╰", edge, "╯"),
	}
	for row, w := range want {
		if got[row] != w {
			t.Errorf("%d 行目 = %q\nwant      %q", row, got[row], w)
		}
	}
	if got[23] != "" || got[9] != "~" {
		t.Errorf("最後の行 = %q、一覧の下の行 = %q", got[23], got[9])
	}
	if c := s.Cursor; c.Row != 3 || c.Col != 15 || c.Shape != CursorBar || !c.Visible {
		t.Errorf("カーソル = %+v", c)
	}
	body, border := paletteTheme[StyleCommandLinePopup], paletteTheme[StyleCommandLinePopupBorder]
	sel := paletteTheme[StylePopupSelected]
	for _, tt := range []struct {
		row, col int
		want     Attr
	}{
		{2, 8, border},
		{2, 36, border}, // 題の文字
		{3, 9, body},
		{3, 10, body},
		{3, 70, body},
		{3, 71, border},
		{5, 8, border},
		{6, 9, sel},
		{6, 10, sel.Over(Attr{Bold: true})},
		{6, 12, sel},
		{6, 70, sel},
		{7, 9, body},
		{7, 10, body.Over(Attr{Bold: true})},
		{7, 70, body},
		{8, 71, border},
	} {
		if a := s.Grid.At(tt.row, tt.col).Attr; a != tt.want {
			t.Errorf("(%d, %d) の色 = %+v, want %+v", tt.row, tt.col, a, tt.want)
		}
	}
	if r.CompletionRows() != 2 {
		t.Errorf("一覧の行の数 = %d", r.CompletionRows())
	}
}

// 検索の入力は、枠線と題を commandLine.popup.border.search の色で描き、題は前方と後方で替える。
// 題の記号が空なら題を描かない。枠線の記号が空なら、ASCII の枠線で描く。
//
// req: FR-216
func TestRenderCommandLinePopupTitle(t *testing.T) {
	search := paletteTheme[StyleCommandLinePopupBorderSearch]
	for _, tt := range []struct {
		kind  rune
		title string
	}{{'/', " Search forward "}, {'?', " Search backward "}} {
		s := renderPalette(paletteRenderer(80, 24, roundedBorder), tt.kind, "foo", nil)
		top := s.Grid.RowText(2)
		if !strings.Contains(top, "─"+tt.title+"─") {
			t.Errorf("%c の題の行 = %q", tt.kind, top)
		}
		if got := rowTexts(s, 4)[3]; got != boxRow(8, "│", " "+string(tt.kind)+"foo"+strings.Repeat(" ", 57), "│") {
			t.Errorf("%c の入力の行 = %q", tt.kind, got)
		}
		if a := s.Grid.At(2, 8).Attr; a != search {
			t.Errorf("%c の枠線の色 = %+v", tt.kind, a)
		}
	}
	// 題の記号を空にすると、題を描かない。
	r := paletteRenderer(80, 24, roundedBorder)
	r.Symbols[SymbolCommandLineTitleCommand] = ""
	s := renderPalette(r, ':', "w", nil)
	if got := rowTexts(s, 3)[2]; got != boxRow(8, "╭", strings.Repeat("─", 62), "╮") {
		t.Errorf("題の無い枠 = %q", got)
	}
	// 枠線の記号が空（本体の既定）なら、ASCII の枠線で描く（枠の中の色が既定では本文と同じなので、枠線で見分ける）。
	// 候補の一覧の枠も同じ枠線で描く。
	s = renderPalette(paletteRenderer(80, 24, ""), ':', "e a", &CompletionMenu{ID: 1, Items: []string{"a1"}, Selected: -1, Start: 2})
	got := rowTexts(s, 8)
	dash := strings.Repeat("-", 62)
	want := []string{
		boxRow(8, "+", dash[:26]+" Cmdline "+dash[:27], "+"),
		boxRow(8, "|", " :e a"+strings.Repeat(" ", 57), "|"),
		boxRow(8, "+", dash, "+"),
		boxRow(8, "+", dash, "+"),
		boxRow(8, "|", " a1"+strings.Repeat(" ", 59), "|"),
		boxRow(8, "+", dash, "+"),
	}
	for i, w := range want {
		if got[2+i] != w {
			t.Errorf("既定の枠線の %d 行目 = %q\nwant              %q", 2+i, got[2+i], w)
		}
	}
	if a := s.Grid.At(2, 8).Attr; a != paletteTheme[StyleCommandLinePopupBorder] {
		t.Errorf("既定の枠線の色 = %+v", a)
	}
}

// 入力にタブ文字があっても、タブ文字の幅を欄の左の端から数えて描くので、最後の行に描くときと同じ幅になり、
// 欄に収まらなければ先頭を隠して、打った末尾とカーソルを見せる（PR #362 のレビューの必須 1）。
//
// req: FR-215
func TestRenderCommandLinePopupTab(t *testing.T) {
	// "/" と "xxxxxx<Tab>" で 8 列ずつになり、7 回で 56 列、"zzz" で 59 列、カーソルで 60 列の欄にちょうど収まる。
	line := strings.Repeat("xxxxxx\t", 7) + "zzz"
	s := renderPalette(paletteRenderer(80, 24, roundedBorder), '/', line, nil)
	// 最初のタブ文字は欄の 7 列目から 1 列、その後は 2 列で描く。
	if got, want := rowTexts(s, 4)[3], boxRow(8, "│", " /xxxxxx "+strings.Repeat("xxxxxx  ", 6)+"zzz  ", "│"); got != want {
		t.Errorf("タブ文字のある入力 = %q\nwant              %q", got, want)
	}
	if c := s.Cursor; c.Row != 3 || c.Col != 10+59 {
		t.Errorf("タブ文字のある入力のカーソル = %+v", c)
	}
	// 30 列の画面（欄は 26 列）では、先頭を隠して末尾の "zzz" とカーソルを見せる。
	s = renderPalette(paletteRenderer(30, 10, roundedBorder), '/', line, nil)
	row := rowTexts(s, 4)[3]
	if !strings.Contains(row, "zzz") || !strings.HasSuffix(row, "│") {
		t.Errorf("狭い画面のタブ文字のある入力 = %q", row)
	}
	if c := s.Cursor; c.Row != 3 || c.Col > 27 || s.Grid.At(3, c.Col-1).Text != "z" || s.Grid.At(3, c.Col-3).Text != "z" {
		t.Errorf("狭い画面のタブ文字のある入力のカーソル = %+v", c)
	}
}

// 入力が 60 列の欄に収まらなければ、画面の幅まで枠を広げ、それでも収まらなければ末尾が見えるように先頭を隠す。
// 枠を置けないほど低いか狭い画面では、最後の行に描く。画面が低ければ、枠を上へ詰める。
//
// req: FR-215
func TestRenderCommandLinePopupSize(t *testing.T) {
	line := strings.Repeat("a", 69)
	s := renderPalette(paletteRenderer(80, 24, roundedBorder), ':', line, nil)
	// 欄は ":" と 69 文字とカーソルの 1 列の 71 列で、枠は 75 列になり、(80 - 75) / 2 = 2 列目から始まる。
	if got := rowTexts(s, 4)[3]; got != boxRow(2, "│", " :"+line+"  ", "│") {
		t.Errorf("広げた枠 = %q", got)
	}
	line = strings.Repeat("b", 30) + "END"
	s = renderPalette(paletteRenderer(30, 10, roundedBorder), ':', line, nil)
	// 欄は 30 - 4 = 26 列で、カーソルの 1 列を残して 25 文字を出す。
	if got := rowTexts(s, 4)[3]; got != "│ "+strings.Repeat("b", 22)+"END  │" {
		t.Errorf("狭い画面の枠 = %q", got)
	}
	if c := s.Cursor; c.Row != 3 || c.Col != 27 {
		t.Errorf("狭い画面のカーソル = %+v", c)
	}
	// 5 行の画面では、最後の行の上の 4 行に 3 行の枠を、1 行目から置く。
	s = renderPalette(paletteRenderer(40, 5, roundedBorder), ':', "q", nil)
	if got := rowTexts(s, 5); !strings.HasPrefix(strings.TrimSpace(got[1]), "╭") || got[4] != "" {
		t.Errorf("低い画面 = %q", got)
	}
	// タブの行を出していても、枠は画面の上の端から数えて同じ 2 行目に出す（noice.nvim の relative = "editor" と同じ）。
	r := paletteRenderer(80, 24, roundedBorder)
	r.TabLine = TabLineAlways
	s = r.Render(&Frame{Mode: ModeCommandLine, CommandLine: "q", CommandLinePopup: true, Tabs: []Tab{{Name: "a"}}})
	if got := rowTexts(s, 4); !strings.Contains(got[2], "╭") || !strings.Contains(got[3], "│ :q ") {
		t.Errorf("タブの行のある画面 = %q", got)
	}
	if c := s.Cursor; c.Row != 3 || c.Col != 12 {
		t.Errorf("タブの行のある画面のカーソル = %+v", c)
	}
	// 3 行の画面と 5 列の画面では、枠を置けないので最後の行に描く。
	for _, sz := range [][2]int{{40, 3}, {5, 10}} {
		s = renderPalette(paletteRenderer(sz[0], sz[1], roundedBorder), ':', "q", nil)
		if got := rowTexts(s, sz[1])[sz[1]-1]; got != ":q" {
			t.Errorf("%d 列 %d 行の画面の最後の行 = %q", sz[0], sz[1], got)
		}
	}
}

// 候補の一覧は、15 行と completionMenu.height と、画面の下の端までに収まる行の数の小さい方まで出し、収まらない項目は送って、
// 枠の中の右の 1 列にスクロールバーを描く。1 行も取れなければ出さない。
//
// req: FR-217
func TestRenderCommandLinePopupMenu(t *testing.T) {
	items := make([]string, 30)
	for i := range items {
		items[i] = fmt.Sprintf("i%02d", i)
	}
	r := paletteRenderer(80, 40, roundedBorder)
	m := &CompletionMenu{ID: 1, Items: items, Selected: 20, Start: 2}
	s := renderPalette(r, ':', "e i20", m)
	got := rowTexts(s, 40)
	if r.CompletionRows() != 15 || got[21] != boxRow(8, "╰", strings.Repeat("─", 62), "╯") {
		t.Errorf("行の数 = %d、一覧の下の枠線 = %q", r.CompletionRows(), got[21])
	}
	if !strings.Contains(got[6+20-r.pum.first], " i20") {
		t.Errorf("選んだ項目の行 = %q（一番上の項目 %d）", got[6+20-r.pum.first], r.pum.first)
	}
	bar, thumb := paletteTheme[StylePopupScrollbar], paletteTheme[StylePopupThumb]
	n := 0
	for row := 6; row < 21; row++ {
		switch s.Grid.At(row, 70).Attr {
		case thumb:
			n++
		case bar:
		default:
			t.Errorf("%d 行目のスクロールバーの色 = %+v", row, s.Grid.At(row, 70).Attr)
		}
	}
	if n != 7 { // 15 * 15 / 30
		t.Errorf("つまみの高さ = %d", n)
	}
	// completionMenu.height で行の数を限る。
	m = &CompletionMenu{ID: 2, Items: items, Selected: -1, Start: 2, MaxHeight: 4}
	renderPalette(r, ':', "e i", m)
	if r.CompletionRows() != 4 {
		t.Errorf("completionMenu.height のときの行の数 = %d", r.CompletionRows())
	}
	// 12 行の画面では、入力の枠の 5 行（上の 2 行を含む）と一覧の枠線の 2 行を除いた 5 行を出す。
	// 一覧は、noice.nvim と同じくステータス行（10 行目）と最後の行（11 行目）にも重ね、下の枠線を最後の行に描く。
	r = paletteRenderer(80, 12, roundedBorder)
	s = renderPalette(r, ':', "e i", &CompletionMenu{ID: 3, Items: items, Selected: -1, Start: 2})
	if got := rowTexts(s, 12); r.CompletionRows() != 5 || !strings.Contains(got[10], "│ i04 ") || got[11] != strings.Repeat(" ", 8)+"╰"+strings.Repeat("─", 62)+"╯" {
		t.Errorf("低い画面の一覧 = %d 行\n%s", r.CompletionRows(), strings.Join(got, "\n"))
	}
	// 1 行しか取れなくても、その 1 行に選んでいる項目を出し、スクロールバーを描く。
	r = paletteRenderer(80, 8, roundedBorder)
	s = renderPalette(r, ':', "e i05", &CompletionMenu{ID: 4, Items: items, Selected: 5, Start: 2})
	if r.CompletionRows() != 1 || !strings.Contains(s.Grid.RowText(6), "│ i05 ") || s.Grid.At(6, 70).Attr != thumb {
		t.Errorf("1 行しか取れない一覧 = %d 行\n%s", r.CompletionRows(), s.Grid.String())
	}
	// 1 行も取れなければ出さない。
	r = paletteRenderer(80, 7, roundedBorder)
	s = renderPalette(r, ':', "e i", &CompletionMenu{ID: 5, Items: items, Selected: -1, Start: 2})
	if r.CompletionRows() != 0 || strings.Contains(s.Grid.String(), "i00") {
		t.Errorf("1 行も取れない一覧 = %d 行", r.CompletionRows())
	}
}

// 枠の上の位置は、マウスの出来事の位置を解くと何も無い所（HitNone）になる。枠の外の表示領域はバッファのテキストのままである。
//
// req: FR-215
func TestRenderCommandLinePopupHit(t *testing.T) {
	r := paletteRenderer(80, 24, roundedBorder)
	renderPalette(r, ':', "e i", &CompletionMenu{ID: 1, Items: []string{"i00", "i01"}, Selected: -1, Start: 2})
	for _, pos := range [][2]int{{2, 8}, {3, 40}, {4, 71}, {6, 30}, {8, 8}} {
		if h := r.HitTest(pos[0], pos[1], HitNone); h.Area != HitNone {
			t.Errorf("(%d, %d) = %+v", pos[0], pos[1], h)
		}
	}
	if h := r.HitTest(3, 5, HitNone); h.Area != HitText {
		t.Errorf("枠の外 = %+v", h)
	}
}
