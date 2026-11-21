package command

import (
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/target"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// visualState は、ビジュアルモードの状態である。
//
// 選択は anchor と head の 2 つの端で表し、カーソルとは別に持つ（#263）。
// 普段は head をカーソルと同じに保つ。カーソルを動かす操作（モーション、テキストオブジェクト、ページ単位のスクロール、
// 拡張機能の操作と呼び出し側の SetCursor がカーソルを動かしたとき）は、動かした後に head をカーソルに合わせる（followCursor）。
// head をカーソルから離すのは、カーソルを動かさずに選択を作る操作（select-all）だけである。
// 選択の範囲（Selection）とオペレーターの範囲はカーソルではなく head から作るので、それらの操作の後の各キーは、
// ビジュアルモードの汎用の規則のとおりに働く（y は全体をヤンクし、j はカーソルから動いて head をカーソルに合わせる）。
type visualState struct {
	// anchor は、選択の動かない側の端である。
	anchor buffer.Position
	// head は、選択の動く側の端である（Vim の VIsual の反対の端で、Vim ではカーソルと同じ）。
	head buffer.Position
	// start と end は、最後に抜けたビジュアルモードの選択の始まりと終わりの位置（マーク '< と '>）である。
	start, end buffer.Position
	marked     bool
}

// visualExtent は、ドットリピートでビジュアルモードのオペレーターを繰り返すための、選択の大きさである（Vim と同じ）。
// 行単位なら行の数、1 行の文字単位なら文字の数、複数の行の文字単位なら行の数と終わりの位置のバイト数を記録する。
type visualExtent struct {
	linewise bool
	lines    int
	chars    int
	endByte  int
	eol      bool // 1 行なら、その行の改行まで選んでいた。複数の行なら、$ で行末を選んでいた
}

// Selection は、ビジュアルモードの選択の始まりと終わり（どちらも含む）と、行単位かどうかを返す。
// ビジュアルモードでなければ ok が false である。
// 端は行末の後（Byte が行の長さ）にあることがあり、そのときはその行の改行を選んでいることを表す（visualCursor）。
// 選択は anchor と head から作り、カーソルの位置には依らない（visualState の doc）。カーソルは選択の端に無いことがある（select-all の後）。
// 両端はバッファの中に丸めて返す。外からの編集の後に ShiftPositions を通らずにバッファが縮んでも（読み直しなど）、
// 呼び出し側（描画、editor/state、オペレーターの範囲）がバッファの外の行を読まない。
func (s *State) Selection() (start, end buffer.Position, linewise, ok bool) {
	if !s.mode.visual() {
		return buffer.Position{}, buffer.Position{}, false, false
	}
	start, end = s.clampPos(s.visual.anchor), s.clampPos(s.visual.head)
	if end.Less(start) {
		start, end = end, start
	}
	return start, end, s.mode == ModeVisualLine, true
}

// followCursor は、ビジュアルモードなら、選択の動く側の端（head）をカーソルに合わせる。
// カーソルを動かす操作の後に呼ぶ（visualState の doc）。
func (s *State) followCursor() {
	if s.mode.visual() {
		s.visual.head = s.cursor
	}
}

// actVisual は、v と V である。ノーマルモードではビジュアルモードへ入り、
// ビジュアルモードでは、同じ種類なら抜け、違う種類なら切り替える（選択の端はそのまま）。
func actVisual(m Mode) actionFunc {
	return func(s *State, _ editcmd.Command) error {
		switch {
		case s.mode == m:
			s.exitVisual()
		case s.mode.visual():
			s.mode = m
		default:
			s.mode = m
			s.visual.anchor, s.visual.head = s.cursor, s.cursor
			s.parser.SetVisual(true)
		}
		return nil
	}
}

// actSelectAll は、select-all である。文字単位のビジュアルモードに入り（行単位のビジュアルモードの中なら文字単位に切り替え）、
// バッファの全体を選ぶ。選択は、最初の行の最初の文字から、最後の行の最後の文字までで、最後の行の改行は含めない
// （Vim 9.1 で ggvG$y がレジスターに最後の改行を入れないことを確かめた）。
//
// カーソルと望みの列は動かさない。画面もスクロールせず、ジャンプリストにも記録しない。
// anchor をバッファの先頭に、head を最後の行の行末の後（ggvG$ の後のカーソルと同じ位置。visualCursor）に置くだけで、
// その後の各キーの振る舞いはビジュアルモードの汎用の規則から決まる（visualState の doc。#263）。
// 最後の行には改行が無いので、行末の後の端は改行を選ばない（charRegion）。
// head を最後の文字でなく行末の後に置くので、1 回目の > で行が伸びても、ggvG$> と同じく最後の文字が選択に残る（FR-77）。
// > を重ねると、FR-77 が両端をずらす前のバイトの位置へ戻すので、2 回目から字下げ 1 段ごとに最後の文字が 1 文字ずつ選択から外れる
// （Vim 9.1 の ggvG$>gv>gvy と同じ結果。PR #265 のレビュー）。
//
// req: FR-130
func actSelectAll(s *State, _ editcmd.Command) error {
	if !s.mode.visual() {
		s.parser.SetVisual(true)
	}
	s.mode = ModeVisual
	s.visual.anchor = pos(0, 0)
	s.visual.head = s.lineEnd(s.lastLine())
	return nil
}

// actSelectAllLines は、select-all-lines（既定のキーは <Leader>a）である。ノーマルモードで gg、V、G を続けて打ったのと同じく、
// 最初の行（回数があればその行）から最後の行までを、行単位のビジュアルモードで選ぶ（FR-220。#364）。
//
// gg と G は、キーで打ったときと同じモーション（file-start と file-end）で動かす。このため、カーソルの位置
// （最後の行の最初の空白でない文字。'startofline' がオンの Vim 9.1 と同じで、Neovim 0.12.5 の既定とは違う）、
// ジャンプリストへの記録（gg の前の位置と、G の前の最初の行の位置）、抜けた後のマーク '< と '>、gv で選び直す範囲は ggVG と同じになる。
// 回数は gg に渡す。オーナーの nvim の割り当て（ノーマルモードの <Leader>a を ggVG にする）で、3<Leader>a が 3ggVG になるのと同じである。
// select-all（FR-130）と違い、カーソルを動かし、ジャンプリストにも記録する。
//
// req: FR-220
func actSelectAllLines(s *State, cmd editcmd.Command) error {
	motion := func(id string, count int, given bool) editcmd.Command {
		return editcmd.Command{
			Kind: editcmd.KindMotion, Count: count, CountGiven: given,
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: editcmd.Ref{ID: id}},
		}
	}
	// ノーマルモードだけの操作である（input の表の modes。設定の keymap でもビジュアルモードには割り当てられない）。
	if err := s.moveMotion(motion("file-start", cmd.Count, cmd.CountGiven)); err != nil {
		return err
	}
	if err := actVisual(ModeVisualLine)(s, cmd); err != nil {
		return err
	}
	return s.visualMotion(motion("file-end", 1, false))
}

// actVisualSwap は、ビジュアルモードの o である。選択の両端（anchor と head）を入れ替え、カーソルを新しい head へ移す。
// カーソルが head にあれば（普段の状態）、カーソルを選択の反対の端へ移すことになる（Vim の v_o）。
func actVisualSwap(s *State, _ editcmd.Command) error {
	if !s.mode.visual() {
		return failf("not in visual mode")
	}
	// 端がバッファの外を指していても、カーソルはバッファの中に置く（Selection の doc）。
	s.visual.anchor, s.visual.head = s.clampPos(s.visual.head), s.clampPos(s.visual.anchor)
	s.cursor = s.visual.head
	s.updateWant()
	return nil
}

// exitVisual は、ビジュアルモードを抜けてノーマルモードへ戻る。選択の端をマーク '< と '> に記録する。
func (s *State) exitVisual() {
	if start, end, _, ok := s.Selection(); ok {
		s.visual.start, s.visual.end, s.visual.marked = start, end, true
	}
	s.mode = ModeNormal
	s.parser.SetVisual(false)
	s.setCursorNormal(s.cursor)
}

// mark は、Ex のアドレスのマーク（'< と '>）を行に解決する。
func (s *State) mark(name rune) (int, bool) {
	if !s.visual.marked {
		return 0, false
	}
	switch name {
	case '<':
		return min(s.visual.start.Line, s.lastLine()), true
	case '>':
		return min(s.visual.end.Line, s.lastLine()), true
	}
	return 0, false
}

// visualMotion は、ビジュアルモードのモーションとテキストオブジェクトで、選択を動かす。
// モーションはカーソルから動き、動いた後に head をカーソルに合わせる（visualState の doc）。
// 失敗したモーションはカーソルを動かさないので、head も動かさない。
func (s *State) visualMotion(cmd editcmd.Command) error {
	var err error
	switch {
	case cmd.Target.Kind == editcmd.TargetTextObject:
		err = s.visualTextObject(cmd)
	case cmd.Target.Kind == editcmd.TargetMotion && cmd.Target.Ref.Builtin() && cmd.Target.Ref.ID == "right":
		err = s.visualRight(cmd)
	default:
		err = s.moveMotion(cmd)
	}
	if err == nil {
		s.followCursor()
	}
	return err
}

// visualRight は、ビジュアルモードの l である。Vim と同じく、最後の文字から行末の後（NUL の位置）まで進める
// （Vim の nv_right() は、'selection' が old でないビジュアルモードで行末の後へ移る）。
// 行末の後（空の行の先頭を含む）にいれば失敗する。ab|c と d の vld は、c と改行を消して abd にする。
func (s *State) visualRight(cmd editcmd.Command) error {
	if s.cursor.Byte >= len(s.line(s.cursor.Line)) {
		return failf("%v", target.ErrNoTarget)
	}
	// オペレーターの後の l と同じく、行末の位置まで進める。
	res, err := s.resolveMotion(cmd.Target, cmd, target.AnyOperator)
	if err != nil {
		return err
	}
	s.cursor = s.clampPos(res.Pos)
	s.want = res.Want
	return nil
}

// visualTextObject は、ビジュアルモードのテキストオブジェクトである。
// 選択が 1 文字（anchor と head が同じ）なら、選択をテキストオブジェクトの範囲にする。
// そうでなければ、head の次の文字から始まるテキストオブジェクトの終わりまで選択を広げる。
// どちらでも、カーソルは選択の終わりへ移る（head は visualMotion がカーソルに合わせる）。
// 行単位のテキストオブジェクト（ip）は、文字単位のビジュアルモードを行単位に切り替える。
//
// Vim は、選択を広げる方向（カーソルが端より前か）や、i( を重ねたときの外側の括弧への広げ方を細かく扱うが、
// ここでは前へ広げることだけを扱う。
func (s *State) visualTextObject(cmd editcmd.Command) error {
	from := s.clampPos(s.visual.head) // バッファの外を指していても読まない（Selection の doc）
	extend := s.visual.head != s.visual.anchor
	if extend {
		line := s.line(from.Line)
		switch {
		case nextChar(line, from.Byte) < len(line):
			from.Byte = nextChar(line, from.Byte)
		case from.Line < s.lastLine():
			from = pos(from.Line+1, 0)
		default:
			return failf("no more text")
		}
	}
	sp, err := s.resolveTextObject(cmd.Target, cmd, from)
	if err != nil {
		return err
	}
	end := sp.End
	if sp.Kind == target.Exclusive && end != sp.Start {
		if end.Byte > 0 {
			end.Byte = prevChar(s.line(end.Line), end.Byte)
		} else if end.Line > 0 {
			end = s.lineEnd(end.Line - 1)
		}
	}
	if !extend {
		s.visual.anchor = sp.Start
	}
	if sp.Kind == target.Linewise && s.mode == ModeVisual {
		s.mode = ModeVisualLine
	}
	s.setCursorNormal(end)
	return nil
}

// visualRegion は、ビジュアルモードの選択を、オペレーターが働く範囲にする。
// 文字単位の選択の終わりが行末（空の行か、$ で動いた行）なら、その後の改行も含める（Vim の 'selection' が inclusive）。
func (s *State) visualRegion() region {
	start, end, linewise, _ := s.Selection()
	if linewise {
		// Vim は、行単位の選択の端（VIsual）が始まりなら列を 0 にし、カーソルが始まりならカーソルの列を残す（do_pending_operator）。
		// Vim のカーソルは選択の動く側の端なので、ここでは head で判定する。
		// アンドゥの後のカーソルがこの位置になる（y の後のカーソルは FR-129 で動かさない。visualOperator）。
		// head が行末の後（NUL の位置）にあれば、その列を残す（vkC のアンドゥの後のカーソル。#86）。
		at := pos(start.Line, 0)
		if s.visual.head.Less(s.visual.anchor) {
			at = s.clampPos(s.visual.head)
		}
		return region{
			start:    at,
			r:        buffer.Range{Start: pos(start.Line, 0), End: s.lineEnd(end.Line)},
			linewise: true,
		}
	}
	// 終わりが行末の後（空の行の先頭を含む）なら、その行の改行まで選ぶ。始まりが行末の後なら、その行の改行から選ぶ。
	eol := end.Byte >= len(s.line(end.Line))
	return s.charRegion(start, end, eol)
}

// visualCursor は、モーションで移った後のビジュアルモードのカーソルを、Vim が置く位置で返す。
// Vim は、'selection' が inclusive のビジュアルモードで、望みの列が行の幅に届かない行（j や k で移った短い行、$ の後）では、
// カーソルを最後の文字ではなく行末の後（NUL の位置）へ置く（coladvance()）。ここでも同じ位置へ置き、
// 選択の端（Selection）とオペレーターの範囲を Vim に合わせる（#89。a と bcd|e の vkd は a を残す）。
// 行末の後から h で戻る列（vkh）も、この位置から数えるので Vim と同じになる。
// l で行末の後へ移るのは visualRight が扱う。ビジュアルモードを抜けると、カーソルは最後の文字へ戻る（exitVisual）。
func (s *State) visualCursor() buffer.Position {
	line := s.line(s.cursor.Line)
	if line != "" && s.cursor.Byte == lastCharStart(line) &&
		(s.want == target.WantEOL || s.want >= s.columns().Column(line, len(line))) {
		return pos(s.cursor.Line, len(line))
	}
	return s.cursor
}

// charRegion は、start から end の文字まで（eol なら end の行の改行まで）の文字単位の範囲を返す。
func (s *State) charRegion(start, end buffer.Position, eol bool) region {
	stop := pos(end.Line, nextChar(s.line(end.Line), end.Byte))
	if eol && end.Line < s.lastLine() {
		stop = pos(end.Line+1, 0)
	}
	return region{start: start, r: buffer.Range{Start: start, End: stop}}
}

// extentOf は、選択の大きさを記録する。
func (s *State) extentOf(rg region) visualExtent {
	ex := visualExtent{linewise: rg.linewise, lines: rg.r.End.Line - rg.r.Start.Line + 1}
	if rg.linewise {
		return ex
	}
	start, end, _, _ := s.Selection()
	ex.lines = end.Line - start.Line + 1
	if ex.lines == 1 {
		ex.eol = rg.r.End.Line > end.Line
		ex.chars = runeCount(s.line(start.Line)[start.Byte:nextChar(s.line(end.Line), end.Byte)])
	} else {
		// 複数の行では、Vim と同じく終わりの列だけを記録し、$ の後だけ行末とする（resel_VIsual_vcol が MAXCOL）。
		// 終わりの列が繰り返す先の行の長さ以上なら、regionFromExtent がその行の改行まで選ぶ（#89）。
		// 望みの列はカーソルのものなので、head がカーソルにあるとき（普段の状態）だけ head の $ の印として読む。
		// select-all の後のように head がカーソルに無ければ、終わりの位置のバイト数だけを記録する。
		ex.eol = s.want == target.WantEOL && s.visual.head == s.cursor
		ex.endByte = end.Byte
	}
	return ex
}

// regionFromExtent は、ドットリピートのために、カーソルから記録した大きさの範囲を作る。
// 行や文字が足りなければ、バッファの終わりや行末で止める。
func (s *State) regionFromExtent(ex visualExtent) region {
	start := s.cursor
	lastL := min(start.Line+ex.lines-1, s.lastLine())
	if ex.linewise {
		return region{start: start, r: buffer.Range{Start: pos(start.Line, 0), End: s.lineEnd(lastL)}, linewise: true}
	}
	var end buffer.Position
	eol := ex.eol
	if ex.lines == 1 {
		line := s.line(start.Line)
		b, _ := advanceChars(line, start.Byte, max(ex.chars-1, 0))
		end = s.clampNormal(pos(start.Line, b))
	} else {
		eol = eol || ex.endByte >= len(s.line(lastL))
		end = s.clampNormal(s.clampPos(pos(lastL, ex.endByte)))
	}
	return s.charRegion(start, end, eol || s.line(end.Line) == "")
}

// linewiseVisualOperators は、ビジュアルモードだけのオペレーターのうち、選択を行単位にして働くもの（D、C、S、Y）と、
// その働きに当たる本体のオペレーターの対応である。キーは input の表の ID である。
var linewiseVisualOperators = map[string]string{
	"delete-lines": "delete",
	"change-lines": "change",
	"yank-lines":   "yank",
}

// visualOperator は、ビジュアルモードの選択に対してオペレーターを実行し、ノーマルモードへ戻る。
// ただし > と < は、ビジュアルモードと選択の範囲を保つ（FR-77。Vim で >gv と打ったときと同じ）。
// 回数は、> と < では字下げを繰り返す回数で、それ以外のオペレーターでは使わない。
// D、C、S、Y（linewiseVisualOperators）は、選択を行単位に切り替えてから働く。
//
// y と Y は、ヤンクの後もカーソルを y を押したときの位置に残す（FR-129）。カーソルが行末の後にあれば、
// <Esc> でノーマルモードへ戻るときと同じく最後の文字へ寄せる。Vim は範囲の始まりへ移す（docs/vim-differences.md）。
// d、c などは、Vim と同じく範囲の始まりへ移す。
//
// req: FR-01, FR-02, FR-77, FR-129
func (s *State) visualOperator(cmd editcmd.Command) error {
	if base, ok := linewiseVisualOperators[cmd.Operator.ID]; ok && cmd.Operator.Builtin() {
		// D、C、S、Y は、選択を行単位に切り替えてから、もとのオペレーターとして働く（Vim の nv_visop は
		// VIsual_mode を 'V' にしてから d、c、y を実行する）。ドットリピートも、行単位の選択に対するもとのオペレーターとして繰り返す。
		s.mode = ModeVisualLine
		cmd.Operator.ID = base
	}
	keep := cmd.Operator.Builtin() && (cmd.Operator.ID == "indent" || cmd.Operator.ID == "dedent")
	mode, anchor, head, cursor, want := s.mode, s.visual.anchor, s.visual.head, s.cursor, s.want
	rg := s.visualRegion()
	ext := s.extentOf(rg)
	s.exitVisual()
	err := s.change(cmd, func() error { return s.regionOperator(cmd, rg) })
	if isYank(cmd.Operator) && err == nil {
		// FR-129: ヤンクはテキストを変えないので、y を押したときの位置はそのまま残っている。
		s.setCursorNormal(cursor)
	}
	if keep && err == nil {
		// > と < は、ずらした後もビジュアルモードと選択を保つ（FR-77）。Vim の >gv と同じく、選択の両端とカーソルは
		// ずらす前の行とバイトの位置に戻し、行が短くなっていれば行末の後へ寄せる。
		s.mode = mode
		s.visual.anchor = s.clampPos(anchor)
		s.visual.head = s.clampPos(head)
		s.cursor = s.clampPos(cursor)
		// 望みの列は、Vim の >gv と同じく、ビジュアルモードの中ではずらす前の値を保つ（v>j は前の列へ移る）。
		// ビジュアルモードを抜けると、exitVisual が戻したカーソルの表示の列から計算し直す（v><Esc>j は字下げの後の列へ移る）。
		s.want = want
		s.parser.SetVisual(true)
	}
	// 範囲の大きさは、. が繰り返す値（s.dot）と組で使うので、. の対象になる値のときだけ記録する。
	// ヤンクや失敗したオペレーターで上書きすると、直前の変更の . が別の大きさの範囲に働く（Vim の vld0vllly0. は 2 文字を消す。#167）。
	// c のようにインサートモードへ移る値は、s.dot をインサートモードを抜けるときに記録するが、範囲の大きさはここで記録しておく。
	if err == nil && repeatable(cmd) {
		s.dotVisual = ext
	}
	return err
}

// regionOperator は、ビジュアルモードの選択（またはその繰り返し）の範囲に対してオペレーターを実行する。
func (s *State) regionOperator(cmd editcmd.Command, rg region) error {
	sp := target.Span{Start: rg.r.Start, End: rg.r.End, Kind: target.Exclusive}
	if rg.linewise {
		sp.Kind = target.Linewise
	}
	s.cursor = rg.start
	return s.runOperator(cmd, sp, rg, opOptions{visual: true, times: max(cmd.Count, 1)})
}
