// Package server は、編集の状態を持つ側（server）である（基本設計 1.2、2 章。FR-58）。
//
// server は internal/editor のイベントループを持ち、client との接続（protocol.ServerConn）から届いたキーをイベントループへ渡し、
// イベントループが組み立てた画面とクリップボードへの書き込みを client へ送る。
// 端末は持たない。端末の raw モード、キーの解読、画面の出力は client（internal/client）が持つ。
//
// # 編集の client
//
// 複数の編集の client を同時に繋ぐ（FR-58、FR-62）。最初の client は、1 つのプロセスの中の接続（protocol.Pipe。FR-60 の構成）でも、
// server のソケット（protocol.NewServerSocket。Listen で待ち受ける）でもよい（Serve）。
// 2 つ目からの client は server のソケットで受け付け、editor.Editor.Attach でイベントループへ渡す。
// 1 つのプロセスの構成でも server のソケットを待ち受け（基本設計 1.2）、ソケットで繋いだ編集の client を受け付ける。
// 待つだけの client（FR-83）は、server のソケットで受け付け、editor.Editor.Wait でファイルを開かせて、タブが閉じられるまで繋いでおく（serveWait）。
// 部品の client（FR-67、FR-115）は、server のソケットで受け付け、editor.Editor.AttachComponent で、その部品を開いた元の編集の client に繋ぐ
// （component.go）。server は、端末多重化ソフトの層で部品の client を起動する関数を editor.Config.OpenPopup と OpenPane に入れる。
// :terminal.popup（FR-168）のコマンドを端末多重化ソフトのポップアップで動かす関数は、editor.Config.RunPopup に入れる（component.go の runPopup）。
//
// # 端末の受け渡し
//
// :! と権限を上げた保存（FR-79、FR-80、FR-117）では、イベントループが clientTerm の editor.TerminalHandover を呼び、
// client へ protocol.TerminalRun などを送る。client の返事（protocol.TerminalDone、TerminalReleased）は、キーと同じ列で
// editor.TermEvent の Terminal としてイベントループへ渡す。
//
// client ごとに識別子（editor.ClientID）を割り当て、接続 1 つを editor.Terminal の口（clientTerm）にする。
// イベントループは client ごとの画面の大きさで描き、client ごとの clientTerm へ渡す（internal/editor の clients.go）。
// client が :q で最後のタブを閉じたら、イベントループが clientTerm.Exit を呼び、その接続へ終わりの知らせを送って閉じる。
// 最後の client が終わったら、イベントループ（editor.Editor.Run）が戻り、Serve が戻る（FR-61）。
//
// # 並行処理
//
// 編集の状態は、Run と Serve を呼んだ goroutine（イベントループ）だけが触る（基本設計 2.1）。
// client の接続の読み取りは別の goroutine で行い、イベントをチャネルでイベントループへ送る。
// client への送信も別の goroutine で行う。イベントループは画面とクリップボードへの書き込みを client ごとの置き場に入れるだけで、送り終えるのを待たない。
// 画面は最新の 1 つだけを置き、送る前に次の画面が来たら古い方を捨てる。クリップボードへの書き込みは捨てずに順に送る。
// こうすると、読み取りの遅い client があっても、イベントループは止まらない（PR #187 のレビューの推奨 2）。
//
// # 層
//
// internal/editor と internal/protocol を import するので、その上の層に置く。
// internal/client と同じ層で、互いに import しない。両者をつなぐのは cmd/vi-imitation である。
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/editor"
	"github.com/piplupflipper/vi-imitation/internal/mux"
	"github.com/piplupflipper/vi-imitation/internal/protocol"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// flushTimeout は、Run が終わるときに、client へまだ送っていないクリップボードへの書き込みを送り終えるのを待つ上限である。
// 超えたら接続を閉じる（読まない client で server が終われなくならないため）。
const flushTimeout = time.Second

// helloTimeout は、server のソケットで受け付けた接続が Hello を送るのを待つ上限である（仮）。
// Hello に続く標準入力の中身（FR-158）を受けている間は、何も読めない時間がこれを超えるまで待つ（helloContext）。
// 超えたら接続を閉じ、次の接続を待つ（Hello を送らない接続で、編集の client を受け付けられなくならないため）。
const helloTimeout = 5 * time.Second

// ErrUnexpectedHello は、client の最初のメッセージが、編集の client の Hello でなかったことを表す。
var ErrUnexpectedHello = errors.New("server: first message is not a hello from an edit client")

// ErrNoClient は、Serve が FirstClientTimeout の間に編集の client を 1 つも繋げなかったことを表す。
var ErrNoClient = errors.New("server: no edit client connected in time")

// Server は、編集の状態を持つ側である。New で作り、Open で起動の引数のファイルを開き、Run か Serve で client を繋いで動かし、Close で拡張機能を止める。
type Server struct {
	// FirstClientTimeout は、ソケットだけで待つ Serve（first が nil）が、最初の編集の client の Hello を待つ上限である。
	// 超えたら ErrNoClient で戻る。0 なら待ち続ける。
	// 時間は、受け付けた接続のどれからも何も読めない時間で数える（Hello に続く標準入力の中身を受けている間は延ばす。FR-158）。
	// client が自動で起動した server（-server）が、起動の途中で client が終わったとき（kill -9、ペインを閉じた）に、
	// 誰も繋がないまま残らないようにする（FR-61。PR #207 のレビューの必須 2）。Serve の前に設定する。
	FirstClientTimeout time.Duration

	ed *editor.Editor
	// client は、最初に繋ぐ編集の client である（editor.New に渡した端末）。Run か Serve で繋ぐ。
	client  *clientTerm
	onPanic func()
	nextID  atomic.Uint64
	log     *slog.Logger

	// mu は terms を守る。
	mu sync.Mutex
	// terms は、繋いだ編集の client のすべてである。Serve の終わりに、まだ終わっていない client へ終わりの知らせを送る。
	terms []*clientTerm
	// sock は、Serve が待ち受けるソケットのパス（string）である。:diffsplit で開くペインの client に渡す（diffsplit.go）。
	sock atomic.Value
	// muxCtx は、端末多重化ソフトの層の操作（ペインの分割）に渡す ctx の元で、Close で取り消す（muxStop）。
	// herdr app の mod の結果の待ち（最大 mux.HerdrAppWait）を、server の終わりで打ち切るためである（PR #441 のレビューの必須 1）。
	muxCtx  context.Context
	muxStop context.CancelFunc
	// herdrPopups は、herdr app の mod がポップアップを知っているかの覚えた答えである（#452）。
	// herdr app の中で withMux がポップアップの関数を入れたときだけ作る。ほかは nil である。
	herdrPopups *herdrPopups
}

// muxContext は、端末多重化ソフトの層の操作に渡す ctx の元を返す。New で作っていなければ（テスト）、取り消さない ctx を返す。
func (s *Server) muxContext() context.Context {
	if s.muxCtx == nil {
		return context.Background()
	}
	return s.muxCtx
}

// New は、cfg でイベントループ（editor.Editor）を作り、Server を返す。
// cfg.OnPanic は、server が起こす goroutine（client の接続の読み取りと送信、ソケットの受け付け）の panic でも呼ぶ。
func New(cfg editor.Config) (*Server, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{onPanic: cfg.OnPanic, log: logger}
	s.muxCtx, s.muxStop = context.WithCancel(context.Background())
	s.client = s.newTerm()
	ed, err := editor.New(s.client, s.withMux(cfg))
	if err != nil {
		s.muxStop()
		return nil, fmt.Errorf("server: %w", err)
	}
	s.ed = ed
	return s, nil
}

// withMux は、cfg の端末多重化ソフトの層を使う関数のうち、空のものに server の関数を入れて返す。
//
// herdr app（#440）の中の中央のポップアップは、mod に頼んで herdr app の窓に出す（#452）。mod が無いか古いときに開くたびに誤りを出さず、
// 部品のポップアップを client の画面の中に描き（FR-67）、:terminal.popup を :! と同じく動かすように、ポップアップを今開けるかを
// editor.Config.PopupReady で知らせる（herdrPopups）。OpenPopup と RunPopup と PopupReady は組で入れ、どれかを呼び出し側が入れていれば
// どれも入れない（テストの偽物の OpenPopup に、mod への問いを混ぜない）。PopupScreenWidth は入れない（herdr app の窓の大きさは画面の
// 論理ピクセルで決まり、端末の列数を前もって知る口が無い。editor は元の client の画面の幅で代える）。
//
// req: FR-316, FR-326
func (s *Server) withMux(cfg editor.Config) editor.Config {
	if cfg.SplitPane == nil {
		cfg.SplitPane = s.splitPane
	}
	if cfg.OpenPane == nil {
		cfg.OpenPane = s.openPane
	}
	if mux.Detect(os.Getenv).Kind() == mux.KindHerdrApp {
		if cfg.OpenPopup == nil && cfg.RunPopup == nil && cfg.PopupReady == nil {
			s.herdrPopups = newHerdrPopups(s.log, s.muxContext, herdrPopupProbe)
			cfg.OpenPopup = s.openPopup
			cfg.RunPopup = s.runPopup
			cfg.PopupReady = s.herdrPopups.Ready
			// 窓の中の部品の client が繋ぐまで、client の画面の中に描かない（画面の中に描いてから窓へ移るちらつきを出さない。#452）。
			cfg.PopupAwaitRemote = true
		}
		return cfg
	}
	if cfg.OpenPopup == nil {
		cfg.OpenPopup = s.openPopup
	}
	if cfg.PopupScreenWidth == nil {
		cfg.PopupScreenWidth = s.popupScreenWidth
	}
	if cfg.RunPopup == nil {
		cfg.RunPopup = s.runPopup
	}
	return cfg
}

// herdrPopupProbe は、herdrPopups の問いである。server を起動した環境の herdr app の層で、mod がポップアップを知っているかを問う。
func herdrPopupProbe(ctx context.Context, target string) (bool, error) {
	a, ok := mux.Detect(os.Getenv).(*mux.HerdrApp)
	if !ok {
		return false, mux.ErrUnsupported
	}
	return a.PopupSupported(ctx, target)
}

// newTerm は、1 つの client との接続を editor.Terminal の口にするものを作る。
func (s *Server) newTerm() *clientTerm {
	t := &clientTerm{onPanic: s.onPanic, log: s.log, events: make(chan editor.TermEvent)}
	t.onEnd = func() { s.untrack(t) }
	return t
}

// clientInfo は、Hello から、イベントループへ渡す client ごとの情報を作る。
func clientInfo(id editor.ClientID, h protocol.Hello) editor.ClientInfo {
	return editor.ClientInfo{ID: id, Pane: h.Pane, WorkDir: h.WorkDir, Env: h.Env, Files: h.Files, ReadOnly: h.ReadOnly, Stdin: h.Stdin}
}

// OpenStdin は、起動の引数の - で標準入力から読んだ text を、名前の無いバッファとして開き、カーソルのあるバッファにする
// （editor.Editor.OpenStdin。FR-158）。readOnly なら編集を禁止した状態で開く（FR-119）。Run の前に呼ぶ。
func (s *Server) OpenStdin(text string, readOnly bool) {
	s.ed.OpenStdin(text, readOnly)
}

// Open は、path のファイルを開き、カーソルのあるバッファにする（editor.Editor.Open）。Run の前に呼ぶ。
func (s *Server) Open(path string) error {
	return s.ed.Open(path)
}

// OpenArgs は、起動の引数のファイル files を開き、最初のファイルをカーソルのあるバッファにする。readOnly なら編集を禁止した状態で開く（FR-119）。
// Vim と同じく、引数の順にバッファの番号（:ls、:b N）を付ける。Open は開いたバッファへ移るので、最後に最初のファイルをもう一度開いて移る
// （開いているファイルは開き直さない）。Run の前に呼ぶ。
func (s *Server) OpenArgs(files []string, readOnly bool) error {
	return s.openArgs(files, readOnly)
}

func (s *Server) openArgs(files []string, readOnly bool) error {
	order := files
	if len(files) > 1 {
		order = append(slices.Clone(files), files[0])
	}
	for _, f := range order {
		if err := s.ed.OpenWith(f, readOnly); err != nil {
			return fmt.Errorf("%s を開けない: %w", f, err)
		}
	}
	return nil
}

// Run は、conn を編集の client として繋ぎ、イベントループを動かす。Serve(ctx, nil, conn) と同じである。
func (s *Server) Run(ctx context.Context, conn protocol.ServerConn) error {
	return s.Serve(ctx, nil, conn)
}

// Serve は、編集の client を繋ぎ、イベントループを動かす。Run と Serve は、1 つの Server で合わせて 1 回だけ呼べる。
//
// first が nil でなければ、それを最初の編集の client にする（1 つのプロセスの構成）。
// first が nil なら、ln で受け付けた接続のうち、最初に編集の client の Hello を送ったものを最初の編集の client にする（server だけのプロセス）。
// FirstClientTimeout の間に来なければ ErrNoClient で戻る。
// 最初の編集の client を繋いだら、その Hello の Stdin（FR-158）と Files を Hello.ReadOnly に従って開いてから（OpenStdin、OpenArgs と同じ順）、イベントループを動かす。
// 開けなければ、その理由の終わりの知らせを送り、誤りで戻る。
// ln が nil でなければ、Serve の間 ln で待ち受け、後から編集の client の Hello を送った接続を、イベントループへ足す（editor.Editor.Attach。FR-62）。
// その client の Files を開けなければ、その理由の終わりの知らせを送って閉じる。編集の client でない接続は、理由付きの終わりの知らせを送って閉じる。
// Serve は戻る前に ln を閉じる。
//
// 戻り値は editor.Editor.Run と同じで、最後の client が :q などで終われば nil、最後の client が接続を閉じたら editor.ErrTerminalLost、
// ctx の終わりで ctx.Err() である。
// client が最初のメッセージを送る前に ctx が終わっても、ctx.Err() で戻る。
// first の最初のメッセージが編集の client の Hello でなければ、ErrUnexpectedHello で戻る。
//
// 戻る前に、まだ繋いでいる編集の client へ終わりの知らせを送り、接続を閉じる。client はそれを見て端末を戻す。
// 拡張機能はまだ止めないので、呼び出し側は client が端末を戻すのを待ってから Close を呼ぶ（editor.Editor.Close の doc の順序）。
func (s *Server) Serve(ctx context.Context, ln net.Listener, first protocol.ServerConn) error {
	var a *acceptor
	if ln != nil {
		s.sock.Store(ln.Addr().String())
		a = s.startAccept(ln)
		defer a.stop()
	}
	conn, hello, err := s.firstClient(ctx, a, first)
	if err != nil {
		return err
	}
	if s.herdrPopups != nil {
		herdrTrace("client", "pane", hello.Pane, "files", hello.Files, "first", true)
		if hello.Pane != "" {
			// 1 つのプロセスの構成の最初の client は admit を通らないので、ここでも問い始める（問いの途中なら重ねない）。
			s.herdrPopups.prime(hello.Pane)
		}
	}
	id := editor.ClientID(s.nextID.Add(1))
	s.client.attach(id, conn, hello)
	s.track(s.client)
	s.ed.SetClientInfo(clientInfo(id, hello))
	if hello.Stdin != nil {
		// req: FR-158
		s.ed.OpenStdin(*hello.Stdin, hello.ReadOnly)
		hello.Stdin = nil // 中身はバッファが持つ。イベントループを動かす間は覚えておかない
	}
	if err = s.openArgs(hello.Files, hello.ReadOnly); err != nil {
		s.client.end(err.Error())
		return err
	}
	if a != nil {
		go s.attachLoop(a)
	}
	err = s.ed.Run(ctx)
	reason := exitReason(ctx, err)
	s.mu.Lock()
	terms := slices.Clone(s.terms)
	s.mu.Unlock()
	for _, t := range terms {
		t.end(reason)
	}
	return err
}

// track は、繋いだ client の t を覚える（Serve の終わりに終わりの知らせを送るため）。
// 終わった（end の後の）client は、untrack で一覧から外す（PR #223 のレビューの推奨 4）。
func (s *Server) track(t *clientTerm) {
	s.mu.Lock()
	s.terms = append(s.terms, t)
	s.mu.Unlock()
}

// untrack は、終わった client の t を一覧から外す。
func (s *Server) untrack(t *clientTerm) {
	s.mu.Lock()
	s.terms = slices.DeleteFunc(s.terms, func(o *clientTerm) bool { return o == t })
	s.mu.Unlock()
}

// attachLoop は、最初の client の後に Hello を送った編集の client を、イベントループへ足す。a が止まるまで続ける。
func (s *Server) attachLoop(a *acceptor) {
	defer s.client.recoverPanic()
	for {
		var j joined
		select {
		case j = <-a.joined:
		case <-a.done:
			return
		}
		id := editor.ClientID(s.nextID.Add(1))
		t := s.newTerm()
		t.attach(id, j.conn, j.hello)
		// Attach の直後に client が終わっても（Exit）、一覧から外せるように、先に足す。
		s.track(t)
		if err := s.ed.Attach(t, clientInfo(id, j.hello)); err != nil {
			reason := err.Error()
			switch {
			case errors.Is(err, editor.ErrStopped):
				reason = "server が終わった"
			case errors.Is(err, editor.ErrSplitCanceled):
				// 見比べの待ちを捨てた後に繋いだ、分割したペインの client。理由を出さずに終わらせ、ペインを閉じさせる（#446）。
				reason = ""
			}
			s.log.Info("編集の client を足せない", "client", id, "err", err)
			t.end(reason)
			continue
		}
		s.log.Info("編集の client を足した", "client", id, "pane", j.hello.Pane)
	}
}

// exitReason は、イベントループが err で終わったときに、client へ知らせる終わりの理由を返す。:q などの正常な終わりは空である。
func exitReason(ctx context.Context, err error) string {
	switch {
	case err == nil, errors.Is(err, editor.ErrTerminalLost):
		// client の側が終わった。知らせる相手はもう読んでいない
		return ""
	case ctx.Err() != nil:
		return "server が止められた: " + context.Cause(ctx).Error()
	}
	return "server が誤りで終わった: " + err.Error()
}

// firstClient は、編集の client の接続と、その Hello を返す。
// first が nil でなければ first の Hello を待つ。nil なら a が受け付けた接続から、編集の client の Hello を送ったものを選ぶ。
func (s *Server) firstClient(ctx context.Context, a *acceptor, first protocol.ServerConn) (protocol.ServerConn, protocol.Hello, error) {
	if first != nil {
		h, err := handshake(ctx, first)
		if errors.Is(err, errPeerClosed) {
			// 大きさを知らせる前に client が閉じた。イベントループに ErrTerminalLost で戻らせる
			_ = first.Close()
			return nil, protocol.Hello{}, editor.ErrTerminalLost
		}
		if err != nil {
			_ = first.Close()
			return nil, protocol.Hello{}, err
		}
		return first, h, nil
	}
	if a == nil {
		return nil, protocol.Hello{}, errors.New("server: no client and no listener")
	}
	var expired <-chan struct{}
	if s.FirstClientTimeout > 0 {
		// 何も読めない時間で数える。最初の client が Hello に続けて標準入力の中身（FR-158）を送っている間は延ばす
		// （中身を受け終えるまで a.joined に来ないので、Serve の始めからの時間で数えると、大きな中身の間に終わってしまう）。
		stop := make(chan struct{})
		defer close(stop)
		start := time.Now()
		expired = idleExpired(s.FirstClientTimeout, func() time.Duration { return a.read.idleSince(start) }, stop)
	}
	select {
	case <-ctx.Done():
		return nil, protocol.Hello{}, ctx.Err()
	case <-expired:
		return nil, protocol.Hello{}, ErrNoClient
	case j := <-a.joined:
		return j.conn, j.hello, nil
	}
}

// errPeerClosed は、handshake の間に client が接続を閉じたことを表す。
var errPeerClosed = errors.New("server: client closed before hello")

// handshake は、conn の最初のメッセージを待ち、編集の client の Hello なら返す。
// 待つ間に ctx が終わったら ctx.Err() を返す。そのとき最初のメッセージを待つ goroutine は、呼び出し側が conn を閉じると戻る。
func handshake(ctx context.Context, conn protocol.ServerConn) (protocol.Hello, error) {
	return handshakeKinds(ctx, conn, protocol.KindEdit)
}

// handshakeKinds は、handshake と同じく conn の最初のメッセージを待ち、kinds のどれかの種類の client の Hello なら返す。
func handshakeKinds(ctx context.Context, conn protocol.ServerConn, kinds ...protocol.ClientKind) (protocol.Hello, error) {
	type received struct {
		m   protocol.ClientMessage
		err error
	}
	ch := make(chan received, 1)
	go func() {
		m, err := conn.Recv()
		ch <- received{m, err}
	}()
	var r received
	select {
	case r = <-ch:
	case <-ctx.Done():
		return protocol.Hello{}, ctx.Err()
	}
	if r.err != nil {
		return protocol.Hello{}, errPeerClosed
	}
	h, ok := r.m.(protocol.Hello)
	if !ok {
		return protocol.Hello{}, fmt.Errorf("%w: %#v", ErrUnexpectedHello, r.m)
	}
	if !slices.Contains(kinds, h.Kind) {
		return protocol.Hello{}, fmt.Errorf("%w: kind %q: %w", ErrUnexpectedHello, h.Kind, errUnsupportedKind)
	}
	return h, nil
}

// errUnsupportedKind は、Hello の client の種類を、まだ受け付けないことを表す（ErrUnexpectedHello を包む）。
var errUnsupportedKind = errors.New("server: unsupported client kind")

// acceptor は、server のソケットで接続を受け付ける goroutine である。
type acceptor struct {
	ln net.Listener
	// joined は、編集の client の Hello を送った接続を、届いた順に渡す。最初のものは firstClient が、後のものは attachLoop が受け取る。
	joined chan joined
	done   chan struct{}
	// read は、受け付けたどれかの接続で最後に読めた時刻である（firstClient の期限）。
	read progress
}

// joined は、編集の client の Hello を送った接続である。
type joined struct {
	conn  protocol.ServerConn
	hello protocol.Hello
}

func (s *Server) startAccept(ln net.Listener) *acceptor {
	a := &acceptor{ln: ln, joined: make(chan joined), done: make(chan struct{})}
	go func() {
		defer s.client.recoverPanic()
		for {
			c, err := ln.Accept()
			if err != nil {
				return // ln を閉じた
			}
			pc := newProgressConn(c, &a.read)
			go s.admit(a, protocol.NewServerSocket(pc), pc)
		}
	}()
	return a
}

// admit は、受け付けた接続 conn の Hello を待ち、編集の client なら a.joined へ渡す。
// 何も読めないまま helloTimeout が経ったら待つのをやめる（helloContext。Hello に続く標準入力の中身を受けている間は延ばす）。
// Hello を送らない接続は閉じ、編集の client でない種類の Hello を送った接続は、理由付きの終わりの知らせを送って閉じる。
// 1 つの接続の Hello を待つ間も、他の接続を受け付けられるように、接続ごとの goroutine で呼ぶ。
func (s *Server) admit(a *acceptor, conn protocol.ServerConn, pc *progressConn) {
	defer s.client.recoverPanic()
	ctx, cancel := helloContext(pc, helloTimeout, a.done)
	h, err := handshakeKinds(ctx, conn, protocol.KindEdit, protocol.KindWait, protocol.KindPopup, protocol.KindPane)
	cancel()
	switch {
	case errors.Is(err, errUnsupportedKind):
		s.log.Info("知らない種類の client の接続を断った", "err", err)
		s.reject(conn, UnsupportedReason)
		return
	case err != nil:
		s.log.Warn("client の接続を断った", "err", err)
		_ = conn.Close()
		return
	case h.Kind == protocol.KindWait:
		s.serveWait(a, conn, h)
		return
	case h.Kind == protocol.KindPopup, h.Kind == protocol.KindPane:
		s.serveComponent(a, conn, h)
		return
	}
	if s.herdrPopups != nil {
		// ペインを知らせない client は、中央のポップアップを mod に頼まない（editor の requestPopup）ので、調べる記録に残す。
		herdrTrace("client", "pane", h.Pane, "files", h.Files)
		if h.Pane != "" {
			// 最初のポップアップの前に、herdr app の mod がポップアップを知っているかを問うておく（#452）。
			s.herdrPopups.prime(h.Pane)
		}
	}
	select {
	case a.joined <- joined{conn: conn, hello: h}:
	case <-a.done:
		s.reject(conn, "server が終わった")
	}
}

// serveWait は、待つだけの client の接続 conn を扱う（FR-83）。admit の goroutine で、待ち終えるまで動く。
//
// Hello のファイルを、Hello の Parent の編集の client のタブに開かせ（editor.Editor.Wait）、それらのタブがすべて閉じられたら、
// 理由の無い終わりの知らせを送って閉じる。待つだけの client はそれを受けて正常に終わる（git はコミットを続ける）。
// ファイルを開けないとき、タブを開いた client が終わったとき、server が終わるときは、理由付きの終わりの知らせを送る
// （待つだけの client は 0 でない終了コードで終わる）。待つだけの client の接続が先に切れたら、待つのをやめる（タブは残す）。
//
// req: FR-83
func (s *Server) serveWait(a *acceptor, conn protocol.ServerConn, h protocol.Hello) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-a.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	ended := make(chan string, 1)
	stopWait, err := s.ed.Wait(ctx, editor.WaitInfo{
		Files: h.Files, ReadOnly: h.ReadOnly, Stdin: h.Stdin, Parent: editor.ClientID(h.Parent),
		Done: func(reason string) { ended <- reason },
	})
	h.Stdin = nil // 中身はバッファが持つ。待つ間は覚えておかない
	if err != nil {
		reason := err.Error()
		if errors.Is(err, editor.ErrStopped) || errors.Is(err, context.Canceled) {
			reason = "server が終わった"
		}
		s.log.Info("待つだけの client を断った", "err", err)
		s.reject(conn, reason)
		return
	}
	s.log.Info("待つだけの client を足した", "files", h.Files, "parent", h.Parent)
	gone := make(chan struct{})
	go func() {
		defer s.client.recoverPanic()
		defer close(gone)
		for {
			if _, err := conn.Recv(); err != nil {
				return // 待つだけの client は何も送らない。接続が切れるか、こちらが閉じるまで読む
			}
		}
	}()
	select {
	case reason := <-ended:
		s.reject(conn, reason)
	case <-gone:
		stopWait()
		_ = conn.Close()
	case <-a.done:
		s.reject(conn, "server が終わった")
	}
}

// UnsupportedReason は、知らない種類の client を断るときに、終わりの知らせ（protocol.Exit）の理由に載せる文言である。
const UnsupportedReason = "この種類の client はまだ使えない"

// rejectTimeout は、断る client へ終わりの知らせを送るのを待つ上限である。超えたら送らずに閉じる。
const rejectTimeout = time.Second

// reject は、受け付けない接続 conn に、理由 reason の終わりの知らせを送ってから閉じる。
// client は、server が異常終了したとみなさずに、理由を出して終わる（基本設計 1.2）。
func (s *Server) reject(conn protocol.ServerConn, reason string) {
	timer := time.AfterFunc(rejectTimeout, func() { _ = conn.Close() }) // 送り終えなければ閉じて Send を戻す
	defer timer.Stop()
	if err := conn.Send(protocol.Exit{Reason: reason}); err != nil && !errors.Is(err, protocol.ErrClosed) {
		s.log.Warn("断る client へ終わりの知らせを送れない", "err", err)
	}
	_ = conn.Close() // 閉じる誤りは、client の側の Recv で分かる
}

func (a *acceptor) stop() {
	close(a.done)
	_ = a.ln.Close() // 受け付けの goroutine を止める。閉じる誤りは扱えない
}

// Close は、端末多重化ソフトの層の操作の待ちを打ち切り（muxStop）、拡張機能を止める（editor.Editor.Close）。
// Run が戻り、client が端末を戻した後に呼ぶ。
func (s *Server) Close(ctx context.Context) error {
	if s.muxStop != nil {
		s.muxStop()
	}
	return s.ed.Close(ctx)
}

// clientTerm は、1 つの client との接続を editor.Terminal の口に合わせる。
// イベントループからは端末に見え、Draw と SetClipboard を client へのメッセージにする。
type clientTerm struct {
	onPanic func()
	log     *slog.Logger
	events  chan editor.TermEvent

	id   editor.ClientID
	conn protocol.ServerConn
	// width と height は、client が Hello で知らせた端末の大きさである。
	width, height int
	// terminal は、client が Hello で端末の受け渡し（protocol.FeatureTerminal）を宣言したことを表す。attach の後は変えない。
	terminal bool

	out outbox
	// ended は、end を 1 回だけ行うためのものである。
	ended sync.Once
	// onEnd は、end の後に呼ぶ関数である（Server の一覧から外す）。newTerm で決め、後から変えない。
	onEnd func()
}

// end は、まだ送っていないクリップボードへの書き込みと、終わりの知らせ（理由 reason）を送り（finish）、接続を閉じる。
// 2 回目からは何もしない。attach の前に呼んだら、接続を閉じるだけにする。
func (t *clientTerm) end(reason string) {
	t.ended.Do(func() {
		if t.conn == nil {
			return
		}
		if t.out.stop != nil {
			t.finish(reason)
		}
		_ = t.conn.Close() // 閉じる誤りは、client の側の Recv の誤りで分かる
	})
	if t.onEnd != nil {
		t.onEnd()
	}
}

// Exit は、editor.ClientExiter である。client が :q で最後のタブを閉じ、他の client が残るときに、イベントループが呼ぶ。
// 送り終えるのを待つとイベントループが止まるので、別の goroutine で end を行う。
func (t *clientTerm) Exit(reason string) {
	go func() {
		defer t.recoverPanic()
		t.end(reason)
	}()
}

// outbox は、client へ送るのを待つ画面とクリップボードへの書き込みの置き場である。
// イベントループが入れ、送る goroutine（clientTerm.send）が取り出す。
//
// 画面もクリップボードへの書き込みも、まだ送っていない最新の 1 つだけを置く。
// クリップボードは OSC 52 でホストのクリップボードに残るのが最後の 1 つなので、まとめても利用者から見て変わらない。
// 読まない client（端末の出力を <C-s> で止めた、固まったペイン）の間にヤンクを繰り返しても、置き場は大きくならない（PR #200 のレビューの推奨 7）。
//
// 端末の受け渡しの頼み（protocol.TerminalRun、TerminalReclaim、TerminalRelease。FR-80、FR-117）は、捨てずに入れた順に ctrl に置き、
// 「クリップボード、頼み、画面」の順に送る。頼みを入れた後に組み立てた画面は、必ず頼みの後に届く。
// client は、端末を渡している間の画面を捨て、取り戻したら次の画面で全体を描き直すので、TerminalReclaim の後に画面が届かないと何も描けない。
// イベントループは、頼みを入れた処理の後に必ず描く（Run の各周回の終わり）ので、この順なら取り戻した後の画面が届く。
type outbox struct {
	mu     sync.Mutex
	screen *ui.Screen               // まだ送っていない最新の画面。nil なら無い
	clip   *string                  // まだ送っていない最新のクリップボードへの書き込み。nil なら無い
	ctrl   []protocol.ServerMessage // まだ送っていない端末の受け渡しの頼み（入れた順）
	exit   string                   // finish で決めた、終わりの知らせ（protocol.Exit）の理由

	wake chan struct{} // 置き場に入れたことを送る goroutine に知らせる。溜めは 1
	stop chan struct{} // Run の終わりで閉じる
	done chan struct{} // 送る goroutine が終わったら閉じる
}

// attach は、Hello を受けた conn を繋ぎ、イベントを読む goroutine と、client へ送る goroutine を起こす。
func (t *clientTerm) attach(id editor.ClientID, conn protocol.ServerConn, hello protocol.Hello) {
	t.id, t.conn = id, conn
	t.width, t.height = defaultSize(hello.Width, hello.Height)
	t.terminal = hello.HasFeature(protocol.FeatureTerminal)
	t.out.wake = make(chan struct{}, 1)
	t.out.stop = make(chan struct{})
	t.out.done = make(chan struct{})
	go t.read()
	go t.send()
}

// defaultWidth と defaultHeight は、client が Hello で端末の大きさを知らせなかったときの大きさである（term.Terminal.Size と同じ）。
const defaultWidth, defaultHeight = 80, 24

// defaultSize は、Hello の大きさを格子の大きさにする。知らせない（0 以下）なら既定の大きさにし、
// 上限を超えれば断らずに上限へ切り詰める（protocol.ClampSize。大きな端末でも起動できるようにする）。
func defaultSize(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return defaultWidth, defaultHeight
	}
	return protocol.ClampSize(w, h)
}

// finish は、イベントループが終わった後に呼ぶ。まだ送っていないクリップボードへの書き込みと、終わりの知らせ（理由 reason）を
// 送り終えるのを、flushTimeout まで待つ。画面は送らない（client はこの後に端末を戻す）。
func (t *clientTerm) finish(reason string) {
	t.out.mu.Lock()
	t.out.exit = reason
	t.out.mu.Unlock()
	close(t.out.stop)
	select {
	case <-t.out.done:
	case <-time.After(flushTimeout):
		t.log.Warn("client へのクリップボードの書き込みを送り終えないまま接続を閉じる")
	}
}

// read は、client から届いたイベントを、client の識別子を付けてイベントループへ送る。
// 接続が閉じたら events を閉じる。Hello がもう一度届いたら、無視する。
func (t *clientTerm) read() {
	defer close(t.events)
	defer t.recoverPanic()
	for {
		m, err := t.conn.Recv()
		if err != nil {
			if !errors.Is(err, protocol.ErrClosed) && !isEOF(err) {
				t.log.Error("client からの受信に失敗", "err", err)
			}
			return
		}
		var ev protocol.Event
		switch m := m.(type) {
		case protocol.Event:
			ev = m
		case protocol.TerminalDone:
			t.events <- editor.TermEvent{Client: t.id, Terminal: &editor.TerminalResult{ID: m.ID, Status: m.Status, Err: m.Err}}
			continue
		case protocol.TerminalReleased:
			t.events <- editor.TermEvent{Client: t.id, Terminal: &editor.TerminalResult{ID: m.ID, Released: true}}
			continue
		default:
			t.log.Warn("client から 2 回目の Hello が届いた。無視する")
			continue
		}
		if ev.Resize {
			if ev.Width <= 0 || ev.Height <= 0 {
				t.log.Warn("client が知らせた端末の大きさが 0 以下なので、無視する", "width", ev.Width, "height", ev.Height)
				continue
			}
			// 上限を超える大きさは、断らずに上限へ切り詰める（protocol.ClampSize）
			ev.Width, ev.Height = protocol.ClampSize(ev.Width, ev.Height)
		}
		t.events <- t.termEvent(ev)
	}
}

// send は、置き場の画面とクリップボードへの書き込みを client へ送る。
// Run が終わったら、残ったクリップボードへの書き込みと終わりの知らせを送って終わる。接続が閉じても終わる。
func (t *clientTerm) send() {
	defer close(t.out.done)
	defer t.recoverPanic()
	for {
		stopping := false
		select {
		case <-t.out.wake:
		case <-t.out.stop:
			stopping = true
		}
		t.out.mu.Lock()
		scr, clip, ctrl, reason := t.out.screen, t.out.clip, t.out.ctrl, t.out.exit
		t.out.screen, t.out.clip, t.out.ctrl = nil, nil, nil
		t.out.mu.Unlock()
		if clip != nil && !t.sent(t.conn.Send(protocol.SetClipboard{Text: *clip})) {
			return
		}
		if stopping {
			t.sent(t.conn.Send(protocol.Exit{Reason: reason}))
			return
		}
		for _, m := range ctrl {
			if !t.sent(t.conn.Send(m)) {
				return
			}
		}
		if scr != nil && !t.sent(t.conn.Send(protocol.Draw{Screen: *scr})) {
			return
		}
	}
}

// sent は、送った結果 err を見て、送り続けるかを返す。接続が閉じていれば false を返す。それ以外の誤りは記録して続ける。
func (t *clientTerm) sent(err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, protocol.ErrClosed):
		return false
	}
	t.log.Error("client への送信に失敗", "err", err)
	return true
}

func isEOF(err error) bool { return errors.Is(err, io.EOF) }

func (t *clientTerm) wakeSender() {
	select {
	case t.out.wake <- struct{}{}:
	default:
	}
}

func (t *clientTerm) termEvent(ev protocol.Event) editor.TermEvent {
	return editor.TermEvent{
		Key:    ev.Key,
		Paste:  ev.Paste,
		Resize: ev.Resize,
		Width:  ev.Width,
		Height: ev.Height,
		// マウスの出来事の位置（FR-184）は、この client の画面の座標のまま渡す。
		MouseRow: ev.MouseRow,
		MouseCol: ev.MouseCol,
		Client:   t.id,
	}
}

// recoverPanic は、server が起こす goroutine の始めに defer で呼ぶ。panic していれば onPanic を呼んでから panic を続ける（基本設計 8 章）。
func (t *clientTerm) recoverPanic() {
	if p := recover(); p != nil {
		if t.onPanic != nil {
			t.onPanic()
		}
		panic(p)
	}
}

// Input は、client から届いたイベントを送るチャネルを返す。client が接続を閉じると閉じる。
func (t *clientTerm) Input() <-chan editor.TermEvent { return t.events }

// Size は、client が Hello で知らせた端末の大きさを返す。
func (t *clientTerm) Size() (width, height int) { return t.width, t.height }

// Draw は、画面 s を client へ送る置き場に入れる。送り終えるのを待たない。
// まだ送っていない前の画面があれば、s で置き換える（client には最新の画面だけが届く）。
func (t *clientTerm) Draw(s ui.Screen) error {
	t.out.mu.Lock()
	t.out.screen = &s
	t.out.mu.Unlock()
	t.wakeSender()
	return nil
}

// HandsOverTerminal は、editor.TerminalHandover である。client が Hello で端末の受け渡しを宣言したかを返す。
// 宣言の無い client（版の違う client）は terminal/run を読み飛ばすので、頼まない。
func (t *clientTerm) HandsOverTerminal() bool { return t.terminal }

// RunInTerminal は、editor.TerminalHandover である。端末を子プロセスへ渡して job を動かすよう client に頼む（FR-79、FR-80）。
func (t *clientTerm) RunInTerminal(job editor.TerminalJob) {
	cmds := make([]protocol.TerminalCommand, len(job.Commands))
	for i, c := range job.Commands {
		cmds[i] = protocol.TerminalCommand(c)
	}
	t.control(protocol.TerminalRun{ID: job.ID, Commands: cmds, Env: job.Env, Prompt: job.Prompt})
}

// ReclaimTerminal は、editor.TerminalHandover である。実行中の id の間に端末を取り戻すよう client に頼む（FR-117）。
func (t *clientTerm) ReclaimTerminal(id uint64) { t.control(protocol.TerminalReclaim{ID: id}) }

// ReleaseTerminal は、editor.TerminalHandover である。取り戻した端末を子プロセスへ渡し直すよう client に頼む（FR-117）。
func (t *clientTerm) ReleaseTerminal(id uint64) { t.control(protocol.TerminalRelease{ID: id}) }

// control は、端末の受け渡しの頼み m を、送る置き場の列に入れる。送り終えるのを待たない（outbox の doc）。
func (t *clientTerm) control(m protocol.ServerMessage) {
	t.out.mu.Lock()
	t.out.ctrl = append(t.out.ctrl, m)
	t.out.mu.Unlock()
	t.wakeSender()
}

// SetClipboard は、text をホストのクリップボードへ送るよう client に頼む（FR-56）。送り終えるのを待たない。
// まだ送っていない前の書き込みがあれば、text で置き換える（outbox の doc）。
func (t *clientTerm) SetClipboard(text string) error {
	t.out.mu.Lock()
	t.out.clip = &text
	t.out.mu.Unlock()
	t.wakeSender()
	return nil
}
