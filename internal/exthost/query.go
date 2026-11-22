package exthost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// Call は、拡張機能 id へリクエスト method を送り、応答を待つ（基本設計 5.4）。
// 成功の応答なら、結果の JSON を result（ポインター）へ読み込む。result が nil なら読み捨てる。
//
// 待つのは timeout の間と、ctx が終わるまでの短い方である。
// ctx は、利用者の中断（マクロの再生の中止など）で待つのをやめるために使う。
// timeout が 0 以下なら、何も送らずに誤りを返す。
//
// 拡張機能が未起動なら起動を始め、起動中なら稼働中になるのを待ち、同じ期限の中で起動と応答を待つ。
// 再起動待ちと無効の拡張機能へは送らず、すぐに [ErrUnavailable] を包んだ誤りを返す。
//
// timeout を超えたら [ErrTimeout] を、ctx が取り消されたら context.Canceled を包んだ誤りを返す。
// どちらの場合も、リクエストを送った後なら、拡張機能へ $/cancel を送り、後から届いた応答は捨てる（FR-17）。
// 拡張機能が誤りで応答したら、その *rpc.Error を包んだ誤りを返す。
//
// 呼んだ goroutine は、応答か期限まで止まる。
// イベントループは、待つ間も画面の描画と利用者の中断を処理する（基本設計 5.4）ので、Call を直接呼ばない。
// 待たずに戻り、結果をイベントで受け取る [Host.Send] を使うか、Call を別の goroutine で呼んで結果を自分のチャネルで受ける。
//
// req: FR-17
func (h *Host) Call(ctx context.Context, id, method string, params any, timeout time.Duration, result any) error {
	if timeout <= 0 {
		return fmt.Errorf("exthost: call %s: timeout must be positive, got %s", method, timeout)
	}
	ctx, cancel := h.queryContext(ctx, timeout)
	defer cancel()
	h.mu.Lock()
	if e, ok := h.exts[id]; ok {
		params = withheldParams(e, params)
	}
	h.mu.Unlock()
	conn, err := h.waitRunning(ctx, id)
	if err != nil {
		return fmt.Errorf("exthost: call %s on %q: %w", method, id, err)
	}
	if err := conn.Call(ctx, method, params, result); err != nil {
		return fmt.Errorf("exthost: call %s on %q: %w", method, id, h.classify(ctx, err))
	}
	return nil
}

// queryContext は、ctx と Host の終了の両方で終わり、timeout で期限を迎える ctx を作る。
func (h *Host) queryContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	stop := context.AfterFunc(h.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// ResolveMotion は、motion/resolve を問い合わせて応答を待つ。誤りの扱いと使い方の注意は [Host.Call] と同じである。
// 結果の Version と OK を確かめて適用するかどうかを決めるのは、呼び出し側である。
func (h *Host) ResolveMotion(ctx context.Context, id string, p ext.MotionResolveParams, timeout time.Duration) (ext.MotionResolveResult, error) {
	var res ext.MotionResolveResult
	err := h.Call(ctx, id, ext.MethodMotionResolve, p, timeout, &res)
	return res, err
}

// ResolveTextObject は、textObject/resolve を問い合わせて応答を待つ。誤りの扱いと使い方の注意は [Host.Call] と同じである。
func (h *Host) ResolveTextObject(ctx context.Context, id string, p ext.TextObjectResolveParams, timeout time.Duration) (ext.TextObjectResolveResult, error) {
	var res ext.TextObjectResolveResult
	err := h.Call(ctx, id, ext.MethodTextObjectResolve, p, timeout, &res)
	return res, err
}

// ApplyOperator は、operator/apply を問い合わせて応答を待つ。誤りの扱いと使い方の注意は [Host.Call] と同じである。
// 返ったトランザクションは、BaseVersion が現在のバージョン番号と一致した場合だけ適用する（FR-15）。
func (h *Host) ApplyOperator(ctx context.Context, id string, p ext.OperatorApplyParams, timeout time.Duration) (ext.OperatorApplyResult, error) {
	var res ext.OperatorApplyResult
	err := h.Call(ctx, id, ext.MethodOperatorApply, p, timeout, &res)
	return res, err
}

// ExecuteCommand は、command/execute を問い合わせて応答を待つ。誤りの扱いと使い方の注意は [Host.Call] と同じである。
// 返ったトランザクション（あれば）は、BaseVersion が現在のバージョン番号と一致した場合だけ適用する（FR-15）。
// コマンドの呼び出しを起動の契機にする拡張機能は、[Host.Activate] を呼ばなくても、この呼び出しで起動する。
//
// 結果の Open があり、[ext.EditorOpenParams.Validate] を通らなければ、結果と一緒に誤りを返す。呼び出し側は操作の失敗として扱う。
// [Host.Send] で command/execute を送った場合は、イベントループが [ValidateCommandResult] で同じ検査をする。
func (h *Host) ExecuteCommand(ctx context.Context, id string, p ext.CommandExecuteParams, timeout time.Duration) (ext.CommandExecuteResult, error) {
	var res ext.CommandExecuteResult
	if err := h.Call(ctx, id, ext.MethodCommandExecute, p, timeout, &res); err != nil {
		return res, err
	}
	if err := ValidateCommandResult(&res); err != nil {
		return res, fmt.Errorf("exthost: call %s on %q: %w", ext.MethodCommandExecute, id, err)
	}
	return res, nil
}

// ValidateCommandResult は、command/execute の結果の Open が [ext.EditorOpenParams.Validate] を通るかを検査する。
// Open が無ければ nil を返す。
// editor/open のリクエストは Host が検査してからイベントにするが、command/execute の結果の Open は Host の外で読まれうるので、
// イベントループは結果の Open を使う前にこれを呼ぶ。
func ValidateCommandResult(res *ext.CommandExecuteResult) error {
	if res.Open == nil {
		return nil
	}
	if err := res.Open.Validate(); err != nil {
		return fmt.Errorf("invalid open in command result: %w", err)
	}
	return nil
}

// SendOptions は、[Host.Send] の設定である。
type SendOptions struct {
	// Tag は、結果の [QueryResult] の Tag にそのまま入れる値である。
	// イベントループは、問い合わせた時点のバージョン番号とカーソル位置を入れておき、結果が届いたときに照合する（FR-18）。
	Tag any
	// Timeout は、起動と応答を待つ上限である。0 以下なら [Config.SendTimeout] を使う。
	// 期限 0 の設定（FR-18）でも、応答しない問い合わせの待ち受けが溜まらないように、この上限で打ち切る。
	Timeout time.Duration
	// StartTimeout は、起動を含む問い合わせ（Send を呼んだ時点で拡張機能が未起動か起動中のもの）で、Timeout の代わりに使う期限である（基本設計 5.2）。
	// 大きなバッファを開いた直後の最初の問い合わせが、全文の送信を含む起動の時間で失敗しないようにする（#43 の裁定 1-b）。
	// Timeout（0 以下なら [Config.SendTimeout]）より短いか 0 以下なら使わない。
	// 稼働中の拡張機能への問い合わせには使わない。
	StartTimeout time.Duration
}

// Query は、[Host.Send] で送った問い合わせである。
type Query struct {
	cancel context.CancelFunc
}

// Cancel は、問い合わせを取り消す。リクエストを送った後なら、拡張機能へ $/cancel を送る。
// 結果がまだ決まっていなければ、context.Canceled を包んだ Err の [QueryResult] を渡す。
// 新しい問い合わせで古い問い合わせが要らなくなったときに呼ぶ。何度呼んでもよい。
func (q *Query) Cancel() {
	q.cancel()
}

// Send は、拡張機能 id へリクエスト method を送り、応答を待たずに戻る。
// 結果は、決まった時点で [QueryResult] のイベントとして [Host.Events] へ 1 回だけ渡す。
// 期限が正の設定（FR-17）でも、イベントループは Send を使えば、待つ間も描画と中断を処理できる。
//
// 拡張機能が未起動なら起動を始め、稼働中になってから送る。
// 拡張機能が無いか、再起動待ちか無効なら、何も送らずに誤りを返し、イベントも渡さない。
// 期限を超えたら [ErrTimeout] を包んだ Err、起動に失敗するか応答の前に拡張機能が終わったら [ErrUnavailable] を包んだ Err の
// QueryResult を渡す。期限を超えたか取り消した問い合わせへ後から届いた応答は捨てる。
//
// req: FR-18
func (h *Host) Send(id, method string, params any, opts SendOptions) (*Query, error) {
	return h.send(id, method, params, opts, h.cfg.SendTimeout, h.ensureStartedLocked)
}

// send は、Send の本体である。opts.Timeout が 0 以下なら defaultTimeout を使う。
// check は、送る前に Host.mu を持ったまま呼び、誤りを返したら送らない。
func (h *Host) send(id, method string, params any, opts SendOptions, defaultTimeout time.Duration, check func(*extension) error) (*Query, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	h.mu.Lock()
	e, err := h.lookupLocked(id)
	if err == nil {
		params = withheldParams(e, params)
		err = check(e)
		starting := e.state == StateNotStarted || e.state == StateStarting || h.kickedLocked(e)
		if err == nil && starting && opts.StartTimeout > timeout {
			timeout = opts.StartTimeout
		}
	}
	if err != nil {
		h.mu.Unlock()
		return nil, fmt.Errorf("exthost: send %s to %q: %w", method, id, err)
	}
	h.wg.Add(1)
	h.mu.Unlock()

	ctx, cancel := h.queryContext(context.Background(), timeout)
	var once sync.Once
	q := &Query{cancel: func() { once.Do(cancel) }}
	go func() {
		defer h.wg.Done()
		defer q.cancel()
		defer h.recoverPanic()
		qr := &QueryResult{Extension: id, Method: method, Tag: opts.Tag}
		qr.Err = h.sendAndWait(ctx, id, method, params, &qr.result)
		if qr.Err != nil {
			qr.Err = fmt.Errorf("exthost: send %s to %q: %w", method, id, qr.Err)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.closing {
			_ = h.pushLocked(qr, nil)
		}
	}()
	return q, nil
}

func (h *Host) sendAndWait(ctx context.Context, id, method string, params any, result *json.RawMessage) error {
	conn, err := h.waitRunning(ctx, id)
	if err != nil {
		return err
	}
	if err := conn.Call(ctx, method, params, result); err != nil {
		return h.classify(ctx, err)
	}
	return nil
}

// ensureStartedLocked は、未起動の拡張機能を起動する。停止中か再起動待ちか無効なら誤りを返す。
func (h *Host) ensureStartedLocked(e *extension) error {
	switch e.state {
	case StateNotStarted:
		h.startLocked(e)
	case StateStarting, StateRunning:
	case StateRestartWait:
		// 起動の契機 socket の拡張機能が、異常の後の待ち時間を過ぎて接続を待っているなら、接続を待たずに起動する（基本設計 6.2）。
		if !h.kickLocked(e, true) {
			return fmt.Errorf("%w: state %s", ErrUnavailable, e.state)
		}
	case StateStopping, StateDisabled:
		return fmt.Errorf("%w: state %s", ErrUnavailable, e.state)
	}
	return nil
}

// waitRunning は、拡張機能を必要なら起動し、稼働中になるまで待って、その接続を返す。
// 待つ間に停止中か再起動待ちか無効になったら、[ErrUnavailable] を包んだ誤りを返す。
func (h *Host) waitRunning(ctx context.Context, id string) (*rpc.Conn, error) {
	h.mu.Lock()
	e, err := h.lookupLocked(id)
	if err == nil {
		err = h.ensureStartedLocked(e)
	}
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}
	for {
		switch e.state {
		case StateRunning:
			conn := e.run.conn
			h.mu.Unlock()
			return conn, nil
		case StateRestartWait:
			if !h.kickedLocked(e) {
				h.mu.Unlock()
				return nil, fmt.Errorf("%w: state %s", ErrUnavailable, StateRestartWait)
			}
		case StateStopping, StateDisabled:
			st := e.state
			h.mu.Unlock()
			return nil, fmt.Errorf("%w: state %s", ErrUnavailable, st)
		case StateNotStarted, StateStarting:
		}
		changed := e.changed
		h.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, h.classify(ctx, ctx.Err())
		}
		h.mu.Lock()
		if h.closing {
			h.mu.Unlock()
			return nil, ErrClosed
		}
	}
}

// classify は、通信の誤りを、このパッケージの誤りの種類で包み直す。
func (h *Host) classify(ctx context.Context, err error) error {
	switch {
	case h.ctx.Err() != nil:
		return fmt.Errorf("%w: %w", ErrClosed, err)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", ErrTimeout, err)
	case errors.Is(err, rpc.ErrClosed):
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}
