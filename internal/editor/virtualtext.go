package editor

import (
	"maps"
	"slices"
	"strconv"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 拡張機能の仮想テキスト（virtualText/set。基本設計 6.1 の「仮想テキスト」。FR-231〜FR-234）と、範囲に直に付ける色（FR-230）。
//
// 仮想テキストの一覧は、診断と同じく拡張機能とバッファの組ごとに持ち（bufferState.virtualTexts）、バッファへ編集を適用するたびに
// 位置をずらす（commitChange）。描画と列の数え方に渡す表（bufferState.virtView。ui.VirtualTexts）は、一覧かバッファが変わるたびに
// イベントループの上で作り直す。仮想テキストはインレイヒントや行ごとの注記で、構文の色（decoration/set）ほど数が多くならない前提で、
// 構文の色のように別の goroutine では作らない。作り直したら表のポインターが変わるので、描画は表示の位置を決め直す（ui.topKey）。

// setVirtualTexts は、virtualText/set の一覧を、diagnostic/set と同じ規則で、バージョン番号に応じてずらすか捨てて持つ（displayEdits）。
// 空の一覧はその組の一覧を消す。持ったかを返す。拡張機能へ送っていないバッファの一覧は捨てる（withheldBuffer）。
//
// req: FR-231
func (e *Editor) setVirtualTexts(extID string, p ext.VirtualTextSetParams) bool {
	bs := e.findBuffer(p.Buffer)
	if bs == nil || e.withheldBuffer(p.Buffer) {
		return false
	}
	eds, ok := bs.displayEdits(p.Version, len(p.VirtualTexts) == 0)
	if !ok {
		return false
	}
	if len(p.VirtualTexts) == 0 {
		if _, had := bs.virtualTexts[extID]; !had {
			return true
		}
		delete(bs.virtualTexts, extID)
	} else {
		if bs.virtualTexts == nil {
			bs.virtualTexts = map[string][]ext.VirtualText{}
		}
		bs.virtualTexts[extID] = shiftVirtualTexts(p.VirtualTexts, eds)
	}
	bs.rebuildVirtualTexts()
	return true
}

// shiftVirtualTexts は、仮想テキストの位置を編集の列でずらした写しを返す。編集が無ければ vs をそのまま返す。
func shiftVirtualTexts(vs []ext.VirtualText, eds []buffer.Edit) []ext.VirtualText {
	if len(eds) == 0 {
		return vs
	}
	out := make([]ext.VirtualText, len(vs))
	for i, v := range vs {
		v.Position = toExtPos(shiftAll(fromExtPos(v.Position), eds))
		out[i] = v
	}
	return out
}

// shiftAllVirtualTexts は、バッファ bs のすべての拡張機能の仮想テキストを、適用した編集 eds に合わせてずらし、表を作り直す。
func (bs *bufferState) shiftAllVirtualTexts(eds []buffer.Edit) {
	if len(bs.virtualTexts) == 0 {
		return
	}
	for id, vs := range bs.virtualTexts {
		bs.virtualTexts[id] = shiftVirtualTexts(vs, eds)
	}
	bs.rebuildVirtualTexts()
}

// removeVirtualTexts は、拡張機能 extID の仮想テキストを消して表を作り直す。消すものが無ければ何もしない。
func (bs *bufferState) removeVirtualTexts(extID string) {
	if _, ok := bs.virtualTexts[extID]; !ok {
		return
	}
	delete(bs.virtualTexts, extID)
	bs.rebuildVirtualTexts()
}

// clearVirtualTexts は、バッファ bs のすべての仮想テキストを消す。
func (bs *bufferState) clearVirtualTexts() {
	bs.virtualTexts = nil
	bs.virtView = nil
}

// rebuildVirtualTexts は、拡張機能ごとの一覧を合わせて、描画に渡す ui.VirtualTexts を作り直す。
// 拡張機能の ID の順に並べ、同じ位置のものは ID の順、同じ拡張機能の中では一覧の順に描く（decoSources と同じ並べ方）。
func (bs *bufferState) rebuildVirtualTexts() {
	if len(bs.virtualTexts) == 0 {
		bs.virtView = nil
		return
	}
	var vts []ui.VirtualText
	attrs := map[*ext.TextAttr]ui.Attr{}
	for _, id := range slices.Sorted(maps.Keys(bs.virtualTexts)) {
		for _, v := range bs.virtualTexts[id] {
			chunks := make([]ui.VirtualChunk, len(v.Chunks))
			for i, c := range v.Chunks {
				chunks[i] = ui.VirtualChunk{Text: c.Text, Style: c.Style, Attr: cachedAttr(attrs, c.Attr)}
			}
			vts = append(vts, ui.VirtualText{Position: fromExtPos(v.Position), Placement: uiPlacement(v.Placement), Chunks: chunks})
		}
	}
	bs.virtView = ui.NewVirtualTexts(vts, bs.buf.LineCount())
}

// uiPlacement は、仮想テキストの置き方を描画の値にする。知らない置き方は exthost が Validate で断っている。
func uiPlacement(p ext.VirtualTextPlacement) ui.VirtualPlacement {
	switch p {
	case ext.VirtualTextEOL:
		return ui.VirtualEOL
	case ext.VirtualTextOverlay:
		return ui.VirtualOverlay
	case ext.VirtualTextInline:
	}
	return ui.VirtualInline
}

// cachedAttr は、uiAttr(a) を、同じポインターについて 1 回だけ求めて cache に覚えて返す。
// decoration/set の受け取った一覧は、styles の表の同じ項目を指す範囲が同じ値を共有する（ext.Decoration.Attr）ので、
// 数十万の範囲でも、色の文字列を読むのは表の項目の数だけになる。
func cachedAttr(cache map[*ext.TextAttr]ui.Attr, a *ext.TextAttr) ui.Attr {
	if a == nil {
		return ui.Attr{}
	}
	if v, ok := cache[a]; ok {
		return v
	}
	v := uiAttr(a)
	cache[a] = v
	return v
}

// uiAttr は、拡張機能が直に付けた色と飾り（FR-230）を、描画の属性にする。色は ext が "#rrggbb" か空であることを確かめてある。
//
// req: FR-230
func uiAttr(a *ext.TextAttr) ui.Attr {
	if a == nil {
		return ui.Attr{}
	}
	return ui.Attr{
		Fg: hexColor(a.Fg), Bg: hexColor(a.Bg), Sp: hexColor(a.Sp),
		Bold: a.Bold, Italic: a.Italic, Underline: a.Underline, Undercurl: a.Undercurl, Reverse: a.Reverse,
	}
}

// hexColor は、"#rrggbb" の色を描画の色にする。空か読めなければ端末の既定の色（付けない）にする。
func hexColor(s string) ui.Color {
	if !ext.IsHexColor(s) {
		return ui.ColorDefault
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return ui.ColorDefault
	}
	return ui.RGB(uint8(v>>16), uint8(v>>8), uint8(v))
}
