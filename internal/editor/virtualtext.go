package editor

import (
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 拡張機能の仮想テキスト（virtualText/set。基本設計 6.1 の「仮想テキスト」。FR-231〜FR-234）と、範囲に直に付ける色（FR-230）。
//
// 仮想テキストは、拡張機能とバッファの組ごとに ui.VirtualLayer で持ち（bufferState.virtLayers）、バッファの全体の並び
// （bufferState.virtView。ui.VirtualTexts）を描画と列の数え方に渡す。持ち方は ui の virtuallayer.go の doc にある。
//
//   - 一覧が届いたら、その拡張機能の VirtualLayer だけを作り直す。版の番号の扱いは診断と同じで、古い版の一覧は今の版へずらす（displayEdits）
//   - 編集のたびに、どの VirtualLayer も、編集の範囲の行だけをずらし、後ろの行は行の番号を足す（commitChange の shiftAllVirtualTexts）
//
// 前は、編集のたびと一覧が届くたびに、全部の拡張機能の一覧から表を一から作り直し、3 万行に 9 万個のインレイヒントで 1 文字ごとに約 58 ms
// かかった（PR #378 のレビューの必須 1）。

// setVirtualTexts は、virtualText/set の一覧を、diagnostic/set と同じ規則で、バージョン番号に応じてずらすか捨てて持つ（displayEdits）。
// 空の一覧はその組の一覧を消す。持ったかを返す。拡張機能へ送っていないバッファの一覧は捨てる（withheldBuffer）。
// 作り直すのは、送ってきた拡張機能の VirtualLayer だけである。
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
		bs.removeVirtualTexts(extID)
		return true
	}
	// 古い版の位置は、ずらす前は今の行の数より後ろの行を指しうるので、行の数で切るのはずらした後にする。
	lines := bs.buf.LineCount()
	if len(eds) > 0 {
		lines = math.MaxInt
	}
	l := ui.NewVirtualLayer(uiVirtualTexts(p.VirtualTexts), lines)
	for _, ed := range eds {
		// 前の版の行の長さは覚えていないので、行末への挿入かは数えない（eolInsert の doc）。
		l.ApplyEdit(ed, false)
	}
	l.Clip(bs.buf.LineCount())
	if bs.virtLayers == nil {
		bs.virtLayers = map[string]*ui.VirtualLayer{}
	}
	bs.virtLayers[extID] = l
	bs.setVirtLayers()
	return true
}

// uiVirtualTexts は、拡張機能の仮想テキストの一覧を描画の値にする。同じ値の直の色は、色の文字列を 1 回だけ読む。
func uiVirtualTexts(vs []ext.VirtualText) []ui.VirtualText {
	attrs := map[ext.TextAttr]ui.Attr{}
	out := make([]ui.VirtualText, len(vs))
	for i, v := range vs {
		chunks := make([]ui.VirtualChunk, len(v.Chunks))
		for j, c := range v.Chunks {
			chunks[j] = ui.VirtualChunk{Text: c.Text, Style: c.Style}
			if c.Attr != nil {
				a, ok := attrs[*c.Attr]
				if !ok {
					a = uiAttr(c.Attr)
					attrs[*c.Attr] = a
				}
				chunks[j].Attr = a
			}
		}
		out[i] = ui.VirtualText{Position: fromExtPos(v.Position), Placement: uiPlacement(v.Placement), Chunks: chunks}
	}
	return out
}

// shiftAllVirtualTexts は、バッファ bs のすべての拡張機能の仮想テキストを、適用した変更 ch の編集に合わせてずらす。
// 時間は、編集の範囲の行の仮想テキストの数と、行の数の変わる編集では仮想テキストのある行の数に比例する（ui.VirtualLayer.ApplyEdit）。
//
// req: FR-231
func (bs *bufferState) shiftAllVirtualTexts(ch buffer.Change) {
	if len(bs.virtLayers) == 0 {
		return
	}
	atEOL := bs.eolInsert(ch)
	changed := false
	for _, l := range bs.virtLayers {
		for _, ed := range ch.Edits {
			if l.ApplyEdit(ed, atEOL) {
				changed = true
			}
		}
	}
	if changed {
		bs.virtView.Changed()
	}
}

// eolInsert は、変更 ch が、行末の位置への改行で始まるテキストの挿入 1 つだけかを返す（ui.VirtualLayer.ApplyEdit の atEOL）。
// o と行単位の p（下へ足す）と行末での <CR> がこの形になり、行末の仮想テキストを元の行に残す。
//
// 編集の前の行の長さは覚えていないので、編集の後のテキストから数える。挿入の後の最後の行は「入れたテキストの最後の行」と
// 「挿入の位置より後ろの元の行の残り」をつないだものなので、それが入れたテキストの最後の行と同じなら、挿入の位置は行末だった。
// 数えられるのは、バッファがちょうど ch の後の版のとき（後に続く変更をまだ適用していないとき）だけで、そうでなければ行末への挿入として扱わない。
func (bs *bufferState) eolInsert(ch buffer.Change) bool {
	if len(ch.Edits) != 1 || bs.buf.Version() != ch.After {
		return false
	}
	ed := ch.Edits[0]
	if ed.Range.Start != ed.Range.End || !strings.HasPrefix(ed.Text, "\n") {
		return false
	}
	last := ed.Range.Start.Line + strings.Count(ed.Text, "\n")
	if last >= bs.buf.LineCount() {
		return false
	}
	return bs.buf.Line(last) == ed.Text[strings.LastIndexByte(ed.Text, '\n')+1:]
}

// removeVirtualTexts は、拡張機能 extID の仮想テキストを消す。消すものが無ければ何もしない。
func (bs *bufferState) removeVirtualTexts(extID string) {
	if _, ok := bs.virtLayers[extID]; !ok {
		return
	}
	delete(bs.virtLayers, extID)
	bs.setVirtLayers()
}

// clearVirtualTexts は、バッファ bs のすべての仮想テキストを消す。
func (bs *bufferState) clearVirtualTexts() {
	bs.virtLayers = nil
	bs.virtView = nil
}

// setVirtLayers は、拡張機能ごとの VirtualLayer を、拡張機能の ID の順に並べて virtView にする。同じ位置のものは ID の順、
// 同じ拡張機能の中では一覧の順に描く（decoSources と同じ並べ方）。仮想テキストが無くなれば virtView を nil にする。
func (bs *bufferState) setVirtLayers() {
	layers := make([]*ui.VirtualLayer, 0, len(bs.virtLayers))
	for _, id := range slices.Sorted(maps.Keys(bs.virtLayers)) {
		layers = append(layers, bs.virtLayers[id])
	}
	if bs.virtView == nil {
		bs.virtView = &ui.VirtualTexts{}
	}
	bs.virtView.SetLayers(layers)
	if bs.virtView.Empty() {
		bs.virtView = nil
	}
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

// cachedAttr は、uiAttr(a) を、同じポインター a について 1 回だけ求めて cache に覚え、その値を指すポインターを返す。a は nil でない。
// decoration/set の受け取った一覧は、styles の表の同じ項目を指す範囲が同じ値を共有する（ext.Decoration.Attr）ので、
// 数十万の範囲でも、色の文字列を読むのは表の項目の数だけになり、描画の範囲（ui.Highlight.Attr）も同じ値を指す。
func cachedAttr(cache map[*ext.TextAttr]*ui.Attr, a *ext.TextAttr) *ui.Attr {
	if v, ok := cache[a]; ok {
		return v
	}
	v := uiAttr(a)
	cache[a] = &v
	return &v
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
	return ui.RGB(hexByte(s[1:3]), hexByte(s[3:5]), hexByte(s[5:7]))
}

// hexByte は、16 進の 2 桁 s（IsHexColor で確かめたもの）の値を返す。
func hexByte(s string) byte {
	var v byte
	for i := range 2 {
		c := s[i]
		switch {
		case c >= 'a':
			c = c - 'a' + 10
		case c >= 'A':
			c = c - 'A' + 10
		default:
			c -= '0'
		}
		v = v<<4 | c
	}
	return v
}
