package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/command/target"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/input"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// queryTag は、exthost.SendOptions の Tag に入れる、問い合わせを見分ける値である。
type queryTag uint64

// pendingQuery は、期限 0 の設定（FR-18）で、結果を待たずに戻った問い合わせである。
type pendingQuery struct {
	q      *exthost.Query
	tag    queryTag
	bs     *bufferState
	method string
	// ref は、問い合わせた操作である。結果を破棄したことを知らせる文言に使う。
	ref editcmd.Ref
	// d は、command.State が保留した操作である。Feed が戻ってから trackDeferred が入れる。
	d *command.Deferred
	// client は、問い合わせを始めた client である。結果はこの client の状態で扱う（applyDeferred。基本設計 1.2）。
	client *clientView
}

// openRequest は、command/execute の結果の Open のうち、Feed が戻ってから行うものである。
type openRequest struct {
	ext string
	p   ext.EditorOpenParams
}

// extQuery は、command.Extensions を exthost で実装する（基本設計 5.4）。
// answer があれば、method が answerMethod の最初の問い合わせには、送らずに answer を返す（ApplyDeferred で、届いた結果を使う）。
// replay があれば、問い合わせには送らずに、再生の途中で届いた結果を問い合わせた順に返す（ResumeReplay。replayAnswered）。
type extQuery struct {
	e            *Editor
	bs           *bufferState
	answer       *exthost.QueryResult
	answerMethod string
	replay       *replayAnswers
}

// errExtension は、拡張機能の操作の失敗を、利用者へ知らせる文言に包む。
// 拡張機能が動いていない（exthost.ErrUnavailable）ときは、exthost の内部の英語の文言を出さずに、日本語の 1 行にする（unavailableError）。
func errExtension(ref editcmd.Ref, err error) error {
	if errors.Is(err, exthost.ErrUnavailable) {
		return &unavailableError{ref: ref, err: err}
	}
	return fmt.Errorf("拡張機能 %s の %s が失敗しました: %w", ref.Extension, ref.ID, err)
}

// unavailableError は、拡張機能が無効か、再起動の待ちか、問い合わせの途中で異常終了したので、操作を使えなかったことを表す。
// 文言は利用者に表示するので日本語にする。errors.Is で exthost.ErrUnavailable を判定できる。
type unavailableError struct {
	ref editcmd.Ref
	err error
}

func (e *unavailableError) Error() string {
	return fmt.Sprintf("拡張機能 %s の %s は使えません: 拡張機能が動いていません（無効か、異常終了の後の再起動の待ち）", e.ref.Extension, e.ref.ID)
}

func (e *unavailableError) Unwrap() error { return e.err }

// errWithheld は、拡張機能へ送っていないバッファ（不正な UTF-8 を含むもの。基本設計 5.3）で、
// 拡張機能の操作が使えないことを、利用者へ知らせる文言にする。
func errWithheld(ref editcmd.Ref) error {
	return fmt.Errorf("拡張機能 %s の %s は使えません: このバッファは不正な UTF-8 を含むので、拡張機能へ送っていません", ref.Extension, ref.ID)
}

// call は、問い合わせて結果を out へ読む。
func (x *extQuery) call(ref editcmd.Ref, method string, params any, info command.QueryInfo, out any) error {
	if x.answer != nil && method == x.answerMethod {
		r := x.answer
		x.answer = nil
		if err := r.Decode(out); err != nil {
			return errExtension(ref, err)
		}
		return nil
	}
	if x.replay != nil {
		r, err := x.replay.next(method)
		if err != nil {
			return errExtension(ref, err)
		}
		if r != nil {
			if err := r.Decode(out); err != nil {
				return errExtension(ref, err)
			}
			return nil
		}
	}
	return x.e.query(x.bs, ref, method, params, info, out, x.replay)
}

// query は、拡張機能 ref.Extension へ method を問い合わせる。期限の扱いは package の doc にある。
// 再生中（info.Replaying）は、送って command.ErrQueryPending を返す（sendReplay）。prev は、再生の続きで操作を実行し直している
// ときの、それまでに届いた結果である（無ければ nil）。
//
// req: FR-17, FR-18, FR-19
func (e *Editor) query(bs *bufferState, ref editcmd.Ref, method string, params any, info command.QueryInfo, out any, prev *replayAnswers) error {
	// マクロの再生の途中なら、ここまでの変更を拡張機能の複製へ送ってから問い合わせる（FR-16）。
	e.syncChanges(bs, bs.state.PendingChanges())
	if e.host.Withheld(bs.id) {
		// 拡張機能はこのバッファの複製を持っていない（基本設計 5.3）。
		// command/execute と provider/invoke は、バッファを使わないコマンド（デバッグの再開など）のために Withheld を立てて送り、
		// それ以外は送らない。
		switch p := params.(type) {
		case ext.CommandExecuteParams:
			p.Withheld = true
			params = p
		case ext.ProviderInvokeParams:
			p.Withheld = true
			params = p
		default:
			return errWithheld(ref)
		}
	}
	// この拡張機能が後から届ける editor/open などは、問い合わせを始めた client に出す（extView。基本設計 1.2）。
	e.extClient[ref.Extension] = e.view
	delete(e.extFromPane, ref.Extension)
	e.tagSeq++
	tag := queryTag(e.tagSeq)
	timeout := e.syncTimeout()
	switch {
	case info.Replaying:
		return e.sendReplay(bs, ref, method, params, tag, prev)
	case timeout == 0:
		q, err := e.host.Send(ref.Extension, method, params, exthost.SendOptions{Tag: tag})
		if err != nil {
			return errExtension(ref, err)
		}
		if e.deferred != nil {
			e.deferred.q.Cancel()
		}
		e.deferred = &pendingQuery{q: q, tag: tag, bs: bs, method: method, ref: ref, client: e.view}
		return command.ErrDeferred
	}
	// 起動を含む問い合わせだけ長い期限で待つ（基本設計 5.2）。拡張機能が稼働中なら exthost は使わない。
	opts := exthost.SendOptions{Tag: tag, Timeout: max(timeout, 0), StartTimeout: e.startSyncTimeout()}
	q, err := e.host.Send(ref.Extension, method, params, opts)
	if err != nil {
		return errExtension(ref, err)
	}
	r := e.wait(q, tag)
	if err := r.Decode(out); err != nil {
		return errExtension(ref, err)
	}
	return nil
}

// wait は、Tag が tag の QueryResult が届くまで待つ（入れ子の待ち）。
// 待つ間は、リサイズに応じて描画し、<C-c> で問い合わせを取り消す。他のイベントは hold で取っておく。
// キーは列に残したままにし、待ちが終わってから処理する。
// Run の ctx が終わったら、問い合わせを取り消し、結果を待たずに ctx.Err() の結果を返す。
func (e *Editor) wait(q *exthost.Query, tag queryTag) *exthost.QueryResult {
	e.waiting++
	defer func() { e.waiting-- }()
	e.render()
	done := context.Background().Done()
	if e.ctx != nil {
		done = e.ctx.Done()
	}
	for {
		if e.interrupt.Load() {
			q.Cancel()
		}
		select {
		case <-done:
			q.Cancel()
			return &exthost.QueryResult{Err: e.ctx.Err()}
		case ev, ok := <-e.host.Events():
			if !ok {
				return &exthost.QueryResult{Err: exthost.ErrClosed}
			}
			if r, ok := ev.(*exthost.QueryResult); ok && r.Tag == tag {
				return r
			}
			if e.hold(ev) {
				// 待ちの間も、進み具合はその場で描き直す（hold の doc。FR-178）。
				e.render()
			}
		case <-e.resizeCh:
			e.applySize()
			e.render()
		case <-e.intrCh:
		}
	}
}

// hold は、入れ子の待ちの間と確認の問いの間に届いたイベントを、後で届いた順に処理するために取っておく。
//
// 取っておく数の上限は exthost の Config.MaxQueuedEvents（既定 1024）である。
// 待ちの間は exthost の列からここへ移すので、exthost の上限（NFR-03）が効かない。その代わりにここで抑える。
// 上限を超えたら、最も古い通知（表示のデータとメッセージ）から捨てる。通知が無ければ、最も古いリクエストに誤りで応答して捨てる。
// 問い合わせの結果（QueryResult）と状態の変化（StateChange）は捨てない。
//
// 部品の中身（StatusLineSet）は、同じ拡張機能の同じ部品の前のものを後のもので置き換えて 1 つにまとめ、上限を超えても捨てない。
// 本体は部品ごとに最後の中身だけを使い、拡張機能は変わったときだけ送るので、最後の更新を捨てると古い中身が出続けるためである
// （PR #239 のレビューの推奨 5）。まとめるので、取っておく数は宣言ファイルの部品の数を超えない。
//
// 例外として、進み具合（ProgressReport）は取っておかず、その場で進み具合の並びに当て（reportProgress）、描き直しが要ることを
// true で返す（基本設計 5.3。FR-178。PR #345 のレビューの必須 1）。進み具合は画面の表示だけを変え、バッファもカーソルも変えないので、
// 実行中の操作の途中で当てても操作に響かない。保存の前の整形（buffer/willSave の待ち）のように、待っている間にこそ出したい知らせだからである。
// 取っておかないので、取っておく列の上限にも数えない（同じレビューの必須 2）。
// 進み具合をその場で当てるので、拡張機能が止まったことを表す状態の変化（StateChange）を取っておくときは、その拡張機能の進み具合を
// その場で消す（clearProgressNow）。取っておいた状態の変化を後で処理するときに消すと、待ちの間に起動し直した後の進み具合まで消えるためである。
func (e *Editor) hold(ev exthost.Event) (redraw bool) {
	switch s := ev.(type) {
	case *exthost.StatusLineSet:
		e.held = slices.DeleteFunc(e.held, func(h exthost.Event) bool {
			p, ok := h.(*exthost.StatusLineSet)
			return ok && p.Extension == s.Extension && p.Params.Part == s.Params.Part
		})
	case *exthost.ProgressReport:
		e.reportProgress(s.Extension, s.Params)
		return true
	case *exthost.StateChange:
		redraw = e.clearProgressNow(s)
	}
	e.held = append(e.held, ev)
	limit := e.cfg.Host.MaxQueuedEvents
	if limit <= 0 {
		limit = exthost.DefaultMaxQueuedEvents
	}
	if len(e.held) <= limit {
		return redraw
	}
	drop := -1
	for i, h := range e.held {
		switch h.(type) {
		case *exthost.DecorationSet, *exthost.MessageShow, *exthost.DiagnosticSet, *exthost.SignSet,
			*exthost.PopupShow, *exthost.OutputAppend, *exthost.ViewUpdate, *exthost.BandSet, *exthost.VirtualTextSet:
			drop = i
		}
		if drop >= 0 {
			break
		}
	}
	if drop < 0 {
		for i, h := range e.held {
			if r, ok := h.(request); ok {
				_ = r.ReplyError(&rpc.Error{Code: rpc.CodeInternalError, Message: "editor is busy: too many pending requests"})
				drop = i
				break
			}
		}
	}
	if drop < 0 {
		return redraw
	}
	e.log.Warn("取っておくイベントが上限を超えたので捨てる", "type", fmt.Sprintf("%T", e.held[drop]))
	e.held = append(e.held[:drop], e.held[drop+1:]...)
	return redraw
}

// trackDeferred は、Feed などが保留した操作 d を、送った問い合わせと結びつける。
// 結びつく操作が無ければ（保留が失敗として扱われた場合）、問い合わせを取り消す。
func (e *Editor) trackDeferred(bs *bufferState, d *command.Deferred) {
	p := e.deferred
	if p == nil || p.d != nil {
		return
	}
	if d == nil || p.bs != bs {
		p.q.Cancel()
		e.deferred = nil
		return
	}
	p.d = d
}

// applyDeferred は、期限 0 の設定で保留した問い合わせの結果 r を適用する（FR-18）。
// バージョン番号とカーソル位置が問い合わせたときと同じ場合だけ適用し、違えば破棄する（command.State.ApplyDeferred）。
// 破棄したら、メッセージの行で知らせる（showDiscarded）。
//
// req: FR-18
func (e *Editor) applyDeferred(p *pendingQuery, r *exthost.QueryResult) {
	e.deferred = nil
	if p.d == nil {
		return
	}
	if p.bs.state.Pending() {
		// キーの列の解釈の途中（d の後など）に届いた結果は破棄する。利用者は続きのキーを、保留した操作の後のつもりで打っていない。
		e.showDiscarded(p, r)
		return
	}
	if r.Err != nil {
		if !errors.Is(r.Err, context.Canceled) {
			if e.host.Withheld(p.bs.id) {
				e.showError(errWithheld(editcmd.Ref{Extension: r.Extension, ID: r.Method}))
			} else {
				e.showError(errExtension(editcmd.Ref{Extension: r.Extension, ID: r.Method}, r.Err))
			}
		}
		return
	}
	x := &extQuery{e: e, bs: p.bs, answer: r, answerMethod: p.method}
	res := p.bs.state.ApplyDeferred(*p.d, x)
	_ = e.handleResult(p.bs, res) // 失敗は handleResult が知らせる
	if errors.Is(res.Err, command.ErrStale) {
		e.showDiscarded(p, r)
	}
}

// showDiscarded は、期限 0 の設定で保留した問い合わせの結果 r を破棄したことを、メッセージの行に 1 行で知らせる（FR-18）。
// 文言は日本語で、Vim に対応するエラーが無いので番号は付けない。
//
// メッセージの行は 1 行で、知らせは前のメッセージを上書きし、次のキー入力で消える。破棄が続いても行が増えることは無い。
// 保留できる問い合わせは 1 つだけで、次の問い合わせにはキー入力が要るので、知らせが重なることもほぼ無い。
// 結果が適用するものを持たない場合（ok が false のもの、command/execute で編集もカーソルの移動も無いもの）は、
// 破棄しても失われるものが無いので知らせない（acp.prompt のように、受け付けたことだけを返すコマンド）。
//
// 新しい問い合わせを送るために取り消した、前の保留中の問い合わせ（query の Cancel）は、結果が届いていないので知らせない。
//
// req: FR-18
func (e *Editor) showDiscarded(p *pendingQuery, r *exthost.QueryResult) {
	if !hasEffect(p.method, r) {
		e.log.Debug("適用するものの無い期限 0 の結果を捨てた", "ext", p.ref.Extension, "id", p.ref.ID)
		return
	}
	text := fmt.Sprintf("拡張機能 %s の %s の結果を破棄しました（問い合わせの後にバッファかカーソルが変わったため）", p.ref.Extension, p.ref.ID)
	e.message = ui.Message{Text: text, Level: ui.MessageWarning}
	e.log.Debug("期限 0 の結果を破棄した", "ext", p.ref.Extension, "id", p.ref.ID)
}

// hasEffect は、method の結果 r が、適用すればバッファかカーソルを変えうるものかを返す。
// ok が false なら、適用しても操作は失敗するだけなので false である。
func hasEffect(method string, r *exthost.QueryResult) bool {
	var v struct {
		OK          bool                  `json:"ok"`
		Transaction *ext.Transaction      `json:"transaction"`
		Open        *ext.EditorOpenParams `json:"open"`
	}
	if err := r.Decode(&v); err != nil || !v.OK {
		return false
	}
	if method != ext.MethodCommandExecute && method != ext.MethodProviderInvoke {
		return true
	}
	return (v.Transaction != nil && len(v.Transaction.Edits) > 0) || v.Open != nil
}

// count は、ext の回数（0 は省略）を返す。
func count(n int, given bool) int {
	if !given {
		return 0
	}
	return n
}

// args は、操作の後に取った文字の引数を、拡張機能へ渡す Args にする（{"char":"x"}。仮。基本設計 6.7）。引数が無ければ nil。
func args(ch rune) json.RawMessage {
	if ch == 0 {
		return nil
	}
	b, _ := json.Marshal(map[string]string{"char": string(ch)})
	return b
}

// commandArgs は、command/execute の Args を作る。
// コマンドラインから引数を付けて呼んだとき（:acp.prompt 説明して）は、引数の文字列を {"text":"..."} で渡す（仮。基本設計 6.7）。
// それ以外は args と同じである。
func commandArgs(q command.ActionQuery) json.RawMessage {
	if q.Text == "" {
		return args(q.Char)
	}
	b, _ := json.Marshal(map[string]string{"text": q.Text})
	return b
}

func toExtPos(p buffer.Position) ext.Position { return ext.Position{Line: p.Line, Byte: p.Byte} }

func fromExtPos(p ext.Position) buffer.Position { return buffer.Position{Line: p.Line, Byte: p.Byte} }

func toExtRange(r buffer.Range) ext.Range {
	return ext.Range{Start: toExtPos(r.Start), End: toExtPos(r.End)}
}

func fromExtRange(r ext.Range) buffer.Range {
	return buffer.Range{Start: fromExtPos(r.Start), End: fromExtPos(r.End)}
}

func fromExtTx(tx ext.Transaction) buffer.Transaction {
	out := buffer.Transaction{BaseVersion: uint64(tx.BaseVersion), Edits: make([]buffer.Edit, len(tx.Edits))}
	for i, ed := range tx.Edits {
		out.Edits[i] = buffer.Edit{Range: fromExtRange(ed.Range), Text: ed.Text}
	}
	return out
}

// checkResult は、拡張機能の結果の OK と、計算のもとにしたバージョン番号を確かめる。
func checkResult(ref editcmd.Ref, ok bool, version ext.Version, b *buffer.Buffer) error {
	if !ok {
		return fmt.Errorf("%w: extension %s returned no result for %s", command.ErrFailed, ref.Extension, ref.ID)
	}
	if uint64(version) != b.Version() {
		return errExtension(ref, fmt.Errorf("バージョン番号が %d で、現在の %d と違います", version, b.Version()))
	}
	return nil
}

// checkPos は、拡張機能が返した位置がバッファの中の文字の境界であるかを確かめる。
func checkPos(ref editcmd.Ref, b *buffer.Buffer, ps ...buffer.Position) error {
	for _, p := range ps {
		if _, err := b.Slice(buffer.Range{Start: p, End: p}); err != nil {
			return errExtension(ref, err)
		}
	}
	return nil
}

// Motion は、motion/resolve を問い合わせる。
func (x *extQuery) Motion(ref editcmd.Ref, b *buffer.Buffer, req target.Request, info command.QueryInfo) (target.Result, error) {
	p := ext.MotionResolveParams{
		Buffer: x.bs.id, Version: ext.Version(b.Version()), ID: ref.ID,
		Cursor: toExtPos(req.Cursor), Count: count(req.Count, req.CountGiven), Args: args(req.Char),
	}
	var r ext.MotionResolveResult
	if err := x.call(ref, ext.MethodMotionResolve, p, info, &r); err != nil {
		return target.Result{}, err
	}
	if err := checkResult(ref, r.OK, r.Version, b); err != nil {
		return target.Result{}, err
	}
	pos := fromExtPos(r.Position)
	if err := checkPos(ref, b, pos); err != nil {
		return target.Result{}, err
	}
	kind := target.Exclusive
	switch {
	case r.Linewise:
		kind = target.Linewise
	case r.Inclusive:
		kind = target.Inclusive
	}
	cols := req.Columns
	if cols == nil {
		cols = target.CharColumns{}
	}
	return target.Result{Pos: pos, Kind: kind, Want: cols.Column(pos.Line, b.Line(pos.Line), pos.Byte)}, nil
}

// TextObject は、textObject/resolve を問い合わせる。
func (x *extQuery) TextObject(ref editcmd.Ref, b *buffer.Buffer, req target.Request, info command.QueryInfo) (target.Span, error) {
	p := ext.TextObjectResolveParams{
		Buffer: x.bs.id, Version: ext.Version(b.Version()), ID: ref.ID,
		Cursor: toExtPos(req.Cursor), Count: count(req.Count, req.CountGiven), Args: args(req.Char),
	}
	if req.Selection != nil {
		sel := toExtRange(*req.Selection)
		p.Selection = &sel
	}
	var r ext.TextObjectResolveResult
	if err := x.call(ref, ext.MethodTextObjectResolve, p, info, &r); err != nil {
		return target.Span{}, err
	}
	if err := checkResult(ref, r.OK, r.Version, b); err != nil {
		return target.Span{}, err
	}
	rg := fromExtRange(r.Range)
	if err := checkPos(ref, b, rg.Start, rg.End); err != nil {
		return target.Span{}, err
	}
	if rg.End.Less(rg.Start) {
		return target.Span{}, errExtension(ref, buffer.ErrInvalidRange)
	}
	kind := target.Exclusive
	if r.Linewise {
		kind = target.Linewise
	}
	// 選択を置き換える答えは、選択を渡した問い合わせ（ビジュアルモード）でだけ読む。
	return target.Span{Start: rg.Start, End: rg.End, Kind: kind, ReplaceSelection: r.ReplaceSelection && p.Selection != nil}, nil
}

// Operator は、operator/apply を問い合わせる。
func (x *extQuery) Operator(ref editcmd.Ref, b *buffer.Buffer, q command.OperatorQuery, info command.QueryInfo) (buffer.Transaction, error) {
	if ref.Extension == input.ProviderExtension {
		return x.providerOperator(ref, b, q, info)
	}
	p := ext.OperatorApplyParams{
		Buffer: x.bs.id, Version: ext.Version(b.Version()), ID: ref.ID,
		Range: toExtRange(q.Span.Range(b)), Count: q.Count, Args: args(q.Char),
	}
	var r ext.OperatorApplyResult
	if err := x.call(ref, ext.MethodOperatorApply, p, info, &r); err != nil {
		return buffer.Transaction{}, err
	}
	if err := checkResult(ref, r.OK, r.Transaction.BaseVersion, b); err != nil {
		return buffer.Transaction{}, err
	}
	return fromExtTx(r.Transaction), nil
}

// Action は、command/execute を問い合わせる。
// 再生中（FR-19）は params の Sync を true にし、結果の Open（カーソルの移動）も、次の操作の前に行う。
func (x *extQuery) Action(ref editcmd.Ref, b *buffer.Buffer, q command.ActionQuery, info command.QueryInfo) (buffer.Transaction, error) {
	if done, err := x.e.helpAction(x.bs, ref, q.Text); done {
		return buffer.Transaction{BaseVersion: b.Version()}, err
	}
	if x.e.previewScrollAction(x.bs, ref, count(q.Count, q.CountGiven)) {
		return buffer.Transaction{BaseVersion: b.Version()}, nil
	}
	if ref.Extension == input.ProviderExtension {
		return x.providerAction(ref, b, q, info)
	}
	if ref.Extension == extManagerExtension {
		return extManagerTx(b), x.e.extManagerAction(q.Text)
	}
	if ref.Extension == messagesExtension {
		x.e.showMessages()
		return extManagerTx(b), nil
	}
	if ref.Extension == introExtension {
		return extManagerTx(b), x.e.showIntro(q.Text, info.Replaying)
	}
	p := ext.CommandExecuteParams{
		Buffer: x.bs.id, Version: ext.Version(b.Version()), ID: ref.ID,
		Cursor: toExtPos(q.Cursor), Count: count(q.Count, q.CountGiven), Args: commandArgs(q), Sync: info.Replaying,
	}
	return x.invokeResult(ref, ext.MethodCommandExecute, p, b, q, info)
}

// invokeResult は、command/execute か provider/invoke（method）を params で問い合わせ、結果の編集とカーソルの移動を扱う。
// 2 つの result は同じ形（ext.ProviderInvokeResult は ext.CommandExecuteResult）である。
func (x *extQuery) invokeResult(ref editcmd.Ref, method string, params any, b *buffer.Buffer, q command.ActionQuery, info command.QueryInfo) (buffer.Transaction, error) {
	var r ext.CommandExecuteResult
	x.e.extRan = true
	x.e.popupMark = popupMark{state: x.bs.state, keys: x.bs.state.KeysFed()}
	err := x.call(ref, method, params, info, &r)
	if (err != nil || !r.OK) && x.e.host.Withheld(x.bs.id) && !errors.Is(err, context.Canceled) &&
		!errors.Is(err, command.ErrDeferred) && !errors.Is(err, command.ErrQueryPending) {
		// 送っていないバッファでは、バッファを使うコマンドは失敗する。拡張機能の返す理由より、送っていないことを知らせる。
		return buffer.Transaction{}, errWithheld(ref)
	}
	if err != nil {
		return buffer.Transaction{}, err
	}
	if !r.OK {
		return buffer.Transaction{}, fmt.Errorf("%w: extension %s command %s failed", command.ErrFailed, ref.Extension, ref.ID)
	}
	if err := exthost.ValidateCommandResult(&r); err != nil {
		return buffer.Transaction{}, errExtension(ref, err)
	}
	tx := buffer.Transaction{BaseVersion: b.Version()}
	if r.Transaction != nil {
		tx = fromExtTx(*r.Transaction)
	}
	if r.Open == nil && r.Transaction == nil && info.Replaying && x.e.popupOpenedBy(ref.Extension, x.bs) {
		// 再生中に、拡張機能が応答の前に popup/open でフォーカスを入れるポップアップを開いた（ファインダー。#246）。
		// 再生の残りは、記録したときと同じくポップアップのバッファで続ける（PR #249 のレビューの必須 2）。
		return buffer.Transaction{}, command.ErrBufferSwitched
	}
	if r.Open == nil {
		return tx, nil
	}
	// Open は Transaction を適用した後に行うので、ここで適用し、State には空のトランザクションを返す。
	if len(tx.Edits) > 0 {
		ch, err := b.Apply(tx)
		if err != nil {
			return buffer.Transaction{}, errExtension(ref, err)
		}
		// State を通さずに適用するので、State.applyTx の代わりに、問い合わせたときのカーソルをアンドゥの単位に記録する。
		b.SetUndoCursor(q.Cursor)
		x.e.syncChanges(x.bs, x.bs.state.PendingChanges())
		x.e.commitChange(x.bs, ch)
	}
	if err := x.e.openFromCommand(ref.Extension, x.bs, *r.Open, info.Replaying); err != nil {
		return buffer.Transaction{}, err
	}
	return buffer.Transaction{BaseVersion: b.Version()}, nil
}

// openFromCommand は、command/execute の結果の Open を行う。
//
// 同じバッファの中の移動はすぐに行う（再生の次の操作の前に動かす）。
// 別のバッファへ移るもの（Focus が true）は、再生中ならすぐに移り、command.ErrBufferSwitched を返す。
// command.State は再生の残りを Result.Remaining に入れて止まり、handleResult が移った先のバッファで続ける
// （Vim と同じく、マクロの残りのキーはカーソルのあるバッファへ入る。基本設計 6 章の冒頭の「transaction を適用した後に移動する」）。
// 再生中でなければ、Feed が戻ってから移る。
func (e *Editor) openFromCommand(extID string, bs *bufferState, p ext.EditorOpenParams, replaying bool) error {
	if p.Peek {
		// 「ポップアップで見る」（FR-110）は移らないので、再生の途中でもカーソルのあるバッファは替わらない。
		// 写しのポップアップは、Feed が戻ってから出す（再生の途中の写しは、再生の後に出る。仮）。
		e.pendingOpen = append(e.pendingOpen, openRequest{ext: extID, p: p})
		return nil
	}
	same := (p.Buffer != nil && *p.Buffer == bs.id) || (p.Path != "" && p.Path == bs.path)
	if same && (!p.Focus || e.cur == bs) {
		e.moveCursor(bs, p, e.cur == bs)
		return nil
	}
	if !replaying || !p.Focus {
		e.pendingOpen = append(e.pendingOpen, openRequest{ext: extID, p: p})
		return nil
	}
	// 再生したキーで呼んだコマンドの結果なので、移る理由をキーからの実行にする（switchTo と editorOpen。FR-154）。
	e.keyActions++
	defer func() { e.keyActions-- }()
	if _, _, err := e.editorOpen(p); err != nil {
		return errExtension(editcmd.Ref{Extension: extID, ID: "open"}, err)
	}
	if e.cur == bs {
		return nil
	}
	return command.ErrBufferSwitched
}

// moveCursor は、editor/open の Position と Version に従って bs のカーソルを動かし、動かしたかを返す。
// jump が true なら、動かす前の位置をジャンプリストに足す（Vim でタグや定義へ移るときと同じ）。
func (e *Editor) moveCursor(bs *bufferState, p ext.EditorOpenParams, jump bool) bool {
	if p.Position == nil {
		return false
	}
	if p.Version != nil && uint64(*p.Version) != bs.buf.Version() {
		return false
	}
	if jump {
		e.markJumpAt(bs, bs.state.Cursor())
	}
	bs.state.SetCursor(fromExtPos(*p.Position))
	return true
}

// doPendingOpens は、Feed の間に溜めた Open を行う。
func (e *Editor) doPendingOpens() {
	ops := e.pendingOpen
	e.pendingOpen = nil
	// 溜めた Open は、利用者のキーで呼んだコマンド（拡張機能の操作とコマンド）の結果である。
	// 移る理由をキーからの実行にする（switchTo と editorOpen。FR-154）。
	e.keyActions++
	defer func() { e.keyActions-- }()
	for _, op := range ops {
		if _, _, err := e.editorOpen(op.p); err != nil {
			e.showError(fmt.Errorf("拡張機能 %s の移動: %w", op.ext, err))
		}
	}
}
