package command

import (
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/option"
)

// MouseKind は、マウスの出来事の位置にあるものの種類である（Mouse.Target）。
type MouseKind int

const (
	// MouseNone は、マウスで操作するものが無い位置である（ステータス行、コマンドラインの行、ポップアップの枠線など）。
	MouseNone MouseKind = iota
	// MouseText は、この State のバッファのテキストの位置である。
	MouseText
	// MouseElsewhere は、この State のバッファの外の、呼び出し側が扱う部品の位置である（タブの行のタブの見出し、
	// フォーカスのあるポップアップの外のバッファなど）。ボタンを押したことは Mouse.Activate で呼び出し側に行わせる。
	MouseElsewhere
	// MouseWindow は、この State の表示領域の窓の、テキストでない部分（ステータス行、コマンドラインの行）である。
	// ボタンを押しても何もせず、ホイールはこの表示領域を送る（Neovim 0.12.5 と同じ。FR-182）。
	MouseWindow
	// MouseScrollbar は、この State の表示領域の右端のスクロールバー（FR-186）の、つまみか印を描いたセルである（FR-189）。
	// 左のボタンを押すと、つまみならドラッグを始め、印ならその印の行へ移る（FR-210、FR-211）。ホイールはこの表示領域を送る。
	// ボタンをスクロールバーで押した後のドラッグ（Target の drag が true）は、列に依らずスクロールバーの行として解く。
	MouseScrollbar
)

// scrollable は、t の位置のホイールでこの State の表示領域を送るかを返す。
func (t MouseTarget) scrollable() bool {
	return t.Kind == MouseText || t.Kind == MouseWindow || t.Kind == MouseScrollbar
}

// MouseTarget は、マウスの出来事の位置を、この State のバッファの位置に解いたものである（Mouse.Target）。
type MouseTarget struct {
	Kind MouseKind
	// Line と Col は、Kind が MouseText のときの、バッファの行と表示の列である。列は望みの列（State の want）と同じ数え方で、
	// 行を折り返すときは見た目の行の列（target.Columns）で数える。行末より右と、印の列と行番号の列の上も、その位置の列のまま入れる。
	// 最後の行より下の位置は、最後の行にする（Neovim 0.12.5 と同じ）。
	Line, Col int
	// Over は、ドラッグの間（Target の drag が true）に、位置が表示領域の上（負）か下（正）へはみ出していた行の数である。
	Over int
	// Row は、Kind が MouseScrollbar のときの、位置の表示領域の中の行（0 から数える）である。ドラッグの間は、表示領域の外へ
	// はみ出した行もそのまま数える。ThumbRow は、直前の描画が描いたつまみの最初の行（表示領域の中の行）である。
	Row, ThumbRow int
	// Thumb は、Kind が MouseScrollbar のときに、位置のセルがつまみの上かである。
	Thumb bool
	// Marks は、Kind が MouseScrollbar のときの、位置のセルに描いた印の種類の行（そのセルに割り付けた行の範囲に入るもの）を、
	// 小さい順に並べたものである。印が無ければ空である。描いた印と同じ選び方の種類の行で、書き換えない。
	Marks []int
}

// Mouse は、マウスの出来事の位置を解く口である（FR-181〜FR-185）。
// 画面の部品の配置は描画の側が持つので、呼び出し側（internal/editor）が State.Mouse に差し込む。
// どの出来事も、いま処理しているマウスのキーの位置を解く。位置はキーの値に含めず、キーと一緒に届く（editcmd の CodeLeftMouse の doc）。
// マクロの再生で記録したマウスのキーを実行するときは、最後に届いたマウスの出来事の位置を解く（Vim と同じ）。
type Mouse interface {
	// Target は、いま処理しているマウスの出来事の位置を解く。drag なら、ボタンを押した部品の中へ寄せて解く（ドラッグの間。Over を入れる）。
	Target(drag bool) MouseTarget
	// Activate は、Target が MouseElsewhere を返した位置でボタンを押したことを、呼び出し側に行わせる
	// （タブの見出しならそのタブへ移り、フォーカスのあるポップアップの外のテキストなら、元のタブへ戻ってその位置へ移る）。
	// 別のバッファへ移したら ErrBufferSwitched を返す。
	Activate() error
	// ScrollbarTop は、直前の描画が描いたスクロールバーのつまみの最初の行を、表示領域の row 行目へ動かすときの、表示領域の中へ
	// 寄せた行 at と、表示領域の最初の行 line を返す（つまみのドラッグ。FR-210）。スクロールバーを描いていなければ ok は false である。
	ScrollbarTop(row int) (at, line int, ok bool)
	// HoldScrollbarThumb は、表示領域の最初の行が line の間、次からの描画がつまみの最初の行を row 行目に描くようにする
	// （ドラッグの間に、つまみをマウスの行に置く。FR-210）。
	HoldScrollbarThumb(row, line int)
}

// HorizontalViewport は、行を折り返さないときに横の位置も動かせる表示領域である（ホイールの横のスクロール。FR-182）。
// State.Viewport がこれを満たさなければ、横のスクロールは何もしない。
type HorizontalViewport interface {
	Viewport
	// Left は、表示領域の左の端に当たる表示の列を返す。
	Left() int
	// Columns は、本文の列数を返す。
	Columns() int
	// SetLeft は、表示領域の左の端の列を left にする。次の描画は、カーソルが余白を残して入っていれば動かさない。
	SetLeft(left int)
}

// mouseActions は、マウスの出来事の操作の ID から、実装への対応である（FR-181〜FR-183）。
// ノーマルモード、ビジュアルモード、インサートモードで同じ ID を使う（internal/input の表で、3 つのモードに同じ行を置く）。
// どれもテキストを変えないので、編集を禁止したバッファでも使え（keepsTextActions）、ドットリピートに記録しない。
// オペレーターの後のマウスの出来事は、表に無いので誤りになる（Neovim の d<LeftMouse> はマウスの位置までを対象にするが、本体は写していない）。
//
// req: FR-181, FR-182, FR-183
var mouseActions = map[string]func(s *State) error{
	"mouse.click":        (*State).mouseClick,
	"mouse.drag":         (*State).mouseDrag,
	"mouse.release":      (*State).mouseRelease,
	"mouse.select-word":  func(s *State) error { return s.mouseSelect(false) },
	"mouse.select-line":  func(s *State) error { return s.mouseSelect(true) },
	"mouse.scroll-down":  func(s *State) error { return s.mouseScroll(true) },
	"mouse.scroll-up":    func(s *State) error { return s.mouseScroll(false) },
	"mouse.scroll-right": func(s *State) error { return s.mouseScrollHorizontal(true) },
	"mouse.scroll-left":  func(s *State) error { return s.mouseScrollHorizontal(false) },
}

// actMouse は、ノーマルモードとビジュアルモードのマウスの出来事の操作である。
func actMouse(id string) actionFunc {
	return func(s *State, _ editcmd.Command) error { return mouseActions[id](s) }
}

// insertMouse は、インサートモードのマウスの出来事の操作である。失敗を結果に入れる（インサートモードの操作は誤りを返さないため）。
func insertMouse(id string) func(*State) {
	return func(s *State) {
		if err := mouseActions[id](s); err != nil {
			s.fail(err)
		}
	}
}

// mouseClick は、<LeftMouse>（mouse.click）である。マウスの位置へカーソルを移す（FR-181）。Neovim 0.12.5 の mouse=a に合わせた。
//
//   - 行末より右の位置は、ノーマルモードとビジュアルモードでは最後の文字、インサートモードでは行末の位置にする
//   - 印の列と行番号の列の上は、その行の本文の左の端の列にする。最後の行より下の "~" の行は、最後の行のその列にする
//   - 望みの列は、押した位置の列にする（行末より右を押した後の j は、押した列へ移る）
//   - ビジュアルモードでは、選択を終えてノーマルモードへ戻ってから移る。インサートモードでは、インサートモードのまま移り、入力を区切る
//   - タブの行のタブの見出しは、そのタブへ移る（Mouse.Activate）
//   - 右端のスクロールバーのつまみは、つまみのドラッグを始め、印はその印の行へ移る（mouseBarPress。FR-210、FR-211）
//
// 押した位置は、続くドラッグ（mouse.drag）の選択の始まりとして覚える。
//
// req: FR-181
func (s *State) mouseClick() error {
	s.mouseAnchor, s.mouseDragging, s.mouseBar = nil, false, nil
	if s.Mouse == nil {
		return nil
	}
	t := s.Mouse.Target(false)
	switch t.Kind {
	case MouseNone, MouseWindow:
		return nil
	case MouseElsewhere:
		return s.Mouse.Activate()
	case MouseScrollbar:
		s.mouseBarPress(t)
		return nil
	case MouseText:
	}
	if s.mode.visual() {
		s.exitVisual()
	}
	s.mouseMove(t)
	at := s.cursor
	s.mouseAnchor = &at
	return nil
}

// MoveToMouse は、マウスのボタンを押したほかの部品から移ってきたバッファで、カーソルを t の位置（Kind は MouseText）へ移す
// （フォーカスのあるポップアップの外を押して、元のタブへ戻ったとき。internal/editor の mouseLocator.Activate）。
// ビジュアルモードなら選択を終える。押した位置は、続くドラッグの選択の始まりとして覚える。
//
// req: FR-181
func (s *State) MoveToMouse(t MouseTarget) {
	if t.Kind != MouseText {
		return
	}
	if s.mode.visual() {
		s.exitVisual()
	}
	s.mouseMove(t)
	at := s.cursor
	s.mouseAnchor = &at
}

// mousePos は、t の位置に当たるカーソルの位置を、今のモードで返す。インサートモードでは行末の位置にも置く（insertByteAt）。
func (s *State) mousePos(t MouseTarget) buffer.Position {
	l := min(max(t.Line, 0), s.lastLine())
	if s.mode == ModeInsert {
		return pos(l, s.insertByteAt(l, t.Col))
	}
	return s.clampNormal(s.clampPos(pos(l, s.columns().ByteAt(s.line(l), t.Col))))
}

// mouseMove は、カーソルを t の位置へ移し、望みの列を t の列にする。インサートモードでは入力を区切る（afterInsertMove）。
func (s *State) mouseMove(t MouseTarget) {
	old := s.cursor
	s.cursor = s.mousePos(t)
	s.want = t.Col
	if s.mode == ModeInsert {
		s.afterInsertMove(old)
		return
	}
	if s.mode.visual() {
		s.cursor = s.visualCursor()
	}
	s.followCursor()
}

// mouseDrag は、<LeftDrag>（mouse.drag）である。ボタンを押した位置からマウスの位置までを、文字単位のビジュアルモードで選ぶ（FR-183）。
//
//   - 押した位置から動いたら、ビジュアルモードに入る。インサートモードでは、インサートモードを抜けてから入る
//     （Neovim の "-- (insert) VISUAL --" は本体に無い。docs/vim-differences.md）
//   - 行末より右へ動かすと、その行の改行まで選ぶ（Neovim と同じ。望みの列が行の幅より右なので visualCursor が行末の後に置く）
//   - 表示領域の上（タブの行）か下（ステータス行、コマンドラインの行）へはみ出したら、はみ出した行の数だけ表示領域を送り、
//     表示領域の端の行へ移る（Neovim と同じ）
//   - 選び始めてから離すまでは、上下の余白（scroll.margin）を残さない（MouseDragging。Neovim の mouse_dragging と同じく、
//     表示領域の端の行へドラッグしても表示領域を送らない）
//   - ボタンを押した位置がこのバッファのテキストでなければ（タブの行など）、何もしない
//
// スクロールバーのつまみを押した後のドラッグは、選ばずに表示領域を動かす（mouseBarMove。FR-210）。
//
// req: FR-183
func (s *State) mouseDrag() error {
	if s.Mouse != nil && s.mouseBar != nil {
		s.mouseBarMove()
		return nil
	}
	if s.Mouse == nil || s.mouseAnchor == nil {
		return nil
	}
	t := s.Mouse.Target(true)
	if t.Kind != MouseText {
		return nil
	}
	if s.mode.visual() {
		s.mouseDragging = true
	}
	if t.Over != 0 {
		if line, ok := s.mouseDragScroll(t.Over); ok {
			t.Line = line
		}
	}
	if !s.mode.visual() {
		if s.mousePos(t) == *s.mouseAnchor {
			return nil // 押した文字の上から動いていない
		}
		anchor := *s.mouseAnchor
		if s.mode == ModeInsert {
			s.finishInsert()
		}
		s.mode = ModeVisual
		s.visual.anchor = s.clampNormal(s.clampPos(anchor))
		s.parser.SetVisual(true)
		s.mouseDragging = true
	}
	s.mouseMove(t)
	return nil
}

// MouseDragging は、マウスのボタンを押した後にドラッグで選び始め、まだ離していないかを返す（FR-183）。
// その間は、描画もスクロールの数え方も、上下の余白（scroll.margin）を残さない（internal/editor の marginsFor と scrollMargin）。
func (s *State) MouseDragging() bool { return s.mouseAnchor != nil && s.mouseDragging }

// lineWidth は、行 l の表示幅（行末の位置の表示の列）を返す。
func (s *State) lineWidth(l int) int {
	line := s.line(l)
	return s.columns().Column(line, len(line))
}

// mouseDragScroll は、ドラッグの位置が表示領域の上（over が負）か下（正）へはみ出した行の数だけ表示領域を送り、
// 送った後の表示領域の端（上へなら最初の行、下へなら最後まで見える最後の行）の行を返す。表示領域が無ければ ok は false である。
func (s *State) mouseDragScroll(over int) (line int, ok bool) {
	v, ok := s.scrollView()
	if !ok {
		return 0, false
	}
	down := over > 0
	v.ScrollRedraw(down, max(over, -over), s.cursor.Line, false)
	s.setView(v, false)
	if !down {
		return v.Top, true
	}
	bot, _ := v.Botline()
	return max(bot-1, v.Top), true
}

// mouseRelease は、<LeftRelease>（mouse.release）である。ドラッグを終える。選択はそのまま残す（Neovim と同じ）。
// つまみの上に描いた印を押して、つまみを動かさずに離したら、その印の行へ移る（mouseBarPress。FR-211）。
//
// req: FR-183, FR-211
func (s *State) mouseRelease() error {
	bar := s.mouseBar
	s.mouseAnchor, s.mouseDragging, s.mouseBar = nil, false, nil
	if bar != nil && !bar.moved && len(bar.marks) > 0 {
		s.mouseMarkJump(bar.marks)
	}
	return nil
}

// mouseBarDrag は、スクロールバーのつまみを押してから離すまでの状態である（State.mouseBar）。
type mouseBarDrag struct {
	// offset は、つまみの最初の行から押した行を引いた行の数である。ドラッグの間、マウスの行とつまみの最初の行の差をこの値に保つ。
	offset int
	// prev は、最後に表示領域の最初の行を決めたときの、表示領域の中へ寄せたつまみの最初の行である（押したときはつまみの最初の行）。
	// moved は、それが押したときから変わったかである（変わらないドラッグでは表示領域を動かさず、印へ移る）。
	prev  int
	moved bool
	// cursorRow と cursorCol は、押したときのカーソルの、表示領域の中の見た目の行と列である。行を折り返さないときの列は、
	// 行の先頭からの表示の列である（縦に送っても横の位置は変わらないため）。
	cursorRow, cursorCol int
	// marks は、つまみの上に描いた印を押したときの、その印の種類の行である（離したときに移る。無ければ空）。
	marks []int
}

// mouseBarPress は、右端のスクロールバーのつまみか印（t。Kind は MouseScrollbar）で左のボタンを押したことである（FR-210、FR-211）。
// nvim-scrollview（コミット ce007c654e6af5f9dc5a8c92ea300e623fface1a の lua/scrollview.lua の handle_mouse）に合わせた。
//
//   - ビジュアルモードなら、選択を終えてノーマルモードへ戻る（nvim-scrollview も、マウスを扱う前にビジュアルモードを抜ける）。
//     インサートモードはインサートモードのままにする
//   - つまみの外の印なら、すぐにその印の行へ移る（mouseMarkJump）
//   - つまみなら、つまみのドラッグを始める。押しただけではカーソルも表示領域も動かさない。つまみの上に描いた印なら、
//     つまみを動かさずに離したときにその印の行へ移る（つまみと印を 1 列に重ねて描くので、どちらも押せるようにする）
//
// req: FR-210, FR-211
func (s *State) mouseBarPress(t MouseTarget) {
	if s.mode.visual() {
		s.exitVisual()
	}
	if !t.Thumb {
		if len(t.Marks) > 0 {
			s.mouseMarkJump(t.Marks)
		}
		return
	}
	v, ok := s.scrollView()
	if !ok {
		return
	}
	d := &mouseBarDrag{offset: t.ThumbRow - t.Row, prev: t.ThumbRow, marks: t.Marks}
	// カーソルの見た目の行の、表示領域の最初の行からの行数。
	height := s.lineHeight()
	d.cursorRow = -v.Skip + s.cursorRow()
	for l := v.Top; l < s.cursor.Line; l++ {
		d.cursorRow += height(l)
	}
	d.cursorCol = s.cursorColumn()
	if w := s.screenWidth(); w > 0 {
		d.cursorCol %= w
	}
	s.mouseBar = d
}

// mouseBarMove は、つまみを押した後のドラッグ（<LeftDrag>）である（FR-210）。nvim-scrollview の handle_mouse に合わせた。
//
//   - つまみの最初の行を、マウスの行に押したときの差（mouseBarDrag.offset）を足した行（表示領域の外なら端の行）へ動かす。
//     表示領域の最初の行は Mouse.ScrollbarTop が決める（0 行目なら最初の行、最後の行ならバッファの最後の行）。
//     つまみは、その行に描かせる（Mouse.HoldScrollbarThumb）
//   - 寄せたつまみの行が前のドラッグ（初めは押したときのつまみの行）から変わらなければ、何もしない（nvim-scrollview の
//     previous_row ~= row。つまみを掴んだまま横へ動かしても、表示領域は動かない）
//   - ドラッグの途中でマウスでないキーを受けたら、ドラッグを終える（State.feed。nvim-scrollview も、マウスでない入力でドラッグを終える）
//   - カーソルは、押したときと同じ表示領域の中の見た目の行と列に置く。その行が最後の行より下なら最後の行にする。上下の余白
//     （scroll.margin）の中になったら、表示領域を動かさずに、余白の外の行へ移す（View.CursorCorrect）。望みの列は置いた列にする。
//     nvim-scrollview は、ドラッグの間はカーソルを最初の行に置き、離したときに押したときの位置へ戻す（H0 と gj と l で動かす）が、
//     本体はドラッグの間から戻した位置に置く（離した後は同じ位置になる）
//   - ジャンプリストには記録しない（nvim-scrollview は keepjumps で動かす）
//   - インサートモードでは、インサートモードのまま、カーソルが動いたときだけ入力を区切る
//
// req: FR-210
func (s *State) mouseBarMove() {
	d := s.mouseBar
	t := s.Mouse.Target(true)
	if t.Kind != MouseScrollbar {
		return
	}
	at, top, ok := s.Mouse.ScrollbarTop(t.Row + d.offset)
	if !ok || at == d.prev {
		return
	}
	v, ok := s.scrollView()
	if !ok {
		return
	}
	d.prev, d.moved = at, true
	v.Top, v.Skip = min(max(top, 0), s.lastLine()), 0
	s.Mouse.HoldScrollbarThumb(at, v.Top)
	// 押したときのカーソルの見た目の行に当たる、新しい表示領域の行と、その行の中の見た目の行。
	height := s.lineHeight()
	line, k := v.Top, max(d.cursorRow, 0)
	for line < s.lastLine() && k >= height(line) {
		k -= height(line)
		line++
	}
	k = min(k, height(line)-1)
	if c := v.CursorCorrect(line); c != line {
		line, k = c, 0
	}
	col := d.cursorCol
	if w := s.screenWidth(); w > 0 {
		col += k * w
	}
	old := s.cursor
	s.cursor = s.mousePos(MouseTarget{Kind: MouseText, Line: line, Col: col})
	s.updateWant()
	s.setView(v, true)
	if s.mode == ModeInsert && s.cursor != old {
		s.afterInsertMove(old)
	}
}

// mouseMarkJump は、スクロールバーの印を押したことである（FR-211）。印の種類の行 lines（小さい順）のうち、カーソルの行より後の
// 最初の行へ移り、無ければ最初の行へ回る（nvim-scrollview の handle_mouse の subsequent(props.lines, current, 1, true)）。
// nvim-scrollview はその行へ normal! {行}G で移るので、本体も G と同じく、移る前の位置をジャンプリストに記録し（<C-o> で戻れる）、
// 行の最初の空白でない文字へ移る。インサートモードでは、インサートモードのまま移り、入力を区切る。
//
// req: FR-211
func (s *State) mouseMarkJump(lines []int) {
	if len(lines) == 0 {
		return
	}
	to := lines[0]
	for _, l := range lines {
		if l > s.cursor.Line {
			to = l
			break
		}
	}
	to = min(max(to, 0), s.lastLine())
	old := s.cursor
	s.markJump()
	s.cursor = s.firstNonBlankPos(to)
	if s.mode != ModeInsert {
		s.cursor = s.clampNormal(s.clampPos(s.cursor))
	}
	s.updateWant()
	s.followCursor()
	if s.mode == ModeInsert {
		s.afterInsertMove(old)
	}
}

// mouseSelect は、<2-LeftMouse>（line が false。mouse.select-word）と <3-LeftMouse>（line が true。mouse.select-line）である
// （FR-183）。マウスの位置へ移ってから、単語（iw と同じ範囲。空白の上なら空白の並び）を文字単位の、行を行単位のビジュアルモードで選ぶ。
// 単語を選んだときのカーソルは単語の終わりに、行を選んだときのカーソルは押した位置に置く（Neovim 0.12.5 と同じ）。
// インサートモードでは、インサートモードを抜けてから選ぶ。続くドラッグは、選んだ範囲の始まりから文字単位で広げる
// （Neovim は単語と行の単位で広げる。docs/vim-differences.md）。
// 4 回目のクリック（Neovim の矩形の選択）は、本体に矩形のビジュアルモードが無いので、1 回目と同じ操作（mouse.click）にした。
// 右端のスクロールバーのつまみと印の上では、もう 1 回の 1 回目のクリックとして扱う（mouseBarPress。印をすばやく続けて押して、
// 同じセルの印の行を順に回れるようにする。docs/vim-differences.md）。
//
// req: FR-183, FR-211
func (s *State) mouseSelect(line bool) error {
	s.mouseAnchor, s.mouseDragging, s.mouseBar = nil, false, nil
	if s.Mouse == nil {
		return nil
	}
	t := s.Mouse.Target(false)
	switch t.Kind {
	case MouseNone, MouseWindow:
		return nil
	case MouseScrollbar:
		s.mouseBarPress(t)
		return nil
	case MouseElsewhere:
		return s.Mouse.Activate()
	case MouseText:
	}
	if s.mode == ModeInsert {
		s.finishInsert()
	}
	if s.mode.visual() {
		s.exitVisual()
	}
	s.mouseMove(t)
	s.parser.SetVisual(true)
	s.visual.anchor, s.visual.head = s.cursor, s.cursor
	if line {
		s.mode = ModeVisualLine
	} else {
		s.mode = ModeVisual
		cmd := editcmd.Command{Count: 1, Target: editcmd.Target{Kind: editcmd.TargetTextObject, Ref: editcmd.Ref{ID: "inner-word"}}}
		if err := s.visualTextObject(cmd); err != nil {
			return err
		}
		s.followCursor()
	}
	at := s.visual.anchor
	s.mouseAnchor = &at
	return nil
}

// mouseScroll は、<ScrollWheelDown>（down が true。mouse.scroll-down）と <ScrollWheelUp>（mouse.scroll-up）である（FR-182）。
// マウスの位置がこのバッファのテキストか、この表示領域のステータス行かコマンドラインの行（MouseWindow。Neovim 0.12.5 と同じ）なら、
// 表示領域を設定の mouse.scrollLines の行だけ送る。送り方は <C-e> と <C-y> と同じで
// （Neovim 0.12.5 の mousescroll=ver:3 の既定と同じく、カーソルが表示領域から出たら、余白を残した端の行へ移す）、
// ビジュアルモードでは選択の端も動き、インサートモードではインサートモードのまま、カーソルが動いたときだけ入力を区切る。
// 0 行の設定と、ほかの部品の位置（タブの行、枠線など）では何もしない。
//
// req: FR-182
func (s *State) mouseScroll(down bool) error {
	n := s.optionInt(option.MouseScrollLines)
	if s.Mouse == nil || n <= 0 || !s.Mouse.Target(false).scrollable() {
		return nil
	}
	old := s.cursor
	if err := actScrollLine(down)(s, editcmd.Command{Count: n, CountGiven: true}); err != nil {
		return err
	}
	if s.mode == ModeInsert && s.cursor != old {
		s.afterInsertMove(old)
	}
	return nil
}

// mouseScrollHorizontal は、<ScrollWheelRight>（right が true。mouse.scroll-right）と <ScrollWheelLeft>（mouse.scroll-left）である
// （FR-182）。行を折り返さないときに、マウスの位置がこのバッファのテキストなら、表示領域の左の端を設定の mouse.scrollColumns の列だけ送る。
// Neovim 0.12.5 の mousescroll=hor:6 に合わせた。
//
//   - 左の端は、表示領域に見える行のうち最も長い行の、最後の列（幅から 1 を引いた列）より右へ送らない
//   - 送った後の左の端がカーソルの行の幅より右なら、カーソルを見える行のうち最も長い行の先頭へ移す
//   - カーソルが左右の余白（scroll.sideMargin）を残した範囲から出たら、範囲の端の列へ移し、望みの列をその列にする
//
// 縦のホイールと同じく、ステータス行とコマンドラインの行の上でも送る。
// 行を折り返すとき、0 列の設定、ほかの部品の位置、横の位置を持たない表示領域では何もしない。
//
// req: FR-182
func (s *State) mouseScrollHorizontal(right bool) error {
	n := s.optionInt(option.MouseScrollColumns)
	hv, ok := s.Viewport.(HorizontalViewport)
	if s.Mouse == nil || !ok || n <= 0 || s.screenWidth() > 0 || !s.Mouse.Target(false).scrollable() {
		return nil
	}
	w := hv.Columns()
	if w <= 0 {
		return nil
	}
	top := min(max(hv.Top(), 0), s.lastLine())
	last := min(top+max(hv.Rows(), 1)-1, s.lastLine())
	longest, widest := s.cursor.Line, 0
	for l := top; l <= last; l++ {
		if lw := s.lineWidth(l); lw > widest {
			longest, widest = l, lw
		}
	}
	left := hv.Left()
	next := left - n
	if right {
		next = left + n
	}
	next = min(max(next, 0), max(widest-1, 0))
	if next == left {
		return nil
	}
	old := s.cursor
	if next > s.lineWidth(s.cursor.Line) {
		s.cursor = pos(longest, 0)
	}
	siso := min(s.optionInt(option.ScrollSideMargin), (w-1)/2)
	lo, hi := next+siso, next+w-1-siso
	line := s.line(s.cursor.Line)
	cols := s.columns()
	want := -1
	if line != "" {
		start, end := s.charColumns(line, s.cursor.Byte)
		switch {
		case start < lo:
			// Neovim の set_leftcol と同じく、左の端に掛かる文字（全角文字、タブ）は、その次の文字にする。
			b := cols.ByteAt(line, lo)
			if st, _ := s.charColumns(line, b); st < next && b < lastCharStart(line) {
				b = nextChar(line, b)
			}
			s.cursor.Byte, want = b, lo
		case end > hi:
			// 右の端に掛かる文字は、その前の文字にする。
			b := cols.ByteAt(line, hi)
			if _, e := s.charColumns(line, b); e > next+w-1 && b > 0 {
				b = prevChar(line, b)
			}
			s.cursor.Byte, want = b, hi
		}
	}
	if s.mode == ModeInsert {
		s.cursor = s.clampPos(s.cursor)
	} else {
		s.cursor = s.clampNormal(s.clampPos(s.cursor))
	}
	if s.cursor != old {
		if want >= 0 {
			s.want = want
		} else {
			s.updateWant()
		}
		if s.mode.visual() {
			s.cursor = s.visualCursor()
		}
		s.followCursor()
	}
	hv.SetLeft(next)
	if s.mode == ModeInsert && s.cursor != old {
		s.afterInsertMove(old)
	}
	return nil
}

// optionInt は、エディタ全体の整数の設定 name の今の値を返す。設定の口が無ければ（テスト）表の既定値である。
func (s *State) optionInt(name string) int {
	d, ok := option.Lookup(name)
	if !ok {
		return 0
	}
	if s.Ex.Options == nil {
		return d.Default.Int()
	}
	return s.Ex.Options.Get(d).Int()
}

// charColumns は、行 line の b の文字が占める最初と最後の表示の列を返す（タブと全角文字は複数の列を占める）。
// 望みの列の数え方（target.Columns）は、文字が占める列を返さないので、ByteAt で前後の列を引いて求める。
func (s *State) charColumns(line string, b int) (start, end int) {
	cols := s.columns()
	width := cols.Column(line, len(line))
	c := min(cols.Column(line, b), max(width-1, 0))
	start, end = c, c
	for start > 0 && cols.ByteAt(line, start-1) == b {
		start--
	}
	for end+1 < width && cols.ByteAt(line, end+1) == b {
		end++
	}
	return start, end
}
