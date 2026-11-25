//go:build linux || darwin

package project

import "syscall"

// xOK は、access(2) の X_OK である。
const xOK = 0x1

// canExecute は、今の利用者が path を実行できるかを access(2) で返す（nvim の executable() と同じ見方）。
// 持ち主の x のビットだけが立つ root の持ち物（0o700）は、root でない利用者には実行できない。
func canExecute(path string) bool {
	return syscall.Access(path, xOK) == nil
}
