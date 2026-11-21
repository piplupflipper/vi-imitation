package editor

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 設定 commandLine.popup が真なら、コマンドラインと検索の入力を、画面の上の中央のポップアップの枠の中に描き、最後の行を空ける。
// 枠の題は入力の種類で替わり、補完の候補の一覧は入力の枠のすぐ下に描く。入力を終えた後のメッセージは、最後の行に出す。
// :option.set で偽にすると、次の描画から最後の行に描く。
//
// req: FR-215, FR-216, FR-217
func TestCmdlinePopup(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a1": "", "a2": "", "t.txt": "x\nalpha\nbeta\n"})
	term := newFakeTerm()
	term.w, term.h = 80, 24
	h := startTerm(t, term, Config{
		WorkDir: dir,
		Options: map[string]option.Value{option.CommandLinePopup: option.BoolValue(true)},
		Symbols: ui.Symbols{ui.SymbolPopupBorder: "╭─╮│╯─╰│"},
	}, filepath.Join(dir, "t.txt"))
	h.settle()

	h.keys(":e a<Tab>")
	h.waitCmdline(":e a<Tab>", "e a1", "[a1] a2")
	h.eventually("入力の枠を描く", func() bool { return strings.Contains(h.screenRow(3), "│ :e a1 ") })
	for row, want := range map[int]string{2: "─ Cmdline ─", 5: "╭──", 6: "│ a1 ", 7: "│ a2 ", 8: "╰──"} {
		if got := h.screenRow(row); !strings.Contains(got, want) {
			t.Errorf("%d 行目 = %q, want %q を含む", row, got, want)
		}
	}
	if got := h.screenRow(23); got != "" {
		t.Errorf("入力の間の最後の行 = %q", got)
	}
	h.keys("<Esc>")

	h.keys("/alp")
	h.eventually("検索の入力の枠を描く", func() bool {
		return strings.Contains(h.screenRow(3), "│ /alp ") && strings.Contains(h.screenRow(2), " Search forward ")
	})
	h.keys("<CR>")
	h.eventually("検索の後に枠を消す", func() bool { return !strings.Contains(h.screenRow(2), "Search") })

	h.keys(":nosuchcommand<CR>")
	h.eventually("誤りを最後の行に出す", func() bool { return strings.HasPrefix(h.screenRow(23), "E492") })

	h.keys(":option.set commandLine.popup false<CR>:q")
	h.eventually("偽にした後は最後の行に描く", func() bool { return h.screenRow(23) == ":q" && !strings.Contains(h.screenRow(3), "│") })
	h.keys("<Esc>")
}

// ペインの部品の client とポップアップの部品の client の画面でも、コマンドラインと検索の入力を画面の上の枠に描く。
// 枠線の記号が空（本体の既定）なら、題を枠の最初の行に描く。
//
// req: FR-215
func TestCmdlinePopupComponents(t *testing.T) {
	var calls muxCalls
	cfg := Config{Extensions: fakeSpecs(t), Options: map[string]option.Value{option.CommandLinePopup: option.BoolValue(true)}}
	calls.config(&cfg)
	h := start(t, cfg)
	peer := h.attach(ClientInfo{ID: 2, Pane: "%9"})

	peer.keys(":fake.pane tree<CR>")
	h.eventually("OpenPane", func() bool { _, ok := calls.last(true); return ok })
	call, _ := calls.last(true)
	h.waitPaneOpened(call.view)
	comp := h.attachComponent(call.view, true)
	comp.waitScreen("▸ dir")
	comp.keys("/di")
	comp.waitScreen(" Search forward ")
	if rows := strings.Split(comp.term.screen(), "\n"); !strings.Contains(rows[3], " /di ") || strings.TrimSpace(rows[5]) != "" {
		t.Errorf("ペインの部品の client の画面:\n%s", comp.term.screen())
	}
	comp.keys("<Esc>")

	peer.keys(":fake.popup list<CR>")
	h.eventually("OpenPopup", func() bool { _, ok := calls.last(false); return ok })
	call, _ = calls.last(false)
	pop := h.attachComponent(call.view, false)
	pop.waitScreen("close me")
	pop.keys(":")
	pop.waitScreen(" Cmdline ")
	if rows := strings.Split(pop.term.screen(), "\n"); !strings.Contains(rows[3], " : ") {
		t.Errorf("ポップアップの部品の client の画面:\n%s", pop.term.screen())
	}
	pop.keys("<Esc>")
}
