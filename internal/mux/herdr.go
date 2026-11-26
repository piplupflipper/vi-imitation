package mux

import (
	"bufio"
	"context"
	_ "embed" // HerdrPluginManifest を埋め込む
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"strconv"
	"sync/atomic"
)

// HerdrPluginID と HerdrPluginEntrypoint は、本体が同梱する herdr のプラグイン（HerdrPluginManifest）の ID と、
// ポップアップと分割したペインを開くときに使う entrypoint（[[panes]] の id）である。
const (
	HerdrPluginID         = "vi-imitation"
	HerdrPluginEntrypoint = "run"
)

// HerdrPluginNotFound は、同梱のプラグインを herdr に登録していないときに、plugin.pane.open が返す誤りの種類（HerdrError.Code）である
// （2026-11-19 に herdr 0.9.0 で、登録していない ID を渡して確かめた）。
const HerdrPluginNotFound = "plugin_not_found"

// HerdrCommandEnv は、herdr のプラグインのペインで起動するコマンドを渡す環境変数の名前である。
// 値は、Command.Args をシェルの引用で囲んでつないだ 1 つの文字列で、プラグインの command が eval して exec する。
const HerdrCommandEnv = "VI_IMITATION_MUX_COMMAND"

// HerdrPluginManifest は、本体が同梱する herdr のプラグインの宣言ファイル（herdr-plugin.toml）の中身である（雛形）。
// herdr でポップアップと分割したペインを開くには、この宣言ファイルを置いたディレクトリを herdr plugin link で登録しておく。
// make dist が配布物の herdr-plugin/ に置き、利用者が herdr plugin link <配布物>/herdr-plugin で 1 回登録する（基本設計 1.3。#244）。
// 登録していなければ、plugin.pane.open は HerdrPluginNotFound の誤りを返す。
//
//go:embed herdr-plugin/herdr-plugin.toml
var HerdrPluginManifest []byte

// Herdr は、herdr 用の実装である（基本設計 1.3）。
//
// herdr の socket API（protocol 22。herdr api schema --json で確かめられる）へ、1 回の操作ごとにソケットへ繋ぎ、
// 改行で区切った JSON のリクエストを 1 つ送って、応答を 1 行読む。
// CLI（herdr pane など）を使わないのは、CLI にペインの ID を指定してフォーカスを移す操作が無いためである（herdr 0.9.0）。
//
// ポップアップと、コマンドを起動する分割したペインは、同梱するプラグイン（HerdrPluginManifest）の entrypoint を
// plugin.pane.open で開く。socket API の pane.split は、起動するコマンドを受け付けないためである。
type Herdr struct {
	// Socket は、herdr の socket API のソケットのパス（環境変数 HERDR_SOCKET_PATH）である。
	Socket string
	// Pane は、この層を作ったプロセスがいるペインの ID（環境変数 HERDR_PANE_ID）である。
	// ペインを別のワークスペースへ動かすと ID は変わるが、herdr は古い ID でもそのペインを引ける（2026-11-18 に herdr 0.9.0 で確かめた）。
	Pane string
}

var _ Mux = (*Herdr)(nil)

// HerdrError は、herdr の socket API が返した誤りである。
type HerdrError struct {
	// Method は、誤りを返したリクエストのメソッドである。
	Method string
	// Code は、誤りの種類（pane_not_found、invalid_params など）である。
	Code string
	// Message は、誤りの説明である。
	Message string
}

func (e *HerdrError) Error() string {
	return fmt.Sprintf("herdr %s: %s: %s", e.Method, e.Code, e.Message)
}

// Kind は KindHerdr を返す。
func (*Herdr) Kind() Kind { return KindHerdr }

// CurrentPane は、HERDR_PANE_ID のペインを pane.get で引き、今の ID を返す。
// HERDR_PANE_ID は、ペインを別のワークスペースへ動かすと古くなる。古い ID のままでも herdr は引けるが、
// ペインどうしを ID で比べられるように、今の ID に直して返す。
func (h *Herdr) CurrentPane(ctx context.Context) (string, error) {
	p, err := h.pane(ctx, h.Pane)
	if err != nil {
		return "", err
	}
	return p.PaneID, nil
}

// Unit は、HERDR_PANE_ID のペインのタブを、herdr のソケットのパスとタブの ID をコロンでつないだ文字列で返す（基本設計 1.2 の規則 3）。
//
// タブの ID は、環境変数 HERDR_TAB_ID ではなく、pane.get が返すペインの tab_id から得る。
// HERDR_TAB_ID はペインを起動したときの値のままで、ペインやタブを別のワークスペースへ動かすと、
// herdr が引けない古い ID になる（2026-11-18 に herdr 0.9.0 で、tab.get が tab_not_found を返した）。
func (h *Herdr) Unit(ctx context.Context) (string, error) {
	p, err := h.pane(ctx, h.Pane)
	if err != nil {
		return "", err
	}
	return h.Socket + ":" + p.TabID, nil
}

// SplitPane は、同梱するプラグインの entrypoint を、placement を split にして plugin.pane.open で開く。
// opts.Size は使わない（herdr 0.9.0 の plugin.pane.open は、split のときに width と height を受け付けない）。
func (h *Herdr) SplitPane(ctx context.Context, opts SplitOptions) (string, error) {
	if err := checkCommand(opts.Target, opts.Command); err != nil {
		return "", err
	}
	if err := checkPercent("size", opts.Size); err != nil {
		return "", err
	}
	params := h.openParams("split", opts.Command)
	params["target_pane_id"] = opts.Target
	params["focus"] = opts.Focus
	switch opts.Direction {
	case Right, Left:
		params["direction"] = "right"
	case Down:
		params["direction"] = "down"
	}
	var res struct {
		PluginPane struct {
			Pane herdrPane `json:"pane"`
		} `json:"plugin_pane"`
	}
	if err := h.call(ctx, "plugin.pane.open", params, &res); err != nil {
		return "", err
	}
	pane := res.PluginPane.Pane.PaneID
	if pane == "" {
		return "", errors.New("herdr plugin.pane.open: no pane id in the response")
	}
	if opts.Direction == Left {
		if err := h.swapLeft(ctx, pane, opts.Target, opts.Focus); err != nil {
			return "", err
		}
	}
	return pane, nil
}

// swapLeft は、target の右に開いた pane を、pane.swap で target と入れ替え、元のペインの左に置く（Direction の Left）。
// herdr 0.9.0 の plugin.pane.open と pane.split は、分割の向きに right と down しか受け付けない（API の schema の SplitDirection）。
// 入れ替えた後のフォーカスは、focus を false で開いても左の新しいペインにあった（2026-11-26 に herdr 0.9.0 で確かめた）。
// このため入れ替えた後に、focus なら pane へ、そうでなければ target へ、pane.focus でフォーカスを移し直す。
// 入れ替えられなかったら、開いた pane を閉じて誤りを返す（向きの違うペインを残さない）。
func (h *Herdr) swapLeft(ctx context.Context, pane, target string, focus bool) error {
	err := h.call(ctx, "pane.swap", map[string]any{"source_pane_id": pane, "target_pane_id": target}, nil)
	if err != nil {
		if cerr := h.ClosePane(ctx, pane); cerr != nil {
			return errors.Join(err, cerr)
		}
		return err
	}
	focused := target
	if focus {
		focused = pane
	}
	return h.call(ctx, "pane.focus", map[string]any{"pane_id": focused}, nil)
}

// OpenPopup は、同梱するプラグインの entrypoint を、placement を popup にして plugin.pane.open で開く。
// opts.KeepOnFailure なら、コマンドを keepOnFailure で包み、0 でない終了コードで終わったときにキーを待ってから閉じる。
// herdr 0.9.0 は、ポップアップの target_pane_id を受け付けず（invalid_params）、アクティブなペインの上に開く。
// このため opts.Target は使わない。元の client のペインでキーを受けて開くときは、ふつうそのペインがアクティブだが、
// キーを受けてからこの呼び出しが herdr に届くまでの間に利用者が別のタブへ移ると、移った先に開く。
// ポップアップが既に開いていると herdr は ui_busy を返すので、ErrPopupBusy に包んで返す。
func (h *Herdr) OpenPopup(ctx context.Context, opts PopupOptions) error {
	if err := checkCommand(opts.Target, opts.Command); err != nil {
		return err
	}
	if err := checkPercent("width", opts.Width); err != nil {
		return err
	}
	if err := checkPercent("height", opts.Height); err != nil {
		return err
	}
	c := opts.Command
	if opts.KeepOnFailure {
		c = keepOnFailure(c)
	}
	params := h.openParams("popup", c)
	if opts.Width > 0 {
		params["width"] = strconv.Itoa(opts.Width) + "%"
	}
	if opts.Height > 0 {
		params["height"] = strconv.Itoa(opts.Height) + "%"
	}
	err := h.call(ctx, "plugin.pane.open", params, nil)
	if he, ok := errors.AsType[*HerdrError](err); ok && he.Code == "ui_busy" {
		return fmt.Errorf("%w: %w", ErrPopupBusy, err)
	}
	return err
}

// ClosePopup は、popup.close で、開いているポップアップを、誰が開いたものかを問わず閉じる。target は使わない。
// herdr 0.9.0 の popup.close は引数を取らず、plugin.pane.open はポップアップのペインの ID を返さず、
// ポップアップは pane.list にも出ない。このため、この層が開いたものかを確かめる手段が無く、
// 利用者が入れた他のプラグイン（starkeep など）のポップアップも閉じうる（Mux.ClosePopup の doc）。
// ポップアップが開いていなければ（popup_not_open）、閉じたものとして nil を返す。
func (h *Herdr) ClosePopup(ctx context.Context, _ string) error {
	err := h.call(ctx, "popup.close", map[string]any{}, nil)
	if he, ok := errors.AsType[*HerdrError](err); ok && he.Code == "popup_not_open" {
		return nil
	}
	return err
}

// FocusPane は、pane.focus でペインへフォーカスを移す。ペインが別のタブにあれば、そのタブへ移る。
// herdr の画面は 1 つなので、from は空でないことだけを確かめて使わない。
func (h *Herdr) FocusPane(ctx context.Context, from, pane string) error {
	if from == "" {
		return ErrNoTarget
	}
	return h.call(ctx, "pane.focus", map[string]any{"pane_id": pane}, nil)
}

// ClosePane は、pane.close でペインを閉じる。
func (h *Herdr) ClosePane(ctx context.Context, pane string) error {
	return h.call(ctx, "pane.close", map[string]any{"pane_id": pane}, nil)
}

// herdrPane は、socket API の PaneInfo のうち、この層が使う項目である。
type herdrPane struct {
	PaneID  string `json:"pane_id"`
	TabID   string `json:"tab_id"`
	Focused bool   `json:"focused"`
}

// PaneFocused は、pane.get の focused を返す（herdr 0.9.0 の pane.get と pane.split の応答の PaneInfo にある項目）。
func (h *Herdr) PaneFocused(ctx context.Context, pane string) (bool, error) {
	p, err := h.pane(ctx, pane)
	if err != nil {
		return false, err
	}
	return p.Focused, nil
}

// pane は、pane.get でペインを引く。
func (h *Herdr) pane(ctx context.Context, id string) (herdrPane, error) {
	var res struct {
		Pane herdrPane `json:"pane"`
	}
	if err := h.call(ctx, "pane.get", map[string]any{"pane_id": id}, &res); err != nil {
		return herdrPane{}, err
	}
	if res.Pane.PaneID == "" || res.Pane.TabID == "" {
		return herdrPane{}, errors.New("herdr pane.get: no pane id or tab id in the response")
	}
	return res.Pane, nil
}

// openParams は、plugin.pane.open の引数のうち、プラグイン、placement、起動するコマンドの部分を作る。
func (*Herdr) openParams(placement string, c Command) map[string]any {
	env := make(map[string]string, len(c.Env)+1)
	maps.Copy(env, c.Env)
	env[HerdrCommandEnv] = shellJoin(c.Args)
	params := map[string]any{
		"plugin_id":  HerdrPluginID,
		"entrypoint": HerdrPluginEntrypoint,
		"placement":  placement,
		"env":        env,
	}
	if c.Dir != "" {
		params["cwd"] = c.Dir
	}
	return params
}

// herdrRequestID は、リクエストの ID の連番である。応答との対応を確かめるのに使う。
var herdrRequestID atomic.Uint64

// call は、socket API へリクエストを 1 つ送り、応答の result を out へ読む。out が nil なら result を読まない。
// ctx の期限は、ソケットの読み書きの期限にする。
func (h *Herdr) call(ctx context.Context, method string, params any, out any) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", h.Socket)
	if err != nil {
		return fmt.Errorf("herdr %s: %w", method, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		if err = conn.SetDeadline(deadline); err != nil {
			return fmt.Errorf("herdr %s: %w", method, err)
		}
	}
	// ctx が取り消されたら、読み書きを止める。
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	id := "vi-imitation:" + strconv.FormatUint(herdrRequestID.Add(1), 10)
	req, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return fmt.Errorf("herdr %s: %w", method, err)
	}
	if _, err = conn.Write(append(req, '\n')); err != nil {
		return fmt.Errorf("herdr %s: %w", method, err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("herdr %s: %w", method, ctxErr)
		}
		return fmt.Errorf("herdr %s: %w", method, err)
	}
	var resp struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("herdr %s: invalid response: %w", method, err)
	}
	if resp.ID != id {
		return fmt.Errorf("herdr %s: response id %q does not match request id %q", method, resp.ID, id)
	}
	if resp.Error != nil {
		return &HerdrError{Method: method, Code: resp.Error.Code, Message: resp.Error.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("herdr %s: invalid result: %w", method, err)
	}
	return nil
}
