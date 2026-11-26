package mux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// herdr app（GUI の herdr。端末のサーバーは herdr-core で、コマンドの名前は hc）の中の実装である（#440）。
//
// herdr app の中のシェルには、環境変数 HC_TERMINAL_ID（その端末の番地。t10 など）と HC_TERMINAL_SOCKET だけが入る。
// ペインを割る命令（pane.split など）は herdr app の命令で、herdr app が別のマシンで動き、SSH でこのホストの hc connect に繋ぐときは、
// このホストから呼べない。このため、herdr app の mod（配布物の herdr-app-mod/。利用者が herdr app に入れる）に依頼を受け渡させる。
//
//  1. 本体は、依頼を、元の client の端末に hc label でラベル（HerdrAppRequestPrefix + 依頼の番号）として書き、
//     mod が依頼のある端末だけを hc ls --label で探せるように、印のラベル（HerdrAppPendingLabel=1）も付ける
//  2. mod は、印のある端末のラベルを読み、依頼のとおりに herdr app の命令（pane.split、pane.swap、pane.focus）を呼ぶ。
//     結果を同じ端末に結果のラベル（HerdrAppReplyPrefix + 依頼の番号）で書き返し、依頼のラベルを消す
//  3. 本体は、結果のラベルを hc get で待つ。期限までに結果が無ければ、mod が入っていないとみなして ErrHerdrAppNoReply を返す
//
// ラベルの名前と、依頼と結果の JSON の形は、mod（herdr-app-mod/main.js）と合わせる。

// 依頼と結果のラベルの名前である。値は JSON（herdrAppRequest、herdrAppReply）である。
const (
	// HerdrAppRequestPrefix は、依頼のラベルの名前の前置きである。後ろに依頼の番号を付ける。
	HerdrAppRequestPrefix = "vi-imitation.req."
	// HerdrAppReplyPrefix は、結果のラベルの名前の前置きである。後ろに依頼の番号を付ける。
	HerdrAppReplyPrefix = "vi-imitation.res."
	// HerdrAppPendingLabel は、依頼のある端末に付ける印のラベルの名前である（値は 1）。
	// mod は hc ls --label vi-imitation.pending=1 で、依頼のある端末だけを引く（hc ls はラベルを返さない）。
	HerdrAppPendingLabel = "vi-imitation.pending"
)

// HerdrAppWait は、herdr app の mod に頼んだ操作の結果を待つ上限の目安である（仮）。
// server は、herdr app の中でペインを分割するときとフォーカスを移すときに、この長さの期限を ctx に付ける。
// mod は依頼のあった後の 30 秒は 0.5 秒ごと、ほかは 2 秒ごとに依頼を探し（herdr-app-mod/main.js）、herdr app が別のマシンなら、hc の呼び出しは SSH を通る。
const HerdrAppWait = 10 * time.Second

// herdrAppPoll は、本体が結果のラベルを読む間隔である。herdrAppMark は、待つ間に印のラベルを付け直す間隔である。
// mod が別の依頼を片付けた後に印を消すのと、本体が依頼を書くのが重なって印が消えても、付け直せば mod が見つける。
const (
	herdrAppPoll = 100 * time.Millisecond
	herdrAppMark = time.Second
)

// ErrHerdrAppNoReply は、herdr app の mod に頼んだ操作の結果が期限までに届かなかったことを表す。
// mod を herdr app に入れていないか、herdr app が動いていない。
var ErrHerdrAppNoReply = errors.New("mux: no reply from the vi-imitation mod of the herdr app")

// HerdrApp は、herdr app の中の実装である（#440）。
//
// ペインの識別子は、hc の端末の番地（t10 など）である。herdr app のペインは端末を 1 つずつ持ち、
// 端末を終わらせるとペインも閉じるので、本体はペインを端末の番地で指す。
// 中央のポップアップ（OpenPopup、ClosePopup）は持たない（ErrUnsupported）。server は、herdr app の中では
// ポップアップを client の画面の中に描く。
type HerdrApp struct {
	// Bin は、hc の実行ファイルの絶対パスである（Detect が、渡された環境の PATH から決める）。
	// 絶対パスでなければ、どの操作も hc を呼ばずに誤りを返す。
	Bin string
	// Socket は、herdr-core のソケットのパス（環境変数 HC_TERMINAL_SOCKET）である。Unit で使う。
	Socket string
	// Terminal は、この層を作ったプロセスがいる端末の番地（環境変数 HC_TERMINAL_ID）である。
	Terminal string
}

var _ Mux = (*HerdrApp)(nil)

// herdrAppRequest は、mod への依頼である（依頼のラベルの値）。
type herdrAppRequest struct {
	// ID は、依頼の番号である（ラベルの名前の後ろと同じ）。
	ID string `json:"id"`
	// Op は、操作である。split（ペインを割る）、focus（Terminal の端末のペインへフォーカスを移す）、
	// focused（Terminal の端末のペインにフォーカスがあるかを問う）のどれかである。
	Op string `json:"op"`
	// Direction は、split の向き（right、down、left）である。left は、mod が右に割ってから元のペインと入れ替える。
	Direction string `json:"direction,omitempty"`
	// Argv は、split で新しいペインの端末で起動するコマンドである。
	Argv []string `json:"argv,omitempty"`
	// Focus は、split で新しいペインへフォーカスを移すかである。false なら mod は元のペインへフォーカスを戻す。
	Focus bool `json:"focus"`
	// Terminal は、focus と focused の相手の端末の番地である。
	Terminal string `json:"terminal,omitempty"`
}

// herdrAppReply は、mod が書き返す結果である（結果のラベルの値）。
type herdrAppReply struct {
	// ID は、依頼の番号である。
	ID string `json:"id"`
	// Error は、操作の誤りである。空なら成功である。
	Error string `json:"error,omitempty"`
	// Terminal は、split で新しいペインの端末の番地である（このホストの番地。herdr app の machine/ の前置きは mod が外す）。
	Terminal string `json:"terminal,omitempty"`
	// FocusError は、split でペインを割った後に、フォーカスを Focus のとおりに移せなかった誤りである。
	FocusError string `json:"focus_error,omitempty"`
	// Focused は、focused の答えである。
	Focused bool `json:"focused,omitempty"`
}

// herdrAppSeq は、依頼の番号の連番である。番号はプロセスの ID と連番を - でつないだものにする。
var herdrAppSeq atomic.Uint64

// Kind は KindHerdrApp を返す。
func (*HerdrApp) Kind() Kind { return KindHerdrApp }

// CurrentPane は、HC_TERMINAL_ID の端末の番地を返す。
func (a *HerdrApp) CurrentPane(context.Context) (string, error) { return a.Terminal, nil }

// Unit は、HC_TERMINAL_ID の端末のタブを、herdr-core のソケットのパスとタブの ID をコロンでつないだ文字列で返す（基本設計 1.2 の規則 3）。
// タブの ID は、herdr app が端末に付けるラベル herdr.tab である（hc get で読む）。
// ラベルが無ければ（herdr app に出していない端末）、端末の番地で代え、端末ごとに 1 つの server にする。
func (a *HerdrApp) Unit(ctx context.Context) (string, error) {
	labels, err := a.labels(ctx, a.Terminal)
	if err != nil {
		return "", err
	}
	if tab := labels["herdr.tab"]; tab != "" {
		return a.Socket + ":" + tab, nil
	}
	return a.Socket + ":" + a.Terminal, nil
}

// SplitPane は、mod に split を頼み、新しいペインの端末の番地を返す。opts.Size は使わない（herdr app の pane.split は大きさを受け付けない）。
// 起動するコマンドの環境変数と作業ディレクトリは、pane.split が受け付けないので、コマンドを sh と env で包んで渡す（herdrAppArgv）。
// mod がペインを割った後にフォーカスだけを移せなかったときは、新しい端末の番地と ErrFocusAfterSplit を包んだ誤りを返す。
//
// req: FR-316
func (a *HerdrApp) SplitPane(ctx context.Context, opts SplitOptions) (string, error) {
	if err := checkCommand(opts.Target, opts.Command); err != nil {
		return "", err
	}
	if err := checkPercent("size", opts.Size); err != nil {
		return "", err
	}
	req := herdrAppRequest{Op: "split", Argv: herdrAppArgv(opts.Command, a.Bin, opts.Target), Focus: opts.Focus}
	switch opts.Direction {
	case Right:
		req.Direction = "right"
	case Down:
		req.Direction = "down"
	case Left:
		req.Direction = "left"
	}
	rep, err := a.ask(ctx, opts.Target, req)
	if err != nil {
		return "", err
	}
	if rep.Terminal == "" {
		return "", errors.New("mux: the herdr app mod replied without a terminal")
	}
	if rep.FocusError != "" {
		return rep.Terminal, fmt.Errorf("%w: %s", ErrFocusAfterSplit, rep.FocusError)
	}
	return rep.Terminal, nil
}

// OpenPopup は ErrUnsupported を返す（herdr app の中の中央のポップアップは、client の画面の中に描く）。
func (*HerdrApp) OpenPopup(context.Context, PopupOptions) error { return ErrUnsupported }

// ClosePopup は ErrUnsupported を返す。
func (*HerdrApp) ClosePopup(context.Context, string) error { return ErrUnsupported }

// FocusPane は、mod に focus を頼み、from の端末のペインから pane の端末のペインへフォーカスを移す。
// 依頼は from の端末に書く。
//
// req: FR-316
func (a *HerdrApp) FocusPane(ctx context.Context, from, pane string) error {
	if from == "" {
		return ErrNoTarget
	}
	_, err := a.ask(ctx, from, herdrAppRequest{Op: "focus", Terminal: pane})
	return err
}

// ClosePane は、hc kill で端末を終わらせる。herdr app は、端末が終わるとそのペインを閉じる。
func (a *HerdrApp) ClosePane(ctx context.Context, pane string) error {
	_, err := a.run(ctx, "kill", pane)
	return err
}

// PaneFocused は、mod に focused を頼み、pane の端末のペインにフォーカスがあるかを返す。依頼は pane の端末に書く。
// hc は herdr app のフォーカスを知らないためである。
func (a *HerdrApp) PaneFocused(ctx context.Context, pane string) (bool, error) {
	rep, err := a.ask(ctx, pane, herdrAppRequest{Op: "focused", Terminal: pane})
	if err != nil {
		return false, err
	}
	return rep.Focused, nil
}

// ask は、端末 target に依頼 req を書き、mod の結果を待って返す。
// ctx が終わるまでに結果が無ければ、依頼のラベルを消して ErrHerdrAppNoReply を包んだ誤りを返す。
// 結果の Error が空でなければ、それを誤りにして返す。
func (a *HerdrApp) ask(ctx context.Context, target string, req herdrAppRequest) (herdrAppReply, error) {
	req.ID = strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(herdrAppSeq.Add(1), 10)
	b, err := json.Marshal(req)
	if err != nil {
		return herdrAppReply{}, err
	}
	reqKey, repKey := HerdrAppRequestPrefix+req.ID, HerdrAppReplyPrefix+req.ID
	if _, err := a.run(ctx, "label", target, reqKey+"="+string(b), HerdrAppPendingLabel+"=1"); err != nil {
		return herdrAppReply{}, err
	}
	marked := time.Now()
	tick := time.NewTicker(herdrAppPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			a.withdraw(ctx, target, reqKey, repKey)
			return herdrAppReply{}, fmt.Errorf("%w (%s %s): %w", ErrHerdrAppNoReply, req.Op, target, ctx.Err())
		case <-tick.C:
		}
		labels, err := a.labels(ctx, target)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			return herdrAppReply{}, err
		}
		v, ok := labels[repKey]
		if !ok {
			if time.Since(marked) >= herdrAppMark && labels[HerdrAppPendingLabel] != "1" {
				if _, err := a.run(ctx, "label", target, HerdrAppPendingLabel+"=1"); err == nil {
					marked = time.Now()
				}
			}
			continue
		}
		_, _ = a.run(ctx, "label", target, repKey+"=")
		var rep herdrAppReply
		if err := json.Unmarshal([]byte(v), &rep); err != nil {
			return herdrAppReply{}, fmt.Errorf("mux: invalid reply from the herdr app mod: %w", err)
		}
		if rep.Error != "" {
			return rep, fmt.Errorf("herdr app %s: %s", req.Op, rep.Error)
		}
		return rep, nil
	}
}

// withdraw は、待ちを打ち切った依頼を取り下げる。期限を過ぎた ctx では hc を呼べないので、短い期限で呼ぶ。
// 依頼と結果のラベルを消す。打ち切る直前に mod が処理を終えていて、結果に新しいペインの端末があれば、そのペインを hc kill で閉じる
// （待つ人のいない新しいペインを残さない。PR #441 のレビューの推奨 3）。
// mod が処理の途中なら、その後に書く結果とペインは残りうる（mod は pane.split の直前に依頼のラベルを読み直して、この窓を狭める）。
func (a *HerdrApp) withdraw(ctx context.Context, target, reqKey, repKey string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), herdrAppMark)
	defer cancel()
	if labels, err := a.labels(cctx, target); err == nil {
		var rep herdrAppReply
		if v, ok := labels[repKey]; ok && json.Unmarshal([]byte(v), &rep) == nil && rep.Terminal != "" && rep.Terminal != target {
			_, _ = a.run(cctx, "kill", rep.Terminal)
		}
	}
	claimKey := herdrAppClaimPrefix + strings.TrimPrefix(reqKey, HerdrAppRequestPrefix)
	_, _ = a.run(cctx, "label", target, reqKey+"=", repKey+"=", claimKey+"=")
}

// herdrAppClaimPrefix は、mod が依頼を引き受けた印のラベルの名前の前置きである（後ろに依頼の番号）。
// 本体は書かず、待ちを打ち切るときに依頼と一緒に消す（引き受けた mod が途中で止まっても印を残さない）。
const herdrAppClaimPrefix = "vi-imitation.claim."

// labels は、hc get で端末 terminal のラベルを読む。
func (a *HerdrApp) labels(ctx context.Context, terminal string) (map[string]string, error) {
	out, err := a.run(ctx, "get", terminal, "--json")
	if err != nil {
		return nil, err
	}
	var t struct {
		Labels map[string]string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(out), &t); err != nil {
		return nil, fmt.Errorf("hc get: invalid output: %w", err)
	}
	return t.Labels, nil
}

// run は、hc を args で起動し、標準出力を返す。
func (a *HerdrApp) run(ctx context.Context, args ...string) (string, error) {
	if !filepath.IsAbs(a.Bin) {
		// PATH を引き直して別の hc を呼ばない（lookPath）。
		return "", fmt.Errorf("hc %s: hc not found in PATH", args[0])
	}
	cmd := exec.CommandContext(ctx, a.Bin, args...) //nolint:gosec // 実行するのは hc で、引数は本体が組み立てたもの
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("hc %s: %w", args[0], err)
		}
		return "", fmt.Errorf("hc %s: %s: %w", args[0], msg, err)
	}
	return stdout.String(), nil
}

// herdrAppScript は、新しいペインの端末で動かす sh のスクリプトである。引数は、作業ディレクトリ（空なら移らない）、hc の実行ファイル、
// 依頼した元の端末の番地、起動するコマンドの順である。
// コマンドを動かし、終わったら、自分の端末（HC_TERMINAL_ID）が元の端末と違うときだけ hc kill で終わらせて、同じ終了コードで終わる。
// herdr app の pane.split が argv を利用者の対話のシェルの中で動かす（tab.new の argv はそうだと herdr app の AGENTS.md にある）なら、
// コマンドが終わってもシェルが残ってペインが閉じない。見比べの版のペインは、拡張機能が buffer/dispose で版を閉じると client が終わり
// （editor の bufferDisposeRequest）、ペインが閉じる前提なので、端末を終わらせてペインを確かに閉じる（PR #441 のレビューの必須 3）。
// argv がそのまま動く場合も、自分の端末を終わらせるだけなので害は無い。元の端末と同じ番地なら（HC_TERMINAL_ID を引き継いだなど）終わらせない。
const herdrAppScript = `[ -z "$1" ] || cd -- "$1" || exit; hc=$2; from=$3; shift 3; "$@"; s=$?; ` +
	`if [ -n "$HC_TERMINAL_ID" ] && [ "$HC_TERMINAL_ID" != "$from" ]; then "$hc" kill "$HC_TERMINAL_ID" >/dev/null 2>&1; fi; exit "$s"`

// herdrAppArgv は、c を、herdr app の pane.split の argv に渡す形にする。bin は hc の実行ファイル、from は依頼した元の端末の番地である。
// pane.split は作業ディレクトリと環境変数を受け付けないので、sh で c.Dir へ移り、env で c.Env を足してから c.Args を起動する（herdrAppScript）。
// 環境変数は名前の順に並べる（テストで比べられるように）。
func herdrAppArgv(c Command, bin, from string) []string {
	argv := []string{"sh", "-c", herdrAppScript, "sh", c.Dir, bin, from, "env"}
	names := make([]string, 0, len(c.Env))
	for name := range c.Env {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		argv = append(argv, name+"="+c.Env[name])
	}
	return append(argv, c.Args...)
}
