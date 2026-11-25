package command

import (
	"errors"

	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// maxPlayDepth は、マクロの再生の入れ子の上限である。マクロが自分を呼ぶ（qa@aq）と、失敗するまで再帰するための歯止めである。
const maxPlayDepth = 100

// startRecording は、q{reg} である。以後 Feed に渡したキーを記録する（FR-05）。
func (s *State) startRecording(name rune) error {
	if s.recording != 0 {
		return failf("already recording")
	}
	if !recordable(name) {
		return failf("invalid register %q", name)
	}
	s.recording = name
	s.recorded = nil
	s.parser.SetRecording(true)
	return nil
}

// recordable は、マクロの記録先にできる名前であるかを返す。
func recordable(name rune) bool {
	return name >= 'a' && name <= 'z' || name >= 'A' && name <= 'Z'
}

// stopRecording は、記録中の q である。記録したキーの列（止めた q を除く）をレジスターに入れる。
// 大文字の名前は追記する。Vim と同じく、無名のレジスターは変えない。
//
// req: FR-05
func (s *State) stopRecording() {
	if s.recording == 0 {
		return
	}
	keys := s.recorded
	if n := len(keys); n > 0 {
		keys = keys[:n-1]
	}
	// qaq のように何も記録しなかった場合も、Vim と同じく空のテキストを書き込む（レジスターを空にする用途）。
	reg := Register{Text: rawText(keys), keys: append([]editcmd.Key{}, keys...), filled: true}
	name := s.recording
	if p := s.registers.slot(name); p != nil {
		if name >= 'A' && name <= 'Z' {
			reg = p.appendTo(reg)
		}
		*p = reg
	}
	s.recording = 0
	s.recorded = nil
	s.parser.SetRecording(false)
}

// play は、@{reg} と @@ である。レジスターのキーの列を、回数だけ 1 個ずつ Feed と同じ解釈に渡す（FR-05）。
// 途中の操作が失敗したら、そこで再生を止める（Vim と同じ）。
// 再生 1 回（回数の繰り返しを含む）の変更は、Vim の `:help undo-blocks` と同じくアンドゥの 1 単位にまとめる（FR-07）。
//
// 再生はドットリピートの対象になる（FR-57）。@@ は、直前に再生したレジスターの名前へ読み替えた値（@a）を記録する。
// 再生の間は、再生ではない直前の変更（lastChange）を初期値とする再生専用の置き場（State.dot をそのまま使う）を持ち、
// 再生の中の変更がそれを更新し、再生の中の . はそれを繰り返す。対象が @a のときに再生の中の . が @a を再生し直して
// 入れ子の上限まで回ることは、この初期値で避ける（2026-11-18 のオーケストレータの裁定、PR #165）。
// 再生が終わったら（失敗で止まった場合も）、置き場の最後の値が再生ではない変更なら lastChange をそれにし、
// 対象を再生した値にする（finishPlayDot）。
// 入れ子の再生（a の中の @b）も同じ規則に従うので、内側の再生が終わると外側の置き場の対象は @b になる。
// 再生を始める前に失敗した場合（@@ で前が無い、名前が無効、入れ子の上限）は、対象を変えない。
// Vim の @: は持たない（要件定義 1.1）。名前の検査（checkNames）が @: を E354 で失敗させるので、ここには来ない。
//
// req: FR-05, FR-07, FR-57
func (s *State) play(cmd editcmd.Command) error {
	name := cmd.Char
	if name == '@' {
		name = s.lastPlayed
		if name == 0 {
			return failf("no previously used register")
		}
	}
	reg, ok := s.registers.Get(name)
	if !ok {
		return failf("invalid register %q", name)
	}
	if s.playDepth >= maxPlayDepth {
		return failf("macro nested too deeply")
	}
	n := max(cmd.Count, 1)
	if s.playDepth == 0 {
		// 拡張機能の結果を待つために止めたときに、止める前の再生が単位を作ったかを見分けるために控える（suspendQuery）。
		s.playUndoStart = s.buf.UndoTip()
	}
	s.buf.BeginUndoGroup()
	defer s.endPlayGroup()
	s.lastPlayed = name
	dot := cmd
	dot.Char = name
	keys := reg.macroKeys()
	s.noteChange()
	s.dot = s.lastChange
	// . から再生した場合も、マクロの中のキー（i など）は、ドットリピートの再実行としてではなく、ふつうの入力として解釈する。
	replaying := s.replaying
	s.replaying = false
	s.playDepth++
	s.playKeys(keys, n)
	if s.mode == ModeNormal || s.mode.visual() {
		s.runResults(s.parser.Flush())
	}
	s.playDepth--
	s.replaying = replaying
	if suspendsReplay(s.res.Err) {
		// 再生は移った先のバッファで（外部のコマンドの後に）続くので、終わったときにドットリピートの対象を決める印を残りに積む。
		s.res.Remaining = append(s.res.Remaining, ReplaySegment{Dot: &dot})
		return nil
	}
	s.finishPlayDot(&dot)
	return nil
}

// noteChange は、ドットリピートの対象が再生ではない変更なら、それを再生ではない直前の変更（lastChange）にする（FR-57）。
// 変更を記録する箇所（recordDot、インサートモードの記録）は State.dot だけを書くので、再生の始まりと終わりでここを通して拾う。
func (s *State) noteChange() {
	if s.dot != nil && s.dot.Kind != editcmd.KindPlay {
		s.lastChange = s.dot
	}
}

// finishPlayDot は、再生が終わったときに、置き場の最後の値を lastChange へ移し、ドットリピートの対象を再生した値 dot にする（FR-57）。
func (s *State) finishPlayDot(dot *editcmd.Command) {
	s.noteChange()
	s.dot = dot
}

// playKeys は、キーの列 keys を n 回、1 個ずつ Feed と同じ解釈に渡す。失敗したら止める。
//
// 途中の Ex コマンドが確認を求めた（Result.Confirm）ら、Vim 9.1 と同じく、マクロの次のキーを答えとして読む
// （"y" なら続きを実行する）。答えのキーが無ければ、確認は Result.Confirm のまま呼び出し側へ渡す。
//
// 拡張機能の操作が ErrBufferSwitched を返した（カーソルのあるバッファが別のバッファへ移った）ら、
// 残りのキーを Result.Remaining に積んで止める。:{range}! と :r ! が外部のコマンドの実行を求めた（ErrShellPending）ときも同じである。呼び出し側は、移った先のバッファの State の ContinueReplay へ渡す。
func (s *State) playKeys(keys []editcmd.Key, n int) {
	for iter := range n {
		if s.interrupted() {
			s.stopInterrupted()
			return
		}
		if len(keys) == 0 {
			return
		}
		for i := 0; i < len(keys); i++ {
			if s.interrupted() {
				s.stopInterrupted()
				return
			}
			s.feed(keys[i])
			if cf := s.res.Confirm; cf != nil && i+1 < len(keys) {
				s.res.Confirm, s.res.Err = nil, nil
				i++
				if keys[i] == editcmd.Rune('y') {
					res, err := cf.Resume(s.buf, &s.Ex)
					s.applyExResult(res, err)
				}
			}
			if s.res.Err != nil {
				if suspendsReplay(s.res.Err) {
					s.res.Remaining = append(s.res.Remaining, ReplaySegment{Keys: keys[i+1:], Count: 1, Depth: s.playDepth})
					if rest := n - iter - 1; rest > 0 {
						s.res.Remaining = append(s.res.Remaining, ReplaySegment{Keys: keys, Count: rest, Depth: s.playDepth})
					}
				}
				return
			}
		}
	}
}

// stopInterrupted は、中断（Interrupted）で再生を止め、ErrInterrupted を記録して、打ちかけのキーとモードを取り消す。
//
// 再生はキーの間のどこでも止まるので、:s/a/aa/<CR> の ":" の直後に止まるとコマンドラインモードのまま残り、
// 利用者が続けて打つキー（ox<Esc> など）がコマンドラインへ入る（#202）。
// Vim 9.1 は、レジスターの再生の途中の CTRL-C を、インサートモードとコマンドラインモードでは <Esc>、
// それ以外では CTRL-C として読ませる（src/getchar.c の vgetorpeek()）。どちらもモードと打ちかけのキーを取り消すので、
// ここでは <Esc> を 1 個渡して、ノーマルモードの打ち始めへ戻す。
// 回数付きの挿入（3ix<Esc>）は、Vim 9.1 の ins_esc() が got_int で回数を 0 にするのと同じく、入れた 1 回分で止める
// （src/edit.c）。<Esc> で回数分くり返すと、大きな回数では中断がかえって長い処理になる。
func (s *State) stopInterrupted() {
	s.fail(ErrInterrupted)
	if s.mode == ModeInsert {
		s.insert.repeat = 1
	}
	if s.mode != ModeNormal || s.parser.Pending() {
		s.feed(editcmd.Esc)
	}
}

// ReplaySegment は、再生の残りの 1 区切りで、Keys を Count 回再生することを表す。
//
// Dot が nil でなければ、キーを再生する代わりに、ドットリピートの対象を Dot にする印である（FR-57）。
// 移る前のバッファで始めた再生（@a）が終わる位置に置き、移った先で再生を終えたときの対象を決める。
//
// Depth は、Keys を再生していたときの入れ子の深さ（playDepth）である。続き（ContinueReplay、ResumeReplay）はこの深さで再生するので、
// 待ちを挟む再帰のマクロ（qa<Space>ff<Esc>@aq。拡張機能の結果、外部のコマンド、別のバッファへの移動で止まる）も、
// 待ちを挟まない再帰と同じく maxPlayDepth で止まる（PR #249 の 4 回目の確認の必須）。0 なら 1 として扱う。
type ReplaySegment struct {
	Keys  []editcmd.Key
	Count int
	Dot   *editcmd.Command
	Depth int
}

// ErrBufferSwitched は、マクロの再生の途中で、拡張機能の操作（再生中の command/execute の結果の open）が
// カーソルのあるバッファを別のバッファへ移したことを表す。Extensions の実装が返す。
// 再生は、移った先のバッファで続ける（Vim と同じく、マクロのキーはカーソルのあるバッファへ入る）。
// State は残りのキーを Result.Remaining に入れて止まり、呼び出し側が移った先の State の ContinueReplay へ渡す。
var ErrBufferSwitched = errors.New("command: replay continues in another buffer")

// ContinueReplay は、別のバッファの State が ErrBufferSwitched で止めた再生の残り segs を、この State で続ける（FR-05、FR-19）。
// 再生中として扱い（Replaying が true）、変更はアンドゥの 1 単位にまとめる。
// 区切りの Dot の印に来たら、ドットリピートの対象をその値にする。途中で失敗したら、残りの印のうち最後のもの
// （いちばん外側の再生）を対象にする（FR-57）。
// 外部のコマンドの出力を入れた（FinishShell）直後なら、残りの変更を、出力と止める前の再生の単位へつなぐ（#235。Vim の @a と同じく
// 再生の全体を 1 回の u で戻す）。
//
// req: FR-05, FR-07, FR-57
func (s *State) ContinueReplay(segs []ReplaySegment) Result {
	s.res = Result{}
	j := s.shellJoin
	s.shellJoin = nil
	if j != nil && s.buf.JoinUndoGroup(j.tip) {
		s.playUndoStart = j.start
	} else {
		if j == nil {
			s.buf.BeginUndoGroup()
		}
		s.playUndoStart = s.buf.UndoTip()
	}
	s.playDepth++
	s.playSegments(segs)
	s.playDepth--
	if s.mode == ModeNormal || s.mode.visual() {
		s.runResults(s.parser.Flush())
	}
	s.endPlayGroup()
	return s.take()
}

// AbortReplay は、拡張機能の結果を待つために止めた再生の残り segs を、err で止める（FR-19）。
// segs のキーは再生せず、segs の Dot の印のうち最後のものをドットリピートの対象にする（ContinueReplay の失敗と同じ。FR-57）。
// 拡張機能のポップアップのキーの view/keySync（internal/editor の keysync.go）の結果を得られなかったときに使う。
//
// req: FR-19
func (s *State) AbortReplay(segs []ReplaySegment, err error) Result {
	s.res = Result{}
	s.fail(err)
	s.stopSegments(segs)
	return s.take()
}

// playSegments は、再生の残り segs を先頭から再生する（ContinueReplay と ResumeReplay）。
// 区切りの Dot の印に来たら、ドットリピートの対象をその値にする。
// 再生がまた止まった（suspendsReplay）ら、まだ再生していない区切りを Result.Remaining に足す。
// 失敗したら（呼ぶ前に失敗していた場合も）、残りの印のうち最後のもの（いちばん外側の再生）を対象にする（FR-57）。
func (s *State) playSegments(segs []ReplaySegment) {
	for i, seg := range segs {
		if s.res.Err != nil {
			s.stopSegments(segs[i:])
			return
		}
		if seg.Dot != nil {
			s.finishPlayDot(seg.Dot)
			continue
		}
		// 止める前に再生していた深さで続ける（ReplaySegment.Depth）。
		depth := s.playDepth
		s.playDepth = max(seg.Depth, 1)
		s.playKeys(seg.Keys, seg.Count)
		s.playDepth = depth
	}
}

// segmentsDepth は、再生の残り segs の最初のキーの区切りの深さを返す（区切りが無ければ 1）。
// ResumeReplay が、止めた操作を実行し直す深さに使う（止めた操作は、残りの最初の区切りと同じ深さで再生していた）。
func segmentsDepth(segs []ReplaySegment) int {
	for _, seg := range segs {
		if seg.Dot == nil {
			return max(seg.Depth, 1)
		}
	}
	return 1
}

// stopSegments は、再生が止まったときに、まだ再生していない区切り segs を扱う（playSegments）。
func (s *State) stopSegments(segs []ReplaySegment) {
	if suspendsReplay(s.res.Err) {
		s.res.Remaining = append(s.res.Remaining, segs...)
		return
	}
	for _, rest := range segs {
		if rest.Dot != nil {
			s.finishPlayDot(rest.Dot)
		}
	}
}

// ResumeReplay は、再生の途中で拡張機能の結果を待つために止めた（ErrQueryPending）再生を、届いた結果で続ける（FR-19。基本設計 5.4）。
//
// d と segs は、止めたときの Result.Deferred と Result.Remaining である。ext は、d を実行し直す間だけ State.Extensions の代わりに使う
// 問い合わせ先で、止めた問い合わせ（と、同じ操作の中のそれより前の問い合わせ）には届いた結果を返す（ApplyDeferred と同じ）。
// 実行し直した後は、ふだんの問い合わせ先で segs を再生する。どちらも再生中として扱う（Replaying が true）。
//
// d を実行し直すのは、バッファのバージョン番号、カーソル、モードが止めたときと同じ場合だけである（ApplyDeferred と同じ条件）。
// 待つ間に拡張機能の編集や外での変更の読み直しでバッファが変わっていれば、ErrStale で再生を止める。
// err が nil でなければ（結果を得られない、期限を超えた、中断した）、d を実行せずに err で再生を止める。
// 止めたときは、segs の Dot の印のうち最後のものをドットリピートの対象にする（ContinueReplay の失敗と同じ）。
// 実行し直した操作か segs の中でまた止まれば、ErrQueryPending などと残りが Result に入る。
//
// 止める前の再生が作ったアンドゥの単位が履歴の末尾のままなら、続きの変更をそこへつなぎ（buffer.Buffer.JoinUndoGroup）、
// 再生の全体を 1 回の u で戻せるようにする（Vim の @a と同じ）。
// 待つ間に別の client の操作でドットリピートの値、直前の検索、直前の :s、直前の f、@@ のレジスターが替わっていても、
// 止めたときの値に戻してから続ける。レジスターは client の間で共有するもの（FR-58）なので戻さない。
//
// req: FR-07, FR-19
func (s *State) ResumeReplay(d Deferred, ext Extensions, segs []ReplaySegment, err error) Result {
	s.res = Result{}
	if r := d.replay; r != nil {
		// 待つ間に別の client の操作でドットリピートの値などが替わっていても、止めたときの値で続ける（#229 のレビュー）。
		s.setRepeatState(r.repeat)
	}
	// 止める前の再生が作ったアンドゥの単位が末尾のまま（待つ間に誰もこのバッファを変えていない）なら、そこへつなぐ。
	// 待つ間に変わっていれば、バージョン番号が変わっているので deferredReady が ErrStale で止める。
	if r := d.replay; r != nil && r.undoTip != 0 && s.buf.JoinUndoGroup(r.undoTip) {
		s.playUndoStart = r.undoStart
	} else {
		if r == nil || r.undoTip == 0 {
			s.buf.BeginUndoGroup()
		}
		s.playUndoStart = s.buf.UndoTip()
	}
	depth := s.playDepth
	s.playDepth = segmentsDepth(segs)
	switch {
	case err != nil:
		s.fail(err)
	case !s.deferredReady(d):
		s.fail(ErrStale)
	default:
		saved := s.Extensions
		s.Extensions = ext
		s.rerunDeferred(d)
		s.Extensions = saved
	}
	s.playSegments(segs)
	s.playDepth = depth
	if s.mode == ModeNormal || s.mode.visual() {
		s.runResults(s.parser.Flush())
	}
	s.endPlayGroup()
	return s.take()
}

// endPlayGroup は、マクロの再生全体をまとめたアンドゥのグループを閉じる。
// 再生がインサートモードで終わった場合（"iX" を @ で再生）は、インサートモードを抜けるときに一緒に閉じる。
func (s *State) endPlayGroup() {
	if s.mode == ModeInsert {
		s.insert.extraGroups++
		return
	}
	s.buf.EndUndoGroup()
}

// recordDot は、実行した値を、ドットリピートの対象であれば記録する（FR-06）。
// インサートモードへ移る値は、インサートモードを抜けるときに finishInsert が記録する。
func (s *State) recordDot(cmd editcmd.Command) {
	if cmd.Mode == editcmd.ModeInsert || !repeatable(cmd) {
		return
	}
	c := cmd
	s.dot = &c
}

// repeatable は、ドットリピートで繰り返す値であるかを返す。
// ヤンク、移動、アンドゥ、モードの切り替えは繰り返さない。拡張機能のオペレーターと操作は、全て繰り返す（FR-12）。
// マクロの再生（KindPlay）は繰り返すが、変更が起きたかを問わず再生の終わりに play が記録するので、ここでは false を返す（FR-57）。
//
// 提供者へ振り分ける本体の操作（editcmd.ProviderExtension。LSP と DAP の操作。FR-94）のうち、対象を取らない操作は繰り返さない。
// ホバー、移動、一覧、ブレークポイント、デバッグの続行は、テキストを変えないからである（FR-06。Vim の K と <C-]> も記録しない）。
// 名前の変更とコードアクションもテキストを変えうるが、編集は提供者が後から buffer/edit で届け、ドットリピートの記録の対象にならない
// （基本設計 6 章の冒頭）。また、キーで呼ぶ名前の変更は新しい名前を持たない（コマンドラインの :rename は Ex コマンドなので、もともと記録しない）。
// このため、これらも記録しない（#203 のレビュー）。
// 範囲を取る整形（gq{モーション}）はオペレーターで、Vim の gq と同じく繰り返す。
//
// req: FR-06
func repeatable(cmd editcmd.Command) bool {
	switch cmd.Kind {
	case editcmd.KindOperator:
		return !isYank(cmd.Operator)
	case editcmd.KindAction:
		if cmd.Action.Provider() {
			return false
		}
		return !cmd.Action.Builtin() || repeatableActions[cmd.Action.ID]
	case editcmd.KindMotion, editcmd.KindRecordStart, editcmd.KindRecordStop, editcmd.KindPlay, editcmd.KindRepeat:
	}
	return false
}

// repeat は、. である。直前の変更の値をもう一度実行する（FR-06）。
//
// 回数を付けると、記録した値の回数を置き換える（2d3w の後の 3. は d3w）。置き換えた回数は以後の . にも残る。
// ビジュアルモードのオペレーターは、カーソルから同じ大きさの範囲に対して繰り返す。
// 拡張機能の操作は、記録した Ref と引数で同じ拡張機能へ問い合わせ直す（FR-12）。
// マクロの再生（@a）は、レジスターの今の中身を再生する。3@a の後の . は 3 回、2. は 2 回再生する（FR-57）。
//
// req: FR-06, FR-12, FR-57
func (s *State) repeat(cmd editcmd.Command) error {
	if s.dot == nil {
		return failf("no previous change")
	}
	d := *s.dot
	if cmd.CountGiven {
		d.Count, d.CountGiven = cmd.Count, true
	}
	// マクロの中の . は、再生（play）が戻した値へ戻す。
	replaying := s.replaying
	s.replaying = true
	defer func() { s.replaying = replaying }()
	if d.Kind == editcmd.KindOperator && d.Target.Kind == editcmd.TargetNone {
		rg := s.regionFromExtent(s.dotVisual)
		return s.change(d, func() error { return s.regionOperator(d, rg) })
	}
	if s.dotLines.cmd == s.dot {
		// 選んだ行ごとに同時に入力した操作（FR-141）は、カーソルの行から同じ数の行に位置を置き直す（insertLines）。
		lines := s.repeatLines
		s.repeatLines = s.dotLines.lines
		defer func() { s.repeatLines = lines }()
	}
	return s.execute(d)
}
