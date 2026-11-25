package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、スレッドとスタックの区画（FR-290、FR-303）と、スコープと変数の区画（FR-289、FR-302、FR-305）の中身である（pane.go）。

// maxFrames は、スレッドの区画で 1 つのスレッドについて読むフレームの数の上限である（仮）。深い再帰で、区画の行が増えすぎないため。
const maxFrames = 200

// sessionSnap は、区画を作るときに debugger.mu の中で写した、1 つのセッションの状態である。
type sessionSnap struct {
	s                     *session
	halted, configured    bool
	thread, frame         int
	hasThread, hasFrame   bool
	gen                   uint64
	supportsSetVariable   bool
	supportsSetExpression bool
}

// snapshot は、動いているセッションの状態を始めた順に、今のセッションと、デバッグを一度でも始めたか（出力の写しがあるか）と一緒に返す。
func (d *debugger) snapshot() (ss []sessionSnap, cur *session, started bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sessions {
		ss = append(ss, sessionSnap{
			s: s, halted: s.halted, configured: s.configured, thread: s.thread, hasThread: s.hasThread,
			frame: s.frame, hasFrame: s.hasFrame, gen: s.gen.Load(),
			supportsSetVariable:   s.supports(func(c *capabilities) bool { return c.SupportsSetVariable }),
			supportsSetExpression: s.supports(func(c *capabilities) bool { return c.SupportsSetExpression }),
		})
	}
	return ss, d.cur, len(d.outLogs) > 0
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
	// expanded は、利用者が開閉した項目の ID から、開いたかへの対応である。無い項目は既定（セッションは開く、止まったスレッドは開く）。
	expanded map[string]bool
	// subtle は、presentationHint が subtle のフレームを出すかである（t）。filter と invert は、フレームの絞り込みの文字列と反転である（f と o）。
	subtle bool
	filter string
	invert bool
	// cache は、セッションごとの、世代 gen のときに読んだスレッドとフレームである。
	cache map[*session]*threadCache
	// frames は、フレームの項目の ID から、そのフレームへの対応である（項目を選んだとき）。
	frames map[string]frameRef
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

// parseThreadItemID は、threadItemID の逆である。
func parseThreadItemID(id string) (sid, tid int, ok bool) {
	n, err := fmt.Sscanf(id, "t%d/%d", &sid, &tid)
	return sid, tid, err == nil && n == 2
}

// threadsOf は、x のスレッドを返す。止まっていれば、止まったスレッドと、利用者が開いたスレッドのフレームも読む。
// 同じ世代で読んだものがあれば読み直さない。読んでいる間に世代が進んだら errStale を返す（FR-287）。
func (p *pane) threadsOf(x sessionSnap) (*threadCache, error) {
	if p.threads.cache == nil {
		p.threads.cache = map[*session]*threadCache{}
	}
	tc := p.threads.cache[x.s]
	if tc == nil || tc.gen != x.gen {
		tc = &threadCache{gen: x.gen, frames: map[int][]stackFrame{}, total: map[int]int{}, ferr: map[int]error{}}
		p.threads.cache[x.s] = tc
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
			delete(p.threads.cache, x.s)
			return nil, errStale
		}
	}
	if !x.halted {
		return tc, nil
	}
	for _, t := range tc.threads {
		want := (x.hasThread && t.ID == x.thread) || p.threads.expanded[threadItemID(x.s, t.ID)]
		if _, done := tc.frames[t.ID]; !want || done || tc.ferr[t.ID] != nil {
			continue
		}
		ctx, cancel := p.ctx()
		var st stackTraceResponse
		err := x.s.callGen(ctx, x.gen, "stackTrace", stackTraceArguments{ThreadID: t.ID, Levels: maxFrames}, &st)
		cancel()
		if errors.Is(err, errStale) {
			delete(p.threads.cache, x.s)
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

// buildThreads は、スレッドの区画の木を作る（FR-290）。セッション（2 つ以上あるとき）、スレッド、フレームの木で、
// 止まったスレッドを開いてフレームを出し、選んでいるフレームに色を付ける。presentationHint が subtle のフレームは、t で出すまで隠す。
// 絞り込み（f）があれば、フレームの表示の文字列がそれを含むもの（o で反転したら含まないもの）だけを出す。
//
// req: FR-290
func (p *pane) buildThreads() (ext.ViewComponent, error) {
	ss, cur, started := p.d.snapshot()
	if len(ss) == 0 {
		if started {
			return leafTree("デバッグは終わった"), nil
		}
		return leafTree("デバッグしていない"), nil
	}
	p.threads.frames = map[string]frameRef{}
	var items []ext.ViewItem
	if p.threads.filter != "" {
		label := "絞り込み: " + oneLine(p.threads.filter)
		if p.threads.invert {
			label = "絞り込み（含まないもの）: " + oneLine(p.threads.filter)
		}
		items = append(items, ext.ViewItem{ID: "filter", Label: label, Highlights: []ext.ViewSpan{{Start: 0, End: len(label), Style: "comment"}}})
	}
	multi := len(ss) > 1
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
	return ext.ViewComponent{Kind: ext.ViewTree, Items: items}, nil
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
		return []ext.ViewItem{{
			ID: sessionItemID(x.s) + "/err", Label: "threads に失敗した: " + oneLine(tc.err.Error()),
			Highlights: []ext.ViewSpan{{Start: 0, End: len("threads に失敗した: "), Style: "diagnostic.error"}},
		}}
	}
	if len(tc.threads) == 0 {
		return []ext.ViewItem{{ID: sessionItemID(x.s) + "/none", Label: "スレッドが無い"}}
	}
	items := make([]ext.ViewItem, 0, len(tc.threads))
	for _, t := range tc.threads {
		id := threadItemID(x.s, t.ID)
		stopped := x.halted && x.hasThread && t.ID == x.thread
		label := oneLine(t.Name)
		var hs []ext.ViewSpan
		if stopped {
			label += "（止まっている）"
			hs = []ext.ViewSpan{{Start: 0, End: len(label), Style: "diagnostic.warning"}}
		}
		item := ext.ViewItem{ID: id, Label: label, Highlights: hs, HasChildren: x.halted, Expanded: p.threads.open(id, stopped)}
		if x.halted && *item.Expanded {
			item.Children = p.frameItems(x, cur, t.ID, tc)
		}
		items = append(items, item)
	}
	return items
}

// frameItems は、セッション x のスレッド tid のフレームの項目を作る。
func (p *pane) frameItems(x sessionSnap, cur *session, tid int, tc *threadCache) []ext.ViewItem {
	if err := tc.ferr[tid]; err != nil {
		label := "stackTrace に失敗した: " + oneLine(err.Error())
		return []ext.ViewItem{{ID: threadItemID(x.s, tid) + "/err", Label: label}}
	}
	frames, ok := tc.frames[tid]
	if !ok {
		return []ext.ViewItem{{ID: threadItemID(x.s, tid) + "/loading", Label: "読んでいます"}}
	}
	var items []ext.ViewItem
	for _, f := range frames {
		if f.PresentationHint == "subtle" && !p.threads.subtle {
			continue
		}
		label := oneLine(f.Name)
		nameEnd := len(label)
		if f.Source != nil && f.Source.Path != "" {
			label += "  " + p.relPath(f.Source.Path) + ":" + strconv.Itoa(f.Line)
		}
		if p.threads.filter != "" && strings.Contains(label, p.threads.filter) == p.threads.invert {
			continue
		}
		id := fmt.Sprintf("%s/%d", threadItemID(x.s, tid), f.ID)
		p.threads.frames[id] = frameRef{s: x.s, tid: tid, frame: f}
		hs := []ext.ViewSpan{{Start: nameEnd, End: len(label), Style: "comment"}}
		if x.s == cur && x.hasFrame && x.frame == f.ID {
			hs = []ext.ViewSpan{{Start: 0, End: len(label), Style: "search"}}
		}
		item := ext.ViewItem{ID: id, Label: label, Highlights: hs}
		if f.Source != nil && f.Source.Path != "" && f.Line > 0 {
			line := fromDAPLine(f.Line, linesStartAt1)
			item.Preview = &ext.ViewPreview{Path: f.Source.Path, Line: &line}
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

// selectFrameItem は、スレッドの区画でフレームの項目を選んだときに、そのフレームを選び（今のセッションも替える。FR-303）、
// そのソースの位置をペインを開いた元の client のタブに開く（FR-290）。スコープの区画は、選び直しの知らせで作り直す。
//
// req: FR-290, FR-303
func (p *pane) selectFrameItem(item string) {
	ref, ok := p.threads.frames[item]
	if !ok {
		return
	}
	if !p.d.selectFrame(ref.s, ref.tid, ref.frame.ID) {
		return
	}
	f := ref.frame
	if f.Source == nil || f.Source.Path == "" || f.Line <= 0 {
		p.d.showInfo("このフレームにはソースの位置が無い")
		return
	}
	p.openAt(f.Source.Path, fromDAPLine(f.Line, linesStartAt1), f.Column)
}

// expandThreadItem は、スレッドの区画のセッションかスレッドの項目を開閉した（view/expand）ことを写す。
// 開いたら、そのセッションかスレッドを選ぶ（今のセッションにし、続行、ステップ実行、一時停止の送り先にする。FR-303）。
// スレッドを開いたときは、そのスレッドの先頭のフレームを選ぶ（スコープの区画がそのフレームを出すため）。
//
// req: FR-303
func (p *pane) expandThreadItem(item string, open bool) {
	p.threads.expanded[item] = open
	defer p.markDirty(secThreads)
	if !open {
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
		if c != nil && c.call(ctx, "stackTrace", stackTraceArguments{ThreadID: tid, Levels: 1}, &st) == nil && len(st.StackFrames) > 0 {
			p.d.selectFrame(s, tid, st.StackFrames[0].ID)
			return
		}
		p.d.selectThread(s, tid)
		return
	}
	var sid int
	if _, err := fmt.Sscanf(item, "s%d", &sid); err == nil {
		if s := p.d.sessionByID(sid); s != nil {
			p.d.selectSession(s)
		}
	}
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

// scopesView は、スコープの区画の状態である。run だけが触る。
type scopesView struct {
	// expanded は、利用者が開閉した項目の ID から、開いたかへの対応である。無い項目は既定（expensive でないスコープは開き、変数は閉じる）。
	expanded map[string]bool
	// cacheFor と cacheGen は、vars を読んだセッションと世代である。セッションか世代が替われば捨てる（変数の参照はセッションごとで、
	// 止まっている間だけ有効。FR-302、FR-305）。
	cacheFor *session
	cacheGen uint64
	scopes   []scope
	vars     map[int][]variable
	verr     map[int]error
	// info は、最後に作った木の項目の ID から、その項目の変数への対応である（s と [[）。last は、最後に作った木である。
	info map[string]varInfo
	// setValues は、セッション setFor の世代 setGen の間に書き換えた変数の項目の ID から、setVariable か setExpression の応答の値への対応である
	// （setValue）。世代が替われば使わない。
	setFor    *session
	setGen    uint64
	setValues map[string]string
	last      ext.ViewComponent
	// frame は、last を作ったときのセッションとフレームと世代である（s で書き換えるとき、止まった状態が変わっていないかを確かめる）。
	snap sessionSnap
}

// varInfo は、スコープの区画の 1 つの項目である。
type varInfo struct {
	parent string
	// ref は、この変数を持つ入れ物（スコープか親の変数）の参照である。scope は、スコープの項目かである。
	ref   int
	scope bool
	v     variable
}

// varsOf は、参照 ref の子の変数を返す（同じ世代で読んだものは読み直さない）。読んでいる間に世代が進んだら errStale を返す。
func (p *pane) varsOf(x sessionSnap, ref int) ([]variable, error) {
	sv := &p.scopes
	if vs, ok := sv.vars[ref]; ok {
		return vs, nil
	}
	if err, ok := sv.verr[ref]; ok {
		return nil, err
	}
	ctx, cancel := p.ctx()
	defer cancel()
	var res variablesResponse
	err := x.s.callGen(ctx, x.gen, "variables", variablesArguments{VariablesReference: ref}, &res)
	if errors.Is(err, errStale) {
		return nil, err
	}
	if err != nil {
		sv.verr[ref] = err
		return nil, err
	}
	sv.vars[ref] = res.Variables
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
// 変数の参照と読んだ変数は、セッションと世代ごとに持ち、替わったら捨てる。読んでいる間に止まった状態が変わったら（続けた、
// 選び直した）、応答を使わずに errStale を返す（FR-302、FR-305）。続けた後は「実行中」にし、変数を読まない。
//
// req: FR-289, FR-302, FR-305
func (p *pane) buildScopes() (ext.ViewComponent, error) {
	ss, cur, started := p.d.snapshot()
	x, ok := current(ss, cur)
	sv := &p.scopes
	sv.info = map[string]varInfo{}
	sv.snap = x
	var c ext.ViewComponent
	switch {
	case !ok && started:
		c = leafTree("デバッグは終わった")
	case !ok:
		c = leafTree("デバッグしていない")
	case !x.halted:
		c = leafTree("実行中")
	case !x.hasFrame:
		c = leafTree("フレームを選んでいない")
	}
	if c.Kind != "" {
		sv.last = c
		return c, nil
	}
	if sv.cacheFor != x.s || sv.cacheGen != x.gen || sv.vars == nil {
		sv.cacheFor, sv.cacheGen, sv.scopes = x.s, x.gen, nil
		sv.vars, sv.verr = map[int][]variable{}, map[int]error{}
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
			children, err := p.variableItems(x, id, sc.VariablesReference)
			if err != nil {
				sv.vars = nil
				return ext.ViewComponent{}, err
			}
			item.Children = children
		}
		items = append(items, item)
	}
	c = ext.ViewComponent{Kind: ext.ViewTree, Items: items}
	sv.last = c
	return c, nil
}

// open は、項目 id を開いて出すかを返す。利用者が開閉していなければ def である。
func (sv *scopesView) open(id string, def bool) bool {
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

// variableItems は、参照 ref の子の変数の項目を作る。開いた子を持つ変数は、その子も読む。
// 項目の ID は、親の ID に区切りの文字（U+001F）と変数の名前を足したもので、止まり直しても同じ変数は同じ ID になる（開閉を保つ）。
func (p *pane) variableItems(x sessionSnap, parent string, ref int) ([]ext.ViewItem, error) {
	sv := &p.scopes
	vs, err := p.varsOf(x, ref)
	if errors.Is(err, errStale) {
		return nil, err
	}
	if err != nil {
		label := "variables に失敗した: " + oneLine(err.Error())
		return []ext.ViewItem{{ID: parent + "\x1f!err", Label: label, Highlights: []ext.ViewSpan{{Start: 0, End: len(label), Style: "diagnostic.error"}}}}, nil
	}
	if len(vs) == 0 {
		return []ext.ViewItem{{ID: parent + "\x1f!none", Label: "（変数が無い）"}}, nil
	}
	items := make([]ext.ViewItem, 0, len(vs))
	seen := map[string]int{}
	for _, v := range vs {
		id := uniqueID(seen, parent+"\x1f"+oneLine(v.Name))
		sv.info[id] = varInfo{parent: parent, ref: ref, v: v}
		if val, ok := sv.setValues[id]; ok && sv.setFor == x.s && sv.setGen == x.gen {
			v.Value = val
		}
		label, hs := variableLabel(v)
		item := ext.ViewItem{ID: id, Label: label, Highlights: hs, HasChildren: v.VariablesReference > 0}
		if v.VariablesReference > 0 {
			open := sv.open(id, false)
			item.Expanded = &open
			if open {
				children, err := p.variableItems(x, id, v.VariablesReference)
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

// scopesParent は、スコープの区画のカーソルを、項目 item の親の項目へ移す（[[。nvim-dap-view の jump_to_parent）。
// 本体の木の部品にこの操作が無いので、最後に作った木を cursor 付きの view/update で送り直す（基本設計 6.4）。
func (p *pane) scopesParent(item string) {
	info, ok := p.scopes.info[item]
	view := ext.ViewID(p.view.Load())
	if !ok || info.parent == "" || view == 0 {
		return
	}
	c := p.scopes.last
	c.Cursor = info.parent
	if err := p.d.srv.UpdateView(ext.ViewUpdateParams{View: view, Child: secScopes, Component: c}); err != nil {
		p.d.logf("view/update scopes: %v", err)
	}
}

// scopesSetValue は、スコープの区画のカーソルの変数の値を書き換える（s。FR-289）。値は input/ask で、今の値を初めの中身にして聞く。
// アダプターが setExpression に対応し、変数に evaluateName があれば setExpression を、なければ setVariable を使う
// （DAP の仕様の SetExpression Request の「a client uses setExpression if the variable has an evaluateName property」）。
// どちらにも対応していなければ、request を送らずに対応していないことを知らせる（FR-304）。
// 問いの答えを待つ間に止まった状態が変わったら（続けた、選び直した）、書き換えない。
//
// req: FR-289, FR-304
func (p *pane) scopesSetValue(item string) {
	info, ok := p.scopes.info[item]
	if !ok || info.scope {
		return
	}
	x := p.scopes.snap
	useExpr := x.supportsSetExpression && info.v.EvaluateName != ""
	if !useExpr && !x.supportsSetVariable {
		p.d.showError(fmt.Sprintf("アダプター %s は変数の値の書き換えに対応していない", x.s.spec.adapter.name))
		return
	}
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		res, err := p.d.srv.AskInput(ctx, ext.InputAskParams{Title: oneLine(info.v.Name) + " = ", Default: oneLine(info.v.Value)})
		if err != nil || res.Cancelled {
			return
		}
		p.post(func() { p.setValue(x, item, info, useExpr, res.Value) })
	}()
}

// setValue は、変数 info の値を value に書き換え、スコープの区画を読み直す。
func (p *pane) setValue(x sessionSnap, id string, info varInfo, useExpr bool, value string) {
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
		sv := &p.scopes
		if sv.setFor != x.s || sv.setGen != x.gen {
			sv.setFor, sv.setGen, sv.setValues = x.s, x.gen, map[string]string{}
		}
		if res.Value != "" {
			sv.setValues[id] = res.Value
		}
	}
	// 書き換えると、ほかの変数の値と参照も変わりうる（DAP の仕様の SetVariableResponse）ので、読んだ変数を捨てて読み直す。
	p.scopes.vars = nil
	p.markDirty(secScopes)
}
