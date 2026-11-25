package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// minAPIVersion は、範囲に直に色を付けられる（ext.Decoration.Attr）本体の拡張機能 API の版である（#368。基本設計 5.3）。
// これより古い本体は、Attr のある decoration/set の通知の全体を誤りとして捨てるので、送らない。
const minAPIVersion = "0.19.0"

// maxDecorationSetBytes は、1 回の decoration/set の本文の大きさの上限（仮）である。
// tree-sitter の拡張機能の上限（基本設計 6.1。maxDecorationSetBytes）と同じ値にした。
// 超えたら空の一覧を送り、message/show で 1 回だけ知らせる。色コードは疎なので、ふつうのファイルでは当たらない。
const maxDecorationSetBytes = 10 << 20

// extension は、色コードの着色の拡張機能である。
//
// 仕事は 2 つの goroutine に分ける（基本設計 6.12）。
//
//   - フック（OnInitialize、OnBufferOpen、OnBufferChange、OnBufferClose）は、ext/server のメッセージを処理する goroutine
//     （以下、ループ）から呼ばれる（ext/server の Server の doc）。ループは、行の並び（bufState.lines）を編集のとおりに置き換え、
//     置き換わった行を「数える行」（bufState.todo）に積み、送る goroutine を起こすだけにする。行を数えない
//   - 送る goroutine（sendLoop）は、数える行の文字列を x.mu を放して数え、結果を入れてから、一覧を組み立てて decoration/set を送る。
//     数えている間や送っている間に届いた編集は、ループが行の並びに入れ続け、送る goroutine は次の回に最新の行だけを数える
//
// 行を数える時間は行の長さに比例する（1 行が 20 MB の minified の JS で 190 ms ほど。PR #395 のレビュー）。
// ループの上で数えると、編集が続いたときに ping の応答が本体の期限（2 秒。基本設計 5.2）を超えて強制終了されるので、
// ループの外で数える（2.2 の規約）。数えている間に編集で置き換わった行（lineState.dead）の結果は捨てる。
type extension struct {
	srv *server.Server
	log *log.Logger
	// send は、decoration/set を送る。srv.SetDecorations で、テストでは差し替える。
	send func(ext.DecorationSetParams) error
	// notify は、message/show を送る。
	notify func(ext.MessageLevel, string) error

	// cfg、scan、supported は、initialize で入れ、その後は変えない。
	cfg  *config
	scan *scanner
	// supported は、本体が範囲に直に色を付けられる（minAPIVersion 以降）かである。
	supported bool
	// warnedOld は、古い本体を知らせたかである。ループだけが読み書きする。
	warnedOld bool

	// wake は、送る goroutine を起こす。容量は 1 で、既に起こしていれば積まない。
	wake chan struct{}

	// mu は、ここから下のフィールドと、bufState と lineState の中身を守る。ループと送る goroutine の両方が触る。
	// 持っている間は、行を数えず、本体へ送らない。
	mu sync.Mutex
	// bufs は、色を塗る対象のバッファ（と、対象から外れて色を消す前のバッファ）ごとの状態である。
	bufs map[ext.BufferID]*bufState
	// pending は、数える行か送り直しの要るバッファである。
	pending map[ext.BufferID]bool
	// busy は、送る goroutine が pending から取り出したバッファを処理している間 true である。
	busy bool
	// attrs は、色ごとの直の色である。同じ色の範囲は同じポインターを使う（ext.Decoration.Attr の doc）。
	attrs map[uint32]*ext.TextAttr
	// warnedLarge は、大きすぎる一覧を知らせたかである。
	warnedLarge bool

	// scanned は、これまでに数えた行の数である。編集で変わった行だけを数え直すことを、テストで確かめるために数える。
	scanned atomic.Int64
}

// bufState は、1 つのバッファの状態である。
type bufState struct {
	// version は、lines のもとのバッファのバージョン番号である。
	version ext.Version
	// lines は、行の並びである。len(lines) はバッファの行数と同じに保つ。
	lines []*lineState
	// todo は、まだ数えていない行と、その文字列である。編集で置き換わった行（dead）も残りうるので、数える前に除く。
	todo []todoLine
	// total は、数えた行の範囲の数の合計である。
	total int
	// dirty は、最後に送った一覧から変わりうることを表す。送る goroutine は、数える行が無くなった時点で dirty なら送る。
	dirty bool
	// sent は、最後に送った一覧が空でないかである。空の一覧を続けて送らないために使う。
	sent bool
	// off は、ファイルの種類が対象から外れたことを表す。lines は空で、送る goroutine が空の一覧を送ったら bufs から除く。
	off bool
}

// lineState は、1 つの行である。行の中身が変わるたびに新しく作り、置き換えた前の行は dead にする。
// 行の数だけ持つので、小さく保つ（行の文字列は todoLine に、編集の手がかりは editHint に分ける）。
type lineState struct {
	// spans は、数えた範囲である。scanned になった後は変えない。
	spans []span
	// hint は、長い行を編集の周りだけ数え直す手がかりである。無ければ nil。数えたら nil にする。
	hint    *editHint
	scanned bool
	// dead は、編集でバッファから外れたことを表す。数えた結果は捨てる。
	dead bool
	// fresh は、編集の途中で作り、まだ数える行に積んでいないことを表す（change が全部の編集の後に積む）。
	fresh bool
}

// editHint は、行が、数えた行 base の [start, oldEnd) を [start, newEnd) に置き換えたものであることを表す。
// 長い行（longLineBytes を超える行）は、base の範囲から編集の周りだけを数え直す（rescanAround）。
type editHint struct {
	base                  *lineState
	start, oldEnd, newEnd int
}

// todoLine は、数える行と、その文字列である（文字列は ext/server の複製と共有するので、写さない）。
type todoLine struct {
	l    *lineState
	text string
}

// newExtension は、フックを登録した拡張機能を作る。送る goroutine は initialize で起こす。
func newExtension() *extension {
	x := &extension{
		srv:     server.New(),
		log:     log.New(os.Stderr, "vi-imitation-ext-colorizer: ", 0),
		wake:    make(chan struct{}, 1),
		bufs:    map[ext.BufferID]*bufState{},
		pending: map[ext.BufferID]bool{},
		attrs:   map[uint32]*ext.TextAttr{},
	}
	x.send = x.srv.SetDecorations
	x.notify = x.srv.ShowMessage
	x.srv.OnInitialize(func(_ context.Context, e *server.InitializeEvent) error {
		cfg, err := parseSettings(e.Settings)
		if err != nil {
			return err
		}
		x.configure(cfg, e.APIVersion)
		go x.sendLoop()
		return nil
	})
	x.srv.OnBufferOpen(func(_ context.Context, e *server.BufferOpenEvent) { x.open(e.Buffer) })
	x.srv.OnBufferChange(func(_ context.Context, e *server.BufferChangeEvent) { x.change(e) })
	x.srv.OnBufferClose(func(_ context.Context, e *server.BufferCloseEvent) { x.closeBuffer(e.Buffer) })
	return x
}

// configure は、initialize で読んだ設定と本体の API の版を入れる。
func (x *extension) configure(cfg *config, apiVersion string) {
	x.cfg = cfg
	x.scan = newScanner(cfg.formats)
	x.supported = !ext.VersionLess(apiVersion, minAPIVersion)
}

// open は、buffer/open で全文を受け取ったバッファの全部の行を、数える行に積む。
// ファイルの種類が対象でなくなった（:set filetype= で変えた）バッファは、送った色を消して忘れる。
//
// req: FR-261
func (x *extension) open(b *server.Buffer) {
	target := x.cfg != nil && x.cfg.targets(b.FileType())
	if target && !x.supported {
		if !x.warnedOld {
			x.warnedOld = true
			x.message(ext.MessageWarning, fmt.Sprintf("colorizer: 本体の拡張機能 API の版が %s より古いので、色コードを塗らない", minAPIVersion))
		}
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	st := x.bufs[b.ID()]
	if !target {
		if st != nil {
			killLines(st.lines)
			*st = bufState{version: b.Version(), sent: st.sent, off: true, dirty: true}
			x.markLocked(b.ID())
		}
		return
	}
	if st == nil {
		st = &bufState{}
		x.bufs[b.ID()] = st
	}
	st.off = false
	x.resetLinesLocked(st, b)
	x.markLocked(b.ID())
}

// resetLinesLocked は、st の行の並びを、バッファ b の全部の行の、まだ数えていない行で作り直す。x.mu を持って呼ぶ。
// 行の数に比例する（行ごとの小さな値を 1 回の割り当てで作るだけで、行は数えない）。
func (x *extension) resetLinesLocked(st *bufState, b *server.Buffer) {
	killLines(st.lines)
	n := b.LineCount()
	block := make([]lineState, n)
	st.lines = make([]*lineState, n)
	st.todo = make([]todoLine, n)
	for i := range block {
		st.lines[i] = &block[i]
		st.todo[i] = todoLine{l: &block[i], text: b.Line(i)}
	}
	st.version = b.Version()
	st.total = 0
	st.dirty = true
}

// killLines は、lines の行をバッファから外れたものにする。
func killLines(lines []*lineState) {
	for _, l := range lines {
		l.dead = true
	}
}

// change は、buffer/change の編集のとおりに行の並びを置き換え、置き換わった行を数える行に積む。行は数えない。
//
// 編集は逐次の数え方（ext.BufferChangeParams）なので、先頭から順に、編集の範囲の行を、入れたテキストの行の数の新しい行に置き換える。
// 後の編集が前の編集の行を置き換えても、数えるのは最後に残った行だけになる。新しい行の文字列は、全部の編集の後にバッファの複製から入れる。
// 1 行の中だけの編集なら、新しい行に、前の行（数えていれば。数えていなければ、前の行が持つ手がかり）と編集の位置を手がかりとして残す。
// 長い行は、送る goroutine がこの手がかりで編集の周りだけを数え直す（rescanAround）。
// 置き換えた行に範囲が無く、行の数も変わらず、新しい行にも範囲が無ければ、送らない（本体は前の一覧のまま描く。基本設計 6.1）。
// 行の数が変わったら、範囲のある行がずれるので、範囲が 1 つでもあれば送る。
//
// req: FR-262
func (x *extension) change(e *server.BufferChangeEvent) {
	b := e.Buffer
	x.mu.Lock()
	defer x.mu.Unlock()
	st := x.bufs[b.ID()]
	if st == nil || st.off {
		return
	}
	// lo と hi は、新しい行（fresh）を含む行の範囲 [lo, hi) である。後の編集で行がずれたら hi もずらす。
	lo, hi := len(st.lines), 0
	for _, ed := range e.Edits {
		start, end := ed.Range.Start.Line, ed.Range.End.Line
		if start < 0 || end < start || end >= len(st.lines) {
			// 複製と行の数がずれた。ext/server が適用できた編集なので起きないはずだが、全部を数え直して立て直す。
			x.resetLinesLocked(st, b)
			x.markLocked(b.ID())
			return
		}
		removed := st.lines[start : end+1]
		for _, l := range removed {
			if l.scanned && len(l.spans) > 0 {
				st.dirty = true
				st.total -= len(l.spans)
			}
		}
		added := strings.Count(ed.Text, "\n") + 1
		if added != len(removed) && st.total > 0 {
			st.dirty = true
		}
		repl := make([]*lineState, added)
		for i := range repl {
			repl[i] = &lineState{fresh: true}
		}
		if start == end && added == 1 {
			hintEdit(repl[0], removed[0], ed.Range.Start.Byte, ed.Range.End.Byte, ed.Range.Start.Byte+len(ed.Text))
		}
		killLines(removed)
		st.lines = slices.Replace(st.lines, start, end+1, repl...)
		if hi > end {
			hi += added - len(removed)
		}
		lo, hi = min(lo, start), max(hi, start+added)
	}
	st.version = b.Version()
	if len(st.lines) != b.LineCount() {
		x.resetLinesLocked(st, b)
		x.markLocked(b.ID())
		return
	}
	for i := lo; i < min(hi, len(st.lines)); i++ {
		if l := st.lines[i]; l.fresh {
			l.fresh = false
			st.todo = append(st.todo, todoLine{l: l, text: b.Line(i)})
		}
	}
	if len(st.todo) > 0 || st.dirty {
		x.markLocked(b.ID())
	}
}

// hintEdit は、行 old の [start, oldEnd) を [start, newEnd) に置き換えた新しい行 l に、編集の周りだけを数え直す手がかりを入れる。
// old が数えた行なら old を、まだ数えていない行で手がかりを持つなら、その手がかりの編集とこの編集を合わせたものを残す。
func hintEdit(l, old *lineState, start, oldEnd, newEnd int) {
	switch h := old.hint; {
	case old.scanned:
		l.hint = &editHint{base: old, start: start, oldEnd: oldEnd, newEnd: newEnd}
	case h != nil:
		// old は h.base の [s1, o1) を [s1, n1) に置き換えたもの。old の [start, oldEnd) を [start, newEnd) に置き換えると、
		// h.base から見て、min(s1, start) から始まり、old の位置 max(n1, oldEnd) で終わる範囲を置き換えたことになる。
		// old の位置 max(n1, oldEnd) は、h.base では n1 - o1 だけ前、l では newEnd - oldEnd だけ後ろになる。
		endOld := max(h.newEnd, oldEnd)
		l.hint = &editHint{
			base:   h.base,
			start:  min(h.start, start),
			oldEnd: endOld - (h.newEnd - h.oldEnd),
			newEnd: endOld + (newEnd - oldEnd),
		}
	}
}

// closeBuffer は、閉じたバッファを忘れる。本体はバッファを閉じたら色も捨てるので、空の一覧は送らない。
func (x *extension) closeBuffer(id ext.BufferID) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if st := x.bufs[id]; st != nil {
		killLines(st.lines)
	}
	delete(x.bufs, id)
	delete(x.pending, id)
}

// markLocked は、バッファ id を処理の要るものにして、送る goroutine を起こす。x.mu を持って呼ぶ。
func (x *extension) markLocked(id ext.BufferID) {
	x.pending[id] = true
	select {
	case x.wake <- struct{}{}:
	default:
	}
}

// sendLoop は、起こされるたびに、処理の要るバッファを 1 つずつ処理する。プロセスが終わるまで続ける。
// 数えている間と送っている間に同じバッファが何度変わっても、処理の印は 1 つなので、終えた後に最新の状態で 1 回だけ処理する。
//
// req: FR-262
func (x *extension) sendLoop() {
	for range x.wake {
		for x.sendOne() {
		}
	}
}

// sendOne は、処理の要るバッファを 1 つ取り出して処理する（process）。無ければ false を返す。
func (x *extension) sendOne() bool {
	x.mu.Lock()
	var id ext.BufferID
	found := false
	for k := range x.pending {
		id, found = k, true
		break
	}
	if !found {
		x.busy = false
		x.mu.Unlock()
		return false
	}
	delete(x.pending, id)
	x.busy = true
	st := x.bufs[id]
	x.mu.Unlock()
	if st != nil {
		x.process(id, st)
	}
	return true
}

// scanJob は、送る goroutine が x.mu を放して数える 1 つの行である。
type scanJob struct {
	l                     *lineState
	text                  string
	base                  []span
	start, oldEnd, newEnd int
}

// process は、バッファ st の数える行を、無くなるまで数え、数える行が無くなった時点で一覧が変わりうるなら送る。
// 行は x.mu を放して数え、結果は、その行がまだバッファにあるときだけ入れる（数えている間に編集で置き換わった行の結果は捨てる）。
func (x *extension) process(id ext.BufferID, st *bufState) {
	for {
		x.mu.Lock()
		if x.bufs[id] != st {
			x.mu.Unlock()
			return
		}
		jobs := x.takeJobsLocked(st)
		if len(jobs) == 0 {
			x.sendLocked(id, st)
			return
		}
		x.mu.Unlock()

		results := make([][]span, len(jobs))
		for k, j := range jobs {
			if j.base != nil && len(j.text) > longLineBytes {
				results[k] = x.scan.rescanAround(j.text, j.base, j.start, j.oldEnd, j.newEnd)
			} else {
				results[k] = x.scan.scanLine(j.text)
			}
		}
		x.scanned.Add(int64(len(jobs)))

		x.mu.Lock()
		for k, j := range jobs {
			l := j.l
			if l.dead || l.scanned {
				continue
			}
			l.spans, l.scanned, l.hint = results[k], true, nil
			if len(l.spans) > 0 {
				st.total += len(l.spans)
				st.dirty = true
			}
		}
		x.mu.Unlock()
	}
}

// takeJobsLocked は、st の数える行のうち、まだバッファにあって数えていないものを取り出す。x.mu を持って呼ぶ。
func (x *extension) takeJobsLocked(st *bufState) []scanJob {
	todo := st.todo
	st.todo = nil
	jobs := make([]scanJob, 0, len(todo))
	for _, t := range todo {
		l := t.l
		if l.dead || l.scanned {
			continue
		}
		j := scanJob{l: l, text: t.text}
		if h := l.hint; h != nil {
			j.base, j.start, j.oldEnd, j.newEnd = h.base.spans, h.start, h.oldEnd, h.newEnd
		}
		jobs = append(jobs, j)
	}
	return jobs
}

// sendLocked は、st の一覧が変わりうるなら送る。x.mu を持って呼び、放して戻る。
// 一覧は x.mu を持って組み立て、符号化と送信と message/show は x.mu を放してから行う。
func (x *extension) sendLocked(id ext.BufferID, st *bufState) {
	if !st.dirty || st.total == 0 && !st.sent {
		st.dirty = false
		if st.off {
			delete(x.bufs, id)
		}
		x.mu.Unlock()
		return
	}
	st.dirty = false
	p := ext.DecorationSetParams{Buffer: id, Version: st.version, Decorations: x.decorationsLocked(st)}
	x.mu.Unlock()

	tooLarge := p.JSONSize() > maxDecorationSetBytes
	if tooLarge {
		p.Decorations = nil
	}
	err := x.send(p)

	x.mu.Lock()
	warn := tooLarge && !x.warnedLarge
	if warn {
		x.warnedLarge = true
	}
	if err == nil && x.bufs[id] == st {
		st.sent = len(p.Decorations) > 0
		if st.off && !x.pending[id] {
			delete(x.bufs, id)
		}
	}
	x.mu.Unlock()
	if err != nil {
		x.log.Printf("decoration/set: %v", err)
	}
	if warn {
		x.message(ext.MessageWarning, "colorizer: 色コードが多すぎるので、このバッファの色コードを塗らない")
	}
}

// decorationsLocked は、st の範囲の一覧を作る。x.mu を持って呼ぶ。数える行が無い（どの行も数えた）ときに呼ぶ。
func (x *extension) decorationsLocked(st *bufState) []ext.Decoration {
	decs := make([]ext.Decoration, 0, st.total)
	for line, l := range st.lines {
		for _, sp := range l.spans {
			decs = append(decs, ext.Decoration{
				Range: ext.Range{Start: ext.Position{Line: line, Byte: sp.start}, End: ext.Position{Line: line, Byte: sp.end}},
				Attr:  x.attrLocked(sp.rgb),
			})
		}
	}
	return decs
}

// attrLocked は、色 rgb の範囲に付ける直の色を、設定の塗り方で返す。x.mu を持って呼ぶ。
//
// background の文字の色は、色の明るさ（ITU-R BT.601 の重み 0.299、0.587、0.114 の加重平均を 255 で割ったもの）が
// 0.5 を超えれば brightFg、そうでなければ darkFg にする（nvim-colorizer.lua の background の塗り方と同じ色になることを確かめた）。
//
// req: FR-260
func (x *extension) attrLocked(rgb uint32) *ext.TextAttr {
	if a, ok := x.attrs[rgb]; ok {
		return a
	}
	c := fmt.Sprintf("#%06x", rgb)
	var a *ext.TextAttr
	switch x.cfg.mode {
	case modeForeground:
		a = &ext.TextAttr{Fg: c}
	case modeUnderline:
		a = &ext.TextAttr{Sp: c, Underline: true}
	case modeBackground:
		fg := x.cfg.darkFg
		r, g, b := float64(rgb>>16), float64(rgb>>8&0xff), float64(rgb&0xff)
		if (0.299*r+0.587*g+0.114*b)/255 > 0.5 {
			fg = x.cfg.brightFg
		}
		a = &ext.TextAttr{Fg: fg, Bg: c}
	}
	x.attrs[rgb] = a
	return a
}

// idle は、処理の要るバッファが無く、処理の途中でもないかを返す。テストで送り終えたことを待つのに使う。
func (x *extension) idle() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return len(x.pending) == 0 && !x.busy
}

// message は、本体へ message/show を送る。送れなければログに書く。x.mu を持たずに呼ぶ。
func (x *extension) message(level ext.MessageLevel, text string) {
	if err := x.notify(level, text); err != nil {
		x.log.Printf("message/show: %v", err)
	}
}
