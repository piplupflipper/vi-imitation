package command

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/target"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// insertState は、インサートモードの 1 回分の入力の状態である。
type insertState struct {
	// cmd は、インサートモードへ移った編集コマンドの値である。抜けるときに Inserted を埋めてドットリピートに記録する。
	cmd editcmd.Command
	// text は、入力した文字と削除の操作の列である。<CR> は文字 '\n' として記録する。
	// 削除（<BS> などの insert.delete-*）は文字と別の種類（editcmd.InsertInput.Op）で記録するので、
	// 入力した制御文字（キーの列の展開から届く '\b'、'\x0b'、'\x7f' など）は文字のまま入る（#257 のレビュー）。
	text []editcmd.InsertInput
	// repeat は、入力したテキストを入れる回数である（3ix<Esc> なら 3）。c では 1 である。
	repeat int
	// extraGroups は、抜けるときに追加で閉じるアンドゥのグループの数である（インサートモードで終わったマクロの再生の分）。
	extraGroups int
	// moved は、カーソルキーで入力を区切った後、まだ文字を入力していないかを表す（Vim の arrow_used）。
	// このときアンドゥのグループは閉じていて、ドットリピートの記録も済んでいる（breakInsert）。
	moved bool
	// others は、同時の入力（FR-141）で、カーソルの他に入力を入れる位置である。1 行に 1 つで、カーソルの行には置かない。
	// 空なら、カーソルの位置だけに入力する（普段のインサートモード）。入力の扱いは typeAtSites にある。
	others []buffer.Position
	// lines は、入力の位置を置いた行の数である（ビジュアルモードの I と A なら選んだ行の数、それ以外は 1）。
	// ドットリピートで同じ数の行に位置を置き直すために、記録と組で残す（recordInsertDot）。
	lines int
	// typedSpace は、このインサートでスペースを打ったかである（Vim の inserted_space。softBackspace）。
	// <Tab> と、字下げの幅の単位で消した <BS> で戻す。
	typedSpace bool
	// autoIndent は、カーソルの行が、自動インデント（FR-162）で入れた字下げのまま、<BS> などの位置の前を消す操作のほかに何も入力していないか
	// である（Vim の did_ai）。このまま入力を終えるか（finishInsert）、別の行へ移るか（afterInsertMove）、<CR> を打つ（lineBreakEdit）と、
	// 字下げの空白を消す。位置の前を消す操作のほかの入力と、カーソルキーで同じ行の中を動いたときに戻す（noteInput）。
	autoIndent bool
}

// dotLines は、インサートモードの入力の記録 cmd と、その入力の位置を置いた行の数 lines の組である（FR-141。recordInsertDot）。
// ドットリピートの対象は値（editcmd.Command）だけなので、行の数を値の外で持ち、どの値の行の数かを cmd で照らす。
// 対象がマクロの再生の置き場（lastChange）へ移って戻っても、同じ値を指すので組は崩れない。
type dotLines struct {
	cmd   *editcmd.Command
	lines int
}

// startInsert は、カーソルの位置から入力するインサートモードへ移る（startInsertAt の位置がカーソルだけのもの）。
func (s *State) startInsert(cmd editcmd.Command, repeat int) {
	s.startInsertAt(cmd, repeat, []buffer.Position{s.cursor})
}

// startInsertAt は、位置の列 sites のすべてに同時に入力するインサートモードへ移る（FR-141）。
// sites は 1 行に 1 つの位置を上の行から並べた列で、空でない。カーソルは最初の位置に置き、他の位置にはカーソルを出さない。
// 位置の列の作り方（lineSites）と、列に同時に入力する処理（typeAtSites）は分けてある。位置が 1 つなら普段のインサートモードである。
//
// アンドゥのグループは呼び出し側（change）が開いたままにしておく。
// ドットリピートの再実行では、記録したテキストを入力してすぐに抜ける。
//
// req: FR-141
func (s *State) startInsertAt(cmd editcmd.Command, repeat int, sites []buffer.Position) {
	s.beginInsert(cmd, repeat, sites, false)
}

// beginInsert は、startInsertAt の、カーソルの行が自動インデント（FR-162）で入れた字下げのままか（autoIndent）を指定する形である。
// o、O と、行単位の c（cc、S）が、字下げを入れてから呼ぶ。
//
// 望みの列は、入力を始める位置（カーソル）の列にする。ノーマルモードの望みの列（j で短い行へ移る前の列、$ の後の行末）は引き継がない。
// A の後も、行末の位置の列であり、target.WantEOL（<End> の後）ではない。
// Vim 9.1 の edit() は、カーソルキーを使っていない間（arrow_used が偽）は毎回 w_set_curswant を立て、次の <Up> と <Down> の前に
// カーソルの列から curswant を数え直す。Neovim 0.12.5 では、"abc" と "abcdef" の 1 行目の A<Down> は 2 行目の 4 列目へ移る（#347）。
func (s *State) beginInsert(cmd editcmd.Command, repeat int, sites []buffer.Position, autoIndent bool) {
	s.mode = ModeInsert
	s.cursor = sites[0]
	s.updateWant()
	// . の中では、バッファの最後の行で位置が足りなくても、記録した行の数を保つ（Vim 9.1 の矩形選択の I の後の . と同じく、
	// 次の . は記録した数の行に置く）。
	lines := max(len(sites), s.repeatLines)
	s.insert = insertState{cmd: cmd, repeat: repeat, others: slices.Clone(sites[1:]), lines: lines, autoIndent: autoIndent}
	if s.replaying {
		// 記録は、打ったときに記録したものをそのまま残す。再実行の位置で何もしない削除の操作があっても落とさない（insertInput と違う）。
		// 入れ方は回数による繰り返しと同じで、続けて並んだ文字をまとめる（#289）。
		s.insert.text = slices.Clone(cmd.Inserted)
		if err := s.typeRepeated(cmd.Inserted, 1, false); err != nil {
			s.fail(err)
		}
		s.finishInsert()
	}
}

// insertKey は、インサートモードのキーを 1 個処理する。キーは次の 2 つの経路に分ける（#257 の決定）。
//
//   - 1 文字の入力の経路: 修飾キーの付かない文字、<CR>（editcmd.InsertLineBreak。自動インデント。FR-162）、
//     <Tab>（editcmd.InsertTab。字下げの設定。FR-160）、<Esc>。下の switch の各 case で直接処理し、操作の表を引かない。
//     1 文字の入力の速さ（要件定義 8.1 の計測の対象）に、表を引く処理を足さないためである
//   - 操作の経路: それ以外のキー（<BS>、<Del>、カーソルキー、修飾キーの付いたキーなど）。switch を抜けた後で、操作の表の
//     インサートモードの登録（KeyParser.InsertAction）を引き、割り当てた操作を insertAction で実行する（FR-127）。
//     割り当ての無いキーは何もしない
//
// 2 つの経路の分け方は、internal/input の InsertTableKey（インサートモードの登録の左辺の検査）と同じにする。
//
// req: FR-01, FR-127
func (s *State) insertKey(k editcmd.Key) {
	if k.Mod == 0 {
		switch k.Code {
		case editcmd.CodeEsc:
			s.finishInsert()
			return
		case editcmd.CodeEnter:
			s.insertInput(editcmd.InsertInput{Op: editcmd.InsertLineBreak})
			return
		case editcmd.CodeTab:
			s.insertInput(editcmd.InsertInput{Op: editcmd.InsertTab})
			return
		case editcmd.CodeRune:
			s.insertRune(k.Rune)
			return
		case editcmd.CodeBackspace, editcmd.CodeDelete, editcmd.CodeUp, editcmd.CodeDown, editcmd.CodeLeft, editcmd.CodeRight,
			editcmd.CodeHome, editcmd.CodeEnd, editcmd.CodePageUp, editcmd.CodePageDown, editcmd.CodeInsert, editcmd.CodeF1,
			editcmd.CodeLeftMouse, editcmd.CodeLeftDrag, editcmd.CodeLeftRelease, editcmd.CodeMiddleMouse, editcmd.CodeMiddleDrag,
			editcmd.CodeMiddleRelease, editcmd.CodeRightMouse, editcmd.CodeRightDrag, editcmd.CodeRightRelease,
			editcmd.CodeScrollWheelUp, editcmd.CodeScrollWheelDown, editcmd.CodeScrollWheelLeft, editcmd.CodeScrollWheelRight:
			// 操作の経路へ進む。マウスの出来事（FR-184）も、ほかの特殊キーと同じく操作の表を引く。
		}
	}
	ref, ok := s.parser.InsertAction(k)
	if ok && isKeymapCommand(ref) {
		// 設定の keymap に書いた Ex コマンド（FR-154）。インサートモードのまま、入力を区切らずに実行する。
		s.keymapCommand(ref.ID)
		return
	}
	if !ok || !ref.Builtin() {
		// 拡張機能の操作は、インサートモードには登録されない（internal/editor の buildKeymap は既定のモードで登録する）。
		return
	}
	if f, ok := insertActions[ref.ID]; ok {
		f(s)
	}
}

// insertActions は、インサートモードの操作の ID（internal/input の builtinInsert の表）から実装への対応である（FR-127）。
// 移動は insertMove、削除は insertInput に削除の種類（editcmd.InsertDeletePrev など）を渡す。
// 削除は文字と同じ入力の記録（insertState.text）に、文字と別の種類として入れるので、アンドゥの単位、ドットリピート、
// 回数による繰り返し（3i）、カーソルキーで区切った後の入力の始め直し（resumeInsert）は、5 つの削除で同じ扱いになる。
//
// req: FR-127, FR-128
var insertActions = map[string]func(*State){
	"insert.left":               func(s *State) { s.insertMove(editcmd.CodeLeft) },
	"insert.right":              func(s *State) { s.insertMove(editcmd.CodeRight) },
	"insert.up":                 func(s *State) { s.insertMove(editcmd.CodeUp) },
	"insert.down":               func(s *State) { s.insertMove(editcmd.CodeDown) },
	"insert.line-start":         func(s *State) { s.insertMove(editcmd.CodeHome) },
	"insert.line-end":           func(s *State) { s.insertMove(editcmd.CodeEnd) },
	"insert.page-up":            func(s *State) { s.insertMove(editcmd.CodePageUp) },
	"insert.page-down":          func(s *State) { s.insertMove(editcmd.CodePageDown) },
	"insert.delete-prev-char":   func(s *State) { s.insertInput(editcmd.InsertInput{Op: editcmd.InsertDeletePrev}) },
	"insert.delete-char":        func(s *State) { s.insertInput(editcmd.InsertInput{Op: editcmd.InsertDeleteChar}) },
	"insert.delete-to-line-end": func(s *State) { s.insertInput(editcmd.InsertInput{Op: editcmd.InsertDeleteToLineEnd}) },
	"insert.delete-prev-word":   func(s *State) { s.insertInput(editcmd.InsertInput{Op: editcmd.InsertDeletePrevWord}) },
	"insert.delete-to-line-start": func(s *State) {
		s.insertInput(editcmd.InsertInput{Op: editcmd.InsertDeleteToLineStart})
	},
}

// InsertActionIDs は、実装のあるインサートモードの操作の ID を返す（順は決まらない）。
// internal/editor のテストが、internal/input の操作の表と照合する（同じ層のパッケージどうしはテストからも import しないため）。
func InsertActionIDs() []string {
	ids := make([]string, 0, len(insertActions))
	for id := range insertActions {
		ids = append(ids, id)
	}
	return ids
}

// insertMove は、インサートモードのカーソルキーでカーソルを動かす（Vim 9.1 の ins_left() などに当たる）。
// c は既定のキー（<Left> など）で、操作の ID（insert.left など）から insertActions が渡す。設定で別のキーに割り当てても同じく動く。
//
//   - <Left> と <Right> は 1 文字動く。行の先頭と行末の位置を越えない（'whichwrap' の既定）
//   - <Up> と <Down> は、望みの列を保って 1 行動く。行の幅が望みの列に届かなければ行末の位置へ移る。
//     望みの列は、インサートモードに入った位置か、最後に入力した後の位置の列である（beginInsert と typeInput）
//   - <Home> は行の先頭へ、<End> は行末の位置へ移る。<End> の後の <Up> と <Down> も行末の位置へ移る
//   - <PageUp> と <PageDown> は、ノーマルモードと同じく 1 ページスクロールする（scrollPage）
//
// 動けたときは、入力をそこで区切る（breakInsert。<Home> と <End> とページ単位のスクロールは afterInsertMove の規則による）。
// 動けなかったときは何もしない。
// Vim はビープを鳴らすが、ノーマルモードのモーションの失敗と違って先行入力を捨てないので、失敗として扱わない（マクロの再生は止まらない）。
//
// req: FR-01, FR-07
func (s *State) insertMove(c editcmd.Code) {
	p := s.cursor
	line := s.line(p.Line)
	want := -1 // -1 なら、動いた後の位置から望みの列を数え直す
	switch c {
	case editcmd.CodeLeft:
		if p.Byte == 0 {
			return
		}
		p.Byte = prevChar(line, p.Byte)
	case editcmd.CodeRight:
		if p.Byte >= len(line) {
			return
		}
		p.Byte = nextChar(line, p.Byte)
	case editcmd.CodeUp, editcmd.CodeDown:
		l := p.Line - 1
		if c == editcmd.CodeDown {
			l = p.Line + 1
		}
		if l < 0 || l > s.lastLine() {
			return
		}
		want = s.want
		p = pos(l, s.insertByteAt(l, want))
	case editcmd.CodeHome:
		p.Byte = 0
	case editcmd.CodeEnd:
		p.Byte = len(line)
		want = target.WantEOL
	case editcmd.CodePageUp, editcmd.CodePageDown:
		if err := s.scrollPage(c == editcmd.CodePageDown, 1); err != nil {
			return
		}
		s.afterInsertMove(p) // p は動く前のカーソルのまま
		return
	case editcmd.CodeRune, editcmd.CodeEsc, editcmd.CodeEnter, editcmd.CodeTab, editcmd.CodeBackspace, editcmd.CodeDelete,
		editcmd.CodeInsert, editcmd.CodeF1,
		editcmd.CodeLeftMouse, editcmd.CodeLeftDrag, editcmd.CodeLeftRelease, editcmd.CodeMiddleMouse, editcmd.CodeMiddleDrag,
		editcmd.CodeMiddleRelease, editcmd.CodeRightMouse, editcmd.CodeRightDrag, editcmd.CodeRightRelease,
		editcmd.CodeScrollWheelUp, editcmd.CodeScrollWheelDown, editcmd.CodeScrollWheelLeft, editcmd.CodeScrollWheelRight:
		return
	}
	old := s.cursor
	s.cursor = p
	if want >= 0 {
		s.want = want
	} else {
		s.updateWant()
	}
	s.afterInsertMove(old)
}

// afterInsertMove は、インサートモードのカーソルキーで、移動の前のカーソルが old だったときの後始末をする。
// カーソルが動いたなら、入力を区切る（breakInsert。同時の入力も終える）。
// 動かなかったときは、同時の入力（FR-141）の間なら何もせず、同時の入力を続ける。終えるかはキーではなく、カーソルが動いたかで決める。
// 区切ると、続く入力を i の 1 か所の入力として記録し直すので、. でほかの行に入れ直せなくなるためである。
// 同時の入力でなければ、Vim 9.1 と同じく動かなくても区切る（行末の <End> も start_arrow() を呼ぶ）。
//
// 自動インデントで入れた字下げのままの行（insertState.autoIndent）から別の行へ移ったなら、その行の末尾の空白を消す。
// 同じ行の中で動いたなら、消さずに字下げを残す（Neovim 0.12.5 の o<Up><Esc> は字下げを消し、o<Left><Esc> は残す。FR-162）。
//
// req: FR-141, FR-162
func (s *State) afterInsertMove(old buffer.Position) {
	if s.cursor == old && len(s.insert.others) > 0 {
		return
	}
	if s.insert.autoIndent {
		s.insert.autoIndent = false
		if s.cursor.Line != old.Line {
			if err := s.dropAutoIndent(old); err != nil {
				s.fail(err)
			}
		}
	}
	s.breakInsert()
}

// insertByteAt は、インサートモードで、行 l の望みの列 want に当たる位置を返す。
// 行の幅が want に届かなければ、行末の位置を返す（インサートモードのカーソルは行末の位置に置ける）。
func (s *State) insertByteAt(l, want int) int {
	line := s.line(l)
	cols := s.columns()
	if line == "" || want > cols.Column(l, line, lastCharStart(line)) {
		return len(line)
	}
	return cols.ByteAt(l, line, want)
}

// breakInsert は、カーソルキーで動いたときに、インサートモードの入力をそこで区切る（Vim の start_arrow()）。
// それまでの入力をアンドゥの 1 単位として閉じ、ドットリピートとレジスター . に記録する。回数による繰り返し（3ix）はしない。
// 続けて文字を入力すると、resumeInsert が i として新しい入力を始める。このため、ifoo<Left>bar<Esc> の後の . は bar を i で入れ、
// u は bar だけを戻す（Vim 9.1 と同じ）。
// 既に区切った後（moved）なら何もしない。
//
// 同時の入力（FR-141）は、ここで終える（endSites）。カーソルが動くと、他の位置との対応が無くなるからである。
// それまでに入れたテキストは、どの位置にも残る。記録は、それまでの同時の入力として、選んだ行の数と組で残す。
//
// req: FR-06, FR-07, FR-141
func (s *State) breakInsert() {
	s.endSites()
	if s.insert.moved {
		return
	}
	s.insert.moved = true
	s.buf.EndUndoGroup()
	cmd := s.insert.cmd
	cmd.Inserted = slices.Clone(s.insert.text)
	s.recordInsertDot(cmd, s.insert.lines)
}

// recordInsertDot は、インサートモードの入力の記録 cmd をドットリピートの対象にし、入力の位置を置いた行の数 lines を組で残す（FR-141）。
// . は、cmd がまだ対象のときだけ lines を使う（repeat）。
func (s *State) recordInsertDot(cmd editcmd.Command, lines int) {
	s.dot = &cmd
	s.dotLines = dotLines{cmd: s.dot, lines: lines}
}

// endSites は、同時の入力（FR-141）を終え、その後の入力をカーソルの位置だけに入れる。同時の入力でなければ何もしない。
func (s *State) endSites() {
	s.insert.others = nil
}

// resumeInsert は、カーソルキーで区切った後（moved）の最初の入力の前に、新しい入力を i として始める。
// アンドゥのグループを開き直し、回数は 1 にする（Vim は区切った後の入力を "1i" として記録し直す）。
func (s *State) resumeInsert() {
	if !s.insert.moved {
		return
	}
	s.buf.BeginUndoGroup()
	s.insert.moved = false
	s.insert.text = nil
	s.insert.repeat = 1
	s.insert.lines = 1
	s.insert.typedSpace = false
	s.insert.cmd = editcmd.Command{Kind: editcmd.KindAction, Action: editcmd.Ref{ID: "insert"}, Count: 1, Mode: editcmd.ModeInsert}
}

// insertRune は、入力した文字を記録して、バッファに入れる。制御文字も文字として入れる。
func (s *State) insertRune(r rune) {
	s.insertInput(editcmd.InsertInput{Op: editcmd.InsertRune, Rune: r})
}

// insertInput は、入力（文字か削除の操作）を記録して、バッファに行う。
//
// 位置の前を消す操作（insert.delete-prev-char、insert.delete-prev-word、insert.delete-to-line-start）を、
// カーソルがバッファの先頭にあるときに打ち、バッファもカーソルも変わらない（どの位置でも編集が無い。inputApplies）なら、
// 記録せず、カーソルキーで区切った後の入力も始め直さない（#289）。
// 記録すると、回数による繰り返しとドットリピートで、その操作が前の回に入れた文字を消す（100ia<BS><BS>b<Esc> が babc になる）。
// Vim 9.1 も、バッファの先頭と空のバッファの <BS>、<C-w>、<C-u> は、鳴らすだけで記録に入れず、区切った入力も始め直さない
// （'backspace' が "indent,eol,start" のときの src/edit.c の ins_bs）。
//
// バッファの先頭のほかでは、何もしなくても記録する。行の先頭の insert.delete-to-line-start は、前の行とつながない（#260 の決め）ので
// 何もしないが、Vim 9.1 の <C-u> は前の行とつなぐ（記録に入る）。記録から落とすと、繰り返しと . の結果が Vim からさらに離れる
// （abc、|def で 3i<C-u>x<Esc> は Vim が abcxdef、記録すると abc、xdef、落とすと abc、xxxdef。PR #314 のレビュー）。
// 位置の後ろを消す操作（insert.delete-char、insert.delete-to-line-end）も、何もしなくても記録する（Vim 9.1 の ins_del と同じ）。
// 空のバッファの i<Del>x<Esc> の記録の <Del> は、後の . で位置の文字を消す。
//
// req: FR-06, FR-127
func (s *State) insertInput(in editcmd.InsertInput) {
	if isDeleteBefore(in.Op) && s.cursor == (buffer.Position{}) && !s.inputApplies(in) {
		return
	}
	s.resumeInsert()
	s.insert.text = append(s.insert.text, in)
	if err := s.typeInput(in); err != nil {
		s.fail(err)
	}
}

// isDeleteBefore は、op が位置の前を消す削除の操作かを返す（insertInput）。
func isDeleteBefore(op editcmd.InsertOp) bool {
	return op == editcmd.InsertDeletePrev || op == editcmd.InsertDeletePrevWord || op == editcmd.InsertDeleteToLineStart
}

// inputApplies は、入力の記録の 1 件 in を typeInput で行ったときに、編集を行うかを返す（insertInput）。
// 同時の入力（FR-141）の間は、どれかの位置で編集を行うかである。
// ただし、ある位置の編集が行の中に収まらなければ、typeAtSites は同時の入力を終えてカーソルの位置だけに行うので、カーソルの位置で決める。
// バッファの先頭の行から選んだ行の I（字下げの無い行）で打った <BS> は、カーソルの位置（バッファの先頭）で何もしないので、
// ほかの位置の行を前の行へつながず、同時の入力も終えずに捨てる（Vim 9.1 の矩形選択の I も、入力を続ける）。
func (s *State) inputApplies(in editcmd.InsertInput) bool {
	_, _, atCursor := s.inputEdit(in, s.cursor)
	applies := atCursor
	for _, p := range s.insert.others {
		ed, _, ok := s.inputEdit(in, p)
		if !ok {
			continue
		}
		if !withinLine(ed, p.Line) {
			return atCursor
		}
		applies = true
	}
	return applies
}

// typeInput は、入力の記録の 1 件をカーソルの位置に行う。同時の入力（FR-141）の間は、すべての位置に行う（typeAtSites）。
// 各位置で行う編集は inputEdit が決める。
func (s *State) typeInput(in editcmd.InsertInput) error {
	if len(s.insert.others) > 0 {
		if done, err := s.typeAtSites(in); done {
			return err
		}
	}
	from := s.cursor
	soft := in.Op == editcmd.InsertDeletePrev && s.softBackspace(from)
	ed, after, ok := s.inputEdit(in, from)
	if !ok {
		s.noteInput(in, from, soft)
		return nil
	}
	if err := s.apply(ed); err != nil {
		return err
	}
	s.cursor = after
	s.updateWant()
	s.noteInput(in, from, soft)
	return nil
}

// noteInput は、入力の記録の 1 件 in をカーソルの位置 from で行った後に、字下げの状態（insertState の typedSpace と autoIndent）を進める
// （FR-160、FR-162）。soft は、in が字下げの幅の単位で消した <BS>（softBackspace）だったかである。
//
//   - スペースの文字を入れたら typedSpace を立て、<Tab> と soft の <BS> で戻す（Vim の inserted_space）
//   - 自動インデントを行う <CR> の後は autoIndent を立てる（Vim の did_ai）
//   - 位置の前を消す操作（<BS>、insert.delete-prev-word、insert.delete-to-line-start）は、autoIndent を残す。
//     ただし、行の先頭で打った（前の行とつないだ）か、消した後のカーソルが行の 2 バイト目より前なら戻す。
//     Neovim 0.12.5 の ins_bs の末尾の「if (curwin->w_cursor.col <= 1) did_ai = false」と同じで、`noet sw=8 ts=8` の
//     タブ文字 2 つの字下げで o<BS><Esc> を打つと、タブ文字 1 つが残る（Vim 9.1 も同じ）
//   - ほかの入力は autoIndent を戻す
func (s *State) noteInput(in editcmd.InsertInput, from buffer.Position, soft bool) {
	switch {
	case in.Op == editcmd.InsertRune && in.Rune == ' ':
		s.insert.typedSpace = true
	case in.Op == editcmd.InsertTab || soft:
		s.insert.typedSpace = false
	}
	switch {
	case in.Op == editcmd.InsertLineBreak:
		s.insert.autoIndent = s.indentConfig().auto
	case isDeleteBefore(in.Op):
		s.insert.autoIndent = s.insert.autoIndent && from.Byte > 0 && s.cursor.Byte > 1
	default:
		s.insert.autoIndent = false
	}
}

// typeAtSites は、同時の入力（FR-141）で、入力の記録の 1 件 in を、カーソルと insert.others のすべての位置に行う。
//
// 各位置の編集は、カーソルの位置だけで行うときと同じもの（inputEdit）である。
// どの位置の編集も、その位置の行の中に収まる（範囲が行をまたがず、入れるテキストに改行が無い）なら、
// 編集は「位置の前の何バイトと後の何バイトを消し、何を入れる」と位置からの相対で書けるので、すべての位置で同時に行う。
// 文字の入力、行の中の <BS>、insert.delete-prev-word、insert.delete-to-line-start などがこれに当たる。
// 1 つの位置でも行の中に収まらなければ（改行の入力、行の先頭の <BS>、行末の <Del> など）、同時の入力を終えて false を返す。
// 呼び出し側は、その入力をカーソルの位置だけに行う。それまでに入れたテキストは、どの位置にも残る。
// 入力の種類や操作の ID では分けず、編集の形だけで決める。何もしない位置（バッファの先頭の <BS> など）は、位置を変えない。
//
// 編集は 1 つのトランザクションにまとめて適用する。どれも別の行の中の編集なので、ほかの位置の行とバイトはずれない。
//
// req: FR-141
func (s *State) typeAtSites(in editcmd.InsertInput) (bool, error) {
	from := s.cursor
	soft := in.Op == editcmd.InsertDeletePrev && s.softBackspace(from)
	sites := append([]buffer.Position{s.cursor}, s.insert.others...)
	edits := make([]buffer.Edit, 0, len(sites))
	for i, p := range sites {
		ed, after, ok := s.inputEdit(in, p)
		if !ok {
			continue
		}
		if !withinLine(ed, p.Line) {
			s.endSites()
			return false, nil
		}
		edits = append(edits, ed)
		sites[i] = after
	}
	if err := s.apply(edits...); err != nil {
		return true, err
	}
	s.cursor = sites[0]
	copy(s.insert.others, sites[1:])
	s.updateWant()
	s.noteInput(in, from, soft)
	return true, nil
}

// withinLine は、編集 ed が行 l の中に収まる（範囲の両端が行 l にあり、入れるテキストに改行が無い）かを返す（typeAtSites）。
func withinLine(ed buffer.Edit, l int) bool {
	return ed.Range.Start.Line == l && ed.Range.End.Line == l && !strings.Contains(ed.Text, "\n")
}

// lineSites は、行 first から last までの各行に、place が返すバイトの位置で入力の位置を 1 つずつ置いた列を、上の行から順に返す（FR-141）。
// place は行のテキストを受け取る。ビジュアルモードの I と A は、ノーマルモードの I と A と同じ関数を渡す（linePlacements）。
// 位置の列を作る処理はこれだけで、列に同時に入力する処理（typeAtSites）は位置の置き方を知らない。
//
// req: FR-141
func (s *State) lineSites(first, last int, place func(line string) int) []buffer.Position {
	sites := make([]buffer.Position, 0, last-first+1)
	for l := first; l <= last; l++ {
		sites = append(sites, pos(l, place(s.line(l))))
	}
	return sites
}

// shiftSites は、外からの編集の後に、同時の入力の位置（insert.others）をそれぞれ shift でずらす（FR-141。ShiftPositions）。
// カーソルは先にずらしておく。次の位置は落とす。
//
//   - 編集が置き換えた範囲の内側にあった位置（inside）。行を消す編集や行を丸ごと書き換える編集は、範囲の中の位置を範囲の始まりへ寄せる。
//     寄せた位置はもとの文字との対応が無く、カーソルと違って画面に出ないので、残すと利用者の気づかない場所（行の先頭や前の行の行末）に入る
//   - バッファの外へ出た位置
//   - カーソルか、下の行にあった位置と同じ行へ寄った位置。1 行に 1 つの位置を保つ（typeAtSites の前提）
//
// 同じ行へ寄ったときに下の行にあった位置を残すのは、範囲の始まりにちょうどある位置が内側に数えられないからである。
// 列 0 の位置（字下げの無い行の I）の行を消す編集（その行の先頭から次の行の先頭まで）では、消した行の位置は範囲の始まりにあって残り、
// 次の行の位置が同じ行へ寄る。編集の後も残るのは次の行のほうなので、下の行にあった位置を残す（PR #274 の再レビュー）。
//
// req: FR-141
func (s *State) shiftSites(shift func(buffer.Position) (buffer.Position, bool)) {
	if len(s.insert.others) == 0 {
		return
	}
	used := map[int]bool{s.cursor.Line: true}
	kept := make([]buffer.Position, 0, len(s.insert.others))
	for _, p := range slices.Backward(s.insert.others) {
		q, inside := shift(p)
		if inside || q != s.clampPos(q) || used[q.Line] {
			continue
		}
		used[q.Line] = true
		kept = append(kept, q)
	}
	slices.Reverse(kept)
	s.insert.others = kept
}

// inputEdit は、入力の記録の 1 件 in を位置 c に行う編集と、行った後の位置を返す。何もしない入力なら ok が false である。
//
//   - 文字（editcmd.InsertRune）は入れる。'\n' は行を分ける。他の制御文字は文字のまま入れる。字下げの設定と自動インデントは当てない
//   - editcmd.InsertTab は、字下げの設定に従ってタブ文字かスペースを入れる（tabEdit。FR-160）
//   - editcmd.InsertLineBreak は行を分け、設定 indent.auto が true なら前の行の字下げを写す（lineBreakEdit。FR-162）
//   - editcmd.InsertDeletePrev は位置の前の文字を消す。行の先頭では前の行とつなぎ、バッファの先頭では何もしない
//     （'backspace' が "indent,eol,start" の動き）。位置の前が字下げのとき（と、indent.useSpaces が true で前の文字が空白のとき）は、
//     字下げの幅の単位で消す（softBackspace。FR-160）
//   - editcmd.InsertDeleteChar は位置の文字を消し、editcmd.InsertDeleteToLineEnd は位置から行末までを消す。
//     どちらも位置は動かない。行末の位置では、どちらも次の行をつなげる（次の行の先頭の空白は残す）。
//     最後の行の行末と空のバッファでは何もしない。InsertDeleteChar は Vim 9.1 のインサートモードの <Del>
//     （'backspace' が "indent,eol,start"）と同じ動きである。InsertDeleteToLineEnd は Vim に対応するキーが無く、
//     行末の位置で次の行をつなげるのは Emacs の C-k に合わせた（#257 の決定）
//   - editcmd.InsertDeletePrevWord は位置の前の単語を消す（prevWordStart）。行の先頭では InsertDeletePrev と同じく前の行とつなぐ
//   - editcmd.InsertDeleteToLineStart は位置から行の先頭（列 0）までを消す。字下げの手前で止めない。行の先頭では何もしない
//   - InsertDeletePrevWord と InsertDeleteToLineStart は、このインサートで入力した文字より前の文字も消す。
//     Vim 9.1 の <C-w> と <C-u> は、'backspace' が "start" なら入力の始まりで 1 度止まり、<C-u> は行の先頭で前の行とつなぐ
//     （#260 の決定。docs/vim-differences.md）
//
// 消したテキストは、レジスターにも履歴にも入れない。
//
// req: FR-127, FR-128, FR-160, FR-162
func (s *State) inputEdit(in editcmd.InsertInput, c buffer.Position) (ed buffer.Edit, after buffer.Position, ok bool) {
	switch in.Op {
	case editcmd.InsertRune:
		if in.Rune == '\n' {
			return buffer.Edit{Range: emptyRange(c), Text: "\n"}, pos(c.Line+1, 0), true
		}
		str := string(in.Rune)
		return buffer.Edit{Range: emptyRange(c), Text: str}, pos(c.Line, c.Byte+len(str)), true
	case editcmd.InsertDeleteChar, editcmd.InsertDeleteToLineEnd:
		line := s.line(c.Line)
		var to buffer.Position
		switch {
		case c.Byte < len(line) && in.Op == editcmd.InsertDeleteChar:
			to = pos(c.Line, nextChar(line, c.Byte))
		case c.Byte < len(line):
			to = pos(c.Line, len(line))
		case c.Line < s.lastLine():
			to = pos(c.Line+1, 0)
		default:
			return buffer.Edit{}, c, false
		}
		return buffer.Edit{Range: buffer.Range{Start: c, End: to}}, c, true
	case editcmd.InsertTab:
		ed, after := s.tabEdit(c)
		return ed, after, true
	case editcmd.InsertLineBreak:
		ed, after := s.lineBreakEdit(c)
		return ed, after, true
	case editcmd.InsertDeletePrev, editcmd.InsertDeletePrevWord, editcmd.InsertDeleteToLineStart:
		var from buffer.Position
		switch {
		case in.Op == editcmd.InsertDeletePrev && s.softBackspace(c):
			ed, after := s.softBackspaceEdit(c)
			return ed, after, true
		case c.Byte > 0 && in.Op == editcmd.InsertDeletePrev:
			from = pos(c.Line, prevChar(s.line(c.Line), c.Byte))
		case c.Byte > 0 && in.Op == editcmd.InsertDeletePrevWord:
			from = s.prevWordStart(c)
		case c.Byte > 0:
			from = pos(c.Line, 0)
		case c.Line > 0 && in.Op != editcmd.InsertDeleteToLineStart:
			from = s.lineEnd(c.Line - 1)
		default:
			return buffer.Edit{}, c, false
		}
		return buffer.Edit{Range: buffer.Range{Start: from, End: c}}, from, true
	}
	return buffer.Edit{}, c, false
}

// prevWordStart は、インサートモードの insert.delete-prev-word で消す範囲の始まりを返す。c は行の先頭より後ろにある。
// ノーマルモードの b（word-prev）と同じ単語の区切りで、カーソルの前の空白を飛ばし、その前の単語の先頭まで戻る（db の範囲）。
// 文字の分類と、結合文字を前の文字と一緒に扱うことも b と同じである。
// このため全角空白（U+3000）は空白として飛ばす。Vim 9.1 の <C-w> は全角空白を空白と見ず、全角空白だけを消す（docs/vim-differences.md）。
// b が前の行へ戻るとき（カーソルの前が字下げか空白だけのとき）は、行の先頭で止める。行をまたいでは消さない（#260 の決定）。
//
// req: FR-128
func (s *State) prevWordStart(c buffer.Position) buffer.Position {
	start := pos(c.Line, 0)
	f, ok := target.LookupMotion("word-prev")
	if !ok {
		return start
	}
	res, err := f(s.buf, target.Request{Cursor: c, Count: 1, Columns: s.Columns})
	if err != nil || res.Pos.Line != c.Line {
		return start
	}
	return res.Pos
}

// finishInsert は、インサートモードを抜ける（<Esc>）。
//
// 回数つきで入った場合（3ix、3o）は、入力したテキストを残りの回数だけ続けて入れる（typeRepeated）。o と O は、そのたびに行を開く。
// 同時の入力（FR-141）の間なら、残りの回数もすべての位置に入れる（typeInput。Vim 9.1 の矩形選択の 2I と同じ）。
// カーソルの行が自動インデントで入れた字下げのままなら、行の末尾の空白を消す（dropAutoIndent。FR-162）。
// カーソルを 1 文字左へ戻し、アンドゥのグループを閉じ、入力の記録を値の Inserted に埋めてドットリピートに記録する（FR-06）。
// 同時の入力のすべての位置の変更は、この 1 つのアンドゥのグループに入る。
// カーソルキーで区切った後、まだ文字を入力していなければ（moved）、区切ったときに閉じて記録してあるので、カーソルを戻すだけにする。
//
// req: FR-06, FR-07, FR-141, FR-162
func (s *State) finishInsert() {
	in := s.insert
	if n := in.repeat - 1; n > 0 && !in.moved {
		size := 0
		for _, x := range in.text {
			size += max(utf8.RuneLen(x.Rune), 1)
		}
		if size*n*(len(in.others)+1) > maxPutBytes {
			s.fail(failf("text too large"))
			n = 0
		}
		open := in.cmd.Action.Builtin() && (in.cmd.Action.ID == "open-below" || in.cmd.Action.ID == "open-above")
		if err := s.typeRepeated(in.text, n, open); err != nil {
			s.fail(err)
		}
	}
	if s.insert.autoIndent {
		// 自動インデントで入れた字下げのまま抜けるなら、字下げを消す（FR-162。Vim の o<Esc> は空の行を残す）。
		s.insert.autoIndent = false
		if err := s.dropAutoIndent(s.cursor); err != nil {
			s.fail(err)
		}
	}
	s.mode = ModeNormal
	if s.cursor.Byte > 0 {
		s.cursor.Byte = prevChar(s.line(s.cursor.Line), s.cursor.Byte)
	}
	s.setCursorNormal(s.cursor)
	for range in.extraGroups {
		s.buf.EndUndoGroup()
	}
	if !in.moved {
		s.buf.EndUndoGroup()
		cmd := in.cmd
		cmd.Inserted = slices.Clone(in.text)
		s.recordInsertDot(cmd, in.lines)
	}
	s.insert = insertState{}
}

// typeRepeated は、入力の記録 text を n 回続けて行う。open なら、各回の前に行末で行を開く（o と O）。
// 回数による繰り返し（finishInsert）と、ドットリピートの再実行の 1 回目（startInsertAt）が使う。
//
// 続けて並んだ文字（改行を除く）は、回の区切りをまたいでまとめ、1 つの編集で入れる（typeText）。
// まとめている途中の文字を消す insert.delete-prev-char（<BS>）は、まとめている文字の最後の 1 文字を落として済ませる（#289）。
// 位置の前の文字は、まとめている文字の最後の文字そのものだからである（prevChar は文字を UTF-8 の 1 文字ずつ戻る）。
// まとめている文字が無いときの <BS>、ほかの削除の操作、改行は、まとめた文字を入れてから、1 件ずつ typeInput で行う。
// 結果のテキストと位置は、記録を 1 件ずつ typeInput で行ったときと同じである。
//
// 1 件ずつ入れると、編集のたびに行の全体を作り直し、カーソルの表示の列を行の先頭から数え直すので、
// 長い行になる繰り返し（100000afoo xy <Esc>）は行の長さの 2 乗の時間を要し、変更の記録も文字の数だけ溜まった（#279）。
// <BS> を挟む繰り返し（100000axy<BS>z<Esc>）も、<BS> を 1 件ずつ行っていたので、同じく 2 乗の時間を要した（#289）。
// Vim 9.1 も、先に届いている文字を 100 バイト（INPUT_BUFLEN）までまとめて行に入れる（src/edit.c の insertchar）。
// ここでは記録の全体が手元にあるので、まとめる長さに上限を置かない（入れる量は finishInsert が maxPutBytes で抑える）。
//
// 途中で失敗すれば、そこで止めて誤りを返す。それまでに入れたテキストは残る。
//
// req: FR-02, FR-06, FR-141
func (s *State) typeRepeated(text []editcmd.InsertInput, n int, open bool) error {
	var run []byte
	flush := func() error {
		if len(run) == 0 {
			return nil
		}
		str := string(run)
		run = run[:0]
		return s.typeText(str)
	}
	for range n {
		if open {
			if err := flush(); err != nil {
				return err
			}
			s.cursor = s.lineEnd(s.cursor.Line)
			// o と O の回数の分の行も、<CR> と同じく自動インデントで開く（FR-162）。
			if err := s.typeInput(editcmd.InsertInput{Op: editcmd.InsertLineBreak}); err != nil {
				return err
			}
		}
		for _, x := range text {
			if x.Op == editcmd.InsertRune && x.Rune != '\n' {
				// string(rune) と同じく、正しくない値は U+FFFD にする（inputEdit）。
				run = utf8.AppendRune(run, x.Rune)
				continue
			}
			if x.Op == editcmd.InsertDeletePrev && len(run) > 0 && !isBlank(run[len(run)-1]) {
				// まとめている最後の文字が空白なら、字下げの幅の単位で消すことがある（softBackspace）ので、まとめた文字を入れてから行う。
				_, size := utf8.DecodeLastRune(run)
				run = run[:len(run)-size]
				continue
			}
			if err := flush(); err != nil {
				return err
			}
			if err := s.typeInput(x); err != nil {
				return err
			}
		}
	}
	return flush()
}

// typeText は、改行を含まないテキスト str を、カーソルの位置に入れる（typeRepeated）。
// 同時の入力（FR-141）の間は、すべての位置に入れる。str の文字を 1 つずつ typeInput で入れたときと同じ結果になる。
// どの位置の編集もその位置の行の中に収まるので、同時の入力は終えない（typeAtSites）。
//
// req: FR-141
func (s *State) typeText(str string) error {
	sites := append([]buffer.Position{s.cursor}, s.insert.others...)
	edits := make([]buffer.Edit, len(sites))
	for i, p := range sites {
		edits[i] = buffer.Edit{Range: emptyRange(p), Text: str}
		sites[i] = pos(p.Line, p.Byte+len(str))
	}
	if err := s.apply(edits...); err != nil {
		return err
	}
	s.cursor = sites[0]
	copy(s.insert.others, sites[1:])
	s.updateWant()
	// 字下げの状態は、文字を 1 つずつ入れたとき（noteInput）と同じに進める。
	if strings.Contains(str, " ") {
		s.insert.typedSpace = true
	}
	s.insert.autoIndent = false
	return nil
}
