package ui

// 一覧、木、ファインダーのポップアップの右に出す、カーソルの項目のプレビュー（基本設計 1.4 の「#288 のプレビュー」。FR-149）。
//
// 窓の幅が設定の境目（PreviewRule.MinWindowWidth）を超えるときだけ、窓を広げて右にプレビューを描く。
// 幅の規則は窓の幅だけで決め、出す場所（中央のポップアップ、端末多重化ソフトのポップアップ、分割したペイン）では分けない。
// 判定は internal/editor が PreviewRule.Layout で 1 か所で行い、出すときだけ Float.Preview を渡す。ここは渡された窓を分けて描く。

// previewSeparator は、枠線の記号（SymbolPopupBorder）が無いときに、一覧とプレビューの間に描く縦の線である。
const previewSeparator = "│"

// PreviewRule は、プレビューを出すかと、窓とプレビューの幅を決める設定の値である（internal/option の preview.* の 3 つ）。
type PreviewRule struct {
	// MinWindowWidth は、窓の幅がこの列数を超えるときだけプレビューを出す境目である（fzf-lua の flip_columns）。
	MinWindowWidth int
	// WidthPercent は、窓の幅のうちプレビューに使う百分率である。
	WidthPercent int
	// WindowWidthPercent は、プレビューを出すときの窓の幅の、画面の幅に対する百分率である（Fill の窓では読まない）。
	WindowWidthPercent int
}

// Layout は、幅 width の画面で、プレビューの欄を持つ窓にプレビューを出すかを返す。出すなら、窓の幅（枠線を含む）と、
// そのうちプレビューに使う列数も返す。fill なら（端末多重化ソフトのポップアップとペインの中の部品の client。Float.Fill）、
// 画面の全体が窓なので、width をそのまま窓の幅にする。そうでなければ、画面の幅の WindowWidthPercent まで窓を広げる。
// 窓の幅が MinWindowWidth 以下か、一覧とプレビューに分けられないほど狭いなら ok は false である。
//
// internal/editor は、同じ規則で、プレビューの中身を開くか（窓に出ないなら開かない）を決める。
//
// req: FR-149
func (p PreviewRule) Layout(width int, fill bool) (window, preview int, ok bool) {
	window, preview, ok = p.Split(width, fill)
	if !ok || window <= p.MinWindowWidth {
		return 0, 0, false
	}
	return window, preview, true
}

// Split は、Layout の、境目（MinWindowWidth）と比べない形である。出すと決めた窓（Float.Preview）の中を、一覧とプレビューに分けるのに使う。
// 出すかは internal/editor が Layout で、実際の窓の幅と同じ値から決める（端末多重化ソフトのポップアップでは、ポップアップの幅の百分率を
// 数える端末の幅）。ui が描く窓の幅（ポップアップの枠線の内側）で判定し直すと、判定が 2 か所で食い違う（PR #298 のレビューの必須 1）。
// 一覧とプレビューに分けられないほど狭ければ ok は false である。
func (p PreviewRule) Split(width int, fill bool) (window, preview int, ok bool) {
	window = width
	if !fill {
		window = width * p.WindowWidthPercent / 100
	}
	preview = window * p.WidthPercent / 100
	// 一覧の側は、枠線の 2 列と区切りの 1 列と、余白を除いて 1 列は残す。プレビューの側も余白を除いて 1 列は残す。
	if preview < 3 || window-preview < 6 {
		return 0, 0, false
	}
	return window, preview, true
}

// Preview は、窓の右に出すプレビューである（Float.Preview）。
type Preview struct {
	// ID は、プレビューの中身を見分ける番号である。替わったら、Line を窓の真ん中に置くようにスクロールし直す。
	ID uint64
	// Title は、プレビューの欄の最初の行に出す見出し（パスと行）である。
	Title string
	// View は、中身のバッファの Frame である。Text、Cursor、Decorations、Mode、Selection、Wrap を読む。
	// nil か Text が nil なら、中身を出さずに Note を出す（読めないファイル、項目にプレビューが無い）。
	View *Frame
	// Line は、色（StylePreviewLine）を付ける行である。負なら付けない。
	Line int
	// Note は、中身を出さないときに、見出しの下に出す理由である。
	Note string
	// Focused は、プレビューにフォーカスがある（キーがプレビューのバッファへ入る）ことを表す。端末のカーソルをプレビューの中に置く。
	Focused bool
}

// previewLayout は、窓 fl にプレビューを出すなら、窓の幅（枠線を含む）とプレビューの列数を返す。
// 出すかは internal/editor が決め、出すときだけ Float.Preview を渡すので、ここでは境目と比べずに分ける（PreviewRule.Split）。
// 中央に置く窓（Center）か画面の全体の窓（Fill）だけに出し、カーソルの近くの窓には出さない
// （窓を画面の幅まで広げると、カーソルの位置に結びつく窓でなくなるため）。
func (r *Renderer) previewLayout(fl *Float) (window, preview int, ok bool) {
	if fl.Preview == nil || (!fl.Center && !fl.Fill) {
		return 0, 0, false
	}
	return fl.PreviewRule.Split(r.Width, fl.Fill)
}

// drawPreviewSeparator は、一覧とプレビューの間の縦の線を、列 col の row top から h 行に描く。
func (r *Renderer) drawPreviewSeparator(g *Grid, theme Theme, top, col, h int) {
	ba, ok := theme[StylePopupBorder]
	if !ok {
		ba = theme.attr(StylePopup)
	}
	s := previewSeparator
	if chars, bordered := r.border(); bordered {
		s = chars[7] // 左の辺の文字
	}
	for i := range h {
		clearSplit(g, top+i, col, col+1)
		g.set(top+i, col, Cell{Text: s, Width: 1, Attr: ba})
	}
}

// drawPreview は、プレビュー p を、row top、列 x から w 列、h 行の範囲に描く（FR-149）。
// 最初の行に見出しを、残りの行に中身を描く。中身は窓の大きさの子の Renderer（Renderer.floatPreview）で描くので、
// 構文の色と横と縦のスクロールが効く。中身が替わったら（Preview.ID）、Line の行を真ん中に置く位置へスクロールし直し、
// Line の行の全体を StylePreviewLine の色で描く。バッファの最終行より後の行は空けておく。
// p.Focused なら、プレビューの中のカーソルを置いた端末のカーソルを ok を true にして返す。
//
// req: FR-149
func (r *Renderer) drawPreview(g *Grid, theme Theme, p *Preview, top, x, w, h int) (Cursor, bool) {
	a := theme.attr(StylePopup)
	for i := range h {
		clearSplit(g, top+i, x, x+w)
		for c := range w {
			g.set(top+i, x+c, blank(a))
		}
	}
	if h <= 0 || w < 3 {
		return Cursor{}, false
	}
	r.drawText(g, top, x+1, x+w-1, p.Title, a.Over(Attr{Bold: true}))
	rows := h - 1
	if p.View == nil || p.View.Text == nil || rows <= 0 {
		r.floatPreview = nil
		if rows > 0 {
			r.drawText(g, top+1, x+1, x+w-1, p.Note, a)
		}
		return Cursor{}, false
	}
	if r.floatPreview == nil || r.previewID != p.ID {
		r.floatPreview = &Renderer{}
		r.previewID = p.ID
		if p.Line >= 0 {
			r.floatPreview.SetScroll(p.Line-(rows-1)/2, 0)
		}
	}
	sub := r.floatPreview
	sub.Width, sub.Height = w-2, rows+2
	sub.Theme, sub.Symbols = theme, r.Symbols
	view := *p.View
	view.Tabs, view.Output, view.List, view.Choice, view.Float, view.Diff = nil, nil, nil, nil, nil, nil
	view.Message, view.HitEnter, view.LineNumbers, view.Search, view.Highlights = Message{}, false, LineNumbers{}, nil, nil
	view.Scrollbar, view.Intro, view.Band = nil, nil, nil
	if view.Mode == ModeCommandLine {
		view.Mode = ModeNormal
	}
	inner := sub.render(&view)
	la := a.Over(theme.attr(StylePreviewLine))
	n := view.Text.LineCount()
	for i := range rows {
		// 中身を折り返して描いたとき（View.Wrap。FR-156）も、表示領域の行ごとの行の番号（rowLine）で、色を付ける行と最終行より後の行を見分ける。
		line := sub.rowLine(i)
		if line < 0 || line >= n {
			continue
		}
		base := a
		if line == p.Line {
			base = la
			for c := range w {
				g.set(top+1+i, x+c, blank(base))
			}
		}
		for c := range sub.Width {
			cell := inner.Grid.At(i, c)
			cell.Attr = base.Over(cell.Attr)
			g.set(top+1+i, x+1+c, cell)
		}
	}
	if !p.Focused || !inner.Cursor.Visible {
		return Cursor{}, false
	}
	cur := inner.Cursor
	cur.Row += top + 1
	cur.Col += x + 1
	return cur, true
}
