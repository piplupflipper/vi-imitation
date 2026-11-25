package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/piplupflipper/vi-imitation/ext/expand"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// clientAddrPlaceholder は、デバッグアダプターの起動の方法の中で、拡張機能が待ち受けるソケットのアドレスに置き換える変数である
// （つなぎ方 clientSocket。transport.go）。
//
// delve の dlv dap は TCP かソケットでしか DAP を話さず、--client-addr で指定したアドレスへ自分から接続する
// （https://github.com/go-delve/delve/blob/master/Documentation/usage/dlv_dap.md）。
// アドレスは "unix:" に続けてソケットのパスを書く形で、dlv の --client-addr の書き方に合わせる。
const clientAddrPlaceholder = "${clientAddr}"

// outputChannel は、output/append で書き足す欄の名前である（基本設計 6.4）。
const outputChannel = "dap"

// session は、1 つのデバッグのセッションである。デバッグアダプターとの DAP の接続と、拡張機能が起動したならその子プロセスを持つ。
//
// 拡張機能はセッションを同時に複数持てる（FR-285）。最上位のセッションは debug.continue などで始め、
// 子のセッションは、アダプターからの逆向きの request の startDebugging で始める（startChild）。
// 子のセッションは親の下に置き（parent と children）、親を終えるときは、子を先に終える（stop と run の stopChildren）。
//
// 開始の手順（run）は DAP の仕様の Overview の Launch Sequencing に従う。
// initialize の response を受け取った後に launch か attach を送り、initialized の event を待って、
// ブレークポイントを setBreakpoints で送り、configurationDone を送ってから launch か attach の response を待つ。
// デバッグアダプターは configurationDone の response の後に launch に応答してよい（同 Overview）ので、
// launch の response を待ってから設定を送ると詰まる。
//
// 終え方（stop）は、endAs と、アダプターを拡張機能が起動したか（cmd が nil でないか）で分ける（FR-284。基本設計 6.4 の「終了と切断」）。
//
// 止まっているか、選んでいるスレッドとフレーム、実行位置、setBreakpoints を送り始めたかは、セッションごとに持ち、debugger.mu で守る。
type session struct {
	d *debugger
	// id は、拡張機能のプロセスの中でセッションを区別する番号である（1 から）。addSessionLocked が振り、以後は変えない。
	id int
	// parent は、子のセッションの親である。最上位のセッションでは nil。作った後は変えない。
	parent *session
	// spec は、このセッションの構成とアダプターの起動の方法である。書き換えない。
	spec *runSpec
	// procMu は、proc を守る。serverPort でポートを選び直すとき（transport.go の respawn）に、proc を替えるためである。
	procMu sync.Mutex
	// proc は、アダプターの子プロセス（拡張機能が起動したもの）か、既に動いているアダプターへの接続の終わりである。nil にしない。
	// serverPort の子のセッションは、親と同じ proc を使う（adapterProc の users）。
	proc *adapterProc
	// released は、proc の使い手から抜けたこと（killWithin）を表す。
	released atomic.Bool
	// respawn は、serverPort で、アダプターが待ち受ける前に終わったときに、ポートを選び直して 1 回だけ起動し直す関数である。
	// ほかのつなぎ方と、起動し直した後と、子のセッションでは nil。
	respawn func() error
	// ln は、clientSocket で待ち受けるソケットである。ほかのつなぎ方では nil。
	ln      *net.UnixListener
	sockDir string
	// dial は、TCP でつなぐ先（serverPort、server、connectFrom）である。ほかのつなぎ方では nil。
	dial *endpoint
	// stdio は、標準入出力で話すときの、子プロセスの標準入力への書き込み口と標準出力の読み取り口である。
	stdinW  *os.File
	stdoutR *os.File

	conn   *dapConn
	connMu sync.Mutex

	events *eventQueue
	// initialized は、initialized の event を受け取ると閉じる。
	initialized     chan struct{}
	initializedOnce sync.Once
	// caps は、initialize の応答の capabilities である。stop が別の goroutine から読むので atomic にする。
	caps atomic.Pointer[capabilities]
	// terminatedCh は、terminated の event を受け取ると閉じる。
	terminatedCh   chan struct{}
	terminatedOnce sync.Once
	// stopping は、利用者が debug.terminate か debug.restart で終わらせたか、親のセッションか拡張機能が終わるところであることを表す。
	stopping atomic.Bool
	// terminated は、terminated の event を受け取ったことを表す。
	terminated atomic.Bool
	// ended は、後始末が終わると閉じる。
	ended chan struct{}
	// eventCtx は、event の処理（stopped）と接続の試し直しが本体とデバッグアダプターへ送る request の ctx の親である。
	// stop の始めと後始末の終わりに cancelEvents で取り消す。止める間に event の処理が本体の応答を待ち続けると、
	// その後ろに並んだ terminated の event を処理できず、stop が期限まで待つからである。
	// 本体から shutdown を受け取った後は、Server のループが止まるので、WaitBuffer などは応答しない。
	eventCtx     context.Context
	cancelEvents context.CancelFunc
	// gen は、遅れて届いた応答を捨てるための世代の番号である（FR-287。基本設計 6.4 の「セッションと状態の分離」）。
	// 止まった、続けた、続行とステップ実行と一時停止を送った、スレッドかフレームかセッションを選び直した、のたびに 1 つ進める。
	gen atomic.Uint64
	// output は、このセッションのデバッグ対象の出力の写しである（基本設計 6.4。後の issue のデバッグのペインの出力の区画が使う）。
	output *server.TextLog
	// reply は、子のセッションで、親が受けた startDebugging へ応答する関数である。最上位のセッションでは nil。
	// replyOnce で 1 回だけ呼ぶ（replyStart）。
	reply     func(error)
	replyOnce sync.Once
	// condWarned は、条件付きのブレークポイントに対応していないことを知らせたことを表す（FR-286）。
	condWarned atomic.Bool

	// 以下は debugger.mu で守る。

	// children は、動いている子のセッションである。
	children []*session
	// closing は、子のセッションを終え始めたこと（stopChildren）を表す。これより後の startDebugging は断る。
	closing bool
	// configured は、初期化で setBreakpoints を送る対象を決め終えたことを表す。これより後に変わったブレークポイントは、その都度送る。
	configured bool
	// halted は、デバッグ対象が止まっている（stopped の後、続行かステップ実行か continued の前）ことを表す。
	halted bool
	// exec は、止まった位置である。止まっていないか、位置が分からなければ nil。
	exec *execPoint
	// thread は、選んでいるスレッドの ID である（最後に止まったスレッド）。hasThread が false なら未知。
	// vscode-js-debug はスレッドの ID に 0 を使うので、0 を未知の印にしない（2026-11-25 に確かめた）。
	thread    int
	hasThread bool
	// frame は、選んでいるフレームの ID である（止まったスレッドの先頭のフレーム）。hasFrame が false なら未知。
	frame    int
	hasFrame bool
}

// linesStartAt1 は、initialize で送る linesStartAt1 と columnsStartAt1 である。
// DAP の既定に合わせて 1 から数え、受け取った値は fromDAPLine と utf16ColumnToByte で本体の位置へ変換する。
const linesStartAt1 = true

// newSession は、spec のセッションを作る。アダプターの起動と接続は spawn と run が行う。
func newSession(d *debugger, spec *runSpec) *session {
	s := &session{
		d:            d,
		spec:         spec,
		events:       newEventQueue(),
		initialized:  make(chan struct{}),
		terminatedCh: make(chan struct{}),
		ended:        make(chan struct{}),
		proc:         newAdapterProc(nil),
		output:       server.NewTextLog(d.srv),
	}
	s.eventCtx, s.cancelEvents = context.WithCancel(context.Background())
	return s
}

// label は、利用者へ知らせる文の中でのセッションの名前である。
func (s *session) label() string { return s.spec.name }

// adapterProc は、アダプターの子プロセス 1 つか、既に動いているアダプターへの接続である。
type adapterProc struct {
	// cmd は、拡張機能が起動したプロセス（見張り。guard_linux.go）である。既に動いているアダプターへ繋ぐときは nil。
	cmd *exec.Cmd
	// done は、プロセスが終わると閉じる。cmd が nil なら、接続が終わると閉じる。
	done chan struct{}
	once sync.Once
	// users は、このプロセスを使うセッションの数である。serverPort の子のセッションは、親のプロセスに繋ぐので増える。
	// 最後の使い手が抜けたときに止める（基本設計 6.4 の「serverPort のアダプターは、そのアダプターに繋いだすべてのセッションを終えた後に止める」）。
	mu    sync.Mutex
	users int
}

func newAdapterProc(cmd *exec.Cmd) *adapterProc {
	return &adapterProc{cmd: cmd, done: make(chan struct{}), users: 1}
}

// markDone は、done を閉じる。2 回目からは何もしない。
func (p *adapterProc) markDone() { p.once.Do(func() { close(p.done) }) }

// acquire は、使い手を 1 つ足す。
func (p *adapterProc) acquire() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users++
}

// release は、使い手を 1 つ減らし、使い手が残っていないかを返す。
func (p *adapterProc) release() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.users > 0 {
		p.users--
	}
	return p.users == 0
}

// idle は、使い手が残っていないかを返す。
func (p *adapterProc) idle() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.users == 0
}

// getProc は、今のアダプターのプロセスを返す。
func (s *session) getProc() *adapterProc {
	s.procMu.Lock()
	defer s.procMu.Unlock()
	return s.proc
}

// procDone は、今のアダプターのプロセスが終わると閉じる通り道を返す。
func (s *session) procDone() <-chan struct{} { return s.getProc().done }

// owned は、拡張機能がアダプターを起動したか（終えた後に終わらなければ止めるか）を返す（FR-284）。
func (s *session) owned() bool { return s.getProc().cmd != nil }

// launched は、デバッグ対象を launch で始めたものとして終えるか（終えるときに終わらせるか）を返す（FR-284）。
// 子のセッションは親の request で決める（runSpec の endAs）。
func (s *session) launched() bool { return s.spec.endAs == "launch" }

// markProcDone は、今のプロセスの done を閉じる。2 回目からは何もしない。
func (s *session) markProcDone() { s.getProc().markDone() }

// forward は、r から読んだテキストを、塊ごとに f へ渡す。r が終わるまで続ける。
func (s *session) forward(r io.ReadCloser, f func(string)) {
	defer r.Close() //nolint:errcheck // 読み取りの口を閉じるだけで、失敗しても失うデータが無い
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			f(string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

func (s *session) closeSocket() {
	if s.ln != nil {
		_ = s.ln.Close()
	}
	if s.sockDir != "" {
		_ = os.RemoveAll(s.sockDir)
	}
}

func (s *session) getConn() *dapConn {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn
}

// connect は、DAP の接続を作る。clientSocket ではアダプターからの接続を、serverPort ではアダプターが待ち受けるまでを、timeout まで待つ。
// 拡張機能が起動していないアダプターでは、接続が終わったら procDone を閉じる（接続に失敗したらすぐ閉じる）。
func (s *session) connect(timeout time.Duration) (err error) {
	if !s.owned() {
		defer func() {
			if err != nil {
				s.markProcDone()
			}
		}()
	}
	onEvent := func(event string, body json.RawMessage) { s.events.push(dapEvent{name: event, body: body}) }
	onRequest := func(seq int, command string, args json.RawMessage) bool {
		// 読み取りの goroutine から呼ばれるので、event の列へ積むだけにする。応答は、startDebugging なら startChild が、
		// ほかは dapConn がすぐに失敗で返す。
		s.events.push(dapEvent{name: reverseRequestEvent + command, body: args, seq: seq})
		return command == "startDebugging"
	}
	var c *dapConn
	switch {
	case s.ln != nil:
		if err := s.ln.SetDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
		nc, err := s.ln.Accept()
		s.closeSocket()
		if err != nil {
			return fmt.Errorf("accept connection from adapter: %w", err)
		}
		c = newDAPConn(nc, nc, nc, onEvent, onRequest)
	case s.dial != nil:
		nc, err := s.dialAdapter(timeout)
		if err != nil {
			return err
		}
		c = newDAPConn(nc, nc, nc, onEvent, onRequest)
	default:
		c = newDAPConn(s.stdoutR, s.stdinW, multiCloser{s.stdinW, s.stdoutR}, onEvent, onRequest)
	}
	s.connMu.Lock()
	s.conn = c
	s.connMu.Unlock()
	if !s.owned() {
		go func() {
			<-c.doneCh()
			s.markProcDone()
		}()
	}
	select {
	case <-s.procDone():
		// 子プロセスが接続の前に終わっていたら、後始末の goroutine は接続を閉じられないので、ここで閉じる。
		c.close()
	default:
	}
	return nil
}

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var errs []error
	for _, c := range m {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// run は、DAP の初期化から終わりまでを行う。spawn（子のセッションは spawnChild）の後に別の goroutine で呼ぶ。
func (s *session) run() {
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		s.handleEvents()
	}()
	err := s.start()
	s.replyStart(err)
	switch {
	case err != nil && s.stopping.Load():
		// 始める途中で終えた（debug.terminate、親のセッションの終わり）。ログにだけ書く。
		s.d.logf("session %d: start interrupted by stop: %v", s.id, err)
		s.kill()
	case err != nil && s.parent != nil:
		s.d.showError(fmt.Sprintf("子のセッション %s を開始できない: %v", s.label(), err))
		s.kill()
	case err != nil:
		s.d.showError("デバッグを開始できない: " + err.Error())
		s.kill()
	case s.stopping.Load():
	case s.parent != nil:
		s.d.logf("session %d: child session %q started (%s)", s.id, s.label(), s.spec.request)
	default:
		s.d.showInfo(fmt.Sprintf("デバッグを開始した（%s、%s）", s.spec.name, s.spec.request))
	}
	if c := s.getConn(); c != nil {
		<-c.doneCh()
	}
	// 親のセッションが終わったら、子のセッションも終える（FR-285）。stop から来たときは、子は既に終えてある。
	s.stopChildren(context.Background(), killGrace)
	if s.spec.adapter.transport != transportServerPort || !s.owned() {
		// serverPort のアダプターは、接続が終わっても次の接続を待ち続けるので、待たずに止める（ほかに使い手が無ければ）。
		<-s.procDoneOrTimeout()
	}
	s.kill()
	s.events.close()
	<-handled
	abnormal := err == nil && !s.stopping.Load() && !s.terminated.Load()
	s.d.sessionEnded(s, abnormal, err != nil)
	s.cancelEvents()
	close(s.ended)
}

// replyStart は、子のセッションで、親が受けた startDebugging へ応答する。err が nil なら受け付けたことを、そうでなければ失敗を返す。
// 最上位のセッションでは何もしない。2 回目からは何もしない。
func (s *session) replyStart(err error) {
	if s.reply == nil {
		return
	}
	s.replyOnce.Do(func() { s.reply(err) })
}

// procDoneOrTimeout は、子プロセスが終わるか、stop の期限が過ぎると閉じる通り道を返す。
func (s *session) procDoneOrTimeout() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		select {
		case <-s.procDone():
		case <-time.After(s.d.timeouts.stop):
		}
		close(ch)
	}()
	return ch
}

// start は、DAP の初期化の手順を行う。
// initialize で申し出る client の能力は、linesStartAt1 と columnsStartAt1 が true、pathFormat が path、supportsVariableType が true、
// supportsStartDebuggingRequest が true（FR-285）である。supportsRunInTerminalRequest は申し出ない（FR-288）。
// launch か attach の引数は、決めた構成を JSON の値にしたもの（type、request、name を含む）である。
// 子のセッションは、startDebugging の configuration をそのまま送る（変数の展開も resolveCommand もしない。基本設計 6.4）。
// 子のセッションは、接続ができた時点で、親が受けた startDebugging へ受け付けたことを応答する。
//
// req: FR-280, FR-285, FR-288
func (s *session) start() error {
	t := s.d.timeouts
	if err := s.connect(t.initialize); err != nil {
		return err
	}
	s.replyStart(nil)
	c := s.getConn()
	ctx, cancel := context.WithTimeout(context.Background(), t.initialize)
	defer cancel()
	var caps capabilities
	err := c.call(ctx, "initialize", initializeArguments{
		ClientID:                      "vi-imitation",
		ClientName:                    "vi-imitation",
		AdapterID:                     s.spec.adapter.adapterID,
		LinesStartAt1:                 linesStartAt1,
		ColumnsStartAt1:               linesStartAt1,
		PathFormat:                    "path",
		SupportsVariableType:          true,
		SupportsStartDebuggingRequest: true,
	}, &caps)
	if err != nil {
		return err
	}
	s.caps.Store(&caps)

	launchCtx, cancelLaunch := context.WithTimeout(context.Background(), t.launch)
	defer cancelLaunch()
	launched := make(chan error, 1)
	args := expand.Plain(s.spec.config)
	go func() { launched <- c.call(launchCtx, s.spec.request, args, nil) }()

	select {
	case <-s.initialized:
	case err := <-launched:
		// launch に失敗の response が返ったか、接続が終わった。
		if err != nil {
			return err
		}
		// launch の response が initialized より先に届いた。initialized を待ち続ける。
		launched <- nil
		select {
		case <-s.initialized:
		case <-launchCtx.Done():
			return errors.New("initialized event did not arrive")
		}
	case <-launchCtx.Done():
		return errors.New("initialized event did not arrive")
	}

	for _, path := range s.d.configure(s) {
		if err := s.sendBreakpoints(launchCtx, path); err != nil {
			s.d.logf("setBreakpoints %s: %v", path, err)
		}
	}
	if caps.SupportsConfigurationDoneRequest {
		if err := c.call(launchCtx, "configurationDone", nil, nil); err != nil {
			return err
		}
	}
	return <-launched
}

// reconfigure は、restart の後に届いた initialized に応えて、すべてのブレークポイントの setBreakpoints と、
// configurationDone（対応していれば）を送り直す（DAP の仕様の Launch Sequencing と同じ順）。
func (s *session) reconfigure() {
	ctx, cancel := context.WithTimeout(s.eventCtx, s.d.timeouts.launch)
	defer cancel()
	s.d.mu.Lock()
	paths := slices.Sorted(maps.Keys(s.d.bps))
	s.d.mu.Unlock()
	for _, path := range paths {
		if err := s.sendBreakpoints(ctx, path); err != nil {
			s.d.logf("setBreakpoints %s: %v", path, err)
		}
	}
	if c := s.getConn(); c != nil && s.supports(func(c *capabilities) bool { return c.SupportsConfigurationDoneRequest }) {
		if err := c.call(ctx, "configurationDone", nil, nil); err != nil {
			s.d.logf("configurationDone after restart: %v", err)
		}
	}
}

// sendBreakpoints は、path のブレークポイントのすべてを setBreakpoints で送り、応答で印を付け直す。
// アダプターが条件付きのブレークポイントに対応していなければ（supportsConditionalBreakpoints が無ければ）、条件を外して送り、
// 対応していないことをセッションごとに 1 回メッセージの行に出す（FR-286）。
//
// req: FR-286
func (s *session) sendBreakpoints(ctx context.Context, path string) error {
	c := s.getConn()
	if c == nil {
		return errDAPClosed
	}
	bps, gen := s.d.snapshotBreakpoints(path)
	conditional := s.supports(func(c *capabilities) bool { return c.SupportsConditionalBreakpoints })
	args := setBreakpointsArguments{Source: source{Name: filepath.Base(path), Path: path}, Breakpoints: []sourceBreakpoint{}}
	dropped := false
	for _, b := range bps {
		sb := sourceBreakpoint{Line: toDAPLine(b.line, linesStartAt1), Condition: b.cond}
		if b.cond != "" && !conditional {
			sb.Condition = ""
			dropped = true
		}
		args.Breakpoints = append(args.Breakpoints, sb)
	}
	if dropped && s.condWarned.CompareAndSwap(false, true) {
		s.d.showError(fmt.Sprintf("アダプター %s は条件付きのブレークポイントに対応していないので、条件を外して送った", s.spec.adapter.name))
	}
	var res setBreakpointsResponse
	if err := c.call(ctx, "setBreakpoints", args, &res); err != nil {
		return err
	}
	if len(res.Breakpoints) != len(args.Breakpoints) {
		// 応答の breakpoints は、送った breakpoints と同じ順に並ぶ（仕様の SetBreakpoints Request）ので、数が合わない応答は印に使わない。
		// vscode-js-debug の子のセッションは、2 回目の initialized の後に送り直した setBreakpoints へ空の列を返す（2026-11-25 に確かめた）。
		s.d.logf("session %d: setBreakpoints %s: %d results for %d breakpoints; ignored", s.id, path, len(res.Breakpoints), len(args.Breakpoints))
		return nil
	}
	s.d.applyBreakpoints(s, path, gen, res.Breakpoints)
	return nil
}

// supports は、initialize の応答の capabilities が f を満たすかを返す。まだ応答を受けていなければ false。
func (s *session) supports(f func(*capabilities) bool) bool {
	c := s.caps.Load()
	return c != nil && f(c)
}

// errStale は、送った後に世代が進んだ（止まった、続けた、選び直した）ので、応答を表示に使わずに捨てたことを表す（FR-287）。
var errStale = errors.New("dap: stale response")

// callGen は、世代 gen のうちに送る request である。応答が届いたときに世代が進んでいれば、result を使わせずに errStale を返す。
// stackTrace（と、後の issue の scopes、variables、evaluate）は、これで送る。
//
// req: FR-287
func (s *session) callGen(ctx context.Context, gen uint64, command string, args, result any) error {
	c := s.getConn()
	if c == nil {
		return errDAPClosed
	}
	if err := c.call(ctx, command, args, result); err != nil {
		return err
	}
	if s.gen.Load() != gen {
		return errStale
	}
	return nil
}

// threadID は、続行やステップ実行の対象のスレッドを返す。選んでいるスレッドが無ければ threads で問い合わせ、最初のスレッドにする。
func (s *session) threadID(ctx context.Context) (int, error) {
	s.d.mu.Lock()
	id, ok := s.thread, s.hasThread
	s.d.mu.Unlock()
	if ok {
		return id, nil
	}
	c := s.getConn()
	if c == nil {
		return 0, errDAPClosed
	}
	var res threadsResponse
	if err := c.call(ctx, "threads", nil, &res); err != nil {
		return 0, err
	}
	if len(res.Threads) == 0 {
		return 0, errors.New("no threads")
	}
	return res.Threads[0].ID, nil
}

// resume は、command（continue、next、stepIn、stepOut）を、選んでいるスレッドへ送る。送る前に世代を進める（FR-287）。
func (s *session) resume(ctx context.Context, command string) error {
	c := s.getConn()
	if c == nil {
		return errDAPClosed
	}
	tid, err := s.threadID(ctx)
	if err != nil {
		return err
	}
	s.gen.Add(1)
	if err := c.call(ctx, command, threadArguments{ThreadID: tid}, nil); err != nil {
		return err
	}
	s.d.mu.Lock()
	s.halted = false
	s.d.mu.Unlock()
	return nil
}

// pause は、DAP の pause を、選んでいるスレッド（選んでいなければ threads の最初のスレッド）へ送る（基本設計 6.4）。
// delve は、pause の threadId に依らずにデバッグ対象の全体を止める。
func (s *session) pause(ctx context.Context) error {
	c := s.getConn()
	if c == nil {
		return errDAPClosed
	}
	tid, err := s.threadID(ctx)
	if err != nil {
		tid = 1
	}
	s.gen.Add(1)
	return c.call(ctx, "pause", threadArguments{ThreadID: tid}, nil)
}

// stop は、デバッグを終える（FR-284）。後始末が終わるまで待つ。子のセッションがあれば、先に子を終える（FR-285）。
//
//   - launch で始めたなら、デバッグアダプターが supportsTerminateRequest を持てば先に terminate を送り、
//     デバッグ対象を穏やかに終わらせる（DAP の仕様の Overview の Debug Session End）。続けて terminateDebuggee を true にした disconnect を送る
//   - attach で繋いだなら、terminate を送らず、terminateDebuggee を false にした disconnect を送る。デバッグ対象は動き続ける
//   - 拡張機能が起動したアダプターは、期限までに終わらなければ強制終了する（kill）。既に動いていたアダプターは、接続を閉じるだけで止めない
//
// 子のセッションの launch か attach かは、親の request で決める（runSpec の endAs）。
// 手順ごとの期限は timeouts.stop である。ctx が終わると、残りの手順を打ち切って強制終了する。
// grace は、強制終了で SIGTERM を送ってから SIGKILL を送るまでの猶予である。
// ctx が終わった後に stop が戻るまでは、最長で grace に少し足した時間である（子のセッションがあれば、その分も足す）。
// ctx が終わって強制終了したときは、run の後始末（sessionEnded）を待たずに戻る。
//
// req: FR-284, FR-285
func (s *session) stop(ctx context.Context, grace time.Duration) {
	s.stopping.Store(true)
	s.cancelEvents()
	s.stopChildren(ctx, grace)
	s.terminate(ctx)
	s.disconnect(ctx, grace)
	wait := time.NewTimer(2 * s.d.timeouts.stop)
	defer wait.Stop()
	select {
	case <-s.ended:
	case <-wait.C:
		s.killWithin(grace)
	case <-ctx.Done():
		s.killWithin(grace)
	}
}

// stopChildren は、子のセッションをすべて並べて終え（stop）、終わるまで待つ。これより後の startDebugging は断る。
func (s *session) stopChildren(ctx context.Context, grace time.Duration) {
	s.d.mu.Lock()
	s.closing = true
	children := slices.Clone(s.children)
	s.d.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range children {
		wg.Go(func() { c.stop(ctx, grace) })
	}
	wg.Wait()
}

// terminate は、launch で始めたセッションで、デバッグアダプターが対応していれば terminate を送り、
// terminated の event か子プロセスの終了を、timeouts.stop と ctx の早い方まで待つ。
// supportsTerminateRequest が無ければ送らない（FR-286）。
//
// req: FR-286
func (s *session) terminate(ctx context.Context) {
	c := s.getConn()
	if c == nil || !s.launched() || !s.supports(func(c *capabilities) bool { return c.SupportsTerminateRequest }) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, s.d.timeouts.stop)
	defer cancel()
	if err := c.call(ctx, "terminate", nil, nil); err != nil {
		s.d.logf("terminate: %v", err)
		return
	}
	select {
	case <-s.terminatedCh:
	case <-s.procDone():
	case <-ctx.Done():
	}
}

// disconnect は、disconnect を送る。terminateDebuggee は launch で始めたときだけ true にする（FR-284）。
// 拡張機能が起動したアダプターなら、子プロセスが終わるのを timeouts.stop と ctx の早い方まで待ち、終わらなければ猶予 grace で強制終了する。
// serverPort のアダプターは次の接続を待ち続けるので、待たずに止める（ほかのセッションが同じアダプターに繋いでいれば止めない）。
// 既に動いていたアダプターなら、接続を閉じるだけで止めない。
//
// attach で繋いだデバッグ対象が止まっていれば、disconnect の前に continue を送って動かす。
// DAP の仕様の DisconnectArguments の suspendDebuggee は「If unspecified, the debuggee should resume execution」だが、
// dlv 1.27.2 の複数の client を受けるサーバー（--accept-multiclient）は、disconnect でデバッグ対象を止まったまま残す
// （service/dap/server.go の onDisconnectRequest の「The target is left in whatever state it is already in」。
// #408 のレビューの推奨 3 で確かめた）。suspendDebuggee も false で明示する。
//
// req: FR-284
func (s *session) disconnect(ctx context.Context, grace time.Duration) {
	if c := s.getConn(); c != nil {
		if !s.launched() {
			s.releaseAttached(ctx, c)
		}
		cctx, cancel := context.WithTimeout(ctx, s.d.timeouts.stop)
		err := c.call(cctx, "disconnect", disconnectArguments{TerminateDebuggee: s.launched()}, nil)
		cancel()
		if err != nil && !errors.Is(err, errDAPClosed) && !s.exited() {
			s.d.logf("disconnect: %v", err)
		}
		if !s.owned() {
			c.close()
			return
		}
	}
	if s.owned() && s.spec.adapter.transport == transportServerPort {
		s.killWithin(grace)
		return
	}
	wait := time.NewTimer(s.d.timeouts.stop)
	defer wait.Stop()
	select {
	case <-s.procDone():
	case <-wait.C:
		s.killWithin(grace)
	case <-ctx.Done():
		s.killWithin(grace)
	}
}

// releaseAttached は、attach で繋いだデバッグ対象を、切断の後も動き続けられるようにする（#408 のレビューの推奨 3）。
// 置いたブレークポイントを空の setBreakpoints で外し、止まっていれば continue を送る。
// dlv の複数の client を受けるサーバーは、切断の後もブレークポイントを残すので、外さずに続けると、次のブレークポイントでまた止まり、
// client の無いまま止まったままになる（2026-11-25 に dlv 1.27.2 の --headless --accept-multiclient で確かめた）。
// 拡張機能が持つブレークポイント（debugger.bps）は変えない。
func (s *session) releaseAttached(ctx context.Context, c *dapConn) {
	cctx, cancel := context.WithTimeout(ctx, s.d.timeouts.stop)
	defer cancel()
	s.d.mu.Lock()
	paths := slices.Sorted(maps.Keys(s.d.bps))
	halted := s.halted
	s.d.mu.Unlock()
	for _, path := range paths {
		args := setBreakpointsArguments{Source: source{Name: filepath.Base(path), Path: path}, Breakpoints: []sourceBreakpoint{}}
		if err := c.call(cctx, "setBreakpoints", args, nil); err != nil {
			s.d.logf("clear breakpoints before disconnect %s: %v", path, err)
		}
	}
	if halted {
		if err := s.resume(cctx, "continue"); err != nil {
			s.d.logf("continue before disconnect: %v", err)
		}
	}
}

// exited は、子プロセスが終わったかどうかを返す。
func (s *session) exited() bool {
	select {
	case <-s.procDone():
		return true
	default:
		return false
	}
}

// kill は、猶予を killGrace にして killWithin を呼ぶ。
func (s *session) kill() { s.killWithin(killGrace) }

// killWithin は、このセッションをアダプターの使い手から外し、ほかに使い手が無ければ、拡張機能が起動したアダプターの子プロセスに SIGTERM を送り、
// grace までに終わらなければ SIGKILL を送る。そのうえで接続を閉じる。
// 終わっていれば何もしない。既に動いていたアダプター（cmd が nil）は止めず、接続だけを閉じる。
// 使い手から外すのは 1 回だけで、2 回目からは、使い手が残っていなければ同じく止める（1 回目の猶予の後もまだ終わっていないときのため）。
// 先に SIGTERM を送るのは、見張り（guard_linux.go）がアダプターに後始末をさせてから子孫を止めるからである
// （dlv は SIGTERM でデバッグ対象を終わらせ、ビルドした実行ファイルを消す）。
// 見張りが SIGKILL で終わっても、アダプターは Pdeathsig の SIGTERM を受ける。
func (s *session) killWithin(grace time.Duration) {
	p := s.getProc()
	var idle bool
	if s.released.CompareAndSwap(false, true) {
		idle = p.release()
	} else {
		idle = p.idle()
	}
	if idle {
		select {
		case <-p.done:
		default:
			if p.cmd != nil && p.cmd.Process != nil {
				_ = p.cmd.Process.Signal(syscall.SIGTERM)
				t := time.NewTimer(grace)
				select {
				case <-p.done:
				case <-t.C:
					_ = p.cmd.Process.Kill()
				}
				t.Stop()
			}
		}
	}
	if c := s.getConn(); c != nil {
		c.close()
	}
	if p.cmd == nil {
		p.markDone()
	}
	s.closeSocket()
}

// killGrace は、kill で SIGTERM を送ってから SIGKILL を送るまでの猶予である。値は仮。
const killGrace = 2 * time.Second

// handleEvents は、デバッグアダプターからの event を届いた順に 1 件ずつ処理する。
// 読み取りの goroutine とは別の goroutine で動くので、stopped の処理で stackTrace の応答を待てる。
func (s *session) handleEvents() {
	for {
		e, ok := s.events.pop()
		if !ok {
			return
		}
		s.handleEvent(e)
	}
}

func (s *session) handleEvent(e dapEvent) {
	switch e.name {
	case "initialized":
		select {
		case <-s.initialized:
			// 2 回目の initialized は、restart の後にアダプターが設定を求め直したもの（dlv 1.27.2 の onRestartRequest は、
			// 始め直した後に initialized を送る）。ブレークポイントと configurationDone を送り直す。
			go s.reconfigure()
		default:
			s.initializedOnce.Do(func() { close(s.initialized) })
		}
	case "stopped":
		var body stoppedEvent
		if err := json.Unmarshal(e.body, &body); err != nil {
			s.d.logf("stopped event: %v", err)
			return
		}
		s.stopped(&body)
	case "continued":
		s.d.continued(s)
	case "breakpoint":
		var body breakpointEvent
		if err := json.Unmarshal(e.body, &body); err != nil {
			s.d.logf("breakpoint event: %v", err)
			return
		}
		if body.Reason == "changed" && body.Breakpoint.ID != 0 {
			s.d.breakpointChanged(s, body.Breakpoint)
		}
	case "exited":
		var body exitedEvent
		if err := json.Unmarshal(e.body, &body); err == nil {
			if s.parent != nil {
				s.d.showInfo(fmt.Sprintf("デバッグ対象が終了した（%s、終了コード %d）", s.label(), body.ExitCode))
			} else {
				s.d.showInfo(fmt.Sprintf("デバッグ対象が終了した（終了コード %d）", body.ExitCode))
			}
		}
	case "terminated":
		s.terminated.Store(true)
		s.terminatedOnce.Do(func() { close(s.terminatedCh) })
		s.d.clearExecution(s)
		// デバッグアダプターがセッションを終えた。子のセッションを終えてから、disconnect を送ってデバッグアダプターを終わらせる。
		// debug.terminate で終わらせている最中なら、stop が disconnect を送るので、ここでは送らない（二重に送ると、閉じた接続への書き込みになる）。
		if !s.stopping.Load() {
			go func() {
				s.stopChildren(context.Background(), killGrace)
				s.disconnect(context.Background(), killGrace)
			}()
		}
	case "output":
		var body outputEvent
		if err := json.Unmarshal(e.body, &body); err != nil {
			s.d.logf("output event: %v", err)
			return
		}
		// telemetry は利用者に見せずにテレメトリへ送る種類である（仕様の OutputEvent の category）。
		if body.Category == "telemetry" {
			return
		}
		s.appendOutput(body.Output)
	default:
		if command, ok := strings.CutPrefix(e.name, reverseRequestEvent); ok {
			if command == "startDebugging" {
				// 子のセッションの接続は待ちを含むので、event の処理を止めないよう別の goroutine で行う。
				go s.startChild(e.seq, e.body)
				return
			}
			// 対応しない逆向きの request（dapConn が失敗で応答した）。
			s.d.logf("adapter request %s is not supported: %s", command, e.body)
			return
		}
		// breakpoint、thread、module などの event は、最初の範囲では使わない。
	}
}

// appendOutput は、デバッグ対象の出力を、セッションの写しと output/append の dap の欄へ書き足す（基本設計 6.4）。
func (s *session) appendOutput(text string) {
	if err := s.output.Append(text, ""); err != nil {
		s.d.logf("output log: %v", err)
	}
	s.d.appendOutput(text)
}

// startChild は、アダプターからの startDebugging（seq の逆向きの request）を受けて、子のセッションを始める（FR-285。基本設計 6.4 の
// 「セッションと状態の分離」）。子のつなぎ方は spawnChild が決める。
// 子のセッションの接続ができたら受け付けたことを、つなげなければ失敗を応答する（session.start と replyStart）。
// 引数の誤りと、このセッションが終わるところでは、子を作らずに失敗を応答する。
//
// req: FR-285
func (s *session) startChild(seq int, body json.RawMessage) {
	c := s.getConn()
	if c == nil {
		return
	}
	reply := func(err error) {
		if rerr := c.respond(seq, "startDebugging", err); rerr != nil {
			s.d.logf("session %d: respond to startDebugging: %v", s.id, rerr)
		}
	}
	var args startDebuggingArguments
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&args); err != nil || args.Configuration == nil {
		s.d.logf("session %d: invalid startDebugging arguments: %s", s.id, body)
		reply(errors.New("invalid arguments"))
		return
	}
	if args.Request != "launch" && args.Request != "attach" {
		s.d.logf("session %d: startDebugging request %q is not launch or attach", s.id, args.Request)
		reply(fmt.Errorf("request %q is not launch or attach", args.Request))
		return
	}
	d := s.d
	d.mu.Lock()
	if s.closing || s.stopping.Load() || !slices.Contains(d.sessions, s) {
		d.mu.Unlock()
		reply(errors.New("the parent session is ending"))
		return
	}
	child, err := spawnChild(d, s, args.Request, args.Configuration)
	if err != nil {
		d.mu.Unlock()
		reply(err)
		d.showError(fmt.Sprintf("アダプター %s が求めた子のセッションを始められない: %v", s.spec.adapter.name, err))
		return
	}
	child.reply = reply
	d.addSessionLocked(child)
	d.mu.Unlock()
	go child.run()
}

// stopped は、stopped の event を処理する。このセッションを今のセッションにし、止まったスレッドを選び、
// 先頭のフレームの位置へカーソルを移し、実行位置の印を付ける。
// stackTrace の応答が届くまでに世代が進んだら（続けた、選び直した。FR-287）、表示に使わずに捨てる。
func (s *session) stopped(body *stoppedEvent) {
	if len(body.HitBreakpointIDs) > 0 {
		s.d.breakpointsHit(s, body.HitBreakpointIDs)
	}
	gen := s.d.markStopped(s, body.ThreadID)
	ctx, cancel := context.WithTimeout(s.eventCtx, s.d.timeouts.request)
	defer cancel()
	tid, err := s.threadID(ctx)
	if err != nil {
		s.d.logf("threads: %v", err)
		s.d.stoppedWithoutSource(s, gen, body.Reason)
		return
	}
	if body.ThreadID == nil {
		s.d.mu.Lock()
		if s.gen.Load() == gen {
			s.thread, s.hasThread = tid, true
		}
		s.d.mu.Unlock()
	}
	var st stackTraceResponse
	err = s.callGen(ctx, gen, "stackTrace", stackTraceArguments{ThreadID: tid, StartFrame: 0, Levels: 1}, &st)
	if errors.Is(err, errStale) {
		s.d.logf("session %d: stackTrace for thread %d: response arrived after the selection changed; dropped", s.id, tid)
		return
	}
	if err != nil {
		s.d.logf("stackTrace: %v", err)
	}
	if err != nil || len(st.StackFrames) == 0 || st.StackFrames[0].Source == nil || st.StackFrames[0].Source.Path == "" {
		s.d.stoppedWithoutSource(s, gen, body.Reason)
		return
	}
	f := st.StackFrames[0]
	line := fromDAPLine(f.Line, linesStartAt1)
	s.d.showStopped(ctx, s, gen, f.ID, f.Source.Path, line, f.Column, body.Reason)
}

// dapEvent は、受け取った 1 件の event か、逆向きの request である。
type dapEvent struct {
	name string
	body json.RawMessage
	// seq は、逆向きの request の seq である（応答に使う）。event では 0。
	seq int
}

// eventQueue は、読み取りの goroutine から event を処理する goroutine へ渡す、上限の無い列である。
// 読み取りの goroutine を止めない（応答を読み続ける）ために、積む側は待たない。
type eventQueue struct {
	mu     sync.Mutex
	items  []dapEvent
	closed bool
	wake   chan struct{}
}

func newEventQueue() *eventQueue { return &eventQueue{wake: make(chan struct{}, 1)} }

func (q *eventQueue) push(e dapEvent) {
	q.mu.Lock()
	q.items = append(q.items, e)
	q.mu.Unlock()
	q.signal()
}

func (q *eventQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// close は、列を閉じる。残っている event は捨てずに pop で返し、空になったら pop は false を返す。
func (q *eventQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

func (q *eventQueue) pop() (dapEvent, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			e := q.items[0]
			q.items[0] = dapEvent{}
			q.items = q.items[1:]
			q.mu.Unlock()
			return e, true
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return dapEvent{}, false
		}
		<-q.wake
	}
}
