package exthost

import (
	"context"
	"fmt"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
)

// 保存の流れ（基本設計 6.7）は、イベントループが次の順に進める。
//
//  1. [Host.SaveHookExtensions] で、宣言ファイルの saveHook が true の稼働中の拡張機能を得る。
//     その順に 1 つずつ [Host.SendWillSave] で buffer/willSave を問い合わせる。未起動の拡張機能は起動しない
//  2. 結果の Transaction があり、BaseVersion が現在のバージョン番号と一致すれば適用し、[Host.ChangeBuffer] を呼ぶ。
//     次の拡張機能には、適用した後のバージョン番号で問い合わせる
//  3. 期限を超えた拡張機能は待たずに次へ進み（Host が $/cancel を送る）、利用者に 1 行のメッセージで知らせる
//  4. ファイルへ書き出し、[Host.DidSave] で稼働中の拡張機能すべてへ buffer/didSave を送る
//
// 1 から 3 の間は、すべての拡張機能から届いたリクエストを処理せずに取っておく（[Event] の doc）。

// SaveHookExtensions は、宣言ファイルの saveHook が true で、稼働中の拡張機能の ID を、[New] に渡した順に返す。
// 稼働中のすべての拡張機能は、拡張機能へ送っていないもの（不正な UTF-8 を含むもの）を除く開いているバッファの複製を持つので、バッファを問わない。
// 送っていないバッファには、[Host.SendWillSave] が送らずに誤りを返す。
func (h *Host) SaveHookExtensions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var ids []string
	for _, e := range h.order {
		if e.spec.Manifest.SaveHook && e.state == StateRunning {
			ids = append(ids, e.spec.Manifest.ID)
		}
	}
	return ids
}

// WindowEventExtensions は、宣言ファイルの windowEvents が true で、稼働中の拡張機能の ID を、[New] に渡した順に返す
// （ext.MethodWindowChange の doc。FR-235）。イベントループは、これらへ [Host.Notify] で window/change を送る。
// 稼働中でない拡張機能は返さないので、窓の変化は起動の契機にならない。
//
// req: FR-235
func (h *Host) WindowEventExtensions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var ids []string
	for _, e := range h.order {
		if e.spec.Manifest.WindowEvents && e.state == StateRunning {
			ids = append(ids, e.spec.Manifest.ID)
		}
	}
	return ids
}

// SendWillSave は、拡張機能 id へ buffer/willSave を送り、応答を待たずに戻る。
// 結果は、[Host.Send] と同じく [QueryResult] のイベントで受け取り、[ext.BufferWillSaveResult] へ読む。
// opts.Timeout が 0 以下なら [Config.WillSaveTimeout] を使う。
// 期限を超えたら、Host が $/cancel を送り、ErrTimeout を包んだ Err の QueryResult を渡す。
//
// Send と違い、稼働中でない拡張機能は起動せず、何も送らずに [ErrUnavailable] を包んだ誤りを返す。
// 宣言ファイルの saveHook が false の拡張機能と、開いていないバッファ（[ErrBufferState]）にも送らない。
// 拡張機能へ送っていないバッファ（不正な UTF-8 を含むもの。基本設計 4.1、5.3）にも送らず、[ErrUnavailable] を包んだ誤りを返す。
func (h *Host) SendWillSave(id string, p ext.BufferWillSaveParams, opts SendOptions) (*Query, error) {
	return h.send(id, ext.MethodBufferWillSave, p, opts, h.cfg.WillSaveTimeout, h.willSaveCheck(p.Buffer))
}

// WillSave は、拡張機能 id へ buffer/willSave を問い合わせて応答を待つ。
// 送る条件は [Host.SendWillSave] と、誤りの扱いと使い方の注意は [Host.Call] と同じである。
// timeout が 0 以下なら [Config.WillSaveTimeout] を使う。
func (h *Host) WillSave(ctx context.Context, id string, p ext.BufferWillSaveParams, timeout time.Duration) (ext.BufferWillSaveResult, error) {
	var res ext.BufferWillSaveResult
	if timeout <= 0 {
		timeout = h.cfg.WillSaveTimeout
	}
	h.mu.Lock()
	e, err := h.lookupLocked(id)
	if err == nil {
		err = h.willSaveCheck(p.Buffer)(e)
	}
	h.mu.Unlock()
	if err != nil {
		return res, fmt.Errorf("exthost: call %s on %q: %w", ext.MethodBufferWillSave, id, err)
	}
	err = h.Call(ctx, id, ext.MethodBufferWillSave, p, timeout, &res)
	return res, err
}

// willSaveCheck は、buffer/willSave を送ってよいかを、Host.mu を持ったまま調べる関数を返す。
func (h *Host) willSaveCheck(buf ext.BufferID) func(*extension) error {
	return func(e *extension) error {
		switch {
		case !e.spec.Manifest.SaveHook:
			return fmt.Errorf("%w: saveHook is false", ErrUnavailable)
		case e.state != StateRunning:
			return fmt.Errorf("%w: state %s", ErrUnavailable, e.state)
		}
		d, ok := h.docs[buf]
		if !ok {
			return fmt.Errorf("%w: buffer %d is not open", ErrBufferState, buf)
		}
		if d.withheld() {
			// 拡張機能は複製を持っていないので、保存の前の編集を計算できない（基本設計 4.1、5.3）。
			return fmt.Errorf("%w: buffer %d contains invalid UTF-8 and is not sent to extensions", ErrUnavailable, buf)
		}
		return nil
	}
}

// Notify は、拡張機能 id が稼働中なら、通知 method を送る。稼働中でなければ何も送らずに nil を返す。
// ポップアップの利用者の操作の知らせ（view/closed、view/select、input/change、view/key。基本設計 5.3）に使う。
// 稼働中でない拡張機能は、そのポップアップを開いたプロセスではないので、知らせる相手がいないためである。
// 知らない拡張機能なら誤りを返す。
func (h *Host) Notify(id, method string, params any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, err := h.lookupLocked(id)
	if err != nil {
		return err
	}
	if e.state == StateRunning && e.run != nil {
		h.notifyLocked(id, e.run, method, params)
	}
	return nil
}

// DidSave は、バッファをファイルへ書き出したことを知らせ、稼働中の拡張機能すべてへ buffer/didSave を送る。
// p.Version は、Host の知っている現在のバージョン番号と一致しなければならない（書き出す前の編集は [Host.ChangeBuffer] で知らせておく）。
// 別名で保存した場合は、先に [Host.UpdateBuffer] でパスを知らせる。
// 開いていないバッファか、バージョン番号が食い違うなら、何も送らずに [ErrBufferState] を包んだ誤りを返す。
// 拡張機能へ送っていないバッファ（不正な UTF-8 を含むもの）なら、何も送らずに nil を返す。
func (h *Host) DidSave(p ext.BufferDidSaveParams) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return ErrClosed
	}
	d, ok := h.docs[p.Buffer]
	if !ok {
		return fmt.Errorf("%w: buffer %d is not open", ErrBufferState, p.Buffer)
	}
	if d.version != p.Version {
		return fmt.Errorf("%w: buffer %d: saved version %d, host has %d", ErrBufferState, p.Buffer, p.Version, d.version)
	}
	if d.withheld() {
		// 拡張機能へ送っていないバッファ（不正な UTF-8 を含むもの）は、拡張機能が扱わないので知らせない。
		return nil
	}
	h.broadcastLocked(ext.MethodBufferDidSave, p)
	return nil
}
