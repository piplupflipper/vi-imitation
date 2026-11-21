package command

import (
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/target"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/scroll"
)

// Viewport は、画面のバッファの表示領域である。スクロールの操作（<C-e>、<C-d>、<C-f>、zt など。FR-173）が読み書きする。
// 表示の状態（どの行から描くか）は描画の側が持つので、呼び出し側（internal/editor）が State.Viewport に差し込む。
// 行の高さ（折り返した見た目の行の数）は State.ScreenLines で、余白（scroll.margin。FR-174）は設定の口（Ex.Options）で数える。
// 数え方は internal/scroll にあり、描画がカーソルを表示領域に入れるときと同じである。
type Viewport interface {
	// Top は、表示領域の最初の行の番号（0 から数える）を返す。
	Top() int
	// Skip は、最初の行を折り返した見た目の行のうち、表示領域より上に隠している行の数を返す（Vim の w_skipcol）。
	Skip() int
	// Rows は、表示領域の行数を返す。
	Rows() int
	// SetView は、表示領域の最初の行を top に、隠す見た目の行を skip にする。横の位置は変えない。
	// hold が 0 以上なら、カーソルが行 hold にある間、描画はカーソルに合わせて最初の行を決め直さない（ui.Renderer.SetScrollHeld）。
	SetView(top, skip, hold int)
}

// stateLines は、State のバッファの行の高さを、縦の位置の数え方（internal/scroll）の Lines にしたものである。
type stateLines struct {
	n      int
	height func(int) int
}

// LineCount は、scroll.Lines の LineCount である。
func (l stateLines) LineCount() int { return l.n }

// Rows は、scroll.Lines の Rows である。
func (l stateLines) Rows(line int) int { return l.height(line) }

// lineHeight は、行の高さ（表示領域の何行を使うか）を返す関数を返す。行を折り返して描くとき（s.ScreenLines）は見た目の行の数で、
// そうでなければ 1 である。
func (s *State) lineHeight() func(int) int {
	sl := s.ScreenLines
	if sl == nil || !sl.Wrap() {
		return func(int) int { return 1 }
	}
	return func(l int) int { return sl.Rows(l, s.buf.Line(l)) }
}

// scrollMargin は、カーソルの上下に残す余白の行数（設定の scroll.margin。FR-174）を返す。設定の口が無ければ（テスト）表の既定値である。
func (s *State) scrollMargin() int {
	if s.MouseDragging() {
		// マウスでドラッグして選んでいる間は、余白を残さない（Neovim 0.12.5 と同じ。mouse.go）。
		return 0
	}
	d, ok := option.Lookup(option.ScrollMargin)
	if !ok {
		return 0
	}
	if s.Ex.Options == nil {
		return d.Default.Int()
	}
	return s.Ex.Options.Get(d).Int()
}

// scrollView は、表示領域の今の位置から、縦の位置の数え方を作る。表示領域が無ければ ok は false である。
// 描画の前に続けて動いたとき（マクロの再生など）に、カーソルが表示領域の外にあれば、描画と同じくカーソルを表示領域に入れた位置から数える
// （Vim では、このときの w_topline は描画を待たずに update_topline で直される）。
func (s *State) scrollView() (v scroll.View, ok bool) {
	vp := s.Viewport
	if vp == nil {
		return v, false
	}
	n := s.buf.LineCount()
	v = scroll.View{
		Lines:  stateLines{n: n, height: s.lineHeight()},
		Height: max(vp.Rows(), 1),
		Width:  s.screenWidth(),
		Margin: s.scrollMargin(),
		Top:    min(max(vp.Top(), 0), n-1),
		Skip:   max(vp.Skip(), 0),
	}
	cur := s.cursor.Line
	if bot, _ := v.Botline(); cur < v.Top || (cur >= bot && cur != v.Top) {
		if w := v.Width; w > 0 && s.ScreenLines.BreakWidth() == 0 {
			// 印の列と行番号の列の幅は分からないので、"<<<" が本文に重なるとして数える（描画は、その幅を引いて数え直す）。
			v.Marker = 3
		}
		row := s.cursorRow()
		v.Follow(cur, row, s.cursorColumn()-row*max(v.Width, 1))
	}
	return v, true
}

// cursorColumn は、カーソルの表示の列（行を折り返すときは、見た目の行の番号 × 本文の幅を足した列）を返す。
func (s *State) cursorColumn() int {
	return s.columns().Column(s.cursor.Line, s.line(s.cursor.Line), s.cursor.Byte)
}

// screenWidth は、行を折り返して描くときの本文の幅を返す。折り返さないなら 0 である。
func (s *State) screenWidth() int {
	if sl := s.ScreenLines; sl != nil && sl.Wrap() {
		return max(sl.Width(), 1)
	}
	return 0
}

// cursorRow は、カーソルが行の中の何番目の見た目の行にあるかを返す。折り返さないなら 0 である。
func (s *State) cursorRow() int {
	w := s.screenWidth()
	if w == 0 {
		return 0
	}
	return s.columns().Column(s.cursor.Line, s.line(s.cursor.Line), s.cursor.Byte) / w
}

// setView は、数え方が決めた位置を表示領域に戻す。hold なら、カーソルがその行にある間は描画で決め直させない（Viewport.SetView）。
func (s *State) setView(v scroll.View, hold bool) {
	h := -1
	if hold {
		h = s.cursor.Line
	}
	s.Viewport.SetView(v.Top, v.Skip, h)
}

// cursorToLine は、カーソルを行 l の望みの列へ移す（Vim の coladvance(curswant)）。望みの列は変えない。
// インサートモードでは行末の位置へも置き、ビジュアルモードでは選択の端を合わせる。
func (s *State) cursorToLine(l int) {
	l = min(max(l, 0), s.lastLine())
	if s.mode == ModeInsert {
		s.cursor = pos(l, s.insertByteAt(l, s.want))
		return
	}
	s.cursor = s.clampNormal(s.clampPos(pos(l, s.columns().ByteAt(l, s.line(l), s.want))))
	if s.mode.visual() {
		s.cursor = s.visualCursor()
	}
	s.followCursor()
}

// actScrollLine は、<C-e>（down が true）と <C-y> である。表示領域を回数の行だけバッファの下（上）へ送る（FR-173）。
// Neovim 0.12.5 の scroll_redraw を写した（internal/scroll の View.ScrollRedraw）。行を折り返していても、バッファの行で数える。
//
//   - カーソルが表示領域から出たら、表示領域の端の行へ移す。余白（scroll.margin）があれば、余白の外の行へ移す
//   - 余白があってバッファの端で表示領域が動かなければ、カーソルを送る向きへ 1 行動かす
//   - カーソルの列は、j と k と同じく望みの列で決め、望みの列は変えない
//   - バッファの端で動けなくても失敗しない（Vim と同じ）
//
// req: FR-173, FR-174
func actScrollLine(down bool) actionFunc {
	return func(s *State, cmd editcmd.Command) error {
		v, ok := s.scrollView()
		if !ok {
			return failf("no viewport")
		}
		cur := v.ScrollRedraw(down, max(cmd.Count, 1), s.cursor.Line, false)
		if cur != s.cursor.Line {
			s.cursorToLine(cur)
		}
		s.setView(v, v.Margin > 0)
		return nil
	}
}

// actHalfPage は、<C-d>（forward が true）と <C-u> である。表示領域とカーソルを、Vim の 'scroll' の行数だけ送る（FR-173）。
//
// req: FR-173
func actHalfPage(forward bool) actionFunc {
	return func(s *State, cmd editcmd.Command) error {
		count := 0
		if cmd.CountGiven {
			count = cmd.Count
		}
		return s.pageScroll(forward, count, true)
	}
}

// actPage は、<C-f> と <PageDown>（forward が true）と、<C-b> と <PageUp> である。表示領域を 1 画面ずつ送る（FR-173）。
//
// req: FR-03, FR-173
func actPage(forward bool) actionFunc {
	return func(s *State, cmd editcmd.Command) error {
		return s.scrollPage(forward, max(cmd.Count, 1))
	}
}

// scrollPage は、表示領域を count 画面だけ送る（<C-f>、<C-b>、<PageDown>、<PageUp>。インサートモードの <PageDown> と <PageUp> も同じ）。
func (s *State) scrollPage(forward bool, count int) error {
	return s.pageScroll(forward, count, false)
}

// halfAmount は、<C-d> と <C-u> で送る行数（Vim の 'scroll'）を、表示領域の行数 rows に対して返す。
// 回数を付けた <C-d> と <C-u> が決めた値を、表示領域の行数が変わるまで使い、変わったら表示領域の半分（1 行より少なければ 1 行）に戻す
// （Vim の win_comp_scroll。Vim は窓ごとに持つが、本体はバッファの State ごとに持つ）。
func (s *State) halfAmount(rows int) int {
	if s.scrollAmount > 0 && s.scrollAmountRows == rows {
		return s.scrollAmount
	}
	return max(rows/2, 1)
}

// pageScroll は、Neovim 0.12.5 の pagescroll を写したもので、half なら <C-d> と <C-u>、そうでなければ <C-f> と <C-b> である。
// 行を折り返すときは、表示領域を見た目の行で数えて送り（View.ScrollSmooth）、最初の行が途中から見える位置で止まったら、行の切れ目まで送り足すか戻す。
//
//   - half: 回数（0 なら省略）があれば 'scroll' を回数（表示領域の行数で頭打ち）にする。'scroll' の見た目の行だけ送る。ただし前へは、
//     バッファの最後の行の後の "~" の行を出すほどは送らない。カーソルは、送った見た目の行の数だけ動かす（折り返すときは gj と gk、
//     そうでなければ j と k と同じ）。表示領域が動かなくても、カーソルは動かす
//   - そうでなければ: 回数 × 1 画面（View.Overlap）の見た目の行を送り、カーソルを、前へなら新しい最初の行、後ろへなら最後まで見える最後の行へ移す
//   - 余白（scroll.margin）があれば、カーソルを余白の外の行へ移す（View.CursorCorrect）
//   - 表示領域もカーソルも動かなければ失敗する。動けば、カーソルの列を望みの列で決める（Neovim の既定の 'nostartofline'）。望みの列は変えない
//
// req: FR-03, FR-156, FR-173, FR-174
func (s *State) pageScroll(forward bool, count int, half bool) error {
	v, ok := s.scrollView()
	if !ok {
		return failf("no viewport")
	}
	n := s.buf.LineCount()
	prev, prevWant := s.cursor, s.want
	cur := prev.Line
	moved := false
	at := prev // カーソルを動かした先（列を含む）。列が変わるのは、折り返すときの half だけである
	v.SmoothCursor = s.smoothCursor(prev)
	if half {
		h := v.Height
		if count > 0 {
			s.scrollAmount, s.scrollAmountRows = min(h, count), h
		}
		c := min(h, s.halfAmount(h))
		cs := c
		if forward && v.Top+1+h+c > n {
			// "~" の行を出すほどは送らない。
			rows := v.TopRows()
			if rows-c < h && v.Top < n-1 {
				rows += v.RowsBetween(v.Top+1, n-1, h+c)
			}
			if rows < h+c {
				c = rows - h
			}
		}
		if c > 0 {
			var d int
			moved, d, _ = v.ScrollSmooth(forward, c, cur)
			cs += d
			// カーソルと望みの列は、送る前に戻してから動かす（Neovim と同じ）。
			s.want = prevWant
		}
		at = s.halfPageCursor(forward, cs)
		cur = at.Line
	} else {
		c := count * v.Overlap(forward)
		moved, _, cur = v.ScrollSmooth(forward, c, cur)
		if moved {
			if forward {
				cur = v.Top
			} else {
				bot, _ := v.Botline()
				cur = bot - 1
			}
			cur = max(cur, 0)
		}
	}
	if v.Margin > 0 {
		cur = v.CursorCorrect(cur)
	}
	if !moved && at.Byte == prev.Byte && cur == prev.Line {
		return failf("cannot scroll further")
	}
	// 列は望みの列で決める（Neovim の beginline(BL_SOL | BL_FIX) の 'nostartofline'）。
	s.cursorToLine(cur)
	s.setView(v, v.Margin > 0)
	return nil
}

// smoothCursor は、ページ単位のスクロールが見た目の行で送るたびに呼ぶカーソルの補正（scroll.View.SmoothCursor）を返す。
// Neovim 0.12.5 の cursor_correct_sms を写したもので、行を折り返していて、カーソルの行が最初の行のときだけ働く。
// カーソルの表示の列が、最初の行の見えている部分から上下の余白を除いた範囲に入るように、望みの列を見た目の行の幅ずつ動かす。
// 動かした列に行が届かず、最初の行の途中から見せているときは、カーソルを次の行へ移して望みの列を 0 にする。
// start は送る前のカーソルで、カーソルの行が start の行のままならその列を、そうでなければ望みの列の文字の列を、カーソルの列とする。
// <C-f> と <C-b> では、ここで変えた望みの列で、送った後のカーソルの列が決まる（Neovim も、行の短いバッファの末尾の近くで <C-b> の列が変わる）。
func (s *State) smoothCursor(start buffer.Position) func(v *scroll.View, cur int) int {
	return func(v *scroll.View, cur int) int {
		w := s.screenWidth()
		if w == 0 || cur != v.Top {
			return cur
		}
		cols := s.columns()
		so, h := max(v.Margin, 0), v.Height
		line := s.line(cur)
		soCols, size := 0, 0
		if so != 0 {
			soCols = w + (so-1)*w
			size = cols.Column(cur, line, len(line))
		}
		switch spaceCols := (h - 1) * w; {
		case v.Top == 0 && v.Skip == 0:
			soCols = 0
		case soCols > spaceCols/2:
			soCols = spaceCols / 2
		}
		for soCols > size && soCols-w >= w {
			soCols -= w
		}
		if soCols >= w && soCols > size {
			soCols -= w
		}
		skipcol := v.Skip * w
		overlap := 0
		if skipcol != 0 && s.ScreenLines.BreakWidth() == 0 {
			overlap = 3 // 最初の行の左の端の "<<<"
		}
		top := skipcol + overlap
		if soCols != 0 {
			top = skipcol + soCols
		}
		bot := skipcol + w + (h-1)*w - soCols
		b := start.Byte
		if cur != start.Line {
			b = cols.ByteAt(cur, line, s.want)
		}
		vc := cols.Column(cur, line, b)
		col := vc
		if col < top {
			if col < w {
				col += w
			}
			for col < top {
				col += w
			}
		} else {
			for col >= bot {
				col -= w
			}
		}
		if col == vc {
			return cur
		}
		s.want = col
		if end := cols.Column(cur, line, len(line)); end <= col && skipcol > 0 && cur < s.lastLine() {
			// 望みの列に届かず、カーソルが最初の行の見えている部分に入らないときは、次の行へ移す。
			if nb := cols.ByteAt(cur, line, col); cols.Column(cur, line, nb) < skipcol+overlap {
				s.want = 0
				return cur + 1
			}
		}
		return cur
	}
}

// halfPageCursor は、<C-d> と <C-u> で、カーソルを見た目の行で cs 行だけ動かした先を返す（カーソルは動かさない）。
// 行を折り返すときは gj と gk（Neovim の nv_screengo）、そうでなければ j と k と同じに動き、バッファの端で止まる。
// 望みの列は、gj と gk が動いた後の望みの列にする。
func (s *State) halfPageCursor(forward bool, cs int) buffer.Position {
	if cs <= 0 {
		return s.cursor
	}
	if s.screenWidth() > 0 {
		id := "display-up"
		if forward {
			id = "display-down"
		}
		if f, ok := target.LookupMotion(id); ok {
			res, err := f(s.buf, target.Request{Cursor: s.cursor, Count: cs, Want: s.want, Columns: s.Columns, Screen: s.ScreenLines})
			if err == nil {
				s.want = res.Want
				return res.Pos
			}
		}
		return s.cursor
	}
	l := s.cursor.Line - cs
	if forward {
		l = s.cursor.Line + cs
	}
	return pos(min(max(l, 0), s.lastLine()), s.cursor.Byte)
}

// scrollCursorKind は、z で始まるスクロールの操作（zt、zz、zb と、z<CR>、z.、z-）のどれかである。
type scrollCursorKind int

const (
	scrollCursorTop scrollCursorKind = iota
	scrollCursorCenter
	scrollCursorBottom
)

// actScrollCursor は、zt、zz、zb（firstNonBlank が false）と、z<CR>、z.、z-（true）である。カーソルの行を、表示領域の上の端、
// 中ほど、下の端に置くように表示領域を動かす（FR-173）。Neovim 0.12.5 の nv_zet を写した。
//
//   - 回数があり、カーソルの行と違えば、カーソルをその行（最後の行より大きければ最後の行）へ移し、移る前の位置をジャンプリストに記録する。
//     列は、移る前のバイトの位置をその行に丸めた位置にする（Neovim の check_cursor_col）
//   - z<CR>、z.、z- は、カーソルを行の最初の空白でない文字へ移し、望みの列をその列にする
//   - 上の端と下の端には、余白（scroll.margin）を残す。中ほど（zz）は、バッファの末尾の近くでも "~" の行を数えて中ほどに置く
//
// req: FR-173, FR-174
func actScrollCursor(kind scrollCursorKind, firstNonBlank bool) actionFunc {
	return func(s *State, cmd editcmd.Command) error {
		v, ok := s.scrollView()
		if !ok {
			return failf("no viewport")
		}
		startLine := s.cursor.Line
		if cmd.CountGiven && cmd.Count > 0 && cmd.Count-1 != s.cursor.Line {
			if s.Navigator != nil {
				s.Navigator.MarkJump(s.cursor)
			}
			l := min(cmd.Count, s.buf.LineCount()) - 1
			s.cursor = s.clampNormal(s.clampPos(pos(l, s.cursor.Byte)))
			if s.mode.visual() {
				s.cursor = s.visualCursor()
			}
			s.followCursor()
		}
		if firstNonBlank {
			s.cursor = s.firstNonBlankPos(s.cursor.Line)
			s.followCursor()
			s.updateWant()
		}
		cur := s.cursor.Line
		// oldBot は、Neovim が描く前に持っている w_botline に当たる行である。zt と zz は w_botline を数え直さないので、
		// 動かす前の表示領域のものになる。zb は、最初の行を変えたときだけ、カーソルの行の次の行にする（scroll_cursor_bot の set_topbot）。
		oldBot, _ := v.Botline()
		oldTop := v.Top
		switch kind {
		case scrollCursorTop:
			row := s.cursorRow()
			v.CursorTop(cur, row, s.cursorColumn()-row*max(v.Width, 1), 0, true)
		case scrollCursorCenter:
			v.CursorHalfway(cur, true, false)
		case scrollCursorBottom:
			v.CursorBot(cur, 0, true)
			if v.Top != oldTop {
				oldBot = cur + 1
			}
		}
		// 決めた位置は、描いた後の最後まで見える行の次の行が oldBot と違うときだけ、描画で余白を数え直させる。
		// Neovim 0.12.5 も、zt、zz、zb は VALID_TOPLINE を立て、描いた後の w_botline が描く前と違うときだけ update_topline で
		// 数え直す（src/nvim/drawscreen.c の win_update の末尾）。100 行のファイルで so=5 の 95zt の後の zz は、最後まで見える行の次の行が
		// 変わるので数え直して最初の行を 90 行目に戻し、G の後の zz は変わらないので 96 行目のままにする（2026-11-21 に確かめた。PR #338 のレビューの必須 1）。
		// 回数でカーソルの行を変えたときは、最後まで見える行の次の行によらず数え直させる。Neovim 0.12.5 でも、so=5 で G の後の 99zz は
		// 最初の行を 94 行目にする（数え直さないと 95 行目。表示領域が 7 行なら 96 行目。zt、zb、z<CR>、z.、z- も同じ。PR #338 の再レビューの必須 1）。
		bot, _ := v.Botline()
		s.setView(v, bot == oldBot && s.cursor.Line == startLine)
		return nil
	}
}
