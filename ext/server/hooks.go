package server

import (
	"context"
	"encoding/json"

	"github.com/piplupflipper/vi-imitation/ext"
)

// フックは、ループの上で呼ばれる（Server の doc の「処理の順序」）。
// 引数のイベントは、後から項目を足せるように構造体のポインターで渡す。
// ctx には、ループの上で呼んでいることの印が付いている。フックの中でこの ctx を Edit に渡すと ErrEditInHandler になる。
// フックの panic は回復して ErrorLog に書く。

// InitializeEvent は、initialize を受け取ったことを表す。
type InitializeEvent struct {
	// APIVersion は、本体が実装している拡張機能 API のバージョンである。
	APIVersion string
	// Settings は、利用者の設定ファイルのうち、この拡張機能に向けた部分である。無ければ nil。
	Settings json.RawMessage
	// NoBufferSync は、本体がこの拡張機能へバッファの同期を送らない（宣言ファイルの noBufferSync）ことを表す。
	// true なら、ハンドラーの b はいつも nil で、WaitBuffer は ErrNoBufferSync を返す。
	NoBufferSync bool
}

// BufferOpenEvent は、buffer/open で全文を受け取り、複製を作り直したことを表す。
type BufferOpenEvent struct {
	// Buffer は、作った複製である。フックが戻るまでの間だけ内容を保つ（Buffer の doc）。
	Buffer *Buffer
	// Reopen は、既に持っていたバッファの全文を受け取ったかどうかである。
	// buffer/resync の応答として本体が送り直したときは true になる。
	// LSP の拡張機能なら、false で textDocument/didOpen を、true で全文の textDocument/didChange を送る、といった振り分けに使う。
	Reopen bool
}

// BufferChangeEvent は、buffer/change の差分を複製に適用したことを表す。
// 差分が欠けて buffer/resync を送った場合は呼ばれず、次の buffer/open で BufferOpenEvent が届く。
type BufferChangeEvent struct {
	// Buffer は、差分を適用した後の複製である。フックが戻るまでの間だけ内容を保つ。
	Buffer *Buffer
	// FromVersion は、Edits を適用する前のバージョン番号である。
	FromVersion ext.Version
	// ToVersion は、Edits を適用した後のバージョン番号で、Buffer.Version() と等しい。
	ToVersion ext.Version
	// Edits は、適用した編集の列で、受け取った逐次の数え方のままである（ext.BufferChangeParams）。
	// 構文の解析や LSP の増分の同期に使う。
	Edits []ext.Edit
}

// BufferCloseEvent は、buffer/close を受け取り、複製を捨てたことを表す。
// 開いていないバッファの buffer/close では呼ばれない。
type BufferCloseEvent struct {
	// Buffer は、閉じたバッファの番号である。
	Buffer ext.BufferID
}

// OnInitialize は、initialize を受け取ったときに呼ぶ関数を登録する。
// f が誤りを返すと、initialize に失敗を応答する。本体はそれを起動の失敗として扱う。
// Serve の後に呼ぶと panic する。
func (s *Server) OnInitialize(f func(ctx context.Context, e *InitializeEvent) error) {
	s.mustNotStarted()
	s.onInit = f
}

// OnBufferOpen は、buffer/open で複製を作ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
func (s *Server) OnBufferOpen(f func(ctx context.Context, e *BufferOpenEvent)) {
	s.mustNotStarted()
	s.onOpen = f
}

// OnBufferChange は、buffer/change の差分を複製に適用したときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
func (s *Server) OnBufferChange(f func(ctx context.Context, e *BufferChangeEvent)) {
	s.mustNotStarted()
	s.onChange = f
}

// OnBufferClose は、buffer/close を受け取って複製を捨てたときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
func (s *Server) OnBufferClose(f func(ctx context.Context, e *BufferCloseEvent)) {
	s.mustNotStarted()
	s.onClose = f
}

// ShutdownEvent は、本体から shutdown を受け取ったことを表す（基本設計 5.2）。
// 中身は無いが、後から項目を足せるように構造体にしている。
type ShutdownEvent struct{}

// OnShutdown は、shutdown を受け取ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
//
// 拡張機能は f の中で、自分が起動した子プロセス（言語サーバー、デバッグアダプター、デバッグ対象など）を
// 正しい手順（LSP の shutdown と exit、DAP の disconnect など）で終わらせる。
// f が戻ると（登録していなければ shutdown を受け取った時点で）、Serve は送信の列を書き出してから接続を閉じ、nil を返す。
// shutdown より後に届いたメッセージは処理しない。
//
// 本体は shutdown を送ってから期限（仮の値）を過ぎると、拡張機能のプロセスグループへ SIGTERM を、
// さらに期限を過ぎると SIGKILL を送る。f はその期限より十分短く終わるように書く。
// f が呼ばれている間はループが止まるので、ping には応答しないが、本体は shutdown の後に ping を送らない。
// 他のフックと同じく、f の中で本体の応答を待つ関数（Edit など）をこの ctx で呼ぶと ErrEditInHandler になる。
// 本体への通知（ShowMessage など）は送れるが、本体がそれを処理する保証は無い。
func (s *Server) OnShutdown(f func(ctx context.Context, e *ShutdownEvent)) {
	s.mustNotStarted()
	s.onShutdown = f
}
