package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/editor"
	"github.com/piplupflipper/vi-imitation/internal/mux"
)

// fakeTmuxScript は、tmux の偽物である。呼ばれた引数を 1 行に 1 つずつ、呼び出しの終わりに空の行を置いて $FAKE_TMUX_LOG へ書く。
// display-message と list-clients には、ペインの window（@1）を表示している tmux の client を 1 つ返す。
// display-popup は、$FAKE_TMUX_BUSY のファイルが残っている間は 1 回ごとに 1 行消して何も開かずに 0 で終わり（ポップアップが既に開いている）、
// それ以外は最後の引数（コマンドの文字列）を sh -c で動かす（ポップアップの中のコマンド）。
const fakeTmuxScript = `#!/bin/sh
[ "$1" = -S ] && shift 2
for a; do printf '%s\n' "$a"; done >> "$FAKE_TMUX_LOG"
printf '\n' >> "$FAKE_TMUX_LOG"
case "$1" in
display-message) printf '$1\t@1\n' ;;
list-clients) printf '/dev/pts/9\t$1\t@1\t0\t200\n' ;;
display-popup)
	if [ -s "$FAKE_TMUX_BUSY" ]; then
		sed -i 1d "$FAKE_TMUX_BUSY"
		exit 0
	fi
	for a; do last=$a; done
	exec sh -c "$last" ;;
esac
`

// tmuxCalls は、偽の tmux のログを、呼び出しごとの引数の列に分ける。
func tmuxCalls(t *testing.T, log string) [][]string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var cur []string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			calls = append(calls, cur)
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return calls
}

// tmux の中の :terminal.popup（Config.RunPopup）は、元の client のペインの window を表示している tmux の client の上に、
// display-popup で、渡した幅と既定の高さのポップアップを、渡した作業ディレクトリで開き、渡したコマンドを、
// 0 でない終了コードならキーを待つスクリプトで包んで動かす。ポップアップが既に開いていれば（mux.ErrPopupBusy）、部品の client と同じく開き直す。
//
// req: FR-168
func TestRunPopupTmux(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(fakeTmuxScript), 0o700); err != nil { //nolint:gosec // テストの偽の tmux は実行できるようにする
		t.Fatal(err)
	}
	tmp := t.TempDir()
	log, busy, out := filepath.Join(tmp, "log"), filepath.Join(tmp, "busy"), filepath.Join(tmp, "out")
	// 最初の 2 回は、ポップアップが既に開いていて開けない。
	if err := os.WriteFile(busy, []byte("1\n2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_TMUX_LOG", log)
	t.Setenv("FAKE_TMUX_BUSY", busy)
	t.Setenv("TMUX", filepath.Join(tmp, "sock")+",1,0")
	t.Setenv("TMUX_PANE", "%5")
	dir := t.TempDir()
	argv := []string{"sh", "-c", `echo "$1" > "$0"`, out, "git log | cat"}
	if err := (&Server{}).runPopup("%5", argv, dir, editor.PopupOptions{Width: 90}); err != nil {
		t.Fatal(err)
	}
	// runPopup は、中のコマンドが起動したら終わりを待たずに戻るので、出力ができるまで待つ。
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(out)
		if err == nil && string(b) == "git log | cat\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ポップアップの中のコマンドの出力 = %q, %v", b, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var popups [][]string
	for _, c := range tmuxCalls(t, log) {
		if c[0] == "display-popup" {
			popups = append(popups, c)
		}
	}
	if len(popups) != 3 {
		t.Fatalf("display-popup の回数 = %d, want 3（開けない 2 回と開いた 1 回）", len(popups))
	}
	// TMUX_PANE は、ポップアップの中で起動した vi-imitation が下のペインの window の server に繋ぐためのもの（mux.InPane）。
	want := []string{"display-popup", "-E", "-c", "/dev/pts/9", "-t", "%5", "-w", "90%", "-h", "80%", "-d", dir, "-e", "TMUX_PANE=%5", "--"}
	if got := popups[2]; !slices.Equal(got[:len(want)], want) || !strings.HasSuffix(got[len(got)-1], " "+out+` 'git log | cat'`) ||
		!strings.Contains(got[len(got)-1], "dd bs=1 count=1") {
		t.Errorf("display-popup の引数 = %q\nwant %q と、コマンドの文字列", got, want)
	}
}

// herdr の中の :terminal.popup（Config.RunPopup）は、同梱のプラグインのポップアップ（plugin.pane.open の popup）を、渡した幅と既定の高さで、
// 渡した作業ディレクトリ（cwd）で開き、コマンドを、0 でない終了コードのときにキーを待つスクリプトで包んで渡す。
// ポップアップが既に開いているとき（ui_busy）は、部品の client と同じく popupBusyTimeout まで開き直し、続けば誤りを返す。
//
// req: FR-168
func TestRunPopupHerdr(t *testing.T) {
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
	var reqs []map[string]any
	busy := 2
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadBytes('\n')
			var req map[string]any
			_ = json.Unmarshal(line, &req)
			mu.Lock()
			reqs = append(reqs, req)
			resp := map[string]any{"id": req["id"], "result": map[string]any{"type": "ok"}}
			if busy != 0 {
				busy--
				resp = map[string]any{"id": req["id"], "error": map[string]any{"code": "ui_busy", "message": "a popup pane is already open"}}
			}
			mu.Unlock()
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
	dir := t.TempDir()
	if err = (&Server{}).runPopup("wW:p7", []string{"/bin/sh", "-c", "keifu-wt"}, dir, editor.PopupOptions{Width: 90}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := slices.Clone(reqs)
	mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("リクエストの数 = %d, want 3（ui_busy の 2 回と開いた 1 回）", len(got))
	}
	params, _ := got[2]["params"].(map[string]any)
	env, _ := params["env"].(map[string]any)
	cmd, _ := env[mux.HerdrCommandEnv].(string)
	// 中で起動した vi-imitation が下のペインのタブの server に繋ぐように、下のペインを別の名前で渡し、中で HERDR_PANE_ID に入れ直す（mux.InPane）。
	if env["VI_IMITATION_MUX_PANE"] != "wW:p7" || !strings.Contains(cmd, "export HERDR_PANE_ID") {
		t.Errorf("env = %v, コマンド = %q", env, cmd)
	}
	if params["placement"] != "popup" || params["width"] != "90%" || params["height"] != "80%" || params["cwd"] != dir {
		t.Errorf("params = %v", params)
	}
	if !strings.HasPrefix(cmd, "sh -c ") || !strings.HasSuffix(cmd, " sh /bin/sh -c keifu-wt") || !strings.Contains(cmd, "dd bs=1 count=1") {
		t.Errorf("コマンド = %q, want 0 でないときにキーを待つスクリプトで包んだもの", cmd)
	}

	// 開き直しても開けなければ、mux.ErrPopupBusy を返す。
	mu.Lock()
	busy = -1
	mu.Unlock()
	start := time.Now()
	err = (&Server{}).runPopup("wW:p7", []string{"/bin/sh", "-c", "true"}, dir, editor.PopupOptions{})
	if !errors.Is(err, mux.ErrPopupBusy) || time.Since(start) < popupBusyTimeout {
		t.Errorf("開けないときの err = %v（%v で戻った）, want popupBusyTimeout の後の mux.ErrPopupBusy", err, time.Since(start))
	}
}

// 端末多重化ソフトの外では、Config.RunPopup は mux.ErrUnsupported を返す（editor は、client のペインを知らなければ呼ばない）。
//
// req: FR-168
func TestRunPopupOutsideMux(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HC_TERMINAL_ID", "")
	if err := (&Server{}).runPopup("%5", []string{"true"}, "", editor.PopupOptions{}); !errors.Is(err, mux.ErrUnsupported) {
		t.Errorf("err = %v, want mux.ErrUnsupported", err)
	}
}
