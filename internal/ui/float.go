package ui

import (
	"fmt"
	"slices"
	"strings"
)

// 浮かべる表示の大きさの上限（仮）。
const (
	// popupMaxWidth は、ポップアップ（Frame.Float）の窓の列数の上限である（枠の余白と枠線を含む）。
	popupMaxWidth = 80
	// popupMaxRows は、カーソルの近くに置くポップアップの窓の行数の上限である（見出しの行と枠線を含む）。
	popupMaxRows = 12
	// floatScanLines は、窓の幅を決めるために表示の幅を測る行数の上限である。大きなバッファをタブから縮めたときに、
	// 描くたびに全行を測らないためである。この行より後の長い行は、横にスクロールして見る。
	floatScanLines = 1000
)

// PopupTextWidth は、ポップアップのテキストの列数の上限（枠線と余白を除く）を、画面の幅 width から返す。
// ホバーの説明のように、本体が前もって折り返してからテキストのバッファにする本文（internal/editor）に使う。
// 画面の大きさがまだ届いていない（width が 0 以下）なら、上限の幅を返す。
func PopupTextWidth(width int) int {
	if width <= 0 {
		width = popupMaxWidth
	}
	return max(min(width, popupMaxWidth)-4, 1)
}

// WrapText は、s の各行を、幅 width の行に空白の位置で折り返した行の列を返す（wrapWords。#155）。末尾の空の行は除く。
func WrapText(s string, width, tabStop int) []string {
	var out []string
	for i, l := range textLines(s) {
		for _, or := range wrapWords(i, l, max(width, 1), tabStopOr(tabStop)) {
			out = append(out, l[or.start:or.end])
		}
	}
	return out
}

// textLines は、s を行に分け、末尾の空の行を除いて返す。
func textLines(s string) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// clearSplit は、row 行目の [x0, x1) の列を上書きする前に、その外にはみ出す全角文字の半分を空白にする。
// 全角文字の右半分だけ、または左半分だけが上書きされると、端末で残りの半分がずれて見えるためである。
func clearSplit(g *Grid, row, x0, x1 int) {
	if x0 > 0 && x0 < g.Width() && g.At(row, x0).Width == 0 {
		g.set(row, x0-1, blank(g.At(row, x0-1).Attr))
	}
	if x1 > 0 && x1 < g.Width() && g.At(row, x1).Width == 0 {
		g.set(row, x1, blank(g.At(row, x1).Attr))
	}
}

// StripCodeFences は、Markdown の本文から、コードの囲みの行（行頭の 3 つまでの空白の後に ``` か ~~~ が続く行。
// CommonMark の fenced code block の開きと閉じ）を除き、囲みの中身の行は残す（#151）。
// gopls のホバーは、宣言を ```go の囲みに入れて返すので、囲みの行がそのまま窓に出ていた。
// 囲みの開きと閉じの対応は追わない。4 つ以上の ` で開いた囲みの中の ``` の行も除くが、ホバーの本文の宣言の囲みでは使われないと見て、簡単な方を選んだ。
// ホバーの本文をテキストのバッファにする前に、internal/editor が呼ぶ。
func StripCodeFences(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0:0]
	for _, l := range lines {
		t := strings.TrimLeft(l, " ")
		if len(l)-len(t) <= 3 && (strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")) {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// drawFloat は、ポップアップ（Frame.Float）を、バッファの表示領域の上に浮かべて描く（基本設計 1.4。FR-68、FR-72）。
// 窓の中のテキストは、窓の大きさの子の Renderer（Renderer.float）で描くので、バッファの表示領域と同じく、構文の色、選択、
// 横と縦のスクロールが効く。子の Renderer は窓（Float.ID）ごとに作り直し、同じ窓の間はスクロールの位置を保つ。
// 窓の中のカーソルを置いた端末のカーソルを、Float.Focused なら ok を true にして返す。
//
// 置き場所は 2 つある。
//
//   - カーソルの近く（Center が false）: カーソルの文字の列から始まり、カーソルの下の行に置く。下に収まらず、上の方が広ければ上の行に置く。
//     行数は popupMaxRows 行（見出しと枠線を含む）と置ける行数の小さい方を上限にする
//   - 中央（Center が true）: バッファの表示領域の縦と横の中央に置く。行数は表示領域に収まるまで使う
//
// 幅は popupMaxWidth 列と画面の幅の小さい方を上限に、テキストの最も長い行（先頭の floatScanLines 行で測る）と見出しに合わせ、
// 左右に 1 列の余白を置く。長い行は折り返さずに、バッファの表示領域と同じく横にスクロールする
// （折り返したいテキスト（ホバーの説明）は、本体が前もって折り返してバッファにする。PopupTextWidth）。
// 枠線の記号（SymbolPopupBorder）があれば、窓の周りに 1 列と 1 行ずつ枠線を描き、見出しを上の枠線に重ねる。
// 枠線が無ければ、見出しを窓の最初の行に描く。幅と行数の上限は枠線と見出しを含めて数える。
// 窓の色は StylePopup で、テキストの色はその上に重ねる。CursorLine なら、カーソルの行を StylePopupSelected で描く。
// フォーカスの無い窓に収まらない行があれば、最後の行に残りの行数を書く（フォーカスを入れればスクロールして読める）。
// Fill なら、枠線を描かずに、窓をバッファの表示領域の全体（画面の幅と、表示領域の行数）に広げる（部品の client の画面）。
// Switcher（切り替えの部品。FR-269）なら、窓の中の最初の行（枠線が無ければ見出しの次の行）に見出しの行を描き（drawSwitcher）、
// 窓を中身の行数と幅に依らず置ける最大の大きさにする。窓の中を描く子の Renderer は子ごとに持つ（useFloatSubs）。
func (r *Renderer) drawFloat(g *Grid, theme Theme, fl *Float, curRow, curCol int) (Cursor, bool) {
	if fl.View == nil || fl.View.Text == nil {
		return Cursor{}, false
	}
	r.console = fl.Console != nil
	if fl.Console != nil {
		return r.drawConsole(g, theme, fl)
	}
	if fl.List != nil {
		return r.drawFinder(g, theme, fl)
	}
	chars, bordered := r.border()
	if fl.Fill {
		bordered = false
	}
	b := 0
	if bordered {
		b = 1
	}
	// 窓の幅は、中身のバッファのタブ文字の幅（View.TabStop）で数える。
	tabStop := tabStopOr(fl.View.TabStop)
	maxW := min(r.Width, popupMaxWidth) - 2*b
	if fl.Fill {
		maxW = r.Width
	}
	// プレビューを出すなら、窓を幅の規則の幅まで広げ、右の pw 列と区切りの 1 列を除いた列を、中身の幅にする（FR-149。preview.go）。
	window, pw, preview := r.previewLayout(fl)
	if preview {
		maxW = window - 2*b - pw - 1
	} else {
		r.floatPreview = nil
	}
	if maxW < 3 {
		return Cursor{}, false
	}
	text := fl.View.Text
	n := text.LineCount()
	width := StringWidth(fl.Title, tabStop)
	for i := range min(n, floatScanLines) {
		width = max(width, StringWidth(text.Line(i), tabStop))
	}
	if fl.Wide || fl.Fill || preview || fl.Switcher != nil {
		width = maxW
	}
	// head は、窓の中の最初の行から置く行（枠線が無いときの見出しの行と、切り替えの部品の見出しの行）の数である。
	titleRows := 0
	if fl.Title != "" && !bordered {
		titleRows = 1
	}
	head := titleRows
	if fl.Switcher != nil {
		head++
	}
	textRows := r.textRows()
	// 上と下に本文を置ける行数は、枠線の 2 行と見出しの行を除いて数える（PR #206 のレビュー）。
	below, above := textRows-curRow-1-2*b-head, curRow-2*b-head
	useAbove := !fl.Center && n > below && above > below
	space := below
	switch {
	case fl.Center, fl.Fill:
		space = textRows - 2*b - head
	case useAbove:
		space = above
	}
	h := min(n, space)
	if fl.Fill || preview || fl.Switcher != nil {
		// 中身が短くても、表示領域の全体を窓にする。プレビューを出す窓も、一覧の行数に依らずプレビューの行を取る。
		// 切り替えの部品も、子を切り替えるたびに窓が伸び縮みしないよう、子の行数に依らず置ける最大の行数にする。
		h = space
	} else if !fl.Center {
		h = min(h, popupMaxRows-2*b-head)
	}
	if h <= 0 {
		return Cursor{}, false
	}
	boxH := h + head
	note := ""
	if !fl.Focused && h < n {
		note = fmt.Sprintf("（残り %d 行）", n-h+1)
		width = max(width, StringWidth(note, tabStop))
	}
	boxW := min(width+2, maxW)
	top := curRow + 1 + b
	x0 := max(min(curCol, r.Width-boxW-2*b), 0) + b
	switch {
	case fl.Fill:
		top, x0 = 0, 0
	case fl.Center:
		top = (textRows-boxH-2*b)/2 + b
		x0 = max((r.Width-boxW-2*b)/2, 0) + b
		if preview {
			x0 = max((r.Width-window)/2, 0) + b
		}
	case useAbove:
		top = curRow - boxH - b
	}
	// outerW は、枠線の内側の窓の幅（プレビューを出すなら、区切りの列とプレビューを含む）である。
	outerW := boxW
	if preview {
		outerW = boxW + 1 + pw
	}
	r.useFloatSubs(fl)
	sub := r.float
	sub.Width, sub.Height = max(boxW-2, 1), h+2
	sub.Theme, sub.Symbols = theme, r.Symbols
	view := *fl.View
	view.Tabs, view.Output, view.List, view.Choice, view.Float, view.Diff = nil, nil, nil, nil, nil, nil
	view.Message, view.HitEnter, view.LineNumbers, view.Search, view.Highlights = Message{}, false, LineNumbers{}, nil, nil
	view.Scrollbar, view.Intro, view.Band, view.Input = nil, nil, nil, nil
	if view.Mode == ModeCommandLine {
		// コマンドラインは画面の最後の行に描く（Render）。窓の中は、コマンドラインに入る前のノーマルモードとして描く。
		view.Mode = ModeNormal
	}
	inner := sub.render(&view)
	// マウスの出来事の位置を解くために、窓の場所と、中のテキストの場所を残す（mouse.go）。「残り N 行」の行はテキストに数えない。
	r.hits.cover(top-b, x0-b, boxH+2*b, outerW+2*b)
	textH := boxH - head
	if note != "" {
		textH--
	}
	r.hits.float, r.hits.floatOK = rect{top: top + head, left: x0 + 1, h: max(textH, 0), w: sub.Width}, textH > 0
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
	for i := range boxH {
		row := top + i
		clearSplit(g, row, x0, x0+boxW)
		base := a
		if i >= head && fl.CursorLine && inner.Cursor.Row == i-head {
			base = sel
		}
		for c := range boxW {
			g.set(row, x0+c, blank(base))
		}
		switch {
		case i < titleRows:
			r.drawText(g, row, x0+1, x0+boxW-1, fl.Title, a.Over(Attr{Bold: true}))
		case i < head:
			r.drawSwitcher(g, theme, fl.Switcher, row, x0, x0+boxW, a)
		case i == boxH-1 && note != "":
			r.drawText(g, row, x0+1, x0+boxW-1, note, a)
		case preview && sub.Top()+i-head >= n:
			// プレビューを出す窓は一覧の行数に依らず高いので、一覧の最後の項目より下の行は空けておく（子の Renderer の "~" を出さない）。
		default:
			for c := range sub.Width {
				cell := inner.Grid.At(i-head, c)
				cell.Attr = base.Over(cell.Attr)
				g.set(row, x0+1+c, cell)
			}
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
	cur.Row += top + head
	cur.Col += x0 + 1
	return cur, true
}

// drawBorder は、row top から h 行、列 x0 から w 列の窓の周りに、枠線の 8 文字 chars（SymbolPopupBorder の並び）を描く。
// 色は StylePopupBorder で、表に無ければ StylePopup である。枠線が画面の外に出る部分は描かない。
//
// req: FR-93
func (r *Renderer) drawBorder(g *Grid, theme Theme, chars [borderChars]string, top, x0, h, w int) {
	a, ok := theme[StylePopupBorder]
	if !ok {
		a = theme.attr(StylePopup)
	}
	r.drawBorderAttr(g, a, chars, top, x0, h, w)
}

// drawBorderAttr は、drawBorder の、枠線を色 a で描くものである（画面の上のポップアップのコマンドラインの枠。FR-216）。
func (r *Renderer) drawBorderAttr(g *Grid, a Attr, chars [borderChars]string, top, x0, h, w int) {
	put := func(row, col int, s string) {
		if row >= 0 && row < r.Height && col >= 0 && col < r.Width {
			clearSplit(g, row, col, col+1)
			g.set(row, col, Cell{Text: s, Width: 1, Attr: a})
		}
	}
	const (
		topLeft = iota
		topEdge
		topRight
		rightEdge
		bottomRight
		bottomEdge
		bottomLeft
		leftEdge
	)
	put(top-1, x0-1, chars[topLeft])
	put(top-1, x0+w, chars[topRight])
	put(top+h, x0-1, chars[bottomLeft])
	put(top+h, x0+w, chars[bottomRight])
	for c := range w {
		put(top-1, x0+c, chars[topEdge])
		put(top+h, x0+c, chars[bottomEdge])
	}
	for i := range h {
		put(top+i, x0-1, chars[leftEdge])
		put(top+i, x0+w, chars[rightEdge])
	}
}

// listMaxWidth は、浮かべる一覧（Frame.List）の窓の列数の上限（仮）である。
const listMaxWidth = 80

// drawList は、選択の一覧（:registers の一覧。FR-54）を、バッファの表示領域の上の端から浮かべて描き、
// 選んでいる項目の行の先頭に置くカーソルを返す。表示領域が無ければ描かずに false を返す。
//
// 窓は、表示領域の横の中央に置き、最初の行に見出しを、続く行に項目を 1 つずつ描く。
// 幅は listMaxWidth 列と画面の幅の小さい方を上限に、見出しと項目の最も長いものに合わせ、左右に 1 列の余白を置く。
// 長い項目は右の端で切る。項目が表示領域に収まらなければ、選んでいる項目が見える位置までずらして描く。
// 選んでいる項目の行は StylePopupSelected で、他の行は StylePopup で描く。
//
// req: FR-54
//
// 枠線の記号（SymbolPopupBorder）があれば、drawPopup と同じく窓の周りに枠線を描き、幅と行数の上限は枠線を含めて数える。
func (r *Renderer) drawList(g *Grid, theme Theme, l *List) (Cursor, bool) {
	chars, bordered := r.border()
	b := 0
	if bordered {
		b = 1
	}
	rows := r.textRows() - 2*b
	maxW := min(r.Width, listMaxWidth) - 2*b
	if rows < 2 || maxW < 3 {
		return Cursor{}, false
	}
	tabStop := tabStopOr(r.tabStop)
	width := StringWidth(l.Title, tabStop)
	for _, it := range l.Items {
		width = max(width, StringWidth(it, tabStop))
	}
	boxW := min(width+2, maxW)
	x0 := max((r.Width-boxW-2*b)/2, 0) + b
	shown := min(len(l.Items), rows-1)
	sel := min(max(l.Selected, 0), max(len(l.Items)-1, 0))
	first := max(sel-shown+1, 0)
	a := theme.attr(StylePopup)
	sa := theme.attr(StylePopupSelected)
	// 浮かべた一覧の窓の中は、マウスで操作しない（mouse.go。窓の場所だけを残す）。
	r.hits.cover(0, x0-b, 1+shown+2*b, boxW+2*b)
	if bordered {
		r.drawBorder(g, theme, chars, b, x0, 1+shown, boxW)
	}
	drawRow := func(row int, s string, attr Attr) {
		clearSplit(g, row, x0, x0+boxW)
		for c := range boxW {
			g.set(row, x0+c, blank(attr))
		}
		r.drawText(g, row, x0+1, x0+boxW-1, s, attr)
	}
	drawRow(b, l.Title, a)
	cur := Cursor{Row: b, Col: x0 + 1, Shape: CursorBlock, Visible: true}
	for i := range shown {
		n := first + i
		attr := a
		if n == sel {
			attr = sa
			cur.Row = b + 1 + i
		}
		drawRow(b+1+i, l.Items[n], attr)
	}
	return cur, true
}

// wrapWords は、s を幅 width の行に、空白（半角の空白とタブ）の位置で折り返した、行ごとのバイトの範囲を返す
// （Vim の 'linebreak' に近い。ホバーの本文の英語の文を単語の途中で切らないため。#155）。空の行は 1 行にする。
//
// 幅を超える文字の前に、同じ行の中で空白があれば、最後の空白の後から次の行を始める。
// 空白が無ければ（幅より長い単語、空白で区切らない日本語の文）、wrapLine と同じく幅を超える文字の前で切る。
// 行の末尾で幅を超える空白は、どの行にも含めずに飛ばす（次の行を空白で始めない）。
func wrapWords(line int, s string, width, tabStop int) []outputRow {
	rows := []outputRow{{line: line}}
	col, rowCol := 0, 0
	// brk は、いまの行の中で最後の空白の後のバイト位置、brkCol はその表示の列、brkEnd はそこで切るときのいまの行の終わりである。
	brk, brkCol, brkEnd := -1, 0, 0
	for off := 0; off < len(s); {
		gl := nextGlyph(s, off, col, tabStop)
		cur := &rows[len(rows)-1]
		blank := gl.kind == glyphTab || (gl.kind == glyphText && gl.text == " ")
		if gl.col+gl.width-rowCol > width && off > cur.start {
			if blank {
				col += gl.width
				off += gl.size
				brk, brkCol, brkEnd = off, col, cur.end
				continue
			}
			if brk > cur.start {
				// 最後の空白の後まで戻り、そこから次の行を始める。
				cur.end = brkEnd
				off, col = brk, brkCol
				gl = nextGlyph(s, off, col, tabStop)
			}
			rows = append(rows, outputRow{line: line, start: off, end: off, col: gl.col})
			cur = &rows[len(rows)-1]
			rowCol = gl.col
		}
		col += gl.width
		off += gl.size
		cur.end = off
		if blank {
			brk, brkCol, brkEnd = off, col, off
		}
	}
	return rows
}

// drawMessageLines は、改行を含むメッセージを、コマンドラインの行から上へ広げて描く（Vim のメッセージの行が広がるのと同じ）。
// 広げるのは画面の半分の行までで、収まらなければ、最後の行に省いた行数を書く。長い行は右の端で切る。
// 上の行は、バッファの表示領域とステータス行の上に重ねる。次のキー入力でメッセージが消えると元に戻る。
// prompt が空でなければ、最後の行に prompt を pa の属性で描き、メッセージの行はその上に置く（問いの行は画面の半分に数えない）。
// prompt を描いた最後の行の、描いた文字の次の列を返す。
func (r *Renderer) drawMessageLines(g *Grid, lines []string, a Attr, prompt string, pa Attr) int {
	limit := max(r.Height/2, 1)
	if prompt != "" {
		limit = min(limit, r.Height-1)
	}
	switch {
	case limit <= 0:
		lines = nil
	case len(lines) > limit:
		rest := len(lines) - (limit - 1)
		lines = append(lines[:limit-1:limit-1], fmt.Sprintf("（残り %d 行）", rest))
	}
	rows := len(lines)
	if prompt != "" {
		rows++
	}
	top := r.Height - rows
	blankRow := func(row int) {
		for c := range r.Width {
			g.set(row, c, blank(Attr{}))
		}
	}
	for i, l := range lines {
		blankRow(top + i)
		r.drawText(g, top+i, 0, r.Width, l, a)
	}
	if prompt == "" {
		return 0
	}
	blankRow(r.Height - 1)
	return r.drawText(g, r.Height-1, 0, r.Width, prompt, pa)
}

// HitEnterPrompt は、改行を含むメッセージの下に出す、続けるためのキーを促す問いである（Frame.HitEnter）。
// Vim 9.1 の日本語のメッセージ（"Press ENTER or type command to continue"）に、ASCII の語の前後の空白を足したもの。
const HitEnterPrompt = "続けるには ENTER を押すかコマンドを入力してください"

// ChoicePrompt は、選択肢の問いの最後の行に出す、選び方の案内である（Frame.Choice）。n は番号で選べる選択肢の数である。
// HitEnterPrompt と同じく、ASCII の語の前後に空白を置く。狭い端末でも <Esc> まで見えるように、文にせず短くする（40 列に収まる）。
func ChoicePrompt(n int) string {
	switch n {
	case 0:
		return "<Esc> で取り消し"
	case 1:
		return "1 で選ぶか <Esc> で取り消し"
	}
	return fmt.Sprintf("1〜%d で選ぶか <Esc> で取り消し", n)
}

// wrapText は、s を幅 width の行に折り返した文字列を返す（wrapLine の範囲を文字列にしたもの）。
func (r *Renderer) wrapText(s string, width int) []string {
	rows := wrapLine(0, s, max(width, 1), tabStopOr(r.tabStop))
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = s[row.start:row.end]
	}
	return out
}

// drawChoice は、選択肢の問いを、画面の下から上へ広げて描き、カーソルの位置を返す（基本設計 6.7）。
// 最後の行に選び方の案内（ChoicePrompt。続けて届いた問いなら前に "問い 2: "）を描き、その上に知らせの行（Choice.Note）と
// 問いの文の行と、選択肢を 1 つずつ "[番号] ラベル" の行で描く。
// 長い行は折り返す。改行を含むメッセージ（drawMessageLines）と違って画面の半分で止めず、案内の行を除く画面の全体まで広げる。
// 収まらなければ、選択肢を残すために問いの文の行を先に減らし（最低 1 行）、それでも収まらなければ選択肢の行を減らす。
// 減らした側の最後の行には、省いた行数を書く。カーソルは案内の末尾（幅に収まらなければ最後の列）に置く。
func (r *Renderer) drawChoice(g *Grid, theme Theme, c *Choice) Cursor {
	a := theme.attr(StyleMessageInfo)
	var title, items []string
	// 知らせの行は問いの文の前に置き、問いの文と同じく減らす（上の行から残すので、知らせが先に残る）。
	for _, l := range slices.Concat(textLines(c.Note), textLines(c.Title)) {
		title = append(title, r.wrapText(l, r.Width)...)
	}
	for i, l := range c.Labels {
		// ラベルの改行は空白にして、選択肢を 1 つの行（折り返しを除く）に保つ。
		l = strings.Join(strings.Fields(l), " ")
		items = append(items, r.wrapText(fmt.Sprintf("[%d] %s", i+1, l), r.Width)...)
	}
	if c.Omitted > 0 {
		items = append(items, r.wrapText(fmt.Sprintf("（他の %d 件は番号が無いので選べません）", c.Omitted), r.Width)...)
	}
	avail := max(r.Height-1, 0)
	tk := min(len(title), max(avail-len(items), min(avail, 1)))
	if 0 < tk && tk < len(title) {
		title = append(title[:tk-1:tk-1], fmt.Sprintf("（問いの文の残り %d 行）", len(title)-(tk-1)))
	}
	ik := min(len(items), avail-tk)
	if ik < len(items) {
		if ik > 0 {
			items = append(items[:ik-1:ik-1], fmt.Sprintf("（残り %d 行）", len(items)-(ik-1)))
		} else {
			items = nil
		}
	}
	lines := slices.Concat(title[:tk], items)
	top := r.Height - 1 - len(lines)
	blankRow := func(row int) {
		for col := range r.Width {
			g.set(row, col, blank(Attr{}))
		}
	}
	for i, l := range lines {
		blankRow(top + i)
		r.drawText(g, top+i, 0, r.Width, l, a)
	}
	row := r.Height - 1
	blankRow(row)
	prompt := ChoicePrompt(len(c.Labels))
	if c.Index > 0 {
		prompt = fmt.Sprintf("問い %d: %s", c.Index, prompt)
	}
	end := r.drawText(g, row, 0, r.Width, prompt, a)
	return Cursor{Row: row, Col: min(end, max(r.Width-1, 0)), Shape: CursorBlock, Visible: r.Width > 0}
}
