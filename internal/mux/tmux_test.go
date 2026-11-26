package mux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// testTmux は、テスト用に tmux -L で立てた別の tmux のサーバーと、その最初のペインを指す Tmux である。
type testTmux struct {
	*Tmux
	session string
}

// newTestTmux は、テスト用の tmux のサーバーを立て、テストの終わりに kill-server で消す。
// tmux が無ければ skip する。利用者の tmux のサーバーには触らない。
func newTestTmux(t *testing.T) *testTmux {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux が無い")
	}
	name := fmt.Sprintf("vi-imitation-mux-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append([]string{"-L", name, "-f", "/dev/null"}, args...)...) //nolint:gosec // テスト用の tmux
		cmd.Env = tmuxFreeEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("new-session", "-d", "-s", "mux", "-x", "200", "-y", "50", "sleep 600")
	info := run("display-message", "-p", "-t", "mux", "#{socket_path}\t#{pane_id}")
	socket, pane, _ := strings.Cut(info, "\t")
	t.Cleanup(func() {
		cmd := exec.Command(bin, "-L", name, "kill-server") //nolint:gosec // テスト用の tmux
		cmd.Env = tmuxFreeEnv()
		_ = cmd.Run()
		// kill-server はソケットのファイルを残すことがあるので消す。
		_ = os.Remove(socket)
	})
	return &testTmux{Tmux: &Tmux{Bin: bin, Socket: socket, Pane: pane}, session: "mux"}
}

// tmuxFreeEnv は、利用者の tmux の環境変数を除いた環境を返す。
func tmuxFreeEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// query は、テスト用の tmux のサーバーで display-message -p を打った結果を返す。
func (tt *testTmux) query(t *testing.T, target, format string) string {
	t.Helper()
	out, err := tt.run(testCtx(t), "display-message", "-p", "-t", target, format)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// waitFile は、path のファイルが空でなくなるまで待ち、中身を返す。
func waitFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 && strings.HasSuffix(string(b), "\n") {
			return string(b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s ができない", path)
	return ""
}

// writeCommand は、環境変数 V と作業ディレクトリと自分のプロセス ID を out に書いてから眠るコマンドである。
func writeCommand(out string) []string {
	return []string{"sh", "-c", `printf '%s\n%s\n%s\n' "$V" "$(pwd)" "$$" > "$1.tmp" && mv "$1.tmp" "$1"; exec sleep 600`, "sh", out}
}

func TestTmuxDetectFromEnv(t *testing.T) {
	tt := newTestTmux(t)
	m := Detect(envOf(map[string]string{"TMUX": tt.Socket + ",1,0", "TMUX_PANE": tt.Pane}))
	if m.Kind() != KindTmux {
		t.Fatalf("Kind = %v, want tmux", m.Kind())
	}
	ctx := testCtx(t)
	pane, err := m.CurrentPane(ctx)
	if err != nil || pane != tt.Pane {
		t.Errorf("CurrentPane = %q, %v; want %q", pane, err, tt.Pane)
	}
	unit, err := m.Unit(ctx)
	want := tt.Socket + ":" + tt.query(t, tt.Pane, "#{window_id}")
	if err != nil || unit != want {
		t.Errorf("Unit = %q, %v; want %q", unit, err, want)
	}
}

// TestTmuxUnitStableInGroupedSessions は、グループにしたセッションを足したり消したりして
// 同じペインの session_id が変わっても、Unit が変わらないことを確かめる（PR #188 のレビュー）。
func TestTmuxUnitStableInGroupedSessions(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	before, err := tt.Unit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sessionBefore := tt.query(t, tt.Pane, "#{session_id}")
	if _, err = tt.run(ctx, "new-session", "-d", "-t", tt.session, "-s", "grouped"); err != nil {
		t.Fatal(err)
	}
	if _, err = tt.run(ctx, "kill-session", "-t", tt.session); err != nil {
		t.Fatal(err)
	}
	if s := tt.query(t, tt.Pane, "#{session_id}"); s == sessionBefore {
		t.Fatalf("session_id が変わらなかった（%s）。前提が崩れている", s)
	}
	after, err := tt.Unit(ctx)
	if err != nil || after != before {
		t.Errorf("Unit = %q, %v; want 変わらずに %q", after, err, before)
	}
}

// TestTmuxUnitFollowsPaneNotActiveWindow は、アクティブな window が別でも、TMUX_PANE のペインの window を返すことを確かめる（基本設計 1.2）。
func TestTmuxUnitFollowsPaneNotActiveWindow(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	if _, err := tt.run(ctx, "new-window", "-t", tt.session, "sleep 600"); err != nil {
		t.Fatal(err)
	}
	unit, err := tt.Unit(ctx)
	want := tt.Socket + ":" + tt.query(t, tt.Pane, "#{window_id}")
	if err != nil || unit != want {
		t.Errorf("Unit = %q, %v; want %q", unit, err, want)
	}
	if active := tt.query(t, tt.session, "#{window_id}"); strings.HasSuffix(unit, ":"+active) {
		t.Errorf("Unit がアクティブな window %s を返した", active)
	}
}

func TestTmuxSplitPane(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	out := filepath.Join(t.TempDir(), "out")
	dir := t.TempDir()
	pane, err := tt.SplitPane(ctx, SplitOptions{
		Target:    tt.Pane,
		Direction: Right,
		Size:      30,
		Command:   Command{Args: writeCommand(out), Env: map[string]string{"V": "it's $V"}, Dir: dir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pane, "%") || pane == tt.Pane {
		t.Fatalf("SplitPane = %q, want a new pane id", pane)
	}
	lines := strings.Split(waitFile(t, out), "\n")
	if lines[0] != "it's $V" {
		t.Errorf("環境変数 V = %q, want %q", lines[0], "it's $V")
	}
	if got, _ := filepath.EvalSymlinks(lines[1]); got != mustEval(t, dir) {
		t.Errorf("作業ディレクトリ = %q, want %q", lines[1], dir)
	}
	if got := tt.query(t, pane, "#{window_id}"); got != tt.query(t, tt.Pane, "#{window_id}") {
		t.Errorf("新しいペインの window = %s, want 元のペインと同じ", got)
	}
	// Focus が false なので、元のペインがアクティブのまま。
	if got := tt.query(t, tt.Pane, "#{pane_active}"); got != "1" {
		t.Errorf("元のペインの pane_active = %s, want 1", got)
	}
	// 右に開き、幅は window の幅のおよそ 30%。
	left, _ := strconv.Atoi(tt.query(t, pane, "#{pane_left}"))
	width, _ := strconv.Atoi(tt.query(t, pane, "#{pane_width}"))
	if left == 0 || width < 50 || width > 70 {
		t.Errorf("新しいペインの左端 = %d、幅 = %d, want 右に幅 60 前後", left, width)
	}

	if err = tt.ClosePane(ctx, pane); err != nil {
		t.Fatal(err)
	}
	panes, err := tt.run(ctx, "list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	for p := range strings.FieldsSeq(panes) {
		if p == pane {
			t.Errorf("ClosePane の後もペイン %s がある", pane)
		}
	}
}

func TestTmuxSplitPaneDownWithFocus(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	pane, err := tt.SplitPane(ctx, SplitOptions{Target: tt.Pane, Direction: Down, Focus: true, Command: Command{Args: []string{"sleep", "600"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := tt.query(t, pane, "#{pane_active}"); got != "1" {
		t.Errorf("新しいペインの pane_active = %s, want 1", got)
	}
	if top, _ := strconv.Atoi(tt.query(t, pane, "#{pane_top}")); top == 0 {
		t.Error("新しいペインが下に開かなかった")
	}
}

// TestTmuxSplitPaneLeft は、Left で元のペインの左に開き、Focus のとおり新しいペインがアクティブになることを確かめる（#437）。
func TestTmuxSplitPaneLeft(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	pane, err := tt.SplitPane(ctx, SplitOptions{Target: tt.Pane, Direction: Left, Focus: true, Command: Command{Args: []string{"sleep", "600"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := tt.query(t, pane, "#{pane_active}"); got != "1" {
		t.Errorf("新しいペインの pane_active = %s, want 1", got)
	}
	newLeft, _ := strconv.Atoi(tt.query(t, pane, "#{pane_left}"))
	oldLeft, _ := strconv.Atoi(tt.query(t, tt.Pane, "#{pane_left}"))
	newTop, _ := strconv.Atoi(tt.query(t, pane, "#{pane_top}"))
	oldTop, _ := strconv.Atoi(tt.query(t, tt.Pane, "#{pane_top}"))
	if newLeft != 0 || oldLeft == 0 || newTop != oldTop {
		t.Errorf("新しいペインの左端 = %d、元のペインの左端 = %d、上端 = %d と %d, want 新しいペインが左に並ぶ", newLeft, oldLeft, newTop, oldTop)
	}
}

func TestTmuxFocusPaneInAnotherWindow(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	// 別の window のペインを作り、元の window をアクティブのままにする。
	other, err := tt.run(ctx, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", tt.session, "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	other = strings.TrimSpace(other)
	if _, err := tt.run(ctx, "split-window", "-d", "-t", other, "sleep 600"); err != nil {
		t.Fatal(err)
	}
	if err := tt.FocusPane(ctx, tt.Pane, other); err != nil {
		t.Fatal(err)
	}
	if got := tt.query(t, other, "#{window_active}#{pane_active}"); got != "11" {
		t.Errorf("window_active と pane_active = %s, want 11", got)
	}
}

// TestTmuxFocusPaneInAnotherSession は、tmux の client が別のセッションを表示しているとき、
// FocusPane がその client をペインのセッションへ切り替えることを確かめる（FR-112 で動かした client。PR #188 のレビュー）。
func TestTmuxFocusPaneInAnotherSession(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	other, err := tt.run(ctx, "new-session", "-d", "-s", "other", "-P", "-F", "#{pane_id}", "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	other = strings.TrimSpace(other)
	if _, err = tt.run(ctx, "split-window", "-d", "-t", other, "sleep 600"); err != nil {
		t.Fatal(err)
	}
	client := tt.attachClient(t, tt.session)
	if err = tt.FocusPane(ctx, tt.Pane, other); err != nil {
		t.Fatal(err)
	}
	out, err := tt.run(ctx, "list-clients", "-F", "#{client_name}\t#{session_name}\t#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	if want := client + "\tother\t" + other; strings.TrimSpace(out) != want {
		t.Errorf("list-clients = %q, want %q", strings.TrimSpace(out), want)
	}
}

// TestTmuxFocusPaneSameWindow は、client が表示している window の中のペインへ移ることを確かめる。
func TestTmuxFocusPaneSameWindow(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	other, err := tt.run(ctx, "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", tt.Pane, "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	other = strings.TrimSpace(other)
	tt.attachClient(t, tt.session)
	if err = tt.FocusPane(ctx, tt.Pane, other); err != nil {
		t.Fatal(err)
	}
	if got := tt.query(t, other, "#{pane_active}"); got != "1" {
		t.Errorf("pane_active = %s, want 1", got)
	}
}

// TestTmuxFocusPaneSwitchesFromClient は、tmux の client が 2 つあるとき、最後に操作された方ではなく、
// from のペインを表示している方を切り替えることを確かめる（PR #188 の再レビュー。client_activity は秒の単位）。
func TestTmuxFocusPaneSwitchesFromClient(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	target, err := tt.run(ctx, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", tt.session, "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	target = strings.TrimSpace(target)
	if _, err = tt.run(ctx, "new-session", "-d", "-s", "other", "sleep 600"); err != nil {
		t.Fatal(err)
	}
	mine := tt.attachClient(t, tt.session)
	// 後から繋いだ方が、最後に操作された client になる（同じ秒なら list-clients の順による）。
	others := tt.attachClient(t, "other")
	if err = tt.FocusPane(ctx, tt.Pane, target); err != nil {
		t.Fatal(err)
	}
	out, err := tt.run(ctx, "list-clients", "-F", "#{client_name}\t#{session_name}\t#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		name, rest, _ := strings.Cut(line, "\t")
		got[name] = rest
	}
	if want := "mux\t" + target; got[mine] != want {
		t.Errorf("from の client %s = %q, want %q", mine, got[mine], want)
	}
	if !strings.HasPrefix(got[others], "other\t") {
		t.Errorf("別の client %s = %q, want other のまま", others, got[others])
	}
}

func TestTmuxFocusPaneUnknown(t *testing.T) {
	tt := newTestTmux(t)
	if err := tt.FocusPane(testCtx(t), tt.Pane, "%999"); err == nil {
		t.Error("無いペインへ移って誤りにならなかった")
	}
	if err := tt.FocusPane(testCtx(t), "", tt.Pane); !errors.Is(err, ErrNoTarget) {
		t.Errorf("from が空: err = %v, want ErrNoTarget", err)
	}
}

// TestTmuxPopupContextCanceled は、開いたか分かる前に ctx が終わっても、ポップアップを開いたまま残さないことを確かめる。
// display-popup を差し止める包みのスクリプトを Bin にして、display-popup が起動してから ctx を切り、
// OpenPopup が ctx の誤りを返した後にポップアップを開かせる（PR #188 の再レビュー）。
//
// 時間の上限で順序を作らない。ctx を時間で切ると、負荷で client の問い合わせが上限を超えることがある。
// また、層は印ができた時点で閉じるので、中のコマンドが何かを書く前に閉じることがあり、中のコマンドの出力は待てない（#313）。
// display-popup はポップアップが閉じるまで戻らないので、包みのスクリプトが display-popup の終わりを書けば、閉じたことになる。
func TestTmuxPopupContextCanceled(t *testing.T) {
	tt := newTestTmux(t)
	tt.attachClient(t, tt.session)
	dir := t.TempDir()
	// OpenPopup の印のディレクトリ（os.MkdirTemp）を、このテストのディレクトリの下に作らせる。
	popupTmp := filepath.Join(dir, "tmp")
	if err := os.Mkdir(popupTmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", popupTmp)
	started := filepath.Join(dir, "started")
	goAhead := filepath.Join(dir, "go")
	closed := filepath.Join(dir, "closed")
	slow := filepath.Join(dir, "slow-tmux")
	// display-popup だけを、started を作ってから go ができるまで止める。
	// display-popup が戻ったら、そのときに印があったか（ポップアップが開いたか）を closed に書く。
	// 印のディレクトリは、この包みが終わった後に層が消す。
	script := "#!/bin/sh\n" +
		"case \" $* \" in *' display-popup -E '*)\n" +
		"\t: > " + shellQuote(started) + "\n" +
		"\twhile [ ! -e " + shellQuote(goAhead) + " ]; do sleep 0.01; done\n" +
		"\t" + shellQuote(tt.Bin) + " \"$@\"\n" +
		"\tstatus=$?\n" +
		"\tset -- " + shellQuote(popupTmp) + "/vi-imitation-popup-*/ready\n" +
		"\tif [ -e \"$1\" ]; then echo opened; else echo not-opened; fi > " + shellQuote(closed+".tmp") + "\n" +
		"\tmv " + shellQuote(closed+".tmp") + " " + shellQuote(closed) + "\n" +
		"\texit $status;;\n" +
		"esac\n" +
		"exec " + shellQuote(tt.Bin) + " \"$@\"\n"
	if err := os.WriteFile(slow, []byte(script), 0o700); err != nil { //nolint:gosec // テスト用の実行ファイル
		t.Fatal(err)
	}
	slowTmux := &Tmux{Bin: slow, Socket: tt.Socket, Pane: tt.Pane}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// display-popup が起動してから ctx を切る。
		for ctx.Err() == nil {
			if _, err := os.Stat(started); err == nil {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	err := slowTmux.OpenPopup(ctx, PopupOptions{Target: tt.Pane, Command: Command{Args: []string{"sleep", "600"}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// ctx の誤りが返った後に、ポップアップを開かせる。層はそれを見つけて閉じる。
	if err = os.WriteFile(goAhead, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := waitFile(t, closed); got != "opened\n" {
		t.Fatalf("display-popup が戻ったときの印 = %q, want ポップアップが開いていた", got)
	}
	// 印のディレクトリは、display-popup が終わった後に層が消す。
	deadline := time.Now().Add(5 * time.Second)
	for {
		left, _ := filepath.Glob(filepath.Join(popupTmp, "vi-imitation-popup-*"))
		if len(left) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("印のディレクトリが残っている: %v", left)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTmuxPopupClientInGroupedSession は、グループにしたセッションで、元のペインの window を表示している
// tmux の client を選ぶことを確かめる（PR #188 のレビュー）。
func TestTmuxPopupClientInGroupedSession(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	if _, err := tt.run(ctx, "new-window", "-d", "-t", tt.session, "sleep 600"); err != nil {
		t.Fatal(err)
	}
	if _, err := tt.run(ctx, "new-session", "-d", "-t", tt.session, "-s", "grouped"); err != nil {
		t.Fatal(err)
	}
	// grouped の client は別の window を表示させる。
	if _, err := tt.run(ctx, "select-window", "-t", "grouped:1"); err != nil {
		t.Fatal(err)
	}
	showing := tt.attachClient(t, tt.session)
	tt.attachClient(t, "grouped")
	got, err := tt.client(ctx, tt.Pane)
	if err != nil {
		t.Fatal(err)
	}
	if got.name != showing {
		t.Errorf("client = %s, want 元のペインの window を表示している %s", got.name, showing)
	}
}

func TestTmuxEmptyTarget(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	cmd := Command{Args: []string{"true"}}
	if _, err := tt.SplitPane(ctx, SplitOptions{Command: cmd}); !errors.Is(err, ErrNoTarget) {
		t.Errorf("SplitPane: err = %v, want ErrNoTarget", err)
	}
	if err := tt.OpenPopup(ctx, PopupOptions{Command: cmd}); !errors.Is(err, ErrNoTarget) {
		t.Errorf("OpenPopup: err = %v, want ErrNoTarget", err)
	}
}

func TestTmuxClosePaneUnknown(t *testing.T) {
	tt := newTestTmux(t)
	if err := tt.ClosePane(testCtx(t), "%999"); err == nil {
		t.Error("無いペインを閉じて誤りにならなかった")
	}
}

func TestTmuxPopupWithoutClient(t *testing.T) {
	tt := newTestTmux(t)
	err := tt.OpenPopup(testCtx(t), PopupOptions{Target: tt.Pane, Command: Command{Args: []string{"true"}}})
	if err == nil || !strings.Contains(err.Error(), "no client") {
		t.Errorf("tmux の client が無いのに err = %v", err)
	}
}

// clientNames は、テスト用の tmux のサーバーの client の名前の集合を返す。
func (tt *testTmux) clientNames(t *testing.T) map[string]bool {
	t.Helper()
	out, err := tt.run(testCtx(t), "list-clients", "-F", "#{client_name}")
	if err != nil {
		return nil
	}
	names := map[string]bool{}
	for n := range strings.FieldsSeq(out) {
		names[n] = true
	}
	return names
}

// attachClient は、script(1) で疑似端末を作り、その中でテスト用の tmux のサーバーの session へ client を繋ぎ、
// その client の名前を返す。script が無ければ skip する。
func (tt *testTmux) attachClient(t *testing.T, session string) string {
	t.Helper()
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script が無い")
	}
	before := tt.clientNames(t)
	attach := shellJoin([]string{tt.Bin, "-S", tt.Socket, "attach", "-t", session})
	cmd := exec.Command(script, "-qfec", attach, "/dev/null") //nolint:gosec // テスト用の tmux の client
	cmd.Env = append(tmuxFreeEnv(), "TERM=xterm-256color")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for n := range tt.clientNames(t) {
			if !before[n] {
				return n
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Skip("script の中の tmux の client が繋がらない")
	return ""
}

// TestTmuxPaneFocused は、PaneFocused が、tmux の client の表示している window のアクティブなペインだけを true にすることを確かめる（#244）。
// tmux の client が無ければ、どのペインも false である。
func TestTmuxPaneFocused(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	other, err := tt.run(ctx, "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", tt.Pane, "sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	other = strings.TrimSpace(other)
	if got, err := tt.PaneFocused(ctx, tt.Pane); err != nil || got {
		t.Errorf("PaneFocused without a client = %v, %v; want false", got, err)
	}
	tt.attachClient(t, tt.session)
	if got, err := tt.PaneFocused(ctx, tt.Pane); err != nil || !got {
		t.Errorf("PaneFocused(active pane) = %v, %v; want true", got, err)
	}
	if got, err := tt.PaneFocused(ctx, other); err != nil || got {
		t.Errorf("PaneFocused(inactive pane) = %v, %v; want false", got, err)
	}
	// 別の window へ移ったら、元の window のアクティブなペインも false になる。
	if _, err := tt.run(ctx, "new-window", "-t", tt.session, "sleep 600"); err != nil {
		t.Fatal(err)
	}
	if got, err := tt.PaneFocused(ctx, tt.Pane); err != nil || got {
		t.Errorf("PaneFocused after switching windows = %v, %v; want false", got, err)
	}
	if _, err := tt.PaneFocused(ctx, "%999"); !errors.Is(err, errNoPane) {
		t.Errorf("PaneFocused(unknown) = %v, want errNoPane", err)
	}
}

// TestTmuxPopupBusy は、ポップアップが開いている間の 2 つ目の OpenPopup が ErrPopupBusy を返し、
// 中のコマンドを起動しないことを確かめる（PR #188 のレビュー）。
func TestTmuxPopupBusy(t *testing.T) {
	tt := newTestTmux(t)
	tt.attachClient(t, tt.session)
	ctx := testCtx(t)
	first := filepath.Join(t.TempDir(), "first")
	if err := tt.OpenPopup(ctx, PopupOptions{Target: tt.Pane, Command: Command{Args: writeCommand(first)}}); err != nil {
		t.Fatal(err)
	}
	waitFile(t, first)
	second := filepath.Join(t.TempDir(), "second")
	err := tt.OpenPopup(ctx, PopupOptions{Target: tt.Pane, Command: Command{Args: writeCommand(second)}})
	if !errors.Is(err, ErrPopupBusy) {
		t.Errorf("2 つ目の OpenPopup: err = %v, want ErrPopupBusy", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, statErr := os.Stat(second); statErr == nil {
		t.Error("2 つ目のポップアップのコマンドが起動した")
	}
	if err = tt.ClosePopup(ctx, tt.Pane); err != nil {
		t.Fatal(err)
	}
}

// TestTmuxPopupQuickCommand は、中のコマンドがすぐ終わっても ErrPopupBusy と取り違えないことを確かめる。
func TestTmuxPopupQuickCommand(t *testing.T) {
	tt := newTestTmux(t)
	tt.attachClient(t, tt.session)
	ctx := testCtx(t)
	for range 5 {
		if err := tt.OpenPopup(ctx, PopupOptions{Target: tt.Pane, Command: Command{Args: []string{"true"}}}); err != nil {
			t.Fatalf("すぐ終わるコマンドで err = %v", err)
		}
	}
}

// TestTmuxClosePopupOnlyOwn は、この層が開いていないポップアップを ClosePopup が閉じないことを確かめる。
func TestTmuxClosePopupOnlyOwn(t *testing.T) {
	tt := newTestTmux(t)
	client := tt.attachClient(t, tt.session)
	ctx := testCtx(t)
	// 利用者が開いたポップアップの代わりに、tmux のコマンドで直接開く。
	out := filepath.Join(t.TempDir(), "other")
	other := shellJoin(writeCommand(out))
	cmd := exec.Command(tt.Bin, "-S", tt.Socket, "display-popup", "-E", "-c", client, other) //nolint:gosec // テスト用の tmux
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = tt.run(testCtx(t), "display-popup", "-C", "-c", client)
		_ = cmd.Wait()
	})
	pid, err := strconv.Atoi(strings.Split(waitFile(t, out), "\n")[2])
	if err != nil {
		t.Fatal(err)
	}
	if err = tt.ClosePopup(ctx, tt.Pane); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if syscall.Kill(pid, 0) != nil {
		t.Error("この層が開いていないポップアップを閉じた")
	}
}

// TestTmuxPopupKeepOnFailure は、KeepOnFailure のポップアップ（keepScript で包む）が、コマンドが 0 でない終了コードで終わったときだけ
// キーを待って開いたまま残り、0 で終わったときと、KeepOnFailure でないポップアップは閉じることを確かめる（FR-168）。
// 開いているかは、層が開いた display-popup のプロセスが動いているか（tmuxPopup.exited）で見る（display-popup はポップアップが閉じるまで戻らない）。
//
// req: FR-168
func TestTmuxPopupKeepOnFailure(t *testing.T) {
	tt := newTestTmux(t)
	tt.attachClient(t, tt.session)
	ctx := testCtx(t)
	for _, tc := range []struct {
		name     string
		keep     bool
		status   int
		wantOpen bool
	}{
		{"keep の 0 は閉じる", true, 0, false},
		{"keep の 3 は残す", true, 3, true},
		{"keep でない 3 は閉じる", false, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out")
			// 層が開いたと見て表に入れるまで（印のファイルを見張る間隔）、すぐには終わらない。
			script := fmt.Sprintf(`echo done > "$1"; sleep 0.3; exit %d`, tc.status)
			err := tt.OpenPopup(ctx, PopupOptions{
				Target: tt.Pane, KeepOnFailure: tc.keep,
				Command: Command{Args: []string{"sh", "-c", script, "sh", out}},
			})
			if err != nil {
				t.Fatal(err)
			}
			waitFile(t, out)
			tt.mu.Lock()
			p := tt.popups[tt.Pane]
			tt.mu.Unlock()
			if p == nil {
				t.Fatal("開いたポップアップが表に無い")
			}
			select {
			case <-p.exited:
				if tc.wantOpen {
					t.Error("終了コードが 0 でないのに、ポップアップが閉じた")
				}
			case <-time.After(2 * time.Second):
				if !tc.wantOpen {
					t.Error("コマンドが終わった後も、ポップアップが開いている")
				}
			}
			if err := tt.ClosePopup(ctx, tt.Pane); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTmuxPopup(t *testing.T) {
	tt := newTestTmux(t)
	tt.attachClient(t, tt.session)
	ctx := testCtx(t)
	out := filepath.Join(t.TempDir(), "out")
	dir := t.TempDir()
	err := tt.OpenPopup(ctx, PopupOptions{
		Target: tt.Pane, Width: 50, Height: 40,
		Command: Command{Args: writeCommand(out), Env: map[string]string{"V": "popup value"}, Dir: dir},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(waitFile(t, out), "\n")
	if lines[0] != "popup value" {
		t.Errorf("環境変数 V = %q, want %q", lines[0], "popup value")
	}
	if got, _ := filepath.EvalSymlinks(lines[1]); got != mustEval(t, dir) {
		t.Errorf("作業ディレクトリ = %q, want %q", lines[1], dir)
	}
	pid, err := strconv.Atoi(lines[2])
	if err != nil {
		t.Fatal(err)
	}
	if err = tt.ClosePopup(ctx, tt.Pane); err != nil {
		t.Fatal(err)
	}
	// ポップアップを閉じると、中のコマンドも終わる。
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("ClosePopup の後もポップアップの中のプロセス %d が残っている", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ScreenWidth は、ポップアップを出す tmux の client の端末の列数（client_width）を返す。ペインの幅ではない（FR-149）。
//
// req: FR-149
func TestTmuxScreenWidth(t *testing.T) {
	tt := newTestTmux(t)
	ctx := testCtx(t)
	if _, err := tt.ScreenWidth(ctx, ""); !errors.Is(err, ErrNoTarget) {
		t.Errorf("ScreenWidth(\"\") = %v, want ErrNoTarget", err)
	}
	if _, err := tt.ScreenWidth(ctx, tt.Pane); err == nil {
		t.Error("ScreenWidth without a client succeeded")
	}
	name := tt.attachClient(t, tt.session)
	out, err := tt.run(ctx, "display-message", "-p", "-c", name, "#{client_width}")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := strconv.Atoi(strings.TrimSpace(out))
	got, err := tt.ScreenWidth(ctx, tt.Pane)
	if err != nil || got != want || got <= 0 {
		t.Errorf("ScreenWidth = %d, %v, want %d", got, err, want)
	}
	var _ ScreenWidther = tt.Tmux
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
