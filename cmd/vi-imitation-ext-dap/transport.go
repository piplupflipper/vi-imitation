package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/piplupflipper/vi-imitation/ext/expand"
)

// この節は、アダプターのつなぎ方（基本設計 6.4 の「アダプターのつなぎ方」と開始の流れの 7。FR-279）である。
//
//	| transport    | 起動 | 接続 |
//	| stdio        | command を起動する | 子プロセスの標準入出力。標準エラー出力はログへ |
//	| clientSocket | Unix ドメインソケットで待ち受け、${clientAddr} を置き換えて起動する | アダプターが繋いでくるのを待つ。標準出力と標準エラー出力はデバッグ対象の出力へ |
//	| serverPort   | 空いた TCP のポートを決め、${port} を置き換えて起動する | そのポートへ、期限まで間を置いて接続を試し直す。標準出力と標準エラー出力はログへ |
//	| server       | 起動しない | host と port へ接続する |
//
// アダプターの connectFrom のキーを構成が持てば（runSpec の adapter.direct）、transport に依らず、起動せずにそのつなぐ先へ直に繋ぐ。
// 起動するアダプターは、どのつなぎ方でも Setpgid と Pdeathsig の組で起動する（startAdapter）。

// dialRetryInterval は、serverPort でアダプターが待ち受けるまで接続を試し直す間である（仮）。
const dialRetryInterval = 50 * time.Millisecond

// adapterProcess は、つなぐ段階の展開を終えたアダプターの起動の方法である。
type adapterProcess struct {
	command string
	args    []string
	env     []string
	cwd     string
}

// expandConnect は、つなぐ段階の展開（基本設計 6.7 の「DAP での使い方」）で、launch の ${port} と ${clientAddr} だけを置き換える。
// values に無い変数（つなぎ方で使えない変数）が残れば、変数を並べた誤りにする。ほかの ${…} は文字のまま残す（展開し直さない）。
//
// req: FR-279
func expandConnect(launch map[string]any, transport string, values map[string]string) (map[string]any, error) {
	vars := map[string]expand.Entry{}
	for name, value := range values {
		vars[name] = expand.Entry{Value: func(string) (string, error) { return value, nil }}
	}
	v, err := expand.Expand(launch, expand.Resolver{Vars: vars, Unknown: expand.Defer, Env: expand.Defer})
	if err != nil {
		return nil, err
	}
	left, err := expand.Vars(v)
	if err != nil {
		return nil, err
	}
	if len(left) > 0 {
		return nil, fmt.Errorf("つなぎ方 %s では %s を使えない", transport, varList(left))
	}
	return expand.Plain(v).(map[string]any), nil
}

// processOf は、展開を終えた launch から、起動するコマンドを読む。
func processOf(launch map[string]any) (adapterProcess, error) {
	var p adapterProcess
	c, _ := launch["command"].(string)
	if c == "" {
		return p, errors.New("command が空")
	}
	p.command = c
	args, err := expand.Strings(launch["args"])
	if err != nil {
		return p, fmt.Errorf("args: %w", err)
	}
	p.args = args
	env, err := expand.StringMap(launch["env"])
	if err != nil {
		return p, fmt.Errorf("env: %w", err)
	}
	if len(env) > 0 {
		p.env = os.Environ()
		for k, v := range env {
			p.env = append(p.env, k+"="+v)
		}
	}
	if cwd, ok := launch["cwd"]; ok && cwd != nil {
		s, ok := cwd.(string)
		if !ok {
			return p, errors.New("cwd が文字列でない")
		}
		p.cwd = s
	}
	return p, nil
}

// freePort は、127.0.0.1 の空いた TCP のポートを返す。待ち受けて番号を得てから閉じるので、
// アダプターが待ち受けるまでの間に別のプロセスが取ることはありうる（基本設計 6.4。そのときは接続に失敗して始めない）。
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return port, ln.Close()
}

// spawn は、spec のアダプターを、つなぎ方に従って起動する（起動しないつなぎ方では、つなぐ先を決めるだけ）。
// 接続と DAP の初期化はまだ行わない（run が行う）。起動できなければ誤りを返し、子プロセスとソケットを残さない。
// 待たない処理だけなので、ext/server のループの上から呼んでよい。
//
// req: FR-279
func spawn(d *debugger, spec *runSpec) (*session, error) {
	s := newSession(d, spec)
	a := spec.adapter
	if a.direct != nil {
		s.dial = a.direct
		return s, nil
	}
	values := map[string]string{}
	switch a.transport {
	case transportServer:
		launch, err := expandConnect(a.launch, a.transport, values)
		if err != nil {
			return nil, err
		}
		ep, err := parseEndpoint(map[string]any{"host": launch["host"], "port": launch["port"]})
		if err != nil {
			return nil, fmt.Errorf("adapters.%s: %w", a.name, err)
		}
		s.dial = &ep
		return s, nil
	case transportClientSocket:
		dir, err := os.MkdirTemp("", "vi-imitation-ext-dap-")
		if err != nil {
			return nil, err
		}
		path := filepath.Join(dir, "dap.sock")
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		s.ln, s.sockDir = ln, dir
		// dlv の --client-addr の書き方（unix: に続けてソケットのパス）に合わせる。
		values["clientAddr"] = "unix:" + path
	case transportServerPort:
		port, err := freePort()
		if err != nil {
			return nil, err
		}
		host := "127.0.0.1"
		if h, ok := a.launch["host"].(string); ok && h != "" && !strings.Contains(h, "${") {
			host = h
		}
		values["port"] = strconv.Itoa(port)
		s.dial = &endpoint{host: host, port: port}
	default:
	}
	launch, err := expandConnect(a.launch, a.transport, values)
	if err != nil {
		s.closeSocket()
		return nil, err
	}
	p, err := processOf(launch)
	if err != nil {
		s.closeSocket()
		return nil, err
	}
	if err := s.startProcess(p); err != nil {
		s.closeSocket()
		return nil, err
	}
	return s, nil
}

// startProcess は、アダプターの子プロセスを起動し、つなぎ方に従って標準入出力をつなぐ。
func (s *session) startProcess(p adapterProcess) error {
	d := s.d
	// 子プロセスの実行ファイルと引数は、利用者の設定から来る。拡張機能はそれを起動するためにある。
	cmd := exec.Command(p.command, p.args...) //nolint:gosec // 利用者が設定したデバッグアダプターを起動する
	cmd.Dir = p.cwd
	cmd.Env = p.env
	var parentEnds, childEnds []*os.File
	closeAll := func(fs []*os.File) {
		for _, f := range fs {
			_ = f.Close()
		}
	}
	fail := func(err error) error {
		closeAll(parentEnds)
		closeAll(childEnds)
		return err
	}
	pipe := func() (r, w *os.File, err error) {
		r, w, err = os.Pipe()
		if err == nil {
			parentEnds, childEnds = append(parentEnds, r), append(childEnds, w)
		}
		return r, w, err
	}
	switch s.spec.adapter.transport {
	case transportClientSocket:
		// dlv dap は、デバッグ対象の標準出力と標準エラー出力を output の event で送らず、自分の標準出力と標準エラー出力へ書く
		// （https://github.com/go-delve/delve/blob/master/Documentation/api/dap/README.md）。デバッグ対象の出力として書き足す。
		r, w, err := pipe()
		if err != nil {
			return fail(err)
		}
		cmd.Stdout, cmd.Stderr = w, w
		go s.forward(r, func(text string) { d.appendOutput(text) })
	case transportServerPort:
		// アダプターの出力はアダプターのログなので、拡張機能のログ（本体が記録する）へ書く。
		r, w, err := pipe()
		if err != nil {
			return fail(err)
		}
		cmd.Stdout, cmd.Stderr = w, w
		go s.forward(r, func(text string) { d.logf("adapter: %s", strings.TrimRight(text, "\n")) })
	default:
		inR, inW, err := os.Pipe()
		if err != nil {
			return fail(err)
		}
		parentEnds, childEnds = append(parentEnds, inW), append(childEnds, inR)
		outR, outW, err := pipe()
		if err != nil {
			return fail(err)
		}
		errR, errW, err := pipe()
		if err != nil {
			return fail(err)
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
		s.stdinW, s.stdoutR = inW, outR
		// 標準入出力で話すときは、標準エラー出力はデバッグアダプターのログなので、拡張機能のログ（本体が記録する）へ書く。
		go s.forward(errR, func(text string) { d.logf("adapter: %s", strings.TrimRight(text, "\n")) })
	}
	s.cmd = cmd
	if err := startAdapter(cmd); err != nil {
		s.cmd = nil
		return fail(err)
	}
	closeAll(childEnds)
	go func() {
		_ = cmd.Wait()
		s.markProcDone()
		// 子プロセスが終わったら、接続とソケットを閉じ、読み取りと Accept を終わらせる。
		s.closeSocket()
		if c := s.getConn(); c != nil {
			c.close()
		}
	}()
	return nil
}

// dialAdapter は、s.dial へ TCP で繋ぐ。拡張機能が起動したアダプター（serverPort）なら、待ち受けるまで timeout まで試し直す。
// 既に動いているアダプター（server と connectFrom）は 1 回だけ試す。
func (s *session) dialAdapter(timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(s.eventCtx, timeout)
	defer cancel()
	var dialer net.Dialer
	for {
		c, err := dialer.DialContext(ctx, "tcp", s.dial.String())
		if err == nil {
			return c, nil
		}
		if s.cmd == nil {
			return nil, fmt.Errorf("connect to adapter %s: %w", s.dial, err)
		}
		select {
		case <-s.procDone:
			return nil, fmt.Errorf("adapter exited before listening on %s", s.dial)
		case <-ctx.Done():
			return nil, fmt.Errorf("connect to adapter %s: %w", s.dial, err)
		case <-time.After(dialRetryInterval):
		}
	}
}
