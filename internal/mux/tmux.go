package mux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tmux は、tmux 用の実装である（基本設計 1.3）。
//
// server は tmux のペインの中のプロセスではないので、どのコマンドにも対象のペインを -t で渡す。
// -t を省くと、対象はアクティブな window になり、起動したペインの window とは限らない（基本設計 1.2）。
type Tmux struct {
	// Bin は、tmux の実行ファイルである。
	Bin string
	// Socket は、tmux のサーバーのソケットのパスである（環境変数 TMUX の最初のカンマの前）。-S に渡す。
	// 空なら -S を付けず、tmux の既定のサーバーを使う。
	Socket string
	// Pane は、Detect に渡した環境変数のペインの ID（TMUX_PANE）である。
	Pane string

	mu sync.Mutex
	// popups は、OpenPopup で開いたポップアップを、PopupOptions.Target ごとに持つ。
	popups map[string]*tmuxPopup
}

// tmuxPopup は、OpenPopup で開いたポップアップである。
type tmuxPopup struct {
	// client は、ポップアップを開いた tmux の client の名前である。
	client string
	// exited は、display-popup のプロセスが終わった（ポップアップが閉じた）ときに閉じる。
	exited chan struct{}
}

var _ Mux = (*Tmux)(nil)

// Kind は KindTmux を返す。
func (*Tmux) Kind() Kind { return KindTmux }

// CurrentPane は、TMUX_PANE のペインの ID を返す。tmux のペインの ID は、ペインを別の window へ動かしても変わらない。
func (t *Tmux) CurrentPane(context.Context) (string, error) {
	return t.Pane, nil
}

// Unit は、TMUX_PANE のペインのある window を、tmux のサーバーのソケットのパスと window の ID を
// コロンでつないだ文字列で返す（基本設計 1.2 の規則 2）。
//
// セッションの ID は含めない。グループにしたセッション（new-session -t）では、同じペインの session_id が、
// セッションを足したり消したりするたびに変わる（PR #188 のレビューで、2026-11-18 に tmux 3.4 で確かめた）。
// window の ID は 1 つの tmux のサーバーの中で一意なので、セッションに依らずに window を指せる。
func (t *Tmux) Unit(ctx context.Context) (string, error) {
	out, err := t.run(ctx, "display-message", "-p", "-t", t.Pane, "#{socket_path}:#{window_id}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// SplitPane は、split-window でペインを分割して開く。
// Focus が false なら -d を付け、フォーカスを元のペインに残す。
func (t *Tmux) SplitPane(ctx context.Context, opts SplitOptions) (string, error) {
	if err := checkCommand(opts.Target, opts.Command); err != nil {
		return "", err
	}
	if err := checkPercent("size", opts.Size); err != nil {
		return "", err
	}
	args := []string{"split-window", "-P", "-F", "#{pane_id}", "-t", opts.Target}
	switch opts.Direction {
	case Right:
		args = append(args, "-h")
	case Down:
		args = append(args, "-v")
	case Left:
		args = append(args, "-h", "-b")
	}
	if !opts.Focus {
		args = append(args, "-d")
	}
	if opts.Size > 0 {
		args = append(args, "-l", strconv.Itoa(opts.Size)+"%")
	}
	args = append(args, t.commandArgs("-c", opts.Command)...)
	out, err := t.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// popupReadyScript は、ポップアップの中で、印のファイル（$0）を作ってからコマンドを exec する sh のスクリプトである。
const popupReadyScript = `: > "$0" && exec "$@"`

// popupPoll は、ポップアップの中のコマンドが起動したかを確かめる間隔である。
const popupPoll = 5 * time.Millisecond

// OpenPopup は、display-popup -E でポップアップを開く。opts.KeepOnFailure なら、コマンドを keepOnFailure で包む。
//
// display-popup は、ポップアップを出す tmux の client（-c）が要る。server は tmux の client ではないので、
// 元のペインの window を表示している tmux の client を探して -c に渡す（client）。
//
// display-popup は、ポップアップが閉じるまで戻らない。一方、tmux の client にポップアップが既に開いていると、
// 何も開かずにすぐ終了コード 0 で戻る（PR #188 のレビューで、2026-11-18 に tmux 3.4 で確かめた）。
// この 2 つを分けるため、中のコマンドの前に印のファイルを作らせ（popupReadyScript）、
// 印ができたら開いたとして戻り、印ができないまま display-popup が 0 で終わったら ErrPopupBusy を返す。
// 印は一時ディレクトリに作り、戻る前に消す。
//
// 開いたか分かる前に ctx が終わったら、ctx の誤りを返す。
// このとき開きかけのポップアップは残さない。層は戻った後も display-popup のプロセスを見張り、
// 印ができたら（ctx が終わった直後に開いた場合を含む）display-popup -C で閉じ、display-popup が終わってから印のディレクトリを消す。
// このため、ctx の誤りが返った後に、中のコマンドが一瞬起動して閉じられることはある（部品の client が繋いですぐ切れる）。
func (t *Tmux) OpenPopup(ctx context.Context, opts PopupOptions) error {
	if err := checkCommand(opts.Target, opts.Command); err != nil {
		return err
	}
	if err := checkPercent("width", opts.Width); err != nil {
		return err
	}
	if err := checkPercent("height", opts.Height); err != nil {
		return err
	}
	c, err := t.client(ctx, opts.Target)
	if err != nil {
		return err
	}
	client := c.name
	dir, err := os.MkdirTemp("", "vi-imitation-popup-")
	if err != nil {
		return fmt.Errorf("tmux display-popup: %w", err)
	}
	// ctx が先に終わったときは、見張りの goroutine が印のディレクトリを消す。
	keepDir := false
	defer func() {
		if !keepDir {
			_ = os.RemoveAll(dir)
		}
	}()
	ready := filepath.Join(dir, "ready")

	args := []string{"display-popup", "-E", "-c", client, "-t", opts.Target}
	if opts.Width > 0 {
		args = append(args, "-w", strconv.Itoa(opts.Width)+"%")
	}
	if opts.Height > 0 {
		args = append(args, "-h", strconv.Itoa(opts.Height)+"%")
	}
	inner := opts.Command
	if opts.KeepOnFailure {
		// 0 でなければキーを待ってから終わるので、-E のまま閉じるのを遅らせる（keepScript の doc）。
		inner = keepOnFailure(inner)
	}
	wrapped := inner
	wrapped.Args = append([]string{"sh", "-c", popupReadyScript, ready}, inner.Args...)
	args = append(args, t.commandArgs("-d", wrapped)...)

	// ポップアップの寿命は呼び出しの ctx に結びつけないので、exec.CommandContext を使わない。
	cmd := exec.Command(t.Bin, t.withSocket(args)...) //nolint:gosec // 実行するのは tmux で、引数は本体が組み立てたもの
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("tmux display-popup: %w", err)
	}
	p := &tmuxPopup{client: client, exited: make(chan struct{})}
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(p.exited)
	}()

	opened := func() bool {
		_, statErr := os.Stat(ready)
		return statErr == nil
	}
	tick := time.NewTicker(popupPoll)
	defer tick.Stop()
	for {
		select {
		case <-p.exited:
			if opened() {
				// 中のコマンドがすぐ終わり、ポップアップも閉じた。
				return nil
			}
			if waitErr != nil {
				return fmt.Errorf("tmux display-popup: %s: %w", strings.TrimSpace(stderr.String()), waitErr)
			}
			return ErrPopupBusy
		case <-tick.C:
			if opened() {
				t.mu.Lock()
				if t.popups == nil {
					t.popups = make(map[string]*tmuxPopup)
				}
				t.popups[opts.Target] = p
				t.mu.Unlock()
				return nil
			}
		case <-ctx.Done():
			keepDir = true
			go t.abandonPopup(p, dir, opened) //nolint:gosec // 呼び出しの ctx が終わった後の後始末なので、その ctx には結びつけない
			return fmt.Errorf("tmux display-popup: %w", ctx.Err())
		}
	}
}

// popupAbandonTimeout は、ctx が終わった後に開いたポップアップを閉じる display-popup -C の期限である。
const popupAbandonTimeout = 5 * time.Second

// abandonPopup は、開いたか分かる前に ctx が終わった OpenPopup の display-popup を見張る。
// 印ができたら display-popup -C でポップアップを閉じ、display-popup が終わったら印のディレクトリを消す。
// 印ができている間は、この層の display-popup のプロセスが動いているので、-C が閉じるのはこの層のポップアップである。
func (t *Tmux) abandonPopup(p *tmuxPopup, dir string, opened func() bool) {
	defer func() { _ = os.RemoveAll(dir) }()
	tick := time.NewTicker(popupPoll)
	defer tick.Stop()
	for {
		select {
		case <-p.exited:
			return
		case <-tick.C:
			if !opened() {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), popupAbandonTimeout)
			_, _ = t.run(ctx, "display-popup", "-C", "-c", p.client)
			cancel()
			select {
			case <-p.exited:
			case <-time.After(popupAbandonTimeout):
			}
			return
		}
	}
}

// ClosePopup は、この層が target に開いたポップアップがまだ開いていれば、display-popup -C で閉じる。
// 開いていなければ、何もせずに nil を返す。
//
// display-popup -C は、その tmux の client のポップアップを、誰が開いたかを問わず閉じる。
// この層のポップアップが開いている間は、同じ tmux の client に別のポップアップは開けないので、
// display-popup のプロセスが動いている間に打つ -C は、この層のポップアップを閉じる。
func (t *Tmux) ClosePopup(ctx context.Context, target string) error {
	t.mu.Lock()
	p := t.popups[target]
	t.mu.Unlock()
	if p == nil {
		return nil
	}
	select {
	case <-p.exited:
		t.forgetPopup(target, p)
		return nil
	default:
	}
	if _, err := t.run(ctx, "display-popup", "-C", "-c", p.client); err != nil {
		return err
	}
	select {
	case <-p.exited:
		t.forgetPopup(target, p)
	case <-ctx.Done():
	}
	return nil
}

// forgetPopup は、閉じたポップアップを表から消す。
func (t *Tmux) forgetPopup(target string, p *tmuxPopup) {
	t.mu.Lock()
	if t.popups[target] == p {
		delete(t.popups, target)
	}
	t.mu.Unlock()
}

// FocusPane は、from のペインを表示している tmux の client を、pane のペインへ移す。
//
// 切り替える tmux の client は、ポップアップと同じ規則（client）で from のペインから選ぶ。
// その client が pane の window を表示していれば、select-pane だけを打つ。
// そうでなければ switch-client -c <client> -t <ペイン> で、その window とペインへ移る。
// ペインを別のセッションの window へ動かした client（FR-112）へも、これで移れる。
// tmux の client が 1 つも無ければ、select-window と select-pane で、ペインのセッションの中だけを切り替える。
func (t *Tmux) FocusPane(ctx context.Context, from, pane string) error {
	if from == "" {
		return ErrNoTarget
	}
	window, err := t.paneWindow(ctx, pane)
	if err != nil {
		return err
	}
	clients, err := t.clients(ctx)
	if err != nil {
		return err
	}
	if len(clients) == 0 {
		_, err = t.run(ctx, "select-window", "-t", pane, ";", "select-pane", "-t", pane)
		return err
	}
	c, err := t.client(ctx, from)
	if err != nil {
		return err
	}
	if c.window == window {
		_, err = t.run(ctx, "select-pane", "-t", pane)
		return err
	}
	_, err = t.run(ctx, "switch-client", "-c", c.name, "-t", pane)
	return err
}

// ClosePane は、kill-pane でペインを閉じる。
func (t *Tmux) ClosePane(ctx context.Context, pane string) error {
	_, err := t.run(ctx, "kill-pane", "-t", pane)
	return err
}

// commandArgs は、起動するディレクトリ（dirFlag で渡す）、環境変数（-e）、コマンドの文字列の引数を返す。
// 環境変数は、引数の並びが毎回同じになるように名前の順に並べる。
func (*Tmux) commandArgs(dirFlag string, c Command) []string {
	var args []string
	if c.Dir != "" {
		args = append(args, dirFlag, c.Dir)
	}
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+c.Env[k])
	}
	return append(args, "--", shellJoin(c.Args))
}

// PaneFocused は、ペインが window のアクティブなペインで、その window をどれかの tmux の client が表示しているかを返す。
// tmux の client が 1 つも無ければ、利用者は見ていないので false である。
func (t *Tmux) PaneFocused(ctx context.Context, pane string) (bool, error) {
	out, err := t.run(ctx, "list-panes", "-a", "-F", "#{pane_id}\t#{window_id}\t#{pane_active}")
	if err != nil {
		return false, err
	}
	window, active, found := "", false, false
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 3 && f[0] == pane {
			window, active, found = f[1], f[2] == "1", true
			break
		}
	}
	if !found {
		return false, fmt.Errorf("%w: %s", errNoPane, pane)
	}
	if !active {
		return false, nil
	}
	clients, err := t.clients(ctx)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(clients, func(c tmuxClient) bool { return c.window == window }), nil
}

// errNoPane は、ペインが見つからないことを表す。
var errNoPane = errors.New("tmux: no such pane")

// paneWindow は、ペインのある window の ID を返す。
// display-message -t は、無いペインを指しても誤りにならないので、list-panes -a で探す。
func (t *Tmux) paneWindow(ctx context.Context, pane string) (string, error) {
	out, err := t.run(ctx, "list-panes", "-a", "-F", "#{pane_id}\t#{window_id}")
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if id, window, ok := strings.Cut(line, "\t"); ok && id == pane {
			return window, nil
		}
	}
	return "", fmt.Errorf("%w: %s", errNoPane, pane)
}

// tmuxClient は、list-clients の 1 行である。
type tmuxClient struct {
	name, session, window string
	activity              int64
	// width は、client の端末の列数（client_width）である。
	width int
}

// clients は、tmux の client の一覧を返す。
func (t *Tmux) clients(ctx context.Context) ([]tmuxClient, error) {
	out, err := t.run(ctx, "list-clients", "-F", "#{client_name}\t#{session_id}\t#{window_id}\t#{client_activity}\t#{client_width}")
	if err != nil {
		return nil, err
	}
	var cs []tmuxClient
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 {
			continue
		}
		activity, _ := strconv.ParseInt(f[3], 10, 64)
		width, _ := strconv.Atoi(f[4])
		cs = append(cs, tmuxClient{name: f[0], session: f[1], window: f[2], activity: activity, width: width})
	}
	return cs, nil
}

// ScreenWidth は、target のペインの上にポップアップを出す tmux の client（OpenPopup と同じ選び方。client）の端末の列数を返す
// （mux.ScreenWidther）。display-popup -w の百分率は、この列数に対するものである（FR-149）。
//
// req: FR-149
func (t *Tmux) ScreenWidth(ctx context.Context, target string) (int, error) {
	if target == "" {
		return 0, ErrNoTarget
	}
	c, err := t.client(ctx, target)
	if err != nil {
		return 0, err
	}
	if c.width <= 0 {
		return 0, fmt.Errorf("tmux: client %s has no width", c.name)
	}
	return c.width, nil
}

// client は、target のペインを表示している tmux の client を返す（ポップアップを出す先と、FocusPane で切り替える先）。
// target のペインの window を表示している client を優先し、無ければ同じセッションの client を使う。
// window の ID は tmux のサーバーの中で一意なので、window はセッションの ID に依らずに比べる。
// グループにしたセッションでは、同じ window を表示している client のセッションの ID が、
// display-message -t <ペイン> の session_id と異なりうるためである（PR #188 のレビュー）。
//
// 候補が複数なら、最後に操作された（client_activity の大きい）ものを選ぶ。
// client_activity は秒の単位なので、同じ順位で同じ秒に操作された client が複数あれば、list-clients の先に出たものを選ぶ。
// どれを選んでも target の window（または同じセッション）を表示している client なので、選び違えても別の window には出ない。
// ただし、同じ window を 2 つの端末で表示していると、利用者が見ていない方の端末に出ることはある。
func (t *Tmux) client(ctx context.Context, target string) (tmuxClient, error) {
	where, err := t.run(ctx, "display-message", "-p", "-t", target, "#{session_id}\t#{window_id}")
	if err != nil {
		return tmuxClient{}, err
	}
	session, window, _ := strings.Cut(strings.TrimSpace(where), "\t")
	clients, err := t.clients(ctx)
	if err != nil {
		return tmuxClient{}, err
	}
	var best *tmuxClient
	bestRank := 0
	for i := range clients {
		c := &clients[i]
		rank := 0
		switch {
		case c.window == window:
			rank = 2
		case c.session == session:
			rank = 1
		default:
			continue
		}
		if best == nil || rank > bestRank || (rank == bestRank && c.activity > best.activity) {
			best, bestRank = c, rank
		}
	}
	if best == nil {
		return tmuxClient{}, fmt.Errorf("tmux: no client is attached to the session of pane %s", target)
	}
	return *best, nil
}

// withSocket は、Socket があれば先頭に -S を足す。
func (t *Tmux) withSocket(args []string) []string {
	if t.Socket == "" {
		return args
	}
	return append([]string{"-S", t.Socket}, args...)
}

// run は、tmux のコマンドを起動し、標準出力を返す。失敗したら標準エラー出力を誤りに含める。
func (t *Tmux) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, t.Bin, t.withSocket(args)...) //nolint:gosec // 実行するのは tmux で、引数は本体が組み立てたもの
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("tmux %s: %w", args[0], err)
		}
		return "", fmt.Errorf("tmux %s: %s: %w", args[0], msg, err)
	}
	return stdout.String(), nil
}
