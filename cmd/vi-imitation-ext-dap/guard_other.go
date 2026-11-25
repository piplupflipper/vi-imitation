//go:build !linux

package main

import (
	"errors"
	"os/exec"
)

// guardAvailable は、アダプターを見張りの下で起動するかである。Linux 以外では子の回収役（PR_SET_CHILD_SUBREAPER）が無いので、
// 見張りを使わず、アダプターを直に起動する（guard_linux.go の doc）。
const guardAvailable = false

// guardCommand は、Linux 以外では使わない。
func guardCommand(adapterProcess) (*exec.Cmd, error) {
	return nil, errors.New("guard is not available")
}

// maybeRunGuard は、Linux 以外では何もしない。
func maybeRunGuard() {}
