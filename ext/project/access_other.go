//go:build !linux && !darwin

package project

// canExecute は、access(2) の無い環境では、x のビット（isExecutable が先に確かめる）だけで実行できるとみなす。
func canExecute(string) bool {
	return true
}
