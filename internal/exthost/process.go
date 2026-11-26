package exthost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// 異常の理由（基本設計 5.2）。StateChange.Err と記録に使う。
var (
	errInitTimeout = errors.New("exthost: initialize timed out")
	errPingTimeout = errors.New("exthost: ping timed out")
	errExited      = errors.New("exthost: extension process exited")
	errConnClosed  = errors.New("exthost: connection closed")
	// errStopped は、[Host.Disable] で止めたことを表す。異常ではないので、連続の失敗に数えない。
	errStopped = errors.New("exthost: extension stopped")
)

// stderrDrainTimeout は、子プロセスを終わらせた後に、標準エラー出力の残りを読み切るのを待つ上限である。
// プロセスグループの外へ抜けた孫プロセスがパイプを持ち続けても、記録の goroutine が残らないようにする。
const stderrDrainTimeout = time.Second

// maxStderrLine は、標準エラー出力の 1 行として記録する上限のバイト数である。超えた分は次の行として記録する。
const maxStderrLine = 64 << 10

// run は、拡張機能のプロセスを 1 回起動した単位である。再起動すると新しい run を作る。
// conn と runningSince は Host.mu が守る。
type run struct {
	conn *rpc.Conn
	// runningSince は、稼働中になった時刻である。稼働中になる前はゼロ値。
	runningSince time.Time
	// queued は、このプロセスから届き、まだイベントループへ渡していないイベントの数である。
	queued int
	// shuttingDown は、shutdown を送ったかどうかである。true の後に届いたリクエストは、イベントループへ渡さずに失敗を応答する。
	shuttingDown bool

	failOnce sync.Once
	failErr  error
	// failed は、通信の goroutine が異常を見つけたときに閉じる（イベントの列の上限の超過など）。
	failed chan struct{}
}

func newRun() *run {
	return &run{failed: make(chan struct{})}
}

// fail は、プロセスを異常と判断したことを、監視の goroutine へ知らせる。最初の理由だけを残す。
func (r *run) fail(err error) {
	r.failOnce.Do(func() {
		r.failErr = err
		close(r.failed)
	})
}

// supervise は、拡張機能のプロセスを起動して監視し、異常で終わったら待ち時間の後に再起動する。
// 連続の失敗が上限に達するか、stop が閉じるか（[Host.Disable]）、Host が閉じると戻る（基本設計 5.2、図 4）。
//
// req: NFR-03
func (h *Host) supervise(e *extension, r *run, stop <-chan struct{}) {
	defer h.wg.Done()
	defer h.recoverPanic()
	id := e.spec.Manifest.ID
	for {
		err := h.runOnce(e, r, stop)
		if h.ctx.Err() != nil {
			return
		}

		h.mu.Lock()
		// Disable の後なら、プロセスがどの理由で終わっても、無効へ移す（再起動しない）。Reload の後なら未起動へ移す。
		if errors.Is(err, errStopped) || e.stopping {
			e.run = nil
			// 止めている間に届き、まだ渡していないイベントは捨てる（Close と同じ扱い）。
			h.purgeLocked(r)
			h.dropPendingLocked(e)
			h.stoppedLocked(e)
			h.mu.Unlock()
			return
		}
		if !r.runningSince.IsZero() && time.Since(r.runningSince) >= h.cfg.ResetAfter {
			e.failures = 0
		}
		e.failures++
		e.run = nil
		h.purgeLocked(r)
		// 図 4 のとおり、異常ではいったん再起動待ちへ移り、連続の失敗が上限に達していれば待たずに無効へ移る。
		h.setStateLocked(e, StateRestartWait, err)
		if e.failures >= h.cfg.MaxFailures {
			h.dropPendingLocked(e)
			h.setStateLocked(e, StateDisabled, fmt.Errorf("%w (%d consecutive failures)", err, e.failures))
			h.mu.Unlock()
			return
		}
		failures := e.failures
		delay := h.restartDelay(failures)
		h.mu.Unlock()

		h.log.Info("extension restart scheduled", "extension", id, "delay", delay, "failures", failures)
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-h.ctx.Done():
			t.Stop()
			return
		case <-stop:
			t.Stop()
			h.mu.Lock()
			if !h.closing {
				h.dropPendingLocked(e)
				h.stoppedLocked(e)
			}
			h.mu.Unlock()
			return
		}

		h.mu.Lock()
		// 起動の契機 socket の拡張機能は、再起動待ちのまま次の接続を待ってから起動する（基本設計 6.2）。
		if !h.waitConnectionLocked(e, stop) {
			if !h.closing {
				h.dropPendingLocked(e)
				h.stoppedLocked(e)
			}
			h.mu.Unlock()
			return
		}
		if h.closing {
			h.mu.Unlock()
			return
		}
		r = newRun()
		e.run = r
		h.setStateLocked(e, StateStarting, nil)
		h.mu.Unlock()
	}
}

// restartDelay は、連続の失敗が failures 回のときの再起動の待ち時間を返す。
func (h *Host) restartDelay(failures int) time.Duration {
	d := h.cfg.RestartDelay
	for i := 1; i < failures && d < h.cfg.MaxRestartDelay; i++ {
		d *= 2
	}
	return min(d, h.cfg.MaxRestartDelay)
}

// runOnce は、子プロセスを 1 回起動し、初期化し、異常が起きるまで監視する。
// 戻る前にプロセスを終わらせ、接続を閉じる。返す値は終わらせた理由である。
//
// Host が閉じたとき（[ErrClosed]）と stop が閉じたとき（errStopped）は、正常な終了として [Host.terminate] の手順で止める。
// 異常（5.2 の条件）のときは、プロセスグループごとすぐに SIGKILL を送る。
// 異常な拡張機能は shutdown を処理できないことが多く、待つと再起動が遅れるためである。
func (h *Host) runOnce(e *extension, r *run, stop <-chan struct{}) (err error) {
	id := e.spec.Manifest.ID
	listener, conns := h.takeSocketFiles(e)
	var extra []*os.File
	if listener != nil {
		extra = append([]*os.File{listener}, conns...)
	}
	p, err := startProcess(e.spec.command(), e.spec.Manifest.Args, e.spec.Dir, h.cfg.Env, extra, memoryLimitBytes(h.cfg.MemoryLimitMB))
	// 受け付けた接続は子プロセスへ複製したので、本体の側は閉じる。起動に失敗したときは接続が失われ、中継コマンドは終わる。
	for _, f := range conns {
		_ = f.Close()
	}
	if err != nil {
		return err
	}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		defer h.recoverPanic()
		h.logStderr(id, p.stderr)
	}()

	conn := rpc.NewConn(p.stdout, p.stdin, h.handler(e, r), h.rpcOptions()...)
	h.mu.Lock()
	r.conn = conn
	h.mu.Unlock()

	defer func() {
		if errors.Is(err, ErrClosed) || errors.Is(err, errStopped) {
			h.terminate(id, r, p)
		}
		p.kill()
		_ = conn.Close()
		<-p.exited
		t := time.NewTimer(stderrDrainTimeout)
		select {
		case <-stderrDone:
		case <-t.C:
		}
		t.Stop()
		_ = p.stderr.Close()
		<-stderrDone
	}()

	if err := h.initialize(e, r, p, stop); err != nil {
		return err
	}
	return h.monitor(r, p, stop)
}

// terminate は、拡張機能のプロセスを正常に止める（基本設計 5.2）。
// shutdown を送り、[Config.ShutdownTimeout] までに終わらなければプロセスグループへ SIGTERM を送り、
// さらに [Config.TerminateTimeout] まで待つ。それでも終わらなければ、呼び出し側が SIGKILL を送る。
// [Host.CloseContext] の ctx が終わったら（h.force）、期限を待たずに戻り、呼び出し側が SIGKILL を送る。
// プロセスが既に終わっていれば何もしない。
func (h *Host) terminate(id string, r *run, p *process) {
	// wait は、プロセスが d の間に終わったら 0、期限を過ぎたら 1、h.force が閉じたら 2 を返す。
	const (
		exited = iota
		timedOut
		forced
	)
	wait := func(d time.Duration) int {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-p.exited:
			return exited
		case <-t.C:
			return timedOut
		case <-h.force:
			return forced
		}
	}
	select {
	case <-p.exited:
		return
	default:
	}
	h.mu.Lock()
	r.shuttingDown = true
	conn := r.conn
	h.mu.Unlock()
	if err := conn.Notify(ext.MethodShutdown, ext.ShutdownParams{}); err != nil {
		h.log.Warn("extension shutdown not sent", "extension", id, "error", err)
	} else {
		switch wait(h.cfg.ShutdownTimeout) {
		case exited:
			return
		case forced:
			h.log.Warn("host close cancelled; sending SIGKILL", "extension", id)
			return
		}
		h.log.Warn("extension did not exit after shutdown; sending SIGTERM", "extension", id, "timeout", h.cfg.ShutdownTimeout)
	}
	p.signal(syscall.SIGTERM)
	switch wait(h.cfg.TerminateTimeout) {
	case exited:
	case forced:
		h.log.Warn("host close cancelled; sending SIGKILL", "extension", id)
	default:
		h.log.Warn("extension did not exit after SIGTERM; sending SIGKILL", "extension", id, "timeout", h.cfg.TerminateTimeout)
	}
}

// initialize は、initialize を送って応答を待ち、届いたら稼働中にして、開いているバッファの全文を送る。
func (h *Host) initialize(e *extension, r *run, p *process, stop <-chan struct{}) error {
	f, err := r.conn.Go(ext.MethodInitialize, ext.InitializeParams{
		APIVersion: ext.APIVersion, Settings: e.spec.Settings, NoBufferSync: !e.syncsBuffers(),
	})
	if err != nil {
		return fmt.Errorf("%w: %w", errConnClosed, err)
	}
	t := time.NewTimer(h.cfg.InitTimeout)
	defer t.Stop()
	select {
	case <-f.Ready():
	case <-t.C:
		_ = f.Cancel()
		return fmt.Errorf("%w after %s", errInitTimeout, h.cfg.InitTimeout)
	case <-p.exited:
		return p.exitError()
	case <-r.conn.Done():
		return connEnded(r, p)
	case <-r.failed:
		return r.failErr
	case <-h.ctx.Done():
		return ErrClosed
	case <-stop:
		return errStopped
	}
	var res ext.InitializeResult
	if err := f.Result(&res); err != nil {
		var rpcErr *rpc.Error
		if errors.As(err, &rpcErr) {
			return fmt.Errorf("exthost: initialize failed: %w", err)
		}
		return fmt.Errorf("%w: %w", errConnClosed, err)
	}
	id := e.spec.Manifest.ID
	if res.APIVersion != ext.APIVersion {
		// 食い違った場合の扱いは未決なので、記録だけして続ける（要件定義 7 章の、本体と拡張機能のバージョンが食い違った場合の扱い）。
		h.log.Warn("extension api version differs", "extension", id, "host", ext.APIVersion, "extension_api", res.APIVersion)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return ErrClosed
	}
	if e.stopping {
		// 初期化の応答を待つ間に Disable された。停止中から稼働中へ戻さない。
		return errStopped
	}
	r.runningSince = time.Now()
	h.setStateLocked(e, StateRunning, nil)
	for _, d := range h.sortedDocsLocked() {
		if !d.withheld() && e.syncsBuffers() {
			h.notifyLocked(id, r, ext.MethodBufferOpen, d.openParams())
		}
	}
	return nil
}

// monitor は、稼働中のプロセスへ定期的に ping を送り、異常を見つけたら理由を返す。
func (h *Host) monitor(r *run, p *process, stop <-chan struct{}) error {
	tick := time.NewTicker(h.cfg.PingInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if err := h.ping(r, p, stop); err != nil {
				return err
			}
		case <-p.exited:
			return p.exitError()
		case <-r.conn.Done():
			return connEnded(r, p)
		case <-r.failed:
			return r.failErr
		case <-h.ctx.Done():
			return ErrClosed
		case <-stop:
			return errStopped
		}
	}
}

// ping は、生存確認を 1 回行う。応答が期限までに届かなければ誤りを返す。
// 拡張機能が ping に誤りで応答しても、応答したので生きているとみなす。
func (h *Host) ping(r *run, p *process, stop <-chan struct{}) error {
	f, err := r.conn.Go(ext.MethodPing, ext.PingParams{})
	if err != nil {
		return fmt.Errorf("%w: %w", errConnClosed, err)
	}
	t := time.NewTimer(h.cfg.PingTimeout)
	defer t.Stop()
	select {
	case <-f.Ready():
		if err := f.Result(nil); err != nil && errors.Is(err, rpc.ErrClosed) {
			return fmt.Errorf("%w: %w", errConnClosed, err)
		}
		return nil
	case <-t.C:
		_ = f.Cancel()
		return fmt.Errorf("%w after %s", errPingTimeout, h.cfg.PingTimeout)
	case <-p.exited:
		return p.exitError()
	case <-r.conn.Done():
		return connEnded(r, p)
	case <-r.failed:
		return r.failErr
	case <-h.ctx.Done():
		return ErrClosed
	case <-stop:
		return errStopped
	}
}

// handler は、プロセス r の接続で、拡張機能から届いたリクエストと通知を受け取るハンドラーを作る。
// 読み取りの goroutine から呼ばれるので、Host.mu を短く持つだけで、イベントループを待たない。
func (h *Host) handler(e *extension, r *run) rpc.Handler {
	id := e.spec.Manifest.ID
	return rpc.HandlerFuncs{
		Request: func(ctx context.Context, req *rpc.Request, reply rpc.Replier) {
			if req.Method == ext.MethodEditorOpen {
				reply = h.closeWithheldBeforeReply(e, r, reply)
			}
			ev, rerr := requestEvent(ctx, id, req, reply)
			if rerr != nil {
				_ = reply(nil, rerr)
				return
			}
			h.mu.Lock()
			if r.shuttingDown {
				h.mu.Unlock()
				_ = reply(nil, &rpc.Error{Code: rpc.CodeInternalError, Message: req.Method + ": extension is shutting down"})
				return
			}
			err := h.pushLocked(ev, r)
			h.mu.Unlock()
			if err != nil {
				r.fail(err)
				_ = reply(nil, err)
			}
		},
		Notification: func(_ context.Context, n *rpc.Notification) {
			var ev Event
			switch n.Method {
			case ext.MethodDecorationSet:
				var p ext.DecorationSetParams
				if !h.decode(id, n, &p) {
					return
				}
				ev = &DecorationSet{Extension: id, Params: p}
			case ext.MethodMessageShow:
				var p ext.MessageShowParams
				if !h.decode(id, n, &p) {
					return
				}
				ev = &MessageShow{Extension: id, Params: p}
			case ext.MethodDiagnosticSet:
				var p ext.DiagnosticSetParams
				if !h.decode(id, n, &p) {
					return
				}
				ev = &DiagnosticSet{Extension: id, Params: p}
			case ext.MethodSignSet:
				var p ext.SignSetParams
				if !h.decode(id, n, &p) {
					return
				}
				ev = &SignSet{Extension: id, Params: p}
			case ext.MethodPopupShow:
				var p ext.PopupShowParams
				if !h.decode(id, n, &p) {
					return
				}
				ev = &PopupShow{Extension: id, Params: p}
			case ext.MethodOutputAppend:
				var p ext.OutputAppendParams
				if !h.decode(id, n, &p) {
					return
				}
				ev = &OutputAppend{Extension: id, Params: p}
			case ext.MethodStatusLineSet:
				var p ext.StatusLineSetParams
				if !h.decode(id, n, &p) {
					return
				}
				ev = &StatusLineSet{Extension: id, Params: p}
			case ext.MethodProgressReport:
				var p ext.ProgressReportParams
				if !h.decode(id, n, &p) {
					return
				}
				if err := p.Validate(); err != nil {
					h.log.Warn("invalid progress/report from extension", "extension", id, "error", err)
					return
				}
				ev = &ProgressReport{Extension: id, Params: p}
			case ext.MethodBandSet:
				var p ext.BandSetParams
				if !h.decode(id, n, &p) {
					return
				}
				if err := p.Validate(); err != nil {
					h.log.Warn("invalid band/set from extension", "extension", id, "error", err)
					return
				}
				ev = &BandSet{Extension: id, Params: p}
			case ext.MethodVirtualTextSet:
				var p ext.VirtualTextSetParams
				if !h.decode(id, n, &p) {
					return
				}
				if err := p.Validate(); err != nil {
					h.log.Warn("invalid virtualText/set from extension", "extension", id, "error", err)
					return
				}
				ev = &VirtualTextSet{Extension: id, Params: p}
			case ext.MethodViewUpdate:
				var p ext.ViewUpdateParams
				if !h.decode(id, n, &p) {
					return
				}
				if err := p.Validate(); err != nil {
					h.log.Warn("invalid view/update from extension", "extension", id, "error", err)
					return
				}
				ev = &ViewUpdate{Extension: id, Params: p}
			case ext.MethodViewClose:
				var p ext.ViewCloseParams
				if !h.decode(id, n, &p) {
					return
				}
				ev = &ViewClose{Extension: id, Params: p}
			case ext.MethodViewAppend:
				var p ext.ViewAppendParams
				if !h.decode(id, n, &p) {
					return
				}
				if err := p.Validate(); err != nil {
					h.log.Warn("invalid view/append from extension", "extension", id, "error", err)
					return
				}
				a := &ViewAppend{Extension: id, Params: p}
				h.mu.Lock()
				merged := h.mergeAppendLocked(a, r)
				h.mu.Unlock()
				if merged {
					return
				}
				ev = a
			case ext.MethodBufferResync:
				var p ext.BufferResyncParams
				if !h.decode(id, n, &p) {
					return
				}
				h.resync(e, r, p)
				return
			default:
				h.log.Debug("unknown notification from extension", "extension", id, "method", n.Method)
				return
			}
			h.mu.Lock()
			err := h.pushLocked(ev, r)
			h.mu.Unlock()
			if err != nil {
				r.fail(err)
			}
		},
	}
}

// closeWithheldBeforeReply は、editor/open の応答の関数 reply を包み、
// 開いたバッファが拡張機能へ送っていないもの（不正な UTF-8 を含むもの）なら、応答より先に要求した拡張機能へ buffer/close を送る。
//
// 拡張機能は、editor/open の応答を受け取った後に複製を待つ（ext/server の WaitBuffer）。
// 送っていないバッファの buffer/open は届かないので、代わりに buffer/close を同じ接続で先に送り、待ちを誤りで終わらせる（基本設計 5.3、6.7）。
// 拡張機能はそのバッファの複製を持っていないので、buffer/close で捨てるものは無い。
// 宣言ファイルの noBufferSync の拡張機能も、どのバッファの buffer/open も受け取らないので、同じく先に buffer/close を送る（#367）。
func (h *Host) closeWithheldBeforeReply(e *extension, r *run, reply rpc.Replier) rpc.Replier {
	return func(result any, err error) error {
		if res, ok := result.(ext.EditorOpenResult); ok && err == nil {
			h.mu.Lock()
			if d, open := h.docs[res.Buffer]; open && (d.withheld() || !e.syncsBuffers()) && e.run == r && e.state == StateRunning {
				h.notifyLocked(e.spec.Manifest.ID, r, ext.MethodBufferClose, ext.BufferCloseParams{Buffer: res.Buffer})
			}
			h.mu.Unlock()
		}
		return reply(result, err)
	}
}

// requestEvent は、拡張機能 id から届いたリクエストを、イベントループへ渡すイベントにする。
// ctx は、rpc のハンドラーに渡された ctx で、取り消しと接続の終了と応答で終わる。イベントの Done にする。
// 知らないメソッドか、読めない params なら、応答に使う誤りを返す。
func requestEvent(ctx context.Context, id string, req *rpc.Request, reply rpc.Replier) (Event, *rpc.Error) {
	ro := replyOnce{reply: reply, done: ctx.Done()}
	switch req.Method {
	case ext.MethodBufferEdit:
		var p ext.BufferEditParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		return &EditRequest{Extension: id, Params: p, reply: reply, done: ctx.Done()}, nil
	case ext.MethodEditorState:
		var p ext.EditorStateParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		return &EditorStateRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodEditorOpen:
		var p ext.EditorOpenParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		return &EditorOpenRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodBufferSave:
		var p ext.BufferSaveParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		return &BufferSaveRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodDiagnosticList:
		var p ext.DiagnosticListParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		return &DiagnosticListRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodChoiceAsk:
		var p ext.ChoiceAskParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		return &ChoiceAskRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodInputAsk:
		var p ext.InputAskParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		return &InputAskRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodPopupOpen:
		var p ext.PopupOpenParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		return &PopupOpenRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodBufferCreate:
		var p ext.BufferCreateParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		return &BufferCreateRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodBufferUpdate:
		var p ext.BufferUpdateParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		return &BufferUpdateRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodBufferDispose:
		var p ext.BufferDisposeParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		return &BufferDisposeRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodDiffStart:
		var p ext.DiffStartParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		return &DiffStartRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodPaneOpen:
		var p ext.PaneOpenParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		return &PaneOpenRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodEditorList:
		var p ext.EditorListParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		return &EditorListRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodEditorExecute:
		var p ext.EditorExecuteParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		return &EditorExecuteRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodOptionSet:
		var p ext.OptionSetParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		return &OptionSetRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodOptionGet:
		var p ext.OptionGetParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		return &OptionGetRequest{Extension: id, Params: p, replyOnce: ro}, nil
	case ext.MethodRegisterSet:
		var p ext.RegisterSetParams
		if err := decodeRequest(req, &p); err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
		}
		return &RegisterSetRequest{Extension: id, Params: p, replyOnce: ro}, nil
	}
	return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "method not found: " + req.Method}
}

// decodeRequest は、リクエストの params を v へ読み込む。params が無ければ v を変えない。
func decodeRequest(req *rpc.Request, v any) *rpc.Error {
	if len(req.Params) == 0 || string(req.Params) == "null" {
		return nil
	}
	if err := json.Unmarshal(req.Params, v); err != nil {
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: req.Method + ": " + err.Error()}
	}
	return nil
}

// decode は、通知の params を v へ読み込む。読めなければ記録して false を返す。
func (h *Host) decode(id string, n *rpc.Notification, v any) bool {
	if err := json.Unmarshal(n.Params, v); err != nil {
		h.log.Warn("invalid notification params from extension", "extension", id, "method", n.Method, "error", err)
		return false
	}
	return true
}

// logStderr は、拡張機能の標準エラー出力を 1 行ずつ記録する。読み取りが終わるまで戻らない。
func (h *Host) logStderr(id string, r io.Reader) {
	br := bufio.NewReaderSize(r, maxStderrLine)
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			if line[len(line)-1] == '\n' {
				line = line[:len(line)-1]
			}
			h.log.Info("extension stderr", "extension", id, "line", string(line))
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return
		}
	}
}

// process は、起動した子プロセスと、本体の側のパイプの端である。
type process struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	stderr *os.File
	// exited は、子プロセスが終わり、cmd.Wait が戻ると閉じる。waitErr はその後に読む。
	exited  chan struct{}
	waitErr error
}

// startProcess は、path の実行ファイルを、引数 args で、新しいプロセスグループの子プロセスとして起動する。
// 標準入出力は os.Pipe でつなぎ、子の側の端は起動後に閉じる。
// dir（宣言ファイルのディレクトリ。空でもよい）は、絶対パスにして環境変数 ext.EnvExtensionDir で渡す。
// env（"名前=値" の列。[Config.Env]）は、本体の環境の後、ext.EnvExtensionDir の前に足す。
// extra は、子プロセスへファイル記述子 ext.SocketFDStart から引き継ぐファイルで、その数を ext.EnvSocketFDs で渡す（基本設計 6.2）。
// extra が無ければ ext.EnvSocketFDs を空の値にする。
// memLimit が 0 でなければ、起動した直後の子プロセスへ prlimit で RLIMIT_AS を memLimit バイトに掛ける（FR-20。[Config.MemoryLimitMB]）。
// 掛けられなければ、子プロセスのグループを強制終了して誤りを返す（上限の無いまま動かさない）。
func startProcess(path string, args []string, dir string, env []string, extra []*os.File, memLimit uint64) (p *process, err error) {
	var opened []*os.File
	defer func() {
		if err != nil {
			for _, f := range opened {
				_ = f.Close()
			}
		}
	}()
	pipe := func() (r, w *os.File, err error) {
		r, w, err = os.Pipe()
		if err == nil {
			opened = append(opened, r, w)
		}
		return r, w, err
	}
	inR, inW, err := pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := pipe()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(path, args...) //nolint:gosec // 宣言ファイルに書かれた実行ファイルと引数を起動するのが、このパッケージの役目である。
	// 作業ディレクトリは本体のものを引き継ぎ、宣言ファイルのディレクトリは環境変数で渡す（基本設計 5.1）。
	// 本体の環境に同じ名前の変数があっても、後に足した値が使われる（os/exec は重複した名前の最後の値を使う）。
	if dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	cmd.Env = append(append(os.Environ(), env...), ext.EnvExtensionDir+"="+dir, socketEnv(len(extra)))
	cmd.ExtraFiles = extra
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = errW
	// 孫プロセスまでまとめて強制終了できるように、子プロセスを新しいプロセスグループの先頭にする。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exthost: start %s: %w", path, err)
	}
	_ = inR.Close()
	_ = outW.Close()
	_ = errW.Close()
	// 子プロセスの回収（cmd.Wait）より前に掛けるので、pid は他のプロセスに使い回されていない。
	// 子プロセスが既に終わっていても、回収するまではゾンビとして残り、prlimit は成功する。
	if memLimit > 0 {
		if err := limitAddressSpace(cmd.Process.Pid, memLimit); err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return nil, fmt.Errorf("exthost: set RLIMIT_AS of %s: %w", path, err)
		}
	}

	p = &process{cmd: cmd, stdin: inW, stdout: outR, stderr: errR, exited: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

// kill は、子プロセスのプロセスグループ全体に SIGKILL を送る（基本設計 5.2 の強制終了）。
// 子プロセスが終わった後でも、同じグループに残った孫プロセスを終わらせる。
// 孫プロセスがパイプを持ち続けると、子を終わらせても接続が閉じないためである。
func (p *process) kill() {
	p.signal(syscall.SIGKILL)
}

// signal は、子プロセスのプロセスグループ全体に sig を送る。グループへ送れなければ子プロセスだけに送る。
func (p *process) signal(sig syscall.Signal) {
	pid := p.cmd.Process.Pid
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = p.cmd.Process.Signal(sig)
	}
}

// exitWaitAfterEOF は、接続が閉じた後に、子プロセスの終了を待つ上限である。
// 子プロセスが終わると、終了コードより先に標準出力の EOF が届くことが多いので、異常の理由を終了コードにするために少し待つ。
const exitWaitAfterEOF = 200 * time.Millisecond

// connEnded は、接続が閉じたときの異常の理由を返す。子プロセスがすぐに終われば、その終了の理由にする。
func connEnded(r *run, p *process) error {
	t := time.NewTimer(exitWaitAfterEOF)
	defer t.Stop()
	select {
	case <-p.exited:
		return fmt.Errorf("%w (%w)", p.exitError(), r.conn.Err())
	case <-t.C:
		return fmt.Errorf("%w: %w", errConnClosed, r.conn.Err())
	}
}

// exitError は、子プロセスが終わったことを表す誤りを返す。exited が閉じた後に呼ぶ。
func (p *process) exitError() error {
	if p.waitErr != nil {
		return fmt.Errorf("%w: %w", errExited, p.waitErr)
	}
	return fmt.Errorf("%w: exit status 0", errExited)
}
