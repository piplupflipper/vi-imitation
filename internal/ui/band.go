package ui

import "strconv"

// Band は、バッファの表示領域の上の端に重ねて描く帯である（Frame.Band。FR-237）。
// 中身は拡張機能が band/set で送ったもので、関数とクラスの見出しを窓の上端に固定する拡張機能（nvim-treesitter-context に当たる）が使う。
// どの窓に出すか、どの拡張機能の行をどの順に並べるかは internal/editor が決め、描画は並べた行をそのまま描く。
type Band struct {
	// Lines は、帯の行を上から並べたものである。空なら描かない。
	Lines []BandLine
}

// BandLine は、帯の 1 行である。
type BandLine struct {
	// Text は、行の中身である。改行を含まない（ext.BandSetParams.Validate）。
	Text string
	// Line は、行に当たるバッファの行（0 から数える）である。負なら無い。行番号の列を出していれば、この行の番号を描く。
	Line int
	// Stale は、帯を作ったときから本文が変わった（バージョン番号が違う）ことを表す。描き方は変えないが、押してもその行へ移らない
	// （HitTest が本文として解く。nvim-treesitter-context の get_source_line も、changedtick が違えば押下を本文へ渡す）。
	Stale bool
	// Spans は、行の中のバイトの範囲ごとの色である。後の範囲ほど上に重ねる。
	Spans []BandSpan
}

// BandSpan は、帯の行の中の色を付ける範囲 [Start, End)（バイト）と、色の名前である。
type BandSpan struct {
	Start, End int
	Style      string
}

// drawBand は、帯 b を、バッファの表示領域 area の上の端から描く（FR-237、FR-239）。重ねる順は layers.go の「窓の重なり」の表にある。
// nvim-treesitter-context（オーナーの nvim に入っているコミット 8decd73460d26ef0a0befb745d022dd1fc2c38b8 の
// lua/treesitter-context/render.lua と context.lua）の窓の出し方に合わせた。
//
//   - 行数は、帯の行の数と、カーソルの表示領域の中の行（上から数えた行数）の小さい方にする。帯がカーソルの行を隠さないよう、
//     上の行から削る（context.lua の calc_max_lines が max_lines を cursor - w0 で抑え、trim_scope の既定の outer が外側から削るのと同じ）。
//     カーソルが最初の行にあれば描かない。帯の下に隠れた本文の行のために、スクロールの余白は足さない
//   - 印の列と行番号の列（本文の左の列の全体）は StyleBandLineNr で塗り、行番号の列を出していれば、行に当たるバッファの行の番号を、
//     本文の行番号と同じく右に寄せて描く（render.lua の render_lno と、窓の色 TreesitterContextLineNumber）
//   - 本文の列は StyleBand で塗り、行の中身を描く。範囲の色は StyleBand の上に重ねる。行は折り返さず、行を折り返さない窓では
//     横のスクロールの位置（left）から描く（render.lua の horizontal_scroll_contexts）。タブ文字は Frame.TabStop で広げる。
//     空白の記号（FR-170）は描かない（帯の窓は 'list' の無い minimal の窓である）
//   - 最後の行には、列の全体に StyleBandBottom を重ねる（render.lua の highlight_bottom の TreesitterContextBottom）
//   - 描いた行の場所と、行ごとのバッファの行を hitState に残す（帯を押してその行へ移る。FR-238）
//
// req: FR-237, FR-239
func (r *Renderer) drawBand(g *Grid, theme Theme, f *Frame, text Text, cur cursorPlace, area textArea) {
	b := f.Band
	if b == nil || len(b.Lines) == 0 || area.rows <= 0 || area.width <= 0 {
		return
	}
	curRow, _ := r.cursorScreen(text, f, cur)
	n := min(len(b.Lines), curRow-area.top, area.rows)
	if n <= 0 {
		return
	}
	lines := b.Lines[len(b.Lines)-n:]
	base, gutter, bottom := theme.attr(StyleBand), theme.attr(StyleBandLineNr), theme.attr(StyleBandBottom)
	left := min(r.textLeft(), area.width)
	for i, l := range lines {
		row := area.top + i
		ga, ta := gutter, base
		if i == n-1 {
			ga, ta = ga.Over(bottom), ta.Over(bottom)
		}
		clearSplit(g, row, 0, area.width)
		for x := 0; x < area.width; x++ {
			a := ta
			if x < left {
				a = ga
			}
			g.set(row, x, blank(a))
		}
		r.drawBandNumber(g, row, l.Line, ga, area.width)
		r.drawBandText(g, theme, row, left, area.width, l, ta)
		line := l.Line
		if l.Stale {
			line = -1
		}
		r.hits.bandLines = append(r.hits.bandLines, line)
	}
	r.hits.band = rect{top: area.top, left: 0, h: n, w: area.width}
}

// drawBandNumber は、帯の row 行目の行番号の列に、バッファの line 行目の番号を描く。列の位置と寄せ方は drawNumber と同じである。
// 行番号の列を出していないか、line が負なら描かない。
func (r *Renderer) drawBandNumber(g *Grid, row, line int, a Attr, width int) {
	x0, w := r.signWidth(), r.numWidth
	if w <= 0 || line < 0 {
		return
	}
	s := strconv.Itoa(line + 1)
	pad := w - 1 - len(s)
	for k := range len(s) {
		if x := x0 + pad + k; x >= x0 && x < width {
			g.set(row, x, Cell{Text: s[k : k+1], Width: 1, Attr: a})
		}
	}
}

// drawBandText は、帯の行 l の中身を、row 行目の left 列目から width 列の手前まで、属性 a の上に範囲の色を重ねて描く。
// 行を折り返さない窓では、表示の列 r.left より左の文字を描かない。左の端で欠ける全角文字とタブ文字の欠けた部分は空白にする。
func (r *Renderer) drawBandText(g *Grid, theme Theme, row, left, width int, l BandLine, a Attr) {
	skip := 0
	if !r.wrap {
		skip = r.left
	}
	tabStop := tabStopOr(r.tabStop)
	col := 0
	for off := 0; off < len(l.Text); {
		gl := nextGlyph(l.Text, off, col, tabStop)
		col = gl.col + gl.width
		off += gl.size
		x := left + gl.col - skip
		if x+gl.width > width {
			break
		}
		if x+gl.width <= left {
			continue
		}
		ca := a
		for _, s := range l.Spans {
			if gl.off >= s.Start && gl.off < s.End {
				ca = ca.Over(theme.attr(s.Style))
			}
		}
		if gl.kind == glyphSpecial {
			ca = ca.Over(theme.attr(StyleSpecialKey))
		}
		switch {
		case x < left:
			// 左の端で欠ける文字は、見える部分を空白にする。
			for i := left; i < x+gl.width; i++ {
				g.set(row, i, blank(ca))
			}
		case gl.kind == glyphText:
			g.set(row, x, Cell{Text: gl.text, Width: gl.width, Attr: ca})
			if gl.width == 2 {
				g.set(row, x+1, Cell{Width: 0, Attr: ca})
			}
		default:
			for i := range gl.width {
				g.set(row, x+i, Cell{Text: gl.cell(i), Width: 1, Attr: ca})
			}
		}
	}
}

// hitBand は、row 行目の col 列目（タブの行を除いた座標）が、直前の Render が描いた帯の、バッファの行に当たる行の上なら、
// その下の本文の位置（hitText）に帯の行を添えた Hit を ok を true にして返す（FR-238）。
// 帯の行がバッファの行に当たらない（行の番号が無いか、作った後に本文が変わった）ときは ok を false にして、本文として解かせる。
//
// req: FR-238
func (r *Renderer) hitBand(row, col int) (Hit, bool) {
	b := r.hits.band
	if !b.contains(row, col) {
		return Hit{}, false
	}
	line := r.hits.bandLines[row-b.top]
	if line < 0 {
		return Hit{}, false
	}
	h := r.hitText(row, col, false)
	if h.Area != HitText {
		return Hit{}, false
	}
	h.Area, h.BandLine = HitBand, line
	return h, true
}
