package ui

// コマンドラインと検索の入力を、画面の最後の行の代わりに、画面の上の中央のポップアップの枠の中に描く（Frame.CommandLinePopup。
// FR-215〜FR-217。基本設計 7.4 の「画面の上のポップアップ」）。
//
// 置き方と大きさは、noice.nvim（コミット 7bfd942445fb63089b59f97ca487d605e715f155）の presets.command_palette の既定
// （lua/noice/config/preset.lua の cmdline_popup と cmdline_popupmenu）と、それを置く nui.nvim
// （コミット 10fc361835c856ba4233ef5ea135b919bf3dce97）の計算（lua/nui/layout/utils.lua の calculate_window_position と
// lua/nui/popup/border.lua の calculate_position）に合わせた。
//
//   - 入力の枠: 中身の窓の position.row = 3 で、枠線はその 1 行上の 2 行目（0 から数える）から始まる。中身の幅は min_width = 60 と
//     入力の幅の大きい方で、左右に 1 列ずつの余白（border.padding = { 0, 1 }）と枠線を足すので、枠の全体は 64 列である。
//     横は col = "50%" で、中身の窓を (画面の幅 - 中身の幅) / 2 の列に置き、枠線と余白の 2 列だけ左から枠を始める
//   - 候補の一覧の枠: 中身の窓の position.row = 6 で、枠線は入力の枠の下の枠線のすぐ下の 5 行目から始まる。幅は 60 列に余白の 2 列を
//     足した 62 列（余白を中身の幅に入れ直す。lua/noice/ui/popupmenu/nui.lua の M.opts）で、枠の全体は入力の枠と同じ 64 列である。
//     行の数は max_height = 15 までである
//   - 題: 入力の枠の上の枠線の中央に、前後に空白を 1 つずつ付けて描く（lua/noice/config/cmdline.lua の border.text.top と、
//     nui.nvim の border.text.top_align の既定の "center"）
//
// 本体は、枠線を記号 popup.border（SymbolPopupBorder）で描く（noice.nvim は常に rounded の枠線を描く）。
// 記号が空（本体の既定）なら、ASCII の枠線（cmdlinePopupBorder）で描く。枠の中の色（StyleCommandLinePopup）は、noice.nvim と同じく
// 既定では本文と同じ（端末の既定の色）なので、枠線を描かないと枠と本文を見分けられないためである（PR #362 のレビューの推奨 1）。
// 枠線は文字と色（StyleCommandLinePopupBorder）で見分けるので、端末の背景が暗くても明るくても見分けられる。
// 行は、noice.nvim の relative = "editor" と同じく、タブの行を含めた画面の上の端から数える（タブの行を出していても、枠は画面の同じ行に出る）。

const (
	// cmdlinePopupTop は、入力の枠の上の端の行（枠線の行）の、画面の上の端から数えた番号である。
	cmdlinePopupTop = 2
	// cmdlinePopupMinWidth は、入力の枠の中の、入力を描く欄の最小の列数である（noice.nvim の cmdline_popup の size.min_width）。
	cmdlinePopupMinWidth = 60
	// cmdlinePopupMenuRows は、候補の一覧の枠に出す項目の行の数の上限である（noice.nvim の command_palette の
	// cmdline_popupmenu の size.max_height）。
	cmdlinePopupMenuRows = 15
)

// cmdlinePopupBorder は、記号 popup.border が空のときに、画面の上のポップアップのコマンドラインの枠に描く枠線である。
// 並びは SymbolPopupBorder と同じく、左上、上、右上、右、右下、下、左下、左である。
var cmdlinePopupBorder = [borderChars]string{"+", "-", "+", "|", "+", "-", "+", "|"}

// cmdlinePopupTitle は、コマンドラインの種類 kind（':'、'/'、'?'）の枠の題の記号の名前を返す（FR-216）。
func cmdlinePopupTitle(kind rune) string {
	switch kind {
	case '/':
		return SymbolCommandLineTitleSearchForward
	case '?':
		return SymbolCommandLineTitleSearchBackward
	}
	return SymbolCommandLineTitleCommand
}

// cmdlineTail は、コマンドラインの行に出す文字列 full の、末尾の後ろのカーソルの 1 列を残して width 列に収まる末尾の部分を返す。
// 収まらなければ、先頭の文字を落とす（末尾が見えるようにする）。幅は、返す部分の先頭を 0 列目として数える
// （タブ文字の幅も、そこから数える）ので、描くときも、返した部分の先頭を欄の左の端としてタブ文字の幅を決める（drawTextFrom）。
func (r *Renderer) cmdlineTail(full string, width int) string {
	s := full
	tabStop := tabStopOr(r.tabStop)
	for s != "" && StringWidth(s, tabStop)+1 > width {
		s = s[nextGlyph(s, 0, 0, tabStop).size:]
	}
	return s
}

// drawCommandLinePopup は、コマンドラインモードの入力を、画面の上の中央のポップアップの枠の中に描き、入力の末尾に置くカーソルを返す
// （FR-215）。枠の題に入力の種類の印を出し（FR-216）、補完の候補の一覧があれば、入力の枠のすぐ下に枠と同じ幅で描く（FR-217）。
// 枠を置けない（最後の行の上に枠の行が収まらない、入力の欄が 2 列も取れない）ときは、何も描かずに ok を false で返す
// （呼び出し側は最後の行に描く）。
//
// 入力は、欄の左の端を 0 列目としてタブ文字の幅を決めて描く（最後の行に描くときと同じ幅になる）。欄に収まらなければ、先頭を隠す。
// 枠の中は StyleCommandLinePopup、枠線と題は、コマンドラインなら StyleCommandLinePopupBorder、検索なら
// StyleCommandLinePopupBorderSearch で描く（noice.nvim の NoiceCmdlinePopupBorder と NoiceCmdlinePopupBorderSearch）。
// 枠の場所は、マウスの出来事の位置を解くときに、浮かべた窓と同じく何も無い所（HitNone）として残す（mouse.go の covers）。
//
// req: FR-215, FR-216
func (r *Renderer) drawCommandLinePopup(g *Grid, theme Theme, f *Frame, prefix rune) (Cursor, bool) {
	var menu *CompletionMenu
	if m := f.Completion; m != nil && m.Start >= 0 && m.Start <= len(f.CommandLine) {
		menu = m
	}
	full := string(prefix) + f.CommandLine
	tail := func(width int) (string, int) {
		s := r.cmdlineTail(full, width)
		return s, StringWidth(s, tabStopOr(r.tabStop))
	}
	return r.drawLinePopup(g, theme, StringWidth(full, tabStopOr(r.tabStop)), tail, cmdlinePopupTitle(prefix), prefix == '/' || prefix == '?', menu)
}

// drawLinePopup は、1 行の入力（前置きを含めて fullWidth 列）を、画面の上の中央のポップアップの枠の中に描き、カーソルを返す。
// fit は、入力の欄の列数を受け取り、欄に描く文字列と、その先頭から数えたカーソルの列を返す（コマンドラインなら末尾の側を残す）。
// 枠の題は記号 titleSymbol、枠線と題の色は、search なら StyleCommandLinePopupBorderSearch、そうでなければ
// StyleCommandLinePopupBorder にする。menu が nil でなければ、入力の枠のすぐ下に候補の一覧を描く（FR-217）。
// コマンドラインと検索の入力（drawCommandLinePopup）と、拡張機能が 1 行の入力を聞く問い（drawInput。FR-275）が使う。
// 枠を置けなければ、何も描かずに ok を false で返す。
func (r *Renderer) drawLinePopup(g *Grid, theme Theme, fullWidth int, fit func(width int) (string, int), titleSymbol string, search bool, menu *CompletionMenu) (Cursor, bool) {
	chars, ok := r.border()
	if !ok {
		chars = cmdlinePopupBorder
	}
	const outerH = 3 // 上の枠線、入力の行、下の枠線
	top := min(cmdlinePopupTop-r.bodyTop, r.Height-1-outerH)
	maxCW := r.Width - 4
	if top < 0 || maxCW < 2 {
		return Cursor{}, false
	}
	tabStop := tabStopOr(r.tabStop)
	cw := min(max(cmdlinePopupMinWidth, fullWidth+1), maxCW)
	iw := cw + 2 // 左右の余白を含めた枠の中の列数
	outerW := iw + 2
	left := (r.Width - outerW) / 2
	x0 := left + 1
	body := theme.attr(StyleCommandLinePopup)
	ba := theme.attr(StyleCommandLinePopupBorder)
	if search {
		ba = theme.attr(StyleCommandLinePopupBorderSearch)
	}
	row := top + 1
	clearSplit(g, row, x0, x0+iw)
	for c := range iw {
		g.set(row, x0+c, blank(body))
	}
	r.drawBorderAttr(g, ba, chars, row, x0, 1, iw)
	if title := r.symbol(titleSymbol); title != "" {
		t := " " + title + " "
		x := x0 + max((iw-StringWidth(t, tabStop))/2, 0)
		r.drawText(g, top, x, x0+iw, t, ba)
	}
	s, at := fit(cw)
	r.drawTextFrom(g, row, x0+1, x0+1+cw, s, body)
	r.hits.cover(top, left, outerH, outerW)
	if menu != nil {
		r.drawPaletteMenu(g, theme, menu, top+outerH, left, outerW, chars)
	} else {
		r.pum.valid = false
	}
	return Cursor{Row: row, Col: min(x0+1+at, x0+cw), Shape: CursorBar, Visible: true}, true
}

// drawPaletteMenu は、補完の候補の一覧 m を、top 行目から、left 列から幅 outerW 列（入力の枠と同じ幅）の枠の中に描く（FR-217）。
// 枠線 chars は StyleCommandLinePopupBorder、項目は StyleCommandLinePopup の上に、下の端の一覧（drawCompletion）と
// 同じ色（選んでいる項目は StylePopupSelected、打った語に当たる文字は StylePopupMatch、スクロールバーは StylePopupScrollbar と
// StylePopupThumb）で描く。項目の文字は、入力の文字と同じく枠の中の左の 1 列の余白の後から並べ、右の 1 列の余白にスクロールバーを描く。
// 一覧の項目を並べ始める位置（CompletionMenu.Start）は使わない（noice.nvim の command_palette も、一覧を補う語の列に合わせない）。
//
// 行の数は、項目の数、cmdlinePopupMenuRows、m.MaxHeight（0 でなければ）と、画面の下の端までに枠ごと収まる行の数の最も小さいものにする。
// 画面が低ければ、noice.nvim と同じく、ステータス行と最後の行の上にも重ねて、入る行だけ出す（PR #362 のレビューの推奨 2）。
// 収まらない項目は、下の端の一覧と同じく選んでいる項目が見えるように送り、スクロールバーを描く。1 行も取れなければ一覧を出さない。
//
// req: FR-217
func (r *Renderer) drawPaletteMenu(g *Grid, theme Theme, m *CompletionMenu, top, left, outerW int, chars [borderChars]string) {
	size := len(m.Items)
	limit := cmdlinePopupMenuRows
	if m.MaxHeight > 0 {
		limit = min(limit, m.MaxHeight)
	}
	h := min(size, limit, r.Height-top-2)
	x0, iw := left+1, outerW-2
	if size == 0 || h < 1 || iw < 3 {
		r.pum.valid = false
		return
	}
	p := completionPlace{top: top + 1, height: h, col: x0 + 1, width: iw - 2, scrollbar: h < size}
	r.drawCompletionRows(g, theme, m, p, x0, x0+iw, theme.attr(StyleCommandLinePopup))
	r.drawBorderAttr(g, theme.attr(StyleCommandLinePopupBorder), chars, p.top, x0, h, iw)
	r.hits.cover(top, left, h+2, outerW)
}
