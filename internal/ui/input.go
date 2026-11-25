package ui

import "fmt"

// drawInput は、拡張機能が 1 行の入力を聞く問い（Frame.Input。input/ask。FR-275）を描き、入力の末尾に置くカーソルを返す。
//
// 問いの文（前置き）と入力の中身を続けて、最後の行に描く（コマンドラインの ":" の代わりに問いの文を置いた形）。
// Frame.CommandLinePopup なら、コマンドラインと同じく画面の上の中央のポップアップの枠に描き、枠の題を記号
// commandLine.title.input（SymbolCommandLineTitleInput）にする（drawLinePopup）。枠を置けなければ最後の行に描く。
// 入力が幅に収まらなければ、コマンドラインと同じく末尾が見えるように先頭の側を隠す（cmdlineTail）。
// 続けて届いた問いの何件目か（Input.Index）は、問いの文の前に "問い 2: " の形で出す（選択肢の問いの案内と同じ。#151）。
//
// 知らせ（Input.Note）は、入力を描いた行（ポップアップなら最後の行の 1 つ下の行を入力の行とみなす）の上の行から上へ、
// 折り返して描く。収まらなければ、入力の行に近い側（後の行）を残す。
//
// req: FR-275
func (r *Renderer) drawInput(g *Grid, theme Theme, f *Frame) Cursor {
	in := f.Input
	full := in.Prompt + in.Text
	if in.Index > 0 {
		full = fmt.Sprintf("問い %d: %s", in.Index, full)
	}
	row := r.Height - 1
	r.pum.valid = false
	var cur Cursor
	popup := false
	if f.CommandLinePopup {
		cur, popup = r.drawLinePopup(g, theme, full, SymbolCommandLineTitleInput, false, nil)
	}
	if !popup {
		s := r.cmdlineTail(full, r.Width)
		end := r.drawText(g, row, 0, r.Width, s, Attr{})
		cur = Cursor{Row: row, Col: min(end, max(r.Width-1, 0)), Shape: CursorBar, Visible: r.Width > 0}
	}
	bottom := row - 1
	if popup {
		bottom = row
	}
	var lines []string
	for _, l := range textLines(in.Note) {
		lines = append(lines, r.wrapText(l, r.Width)...)
	}
	if n := max(bottom, 0); len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	a := theme.attr(StyleMessageInfo)
	top := bottom - len(lines) + 1
	for i, l := range lines {
		for col := range r.Width {
			g.set(top+i, col, blank(Attr{}))
		}
		r.drawText(g, top+i, 0, r.Width, l, a)
	}
	return cur
}
