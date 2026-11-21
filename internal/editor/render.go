package editor

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// uiMode は、command のモードを描画のモードにする。
func uiMode(m command.Mode) ui.Mode {
	switch m {
	case command.ModeInsert:
		return ui.ModeInsert
	case command.ModeVisual:
		return ui.ModeVisual
	case command.ModeVisualLine:
		return ui.ModeVisualLine
	case command.ModeCommandLine:
		return ui.ModeCommandLine
	case command.ModeNormal:
	}
	return ui.ModeNormal
}

// frame は、カーソルのあるバッファの状態と表示のデータから、描画の入力を作る。
// コマンドラインの行には、確認の問い、選択肢の問い、メッセージの順に、最初にあるものを出す。
// 問いが無ければ、ポップアップを浮かべる。問いもポップアップもメッセージも無く、ノーマルモードで
// キーの列の途中でなければ、カーソルの行の診断の本文をメッセージの行に出す（cursorDiagnostic）。
// ただし、診断をポップアップに出す設定（diagnostic.popup）なら出さない（diagpopup.go。FR-171）。
//
// フォーカスのあるポップアップがあれば、バッファの表示領域には開いたときのタブ（tab）を描き、
// ステータス行、モード、コマンドラインは、キーを受けるポップアップのバッファ（cur）のものを出す。
// タブ文字の幅（ui.Frame.TabStop）は、描くたびに、表示領域に描くバッファの設定の今の値（tab.width）を読む（FR-161）。
//
// req: FR-161, FR-171
//
// 行番号の列（ui.Frame.LineNumbers）は詰めない。出すのは client の画面のバッファの表示領域だけなので、
// その画面を描く renderView の 1 か所で詰める（bufferFrame）。frame を元に作る部品の client の画面（popupComponentFrame）には、
// 消し忘れても行番号が載らない（PR #272 のレビューの必須 1）。
func (e *Editor) frame() *ui.Frame {
	bs := e.tab()
	cur := e.cur
	st := cur.state
	f := &ui.Frame{
		Text:             bs.buf,
		LineLayout:       e.lineLayout(bs, false), // 空白の記号を描くかは bufferFrame が決め直す
		Margins:          e.marginsFor(bs.state),
		Name:             bufferTitle(cur),
		Modified:         cur.buf.Modified(),
		ReadOnly:         cur.buf.ReadOnly(),
		FileType:         cur.fileType(),
		Mode:             uiMode(st.Mode()),
		Cursor:           bs.state.Cursor(),
		CommandLine:      st.CommandLine(),
		CommandLineType:  st.CommandLineType(),
		Completion:       e.wildMenuFrame(cur),
		CommandLinePopup: e.cmdlinePopup(),
		Recording:        st.Recording(),
		Message:          e.message,
		Decorations:      bs.decoView,
		Diagnostics:      bs.uiDiagnostics(),
		Signs:            bs.uiSigns(),
		Output:           e.uiOutput(),
		Tabs:             e.uiTabs(),
		Diff:             e.uiDiff(bs),
		ExtParts:         e.extParts,
	}
	f.HitEnter = e.hitEnter()
	f.OperatorPending = st.Mode() == command.ModeNormal && st.Pending()
	// ビジュアルモードから / か ? で打っている間も選択を出し、打つたびの検索の一致まで広げる（command.State.ShownSelection。FR-153）。
	if start, end, linewise, ok := st.ShownSelection(); ok && cur == bs {
		f.Selection = ui.Selection{Active: true, Start: start, End: end, Linewise: linewise}
	}
	// 問いは、問いを出した client の画面にだけ出す（clients.go の doc）。
	question := e.showsQuestion()
	switch {
	case question && e.confirm != nil:
		f.Message = ui.Message{Text: e.confirm.Prompt, Level: ui.MessageWarning}
	case question && e.prompt != nil:
		f.Message = ui.Message{Text: e.prompt.text, Level: ui.MessageWarning}
	case question && e.activeChoice() != nil:
		f.Choice = e.uiChoice()
		f.List = e.uiRegList()
	case !question && (e.asking() || e.activeChoice() != nil):
		// 問いの間は、このペインのキーを処理せずに待たせるので、その理由を出す（PR #223 のレビューの推奨 2）。
		q := e.asker
		if q == nil {
			q = e.op
		}
		f.Message = ui.Message{Text: fmt.Sprintf("client %d の問いの答えを待っています（答えるまで、このペインのキーは処理しません）", q.info.ID), Level: ui.MessageWarning}
	case e.regList != nil:
		f.List = e.uiRegList()
	case e.float != nil:
		if e.float.remote == nil {
			// 部品の client が描くポップアップ（端末多重化ソフトのポップアップの中。component.go）は、この画面には描かない。
			f.Float = e.floatFrame(e.float)
		}
	case f.Message.Text == "" && st.Mode() == command.ModeNormal && !st.Pending() && !e.wantsDiagnosticPopup():
		// 他に出すものが無ければ、カーソルの行の診断の本文を出す。
		// 診断をポップアップに出す設定（diagnostic.popup）なら、Neovim の open_float と同じく、メッセージの行には出さない（FR-171）。
		if m, ok := bs.cursorDiagnostic(toExtPos(st.Cursor())); ok {
			f.Message = m
		}
	}
	return f
}

// bufferFrame は、client の画面（バッファの表示領域のある画面）の描画の入力を作る。frame に行番号の列の出し方、検索の一致のパターン、
// 一時的な色付け（タブのバッファのものなら。flashHighlights。FR-140）を足したものである。
// 行番号を出すかは、この 1 か所で決める。部品の client の画面とポップアップの窓の中の Frame は、ここを通らないので行番号が載らない。
// / か ? でパターンを打っている間は、打つたびの検索で出す一致を足す（incSearchFrame。FR-153）。
//
// 行を折り返すか（ui.Frame.Wrap）も、描くたびに設定の今の値（lineWrap.enabled）から詰める（FR-156）。
// 行の文字の並べ方（ui.Frame.LineLayout。タブ文字の幅と、空白の記号を描くか）は、gj と gk と望みの列と同じ lineLayout で作る（FR-170）。
// 行番号の列と同じく、空白の記号を描くのは client の画面のバッファの表示領域だけである（ポップアップとプレビューの中には描かない）。
// 拡張機能の進み具合（uiProgress。FR-178）と、右端のスクロールバーと印（uiScrollbar。FR-186、FR-187）と、
// 起動の案内（uiIntro。FR-205、FR-207）も、この画面にだけ重ねる。
//
// req: FR-135, FR-140, FR-153, FR-156, FR-170, FR-178, FR-186, FR-205
func (e *Editor) bufferFrame() *ui.Frame {
	f := e.frame()
	f.LineNumbers = e.uiLineNumbers()
	f.Wrap = e.lineWrap()
	bs := e.tab()
	f.LineLayout = e.lineLayout(bs, true)
	f.Search = e.uiSearch()
	f.Highlights = e.flashHighlights(e.tab())
	f.Progress = e.uiProgress()
	f.Scrollbar = e.uiScrollbar(bs, f.Search)
	f.Intro = e.uiIntro()
	e.incSearchFrame(f)
	return f
}

// uiSearch は、検索の一致に色を付けるパターンを、エディタ全体の設定の今の値（search.highlight）と直前の検索のパターンから作る（FR-137）。
// 色を付けないとき（設定が false、:search.clearHighlight で消した、パターンが無い、パターンが正しくない）は nil を返す。
// 描くたびに読むので、:set hlsearch、/、*、:nohlsearch の後に呼ぶ処理は無い（基本設計 7.3）。
// 色を付けるのは、行番号の列と同じく client の画面のバッファの表示領域だけである（bufferFrame）。
//
// 直前の検索は client の間で共有する状態なので、最後にそれを持った State（e.shared）から読む。
// 別の client で検索した直後でも、この client の画面に同じパターンの色が付く（Vim で窓をまたいで同じ色が付くのと同じ）。
// この client で / か ? を打っている間は、直前の検索のパターンの代わりに、打つたびの検索のパターンを使う（incSearchPattern。FR-153）。
//
// req: FR-137, FR-138, FR-153
func (e *Editor) uiSearch() *regexp.Regexp {
	if !e.options.Get(option.SearchHighlight).Bool() {
		return nil
	}
	st := e.shared
	if st == nil && e.cur != nil {
		st = e.cur.state
	}
	if st == nil {
		return nil
	}
	pat := st.HighlightPattern(e.options.Revision(option.SearchHighlight))
	if p, ok := e.incSearchPattern(); ok {
		pat = p
	}
	if pat == "" {
		return nil
	}
	if c := &e.searchRegexp; c.pattern != pat {
		// 描くたびにコンパイルしないよう、直前のパターンの結果を覚えておく。正しくないパターンは nil を覚える。
		re, err := regexp.Compile(pat)
		if err != nil {
			re = nil
		}
		c.pattern, c.re = pat, re
	}
	return e.searchRegexp.re
}

// searchRegexp は、uiSearch がコンパイルした直前のパターンと、その結果である。
type searchRegexp struct {
	pattern string
	re      *regexp.Regexp
}

// uiLineNumbers は、行番号の列の出し方を、エディタ全体の設定の今の値（lineNumber.show と lineNumber.minWidth）から作る（FR-135、FR-136）。
// 描くたびに読むので、:set number などで値を変えたときに呼ぶ処理は無い（基本設計 7.3）。
// 使うのは bufferFrame だけである。
//
// req: FR-135
func (e *Editor) uiLineNumbers() ui.LineNumbers {
	return ui.LineNumbers{
		Show:     e.options.Get(option.LineNumberShow).Bool(),
		MinWidth: e.options.Get(option.LineNumberMinWidth).Int(),
	}
}

// margins は、カーソルの上下と左右に残す余白を、エディタ全体の設定の今の値（scroll.margin、scroll.sideMargin）から作る（FR-174）。
// 描くたびに読むので、:set scrolloff= などで値を変えたときに呼ぶ処理は無い（基本設計 7.3）。
// client の画面のバッファ（frame）と、ポップアップの中のバッファ（floatFrame）と、プレビューの中身（uiPreview）の Frame に詰める。
// ファインダーの一覧（fzf に当たる部品）には詰めない。スクロールの操作（internal/command）は、同じ設定を State の設定の口から読む。
//
// req: FR-174
func (e *Editor) margins() ui.Margins {
	return ui.Margins{
		Rows:    e.options.Get(option.ScrollMargin).Int(),
		Columns: e.options.Get(option.ScrollSideMargin).Int(),
	}
}

// marginsFor は、State st の表示領域に詰める余白を返す。マウスでドラッグして選んでいる間（command.State.MouseDragging）は、
// Neovim 0.12.5 と同じく上下の余白を残さない（ドラッグの位置が表示領域の端の行でも、表示領域を送らない。FR-183）。
//
// req: FR-174, FR-183
func (e *Editor) marginsFor(st *command.State) ui.Margins {
	m := e.margins()
	if st != nil && st.MouseDragging() {
		m.Rows = 0
	}
	return m
}

// lineWrap は、長い行を折り返して描くかを、エディタ全体の設定の今の値（lineWrap.enabled）から返す（FR-156）。
// 描くたびと、gj と gk で見た目の行を数えるたびに読むので、:set wrap などで値を変えたときに呼ぶ処理は無い（基本設計 7.3）。
// 使うのは、client の画面のバッファの表示領域（bufferFrame）と、ポップアップのプレビューの中身（floatFrame）と、screenLines である。
// ホバーとヘルプのポップアップは、窓の幅で折り返してからバッファにする（今のまま）ので、ここを読まない。
func (e *Editor) lineWrap() bool {
	return e.options.Get(option.LineWrapEnabled).Bool()
}

// lineLayout は、バッファ bs の行の文字の並べ方（ui.LineLayout）を、設定の今の値（tab.width と whitespace.show）から作る。
// onScreen は、client の画面のバッファの表示領域に描く（または、そこに描くバッファの列を数える）かである。
// 描画（frame、bufferFrame、ポップアップとプレビューの中身の Frame）、gj と gk（screenLines）、望みの列（wrapColumns）は、どれもこの 1 か所で作るので、
// 並べ方に項目を足しても、描き方と列の数え方が食い違わない（PR #334 のレビューの推奨 3 と再レビューの推奨）。
// 描くたびと、列を数えるたびに読むので、:set list や :set tabstop= で値を変えたときに呼ぶ処理は無い（基本設計 7.3）。
//
// 空白の記号を描くのは client の画面のバッファの表示領域（onScreen）だけで、ポップアップとプレビューの中のバッファには描かない（FR-170）。
// プレビューに開いているバッファを出すときも、そのバッファの State の表示領域でなく、描く先（プレビュー）で決める。
// 記号を描くときは、タブ文字の上のカーソルの列と望みの列が、タブの最初の列になる（ui.Columns の doc）。
//
// req: FR-161, FR-170
func (e *Editor) lineLayout(bs *bufferState, onScreen bool) ui.LineLayout {
	return ui.LineLayout{
		TabStop:    bs.tabWidth(),
		Whitespace: onScreen && e.options.Get(option.WhitespaceShow).Bool(),
	}
}

// onScreen は、State st の表示領域が client の画面のバッファの表示領域（viewport）かを返す（lineLayout の onScreen）。
// ポップアップ（floatViewport）とプレビュー（previewViewport）の中の State では false である。
func onScreen(st *command.State) bool {
	_, ok := st.Viewport.(viewport)
	return ok
}

// screenLines は、行を折り返して描くときの見た目の行の数え方（command.State.ScreenLines。gj と gk、ページ単位のスクロール）を、
// その State の表示領域（State.Viewport）を描く描画の部品につなぐ口である（FR-157）。
// client の画面のバッファ（viewport）と、ポップアップのプレビューの中身（previewViewport）は、設定（lineWrap.enabled）が true なら折り返して数える。
// ほかのポップアップの中のバッファ（floatViewport）は折り返して描かないので、折り返さないとして数える。
// 表示領域の幅は、その描画の部品の直前の描画の本文の幅で、まだ描いていない（幅が 0）なら折り返さないとして数える。
//
// 同じ口で、State の望みの列の数え方（command.State.Columns。wrapColumns）も、折り返して数えるときは描画と同じ列（ui.WrapLines）にする。
// Vim も 'wrap' のときは、j と k の望みの列（curswant）を、全角文字を送った ">" の列を含む列で数える。
//
// タブ文字の幅は、どちらの数え方も、State のバッファ（bs）の設定の今の値（tab.width。FR-161）で数える。描画（Frame.TabStop）と同じ値である。
type screenLines struct {
	e  *Editor
	bs *bufferState
	st *command.State
}

// lines は、折り返して数えるなら、その数え方を ok を true にして返す。
//
// req: FR-161
func (s screenLines) lines() (l ui.WrapLines, ok bool) {
	if !s.e.lineWrap() {
		return l, false
	}
	var r *ui.Renderer
	switch v := s.st.Viewport.(type) {
	case viewport:
		r = v.r
	case previewViewport:
		r = v.sub()
	}
	if r == nil || r.TextWidth() <= 0 {
		return l, false
	}
	return r.WrapLines(s.e.lineLayout(s.bs, onScreen(s.st))), true
}

// Wrap は、行を折り返して数えるかを返す。
func (s screenLines) Wrap() bool {
	_, ok := s.lines()
	return ok
}

// Width は、ui.WrapLines.Width である。
func (s screenLines) Width() int {
	l, _ := s.lines()
	return l.Width()
}

// BreakWidth は、ui.WrapLines.BreakWidth である。
func (s screenLines) BreakWidth() int {
	l, _ := s.lines()
	return l.BreakWidth()
}

// Rows は、ui.WrapLines.Rows である。
func (s screenLines) Rows(line string) int {
	l, _ := s.lines()
	return l.Rows(line)
}

// wrapColumns は、State の望みの列の数え方（command.State.Columns）である。行を折り返して数えるとき（screenLines）は
// 描画と同じ列（ui.WrapLines の Column と ByteAt）で、そうでなければ行の先頭からの表示の列（ui.Columns）で数える。
// どちらも、タブ文字の幅は State のバッファの今の tab.width で数える（FR-161）。
type wrapColumns struct {
	sl screenLines
}

// plain は、行を折り返さないときの数え方を返す。
func (c wrapColumns) plain() ui.Columns {
	cols := ui.Columns{LineLayout: c.sl.e.lineLayout(c.sl.bs, onScreen(c.sl.st))}
	if r := c.sl.e.renderer; r != nil {
		cols.Symbols = r.Symbols
	}
	return cols
}

// Column は、target.Columns の Column である。
//
// req: FR-161
func (c wrapColumns) Column(line string, byteOff int) int {
	if l, ok := c.sl.lines(); ok {
		return l.Column(line, byteOff)
	}
	return c.plain().Column(line, byteOff)
}

// ByteAt は、target.Columns の ByteAt である。
func (c wrapColumns) ByteAt(line string, col int) int {
	if l, ok := c.sl.lines(); ok {
		return l.ByteAt(line, col)
	}
	return c.plain().ByteAt(line, col)
}

// hitEnter は、改行を含むメッセージを出していて、Vim の hit-enter の問いと同じくキーを待っているかを返す。
// 待っている間は、メッセージの下に問いを出してカーソルをその末尾に置き（ui.Frame.HitEnter）、<CR> と <Space> はメッセージを消すだけにする（handleKey）。
// 確認の問いと選択肢の問いを出している間と、コマンドラインモード（コマンドラインの行にメッセージを出さない）では待たない。
// モードは問わない。インサートモードの間に拡張機能のメッセージが届いても待つ（Vim も、インサートモードの間に出た複数行のメッセージで hit-enter の問いを出す）。
func (e *Editor) hitEnter() bool {
	return !e.asking() && e.activeChoice() == nil && e.cur.state.Mode() != command.ModeCommandLine &&
		strings.Contains(strings.TrimRight(e.message.Text, "\n"), "\n")
}

// viewport は、描画の表示領域（ui.Renderer の縦のスクロールの位置）を、command.State のスクロールの操作
// （<C-e>、<C-d>、<C-f>、zt など。FR-173）へ渡す口である（command.Viewport）。
type viewport struct{ r *ui.Renderer }

// Top は、表示領域の最初の行を返す。
func (v viewport) Top() int { return v.r.Top() }

// Skip は、最初の行の上に隠している見た目の行の数を返す。
func (v viewport) Skip() int { return v.r.Skip() }

// Rows は、表示領域の行数を返す。直前の描画で、ステータス行とコマンドラインの行と出力の欄を除いた行数である。
// 画面の大きさがまだ届いていないとき（0 行）も、1 行として扱う。
func (v viewport) Rows() int { return max(v.r.TextRows(), 1) }

// SetView は、command.Viewport の SetView である。hold が負なら、次の描画でカーソルに合わせて決め直す（ui.Renderer.SetScrollHeld）。
func (v viewport) SetView(top, skip, hold int) { v.r.SetScrollHeld(top, skip, hold) }

// Left は、command.HorizontalViewport の Left である（ホイールの横のスクロール。FR-182）。
func (v viewport) Left() int { return v.r.Left() }

// Columns は、command.HorizontalViewport の Columns である。直前の描画の本文の列数である。
func (v viewport) Columns() int { return v.r.TextWidth() }

// SetLeft は、command.HorizontalViewport の SetLeft である。
func (v viewport) SetLeft(left int) { v.r.SetLeft(left) }

// render は、繋いでいるすべての client の画面を描いて、それぞれの端末へ出す。
// 描き終えたら、読み込む client を描く前のものに戻す。
// 見比べている組は、描く前に差分の計算し直しを始め、描いた後に両側のスクロールをそろえる（diff.go）。
func (e *Editor) render() {
	e.syncAsker()
	e.refreshDiffs()
	e.eachView(func(*clientView) {
		e.expireFloat()
		e.notifyPaneTabs()
		e.syncPreviews()
		// 打つたびの検索（FR-153）は、描く前に問いを読んで検索を始め、画面の位置を決め直す。
		e.syncIncSearch()
		e.renderView()
		e.renderComponents()
	})
	e.syncDiffScroll()
}

// renderView は、読み込んでいる client の画面を描いて端末へ出す。
func (e *Editor) renderView() {
	if e.cur == nil || e.quit || e.renderer.Width <= 0 || e.renderer.Height <= 0 {
		return
	}
	scr := e.renderer.Render(e.bufferFrame())
	// 端末にマウスの報告を求めるかは、描くたびに設定の今の値で決める（FR-180。:option.set の後の次の描画から効く）。
	scr.Mouse = e.mouseEnabled()
	if err := e.term.Draw(scr); err != nil {
		e.log.Error("描画に失敗", "err", err)
	}
}
