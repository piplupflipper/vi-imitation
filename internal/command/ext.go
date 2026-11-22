package command

import (
	"cmp"
	"errors"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/ex"
	"github.com/piplupflipper/vi-imitation/internal/command/target"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// ErrNoExtensions は、拡張機能の操作を実行しようとしたが、State.Extensions が nil であることを表す。
var ErrNoExtensions = errors.New("command: no extension host")

// ErrDeferred は、Extensions の実装が、結果を待たずに返したことを表す（期限が 0 の設定。FR-18、基本設計 5.4）。
// State は、これを受けた操作を失敗にせず、Result.Deferred に保留して返す。
// 呼び出し側は、結果が届いた時点で State.ApplyDeferred を呼ぶ。
// マクロとドットリピートの再生中（QueryInfo.Replaying）は返さない。返した場合は失敗として扱う（FR-19）。
var ErrDeferred = errors.New("command: extension result deferred")

// ErrQueryPending は、マクロとドットリピートの再生中（QueryInfo.Replaying）に、Extensions の実装が問い合わせを送って、
// 結果を待たずに返したことを表す（FR-19。基本設計 5.4）。誤りではない。
// State は、その操作を Result.Deferred に、再生の残りを ErrBufferSwitched と同じく Result.Remaining に入れて止まる。
// 呼び出し側は、結果が届いたら、Result.Deferred と Result.Remaining を State.ResumeReplay へ渡して再生を続ける。
// 呼び出し側は、待つ間もイベントループを止めずに済む（外部のコマンドの ErrShellPending と同じ形）。
var ErrQueryPending = errors.New("command: replay waits for an extension result")

// Extensions は、拡張機能が提供する操作（提供元が拡張機能の editcmd.Ref）を問い合わせる口である（基本設計 3 章、5.4）。
//
// 問い合わせの実装（exthost への RPC、期限の扱い、拡張機能の状態）は、呼び出し側（internal/editor）が差し込む。
// このパッケージは、結果が返るまで待つ同期の呼び出しとして扱う。
// 期限を超えた、拡張機能が動いていない、といった失敗はエラーで返す。その場合、操作は何もせずに失敗する（基本設計 8 章）。
//
// 期限の 3 つの場合（基本設計 5.4 の表）は、実装が次のように扱う想定である。
//
//   - 期限が正の値（FR-17）: 期限まで待ち、超えたらエラーを返す
//   - 期限が 0（FR-18）: 問い合わせを送って、待たずに ErrDeferred を返す。結果が届いたら State.ApplyDeferred で適用する
//   - info.Replaying が true（FR-19）: 期限の設定に関わらず、結果が届くまで待つ。
//     待たずに ErrQueryPending を返し、結果が届いてから State.ResumeReplay で続けてもよい（internal/editor はこちら）
//
// ドットリピートとマクロの再生では、記録した Ref と引数で、同じ拡張機能へその都度問い合わせ直す（FR-12）。
// このため、拡張機能の側に繰り返しのための処理は要らない。
type Extensions interface {
	// Motion は、モーションの移動先を問い合わせる（motion/resolve）。req は本体のモーションと同じ形で渡す。
	Motion(ref editcmd.Ref, b *buffer.Buffer, req target.Request, info QueryInfo) (target.Result, error)
	// TextObject は、テキストオブジェクトの範囲を問い合わせる（textObject/resolve）。
	TextObject(ref editcmd.Ref, b *buffer.Buffer, req target.Request, info QueryInfo) (target.Span, error)
	// Operator は、範囲に対するオペレーターの編集を問い合わせる（operator/apply）。
	// 返すトランザクションの BaseVersion は、問い合わせたときの b.Version() である必要がある（FR-15）。
	Operator(ref editcmd.Ref, b *buffer.Buffer, q OperatorQuery, info QueryInfo) (buffer.Transaction, error)
	// Action は、対象を取らない操作を実行する（command/execute）。編集しない操作は、Edits が空のトランザクションを返す。
	Action(ref editcmd.Ref, b *buffer.Buffer, q ActionQuery, info QueryInfo) (buffer.Transaction, error)
}

// QueryInfo は、全ての問い合わせに共通して渡す情報である。
type QueryInfo struct {
	// Replaying は、マクロかドットリピートの再生中の問い合わせであるかを表す。
	// true なら、実装は期限の設定に関わらず結果を待つ（FR-19）。
	Replaying bool
}

// queryInfo は、現在の状態の QueryInfo を返す。
func (s *State) queryInfo() QueryInfo { return QueryInfo{Replaying: s.Replaying()} }

// Replaying は、マクロかドットリピートの再生中であるかを返す（FR-19）。
// 再生中は、Extensions の問い合わせにも QueryInfo.Replaying として渡す。
func (s *State) Replaying() bool { return s.replaying || s.playDepth > 0 }

// OperatorQuery は、拡張機能のオペレーターへ渡す引数である。
type OperatorQuery struct {
	// Span は、オペレーターが働く範囲である。
	Span target.Span
	// Cursor は、オペレーターを入力したときのカーソルである。
	Cursor buffer.Position
	// Count は、回数である。
	Count int
	// Char は、対象の後に取った文字の引数である（surround の ysiw) の ')'）。取らなければ 0。
	Char rune
	// Register は、指定したレジスターの名前である。省略したときは 0。
	Register rune
}

// ActionQuery は、拡張機能の対象を取らない操作へ渡す引数である。
type ActionQuery struct {
	Cursor     buffer.Position
	Count      int
	CountGiven bool
	Char       rune
	Register   rune
	// Text は、コマンドラインから呼んだとき（:acp.prompt 説明して）の、コマンドの ID の後の引数の文字列である。
	// キーから呼んだときと、引数を書かなかったときは空である。
	Text string
}

// extCommand は、コマンドラインから呼んだ拡張機能のコマンド a（ex.ActionExtCommand）を問い合わせ、返った編集を適用する。
// 返った編集は、同じコマンドラインの前のコマンドの編集と合わせてアンドゥの 1 単位にする（runCommandLine がグループを開く）。
// 期限 0 で保留して後から適用するときは、それだけで 1 単位にする。
// Vim の Ex コマンドと同じく、ドットリピートには記録しない（マクロに記録したコマンドラインのキーは、再生で同じく呼ぶ）。
// 回数は渡さない（0。省略）。
//
// req: FR-11
func (s *State) extCommand(a ex.Action) error {
	if s.Extensions == nil {
		return ErrNoExtensions
	}
	ref := editcmd.Ref{Extension: a.Extension, ID: a.Command}
	tx, err := s.Extensions.Action(ref, s.buf, ActionQuery{Cursor: s.cursor, Text: a.Text}, s.queryInfo())
	if err != nil {
		return err
	}
	if len(tx.Edits) > 0 {
		s.buf.BeginUndoGroup()
		err = s.applyTx(tx)
		s.buf.EndUndoGroup()
		if err != nil {
			return err
		}
	}
	s.setCursorAfterEx(s.cursor)
	return nil
}

// runExtCommand は、extCommand を実行し、失敗を結果に記録する。
// 拡張機能が結果を保留した（ErrDeferred）場合は、run と同じく、再生中でなければ Result.Deferred に記録する。
func (s *State) runExtCommand(a ex.Action) {
	d := Deferred{Version: s.buf.Version(), Cursor: s.cursor, mode: s.mode, anchor: s.visual.anchor, head: s.visual.head, want: s.want, exCommand: &a}
	err := s.extCommand(a)
	switch {
	case err == nil:
	case errors.Is(err, ErrDeferred) && !s.Replaying() && s.res.Deferred == nil:
		s.cursor, s.want = d.Cursor, d.want
		s.res.Deferred = &d
	case errors.Is(err, ErrQueryPending):
		s.suspendQuery(d, err)
	default:
		s.fail(err)
	}
}

// resolveMotion は、モーションを本体の表か拡張機能で解決する。f、F、t、T の検索は、失敗しても ; と , のために記録する。
func (s *State) resolveMotion(t editcmd.Target, cmd editcmd.Command, op target.Operator) (target.Result, error) {
	req := s.request(cmd, op)
	var (
		res target.Result
		err error
	)
	if t.Ref.Builtin() {
		f, ok := target.LookupMotion(t.Ref.ID)
		if !ok {
			return target.Result{}, failf("unknown motion %q", t.Ref.ID)
		}
		res, err = f(s.buf, req)
	} else {
		if s.Extensions == nil {
			return target.Result{}, ErrNoExtensions
		}
		res, err = s.Extensions.Motion(t.Ref, s.buf, req, s.queryInfo())
	}
	if res.Find.Char != 0 && t.Ref.ID != "find-repeat" && t.Ref.ID != "find-repeat-reverse" {
		s.lastFind = res.Find
	}
	switch {
	case t.Ref.Builtin() && searchMotionIDs[t.Ref.ID]:
		err = s.searchResult(t, res, err)
	case errors.Is(err, target.ErrNoTarget):
		err = failf("%v", err)
	}
	if err == nil && t.Ref.Builtin() && jumpMotions[t.Ref.ID] {
		// 移る前のカーソルをジャンプリストに記録する（Vim 9.1 の setpcmark）。オペレーターの対象（dG）でも記録する。
		s.markJump()
	}
	return res, err
}

// resolveTextObject は、テキストオブジェクトを本体の表か拡張機能で解決する。
// sel は、ビジュアルモードの今の選択の範囲（extSelection）で、オペレーターの後では nil である。
func (s *State) resolveTextObject(t editcmd.Target, cmd editcmd.Command, from buffer.Position, sel *buffer.Range) (target.Span, error) {
	req := s.request(cmd, target.NoOperator)
	req.Cursor = from
	req.Selection = sel
	var (
		sp  target.Span
		err error
	)
	if t.Ref.Builtin() {
		f, ok := target.LookupTextObject(t.Ref.ID)
		if !ok {
			return target.Span{}, failf("unknown text object %q", t.Ref.ID)
		}
		sp, err = f(s.buf, req)
	} else {
		if s.Extensions == nil {
			return target.Span{}, ErrNoExtensions
		}
		sp, err = s.Extensions.TextObject(t.Ref, s.buf, req, s.queryInfo())
	}
	if errors.Is(err, target.ErrNoTarget) {
		return sp, failf("%v", err)
	}
	return sp, err
}

// request は、モーションとテキストオブジェクトへの入力を作る。
func (s *State) request(cmd editcmd.Command, op target.Operator) target.Request {
	return target.Request{
		Cursor:     s.cursor,
		Count:      cmd.Count,
		CountGiven: cmd.CountGiven,
		Char:       cmd.Target.Char,
		Want:       s.want,
		Columns:    s.Columns,
		Screen:     s.ScreenLines,
		Last:       s.lastFind,
		Operator:   op,
		Search:     target.Search{Pattern: cmp.Or(cmd.Target.Pattern, s.Ex.LastPattern), Backward: s.searchBackward},
		// / と ? を <CR> で決めたときだけ 0 でない（finishSearch）。
		SearchSteps: s.searchSteps,
		// コメントのまとまりのテキストオブジェクト（gc）だけが呼ぶ（FR-169）。
		CommentString: s.commentString,
	}
}
