package ui

import "sort"

// Scrollbar は、バッファの表示領域の右端の 1 列に描くスクロールバーである（Frame.Scrollbar。FR-186、FR-187）。
// つまみ（見えている行の範囲の位置と大きさ）と、行の印（診断と検索の一致の位置など）を描く。
// 出すかどうか（設定、出す client、ファイルの種類）は internal/editor が決め、出すときだけ詰める。
// 描画は、表示領域に収まらない（スクロールできる）ときだけ描く（drawScrollbar）。
type Scrollbar struct {
	// Marks は、印の種類ごとの、印を出す行の一覧である。種類の数と中身は internal/editor が決め、
	// 描画は種類の名前で分岐せず、ScrollbarMarks の優先と色と記号の名前だけで描く。
	Marks []ScrollbarMarks
}

// ScrollbarMarks は、スクロールバーに出す 1 つの種類の印である（FR-187）。
type ScrollbarMarks struct {
	// Lines は、印を出す行（0 から数える）の一覧である。小さい順に並べる。同じ行が続いてもよい。
	// 描画は表示領域の行ごとに二分探索で引くので、一覧の長さによらず、描く量は表示領域の行数と種類の数に比例する。
	Lines []int
	// Priority は、同じセルに複数の種類の印が重なるときの優先である。大きいほうを出す。同じなら Marks の前のものを出す。
	Priority int
	// Style は、印の色の名前である。つまみの上の印は、つまみの色（StyleScrollbar）の上に重ねる。
	Style string
	// Symbol は、印の記号の名前（設定の symbols に書ける名前）である。表示幅が 1 でない記号（空を含む）なら、この種類の印を描かない。
	Symbol string
}

// scrollbarMinTextWidth は、スクロールバーを描く本文の幅の下限である。本文の 1 列しかない画面では、その列を隠さないよう描かない。
const scrollbarMinTextWidth = 2

// drawScrollbar は、スクロールバー sb を、バッファの表示領域 area の右端の 1 列（本文の最後の列に重ねる）に描く（FR-186、FR-187）。
// 重ねる順は layers.go の「窓の重なり」の表にある。text は描いているバッファで、表示の範囲は直前の buildView の値（top、bot、skip、view）を使う。
//
//   - 表示領域にバッファの全体が収まる（最初の行が先頭で、最後の行まで描き、途中から描いた行も描ききれなかった行も無い）ときは描かない
//   - 行は、見た目の行でなく本文の行で数える。行 l は表示領域の floor(l×行数÷バッファの行数) 行目に当てる
//   - つまみは、最初の行（top）を当てた行から、最後まで見えている行の次の行（bot。Vim の w_botline）を当てた行の手前（端数は切り上げ）
//     までで、少なくとも 1 行にする。最後の行まで見えていれば、表示領域の最後の行まで伸ばす
//   - つまみのセルは、下に描いた文字と色を残し、背景の色などを StyleScrollbar で上書きする（端末のセルでは透かせないので、
//     nvim-scrollview の winblend の代わりに、下の文字を残す）。全角文字の右半分に当たるときは、その文字を空白に替える
//   - 印は、行の範囲に印を出す行がある種類のうち、Priority の最も大きいものを 1 つだけ出す。印のセルの文字は記号に替え、
//     色は、つまみの上なら StyleScrollbar の上に Style を、ほかは Style だけを使う
//   - 本文の幅が scrollbarMinTextWidth より狭いときは描かない
//   - 描いた列の場所と、行ごとのセルに割り付けたもの（scrollbarCell）を hitState に残す。HitTest は、つまみと印のセルだけを
//     HitScrollbar として解き、ほかのセルは本文として解く（FR-189）
//
// req: FR-186, FR-187
func (r *Renderer) drawScrollbar(g *Grid, theme Theme, sb *Scrollbar, text Text, area textArea) {
	if sb == nil || area.rows <= 0 || r.textWidth() < scrollbarMinTextWidth || !r.overflows(text, area.rows) {
		return
	}
	n, h := text.LineCount(), area.rows
	start, end := r.scrollbarThumb(n, h)
	if p := r.hits.pin; p.ok && p.top == r.top && p.lines == n && p.rows == h {
		// つまみのドラッグの間（HoldScrollbarThumb）は、つまみの最初の行をマウスの行に置く。長さは変えず、
		// 最後の行まで見えていれば、今までどおり表示領域の最後の行まで伸ばす。
		if end < h {
			end = end - start + p.row
		}
		start = p.row
		end = min(max(end, start+1), h)
	} else {
		r.hits.pin.ok = false
	}
	thumb := theme.attr(StyleScrollbar)
	x := area.width - 1
	// マウスの位置を解く HitTest が、描いたセル（つまみと印）を引く（FR-189）。
	r.hits.bar, r.hits.barOK = rect{top: area.top, left: x, h: h, w: 1}, true
	r.hits.barThumb, r.hits.barLines = start, n
	r.hits.barCells = r.hits.barCells[:0]
	for i := range h {
		cell := r.scrollbarCellAt(sb, i, n, h, start, end)
		r.hits.barCells = append(r.hits.barCells, cell)
		if cell.mark < 0 && !cell.thumb {
			continue
		}
		row := area.top + i
		c := g.At(row, x)
		if c.Width == 0 {
			// 全角文字の右半分。左半分ごと空白にする（浮かべた窓の端と同じ）。
			clearSplit(g, row, x, x+1)
			c = blank(c.Attr)
		}
		switch {
		case cell.mark >= 0:
			m := sb.Marks[cell.mark]
			var base Attr
			if cell.thumb {
				base = thumb
			}
			c = Cell{Text: r.symbol(m.Symbol), Width: 1, Attr: base.Over(theme.attr(m.Style))}
		default:
			c.Attr = c.Attr.Over(thumb)
		}
		g.set(row, x, c)
	}
}

// scrollbarCell は、スクロールバーの 1 行のセルに割り付けたものと描いたものである。drawScrollbar が行ごとに決めて hitState に残し、
// HitTest がそのまま返す。描くものと押したものの割り付けと印の選び方を、ここ（scrollbarCellAt）の 1 か所で決める。
type scrollbarCell struct {
	// lo と hi は、このセルに割り付けた本文の行の範囲 [lo, hi) である。
	lo, hi int
	// thumb は、つまみのセルかである。
	thumb bool
	// mark は、このセルに描いた印の種類の、Scrollbar.Marks の添字である。印が無ければ -1 である。
	mark int
	// lines は、mark の種類の行（ScrollbarMarks.Lines）のうち、[lo, hi) に入るものである（Lines の部分の列）。
	lines []int
}

// scrollbarCellAt は、表示領域の i 行目（h 行のうち。バッファは n 行。つまみは [start, end) 行）のセルに割り付けたものと、出す印を返す。
// i 行目に当たる行は、floor(l×h÷n) が i になる行 l で、[ceil(i×n÷h), ceil((i+1)×n÷h)) である。
// 印は、その範囲に行がある種類のうち、Priority の最も大きいもの（同じなら Marks の前のもの）で、記号の表示幅が 1 でない種類は選ばない。
func (r *Renderer) scrollbarCellAt(sb *Scrollbar, i, n, h, start, end int) scrollbarCell {
	n = max(n, 1)
	c := scrollbarCell{lo: min((i*n+h-1)/h, n), hi: min(((i+1)*n+h-1)/h, n), thumb: i >= start && i < end, mark: -1}
	for idx, k := range sb.Marks {
		if c.mark >= 0 && k.Priority <= sb.Marks[c.mark].Priority {
			continue
		}
		if StringWidth(r.symbol(k.Symbol), DefaultTabStop) != 1 {
			continue
		}
		j := sort.SearchInts(k.Lines, c.lo)
		e := j + sort.SearchInts(k.Lines[j:], c.hi)
		if e > j {
			c.mark, c.lines = idx, k.Lines[j:e]
		}
	}
	return c
}

// ScrollbarMarkLines は、直前の Render が描いたスクロールバーの i 行目（表示領域の行。Hit.Row）のセルに描いた印の種類の行
// （ScrollbarMarks.Lines のうち、そのセルに割り付けた範囲 [Hit.Line, Hit.LineEnd) に入るもの）を、小さい順に返す。
// 印を押してその行へ移る操作（FR-211）が、描いた印と同じ選び方の行を引く。印が無いセル、描いていない行、描いていないときは nil である。
// 返す一覧は Frame.Scrollbar の一覧の部分の列なので、書き換えない。
func (r *Renderer) ScrollbarMarkLines(i int) []int {
	if !r.hits.barOK || i < 0 || i >= len(r.hits.barCells) {
		return nil
	}
	c := r.hits.barCells[i]
	if c.mark < 0 {
		return nil
	}
	return c.lines
}

// ScrollbarTop は、直前の Render が描いたスクロールバーのつまみの最初の行を、表示領域の i 行目（0 から数える）へ動かすときの、
// 表示領域の中へ寄せた行 row と、表示領域の最初の行 line（0 から数える）を返す（つまみのドラッグ。FR-210）。
// スクロールバーを描いていなければ ok は false である。
//
// i は表示領域の行の範囲 [0, h) へ寄せ、行 round(i×(n−1)÷(h−1)) を返す（h は表示領域の行数、n はバッファの行数）。
// 0 行目は最初の行、最後の行は最後の行（表示領域にはその行と "~" の行だけが見える）になる。
// nvim-scrollview（コミット ce007c654e6af5f9dc5a8c92ea300e623fface1a）の simple の数え方の対応表（lua/scrollview.lua の
// simple_topline_lookup）と同じである。本体のつまみの数え方（scrollbarThumb の floor(最初の行×h÷n)）で描き直すと、
// つまみの最初の行は i 行目からずれることがある（h が 40、n が 201、i が 1 なら最初の行は 5 で、つまみは 0 行目に描く）。
// ドラッグの間は HoldScrollbarThumb で、つまみを row 行目に描かせる。
//
// req: FR-210
func (r *Renderer) ScrollbarTop(i int) (row, line int, ok bool) {
	h, n := len(r.hits.barCells), max(r.hits.barLines, 1)
	if !r.hits.barOK || h <= 0 {
		return 0, 0, false
	}
	row = min(max(i, 0), h-1)
	if h == 1 {
		return 0, 0, true
	}
	// 四捨五入する（nvim-scrollview の round と同じ）。
	return row, (2*row*(n-1) + (h - 1)) / (2 * (h - 1)), true
}

// HoldScrollbarThumb は、次からの Render が、表示領域の最初の行が line の間（バッファの行の数と表示領域の行数も変わらない間）、
// スクロールバーのつまみの最初の行を row 行目に描くようにする（つまみのドラッグの間。FR-210）。nvim-scrollview も、
// ドラッグの間はつまみの窓をマウスの行へ直接動かす（lua/scrollview.lua の move_scrollbar）。つまみの長さは変えない。
// 最初の行が変われば（ほかの操作で送ったとき）、元の数え方に戻る。ScrollbarTop の数え方で描き直すとつまみがマウスの行から
// ずれることがあり、ドラッグの間につまみとマウスが離れて見えないようにするためである。
//
// req: FR-210
func (r *Renderer) HoldScrollbarThumb(row, line int) {
	r.hits.pin = scrollbarPin{ok: true, row: row, top: line, lines: r.hits.barLines, rows: len(r.hits.barCells)}
}

// scrollbarPin は、HoldScrollbarThumb が決めたつまみの位置である。top は最初の行、lines と rows は決めたときのバッファの行の数と
// 表示領域の行数で、どれかが今の描画と違えば使わない。
type scrollbarPin struct {
	ok                    bool
	row, top, lines, rows int
}

// overflows は、直前の buildView で、バッファの全体が表示領域（rows 行）に収まらなかったかを返す。
// 最初の行が先頭でない、最初の行を途中の見た目の行から描いた、最後まで描いた行の次の行（bot）が最後の行より前、
// 表示領域の最後の行に描いた行を描ききれなかった、のどれかなら収まらない。
func (r *Renderer) overflows(text Text, rows int) bool {
	n := text.LineCount()
	if r.top > 0 || r.skip > 0 || r.bot < n {
		return true
	}
	// bot は最初の行を描ききれなくても最初の行の次の行になるので、最後の行の見た目の行を描ききったかも見る。
	if len(r.view) >= rows && rows > 0 {
		last := r.view[rows-1]
		return last.line >= 0 && last.line < n && last.k+1 < r.height(text, last.line)
	}
	return false
}

// scrollbarThumb は、行数 n のバッファを h 行の表示領域に当てたときの、つまみの行の範囲 [start, end) を返す（drawScrollbar の doc）。
func (r *Renderer) scrollbarThumb(n, h int) (start, end int) {
	n = max(n, 1)
	start = min(r.top*h/n, h-1)
	end = (r.bot*h + n - 1) / n
	if r.bot >= n {
		end = h
	}
	end = min(max(end, start+1), h)
	return start, end
}
