package ui

// 長い行の折り返し（FR-156。基本設計の「行の折り返し」）。
//
// Frame.Wrap が true なら、バッファの行を本文の幅（textWidth）で見た目の行に分けて描く（Vim の 'wrap'。'linebreak' はオフで、文字の切れ目で分ける）。
// 分け方は wrapper の 1 か所で決め、描画（drawWrapped）、カーソルの位置（placeCursor）、スクロール（scroll.go）、gj と gk（WrapLines）が使う。
// 行番号の列、印の列、構文と診断と検索の一致と範囲の色、選択の色は、見た目の行に分けた後の表示領域の行（viewRow）と、
// バイトの範囲（span）の上に載るので、折り返しのための分岐を持たない。
//
// 見せ方は Neovim 0.12.5 の既定（'display' は lastline、'showbreak' は空、'smoothscroll' はオフ）に合わせた。2026-11-20 に tmux の
// 41 列の画面で、半角の行、全角の行、タブを含む行、画面より長い行を確かめた。
//
//   - 全角文字が見た目の行の最後の 1 列に入らなければ、その列に ">"（StyleNonText）を出し、文字を次の見た目の行へ送る
//   - タブと ^A、<80> の形の表記は、見た目の行の右の端で分けて、残りを次の見た目の行に描く
//   - 続きの見た目の行の頭には、記号 SymbolShowBreak（既定は空）を StyleNonText で描き、その右から文字を描く（Vim の 'showbreak'）
//   - 行番号の列と印の列は、行の最初の見た目の行にだけ出し、続きの行は空ける（行番号の列は StyleLineNr の空白で埋める）
//   - 表示領域の最後に収まりきらない行は、入るところまで描き、最後の行の右の 3 列に "@@@"（StyleNonText）を出す。
//     ただし、その行が表示領域の最初の行なら出さない（nvim 0.12.5 で、画面より長い行の先頭を最初の行にしたときに出なかった）
//   - 表示領域より高い行は、カーソルの見た目の行が入るように、行の途中の見た目の行から描く（Vim の w_skipcol。Renderer.skip）。
//     そのときは、表示領域の最初の行の左の 3 列に "<<<"（StyleNonText）を出す（nvim は行番号の列の上にも描く）
//   - インサートモードで、ちょうど見た目の行の幅で終わる行の行末にいるカーソルは、次の見た目の行の左の端に置く（nvim と同じく、
//     行の見た目の行の数は増やさない）
//   - 折り返すときは横にスクロールしない
//
// 折り返すときの表示の列は、Vim の vcol と同じく「見た目の行の番号 × 本文の幅 + 見た目の行の中の列」で数える（wrapper.vcolAt）。
// 全角文字を送った ">" の列と、続きの行の記号の列も数える。タブの幅、望みの列（WrapLines の Column と ByteAt。internal/command の
// State.Columns に差し込む）、ルーラーの表示の列は、どれもこの列で数える。">" の列を数えないと、その後のタブの幅が nvim と 1 列ずれ、
// gj と gk が見た目の行を飛ばす（PR #310 のレビューの必須 1、3）。折り返さないときは、今までどおり行の先頭からの表示の列で数える。

const (
	// lastLineMark は、表示領域の最後に収まりきらない行の、最後の行の右の端に出す印である（Vim の 'display' の lastline。'fillchars' の lastline の既定）。
	lastLineMark = "@@@"
	// skipMark は、行の途中の見た目の行から描くときに、表示領域の最初の行の左の端に出す印である（Neovim の w_skipcol の "<<<"）。
	skipMark = "<<<"
)

// wrapper は、バッファの 1 行を見た目の行に分ける規則である。
type wrapper struct {
	width int // 見た目の行の列数（本文の幅）。1 以上
	sbw   int // 続きの見た目の行の頭に描く記号の列数。width-2 以下（全角文字を 1 つは置ける）
	// rule は、文字を列に並べる規則（タブ文字の幅と、空白の記号。whitespace.go）である。
	rule lineRule
}

// wrapPos は、行を折り返したときのセルの位置である。row は行の見た目の行のうち何番目か（0 から数える）、
// x はその見た目の行の左の端（本文の左の端）からの列である。続きの見た目の行は、記号の列（sbw）から文字を置く。
type wrapPos struct{ row, x int }

// wrapper は、直前の layout の本文の幅と、記号の設定と、描いている Frame の規則（r.rule）で、行の分け方を返す。
func (r *Renderer) wrapper() wrapper {
	w := max(r.textWidth(), 1)
	sbw := StringWidth(r.symbol(SymbolShowBreak), DefaultTabStop)
	if sbw > w-2 {
		sbw = 0
	}
	return wrapper{width: w, sbw: sbw, rule: r.rule}
}

// rowStart は、見た目の row 行目の、文字を置き始める列を返す。
func (w wrapper) rowStart(row int) int {
	if row == 0 {
		return 0
	}
	return w.sbw
}

// nextRow は、p を次の見た目の行の、文字を置き始める列へ進める。
func (w wrapper) nextRow(p *wrapPos) {
	p.row++
	p.x = w.sbw
}

// place は、文字 gl を p から置き、文字の最初のセルの位置を first で返して、p を文字の次のセルへ進める。
// 次のセルが見た目の行の右の端を越えても、次の文字を置くまでは次の見た目の行へ進めない。
// このため、置いた後の p.row は、文字の最後のセルの見た目の行である。
//
// 全角文字が見た目の行に収まらなければ次の見た目の行へ送り、送る前の位置（">" を出す位置）を pad で返す（padded）。
// 見た目の行の頭でも収まらない（幅が 1 列しかない）ときは送らずに置き、右の端からはみ出させる（描くときは ">" にする）。
// タブと ^A などの表記は、セルごとに置き、右の端で分ける。
func (w wrapper) place(p *wrapPos, gl glyph) (first, pad wrapPos, padded bool) {
	if gl.kind == glyphText {
		switch {
		case gl.width == 2 && p.x+2 > w.width && p.x > w.rowStart(p.row):
			pad, padded = *p, p.x < w.width
			w.nextRow(p)
		case p.x >= w.width:
			w.nextRow(p)
		}
		first = *p
		p.x += gl.width
		return first, pad, padded
	}
	for i := range gl.width {
		if p.x >= w.width {
			w.nextRow(p)
		}
		if i == 0 {
			first = *p
		}
		p.x++
	}
	return first, pad, padded
}

// eol は、行の最後の文字を置いた後の p から、行末の位置（行の区切りを表すセル）を返す。
// 見た目の行の右の端を越えていれば、次の見た目の行の左の端にする。
func (w wrapper) eol(p wrapPos) wrapPos {
	if p.x >= w.width {
		w.nextRow(&p)
	}
	return p
}

// vcolAt は、次のセルを p から置くときの、そのセルの表示の列（Vim の vcol）を返す。
// 表示の列は「見た目の行の番号 × 本文の幅 + 見た目の行の中の列」で、">" の列と続きの行の記号の列も数える。
// p が見た目の行の右の端を越えていれば、次の見た目の行の、文字を置き始める列の表示の列にする。
// タブの幅（nextGlyph の列）はこの列で決める（nvim 0.12.5 と同じ）。
func (w wrapper) vcolAt(p wrapPos) int {
	if p.x >= w.width {
		return (p.row+1)*w.width + w.sbw
	}
	return p.row*w.width + p.x
}

// vcol は、セルの位置 p の表示の列を返す。p.x が本文の幅に等しい（行末の位置が右の端を越えた）ときも、そのまま数える。
func (w wrapper) vcol(p wrapPos) int {
	return p.row*w.width + p.x
}

// next は、走査 wk の次の文字を、p から置いたときの配置を返す（タブの幅は、置く位置の表示の列で決める）。
func (w wrapper) next(wk *lineWalk, p wrapPos) glyph {
	return wk.next(w.vcolAt(p))
}

// rows は、n 行目（テキスト s）を折り返した見た目の行の数（1 以上）を返す。空の行は 1 行である。
// 行末の位置が次の見た目の行に出る（ちょうど幅で終わる）ときも、その行は数えない。
// ただし行末の記号を描くとき（FR-170）は、行末の位置も 1 セルとして数える（lastRow）。
// inline の仮想テキストは本文と同じく折り返して数え、eol と overlay の仮想テキストは数えない（FR-233）。
//
// req: FR-233
func (w wrapper) rows(n int, s string) int {
	p := wrapPos{}
	wk := w.rule.walk(n, s)
	for wk.more() {
		w.place(&p, w.next(&wk, p))
	}
	return w.lastRow(p) + 1
}

// lastRow は、行の最後の文字を置いた後の p から、行の最後の見た目の行の番号を返す。
// 行末の記号を描くとき（lineRule.eolCell）は、行末の位置（eol）の見た目の行にする。Vim の plines_win_nofold も、'list' で eol の記号があれば
// 行末の 1 列を足して数える（2026-11-21 に nvim 0.12.5 と Vim 9.1 で、20 列の画面のちょうど 20 文字の行が、行末の記号を 2 行目に出して
// 2 行を使うことを確かめた）。
func (w wrapper) lastRow(p wrapPos) int {
	if w.rule.eolCell() {
		return w.eol(p).row
	}
	return p.row
}

// column は、n 行目（テキスト s）の byteOff の文字の表示の列（vcol）を返す。タブは最後のセルの列（Vim のノーマルモードのカーソルの列）、
// ほかの文字は最初のセルの列である。全角文字を次の見た目の行へ送ったときは、送った先の列である。
// byteOff が行の長さ以上なら、最後の文字の次の列（Vim の linetabsize）を返す。
// inline の仮想テキストの扱いは Columns.Column と同じである（文字の前の仮想テキストは数え、行末の位置の仮想テキストは数えない）。
//
// req: FR-232
func (w wrapper) column(n int, s string, byteOff int) int {
	p := wrapPos{}
	wk := w.rule.walk(n, s)
	for wk.more() {
		gl := w.next(&wk, p)
		first, _, _ := w.place(&p, gl)
		switch {
		case gl.virt != 0:
			if gl.off >= len(s) && byteOff >= len(s) {
				return w.vcol(first)
			}
		case byteOff < gl.off+gl.size:
			if gl.kind == glyphTab {
				return w.vcol(p) - 1
			}
			return w.vcol(first)
		}
	}
	return w.vcol(p)
}

// byteAt は、n 行目（テキスト s）で表示の列 col にある文字の位置を返す。列が文字の手前の ">" の列か、続きの行の記号の列なら、
// その後の文字である（Vim の coladvance と同じく、送った全角文字と記号の列は、その後の文字の列として数える）。
// inline の仮想テキストの列も、その後の文字の位置（行末の位置の仮想テキストなら最後の文字）を返す（Columns.ByteAt と同じ）。
// col が行の最後の文字の列より右なら最後の文字を、空の行では 0 を返す。
//
// req: FR-232
func (w wrapper) byteAt(n int, s string, col int) int {
	p := wrapPos{}
	wk := w.rule.walk(n, s)
	last := 0
	for wk.more() {
		gl := w.next(&wk, p)
		w.place(&p, gl)
		if col < w.vcol(p) {
			if gl.virt == 0 || gl.off < len(s) {
				return gl.off
			}
			return last
		}
		if gl.virt == 0 {
			last = gl.off
		}
	}
	return last
}

// cursor は、n 行目（テキスト s）の byteOff の文字にカーソルを置く位置を返す。tabLast なら、タブの最後のセルにする（ノーマルモード）。
// byteOff が行の長さ以上なら、行末の位置（eol）を返す。inline の仮想テキストの扱いは cursorRows の doc にある。
func (w wrapper) cursor(n int, s string, byteOff int, tabLast bool) wrapPos {
	_, p := w.cursorAt(n, s, byteOff, tabLast)
	return p
}

// cursorAt は、cursor と同じ位置と、その文字の最初のバイトの位置（行末なら行の長さ）を返す。
func (w wrapper) cursorAt(n int, s string, byteOff int, tabLast bool) (int, wrapPos) {
	off, p, _ := w.cursorRows(n, s, byteOff, tabLast)
	return off, p
}

// cursorRows は、cursorAt の結果に加えて、行の見た目の行の数（rows）も、行を 1 回だけ数えて返す。
// カーソルの文字の前に inline の仮想テキストがあれば、tabLast（ノーマルモード）なら文字の上に、そうでなければ（インサートモード）
// 仮想テキストの最初の文字の位置に置く。行末の位置（byteOff が行の長さ以上）も、行末の位置の仮想テキストの前に置く（FR-232）。
//
// req: FR-232
func (w wrapper) cursorRows(n int, s string, byteOff int, tabLast bool) (at int, cur wrapPos, rows int) {
	p := wrapPos{}
	at = -1
	var gap wrapPos
	gapped := false
	wk := w.rule.walk(n, s)
	for wk.more() {
		gl := w.next(&wk, p)
		first, _, _ := w.place(&p, gl)
		if gl.virt != 0 {
			if !gapped {
				gap, gapped = first, true
			}
			if at < 0 && gl.off >= len(s) && byteOff >= len(s) {
				at, cur = len(s), gap
			}
			continue
		}
		if at < 0 && byteOff < gl.off+gl.size {
			at, cur = gl.off, first
			switch {
			case !tabLast && gapped:
				cur = gap
			case tabLast && gl.kind == glyphTab:
				cur = wrapPos{row: p.row, x: p.x - 1}
			}
		}
		gapped = false
	}
	if at < 0 {
		at, cur = len(s), w.eol(p)
	}
	return at, cur, w.lastRow(p) + 1
}

// span は、n 行目（テキスト s）のうち、見た目の k0 行目から k1 行目の手前までに掛かる文字のバイトの範囲 [from, to) を返す。
// 検索の一致を集める範囲（searchSpans）に使う。
func (w wrapper) span(n int, s string, k0, k1 int) (from, to int) {
	p := wrapPos{}
	from = -1
	wk := w.rule.walk(n, s)
	for wk.more() {
		gl := w.next(&wk, p)
		first, _, _ := w.place(&p, gl)
		if first.row >= k1 {
			return max(from, gl.off), gl.off
		}
		if gl.virt == 0 && from < 0 && p.row >= k0 {
			from = gl.off
		}
	}
	if from < 0 {
		from = len(s)
	}
	return from, len(s)
}

// WrapLines は、行を折り返して描くときの見た目の行の数え方と表示の列の数え方である（gj と gk。FR-157）。Renderer.WrapLines で作る。
// internal/command の State.ScreenLines（target.ScreenLines）と、折り返すときの State.Columns（target.Columns）に使う
// （internal/editor がつなぐ）。internal/command とは同じ層なので、インターフェースを import せずに、同じ形のメソッドを持たせる。
//
// 列は、描画と同じ wrapper.vcolAt で数える表示の列（Vim の vcol）で、全角文字を送った ">" の列と続きの行の記号の列も数える。
// このため、見た目の行の n 番目の行の左の端の列は、どの行でも n × Width である（Vim の nv_screengo が前提にする数え方）。
type WrapLines struct {
	w wrapper
}

// WrapLines は、直前の Render の本文の幅と、行の文字の並べ方 l（タブ文字の幅と、空白の記号を描くか。描く Frame の LineLayout と同じ値）で、
// 行を折り返す数え方を返す。画面の大きさがまだ届いていなければ、幅を 1 列として数える。
// 並べ方を引数で受けるのは、次の Render の前に設定（tab.width、whitespace.show）が変わっても、使う側が今の値で数えられるようにするためである（FR-161）。
func (r *Renderer) WrapLines(l LineLayout) WrapLines {
	w := r.wrapper()
	w.rule = newLineRule(l, r.symbol)
	return WrapLines{w: w}
}

// Width は、見た目の行の列数（本文の幅）を返す。
func (l WrapLines) Width() int { return l.w.width }

// BreakWidth は、続きの見た目の行の頭に描く記号（SymbolShowBreak）の列数を返す。付けないなら 0 である。
func (l WrapLines) BreakWidth() int { return l.w.sbw }

// Rows は、n 行目（テキスト line）を折り返した見た目の行の数（1 以上）を返す。n は、その行の仮想テキスト（LineLayout.Virtual）を引く行の番号である。
func (l WrapLines) Rows(n int, line string) int { return l.w.rows(n, line) }

// Column は、n 行目（テキスト line）の byteOff の位置の文字の表示の列を返す。タブはタブが占める最後の列、ほかの文字は最初の列である
// （Columns.Column と同じ）。byteOff が行の長さ以上なら、最後の文字の次の列を返す。
func (l WrapLines) Column(n int, line string, byteOff int) int { return l.w.column(n, line, byteOff) }

// ByteAt は、n 行目（テキスト line）で表示の列 col を占める文字の位置を返す。">" の列と続きの行の記号の列は、その後の文字の列として数える。
// col が行の最後の文字より右なら最後の文字を、空の行では 0 を返す（Columns.ByteAt と同じ）。
func (l WrapLines) ByteAt(n int, line string, col int) int { return l.w.byteAt(n, line, max(col, 0)) }

// viewRow は、表示領域の 1 行に描く中身（見た目の行）である（Renderer.view）。
// 行番号の列、印の列、本文は、この行ごとの中身から描くので、折り返すかどうかで描き方を分けない。
type viewRow struct {
	// line は、描くバッファの行の番号である。見比べの詰め物の行なら -1 で、バッファの行数以上なら最終行より後の "~" の行である。
	line int
	// k は、行を折り返した見た目の行のうち何番目か（0 から数える）である。折り返さないときは 0 である。
	k int
}

// buildView は、スクロールの位置（top、topFill、skip）から、表示領域の rows 行の中身を決めて r.view に入れ、
// 最後まで描いた行の次の行を r.bot に入れる（最初の行を最後まで描けなくても、最初の行の次の行にする。nvim 0.12.5 のルーラーと同じ）。
// 見比べている間は、表示の最初の行の上に topFill 行、ほかの行の上にその行の詰め物の行（Diff.fill）を置く。
func (r *Renderer) buildView(text Text, f *Frame, rows int) {
	r.view = r.view[:0]
	n := text.LineCount()
	for range min(r.topFill, rows) {
		r.view = append(r.view, viewRow{line: -1})
	}
	r.bot = r.top
	for line := r.top; len(r.view) < rows; line++ {
		if line > r.top {
			for k := f.Diff.fill(line); k > 0 && len(r.view) < rows; k-- {
				r.view = append(r.view, viewRow{line: -1})
			}
			if len(r.view) >= rows {
				break
			}
		}
		if line >= n {
			r.view = append(r.view, viewRow{line: line})
			continue
		}
		h := r.height(text, line)
		k := 0
		if line == r.top {
			k = min(r.skip, h-1)
		}
		for ; k < h && len(r.view) < rows; k++ {
			r.view = append(r.view, viewRow{line: line, k: k})
		}
		if k == h {
			r.bot = line + 1
		}
	}
	r.bot = max(r.bot, r.top+1)
}

// rowLine は、直前の Render で、表示領域の i 行目に描いたバッファの行の番号を返す。詰め物の行なら -1 で、
// バッファの行数以上なら最終行より後の "~" の行である。プレビュー（drawPreview）が、行の色を付ける行を引くのに使う。
func (r *Renderer) rowLine(i int) int {
	if i >= 0 && i < len(r.view) {
		return r.view[i].line
	}
	return r.top + i
}

// cursorScreen は、カーソルを置く画面の行と列を返す。行を折り返すときは、カーソルの見た目の行と、その行の中の列から数える。
func (r *Renderer) cursorScreen(text Text, f *Frame, cur cursorPlace) (row, col int) {
	if !r.wrap {
		return r.screenRow(text, f, cur.line), r.textLeft() + cur.col - r.left
	}
	return r.screenRow(text, f, cur.line) + cur.row, r.textLeft() + cur.x
}
