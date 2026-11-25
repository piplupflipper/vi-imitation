package ui

// finderPrompt は、ファインダーの入力の行の前に置く印である（fzf の既定の "> " と同じ）。
const finderPrompt = "> "

// finderEmpty は、ファインダーの一覧が空のときに一覧の最初の行に書く文である。
const finderEmpty = "（一致する項目はありません）"

// drawFinder は、入力の行と一覧の組（ファインダー。Float.List。FR-106）を、バッファの表示領域の中央に浮かべて描く。
// 窓の幅は popupMaxWidth 列と画面の幅の小さい方、行数は表示領域に収まる最大で、中身の多さで窓の大きさを変えない
// （打つたびに一覧の件数が変わっても、窓が伸び縮みしないため）。
//
// 窓の中は、上から、見出しの行（枠線が無いとき。枠線があれば上の枠線に重ねる）、入力の行（finderPrompt の後に Float.View）、
// 区切りの行（枠線があるときだけ。枠線の上の辺の文字）、一覧（Float.List）の順に置く。
// 入力の行と一覧は、それぞれ窓の大きさの子の Renderer（Renderer.float と Renderer.floatList）で描くので、
// 構文の色（一覧の一致した文字の StyleFinderMatch など）と横のスクロールが効き、一覧はカーソルの行が見える位置まで縦にスクロールする。
// 一覧のカーソルの行は StylePopupSelected で描く。端末のカーソルは、Float.Focused なら入力の行のカーソルに置く。
//
// req: FR-106
func (r *Renderer) drawFinder(g *Grid, theme Theme, fl *Float) (Cursor, bool) {
	chars, bordered := r.border()
	if fl.Fill {
		// 端末多重化ソフトのポップアップの中の部品の client では、枠線を描かずに画面の全体を使う（drawFloat の Fill と同じ。#248）。
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
	// プレビューを出すなら、窓を幅の規則の幅まで広げ、入力の行と一覧を左に、プレビューを右に置く（FR-149。preview.go）。
	window, pw, preview := r.previewLayout(fl)
	if preview {
		boxW = window - 2*b - pw - 1
	} else {
		r.floatPreview = nil
	}
	head, sep := 0, 0
	if fl.Title != "" && !bordered {
		head = 1
	}
	if bordered {
		sep = 1
	}
	textRows := r.textRows()
	boxH := textRows - 2*b
	listRows := boxH - head - 1 - sep
	promptW := StringWidth(finderPrompt, tabStop)
	if boxW < promptW+3 || listRows < 1 {
		return Cursor{}, false
	}
	top := (textRows-boxH-2*b)/2 + b
	x0 := max((r.Width-boxW-2*b)/2, 0) + b
	outerW := boxW
	if preview {
		x0 = max((r.Width-window)/2, 0) + b
		outerW = boxW + 1 + pw
	}
	r.useFloatSubs(fl)
	// ファインダーの窓の中は、マウスで操作しない（mouse.go。窓の場所だけを残す）。
	r.hits.cover(top-b, x0-b, boxH+2*b, outerW+2*b)
	in, list := r.float, r.floatList
	in.Width, in.Height = boxW-2-promptW, 1+2
	list.Width, list.Height = boxW-2, listRows+2
	for _, s := range []*Renderer{in, list} {
		s.Theme, s.Symbols = theme, r.Symbols
	}
	view := *fl.View
	view.Tabs, view.Output, view.List, view.Choice, view.Float, view.Diff = nil, nil, nil, nil, nil, nil
	view.Message, view.HitEnter, view.LineNumbers, view.Search = Message{}, false, LineNumbers{}, nil
	view.Scrollbar, view.Intro, view.Band, view.Input = nil, nil, nil, nil
	if view.Mode == ModeCommandLine {
		view.Mode = ModeNormal
	}
	inner := in.render(&view)
	lv := Frame{Text: fl.List.Text, Cursor: fl.List.Cursor, Decorations: fl.List.Decorations}
	listed := list.render(&lv)

	a := theme.attr(StylePopup)
	sel := theme.attr(StylePopupSelected)
	if bordered {
		r.drawBorder(g, theme, chars, top, x0, boxH, outerW)
		if fl.Title != "" {
			ba, ok := theme[StylePopupBorder]
			if !ok {
				ba = a
			}
			r.drawText(g, top-1, x0+1, x0+boxW-1, fl.Title, ba.Over(Attr{Bold: true}))
		}
	}
	blankRow := func(row int, attr Attr) {
		clearSplit(g, row, x0, x0+boxW)
		for c := range boxW {
			g.set(row, x0+c, blank(attr))
		}
	}
	row := top
	if head > 0 {
		blankRow(row, a)
		r.drawText(g, row, x0+1, x0+boxW-1, fl.Title, a.Over(Attr{Bold: true}))
		row++
	}
	inRow := row
	blankRow(row, a)
	r.drawText(g, row, x0+1, x0+1+promptW, finderPrompt, a.Over(Attr{Bold: true}))
	for c := range in.Width {
		cell := inner.Grid.At(0, c)
		cell.Attr = a.Over(cell.Attr)
		g.set(row, x0+1+promptW+c, cell)
	}
	row++
	if sep > 0 {
		ba, ok := theme[StylePopupBorder]
		if !ok {
			ba = a
		}
		clearSplit(g, row, x0, x0+boxW)
		for c := range boxW {
			g.set(row, x0+c, Cell{Text: chars[1], Width: 1, Attr: ba})
		}
		row++
	}
	for i := range listRows {
		base := a
		if fl.ListCount > 0 && listed.Cursor.Row == i {
			base = sel
		}
		blankRow(row+i, base)
		if fl.ListCount == 0 {
			if i == 0 {
				r.drawText(g, row, x0+1, x0+boxW-1, finderEmpty, a)
			}
			continue
		}
		if list.Top()+i >= fl.List.Text.LineCount() {
			// 一覧の最後の項目より下の行は空けておく（子の Renderer がバッファの外の行に描く印を出さない）。
			continue
		}
		for c := range list.Width {
			cell := listed.Grid.At(i, c)
			cell.Attr = base.Over(cell.Attr)
			g.set(row+i, x0+1+c, cell)
		}
	}
	if preview {
		r.drawPreviewSeparator(g, theme, top, x0+boxW, boxH)
		pcur, pok := r.drawPreview(g, theme, fl.Preview, top, x0+boxW+1, pw, boxH)
		if fl.Preview.Focused {
			return pcur, pok
		}
	}
	if !fl.Focused || !inner.Cursor.Visible {
		return Cursor{}, false
	}
	cur := inner.Cursor
	cur.Row += inRow
	cur.Col += x0 + 1 + promptW
	return cur, true
}
