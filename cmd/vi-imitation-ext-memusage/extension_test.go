package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

const testTimeout = 5 * time.Second

// host は、本体の役である。届いた statusline/set と message/show と popup/open を記録し、popup/open には ID 1 で応答する。
type host struct {
	conn *rpc.Conn

	mu      sync.Mutex
	parts   []ext.StatusLineSetParams
	msgs    []ext.MessageShowParams
	popups  []ext.PopupOpenParams
	changed chan struct{}
}

func (h *host) record(f func()) {
	h.mu.Lock()
	f()
	h.mu.Unlock()
	select {
	case h.changed <- struct{}{}:
	default:
	}
}

// wait は、cond が true になるまで待つ。
func (h *host) wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		h.mu.Lock()
		ok := cond()
		h.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-h.changed:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("待ちきれない: %s", what)
		}
	}
}

// connect は、x を io.Pipe の上で動かし、settings を渡して initialize を済ませた本体の役を返す。
func connect(t *testing.T, x *extension, settings string) *host {
	t.Helper()
	h := &host{changed: make(chan struct{}, 1)}
	hostR, extW := io.Pipe()
	extR, hostW := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- x.server.Serve(context.Background(), extR, extW) }()
	h.conn = rpc.NewConn(hostR, hostW, rpc.HandlerFuncs{
		Notification: func(_ context.Context, n *rpc.Notification) {
			switch n.Method {
			case ext.MethodStatusLineSet:
				var p ext.StatusLineSetParams
				_ = json.Unmarshal(n.Params, &p)
				h.record(func() { h.parts = append(h.parts, p) })
			case ext.MethodMessageShow:
				var p ext.MessageShowParams
				_ = json.Unmarshal(n.Params, &p)
				h.record(func() { h.msgs = append(h.msgs, p) })
			}
		},
		Request: func(_ context.Context, req *rpc.Request, reply rpc.Replier) {
			if req.Method != ext.MethodPopupOpen {
				_ = reply(nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: req.Method})
				return
			}
			var p ext.PopupOpenParams
			_ = json.Unmarshal(req.Params, &p)
			h.record(func() { h.popups = append(h.popups, p) })
			_ = reply(ext.PopupOpenResult{View: 1}, nil)
		},
	})
	t.Cleanup(func() {
		_ = h.conn.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve = %v, want nil", err)
			}
		case <-time.After(testTimeout):
			t.Error("Serve did not return after the host closed")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	params := ext.InitializeParams{APIVersion: ext.APIVersion}
	if settings != "" {
		params.Settings = json.RawMessage(settings)
	}
	var res ext.InitializeResult
	if err := h.conn.Call(ctx, ext.MethodInitialize, params, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return h
}

// growingSource は、集計のたびに本体の RSS が 1 MiB ずつ増える偽の木を作る。
func growingSource(t *testing.T) func(context.Context) (source, error) {
	t.Helper()
	var n atomic.Int64
	return func(context.Context) (source, error) {
		src := newFakeTree(t)
		p := src.procs[100]
		p.rssKB += n.Add(1) * 1024
		src.procs[100] = p
		return src, nil
	}
}

func newTestExtension(t *testing.T) *extension {
	t.Helper()
	x := newExtension()
	x.root = func() int { return 100 }
	x.newSource = growingSource(t)
	return x
}

// 起動するとすぐに部品の中身を送り、設定の間隔で送り直す。
//
// req: FR-227
func TestStatusLineUpdates(t *testing.T) {
	h := connect(t, newTestExtension(t), `{"intervalMs": 500}`)
	start := time.Now()
	h.wait(t, "1 回目の部品", func() bool { return len(h.parts) >= 1 })
	h.wait(t, "2 回目の部品", func() bool { return len(h.parts) >= 2 })
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Errorf("2 回目までの間隔 = %s, want 500ms 前後", d)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.parts[0]; p.Part != partRSS || p.Text != "vi 19M · ext 512M" {
		t.Errorf("1 回目 = %+v", p)
	}
	if p := h.parts[1]; p.Text != "vi 20M · ext 512M" {
		t.Errorf("2 回目 = %+v", p)
	}
	if len(h.msgs) != 0 {
		t.Errorf("メッセージ = %+v", h.msgs)
	}
}

// 中身が変わらなければ送り直さない。
func TestStatusLineUnchanged(t *testing.T) {
	x := newExtension()
	x.root = func() int { return 100 }
	x.newSource = func(context.Context) (source, error) { return newFakeTree(t), nil }
	h := connect(t, x, `{"intervalMs": 500}`)
	h.wait(t, "1 回目の部品", func() bool { return len(h.parts) >= 1 })
	time.Sleep(1200 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.parts) != 1 {
		t.Errorf("変わらない中身を送り直した: %+v", h.parts)
	}
}

func TestParseSettings(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want time.Duration
		warn string
	}{
		{"", defaultInterval, ""},
		{`{}`, defaultInterval, ""},
		{`{"intervalMs": 1000}`, time.Second, ""},
		{`{"intervalMs": 500}`, 500 * time.Millisecond, ""},
		{`{"intervalMs": 499}`, defaultInterval, "500 以上 3600000 以下"},
		{`{"intervalMs": "5s"}`, defaultInterval, "読めない"},
		{`{"interval": 1000}`, defaultInterval, "読めない"},
	} {
		got, warn := parseSettings(json.RawMessage(c.raw))
		if got != c.want || (warn == nil) != (c.warn == "") || (warn != nil && !strings.Contains(warn.Error(), c.warn)) {
			t.Errorf("parseSettings(%s) = %s, %v; want %s, %q", c.raw, got, warn, c.want, c.warn)
		}
	}
}

// 設定の誤りは、既定の間隔で動きながら message/show で知らせる。
func TestSettingsWarning(t *testing.T) {
	h := connect(t, newTestExtension(t), `{"intervalMs": 10}`)
	h.wait(t, "設定の誤りのメッセージ", func() bool { return len(h.msgs) == 1 })
	h.wait(t, "部品", func() bool { return len(h.parts) >= 1 })
	h.mu.Lock()
	defer h.mu.Unlock()
	if m := h.msgs[0]; m.Level != ext.MessageWarning || !strings.Contains(m.Text, "intervalMs") {
		t.Errorf("メッセージ = %+v", m)
	}
}

// memusage.show は、受け付けたことを応答し、内訳を画面の中央のポップアップに出す。
//
// req: FR-228
func TestShow(t *testing.T) {
	h := connect(t, newTestExtension(t), "")
	if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var res ext.CommandExecuteResult
	if err := h.conn.Call(ctx, ext.MethodCommandExecute, ext.CommandExecuteParams{Buffer: 1, Version: 1, ID: cmdShow}, &res); err != nil || !res.OK {
		t.Fatalf("command/execute = %+v, %v", res, err)
	}
	h.wait(t, "ポップアップ", func() bool { return len(h.popups) == 1 })
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.popups[0]
	if p.Placement != ext.PopupCenter || p.Component.Kind != ext.ViewText || !strings.Contains(p.Component.Text, "lsp               101") ||
		!strings.Contains(p.Component.Text, "gopls serve") {
		t.Errorf("popup/open = %+v", p)
	}
}
