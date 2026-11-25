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
	// StackTraceDelayMs は、最初の stackTrace の応答を遅らせる時間（ミリ秒）である。受け取ったら "stackTrace requested" を出す。
	StackTraceDelayMs int `json:"stackTraceDelayMs"`
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
}

var flagCaps capFlags

func main() {
	clientAddr := flag.String("client-addr", "", "unix:<path> of the client socket")
	listen := flag.String("listen", "", "listen on this TCP address (host:port) and serve each connection (serverPort and server)")
	terminate := flag.Bool("terminate", false, "support the terminate request")
	sleep := flag.Bool("sleep", false, "sleep forever (the debuggee stand-in)")
	flag.BoolVar(&flagCaps.noConditional, "no-conditional", false, "do not support conditional breakpoints")
	flag.BoolVar(&flagCaps.restart, "restart", false, "support the restart request")
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
	a := &adapter{r: bufio.NewReader(r), w: w, bps: map[string][]int{}, cur: -1, terminate: *terminate}
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
		a := &adapter{r: bufio.NewReader(c), w: c, bps: map[string][]int{}, cur: -1, terminate: terminate, conn: c}
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
		a.respond(m, map[string]any{
			"supportsConfigurationDoneRequest": true, "supportsTerminateRequest": a.terminate,
			"supportsConditionalBreakpoints": !flagCaps.noConditional, "supportsRestartRequest": flagCaps.restart,
		})
		a.output("console", fmt.Sprintf("initialize supportsStartDebuggingRequest=%v\n", args.SupportsStartDebuggingRequest))
	case "restart":
		a.output("console", fmt.Sprintf("restart args %s\n", m.Arguments))
		a.respond(m, nil)
		a.stateMu.Lock()
		a.stops, a.cur, a.running = nil, -1, false
		a.stateMu.Unlock()
		a.run()
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
			res = append(res, map[string]any{"verified": !slices.Contains(a.opts.Unverified, b.Line), "line": line})
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
		a.run()
	case "threads":
		a.respond(m, map[string]any{"threads": []map[string]any{{"id": 1, "name": "main"}}})
	case "stackTrace":
		a.stateMu.Lock()
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
		a.respond(m, map[string]any{"stackFrames": []map[string]any{frame}})
	case "continue", "next", "stepIn", "stepOut":
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
	a.stateMu.Unlock()
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

func (a *adapter) stopped(reason string) {
	body := map[string]any{"reason": reason, "allThreadsStopped": true}
	if !a.opts.NoThreadID {
		body["threadId"] = 1
	}
	a.event("stopped", body)
}
