package ui

import (
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// consoleFloat は、出力 out と入力の行 in の書き足すテキストと入力の行の組の窓である。
func consoleFloat(out, in string, outCur, inCur buffer.Position) *Float {
	return &Float{
		ID:      7,
		View:    &Frame{Text: buffer.New(in), Cursor: inCur, Mode: ModeInsert},
		Focused: true,
		Console: &Console{Prompt: "> ", Output: &Frame{Text: buffer.New(out), Cursor: outCur}},
	}
}

func screenRows(s Screen) []string {
	rows := strings.Split(s.Grid.String(), "\n")
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	return rows
}

// 書き足すテキストと入力の行の組は、上に出力を、最後の行に前置きと入力の行を置く。画面の全体の窓（部品の client）では、
// 見出しを最初の行に描き、出力の最後の行より下は空ける。端末のカーソルは入力の行に置き、出力へフォーカスを移せば出力に置く。
//
// req: FR-274
func TestConsoleFill(t *testing.T) {
	r := &Renderer{Width: 20, Height: 8, Theme: testTheme(), Symbols: Symbols{SymbolPopupBorder: "┌─┐│┘─└│"}}
	fl := consoleFloat("one\ntwo", "abc", pos(1, 0), pos(0, 3))
	fl.Title, fl.Fill = "repl", true
	s := r.Render(&Frame{Float: fl})
	rows := screenRows(s)
	// 表示領域は 6 行（ステータス行とコマンドラインの行を除く）。見出し、出力の 4 行、入力の行。
	want := []string{" repl", " one", " two", "", "", " > abc"}
	for i, w := range want {
		if rows[i] != w {
			t.Errorf("行 %d = %q, want %q（画面:\n%s）", i, rows[i], w, s.Grid.String())
		}
	}
	if strings.Contains(s.Grid.String(), "┌") || strings.Contains(s.Grid.String(), "~") {
		t.Errorf("Fill の窓に枠線か ~ を描いた:\n%s", s.Grid.String())
	}
	if c := s.Cursor; c.Row != 5 || c.Col != 6 || c.Shape != CursorBar {
		t.Errorf("入力の行のカーソル = %+v", c)
	}
	if r.ConsoleOutputRenderer() == nil {
		t.Errorf("ConsoleOutputRenderer = nil")
	}

	fl.Console.OutputFocused = true
	fl.View.Mode = ModeNormal
	fl.Console.Output.Mode = ModeNormal
	s = r.Render(&Frame{Float: fl})
	if c := s.Cursor; c.Row != 2 || c.Col != 1 {
		t.Errorf("出力のカーソル = %+v", c)
	}
}

// 中央の窓では枠線を描き、出力と入力の行の間に区切りの行を置く。出力は、カーソルの行（最後の行）が見えるまで縦にスクロールし、
// 長い入力の行はカーソルが見えるまで横にスクロールする。全角文字は 2 列で描く。
//
// req: FR-274
func TestConsoleCenter(t *testing.T) {
	r := &Renderer{Width: 24, Height: 8, Theme: testTheme(), Symbols: Symbols{SymbolPopupBorder: "┌─┐│┘─└│"}}
	var out []string
	for i := range 9 {
		out = append(out, strings.Repeat(string(rune('a'+i)), 3))
	}
	long := "式は" + strings.Repeat("x", 30)
	fl := consoleFloat(strings.Join(out, "\n"), long, pos(8, 0), pos(0, len(long)))
	fl.Center = true
	s := r.Render(&Frame{Text: buffer.New(""), Float: fl})
	rows := screenRows(s)
	// 表示領域は 6 行（ステータス行を除く）。枠線の 2 行を除く 4 行に、出力の 2 行、区切りの行、入力の行。
	for i, w := range []string{"┌──", "│ hhh", "│ iii", "│───", "│ > xxx", "└──"} {
		if !strings.HasPrefix(strings.TrimLeft(rows[i], " "), w) {
			t.Errorf("行 %d = %q, want prefix %q（画面:\n%s）", i, rows[i], w, s.Grid.String())
		}
	}
	// 入力の行は、末尾のカーソルが見えるまで横にスクロールし、先頭の全角文字は見えない。
	if strings.Contains(rows[4], "式") {
		t.Errorf("入力の行が横にスクロールしていない: %q", rows[4])
	}
	if c := s.Cursor; c.Row != 4 {
		t.Errorf("カーソルの行 = %d", c.Row)
	}
}

// 狭い画面では、区切りの行と出力を減らし、入力の行は残す。前置きが窓より広いときは、入力の 1 列を残して前置きを切る。
// 入力の行を置けないほど狭ければ描かない。
//
// req: FR-274
func TestConsoleNarrow(t *testing.T) {
	r := &Renderer{Width: 6, Height: 3, Theme: testTheme()}
	fl := consoleFloat("out", "in", pos(0, 0), pos(0, 2))
	fl.Fill = true
	fl.Console.Prompt = "長い前置き> "
	s := r.Render(&Frame{Float: fl})
	rows := screenRows(s)
	// 表示領域は 1 行なので、出力は描かず、入力の行だけ。窓の中の幅 4 列のうち、前置きに 3 列、入力に 1 列。
	if !strings.HasPrefix(rows[0], " 長") {
		t.Errorf("前置きの行 = %q（画面:\n%s）", rows[0], s.Grid.String())
	}
	if c := s.Cursor; c.Row != 0 || c.Col < 1 || c.Col > 5 {
		t.Errorf("カーソル = %+v", c)
	}
	// 見出しのある窓でも、1 行しか無ければ見出しを除いて入力の行を描く。
	fl.Title = "repl"
	fl.Console.Prompt = "> "
	s = r.Render(&Frame{Float: fl})
	// 入力の幅は 2 列なので、行末のカーソルが見えるまで横にスクロールしている。
	if rows := screenRows(s); !strings.HasPrefix(rows[0], " >") || s.Cursor.Row != 0 || s.Cursor.Col < 3 || s.Cursor.Col > 4 {
		t.Errorf("見出しのある 1 行の窓 = %q, カーソル %+v（画面:\n%s）", rows[0], s.Cursor, s.Grid.String())
	}
	r = &Renderer{Width: 2, Height: 3, Theme: testTheme()}
	_ = r.Render(&Frame{Float: fl}) // 描けない幅でも panic しない
}

// 入力の行の補完の候補の一覧は、入力の行の真上に、置き換える語の始まり（Menu.Start）の文字の列から並べ、項目の右に種類を出す。
// 40 桁 8 行のペイン（部品の client の画面）でも、窓の中に収め、項目が窓の行より多ければスクロールバーを描いて送る。
// 右の端に収まらなければ左へ寄せる。出力にフォーカスを移しているときは描かない。
//
// req: FR-297
func TestConsoleMenu(t *testing.T) {
	r := &Renderer{Width: 40, Height: 8, Theme: completionTheme}
	in := "x = obj.ab"
	fl := consoleFloat("o1\no2\no3\no4", in, pos(3, 0), pos(0, len(in)))
	fl.Title, fl.Fill = "repl", true
	fl.Console.Menu = &CompletionMenu{ID: 1, Items: []string{"abc", "abd"}, Kinds: []string{"method", "variable"}, Selected: 1, Start: 8, Match: "ab"}
	s := r.Render(&Frame{Float: fl})
	rows := rowTexts(s, 8)
	// 表示領域は 6 行。見出し、出力の 4 行、入力の行。入力の "ab" は 11 列目から（前置きの左の 1 列と "> " の後）。
	if got := rows[3][10:]; !strings.HasPrefix(got, " abc method") {
		t.Errorf("一覧の 1 行目 = %q（画面:\n%s）", got, s.Grid.String())
	}
	if got := rows[4][10:]; !strings.HasPrefix(got, " abd variable") {
		t.Errorf("一覧の 2 行目 = %q", got)
	}
	if rows[2] != " o2" || !strings.HasPrefix(rows[5], " > x = obj.ab") {
		t.Errorf("一覧の外の行 = %q, %q", rows[2], rows[5])
	}
	if a := s.Grid.At(4, 11).Attr; a != completionTheme[StylePopupSelected].Over(Attr{Bold: true}) {
		t.Errorf("選んだ項目の打った語の色 = %+v", a)
	}
	if c := s.Cursor; c.Row != 5 || c.Col != 13 {
		t.Errorf("カーソル = %+v", c)
	}

	// 項目が窓の行より多ければ、見出しの行まで使い、スクロールバーを描く。一覧は窓の外へ出ない。
	items := make([]string, 10)
	for i := range items {
		items[i] = "ab" + string(rune('0'+i))
	}
	fl.Console.Menu = &CompletionMenu{ID: 2, Items: items, Selected: -1, Start: 8}
	s = r.Render(&Frame{Float: fl})
	rows = rowTexts(s, 8)
	for i := range 5 {
		if got := rows[i][11:14]; got != items[i] {
			t.Errorf("%d 行目 = %q, want %s（画面:\n%s）", i, rows[i], items[i], s.Grid.String())
		}
	}
	if a := s.Grid.At(0, 26).Attr; a != completionTheme[StylePopupThumb] {
		t.Errorf("スクロールバーのつまみ = %+v", a)
	}

	// 右の端に収まらなければ、窓の右の端に寄せる。
	long := strings.Repeat("y", 30) + ".a"
	fl.View = &Frame{Text: buffer.New(long), Cursor: pos(0, len(long)), Mode: ModeInsert}
	fl.Console.Menu = &CompletionMenu{ID: 3, Items: []string{"abc"}, Selected: -1, Start: len(long) - 1}
	s = r.Render(&Frame{Float: fl})
	if got := s.Grid.RowText(4)[23:]; got != " abc"+strings.Repeat(" ", 13) {
		t.Errorf("右に寄せた一覧 = %q（画面:\n%s）", s.Grid.RowText(4), s.Grid.String())
	}

	// 出力にフォーカスを移していれば描かない。
	fl.Console.OutputFocused = true
	if s = r.Render(&Frame{Float: fl}); strings.Contains(s.Grid.String(), "abc") {
		t.Errorf("出力のフォーカスで一覧を描いた:\n%s", s.Grid.String())
	}
}

// 種類の欄が窓に入らなければ、種類を省いて項目だけを出す。
//
// req: FR-297
func TestConsoleMenuNarrowKind(t *testing.T) {
	r := &Renderer{Width: 18, Height: 8, Theme: completionTheme}
	fl := consoleFloat("", "a", pos(0, 0), pos(0, 1))
	fl.Fill = true
	fl.Console.Menu = &CompletionMenu{ID: 1, Items: []string{"abcdefghijkl"}, Kinds: []string{"function"}, Selected: -1, Start: 0}
	s := r.Render(&Frame{Float: fl})
	got := s.Grid.RowText(4)
	if strings.Contains(got, "function") || !strings.Contains(got, "abcdefghijkl") {
		t.Errorf("狭い一覧 = %q（画面:\n%s）", got, s.Grid.String())
	}
}
