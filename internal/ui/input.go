package ui

import "fmt"

// drawInput は、拡張機能が 1 行の入力を聞く問い（Frame.Input。input/ask。FR-275）を描き、入力のカーソルの位置を返す。
//
// 問いの文（前置き）と入力の中身を続けて、最後の行に描く（コマンドラインの ":" の代わりに問いの文を置いた形）。
// Frame.CommandLinePopup なら、コマンドラインと同じく画面の上の中央のポップアップの枠に描き、枠の題を記号
// commandLine.title.input（SymbolCommandLineTitleInput）にする（drawLinePopup）。枠を置けなければ最後の行に描く。
// 続けて届いた問いの何件目か（Input.Index）は、問いの文の前に "問い 2: " の形で出す（選択肢の問いの案内と同じ。#151）。
// 入力が幅に収まらなければ、問いの文を残して中身の側を横に送る（inputLine。PR #411 のレビューの推奨 2）。
//
// 知らせ（Input.Note）は、入力を描いた行（ポップアップなら最後の行の 1 つ下の行を入力の行とみなす）の上の行から上へ、
// 折り返して描く。収まらなければ、入力の行に近い側（後の行）を残す。
//
// req: FR-275
func (r *Renderer) drawInput(g *Grid, theme Theme, f *Frame) Cursor {
	in := f.Input
	prompt := in.Prompt
	if in.Index > 0 {
		prompt = fmt.Sprintf("問い %d: %s", in.Index, prompt)
	}
	tabStop := tabStopOr(r.tabStop)
	fit := func(width int) (string, int) { return r.inputLine(prompt, in.Text, in.Cursor, width) }
	row := r.Height - 1
	r.pum.valid = false
	var cur Cursor
	popup := false
	if f.CommandLinePopup {
		cur, popup = r.drawLinePopup(g, theme, StringWidth(prompt+in.Text, tabStop), fit, SymbolCommandLineTitleInput, false, nil)
	}
	if !popup {
		s, at := fit(r.Width)
		r.drawText(g, row, 0, r.Width, s, Attr{})
		cur = Cursor{Row: row, Col: min(at, max(r.Width-1, 0)), Shape: CursorBar, Visible: r.Width > 0}
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

// inputLine は、問いの文 prompt と入力の中身 text（カーソルはバイトの位置 cursor）を width 列の欄に描く文字列と、
// その先頭から数えたカーソルの列を返す。
//
// 問いの文は欄の半分の列までを先頭から残し（超えた末尾は切る）、残りの列に中身を描く。何の問いに答えているかを、
// 長い中身を打っても見えるようにするためである（: のコマンドラインは 1 文字の前置きごと先頭を隠す）。
// 中身は、カーソルの前の側を、カーソルの 1 列を残して収まる末尾だけ残し（cmdlineTail）、その後にカーソルの後の側を続ける。
// 欄に収まらない後ろの側は、描くときに切れる。
func (r *Renderer) inputLine(prompt, text string, cursor, width int) (string, int) {
	tabStop := tabStopOr(r.tabStop)
	if cursor < 0 || cursor > len(text) {
		cursor = len(text)
	}
	if limit := width / 2; StringWidth(prompt, tabStop) > limit {
		prompt = r.headWidth(prompt, limit)
	}
	pw := StringWidth(prompt, tabStop)
	before := r.cmdlineTail(text[:cursor], width-pw)
	return prompt + before + text[cursor:], StringWidth(prompt+before, tabStop)
}

// headWidth は、s の先頭から、表示幅が width 列を超えない所までを返す。
func (r *Renderer) headWidth(s string, width int) string {
	tabStop := tabStopOr(r.tabStop)
	col, off := 0, 0
	for off < len(s) {
		gl := nextGlyph(s, off, col, tabStop)
		if col+gl.width > width {
			break
		}
		col += gl.width
		off += gl.size
	}
	return s[:off]
}
