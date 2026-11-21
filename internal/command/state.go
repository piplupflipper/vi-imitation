// Package command は、編集コマンドの値（internal/input の Command）を実行し、レジスター、ドットリピート、マクロの記録を受け持つ
// （基本設計 2 章、3 章）。
//
// 編集の状態（バッファ、カーソル、望みの列、モード、レジスター、ドットリピートとマクロの記録、直前の f の検索、Ex の状態）は
// State 1 つにまとめる。State はイベントループの goroutine 1 つだけが触る前提で、排他制御をしない（基本設計 2.1）。
// 端末にも拡張機能のプロセスにも依存しない。拡張機能への問い合わせは Extensions を通して呼び出し側が差し込む。
//
// # 使い方
//
// イベントループは、キーを 1 個ずつ State.Feed に渡す。Feed はモードに応じてキーを解釈し、
// 編集コマンドの値が完成すれば実行して、バッファに適用した変更、Ex コマンドが求める動作、失敗を Result で返す。
// カーソルとモードは、Feed の後に Cursor と Mode で読む。
//
// # Vim との対応
//
// 振る舞いは Vim 9.1 を `vim -Nu NONE` で起動したときの既定の設定に合わせる。
// 'shiftwidth' と 'tabstop' は 8、'expandtab' と 'autoindent' はオフ、'joinspaces' はオン、'selection' は inclusive である。
// ただし 'backspace' は、Vim の既定の空ではなく、多くの利用者が設定する "indent,eol,start" に当たる動きにする
// （インサートモードの <BS> で、挿入を始めた位置より前の文字や行の区切りも消せる）。
// Vim と違う点は、各関数の doc コメントに書く。1 つの関数に収まらない違いと未実装の機能（:normal など）は、
// docs/vim-differences.md の一覧に書く。
package command

import (
	"errors"
	"fmt"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/ex"
	"github.com/piplupflipper/vi-imitation/internal/command/target"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// Mode は、エディタのモードである（FR-01）。
type Mode int

// モード。
const (
	// ModeNormal は、ノーマルモードである。
	ModeNormal Mode = iota
	// ModeInsert は、インサートモードである。
	ModeInsert
	// ModeVisual は、文字単位のビジュアルモード（v）である。
	ModeVisual
	// ModeVisualLine は、行単位のビジュアルモード（V）である。
	ModeVisualLine
	// ModeCommandLine は、コマンドラインモード（:）である。
	ModeCommandLine
)

// String は、モードの名前を返す。
func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "normal"
	case ModeInsert:
		return "insert"
	case ModeVisual:
		return "visual"
	case ModeVisualLine:
		return "visual line"
	case ModeCommandLine:
		return "command line"
	}
	return fmt.Sprintf("Mode(%d)", int(m))
}

// visual は、ビジュアルモードのどちらかであるかを返す。
func (m Mode) visual() bool { return m == ModeVisual || m == ModeVisualLine }

// ErrFailed は、操作が成り立たなかったことを表す（Vim がビープを鳴らす場面）。
// モーションの移動先が無い、レジスターが空、回数が行の数を超える、などである。
// 失敗した操作はバッファを変えない。
var ErrFailed = errors.New("command: operation failed")

// ErrInterrupted は、State.Interrupted が true を返したので、マクロの再生を途中で止めたことを表す。
var ErrInterrupted = errors.New("command: interrupted")

// interrupted は、利用者が中断を求めたかを返す。
func (s *State) interrupted() bool { return s.Interrupted != nil && s.Interrupted() }

// failf は、ErrFailed を包んだエラーを返す。
func failf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrFailed, fmt.Sprintf(format, args...))
}

// Result は、Feed、Execute、ExecuteCommandLine の結果である。
type Result struct {
	// Changes は、バッファに適用した変更を、適用した順に並べたものである。拡張機能へ差分を送る（FR-16）ときに使う。
	Changes []buffer.Change
	// Actions は、Ex コマンドが求めるエディタの動作（保存、終了、ファイルを開く）である。呼び出し側が先頭から順に行う。
	Actions []ex.Action
	// Deferred は、拡張機能が結果を保留した（ErrDeferred）操作である。無ければ nil。
	// 呼び出し側は、結果が届いた時点で State.ApplyDeferred に渡す（FR-18）。
	// Err が ErrQueryPending のときは、再生中に結果を待つために止めた操作で、State.ResumeReplay に渡す（FR-19）。
	Deferred *Deferred
	// Err は、操作の失敗である。マクロの再生は、失敗した時点で止める。
	// 失敗までに適用した変更は Changes に残る。
	Err error
	// Confirm は、Ex コマンドが続きを実行する前に利用者の確認を求めたときに nil でない（逆順の範囲の :4,2d など）。
	// このとき Err は E493 の *ex.Error である。呼び出し側は Confirm.Prompt を示して y か n を受け、State.Confirm に渡す。
	Confirm *ex.Confirm
	// Remaining は、Err が ErrBufferSwitched か ErrShellPending か ErrQueryPending のときの、マクロの再生の残りである。
	// 呼び出し側は、カーソルのあるバッファの State の ContinueReplay（ErrQueryPending なら ResumeReplay）に渡して再生を続ける。
	Remaining []ReplaySegment
	// Message は、操作が利用者に知らせる通知である（検索したパターンと、検索の折り返しの警告）。無ければ Text が空である。
	// 呼び出し側は、Err が nil でなければ Err を優先して知らせる。
	Message Message
	// Shell は、Ex コマンド（:{range}! と :r !）が外部のコマンドの実行を求めたときに nil でない（FR-81）。
	// 呼び出し側はコマンドを実行し、終わったら State.FinishShell に結果を渡す。
	Shell *ex.ShellRequest
	// Yanked は、呼び出しの中で本体の y（yy、Y、yiw、ビジュアルモードの y と Y を含む）がレジスターに写した範囲である。
	// 呼び出しの中で何度ヤンクしても、最後の 1 つだけを持つ。無ければ nil。
	// 呼び出し側は、ヤンクした範囲に一時的に色を付ける（FR-140。internal/editor）。
	Yanked *Yanked
}

// Yanked は、本体の y がレジスターに写した範囲である（Result.Yanked）。
// d、c、x のようにテキストを消すオペレーターは、消した範囲を知らせない。ブラックホールのレジスター（"_y）へのヤンクは、
// どこにも写さないので知らせない（nvim 0.12.5 も、"_yy では TextYankPost を起こさない。2026-11-20 に確かめた）。
type Yanked struct {
	// Range は、ヤンクした範囲（半開区間）である。行単位の範囲は、始まりの行の先頭から終わりの行の行末までである。
	// 位置は、ヤンクした時点のテキスト（バージョン番号 Version のテキスト）を指す。
	Range buffer.Range
	// Linewise は、行単位でヤンクしたかである。
	Linewise bool
	// Version は、ヤンクした時点のバッファのバージョン番号である。呼び出しの中でヤンクの後にバッファを変えていれば、今の番号と違う。
	Version uint64
	// Replayed は、マクロの再生かドットリピートの中のヤンクであるかである（Replaying）。
	// nvim の vim.hl.on_yank が、マクロの実行中（reg_executing()）のヤンクに色を付けないのに当たる判断は、呼び出し側が行う。
	Replayed bool
}

// State は、1 つのバッファを編集する状態である。
// ゼロ値は使わず、New で作る。
type State struct {
	// Columns は、望みの列の数え方である。nil なら target.CharColumns を使う。
	// 表示幅で数える実装（internal/ui）は、呼び出し側が差し込む。
	Columns target.Columns
	// ScreenLines は、行を折り返して描くときの見た目の行の数え方である（gj と gk。FR-157）。nil なら折り返さないとして扱い、
	// gj と gk は j と k と同じに動く。スクロールの操作（<C-e>、<C-d>、<C-f>、zt など）も、行の高さをこれで数える。
	// 画面の幅と設定（lineWrap.enabled）で決まるので、呼び出し側（internal/editor）が差し込む。
	ScreenLines target.ScreenLines
	// TextWidth は、文字列 s を列 0 から並べたときの表示幅を、タブ文字の幅 tabStop で数える関数である（字下げの幅の数え方。FR-160）。
	// nil なら、タブ文字は次のタブ位置まで、ほかの文字は 1 列として数える。全角文字の幅を数える実装（ui.StringWidth）は、呼び出し側が差し込む。
	TextWidth func(s string, tabStop int) int
	// Extensions は、拡張機能が提供する操作の問い合わせ先である。nil なら、拡張機能の操作は失敗する。
	Extensions Extensions
	// Ex は、Ex コマンドの呼び出しをまたいで保つ状態である。Mark は New が '< と '> を返す関数にする。
	Ex ex.State
	// Interrupted は、利用者が中断を求めたか（端末の <C-c> など）を返す関数である。nil なら中断しない。
	// マクロの再生（999999999@a など）は、キーやコマンドラインを 1 つ実行するたびにこれを呼び、true なら ErrInterrupted で止める。
	// 再生は Feed の中で同期に進むので、呼び出し側は別の goroutine で立てた印（atomic.Bool など）を読む関数を渡す。
	Interrupted func() bool
	// Viewport は、画面のバッファの表示領域である。スクロールの操作（<C-e>、<C-d>、<C-f>、<PageDown>、zt など。FR-173）が読み書きする。
	// nil なら、スクロールの操作は失敗する。
	Viewport Viewport
	// Navigator は、ジャンプリストとバッファの切り替えの口である。nil なら、ジャンプを記録せず、<C-o>、<C-i>、<C-^> は失敗する。
	Navigator Navigator
	// Mouse は、マウスの出来事の位置を解く口である（FR-181〜FR-185。mouse.go）。nil なら、マウスの出来事の操作は何もしない。
	Mouse Mouse
	// KeyHook は、ノーマルモードでキーの列の途中でないときと、インサートモードのときに、キーを解釈する前に呼ぶ関数である。
	// done が true のキーは、この State では解釈しない。返した誤りは操作の失敗として結果に入れる（ErrBufferSwitched なら、
	// マクロの再生の残りは移った先のバッファで続く）。nil なら呼ばない。
	// 呼び出し側は、キーの列の途中のキーを done で預かってよい。列が当たらなかったら、done を false にして預かったキーを held で返す。
	// State は held を k より先に、通常のキーとして（KeyHook を通さずに）解釈する。
	// Feed の記録とマクロの再生の両方を通るので、呼び出し側が引き受けたキーもマクロに記録され、再生で同じく引き受けられる
	// （ポップアップを閉じる <Esc> と、項目を選ぶ <CR>。internal/editor。PR #242 のレビューの必須 1）。
	KeyHook func(k editcmd.Key, insert bool) (done bool, held []editcmd.Key, err error)

	buf    *buffer.Buffer
	cursor buffer.Position
	want   int
	mode   Mode

	// scrollAmount は、回数を付けた <C-d> と <C-u> が決めた、送る行数（Vim の 'scroll'）である。0 なら決めていない。
	// scrollAmountRows は、決めたときの表示領域の行数で、表示領域の行数が変わったら決めていないとして扱う（halfAmount）。
	scrollAmount, scrollAmountRows int
	// mouseAnchor は、マウスのボタンを押した位置（ドラッグで選ぶ範囲の始まり）である。ボタンを押していなければ nil である（mouse.go）。
	mouseAnchor *buffer.Position
	// mouseDragging は、ボタンを押した後にドラッグで選び始め、まだ離していないことを表す（MouseDragging）。
	mouseDragging bool
	// mouseBar は、スクロールバーのつまみを押してから離すまでの、つまみのドラッグの状態である。押していなければ nil である（mouse.go）。
	mouseBar *mouseBarDrag

	parser    KeyParser
	registers Registers
	lastFind  target.Find

	// res は、処理中の Feed などの結果を溜める。
	res Result

	// undoAt は、次の apply が新しいアンドゥの単位を作るときに、変更の前のカーソルとして記録する位置である。
	// undoAtSet が false なら、apply の時点のカーソルを記録する。apply が使うと undoAtSet を戻す（markUndoCursor）。
	undoAt    buffer.Position
	undoAtSet bool

	// ドットリピート（FR-06）。
	dot *editcmd.Command
	// lastChange は、再生（@a）ではない直前の変更である。マクロの再生の置き場の初期値にする（FR-57）。
	// State.dot が再生ではない値の間は、dot より古いことがある（再生の始まりと終わりの noteChange で追いつく）。
	lastChange *editcmd.Command
	dotVisual  visualExtent
	// dotLines は、インサートモードの入力の記録をドットリピートの対象にしたときの、入力の位置を置いた行の数である（FR-141）。
	dotLines dotLines
	// repeatLines は、. が行ごとに入力の位置を置く操作（I と A）を繰り返している間の、位置を置く行の数である（repeat）。
	// 0 なら繰り返しの中ではない。actInsert が読む。
	repeatLines int
	replaying   bool // ドットリピートの再実行中
	insert      insertState
	visual      visualState
	cmdline     []rune
	search      *pendingSearch // / か ? でパターンを入力している間の値。Ex コマンドの入力中は nil
	// searchGen は、/ か ? の入力を始めた回数である（pendingSearch.gen）。
	searchGen uint64
	// searchSteps は、<CR> で決めた / と ? を実行している間だけ、<C-g> と <C-t> で動かした数を持つ（finishSearch。request が読む）。
	searchSteps int
	// searchBackward は、直前の検索（/、?、*、#）が後ろ向きだったかである。n と N の向きを決める。
	searchBackward bool
	// matchCache は、検索の一致の件数（FR-167）のために最後に集めた一致の一覧である（searchCount）。無ければ nil。
	matchCache *matchCache
	recording  rune // マクロの記録先。0 なら記録していない
	recorded   []editcmd.Key
	lastPlayed rune // @@ が再生するレジスター
	playDepth  int
	// playUndoStart は、いちばん外側の再生（play、ContinueReplay、ResumeReplay）を始めたときのアンドゥの履歴の末尾の識別子である
	// （buffer.Buffer.UndoTip）。止めたときの末尾と違えば、止める前の再生が単位を作っている（suspendQuery、ResumeReplay）。
	playUndoStart uint64
	// shellWait は、再生の途中の :{range}! と :r ! で外部のコマンドを待つために止めた（ErrShellPending）ときの控えである。
	// FinishShell が使って下ろす。再生の外で外部のコマンドを求めたら nil にする（applyExResult）。
	shellWait *shellSuspend
	// shellJoin は、FinishShell が再生の途中の外部のコマンドの出力を入れた後の、アンドゥの単位である。
	// 続く ContinueReplay が、再生の残りの変更をこの単位へつなぐ。Feed は下ろす。
	shellJoin *undoJoin
	// lastPut は、直前に実行した値が貼り付けなら、その記録である（FR-55）。execute が値を実行するたびに prevPut へ移して消すので、
	// 別の操作を挟むと無くなる。prevPut は、実行中の値の直前の貼り付けの記録で、<C-p> と <C-n>（actPutCycle）が読む。
	lastPut, prevPut *putRecord
	// keysFed は、解釈したキーの数である（KeysFed）。マクロの再生で解釈したキーも数える。
	keysFed uint64
}

// KeysFed は、この State が解釈したキーの数を返す。利用者が打ったキーに加えて、マクロの再生で解釈したキーも数える。
// 呼び出し側（internal/editor）は、拡張機能に問い合わせた後に次のキーを解釈したかを、この値の変化で調べる
// （popup/show を「次のキー入力で消す」規則。基本設計 6.7）。
func (s *State) KeysFed() uint64 { return s.keysFed }

// KeyParser は、キーを 1 個ずつ受け取り、編集コマンドの値に解釈する口である（基本設計 3 章の「入力の解釈」）。
// internal/input の *Parser が満たす。internal/input は同じ層のパッケージなので、このパッケージは import せず、
// 呼び出し側（internal/editor）が New に渡す。各メソッドの意味は input.Parser の同じ名前のメソッドと同じである。
type KeyParser interface {
	Feed(k editcmd.Key) []editcmd.Result
	Flush() []editcmd.Result
	Pending() bool
	SetRecording(on bool)
	SetVisual(on bool)
	// InsertAction は、インサートモードのキー k に割り当てた操作を返す（FR-127）。insertKey が、文字、<CR>、<Tab>、<Esc> の他のキーで呼ぶ。
	InsertAction(k editcmd.Key) (editcmd.Ref, bool)
}

// New は、バッファ b を編集する State を、ノーマルモード、カーソルは先頭で作る。
// p は、キーを解釈する口である（ふつうは input.NewParser に、拡張機能の操作を足したキーマップを渡したもの）。
// マクロの再生も p を通すので、p は State 専用のものを渡す。
func New(b *buffer.Buffer, p KeyParser) *State {
	s := &State{buf: b, parser: p}
	s.Ex.Mark = s.mark
	return s
}

// Buffer は、編集しているバッファを返す。
func (s *State) Buffer() *buffer.Buffer { return s.buf }

// Cursor は、カーソルの位置を返す。
// ノーマルモードでは、空の行を除いて行末の位置を指さない。インサートモードでは行末の位置を指しうる。
// ビジュアルモードでは、Vim と同じく、j、k、$ で移った短い行や l で進んだ行で行末の位置を指しうる（visualCursor）。
func (s *State) Cursor() buffer.Position { return s.cursor }

// SetCursor は、カーソルを pos へ移す。バッファの外の位置は最も近い位置に丸める。
// 望みの列は、移した位置の列にする。
// ビジュアルモードでは、行末の位置（行末の後）をそのまま残す（Vim の check_cursor_col は、'selection' が old でない
// ビジュアルモードで NUL の位置を残す。拡張機能の編集の後に呼び出し側が置き直しても、選択が改行を含んだままになる。#89）。
// ビジュアルモードでは、カーソルが動いたなら、カーソルを動かす他の操作と同じく、選択の動く側の端（head）をカーソルに合わせる
// （visualState の doc）。動かなければ head に触れないので、呼び出し側が同じ位置へ置き直しても select-all の選択は残る。
// インサートモードの同時の入力（FR-141）の間にカーソルが動いたなら、カーソルキーで動いたときと同じく同時の入力を終える（endSites）。
// 外からの編集に合わせてカーソルをずらすときは、SetCursor ではなく ShiftPositions を使う。
func (s *State) SetCursor(pos buffer.Position) {
	old := s.cursor
	s.placeCursor(pos)
	if s.cursor != old {
		s.followCursor()
		s.endSites()
	}
	s.updateWant()
}

// ShiftPositions は、利用者の操作でない編集（拡張機能の buffer/edit など）をバッファへ適用した後に、
// カーソルと、ビジュアルモードの選択の両端（anchor と head）と、インサートモードの同時の入力の位置（FR-141）を、
// それぞれ shift でずらしてバッファの中に丸める。
// shift は、編集の前の位置を編集の後の位置へ写す関数である（editor の shiftKept）。
// inside は、位置が編集で置き換えた範囲の内側（範囲の始まりより後ろで、終わりより前）にあったかである。
// 内側の位置は、写した先が範囲の始まりに寄せた位置で、もとの文字との対応が無い。
// 選択の両端は、カーソルのずれではなく自分の位置のずれで動く（SetCursor のように head をカーソルへ合わせない）。
// 普段の状態（head がカーソルにある）では、両方が同じ位置へずれるので、head はカーソルにあるままである。
// カーソルと選択の両端は、内側にあっても寄せた位置に置く。
// 同時の入力の位置も自分の位置のずれで動き、内側にあった位置、バッファの外へ出た位置、ほかの位置と同じ行へ寄った位置は落とす（shiftSites）。
// 望みの列は、ずらした後のカーソルの列にする（SetCursor と同じ）。
//
// req: FR-141
func (s *State) ShiftPositions(shift func(buffer.Position) (p buffer.Position, inside bool)) {
	to := func(p buffer.Position) buffer.Position {
		q, _ := shift(p)
		return q
	}
	if s.mode.visual() {
		s.visual.anchor = s.clampPos(to(s.visual.anchor))
		s.visual.head = s.clampPos(to(s.visual.head))
	}
	s.placeCursor(to(s.cursor))
	s.shiftSites(shift)
	s.updateWant()
}

// placeCursor は、カーソルを pos へ置き、モードに合わせて丸める（SetCursor の doc）。head と望みの列には触れない。
func (s *State) placeCursor(pos buffer.Position) {
	s.cursor = s.clampPos(pos)
	if s.mode != ModeInsert && !s.mode.visual() {
		s.cursor = s.clampNormal(s.cursor)
	}
}

// Want は、望みの列（Vim の curswant）を返す。target.WantEOL は「行末」を表す。
func (s *State) Want() int { return s.want }

// Mode は、現在のモードを返す。
func (s *State) Mode() Mode { return s.mode }

// Registers は、レジスターを返す。呼び出し側が読み書きしてよい。
func (s *State) Registers() *Registers { return &s.registers }

// LastFind は、; と , が繰り返す直前の f、F、t、T の検索を返す。
func (s *State) LastFind() target.Find { return s.lastFind }

// Recording は、マクロを記録しているレジスターを返す。記録していなければ 0 を返す。
func (s *State) Recording() rune { return s.recording }

// Pending は、ノーマルモードかビジュアルモードで、キーの列の解釈が途中であるかを返す。
func (s *State) Pending() bool { return s.parser.Pending() }

// Flush は、時間切れで、待っている曖昧なキーの列を確定させて実行する（KeyParser.Flush）。
// イベントループは、キーが一定の時間来なかったときに呼ぶ。
func (s *State) Flush() Result {
	s.res = Result{}
	if s.mode == ModeNormal || s.mode.visual() {
		s.runResults(s.parser.Flush())
	}
	return s.take()
}

// PendingChanges は、処理中の Feed、Execute、ApplyDeferred などが、その呼び出しの中でここまでに適用した変更を、適用した順に返す。
// 呼び出しが戻ると Result.Changes として渡す変更と同じものである。
// Extensions の実装は、マクロの再生の途中で問い合わせる前に、ここまでの差分を拡張機能へ送る（FR-16）ためにこれを読む。
// 返したスライスは書き換えない。
func (s *State) PendingChanges() []buffer.Change { return s.res.Changes }

// take は、溜めた結果を返し、溜める場所を空にする。
func (s *State) take() Result {
	r := s.res
	s.res = Result{}
	return r
}

// Feed は、キーを 1 個受け取り、現在のモードに応じて解釈し、完成した操作を実行する。
// マクロの記録中は、受け取ったキーを記録する（FR-05）。
//
// req: FR-01, FR-05
func (s *State) Feed(k editcmd.Key) Result {
	s.res = Result{}
	s.shellJoin = nil
	if s.recording != 0 {
		s.recorded = append(s.recorded, k)
	}
	s.feed(k)
	return s.take()
}

// FeedUnrecorded は、Feed と同じくキーを 1 個解釈するが、記録中のマクロに記録しない。
// 呼び出し側が、記録済みのキーから作ったキーを渡すとき（キーの一覧で選んだキーの実行。internal/editor）に使う。
func (s *State) FeedUnrecorded(k editcmd.Key) Result {
	s.res = Result{}
	s.feed(k)
	return s.take()
}

// feed は、キーを 1 個解釈する。マクロの再生もここを通るので、記録はしない。
func (s *State) feed(k editcmd.Key) {
	s.keysFed++
	if s.mouseBar != nil && !k.IsMouse() {
		// スクロールバーのつまみのドラッグの途中でマウスでないキーを受けたら、ドラッグを終える（FR-210。mouse.go の mouseBarMove）。
		s.mouseBar = nil
	}
	if h := s.KeyHook; h != nil && (s.mode == ModeInsert || (s.mode == ModeNormal && !s.parser.Pending())) {
		done, held, err := h(k, s.mode == ModeInsert)
		if done {
			if err != nil {
				s.fail(err)
			}
			return
		}
		// 呼び出し側が途中まで預かったキーの列が当たらなかった。預かったキーを通常のキーとして解釈してから k を解釈する
		// （Vim のマッピングの途中で当たらなかったキーの扱いと同じ。PR #242 の再レビューの必須）。
		for _, hk := range held {
			s.dispatch(hk)
		}
	}
	s.dispatch(k)
}

// dispatch は、キー k をモードに応じて解釈する（feed の KeyHook の後の部分）。
func (s *State) dispatch(k editcmd.Key) {
	switch s.mode {
	case ModeInsert:
		s.insertKey(k)
		return
	case ModeCommandLine:
		s.cmdlineKey(k)
		return
	case ModeVisual, ModeVisualLine:
		if k.Code == editcmd.CodeEsc && !s.parser.Pending() {
			s.exitVisual()
			return
		}
	case ModeNormal:
	}
	s.runResults(s.parser.Feed(k))
}

// runResults は、Parser の結果を順に実行する。
// 再生中の問い合わせの結果を待つために止めた（ErrQueryPending）ら、残りの結果は実行しない。
// 待つ間に実行すると、結果が届いて操作を実行し直す（ResumeReplay）ときに、バッファかカーソルが変わっているためである。
func (s *State) runResults(rs []editcmd.Result) {
	for _, r := range rs {
		if errors.Is(s.res.Err, ErrQueryPending) {
			return
		}
		switch r.Status {
		case editcmd.StatusDone:
			s.run(r.Command)
		case editcmd.StatusInvalid:
			s.fail(failf("invalid key sequence"))
		case editcmd.StatusCanceled:
		}
	}
}

// fail は、失敗を結果に記録する。最初の失敗を残す。
func (s *State) fail(err error) {
	if s.res.Err == nil {
		s.res.Err = err
	}
}

// Execute は、編集コマンドの値 cmd を実行する。
// キーを通さずに値を実行したいとき（拡張機能の操作をコマンドから呼ぶなど）に使う。Feed も内部でこれと同じ処理を通る。
func (s *State) Execute(cmd editcmd.Command) Result {
	s.res = Result{}
	s.run(cmd)
	return s.take()
}

// run は、値を実行し、失敗を結果に記録する。
// 拡張機能が結果を保留した（ErrDeferred）場合は、再生中でなければ失敗にせず、Result.Deferred に記録する。
// ドットリピートとマクロの再生の中で保留が返った場合は、失敗として扱う（FR-19）。
func (s *State) run(cmd editcmd.Command) {
	d := Deferred{Command: cmd, Version: s.buf.Version(), Cursor: s.cursor, mode: s.mode, anchor: s.visual.anchor, head: s.visual.head, want: s.want}
	err := s.execute(cmd)
	switch {
	case err == nil:
	case errors.Is(err, ErrDeferred) && !s.Replaying() && s.res.Deferred == nil &&
		cmd.Kind != editcmd.KindRepeat && cmd.Kind != editcmd.KindPlay:
		// 保留した操作は、まだ行っていないものとして扱う。ビジュアルモードのオペレーターが動かしたカーソルを戻す。
		s.cursor, s.want = d.Cursor, d.want
		s.res.Deferred = &d
	case errors.Is(err, ErrQueryPending):
		s.suspendQuery(d, err)
	default:
		s.fail(err)
	}
}

// suspendQuery は、再生中の問い合わせの結果を待つために、操作 d を Result.Deferred に入れて再生を止める（ErrQueryPending）。
// 保留した操作は、まだ行っていないものとして扱い、カーソルを戻す（ErrDeferred と同じ）。
// 入れ子の操作（. が繰り返す操作）で内側が既に入れていれば、内側のものを残す。
func (s *State) suspendQuery(d Deferred, err error) {
	if s.res.Deferred == nil {
		s.cursor, s.want = d.Cursor, d.want
		// 続けるときに、止める前の再生の単位へつなぎ、ドットリピートの値などを止めたときのものに戻す（ResumeReplay）。
		d.replay = &replaySnapshot{repeat: s.repeatState()}
		if s.playDepth > 0 && s.buf.UndoTip() != s.playUndoStart {
			d.replay.undoTip, d.replay.undoStart = s.buf.UndoTip(), s.playUndoStart
		}
		s.res.Deferred = &d
	}
	s.fail(err)
}

// execute は、編集コマンドの値を 1 つ実行する。
//
// req: FR-02
func (s *State) execute(cmd editcmd.Command) error {
	// 貼り付けの置き換え（FR-55）の対象は、直前の値の貼り付けだけである。
	s.prevPut, s.lastPut = s.lastPut, nil
	if t := cmd.Target; (cmd.Kind == editcmd.KindMotion || cmd.Kind == editcmd.KindOperator) &&
		t.Kind == editcmd.TargetMotion && t.Ref.Builtin() && t.Pattern == "" {
		if delim, ok := searchInputMotions[t.Ref.ID]; ok {
			// / と ? は、パターンをコマンドラインで受け取ってから実行する。
			s.startSearch(cmd, delim)
			return nil
		}
	}
	if err := s.checkNames(cmd); err != nil {
		return err
	}
	if cmd.Kind == editcmd.KindAction && isKeymapCommand(cmd.Action) {
		// 設定の keymap に書いた Ex コマンド（FR-154）。編集を禁止したバッファの検査とドットリピートの記録は、
		// コマンドラインで打ったときと同じく Ex コマンドの側に任せる。誤りは keymapCommand が Result.Err に入れる。
		s.keymapCommand(cmd.Action.ID)
		return nil
	}
	if err := s.checkReadOnly(cmd); err != nil {
		return err
	}
	switch cmd.Kind {
	case editcmd.KindMotion:
		if s.mode.visual() {
			return s.visualMotion(cmd)
		}
		return s.moveMotion(cmd)
	case editcmd.KindOperator:
		if s.mode.visual() {
			return s.visualOperator(cmd)
		}
		return s.change(cmd, func() error { return s.operator(cmd) })
	case editcmd.KindAction:
		return s.change(cmd, func() error { return s.action(cmd) })
	case editcmd.KindRecordStart:
		return s.startRecording(cmd.Char)
	case editcmd.KindRecordStop:
		s.stopRecording()
		return nil
	case editcmd.KindPlay:
		return s.play(cmd)
	case editcmd.KindRepeat:
		return s.repeat(cmd)
	}
	return failf("unknown command kind %d", cmd.Kind)
}

// checkNames は、値が指定したレジスターの名前（"x の x、q と @ の引数）を確かめ、使えない名前なら *InvalidRegisterError を返す（FR-50）。
// 入力の解釈は名前を検査せずに値へ入れるので、拡張機能のオペレーターと操作に渡す名前も含めてここで確かめる。
// @ の引数の @（@@）は、レジスターの名前ではないので通す。Vim の @: は持たない（要件定義 1.1）ので、: も E354 にする。
//
// req: FR-50
func (s *State) checkNames(cmd editcmd.Command) error {
	if err := checkRegister(cmd.Register); err != nil {
		return err
	}
	switch cmd.Kind {
	case editcmd.KindRecordStart:
		if !recordable(cmd.Char) {
			return &InvalidRegisterError{Name: cmd.Char}
		}
	case editcmd.KindPlay:
		// 再生できるのは a〜z（A〜Z は a〜z と同じ）と @（@@）だけである。@_ は、空の再生をドットリピートの対象にしないよう失敗させる。
		if c := cmd.Char; c != '@' && !recordable(c) {
			return &InvalidRegisterError{Name: c}
		}
	case editcmd.KindMotion, editcmd.KindOperator, editcmd.KindAction, editcmd.KindRecordStop, editcmd.KindRepeat:
	}
	return nil
}

// change は、バッファを変えうる操作 f を、アンドゥの 1 単位として実行する（FR-07）。
// インサートモードへ移った場合は、グループを開いたままにし、インサートモードを抜けるときに閉じる。
//
// req: FR-07
func (s *State) change(cmd editcmd.Command, f func() error) error {
	s.buf.BeginUndoGroup()
	err := f()
	if s.mode == ModeInsert && err == nil {
		return nil
	}
	s.buf.EndUndoGroup()
	if err == nil {
		s.recordDot(cmd)
	}
	return err
}

// apply は、編集の列を 1 つのトランザクションとしてバッファに適用し、変更を結果に溜める。
func (s *State) apply(edits ...buffer.Edit) error {
	if len(edits) == 0 {
		return nil
	}
	return s.applyTx(buffer.Transaction{BaseVersion: s.buf.Version(), Edits: edits})
}

// applyTx は、拡張機能が返したトランザクションを適用する。元にしたバージョン番号が違えば失敗する（FR-15）。
// 適用が新しいアンドゥの単位を作ったなら、その単位に変更の前のカーソルを記録する（markUndoCursor）。
func (s *State) applyTx(tx buffer.Transaction) error {
	at := s.cursor
	if s.undoAtSet {
		at = s.undoAt
	}
	ch, err := s.buf.Apply(tx)
	if err != nil {
		return err
	}
	if len(tx.Edits) > 0 {
		s.undoAtSet = false
		s.buf.SetUndoCursor(at)
	}
	s.res.Changes = append(s.res.Changes, ch)
	return nil
}

// markUndoCursor は、次の apply が作るアンドゥの単位に記録する、変更の前のカーソルの位置を p にする。
// Vim は、変更を始める前にカーソルを動かしてから単位を作る操作がある（dd は最初の空白でない文字へ、オペレーターは範囲の始まりへ）。
// その位置を、アンドゥとリドゥの後にカーソルを戻す先として記録する（actUndo）。
func (s *State) markUndoCursor(p buffer.Position) {
	s.undoAt, s.undoAtSet = p, true
}

// clearUndoCursor は、markUndoCursor で決めた位置を、apply が使わなかった場合も含めて捨てる。
// 変更しなかった操作（y など）の位置が、後の操作のアンドゥの単位に残らないようにする。
func (s *State) clearUndoCursor() {
	s.undoAtSet = false
}
