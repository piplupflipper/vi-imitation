package editor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
	"github.com/piplupflipper/vi-imitation/internal/command"
)

// fakeCompleteWords は、fakeComplete が返す候補の語と種類である。
var fakeCompleteWords = []struct{ word, kind string }{
	{"alpha", "variable"}, {"alps", "function"}, {"beta", "variable"}, {"あいう", "variable"}, {"あかい", "module"},
}

// fakeComplete は、偽の拡張機能の console/complete の答えである（FR-297）。カーソルの前の語（空白、"."、"(" の後ろから
// カーソルまで）で始まる fakeCompleteWords を、その語を置き換える範囲の候補にして返す。問い合わせを受けたことを
// "complete seq=<番号> text=<中身> cursor=<位置>" と出す。中身に応じて次のとおり答える。
//
//   - fail を含む: OK を false にする
//   - none を含む: 候補を 0 件にする
//   - slow を含む: slowDelay の後に答える（その前に取り消されたら誤りで答える）
//   - hang を含む: 取り消されるまで答えない
func fakeComplete(ctx context.Context, s *server.Server, p *ext.ConsoleCompleteParams, reply func(ext.ConsoleCompleteResult, error)) {
	_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("complete seq=%d text=%s cursor=%d", p.Seq, p.Text, p.Cursor))
	before := p.Text[:min(max(p.Cursor, 0), len(p.Text))]
	start := strings.LastIndexAny(before, " .(") + 1
	res := ext.ConsoleCompleteResult{OK: !strings.Contains(p.Text, "fail")}
	if res.OK && !strings.Contains(p.Text, "none") {
		for _, w := range fakeCompleteWords {
			if strings.HasPrefix(w.word, before[start:]) {
				res.Items = append(res.Items, ext.ConsoleCompletionItem{Label: w.word, Start: start, End: p.Cursor, Kind: w.kind})
			}
		}
	}
	switch {
	case strings.Contains(p.Text, "hang"):
		go func() {
			<-ctx.Done()
			reply(ext.ConsoleCompleteResult{}, ctx.Err())
		}()
	case strings.Contains(p.Text, "slow"):
		go func() {
			select {
			case <-time.After(slowDelay):
				reply(res, nil)
			case <-ctx.Done():
				reply(ext.ConsoleCompleteResult{}, ctx.Err())
			}
		}()
	default:
		reply(res, nil)
	}
}

// startComplete は、80 列 24 行の偽の端末でエディタを起動し、:fake.popup kind で補完を引き受ける組（complete）を中央に開く。
func startComplete(t *testing.T) *harness {
	t.Helper()
	term := newFakeTerm()
	term.w, term.h = 80, 24
	h := startTerm(t, term, Config{Extensions: fakeSpecs(t)})
	h.keys(":fake.popup complete<CR>")
	h.eventually("組", func() bool { return h.e.float != nil && h.e.float.kind == floatConsole })
	var view uint64
	h.e.do(func() { view = h.e.float.id })
	h.waitPopupOpened(view)
	return h
}

// menuInfo は、組の補完の一覧をテストで比べる形にしたものである。
type menuInfo struct {
	items    string // 一覧の項目を , でつないだもの。出していなければ空
	selected int
	input    string
	cursor   int
	mode     command.Mode
}

// completeMenuOf は、組 f の補完の一覧と入力の行の状態を返す。イベントループの goroutine で呼ぶ。
// 分割したペインの部品はキーの間はフォーカスを外しているので、描くとき（paneComponentFrame）と同じくフォーカスを入れて決める。
func completeMenuOf(e *Editor, f *floatState) menuInfo {
	focused := f.focused
	f.focused = true
	defer func() { f.focused = focused }()
	mi := menuInfo{input: f.bs.buf.Line(0), cursor: f.bs.state.Cursor().Byte, mode: f.bs.state.Mode(), selected: -1}
	if m := e.uiConsoleMenu(f); m != nil {
		mi.items, mi.selected = strings.Join(m.Items, ","), m.Selected
	}
	return mi
}

// cmenu は、読み込んでいる client の中央のポップアップの組の補完の一覧を返す。
func (h *harness) cmenu() (mi menuInfo) {
	h.settle()
	h.e.do(func() {
		if f := h.e.float; f != nil && f.con != nil {
			mi = completeMenuOf(h.e, f)
		}
	})
	return mi
}

// waitMenu は、中央のポップアップの組の補完の一覧の項目が items になるまで待つ。
func (h *harness) waitMenu(items string) {
	h.t.Helper()
	h.settle()
	h.eventually("補完の一覧 "+items, func() bool {
		f := h.e.float
		return f != nil && f.con != nil && completeMenuOf(h.e, f).items == items
	})
}

// 打つたびに拡張機能へ問い合わせ、返った候補を一覧に出す（前もって選ばない。種類も出す）。<C-n>、<Tab>、<Down> で次の、
// <C-p>、<S-Tab>、<Up> で前の候補を選び、端の先は選んでいない状態へ戻る。<CR> は選んだ候補を入れ、送らない。
// 入れた後は一覧を出さず、次の <CR> で中身を送る。選んでいないときの <CR> は、一覧を閉じて中身を送る。
//
// req: FR-297
func TestConsoleCompleteSelect(t *testing.T) {
	h := startComplete(t)
	h.keys("al")
	h.waitMenu("alpha,alps")
	if mi := h.cmenu(); mi.selected != -1 || mi.input != "al" {
		t.Errorf("出したとき: %+v", mi)
	}
	if scr := h.term.screen(); !strings.Contains(scr, "alpha variable") || !strings.Contains(scr, "alps  function") {
		t.Errorf("一覧の画面:\n%s", scr)
	}
	for _, tt := range []struct {
		keys string
		want int
	}{
		{"<C-n>", 0}, {"<Tab>", 1}, {"<Down>", -1}, {"<C-n>", 0}, {"<C-p>", -1}, {"<S-Tab>", 1}, {"<Up>", 0},
	} {
		h.keys(tt.keys)
		if mi := h.cmenu(); mi.selected != tt.want || mi.input != "al" {
			t.Fatalf("%s の後: %+v, want selected %d", tt.keys, mi, tt.want)
		}
	}
	h.keys("<CR>")
	if mi := h.cmenu(); mi.input != "alpha" || mi.cursor != len("alpha") || mi.items != "" || mi.mode != command.ModeInsert {
		t.Fatalf("<CR> で入れた後: %+v", mi)
	}
	if h.seen("kind=1 item=") {
		t.Fatal("候補を入れた <CR> で送った")
	}
	h.keys("<CR>")
	h.waitSeen("kind=1 item=alpha")

	// 選んでいないときの <CR> は、一覧を閉じて送る。
	h.keys("be")
	h.waitMenu("beta")
	h.keys("<CR>")
	h.waitSeen("kind=1 item=be key=")
	if mi := h.cmenu(); mi.input != "" || mi.items != "" {
		t.Errorf("送った後: %+v", mi)
	}
}

// 候補は、カーソルの前の語の一部（置き換える範囲）だけを置き換え、カーソルの後ろの文字を残す。カーソルだけを動かすと一覧を閉じ、
// 補完のキー（一覧を出していないときの <Tab>）で問い合わせ直す。<C-y> も選んだ候補を入れ、<C-e> は一覧を閉じる。
// 多バイト文字の語も、文字の境目で置き換える。カーソルの前が空白なら問い合わせない。
//
// req: FR-297
func TestConsoleCompleteRange(t *testing.T) {
	h := startComplete(t)
	h.keys("x.alZZ")
	h.settle()
	h.keys("<Left><Left>")
	if mi := h.cmenu(); mi.items != "" || mi.cursor != len("x.al") {
		t.Fatalf("カーソルを動かした後: %+v", mi)
	}
	h.keys("<Tab>")
	h.waitSeen("complete seq=")
	h.waitMenu("alpha,alps")
	h.keys("<Tab><Tab><C-y>")
	if mi := h.cmenu(); mi.input != "x.alpsZZ" || mi.cursor != len("x.alps") || mi.items != "" {
		t.Fatalf("<C-y> で入れた後: %+v", mi)
	}

	// <C-e> で閉じる。閉じた後の補完のキーでは問い合わせ直す。
	h.keys("<End><C-u>(")
	h.waitMenu("alpha,alps,beta,あいう,あかい")
	h.keys("<C-e>")
	if mi := h.cmenu(); mi.items != "" || mi.input != "(" {
		t.Fatalf("<C-e> の後: %+v", mi)
	}

	// 多バイト文字。打ち足した文字で、次の答えを待たずに前の候補を絞り込む。
	h.keys("あ")
	h.waitMenu("あいう,あかい")
	h.keys("<C-n><C-n><CR>")
	if mi := h.cmenu(); mi.input != "(あかい" || mi.cursor != len("(あかい") {
		t.Fatalf("多バイト文字の候補を入れた後: %+v", mi)
	}

	// カーソルの前が空白なら問い合わせず、一覧を閉じる。
	h.keys(" ")
	if mi := h.cmenu(); mi.items != "" {
		t.Errorf("空白の後: %+v", mi)
	}
}

// 候補が 0 件の答え、OK が false の答えでは一覧を出さず、<CR> は送る。
//
// req: FR-297
func TestConsoleCompleteEmpty(t *testing.T) {
	h := startComplete(t)
	for _, text := range []string{"zz", "none", "fail a"} {
		h.keys(text)
		h.waitSeen("complete seq=")
		h.waitShown(fmt.Sprintf("complete seq=%d text=%s cursor=%d", h.completeSeq(), text, len(text)))
		if mi := h.cmenu(); mi.items != "" {
			t.Errorf("%s: %+v", text, mi)
		}
		h.keys("<CR>")
		h.waitSeen("kind=1 item=" + text + " key=")
	}
}

// completeSeq は、中央のポップアップの組の、最後に送った補完の問い合わせの番号を返す。
func (h *harness) completeSeq() (seq uint64) {
	h.settle()
	h.e.do(func() { seq = h.e.float.con.comp.seq })
	return seq
}

// 遅い答えを待つ間もキーの処理を続ける。答えの前に入力の行の中身が変わった（打ち足した）なら、その答えを捨て、
// 次の問い合わせの答えを出す。答えの前に消して空白で終わる中身にしたら、問い合わせを取り消し、遅れて届く答えも出さない。
// 答えの前に <CR> で送れば、送った中身で一覧を出さない。
//
// req: FR-297
func TestConsoleCompleteSlow(t *testing.T) {
	h := startComplete(t)
	h.keys("slow a")
	h.waitSeen("text=slow a cursor=6")
	first := h.completeSeq()
	h.keys("lph")
	h.settle()
	if mi := h.cmenu(); mi.input != "slow alph" {
		t.Fatalf("待つ間のキー: %+v", mi)
	}
	h.waitMenu("alpha")
	if seq := h.completeSeq(); seq != first+3 {
		t.Errorf("問い合わせの番号 = %d, want %d", seq, first+3)
	}

	// 答えの前に消す。
	h.keys("<C-u>slow al")
	h.waitSeen("text=slow al cursor=7")
	h.keys("<BS><BS>")
	time.Sleep(2 * slowDelay)
	if mi := h.cmenu(); mi.items != "" || mi.input != "slow " {
		t.Errorf("消した後に届いた答え: %+v", mi)
	}

	// 答えの前に送る。
	h.keys("<C-u>slow be<CR>")
	h.waitSeen("kind=1 item=slow be key=")
	time.Sleep(2 * slowDelay)
	if mi := h.cmenu(); mi.items != "" || mi.input != "" {
		t.Errorf("送った後に届いた答え: %+v", mi)
	}

	// 答えない問い合わせを待つ間も打てて、閉じると取り消す。
	h.keys("hang a")
	h.waitSeen("text=hang a cursor=6")
	h.keys("<Esc><Esc>")
	h.eventually("閉じる", func() bool { return h.e.float == nil })
}

// 一覧を出したままの <Esc> は、一覧を閉じてノーマルモードへ戻る。入力の行の中身は変えず、インサートモードに入り直しても出さない。
//
// req: FR-297
func TestConsoleCompleteEsc(t *testing.T) {
	h := startComplete(t)
	h.keys("al")
	h.waitMenu("alpha,alps")
	h.keys("<C-n><Esc>")
	if mi := h.cmenu(); mi.items != "" || mi.input != "al" || mi.mode != command.ModeNormal {
		t.Fatalf("<Esc> の後: %+v", mi)
	}
	h.keys("a")
	if mi := h.cmenu(); mi.items != "" || mi.mode != command.ModeInsert {
		t.Fatalf("入り直した後: %+v", mi)
	}
	if scr := h.term.screen(); strings.Contains(scr, "alps") {
		t.Errorf("入り直した後の画面:\n%s", scr)
	}
}

// マクロの再生では問い合わせず、補完のキーは何も入れずに捨てる（答えの届く時点で結果が変わらないため）。
//
// req: FR-297
func TestConsoleCompleteReplay(t *testing.T) {
	h := startComplete(t)
	h.keys("<Esc>qqA")
	h.keys("be")
	h.waitMenu("beta")
	h.keys("<C-e><Tab><Esc>q")
	h.settle()
	before := h.completeSeq()
	h.keys("@q")
	h.settle()
	if mi := h.cmenu(); mi.input != "bebe" || mi.mode != command.ModeNormal {
		t.Fatalf("再生の後: %+v", mi)
	}
	if seq := h.completeSeq(); seq != before {
		t.Errorf("再生で問い合わせた: %d → %d", before, seq)
	}
}

// 補完を引き受けない組（今までの組）は問い合わせず、<Tab> はタブ文字として入る。
//
// req: FR-274
func TestConsoleNoComplete(t *testing.T) {
	h := start(t, Config{Extensions: fakeSpecs(t)})
	openConsole(t, h)
	h.keys("al<Tab>")
	h.settle()
	if ci, _ := h.console(); ci.input != "al\t" {
		t.Errorf("入力の行 = %q", ci.input)
	}
	if h.seen("complete seq=") {
		t.Error("補完を引き受けない組で問い合わせた")
	}
}

// 切り替えの部品の子の組でも補完する。問い合わせには子の ID を付ける。一覧を出したまま見出しの行を押して区画を替えると、
// 一覧を閉じてインサートモードを終え、戻っても出さない。
//
// req: FR-297, FR-301
func TestConsoleCompleteSwitcher(t *testing.T) {
	h := startSwitcher(t)
	openSwitcher(t, h, "cswitcher")
	h.keys("R")
	h.settle()
	h.keys("ial")
	h.waitMenu("alpha,alps")
	var child string
	h.e.do(func() { child = h.e.float.child })
	if child != "repl" {
		t.Fatalf("子 = %q", child)
	}
	row, col := -1, -1
	for i, l := range strings.Split(h.term.screen(), "\n") {
		if j := strings.Index(l, " List [L] "); j >= 0 {
			row, col = i, len([]rune(l[:j]))+2
		}
	}
	if row < 0 {
		t.Fatalf("見出しの行が無い:\n%s", h.term.screen())
	}
	h.click(row, col)
	if si, _ := h.switcher(); si.sel != "list" || si.mode != command.ModeNormal {
		t.Fatalf("見出しを押した後: %+v", si)
	}
	h.keys("R")
	h.settle()
	h.keys("A")
	if mi := h.cmenu(); mi.items != "" || mi.input != "al" || mi.mode != command.ModeInsert {
		t.Fatalf("戻った後: %+v", mi)
	}
}

// 分割したペイン（40 桁、8 行）の組でも、ペインで打ったキーで問い合わせ、一覧をペインの画面の中に描く。
//
// req: FR-297
func TestConsoleCompletePane(t *testing.T) {
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t)}
	calls.config(&cfg)
	h := start(t, cfg)
	h.e.do(func() { h.e.clients[0].info = ClientInfo{ID: 1, Pane: "%1"} })
	h.keys(":fake.pane complete<CR>")
	h.eventually("OpenPane", func() bool { _, ok := calls.last(true); return ok })
	call, _ := calls.last(true)
	h.waitPaneOpened(call.view)
	term := &exitTerm{fakeTerm: newFakeTerm()}
	term.w, term.h = 40, 8
	h.peers = append(h.peers, term.fakeTerm)
	if err := h.e.AttachComponent(term, ComponentInfo{View: call.view, Pane: true, MuxPane: "%p1"}); err != nil {
		t.Fatal(err)
	}
	comp := &compPeer{h: h, term: term}
	comp.waitScreen("> ")
	comp.keys("x.")
	comp.waitScreen("あかい")
	rows := strings.Split(term.screen(), "\n")
	if len(rows) != 8 {
		t.Fatalf("ペインの画面の行数 = %d", len(rows))
	}
	for i, r := range rows {
		if w := len([]rune(r)); w > 40 {
			t.Errorf("行 %d が 40 桁を超える: %q", i, r)
		}
	}
	if !strings.Contains(rows[5], "> x.") {
		t.Errorf("入力の行:\n%s", term.screen())
	}
	comp.keys("<C-n><C-n><CR>")
	h.settle()
	var mi menuInfo
	h.e.do(func() { mi = completeMenuOf(h.e, h.e.panes[0]) })
	if mi.input != "x.alps" || mi.items != "" {
		t.Errorf("ペインで入れた後: %+v", mi)
	}
}
