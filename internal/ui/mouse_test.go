package ui

import (
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// HitTest は、直前の Render の画面の位置を、タブの行の見出し、バッファの行と表示の列（行番号の列は本文の左の端、行末より右はそのままの列、
// 最後の行より下は最後の行）に解き、ステータス行とコマンドラインの行には何も無いとする。
// 寄せて解くときは、表示領域の上と下へはみ出した行の数を返す。
//
// req: FR-181
func TestHitTestText(t *testing.T) {
	b := buffer.New("hello\n\tx\nabcdefghijklmnopqrstuvwxyz0123\n")
	r := newRenderer(20, 8)
	tabs := []Tab{{Name: "a.go", Current: true}, {Name: "b.go"}}
	r.Render(&Frame{Text: b, Cursor: pos(0, 0), Tabs: tabs, LineNumbers: LineNumbers{Show: true, MinWidth: 4}})
	tests := []struct {
		row, col int
		clamp    HitArea
		want     Hit
	}{
		{0, 0, HitNone, Hit{Area: HitTabLine, Tab: 0}},
		{0, 5, HitNone, Hit{Area: HitTabLine, Tab: 0}},
		{0, 6, HitNone, Hit{Area: HitTabLine, Tab: 1}},
		{0, 15, HitNone, Hit{Area: HitTabLine, Tab: -1}},
		{1, 4, HitNone, Hit{Area: HitText, Line: 0, Col: 0, Tab: -1}},
		{1, 1, HitNone, Hit{Area: HitText, Line: 0, Col: 0, Tab: -1}},
		{1, 12, HitNone, Hit{Area: HitText, Line: 0, Col: 8, Tab: -1}},
		{2, 7, HitNone, Hit{Area: HitText, Line: 1, Col: 3, Tab: -1}},
		{4, 9, HitNone, Hit{Area: HitText, Line: 2, Col: 5, Below: true, Tab: -1}},
		{6, 3, HitNone, Hit{Area: HitStatusLine, Tab: -1}},
		{7, 3, HitNone, Hit{Area: HitCommandLine, Tab: -1}},
		{8, 3, HitNone, Hit{Area: HitNone, Tab: -1}},
		{3, 20, HitNone, Hit{Area: HitNone, Tab: -1}},
		// ドラッグの間は、表示領域の中へ寄せる。タブの行は上へ 1 行、コマンドラインの行は下へ 2 行のはみ出しである。
		{0, 6, HitText, Hit{Area: HitText, Line: 0, Col: 2, Over: -1, Tab: -1}},
		{7, 30, HitText, Hit{Area: HitText, Line: 2, Col: 15, Below: true, Over: 2, Tab: -1}},
		// 画面の外の位置は、画面の端の行と列に寄せてから数える（Neovim 0.12.5 と同じ）。
		{20, 9, HitText, Hit{Area: HitText, Line: 2, Col: 5, Below: true, Over: 2, Tab: -1}},
		{-5, 3, HitText, Hit{Area: HitText, Line: 0, Col: 0, Over: -1, Tab: -1}},
	}
	for _, tt := range tests {
		if got := r.HitTest(tt.row, tt.col, tt.clamp); got != tt.want {
			t.Errorf("HitTest(%d, %d, %v) = %+v, want %+v", tt.row, tt.col, tt.clamp, got, tt.want)
		}
	}
	// 横にスクロールしたら、表示の列にスクロールの位置を足す。
	r.Render(&Frame{Text: b, Cursor: pos(2, 29), Tabs: tabs, LineNumbers: LineNumbers{Show: true, MinWidth: 4}})
	left := r.Left()
	if left == 0 {
		t.Fatal("横にスクロールしていない")
	}
	if got := r.HitTest(3, 5, HitNone); got.Line != 2 || got.Col != left+1 {
		t.Errorf("横にスクロールした後 = %+v, want 行 2 の列 %d", got, left+1)
	}
}

// 行を折り返すときは、見た目の行の番号 × 本文の幅 + 見た目の行の中の列を表示の列にする（WrapLines の列）。
//
// req: FR-181
func TestHitTestWrap(t *testing.T) {
	b := buffer.New("abcdefghijklmno\nxy\n")
	r := newRenderer(10, 6)
	r.TabLine = TabLineNever
	r.Render(&Frame{Text: b, Cursor: pos(0, 0), Wrap: true})
	for _, tt := range []struct {
		row, col, line, vcol int
	}{
		{0, 3, 0, 3},
		{1, 3, 0, 13},
		{1, 8, 0, 18},
		{2, 1, 1, 1},
	} {
		got := r.HitTest(tt.row, tt.col, HitNone)
		if got.Area != HitText || got.Line != tt.line || got.Col != tt.vcol {
			t.Errorf("HitTest(%d, %d) = %+v, want 行 %d の列 %d", tt.row, tt.col, got, tt.line, tt.vcol)
		}
		if got.Line == 0 {
			wl := r.WrapLines(LineLayout{})
			if c := wl.ByteAt(0, b.Line(0), got.Col); c != min(tt.vcol, 14) {
				t.Errorf("列 %d のバイト = %d", got.Col, c)
			}
		}
	}
}

// ポップアップの窓の中のテキストは、窓の中のバッファの行と列に解く。窓の余白と見出しには何も無い。
// 部品の client の画面（Fill）でも同じである（FR-185）。
//
// req: FR-185
func TestHitTestFloat(t *testing.T) {
	b := buffer.New("one\ntwo\n")
	inner := buffer.New("item 1\nitem 2\nitem 3\n")
	r := newRenderer(20, 6)
	r.TabLine = TabLineNever
	fr := &Frame{Text: b, Cursor: pos(0, 0), Float: &Float{View: &Frame{Text: inner, Cursor: pos(1, 0)}, Fill: true, Title: "T"}}
	r.Render(fr)
	tests := []struct {
		row, col int
		clamp    HitArea
		want     Hit
	}{
		{0, 3, HitNone, Hit{Area: HitNone, Tab: -1}},
		{1, 1, HitNone, Hit{Area: HitFloat, Line: 0, Col: 0, Tab: -1}},
		{2, 5, HitNone, Hit{Area: HitFloat, Line: 1, Col: 4, Tab: -1}},
		{1, 0, HitNone, Hit{Area: HitNone, Tab: -1}},
		{3, 2, HitNone, Hit{Area: HitFloat, Line: 2, Col: 1, Tab: -1}},
		{4, 2, HitNone, Hit{Area: HitStatusLine, Tab: -1}},
		{0, 3, HitFloat, Hit{Area: HitFloat, Line: 0, Col: 2, Over: -1, Tab: -1}},
		{5, 0, HitFloat, Hit{Area: HitFloat, Line: 2, Col: 0, Over: 2, Tab: -1}},
		// 窓の下のバッファの表示領域へ寄せるときは、窓に関わらずその位置の行と列にする。
		{1, 1, HitText, Hit{Area: HitText, Line: 1, Col: 1, Tab: -1}},
	}
	for _, tt := range tests {
		if got := r.HitTest(tt.row, tt.col, tt.clamp); got != tt.want {
			t.Errorf("HitTest(%d, %d, %v) = %+v, want %+v", tt.row, tt.col, tt.clamp, got, tt.want)
		}
	}
	// 窓を描かなくなったら、窓の中として解かない。
	r.Render(&Frame{Text: b, Cursor: pos(0, 0)})
	if got := r.HitTest(1, 1, HitNone); got.Area != HitText {
		t.Errorf("窓を閉じた後 = %+v", got)
	}
	if got := r.HitTest(1, 1, HitFloat); got.Area != HitNone {
		t.Errorf("窓を閉じた後に窓へ寄せた = %+v", got)
	}
	if got := (&Renderer{Width: 10, Height: 5}).HitTest(0, 0, HitNone); got.Area != HitNone {
		t.Errorf("描く前 = %+v", got)
	}
}
