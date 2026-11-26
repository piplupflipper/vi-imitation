package editor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/diff"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// diffCall は、Config.SplitPane に渡された引数である。
type diffCall struct {
	from, file, dir, token string
	left                   bool
}

// env は、分割したペインの client が Hello で知らせる環境変数（待ちの印）である。
func (c diffCall) env() []string { return []string{EnvSplit + "=" + c.token} }

// recordSplit は、呼ばれた引数を calls へ送るだけの Config.SplitPane の偽物を返す。
func recordSplit(calls chan<- diffCall) func(from, file, dir, token string, left bool) error {
	return func(from, file, dir, token string, left bool) error {
		calls <- diffCall{from, file, dir, token, left}
		return nil
	}
}

// startDiffPair は、a.txt を開いた client 1（ペイン %1）と、:diffsplit b.txt で開いたペインの client 2（ペイン %2）を用意し、
// 2 つのタブの見比べを始めた harness と client 2 を返す。SplitPane の偽物は、呼ばれた引数を記録するだけで、client 2 はテストが繋ぐ。
func startDiffPair(t *testing.T, files map[string]string) (*harness, *peer, string, diffCall) {
	t.Helper()
	return startDiffPairTerm(t, newFakeTerm(), files)
}

// startDiffPairTerm は、startDiffPair の、client 1 の偽の端末を term（大きさを変えたもの）にする形である。
func startDiffPairTerm(t *testing.T, term *fakeTerm, files map[string]string) (*harness, *peer, string, diffCall) {
	t.Helper()
	dir := writeFiles(t, files)
	calls := make(chan diffCall, 1)
	h := startTerm(t, term, Config{WorkDir: dir, SplitPane: recordSplit(calls)}, "a.txt")
	h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })
	h.keys(":diffsplit b.txt<CR>")
	h.settle()
	var call diffCall
	select {
	case call = <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal(":diffsplit がペインを開かない")
	}
	b := h.attach(ClientInfo{ID: 2, Pane: "%2", WorkDir: dir, Files: []string{call.file}, Env: call.env()})
	h.settle()
	return h, b, dir, call
}

// diffSessions は、見比べている組の数を返す。
func (h *harness) diffSessions() int {
	n := 0
	h.e.do(func() { n = len(h.e.diff.sessions) })
	return n
}

// clientLine は、client c（0 から数える）のカーソルの行を返す。
func (h *harness) clientLine(c int) int {
	line := -1
	h.e.do(func() { line = h.e.viewOf(h.e.clients[c]).cur.state.Cursor().Line })
	return line
}

// topRow は、client c の表示領域の最初の画面の行を、見比べの画面の行の番号（diff.Layout.Row。詰め物を含む）で返す。
// イベントループの中（eventually の cond）で呼ぶ。
func (h *harness) topRow(c int) int {
	v := h.e.viewOf(h.e.clients[c])
	s, side := h.e.diffOf(v.cur)
	if s == nil {
		return -1
	}
	top, fill := v.renderer.DiffTop()
	return s.layout[side].Row(top) - fill
}

// :diffsplit は、元の client のペインを分割して、そこで起動した client のタブと見比べ始める（FR-99）。
// 両側の画面に差分の色と詰め物を出し、]c、[c で差分へ移り、do と dp で取り込みと送りをする（FR-101）。
// :diffoff で見比べを終える。
//
// req: FR-99, FR-101
func TestDiffSplit(t *testing.T) {
	h, b, dir, call := startDiffPair(t, map[string]string{
		"a.txt": "one\ntwo\nthree\nfour\n",
		"b.txt": "one\nTWO\nthree\n",
	})
	// :diffsplit は、いまのペインの右に開く（#437 で変えない）。
	if want := (diffCall{"%1", filepath.Join(dir, "b.txt"), dir, "1", false}); call != want {
		t.Errorf("SplitPane = %+v, want %+v", call, want)
	}
	if n := h.diffSessions(); n != 1 {
		t.Fatalf("見比べの組 = %d, want 1", n)
	}
	// A の画面: 2 行目は変えた行（diff.change の背景の色）、4 行目は A だけの行。B の画面: 最後の行の下に詰め物。
	h.e.do(func() {
		s := h.e.diff.sessions[0]
		if got := s.view[0].Lines[1].Style; got != "diff.change" {
			t.Errorf("A の 2 行目 = %q", got)
		}
		if got := s.view[0].Lines[3].Style; got != "diff.add" {
			t.Errorf("A の 4 行目 = %q", got)
		}
		if got := s.view[1].Fill[3]; got != 1 {
			t.Errorf("B の最後の行の下の詰め物 = %d, want 1", got)
		}
	})
	// B の画面の 2 行目（タブの行の下の 2 行目）の TWO は、違う文字を diff.text（背景 9）で、行末の右を diff.change（背景 5）で描く。
	ft := b.term.fakeTerm
	ft.mu.Lock()
	text, rest := ft.last.Grid.At(2, 0).Attr, ft.last.Grid.At(2, 10).Attr
	ft.mu.Unlock()
	if text.Bg != ui.Indexed(9) || rest.Bg != ui.Indexed(5) {
		t.Errorf("B の TWO の属性 = %+v、行末の右 = %+v, want 背景 9 と 5", text, rest)
	}

	// ]c と [c。回数が残りの差分より多ければ、最後の差分へ移る（Vim 9.1 と同じ）。
	h.keys("]c")
	h.settle()
	if got := h.clientLine(0); got != 1 {
		t.Errorf("]c の後の行 = %d, want 1", got)
	}
	h.keys("gg5]c")
	h.settle()
	if got := h.clientLine(0); got != 3 {
		t.Errorf("5]c の後の行 = %d, want 3", got)
	}
	h.keys("[c")
	h.settle()
	if got := h.clientLine(0); got != 1 {
		t.Errorf("[c の後の行 = %d, want 1", got)
	}

	// do は B の TWO を A に取り込み、dp は A の four を B へ送る。
	h.keys("do")
	h.settle()
	if got := h.bufferText("a.txt"); got != "one\nTWO\nthree\nfour" {
		t.Errorf("do の後の a.txt = %q", got)
	}
	h.keys("Gdp")
	h.settle()
	if got := h.bufferText("b.txt"); got != "one\nTWO\nthree\nfour" {
		t.Errorf("dp の後の b.txt = %q", got)
	}
	h.e.do(func() {
		if n := len(h.e.diff.sessions[0].res.Hunks); n != 0 {
			t.Errorf("do と dp の後の差分 = %d, want 0", n)
		}
	})
	// do で変えたバッファは、u で 1 回で戻る。
	h.keys("ggu")
	h.settle()
	if got := h.bufferText("a.txt"); got != "one\ntwo\nthree\nfour" {
		t.Errorf("u の後の a.txt = %q", got)
	}

	b.keys(":diffoff<CR>")
	h.settle()
	if n := h.diffSessions(); n != 0 {
		t.Errorf(":diffoff の後の組 = %d", n)
	}
	if s := h.term.screen(); strings.Contains(s, "-----") {
		t.Errorf(":diffoff の後に詰め物が残った:\n%s", s)
	}
}

// 片側を編集すると、差分を計算し直す（FR-99）。片側をスクロールすると、もう片側の表示の最初の行を、
// 詰め物を含めた同じ画面の行にそろえ、カーソルを対応する行へ動かす（FR-100）。
//
// req: FR-99, FR-100
func TestDiffScrollAndUpdate(t *testing.T) {
	var a, b strings.Builder
	for i := range 30 {
		line := string(rune('a'+i%26)) + "\n"
		a.WriteString(line)
		if i < 5 || i >= 10 {
			b.WriteString(line) // B は 6〜10 行目が無い
		}
	}
	h, p, _, _ := startDiffPair(t, map[string]string{"a.txt": a.String(), "b.txt": b.String()})

	// A で G と打つと、B も最後の行が見える位置へそろい、カーソルは対応する行へ移る。
	h.keys("G")
	h.settle()
	h.eventually("スクロールのそろえ", func() bool { return h.topRow(0) == h.topRow(1) && h.topRow(0) > 0 })
	if got := h.clientLine(1); got != 24 {
		t.Errorf("B のカーソルの行 = %d, want 24（A の 29 行目に対応する行）", got)
	}

	// A で gg と戻り、B で 8j と打つと、今度は B に合わせて A がそろう。
	h.keys("gg")
	h.settle()
	p.keys("8j")
	h.settle()
	h.eventually("B に合わせたそろえ", func() bool { return h.topRow(0) == h.topRow(1) })
	if got, want := h.clientLine(0), 13; got != want {
		t.Errorf("A のカーソルの行 = %d, want %d", got, want)
	}

	// B の 5 行目の後に F を入れると、計算し直して、F を A の f と組にした変えた行にする。
	p.keys("gg5GoF<Esc>")
	h.settle()
	h.eventually("計算し直し", func() bool {
		s := h.e.diff.sessions[0]
		return s.res != nil && !s.stale() && s.view[1].Lines[5].Style == ui.StyleDiffChange
	})
}

// 見比べを始められない場合と、見比べていないタブの do と dp を確かめる。
//
// req: FR-99, FR-101
func TestDiffErrors(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n", "c.txt": "c\n"})
	h := start(t, Config{WorkDir: dir}, "a.txt")

	// 端末多重化ソフトの外（ペインを知らない、SplitPane が無い、client が 1 つ）では :diffsplit も :diffthis も使えない。
	for _, cmd := range []string{":diffsplit b.txt<CR>", ":diffthis<CR>"} {
		h.keys(cmd)
		h.settle()
		if msg := h.message(); !strings.Contains(msg, "端末多重化ソフト") {
			t.Errorf("%s の外のメッセージ = %q", cmd, msg)
		}
	}
	h.keys(":diffsplit<CR>")
	h.settle()
	if msg := h.message(); !strings.HasPrefix(msg, "E471") {
		t.Errorf("引数の無い :diffsplit = %q", msg)
	}
	// 見比べていないタブの do と dp は、Vim 9.1 と同じく E99 で、バッファは変えない。
	for _, k := range []string{"do", "dp"} {
		h.keys(k)
		h.settle()
		if msg := h.message(); !strings.HasPrefix(msg, "E99") {
			t.Errorf("見比べていない %s = %q", k, msg)
		}
	}
	if got := h.bufferText("a.txt"); got != "a" {
		t.Errorf("a.txt = %q", got)
	}

	p := h.attach(ClientInfo{ID: 2, WorkDir: dir, Files: []string{filepath.Join(dir, "b.txt")}, ReadOnly: true})
	// 同じ client の 2 つのタブは見比べられない。同じ client で打ち直すと、後のタブが相手を待つ。
	h.keys(":difft<CR>:e c.txt<CR>:diffthis<CR>")
	h.settle()
	if msg := h.message(); !strings.Contains(msg, "別の client") {
		t.Errorf("同じ client の :diffthis = %q", msg)
	}
	if n := h.diffSessions(); n != 0 {
		t.Errorf("組 = %d", n)
	}
	// 同じ client のタブのファイルへの :diffsplit も断る。
	h.e.do(func() { h.e.cfg.SplitPane = func(string, string, string, string, bool) error { return nil } })
	h.keys(":diffsplit a.txt<CR>")
	h.settle()
	if msg := h.message(); !strings.Contains(msg, "この client のタブ") {
		t.Errorf("同じ client のタブへの :diffsplit = %q", msg)
	}

	// 別の client のタブどうしは :diffthis で見比べ始める。相手が編集を禁止していれば dp を断る（E21）。
	p.keys(":diffthis<CR>")
	h.settle()
	if n := h.diffSessions(); n != 1 {
		t.Fatalf(":diffthis の後の組 = %d", n)
	}
	h.keys("dp")
	h.settle()
	if msg := h.message(); !strings.HasPrefix(msg, "E21") {
		t.Errorf("編集を禁止した相手への dp = %q", msg)
	}
	if got := h.bufferText("b.txt"); got != "b" {
		t.Errorf("b.txt = %q", got)
	}
	// do は相手が編集を禁止していても取り込める。
	h.keys("do")
	h.settle()
	if got := h.bufferText("c.txt"); got != "b" {
		t.Errorf("do の後の c.txt = %q", got)
	}
	// 見比べているタブの :diffthis は、そのことを知らせる。
	h.keys(":diffthis<CR>")
	h.settle()
	if msg := h.message(); !strings.Contains(msg, "既に見比べています") {
		t.Errorf("見比べているタブの :diffthis = %q", msg)
	}
	// 見比べているバッファを閉じると、見比べも終わり、相手の client に知らせる。
	p.keys(":q<CR>")
	h.settle()
	h.eventually("組が終わる", func() bool { return len(h.e.diff.sessions) == 0 })
	if msg := h.message(); !strings.Contains(msg, "見比べが終わりました") {
		t.Errorf("相手のタブを閉じた後のメッセージ = %q", msg)
	}
}

// 3 つ目の client が見比べに加わると、前の組の残る側に組が終わったことを知らせ、:diffthis の待ちを残さない（PR #232 のレビューの推奨 4）。
//
// req: FR-99
func TestDiffThirdClient(t *testing.T) {
	h, _, dir, _ := startDiffPair(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n", "r.txt": "r\n"})
	r := h.attach(ClientInfo{ID: 3, Pane: "%3", WorkDir: dir, Files: []string{filepath.Join(dir, "r.txt")}})
	r.keys(":diffthis<CR>:diffsplit b.txt<CR>")
	h.settle()
	h.e.do(func() {
		if n := len(h.e.diff.sessions); n != 1 {
			t.Fatalf("組 = %d", n)
		}
		s := h.e.diff.sessions[0]
		if filepath.Base(s.bs[0].path) != "r.txt" || filepath.Base(s.bs[1].path) != "b.txt" {
			t.Errorf("組 = %s と %s, want r.txt と b.txt", s.bs[0].path, s.bs[1].path)
		}
		if h.e.diff.this != nil {
			t.Errorf(":diffthis の待ちが残った: %s", h.e.diff.this.path)
		}
	})
	var msg string
	h.e.do(func() { msg = h.e.viewOf(h.e.clients[0]).message.Text })
	if !strings.Contains(msg, "a.txt の見比べが終わりました") {
		t.Errorf("前の組の a.txt の client のメッセージ = %q", msg)
	}
}

// do と dp は、Vim 9.1 と同じく d のオペレーターの後の o と p として読むので、d の後に時間を置いても do と dp になる（PR #232 のレビューの必須 1）。
// d の後のモーション（dw、dd、d2j）と、レジスターを付けた "ado も変わらない。
//
// req: FR-101
func TestDiffObtainAfterPause(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "a\nb\nc\n", "b.txt": "a\nB\nc\n"})
	calls := make(chan diffCall, 1)
	h := start(t, Config{WorkDir: dir, KeyTimeout: 20 * time.Millisecond, SplitPane: recordSplit(calls)}, "a.txt")
	h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })
	h.keys(":diffsplit b.txt<CR>")
	h.settle()
	call := <-calls
	p := h.attach(ClientInfo{ID: 2, Pane: "%2", WorkDir: dir, Files: []string{call.file}, Env: call.env()})

	// 時間切れ（20 ms）より長く置いてから o を打つ。
	h.keys("jd")
	h.settle()
	time.Sleep(100 * time.Millisecond)
	h.keys("o")
	h.settle()
	if got := h.bufferText("a.txt"); got != "a\nB\nc" {
		t.Errorf("d、時間を置いて o の後の a.txt = %q", got)
	}
	h.keys("u")
	p.keys("2Gd")
	h.settle()
	time.Sleep(100 * time.Millisecond)
	p.keys("p")
	h.settle()
	if got := h.bufferText("a.txt"); got != "a\nB\nc" {
		t.Errorf("d、時間を置いて p の後の a.txt = %q", got)
	}
	// d の後のモーションは、時間を置いても変わらない。
	h.keys("u")
	h.settle()
	h.keys("gg\"ayyd")
	h.settle()
	time.Sleep(100 * time.Millisecond)
	h.keys("j")
	h.settle()
	if got := h.bufferText("a.txt"); got != "c" {
		t.Errorf("d、時間を置いて j の後の a.txt = %q", got)
	}
}

// 計算の結果の画面の行（diff.Layout）は、両側で数が等しい。startDiffPair の 2 つのタブで確かめる（computeDiff の組み立ての確認）。
func TestComputeDiffViews(t *testing.T) {
	res, layouts, views := computeDiff([2]diff.Text{diff.Parse("x\nab\n"), diff.Parse("ac\ny\nz\n")})
	if len(res.Hunks) == 0 {
		t.Fatal("差分が無い")
	}
	if layouts[0].Rows() != layouts[1].Rows() {
		t.Errorf("Rows = %d, %d", layouts[0].Rows(), layouts[1].Rows())
	}
	for i, v := range views {
		for line, dl := range v.Lines {
			for _, r := range dl.Text {
				if r.Start >= r.End {
					t.Errorf("side %d line %d: 空の範囲 %+v", i, line, r)
				}
			}
		}
	}
}
