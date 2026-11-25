package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// viewHost は、テストで本体の役をし、pane/open、view/update、view/append を、本体と同じ手順（ext.AppendViewText と
// ext.ViewTextOverflow）で部品ごとの中身に当てる。通知は届いた順に当てる（rpc.Conn は届いた順にハンドラーを呼び、
// ハンドラーは host.notes へ順に積む）。
type viewHost struct {
	*host
	mu    sync.Mutex
	next  ext.ViewID
	views map[ext.ViewID]*viewText
	// got は、当てた通知である。
	got []*rpc.Notification
	// failOpen が true なら、pane/open を誤りで断る。
	failOpen bool
	synced   chan struct{}
}

// methodMarker は、sync が notes に積む印である。拡張機能からは届かない。
const methodMarker = "test/marker"

type viewText struct {
	lines []string
	eol   bool
}

func (v *viewText) append(text string) {
	last := len(v.lines) - 1
	lines, _, eol := ext.AppendViewText(v.lines[last], nil, v.eol, text, nil)
	if !v.eol {
		v.lines = v.lines[:last]
	}
	v.lines = append(v.lines, lines...)
	v.eol = eol
	if d := ext.ViewTextOverflow(len(v.lines)); d > 0 {
		v.lines = v.lines[d:]
	}
}

func startViewHost(t *testing.T, s *server.Server) *viewHost {
	t.Helper()
	vh := &viewHost{views: map[ext.ViewID]*viewText{}, synced: make(chan struct{})}
	vh.host = startWith(t, s, func(req *rpc.Request, reply rpc.Replier) bool {
		if req.Method != ext.MethodPaneOpen {
			return false
		}
		var p ext.PaneOpenParams
		_ = json.Unmarshal(req.Params, &p)
		vh.mu.Lock()
		defer vh.mu.Unlock()
		if vh.failOpen {
			_ = reply(nil, &rpc.Error{Code: rpc.CodeInternalError, Message: "no pane"})
			return true
		}
		vh.next++
		lines, eol := ext.SplitViewText(p.Component.Text)
		vh.views[vh.next] = &viewText{lines: lines, eol: eol}
		_ = reply(ext.PaneOpenResult{View: vh.next}, nil)
		return true
	})
	// 読み取りの goroutine を止めないよう、届いた通知はすぐに当てる。
	go func() {
		for n := range vh.notes {
			if n.Method == methodMarker {
				vh.synced <- struct{}{}
				continue
			}
			vh.apply(n)
		}
	}()
	vh.ping(t) // Serve が接続を持つまで待つ
	return vh
}

func (vh *viewHost) apply(n *rpc.Notification) {
	vh.mu.Lock()
	defer vh.mu.Unlock()
	vh.got = append(vh.got, n)
	switch n.Method {
	case ext.MethodViewUpdate:
		var p ext.ViewUpdateParams
		_ = json.Unmarshal(n.Params, &p)
		if v := vh.views[p.View]; v != nil {
			v.lines, v.eol = ext.SplitViewText(p.Component.Text)
		}
	case ext.MethodViewAppend:
		var p ext.ViewAppendParams
		_ = json.Unmarshal(n.Params, &p)
		if v := vh.views[p.View]; v != nil && p.Child == "" {
			v.append(p.Text)
		}
	}
}

// sync は、拡張機能がそれまでに送った通知を、すべて当て終えるのを待つ。
func (vh *viewHost) sync(t *testing.T) {
	t.Helper()
	vh.ping(t)
	vh.notes <- &rpc.Notification{Method: methodMarker}
	recvWithin(t, vh.synced, "sync")
}

// count は、当てた通知の数を返す。
func (vh *viewHost) count() int {
	vh.mu.Lock()
	defer vh.mu.Unlock()
	return len(vh.got)
}

func (vh *viewHost) last() *rpc.Notification {
	vh.mu.Lock()
	defer vh.mu.Unlock()
	return vh.got[len(vh.got)-1]
}

func (vh *viewHost) text(view ext.ViewID) string {
	vh.mu.Lock()
	defer vh.mu.Unlock()
	v := vh.views[view]
	if v == nil {
		return "<closed>"
	}
	return ext.JoinViewText(v.lines, v.eol)
}

// closeView は、利用者が部品を閉じたことを本体の役から知らせる。
func (vh *viewHost) closeView(t *testing.T, view ext.ViewID) {
	t.Helper()
	vh.mu.Lock()
	delete(vh.views, view)
	vh.mu.Unlock()
	vh.notify(t, ext.MethodViewClosed, ext.ViewClosedParams{View: view})
}

func (vh *viewHost) setFailOpen(on bool) {
	vh.mu.Lock()
	vh.failOpen = on
	vh.mu.Unlock()
}

func openPane(s *server.Server) func(ext.ViewComponent) (ext.ViewID, error) {
	return func(c ext.ViewComponent) (ext.ViewID, error) {
		res, err := s.OpenPane(context.Background(), ext.PaneOpenParams{Component: c})
		return res.View, err
	}
}

// 写しは、結びついている間は view/append を送り、閉じている間は写しにだけ足す。閉じた知らせで、拡張機能が何もしなくても外れ、
// 開き直すと、閉じる前の中身と閉じている間に書き足した中身を出す。
//
// req: FR-273
func TestTextLog(t *testing.T) {
	s := server.New()
	var closed []ext.ViewID
	var mu sync.Mutex
	s.OnView(func(_ context.Context, e *server.ViewEvent) {
		if e.Kind == server.ViewClosed {
			mu.Lock()
			closed = append(closed, e.View)
			mu.Unlock()
		}
	})
	vh := startViewHost(t, s)
	log := server.NewTextLog(s)
	if err := log.Append("before\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := log.Attached(); ok {
		t.Fatalf("attached before Open")
	}
	if err := log.Open(openPane(s), ""); err != nil {
		t.Fatal(err)
	}
	view, child, ok := log.Attached()
	if !ok || child != "" {
		t.Fatalf("Attached = %v %q %v", view, child, ok)
	}
	if err := log.Append("one", "diagnostic.error"); err != nil {
		t.Fatal(err)
	}
	vh.sync(t)
	n := vh.last()
	var p ext.ViewAppendParams
	_ = json.Unmarshal(n.Params, &p)
	if n.Method != ext.MethodViewAppend || p.View != view || p.Text != "one" || len(p.Highlights) != 1 || p.Highlights[0] != (ext.ViewSpan{Start: 0, End: 3, Style: "diagnostic.error"}) {
		t.Fatalf("append = %s %s", n.Method, n.Params)
	}

	// 閉じた知らせで外れ、OnView のフックにも届く。閉じている間の書き足しは送らない。
	vh.closeView(t, view)
	vh.sync(t)
	if _, _, ok := log.Attached(); ok {
		t.Errorf("still attached after view/closed")
	}
	mu.Lock()
	if len(closed) != 1 || closed[0] != view {
		t.Errorf("OnView closed = %v", closed)
	}
	mu.Unlock()
	sent := vh.count()
	if err := log.Append(" two\nthree\n", ""); err != nil {
		t.Fatal(err)
	}
	vh.sync(t)
	if vh.count() != sent {
		t.Errorf("sent a view/append while closed: %s", vh.last().Params)
	}

	// 開き直すと、閉じる前と閉じている間の中身が戻る。色も戻る。
	var opened ext.ViewComponent
	if err := log.Open(func(c ext.ViewComponent) (ext.ViewID, error) {
		opened = c
		return openPane(s)(c)
	}, ""); err != nil {
		t.Fatal(err)
	}
	if opened.Text != "before\none two\nthree\n" {
		t.Errorf("reopened text = %q", opened.Text)
	}
	if len(opened.Highlights) != 1 || opened.Highlights[0].Range != (ext.Range{Start: ext.Position{Line: 1}, End: ext.Position{Line: 1, Byte: 3}}) {
		t.Errorf("reopened highlights = %+v", opened.Highlights)
	}
	view2, _, _ := log.Attached()
	if view2 == view {
		t.Fatalf("reopened view = %d", view2)
	}

	// 別の写しが同じ部品に付け替えると、前の写しは外れ、部品の中身は新しい写しの中身になる。
	other := server.NewTextLog(s)
	_ = other.Append("other\n", "")
	if err := other.Rebind(view2, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := log.Attached(); ok {
		t.Errorf("the old log is still attached after Rebind")
	}
	_ = log.Append("hidden\n", "")
	_ = other.Append("more\n", "")
	vh.sync(t)
	if got := vh.text(view2); got != "other\nmore\n" {
		t.Errorf("after Rebind: %q", got)
	}
	// 付け替え直すと、写しの中身（付け替えられている間の書き足しを含む）を出す。
	if err := log.Rebind(view2, ""); err != nil {
		t.Fatal(err)
	}
	vh.sync(t)
	if got := vh.text(view2); got != "before\none two\nthree\nhidden\n" {
		t.Errorf("after Rebind back: %q", got)
	}
	log.Detach()
	sent = vh.count()
	_ = log.Append("detached\n", "")
	vh.sync(t)
	if vh.count() != sent {
		t.Errorf("sent a view/append after Detach")
	}

	if err := log.Rebind(view2, "console"); !errors.Is(err, server.ErrChildUnsupported) {
		t.Errorf("Rebind to a child = %v", err)
	}
	// 閉じた部品には結びつけない。
	vh.closeView(t, view2)
	vh.sync(t)
	sent = vh.count()
	if err := log.Rebind(view2, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := log.Attached(); ok {
		t.Errorf("attached to a closed view")
	}
	vh.sync(t)
	if vh.count() != sent {
		t.Errorf("sent to a closed view")
	}

	// open が誤りを返したら結びつけず、その間の書き足しも写しに残す。
	vh.setFailOpen(true)
	err := log.Open(func(c ext.ViewComponent) (ext.ViewID, error) {
		_ = log.Append("during\n", "")
		return openPane(s)(c)
	}, "")
	if err == nil {
		t.Errorf("Open with a failing open = nil")
	}
	if _, _, ok := log.Attached(); ok {
		t.Errorf("attached after a failed Open")
	}
	vh.setFailOpen(false)
	// 開いている最中の書き足しは、開き終えてから送る。
	err = log.Open(func(c ext.ViewComponent) (ext.ViewID, error) {
		_ = log.Append("opening\n", "")
		if nested := log.Open(openPane(s), ""); !errors.Is(nested, server.ErrOpening) {
			t.Errorf("nested Open = %v", nested)
		}
		return openPane(s)(c)
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	view3, _, _ := log.Attached()
	vh.sync(t)
	if got := vh.text(view3); !strings.HasSuffix(got, "hidden\ndetached\nduring\nopening\n") {
		t.Errorf("after Open: %q", got)
	}

	// 開き直している最中に Detach を呼ぶと、open が戻っても結びつけない（PR #405 のレビューの推奨 1）。
	var tail bool
	if err := log.Open(func(c ext.ViewComponent) (ext.ViewID, error) {
		tail = c.Tail
		log.Detach()
		_ = log.Append("while detaching\n", "")
		return openPane(s)(c)
	}, ""); err != nil {
		t.Fatal(err)
	}
	if !tail {
		t.Errorf("Open did not ask to start at the tail")
	}
	if v, _, ok := log.Attached(); ok {
		t.Errorf("attached to %d after Detach during Open", v)
	}
	sent = vh.count()
	_ = log.Append("after detach\n", "")
	vh.sync(t)
	if vh.count() != sent {
		t.Errorf("sent a view/append after Detach during Open: %s", vh.last().Params)
	}
	// その後の Open は、また結びつける。
	if err := log.Open(openPane(s), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := log.Attached(); !ok {
		t.Errorf("not attached after the next Open")
	}
}

// 開いている間の書き足しが、つないだ 1 件の大きさの上限（ext.ViewAppendJoinMaxBytes）を超えたら、つなぐのをやめ、
// 開き終えたときに写しの中身の全体を view/update で送る。本体の役の中身は写しと同じになる（PR #405 の再レビュー）。
//
// req: FR-273
func TestTextLogOpenLargePending(t *testing.T) {
	s := server.New()
	vh := startViewHost(t, s)
	log := server.NewTextLog(s)
	chunk := strings.Repeat("z", 1<<20) // 改行の無い出力
	if err := log.Open(func(c ext.ViewComponent) (ext.ViewID, error) {
		for range 5 {
			_ = log.Append(chunk, "")
		}
		return openPane(s)(c)
	}, ""); err != nil {
		t.Fatal(err)
	}
	view, _, _ := log.Attached()
	vh.sync(t)
	if n := vh.last(); n.Method != ext.MethodViewUpdate {
		t.Errorf("last notification = %s, want view/update", n.Method)
	}
	if got := vh.text(view); got != log.Text() {
		t.Errorf("shown text differs from the copy (%d vs %d bytes)", len(got), len(log.Text()))
	}
	// 小さい書き足しは、今までどおり view/append でつないで送る。
	if err := log.Open(func(c ext.ViewComponent) (ext.ViewID, error) {
		_ = log.Append("small\n", "")
		return openPane(s)(c)
	}, ""); err != nil {
		t.Fatal(err)
	}
	vh.sync(t)
	if n := vh.last(); n.Method != ext.MethodViewAppend {
		t.Errorf("last notification = %s, want view/append", n.Method)
	}
}

// 写しは本体と同じ上限で古い行を捨てるので、開き直した中身は、本体が閉じる前に出していた中身と同じである。
//
// req: FR-273
func TestTextLogLimit(t *testing.T) {
	s := server.New()
	vh := startViewHost(t, s)
	log := server.NewTextLog(s)
	if err := log.Open(openPane(s), ""); err != nil {
		t.Fatal(err)
	}
	view, _, _ := log.Attached()
	for i := range ext.ViewTextMaxLines + 500 {
		if err := log.Append(fmt.Sprintf("line %d\n", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	_ = log.Append(strings.Repeat("x", 5000), "")
	vh.sync(t)
	shown := vh.text(view)
	if shown != log.Text() {
		t.Errorf("the copy differs from the shown text (%d vs %d bytes)", len(log.Text()), len(shown))
	}
	// 10500 行と、1 行の上限で 2 行に分けた 5000 バイトの行で 10502 行になり、古い 502 行を捨てる。
	if !strings.HasPrefix(shown, "line 502\n") {
		t.Errorf("first line = %q", shown[:20])
	}
}

// 開き直し（Open）と付け替え（Rebind）の最中に、別の goroutine から書き足しが届いても、部品の中身から抜けず、重ならない。
// -race で動かして、錠の取り方も確かめる。
//
// req: FR-273
func TestTextLogConcurrent(t *testing.T) {
	s := server.New()
	vh := startViewHost(t, s)
	logs := []*server.TextLog{server.NewTextLog(s), server.NewTextLog(s)}
	const writers, perWriter = 4, 300
	var wg sync.WaitGroup
	for li, log := range logs {
		for w := range writers {
			wg.Go(func() {
				for i := range perWriter {
					if err := log.Append(fmt.Sprintf("%d-%d-%d\n", li, w, i), ""); err != nil {
						t.Errorf("Append: %v", err)
						return
					}
				}
			})
		}
	}
	// 書き足している間に、開き直しと、2 つの写しの同じ部品への付け替えと、閉じた知らせを繰り返す。
	var shared ext.ViewID
	for round := range 60 {
		log := logs[round%2]
		switch round % 4 {
		case 0, 1:
			if err := log.Open(openPane(s), ""); err != nil {
				t.Fatal(err)
			}
			shared, _, _ = log.Attached()
		case 2:
			if err := log.Rebind(shared, ""); err != nil {
				t.Fatal(err)
			}
		case 3:
			if v, _, ok := log.Attached(); ok && round%8 == 3 {
				vh.closeView(t, v)
			}
		}
	}
	wg.Wait()
	vh.sync(t)
	for li, log := range logs {
		view, _, ok := log.Attached()
		if !ok {
			if err := log.Open(openPane(s), ""); err != nil {
				t.Fatal(err)
			}
			view, _, _ = log.Attached()
			vh.sync(t)
		}
		if got, want := vh.text(view), log.Text(); got != want {
			t.Errorf("log %d: the shown text differs from the copy (%d vs %d bytes)", li, len(got), len(want))
		}
		// 写しには、すべての書き足しが、書いた goroutine ごとの順で 1 回ずつ入っている。
		text := log.Text()
		for w := range writers {
			pos := 0
			for i := range perWriter {
				line := fmt.Sprintf("%d-%d-%d\n", li, w, i)
				j := strings.Index(text[pos:], line)
				if j < 0 {
					t.Fatalf("log %d: %q is missing or out of order", li, line)
				}
				pos += j + len(line)
			}
		}
		if n := strings.Count(text, "\n"); n != writers*perWriter {
			t.Errorf("log %d: %d lines, want %d", li, n, writers*perWriter)
		}
	}
}
