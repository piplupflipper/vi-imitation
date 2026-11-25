package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、ウォッチ式の区画（FR-292、FR-309）の中身である（pane.go）。
//
// 区画は、利用者が足した式ごとに「式 = 値」の項目を木で出す。値は、今のセッションの選んでいるフレームで evaluate（context は watch）した
// 結果で、止まるたびと、フレームを選び直したときに評価し直す（区画の作り直しの印は、スコープの区画と同じ変わり目で付く）。
// 結果が構造を持てば（variablesReference が 0 でない）、子をスコープの区画と同じく読む（varTree。100 個ずつの読み込みを含む）。
// 動いている間は評価せず、前の木を残して先頭に動いていることを示す 1 行を足す（スコープの区画と同じ。PR #423 の取り決め）。
// 足した式は、拡張機能のプロセスが動いている間（本体を終えるまで）残し、セッションをまたいで使う（基本設計 6.4 の「拡張機能に 1 つ」）。
// ファイルには書かない。

// watchExpr は、足した 1 つの式である。id は、足した順に振る、変わらない番号で、項目の ID（watchItemID）に使う。
type watchExpr struct {
	id   int
	expr string
}

// evalResult は、1 つの式の評価の結果である。failure は、評価の誤りの文（アダプターの失敗の応答の文）で、成り立てば空である。
type evalResult struct {
	res     evaluateResponse
	failure string
}

// watchesView は、ウォッチ式の区画の状態である。run だけが触る。
type watchesView struct {
	varTree
	exprs  []watchExpr
	nextID int
	// results は、式の番号から、varTree が読んだ変数と同じセッションと世代での評価の結果への対応である。
	// 同じ止まった状態の間は評価し直さない（足した式だけを評価する）。
	results map[int]evalResult
	// cursor は、次に作る木で、カーソルを置く項目の ID である（足した式、消した式の次の式）。空なら置かない。
	cursor string
}

// watchItemID は、式の番号 id の項目の ID である。子の変数の項目の ID は、これに区切りの文字と変数の名前を足したものである。
func watchItemID(id int) string { return "w" + strconv.Itoa(id) }

// rootIndex は、項目 item（式か、その子の変数）の式の、exprs の中の添字を返す。無ければ -1。
func (w *watchesView) rootIndex(item string) int {
	head, _, _ := strings.Cut(item, "\x1f")
	return slices.IndexFunc(w.exprs, func(e watchExpr) bool { return watchItemID(e.id) == head })
}

// forget は、式の番号 id の評価の結果と、開閉と書き換えた値の覚えを捨てる（書き換えたか消した式）。
func (w *watchesView) forget(id int) {
	delete(w.results, id)
	root := watchItemID(id)
	for k := range w.expanded {
		if k == root || strings.HasPrefix(k, root+"\x1f") {
			delete(w.expanded, k)
		}
	}
	for k := range w.setValues {
		if k == root || strings.HasPrefix(k, root+"\x1f") {
			delete(w.setValues, k)
		}
	}
}

// buildWatches は、ウォッチ式の区画の木を作る（FR-292）。止まっていれば、今のセッションの選んでいるフレームで式を評価し、
// 結果が構造を持てば開いて（nvim-dap-view の add_watch_expr と同じく既定で開く）子を出す。評価の誤りは、その式の行に誤りの色で出す。
// デバッグしていないとき、一度も止まっていないときは、式だけを出す。動いている間は、最後に止まっている間に作った木に、
// 動いていることを示す 1 行を足して出す（評価しない）。読む前と読んでいる間に止まった状態が変わったら、errStale を返す（FR-305）。
//
// req: FR-292, FR-305
func (p *pane) buildWatches() (ext.ViewComponent, error) {
	ss, cur, started := p.d.snapshot()
	if anyStopping(ss) {
		return ext.ViewComponent{}, errStale
	}
	x, ok := current(ss, cur)
	w := &p.watches
	w.snap = x
	if !ok {
		w.cacheFor, w.vars, w.lastFor, w.setFor, w.results = nil, nil, nil, nil, nil
	}
	var c ext.ViewComponent
	switch {
	case len(w.exprs) == 0:
		c = leafTree("式が無い（a で足す）")
	case !ok && started:
		c = w.plain("（デバッグは終わった）")
	case !ok:
		c = w.plain("（デバッグしていない）")
	case !x.halted:
		c = w.running()
	}
	if c.Kind != "" {
		w.last = c
		c.Cursor, w.cursor = w.cursor, ""
		return c, nil
	}
	w.info = map[string]varInfo{}
	if w.fresh(x) {
		w.results = map[int]evalResult{}
	}
	if err := p.prefetchWatches(x); err != nil {
		w.vars = nil
		return ext.ViewComponent{}, err
	}
	items := make([]ext.ViewItem, 0, len(w.exprs))
	for _, e := range w.exprs {
		item, err := p.watchItem(x, e)
		if err != nil {
			w.vars = nil
			return ext.ViewComponent{}, err
		}
		items = append(items, item)
	}
	c = ext.ViewComponent{Kind: ext.ViewTree, Items: items}
	w.last, w.lastReal, w.lastFor = c, c, x.s
	c.Cursor, w.cursor = w.cursor, ""
	return c, nil
}

// prefetchWatches は、まだ評価していない式の evaluate と、開いて出す結果の最初の子の variables を、それぞれ maxInFlight 個ずつ並行に送る
// （inFlight）。1 つずつ送ると、アダプターの応答の間が足し合わされ、評価し終えるまでに打ったステップ実行が遅れた（maxInFlight の doc）。
// 読む前と読んでいる間に止まった状態が変わったら、errStale を返す。
func (p *pane) prefetchWatches(x sessionSnap) error {
	w := &p.watches
	var jobs []watchExpr
	for _, e := range w.exprs {
		if _, ok := w.results[e.id]; !ok {
			jobs = append(jobs, e)
		}
	}
	if len(jobs) > 0 {
		if x.s.gen.Load() != x.gen {
			return errStale
		}
		res := make([]evalResult, len(jobs))
		errs := make([]error, len(jobs))
		p.inFlight(x, len(jobs), func(i int) error {
			args := evaluateArguments{Expression: jobs[i].expr, Context: "watch"}
			if x.hasFrame {
				f := x.frame
				args.FrameID = &f
			}
			ctx, cancel := p.ctx()
			defer cancel()
			errs[i] = x.s.callGen(ctx, x.gen, "evaluate", args, &res[i].res)
			return errs[i]
		})
		if x.s.gen.Load() != x.gen {
			return errStale // 呼ばなかった式がある
		}
		for i, e := range jobs {
			if errors.Is(errs[i], errStale) {
				return errStale
			}
			if errs[i] != nil {
				res[i].failure = adapterMessage(errs[i])
			}
			w.results[e.id] = res[i]
		}
	}
	var keys []varsKey
	for _, e := range w.exprs {
		r := w.results[e.id]
		if r.failure == "" && r.res.VariablesReference > 0 && w.open(watchItemID(e.id), true) {
			keys = append(keys, firstKeys(r.res.VariablesReference, r.res.IndexedVariables, r.res.NamedVariables)...)
		}
	}
	return p.prefetchVars(&w.varTree, x, keys)
}

// maxInFlight は、ウォッチ式の区画が一度にアダプターへ送っておく request の数の上限である（仮）。
// 2026-11-25 に、10 個の式（構造を持つ結果を含む）で、止まってから評価し終えるまでを測った。1 つずつ送ると debugpy 1.8.22 で約 0.65 秒、
// vscode-js-debug で約 0.7 秒かかり、その間（0.4 秒の間を置いて打ったとき）のステップ実行が、送ってある request の後ろに並んで
// 約 40 ms 遅れた。上限を置かずに並行に送ると、vscode-js-debug は約 0.3 秒で終えたが、debugpy は 1.2〜1.8 秒に延びた。
// 2 つずつなら debugpy は約 0.3 秒、vscode-js-debug は約 0.5 秒で終えた。3 つずつでは debugpy が約 0.46 秒に延びた（#424 の PR の測り）。
const maxInFlight = 2

// inFlight は、f(0) から f(n-1) を、一度に maxInFlight 個まで並行に呼び、すべて終わるまで待つ。
// 呼ぶ前に止まった状態が変わっていれば（世代が進んでいれば）、残りを呼ばない（ステップ実行を、読み残しの request の後ろに並べないため）。
// f が errStale を返したら、残りを呼ばない。
func (p *pane) inFlight(x sessionSnap, n int, f func(i int) error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	next, stop := 0, false
	take := func() (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		if stop || next >= n || x.s.gen.Load() != x.gen {
			stop = true
			return 0, false
		}
		next++
		return next - 1, true
	}
	for range min(maxInFlight, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i, ok := take()
				if !ok {
					return
				}
				if errors.Is(f(i), errStale) {
					mu.Lock()
					stop = true
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
}

// firstKeys は、参照 ref の変数（添字の子 indexed 個、名前の子 named 個）を開いたときに、childItems が最初に読む鍵である。
func firstKeys(ref, indexed, named int) []varsKey {
	if indexed <= pageSize {
		return []varsKey{{ref: ref}}
	}
	var keys []varsKey
	if named > 0 {
		keys = append(keys, varsKey{ref: ref, filter: "named"})
	}
	return append(keys, varsKey{ref: ref, filter: "indexed", start: 0, count: min(pageSize, indexed)})
}

// prefetchVars は、木 sv がまだ読んでいない鍵 keys の variables を、maxInFlight 個ずつ並行に送って読む（prefetchWatches）。
func (p *pane) prefetchVars(sv *varTree, x sessionSnap, keys []varsKey) error {
	var todo []varsKey
	for _, k := range keys {
		_, done := sv.vars[k]
		_, failed := sv.verr[k]
		if !done && !failed && !slices.Contains(todo, k) {
			todo = append(todo, k)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	if x.s.gen.Load() != x.gen {
		return errStale
	}
	res := make([]variablesResponse, len(todo))
	errs := make([]error, len(todo))
	p.inFlight(x, len(todo), func(i int) error {
		k := todo[i]
		ctx, cancel := p.ctx()
		defer cancel()
		errs[i] = x.s.callGen(ctx, x.gen, "variables", variablesArguments{VariablesReference: k.ref, Filter: k.filter, Start: k.start, Count: k.count}, &res[i])
		return errs[i]
	})
	if x.s.gen.Load() != x.gen {
		return errStale
	}
	for i, k := range todo {
		switch {
		case errors.Is(errs[i], errStale):
			return errStale
		case errs[i] != nil:
			sv.verr[k] = errs[i]
		default:
			sv.vars[k] = res[i].Variables
		}
	}
	return nil
}

// plain は、評価せずに式だけを並べた木を作る。note は、式の後に添える、評価していない理由である。
func (w *watchesView) plain(note string) ext.ViewComponent {
	w.info = map[string]varInfo{}
	items := make([]ext.ViewItem, 0, len(w.exprs))
	for _, e := range w.exprs {
		items = append(items, w.plainItem(e, note))
	}
	return ext.ViewComponent{Kind: ext.ViewTree, Items: items}
}

func (w *watchesView) plainItem(e watchExpr, note string) ext.ViewItem {
	id := watchItemID(e.id)
	w.info[id] = varInfo{watch: true, v: variable{Name: e.expr, EvaluateName: e.expr}}
	name := oneLine(e.expr)
	label := name + "  " + note
	return ext.ViewItem{ID: id, Label: label, Highlights: []ext.ViewSpan{
		{Start: 0, End: len(name), Style: "variable"}, {Start: len(name) + 2, End: len(label), Style: "comment"},
	}}
}

// running は、動いている間の木を作る。最後に止まっている間に作った木の式の項目（子を含む）を、今の式の並びで並べ直し、
// その後に足した式は式だけを出し、先頭に動いていることを示す 1 行を足す。同じ項目の ID なので、本体はカーソルと開閉を保つ。
func (w *watchesView) running() ext.ViewComponent {
	prev := map[string]ext.ViewItem{}
	if w.lastFor == w.snap.s {
		for _, it := range w.lastReal.Items {
			prev[it.ID] = it
		}
	}
	items := []ext.ViewItem{runningItem()}
	for _, e := range w.exprs {
		if it, ok := prev[watchItemID(e.id)]; ok {
			items = append(items, it)
			continue
		}
		items = append(items, w.plainItem(e, "（止まったら評価する）"))
	}
	return ext.ViewComponent{Kind: ext.ViewTree, Items: items}
}

// watchItem は、式 e を評価し（同じ止まった状態で評価したものがあれば使い回す）、その項目を作る。
func (p *pane) watchItem(x sessionSnap, e watchExpr) (ext.ViewItem, error) {
	w := &p.watches
	r, ok := w.results[e.id]
	if !ok {
		if x.s.gen.Load() != x.gen {
			return ext.ViewItem{}, errStale
		}
		args := evaluateArguments{Expression: e.expr, Context: "watch"}
		if x.hasFrame {
			f := x.frame
			args.FrameID = &f
		}
		ctx, cancel := p.ctx()
		err := x.s.callGen(ctx, x.gen, "evaluate", args, &r.res)
		cancel()
		if errors.Is(err, errStale) {
			return ext.ViewItem{}, err
		}
		if err != nil {
			r.failure = adapterMessage(err)
		}
		w.results[e.id] = r
	}
	id := watchItemID(e.id)
	name := oneLine(e.expr)
	if r.failure != "" {
		w.info[id] = varInfo{watch: true, failure: r.failure, v: variable{Name: e.expr, EvaluateName: e.expr}}
		label := name + " = " + oneLine(r.failure)
		return ext.ViewItem{ID: id, Label: label, Highlights: []ext.ViewSpan{
			{Start: 0, End: len(name), Style: "variable"}, {Start: len(name) + 3, End: len(label), Style: "diagnostic.error"},
		}}, nil
	}
	v := variable{
		Name: e.expr, Value: r.res.Result, Type: r.res.Type, EvaluateName: e.expr, VariablesReference: r.res.VariablesReference,
		IndexedVariables: r.res.IndexedVariables, NamedVariables: r.res.NamedVariables,
	}
	if val, ok := w.setValues[id]; ok && w.setFor == x.s && w.setGen == x.gen {
		v.Value = val
	}
	w.info[id] = varInfo{watch: true, v: v}
	label := name + " = " + oneLine(v.Value)
	item := ext.ViewItem{ID: id, Label: label, Highlights: []ext.ViewSpan{{Start: 0, End: len(name), Style: "variable"}}, HasChildren: v.VariablesReference > 0}
	if v.VariablesReference > 0 {
		open := w.open(id, true)
		item.Expanded = &open
		if open {
			children, err := p.childItems(&w.varTree, x, id, v)
			if err != nil {
				return ext.ViewItem{}, err
			}
			item.Children = children
		}
	}
	return item, nil
}

// askWatch は、式を input/ask で聞き、答えを足す（a は末尾に、i はカーソルの項目の式の前に）か、書き換える（e。今の式を初めの中身にする）。
// 問いの答えを待つ間もペインの処理を止めないよう、別の goroutine で聞く。空の答えと取り消しは何もしない（nvim-dap-view も空の式を足さない）。
//
// req: FR-292
func (p *pane) askWatch(item string, mode byte) {
	w := &p.watches
	def := ""
	target := -1
	switch mode {
	case 'i':
		if i := w.rootIndex(item); i >= 0 {
			target = w.exprs[i].id
		}
	case 'e':
		i := w.rootIndex(item)
		if i < 0 || item != watchItemID(w.exprs[i].id) {
			p.d.showInfo("カーソルの項目は式でない（式の行で e を打つ）")
			return
		}
		target, def = w.exprs[i].id, w.exprs[i].expr
	}
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		res, err := p.d.srv.AskInput(ctx, ext.InputAskParams{Title: "式: ", Default: def})
		if err != nil || res.Cancelled {
			return
		}
		expr := strings.TrimSpace(res.Value)
		if expr == "" {
			return
		}
		p.post(func() { p.putWatch(expr, mode, target) })
	}()
}

// putWatch は、式 expr を足すか書き換える（askWatch の答え）。target は、i なら前に差し込む式の番号、e なら書き換える式の番号である
// （-1 なら、i は末尾に足す）。同じ式が既にあれば足さず、カーソルをその式に置く（nvim-dap-view は式を鍵にして持つので、同じ式は 1 つ）。
func (p *pane) putWatch(expr string, mode byte, target int) {
	w := &p.watches
	if i := slices.IndexFunc(w.exprs, func(e watchExpr) bool { return e.expr == expr }); i >= 0 && w.exprs[i].id != target {
		p.d.showInfo("式 " + oneLine(expr) + " は既にある")
		w.cursor = watchItemID(w.exprs[i].id)
		p.markDirty(secWatches)
		return
	}
	at := slices.IndexFunc(w.exprs, func(e watchExpr) bool { return e.id == target })
	switch {
	case mode == 'e' && at < 0:
		return // 答えを待つ間に消した
	case mode == 'e':
		w.forget(target)
		w.exprs[at].expr = expr
		w.cursor = watchItemID(target)
	default:
		w.nextID++
		e := watchExpr{id: w.nextID, expr: expr}
		if mode == 'i' && at >= 0 {
			w.exprs = slices.Insert(w.exprs, at, e)
		} else {
			w.exprs = append(w.exprs, e)
		}
		w.cursor = watchItemID(e.id)
	}
	p.markDirty(secWatches)
}

// deleteWatch は、カーソルの項目の式を消す（d）。カーソルは次の式（無ければ前の式）に置く。式の行でなければ知らせる
// （nvim-dap-view の remove_watch_expr も、式の行でだけ消す）。
//
// req: FR-292
func (p *pane) deleteWatch(item string) {
	w := &p.watches
	i := w.rootIndex(item)
	if i < 0 || item != watchItemID(w.exprs[i].id) {
		p.d.showInfo("カーソルの項目は式でない（式の行で d を打つ）")
		return
	}
	w.forget(w.exprs[i].id)
	w.exprs = slices.Delete(w.exprs, i, i+1)
	switch {
	case i < len(w.exprs):
		w.cursor = watchItemID(w.exprs[i].id)
	case i > 0:
		w.cursor = watchItemID(w.exprs[i-1].id)
	}
	p.markDirty(secWatches)
}

// copyWatchValue は、カーソルの項目の値を、ヤンクできるよう中央のポップアップのテキストに出す（c）。
// アダプターが supportsClipboardContext を示し、項目に式（式そのものか、変数の evaluateName）があれば、evaluate の context を
// clipboard にした結果を、そうでなければ表示している値（切る前の値）を出す（基本設計 6.4 の capabilities の表）。
// 本体の拡張機能 API に、拡張機能がレジスターへ値を入れる口が無いので、利用者がポップアップの中でヤンクする
// （nvim-dap-view は + のレジスターへ直に入れる。FR-311 で、口ができたら直に入れる）。
//
// req: FR-292
func (p *pane) copyWatchValue(item string) {
	w := &p.watches
	info, ok := w.info[item]
	if !ok {
		return
	}
	x := w.snap
	switch {
	case x.s == nil:
		p.d.showError("デバッグしていないので、値を読めない")
		return
	case !x.halted:
		p.d.showError("動いている間は、値を読めない")
		return
	case info.failure != "":
		// 評価の誤りの行は、誤りの全文を出す（狭いペインでは行が切れて読めないため。PR #425 のレビューの推奨 3）。
		p.openValuePopup(oneLine(info.v.Name)+" の評価の誤り", info.failure)
		return
	}
	value := info.v.Value
	if x.supportsClipboard && info.v.EvaluateName != "" {
		args := evaluateArguments{Expression: info.v.EvaluateName, Context: "clipboard"}
		if x.hasFrame {
			f := x.frame
			args.FrameID = &f
		}
		ctx, cancel := p.ctx()
		var res evaluateResponse
		err := x.s.callGen(ctx, x.gen, "evaluate", args, &res)
		cancel()
		switch {
		case errors.Is(err, errStale):
			p.d.showError("止まった状態が変わったので、値を出さない")
			return
		case err != nil:
			p.d.logf("evaluate clipboard %q: %v", info.v.EvaluateName, err)
		default:
			value = res.Result
		}
	}
	if value == "" {
		p.d.showInfo("コピーする値が無い")
		return
	}
	p.openValuePopup(oneLine(info.v.Name)+" の値", value)
}

// openValuePopup は、text をヤンクできるよう中央のポップアップのテキストに出す。題には、ヤンクのキーを添える。
// 1 行なら、元の行の中へ文字単位で貼れる 0y$ を、2 行以上なら全体を行単位でヤンクする ggyG を案内する（PR #425 のレビューの推奨 7。
// yy は行単位なので、p で元のペインの次の行に貼る。nvim-dap-view の c は文字単位で + のレジスターへ入れる）。
func (p *pane) openValuePopup(name, text string) {
	text = strings.TrimRight(strings.ToValidUTF8(normalizeNewlines(text), "�"), "\n")
	hint := "0y$ で文字単位にヤンク、<Esc> で閉じる"
	if strings.Contains(text, "\n") {
		hint = "ggyG で全体をヤンク、<Esc> で閉じる"
	}
	title := name + "（" + hint + "）"
	go func() {
		ctx, cancel := p.ctx()
		defer cancel()
		_, err := p.d.srv.OpenPopup(ctx, ext.PopupOpenParams{
			Component: ext.ViewComponent{Kind: ext.ViewText, Title: title, Text: text},
			Placement: ext.PopupCenter,
		})
		if err != nil {
			p.d.showError("値を出せない: " + err.Error())
		}
	}()
}
