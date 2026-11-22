package server

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// この節は、標準の拡張機能（基本設計 6 章）のために足したメッセージ（基本設計 5.3 の 2 つ目の表）を扱う。

// ErrBufferClosed は、WaitBuffer で待っている間か、待ち始める前に、そのバッファの buffer/close が届いた（その後に buffer/open が届いていない）ことを表す。
// 本体は、不正な UTF-8 を含むバッファを拡張機能へ送らず、editor/open で開いたときは buffer/open の代わりに buffer/close を送る（ext.BufferCloseParams）。
var ErrBufferClosed = errors.New("server: buffer closed while waiting")

// ErrNoBufferSync は、本体がこの拡張機能へバッファの同期を送らない（宣言ファイルの noBufferSync。ext.InitializeParams の NoBufferSync）ので、
// WaitBuffer がバッファの複製を待たなかったことを表す。待っても buffer/open は届かず、ctx の期限まで戻らなくなるためである（#367）。
var ErrNoBufferSync = errors.New("server: the host does not sync buffers to this extension (noBufferSync)")

// call は、本体へリクエスト method を送り、応答を待つ。ループの上から呼ぶと送らずに ErrEditInHandler を返す。
func call[R any](ctx context.Context, s *Server, method string, params any) (R, error) {
	var res R
	if onLoop(ctx, s) {
		return res, ErrEditInHandler
	}
	conn := s.conn.Load()
	if conn == nil {
		return res, ErrNotRunning
	}
	err := conn.Call(ctx, method, params, &res)
	return res, err
}

// EditorState は、本体へ editor/state を送り、編集の状態（カーソルのあるバッファ、カーソル位置、モード、選択範囲、
// 開いているバッファの一覧）を問い合わせる。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
func (s *Server) EditorState(ctx context.Context) (ext.EditorStateResult, error) {
	return call[ext.EditorStateResult](ctx, s, ext.MethodEditorState, ext.EditorStateParams{})
}

// OpenEditor は、本体へ editor/open を送り、ファイルかバッファを開かせる。
// p が ext.EditorOpenParams.Validate を通らなければ、送らずにその誤りを返す。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
//
// 本体は、この拡張機能がそのバッファの複製を持っていなければ、応答より先に buffer/open を送る。
// ただし buffer/open はループが処理するので、OpenEditor が戻った時点では複製がまだできていないことがある。
// 複製を読むときは、応答の Buffer と Version で WaitBuffer を呼んでから読む。
func (s *Server) OpenEditor(ctx context.Context, p ext.EditorOpenParams) (ext.EditorOpenResult, error) {
	if err := p.Validate(); err != nil {
		return ext.EditorOpenResult{}, err
	}
	return call[ext.EditorOpenResult](ctx, s, ext.MethodEditorOpen, p)
}

// SaveBuffer は、本体へ buffer/save を送り、バッファをファイルへ書き出させる。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
func (s *Server) SaveBuffer(ctx context.Context, p ext.BufferSaveParams) (ext.BufferSaveResult, error) {
	return call[ext.BufferSaveResult](ctx, s, ext.MethodBufferSave, p)
}

// ListDiagnostics は、本体へ diagnostic/list を送り、本体が受け取り済みの診断を問い合わせる。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
func (s *Server) ListDiagnostics(ctx context.Context, p ext.DiagnosticListParams) (ext.DiagnosticListResult, error) {
	return call[ext.DiagnosticListResult](ctx, s, ext.MethodDiagnosticList, p)
}

// AskChoice は、本体へ choice/ask を送り、利用者に選択肢から 1 つ選ばせる。
// 本体は利用者の応答を待つので、応答までの時間に上限は無い。打ち切るときは ctx を取り消す。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
func (s *Server) AskChoice(ctx context.Context, p ext.ChoiceAskParams) (ext.ChoiceAskResult, error) {
	return call[ext.ChoiceAskResult](ctx, s, ext.MethodChoiceAsk, p)
}

// SetDiagnostics は、本体へ diagnostic/set を送る。応答は待たない。どこから呼んでもよい。
func (s *Server) SetDiagnostics(p ext.DiagnosticSetParams) error {
	return s.notify(ext.MethodDiagnosticSet, p)
}

// SetSigns は、本体へ sign/set を送る。応答は待たない。どこから呼んでもよい。
func (s *Server) SetSigns(p ext.SignSetParams) error {
	return s.notify(ext.MethodSignSet, p)
}

// ShowPopup は、本体へ popup/show を送る。応答は待たない。どこから呼んでもよい。
func (s *Server) ShowPopup(p ext.PopupShowParams) error {
	return s.notify(ext.MethodPopupShow, p)
}

// AppendOutput は、本体へ output/append を送り、名前が channel の出力の欄へ text を書き足させる。
// 応答は待たない。どこから呼んでもよい。
func (s *Server) AppendOutput(channel, text string) error {
	return s.notify(ext.MethodOutputAppend, ext.OutputAppendParams{Channel: channel, Text: text})
}

// BufferWillSaveHandler は、buffer/willSave に答えるハンドラーである。
// b は p.Buffer の複製で、そのバージョン番号は p.Version と一致する。
// 返した result に Transaction があれば、その BaseVersion は、Server が p.Version で上書きする。
// 編集しないなら、Transaction を省いた result を返す。
type BufferWillSaveHandler func(ctx context.Context, b *Buffer, p *ext.BufferWillSaveParams) (ext.BufferWillSaveResult, error)

// AsyncBufferWillSaveHandler は、buffer/willSave に後から答えるハンドラーである。
// 言語サーバーへ整形を問い合わせるときに使う。守ることは他の非同期のハンドラーと同じである。
type AsyncBufferWillSaveHandler func(ctx context.Context, b *Buffer, p *ext.BufferWillSaveParams, reply func(ext.BufferWillSaveResult, error))

// BufferWillSave は、保存の直前の buffer/willSave に答えるハンドラーを登録する。
// 本体が buffer/willSave を送るのは、宣言ファイルの saveHook が true の拡張機能だけである。
// 2 回登録するか（BufferWillSaveAsync との重複を含む）、Serve の後に呼ぶと panic する。
//
// 登録していない拡張機能が buffer/willSave を受け取ったら、Server は編集無しを応答する。
// バッファを開いていないか、複製のバージョン番号が一致しなければ、ハンドラーを呼ばずに失敗を応答する。
// 本体は、失敗の応答も期限の超過も、その拡張機能の編集無しとして扱い、保存を続ける（基本設計 6.7）。
func (s *Server) BufferWillSave(h BufferWillSaveHandler) {
	register(s, s.willSave, "buffer/willSave handler", "", syncEntry[ext.BufferWillSaveParams, ext.BufferWillSaveResult](h))
}

// BufferWillSaveAsync は、BufferWillSave の非同期版である。
func (s *Server) BufferWillSaveAsync(h AsyncBufferWillSaveHandler) {
	register(s, s.willSave, "buffer/willSave handler", "", asyncEntry[ext.BufferWillSaveParams, ext.BufferWillSaveResult](h))
}

func (s *Server) bufferWillSave(ctx context.Context, req *rpc.Request, reply rpc.Replier) {
	if len(s.willSave) == 0 {
		var p ext.BufferWillSaveParams
		if err := decodeParams(req, &p); err != nil {
			s.reply(req.Method, reply, nil, err)
			return
		}
		s.reply(req.Method, reply, ext.BufferWillSaveResult{}, nil)
		return
	}
	query(ctx, s, req, reply, s.willSave, func(p *ext.BufferWillSaveParams) (ext.BufferID, ext.Version, string) {
		return p.Buffer, p.Version, ""
	}, nil, func(r *ext.BufferWillSaveResult, v ext.Version) {
		if r.Transaction != nil {
			r.Transaction.BaseVersion = v
		}
	})
}

// BufferDidSaveEvent は、buffer/didSave を受け取ったことを表す。
type BufferDidSaveEvent struct {
	// Buffer は、書き出したバッファの複製である。複製を持っていないか、送り直しを待っている場合は nil。
	// フックが戻るまでの間だけ内容を保つ。
	Buffer *Buffer
	// Params は、受け取った params である。Params.Version は書き出した時点のバージョン番号である。
	Params ext.BufferDidSaveParams
}

// OnBufferDidSave は、buffer/didSave を受け取ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
// LSP の拡張機能が textDocument/didSave を、DAP の拡張機能がブレークポイントの送り直しを行うのに使う。
func (s *Server) OnBufferDidSave(f func(ctx context.Context, e *BufferDidSaveEvent)) {
	s.mustNotStarted()
	s.onDidSave = f
}

func (s *Server) bufferDidSave(ctx context.Context, p *ext.BufferDidSaveParams) {
	if s.onDidSave == nil {
		return
	}
	e := &BufferDidSaveEvent{Params: *p}
	if b, ok := s.buffers[p.Buffer]; ok && !s.stale[p.Buffer] {
		e.Buffer = b
	}
	s.hook("buffer did save hook", func() { s.onDidSave(ctx, e) })
}

// WaitBuffer は、バッファ id の複製が、バージョン番号 version 以降になるまで待ち、その時点の複製の Clone を返す。
// 既にそうなっていれば待たずに返す。
// editor/open（OpenEditor）の応答を受け取った後に、本体が先に送った buffer/open の処理を待つのに使う（基本設計 6.7）。
// 本体が buffer/edit を適用した後の buffer/change を待つのにも使える。
//
// 複製は、ループが buffer/open か buffer/change を処理した時点で調べる。
// 送り直しを待っている（buffer/resync を送った）複製は、次の buffer/open まで条件を満たさない。
//
// ctx が終わったら ctx.Err() を、待つ間か待ち始める前にそのバッファの buffer/close が届いたら（その後に buffer/open が届いていなければ）ErrBufferClosed を、
// 接続が終わったか Serve の前なら ErrNotRunning を返す。
// 本体がこの拡張機能へバッファの同期を送らない（宣言ファイルの noBufferSync）なら、待たずに ErrNoBufferSync を返す。
// Edit と同じく、ループの上から呼ぶと、ループが自分を待って止まるので、待たずに ErrEditInHandler を返す。
//
// req: FR-229
func (s *Server) WaitBuffer(ctx context.Context, id ext.BufferID, version ext.Version) (*Buffer, error) {
	if onLoop(ctx, s) {
		return nil, ErrEditInHandler
	}
	if s.noBufferSync.Load() {
		return nil, ErrNoBufferSync
	}
	conn, q := s.conn.Load(), s.loopq.Load()
	if conn == nil || q == nil {
		return nil, ErrNotRunning
	}
	w := &bufWaiter{version: version, done: make(chan struct{})}
	q.push(inbound{fn: func() { s.addWaiter(id, w) }})
	select {
	case <-w.done:
		return w.buf, w.err
	case <-ctx.Done():
		w.cancelled.Store(true)
		return nil, ctx.Err()
	case <-conn.Done():
		return nil, ErrNotRunning
	}
}

// bufWaiter は、WaitBuffer の 1 つの呼び出しである。cancelled 以外は、ループだけが書く。
type bufWaiter struct {
	version ext.Version
	// cancelled は、呼び出し側が待つのをやめたことを表す。ループは次に調べたときに捨てる。
	cancelled atomic.Bool
	// done は、buf か err を決めたときに閉じる。
	done chan struct{}
	buf  *Buffer
	err  error
}

func (w *bufWaiter) resolve(b *Buffer, err error) {
	w.buf, w.err = b, err
	close(w.done)
}

// ready は、複製 id が w の条件を満たしていれば、その複製を返す。ループから呼ぶ。
func (s *Server) ready(id ext.BufferID, w *bufWaiter) (*Buffer, bool) {
	b, ok := s.buffers[id]
	if !ok || s.stale[id] || b.version < w.version {
		return nil, false
	}
	return b, true
}

// addWaiter は、w の条件を調べ、満たしていれば返し、満たしていなければ待ちに加える。ループから呼ぶ。
func (s *Server) addWaiter(id ext.BufferID, w *bufWaiter) {
	if w.cancelled.Load() {
		return
	}
	if b, ok := s.ready(id, w); ok {
		w.resolve(b.Clone(), nil)
		return
	}
	if s.closed[id] {
		w.resolve(nil, ErrBufferClosed)
		return
	}
	s.waiters[id] = append(s.waiters[id], w)
}

// wakeWaiters は、複製 id が変わった後に、条件を満たした待ちを返す。ループから呼ぶ。
func (s *Server) wakeWaiters(id ext.BufferID) {
	ws := s.waiters[id]
	if len(ws) == 0 {
		return
	}
	kept := ws[:0]
	for _, w := range ws {
		if w.cancelled.Load() {
			continue
		}
		b, ok := s.ready(id, w)
		if !ok {
			kept = append(kept, w)
			continue
		}
		// 呼び出しごとに別の Clone を返す。同じ Clone を共有すると、呼び出し側どうしの読み書きが競合しうる。
		w.resolve(b.Clone(), nil)
	}
	clear(ws[len(kept):])
	if len(kept) == 0 {
		delete(s.waiters, id)
		return
	}
	s.waiters[id] = kept
}

// failWaiters は、複製 id の待ちをすべて err で返す。ループから呼ぶ。
func (s *Server) failWaiters(id ext.BufferID, err error) {
	for _, w := range s.waiters[id] {
		if !w.cancelled.Load() {
			w.resolve(nil, err)
		}
	}
	delete(s.waiters, id)
}
