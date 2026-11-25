package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/piplupflipper/vi-imitation/ext/expand"
)

// この節は、構成の resolveCommand（外部のコマンドが出す JSON で、始めるときに決まる値を決める。FR-282）である。
// 取り決めは基本設計 6.4 の「外部のコマンドの取り決め」にある。

const (
	// defaultResolveTimeout は、resolveCommand の timeoutMs を省いたときの期限である（仮）。
	defaultResolveTimeout = 10 * time.Second
	// maxResolveOutput は、標準出力の大きさの上限である（仮）。
	maxResolveOutput = 1 << 20
	// resolveStderrTail は、失敗したときに要旨を取るために覚える標準エラー出力の末尾の大きさである。
	resolveStderrTail = 4 << 10
)

// resolveCommand は、構成の resolveCommand の項目を読んだものである。
type resolveCommand struct {
	argv    []string
	timeout time.Duration
}

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
	// Adapter は、使うアダプターの起動の方法である。${port} と ${clientAddr} は文字のまま残る（expand.Text の MarshalJSON）。
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

// runResolveCommand は、rc を起動して標準入力に in を書き、標準出力の JSON を構成 cfg とアダプターの起動の方法 launch に重ねる。
// cwd はワークスペースの根で、環境は拡張機能のプロセスの環境をそのまま渡す（構成の env は渡さない）。
// コマンドは Setpgid と Pdeathsig の組で起動し、ctx が終わったか期限を超えたら、プロセスグループへ SIGKILL を送る。
// logf には、標準エラー出力の全体を書く。
// 失敗（起動できない、0 でない終了コード、期限、不正な JSON、上限、扱えない項目）は、理由と標準エラー出力の要旨を持つ誤りを返す。
//
// req: FR-282
func runResolveCommand(ctx context.Context, rc *resolveCommand, cwd string, in resolveInput, logf func(string, ...any)) (cfg, launch map[string]any, err error) {
	input, err := json.Marshal(in)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(rc.argv[0], rc.argv[1:]...) //nolint:gosec // 利用者が構成に書いた外部のコマンドを起動する
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(input)
	var stdout limitedBuffer
	stdout.limit = maxResolveOutput
	var stderr tailBuffer
	stderr.limit = resolveStderrTail
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err = startAdapter(cmd); err != nil {
		return nil, nil, fmt.Errorf("resolveCommand %s を起動できない: %w", rc.argv[0], err)
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
		return nil, nil, withStderr(fmt.Errorf("resolveCommand %s が期限（%s）までに終わらなかった", rc.argv[0], rc.timeout), &stderr)
	case <-ctx.Done():
		killGroup(cmd)
		<-waited //nolint:errcheck // 止めたプロセスの終わりを待つだけで、終了の状態は使わない
		return nil, nil, errResolveCancelled
	}
	if s := stderr.full.buf.String(); s != "" {
		logf("resolveCommand stderr: %s", strings.TrimRight(s, "\n"))
	}
	if werr != nil {
		return nil, nil, withStderr(fmt.Errorf("resolveCommand %s が失敗した: %w", rc.argv[0], werr), &stderr)
	}
	if stdout.over {
		return nil, nil, withStderr(fmt.Errorf("resolveCommand %s の標準出力が上限（%d バイト）を超えた", rc.argv[0], maxResolveOutput), &stderr)
	}
	v, err := decodeJSON(stdout.buf.Bytes())
	if err != nil {
		return nil, nil, withStderr(fmt.Errorf("resolveCommand %s の標準出力が 1 つの JSON でない: %w", rc.argv[0], err), &stderr)
	}
	out, ok := v.(map[string]any)
	if !ok {
		return nil, nil, withStderr(fmt.Errorf("resolveCommand %s の標準出力が JSON のオブジェクトでない", rc.argv[0]), &stderr)
	}
	cfg, launch, err = parseResolveOutput(out)
	if err != nil {
		return nil, nil, withStderr(fmt.Errorf("resolveCommand %s の出力: %w", rc.argv[0], err), &stderr)
	}
	return cfg, launch, nil
}

// parseResolveOutput は、resolveCommand の出力の configuration と adapter を読む。
// ほかの項目と、configuration の中の拡張機能が読む項目と、adapter の起動の方法でない項目は誤りにする。
func parseResolveOutput(out map[string]any) (cfg, launch map[string]any, err error) {
	var bad []string
	for k := range out {
		if k != "configuration" && k != "adapter" {
			bad = append(bad, k)
		}
	}
	if c, ok := out["configuration"]; ok && c != nil {
		m, ok := c.(map[string]any)
		if !ok {
			return nil, nil, errors.New("configuration がオブジェクトでない")
		}
		for k := range m {
			if slices.Contains(extensionKeys, k) {
				bad = append(bad, "configuration."+k)
			}
		}
		cfg = m
	}
	if a, ok := out["adapter"]; ok && a != nil {
		m, ok := a.(map[string]any)
		if !ok {
			return nil, nil, errors.New("adapter がオブジェクトでない")
		}
		for k := range m {
			if !slices.Contains(launchKeys, k) {
				bad = append(bad, "adapter."+k)
			}
		}
		launch = m
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		return nil, nil, fmt.Errorf("扱えない項目がある: %s", strings.Join(bad, ", "))
	}
	return cfg, launch, nil
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

// withStderr は、err に標準エラー出力の要旨（最後の空でない行）を添える。
func withStderr(err error, stderr *tailBuffer) error {
	if line := lastLine(stderr.tail()); line != "" {
		return fmt.Errorf("%w（%s）", err, line)
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
