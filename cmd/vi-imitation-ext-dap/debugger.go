package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// providedOps は、この拡張機能が引き受ける本体の操作である（基本設計 5.5。FR-94）。
// 宣言ファイル manifest.json の provides と揃える（TestManifest）。
// debug.toggleUI（DAP のペイン。FR-108）は、ペインを開く pane/open ができるまで引き受けない。
var providedOps = []ext.Operation{
	ext.OpBreakpointToggle, ext.OpBreakpointConditional, ext.OpBreakpointClear,
	ext.OpDebugContinue, ext.OpDebugPause, ext.OpDebugTerminate, ext.OpDebugRestart, ext.OpDebugRunLast,
	ext.OpDebugStepOver, ext.OpDebugStepIn, ext.OpDebugStepOut,
}

// resumeCommands は、続行とステップ実行の操作から、DAP の request の名前への対応である。
var resumeCommands = map[ext.Operation]string{
	ext.OpDebugContinue: "continue",
	ext.OpDebugStepOver: "next",
	ext.OpDebugStepIn:   "stepIn",
	ext.OpDebugStepOut:  "stepOut",
}

// timeouts は、待つ時間の上限である。値は仮で、テストが短くする。
type timeouts struct {
	// initialize は、ソケットへの接続と initialize の応答を待つ時間である。
	initialize time.Duration
	// launch は、launch の応答を待つ時間である。delve は launch でデバッグ対象をビルドするので長く取る。
	launch time.Duration
	// stop は、disconnect の応答と子プロセスの終了を待つ時間である。
	stop time.Duration
	// request は、その他の request（本体への editor/open、DAP の stackTrace など）の応答を待つ時間である。
	request time.Duration
}

// shutdownTimeout は、拡張機能が終わるときに、デバッグアダプターとデバッグ対象を正しい手順（terminate と disconnect）で
// 終わらせるのを待つ時間の上限である。超えたら、猶予 shutdownKillGrace でデバッグアダプターを強制終了する（session.killWithin）。
//
// 本体は shutdown を送ってから期限（仮に 3 秒。基本設計 5.2）を過ぎると、この拡張機能のプロセスグループへ SIGTERM を送る。
// 2 つの和がその期限より短くなるようにする。値は仮。
const (
	shutdownTimeout   = 1500 * time.Millisecond
	shutdownKillGrace = time.Second
)

func defaultTimeouts() timeouts {
	return timeouts{initialize: 10 * time.Second, launch: 120 * time.Second, stop: 5 * time.Second, request: 10 * time.Second}
}

// bpState は、ブレークポイントがデバッグアダプターに検証されたかどうかである。
type bpState int

const (
	// bpPending は、デバッグアダプターに送っていないか、応答をまだ受け取っていないことを表す。breakpoint の印で表示する（基本設計 6.4）。
	bpPending bpState = iota
	bpVerified
	bpUnverified
)

// bp は、1 つのブレークポイントである。
type bp struct {
	// line は、0 から数えた行で、拡張機能の持つバッファの複製の現在のバージョン番号の位置を指す。
	line  int
	state bpState
	// cond は、条件付きのブレークポイント（breakpoint.conditional）の条件の式である。条件の無いものは空。
	// DAP の SourceBreakpoint の condition で送る。
	cond string
}

// prepJob は、デバッグを始める準備（構成の選択と値の決定。prepare）の 1 回である。
// debug.terminate と shutdown で cancel を呼んで取り消す（FR-282 の取り消し）。
type prepJob struct {
	cancel context.CancelFunc
}

// bufState は、開いているバッファの番号と、複製のバージョン番号である。
type bufState struct {
	id      ext.BufferID
	version ext.Version
}

// execPoint は、デバッグ対象が止まった位置である。
type execPoint struct {
	path string
	line int
}

// debugger は、DAP の拡張機能の状態である。
//
// ブレークポイントは、ファイルの絶対パスごとに持つ（基本設計 6.4、6.7 のバッファのパス）。
// バッファを閉じても、デバッグを終えても消さない。
//
// 状態は mu で守る。Server のループ（コマンドのハンドラーとフック）と、デバッグの goroutine（session）の両方から触る。
// mu を持ったまま待つ処理（DAP や本体への request）を行わない。
// sign/set は応答を待たない通知なので mu を持ったまま送り、同じバッファの一覧が送った順に届くようにする。
type debugger struct {
	srv      *server.Server
	settings settings
	timeouts timeouts
	logger   *log.Logger

	mu sync.Mutex
	// bps は、ファイルのパスから、行の昇順のブレークポイントの列への対応である。
	bps map[string][]bp
	// gens は、ファイルのパスごとの、ブレークポイントの列を変えた回数である。
	// setBreakpoints の応答を受け取ったとき、送った後に列が変わっていれば応答を捨てるのに使う。
	gens map[string]uint64
	// buffers は、開いているバッファのパスから、番号とバージョン番号への対応である。
	buffers map[string]bufState
	// paths は、開いているバッファの番号からパスへの対応である。
	paths map[ext.BufferID]string
	exec  *execPoint
	sess  *session
	// shutdownOnce は、shutdown の後始末を 1 回だけ行うためのものである。
	shutdownOnce sync.Once
	// configured は、sess の初期化で setBreakpoints を送る対象を決め終えたことを表す。
	// これより後に変わったブレークポイントは、その都度送る。
	configured bool
	// last は、最後に始めたデバッグの、値を決め終えた構成とアダプターの起動の方法である。まだ始めていなければ nil。
	last *runSpec
	// preparing は、デバッグを始める準備の途中の仕事である。無ければ nil。
	preparing *prepJob
	// pickers は、開いている一覧（picker.go）の知らせの受け口である。
	pickers pickers
	// compoundsShown は、compounds を扱えないことを知らせた launch.json のパスである（拡張機能のプロセスが動いている間に 2 回出さない）。
	compoundsShown map[string]bool
	// restartPending は、debug.restart で止めたセッションが終わったら、last で始め直すことを表す。
	restartPending bool
	// stopWaiter は、再生中（sync）に呼ばれた、デバッグ対象を動かす操作の応答の受け口である（waitStopLocked）。
	// 次の停止（showStopped、stoppedWithoutSource）かデバッグの終わり（sessionEnded）で結果を届ける。無ければ nil。
	stopWaiter *waiter
}

// opHandler は、この拡張機能の操作のハンドラーである。
// w が nil でなければ、provider/invoke の応答を、w に結果が届くまで遅らせる（asyncProvider）。
// w を返すのは、本体がマクロかドットリピートを再生している（p.Sync）ときだけである。
type opHandler func(ctx context.Context, b *server.Buffer, p *ext.ProviderInvokeParams) (res ext.ProviderInvokeResult, w *waiter)

// waiter は、再生中（p.Sync）の provider/invoke の応答を、操作の完了まで遅らせるための結果の受け口である（基本設計 6 章の冒頭。FR-19）。
// 本体は再生中に応答を待ってから次の操作へ進むので、応答を遅らせれば、次の操作（2 回目のステップ実行など）は前の操作の完了の後に届く。
type waiter struct {
	ch chan ext.ProviderInvokeResult

	mu sync.Mutex
	// resolved は、resolve を呼んだことを表す。abandoned は、本体が待つのをやめた（abandon）ことを表す。
	resolved, abandoned bool
}

func newWaiter() *waiter { return &waiter{ch: make(chan ext.ProviderInvokeResult, 1)} }

// resolve は、結果を届け、本体へ応答として届く見込みがあるかを返す。2 回目からは捨てて true を返す。
// 本体が既に待つのをやめていれば（abandon の後）、届けずに false を返す。呼び出し側は、結果の open を editor/open で送り直す。
// w が nil なら何もせずに true を返す。どこから呼んでもよい。
func (w *waiter) resolve(r ext.ProviderInvokeResult) bool {
	if w == nil {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.resolved {
		return true
	}
	w.resolved = true
	if w.abandoned {
		return false
	}
	w.ch <- r
	return true
}

// abandon は、本体が待つのをやめたことを記録する。既に届いていて応答に使っていない結果があれば、それを返す。
func (w *waiter) abandon() (ext.ProviderInvokeResult, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.abandoned = true
	select {
	case r := <-w.ch:
		return r, true
	default:
		return ext.ProviderInvokeResult{}, false
	}
}

// asyncProvider は、h を provider/invoke のハンドラーにする。
// h が waiter を返したら、別の goroutine でその結果を待ってから応答する。Server のループは止めない。
// 本体が待つのをやめた（期限を超えて $/cancel を送った）ら、waiter を外して取り消しで応答する。
// その後の停止は、再生中でないときと同じく editor/open で知らせる。
// 期限とほぼ同時に止まり、結果が既に届いていたときも、本体はその応答を捨てるので、結果の open を editor/open で送り直す（#217 のレビュー）。
func (d *debugger) asyncProvider(h opHandler) server.AsyncProviderHandler {
	return func(ctx context.Context, b *server.Buffer, p *ext.ProviderInvokeParams, reply func(ext.ProviderInvokeResult, error)) {
		res, w := h(ctx, b, p)
		if w == nil {
			reply(res, nil)
			return
		}
		go func() {
			select {
			case r := <-w.ch:
				if ctx.Err() == nil {
					reply(r, nil)
					return
				}
				d.openLater(r.Open)
			case <-ctx.Done():
				d.dropStopWaiter(w)
				if r, ok := w.abandon(); ok {
					d.openLater(r.Open)
				}
			}
			reply(ext.ProviderInvokeResult{}, ctx.Err())
		}()
	}
}

// openLater は、本体が捨てた応答の open を、再生中でないときと同じく editor/open で送る。open が nil なら何もしない。
// Server のループの外から呼ぶ。
func (d *debugger) openLater(open *ext.EditorOpenParams) {
	if open == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.timeouts.request)
	defer cancel()
	if _, err := d.srv.OpenEditor(ctx, *open); err != nil {
		d.logf("editor/open: %v", err)
	}
}

// waitStopLocked は、デバッグ対象の次の停止かデバッグの終わりに結果を届ける waiter を置いて返す。mu を持って呼ぶ。
// 前の waiter が残っていれば（本体が待つのをやめた後の取り消しがまだ届いていないなど）、失敗を届けて置き換える。
func (d *debugger) waitStopLocked() *waiter {
	d.stopWaiter.resolve(ext.ProviderInvokeResult{OK: false})
	d.stopWaiter = newWaiter()
	return d.stopWaiter
}

// takeStopWaiterLocked は、置いてある waiter を外して返す。無ければ nil。mu を持って呼ぶ。
func (d *debugger) takeStopWaiterLocked() *waiter {
	w := d.stopWaiter
	d.stopWaiter = nil
	return w
}

// takeStopWaiter は、takeStopWaiterLocked の mu を取る版である。
func (d *debugger) takeStopWaiter() *waiter {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.takeStopWaiterLocked()
}

// dropStopWaiter は、w が置いてある waiter なら外す。
func (d *debugger) dropStopWaiter(w *waiter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopWaiter == w {
		d.stopWaiter = nil
	}
}

func newDebugger() *debugger {
	d := &debugger{
		settings: defaultSettings(),
		timeouts: defaultTimeouts(),
		logger:   log.New(os.Stderr, "vi-imitation-ext-dap: ", log.LstdFlags),
		bps:      make(map[string][]bp),
		gens:     make(map[string]uint64),
		buffers:  make(map[string]bufState),
		paths:    make(map[ext.BufferID]string),
		pickers: pickers{
			waiting: make(map[ext.ViewID]chan server.ViewEvent),
			early:   make(map[ext.ViewID][]server.ViewEvent),
		},
		compoundsShown: make(map[string]bool),
	}
	d.srv = d.newServer()
	return d
}

// newServer は、コマンドとフックを登録した Server を作る。
func (d *debugger) newServer() *server.Server {
	s := server.New()
	s.ErrorLog = d.logger
	s.OnInitialize(d.onInitialize)
	s.OnBufferOpen(d.onBufferOpen)
	s.OnBufferChange(d.onBufferChange)
	s.OnBufferClose(d.onBufferClose)
	s.OnBufferDidSave(d.onBufferDidSave)
	s.OnView(d.onView)
	s.ProviderAsync(ext.OpBreakpointToggle, d.asyncProvider(d.noArgs(d.toggleBreakpoint)))
	s.ProviderAsync(ext.OpBreakpointConditional, d.asyncProvider(d.conditionalBreakpoint))
	s.ProviderAsync(ext.OpBreakpointClear, d.asyncProvider(d.noArgs(d.clearBreakpoints)))
	s.ProviderAsync(ext.OpDebugContinue, d.asyncProvider(d.noArgs(d.continueOrStart)))
	s.ProviderAsync(ext.OpDebugPause, d.asyncProvider(d.noArgs(d.pause)))
	s.ProviderAsync(ext.OpDebugTerminate, d.asyncProvider(d.noArgs(d.stop)))
	s.ProviderAsync(ext.OpDebugRestart, d.asyncProvider(d.noArgs(d.restart)))
	s.ProviderAsync(ext.OpDebugRunLast, d.asyncProvider(d.noArgs(d.runLast)))
	for _, op := range []ext.Operation{ext.OpDebugStepOver, ext.OpDebugStepIn, ext.OpDebugStepOut} {
		s.ProviderAsync(op, d.asyncProvider(d.noArgs(d.resume)))
	}
	s.OnShutdown(func(context.Context, *server.ShutdownEvent) { d.shutdown() })
	return s
}

// shutdown は、拡張機能が終わるときに、デバッグ中ならデバッグ対象とデバッグアダプターを終わらせ、終わるまで待つ。
// 正しい手順を待つのは shutdownTimeout までで、それを過ぎたら猶予 shutdownKillGrace で強制終了する。
// 2 回目からは何もしない（1 回目が戻るまで待つ）。
//
// 本体から shutdown を受け取ったとき（OnShutdown のフック。基本設計 5.2 の正常な終了）と、
// 本体が標準入力を閉じたとき、シグナル（SIGINT、SIGTERM）を受け取ったときに走る。
// フックの中では Server のループが止まるが、本体は shutdown の後に問い合わせと ping を送らないので、待ってよい。
// 本体の異常のときの強制終了（拡張機能のプロセスグループへの SIGKILL）では走らない。
// その場合は、デバッグアダプターが Pdeathsig の SIGTERM で自分で後始末する（startAdapter の doc）。
//
// req: FR-31
func (d *debugger) shutdown() {
	d.shutdownOnce.Do(func() {
		d.mu.Lock()
		s := d.sess
		if d.preparing != nil {
			// 始める準備（resolveCommand など）を取り消す（FR-282）。
			d.preparing.cancel()
			d.preparing = nil
		}
		d.mu.Unlock()
		if s == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		s.stop(ctx, shutdownKillGrace)
	})
}

func (d *debugger) logf(format string, args ...any) { d.logger.Printf(format, args...) }

func (d *debugger) showInfo(text string) {
	if err := d.srv.ShowMessage(ext.MessageInfo, text); err != nil {
		d.logf("message/show: %v", err)
	}
}

func (d *debugger) showError(text string) {
	if err := d.srv.ShowMessage(ext.MessageError, text); err != nil {
		d.logf("message/show: %v", err)
	}
}

// fail は、操作を失敗させ、理由を利用者へ表示する。
func (d *debugger) fail(text string) (ext.ProviderInvokeResult, *waiter) {
	d.showError(text)
	return ext.ProviderInvokeResult{OK: false}, nil
}

// ok は、すぐに成功で応答する結果である。
func ok() (ext.ProviderInvokeResult, *waiter) { return ext.ProviderInvokeResult{OK: true}, nil }

// argsText は、provider/invoke の args のうち、コマンドラインから呼んだときの引数の文字列（ext.ProviderArgs の Text）を返す。
func argsText(p *ext.ProviderInvokeParams) (string, error) {
	var a ext.ProviderArgs
	if len(p.Args) > 0 {
		if err := json.Unmarshal(p.Args, &a); err != nil {
			return "", err
		}
	}
	return a.Text, nil
}

// noArgs は、引数を取らない操作のハンドラー h を包む。
// コマンドラインから引数を付けて呼んだ（:debug.continue foo）ら、h を呼ばずに、引数を取らないことを知らせて失敗させる。
// 引数の形は ACP の拡張機能（acp.prompt の {"text":"..."}）と揃える。
func (d *debugger) noArgs(h opHandler) opHandler {
	return func(ctx context.Context, b *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
		text, err := argsText(p)
		if err != nil {
			return d.fail(fmt.Sprintf("%s: 引数を読めない: %v", p.Operation, err))
		}
		if strings.TrimSpace(text) != "" {
			return d.fail(fmt.Sprintf("%s: 引数を取らない", p.Operation))
		}
		return h(ctx, b, p)
	}
}

// appendOutput は、デバッグ対象の出力を output/append の dap の欄へ書き足す（基本設計 6.4）。
//
// req: FR-31
func (d *debugger) appendOutput(text string) {
	if err := d.srv.AppendOutput(outputChannel, text); err != nil {
		d.logf("output/append: %v", err)
	}
}

func (d *debugger) onInitialize(_ context.Context, e *server.InitializeEvent) error {
	s, err := parseSettings(e.Settings)
	if err != nil {
		return fmt.Errorf("invalid settings: %w", err)
	}
	d.mu.Lock()
	d.settings = s
	d.mu.Unlock()
	return nil
}

func (d *debugger) onBufferOpen(_ context.Context, e *server.BufferOpenEvent) {
	path := e.Buffer.Path()
	if path == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buffers[path] = bufState{id: e.Buffer.ID(), version: e.Buffer.Version()}
	d.paths[e.Buffer.ID()] = path
	if len(d.bps[path]) > 0 || (d.exec != nil && d.exec.path == path) {
		d.publishLocked(path)
	}
}

// onBufferChange は、編集に合わせて、ブレークポイントと実行位置の行をずらす（基本設計 6.4 のブレークポイントの位置の追従）。
// 本体も印の行を同じ規則でずらす（基本設計 6.7）ので、sign/set は送り直さない。
// デバッグアダプターへは、保存したときに送り直す（onBufferDidSave）。
func (d *debugger) onBufferChange(_ context.Context, e *server.BufferChangeEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	path, ok := d.paths[e.Buffer.ID()]
	if !ok {
		return
	}
	d.buffers[path] = bufState{id: e.Buffer.ID(), version: e.ToVersion}
	if bps := d.bps[path]; len(bps) > 0 {
		d.bps[path] = shiftBreakpoints(bps, e.Edits)
		d.gens[path]++
	}
	if d.exec != nil && d.exec.path == path {
		for _, ed := range e.Edits {
			d.exec.line = shiftLine(d.exec.line, ed)
		}
	}
}

func (d *debugger) onBufferClose(_ context.Context, e *server.BufferCloseEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if path, ok := d.paths[e.Buffer]; ok {
		delete(d.paths, e.Buffer)
		delete(d.buffers, path)
	}
}

// onBufferDidSave は、デバッグ中なら、保存したファイルのブレークポイントを setBreakpoints で送り直す（基本設計 6.4）。
// 保存まで送り直さないのは、デバッグ対象が実行するのは保存されたファイルだからである。
func (d *debugger) onBufferDidSave(_ context.Context, e *server.BufferDidSaveEvent) {
	path := e.Params.Path
	if path == "" && e.Buffer != nil {
		path = e.Buffer.Path()
	}
	d.mu.Lock()
	s := d.sess
	send := s != nil && d.configured && path != ""
	d.mu.Unlock()
	if send {
		go d.sendBreakpoints(s, path) //nolint:gosec // G118: ハンドラーとフックの ctx は戻ると無効になるので引き継がない
	}
}

// sendBreakpoints は、ループの外で setBreakpoints を送る。
func (d *debugger) sendBreakpoints(s *session, path string) {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeouts.request)
	defer cancel()
	if err := s.sendBreakpoints(ctx, path); err != nil {
		d.logf("setBreakpoints %s: %v", path, err)
	}
}

// toggleBreakpoint は、カーソルの行のブレークポイントを切り替える（基本設計 6.4。本体の操作 breakpoint.toggle）。
// 印は sign/set ですぐ送り、デバッグ中なら setBreakpoints も送る。
//
// req: FR-31, FR-94
func (d *debugger) toggleBreakpoint(_ context.Context, b *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	return d.editBreakpoints(b, p.Sync, func(bps []bp) []bp {
		line := p.Cursor.Line
		if i := slices.IndexFunc(bps, func(x bp) bool { return x.line == line }); i >= 0 {
			return slices.Delete(bps, i, i+1)
		}
		return normalize(append(bps, bp{line: line, state: bpPending}))
	})
}

// conditionalBreakpoint は、カーソルの行に、条件付きのブレークポイントを置く（本体の操作 breakpoint.conditional）。
// 条件の式は、コマンドラインの引数（:breakpoint.conditional i > 3）で受け取る。
// その行に既にブレークポイントがあれば、条件を置き換える。
// キーで呼ばれて引数が無ければ、渡し方を知らせて失敗する（入力の行の部品（基本設計 1.4）ができるまでの仮の形）。
//
// req: FR-94
func (d *debugger) conditionalBreakpoint(_ context.Context, b *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	text, err := argsText(p)
	if err != nil {
		return d.fail(fmt.Sprintf("%s: 引数を読めない: %v", p.Operation, err))
	}
	cond := strings.TrimSpace(text)
	if cond == "" {
		return d.fail("条件の式を :breakpoint.conditional 式 で渡す")
	}
	return d.editBreakpoints(b, p.Sync, func(bps []bp) []bp {
		line := p.Cursor.Line
		bps = slices.DeleteFunc(bps, func(x bp) bool { return x.line == line })
		return normalize(append(bps, bp{line: line, state: bpPending, cond: cond}))
	})
}

// editBreakpoints は、バッファ b のファイルのブレークポイントの列を edit で変え、印を送り、デバッグ中なら setBreakpoints を送る。
// sync（再生中）なら、setBreakpoints の応答を受け取ってから応答する。
// 再生の次の操作（続行など）が、置いたブレークポイントより先にデバッグアダプターへ届かないようにするためである。
func (d *debugger) editBreakpoints(b *server.Buffer, sync bool, edit func([]bp) []bp) (ext.ProviderInvokeResult, *waiter) {
	if b == nil {
		// 本体が不正な UTF-8 のためにバッファを送っていない（p.Withheld）。本体が理由を利用者へ知らせる。
		return ext.ProviderInvokeResult{OK: false}, nil
	}
	path := b.Path()
	if path == "" {
		return d.fail("ファイルに対応しないバッファにはブレークポイントを置けない")
	}
	d.mu.Lock()
	d.buffers[path] = bufState{id: b.ID(), version: b.Version()}
	d.paths[b.ID()] = path
	bps := edit(slices.Clone(d.bps[path]))
	if len(bps) == 0 {
		delete(d.bps, path)
	} else {
		d.bps[path] = bps
	}
	d.gens[path]++
	d.publishLocked(path)
	s := d.sess
	send := s != nil && d.configured
	d.mu.Unlock()
	if !send {
		return ok()
	}
	// ハンドラーの ctx は戻ると無効になるので引き継がない。
	return d.sendBreakpointsAll(s, []string{path}, sync)
}

// sendBreakpointsAll は、paths のブレークポイントをループの外で setBreakpoints で送る。
// sync なら、すべて送り終えてから成功を届ける waiter を返す。
func (d *debugger) sendBreakpointsAll(s *session, paths []string, sync bool) (ext.ProviderInvokeResult, *waiter) {
	if !sync {
		for _, path := range paths {
			go d.sendBreakpoints(s, path)
		}
		return ok()
	}
	w := newWaiter()
	go func() {
		for _, path := range paths {
			d.sendBreakpoints(s, path)
		}
		w.resolve(ext.ProviderInvokeResult{OK: true})
	}()
	return ext.ProviderInvokeResult{}, w
}

// clearBreakpoints は、すべてのファイルのブレークポイントを消す（本体の操作 breakpoint.clear）。
// 印を消し、デバッグ中なら、消したファイルごとに空の setBreakpoints を送る。
//
// req: FR-94
func (d *debugger) clearBreakpoints(_ context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	paths := slices.Sorted(maps.Keys(d.bps))
	for _, path := range paths {
		delete(d.bps, path)
		d.gens[path]++
		d.publishLocked(path)
	}
	s := d.sess
	send := s != nil && d.configured
	d.mu.Unlock()
	d.showInfo(fmt.Sprintf("ブレークポイントを消した（%d ファイル）", len(paths)))
	if !send {
		return ok()
	}
	return d.sendBreakpointsAll(s, paths, p.Sync)
}

// continueOrStart は、本体の操作 debug.continue を処理する。デバッグ中なら続行し、始まっていなければ始める（要件定義 4.11）。
//
// req: FR-31, FR-94
func (d *debugger) continueOrStart(ctx context.Context, b *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	running := d.sess != nil
	preparing := d.preparing != nil
	d.mu.Unlock()
	if preparing {
		return d.fail("デバッグを始める準備の途中（やめるときは debug.terminate）")
	}
	if !running {
		return d.start(ctx, b, p)
	}
	return d.resume(ctx, b, p)
}

// runLast は、前回と同じ launch の引数とデバッグアダプターで、デバッグを始める（本体の操作 debug.runLast）。
// 前回が無いか、デバッグ中なら失敗する。
//
// req: FR-94
func (d *debugger) runLast(_ context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last == nil {
		return d.fail("前回のデバッグが無い")
	}
	if d.preparing != nil {
		return d.fail("デバッグを始める準備の途中（やめるときは debug.terminate）")
	}
	return d.startLocked(d.last, p.Sync)
}

// restart は、デバッグを終えてから、前回と同じ設定で始め直す（本体の操作 debug.restart）。
// デバッグ中でなければ、runLast と同じく前回の設定で始める。
// 終えるのは debug.terminate と同じ手順で、終わった後（sessionEnded）に始める。
// 再生中（sync）なら、始め直したデバッグ対象が最初に止まるまで応答を遅らせる（start と同じ）。
//
// req: FR-94
func (d *debugger) restart(ctx context.Context, b *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	s := d.sess
	var w *waiter
	if s != nil {
		d.restartPending = true
		if p.Sync {
			// 始め直すのは sessionEnded なので、waiter は終わりでは外さずに、始め直した後の停止まで残す。
			w = d.waitStopLocked()
		}
	}
	d.mu.Unlock()
	if s == nil {
		return d.runLast(ctx, b, p)
	}
	s.stopping.Store(true)
	go s.stop(context.Background(), killGrace) //nolint:gosec // ハンドラーが戻った後も止め終えるまで続けるので、ハンドラーの ctx は使わない
	if w != nil {
		return ext.ProviderInvokeResult{}, w
	}
	return ok()
}

// pause は、実行中のデバッグ対象を一時停止する（本体の操作 debug.pause）。止まったことは stopped の event で知らせる。
// 再生中（sync）なら、止まるまで応答を遅らせる。
// 再生中で既に止まっていれば、pause を送らずにすぐに応答する。送ると、デバッグアダプターによっては stopped の event が後から届き、
// 再生の次の操作（続行など）の停止として取り違えるためである。
//
// req: FR-94, FR-19
func (d *debugger) pause(_ context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	s := d.sess
	ready := d.configured
	stopped := d.exec != nil
	var w *waiter
	if s != nil && ready && p.Sync && !stopped {
		w = d.waitStopLocked()
	}
	d.mu.Unlock()
	if s == nil || !ready {
		return d.fail("デバッグを始めていない")
	}
	if p.Sync && stopped {
		return ok()
	}
	go func() { //nolint:gosec // G118: ハンドラーの ctx は応答すると取り消されるので引き継がない
		ctx, cancel := context.WithTimeout(context.Background(), d.timeouts.request)
		defer cancel()
		if err := s.pause(ctx); err != nil && !errors.Is(err, errDAPClosed) {
			d.logf("pause: %v", err)
			d.dropStopWaiter(w)
			w.resolve(ext.ProviderInvokeResult{OK: false})
			d.showError(fmt.Sprintf("pause に失敗した: %v", err))
		}
	}()
	if w != nil {
		return ext.ProviderInvokeResult{}, w
	}
	return ok()
}

// start は、デバッグを始める（基本設計 6.4 の開始の流れ）。
//
// 再生中（sync）でなければ、受け付けたことだけを応答し、構成の選択と値の決定（prepare）を別の goroutine で行う。
// 候補の一覧、choice/ask、${pickProcess}、resolveCommand は、利用者の応答や外部のコマンドを待つので、
// ext/server のメッセージを処理する goroutine の上で行わない（dev-cycle 2.2）。結果は message/show で知らせる。
//
// 再生中（sync）は、一覧も問いも出さず、前回に値を決め終えた構成で始める。前回が無ければ失敗させる（FR-280）。
// デバッグ対象が最初に止まるか、止まらずに終わるまで応答を遅らせる（startLocked）。
//
// req: FR-31, FR-19, FR-280
func (d *debugger) start(_ context.Context, b *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p.Sync {
		if d.last == nil {
			return d.fail("再生の中では構成を選べない（前回のデバッグが無い）")
		}
		return d.startLocked(d.last, true)
	}
	if d.preparing != nil || d.sess != nil {
		return d.fail("デバッグは既に始まっている")
	}
	// 本体が不正な UTF-8 のためにバッファを送っていない（p.Withheld）なら b は nil で、ファイルに対応しないバッファと同じに扱う。
	inv := newInvocation(b, p)
	ctx, cancel := context.WithCancel(context.Background())
	job := &prepJob{cancel: cancel}
	d.preparing = job
	go d.prepareAndStart(ctx, job, inv)
	return ok()
}

// prepareAndStart は、構成を選んで値を決め、アダプターにつないでデバッグを始める。start の後に別の goroutine で呼ぶ。
func (d *debugger) prepareAndStart(ctx context.Context, job *prepJob, inv invocation) {
	defer job.cancel()
	spec, err := d.prepare(ctx, inv)
	d.mu.Lock()
	current := d.preparing == job
	if current {
		d.preparing = nil
	}
	if err == nil && current {
		d.startLocked(spec, false)
	}
	d.mu.Unlock()
	if err == nil {
		return
	}
	var se *startError
	switch {
	case errors.As(err, &se) && se.cancelled, !current:
		d.showInfo(errStartCancelled.msg)
	default:
		d.showError(err.Error())
	}
}

// startLocked は、spec のアダプターにつなぎ（spawn）、デバッグを始め、spec を前回の設定として覚える。mu を持って呼ぶ。
// sync なら、デバッグ対象が最初に止まるか、デバッグが終わるまで応答を遅らせる waiter を返す。
// waiter は mu を持ったまま、セッションを動かし始める前に置くので、最初の停止を取りこぼさない。
func (d *debugger) startLocked(spec *runSpec, sync bool) (ext.ProviderInvokeResult, *waiter) {
	if d.sess != nil {
		return d.fail("デバッグは既に始まっている")
	}
	s, err := spawn(d, spec)
	if err != nil {
		return d.fail(fmt.Sprintf("デバッグアダプター %s を起動できない: %v", spec.adapter.name, err))
	}
	d.sess = s
	d.configured = false
	d.last = spec
	var w *waiter
	if sync {
		w = d.waitStopLocked()
	}
	go s.run()
	if w != nil {
		return ext.ProviderInvokeResult{}, w
	}
	return ok()
}

// stop は、デバッグを終える（基本設計 6.4。本体の操作 debug.terminate）。disconnect は別の goroutine で送る。
// 再生中（sync）なら、終え終わる（session.stop が戻る）まで応答を遅らせる。
//
// req: FR-31, FR-94
func (d *debugger) stop(_ context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	s := d.sess
	d.restartPending = false
	job := d.preparing
	d.preparing = nil
	d.mu.Unlock()
	if job != nil {
		// 始める準備の途中（一覧、問い、resolveCommand）なら、取り消して始めない（FR-282）。知らせは prepareAndStart が出す。
		job.cancel()
		return ok()
	}
	if s == nil {
		return d.fail("デバッグを始めていない")
	}
	s.stopping.Store(true)
	if !p.Sync {
		go s.stop(context.Background(), killGrace) //nolint:gosec // ハンドラーが戻った後も止め終えるまで続けるので、ハンドラーの ctx は使わない
		return ok()
	}
	w := newWaiter()
	go func() { //nolint:gosec // G118: 応答した後も止め終えるまで続けるので、ハンドラーの ctx は使わない
		s.stop(context.Background(), killGrace)
		w.resolve(ext.ProviderInvokeResult{OK: true})
	}()
	return ext.ProviderInvokeResult{}, w
}

// resume は、続行とステップ実行の操作（debug.continue、debug.stepOver、debug.stepIn、debug.stepOut）を、
// DAP の continue、next、stepIn、stepOut で送る（基本設計 6.4）。
// 送る前に実行位置の印を消す。DAP の仕様では、続行を意味する request の後に continued の event は届かないためである。
// request が失敗したら、印を戻して利用者に知らせる。
//
// 再生中（sync）なら、デバッグ対象が次に止まるか、デバッグが終わるまで応答を遅らせ、止まった位置を応答の open で返す
// （showStopped）。DAP の request の応答は、デバッグ対象が動き始めたことしか表さない（next の応答の後に stopped の event が届く）ので、
// request の応答で答えると、再生の次のステップ実行が、まだ動いているデバッグ対象へ届いて失敗する（#210）。
//
// req: FR-31, FR-94, FR-19
func (d *debugger) resume(_ context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	command := resumeCommands[p.Operation]
	d.mu.Lock()
	s := d.sess
	ready := d.configured
	var w *waiter
	if s != nil && ready && p.Sync {
		// request を送る前に置き、request の応答より先に届く stopped の event も取りこぼさない。
		w = d.waitStopLocked()
	}
	d.mu.Unlock()
	if s == nil || !ready {
		return d.fail("デバッグを始めていない")
	}
	prev := d.clearExecution()
	go func() { //nolint:gosec // G118: ハンドラーの ctx は応答すると取り消されるので引き継がない
		ctx, cancel := context.WithTimeout(context.Background(), d.timeouts.request)
		defer cancel()
		if err := s.resume(ctx, command); err != nil {
			d.logf("%s: %v", command, err)
			if errors.Is(err, errDAPClosed) {
				// デバッグアダプターが終わった。知らせるのは後始末（sessionEnded）で、waiter もそこで結果を受け取る。
				return
			}
			d.dropStopWaiter(w)
			w.resolve(ext.ProviderInvokeResult{OK: false})
			d.restoreExecution(prev)
			d.showError(fmt.Sprintf("%s に失敗した: %v", command, err))
		}
	}()
	if w != nil {
		return ext.ProviderInvokeResult{}, w
	}
	return ok()
}

// configure は、s の初期化で setBreakpoints を送るファイルの一覧を返し、以後の変更をその都度送るようにする。
func (d *debugger) configure(s *session) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sess != s {
		return nil
	}
	d.configured = true
	return slices.Sorted(maps.Keys(d.bps))
}

// snapshotBreakpoints は、path のブレークポイント（行と条件）の写しと、列を変えた回数を返す。
func (d *debugger) snapshotBreakpoints(path string) ([]bp, uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.bps[path]), d.gens[path]
}

// applyBreakpoints は、setBreakpoints の応答を path のブレークポイントへ反映し、印を付け直す（基本設計 6.4）。
// 送った後に列が変わっていれば（gen が違えば）、応答を捨てる。
// 応答の行が送った行と違えば、デバッグアダプターが実際に置いた行へ移す。
//
// req: FR-31
func (d *debugger) applyBreakpoints(path string, gen uint64, res []breakpoint) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gens[path] != gen {
		return
	}
	bps := slices.Clone(d.bps[path])
	for i := range bps {
		if i >= len(res) {
			break
		}
		if res[i].Verified {
			bps[i].state = bpVerified
		} else {
			bps[i].state = bpUnverified
		}
		if res[i].Line > 0 {
			bps[i].line = fromDAPLine(res[i].Line, linesStartAt1)
		}
	}
	d.bps[path] = normalize(bps)
	d.publishLocked(path)
}

// showStopped は、デバッグ対象が path の line 行（DAP の列は column）で止まったことを表示する（基本設計 6.4）。
// editor/open でカーソルを移し、sign/set で実行位置の印を付け、message/show で止まった理由を知らせる。
// session の event の goroutine から呼ぶ。
//
// 再生中（sync）の操作がこの停止を待っていれば（stopWaiter）、editor/open を送らずに、止まった位置をその操作の応答の open で返す。
// 本体は応答を待つ間も editor/open を処理するが、送ると待つ間にカーソルが移り、本体が再生の続きで操作を実行し直すときに
// カーソルが変わったとみなして再生を止める（基本設計 5.4、6 章の冒頭、6.4）。
//
// req: FR-31, FR-19
func (d *debugger) showStopped(ctx context.Context, path string, line, column int, reason string) {
	d.mu.Lock()
	d.exec = &execPoint{path: path, line: line}
	d.publishLocked(path)
	w := d.takeStopWaiterLocked()
	buf, opened := d.buffers[path]
	d.mu.Unlock()
	d.showInfo(fmt.Sprintf("停止した（%s）: %s:%d", reason, path, line+1))
	if w != nil {
		if !replayStopReason(reason) {
			// 記録したときと違う止まり方なので、再生を止める（仮。基本設計 6 章の冒頭）。止まった位置へは下で editor/open で移る。
			w.resolve(ext.ProviderInvokeResult{OK: false})
			d.showError(fmt.Sprintf("再生の途中でデバッグ対象が止まった（%s）ので、再生を止めた", reason))
		} else if w.resolve(ext.ProviderInvokeResult{OK: true, Open: d.stopOpen(ctx, path, line, column, buf, opened)}) {
			return
		}
		// 本体が待つのをやめた後に止まったなら、再生中でないときと同じく editor/open で移る。
	}

	res, err := d.srv.OpenEditor(ctx, ext.EditorOpenParams{Path: path, Focus: true})
	if err != nil {
		d.logf("editor/open %s: %v", path, err)
		return
	}
	// 本体は応答より先に buffer/open を送るが、複製ができるのはループがそれを処理した後である（ext.EditorOpenParams）。
	b, err := d.srv.WaitBuffer(ctx, res.Buffer, res.Version)
	if err != nil {
		d.logf("wait buffer %d: %v", res.Buffer, err)
		return
	}
	// 複製ができたので、印を送り直す（onBufferOpen で送っているが、開いていなかったファイルでは exec を置く前のことがある）。
	d.mu.Lock()
	if d.exec != nil && d.exec.path == path {
		line = d.exec.line
		d.publishLocked(path)
	}
	d.mu.Unlock()
	line = min(line, b.LineCount()-1)
	pos := ext.Position{Line: line, Byte: utf16ColumnToByte(b.Line(line), column, linesStartAt1)}
	id, version := b.ID(), b.Version()
	if _, err := d.srv.OpenEditor(ctx, ext.EditorOpenParams{Buffer: &id, Position: &pos, Version: &version, Focus: true}); err != nil {
		d.logf("editor/open buffer %d: %v", id, err)
	}
}

// stopOpen は、再生中の操作の応答で返す、止まった位置へのカーソルの移動を作る。
// path のバッファを開いていれば（opened）、複製の行から DAP の列をバイトの位置にし、バッファの番号とバージョン番号で指す。
// 開いていなければ、パスと行の先頭で指す（本体が開いてから移る）。
func (d *debugger) stopOpen(ctx context.Context, path string, line, column int, buf bufState, opened bool) *ext.EditorOpenParams {
	if opened {
		// 本体は応答を待つ間、再生した client のキーを処理しないので、ふつうは複製は buf.version に既にある。
		b, err := d.srv.WaitBuffer(ctx, buf.id, buf.version)
		switch {
		case err != nil:
			d.logf("wait buffer %d: %v", buf.id, err)
		case b.LineCount() > 0:
			line = min(line, b.LineCount()-1)
			pos := ext.Position{Line: line, Byte: utf16ColumnToByte(b.Line(line), column, linesStartAt1)}
			id, version := b.ID(), b.Version()
			return &ext.EditorOpenParams{Buffer: &id, Position: &pos, Version: &version, Focus: true}
		}
	}
	// 開いていないファイルは、本体が開く前なので、ファイルの中身から列をバイトの位置にする。読めなければ行の先頭にする。
	pos := ext.Position{Line: line}
	if data, err := os.ReadFile(path); err == nil {
		ls := strings.Split(string(data), "\n")
		if line < len(ls) {
			pos.Byte = utf16ColumnToByte(strings.TrimSuffix(ls[line], "\r"), column, linesStartAt1)
		}
	}
	return &ext.EditorOpenParams{Path: path, Position: &pos, Focus: true}
}

// replayStopReasons は、再生中の続行、ステップ実行、開始、一時停止が待つ停止の理由（DAP の stopped の reason）である（仮）。
// 記録したときと同じく、ブレークポイントかステップ実行か一時停止で止まったものとして再生を続ける。
// それ以外（exception、goto など。delve の panic は exception）は、記録したときと違う止まり方として再生を止める（#217 のレビュー）。
var replayStopReasons = []string{"step", "breakpoint", "function breakpoint", "data breakpoint", "instruction breakpoint", "pause", "entry"}

// replayStopReason は、reason が再生を続けてよい停止の理由かを返す。
func replayStopReason(reason string) bool { return slices.Contains(replayStopReasons, reason) }

// stoppedWithoutSource は、止まった位置（ソースのパス）が分からない停止を知らせる。
// 再生中の操作がこの停止を待っていれば、カーソルを動かさずに成功を返す。
// ただし、記録したときと違う止まり方（replayStopReason が false。例外など）なら、showStopped と同じく失敗を返して再生を止める（#224）。
//
// req: FR-19
func (d *debugger) stoppedWithoutSource(reason string) {
	w := d.takeStopWaiter()
	d.showInfo("停止した（" + reason + "）")
	if w == nil {
		return
	}
	if !replayStopReason(reason) {
		w.resolve(ext.ProviderInvokeResult{OK: false})
		d.showError(fmt.Sprintf("再生の途中でデバッグ対象が止まった（%s）ので、再生を止めた", reason))
		return
	}
	w.resolve(ext.ProviderInvokeResult{OK: true})
}

// clearExecution は、実行位置の印を消し、消す前の位置を返す（基本設計 6.4 の continued と terminated）。
func (d *debugger) clearExecution() *execPoint {
	d.mu.Lock()
	defer d.mu.Unlock()
	prev := d.exec
	if prev != nil {
		d.exec = nil
		d.publishLocked(prev.path)
	}
	return prev
}

// restoreExecution は、clearExecution で消した位置を戻す。その後に別の位置で止まっていれば何もしない。
func (d *debugger) restoreExecution(prev *execPoint) {
	if prev == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.exec == nil && d.sess != nil {
		d.exec = prev
		d.publishLocked(prev.path)
	}
}

// sessionEnded は、s の後始末の最後に呼ぶ。ブレークポイントを未検証に戻し、実行位置の印を消し、利用者に知らせる。
// abnormal は、利用者の debug.terminate でも terminated の event でもなく、デバッグアダプターが終わったことを表す。
// quiet は、開始に失敗して既に知らせたので、終わりを知らせないことを表す。
//
// req: FR-31
func (d *debugger) sessionEnded(s *session, abnormal, quiet bool) {
	d.mu.Lock()
	if d.sess != s {
		d.mu.Unlock()
		return
	}
	d.sess = nil
	d.configured = false
	d.exec = nil
	for path, bps := range d.bps {
		for i := range bps {
			bps[i].state = bpPending
		}
		d.gens[path]++
	}
	for path := range d.buffers {
		d.publishLocked(path)
	}
	restart := d.restartPending && d.last != nil
	d.restartPending = false
	// 再生中の操作が停止を待っていれば、止まらずに終わったので、カーソルを動かさずに応答させる。
	// 開始に失敗したか、デバッグアダプターが異常終了したなら失敗にして、再生を止める（FR-19）。
	// debug.restart で始め直すときは、始め直した後の停止まで待たせる。
	var w *waiter
	if !restart {
		w = d.takeStopWaiterLocked()
	}
	d.mu.Unlock()
	w.resolve(ext.ProviderInvokeResult{OK: !quiet && !abnormal})
	switch {
	case quiet:
	case abnormal:
		d.showError("デバッグアダプターが異常終了した")
	default:
		d.showInfo("デバッグを終了した")
	}
	if restart {
		// debug.restart で止めたので、前回の設定で始め直す。失敗は startLocked が知らせる。
		d.mu.Lock()
		if res, _ := d.startLocked(d.last, false); !res.OK {
			d.takeStopWaiterLocked().resolve(ext.ProviderInvokeResult{OK: false})
		}
		d.mu.Unlock()
	}
}

// publishLocked は、path のバッファの印（ブレークポイントと実行位置）のすべてを sign/set で送る。
// バッファを開いていなければ何もしない。mu を持って呼ぶ。
//
// 印は、行の昇順のブレークポイントの後に実行位置を並べる。1 つの行に複数の印があるときの表示の優先順位は本体が決める（基本設計 6.7）。
func (d *debugger) publishLocked(path string) {
	buf, ok := d.buffers[path]
	if !ok {
		return
	}
	signs := []ext.Sign{}
	for _, x := range d.bps[path] {
		kind := ext.SignBreakpoint
		if x.state == bpUnverified {
			kind = ext.SignBreakpointUnverified
		}
		signs = append(signs, ext.Sign{Line: x.line, Kind: kind})
	}
	if d.exec != nil && d.exec.path == path {
		signs = append(signs, ext.Sign{Line: d.exec.line, Kind: ext.SignExecutionPoint})
	}
	if err := d.srv.SetSigns(ext.SignSetParams{Buffer: buf.id, Version: buf.version, Signs: signs}); err != nil {
		d.logf("sign/set: %v", err)
	}
}
