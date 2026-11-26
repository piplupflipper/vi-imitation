package server

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// herdr の中で diff/start のペインを左に開いたとき（Config.SplitPane の left）、入れ替えた後のフォーカスの移し直し（pane.focus）だけに
// 失敗しても、splitPane は誤りを返さない（PR #438 の再レビューの推奨）。ペインは開いて client も起動しているので、editor が待ちを捨てずに
// 見比べを続けるためである。開いたペインは閉じない。
func TestSplitPaneLeftFocusFailsHerdr(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "vih")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var methods []string
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadBytes('\n')
			var req struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(line, &req)
			mu.Lock()
			methods = append(methods, req.Method)
			mu.Unlock()
			var resp map[string]any
			switch req.Method {
			case "plugin.pane.open":
				resp = map[string]any{"id": req.ID, "result": map[string]any{"type": "plugin_pane_opened",
					"plugin_pane": map[string]any{"pane": map[string]any{"pane_id": "wW:p9", "tab_id": "wW:t1"}}}}
			case "pane.focus":
				resp = map[string]any{"id": req.ID, "error": map[string]any{"code": "pane_not_found", "message": "pane not found"}}
			default:
				resp = map[string]any{"id": req.ID, "result": map[string]any{"type": "ok"}}
			}
			b, _ := json.Marshal(resp)
			_, _ = conn.Write(append(b, '\n'))
			_ = conn.Close()
		}
	}()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "wW:p7")
	t.Setenv("HERDR_SOCKET_PATH", sock)
	s := &Server{log: slog.New(slog.DiscardHandler)}
	s.sock.Store(filepath.Join(sockDir, "server.sock"))
	if err := s.splitPane("wW:p8", "", t.TempDir(), "1", true); err != nil {
		t.Errorf("splitPane = %v, want nil（フォーカスだけの失敗は警告に留める）", err)
	}
	mu.Lock()
	got := slices.Clone(methods)
	mu.Unlock()
	if want := []string{"plugin.pane.open", "pane.swap", "pane.focus"}; !slices.Equal(got, want) {
		t.Errorf("herdr へのリクエスト = %v, want %v（開いたペインを閉じない）", got, want)
	}
}
