package editor

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/config"
)

// popupCall は、Config.RunPopup が受けた呼び出しである。
type popupCall struct {
	from string
	argv []string
	dir  string
	opts PopupOptions
}

// popupRecorder は、Config.RunPopup の偽物である。受けた呼び出しを記録し、err を返す。
type popupRecorder struct {
	mu    sync.Mutex
	calls []popupCall
	err   error
}

func (r *popupRecorder) run(from string, argv []string, dir string, opts PopupOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, popupCall{from: from, argv: slices.Clone(argv), dir: dir, opts: opts})
	return r.err
}

func (r *popupRecorder) list() []popupCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// attachPaneHandover は、端末を子プロセスへ渡せ、端末多重化ソフトのペイン pane にいる client（識別子 2、作業ディレクトリ dir）を足す。
// pane が空なら、端末多重化ソフトの外の client である。
func (h *harness) attachPaneHandover(dir, pane string, files ...string) *handoverPeer {
	h.t.Helper()
	ho := &handoverTerm{exitTerm: &exitTerm{fakeTerm: newFakeTerm()}}
	h.peers = append(h.peers, ho.fakeTerm)
	if err := h.e.Attach(ho, ClientInfo{ID: 2, WorkDir: dir, Pane: pane, Files: files}); err != nil {
		h.t.Fatalf("Attach = %v", err)
	}
	return &handoverPeer{peer: &peer{h: h, term: ho.exitTerm}, ho: ho}
}

// clientMessage は、識別子 id の client のメッセージの行を返す。
func (h *harness) clientMessage(id ClientID) string {
	var msg string
	h.e.do(func() { msg = h.clientMessageInLoop(id) })
	return msg
}

// clientMessageInLoop は、clientMessage の、イベントループの上（eventually の条件の中）で呼ぶ形である。
func (h *harness) clientMessageInLoop(id ClientID) string {
	return h.e.viewOf(h.e.findClient(id)).message.Text
}

// 端末多重化ソフトの中の client の :terminal.popup は、そのペインの上のポップアップで、:{range}! と同じシェルに -c でコマンドを渡し、
// :! と同じ作業ディレクトリ（client の作業ディレクトリ）で動かす。幅はファインダーの窓と同じ preview.windowWidthPercent である。
// 端末は子プロセスへ渡さず、エディタは止めない（続くキーをすぐ処理する）。
//
// req: FR-168
func TestTerminalPopup(t *testing.T) {
	t.Setenv("SHELL", "/bin/test-shell")
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n", "b.txt": "xyz\n"})
	rec := &popupRecorder{}
	h := start(t, Config{WorkDir: dir, RunPopup: rec.run}, "a.txt")
	b := h.attachPaneHandover(dir, "%5", filepath.Join(dir, "b.txt"))
	b.keys(":terminal.popup git log --oneline | head<CR>x")
	h.eventually("ポップアップの頼みと続くキー", func() bool {
		return len(rec.list()) == 1 && h.e.findPath(filepath.Join(dir, "b.txt")).buf.Text() == "yz"
	})
	got := rec.list()[0]
	want := popupCall{from: "%5", argv: []string{"/bin/test-shell", "-c", "git log --oneline | head"}, dir: dir, opts: PopupOptions{Width: 90}}
	if got.from != want.from || !slices.Equal(got.argv, want.argv) || got.dir != want.dir || got.opts != want.opts {
		t.Errorf("RunPopup = %+v, want %+v", got, want)
	}
	if log := b.ho.log(); len(log) != 0 {
		t.Errorf("端末を子プロセスへ渡した: %q", log)
	}
	if msg := h.clientMessage(2); msg != "" {
		t.Errorf("メッセージ = %q, want 空", msg)
	}
	h.e.do(func() {
		if h.e.shell != nil {
			t.Error("外部のコマンドの実行として持った")
		}
	})
	// 幅は設定 preview.windowWidthPercent に従う。
	b.keys(":option.set preview.windowWidthPercent 70<CR>:terminal.popup ls<CR>")
	h.eventually("2 回目の頼み", func() bool { return len(rec.list()) == 2 })
	if got := rec.list()[1]; got.opts.Width != 70 || got.argv[2] != "ls" {
		t.Errorf("2 回目の RunPopup = %+v", got)
	}
}

// 端末多重化ソフトの外の client（ペインを知らない）の :terminal.popup は、:! と同じく端末を子プロセスへ渡して動かし（同じ頼み、同じ問い）、
// 外で動かしたことを知らせない。Config.RunPopup は呼ばない。引数が無ければ E471 で、何も動かさない。
//
// req: FR-168
func TestTerminalPopupOutsideMux(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n", "b.txt": "xyz\n"})
	rec := &popupRecorder{}
	h := start(t, Config{WorkDir: dir, RunPopup: rec.run, ChildEnv: []string{"EDITOR=/bin/me"}}, "a.txt")
	b := h.attachPaneHandover(dir, "", filepath.Join(dir, "b.txt"))
	b.keys(":terminal.popup git log --oneline<CR>")
	j := b.job()
	if len(j.Commands) != 1 || j.Commands[0].Shell != "git log --oneline" || !j.Prompt {
		t.Errorf("頼み = %+v, want :! と同じ頼み", j)
	}
	if want := []string{"EDITOR=/bin/me", EnvClient + "=2"}; !slices.Equal(j.Env, want) {
		t.Errorf("Env = %q, want %q", j.Env, want)
	}
	b.reply(TerminalResult{ID: j.ID})
	h.eventually("実行の終わり", func() bool { return h.e.shell == nil })
	if msg := h.clientMessage(2); msg != "" {
		t.Errorf("メッセージ = %q, want 空（外で動かしたことを知らせない）", msg)
	}
	if calls := rec.list(); len(calls) != 0 {
		t.Errorf("外で RunPopup を呼んだ: %+v", calls)
	}

	b.keys(":terminal.popup<CR>")
	h.eventually("引数の無い誤り", func() bool { return strings.HasPrefix(h.clientMessageInLoop(2), "E471") })
	if j2, _ := b.ho.lastJob(); j2.ID != j.ID || len(rec.list()) != 0 {
		t.Errorf("引数が無いのに動かした")
	}
}

// 端末多重化ソフトのポップアップを開けない（herdr のポップアップが開いている mux.ErrPopupBusy が、server の開き直しの後も続いたなど）ときは、
// 誤りを、コマンドを打った client のメッセージの行に出す。:! へは切り替えない。
//
// req: FR-168
func TestTerminalPopupBusy(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n", "b.txt": "xyz\n"})
	rec := &popupRecorder{err: errors.New("mux: a popup is already open")}
	h := start(t, Config{WorkDir: dir, RunPopup: rec.run}, "a.txt")
	b := h.attachPaneHandover(dir, "wW:p7", filepath.Join(dir, "b.txt"))
	b.keys(":terminal.popup keifu-wt<CR>")
	h.eventually("誤りの知らせ", func() bool {
		return h.clientMessageInLoop(2) == "端末多重化ソフトのポップアップを開けません: mux: a popup is already open"
	})
	if log := b.ho.log(); len(log) != 0 {
		t.Errorf("開けないときに端末を子プロセスへ渡した: %q", log)
	}
}

// ポップアップを今開けない（Config.PopupReady が false。herdr app に mod が無いか古い）client の :terminal.popup は、
// 端末多重化ソフトの外と同じく :! と同じ経路で動かし、RunPopup を呼ばず、誤りも出さない。
// 中央のポップアップの部品も OpenPopup を呼ばずに、元の client の画面の中央に描き、誤りを出さない。
// PopupReady が true に変われば、どちらも端末多重化ソフトのポップアップに出す。PopupReady には client のペインを渡す。
//
// req: FR-326
func TestTerminalPopupNotReady(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n", "b.txt": "xyz\n"})
	rec := &popupRecorder{}
	var mu sync.Mutex
	ready := false
	var asked []string
	var calls muxCalls
	cfg := Config{WorkDir: dir, RunPopup: rec.run, Extensions: fakeSpecs(t), PopupReady: func(from string) bool {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, from)
		return ready
	}}
	calls.config(&cfg)
	h := start(t, cfg, "a.txt")
	b := h.attachPaneHandover(dir, "t7", filepath.Join(dir, "b.txt"))
	message := func() string {
		var msg string
		h.e.do(func() { msg = h.e.viewOf(h.e.findClient(2)).message.Text })
		return msg
	}
	b.keys(":terminal.popup git log --oneline<CR>")
	j := b.job()
	if len(j.Commands) != 1 || j.Commands[0].Shell != "git log --oneline" {
		t.Errorf("頼み = %+v, want :! と同じ頼み", j)
	}
	b.reply(TerminalResult{ID: j.ID})
	h.eventually("実行の終わり", func() bool { return h.e.shell == nil })
	if msg := message(); msg != "" {
		t.Errorf("メッセージ = %q, want 空", msg)
	}

	b.keys(":fake.popup list<CR>")
	h.eventually("元の client の画面のポップアップ", func() bool { return strings.Contains(b.term.screen(), "close me") })
	h.settle()
	calls.mu.Lock()
	n := len(calls.popups)
	calls.mu.Unlock()
	if n != 0 || len(rec.list()) != 0 {
		t.Errorf("開けないのに OpenPopup（%d 回）か RunPopup（%+v）を呼んだ", n, rec.list())
	}
	if msg := message(); strings.Contains(msg, "端末多重化ソフト") {
		t.Errorf("メッセージ = %q, want 端末多重化ソフトのポップアップの誤りを出さない", msg)
	}
	b.keys("<Esc>")
	h.eventually("ポップアップを閉じる", func() bool { return h.e.viewOf(h.e.findClient(2)).float == nil })

	mu.Lock()
	ready = true
	mu.Unlock()
	b.keys(":terminal.popup ls<CR>")
	h.eventually("開けるようになった後の頼み", func() bool { return len(rec.list()) == 1 })
	b.keys(":fake.popup list<CR>")
	h.eventually("開けるようになった後の OpenPopup", func() bool { _, ok := calls.last(false); return ok })
	mu.Lock()
	defer mu.Unlock()
	if len(asked) < 4 || slices.ContainsFunc(asked, func(p string) bool { return p != "t7" }) {
		t.Errorf("PopupReady の引数 = %q, want client のペイン t7 で 4 回以上", asked)
	}
}

// キーに { "command": "terminal.popup", "args": ... } で割り当てると、コマンドラインを出さずに同じくポップアップで動かす（FR-154）。
// マクロに記録した :terminal.popup は、再生でもポップアップを開き、ポップアップの終わりを待たずに再生の残りを続ける。
//
// req: FR-168
func TestTerminalPopupKeymapAndMacro(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n", "b.txt": "uvwxyz\n"})
	rec := &popupRecorder{}
	h := start(t, Config{
		WorkDir: dir, RunPopup: rec.run,
		KeymapCommands: map[string]map[string]config.KeymapCommand{
			"normal": {"<Leader>gk": {Name: "terminal.popup", Args: "keifu-wt"}},
		},
	}, "a.txt")
	if msg := h.message(); msg != "" {
		t.Fatalf("起動の message = %q", msg)
	}
	b := h.attachPaneHandover(dir, "%5", filepath.Join(dir, "b.txt"))
	b.keys("<Space>gk")
	h.eventually("キーの頼み", func() bool { return len(rec.list()) == 1 })
	if got := rec.list()[0]; got.from != "%5" || !slices.Equal(got.argv, []string{"/bin/sh", "-c", "keifu-wt"}) {
		t.Errorf("RunPopup = %+v", got)
	}

	b.keys("qa:terminal.popup true<CR>xq")
	h.eventually("記録の中の頼み", func() bool { return len(rec.list()) == 2 })
	b.keys("2@a")
	h.eventually("再生の残り", func() bool {
		return len(rec.list()) == 4 && h.e.findPath(filepath.Join(dir, "b.txt")).buf.Text() == "xyz"
	})
	if log := b.ho.log(); len(log) != 0 {
		t.Errorf("端末を子プロセスへ渡した: %q", log)
	}
}
