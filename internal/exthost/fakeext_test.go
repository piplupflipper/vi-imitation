package exthost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// テスト用の拡張機能は、テストの実行ファイル自身を子プロセスとして起動したものである。
// 環境変数 fakeModeEnv があれば、TestMain はテストを実行せずに拡張機能として動く。
// 拡張機能は internal/rpc と ext の型で直接書く（ext の補助関数には依存しない）。
const fakeModeEnv = "EXTHOST_FAKE_EXT_MODE"

// 拡張機能の動き方（fakeModeEnv の値）。
const (
	// modeNormal は、すべてのリクエストに応答する。
	modeNormal = "normal"
	// modeNoInit は、initialize に応答しない。
	modeNoInit = "noinit"
	// modeExit は、起動してすぐに終了する。
	modeExit = "exit"
	// modeServer は、ext/server で書いた拡張機能として動く（serverext_test.go）。
	modeServer = "server"
	// modeIgnoreShutdown は、shutdown を無視する。SIGTERM では既定の動作どおりに終わる。
	modeIgnoreShutdown = "ignoreshutdown"
	// modeStubborn は、shutdown を無視し、SIGTERM を受け取っても標準エラー出力に記録するだけで終わらない。
	modeStubborn = "stubborn"
	// modeSlowInit は、initialize に slowInitDelay だけ遅れて応答する。それ以外は modeNormal と同じである。
	modeSlowInit = "slowinit"
	// modeSocket は、modeNormal に加えて、本体から引き継いだ待ち受けと接続（socket の契機）で、
	// 接続ごとに自分のプロセス ID と引き継いだ接続の数を 1 行書いて閉じる（socket_test.go）。
	modeSocket = "socket"
)

// slowInitDelay は、modeSlowInit の拡張機能が initialize の応答を遅らせる時間である。
const slowInitDelay = 300 * time.Millisecond

// stubbornSIGTERM は、modeStubborn の拡張機能が SIGTERM を受け取ったときに標準エラー出力へ書く行である。
const stubbornSIGTERM = "fake extension got SIGTERM"

// テストから拡張機能へ送る、テスト専用のリクエスト。
const (
	methodState      = "test/state"      // 拡張機能が持つバッファの写しと、受け取ったメッセージの記録を返す
	methodExit       = "test/exit"       // 応答せずに終了コード 3 で終わる
	methodPanic      = "test/panic"      // panic する
	methodHang       = "test/hang"       // 読み取りの goroutine の中で無限ループに入る
	methodGrandchild = "test/grandchild" // 標準出力を継承した孫プロセス（sleep）を起動し、その pid を返す
	methodForget     = "test/forget"     // バッファのバージョン番号を食い違わせる（次の buffer/change で buffer/resync を送る）
	methodEdit       = "test/edit"       // params の BufferEditParams で本体へ buffer/edit を送り、その結果を返す
	methodNotify     = "test/notify"     // decoration/set、message/show、progress/report を送る
	methodGarbage    = "test/garbage"    // JSON-RPC として解釈できないメッセージを標準出力へ書く
	methodFlood      = "test/flood"      // message/show を params の数だけ送る
	methodEnv        = "test/env"        // 環境変数 ext.EnvExtensionDir と ext.EnvMCPSocket の値と作業ディレクトリを返す
)

// fakeArgs は、問い合わせの Args に入れる、テスト用の指示である。
type fakeArgs struct {
	// DelayMs は、応答を遅らせるミリ秒である。
	DelayMs int `json:"delayMs"`
}

// fakeEnv は、methodEnv の応答である。
type fakeEnv struct {
	Dir    string `json:"dir"`
	DirSet bool   `json:"dirSet"`
	Wd     string `json:"wd"`
	// MCP は、環境変数 ext.EnvMCPSocket の値である。
	MCP    string `json:"mcp"`
	MCPSet bool   `json:"mcpSet"`
	// SocketFDs は、環境変数 ext.EnvSocketFDs の値である。
	SocketFDs    string `json:"socketFDs"`
	SocketFDsSet bool   `json:"socketFDsSet"`
}

// fakeBuffer は、拡張機能が持つバッファの写しである。
type fakeBuffer struct {
	Version ext.Version `json:"version"`
	Text    string      `json:"text"`
}

// fakeState は、test/state の結果である。
type fakeState struct {
	PID     int                         `json:"pid"`
	Buffers map[ext.BufferID]fakeBuffer `json:"buffers"`
	// FinalNewline は、バッファごとに、最後に受け取った buffer/open の finalNewline である。省かれていれば nil。
	FinalNewline map[ext.BufferID]*bool `json:"finalNewline"`
	// Log は、受け取ったメッセージと、取り消しの記録である。
	Log []string `json:"log"`
}

// fakeExt は、テスト用の拡張機能の状態である。
type fakeExt struct {
	mode string
	conn *rpc.Conn
	// ready は、conn を代入した後に閉じる。ハンドラーは conn を読む前にこれを待つ。
	// NewConn は代入より先に読み取りの goroutine を始めるので、起動直後のリクエストのハンドラーが nil の conn を読まないようにする。
	ready chan struct{}

	mu    sync.Mutex
	docs  map[ext.BufferID]*fakeDoc
	log   []string
	spins atomic.Int64
}

type fakeDoc struct {
	version      ext.Version
	lines        []string
	finalNewline *bool
}

func runFakeExt(mode string) int {
	fmt.Fprintf(os.Stderr, "fake extension started mode=%s\n", mode)
	if mode == modeExit {
		return 2
	}
	if mode == modeServer {
		return runServerExt()
	}
	if mode == modeStubborn {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGTERM)
		go func() {
			for range sigs {
				fmt.Fprintln(os.Stderr, stubbornSIGTERM)
			}
		}()
	}
	if mode == modeSocket {
		serveInheritedSocket()
	}
	f := &fakeExt{mode: mode, docs: map[ext.BufferID]*fakeDoc{}, ready: make(chan struct{})}
	f.conn = rpc.NewConn(os.Stdin, os.Stdout, rpc.HandlerFuncs{Request: f.request, Notification: f.notification})
	close(f.ready)
	<-f.conn.Done()
	return 0
}

// serveInheritedSocket は、本体から引き継いだ接続と、引き継いだ待ち受けで受け付けた接続へ、
// "<プロセス ID> <引き継いだ接続の数>" の 1 行を書いて閉じる。待ち受けを引き継いでいなければ何もしない。
func serveInheritedSocket() {
	ln, conns, err := server.InheritedSocket()
	if err != nil {
		fmt.Fprintf(os.Stderr, "inherited socket: %v\n", err)
	}
	if ln == nil {
		return
	}
	line := fmt.Sprintf("%d %d\n", os.Getpid(), len(conns))
	answer := func(c net.Conn) {
		_, _ = io.WriteString(c, line)
		_ = c.Close()
	}
	for _, c := range conns {
		answer(c)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			answer(c)
		}
	}()
}

func (f *fakeExt) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, fmt.Sprintf(format, args...))
}

func (f *fakeExt) request(ctx context.Context, req *rpc.Request, reply rpc.Replier) {
	<-f.ready
	f.record("request:%s", req.Method)
	switch req.Method {
	case ext.MethodInitialize:
		var p ext.InitializeParams
		_ = json.Unmarshal(req.Params, &p)
		if p.NoBufferSync {
			f.record("initialize:noBufferSync")
		}
		if f.mode == modeNoInit {
			return
		}
		if f.mode == modeSlowInit {
			time.Sleep(slowInitDelay)
		}
		_ = reply(ext.InitializeResult{APIVersion: ext.APIVersion}, nil)
	case ext.MethodPing:
		_ = reply(ext.PingResult{}, nil)
	case ext.MethodTextObjectResolve:
		var p ext.TextObjectResolveParams
		_ = json.Unmarshal(req.Params, &p)
		f.later(ctx, req.Method, p.Count, p.Args, reply, ext.TextObjectResolveResult{
			OK: true, Version: p.Version,
			Range: ext.Range{Start: p.Cursor, End: ext.Position{Line: p.Cursor.Line, Byte: p.Cursor.Byte + p.Count}},
		})
	case ext.MethodMotionResolve:
		var p ext.MotionResolveParams
		_ = json.Unmarshal(req.Params, &p)
		f.later(ctx, req.Method, p.Count, p.Args, reply, ext.MotionResolveResult{
			OK: true, Version: p.Version, Position: ext.Position{Line: p.Cursor.Line, Byte: p.Cursor.Byte + p.Count},
		})
	case ext.MethodOperatorApply:
		var p ext.OperatorApplyParams
		_ = json.Unmarshal(req.Params, &p)
		f.later(ctx, req.Method, p.Count, p.Args, reply, ext.OperatorApplyResult{
			OK: true, Transaction: ext.Transaction{BaseVersion: p.Version, Edits: []ext.Edit{{Range: p.Range, Text: "X"}}},
		})
	case ext.MethodCommandExecute:
		var p ext.CommandExecuteParams
		_ = json.Unmarshal(req.Params, &p)
		f.record("command:%s:withheld=%v", p.ID, p.Withheld)
		f.later(ctx, req.Method, p.Count, p.Args, reply, ext.CommandExecuteResult{
			OK: true, Transaction: &ext.Transaction{BaseVersion: p.Version, Edits: []ext.Edit{{Text: p.ID}}},
		})
	case methodState:
		_ = reply(f.state(), nil)
	case methodEnv:
		wd, err := os.Getwd()
		dir, ok := os.LookupEnv(ext.EnvExtensionDir)
		mcp, mcpSet := os.LookupEnv(ext.EnvMCPSocket)
		fds, fdsSet := os.LookupEnv(ext.EnvSocketFDs)
		_ = reply(fakeEnv{Dir: dir, DirSet: ok, Wd: wd, MCP: mcp, MCPSet: mcpSet, SocketFDs: fds, SocketFDsSet: fdsSet}, err)
	case methodExit:
		os.Exit(3)
	case methodPanic:
		panic("fake extension panic")
	case methodHang:
		for {
			f.spins.Add(1)
		}
	case methodGrandchild:
		cmd := exec.Command("sleep", "1000")
		cmd.Stdout = os.Stdout
		if err := cmd.Start(); err != nil {
			_ = reply(nil, err)
			return
		}
		_ = reply(cmd.Process.Pid, nil)
	case methodForget:
		var p ext.BufferCloseParams
		_ = json.Unmarshal(req.Params, &p)
		f.mu.Lock()
		if d, ok := f.docs[p.Buffer]; ok {
			d.version += 1000
		}
		f.mu.Unlock()
		_ = reply(nil, nil)
	case methodEdit:
		var p ext.BufferEditParams
		_ = json.Unmarshal(req.Params, &p)
		go func() {
			var res ext.BufferEditResult
			err := f.conn.Call(ctx, ext.MethodBufferEdit, p, &res)
			_ = reply(res, err)
		}()
	case methodNotify:
		_ = f.conn.Notify(ext.MethodDecorationSet, ext.DecorationSetParams{
			Buffer: 1, Version: 7, Decorations: []ext.Decoration{{Style: "keyword"}},
		})
		_ = f.conn.Notify(ext.MethodMessageShow, ext.MessageShowParams{Level: ext.MessageInfo, Text: "hello"})
		// 識別子の無い progress/report は本体が捨てるので、後の正しいものだけがイベントになる。
		_ = f.conn.Notify(ext.MethodProgressReport, ext.ProgressReportParams{Kind: ext.ProgressBegin, Title: "bad"})
		_ = f.conn.Notify(ext.MethodProgressReport, ext.ProgressReportParams{Token: "t", Kind: ext.ProgressBegin, Title: "load", Percentage: ext.Percent(5)})
		// 検査を通らない band/set（窓が空）も本体が捨てるので、後の正しいものだけがイベントになる。
		_ = f.conn.Notify(ext.MethodBandSet, ext.BandSetParams{Lines: []ext.BandLine{{Text: "bad"}}})
		_ = f.conn.Notify(ext.MethodBandSet, ext.BandSetParams{Window: "client/1", Buffer: 1, Version: 7, Lines: []ext.BandLine{{Text: "func f() {", Line: ext.SourceLine(2)}}})
		_ = reply(nil, nil)
	case methodGarbage:
		_, _ = os.Stdout.WriteString("Content-Length: 5\r\n\r\nhello")
	case methodFlood:
		var n int
		_ = json.Unmarshal(req.Params, &n)
		for i := range n {
			_ = f.conn.Notify(ext.MethodMessageShow, ext.MessageShowParams{Level: ext.MessageInfo, Text: fmt.Sprint(i)})
		}
		_ = reply(nil, nil)
	default:
		_ = reply(nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: req.Method})
	}
}

// later は、Args の DelayMs だけ待ってから result で応答する。待つ間に取り消されたら記録して応答しない。
// 取り消しの通知を無視する拡張機能を演じるときは、遅れた応答も送る（count が負のとき）。
func (f *fakeExt) later(ctx context.Context, method string, count int, raw json.RawMessage, reply rpc.Replier, result any) {
	var a fakeArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &a)
	}
	go func() {
		select {
		case <-time.After(time.Duration(a.DelayMs) * time.Millisecond):
		case <-ctx.Done():
			f.record("cancelled:%s:%d", method, count)
		}
		// 取り消されても応答を送る。本体が遅れた応答を捨てることを確かめるためである。
		_ = reply(result, nil)
	}()
}

func (f *fakeExt) notification(_ context.Context, n *rpc.Notification) {
	<-f.ready
	switch n.Method {
	case ext.MethodBufferOpen:
		var p ext.BufferOpenParams
		_ = json.Unmarshal(n.Params, &p)
		f.mu.Lock()
		f.docs[p.Buffer] = &fakeDoc{version: p.Version, lines: strings.Split(p.Text, "\n"), finalNewline: p.FinalNewline}
		f.log = append(f.log, fmt.Sprintf("open:%d:%d", p.Buffer, p.Version))
		f.mu.Unlock()
	case ext.MethodBufferChange:
		var p ext.BufferChangeParams
		_ = json.Unmarshal(n.Params, &p)
		f.mu.Lock()
		d, ok := f.docs[p.Buffer]
		if !ok || d.version != p.FromVersion {
			f.log = append(f.log, fmt.Sprintf("resync:%d", p.Buffer))
			var v ext.Version
			if ok {
				v = d.version
			}
			f.mu.Unlock()
			_ = f.conn.Notify(ext.MethodBufferResync, ext.BufferResyncParams{Buffer: p.Buffer, Version: v})
			return
		}
		lines, err := applyEdits(d.lines, p.Edits)
		d.lines = lines
		if err != nil {
			f.log = append(f.log, fmt.Sprintf("bad-change:%d:%v", p.Buffer, err))
		} else {
			d.version = p.ToVersion
			f.log = append(f.log, fmt.Sprintf("change:%d:%d->%d", p.Buffer, p.FromVersion, p.ToVersion))
		}
		f.mu.Unlock()
	case ext.MethodBufferClose:
		var p ext.BufferCloseParams
		_ = json.Unmarshal(n.Params, &p)
		f.mu.Lock()
		delete(f.docs, p.Buffer)
		f.log = append(f.log, fmt.Sprintf("close:%d", p.Buffer))
		f.mu.Unlock()
	case ext.MethodShutdown:
		f.record("notification:%s", n.Method)
		if f.mode == modeNormal || f.mode == modeSlowInit || f.mode == modeSocket {
			// 片付けるものは無いので、すぐに終わる。
			os.Exit(0)
		}
	default:
		f.record("notification:%s", n.Method)
	}
}

func (f *fakeExt) state() fakeState {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := fakeState{
		PID: os.Getpid(), Buffers: map[ext.BufferID]fakeBuffer{}, FinalNewline: map[ext.BufferID]*bool{},
		Log: append([]string(nil), f.log...),
	}
	for id, d := range f.docs {
		s.Buffers[id] = fakeBuffer{Version: d.version, Text: strings.Join(d.lines, "\n")}
		s.FinalNewline[id] = d.finalNewline
	}
	return s
}

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeModeEnv); mode != "" {
		os.Exit(runFakeExt(mode))
	}
	os.Exit(m.Run())
}
