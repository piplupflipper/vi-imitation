package editor

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 書き足すテキストと入力の行の組（ext.ViewConsole）の入力の行の補完（FR-297。基本設計 1.4 の「#427 の入力の行の補完」）。
//
// 部品を開くときに ext.ViewComponent.Complete を真にした組（consoleState.complete）では、インサートモードで入力の行の中身が
// 利用者のキーで変わるたびと、補完のキー（一覧を出していないときの <Tab>、<C-n>、<C-p>、<C-Space>）で、持ち主の拡張機能へ
// console/complete を問い合わせる（requestCompletion）。問い合わせは exthost.Host.Send で送り、答えを待たずにキーの処理を続ける
// （ファインダーの一覧と同じく、FR-17 の期限で待たない）。答えは exthost.QueryResult のイベントで届き（consoleCompleted）、
// 最後に送った問い合わせの答えで、送ったときと入力の行の中身とカーソルが同じなら、補完の候補の一覧（ui.CompletionMenu）に出す。
// 次の問い合わせを送るときと、一覧を閉じるときは、答えを待っている前の問い合わせを取り消す（$/cancel）。
//
// 一覧の出し方とキーは、オーナーの nvim の blink.cmp の設定（~/.config/nvim/lua/plugins/blink.lua。preset の enter、preselect と
// auto_insert を切る）に合わせた。本体にはインサートモードの補完とその設定がまだ無い（基本設計 6.3 と 10 章の未決）ためである。
//
//   - 一覧は、打つたびに自動で出す。カーソルの前が空白か、入力の行の先頭なら出さない（閉じる）
//   - 候補を前もって選ばない。選ぶまで入力の行の中身を変えない
//   - <C-n>、<Tab>、<Down> で次の、<C-p>、<S-Tab>、<Up> で前の候補を選ぶ（端の先は選んでいない状態へ戻る）
//   - <CR> と <C-y> は、選んだ候補を入れる。選んでいなければ一覧を閉じ、<CR> は今までどおり入力の行の中身を送る
//   - <C-e> は一覧を閉じる。<Esc> は一覧を閉じてノーマルモードへ戻る
//
// 答えが届くまでに打ち足した文字は、前の答えの候補を、置き換える範囲の始まりからカーソルまでの文字で絞り込んで出し続け
// （打つたびに一覧が消えて出直さないため）、次の答えで置き換える。
//
// マクロとドットリピートの再生では、問い合わせず、一覧を出さない（答えが届く時点が記録したときと違い、結果が決まらないため）。
// このため、記録したときに一覧で候補を選んだキーは、再生では一覧の無いときのキー（<Down> は履歴、<CR> は送信）になる。

// consoleCompletion は、書き足すテキストと入力の行の組の補完の状態である（consoleState.comp）。
type consoleCompletion struct {
	// seq は、最後に送った問い合わせの番号（ext.ConsoleCompleteParams.Seq）である。
	seq uint64
	// pending は、答えを待っている問い合わせの番号である。0 なら待っていない。query はその問い合わせである。
	pending uint64
	query   *exthost.Query
	// asked と askedAt は、待っている問い合わせで送った入力の行の中身とカーソルである。
	asked   string
	askedAt int
	// items は、出している答えの候補である。base と baseAt は、その答えを求めた入力の行の中身とカーソルで、候補の範囲はここを指す。
	items  []ext.ConsoleCompletionItem
	base   string
	baseAt int
	// shown は、今の入力の行の中身（text と at）に合わせて絞り込んだ候補の、items の添字である。
	shown    []int
	text     string
	at       int
	selected int
	// menuID は、一覧を見分ける番号（ui.CompletionMenu.ID）である。答えが替わるたびに替える。
	menuID uint64
	// last と lastAt は、キーの処理の後に最後に見た入力の行の中身とカーソルである（completeAfterKey）。
	last   string
	lastAt int
}

// 入力の行の補完のキー（blink.cmp の preset の enter と、オーナーの設定の <Tab>、<S-Tab>）。
var (
	completeNextKeys   = editcmd.MustParseKeys("<C-n><Tab><Down>")
	completePrevKeys   = editcmd.MustParseKeys("<C-p><S-Tab><Up>")
	completeAcceptKeys = editcmd.MustParseKeys("<C-y><CR>")
	completeCloseKey   = editcmd.MustParseKeys("<C-e>")[0]
	// completeStartKeys は、一覧を出していないときに問い合わせを始めるキーである。
	completeStartKeys = editcmd.MustParseKeys("<C-n><C-p><Tab><S-Tab><C-Space>")
)

// completeKey は、補完を引き受けた組 f の入力の行のインサートモードのキー k を、補完の操作として扱えれば扱い、true を返す（FR-297）。
// 一覧を出していれば、候補を選ぶキー、入れるキー、閉じるキーを扱う。選んでいないときの <CR> は、一覧を閉じて false を返す
// （呼び出し側が中身を送る）。一覧を出していなければ、補完のキーで問い合わせる。再生中は問い合わせず、補完のキーを捨てる。
//
// req: FR-297
func (e *Editor) completeKey(f *floatState, k editcmd.Key) bool {
	con := f.con
	c := &con.comp
	if f.bs.state.Replaying() {
		return slices.Contains(completeStartKeys, k)
	}
	if e.consoleMenuShown(f) {
		switch {
		case slices.Contains(completeNextKeys, k):
			c.selected = c.choice().step(1)
			return true
		case slices.Contains(completePrevKeys, k):
			c.selected = c.choice().step(-1)
			return true
		case slices.Contains(completeAcceptKeys, k):
			if c.selected >= 0 {
				e.acceptCompletion(f)
				return true
			}
			e.cancelCompletion(con)
			return k != consoleEnter
		case k == completeCloseKey:
			e.cancelCompletion(con)
			return true
		}
		return false
	}
	if slices.Contains(completeStartKeys, k) {
		e.requestCompletion(f)
		return true
	}
	return false
}

// choice は、出している候補と選んでいる候補を、コマンドラインの補完と同じ選び方（completionChoice）の形にして返す。
// 最後の候補の次と最初の候補の前は、選んでいない状態（-1）へ戻り、その次はもう一方の端へ回る（Neovim の補完の一覧と同じ）。
func (c *consoleCompletion) choice() *completionChoice {
	items := make([]string, len(c.shown))
	for i, j := range c.shown {
		items[i] = c.items[j].Insert()
	}
	return &completionChoice{items: items, shown: items, orig: c.text, selected: c.selected}
}

// requestCompletion は、組 f の入力の行の今の中身とカーソルで、持ち主の拡張機能へ console/complete を問い合わせる（FR-297）。
// 答えを待っている前の問い合わせは取り消す。出している一覧は、答えが届くまで出し続ける（打ち足した文字で絞り込む。refreshCompletion）。
//
// req: FR-297
func (e *Editor) requestCompletion(f *floatState) {
	con := f.con
	c := &con.comp
	if c.query != nil {
		c.query.Cancel()
		c.query = nil
	}
	c.pending = 0
	if f.ext == "" {
		return
	}
	text, at := f.bs.buf.Line(0), min(f.bs.state.Cursor().Byte, len(f.bs.buf.Line(0)))
	c.seq++
	p := ext.ConsoleCompleteParams{View: ext.ViewID(f.id), Child: f.child, Text: text, Cursor: at, Seq: c.seq}
	q, err := e.host.Send(f.ext, ext.MethodConsoleComplete, p, exthost.SendOptions{Tag: completeTag{con: con, seq: c.seq}})
	if err != nil {
		e.log.Warn("console/complete を送れない", "extension", f.ext, "err", err)
		return
	}
	c.pending, c.query, c.asked, c.askedAt = c.seq, q, text, at
}

// completeTag は、console/complete の問い合わせの exthost.SendOptions.Tag である。
type completeTag struct {
	con *consoleState
	seq uint64
}

// cancelCompletion は、組の補完の一覧を閉じ、答えを待っている問い合わせを取り消す。
func (e *Editor) cancelCompletion(con *consoleState) {
	c := &con.comp
	if c.query != nil {
		c.query.Cancel()
	}
	c.query, c.pending = nil, 0
	c.items, c.shown, c.selected = nil, nil, -1
}

// consoleCompleted は、console/complete の答え r を、問い合わせた組（t.con）に当てる（FR-297）。
// その組がもう無いか、最後に送った問い合わせの答えでなければ捨てる。失敗した答え（誤り、OK が false）なら一覧を閉じる。
// 送った後に入力の行の中身かカーソルが変わっていれば捨てる（変わるたびに次を送っているので、その答えを待つ）。
// ext.ConsoleCompletionItem.Validate を通らない候補は、ログに残して捨てる。
//
// req: FR-297
func (e *Editor) consoleCompleted(t completeTag, r *exthost.QueryResult) {
	f := e.findConsole(t.con)
	if f == nil {
		return
	}
	con := t.con
	c := &con.comp
	if c.pending == 0 || t.seq != c.pending {
		return
	}
	c.query, c.pending = nil, 0
	var res ext.ConsoleCompleteResult
	if err := r.Decode(&res); err != nil || !res.OK {
		if err != nil {
			e.log.Warn("console/complete が失敗した", "extension", f.ext, "err", err)
		}
		c.items, c.shown, c.selected = nil, nil, -1
		return
	}
	ct := f.contentOf(con)
	text, at := ct.bs.buf.Line(0), min(ct.bs.state.Cursor().Byte, len(ct.bs.buf.Line(0)))
	if text != c.asked || at != c.askedAt {
		return
	}
	items := make([]ext.ConsoleCompletionItem, 0, len(res.Items))
	for _, it := range res.Items {
		if err := it.Validate(text, at); err != nil {
			e.log.Warn("console/complete の候補を捨てた", "extension", f.ext, "label", it.Label, "err", err)
			continue
		}
		items = append(items, it)
	}
	c.items, c.base, c.baseAt = items, text, at
	c.shown, c.selected = nil, -1
	c.menuID++
	refreshCompletion(con, text, at)
}

// findConsole は、組の状態 con を中身に持つ部品（ポップアップかペイン。切り替えの部品の見えていない子を含む）を返す。無ければ nil である。
func (e *Editor) findConsole(con *consoleState) *floatState {
	for _, cv := range e.clients {
		if cv.detached {
			continue
		}
		v := e.viewOf(cv)
		for _, f := range append([]*floatState{v.float}, v.panes...) {
			if f != nil && f.contentOf(con) != nil {
				return f
			}
		}
	}
	return nil
}

// contentOf は、部品 f の中身のうち、組の状態が con のものを返す。無ければ nil である。
func (f *floatState) contentOf(con *consoleState) *floatContent {
	for _, ct := range f.contents() {
		if ct.con == con {
			return ct
		}
	}
	return nil
}

// refreshCompletion は、組の補完の一覧を、入力の行の今の中身 text とカーソル at に合わせて絞り込む。
// 答えを求めたときと同じなら、すべての候補を出す。答えを求めた後にカーソルの位置へ文字を打ち足しただけなら、置き換える範囲の始まりから
// カーソルまでの文字で始まる候補（大文字と小文字を区別しない）だけを出す。ほかの変わり方なら、一覧を閉じる。
// 出す候補が替わったら、選んでいない状態に戻す。
func refreshCompletion(con *consoleState, text string, at int) {
	c := &con.comp
	var shown []int
	switch {
	case len(c.items) == 0:
	case text == c.base && at == c.baseAt:
		for i := range c.items {
			shown = append(shown, i)
		}
	case typedAfter(c.base, c.baseAt, text, at):
		for i, it := range c.items {
			if prefix := text[it.Start:at]; hasPrefixFold(it.Insert(), prefix) {
				shown = append(shown, i)
			}
		}
	}
	if !slices.Equal(shown, c.shown) {
		c.selected = -1
	}
	c.shown, c.text, c.at = shown, text, at
}

// typedAfter は、入力の行の中身 text とカーソル at が、中身 base のカーソル baseAt の位置へ文字を打ち足しただけのものかを返す。
func typedAfter(base string, baseAt int, text string, at int) bool {
	n := at - baseAt
	return n > 0 && len(text) == len(base)+n && text[:baseAt] == base[:baseAt] && text[at:] == base[baseAt:]
}

// hasPrefixFold は、s が prefix で始まるかを、大文字と小文字を区別せずに返す。
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// consoleMenuShown は、組 f の入力の行の補完の一覧を出しているかを返す。フォーカスが入力の行にあり、インサートモードで、
// 絞り込んだ候補が 1 つ以上あり、それが今の入力の行の中身とカーソルに合わせたものであるときだけ出す。
func (e *Editor) consoleMenuShown(f *floatState) bool {
	con := f.con
	if con == nil || !con.complete || !f.focused || con.outFocus || len(con.comp.shown) == 0 {
		return false
	}
	st := f.bs.state
	if st.Mode() != command.ModeInsert || st.Replaying() {
		return false
	}
	return con.comp.text == f.bs.buf.Line(0) && con.comp.at == st.Cursor().Byte
}

// acceptCompletion は、組 f の補完の一覧で選んでいる候補を入力の行に入れ、一覧を閉じる（FR-297）。
// 候補の範囲（答えを求めたときの中身の中の位置）を今の中身の位置へ移し（打ち足した文字の分だけ終わりをずらす）、その範囲を
// 候補の入れる文字列で置き換えて、カーソルを入れた文字列の後ろに置く。入れる文字列の改行は空白に替える（consoleInputText）。
// 入れた後の中身では問い合わせない（入れたことで一覧が出直さないため。blink.cmp と同じ）。
//
// req: FR-297
func (e *Editor) acceptCompletion(f *floatState) {
	con := f.con
	c := &con.comp
	it := c.items[c.shown[c.selected]]
	text, at := c.text, c.at
	start, end := it.Start, it.End
	if end >= c.baseAt {
		end += at - c.baseAt
	}
	ins := consoleInputText(it.Insert())
	next := text[:start] + ins + text[min(end, len(text)):]
	e.cancelCompletion(con)
	e.reload(f.bs, buffer.New(next))
	cursor := start + len(ins)
	f.bs.state.SetCursor(buffer.Position{Byte: cursor})
	c.last, c.lastAt = next, cursor
}

// completeAfterKey は、補完を引き受けた組 f で、キーの処理の後に、入力の行の中身かカーソルが変わっていれば補完を決め直す（FR-297）。
// インサートモードでなくなったら一覧を閉じる。インサートモードで、中身が変わり、カーソルの前が空白でも入力の行の先頭でもなければ
// 問い合わせる。中身が変わらずにカーソルだけが動いたときと、カーソルの前が空白か先頭なら、一覧を閉じる。
// 再生中は問い合わせない（completeKey）。履歴をたどった中身と、送った後の空の中身では問い合わせない（setConsoleInput が覚える）。
//
// req: FR-297
func (e *Editor) completeAfterKey(f *floatState) {
	con := f.con
	if con == nil || !con.complete {
		return
	}
	c := &con.comp
	st := f.bs.state
	text, at := f.bs.buf.Line(0), min(st.Cursor().Byte, len(f.bs.buf.Line(0)))
	if st.Mode() != command.ModeInsert || con.outFocus || st.Replaying() {
		if c.pending != 0 || len(c.items) > 0 {
			e.cancelCompletion(con)
		}
		c.last, c.lastAt = text, at
		return
	}
	if text == c.last && at == c.lastAt {
		return
	}
	changed := text != c.last
	c.last, c.lastAt = text, at
	if !changed || at == 0 || unicode.IsSpace(lastRune(text[:at])) {
		e.cancelCompletion(con)
		return
	}
	refreshCompletion(con, text, at)
	e.requestCompletion(f)
}

// lastRune は、s の最後の文字を返す。s が空なら utf8.RuneError を返す。
func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

// noteConsoleInput は、本体が組の入力の行の中身を替えた（履歴をたどった、送って空にした）ことを補完に知らせる。
// 一覧を閉じ、その中身を最後に見た中身として覚えて、次のキーの処理の後に問い合わせないようにする。
func (e *Editor) noteConsoleInput(con *consoleState, text string) {
	if !con.complete {
		return
	}
	e.cancelCompletion(con)
	con.comp.last, con.comp.lastAt = text, len(text)
}

// uiConsoleMenu は、組 f の入力の行の補完の一覧の描画の入力を作る。出していなければ nil である。
func (e *Editor) uiConsoleMenu(f *floatState) *ui.CompletionMenu {
	if !e.consoleMenuShown(f) {
		return nil
	}
	c := &f.con.comp
	m := &ui.CompletionMenu{
		ID:        f.id<<32 | c.menuID,
		Selected:  c.selected,
		Start:     c.at,
		MaxHeight: e.options.Get(option.CompletionMenuHeight).Int(),
	}
	hasKind := false
	for _, i := range c.shown {
		it := c.items[i]
		m.Items = append(m.Items, it.Label)
		m.Kinds = append(m.Kinds, it.Kind)
		m.Start = min(m.Start, it.Start)
		hasKind = hasKind || it.Kind != ""
	}
	if !hasKind {
		m.Kinds = nil
	}
	if len(c.shown) > 0 {
		// 打った語は、最初の候補の置き換える範囲の始まりからカーソルまでの文字である（その先頭の候補の文字を色で示す）。
		m.Match = c.text[c.items[c.shown[0]].Start:c.at]
	}
	return m
}
