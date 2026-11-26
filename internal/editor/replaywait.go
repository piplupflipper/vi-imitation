package editor

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
)

// heldInput は、外部のコマンドの実行（shellJob）か、再生中の拡張機能の結果の待ち（replayWait）の間に止めた、
// キーマップの展開の残りのキーと、貼り付けのテキストである。待ちが終わった後に、この順で続ける（resumeInput）。
// 実行できなかった、中断したときは捨てる（Vim の再生とキーマップが誤りで止まるのと同じ）。
type heldInput struct {
	// mapped は、キーマップの展開の途中で待ちを始めたときの、展開の残りのキーである（feedMapped）。
	mapped []editcmd.Key
	// paste は、貼り付けの前に確定させたキーで待ちを始めたときの、貼り付けのテキストである（handlePaste）。
	paste string
}

// replayWait は、マクロとドットリピートの再生の途中で、拡張機能の結果を待っている問い合わせである（FR-19。基本設計 5.4）。
// 問い合わせた client の clientView.replay に置き、イベントループの goroutine だけが触る。
//
// 再生は command.ErrQueryPending で止まり、結果が届いたら replayAnswered が command.State.ResumeReplay で続ける。
// 待つ間もイベントループは止めない。拡張機能のイベント、他の client のキー、外部のコマンドの結果などは処理を続ける。
// 問い合わせた client のキーは <C-c>（中断）のほかは処理せずに列に残し、続け終えてから処理する（keyAllowed。外部のコマンドと同じ）。
type replayWait struct {
	heldInput
	q   *exthost.Query
	tag queryTag
	// bs は、問い合わせた State のバッファである。結果はこのバッファの State に渡す。
	bs *bufferState
	// client は、問い合わせた client である。
	client *clientView
	// ref は、問い合わせた操作である。失敗を知らせる文言に使う。
	ref editcmd.Ref
	// answers は、同じ操作の中で、この問い合わせより前に届いた結果である（拡張機能のテキストオブジェクトと拡張機能のオペレーターの組など）。
	// 操作を実行し直すときに、問い合わせた順に返す（replayAnswers）。
	answers []*exthost.QueryResult
	// d と rest は、State が止めた操作と再生の残り（command.Result の Deferred と Remaining）である。
	// handleResult が holdReplay で入れる。d が nil の間は、State がまだ止まったことを返していない。
	d    *command.Deferred
	rest []command.ReplaySegment
	// interrupted は、<C-c> で問い合わせを取り消したかである。
	interrupted bool
	// key は、操作の問い合わせでなく、Sync の popupKeys のキーの view/keySync の待ちであることを表す（keysync.go。FR-315）。
	// State はキーの KeyHook の誤りとして止まるので、d は無く、rest だけを持つ。結果は keyReplayAnswered が受ける。
	key bool
	// float は、key の待ちのキーを打ったポップアップかペインである。分割したペインなら、続きをそのペインで再生する（keyReplayAnswered）。
	float *floatState
	// held は、State が止まったことを返し、holdReplay が rest（と d）を結びつけたことを表す。
	held bool
}

// replayAnswers は、再生の途中で止めた操作を実行し直す間に、届いた結果を問い合わせた順に返す（extQuery.call）。
// 返し終えた後の問い合わせは、送って、それまでの結果と一緒に replayWait に持たせる。
type replayAnswers struct {
	list []*exthost.QueryResult
	used int
}

// next は、次に返す結果を返す。method が問い合わせたときと違えば（実行し直した操作が別の問い合わせをした）、誤りにする。
func (a *replayAnswers) next(method string) (*exthost.QueryResult, error) {
	if a.used >= len(a.list) {
		return nil, nil
	}
	r := a.list[a.used]
	a.used++
	if r.Method != "" && r.Method != method {
		return nil, fmt.Errorf("再生の続きで、問い合わせが %s から %s に変わりました", r.Method, method)
	}
	return r, nil
}

// consumed は、返し終えた結果を返す。
func (a *replayAnswers) consumed() []*exthost.QueryResult {
	if a == nil {
		return nil
	}
	return slices.Clone(a.list[:a.used])
}

// sendReplay は、再生中の問い合わせを送り、結果を待たずに command.ErrQueryPending を返す（FR-19）。
// 期限は Config.ReplayTimeout である。結果は replayAnswered が受ける。
//
// req: FR-19
func (e *Editor) sendReplay(bs *bufferState, ref editcmd.Ref, method string, params any, tag queryTag, prev *replayAnswers) error {
	c := e.view
	q, err := e.host.Send(ref.Extension, method, params, exthost.SendOptions{Tag: tag, Timeout: max(e.cfg.ReplayTimeout, 0)})
	if err != nil {
		return errExtension(ref, err)
	}
	if old := c.replay; old != nil {
		// 1 つの client が待つのは 1 つだけである。ここに来るのは、前の待ちを State が返さなかった（holdReplay の前に失敗した）ときである。
		old.q.Cancel()
	}
	c.replay = &replayWait{q: q, tag: tag, bs: bs, client: c, ref: ref, answers: prev.consumed()}
	return command.ErrQueryPending
}

// holdReplay は、State が再生中の問い合わせのために止めた（command.ErrQueryPending）結果 res を、待っている問い合わせに結びつける。
// 結びつく問い合わせが無ければ、State が止めた操作を続ける手立てが無いので、誤りを返す。
func (e *Editor) holdReplay(res command.Result) error {
	w := e.view.replay
	if w == nil || w.held || (res.Deferred == nil) != w.key {
		return errors.New("再生の続きを待てません（内部の誤り）")
	}
	w.d = res.Deferred
	w.rest = res.Remaining
	w.held = true
	return nil
}

// dropOrphanReplay は、問い合わせを送ったのに State が止まったことを返さなかった（誤りに変えた）待ちを取り消す。
// handleResult が、ErrQueryPending でない結果を扱った後に呼ぶ。
func (e *Editor) dropOrphanReplay() {
	if c := e.view; c != nil && c.replay != nil && !c.replay.held {
		e.log.Warn("再生中の問い合わせの結果を待たずに捨てる", "ext", c.replay.ref.Extension, "id", c.replay.ref.ID)
		c.replay.q.Cancel()
		c.replay = nil
	}
}

// replayTimeout は、再生中の問い合わせの期限を返す（Config.ReplayTimeout。0 以下なら exthost の Config.SendTimeout）。
func (e *Editor) replayTimeout() time.Duration {
	switch {
	case e.cfg.ReplayTimeout > 0:
		return e.cfg.ReplayTimeout
	case e.cfg.Host.SendTimeout > 0:
		return e.cfg.Host.SendTimeout
	}
	return exthost.DefaultSendTimeout
}

// replayFor は、Tag が tag の問い合わせを待っている client を返す。無ければ nil。
func (e *Editor) replayFor(tag any) *clientView {
	for _, c := range e.clients {
		if c.replay != nil && c.replay.tag == tag {
			return c
		}
	}
	return nil
}

// replayPending は、どれかの client が再生中の拡張機能の結果を待っているかを返す。
func (e *Editor) replayPending() bool {
	return slices.ContainsFunc(e.clients, func(c *clientView) bool { return c.replay != nil })
}

// interruptReplay は、再生中の拡張機能の結果を待っている client の <C-c> の印が立っていれば、問い合わせを取り消す。
// 印は下ろさない（キーの列の <C-c> までの先打ちは、取り消しの結果が届いた後に replayAnswered が捨てる）。
func (e *Editor) interruptReplay() {
	for _, c := range e.clients {
		if w := c.replay; w != nil && c.intr.Load() && !w.interrupted {
			w.interrupted = true
			w.q.Cancel()
		}
	}
}

// replayAnswered は、client c が待っていた再生中の問い合わせの結果 r で、止めた再生を続ける（command.State.ResumeReplay）。
// 結果を得られなかった（期限を超えた、拡張機能が終わった、<C-c> で取り消した）ら、その操作を失敗にして再生を止める。
// 中断したときは、外部のコマンドと同じく <C-c> までの先打ちのキーを捨てる。残ったキーは、この後に処理する。
// 待つ間に止めたキーマップの展開の残りと貼り付けは、再生を続け終えた後に続ける（resumeInput）。
//
// req: FR-19
func (e *Editor) replayAnswered(c *clientView, r *exthost.QueryResult) {
	w := c.replay
	c.replay = nil
	// 結果が届いたのと同じ頃に <C-c> を打っていた（Run が intrCh より先に結果を取り出した）ら、結果が届いていても中断として扱う。
	// 中断の印だけを下ろして続けると、待ちを挟む再帰のマクロが <C-c> で止まらない（PR #249 の 4 回目の確認の必須）。
	interrupted := w.interrupted
	if e.keys.interrupted(c) {
		interrupted = true
		e.interrupt.Store(false)
		c.intr.Store(false)
		e.keys.dropThrough(interruptKey, c)
	}
	if !e.keys.empty() {
		signal(e.keys.ready)
	}
	bs := w.bs
	if !w.held || !slices.Contains(e.bufs, bs) || c.detached {
		return
	}
	if w.key {
		// Sync の popupKeys のキーの待ち（keysync.go。FR-315）。
		e.keyReplayAnswered(c, w, r, interrupted)
		return
	}
	e.useClient(c)
	// 再生の続きが、待つ間に開いたポップアップの入力の行へ打った（ファインダー。#246）なら、キーの後と同じく input/change で知らせる。
	defer e.afterFloatKey()
	var err error
	switch {
	case interrupted:
		err = command.ErrInterrupted
	case c.inputClosed:
		// 端末の入力が閉じた（端末多重化ソフトのペインが消えた、client が切れた）ら、続けずに止める（PR #249 の 4 回目の確認の必須）。
		err = errors.New("端末が閉じたので、再生を止めました")
	case r.Err == nil:
	case e.host.Withheld(bs.id):
		err = errWithheld(w.ref)
	case errors.Is(r.Err, exthost.ErrTimeout):
		// 操作の失敗と見分けられるように、再生の期限（Config.ReplayTimeout）で待つのをやめたことを書く（#229 のレビューの推奨 4）。
		err = fmt.Errorf("再生の途中で拡張機能 %s の %s の結果を %s 待ったので、再生を止めました", w.ref.Extension, w.ref.ID, e.replayTimeout())
	default:
		err = errExtension(w.ref, r.Err)
	}
	x := &extQuery{e: e, bs: bs, replay: &replayAnswers{list: append(slices.Clone(w.answers), r)}}
	res := bs.state.ResumeReplay(*w.d, x, w.rest, err)
	if errors.Is(res.Err, command.ErrStale) {
		res.Err = fmt.Errorf("拡張機能 %s の %s の結果を待つ間にバッファかカーソルが変わったので、再生を止めました", w.ref.Extension, w.ref.ID)
	}
	if e.handleResult(bs, res) != nil && e.suspension() == nil {
		// 失敗は handleResult が知らせた。展開の残りと貼り付けは捨てる。
		// 続きの中でまた待ちを始めた（ErrQueryPending、ErrShellPending）ときは、失敗ではないので、resumeInput が待ちに引き継ぐ。
		e.remap.reset()
		return
	}
	e.resumeInput(w.heldInput)
}

// suspension は、操作中の client（読み込んでいる client）が、外部のコマンドの実行か再生中の拡張機能の結果か補完の候補を待っていれば、
// 待つ間に止めた入力の置き場を返す。待っていなければ nil。
// feedMapped と handlePaste は、これが nil でなくなったら、残りのキーと貼り付けをここへ移す。
func (e *Editor) suspension() *heldInput {
	c := e.view
	// 外部のコマンドの子孫が起動した待つだけの client のタブを編集している間（FR-117 の reclaimed）は、その client は待っていない。
	// このとき e.shell は前から動いているもので、このキーで始めたものではない。
	if e.shell != nil && e.shell.client == c && !e.shell.reclaimed {
		return &e.shell.heldInput
	}
	if c != nil && c.replay != nil {
		return &c.replay.heldInput
	}
	if c != nil && c.completionJob != nil {
		// 補完の候補を集めている（completion.go。FR-199）。
		return &c.completionJob.heldInput
	}
	return nil
}

// resumeInput は、待ちが終わった後に、止めていたキーマップの展開の残りと貼り付けを続ける。
// 続きの中でまた待ちを始めたら、まだ続けていないものをその待ちに引き継ぐ。
func (e *Editor) resumeInput(in heldInput) {
	if h := e.suspension(); h != nil {
		h.mapped = append(h.mapped, in.mapped...)
		h.paste = in.paste
		return
	}
	if len(in.mapped) > 0 {
		e.feedMapped(in.mapped)
		if h := e.suspension(); h != nil {
			h.paste = in.paste
			return
		}
	}
	if in.paste != "" {
		e.handlePaste(in.paste)
	}
	if e.suspension() == nil {
		// 待ちの間に時間が来た契機は知らせていない（holdReady）ので、入力を続け終えた時点から待ち直す（cursorhold.go。FR-171）。
		e.armHold()
	}
}
