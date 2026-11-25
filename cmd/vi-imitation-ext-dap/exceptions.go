package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、例外のフィルター（FR-294）と、例外で止まったときの中身（基本設計 6.4 の「#424 のウォッチ式、REPL、例外」）である。
//
// 例外のフィルターの有効と無効は、アダプターの種類（設定の adapters の名前）ごとに拡張機能が持ち（debugger.excEnabled）、
// 拡張機能のプロセスが動いている間、次のセッションの開始でも送る。覚えていないフィルターは、アダプターが default としたものを有効にする
// （nvim-dap-view（コミット b4486a6e83c45db4712849c09a76ef95b41a2133）の listeners.lua の initialize の listener と同じ）。

// noteExceptionFilters は、アダプター adapter の initialize の応答の例外のフィルターを覚え、例外の区画を作り直す。
// セッションが無いときも、最後に覚えたアダプターのフィルターを区画に出し、次の開始の前に切り替えられるようにする。
func (d *debugger) noteExceptionFilters(adapter string, filters []exceptionFilter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.excFilters == nil {
		d.excFilters = map[string][]exceptionFilter{}
	}
	d.excFilters[adapter] = slices.Clone(filters)
	d.excLast = adapter
	d.pane.markDirty(secExceptions)
}

// exceptionEnabledLocked は、アダプター adapter のフィルター f が有効かを返す。利用者が切り替えていなければ f.Default である。mu を持って呼ぶ。
func (d *debugger) exceptionEnabledLocked(adapter string, f exceptionFilter) bool {
	if v, ok := d.excEnabled[adapter][f.Filter]; ok {
		return v
	}
	return f.Default
}

// enabledExceptionFilters は、アダプター adapter のフィルター filters のうち、有効なものの ID を、filters の並びで返す（空なら空の列）。
func (d *debugger) enabledExceptionFilters(adapter string, filters []exceptionFilter) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := []string{}
	for _, f := range filters {
		if d.exceptionEnabledLocked(adapter, f) {
			ids = append(ids, f.Filter)
		}
	}
	return ids
}

// sendExceptionBreakpoints は、アダプターが例外のフィルターを示していれば、有効なフィルターを setExceptionBreakpoints で送る。
// 示していなければ送らない（DAP の仕様の SetExceptionBreakpoints Request の「Clients should only call this request if the
// corresponding capability exceptionBreakpointFilters returns one or more filters」）。
//
// req: FR-294
func (s *session) sendExceptionBreakpoints(ctx context.Context) error {
	caps := s.caps.Load()
	if caps == nil || len(caps.ExceptionBreakpointFilters) == 0 {
		return nil
	}
	c := s.getConn()
	if c == nil {
		return errDAPClosed
	}
	filters := s.d.enabledExceptionFilters(s.spec.adapter.name, caps.ExceptionBreakpointFilters)
	return c.call(ctx, "setExceptionBreakpoints", setExceptionBreakpointsArguments{Filters: filters}, nil)
}

// exceptionsAdapter は、例外の区画に出すアダプターの名前と、そのフィルターを返す。今のセッションのアダプター、無ければ最後に
// capabilities を受けたアダプターである。どれも無ければ adapter が空である。
func (d *debugger) exceptionsAdapter() (adapter string, filters []exceptionFilter, enabled []bool, live bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	adapter = d.excLast
	if d.cur != nil {
		adapter, live = d.cur.spec.adapter.name, true
	}
	filters = slices.Clone(d.excFilters[adapter])
	for _, f := range filters {
		enabled = append(enabled, d.exceptionEnabledLocked(adapter, f))
	}
	return adapter, filters, enabled, live
}

// exceptionItemID は、例外の区画の項目の ID である。
func exceptionItemID(filter string) string { return "x\x1f" + filter }

// buildExceptions は、例外の区画の一覧を作る（FR-294）。アダプターの例外のフィルターの label に、有効なら [x]、無効なら [ ] を付ける。
// アダプターがフィルターを示さなければ、そのことを出す（基本設計 6.4 の capabilities の表。FR-309）。
// デバッグしていないときは、最後に使ったアダプターのフィルターを、次のデバッグの開始で送るものとして出す。
//
// req: FR-294, FR-309
func (p *pane) buildExceptions() ext.ViewComponent {
	adapter, filters, enabled, live := p.d.exceptionsAdapter()
	p.excAdapter = adapter
	switch {
	case adapter == "":
		return ext.ViewComponent{Kind: ext.ViewList, Items: []ext.ViewItem{{ID: "none", Label: "デバッグを始めると、アダプターの例外のフィルターを出す"}}}
	case len(filters) == 0:
		return ext.ViewComponent{Kind: ext.ViewList, Items: []ext.ViewItem{{ID: "none", Label: "このアダプターに例外のフィルターはありません（" + adapter + "）"}}}
	}
	items := make([]ext.ViewItem, 0, len(filters)+1)
	if !live {
		label := "次のデバッグの開始で送る（" + adapter + "）"
		items = append(items, ext.ViewItem{ID: "note", Label: label, Highlights: []ext.ViewSpan{{Start: 0, End: len(label), Style: "comment"}}})
	}
	for i, f := range filters {
		mark := "[ ] "
		if enabled[i] {
			mark = "[x] "
		}
		label := mark + oneLine(f.Label)
		hs := []ext.ViewSpan{{Start: 0, End: len(mark) - 1, Style: "keyword"}}
		if f.Description != "" {
			start := len(label) + 2
			label += "  " + oneLine(f.Description)
			hs = append(hs, ext.ViewSpan{Start: start, End: len(label), Style: "comment"})
		}
		items = append(items, ext.ViewItem{ID: exceptionItemID(f.Filter), Label: label, Highlights: hs})
	}
	return ext.ViewComponent{Kind: ext.ViewList, Items: items}
}

// toggleException は、例外の区画で選んだフィルターの有効と無効を切り替え、そのアダプターの動いているセッション（子のセッションを含む）へ
// setExceptionBreakpoints を送る（FR-294。nvim-dap-view の exceptions/init.lua も、同じアダプターのすべてのセッションへ送る）。
// 切り替えた状態は、アダプターの種類ごとに覚え、次のセッションの開始でも送る。
//
// req: FR-294
func (p *pane) toggleException(item string) {
	filter, ok := strings.CutPrefix(item, "x\x1f")
	adapter := p.excAdapter
	if !ok || adapter == "" {
		return
	}
	d := p.d
	d.mu.Lock()
	i := slices.IndexFunc(d.excFilters[adapter], func(f exceptionFilter) bool { return f.Filter == filter })
	if i < 0 {
		d.mu.Unlock()
		return
	}
	f := d.excFilters[adapter][i]
	on := !d.exceptionEnabledLocked(adapter, f)
	if d.excEnabled == nil {
		d.excEnabled = map[string]map[string]bool{}
	}
	if d.excEnabled[adapter] == nil {
		d.excEnabled[adapter] = map[string]bool{}
	}
	d.excEnabled[adapter][filter] = on
	var targets []*session
	for _, s := range d.sessions {
		if s.configured && s.spec.adapter.name == adapter {
			targets = append(targets, s)
		}
	}
	d.mu.Unlock()
	p.markDirty(secExceptions)
	for _, s := range targets {
		ctx, cancel := p.ctx()
		err := s.sendExceptionBreakpoints(ctx)
		cancel()
		if err != nil && !errors.Is(err, errDAPClosed) {
			d.showError(fmt.Sprintf("例外のフィルターを送れない（%s）: %s", s.label(), adapterMessage(err)))
		}
	}
}

// adapterMessage は、アダプターの失敗の応答なら、その文（body.error.format か message）だけを返す。ほかの誤りは Error() である。
func adapterMessage(err error) string {
	var de *dapError
	if errors.As(err, &de) {
		if de.Format != "" {
			return de.Format
		}
		if de.Message != "" {
			return de.Message
		}
	}
	return err.Error()
}

// showException は、reason が exception の停止で、例外の中身をメッセージの行と REPL の区画に出す（基本設計 6.4）。
// アダプターが exceptionInfo に対応すれば、止まったスレッド tid の exceptionInfo を読み、例外の ID、説明、止まった条件（breakMode）を
// メッセージの行に、型の名前とメッセージとスタックトレース（内側の例外を含む）を REPL に書き足す（nvim-dap の session.lua の
// _show_exception_info と同じ中身。nvim-dap はこれを診断にして止まった行に出す）。対応しなければ request を送らず、
// stopped の event の text か description を出す。止まった位置を出し終えた後（showStopped）に呼ぶ。
//
// req: FR-309
func (s *session) showException(ctx context.Context, gen uint64, tid int, body *stoppedEvent) {
	if s.gen.Load() != gen {
		return
	}
	summary := strings.TrimSpace(body.Text)
	if summary == "" {
		summary = strings.TrimSpace(body.Description)
	}
	var detail []string
	if s.supports(func(c *capabilities) bool { return c.SupportsExceptionInfoRequest }) {
		var res exceptionInfoResponse
		err := s.callGen(ctx, gen, "exceptionInfo", exceptionInfoArguments{ThreadID: tid}, &res)
		switch {
		case errors.Is(err, errStale):
			return
		case err != nil:
			s.d.logf("session %d: exceptionInfo: %v", s.id, err)
		default:
			summary = res.ExceptionID
			if res.BreakMode != "" {
				summary += "、" + res.BreakMode
			}
			if res.Description != "" {
				summary += ": " + res.Description
			}
			if res.Details != nil {
				detail = exceptionDetailLines(*res.Details, 0)
			}
		}
	}
	if summary == "" {
		summary = "例外の中身は分からない"
	}
	s.d.showError("例外で止まった（" + oneLine(summary) + "）")
	text := "例外で止まった（" + s.label() + "）: " + summary + "\n"
	for _, l := range detail {
		text += l + "\n"
	}
	s.d.pane.replWrite(text, "diagnostic.error")
}

// maxInnerExceptions は、REPL に出す内側の例外の深さの上限である（仮）。
const maxInnerExceptions = 5

// exceptionDetailLines は、例外の詳しい中身を、depth 段の字下げの行にする。
func exceptionDetailLines(e exceptionDetails, depth int) []string {
	indent := strings.Repeat("  ", depth+1)
	var lines []string
	head := strings.TrimSpace(strings.Join(slices.DeleteFunc([]string{e.TypeName, e.Message}, func(s string) bool { return s == "" }), ": "))
	if head != "" {
		lines = append(lines, indent+head)
	}
	if st := strings.TrimRight(normalizeNewlines(e.StackTrace), "\n"); st != "" {
		for _, l := range strings.Split(st, "\n") {
			lines = append(lines, indent+l)
		}
	}
	if depth+1 < maxInnerExceptions {
		for _, in := range e.InnerException {
			lines = append(lines, indent+"内側の例外:")
			lines = append(lines, exceptionDetailLines(in, depth+1)...)
		}
	}
	return lines
}

// normalizeNewlines は、\r\n と \r を \n にする。
func normalizeNewlines(s string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
}
