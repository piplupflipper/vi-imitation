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
	"github.com/piplupflipper/vi-imitation/ext/expand"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// providedOps は、この拡張機能が引き受ける本体の操作である（基本設計 5.5。FR-94）。
// 宣言ファイル manifest.json の provides と揃える（TestManifest）。
// debug.toggleUI は、DAP のペイン（FR-108）を開け閉めする（pane.go）。
var providedOps = []ext.Operation{
	ext.OpBreakpointToggle, ext.OpBreakpointConditional, ext.OpBreakpointClear,
	ext.OpDebugContinue, ext.OpDebugPause, ext.OpDebugTerminate, ext.OpDebugRestart, ext.OpDebugRunLast,
	ext.OpDebugStepOver, ext.OpDebugStepIn, ext.OpDebugStepOut, ext.OpDebugToggleUI,
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
	// sessions は、動いているセッション（最上位のセッションと子のセッション）を、始めた順に並べたものである（FR-285）。
	// 最上位のセッションは一度に 1 つで、デバッグ中かどうかは sessions が空でないかで決める。
	sessions []*session
	// cur は、今のセッション（止まったときに今のセッションが無いか動いていたセッション。markStopped と targetLocked。
	// selectSession で選び直せる）である。sessions が空なら nil。
	cur *session
	// nextID は、次に振るセッションの番号である。
	nextID int
	// bpRes は、ファイルのパスごとの、セッションごとの最後の setBreakpoints の応答である（applyBreakpoints）。
	bpRes map[string]map[*session]bpResult
	// onSelect は、今のセッションか、その選んでいるスレッドかフレームが変わったときに呼ぶ関数である（後の issue のデバッグのペインが使う）。
	// mu を持たずに呼ぶ。nil なら呼ばない。
	onSelect func(selection)
	// shutdownOnce は、shutdown の後始末を 1 回だけ行うためのものである。
	shutdownOnce sync.Once
	// last は、最後に始めたデバッグの、値を決め終えた構成とアダプターの起動の方法である。まだ始めていなければ nil。
	last *runSpec
	// preparing は、デバッグを始める準備の途中の仕事である。無ければ nil。
	preparing *prepJob
	// pickers は、開いている一覧（picker.go）の知らせの受け口である。
	pickers pickers
	// apiVersion は、本体の拡張機能 API の版（initialize の apiVersion）である。apiWarned は、古いことを知らせたことを表す。
	apiVersion string
	apiWarned  bool
	// compoundsShown は、compounds を扱えないことを知らせた launch.json のパスである（拡張機能のプロセスが動いている間に 2 回出さない）。
	compoundsShown map[string]bool
	// restartPending は、debug.restart で止めたセッションが終わったら、last で始め直すことを表す。
	restartPending bool
	// stopWaiter は、再生中（sync）に呼ばれた、デバッグ対象を動かす操作の応答の受け口である（waitStopLocked）。
	// 次の停止（showStopped、stoppedWithoutSource）かデバッグの終わり（sessionEnded）で結果を届ける。無ければ nil。
	stopWaiter *waiter
	// waitScope は、stopWaiter が待つセッションである。そのセッションか、その子孫のセッションの停止と、そのセッションの終わりで結果を届ける。
	// nil なら、どのセッションの停止でも届ける（debug.restart で、始め直すセッションをまだ作っていない間）。
	waitScope *session
	// waitTree は、waitScope の子孫のセッションの停止でも結果を届けるかである（開始と restart）。
	waitTree bool
	// pane は、デバッグの情報を出すペインである（pane.go。FR-108）。作った後は変えない。
	pane *pane
	// outLogs は、今のデバッグ（最後に始めた最上位のセッションとその子孫）のセッションを、始めた順に並べたものである。
	// 終わったセッションも、次に最上位のセッションを始めるまで残し、出力の区画でその出力の写しを読めるようにする（FR-291）。
	outLogs []*session
	// outputTap は、テストが、セッションの出力の写しに足したテキストを受け取る関数である。nil なら呼ばない。作った後は変えない。
	outputTap func(text string)
	// excEnabled は、アダプターの名前ごとの、利用者が例外の区画で切り替えた例外のフィルターの有効と無効である（FR-294。exceptions.go）。
	// 拡張機能のプロセスが動いている間、次のセッションの開始でも送る。
	excEnabled map[string]map[string]bool
	// excFilters は、アダプターの名前ごとの、最後に initialize の応答で受けた例外のフィルターである。excLast は、最後に受けたアダプターの名前で、
	// セッションが無いときに例外の区画に出す。
	excFilters map[string][]exceptionFilter
	excLast    string
}

// bpResult は、1 つのセッションの、1 つのファイルの setBreakpoints の応答である。gen は、送ったときのブレークポイントの列の世代（debugger.gens）である。
type bpResult struct {
	gen uint64
	res []breakpoint
}

// selection は、今のセッションと、そのセッションの選んでいるスレッドとフレームである（基本設計 6.4 の「セッションと状態の分離」）。
// 後の issue のデバッグのペイン（スレッドとスタックの木、スコープ、ウォッチ式、REPL）は、これを引いて表示を作る。
type selection struct {
	// session は、今のセッションである。デバッグ中でなければ nil。
	session *session
	// thread は、選んでいるスレッドの ID である。hasThread が false なら未知。
	thread    int
	hasThread bool
	// frame は、選んでいるフレームの ID である。hasFrame が false なら未知。
	frame    int
	hasFrame bool
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
		if !d.apiSupported(p.Operation) {
			reply(ext.ProviderInvokeResult{OK: false}, nil)
			return
		}
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

// waitStopLocked は、セッション scope（tree なら、とその子孫）の次の停止か、scope の終わりに結果を届ける waiter を置いて返す。
// mu を持って呼ぶ。scope が nil なら、どのセッションの停止でも届ける。
// 前の waiter が残っていれば（本体が待つのをやめた後の取り消しがまだ届いていないなど）、失敗を届けて置き換える。
//
// 複数のセッションがあるとき、再生中の続行、ステップ実行、一時停止（FR-19）は、その操作を送ったセッションだけの停止を待つ（基本設計 5.5）。
// デバッグの開始と restart は、最上位のセッションか、その子孫のセッションの最初の停止を待つ（vscode-js-debug は子のセッションで止まる）。
//
// req: FR-19
func (d *debugger) waitStopLocked(scope *session, tree bool) *waiter {
	d.stopWaiter.resolve(ext.ProviderInvokeResult{OK: false})
	d.stopWaiter = newWaiter()
	d.waitScope, d.waitTree = scope, tree
	return d.stopWaiter
}

// takeStopWaiterLocked は、置いてある waiter を外して返す。無ければ nil。mu を持って呼ぶ。
func (d *debugger) takeStopWaiterLocked() *waiter {
	w := d.stopWaiter
	d.stopWaiter = nil
	d.waitScope = nil
	return w
}

// takeStopWaiterForLocked は、置いてある waiter が s の停止を待っていれば（s が待つセッションかその子孫なら）外して返す。
// そうでなければ nil。mu を持って呼ぶ。
func (d *debugger) takeStopWaiterForLocked(s *session) *waiter {
	if d.stopWaiter == nil || !d.inScopeLocked(s) {
		return nil
	}
	return d.takeStopWaiterLocked()
}

// inScopeLocked は、s が、置いてある waiter の待つセッション（waitTree なら、かその子孫）か（待つセッションが nil なら常に）を返す。
// mu を持って呼ぶ。
func (d *debugger) inScopeLocked(s *session) bool {
	if d.waitScope == nil || s == d.waitScope {
		return true
	}
	if !d.waitTree {
		return false
	}
	for x := s.parent; x != nil; x = x.parent {
		if x == d.waitScope {
			return true
		}
	}
	return false
}

// dropStopWaiter は、w が置いてある waiter なら外す。
func (d *debugger) dropStopWaiter(w *waiter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopWaiter == w {
		d.stopWaiter = nil
		d.waitScope = nil
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
		bpRes:    make(map[string]map[*session]bpResult),
		pickers: pickers{
			waiting: make(map[ext.ViewID]chan server.ViewEvent),
			early:   make(map[ext.ViewID][]server.ViewEvent),
		},
		compoundsShown: make(map[string]bool),
	}
	d.srv = d.newServer()
	d.pane = newPane(d)
	// 今のセッションが替わったら、出力の区画に出すセッションを合わせる。スレッドとスコープの区画の印は、選び直し（choose）と、
	// 止まった位置を出した後（showStopped）で付ける（止まったときにここで付けると、止まった位置の stackTrace より先に読む）。
	// 例外の区画は今のセッションのアダプターのフィルターを出すので、今のセッションが替わったら作り直す（PR #425 のレビューの推奨 5）。
	d.onSelect = func(selection) { d.pane.markDirty(secConsole, secExceptions) }
	go d.pane.run()
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
	s.ProviderAsync(ext.OpDebugToggleUI, d.asyncProvider(d.noArgs(d.toggleUI)))
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
		defer d.pane.stop()
		d.mu.Lock()
		s := d.rootLocked()
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

// rootLocked は、最上位のセッションを返す。デバッグ中でなければ nil。mu を持って呼ぶ。
func (d *debugger) rootLocked() *session {
	for _, s := range d.sessions {
		if s.parent == nil {
			return s
		}
	}
	return nil
}

// addSessionLocked は、s に番号を振り、動いているセッションに足す。子のセッションは親の children にも足す。
// 今のセッションが無ければ、s を今のセッションにする。mu を持って呼ぶ。
//
// 最上位のセッションなら、前のデバッグのセッションの出力の写しを捨てる（次にデバッグを始めるまで読める。FR-291）。
func (d *debugger) addSessionLocked(s *session) {
	d.nextID++
	s.id = d.nextID
	d.sessions = append(d.sessions, s)
	if s.parent == nil {
		for _, old := range d.outLogs {
			old.output.Detach()
		}
		d.outLogs = nil
	}
	d.outLogs = append(d.outLogs, s)
	d.pane.markDirty(secThreads, secScopes, secWatches, secExceptions, secConsole)
	if s.parent != nil {
		s.parent.children = append(s.parent.children, s)
	}
	if d.cur == nil {
		d.cur = s
	}
}

// selectionLocked は、今の選択を返す。mu を持って呼ぶ。
func (d *debugger) selectionLocked() selection {
	if d.cur == nil {
		return selection{}
	}
	s := d.cur
	return selection{session: s, thread: s.thread, hasThread: s.hasThread, frame: s.frame, hasFrame: s.hasFrame}
}

// currentSelection は、今のセッションと、その選んでいるスレッドとフレームを返す（後の issue のデバッグのペインが引く）。
func (d *debugger) currentSelection() selection {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.selectionLocked()
}

// notifySelect は、選択が変わったことを onSelect へ知らせる。mu を持たずに呼ぶ。
func (d *debugger) notifySelect(sel selection) {
	d.mu.Lock()
	f := d.onSelect
	d.mu.Unlock()
	if f != nil {
		f(sel)
	}
}

// selectSession は、s を今のセッションにする（後の issue のスレッドの区画でセッションを選んだとき）。s の世代を進める（FR-287）。
// s が動いていなければ false を返す。
//
// req: FR-287
func (d *debugger) selectSession(s *session) bool {
	return d.choose(s, func() {})
}

// selectThread は、s を今のセッションにし、そのスレッド thread を選び（続行、ステップ実行、一時停止の送り先にする）、
// フレームの選択を外す（スレッドの区画の s で選んだとき。FR-303）。s の世代を進めるので、それより前に送った stackTrace などの応答は捨てる（FR-287）。
// s が動いていなければ false を返す。
//
// req: FR-287
func (d *debugger) selectThread(s *session, thread int) bool {
	return d.choose(s, func() { s.thread, s.hasThread, s.frame, s.hasFrame, s.frameAt = thread, true, 0, false, nil })
}

// selectThreadFrame は、selectThread と同じく s のスレッド thread を選び、フレーム f も選ぶ（スレッドの区画の s で、
// スレッドかフレームを選んだとき。スコープの区画がそのフレームを出す）。
//
// req: FR-287
func (d *debugger) selectThreadFrame(s *session, thread int, f stackFrame) bool {
	return d.choose(s, func() {
		s.thread, s.hasThread, s.frame, s.hasFrame, s.frameAt = thread, true, f.ID, true, frameLocOf(f)
	})
}

// selectFrame は、s を今のセッションにし、フレーム f を選ぶ（スレッドの区画でフレームを <CR> で選んだとき）。
// 選んでいるスレッド（続行とステップ実行の送り先）は替えない。別のスレッドのフレームを見るために選んでも、ステップ実行が黙って
// そのスレッドへ送られないためである（nvim-dap（コミット cfa2d58f4537aca6ca83e2de1a0d9f1491121264）の lua/dap/session.lua の
// _frame_set は current_frame だけを替え、ステップ実行は stopped_thread_id へ送る。PR #423 のレビューの裁定要 1）。
// s の世代を進める（FR-287）。s が動いていなければ false を返す。
//
// req: FR-287
func (d *debugger) selectFrame(s *session, f stackFrame) bool {
	return d.choose(s, func() { s.frame, s.hasFrame, s.frameAt = f.ID, true, frameLocOf(f) })
}

// choose は、s を今のセッションにし、mu を持って set を呼び、s の世代を進めて、選択が変わったことを知らせる。
func (d *debugger) choose(s *session, set func()) bool {
	d.mu.Lock()
	if !slices.Contains(d.sessions, s) {
		d.mu.Unlock()
		return false
	}
	d.cur = s
	set()
	s.gen.Add(1)
	d.pane.markDirty(secThreads, secScopes, secWatches)
	sel := d.selectionLocked()
	d.mu.Unlock()
	d.notifySelect(sel)
	return true
}

// markStopped は、s が止まった（stopped の event）ことを記録する。止まったスレッド tid（nil は未知）を選び、
// フレームの選択を外し、世代を進めて、その世代を返す（FR-285、FR-287）。下の条件のときだけ s を今のセッションにする。
//
// s を今のセッションにするのは、今のセッションが無いか、s か、止まっていないときだけである（nvim-dap（コミット
// cfa2d58f4537aca6ca83e2de1a0d9f1491121264）の lua/dap.lua の listeners.after.event_stopped['dap.sessions'] と同じ）。
// 今のセッションが止まっていれば、利用者がそのセッションを調べている途中なので、今のセッションを替えず、カーソルも移さない。
// 再生中（sync）の操作が別のセッションの停止を待っているとき（s が待っているセッションの子孫でないとき）も、替えない
// （カーソルが動くと本体が再生を止め、再生の次の操作が別のセッションへ届くため）。
// focus は、s を今のセッションにしたか（止まった位置へカーソルを移すか）である。
//
// req: FR-285
func (d *debugger) markStopped(s *session, tid *int) (gen uint64, focus bool) {
	d.mu.Lock()
	s.halted = true
	s.thread, s.hasThread, s.frame, s.hasFrame, s.frameAt = 0, tid != nil, 0, false, nil
	if tid != nil {
		s.thread = *tid
	}
	s.stoppedThread, s.hasStoppedThread = s.thread, s.hasThread
	// ペインの区画の作り直しの印は、ここでは付けない。付けると、ペインの読み込み（threads、stackTrace、scopes、variables）が、
	// 止まった位置を出すための stackTrace より先にアダプターへ届き、カーソルの移動が遅れる（PR #423 のレビューの必須 1）。
	// 印は、止まった位置を出し終えた後（showStopped と stoppedWithoutSource）で付ける。
	gen = s.gen.Add(1)
	s.stopGen = gen
	cur := d.cur
	focus = cur == nil || cur == s || !slices.Contains(d.sessions, cur) || !cur.halted
	if focus && d.stopWaiter != nil && !d.inScopeLocked(s) {
		focus = false
	}
	focus = focus && slices.Contains(d.sessions, s)
	if focus {
		d.cur = s
	}
	sel := d.selectionLocked()
	d.mu.Unlock()
	if focus {
		d.notifySelect(sel)
	}
	return gen, focus
}

// targetLocked は、続行とステップ実行を送るセッションを返す。今のセッションが止まっていればそれ、止まっていなければ、
// 止まっているセッション（始めた順の最初のもの）を今のセッションにしてそれを返す（nvim-dap の lua/dap.lua の first_stopped_session と同じ）。
// どれも止まっていなければ今のセッションを返す。changed は、今のセッションを替えたかである。mu を持って呼ぶ。
//
// req: FR-285
func (d *debugger) targetLocked() (s *session, changed bool) {
	if d.cur != nil && d.cur.halted {
		return d.cur, false
	}
	for _, x := range d.sessions {
		if x.halted {
			d.cur = x
			x.redirected = true
			return x, true
		}
	}
	return d.cur, false
}

// continued は、s の continued の event を処理する。s の実行位置の印を消し、世代を進める（FR-287）。
func (d *debugger) continued(s *session) {
	d.mu.Lock()
	if s.halted {
		s.resumedAt = time.Now()
	}
	s.halted = false
	s.gen.Add(1)
	d.pane.markDirty(secThreads, secScopes, secWatches)
	d.mu.Unlock()
	d.clearExecution(s)
}

// toggleUI は、デバッグの情報のペインを開け閉めする（本体の操作 debug.toggleUI。FR-277）。閉じてもデバッグは止めない。
// pane/open は応答を待つので、ペインの goroutine で行い、受け付けたことだけを応答する（再生中も同じ）。
//
// req: FR-277
func (d *debugger) toggleUI(context.Context, *server.Buffer, *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.pane.toggle()
	return ok()
}

// minAPIVersion は、この拡張機能が要る本体の拡張機能 API の版である（基本設計 1.4 の「古い本体での標準の拡張機能」。FR-306）。
// 値は、使うメッセージのうち最後に足したものの版で、今は切り替えの部品（pane/open の switcher、子を指す view/update、
// view/childSelect、popupKeys の children と子を切り替える action。API 0.23.0）である。
// view/append と server.TextLog は 0.20.0、input/ask は 0.21.0、progress/report（resolveCommand の進み具合）は 0.14.0、
// pane/open の center と closeOnSelect（構成の候補と ${pickProcess} の一覧）は 0.10.0、editor/state の workDir は 0.9.0 で足した。
const minAPIVersion = "0.23.0"

// apiSupported は、本体の拡張機能 API の版が minAPIVersion 以上かを返す。
// 足りなければ、最初の 1 回だけ要る版をメッセージの行に出す。操作は、段階 2 の取り決めだけを使うものも含めてすべて断る（基本設計 1.4）。
// 前の形（1 つの部品のペイン、output/append）には代えない。
//
// req: FR-306
func (d *debugger) apiSupported(op ext.Operation) bool {
	d.mu.Lock()
	old := ext.VersionLess(d.apiVersion, minAPIVersion)
	first := old && !d.apiWarned
	if first {
		d.apiWarned = true
	}
	version := d.apiVersion
	d.mu.Unlock()
	if first {
		d.showError(fmt.Sprintf("DAP の拡張機能には本体の拡張機能 API %s 以上が要ります（この本体は %s）", minAPIVersion, version))
	}
	if old {
		d.logf("%s: refused (host API %s < %s)", op, version, minAPIVersion)
	}
	return !old
}

func (d *debugger) onInitialize(_ context.Context, e *server.InitializeEvent) error {
	d.mu.Lock()
	d.apiVersion = e.APIVersion
	d.mu.Unlock()
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
	if len(d.bps[path]) > 0 || slices.ContainsFunc(d.sessions, func(s *session) bool { return s.exec != nil && s.exec.path == path }) {
		d.publishLocked(path)
	}
	// 選んでいるフレームのファイルを後から開いたとき（開いていないファイルのフレーム）と、ファイルの種類が変わったとき（コメントの記号）に、
	// 行末の変数の値を出し直す（FR-296）。
	d.pane.bufferOpened(e.Buffer.ID(), d.settings.vt.enabled)
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
	for _, s := range d.sessions {
		if s.exec != nil && s.exec.path == path {
			for _, ed := range e.Edits {
				s.exec.line = shiftLine(s.exec.line, ed)
			}
		}
		if s.frameAt != nil && s.frameAt.path == path {
			at := *s.frameAt
			for _, ed := range e.Edits {
				at.line = shiftLine(at.line, ed)
			}
			s.frameAt = &at
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
	// 本体はバッファごと仮想テキストを消すので、送らずに覚えたものを捨てる。
	d.pane.bufferClosed(e.Buffer, d.settings.vt.enabled)
}

// onBufferDidSave は、デバッグ中なら、保存したファイルのブレークポイントを setBreakpoints で送り直す（基本設計 6.4）。
// 保存まで送り直さないのは、デバッグ対象が実行するのは保存されたファイルだからである。
func (d *debugger) onBufferDidSave(_ context.Context, e *server.BufferDidSaveEvent) {
	path := e.Params.Path
	if path == "" && e.Buffer != nil {
		path = e.Buffer.Path()
	}
	if path == "" {
		return
	}
	d.mu.Lock()
	targets := d.configuredLocked()
	d.mu.Unlock()
	for _, s := range targets {
		go d.sendBreakpoints(s, path) //nolint:gosec // G118: ハンドラーとフックの ctx は戻ると無効になるので引き継がない
	}
}

// configuredLocked は、setBreakpoints をその都度送るセッション（初期化で送る対象を決め終えたもの）を返す。
// ブレークポイントは、すべてのセッションに送る（FR-285）。mu を持って呼ぶ。
func (d *debugger) configuredLocked() []*session {
	var out []*session
	for _, s := range d.sessions {
		if s.configured {
			out = append(out, s)
		}
	}
	return out
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
	targets := d.configuredLocked()
	d.mu.Unlock()
	if len(targets) == 0 {
		return ok()
	}
	// ハンドラーの ctx は戻ると無効になるので引き継がない。
	return d.sendBreakpointsAll(targets, []string{path}, sync)
}

// sendBreakpointsAll は、paths のブレークポイントを、セッション targets のそれぞれへ、ループの外で setBreakpoints で送る（FR-285）。
// wait（再生中）なら、すべて送り終えてから成功を届ける waiter を返す。
func (d *debugger) sendBreakpointsAll(targets []*session, paths []string, wait bool) (ext.ProviderInvokeResult, *waiter) {
	if !wait {
		for _, s := range targets {
			for _, path := range paths {
				go d.sendBreakpoints(s, path)
			}
		}
		return ok()
	}
	w := newWaiter()
	go func() {
		var wg sync.WaitGroup
		for _, s := range targets {
			wg.Go(func() {
				for _, path := range paths {
					d.sendBreakpoints(s, path)
				}
			})
		}
		wg.Wait()
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
	targets := d.configuredLocked()
	d.mu.Unlock()
	d.showInfo(fmt.Sprintf("ブレークポイントを消した（%d ファイル）", len(paths)))
	if len(targets) == 0 {
		return ok()
	}
	return d.sendBreakpointsAll(targets, paths, p.Sync)
}

// continueOrStart は、本体の操作 debug.continue を処理する。デバッグ中なら続行し、始まっていなければ始める（要件定義 4.11）。
//
// req: FR-31, FR-94
func (d *debugger) continueOrStart(ctx context.Context, b *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	running := len(d.sessions) > 0
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

// restart は、デバッグを始め直す（本体の操作 debug.restart）。
// デバッグ中でなければ、runLast と同じく前回の設定で始める。
// 最上位のセッションのアダプターが restart に対応していれば（supportsRestartRequest）、そのセッションへ restart を送る（FR-286）。
// 引数は、前回の構成（DAP の仕様の RestartArguments の「The latest version of the launch or attach configuration」）である。
// 対応していなければ、debug.terminate と同じ手順で終えてから、終わった後（sessionEnded）に前回の設定で始める。
// 再生中（sync）なら、始め直したデバッグ対象が最初に止まるまで応答を遅らせる（start と同じ）。
//
// req: FR-94, FR-286
func (d *debugger) restart(ctx context.Context, b *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	s := d.rootLocked()
	if s == nil {
		d.mu.Unlock()
		return d.runLast(ctx, b, p)
	}
	byRequest := s.supports(func(c *capabilities) bool { return c.SupportsRestartRequest })
	var w *waiter
	switch {
	case byRequest && p.Sync:
		w = d.waitStopLocked(s, true)
	case byRequest:
	default:
		d.restartPending = true
		if p.Sync {
			// 始め直すのは sessionEnded なので、waiter は終わりでは外さずに、始め直した後の停止まで残す。
			w = d.waitStopLocked(nil, true)
		}
	}
	d.mu.Unlock()
	if byRequest {
		go d.restartByRequest(s, w) //nolint:gosec // G118: ハンドラーの ctx は応答すると取り消されるので引き継がない
	} else {
		s.stopping.Store(true)
		go s.stop(context.Background(), killGrace) //nolint:gosec // ハンドラーが戻った後も止め終えるまで続けるので、ハンドラーの ctx は使わない
	}
	if w != nil {
		return ext.ProviderInvokeResult{}, w
	}
	return ok()
}

// restartByRequest は、s へ restart を送る（FR-286）。送る前に、実行位置の印を消し、世代を進める。
// 失敗したら知らせ、再生中の waiter に失敗を届ける。
func (d *debugger) restartByRequest(s *session, w *waiter) {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeouts.launch)
	defer cancel()
	c := s.getConn()
	if c == nil {
		return
	}
	d.mu.Lock()
	s.halted = false
	s.resumedAt = time.Now()
	s.gen.Add(1)
	d.pane.markDirty(secThreads, secScopes, secWatches)
	d.mu.Unlock()
	d.clearExecution(s)
	if err := c.call(ctx, "restart", restartArguments{Arguments: expand.Plain(s.spec.config)}, nil); err != nil {
		d.logf("restart: %v", err)
		d.dropStopWaiter(w)
		w.resolve(ext.ProviderInvokeResult{OK: false})
		d.showError(fmt.Sprintf("restart に失敗した: %v", err))
		return
	}
	d.showInfo(fmt.Sprintf("デバッグを始め直した（%s）", s.spec.name))
}

// pause は、実行中のデバッグ対象を一時停止する（本体の操作 debug.pause）。止まったことは stopped の event で知らせる。
// pause は、今のセッションの選んでいるスレッドへ送る（FR-285）。
// 再生中（sync）なら、止まるまで応答を遅らせる。
// 再生中で既に止まっていれば、pause を送らずにすぐに応答する。送ると、デバッグアダプターによっては stopped の event が後から届き、
// 再生の次の操作（続行など）の停止として取り違えるためである。
//
// req: FR-94, FR-19, FR-285
func (d *debugger) pause(_ context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	s := d.cur
	ready := s != nil && s.configured
	stopped := s != nil && s.halted
	var w *waiter
	if ready && p.Sync && !stopped {
		w = d.waitStopLocked(s, false)
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
		target, tid, err := d.pauseTarget(ctx, s)
		if err == nil {
			d.mu.Lock()
			if w != nil && d.stopWaiter == w {
				d.waitScope = target
			}
			d.mu.Unlock()
			err = target.pause(ctx, tid)
		}
		if err != nil && !errors.Is(err, errDAPClosed) {
			d.logf("pause: %v", err)
			d.dropStopWaiter(w)
			w.resolve(ext.ProviderInvokeResult{OK: false})
			if errors.Is(err, errNoThreads) {
				d.showError("一時停止できるスレッドが無い（どのセッションの threads も空）")
				return
			}
			d.showError(fmt.Sprintf("pause に失敗した: %v", err))
		}
	}()
	if w != nil {
		return ext.ProviderInvokeResult{}, w
	}
	return ok()
}

// errNoThreads は、一時停止を送れるスレッドが、どのセッションにも無いことを表す。
var errNoThreads = errors.New("no threads to pause")

// pauseTarget は、一時停止を送るセッションとスレッドを決める。s がスレッドを選んでいればそれにする。
// 選んでいなければ（一度も止まっていない）、s、後に始めたセッションの順に threads を問い合わせ、スレッドを持つ最初のセッションの
// 最初のスレッドにする。vscode-js-debug の親のセッションはデバッグ対象を持たず、threads が空なので、一度も止まる前の一時停止は、
// 子のセッションへ送る（PR #414 のレビューの推奨 1）。どのセッションにもスレッドが無ければ errNoThreads を返す。
func (d *debugger) pauseTarget(ctx context.Context, s *session) (*session, int, error) {
	d.mu.Lock()
	if s.hasThread {
		tid := s.thread
		d.mu.Unlock()
		return s, tid, nil
	}
	cands := []*session{s}
	for i := len(d.sessions) - 1; i >= 0; i-- {
		if x := d.sessions[i]; x != s && x.configured {
			cands = append(cands, x)
		}
	}
	d.mu.Unlock()
	for _, x := range cands {
		c := x.getConn()
		if c == nil {
			continue
		}
		var res threadsResponse
		if err := c.call(ctx, "threads", nil, &res); err != nil {
			d.logf("session %d: threads: %v", x.id, err)
			continue
		}
		if len(res.Threads) > 0 {
			return x, res.Threads[0].ID, nil
		}
	}
	return nil, 0, errNoThreads
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
	if d.preparing != nil || len(d.sessions) > 0 {
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
	if len(d.sessions) > 0 {
		return d.fail("デバッグは既に始まっている")
	}
	s, err := spawn(d, spec)
	if err != nil {
		return d.fail(fmt.Sprintf("デバッグアダプター %s を起動できない: %v", spec.adapter.name, err))
	}
	d.addSessionLocked(s)
	d.last = spec
	if d.settings.ui.autoOpen {
		// launch か attach を送る前に開く（FR-277）。開くのはペインの goroutine で、セッションの開始を待たせない。
		d.pane.requestOpen()
	}
	var w *waiter
	if sync {
		w = d.waitStopLocked(s, true)
	} else if d.stopWaiter != nil && d.waitScope == nil {
		// debug.restart（再生中）が始め直すのを待っている。始め直したセッションとその子孫の停止を待たせる。
		d.waitScope, d.waitTree = s, true
	}
	go s.run()
	if w != nil {
		return ext.ProviderInvokeResult{}, w
	}
	return ok()
}

// stop は、デバッグを終える（基本設計 6.4。本体の操作 debug.terminate）。disconnect は別の goroutine で送る。
// 最上位のセッションを終え、その前に子のセッションをすべて終える（FR-285）。
// 再生中（sync）なら、終え終わる（session.stop が戻る）まで応答を遅らせる。
//
// req: FR-31, FR-94
func (d *debugger) stop(_ context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	d.mu.Lock()
	s := d.rootLocked()
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
// 送る先は、今のセッションが止まっていればそのセッション、止まっていなければ止まっているセッション（targetLocked）の、
// 選んでいるスレッドである（FR-285）。今のセッションが動き出した後も、ほかに止まっているセッションを続けられる。
// 再生中（sync）なら、そのセッションのデバッグ対象が次に止まるか、そのセッションが終わるまで応答を遅らせ、止まった位置を応答の open で返す
// （showStopped）。DAP の request の応答は、デバッグ対象が動き始めたことしか表さない（next の応答の後に stopped の event が届く）ので、
// request の応答で答えると、再生の次のステップ実行が、まだ動いているデバッグ対象へ届いて失敗する（#210）。
//
// req: FR-31, FR-94, FR-19, FR-285
func (d *debugger) resume(_ context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, *waiter) {
	command := resumeCommands[p.Operation]
	d.mu.Lock()
	s, changed := d.targetLocked()
	ready := s != nil && s.configured
	var w *waiter
	if ready && p.Sync {
		// request を送る前に置き、request の応答より先に届く stopped の event も取りこぼさない。
		w = d.waitStopLocked(s, false)
	}
	sel := d.selectionLocked()
	d.mu.Unlock()
	if changed {
		d.notifySelect(sel)
		d.showInfo(fmt.Sprintf("今のセッションが動いているので、止まっているセッション %s へ %s を送る", s.label(), command))
	}
	if !ready {
		return d.fail("デバッグを始めていない")
	}
	prev := d.clearExecution(s)
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
			d.restoreExecution(s, prev)
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
	if !slices.Contains(d.sessions, s) {
		return nil
	}
	s.configured = true
	return slices.Sorted(maps.Keys(d.bps))
}

// snapshotBreakpoints は、path のブレークポイント（行と条件）の写しと、列を変えた回数を返す。
func (d *debugger) snapshotBreakpoints(path string) ([]bp, uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.bps[path]), d.gens[path]
}

// applyBreakpoints は、セッション s の setBreakpoints の応答を path のブレークポイントへ反映し、印を付け直す（基本設計 6.4）。
// 送った後に列が変わっていれば（gen が違えば）、応答を捨てる。
//
// ブレークポイントはすべてのセッションに送る（FR-285）ので、応答もセッションごとに覚え、mergeBreakpointsLocked で合わせる。
//
// req: FR-31, FR-285
func (d *debugger) applyBreakpoints(s *session, path string, gen uint64, res []breakpoint) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gens[path] != gen || !slices.Contains(d.sessions, s) {
		return
	}
	if d.bpRes[path] == nil {
		d.bpRes[path] = map[*session]bpResult{}
	}
	d.bpRes[path][s] = bpResult{gen: gen, res: res}
	d.mergeBreakpointsLocked(path)
}

// breakpointChanged は、セッション s の breakpoint の event（reason が changed）で、setBreakpoints の応答で ID が b.ID だった
// ブレークポイントの検証の結果と行を b に替え、印を付け直す（DAP の仕様の Breakpoint Event の「The id attribute is used to find
// the target breakpoint, the other attributes are used as the new values」）。
// vscode-js-debug は、setBreakpoints の応答では verified を false にし、デバッグ対象のスクリプトを読んだ後にこの event で検証を知らせる
// （2026-11-25 に、手元の vscode-js-debug の launch で確かめた）。
func (d *debugger) breakpointChanged(s *session, b breakpoint) {
	d.updateBreakpointResults(s, []int{b.ID}, func(breakpoint) breakpoint { return b })
}

// breakpointsHit は、セッション s が stopped の hitBreakpointIds で知らせた ID のブレークポイントを、検証できたものにする。
// vscode-js-debug の attach は、setBreakpoints の応答で verified を false にしたまま breakpoint の event を送らず、
// そのブレークポイントで止まる（2026-11-25 に、手元の vscode-js-debug で node --inspect のプロセスへ attach して確かめた）。
func (d *debugger) breakpointsHit(s *session, ids []int) {
	d.updateBreakpointResults(s, ids, func(b breakpoint) breakpoint {
		b.Verified = true
		return b
	})
}

// updateBreakpointResults は、セッション s の setBreakpoints の応答のうち、ID が ids のどれかのものを f で替え、印を付け直す。
func (d *debugger) updateBreakpointResults(s *session, ids []int, f func(breakpoint) breakpoint) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for path, byS := range d.bpRes {
		r, ok := byS[s]
		if !ok || r.gen != d.gens[path] {
			continue
		}
		changed := false
		for i := range r.res {
			if r.res[i].ID != 0 && slices.Contains(ids, r.res[i].ID) {
				if !changed {
					r.res = slices.Clone(r.res)
					changed = true
				}
				r.res[i] = f(r.res[i])
			}
		}
		if changed {
			byS[s] = r
			d.mergeBreakpointsLocked(path)
		}
	}
}

// mergeBreakpointsLocked は、path のブレークポイントの印を、セッションごとの応答（今の列の世代のもの）から決め直す。mu を持って呼ぶ。
// どれかのセッションが検証できたものは breakpoint、応答があってどのセッションも検証できなかったものは breakpointUnverified にする
// （vscode-js-debug の親のセッションのように、デバッグ対象を持たないセッションの応答で、子のセッションの検証を消さないため）。
// 応答の行が送った行と違えば、デバッグアダプターが実際に置いた行へ移す。行は、セッションを始めた順に、検証できた応答の行を先に採る。
// 移した結果、ブレークポイントが重なって数が変わったら、列の世代を進める（ほかのセッションの応答の並びと合わなくなるため）。
// 今の世代の応答が 1 つも無ければ、何もしない。
func (d *debugger) mergeBreakpointsLocked(path string) {
	gen := d.gens[path]
	var results [][]breakpoint
	for _, s := range d.sessions {
		if r, ok := d.bpRes[path][s]; ok && r.gen == gen {
			results = append(results, r.res)
		}
	}
	if len(results) == 0 {
		return
	}
	bps := slices.Clone(d.bps[path])
	for i := range bps {
		answered, verified, line, anyLine := false, false, 0, 0
		for _, res := range results {
			if i >= len(res) {
				continue
			}
			answered = true
			if res[i].Verified && !verified {
				verified = true
				line = res[i].Line
			}
			if anyLine == 0 {
				anyLine = res[i].Line
			}
		}
		if !answered {
			continue
		}
		bps[i].state = bpUnverified
		if verified {
			bps[i].state = bpVerified
		}
		if line == 0 {
			line = anyLine
		}
		if line > 0 {
			bps[i].line = fromDAPLine(line, linesStartAt1)
		}
	}
	merged := normalize(bps)
	if len(merged) != len(d.bps[path]) {
		d.gens[path]++
	}
	d.bps[path] = merged
	d.publishLocked(path)
}

// showStopped は、デバッグ対象が先頭のフレーム f（ソースのパスを持つもの）の line 行（本体の行。DAP の列は f の列）で止まったことを
// 表示する（基本設計 6.4）。
// editor/open でカーソルを移し、sign/set で実行位置の印を付け、message/show で止まった理由を知らせる。
// session の event の goroutine から呼ぶ。
//
// 再生中（sync）の操作がこの停止を待っていれば（stopWaiter）、editor/open を送らずに、止まった位置をその操作の応答の open で返す。
// 本体は応答を待つ間も editor/open を処理するが、送ると待つ間にカーソルが移り、本体が再生の続きで操作を実行し直すときに
// カーソルが変わったとみなして再生を止める（基本設計 5.4、6 章の冒頭、6.4）。
//
// 実行位置の印はセッションごとに持ち（FR-285）、選んでいるフレームを先頭のフレーム f にする。
// gen は、止まったときの s の世代で、ここまでに世代が進んでいれば（続けた、選び直した）、何も表示しない（FR-287）。
// 再生中の操作は、待っているセッション（とその子孫）の停止でだけ応答する（waitStopLocked）。
// focus が false（今のセッションにしなかった。markStopped）で、再生中の操作も待っていなければ、印とメッセージだけにし、カーソルを移さない。
//
// req: FR-31, FR-19, FR-285, FR-287
func (d *debugger) showStopped(ctx context.Context, s *session, gen uint64, focus bool, f stackFrame, line int, reason string) {
	path, column := f.Source.Path, f.Column
	d.mu.Lock()
	if s.gen.Load() != gen || !slices.Contains(d.sessions, s) {
		d.mu.Unlock()
		d.logf("session %d: stop at %s:%d is no longer current; dropped", s.id, path, line+1)
		return
	}
	s.exec = &execPoint{path: path, line: line}
	s.frame, s.hasFrame = f.ID, true
	s.frameAt = &frameLoc{path: path, line: line, name: f.Name}
	s.stopGen = 0
	s.shownAt = time.Now()
	// ペインの区画は、止まった位置への移動（editor/open か再生の応答）を済ませた後に作り直す（markStopped の doc）。
	defer d.pane.markDirty(secThreads, secScopes, secWatches)
	d.publishLocked(path)
	w := d.takeStopWaiterForLocked(s)
	buf, opened := d.buffers[path]
	multi := len(d.sessions) > 1
	redirected := s.redirected
	s.redirected = false
	isCur := d.cur == s
	sel := d.selectionLocked()
	d.mu.Unlock()
	if isCur {
		d.notifySelect(sel)
	}
	d.showInfo(fmt.Sprintf("%s: %s:%d", d.stoppedText(s, reason, multi, redirected), path, line+1))
	if !focus && w == nil {
		// 今のセッションでないセッションが止まった（markStopped）。印とメッセージだけにし、カーソルは移さない。
		return
	}
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
	if s.exec != nil && s.exec.path == path {
		line = s.exec.line
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

// stoppedText は、止まったことの知らせの文（位置を除く）を作る。セッションが 2 つ以上あればセッションの名前を添え、
// 続行かステップ実行をこのセッションへ替えて送った後（redirected）なら、そのことも添える
// （替えたことの知らせが、すぐ後のこの知らせで上書きされて読めないため。PR #414 の再レビューの推奨 B）。
func (d *debugger) stoppedText(s *session, reason string, multi, redirected bool) string {
	switch {
	case redirected:
		return fmt.Sprintf("停止した（%s、%s。今のセッションが動いていたので、このセッションへ送った）", reason, s.label())
	case multi:
		return fmt.Sprintf("停止した（%s、%s）", reason, s.label())
	default:
		return fmt.Sprintf("停止した（%s）", reason)
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
// gen は止まったときの s の世代で、ここまでに世代が進んでいれば何もしない（FR-287）。
//
// req: FR-19
func (d *debugger) stoppedWithoutSource(s *session, gen uint64, reason string) {
	d.mu.Lock()
	if s.gen.Load() != gen {
		d.mu.Unlock()
		return
	}
	s.stopGen = 0
	s.shownAt = time.Now()
	defer d.pane.markDirty(secThreads, secScopes, secWatches)
	w := d.takeStopWaiterForLocked(s)
	multi := len(d.sessions) > 1
	redirected := s.redirected
	s.redirected = false
	d.mu.Unlock()
	d.showInfo(d.stoppedText(s, reason, multi, redirected))
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

// clearExecution は、s の実行位置の印を消し、消す前の位置を返す（基本設計 6.4 の continued と terminated）。
// ほかのセッションの印は消さない（FR-285）。
func (d *debugger) clearExecution(s *session) *execPoint {
	d.mu.Lock()
	defer d.mu.Unlock()
	prev := s.exec
	if prev != nil {
		s.exec = nil
		d.publishLocked(prev.path)
	}
	return prev
}

// restoreExecution は、clearExecution で消した s の位置を戻す。その後に別の位置で止まっていれば何もしない。
func (d *debugger) restoreExecution(s *session, prev *execPoint) {
	if prev == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.exec == nil && slices.Contains(d.sessions, s) {
		s.exec = prev
		d.publishLocked(prev.path)
	}
}

// sessionEnded は、s の後始末の最後に呼ぶ。s を動いているセッションから外し、s の実行位置の印を消す。
// ほかのセッションが残れば、ほかのセッションの実行位置の印とブレークポイントの印は消さず、ブレークポイントの検証の印を、
// 残ったセッションの応答で決め直す（FR-285）。今のセッションが s なら、止まっているセッション、親、最初のセッションの順に選び直す。
// すべてのセッションが終わったら、ブレークポイントを未検証に戻し、利用者に知らせる。
// abnormal は、利用者の debug.terminate でも terminated の event でもなく、デバッグアダプターが終わったことを表す。
// quiet は、開始に失敗して既に知らせたので、終わりを知らせないことを表す。
//
// req: FR-31, FR-285
func (d *debugger) sessionEnded(s *session, abnormal, quiet bool) {
	d.mu.Lock()
	i := slices.Index(d.sessions, s)
	if i < 0 {
		d.mu.Unlock()
		return
	}
	d.sessions = slices.Delete(d.sessions, i, i+1)
	if p := s.parent; p != nil {
		if j := slices.Index(p.children, s); j >= 0 {
			p.children = slices.Delete(p.children, j, j+1)
		}
	}
	s.configured = false
	lateFailure := s.lateFailure
	prevExec := s.exec
	s.exec = nil
	for _, res := range d.bpRes {
		delete(res, s)
	}
	last := len(d.sessions) == 0
	if last {
		clear(d.bpRes)
		for path, bps := range d.bps {
			for i := range bps {
				bps[i].state = bpPending
			}
			d.gens[path]++
		}
		for path := range d.buffers {
			d.publishLocked(path)
		}
	} else {
		for path := range d.bps {
			d.mergeBreakpointsLocked(path)
		}
		if prevExec != nil {
			d.publishLocked(prevExec.path)
		}
	}
	selChanged := false
	if d.cur == s {
		d.cur = d.pickCurrentLocked(s)
		selChanged = true
	}
	sel := d.selectionLocked()
	restart := last && s.parent == nil && d.restartPending && d.last != nil
	if last {
		d.restartPending = false
	}
	d.pane.markDirty(secThreads, secScopes, secWatches, secExceptions, secConsole)
	if last && !restart && d.settings.ui.autoClose {
		// すべてのセッションが終わったら閉じる（FR-277）。debug.restart で始め直すときは閉じない。
		d.pane.requestClose()
	}
	// 再生中の操作が停止を待っていれば、止まらずに終わったので、カーソルを動かさずに応答させる。
	// 開始に失敗したか、デバッグアダプターが異常終了したなら失敗にして、再生を止める（FR-19）。
	// 待っているのが s でなければ（親のセッションの操作で、子のセッションが終わった）、待たせ続ける。
	// debug.restart で始め直すときは、始め直した後の停止まで待たせる。
	var w *waiter
	switch {
	case restart:
	case d.waitScope == s || last:
		w = d.takeStopWaiterLocked()
	}
	d.mu.Unlock()
	if selChanged {
		d.notifySelect(sel)
	}
	w.resolve(ext.ProviderInvokeResult{OK: !quiet && !abnormal})
	// 後から届いた失敗（dlv の restart のビルドの失敗）は、終わりの知らせに添える。アダプターは失敗の応答の後に terminated を送るので、
	// 失敗の知らせだけを出すと、すぐ後の終わりの知らせで上書きされて読めない（PR #414 の再レビューの推奨 B）。
	suffix := ""
	if lateFailure != "" {
		suffix = "（" + lateFailure + "）"
	}
	switch {
	case s.parent != nil && abnormal:
		d.showError(fmt.Sprintf("子のセッション %s のデバッグアダプターが異常終了した%s", s.label(), suffix))
	case s.parent != nil && suffix != "":
		d.showError(fmt.Sprintf("子のセッション %s が終わった%s", s.label(), suffix))
	case s.parent != nil:
		d.logf("session %d: child session %q ended", s.id, s.label())
	case quiet:
	case abnormal:
		d.showError("デバッグアダプターが異常終了した" + suffix)
	case suffix != "":
		d.showError("デバッグを終了した" + suffix)
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
	// ブレークポイントの区画も作り直す（ブレークポイントの列か検証の印が変わったとき、ここを通る）。
	d.pane.markDirty(secBreakpoints)
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
	// 実行位置の印は、止まっているセッションごとに付ける（FR-285）。同じ行に重なれば 1 つにする。
	var execLines []int
	for _, s := range d.sessions {
		if s.exec != nil && s.exec.path == path && !slices.Contains(execLines, s.exec.line) {
			execLines = append(execLines, s.exec.line)
		}
	}
	for _, line := range execLines {
		signs = append(signs, ext.Sign{Line: line, Kind: ext.SignExecutionPoint})
	}
	if err := d.srv.SetSigns(ext.SignSetParams{Buffer: buf.id, Version: buf.version, Signs: signs}); err != nil {
		d.logf("sign/set: %v", err)
	}
}

// pickCurrentLocked は、今のセッション ended が終わったときに、次の今のセッションを選ぶ。
// 止まっているセッション（始めた順）、ended の親、最初のセッションの順で、無ければ nil。mu を持って呼ぶ。
func (d *debugger) pickCurrentLocked(ended *session) *session {
	for _, s := range d.sessions {
		if s.halted {
			return s
		}
	}
	if p := ended.parent; p != nil && slices.Contains(d.sessions, p) {
		return p
	}
	if len(d.sessions) > 0 {
		return d.sessions[0]
	}
	return nil
}
