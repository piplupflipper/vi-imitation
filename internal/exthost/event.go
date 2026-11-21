package exthost

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// Event は、[Host.Events] からイベントループへ渡すイベントである。
// 値は次の型のポインターのいずれかである。
//
//   - 拡張機能からのリクエスト: [EditRequest]、[EditorStateRequest]、[EditorOpenRequest]、[BufferSaveRequest]、
//     [DiagnosticListRequest]、[ChoiceAskRequest]、[PopupOpenRequest]、[BufferCreateRequest]、[BufferUpdateRequest]、
//     [BufferDisposeRequest]、[DiffStartRequest]、[PaneOpenRequest]、[EditorListRequest]、[EditorExecuteRequest]、
//     [OptionSetRequest]、[OptionGetRequest]
//   - 拡張機能からの通知: [DecorationSet]、[MessageShow]、[DiagnosticSet]、[SignSet]、[PopupShow]、[OutputAppend]、
//     [ViewUpdate]、[ViewClose]、[BandSet]
//   - Host が作るもの: [StateChange]、[QueryResult]
//
// # 拡張機能からのリクエストの扱い
//
// リクエストのイベントは、イベントループが 1 件ずつ処理し、Reply か ReplyError で 1 回だけ応答する。
// 応答しないと、拡張機能の側の呼び出しは応答を待ち続ける。
// 拡張機能が既に終わっていれば、応答は誤りを返す（返しても害は無い）。
//
// イベントループは、ある拡張機能へのキー入力ごとの問い合わせ（基本設計 5.4）か command/execute の結果を待っている間は、
// その拡張機能から届いたリクエストを処理せずに取っておき、待ちが終わってから処理する。
// 保存の流れで buffer/willSave の応答を待っている間は、すべての拡張機能から届いたリクエストを取っておく（基本設計 5.3）。
// Host はこの待たせ方をしない（イベントを届いた順に渡すだけである）ので、取っておくのはイベントループの責務である。
//
// 取っておいたリクエストを処理する前に、イベントループはその Done を調べ、閉じていれば応答せずに捨てる。
// Done は、拡張機能がリクエストを取り消したとき（$/cancel）と、拡張機能のプロセスが終わって接続が閉じたときに閉じる。
// Host は、異常と判断した拡張機能について、接続を閉じてから [StateChange] を積む。
// このため、StateChange を受け取った時点で、その拡張機能から取っておいたリクエストの Done はすべて閉じている。
// これで、異常と判断した拡張機能の編集を後から適用しない（まだ渡していないイベントは Host が捨てる）。
// 表示中の choice/ask も、Done が閉じたら取り下げる。
type Event interface {
	isEvent()
}

// replyOnce は、リクエストのイベントが持つ応答の関数と、取り消しの知らせである。
type replyOnce struct {
	reply rpc.Replier
	done  <-chan struct{}
}

// Done は、拡張機能がこのリクエストを取り消したか、拡張機能の接続が閉じたか、応答した後に閉じるチャネルを返す。
// 閉じた後の応答は、拡張機能に届かないか、届いても使われない。イベントループの使い方は [Event] の doc にある。
func (r *replyOnce) Done() <-chan struct{} {
	return r.done
}

// ReplyError は、拡張機能へ失敗の応答を返す。err が *rpc.Error を包んでいればそのコードを、そうでなければ内部の誤りのコードを使う。
// 開けないファイルへの editor/open のように、result の形で表せない失敗に使う。
// Reply と合わせて 1 回だけ応答でき、2 回目以降の呼び出しは何も送らずに誤りを返す。
func (r *replyOnce) ReplyError(err error) error {
	return r.reply(nil, err)
}

// EditRequest は、拡張機能からの buffer/edit のリクエストである（基本設計 5.3）。
//
// イベントループは、Params.Transaction の BaseVersion がバッファの現在のバージョン番号と一致した場合だけ適用し（FR-15）、
// 結果を [EditRequest.Reply] で返す。応答しないと、拡張機能の側の呼び出しは応答を待ち続ける。
type EditRequest struct {
	// Extension は、要求した拡張機能の ID である。
	Extension string
	Params    ext.BufferEditParams

	reply rpc.Replier
	done  <-chan struct{}
}

// Done は、拡張機能がこのリクエストを取り消したか、拡張機能の接続が閉じたか、応答した後に閉じるチャネルを返す。
// 使い方は [Event] の doc にある。
func (r *EditRequest) Done() <-chan struct{} {
	return r.done
}

// Reply は、拡張機能へ buffer/edit の結果を返す。2 回目以降の呼び出しは何も送らずに誤りを返す。
// 拡張機能が既に終わっていれば誤りを返す（返しても害は無い）。
func (r *EditRequest) Reply(result ext.BufferEditResult) error {
	return r.reply(result, nil)
}

// ReplyError は、拡張機能へ失敗の応答を返す。使い方は他のリクエストのイベントの ReplyError と同じである。
func (r *EditRequest) ReplyError(err error) error {
	return r.reply(nil, err)
}

// EditorStateRequest は、拡張機能からの editor/state のリクエストである（基本設計 6.7）。
// イベントループは、カーソルのあるバッファ、カーソル位置、モード、選択範囲、開いているバッファの一覧を Reply で返す。
type EditorStateRequest struct {
	// Extension は、要求した拡張機能の ID である。
	Extension string
	Params    ext.EditorStateParams
	replyOnce
}

// Reply は、拡張機能へ editor/state の結果を返す。
func (r *EditorStateRequest) Reply(result ext.EditorStateResult) error {
	return r.reply(result, nil)
}

// EditorOpenRequest は、拡張機能からの editor/open のリクエストである（基本設計 6.7）。
// Params は [ext.EditorOpenParams.Validate] を通ったものである（通らなければ Host が誤りを応答し、イベントにしない）。
//
// イベントループは、まだ開いていないファイルなら開いて [Host.OpenBuffer] を呼んでから Reply する。
// OpenBuffer の buffer/open と応答は同じ接続で送るので、拡張機能には buffer/open が応答より先に届く。
// 拡張機能の側は、応答を受け取った後に ext/server の WaitBuffer で複製を待つ。
// 開いたバッファが拡張機能へ送っていないもの（不正な UTF-8 を含むもの）なら、Reply は応答より先に buffer/close を送り、待ちを誤りで終わらせる。
type EditorOpenRequest struct {
	Extension string
	Params    ext.EditorOpenParams
	replyOnce
}

// Reply は、拡張機能へ editor/open の結果を返す。
func (r *EditorOpenRequest) Reply(result ext.EditorOpenResult) error {
	return r.reply(result, nil)
}

// BufferSaveRequest は、拡張機能からの buffer/save のリクエストである（基本設計 6.7）。
// イベントループは、利用者の保存と同じ保存の流れで書き出す。
// Params.SkipHooks が true なら buffer/willSave を飛ばす。書き出した後に [Host.DidSave] を呼ぶ。
type BufferSaveRequest struct {
	Extension string
	Params    ext.BufferSaveParams
	replyOnce
}

// Reply は、拡張機能へ buffer/save の結果を返す。
func (r *BufferSaveRequest) Reply(result ext.BufferSaveResult) error {
	return r.reply(result, nil)
}

// DiagnosticListRequest は、拡張機能からの diagnostic/list のリクエストである（基本設計 6.7）。
// イベントループは、受け取り済みの診断を、編集に合わせてずらした位置で返す。
type DiagnosticListRequest struct {
	Extension string
	Params    ext.DiagnosticListParams
	replyOnce
}

// Reply は、拡張機能へ diagnostic/list の結果を返す。
func (r *DiagnosticListRequest) Reply(result ext.DiagnosticListResult) error {
	return r.reply(result, nil)
}

// ChoiceAskRequest は、拡張機能からの choice/ask のリクエストである（基本設計 6.7）。
// 利用者の応答を待つので、キー入力ごとの問い合わせの期限を適用しない。
// イベントループは一度に 1 つの問いだけを表示し、表示中に届いた問いは順に待たせる。
// 問いを出した拡張機能が再起動した（[StateChange] が届いた）場合は、その拡張機能の問いを取り下げる。
type ChoiceAskRequest struct {
	Extension string
	Params    ext.ChoiceAskParams
	replyOnce
}

// Reply は、拡張機能へ choice/ask の結果を返す。
func (r *ChoiceAskRequest) Reply(result ext.ChoiceAskResult) error {
	return r.reply(result, nil)
}

// DecorationSet は、拡張機能からの decoration/set の通知である（基本設計 6.1）。
type DecorationSet struct {
	Extension string
	Params    ext.DecorationSetParams
}

// MessageShow は、拡張機能からの message/show の通知である。
type MessageShow struct {
	Extension string
	Params    ext.MessageShowParams
}

// DiagnosticSet は、拡張機能からの diagnostic/set の通知である（基本設計 6.7 の表示のデータ）。
// イベントループは、拡張機能とバッファの組ごとに一覧を持ち、Params.Version に応じてずらすか捨てる（[ext.DiagnosticSetParams]）。
type DiagnosticSet struct {
	Extension string
	Params    ext.DiagnosticSetParams
}

// SignSet は、拡張機能からの sign/set の通知である（基本設計 6.7 の表示のデータ）。
type SignSet struct {
	Extension string
	Params    ext.SignSetParams
}

// PopupShow は、拡張機能からの popup/show の通知である（基本設計 6.7 の表示のデータ）。
type PopupShow struct {
	Extension string
	Params    ext.PopupShowParams
}

// OutputAppend は、拡張機能からの output/append の通知である（基本設計 6.7 の表示のデータ）。
type OutputAppend struct {
	Extension string
	Params    ext.OutputAppendParams
}

// StatusLineSet は、拡張機能からの statusline/set の通知である（基本設計 7.1。FR-92）。
// 宣言ファイルで足した部品かどうかは、イベントループが確かめる。
type StatusLineSet struct {
	Extension string
	Params    ext.StatusLineSetParams
}

// ProgressReport は、拡張機能からの progress/report の通知である（基本設計 6.7 の「進み具合の表示」。FR-178）。
// Params は ext.ProgressReportParams.Validate を通したものである。
type ProgressReport struct {
	Extension string
	Params    ext.ProgressReportParams
}

// BandSet は、拡張機能からの band/set の通知である（基本設計 6.7 の「窓の上端の帯」。FR-237）。
// Params は ext.BandSetParams.Validate を通したものである。
type BandSet struct {
	Extension string
	Params    ext.BandSetParams
}

// PopupOpenRequest は、拡張機能からの popup/open のリクエストである（基本設計 1.4、5.3。FR-68、FR-72）。
// Params は ext.PopupOpenParams.Validate を通したものである。
// イベントループは、ポップアップを開いて ID を [PopupOpenRequest.Reply] で返す。
type PopupOpenRequest struct {
	Extension string
	Params    ext.PopupOpenParams
	replyOnce
}

// Reply は、拡張機能へ popup/open の結果を返す。
func (r *PopupOpenRequest) Reply(result ext.PopupOpenResult) error {
	return r.reply(result, nil)
}

// BufferCreateRequest は、拡張機能からの buffer/create のリクエストである（基本設計 5.3、6.9。FR-88、FR-104）。
// Params は ext.BufferCreateParams.Validate を通したものである。
// イベントループは、ファイルでないバッファを作って [Host.OpenBuffer] を呼んでから [BufferCreateRequest.Reply] で番号を返す。
type BufferCreateRequest struct {
	Extension string
	Params    ext.BufferCreateParams
	replyOnce
}

// Reply は、拡張機能へ buffer/create の結果を返す。
func (r *BufferCreateRequest) Reply(result ext.BufferCreateResult) error {
	return r.reply(result, nil)
}

// BufferUpdateRequest は、拡張機能からの buffer/update のリクエストである（ext の diffstart.go）。
type BufferUpdateRequest struct {
	Extension string
	Params    ext.BufferUpdateParams
	replyOnce
}

// Reply は、拡張機能へ buffer/update の結果を返す。
func (r *BufferUpdateRequest) Reply(result ext.BufferUpdateResult) error {
	return r.reply(result, nil)
}

// BufferDisposeRequest は、拡張機能からの buffer/dispose のリクエストである（ext の diffstart.go）。
type BufferDisposeRequest struct {
	Extension string
	Params    ext.BufferDisposeParams
	replyOnce
}

// Reply は、拡張機能へ buffer/dispose の結果を返す。
func (r *BufferDisposeRequest) Reply() error {
	return r.reply(ext.BufferDisposeResult{}, nil)
}

// DiffStartRequest は、拡張機能からの diff/start のリクエストである（基本設計 5.3、6.8。FR-99、FR-104）。
// Params は ext.DiffStartParams.Validate を通したものである。
type DiffStartRequest struct {
	Extension string
	Params    ext.DiffStartParams
	replyOnce
}

// Reply は、拡張機能へ diff/start の結果を返す。
func (r *DiffStartRequest) Reply(result ext.DiffStartResult) error {
	return r.reply(result, nil)
}

// PaneOpenRequest は、拡張機能からの pane/open のリクエストである（基本設計 1.2、1.3、5.3。FR-115、FR-123）。
// Params は ext.PaneOpenParams.Validate を通したものである。
// イベントループは、ペイン（端末多重化ソフトの外では中央のポップアップ）を開いて ID を [PaneOpenRequest.Reply] で返す。
type PaneOpenRequest struct {
	Extension string
	Params    ext.PaneOpenParams
	replyOnce
}

// Reply は、拡張機能へ pane/open の結果を返す。
func (r *PaneOpenRequest) Reply(result ext.PaneOpenResult) error {
	return r.reply(result, nil)
}

// EditorListRequest は、拡張機能からの editor/list のリクエストである（基本設計 6.9。FR-107）。
// Params は ext.EditorListParams.Validate を通したものである。
type EditorListRequest struct {
	Extension string
	Params    ext.EditorListParams
	replyOnce
}

// Reply は、拡張機能へ editor/list の結果を返す。
func (r *EditorListRequest) Reply(result ext.EditorListResult) error {
	return r.reply(result, nil)
}

// EditorExecuteRequest は、拡張機能からの editor/execute のリクエストである（基本設計 6.9）。
// Params は ext.EditorExecuteParams.Validate を通したものである。
type EditorExecuteRequest struct {
	Extension string
	Params    ext.EditorExecuteParams
	replyOnce
}

// Reply は、拡張機能へ editor/execute の結果を返す。
func (r *EditorExecuteRequest) Reply() error {
	return r.reply(ext.EditorExecuteResult{}, nil)
}

// OptionSetRequest は、拡張機能からの option/set のリクエストである（基本設計 7.3。#349。FR-190、FR-191）。
// Params は ext.OptionSetParams.Validate を通したものである。項目の名前と値は、イベントループが設定の表で確かめる。
type OptionSetRequest struct {
	Extension string
	Params    ext.OptionSetParams
	replyOnce
}

// Reply は、拡張機能へ option/set の結果を返す。
func (r *OptionSetRequest) Reply() error {
	return r.reply(ext.OptionSetResult{}, nil)
}

// OptionGetRequest は、拡張機能からの option/get のリクエストである（基本設計 7.3。#349。FR-192）。
type OptionGetRequest struct {
	Extension string
	Params    ext.OptionGetParams
	replyOnce
}

// Reply は、拡張機能へ option/get の結果を返す。
func (r *OptionGetRequest) Reply(result ext.OptionGetResult) error {
	return r.reply(result, nil)
}

// ViewUpdate は、拡張機能からの view/update の通知である。読めない部品（ext.ViewComponent.Validate の誤り）は Host が捨てる。
type ViewUpdate struct {
	Extension string
	Params    ext.ViewUpdateParams
}

// ViewClose は、拡張機能からの view/close の通知である。
type ViewClose struct {
	Extension string
	Params    ext.ViewCloseParams
}

// StateChange は、拡張機能の状態が変わったことを知らせる。
// 異常による遷移（再起動待ちと無効）では、Err に異常の内容が入る。利用者への 1 行のメッセージに使う（基本設計 8 章）。
type StateChange struct {
	Extension string
	State     State
	Err       error
}

// QueryResult は、[Host.Send] で送った問い合わせの結果である（FR-18）。
//
// イベントループは、Tag に入れておいたバージョン番号とカーソル位置を現在の値と比べ、
// 同じ場合だけ結果を適用する。違えば破棄する。
type QueryResult struct {
	Extension string
	Method    string
	// Tag は、[Host.Send] に渡した値である。
	Tag any
	// Err は、問い合わせが失敗した理由である。成功したときは nil。
	Err error

	result json.RawMessage
}

// Decode は、成功した結果の JSON を v（ポインター）へ読み込む。失敗した問い合わせなら Err を返す。
func (r *QueryResult) Decode(v any) error {
	if r.Err != nil {
		return r.Err
	}
	if err := json.Unmarshal(r.result, v); err != nil {
		return fmt.Errorf("exthost: decode %s result: %w", r.Method, err)
	}
	return nil
}

func (*EditRequest) isEvent()           {}
func (*EditorStateRequest) isEvent()    {}
func (*EditorOpenRequest) isEvent()     {}
func (*BufferSaveRequest) isEvent()     {}
func (*DiagnosticListRequest) isEvent() {}
func (*ChoiceAskRequest) isEvent()      {}
func (*DecorationSet) isEvent()         {}
func (*MessageShow) isEvent()           {}
func (*DiagnosticSet) isEvent()         {}
func (*SignSet) isEvent()               {}
func (*PopupShow) isEvent()             {}
func (*OutputAppend) isEvent()          {}
func (*StatusLineSet) isEvent()         {}
func (*ProgressReport) isEvent()        {}
func (*BandSet) isEvent()               {}
func (*PopupOpenRequest) isEvent()      {}
func (*BufferCreateRequest) isEvent()   {}
func (*DiffStartRequest) isEvent()      {}
func (*BufferUpdateRequest) isEvent()   {}
func (*BufferDisposeRequest) isEvent()  {}
func (*PaneOpenRequest) isEvent()       {}
func (*EditorListRequest) isEvent()     {}
func (*EditorExecuteRequest) isEvent()  {}
func (*OptionSetRequest) isEvent()      {}
func (*OptionGetRequest) isEvent()      {}
func (*ViewUpdate) isEvent()            {}
func (*ViewClose) isEvent()             {}
func (*StateChange) isEvent()           {}
func (*QueryResult) isEvent()           {}

// queued は、イベントループへ渡す前の列の 1 件である。
// r は、拡張機能から届いたイベントなら、それを受け取ったプロセスである。それ以外は nil。
type queued struct {
	ev Event
	r  *run
}

// errTooManyEvents は、拡張機能からのイベントが列の上限を超えたことを表す。
var errTooManyEvents = errors.New("exthost: too many queued events")

// pushLocked は、イベントを列に積む。r が nil でなければ、r の積んだ数を数え、上限を超えたら積まずに誤りを返す。
func (h *Host) pushLocked(ev Event, r *run) error {
	if r != nil {
		if r.queued >= h.cfg.MaxQueuedEvents {
			return fmt.Errorf("%w: limit %d", errTooManyEvents, h.cfg.MaxQueuedEvents)
		}
		r.queued++
	}
	h.queue = append(h.queue, queued{ev: ev, r: r})
	select {
	case h.wake <- struct{}{}:
	default:
	}
	return nil
}

// purgeLocked は、プロセス r から届いたイベントのうち、まだ渡していないものを捨てる。
// 異常と判断した拡張機能の編集を、後から適用しないためである。
func (h *Host) purgeLocked(r *run) {
	kept := h.queue[:0]
	for _, q := range h.queue {
		if q.r != r {
			kept = append(kept, q)
		}
	}
	clear(h.queue[len(kept):])
	h.queue = kept
	r.queued = 0
}

// pump は、列のイベントを 1 件ずつイベントループへ渡す。Host が閉じたら events を閉じて終わる。
func (h *Host) pump() {
	defer close(h.pumpDone)
	defer close(h.events)
	defer h.recoverPanic()
	for {
		h.mu.Lock()
		if len(h.queue) == 0 {
			h.mu.Unlock()
			select {
			case <-h.wake:
				continue
			case <-h.ctx.Done():
				return
			}
		}
		q := h.queue[0]
		h.queue[0] = queued{}
		h.queue = h.queue[1:]
		if q.r != nil {
			q.r.queued--
		}
		h.mu.Unlock()
		select {
		case h.events <- q.ev:
		case <-h.ctx.Done():
			return
		}
	}
}
