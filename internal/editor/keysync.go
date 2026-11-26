package editor

import (
	"errors"
	"fmt"
	"slices"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
)

// この節は、拡張機能のポップアップとペインのキー（contributes.popupKeys）の、レジスターへ入れる結果の扱いを持つ（FR-19、FR-315。
// 基本設計 5.3 の「#432 のレジスターへ入れる口」）。
//
//   - キーの番号（Origin）: view/key に番号を付け、その番号の register/set の知らせとクリップボードを、キーを打った client へ送る
//   - 再生中の Sync のキー: view/key の代わりに view/keySync を送り、応答を待つ間は再生を止める（replayWait）。応答の中身を
//     レジスターへ書いてから、再生の残りを続ける

// maxKeyOrigins は、覚えておくキーの番号の数である（仮）。古いものから忘れ、忘れた番号の register/set は操作中の client へ送る。
// 拡張機能はキーを受けてすぐ（DAP なら評価の往復の後）register/set を送るので、続けて打つキーの数より十分に大きければよい。
const maxKeyOrigins = 256

// keyOrigins は、view/key に付けた番号と、そのキーを打った client の組を、新しい順に覚えたものである。
type keyOrigins struct {
	next uint64
	// list は、古い順の組である。maxKeyOrigins を超えたら先頭から捨てる。
	list []keyOrigin
}

type keyOrigin struct {
	n      uint64
	client *clientView
}

// noteKeyOrigin は、今処理しているキー（読み込んでいる client のキー）に番号を付けて覚え、その番号を返す。
// ペインの部品の client で打ったキーは、元の編集の client のキーとして処理する（基本設計 1.2）ので、元の client を覚える。
func (e *Editor) noteKeyOrigin() uint64 {
	o := &e.origins
	o.next++
	o.list = append(o.list, keyOrigin{n: o.next, client: e.view})
	if len(o.list) > maxKeyOrigins {
		o.list = append(o.list[:0:0], o.list[len(o.list)-maxKeyOrigins:]...)
	}
	return o.next
}

// originClient は、番号 n のキーを打った client を返す。番号が 0 か、覚えていないか、その client が終わっていれば、
// 操作中の client を返す（FR-56 の注の表。PR #433 のレビューの推奨 2）。
func (e *Editor) originClient(n uint64) *clientView {
	if n != 0 {
		for i := len(e.origins.list) - 1; i >= 0; i-- {
			if k := e.origins.list[i]; k.n == n {
				if k.client != nil && !k.client.detached {
					return k.client
				}
				break
			}
		}
	}
	return e.op
}

// sendKeyReplay は、再生中の Sync のキー（popupKeys の id、params p）を view/keySync で送り、結果を待たずに command.ErrQueryPending を返す
// （FR-19、FR-315）。State は ErrQueryPending を KeyHook の誤りとして受け、再生の残りを Result.Remaining に入れて止まる。
// 結果は keyReplayAnswered が受け、レジスターへ書いてから残りを続ける。期限は再生の期限（Config.ReplayTimeout）である。
//
// req: FR-19, FR-315
func (e *Editor) sendKeyReplay(f *floatState, id string, p ext.ViewKeyParams) error {
	ref := editcmd.Ref{Extension: f.ext, ID: id}
	e.noteViewClient(f, false)
	e.tagSeq++
	tag := queryTag(e.tagSeq)
	c := e.view
	q, err := e.host.Send(f.ext, ext.MethodViewKeySync, p, exthost.SendOptions{Tag: tag, Timeout: max(e.cfg.ReplayTimeout, 0)})
	if err != nil {
		return errExtension(ref, err)
	}
	if old := c.replay; old != nil {
		old.q.Cancel()
	}
	c.replay = &replayWait{q: q, tag: tag, bs: e.cur, client: c, ref: ref, key: true, float: f}
	return command.ErrQueryPending
}

// keyReplayAnswered は、client c が待っていた view/keySync の結果 r を受け、応答の中身をレジスターへ書いてから、止めた再生の残り
// （replayWait.rest）をカーソルのあるバッファで続ける（command.State.ContinueReplay）。
// 結果を得られなかった（期限を超えた、拡張機能が終わった、<C-c> で取り消した）か、書けなかった（使えない名前）ら、
// その位置で再生を止める（command.State.AbortReplay。FR-19）。
//
// req: FR-19, FR-315
func (e *Editor) keyReplayAnswered(c *clientView, w *replayWait, r *exthost.QueryResult, interrupted bool) {
	e.useClient(c)
	var err error
	var res ext.ViewKeyResult
	switch {
	case interrupted:
		err = command.ErrInterrupted
	case c.inputClosed:
		err = errors.New("端末が閉じたので、再生を止めました")
	case errors.Is(r.Err, exthost.ErrTimeout):
		err = fmt.Errorf("再生の途中で拡張機能 %s の %s の結果を %s 待ったので、再生を止めました", w.ref.Extension, w.ref.ID, e.replayTimeout())
	case r.Err != nil:
		err = errExtension(w.ref, r.Err)
	default:
		if derr := r.Decode(&res); derr != nil {
			err = errExtension(w.ref, derr)
		}
	}
	if err == nil && res.Register != nil {
		if p := *res.Register; p.Validate() != nil {
			err = errExtension(w.ref, p.Validate())
		} else {
			err = e.writeRegister(p)
		}
	}
	// 分割したペインで打ったキーなら、続きもそのペインで再生する。ペインのキーは、処理する間だけペインを元の client のフォーカスのある
	// ポップアップにし、処理し終えたら元のタブへ戻す（paneInput）ので、待つ間にカーソルのあるバッファは元のタブに戻っている。
	// 戻したまま続けると、残りのキー（qqj"Azykq の k）を元のタブで再生し、ペインのキーが効かなかった。
	inPane := false
	if f := w.float; f != nil && f.pane && slices.Contains(e.panes, f) && e.float != f {
		e.enterPane(f)
		inPane = true
	}
	next := e.cur
	var out command.Result
	if err != nil {
		out = next.state.AbortReplay(w.rest, err)
	} else {
		out = next.state.ContinueReplay(w.rest)
	}
	failed := e.handleResult(next, out) != nil && e.suspension() == nil
	e.afterFloatKey()
	if inPane {
		e.leavePane()
	}
	if failed {
		e.remap.reset()
		return
	}
	e.resumeInput(w.heldInput)
}
