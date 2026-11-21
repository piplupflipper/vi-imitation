package editor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// このファイルは、右端のスクロールバーのつまみのドラッグ（FR-210）と、印を押してその行へ移ること（FR-211）を確かめる。
//
// 期待値は、2026-11-22 に、オーナーの nvim に入っている nvim-scrollview（コミット ce007c654e6af5f9dc5a8c92ea300e623fface1a）を
// Neovim 0.12.5（mouse=a、オーナーの設定の current_only と signs_on_startup）で読み込み、tmux 3.6 の 80 列 12 行のペイン
// （表示領域は 10 行）に SGR の形のマウスの列（tmux send-keys -H）を送って、最初の行、カーソル、ジャンプリスト、モードを確かめて取った。
// 本体の端末は、タブの行の分だけ 1 行多い 40 列 13 行にして、表示領域を同じ 10 行にした。行の番号は本体の 0 から数える番号で書く
// （Neovim の行の番号から 1 を引いたもの）。

// barLines は、"line 1" から "line n" までの n 行のテキストを返す。marked の行（1 から数える）は "x 50" のように "line" を "x" にする。
func barLines(n int, marked ...int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		word := "line"
		for _, m := range marked {
			if m == i {
				word = "x"
			}
		}
		fmt.Fprintf(&b, "%s %d\n", word, i)
	}
	return b.String()
}

// barStart は、40 列 13 行の端末（タブの行の下の 10 行が表示領域で、画面の 1 行目から 10 行目。右端は 39 列目）で、
// スクロールバーを出す設定に extra を足して a.txt を開く。
func barStart(t *testing.T, text string, extra map[string]option.Value) *harness {
	t.Helper()
	dir := writeFiles(t, map[string]string{"a.txt": text})
	term := newFakeTerm()
	term.w, term.h = 40, 13
	h := startTerm(t, term, Config{WorkDir: dir, Options: scrollbarOpts(extra)}, "a.txt")
	h.settle()
	return h
}

// barDrag は、画面の from 行目の右端（つまみ）を押し、rows の行へ順にドラッグしてから、最後の行で離す。
func (h *harness) barDrag(from int, rows ...int) {
	h.t.Helper()
	h.mouse("<LeftMouse>", from, 39)
	last := from
	for _, r := range rows {
		h.mouse("<LeftDrag>", r, 39)
		last = r
	}
	h.mouse("<LeftRelease>", last, 39)
}

// つまみを押してドラッグすると、つまみの最初の行を、マウスの行に押したときのつまみの中の位置の差を足した行へ動かすように
// 表示領域を送る。最初の行は nvim-scrollview の simple の数え方（round(行×(n−1)÷(h−1))）で決める。
// カーソルは、押したときと同じ表示領域の中の行と列に置き、ジャンプリストには記録しない。押して離しただけでは何も動かさない。
//
// req: FR-210
func TestScrollbarThumbDrag(t *testing.T) {
	h := barStart(t, barLines(200), nil)
	h.keys("4j3l")
	h.settle()
	// Neovim: 4j3l の後に 1 行目のつまみを押して 3 行目、5 行目へドラッグして離すと、最初の行 89、カーソル 93 行目の 4 列目。
	h.barDrag(1, 3, 5)
	if top, cur := h.viewTop(), h.cursor(); top != 88 || cur != (buffer.Position{Line: 92, Byte: 3}) {
		t.Errorf("ドラッグの後 = 最初の行 %d、カーソル %+v, want 88、92:3", top, cur)
	}
	// ドラッグはジャンプに当たらない。<C-o> で押す前の行（4）へ戻らない。
	h.keys("<C-o>")
	h.settle()
	if cur := h.cursor(); cur.Line == 4 {
		t.Errorf("<C-o> でドラッグの前の行へ戻った: %+v", cur)
	}
	// つまみ（今は 5 行目。88×10÷200 = 4.4）を押して、動かさずに離しても何も動かない（Neovim と同じ）。
	h.keys("<C-o><C-i>")
	h.settle()
	top, cur := h.viewTop(), h.cursor()
	h.barDrag(5)
	if gt, gc := h.viewTop(), h.cursor(); gt != top || gc != cur {
		t.Errorf("押して離しただけの後 = 最初の行 %d、カーソル %+v, want %d、%+v", gt, gc, top, cur)
	}
	if m := h.mode(); m != command.ModeNormal {
		t.Errorf("モード = %v", m)
	}
	// ビジュアルモードでつまみを押すと、選択を終えてノーマルモードへ戻る（nvim-scrollview と同じ）。
	h.keys("vj")
	h.settle()
	h.barDrag(5)
	if m := h.mode(); m != command.ModeNormal {
		t.Errorf("ビジュアルモードでつまみを押した後のモード = %v", m)
	}

	// 最後の行へドラッグすると、最初の行はバッファの最後の行で、カーソルはその行へ寄せる。最初の行へ戻すと先頭に戻る。
	h.barDrag(5, 10)
	if top, cur := h.viewTop(), h.cursor(); top != 199 || cur.Line != 199 {
		t.Errorf("最後の行へのドラッグの後 = 最初の行 %d、カーソル %+v", top, cur)
	}
	h.barDrag(10, 0)
	if top := h.viewTop(); top != 0 {
		t.Errorf("タブの行へのドラッグの後の最初の行 = %d", top)
	}
}

// つまみの中のどの行を押しても、押した行とつまみの最初の行の差を保ってドラッグする。
//
// req: FR-210
func TestScrollbarThumbOffset(t *testing.T) {
	// 30 行のうち 10 行を見ているので、つまみは表示領域の 0 行目から 3 行目（画面の 1 行目から 4 行目）である。
	h := barStart(t, barLines(30), nil)
	h.keys("2j")
	h.settle()
	waitBar(h, "####......")
	// Neovim: 2j の後につまみの 2 行目を押して 4 行目へドラッグすると、最初の行 7、カーソル 9 行目。
	h.barDrag(2, 4)
	if top, cur := h.viewTop(), h.cursor(); top != 6 || cur.Line != 8 {
		t.Errorf("ドラッグの後 = 最初の行 %d、カーソル %+v, want 6、8 行目", top, cur)
	}
	// つまみは 2 行目からになる（6×10÷30 = 2）。
	waitBar(h, "..####....")
}

// つまみのドラッグは、上下の余白（scroll.margin）を残す。カーソルが余白の中になれば、表示領域を動かさずに余白の外の行へ移す
// （nvim-scrollview が離したときにカーソルを戻す H は、'scrolloff' の外の行へ移る）。
// インサートモードでは、インサートモードのまま、押したときと同じ表示領域の中の位置へ移る。
//
// req: FR-210
func TestScrollbarThumbDragMarginAndInsert(t *testing.T) {
	h := barStart(t, barLines(200), map[string]option.Value{option.ScrollMargin: option.IntValue(2)})
	h.barDrag(1, 5)
	if top, cur := h.viewTop(), h.cursor(); top != 88 || cur.Line != 90 {
		t.Errorf("余白のあるドラッグの後 = 最初の行 %d、カーソル %+v, want 88、90 行目", top, cur)
	}

	// Neovim: gg3j2li の後に 1 行目のつまみを 5 行目へドラッグして Z を打つと、最初の行 89 の 4 行目の "line 92" の 3 列目に入る。
	h = barStart(t, barLines(200), nil)
	h.keys("gg3j2li")
	h.settle()
	h.barDrag(1, 5)
	h.keys("Z<Esc>")
	h.settle()
	if top, got := h.viewTop(), h.cursorLine(); top != 88 || got != "liZne 92" {
		t.Errorf("インサートモードのドラッグの後 = 最初の行 %d、行 %q", top, got)
	}
}

// 行を折り返すときも、カーソルは押したときと同じ表示領域の中の見た目の行と列に置く。
//
// req: FR-210
func TestScrollbarThumbDragWrap(t *testing.T) {
	var b strings.Builder
	for i := range 100 {
		if i%2 == 0 {
			// 2 行に折り返す行（本文の幅は 40 列）。
			fmt.Fprintf(&b, "%s %d\n", strings.Repeat("w", 45), i)
		} else {
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	h := barStart(t, b.String(), map[string]option.Value{option.LineWrapEnabled: option.BoolValue(true)})
	// 0 行目の 2 つ目の見た目の行の 3 列目（表示領域の 1 行目）。
	h.keys("42l")
	h.settle()
	h.barDrag(1, 5)
	// 最初の行は round(4×99÷9) = 44 で、表示領域の 1 行目は 44 行目の 2 つ目の見た目の行、その 3 列目は 43 バイト目である。
	if top, cur := h.viewTop(), h.cursor(); top != 44 || cur != (buffer.Position{Line: 44, Byte: 42}) {
		t.Errorf("折り返すときのドラッグの後 = 最初の行 %d、カーソル %+v, want 44、44:42", top, cur)
	}
}

// 印を押すと、その印のセルの印の行のうちカーソルの行より後の最初の行へ移り、無ければ最初の行へ回る。移る前の位置は
// ジャンプリストに記録する（<C-o> で戻れる）。つまみの上に描いた印は、つまみを動かさずに離したときに移る。
// ビジュアルモードでは選択を終えてから、インサートモードではインサートモードのまま移る。ダブルクリックも 1 回のクリックとして扱う。
//
// req: FR-211
func TestScrollbarMarkClick(t *testing.T) {
	// 50 行目、52 行目、120 行目（本体の 49、51、119）に一致がある。表示領域の行 l÷20 に出るので、2 行目に 49 と 51、5 行目に 119。
	h := barStart(t, barLines(200, 50, 52, 120), map[string]option.Value{option.SearchHighlight: option.BoolValue(true)})
	h.keys("/x<CR>gg")
	h.settle()
	waitBar(h, "#.=..=....")
	// Neovim: 3 行目の印を押すと 50 行目へ移る。
	h.click(3, 39)
	if cur := h.cursor(); cur != (buffer.Position{Line: 49}) {
		t.Errorf("印を押した後のカーソル = %+v, want 49:0", cur)
	}
	// 49 行目が見えると、つまみは 2 行目へ来て、印はその上に重なる。押すと次の行（51）、もう一度押すと回って 49 へ移る
	// （Neovim では印はつまみの左の列にずれ、そこを押すと同じ順に移る）。
	waitBar(h, "..=..=....")
	h.click(3, 39)
	h.settle()
	if cur := h.cursor(); cur.Line != 51 {
		t.Errorf("つまみの上の印を押した後のカーソル = %+v, want 51 行目", cur)
	}
	h.mouse("<LeftMouse>", 3, 39)
	h.mouse("<LeftRelease>", 3, 39)
	// 続けて同じ位置で押すとダブルクリックになるが、本体のテストの端末は回数を数えないので、1 回目のクリックである。
	if cur := h.cursor(); cur.Line != 49 {
		t.Errorf("回った後のカーソル = %+v, want 49 行目", cur)
	}
	// Neovim: ジャンプリストは 1、50、52 で、<C-o> は 52 行目へ、もう一度の <C-o> は 1 行目へ戻る（50 行目は今の位置として
	// 入れ直すので、前の 50 行目は消える）。
	h.keys("<C-o>")
	h.settle()
	if cur := h.cursor(); cur.Line != 51 {
		t.Errorf("<C-o> の後のカーソル = %+v, want 51 行目", cur)
	}
	h.keys("<C-o>")
	h.settle()
	if cur := h.cursor(); cur.Line != 0 {
		t.Errorf("2 回目の <C-o> の後のカーソル = %+v, want 0 行目", cur)
	}

	// つまみの上の印を押してドラッグしたら、印へは移らずにつまみを動かす。
	h.click(3, 39)
	waitBar(h, "..=..=....")
	h.barDrag(3, 4)
	if top, cur := h.viewTop(), h.cursor(); cur.Line == 51 || top != 66 {
		t.Errorf("つまみの上の印からドラッグした後 = 最初の行 %d、カーソル %+v, want 66", top, cur)
	}

	// ビジュアルモードでは選択を終えてから移る。Neovim: gg v j の後に 6 行目の印を押すと、ノーマルモードで 120 行目。
	h.keys("ggvj")
	h.settle()
	h.click(6, 39)
	if m, cur := h.mode(), h.cursor(); m != command.ModeNormal || cur.Line != 119 {
		t.Errorf("ビジュアルモードで印を押した後 = %v %+v", m, cur)
	}
	// Neovim: gg の後に 6 行目の印を押して <C-o> を打つと 1 行目へ戻る。
	h.keys("gg")
	h.settle()
	h.click(6, 39)
	h.keys("<C-o>")
	h.settle()
	if cur := h.cursor(); cur.Line != 0 {
		t.Errorf("<C-o> の後のカーソル = %+v, want 0 行目", cur)
	}
	// インサートモードでは、インサートモードのまま移る。
	h.keys("i")
	h.settle()
	h.click(6, 39)
	h.keys("Q")
	h.settle()
	if m, got := h.mode(), h.cursorLine(); m != command.ModeInsert || got != "Qx 120" {
		t.Errorf("インサートモードで印を押した後 = %v %q", m, got)
	}
	h.keys("<Esc>")
	h.settle()
	// すばやく続けて押した 2 回目と 3 回目（<2-LeftMouse>、<3-LeftMouse>）も、もう 1 回のクリックとして扱い、同じセルの印を順に回る
	// （Neovim では 2 回目が本文の単語の選択になる。docs/vim-differences.md）。つまみの上の印は、離したときに移る。
	h.keys("gg")
	h.settle()
	h.click(3, 39)
	waitBar(h, "..=..=....")
	h.mouse("<2-LeftMouse>", 3, 39)
	h.mouse("<LeftRelease>", 3, 39)
	if m, cur := h.mode(), h.cursor(); m != command.ModeNormal || cur.Line != 51 {
		t.Errorf("印の上の 2 回目のクリックの後 = %v %+v, want 51 行目", m, cur)
	}
	h.mouse("<3-LeftMouse>", 3, 39)
	h.mouse("<LeftRelease>", 3, 39)
	if m, cur := h.mode(), h.cursor(); m != command.ModeNormal || cur.Line != 49 {
		t.Errorf("印の上の 3 回目のクリックの後 = %v %+v, want 49 行目", m, cur)
	}
}

// つまみを押したまま、行を変えずに動かしても（横にぶれても）、表示領域もカーソルも動かさない。つまみの上の印なら、動かさずに
// 離したときと同じくその行へ移る。表示領域が 1 行なら、どこへドラッグしても動かない（nvim-scrollview も、寄せたつまみの行が
// 変わったときだけ最初の行を決め直す）。
//
// req: FR-210, FR-211
func TestScrollbarThumbDragSameRow(t *testing.T) {
	// Neovim: 100G の後につまみを押して横へドラッグして離すと、最初の行 96、カーソル 100 のまま。
	h := barStart(t, barLines(200), nil)
	h.keys("100G")
	h.settle()
	top, cur := h.viewTop(), h.cursor()
	if top != 95 || cur.Line != 99 {
		t.Fatalf("100G の後 = 最初の行 %d、カーソル %+v", top, cur)
	}
	h.mouse("<LeftMouse>", 5, 39)
	h.mouse("<LeftDrag>", 5, 30)
	h.mouse("<LeftRelease>", 5, 30)
	if gt, gc := h.viewTop(), h.cursor(); gt != top || gc != cur {
		t.Errorf("横へのドラッグの後 = 最初の行 %d、カーソル %+v, want %d、%+v", gt, gc, top, cur)
	}

	// 95 行目と 98 行目（本体の 94、97）に一致があり、つまみ（表示領域の 4 行目と 5 行目）の 4 行目の上に印がある。
	h = barStart(t, barLines(200, 95, 98), map[string]option.Value{option.SearchHighlight: option.BoolValue(true)})
	h.keys("/x<CR>100G")
	h.settle()
	waitBar(h, "....=#....")
	h.mouse("<LeftMouse>", 5, 39)
	h.mouse("<LeftDrag>", 5, 30)
	h.mouse("<LeftRelease>", 5, 30)
	if cur := h.cursor(); cur.Line != 94 || h.viewTop() < 90 {
		t.Errorf("つまみの上の印から横へのドラッグの後 = 最初の行 %d、カーソル %+v, want 94 行目", h.viewTop(), cur)
	}

	// 40 列 4 行の端末（表示領域は 1 行）。Neovim の 80 列 3 行: 20G の後に同じ操作をしても、最初の行 20、カーソル 20 のまま。
	dir := writeFiles(t, map[string]string{"a.txt": barLines(50)})
	term := newFakeTerm()
	term.w, term.h = 40, 4
	h = startTerm(t, term, Config{WorkDir: dir, Options: scrollbarOpts(nil)}, "a.txt")
	h.keys("20G")
	h.settle()
	h.mouse("<LeftMouse>", 1, 39)
	h.mouse("<LeftDrag>", 3, 39)
	h.mouse("<LeftRelease>", 3, 39)
	if top, cur := h.viewTop(), h.cursor(); top != 19 || cur.Line != 19 {
		t.Errorf("表示領域が 1 行のドラッグの後 = 最初の行 %d、カーソル %+v, want 19、19 行目", top, cur)
	}
}

// つまみのドラッグの途中でマウスでないキーを受けたら、ドラッグを終える。続くドラッグは何もせず、キーの働き（v の選択、j の移動）は
// そのまま残る（nvim-scrollview も、マウスでない入力でドラッグを終える）。
//
// req: FR-210
func TestScrollbarThumbDragKey(t *testing.T) {
	h := barStart(t, barLines(200), nil)
	h.mouse("<LeftMouse>", 1, 39)
	h.mouse("<LeftDrag>", 3, 39)
	if top, cur := h.viewTop(), h.cursor(); top != 44 || cur.Line != 44 {
		t.Fatalf("ドラッグの後 = 最初の行 %d、カーソル %+v", top, cur)
	}
	h.keys("v")
	h.settle()
	h.mouse("<LeftDrag>", 6, 39)
	h.mouse("<LeftRelease>", 6, 39)
	if top, cur, m := h.viewTop(), h.cursor(), h.mode(); top != 44 || cur.Line != 44 || m != command.ModeVisual {
		t.Errorf("v の後のドラッグ = 最初の行 %d、カーソル %+v、モード %v", top, cur, m)
	}
	h.keys("<Esc>")
	h.settle()

	h.mouse("<LeftMouse>", 3, 39)
	h.mouse("<LeftDrag>", 4, 39)
	top := h.viewTop()
	h.keys("jjj")
	h.settle()
	want := h.cursor()
	h.mouse("<LeftDrag>", 4, 39)
	h.mouse("<LeftDrag>", 6, 39)
	h.mouse("<LeftRelease>", 6, 39)
	if gt, gc := h.viewTop(), h.cursor(); gt != top || gc != want {
		t.Errorf("jjj の後のドラッグ = 最初の行 %d、カーソル %+v, want %d、%+v", gt, gc, top, want)
	}
}

// つまみと印の操作は、mouse.click、mouse.drag、mouse.release の中で位置によって分けるので、keymap でほかのボタンに
// 割り当て直すと、そのボタンでつまみをドラッグできる。<LeftMouse> をほかのキーの列に割り当てると、左のボタンではつまみを押せない。
//
// req: FR-210
func TestScrollbarKeymap(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": barLines(200)})
	term := newFakeTerm()
	term.w, term.h = 40, 13
	cfg := Config{
		WorkDir: dir, Options: scrollbarOpts(nil),
		Keymap: map[string]map[string]string{"normal": {"<LeftMouse>": "j"}},
		KeymapActions: map[string]map[string]string{"normal": {
			"<RightMouse>": "mouse.click", "<RightDrag>": "mouse.drag", "<RightRelease>": "mouse.release",
		}},
	}
	h := startTerm(t, term, cfg, "a.txt")
	h.settle()
	h.mouse("<RightMouse>", 1, 39)
	h.mouse("<RightDrag>", 5, 39)
	h.mouse("<RightRelease>", 5, 39)
	if top := h.viewTop(); top != 88 {
		t.Errorf("右のボタンでドラッグした後の最初の行 = %d, want 88", top)
	}
	// 左のボタンは j なので、つまみ（5 行目）を押してドラッグしても表示領域は動かない（j で 1 行下へ移るだけ）。
	before := h.cursor()
	h.mouse("<LeftMouse>", 5, 39)
	h.mouse("<LeftDrag>", 1, 39)
	h.mouse("<LeftRelease>", 1, 39)
	if top, cur := h.viewTop(), h.cursor(); top != 88 || cur.Line != before.Line+1 {
		t.Errorf("<LeftMouse> を j にした後のドラッグ = 最初の行 %d、カーソル %+v（前は %+v）", top, cur, before)
	}
}

// waitBar は、40 列 13 行の端末の表示領域（10 行）の右端の列が want になるまで待つ。記号は rightColumn と同じである。
func waitBar(h *harness, want string) {
	h.t.Helper()
	h.eventually(fmt.Sprintf("右端の列 %q", want), func() bool { return barColumn(h.term) == want })
}

// barColumn は、40 列 13 行の端末の表示領域（画面の 1 行目から 10 行目）の右端の列を、rightColumn と同じ記号で並べた文字列を返す。
func barColumn(term *fakeTerm) string {
	term.mu.Lock()
	defer term.mu.Unlock()
	g := term.last.Grid
	if g == nil {
		return ""
	}
	thumb := ui.DefaultTheme()[ui.StyleScrollbar]
	x := g.Width() - 1
	var b strings.Builder
	for row := 1; row <= 10; row++ {
		c := g.At(row, x)
		switch {
		case len(c.Text) == 1 && strings.ContainsAny(c.Text, "EWIH="):
			b.WriteString(c.Text)
		case c.Attr.Over(thumb) == c.Attr && c.Attr != (ui.Attr{}):
			b.WriteByte('#')
		default:
			b.WriteByte('.')
		}
	}
	return b.String()
}
