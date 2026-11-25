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

// 狭い見出しの行（40 桁と 60 桁のペイン）では、選んでいる子の見出しを " 名前 [キー] " のまま出し、ほかの子のキーの添え字を省き、
// それでも収まらなければほかの子の名前を切り詰めて "…" を置き、7 つの子をすべて並べる。
//
// req: FR-269
func TestSwitcherBarNarrow(t *testing.T) {
	names := []string{"Watches", "Scopes", "Exceptions", "Breakpoints", "Threads", "REPL", "Console"}
	keys := []string{"W", "S", "E", "B", "T", "R", "C"}
	bar := func(width, sel int) string {
		sw := &Switcher{Selected: sel}
		for i, n := range names {
			sw.Children = append(sw.Children, SwitcherChild{ID: n, Title: n, Key: keys[i]})
		}
		r := &Renderer{Width: width, Height: 6, Theme: testTheme()}
		s := r.Render(&Frame{Float: &Float{ID: 1, View: &Frame{Text: buffer.New("x")}, Fill: true, Switcher: sw}})
		return screenRows(s)[0]
	}
	for _, tt := range []struct {
		width, sel int
		want       string
	}{
		{100, 3, " Watches [W]  Scopes [S]  Exceptions [E]  Breakpoints [B]  Threads [T]  REPL [R]  Console [C]"},
		{80, 3, " Watches  Scopes  Exceptions  Breakpoints [B]  Threads  REPL  Console"},
		{60, 3, " Watc…  Scop…  Exce…  Breakpoints [B]  Thre…  REPL  Cons…"},
		{40, 0, " Watches [W]  S…  E…  B…  T…  R…  C…"},
		{40, 3, " W  S  E  Breakpoints [B]  T  R  C"}, // 表示幅 1 まで切り詰めたら、名前の最初の文字だけを出す
	} {
		if got := bar(tt.width, tt.sel); got != tt.want {
			t.Errorf("幅 %d、選んでいる子 %d: %q, want %q", tt.width, tt.sel, got, tt.want)
		}
	}
}

// 全角と半角の混ざった名前でも、切り詰めた名前は最初の 1 文字を必ず残す。最も短くしても収まらないとき（20 桁）と、
// 切り詰めた名前が別の子と同じになって見分けられないとき（頭文字が同じ子）は、すべてを並べるのをやめ、選んでいる子の周りを
// キーの添え字を省いた長さで出し、隠れた側に "<" と ">" を置く。
//
// req: FR-269
func TestSwitcherBarWide(t *testing.T) {
	bar := func(names []string, keys []string, width, sel int) string {
		sw := &Switcher{Selected: sel}
		for i, n := range names {
			sw.Children = append(sw.Children, SwitcherChild{ID: n, Title: n, Key: keys[i]})
		}
		r := &Renderer{Width: width, Height: 6, Theme: testTheme()}
		s := r.Render(&Frame{Float: &Float{ID: 1, View: &Frame{Text: buffer.New("x")}, Fill: true, Switcher: sw}})
		return screenRows(s)[0]
	}
	ja := []string{"ウォッチ式", "スコープ", "例外", "ブレークポイント", "スレッド", "REPL", "出力"}
	ja2 := []string{"ウォッチ式", "スコープ", "例外", "ブレークポイント", "糸口", "REPL", "出力"}
	jaKeys := []string{"W", "S", "E", "B", "T", "R", "C"}
	extra := []string{"Watches", "Scopes", "Exceptions", "Breakpoints", "Threads", "REPL", "Console", "Extra0", "Extra1", "Extra2", "Extra3", "Extra4"}
	extraKeys := []string{"W", "S", "E", "B", "T", "R", "C", "", "", "", "", ""}
	for _, tt := range []struct {
		name       string
		names      []string
		keys       []string
		width, sel int
		want       string
	}{
		{"全角 60 桁", ja, jaKeys, 60, 0, " ウォッチ式 [W]  スコ…  例外  ブレ…  スレ…  REPL  出力"},
		// 頭文字の違う全角の名前は、最初の 1 文字を残して並べる。
		{"全角 40 桁の最初の子", ja2, jaKeys, 40, 0, " ウォッチ式 [W]  ス  例  ブ  糸  R…  出"},
		// 選んでいる子が長く、最初の 1 文字まで縮めても収まらないときは、選んでいる子の周りを出す。
		{"全角 40 桁", ja2, jaKeys, 40, 3, "< スコープ  例外  ブレークポイント [B] >"},
		// スコープとスレッドは「ス」で見分けられなくなるので、選んでいる子の周りを出す。
		{"全角の見分けられない子 40 桁", ja, jaKeys, 40, 3, "< スコープ  例外  ブレークポイント [B] >"},
		{"全角の見分けられない子の最初の子", ja, jaKeys, 40, 0, " ウォッチ式 [W]  スコープ  例外  ブレー>"},
		{"全角 20 桁", ja2, jaKeys, 20, 3, "< ブレークポイント >"},
		{"頭文字が同じ子 40 桁", extra, extraKeys, 40, 0, " Watches [W]  Scopes  Exceptions  Break>"},
		{"頭文字が同じ子の後ろ", extra, extraKeys, 40, 9, "< REPL  Console  Extra0  Extra1  Extra2>"},
	} {
		got := bar(tt.names, tt.keys, tt.width, tt.sel)
		if got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
		if w := StringWidth(got, 8); w > tt.width {
			t.Errorf("%s: 表示幅 %d が %d を超えた", tt.name, w, tt.width)
		}
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
