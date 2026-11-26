package editor

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// createdBuffer は、タイトルが title のバッファの、編集の禁止、タブの持ち主の client の ID（タブでなければ 0）、その数を返す。
// 複数あれば最後のものを返す。
func (h *harness) createdBuffer(title string) (readOnly bool, owner ClientID, n int) {
	h.e.do(func() {
		for _, bs := range h.e.bufs {
			if bs.title == title {
				readOnly, n = bs.buf.ReadOnly(), n+1
				owner = 0
				if bs.owner != nil {
					owner = bs.owner.info.ID
				}
			}
		}
	})
	return readOnly, owner, n
}

// waitSplit は、SplitPane の呼び出しを待って返す。
func waitSplit(t *testing.T, calls <-chan diffCall) diffCall {
	t.Helper()
	select {
	case c := <-calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("ペインを開かない")
	}
	return diffCall{}
}

// 拡張機能は、buffer/create で編集を禁止したファイルでないバッファを作り（FR-88）、diff/start でファイルのタブと見比べさせられる（FR-99）。
// 作ったバッファはタブでないので、本体はファイルのタブの client のペインを分割し、ファイルを渡さずに起動した新しい client のタブにしてから
// 見比べを始める。新しい client は、分割のときに渡した印（EnvSplit）で見分け、印の無い client には渡さない。
// 待っている間の次の diff/start は断り、そのとき作ったバッファは閉じる（FR-64）。
// git 連携の拡張機能（FR-104）が HEAD の版と見比べるのに使う。
//
// req: FR-64, FR-88, FR-99
func TestDiffStartFromExtension(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\ntwo\n"})
	calls := make(chan diffCall, 2)
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t), SplitPane: recordSplit(calls)}, "a.txt")
	h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })
	var canSplit bool
	h.e.do(func() { canSplit = h.e.editorState().CanSplit })
	if !canSplit {
		t.Error("editor/state の canSplit が false")
	}

	h.keys(":fake.headdiff<CR>")
	h.waitMessage("headdiff buf=")
	if msg := h.message(); !strings.Contains(msg, "split=true err=<nil>") {
		t.Fatalf("message = %q", msg)
	}
	call := waitSplit(t, calls)
	// ファイルを渡さずに、ファイルのタブの client のペインを分割する。タブでないバッファが B（右の側）なので、右に開く。
	if want := (diffCall{"%1", "", dir, "1", false}); call != want {
		t.Errorf("SplitPane = %+v, want %+v", call, want)
	}
	readOnly, owner, n := h.createdBuffer("HEAD:x")
	if n != 1 || !readOnly || owner != 0 {
		t.Fatalf("作ったバッファ: n=%d readOnly=%v owner=%d", n, readOnly, owner)
	}

	// 待っている間の次の diff/start は断り、そのとき作ったバッファを閉じる。
	h.keys(":fake.headdiff<CR>")
	h.waitMessage("待っています")
	if _, _, n := h.createdBuffer("HEAD:x"); n != 1 {
		t.Errorf("断った diff/start のバッファが残った: HEAD:x が %d 個", n)
	}

	// 印の無い client（同じ window で別に起動したもの）には渡さない。
	other := h.attach(ClientInfo{ID: 3, Pane: "%3", WorkDir: dir})
	h.settle()
	if _, owner, _ := h.createdBuffer("HEAD:x"); owner != 0 {
		t.Fatalf("印の無い client 3 が作ったバッファを取った")
	}
	other.keys(":q<CR>")
	h.settle()

	// 印の同じ client は、名前の無い空のタブの代わりに、作ったバッファをタブにする。
	b := h.attach(ClientInfo{ID: 2, Pane: "%2", WorkDir: dir, Env: call.env()})
	h.settle()
	if _, owner, _ := h.createdBuffer("HEAD:x"); owner != 2 {
		t.Fatalf("作ったバッファの持ち主 = %d, want 2", owner)
	}
	h.e.do(func() {
		c := h.e.clients[len(h.e.clients)-1]
		if len(c.tabs) != 1 {
			t.Errorf("client 2 のタブ = %d, want 1", len(c.tabs))
		}
		for _, kv := range c.info.Env {
			if strings.HasPrefix(kv, EnvSplit+"=") {
				t.Errorf("印が client の環境に残った: %q", c.info.Env)
			}
		}
	})
	if n := h.diffSessions(); n != 1 {
		t.Fatalf("見比べの組 = %d, want 1", n)
	}
	h.e.do(func() {
		s := h.e.diff.sessions[0]
		if s.bs[0].path == "" || s.bs[1].title != "HEAD:x" {
			t.Errorf("組 = %q と %q, want a.txt と HEAD:x", s.bs[0].path, s.bs[1].title)
		}
		if got := s.view[0].Lines[1].Style; got != "diff.change" {
			t.Errorf("a.txt の 2 行目 = %q, want diff.change", got)
		}
	})

	// 編集を禁止した HEAD の版へは dp で送れず（E21）、do で取り込める（基本設計 6.8）。
	h.keys("jdp")
	h.settle()
	if msg := h.message(); !strings.HasPrefix(msg, "E21") {
		t.Errorf("HEAD の版への dp = %q", msg)
	}
	h.keys("do")
	h.settle()
	if got := h.bufferText("a.txt"); got != "one\nTWO" {
		t.Errorf("do の後の a.txt = %q", got)
	}
	// HEAD の版の側でも編集は断る。
	b.keys("x")
	h.settle()
	var head string
	h.e.do(func() { head = h.e.diff.sessions[0].bs[1].buf.Text() })
	if head != "one\nTWO" {
		t.Errorf("HEAD の版 = %q", head)
	}

	// 同じ client の 2 つのタブは見比べられない。
	h.keys(fmt.Sprintf(":e %s/b.txt<CR>:fake.headdiff %d<CR>", dir, 1))
	h.waitMessage("同じ client のタブです")
	// どちらもタブでなければ断り、作った 2 つのバッファを閉じる。
	h.keys(":fake.headdiff notab<CR>")
	h.waitMessage("どちらもタブではありません")
	if _, _, n := h.createdBuffer("other"); n != 0 {
		t.Errorf("断った diff/start のバッファ other が %d 個残った", n)
	}
	if _, _, n := h.createdBuffer("HEAD:x"); n != 1 {
		t.Errorf("HEAD:x が %d 個", n)
	}
}

// diff/start で、タブでないバッファが A（見比べの左の側）なら、タブの client のペインの左に新しいペインを開き、
// そのバッファを左の側にして見比べを始める（#437。git 連携の拡張機能は、比べる元の版を A にする）。
//
// req: FR-99
func TestDiffStartBaseLeft(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\ntwo\n"})
	calls := make(chan diffCall, 1)
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t), SplitPane: recordSplit(calls)}, "a.txt")
	h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })

	h.keys(":fake.headdiff base<CR>")
	h.waitMessage("headdiff buf=")
	if msg := h.message(); !strings.Contains(msg, "split=true err=<nil>") {
		t.Fatalf("message = %q", msg)
	}
	call := waitSplit(t, calls)
	if want := (diffCall{"%1", "", dir, "1", true}); call != want {
		t.Errorf("SplitPane = %+v, want %+v（左に開く）", call, want)
	}
	h.attach(ClientInfo{ID: 2, Pane: "%2", WorkDir: dir, Env: call.env()})
	h.settle()
	if n := h.diffSessions(); n != 1 {
		t.Fatalf("見比べの組 = %d, want 1", n)
	}
	h.e.do(func() {
		s := h.e.diff.sessions[0]
		if s.bs[0].title != "HEAD:x" || s.bs[1].path != filepath.Join(dir, "a.txt") {
			t.Errorf("組 = %q と %q, want HEAD:x と a.txt", s.bs[0].title, s.bs[1].path)
		}
		// フォーカスは右のファイルのペインに残るので、キーを打つ前でも、editor/state はファイルのバッファを返す（PR #438 のレビューの推奨 1）。
		if got := h.e.editorState().Buffer; got != s.bs[1].id {
			t.Errorf("左に開いた直後の editor/state の buffer = %d, want a.txt の %d", got, s.bs[1].id)
		}
		if h.e.op != s.bs[1].owner {
			t.Errorf("操作中の client = client %d, want a.txt の client", h.e.op.info.ID)
		}
	})
}

// buffer/create で作った拡張機能は、buffer/update で、編集を禁止したバッファの中身を置き換え、buffer/dispose で閉じられる。
// 他のバッファ（利用者が開いたファイル）には送れない。
func TestCreatedBufferUpdateDispose(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	// 端末多重化ソフトの外なので diff/start は断られ、作ったバッファは閉じる。update の相手にはファイルでないバッファを別に作る。
	h.keys(":fake.headdiff<CR>")
	h.waitMessage("headdiff buf=")
	if _, _, n := h.createdBuffer("HEAD:x"); n != 0 {
		t.Fatalf("見比べの始まらなかったバッファが %d 個残った", n)
	}
	var id uint64
	h.e.do(func() {
		bs := h.e.newBuffer(buffer.New("x"), "", "")
		bs.title, bs.creator = "made", "fake"
		bs.buf.SetReadOnly(true)
		id = uint64(bs.id)
	})
	h.keys(fmt.Sprintf(`:fake.headdiff update %d alpha\nbeta<CR>`, id))
	h.waitMessage("headdiff update")
	if msg := h.message(); !strings.Contains(msg, "err=<nil>") {
		t.Fatalf("update: %q", msg)
	}
	var text string
	var ro bool
	h.e.do(func() {
		bs := h.e.findBuffer(ext.BufferID(id))
		text, ro = bs.buf.Text(), bs.buf.ReadOnly()
	})
	if text != "alpha\nbeta" || !ro {
		t.Errorf("置き換えた後 = %q readOnly=%v", text, ro)
	}
	h.keys(":fake.headdiff update 1 x<CR>")
	h.waitMessage("not created by this extension")
	if got := h.bufferText("a.txt"); got != "one" {
		t.Errorf("a.txt = %q", got)
	}
	h.keys(fmt.Sprintf(":fake.headdiff dispose %d<CR>", id))
	h.waitMessage("headdiff dispose err=<nil>")
	if _, _, n := h.createdBuffer("made"); n != 0 {
		t.Errorf("dispose の後に %d 個残った", n)
	}
	h.keys(":fake.headdiff dispose 1<CR>")
	h.waitMessage("not created by this extension")
}

// buffer/dispose で閉じるバッファが最後の client の最後のタブなら、名前の無い空のバッファのタブに替え、client も server も終わらない。
// 未保存の変更のある、どのタブでもないバッファ（端末が閉じた client の残したもの）も残る（PR #382 の再レビューの必須）。
//
// req: FR-64
func TestCreatedBufferDisposeLastWindow(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	h.keys("Iunsaved <Esc>")
	h.eventually("a.txt を変える", func() bool { return h.e.cur.buf.Text() == "unsaved one" && h.e.cur.buf.Modified() })
	var id uint64
	h.e.do(func() {
		// 左のペインの client が端末を閉じて、未保存の a.txt を、どのタブでもないバッファとして残した形にする（loseClient）。
		a := h.e.cur
		bs := h.e.newBuffer(buffer.New("base"), "", "")
		bs.title, bs.creator = "origin/main...HEAD:a.txt", "fake"
		bs.buf.SetReadOnly(true)
		h.e.enter(bs)
		c := h.e.view
		c.tabs = slices.DeleteFunc(c.tabs, func(o *bufferState) bool { return o == a })
		a.owner = nil
		id = uint64(bs.id)
	})
	h.keys(fmt.Sprintf(":fake.headdiff dispose %d<CR>", id))
	h.waitMessage("headdiff dispose err=<nil>")
	var clients, tabs int
	var quit, unnamed bool
	h.e.do(func() {
		clients, tabs, quit = len(h.e.clients), len(h.e.view.tabs), h.e.quit
		unnamed = h.e.cur.path == "" && h.e.cur.title == "" && h.e.cur.buf.Text() == ""
	})
	if clients != 1 || tabs != 1 || quit || !unnamed {
		t.Errorf("dispose の後: clients=%d tabs=%d quit=%v 名前の無い空のバッファ=%v", clients, tabs, quit, unnamed)
	}
	if got := h.bufferText("a.txt"); got != "unsaved one" {
		t.Errorf("a.txt = %q", got)
	}
	if _, _, n := h.createdBuffer("origin/main...HEAD:a.txt"); n != 0 {
		t.Errorf("閉じたバッファが %d 個残った", n)
	}
	// 他の client が残っていれば（見比べのペイン）、最後のタブを閉じた client は :q と同じく終わる。この経路は
	// cmd/vi-imitation-ext-git の TestChangesInTmux が実物の tmux で確かめる。
}

// 端末多重化ソフトの外では、diff/start で分割できないので断り（基本設計 6.8）、作ったバッファを閉じる（FR-64）。
// editor/state の canSplit は false になる。
//
// req: FR-64, FR-99
func TestDiffStartOutsideMux(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt")
	var canSplit bool
	h.e.do(func() { canSplit = h.e.editorState().CanSplit })
	if canSplit {
		t.Error("端末多重化ソフトの外で canSplit が true")
	}
	for range 2 {
		h.keys(":fake.headdiff<CR>")
		h.waitMessage("headdiff buf=")
		if msg := h.message(); !strings.Contains(msg, "端末多重化ソフト") || !strings.Contains(msg, "split=false") {
			t.Errorf("message = %q", msg)
		}
		h.e.do(func() { h.e.message = infoMessage("") })
	}
	if _, _, n := h.createdBuffer("HEAD:x"); n != 0 {
		t.Errorf("隠れたバッファが %d 個残った", n)
	}
	if n := h.diffSessions(); n != 0 {
		t.Errorf("組 = %d", n)
	}

	// 他の拡張機能が作ったバッファを diff/start に渡して断られても、そのバッファは閉じない（PR #247 の再レビューの推奨 1）。
	var id uint64
	h.e.do(func() {
		bs := h.e.newBuffer(buffer.New("x"), "", "")
		bs.title, bs.creator = "others", "another"
		id = uint64(bs.id)
	})
	h.keys(fmt.Sprintf(":fake.headdiff %d<CR>", id))
	h.waitMessage("端末多重化ソフト")
	if _, _, n := h.createdBuffer("others"); n != 1 {
		t.Errorf("他の拡張機能が作ったバッファを閉じた（残りは %d 個）", n)
	}
}

// ペインを分割できなかったら、待ちを捨て、作ったバッファを閉じる（FR-64）。
//
// req: FR-64
func TestDiffStartSplitFails(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t), SplitPane: func(string, string, string, string, bool) error {
		return errors.New("split failed")
	}}, "a.txt")
	h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })
	h.keys(":fake.headdiff<CR>")
	h.waitMessage("見比べのペインを開けません")
	h.eventually("作ったバッファを閉じる", func() bool {
		for _, bs := range h.e.bufs {
			if bs.title == "HEAD:x" {
				return false
			}
		}
		return h.e.diff.pending == nil
	})
}

// gatedSplit は、呼ばれた引数を calls へ送り、release から値を受け取るまで戻らない Config.SplitPane の偽物を返す。
// 端末多重化ソフトの層の分割が、入れ替えとフォーカスの移し直し（herdr app では mod の結果）を待って戻るのを真似る。
func gatedSplit(calls chan<- diffCall, release <-chan error) func(from, file, dir, token string, left bool) error {
	return func(from, file, dir, token string, left bool) error {
		calls <- diffCall{from, file, dir, token, left}
		return <-release
	}
}

// diff/start の Wait（#447）は、分割（Config.SplitPane）が戻り、かつ新しい client が繋いで見比べが始まってから応答する。
// どちらが先でも同じで、片方だけでは応答しない。
//
// req: FR-317
func TestDiffStartWait(t *testing.T) {
	for _, attachFirst := range []bool{true, false} {
		name := "split first"
		if attachFirst {
			name = "attach first"
		}
		t.Run(name, func(t *testing.T) {
			dir := writeFiles(t, map[string]string{"a.txt": "one\ntwo\n"})
			calls := make(chan diffCall, 1)
			release := make(chan error, 1)
			h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t), SplitPane: gatedSplit(calls, release)}, "a.txt")
			h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })

			h.keys(":fake.headdiff wait base<CR>")
			call := waitSplit(t, calls)
			if !call.left {
				t.Errorf("SplitPane = %+v, want 左に開く", call)
			}
			notYet := func(what string) {
				t.Helper()
				h.settle()
				time.Sleep(200 * time.Millisecond)
				if msg := h.message(); strings.Contains(msg, "headdiff buf=") {
					t.Fatalf("%s で応答した: %q", what, msg)
				}
			}
			if attachFirst {
				h.attach(ClientInfo{ID: 2, Pane: "%2", WorkDir: dir, Env: call.env()})
				notYet("client が繋いだだけ（分割が戻る前）")
				release <- nil
			} else {
				release <- nil
				notYet("分割が戻っただけ（client が繋ぐ前）")
				h.attach(ClientInfo{ID: 2, Pane: "%2", WorkDir: dir, Env: call.env()})
			}
			h.waitMessage("headdiff buf=")
			if msg := h.message(); !strings.Contains(msg, "split=true err=<nil>") {
				t.Fatalf("message = %q", msg)
			}
			if n := h.diffSessions(); n != 1 {
				t.Errorf("応答の時点の見比べの組 = %d, want 1", n)
			}
		})
	}
}

// diff/start の Wait で、分割に失敗したら、その誤りで応答する。新しい client が繋ぐ前に版を閉じたら、待ちを捨てて誤りで応答する（#447）。
//
// req: FR-317
func TestDiffStartWaitFails(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\n"})
	calls := make(chan diffCall, 2)
	release := make(chan error, 2)
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t), SplitPane: gatedSplit(calls, release)}, "a.txt")
	h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })

	h.keys(":fake.headdiff wait<CR>")
	waitSplit(t, calls)
	release <- errors.New("split failed")
	// 本体の誤り（分割の誤り）がメッセージの行に出ているので、拡張機能の知らせは履歴で見る。
	h.eventually("分割の誤りでの応答", func() bool {
		for _, m := range h.e.msgHistory {
			if strings.Contains(m.Text, "headdiff buf=") && strings.Contains(m.Text, "見比べのペインを開けません: split failed") {
				return true
			}
		}
		return false
	})
	h.keys("<Esc>")

	h.keys(":fake.headdiff wait<CR>")
	waitSplit(t, calls)
	var id ext.BufferID
	h.eventually("作ったバッファ", func() bool {
		for _, bs := range h.e.bufs {
			if bs.title == "HEAD:x" {
				id = bs.id
				return true
			}
		}
		return false
	})
	h.keys(fmt.Sprintf(":fake.headdiff dispose %d<CR>", id))
	// diff/start の応答（誤り）と buffer/dispose の応答の知らせは、どちらが後になるか決まらないので、履歴で見る。
	h.eventually("版を閉じたときの誤りでの応答", func() bool {
		for _, m := range h.e.msgHistory {
			if strings.Contains(m.Text, "headdiff buf=") && strings.Contains(m.Text, "見比べが始まりませんでした") {
				return true
			}
		}
		return false
	})
	release <- nil
}

// diff/start で分割を頼んだ後、新しい client が繋ぐ前に、まだタブでない版を buffer/dispose で閉じると、
// 後から繋いだその client を断り（ErrSplitCanceled）、名前の無い空のタブの client を残さない（#446）。
// server は理由を付けずに client を終わらせるので、ペインが閉じる。
//
// req: FR-318
func TestDiffStartDisposedBeforeAttach(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\n"})
	calls := make(chan diffCall, 1)
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t), SplitPane: recordSplit(calls)}, "a.txt")
	h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })

	h.keys(":fake.headdiff base<CR>")
	h.waitMessage("headdiff buf=")
	call := waitSplit(t, calls)
	var id ext.BufferID
	h.e.do(func() {
		for _, bs := range h.e.bufs {
			if bs.title == "HEAD:x" {
				id = bs.id
			}
		}
	})
	h.keys(fmt.Sprintf(":fake.headdiff dispose %d<CR>", id))
	h.waitMessage("headdiff dispose err=<nil>")

	term := &exitTerm{fakeTerm: newFakeTerm()}
	h.peers = append(h.peers, term.fakeTerm)
	if err := h.e.Attach(term, ClientInfo{ID: 2, Pane: "%2", WorkDir: dir, Env: call.env()}); !errors.Is(err, ErrSplitCanceled) {
		t.Fatalf("Attach = %v, want ErrSplitCanceled", err)
	}
	var n int
	h.e.do(func() { n = len(h.e.clients) })
	if n != 1 {
		t.Errorf("client の数 = %d, want 1（空のタブの client を残さない）", n)
	}
	// 同じ印で 2 度目に繋いだ client（ふつうは無い）は、ふつうの client として繋ぐ（印は 1 回だけ使う）。
	h.attach(ClientInfo{ID: 3, Pane: "%3", WorkDir: dir, Env: call.env()})
}

// 新しい client が繋ぐ時点で、待っているバッファが閉じずに別の client のタブになっていた（diffAdopt が待ちを捨てる場合）も、
// その client を断り、空のタブの client を残さない（#448 のレビューの推奨 3。#446）。
// 待ちのバッファが別の client のタブになる経路（拡張機能の editor/open など）の代わりに、持ち主を直に入れて真似る。
//
// req: FR-318
func TestDiffStartAdoptStale(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\n"})
	calls := make(chan diffCall, 1)
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t), SplitPane: recordSplit(calls)}, "a.txt")
	h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })

	h.keys(":fake.headdiff base<CR>")
	h.waitMessage("headdiff buf=")
	call := waitSplit(t, calls)
	var buf *bufferState
	h.e.do(func() {
		buf = h.e.diff.pending.buf
		buf.owner = h.e.clients[0]
	})
	term := &exitTerm{fakeTerm: newFakeTerm()}
	h.peers = append(h.peers, term.fakeTerm)
	err := h.e.Attach(term, ClientInfo{ID: 2, Pane: "%2", WorkDir: dir, Env: call.env()})
	h.e.do(func() { buf.owner = nil })
	if !errors.Is(err, ErrSplitCanceled) {
		t.Fatalf("Attach = %v, want ErrSplitCanceled", err)
	}
	var n int
	var pending bool
	h.e.do(func() { n, pending = len(h.e.clients), h.e.diff.pending != nil })
	if n != 1 || pending {
		t.Errorf("client の数 = %d, 待ち = %v; want 1, false（空のタブの client を残さない）", n, pending)
	}
}
