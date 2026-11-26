package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/piplupflipper/vi-imitation/internal/term/ptytest"
)

// envRunMain は、テストの実行ファイルを、エディタ本体として起動させる環境変数である。
// 偽の端末の結合のテストは、テストの実行ファイル自身を子プロセスとして起動し、main を動かす（go build を待たないため）。
const envRunMain = "VI_IMITATION_TEST_RUN_MAIN"

// envPanicOnStderr は、エディタとして起動したテストの実行ファイルで、拡張機能の標準エラー出力にこの値の行が来たら、
// それを記録するロガーの中で panic させる環境変数である。拡張機能の goroutine の panic で端末が戻ることを確かめるのに使う（#114）。
const envPanicOnStderr = "VI_IMITATION_TEST_PANIC_ON_STDERR"

// envFirstClientTimeout は、エディタとして起動したテストの実行ファイルで、firstClientTimeout を置き換える環境変数である（time.ParseDuration の形）。
const envFirstClientTimeout = "VI_IMITATION_TEST_FIRST_CLIENT_TIMEOUT"

func TestMain(m *testing.M) {
	if os.Getenv(envRunMain) == "1" {
		if d, err := time.ParseDuration(os.Getenv(envFirstClientTimeout)); err == nil {
			firstClientTimeout = d
		}
		if marker := os.Getenv(envPanicOnStderr); marker != "" {
			wrapLogHandler = func(h slog.Handler) slog.Handler { return panicHandler{Handler: h, marker: marker} }
		}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// muxFreeEnv は、テストを動かす環境から、tmux、herdr、herdr app の中であることを表す環境変数と VI_IMITATION_SOCKET を除いたものを返す。
// テストを tmux、herdr、herdr app の中で動かしても、起動したエディタが端末多重化ソフトの外の構成（FR-60）で動き、
// 利用者の tmux、herdr、herdr app に server の単位を問い合わせず、ラベルも書かないようにする。
func muxFreeEnv() []string {
	return withoutEnv(os.Environ(), "TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_SOCKET_PATH",
		"HC_TERMINAL_ID", "HC_TERMINAL_SOCKET", envSocket)
}

// panicHandler は、拡張機能の標準エラー出力の記録（exthost の "extension stderr"）の行が marker なら panic するロガーである。
// exthost は、標準エラー出力を、拡張機能ごとの goroutine で読んで記録する。
type panicHandler struct {
	slog.Handler
	marker string
}

func (h panicHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h panicHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "line" && a.Value.String() == h.marker {
			panic("テストが起こした panic: " + h.marker)
		}
		return true
	})
	return h.Handler.Handle(ctx, r)
}

// editorProc は、偽の端末の上で起動したエディタのプロセスである。
type editorProc struct {
	cmd    *exec.Cmd
	master *os.File
	tty    *os.File
	out    *ptytest.Capture
	done   chan error
}

// startEditor は、偽の端末（80 列 24 行）を制御端末にして、エディタを args で起動する。
// 利用者の設定と拡張機能を読まないように、XDG_CONFIG_HOME と XDG_DATA_HOME を空のディレクトリにする。
func startEditor(t *testing.T, args ...string) *editorProc {
	t.Helper()
	return startEditorWithConfig(t, "", args...)
}

// startEditorWithConfig は、設定ファイルの内容を configText にして、エディタを起動する。空なら設定ファイルを置かない。
func startEditorWithConfig(t *testing.T, configText string, args ...string) *editorProc {
	t.Helper()
	xdg := t.TempDir()
	if configText != "" {
		dir := filepath.Join(xdg, "config", "vi-imitation")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.jsonc"), []byte(configText), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return startEditorIn(t, xdg, nil, args...)
}

// startEditorIn は、XDG_CONFIG_HOME と XDG_DATA_HOME を xdg の下の config と data にし、環境変数 env を足して、エディタを起動する。
func startEditorIn(t *testing.T, xdg string, env []string, args ...string) *editorProc {
	t.Helper()
	return startEditorSized(t, 80, 24, xdg, env, args...)
}

// startEditorSized は、startEditorIn と同じく、ただし偽の端末を cols 列 rows 行にしてエディタを起動する。
func startEditorSized(t *testing.T, cols, rows uint16, xdg string, env []string, args ...string) *editorProc {
	t.Helper()
	return startEditorStdin(t, cols, rows, xdg, env, nil, args...)
}

// startEditorStdin は、startEditorSized と同じく、ただし標準入力を stdin にしてエディタを起動する。nil なら偽の端末にする。
// 偽の端末は制御端末なので、エディタは /dev/tty でそれを開ける（起動の引数の - の後にキーを読む端末。FR-158）。
func startEditorStdin(t *testing.T, cols, rows uint16, xdg string, env []string, stdin io.Reader, args ...string) *editorProc {
	t.Helper()
	master, tty := ptytest.Open(t, cols, rows)
	cmd := exec.Command(os.Args[0], args...) //nolint:gosec // テストの実行ファイル自身をエディタとして起動する
	cmd.Env = append(muxFreeEnv(), envRunMain+"=1",
		"XDG_CONFIG_HOME="+filepath.Join(xdg, "config"), "XDG_DATA_HOME="+filepath.Join(xdg, "data"))
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if stdin != nil {
		cmd.Stdin = stdin
	}
	// 新しいセッションを作り、tty を制御端末にする。端末の大きさを変えると、カーネルがエディタへ SIGWINCH を送る。
	// Ctty は子の fd の番号で、標準出力（1）はいつも tty である。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 1}
	p := &editorProc{cmd: cmd, master: master, tty: tty, out: ptytest.StartCapture(master), done: make(chan error, 1)}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill() // 失敗したテストの後始末
			if err := <-p.done; err != nil {
				t.Logf("後始末で止めたエディタ: %v", err)
			}
		}
	})
	return p
}

func (p *editorProc) write(t *testing.T, s string) {
	t.Helper()
	if _, err := p.master.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// wait は、エディタが終わるのを待ち、終了の誤り（終了コードが 0 なら nil）を返す。
func (p *editorProc) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-p.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("エディタが終わらない。出力: %q", p.out.String())
	}
	return nil
}

// checkRestored は、エディタの終了の後に、端末の設定が起動の前と同じで、代替画面から戻る列が出たことを確かめる。
func (p *editorProc) checkRestored(t *testing.T, before unix.Termios) {
	t.Helper()
	if after := ptytest.Termios(t, p.tty); after != before {
		t.Errorf("端末の設定が戻っていない:\n before %+v\n after  %+v", before, after)
	}
	// :! などで代替画面を出入りしたテストでは、前の "\x1b[?1049l" が既に出ているので、終わるときのものを待つ（PR #234 の再レビュー 3 の必須 3）。
	p.out.WaitFor(t, "最後に代替画面から戻る列", func(out string) bool {
		return strings.LastIndex(out, "\x1b[?1049l") > strings.LastIndex(out, "\x1b[?1049h")
	})
	out := p.out.String()
	if i, j := strings.LastIndex(out, "\x1b[?1049h"), strings.LastIndex(out, "\x1b[?1049l"); j < i {
		t.Errorf("代替画面から戻る列が、切り替える列より前にある: %q", out)
	}
	for _, seq := range []string{"\x1b[?2004l", "\x1b[?25h", "\x1b[?7h", "\x1b[0 q"} {
		if !strings.Contains(out, seq) {
			t.Errorf("出力に %q が無い", seq)
		}
	}
}

// TestEditInPTY は、偽の端末の上で、起動、文字の入力、:wq での保存と終了、終了の後に端末が元に戻ることを確かめる。
func TestEditInPTY(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	p := startEditor(t, path)
	before := ptytest.Termios(t, p.tty)
	p.out.WaitForString(t, "a.txt")

	p.write(t, "ihello あ")
	p.out.WaitForString(t, "INSERT")
	// ESC の単独は、続きを待つ時間（50 ms）の後に Esc になる（続けて送る形は TestEscThenKeyInPTY が確かめる）。
	p.write(t, "\x1b")
	p.out.WaitFor(t, "ノーマルモードの表示", func(out string) bool {
		return strings.Contains(out[strings.LastIndex(out, "INSERT"):], "NORMAL")
	})
	p.write(t, ":wq\r")
	if err := p.wait(t); err != nil {
		t.Fatalf("エディタの終了 = %v, want 正常終了。出力: %q", err, p.out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello あ\n" {
		t.Errorf("保存した内容 = %q, want %q", got, "hello あ\n")
	}
	p.checkRestored(t, before)
}

// TestEscThenKeyInPTY は、<Esc> の直後のキーが 1 回の読み取りで届いても、<Esc> とそのキーとして処理することを確かめる
// （Vim と Neovim と同じ。PR #111 のレビューの必須 1）。tmux は、escape-time の間に届いたキーを ESC とキーの組で送る。
func TestEscThenKeyInPTY(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("abc\ndef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := startEditor(t, path)
	before := ptytest.Termios(t, p.tty)
	p.out.WaitForString(t, "a.txt")
	// ix<Esc>j でノーマルモードへ戻って 2 行目へ動き、iZ<Esc>: で戻ってから :wq する。全体を 1 回で書く。
	p.write(t, "ix\x1bjiZ\x1b:wq\r")
	if err := p.wait(t); err != nil {
		t.Fatalf("エディタの終了 = %v。出力: %q", err, p.out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "xabc\nZdef\n" {
		t.Errorf("保存した内容 = %q, want %q", got, "xabc\nZdef\n")
	}
	p.checkRestored(t, before)
}

// TestSignalWhileLoadingInPTY は、大きいファイルを開いている途中に SIGTERM を受けても、端末を戻して終わることを確かめる
// （PR #111 のレビューの必須 2）。シグナルの受け付けは端末を開く前に始める。
func TestSignalWhileLoadingInPTY(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("0123456789 abcdefghij あいうえお\n", 1_000_000)), 0o600); err != nil {
		t.Fatal(err)
	}
	p := startEditor(t, path)
	before := ptytest.Termios(t, p.tty)
	// 端末を代替画面に切り替えた直後（ファイルを読む前か途中）に送る。
	p.out.WaitForString(t, "\x1b[?1049h")
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(t); err == nil {
		t.Error("シグナルで終わったのに終了コードが 0")
	}
	p.checkRestored(t, before)
	// 終了の理由は終わる前に書かれているが、Capture の読み取りが遅れることがあるので待つ（#116）。
	p.out.WaitForString(t, "シグナルを受けて終了した")
}

// TestSuspendResumeInPTY は、SIGTSTP で端末を戻してから止まり、SIGCONT で raw モードに入り直して全体を描き直すことを確かめる。
func TestSuspendResumeInPTY(t *testing.T) {
	p := startEditor(t)
	before := ptytest.Termios(t, p.tty)
	p.out.WaitForString(t, "[No Name]")
	if err := p.cmd.Process.Signal(syscall.SIGTSTP); err != nil {
		t.Fatal(err)
	}
	waitState(t, p.cmd.Process.Pid, 'T')
	if got := ptytest.Termios(t, p.tty); got != before {
		t.Errorf("止まっている間の端末の設定が戻っていない:\n before %+v\n got    %+v", before, got)
	}
	// エディタは代替画面から戻る列を書いてから止まるので、止まった時点で列は pty に書かれている。
	// ただし、テストの側が master から読む goroutine（Capture）は遅れることがあるので、読み終えるまで待つ。
	// 直ちに確かめていたときは、CPU に負荷をかけると 200 回に 1 回ほど、列が 5 ms ほど遅れて読まれて落ちた（#116）。
	// 止まっている間はエディタが書かないので、待って届いた列は止まる前に書いたものである。
	p.out.WaitForString(t, "\x1b[?1049l")
	mark := len(p.out.String())
	if err := p.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	p.out.WaitFor(t, "再開の後の描き直し", func(out string) bool {
		rest := out[mark:]
		return strings.Contains(rest, "\x1b[?1049h") && strings.Contains(rest, "\x1b[2J") && strings.Contains(rest, "[No Name]")
	})
	if raw := ptytest.Termios(t, p.tty); raw.Lflag&(unix.ICANON|unix.ECHO) != 0 {
		t.Errorf("再開の後に raw モードでない: lflag=%#x", raw.Lflag)
	}
	p.write(t, ":q\r")
	if err := p.wait(t); err != nil {
		t.Fatalf("エディタの終了 = %v", err)
	}
	p.checkRestored(t, before)
}

// waitState は、プロセス pid の状態（/proc/<pid>/stat の 3 つ目の欄）が state になるまで待つ。
func waitState(t *testing.T, pid int, state byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			t.Fatal(err)
		}
		// 2 つ目の欄（実行ファイルの名前）は括弧で囲まれ、空白を含みうるので、最後の ')' の後を読む。
		if i := strings.LastIndexByte(string(b), ')'); i >= 0 && i+2 < len(b) && b[i+2] == state {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("プロセスの状態が %c にならない: %s", state, b)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestConfigInPTY は、設定ファイルを読んで editor へ渡すことを確かめる。
// 誤りのある項目は起動後のメッセージで位置と内容を知らせ（基本設計 8 章）、他の項目（keymap、fixendofline）は使う。
func TestConfigInPTY(t *testing.T) {
	// 末尾に改行の無い既存のファイル。新しいファイルは、Vim の 'endofline' と同じく改行を付けて書くので使わない。
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := startEditorWithConfig(t, `{
  // コメントを書ける（JSONC）
  "keymap": { "normal": { "Q": "ihi<Esc>" } },
  "fixendofline": false,
  "unknownItem": 1,
}`, path)
	before := ptytest.Termios(t, p.tty)
	p.out.WaitForString(t, "config.jsonc:5:")
	p.write(t, "Q")
	p.out.WaitForString(t, "[+]")
	p.write(t, ":wq\r")
	if err := p.wait(t); err != nil {
		t.Fatalf("エディタの終了 = %v。出力: %q", err, p.out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hix" {
		t.Errorf("保存した内容 = %q, want %q（keymap と fixendofline: false）", got, "hix")
	}
	p.checkRestored(t, before)
}

// TestColorSchemeInPTY は、設定の colorscheme で同梱の iceberg を選ぶと iceberg の色で描き、
// 設定の colors に書いた名前だけがその上で置き換わることを確かめる。設定ファイルが無ければ 24 ビットの色を送らない（既定の 16 色のまま）。
//
// req: FR-125
func TestColorSchemeInPTY(t *testing.T) {
	p := startEditorWithConfig(t, `{
  "colorscheme": "iceberg",
  "colors": { "nontext": { "fg": "#444b71" } },
}`)
	before := ptytest.Termios(t, p.tty)
	p.out.WaitFor(t, "iceberg の色", func(out string) bool {
		return strings.Contains(out, "[No Name]") &&
			// iceberg の statusline（StatusLine。反転、#818596 の文字の色、#17171b の背景の色）
			strings.Contains(out, "7;38;2;129;133;150;48;2;23;23;27m") &&
			// iceberg の tabline.selected（TabLineSel。#9a9ca5 の文字、#161821 の背景）
			strings.Contains(out, "38;2;154;156;165;48;2;22;24;33m") &&
			// colors で置き換えた nontext（#444b71）
			strings.Contains(out, "38;2;68;75;113")
	})
	if out := p.out.String(); strings.Contains(out, "38;2;36;41;64") {
		t.Errorf("colors で置き換えた iceberg の nontext（#242940）を描いた: %q", out)
	}
	p.write(t, ":q\r")
	if err := p.wait(t); err != nil {
		t.Fatalf("エディタの終了 = %v。出力: %q", err, p.out.String())
	}
	p.checkRestored(t, before)

	p = startEditor(t)
	p.out.WaitForString(t, "[No Name]")
	p.write(t, ":q\r")
	if err := p.wait(t); err != nil {
		t.Fatalf("エディタの終了 = %v", err)
	}
	if out := p.out.String(); strings.Contains(out, "8;2;") {
		t.Errorf("設定ファイルが無いのに 24 ビットの色を送った: %q", out)
	}
}

// TestEditResizeInPTY は、端末の大きさを変えると、新しい大きさで画面を描き直すことを確かめる。
func TestEditResizeInPTY(t *testing.T) {
	p := startEditor(t)
	before := ptytest.Termios(t, p.tty)
	p.out.WaitForString(t, "[No Name]")
	ptytest.SetSize(t, p.master, 40, 10)
	// 大きさが変わると、画面を消して全体を書き直す。ステータス行は 9 行目（CUP の 9;1H）に来る。
	p.out.WaitFor(t, "10 行の画面の描き直し", func(out string) bool {
		i := strings.LastIndex(out, "\x1b[2J")
		return strings.Count(out, "\x1b[2J") >= 2 && strings.Contains(out[i:], "\x1b[9;1H")
	})
	p.write(t, ":q\r")
	if err := p.wait(t); err != nil {
		t.Fatalf("エディタの終了 = %v", err)
	}
	p.checkRestored(t, before)
}

// TestSignalRestoresTerminal は、SIGTERM と SIGHUP で終わるときも端末を元に戻すことを確かめる。
func TestSignalRestoresTerminal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			p := startEditor(t)
			before := ptytest.Termios(t, p.tty)
			p.out.WaitForString(t, "[No Name]")
			if err := p.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if err := p.wait(t); err == nil {
				t.Error("シグナルで終わったのに終了コードが 0")
			}
			p.checkRestored(t, before)
			p.out.WaitForString(t, "シグナルを受けて終了した") // Capture の読み取りの遅れを待つ（#116）
		})
	}
}
