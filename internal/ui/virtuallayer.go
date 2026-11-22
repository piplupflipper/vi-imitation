package ui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// 仮想テキストの持ち方（FR-231。基本設計 6.1 の「仮想テキスト」の「持ち方」）。
//
// 拡張機能ごとの一覧を VirtualLayer に行ごとに分けて持ち、バッファの仮想テキストの全体（VirtualTexts）は、その並びとして持つ。
// どちらも作った後に持ち主（internal/editor のイベントループ）が変える。描画と列の数え方も同じイベントループの上で読むので、排他制御はしない。
//
// 前は、編集のたびと、どの拡張機能の一覧が届くたびにも、全部の拡張機能の一覧から表を一から作り直していたので、3 万行に 9 万個の
// インレイヒントのあるバッファで 1 文字の入力ごとに約 58 ms かかった（PR #378 のレビューの必須 1）。いまは次のとおりにする。
//
//   - 一覧が届いたら、その拡張機能の VirtualLayer だけを作り直す（時間はその一覧の大きさに比例する）
//   - 編集では、編集の範囲の行の仮想テキストだけを位置をずらして分け直し、それより後ろの行は行の番号を足すだけにする（ApplyEdit）。
//     行の数が変わらない編集（1 行の中の入力）で、編集の行に仮想テキストが無ければ、何もしない
//   - 描く形（lineVirtual）は、引かれた行の分だけ作って覚え、その行が変わったときに捨てる。複数の拡張機能が同じ行に仮想テキストを持つ行は、
//     合わせた形を VirtualTexts が覚え、どれかが変わったら捨てる

// VirtualLayer は、1 つの拡張機能の仮想テキストを行ごとに分けたものである。NewVirtualLayer で作り、ApplyEdit で編集に合わせてずらす。
type VirtualLayer struct {
	// lines は、仮想テキストのある行を、行の番号の順に並べたものである。
	lines []layerLine
}

// layerLine は、1 つの拡張機能の、1 行の仮想テキストである。
type layerLine struct {
	line  int
	items []virtItem // 位置のバイトの順（同じなら一覧の順）
	lv    *lineVirtual
}

// virtItem は、1 つの仮想テキスト（VirtualText）の、行の中の位置と中身である。
type virtItem struct {
	byte      int
	placement VirtualPlacement
	order     int // 拡張機能の一覧の中の位置
	pieces    []virtPiece
}

// lineVirtual は、1 行の仮想テキストを描く形である。
type lineVirtual struct {
	// inline は、inline の文字列を、位置のバイトの順（同じなら一覧の順）に並べたものである。
	inline []virtPiece
	// eol と overlay は、eol と overlay の仮想テキストを、位置のバイトの順（同じなら一覧の順）に並べたものである。
	eol, overlay []virtGroup
}

// virtPiece は、仮想テキストの 1 つの文字列（VirtualChunk）である。
type virtPiece struct {
	byte  int       // 位置のバイト（0 以上）
	order int       // 描く順（同じ位置の inline と overlay の順に使う。virtGroup.order と同じ数え方）
	text  string    // 描く文字列。空でない
	style decoStyle // 色の種類の名前と直に重ねる色
}

// virtGroup は、eol か overlay の 1 つの仮想テキストの文字列の並びである。
type virtGroup struct {
	byte   int
	order  int
	pieces []virtPiece
}

// NewVirtualLayer は、1 つの拡張機能の仮想テキストの一覧 vts を行ごとに分ける。lines は描くテキストの行数で、その外の行の仮想テキストは捨てる。
// 文字列の空な VirtualChunk は捨て、どの文字列も空な仮想テキストも捨てる。
// 同じ値の直の色（VirtualChunk.Attr）は 1 つの値を指させる（decoStyle はポインターで比べるため）。
//
// req: FR-231
func NewVirtualLayer(vts []VirtualText, lines int) *VirtualLayer {
	attrs := map[Attr]*Attr{}
	l := &VirtualLayer{}
	byLine := map[int][]virtItem{}
	for i, vt := range vts {
		n := vt.Position.Line
		if n < 0 || n >= lines {
			continue
		}
		it := virtItem{byte: max(vt.Position.Byte, 0), placement: vt.Placement, order: i}
		for _, c := range vt.Chunks {
			t := virtualTextOf(c.Text)
			if t == "" {
				continue
			}
			st := decoStyle{name: c.Style}
			if c.Attr != (Attr{}) {
				a, ok := attrs[c.Attr]
				if !ok {
					v := c.Attr
					a = &v
					attrs[c.Attr] = a
				}
				st.attr = a
			}
			it.pieces = append(it.pieces, virtPiece{text: t, style: st})
		}
		if len(it.pieces) > 0 {
			byLine[n] = append(byLine[n], it)
		}
	}
	l.lines = make([]layerLine, 0, len(byLine))
	for n, items := range byLine {
		sortItems(items)
		l.lines = append(l.lines, layerLine{line: n, items: items})
	}
	slices.SortFunc(l.lines, func(a, b layerLine) int { return cmp.Compare(a.line, b.line) })
	return l
}

// sortItems は、1 行の仮想テキストを、位置のバイトの順（同じなら一覧の順）に並べる。
func sortItems(items []virtItem) {
	slices.SortFunc(items, func(a, b virtItem) int { return cmp.Or(cmp.Compare(a.byte, b.byte), cmp.Compare(a.order, b.order)) })
}

// virtualTextOf は、仮想テキストの文字列の改行、復帰、タブ文字を空白にする。
// タブ文字の幅は置く列で変わり、改行は 1 行に描けないので、どちらも幅の決まった空白 1 つにする。
func virtualTextOf(s string) string {
	if !strings.ContainsAny(s, "\t\n\r") {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

// Empty は、仮想テキストが 1 つも無いかを返す。l が nil でもよい。
func (l *VirtualLayer) Empty() bool { return l == nil || len(l.lines) == 0 }

// Clip は、lines 行目以降の行の仮想テキストを捨てる。
func (l *VirtualLayer) Clip(lines int) {
	l.lines = l.lines[:l.find(lines)]
}

// find は、行の番号が n 以上の最初の lines の添字を返す。
func (l *VirtualLayer) find(n int) int {
	i, _ := slices.BinarySearchFunc(l.lines, n, func(e layerLine, n int) int { return cmp.Compare(e.line, n) })
	return i
}

// line は、n 行目の描く形を、rank（並びの中の拡張機能の位置）で順を数えて返す。無ければ nil を返す。
// 同じ rank で引いた形は、その行が変わるまで覚えておく。
func (l *VirtualLayer) line(n, rank int) *lineVirtual {
	i := l.find(n)
	if i >= len(l.lines) || l.lines[i].line != n {
		return nil
	}
	ll := &l.lines[i]
	if ll.lv == nil {
		ll.lv = buildLine(ll.items, rank)
	}
	return ll.lv
}

// rankShift は、並びの中の拡張機能の位置を、描く順（virtPiece.order）の上の桁にするための桁数である。
// 一覧の中の位置は 2^32 個より少ない前提で、拡張機能の順を先に、同じ拡張機能の中では一覧の順に描く。
const rankShift = 32

// buildLine は、1 行の仮想テキストから描く形を作る。
func buildLine(items []virtItem, rank int) *lineVirtual {
	lv := &lineVirtual{}
	for _, it := range items {
		order := rank<<rankShift | it.order
		switch it.placement {
		case VirtualInline:
			for _, p := range it.pieces {
				p.byte, p.order = it.byte, order
				lv.inline = append(lv.inline, p)
			}
		case VirtualEOL:
			lv.eol = append(lv.eol, virtGroup{byte: it.byte, order: order, pieces: it.pieces})
		case VirtualOverlay:
			lv.overlay = append(lv.overlay, virtGroup{byte: it.byte, order: order, pieces: it.pieces})
		}
	}
	return lv
}

// ApplyEdit は、バッファに適用した編集 ed に合わせて、仮想テキストの位置をずらす。変えたかを返す。
// 編集の範囲の行の仮想テキストは shiftVirtual でずらして行ごとに分け直し、範囲より後ろの行は、増減した行の数を行の番号に足す。
//
// ただし、行末の仮想テキストは、その行の改行の手前に結び付けて数え、改行の後ろに入れた本文では動かさない（left gravity）。
//
//   - atEOL（ed が行末の位置への挿入であること。持ち主が編集の前の行の長さから決める）で、ed のテキストが改行で始まるなら、
//     ed の位置にある仮想テキストは inline も eol も動かさない。o と行単位の p は行末に「改行と行」を入れる編集として届くので、
//     行末のインレイヒントと注記は、Neovim 0.12.5 と同じく元の行に残る（最後の行の下に足すときも同じ。PR #378 の再レビューの必須 1）
//   - eol の仮想テキストは、行に付けるもので、位置のバイトは並べる順にだけ使う（ext.VirtualTextEOL）ので、編集の範囲の始まりと同じ位置に
//     あれば、どの編集でも動かさない（J の後の u で、行末の注記が元の行に戻る）
//
// 行末での <CR>（A<CR>）も o と同じ形の編集で届くので、行末の仮想テキストは元の行に残る（Neovim は次の行へ移す。docs/vim-differences.md）。
// 編集の形からは o と A<CR> を見分けられないためである。行の途中の <CR> では、行末の位置は範囲の始まりより後ろなので、次の行へ移る（Neovim と同じ）。
//
// req: FR-231
func (l *VirtualLayer) ApplyEdit(ed buffer.Edit, atEOL bool) bool {
	if l.Empty() {
		return false
	}
	s, e := ed.Range.Start, ed.Range.End
	newEnd := editEnd(ed)
	delta := newEnd.Line - e.Line
	i0, i1 := l.find(s.Line), l.find(e.Line+1)
	if i0 == i1 && delta == 0 {
		return false
	}
	for i := i1; i < len(l.lines); i++ {
		l.lines[i].line += delta
	}
	if i0 == i1 {
		return true
	}
	// 範囲の行の仮想テキストをずらして、ずらした後の行ごとに分け直す。ずらした後の行は s.Line から newEnd.Line の間にあり、
	// 範囲より前の行と、行の番号を足した後ろの行の間に収まる。
	newlineAtEOL := atEOL && s == e && strings.HasPrefix(ed.Text, "\n")
	byLine := map[int][]virtItem{}
	for _, ll := range l.lines[i0:i1] {
		for _, it := range ll.items {
			p := buffer.Position{Line: ll.line, Byte: it.byte}
			if p != s || (it.placement != VirtualEOL && !newlineAtEOL) {
				p = shiftVirtual(p, ed, newEnd)
			}
			it.byte = p.Byte
			byLine[p.Line] = append(byLine[p.Line], it)
		}
	}
	moved := make([]layerLine, 0, len(byLine))
	for n, items := range byLine {
		sortItems(items)
		moved = append(moved, layerLine{line: n, items: items})
	}
	slices.SortFunc(moved, func(a, b layerLine) int { return cmp.Compare(a.line, b.line) })
	l.lines = slices.Replace(l.lines, i0, i1, moved...)
	return true
}

// editEnd は、編集 ed を適用した後のテキストの、入れたテキストの終わりの位置を返す。
func editEnd(ed buffer.Edit) buffer.Position {
	s := ed.Range.Start
	nl := strings.Count(ed.Text, "\n")
	if nl == 0 {
		return buffer.Position{Line: s.Line, Byte: s.Byte + len(ed.Text)}
	}
	return buffer.Position{Line: s.Line + nl, Byte: len(ed.Text) - strings.LastIndex(ed.Text, "\n") - 1}
}

// shiftVirtual は、仮想テキストの位置 p を、編集 ed（入れたテキストの終わりは newEnd）の後のテキストの位置へずらす。
// Neovim 0.12.5 の extmark の既定（right_gravity）と同じく、編集の範囲の中か、範囲の始まりか終わりと同じ位置は、入れたテキストの後ろへ移す
// （範囲を消して範囲の始まりに寄せた後、入れたテキストに押されて後ろへ動く）。範囲より前の位置は動かさず、範囲より後ろの位置は、
// 編集で増減した行の数だけ動かし、範囲の終わりと同じ行にあれば列のバイト数も動かす。
//
// 診断と印のずらし方（internal/editor の shiftPos）は、範囲の中の位置を範囲の始まりに寄せる。仮想テキストを後ろへ動かすのは、
// Neovim 0.12.5 の位置に合わせるためである。2026-11-22 に nvim_buf_get_extmarks で、abc def の b の前の inline が gUU で ABC の後ろへ、
// xyz と "  abc" の J で、xyz の行末と次の行の先頭の inline が入れた空白の後ろへ動くことを確かめた（大文字と小文字の操作は変わる文字の続き
// だけを、J はつなぐ箇所の改行と空白だけを置き換える編集にしてある。internal/command の caseEdits と actJoin）。
func shiftVirtual(p buffer.Position, ed buffer.Edit, newEnd buffer.Position) buffer.Position {
	s, end := ed.Range.Start, ed.Range.End
	switch {
	case p.Less(s):
		return p
	case !end.Less(p):
		return newEnd
	case p.Line == end.Line:
		return buffer.Position{Line: newEnd.Line, Byte: newEnd.Byte + p.Byte - end.Byte}
	}
	return buffer.Position{Line: p.Line + newEnd.Line - end.Line, Byte: p.Byte}
}

// VirtualTexts は、1 つのバッファの仮想テキストの全体（拡張機能ごとの VirtualLayer の並び）である。
// LineLayout.Virtual に入れて渡し、描画、望みの列、折り返しの数え方が同じ値を読む。nil は仮想テキストが無いことを表す。
//
// 中身は持ち主が SetLayers と Changed で変える。表示の位置を決め直すかの判定（topKey）は、変えるたびに進む世代の番号（Gen）で
// 見分ける。
type VirtualTexts struct {
	layers []*VirtualLayer
	gen    uint64
	// merged は、複数の拡張機能が仮想テキストを持つ行の、合わせた描く形の覚えである。Changed で捨てる。
	merged map[int]*lineVirtual
}

// NewVirtualTexts は、1 つの拡張機能の一覧 vts だけからなる VirtualTexts を返す（NewVirtualLayer と SetLayers の組）。
// lines は描くテキストの行数である。仮想テキストが無ければ nil を返す。
func NewVirtualTexts(vts []VirtualText, lines int) *VirtualTexts {
	l := NewVirtualLayer(vts, lines)
	if l.Empty() {
		return nil
	}
	v := &VirtualTexts{}
	v.SetLayers([]*VirtualLayer{l})
	return v
}

// SetLayers は、拡張機能ごとの仮想テキストの並びを layers にする。並びの前のものを先に描く（同じ位置の inline を左に並べ、
// overlay を下に重ねる）。空の VirtualLayer は除く。layers の中身は、この後も持ち主が ApplyEdit で変えてよく、変えたら Changed を呼ぶ。
func (v *VirtualTexts) SetLayers(layers []*VirtualLayer) {
	v.layers = v.layers[:0]
	for _, l := range layers {
		if !l.Empty() {
			v.layers = append(v.layers, l)
		}
	}
	// 拡張機能の位置（rank）が変わると、覚えた描く形の順が合わなくなるので、すべて捨てる。
	for _, l := range v.layers {
		for i := range l.lines {
			l.lines[i].lv = nil
		}
	}
	v.Changed()
}

// Changed は、並びの VirtualLayer のどれかを変えた（ApplyEdit で位置をずらした）ことを知らせ、合わせた描く形の覚えを捨てて、世代の番号を進める。
func (v *VirtualTexts) Changed() {
	v.gen++
	clear(v.merged)
}

// Empty は、仮想テキストが 1 つも無いかを返す。v が nil でもよい。
func (v *VirtualTexts) Empty() bool {
	if v == nil {
		return true
	}
	for _, l := range v.layers {
		if !l.Empty() {
			return false
		}
	}
	return true
}

// Gen は、中身の世代の番号である。中身を変えるたびに進む。v が nil なら 0 を返す。
func (v *VirtualTexts) Gen() uint64 {
	if v == nil {
		return 0
	}
	return v.gen
}

// line は、n 行目の描く形を返す。無ければ nil を返す。v が nil でもよい。
// 時間は拡張機能の数と、拡張機能ごとの仮想テキストのある行の数の対数に比例する。
func (v *VirtualTexts) line(n int) *lineVirtual {
	if v == nil {
		return nil
	}
	var one *lineVirtual
	count := 0
	for rank, l := range v.layers {
		if lv := l.line(n, rank); lv != nil {
			one = lv
			count++
		}
	}
	if count <= 1 {
		return one
	}
	if lv, ok := v.merged[n]; ok {
		return lv
	}
	lv := &lineVirtual{}
	for rank, l := range v.layers {
		if p := l.line(n, rank); p != nil {
			lv.inline = append(lv.inline, p.inline...)
			lv.eol = append(lv.eol, p.eol...)
			lv.overlay = append(lv.overlay, p.overlay...)
		}
	}
	slices.SortStableFunc(lv.inline, func(a, b virtPiece) int { return cmp.Compare(a.byte, b.byte) })
	slices.SortStableFunc(lv.eol, func(a, b virtGroup) int { return cmp.Compare(a.byte, b.byte) })
	slices.SortStableFunc(lv.overlay, func(a, b virtGroup) int { return cmp.Compare(a.byte, b.byte) })
	if v.merged == nil {
		v.merged = map[int]*lineVirtual{}
	}
	v.merged[n] = lv
	return lv
}
