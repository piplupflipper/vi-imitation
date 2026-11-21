package editor

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/command/ex"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// コマンドラインの補完（基本設計 7.4。FR-195〜FR-200）。
//
// Ex コマンドのコマンドライン（":"）で利用者が打った <Tab> と <S-Tab> で、行末の語を候補で補い、候補をコマンドラインの真上の
// ポップアップの一覧（ui.Frame.Completion）に出す。どこから何を補うかは internal/command/ex の Complete が決め、
// 候補はここで集める（コマンドの名前、ファイル、バッファ、:help の語、:ext の引数）。補い方は Neovim 0.12.5 の
// command_line_wildchar_complete と nextwild（src/nvim/ex_getln.c、src/nvim/cmdexpand.c）に倣い、設定の
// commandLine.completeMode（Vim の 'wildmode'）の部分を <Tab> を打った回数で選ぶ。候補の選び方は completion.go の completionChoice である。
//
// 補ったテキストは、<BS> と文字のキーを State に渡して、コマンドラインの語を置き換える（wildSetWord）。
// このため、マクロを記録している間に補ったときは、補った後の文字が記録に入り、再生では同じコマンドラインになる
// （Vim は <Tab> そのものを記録し、再生の <Tab> は補わずにタブ文字を入れる。docs/vim-differences.md）。
// マクロとドットリピートの再生のキーは State の中で解釈するので、ここを通らず、<Tab> はこれまでどおりタブ文字として入る（Vim と同じ）。
// キーマップの展開で入った <Tab> は補う（Vim は 'wildcharm' に書いたキーでだけ補う）。
//
// 補う状態は client ごとに持つ（viewFields.wild）。キーを処理するたびと描くたびに、状態を作ったときのバッファとコマンドラインが
// 今のものと同じかを確かめ、違えば（実行した、<Esc> で抜けた、別のバッファへ移った）捨てる（currentWild）。
//
// ファイルの候補は、大きなディレクトリでも編集の全体を止めないよう、イベントループの外で集める（completion.go の completionJob。
// filecomplete.go）。集めている間は、その client のキーとキーマップの展開の残りを止めておき、集め終えて補った後に続けるので、
// <Tab> の直後に届いた <CR> も、補った後のコマンドラインを実行する。

// wildMenu は、1 つの client の、コマンドラインの補完の状態である（viewFields.wild）。Neovim の expand_T と
// CommandLineState の wim_index に当たる。候補と選んでいる候補は completionChoice に持つ。
type wildMenu struct {
	completionChoice
	// bs は、補っているコマンドラインを持つバッファである。
	bs *bufferState
	// line は、最後に補った後のコマンドラインである。今のコマンドラインと違えば、外で変わったので捨てる。
	line string
	// start は、補う語のコマンドラインの中のバイト位置である。語は start から行末までである。
	start int
	// index は、次の <Tab> で使う commandLine.completeMode の部分の番号である（Neovim の wim_index）。
	index int
	// menu は、候補の一覧を出しているかである。id は出している一覧の番号、menuStart は一覧の項目を並べ始める位置である。
	menu      bool
	id        uint64
	menuStart int
	// tail は、一覧に、ファイルのパスの最後の部分だけを出すかである（Neovim の cmd_showtail）。
	tail bool
	// pending は、候補をイベントループの外で集めている途中かである（completionJob）。
	pending bool
}

// wimFlags は、commandLine.completeMode の 1 つの部分である（Neovim の wim_flags の 1 つ）。
type wimFlags struct {
	full, longest, noselect bool
}

// wildFlags は、設定の commandLine.completeMode の今の値を、4 つの部分にして返す。書いた部分より後ろは、最後に書いた部分と同じにする
// （Vim 9.1 と Neovim 0.12.5 の check_opt_wim）。値は設定の検査（option.Def.Words）を通っているので、読めなければ full にする。
//
// req: FR-196
func (e *Editor) wildFlags() [4]wimFlags {
	var out [4]wimFlags
	parts, err := option.CompleteModeWords.Parse(e.options.Get(option.CommandLineCompleteMode).String())
	if err != nil {
		parts = [][]string{{"full"}}
	}
	for i := range out {
		if i >= len(parts) {
			if i > 0 {
				out[i] = out[i-1] //nolint:gosec // i は 1 から 3 で、out の長さは 4 である
			}
			continue
		}
		for _, w := range parts[i] {
			switch w {
			case "full":
				out[i].full = true
			case "longest":
				out[i].longest = true
			case "noselect":
				out[i].noselect = true
			}
		}
	}
	return out
}

// 補完で読むキー。
var (
	wildTabKey      = editcmd.Special(editcmd.CodeTab)
	wildShiftTabKey = editcmd.Key{Code: editcmd.CodeTab, Mod: editcmd.ModShift}
	wildNextKey     = editcmd.Key{Code: editcmd.CodeRune, Rune: 'n', Mod: editcmd.ModCtrl}
	wildPrevKey     = editcmd.Key{Code: editcmd.CodeRune, Rune: 'p', Mod: editcmd.ModCtrl}
	wildCancelKey   = editcmd.Key{Code: editcmd.CodeRune, Rune: 'e', Mod: editcmd.ModCtrl}
	wildAcceptKey   = editcmd.Key{Code: editcmd.CodeRune, Rune: 'y', Mod: editcmd.ModCtrl}
)

// currentWild は、読み込んでいる client の補う状態を、今のコマンドラインのものであれば返す。
// 状態を作ったバッファがカーソルのあるバッファでない、Ex コマンドのコマンドラインモードでない、コマンドラインが最後に補った後と違う
// ときは、状態を捨てて nil を返す。
func (e *Editor) currentWild() *wildMenu {
	w := e.wild
	if w == nil {
		return nil
	}
	st := w.bs.state
	if w.bs != e.cur || st.Mode() != command.ModeCommandLine || st.CommandLineType() != ':' || st.CommandLine() != w.line {
		e.endWild()
		return nil
	}
	return w
}

// endWild は、読み込んでいる client の補う状態を捨てる。候補を集めている途中なら取り消す
// （仕事は取り消しの後に終わり、止めていた入力をそこで続ける。completionJobDone）。
func (e *Editor) endWild() {
	if w := e.wild; w != nil && w.pending {
		if j := e.view.completionJob; j != nil {
			j.cancel()
		}
	}
	e.wild = nil
}

// wildKey は、利用者のキー k がコマンドラインの補完のキーなら処理して true を返す（feedKey が State へ渡す前に呼ぶ）。
// true を返したキーは State へ渡さない。補完のキーでなければ、補う状態を終えて false を返す（キーはふつうに処理される）。
//
//   - <Tab>: 補う。補うものが無いところ（ex.CompleteNothing）では false を返し、タブ文字が入る（Neovim と同じ）
//   - <S-Tab>: 候補が無ければ、補い始めて最後の候補を入れる。あれば <C-p> と同じ
//   - <C-n>、<C-p>: 候補があれば、次と前の候補を入れる
//   - 一覧を出している間の <Down>、<Right> と <Up>、<Left>: 次と前の候補（<Down> と <Up> はオーナーの nvim の割り当て。
//     <Left> と <Right> は Neovim の wildmenu_translate_key）。<PageDown> と <PageUp>: 一覧の高さだけ先と前の候補
//   - 一覧を出している間の <C-e>: 補う前の語に戻して一覧を閉じる。<C-y>: 入れている候補のまま一覧を閉じる
//   - 一覧を出している間のマウスの出来事: 何もしない（コマンドラインはマウスを読まない。一覧も閉じない）
//
// 候補を集めている間（wildMenu.pending）の client のキーは、ここへ届く前に止めている（completionJob）。
//
// req: FR-195, FR-198
func (e *Editor) wildKey(k editcmd.Key) bool {
	st := e.cur.state
	if st.Mode() != command.ModeCommandLine || st.CommandLineType() != ':' {
		if e.wild != nil {
			e.endWild()
		}
		return false
	}
	w := e.currentWild()
	if w != nil && w.pending {
		e.endWild()
		return false
	}
	switch {
	case k == wildTabKey:
		return e.wildTab(w)
	case k == wildShiftTabKey && (w == nil || len(w.items) == 0):
		return e.wildStart(true)
	case w == nil || len(w.items) == 0:
	case k == wildNextKey || (w.menu && (k == editcmd.Special(editcmd.CodeDown) || k == editcmd.Special(editcmd.CodeRight))):
		e.wildSelect(w, w.step(1))
		return true
	case k == wildPrevKey || k == wildShiftTabKey || (w.menu && (k == editcmd.Special(editcmd.CodeUp) || k == editcmd.Special(editcmd.CodeLeft))):
		e.wildSelect(w, w.step(-1))
		return true
	case w.menu && (k == editcmd.Special(editcmd.CodePageDown) || k == editcmd.Special(editcmd.CodePageUp)):
		e.wildSelect(w, w.page(k.Code == editcmd.CodePageDown, e.renderer.CompletionRows()))
		return true
	case w.menu && k == wildCancelKey:
		e.wildSetWord(w, w.orig)
		e.endWild()
		return true
	case w.menu && k == wildAcceptKey:
		e.endWild()
		return true
	case w.menu && k.IsMouse():
		return true
	}
	if w != nil {
		e.endWild()
	}
	return false
}

// wildTab は、<Tab> を処理する。候補を集めていれば（2 回目からの <Tab>）、今の部分の補い方で補う。
// 集めていなければ補い始める（wildStart）。Neovim の command_line_wildchar_complete の、xp_numfiles > 0 の側である。
//
// req: FR-195, FR-196
func (e *Editor) wildTab(w *wildMenu) bool {
	if w == nil || len(w.items) == 0 {
		return e.wildStart(false)
	}
	f := e.wildFlags()[w.index]
	switch {
	case f.longest:
		e.wildLongest(w)
	case f.full:
		e.wildSelect(w, w.step(1))
	}
	if w.index < 3 {
		w.index++
	}
	return true
}

// wildStart は、行末の語を補い始める。reverse は <S-Tab> で始めたかである。補うものが無ければ false を返す。
// ファイルの候補はイベントループの外で集め、集め終えてから補う（startWildFiles）。それ以外はその場で集めて補う。
func (e *Editor) wildStart(reverse bool) bool {
	bs := e.cur
	line := bs.state.CommandLine()
	c := ex.Complete(&bs.state.Ex, line)
	w := &wildMenu{bs: bs, line: line, start: c.Start}
	w.orig, w.selected = line[c.Start:], -1
	switch c.Kind {
	case ex.CompleteNothing:
		return false
	case ex.CompleteFile:
		e.endWild()
		e.wild = w
		e.startWildFiles(w, reverse)
		return true
	case ex.CompleteCommand:
		w.items = withPrefix(e.completionCommandNames(), w.orig)
	case ex.CompleteList:
		w.items = c.Candidates
	case ex.CompleteBuffer:
		w.items = e.completionBuffers(ex.UnescapeBufferArg(w.orig))
	case ex.CompleteExtCommand:
		items, ok := e.completionExtArgs(c.Command, w.orig)
		if !ok {
			return false
		}
		w.items = items
	}
	w.shown = w.items
	e.endWild()
	e.wild = w
	e.wildBegin(w, reverse)
	return true
}

// startWildFiles は、ファイルの候補を集める仕事を始める（FR-199）。集め終えたら補う（completionJob の done）。
func (e *Editor) startWildFiles(w *wildMenu, reverse bool) {
	w.tail = strings.Contains(w.orig, "/")
	w.pending = true
	q := fileQuery{
		word: w.orig, workDir: e.workDir(), env: e.fileEnvSnapshot(),
		ignoreCase: e.options.Get(option.CommandLineCompleteIgnoreCase).Bool(),
	}
	collect := e.wildCollect
	if collect == nil {
		collect = collectFiles
	}
	e.startCompletionJob(
		func(ctx context.Context) ([]string, []string, error) { return collect(ctx, q) },
		func(items, shown []string, err error) {
			w.pending = false
			if e.currentWild() != w {
				return
			}
			if err != nil {
				// 集められなかった（ディレクトリが無い、読めない、<C-c> で取り消した）。Neovim と同じく、何も入れずに終える。
				e.endWild()
				return
			}
			w.items, w.shown = items, shown
			e.wildBegin(w, reverse)
		})
}

// wildBegin は、集めた候補で、補い始めの <Tab>（reverse なら <S-Tab>）を処理する。
// Neovim の command_line_wildchar_complete の、初めて <Tab> を打った側（xp_numfiles が -1 の側）と、<S-Tab> の側である。
//
//   - 部分に longest があれば、候補の共通の先頭を入れる。今の語より短くなるなら入れない（nextwild の WILD_LONGEST）。
//     共通の先頭は大文字と小文字を区別して数える（Neovim は 'fileignorecase' のときだけ区別しない）
//   - longest が無く noselect があれば、何も入れずに一覧を出す。どちらも無ければ最初の候補を入れる
//   - 候補が 1 つだけなら、入れて補う状態を終える（次の <Tab> はその後から補い始める。:e dir/ の後の <Tab> は dir の中を補う）
//   - 候補が 2 つ以上なら、部分の full か noselect で一覧を出す。longest で語が伸びなかったときは、次の部分の full で最初の候補を入れる
//
// req: FR-195, FR-196
func (e *Editor) wildBegin(w *wildMenu, reverse bool) {
	n := len(w.items)
	if n == 0 {
		// 候補が無い（Neovim は音を鳴らす）。打った語のまま、補う状態を終える。
		e.endWild()
		return
	}
	flags := e.wildFlags()
	if reverse {
		// <S-Tab>: 最初の候補を入れてから一覧を出し、2 回戻して最後の候補にする（Neovim の "<S-Tab> goes to last match"）。
		e.wildSetWord(w, w.items[0])
		w.selected = 0
		if n == 1 {
			e.endWild()
			return
		}
		e.wildShowMenu(w, flags[0].noselect)
		e.wildSelect(w, w.step(-1))
		e.wildSelect(w, w.step(-1))
		return
	}
	f0 := flags[0]
	before := len(w.bs.state.CommandLine())
	switch {
	case f0.longest:
		e.wildLongest(w)
	case f0.noselect:
	default:
		e.wildSetWord(w, w.items[0])
		w.selected = 0
	}
	if n == 1 && (f0.longest || !f0.noselect) {
		e.endWild()
		return
	}
	if n > 1 || f0.noselect {
		switch {
		case f0.longest && f0.full:
			e.wildShowMenu(w, true)
		case f0.longest && len(w.bs.state.CommandLine()) == before:
			if f1 := flags[1]; f1.full && !f1.noselect {
				e.wildSelect(w, w.step(1))
			} else if f1.full || f1.noselect {
				e.wildShowMenu(w, true)
			}
		case !f0.longest && (f0.full || f0.noselect):
			e.wildShowMenu(w, f0.noselect)
		}
	}
	if w.index < 3 {
		w.index++
	}
}

// wildLongest は、候補の共通の先頭を入れ、候補を選んでいない状態にする（Neovim の find_longest_match）。
// 共通の先頭が今の語より短ければ入れない（Neovim の nextwild の WILD_LONGEST）。
func (e *Editor) wildLongest(w *wildMenu) {
	p := longestCommonPrefix(w.items)
	w.selected = -1
	if len(p) >= len(w.bs.state.CommandLine())-w.start {
		e.wildSetWord(w, p)
	}
}

// wildSelect は、i 番目の候補（-1 なら打った語）をコマンドラインに入れる。一覧を出していなければ、入れる前に出す
// （Neovim の get_next_or_prev_match）。
//
// req: FR-198
func (e *Editor) wildSelect(w *wildMenu, i int) {
	if !w.menu {
		e.wildShowMenu(w, false)
	}
	w.selected = i
	e.wildSetWord(w, w.text(i))
}

// wildShowMenu は、候補の一覧を出す。一覧の項目は、ファイルの候補なら今のコマンドラインの語のパスの最後の部分（pathTail）から、
// それ以外は語の始まりから並べる（Neovim の cmdline_pum_create の compl_startcol）。noselect なら、何も選んでいない一覧にする。
func (e *Editor) wildShowMenu(w *wildMenu, noselect bool) {
	w.menu = true
	e.wildMenuID++
	w.id = e.wildMenuID
	w.menuStart = w.start
	if w.tail {
		w.menuStart += pathTail(w.bs.state.CommandLine()[w.start:], noselect)
	}
	if noselect {
		w.selected = -1
	}
}

// pathTail は、パス s の最後の部分の始まりのバイト位置を返す（Neovim の showmatches_gettail）。
// eager なら最後の "/" の後である。eager でなければ、後に "/" でない文字が続く最後の "/" の後で（"a/b/" は "b/" の始まり）、
// そのような "/" が無ければ 0 である。Neovim は、選んでいない一覧を出すときだけ eager で数える。
func pathTail(s string, eager bool) int {
	t, sep := 0, false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '/' && eager:
			t = i + 1
		case s[i] == '/':
			sep = true
		case sep:
			t, sep = i, false
		}
	}
	return t
}

// wildSetWord は、コマンドラインの補う語（start から行末）を text に置き換え、置き換えた後のコマンドラインを覚える。
// 語と text の共通の先頭は残し、残りを <BS> で消して、text の残りを文字のキーで入れる（マクロの記録に入れるため。ファイルの doc）。
func (e *Editor) wildSetWord(w *wildMenu, text string) {
	st := w.bs.state
	word := []rune(st.CommandLine()[w.start:])
	want := []rune(text)
	n := 0
	for n < len(word) && n < len(want) && word[n] == want[n] {
		n++
	}
	feed := st.Feed
	if e.noRecord {
		feed = st.FeedUnrecorded
	}
	for range len(word) - n {
		feed(editcmd.Special(editcmd.CodeBackspace))
	}
	for _, r := range want[n:] {
		feed(editcmd.Rune(r))
	}
	w.line = st.CommandLine()
}

// wildMenuFrame は、読み込んでいる client の、バッファ bs のコマンドラインの候補の一覧の描画の入力を返す。
// 一覧を出していなければ nil を返す。一覧の高さの上限は、描くたびに設定の completionMenu.height の今の値を読む（FR-197）。
//
// req: FR-197
func (e *Editor) wildMenuFrame(bs *bufferState) *ui.CompletionMenu {
	w := e.wild
	if w == nil || !w.menu || w.bs != bs || w.bs.state.Mode() != command.ModeCommandLine || w.bs.state.CommandLine() != w.line {
		return nil
	}
	return &ui.CompletionMenu{
		ID: w.id, Items: w.shown, Selected: w.selected, Start: w.menuStart, Match: w.orig,
		MaxHeight: e.options.Get(option.CompletionMenuHeight).Int(),
	}
}

// cmdlinePopup は、コマンドラインと検索の入力を画面の上の中央のポップアップに描くか（ui.Frame.CommandLinePopup）を、
// 描くたびに設定の commandLine.popup の今の値から読む（FR-215）。client の画面（frame）と、ペインの部品の client の画面
// （paneComponentFrame）の両方に詰める。ポップアップの部品の client の画面（popupComponentFrame）は frame を元に作るので、同じ値になる。
// 補完の候補の一覧の中身（wildMenuFrame）は、どちらに描くときも同じものを渡し、描く場所は ui が決める（FR-217）。
//
// req: FR-215
func (e *Editor) cmdlinePopup() bool {
	return e.options.Get(option.CommandLinePopup).Bool()
}

// withPrefix は、words のうち prefix で始まるものを、元の順に返す。
func withPrefix(words []string, prefix string) []string {
	var out []string
	for _, w := range words {
		if strings.HasPrefix(w, prefix) {
			out = append(out, w)
		}
	}
	return out
}

// completionCommandNames は、コマンドの名前の補完の候補（FR-195）を、名前の順に返す。本体の Ex コマンドと別名の省略しない名前
// （ex.Names）と、拡張機能のコマンドの口から呼ぶコマンドの名前（exCommands。拡張機能のコマンド、provides の操作の名前、:help、:keys、
// :ext、:messages、:intro）である。本体が同じコマンドのために足した短い名前（:h、:mes から :message まで、:int と :intr）は出さない
// （Vim の補完も省略形を出さない）。
//
// req: FR-195
func (e *Editor) completionCommandNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, n := range ex.Names(e.aliases) {
		add(n.Full)
	}
	for name, id := range e.exCommands {
		switch {
		case id == helpExtension && name != keysCommandName && name != helpCommandNames[0]:
		case id == messagesExtension && name != messagesCommandNames[len(messagesCommandNames)-1]:
		case id == introExtension && name != introCommandNames[len(introCommandNames)-1]:
		default:
			add(name)
		}
	}
	slices.Sort(out)
	return out
}

// completionBuffers は、バッファの名前の補完の候補（FR-195）を、コマンドラインに書く形（ex.EscapeBufferArg）で返す。
// 名前は :ls の名前（bufferName）で、ファイルの無いバッファとポップアップのバッファは出さない。
// 名前かファイルの名前が pat で始まるバッファを番号の順に並べ、その後に名前が pat を含むバッファを番号の順に並べる
// （:b の名前の引き方 bufferByName と同じ段の順）。
//
// req: FR-195
func (e *Editor) completionBuffers(pat string) []string {
	var head, rest []string
	for _, bs := range e.bufs {
		if bs.popup || bs.path == "" {
			continue
		}
		name := e.bufferName(bs)
		switch {
		case strings.HasPrefix(name, pat) || strings.HasPrefix(filepath.Base(bs.path), pat):
			head = append(head, ex.EscapeBufferArg(name))
		case strings.Contains(name, pat):
			rest = append(rest, ex.EscapeBufferArg(name))
		}
	}
	return append(head, rest...)
}

// helpCompletionLimit は、:help の語の候補の数の上限である（Neovim 0.12.5 の :help の補完の上限の 300 と同じ）。
const helpCompletionLimit = 300

// completionExtArgs は、拡張機能のコマンドの口から呼ぶコマンド id の引数の、語 word の候補を返す。
// 本体の :help と :h は :help で引ける語（listHelpWords。語で始まるものを名前の順に、その後に語を大文字と小文字を区別せずに含むものを
// 名前の順に、300 個まで）、:ext は sync と update である。ほかのコマンド（拡張機能のコマンドと provides の操作）は、
// 拡張機能に補わせる口が無いので補わず、ok を false で返す（<Tab> はタブ文字として入る）。
//
// req: FR-195
func (e *Editor) completionExtArgs(id, word string) (items []string, ok bool) {
	switch e.exCommands[id] {
	case helpExtension:
		if !slices.Contains(helpCommandNames, id) {
			return nil, false
		}
		var head, rest []string
		lw := strings.ToLower(word)
		for _, w := range e.helpWords() {
			switch {
			case strings.HasPrefix(w, word):
				head = append(head, w)
			case strings.Contains(strings.ToLower(w), lw):
				rest = append(rest, w)
			}
		}
		slices.Sort(head)
		slices.Sort(rest)
		items = slices.Concat(head, rest)
		return items[:min(len(items), helpCompletionLimit)], true
	case extManagerExtension:
		return withPrefix([]string{"sync", "update"}, word), true
	}
	return nil, false
}

// helpWords は、:help で引ける語の一覧（listHelpWords の語）を返す。語は起動した後に変わらないので、最初に求めたものを使い回す。
func (e *Editor) helpWords() []string {
	if e.helpWordCache == nil {
		ws := e.listHelpWords()
		e.helpWordCache = make([]string, len(ws))
		for i, w := range ws {
			e.helpWordCache[i] = w.Word
		}
	}
	return e.helpWordCache
}
