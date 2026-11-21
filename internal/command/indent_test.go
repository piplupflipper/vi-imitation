package command

import (
	"fmt"
	"testing"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/internal/option"
)

// fakeOptions は、設定の値の口（ex.Options）を、1 つの option.Values で持つ（internal/editor の optionAccess の代わり）。
type fakeOptions struct{ vs *option.Values }

func (o fakeOptions) Get(d option.Def) option.Value          { return o.vs.Get(d.Name) }
func (o fakeOptions) Set(d option.Def, v option.Value) error { return o.vs.Set(d, v) }
func (o fakeOptions) Revision(d option.Def) uint64           { return o.vs.Revision(d.Name) }
func (o fakeOptions) set(t *testing.T, name string, v option.Value) {
	t.Helper()
	mustSet(t, o.vs, name, v)
}

// mustSet は、vs の項目 name を v にする。
func mustSet(t *testing.T, vs *option.Values, name string, v option.Value) {
	t.Helper()
	d, ok := option.Lookup(name)
	if !ok {
		t.Fatalf("%s が表に無い", name)
	}
	if err := vs.Set(d, v); err != nil {
		t.Fatal(err)
	}
}

// indentConfigs は、indentCases の設定の組の名前から、indent.useSpaces、indent.width、tab.width への対応である。
// 期待値は、Neovim 0.12.5 を nvim --clean で起動し、:set の右の値（と、autoindent か noautoindent）にして取った。
// indent.useSpaces が true の組は softtabstop を indent.width と同じに、false の組は 0 にした（FR-160）。'smarttab' は Neovim の既定のオンである。
var indentConfigs = map[string]struct {
	useSpaces    bool
	width, tab   int
	nvimSettings string
}{
	"A": {false, 8, 8, "noet sw=8 sts=0 ts=8"},
	"B": {true, 4, 4, "et sw=4 sts=4 ts=4"},
	"C": {false, 4, 4, "noet sw=4 sts=0 ts=4"},
	"D": {true, 2, 8, "et sw=2 sts=2 ts=8"},
	"E": {false, 4, 8, "noet sw=4 sts=0 ts=8"},
}

// setupIndent は、text を内容とし、設定の組 cfg と自動インデント auto の State を作る。全角文字の幅は 2 列として数える。
func setupIndent(t *testing.T, cfg string, auto bool, text string) *State {
	t.Helper()
	c, ok := indentConfigs[cfg]
	if !ok {
		t.Fatalf("設定の組 %q が無い", cfg)
	}
	s := setup(t, text)
	o := fakeOptions{vs: &option.Values{}}
	o.set(t, option.IndentUseSpaces, option.BoolValue(c.useSpaces))
	o.set(t, option.IndentWidth, option.IntValue(c.width))
	o.set(t, option.TabWidth, option.IntValue(c.tab))
	o.set(t, option.IndentAuto, option.BoolValue(auto))
	s.Ex.Options = o
	s.TextWidth = wideTextWidth
	s.Columns = widthColumns{tab: c.tab}
	return s
}

// widthColumns は、internal/ui の Columns と同じ数え方を、wideTextWidth の幅で写したものである（タブ文字の上ではタブ文字の最後の列）。
type widthColumns struct{ tab int }

func (c widthColumns) Column(_ int, line string, byteOff int) int {
	for i, r := range line {
		if i == byteOff {
			col := wideTextWidth(line[:i], c.tab)
			if r == '\t' {
				return wideTextWidth(line[:i+1], c.tab) - 1
			}
			return col
		}
	}
	return wideTextWidth(line, c.tab)
}

func (c widthColumns) ByteAt(_ int, line string, col int) int {
	last := 0
	for i := range line {
		_, size := utf8.DecodeRuneInString(line[i:])
		if col < wideTextWidth(line[:i+size], c.tab) {
			return i
		}
		last = i
	}
	return last
}

// wideTextWidth は、State.TextWidth の代わりに、タブ文字を次のタブ位置まで、U+3000 以上の文字を 2 列、ほかを 1 列として数える
// （internal/ui は同じ層なので import しない。このテストで使う全角文字は「あ」だけである）。
func wideTextWidth(s string, tabStop int) int {
	w := 0
	for _, r := range s {
		switch {
		case r == '\t':
			w += tabStop - w%tabStop
		case r >= 0x3000:
			w += 2
		default:
			w++
		}
	}
	return w
}

// インサートモードの <Tab> と <BS>、<CR>、o、O、cc と、> と < は、字下げの設定（indent.useSpaces、indent.width、tab.width、
// indent.auto）の組ごとに、Neovim 0.12.5 と同じテキストとカーソルになる（期待値は indentCases の doc）。
// ノーマルモードの > と < は Vim の >> と << と同じなので、Neovim では >> と << で取った（FR-76）。
// > と < の後のカーソルは、Neovim の既定の 'nostartofline' で Vim 9.1 と違うので比べない（Vim 9.1 との突き合わせは shift_test.go にある）。
//
// req: FR-160, FR-162
func TestIndentNvim(t *testing.T) {
	for _, tc := range append(indentCases, indentBSCases...) {
		t.Run(fmt.Sprintf("%s/ai=%v/%q/%s", tc.cfg, tc.auto, tc.text, tc.keys), func(t *testing.T) {
			s := setupIndent(t, tc.cfg, tc.auto, tc.text)
			feedKeys(s, tc.keys)
			if got := s.buf.Text(); got != tc.want {
				t.Errorf("text = %q, want %q（Neovim の :set %s）", got, tc.want, indentConfigs[tc.cfg].nvimSettings)
			}
			if tc.cursor != "" {
				if got := s.Cursor().String(); got != tc.cursor {
					t.Errorf("cursor = %s, want %s", got, tc.cursor)
				}
			}
		})
	}
}

// indent.width が 0 なら tab.width の幅で字下げする（Vim の shiftwidth=0。本体の go と make の既定）。
// 設定は使うたびに読むので、途中で変えた値が次の操作から効く。
//
// req: FR-160
func TestIndentWidthZeroAndLiveChange(t *testing.T) {
	s := setupIndent(t, "C", false, "x")
	o := s.Ex.Options.(fakeOptions)
	o.set(t, option.IndentWidth, option.IntValue(0))
	o.set(t, option.TabWidth, option.IntValue(3))
	o.set(t, option.IndentUseSpaces, option.BoolValue(true))
	feedKeys(s, ">")
	if got, want := s.buf.Text(), "   x"; got != want {
		t.Errorf("indent.width 0、tab.width 3 の > = %q, want %q", got, want)
	}
	o.set(t, option.IndentUseSpaces, option.BoolValue(false))
	feedKeys(s, ">")
	if got, want := s.buf.Text(), "\t\tx"; got != want {
		t.Errorf("indent.useSpaces を false にした後の > = %q, want %q", got, want)
	}
}

// 端末から貼り付けたテキストのタブ文字と改行は、字下げの設定と自動インデントを当てずに、そのまま入る（Neovim の貼り付けと同じ）。
// 打った <Tab> と <CR> は設定に従うので、. で繰り返すと、繰り返す位置で設定を当て直す。
//
// req: FR-160, FR-162
func TestIndentPasteLiteral(t *testing.T) {
	s := setupIndent(t, "B", true, "    foo")
	feedKeys(s, "A")
	s.Paste("\tx\ny")
	if got, want := s.buf.Text(), "    foo\tx\ny"; got != want {
		t.Errorf("貼り付け = %q, want %q", got, want)
	}
}
