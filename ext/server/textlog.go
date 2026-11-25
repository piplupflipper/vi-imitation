package server

import (
	"errors"
	"sync"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、テキストの部品の末尾への書き足し（view/append）と、書き足した中身の写し（TextLog）を扱う
// （基本設計 1.4 の「#399 の切り替えの部品と書き足し」の「末尾への書き足し」。FR-272、FR-273）。

// AppendView は、本体へ view/append を送り、開いたテキストの部品の末尾へ書き足させる。応答は待たない。どこから呼んでもよい。
// p が ext.ViewAppendParams.Validate を通らなければ、送らずにその誤りを返す。
// 閉じた後に開き直したときに中身を戻したいときは、TextLog を使う。
func (s *Server) AppendView(p ext.ViewAppendParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return s.notify(ext.MethodViewAppend, p)
}

// ErrOpening は、TextLog が開き直している最中（Open の open を呼んでいる間）に、Open か Rebind を呼んだことを表す。
var ErrOpening = errors.New("server: the text log is being opened")

// ErrChildUnsupported は、切り替えの部品の子（空でない child）へ Rebind したことを表す。
// 子だけを置き換える view/update（基本設計 1.4 の「子を指す更新」）は、切り替えの部品を足すときに足す。
var ErrChildUnsupported = errors.New("server: rebinding to a child of a switcher is not supported yet")

// textLogKey は、写しを結びつける部品（部品の ID と、切り替えの部品の子の ID の組）である。
type textLogKey struct {
	view  ext.ViewID
	child string
}

// textLogs は、Server が持つ、部品に結びついている写しの表である。
// 1 つの部品（と子）には一度に 1 つの写しだけを結びつける。表が結びつきの正で、TextLog の view と child は、
// 表がその写しを指している間だけ有効である（表から外れた写しは、次に触ったときに結びつきを外す）。
//
// 錠の順は、TextLog.mu の後に textLogs.mu である。textLogs.mu を持ったまま TextLog.mu を取らない。
type textLogs struct {
	mu   sync.Mutex
	byID map[textLogKey]*TextLog
	// closed は、view/closed を受けた部品の ID である。本体は ID を使い回さないので、閉じた後に Open が結びつけないために覚える。
	// 1 つあたり数バイトで、開いた部品の数だけ増える（Server.closed と同じ考え方）。
	closed map[ext.ViewID]bool
}

// viewClosed は、view/closed を受けた部品 view に結びついた写しをすべて外す。ループから、OnView のフックより先に呼ぶ。
func (s *Server) viewClosed(view ext.ViewID) {
	l := &s.textLogs
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed == nil {
		l.closed = map[ext.ViewID]bool{}
	}
	l.closed[view] = true
	for k := range l.byID {
		if k.view == view {
			delete(l.byID, k)
		}
	}
}

// TextLog は、テキストの部品へ書き足す中身の写しである（FR-273）。
//
// 拡張機能は、書き足すたびに Append を呼ぶ。写しは、部品に結びついている間は本体へ view/append を送り、
// 閉じている間は写しにだけ足す。利用者が閉じた部品を開き直すときは Open を呼ぶと、写しの中身（閉じる前の中身と、閉じている間に
// 書き足した中身）を部品にして開き、結びつけ直す。拡張機能は、写しを持つ処理と、閉じた知らせで結びつきを外す処理を書かなくてよい
// （Server が view/closed を受けたら、OnView のフックより先に、その部品に結びついた写しを外す）。
//
// 写しは、本体と同じ関数（ext.AppendViewText と ext.ViewTextOverflow）で行を分けて古い行を捨てるので、開き直した中身は、
// 本体が閉じる前に出していた中身と同じである。
//
// 1 つの写しは一度に 1 つの部品（と子）にだけ結びつき、1 つの部品（と子）には一度に 1 つの写しだけが結びつく。
// 中身の取り出し、送信、結びつけを写しの錠の中の 1 つの操作にするので、開き直しと付け替えの最中に Append が届いても、
// 書き足しは表示から抜けず、重ならない。どのメソッドも、どの goroutine から呼んでもよい。
type TextLog struct {
	s  *Server
	mu sync.Mutex
	// lines と spans は、写しの中身の行と、行ごとの色の範囲である。eol は、中身の末尾が改行で終わっているかである（ext.SplitViewText）。
	lines []string
	spans [][]ext.ViewSpan
	eol   bool
	// bound、view、child は、結びついた部品である。Server の表（textLogs）がこの写しを指している間だけ有効である。
	bound bool
	view  ext.ViewID
	child string
	// opening は、Open が open を呼んでいる間であることを表す。pending は、その間に書き足した分を 1 件につないだものである。
	opening bool
	pending *ext.ViewAppendJoin
	// detached は、Open が open を呼んでいる間に Detach を呼んだことを表す。Open は、open が戻っても結びつけない。
	detached bool
}

// NewTextLog は、s の上の空の写しを作る。行数の上限は本体と同じ値（ext.ViewTextMaxLines）である。
func NewTextLog(s *Server) *TextLog {
	return &TextLog{s: s, lines: []string{""}, spans: [][]ext.ViewSpan{nil}}
}

// Append は、text を写しに足す。style は 7.1 の色の名前で、空でなければ text の全体にその色を付ける。
// 結びついていれば、錠を持ったまま view/append（結びついた部品と子）を送る。開き直している最中（Open）なら、送らずに
// 「開いている間の書き足し」に積み、開き終えたときに送る。送る順は呼んだ順である。
// text が不正な UTF-8 を含めば、何もせずに誤りを返す。誤りは、送れなかったとき（接続が無いとき）にも返すが、写しには足してある。
//
// req: FR-273
func (t *TextLog) Append(text, style string) error {
	if text == "" {
		return nil
	}
	p := ext.ViewAppendParams{Text: text}
	if style != "" {
		p.Highlights = []ext.ViewSpan{{Start: 0, End: len(text), Style: style}}
	}
	if err := p.Validate(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.add(p.Text, p.Highlights)
	if t.opening {
		if t.pending == nil {
			t.pending = &ext.ViewAppendJoin{}
		}
		t.pending.Add(p.Text, p.Highlights)
		return nil
	}
	return t.sendLocked(p)
}

// add は、写しの中身へ text を足し、上限を超えた古い行を捨てる。t.mu を持って呼ぶ。
func (t *TextLog) add(text string, spans []ext.ViewSpan) {
	last := len(t.lines) - 1
	lines, lineSpans, eol := ext.AppendViewText(t.lines[last], t.spans[last], t.eol, text, spans)
	if !t.eol {
		t.lines, t.spans = t.lines[:last], t.spans[:last]
	}
	t.lines = append(t.lines, lines...)
	t.spans = append(t.spans, lineSpans...)
	t.eol = eol
	if d := ext.ViewTextOverflow(len(t.lines)); d > 0 {
		// 捨てた行を指したままにしない。
		clear(t.lines[:d])
		clear(t.spans[:d])
		t.lines, t.spans = t.lines[d:], t.spans[d:]
	}
}

// sendLocked は、結びついていれば p を結びついた部品への view/append にして送る。t.mu を持って呼ぶ。
func (t *TextLog) sendLocked(p ext.ViewAppendParams) error {
	if !t.bound {
		return nil
	}
	l := &t.s.textLogs
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byID[textLogKey{t.view, t.child}] != t {
		t.bound = false // 閉じたか、別の写しが付け替えた
		return nil
	}
	p.View, p.Child = t.view, t.child
	return t.s.notify(ext.MethodViewAppend, p)
}

// component は、写しの中身をテキストの部品にする。t.mu を持って呼ぶ。
func (t *TextLog) component() ext.ViewComponent {
	c := ext.ViewComponent{Kind: ext.ViewText, Text: ext.JoinViewText(t.lines, t.eol), Tail: true}
	for i, ss := range t.spans {
		for _, s := range ss {
			c.Highlights = append(c.Highlights, ext.Decoration{
				Range: ext.Range{Start: ext.Position{Line: i, Byte: s.Start}, End: ext.Position{Line: i, Byte: s.End}},
				Style: s.Style,
			})
		}
	}
	return c
}

// unbindLocked は、結びつきを外す。t.mu を持って呼ぶ。
func (t *TextLog) unbindLocked() {
	if !t.bound {
		return
	}
	l := &t.s.textLogs
	l.mu.Lock()
	k := textLogKey{t.view, t.child}
	if l.byID[k] == t {
		delete(l.byID, k)
	}
	l.mu.Unlock()
	t.bound = false
}

// bindLocked は、部品 view と子 child に結びつける。その部品と子に結びついていた別の写しは外れる。
// 部品が既に閉じていれば結びつけずに false を返す。t.mu と t.s.textLogs.mu を持って呼ぶ。
func (t *TextLog) bindLocked(view ext.ViewID, child string) bool {
	l := &t.s.textLogs
	if l.closed[view] {
		return false
	}
	if l.byID == nil {
		l.byID = map[textLogKey]*TextLog{}
	}
	l.byID[textLogKey{view, child}] = t
	t.bound, t.view, t.child = true, view, child
	return true
}

// Open は、写しの中身を部品にして開き直し、開いた部品に結びつける（FR-273）。
//
// 錠の中で、前の結びつきを外し、写しの中身からテキストの部品を作り、開いている最中の印を立てて錠を放し、open を呼ぶ。
// open は、呼び出す側が、受け取った部品を pane/open の部品か切り替えの部品の子に入れて開き、部品の ID を返す関数である
// （OpenPane を呼ぶ関数など）。open の間は錠を持たないので、pane/open の応答を待つ間も Append は止まらない。
// その間の書き足しは写しに足し、送らずに積む。open が戻ったら錠を取り直し、部品（と子 child）に結びつけ、積んだ書き足しを
// view/append で送ってから印を下ろす。部品の中身は open に渡した時点の写しなので、積んだ書き足しを送ると、抜けも重なりも無い。
//
// open が誤りを返したら、結びつけずに印を下ろしてその誤りを返す（積んだ書き足しは写しに入っているので捨てない）。
// open が戻る前に部品が閉じていたら（view/closed が先に届いたら）、結びつけない。open の間に Detach を呼んでいても結びつけない。
// 渡す部品は、末尾から見せて追従する印（ext.ViewComponent.Tail）を立てる。開き直した表示は、閉じる前と同じく末尾に追従する。
// 開き直している最中に Open か Rebind を呼ぶと ErrOpening を返す。
// open の中で Server のハンドラーの ctx で OpenPane を呼ぶと ErrEditInHandler になるので、Open はフックの外から呼ぶ。
//
// req: FR-273
func (t *TextLog) Open(open func(ext.ViewComponent) (ext.ViewID, error), child string) error {
	t.mu.Lock()
	if t.opening {
		t.mu.Unlock()
		return ErrOpening
	}
	t.unbindLocked()
	c := t.component()
	t.opening, t.detached = true, false
	t.mu.Unlock()

	id, err := open(c)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.opening = false
	pending := t.pending
	t.pending = nil
	if err != nil {
		return err
	}
	if t.detached {
		t.detached = false
		return nil
	}
	l := &t.s.textLogs
	l.mu.Lock()
	defer l.mu.Unlock()
	if !t.bindLocked(id, child) || pending == nil {
		return nil
	}
	return t.s.notify(ext.MethodViewAppend, pending.Params(id, child))
}

// Rebind は、開いている部品 view に結びつけ直し、写しの中身で部品の中身を置き換える。
// 錠の中で、前の結びつきと、同じ部品に結びついていた別の写しを外し、写しの中身を部品にした view/update を送り、結びつける。
// view/update と view/append を同じ錠の中で送るので、付け替えの前の書き足しは view/update の中身に入り、後の書き足しは
// view/update の後の view/append になる。部品が既に閉じていれば、何も送らずに結びつけない。
//
// 切り替えの部品の子（空でない child）への付け替えは、子を指す view/update がまだ無いので、ErrChildUnsupported を返す。
// 開き直している最中（Open）は ErrOpening を返す。
func (t *TextLog) Rebind(view ext.ViewID, child string) error {
	if child != "" {
		return ErrChildUnsupported
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.opening {
		return ErrOpening
	}
	t.unbindLocked()
	l := &t.s.textLogs
	l.mu.Lock()
	defer l.mu.Unlock()
	if !t.bindLocked(view, child) {
		return nil
	}
	return t.s.notify(ext.MethodViewUpdate, ext.ViewUpdateParams{View: view, Component: t.component()})
}

// Detach は、結びつきを外す。その後の Append は写しにだけ足す。
// 開き直している最中（Open が open を呼んでいる間）に呼ぶと、Open は open が戻っても結びつけず、その間の書き足しも送らない
// （開いた部品は、拡張機能の側で閉じる）。
func (t *TextLog) Detach() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.opening {
		t.detached = true
	}
	t.unbindLocked()
}

// Attached は、結びついた部品と子を返す。結びついていなければ ok が false である。
// 部品が閉じた（view/closed を受けた）後と、別の写しが同じ部品に付け替えた後は、結びついていない。
func (t *TextLog) Attached() (view ext.ViewID, child string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.bound {
		return 0, "", false
	}
	l := &t.s.textLogs
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byID[textLogKey{t.view, t.child}] != t {
		t.bound = false
		return 0, "", false
	}
	return t.view, t.child, true
}
