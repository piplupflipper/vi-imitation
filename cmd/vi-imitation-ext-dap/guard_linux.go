//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// この節は、デバッグアダプターを見張るプロセス（見張り）である（#408 のレビューの必須 1）。
//
// 拡張機能は、アダプターを直に起動せず、自分の実行ファイルを guardArg を付けて起動し、その見張りにアダプターを起動させる。
// 見張りは、Linux の子の回収役（PR_SET_CHILD_SUBREAPER）になるので、アダプターが起動したデバッグ対象は、アダプターが先に終わっても
// 見張りの子として残る。見張りは次のときに、アダプターとその下のプロセス（子孫）をすべて止める。
//
//   - SIGTERM などを受けたとき。拡張機能が SIGKILL で終わったときも、見張りは Pdeathsig の SIGTERM を受ける（startAdapter）
//   - アダプターが終わったとき。残った子孫（アダプターが別のプロセスグループで起動し、置いていったデバッグ対象）を止める
//
// vscode-js-debug の dapDebugServer.js（node）は、SIGTERM の既定の動作で終わり、起動したデバッグ対象の node を残す
// （2026-11-25 のレビューで、拡張機能へ SIGKILL を送って確かめた）。dlv と debugpy は SIGTERM で自分のデバッグ対象を止めるが、
// どのアダプターでも残らないように、見張りで揃える。
// 止めるときは、先にアダプターへ SIGTERM を送り、guardGrace の間に自分で後始末をさせる（dlv はビルドした実行ファイルを消す）。
// その後に、残った子孫へ SIGKILL を送る。
// attach の相手（アダプターの子孫でないプロセス）は止めない。

// guardArg は、見張りとして起動するときの最初の引数である。続けて -- とアダプターのコマンドと引数を渡す。
const guardArg = "--vi-imitation-ext-dap-guard"

// guardGrace は、見張りがアダプターへ SIGTERM を送ってから、子孫へ SIGKILL を送るまでの猶予である（仮）。
// 拡張機能が見張りを強制終了する猶予（killGrace、shutdownKillGrace）より短くする。
const guardGrace = 500 * time.Millisecond

// prSetChildSubreaper は、prctl(2) の PR_SET_CHILD_SUBREAPER である。
const prSetChildSubreaper = 36

// guardAvailable は、アダプターを見張りの下で起動するかである。
const guardAvailable = true

// guardCommand は、アダプター p を見張りの下で起動するコマンドを作る。
// p.command は、ここで PATH から探すか、パスなら実行できるかを確かめる（見つからない誤りを、見張りを起動する前に返すため）。
// 相対パスは、アダプターの作業ディレクトリから解く（exec.Cmd の Dir と同じ）。
func guardCommand(p adapterProcess) (*exec.Cmd, error) {
	command := p.command
	if strings.ContainsRune(command, filepath.Separator) && !filepath.IsAbs(command) && p.cwd != "" {
		command = filepath.Join(p.cwd, command)
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return nil, err
	}
	if command, err = filepath.Abs(path); err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := append([]string{guardArg, "--", command}, p.args...)
	return exec.Command(self, args...), nil //nolint:gosec // 拡張機能の実行ファイルを見張りとして起動する
}

// maybeRunGuard は、引数が見張りの起動（guardArg）なら、見張りとして動いて終了する。そうでなければ何もしない。
// main と、テストの TestMain の最初に呼ぶ。
func maybeRunGuard() {
	if len(os.Args) >= 4 && os.Args[1] == guardArg && os.Args[2] == "--" {
		os.Exit(runGuard(os.Args[3:]))
	}
}

// runGuard は、argv のアダプターを起動して見張り、終了コードを返す。
func runGuard(argv []string) int {
	runtime.LockOSThread()
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		fmt.Fprintf(os.Stderr, "guard: PR_SET_CHILD_SUBREAPER: %v\n", errno)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // 利用者が設定したデバッグアダプターを起動する
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// アダプターは見張りと別のプロセスグループにし、見張りが SIGKILL で終わったら SIGTERM を受ける（startAdapter と同じ組）。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "guard: %v\n", err)
		return 127
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-sigs:
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(guardGrace):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	killDescendants(os.Getpid())
	code := 1
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		code = cmd.ProcessState.ExitCode()
	}
	return code
}

// killDescendants は、pid の子孫のプロセスへ SIGKILL を送り、見張りの子になったものを回収する。
// 子孫は /proc の親のプロセス ID からたどる。止めた後に子孫が新しいプロセスを起こしうるので、子孫が無くなるまで数回繰り返す。
func killDescendants(pid int) {
	for range 5 {
		ds := descendants(pid)
		if len(ds) == 0 {
			return
		}
		for _, d := range ds {
			_ = syscall.Kill(d, syscall.SIGKILL)
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			var ws syscall.WaitStatus
			p, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if err != nil || p == 0 {
				if len(descendants(pid)) == 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
}

// descendants は、/proc を読み、pid の子孫（ゾンビを除く）のプロセス ID を返す。
func descendants(pid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		// 2 つ目の欄（実行ファイルの名前）は括弧で囲まれ、空白を含みうるので、最後の ')' の後から読む。
		s := string(data)
		fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		if len(fields) < 2 || fields[0] == "Z" {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		children[ppid] = append(children[ppid], p)
	}
	var out []int
	queue := []int{pid}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			out = append(out, c)
			queue = append(queue, c)
		}
	}
	return out
}
