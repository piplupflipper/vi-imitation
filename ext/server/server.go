package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// ErrNotRunning は、Serve の前か後に、本体へメッセージを送ろうとしたことを表す。
var ErrNotRunning = errors.New("server: not serving")

// ErrEditInHandler は、Server が呼んだハンドラーかフックの中から、その ctx で、本体の応答を待つ関数を呼んだことを表す。
// 対象は Edit、EditorState、OpenEditor、SaveBuffer、ListDiagnostics、AskChoice、AskInput、WaitBuffer である。
// 待つと詰まるので、これらはリクエストを送らずにこの誤りを返す（Edit と WaitBuffer の doc を参照）。
var ErrEditInHandler = errors.New("server: request to the host called from a handler or hook; it would block the message loop")

// IsReadOnly は、err が、編集を禁止したバッファへの buffer/edit を本体が断った誤りの応答（code が ext.ErrorCodeReadOnly）かを返す。
// Edit が返した誤りに使う。
func IsReadOnly(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == ext.ErrorCodeReadOnly
}

// shutdownFlushTimeout は、shutdown を処理した後に、送信の列を本体へ書き出すのを待つ上限である（仮）。
const shutdownFlushTimeout = time.Second

// codeRequestCancelled は、取り消されたリクエストへの応答のエラーコードである（LSP の RequestCancelled と同じ値）。
const codeRequestCancelled = -32800

// Server は、拡張機能の側のメッセージループである。
// 本体との JSON-RPC の接続、バッファの複製の管理、登録したハンドラーの呼び出しを行う。
//
// New で作り、Motion などでハンドラーを登録してから、Run（標準入出力）か Serve で動かす。
// 登録は Serve の前に済ませる。Serve の後に登録すると panic する。
//
// # 処理の順序
//
// 本体から届いたリクエストと通知は、届いた順に、Server の持つ 1 つの goroutine（以下、ループ）が 1 件ずつ処理する。
// ハンドラーとフック（OnInitialize、OnBufferOpen、OnBufferChange、OnBufferClose、OnBufferDidSave、OnShutdown）は、すべてループから呼ばれる。
// このため、ハンドラーが読む複製は、本体がそのリクエストより前に送った buffer/change をすべて適用した内容になる。
// ハンドラーとフックが戻るまで次のメッセージを処理しないので、どちらも速やかに戻る。
//
// 時間のかかる問い合わせ（言語サーバーの応答を待つ定義へのジャンプなど）は、MotionAsync などで登録する。
// 非同期のハンドラーは、ループの上で呼ばれて速やかに戻り、結果は後で別の goroutine から reply で返す。
// その間もループは buffer/change と ping を処理し続ける。
// 時間のかかる解析（構文の解析など）は、Buffer.Clone した複製を別の goroutine に渡して行い、結果は SetDecorations などで送る。
//
// 受信そのものは別の goroutine が続けるので、ハンドラーの実行中に届いた $/cancel で、ハンドラーの ctx が取り消される。
//
// # 補助が処理するメッセージ
//
//   - initialize: ext.APIVersion を応答する。OnInitialize を登録していれば、応答の前に呼ぶ
//   - ping: 空の結果を応答する。ループが応答するので、同期のハンドラーかフックが止まっていれば ping も応答しない。
//     本体はこれで拡張機能の無限ループやデッドロックを検出する（基本設計 5.2）
//   - $/cancel: 対象のリクエストのハンドラーの ctx を取り消す。まだ処理していないリクエストなら、ハンドラーを呼ばずに失敗を応答する
//   - buffer/open、buffer/change、buffer/close: 複製を作る、更新する、捨てる
//   - buffer/willSave: BufferWillSave で登録したハンドラーを呼ぶ。登録していなければ、編集無しを応答する
//   - buffer/didSave: OnBufferDidSave で登録したフックを呼ぶ
//   - console/complete: ConsoleComplete で登録したハンドラーを呼ぶ。登録していなければ、OK を false にした結果を応答する
//   - view/keySync: OnView で登録したフックに、Sync を真にした ViewKey の ViewEvent を渡す。フックが ViewEvent.Defer を呼ばなければ、
//     空の結果を応答する（register.go）
//   - window/change: OnWindowChange で登録したフックを呼ぶ
//   - shutdown: OnShutdown で登録したフックを呼び、送信の列を書き出してから接続を閉じ、Serve から nil を返す
//
// # 本体へ送るメッセージ
//
// 通知（SetDecorations、SetVirtualTexts、ShowMessage、SetDiagnostics、SetSigns、ShowPopup、AppendOutput、ReportProgress、SetBand）は、どこから呼んでもよい。
// リクエスト（Edit、EditorState、OpenEditor、SaveBuffer、ListDiagnostics、AskChoice、AskInput）と WaitBuffer は、
// 本体の応答かループの処理を待つので、ループの上から呼んではならない（Edit の doc）。
// ハンドラーかフックから使うときは、go で別の goroutine を起こし、Server が渡したのとは別の ctx で呼ぶ。
//
// buffer/change の FromVersion が複製の番号と一致しないか、編集の位置が複製の外を指す場合は、差分が欠けたと判断し、
// buffer/resync を送る（基本設計 5.3）。次の buffer/open が届くまで、そのバッファの差分は捨て、
// そのバッファへの問い合わせには失敗を応答する。
//
// # 失敗の応答
//
// 次の場合、Server はハンドラーを呼ばずに、または呼んだ後に、JSON-RPC のエラーを応答する。
// 本体はどれも操作の失敗として扱う。
//
//   - ID が登録されていない
//   - バッファを開いていないか、複製のバージョン番号が params の Version と一致しない
//   - ハンドラーが誤りを返した
//   - ハンドラーが panic した。panic は回復し、拡張機能のプロセスは動き続ける。スタックは ErrorLog に書く
//
// # 標準出力
//
// Run は標準出力を本体とのメッセージに使う。拡張機能は標準出力へ他のものを書いてはならない。
// ログは標準エラー出力へ書く。本体はそれをログとして記録する（基本設計 5.3）。
type Server struct {
	// ErrorLog は、panic と内部の誤りを書くロガーである。nil なら標準エラー出力へ書く。
	ErrorLog *log.Logger

	motions     map[string]entry[ext.MotionResolveParams, ext.MotionResolveResult]
	textObjects map[string]entry[ext.TextObjectResolveParams, ext.TextObjectResolveResult]
	operators   map[string]entry[ext.OperatorApplyParams, ext.OperatorApplyResult]
	commands    map[string]entry[ext.CommandExecuteParams, ext.CommandExecuteResult]
	// providers は、provider/invoke のハンドラーである。キーは操作の名前（ext.Operation の文字列）である。
	providers  map[string]entry[ext.ProviderInvokeParams, ext.ProviderInvokeResult]
	onInit     func(ctx context.Context, e *InitializeEvent) error
	onOpen     func(ctx context.Context, e *BufferOpenEvent)
	onChange   func(ctx context.Context, e *BufferChangeEvent)
	onClose    func(ctx context.Context, e *BufferCloseEvent)
	onDidSave  func(ctx context.Context, e *BufferDidSaveEvent)
	onShutdown func(ctx context.Context, e *ShutdownEvent)
	// onView は、ポップアップの利用者の操作の知らせ（view.go）のフックである。
	onView func(ctx context.Context, e *ViewEvent)
	// textLogs は、部品に結びついている書き足すテキストの写し（textlog.go の TextLog）の表である。どの goroutine からも触る。
	textLogs textLogs
	// onWindow は、窓の変化の知らせ（window.go）のフックである。
	onWindow func(ctx context.Context, e *WindowChangeEvent)
	// windows は、窓ごとに最後に届いた window/change の番号である（window.go の windowArrived）。読み取りの goroutine とループが触る。
	windows windowLatest
	// willSave は、buffer/willSave のハンドラーである。登録は 1 つだけで、キーは空文字列である。
	willSave map[string]entry[ext.BufferWillSaveParams, ext.BufferWillSaveResult]
	// consoleComplete は、console/complete のハンドラーである（consolecomplete.go）。nil なら登録していない。
	consoleComplete AsyncConsoleCompleteHandler
	// registers は、本体へ送る register/set の列である（register.go の SetRegisterAsync）。どの goroutine からも触る。
	registers registerQueue

	started atomic.Bool
	conn    atomic.Pointer[rpc.Conn]
	// loopq は、Serve の間のループの列である。WaitBuffer がループへ処理を頼むのに使う。
	loopq atomic.Pointer[queue]
	// noBufferSync は、initialize の NoBufferSync（本体がバッファの同期を送らない）を受け取ったことを表す。WaitBuffer は待たずに誤りを返す。
	noBufferSync atomic.Bool

	// 以下は、ループだけが触る。
	buffers map[ext.BufferID]*Buffer
	// stale は、buffer/resync を送り、buffer/open を待っているバッファである。
	stale map[ext.BufferID]bool
	// waiters は、WaitBuffer で複製を待っている呼び出しである。
	waiters map[ext.BufferID][]*bufWaiter
	// closed は、buffer/close を受け取り、その後に buffer/open を受け取っていないバッファである。
	// 本体は、不正な UTF-8 を含むバッファへの editor/open の応答より先に buffer/close を送る（ext.BufferCloseParams）。
	// WaitBuffer がその応答の後に呼ばれても待ち続けないように、閉じたことを覚えておく。
	// 本体はバッファの番号を使い回さないので、閉じた番号が増えても 1 つあたり数バイトである。
	closed map[ext.BufferID]bool
	// shutdown は、shutdown を処理したかどうかである。true になったら Serve は接続を閉じて戻る。
	shutdown bool
}

// New は、ハンドラーを登録していない Server を作る。
func New() *Server {
	return &Server{
		motions:     make(map[string]entry[ext.MotionResolveParams, ext.MotionResolveResult]),
		textObjects: make(map[string]entry[ext.TextObjectResolveParams, ext.TextObjectResolveResult]),
		operators:   make(map[string]entry[ext.OperatorApplyParams, ext.OperatorApplyResult]),
		commands:    make(map[string]entry[ext.CommandExecuteParams, ext.CommandExecuteResult]),
		providers:   make(map[string]entry[ext.ProviderInvokeParams, ext.ProviderInvokeResult]),
		willSave:    make(map[string]entry[ext.BufferWillSaveParams, ext.BufferWillSaveResult]),
		buffers:     make(map[ext.BufferID]*Buffer),
		stale:       make(map[ext.BufferID]bool),
		waiters:     make(map[ext.BufferID][]*bufWaiter),
		closed:      make(map[ext.BufferID]bool),
	}
}

func (s *Server) mustNotStarted() {
	if s.started.Load() {
		panic("server: register after Serve")
	}
}

// Run は、標準入力と標準出力で本体と通信し、Serve と同じく動く。
// 拡張機能の main から呼ぶ。
func (s *Server) Run(ctx context.Context) error {
	return s.Serve(ctx, os.Stdin, os.Stdout)
}

// Serve は、r から本体のメッセージを読み、w へ書き、接続が終わるまでメッセージを処理する。
// r と w が io.Closer を実装していれば、終わるときに閉じる。
//
// 本体が接続を閉じたとき（r が EOF になったとき）と、shutdown を処理し終えたときと、ctx が終わったときは nil を返す。
// JSON-RPC として解釈できないメッセージを受け取ったときなど、それ以外の理由で終わったときは、その誤りを返す。
// 1 つの Server で Serve を呼べるのは 1 回だけで、2 回目は誤りを返す。
//
// ループは Serve を呼んだ goroutine で動く。
// 接続が終わると、実行中のハンドラーとフックの ctx は取り消されるが、Serve はそれらが戻るまで戻らない。
// ctx を見ずに止まっているハンドラーがあると、ctx を取り消しても Serve は戻らない。
// 非同期のハンドラーが後で呼ぶ reply は待たない（接続が終わった後の reply は何も送らない）。
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("server: Serve called twice")
	}
	q := newQueue()
	conn := rpc.NewConn(r, w, rpc.HandlerFuncs{
		Request: func(ctx context.Context, req *rpc.Request, reply rpc.Replier) {
			q.push(inbound{ctx: ctx, req: req, reply: reply})
		},
		Notification: func(ctx context.Context, n *rpc.Notification) {
			q.push(inbound{ctx: ctx, note: n, winSeq: s.windowArrived(n)})
		},
	}, rpc.WithCancelMethod(ext.MethodCancel))
	s.conn.Store(conn)
	defer s.conn.Store(nil)
	s.loopq.Store(q)
	defer s.loopq.Store(nil)

	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	for {
		batch := q.take()
		for _, m := range batch {
			if conn.Err() != nil {
				break
			}
			s.dispatch(m)
			if s.shutdown {
				// フックが送った通知を本体へ届けてから閉じる。本体が読まなくても、期限で諦める。
				fctx, cancel := context.WithTimeout(ctx, shutdownFlushTimeout)
				_ = conn.Flush(fctx)
				cancel()
				_ = conn.Close()
				return nil
			}
		}
		if len(batch) > 0 {
			continue
		}
		select {
		case <-q.wake:
		case <-conn.Done():
			_ = conn.Close()
			err := conn.Err()
			if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return err
		}
	}
}

// Edit は、本体へ buffer/edit を送り、応答を待つ。
// 補完の確定や整形のように、拡張機能の側から始める編集に使う。
// 編集を禁止したバッファへの要求は、本体が誤りの応答で断るので、IsReadOnly が true を返す誤りになる。作り直しても通らない。
// 適用されると、本体は buffer/change を送り、複製はループがそれを処理した時点で更新される（Edit が戻った時点ではまだ古いことがある）。
//
// ループの上（同期のハンドラー、非同期のハンドラーの同期の部分、フック）で Edit の応答を待ってはならない。
// 本体はループの応答を待つ間 buffer/edit を処理できず、ループは Edit の応答を待つので、本体の期限まで詰まる。
// Server が渡した ctx（またはそれから作った ctx）で、ループの上から呼ぶと、送らずに ErrEditInHandler を返す。
// 問い合わせの編集は OperatorApplyResult と CommandExecuteResult の Transaction で返し、
// フックから編集したいときは、go で別の goroutine を起こし、その中で Server が渡したのとは別の ctx で呼ぶ（Server の doc）。
// Server が渡した ctx（またはそれから作った ctx）は、フックが戻るまで、別の goroutine から渡しても ErrEditInHandler になる（loopMark の doc）。
// 非同期のハンドラーの reply の前に、その結果を待つ本体へ Edit するのも同じ理由で詰まる。
func (s *Server) Edit(ctx context.Context, p ext.BufferEditParams) (ext.BufferEditResult, error) {
	var res ext.BufferEditResult
	if onLoop(ctx, s) {
		return res, ErrEditInHandler
	}
	conn := s.conn.Load()
	if conn == nil {
		return res, ErrNotRunning
	}
	err := conn.Call(ctx, ext.MethodBufferEdit, p, &res)
	return res, err
}

// SetDecorations は、本体へ decoration/set を送る。応答は待たない。どこから呼んでもよい。
func (s *Server) SetDecorations(p ext.DecorationSetParams) error {
	return s.notify(ext.MethodDecorationSet, p)
}

// ShowMessage は、本体へ message/show を送り、利用者へメッセージを表示させる。応答は待たない。どこから呼んでもよい。
func (s *Server) ShowMessage(level ext.MessageLevel, text string) error {
	return s.notify(ext.MethodMessageShow, ext.MessageShowParams{Level: level, Text: text})
}

func (s *Server) notify(method string, params any) error {
	conn := s.conn.Load()
	if conn == nil {
		return ErrNotRunning
	}
	return conn.Notify(method, params)
}

// loopKey は、ループの上で呼んでいることの印を ctx に付けるキーである。
type loopKey struct{}

// loopMark は、ループの上の呼び出しの印である。呼び出しが戻ると active を false にする。
// 非同期のハンドラーが ctx を別の goroutine へ持ち出しても、戻った後は印が効かない。
// 印は goroutine を区別しないので、戻る前は、持ち出した先の goroutine から呼んでもループの上からの呼び出しとみなす（#214）。
type loopMark struct {
	s      *Server
	active atomic.Bool
}

// onLoop は、ctx に s のループの上で有効な印が付いているかどうかを返す。
func onLoop(ctx context.Context, s *Server) bool {
	m, ok := ctx.Value(loopKey{}).(*loopMark)
	return ok && m.s == s && m.active.Load()
}

// enter は、ctx にループの上の印を付ける。返した関数で印を外す。
func (s *Server) enter(ctx context.Context) (context.Context, func()) {
	m := &loopMark{s: s}
	m.active.Store(true)
	return context.WithValue(ctx, loopKey{}, m), func() { m.active.Store(false) }
}

// dispatch は、1 件のメッセージを処理する。
func (s *Server) dispatch(m inbound) {
	if m.fn != nil {
		m.fn()
		return
	}
	ctx, leave := s.enter(m.ctx)
	defer leave()
	if m.note != nil {
		if m.note.Method == ext.MethodWindowChange {
			s.windowChange(ctx, m.note, m.winSeq)
			return
		}
		s.handleNotification(ctx, m.note)
		return
	}
	if ctx.Err() != nil {
		// 処理する前に取り消されたか、接続が終わった。
		s.reply(m.req.Method, m.reply, nil, &rpc.Error{Code: codeRequestCancelled, Message: "request cancelled: " + m.req.Method})
		return
	}
	s.handleRequest(ctx, m.req, m.reply)
}

func (s *Server) reply(method string, reply rpc.Replier, result any, err error) {
	if rerr := reply(result, err); rerr != nil {
		s.logf("reply to %s: %v", method, rerr)
	}
}

func (s *Server) handleRequest(ctx context.Context, req *rpc.Request, reply rpc.Replier) {
	switch req.Method {
	case ext.MethodInitialize:
		res, err := s.initialize(ctx, req)
		s.reply(req.Method, reply, res, err)
	case ext.MethodPing:
		s.reply(req.Method, reply, ext.PingResult{}, nil)
	case ext.MethodMotionResolve:
		query(ctx, s, req, reply, s.motions, func(p *ext.MotionResolveParams) (ext.BufferID, ext.Version, string) {
			return p.Buffer, p.Version, p.ID
		}, nil, func(r *ext.MotionResolveResult, v ext.Version) { r.Version = v })
	case ext.MethodTextObjectResolve:
		query(ctx, s, req, reply, s.textObjects, func(p *ext.TextObjectResolveParams) (ext.BufferID, ext.Version, string) {
			return p.Buffer, p.Version, p.ID
		}, nil, func(r *ext.TextObjectResolveResult, v ext.Version) { r.Version = v })
	case ext.MethodOperatorApply:
		query(ctx, s, req, reply, s.operators, func(p *ext.OperatorApplyParams) (ext.BufferID, ext.Version, string) {
			return p.Buffer, p.Version, p.ID
		}, nil, func(r *ext.OperatorApplyResult, v ext.Version) { r.Transaction.BaseVersion = v })
	case ext.MethodBufferWillSave:
		s.bufferWillSave(ctx, req, reply)
	case ext.MethodCommandExecute:
		query(ctx, s, req, reply, s.commands, func(p *ext.CommandExecuteParams) (ext.BufferID, ext.Version, string) {
			return p.Buffer, p.Version, p.ID
		}, func(p *ext.CommandExecuteParams) bool { return p.Withheld }, func(r *ext.CommandExecuteResult, v ext.Version) {
			if r.Transaction != nil {
				r.Transaction.BaseVersion = v
			}
		})
	case ext.MethodProviderInvoke:
		query(ctx, s, req, reply, s.providers, func(p *ext.ProviderInvokeParams) (ext.BufferID, ext.Version, string) {
			return p.Buffer, p.Version, string(p.Operation)
		}, func(p *ext.ProviderInvokeParams) bool { return p.Withheld }, func(r *ext.ProviderInvokeResult, v ext.Version) {
			if r.Transaction != nil {
				r.Transaction.BaseVersion = v
			}
		})
	case ext.MethodConsoleComplete:
		s.consoleCompleteRequest(ctx, req, reply)
	case ext.MethodViewKeySync:
		s.viewKeySync(ctx, req, reply)
	default:
		s.reply(req.Method, reply, nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "method not found: " + req.Method})
	}
}

func (s *Server) initialize(ctx context.Context, req *rpc.Request) (any, error) {
	var p ext.InitializeParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	s.noBufferSync.Store(p.NoBufferSync)
	if s.onInit != nil {
		e := &InitializeEvent{APIVersion: p.APIVersion, Settings: p.Settings, NoBufferSync: p.NoBufferSync}
		if err := s.protect(req.Method, func() error { return s.onInit(ctx, e) }); err != nil {
			return nil, err
		}
	}
	return ext.InitializeResult{APIVersion: ext.APIVersion}, nil
}

// bufferAt は、id のバッファの複製が version の時点にあれば返す。
func (s *Server) bufferAt(id ext.BufferID, version ext.Version) (*Buffer, error) {
	b, ok := s.buffers[id]
	switch {
	case !ok:
		return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("buffer %d is not open", id)}
	case s.stale[id]:
		return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("buffer %d is waiting for resync", id)}
	case b.version != version:
		return nil, &rpc.Error{
			Code:    rpc.CodeInvalidParams,
			Message: fmt.Sprintf("buffer %d: version %d does not match the replica %d", id, version, b.version),
		}
	}
	return b, nil
}

func (s *Server) handleNotification(ctx context.Context, n *rpc.Notification) {
	switch n.Method {
	case ext.MethodBufferOpen:
		var p ext.BufferOpenParams
		if err := decodeParams(n, &p); err != nil {
			s.logf("%v", err)
			return
		}
		_, held := s.buffers[p.Buffer]
		b := newBuffer(&p)
		s.buffers[p.Buffer] = b
		delete(s.stale, p.Buffer)
		delete(s.closed, p.Buffer)
		if s.onOpen != nil {
			e := &BufferOpenEvent{Buffer: b, Reopen: held}
			s.hook("buffer open hook", func() { s.onOpen(ctx, e) })
		}
		s.wakeWaiters(p.Buffer)
	case ext.MethodBufferChange:
		var p ext.BufferChangeParams
		if err := decodeParams(n, &p); err != nil {
			s.logf("%v", err)
			return
		}
		s.bufferChange(ctx, &p)
	case ext.MethodBufferClose:
		var p ext.BufferCloseParams
		if err := decodeParams(n, &p); err != nil {
			s.logf("%v", err)
			return
		}
		_, open := s.buffers[p.Buffer]
		delete(s.buffers, p.Buffer)
		delete(s.stale, p.Buffer)
		s.closed[p.Buffer] = true
		if open && s.onClose != nil {
			e := &BufferCloseEvent{Buffer: p.Buffer}
			s.hook("buffer close hook", func() { s.onClose(ctx, e) })
		}
		s.failWaiters(p.Buffer, ErrBufferClosed)
	case ext.MethodBufferDidSave:
		var p ext.BufferDidSaveParams
		if err := decodeParams(n, &p); err != nil {
			s.logf("%v", err)
			return
		}
		s.bufferDidSave(ctx, &p)
	case ext.MethodShutdown:
		// params は中身が無いので読まない。読めない params でも止める。
		s.shutdown = true
		if s.onShutdown != nil {
			s.hook("shutdown hook", func() { s.onShutdown(ctx, &ShutdownEvent{}) })
		}
	default:
		// 知らない通知は捨てる（JSON-RPC 2.0 では通知に応答しない）。
		s.viewNotification(ctx, n)
	}
}

// bufferChange は、buffer/change の差分を複製に適用する。
// 差分が欠けたと判断したら buffer/resync を送り、buffer/open が届くまで以後の差分を捨てる。
//
// req: FR-16
func (s *Server) bufferChange(ctx context.Context, p *ext.BufferChangeParams) {
	if s.stale[p.Buffer] {
		return
	}
	b, ok := s.buffers[p.Buffer]
	var cause error
	switch {
	case !ok:
		cause = errors.New("buffer is not open")
	case b.version != p.FromVersion:
		cause = fmt.Errorf("fromVersion %d does not match the replica %d", p.FromVersion, b.version)
	default:
		cause = b.applyChange(p)
	}
	if cause == nil {
		if s.onChange != nil {
			e := &BufferChangeEvent{Buffer: b, FromVersion: p.FromVersion, ToVersion: p.ToVersion, Edits: p.Edits}
			s.hook("buffer change hook", func() { s.onChange(ctx, e) })
		}
		s.wakeWaiters(p.Buffer)
		return
	}
	s.logf("buffer %d: %v; requesting resync", p.Buffer, cause)
	s.stale[p.Buffer] = true
	var have ext.Version
	if ok {
		have = b.version
	}
	if err := s.notify(ext.MethodBufferResync, ext.BufferResyncParams{Buffer: p.Buffer, Version: have}); err != nil {
		s.logf("send buffer/resync: %v", err)
	}
}

// hook は、フックを呼び、panic を回復して ErrorLog に書く。
func (s *Server) hook(name string, f func()) {
	_ = s.protect(name, func() error { f(); return nil })
}

// protect は f を呼び、f の panic を回復して誤りに変える。
// 回復した panic は、スタックと一緒に ErrorLog に書く。
func (s *Server) protect(name string, f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("panic in %s: %v\n%s", name, r, debug.Stack())
			err = &rpc.Error{Code: rpc.CodeInternalError, Message: fmt.Sprintf("panic in %s: %v", name, r)}
		}
	}()
	return f()
}

func (s *Server) logf(format string, args ...any) {
	l := s.ErrorLog
	if l == nil {
		l = log.New(os.Stderr, "", log.LstdFlags)
	}
	l.Printf(format, args...)
}

// decodeParams は、リクエストか通知の params を v へ読み込む。読めなければ CodeInvalidParams の誤りを返す。
func decodeParams(m any, v any) error {
	var method string
	var params json.RawMessage
	switch m := m.(type) {
	case *rpc.Request:
		method, params = m.Method, m.Params
	case *rpc.Notification:
		method, params = m.Method, m.Params
	}
	if len(params) == 0 {
		params = []byte("{}")
	}
	if err := json.Unmarshal(params, v); err != nil {
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("%s: invalid params: %v", method, err)}
	}
	return nil
}

// inbound は、本体から届いた 1 件のメッセージか、ループで行う処理である。
// req、note、fn のどれか 1 つだけが nil でない。fn は WaitBuffer がループへ頼む処理である。
type inbound struct {
	ctx   context.Context
	req   *rpc.Request
	reply rpc.Replier
	note  *rpc.Notification
	fn    func()
	// winSeq は、note が window/change のときの、届いた順の番号である（windowArrived）。ほかの通知では 0 である。
	winSeq uint64
}

// queue は、接続の読み取りの goroutine から、ループへメッセージを渡す、上限の無い列である。
// 読み取りの goroutine を止めない（$/cancel と応答を読み続ける）ために、積む側は待たない。
type queue struct {
	mu    sync.Mutex
	items []inbound
	wake  chan struct{}
}

func newQueue() *queue { return &queue{wake: make(chan struct{}, 1)} }

func (q *queue) push(m inbound) {
	q.mu.Lock()
	q.items = append(q.items, m)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queue) take() []inbound {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.items
	q.items = nil
	return items
}
