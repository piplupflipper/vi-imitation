package ui

import "slices"

// Tab は、タブの行に出すタブ 1 つである（FR-62。基本設計 4.4 の段階 3 のタブとバッファ）。
type Tab struct {
	// Name は、タブに出すバッファの名前である。空なら "[No Name]" と出す。
	Name string
	// Modified は、バッファに保存していない変更があるかどうかである。true なら名前の後に未保存の印（記号 SymbolModified）を出す。
	Modified bool
	// ReadOnly は、バッファの編集を禁止しているかどうかである。true なら名前の後に編集の禁止の印（記号 SymbolReadOnly）を出す（FR-87）。
	ReadOnly bool
	// Current は、カーソルのあるタブかどうかである。StyleTabLineSelected の色で描く。
	Current bool
}

// TabLineMode は、タブの行を出す場合を決める（設定の tabline の show。基本設計 7.1）。
type TabLineMode int

const (
	// TabLineAlways は、タブが 1 つでもタブの行を出す（Vim の 'showtabline' の 2。VS Code に寄せた既定。#215 のオーナーの裁定）。
	TabLineAlways TabLineMode = iota
	// TabLineMultiple は、タブが 2 つ以上あるときだけタブの行を出す（Vim の 'showtabline' の既定の 1）。
	TabLineMultiple
	// TabLineNever は、タブの行を出さない（Vim の 'showtabline' の 0）。設定からは選べず、テストが Vim の画面と比べるときに使う。
	TabLineNever
)

// tabLineShown は、f のタブの行を出すかを返す。画面が低く、バッファの表示領域、ステータス行、コマンドラインの行を
// 1 行ずつも取れないときは出さない。
func (r *Renderer) tabLineShown(f *Frame) bool {
	if r.Height < 4 || len(f.Tabs) == 0 {
		return false
	}
	switch r.TabLine {
	case TabLineAlways:
		return true
	case TabLineNever:
		return false
	case TabLineMultiple:
	}
	return len(f.Tabs) >= 2
}

// tabLabel は、タブ t の見出しの文字列を返す。名前の前後に空白を 1 つずつ置き、未保存と編集の禁止の印を名前の後に続ける
// （ステータス行の " name [+] [RO]" と同じ並び）。
func (r *Renderer) tabLabel(t Tab) string {
	name := t.Name
	if name == "" {
		name = "[No Name]"
	}
	s := " " + name
	if t.Modified {
		s += " " + r.symbol(SymbolModified)
	}
	if t.ReadOnly {
		s += " " + r.symbol(SymbolReadOnly)
	}
	return s + " "
}

// TabLinePartTabs は、タブの行の部品のうち、タブの見出しの並び（Frame.Tabs）である（設定の tabline の left と right に書ける ID。FR-62、FR-92）。
// ステータス行には並べられない。
const TabLinePartTabs = "tabs"

// DefaultTabLineParts は、タブの行の既定の並びを返す。左にタブの見出しの並びだけを置く（#215 のタブの行と同じ見た目）。
func DefaultTabLineParts() StatusLine {
	return StatusLine{Left: []string{TabLinePartTabs}}
}

// IsTabLinePart は、id が、タブの行に並べられる本体の部品の ID かを返す。
// タブの見出しの並び（TabLinePartTabs）と、ステータス行の本体の部品（IsStatusPart）である。
func IsTabLinePart(id string) bool {
	return id == TabLinePartTabs || IsStatusPart(id)
}

// tabLineSegs は、タブの行の部品 ids を、描く順の文字列と属性の列にする（statusSegs）。
// タブの見出しの並び（TabLinePartTabs）は、文字列の無い印の要素（id だけ）にし、幅は drawTabLine が決める。
func (r *Renderer) tabLineSegs(ids []string, theme Theme, base Attr, text Text, f *Frame, cur cursorPlace) []statusSeg {
	var segs []statusSeg
	from := 0
	for i, id := range ids {
		if id != TabLinePartTabs {
			continue
		}
		segs = append(segs, r.statusSegs(ids[from:i], theme, base, text, f, cur)...)
		segs = append(segs, statusSeg{id: TabLinePartTabs})
		from = i + 1
	}
	return append(segs, r.statusSegs(ids[from:], theme, base, text, f, cur)...)
}

// drawTabLine は、画面の一番上の行にタブの行を描く（FR-62）。
//
// 部品の並びは Renderer.TabLineParts（nil なら DefaultTabLineParts）で決める（FR-92）。
// 左の部品を左の端から、右の部品を右の端に寄せて描き、残りの列を StyleTabLineFill で描く。
// タブの見出しの並び（TabLinePartTabs）は、他の部品を描いた残りの幅を使う。2 つ以上並べたら、左、右の順で最初のものだけを出す。
// 幅が足りなければ、ステータス行と同じく右の部品を優先し、左の部品を右の端で切る。
// ステータス行の部品の色は、StyleTabLineFill の色の上に重ねる。
//
// タブの見出しは drawTabs が描く。
//
// req: FR-62, FR-87, FR-92
func (r *Renderer) drawTabLine(g *Grid, theme Theme, f *Frame) {
	fill := theme.attr(StyleTabLineFill)
	for c := range r.Width {
		g.set(0, c, blank(fill))
	}
	layout := DefaultTabLineParts()
	if r.TabLineParts != nil {
		layout = *r.TabLineParts
	}
	text := f.Text
	if text == nil {
		text = emptyText{}
	}
	cur := r.placeCursor(text, f)
	// タブの見出しの並びは、左、右の順で最初のものだけを残す。
	seen := false
	keepFirstTabs := func(segs []statusSeg) []statusSeg {
		return slices.DeleteFunc(segs, func(s statusSeg) bool {
			if s.id != TabLinePartTabs {
				return false
			}
			drop := seen
			seen = true
			return drop
		})
	}
	left := keepFirstTabs(r.tabLineSegs(layout.Left, theme, fill, text, f, cur))
	right := keepFirstTabs(r.tabLineSegs(layout.Right, theme, fill, text, f, cur))
	fixed := 0
	for _, s := range slices.Concat(left, right) {
		fixed += StringWidth(s.text, r.tabStop)
	}
	// ステータス行と同じく、本体の部品より先に拡張機能の部品を切る。タブの見出しを並べたら、いまのタブの見出しの幅を残す。
	reserve := 0
	if slices.ContainsFunc(slices.Concat(left, right), func(s statusSeg) bool { return s.id == TabLinePartTabs }) {
		for _, t := range f.Tabs {
			if t.Current {
				reserve = StringWidth(r.tabLabel(t), r.tabStop)
			}
		}
	}
	if fixed+reserve > r.Width {
		trimmed := r.trimExtSegs(fixed+reserve-r.Width, left, right)
		left, right = trimmed[0], trimmed[1]
		fixed = 0
		for _, s := range slices.Concat(left, right) {
			fixed += StringWidth(s.text, r.tabStop)
		}
	}
	tabsWidth := max(r.Width-fixed, 0)
	segWidth := func(s statusSeg) int {
		if s.id == TabLinePartTabs {
			return tabsWidth
		}
		return StringWidth(s.text, r.tabStop)
	}
	draw := func(col, limit int, segs []statusSeg) {
		for _, s := range segs {
			if col >= limit {
				return
			}
			if s.id == TabLinePartTabs {
				r.drawTabs(g, theme, f.Tabs, col, min(col+tabsWidth, limit))
			} else {
				r.drawSegs(g, 0, col, limit, []statusSeg{s})
			}
			col += segWidth(s)
		}
	}
	rw := 0
	for _, s := range right {
		rw += segWidth(s)
	}
	start := max(r.Width-rw, 0)
	draw(start, r.Width, right)
	draw(0, start, left)
}

// drawTabs は、タブの見出しを、1 行目の列 col から列 limit の手前まで左から並べる（FR-62）。
// カーソルのあるタブを StyleTabLineSelected、他を StyleTabLine で描く。
// 見出しが幅に収まらなければ、カーソルのあるタブが収まるまで左のタブを省き、省いたことを左の端の "<" で示す。
// 右に収まらないタブの見出しは、収まる列で切る。
//
// req: FR-62, FR-87
func (r *Renderer) drawTabs(g *Grid, theme Theme, tabs []Tab, col0, limit int) {
	avail := limit - col0
	if avail <= 0 {
		return
	}
	labels := make([]string, len(tabs))
	widths := make([]int, len(tabs))
	cur := 0
	for i, t := range tabs {
		labels[i] = r.tabLabel(t)
		widths[i] = StringWidth(labels[i], r.tabStop)
		if t.Current {
			cur = i
		}
	}
	// カーソルのあるタブの右の端までが幅に収まるように、左のタブを省く。省いたら左の端の 1 列を "<" に使う。
	first := 0
	for first < cur {
		lead := 0
		if first > 0 {
			lead = 1
		}
		if sumTo(widths, first, cur)+lead <= avail {
			break
		}
		first++
	}
	col := col0
	if first > 0 {
		g.set(0, col0, Cell{Text: "<", Width: 1, Attr: theme.attr(StyleTabLine)})
		col++
	}
	want := col
	for i := first; i < len(tabs) && col < limit; i++ {
		a := theme.attr(StyleTabLine)
		if tabs[i].Current {
			a = theme.attr(StyleTabLineSelected)
		}
		want += widths[i]
		from := col
		col = r.drawText(g, 0, col, limit, labels[i], a)
		// タブの行のクリックでタブを移るために、見出しの場所を残す（mouse.go。FR-181）。
		r.hits.tabs = append(r.hits.tabs, tabSpan{from: from, to: col, index: i})
		if col < want {
			break // 収まらずに切れた
		}
	}
}

// sumTo は、widths[from] から widths[to] までの合計を返す。
func sumTo(widths []int, from, to int) int {
	w := 0
	for _, x := range widths[from : to+1] {
		w += x
	}
	return w
}

// renderWithTabLine は、タブの行を出すときの Render である。バッファの表示領域からステータス行とコマンドラインの行までを、
// 1 行低い画面として描き、その下に 1 行ずらして置き、空いた一番上の行にタブの行を描く。
func (r *Renderer) renderWithTabLine(f *Frame) Screen {
	r.Height--
	r.tabRows, r.bodyTop = 0, 1
	body := r.render(f)
	r.Height++
	r.tabRows, r.bodyTop = 1, 0
	theme := r.Theme
	if theme == nil {
		theme = DefaultTheme()
	}
	g := NewGrid(r.Width, r.Height)
	for i := range body.Grid.Height() {
		g.SetRow(i+1, body.Grid.Row(i))
	}
	r.drawTabLine(g, theme, f)
	cur := body.Cursor
	cur.Row++
	return Screen{Grid: g, Cursor: cur}
}
