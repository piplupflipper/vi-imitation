package editor

import (
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// inlineVT は、最初の行のバイト b の inline の仮想テキストである。
func inlineVT(b int, text string) ext.VirtualText {
	return ext.VirtualText{Position: ext.Position{Line: 0, Byte: b}, Placement: ext.VirtualTextInline, Chunks: []ext.VirtualTextChunk{{Text: text}}}
}

// setVT は、カーソルのあるバッファの今の版で、拡張機能 x の仮想テキストを vts にする。
func (h *harness) setVT(vts ...ext.VirtualText) {
	h.e.do(func() {
		bs := h.e.cur
		h.e.setVirtualTexts("x", ext.VirtualTextSetParams{Buffer: bs.id, Version: ext.Version(bs.buf.Version()), VirtualTexts: vts})
	})
}

// cell は、最後に描いた画面のうち、prefix で始まる最初の行の col 列のセルを返す（タブの行の有無によらず、バッファの行を引くため）。
// その行が無ければ空白のセルを返す。
func (h *harness) cell(prefix string, col int) ui.Cell {
	h.term.mu.Lock()
	defer h.term.mu.Unlock()
	g := h.term.last.Grid
	if g == nil {
		return ui.Cell{}
	}
	for row := range g.Height() {
		if strings.HasPrefix(g.RowText(row), prefix) {
			return g.At(row, col)
		}
	}
	return ui.Cell{}
}

// 仮想テキストのある行で、j と k の望みの列と、横に動くカーソルが仮想テキストの列を数え、カーソルを仮想テキストの上に置かない
// （nvim 0.12.5 で "abcdef" の 2 バイト目に "XXXX" を置き、e で j を押すと下の行の i へ、下の行の d で k を押すと c へ移った）。
// インサートモードのカーソルは仮想テキストの前に置き、打った文字は仮想テキストの前に入る（仮想テキストは打った文字の後ろへずれる）。
//
// req: FR-231, FR-232
func TestVirtualTextCursorMotion(t *testing.T) {
	h := start(t, Config{})
	h.keys("iabcdef<CR>abcdefghijklmnop<Esc>gg0")
	h.settle()
	h.setVT(inlineVT(2, "XXXX"))
	h.screenHas("inline の仮想テキスト", "abXXXXcdef\nabcdefghijklmnop")
	h.keys("4lj")
	h.settle()
	if got := h.cursor(); got != (buffer.Position{Line: 1, Byte: 8}) {
		t.Errorf("e から j = %+v, want 1,8（i）", got)
	}
	h.keys("k")
	h.settle()
	if got := h.cursor(); got != (buffer.Position{Line: 0, Byte: 4}) {
		t.Errorf("i から k = %+v, want 0,4（e）", got)
	}
	for _, keys := range []string{"j0lllk", "j0llllk", "j0llllllk"} {
		h.keys(keys)
		h.settle()
		if got := h.cursor(); got != (buffer.Position{Line: 0, Byte: 2}) {
			t.Errorf("%s = %+v, want 0,2（c。仮想テキストの列からは後ろの文字へ）", keys, got)
		}
	}
	h.keys("0iQ<Esc>")
	h.settle()
	// 0 で行頭、i で a の前に Q を入れた。仮想テキストは c の前のまま、1 バイト後ろへずれる。
	h.screenHas("前の編集でずれる", "QabXXXXcdef")
	h.keys("0llli")
	h.settle()
	h.term.mu.Lock()
	c := h.term.last.Cursor
	h.term.mu.Unlock()
	if c.Col != 3 {
		t.Errorf("インサートモードの c の前のカーソルの列 = %d, want 3（仮想テキストの前）", c.Col)
	}
	h.keys("Z<Esc>")
	h.screenHas("仮想テキストの位置で打った文字は前に入る", "QabZXXXXcdef")
}

// virtualText/set は diagnostic/set と同じく、古い版の一覧を今の版へずらし、新しい版の一覧を捨てる。空の一覧で消え、拡張機能が
// 異常で終わったら消える。描くのは client の画面のバッファの表示領域だけで、lineLayout が渡す。
//
// req: FR-231, FR-234
func TestVirtualTextVersions(t *testing.T) {
	h := start(t, Config{})
	h.keys("iabc<Esc>")
	h.settle()
	var old ext.Version
	h.e.do(func() { old = ext.Version(h.e.cur.buf.Version()) })
	h.keys("0iZZ<Esc>")
	h.settle()
	h.e.do(func() {
		e, bs := h.e, h.e.cur
		if !e.setVirtualTexts("x", ext.VirtualTextSetParams{Buffer: bs.id, Version: old, VirtualTexts: []ext.VirtualText{inlineVT(1, "V")}}) {
			t.Fatal("覚えている古い版の一覧を持たない")
		}
		if e.setVirtualTexts("y", ext.VirtualTextSetParams{Buffer: bs.id, Version: old + 100, VirtualTexts: []ext.VirtualText{inlineVT(0, "N")}}) {
			t.Error("新しい版の一覧を持った")
		}
		if l := e.lineLayout(bs, false); l.Virtual != nil {
			t.Error("ポップアップとプレビューの並べ方に仮想テキストがある")
		}
		if l := e.lineLayout(bs, true); l.Virtual == nil {
			t.Error("client の画面の並べ方に仮想テキストが無い")
		}
	})
	h.screenHas("ずらした位置", "ZZaVbc")
	h.e.do(func() { h.e.clearExtension("x") })
	h.keys("<Esc>")
	h.screenHas("拡張機能が終わって消える", "ZZabc\n")
	h.setVT(inlineVT(0, "W"))
	h.screenHas("持つ", "WZZabc")
	h.setVT()
	h.keys("<Esc>")
	h.screenHas("空の一覧で消える", "ZZabc\n")
}

// J と gUU は、行の中身を置き換えずに、つなぐ箇所と変わる文字の続きだけを編集するので、行の仮想テキストは行頭へ寄らない
// （PR #378 のレビューの推奨 1）。2026-11-22 に Neovim 0.12.5 の nvim_buf_get_extmarks で確かめた位置と同じにした
// （xyz と空の行の J は xyz123。abc def の b の前の inline は、gUU で変わった文字の続き abc の後ろへ動く）。
//
// req: FR-231
func TestVirtualTextJoinAndCase(t *testing.T) {
	h := start(t, Config{})
	h.keys("ixyz<CR><CR>abc def<Esc>gg0")
	h.settle()
	h.setVT(inlineVT(3, "123"), ext.VirtualText{Position: ext.Position{Line: 2, Byte: 1}, Placement: ext.VirtualTextInline, Chunks: []ext.VirtualTextChunk{{Text: "<b>"}}})
	h.keys("J")
	h.screenHas("J の後の行末の inline", "xyz123\na<b>bc def")
	h.keys("jgUU")
	h.screenHas("gUU の後", "xyz123\nABC<b> DEF")
	// 取り消しの編集も変わった文字の続きだけを置き換えるので、仮想テキストはその続きの後ろに残る（行頭へは寄らない）。
	h.keys("u")
	h.screenHas("u の後", "xyz123\nabc<b> def")
}

// decoration/set の範囲に直に付けた色（ext.Decoration.Attr）を、名前の色の上に重ねて描く。名前の無い範囲も直の色で描く。
//
// req: FR-230
func TestDecorationAttrDrawn(t *testing.T) {
	h := start(t, Config{})
	h.keys("iabcdef<Esc>")
	h.settle()
	hint := &ext.TextAttr{Fg: "#8b80a8", Italic: true}
	h.e.do(func() {
		bs := h.e.cur
		h.e.setDecorations("x", ext.DecorationSetParams{Buffer: bs.id, Version: ext.Version(bs.buf.Version()), Decorations: []ext.Decoration{
			{Range: ext.Range{Start: ext.Position{Byte: 0}, End: ext.Position{Byte: 2}}, Attr: hint},
			{Range: ext.Range{Start: ext.Position{Byte: 2}, End: ext.Position{Byte: 4}}, Style: "comment", Attr: &ext.TextAttr{Bg: "#FF0000"}},
		}})
	})
	want := ui.Attr{Fg: ui.RGB(0x8b, 0x80, 0xa8), Italic: true}
	h.eventually("直の色", func() bool { return h.cell("abcdef", 0).Attr == want })
	if a := h.cell("abcdef", 2).Attr; a.Bg != ui.RGB(255, 0, 0) || a.Fg == ui.ColorDefault {
		t.Errorf("名前の色の上の直の色 = %+v, want 名前の文字の色と赤の背景", a)
	}
	if a := h.cell("abcdef", 4).Attr; a != (ui.Attr{}) {
		t.Errorf("範囲の外 = %+v", a)
	}
}

// eol の仮想テキストを、行末の 1 列の次に、直の色で描く（インレイヒントの色の例）。
//
// req: FR-231, FR-234
func TestVirtualTextEOLDrawn(t *testing.T) {
	h := start(t, Config{})
	h.keys("iabc<Esc>")
	h.settle()
	h.setVT(ext.VirtualText{Position: ext.Position{Line: 0, Byte: 3}, Placement: ext.VirtualTextEOL, Chunks: []ext.VirtualTextChunk{{Text: "hint", Attr: &ext.TextAttr{Fg: "#8b80a8"}}}})
	h.keys("<Esc>")
	h.screenHas("eol", "abc hint")
	if a := h.cell("abc hint", 4).Attr; a.Fg != ui.RGB(0x8b, 0x80, 0xa8) {
		t.Errorf("eol の色 = %+v", a)
	}
}
