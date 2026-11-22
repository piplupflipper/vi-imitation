package ui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// 仮想テキスト（FR-231〜FR-234。基本設計 6.1 の「仮想テキスト」）。
//
// 拡張機能が virtualText/set で送った、本文に無い文字列を描く。置き方は Neovim 0.12.5 の nvim_buf_set_extmark の virt_text_pos に合わせた
// 3 つで、2026-11-22 に nvim --clean を tmux の 30 列の画面で動かし、capture-pane で比べて決めた。
//
//   - inline: 位置の文字の前に差し込み、後ろの本文を右へずらす。行の文字と同じく並べる（lineWalk）ので、折り返し（FR-156）では
//     本文と一緒に折り返し、横のスクロールでは一緒に送り、タブ文字の幅は仮想テキストの列も数えて決める。
//     カーソルは仮想テキストの上に置かない。ノーマルモードとビジュアルモードで位置の文字の上にいるカーソルは仮想テキストの後ろ（文字の上）、
//     インサートモードのカーソルと行末のカーソルは仮想テキストの前に置く。望みの列とルーラーの列は、どちらもその置いた列で数える
//     （Columns、WrapLines）。列から文字を引くとき（j と k の望みの列、マウスの位置）は、仮想テキストの列を後ろの文字の列として引く。
//     行末の位置の仮想テキストの列は、行の最後の文字の列として引く
//   - eol: 行末のセル（行の区切りを表すセル。空白の記号を描くときは行末の記号）の次の列から描く。同じ行に複数あれば、位置のバイトの順
//     （同じなら一覧の順）に 1 列ずつ空けて並べる。行を折り返すときも見た目の行を増やさず、右の端で切る。
//     横にスクロールして行が画面の左の端より短いときは、行末のセルと同じく画面の 0 列目の次から描く（Neovim と同じ）
//   - overlay: 位置の文字の最初のセルから、本文を右へずらさずに上に重ねて描く。同じ位置の inline の後ろの文字に重ねる。行末より右にも
//     はみ出して描き、見た目の行の右の端で切る（次の見た目の行へは続けない）。横にスクロールして画面の左の端より左に出た部分は描かない。
//     Neovim 0.12.5 は左の端より左の overlay を左の端に寄せて描くが、拡張機能は左の端の位置を知らないので、本文と一緒に隠す方にした
//
// 仮想テキストのセルには、本文の構文の色、見比べの色、診断、検索の一致、範囲の色、選択の色を重ねず、拡張機能が付けた色だけで描く
// （Neovim の hl_mode の既定の "replace"。2026-11-22 に、選択の中の inline の仮想テキストが選択の色にならないことを確かめた）。
// 窓の重なり（layers.go）では、バッファの行（順 1）の一部として描く。overlay と eol は、行の文字と見比べの色を描いた後に重ねる。

// VirtualPlacement は、仮想テキストの置き方である（ext.VirtualTextPlacement）。
type VirtualPlacement int

// 仮想テキストの置き方。
const (
	// VirtualInline は、位置の文字の前に差し込み、後ろの本文を右へずらす。
	VirtualInline VirtualPlacement = iota
	// VirtualEOL は、行末のセルの次の列から描く。
	VirtualEOL
	// VirtualOverlay は、位置の文字の最初のセルから、本文の上に重ねて描く。
	VirtualOverlay
)

// VirtualChunk は、仮想テキストの、同じ色で描く 1 つの文字列である。
type VirtualChunk struct {
	// Text は、描く文字列である。改行、復帰、タブ文字は空白 1 つとして描く（NewVirtualTexts）。
	Text string
	// Style は、色の種類の名前である。Theme に無い名前なら名前の色を付けない。
	Style string
	// Attr は、名前の色の上に直に重ねる色と飾りである。
	Attr Attr
}

// VirtualText は、バッファの 1 つの位置に出す仮想テキストである。
type VirtualText struct {
	// Position は、描く時点のテキストの位置である。Byte が文字の途中なら、その文字の位置として扱い、行の長さを超えれば行末の位置にする。
	// 編集に合わせて位置をずらすのは internal/editor の役目である（Highlight と同じ）。
	Position  buffer.Position
	Placement VirtualPlacement
	Chunks    []VirtualChunk
}

// VirtualTexts は、仮想テキストの一覧を、行で引けるように分けたものである。NewVirtualTexts で作る。
// 作った後は変えないので、同じ値を何度の描画と列の数え方に渡してよい。nil は仮想テキストが無いことを表す。
//
// LineLayout.Virtual に入れて渡し、描画、望みの列、折り返しの数え方が同じ値を読む。比べられるポインターなので、
// 表示の位置を決め直すかの判定（topKey）は、一覧を作り直したときにだけ決め直す。
type VirtualTexts struct {
	lines  map[int]*lineVirtual
	styles []decoStyle
}

// lineVirtual は、1 行の仮想テキストである。
type lineVirtual struct {
	// inline は、inline の文字列を、位置のバイトの順（同じなら一覧の順）に並べたものである。
	inline []virtPiece
	// eol と overlay は、eol と overlay の仮想テキストを、位置のバイトの順（同じなら一覧の順）に並べたものである。
	eol, overlay []virtGroup
}

// virtPiece は、仮想テキストの 1 つの文字列（VirtualChunk）である。
type virtPiece struct {
	byte  int    // 位置のバイト（0 以上）
	text  string // 描く文字列。空でない
	style int    // VirtualTexts.styles の添字
}

// virtGroup は、eol か overlay の 1 つの仮想テキストの文字列の並びである。
type virtGroup struct {
	byte   int
	pieces []virtPiece
}

// NewVirtualTexts は、vts を行ごとに分けた VirtualTexts を返す。lines は描くテキストの行数で、その外の行の仮想テキストは捨てる。
// 文字列の空な VirtualChunk は捨て、どの文字列も空な仮想テキストも捨てる。一覧が空になれば nil を返す。
//
// req: FR-231
func NewVirtualTexts(vts []VirtualText, lines int) *VirtualTexts {
	order := make([]int, 0, len(vts))
	for i, vt := range vts {
		if vt.Position.Line >= 0 && vt.Position.Line < lines {
			order = append(order, i)
		}
	}
	slices.SortStableFunc(order, func(a, b int) int {
		pa, pb := vts[a].Position, vts[b].Position
		return cmp.Or(cmp.Compare(pa.Line, pb.Line), cmp.Compare(max(pa.Byte, 0), max(pb.Byte, 0)))
	})
	v := &VirtualTexts{lines: map[int]*lineVirtual{}}
	var st styleTable
	for _, i := range order {
		vt := vts[i]
		b := max(vt.Position.Byte, 0)
		var ps []virtPiece
		for _, c := range vt.Chunks {
			if t := virtualTextOf(c.Text); t != "" {
				ds := decoStyle{name: c.Style}
				if c.Attr != (Attr{}) {
					a := c.Attr
					ds.attr = &a
				}
				ps = append(ps, virtPiece{byte: b, text: t, style: st.index(ds)})
			}
		}
		if len(ps) == 0 {
			continue
		}
		lv := v.lines[vt.Position.Line]
		if lv == nil {
			lv = &lineVirtual{}
			v.lines[vt.Position.Line] = lv
		}
		switch vt.Placement {
		case VirtualInline:
			lv.inline = append(lv.inline, ps...)
		case VirtualEOL:
			lv.eol = append(lv.eol, virtGroup{byte: b, pieces: ps})
		case VirtualOverlay:
			lv.overlay = append(lv.overlay, virtGroup{byte: b, pieces: ps})
		}
	}
	if len(v.lines) == 0 {
		return nil
	}
	v.styles = st.names
	return v
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

// line は、n 行目の仮想テキストを返す。無ければ nil を返す。v が nil でもよい。
func (v *VirtualTexts) line(n int) *lineVirtual {
	if v == nil {
		return nil
	}
	return v.lines[n]
}

// attr は、文字列の色（名前の色の上に直に重ねる色を重ねたもの）を返す。style は virtPiece.style である。
//
// req: FR-234
func (v *VirtualTexts) attr(theme Theme, style int) Attr {
	return v.styles[style].over(theme)
}

// lineWalk は、1 行の文字と inline の仮想テキストの文字を、画面に並べる順に 1 つずつ返す走査である（lineRule.walk で作る）。
// 本文、カーソルの位置、折り返し、望みの列、検索の一致を集める範囲は、どれもこの走査で文字を並べるので、仮想テキストの列を同じに数える。
//
// 仮想テキストの文字は、glyph.virt が 0 でなく（virt-1 が VirtualTexts.styles の添字）、size が 0 で、off が後ろの本文の文字の位置
// （行末の位置の仮想テキストなら行の長さ）の glyph として返す。位置が文字の途中を指す仮想テキストは、その文字の前に返す。
type lineWalk struct {
	m   *lineRule
	s   string
	tab int // タブ文字の幅（0 より大きい）
	inl []virtPiece
	vi  int // 次に返す inline の文字列
	vo  int // inl[vi] の文字列の中の、次に返す文字のバイト
	off int // 次に返す本文の文字のバイト
}

// walk は、n 行目（テキスト s）を並べる走査を返す。
func (m *lineRule) walk(n int, s string) lineWalk {
	w := lineWalk{m: m, s: s, tab: tabStopOr(m.tabStop)}
	if lv := m.virt.line(n); lv != nil {
		w.inl = lv.inline
	}
	return w
}

// more は、まだ返していない文字があるかを返す。
func (w *lineWalk) more() bool { return w.off < len(w.s) || w.vi < len(w.inl) }

// next は、次の文字を列 col に置いた配置を返す。more が true のときだけ呼ぶ。
// 仮想テキストの無い行（ほとんどの行）では nextGlyph を直に呼ぶ短い関数としてインライン展開されるようにし、
// 仮想テキストのある行の処理は nextMixed に、空白の記号を描くときのタブ文字は lineRule.listTab に分ける。
// 長い行を数えるとき（数十万文字の行の折り返し）に、1 文字あたりの関数の呼び出しを増やさないためである（PR #310 のレビューの推奨 2 と同じ）。
func (w *lineWalk) next(col int) glyph {
	if w.vi < len(w.inl) {
		return w.nextMixed(col)
	}
	// nextGlyph の速い道（表示できる ASCII の文字で、後に結合文字が続かない）を、呼ばずにここで返す。
	if off := w.off; w.s[off] >= 0x20 && w.s[off] < 0x7f && (off+1 == len(w.s) || w.s[off+1] < 0x80) {
		w.off++
		return glyph{off: off, size: 1, col: col, width: 1, kind: glyphText, text: w.s[off : off+1]}
	}
	g := nextGlyph(w.s, w.off, col, w.tab)
	w.off += g.size
	if g.kind == glyphTab && w.m.list {
		w.m.listTab(&g)
	}
	return g
}

// nextMixed は、まだ返していない inline の仮想テキストのある行の next である。
func (w *lineWalk) nextMixed(col int) glyph {
	b := w.inl[w.vi].byte
	if w.off >= len(w.s) || b <= w.off {
		return w.virtual(col)
	}
	if g := w.m.glyph(w.s, w.off, col); b >= g.off+g.size {
		w.off += g.size
		return g
	}
	return w.virtual(col)
}

// virtual は、次の inline の仮想テキストの文字を、列 col に置いた配置を返す。
func (w *lineWalk) virtual(col int) glyph {
	p := w.inl[w.vi]
	g := nextGlyph(p.text, w.vo, col, tabStopOr(w.m.tabStop))
	w.vo += g.size
	if w.vo >= len(p.text) {
		w.vi++
		w.vo = 0
	}
	g.off, g.size, g.virt = w.off, 0, p.style+1
	return g
}

// virtualWidth は、ps の文字列を並べた列数を返す。
func virtualWidth(ps []virtPiece) int {
	n := 0
	for _, p := range ps {
		n += StringWidth(p.text, DefaultTabStop)
	}
	return n
}

// drawVirtual は、ps の文字列を、表示領域の row 行目（グリッドの行）の本文の x 列目（本文の左の端を 0 とする）から並べ、
// 本文の 0 列目から limit 列目の手前に掛かるセルだけを描いて、並べ終えた次の列を返す（eol と overlay の仮想テキスト）。
// 収まらない全角文字は描かない。描いたセルに半分だけ重なった本文の全角文字は、残りの半分を空白にする。
func (d *drawer) drawVirtual(row, x, limit int, ps []virtPiece) int {
	x0 := d.r.textLeft()
	if end := min(x+virtualWidth(ps), limit); end > max(x, 0) {
		clearSplit(d.g, row, x0+max(x, 0), x0+end)
	}
	for _, p := range ps {
		a := d.r.rule.virt.attr(d.theme, p.style)
		for off := 0; off < len(p.text) && x < limit; {
			gl := nextGlyph(p.text, off, 0, DefaultTabStop)
			off += gl.size
			switch {
			case gl.kind != glyphText:
				for i := range gl.width {
					if c := x + i; c >= 0 && c < limit {
						d.g.set(row, x0+c, Cell{Text: gl.cell(i), Width: 1, Attr: a})
					}
				}
			case x < 0 || x+gl.width > limit:
			case gl.width == 2:
				d.g.set(row, x0+x, Cell{Text: gl.text, Width: 2, Attr: a})
				d.g.set(row, x0+x+1, Cell{Width: 0, Attr: a})
			default:
				d.g.set(row, x0+x, Cell{Text: gl.text, Width: 1, Attr: a})
			}
			x += gl.width
		}
	}
	return x
}

// drawEOLVirtual は、lv の eol の仮想テキストを、表示領域の row 行目の本文の x 列目から、1 列ずつ空けて並べ、limit 列目の手前で切る。
//
// req: FR-231, FR-233
func (d *drawer) drawEOLVirtual(row, x, limit int, lv *lineVirtual) {
	for i, g := range lv.eol {
		if i > 0 {
			x++
		}
		if x >= limit {
			return
		}
		x = d.drawVirtual(row, x, limit, g.pieces)
	}
}

// virtAnchor は、overlay の仮想テキストを重ねる所である。描画の走査（drawLine、drawWrapped）が、位置の文字を置いたときに記録する。
type virtAnchor struct {
	group int     // lineVirtual.overlay の添字
	col   int     // 折り返さないときの、位置の文字の最初のセルの表示の列
	pos   wrapPos // 折り返すときの、位置の文字の最初のセルの位置
}

// overlayAnchors は、lv の overlay のうち、本文の文字 gl（仮想テキストでない）の位置に重ねるものの所を dst に足し、次に見る添字を返す。
// 位置が gl の範囲に入る（文字の途中を指すものを含む）ものと、gl より前の位置のものを、gl に重ねる。
func overlayAnchors(dst []virtAnchor, lv *lineVirtual, oi int, gl glyph, col int, pos wrapPos) ([]virtAnchor, int) {
	if lv == nil {
		return dst, oi
	}
	for oi < len(lv.overlay) && lv.overlay[oi].byte < gl.off+gl.size {
		dst = append(dst, virtAnchor{group: oi, col: col, pos: pos})
		oi++
	}
	return dst, oi
}
