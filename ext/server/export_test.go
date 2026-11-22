package server

// WindowsArrived は、接続の読み取りの goroutine が受け取った window/change の数を返す（テストが、知らせが届き終えるのを待つため）。
func (s *Server) WindowsArrived() uint64 {
	s.windows.mu.Lock()
	defer s.windows.mu.Unlock()
	return s.windows.arrived
}
