package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/editor"
	"github.com/piplupflipper/vi-imitation/internal/mux"
	"github.com/piplupflipper/vi-imitation/internal/protocol"
	"github.com/piplupflipper/vi-imitation/internal/server"
)

// envSocket は、server のソケットのパスを渡す環境変数である（基本設計 1.2 のソケットのパスの規則 1。FR-82）。
const envSocket = "VI_IMITATION_SOCKET"

// envServerStatusFD は、client が起動した server（-server）へ、起動の結果を書くパイプのファイル記述子の番号を渡す環境変数である。
// server は読んだら環境変数から消す（拡張機能と :! のコマンドへ引き継がない）。
const envServerStatusFD = "VI_IMITATION_SERVER_STATUS_FD"

// server が起動の結果としてパイプに書く 1 行。どれでもない行は、起動の誤りの文言である。
const (
	// statusReady は、ソケットで待ち受け、編集の client を受け付けられるようになったことを表す。
	// 起動の引数のファイルは server が持たない。繋いだ client が Hello で送り、server はその client を繋いだ後に開く。
	statusReady = "ready"
	// statusLocked は、他のプロセスが server のロックを持っていたので、server にならずに終わったことを表す。
	statusLocked = "locked"
)

// unitTimeout は、端末多重化ソフトに server の単位（tmux の window、herdr のタブ）を問い合わせる時間の上限である（仮）。
const unitTimeout = 2 * time.Second

// startTimeout は、server が無いときに、server を起動して繋ぐまでを待つ上限である（仮）。
// 基本設計 1.2 の「ロックを取れなかった側は 1 秒まで待つ」より長くした。server の起動では設定と拡張機能の宣言ファイルを読み、
// -race のテストでは 1 秒を超えることがあるためである。
const startTimeout = 5 * time.Second

// lockedWait は、他のプロセスがロックを持っていたときに、そのプロセスのソケットに繋げるのを待ってから、もう一度 server を起動するまでの時間である。
// ロックを持つのは起動の途中か終わりの途中の server で、終わりの途中の server はソケットを閉じた直後にロックを外すので、短くてよい。
const lockedWait = 200 * time.Millisecond

// dialPoll は、server のソケットに繋げるまで繋ぎ直す間隔である。
const dialPoll = 10 * time.Millisecond

// firstClientTimeout は、client が起動した server（-server）が、最初の編集の client を待つ上限である（仮。server.Server.FirstClientTimeout）。
// 起動した client は ready を読んですぐ繋ぐので、ふつうは数 ms で繋がる。
// 起動の途中で client が終わった（kill -9、ペインを閉じた、startTimeout で諦めた）ときに、server が誰も繋がないまま残らないようにする。
// 負荷の高いマシンで、起動した client が ready を読んでから繋ぐまでに掛かりうる時間より十分に長くした。
// テストは、TestMain で短くする。
var firstClientTimeout = 10 * time.Second

// startOptions は、client が server を起動するときに渡すものである。
// 起動の引数のファイルと -R は渡さない（繋いだ client が Hello で送る）。
type startOptions struct {
	logPath string
}

// focusTimeout は、FR-63 のフォーカスの移動で、端末多重化ソフトの操作を待つ上限である（仮）。
const focusTimeout = 2 * time.Second

// clientEnvNames は、編集の client が Hello で server へ知らせる環境変数の名前である（protocol.Hello.Env）。
// server のプロセスの環境は server を起動した client のものなので、外部のコマンドへは client ごとにこの値で上書きする（#215）。
// editor.EnvSplit は、server が見比べのために分割したペインの client の印で、server が読んで外す（#245）。
var clientEnvNames = []string{"TMUX_PANE", "HERDR_PANE_ID", "HERDR_TAB_ID", "HC_TERMINAL_ID", editor.EnvSplit}

// clientHello は、編集の client が server へ送る Hello を作る。files は絶対パスにしたもの、w と h は端末の大きさである。
// ペイン（FR-63）は、getenv から判定した端末多重化ソフトのペインの ID（tmux の TMUX_PANE、herdr の HERDR_PANE_ID、
// herdr app の端末の番地 HC_TERMINAL_ID。#440）で、作業ディレクトリと、clientEnvNames の環境変数も載せる（PR #207 の未確認事項）。
func clientHello(getenv func(string) string, files []string, readOnly bool, w, h int) protocol.Hello {
	hello := protocol.Hello{Kind: protocol.KindEdit, Width: w, Height: h, Files: files, ReadOnly: readOnly}
	switch mux.Detect(getenv).Kind() {
	case mux.KindTmux:
		hello.Pane = getenv("TMUX_PANE")
	case mux.KindHerdr:
		hello.Pane = getenv("HERDR_PANE_ID")
	case mux.KindHerdrApp:
		hello.Pane = getenv("HC_TERMINAL_ID")
	case mux.KindNone:
	}
	if wd, err := os.Getwd(); err == nil {
		hello.WorkDir = wd
	}
	for _, name := range clientEnvNames {
		if v := getenv(name); v != "" {
			hello.Env = append(hello.Env, name+"="+v)
		}
	}
	return hello
}

// withFocus は、cfg に、端末多重化ソフトの層 m でペインへフォーカスを移す関数（FR-63。editor.Config.FocusPane）を入れて返す。
// 端末多重化ソフトの外なら入れない。
func withFocus(cfg editor.Config, m mux.Mux) editor.Config {
	if m.Kind() == mux.KindNone {
		return cfg
	}
	// herdr app（#440）は mod に頼んで結果を待つので、待つ上限を mux.HerdrAppWait にする。
	wait := focusTimeout
	if m.Kind() == mux.KindHerdrApp {
		wait = mux.HerdrAppWait
	}
	cfg.FocusPane = func(from, pane string) error {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		return m.FocusPane(ctx, from, pane)
	}
	cfg.PaneFocused = func(pane string) (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		return m.PaneFocused(ctx, pane)
	}
	return cfg
}

// connectAuto は、基本設計 1.2 の規則で server のソケットのパスを決め、server に繋ぐ（FR-59）。
//
//  1. 環境変数 VI_IMITATION_SOCKET があり、その server に繋げれば、待つだけの client になる（FR-83。run が connectAuto の前に waitClient で扱う）。
//     繋げなければ（シェルが server より長く残った）、規則 2 以降で決める。connectAuto はこの規則を見ない
//  2. tmux の中なら、tmux のサーバーのソケットのパスと window の ID（m.Unit）から決めたパスの server に繋ぐ
//  3. herdr の中なら、herdr のソケットのパスとタブの ID（m.Unit）から決めたパスの server に繋ぐ
//
// 2 と 3 で server がいなければ、start で自分の子として server を起動してから繋ぐ（connectOrStart）。
// 1〜3 のどれでもなければ、conn も problem も nil を返し、呼び出し側は 1 つのプロセスで動かす（規則 4。FR-60）。
// 端末多重化ソフトの中なのに server を使えないとき（単位を引けない、ディレクトリを使えない、server を起動できない）は、
// problem にその理由を入れて返す。呼び出し側は、それを知らせて 1 つのプロセスで動かす（基本設計 1.2）。
//
// 返した接続は、起動したときに決めたソケットのものなので、ペインを別の window へ動かしても繋いだままである（FR-112）。
//
// req: FR-59, FR-112
func connectAuto(ctx context.Context, getenv func(string) string, m mux.Mux, start func(path string) (*serverChild, error), logger *slog.Logger) (conn protocol.ClientConn, problem error) {
	if p := getenv(envSocket); p != "" {
		logger.Info(envSocket+" の server に繋げないので、次の規則でソケットを決める", "path", p)
	}
	if m.Kind() == mux.KindNone {
		return nil, nil
	}
	uctx, cancel := context.WithTimeout(ctx, unitTimeout)
	unit, err := m.Unit(uctx)
	cancel()
	if err != nil {
		what := "tmux の window"
		switch m.Kind() {
		case mux.KindHerdr:
			what = "herdr のタブ"
		case mux.KindHerdrApp:
			what = "herdr app のタブ"
		case mux.KindNone, mux.KindTmux:
		}
		return nil, fmt.Errorf("%sを引けないので 1 つのプロセスで動かす: %w", what, err)
	}
	path, err := server.SocketPathForUnit(unit, getenv("XDG_RUNTIME_DIR"), os.TempDir())
	if err != nil {
		return nil, fmt.Errorf("server のディレクトリを使えないので 1 つのプロセスで動かす: %w", err)
	}
	logger.Info("server のソケット", "unit", unit, "path", path)
	conn, err = connectOrStart(ctx, path, start)
	if err != nil {
		return nil, fmt.Errorf("server に繋げないので 1 つのプロセスで動かす: %w", err)
	}
	return conn, nil
}

// connectOrStart は、path の server に繋ぐ。繋げなければ start で server を起動し、繋げるまで待つ（基本設計 1.2）。
//
// 同じ window の 2 つのペインでほぼ同時に起動すると、どちらの client も server を起動するが、
// server のロック（server.Lock）を取れた 1 つだけが server になり、もう一方は statusLocked を書いて終わる。
// ロックを取れなかった server を起動した client は、ロックを取った server のソケットに繋げるまで待つ。
// lockedWait の間に繋げなければ（ロックを持っていたのが終わりの途中の server だった）、もう一度 server を起動する。
// 全体で startTimeout まで待つ。
func connectOrStart(ctx context.Context, path string, start func(path string) (*serverChild, error)) (protocol.ClientConn, error) {
	if c, err := protocol.Dial(path); err == nil {
		return c, nil
	}
	deadline := time.Now().Add(startTimeout)
	for {
		child, err := start(path)
		if err != nil {
			return nil, err
		}
		status, err := child.wait(ctx, deadline)
		if err != nil {
			return nil, err
		}
		switch status {
		case statusReady:
			c, err := protocol.Dial(path)
			if err != nil {
				return nil, fmt.Errorf("起動した server に繋げない: %w", err)
			}
			return c, nil
		case statusLocked:
			until := time.Now().Add(lockedWait)
			if until.After(deadline) {
				until = deadline
			}
			if c := dialUntil(ctx, path, until); c != nil {
				return c, nil
			}
		default:
			return nil, fmt.Errorf("server を起動できない: %s", status)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("server のソケット %s に %v の間に繋げない", path, startTimeout)
		}
	}
}

// dialUntil は、path の server に繋げるまで dialPoll ごとに繋ぎ直す。deadline を過ぎるか ctx が終われば nil を返す。
func dialUntil(ctx context.Context, path string, deadline time.Time) protocol.ClientConn {
	for {
		if c, err := protocol.Dial(path); err == nil {
			return c
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(dialPoll):
		}
	}
}

// serverChild は、client が起動した server のプロセスの、起動の結果を読むパイプである。
type serverChild struct {
	// status は、server が書いた起動の結果の 1 行を 1 回だけ送る。何も書かずにパイプを閉じたら空を送る。
	status chan string
}

// wait は、server が起動の結果を書くのを、deadline か ctx の終わりまで待つ。
func (c *serverChild) wait(ctx context.Context, deadline time.Time) (string, error) {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case s := <-c.status:
		if s == "" {
			return "", errors.New("server が起動の結果を知らせずに終わった")
		}
		return s, nil
	case <-timer.C:
		return "", fmt.Errorf("server が %v の間に起動しない", startTimeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// newServerChild は、r から起動の結果の 1 行を読む goroutine を起こす。読み終えたら r を閉じる。
func newServerChild(r io.ReadCloser) *serverChild {
	c := &serverChild{status: make(chan string, 1)}
	go func() {
		defer func() { _ = r.Close() }()               // 読み終えたパイプを閉じる。誤りは扱えない
		line, _ := bufio.NewReader(r).ReadString('\n') // 読めなければ空の行（結果を知らせずに終わった）
		c.status <- strings.TrimSuffix(line, "\n")
	}()
	return c
}

// startServerProcess は、この実行ファイルを -server path で、自分の子として起動する（基本設計 1.2）。
//
// server は、client の端末のシグナル（SIGHUP、SIGINT）を受けず、client が終わっても動き続けるよう、
// 新しいセッション（したがって新しいプロセスグループ）で起動する（FR-61）。
// 標準入出力は /dev/null にする（端末を持つのは client だけである）。
// -log を指定していれば、標準エラー出力をそのログのファイルにする（server の panic の跡を残すため）。
// 起動の結果は、3 番のファイル記述子のパイプで受け取る（serveAuto）。
func startServerProcess(path string, opts startOptions) (*serverChild, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("実行ファイルのパスを決められない: %w", err)
	}
	args := []string{"-server", path}
	if opts.logPath != "" {
		logPath, aerr := filepath.Abs(opts.logPath)
		if aerr != nil {
			return nil, fmt.Errorf("ログのファイルのパスを決められない: %w", aerr)
		}
		args = append(args, "-log", logPath)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("server の起動の結果を受けるパイプを作れない: %w", err)
	}
	cmd := exec.Command(exe, args...) //nolint:gosec // 自分自身の実行ファイルを server として起動する
	cmd.Env = append(withoutEnv(os.Environ(), envSocket, envServerStatusFD), envServerStatusFD+"=3")
	cmd.ExtraFiles = []*os.File{w} // 3 番
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if opts.logPath != "" {
		if f, ferr := os.OpenFile(opts.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
			defer func() { _ = f.Close() }() // 子に渡した後は要らない
			cmd.Stderr = f
		}
	}
	err = cmd.Start()
	_ = w.Close() // 書く側は子だけが持つ。子が終わるか結果を書いて閉じると、読む側が EOF になる
	if err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("server を起動できない: %w", err)
	}
	go func() { _ = cmd.Wait() }() // client より先に終わった server（ロックを取れなかったもの）を回収する
	return newServerChild(r), nil
}

// withoutEnv は、env から names の環境変数を除いたものを返す。
func withoutEnv(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		keep := true
		for _, n := range names {
			if name == n {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

// statusPipe は、server が起動の結果を書くパイプである。書くのは 1 回だけで、書いたら閉じる。nil なら何もしない。
type statusPipe struct{ f *os.File }

// takeStatusPipe は、環境変数 envServerStatusFD のファイル記述子を statusPipe にし、環境変数を消す。無ければ nil のパイプを返す。
func takeStatusPipe() *statusPipe {
	v, ok := os.LookupEnv(envServerStatusFD)
	if !ok {
		return &statusPipe{}
	}
	_ = os.Unsetenv(envServerStatusFD) // 消せなくても、子に渡るのは使われない番号だけである
	fd, err := strconv.Atoi(v)
	if err != nil || fd < 3 {
		return &statusPipe{}
	}
	return &statusPipe{f: os.NewFile(uintptr(fd), "server-status")}
}

// send は、起動の結果 s を 1 行書いて閉じる。2 回目からは何もしない。
func (p *statusPipe) send(s string) {
	if p.f == nil {
		return
	}
	_, _ = fmt.Fprintln(p.f, strings.ReplaceAll(s, "\n", " ")) // client が終わっていれば読まれないだけである
	_ = p.f.Close()
	p.f = nil
}

// serveAuto は、-server のときの本体で、client が自動で起動した server として動く（基本設計 1.2）。端末を使わない。
//
// path のディレクトリのロック（server.Lock）を取れたときだけ server になる。取れなければ statusLocked を知らせて、正常に終わる。
// ロックを取ったら、繋げない古い server.sock を消して path で待ち受け（server.Listen）、statusReady を知らせる。
// 起動の引数のファイルは持たない。最初に繋いだ編集の client が Hello で送ったファイルを、その client を繋いだ後に開く（server.Server.Serve）。
// 起動の途中の誤りは、その文言を起動の結果として知らせる。
// 最後の編集の client が終わる（:q か、接続が切れた）とイベントループが戻り、ソケットを閉じ、ロックを外してから拡張機能を止めて終わる（FR-61）。
// firstClientTimeout の間に編集の client が 1 つも繋がなければ、同じく終わる（起動の途中で client が終わったとき）。
// ロックを拡張機能を止める前に外すのは、止めるのに数秒かかる間に同じ window で起動した client が、新しい server を起動できるようにするためである。
//
// req: FR-61
func serveAuto(path, logPath string) (err error) {
	status := takeStatusPipe()
	defer func() {
		if err != nil {
			status.send(err.Error())
		}
	}()
	logger, closeLog, err := openLog(logPath)
	if err != nil {
		return err
	}
	defer closeLog()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	defer stop()

	dir := filepath.Dir(path)
	if err = server.PrepareDir(dir); err != nil {
		return err
	}
	lock, err := server.Lock(dir)
	if errors.Is(err, server.ErrLocked) {
		logger.Info("他のプロセスが server のロックを持っているので、server にならずに終わる", "path", path)
		status.send(statusLocked)
		return nil
	}
	if err != nil {
		return err
	}
	unlock := func() {
		if lock != nil {
			_ = lock.Close() // 閉じるとロックが外れる。誤りはプロセスの終わりで外れるので扱わない
			lock = nil
		}
	}
	defer unlock()
	ln, err := server.Listen(path)
	if err != nil {
		return fmt.Errorf("server のソケットで待ち受けられない: %w", err)
	}
	srv, err := server.New(withFocus(withChildEnv(loadConfig(logger), path, logger), mux.Detect(os.Getenv)))
	if err != nil {
		_ = ln.Close() // ソケットのファイルを消す
		return fmt.Errorf("エディタを作れない: %w", err)
	}
	srv.FirstClientTimeout = firstClientTimeout
	logger.Info("server を起動した", "path", path, "pid", os.Getpid())
	status.send(statusReady)
	err = srv.Serve(ctx, ln, nil) // 戻る前に ln を閉じ、ソケットのファイルを消す
	unlock()
	closeServer(srv, logger)
	switch {
	case errors.Is(err, server.ErrNoClient):
		logger.Warn("編集の client が繋がないので終わる", "timeout", firstClientTimeout)
		return nil
	case err == nil, errors.Is(err, editor.ErrTerminalLost):
		// 最後の編集の client が終わったら、server も終わる（FR-61）
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("シグナルを受けて終了した: %w", context.Cause(ctx))
	}
	return err
}
