// Command fakeadapter は、vi-imitation-ext-dap のテストで使う、DAP を話すデバッグアダプターの代役である。
//
// 実際のプログラムは動かさず、受け取ったブレークポイントの位置で順に止まったことにする。
// 既定では標準入出力で話し、-client-addr unix:<path> を渡すと、そのソケットへ自分から接続して話す（dlv dap の --client-addr と同じ形）。
//
// 振る舞いは launch の引数の fake で変える（fakeOptions）。
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// fakeOptions は、launch の引数の fake の項目である。
type fakeOptions struct {
	// Unverified は、検証できなかったことにする行（DAP の行）である。
	Unverified []int `json:"unverified"`
	// MoveBy は、ブレークポイントを実際に置く行を、求められた行からずらす行数である。
	MoveBy int `json:"moveBy"`
	// CrashOn は、この request を受け取ったら応答せずに終了するコマンドの名前である。
	CrashOn string `json:"crashOn"`
	// FailLaunch は、launch に失敗の応答を返す。
	FailLaunch bool `json:"failLaunch"`
	// Reverse は、configurationDone の後に、逆向きの request の runInTerminal を送る。
	Reverse bool `json:"reverse"`
	// NoThreadID は、stopped の event の threadId を省く。
	NoThreadID bool `json:"noThreadId"`
	// Column は、stackTrace のフレームの列である。0 なら 1。
	Column int `json:"column"`
	// Child は、launch で子プロセス（デバッグ対象の代役）を別のプロセスグループで起動し、その pid を出力する。
	// SIGTERM を受け取ったら、子プロセスを終わらせてから終了する（dlv と同じ）。SIGKILL では子プロセスが残る。
	Child bool `json:"child"`
	// LaunchBeforeInitialized は、launch の応答を initialized の event より先に返す。
	LaunchBeforeInitialized bool `json:"launchBeforeInitialized"`
	// Ignore は、受け取っても応答も処理もしない request のコマンドの名前である（止まったデバッグアダプターの代役）。
	Ignore []string `json:"ignore"`
	// StopDelayMs は、continue、next、stepIn、stepOut の応答から stopped の event までの時間（ミリ秒）である。
	// 0 でなければ、その間はデバッグ対象が動いていることにし、続行とステップ実行の request に失敗を返す（delve と同じ。#210）。
	StopDelayMs int `json:"stopDelayMs"`
	// ExceptionOnContinue は、continue で次のブレークポイントへ進まずに、その場で reason が exception の stopped を送る（panic の代役）。
	ExceptionOnContinue bool `json:"exceptionOnContinue"`
	// NoSource は、stackTrace のフレームの source を省く（止まった位置のソースが分からない停止の代役）。
	NoSource bool `json:"noSource"`
	// OrphanOnTerm は、SIGTERM で子プロセスを止めずに終わる（vscode-js-debug の dapDebugServer.js の代役）。
	OrphanOnTerm bool `json:"orphanOnTerm"`
	// StartDebugging は、configurationDone の後に、逆向きの request の startDebugging を送る（vscode-js-debug の子のセッションの代役）。
	// 送った後は、ParentRuns でなければブレークポイントで止まらない（デバッグ対象は子のセッションにある）。
	StartDebugging bool `json:"startDebugging"`
	// ChildRequest は、startDebugging の request である。空なら、受けた launch か attach と同じ。
	ChildRequest string `json:"childRequest"`
	// ChildConfig は、startDebugging の configuration である。nil なら {"type":"fake","name":"child","__pendingTargetId":"t1"}。
	ChildConfig map[string]any `json:"childConfig"`
	// ChildListen は、子のセッションのために自分の中で TCP で待ち受け、configuration の connect に host と port を入れる
	// （debugpy の子のプロセスの代役）。
	ChildListen bool `json:"childListen"`
	// ParentRuns は、startDebugging を送った後も、自分のブレークポイントで止まる（debugpy の親のプロセスの代役）。
	ParentRuns bool `json:"parentRuns"`
	// VerifyLater は、setBreakpoints の応答では verified を false にして ID を振り、configurationDone の後に、
	// reason が changed の breakpoint の event で verified を true にする（vscode-js-debug の launch の代役）。
	VerifyLater bool `json:"verifyLater"`
	// NeverVerify は、setBreakpoints の応答で verified を false にして ID を振り、breakpoint の event を送らず、
	// 止まったときの stopped の hitBreakpointIds で知らせる（vscode-js-debug の attach の代役）。
	NeverVerify bool `json:"neverVerify"`
	// ThreadZero は、スレッドの ID を 0 にする（vscode-js-debug の代役）。
	ThreadZero bool `json:"threadZero"`
	// RunForever は、continue に応答した後、止まらずに動き続ける（終わりもしない）。"continued after <launch か attach>" を出す。
	RunForever bool `json:"runForever"`
	// FirstStopDelayMs は、最初のブレークポイントで止まるまでの時間（ミリ秒）である。
	FirstStopDelayMs int `json:"firstStopDelayMs"`
	// Idle は、configurationDone の後に止まらずに動き続ける（ブレークポイントで止まらない、終わらない）。
	Idle bool `json:"idle"`
	// NoThreads は、threads に空の列を返す（vscode-js-debug の親のセッションの代役）。
	NoThreads bool `json:"noThreads"`
	// StackTraceDelayMs は、最初の stackTrace の応答を遅らせる時間（ミリ秒）である。受け取ったら "stackTrace requested" を出す。
	StackTraceDelayMs int `json:"stackTraceDelayMs"`
	// VariablesDelayMs は、最初の variables（参照 1）の応答を遅らせる時間（ミリ秒）である。受け取ったら "variables requested" を出す。
	VariablesDelayMs int `json:"variablesDelayMs"`
	// ExtraOutput は、configurationDone の後に、stderr の CRLF の行、塊の境目で分かれた CRLF、\r だけの行、important の行を出す。
	ExtraOutput bool `json:"extraOutput"`
	// StaleAfterSet は、setVariable と setExpression の応答では新しい値を返し、その後の variables では前の値を返す
	// （vscode-js-debug 1.117.0 の setExpression の代役）。
	StaleAfterSet bool `json:"staleAfterSet"`
	// TwoThreads は、threads に止まっていない 2 つ目のスレッド（ID 2、worker）を足す。そのスタックは、ソースのファイルが無いフレーム 1 つである。
	// 続行とステップ実行は、止まったスレッド（ID 1）へ送られたものだけを受ける。
	TwoThreads bool `json:"twoThreads"`
	// CallerPath は、stackTrace の 3 つ目のフレーム（main.caller）のソースのパスである。空なら止まった位置のファイル。
	CallerPath string `json:"callerPath"`
	// ThreadEvents は、configurationDone の 500 ミリ秒後にスレッド 7 が始まった thread の event を、1 秒後に終わった event を送る。
	ThreadEvents bool `json:"threadEvents"`
	// TopSubtle は、stackTrace の先頭のフレームの presentationHint を subtle にする（一時停止で runtime.gopark に止まった dlv の代役）。
	TopSubtle bool `json:"topSubtle"`
	// ANSI は、configurationDone の後に、端末の色の並びを含む行を、並びの途中で塊を分けて出す。
	ANSI bool `json:"ansi"`
	// EvaluateDelayMs は、最初の evaluate（context が watch）の応答を遅らせる時間（ミリ秒）である。受け取ったら "evaluate requested" を出す。
	EvaluateDelayMs int `json:"evaluateDelayMs"`
}

type message struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	Command    string          `json:"command,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	RequestSeq int             `json:"request_seq,omitempty"`
	Success    bool            `json:"success"`
	Message    string          `json:"message,omitempty"`
	Body       any             `json:"body,omitempty"`
	Event      string          `json:"event,omitempty"`
}

type location struct {
	path string
	line int
}

type adapter struct {
	r  *bufio.Reader
	w  io.Writer
	mu sync.Mutex
	// seq は、送ったメッセージの番号である。
	seq int

	opts      fakeOptions
	launchSeq int
	// command は、受けた launch か attach である。
	command   string
	bps       map[string][]int
	terminate bool
	// delayed は、StackTraceDelayMs で stackTrace を遅らせ終えたことを表す。
	delayed bool
	// varDelayed は、VariablesDelayMs で variables を遅らせ終えたことを表す。stateMu で守る。
	varDelayed bool
	// evalDelayed は、EvaluateDelayMs で evaluate を遅らせ終えたことを表す。stateMu で守る。
	evalDelayed bool
	// nstop は、止まった回数である。overrides は、setVariable と setExpression で書き換えた値（名前から）で、止まるたびに捨てる。
	// どちらも stateMu で守る。
	nstop     int
	overrides map[string]string
	// bpIDs は、VerifyLater と NeverVerify で振ったブレークポイントの ID（置いた位置から）である。nextBPID は次に振る ID。
	bpIDs    map[location]int
	nextBPID int

	// stateMu は、stops、cur、running を守る。StopDelayMs の停止は別の goroutine で起きる。
	stateMu sync.Mutex
	stops   []location
	cur     int
	// running は、StopDelayMs の間、デバッグ対象が動いていることを表す。
	running bool

	childMu sync.Mutex
	child   *exec.Cmd

	// conn は、TCP で待ち受けたとき（-listen）の接続である。disconnect で、終わらずにこれを閉じる。標準入出力とソケットでは nil。
	conn net.Conn
}

// capFlags は、initialize の応答の capabilities のうち、コマンドラインで変えるものである。
type capFlags struct {
	noConditional bool
	restart       bool
	restartFail   bool
	noSetVariable bool
	setExpression bool
	// noExceptions は、例外のフィルターを示さない。noExceptionInfo は、exceptionInfo に対応しない。clipboard は、evaluate の clipboard に対応する。
	noExceptions    bool
	noExceptionInfo bool
	clipboard       bool
}

var flagCaps capFlags

func main() {
	clientAddr := flag.String("client-addr", "", "unix:<path> of the client socket")
	listen := flag.String("listen", "", "listen on this TCP address (host:port) and serve each connection (serverPort and server)")
	terminate := flag.Bool("terminate", false, "support the terminate request")
	sleep := flag.Bool("sleep", false, "sleep forever (the debuggee stand-in)")
	flag.BoolVar(&flagCaps.noConditional, "no-conditional", false, "do not support conditional breakpoints")
	flag.BoolVar(&flagCaps.restart, "restart", false, "support the restart request")
	flag.BoolVar(&flagCaps.restartFail, "restart-fail", false, "fail the restart after acknowledging it (as dlv does on a build error)")
	flag.BoolVar(&flagCaps.noSetVariable, "no-set-variable", false, "do not support setVariable")
	flag.BoolVar(&flagCaps.setExpression, "set-expression", false, "support setExpression")
	flag.BoolVar(&flagCaps.noExceptions, "no-exceptions", false, "do not show exception breakpoint filters")
	flag.BoolVar(&flagCaps.noExceptionInfo, "no-exception-info", false, "do not support exceptionInfo")
	flag.BoolVar(&flagCaps.clipboard, "clipboard", false, "support the clipboard context of evaluate")
	flag.Parse()
	if *sleep {
		select {}
	}
	if *listen != "" {
		serve(*listen, *terminate)
		return
	}
	var r io.Reader = os.Stdin
	var w io.Writer = os.Stdout
	if *clientAddr != "" {
		c, err := net.Dial("unix", strings.TrimPrefix(*clientAddr, "unix:"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		r, w = c, c
		fmt.Println("fakeadapter stdout")
	}
	fmt.Fprintln(os.Stderr, "fakeadapter started")
	a := &adapter{r: bufio.NewReader(r), w: w, bps: map[string][]int{}, bpIDs: map[location]int{}, cur: -1, terminate: *terminate}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	go func() {
		<-sigs
		a.stateMu.Lock()
		orphan := a.opts.OrphanOnTerm
		a.stateMu.Unlock()
		if !orphan {
			a.killChild()
			killAllServed()
		}
		os.Exit(0)
	}()
	for {
		m, err := a.read()
		if err != nil {
			// 接続が切れても自分では終わらず、シグナルを待つ。拡張機能が子プロセスを終わらせる経路（SIGTERM）を試すためである。
			select {}
		}
		a.handle(m)
	}
}

// serve は、addr で TCP の接続を待ち受け、接続ごとに DAP を話す（vscode-js-debug の dapDebugServer.js と、既に動いているアダプターの代役）。
// 待ち受けたら "listening <port>" を標準出力へ書く。disconnect を受けても終わらず、次の接続を待つ。SIGTERM で終わる。
func serve(addr string, terminate bool) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("listening %d\n", ln.Addr().(*net.TCPAddr).Port)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	go func() {
		<-sigs
		killAllServed()
		os.Exit(0)
	}()
	acceptLoop(ln, terminate)
	os.Exit(1)
}

var (
	servedMu sync.Mutex
	served   []*adapter
)

// killAllServed は、待ち受けて話したどの接続の子プロセスも終わらせる。
func killAllServed() {
	servedMu.Lock()
	defer servedMu.Unlock()
	for _, a := range served {
		a.killChild()
	}
}

// acceptLoop は、ln で接続を受け、接続ごとに DAP を話す。ln が閉じたら戻る。
func acceptLoop(ln net.Listener, terminate bool) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		a := &adapter{r: bufio.NewReader(c), w: c, bps: map[string][]int{}, bpIDs: map[location]int{}, cur: -1, terminate: terminate, conn: c}
		servedMu.Lock()
		served = append(served, a)
		servedMu.Unlock()
		go func() {
			for {
				m, err := a.read()
				if err != nil {
					_ = c.Close()
					return
				}
				a.handle(m)
			}
		}()
	}
}

func (a *adapter) read() (*message, error) {
	length := -1
	for {
		line, err := a.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			length, err = strconv.Atoi(v)
			if err != nil {
				return nil, err
			}
		}
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(a.r, buf); err != nil {
		return nil, err
	}
	var m message
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (a *adapter) send(m message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	m.Seq = a.seq
	body, _ := json.Marshal(m)
	fmt.Fprintf(a.w, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func (a *adapter) respond(req *message, body any) {
	a.send(message{Type: "response", RequestSeq: req.Seq, Command: req.Command, Success: true, Body: body})
}

func (a *adapter) respondError(req *message, text string) {
	a.send(message{
		Type: "response", RequestSeq: req.Seq, Command: req.Command, Success: false, Message: "failed",
		Body: map[string]any{"error": map[string]any{"id": 1, "format": text}},
	})
}

func (a *adapter) event(name string, body any) {
	a.send(message{Type: "event", Event: name, Body: body})
}

func (a *adapter) output(category, text string) {
	a.event("output", map[string]any{"category": category, "output": text})
}

func (a *adapter) handle(m *message) {
	if m.Type == "response" {
		// 逆向きの request への応答である。
		if m.Command == "startDebugging" {
			a.output("console", fmt.Sprintf("startDebugging success=%v %s\n", m.Success, m.Message))
			return
		}
		a.output("console", fmt.Sprintf("runInTerminal success=%v\n", m.Success))
		a.run()
		return
	}
	if m.Type != "request" {
		return
	}
	if a.opts.CrashOn == m.Command {
		os.Exit(3)
	}
	if slices.Contains(a.opts.Ignore, m.Command) {
		return
	}
	switch m.Command {
	case "initialize":
		var args struct {
			LinesStartAt1                 bool `json:"linesStartAt1"`
			SupportsStartDebuggingRequest bool `json:"supportsStartDebuggingRequest"`
		}
		_ = json.Unmarshal(m.Arguments, &args)
		if !args.LinesStartAt1 {
			a.respondError(m, "this fake requires linesStartAt1")
			return
		}
		caps := map[string]any{
			"supportsConfigurationDoneRequest": true, "supportsTerminateRequest": a.terminate,
			"supportsConditionalBreakpoints": !flagCaps.noConditional, "supportsRestartRequest": flagCaps.restart,
			"supportsSetVariable": !flagCaps.noSetVariable, "supportsSetExpression": flagCaps.setExpression,
			"supportsExceptionInfoRequest": !flagCaps.noExceptionInfo, "supportsClipboardContext": flagCaps.clipboard,
		}
		if !flagCaps.noExceptions {
			// dlv 1.27.2 の 2 つのフィルターと同じ形（片方は default が true）。
			caps["exceptionBreakpointFilters"] = []map[string]any{
				{"filter": "panic", "label": "Unrecovered Panics", "default": true},
				{"filter": "all", "label": "All Exceptions", "description": "every throw"},
			}
		}
		a.respond(m, caps)
		a.output("console", fmt.Sprintf("initialize supportsStartDebuggingRequest=%v\n", args.SupportsStartDebuggingRequest))
	case "restart":
		a.output("console", fmt.Sprintf("restart args %s\n", m.Arguments))
		a.respond(m, nil)
		if flagCaps.restartFail {
			// dlv 1.27.2 の onRestartRequest と同じく、成功の応答の後に、同じ request へ失敗の応答を送り、セッションを終える。
			a.respondError(m, "Failed to restart: build error in main.go")
			a.event("terminated", nil)
			return
		}
		a.stateMu.Lock()
		a.stops, a.cur, a.running = nil, -1, false
		a.stateMu.Unlock()
		// dlv と同じく、始め直した後に initialized を送り、setBreakpoints と configurationDone を待ってから走らせる。
		a.launchSeq = 0
		a.bps = map[string][]int{}
		a.event("initialized", nil)
	case "launch", "attach":
		var args struct {
			Program string      `json:"program"`
			Fake    fakeOptions `json:"fake"`
		}
		_ = json.Unmarshal(m.Arguments, &args)
		a.stateMu.Lock()
		a.opts = args.Fake
		a.command = m.Command
		a.stateMu.Unlock()
		if a.opts.FailLaunch {
			a.respondError(m, "cannot build "+args.Program)
			return
		}
		// 受け取った引数の全体（fake を除く）を 1 行の JSON で出す。テストが type、request、name、env などを確かめる。
		// どのアダプターのプロセスが受けたかを、_pid に入れる（子のセッションのテスト）。
		var all map[string]any
		_ = json.Unmarshal(m.Arguments, &all)
		delete(all, "fake")
		all["_pid"] = os.Getpid()
		body, _ := json.Marshal(all)
		a.output("console", fmt.Sprintf("%s args %s\n", m.Command, body))
		if v := os.Getenv("FAKEADAPTER_ENV"); v != "" {
			a.output("console", "adapterenv "+v+"\n")
		}
		a.output("console", "launch "+args.Program+"\n")
		a.output("console", fmt.Sprintf("adapter %d\n", os.Getpid()))
		if a.opts.Child {
			if err := a.startChild(); err != nil {
				a.respondError(m, err.Error())
				return
			}
		}
		if wd, err := os.Getwd(); err == nil {
			a.output("console", "cwd "+wd+"\n")
		}
		if a.opts.LaunchBeforeInitialized {
			a.respond(m, nil)
		} else {
			a.launchSeq = m.Seq
		}
		a.event("initialized", nil)
	case "setBreakpoints":
		var args struct {
			Source struct {
				Path string `json:"path"`
			} `json:"source"`
			Breakpoints []struct {
				Line      int    `json:"line"`
				Condition string `json:"condition"`
			} `json:"breakpoints"`
		}
		_ = json.Unmarshal(m.Arguments, &args)
		var lines []int
		var res []map[string]any
		for _, b := range args.Breakpoints {
			line := b.Line + a.opts.MoveBy
			lines = append(lines, line)
			if b.Condition != "" {
				a.output("console", fmt.Sprintf("condition %d %q\n", line, b.Condition))
			}
			r := map[string]any{"verified": !slices.Contains(a.opts.Unverified, b.Line), "line": line}
			if a.opts.VerifyLater || a.opts.NeverVerify {
				a.nextBPID++
				r["id"], r["verified"] = a.nextBPID, false
				a.bpIDs[location{args.Source.Path, line}] = a.nextBPID
			}
			res = append(res, r)
		}
		a.bps[args.Source.Path] = lines
		a.output("console", fmt.Sprintf("setBreakpoints %s %v\n", filepath.Base(args.Source.Path), lines))
		a.respond(m, map[string]any{"breakpoints": res})
	case "configurationDone":
		a.respond(m, nil)
		if a.launchSeq != 0 {
			a.respond(&message{Seq: a.launchSeq, Command: "launch"}, nil)
		}
		a.output("stdout", "hello from debuggee\n")
		a.output("telemetry", "telemetry must not be shown\n")
		if a.opts.ExtraOutput {
			a.output("stderr", "err line\r\n")
			a.output("stdout", "split\r")
			a.output("stdout", "\nprogress 1\rprogress 2\n")
			a.output("important", "important note\n")
		}
		if a.opts.ThreadEvents {
			time.AfterFunc(500*time.Millisecond, func() {
				a.event("thread", map[string]any{"reason": "started", "threadId": 7})
			})
			time.AfterFunc(1000*time.Millisecond, func() {
				a.event("thread", map[string]any{"reason": "exited", "threadId": 7})
			})
		}
		if a.opts.ANSI {
			a.output("stdout", "\x1b[31mred\x1b[0m \x1b]0;title\x07plain \x1b[1")
			a.output("stdout", ";32mgreen\x1b[0m end\n")
		}
		if a.opts.VerifyLater {
			for _, id := range a.bpIDs {
				a.event("breakpoint", map[string]any{"reason": "changed", "breakpoint": map[string]any{"id": id, "verified": true}})
			}
		}
		if a.opts.Reverse {
			a.send(message{Type: "request", Command: "runInTerminal", Arguments: json.RawMessage(`{"args":["true"]}`)})
			return
		}
		if a.opts.StartDebugging {
			// 子のセッションを求める。ParentRuns でなければ、自分のブレークポイントでは止まらない。
			if err := a.requestChild(); err != nil {
				a.output("stderr", "startDebugging: "+err.Error()+"\n")
			}
			if !a.opts.ParentRuns {
				return
			}
		}
		if a.opts.Idle {
			return
		}
		a.run()
	case "threads":
		if a.opts.NoThreads {
			a.respond(m, map[string]any{"threads": []map[string]any{}})
			return
		}
		a.output("console", "threads\n")
		threads := []map[string]any{{"id": a.threadID(), "name": "main"}}
		if a.opts.TwoThreads {
			threads = append(threads, map[string]any{"id": 2, "name": "worker"})
		}
		a.respond(m, map[string]any{"threads": threads})
	case "stackTrace":
		var targs struct {
			ThreadID int `json:"threadId"`
			Levels   int `json:"levels"`
		}
		_ = json.Unmarshal(m.Arguments, &targs)
		// 受けた順を出す（止まった位置の stackTrace が、ペインの読み込みより先に届くことのテスト）。
		a.output("console", fmt.Sprintf("stackTrace thread=%d levels=%d\n", targs.ThreadID, targs.Levels))
		if a.opts.TwoThreads && targs.ThreadID == 2 {
			// 止まっていないスレッドの代役。ソースの無いフレーム（<node_internals> の代役）を 1 つ持つ。
			a.respond(m, map[string]any{"stackFrames": []map[string]any{
				{"id": 2000, "name": "worker.run", "line": 1, "column": 1, "source": map[string]any{"name": "loader", "path": "<node_internals>/loader"}},
			}})
			return
		}
		a.stateMu.Lock()
		if a.cur < 0 || a.cur >= len(a.stops) {
			// ブレークポイントでなく一時停止で止まった（位置を持たない）。
			a.stateMu.Unlock()
			a.respond(m, map[string]any{"stackFrames": []map[string]any{}})
			return
		}
		loc := a.stops[a.cur]
		delay := a.opts.StackTraceDelayMs > 0 && !a.delayed
		a.delayed = a.delayed || delay
		a.stateMu.Unlock()
		if delay {
			// 応答を遅らせる間も、ほかの request（continue など）を受けられるよう、別の goroutine で応答する。
			a.output("console", "stackTrace requested\n")
			go func() {
				time.Sleep(time.Duration(a.opts.StackTraceDelayMs) * time.Millisecond)
				a.respond(m, map[string]any{"stackFrames": []map[string]any{{
					"id": 1000, "name": "main.main", "line": loc.line, "column": 1,
					"source": map[string]any{"name": filepath.Base(loc.path), "path": loc.path},
				}}})
			}()
			return
		}
		col := a.opts.Column
		if col == 0 {
			col = 1
		}
		frame := map[string]any{
			"id": 1000, "name": "main.main", "line": loc.line, "column": col,
			"source": map[string]any{"name": filepath.Base(loc.path), "path": loc.path},
		}
		if a.opts.NoSource {
			delete(frame, "source")
		}
		if a.opts.TopSubtle {
			frame["presentationHint"] = "subtle"
		}
		// 先頭のフレームの後に、subtle のフレームと、呼んだ側のフレームを並べる（スレッドの区画のテスト）。levels が 1 なら先頭だけ。
		// 呼んだ側のフレームのパスは、CallerPath があればそれ（手元にあるファイルのフレームのテスト）、なければ止まった位置のファイルである。
		callerPath := loc.path
		if a.opts.CallerPath != "" {
			callerPath = a.opts.CallerPath
		}
		frames := []map[string]any{frame,
			{"id": 1001, "name": "runtime.main", "line": 1, "column": 1, "presentationHint": "subtle",
				"source": map[string]any{"name": filepath.Base(loc.path), "path": loc.path}},
			{"id": 1002, "name": "main.caller", "line": 3, "column": 1,
				"source": map[string]any{"name": filepath.Base(callerPath), "path": callerPath}},
		}
		var sargs struct {
			Levels int `json:"levels"`
		}
		_ = json.Unmarshal(m.Arguments, &sargs)
		if sargs.Levels > 0 && sargs.Levels < len(frames) {
			frames = frames[:sargs.Levels]
		}
		a.respond(m, map[string]any{"stackFrames": frames, "totalFrames": 3})
	case "scopes":
		var sargs struct {
			FrameID int `json:"frameId"`
		}
		_ = json.Unmarshal(m.Arguments, &sargs)
		a.output("console", fmt.Sprintf("scopes %d\n", sargs.FrameID))
		if sargs.FrameID == 1002 {
			a.respond(m, map[string]any{"scopes": []map[string]any{{"name": "Locals", "variablesReference": 11}}})
			return
		}
		a.respond(m, map[string]any{"scopes": []map[string]any{
			{"name": "Locals", "variablesReference": 1},
			{"name": "Globals", "variablesReference": 2, "expensive": true},
		}})
	case "variables":
		var vargs struct {
			VariablesReference int    `json:"variablesReference"`
			Filter             string `json:"filter"`
			Start              int    `json:"start"`
			Count              int    `json:"count"`
		}
		_ = json.Unmarshal(m.Arguments, &vargs)
		a.output("console", fmt.Sprintf("variables %d %s %d %d\n", vargs.VariablesReference, vargs.Filter, vargs.Start, vargs.Count))
		a.stateMu.Lock()
		vars, ok := a.variables(vargs.VariablesReference)
		// big（参照 3）の子は添字の子だけで、filter と start と count で範囲を返す（DAP の仕様の VariablesArguments）。
		if vargs.VariablesReference == 3 {
			switch {
			case vargs.Filter == "named":
				vars = nil
			case vargs.Count > 0:
				vars = vars[min(vargs.Start, len(vars)):min(vargs.Start+vargs.Count, len(vars))]
			}
		}
		delay := vargs.VariablesReference == 1 && a.opts.VariablesDelayMs > 0 && !a.varDelayed
		a.varDelayed = a.varDelayed || delay
		a.stateMu.Unlock()
		if !ok {
			a.respondError(m, fmt.Sprintf("unknown variablesReference %d", vargs.VariablesReference))
			return
		}
		if delay {
			a.output("console", "variables requested\n")
			go func() {
				time.Sleep(time.Duration(a.opts.VariablesDelayMs) * time.Millisecond)
				a.respond(m, map[string]any{"variables": vars})
			}()
			return
		}
		a.respond(m, map[string]any{"variables": vars})
	case "setVariable", "setExpression":
		var sargs struct {
			VariablesReference int    `json:"variablesReference"`
			Name               string `json:"name"`
			Expression         string `json:"expression"`
			Value              string `json:"value"`
		}
		_ = json.Unmarshal(m.Arguments, &sargs)
		name := sargs.Name
		if m.Command == "setExpression" {
			name = sargs.Expression
		}
		a.output("console", fmt.Sprintf("%s %s=%s ref=%d\n", m.Command, name, sargs.Value, sargs.VariablesReference))
		a.stateMu.Lock()
		if a.overrides == nil {
			a.overrides = map[string]string{}
		}
		if !a.opts.StaleAfterSet {
			a.overrides[name] = sargs.Value
		}
		a.stateMu.Unlock()
		a.respond(m, map[string]any{"value": sargs.Value})
	case "setExceptionBreakpoints":
		var eargs struct {
			Filters []string `json:"filters"`
		}
		_ = json.Unmarshal(m.Arguments, &eargs)
		a.output("console", fmt.Sprintf("setExceptionBreakpoints %v\n", eargs.Filters))
		a.respond(m, map[string]any{})
	case "exceptionInfo":
		a.output("console", "exceptionInfo\n")
		a.respond(m, map[string]any{
			"exceptionId": "panic", "description": "boom", "breakMode": "unhandled",
			"details": map[string]any{
				"typeName": "runtime.Error", "message": "boom", "stackTrace": "main.main()\r\n\tmain.go:6",
				"innerException": []map[string]any{{"typeName": "inner.Error", "message": "cause"}},
			},
		})
	case "evaluate":
		a.evaluate(m)
	case "continue", "next", "stepIn", "stepOut":
		var targs struct {
			ThreadID int `json:"threadId"`
		}
		_ = json.Unmarshal(m.Arguments, &targs)
		if targs.ThreadID != a.threadID() {
			a.respondError(m, fmt.Sprintf("unknown thread %d", targs.ThreadID))
			return
		}
		a.output("console", fmt.Sprintf("%s thread=%d\n", m.Command, targs.ThreadID))
		a.stateMu.Lock()
		running := a.running
		a.running = a.opts.StopDelayMs > 0
		a.stateMu.Unlock()
		if running {
			a.respondError(m, "debuggee is running")
			return
		}
		var body any
		if m.Command == "continue" {
			body = map[string]any{"allThreadsContinued": true}
		}
		a.respond(m, body)
		if a.opts.RunForever && m.Command == "continue" {
			a.stateMu.Lock()
			a.running = true
			a.stateMu.Unlock()
			a.output("console", "continued after "+a.command+"\n")
			return
		}
		if a.opts.StopDelayMs == 0 {
			a.resume(m.Command)
			return
		}
		command := m.Command
		time.AfterFunc(time.Duration(a.opts.StopDelayMs)*time.Millisecond, func() {
			a.stateMu.Lock()
			a.running = false
			a.stateMu.Unlock()
			a.resume(command)
		})
	case "pause":
		a.stateMu.Lock()
		a.running = false
		a.stateMu.Unlock()
		a.respond(m, nil)
		a.output("console", "pause\n")
		a.stopped("pause")
	case "terminate":
		a.respond(m, nil)
		a.output("console", "terminate requested\n")
		a.killChild()
		a.event("terminated", nil)
	case "disconnect":
		var args struct {
			TerminateDebuggee bool `json:"terminateDebuggee"`
		}
		_ = json.Unmarshal(m.Arguments, &args)
		a.output("console", fmt.Sprintf("disconnect terminateDebuggee=%v after %s\n", args.TerminateDebuggee, a.command))
		a.respond(m, nil)
		if args.TerminateDebuggee {
			a.killChild()
		}
		if a.conn != nil {
			// 待ち受けたアダプターは終わらずに、次の接続を待つ。
			_ = a.conn.Close()
			return
		}
		os.Exit(0)
	default:
		a.respondError(m, "unknown command "+m.Command)
	}
}

// requestChild は、逆向きの request の startDebugging を送る。ChildListen なら、子のセッションのために待ち受け、
// configuration の connect にそのホストとポートを入れる。
func (a *adapter) requestChild() error {
	cfg := a.opts.ChildConfig
	if cfg == nil {
		cfg = map[string]any{"type": "fake", "name": "child", "__pendingTargetId": "t1"}
	}
	if a.opts.ChildListen {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		go acceptLoop(ln, a.terminate)
		cfg["connect"] = map[string]any{"host": "127.0.0.1", "port": ln.Addr().(*net.TCPAddr).Port}
	}
	request := a.opts.ChildRequest
	if request == "" {
		request = a.command
	}
	args, err := json.Marshal(map[string]any{"request": request, "configuration": cfg})
	if err != nil {
		return err
	}
	a.send(message{Type: "request", Command: "startDebugging", Arguments: args})
	return nil
}

// startChild は、自分を -sleep で別のプロセスグループに起動し、pid を出力する。
func (a *adapter) startChild() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "-sleep")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	a.childMu.Lock()
	a.child = cmd
	a.childMu.Unlock()
	a.output("console", fmt.Sprintf("child %d\n", cmd.Process.Pid))
	return nil
}

// killChild は、子プロセスを終わらせて待つ。
func (a *adapter) killChild() {
	a.childMu.Lock()
	defer a.childMu.Unlock()
	if a.child != nil {
		_ = a.child.Process.Kill()
		_ = a.child.Wait()
		a.child = nil
	}
}

// resume は、続行かステップ実行（command）の後に止まる。next は 1 行進み、stepIn と stepOut はその場で止まる。
func (a *adapter) resume(command string) {
	switch command {
	case "continue":
		if a.opts.ExceptionOnContinue {
			a.stopped("exception")
			return
		}
		a.next("breakpoint")
	case "next":
		a.stateMu.Lock()
		a.stops[a.cur].line++
		a.stateMu.Unlock()
		a.stopped("step")
	default:
		a.stopped("step")
	}
}

// run は、ブレークポイントの位置を止まる順に並べ、最初の位置で止まる。
func (a *adapter) run() {
	paths := make([]string, 0, len(a.bps))
	for p := range a.bps {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	a.stateMu.Lock()
	for _, p := range paths {
		for _, l := range a.bps[p] {
			a.stops = append(a.stops, location{p, l})
		}
	}
	delay := a.opts.FirstStopDelayMs
	a.stateMu.Unlock()
	if delay > 0 {
		time.AfterFunc(time.Duration(delay)*time.Millisecond, func() { a.next("breakpoint") })
		return
	}
	a.next("breakpoint")
}

// next は、次のブレークポイントで止まる。無ければ終了する。
func (a *adapter) next(reason string) {
	a.stateMu.Lock()
	a.cur++
	done := a.cur >= len(a.stops)
	a.stateMu.Unlock()
	if done {
		a.event("exited", map[string]any{"exitCode": 0})
		a.event("terminated", nil)
		return
	}
	a.stopped(reason)
}

// variables は、参照 ref の子の変数を返す。stateMu を持って呼ぶ。
//   - 1（先頭のフレームの Locals）: x（止まった回数。書き換えればその値）、big（3000 の要素の列）、s（長い文字列）、nested（深い入れ子）
//   - 2（Globals）: g
//   - 3: big の要素。11（呼んだ側のフレームの Locals）: y。20 から: nested の子
func (a *adapter) variables(ref int) ([]map[string]any, bool) {
	val := func(name, def string) string {
		if v, ok := a.overrides[name]; ok {
			return v
		}
		return def
	}
	switch {
	case ref == 1:
		return []map[string]any{
			{"name": "x", "value": val("x", strconv.Itoa(a.nstop)), "type": "int", "evaluateName": "x", "variablesReference": 0},
			{"name": "big", "value": "[]int len: 3000", "type": "[]int", "evaluateName": "big", "variablesReference": 3, "indexedVariables": 3000},
			{"name": "s", "value": strings.Repeat("long ", 400), "type": "string", "variablesReference": 0},
			{"name": "nested", "value": "{...}", "type": "T", "variablesReference": 20},
		}, true
	case ref == 2:
		return []map[string]any{{"name": "g", "value": "1", "type": "int", "variablesReference": 0}}, true
	case ref == 3:
		out := make([]map[string]any, 3000)
		for i := range out {
			out[i] = map[string]any{"name": fmt.Sprintf("[%d]", i), "value": strconv.Itoa(i), "type": "int", "variablesReference": 0}
		}
		return out, true
	case ref == 11:
		return []map[string]any{{"name": "y", "value": val("y", "7"), "type": "int", "variablesReference": 0}}, true
	case ref >= 20 && ref < 120:
		return []map[string]any{{"name": "next", "value": fmt.Sprintf("depth %d", ref-19), "variablesReference": ref + 1}}, true
	}
	return nil, false
}

// evaluate は、evaluate の request に答える。受けたことを "evaluate <context> <式> frame=<frameId か none>" で出す。
//   - x: 止まった回数（書き換えればその値）。big: 3000 の要素の列（参照 3）。nested: 深い入れ子（参照 20）。
//   - y: 呼んだ側のフレーム（1002）でだけ 7。ほかのフレームでは誤り（フレームを選び直したときのテスト）。
//   - multi: 2 行にわたる結果。x = N: x を N に書き換える（REPL の代入）。
//   - ほかの式は誤り。context が clipboard なら、結果の前に clip: を付ける。
func (a *adapter) evaluate(m *message) {
	var args struct {
		Expression string `json:"expression"`
		FrameID    *int   `json:"frameId"`
		Context    string `json:"context"`
	}
	_ = json.Unmarshal(m.Arguments, &args)
	frame := "none"
	if args.FrameID != nil {
		frame = strconv.Itoa(*args.FrameID)
	}
	a.output("console", fmt.Sprintf("evaluate %s %s frame=%s\n", args.Context, args.Expression, frame))
	a.stateMu.Lock()
	var body map[string]any
	var fail string
	switch e := args.Expression; {
	case e == "x":
		v := strconv.Itoa(a.nstop)
		if o, ok := a.overrides["x"]; ok {
			v = o
		}
		body = map[string]any{"result": v, "type": "int", "variablesReference": 0}
	case e == "big":
		body = map[string]any{"result": "[]int len: 3000", "type": "[]int", "variablesReference": 3, "indexedVariables": 3000}
	case e == "nested":
		body = map[string]any{"result": "{...}", "variablesReference": 20}
	case e == "y" && frame == "1002":
		body = map[string]any{"result": "7", "variablesReference": 0}
	case e == "multi":
		body = map[string]any{"result": "line1\nline2", "variablesReference": 0}
	case strings.HasPrefix(e, "x = "):
		if a.overrides == nil {
			a.overrides = map[string]string{}
		}
		a.overrides["x"] = strings.TrimPrefix(e, "x = ")
		body = map[string]any{"result": a.overrides["x"], "variablesReference": 0}
	default:
		fail = "could not evaluate " + e
	}
	delay := args.Context == "watch" && a.opts.EvaluateDelayMs > 0 && !a.evalDelayed
	a.evalDelayed = a.evalDelayed || delay
	a.stateMu.Unlock()
	if fail != "" {
		a.respondError(m, fail)
		return
	}
	if args.Context == "clipboard" {
		body["result"] = "clip:" + body["result"].(string)
	}
	if delay {
		a.output("console", "evaluate requested\n")
		go func() {
			time.Sleep(time.Duration(a.opts.EvaluateDelayMs) * time.Millisecond)
			a.respond(m, body)
		}()
		return
	}
	a.respond(m, body)
}

func (a *adapter) stopped(reason string) {
	a.stateMu.Lock()
	a.nstop++
	n := a.nstop
	a.overrides = map[string]string{}
	a.stateMu.Unlock()
	a.output("console", fmt.Sprintf("stopped %d\n", n))
	body := map[string]any{"reason": reason, "allThreadsStopped": true}
	if reason == "exception" {
		body["text"] = "panic from the stopped event"
	}
	if !a.opts.NoThreadID {
		body["threadId"] = a.threadID()
	}
	if reason == "breakpoint" {
		a.stateMu.Lock()
		if id, ok := a.bpIDs[a.stops[a.cur]]; ok {
			body["hitBreakpointIds"] = []int{id}
		}
		a.stateMu.Unlock()
	}
	a.event("stopped", body)
}

// threadID は、スレッドの ID である。ThreadZero なら 0、そうでなければ 1。
func (a *adapter) threadID() int {
	if a.opts.ThreadZero {
		return 0
	}
	return 1
}
