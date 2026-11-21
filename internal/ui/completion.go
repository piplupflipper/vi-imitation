package ui

import (
	"strings"
	"unicode/utf8"
)

// 補完の候補の一覧（ポップアップの一覧。FR-197。基本設計 7.4）の描き方。
//
// 一覧の中身（項目、選んでいる項目、項目を並べ始めるコマンドラインの中の位置）は internal/editor が持ち、
// 描く位置と大きさはここで決める。コマンドラインの一覧は、Neovim 0.12.5 の wildoptions=pum（popupmenu.c の pum_display）と
// 同じく、コマンドラインの行の真上に、項目を並べ始める列から下へ詰めて描く（drawCompletion）。
// コマンドラインを画面の上のポップアップに出すとき（Frame.CommandLinePopup。FR-215）は、同じ中身を入力の枠のすぐ下に描く
// （cmdlinepopup.go の drawPaletteMenu。FR-217）。どちらも、項目の行は drawCompletionRows で描き、中身の作り方は変えない。

// CompletionMenu は、補完の候補の一覧である（Frame.Completion。FR-197）。
type CompletionMenu struct {
	// ID は、一覧を見分ける番号である。替わったら、一覧の中のスクロールの位置を先頭に戻す（Neovim の pum_clear）。
	ID uint64
	// Items は、項目に出す文字列である。1 項目を 1 行に出す。
	Items []string
	// Selected は、選んでいる項目の添字である。負なら選んでいない（打った語のまま）。その行を StylePopupSelected で描く。
	Selected int
	// Start は、Frame.CommandLine の中の、項目を並べ始める位置のバイト位置である。一覧の項目の文字は、コマンドラインの行の
	// この位置の文字と同じ列から始まり、その 1 列左に空白を 1 つ置く（Neovim と同じ）。
	Start int
	// Match は、打った語である。項目の先頭がこれと大文字と小文字を区別せずに同じなら、その部分を StylePopupMatch で重ねて描く
	// （Neovim 0.12.5 の PmenuMatch）。空なら重ねない。
	Match string
	// MaxHeight は、出す行の数の上限である（設定の completionMenu.height。Vim の 'pumheight'）。0 なら画面に収まるだけ出す。
	MaxHeight int
}

// completionMinWidth は、一覧の項目の欄の最小の列数である（Vim と Neovim の 'pumwidth' の既定の 15）。
const completionMinWidth = 15

// completionTrunc は、欄に収まらない項目の右の端に描く記号である（Neovim 0.12.5 の 'fillchars' の trunc の既定）。
const completionTrunc = ">"

// pumState は、Renderer が描いた補完の一覧の、描画をまたいで保つ状態である。
type pumState struct {
	// id は、最後に描いた一覧の ID である。
	id uint64
	// first は、一番上の行に出す項目の添字である（Neovim の pum_first）。
	first int
	// selected、height、size は、first を最後に決めたときの選んでいる項目、行の数、項目の数である。同じなら決め直さない。
	selected, height, size int
	// valid は、前の描画で一覧を描いたかである。
	valid bool
}

// CompletionRows は、直前の Render が描いた補完の候補の一覧の行の数を返す。描いていなければ 0 を返す。
// <PageDown> と <PageUp> が送る候補の数（internal/editor）に使う（Neovim の pum_get_height）。
func (r *Renderer) CompletionRows() int {
	if !r.pum.valid {
		return 0
	}
	return r.pum.height
}

// completionPlace は、一覧の場所である。
type completionPlace struct {
	top, height int // 最初の行と行の数
	col, width  int // 項目の欄の最初の列と列数（左の空白と右のスクロールバーを除く）
	scrollbar   bool
}

// placeCompletion は、一覧の場所を決める。row は一覧の真下の行（コマンドラインの行）、col は項目を並べ始める列である。
// 出せないとき（項目が無い、1 行しか取れないのに 2 項目以上ある）は ok が false である。
// 縦と横の決め方は、Neovim 0.12.5 の pum_compute_vertical_placement と pum_compute_horizontal_placement の、
// コマンドラインで枠線の無いときと同じである。
//
// req: FR-197
func (r *Renderer) placeCompletion(m *CompletionMenu, row, col int) (completionPlace, bool) {
	size := len(m.Items)
	var p completionPlace
	if size == 0 || row <= 0 {
		return p, false
	}
	p.top, p.height = row-size, size
	if row < size {
		p.top, p.height = 0, row
	}
	if m.MaxHeight > 0 && p.height > m.MaxHeight {
		p.top += p.height - m.MaxHeight
		p.height = m.MaxHeight
	}
	if p.height < 1 || (p.height == 1 && size > 1) {
		return p, false
	}
	p.scrollbar = p.height < size
	sb := 0
	if p.scrollbar {
		sb = 1
	}
	tabStop := tabStopOr(r.tabStop)
	base := 0
	for _, it := range m.Items {
		base = max(base, StringWidth(it, tabStop))
	}
	width := base
	if width < completionMinWidth {
		width = completionMinWidth
	} else {
		width++ // 項目の後ろに 1 列を空ける（set_pum_width_aligned_with_cursor の end_padding）
	}
	p.col, p.width = col, width
	avail := r.Width - col - sb
	switch {
	case avail >= width:
	case avail > completionMinWidth:
		p.width = avail
	case avail+col > completionMinWidth:
		p.width = completionMinWidth + 1
		p.col = r.Width - p.width - sb
	default:
		p.col, p.width = 0, r.Width-sb
	}
	if p.width <= 0 {
		return p, false
	}
	return p, true
}

// scrollCompletion は、選んでいる項目 sel が見えるように、一番上の行の項目 first を決め直して返す。
// Neovim 0.12.5 の pum_set_selected と同じ決め方（選んでいる項目の上と下に、行の数の半分と 3 の小さい方の行を残す）である。
func scrollCompletion(first, sel, height, size int) int {
	if sel >= 0 && sel < size {
		context := height / 2
		scrollOffset := sel - height
		switch {
		case first > sel-4:
			if first > sel-2 {
				first -= height - 2
				if first < 0 {
					first = 0
				} else if first > sel {
					first = sel
				}
			} else {
				first = sel
			}
		case first < scrollOffset+5:
			if first < scrollOffset+3 {
				first = max(first+height-2, scrollOffset+1)
			} else {
				first = scrollOffset + 1
			}
		}
		context = min(context, 3)
		if height > 2 {
			if first > sel-context {
				first = max(sel-context, 0)
			} else if first < sel+context-height+1 {
				first = sel + context - height + 1
			}
		}
		first = min(first, size-height)
	}
	return max(min(first, size-height), 0)
}

// drawCompletion は、一覧 m を、row 行目（コマンドラインの行）の真上に、項目を col 列から並べて描く。
// 選んでいる項目は StylePopupSelected、ほかは StylePopup で描き、打った語に当たる先頭を StylePopupMatch で重ねる。
// 項目が行の数より多ければ、右の端の列にスクロールバー（StylePopupScrollbar の溝と StylePopupThumb のつまみ）を描く。
// 欄に収まらない項目は、収まる所まで描いて右の端に completionTrunc を描く。
//
// req: FR-197
func (r *Renderer) drawCompletion(g *Grid, theme Theme, m *CompletionMenu, row, col int) {
	p, ok := r.placeCompletion(m, row, col)
	if !ok {
		r.pum.valid = false
		return
	}
	left := p.col
	if p.col > 0 {
		left = p.col - 1 // 項目の左の空白
	}
	right := p.col + p.width
	if p.scrollbar {
		right++
	}
	r.drawCompletionRows(g, theme, m, p, left, right, theme.attr(StylePopup))
	r.hits.cover(p.top, left, p.height, right-left)
}

// drawCompletionRows は、一覧 m の項目を、場所 p に描く（drawCompletion と drawPaletteMenu が使う）。
// 各行の left 列から right 列の手前までを、選んでいる項目なら StylePopupSelected、ほかは normal の空白で塗ってから、
// 項目の文字を p の欄に描く。項目が行の数より多ければ、欄の右の列（p.col + p.width）にスクロールバーを描く。
// 一覧の送り方（一番上の行の項目）は、描画をまたいで r.pum に保つ。
// マウスの出来事の位置を解くための一覧の場所（r.hits.cover）は残さない。呼び出し側が、枠線や左の空白を含めた一覧の全体の場所を残す
// （残さないと、一覧の上の押下が下の本文へ通り抜ける。mouse.go の covers）。
func (r *Renderer) drawCompletionRows(g *Grid, theme Theme, m *CompletionMenu, p completionPlace, left, right int, normal Attr) {
	size := len(m.Items)
	st := &r.pum
	if !st.valid || st.id != m.ID {
		*st = pumState{id: m.ID, selected: -2}
	}
	if st.selected != m.Selected || st.height != p.height || st.size != size {
		st.first = scrollCompletion(st.first, m.Selected, p.height, size)
		st.selected, st.height, st.size = m.Selected, p.height, size
	}
	st.valid = true
	first := max(min(st.first, size-p.height), 0)
	thumbPos, thumbHeight := 0, 1
	if p.scrollbar {
		thumbHeight = max(p.height*p.height/size, 1)
		rng := size - p.height
		thumbPos = (first*(p.height-thumbHeight) + rng/2) / rng
	}
	selected := theme.attr(StylePopupSelected)
	match := theme.attr(StylePopupMatch)
	for i := range p.height {
		idx := first + i
		y := p.top + i
		a := normal
		if idx == m.Selected {
			a = selected
		}
		clearSplit(g, y, left, right)
		for x := left; x < right; x++ {
			g.set(y, x, blank(a))
		}
		r.drawCompletionItem(g, y, p, m.Items[idx], m.Match, a, a.Over(match))
		if p.scrollbar {
			sa := theme.attr(StylePopupScrollbar)
			if i >= thumbPos && i < thumbPos+thumbHeight {
				sa = theme.attr(StylePopupThumb)
			}
			g.set(y, p.col+p.width, blank(sa))
		}
	}
}

// drawCompletionItem は、項目 text を row 行目の欄 p に描く。先頭が leader と大文字と小文字を区別せずに同じなら、その部分を ma で描く。
// 欄に収まらなければ、右の端の列を completionTrunc にする（右の端が全角文字の右半分なら、左半分を空白にする）。
func (r *Renderer) drawCompletionItem(g *Grid, row int, p completionPlace, text, leader string, a, ma Attr) {
	tabStop := tabStopOr(r.tabStop)
	limit := p.col + p.width
	matched := 0
	if leader != "" && len(text) >= len(leader) && strings.EqualFold(text[:len(leader)], leader) && utf8.ValidString(text[:len(leader)]) {
		matched = len(leader)
	}
	trunc := StringWidth(text, tabStop) > p.width
	end := r.drawText(g, row, p.col, limit, text[:matched], ma)
	if end == p.col+StringWidth(text[:matched], tabStop) {
		r.drawText(g, row, end, limit, text[matched:], a)
	}
	if trunc && p.width > 0 {
		x := limit - 1
		if p.width > 1 && g.At(row, x).Width == 0 {
			g.set(row, x-1, blank(a))
		}
		g.set(row, x, Cell{Text: completionTrunc, Width: 1, Attr: a})
	}
}
