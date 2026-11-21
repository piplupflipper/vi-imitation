package editor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
)

// 設定の keymap を書かなくても、ノーマルモードとビジュアルモードの <Leader>a（既定の leader では <Space>a）は select-all である（#364）。
// カーソルを動かさずに全体を文字単位で選び、ジャンプリストにも記録しない。行単位のビジュアルモードの中では文字単位に切り替える。
// leader を変えれば、新しい leader で始まる。<Leader>? とキーの一覧（:keys）にも本体の既定のキーとして出る。
//
// req: FR-90, FR-130
func TestSelectAllDefaultKey(t *testing.T) {
	const text = "abc\n  de\nfghij\n"
	for _, tt := range []struct{ leader, pre, keys string }{
		{"", "jlll", "<Space>a"},
		{"", "jlllV", "<Space>a"},
		{"", "jlllvk", "<Space>a"},
		{",", "jlll", ",a"},
	} {
		t.Run(tt.leader+tt.pre+tt.keys, func(t *testing.T) {
			dir := writeFiles(t, map[string]string{"a.txt": text})
			h := start(t, Config{WorkDir: dir, Leader: tt.leader}, "a.txt")
			h.keys(tt.pre)
			h.settle()
			var cur buffer.Position
			var jumps string
			h.e.do(func() { cur, jumps = h.e.cur.state.Cursor(), fst(h.e.jumpList()) })
			h.keys(tt.keys)
			h.settle()
			h.e.do(func() {
				st := h.e.cur.state
				start, end, linewise, ok := st.Selection()
				if !ok || linewise || st.Mode() != command.ModeVisual || start != (buffer.Position{}) || end != (buffer.Position{Line: 2, Byte: 5}) {
					t.Errorf("selection = %v-%v linewise %v ok %v mode %v", start, end, linewise, ok, st.Mode())
				}
				if st.Cursor() != cur || fst(h.e.jumpList()) != jumps {
					t.Errorf("cursor %v jumps %q, want %v %q", st.Cursor(), fst(h.e.jumpList()), cur, jumps)
				}
			})
			h.keys("y")
			h.settle()
			h.e.do(func() {
				if r, _ := h.e.cur.state.Registers().Get(0); r.Text != "abc\n  de\nfghij" || r.Linewise {
					t.Errorf("register = %q linewise %v", r.Text, r.Linewise)
				}
			})
		})
	}

	h := start(t, Config{})
	h.keys("<Space>?")
	h.settle()
	if _, text := h.listText(); !strings.Contains(text, "\n<Space>a ") || !strings.Contains(text, "カーソルを動かさずにバッファの全体を選ぶ") {
		t.Errorf("<Space>? lacks <Space>a:\n%s", text)
	}
	h.keys("<Esc>:keys<CR>")
	h.settle()
	if _, text := h.listText(); !strings.Contains(text, "\n<Space>a ") {
		t.Errorf(":keys lacks <Space>a:\n%s", text)
	}
}

// 外からの編集（拡張機能の buffer/edit。MCP の edit_buffer、ACP の書き込み）は、カーソルと同じ規則で
// ビジュアルモードの選択の両端もずらす（applyExternal の ShiftPositions）。行を足す編集の後の y は、ずれた選択の全体をヤンクする
// （PR #265 のレビュー）。
//
// 各ケースが見張るもの:
//   - 「Gvgg の後に末尾の行を消す」「途中に行を足す」「先頭に行を足す」は、両端のずらし（ShiftPositions）を見張る。
//     applyExternal を SetCursor の呼び出しに戻すと、この 3 件が失敗する
//   - 「末尾の行を消す」は、レビューで本体が落ちた入力の通しの確認である。ずらしを外しても Selection の丸めで成功するので、
//     ずらしの見張りにはならない。異常終了は、ずらしと丸めの両方が無いときに起きる。丸めは TestSelectAllShrunkWithoutShift が見張る
//     （丸めを外すと、そちらが index out of range で panic する）
//
// req: FR-130
func TestSelectAllExternalEdit(t *testing.T) {
	const text = "abc\nde\nfghij\n"
	tests := []struct {
		name, keys string
		edit       buffer.Edit
		wantText   string
		wantReg    string
	}{
		// 2 行目の行末から最後までを消す（末尾の行を消す）。
		{"末尾の行を消す", "<Space>a", buffer.Edit{Range: buffer.Range{Start: buffer.Position{Line: 1, Byte: 2}, End: buffer.Position{Line: 2, Byte: 5}}}, "abc\nde", "abc\nde"},
		// main からある同じ種類の欠陥（anchor が消えた行に残る）も、同じ規則で直る。
		{"Gvgg の後に末尾の行を消す", "Gvgg", buffer.Edit{Range: buffer.Range{Start: buffer.Position{Line: 1, Byte: 2}, End: buffer.Position{Line: 2, Byte: 5}}}, "abc\nde", "abc\nde"},
		// 3 行目の前に行を足す。最後の行も選択に残る。
		{"途中に行を足す", "<Space>a", buffer.Edit{Range: buffer.Range{Start: buffer.Position{Line: 2}, End: buffer.Position{Line: 2}}, Text: "Y\n"}, "abc\nde\nY\nfghij", "abc\nde\nY\nfghij"},
		// 先頭に行を足す。選択の始まりは、診断と同じく挿入の後ろへずれる（足した行は選択の外）。
		// カーソルもずれるが、head をカーソルへ合わせないので、選択は元の全体のままである。
		{"先頭に行を足す", "<Space>a", buffer.Edit{Text: "X\n"}, "X\nabc\nde\nfghij", "abc\nde\nfghij"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeFiles(t, map[string]string{"a.txt": text})
			h := start(t, Config{WorkDir: dir, KeymapActions: map[string]map[string]string{"normal": {"<Leader>a": "select-all"}}}, "a.txt")
			h.keys("jl")
			h.keys(tt.keys)
			h.settle()
			h.e.do(func() {
				bs := h.e.cur
				tx := buffer.Transaction{BaseVersion: bs.buf.Version(), Edits: []buffer.Edit{tt.edit}}
				if _, err := h.e.applyExternal(bs, tx); err != nil {
					t.Error(err)
				}
				// editor/state（MCP の list_buffers が呼ぶ）が、選択の端でバッファの外の行を読まない。
				res := h.e.editorState()
				if len(res.Selections) != 1 {
					t.Errorf("selections = %+v", res.Selections)
				}
			})
			h.keys("y")
			h.settle()
			if got := h.text(); got != tt.wantText {
				t.Errorf("text = %q, want %q", got, tt.wantText)
			}
			var reg string
			h.e.do(func() {
				r, _ := h.e.cur.state.Registers().Get(0)
				reg = r.Text
			})
			if reg != tt.wantReg {
				t.Errorf("register = %q, want %q", reg, tt.wantReg)
			}
		})
	}
}

// Selection は、外からの編集の後に ShiftPositions を通らずにバッファが縮んでも（読み直しなど）、両端をバッファの中に丸めて返す。
// Selection の丸め（internal/command の Selection の clampPos）を見張る。丸めを外すと、editor/state か y が index out of range で panic する。
func TestSelectAllShrunkWithoutShift(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\nde\nfghij\n"})
	h := start(t, Config{WorkDir: dir, KeymapActions: map[string]map[string]string{"normal": {"<Leader>a": "select-all"}}}, "a.txt")
	h.keys("<Space>a")
	h.settle()
	h.e.do(func() {
		bs := h.e.cur
		// applyExternal を通さずに、バッファだけを縮める。
		if _, err := bs.buf.Apply(buffer.Transaction{BaseVersion: bs.buf.Version(), Edits: []buffer.Edit{
			{Range: buffer.Range{Start: buffer.Position{Line: 0, Byte: 3}, End: buffer.Position{Line: 2, Byte: 5}}},
		}}); err != nil {
			t.Fatal(err)
		}
		start, end, _, ok := bs.state.Selection()
		if got := fmt.Sprintf("%v-%v %v", start, end, ok); got != "0:0-0:3 true" {
			t.Errorf("selection = %s", got)
		}
		_ = h.e.editorState()
	})
	h.keys("y")
	h.settle()
	h.e.do(func() {
		if r, _ := h.e.cur.state.Registers().Get(0); r.Text != "abc" {
			t.Errorf("register = %q", r.Text)
		}
	})
}
