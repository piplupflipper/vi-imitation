package server

import "github.com/piplupflipper/vi-imitation/ext"

// Text は、写しの中身をテキストにして返す（テストが、本体の役に出した中身と比べるため）。
func (t *TextLog) Text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return ext.JoinViewText(t.lines, t.eol)
}

// WindowsArrived は、接続の読み取りの goroutine が受け取った window/change の数を返す（テストが、知らせが届き終えるのを待つため）。
func (s *Server) WindowsArrived() uint64 {
	s.windows.mu.Lock()
	defer s.windows.mu.Unlock()
	return s.windows.arrived
}
