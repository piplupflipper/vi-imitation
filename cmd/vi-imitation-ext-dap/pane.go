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

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// この節は、デバッグの情報を出すペイン（FR-108）である（基本設計 6.4 の「区画」と「表示の更新と、表示を閉じた後の中身」）。
//
// ペインは、7 つの区画を子にした 1 つの切り替えの部品（ext.ViewSwitcher。FR-269）で、pane/open で端末多重化ソフトのペインに出す
// （外では本体が中央のポップアップに代える。FR-123）。debug.toggleUI（<leader>du）で開け閉めする（FR-277）。
// この版で中身を作る区画は、スコープと変数（scopes）、スレッドとスタック（threads）、ブレークポイント（breakpoints）、
// デバッグ対象の出力（console）である。ウォッチ式（watches）、例外（exceptions）、REPL（repl）は、次の issue で作るまで、
// まだ無いことを書いたテキストの子にしておく。
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
	keyBreakpointsDel = "dap.breakpoints.delete"
	keyConsoleNext    = "dap.console.nextSession"
	keyConsolePrev    = "dap.console.prevSession"
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
}

func newPane(d *debugger) *pane {
	p := &pane{d: d, dirty: map[string]bool{}, wake: make(chan struct{}, 1), quit: make(chan struct{})}
	p.workDir, _ = os.Getwd()
	p.empty = server.NewTextLog(d.srv)
	p.threads.expanded = map[string]bool{}
	p.scopes.expanded = map[string]bool{}
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
	if err := p.d.srv.CloseView(id); err != nil {
		p.d.logf("view/close: %v", err)
	}
}

// openPane は、区画をすべて作って pane/open で開く（基本設計 6.4 の「開き直した」）。
// 出力の区画は、出すセッションの写し（server.TextLog）の Open で開き、写しの中身（閉じている間の出力を含む）を出す（FR-273）。
// 選ぶ区画は、閉じたときに選んでいた区画で、初めては ui.defaultSection である。見えていない区画のうち、スコープの区画は
// 作らずに印を付け、選ばれたとき（view/childSelect）に作る。スレッドの区画は止まった位置の印のため常に作る。
//
// req: FR-108, FR-278, FR-291
func (p *pane) openPane() {
	ui := p.ui()
	if !slices.Contains(ui.sections, p.selected) {
		p.selected = ui.defaultSection
	}
	children := make([]ext.ViewChild, len(ui.sections))
	consoleAt := -1
	for i, sec := range ui.sections {
		def := sectionDefs[sec]
		children[i] = ext.ViewChild{ID: sec, Title: def.title, Key: def.key, Component: p.build(sec, sec == p.selected)}
		if sec == secConsole {
			consoleAt = i
		}
	}
	params := ext.PaneOpenParams{
		Component: ext.ViewComponent{Kind: ext.ViewSwitcher, Children: children, Selected: p.selected},
		Direction: ui.direction,
		Size:      ui.size,
	}
	var id ext.ViewID
	open := func(c ext.ViewComponent) (ext.ViewID, error) {
		if consoleAt >= 0 {
			params.Component.Children[consoleAt].Component = c
		}
		ctx, cancel := p.ctx()
		defer cancel()
		res, err := p.d.srv.OpenPane(ctx, params)
		id = res.View
		return res.View, err
	}
	p.opening.Store(true)
	var err error
	var log *server.TextLog
	if consoleAt >= 0 {
		log = p.consoleTarget()
		err = log.Open(open, secConsole)
	} else {
		_, err = open(ext.ViewComponent{})
	}
	if err == nil {
		p.view.Store(uint64(id))
		p.console = log
	}
	p.opening.Store(false)
	if err != nil {
		p.d.showError(fmt.Sprintf("DAP のペインを開けない: %v", err))
	}
}

// build は、区画 sec の部品を作る。visible が false のスコープの区画は作らずに、作り直しの印を残す。
// 作っている間に止まった状態が変わった（errStale）ら、読み直していることを出し、印を残す（変わり目の知らせが印を付け直す）。
func (p *pane) build(sec string, visible bool) ext.ViewComponent {
	switch sec {
	case secThreads:
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
	case secWatches:
		return ext.ViewComponent{Kind: ext.ViewText, Text: "ウォッチ式の区画は、まだ作っていない"}
	case secExceptions:
		return ext.ViewComponent{Kind: ext.ViewText, Text: "例外の区画は、まだ作っていない"}
	case secRepl:
		return ext.ViewComponent{Kind: ext.ViewText, Text: "REPL の区画は、まだ作っていない"}
	}
	return ext.ViewComponent{Kind: ext.ViewText}
}

// leafTree は、子の無い項目 1 つだけの木（区画の中身が無いことの知らせ）を作る。
func leafTree(label string) ext.ViewComponent {
	return ext.ViewComponent{Kind: ext.ViewTree, Items: []ext.ViewItem{{ID: "none", Label: label}}}
}

// refresh は、ペインを開いていれば、作り直しの印の付いた区画のうち、選んでいる区画と、スレッドとブレークポイントの区画を作り直し、
// 出力の区画に出す写しを今のセッションに合わせる（基本設計 6.4 の「表示の更新」）。見えていないスコープの区画は印を残す。
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
		if sec == secScopes && !visible {
			continue
		}
		if sec != secThreads && sec != secBreakpoints && sec != secScopes {
			continue
		}
		if !p.isDirty(sec) {
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
	case server.ViewSelected:
		switch e.Child {
		case secThreads:
			p.selectFrameItem(e.Item)
		case secBreakpoints:
			p.jumpBreakpoint(e.Item)
		}
	case server.ViewExpanded:
		switch e.Child {
		case secThreads:
			p.expandThreadItem(e.Item, e.Expanded)
		case secScopes:
			p.scopes.expanded[e.Item] = e.Expanded
			p.markDirty(secScopes)
		}
	case server.ViewKey:
		p.onKey(e)
	case server.ViewInputChanged, server.ViewTabChanged:
	}
}

// onKey は、区画の中のキー（contributes.popupKeys）を処理する。
func (p *pane) onKey(e server.ViewEvent) {
	switch e.Key {
	case keyScopesParent:
		p.scopesParent(e.Item)
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

// relPath は、path を作業ディレクトリからの相対のパスにする。作業ディレクトリの外なら path のまま返す。
func (p *pane) relPath(path string) string {
	if p.workDir != "" {
		if rest, ok := strings.CutPrefix(path, p.workDir+string(os.PathSeparator)); ok {
			return rest
		}
	}
	return path
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
		label := fmt.Sprintf("%s:%d", p.relPath(r.path), r.b.line+1)
		pathEnd := len(label)
		if r.b.cond != "" {
			label += "  if " + oneLine(r.b.cond)
		}
		var hs []ext.ViewSpan
		if r.b.state == bpUnverified {
			start := len(label)
			label += "  （確かめられていない）"
			hs = append(hs, ext.ViewSpan{Start: start, End: len(label), Style: "diagnostic.warning"})
		}
		hs = append([]ext.ViewSpan{{Start: 0, End: pathEnd, Style: "comment"}}, hs...)
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
