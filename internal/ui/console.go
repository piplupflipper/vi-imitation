package ui

// drawConsole は、書き足すテキストと入力の行の組（Float.Console。FR-274）を描く。
// 中央の窓（Center）ならバッファの表示領域の中央に、画面の全体の窓（Fill。部品の client の画面）なら表示領域の全体に描く。
// 窓の幅は popupMaxWidth 列と画面の幅の小さい方、行数は表示領域に収まる最大で、中身の多さで窓の大きさを変えない
// （書き足すたびに窓が伸び縮みしないため。drawFinder と同じ）。
//
// 窓の中は、上から、見出しの行（枠線が無いとき。枠線があれば上の枠線に重ねる）、出力（Console.Output）、
// 区切りの行（枠線があるときだけ。枠線の上の辺の文字）、入力の行（Console.Prompt の後に Float.View）の順に置く。
// ファインダー（drawFinder）の並びを上下に替えたものである（基本設計 1.4 の「書き足すテキストと入力の行の組」）。
// 出力と入力の行は、それぞれ窓の大きさの子の Renderer（Renderer.floatList と Renderer.float）で描くので、
// 構文の色、選択、横のスクロール（入力の行が窓より長いとき）と、出力の縦のスクロール（カーソルの行が見える位置まで）が効く。
// 出力の最後の行より下の行は空けておく（子の Renderer の "~" を出さない）。
//
// 狭い画面では、まず区切りの行を、次に出力を、次に見出しの行を減らし、入力の行は 1 列でも残す。前置きが入力の行の幅を超えるときは、
// 入力の 1 列を残して前置きの右を切る。入力の行を置けないほど狭いときは描かない。
// 端末のカーソルは、Float.Focused なら、Console.OutputFocused に従って出力か入力の行のカーソルに置く。
//
// req: FR-274
func (r *Renderer) drawConsole(g *Grid, theme Theme, fl *Float) (Cursor, bool) {
	con := fl.Console
	chars, bordered := r.border()
	if fl.Fill {
		bordered = false
	}
	b := 0
	if bordered {
		b = 1
	}
	tabStop := tabStopOr(fl.View.TabStop)
	boxW := min(r.Width, popupMaxWidth) - 2*b
	if fl.Fill {
		boxW = r.Width
	}
	r.floatPreview = nil
	textRows := r.textRows()
	boxH := textRows - 2*b
	// 窓の中の最初の行から、見出しの行（枠線が無いとき）と切り替えの部品の見出しの行（FR-269）を置く。
	titleRows, bar := 0, 0
	if fl.Title != "" && !bordered {
		titleRows = 1
	}
	if fl.Switcher != nil {
		bar = 1
	}
	if boxH-titleRows-bar < 1 {
		// 見出しより入力の行を残す。
		titleRows = 0
	}
	if boxH-bar < 1 {
		bar = 0
	}
	head := titleRows + bar
	avail := boxH - head
	innerW := boxW - 2
	if avail < 1 || innerW < 2 {
		return Cursor{}, false
	}
	sep := 0
	if bordered && avail >= 3 {
		sep = 1
	}
	outRows := avail - 1 - sep
	promptW := min(StringWidth(con.Prompt, tabStop), innerW-1)
	top := (textRows-boxH-2*b)/2 + b
	x0 := max((r.Width-boxW-2*b)/2, 0) + b
	r.useFloatSubs(fl)
	// 窓の中は、マウスで操作しない（mouse.go。窓の場所だけを残す。ファインダーと同じ）。
	r.hits.cover(top-b, x0-b, boxH+2*b, boxW+2*b)
	in, out := r.float, r.floatList
	in.Width, in.Height = innerW-promptW, 1+2
	out.Width, out.Height = innerW, outRows+2
	for _, s := range []*Renderer{in, out} {
		s.Theme, s.Symbols = theme, r.Symbols
	}
	strip := func(v *Frame) {
		v.Tabs, v.Output, v.List, v.Choice, v.Float, v.Diff = nil, nil, nil, nil, nil, nil
		v.Message, v.HitEnter, v.LineNumbers, v.Search, v.Highlights = Message{}, false, LineNumbers{}, nil, nil
		v.Scrollbar, v.Intro, v.Band, v.Input = nil, nil, nil, nil
		if v.Mode == ModeCommandLine {
			// コマンドラインは画面の最後の行に描く（Render）。窓の中は、コマンドラインに入る前のノーマルモードとして描く。
			v.Mode = ModeNormal
		}
	}
	view := *fl.View
	strip(&view)
	inner := in.render(&view)
	var outView Frame
	if con.Output != nil {
		outView = *con.Output
	}
	strip(&outView)
	var outScr Screen
	if outRows > 0 {
		outScr = out.render(&outView)
	}

	a := theme.attr(StylePopup)
	ba, ok := theme[StylePopupBorder]
	if !ok {
		ba = a
	}
	if bordered {
		r.drawBorder(g, theme, chars, top, x0, boxH, boxW)
		if fl.Title != "" {
			r.drawText(g, top-1, x0+1, x0+boxW-1, fl.Title, ba.Over(Attr{Bold: true}))
		}
	}
	blankRow := func(row int) {
		clearSplit(g, row, x0, x0+boxW)
		for c := range boxW {
			g.set(row, x0+c, blank(a))
		}
	}
	row := top
	if titleRows > 0 {
		blankRow(row)
		r.drawText(g, row, x0+1, x0+boxW-1, fl.Title, a.Over(Attr{Bold: true}))
		row++
	}
	if bar > 0 {
		r.drawSwitcher(g, theme, fl.Switcher, row, x0, x0+boxW, a)
		row++
	}
	outTop := row
	lines := 1
	if outView.Text != nil {
		lines = outView.Text.LineCount()
	}
	for i := range outRows {
		blankRow(row + i)
		if l := out.rowLine(i); l < 0 || l >= lines {
			// 出力の最後の行より下の行（と見比べの詰め物の行）は空けておく。
			continue
		}
		for c := range out.Width {
			cell := outScr.Grid.At(i, c)
			cell.Attr = a.Over(cell.Attr)
			g.set(row+i, x0+1+c, cell)
		}
	}
	row += outRows
	if sep > 0 {
		clearSplit(g, row, x0, x0+boxW)
		for c := range boxW {
			g.set(row, x0+c, Cell{Text: chars[1], Width: 1, Attr: ba})
		}
		row++
	}
	inRow := row
	blankRow(row)
	if promptW > 0 {
		r.drawText(g, row, x0+1, x0+1+promptW, con.Prompt, a.Over(Attr{Bold: true}))
	}
	for c := range in.Width {
		cell := inner.Grid.At(0, c)
		cell.Attr = a.Over(cell.Attr)
		g.set(row, x0+1+promptW+c, cell)
	}
	if !fl.Focused {
		return Cursor{}, false
	}
	if con.OutputFocused {
		if outRows <= 0 || !outScr.Cursor.Visible {
			return Cursor{}, false
		}
		cur := outScr.Cursor
		cur.Row += outTop
		cur.Col += x0 + 1
		return cur, true
	}
	if !inner.Cursor.Visible {
		return Cursor{}, false
	}
	cur := inner.Cursor
	cur.Row += inRow
	cur.Col += x0 + 1 + promptW
	if con.Menu != nil {
		r.drawConsoleMenu(g, theme, con.Menu, fl.View, cur.Col, inRow, top, x0, x0+boxW)
	}
	return cur, true
}

// drawConsoleMenu は、書き足すテキストと入力の行の組の入力の行の補完の候補の一覧 m（Console.Menu。FR-297）を、入力の行（inRow 行目）の
// 真上に描く。項目は、入力の行の m.Start の文字の列から並べる（カーソルの列 curCol から、m.Start からカーソルまでの表示幅を引いた列）。
// 一覧は、窓の中（top 行目から inRow 行目の手前までの行と、left 列から right 列の手前までの列）に収め、出力、見出しの行、
// 切り替えの部品の見出しの行の上に重ねる。描き方（色、選んでいる項目、スクロールバー、種類の欄）はコマンドラインの一覧と同じである。
func (r *Renderer) drawConsoleMenu(g *Grid, theme Theme, m *CompletionMenu, view *Frame, curCol, inRow, top, left, right int) {
	col := curCol
	if view != nil && view.Text != nil && view.Text.LineCount() > 0 {
		line, at := view.Text.Line(0), view.Cursor.Byte
		if start := m.Start; start >= 0 && start <= at && at <= len(line) {
			col -= StringWidth(line[start:at], tabStopOr(view.TabStop))
		}
	}
	p, ok := r.placeCompletionIn(m, inRow, col, top, left, right)
	if !ok {
		r.pum.valid = false
		return
	}
	l := p.col
	if p.col > left {
		l = p.col - 1 // 項目の左の空白
	}
	rt := p.col + p.width
	if p.scrollbar {
		rt++
	}
	r.drawCompletionRows(g, theme, m, p, l, rt, theme.attr(StylePopup))
}
