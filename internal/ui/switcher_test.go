package ui

import (
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// switcherFloat は、子 n 個（名前 c0、c1、…、偶数番目にキー K0、K2、…）の切り替えの部品の窓で、sel 番目の子として text を出す。
func switcherFloat(n, sel int, text string) *Float {
	sw := &Switcher{Selected: sel}
	for i := range n {
		c := SwitcherChild{ID: string(rune('a' + i)), Title: "c" + string(rune('0'+i))}
		if i%2 == 0 {
			c.Key = "K" + string(rune('0'+i))
		}
		sw.Children = append(sw.Children, c)
	}
	return &Float{ID: 3, View: &Frame{Text: buffer.New(text)}, Focused: true, Fill: true, Switcher: sw}
}

// 切り替えの部品は、窓の最初の行（見出しの後）に子の名前とキーを並べ、選んでいる子を StyleViewChildSelected、ほかを StyleViewChild で描き、
// その下に選んでいる子を描く。見出しの行の子の名前は HitChild に解け、見出しの無い列は HitNone である。
// 幅に収まらなければ、選んでいる子の名前が見えるように左を省いて "<" を、右を切って ">" を置く。
//
// req: FR-269
func TestSwitcherBar(t *testing.T) {
	theme := testTheme()
	theme[StyleViewChild] = Attr{Underline: true}
	theme[StyleViewChildSelected] = Attr{Bold: true}
	r := &Renderer{Width: 40, Height: 8, Theme: theme}
	s := r.Render(&Frame{Float: switcherFloat(3, 1, "one\ntwo")})
	rows := screenRows(s)
	if rows[0] != " c0 [K0]  c1  c2 [K2]" || rows[1] != " one" || rows[2] != " two" {
		t.Fatalf("画面:\n%s", s.Grid.String())
	}
	if a := s.Grid.At(0, 9).Attr; !a.Bold {
		t.Errorf("選んでいる子の色 = %+v", a)
	}
	if a := s.Grid.At(0, 1).Attr; !a.Underline || a.Bold {
		t.Errorf("選んでいない子の色 = %+v", a)
	}
	if h := r.HitTest(0, 10, HitNone); h.Area != HitChild || h.Child != 1 {
		t.Errorf("見出しの c1 = %+v", h)
	}
	if h := r.HitTest(0, 30, HitNone); h.Area == HitChild {
		t.Errorf("見出しの無い列 = %+v", h)
	}
	if h := r.HitTest(1, 2, HitNone); h.Area != HitFloat || h.Line != 0 {
		t.Errorf("子の 1 行目 = %+v", h)
	}

	// 狭い窓では、選んでいる子（最後から 2 番目）が見えるまで左を省き、右を切る。
	r = &Renderer{Width: 16, Height: 8, Theme: theme}
	s = r.Render(&Frame{Float: switcherFloat(6, 4, "x")})
	row := screenRows(s)[0]
	if !strings.HasPrefix(row, "<") || !strings.Contains(row, "c4 [K4]") || !strings.HasSuffix(row, ">") {
		t.Errorf("狭い見出しの行 = %q", row)
	}

	// 子ごとに窓の中の子の Renderer を持ち、切り替えて戻ってもスクロールの位置を保つ。
	r = &Renderer{Width: 40, Height: 8, Theme: theme}
	long := strings.Repeat("l\n", 30) + "end"
	fl := switcherFloat(2, 0, long)
	fl.View.Cursor = buffer.Position{Line: 30}
	r.Render(&Frame{Float: fl})
	a, _ := r.FloatChildRenderers(3, "a")
	if a == nil || a.Top() == 0 {
		t.Fatalf("子 a の Renderer = %v", a)
	}
	top := a.Top()
	r.Render(&Frame{Float: switcherFloat(2, 1, "b")})
	fl.View.Cursor = buffer.Position{Line: 29}
	r.Render(&Frame{Float: fl})
	if a2, _ := r.FloatChildRenderers(3, "a"); a2 != a || a2.Top() != top {
		t.Errorf("戻った子 a の Top = %d, want %d", a2.Top(), top)
	}
	if b, _ := r.FloatChildRenderers(3, "b"); b == nil || b == a {
		t.Errorf("子 b の Renderer = %v", b)
	}
	if x, _ := r.FloatChildRenderers(4, "a"); x != nil {
		t.Errorf("別の窓の子の Renderer = %v", x)
	}
}

// 書き足すテキストと入力の行の組の子では、見出しの行を出力の上に置く。狭い窓では、見出しより先に組の見出しを省き、入力の行を残す。
//
// req: FR-269
func TestSwitcherConsole(t *testing.T) {
	r := &Renderer{Width: 30, Height: 8, Theme: testTheme()}
	fl := consoleFloat("out", "in", pos(0, 0), pos(0, 2))
	fl.Fill, fl.Title = true, "dap"
	fl.Switcher = &Switcher{Children: []SwitcherChild{{ID: "repl", Title: "Repl", Key: "R"}}}
	rows := screenRows(r.Render(&Frame{Float: fl}))
	for i, w := range []string{" dap", " Repl [R]", " out", "", "", " > in"} {
		if rows[i] != w {
			t.Errorf("行 %d = %q, want %q", i, rows[i], w)
		}
	}
	r = &Renderer{Width: 30, Height: 4, Theme: testTheme()}
	rows = screenRows(r.Render(&Frame{Float: fl}))
	if rows[0] != " Repl [R]" || rows[1] != " > in" {
		t.Errorf("狭い窓: %q", rows)
	}
}
