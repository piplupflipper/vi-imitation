package editor

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
)

// windowSends は、テストが差し替えた window/change の送り先が受け取ったものである。
type windowSends struct {
	mu sync.Mutex
	ps []ext.WindowChangeParams
}

func (w *windowSends) add(_ string, p ext.WindowChangeParams) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ps = append(w.ps, p)
	return nil
}

// count は、受け取ったものの数を返す。
func (w *windowSends) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.ps)
}

// has は、cond を満たすものを受け取ったかを返す。
func (w *windowSends) has(cond func(ext.WindowChangeParams) bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.ContainsFunc(w.ps, cond)
}

// fireLast は、最後に掛けたタイマーの時間が来たことにして、イベントループが処理するのを待つ。
func (ft *fakeTimers) fireLast(h *harness) {
	ft.fire(ft.count() - 1)
	h.waitLoop()
}

// take は、受け取ったものを返して空にする。
func (w *windowSends) take() []ext.WindowChangeParams {
	w.mu.Lock()
	defer w.mu.Unlock()
	ps := w.ps
	w.ps = nil
	return ps
}

// startWindows は、40 列 12 行の端末（タブの行の下の 9 行が表示領域）で 100 行の a.txt を開き、window/change の送り先を
// 拡張機能 "w" だけにして、送ったものを集める。タイマーは偽物に差し替える。
func startWindows(t *testing.T) (*harness, *windowSends, *fakeTimers) {
	t.Helper()
	dir := writeFiles(t, map[string]string{"a.txt": barLines(100)})
	term := newFakeTerm()
	term.w, term.h = 40, 12
	h := startTerm(t, term, Config{WorkDir: dir}, "a.txt")
	sends, ft := &windowSends{}, &fakeTimers{}
	h.e.do(func() {
		h.e.afterFunc = ft.after
		h.e.windows.exts = func() []string { return []string{"w"} }
		h.e.windows.send = sends.add
	})
	h.settle()
	// 差し替えた後の最初の描画（e.do の後の描画）で、最初の client（ID は 0）の窓を送り、間引きのタイマーを掛ける。
	h.eventually("最初の知らせ", func() bool { return sends.count() == 1 && ft.count() == 1 })
	if got := sends.take(); got[0].Window != "client/0" || got[0].Cursor.Line != 0 {
		t.Fatalf("最初の知らせ = %+v", got)
	}
	ft.fire(0)
	h.waitLoop()
	return h, sends, ft
}

// 描いた後に、窓の見えている行の範囲、カーソル、モードが前に送ったものと違えば window/change を送る。送った後の 150 ミリ秒は送らず、
// 時間が来たら、その間に変わった最後の中身を 1 回だけ送る（nvim-treesitter-context の throttle_by_id と同じく 2 回まで）。
// その間に変わらなければ送らない。
//
// req: FR-235, FR-236
func TestWindowChangeThrottle(t *testing.T) {
	h, sends, ft := startWindows(t)
	h.keys("j")
	h.settle()
	got := sends.take()
	if len(got) != 1 {
		t.Fatalf("1 回目 = %+v", got)
	}
	want := ext.WindowChangeParams{
		Window: "client/0", Buffer: 1, Version: got[0].Version, Top: 0, Bottom: 9, Rows: 9, Columns: 40,
		Cursor: ext.Position{Line: 1}, Mode: ext.ModeNormal,
	}
	if got[0] != want {
		t.Fatalf("1 回目 = %+v, want %+v", got[0], want)
	}
	if ft.count() != 2 || ft.ds[1] != windowNotifyInterval {
		t.Fatalf("間引きのタイマー = %d 個 %v", ft.count(), ft.ds)
	}
	// 間引きの時間の間は送らず、時間が来たら最後の中身だけを送る。
	h.keys("j")
	h.keys("20j")
	h.keys("i")
	h.settle()
	if early := sends.take(); len(early) != 0 {
		t.Fatalf("間引きの間に送った: %+v", early)
	}
	ft.fire(1)
	h.eventually("時間が来た後の知らせ", func() bool { return sends.count() > 0 })
	got = sends.take()
	// 20j は表示領域の行数より遠くへ動くので、カーソルの行を表示領域の中ほどに置く（最初の行は 18）。
	if len(got) != 1 || got[0].Cursor.Line != 22 || got[0].Mode != ext.ModeInsert || got[0].Top != 18 || got[0].Bottom != 27 {
		t.Fatalf("時間が来た後 = %+v", got)
	}
	// 送ったので、もう 1 回間引きの時間を掛ける。その間に何も変わらなければ、時間が来ても送らず、タイマーも掛けない。
	if ft.count() != 3 {
		t.Fatalf("2 回目の間引きのタイマー = %d 個", ft.count())
	}
	ft.fire(2)
	h.waitLoop()
	if got := sends.take(); len(got) != 0 || ft.count() != 3 {
		t.Fatalf("変わらない間引きの後 = %+v、タイマー %d 個", got, ft.count())
	}
	// 間引きの時間の外なら、すぐに送る。変わらない描画（同じ位置への移動）では送らない。
	h.keys("<Esc>")
	h.settle()
	if got := sends.take(); len(got) != 1 || got[0].Mode != ext.ModeNormal || got[0].Cursor.Line != 22 {
		t.Fatalf("<Esc> の後 = %+v", got)
	}
}

// 送る相手の拡張機能の状態が変わったら（再起動を含む）、送った中身を忘れ、次に描いた後にすべての窓を送り直す。
// 繋いだ client の窓は別の識別子で送り、client が終わったら、閉じた知らせを送る。
//
// req: FR-235
func TestWindowChangeClients(t *testing.T) {
	h, sends, ft := startWindows(t)
	h.keys("j")
	h.settle()
	sends.take()
	ft.fireLast(h)

	h.e.do(func() {
		h.e.handleEvent(&exthost.StateChange{Extension: "w", State: exthost.StateRunning})
		h.e.render()
	})
	if got := sends.take(); len(got) != 1 || got[0].Window != "client/0" || got[0].Cursor.Line != 1 {
		t.Fatalf("稼働中になった後 = %+v", got)
	}
	ft.fireLast(h)

	dir := t.TempDir()
	if err := os.WriteFile(dir+"/b.txt", []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := h.attach(ClientInfo{ID: 2, Files: []string{dir + "/b.txt"}})
	isSecond := func(s ext.WindowChangeParams) bool { return s.Window == "client/2" && !s.Closed }
	h.eventually("繋いだ client の窓", func() bool { return sends.has(isSecond) })
	for _, s := range sends.take() {
		if isSecond(s) && s.Buffer == 1 {
			t.Fatalf("繋いだ client の窓のバッファ = %+v", s)
		}
	}
	ft.fireLast(h)
	p.keys(":q<CR>")
	h.settle()
	ft.fireLast(h)
	h.eventually("閉じた窓の知らせ", func() bool {
		return sends.has(func(s ext.WindowChangeParams) bool { return s.Window == "client/2" && s.Closed })
	})
}

// 帯を窓の上端に描き、ノーマルモードで帯の行を押すと、その行の先頭へ移って移る前の位置をジャンプリストに記録する。
// インサートモードとビジュアルモードでは帯の下の本文を押したことにし、作った後に本文が変わった帯の行と、行の無い帯の行も本文である。
// 拡張機能が止まったら帯を消す。
//
// req: FR-237, FR-238
func TestBandClick(t *testing.T) {
	h, _, _ := startWindows(t)
	h.keys("50Gzt")
	h.settle()
	var version ext.Version
	set := func(lines ...ext.BandLine) {
		h.e.do(func() {
			version = ext.Version(h.e.cur.buf.Version())
			h.e.setBand("w", ext.BandSetParams{Window: windowIDOf(h.e.view), Buffer: h.e.cur.id, Version: version, Lines: lines})
			h.e.render()
		})
	}
	set(ext.BandLine{Text: "line 10", Line: ext.SourceLine(9)}, ext.BandLine{Text: "---"})
	// zt の後はカーソルが最初の行にあるので、帯はカーソルの行を隠さないよう描かない。
	if row := h.screenRow(1); !strings.HasPrefix(row, "line 50") {
		t.Fatalf("カーソルが最初の行のときの 1 行目 = %q", row)
	}
	h.keys("5j")
	h.settle()
	set(ext.BandLine{Text: "line 10", Line: ext.SourceLine(9)}, ext.BandLine{Text: "---"})
	if r1, r2, r3 := h.screenRow(1), h.screenRow(2), h.screenRow(3); !strings.HasPrefix(r1, "line 10") || !strings.HasPrefix(r2, "---") || !strings.HasPrefix(r3, "line 52") {
		t.Fatalf("帯 = %q %q %q", r1, r2, r3)
	}
	// インサートモードでは、帯の下の本文（49 行目。0 から数える）を押したことにする。
	h.keys("i")
	h.click(1, 2)
	if cur, m := h.cursor(), h.mode(); cur != (buffer.Position{Line: 49, Byte: 2}) || m != command.ModeInsert {
		t.Fatalf("インサートモードで帯を押した後 = %+v %v", cur, m)
	}
	h.keys("<Esc>5j")
	h.settle()
	// 行の無い帯の行は本文である。
	h.click(2, 1)
	if cur := h.cursor(); cur.Line != 50 {
		t.Fatalf("行の無い帯の行を押した後 = %+v", cur)
	}
	h.keys("5j")
	h.settle()
	h.click(1, 5)
	if cur := h.cursor(); cur != (buffer.Position{Line: 9}) {
		t.Fatalf("帯を押した後 = %+v, want 9:0", cur)
	}
	h.keys("<C-o>")
	h.settle()
	if cur := h.cursor(); cur.Line != 55 {
		t.Fatalf("<C-o> の後 = %+v, want 55 行目", cur)
	}
	// ビジュアルモードでは、選択を終えて帯の下の本文へ移る。ホイールは表示領域を送る。
	h.keys("v")
	h.settle()
	vtop := h.viewTop()
	h.click(1, 0)
	if cur, m := h.cursor(), h.mode(); cur.Line != vtop || m != command.ModeNormal {
		t.Fatalf("ビジュアルモードで帯を押した後 = %+v %v, want %d 行目のノーマルモード", cur, m, vtop)
	}
	h.keys("5j")
	h.settle()
	vtop = h.viewTop()
	h.mouse("<ScrollWheelDown>", 1, 0)
	if got := h.viewTop(); got != vtop+3 {
		t.Fatalf("帯の上のホイールの後の最初の行 = %d, want %d", got, vtop+3)
	}

	// 作った後に本文が変わった帯の行は、押してもその行へ移らない。
	h.keys("zt5j")
	h.settle()
	set(ext.BandLine{Text: "line 10", Line: ext.SourceLine(9)})
	h.keys("x")
	h.settle()
	top := h.viewTop()
	h.click(1, 0)
	if cur := h.cursor(); cur.Line != top {
		t.Fatalf("本文が変わった後に帯を押した後 = %+v, want 帯の下の %d 行目", cur, top)
	}

	// 拡張機能が止まったら帯を消す。
	h.keys("5j")
	h.settle()
	set(ext.BandLine{Text: "BAND", Line: ext.SourceLine(0)})
	if row := h.screenRow(1); !strings.HasPrefix(row, "BAND") {
		t.Fatalf("帯 = %q", row)
	}
	h.e.do(func() {
		h.e.handleEvent(&exthost.StateChange{Extension: "w", State: exthost.StateRestartWait})
		h.e.render()
	})
	if row := h.screenRow(1); strings.HasPrefix(row, "BAND") {
		t.Fatalf("止まった後の帯 = %q", row)
	}
}

// windowSpecs は、window/change を受けて帯を置く拡張機能（ファイルの種類 go で起動する）を返す。
func windowSpecs(t *testing.T) []exthost.Spec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := ext.Manifest{
		ID: "win", Command: exe, Args: []string{fakeExtFlag, fakeWindow}, WindowEvents: true,
		Activation: []ext.Activation{{On: ext.ActivationFileType, Pattern: "go"}},
	}
	if _, err := ext.ValidateManifests([]ext.Manifest{m}); err != nil {
		t.Fatal(err)
	}
	return []exthost.Spec{{Manifest: m, Dir: t.TempDir()}}
}

// 拡張機能のプロセスを通して、window/change を受けた拡張機能が band/set で置いた帯を、窓の上端に描く。
//
// req: FR-235, FR-237
func TestBandFromExtension(t *testing.T) {
	var b strings.Builder
	b.WriteString("func main() {\n")
	for i := range 60 {
		fmt.Fprintf(&b, "\tx%d()\n", i)
	}
	b.WriteString("}\n")
	dir := writeFiles(t, map[string]string{"main.go": b.String()})
	term := newFakeTerm()
	term.w, term.h = 40, 12
	h := startTerm(t, term, Config{WorkDir: dir, Extensions: windowSpecs(t)}, "main.go")
	h.eventually("拡張機能の起動", func() bool { return h.e.host != nil && len(h.e.host.WindowEventExtensions()) == 1 })
	h.keys("40G")
	h.settle()
	h.eventually("帯", func() bool { return strings.HasPrefix(h.screenRow(1), "func main() {") && h.viewTopInLoop() > 0 })
	// 最初の行が見える位置へ戻ると、拡張機能が帯を消す。
	h.keys("gg")
	h.settle()
	h.eventually("帯を消す", func() bool { return len(h.e.bands) == 0 })
	if row := h.screenRow(2); !strings.HasPrefix(row, "\tx0()") && !strings.HasPrefix(row, "        x0()") {
		t.Errorf("帯を消した後の 2 行目 = %q", row)
	}
}

// viewTopInLoop は、イベントループの goroutine の上で、読み込んでいる client の表示領域の最初の行を返す（eventually の中で使う）。
func (h *harness) viewTopInLoop() int { return h.e.renderer.Top() }
