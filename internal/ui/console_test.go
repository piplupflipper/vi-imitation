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
