package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、スレッドとスタックの区画（FR-290、FR-303）と、スコープと変数の区画（FR-289、FR-302、FR-305）の中身である（pane.go）。
//
// どちらの区画も、動いている間は前の中身を残し、先頭に動いていることを示す 1 行を足す（request を送らない）。
// 止まったら、同じ項目の ID で作り直すので、本体はカーソルの項目と木の開閉を保つ（PR #423 のレビューの必須 2。
// nvim-dap-view の listeners.lua は、続けたときにスコープの区画を描き直さない）。

// maxFrames は、スレッドの区画で 1 つのスレッドについて読むフレームの数の上限である（仮）。深い再帰で、区画の行が増えすぎないため。
const maxFrames = 200

// pageSize は、添字の子の多い変数の子を、1 回の variables で読む数である（VS Code の 100 ずつの範囲と同じ数。仮）。
// 残りは、debugpy の more の項目と同じく、開いたときに続きを読む項目にする（PR #423 のレビューの推奨 5）。
const pageSize = 100

// runningLabel は、動いている間に区画の先頭に足す 1 行である。runningID はその項目の ID である。
const (
	runningLabel = "動いている（止まったら読み直す）"
	runningID    = "\x1frunning"
)

// sessionSnap は、区画を作るときに debugger.mu の中で写した、1 つのセッションの状態である。
type sessionSnap struct {
	s                     *session
	halted, configured    bool
	thread, frame         int
	hasThread, hasFrame   bool
	stoppedThread         int
	hasStoppedThread      bool
	gen                   uint64
	stopping              bool
	supportsSetVariable   bool
	supportsSetExpression bool
	supportsClipboard     bool
}

// snapshot は、動いているセッションの状態を始めた順に、今のセッションと、デバッグを一度でも始めたか（出力の写しがあるか）と一緒に返す。
func (d *debugger) snapshot() (ss []sessionSnap, cur *session, started bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sessions {
		ss = append(ss, sessionSnap{
			s: s, halted: s.halted, configured: s.configured, thread: s.thread, hasThread: s.hasThread,
			frame: s.frame, hasFrame: s.hasFrame, stoppedThread: s.stoppedThread, hasStoppedThread: s.hasStoppedThread,
			gen:                   s.gen.Load(),
			stopping:              s.stopGen != 0 && s.stopGen == s.gen.Load(),
			supportsSetVariable:   s.supports(func(c *capabilities) bool { return c.SupportsSetVariable }),
			supportsSetExpression: s.supports(func(c *capabilities) bool { return c.SupportsSetExpression }),
			supportsClipboard:     s.supports(func(c *capabilities) bool { return c.SupportsClipboardContext }),
		})
	}
	return ss, d.cur, len(d.outLogs) > 0
}

// stoppingAny は、止まった位置を出し終える前のセッションがあるかを返す。その間、ペインはスレッドとスコープの区画を読まない
// （止まった位置の stackTrace より先に、ペインの request をアダプターへ送らないため。PR #423 のレビューの必須 1）。
// 出し終えたら（showStopped、stoppedWithoutSource）、作り直しの印が付く。
func (d *debugger) stoppingAny() bool {
	ss, _, _ := d.snapshot()
	return anyStopping(ss)
}

// anyStopping は、写し ss に、止まった位置を出し終える前のセッションがあるかを返す。区画を作り始めた後に止まったとき
// （refresh の stoppingAny の後、区画の写しを取る前に止まったとき）は、作るのをやめて errStale にする。止まった位置を出し終えると
// 作り直しの印が付く。スレッドの区画が、選んでいるフレームの決まる前の中身を送っていた（#424 の make check で見つけた揺れ）。
func anyStopping(ss []sessionSnap) bool {
	return slices.ContainsFunc(ss, func(x sessionSnap) bool { return x.stopping })
}

// runningShowDelay は、動き出してから、区画を「動いている」の中身に作り直すまでの間である（仮）。
// ステップ実行は、ふつうこの間に止まる（2026-11-25 の手元の測りで、止まった位置へ移るまで dlv は約 12 ms、debugpy と vscode-js-debug は
// 約 55 ms）ので、動いている間の中身を送らずに、止まった後の中身だけを送る。ステップ実行のたびに「動いている」の中身を本体へ送ると、
// 本体がその描き直しをしている間に止まった位置への移動が届き、元のペインの移動が遅れた（#424 の測り。ウォッチ式を 10 個出して約 9 ms）。
// nvim-dap-view も、続けたときにスコープとウォッチ式の区画を描き直さない（listeners.lua）。
const runningShowDelay = 150 * time.Millisecond

// settleDelay は、止まった位置を出し始めてから、区画（スレッド、スコープ、ウォッチ式）を読み始めるまでの待ちである（仮。PR #425 のレビューの
// 必須 1 へのオーケストレータの決め）。ステップ実行を続けて打つと、前の停止の区画の読み込み（vscode-js-debug でウォッチ式に 2 万の要素の列を
// 置くと約 0.5 秒）がアダプターに送ってある間に次の next が届き、その後ろに並んで遅れた。待ちの間に次のステップ実行か続行が来れば、
// その停止の区画は読まない（runningHold の間は読まないため）。0.1 秒の間でステップ実行を打つと、止まった位置を出してから次の next まで
// 約 100 ms なので、それより長くした（PR #425 の測り）。
const settleDelay = 120 * time.Millisecond

// runningHold は、区画を読み始めるのを待つ残りの時間を返す（無ければ 0）。動き出してから runningShowDelay が経っていないセッションか、
// 止まった位置を出し始めてから settleDelay が経っていない止まっているセッションがあれば、その残りである。
func (d *debugger) runningHold() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	var hold time.Duration
	for _, s := range d.sessions {
		var left time.Duration
		switch {
		case !s.halted && !s.resumedAt.IsZero():
			left = runningShowDelay - time.Since(s.resumedAt)
		case s.halted && !s.shownAt.IsZero():
			left = settleDelay - time.Since(s.shownAt)
		}
		hold = max(hold, left)
	}
	return hold
}

// sessionByID は、番号 id の動いているセッションを返す。無ければ nil。
func (d *debugger) sessionByID(id int) *session {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sessions {
		if s.id == id {
			return s
		}
	}
	return nil
}

// threadsView は、スレッドの区画の状態である。run だけが触る。
type threadsView struct {
	// expanded は、利用者が開閉した項目の ID から、開いたかへの対応である。無い項目は既定（セッションは開く。止まったスレッド、
	// 選んでいるスレッド、選んでいるフレームのスレッドは開く）。
	expanded map[string]bool
	// subtle は、presentationHint が subtle のフレームを出すかである（t）。filter と invert は、フレームの絞り込みの文字列と反転である（f と o）。
	subtle bool
	filter string
	invert bool
	// cache は、セッションごとの、最後に読んだスレッドとフレームである。動いている間は、前に読んだものを出す。
	cache map[*session]*threadCache
	// frames は、フレームの項目の ID から、そのフレームへの対応である（項目を選んだとき）。
	frames map[string]frameRef
	// last は、最後に作った木である（区画に切り替えた直後に、読み直していることを添えて出す。pane.markReloading）。
	last ext.ViewComponent
}

// threadCache は、1 つのセッションの、1 つの世代のスレッドとフレームである。
type threadCache struct {
	gen     uint64
	threads []thread
	err     error
	frames  map[int][]stackFrame
	total   map[int]int
	ferr    map[int]error
}

// frameRef は、スレッドの区画のフレームの項目が指すフレームである。
type frameRef struct {
	s     *session
	tid   int
	frame stackFrame
}

func sessionItemID(s *session) string         { return "s" + strconv.Itoa(s.id) }
func threadItemID(s *session, tid int) string { return fmt.Sprintf("t%d/%d", s.id, tid) }

// frameItemID は、フレームの項目の ID である。フレームの ID でなくスタックの中の位置で作り、止まり直しても同じ深さのフレームは
// 同じ ID にする（アダプターはフレームの ID を止まるたびに振り直すので、ID で作るとカーソルの項目を保てない）。
func frameItemID(s *session, tid, index int) string {
	return fmt.Sprintf("t%d/%d/#%d", s.id, tid, index)
}

// parseThreadItemID は、threadItemID の逆である。フレームの項目の ID なら、そのスレッドを返す。
func parseThreadItemID(id string) (sid, tid int, ok bool) {
	n, err := fmt.Sscanf(id, "t%d/%d", &sid, &tid)
	return sid, tid, err == nil && n == 2
}

// threadsOf は、x のスレッドを返す。止まっていれば、止まったスレッド、選んでいるスレッド、利用者が開いたスレッドのフレームも読む。
// 同じ世代で読んだものがあれば読み直さない。動いている間は、前に読んだものを読み直さずに返す（動いている間に request を送って、
// 次に止まったときの stackTrace を待たせないため。PR #423 のレビューの必須 1）。前に読んだものが無ければ threads だけを読む。
// 読む前と読んでいる間に世代が進んだら errStale を返す（FR-287）。
func (p *pane) threadsOf(x sessionSnap) (*threadCache, error) {
	if p.threads.cache == nil {
		p.threads.cache = map[*session]*threadCache{}
	}
	tc := p.threads.cache[x.s]
	if tc != nil && !x.halted {
		return tc, nil
	}
	if tc == nil || tc.gen != x.gen {
		if x.s.gen.Load() != x.gen {
			return nil, errStale
		}
		tc = &threadCache{gen: x.gen, frames: map[int][]stackFrame{}, total: map[int]int{}, ferr: map[int]error{}}
		c := x.s.getConn()
		if c == nil {
			tc.err = errDAPClosed
		} else {
			ctx, cancel := p.ctx()
			var res threadsResponse
			tc.err = c.call(ctx, "threads", nil, &res)
			cancel()
			tc.threads = res.Threads
		}
		if x.s.gen.Load() != x.gen {
			return nil, errStale
		}
		p.threads.cache[x.s] = tc
	}
	if !x.halted {
		return tc, nil
	}
	for _, t := range tc.threads {
		want := (x.hasStoppedThread && t.ID == x.stoppedThread) || (x.hasThread && t.ID == x.thread) ||
			p.threads.expanded[threadItemID(x.s, t.ID)]
		if _, done := tc.frames[t.ID]; !want || done || tc.ferr[t.ID] != nil {
			continue
		}
		if x.s.gen.Load() != x.gen {
			return nil, errStale
		}
		ctx, cancel := p.ctx()
		var st stackTraceResponse
		err := x.s.callGen(ctx, x.gen, "stackTrace", stackTraceArguments{ThreadID: t.ID, Levels: maxFrames}, &st)
		cancel()
		if errors.Is(err, errStale) {
			return nil, errStale
		}
		if err != nil {
			tc.ferr[t.ID] = err
			continue
		}
		tc.frames[t.ID] = st.StackFrames
		tc.total[t.ID] = st.TotalFrames
	}
	return tc, nil
}

// threadEvent は、セッション s の thread の event（スレッドが始まった、終わった）を、読んだスレッドの一覧に足し引きし、
// スレッドの区画を作り直す（PR #423 の再レビューの必須 1）。動いている間は threads を読み直さないので、event で一覧を保つ。
// 始まったスレッドの名前は event に無いので、次に止まって読み直すまで、名前を空にしておく（区画には ID を出す）。
// まだ一覧を読んでいなければ何もしない（開始の後の 1 回の threads が、そのスレッドも返す）。どこから呼んでもよい。
func (p *pane) threadEvent(s *session, e threadEvent) {
	p.post(func() {
		tc := p.threads.cache[s]
		if tc == nil {
			return
		}
		i := slices.IndexFunc(tc.threads, func(t thread) bool { return t.ID == e.ThreadID })
		switch {
		case e.Reason == "started" && i < 0:
			tc.threads = append(slices.Clone(tc.threads), thread{ID: e.ThreadID})
		case e.Reason == "exited" && i >= 0:
			tc.threads = slices.Delete(slices.Clone(tc.threads), i, i+1)
		default:
			return
		}
		p.markDirty(secThreads)
	})
}

// buildThreads は、スレッドの区画の木を作る（FR-290）。セッション（2 つ以上あるとき）、スレッド、フレームの木で、
// 止まったスレッド（stopped の threadId）に「止まっている」を、選んでいるスレッド（続行とステップ実行の送り先）が別なら「送り先」を添え、
// それらと選んでいるフレームのスレッドを開いてフレームを出し、選んでいるフレームに色を付ける。
// presentationHint が subtle のフレームは、t で出すまで隠す。絞り込み（f）があれば、フレームの表示の文字列がそれを含むもの
// （o で反転したら含まないもの）だけを出す。終わったセッションの読んだものは捨てる（PR #423 のレビューの推奨 1）。
//
// req: FR-290
func (p *pane) buildThreads() (ext.ViewComponent, error) {
	ss, cur, started := p.d.snapshot()
	if anyStopping(ss) {
		return ext.ViewComponent{}, errStale
	}
	for s := range p.threads.cache {
		if !slices.ContainsFunc(ss, func(x sessionSnap) bool { return x.s == s }) {
			delete(p.threads.cache, s)
		}
	}
	if len(ss) == 0 {
		if started {
			return leafTree("デバッグは終わった"), nil
		}
		return leafTree("デバッグしていない"), nil
	}
	p.threads.frames = map[string]frameRef{}
	var items []ext.ViewItem
	multi := len(ss) > 1
	if x, ok := current(ss, cur); ok && !multi && !x.halted && x.configured {
		items = append(items, runningItem())
	}
	if p.threads.filter != "" {
		label := "絞り込み: " + oneLine(p.threads.filter)
		if p.threads.invert {
			label = "絞り込み（含まないもの）: " + oneLine(p.threads.filter)
		}
		items = append(items, ext.ViewItem{ID: "filter", Label: label, Highlights: []ext.ViewSpan{{Start: 0, End: len(label), Style: "comment"}}})
	}
	for _, x := range ss {
		var children []ext.ViewItem
		if !x.configured {
			children = []ext.ViewItem{{ID: sessionItemID(x.s) + "/starting", Label: "始めています"}}
		} else {
			tc, err := p.threadsOf(x)
			if err != nil {
				return ext.ViewComponent{}, err
			}
			children = p.threadItems(x, cur, tc)
		}
		if !multi {
			items = append(items, children...)
			continue
		}
		state := "動いている"
		if x.halted {
			state = "止まっている"
		}
		label := oneLine(x.s.label()) + "（" + state + "）"
		var hs []ext.ViewSpan
		if x.s == cur {
			hs = []ext.ViewSpan{{Start: 0, End: len(label), Style: "keyword"}}
		}
		id := sessionItemID(x.s)
		items = append(items, ext.ViewItem{ID: id, Label: label, Highlights: hs, Children: children, Expanded: p.threads.open(id, true)})
	}
	p.threads.last = ext.ViewComponent{Kind: ext.ViewTree, Items: items}
	return p.threads.last, nil
}

// runningItem は、動いている間に区画の先頭に足す項目である。
func runningItem() ext.ViewItem {
	return ext.ViewItem{ID: runningID, Label: runningLabel, Highlights: []ext.ViewSpan{{Start: 0, End: len(runningLabel), Style: "comment"}}}
}

// open は、項目 id を開いて出すかを返す。利用者が開閉していなければ def である。
func (t *threadsView) open(id string, def bool) *bool {
	v, ok := t.expanded[id]
	if !ok {
		v = def
	}
	return &v
}

// threadItems は、セッション x のスレッドの項目を作る。
func (p *pane) threadItems(x sessionSnap, cur *session, tc *threadCache) []ext.ViewItem {
	if tc.err != nil {
		prefix := "threads に失敗した: "
		return []ext.ViewItem{{
			ID: sessionItemID(x.s) + "/err", Label: prefix + oneLine(tc.err.Error()),
			Highlights: []ext.ViewSpan{{Start: 0, End: len(prefix), Style: "diagnostic.error"}},
		}}
	}
	if len(tc.threads) == 0 {
		return []ext.ViewItem{{ID: sessionItemID(x.s) + "/none", Label: "スレッドが無い"}}
	}
	items := make([]ext.ViewItem, 0, len(tc.threads))
	for _, t := range tc.threads {
		id := threadItemID(x.s, t.ID)
		stopped := x.halted && x.hasStoppedThread && t.ID == x.stoppedThread
		target := x.halted && x.hasThread && t.ID == x.thread && !stopped
		label := oneLine(t.Name)
		if label == "" {
			label = "スレッド " + strconv.Itoa(t.ID)
		}
		var hs []ext.ViewSpan
		switch {
		case stopped:
			label += "（止まっている）"
			hs = []ext.ViewSpan{{Start: 0, End: len(label), Style: "diagnostic.warning"}}
		case target:
			label += "（送り先）"
			hs = []ext.ViewSpan{{Start: 0, End: len(label), Style: "keyword"}}
		}
		frames := tc.frames[t.ID]
		holdsFrame := x.hasFrame && slices.ContainsFunc(frames, func(f stackFrame) bool { return f.ID == x.frame })
		item := ext.ViewItem{
			ID: id, Label: label, Highlights: hs, HasChildren: x.halted || len(frames) > 0,
			Expanded: p.threads.open(id, stopped || target || holdsFrame),
		}
		if item.HasChildren && *item.Expanded {
			item.Children = p.frameItems(x, cur, t.ID, tc)
		}
		items = append(items, item)
	}
	return items
}

// frameItems は、セッション x のスレッド tid のフレームの項目を作る。項目は「関数の名前  ファイルの名前:行  ディレクトリ」で、
// 狭いペインでも行番号が見えるよう、ディレクトリを最後に置く（PR #423 のレビューの推奨 4）。
func (p *pane) frameItems(x sessionSnap, cur *session, tid int, tc *threadCache) []ext.ViewItem {
	if err := tc.ferr[tid]; err != nil {
		label := "stackTrace に失敗した: " + oneLine(err.Error())
		return []ext.ViewItem{{ID: threadItemID(x.s, tid) + "/err", Label: label}}
	}
	frames, ok := tc.frames[tid]
	if !ok {
		return []ext.ViewItem{{ID: threadItemID(x.s, tid) + "/loading", Label: "止まったら読む"}}
	}
	var items []ext.ViewItem
	for i, f := range frames {
		// subtle のフレームは t で出すまで隠す。ただし選んでいるフレーム（スコープの区画が出しているフレーム。一時停止で
		// runtime.gopark に止まったときの先頭のフレームなど）は隠さない（PR #423 の再レビューの推奨 2）。
		selected := x.hasFrame && x.frame == f.ID
		if f.PresentationHint == "subtle" && !p.threads.subtle && !selected {
			continue
		}
		label := oneLine(f.Name)
		hs := []ext.ViewSpan{{Start: 0, End: len(label), Style: "function"}}
		if f.Source != nil && f.Source.Path != "" {
			loc, dir := p.location(f.Source.Path, f.Line)
			label += "  " + loc
			if dir != "" {
				start := len(label) + 2
				label += "  " + dir
				hs = append(hs, ext.ViewSpan{Start: start, End: len(label), Style: "comment"})
			}
		}
		if p.threads.filter != "" && strings.Contains(label, p.threads.filter) == p.threads.invert {
			continue
		}
		id := frameItemID(x.s, tid, i)
		p.threads.frames[id] = frameRef{s: x.s, tid: tid, frame: f}
		if x.s == cur && selected {
			hs = []ext.ViewSpan{{Start: 0, End: len(label), Style: "search"}}
		}
		item := ext.ViewItem{ID: id, Label: label, Highlights: hs}
		if path, ok := frameFile(f); ok {
			line := fromDAPLine(f.Line, linesStartAt1)
			item.Preview = &ext.ViewPreview{Path: path, Line: &line}
		}
		items = append(items, item)
	}
	if n := tc.total[tid]; n > len(frames) && len(frames) >= maxFrames {
		items = append(items, ext.ViewItem{ID: threadItemID(x.s, tid) + "/more", Label: fmt.Sprintf("（ほかに %d のフレームは出していない）", n-len(frames))})
	}
	if len(items) == 0 {
		items = append(items, ext.ViewItem{ID: threadItemID(x.s, tid) + "/empty", Label: "出すフレームが無い"})
	}
	return items
}

// frameFile は、フレーム f のソースが、開けるファイル（絶対パスで、ある）かを返す。vscode-js-debug の <node_internals>/… のような
// 絶対パスでないパスと、無いファイルは、ソースの無いフレームとして扱う（PR #423 のレビューの推奨 3。nvim-dap-view の threads/view.lua も、
// vim.uv.fs_stat で無いファイルのフレームを出さない）。
func frameFile(f stackFrame) (string, bool) {
	if f.Source == nil || f.Source.Path == "" || f.Line <= 0 || !filepath.IsAbs(f.Source.Path) {
		return "", false
	}
	if fi, err := os.Stat(f.Source.Path); err != nil || fi.IsDir() {
		return "", false
	}
	return f.Source.Path, true
}

// location は、path の line 行（DAP の行）を「ファイルの名前:行」と、ディレクトリ（作業ディレクトリからの相対、ホームの下は ~/。
// 作業ディレクトリそのものなら空）に分ける。絶対パスでないパス（<node_internals>/…）は、全体を 1 つ目に入れる。
func (p *pane) location(path string, line int) (loc, dir string) {
	if !filepath.IsAbs(path) {
		return oneLine(path) + ":" + strconv.Itoa(line), ""
	}
	return oneLine(filepath.Base(path)) + ":" + strconv.Itoa(line), p.dirLabel(filepath.Dir(path))
}

// dirLabel は、ディレクトリ dir を、作業ディレクトリからの相対（そのものなら空）、ホームの下なら ~/ で始まるパスにする。
func (p *pane) dirLabel(dir string) string {
	if p.workDir != "" {
		if dir == p.workDir {
			return ""
		}
		if rest, ok := strings.CutPrefix(dir, p.workDir+string(os.PathSeparator)); ok {
			return oneLine(rest)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(dir, home+string(os.PathSeparator)); ok {
			return "~/" + oneLine(rest)
		}
	}
	return oneLine(dir)
}

// selectFrameItem は、スレッドの区画でフレームの項目を <CR> で選んだときに、そのフレームを選び（スコープの区画がそのフレームを出す）、
// そのソースの位置をペインを開いた元の client のタブに開く（FR-290）。選んでいるスレッド（続行とステップ実行の送り先）は替えない
// （selectFrame。PR #423 のレビューの裁定要 1）。別のセッションのフレームなら、今のセッションをそのセッションにし、そのことを知らせる。
// ソースのファイルが無いフレームは、選ぶだけにし、移れないことを知らせる。
//
// req: FR-290
func (p *pane) selectFrameItem(item string) {
	ref, ok := p.threads.frames[item]
	if !ok {
		return
	}
	prev := p.d.currentSelection().session
	if !p.d.selectFrame(ref.s, ref.frame.ID) {
		return
	}
	if prev != ref.s {
		// スコープの区画（と次の issue のウォッチ式と REPL）は今のセッションのフレームで評価するので、別のセッションのフレームを
		// 選んだら、今のセッション（続行とステップ実行の送り先）もそのセッションにする。送り先のスレッドは、そのセッションの選んでいる
		// スレッドのままにする（PR #423 の再レビューの裁定要 1 へのオーケストレータの決め。案 A）。
		p.d.showInfo("続行とステップ実行の送り先を、セッション " + ref.s.label() + " にした")
	}
	path, ok := frameFile(ref.frame)
	if !ok {
		where := "ソースの位置が無い"
		if ref.frame.Source != nil && ref.frame.Source.Path != "" {
			where = "ソースのファイルが無い（" + oneLine(ref.frame.Source.Path) + "）"
		}
		p.d.showInfo("このフレームには" + where + "ので、移らない")
		return
	}
	p.openAt(path, fromDAPLine(ref.frame.Line, linesStartAt1), ref.frame.Column)
}

// selectItem は、スレッドの区画の s で、カーソルの項目のセッションかスレッドを選ぶ（FR-303）。そのセッションを今のセッションにし、
// スレッド（フレームの項目ならそのスレッド）を続行、ステップ実行、一時停止の送り先にする。スレッドを選んだら、その先頭のフレームを
// （フレームの項目ならそのフレームを）選ぶ。開閉（<CR>）では選ばない（PR #423 のレビューの裁定要 1）。nvim-dap-view のスレッドの区画には
// 送り先を選ぶキーが無く、セッションの区画の switch_session は <CR> なので、ここでは s（select）にした。
//
// req: FR-303
func (p *pane) selectItem(item string) {
	if ref, ok := p.threads.frames[item]; ok {
		if p.d.selectThreadFrame(ref.s, ref.tid, ref.frame.ID) {
			p.d.showInfo(fmt.Sprintf("送り先を %s のスレッド %s にした", ref.s.label(), p.threadName(ref.s, ref.tid)))
		}
		return
	}
	if sid, tid, ok := parseThreadItemID(item); ok {
		s := p.d.sessionByID(sid)
		if s == nil {
			return
		}
		ctx, cancel := p.ctx()
		defer cancel()
		var st stackTraceResponse
		c := s.getConn()
		done := false
		if c != nil && c.call(ctx, "stackTrace", stackTraceArguments{ThreadID: tid, Levels: 1}, &st) == nil && len(st.StackFrames) > 0 {
			done = p.d.selectThreadFrame(s, tid, st.StackFrames[0].ID)
		} else {
			done = p.d.selectThread(s, tid)
		}
		if done {
			p.d.showInfo(fmt.Sprintf("送り先を %s のスレッド %s にした", s.label(), p.threadName(s, tid)))
		}
		return
	}
	var sid int
	if _, err := fmt.Sscanf(item, "s%d", &sid); err == nil {
		if s := p.d.sessionByID(sid); s != nil && p.d.selectSession(s) {
			p.d.showInfo("今のセッションを " + s.label() + " にした")
		}
	}
}

// threadName は、読んだスレッドの名前を返す。無ければ ID である。
func (p *pane) threadName(s *session, tid int) string {
	if tc := p.threads.cache[s]; tc != nil {
		for _, t := range tc.threads {
			if t.ID == tid {
				return oneLine(t.Name)
			}
		}
	}
	return strconv.Itoa(tid)
}

// askThreadFilter は、フレームの絞り込みの文字列を input/ask で聞く（f。今の絞り込みを初めの中身にする。FR-290）。
// 問いの答えを待つ間もペインの処理を止めないよう、別の goroutine で聞く。空の答えは絞り込みを外す。
//
// req: FR-290
func (p *pane) askThreadFilter() {
	cur := p.threads.filter
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		res, err := p.d.srv.AskInput(ctx, ext.InputAskParams{Title: "フレームの絞り込み: ", Default: cur})
		if err != nil || res.Cancelled {
			return
		}
		p.post(func() {
			p.threads.filter = res.Value
			p.markDirty(secThreads)
		})
	}()
}

// varTree は、変数の木（スコープの区画とウォッチ式の区画）の状態である。run だけが触る。
type varTree struct {
	// expanded は、利用者が開閉した項目の ID から、開いたかへの対応である。無い項目は区画ごとの既定（open の def）。
	expanded map[string]bool
	// cacheFor と cacheGen は、vars を読んだセッションと世代である。セッションか世代が替われば捨てる（変数の参照はセッションごとで、
	// 止まっている間だけ有効。FR-302、FR-305）。
	cacheFor *session
	cacheGen uint64
	vars     map[varsKey][]variable
	verr     map[varsKey]error
	// info は、最後に作った木の項目の ID から、その項目の変数への対応である（s と [[）。last は、最後に作った木である。
	info map[string]varInfo
	last ext.ViewComponent
	// lastReal と lastFor は、最後に止まっている間に作った木と、そのセッションである。動いている間は、これに印を足して出す。
	lastReal ext.ViewComponent
	lastFor  *session
	// setValues は、セッション setFor の世代 setGen の間に書き換えた変数の項目の ID から、setVariable か setExpression の応答の値への対応である
	// （setValue）。世代が替われば使わない。
	setFor    *session
	setGen    uint64
	setValues map[string]string
	// snap は、last を作ったときのセッションの写しである（s で書き換えるとき、止まった状態が変わっていないかを確かめる）。
	snap sessionSnap
}

// fresh は、読んだ変数がセッション x のその世代のものでなければ捨て、捨てたかを返す。
func (sv *varTree) fresh(x sessionSnap) bool {
	if sv.cacheFor == x.s && sv.cacheGen == x.gen && sv.vars != nil {
		return false
	}
	sv.cacheFor, sv.cacheGen = x.s, x.gen
	sv.vars, sv.verr = map[varsKey][]variable{}, map[varsKey]error{}
	return true
}

// scopesView は、スコープの区画の状態である。run だけが触る。
type scopesView struct {
	varTree
	// scopes は、読んだスコープである（varTree の読んだ変数と同じセッションと世代のもの）。
	scopes []scope
}

// varsKey は、読んだ子の変数の鍵（参照と、添字の範囲）である。
type varsKey struct {
	ref          int
	filter       string
	start, count int
}

// varInfo は、スコープの区画の 1 つの項目である。
type varInfo struct {
	parent string
	// ref は、この変数を持つ入れ物（スコープか親の変数）の参照である。scope は、スコープの項目かである。
	ref   int
	scope bool
	// watch は、ウォッチ式の項目（式そのもの）かである。v.Name と v.EvaluateName は式、v.Value は評価の結果である。
	watch bool
	// failure は、ウォッチ式の評価の誤りの全文である（誤りの項目の c で出す）。
	failure string
	v       variable
}

// varsOf は、木 sv の鍵 k の子の変数を返す（同じ世代で読んだものは読み直さない）。読む前と読んでいる間に世代が進んだら errStale を返す。
func (p *pane) varsOf(sv *varTree, x sessionSnap, k varsKey) ([]variable, error) {
	if vs, ok := sv.vars[k]; ok {
		return vs, nil
	}
	if err, ok := sv.verr[k]; ok {
		return nil, err
	}
	if x.s.gen.Load() != x.gen {
		return nil, errStale
	}
	ctx, cancel := p.ctx()
	defer cancel()
	var res variablesResponse
	err := x.s.callGen(ctx, x.gen, "variables", variablesArguments{VariablesReference: k.ref, Filter: k.filter, Start: k.start, Count: k.count}, &res)
	if errors.Is(err, errStale) {
		return nil, err
	}
	if err != nil {
		sv.verr[k] = err
		return nil, err
	}
	sv.vars[k] = res.Variables
	return res.Variables, nil
}

// current は、今のセッションの写しを返す。無ければ ok が false。
func current(ss []sessionSnap, cur *session) (sessionSnap, bool) {
	for _, x := range ss {
		if x.s == cur {
			return x, true
		}
	}
	return sessionSnap{}, false
}

// buildScopes は、スコープの区画の木を作る（FR-289）。今のセッションの選んでいるフレームのスコープ（scopes）を最上位にし、
// expensive でないスコープを開いて変数（variables）を出す。子を持つ変数は、開いたとき（view/expand）に読む。
// 添字の子が pageSize より多い変数は、pageSize ずつ読み、残りを開いたときに読む more の項目にする。
// 変数の参照と読んだ変数は、セッションと世代ごとに持ち、替わったら捨てる。読む前と読んでいる間に止まった状態が変わったら（続けた、
// 選び直した）、応答を使わずに errStale を返す（FR-302、FR-305）。動いている間は、変数を読まず、最後に止まっている間に作った木に
// 動いていることを示す 1 行を足して出す（カーソルの項目と開閉を保つため）。
//
// req: FR-289, FR-302, FR-305
func (p *pane) buildScopes() (ext.ViewComponent, error) {
	ss, cur, started := p.d.snapshot()
	if anyStopping(ss) {
		return ext.ViewComponent{}, errStale
	}
	x, ok := current(ss, cur)
	sv := &p.scopes
	sv.snap = x
	var c ext.ViewComponent
	switch {
	case !ok && started:
		c = leafTree("デバッグは終わった")
	case !ok:
		c = leafTree("デバッグしていない")
	case !x.halted && sv.lastFor == x.s:
		c = sv.lastReal
		c.Items = append([]ext.ViewItem{runningItem()}, c.Items...)
	case !x.halted:
		c = leafTree("実行中")
	case !x.hasFrame:
		c = leafTree("フレームを選んでいない")
	}
	if !ok {
		sv.cacheFor, sv.vars, sv.lastFor, sv.setFor = nil, nil, nil, nil
	}
	if c.Kind != "" {
		sv.last = c
		return c, nil
	}
	sv.info = map[string]varInfo{}
	if sv.fresh(x) {
		sv.scopes = nil
		if x.s.gen.Load() != x.gen {
			sv.vars = nil
			return ext.ViewComponent{}, errStale
		}
		ctx, cancel := p.ctx()
		var res scopesResponse
		err := x.s.callGen(ctx, x.gen, "scopes", scopesArguments{FrameID: x.frame}, &res)
		cancel()
		if errors.Is(err, errStale) {
			sv.vars = nil
			return ext.ViewComponent{}, err
		}
		if err != nil {
			sv.vars = nil
			c = leafTree("scopes に失敗した: " + oneLine(err.Error()))
			sv.last = c
			return c, nil
		}
		sv.scopes = res.Scopes
	}
	if len(sv.scopes) == 0 {
		c = leafTree("スコープが無い")
		sv.last = c
		return c, nil
	}
	var items []ext.ViewItem
	seen := map[string]int{}
	for _, sc := range sv.scopes {
		id := uniqueID(seen, oneLine(sc.Name))
		open := sv.open(id, !sc.Expensive)
		sv.info[id] = varInfo{scope: true, ref: sc.VariablesReference, v: variable{Name: sc.Name, VariablesReference: sc.VariablesReference}}
		item := ext.ViewItem{
			ID: id, Label: oneLine(sc.Name), HasChildren: sc.VariablesReference > 0, Expanded: &open,
			Highlights: []ext.ViewSpan{{Start: 0, End: len(oneLine(sc.Name)), Style: "keyword"}},
		}
		if open && sc.VariablesReference > 0 {
			children, err := p.childItems(&sv.varTree, x, id, variable{VariablesReference: sc.VariablesReference})
			if err != nil {
				sv.vars = nil
				return ext.ViewComponent{}, err
			}
			item.Children = children
		}
		items = append(items, item)
	}
	c = ext.ViewComponent{Kind: ext.ViewTree, Items: items}
	sv.last, sv.lastReal, sv.lastFor = c, c, x.s
	return c, nil
}

// open は、項目 id を開いて出すかを返す。利用者が開閉していなければ def である。
func (sv *varTree) open(id string, def bool) bool {
	if v, ok := sv.expanded[id]; ok {
		return v
	}
	return def
}

// uniqueID は、同じ親の中で同じ名前が続いたとき（Go の内側のブロックの同じ名前の変数など）に、2 つ目から #2 の形の印を付けた ID を返す。
func uniqueID(seen map[string]int, id string) string {
	seen[id]++
	if n := seen[id]; n > 1 {
		return id + "#" + strconv.Itoa(n)
	}
	return id
}

// childItems は、変数 v（スコープなら参照だけを入れたもの）の子の項目を作る。添字の子が pageSize より多ければ、名前の子と、
// 最初の pageSize の添字の子と、残りを読む more の項目にする（indexedPage）。
func (p *pane) childItems(sv *varTree, x sessionSnap, parent string, v variable) ([]ext.ViewItem, error) {
	seen := map[string]int{}
	if v.IndexedVariables <= pageSize {
		return p.variableItems(sv, x, parent, v.VariablesReference, varsKey{ref: v.VariablesReference}, seen)
	}
	var items []ext.ViewItem
	if v.NamedVariables > 0 {
		named, err := p.variableItems(sv, x, parent, v.VariablesReference, varsKey{ref: v.VariablesReference, filter: "named"}, seen)
		if err != nil {
			return nil, err
		}
		items = append(items, named...)
	}
	page, err := p.indexedPage(sv, x, parent, v.VariablesReference, 0, v.IndexedVariables, seen)
	if err != nil {
		return nil, err
	}
	return append(items, page...), nil
}

// indexedPage は、参照 ref の添字の子のうち start から pageSize 個の項目と、残りがあれば、開いたときに続きを読む more の項目を作る
// （debugpy が 100 ずつ返して more の項目を付けるのと同じ見た目。PR #423 のレビューの推奨 5）。
func (p *pane) indexedPage(sv *varTree, x sessionSnap, parent string, ref, start, total int, seen map[string]int) ([]ext.ViewItem, error) {
	n := min(pageSize, total-start)
	items, err := p.variableItems(sv, x, parent, ref, varsKey{ref: ref, filter: "indexed", start: start, count: n}, seen)
	if err != nil {
		return nil, err
	}
	if start+n >= total {
		return items, nil
	}
	id := parent + "\x1f!more" + strconv.Itoa(start+n)
	if sv.open(id, false) {
		// 開いた more の項目は、続きの pageSize 個（と次の more）に置き換え、同じ深さに並べる（開き続けても字下げが深くならない。
		// PR #423 の再レビューの推奨 5）。続きの項目の ID は、同じ変数の子として作る。
		rest, err := p.indexedPage(sv, x, parent, ref, start+n, total, seen)
		if err != nil {
			return nil, err
		}
		return append(items, rest...), nil
	}
	label := fmt.Sprintf("more: 残り %d 個", total-start-n)
	f := false
	more := ext.ViewItem{ID: id, Label: label, HasChildren: true, Expanded: &f, Highlights: []ext.ViewSpan{{Start: 0, End: len(label), Style: "comment"}}}
	return append(items, more), nil
}

// variableItems は、鍵 k の子の変数の項目を作る。開いた子を持つ変数は、その子も読む。
// 項目の ID は、親の ID に区切りの文字（U+001F）と変数の名前を足したもので、止まり直しても同じ変数は同じ ID になる（開閉とカーソルを保つ）。
func (p *pane) variableItems(sv *varTree, x sessionSnap, parent string, ref int, k varsKey, seen map[string]int) ([]ext.ViewItem, error) {
	vs, err := p.varsOf(sv, x, k)
	if errors.Is(err, errStale) {
		return nil, err
	}
	if err != nil {
		label := "variables に失敗した: " + oneLine(err.Error())
		return []ext.ViewItem{{ID: parent + "\x1f!err" + k.filter + strconv.Itoa(k.start), Label: label, Highlights: []ext.ViewSpan{{Start: 0, End: len(label), Style: "diagnostic.error"}}}}, nil
	}
	if len(vs) == 0 && k.filter == "" {
		return []ext.ViewItem{{ID: parent + "\x1f!none", Label: "（変数が無い）"}}, nil
	}
	items := make([]ext.ViewItem, 0, len(vs))
	for _, v := range vs {
		id := uniqueID(seen, parent+"\x1f"+oneLine(v.Name))
		if val, ok := sv.setValues[id]; ok && sv.setFor == x.s && sv.setGen == x.gen {
			v.Value = val
		}
		sv.info[id] = varInfo{parent: parent, ref: ref, v: v}
		label, hs := variableLabel(v)
		item := ext.ViewItem{ID: id, Label: label, Highlights: hs, HasChildren: v.VariablesReference > 0}
		if v.VariablesReference > 0 {
			open := sv.open(id, false)
			item.Expanded = &open
			if open {
				children, err := p.childItems(sv, x, id, v)
				if err != nil {
					return nil, err
				}
				item.Children = children
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// variableLabel は、変数の項目の表示の文字列（名前: 型 = 値。型が無ければ 名前 = 値）と色を作る（基本設計 6.4 の区画の表）。
func variableLabel(v variable) (string, []ext.ViewSpan) {
	name := oneLine(v.Name)
	label := name
	hs := []ext.ViewSpan{{Start: 0, End: len(name), Style: "variable"}}
	if v.Type != "" {
		start := len(label) + len(": ")
		label += ": " + oneLine(v.Type)
		hs = append(hs, ext.ViewSpan{Start: start, End: len(label), Style: "type"})
	}
	if v.Value != "" {
		label += " = " + oneLine(v.Value)
	}
	return label, hs
}

// jumpParent は、変数の木 sv の区画 child（スコープかウォッチ式）のカーソルを、項目 item の親の項目へ移す（[[。nvim-dap-view の jump_to_parent）。
// 本体の木の部品にこの操作が無いので、最後に作った木を cursor 付きの view/update で送り直す（基本設計 6.4）。
// more の項目の下の項目は、more の項目の親（その変数）へ移す。
func (p *pane) jumpParent(sv *varTree, child, item string) {
	info, ok := sv.info[item]
	view := ext.ViewID(p.view.Load())
	if !ok || info.parent == "" || view == 0 {
		return
	}
	parent := info.parent
	for {
		i := strings.LastIndex(parent, "\x1f!more")
		if i < 0 || strings.Contains(parent[i+1:], "\x1f") {
			break
		}
		parent = parent[:i]
	}
	c := sv.last
	c.Cursor = parent
	if err := p.d.srv.UpdateView(ext.ViewUpdateParams{View: view, Child: child, Component: c}); err != nil {
		p.d.logf("view/update %s: %v", child, err)
	}
}

// flatten は、値の改行を空白にする（input/ask の初めの中身は改行を含めない）。oneLine と違い、長くても切らない（PR #423 のレビューの推奨 6）。
func flatten(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// scopesSetValue は、スコープの区画のカーソルの変数の値を書き換える（s。FR-289）。値は input/ask で、今の値を初めの中身にして聞く。
// アダプターが setExpression に対応し、変数に evaluateName があれば setExpression を、なければ setVariable を使う
// （DAP の仕様の SetExpression Request の「a client uses setExpression if the variable has an evaluateName property」）。
// どちらにも対応していなければ、request を送らずに対応していないことを知らせる（FR-304）。
// 動いている間（前の木を出している間）は書き換えない。問いの答えを待つ間に止まった状態が変わったら（続けた、選び直した）、書き換えない。
//
// req: FR-289, FR-304
func (p *pane) scopesSetValue(item string) {
	p.askSetValue(&p.scopes.varTree, item)
}

// askSetValue は、変数の木 sv のカーソルの項目 item の値を書き換える（スコープとウォッチ式の区画の s）。
// ウォッチ式の項目（式そのもの）は、evaluate の結果に evaluateName が無いので、式を setExpression で書き換える
// （nvim-dap-view の watches/actions.lua の set_expr と同じ）。setExpression に対応しないアダプターでは、request を送らずに知らせる（FR-309）。
// 子の変数は、スコープの区画と同じく選ぶ。
//
// req: FR-292, FR-309
func (p *pane) askSetValue(sv *varTree, item string) {
	info, ok := sv.info[item]
	if !ok || info.scope {
		return
	}
	x := sv.snap
	if x.s == nil {
		p.d.showError("デバッグしていないので、値を書き換えられない")
		return
	}
	if !x.halted {
		p.d.showError("動いている間は、変数の値を書き換えられない")
		return
	}
	useExpr := x.supportsSetExpression && info.v.EvaluateName != ""
	if info.watch && !x.supportsSetExpression {
		p.d.showError(fmt.Sprintf("アダプター %s は式の値の書き換え（setExpression）に対応していない", x.s.spec.adapter.name))
		return
	}
	if !useExpr && !x.supportsSetVariable {
		p.d.showError(fmt.Sprintf("アダプター %s は変数の値の書き換えに対応していない", x.s.spec.adapter.name))
		return
	}
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		res, err := p.d.srv.AskInput(ctx, ext.InputAskParams{Title: oneLine(info.v.Name) + " = ", Default: flatten(info.v.Value)})
		if err != nil || res.Cancelled {
			return
		}
		p.post(func() { p.setValue(sv, x, item, info, useExpr, res.Value) })
	}()
}

// setValue は、木 sv の変数 info の値を value に書き換え、スコープとウォッチ式の区画を読み直す。
func (p *pane) setValue(sv *varTree, x sessionSnap, id string, info varInfo, useExpr bool, value string) {
	if x.s.gen.Load() != x.gen {
		p.d.showError("止まった状態が変わったので、値を書き換えなかった")
		return
	}
	ctx, cancel := p.ctx()
	defer cancel()
	var err error
	var res struct {
		Value string `json:"value"`
	}
	if useExpr {
		err = x.s.callGen(ctx, x.gen, "setExpression", setExpressionArguments{Expression: info.v.EvaluateName, Value: value, FrameID: x.frame}, &res)
	} else {
		err = x.s.callGen(ctx, x.gen, "setVariable", setVariableArguments{VariablesReference: info.ref, Name: info.v.Name, Value: value}, &res)
	}
	switch {
	case errors.Is(err, errStale):
		p.d.showError("止まった状態が変わったので、書き換えた値を出さない")
	case err != nil:
		p.d.showError(fmt.Sprintf("値を書き換えられない: %v", err))
	default:
		// vscode-js-debug 1.117.0 は、setExpression の直後の variables で書き換える前の値を返す（#421 で実物で確かめた。
		// nvim-dap-view も scopes/view.lua で、vscode-js-debug の issue 2320 への回り道として応答の値を出す）。
		// 同じ止まった状態の間は、応答の値をその変数の値として出す。
		if sv.setFor != x.s || sv.setGen != x.gen {
			sv.setFor, sv.setGen, sv.setValues = x.s, x.gen, map[string]string{}
		}
		if res.Value != "" {
			sv.setValues[id] = res.Value
		}
	}
	// 書き換えると、ほかの変数の値と参照も変わりうる（DAP の仕様の SetVariableResponse）ので、どちらの区画も読んだ変数と
	// 評価の結果を捨てて読み直す（nvim-dap-view の listeners.lua も、setExpression と setVariable の後にウォッチ式を評価し直す）。
	p.scopes.vars = nil
	p.watches.vars = nil
	p.markDirty(secScopes, secWatches)
}
