package main

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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

const testTimeout = 10 * time.Second

// fakeAdapter は、TestMain が testdata/fakeadapter をビルドした実行ファイルのパスである。
var fakeAdapter string

// extBinary は、TestMain がこの拡張機能をビルドした実行ファイルのパスである。拡張機能のプロセスを強制終了するテストで使う。
var extBinary string

func TestMain(m *testing.M) {
	// テストの中の拡張機能は、アダプターの見張りに、このテストの実行ファイルを使う（os.Executable）。
	maybeRunGuard()
	dir, err := os.MkdirTemp("", "ext-dap-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeAdapter = filepath.Join(dir, "fakeadapter")
	out, err := exec.Command("go", "build", "-o", fakeAdapter, "./testdata/fakeadapter").CombinedOutput() //nolint:gosec // テスト用のデバッグアダプターを一時ディレクトリへビルドする
	if err != nil {
		fmt.Fprintf(os.Stderr, "build fakeadapter: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	extBinary = filepath.Join(dir, "vi-imitation-ext-dap")
	out, err = exec.Command("go", "build", "-o", extBinary, ".").CombinedOutput() //nolint:gosec // テストのために拡張機能を一時ディレクトリへビルドする
	if err != nil {
		fmt.Fprintf(os.Stderr, "build extension: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// host は、本体の役である。拡張機能の Server と io.Pipe でつなぎ、届いた通知とリクエストを通り道へ流す。
type host struct {
	t    *testing.T
	conn *rpc.Conn
	d    *debugger

	signs   chan ext.SignSetParams
	msgs    chan ext.MessageShowParams
	outputs chan string
	opens   chan ext.EditorOpenParams

	panes   chan openedPane
	closes  chan ext.ViewID
	asks    chan ext.ChoiceAskParams
	answers chan ext.ChoiceAskResult
	// inputAsks と inputAnswers は、input/ask の問いと、テストが渡す答えである。
	inputAsks    chan ext.InputAskParams
	inputAnswers chan ext.InputAskResult
	// updates と appends は、拡張機能が送った view/update と view/append である（ペインのテスト）。
	updates chan ext.ViewUpdateParams
	appends chan ext.ViewAppendParams
	// popups は、拡張機能が popup/open で開いた部品である（ウォッチ式の区画の値のヤンク）。
	popups chan ext.PopupOpenParams
	// registers は、拡張機能が register/set で入れた値である（ウォッチ式の区画の c。FR-311）。
	registers chan ext.RegisterSetParams

	mu       sync.Mutex
	versions map[ext.BufferID]ext.Version
	ids      map[string]ext.BufferID
	// workDir は、editor/state で返す作業ディレクトリである。
	workDir  string
	nextView ext.ViewID
}

// openedPane は、拡張機能が pane/open で開いた部品である。
type openedPane struct {
	id     ext.ViewID
	params ext.PaneOpenParams
}

// newHost は、settings（JSON。空なら設定無し）で拡張機能を初期化した本体の役を返す。
func newHost(t *testing.T, settings string) *host {
	t.Helper()
	d := newDebugger()
	if !testing.Verbose() {
		// デバッグアダプターの標準エラー出力などのログは、-v のときだけ出す。
		d.logger.SetOutput(io.Discard)
	}
	d.timeouts = timeouts{initialize: testTimeout, launch: testTimeout, stop: 2 * time.Second, request: testTimeout}
	h := newHostState(t)
	h.d = d
	// デバッグ対象の出力は、セッションの写しに足したものを受け取る（output/append の欄には書かない）。
	d.outputTap = func(s string) {
		select {
		case h.outputs <- s:
		default:
		}
	}
	hostR, extW := io.Pipe()
	extR, hostW := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- d.srv.Serve(context.Background(), extR, extW) }()
	h.conn = rpc.NewConn(hostR, hostW, rpc.HandlerFuncs{
		Notification: h.notification,
		Request:      h.request,
	})
	t.Cleanup(func() {
		d.shutdown()
		_ = h.conn.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve = %v, want nil", err)
			}
		case <-time.After(testTimeout):
			t.Error("Serve did not return after the host closed")
		}
	})
	h.initialize(settings)
	return h
}

// newHostState は、接続の無い本体の役を作る。
func newHostState(t *testing.T) *host {
	return &host{
		t:            t,
		signs:        make(chan ext.SignSetParams, 1024),
		msgs:         make(chan ext.MessageShowParams, 1024),
		outputs:      make(chan string, 1024),
		opens:        make(chan ext.EditorOpenParams, 1024),
		panes:        make(chan openedPane, 16),
		closes:       make(chan ext.ViewID, 16),
		asks:         make(chan ext.ChoiceAskParams, 16),
		answers:      make(chan ext.ChoiceAskResult, 16),
		inputAsks:    make(chan ext.InputAskParams, 16),
		inputAnswers: make(chan ext.InputAskResult, 16),
		updates:      make(chan ext.ViewUpdateParams, 4096),
		appends:      make(chan ext.ViewAppendParams, 4096),
		popups:       make(chan ext.PopupOpenParams, 16),
		registers:    make(chan ext.RegisterSetParams, 1024),
		versions:     map[ext.BufferID]ext.Version{},
		ids:          map[string]ext.BufferID{},
		workDir:      t.TempDir(),
	}
}

// initialize は、settings（JSON。空なら設定無し）で initialize を送る。
func (h *host) initialize(settings string) {
	h.t.Helper()
	p := ext.InitializeParams{APIVersion: ext.APIVersion}
	if settings != "" {
		p.Settings = json.RawMessage(settings)
	}
	var res ext.InitializeResult
	if err := h.conn.Call(h.ctx(), ext.MethodInitialize, p, &res); err != nil {
		h.t.Fatalf("initialize: %v", err)
	}
}

func (h *host) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	h.t.Cleanup(cancel)
	return ctx
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Errorf("decode %T: %v", v, err)
	}
	return v
}

func (h *host) notification(_ context.Context, n *rpc.Notification) {
	switch n.Method {
	case ext.MethodSignSet:
		h.signs <- decode[ext.SignSetParams](h.t, n.Params)
	case ext.MethodMessageShow:
		h.msgs <- decode[ext.MessageShowParams](h.t, n.Params)
	case ext.MethodOutputAppend:
		h.t.Errorf("output/append was sent: %s", n.Params)
	case ext.MethodViewUpdate:
		h.updates <- decode[ext.ViewUpdateParams](h.t, n.Params)
	case ext.MethodViewAppend:
		h.appends <- decode[ext.ViewAppendParams](h.t, n.Params)
	case ext.MethodViewClose:
		h.closes <- decode[ext.ViewCloseParams](h.t, n.Params).View
	}
}

// request は、editor/open、editor/state、pane/open、choice/ask、input/ask に答える。
// editor/open は、開いているバッファだけを扱い、応答の版は本体の役が覚えている現在の版にする。
// editor/state は、作業ディレクトリ workDir だけを返す。
// pane/open は、部品を panes に流し、新しい部品の ID で答える。テストは選んだことを selectItem で知らせる。
// choice/ask は、問いを asks に流し、テストが answer で渡した答えで答える。input/ask も同じく inputAsks と inputAnswers で答える。
func (h *host) request(_ context.Context, req *rpc.Request, reply rpc.Replier) {
	switch req.Method {
	case ext.MethodEditorState:
		h.mu.Lock()
		wd := h.workDir
		h.mu.Unlock()
		_ = reply(ext.EditorStateResult{WorkDir: wd}, nil)
		return
	case ext.MethodPaneOpen:
		p := decode[ext.PaneOpenParams](h.t, req.Params)
		h.mu.Lock()
		h.nextView++
		id := h.nextView
		h.mu.Unlock()
		_ = reply(ext.PaneOpenResult{View: id}, nil)
		h.panes <- openedPane{id: id, params: p}
		return
	case ext.MethodPopupOpen:
		p := decode[ext.PopupOpenParams](h.t, req.Params)
		h.mu.Lock()
		h.nextView++
		id := h.nextView
		h.mu.Unlock()
		_ = reply(ext.PopupOpenResult{View: id}, nil)
		h.popups <- p
		return
	case ext.MethodRegisterSet:
		_ = reply(ext.RegisterSetResult{}, nil)
		h.registers <- decode[ext.RegisterSetParams](h.t, req.Params)
		return
	case ext.MethodChoiceAsk:
		p := decode[ext.ChoiceAskParams](h.t, req.Params)
		h.asks <- p
		go func() { _ = reply(<-h.answers, nil) }()
		return
	case ext.MethodInputAsk:
		p := decode[ext.InputAskParams](h.t, req.Params)
		h.inputAsks <- p
		go func() { _ = reply(<-h.inputAnswers, nil) }()
		return
	case ext.MethodEditorOpen:
	default:
		_ = reply(nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: req.Method})
		return
	}
	p := decode[ext.EditorOpenParams](h.t, req.Params)
	h.opens <- p
	h.mu.Lock()
	defer h.mu.Unlock()
	id := ext.BufferID(0)
	if p.Buffer != nil {
		id = *p.Buffer
	} else {
		var ok bool
		id, ok = h.ids[p.Path]
		if !ok {
			_ = reply(nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "not open: " + p.Path})
			return
		}
	}
	v := h.versions[id]
	_ = reply(ext.EditorOpenResult{Buffer: id, Version: v, Moved: p.Position != nil && (p.Version == nil || *p.Version == v)}, nil)
}

func (h *host) open(id ext.BufferID, path, text string) {
	h.mu.Lock()
	h.versions[id] = 1
	if path != "" {
		h.ids[path] = id
	}
	h.mu.Unlock()
	if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: id, Path: path, Version: 1, Text: text}); err != nil {
		h.t.Fatalf("buffer/open: %v", err)
	}
}

func (h *host) change(id ext.BufferID, edits ...ext.Edit) {
	h.mu.Lock()
	from := h.versions[id]
	h.versions[id] = from + 1
	h.mu.Unlock()
	err := h.conn.Notify(ext.MethodBufferChange, ext.BufferChangeParams{Buffer: id, FromVersion: from, ToVersion: from + 1, Edits: edits})
	if err != nil {
		h.t.Fatalf("buffer/change: %v", err)
	}
}

func (h *host) didSave(id ext.BufferID, path string) {
	h.mu.Lock()
	v := h.versions[id]
	h.mu.Unlock()
	if err := h.conn.Notify(ext.MethodBufferDidSave, ext.BufferDidSaveParams{Buffer: id, Version: v, Path: path}); err != nil {
		h.t.Fatalf("buffer/didSave: %v", err)
	}
}

// テストで呼ぶ本体の操作。段階 2 のコマンド（dap.start など）を置き換えたもの（#203）。
// 開始は、始まっていなければ始める debug.continue で行う。
const (
	cmdStart            = ext.OpDebugContinue
	cmdStop             = ext.OpDebugTerminate
	cmdToggleBreakpoint = ext.OpBreakpointToggle
	cmdContinue         = ext.OpDebugContinue
	cmdNext             = ext.OpDebugStepOver
	cmdStepIn           = ext.OpDebugStepIn
	cmdStepOut          = ext.OpDebugStepOut
)

// exec は、本体の操作 op を、バッファ buf のカーソルの行 line で provider/invoke で呼ぶ。
func (h *host) exec(buf ext.BufferID, op ext.Operation, line int) ext.ProviderInvokeResult {
	h.t.Helper()
	return h.invoke(buf, op, line, "")
}

// invoke は、exec と同じだが、args を付けられる。args が空なら省く。
func (h *host) invoke(buf ext.BufferID, op ext.Operation, line int, args string) ext.ProviderInvokeResult {
	h.t.Helper()
	h.mu.Lock()
	v := h.versions[buf]
	h.mu.Unlock()
	var res ext.ProviderInvokeResult
	p := ext.ProviderInvokeParams{Operation: op, Buffer: buf, Version: v, Cursor: ext.Position{Line: line}}
	if args != "" {
		p.Args = json.RawMessage(args)
	}
	if err := h.conn.Call(h.ctx(), ext.MethodProviderInvoke, p, &res); err != nil {
		h.t.Fatalf("provider/invoke %s: %v", op, err)
	}
	return res
}

// replay は、本体がマクロを再生しているときと同じく、sync を true にして本体の操作 op を呼ぶ（FR-19）。
// 本体は再生中、応答が届くまで次の操作を送らないので、この呼び出しも応答まで待つ。
func (h *host) replay(op ext.Operation, line int) ext.ProviderInvokeResult {
	h.t.Helper()
	h.mu.Lock()
	v := h.versions[1]
	h.mu.Unlock()
	var res ext.ProviderInvokeResult
	p := ext.ProviderInvokeParams{Operation: op, Buffer: 1, Version: v, Cursor: ext.Position{Line: line}, Sync: true}
	if err := h.conn.Call(h.ctx(), ext.MethodProviderInvoke, p, &res); err != nil {
		h.t.Fatalf("provider/invoke %s (sync): %v", op, err)
	}
	return res
}

// noOpen は、editor/open が届いていないことを確かめる。
func (h *host) noOpen(what string) {
	h.t.Helper()
	select {
	case p := <-h.opens:
		h.t.Errorf("%s: editor/open %+v was sent; want the stop in the result", what, p)
	default:
	}
}

// drainOpens は、届いた editor/open を読み捨てる。
func (h *host) drainOpens() {
	for {
		select {
		case <-h.opens:
		default:
			return
		}
	}
}

// mustExec は、exec の結果の ok が true であることを確かめる。
// テストで編集するバッファは 1 つなので、バッファは 1 に決めている。
func (h *host) mustExec(op ext.Operation, line int) {
	h.t.Helper()
	if res := h.exec(1, op, line); !res.OK {
		h.t.Fatalf("provider/invoke %s: ok = false", op)
	}
}

// waitSigns は、バッファ buf の sign/set のうち、印が want と同じになるものが届くまで待つ。途中の一覧は読み捨てる。
// バッファは mustExec と同じく 1 に決めている。
func (h *host) waitSigns(want ...ext.Sign) {
	const buf ext.BufferID = 1
	h.t.Helper()
	if want == nil {
		want = []ext.Sign{}
	}
	var last []ext.Sign
	deadline := time.After(testTimeout)
	for {
		select {
		case p := <-h.signs:
			if p.Buffer != buf {
				continue
			}
			last = p.Signs
			if slices.Equal(p.Signs, want) {
				return
			}
		case <-deadline:
			h.t.Fatalf("sign/set %v did not arrive; last = %v", want, last)
			return
		}
	}
}

// waitMessage は、text を含む message/show が届くまで待ち、それを返す。
func (h *host) waitMessage(text string) ext.MessageShowParams {
	h.t.Helper()
	var seen []string
	deadline := time.After(testTimeout)
	for {
		select {
		case m := <-h.msgs:
			if strings.Contains(m.Text, text) {
				return m
			}
			seen = append(seen, m.Text)
		case <-deadline:
			h.t.Fatalf("message/show %q did not arrive; seen = %q", text, seen)
			return ext.MessageShowParams{}
		}
	}
}

// waitOutput は、text を含む output/append が届くまで待つ。途中で届いたものを返す。
func (h *host) waitOutput(text string) []string {
	h.t.Helper()
	var seen []string
	deadline := time.After(testTimeout)
	for {
		select {
		case s := <-h.outputs:
			seen = append(seen, s)
			if strings.Contains(s, text) {
				return seen
			}
		case <-deadline:
			h.t.Fatalf("output/append %q did not arrive; seen = %q", text, seen)
			return nil
		}
	}
}

// waitOpen は、editor/open のうち、カーソルの移動先を持つものが届くまで待つ。
func (h *host) waitOpen() ext.EditorOpenParams {
	h.t.Helper()
	deadline := time.After(testTimeout)
	for {
		select {
		case p := <-h.opens:
			if p.Position != nil {
				return p
			}
		case <-deadline:
			h.t.Fatal("editor/open with a position did not arrive")
			return ext.EditorOpenParams{}
		}
	}
}

// waitEnded は、デバッグの状態が消える（すべてのセッションが終わる）まで待つ。
func (h *host) waitEnded() {
	h.t.Helper()
	h.waitSessions(0)
}

// waitSessions は、動いているセッションの数が n になるまで待ち、セッションを始めた順に返す。
func (h *host) waitSessions(n int) []*session {
	h.t.Helper()
	deadline := time.Now().Add(testTimeout)
	var got int
	for time.Now().Before(deadline) {
		h.d.mu.Lock()
		ss := slices.Clone(h.d.sessions)
		h.d.mu.Unlock()
		if got = len(ss); got == n {
			return ss
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("sessions = %d, want %d", got, n)
	return nil
}

// fakeSettings は、テスト用のデバッグアダプターを使う設定を返す。fake は launch の引数の fake の項目である。
func fakeSettings(t *testing.T, args []string, fake map[string]any) string {
	t.Helper()
	s := map[string]any{
		"adapter": map[string]any{"command": fakeAdapter, "args": args},
		"launch":  map[string]any{"fake": fake},
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	srcPath = "/src/app/main.go"
	srcText = "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(1)\n\tfmt.Println(2)\n\tfmt.Println(3)\n}"
)

var (
	bp2   = ext.Sign{Line: 2, Kind: ext.SignBreakpoint}
	bp5   = ext.Sign{Line: 5, Kind: ext.SignBreakpoint}
	exec2 = ext.Sign{Line: 2, Kind: ext.SignExecutionPoint}
	exec5 = ext.Sign{Line: 5, Kind: ext.SignExecutionPoint}
	exec6 = ext.Sign{Line: 6, Kind: ext.SignExecutionPoint}
)

// TestToggleBreakpointBeforeStart は、デバッグを始める前のブレークポイントの切り替えで、breakpoint の印が付いて消えることを確かめる。
//
// req: FR-31
func TestToggleBreakpointBeforeStart(t *testing.T) {
	h := newHost(t, "")
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 5)
	h.waitSigns(bp5)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.waitSigns(bp2, bp5)
	h.mustExec(cmdToggleBreakpoint, 5)
	h.waitSigns(bp2)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.waitSigns()

	// ファイルに対応しないバッファには置けない。
	h.open(2, "", "scratch")
	if res := h.exec(2, cmdToggleBreakpoint, 0); res.OK {
		t.Error("toggle on a buffer without a path: ok = true, want false")
	}
	h.waitMessage("ブレークポイントを置けない")
}

// TestCommandArgs は、breakpoint.conditional を除くどの操作も引数を取らず、コマンドラインから引数を付けて呼ぶと（args の text）、
// 何もせずに ok を false で返して知らせることを確かめる。空白だけの text と、キーで取った文字（char）は引数として扱わない。
func TestCommandArgs(t *testing.T) {
	h := newHost(t, "")
	h.open(1, srcPath, srcText)
	call := func(op ext.Operation, args string) ext.ProviderInvokeResult {
		t.Helper()
		return h.invoke(1, op, 5, args)
	}
	for _, op := range providedOps {
		if op == ext.OpBreakpointConditional {
			continue
		}
		if res := call(op, `{"text":"foo"}`); res.OK {
			t.Errorf("%s foo: ok = true", op)
		}
		h.waitMessage(string(op) + ": 引数を取らない")
	}
	if res := call(cmdToggleBreakpoint, `{"text":" "}`); !res.OK {
		t.Error("空白だけの text: ok = false")
	}
	h.waitSigns(bp5)
	if res := call(cmdToggleBreakpoint, `{"char":"x"}`); !res.OK {
		t.Error("char: ok = false")
	}
	h.waitSigns()
}

// TestDebugSession は、初期化、ブレークポイント、停止の印とカーソルの移動、デバッグ対象の出力、続行、ステップ実行、終了を通しで確かめる。
//
// req: FR-31
func TestDebugSession(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, nil))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdToggleBreakpoint, 5)
	h.waitSigns(bp2, bp5)

	// 始める前のステップ実行は失敗する。続行（debug.continue）は始める。
	if res := h.exec(1, cmdNext, 0); res.OK {
		t.Error("step before start: ok = true, want false")
	}

	h.mustExec(cmdStart, 0)
	h.waitMessage("デバッグを開始した")
	h.waitSigns(bp2, bp5, exec2)
	if p := h.waitOpen(); p.Buffer == nil || *p.Buffer != 1 || p.Position.Line != 2 || p.Position.Byte != 0 || !p.Focus {
		t.Errorf("editor/open = %+v, want buffer 1 at 2:0 with focus", p)
	}
	h.waitMessage("停止した（breakpoint）")
	out := strings.Join(h.waitOutput("hello from debuggee"), "")
	if !strings.Contains(out, "launch /src/app") {
		t.Errorf("program = dir of the buffer is not launched; output = %q", out)
	}
	if strings.Contains(out, "telemetry") {
		t.Errorf("telemetry output was shown: %q", out)
	}
	if res := h.exec(1, ext.OpDebugRunLast, 0); res.OK {
		t.Error("runLast while debugging: ok = true, want false")
	}

	h.mustExec(cmdContinue, 0)
	h.waitSigns(bp2, bp5, exec5)
	h.mustExec(cmdNext, 0)
	h.waitSigns(bp2, bp5, exec6)
	h.mustExec(cmdStepIn, 0)
	h.waitSigns(bp2, bp5, exec6)
	h.mustExec(cmdContinue, 0)
	h.waitMessage("終了コード 0")
	h.waitSigns(bp2, bp5)
	h.waitMessage("デバッグを終了した")
	h.waitEnded()

	// 終わった後のステップ実行は失敗する。
	if res := h.exec(1, cmdNext, 0); res.OK {
		t.Error("step after the end: ok = true, want false")
	}
}

// TestBreakpointVerification は、setBreakpoints の応答の verified と実際の行で印を付け直すことを確かめる。
//
// req: FR-31
func TestBreakpointVerification(t *testing.T) {
	// DAP の行 6（本体の行 5）を検証できなかったことにする。
	h := newHost(t, fakeSettings(t, nil, map[string]any{"unverified": []int{6}}))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdToggleBreakpoint, 5)
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, ext.Sign{Line: 5, Kind: ext.SignBreakpointUnverified}, exec2)

	// デバッグ中の切り替えは、すぐに setBreakpoints で送る。
	h.mustExec(cmdToggleBreakpoint, 7)
	h.waitOutput("setBreakpoints main.go [3 6 8]")
	h.waitSigns(bp2, ext.Sign{Line: 5, Kind: ext.SignBreakpointUnverified}, ext.Sign{Line: 7, Kind: ext.SignBreakpoint}, exec2)

	// 終わったら、検証の結果を忘れて breakpoint の印に戻す。
	h.mustExec(cmdStop, 0)
	h.waitMessage("デバッグを終了した")
	h.waitSigns(bp2, bp5, ext.Sign{Line: 7, Kind: ext.SignBreakpoint})
}

// TestBreakpointMovedByAdapter は、デバッグアダプターがブレークポイントを別の行に置いたとき、その行へ印を移すことを確かめる。
//
// req: FR-31
func TestBreakpointMovedByAdapter(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"moveBy": 1}))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	h.waitSigns(ext.Sign{Line: 3, Kind: ext.SignBreakpoint}, ext.Sign{Line: 3, Kind: ext.SignExecutionPoint})
}

// TestFollowEditsAndResendOnSave は、編集でブレークポイントの行をずらし、保存したときに setBreakpoints を送り直すことを確かめる。
//
// req: FR-31
func TestFollowEditsAndResendOnSave(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, nil))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 5)
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp5, exec5)
	h.waitOutput("setBreakpoints main.go [6]")

	// 行 0 の前に 2 行足す。保存するまで送り直さない。
	h.change(1, ext.Edit{Range: ext.Range{}, Text: "// a\n// b\n"})
	h.didSave(1, srcPath)
	h.waitOutput("setBreakpoints main.go [8]")
	h.waitSigns(ext.Sign{Line: 7, Kind: ext.SignBreakpoint}, ext.Sign{Line: 7, Kind: ext.SignExecutionPoint})
}

// TestAdapterCrash は、デバッグアダプターが異常終了したとき、利用者に知らせ、実行位置の印を消し、続行を失敗させることを確かめる。
//
// req: FR-31
func TestAdapterCrash(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"crashOn": "continue"}))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, exec2)
	h.mustExec(cmdContinue, 0)
	if m := h.waitMessage("デバッグアダプターが異常終了した"); m.Level != ext.MessageError {
		t.Errorf("level = %q, want error", m.Level)
	}
	h.waitSigns(bp2)
	h.waitEnded()
	if res := h.exec(1, cmdNext, 0); res.OK {
		t.Error("step after the crash: ok = true, want false")
	}
	// もう一度始められる。
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, exec2)
}

// TestProviderOperations は、段階 3 で足した DAP の操作（条件付きのブレークポイント、すべて消す、前回の設定で実行、再起動、一時停止）を確かめる。
//
// req: FR-94
func TestProviderOperations(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, nil))
	h.open(1, srcPath, srcText)

	// 前回が無ければ runLast は失敗する。条件の無い breakpoint.conditional も失敗する。
	if res := h.exec(1, ext.OpDebugRunLast, 0); res.OK {
		t.Error("runLast without a previous run: ok = true")
	}
	h.waitMessage("前回のデバッグが無い")
	if res := h.exec(1, ext.OpBreakpointConditional, 5); res.OK {
		t.Error("conditional without a condition: ok = true")
	}
	h.waitMessage(":breakpoint.conditional")

	// 条件付きのブレークポイントは、setBreakpoints の condition で送る。
	if res := h.invoke(1, ext.OpBreakpointConditional, 5, `{"text":"i > 3"}`); !res.OK {
		t.Fatal("conditional: ok = false")
	}
	h.mustExec(cmdToggleBreakpoint, 2)
	h.waitSigns(bp2, bp5)
	h.mustExec(cmdStart, 0)
	h.waitOutput(`condition 6 "i > 3"`)
	h.waitSigns(bp2, bp5, exec2)

	// 一時停止は threads の後に pause を送る。
	h.mustExec(ext.OpDebugPause, 0)
	h.waitOutput("pause")

	// すべて消すと、印を消し、空の setBreakpoints を送る。
	h.mustExec(ext.OpBreakpointClear, 0)
	h.waitSigns(exec2)
	h.waitOutput("setBreakpoints main.go []")

	// 再起動は、終えてから前回の設定で始め直す。
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(ext.OpDebugRestart, 0)
	h.waitMessage("デバッグを終了した")
	h.waitMessage("デバッグを開始した")
	h.waitSigns(bp2, exec2)

	// 終えた後の runLast は、前回の設定で始める。
	h.mustExec(cmdStop, 0)
	h.waitEnded()
	h.mustExec(ext.OpDebugRunLast, 0)
	h.waitSigns(bp2, exec2)
}

// TestReplayWaitsForStop は、マクロの再生中（sync）のステップ実行が、デバッグ対象が止まるまで応答せず、
// 止まった位置を応答の open で返すことを確かめる（#210）。
// <Space>dn を 2 回記録したマクロの再生は、本体から sync の debug.stepOver が、前の応答を待って 2 回届く形になる。
//
// テスト用のデバッグアダプターは、ステップ実行の応答から 200 ms の間デバッグ対象が動いていることにし、その間のステップ実行を断る。
// 再生中でない呼び出しは受け付けたことだけを応答するので、続けて 2 回呼ぶと 2 回目が断られる（#210 の症状）。
//
// req: FR-19, FR-94
func TestReplayWaitsForStop(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"stopDelayMs": 200}))
	h.open(1, srcPath, srcText)
	h.prime()
	if res := h.replay(cmdToggleBreakpoint, 2); !res.OK || res.Open != nil {
		t.Fatalf("toggle (sync) = %+v, want ok without open", res)
	}
	h.waitSigns(bp2)

	// 始める操作は、最初に止まるまで待ち、止まった位置を返す。
	res := h.replay(cmdStart, 0)
	if !res.OK || res.Open == nil || res.Open.Buffer == nil || *res.Open.Buffer != 1 || res.Open.Position.Line != 2 || !res.Open.Focus {
		t.Fatalf("start (sync) = %+v, want ok with open at buffer 1 line 2", res)
	}
	h.noOpen("start (sync)")
	h.waitSigns(bp2, exec2)

	// 再生中でなければ、2 回目は動いている間に届いて断られる。
	h.mustExec(cmdNext, 0)
	h.exec(1, cmdNext, 0)
	h.waitMessage("debuggee is running")
	h.waitSigns(bp2, ext.Sign{Line: 3, Kind: ext.SignExecutionPoint})
	h.waitOpen()
	h.drainOpens()

	// 再生中は、止まるまで待つので 2 回とも進む。
	for _, want := range []int{4, 5} {
		start := time.Now()
		res := h.replay(cmdNext, 0)
		if !res.OK || res.Open == nil || res.Open.Position == nil || res.Open.Position.Line != want {
			t.Fatalf("next (sync) = %+v, want ok with open at line %d", res, want)
		}
		if d := time.Since(start); d < 150*time.Millisecond {
			t.Errorf("next (sync) replied after %v, before the debuggee stopped", d)
		}
	}
	h.noOpen("next (sync)")
	h.waitSigns(bp2, exec5)

	// 既に止まっていれば、一時停止はすぐに応答する。
	if res := h.replay(ext.OpDebugPause, 0); !res.OK || res.Open != nil {
		t.Errorf("pause while stopped (sync) = %+v, want ok without open", res)
	}

	// 止まらずに終われば、カーソルを動かさずに応答する。
	if res := h.replay(cmdContinue, 0); !res.OK || res.Open != nil {
		t.Errorf("continue to the end (sync) = %+v, want ok without open", res)
	}
	h.waitEnded()

	// 終えた後の runLast は最初の停止まで待ち、終える操作は終え終わってから応答する。
	if res := h.replay(ext.OpDebugRunLast, 0); !res.OK || res.Open == nil || res.Open.Position.Line != 2 {
		t.Fatalf("runLast (sync) = %+v, want ok with open at line 2", res)
	}
	if res := h.replay(cmdStop, 0); !res.OK {
		t.Errorf("terminate (sync) = %+v, want ok", res)
	}
	h.d.mu.Lock()
	ended := h.d.rootLocked() == nil
	h.d.mu.Unlock()
	if !ended {
		t.Error("terminate (sync) replied before the session ended")
	}
	h.noOpen("runLast (sync)")
}

// TestReplayStepFailure は、再生中のステップ実行が失敗したら、止まるのを待たずに ok を false で返す（本体が再生を止める）ことを確かめる。
//
// req: FR-19
func TestReplayStepFailure(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"ignore": []string{"stepOut"}}))
	h.d.timeouts.request = 300 * time.Millisecond
	h.open(1, srcPath, srcText)
	h.prime()
	h.mustExec(cmdToggleBreakpoint, 2)
	if res := h.replay(cmdStart, 0); !res.OK {
		t.Fatalf("start (sync) = %+v", res)
	}
	if res := h.replay(cmdStepOut, 0); res.OK {
		t.Errorf("stepOut without a response (sync) = %+v, want ok = false", res)
	}
	// 始める前の操作は待たずに失敗する。
	h2 := newHost(t, fakeSettings(t, nil, nil))
	h2.open(1, srcPath, srcText)
	if res := h2.replay(cmdNext, 0); res.OK {
		t.Errorf("next before start (sync) = %+v, want ok = false", res)
	}
}

// TestReplayStopsOnException は、再生中の続行で、デバッグ対象がブレークポイントとステップ実行以外の理由（exception）で止まったら、
// ok を false で返して再生を止め、止まった位置へは editor/open で移すことを確かめる（仮。#217 のレビュー）。
//
// req: FR-19
func TestReplayStopsOnException(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"exceptionOnContinue": true}))
	h.open(1, srcPath, srcText)
	h.prime()
	h.mustExec(cmdToggleBreakpoint, 2)
	if res := h.replay(cmdStart, 0); !res.OK {
		t.Fatalf("start (sync) = %+v", res)
	}
	if res := h.replay(cmdContinue, 0); res.OK {
		t.Errorf("continue stopped by an exception (sync) = %+v, want ok = false", res)
	}
	h.waitMessage("再生を止めた")
	if p := h.waitOpen(); p.Buffer == nil || *p.Buffer != 1 || p.Position.Line != 2 {
		t.Errorf("editor/open = %+v, want buffer 1 line 2", p)
	}
}

// TestReplayStopsOnExceptionWithoutSource は、止まった位置のソースが分からない停止でも、ブレークポイントとステップ実行以外の理由なら
// ok を false で返して再生を止めることを確かめる（#224 のコメント）。ブレークポイントで止まったときは、カーソルを動かさずに続ける。
//
// req: FR-19
func TestReplayStopsOnExceptionWithoutSource(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"exceptionOnContinue": true, "noSource": true}))
	h.open(1, srcPath, srcText)
	h.prime()
	h.mustExec(cmdToggleBreakpoint, 2)
	if res := h.replay(cmdStart, 0); !res.OK || res.Open != nil {
		t.Fatalf("start (sync) = %+v, want ok without open", res)
	}
	if res := h.replay(cmdContinue, 0); res.OK {
		t.Errorf("continue stopped by an exception without source (sync) = %+v, want ok = false", res)
	}
	h.waitMessage("再生を止めた")
}

// TestReplayStopInUnopenedFile は、再生中の停止の位置が開いていないファイルにあれば、ファイルの中身から列をバイトの位置にして
// パスで返すことを確かめる（#217 のレビュー）。
//
// req: FR-19
func TestReplayStopInUnopenedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(path, []byte("x\na𝄞b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// DAP の列 4（1 から数える）は、"a𝄞b" の b の前（5 バイト目）。
	h := newHost(t, fakeSettings(t, nil, map[string]any{"column": 4}))
	h.open(2, path, "x\na𝄞b")
	if res := h.invoke(2, cmdToggleBreakpoint, 1, ""); !res.OK {
		t.Fatal("toggle: ok = false")
	}
	if err := h.conn.Notify(ext.MethodBufferClose, ext.BufferCloseParams{Buffer: 2}); err != nil {
		t.Fatal(err)
	}
	h.open(1, srcPath, srcText)
	h.prime()
	res := h.replay(cmdStart, 0)
	if !res.OK || res.Open == nil || res.Open.Path != path || res.Open.Buffer != nil || res.Open.Position == nil || *res.Open.Position != (ext.Position{Line: 1, Byte: 5}) {
		t.Errorf("start (sync) = %+v, want open %s at 1:5", res, path)
	}
}

// waiter は、本体が待つのをやめた後の結果を届けず、やめる前に届いていた結果を返す。
func TestWaiterAbandon(t *testing.T) {
	w := newWaiter()
	want := ext.ProviderInvokeResult{OK: true}
	if !w.resolve(want) {
		t.Error("resolve before abandon = false")
	}
	if r, ok := w.abandon(); !ok || r.OK != want.OK {
		t.Errorf("abandon after resolve = %+v, %v", r, ok)
	}
	w = newWaiter()
	if _, ok := w.abandon(); ok {
		t.Error("abandon before resolve returned a result")
	}
	if w.resolve(want) {
		t.Error("resolve after abandon = true")
	}
}

// TestManifest は、宣言ファイルが検査を通り、provides が引き受ける操作と一致することを確かめる。
func TestManifest(t *testing.T) {
	data, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m ext.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if warnings, verr := m.Validate(); verr != nil || len(warnings) > 0 {
		t.Fatalf("Validate = %v, %v", warnings, verr)
	}
	if !slices.Equal(m.Provides, providedOps) || len(m.Contributes.Commands) != 0 || len(m.Activation) != 0 {
		t.Errorf("manifest = %+v, want provides %v only", m, providedOps)
	}
	// 区画の中のキーは、pane.go が view/key で受ける ID と、効かせる区画が合う（子を切り替える 2 つは区画を限らない）。
	want := map[string][]string{
		keyScopesParent: {secScopes}, keyScopesSetValue: {secScopes},
		keyThreadsSubtle: {secThreads}, keyThreadsFilter: {secThreads}, keyThreadsInvert: {secThreads}, keyThreadsSelect: {secThreads},
		keyBreakpointsDel: {secBreakpoints}, keyConsoleNext: {secConsole}, keyConsolePrev: {secConsole},
		keyWatchesParent: {secWatches}, keyWatchesAppend: {secWatches}, keyWatchesInsert: {secWatches}, keyWatchesEdit: {secWatches},
		keyWatchesDelete: {secWatches}, keyWatchesSet: {secWatches}, keyWatchesCopy: {secWatches},
		"dap.view.next": nil, "dap.view.prev": nil,
	}
	got := map[string][]string{}
	for _, k := range m.Contributes.PopupKeys {
		got[k.ID] = k.Children
	}
	if mustJSON(t, got) != mustJSON(t, want) {
		t.Errorf("popupKeys = %v, want %v", got, want)
	}
}

// TestLaunchFailure は、launch が失敗したとき、理由を知らせてデバッグアダプターを終わらせることを確かめる。
//
// req: FR-31
func TestLaunchFailure(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"failLaunch": true}))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdStart, 0)
	if m := h.waitMessage("デバッグを開始できない"); !strings.Contains(m.Text, "cannot build /src/app") || m.Level != ext.MessageError {
		t.Errorf("message = %+v", m)
	}
	h.waitEnded()
}

// TestAdapterNotFound は、デバッグアダプターを起動できなければ、コマンドが失敗することを確かめる。
//
// req: FR-31
func TestAdapterNotFound(t *testing.T) {
	h := newHost(t, `{"adapter": {"command": "/nonexistent/adapter"}}`)
	h.open(1, srcPath, srcText)
	// 構成を決める間は待たせないので、開始は受け付けてから、起動できないことを知らせる。
	h.mustExec(cmdStart, 0)
	if m := h.waitMessage("起動できない"); m.Level != ext.MessageError {
		t.Errorf("message = %+v, want an error", m)
	}
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	if h.d.rootLocked() != nil || h.d.preparing != nil {
		t.Error("a session remains after the adapter failed to start")
	}
}

// prime は、前回の構成（debug.runLast と再生の開始が使う）を、srcPath のバッファで始めたときの構成にする。
// 再生中（sync）の開始は一覧も問いも出さずに前回の構成で始める（FR-280）ので、再生のテストの前に呼ぶ。
func (h *host) prime() {
	h.t.Helper()
	spec, err := h.d.prepare(context.Background(), invocation{file: srcPath, line: 1, column: 1})
	if err != nil {
		h.t.Fatalf("prepare: %v", err)
	}
	h.d.mu.Lock()
	h.d.last = spec
	h.d.mu.Unlock()
}

// TestStop は、dap.stop で disconnect を送り、デバッグアダプターが終わって状態が消えることを確かめる。
//
// req: FR-31
func TestStop(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, nil))
	h.open(1, srcPath, srcText)
	if res := h.exec(1, cmdStop, 0); res.OK {
		t.Error("stop before start: ok = true, want false")
	}
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, exec2)
	h.d.mu.Lock()
	s := h.d.rootLocked()
	h.d.mu.Unlock()
	h.mustExec(cmdStop, 0)
	h.waitSigns(bp2)
	h.waitMessage("デバッグを終了した")
	h.waitEnded()
	select {
	case <-s.procDone():
	case <-time.After(testTimeout):
		t.Fatal("adapter process did not exit")
	}
	if !s.getProc().cmd.ProcessState.Success() {
		t.Errorf("adapter exit = %v, want success (exited by disconnect, not killed)", s.getProc().cmd.ProcessState)
	}
}

// TestStopWithTerminate は、デバッグアダプターが supportsTerminateRequest を持てば、dap.stop で先に terminate を送り、
// その後の terminated の event で disconnect を二重に送らずに終わることを確かめる。
//
// req: FR-31
func TestStopWithTerminate(t *testing.T) {
	h := newHost(t, fakeSettings(t, []string{"-terminate"}, nil))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, exec2)
	h.d.mu.Lock()
	s := h.d.rootLocked()
	h.d.mu.Unlock()
	h.mustExec(cmdStop, 0)
	h.waitOutput("terminate requested")
	h.waitMessage("デバッグを終了した")
	h.waitEnded()
	if !s.getProc().cmd.ProcessState.Success() {
		t.Errorf("adapter exit = %v, want success (exited by disconnect)", s.getProc().cmd.ProcessState)
	}
}

// TestSocketTransport は、引数に ${clientAddr} があるとき、ソケットでデバッグアダプターと話し、
// デバッグアダプターの標準出力を出力の欄へ書き足すことを確かめる（dlv dap --client-addr の形）。
//
// req: FR-31
func TestSocketTransport(t *testing.T) {
	h := newHost(t, fakeSettings(t, []string{"-client-addr=" + clientAddrPlaceholder}, nil))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, exec2)
	h.waitOutput("fakeadapter stdout")
	h.d.mu.Lock()
	dir := h.d.rootLocked().sockDir
	h.d.mu.Unlock()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket dir %s remains after accept: %v", dir, err)
	}
}

// TestAdapterCwd は、デバッグアダプターの作業ディレクトリを、dap.start を呼んだバッファのファイルのあるディレクトリにすることを確かめる。
// dlv dap は program を自分の作業ディレクトリでビルドするので、本体の作業ディレクトリがモジュールの外だと失敗する。
//
// req: FR-31
func TestAdapterCwd(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newHost(t, fakeSettings(t, nil, nil))
	h.open(1, filepath.Join(dir, "main.go"), srcText)
	h.mustExec(cmdStart, 0)
	h.waitOutput("cwd " + dir + "\n")
}

// TestReverseRequest は、対応しない逆向きの request（runInTerminal）に失敗の応答を返し、デバッグを続けられることを確かめる。
func TestReverseRequest(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"reverse": true}))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	h.waitOutput("runInTerminal success=false")
	h.waitSigns(bp2, exec2)
}

// TestStoppedWithoutThreadID は、stopped の event が threadId を持たないとき、threads で問い合わせたスレッドで続けることを確かめる。
//
// req: FR-31
func TestStoppedWithoutThreadID(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, map[string]any{"noThreadId": true, "launchBeforeInitialized": true}))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdToggleBreakpoint, 5)
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, bp5, exec2)
	h.mustExec(cmdContinue, 0)
	h.waitSigns(bp2, bp5, exec5)
}

// TestStoppedColumn は、停止位置の列（UTF-16 のコード単位）を、本体の位置（バイト数）へ変換してカーソルを移すことを確かめる。
//
// req: FR-31
func TestStoppedColumn(t *testing.T) {
	// DAP の列 4（1 から数える）は、"a𝄞b" の b の前。𝄞 は UTF-16 で 2 単位、UTF-8 で 4 バイト。
	h := newHost(t, fakeSettings(t, nil, map[string]any{"column": 4}))
	h.open(1, srcPath, "x\na𝄞b")
	h.mustExec(cmdToggleBreakpoint, 1)
	h.mustExec(cmdStart, 0)
	if p := h.waitOpen(); p.Position.Line != 1 || p.Position.Byte != 5 {
		t.Errorf("cursor = %+v, want 1:5", *p.Position)
	}
}

func TestShiftLine(t *testing.T) {
	pos := func(l, b int) ext.Position { return ext.Position{Line: l, Byte: b} }
	edit := func(s, e ext.Position, text string) ext.Edit {
		return ext.Edit{Range: ext.Range{Start: s, End: e}, Text: text}
	}
	tests := []struct {
		name string
		line int
		e    ext.Edit
		want int
	}{
		{"insert lines above", 5, edit(pos(0, 0), pos(0, 0), "a\nb\n"), 7},
		{"insert at the line head moves the line", 5, edit(pos(5, 0), pos(5, 0), "a\n"), 6},
		{"insert inside the line keeps it", 5, edit(pos(5, 3), pos(5, 3), "a\n"), 5},
		{"insert below keeps it", 5, edit(pos(6, 0), pos(6, 0), "a\n"), 5},
		{"delete lines above", 5, edit(pos(1, 0), pos(3, 0), ""), 3},
		{"delete the line itself", 5, edit(pos(5, 0), pos(6, 0), ""), 5},
		{"delete over the line", 5, edit(pos(4, 2), pos(6, 1), ""), 4},
		{"join with the previous line", 5, edit(pos(4, 3), pos(5, 0), ""), 4},
		{"replace in the line", 5, edit(pos(5, 0), pos(5, 4), "x"), 5},
	}
	for _, tt := range tests {
		if got := shiftLine(tt.line, tt.e); got != tt.want {
			t.Errorf("%s: shiftLine(%d) = %d, want %d", tt.name, tt.line, got, tt.want)
		}
	}
}

func TestShiftBreakpointsMerges(t *testing.T) {
	bps := []bp{{line: 4, state: bpVerified}, {line: 5, state: bpUnverified}}
	got := shiftBreakpoints(bps, []ext.Edit{{Range: ext.Range{Start: ext.Position{Line: 4}, End: ext.Position{Line: 5}}}})
	want := []bp{{line: 4, state: bpVerified}}
	if !slices.Equal(got, want) {
		t.Errorf("shiftBreakpoints = %v, want %v", got, want)
	}
	if bps[1].line != 5 {
		t.Error("shiftBreakpoints changed its input")
	}
}

func TestUTF16ColumnToByte(t *testing.T) {
	tests := []struct {
		text     string
		column   int
		startAt1 bool
		want     int
	}{
		{"abc", 1, true, 0},
		{"abc", 3, true, 2},
		{"abc", 4, true, 3},
		{"abc", 9, true, 3},
		{"abc", 0, true, 0},
		{"abc", 2, false, 2},
		{"あい", 1, false, 3},
		{"a𝄞b", 2, false, 1},   // サロゲートペアの間は文字の先頭へ寄せる
		{"a𝄞b", 3, false, 5},   // 𝄞 は 2 単位
		{"\xffb", 1, false, 1}, // 不正なバイトは 1 単位
	}
	for _, tt := range tests {
		if got := utf16ColumnToByte(tt.text, tt.column, tt.startAt1); got != tt.want {
			t.Errorf("utf16ColumnToByte(%q, %d, %v) = %d, want %d", tt.text, tt.column, tt.startAt1, got, tt.want)
		}
	}
}

func TestLineConversion(t *testing.T) {
	if got := toDAPLine(0, true); got != 1 {
		t.Errorf("toDAPLine(0, true) = %d", got)
	}
	if got := toDAPLine(3, false); got != 3 {
		t.Errorf("toDAPLine(3, false) = %d", got)
	}
	if got := fromDAPLine(1, true); got != 0 {
		t.Errorf("fromDAPLine(1, true) = %d", got)
	}
	if got := fromDAPLine(0, true); got != 0 {
		t.Errorf("fromDAPLine(0, true) = %d, want clamped 0", got)
	}
	if got := fromDAPLine(4, false); got != 4 {
		t.Errorf("fromDAPLine(4, false) = %d", got)
	}
}

// TestDAPConnErrorResponse は、失敗の response の body.error.format を誤りの文に使い、接続が終わったら待っている call が戻ることを確かめる。
func TestDAPConnErrorResponse(t *testing.T) {
	adR, clW := io.Pipe()
	clR, adW := io.Pipe()
	c := newDAPConn(clR, clW, multiCloser{clR, clW}, nil, nil)
	defer c.close()
	go func() {
		r := bufio.NewReader(adR)
		for i := 0; ; i++ {
			body, err := rpc.ReadMessage(r, maxDAPMessageSize)
			if err != nil {
				return
			}
			var m dapMessage
			_ = json.Unmarshal(body, &m)
			if i == 0 {
				resp, _ := json.Marshal(map[string]any{
					"seq": 1, "type": "response", "request_seq": m.Seq, "command": m.Command, "success": false,
					"message": "x", "body": map[string]any{"error": map[string]any{"id": 7, "format": "no such program"}},
				})
				_ = rpc.WriteMessage(adW, resp)
				continue
			}
			// 2 つ目の request には答えずに切る。
			_ = adW.Close()
			return
		}
	}()
	err := c.call(context.Background(), "launch", nil, nil)
	var de *dapError
	if !errors.As(err, &de) || !strings.Contains(err.Error(), "no such program") {
		t.Errorf("call = %v, want dapError with the format", err)
	}
	err = c.call(context.Background(), "threads", nil, nil)
	if !errors.Is(err, errDAPClosed) {
		t.Errorf("call after the adapter closed = %v, want errDAPClosed", err)
	}
}
