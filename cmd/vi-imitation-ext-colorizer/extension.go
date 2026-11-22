package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strconv"
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
//     （以下、ループ）から呼ばれる（ext/server の Server の doc）。ループは、行ごとの範囲（bufState）を、変わった行だけ
//     数え直して保ち、送り直しの要るバッファを pending に入れて、送る goroutine を起こす
//   - 送る goroutine（sendLoop）は、pending のバッファの一覧を組み立てて decoration/set を送る。送っている間に届いた
//     変更はループが bufState に入れ続け、送り終えたら最新の状態で 1 回だけ送り直す
//
// 一覧の符号化は範囲の数に比例してかかる（2 万の範囲で 10 ms ほど）。ループの上で変更のたびに符号化すると、
// 変更が続いたとき（マクロの再生、キーの連打）にループが遅れ、ping の応答が本体の期限（2 秒。基本設計 5.2）を超えうる。
// 送る goroutine に分けて、送っている間の変更をまとめることで、ループの 1 回の処理を変わった行の数え直しだけにした。
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

	// mu は、ここから下のフィールドを守る。ループと送る goroutine の両方が触る。
	mu sync.Mutex
	// bufs は、色を塗る対象のバッファ（と、対象から外れて色を消す前のバッファ）ごとの状態である。
	bufs map[ext.BufferID]*bufState
	// pending は、送り直しの要るバッファである。
	pending map[ext.BufferID]bool
	// sending は、送る goroutine が pending から取り出した一覧を送っている間 true である。
	sending bool
	// attrs は、色ごとの直の色である。同じ色の範囲は同じポインターを使う（ext.Decoration.Attr の doc）。
	attrs map[uint32]*ext.TextAttr
	// warnedLarge は、大きすぎる一覧を知らせたかである。
	warnedLarge bool

	// scanned は、これまでに数えた行の数である。編集で変わった行だけを数え直すことを、テストで確かめるために数える。
	scanned atomic.Int64
}

// bufState は、1 つのバッファの、行ごとの色コードの範囲である。
type bufState struct {
	// version は、lines を数えたもとのバッファのバージョン番号である。
	version ext.Version
	// lines は、行ごとの範囲である。len(lines) はバッファの行数と同じに保つ。
	lines []lineSpans
	// total は、lines の範囲の数の合計である。
	total int
	// sent は、最後に送った一覧が空でないかである。空の一覧を続けて送らないために使う。
	sent bool
	// off は、ファイルの種類が対象から外れたことを表す。lines は空で、送る goroutine が空の一覧を送ったら bufs から除く。
	off bool
}

// lineSpans は、1 行の範囲である。dirty は、編集で置き換わり、まだ数え直していない行であることを表す。
type lineSpans struct {
	spans []span
	dirty bool
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
	x.supported = !versionLess(apiVersion, minAPIVersion)
}

// open は、buffer/open で全文を受け取ったバッファを、全部の行について数え直して送る。
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
			*st = bufState{version: b.Version(), sent: st.sent, off: true}
			x.markLocked(b.ID())
		}
		return
	}
	if st == nil {
		st = &bufState{}
		x.bufs[b.ID()] = st
	}
	st.off = false
	x.rescanAllLocked(st, b)
	x.markLocked(b.ID())
}

// rescanAllLocked は、st をバッファ b の全部の行から作り直す。x.mu を持って呼ぶ。
func (x *extension) rescanAllLocked(st *bufState, b *server.Buffer) {
	st.version = b.Version()
	st.lines = make([]lineSpans, b.LineCount())
	st.total = 0
	for i := range st.lines {
		st.lines[i].spans = x.scan.scanLine(b.Line(i))
		st.total += len(st.lines[i].spans)
	}
	x.scanned.Add(int64(len(st.lines)))
}

// change は、buffer/change の編集で置き換わった行だけを数え直し、一覧が変わりうるときだけ送り直す。
//
// 編集は逐次の数え方（ext.BufferChangeParams）なので、先頭から順に、編集の範囲の行を、入れたテキストの行の数の
// 数え直していない行に置き換える。後の編集が前の編集の行を置き換えても、数え直すのは最後に残った行だけになる。
// 全部の編集の後に、数え直していない行をバッファの複製から数える。
// 変わった行に前も後も範囲が無く、行の数も変わらなければ、送らない（本体は前の一覧のまま描く。基本設計 6.1）。
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
	needSend := false
	// lo と hi は、数え直していない行を含む行の範囲 [lo, hi) である。後の編集で行がずれたら hi もずらす。
	lo, hi := len(st.lines), 0
	for _, ed := range e.Edits {
		start, end := ed.Range.Start.Line, ed.Range.End.Line
		if start < 0 || end < start || end >= len(st.lines) {
			// 複製と行の数がずれた。ext/server が適用できた編集なので起きないはずだが、全部を数え直して立て直す。
			x.rescanAllLocked(st, b)
			x.markLocked(b.ID())
			return
		}
		for _, l := range st.lines[start : end+1] {
			if len(l.spans) > 0 {
				needSend = true
				st.total -= len(l.spans)
			}
		}
		added := strings.Count(ed.Text, "\n") + 1
		if added != end-start+1 && st.total > 0 {
			needSend = true
		}
		repl := make([]lineSpans, added)
		for i := range repl {
			repl[i].dirty = true
		}
		st.lines = slices.Replace(st.lines, start, end+1, repl...)
		if hi > end {
			hi += added - (end - start + 1)
		}
		lo, hi = min(lo, start), max(hi, start+added)
	}
	st.version = b.Version()
	if len(st.lines) != b.LineCount() {
		x.rescanAllLocked(st, b)
		x.markLocked(b.ID())
		return
	}
	for i := lo; i < min(hi, len(st.lines)); i++ {
		l := &st.lines[i]
		if !l.dirty {
			continue
		}
		l.dirty = false
		l.spans = x.scan.scanLine(b.Line(i))
		x.scanned.Add(1)
		if len(l.spans) > 0 {
			needSend = true
			st.total += len(l.spans)
		}
	}
	if needSend {
		x.markLocked(b.ID())
	}
}

// closeBuffer は、閉じたバッファを忘れる。本体はバッファを閉じたら色も捨てるので、空の一覧は送らない。
func (x *extension) closeBuffer(id ext.BufferID) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.bufs, id)
	delete(x.pending, id)
}

// markLocked は、バッファ id を送り直しの要るものにして、送る goroutine を起こす。x.mu を持って呼ぶ。
func (x *extension) markLocked(id ext.BufferID) {
	x.pending[id] = true
	select {
	case x.wake <- struct{}{}:
	default:
	}
}

// sendLoop は、起こされるたびに、送り直しの要るバッファの一覧を 1 つずつ送る。プロセスが終わるまで続ける。
// 送っている間に同じバッファが何度変わっても、pending の印は 1 つなので、送り終えた後に最新の状態で 1 回だけ送る。
//
// req: FR-262
func (x *extension) sendLoop() {
	for range x.wake {
		for x.sendOne() {
		}
	}
}

// sendOne は、送り直しの要るバッファを 1 つ取り出して送る。無ければ false を返す。
// 一覧は x.mu を持って組み立て、符号化と送信は x.mu を放してから行う。
func (x *extension) sendOne() bool {
	x.mu.Lock()
	var id ext.BufferID
	found := false
	for k := range x.pending {
		id, found = k, true
		break
	}
	if !found {
		x.sending = false
		x.mu.Unlock()
		return false
	}
	delete(x.pending, id)
	st := x.bufs[id]
	if st == nil || st.total == 0 && !st.sent {
		if st != nil && st.off {
			delete(x.bufs, id)
		}
		x.mu.Unlock()
		return true
	}
	x.sending = true
	p := ext.DecorationSetParams{Buffer: id, Version: st.version, Decorations: x.decorationsLocked(st)}
	x.mu.Unlock()

	tooLarge := p.JSONSize() > maxDecorationSetBytes
	if tooLarge {
		p.Decorations = nil
	}
	err := x.send(p)

	x.mu.Lock()
	defer x.mu.Unlock()
	if tooLarge && !x.warnedLarge {
		x.warnedLarge = true
		x.message(ext.MessageWarning, "colorizer: 色コードが多すぎるので、このバッファの色コードを塗らない")
	}
	if err != nil {
		x.log.Printf("decoration/set: %v", err)
		return true
	}
	if cur := x.bufs[id]; cur == st {
		st.sent = len(p.Decorations) > 0
		if st.off && !x.pending[id] {
			delete(x.bufs, id)
		}
	}
	return true
}

// decorationsLocked は、st の範囲の一覧を作る。x.mu を持って呼ぶ。
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
// 0.5 を超えれば brightFg、そうでなければ darkFg にする（nvim-colorizer.lua の background と同じ判定）。
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

// idle は、送り直しの要るバッファが無く、送っている途中でもないかを返す。テストで送り終えたことを待つのに使う。
func (x *extension) idle() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return len(x.pending) == 0 && !x.sending
}

// message は、本体へ message/show を送る。送れなければログに書く。
func (x *extension) message(level ext.MessageLevel, text string) {
	if err := x.notify(level, text); err != nil {
		x.log.Printf("message/show: %v", err)
	}
}

// versionLess は、"major.minor.patch" の版 a が b より古いかを返す。読めない版は古いものとして扱う。
func versionLess(a, b string) bool {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka {
		return true
	}
	if !okb {
		return false
	}
	return slices.Compare(pa[:], pb[:]) < 0
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
