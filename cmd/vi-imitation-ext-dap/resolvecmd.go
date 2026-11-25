package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/piplupflipper/vi-imitation/ext/expand"
)

// この節は、構成の resolveCommand（外部のコマンドが出す JSON で、始めるときに決まる値を決める。FR-282）である。
// 取り決めは基本設計 6.4 の「外部のコマンドの取り決め」にある。
//
// resolveCommand は、利用者の設定（本体の設定ファイル）の構成にだけ書ける。.vscode/launch.json の構成に書いてあれば、
// 動かさずに扱えない項目として始めない（#408 のレビューの裁定要 1 へのオーケストレータの決め。resolve.go）。
// 出力は構成の値だけを置き換え、アダプターの起動の方法（command、args、env、cwd、つなぎ方）は置き換えられない
// （アダプターの起動の方法は、利用者の設定の adapters だけが決める。同じ決め）。

const (
	// defaultResolveTimeout は、resolveCommand の timeoutMs を省いたときの期限である（仮）。
	defaultResolveTimeout = 10 * time.Second
	// maxResolveOutput は、標準出力の大きさの上限である（仮）。
	maxResolveOutput = 1 << 20
	// resolveStderrTail は、失敗したときに要旨を取るために覚える標準エラー出力の末尾の大きさである。
	resolveStderrTail = 4 << 10
	// resolveWaitDelay は、コマンドが終わった（か止めた）後に、標準出力と標準エラー出力を読み終えるのを待つ上限である（仮）。
	resolveWaitDelay = 500 * time.Millisecond
)

// resolveCommand は、構成の resolveCommand の項目を読んだものである。
type resolveCommand struct {
	argv    []string
	timeout time.Duration
}

// name は、知らせの文に入れるコマンドの名前（実行ファイルの最後の名前）である。長い絶対パスで理由が画面の幅から外れないようにする。
func (rc *resolveCommand) name() string { return filepath.Base(rc.argv[0]) }

// parseResolveCommand は、展開を終えた構成の resolveCommand を読む。無ければ nil を返す。
func parseResolveCommand(v any) (*resolveCommand, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := expand.Plain(v).(map[string]any)
	if !ok {
		return nil, errors.New("resolveCommand がオブジェクトでない")
	}
	argv, err := expand.Strings(m["command"])
	if err != nil || len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("resolveCommand.command が空でない文字列の配列でない")
	}
	rc := &resolveCommand{argv: argv, timeout: defaultResolveTimeout}
	if t, ok := m["timeoutMs"]; ok {
		n, ok := t.(json.Number)
		ms, err := n.Int64()
		if !ok || err != nil || ms <= 0 {
			return nil, errors.New("resolveCommand.timeoutMs が正の整数でない")
		}
		rc.timeout = time.Duration(ms) * time.Millisecond
	}
	return rc, nil
}

// resolveInput は、resolveCommand の標準入力に書く JSON である。
type resolveInput struct {
	// Configuration は、展開と envFile を当てた構成である。
	Configuration map[string]any `json:"configuration"`
	// Adapter は、使うアダプターの起動の方法である（読むだけで、出力で置き換えられない）。
	// ${port} と ${clientAddr} は文字のまま残る（expand.Text の MarshalJSON）。
	Adapter         map[string]any `json:"adapter"`
	File            string         `json:"file"`
	FileType        string         `json:"fileType"`
	WorkspaceFolder string         `json:"workspaceFolder"`
	WorkDir         string         `json:"workDir"`
}

// extensionKeys は、構成のうち拡張機能が読む項目である。アダプターへ送らない（基本設計 6.4）。
// resolveCommand の出力の configuration に書くと誤りにする（4 の段までに読み終えたもの。PR #401 のレビューの推奨 2）。
var extensionKeys = []string{
	"resolveCommand", "envFile", "linux", "windows", "osx", "presentation",
	"preLaunchTask", "postDebugTask", "serverReadyAction", "internalConsoleOptions",
}

// errResolveCancelled は、resolveCommand を取り消した（debug.terminate、shutdown）ことを表す。
var errResolveCancelled = errors.New("取り消した")

// runResolveCommand は、rc を起動して標準入力に in を書き、標準出力の JSON の configuration を返す。
// cwd はワークスペースの根で、環境は拡張機能のプロセスの環境をそのまま渡す（構成の env は渡さない）。
// コマンドは Setpgid と Pdeathsig の組で起動し、ctx が終わったか期限を超えたら、プロセスグループへ SIGKILL を送る。
// 標準出力を開いたままの子プロセスがプロセスグループの外に残っても、コマンドが終わってから resolveWaitDelay で読むのを打ち切る。
// logf には、標準エラー出力の全体と、失敗の全文を書く。
// 失敗（起動できない、0 でない終了コード、期限、不正な JSON、上限、扱えない項目）は、標準エラー出力の要旨を先に置いた短い誤りを返す。
//
// req: FR-282
func runResolveCommand(ctx context.Context, rc *resolveCommand, cwd string, in resolveInput, logf func(string, ...any)) (map[string]any, error) {
	input, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	name := rc.name()
	cmd := exec.Command(rc.argv[0], rc.argv[1:]...) //nolint:gosec // 利用者が設定に書いた外部のコマンドを起動する
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(input)
	var stdout limitedBuffer
	stdout.limit = maxResolveOutput
	var stderr tailBuffer
	stderr.limit = resolveStderrTail
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// 標準出力の書き込み口を持つ子プロセスがプロセスグループの外（setsid）に残ると、Wait はその子が閉じるまで返らない。
	// コマンドが終わった（か止めた）後は、resolveWaitDelay で読むのを打ち切る（#408 のレビューの必須 4）。
	cmd.WaitDelay = resolveWaitDelay
	fail := func(err error) (map[string]any, error) {
		err = withStderr(err, &stderr)
		logf("resolveCommand %s: %v", strings.Join(rc.argv, " "), err)
		return nil, err
	}
	if err = startAdapter(cmd); err != nil {
		return fail(fmt.Errorf("resolveCommand %s を起動できない: %w", name, err))
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	timer := time.NewTimer(rc.timeout)
	defer timer.Stop()
	var werr error
	select {
	case werr = <-waited:
	case <-timer.C:
		killGroup(cmd)
		<-waited //nolint:errcheck // 止めたプロセスの終わりを待つだけで、終了の状態は使わない
		return fail(fmt.Errorf("resolveCommand %s が期限（%s）までに終わらなかった", name, rc.timeout))
	case <-ctx.Done():
		killGroup(cmd)
		<-waited //nolint:errcheck // 止めたプロセスの終わりを待つだけで、終了の状態は使わない
		return nil, errResolveCancelled
	}
	if s := stderr.full.buf.String(); s != "" {
		logf("resolveCommand stderr: %s", strings.TrimRight(s, "\n"))
	}
	if errors.Is(werr, exec.ErrWaitDelay) {
		// コマンドは 0 で終わったが、標準出力を閉じていない子プロセスがある。それまでに読んだ出力を使う。
		logf("resolveCommand %s: a child process keeps stdout open; using the output read so far", name)
		werr = nil
	}
	if werr != nil {
		return fail(fmt.Errorf("resolveCommand %s が失敗した（%w）", name, werr))
	}
	if stdout.over {
		return fail(fmt.Errorf("resolveCommand %s の標準出力が上限（%d バイト）を超えた", name, maxResolveOutput))
	}
	v, err := decodeJSON(stdout.buf.Bytes())
	if err != nil {
		return fail(fmt.Errorf("resolveCommand %s の標準出力が 1 つの JSON でない: %w", name, err))
	}
	out, ok := v.(map[string]any)
	if !ok {
		return fail(fmt.Errorf("resolveCommand %s の標準出力が JSON のオブジェクトでない", name))
	}
	cfg, err := parseResolveOutput(out)
	if err != nil {
		return fail(fmt.Errorf("resolveCommand %s の出力: %w", name, err))
	}
	return cfg, nil
}

// parseResolveOutput は、resolveCommand の出力の configuration を読む。
// ほかの項目（アダプターの起動の方法を置き換える adapter を含む）と、configuration の中の拡張機能が読む項目は誤りにする。
func parseResolveOutput(out map[string]any) (map[string]any, error) {
	var bad []string
	for k := range out {
		if k != "configuration" {
			bad = append(bad, k)
		}
	}
	var cfg map[string]any
	if c, ok := out["configuration"]; ok && c != nil {
		m, ok := c.(map[string]any)
		if !ok {
			return nil, errors.New("configuration がオブジェクトでない")
		}
		for k := range m {
			// type はアダプターを差し替えることになるので扱えない（PR #410 の再レビューの推奨 C へのオーケストレータの決め）。
			// request は置き換えられ、置き換えた値で launch か attach か（終え方を含む）を決める（resolve.go）。
			if slices.Contains(extensionKeys, k) || k == "type" {
				bad = append(bad, "configuration."+k)
			}
		}
		cfg = m
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		return nil, fmt.Errorf("扱えない項目がある: %s", strings.Join(bad, ", "))
	}
	return cfg, nil
}

// overlay は、src のキーで dst の同じキーを置き換える（1 段目だけで、中のオブジェクトは混ぜない）。null はそのキーを消す。
func overlay(dst, src map[string]any) {
	for k, v := range src {
		if v == nil {
			delete(dst, k)
			continue
		}
		dst[k] = v
	}
}

// withStderr は、標準エラー出力の要旨（最後の空でない行）を err の前に置く。
// メッセージの行は画面の幅で切れるので、外部のコマンドが断った理由（いちばん要る情報）を先に出す（#408 のレビューの推奨 2）。
func withStderr(err error, stderr *tailBuffer) error {
	if line := lastLine(stderr.tail()); line != "" {
		return fmt.Errorf("%s: %w", line, err)
	}
	return err
}

// lastLine は、s の最後の空でない行を返す。
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\r\n\t "), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// limitedBuffer は、limit バイトまでを覚え、超えたら over を立てて残りを捨てる。
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); len(p) > room {
		b.over = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

// tailBuffer は、書かれた全体（ログに書く）と、末尾の limit バイトを覚える。
// 全体は maxResolveOutput で切る（ログを際限なく大きくしないため）。
type tailBuffer struct {
	full  limitedBuffer
	last  []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	if b.full.limit == 0 {
		b.full.limit = maxResolveOutput
	}
	_, _ = b.full.Write(p)
	b.last = append(b.last, p...)
	if over := len(b.last) - b.limit; over > 0 {
		b.last = b.last[over:]
	}
	return len(p), nil
}

func (b *tailBuffer) tail() string { return string(b.last) }

var _ io.Writer = (*tailBuffer)(nil)
