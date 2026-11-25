//go:build linux

package main

import (
	"os/exec"
	"runtime"
	"sync"
	"syscall"
)

// startAdapter は、デバッグアダプターの子プロセスを起動する。
//
// 子プロセスは別のプロセスグループにし（下の Setsid）、拡張機能が終わったら SIGTERM を受け取るようにする（Pdeathsig）。
// 本体は、拡張機能を終わらせるときに、拡張機能のプロセスグループへすぐに SIGKILL を送る（internal/exthost の process.kill）。
// 同じプロセスグループのままだと、dlv も SIGKILL で死に、dlv が別のプロセスグループで起動したデバッグ対象が孤児で残る
// （PR #58 のレビューで、dlv 1.27.2 で確かめた）。dlv は SIGTERM を受け取ると、デバッグ対象を終わらせて、ビルドした実行ファイルも消す。
// 別のプロセスグループにするだけだと本体の SIGKILL が届かなくなるので、Pdeathsig と必ず組にする。
//
// Pdeathsig は、子プロセスを起動したスレッドが終わったときにも送られる（golang/go#27505）。
// このため、起動は、ロックしたまま終わらない 1 つのスレッド（spawnThread）の上で行う。
//
// 別のプロセスグループに加えて、制御端末を持たない新しいセッションにする（Setsid。プロセスグループも新しくなる）。
// 端末多重化ソフトの外では、拡張機能は本体と同じセッションにいて、本体の端末を制御端末に持つ。
// 同じセッションのままだと、debugpy 1.8.22 のランチャー（launcher/debuggee.py）が /dev/tty を開いて os.tcsetpgrp で
// デバッグ対象を端末の前面のプロセスグループにし、本体が端末の前面から外れて、次のキーで止められた（PR #410 の再レビューの必須 A）。
// 見張り（guard_linux.go）とその下のアダプター、デバッグ対象は、このセッションの中にいるので、本体の端末を開けない。
// resolveCommand の外部のコマンドも同じ形で起動する。
func startAdapter(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGTERM}
	return onSpawnThread(cmd.Start)
}

// killGroup は、startAdapter で起動した cmd のプロセスグループへ SIGKILL を送る（resolveCommand の取り消しと期限。FR-282）。
// コマンドが起こした子プロセスも同じプロセスグループにいるので、一緒に止まる。
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

var (
	spawnOnce sync.Once
	spawnReqs chan func()
)

// onSpawnThread は、f を、起動のためのスレッドの上で呼び、戻り値を返す。
func onSpawnThread(f func() error) error {
	spawnOnce.Do(func() {
		spawnReqs = make(chan func())
		go func() {
			// このスレッドは、拡張機能が終わるまで終わらせない（UnlockOSThread しない）。
			runtime.LockOSThread()
			for req := range spawnReqs {
				req()
			}
		}()
	})
	done := make(chan error, 1)
	spawnReqs <- func() { done <- f() }
	return <-done
}
