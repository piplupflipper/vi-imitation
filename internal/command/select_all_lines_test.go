package command

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/ex"
)

// markJumpNav は、MarkJump に渡された位置（Vim の setpcmark の位置）を順に記録する Navigator である。
type markJumpNav struct{ marks []buffer.Position }

func (n *markJumpNav) MarkJump(p buffer.Position) { n.marks = append(n.marks, p) }

func (n *markJumpNav) Navigate(ex.Action, buffer.Position) (buffer.Position, bool, error) {
	return buffer.Position{}, false, errors.New("unexpected")
}

func (n *markJumpNav) RunActions([]ex.Action) error { return nil }

// selectAllLinesState は、select-all-lines と ggVG を比べるための、キーを打った後の状態である。
type selectAllLinesState struct {
	Text, Cursor, Selection, Register string
	Mode                              Mode
	Want                              int
	MarkStart, MarkEnd                int
	Marked                            bool
	Jumps                             []buffer.Position
}

// runSelectAllLines は、text（"|" がカーソル）のバッファで keys を打った後の状態を返す。
func runSelectAllLines(t *testing.T, text, keys string) selectAllLinesState {
	t.Helper()
	s := setup(t, text)
	nav := &markJumpNav{}
	s.Navigator = nav
	feedKeys(s, keys)
	st := selectAllLinesState{Text: s.buf.Text(), Cursor: s.Cursor().String(), Mode: s.Mode(), Want: s.Want(), Jumps: nav.marks}
	if start, end, linewise, ok := s.Selection(); ok {
		st.Selection = fmt.Sprintf("%s-%s %v", start, end, linewise)
	}
	if r, _ := s.registers.Get(0); !r.empty() {
		st.Register = vimReg(r)
	}
	st.MarkStart, st.Marked = s.mark('<')
	st.MarkEnd, _ = s.mark('>')
	return st
}

// select-all-lines（既定のキーは <Space>a）は、ノーマルモードで ggVG と打ったのと同じ状態にする。
// カーソル、望みの列、選択、ジャンプリストに記録する位置、抜けた後のマーク '< と '>、続くオペレーターの結果が同じである。
// 回数は gg に渡る（3<Space>a は 3ggVG と同じ）。
//
// req: FR-220
func TestSelectAllLinesLikeGgVG(t *testing.T) {
	texts := []string{
		"abc\n  d|e\nfghij",
		"|abc\n  de\nfghij",
		"abc\n  de\n  fgh|ij",
		"  hel|lo",
		"",
		"a\n\n|\n  b\n",
	}
	suffixes := []string{"", "<Esc>", "y", "Y", "d", "<Esc>u", ">", "j", "kd", "o", ":<C-u><Esc>"}
	for _, text := range texts {
		for _, count := range []string{"", "2", "9"} {
			for _, suf := range suffixes {
				t.Run(fmt.Sprintf("%q/%s<Space>a%s", text, count, suf), func(t *testing.T) {
					got := runSelectAllLines(t, text, count+"<Space>a"+suf)
					want := runSelectAllLines(t, text, count+"ggVG"+suf)
					if !reflect.DeepEqual(got, want) {
						t.Errorf("\n got  %+v\n want %+v（ggVG）", got, want)
					}
				})
			}
		}
	}
}

// select-all-lines の結果を、Vim 9.1 の ggVG の結果と比べる（vim -Nu NONE -n --not-a-term。2026-11-22 に確かめた）。
// 位置は 0 から数えた「行:バイト」で、ジャンプは Vim の getjumplist() に残る位置ではなく、setpcmark に渡る位置（同じ行の重なりを除く前）である。
// カーソルの列は Vim 9.1（'startofline' がオン）と同じく行の最初の空白でない文字で、Neovim 0.12.5 の既定（'nostartofline'）は望みの列を保つ。
//
// req: FR-220
func TestSelectAllLinesVim(t *testing.T) {
	tests := []struct {
		text, keys        string
		cursor, selection string
		mode              Mode
		jumps             []buffer.Position
		register          string
		markStart, mEnd   int
	}{
		// Vim では、カーソルは [3, 1]（1 から数える）、getjumplist() は [[2, 3], [1, 0]]。
		{"abc\n  d|e\nfghij", "<Space>a", "2:0", "0:0-2:0 true", ModeVisualLine, []buffer.Position{pos(1, 3), pos(0, 0)}, "", 0, 0},
		// <Esc> の後の '< は 1 行目、'> は 3 行目（Vim の getpos は [1, 1] と [3, MAXCOL]）。
		{"abc\n  d|e\nfghij", "<Space>a<Esc>", "2:0", "", ModeNormal, []buffer.Position{pos(1, 3), pos(0, 0)}, "", 0, 2},
		// y は行単位で全体をヤンクする。カーソルは y を押したときの位置に残る（FR-129。Vim は 1 行目の先頭へ移る）。
		{"abc\n  d|e\nfghij", "<Space>ay", "2:0", "", ModeNormal, []buffer.Position{pos(1, 3), pos(0, 0)}, "V:abc\n  de\nfghij\n", 0, 2},
		// 1 行のバッファ。Vim では、カーソルは [1, 3]、getjumplist() は [[1, 2]]（同じ行の 2 つの記録が 1 つになる）。
		{"  hel|lo", "<Space>a", "0:2", "0:2-0:2 true", ModeVisualLine, []buffer.Position{pos(0, 5), pos(0, 2)}, "", 0, 0},
		{"  hel|lo", "<Space>ad", "0:0", "", ModeNormal, []buffer.Position{pos(0, 5), pos(0, 2)}, "V:  hello\n", 0, 0},
		// 空のバッファ。Vim では、カーソルは [1, 1]、y のレジスターは 1 行の空の行。
		{"", "<Space>a", "0:0", "0:0-0:0 true", ModeVisualLine, []buffer.Position{pos(0, 0), pos(0, 0)}, "", 0, 0},
		{"", "<Space>ay", "0:0", "", ModeNormal, []buffer.Position{pos(0, 0), pos(0, 0)}, "V:\n", 0, 0},
		// 回数は gg に渡る。Vim の 2ggVG は、カーソルが [3, 1]、選択の始まり（getpos('v')）が [2, 3]、getjumplist() は [[2, 2]]。
		{"abc\n  d|e\nfghij", "2<Space>a", "2:0", "1:2-2:0 true", ModeVisualLine, []buffer.Position{pos(1, 3), pos(1, 2)}, "", 0, 0},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q/%s", tt.text, tt.keys), func(t *testing.T) {
			got := runSelectAllLines(t, tt.text, tt.keys)
			if got.Cursor != tt.cursor || got.Selection != tt.selection || got.Mode != tt.mode || got.Register != tt.register {
				t.Errorf("cursor %s selection %q mode %v register %q, want %s %q %v %q", got.Cursor, got.Selection, got.Mode, got.Register, tt.cursor, tt.selection, tt.mode, tt.register)
			}
			if !reflect.DeepEqual(got.Jumps, tt.jumps) {
				t.Errorf("jumps = %v, want %v", got.Jumps, tt.jumps)
			}
			if got.Mode == ModeNormal && (!got.Marked || got.MarkStart != tt.markStart || got.MarkEnd != tt.mEnd) {
				t.Errorf("marks = %d-%d %v, want %d-%d", got.MarkStart, got.MarkEnd, got.Marked, tt.markStart, tt.mEnd)
			}
		})
	}
}
