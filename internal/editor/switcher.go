package editor

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/input"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 切り替えの部品（ext.ViewSwitcher。基本設計 1.4 の「#399 の切り替えの部品と書き足し」の「切り替えの部品」。FR-269〜FR-271、FR-301）。
//
// # 子ごとの中身
//
// 切り替えの部品は、子ごとに部品の中身（floatContent。バッファ、カーソルのある項目、木の開閉、入力の行と履歴、書き足しの状態）を持ち、
// 選んでいる子の中身を floatState.floatContent に置く。キーを受けるのも、描くのも、その中身である。
// このため、一覧、木、テキスト、書き足すテキストと入力の行の組の処理（popup.go、console.go、viewappend.go）は、
// 切り替えの部品の子でも、子でない部品と同じ関数がそのまま働く。
// 子を切り替えるのは、floatState.floatContent を別の子の中身へ替えることで、前の子の中身は捨てない（カーソル、木の開閉、入力の行を保つ）。
// 見えていない子への view/update と view/append は、その間だけ floatState.floatContent をその子の中身にして当てる（withContent）。
// スクロールの位置は、窓の中を描く子の Renderer を ui が子ごとに持つ（ui.Renderer.FloatChildRenderers）ので、子ごとに保たれる。
//
// # フォーカスとモード
//
// 最後にフォーカスのあったバッファ（書き足すテキストと入力の行の組では出力か入力の行）は consoleState.outFocus が持つ。
// 子へ切り替えると、キーを受けるバッファをその子のフォーカスのあったバッファへ移す（switchFocus）。
// 子はどれもノーマルモードで出す。書き足すテキストと入力の行の組の子も、入力の行のノーマルモードで始め（組を単体で出したときと違う）、
// 子を離れるときにインサートモードとビジュアルモードを終える（FR-301。<Tab> で区画を回すたびに組の子で止まらないため）。

// floatSwitcher は、切り替えの部品の子の並びである（floatState.sw）。
type floatSwitcher struct {
	children []*switchChild
	// sel は、選んでいる子の添字である。その子の中身が floatState.floatContent にある（withContent の間を除く）。
	sel int
}

// switchChild は、切り替えの部品の子 1 つである。
type switchChild struct {
	id, title string
	// keyText は、その子へ直に切り替えるキーの表記（ext.ViewChild.Key）で、見出しの行に [keyText] で出す。
	// key はそれを読んだキーで、hasKey が false なら無い。
	keyText string
	key     editcmd.Key
	hasKey  bool
	content *floatContent
}

// cur は、選んでいる子を返す。
func (sw *floatSwitcher) cur() *switchChild { return sw.children[sw.sel] }

// index は、ID が id の子の添字を返す。無ければ -1 である。
func (sw *floatSwitcher) index(id string) int {
	return slices.IndexFunc(sw.children, func(c *switchChild) bool { return c.id == id })
}

// contents は、部品 f の中身をすべて返す。切り替えの部品なら子の中身を並びの順に、ほかの部品なら f の中身だけを返す。
func (f *floatState) contents() []*floatContent {
	if f.sw == nil {
		return []*floatContent{f.floatContent}
	}
	out := make([]*floatContent, len(f.sw.children))
	for i, c := range f.sw.children {
		out[i] = c.content
	}
	return out
}

// withContent は、部品 f の中身を一時だけ ct にして fn を呼び、戻ったら元に戻す。見えていない子への view/update と view/append を、
// 子でない部品と同じ関数で当てるのに使う。fn の中で子を切り替えたり、部品を閉じたりしない。
func (f *floatState) withContent(ct *floatContent, fn func()) {
	saved := f.floatContent
	f.floatContent = ct
	defer func() { f.floatContent = saved }()
	fn()
}

// buffers は、中身 ct のバッファ（入力の行、ファインダーの一覧、組の出力。無いものは除く）を返す。
func (ct *floatContent) buffers() []*bufferState {
	out := []*bufferState{ct.bs}
	if ct.list != nil {
		out = append(out, ct.list)
	}
	if ct.con != nil && ct.con.out != nil {
		out = append(out, ct.con.out)
	}
	return out
}

// hasBuffer は、部品 f のどれかの中身（切り替えの部品なら、見えていない子を含む）が bs を出しているかを返す。
func (f *floatState) hasBuffer(bs *bufferState) bool {
	for _, ct := range f.contents() {
		if slices.Contains(ct.buffers(), bs) {
			return true
		}
	}
	return false
}

// focusBuffer は、中身 ct のうち、キーを受ける方のバッファ（書き足すテキストと入力の行の組で出力へフォーカスを移していれば出力、
// ほかは bs）を返す。
func (e *Editor) focusBuffer(ct *floatContent) *bufferState {
	if ct.con != nil && ct.con.outFocus && slices.Contains(e.bufs, ct.con.out) {
		return ct.con.out
	}
	return ct.bs
}

// errSwitcherKey は、切り替えの部品の子のキー（ext.ViewChild.Key）を断る誤りである。
var errSwitcherKey = errors.New("switcher の子の key")

// childKeys は、切り替えの部品 c の子のキーを読む。キーとして読めない表記、2 つ以上のキー、本体の部品の操作と重なるキー
// （<CR>、<Esc>、:、<C-w>。基本設計 1.4）、読んだ後に重なるキーは誤りにする。子の添字ごとのキー（無ければ ok が false）を返す。
func childKeys(c ext.ViewComponent) ([]editcmd.Key, []bool, error) {
	keys := make([]editcmd.Key, len(c.Children))
	has := make([]bool, len(c.Children))
	reserved := []editcmd.Key{editcmd.Special(editcmd.CodeEnter), editcmd.Esc, editcmd.Rune(':'), ctrlW}
	for i, ch := range c.Children {
		if ch.Key == "" {
			continue
		}
		ks, err := editcmd.ParseKeys(ch.Key)
		switch {
		case err != nil:
			return nil, nil, fmt.Errorf("%w %q を読めない: %w", errSwitcherKey, ch.Key, err)
		case len(ks) != 1:
			return nil, nil, fmt.Errorf("%w %q は 1 つのキーでない", errSwitcherKey, ch.Key)
		case slices.Contains(reserved, ks[0]):
			return nil, nil, fmt.Errorf("%w %q は本体の部品の操作と重なる", errSwitcherKey, ch.Key)
		}
		for j := range i {
			if has[j] && keys[j] == ks[0] {
				return nil, nil, fmt.Errorf("%w %q が子 %q のキーと重なる", errSwitcherKey, ch.Key, c.Children[j].ID)
			}
		}
		keys[i], has[i] = ks[0], true
	}
	return keys, has, nil
}

// newChildContent は、切り替えの部品の子 ch の中身を作る。子の部品の見出しは読まず、子の名前をバッファの名前にする
// （ステータス行に出る。書き足すテキストと入力の行の組は決まった名前）。
func (e *Editor) newChildContent(ch ext.ViewChild, extID string) *floatContent {
	c := ch.Component
	c.Title = ch.Title
	ct := e.newComponentFloat(c, extID).floatContent
	ct.child = ch.ID
	return ct
}

// newSwitcherFloat は、切り替えの部品 c を中身にした部品を作る（FR-269）。子ごとに中身を作り、選ぶ子（c.Selected。省けば最初の子）の
// 中身を出す。開くのは呼び出し側である。子のキーは childKeys で読めることを、呼び出し側が先に確かめる。
//
// req: FR-269
func (e *Editor) newSwitcherFloat(c ext.ViewComponent, extID string) *floatState {
	keys, has, _ := childKeys(c)
	sw := &floatSwitcher{}
	for i, ch := range c.Children {
		sc := &switchChild{id: ch.ID, title: ch.Title, keyText: ch.Key, content: e.newChildContent(ch, extID)}
		if has != nil {
			sc.key, sc.hasKey = keys[i], has[i]
		}
		sw.children = append(sw.children, sc)
	}
	sw.sel = max(c.ChildIndex(c.Selected), 0)
	return &floatState{floatContent: sw.cur().content, sw: sw, ext: extID, title: c.Title, scratch: true}
}

// checkComponentKeys は、開く部品 c が切り替えの部品なら、子のキーを読めるかを確かめる（popup/open と pane/open）。
func checkComponentKeys(c ext.ViewComponent) error {
	if c.Kind != ext.ViewSwitcher {
		return nil
	}
	_, _, err := childKeys(c)
	return err
}

// bindViewports は、部品 f のどの中身のバッファのページ単位のスクロール（command.State.Viewport）も、f の窓の表示領域につなぐ。
// 切り替えの部品の見えていない子は、その子を描いたときの子の Renderer（ui.Renderer.FloatChildRenderers）につなぐ。
// 開いたとき（openFloat、paneOpenRequest）と、子の中身を作ったとき（view/update）に呼ぶ。
func (e *Editor) bindViewports(f *floatState) {
	for _, ct := range f.contents() {
		ct.bs.state.Viewport = floatViewport{f: f, r: e.renderer, ct: ct}
		if ct.con != nil {
			ct.con.out.state.Viewport = consoleOutViewport{f: f, r: e.renderer, ct: ct}
		}
	}
}

// childRenderers は、部品 f の中身 ct を描く子の Renderer（窓の中と、組の出力）を返す。描いていなければ nil である。
// 切り替えの部品の子なら、ui が子ごとに持つものを返す（見えていない子もその子のもの）。ほかの部品なら、窓の中を描いた子の Renderer である。
func (f *floatState) childRenderers(r *ui.Renderer, ct *floatContent) (text, list *ui.Renderer) {
	if c := f.remote; c != nil {
		r = c.r
	}
	if f.sw != nil && ct != nil {
		return r.FloatChildRenderers(f.id, ct.child)
	}
	return r.FloatRenderer(), r.ConsoleOutputRenderer()
}

// selectChild は、切り替えの部品 f の i 番目の子を選んで出す（FR-269、FR-301）。前の子の中身は捨てない。
// f にフォーカスがあれば（中央のポップアップか、キーを処理している間のペイン）、キーを受けるバッファを、選んだ子の最後にフォーカスの
// あったバッファ（組なら出力か入力の行）へ移す。前の子のバッファのインサートモードとビジュアルモードは、離れるときに終えて
// ノーマルモードにする（FR-301。<Tab> で区画を回すたびに組の子のインサートモードで止まらないため。nvim-dap-view の REPL と同じく、
// 組の子は入力の行のノーマルモードで始まり、i などで打ち始める）。前の子のプレビューは閉じる（見えていない子のプレビューは開かない）。
// user なら、利用者の操作で替えたので、持ち主の拡張機能へ view/childSelect を送る。同じ子なら何もしない。
//
// req: FR-269, FR-270, FR-301
func (e *Editor) selectChild(f *floatState, i int, user bool) {
	sw := f.sw
	if sw == nil || i < 0 || i >= len(sw.children) || i == sw.sel {
		return
	}
	old := f.floatContent
	sw.sel = i
	f.pending, f.count = nil, nil
	ch := sw.cur()
	f.floatContent = ch.content
	if f.focused && (old == nil || slices.Contains(old.buffers(), e.cur) || (old.preview.bs != nil && e.cur == old.preview.bs)) {
		e.switchFocus(e.focusBuffer(f.floatContent))
	}
	if old != nil {
		if old.con != nil {
			// 離れる組の子の補完の一覧を閉じる（FR-297。インサートモードを終えるので、戻っても出し直さない）。
			e.cancelCompletion(old.con)
		}
		for _, bs := range old.buffers() {
			if m := bs.state.Mode(); m == command.ModeInsert || m == command.ModeVisual || m == command.ModeVisualLine {
				bs.state.SuspendMode()
			}
		}
		f.withContent(old, func() { e.closePreview(f) })
	}
	if user {
		e.notifyView(f, ext.MethodViewChildSelect, ext.ViewChildSelectParams{View: ext.ViewID(f.id), Child: ch.id})
	}
}

// moveChild は、切り替えの部品 f の子を、how（next、prev、first、last）の向きに n 回（next と prev。0 なら 1 回）切り替える。
// 次と前は端で折り返し、最初と最後は折り返さない（nvim-dap-view の navigate の wrap と同じ）。利用者の操作として view/childSelect を送る。
// 切り替えの部品でなければ何もしない。
//
// req: FR-269
func (e *Editor) moveChild(f *floatState, how string, n int) {
	if f == nil || f.sw == nil {
		return
	}
	k, sel := len(f.sw.children), f.sw.sel
	n = max(n, 1)
	var to int
	switch how {
	case "next":
		to = (sel + n) % k
	case "prev":
		to = ((sel-n)%k + k) % k
	case "first":
		to = 0
	case "last":
		to = k - 1
	default:
		return
	}
	e.selectChild(f, to, true)
}

// switcherOf は、読み込んでいる client で、バッファ bs がキーを受けている切り替えの部品（フォーカスのあるポップアップかペイン）を返す。
// bs がその部品の選んでいる子のバッファ（書き足すテキストと入力の行の組では、出力と入力の行のどちらも）でなければ nil である。
func (e *Editor) switcherOf(bs *bufferState) *floatState {
	f := e.float
	if f == nil || !f.focused || f.sw == nil || !slices.Contains(f.buffers(), bs) {
		return nil
	}
	return f
}

// viewChildAction は、ref が切り替えの部品の子を切り替える本体の操作（input.ViewChildPrefix で始まる ID）なら、
// バッファ bs でキーを受けている切り替えの部品の子を n 回（0 なら 1 回）切り替え、true を返す。
// bs が切り替えの部品の子に属さなければ何もせず、誤りも出さない（基本設計 1.4）。それ以外の操作なら false を返す。
// キーを受けるバッファが替わったら command.ErrBufferSwitched を返し、マクロの再生の残りを移った先のバッファで続けさせる。
//
// req: FR-269
func (e *Editor) viewChildAction(bs *bufferState, ref editcmd.Ref, n int) (bool, error) {
	how, ok := strings.CutPrefix(ref.ID, input.ViewChildPrefix)
	if !ref.Provider() || !ok {
		return false, nil
	}
	f := e.switcherOf(bs)
	if f == nil {
		return true, nil
	}
	e.moveChild(f, how, n)
	if e.cur != bs {
		return true, command.ErrBufferSwitched
	}
	return true, nil
}

// childKey は、切り替えの部品 f で、キーの列の途中でないときに打ったキー k が、子の直に切り替えるキー（ext.ViewChild.Key）なら、
// その子へ切り替えて true を返す（FR-269）。contributes.popupKeys と本体の既定のキーより先に引く（popupKey から呼ぶ）。
func (e *Editor) childKey(f *floatState, k editcmd.Key) bool {
	if f.sw == nil || len(f.pending) > 0 {
		return false
	}
	for i, c := range f.sw.children {
		if c.hasKey && c.key == k {
			e.selectChild(f, i, true)
			return true
		}
	}
	return false
}

// childUpdate は、切り替えの部品 f の子 id への view/update（部品 c）を当てる（FR-270）。見えていない子にもその場で当てる。
// 知らない子と、部品の種類が子を作ったときと違う更新は捨てる。カーソルと木の開閉を保つ規則は、子でない部品の view/update と同じである。
//
// req: FR-270
func (e *Editor) childUpdate(f *floatState, id string, c ext.ViewComponent) {
	i := f.sw.index(id)
	if i < 0 {
		return
	}
	ct := f.sw.children[i].content
	if componentKind(c.Kind) != ct.kind {
		return
	}
	f.withContent(ct, func() { e.setFloatContent(f, c) })
}

// switcherUpdate は、切り替えの部品 f の全体を置き換える view/update（部品 c）を当てる（FR-270）。
// 同じ ID で同じ種類の子は状態を保ったまま中身を置き換え、無くなった ID の子と種類の替わった子は捨て、新しい ID の子は作る。
// 子の並びと名前とキーは c のとおりにする。c.Selected があればその子を選び（view/childSelect は送らない）、無ければ選んでいる子を保つ。
// 選んでいた子が無くなったら最初の子を選ぶ。子のキーを読めない更新は捨てる。
//
// req: FR-270
func (e *Editor) switcherUpdate(f *floatState, c ext.ViewComponent) {
	if len(c.Children) == 0 {
		// 選ぶ子だけを替える（ext.ViewUpdateParams.SelectsOnly）。子の中身は変えない。知らない子なら捨てる。
		if i := f.sw.index(c.Selected); i >= 0 {
			e.selectChild(f, i, false)
		}
		return
	}
	keys, has, err := childKeys(c)
	if err != nil {
		e.log.Warn("switcher の view/update を捨てた", "extension", f.ext, "err", err)
		return
	}
	if c.Title != "" {
		f.title = c.Title
	}
	sw := f.sw
	oldID := sw.cur().id
	old := map[string]*switchChild{}
	for _, ch := range sw.children {
		old[ch.id] = ch
	}
	var next []*switchChild
	for i, ch := range c.Children {
		sc := old[ch.ID]
		if sc != nil && componentKind(ch.Component.Kind) == sc.content.kind {
			delete(old, ch.ID)
			cc := ch.Component
			f.withContent(sc.content, func() { e.setFloatContent(f, cc) })
			if sc.content.kind != floatConsole {
				// 子の名前を替えたら、ステータス行に出るバッファの名前も付け直す（組は決まった名前）。
				sc.content.bs.title = cmp.Or(ch.Title, "[ポップアップ]")
			}
		} else {
			sc = &switchChild{id: ch.ID, content: e.newChildContent(ch, f.ext)}
		}
		sc.title, sc.keyText, sc.key, sc.hasKey = ch.Title, ch.Key, keys[i], has[i]
		next = append(next, sc)
	}
	sel := slices.IndexFunc(next, func(ch *switchChild) bool { return ch.id == oldID })
	if c.Selected != "" {
		sel = c.ChildIndex(c.Selected)
	}
	sel = max(sel, 0)
	// 出していた子の中身（f.floatContent）から選ぶ子へ切り替えてから（キーを受けるバッファを移してから）、捨てた子のバッファを閉じる。
	// 出していた子が無くなったら sel を -1 にして、selectChild がその中身からフォーカスとプレビューを移す。
	prev := f.floatContent
	sw.children = next
	sw.sel = slices.IndexFunc(next, func(ch *switchChild) bool { return ch.content == prev })
	e.selectChild(f, sel, false)
	e.bindViewports(f)
	for _, ch := range old {
		e.closeContent(f, ch.content)
	}
}

// closeContent は、部品 f の中身 ct（切り替えの部品の見えていない子）のバッファとプレビューを閉じる。
func (e *Editor) closeContent(f *floatState, ct *floatContent) {
	if ct == nil || ct == f.floatContent {
		return
	}
	if ct.con != nil {
		e.cancelCompletion(ct.con) // 答えを待っている補完の問い合わせを取り消す（FR-297）
	}
	f.withContent(ct, func() { e.closePreview(f) })
	for _, bs := range ct.buffers() {
		if slices.Contains(e.bufs, bs) {
			e.closeBufferState(bs)
		}
	}
}

// uiSwitcher は、切り替えの部品 f の見出しの行の描画の入力を作る（floatFrame）。切り替えの部品でなければ nil である。
func (f *floatState) uiSwitcher() *ui.Switcher {
	if f.sw == nil {
		return nil
	}
	out := &ui.Switcher{Selected: f.sw.sel}
	for _, c := range f.sw.children {
		out.Children = append(out.Children, ui.SwitcherChild{ID: c.id, Title: c.title, Key: c.keyText})
	}
	return out
}
