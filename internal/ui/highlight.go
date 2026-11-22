package ui

// highlightSpans は、Frame.Highlights の範囲を、表示領域の行ごとの範囲（d.hls）に分ける（FR-140）。
// rows は表示領域の行数で、表示領域に見えている行の範囲だけを分ける（一覧の範囲がバッファの全体でも、見えていない行は数えない）。
//
//   - 範囲は半開区間で、行ごとに範囲に掛かる文字にだけ付ける。行末の 1 セル（行の区切りを表すセル）には付けないので、
//     範囲が行をまたいでも、空の行には色が出ない（nvim 0.12.5 の vim.hl.on_yank と同じ。2026-11-20 に tmux で、yj と
//     複数の行の vjjy の画面を確かめた）
//   - 位置は描画する時点のテキストを指す必要がある。テキストの外の位置は、行の長さと最後の行で切る
//   - Theme に色の無い名前の範囲は付けない
//   - 重ねる順は highlightOrder に一覧での位置を足したもので、後ろの範囲を上に重ねる（layers.go）
//
// req: FR-140
func (d *drawer) highlightSpans(rows int) {
	d.hls = make([][]span, rows)
	top := d.r.top
	for i, hl := range d.f.Highlights {
		a := d.theme.attr(hl.Style)
		if hl.Attr != nil {
			a = a.Over(*hl.Attr)
		}
		if a == (Attr{}) {
			continue
		}
		s, e := hl.Range.Start, hl.Range.End
		if e.Less(s) {
			s, e = e, s
		}
		for n := max(s.Line, top); n <= e.Line && n < top+rows && n < d.text.LineCount(); n++ {
			ln := len(d.text.Line(n))
			b0, b1 := 0, ln
			if n == s.Line {
				b0 = min(max(s.Byte, 0), ln)
			}
			if n == e.Line {
				b1 = min(max(e.Byte, 0), ln)
			}
			if b0 < b1 {
				d.hls[n-top] = append(d.hls[n-top], span{start: b0, end: b1, order: highlightOrder + i, attr: a})
			}
		}
	}
}
