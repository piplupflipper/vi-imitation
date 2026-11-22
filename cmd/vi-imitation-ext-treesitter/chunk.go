package main

import (
	"context"
	"encoding/json"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// ブロックの範囲の罫線は、カーソルを含む構文のまとまり（block.go）の範囲を、行頭の字下げの上に重ねる罫線
// （virtualText/set の overlay）で示すものである（基本設計 6.1 の「ブロックの範囲の罫線」。FR-265〜FR-267。
// オーナーの nvim の hlchunk.nvim の chunk に当たる）。
//
// 罫線の形は、hlchunk.nvim（コミット 4ddc056e4ca45f1c7dc6aa21935df710b263b1aa）の lua/hlchunk/mods/chunk/init.lua の
// get_chunk_data（straight の既定の false）に合わせた。
//
//   - まとまりは、カーソルを含むまとまり（blocksAt。テキストオブジェクトの an と同じ単位）のうち、複数の行にわたる最も内側のものである
//     （hlchunk.nvim の get_chunk_range_by_treesitter が、カーソルのノードから親へたどって、始まりと終わりの行が違う最初のノードを採るのと同じ）
//   - 罫線の列（startCol）は、まとまりの始まりの行と終わりの行の字下げの幅の小さい方から、字下げの 1 段の幅（indent.width。Vim の shiftwidth）を
//     引いた列（0 より小さければ 0）である
//   - 始まりの行の字下げが 0 でなければ、startCol から字下げの終わりまでに、左上の角、横線、左向きの記号を描く
//   - 間の行には、startCol に縦線を描く。startCol が行頭の空白の中にある行と、空白だけの行（空の行を含む。行末より右に描く）に描き、
//     startCol より左から本文の始まる行には描かない
//   - 終わりの行の字下げが 0 でなければ、startCol から字下げの終わりまでに、左下の角、横線、右向きの矢印を描く
//
// 描く動き（duration）は、hlchunk.nvim と同じく、始まりの行の本文の側から角へ、縦線を上から下へ、終わりの行の角から矢印へ、
// 1 つの線のように順に描く（get_chunk_data が始まりの行の記号を逆の順に並べ、LoopTask が 1 文字ずつ置く）。

// chunkMinFrameMs は、描く動きの 1 こまの間隔の下限のミリ秒である（仮）。
// hlchunk.nvim は、duration を記号の数で割った間隔で 1 文字ずつ置くので、長いまとまりでは間隔が数ミリ秒になる。
// 1 こまごとに virtualText/set を送り、本体が描き直すので、端末で見分けられる間隔（約 60 こま毎秒）より細かくは送らず、
// 1 こまに複数の記号をまとめる。
const chunkMinFrameMs = 16

// chunkStyle と chunkErrorStyle は、罫線の色の名前である（本体の internal/ui の StyleChunk と StyleChunkError。
// hlchunk.nvim の HLChunk1 と HLChunk2）。
const (
	chunkStyle      = "chunk"
	chunkErrorStyle = "chunk.error"
)

// chunkOptionTimeout は、字下げの幅とタブ文字の幅を本体に問い合わせる（option/get）ときの待ちの上限である。
const chunkOptionTimeout = 2 * time.Second

// chunkCell は、罫線の 1 つのセルである。
type chunkCell struct {
	// row と col は、バッファの行と、行の先頭からの表示の列である。
	row, col int
	// text は、描く記号である（表示幅が 1）。
	text string
	// anchor は、セルを重ねる位置（行の中のバイト）で、pad は、その位置の文字の最初のセルから col までの列数である。
	// タブ文字の途中の列と、空白だけの行の行末より右の列は、pad の空白を前に置いて重ねる。
	anchor, pad int
}

// chunkShape は、1 つのまとまりの罫線である。
type chunkShape struct {
	// start と end はまとまりの始まりと終わりの行、col は罫線の列、beg と fin は始まりと終わりの行の字下げの幅である。
	start, end, col, beg, fin int
	// rows は、cells を作った行の範囲である（窓の見えている行とその前後。chunkView）。
	rows rowRange
	// cells は、rows の中の罫線のセルを、描く順に並べたものである。
	cells []chunkCell
	// style は、罫線の色の名前である。
	style string
}

// sameChunk は、s と o が同じまとまりの同じ形の罫線か（セルを作った行の範囲は比べない）を返す。
func (s chunkShape) sameChunk(o chunkShape) bool {
	return s.start == o.start && s.end == o.end && s.col == o.col && s.beg == o.beg && s.fin == o.fin && s.style == o.style
}

// covers は、窓の見えている行 view のうちまとまりに掛かる行を、s のセルがすべて含むかを返す。
func (s chunkShape) covers(view rowRange) bool {
	lo, hi := max(view.lo, s.start), min(view.hi, s.end)
	return lo > hi || s.rows.lo <= lo && hi <= s.rows.hi
}

// rowRange は、行の範囲 [lo, hi]（両端を含む）である。
type rowRange struct{ lo, hi int }

// allRows は、すべての行である。
var allRows = rowRange{0, int(^uint(0) >> 1)}

// chunkView は、窓の知らせ p から、罫線のセルを作る行の範囲を返す。見えている行と、その前後の 1 画面分の行である。
// 大きなまとまり（10 万行の JSON の全体）でも、こまごとに作って送るセルの数を窓の大きさで抑える（PR #396 のレビューの推奨 2）。
func chunkView(p ext.WindowChangeParams) rowRange {
	rows := max(p.Bottom-p.Top, p.Rows, 1)
	return rowRange{lo: max(p.Top-rows, 0), hi: p.Bottom + rows}
}

// visibleRows は、窓の知らせ p の見えている行である（Bottom は最後まで描いた行の次の行なので、途中まで描いた行も含める）。
func visibleRows(p ext.WindowChangeParams) rowRange {
	return rowRange{lo: p.Top, hi: max(p.Bottom, p.Top)}
}

// indentOptions は、罫線の列を数えるのに使う、バッファの設定の値である。
type indentOptions struct {
	// tab は、タブ文字の表示幅（tab.width）、shift は、字下げの 1 段の幅（indent.width。0 なら tab）である。
	tab, shift int
}

// defaultIndentOptions は、本体に問い合わせられないときの値である（本体の tab.width と indent.width の既定）。
var defaultIndentOptions = indentOptions{tab: 8, shift: 8}

// chunkAt は、カーソル cursor を含むまとまりの罫線のうち、行の範囲 rows の中のセルを返す。まとまりが無ければ ok は false である。
// errorSign が true なら、構文の誤りを含むまとまりの罫線を chunk.error の色にする（hlchunk.nvim の error_sign）。
//
// req: FR-265
func (d *document) chunkAt(cursor ext.Position, rows rowRange, opt indentOptions, sym chunkSymbols, errorSign bool) (chunkShape, bool) {
	if cursor.Line < 0 || cursor.Line >= len(d.lineStarts) {
		return chunkShape{}, false
	}
	off := d.lineStarts[cursor.Line] + min(max(cursor.Byte, 0), d.lineLen(cursor.Line))
	q := span{off, min(off+1, len(d.src))}
	for _, b := range blocksAt(d.tree, d.src, q) {
		start, end := blockRows(&b.node)
		if start == end {
			continue
		}
		shape := d.chunkCells(start, end, rows, opt, sym)
		shape.style = chunkStyle
		if errorSign && b.node.HasError() {
			shape.style = chunkErrorStyle
		}
		return shape, true
	}
	return chunkShape{}, false
}

// blockRows は、まとまりの全体のノード n の始まりと終わりの行を返す。改行で終わるノード（次の行の 0 バイトで終わる）は、その前の行で終わるとする。
func blockRows(n *sitter.Node) (int, int) {
	start, end := int(n.StartPosition().Row), int(n.EndPosition().Row)
	if n.EndPosition().Column == 0 && end > start {
		end--
	}
	return start, end
}

// chunkCells は、行 start から end までのまとまりの罫線を返す。セルは行の範囲 rows の中のものだけを、描く順に並べる（この節の初めのコメント）。
func (d *document) chunkCells(start, end int, rows rowRange, opt indentOptions, sym chunkSymbols) chunkShape {
	beg, fin := d.indentWidth(start, opt.tab), d.indentWidth(end, opt.tab)
	col := max(min(beg, fin)-opt.shift, 0)
	s := chunkShape{start: start, end: end, col: col, beg: beg, fin: fin, rows: rows}
	in := func(row int) bool { return rows.lo <= row && row <= rows.hi }
	if beg > 0 && in(start) {
		top := d.edgeCells(start, col, beg, sym.leftTop, sym.horizontalLine, sym.leftArrow, opt.tab)
		slices.Reverse(top)
		s.cells = append(s.cells, top...)
	}
	for row := max(start+1, rows.lo); row < end && row <= rows.hi; row++ {
		if anchor, pad, ok := d.anchorAt(row, col, opt.tab); ok {
			s.cells = append(s.cells, chunkCell{row: row, col: col, text: sym.verticalLine, anchor: anchor, pad: pad})
		}
	}
	if fin > 0 && in(end) {
		s.cells = append(s.cells, d.edgeCells(end, col, fin, sym.leftBottom, sym.horizontalLine, sym.rightArrow, opt.tab)...)
	}
	return s
}

// edgeCells は、行 row の列 from から to の前までに、角 corner、横線 line、端 tip を左から並べたセルを返す。
// 列が 1 つなら角だけにする（hlchunk.nvim は角と端の 2 文字を置き、端が本文の最初の文字に重なる）。
func (d *document) edgeCells(row, from, to int, corner, line, tip string, tab int) []chunkCell {
	var cells []chunkCell
	for col := from; col < to; col++ {
		text := line
		switch col {
		case from:
			text = corner
		case to - 1:
			text = tip
		}
		anchor, pad, ok := d.anchorAt(row, col, tab)
		if !ok {
			break
		}
		cells = append(cells, chunkCell{row: row, col: col, text: text, anchor: anchor, pad: pad})
	}
	return cells
}

// crWidth は、本体が行末の復帰（CRLF の改行の \r）を描く ^M の列数である。
const crWidth = 2

// lineBody は、行 row の中身から行末の復帰（\r）を除いたものと、復帰があったかを返す。
func (d *document) lineBody(row int) ([]byte, bool) {
	line := d.line(row)
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1], true
	}
	return line, false
}

// indentWidth は、行 row の行頭の空白とタブ文字の表示幅を返す。空白だけの行は、行の全体の幅である（Vim の indent()）。
// 行末の復帰（CRLF の改行）は字下げに数えず、空白だけの行の終わりとして扱う（PR #396 のレビューの推奨 5）。
func (d *document) indentWidth(row, tab int) int {
	w := 0
	body, _ := d.lineBody(row)
	for _, c := range body {
		switch c {
		case ' ':
			w++
		case '\t':
			w += tab - w%tab
		default:
			return w
		}
	}
	return w
}

// anchorAt は、行 row の表示の列 col にセルを重ねる位置を返す。
// col が行頭の空白とタブ文字の中にあれば、その文字のバイトと、文字の最初のセルから col までの列数を返す。
// 行が空白とタブ文字だけで、col が行末より右なら、行末のバイトと、行末から col までの列数を返す。
// col より左から本文が始まる行では ok が false である。
// 行末の復帰（CRLF の改行）は空白と同じに扱い、本体が描く ^M の 2 列として数える（PR #396 のレビューの推奨 5）。
func (d *document) anchorAt(row, col, tab int) (anchor, pad int, ok bool) {
	line, cr := d.lineBody(row)
	defer func() {
		if ok && cr && anchor == len(line) && pad >= crWidth {
			// 復帰より右の列は、復帰の後ろ（行末）に重ねる。
			anchor, pad = len(line)+1, pad-crWidth
		}
	}()
	w := 0
	for i, c := range line {
		var cw int
		switch c {
		case ' ':
			cw = 1
		case '\t':
			cw = tab - w%tab
		default:
			return 0, 0, false
		}
		if col < w+cw {
			return i, col - w, true
		}
		w += cw
	}
	return len(line), col - w, true
}

// chunkOverlays は、罫線のセル cells を、行ごとの overlay の仮想テキストにする。同じ行の続いた列のセルは 1 つの文字列にまとめる。
//
// req: FR-265
func chunkOverlays(cells []chunkCell, style string) []ext.VirtualText {
	byRow := map[int][]chunkCell{}
	var rows []int
	for _, c := range cells {
		if _, ok := byRow[c.row]; !ok {
			rows = append(rows, c.row)
		}
		byRow[c.row] = append(byRow[c.row], c)
	}
	slices.Sort(rows)
	var out []ext.VirtualText
	for _, row := range rows {
		cs := byRow[row]
		slices.SortFunc(cs, func(a, b chunkCell) int { return a.col - b.col })
		for i := 0; i < len(cs); {
			j := i + 1
			for j < len(cs) && cs[j].col == cs[j-1].col+1 {
				j++
			}
			var text strings.Builder
			text.WriteString(strings.Repeat(" ", cs[i].pad))
			for _, c := range cs[i:j] {
				text.WriteString(c.text)
			}
			out = append(out, ext.VirtualText{
				Position:  ext.Position{Line: row, Byte: cs[i].anchor},
				Placement: ext.VirtualTextOverlay,
				Chunks:    []ext.VirtualTextChunk{{Text: text.String(), Style: style}},
			})
			i = j
		}
	}
	return out
}

// chunkBuffer は、1 つのバッファの罫線の状態である。chunker.mu で守る。
type chunkBuffer struct {
	// window は、罫線を決めるカーソルの窓（このバッファを出す窓のうち、最後にカーソルが動いた窓）である。空なら、どの窓も出していない。
	window ext.WindowID
	// cursor は、window のカーソルである。
	cursor ext.Position
	// view は、window の知らせから決めた、罫線のセルを作る行の範囲である（chunkView）。
	view rowRange
	// due は、罫線を決め直す時刻である。ゼロなら決め直さない。
	due time.Time
	// lazy は、決め直す契機がカーソルの移動であることを表す（hlchunk.nvim の render の lazy）。
	// 罫線が前と同じなら描き直さず、違えば描く動きで描く。false（編集の後）なら、描く動きを止めて一度に描き直す。
	lazy bool
	// shape と version は、最後に決めた罫線と、それを決めた構文木のバージョン番号である。drawn が false なら、罫線を出していない。
	shape   chunkShape
	version ext.Version
	drawn   bool
	// anim は、描いている途中の動きである。nil なら、shape をすべて描いてある。
	anim *chunkAnim
}

// chunkAnim は、罫線を描く動きである。
type chunkAnim struct {
	// frame は描いたこまの数、frames はこまの数、interval はこまの間隔、next は次のこまを描く時刻である。
	frame, frames int
	interval      time.Duration
	next          time.Time
}

// chunker は、バッファごとのブロックの範囲の罫線を作って送る。
//
// window/change のフック（ループの上）と、解析の goroutine の parsed は、決め直す時刻を覚えて罫線を作る goroutine（work）を起こすだけである。
// 罫線を作る goroutine は、解析の goroutine が最後に解析した構文木の複製（treeStore.latest）で罫線を決め、描く動きのこまを送る。
// 仮想テキストはバッファごとなので（virtualText/set）、同じバッファを出す窓が複数あれば、最後にカーソルが動いた窓のカーソルで決める
// （hlchunk.nvim の extmark もバッファの名前空間に置くので、同じバッファの窓では、カーソルを動かした窓の罫線が両方に出る）。
type chunker struct {
	trees *treeStore
	log   *log.Logger
	// send は virtualText/set を送る関数、getOptions は option/get を送る関数である。register が Server のメソッドを入れる。
	send       func(ext.VirtualTextSetParams) error
	getOptions func(context.Context, ext.OptionGetParams) (ext.OptionGetResult, error)

	mu sync.Mutex
	// settings は、設定の chunk の値である。configure が入れる。mu で守る。
	settings chunkSettings
	// windows は、窓ごとの最後の知らせである。mu で守る。
	windows map[ext.WindowID]ext.WindowChangeParams
	// bufs は、罫線を決めるバッファの状態である。mu で守る。
	bufs map[ext.BufferID]*chunkBuffer
	// warned は、option/get の誤りをログに書いたかである。罫線を作る goroutine だけが触る。
	warned bool
	wake   chan struct{} // 決め直す時刻を変えたことを罫線を作る goroutine に知らせる。容量 1
	stop   chan struct{}
	done   chan struct{} // 罫線を作る goroutine が終わったら閉じる。start の前は nil
}

func newChunker(trees *treeStore, logger *log.Logger) *chunker {
	return &chunker{
		trees:    trees,
		log:      logger,
		settings: defaultChunkSettings,
		windows:  map[ext.WindowID]ext.WindowChangeParams{},
		bufs:     map[ext.BufferID]*chunkBuffer{},
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
	}
}

// register は、送る関数を s から入れ、罫線を作る goroutine を動かし始める。
func (c *chunker) register(s *server.Server) {
	c.send = s.SetVirtualTexts
	c.getOptions = s.GetOptions
	c.done = make(chan struct{})
	go c.work()
}

// close は、罫線を作る goroutine を止めて終わりを待つ。
func (c *chunker) close() {
	if c.done != nil {
		close(c.stop)
		<-c.done
		c.done = nil
	}
}

// configure は、設定の chunk の値を入れる。initialize の中（窓の知らせより前）で呼ぶ。
func (c *chunker) configure(s chunkSettings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = s
}

// windowChanged は、window/change を覚え、カーソルが動いたか窓のバッファが替わったら、delay の後に罫線を決め直させる。ループの上で呼ぶ。
// 見えている範囲だけが変わった（スクロール）ときは、ふつうは決め直さない。罫線はバッファの位置に重ねるので、本体が本文と一緒に送る。
// ただし、送ったセルは窓の見えている行とその前後だけ（chunkView）なので、見えている行が送ったセルの行の外へ出たら、待たずに一度に描き直させる。
// 窓が無くなったか、窓のバッファが替わって、前のバッファを出す窓が無くなったら、そのバッファの罫線を消させる。
//
// req: FR-265, FR-266
func (c *chunker) windowChanged(p ext.WindowChangeParams) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.settings.enable {
		return
	}
	prev, had := c.windows[p.Window]
	if p.Closed {
		delete(c.windows, p.Window)
		if had {
			c.leaveLocked(prev.Buffer, p.Window)
		}
		return
	}
	c.windows[p.Window] = p
	if had && prev.Buffer != p.Buffer {
		c.leaveLocked(prev.Buffer, p.Window)
	}
	if had && prev.Buffer == p.Buffer && prev.Cursor == p.Cursor {
		if b, ok := c.bufs[p.Buffer]; ok && b.window == p.Window {
			b.view = chunkView(p)
			if b.drawn && !b.shape.covers(visibleRows(p)) {
				c.scheduleLocked(b, false)
			}
		}
		return
	}
	b := c.bufferLocked(p.Buffer)
	b.window, b.cursor, b.view = p.Window, p.Cursor, chunkView(p)
	c.scheduleLocked(b, true)
}

// leaveLocked は、窓 w がバッファ id を出さなくなったことを覚える。w が罫線を決める窓なら、同じバッファを出すほかの窓に替えて決め直させ、
// ほかに無ければ罫線を消させる。mu を取って呼ぶ。
func (c *chunker) leaveLocked(id ext.BufferID, w ext.WindowID) {
	b, ok := c.bufs[id]
	if !ok || b.window != w {
		return
	}
	b.window = ""
	for _, wid := range slices.Sorted(maps.Keys(c.windows)) {
		if p := c.windows[wid]; p.Buffer == id {
			b.window, b.cursor, b.view = wid, p.Cursor, chunkView(p)
			break
		}
	}
	c.scheduleLocked(b, b.window != "")
}

// parsed は、バッファ id の構文木が変わった（解析し直した、捨てた）ことを知らせ、待たずに一度に罫線を決め直させる
// （hlchunk.nvim の TextChanged の render。lazy が false）。解析の goroutine で呼ぶ。
//
// req: FR-266
func (c *chunker) parsed(id ext.BufferID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, ok := c.bufs[id]; ok {
		c.scheduleLocked(b, false)
	}
}

// bufferLocked は、バッファ id の状態を返す。無ければ作る。mu を取って呼ぶ。
func (c *chunker) bufferLocked(id ext.BufferID) *chunkBuffer {
	b, ok := c.bufs[id]
	if !ok {
		b = &chunkBuffer{}
		c.bufs[id] = b
	}
	return b
}

// scheduleLocked は、b の罫線を決め直させる。lazy（カーソルの移動）なら delay の後に、そうでなければすぐに決め直す。
// delay の間にカーソルがまた動いたら、そこから delay を待ち直す（hlchunk.nvim の debounce）。
// すぐに決め直すことになっている間のカーソルの移動は、待たせずにすぐ決め直させたまま、カーソルの移動として扱わない。mu を取って呼ぶ。
func (c *chunker) scheduleLocked(b *chunkBuffer, lazy bool) {
	now := time.Now()
	switch {
	case !lazy:
		b.due, b.lazy = now, false
	case b.due.IsZero() || b.lazy:
		b.due, b.lazy = now.Add(time.Duration(c.settings.delay)*time.Millisecond), true
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// chunkTask は、罫線を作る goroutine が 1 回に行う、1 つのバッファの仕事である。
type chunkTask struct {
	id ext.BufferID
	// frame が true なら描く動きの次のこまを送り、false なら罫線を決め直す。
	frame bool
	// window、cursor、view、lazy は、決め直すときの窓とカーソルと、セルを作る行の範囲と、契機である。
	window ext.WindowID
	cursor ext.Position
	view   rowRange
	lazy   bool
}

// work は、罫線を作る goroutine の本体である。stop が閉じるまで、時刻の来た仕事をして、次の時刻まで待つ。
func (c *chunker) work() {
	defer close(c.done)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		tasks, next := c.dueTasks(time.Now())
		for _, t := range tasks {
			if t.frame {
				c.nextFrame(t.id)
			} else {
				c.render(t)
			}
		}
		if len(tasks) > 0 {
			continue
		}
		var tick <-chan time.Time
		if !next.IsZero() {
			timer.Reset(time.Until(next))
			tick = timer.C
		}
		select {
		case <-c.stop:
			return
		case <-c.wake:
		case <-tick:
		}
		timer.Stop()
	}
}

// dueTasks は、時刻 now までに来た仕事を返し、次の仕事の時刻を返す（無ければゼロ）。1 つのバッファの仕事は 1 回に 1 つで、決め直しを先にする。
func (c *chunker) dueTasks(now time.Time) ([]chunkTask, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var tasks []chunkTask
	var next time.Time
	later := func(t time.Time) {
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	for id, b := range c.bufs {
		switch {
		case !b.due.IsZero() && !b.due.After(now):
			tasks = append(tasks, chunkTask{id: id, window: b.window, cursor: b.cursor, view: b.view, lazy: b.lazy})
			b.due = time.Time{}
		case b.anim != nil && !b.anim.next.After(now):
			tasks = append(tasks, chunkTask{id: id, frame: true})
		default:
			if !b.due.IsZero() {
				later(b.due)
			}
			if b.anim != nil {
				later(b.anim.next)
			}
		}
	}
	return tasks, next
}

// render は、t のバッファの罫線を決め直し、変わったら送る。ループの上では呼ばない（option/get の応答を待つ）。
func (c *chunker) render(t chunkTask) {
	c.mu.Lock()
	settings := c.settings
	c.mu.Unlock()
	var shape chunkShape
	var version ext.Version
	ok := false
	if t.window != "" {
		if d, v := c.trees.latest(t.id); d != nil {
			// 大きいファイルでは描かない（hlchunk.nvim の max_file_size。ファイルの大きさの代わりに全文のバイト数で比べる）。
			if settings.maxFileSize == 0 || len(d.src) < settings.maxFileSize {
				shape, ok = d.chunkAt(t.cursor, t.view, c.indentOptions(t.id), settings.symbols, settings.errorSign)
			}
			version = v
			d.close()
		}
	}
	c.mu.Lock()
	b, exists := c.bufs[t.id]
	if !exists {
		c.mu.Unlock()
		return
	}
	// 送るセル（cells）は、決めた後に変えないスライスを指すので、仮想テキストはロックの外で作る（PR #396 のレビューの推奨 2）。
	erase, send := false, false
	var cells []chunkCell
	switch {
	case !ok || len(shape.cells) == 0:
		// まとまりが無ければ消す（hlchunk.nvim の NO_CHUNK）。
		erase = b.drawn
		b.drawn, b.anim, b.shape = false, nil, chunkShape{}
		if b.window == "" && b.due.IsZero() {
			delete(c.bufs, t.id)
		}
	case t.lazy && b.drawn && b.shape.sameChunk(shape) && b.shape.rows == shape.rows:
		// カーソルが同じまとまりの中で動いただけなら描き直さない。描く動きの途中なら続ける（hlchunk.nvim の render の lazy）。
	case t.lazy && b.drawn && b.shape.sameChunk(shape):
		// 同じまとまりで、セルを作る行の範囲だけが変わった（スクロールした）なら、描く動きをせずに今の範囲のセルを描く。
		b.shape, b.version, b.anim = shape, version, nil
		send, cells = true, shape.cells
	default:
		wasDrawn := b.drawn
		b.shape, b.version, b.drawn, b.anim = shape, version, true, nil
		if t.lazy && settings.delay > 0 && settings.duration > 0 {
			b.anim = newChunkAnim(len(shape.cells), settings.duration)
			// 前の罫線を消してから描き始める（hlchunk.nvim は描く動きの前に名前空間の全体を消す）。
			erase = wasDrawn
		} else {
			send, cells = true, shape.cells
		}
	}
	c.mu.Unlock()
	switch {
	case send:
		c.publish(&ext.VirtualTextSetParams{Buffer: t.id, Version: version, VirtualTexts: chunkOverlays(cells, shape.style)})
	case erase:
		c.publish(&ext.VirtualTextSetParams{Buffer: t.id, Version: version})
	}
}

// newChunkAnim は、n 個のセルを duration ミリ秒で描く動きを作る。こまの間隔は chunkMinFrameMs より短くしない。
//
// req: FR-266
func newChunkAnim(n, duration int) *chunkAnim {
	frames := max(min(n, duration/chunkMinFrameMs), 1)
	interval := time.Duration(duration) * time.Millisecond / time.Duration(frames)
	return &chunkAnim{frames: frames, interval: interval, next: time.Now().Add(interval)}
}

// shownCells は、frames こまのうち frame こまを描いたときに見せるセルの数である。n 個のセルを、こまごとに等しく増やす。
func shownCells(n, frame, frames int) int {
	return (n*frame + frames - 1) / frames
}

// nextFrame は、バッファ id の描く動きの次のこまを送る。
//
// req: FR-266
func (c *chunker) nextFrame(id ext.BufferID) {
	c.mu.Lock()
	b, ok := c.bufs[id]
	if !ok || b.anim == nil {
		c.mu.Unlock()
		return
	}
	a := b.anim
	a.frame++
	cells := b.shape.cells[:shownCells(len(b.shape.cells), a.frame, a.frames)]
	if a.frame >= a.frames {
		b.anim = nil
	} else {
		a.next = a.next.Add(a.interval)
	}
	version, style := b.version, b.shape.style
	c.mu.Unlock()
	// セルのスライスは決めた後に変えないので、仮想テキストはロックの外で作る。
	c.publish(&ext.VirtualTextSetParams{Buffer: id, Version: version, VirtualTexts: chunkOverlays(cells, style)})
}

// publish は、virtualText/set を送る。p が nil なら何もしない。送れなければログに書く。
func (c *chunker) publish(p *ext.VirtualTextSetParams) {
	if p == nil {
		return
	}
	if err := c.send(*p); err != nil {
		c.log.Printf("virtualText/set: %v", err)
	}
}

// indentOptions は、バッファ id のタブ文字の幅と字下げの幅を本体に問い合わせる。問い合わせられなければ本体の既定の値を使う。
func (c *chunker) indentOptions(id ext.BufferID) indentOptions {
	out := defaultIndentOptions
	if c.getOptions == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), chunkOptionTimeout)
	defer cancel()
	res, err := c.getOptions(ctx, ext.OptionGetParams{Buffer: id, Names: []string{"tab.width", "indent.width"}})
	if err != nil {
		if !c.warned {
			c.warned = true
			c.log.Printf("option/get: %v; using tab.width %d and indent.width %d", err, out.tab, out.shift)
		}
		return out
	}
	var tab, shift int
	if json.Unmarshal(res.Values["tab.width"], &tab) == nil && tab > 0 {
		out.tab = tab
	}
	out.shift = out.tab
	if json.Unmarshal(res.Values["indent.width"], &shift) == nil && shift > 0 {
		out.shift = shift
	}
	return out
}
