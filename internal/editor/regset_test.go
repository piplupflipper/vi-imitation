package editor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/command"
)

// regOf は、カーソルのあるバッファのレジスター name（0 は無名のレジスター）の内容を返す。
func (h *harness) regOf(name rune) command.Register {
	var r command.Register
	h.e.do(func() { r, _ = h.e.cur.state.Registers().Get(name) })
	return r
}

// historyLen は、レジスターの履歴の件数を返す。
func (h *harness) historyLen() int {
	var n int
	h.e.do(func() { n = len(h.e.cur.state.Registers().History()) })
	return n
}

// 拡張機能の register/set は、名前を指定した y と同じくレジスターへ書き、履歴の先頭が変わればホストのクリップボードへ送る。
// 多バイト文字、改行を含む値、空の値、行単位、大文字の名前の追記、_、使えない名前（Vim の読み取り専用のレジスターを含む）、
// 履歴の 1 件の上限を超える値（数 MiB）、クリップボードへ送るには大きい値を確かめ、入れた値を p で貼れることも確かめる。
//
// req: FR-315
func TestRegisterSet(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t)})
	set := func(args string) {
		t.Helper()
		h.keys(":fake.setreg " + args + "<CR>")
	}
	clips := func() []string { return h.term.clipboard() }

	// 名前を省くと無名のレジスター（履歴の先頭）へ文字単位で入れ、クリップボードへ送る。知らせは出さない（y と同じ）。
	set(`- c "値\n二"`)
	h.eventually("無名のレジスター", func() bool {
		r, _ := h.e.cur.state.Registers().Get(0)
		return r.Text == "値\n二"
	})
	if r := h.regOf(0); r.Linewise {
		t.Errorf("unnamed = %+v, want charwise", r)
	}
	if c := clips(); len(c) == 0 || c[len(c)-1] != "値\n二" {
		t.Errorf("clipboard = %q", c)
	}
	if msg := h.message(); msg != "" {
		t.Errorf("message after register/set = %q, want none", msg)
	}
	// 入れた値は p で貼れる（文字単位なので、カーソルの後ろに入る）。
	h.keys("iab<Esc>0p")
	h.settle()
	if got := h.text(); got != "a値\n二b" {
		t.Errorf("p after register/set: %q", got)
	}

	// 名前付きのレジスターへ行単位で入れる。最後の改行は 1 つだけ取り除き、履歴にも積む。クリップボードへは改行を付けて送る。
	set(`a l "l1\nl2\n"`)
	h.eventually("a", func() bool { r, _ := h.e.cur.state.Registers().Get('a'); return r.Text == "l1\nl2" })
	if r := h.regOf('a'); !r.Linewise {
		t.Errorf(`"a = %+v, want linewise`, r)
	}
	if r := h.regOf(0); r.Text != "l1\nl2" || !r.Linewise {
		t.Errorf("unnamed after \"a = %+v", r)
	}
	if c := clips(); c[len(c)-1] != "l1\nl2\n" {
		t.Errorf("clipboard = %q", c)
	}
	// 大文字の名前は追記する（行単位の後の文字単位も、行単位としてつなぐ）。
	set(`A c "l3"`)
	h.eventually("A", func() bool { r, _ := h.e.cur.state.Registers().Get('a'); return r.Text == "l1\nl2\nl3" })

	// 空の値は、空のテキストが入る（空の行の yl と同じ）。
	set(`b c ""`)
	h.eventually("空の値", func() bool { r, _ := h.e.cur.state.Registers().Get(0); return r.Text == "" })
	n := h.historyLen()

	// _ には何も書かず、履歴も変えない。
	set(`_ c "x"`)
	h.keys(":fake.setreg - c " + `"after"` + "<CR>")
	h.eventually("_ の後", func() bool { r, _ := h.e.cur.state.Registers().Get(0); return r.Text == "after" })
	if got := h.historyLen(); got != n+1 {
		t.Errorf("history after _ and unnamed = %d, want %d", got, n+1)
	}

	// 使えない名前（Vim の読み取り専用のレジスター . と %、数字）は E354 で断り、何も書かない。拡張機能には誤りで応答する。
	for _, name := range []string{".", "%", "0"} {
		set(name + ` c "bad"`)
		h.waitMessage(fmt.Sprintf("E354: レジスターの名前に使えない文字です: '%s'", name))
		h.keys("<Esc>")
		h.settle()
	}
	if r := h.regOf(0); r.Text != "after" {
		t.Errorf("unnamed after invalid names = %q", r.Text)
	}

	// 履歴の 1 件の上限（1 MiB）を超える値（3 MiB）は、履歴に積まずに無名のレジスターにだけ置き、そのことを知らせる（FR-53）。
	// 名前付きのレジスターには書く。履歴の先頭は変わらないので、クリップボードへ送らない。
	before := len(clips())
	set("c c big:3145728")
	h.waitMessage("3145728 バイトのテキストは上限の 1048576 バイトを超えるので")
	if r := h.regOf(0); len(r.Text) != 3145728 {
		t.Errorf("unnamed after a 3 MiB value: %d bytes", len(r.Text))
	}
	if r := h.regOf('c'); len(r.Text) != 3145728 {
		t.Errorf(`"c after a 3 MiB value: %d bytes`, len(r.Text))
	}
	if got := len(clips()); got != before {
		t.Errorf("clipboard was sent %d times for the oversized value", got-before)
	}
	// 履歴に積むがクリップボードへ送るには大きい値は、送らずに知らせる（y と同じ）。
	set("- c big:800000")
	h.waitMessage("クリップボードへ送りませんでした: 大きすぎます（800000 バイト）")
	if got := len(clips()); got != before {
		t.Errorf("clipboard was sent for the 800000-byte value")
	}
}

// 拡張機能のポップアップで、" と名前を付けて popupKeys のキーを打つと、view/key の register でその名前を知らせ、
// 拡張機能はそのレジスターへ入れられる。付けなければ register を省く。使えない名前は E354 を出して view/key を送らない。
// popupKeys に当たらないキー（yy）は、今までどおり名前を付けた本体のキーとして解釈し、"a<Esc> はポップアップを閉じない。
// マクロに記録した "bzy も、再生で同じ名前を知らせる。
//
// req: FR-315
func TestPopupKeyRegister(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t)})
	h.keys(":fake.popup list<CR>")
	h.eventually("一覧", func() bool { return h.e.float != nil && h.e.float.ext == "fake" })
	var view uint64
	h.e.do(func() { view = h.e.float.id })
	h.waitPopupOpened(view)

	h.keys(`"azy`)
	h.waitMessage(fmt.Sprintf("view %d kind=3 item=one key=fake.yank register=a", view))
	h.eventually(`"a`, func() bool { r, _ := h.e.cur.state.Registers().Get('a'); return r.Text == "yank:one" })
	if r := h.regOf(0); r.Text != "yank:one" {
		t.Errorf("unnamed after \"azy = %q", r.Text)
	}

	h.keys("jzy")
	h.waitMessage(fmt.Sprintf("view %d kind=3 item=close key=fake.yank", view))
	if msg := h.message(); strings.Contains(msg, "register=") {
		t.Errorf("zy without a name: %q", msg)
	}
	h.eventually("無名のレジスター", func() bool { r, _ := h.e.cur.state.Registers().Get(0); return r.Text == "yank:close" })

	// 使えない名前は E354 で、view/key を送らない。
	h.keys(`".zy`)
	h.waitMessage("E354: レジスターの名前に使えない文字です: '.'")
	h.keys(`"+zy`)
	h.waitMessage("E354: レジスターの名前に使えない文字です: '+'")
	h.settle()
	if r := h.regOf(0); r.Text != "yank:close" {
		t.Errorf("unnamed after \".zy = %q", r.Text)
	}

	// popupKeys に当たらない yy は、名前を付けた本体のヤンクになる。<Esc> は " と名前を捨てるだけで、ポップアップを閉じない。
	h.keys(`"byy`)
	h.eventually(`"byy`, func() bool { r, _ := h.e.cur.state.Registers().Get('b'); return r.Text == "close me" && r.Linewise })
	h.keys(`"c<Esc>`)
	h.settle()
	if _, ok := h.float(); !ok {
		t.Fatalf(`"c<Esc> closed the popup`)
	}
	// 名前を付けた後の popupKeys の途中のキー（z）が当たらなければ、名前を付けた本体のキーとして解釈する（zz はスクロール）。
	h.keys(`"dzzzy`)
	h.waitMessage("key=fake.yank")
	if msg := h.message(); strings.Contains(msg, "register=") {
		t.Errorf(`"dzz then zy: %q`, msg)
	}

	// マクロに記録した "ezy は、再生で同じ名前を知らせる。カーソルの項目は再生の時点のものである。
	h.keys(`qq"ezyq`)
	h.eventually(`"e`, func() bool { r, _ := h.e.cur.state.Registers().Get('e'); return r.Text == "yank:close" })
	h.keys("j@q")
	h.waitMessage(fmt.Sprintf("view %d kind=3 item=update key=fake.yank register=e", view))
	h.eventually(`@q の "e`, func() bool { r, _ := h.e.cur.state.Registers().Get('e'); return r.Text == "yank:update" })
	if r := h.regOf('q'); r.Text != `"ezy` {
		t.Errorf(`"q = %q`, r.Text)
	}
	// 使えない名前で止まったマクロは、残りを再生しない（本体の y の E354 と同じ）。
	h.keys(`qr".zyjq`)
	h.settle()
	h.keys("gg@r")
	h.waitMessage("E354")
	h.settle()
	var line int
	h.e.do(func() { line = h.e.float.bs.state.Cursor().Line })
	if line != 0 {
		t.Errorf("@r with an invalid name ran the rest: cursor line %d", line)
	}
}
