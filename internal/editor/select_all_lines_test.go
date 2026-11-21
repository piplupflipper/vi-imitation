package editor

import (
	"fmt"
	"strings"
	"testing"
)

// selectAllLinesSnapshot は、キーを打った後の、ジャンプリスト、カーソル、モード、選択、テキスト、無名レジスターである。
func (h *harness) selectAllLinesSnapshot() string {
	h.settle()
	var out string
	h.e.do(func() {
		st := h.e.cur.state
		list, idx, cur := h.e.jumpList()
		start, end, linewise, ok := st.Selection()
		r, _ := st.Registers().Get(0)
		out = fmt.Sprintf("jumps %q idx %d cur %s mode %v selection %v-%v %v %v text %q register %q %v",
			list, idx, cur, st.Mode(), start, end, linewise, ok, h.e.cur.buf.Text(), r.Text, r.Linewise)
	})
	return out
}

// 本体の既定のキーの <Leader>a（select-all-lines）は、ノーマルモードで ggVG と打ったのと同じ状態にする。
// ジャンプリスト（<C-o> で戻る先を含む）、カーソル、選択、y の結果が同じで、leader を変えれば新しい leader で始まる。
//
// req: FR-220
func TestSelectAllLinesDefaultKey(t *testing.T) {
	const text = "abc\n  de\nfghij\n"
	suffixes := []string{"", "<Esc>", "<Esc><C-o>", "<Esc><C-o><C-o>", "y", "y<C-o>", "d", "jy"}
	for _, leader := range []string{"", ","} {
		lk := "<Space>"
		if leader != "" {
			lk = leader
		}
		for _, pre := range []string{"jlll", "G$", "", "2"} {
			for _, suf := range suffixes {
				t.Run(fmt.Sprintf("%s/%s%sa%s", lk, pre, lk, suf), func(t *testing.T) {
					run := func(keys string) string {
						dir := writeFiles(t, map[string]string{"a.txt": text})
						h := start(t, Config{WorkDir: dir, Leader: leader}, "a.txt")
						h.keys(keys)
						return h.selectAllLinesSnapshot()
					}
					got := run(pre + lk + "a" + suf)
					want := run(pre + "ggVG" + suf)
					if got != want {
						t.Errorf("\n got  %s\n want %s（ggVG）", got, want)
					}
					// 比べの前提として、ジャンプリストとカーソルが Vim 9.1 の ggVG と同じであることを 1 件で確かめる
					// （Vim の getjumplist() は [[2, 3], [1, 0]]、カーソルは [3, 1]。2026-11-22）。
					if pre == "jlll" && suf == "<Esc>" {
						if w := `jumps "a.txt:2.3 a.txt:1.0" idx 2 cur a.txt:3.0 mode normal`; !strings.HasPrefix(got, w) {
							t.Errorf("got %s, want the prefix %s", got, w)
						}
					}
				})
			}
		}
	}
}

// <Leader>a は、<Leader>? とキーの一覧（:keys）に、本体の既定のキーとして出る。
//
// req: FR-90, FR-220
func TestSelectAllLinesKeyList(t *testing.T) {
	h := start(t, Config{})
	h.keys("<Space>?")
	h.settle()
	_, text := h.listText()
	if !strings.Contains(text, "\n<Space>a ") || !strings.Contains(text, "最後の行までを、行単位のビジュアルモードで選ぶ（ggVG）") {
		t.Errorf("<Space>? lacks <Space>a:\n%s", text)
	}
	h.keys("<Esc>:keys<CR>")
	h.settle()
	if _, text = h.listText(); !strings.Contains(text, "\n<Space>a ") {
		t.Errorf(":keys lacks <Space>a:\n%s", text)
	}
}
