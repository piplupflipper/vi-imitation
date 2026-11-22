package main

import (
	"context"
	"log"
	"slices"
	"strings"
	"sync"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// 見出しの帯は、窓の最初の行より上で始まり、カーソルの行まで続く構文のまとまり（block.go）の見出しの行を、
// 窓の上端の帯（band/set）に外側から順に出すものである（基本設計 6.1 の「見出しの帯」。FR-245〜FR-248。
// オーナーの nvim の nvim-treesitter-context に当たる）。
//
// 帯の行は次のとおりに決める。nvim-treesitter-context（コミット 8decd73460d26ef0a0befb745d022dd1fc2c38b8）の
// lua/treesitter-context/context.lua の get（mode の既定の cursor）に合わせ、言語ごとのクエリ（queries/<言語>/context.scm）の代わりに、
// テキストオブジェクトの an と同じ構文のまとまりを使う。
//
//   - 見出しの候補は、カーソルの位置を含むまとまり（blocksAt）の見出しである。まとまりの見出し（headings）は、まとまりの最初の行から、
//     カーソルを含む容れ物（関数の複数行の引数の並びと本体のように、1 つのまとまりは複数の容れ物を持つ）の中身（bodySpan）の始まりの前の行までで、
//     中身が行の途中から始まるなら（func f() { x := 1）その行を含む。カーソルの行とその後の行は含めない。
//     カーソルが節（else、elif の本体。clauseRow）の中にあれば、まとまりの見出しはその前の節でない容れ物（if の本体）までにし、
//     節の始まりの行（} else {、elif x:）からその節の中身の前までを、もう 1 つの見出しにする
//   - 見出しの行の末尾の空白だけの行は除き、1 つの見出しは multilineThreshold の行までにする（context.lua の get_text_for_range）
//   - 帯の行数の上限は、設定の maxLines（0 は上限なし）と、カーソルの行から窓の最初の行を引いた数の小さい方である（calc_max_lines）。
//     本体の帯の行数の上限（ext.MaxBandLines）も超えない
//   - 外側の見出しから順に、見出しの最初の行が「窓の最初の行 + それまでに採った見出しの行数（上限で抑える）」より上にあるものを採る。
//     同じ行で始まる見出しは内側のものを採る
//   - 採った行数が上限を超えたら、外側の見出しから削る（trim_scope の既定の outer）。削る見出しが上限より長ければ、その見出しの後ろの行を削る
//
// 帯の行は、バッファの行の全体を、本文と同じ構文の色（paint）を付けて送る。行に当たるバッファの行も書くので、本体は帯の行番号を描き、
// 帯を押すとその行へ移る（FR-238）。

// heading は、帯に出す 1 つの見出しで、バッファの行の並び [start, end) である。
type heading struct{ start, end int }

func (h heading) rows() int { return h.end - h.start }

// stickyWindow は、見出しの帯の 1 つの窓の状態である。
type stickyWindow struct {
	// params は、最後に受け取った window/change である。
	params ext.WindowChangeParams
	// sent は、最後に送った帯である。送っていないか、空の帯を送ったなら nil である。
	sent *ext.BandSetParams
}

// sticky は、窓ごとの見出しの帯を作って送る。
//
// window/change のフック（ループの上）は、窓の知らせを覚えて帯を作る goroutine（work）を起こすだけで、構文木を触らない。
// 帯を作る goroutine は、解析の goroutine が最後に解析した構文木の複製（treeStore.latest）で帯を作る。
// 知らせのバージョン番号の構文木を待たないので、解析の途中なら 1 つ前の構文木で作り、帯にはその構文木のバージョン番号を書く
// （本体は、今のバージョン番号と違う帯を押しても移らない。FR-238）。解析が終わったら（parsed）作り直す。
type sticky struct {
	trees *treeStore
	log   *log.Logger
	// send は band/set を送る関数である。server が Server.SetBand を入れる。
	send func(ext.BandSetParams) error

	mu sync.Mutex
	// settings は、設定の context の値である。configure が入れる。mu で守る。
	settings contextSettings
	// enabled は、帯を出すかである。設定の enable から始め、切り替えのコマンド（toggle）で変わる。mu で守る。
	enabled bool
	// windows は、窓ごとの状態である。mu で守る。
	windows map[ext.WindowID]*stickyWindow
	// dirty は、帯を作り直す窓である。mu で守る。
	dirty map[ext.WindowID]bool
	wake  chan struct{} // dirty に足したことを帯を作る goroutine に知らせる。容量 1
	stop  chan struct{}
	done  chan struct{} // 帯を作る goroutine が終わったら閉じる。start の前は nil
}

func newSticky(trees *treeStore, logger *log.Logger) *sticky {
	return &sticky{
		trees:    trees,
		log:      logger,
		settings: defaultContextSettings,
		enabled:  defaultContextSettings.enable,
		windows:  map[ext.WindowID]*stickyWindow{},
		dirty:    map[ext.WindowID]bool{},
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
	}
}

// stickyToggleCommand は、見出しの帯を出すかを切り替えるコマンドの ID である（FR-248。nvim-treesitter-context の :TSContext toggle）。
const stickyToggleCommand = "treesitter.context.toggle"

// register は、window/change のフックと、切り替えのコマンドを s に登録し、帯を作る goroutine を動かし始める。
func (k *sticky) register(s *server.Server) {
	k.send = s.SetBand
	s.OnWindowChange(func(_ context.Context, e *server.WindowChangeEvent) { k.windowChanged(e.Params) })
	s.Command(stickyToggleCommand, func(context.Context, *server.Buffer, *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
		k.toggle()
		return ext.CommandExecuteResult{OK: true}, nil
	})
	k.done = make(chan struct{})
	go k.work()
}

// close は、帯を作る goroutine を止めて終わりを待つ。
func (k *sticky) close() {
	if k.done != nil {
		close(k.stop)
		<-k.done
		k.done = nil
	}
}

// configure は、設定の context の値を入れる。initialize の中（窓の知らせより前）で呼ぶ。
func (k *sticky) configure(c contextSettings) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.settings = c
	k.enabled = c.enable
}

// windowChanged は、window/change を覚えて、その窓の帯を作り直させる。ループの上で呼ぶ。
// 窓が無くなったら忘れる。本体が、その窓の帯を消す（基本設計 6.7 の「窓の上端の帯」の持ち方）。
//
// req: FR-245
func (k *sticky) windowChanged(p ext.WindowChangeParams) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if p.Closed {
		delete(k.windows, p.Window)
		delete(k.dirty, p.Window)
		return
	}
	w, ok := k.windows[p.Window]
	if !ok {
		w = &stickyWindow{}
		k.windows[p.Window] = w
	}
	w.params = p
	k.markLocked(p.Window)
}

// parsed は、バッファ id の構文木が変わった（解析し直した、捨てた）ことを知らせ、そのバッファを出している窓の帯を作り直させる。
// 解析の goroutine で呼ぶ。
func (k *sticky) parsed(id ext.BufferID) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for wid, w := range k.windows {
		if w.params.Buffer == id {
			k.markLocked(wid)
		}
	}
}

// toggle は、帯を出すかを切り替え、すべての窓の帯を作り直させる（出さないなら消させる）。ループの上で呼ぶ。
//
// req: FR-248
func (k *sticky) toggle() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.enabled = !k.enabled
	for wid := range k.windows {
		k.markLocked(wid)
	}
}

// markLocked は、窓 wid の帯を作り直させる。mu を取って呼ぶ。
func (k *sticky) markLocked(wid ext.WindowID) {
	k.dirty[wid] = true
	select {
	case k.wake <- struct{}{}:
	default:
	}
}

// work は、帯を作る goroutine の本体である。stop が閉じるまで、作り直す窓の帯を作って送る。
func (k *sticky) work() {
	defer close(k.done)
	for {
		select {
		case <-k.stop:
			return
		case <-k.wake:
		}
		k.mu.Lock()
		var todo []ext.WindowChangeParams
		for wid := range k.dirty {
			if w, ok := k.windows[wid]; ok {
				todo = append(todo, w.params)
			}
		}
		clear(k.dirty)
		enabled, settings := k.enabled, k.settings
		k.mu.Unlock()
		for _, p := range todo {
			band := ext.BandSetParams{Window: p.Window, Buffer: p.Buffer, Version: p.Version, Seq: p.Seq}
			if enabled {
				band = k.build(p, settings)
			}
			k.publish(band)
		}
	}
}

// build は、窓の知らせ p の帯を、バッファの最後に解析した構文木で作る。構文木が無ければ（解析しないバッファ）行の空の帯である。
func (k *sticky) build(p ext.WindowChangeParams, c contextSettings) ext.BandSetParams {
	band := ext.BandSetParams{Window: p.Window, Buffer: p.Buffer, Version: p.Version, Seq: p.Seq}
	d, version := k.trees.latest(p.Buffer)
	if d == nil {
		return band
	}
	defer d.close()
	band.Version = version
	band.Lines = d.bandLines(d.contextHeadings(p.Top, p.Cursor, c))
	return band
}

// publish は、帯を送る。窓が無くなったか、後の知らせが届いていれば送らない（作り直しが続く）。
// 最後に送った帯と同じなら送らない。
func (k *sticky) publish(band ext.BandSetParams) {
	k.mu.Lock()
	w, ok := k.windows[band.Window]
	if !ok || w.params.Seq != band.Seq {
		k.mu.Unlock()
		return
	}
	if sameBand(w.sent, band) {
		k.mu.Unlock()
		return
	}
	if len(band.Lines) == 0 {
		w.sent = nil
	} else {
		w.sent = &band
	}
	k.mu.Unlock()
	if err := k.send(band); err != nil {
		k.log.Printf("band/set: %v", err)
	}
}

// sameBand は、最後に送った帯 sent と、これから送る帯 b が同じ中身か（送らなくてよいか）を返す。
// 帯の通し番号（Seq）は比べない。sent が nil なら、b が行の空の帯のとき同じとする。
func sameBand(sent *ext.BandSetParams, b ext.BandSetParams) bool {
	if sent == nil {
		return len(b.Lines) == 0
	}
	return sent.Buffer == b.Buffer && sent.Version == b.Version && slices.EqualFunc(sent.Lines, b.Lines, func(x, y ext.BandLine) bool {
		return x.Text == y.Text && (x.Line == nil) == (y.Line == nil) && (x.Line == nil || *x.Line == *y.Line) && slices.Equal(x.Highlights, y.Highlights)
	})
}

// contextHeadings は、窓の最初の行 top とカーソル cursor から、帯に出す見出しを外側から順に返す（この節の初めのコメント）。
//
// req: FR-245
func (d *document) contextHeadings(top int, cursor ext.Position, c contextSettings) []heading {
	if cursor.Line >= len(d.lineStarts) {
		return nil
	}
	maxLines := cursor.Line - top
	if c.maxLines > 0 {
		maxLines = min(maxLines, c.maxLines)
	}
	maxLines = min(maxLines, ext.MaxBandLines)
	if maxLines <= 0 {
		return nil
	}
	off := d.lineStarts[cursor.Line] + min(max(cursor.Byte, 0), d.lineLen(cursor.Line))
	q := span{off, min(off+1, len(d.src))}
	blocks := blocksAt(d.tree, d.src, q)
	var cands []heading
	for i := len(blocks) - 1; i >= 0; i-- {
		cands = append(cands, d.headings(blocks[i], off, cursor.Line, c.multilineThreshold)...)
	}
	var out []heading
	height := 0
	for _, h := range cands {
		if h.start >= top+min(maxLines, height) {
			continue
		}
		if n := len(out); n > 0 && out[n-1].start == h.start {
			// 同じ行で始まる見出しは、内側のものを採る（context.lua の get）。
			height -= out[n-1].rows()
			out = out[:n-1]
		}
		out = append(out, h)
		height += h.rows()
	}
	// 上限を超えた分を、外側の見出しから削る（context.lua の trim_contexts。trim_scope の既定の outer）。
	for trim := height - maxLines; trim > 0 && len(out) > 0; {
		if n := out[0].rows(); n <= trim {
			out = out[1:]
			trim -= n
			continue
		}
		out[0].end -= trim
		trim = 0
	}
	return out
}

// headings は、まとまり b の見出しを返す（この節の初めのコメント）。off はカーソルのバイト位置、cursorRow はその行、
// threshold は 1 つの見出しの行数の上限である。見出しは、カーソルの行より前の行だけにする。
//
// まとまりの見出しと、カーソルが節の容れ物（else、elif の本体。clauseRow）の中にあれば、その節の見出しの、多くて 2 つである。
// まとまりの見出しの終わりは、次の容れ物の中身の始まりで決める。
//
//   - カーソルが節でない容れ物の中にあれば、その容れ物（関数の複数行の引数の並びの後の本体なら、本体）
//   - カーソルが節の容れ物の中にあれば、それより前の、節でない最後の容れ物（if の本体）
//   - カーソルがどの容れ物の中にも無ければ（見出しの上）、カーソルより後の最初の容れ物
func (d *document) headings(b block, off, cursorRow, threshold int) []heading {
	k := -1
	for i := range b.bodies {
		if nodeSpan(&b.bodies[i]).contains(off) {
			k = i
		}
	}
	clause := -1
	if k >= 0 {
		clause = d.clauseRow(b, k)
	}
	head := -1
	switch {
	case k >= 0 && clause < 0:
		head = k
	case k >= 0:
		for i := range k {
			if d.clauseRow(b, i) < 0 {
				head = i
			}
		}
	default:
		for i := range b.bodies {
			if int(b.bodies[i].StartByte()) > off {
				head = i
				break
			}
		}
	}
	if head < 0 {
		head = len(b.bodies) - 1
	}
	start := int(b.node.StartPosition().Row)
	var out []heading
	add := func(h heading) {
		h.end = min(h.end, cursorRow)
		if h.start < h.end {
			out = append(out, d.trimHeading(h, threshold))
		}
	}
	add(heading{start, d.contentRow(&b.bodies[head], start)})
	if clause > start {
		add(heading{clause, d.contentRow(&b.bodies[k], clause)})
	}
	return out
}

// clauseRow は、まとまり b の i 番目の容れ物が節（else、elif の本体）の中にあれば、その節の始まりの行を返す。節の中に無ければ -1 である。
// 容れ物からまとまりの全体のノードの手前までの祖先（容れ物を含む）のうち、最も近い、親の alternative のフィールドの子か、
// 予約語で閉じる容れ物の中の節（isClause。bash の elif、else）を節とする。
// Go の } else { の本体の block、} else if x { の if 文、Python の elif と else の節が当たる。
func (d *document) clauseRow(b block, i int) int {
	for n := &b.bodies[i]; n != nil && n.Id() != b.node.Id(); n = n.Parent() {
		if inField(n, []string{"alternative"}) || isClause(n, d.src) {
			return int(n.StartPosition().Row)
		}
	}
	return -1
}

// contentRow は、容れ物 c の見出しの次の行を返す。c の中身（bodySpan）が始まる行で、中身がその行の途中から始まる（前に空白でない文字がある）なら
// その次の行である。from より前にはしない（見出しは少なくとも 1 行ある）。
func (d *document) contentRow(c *sitter.Node, from int) int {
	s := d.bodySpan(c).start
	p := d.position(min(s, len(d.src)))
	row := p.Line
	if !isBlankBytes(d.src[d.lineStarts[row]:s]) {
		row++
	}
	return max(row, from+1)
}

// trimHeading は、見出し h の後ろの空白だけの行を除き（少なくとも 1 行は残す）、threshold の行までにする（context.lua の get_text_for_range）。
func (d *document) trimHeading(h heading, threshold int) heading {
	h.end = min(h.end, len(d.lineStarts))
	for h.end-1 > h.start && strings.TrimSpace(string(d.line(h.end-1))) == "" {
		h.end--
	}
	h.end = min(h.end, h.start+max(threshold, 1))
	return h
}

// line は、row 行目の中身（改行を含まない）を返す。
func (d *document) line(row int) []byte {
	return d.src[d.lineStarts[row] : d.lineStarts[row]+d.lineLen(row)]
}

// bandLines は、見出しの行を帯の行にする。行の全体を、本文と同じ構文の色を付けて送る（FR-246）。
// 帯の行に改行を書けないので、行の中の復帰（\r）は空白に替える（バイトの位置は変えない）。
//
// req: FR-245, FR-246
func (d *document) bandLines(hs []heading) []ext.BandLine {
	var lines []ext.BandLine
	for _, h := range hs {
		for row := h.start; row < h.end; row++ {
			from := d.lineStarts[row]
			text := d.line(row)
			paint := d.paint(from, from+len(text))
			var hls []ext.BandHighlight
			for i := 0; i < len(paint); {
				j := i + 1
				for j < len(paint) && paint[j] == paint[i] {
					j++
				}
				if paint[i] != noStyle {
					hls = append(hls, ext.BandHighlight{Start: i, End: j, Style: paint[i].String()})
				}
				i = j
			}
			lines = append(lines, ext.BandLine{Text: strings.ReplaceAll(string(text), "\r", " "), Line: ext.SourceLine(row), Highlights: hls})
		}
	}
	return lines
}
