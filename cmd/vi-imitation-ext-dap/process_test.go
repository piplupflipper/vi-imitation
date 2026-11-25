//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// gone は、pid のプロセスが無いか、終わってゾンビになっていることを返す。
func gone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	// 3 つ目の欄が状態である。2 つ目の欄（実行ファイルの名前）は括弧で囲まれ、空白を含みうるので、最後の ')' の後から読む。
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) > 0 && fields[0] == "Z"
}

// waitGone は、pid のプロセスが終わるまで待つ。
func waitGone(t *testing.T, what string, pid int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if gone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("%s (pid %d) remains", what, pid)
}

// waitPID は、"<prefix> <pid>\n" の出力から pid を読む。
func (h *host) waitPID(prefix string) int {
	h.t.Helper()
	seen := h.waitOutput(prefix + " ")
	var pid int
	for _, s := range seen {
		if i := strings.Index(s, prefix+" "); i >= 0 {
			if _, err := fmt.Sscanf(s[i:], prefix+" %d", &pid); err == nil {
				return pid
			}
		}
	}
	h.t.Fatalf("pid of %s not found in %q", prefix, seen)
	return 0
}

// TestKillTerminatesDebuggee は、session.kill がデバッグアダプターに先に SIGTERM を送り、
// デバッグアダプターが別のプロセスグループで起動したデバッグ対象を終わらせられることを確かめる（PR #58 のレビューの必須 1）。
//
// req: FR-31
func TestKillTerminatesDebuggee(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"child": true}))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	child := h.waitPID("child")
	h.waitSigns(bp2, exec2)
	h.d.mu.Lock()
	s := h.d.sess
	h.d.mu.Unlock()
	s.kill()
	waitGone(t, "debuggee", child)
	if ws, ok := s.getProc().cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || ws.Signaled() {
		t.Errorf("adapter exit = %v, want exit by itself on SIGTERM", s.getProc().cmd.ProcessState)
	}
}

// TestExtensionKilled は、拡張機能のプロセスが SIGKILL で終わったとき（本体の強制終了。internal/exthost の process.kill）、
// デバッグアダプターが SIGTERM を受け取り、デバッグ対象と一緒に終わることを確かめる（PR #58 のレビューの必須 1）。
//
// req: FR-31
func TestExtensionKilled(t *testing.T) {
	h := newHostState(t)
	cmd := exec.Command(extBinary)
	// 本体と同じく、拡張機能を別のプロセスグループで起動する（internal/exthost の startProcess）。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h.conn = rpc.NewConn(stdout, stdin, rpc.HandlerFuncs{Notification: h.notification, Request: h.request})
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = h.conn.Close()
		_ = cmd.Wait()
	})
	h.initialize(fakeSettings(t, nil, map[string]any{"child": true, "pid": true}))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	adapter := h.waitPID("adapter")
	child := h.waitPID("child")
	h.waitSigns(bp2, exec2)

	// 本体の強制終了と同じく、拡張機能のプロセスグループへ SIGKILL を送る。
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitGone(t, "adapter", adapter)
	waitGone(t, "debuggee", child)
}

// TestExtensionKilledOrphaningAdapter は、SIGTERM で子を残して終わるアダプター（vscode-js-debug の dapDebugServer.js の代役）でも、
// 拡張機能が SIGKILL で終わったら、見張り（guard_linux.go）がアダプターとデバッグ対象を止めることを確かめる（#408 のレビューの必須 1）。
//
// req: FR-31
func TestExtensionKilledOrphaningAdapter(t *testing.T) {
	h := newHostState(t)
	cmd := exec.Command(extBinary)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = h.conn.Close()
		_ = cmd.Wait()
	})
	h.conn = rpc.NewConn(stdout, stdin, rpc.HandlerFuncs{Notification: h.notification, Request: h.request})
	h.initialize(fakeSettings(t, nil, map[string]any{"child": true, "orphanOnTerm": true}))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	adapter := h.waitPID("adapter")
	child := h.waitPID("child")
	killAtCleanup(t, child)
	h.waitSigns(bp2, exec2)

	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitGone(t, "adapter", adapter)
	waitGone(t, "debuggee", child)
}

// sessionOf は、pid のプロセスのセッションの ID と制御端末（tty_nr。無ければ 0）を /proc/<pid>/stat から読む。
func sessionOf(t *testing.T, pid int) (sid, tty int) {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	// ')' の後の欄は、状態、親、プロセスグループ、セッション、制御端末の順である。
	fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if _, err := fmt.Sscan(fields[3], &sid); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscan(fields[4], &tty); err != nil {
		t.Fatal(err)
	}
	return sid, tty
}

// TestAdapterOwnSession は、アダプターとデバッグ対象を、拡張機能と別の、制御端末を持たないセッションで動かすことを確かめる
// （PR #410 の再レビューの必須 A。端末多重化ソフトの外で、debugpy のランチャーが本体の端末の前面を取らないため）。
//
// req: FR-31
func TestAdapterOwnSession(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"child": true}))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	adapter := h.waitPID("adapter")
	child := h.waitPID("child")
	h.waitSigns(bp2, exec2)
	mySID, _ := sessionOf(t, os.Getpid())
	for name, pid := range map[string]int{"adapter": adapter, "debuggee": child} {
		sid, tty := sessionOf(t, pid)
		if sid == mySID || tty != 0 {
			t.Errorf("%s: session %d (extension %d), tty %d; want another session without a controlling terminal", name, sid, mySID, tty)
		}
	}
}

// hostShutdownTimeout は、本体が shutdown を送ってから、拡張機能のプロセスグループへ SIGTERM を送るまでの期限である
// （internal/exthost の DefaultShutdownTimeout。基本設計 5.2。値は仮）。
const hostShutdownTimeout = 3 * time.Second

// shutdown は、本体の役から shutdown を送り、拡張機能が接続を閉じる（Serve が戻る）までの時間を返す。
func (h *host) shutdown() time.Duration {
	h.t.Helper()
	start := time.Now()
	if err := h.conn.Notify(ext.MethodShutdown, ext.ShutdownParams{}); err != nil {
		h.t.Fatalf("shutdown: %v", err)
	}
	select {
	case <-h.conn.Done():
	case <-time.After(testTimeout):
		h.t.Fatal("the extension did not close the connection after shutdown")
	}
	return time.Since(start)
}

// startDebugging は、デバッグを始めて最初のブレークポイントで止まるまで進め、セッションとデバッグ対象の pid を返す。
func (h *host) startDebugging() (*session, int) {
	h.t.Helper()
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	child := h.waitPID("child")
	h.waitSigns(bp2, exec2)
	h.d.mu.Lock()
	s := h.d.sess
	h.d.mu.Unlock()
	return s, child
}

// TestShutdownStopsSession は、本体から shutdown を受け取ると、デバッグ中のデバッグアダプターへ terminate と disconnect を送り、
// デバッグアダプターとデバッグ対象が終わってから接続を閉じること、それが本体の期限の中で終わることを確かめる（基本設計 5.2 の正常な終了）。
//
// req: FR-31
func TestShutdownStopsSession(t *testing.T) {
	h := newHost(t, fakeSettings(t, []string{"-terminate"}, map[string]any{"child": true}))
	s, child := h.startDebugging()
	if d := h.shutdown(); d >= hostShutdownTimeout {
		t.Errorf("shutdown took %s, want < %s", d, hostShutdownTimeout)
	}
	// 接続を閉じた時点で、後始末は終わっている。待たずに確かめる。
	select {
	case <-s.ended:
	default:
		t.Fatal("the session has not ended when the connection is closed")
	}
	if !s.getProc().cmd.ProcessState.Success() {
		t.Errorf("adapter exit = %v, want success (exited by disconnect, not killed)", s.getProc().cmd.ProcessState)
	}
	if !gone(child) {
		t.Errorf("debuggee (pid %d) remains", child)
	}
	h.waitOutput("terminate requested")
	h.waitMessage("デバッグを終了した")
}

// TestShutdownHungAdapter は、デバッグアダプターが terminate と disconnect に応答しないとき、shutdownTimeout で打ち切って
// 強制終了し、本体の期限（SIGTERM を送るまで）の中で接続を閉じること、デバッグ対象が残らないことを確かめる。
//
// req: FR-31
func TestShutdownHungAdapter(t *testing.T) {
	h := newHost(t, fakeSettings(t, []string{"-terminate"}, map[string]any{"child": true, "ignore": []string{"terminate", "disconnect"}}))
	s, child := h.startDebugging()
	d := h.shutdown()
	if d < shutdownTimeout || d >= hostShutdownTimeout {
		t.Errorf("shutdown took %s, want in [%s, %s)", d, shutdownTimeout, hostShutdownTimeout)
	}
	select {
	case <-s.procDone():
	default:
		t.Fatal("the adapter remains after the connection is closed")
	}
	waitGone(t, "debuggee", child)
}
