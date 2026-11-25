package ui

import (
	"fmt"
	"strconv"
	"strings"
)

// 画面の部品の大きさ（仮）。
const (
	// signColumnWidth は、印の列の列数である。Vim の 'signcolumn' の列数（2）と同じである。
	signColumnWidth = 2
	// outputMaxRows は、出力の欄の中身の行数の上限である。Vim の :copen の既定の高さ（10 行）と同じである。
	outputMaxRows = 10
)

// signMark は、印の列に出す 1 つの印の色と優先順位である。
// 表記は、色の名前と同じ名前の記号（SymbolSign...。既定は defaultSymbols。設定の symbols で変えられる。FR-93）である。
type signMark struct {
	style string
	rank  int // 大きいほど優先する
}

// 印の表記と優先順位（基本設計 6.7）。
//
// 1 つの行に複数の印があれば、次の順に高いものを 1 つだけ出す。
//
//  1. executionPoint（デバッグ対象が止まった位置）
//  2. breakpoint
//  3. breakpointUnverified
//  4. 診断の印。error、warning、info、hint の順
//
// デバッガーの印を診断の印より上にするのは、デバッグ中に利用者が見る位置（止まった行と置いたブレークポイント）を、
// 編集中に出る診断で隠さないためである。止まった行のブレークポイントは "=>" に隠れるが、続行すると印が戻る。
// 既定の表記（=>、B、b、E、W、I、H）は ASCII にし、端末の文字幅（'ambiwidth'）で列がずれないようにする。
var (
	signMarks = map[SignKind]signMark{
		SignExecutionPoint:       {style: StyleSignExecutionPoint, rank: 7},
		SignBreakpoint:           {style: StyleSignBreakpoint, rank: 6},
		SignBreakpointUnverified: {style: StyleSignBreakpointUnverified, rank: 5},
	}
	diagnosticMarks = map[Severity]signMark{
		SeverityError:   {style: StyleSignDiagnosticError, rank: 4},
		SeverityWarning: {style: StyleSignDiagnosticWarning, rank: 3},
		SeverityInfo:    {style: StyleSignDiagnosticInfo, rank: 2},
		SeverityHint:    {style: StyleSignDiagnosticHint, rank: 1},
	}
)

// layout は、f に合わせて、印の列と行番号の列の幅と、出力の欄の行数を決める。Render の最初に呼ぶ。
//
// 印の列は、Vim の 'signcolumn' の既定（auto）と同じく、印か診断が 1 つでもあれば出し、無ければ出さない。
// 行番号の列（f.LineNumbers）は、印の列の右に置き、幅は描くたびに f.Text の行数から決める（LineNumbers.width）。
// 出力の欄は、画面の下（ステータス行とコマンドラインの行の間）に、欄の中身と欄の名前の行を置く。
// 中身の行数は outputMaxRows 行と、画面の行数からステータス行、欄の名前の行、コマンドラインの行の 3 行を除いた行数の半分
// （切り捨て）の小さい方にする。
// 画面が低く、バッファの表示領域と欄の中身を 1 行ずつも取れないときは、欄を出さない。
func (r *Renderer) layout(f *Frame) {
	r.gutter = max(r.GutterWidth, 0)
	if len(f.Signs) > 0 || len(f.Diagnostics) > 0 {
		r.gutter = max(r.gutter, signColumnWidth)
	}
	lineCount := 1
	if f.Text != nil {
		lineCount = f.Text.LineCount()
	}
	r.numWidth = f.LineNumbers.width(lineCount)
	r.wrap = f.Wrap
	r.outRows = 0
	if f.Output != nil {
		r.outRows = min(outputMaxRows, (r.Height-3)/2)
		if r.outRows < 1 {
			r.outRows = 0
		}
	}
}

// OutputRows は、直前の Render で出力の欄の中身に使った行数を返す。欄を出していなければ 0 である。
func (r *Renderer) OutputRows() int { return r.outRows }

// TextRows は、直前の Render でバッファの表示領域に使った行数を返す。
func (r *Renderer) TextRows() int { return r.textRows() }

// TextWidth は、直前の Render でバッファのテキストを描いた列数（本文の幅）を返す。まだ描いていなければ 0 である。
func (r *Renderer) TextWidth() int { return r.textWidth() }

// FloatRenderer は、直前の Render でポップアップ（Frame.Float）の窓の中を描いた子の Renderer を返す。窓が無ければ nil である。
// 窓の中のバッファのページ単位のスクロール（command.State.Viewport）を、窓の表示領域につなぐのに使う（internal/editor）。
func (r *Renderer) FloatRenderer() *Renderer { return r.float }

// ConsoleOutputRenderer は、直前の Render で書き足すテキストと入力の行の組（Float.Console）の出力を描いた子の Renderer を返す。
// 描いていなければ nil である。出力のバッファのページ単位のスクロールと、書き足しで古い行を捨てたときの見ている行のずらしを、
// 出力の表示領域につなぐのに使う（internal/editor。FR-274）。
func (r *Renderer) ConsoleOutputRenderer() *Renderer {
	if r.float == nil || !r.console {
		return nil
	}
	return r.floatList
}

// PreviewRenderer は、直前の Render でポップアップのプレビュー（Float.Preview）の中身を描いた子の Renderer を返す。描いていなければ nil である。
// プレビューにフォーカスを入れたときのページ単位のスクロールを、プレビューの表示領域につなぐのに使う（internal/editor）。
func (r *Renderer) PreviewRenderer() *Renderer { return r.floatPreview }

// signsByLine は、表示領域の行ごとに、優先順位の最も高い印を返す。印の無い行はゼロ値である。
// DAP のブレークポイントと止まった位置（基本設計 6.4）は、この印で見る。
//
// req: FR-31
func (d *drawer) signsByLine(rows int) []signMark {
	if d.r.gutter == 0 {
		return nil
	}
	out := make([]signMark, rows)
	top := d.r.top
	put := func(line int, m signMark) {
		if i := line - top; i >= 0 && i < rows && m.rank > out[i].rank {
			out[i] = m
		}
	}
	for _, s := range d.f.Signs {
		if m, ok := signMarks[s.Kind]; ok {
			put(s.Line, m)
		}
	}
	for _, dg := range d.f.Diagnostics {
		if m, ok := diagnosticMarks[dg.Severity]; ok {
			put(min(dg.Range.Start.Line, dg.Range.End.Line), m)
		}
	}
	return out
}

// drawSign は、表示領域の row 行目の印の列を描く。印の表記は記号の設定（Renderer.Symbols）で変えられる。
//
// req: FR-93
func (d *drawer) drawSign(row int, m signMark) {
	w := min(d.r.gutter, d.r.Width)
	for c := range w {
		d.g.set(row, c, blank(Attr{}))
	}
	if m.rank == 0 {
		return
	}
	// 1 列の印は、右の 1 列を印の色の空白で埋める。
	a := d.theme.attr(m.style)
	limit := min(signColumnWidth, w)
	for c := d.r.drawText(d.g, row, 0, limit, d.r.symbol(m.style), a); c < limit; c++ {
		d.g.set(row, c, blank(a))
	}
}

// drawGutter は、表示領域の row 行目（中身は vr）の、印の列と行番号の列を描く。signs は signsByLine の結果である。
// 印と番号は行の最初の見た目の行にだけ出し、折り返した続きの見た目の行（vr.k が 0 でない）は空ける（Vim と同じ。FR-156）。
func (d *drawer) drawGutter(row int, vr viewRow, signs []signMark) {
	if signs != nil {
		var m signMark
		if vr.k == 0 {
			m = signs[vr.line-d.r.top]
		}
		d.drawSign(row, m)
	}
	if d.r.numWidth > 0 {
		d.drawNumber(row, vr)
	}
}

// drawNumber は、表示領域の row 行目（中身は vr）に、バッファの vr.line 行目（0 から数える）の行番号を描く（FR-135）。
// 列は印の列の右の r.numWidth 列で、番号を最後の 1 列の手前で右に寄せ、最後の 1 列は空ける。
// 列の全体（右寄せの前の空白と最後の 1 列を含む）に StyleLineNr を付ける（Vim の LineNr と同じ）。画面の右の端で切る。
// 折り返した続きの見た目の行（vr.k が 0 でない）は、番号を書かずに StyleLineNr の空白で埋める（nvim 0.12.5 と同じ。FR-156）。
//
// req: FR-135, FR-156
func (d *drawer) drawNumber(row int, vr viewRow) {
	x0, w := d.r.signWidth(), d.r.numWidth
	a := d.theme.attr(StyleLineNr)
	s := ""
	if vr.k == 0 {
		s = strconv.Itoa(vr.line + 1)
	}
	pad := w - 1 - len(s)
	for i := range w {
		c := blank(a)
		if k := i - pad; k >= 0 && k < len(s) {
			c = Cell{Text: s[k : k+1], Width: 1, Attr: a}
		}
		if x := x0 + i; x < d.r.Width {
			d.g.set(row, x, c)
		}
	}
}

// outputRow は、出力の欄に出す 1 行の、Lines の行の番号とバイトの範囲 [start, end) である。
type outputRow struct {
	line, start, end int
	col              int // start の文字の、行の先頭からの表示の列
}

// wrapLine は、s を幅 width の行に折り返した、行ごとのバイトの範囲を返す（Vim の 'wrap'）。空の行は 1 行にする。
// 1 文字が幅より広ければ（幅 1 の欄の全角文字）、その文字だけで 1 行にする。
func wrapLine(line int, s string, width, tabStop int) []outputRow {
	rows := []outputRow{{line: line}}
	col, rowCol := 0, 0
	for off := 0; off < len(s); {
		gl := nextGlyph(s, off, col, tabStop)
		cur := &rows[len(rows)-1]
		if gl.col+gl.width-rowCol > width && off > cur.start {
			rows = append(rows, outputRow{line: line, start: off, end: off, col: gl.col})
			cur = &rows[len(rows)-1]
			rowCol = gl.col
		}
		col += gl.width
		off += gl.size
		cur.end = off
	}
	return rows
}

// OutputLineRows は、出力の欄の 1 行 s を、直前の Render の画面の幅で折り返したときの画面の行数を返す（1 以上）。
// 欄のスクロールの位置を画面の行で数えるために、internal/editor が使う。
func (r *Renderer) OutputLineRows(s string) int {
	return len(wrapLine(0, s, max(r.Width, 1), tabStopOr(r.tabStop)))
}

// outputRows は、出力の欄に出す行を、上から順に返す。
// Bottom の手前の行を折り返した画面の行のうち、下から Skip 行を除いて、その上の n 行を出す。
// 先頭の行まで集めても足りなければ（画面の幅が変わったときなど）、先頭の行から下へ欄が埋まるまで集める。
func (r *Renderer) outputRows(o *Output, n int) []outputRow {
	lines := o.Lines
	// 最後の行が空なら、改行で終わったテキストの、まだ何も書かれていない行なので出さない。
	if k := len(lines); k > 0 && lines[k-1] == "" {
		lines = lines[:k-1]
	}
	bottom := len(lines)
	if o.Bottom >= 0 && o.Bottom < bottom {
		bottom = o.Bottom
	}
	tabStop := tabStopOr(r.tabStop)
	skip := 0
	if o.Bottom >= 0 {
		skip = max(o.Skip, 0)
	}
	var rows []outputRow
	first := bottom
	for first > 0 && len(rows) < n+skip {
		first--
		rows = append(wrapLine(first, lines[first], r.Width, tabStop), rows...)
	}
	// 先頭の行まで集めても足りなければ、先頭の行から欄が埋まるところまでにする。
	skip = min(skip, max(len(rows)-n, 0))
	rows = rows[:len(rows)-skip]
	if len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	for next := bottom; len(rows) < n && next < len(lines); next++ {
		rows = append(rows, wrapLine(next, lines[next], r.Width, tabStop)...)
	}
	if len(rows) > n {
		rows = rows[:n]
	}
	return rows
}

// drawOutput は、出力の欄を、row0 行目から outRows 行と、その下の欄の名前の行に描く。
func (r *Renderer) drawOutput(g *Grid, theme Theme, o *Output, row0 int) {
	rows := r.outputRows(o, r.outRows)
	tabStop := tabStopOr(r.tabStop)
	special := theme.attr(StyleSpecialKey)
	for i, or := range rows {
		r.drawSegment(g, row0+i, o.Lines[or.line], or, special)
	}
	status := row0 + r.outRows
	a := theme.attr(StyleStatusLineNC)
	for c := range r.Width {
		g.set(status, c, blank(a))
	}
	total := len(o.Lines)
	if total > 0 && o.Lines[total-1] == "" {
		total--
	}
	last := 0
	if len(rows) > 0 {
		last = rows[len(rows)-1].line + 1
	}
	right := fmt.Sprintf("%d/%d ", last, total)
	start := max(r.Width-StringWidth(right, tabStop), 0)
	r.drawText(g, status, start, r.Width, right, a)
	r.drawText(g, status, 0, start-1, " [出力] "+strings.TrimSpace(o.Title), a)
}

// drawSegment は、折り返した 1 行（行 s の [or.start, or.end)）を、画面の row 行目の左の端から描く。
// タブの幅は、行の先頭からの列で決まるので、折り返した位置の列（or.col）から数え続ける。
// 制御文字（デバッグ対象の出力の端末のエスケープシーケンスなど）は、バッファと同じく ^[ の形で出し、端末へそのまま送らない。
func (r *Renderer) drawSegment(g *Grid, row int, s string, or outputRow, special Attr) {
	r.drawSegmentAt(g, row, 0, r.Width, s, or, Attr{}, special)
}

// drawSegmentAt は、折り返した 1 行を、画面の row 行目の x0 列から、x0+width 列の手前までに属性 base で描く。
// special は、^[ の形で出す文字の属性で、base の上に重ねる。
func (r *Renderer) drawSegmentAt(g *Grid, row, x0, width int, s string, or outputRow, base, special Attr) {
	tabStop := tabStopOr(r.tabStop)
	col := or.col
	put := func(x int, c Cell) {
		if x >= 0 && x < width {
			g.set(row, x0+x, c)
		}
	}
	for off := or.start; off < or.end; {
		gl := nextGlyph(s, off, col, tabStop)
		x := gl.col - or.col
		switch gl.kind {
		case glyphTab, glyphWhitespace:
			// 出力の欄の行は nextGlyph で並べるので glyphWhitespace にはならない（空白の記号はバッファの表示領域にだけ描く）。
			for i := range gl.width {
				put(x+i, Cell{Text: gl.cell(i), Width: 1, Attr: base})
			}
		case glyphSpecial:
			for i := range len(gl.text) {
				put(x+i, Cell{Text: gl.text[i : i+1], Width: 1, Attr: base.Over(special)})
			}
		case glyphText:
			if gl.width == 2 && x+1 >= width {
				put(x, Cell{Text: ">", Width: 1, Attr: base})
				break
			}
			put(x, Cell{Text: gl.text, Width: gl.width, Attr: base})
			if gl.width == 2 {
				put(x+1, Cell{Width: 0, Attr: base})
			}
		}
		col += gl.width
		off += gl.size
	}
}
