package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// この節は、デバッグの情報を出すペイン（FR-108）である（基本設計 6.4 の「区画」と「表示の更新と、表示を閉じた後の中身」）。
//
// ペインは、7 つの区画を子にした 1 つの切り替えの部品（ext.ViewSwitcher。FR-269）で、pane/open で端末多重化ソフトのペインに出す
// （外では本体が中央のポップアップに代える。FR-123）。debug.toggleUI（<leader>du）で開け閉めする（FR-277）。
// 区画は、ウォッチ式（watches。panewatches.go）、スコープと変数（scopes）、例外（exceptions。exceptions.go）、ブレークポイント（breakpoints）、
// スレッドとスタック（threads。paneviews.go）、REPL（repl。panerepl.go）、デバッグ対象の出力（console）の 7 つである。
//
// ペインの処理は、1 つの goroutine（run）が、積んだ操作を積んだ順に行う。本体の部品の知らせ（ext/server のループ）と、
// デバッグの goroutine（止まった、続けた、セッションの増減、選び直し、ブレークポイントの変化）は、操作を積むか、作り直しの要る区画の
// 印（markDirty）を付けるだけで、待たない。scopes、variables、stackTrace、threads の応答待ちはこの goroutine で行うので、
// ext/server のメッセージを処理する goroutine を止めず（dev-cycle 2.2）、ステップ実行の request の送信も待たせない。
// 印は、積んだ操作をすべて行った後にまとめて作り直すので、続けてステップ実行したときは最後の状態だけを作る。

// 区画の ID（FR-278。nvim-dap-view の区画の名前と同じ）。
const (
	secWatches     = "watches"
	secScopes      = "scopes"
	secExceptions  = "exceptions"
	secBreakpoints = "breakpoints"
	secThreads     = "threads"
	secRepl        = "repl"
	secConsole     = "console"
)

// allSections は、区画の既定の並び（nvim-dap-view の既定の winbar.sections に console を足したもの。FR-278）である。
var allSections = []string{secWatches, secScopes, secExceptions, secBreakpoints, secThreads, secRepl, secConsole}

// sectionDef は、区画の見出しの名前と、その区画へ直に切り替えるキー（切り替えの部品の子の Key）である。
// nvim-dap-view（コミット b4486a6e83c45db4712849c09a76ef95b41a2133）の lua/dap-view/config.lua の winbar.base_sections と同じにした。
var sectionDefs = map[string]struct{ title, key string }{
	secWatches:     {"Watches", "W"},
	secScopes:      {"Scopes", "S"},
	secExceptions:  {"Exceptions", "E"},
	secBreakpoints: {"Breakpoints", "B"},
	secThreads:     {"Threads", "T"},
	secRepl:        {"REPL", "R"},
	secConsole:     {"Console", "C"},
}

// 区画の中のキー（宣言ファイルの contributes.popupKeys の ID。基本設計 6.4 の区画の表）。
const (
	keyScopesParent   = "dap.scopes.parent"
	keyScopesSetValue = "dap.scopes.setValue"
	keyThreadsSubtle  = "dap.threads.toggleSubtle"
	keyThreadsFilter  = "dap.threads.filter"
	keyThreadsInvert  = "dap.threads.invertFilter"
	keyThreadsSelect  = "dap.threads.select"
	keyBreakpointsDel = "dap.breakpoints.delete"
	keyConsoleNext    = "dap.console.nextSession"
	keyConsolePrev    = "dap.console.prevSession"
	keyWatchesParent  = "dap.watches.parent"
	keyWatchesAppend  = "dap.watches.append"
	keyWatchesInsert  = "dap.watches.insert"
	keyWatchesEdit    = "dap.watches.edit"
	keyWatchesDelete  = "dap.watches.delete"
	keyWatchesSet     = "dap.watches.setValue"
	keyWatchesCopy    = "dap.watches.copyValue"
)

// uiSettings は、設定の ui（ペインと区画。FR-108、FR-277、FR-278）である。既定は nvim-dap-view の既定に合わせる（基本設計 6.4）。
type uiSettings struct {
	// autoOpen は、デバッグを始めたとき（launch か attach を送る前）にペインを開くかである。
	autoOpen bool
	// autoClose は、すべてのセッションが終わったときにペインを閉じるかである。
	autoClose bool
	// direction と size は、pane/open の向きと、元のペインに対する百分率である。
	direction ext.PaneDirection
	size      int
	// sections は、出す区画の ID とその並びである。defaultSection は、初めて開いたときに選ぶ区画である。
	sections       []string
	defaultSection string
}

func defaultUISettings() uiSettings {
	return uiSettings{direction: ext.PaneDown, size: 25, sections: slices.Clone(allSections), defaultSection: secWatches}
}

// parseUISettings は、設定の ui を読む。知らない項目、知らない区画、重なった区画、並びに無い defaultSection は誤りにする。
// defaultSection を省き、既定の watches を並びから外したときは、並びの最初の区画にする。
//
// req: FR-108, FR-277, FR-278
func parseUISettings(raw any) (uiSettings, error) {
	u := defaultUISettings()
	obj, ok := raw.(map[string]any)
	if !ok {
		return u, errors.New("ui is not an object")
	}
	explicitDefault := false
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range obj {
			if !yield(k) {
				return
			}
		}
	}) {
		v := obj[k]
		switch k {
		case "autoOpen", "autoClose":
			b, ok := v.(bool)
			if !ok {
				return u, fmt.Errorf("ui.%s is not a boolean", k)
			}
			if k == "autoOpen" {
				u.autoOpen = b
			} else {
				u.autoClose = b
			}
		case "direction":
			s, _ := v.(string)
			switch ext.PaneDirection(s) {
			case ext.PaneDown, ext.PaneRight:
				u.direction = ext.PaneDirection(s)
			default:
				return u, fmt.Errorf("ui.direction %v is not \"down\" or \"right\"", v)
			}
		case "size":
			n, ok := v.(json.Number)
			i, err := n.Int64()
			if !ok || err != nil || i < 1 || i > 99 {
				return u, fmt.Errorf("ui.size %v is not an integer from 1 to 99", v)
			}
			u.size = int(i)
		case "sections":
			arr, ok := v.([]any)
			if !ok || len(arr) == 0 {
				return u, errors.New("ui.sections is not a non-empty array")
			}
			u.sections = nil
			for _, x := range arr {
				s, _ := x.(string)
				if _, known := sectionDefs[s]; !known {
					return u, fmt.Errorf("ui.sections: unknown section %v (one of %s)", x, strings.Join(allSections, ", "))
				}
				if slices.Contains(u.sections, s) {
					return u, fmt.Errorf("ui.sections: %s appears twice", s)
				}
				u.sections = append(u.sections, s)
			}
		case "defaultSection":
			s, ok := v.(string)
			if !ok {
				return u, errors.New("ui.defaultSection is not a string")
			}
			u.defaultSection, explicitDefault = s, true
		default:
			return u, fmt.Errorf("ui.%s is not a known item", k)
		}
	}
	if !slices.Contains(u.sections, u.defaultSection) {
		if explicitDefault {
			return u, fmt.Errorf("ui.defaultSection %q is not in ui.sections", u.defaultSection)
		}
		u.defaultSection = u.sections[0]
	}
	return u, nil
}

// pane は、ペインの状態である。下の「run だけが触る」の項目は、run の goroutine だけが読み書きする。
type pane struct {
	d *debugger

	mu    sync.Mutex
	ops   []func()
	dirty map[string]bool
	wake  chan struct{}
	quit  chan struct{}
	// view は、開いているペインの ID である。閉じていれば 0。onView（ext/server のループ）が読むので atomic にする。
	view atomic.Uint64
	// opening は、pane/open の応答を待っていることを表す。その間に届いた部品の知らせは、ペインのものかもしれないので run に積む。
	opening atomic.Bool

	// 以下は run だけが触る。

	// selected は、選んでいる区画（閉じた後は、閉じたときに選んでいた区画）である。空なら設定の defaultSection。
	selected string
	// workDir は、フレームとブレークポイントのパスを相対で出す起点（拡張機能の作業ディレクトリ）である。
	workDir string
	// console は、出力の区画に結びつけている写しである。empty は、出す写しが無いときに結びつける空の写しである。
	console *server.TextLog
	empty   *server.TextLog
	// consolePick は、]s と [s で選んだ、出力の区画に出すセッションである。nil なら今のセッション。
	// lastCur は、consolePick を選んだときの今のセッションで、今のセッションが替わったら consolePick を捨てる。
	consolePick, lastCur *session
	threads              threadsView
	scopes               scopesView
	watches              watchesView
	// excAdapter は、例外の区画に最後に出したアダプターの名前である（選んだ項目のフィルターを引く）。
	excAdapter string
	// replHistory は、REPL の入力の履歴（本体と同じ規則で積んだもの。開き直したときに渡す）、replLast は最後に評価した式である。
	// replDone は、最後に始めた評価の goroutine が終わると閉じる（次の評価はそれを待つ）。
	replHistory []string
	replLast    string
	replDone    chan struct{}
	// holdTimer は、動き出したばかりの間に作り直しを待たせ、後で run を起こす予定があることを表す（holdOnce）。
	holdTimer bool

	// repl は、REPL の区画の出力の写しである。作った後は変えない。どの goroutine からも書き足せる（server.TextLog）。
	repl *server.TextLog
	// comp は、REPL の入力の行の補完の問い合わせの状態である（panecomplete.go）。どの goroutine からも触れる。
	comp replCompleter
}

func newPane(d *debugger) *pane {
	p := &pane{d: d, dirty: map[string]bool{}, wake: make(chan struct{}, 1), quit: make(chan struct{})}
	p.workDir, _ = os.Getwd()
	p.empty = server.NewTextLog(d.srv)
	p.repl = server.NewConsoleLog(d.srv, replPrompt)
	p.comp = newReplCompleter()
	p.threads.expanded = map[string]bool{}
	p.scopes.expanded = map[string]bool{}
	p.watches.expanded = map[string]bool{}
	return p
}

// post は、op を run の列に積む。待たない。どこから呼んでもよい。
func (p *pane) post(op func()) {
	p.mu.Lock()
	p.ops = append(p.ops, op)
	p.mu.Unlock()
	p.signal()
}

func (p *pane) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// markDirty は、区画 secs の中身の作り直しが要る印を付ける。待たない。debugger.mu を持ったまま呼んでよい（pane.mu だけを取る）。
// 止まった、続けた、続行とステップ実行と一時停止の送信、セッションの増減、選び直し、ブレークポイントの変化で呼ぶ
// （PR #414 の未確認事項に並べた、表示のための変わり目の知らせ）。
func (p *pane) markDirty(secs ...string) {
	p.mu.Lock()
	for _, s := range secs {
		p.dirty[s] = true
	}
	p.mu.Unlock()
	p.signal()
}

// run は、stop まで、積んだ操作を順に行い、そのたびに作り直しの印の付いた区画を作り直す。
func (p *pane) run() {
	for {
		select {
		case <-p.wake:
		case <-p.quit:
			return
		}
		for {
			p.mu.Lock()
			ops := p.ops
			p.ops = nil
			p.mu.Unlock()
			if len(ops) == 0 {
				break
			}
			for _, op := range ops {
				op()
			}
		}
		p.refresh()
	}
}

// stop は、run を終わらせる。2 回目からは何もしない。
func (p *pane) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.quit:
	default:
		close(p.quit)
	}
}

func (p *pane) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), p.d.timeouts.request)
}

// takeDirty は、区画 sec の印を外し、付いていたかを返す。
func (p *pane) takeDirty(sec string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := p.dirty[sec]
	delete(p.dirty, sec)
	return v
}

// isDirty は、区画 sec に印が付いているかを返す。
func (p *pane) isDirty(sec string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dirty[sec]
}

// ui は、設定の ui を返す。
func (p *pane) ui() uiSettings {
	p.d.mu.Lock()
	defer p.d.mu.Unlock()
	return p.d.settings.ui
}

// owns は、部品の知らせ e がペインのものかもしれないか（ペインの ID か、ペインを開いている最中か）を返す。onView から呼ぶ。
func (p *pane) owns(e *server.ViewEvent) bool {
	return p.opening.Load() || (e.View != 0 && uint64(e.View) == p.view.Load())
}

// toggle は、ペインを開け閉めする（debug.toggleUI。FR-277）。閉じてもデバッグは止めない。
func (p *pane) toggle() {
	p.post(func() {
		if p.view.Load() != 0 {
			p.closePane()
			return
		}
		p.openPane()
	})
}

// requestOpen は、開いていなければペインを開く（ui.autoOpen）。
func (p *pane) requestOpen() {
	p.post(func() {
		if p.view.Load() == 0 {
			p.openPane()
		}
	})
}

// requestClose は、開いていればペインを閉じる（ui.autoClose）。
func (p *pane) requestClose() {
	p.post(func() {
		if p.view.Load() != 0 {
			p.closePane()
		}
	})
}

// closePane は、ペインを閉じる。本体は view/close で閉じたペインについて view/closed を送らないので、出力の写しの結びつきを自分で外す。
//
// req: FR-277
func (p *pane) closePane() {
	id := ext.ViewID(p.view.Swap(0))
	if p.console != nil {
		p.console.Detach()
		p.console = nil
	}
	p.repl.Detach()
	if err := p.d.srv.CloseView(id); err != nil {
		p.d.logf("view/close: %v", err)
	}
}

// openPane は、区画をすべて作って pane/open で開く（基本設計 6.4 の「開き直した」）。
// 出力の区画と REPL の区画は、それぞれの写し（server.TextLog）の Open で開き、写しの中身（閉じている間の出力を含む）を出す（FR-273）。
// 2 つの写しの Open は入れ子にし、内側の open が pane/open を送る（どちらの写しも、開いている間の書き足しを開いた後に送る）。
// REPL の入力の履歴は、拡張機能が積んだものを渡す（FR-274）。
// 選ぶ区画は、閉じたときに選んでいた区画で、初めては ui.defaultSection である。見えていない区画のうち、スレッド、スコープ、ウォッチ式の
// 区画（アダプターへ request を送って読む区画）は、作らずに印を付け、選ばれたとき（view/childSelect）に作る（#424 の裁定）。
//
// req: FR-108, FR-278, FR-291, FR-293
func (p *pane) openPane() {
	ui := p.ui()
	if !slices.Contains(ui.sections, p.selected) {
		p.selected = ui.defaultSection
	}
	children := make([]ext.ViewChild, len(ui.sections))
	logAt := map[string]int{}
	for i, sec := range ui.sections {
		def := sectionDefs[sec]
		children[i] = ext.ViewChild{ID: sec, Title: def.title, Key: def.key, Component: p.build(sec, sec == p.selected)}
		if sec == secConsole || sec == secRepl {
			logAt[sec] = i
		}
	}
	params := ext.PaneOpenParams{
		Component: ext.ViewComponent{Kind: ext.ViewSwitcher, Children: children, Selected: p.selected},
		Direction: ui.direction,
		Size:      ui.size,
	}
	var id ext.ViewID
	open := func(ext.ViewComponent) (ext.ViewID, error) {
		ctx, cancel := p.ctx()
		defer cancel()
		res, err := p.d.srv.OpenPane(ctx, params)
		id = res.View
		return res.View, err
	}
	// 写しの Open で包む。部品は、包んだ Open が渡す写しの中身にする。
	var console *server.TextLog
	if i, ok := logAt[secRepl]; ok {
		inner := open
		open = func(ext.ViewComponent) (ext.ViewID, error) {
			var vid ext.ViewID
			err := p.repl.Open(func(c ext.ViewComponent) (ext.ViewID, error) {
				c.History = p.replHistory
				// 入力の行の補完を引き受ける（FR-310。panecomplete.go）。開くときだけ読まれ、写しの view/update では消えない（FR-297）。
				c.Complete = true
				params.Component.Children[i].Component = c
				var err error
				vid, err = inner(c)
				return vid, err
			}, secRepl)
			return vid, err
		}
	}
	if i, ok := logAt[secConsole]; ok {
		inner := open
		console = p.consoleTarget()
		open = func(ext.ViewComponent) (ext.ViewID, error) {
			var vid ext.ViewID
			err := console.Open(func(c ext.ViewComponent) (ext.ViewID, error) {
				params.Component.Children[i].Component = c
				var err error
				vid, err = inner(c)
				return vid, err
			}, secConsole)
			return vid, err
		}
	}
	p.opening.Store(true)
	_, err := open(ext.ViewComponent{})
	if err == nil {
		p.view.Store(uint64(id))
		p.console = console
	}
	p.opening.Store(false)
	if err != nil {
		p.d.showError(fmt.Sprintf("DAP のペインを開けない: %v", err))
	}
}

// build は、区画 sec の部品を作る。visible が false のスコープとウォッチ式の区画は作らずに、作り直しの印を残す。
// 作っている間に止まった状態が変わった（errStale）ら、読み直していることを出し、印を残す（変わり目の知らせが印を付け直す）。
// REPL と出力の区画の中身は写しなので、openPane が写しの Open で入れる（ここでは空の部品を返す）。
func (p *pane) build(sec string, visible bool) ext.ViewComponent {
	switch sec {
	case secWatches:
		if !visible {
			p.markDirty(sec)
			return leafTree("（区画を選ぶと評価します）")
		}
		p.takeDirty(sec)
		c, err := p.buildWatches()
		if err != nil {
			p.markDirty(sec)
			return leafTree("読み直しています")
		}
		return c
	case secExceptions:
		p.takeDirty(sec)
		return p.buildExceptions()
	case secRepl:
		return ext.ViewComponent{Kind: ext.ViewConsole, Prompt: replPrompt}
	case secThreads:
		if !visible {
			p.markDirty(sec)
			return leafTree("（区画を選ぶと読みます）")
		}
		p.takeDirty(sec)
		c, err := p.buildThreads()
		if err != nil {
			p.markDirty(sec)
			return leafTree("読み直しています")
		}
		return c
	case secScopes:
		if !visible {
			p.markDirty(sec)
			return leafTree("（区画を選ぶと読みます）")
		}
		p.takeDirty(sec)
		c, err := p.buildScopes()
		if err != nil {
			p.markDirty(sec)
			return leafTree("読み直しています")
		}
		return c
	case secBreakpoints:
		p.takeDirty(sec)
		return p.buildBreakpoints()
	}
	return ext.ViewComponent{Kind: ext.ViewText}
}

// leafTree は、子の無い項目 1 つだけの木（区画の中身が無いことの知らせ）を作る。
func leafTree(label string) ext.ViewComponent {
	return ext.ViewComponent{Kind: ext.ViewTree, Items: []ext.ViewItem{{ID: "none", Label: label}}}
}

// refresh は、ペインを開いていれば、作り直しの印の付いた区画のうち、選んでいる区画だけを作り直し、出力の区画に出す写しを今のセッションに合わせる（基本設計 6.4 の「表示の更新」）。見えていないスレッド、スコープ、ウォッチ式の区画は
// 印を残し、アダプターへ request を送らない（#424 の裁定。ウォッチ式を 10 個置いても、見えていなければステップ実行を遅らせない）。
// スレッド、スコープ、ウォッチ式の区画は、止まった位置を出し終えるまで読まない（PR #423 のレビューの必須 1）。
func (p *pane) refresh() {
	view := ext.ViewID(p.view.Load())
	if view == 0 {
		return
	}
	ui := p.ui()
	if p.takeDirty(secConsole) && slices.Contains(ui.sections, secConsole) {
		p.syncConsole(view)
	}
	for _, sec := range ui.sections {
		visible := sec == p.selected
		reads := sec == secThreads || sec == secScopes || sec == secWatches
		if reads && !visible {
			// 見えていない区画は読まず、印を残して、選ばれたとき（view/childSelect）に読む（#424 の裁定）。
			if p.isDirty(sec) {
				p.forgetShown(sec)
			}
			continue
		}
		if !reads && (sec != secBreakpoints && sec != secExceptions || !visible) {
			// ブレークポイントと例外の区画も、見えているときだけ作り直して送る。止まるたびに実行位置の印でブレークポイントの区画に
			// 印が付き、見えていなくても送っていたので、本体がその描き直しをする分、止まった位置への移動が約 5 ms 遅れていた（#424 の測り）。
			continue
		}
		if !p.isDirty(sec) {
			continue
		}
		if reads && p.d.stoppingAny() {
			// 止まった位置を出し終えるまで待つ。印は残し、出し終えたときの印で作り直す。
			continue
		}
		if hold := p.d.runningHold(); reads && hold > 0 {
			// 動き出したばかりなので、「動いている」の中身を送らずに待つ。その間に止まれば、止まった後の中身だけを送る（runningShowDelay）。
			p.holdOnce(hold)
			continue
		}
		c := p.build(sec, visible)
		if p.isDirty(sec) {
			// 作っている間に止まった状態が変わった。古い中身を送らず、次の作り直しを待つ。
			continue
		}
		if err := p.d.srv.UpdateView(ext.ViewUpdateParams{View: view, Child: sec, Component: c}); err != nil {
			p.d.logf("view/update %s: %v", sec, err)
		}
	}
}

// reloadingLabel と reloadingID は、区画に切り替えた直後、読み直すまでの間に、最後に作った木の先頭に足す 1 行とその ID である。
const (
	reloadingLabel = "読み直しています（下は前に読んだ中身）"
	reloadingID    = "\x1freloading"
)

// markReloading は、作り直しの印の付いた区画 sec（スレッド、スコープ、ウォッチ式）に切り替えたとき、読み直すまでの間、
// 最後に作った木の先頭に読み直していることを示す 1 行を足して送る（PR #425 のレビューの推奨 1）。本体は切り替えた子の最後の中身を
// そのまま出すので、前の停止の値が、最新の値と見分けられずに見えていた。同じ項目の ID で送るので、本体はカーソルと開閉を保つ。
func (p *pane) markReloading(sec string) {
	view := ext.ViewID(p.view.Load())
	if view == 0 || !p.isDirty(sec) {
		return
	}
	var last ext.ViewComponent
	switch sec {
	case secThreads:
		last = p.threads.last
	case secScopes:
		last = p.scopes.last
		if p.scopes.lastFor == nil {
			// 見えていない間に止まり直した（forgetShown）か、まだ読んでいない。前の停止の値を出さない。
			p.sendReloading(view, sec, leafTree("読み直しています"))
			return
		}
	case secWatches:
		last = p.watches.last
		if p.watches.lastFor == nil && len(p.watches.exprs) > 0 {
			p.sendReloading(view, sec, p.watches.plain("（読み直しています）"))
			return
		}
	default:
		return
	}
	if last.Kind != ext.ViewTree || len(last.Items) == 0 || last.Items[0].ID == reloadingID {
		return
	}
	c := last
	items := slices.DeleteFunc(slices.Clone(c.Items), func(it ext.ViewItem) bool { return it.ID == runningID })
	c.Items = append([]ext.ViewItem{{ID: reloadingID, Label: reloadingLabel, Highlights: []ext.ViewSpan{{Start: 0, End: len(reloadingLabel), Style: "comment"}}}}, items...)
	c.Cursor = ""
	p.sendReloading(view, sec, c)
}

// sendReloading は、区画 sec の読み直すまでの中身 c を送る。
func (p *pane) sendReloading(view ext.ViewID, sec string, c ext.ViewComponent) {
	if err := p.d.srv.UpdateView(ext.ViewUpdateParams{View: view, Child: sec, Component: c}); err != nil {
		p.d.logf("view/update %s: %v", sec, err)
	}
}

// forgetShown は、見えていない区画 sec の、最後に止まっている間に作った木を、動いている間の中身に使わないようにする。
// 見えていない間に止まり直していれば、その木は最新の停止のものでないので、動いている間に区画へ切り替えたときに、
// 前の停止の値を「動いている」の印と一緒に出さず、式だけ（スコープは「実行中」）を出す。止まっていれば、選んだときに読み直す。
func (p *pane) forgetShown(sec string) {
	ss, cur, _ := p.d.snapshot()
	x, ok := current(ss, cur)
	if !ok || !x.halted {
		return
	}
	switch sec {
	case secScopes:
		p.scopes.lastFor = nil
	case secWatches:
		p.watches.lastFor = nil
	}
}

// holdOnce は、d の後に run を起こす（印の残った区画を作り直させる）。既に起こす予定があれば何もしない。run で呼ぶ。
func (p *pane) holdOnce(d time.Duration) {
	if p.holdTimer {
		return
	}
	p.holdTimer = true
	time.AfterFunc(d, func() {
		p.post(func() { p.holdTimer = false })
	})
}

// onEvent は、本体からのペインの知らせを処理する。run で呼ぶ。
func (p *pane) onEvent(e server.ViewEvent) {
	if uint64(e.View) != p.view.Load() || e.View == 0 {
		return
	}
	switch e.Kind {
	case server.ViewClosed:
		// 写しの結びつきは ext/server が外す。区画の状態（開いた変数、選んだ区画）と出力の写しは持ち続ける。
		p.view.Store(0)
		p.console = nil
	case server.ViewChildSelected:
		p.selected = e.Child
		p.markReloading(e.Child)
	case server.ViewSelected:
		switch e.Child {
		case secThreads:
			p.selectFrameItem(e.Item)
		case secBreakpoints:
			p.jumpBreakpoint(e.Item)
		case secExceptions:
			p.toggleException(e.Item)
		case secRepl:
			p.replSubmit(e.Item)
		}
	case server.ViewExpanded:
		switch e.Child {
		case secWatches:
			p.watches.expanded[e.Item] = e.Expanded
			p.markDirty(secWatches)
			p.warnRunning(e.Expanded)
		case secThreads:
			// 開閉は覗くだけで、セッションとスレッドを選ばない（選ぶのは s。PR #423 のレビューの裁定要 1）。
			p.threads.expanded[e.Item] = e.Expanded
			p.markDirty(secThreads)
		case secScopes:
			p.scopes.expanded[e.Item] = e.Expanded
			p.markDirty(secScopes)
			p.warnRunning(e.Expanded)
		}
	case server.ViewKey:
		p.onKey(e)
	case server.ViewInputChanged, server.ViewTabChanged:
	}
}

// warnRunning は、動いている間に変数を開いたら、読めないことを知らせる。動いている間は変数を読まない（前の木を出している）ので、
// 開いた項目は閉じ戻る。黙って戻さず、止まったら開くことを知らせる（PR #423 の再レビューの推奨 4）。開いた状態は覚えておき、止まったときに読む。
func (p *pane) warnRunning(expanded bool) {
	if !expanded {
		return
	}
	ss, cur, _ := p.d.snapshot()
	if x, ok := current(ss, cur); ok && !x.halted {
		p.d.showInfo("動いている間は変数を読めない。止まったら開く")
	}
}

// onKey は、区画の中のキー（contributes.popupKeys）を処理する。
func (p *pane) onKey(e server.ViewEvent) {
	switch e.Key {
	case keyScopesParent:
		p.jumpParent(&p.scopes.varTree, secScopes, e.Item)
	case keyWatchesParent:
		p.jumpParent(&p.watches.varTree, secWatches, e.Item)
	case keyWatchesAppend:
		p.askWatch(e.Item, 'a')
	case keyWatchesInsert:
		p.askWatch(e.Item, 'i')
	case keyWatchesEdit:
		p.askWatch(e.Item, 'e')
	case keyWatchesDelete:
		p.deleteWatch(e.Item)
	case keyWatchesSet:
		p.askSetValue(&p.watches.varTree, e.Item)
	case keyWatchesCopy:
		p.copyWatchValue(e.Item)
	case keyScopesSetValue:
		p.scopesSetValue(e.Item)
	case keyThreadsSubtle:
		p.threads.subtle = !p.threads.subtle
		p.markDirty(secThreads)
	case keyThreadsFilter:
		p.askThreadFilter()
	case keyThreadsInvert:
		p.threads.invert = !p.threads.invert
		p.markDirty(secThreads)
	case keyThreadsSelect:
		p.selectItem(e.Item)
	case keyBreakpointsDel:
		p.deleteBreakpoint(e.Item)
	case keyConsoleNext:
		p.cycleConsole(1)
	case keyConsolePrev:
		p.cycleConsole(-1)
	}
}

// consoleTarget は、出力の区画に出す写しを返す。]s と [s で選んだセッション、今のセッション、最後に始めたセッションの順で、
// どれも無ければ空の写しである。今のセッションが替わったら、]s と [s で選んだものを捨てる（FR-291）。
func (p *pane) consoleTarget() *server.TextLog {
	d := p.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cur != p.lastCur {
		p.consolePick, p.lastCur = nil, d.cur
	}
	switch {
	case p.consolePick != nil && slices.Contains(d.outLogs, p.consolePick):
		return p.consolePick.output
	case d.cur != nil && slices.Contains(d.outLogs, d.cur):
		return d.cur.output
	case len(d.outLogs) > 0:
		return d.outLogs[len(d.outLogs)-1].output
	}
	return p.empty
}

// syncConsole は、出力の区画に出す写しが替わっていれば、新しい写しを出力の区画に付け替える（Rebind。前の写しは外れる）。
//
// req: FR-291
func (p *pane) syncConsole(view ext.ViewID) {
	log := p.consoleTarget()
	if log == p.console {
		return
	}
	if err := log.Rebind(view, secConsole); err != nil {
		p.d.logf("rebind console: %v", err)
		return
	}
	p.console = log
}

// cycleConsole は、出力の区画に出すセッションを、始めた順の次（step が 1）か前（-1）のセッションにする（]s と [s。FR-291）。
// 終わったセッションも、次にデバッグを始めるまで選べる。
//
// req: FR-291
func (p *pane) cycleConsole(step int) {
	d := p.d
	cur := p.consoleTarget()
	d.mu.Lock()
	logs := slices.Clone(d.outLogs)
	d.mu.Unlock()
	if len(logs) < 2 {
		d.showInfo("出力の区画に出せるセッションは 1 つだけ")
		return
	}
	i := slices.IndexFunc(logs, func(s *session) bool { return s.output == cur })
	next := logs[((i+step)%len(logs)+len(logs))%len(logs)]
	p.consolePick = next
	if view := ext.ViewID(p.view.Load()); view != 0 {
		p.syncConsole(view)
	}
	d.showInfo("出力の区画: " + next.label())
}

// oneLine は、項目の表示の文字列に使えるよう、改行を空白にし、長すぎるものを切る。
func oneLine(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	if r := []rune(s); len(r) > maxLabelRunes {
		s = string(r[:maxLabelRunes]) + "…"
	}
	return s
}

// maxLabelRunes は、区画の項目の表示の文字列（変数の値など）の文字数の上限である（仮）。長い文字列の変数で、行が画面を何度も埋めないため。
const maxLabelRunes = 500

// buildBreakpoints は、ブレークポイントの区画の一覧を作る（FR-295）。拡張機能が持つすべてのブレークポイントを、パス、行の順に並べ、
// 条件と、アダプターが確かめられなかったことを添える。項目にプレビュー（FR-149）とファイルのアイコン（FR-152）を付ける。
//
// req: FR-295
func (p *pane) buildBreakpoints() ext.ViewComponent {
	d := p.d
	d.mu.Lock()
	paths := slices.Sorted(func(yield func(string) bool) {
		for path := range d.bps {
			if !yield(path) {
				return
			}
		}
	})
	type row struct {
		path string
		b    bp
	}
	var rows []row
	for _, path := range paths {
		for _, b := range d.bps[path] {
			rows = append(rows, row{path, b})
		}
	}
	d.mu.Unlock()
	if len(rows) == 0 {
		return ext.ViewComponent{Kind: ext.ViewList, Items: []ext.ViewItem{{ID: "none", Label: "ブレークポイントは無い"}}}
	}
	items := make([]ext.ViewItem, 0, len(rows))
	for _, r := range rows {
		// 狭いペインでも行番号が見えるよう、ファイルの名前と行を先に、ディレクトリを最後に置く（PR #423 のレビューの推奨 4）。
		loc, dir := p.location(r.path, r.b.line+1)
		label := loc
		var hs []ext.ViewSpan
		if r.b.cond != "" {
			label += "  if " + oneLine(r.b.cond)
		}
		if r.b.state == bpUnverified {
			start := len(label)
			label += "  （確かめられていない）"
			hs = append(hs, ext.ViewSpan{Start: start, End: len(label), Style: "diagnostic.warning"})
		}
		if dir != "" {
			start := len(label) + 2
			label += "  " + dir
			hs = append(hs, ext.ViewSpan{Start: start, End: len(label), Style: "comment"})
		}
		line := r.b.line
		items = append(items, ext.ViewItem{
			ID: bpItemID(r.path, r.b.line), Label: label, Highlights: hs,
			Preview: &ext.ViewPreview{Path: r.path, Line: &line}, File: &ext.ViewFile{Path: r.path},
		})
	}
	return ext.ViewComponent{Kind: ext.ViewList, Items: items}
}

// bpItemID は、ブレークポイントの区画の項目の ID である。
func bpItemID(path string, line int) string { return fmt.Sprintf("b%d\x1f%s", line, path) }

// parseBPItemID は、bpItemID の逆である。
func parseBPItemID(id string) (path string, line int, ok bool) {
	head, path, found := strings.Cut(id, "\x1f")
	if !found {
		return "", 0, false
	}
	if _, err := fmt.Sscanf(head, "b%d", &line); err != nil {
		return "", 0, false
	}
	return path, line, true
}

// jumpBreakpoint は、ブレークポイントの区画で選んだ項目の位置を、ペインを開いた元の client のタブに開く（FR-295）。
//
// req: FR-295
func (p *pane) jumpBreakpoint(item string) {
	path, line, ok := parseBPItemID(item)
	if !ok {
		return
	}
	p.openAt(path, line, 0)
}

// openAt は、path の line 行（DAP の列は column。0 なら行の先頭）を、ペインを開いた元の client のタブに開く（editor/open。基本設計 1.2）。
func (p *pane) openAt(path string, line, column int) {
	d := p.d
	ctx, cancel := p.ctx()
	defer cancel()
	d.mu.Lock()
	buf, opened := d.buffers[path]
	d.mu.Unlock()
	open := d.stopOpen(ctx, path, line, column, buf, opened)
	if _, err := d.srv.OpenEditor(ctx, *open); err != nil {
		d.showError(fmt.Sprintf("%s を開けない: %v", path, err))
	}
}

// deleteBreakpoint は、ブレークポイントの区画のカーソルの項目のブレークポイントを消す（d。FR-295）。
// 印を送り直し、デバッグ中なら setBreakpoints を送る。
//
// req: FR-295
func (p *pane) deleteBreakpoint(item string) {
	path, line, ok := parseBPItemID(item)
	if !ok {
		return
	}
	d := p.d
	d.mu.Lock()
	bps := slices.DeleteFunc(slices.Clone(d.bps[path]), func(x bp) bool { return x.line == line })
	if len(bps) == len(d.bps[path]) {
		d.mu.Unlock()
		return
	}
	if len(bps) == 0 {
		delete(d.bps, path)
	} else {
		d.bps[path] = bps
	}
	d.gens[path]++
	d.publishLocked(path)
	targets := d.configuredLocked()
	d.mu.Unlock()
	d.sendBreakpointsAll(targets, []string{path}, false)
}
