package editor

import (
	"path/filepath"
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

// waitInput は、1 行の入力の問いを出し、その中身が want になり、最後の行に row が描かれるまで待つ。
func (h *harness) waitInput(what, want, row string) {
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

// 拡張機能が入力の問いを取り下げたら、そのことをメッセージの行に知らせ、問いに向けて打ち続けたキーを、<Esc> か <CR> を打つまで
// 編集のコマンドにせずに捨てる（PR #411 のレビューの必須 1。問いの中身の le の後に n(items) を打つと、バッファを書き換えていた）。
// 選択肢の問いの取り下げでは、答えになるキー（番号と <Esc>）を 1 つだけ捨てる。
//
// req: FR-275
func TestInputAskWithdrawn(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\ndef\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	h.keys(":fake.inputcancel<CR>")
	h.waitInput("取り下げる問い", "", "式:")
	h.keys("le")
	h.waitInput("打ちかけ", "le", "式: le")
	h.eventually("取り下げ", func() bool { return h.e.activeChoice() == nil })
	h.keys("n(items)")
	h.settle()
	if got := h.text(); got != "abc\ndef" {
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
	if got := h.text(); got != "def" {
		t.Errorf("捨て終えた後の dd = %q", got)
	}

	// 選択肢の問いの取り下げの後は、番号のキーを 1 つだけ捨て、次のキーから元どおりに処理する。
	h.keys(":fake.inputcancel choice<CR>")
	h.eventually("選択肢の問い", func() bool { q := h.e.activeChoice(); return q != nil && q.choice != nil })
	h.eventually("選択肢の問いの取り下げ", func() bool { return h.e.activeChoice() == nil })
	h.keys("2x")
	h.settle()
	if got := h.text(); got != "ef" {
		t.Errorf("選択肢の問いの取り下げの後の 2x = %q, want ef（2 を捨てて x だけ）", got)
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
	h.waitInput("A の入力", "ok", "名前: ok")
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
	h.keys(`\x`)
	h.waitInput("終わる拡張機能の問い", "d", "終わる: d")
	h.keys("e")
	h.eventually("問いを取り下げる", func() bool { return h.e.activeChoice() == nil && h.e.asker == nil })
	h.settle()
	// 知らせは、拡張機能の異常終了の知らせと前後するので、後に出た方がメッセージの行に残る。捨てる状態に入ったことを確かめる。
	h.eventually("問いを消して捨て始める", func() bool {
		return !strings.Contains(h.term.screen(), "終わる: d") && h.e.withdrawn != nil && h.e.withdrawn.input
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
