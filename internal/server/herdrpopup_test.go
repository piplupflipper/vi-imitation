package server

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/mux"
	"github.com/piplupflipper/vi-imitation/internal/mux/muxtest"
	"github.com/piplupflipper/vi-imitation/internal/protocol"
)

// fakeWindows は、herdr app の mod の窓（popup。#452）を真似た偽の係である。
// popup の依頼を受けると、窓が開いていれば Busy を返し、開いていなければ窓を開いた印を付け、窓の端末の中身を別の goroutine で動かす。
// 中身が終わったら（コマンドが終わった、部品の client が終わりの知らせを受けた）窓を閉じる。
// 中身は、argv が部品の client（-popup <部品の ID> -connect <ソケット>）ならソケットに繋ぐポップアップの部品の client として、
// そうでなければ argv をそのまま Cwd で動かす（hc run の代わり。env の環境変数で HC_TERMINAL_ID を入れ直すのも、そのまま効く）。
type fakeWindows struct {
	t *testing.T

	mu     sync.Mutex
	open   bool
	popups []muxtest.Request
	closed int
	// screens は、部品の client が受けた画面を送る。
	screens chan string
	// comp は、開いている部品の client の接続である。
	comp protocol.ClientConn
}

func newFakeWindows(t *testing.T) *fakeWindows {
	return &fakeWindows{t: t, screens: make(chan string, 64)}
}

// handle は、偽の係（muxtest.HC.ServeMod）の handle である。
func (w *fakeWindows) handle(_ string, req muxtest.Request) muxtest.Reply {
	switch req.Op {
	case "hello":
		return muxtest.Reply{Ops: []string{"split", "focus", "focused", "popup", "popup_close"}}
	case "popup":
		w.mu.Lock()
		if w.open {
			w.mu.Unlock()
			return muxtest.Reply{Busy: true, Error: "a vi-imitation popup is already open"}
		}
		w.open = true
		w.popups = append(w.popups, req)
		w.mu.Unlock()
		go func() {
			w.run(req)
			w.mu.Lock()
			w.open = false
			w.closed++
			w.mu.Unlock()
		}()
		return muxtest.Reply{Terminal: "t20"}
	}
	return muxtest.Reply{Error: "unknown op " + req.Op}
}

// run は、窓の端末の中身を動かし、終わるまで待つ。
func (w *fakeWindows) run(req muxtest.Request) {
	if i := slices.Index(req.Argv, ComponentFlagPopup); i >= 0 {
		j := slices.Index(req.Argv, "-connect")
		view, err := strconv.ParseUint(req.Argv[i+1], 10, 64)
		if err != nil || j < 0 {
			w.t.Errorf("部品の client の argv = %q", req.Argv)
			return
		}
		c, err := protocol.Dial(req.Argv[j+1])
		if err != nil {
			w.t.Errorf("Dial: %v", err)
			return
		}
		defer func() { _ = c.Close() }()
		w.mu.Lock()
		w.comp = c
		w.mu.Unlock()
		if err := c.Send(protocol.Hello{Kind: protocol.KindPopup, Width: 50, Height: 10, View: view}); err != nil {
			w.t.Errorf("Send: %v", err)
			return
		}
		for {
			m, err := c.Recv()
			if err != nil {
				return
			}
			switch m := m.(type) {
			case protocol.Draw:
				select {
				case w.screens <- m.Screen.Grid.String():
				default:
				}
			case protocol.Exit:
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...) //nolint:gosec // テストの窓の端末で、本体が組み立てたコマンドを動かす
	cmd.Dir = req.Cwd
	cmd.Env = append(os.Environ(), "HC_TERMINAL_ID=t20")
	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Logf("窓のコマンド: %v\n%s", err, out)
	}
}

// waitClosed は、窓が n 回閉じるまで待つ。
func (w *fakeWindows) waitClosed(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w.mu.Lock()
		closed := w.closed
		w.mu.Unlock()
		if closed >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("窓が閉じない（%d 回閉じた, want %d）", closed, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitPopups は、popup の依頼を n 個受けるまで待ち、受けた依頼を返す。
func (w *fakeWindows) waitPopups(t *testing.T, n int) []muxtest.Request {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w.mu.Lock()
		got := slices.Clone(w.popups)
		w.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("popup の依頼が %d 個届かない（%d 個）", n, len(got))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitPopupReady は、server が mod への問い（client が繋いだときの hello）を終えるまで待つ。
func waitPopupReady(t *testing.T, srv *Server) {
	t.Helper()
	if srv.herdrPopups == nil {
		t.Fatal("herdr app の中で、ポップアップの答えを覚えていない")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.herdrPopups.waitAsked(ctx); err != nil {
		t.Fatalf("mod への問いが終わらない: %v", err)
	}
}

// herdr app の中で、mod がポップアップを知っていれば（hello の答え）、:terminal.popup は、元の client の端末 t7 に popup の依頼を書く。
// 依頼のコマンドは、0 でないときにキーを待つスクリプトで包み、中で起動した vi-imitation が t7 のタブの server に繋ぐように
// HC_TERMINAL_ID を t7 にし、作業ディレクトリは client のものにする。窓はコマンドが終わると閉じる（偽の係が真似る）。
// 窓が開いている間の次の :terminal.popup は、mod が Busy を返し、開き直しても開けなければ誤りを出す。
//
// req: FR-325, FR-327
func TestTerminalPopupHerdrApp(t *testing.T) {
	hc := inHerdrApp(t)
	t.Setenv("SHELL", "/bin/sh")
	w := newFakeWindows(t)
	hc.ServeMod(t, w.handle)
	c, ch, srv, dir := startHerdrAppServerAt(t)
	waitPopupReady(t, srv)

	sendKeys(t, c, ":terminal.popup echo $HC_TERMINAL_ID > out.txt<CR>")
	req := w.waitPopups(t, 1)[0]
	w.waitClosed(t, 1)
	if req.Cwd != dir || req.Height != popupHeight || req.Width != 90 {
		t.Errorf("依頼 = %+v, want 作業ディレクトリ %s、幅 90、高さ %d", req, dir, popupHeight)
	}
	if !slices.Contains(req.Argv, "HC_TERMINAL_ID=t7") || !strings.Contains(strings.Join(req.Argv, " "), "dd bs=1 count=1") {
		t.Errorf("argv = %q, want HC_TERMINAL_ID=t7 と、0 でないときにキーを待つスクリプト", req.Argv)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "out.txt")); err != nil || strings.TrimSpace(string(b)) != "t7" {
		t.Errorf("窓のコマンドの出力 = %q, %v; want t7", b, err)
	}
	if got := servedOps(hc, "popup"); len(got) != 1 || got[0].Terminal != "t7" {
		t.Errorf("popup の依頼 = %+v, want 端末 t7 に 1 つ", got)
	}
	waitNoLabels(t, hc, "t7")

	// 窓が開いている間は、mod が Busy を返す。前の窓が popupBusyTimeout（1.5 秒）より後に閉じても、herdr app では
	// herdrAppPopupBusyTimeout まで開き直すので、閉じた後に開く（PR #453 のレビューの推奨 2）。
	sendKeys(t, c, ":terminal.popup sleep 2<CR>")
	w.waitPopups(t, 2)
	sendKeys(t, c, ":terminal.popup true<CR>")
	w.waitPopups(t, 3)
	w.waitClosed(t, 3)

	// 開き直しても開けなければ、誤りをメッセージの行に出す。
	old := herdrAppPopupBusyTimeout.Load()
	herdrAppPopupBusyTimeout.Store(int64(500 * time.Millisecond))
	t.Cleanup(func() { herdrAppPopupBusyTimeout.Store(old) })
	sendKeys(t, c, ":terminal.popup sleep 3<CR>")
	w.waitPopups(t, 4)
	sendKeys(t, c, ":terminal.popup true<CR>")
	waitScreenText(t, ch, "端末多重化ソフトのポップアップを開けません")
	if got := w.waitPopups(t, 4); len(got) != 4 {
		t.Errorf("窓が開いている間に開いた: %+v", got)
	}
	w.waitClosed(t, 4)
}

// herdr app の中で、mod がポップアップを知っていれば、中央のポップアップの部品（:h のヘルプ）は、mod の窓の中の部品の client に描く。
// 部品の client で <Esc> を打つと部品を閉じ、部品の client は終わりの知らせを受けて終わり、窓が閉じる。
//
// req: FR-325
func TestComponentPopupHerdrApp(t *testing.T) {
	hc := inHerdrApp(t)
	w := newFakeWindows(t)
	hc.ServeMod(t, w.handle)
	c, _, srv, _ := startHerdrAppServerAt(t)
	waitPopupReady(t, srv)

	sendKeys(t, c, ":h K<CR>")
	req := w.waitPopups(t, 1)[0]
	if req.Argv[0] != "env" || !slices.Contains(req.Argv, ComponentFlagPopup) || req.Width != popupWidth || req.Height != popupHeight {
		t.Errorf("依頼 = %+v, want 部品の client を起動する依頼", req)
	}
	waitScreenText(t, w.screens, "K")
	w.mu.Lock()
	comp := w.comp
	w.mu.Unlock()
	sendKeys(t, comp, "<Esc>")
	w.waitClosed(t, 1)
	// 本体が mod の結果を読んで、依頼のラベルを片付ける（OpenPopup が戻る）まで待つ。
	waitNoLabels(t, hc, "t7")
	if got := servedOps(hc, "popup"); len(got) != 1 || got[0].Terminal != "t7" {
		t.Errorf("popup の依頼 = %+v, want 端末 t7 に 1 つ", got)
	}
}

// herdr app に mod が入っていないか、ポップアップを知らない古い mod（hello に unknown op）なら、中央のポップアップの部品は
// mod に頼まずに client の画面の中に描き、誤りを出さない。
//
// req: FR-326
func TestComponentPopupHerdrAppFallback(t *testing.T) {
	for _, name := range []string{"oldmod", "nomod"} {
		t.Run(name, func(t *testing.T) {
			hc := inHerdrApp(t)
			if name == "oldmod" {
				hc.ServeMod(t, func(_ string, req muxtest.Request) muxtest.Reply {
					return muxtest.Reply{Error: "unknown op " + req.Op}
				})
			}
			c, ch, srv, _ := startHerdrAppServerAt(t)
			if name == "oldmod" {
				waitPopupReady(t, srv)
			}
			sendKeys(t, c, ":h K<CR>")
			waitScreenText(t, ch, "[ヘルプ]")
			time.Sleep(300 * time.Millisecond)
			if got := servedOps(hc, "popup"); len(got) != 0 {
				t.Errorf("popup の依頼を書いた: %+v", got)
			}
			for k := range hc.Labels(t, "t7") {
				if strings.HasPrefix(k, mux.HerdrAppRequestPrefix) && name == "oldmod" {
					t.Errorf("依頼のラベル %s が残った", k)
				}
			}
		})
	}
}

// 覚えた答えは、問いの途中と、「開けない」と分かってから herdrPopupRetry の間は false で、その間は問い直さない。
// 問いが true を返したら true になる。開いた後に mod が無いか古いと分かったら（fail）、false に戻る。
// mod が入っていない（結果が届かない）問いは false として覚える。
//
// req: FR-326
func TestHerdrPopupsReady(t *testing.T) {
	var mu sync.Mutex
	answer, asked := false, 0
	probe := func(context.Context, string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		asked++
		return answer, nil
	}
	p := newHerdrPopups(nil, context.Background, probe)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if p.Ready("t7") {
		t.Error("問う前に true")
	}
	if err := p.waitAsked(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if p.Ready("t7") {
			t.Error("「開けない」の後に true")
		}
	}
	_ = p.waitAsked(ctx)
	mu.Lock()
	if asked != 1 {
		t.Errorf("問い = %d 回, want 1（herdrPopupRetry の間は問い直さない）", asked)
	}
	answer = true
	mu.Unlock()

	// herdrPopupRetry が経ったことにする。
	p.mu.Lock()
	p.failed = time.Now().Add(-herdrPopupRetry)
	p.mu.Unlock()
	p.Ready("t7")
	_ = p.waitAsked(ctx)
	if !p.Ready("t7") {
		t.Error("問いが true を返した後に false")
	}
	p.fail(errors.New("other"))
	if !p.Ready("t7") {
		t.Error("mod と関係の無い誤りで false になった")
	}
	p.fail(mux.ErrHerdrAppModOutdated)
	if p.Ready("t7") {
		t.Error("古い mod と分かった後に true")
	}

	// mod が入っていない（結果が届かない）。
	inHerdrApp(t)
	short, scancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer scancel()
	q := newHerdrPopups(nil, func() context.Context { return short }, herdrPopupProbe)
	q.prime("t7")
	if err := q.waitAsked(ctx); err != nil {
		t.Fatal(err)
	}
	if q.Ready("t7") {
		t.Error("mod が入っていないのに true")
	}
}
