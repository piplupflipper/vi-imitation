package ui

import (
	"cmp"
	"fmt"
	"slices"
)

// CursorShape は、カーソルの形である。
type CursorShape int

// カーソルの形。
const (
	// CursorBlock は、文字を覆う箱の形である。ノーマルモードとビジュアルモードで使う。
	CursorBlock CursorShape = iota
	// CursorBar は、文字の左の縦線である。インサートモードとコマンドラインモードで使う。
	CursorBar
	// CursorUnderline は、文字の下の線である。オペレーターの後でモーションを待つ間に使う（Vim の既定の 'guicursor' の o と同じ）。
	CursorUnderline
)

// Cursor は、端末のカーソルの位置と形である。Row と Col は 0 から数える。
type Cursor struct {
	Row, Col int
	Shape    CursorShape
	// Visible は、カーソルを表示するかどうかである。カーソルを置く場所が画面に無いとき（表示領域が 0 行のときなど）は false である。
	Visible bool
}

// Screen は、1 回の描画の結果である。
type Screen struct {
	// Grid は、画面の全体の内容である。
	Grid *Grid
	// Cursor は、端末のカーソルの位置と形である。
	Cursor Cursor
	// Mouse は、端末にマウスの報告を求めるかである（設定の mouse.enabled。FR-180）。
	// Renderer は値を入れず、画面を端末へ送る側（internal/editor）が入れる。端末の側（internal/term の Painter）は、
	// 前回の画面と違えば、報告を求める列か止める列を出す。
	Mouse bool
}

// Renderer は、Frame から画面の格子を作る。
//
// 画面は上から、バッファの表示領域、ステータス行（1 行）、コマンドラインの行（1 行）に分ける。
// 出力の欄（Frame.Output）を開いていれば、ステータス行とコマンドラインの行の間に、欄の中身と欄の名前の行を置く（Vim の :copen の窓と同じ並び）。
// バッファの表示領域の左には、印の列（印か診断があるときだけ 2 列）を置き、その右に行番号の列（Frame.LineNumbers）を置く（layout）。
// GutterWidth で印の列の場所を空けておくこともできる。
// バッファの行は、Frame.Wrap が false なら折り返さず、長い行は横にスクロールして表示する（Vim の 'nowrap'）。
// true なら本文の幅で折り返す（wrap.go。FR-156）。出力の欄の行は折り返す。
//
// Renderer は、縦と横のスクロールの位置を、描画をまたいで持つ。
// 表示領域の行数が変わったとき（出力の欄の開け閉め、端末の大きさの変更）は、カーソルの行が表示領域の中で同じ割合の位置に来るように縦の位置を決める（fitHeight）。
// スクロールの位置は表示のための状態で、編集の状態（バッファ、カーソル、モード）は持たず、変更もしない。
// イベントループの goroutine 1 つだけが使う前提で、排他制御をしない（基本設計 2.1）。
type Renderer struct {
	// Width と Height は、画面の列数と行数である。
	Width, Height int
	// Theme は、色の種類の名前から属性への対応である。nil なら DefaultTheme を使う。
	Theme Theme
	// GutterWidth は、バッファの表示領域の左に、印の列の場所として必ず空ける列数である。行番号の列とテキストはその右から描く。
	// 印か診断があれば、少なくとも印の列の 2 列を空ける。
	GutterWidth int
	// Symbols は、記号の名前から描く文字列への対応（設定の symbols。FR-93）である。表に無い名前は DefaultSymbols の値を使う。
	// 値は CheckSymbol を通したものにする。
	Symbols Symbols
	// StatusLine は、ステータス行に並べる部品の列（設定の statusline。FR-92）である。nil なら DefaultStatusLine を使う。
	StatusLine *StatusLine
	// TabLineParts は、タブの行に並べる部品の列（設定の tabline の left と right。FR-92）である。nil なら DefaultTabLineParts を使う。
	TabLineParts *StatusLine
	// TabLine は、タブの行（Frame.Tabs）を出す場合である（設定の tabline.show）。ゼロ値はタブが 1 つでも出す（TabLineAlways）。
	TabLine TabLineMode

	// tabStop は、Render（render）が描いている Frame の TabStop（タブ文字の表示幅。FR-161）である。0 以下なら DefaultTabStop を使う。
	// Render の始めに入れ、描画の中でタブ幅を使う箇所（本文、ステータス行、ポップアップの幅など）が読む。
	tabStop int
	// rule は、render が描いている Frame のバッファの行を並べる規則（タブ文字の幅と、空白の記号。whitespace.go）である。
	// render の始めに Frame の TabStop と Whitespace から作り、本文、カーソルの位置、折り返し（wrapper）が読む。
	rule lineRule

	top int // 表示領域の最初の行の番号（0 から数える）
	// topFill は、見比べている間（Frame.Diff）に、最初の行の上に見せる詰め物の行の数である（diff.go。Vim の w_topfill）。
	topFill int
	// diffOn は、直前の Render が見比べている間（Frame.Diff が nil でない）だったかである。
	diffOn bool
	// tabRows は、直前の Render でタブの行に使った行数（0 か 1）である。Render の外から読む表示領域の行数（TextRows）から除く。
	tabRows int
	// bodyTop は、render が描いている格子の最初の行の、画面の中の行の番号（タブの行を出しているときは 1、ほかは 0）である。
	// render の中では tabRows が 0 なので、画面の上の端から数える位置（drawCommandLinePopup）はこれで引く。
	bodyTop int
	left    int // 表示領域の左の端の列（行の先頭を 0 とする表示の列）
	// skip は、行を折り返すとき、最初の行（top）を折り返した見た目の行のうち、表示領域より上に隠す行数である
	// （Vim の w_skipcol を見た目の行で数えたもの）。最初の行が表示領域より高く、カーソルがその行にあるときだけ 0 でない（scroll）。
	skip int
	// margins は、render が描いている Frame の余白（Frame.Margins。FR-174）である。scroll が読む。
	margins Margins
	// valid は、直前に縦の位置をカーソルに合わせて決めたときの入力である。次の描画で同じなら、最初の行を決め直さない（scroll）。
	// スクロールの位置を変える口（SetScroll など）は、ゼロ値に戻して決め直させる。
	valid topKey

	// wrap は、直前の layout の Frame.Wrap（行を折り返すか）である。
	wrap bool
	// center は、EnterBuffer の後の次の Render で、行を折り返すなら、カーソルの行を表示領域の中ほどに置き直すことを表す。
	center bool
	// view は、直前の Render の表示領域の行ごとの中身（見た目の行）である（buildView）。
	view []viewRow
	// bot は、直前の Render で、表示領域に最後まで描いた行の次の行の番号である（Vim の w_botline）。行を折り返すときのルーラーに使う。
	bot int
	// heights は、1 回の Render の中で数えた、行の見た目の行の数の覚え（行の番号から数）である。Render の始めに空にする。
	heights map[int]int
	// curMemo は、1 回の描画の中で求めたカーソルの配置（placeCursor）の覚えである。本文とタブの行（ルーラーの部品）が
	// 同じ Frame のカーソルを求めるので、長い行を 2 回数えないようにする。render の始めに空にする。
	curMemo cursorMemo

	// gutter、numWidth、outRows は、直前の Render（layout）で決めた、印の列の列数、行番号の列の列数（出さなければ 0）、
	// 出力の欄の中身の行数である。
	gutter, numWidth, outRows int

	// 以下は、表示領域の行数が変わったときに最初の行を決める（fitHeight）ための、直前の Render の状態である。
	rendered bool // Render を 1 回以上行ったか
	rows     int  // 直前の Render の表示領域の行数
	fraction int  // カーソルの行の、表示領域の中の相対位置（Vim の w_fraction。fractionMult 分の 1 の単位）
	fracRow  int  // fraction を最後に使ったときのカーソルの画面の行に 1 を足したもの。0 なら未設定（Vim の w_prev_fraction_row）

	// float は、ポップアップ（Frame.Float）の窓の中を描く子の Renderer で、floatID はその窓の Float.ID である（drawFloat）。
	// 窓が替わるか無くなったら作り直す。
	float   *Renderer
	floatID uint64
	// floatList は、ファインダーのポップアップ（Float.List）の一覧を描く子の Renderer である（drawFinder）。float と同じく窓ごとに作り直す。
	floatList *Renderer
	// floatPreview は、ポップアップのプレビュー（Float.Preview。FR-149）の中身を描く子の Renderer で、previewID はその中身の Preview.ID である
	// （drawPreview）。中身が替わるか、プレビューを描かなくなったら作り直す。
	floatPreview *Renderer
	previewID    uint64
	// posFrom は、ステータス行の範囲の位置（StatusPartPosition）を、この Renderer の代わりに数える Renderer である。
	// フォーカスのあるポップアップのステータス行を描く間だけ、窓の子の Renderer を入れる。
	posFrom *Renderer
	// hits は、直前の Render が描いた部品の場所である。マウスの出来事の位置を解く（HitTest。mouse.go）。
	hits hitState
	// pum は、補完の候補の一覧（Frame.Completion。FR-197）の、描画をまたいで保つスクロールの位置である（completion.go）。
	pum pumState
}

// fractionMult は、fraction の単位である（Vim の FRACTION_MULT と同じ値）。
const fractionMult = 16384

// Top は、表示領域の最初の行の番号（0 から数える）を返す。
func (r *Renderer) Top() int { return r.top }

// Left は、表示領域の左の端に当たる表示の列（0 から数える）を返す。
func (r *Renderer) Left() int { return r.left }

// SetLeft は、横のスクロールの位置（表示領域の左の端の列）を left にする。負の値は 0 にする。縦の位置は変えない。
// 次の Render は、カーソルが左右の余白を残して表示領域に入っていれば、横の位置を動かさない（scroll）。
// ホイールの横のスクロール（internal/command の mouse.go。FR-182）が、カーソルを表示領域の中へ移してから使う。
func (r *Renderer) SetLeft(left int) { r.left = max(left, 0) }

// SetScroll は、スクロールの位置を変える。負の値は 0 にする。
// 次の Render は、この位置からカーソルが画面に収まるようにスクロールする。
func (r *Renderer) SetScroll(top, left int) {
	r.top, r.left = max(top, 0), max(left, 0)
	r.topFill, r.skip = 0, 0
	r.valid = topKey{}
}

// ScrollPos は、Renderer の表示の位置である。縦と横のスクロールの位置に加えて、見比べている間の最初の行の上の詰め物の行の数
// （Vim の w_topfill）と、行を折り返すときに最初の行の途中から見せる位置（Vim の w_skipcol。Renderer.skip）を含む。
// 中身は描画の内部の値で、呼び出し側は ScrollPos で読んだ値を SetScrollPos で戻すだけに使う。
// 表示の位置を決める値を足すときは、ここに足す。
type ScrollPos struct {
	top, left, topFill, skip int
}

// ScrollPos は、今の表示の位置を返す。
func (r *Renderer) ScrollPos() ScrollPos {
	return ScrollPos{top: r.top, left: r.left, topFill: r.topFill, skip: r.skip}
}

// SetScrollPos は、表示の位置を、前に ScrollPos で読んだ p に戻す。SetScroll と違い、詰め物の行の数と、最初の行の途中から見せる位置も戻す。
// 次の Render は、この位置からカーソルが画面に収まるようにスクロールする。
func (r *Renderer) SetScrollPos(p ScrollPos) {
	r.top, r.left, r.topFill, r.skip = max(p.top, 0), max(p.left, 0), max(p.topFill, 0), max(p.skip, 0)
	r.valid = topKey{}
}

// EnterBuffer は、表示するバッファを替えたときに、スクロールの位置を、替えた先のバッファ（行数 lineCount）のカーソルの行 line に合わせて決め直す。
// 縦は、line を表示領域の中ほど（上から (h-1)/2 行目。h は直前の Render の表示領域の行数）に置き、
// 最初の行が先頭より前にも、バッファの最終行の後ろに "~" の行が出る位置にもならないように詰める。横は左の端に戻す。
// 次の Render は、この位置からカーソルが画面に収まるようにスクロールする。
//
// Vim 9.1 の enter_buffer（最初の行を 1 にしてから、バッファに残したカーソルで scroll_cursor_halfway を呼ぶ）に合わせた。
// 前のバッファの最初の行を使い続けると、定義へ移動して戻った（<C-o>）ときに、画面に収まる短いファイルの先頭の行が隠れる（#155）。
// 値は、tmux の 33 行と 32 行の表示領域で、400 行、40 行、22 行のファイルのカーソルの行を変えて、別のバッファから戻ったときの line("w0") を測って合わせた。
//
// 行を折り返して描いていたら（直前の Render の Frame.Wrap）、行の高さはここでは分からないので、次の Render で、
// 見た目の行の数でカーソルの行を中ほどに置き直す（scroll.View.CursorHalfway。Vim の scroll_cursor_halfway）。
func (r *Renderer) EnterBuffer(lineCount, line int) {
	h := r.textRows()
	r.top = max(min(line-(h-1)/2, lineCount-h), 0)
	r.left = 0
	r.topFill, r.skip = 0, 0
	r.center = r.wrap
	r.valid = topKey{}
}

// textRows は、バッファの表示領域の行数を返す。
// 出力の欄を開いていれば、欄の中身と欄の名前の行の分だけ減る。
func (r *Renderer) textRows() int {
	if r.outRows > 0 {
		return max(r.Height-r.tabRows-3-r.outRows, 0)
	}
	return max(r.Height-r.tabRows-2, 0)
}

// signWidth は、印の列の列数（行番号の列を描く最初の画面の列）を返す。
func (r *Renderer) signWidth() int {
	return max(r.gutter, r.GutterWidth, 0)
}

// textLeft は、バッファのテキストを描く最初の画面の列を返す。印の列と行番号の列の右である。
// カーソルの画面の列と、カーソルの近くに浮かべる窓の位置も、この列から数える。
func (r *Renderer) textLeft() int {
	return min(r.signWidth()+r.numWidth, max(r.Width, 0))
}

// textWidth は、バッファのテキストを描く列数を返す。
func (r *Renderer) textWidth() int {
	return max(r.Width-r.textLeft(), 0)
}

// emptyText は、Frame.Text が nil のときに使う、空の 1 行のバッファである。
type emptyText struct{}

func (emptyText) LineCount() int  { return 1 }
func (emptyText) Line(int) string { return "" }

// cursorPlace は、カーソルの文字の画面上の配置である。
type cursorPlace struct {
	line int // 行の番号
	byte int // 行の中のバイト位置
	col  int // カーソルを置く表示の列
	end  int // カーソルの文字が占める最後の列の次の列。カーソルを画面に収める判定に使う
	// start は、行を折り返さないときの、カーソルの文字が占める最初の列である（ノーマルモードのタブの上では col より左）。
	// カーソルを画面に収める判定に使う（Neovim の curs_columns の startcol）。
	start int
	// row と x は、行を折り返すとき（Renderer.wrap）の、カーソルを置く見た目の行（行の中で何番目か）と、その行の左の端からの列である。
	// 折り返さないときは 0 である。
	row, x int
}

// Render は、f を描いた画面を返す。
// カーソルが表示領域に収まるように、縦と横のスクロールの位置を更新する。
// タブの行を出すとき（f.Tabs と TabLine。tabLineShown）は、一番上の行をタブの行にし、残りをその下に描く。
func (r *Renderer) Render(f *Frame) Screen {
	r.tabStop = f.TabStop
	if r.tabLineShown(f) {
		return r.renderWithTabLine(f)
	}
	r.tabRows = 0
	return r.render(f)
}

// render は、タブの行を除いた Render である。
func (r *Renderer) render(f *Frame) Screen {
	theme := r.Theme
	if theme == nil {
		theme = DefaultTheme()
	}
	r.tabStop = f.TabStop
	r.rule = newLineRule(f.LineLayout, r.symbol)
	r.margins = f.Margins
	text := f.Text
	if text == nil {
		text = emptyText{}
	}
	g := NewGrid(r.Width, r.Height)
	r.layout(f)
	r.hits.reset()
	r.hits.lines = text.LineCount()
	if r.heights == nil {
		r.heights = map[int]int{}
	}
	clear(r.heights)
	r.curMemo = cursorMemo{}
	cur := r.placeCursor(text, f)
	prevTop := r.top
	resized := r.fitHeight(text, cur)
	if r.center {
		// EnterBuffer の後は、見た目の行の数でカーソルの行を中ほどに置き直す（折り返すときだけ。EnterBuffer の doc）。
		r.center = false
		if r.wrap && r.textRows() > 0 {
			v := r.scrollView(text, r.textRows())
			v.CursorHalfway(cur.line, false, false)
			r.setView(v)
		}
	}
	r.scroll(text, cur)
	r.fitDiff(f, text, cur, prevTop)
	if resized {
		r.fracRow = r.cursorRow(text, cur) + 1
	}
	r.rendered, r.rows = true, r.textRows()

	rows := r.textRows()
	r.buildView(text, f, rows)
	d := drawer{r: r, g: g, theme: theme, text: text, f: f, cur: cur}
	d.collectSpans(rows)
	signs := d.signsByLine(rows)
	// 表示領域の行ごとの中身（見た目の行。buildView）を上から描く。見比べている間の詰め物の行（diff.go）、最終行より後の "~" の行、
	// バッファの行の見た目の行がある。バッファの 1 行の見た目の行は、続けて 1 回の drawLine で描く。
	for row := 0; row < rows; {
		vr := r.view[row]
		switch {
		case vr.line < 0:
			d.drawFiller(row)
			row++
		case vr.line >= text.LineCount():
			g.set(row, 0, Cell{Text: "~", Width: 1, Attr: theme.attr(StyleNonText)})
			row++
		default:
			end := row + 1
			for end < rows && r.view[end].line == vr.line {
				end++
			}
			for i := row; i < end; i++ {
				d.drawGutter(i, r.view[i], signs)
			}
			d.drawLine(row, vr.line, vr.k, end-row)
			row = end
		}
	}
	d.drawMarks(rows)
	area := textArea{top: 0, rows: rows, width: r.Width}
	r.drawIntro(g, f.Intro, area)
	r.drawBand(g, theme, f, text, cur, area)
	r.drawScrollbar(g, theme, f.Scrollbar, text, area)
	r.drawProgress(g, theme, f.Progress, area)
	curRow, curCol := r.cursorScreen(text, f, cur)
	var listCursor *Cursor
	switch {
	case f.List != nil && rows > 0:
		if c, ok := r.drawList(g, theme, f.List); ok {
			listCursor = &c
		}
	case f.Float != nil && rows > 0:
		if c, ok := r.drawFloat(g, theme, f.Float, curRow, curCol); ok {
			listCursor = &c
		}
	default:
		r.float, r.floatList, r.floatPreview = nil, nil, nil
	}
	screen := Screen{Grid: g, Cursor: Cursor{
		Row: curRow, Col: curCol, Shape: CursorBlock,
		Visible: rows > 0 && r.textWidth() > 0,
	}}
	switch {
	case f.Mode == ModeInsert:
		screen.Cursor.Shape = CursorBar
	case f.OperatorPending:
		screen.Cursor.Shape = CursorUnderline
	}
	if listCursor != nil {
		screen.Cursor = *listCursor
	}
	if r.Height >= 2 {
		fl := f.Float
		switch {
		case listCursor != nil && fl != nil && fl.Preview != nil && fl.Preview.Focused && fl.Preview.View != nil && r.floatPreview != nil:
			// フォーカスがプレビューにあれば、ルーラーと範囲の位置をプレビューのバッファのものにする（FR-149）。
			pv := fl.Preview.View
			r.posFrom = r.floatPreview
			r.drawStatus(g, theme, pv.Text, f, r.floatPreview.placeCursor(pv.Text, pv), rows)
			r.posFrom = nil
		case listCursor != nil && fl != nil && fl.Focused && r.float != nil:
			// フォーカスのあるポップアップでは、ルーラーと範囲の位置をキーを受けるポップアップのバッファのものにする（PR #242 のレビュー）。
			r.posFrom = r.float
			r.drawStatus(g, theme, fl.View.Text, f, r.float.placeCursor(fl.View.Text, fl.View), rows)
			r.posFrom = nil
		default:
			r.drawStatus(g, theme, text, f, cur, rows)
		}
	}
	if r.outRows > 0 {
		r.drawOutput(g, theme, f.Output, rows+1)
	}
	if r.Height >= 1 {
		if c, ok := r.drawCommandLine(g, theme, f); ok {
			screen.Cursor = c
		}
	}
	return screen
}

// placeCursor は、カーソルの位置をバッファの中に収め、画面上の列を求める。
//
// ノーマルモードとビジュアルモードでは、タブの上のカーソルをタブの最後の列に置く。インサートモードでは最初の列に置く（Vim と同じ）。
// 空白の記号を描くとき（Frame.Whitespace）は、どのモードでも最初の列に置く（Vim の 'list' と同じ。r.rule がタブ文字を glyphTab にしない）。
// 行末より後ろ（インサートモードで行末に挿入するとき）は、行の表示幅の列に置く。
// 行を折り返すとき（layout が決めた r.wrap）は、見た目の行と、その行の中の列も求める（wrapper.cursor）。
// そのときの表示の列（ルーラーに出す列）は、">" の列も数える折り返すときの列（wrapper.vcolAt の doc）にする（Vim と同じ）。
// 折り返すときは、行の先頭から 1 回だけ数える（行の先頭からの表示の列は使わないので数えない）。
// 同じ Render の中で同じ Frame と同じ並び（折り返すか、本文の幅）なら、前に求めた配置を返す（curMemo）。
func (r *Renderer) placeCursor(text Text, f *Frame) cursorPlace {
	key := cursorMemo{ok: true, f: f, wrap: r.wrap, width: r.textWidth()}
	if m := r.curMemo; m.ok && m.f == key.f && m.wrap == key.wrap && m.width == key.width {
		return m.p
	}
	var p cursorPlace
	if r.wrap {
		line := min(max(f.Cursor.Line, 0), text.LineCount()-1)
		w := r.wrapper()
		// 行の見た目の行の数も同じ 1 回で数えて覚える（スクロールがカーソルの行の高さを数え直さないため）。
		off, c, rows := w.cursorRows(line, text.Line(line), max(f.Cursor.Byte, 0), f.Mode != ModeInsert)
		p = cursorPlace{line: line, byte: off, row: c.row, x: c.x, col: w.vcol(c)}
		p.end = p.col + 1
		if r.heights != nil && f.Text != nil {
			r.heights[line] = rows
		}
	} else {
		p = r.placeColumn(text, f)
	}
	key.p = p
	r.curMemo = key
	return p
}

// cursorMemo は、placeCursor の覚えである（Renderer.curMemo）。
type cursorMemo struct {
	ok    bool
	f     *Frame
	wrap  bool
	width int
	p     cursorPlace
}

// placeColumn は、placeCursor の、行の先頭からの表示の列を求める部分である。
// カーソルの文字の前に inline の仮想テキストがあれば、ノーマルモードとビジュアルモードでは文字の上に、インサートモードでは
// 仮想テキストの前に置く。行末の位置も、行末の位置の仮想テキストの前に置く（Neovim 0.12.5 と同じ。FR-232）。
//
// req: FR-232
func (r *Renderer) placeColumn(text Text, f *Frame) cursorPlace {
	line := min(max(f.Cursor.Line, 0), text.LineCount()-1)
	s := text.Line(line)
	b := min(max(f.Cursor.Byte, 0), len(s))
	col, gap := 0, -1
	wk := r.rule.walk(line, s)
	for wk.more() {
		gl := wk.next(col)
		if gl.virt != 0 {
			if gap < 0 {
				gap = col
			}
			if gl.off >= len(s) && b >= len(s) {
				break
			}
			col += gl.width
			continue
		}
		if b < gl.off+gl.size {
			p := cursorPlace{line: line, byte: gl.off, col: gl.col, end: gl.col + 1, start: gl.col}
			switch {
			case f.Mode == ModeInsert && gap >= 0:
				p.col, p.start, p.end = gap, gap, gap+1
			case gl.kind == glyphTab && f.Mode != ModeInsert:
				p.col = gl.col + gl.width - 1
				p.end = p.col + 1
			case gl.kind == glyphText && gl.width == 2:
				p.end = gl.col + 2
			}
			return p
		}
		gap = -1
		col += gl.width
	}
	return cursorPlace{line: line, byte: len(s), col: col, end: col + 1, start: col}
}

// fitHeight は、表示領域の行数が直前の Render から変わったとき（出力の欄を開け閉めした、端末の大きさが変わった）に、
// カーソルの行が、表示領域の中で前と同じ割合の位置に来るように最初の行を決める。
// Vim 9.1 の win_new_height と scroll_to_fraction（'splitkeep' が既定の cursor のとき）に合わせた。
//
// 前の割合は、前の行数のままカーソルを表示領域に収めた（Vim が高さを変える前に行う validate_cursor）ときのカーソルの画面の行から求める。
// 直前の Render の後に、カーソルの移動と行数の変更が 1 回の描画にまとまっても（G:output の続けての入力）、Vim と同じく移動の後の位置から求めるためである。
// 割合は、カーソルの画面の行が前の変更の後から動いていなければ前の値を使い続ける（Vim の w_prev_fraction_row）。
// 開け閉めを繰り返しても位置がずれていかないためである。
// 表示領域にバッファの全体が収まり、最初の行が先頭なら、最初の行を変えない（Vim と同じ）。
//
// この後の scroll が、カーソルが表示領域に収まるようにする。
// 前の行数が 0 でない変更のとき（Render が、scroll の後のカーソルの画面の行を w_prev_fraction_row として記録するとき）に true を返す。
//
// 行を折り返すときは、行と画面の行の代わりに見た目の行で数える（Vim の scroll_to_fraction も、行の高さを足して最初の行を決める）。
// 最初の行は、カーソルの行から上へ、見た目の行の数が割合の位置に収まるところまで戻した行にし、行の途中からは見せない。
// 折り返さないときは、どの行の高さも 1 なので、数え方は変わらない。
func (r *Renderer) fitHeight(text Text, cur cursorPlace) bool {
	h, prev := r.textRows(), r.rows
	if !r.rendered || h == prev {
		return false
	}
	r.valid = topKey{}
	if prev > 0 {
		v := r.scrollView(text, prev)
		v.Follow(cur.line, cur.row, cur.x)
		r.setView(v)
		if wrow := r.cursorRow(text, cur); prev > 1 && wrow+1 != r.fracRow {
			// 最初の行と最後の行のカーソルは、その行の真ん中にあるとして数える（Vim の set_fraction）。
			r.fraction = (wrow*fractionMult + fractionMult/2) / prev
		}
	}
	if h > 0 && (h < r.contentRows(text, h) || r.top > 0) {
		wrow := (r.fraction*h - 1) / fractionMult
		top, above := cur.line, cur.row
		for top > 0 && above+r.height(text, top-1) <= wrow {
			top--
			above += r.height(text, top)
		}
		r.top, r.skip = top, 0
	}
	return prev > 0
}

// height は、行 n を描く表示領域の行数を返す。折り返さなければ 1 で、折り返すなら見た目の行の数である（1 回の Render の中で覚える）。
func (r *Renderer) height(text Text, n int) int {
	if !r.wrap || n < 0 || n >= text.LineCount() {
		return 1
	}
	if h, ok := r.heights[n]; ok {
		return h
	}
	h := r.wrapper().rows(n, text.Line(n))
	if r.heights != nil {
		r.heights[n] = h
	}
	return h
}

// contentRows は、バッファの全体を描く表示領域の行数を返す。limit より多いと分かったら、数えるのをやめて limit+1 を返す。
// 折り返さなければ行数である。
func (r *Renderer) contentRows(text Text, limit int) int {
	n := text.LineCount()
	if !r.wrap || n > limit {
		return n
	}
	sum := 0
	for i := range n {
		sum += r.height(text, i)
		if sum > limit {
			return limit + 1
		}
	}
	return sum
}

// cursorRow は、カーソルを置く見た目の行の、表示の最初の行（top の skip 番目の見た目の行）からの行数を返す。見比べの詰め物の行は数えない。
// 折り返さなければ、カーソルの行と最初の行の差である。
func (r *Renderer) cursorRow(text Text, cur cursorPlace) int {
	if !r.wrap || cur.line < r.top {
		return cur.line - r.top
	}
	row := cur.row - r.skip
	for n := r.top; n < cur.line; n++ {
		row += r.height(text, n)
	}
	return row
}

// span は、1 行の中の、属性を重ねるバイトの範囲 [start, end) である。
// end が行の長さより大きければ、行末の 1 セル（行の区切りを表すセル）も含む。
type span struct {
	start, end int
	order      int // 重ねる順。大きい方を上に重ねる（色の種類ごとの順は layers.go の表）
	attr       Attr
}

// drawer は、1 回の描画の作業の状態である。
type drawer struct {
	r     *Renderer
	g     *Grid
	theme Theme
	text  Text
	f     *Frame
	cur   cursorPlace
	diags [][]span // 表示領域の行ごとの診断の範囲
	hls   [][]span // 表示領域の行ごとの、Frame.Highlights の範囲
	sel   []span   // 表示領域の行ごとの、選択の範囲（end が 0 なら選択なし）
	sweep sweep    // 行ごとに使い回す作業の領域
	// spaces は、描いている行の行頭の空白と行末の空白の範囲である。空白の記号を描くとき（FR-170）だけ、prepare が行ごとに求める（look が読む）。
	spaces lineSpaces
	// anchors は、描いている行の overlay の仮想テキストを重ねる所を集める、行ごとに使い回す作業の領域である（drawLine、drawWrapped）。
	anchors []virtAnchor
}

// collectSpans は、診断、範囲の色（Frame.Highlights）、選択の範囲を、表示領域の行ごとに分ける。
// 構文の色は Decorations が行ごとに分けて持っているので、ここでは分けない。
//
// 範囲の色は、行ごとに範囲に掛かる文字にだけ付け、行末の 1 セルには付けない（highlightSpans）。
func (d *drawer) collectSpans(rows int) {
	d.diags = make([][]span, rows)
	d.sel = make([]span, rows)
	d.highlightSpans(rows)
	top := d.r.top
	for i, dg := range d.f.Diagnostics {
		a := d.theme.attr("diagnostic." + string(dg.Severity))
		if a == (Attr{}) {
			continue
		}
		s, e := dg.Range.Start, dg.Range.End
		if e.Less(s) {
			s, e = e, s
		}
		for n := max(s.Line, top); n <= e.Line && n < top+rows && n < d.text.LineCount(); n++ {
			ln := len(d.text.Line(n))
			b0, b1 := 0, ln
			if n == s.Line {
				b0 = min(max(s.Byte, 0), ln)
			}
			if n == e.Line {
				b1 = min(max(e.Byte, 0), ln)
			}
			if s == e {
				b1 = b0 + 1
			}
			if b0 < b1 {
				d.diags[n-top] = append(d.diags[n-top], span{start: b0, end: b1, order: diagnosticOrder + i, attr: a})
			}
		}
	}

	sel := d.f.Selection
	if !sel.Active {
		return
	}
	s, e := sel.Start, sel.End
	if e.Less(s) {
		s, e = e, s
	}
	a := d.theme.attr(StyleVisual)
	for n := max(s.Line, top); n <= e.Line && n < top+rows && n < d.text.LineCount(); n++ {
		ln := len(d.text.Line(n))
		b0, b1 := 0, ln+1
		if !sel.Linewise {
			if n == s.Line {
				b0 = s.Byte
			}
			if n == e.Line && e.Byte < ln {
				b1 = e.Byte + 1
			}
		}
		d.sel[n-top] = span{start: b0, end: b1, attr: a}
	}
}

// sweep は、1 行の範囲を左から順に重ねるための作業の領域である。
// 文字を左から順に渡すと、その文字に掛かる範囲だけを active に持つので、文字ごとに行の範囲の全部をなめずに済む。
type sweep struct {
	spans  []span     // 開始の順に並べた、その行の範囲
	next   int        // まだ active に入れていない最初の範囲
	active []span     // いま見ている文字に掛かる範囲
	dirty  bool       // active の並びを重ねる順に直す必要があるか
	decos  []decoSpan // その行の構文の色の範囲（Decorations.appendLine の結果）
}

// reset は、行の範囲を空にする。
func (w *sweep) reset() {
	w.spans, w.active = w.spans[:0], w.active[:0]
	w.next, w.dirty = 0, false
}

// advance は、バイトの範囲 [off, off+size) の文字へ進む。size が 0 なら行末のセルを表す。
// 文字は左から順に渡す必要がある。
func (w *sweep) advance(off, size int) {
	hi := off + max(size, 1)
	for w.next < len(w.spans) && w.spans[w.next].start < hi {
		w.active = append(w.active, w.spans[w.next])
		w.next++
		w.dirty = true
	}
	k := 0
	for _, sp := range w.active {
		if sp.end > off {
			w.active[k] = sp
			k++
		}
	}
	w.active = w.active[:k]
}

// attr は、いまの文字に掛かる範囲の属性を、重ねる順に重ねて返す。
// mark は、その文字の表記の色（^A などの表記と空白の記号。layers.go の glyphOrder）で、glyphOrder の順に重ねる。空なら重ねない。
func (w *sweep) attr(mark Attr) Attr {
	if w.dirty {
		slices.SortFunc(w.active, func(a, b span) int { return cmp.Compare(a.order, b.order) })
		w.dirty = false
	}
	var a Attr
	pending := mark != (Attr{})
	for _, sp := range w.active {
		if pending && sp.order > glyphOrder {
			a, pending = a.Over(mark), false
		}
		a = a.Over(sp.attr)
	}
	if pending {
		a = a.Over(mark)
	}
	return a
}

// prepare は、表示領域の row 行目（バッファの line 行目、テキスト s）の構文の色、見比べの色、診断、検索の一致、
// 範囲の色（Frame.Highlights）の範囲を sweep に詰める。
// visible は、その行で表示の範囲に掛かる文字のバイトの範囲を返す関数で、検索の一致を集めるとき（searchSpans）だけ呼ぶ。
//
// req: FR-35, FR-137, FR-140
func (d *drawer) prepare(row, line int, s string, visible func() (from, to int)) {
	ln := len(s)
	if d.r.rule.list {
		d.spaces = spacesOf(s)
	}
	w := &d.sweep
	w.reset()
	w.decos = d.f.Decorations.appendLine(w.decos[:0], line)
	for _, ds := range w.decos {
		a := d.f.Decorations.attr(d.theme, ds)
		start, end := min(ds.start, ln), min(ds.end, ln)
		if a == (Attr{}) || start >= end {
			continue
		}
		w.spans = append(w.spans, span{start: start, end: end, order: ds.order, attr: a})
	}
	n := len(w.spans)
	w.spans = append(append(w.spans, d.diags[row]...), d.diffSpans(line, ln)...)
	if d.f.Search != nil {
		from, to := visible()
		w.spans = d.searchSpans(w.spans, s, from, to)
	}
	w.spans = append(w.spans, d.hls[row]...)
	if len(w.spans) > n {
		slices.SortStableFunc(w.spans, func(a, b span) int { return cmp.Compare(a.start, b.start) })
	}
}

// attrAt は、バッファの line 行目の、バイトの範囲 [off, off+size) の文字の属性を返す。size が 0 なら行末のセルを表す。
// mark は、その文字の表記の色（drawer.look。^A などの表記と空白の記号）で、構文の色の上、診断の下に重ねる（layers.go）。
// 同じ行の中では、off の小さい順に呼ぶ必要がある。
func (d *drawer) attrAt(row, line, off, size int, mark Attr) Attr {
	d.sweep.advance(off, size)
	a := d.sweep.attr(mark)
	sp := d.sel[row]
	covers := sp.start < off+max(size, 1) && sp.end > off
	// カーソルの文字は反転しない。端末のカーソルが反転で描かれても文字が読めるようにするためで、Vim と同じ。
	// コマンドラインモード（ビジュアルモードから / を打っている間）は、端末のカーソルがコマンドラインの行にあるので、カーソルの文字にも重ねる
	// （nvim 0.12.5 の v/foo も、打っている間は一致の先頭の文字まで選択の色で出す）。
	if sp.end > 0 && covers && (d.f.Mode == ModeCommandLine || line != d.cur.line || off != d.cur.byte) {
		a = a.Over(sp.attr)
	}
	return a
}

// drawLine は、バッファの line 行目を、表示領域の row 行目から描く。
// 行を折り返すときは、行の見た目の行のうち k 番目から n 行を描く（drawWrapped）。折り返さないときは k は 0、n は 1 である。
//
// 行の先頭から文字の列を数え直すので、時間は行の長さ（表示の左の端より左の部分を含む）に比例する。
// 文字ごとの割り当ては、^A などの表記を作るときと、行頭の結合文字だけである。
// 見比べている間は、詰め物の行の分だけ row が line - top より大きくなる。診断と選択の範囲は line - top で引く。
func (d *drawer) drawLine(row, line, k, n int) {
	if d.r.wrap {
		d.drawWrapped(row, line, k, n)
		return
	}
	s := d.text.Line(line)
	left, w, x0 := d.r.left, d.r.textWidth(), d.r.textLeft()
	idx := line - d.r.top
	d.prepare(idx, line, s, func() (int, int) { return d.visibleBytes(line, s) })
	put := func(x int, c Cell) {
		if x >= 0 && x < w {
			d.g.set(row, x0+x, c)
		}
	}
	// 仮想テキスト（virtual.go）の inline は行の文字と一緒に並べ、overlay は重ねる文字の列を覚えて、行の文字を描いた後に重ねる。
	lv := d.r.rule.virt.line(line)
	anchors, oi := d.anchors[:0], 0
	col := 0
	wk := d.r.rule.walk(line, s)
	for wk.more() && col < left+w {
		gl := wk.next(col)
		col += gl.width
		anchors, oi = overlayAnchors(anchors, lv, oi, gl, gl.col, wrapPos{})
		if gl.col+gl.width <= left {
			if gl.virt == 0 {
				d.sweep.advance(gl.off, gl.size)
			}
			continue
		}
		var a Attr
		if gl.virt != 0 {
			a = lv.inline[gl.virt-1].style.over(d.theme)
		} else {
			var base Attr
			gl, base = d.look(gl)
			a = d.attrAt(idx, line, gl.off, gl.size, base)
		}
		x := gl.col - left
		switch gl.kind {
		case glyphTab, glyphSpecial, glyphWhitespace:
			for i := range gl.width {
				put(x+i, Cell{Text: gl.cell(i), Width: 1, Attr: a})
			}
		case glyphText:
			switch {
			case gl.width < 2:
				put(x, Cell{Text: gl.text, Width: 1, Attr: a})
			case x < 0:
				// 全角文字の左半分が画面の左の端より左にある。Vim と同じく "<" を出す。
				put(0, Cell{Text: "<", Width: 1, Attr: a.Over(d.theme.attr(StyleNonText))})
			case x+1 >= w:
				// 右の端に半分だけ掛かる全角文字は、Vim と同じく最後の列に ">" を出す。
				put(x, Cell{Text: ">", Width: 1, Attr: a.Over(d.theme.attr(StyleNonText))})
			default:
				put(x, Cell{Text: gl.text, Width: 2, Attr: a})
				put(x+1, Cell{Width: 0, Attr: a})
			}
		}
	}
	// 行末のセルは、行が画面の左の端より短ければ（横にスクロールしたとき）画面の 0 列目に描く。Vim 9.1 と Neovim 0.12.5 も、
	// その行の 0 列目に行末の記号と、行末に掛かる選択の色を描く（2026-11-21 に tmux で、'list' の有無の両方で確かめた。PR #334 のレビューの必須 2）。
	// 見比べの行の色は行末のセルの次から埋める。0 列目に行末のセルを描いたときは、そのセルを残す。
	fill := col + 1 - left
	ended := col < left+w
	if ended {
		if c, ok := d.eolCell(idx, line, len(s)); ok {
			put(max(col-left, 0), c)
			fill = max(fill, 1)
		}
		// 行末の位置の overlay は、行末のセルに重ねる。
		for ; lv != nil && oi < len(lv.overlay); oi++ {
			anchors = append(anchors, virtAnchor{group: oi, col: col})
		}
	}
	d.diffFillRest(row, line, fill)
	if lv != nil {
		// eol は overlay の上に描く（Neovim 0.12.5 と同じ。PR #378 のレビューの推奨 3）。
		for _, an := range anchors {
			d.drawVirtual(row, an.col-left, w, lv.overlay[an.group].pieces)
		}
		if ended {
			d.drawEOLVirtual(row, max(col-left, 0)+1, w, lv)
		}
	}
	d.anchors = anchors
}

// drawWrapped は、行を折り返すとき（FR-156）の drawLine である。バッファの line 行目の見た目の行のうち、k 番目から n 行を、
// 表示領域の row 行目から描く。文字の置き方は wrapper.place で決め、色は折り返さないときと同じく、バイトの範囲（prepare と attrAt）で重ねる。
// 全角文字を次の見た目の行へ送った列には ">" を、続きの見た目の行の頭には記号（SymbolShowBreak）を、どちらも StyleNonText で描く。
// 時間は、行の先頭から、描く最後の見た目の行までの長さに比例する。
//
// req: FR-156
func (d *drawer) drawWrapped(row, line, k, n int) {
	s := d.text.Line(line)
	w := d.r.wrapper()
	x0 := d.r.textLeft()
	idx := line - d.r.top
	d.prepare(idx, line, s, func() (int, int) { return w.span(line, s, k, k+n) })
	nonText := d.theme.attr(StyleNonText)
	put := func(p wrapPos, c Cell) {
		if i := p.row - k; i >= 0 && i < n && p.x >= 0 && p.x < w.width {
			d.g.set(row+i, x0+p.x, c)
		}
	}
	// 仮想テキストは drawLine と同じく、inline を行の文字と一緒に並べ、overlay を後から重ねる（FR-233）。
	lv := d.r.rule.virt.line(line)
	anchors, oi := d.anchors[:0], 0
	p := wrapPos{}
	done := true
	wk := w.rule.walk(line, s)
	for wk.more() {
		gl := w.next(&wk, p)
		start := p
		first, pad, padded := w.place(&p, gl)
		if first.row >= k+n {
			done = false
			break
		}
		anchors, oi = overlayAnchors(anchors, lv, oi, gl, 0, first)
		if p.row < k {
			if gl.virt == 0 {
				d.sweep.advance(gl.off, gl.size)
			}
			continue
		}
		var a Attr
		if gl.virt != 0 {
			a = lv.inline[gl.virt-1].style.over(d.theme)
		} else {
			var base Attr
			gl, base = d.look(gl)
			a = d.attrAt(idx, line, gl.off, gl.size, base)
		}
		if padded {
			put(pad, Cell{Text: ">", Width: 1, Attr: nonText})
		}
		switch gl.kind {
		case glyphTab, glyphSpecial, glyphWhitespace:
			// セルごとに、place と同じく右の端で次の見た目の行へ分けて置く。
			c := start
			for i := range gl.width {
				if c.x >= w.width {
					w.nextRow(&c)
				}
				put(c, Cell{Text: gl.cell(i), Width: 1, Attr: a})
				c.x++
			}
		case glyphText:
			switch {
			case gl.width < 2:
				put(first, Cell{Text: gl.text, Width: 1, Attr: a})
			case first.x+1 >= w.width:
				// 見た目の行の幅が 1 列しかなく、全角文字が収まらない。折り返さないときの右の端と同じく ">" を出す。
				put(first, Cell{Text: ">", Width: 1, Attr: a.Over(nonText)})
			default:
				put(first, Cell{Text: gl.text, Width: 2, Attr: a})
				put(wrapPos{row: first.row, x: first.x + 1}, Cell{Width: 0, Attr: a})
			}
		}
	}
	eolRow := -1
	var e wrapPos
	if done {
		e = w.eol(p)
		if c, ok := d.eolCell(idx, line, len(s)); ok {
			put(e, c)
		}
		if i := e.row - k; i >= 0 && i < n {
			d.diffFillRest(row+i, line, e.x+1)
			eolRow = row + i
		}
		for ; lv != nil && oi < len(lv.overlay); oi++ {
			anchors = append(anchors, virtAnchor{group: oi, pos: e})
		}
	}
	// overlay は、重ねる文字の見た目の行の右の端で切る（次の見た目の行へは続けない。Neovim 0.12.5 と同じ）。eol は overlay の上に描く。
	for _, an := range anchors {
		if i := an.pos.row - k; i >= 0 && i < n {
			d.drawVirtual(row+i, an.pos.x, w.width, lv.overlay[an.group].pieces)
		}
	}
	if lv != nil && eolRow >= 0 {
		d.drawEOLVirtual(eolRow, e.x+1, w.width, lv)
	}
	d.anchors = anchors
	if w.sbw > 0 {
		sb := d.r.symbol(SymbolShowBreak)
		for i := max(k, 1) - k; i < n; i++ {
			d.r.drawText(d.g, row+i, x0, x0+w.sbw, sb, nonText)
		}
	}
}

// drawMarks は、行を折り返すときの表示領域の端の印を描く（wrap.go の doc）。
// 最初の行を途中の見た目の行から描いていれば、最初の行の左の 3 列に "<<<" を出す。
// 最後の行に描いた行が収まりきらず、その行が表示領域の最初の行でなければ、最後の行の右の 3 列に "@@@" を出す。
//
// req: FR-156
func (d *drawer) drawMarks(rows int) {
	r := d.r
	if !r.wrap || rows <= 0 {
		return
	}
	a := d.theme.attr(StyleNonText)
	if r.skip > 0 {
		x1 := min(len(skipMark), max(r.Width, 0))
		clearSplit(d.g, 0, 0, x1)
		r.drawText(d.g, 0, 0, x1, skipMark[:x1], a)
	}
	last := r.view[rows-1]
	if last.line < 0 || last.line >= d.text.LineCount() || last.line == r.top {
		return
	}
	if last.k+1 < r.height(d.text, last.line) {
		x := max(r.Width-len(lastLineMark), 0)
		clearSplit(d.g, rows-1, x, r.Width)
		r.drawText(d.g, rows-1, x, r.Width, lastLineMark[:r.Width-x], a)
	}
}

// drawText は、row 行目の col 列から、列 limit の手前まで s を描き、描き終えた次の列を返す。
// 収まらない文字の手前で止める。タブ文字の幅は、グリッドの左の端を 0 列目として決める。
func (r *Renderer) drawText(g *Grid, row, col, limit int, s string, a Attr) int {
	return r.drawGlyphs(g, row, 0, col, limit, s, a)
}

// drawTextFrom は、drawText の、タブ文字の幅を col 列目を 0 列目として決めるものである（画面の上のポップアップのコマンドラインの欄。
// 幅を cmdlineTail と同じく欄の左の端から数えるため。PR #362 のレビューの必須 1）。
func (r *Renderer) drawTextFrom(g *Grid, row, col, limit int, s string, a Attr) int {
	return r.drawGlyphs(g, row, col, 0, limit-col, s, a) + col
}

// drawGlyphs は、s の文字を、origin 列目を 0 列目とした col 列目から、その数え方の列 limit の手前まで描き、
// 描き終えた次の列（同じ数え方）を返す。タブ文字の幅は、その数え方の列で決める。
func (r *Renderer) drawGlyphs(g *Grid, row, origin, col, limit int, s string, a Attr) int {
	tabStop := tabStopOr(r.tabStop)
	for off := 0; off < len(s); {
		gl := nextGlyph(s, off, col, tabStop)
		if gl.col+gl.width > limit {
			break
		}
		x := origin + gl.col
		switch gl.kind {
		case glyphTab, glyphSpecial, glyphWhitespace:
			for i := range gl.width {
				g.set(row, x+i, Cell{Text: gl.cell(i), Width: 1, Attr: a})
			}
		case glyphText:
			g.set(row, x, Cell{Text: gl.text, Width: gl.width, Attr: a})
			if gl.width == 2 {
				g.set(row, x+1, Cell{Width: 0, Attr: a})
			}
		}
		col = gl.col + gl.width
		off += gl.size
	}
	return col
}

// position は、表示している範囲の位置を、Vim のルーラーと同じ形（All、Top、Bot、NN%）で返す。
// 行を折り返すときは、表示領域に最後まで描いた行の次の行（r.bot。Vim の w_botline）から下の行数を数える。
func (r *Renderer) position(lineCount int) string {
	above := r.top
	below := max(lineCount-(r.top+r.textRows()), 0)
	if r.wrap {
		below = max(lineCount-r.bot, 0)
	}
	switch {
	case above == 0 && below == 0:
		return "All"
	case above == 0:
		return "Top"
	case below == 0:
		return "Bot"
	}
	return fmt.Sprintf("%d%%", above*100/(above+below))
}

// drawCommandLine は、最後の行にコマンドラインかメッセージを描く。選択肢の問い（Frame.Choice）か 1 行の入力の問い（Frame.Input）が
// あれば、どちらより先に問いを描く。
// コマンドラインモードでは ":" と入力中の文字列を出し、カーソル（縦線）を ok が true で返す。
// 入力中の文字列が幅に収まらなければ、末尾が見えるように先頭の側を隠す。
// Frame.CommandLinePopup なら、入力を最後の行でなく画面の上の中央のポップアップに描き（drawCommandLinePopup。FR-215）、
// 最後の行は空けておく。ポップアップを置けないほど画面が狭いか低ければ、最後の行に描く。
func (r *Renderer) drawCommandLine(g *Grid, theme Theme, f *Frame) (Cursor, bool) {
	row := r.Height - 1
	if f.Choice != nil {
		// 選択肢の問いの間のキーは問いへの答えにする（internal/editor）ので、コマンドラインモードでも問いを出す。
		return r.drawChoice(g, theme, f.Choice), true
	}
	if f.Input != nil {
		// 1 行の入力の問いの間のキーも問いへの答えにする（internal/editor）ので、コマンドラインモードでも問いを出す。
		return r.drawInput(g, theme, f), true
	}
	if f.Mode == ModeCommandLine {
		prefix := ':'
		if f.CommandLineType != 0 {
			prefix = f.CommandLineType
		}
		if f.CommandLinePopup {
			// 画面の上の中央のポップアップに描く（FR-215）。置けなければ、最後の行に描く。
			if c, ok := r.drawCommandLinePopup(g, theme, f, prefix); ok {
				return c, true
			}
		}
		full := string(prefix) + f.CommandLine
		tabStop := tabStopOr(r.tabStop)
		// 末尾の後ろのカーソルの 1 列を残して収まるまで、先頭の文字を落とす。
		s := r.cmdlineTail(full, r.Width)
		end := r.drawText(g, row, 0, r.Width, s, Attr{})
		if m := f.Completion; m != nil && m.Start >= 0 && m.Start <= len(f.CommandLine) {
			// 補完の候補の一覧は、項目を並べ始める位置の文字の列（落とした先頭の文字の分だけ左へずらした列）に合わせる（FR-197）。
			// req: FR-197
			dropped := len(full) - len(s)
			at := len(string(prefix)) + m.Start
			col := 0
			if at > dropped {
				col = StringWidth(full[dropped:at], tabStop)
			}
			r.drawCompletion(g, theme, m, row, col)
		} else {
			r.pum.valid = false
		}
		return Cursor{Row: row, Col: min(end, max(r.Width-1, 0)), Shape: CursorBar, Visible: r.Width > 0}, true
	}
	r.pum.valid = false
	lines := textLines(f.Message.Text)
	if len(lines) == 0 {
		return Cursor{}, false
	}
	style := StyleMessageInfo
	switch f.Message.Level {
	case MessageInfo:
	case MessageWarning:
		style = StyleMessageWarning
	case MessageError:
		style = StyleMessageError
	}
	if len(lines) > 1 {
		if !f.HitEnter {
			r.drawMessageLines(g, lines, theme.attr(style), "", Attr{})
			return Cursor{}, false
		}
		end := r.drawMessageLines(g, lines, theme.attr(style), HitEnterPrompt, theme.attr(StyleMessageInfo))
		return Cursor{Row: row, Col: min(end, max(r.Width-1, 0)), Shape: CursorBlock, Visible: r.Width > 0}, true
	}
	if f.Message.Right == "" {
		r.drawText(g, row, 0, r.Width, lines[0], theme.attr(style))
		return Cursor{}, false
	}
	r.drawMessageRight(g, row, 0, r.Width, lines[0], f.Message.Right, theme.attr(style))
	return Cursor{}, false
}

// messageRightMargin は、メッセージの行の右に寄せる文字列（Message.Right）の右に空ける列の数である。
// Neovim 0.12.5 の 'showcmd' がオフのとき（本体には showcmd の欄が無い）の検索の件数と同じく、右の端の 2 列を空ける
// （2026-11-21 に nvim --clean -c 'set noshowcmd' を 50 列の tmux で動かし、"[2/4]" が 44 列目から 48 列目に出ることを確かめた）。
const messageRightMargin = 2

// drawMessageRight は、row 行目の col 列から width 列の範囲に、左の text と、右に寄せた right を描く
// （検索のパターンと一致の件数。FR-167）。今の呼び出し側はメッセージの行だけで、範囲は行の全体である。
// 範囲を受けるのは、コマンドラインと検索の入力をポップアップに出す設定（PR #322 のレビューの推奨 3）で枠の中に描くことを考えたためだが、
// その設定（Frame.CommandLinePopup。FR-215）でも、一致の件数は入力を終えた後のメッセージなので、最後の行に描く（#359 の決め）。
// right は、範囲の右の端の messageRightMargin 列を空けた位置で終わるように置く。text は right の手前の 1 列を空けて収まるところまで描き、
// 収まらない後ろの側は描かない（right を隠さない）。right も収まらないほど狭ければ、right を範囲の左の端から描ける分だけ描く。
//
// req: FR-167
func (r *Renderer) drawMessageRight(g *Grid, row, col, width int, text, right string, a Attr) {
	// 幅は、text と right を描く drawText と同じく、描いている Frame のタブ文字の幅（Frame.TabStop）で数える。
	tabStop := tabStopOr(r.tabStop)
	end := col + width
	start := end - messageRightMargin - StringWidth(right, tabStop)
	if start < col {
		r.drawText(g, row, col, end, right, a)
		return
	}
	r.drawText(g, row, col, max(start-1, col), text, a)
	r.drawText(g, row, start, end, right, a)
}
