package editor

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// openFakeList は、偽の拡張機能の一覧のポップアップを開き、開いた知らせを待つ。
func (h *harness) openFakeList() {
	h.t.Helper()
	var before uint64
	h.e.do(func() {
		if h.e.float != nil {
			before = h.e.float.id
		}
	})
	h.keys(":fake.popup list<CR>")
	h.eventually("一覧", func() bool { return h.e.float != nil && h.e.float.ext == "fake" && h.e.float.id != before })
	var view uint64
	h.e.do(func() { view = h.e.float.id })
	h.waitPopupOpened(view)
}

// 続けて打ったキーの register/set は、打った順に書く（PR #433 のレビューの必須 1 の再現。偽の拡張機能はフックから SetRegisterAsync で積む）。
//
// req: FR-315
func TestRegisterSetOrder(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t)})
	h.openFakeList()
	const want = "yank:oneyank:closeyank:oneyank:close"
	for i := range 10 {
		h.keys(`gg"azyj"Azyk"Azyj"Azy`)
		h.settle()
		h.eventually(fmt.Sprintf("%d 回目の a", i), func() bool {
			r, _ := h.e.cur.state.Registers().Get('a')
			return len(r.Text) == len(want)
		})
		if r := h.regOf('a'); r.Text != want {
			t.Fatalf("%d 回目: a = %q, want %q", i, r.Text, want)
		}
	}
}

// マクロの再生中の Sync の popupKeys のキー（zy）は、拡張機能の応答（view/keySync）を待ち、応答の中身をレジスターへ書いてから次のキーへ
// 進む。"azy<Esc>"ap の再生は、入れた後の a を貼る（PR #433 のレビューの裁定要 1 の再現）。応答が無ければ、再生の期限でその位置で止める。
//
// req: FR-19, FR-315
func TestRegisterSetReplayWaits(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t), ReplayTimeout: 2 * time.Second})
	h.openFakeList()
	h.keys(`qq"azy<Esc>"apq`)
	h.settle()
	if r := h.regOf('q'); r.Text != "\"azy\x1b\"ap" {
		t.Fatalf(`"q = %q`, r.Text)
	}
	for i := range 5 {
		h.keys(`:fake.setreg a c "old"<CR>`)
		h.eventually("old", func() bool { r, _ := h.e.cur.state.Registers().Get('a'); return r.Text == "old" })
		h.keys("ggdG")
		h.openFakeList()
		h.keys("@q")
		h.settle()
		h.eventually("再生の後", func() bool { return h.e.float == nil && !h.e.replayPending() })
		if got := h.text(); got != "yank:one" {
			t.Fatalf("%d 回目: @q pasted %q, want yank:one", i, got)
		}
	}

	// 応答しないキー（zn）は、再生の期限で止め、残り（j）を再生しない。
	h.openFakeList()
	h.keys("qwznjq")
	h.settle()
	h.keys("gg@w")
	h.waitMessage("待ったので、再生を止めました")
	h.settle()
	var line int
	h.e.do(func() { line = h.e.float.bs.state.Cursor().Line })
	if line != 0 {
		t.Errorf("@w after the timeout ran the rest: cursor line %d", line)
	}
}

// 再生中の Sync のキーの待ちを挟む再帰のマクロ（qr"Azy@rq の後の @r）も、待ちを挟まない再帰と同じく入れ子の上限で止まり、
// <C-c> を打てば、上限より前に止まる（dev-cycle 2.2。PR #249 の 4 回目の確認の必須と同じ確かめ）。
//
// req: FR-05, FR-19, FR-315
func TestRegisterSetReplayRecursion(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t)})
	h.openFakeList()
	h.keys(`qr"Azy@rq`)
	h.settle()
	run := func(keys string) int {
		t.Helper()
		h.keys(`:fake.setreg a c ""<CR>`)
		h.eventually("空の a", func() bool { r, _ := h.e.cur.state.Registers().Get('a'); return r.Text == "" })
		if _, ok := h.float(); !ok {
			h.openFakeList()
		}
		h.keys(keys)
		deadline := time.Now().Add(60 * time.Second)
		for {
			var done bool
			h.e.do(func() { done = !h.e.replayPending() })
			if done {
				time.Sleep(300 * time.Millisecond)
				h.e.do(func() { done = !h.e.replayPending() })
				if done {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: 再生が止まらない", keys)
			}
			time.Sleep(20 * time.Millisecond)
		}
		h.settle()
		return strings.Count(h.regOf('a').Text, "yank:one")
	}
	if n := run("@r"); n < 90 || n > 101 {
		t.Errorf("上限: a に %d 回入れた（約 100 回で止まるはず）", n)
	}
	if n := run("@r<C-c>"); n >= 90 {
		t.Errorf("<C-c>: a に %d 回入れた（中断で止まらなかった）", n)
	}
}

// キーの結果の register/set は、届くまでの間にほかの client がその拡張機能へ問い合わせても、キーを打った client の端末の
// クリップボードへ送る（view/key の Origin。PR #433 のレビューの推奨 2）。
//
// req: FR-315
func TestRegisterSetOrigin(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\n", "b.txt": "beta\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	b := h.attach(ClientInfo{ID: 2, Files: []string{filepath.Join(dir, "b.txt")}})
	h.openFakeList()
	// update の項目の zy は、偽の拡張機能が 300 ms 後に register/set を送る。その間に B が fake.echo を問い合わせる。
	h.keys("jjzy")
	h.settle()
	b.keys(":fake.echo x<CR>")
	h.settle()
	// レジスターは client の間で共有するので、最後に持った State（e.shared）から読む。
	h.eventually("無名のレジスター", func() bool { r, _ := h.e.shared.Registers().Get(0); return r.Text == "yank:update" })
	h.eventually("A のクリップボード", func() bool {
		c := h.term.clipboard()
		return len(c) > 0 && c[len(c)-1] == "yank:update"
	})
	if c := b.term.clipboard(); len(c) > 0 {
		t.Errorf("B's clipboard = %q", c)
	}
}

// 64 MiB のメッセージの上限を超える値は、ext/server が送らずに誤りを返し、拡張機能は止まらない（PR #433 のレビューの推奨 1）。
//
// req: FR-315
func TestRegisterSetTooLarge(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t)})
	h.keys(":fake.setreg - c big:70000000<CR>")
	h.waitMessage("setreg err=server: register/set exceeds the message size limit")
	h.keys(`:fake.setreg - c "after"<CR>`)
	h.eventually("上限の後", func() bool { r, _ := h.e.cur.state.Registers().Get(0); return r.Text == "after" })
	if msg := h.message(); strings.Contains(msg, "異常終了") {
		t.Errorf("message = %q", msg)
	}
}
