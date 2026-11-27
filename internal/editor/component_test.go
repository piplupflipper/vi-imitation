package editor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// muxCalls は、Config.OpenPopup と OpenPane の偽物が受けた呼び出しを記録する。
type muxCalls struct {
	mu     sync.Mutex
	popups []muxCall
	panes  []muxCall
	// fail が nil でなければ、呼び出しはこの誤りを返す。
	fail error
}

type muxCall struct {
	from  string
	view  uint64
	opts  PaneOptions
	popup PopupOptions
}

func (m *muxCalls) config(cfg *Config) {
	cfg.OpenPopup = func(from string, view uint64, _ string, opts PopupOptions) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.popups = append(m.popups, muxCall{from: from, view: view, popup: opts})
		return m.fail
	}
	cfg.OpenPane = func(from string, view uint64, _ string, opts PaneOptions) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.panes = append(m.panes, muxCall{from: from, view: view, opts: opts})
		return m.fail
	}
}

func (m *muxCalls) last(panes bool) (muxCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.popups
	if panes {
		l = m.panes
	}
	if len(l) == 0 {
		return muxCall{}, false
	}
	return l[len(l)-1], true
}

// compPeer は、AttachComponent で繋いだ部品の client の偽の端末である。
type compPeer struct {
	h    *harness
	term *exitTerm
}

// attachComponent は、部品 view を描く部品の client を繋ぐ。
// pane なら、ペインの部品の client として繋ぐ。
func (h *harness) attachComponent(view uint64, pane bool) *compPeer {
	h.t.Helper()
	term := &exitTerm{fakeTerm: newFakeTerm()}
	h.peers = append(h.peers, term.fakeTerm)
	info := ComponentInfo{View: view, Pane: pane}
	if pane {
		info.MuxPane = fmt.Sprintf("%%p%d", view)
	}
	if err := h.e.AttachComponent(term, info); err != nil {
		h.t.Fatalf("AttachComponent(%d) = %v", view, err)
	}
	return &compPeer{h: h, term: term}
}

func (p *compPeer) keys(s string) {
	p.h.t.Helper()
	for _, k := range editcmd.MustParseKeys(s) {
		p.term.in <- TermEvent{Key: k}
		p.h.sent++
	}
}

// waitScreen は、部品の client の画面が substr を含むまで待つ。
func (p *compPeer) waitScreen(substr string) {
	p.h.t.Helper()
	p.h.eventually("部品の client の画面に "+substr, func() bool { return strings.Contains(p.term.screen(), substr) })
}

// clientOf は、偽の端末 term の client を返す。イベントループの goroutine で呼ぶ。
func (h *harness) clientOf(term Terminal) *clientView {
	for _, c := range h.e.clients {
		if h.e.viewOf(c).term == term {
			return c
		}
	}
	return nil
}

// Config.PopupAwaitRemote（herdr app）なら、OpenPopup を頼んでから部品の client が繋ぐまで、中央のポップアップを元の client の画面に描かない
// （画面の中に描いてから窓へ移るちらつきを出さない）。繋いだら部品の client の画面に描く。
// OpenPopup が誤りを返したら、元の client の画面に描いて理由を出す。成功しても PopupAwaitTimeout までに繋がなければ、画面に描いて知らせる。
//
// req: FR-325
func TestComponentPopupAwaitRemote(t *testing.T) {
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t), PopupAwaitRemote: true, PopupAwaitTimeout: 300 * time.Millisecond}
	calls.config(&cfg)
	h := start(t, cfg)
	peer := h.attach(ClientInfo{ID: 2, Pane: "t7"})

	peer.keys(":fake.popup list<CR>")
	h.eventually("OpenPopup", func() bool { _, ok := calls.last(false); return ok })
	call, _ := calls.last(false)
	h.settle()
	if strings.Contains(peer.term.screen(), "close me") {
		t.Fatalf("部品の client が繋ぐ前に、元の client の画面に描いた:\n%s", peer.term.screen())
	}
	comp := h.attachComponent(call.view, false)
	comp.waitScreen("close me")
	h.settle()
	if strings.Contains(peer.term.screen(), "close me") {
		t.Errorf("部品の client が繋いだ後に、元の client の画面に描いた")
	}
	// 待つ上限を過ぎても、繋いだ後は何も出さない。
	time.Sleep(400 * time.Millisecond)
	h.settle()
	var msg string
	h.e.do(func() { msg = h.e.viewOf(h.clientOf(peer.term)).message.Text })
	if strings.Contains(peer.term.screen(), "close me") || strings.Contains(msg, "画面の中に出します") {
		t.Errorf("繋いだ後に画面の中に描いた: %q", msg)
	}
	comp.keys("<Esc>")
	h.eventually("閉じる", func() bool { return h.e.viewOf(h.clientOf(peer.term)).float == nil })

	// 開けたと答えたのに繋がなければ、上限の後に画面の中に描いて知らせる。
	peer.keys(":fake.popup list<CR>")
	h.eventually("2 つ目の OpenPopup", func() bool { c, _ := calls.last(false); return c.view != call.view })
	h.settle()
	if strings.Contains(peer.term.screen(), "close me") {
		t.Fatal("繋ぐ前に描いた")
	}
	h.waitMessage("端末多重化ソフトのポップアップに部品が出ないので、この画面の中に出します")
	h.eventually("上限の後の画面の中のポップアップ", func() bool { return strings.Contains(peer.term.screen(), "close me") })
	peer.keys("<Esc>")
	h.eventually("閉じる", func() bool { return h.e.viewOf(h.clientOf(peer.term)).float == nil })

	// 開けなければ、すぐ画面の中に描いて理由を出す。
	calls.mu.Lock()
	calls.fail = errors.New("no mod")
	calls.mu.Unlock()
	peer.keys(":fake.popup list<CR>")
	h.waitMessage("端末多重化ソフトのポップアップを開けないので、この画面の中に出します: no mod")
	h.eventually("画面の中のポップアップ", func() bool { return strings.Contains(peer.term.screen(), "close me") })
}

// 端末多重化ソフトの中（client のペインを知っている）で中央のポップアップを開くと、Config.OpenPopup で端末多重化ソフトの
// ポップアップを開かせる。ポップアップの部品の client が繋ぐまでは元の client の画面の中央に描き、繋いだら部品の client の画面に
// 部品を広げて描き、元の client の画面には描かない。部品の client で打ったキーは元の client の操作になり（popupKeys、<CR>、<Esc>）、
// 閉じたら部品の client に終わりを知らせる。部品の client が先に終わったら、ポップアップを閉じて view/closed を送る。
//
// req: FR-66, FR-67
func TestComponentPopup(t *testing.T) {
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t)}
	calls.config(&cfg)
	h := start(t, cfg)
	peer := h.attach(ClientInfo{ID: 2, Pane: "%9"})

	peer.keys(":fake.popup list<CR>")
	h.eventually("ポップアップ", func() bool { return h.e.viewOf(h.clientOf(peer.term)).float != nil })
	h.eventually("OpenPopup", func() bool { _, ok := calls.last(false); return ok })
	call, _ := calls.last(false)
	if call.from != "%9" {
		t.Errorf("OpenPopup from = %q, want the pane of the client that opened it", call.from)
	}
	// 部品の client が繋ぐまでは、元の client の画面の中央に描く。
	h.eventually("元の client の画面のポップアップ", func() bool { return strings.Contains(peer.term.screen(), "close me") })

	// ペインの部品の client は、中央のポップアップに繋がない（PR #248 のレビューの推奨 3）。
	if err := h.e.AttachComponent(&exitTerm{fakeTerm: newFakeTerm()}, ComponentInfo{View: call.view, Pane: true}); !errors.Is(err, ErrUnknownView) {
		t.Errorf("pane AttachComponent to a popup = %v, want ErrUnknownView", err)
	}
	comp := h.attachComponent(call.view, false)
	if err := h.e.AttachComponent(&exitTerm{fakeTerm: newFakeTerm()}, ComponentInfo{View: call.view}); !errors.Is(err, ErrUnknownView) {
		t.Errorf("second AttachComponent = %v, want ErrUnknownView", err)
	}
	comp.waitScreen("close me")
	h.eventually("元の client の画面から消える", func() bool { return !strings.Contains(peer.term.screen(), "close me") })
	if scr := comp.term.screen(); !strings.HasPrefix(strings.TrimLeft(scr, " "), "list") || strings.Contains(scr, "┌") {
		t.Errorf("component screen (title on the first row, no border):\n%s", scr)
	}

	// 部品の client のキーは、元の client の操作として処理する。
	h.waitPopupOpened(call.view)
	comp.keys("x")
	h.waitMessage("kind=3 item=one key=fake.pick")
	comp.keys("j")
	h.settle()
	comp.keys("<CR>")
	h.waitMessage("kind=1 item=close")
	// 選んだ close に、拡張機能は view/close で応える。閉じたら部品の client に終わりを知らせる。
	h.eventually("view/close", func() bool { return h.e.viewOf(h.clientOf(peer.term)).float == nil })
	h.eventually("部品の client の終わり", func() bool { return len(comp.term.exits()) == 1 })
	if got := comp.term.exits(); got[0] != "" {
		t.Errorf("component exit reason = %q, want empty", got[0])
	}

	// 部品の client が先に終わったら（利用者が端末多重化ソフトでポップアップを閉じた）、ポップアップを閉じ、view/closed を送る。
	peer.keys(":fake.popup list<CR>")
	h.eventually("2 つ目の OpenPopup", func() bool { c, _ := calls.last(false); return c.view != call.view })
	call, _ = calls.last(false)
	comp = h.attachComponent(call.view, false)
	comp.waitScreen("close me")
	h.waitPopupOpened(call.view)
	comp.term.closeInput()
	h.waitMessage(fmt.Sprintf("view %d kind=0 item= key=", call.view))
	h.eventually("閉じたポップアップ", func() bool { return h.e.viewOf(h.clientOf(peer.term)).float == nil })

	// 端末多重化ソフトのポップアップを開けなければ、理由を出し、元の client の画面の中央に描いたままにする。
	calls.mu.Lock()
	calls.fail = errors.New("no popup")
	calls.mu.Unlock()
	peer.keys(":fake.popup list<CR>")
	h.waitMessage("端末多重化ソフトのポップアップを開けないので、この画面の中に出します: no popup")
	h.eventually("元の client の画面のポップアップ", func() bool { return strings.Contains(peer.term.screen(), "close me") })

	// 端末多重化ソフトの外の client（ペインを知らない）は、OpenPopup を呼ばずに画面の中央に描く。
	calls.mu.Lock()
	n := len(calls.popups)
	calls.mu.Unlock()
	h.keys(":fake.popup list<CR>")
	h.eventually("外のポップアップ", func() bool { return h.e.float != nil && h.e.float.center })
	h.settle()
	calls.mu.Lock()
	defer calls.mu.Unlock()
	if len(calls.popups) != n {
		t.Errorf("OpenPopup was called for a client outside the terminal multiplexer")
	}
}

// 行番号の列（lineNumber.show）は、client の画面のバッファの表示領域にだけ出し、ポップアップの部品の client の画面には出さない
// （基本設計 6.7）。部品の client の画面は client の画面の Frame から作るので、行番号を詰めるのは client の画面を描く 1 か所にした。
// 部品の client の画面に一覧（選択肢の問い）を出すときは、窓で表示領域を覆わないので、行番号が載っていると 0 行目に 1 が見えた
// （PR #272 のレビューの必須 1）。
//
// req: FR-135
func TestComponentPopupWithoutLineNumbers(t *testing.T) {
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t), Options: map[string]option.Value{option.LineNumberShow: option.BoolValue(true)}}
	calls.config(&cfg)
	h := start(t, cfg)
	peer := h.attach(ClientInfo{ID: 2, Pane: "%9"})
	peer.keys(":fake.popup list<CR>")
	h.eventually("OpenPopup", func() bool { _, ok := calls.last(false); return ok })
	call, _ := calls.last(false)
	comp := h.attachComponent(call.view, false)
	comp.waitScreen("close me")
	// 元の client の画面には行番号が出る（0 行目はタブの行）。
	h.eventually("元の client の画面の行番号", func() bool {
		return strings.HasPrefix(strings.Split(peer.term.screen(), "\n")[1], "  1 ")
	})
	h.e.do(func() {
		c := h.clientOf(peer.term)
		h.e.withView(c, func() {
			fr := h.e.popupComponentFrame(h.e.float)
			if fr.LineNumbers != (ui.LineNumbers{}) {
				t.Errorf("部品の client の画面の LineNumbers = %+v, want ゼロ値", fr.LineNumbers)
			}
			// 一覧を出す（窓で覆わない）ときも、0 行目に番号が出ない。
			fr.Float = nil
			fr.List = &ui.List{Title: "q", Items: []string{"[1] a"}}
			s := (&ui.Renderer{Width: 40, Height: 10}).Render(fr)
			if row := s.Grid.RowText(0); strings.TrimSpace(row) != "q" {
				t.Errorf("部品の client の画面の 0 行目 = %q, want 一覧の見出しだけ", row)
			}
			if fb := h.e.bufferFrame(); !fb.LineNumbers.Show {
				t.Errorf("client の画面の LineNumbers = %+v, want 出す", fb.LineNumbers)
			}
		})
	})
}

// 拡張機能の pane/open は、元の client のペインを Config.OpenPane で分割させ、ペインの部品の client が描く。
// ペインの部品の client で打ったキーは元の client の操作になり（ヤンクしたレジスターを元の client で貼れる、<CR> で選ぶ、view/expand）、
// <Esc> では閉じない。元の client は、ペインを開いている間も自分のタブを編集できる。:q でペインを閉じ、部品の client に終わりを知らせる。
// 部品の client が先に終わったら view/closed を送る。
//
// req: FR-66, FR-115
func TestComponentPane(t *testing.T) {
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t)}
	calls.config(&cfg)
	h := start(t, cfg)
	peer := h.attach(ClientInfo{ID: 2, Pane: "%9"})

	peer.keys(":fake.pane tree<CR>")
	h.eventually("OpenPane", func() bool { _, ok := calls.last(true); return ok })
	call, _ := calls.last(true)
	if call.from != "%9" || call.opts != (PaneOptions{Down: true, Size: 30}) {
		t.Errorf("OpenPane = %+v", call)
	}
	// fake.pane が後から出す "pane <ID>" の知らせが、操作の知らせを上書きしないように、先に届くのを待つ（#313）。
	h.waitPaneOpened(call.view)
	var floating bool
	h.e.do(func() { floating = h.e.viewOf(h.clientOf(peer.term)).float != nil })
	if floating {
		t.Errorf("pane/open opened a popup on the client")
	}
	if err := h.e.AttachComponent(&exitTerm{fakeTerm: newFakeTerm()}, ComponentInfo{View: call.view}); !errors.Is(err, ErrUnknownView) {
		t.Errorf("popup AttachComponent to a pane = %v, want ErrUnknownView", err)
	}
	comp := h.attachComponent(call.view, true)
	comp.waitScreen("▸ dir")

	// ペインで開いた木の <CR> は開閉する（view/expand）。<Esc> では閉じない。
	comp.keys("<CR>")
	h.waitMessage("kind=4 item=dir key= expanded=true")
	comp.waitScreen("▾ dir")
	comp.keys("<Esc>")
	h.settle()
	var panes int
	h.e.do(func() { panes = len(h.e.viewOf(h.clientOf(peer.term)).panes) })
	if panes != 1 {
		t.Fatalf("panes after <Esc> = %d, want 1", panes)
	}

	// 元の client は自分のタブを編集でき、ペインでヤンクしたものを貼れる（元の client の操作で、レジスターを共有する）。
	comp.keys("yy")
	h.settle()
	peer.keys("ihello <Esc>p")
	h.settle()
	var text string
	h.e.do(func() { text = h.e.viewOf(h.clientOf(peer.term)).cur.buf.Text() })
	if text != "hello \n▾ dir" {
		t.Errorf("peer text = %q, want the line yanked in the pane pasted", text)
	}
	if scr := peer.term.screen(); strings.Contains(scr, "▾ dir\n") && strings.Contains(scr, "    a") {
		t.Errorf("the pane is drawn on the client screen:\n%s", scr)
	}

	// 子の無い項目の <CR> は選ぶ（view/select）。
	comp.keys("j<CR>")
	h.waitMessage("kind=1 item=dir/a")

	// :q でペインを閉じ、部品の client に終わりを知らせる。拡張機能へは view/closed を送る。
	comp.keys(":q<CR>")
	h.waitMessage("kind=0 item= key=")
	h.eventually("部品の client の終わり", func() bool { return len(comp.term.exits()) == 1 })
	h.e.do(func() { panes = len(h.e.viewOf(h.clientOf(peer.term)).panes) })
	if panes != 0 {
		t.Errorf("panes after :q = %d", panes)
	}
	h.e.do(func() { text = h.e.viewOf(h.clientOf(peer.term)).cur.buf.Text() })
	if text != "hello \n▾ dir" {
		t.Errorf(":q in the pane changed the client: %q", text)
	}

	// 部品の client が先に終わったら（利用者が端末多重化ソフトでペインを閉じた）、view/closed を送る。
	peer.keys(":fake.pane list<CR>")
	h.eventually("2 つ目の OpenPane", func() bool { c, _ := calls.last(true); return c.view != call.view })
	call, _ = calls.last(true)
	comp = h.attachComponent(call.view, true)
	comp.waitScreen("close me")
	h.waitPaneOpened(call.view)
	comp.term.closeInput()
	// 1 つ目のペインの view/closed（kind=0）と取り違えないように、このペインの ID で待つ。
	h.waitMessage(fmt.Sprintf("view %d kind=0 item= key=", call.view))
	h.e.do(func() { panes = len(h.e.viewOf(h.clientOf(peer.term)).panes) })
	if panes != 0 {
		t.Errorf("panes after the component client is gone = %d", panes)
	}

	// 元の client が終わったら、ペインの部品の client も終わらせる。
	peer.keys(":fake.pane list<CR>")
	h.eventually("3 つ目の OpenPane", func() bool { c, _ := calls.last(true); return c.view != call.view })
	call, _ = calls.last(true)
	comp = h.attachComponent(call.view, true)
	comp.waitScreen("close me")
	peer.keys(":q!<CR>")
	h.eventually("元の client が終わったときの部品の client の終わり", func() bool { return len(comp.term.exits()) == 1 })
}

// ペインで選んだ後に拡張機能が送る editor/open は、その拡張機能を後から別の client が使っていても、ペインを開いた client のタブに開く
// （基本設計 1.2。FR-105。PR #248 のレビューの必須）。ペインの部品の client のキーで選んで開いたら、
// 端末多重化ソフトのフォーカスをペインからその client のペインへ移す（推奨 1）。
//
// req: FR-115
func TestPaneSelectionGoesToOpener(t *testing.T) {
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t)}
	opened := fakeOpenedPath(os.Getpid())
	if err := os.WriteFile(opened, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(opened) })
	calls.config(&cfg)
	var mu sync.Mutex
	var focused [][2]string
	cfg.FocusPane = func(from, pane string) error {
		mu.Lock()
		defer mu.Unlock()
		focused = append(focused, [2]string{from, pane})
		return nil
	}
	// active は、偽の端末多重化ソフトで部品のペインがアクティブかである。asked は PaneFocused に渡されたペインである。
	active := true
	var asked []string
	cfg.PaneFocused = func(pane string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, pane)
		return active, nil
	}
	h := start(t, cfg)
	// 上限（paneFocusWindow）の内に届いたかは、時計に任せず順序で決める（#313）。上限は長くし、上限を過ぎた場合（late）は
	// 選んだ時刻を上限より前にずらす。負荷で選んでから届くまでが 1 秒を超えても、上限の内として扱う。
	h.e.do(func() { h.e.paneFocus = time.Hour })
	a := h.attach(ClientInfo{ID: 2, Pane: "%9"})
	a.keys(":fake.pane files<CR>")
	h.eventually("OpenPane", func() bool { _, ok := calls.last(true); return ok })
	call, _ := calls.last(true)
	comp := h.attachComponent(call.view, true)
	comp.waitScreen("opened.txt")

	// 最初の client（端末多重化ソフトの外）で同じ拡張機能を使い、拡張機能へ最後に問い合わせた client にする。
	h.keys(":fake.popup list<CR>")
	h.eventually("最初の client のポップアップ", func() bool { return h.e.viewOf(h.clientOf(h.term)).float != nil })
	h.keys("<Esc>")
	h.settle()

	comp.keys("<CR>")
	t.Cleanup(func() {
		if t.Failed() {
			h.e.do(func() {
				for _, m := range h.e.msgHistory {
					t.Logf("history: %s", m.Text)
				}
				for _, b := range h.e.bufs {
					t.Logf("buf %d %q owner=%v", b.id, b.path, b.owner != nil)
				}
			})
		}
	})
	h.eventually("ペインを開いた client のタブ", func() bool {
		return h.e.viewOf(h.clientOf(a.term)).cur.path == opened
	})
	var firstPath string
	h.e.do(func() { firstPath = h.e.viewOf(h.clientOf(h.term)).cur.path })
	if firstPath == opened {
		t.Errorf("the file opened in the client that used the extension last")
	}
	h.eventually("フォーカスの移動", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(focused) == 1 && focused[0] == [2]string{"%9", "%9"}
	})

	// フォーカスを戻すのは、ペインで選んだ後の最初の editor/open だけで、その後に利用者がどれかの client でキーを打っていれば戻さない。
	// 選んでから上限（paneFocusWindow）を過ぎて届いた editor/open（別の window へ移った後など）でも戻さない。
	// どちらでも、ペインを開いた client のタブは開いたファイルにする（PR #248 の再レビューの必須）。
	focusCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(focused)
	}
	picked := func() int {
		n := 0
		for _, m := range h.e.msgHistory {
			if strings.HasPrefix(m.Text, "picked ") {
				n++
			}
		}
		return n
	}
	// 最初に選んだ分の知らせ（picked open）を待つ。拡張機能は、editor/open の応答を受けてから知らせるので、タブが替わった後に届きうる。
	// 待たずに次の pick で数え始めると、遅れて届いたこの知らせを次の選択の分と取り違え、前の選択が開く前に次を選ぶ（#313）。
	h.eventually("最初に選んだファイルを開いた知らせ", func() bool { return picked() == 1 })
	// selected は、拡張機能が項目 item を選んだ知らせを受けたか（OnView の message/show がイベントループに届いたか）を返す。
	// 届いていれば、選んだキーは処理済みで、印（extFromPane）が立っている。イベントループの goroutine で呼ぶ。
	selected := func(item string) bool {
		return slices.ContainsFunc(h.e.msgHistory, func(m ui.Message) bool { return strings.HasSuffix(m.Text, "item="+item+" key=") })
	}
	// release は、fake.pane の項目 item（slow か late）の editor/open を送らせる。
	release := func(item string) {
		t.Helper()
		if err := os.WriteFile(fakeReleasePath(os.Getpid(), item), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = os.Remove(fakeReleasePath(os.Getpid(), "slow"))
		_ = os.Remove(fakeReleasePath(os.Getpid(), "late"))
	})
	pick := func(row string, between func()) {
		t.Helper()
		var before int
		h.e.do(func() { before = picked() })
		comp.keys("gg" + row + "<CR>")
		between()
		h.eventually("選んだファイルを開いた知らせ", func() bool { return picked() == before+1 })
		h.settle()
		var path string
		h.e.do(func() { path = h.e.viewOf(h.clientOf(a.term)).cur.path })
		if path != opened {
			t.Errorf("tab of the client that opened the pane = %q", path)
		}
	}
	pick("j", func() { // slow: 選んだ後、届く前に別の client でキーを打つ
		h.eventually("選んだ知らせ", func() bool { return selected("slow") })
		h.keys("ixy<Esc>")
		h.settle()
		release("slow")
	})
	pick("jj", func() { // late: 選んでから上限を過ぎて届く
		h.eventually("選んだ知らせ", func() bool { return selected("late") })
		var marked bool
		h.e.do(func() {
			var m paneMark
			if m, marked = h.e.extFromPane["fake"]; marked {
				m.at = m.at.Add(-2 * h.e.paneFocusLimit())
				h.e.extFromPane["fake"] = m
			}
		})
		if !marked {
			t.Fatal("late を選んだ後に、フォーカスを戻す印が立っていない")
		}
		release("late")
	})
	time.Sleep(100 * time.Millisecond)
	if n := focusCount(); n != 1 {
		t.Errorf("FocusPane calls = %d, want 1 (only the first selection)", n)
	}
	// 遅れずに届けば、また戻す（印は選ぶたびに立つ）。
	pick("", func() {})
	h.eventually("2 回目のフォーカスの移動", func() bool { return focusCount() == 2 })
	mu.Lock()
	if want := fmt.Sprintf("%%p%d", call.view); len(asked) == 0 || asked[len(asked)-1] != want {
		t.Errorf("PaneFocused asked %q, want the pane of the component client %q", asked, want)
	}
	mu.Unlock()

	// 利用者がキーを打たずに部品のペインから離れていれば（別の window へ移った）、1 秒以内に届いても戻さない（3 回目の確認の推奨 1）。
	mu.Lock()
	active = false
	askedBefore := len(asked)
	mu.Unlock()
	pick("", func() {})
	// PaneFocused が問われてから active を戻す。問われる前に戻すと、フォーカスを戻してしまう（#313）。
	h.eventually("PaneFocused の問い合わせ", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(asked) > askedBefore
	})
	time.Sleep(100 * time.Millisecond)
	if n := focusCount(); n != 2 {
		t.Errorf("FocusPane calls after leaving the pane = %d, want 2", n)
	}
	mu.Lock()
	active = true
	mu.Unlock()

	// ペインの中だけのキー（view/key）の後の editor/open では戻さない（オーケストレータの決定。DAP のペインのステップ実行など）。
	var before int
	h.e.do(func() { before = picked() })
	comp.keys("ggx")
	h.eventually("view/key の後に開いた知らせ", func() bool { return picked() == before+1 })
	h.settle()
	time.Sleep(100 * time.Millisecond)
	if n := focusCount(); n != 2 {
		t.Errorf("FocusPane calls after view/key = %d, want 2", n)
	}

	// ペインで :q を打ったときの view/closed の後のメッセージも、ペインを開いた client に出る。
	comp.keys(":q<CR>")
	h.eventually("ペインを開いた client の view/closed", func() bool {
		return strings.Contains(h.e.viewOf(h.clientOf(a.term)).message.Text, "kind=0")
	})
}

// テキストの部品は、カーソルが最後の行にあれば、view/update で書き足した後も最後の行へカーソルを置き、表示が末尾を追う
// （PR #248 のレビューの推奨 2）。最後の行より上を見ていれば、行の番号を保つ。
func TestTextFollowsTail(t *testing.T) {
	h := start(t, Config{})
	text := func(n int) ext.ViewComponent {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d", i+1)
		}
		return ext.ViewComponent{Kind: ext.ViewText, Text: strings.Join(lines, "\n")}
	}
	var got []int
	h.e.do(func() {
		f := h.e.newComponentFloat(text(1), "")
		h.e.setFloatContent(f, text(3))
		got = append(got, f.bs.state.Cursor().Line)
		f.bs.state.SetCursor(buffer.Position{Line: 1})
		h.e.setFloatContent(f, text(5))
		got = append(got, f.bs.state.Cursor().Line)
		h.e.closeBufferState(f.bs)
	})
	if !slices.Equal(got, []int{2, 1}) {
		t.Errorf("cursor lines = %v, want [2 1]", got)
	}
}

// 端末多重化ソフトの外（client のペインを知らない）と、ペインを開けなかったときは、pane/open の部品を、
// 元の client の画面の中央のポップアップに出す（FR-123）。中央のポップアップなので、<Esc> で閉じ、view/closed を送る。
// ペインを開けなかったときは、同じ ID のまま移すので、拡張機能は違いを知らずに view/update を送れる。
//
// req: FR-123
func TestPaneOutsideMux(t *testing.T) {
	var calls muxCalls
	calls.fail = errors.New("no pane")
	cfg := Config{Extensions: fakeSpecs(t)}
	calls.config(&cfg)
	h := start(t, cfg)

	h.keys(":fake.pane list<CR>")
	h.eventually("中央のポップアップ", func() bool { return h.e.float != nil && h.e.float.center && h.e.float.focused })
	if fi, _ := h.float(); fi.kind != floatList || fi.text != "first\nclose me\nupdate me" {
		t.Errorf("popup: %+v", fi)
	}
	calls.mu.Lock()
	n := len(calls.panes)
	calls.mu.Unlock()
	if n != 0 {
		t.Errorf("OpenPane was called outside the terminal multiplexer")
	}
	// 拡張機能は view/update で中身を替えられる。
	h.keys("jj<CR>")
	h.eventually("view/update", func() bool { return popupText(h.e) == "updated" })
	h.keys("<Esc>")
	h.waitMessage("kind=0 item= key=")
	if _, ok := h.float(); ok {
		t.Errorf("<Esc> did not close the popup")
	}

	// ペインを開けなかったときは、理由を出して中央のポップアップへ移す。
	peer := h.attach(ClientInfo{ID: 2, Pane: "%9"})
	peer.keys(":fake.pane list<CR>")
	h.eventually("OpenPane", func() bool { _, ok := calls.last(true); return ok })
	call, _ := calls.last(true)
	// 中央のポップアップへ移した後に、端末多重化ソフトのポップアップも開けずに誤りを出すので、メッセージの履歴で確かめる。
	h.eventually("ペインを開けなかった誤り", func() bool {
		return slices.ContainsFunc(h.e.msgHistory, func(m ui.Message) bool {
			return m.Text == "ペインを開けないので、中央のポップアップに出します: no pane"
		})
	})
	var id uint64
	h.e.do(func() {
		if f := h.e.viewOf(h.clientOf(peer.term)).float; f != nil && f.center && !f.pane {
			id = f.id
		}
	})
	if id != call.view {
		t.Errorf("fallback popup id = %d, want the pane id %d", id, call.view)
	}
	peer.keys("jj<CR>")
	h.eventually("移した後の view/update", func() bool {
		f := h.e.viewOf(h.clientOf(peer.term)).float
		return f != nil && f.bs.buf.Text() == "updated"
	})
	if !slices.Contains(peer.term.exits(), "") && len(peer.term.exits()) != 0 {
		t.Errorf("peer exited: %q", peer.term.exits())
	}
}

// pane/open の placement が center なら、端末多重化ソフトの中でもペインを分割せずに中央のポップアップ（端末多重化ソフトのポップアップ）に出す。
// closeOnSelect なら、木の子の無い項目を選んだら（view/select の後に）閉じて view/closed を送る。子を持つ項目の <CR> は開閉で、閉じない。
// 閉じても、選んだ選択への editor/execute の許しは残す。分割したペインは、closeOnSelect でも選んで閉じない（FR-147）。
// 分割したペインには、元の client のカーソルのあるタブが替わるたびに view/tabChange を送り、開いた時点のタブは送らない。
//
// req: FR-147
func TestPanePlacementAndCloseOnSelect(t *testing.T) {
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t)}
	calls.config(&cfg)
	h := start(t, cfg)
	peer := h.attach(ClientInfo{ID: 2, Pane: "%9"})
	popup := func() *floatState { return h.e.viewOf(h.clientOf(peer.term)).float }

	peer.keys(":fake.pane center tree<CR>")
	h.eventually("中央のポップアップ", func() bool { f := popup(); return f != nil && f.center && f.focused && !f.pane })
	h.eventually("端末多重化ソフトのポップアップ", func() bool { _, ok := calls.last(false); return ok })
	if _, ok := calls.last(true); ok {
		t.Error("pane/open with placement center split the pane")
	}
	// fake.pane が後から出す "pane <ID>" の知らせが、操作の知らせを上書きしないように、先に届くのを待つ（#313）。
	var centerView uint64
	h.e.do(func() { centerView = popup().id })
	h.waitPaneOpened(centerView)
	peer.keys("<CR>")
	h.waitMessage("kind=4 item=dir key= expanded=true")
	var open bool
	h.e.do(func() { open = popup() != nil })
	if !open {
		t.Fatal("opening a parent item closed the popup")
	}
	peer.keys("G<CR>")
	h.eventually("選んで閉じたポップアップ", func() bool { return popup() == nil })
	h.e.do(func() {
		if _, ok := h.e.execGrants["fake"]; !ok {
			t.Error("closing on select revoked the editor/execute grant of the selection")
		}
	})
	// 拡張機能には、view/select の後に view/closed が届く。
	h.eventually("view/select の後の view/closed", func() bool {
		sel := slices.IndexFunc(h.e.msgHistory, func(m ui.Message) bool { return strings.Contains(m.Text, "kind=1 item=top") })
		return sel >= 0 && slices.ContainsFunc(h.e.msgHistory[sel+1:], func(m ui.Message) bool { return strings.Contains(m.Text, "kind=0 ") })
	})

	// 分割したペインは、closeOnSelect でも選んで閉じない。元の client のタブが替わると view/tabChange（kind=5）を送る。
	peer.keys(":fake.pane closing tree<CR>")
	h.eventually("OpenPane", func() bool { _, ok := calls.last(true); return ok })
	call, _ := calls.last(true)
	comp := h.attachComponent(call.view, true)
	comp.waitScreen("▸ dir")
	h.waitPaneOpened(call.view)
	comp.keys("G<CR>")
	h.waitMessage(fmt.Sprintf("view %d kind=1 item=top", call.view))
	h.settle()
	var panes int
	var tabChanges int
	h.e.do(func() {
		panes = len(h.e.viewOf(h.clientOf(peer.term)).panes)
		for _, m := range h.e.msgHistory {
			if strings.Contains(m.Text, "kind=5") {
				tabChanges++
			}
		}
	})
	if panes != 1 {
		t.Errorf("panes after selecting in a closing split pane = %d, want 1", panes)
	}
	if tabChanges != 0 {
		t.Errorf("view/tabChange sent %d times before the tab changed", tabChanges)
	}
	dir := t.TempDir()
	peer.keys(":e " + filepath.Join(dir, "x.txt") + "<CR>")
	want := fmt.Sprintf("view %d kind=5 item=%s", call.view, filepath.Join(dir, "x.txt"))
	h.eventually("view/tabChange", func() bool {
		return slices.ContainsFunc(h.e.msgHistory, func(m ui.Message) bool { return strings.HasPrefix(m.Text, want) })
	})
}
