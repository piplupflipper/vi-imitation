package editor

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// lastRow は、描いた画面の最後の行（末尾の空白を除く）を返す。
func (h *harness) lastRow() string {
	h.t.Helper()
	lines := strings.Split(h.term.screen(), "\n")
	return strings.TrimRight(lines[len(lines)-1], " ")
}

// waitInput は、送ったキーをすべて処理し終えてから、1 行の入力の問いの中身が want で、最後の行に row が描かれることを確かめる。
// 先にキーを処理し終えるのを待つのは、途中の中身が want と同じになる手順（x|y の <BS> の後の y）で早く抜けないためである
// （PR #411 の再レビューの必須 1）。
func (h *harness) waitInput(what, want, row string) {
	h.t.Helper()
	h.settle()
	h.waitInputHeld(what, want, row)
}

// waitInputHeld は、waitInput のうち、キーを処理し終えるのを待たないものである。問いの間に待たせている別の client のキーがあり、
// settle が終わらないときに使う。
func (h *harness) waitInputHeld(what, want, row string) {
	h.t.Helper()
	h.eventually(what, func() bool {
		q := h.e.activeChoice()
		return q != nil && q.input != nil && q.text == want
	})
	h.e.do(h.e.render)
	if got := h.lastRow(); got != row {
		h.t.Fatalf("%s: 最後の行 = %q, want %q", what, got, row)
	}
}

// outputOf は、fake の拡張機能が出力の欄 channel に書き足した中身を返す。イベントループの goroutine で呼ぶ。
func (h *harness) outputOf(channel string) string {
	return strings.Join(h.e.outputs[outputKey{"fake", channel}], "\n") // 行に分けて持つので、改行でつなぐと書き足した中身に戻る
}

// input/ask は、コマンドラインの行に問いの文を前置きにして初めの中身を出し、: と同じキーとインサートモードの <C-w> と <C-u> で
// 編集させる。<BS>、<Tab>、貼り付け、多バイト文字と全角文字を入れられ、空の中身の <BS> では取り消さない。
// <CR> で確定した中身（空の文字列を含む）と、<Esc> と <C-c> の取り消しを区別して返す。問いの間のキーでバッファを編集しない。
//
// req: FR-275
func TestInputAsk(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")

	h.keys(":fake.input 既定<CR>")
	h.waitInput("初めの中身", "既定", "名前: 既定")
	h.keys("<BS>値 x")
	h.paste("a\nb\x01")
	h.waitInput("編集と貼り付け", "既値 xa b", "名前: 既値 xa b")
	h.keys("<C-w>")
	h.waitInput("<C-w> で 1 語", "既値 xa ", "名前: 既値 xa")
	h.keys("<C-w>")
	h.waitInput("<C-w> で空白と 1 語", "既値 ", "名前: 既値")
	h.keys("<C-u>")
	h.waitInput("<C-u>", "", "名前:")
	h.keys("<BS><BS>")
	h.waitInput("空の <BS> で取り消さない", "", "名前:")
	h.keys("ｘ全角<Tab>y")
	h.waitInput("全角とタブ", "ｘ全角\ty", "名前: ｘ全角    y") // 全角は 2 列ずつ数え、タブ文字は 16 列目まで
	h.keys("<CR>")
	h.eventually("確定した中身", func() bool {
		return h.outputOf("input") == "value=\"ｘ全角\\ty\" cancelled=false err=<nil>\n"
	})
	h.eventually("答えの知らせ", func() bool {
		return h.e.message.Text == "問いに「ｘ全角\ty」と答えました" && h.e.activeChoice() == nil
	})

	// 空の中身の確定と、<Esc> と <C-c> の取り消しを区別する。
	h.keys(`\i`)
	h.waitInput("初めの中身が無い問い", "", "名前:")
	h.keys("<CR>")
	h.eventually("空の確定", func() bool { return strings.HasSuffix(h.outputOf("input"), "value=\"\" cancelled=false err=<nil>\n") })
	h.eventually("空の確定の知らせ", func() bool { return h.e.message.Text == "問いに空の文字列で答えました" })
	h.keys(`\i`)
	h.waitInput("取り消す問い", "", "名前:")
	h.keys("abc<Esc>")
	h.eventually("<Esc> の取り消し", func() bool {
		return strings.HasSuffix(h.outputOf("input"), "value=\"\" cancelled=true err=<nil>\n")
	})
	h.keys(`\i`)
	h.waitInput("<C-c> で取り消す問い", "", "名前:")
	// <C-c> は、先打ちのキーと一緒に送ると先打ちを捨てる中断（#202）になるので、q を処理し終えてから送る。
	h.keys("q")
	h.waitInput("<C-c> の前の入力", "q", "名前: q")
	h.keys("<C-c>")
	h.eventually("<C-c> の取り消し", func() bool {
		return strings.Count(h.outputOf("input"), "cancelled=true") == 2 && h.e.activeChoice() == nil
	})
	h.settle()
	if got := h.text(); got != "abc" {
		t.Errorf("問いの間のキーで編集した: %q", got)
	}
	if h.mode() != command.ModeNormal {
		t.Errorf("mode = %v", h.mode())
	}
}

// 入力の行の中では、<Left>、<Right>、<Home>、<End> でカーソルを動かし、<Del> でカーソルの後の文字を消して、既定値の途中を直せる。
// <BS>、<Del>、<Left>、<Right> は、結合文字を前の文字と一緒に 1 文字として扱う（Vim 9.1 のコマンドラインの既定と同じ）。
// 貼り付けと <C-w> と <C-u> も、カーソルの位置で働く。
//
// req: FR-275
func TestInputAskCursor(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	cursor := func() int {
		var c int
		h.e.do(func() { c = h.e.choices[0].cursor })
		return c
	}
	h.keys(":fake.input --port 5678 -v<CR>")
	h.waitInput("既定値", "--port 5678 -v", "名前: --port 5678 -v")
	// 5678 を 9000 に直す。
	h.keys("<Left><Left><Left><BS><BS><BS><BS>9000")
	h.waitInput("途中を直す", "--port 9000 -v", "名前: --port 9000 -v")
	if got := cursor(); got != len("--port 9000") {
		t.Errorf("cursor = %d", got)
	}
	h.keys("<Home><Del><Del>")
	h.waitInput("<Home> と <Del>", "port 9000 -v", "名前: port 9000 -v")
	h.keys("<Right><Right><Right><Right><C-u>")
	h.waitInput("カーソルの前を <C-u> で消す", " 9000 -v", "名前:  9000 -v")
	h.keys("<End><Left><Left><C-w>")
	h.waitInput("カーソルの前の単語を <C-w> で消す", " -v", "名前:  -v")
	h.paste("x")
	h.waitInput("カーソルの位置に貼り付ける", " x-v", "名前:  x-v")
	h.keys("<End><C-u>")
	// 「か」と結合文字の濁点（U+3099）の「が」を、<BS> と <Del> と <Left> で 1 文字として扱う。
	h.keys("xがy")
	h.waitInput("結合文字", "xがy", "名前: xがy")
	h.keys("<Left><BS>")
	h.waitInput("結合文字ごと <BS> で消す", "xy", "名前: xy")
	h.keys("<BS>が<Left><Del>")
	h.waitInput("結合文字ごと <Del> で消す", "y", "名前: y")
	h.keys("<CR>")
	h.eventually("確定", func() bool { return strings.Contains(h.outputOf("input"), `value="y" cancelled=false`) })
}

// 拡張機能が入力の問いを取り下げたら、キーを待たずに問いを画面から消してメッセージの行に知らせ、問いに向けて打ち続けたキーを、
// <Esc> か <CR> を打つまで編集のコマンドにせずに捨てる（PR #411 のレビューの必須 1。問いの中身の le の後に n(items) を打つと、
// バッファを書き換えていた）。捨てるのは取り下げから短い間（withdrawnDiscardWindow）だけで、問いに何も打っていなければ捨てない。
// 時間が経ってから打った dd と @a は効く（PR #411 の再レビューの推奨 1）。
// 選択肢の問いの取り下げでは、答えになるキー（番号と <Esc>）を 1 つだけ捨てる。
//
// req: FR-275
func TestInputAskWithdrawn(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\ndef\nghi\njkl\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	// 負荷で遅れても捨てる間が切れないように、捨てる間を長くしておく。
	h.e.do(func() { h.e.withdrawWindow = time.Minute })
	h.keys(":fake.inputcancel<CR>")
	h.waitInput("取り下げる問い", "", "式:")
	h.keys("le")
	h.waitInput("打ちかけ", "le", "式: le")
	h.eventually("取り下げ", func() bool { return h.e.withdrawn != nil })
	// キーを打たなくても、取り下げた時点で描き直して問いを消す（PR #411 の再レビューの推奨 2）。
	h.eventually("問いを画面から消す", func() bool {
		return strings.Contains(h.lastRow(), "問い「式:」は拡張機能が取り下げました") && !strings.Contains(h.term.screen(), "式: le")
	})
	h.keys("n(items)")
	h.settle()
	if got := h.text(); got != "abc\ndef\nghi\njkl" {
		t.Errorf("取り下げの後のキーで編集した: %q", got)
	}
	if h.mode() != command.ModeNormal {
		t.Errorf("mode = %v", h.mode())
	}
	if msg := h.message(); !strings.Contains(msg, "問い「式:」は拡張機能が取り下げました") {
		t.Errorf("message = %q", msg)
	}
	h.paste("dd")
	h.keys("<CR>")
	h.settle()
	if msg := h.message(); msg != "取り下げた問いに向けて打ったキーを捨てました" {
		t.Errorf("<CR> の後の message = %q", msg)
	}
	// <CR> の後は、キーを元どおりに処理する。
	h.keys("dd")
	h.settle()
	if got := h.text(); got != "def\nghi\njkl" {
		t.Errorf("捨て終えた後の dd = %q", got)
	}

	// 問いに何も打たないまま取り下げられたら、直後のキーも捨てない。
	h.keys(":fake.inputcancel<CR>")
	h.waitInput("何も打たない問い", "", "式:")
	h.eventually("何も打たない問いの取り下げ", func() bool { return h.e.activeChoice() == nil })
	h.keys("dd")
	h.settle()
	if got := h.text(); got != "ghi\njkl" {
		t.Errorf("何も打たなかった問いの取り下げの後の dd = %q", got)
	}

	// 取り下げから捨てる間が過ぎた後に打った dd と @a は、<Esc> を打たなくても効く。
	h.e.do(func() {
		h.e.withdrawWindow = 100 * time.Millisecond
		h.e.cur.state.Registers().Set('a', command.Register{Text: "x"})
	})
	h.keys(":fake.inputcancel<CR>")
	h.waitInput("打ってから待つ問い", "", "式:")
	h.keys("q")
	h.waitInput("打ちかけ", "q", "式: q")
	h.eventually("待つ問いの取り下げ", func() bool { return h.e.withdrawn != nil })
	time.Sleep(200 * time.Millisecond)
	h.keys("dd@a")
	h.settle()
	if got := h.text(); got != "kl" {
		t.Errorf("時間が経ってから打った dd@a = %q, want kl", got)
	}

	// 選択肢の問いの取り下げの後は、番号のキーを 1 つだけ捨て、次のキーから元どおりに処理する。
	h.e.do(func() { h.e.withdrawWindow = time.Minute })
	h.keys(":fake.inputcancel choice<CR>")
	h.eventually("選択肢の問い", func() bool { q := h.e.activeChoice(); return q != nil && q.choice != nil })
	h.eventually("選択肢の問いの取り下げ", func() bool { return h.e.activeChoice() == nil })
	h.keys("2x")
	h.settle()
	if got := h.text(); got != "l" {
		t.Errorf("選択肢の問いの取り下げの後の 2x = %q, want l（2 を捨てて x だけ）", got)
	}
}

// 問いの答えのキーは、マクロに記録しない（choice/ask と同じ）。問いを出したキー（拡張機能のコマンド）だけが記録に残る。
//
// req: FR-275
func TestInputAskNotRecorded(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	h.keys(`qa\i`)
	h.waitInput("記録中の問い", "", "名前:")
	h.keys("xyz<CR>")
	h.eventually("答え", func() bool { return strings.Contains(h.outputOf("input"), `value="xyz"`) })
	h.keys("q")
	h.settle()
	var got string
	h.e.do(func() {
		r, _ := h.e.cur.state.Registers().Get('a')
		got = r.Text
	})
	if got != `\i` {
		t.Errorf("レジスター a = %q, want %q", got, `\i`)
	}
}

// choice/ask と input/ask は 1 本の列に届いた順に並べ、一度に 1 つだけ出す。続けて届いた問いには、何件目かと、
// 前の問いへの答えと待たせている数を出す（choice/ask の #151 と同じ）。
//
// req: FR-275
func TestInputAskQueue(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t), TabLine: ui.TabLineMultiple}, "a.txt")
	h.keys(`\m`)
	h.eventually("3 つの問い", func() bool { return len(h.e.choices) == 3 && h.e.activeChoice().choice != nil })
	h.settle()
	h.screenHas("選択肢の問いが先", "\nこの後に 2 件の問いが待っています\nどれ?\n[1] A\n[2] B\n問い 1: 1〜2 で選ぶか <Esc> で取り消し")
	h.keys("2")
	h.waitInput("2 件目の入力の問い", "x", "問い 2: 式: x")
	h.screenHas("2 件目の知らせ", "\n問い 1 に「B」と答えました\nこの後に 1 件の問いが待っています\n問い 2: 式: x")
	h.keys("+1<CR>")
	h.waitInput("3 件目の入力の問い", "", "問い 3: 値:")
	h.screenHas("3 件目の知らせ", "\n問い 2 に「x+1」と答えました\n問い 3: 値:")
	h.keys("<Esc>")
	want := "choice selected=b cancelled=false err=<nil>\n" +
		"input1 value=\"x+1\" cancelled=false err=<nil>\n" +
		"input2 value=\"\" cancelled=true err=<nil>\n"
	var got string
	h.eventually("拡張機能へ返った答え", func() bool { got = h.outputOf("ask"); return strings.Count(got, "\n") == 3 })
	if got != want {
		t.Errorf("答え = %q, want %q", got, want)
	}
	h.eventually("最後の答え", func() bool {
		return h.e.message.Text == "問い 3 を取り消しました" && h.e.activeChoice() == nil
	})
	if got := h.text(); got != "abc" {
		t.Errorf("問いの間の入力で編集した: %q", got)
	}
}

// 入力の問いを出している間は、他の client のキーを処理せずに待たせ、待っている理由を出す。答えた後に待たせたキーを処理する。
// 他の client のキーは、入力の中身にも入れない。
//
// req: FR-275
func TestInputAskOtherClient(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n", "b.txt": "bb\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	h.e.do(func() { h.e.clients[0].info.ID = 1 })
	b := h.attach(ClientInfo{ID: 2, Files: []string{filepath.Join(dir, "b.txt")}})
	h.keys(`\i`)
	h.eventually("A の問い", func() bool { q := h.e.activeChoice(); return q != nil && q.input != nil })
	b.keys("x")
	h.keys("ok")
	h.waitInputHeld("A の入力", "ok", "名前: ok")
	time.Sleep(50 * time.Millisecond)
	if got := h.bufferText("b.txt"); got != "bb" {
		t.Fatalf("問いの間に B のキーを処理した: b.txt = %q", got)
	}
	h.eventually("B に待つ理由", func() bool {
		return strings.Contains(b.term.screen(), "client 1 の問いの答えを待っています")
	})
	h.keys("<CR>")
	h.eventually("A の答え", func() bool { return strings.Contains(h.outputOf("input"), `value="ok" cancelled=false`) })
	deadline := time.Now().Add(10 * time.Second)
	for h.bufferText("b.txt") != "b" {
		if time.Now().After(deadline) {
			t.Fatalf("待たせていた B の x を処理しない: b.txt = %q", h.bufferText("b.txt"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 問いを出した拡張機能が答えを待つ間に終わったら、その問いを取り下げて知らせる。問いに向けて打ったキーを <Esc> まで捨てた後は、
// キーを元どおりに処理する。
//
// req: FR-275
func TestInputAskExtensionExit(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	h.e.do(func() { h.e.withdrawWindow = time.Minute })
	h.keys(`\x`)
	h.waitInput("終わる拡張機能の問い", "d", "終わる: d")
	h.keys("e")
	h.waitInput("終わる前に打つ", "de", "終わる: de")
	h.eventually("問いを取り下げる", func() bool { return h.e.activeChoice() == nil && h.e.asker == nil })
	h.settle()
	// 知らせは、拡張機能の異常終了の知らせと前後するので、後に出た方がメッセージの行に残る。捨てる状態に入ったことを確かめる。
	h.eventually("問いを消して捨て始める", func() bool {
		return !strings.Contains(h.term.screen(), "終わる: de") && h.e.withdrawn != nil && h.e.withdrawn.input
	})
	// 異常終了の知らせが届いてから打つ（捨てたキーで取り下げの知らせを出し直すことを確かめるため）。
	h.eventually("異常終了の知らせ", func() bool {
		return slices.ContainsFunc(h.e.msgHistory, func(m ui.Message) bool { return strings.Contains(m.Text, "異常終了") })
	})
	h.keys("x")
	h.settle()
	if msg := h.message(); !strings.Contains(msg, "問い「終わる:」は拡張機能が取り下げました") {
		t.Errorf("捨てたキーの後の message = %q", msg)
	}
	h.keys("<Esc>")
	h.settle()
	if got := h.text(); got != "abc" {
		t.Errorf("取り下げた問いに向けた x で編集した: %q", got)
	}
	h.keys("x")
	h.settle()
	if got := h.text(); got != "bc" {
		t.Errorf("捨て終えた後の x = %q, want bc", got)
	}
}

// ペインの部品の client のキーから頼んだ問いは、そのペインの画面にも出し、ペインで打ったキーで答えられる（PR #411 の裁定要 1。
// 端末多重化ソフトでペインを拡大していても問いが見える）。ペインを閉じた後に頼んだ問いは、元の client の画面にだけ出す。
//
// req: FR-275
func TestInputAskPane(t *testing.T) {
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t)}
	calls.config(&cfg)
	h := start(t, cfg)
	peer := h.attach(ClientInfo{ID: 2, Pane: "%9"})
	peer.keys(":fake.pane list<CR>")
	h.eventually("OpenPane", func() bool { _, ok := calls.last(true); return ok })
	call, _ := calls.last(true)
	h.waitPaneOpened(call.view)
	comp := h.attachComponent(call.view, true)
	comp.waitScreen("close me")

	comp.keys("zw")
	h.eventually("ペインからの問い", func() bool { q := h.e.activeChoice(); return q != nil && q.input != nil })
	comp.keys("len(items)")
	h.eventually("ペインの画面の最後の行", func() bool {
		lines := strings.Split(comp.term.screen(), "\n")
		return strings.TrimRight(lines[len(lines)-1], " ") == "ウォッチ式: len(items)"
	})
	if !strings.Contains(peer.term.screen(), "ウォッチ式: len(items)") {
		t.Errorf("元の client の画面に問いが無い:\n%s", peer.term.screen())
	}
	comp.keys("<CR>")
	h.eventually("ペインで打った答え", func() bool { return strings.Contains(h.outputOf("input"), `value="len(items)" cancelled=false`) })

	// 元の client のキーから頼んだ問いは、ペインの画面には出さない。
	peer.keys(`:fake.input<CR>`)
	h.eventually("元の client の問い", func() bool { q := h.e.activeChoice(); return q != nil && q.input != nil })
	h.eventually("元の client の画面の問い", func() bool { return strings.Contains(peer.term.screen(), "名前:") })
	h.e.do(h.e.render)
	if strings.Contains(comp.term.screen(), "名前:") {
		t.Errorf("元の client から頼んだ問いがペインに出た:\n%s", comp.term.screen())
	}
	peer.keys("<Esc>")
	h.eventually("取り消し", func() bool { return h.e.activeChoice() == nil })
}

// 設定 commandLine.popup が真なら、入力の問いを画面の上の中央のポップアップの枠に出し、枠の題を commandLine.title.input にする。
// 端末多重化ソフトの中で中央のポップアップを開いている（部品の client が描いている）ときは、部品の client の画面にも問いを出し、
// 部品の client で打ったキーで答えられる。
//
// req: FR-275
func TestInputAskPopupAndComponent(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n"})
	term := newFakeTerm()
	term.w, term.h = 80, 24
	var calls muxCalls
	cfg := Config{
		WorkDir: dir, Extensions: fakeSpecs(t),
		Options: map[string]option.Value{option.CommandLinePopup: option.BoolValue(true)},
		Symbols: ui.Symbols{ui.SymbolPopupBorder: "╭─╮│╯─╰│"},
	}
	calls.config(&cfg)
	h := startTerm(t, term, cfg, filepath.Join(dir, "a.txt"))
	h.settle()
	h.keys(":fake.input 全角の既定<CR>")
	h.eventually("枠の中の入力", func() bool { return strings.Contains(h.screenRow(3), "│ 名前: 全角の既定 ") })
	if got := h.screenRow(2); !strings.Contains(got, "─ Input ─") {
		t.Errorf("枠の題の行 = %q", got)
	}
	if got := h.screenRow(23); got != "" {
		t.Errorf("問いの間の最後の行 = %q", got)
	}
	h.keys("<CR>")
	h.eventually("答え", func() bool { return strings.Contains(h.outputOf("input"), `value="全角の既定"`) })

	// 端末多重化ソフトの中の client が中央のポップアップを開き、部品の client が繋いだ後に問いが届く。
	peer := h.attach(ClientInfo{ID: 2, Pane: "%9"})
	peer.keys(":fake.popup list<CR>")
	h.eventually("OpenPopup", func() bool { _, ok := calls.last(false); return ok })
	call, _ := calls.last(false)
	comp := h.attachComponent(call.view, false)
	comp.waitScreen("close me")
	peer.keys(`:fake.input<CR>`)
	h.eventually("peer の問い", func() bool { q := h.e.activeChoice(); return q != nil && q.input != nil })
	comp.waitScreen("名前:")
	comp.keys("中")
	comp.waitScreen("名前: 中")
	comp.keys("<CR>")
	h.eventually("部品の client で打った答え", func() bool { return strings.Contains(h.outputOf("input"), `value="中"`) })
}

// passAskGap は、問いに答えた後にキーを取っておく間（askGap）を、時間が過ぎたときと同じく終える。
// 負荷で拡張機能の往復が遅れても切れないように間を長くしたテストが、その後のキーを処理させるのに使う。
func (h *harness) passAskGap() {
	h.t.Helper()
	h.e.do(func() {
		if g := h.e.askGap; g != nil {
			h.e.endAskGap(g)
		}
	})
}

// keysQueued は、Vim の表記のキーの列を、イベントループを止めている間にすべて列へ積ませてから処理させる。
// 2 行の貼り付けや tmux send-keys のように、先のキーを処理する時点で後のキーがもう列にある場面を、負荷に依らず作るために使う。
func (h *harness) keysQueued(s string) {
	h.t.Helper()
	h.e.do(func() {
		h.keys(s)
		deadline := time.Now().Add(10 * time.Second)
		for h.e.forwarded.Load() != h.sent {
			if time.Now().After(deadline) {
				h.t.Errorf("キーを列に積み終えない")
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
}

// waitBuffer は、ファイル名が name のバッファのテキストが want になるまで待つ（bufferText は eventually の中で呼べない）。
func (h *harness) waitBuffer(what, name, want string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for h.bufferText(name) != want {
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: %s = %q, want %q", what, name, h.bufferText(name), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 拡張機能が問いを続けて聞くとき、1 つ目の答えと一緒に列に届いていたキー（2 行の貼り付けと先打ち）は、
// バッファの編集にせずに取っておき、2 つ目の問いの入力にする（#420。PR #418 のレビューの推奨 1 の再現 1）。
// 2 つ目が選択肢の問いでも同じく、取っておいたキーを答えにする。
//
// req: FR-308
func TestInputAskTypeahead(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	// 負荷で拡張機能の往復が遅れても取っておく間が切れないように、間を長くしておく。
	h.e.do(func() { h.e.askGapWindow = time.Minute })
	h.keys(":fake.inputseq<CR>")
	h.waitInput("1 つ目の問い", "", "モジュール:")
	// 1 つ目の答えと 2 つ目の答えを、1 回で続けて送る（tmux send-keys 'pkg.worker' Enter 'a b' Enter と同じ）。
	h.keysQueued("pkg.worker<CR>a b<CR>")
	want := "module value=\"pkg.worker\" cancelled=false err=<nil>\n" + "args value=\"a b\" cancelled=false err=<nil>\n"
	h.eventually("2 つの答え", func() bool { return h.outputOf("seq") == want })
	h.settle()
	if got := h.text(); got != "abc" {
		t.Errorf("問いの間に打ったキーで編集した: %q", got)
	}
	if h.mode() != command.ModeNormal {
		t.Errorf("mode = %v", h.mode())
	}

	h.keys(":fake.inputseq choice<CR>")
	h.waitInput("選択肢が続く 1 つ目の問い", "", "モジュール:")
	h.keysQueued("m<CR>2")
	want += "module value=\"m\" cancelled=false err=<nil>\n" + "choice selected=b cancelled=false err=<nil>\n"
	h.eventually("選択肢の答え", func() bool { return h.outputOf("seq") == want })
	h.settle()
	if got := h.text(); got != "abc" {
		t.Errorf("選択肢の問いの前に打った 2 で編集した: %q", got)
	}
}

// 問いが 1 つだけで、答えと一緒に列に届いていたキーは、取っておく間（askGapWindow）が過ぎるまで処理せず、
// 過ぎた後にふつうのキーとして処理する。答えの時点で列にキーが無ければ（手で打つキー）、取っておかずにすぐ処理する
// （PR #422 のレビューの裁定要 1）。取り消し（<Esc>）の後も取っておかない。
//
// req: FR-308
func TestInputAskTypeaheadSingle(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\ndef\nghi\njkl\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	h.e.do(func() { h.e.askGapWindow = time.Minute })
	h.keys(`\i`)
	h.waitInput("1 つだけの問い", "", "名前:")
	h.keysQueued("v<CR>dd")
	h.eventually("答え", func() bool { return strings.Contains(h.outputOf("input"), `value="v" cancelled=false`) })
	h.eventually("取っておく間", func() bool { return h.e.askGap != nil && h.e.keys.has(h.e.askGap.c) })
	if got := h.text(); got != "abc\ndef\nghi\njkl" {
		t.Errorf("取っておく間に dd を処理した: %q", got)
	}
	h.passAskGap()
	h.settle()
	if got := h.text(); got != "def\nghi\njkl" {
		t.Errorf("間が過ぎた後の dd = %q, want def\\nghi\\njkl", got)
	}

	// 答えを処理し終えてから打った dd は、取っておかない（間が 1 分なので、取っておけば settle が終わらない）。
	h.keys(`\i`)
	h.waitInput("手で打つ問い", "", "名前:")
	h.keys("w<CR>")
	h.eventually("手で打った答え", func() bool { return strings.Contains(h.outputOf("input"), `value="w" cancelled=false`) })
	h.settle()
	h.e.do(func() {
		if h.e.askGap != nil {
			t.Errorf("列が空なのに取っておく間を始めた")
		}
	})
	h.keys("dd")
	h.settle()
	if got := h.text(); got != "ghi\njkl" {
		t.Errorf("答えの後に打った dd = %q, want ghi\\njkl", got)
	}

	h.keys(`\i`)
	h.waitInput("取り消す問い", "", "名前:")
	h.keysQueued("<Esc>dd")
	h.settle()
	if got := h.text(); got != "jkl" {
		t.Errorf("取り消しと一緒に送った dd = %q, want jkl", got)
	}
}

// 取っておくのは、問いに答えた client のキーだけで、ほかの client のキーは取っておく間も止めない（PR #422 のレビューの推奨 1）。
//
// req: FR-308
func TestInputAskTypeaheadOtherClient(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\ndef\n", "b.txt": "bb\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	h.e.do(func() {
		h.e.clients[0].info.ID = 1
		h.e.askGapWindow = time.Minute
	})
	b := h.attach(ClientInfo{ID: 2, Files: []string{filepath.Join(dir, "b.txt")}})
	h.keys(`\i`)
	h.waitInput("A の問い", "", "名前:")
	h.keysQueued("v<CR>dd")
	h.eventually("A の取っておく間", func() bool { return h.e.askGap != nil })
	b.keys("x")
	h.waitBuffer("取っておく間の B の x", "b.txt", "b")
	if got := h.bufferText("a.txt"); got != "abc\ndef" {
		t.Errorf("取っておく間に A の dd を処理した: %q", got)
	}
	h.passAskGap()
	h.waitBuffer("間の後の A の dd", "a.txt", "def")
}

// 拡張機能の一覧の項目を選んだ直後に列に届いていたキーも、拡張機能が続けて聞く問いの入力にする（PR #422 のレビューの推奨 2。
// DAP の構成の一覧の後の inputs）。選んだ拡張機能がファイルを開いたら（ファイラ）、取っておいたキーを、間を待たずに
// 開いたファイルへのふつうのキーとして処理する。
//
// req: FR-308
func TestInputAskTypeaheadAfterSelect(t *testing.T) {
	opened := fakeOpenedPath(os.Getpid())
	if err := os.WriteFile(opened, []byte("x\ny\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(opened) })
	dir := writeFiles(t, map[string]string{"a.txt": "abc\ndef\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	h.e.do(func() { h.e.askGapWindow = time.Minute })
	h.keys(":fake.popup asklist<CR>")
	h.eventually("問いを続ける一覧", func() bool { return h.e.float != nil && h.e.float.ext == "fake" })
	h.keysQueued("<CR>a b<CR>")
	h.eventually("一覧の後の問いの答え", func() bool { return h.outputOf("seq") == "args value=\"a b\" cancelled=false err=<nil>\n" })
	h.settle()
	if got := h.bufferText("a.txt"); got != "abc\ndef" {
		t.Errorf("一覧で選んだ後のキーで編集した: %q", got)
	}

	h.keys("<Esc>:fake.popup files<CR>")
	h.eventually("ファイルの一覧", func() bool { return h.e.float != nil && h.e.float.title == "files" })
	h.keysQueued("gg<CR>dd")
	// 間は 1 分なので、editor/open で間を終えなければ、ここは待ちきれない。
	h.waitBuffer("開いたファイルへの dd", filepath.Base(opened), "y")
	if got := h.bufferText("a.txt"); got != "abc\ndef" {
		t.Errorf("ファイルの一覧で選んだ後の dd を元のファイルへ: %q", got)
	}
}
