package mux

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// herdrRequest は、偽の herdr が受けたリクエストである。
type herdrRequest struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

// fakeHerdr は、herdr の socket API の偽物である。受けたリクエストを記録し、reply が返す値を応答する。
// reply が返す値は、result か error の値を持つ map である。nil を返すと応答しない。
type fakeHerdr struct {
	socket string
	reply  func(req herdrRequest) map[string]any

	mu   sync.Mutex
	reqs []herdrRequest
}

// newFakeHerdr は、偽の herdr を一時ディレクトリのソケットで待ち受けさせる。
// ソケットのパスの長さの上限（108 バイト）に収めるため、t.TempDir ではなく短い名前の一時ディレクトリを使う。
func newFakeHerdr(t *testing.T, reply func(req herdrRequest) map[string]any) *fakeHerdr {
	t.Helper()
	dir, err := os.MkdirTemp("", "mux")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fakeHerdr{socket: filepath.Join(dir, "h.sock"), reply: reply}
	ln, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeHerdr) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var req herdrRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	resp := f.reply(req)
	if resp == nil {
		// 応答しないまま、相手が閉じるまで待つ。
		_, _ = conn.Read(make([]byte, 1))
		return
	}
	if _, ok := resp["id"]; !ok {
		resp["id"] = req.ID
	}
	b, _ := json.Marshal(resp)
	_, _ = conn.Write(append(b, '\n'))
}

// requests は、受けたリクエストの写しを返す。
func (f *fakeHerdr) requests() []herdrRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]herdrRequest(nil), f.reqs...)
}

// only は、受けたリクエストがちょうど 1 つであることを確かめて返す。
func (f *fakeHerdr) only(t *testing.T) herdrRequest {
	t.Helper()
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("リクエストの数 = %d, want 1: %+v", len(reqs), reqs)
	}
	return reqs[0]
}

func ok(result map[string]any) map[string]any {
	return map[string]any{"result": result}
}

func herdrErr(code, message string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": message}}
}

// paneInfo は、pane.get などが返す PaneInfo の偽物である。
func paneInfo(pane, tab string) map[string]any {
	return map[string]any{
		"pane_id": pane, "tab_id": tab, "workspace_id": "wW", "terminal_id": "term_1",
		"focused": false, "agent_status": "unknown", "revision": 0,
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestHerdrCurrentPaneAndUnit(t *testing.T) {
	// HERDR_PANE_ID の古い ID（wT:p90）で引くと、herdr は今の ID（wW:p7）のペインを返す。
	f := newFakeHerdr(t, func(req herdrRequest) map[string]any {
		if req.Method != "pane.get" || req.Params["pane_id"] != "wT:p90" {
			return herdrErr("pane_not_found", "pane not found")
		}
		return ok(map[string]any{"type": "pane_info", "pane": paneInfo("wW:p7", "wW:t4")})
	})
	h := Detect(envOf(map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "wT:p90", "HERDR_SOCKET_PATH": f.socket}))
	ctx := testCtx(t)
	pane, err := h.CurrentPane(ctx)
	if err != nil || pane != "wW:p7" {
		t.Errorf("CurrentPane = %q, %v; want wW:p7", pane, err)
	}
	unit, err := h.Unit(ctx)
	if want := f.socket + ":wW:t4"; err != nil || unit != want {
		t.Errorf("Unit = %q, %v; want %q", unit, err, want)
	}
}

// PaneFocused は、pane.get の focused を返す（#244）。
func TestHerdrPaneFocused(t *testing.T) {
	f := newFakeHerdr(t, func(req herdrRequest) map[string]any {
		p := paneInfo(req.Params["pane_id"].(string), "wW:t4")
		p["focused"] = req.Params["pane_id"] == "wW:p1"
		return ok(map[string]any{"type": "pane_info", "pane": p})
	})
	h := &Herdr{Socket: f.socket, Pane: "wW:p1"}
	ctx := testCtx(t)
	if got, err := h.PaneFocused(ctx, "wW:p1"); err != nil || !got {
		t.Errorf("PaneFocused(wW:p1) = %v, %v; want true", got, err)
	}
	if got, err := h.PaneFocused(ctx, "wW:p2"); err != nil || got {
		t.Errorf("PaneFocused(wW:p2) = %v, %v; want false", got, err)
	}
}

func TestHerdrSplitPane(t *testing.T) {
	f := newFakeHerdr(t, func(herdrRequest) map[string]any {
		return ok(map[string]any{"type": "plugin_pane_opened", "plugin_pane": map[string]any{
			"plugin_id": HerdrPluginID, "entrypoint": HerdrPluginEntrypoint, "pane": paneInfo("wW:p1W", "wW:t4"),
		}})
	})
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	pane, err := h.SplitPane(testCtx(t), SplitOptions{
		Target:    "wW:p8",
		Direction: Down,
		Size:      30,
		Focus:     true,
		Command: Command{
			Args: []string{"/opt/vi-imitation", "--pane", "it's"},
			Env:  map[string]string{"VI_IMITATION_SOCKET": "/run/me/server.sock"},
			Dir:  "/work",
		},
	})
	if err != nil || pane != "wW:p1W" {
		t.Fatalf("SplitPane = %q, %v; want wW:p1W", pane, err)
	}
	req := f.only(t)
	want := map[string]any{
		"plugin_id":      HerdrPluginID,
		"entrypoint":     HerdrPluginEntrypoint,
		"placement":      "split",
		"direction":      "down",
		"target_pane_id": "wW:p8",
		"focus":          true,
		"cwd":            "/work",
		"env": map[string]any{
			"VI_IMITATION_SOCKET": "/run/me/server.sock",
			HerdrCommandEnv:       `/opt/vi-imitation --pane 'it'\''s'`,
		},
	}
	if req.Method != "plugin.pane.open" || !reflect.DeepEqual(req.Params, want) {
		t.Errorf("リクエスト = %s %v\nwant plugin.pane.open %v", req.Method, req.Params, want)
	}
}

func TestHerdrSplitPaneDefaults(t *testing.T) {
	f := newFakeHerdr(t, func(herdrRequest) map[string]any {
		return ok(map[string]any{"plugin_pane": map[string]any{"pane": paneInfo("wW:p2", "wW:t4")}})
	})
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	if _, err := h.SplitPane(testCtx(t), SplitOptions{Target: "wW:p8", Direction: Right, Command: Command{Args: []string{"x"}}}); err != nil {
		t.Fatal(err)
	}
	p := f.only(t).Params
	if p["target_pane_id"] != "wW:p8" || p["direction"] != "right" || p["focus"] != false {
		t.Errorf("params = %v, want target wW:p8, right, focus false", p)
	}
	if _, ok := p["cwd"]; ok {
		t.Errorf("Dir が空なのに cwd を送った: %v", p)
	}
}

// TestHerdrSplitPaneLeft は、Left なら右に開いてから pane.swap で元のペインと入れ替え、
// Focus のとおりのペインへ pane.focus でフォーカスを移し直すことを確かめる（#437）。
func TestHerdrSplitPaneLeft(t *testing.T) {
	for _, tc := range []struct {
		focus bool
		want  string
	}{{true, "wW:p2"}, {false, "wW:p8"}} {
		f := newFakeHerdr(t, func(req herdrRequest) map[string]any {
			if req.Method == "pane.swap" || req.Method == "pane.focus" {
				return ok(map[string]any{"type": "ok"})
			}
			return ok(map[string]any{"plugin_pane": map[string]any{"pane": paneInfo("wW:p2", "wW:t4")}})
		})
		h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
		pane, err := h.SplitPane(testCtx(t), SplitOptions{Target: "wW:p8", Direction: Left, Focus: tc.focus, Command: Command{Args: []string{"x"}}})
		if err != nil || pane != "wW:p2" {
			t.Fatalf("SplitPane = %q, %v; want wW:p2", pane, err)
		}
		reqs := f.requests()
		if len(reqs) != 3 {
			t.Fatalf("リクエスト = %v, want plugin.pane.open、pane.swap、pane.focus", reqs)
		}
		if p := reqs[0].Params; reqs[0].Method != "plugin.pane.open" || p["direction"] != "right" || p["target_pane_id"] != "wW:p8" || p["focus"] != tc.focus {
			t.Errorf("1 つ目 = %s %v, want plugin.pane.open の right", reqs[0].Method, p)
		}
		want := map[string]any{"source_pane_id": "wW:p2", "target_pane_id": "wW:p8"}
		if reqs[1].Method != "pane.swap" || !reflect.DeepEqual(reqs[1].Params, want) {
			t.Errorf("2 つ目 = %s %v, want pane.swap %v", reqs[1].Method, reqs[1].Params, want)
		}
		if reqs[2].Method != "pane.focus" || reqs[2].Params["pane_id"] != tc.want {
			t.Errorf("focus=%v: 3 つ目 = %s %v, want pane.focus %s", tc.focus, reqs[2].Method, reqs[2].Params, tc.want)
		}
	}
}

// TestHerdrSplitPaneLeftSwapFails は、入れ替えに失敗したら、開いたペインを閉じて誤りを返すことを確かめる（#437）。
func TestHerdrSplitPaneLeftSwapFails(t *testing.T) {
	f := newFakeHerdr(t, func(req herdrRequest) map[string]any {
		switch req.Method {
		case "pane.swap":
			return herdrErr("invalid_params", "nope")
		case "pane.close":
			return ok(map[string]any{"type": "ok"})
		}
		return ok(map[string]any{"plugin_pane": map[string]any{"pane": paneInfo("wW:p2", "wW:t4")}})
	})
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	if _, err := h.SplitPane(testCtx(t), SplitOptions{Target: "wW:p8", Direction: Left, Command: Command{Args: []string{"x"}}}); err == nil {
		t.Fatal("SplitPane: err = nil, want the pane.swap error")
	}
	reqs := f.requests()
	if len(reqs) != 3 || reqs[2].Method != "pane.close" || reqs[2].Params["pane_id"] != "wW:p2" {
		t.Errorf("リクエスト = %v, want 最後に wW:p2 の pane.close", reqs)
	}
}

// TestHerdrEmptyTarget は、元のペインが空なら、server の環境変数のペインを既定にせず ErrNoTarget を返すことを確かめる。
func TestHerdrEmptyTarget(t *testing.T) {
	f := newFakeHerdr(t, func(herdrRequest) map[string]any { return ok(map[string]any{"type": "ok"}) })
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	cmd := Command{Args: []string{"x"}}
	if _, err := h.SplitPane(testCtx(t), SplitOptions{Command: cmd}); !errors.Is(err, ErrNoTarget) {
		t.Errorf("SplitPane: err = %v, want ErrNoTarget", err)
	}
	if err := h.OpenPopup(testCtx(t), PopupOptions{Command: cmd}); !errors.Is(err, ErrNoTarget) {
		t.Errorf("OpenPopup: err = %v, want ErrNoTarget", err)
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("リクエストを %d 件送った", n)
	}
}

func TestHerdrOpenPopupBusy(t *testing.T) {
	f := newFakeHerdr(t, func(herdrRequest) map[string]any { return herdrErr("ui_busy", "a popup pane is already open") })
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	err := h.OpenPopup(testCtx(t), PopupOptions{Target: "wW:p7", Command: Command{Args: []string{"x"}}})
	if !errors.Is(err, ErrPopupBusy) {
		t.Errorf("err = %v, want ErrPopupBusy", err)
	}
	if he, ok := errors.AsType[*HerdrError](err); !ok || he.Code != "ui_busy" {
		t.Errorf("err = %v, want HerdrError ui_busy も含む", err)
	}
}

func TestHerdrSplitPaneWithoutPaneID(t *testing.T) {
	f := newFakeHerdr(t, func(herdrRequest) map[string]any { return ok(map[string]any{"type": "ok"}) })
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	if _, err := h.SplitPane(testCtx(t), SplitOptions{Target: "wW:p7", Command: Command{Args: []string{"x"}}}); err == nil {
		t.Error("ペインの ID の無い応答を受け付けた")
	}
}

func TestHerdrOpenPopup(t *testing.T) {
	f := newFakeHerdr(t, func(herdrRequest) map[string]any { return ok(map[string]any{"type": "ok"}) })
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	err := h.OpenPopup(testCtx(t), PopupOptions{
		Target: "wW:p7", Width: 80, Height: 60,
		Command: Command{Args: []string{"vi-imitation", "--popup"}, Env: map[string]string{"A": "1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"plugin_id":  HerdrPluginID,
		"entrypoint": HerdrPluginEntrypoint,
		"placement":  "popup",
		"width":      "80%",
		"height":     "60%",
		"env":        map[string]any{"A": "1", HerdrCommandEnv: "vi-imitation --popup"},
	}
	// herdr 0.9.0 はポップアップの target_pane_id を invalid_params で断るので、送らない。
	if req := f.only(t); req.Method != "plugin.pane.open" || !reflect.DeepEqual(req.Params, want) {
		t.Errorf("リクエスト = %s %v\nwant plugin.pane.open %v", req.Method, req.Params, want)
	}
}

// TestHerdrOpenPopupKeepOnFailure は、KeepOnFailure のポップアップのコマンドを keepScript で包むことと、
// 包んだコマンドを宣言ファイルの command で動かすと、0 で終わればすぐ終わり、0 でなければ終了コードを出し、
// キーを 1 つ（Enter でなく q でもよい）読むまで待ってから、同じ終了コードで終わることを確かめる（FR-168）。
//
// req: FR-168
func TestHerdrOpenPopupKeepOnFailure(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh が無い")
	}
	f := newFakeHerdr(t, func(herdrRequest) map[string]any { return ok(map[string]any{"type": "ok"}) })
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	for _, status := range []int{0, 3} {
		args := []string{"sh", "-c", `echo ran; exit "$1"`, "sh", strconv.Itoa(status)}
		if err := h.OpenPopup(testCtx(t), PopupOptions{Target: "wW:p7", KeepOnFailure: true, Command: Command{Args: args}}); err != nil {
			t.Fatal(err)
		}
	}
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("リクエストの数 = %d, want 2", len(reqs))
	}
	manifest := manifestCommand(t)
	for i, status := range []int{0, 3} {
		env, _ := reqs[i].Params["env"].(map[string]any)
		command, _ := env[HerdrCommandEnv].(string)
		if !strings.HasPrefix(command, shellJoin([]string{"sh", "-c", keepScript, "sh"})) {
			t.Fatalf("コマンドを包んでいない: %q", command)
		}
		c := exec.CommandContext(testCtx(t), manifest[0], manifest[1:]...) //nolint:gosec // 同梱する宣言ファイルの command を試す
		c.Env = append(os.Environ(), HerdrCommandEnv+"="+command)
		in, err := c.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		c.Stdout = &out
		if err = c.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		if status != 0 {
			// 0 でないときは、キーを打つまで終わらない。
			select {
			case werr := <-done:
				t.Fatalf("キーを待たずに終わった: %v, %q", werr, out.String())
			case <-time.After(300 * time.Millisecond):
			}
			if _, err = in.Write([]byte("q")); err != nil {
				t.Fatal(err)
			}
		}
		err = <-done
		_ = in.Close()
		got := 0
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			got = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if got != status {
			t.Errorf("終了コード = %d, want %d", got, status)
		}
		prompted := strings.Contains(out.String(), "閉じるには何かキーを押してください")
		if !strings.HasPrefix(out.String(), "ran\n") || prompted != (status != 0) {
			t.Errorf("終了コード %d の出力 = %q", status, out.String())
		}
		if status != 0 && !strings.Contains(out.String(), "シェルが 3 を返しました") {
			t.Errorf("終了コードを出していない: %q", out.String())
		}
	}
}

func TestHerdrClosePopup(t *testing.T) {
	code := "popup_not_open"
	f := newFakeHerdr(t, func(herdrRequest) map[string]any { return herdrErr(code, "no popup is open") })
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	ctx := testCtx(t)
	if err := h.ClosePopup(ctx, ""); err != nil {
		t.Errorf("開いていないポップアップを閉じて誤り: %v", err)
	}
	code = "internal"
	if err := h.ClosePopup(ctx, ""); err == nil {
		t.Error("popup_not_open でない誤りを捨てた")
	}
	for _, req := range f.requests() {
		if req.Method != "popup.close" {
			t.Errorf("method = %s, want popup.close", req.Method)
		}
	}
}

func TestHerdrFocusAndClosePane(t *testing.T) {
	f := newFakeHerdr(t, func(herdrRequest) map[string]any {
		return ok(map[string]any{"type": "pane_info", "pane": paneInfo("wW:p1", "wW:t1")})
	})
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	ctx := testCtx(t)
	if err := h.FocusPane(ctx, "wW:p7", "wW:p1"); err != nil {
		t.Fatal(err)
	}
	if err := h.ClosePane(ctx, "wW:p2"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range f.requests() {
		got = append(got, r.Method+" "+r.Params["pane_id"].(string))
	}
	if want := []string{"pane.focus wW:p1", "pane.close wW:p2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("リクエスト = %q, want %q", got, want)
	}
}

func TestHerdrError(t *testing.T) {
	f := newFakeHerdr(t, func(herdrRequest) map[string]any { return herdrErr("pane_not_found", "pane wW:p9 not found") })
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	err := h.FocusPane(testCtx(t), "wW:p7", "wW:p9")
	he, ok := errors.AsType[*HerdrError](err)
	if !ok || he.Code != "pane_not_found" || he.Method != "pane.focus" {
		t.Errorf("err = %v, want HerdrError pane_not_found", err)
	}
}

func TestHerdrResponseIDMismatch(t *testing.T) {
	f := newFakeHerdr(t, func(herdrRequest) map[string]any {
		return map[string]any{"id": "other", "result": map[string]any{"type": "ok"}}
	})
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	if err := h.ClosePane(testCtx(t), "wW:p1"); err == nil {
		t.Error("ID の違う応答を受け付けた")
	}
}

func TestHerdrContextCanceled(t *testing.T) {
	f := newFakeHerdr(t, func(herdrRequest) map[string]any { return nil })
	h := &Herdr{Socket: f.socket, Pane: "wW:p7"}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	err := h.FocusPane(ctx, "wW:p7", "wW:p1")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("取り消してから戻るまで %v かかった", d)
	}
}

func TestHerdrNoServer(t *testing.T) {
	h := &Herdr{Socket: filepath.Join(t.TempDir(), "none.sock"), Pane: "wW:p7"}
	if err := h.ClosePane(testCtx(t), "wW:p1"); err == nil {
		t.Error("herdr のソケットが無いのに誤りにならなかった")
	}
}

// manifestCommand は、宣言ファイルの [[panes]] の command の配列を取り出す。
// TOML の基本の文字列の配列は、ここで使う範囲では JSON の配列と同じ書き方である。
func manifestCommand(t *testing.T) []string {
	t.Helper()
	for line := range strings.SplitSeq(string(HerdrPluginManifest), "\n") {
		if rest, found := strings.CutPrefix(line, "command = "); found {
			var cmd []string
			if err := json.Unmarshal([]byte(rest), &cmd); err != nil {
				t.Fatalf("command を読めない: %v", err)
			}
			return cmd
		}
	}
	t.Fatal("宣言ファイルに command が無い")
	return nil
}

func TestHerdrPluginManifest(t *testing.T) {
	m := string(HerdrPluginManifest)
	for _, want := range []string{
		`id = "` + HerdrPluginID + `"`,
		`id = "` + HerdrPluginEntrypoint + `"`,
		"$" + HerdrCommandEnv,
		`placement = "popup"`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("宣言ファイルに %q が無い", want)
		}
	}
}

// TestHerdrPluginCommandRunsArgs は、宣言ファイルの command が、HerdrCommandEnv に入れたコマンドを
// 引数を崩さずに起動することを、sh で確かめる。
func TestHerdrPluginCommandRunsArgs(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh が無い")
	}
	cmd := manifestCommand(t)
	args := []string{"printf", `%s\n`, "with space", "it's", "$HOME", "a=b"}
	params := (&Herdr{}).openParams("popup", Command{Args: args})
	env := params["env"].(map[string]string)
	ctx := testCtx(t)
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...) //nolint:gosec // 同梱する宣言ファイルの command を試す
	c.Env = append(os.Environ(), HerdrCommandEnv+"="+env[HerdrCommandEnv])
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "with space\nit's\n$HOME\na=b\n"
	if !bytes.Equal(out, []byte(want)) {
		t.Errorf("出力 = %q, want %q", out, want)
	}
}
