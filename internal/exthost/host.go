// Package exthost は、拡張機能のプロセスの起動、監視、期限、再起動、差分の送信を受け持つ（基本設計 2 章、5.2〜5.4）。
//
// 本体のイベントループ（internal/editor）は、[Host] を 1 つ作り、次のように使う。
//
//   - 最初の画面を描き終えたら [Host.ActivateStartup] を 1 回呼び、起動の契機 startup の拡張機能を起動する
//   - バッファを開く、変える、閉じるたびに [Host.OpenBuffer]、[Host.ChangeBuffer]、[Host.CloseBuffer] を呼ぶ。
//     稼働中の拡張機能へ全文と差分を送る（FR-16）
//   - 拡張機能の操作が呼ばれたら、[Host.Send] で問い合わせ、結果を [QueryResult] のイベントで受け取る（5.4）。
//     Send は待たずに戻るので、イベントループは結果を待つ間も描画と利用者の中断を処理できる。
//     期限が正の設定（FR-17）では期限を渡し、期限 0 の設定（FR-18）では結果が届いた時点で照合する。
//     応答を待って止まってよい呼び出し側（テストや計測のコマンド）は、[Host.Call] と [Host.ResolveMotion] などを使う
//   - [Host.Events] のチャネルから、拡張機能からの要求と通知、状態の変化を受け取り、イベントループの goroutine で処理する
//   - バッファを保存するときは、[Host.SaveHookExtensions] の拡張機能へ 1 つずつ buffer/willSave を問い合わせ（[Host.SendWillSave]）、
//     書き出した後に [Host.DidSave] を呼ぶ（基本設計 6.7 の保存の流れ）
//
// 拡張機能のプロセスとの通信と監視は、このパッケージの中の goroutine が行う。
// それらの goroutine はバッファ（internal/buffer）を触らない。
// 拡張機能を再起動したときと buffer/resync に応じるときに全文を送れるように、[Host] は開いているバッファの写し（行の配列）を持つ。
// 写しは、[Host.OpenBuffer] で受け取った全文に、[Host.ChangeBuffer] で受け取った差分を適用して保つ。
//
// [Host] のメソッドは、複数の goroutine から並行に呼んでよい。
// ただし、問い合わせの結果とバッファの版を照らし合わせるのは呼び出し側の責務であり、
// その照合はイベントループの goroutine で行う前提である（基本設計 2.1）。
package exthost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// 設定の既定値。すべて仮である。
// 期限と再起動の値、終了の期限は、基本設計 5.2 に表と一緒に書いてある。
const (
	// DefaultInitTimeout は、起動から initialize の応答までの期限の既定値（仮）である。
	DefaultInitTimeout = 5 * time.Second
	// DefaultPingInterval は、生存確認の間隔の既定値（仮）である。
	DefaultPingInterval = 5 * time.Second
	// DefaultPingTimeout は、生存確認の応答の期限の既定値（仮）である。
	DefaultPingTimeout = 2 * time.Second
	// DefaultRestartDelay は、1 回目の失敗の後の再起動の待ち時間の既定値（仮）である。
	DefaultRestartDelay = 500 * time.Millisecond
	// DefaultMaxRestartDelay は、再起動の待ち時間の上限の既定値（仮）である。
	DefaultMaxRestartDelay = 30 * time.Second
	// DefaultMaxFailures は、無効にするまでの連続の失敗の回数の既定値（仮）である。
	DefaultMaxFailures = 5
	// DefaultResetAfter は、連続の失敗の数え直しに要る、稼働の継続時間の既定値（仮）である。
	DefaultResetAfter = time.Minute
	// DefaultSendTimeout は、期限を指定しない Send の待ちの上限の既定値（仮）である。
	DefaultSendTimeout = 10 * time.Second
	// DefaultMaxOutboxBytes は、拡張機能 1 つあたりの送信の列の上限の既定値（仮）である。
	DefaultMaxOutboxBytes = 64 << 20
	// DefaultWillSaveTimeout は、保存の流れで buffer/willSave の応答を待つ期限の既定値（仮）である（基本設計 6.7）。
	// 整形が言語サーバーの処理を待つので、キー入力ごとの問い合わせの期限より長くする。
	DefaultWillSaveTimeout = time.Second
	// DefaultMaxQueuedEvents は、拡張機能 1 つあたりの、イベントループへ渡していないイベントの数の上限の既定値（仮）である。
	DefaultMaxQueuedEvents = 1024
	// DefaultShutdownTimeout は、shutdown を送ってから、拡張機能が自分で終わるのを待つ期限の既定値（仮）である。
	// 言語サーバーの shutdown と exit、デバッグアダプターの disconnect を待てる長さにする。
	DefaultShutdownTimeout = 3 * time.Second
	// DefaultTerminateTimeout は、SIGTERM を送ってから、SIGKILL を送るまでの期限の既定値（仮）である。
	DefaultTerminateTimeout = time.Second
)

// Config は、[Host] の動作の設定である。
// ゼロ値の項目には、対応する Default で始まる定数（仮の値）を使う。
type Config struct {
	// InitTimeout は、子プロセスを起動してから initialize の応答が届くまでの期限である（5.2 の異常の条件）。
	InitTimeout time.Duration
	// PingInterval は、稼働中の拡張機能へ ping を送る間隔である。
	PingInterval time.Duration
	// PingTimeout は、ping の応答の期限である。超えたら異常と判断し、強制終了する（5.2）。
	PingTimeout time.Duration
	// RestartDelay は、1 回目の失敗の後に再起動するまでの待ち時間である。
	// 失敗が続くたびに 2 倍にし、MaxRestartDelay で頭打ちにする（図 4 の「待ち時間は失敗ごとに延長」）。
	RestartDelay time.Duration
	// MaxRestartDelay は、再起動の待ち時間の上限である。
	MaxRestartDelay time.Duration
	// MaxFailures は、連続の失敗の上限である。連続の失敗がこの回数に達したら、再起動せずに無効にする。
	MaxFailures int
	// ResetAfter は、連続の失敗を数え直すのに要る、稼働の継続時間である。
	// 稼働中になってからこの時間より長く動いた後の失敗は、連続の 1 回目として数える。
	ResetAfter time.Duration
	// SendTimeout は、[SendOptions] の Timeout を指定しない [Host.Send] が、起動と応答を待つ上限である。
	// 期限 0 の設定（FR-18）でも、応答しない問い合わせの待ち受けが溜まり続けないようにする。
	SendTimeout time.Duration
	// WillSaveTimeout は、[Host.SendWillSave] と [Host.WillSave] で、期限を指定しないときに使う期限である（基本設計 6.7 の保存の流れ）。
	WillSaveTimeout time.Duration
	// MaxOutboxBytes は、拡張機能 1 つあたりの、送信の列に積んだまま書いていないバイト数の上限である。
	// 拡張機能が標準入力を読まなくなったときに、本体のメモリを使い切らないための上限である（NFR-03）。
	// 送る時点で、列に残っている量がこの上限を超えていたら、送らずに異常と判断する（[ErrOutboxFull]）。
	// 列が空なら、上限より大きいメッセージ（大きなバッファの全文）も送る。
	// このため列の量は、最大で上限と 1 件のメッセージの和に収まる。
	MaxOutboxBytes int
	// MaxQueuedEvents は、拡張機能 1 つあたりの、イベントループへ渡していないイベントの数の上限である。
	// 拡張機能が通知を送り続けたときに、本体のメモリを使い切らないための上限で、超えたら異常と判断する（NFR-03）。
	MaxQueuedEvents int
	// ShutdownTimeout は、拡張機能を正常に止めるとき（[Host.Close]、[Host.Disable]）に、
	// shutdown を送ってから、拡張機能のプロセスが自分で終わるのを待つ期限である（基本設計 5.2）。
	// 超えたらプロセスグループへ SIGTERM を送る。
	ShutdownTimeout time.Duration
	// TerminateTimeout は、SIGTERM を送ってから、プロセスが終わるのを待つ期限である。超えたら SIGKILL を送る。
	TerminateTimeout time.Duration
	// MemoryLimitMB は、拡張機能の子プロセスの RLIMIT_AS（仮想アドレス空間の大きさの上限）で、単位は MiB（2^20 バイト）である（FR-20、基本設計 7 章）。
	// 0 以下は上限なしで、子プロセスは本体の上限をそのまま継ぐ。設定の extensions.memoryLimitMb（config.Config.MemoryLimitMB）をそのまま入れる。
	//
	// 子プロセスを起動した直後に prlimit で、ソフトとハードの両方に掛ける。本体自身の上限は変えない。
	// 本体のハードの上限がこれより小さければ、本体のハードの上限を使う。
	// 上限は fork で継がれるので、拡張機能が起動する子プロセス（言語サーバー、デバッグアダプター）にも同じ値が掛かる。
	// RLIMIT_AS はプロセスごとに数えるので、拡張機能とその子プロセスの合計ではなく、それぞれが上限まで使える。
	// 起動してから prlimit を呼ぶまでの短い間（拡張機能が initialize を受け取る前）は上限が掛かっていない。
	// その間に確保した領域も、掛けた後の仮想アドレス空間の量に数える。
	// ただし、その間に拡張機能が起動した子プロセス（main の冒頭で起動したものなど）は、上限を継がない。
	// 子プロセスは initialize を受け取った後に起動する。
	//
	// RLIMIT_AS は実際に使っているメモリではなく仮想アドレスの量で数える。
	// Go の実行時は起動時に大きな領域を予約するので、Go で書いた拡張機能はヒープが小さくても、上限が小さいと起動できない。
	// 起動できるかどうかは上限について単調でなく、ある値で起動できても、それより大きい値で起動できないことがある。
	// 2026-11-16 の計測（32 CPU の Linux、カーネル 7.0.0、Go 1.27.4）では、vi-imitation-ext-line は 760 MiB と 2048 MiB で起動し、
	// 1150〜1500 MiB では起動しなかった。vi-imitation-ext-script は 2048 MiB で 2〜3 割が失敗し、2560 MiB 以上で 20/20 起動した。
	// 言語サーバーも同じ値を継ぐので、値は言語サーバーの分も含めて決める（gopls v0.24.0 は 4096 MiB で失敗し、6144 MiB で通った）。
	// 詳しい表は基本設計 7 章にある。
	// -race でビルドした実行ファイルは、検出器が大きな領域を予約するので、上限の下では起動できない。
	// 上限に当たった拡張機能は確保に失敗して異常終了し、本体は子プロセスの終了として扱う（基本設計 5.2）。
	MemoryLimitMB int64
	// Logger は、拡張機能の標準エラー出力と、状態の変化を記録する先である。nil なら slog.Default() を使う。
	Logger *slog.Logger
	// OnPanic は、Host が起こす goroutine（拡張機能の接続の読み書きと、そこから呼ぶハンドラーを含む）が panic したときに、
	// その goroutine の上で呼ぶ関数である。呼んだ後、同じ値で panic を続ける（panic を止めはしない）。
	// 本体は、端末を元に戻す関数を渡す（基本設計 8 章、#114）。Go の panic は他の goroutine の recover で捕まえられないので、
	// panic した goroutine の上で戻す。どの goroutine からも、何度呼ばれてもよい関数にする。nil なら何もしない。
	OnPanic func()
	// Env は、すべての拡張機能の子プロセスに足す環境変数の列である。各要素は "名前=値" の形にする。
	// 子プロセスの環境は、本体の環境、Env、ext.EnvExtensionDir の順に並べ、同じ名前は後のものが勝つ（os/exec の決まり）。
	// MCP のソケットのパス（ext.EnvMCPSocket。[NewMCPSocket]）を渡すのに使う（基本設計 6.2）。
	// ext.EnvSocketFDs は Host が子プロセスごとに決めるので、ここに書いても上書きする。
	Env []string
	// SocketPath は、起動の契機 socket（ext.ActivationSocket）のために、[New] の時点から待ち受ける Unix ドメインソケットのパスである（基本設計 6.2）。
	// 本体が起動ごとに決める MCP のソケットのパス（[NewMCPSocket]）を入れる。
	// 空か、socket の契機を宣言した拡張機能が無ければ待ち受けない。
	// 待ち受けたソケットのファイルは [Host.Close] で消す。
	SocketPath string
}

// recoverPanic は、Host が起こす goroutine の始めに defer で呼ぶ（他の defer より後に書き、先に動かす）。
// panic していれば、Config.OnPanic を呼んでから同じ値で panic を続ける。
func (h *Host) recoverPanic() {
	if h.cfg.OnPanic == nil {
		return
	}
	if p := recover(); p != nil {
		h.cfg.OnPanic()
		panic(p)
	}
}

// rpcOptions は、拡張機能との接続（rpc.Conn）に付けるオプションである。接続の goroutine の panic にも OnPanic を呼ぶ。
func (h *Host) rpcOptions() []rpc.Option {
	if h.cfg.OnPanic == nil {
		return nil
	}
	return []rpc.Option{rpc.WithPanicHook(h.cfg.OnPanic)}
}

func (c Config) withDefaults() Config {
	if c.InitTimeout <= 0 {
		c.InitTimeout = DefaultInitTimeout
	}
	if c.PingInterval <= 0 {
		c.PingInterval = DefaultPingInterval
	}
	if c.PingTimeout <= 0 {
		c.PingTimeout = DefaultPingTimeout
	}
	if c.RestartDelay <= 0 {
		c.RestartDelay = DefaultRestartDelay
	}
	if c.MaxRestartDelay <= 0 {
		c.MaxRestartDelay = DefaultMaxRestartDelay
	}
	if c.MaxFailures <= 0 {
		c.MaxFailures = DefaultMaxFailures
	}
	if c.ResetAfter <= 0 {
		c.ResetAfter = DefaultResetAfter
	}
	if c.SendTimeout <= 0 {
		c.SendTimeout = DefaultSendTimeout
	}
	if c.WillSaveTimeout <= 0 {
		c.WillSaveTimeout = DefaultWillSaveTimeout
	}
	if c.MaxOutboxBytes <= 0 {
		c.MaxOutboxBytes = DefaultMaxOutboxBytes
	}
	if c.MaxQueuedEvents <= 0 {
		c.MaxQueuedEvents = DefaultMaxQueuedEvents
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = DefaultShutdownTimeout
	}
	if c.TerminateTimeout <= 0 {
		c.TerminateTimeout = DefaultTerminateTimeout
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// Spec は、[Host] が管理する 1 つの拡張機能の定義である。
//
// 宣言ファイルを探して JSONC を読むのは internal/config である（#9 の裁定と PR #109。基本設計 7 章）。
// このパッケージは、読み込んで検査した後の値を受け取る。
type Spec struct {
	// Manifest は、検査済み（[ext.ValidateManifests] が誤りを返さなかった）の宣言ファイルの内容である。
	Manifest ext.Manifest
	// Dir は、宣言ファイルのあるディレクトリである。Manifest.Command が相対パスなら、ここから解決する。
	Dir string
	// Settings は、利用者の設定のうち、この拡張機能に向けた部分である。initialize でそのまま渡す。無ければ nil。
	Settings json.RawMessage
}

// command は、起動する実行ファイルのパスを返す。
func (s *Spec) command() string {
	if filepath.IsAbs(s.Manifest.Command) || s.Dir == "" {
		return s.Manifest.Command
	}
	return filepath.Join(s.Dir, s.Manifest.Command)
}

// State は、拡張機能の状態である（基本設計 5.2、図 4）。
type State int

// 拡張機能の状態。
const (
	// StateNotStarted は、宣言ファイルだけを読み、子プロセスを起動していない状態である。
	StateNotStarted State = iota
	// StateStarting は、子プロセスを起動し、initialize の応答を待っている状態である。
	StateStarting
	// StateRunning は、initialize の応答が届き、問い合わせに応答できる状態である。
	StateRunning
	// StateStopping は、[Host.Disable] で shutdown を送り、プロセスが終わるのを待っている状態である（基本設計 5.2）。
	// 問い合わせと buffer/willSave は送らずに [ErrUnavailable] で失敗させ、buffer/change などの通知も送らない。
	// プロセスが終わると StateDisabled へ移る。[Host.Reload] で止めたときは、StateNotStarted へ移る。
	StateStopping
	// StateRestartWait は、異常で子プロセスを終わらせ、再起動の待ち時間が過ぎるのを待っている状態である。
	StateRestartWait
	// StateDisabled は、連続の失敗が上限に達したか、[Host.Disable] で止めたので、自動では起動しない状態である。
	// [Host.Restart] で起動し直せる。
	// 上限に達した失敗でも、いったん StateRestartWait へ移ってから、待たずにこの状態へ移る（図 4）。
	StateDisabled
)

// String は、状態の名前を返す。
func (s State) String() string {
	switch s {
	case StateNotStarted:
		return "not-started"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateRestartWait:
		return "restart-wait"
	case StateDisabled:
		return "disabled"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// 誤りの種類。返す誤りはこれらを包むので、errors.Is で判定する。
var (
	// ErrUnknownExtension は、指定した ID の拡張機能が無いことを表す。
	ErrUnknownExtension = errors.New("exthost: unknown extension")
	// ErrUnavailable は、拡張機能が再起動待ちか無効であるか、問い合わせの途中で異常終了したことを表す。
	ErrUnavailable = errors.New("exthost: extension unavailable")
	// ErrTimeout は、期限付きの問い合わせが期限を超えたことを表す（FR-17）。
	ErrTimeout = errors.New("exthost: query timed out")
	// ErrOutboxFull は、拡張機能が標準入力を読まず、送信の列が [Config.MaxOutboxBytes] を超えたことを表す。
	// 異常の理由として [StateChange] の Err に入る。
	ErrOutboxFull = errors.New("exthost: extension is not reading its input (outbox limit exceeded)")
	// ErrClosed は、[Host.Close] の後に呼ばれたことを表す。
	ErrClosed = errors.New("exthost: host closed")
)

// Host は、拡張機能のプロセスを管理する。New で作る。
type Host struct {
	cfg Config
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	// wg は、supervise と、Send の待ち受けの goroutine を数える。
	wg     sync.WaitGroup
	events chan Event
	// pumpDone は、イベントを渡す goroutine が終わると閉じる。
	pumpDone chan struct{}
	// wake は、queue に積んだことを、イベントを渡す goroutine へ知らせる。
	wake chan struct{}
	// force は、[Host.CloseContext] の ctx が終わると閉じる。正常な終了の期限を待たずに SIGKILL へ進める。
	force chan struct{}

	// mu は、ここから下のフィールドと、各 extension と run の可変のフィールドを守る。
	mu      sync.Mutex
	exts    map[string]*extension
	order   []*extension
	docs    map[ext.BufferID]*document
	queue   []queued
	closing bool
	// startup は、起動の契機 startup（ext.ActivationStartup）が成り立った（[Host.ActivateStartup] を呼んだ）ことを表す。
	// [Host.Reload] で未起動へ戻した拡張機能を、この契機で起動し直すかを決めるのに使う。
	startup bool
	// sock は、起動の契機 socket のために待ち受けているソケットである。待ち受けていなければ nil。New の後は変えない。
	sock *socketActivator
}

// extension は、1 つの拡張機能の状態である。可変のフィールドは Host.mu が守る。
type extension struct {
	spec  Spec
	state State
	// run は、起動中か稼働中のプロセスである。それ以外の状態では nil。
	run *run
	// failures は、連続の失敗の回数である。
	failures int
	// changed は、状態が変わると閉じ、新しいチャネルに取り替える。状態の変化を待つのに使う。
	changed chan struct{}
	// stop は、[Host.Disable] で閉じる。起動するたびに新しいチャネルにする。監視の goroutine はこれで止める。
	stop chan struct{}
	// stopping は、stop を閉じたかどうかである。
	stopping bool
	// reload は、[Host.Reload] で止めていることを表す。止まったら無効ではなく未起動へ移す。
	reload bool
}

// New は、specs の拡張機能を管理する Host を作る。子プロセスは起動しない（FR-14、NFR-04）。
// 起動の契機 socket（ext.ActivationSocket）を宣言した拡張機能があれば、[Config.SocketPath] で待ち受けを始め、
// 最初の接続でその拡張機能を起動する（基本設計 6.2）。待ち受けられなくても誤りにせず、記録だけする。
// 拡張機能の ID が重複していれば誤りを返す。
// 使い終わったら Close を呼ぶ。
func New(specs []Spec, cfg Config) (*Host, error) {
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	h := &Host{
		cfg:      cfg,
		log:      cfg.Logger,
		ctx:      ctx,
		cancel:   cancel,
		events:   make(chan Event),
		pumpDone: make(chan struct{}),
		wake:     make(chan struct{}, 1),
		force:    make(chan struct{}),
		exts:     make(map[string]*extension),
		docs:     make(map[ext.BufferID]*document),
	}
	for _, s := range specs {
		id := s.Manifest.ID
		if _, dup := h.exts[id]; dup {
			cancel()
			return nil, fmt.Errorf("exthost: duplicate extension id %q", id)
		}
		e := &extension{spec: s, changed: make(chan struct{})}
		h.exts[id] = e
		h.order = append(h.order, e)
	}
	h.startSocket()
	go h.pump()
	return h, nil
}

// Events は、イベントループへ渡すイベントのチャネルを返す。
// イベントの種類と、拡張機能からのリクエストの扱いは [Event] の doc にある。
//
// チャネルはバッファを持たず、Host の中の列から 1 件ずつ渡す。
// 受け取らない間も、拡張機能との通信は止まらない（列の上限は [Config.MaxQueuedEvents]）。
// 拡張機能を異常と判断して終わらせたとき、その拡張機能からのイベントのうち、まだ渡していないものは捨てる。
// [Host.Close] の後に閉じる。
func (h *Host) Events() <-chan Event {
	return h.events
}

// State は、拡張機能の現在の状態を返す。ID が無ければ ok は false である。
func (h *Host) State(id string) (st State, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.exts[id]
	if !ok {
		return 0, false
	}
	return e.state, true
}

// Rank は、拡張機能 id の宣言ファイルを読んだ順（[New] に渡した specs の順。0 から数える）を返す。ID が無ければ -1 を返す。
// 複数の拡張機能が同じものを申し出たときの勝ち負けを、[Host.Provider] と同じ順で決めるのに使う（option/set。#349）。
func (h *Host) Rank(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, e := range h.order {
		if e.spec.Manifest.ID == id {
			return i
		}
	}
	return -1
}

// Provider は、本体の操作 op を、ファイルの種類 fileType のバッファで引き受ける拡張機能（提供者）の ID を返す（基本設計 5.5。FR-94）。
// 選び方は [ext.Manifest] の Provides の doc のとおりで、op を provides で名乗り、fileType の候補になる
// （[ext.Manifest.HandlesFileType]）拡張機能のうち、未起動か起動中か稼働中のものを、specs の順に探して最初のものを返す。
// 停止中、再起動待ち、無効の拡張機能は飛ばす（その操作には使えないので、次の候補を使う）。
// 候補が無ければ ok は false である。拡張機能は起動しない（問い合わせの [Host.Send] が起動する）。
//
// req: FR-94
func (h *Host) Provider(op ext.Operation, fileType string) (id string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.order {
		m := &e.spec.Manifest
		if !m.ProvidesOp(op) || !m.HandlesFileType(fileType) {
			continue
		}
		switch e.state {
		case StateNotStarted, StateStarting, StateRunning:
			return m.ID, true
		case StateStopping, StateRestartWait, StateDisabled:
		}
	}
	return "", false
}

// Activate は、起動の契機 kind が value について成り立ったことを知らせ、その契機を宣言した未起動の拡張機能を起動する。
// value は、操作の契機なら操作の ID、ファイルの種類の契機ならファイルの種類の名前である。
// 未知の種類の契機は、宣言にあっても無視する（[ext.Manifest.KnownActivation]）。
//
// 起動は非同期で、子プロセスの起動と初期化を待たずに戻る。
// 起動を始めた拡張機能の ID を返す。
//
// req: FR-14, NFR-04
func (h *Host) Activate(kind ext.ActivationKind, value string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.activateLocked(kind, value)
}

func (h *Host) activateLocked(kind ext.ActivationKind, value string) []string {
	if h.closing || value == "" {
		return nil
	}
	var started []string
	for _, e := range h.order {
		if e.state != StateNotStarted {
			continue
		}
		for _, a := range e.spec.Manifest.KnownActivation() {
			if a.On != kind {
				continue
			}
			v := a.ID
			if kind == ext.ActivationFileType {
				v = a.Pattern
			}
			if v == value {
				h.startLocked(e)
				started = append(started, e.spec.Manifest.ID)
				break
			}
		}
	}
	return started
}

// ActivateStartup は、起動の契機 startup（ext.ActivationStartup）が成り立ったことを知らせ、
// その契機を宣言した未起動の拡張機能を、宣言ファイルを読んだ順にすべて起動する（基本設計 5.2。FR-225）。
// イベントループ（internal/editor）が、最初の画面を描き終えた後に 1 回呼ぶ。
// 最初の画面を描く前に子プロセスを起動しないので、本体の起動の速さ（NFR-05）を損なわない。
//
// 2 回目からの呼び出しは、未起動の startup の拡張機能が残っていればそれだけを起動し、動いているものには何もしない。
// 呼んだ後に [Host.Reload] で未起動へ戻った startup の拡張機能は、すぐに起動し直す（filetype の契機と同じ。Reload の doc）。
// 再起動待ちと無効の拡張機能は起動しない（異常の後の再起動は、他の契機の拡張機能と同じく待ち時間の後に本体が行う）。
//
// 起動は非同期で、子プロセスの起動と初期化を待たずに戻る。起動を始めた拡張機能の ID を返す。
//
// req: FR-225
func (h *Host) ActivateStartup() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return nil
	}
	h.startup = true
	var started []string
	for _, e := range h.order {
		if e.state == StateNotStarted && hasStartup(&e.spec.Manifest) {
			h.startLocked(e)
			started = append(started, e.spec.Manifest.ID)
		}
	}
	return started
}

// hasStartup は、宣言ファイル m が起動の契機 startup を持つかを返す。
func hasStartup(m *ext.Manifest) bool {
	for _, a := range m.KnownActivation() {
		if a.On == ext.ActivationStartup {
			return true
		}
	}
	return false
}

// Start は、未起動の拡張機能を、契機に依らずに起動する。起動は非同期である。
// 未起動でなければ何もしない。
func (h *Host) Start(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, err := h.lookupLocked(id)
	if err != nil {
		return err
	}
	if e.state == StateNotStarted {
		h.startLocked(e)
	}
	return nil
}

// Restart は、無効になった拡張機能の連続の失敗の数を 0 に戻し、起動し直す（図 4 の「利用者が再起動を指示」）。
// 未起動の拡張機能なら起動する。起動の契機 socket の拡張機能が異常の後の再起動待ちなら、接続を待たずに起動する
// （再起動の待ち時間の中なら、待ち時間が過ぎた時点で起動する。基本設計 6.2）。それ以外の状態では何もしない。起動は非同期である。
// 停止中（[StateStopping]）でも何もしないので、無効へ移った後に呼び直す。
func (h *Host) Restart(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, err := h.lookupLocked(id)
	if err != nil {
		return err
	}
	switch e.state {
	case StateDisabled, StateNotStarted:
		e.failures = 0
		h.startLocked(e)
	case StateRestartWait:
		// 起動の契機 socket の拡張機能は、異常の後に接続を待つので、利用者の指示で接続を待たずに起動する（基本設計 6.2）。
		// 再起動の待ち時間の中なら、待ち時間が過ぎた時点で起動する。それ以外の拡張機能は、待ち時間の後に自動で起動する。
		h.kickLocked(e, false)
	case StateStarting, StateRunning, StateStopping:
	}
	return nil
}

// Disable は、拡張機能を正常に止め、無効にする（利用者の指示による無効化）。止めるのは非同期で、待たずに戻る。
//
// 起動中か稼働中なら、すぐに [StateStopping] へ移し、Close と同じ手順（shutdown、期限後に SIGTERM、さらに期限後に SIGKILL）で
// プロセスを終わらせ、終わった後に [StateDisabled] へ移る。
// 再起動待ちなら、待つのをやめて無効にする。未起動なら、そのまま無効にする。停止中か無効なら何もしない。
// 止めたことは連続の失敗に数えない。[Host.Restart] で起動し直せる。
//
// shutdown を送った後に拡張機能から届いたリクエスト（buffer/edit など）は、イベントループへ渡さずに失敗を応答する。
// 通知は渡すが、無効へ移った時点でイベントループへ渡していないイベントは、リクエストも通知も捨てる。
func (h *Host) Disable(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, err := h.lookupLocked(id)
	if err != nil {
		return err
	}
	// 先の Reload で止めている途中なら、止まった後も未起動へ戻さずに無効にする。
	e.reload = false
	switch e.state {
	case StateNotStarted:
		h.setStateLocked(e, StateDisabled, nil)
	case StateStarting, StateRunning:
		h.requestStopLocked(e)
		h.setStateLocked(e, StateStopping, nil)
	case StateRestartWait:
		h.requestStopLocked(e)
	case StateStopping, StateDisabled:
	}
	return nil
}

// Reload は、拡張機能の実行ファイルを入れ替えた後（:ext sync。基本設計 5.6）に、次の起動の契機で新しい実行ファイルを起動するようにする。
//
// 起動中か稼働中なら、[Host.Disable] と同じ手順（shutdown、期限後に SIGTERM、さらに期限後に SIGKILL）で正常に止め、
// プロセスが終わったら、無効ではなく未起動（[StateNotStarted]）へ移す。停止中なら、止まった後に未起動へ移す。
// 再起動待ちなら待つのをやめ、無効なら連続の失敗の数を 0 に戻して、未起動へ移す。未起動なら何もしない。止めるのは非同期で、待たずに戻る。
// 止めたことは連続の失敗に数えない。
//
// 未起動へ移した時点で、開いているバッファのファイルの種類で起動の契機（filetype）が成り立てば、すぐに起動し直す。
// ファイルの種類の契機はバッファを開いたときにしか成り立たないので、そうしないと、開いたままのバッファの
// LSP や構文の色の拡張機能が、別のファイルを開くまで起動しない（PR #220 のレビュー）。
// 起動の契機 startup の拡張機能も、[Host.ActivateStartup] の後なら、同じ理由ですぐに起動し直す（その契機は 1 回しか成り立たない）。
// それ以外の契機（操作、コマンド、socket）の拡張機能は、次にその契機が成り立ったときに起動する。
//
// 宣言ファイルは読み直さない。New に渡した Spec の起動の契機と command のパスのまま、そのパスの実行ファイルを起動する。
func (h *Host) Reload(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, err := h.lookupLocked(id)
	if err != nil {
		return err
	}
	switch e.state {
	case StateNotStarted:
	case StateStarting, StateRunning:
		e.reload = true
		h.requestStopLocked(e)
		h.setStateLocked(e, StateStopping, nil)
	case StateStopping:
		e.reload = true
	case StateRestartWait:
		e.reload = true
		h.requestStopLocked(e)
	case StateDisabled:
		e.failures = 0
		h.setStateLocked(e, StateNotStarted, nil)
		h.activateByDocsLocked(e)
	}
	return nil
}

// stoppedLocked は、利用者の指示（[Host.Disable] か [Host.Reload]）で止めた拡張機能 e を、無効か未起動へ移す。
// 未起動へ移したら、開いているバッファのファイルの種類で起動し直す（activateByDocsLocked）。
func (h *Host) stoppedLocked(e *extension) {
	if e.reload {
		e.reload = false
		e.failures = 0
		h.setStateLocked(e, StateNotStarted, nil)
		h.activateByDocsLocked(e)
		return
	}
	h.setStateLocked(e, StateDisabled, nil)
}

// activateByDocsLocked は、未起動の拡張機能 e のファイルの種類の契機が、開いているバッファのどれかで成り立てば起動する。
// 起動の契機 startup が既に成り立っていて（[Host.ActivateStartup]）、e がその契機を持てば、バッファに依らずに起動する。
func (h *Host) activateByDocsLocked(e *extension) {
	if h.closing || e.state != StateNotStarted {
		return
	}
	if h.startup && hasStartup(&e.spec.Manifest) {
		h.startLocked(e)
		return
	}
	for _, a := range e.spec.Manifest.KnownActivation() {
		if a.On != ext.ActivationFileType || a.Pattern == "" {
			continue
		}
		for _, d := range h.docs {
			if d.fileType == a.Pattern {
				h.startLocked(e)
				return
			}
		}
	}
}

// requestStopLocked は、拡張機能の監視の goroutine へ、止めるよう知らせる。
func (h *Host) requestStopLocked(e *extension) {
	if !e.stopping {
		e.stopping = true
		close(e.stop)
	}
}

// Close は、すべての拡張機能のプロセスを正常に止め、管理の goroutine が終わるのを待つ（基本設計 5.2）。
// その後に [Host.Events] のチャネルを閉じる。
//
// 起動中か稼働中の拡張機能へは shutdown を送り、[Config.ShutdownTimeout] までにプロセスが終わらなければ
// プロセスグループへ SIGTERM を、さらに [Config.TerminateTimeout] を過ぎたら SIGKILL を送る。
// 拡張機能ごとに並行して行うので、Close が待つのは最も長くかかった拡張機能の分である。
// プロセスが終わった後も、同じプロセスグループに残ったプロセスには SIGKILL を送る。
// shutdown を送った後に拡張機能から届いたイベントは、[Host.Events] へ渡さない。
// Close の後の問い合わせは [ErrClosed] を包んだ誤りを返す。2 回目以降の呼び出しは何もしない。
//
// Close は、拡張機能が応答しないと最大で ShutdownTimeout と TerminateTimeout の和だけ戻らない。
// 待つ上限を呼び出し側で決めるときは [Host.CloseContext] を使う。
// イベントループ（internal/editor）は、端末の後始末（画面の復元）を先に済ませてから呼ぶ。
// 利用者に、エディタが終わらずに止まって見えないようにするためである。
func (h *Host) Close() error {
	return h.CloseContext(context.Background())
}

// CloseContext は、Close と同じく拡張機能を正常に止めるが、ctx が終わったら期限を待たずに、
// 残っている拡張機能のプロセスグループへ SIGKILL を送って終わらせる。
// ctx が終わったために SIGKILL へ切り替えたときは、ctx.Err() を返す。それ以外は nil を返す。
// 2 回目以降の呼び出しは、1 回目の Close が終わるのを待つだけで、nil を返す。
func (h *Host) CloseContext(ctx context.Context) error {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		<-h.pumpDone
		return nil
	}
	h.closing = true
	h.mu.Unlock()
	h.cancel()
	done := make(chan struct{})
	go func() {
		defer h.recoverPanic()
		h.wg.Wait()
		close(done)
	}()
	var err error
	select {
	case <-done:
	case <-ctx.Done():
		err = ctx.Err()
		close(h.force)
		<-done
	}
	h.closeSocket()
	<-h.pumpDone
	return err
}

func (h *Host) lookupLocked(id string) (*extension, error) {
	if h.closing {
		return nil, ErrClosed
	}
	e, ok := h.exts[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownExtension, id)
	}
	return e, nil
}

// setStateLocked は、拡張機能の状態を変え、状態の変化を待つ者を起こし、StateChange のイベントを積む。
func (h *Host) setStateLocked(e *extension, st State, cause error) {
	e.state = st
	close(e.changed)
	e.changed = make(chan struct{})
	id := e.spec.Manifest.ID
	if cause != nil {
		h.log.Warn("extension state changed", "extension", id, "state", st.String(), "error", cause)
	} else {
		h.log.Info("extension state changed", "extension", id, "state", st.String())
	}
	// 拡張機能から届いたイベントではない（r が nil）ので、上限で拒まれることは無い。
	_ = h.pushLocked(&StateChange{Extension: id, State: st, Err: cause}, nil)
}

// startLocked は、拡張機能の状態を起動中にし、プロセスを管理する goroutine を起動する。
func (h *Host) startLocked(e *extension) {
	r := newRun()
	e.run = r
	e.stop = make(chan struct{})
	e.stopping = false
	e.reload = false
	h.setStateLocked(e, StateStarting, nil)
	h.wg.Add(1)
	go h.supervise(e, r, e.stop)
}
