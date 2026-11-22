package command

import (
	"strings"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/target"
)

// lastLine は、最後の行の番号を返す。
func (s *State) lastLine() int { return s.buf.LineCount() - 1 }

// line は、i 番目の行を返す。
func (s *State) line(i int) string { return s.buf.Line(i) }

// pos は、位置を作る。
func pos(line, b int) buffer.Position { return buffer.Position{Line: line, Byte: b} }

// clampPos は、位置をバッファの中の文字の境界に丸める。行末の位置は許す。
func (s *State) clampPos(p buffer.Position) buffer.Position {
	p.Line = min(max(p.Line, 0), s.lastLine())
	line := s.line(p.Line)
	p.Byte = min(max(p.Byte, 0), len(line))
	for p.Byte > 0 && p.Byte < len(line) && !utf8.RuneStart(line[p.Byte]) {
		p.Byte--
	}
	return p
}

// clampNormal は、ノーマルモードのカーソルとして、行末の位置を行の最後の文字へ寄せる。空の行では 0 にする。
func (s *State) clampNormal(p buffer.Position) buffer.Position {
	line := s.line(p.Line)
	if p.Byte >= len(line) {
		p.Byte = lastCharStart(line)
	}
	return p
}

// lastCharStart は、行の最後の文字の位置を返す。空の行では 0 を返す。
func lastCharStart(line string) int {
	if line == "" {
		return 0
	}
	_, size := utf8.DecodeLastRuneInString(line)
	return len(line) - size
}

// nextChar は、行 line の b の文字の次の位置を返す。行末なら b のまま返す。
func nextChar(line string, b int) int {
	if b >= len(line) {
		return b
	}
	_, size := utf8.DecodeRuneInString(line[b:])
	return b + size
}

// prevChar は、行 line の b の前の文字の位置を返す。行の先頭なら 0 を返す。
func prevChar(line string, b int) int {
	if b <= 0 {
		return 0
	}
	_, size := utf8.DecodeLastRuneInString(line[:b])
	return b - size
}

// firstNonBlank は、行の最初の空白でない文字の位置を返す。空白だけの行では最後の文字の位置を返す（Vim の ^）。
func firstNonBlank(line string) int {
	i := len(line) - len(strings.TrimLeft(line, " \t"))
	if i == len(line) {
		return lastCharStart(line)
	}
	return i
}

// firstNonBlankPos は、行 l の最初の空白でない文字の位置を返す。
func (s *State) firstNonBlankPos(l int) buffer.Position {
	return pos(l, firstNonBlank(s.line(l)))
}

// columns は、望みの列の数え方を返す。
func (s *State) columns() target.Columns {
	if s.Columns == nil {
		return target.CharColumns{}
	}
	return s.Columns
}

// updateWant は、望みの列をカーソルの列にする。
//
// インサートモードでタブ文字の上にいるときは、タブ文字の最初の列にする。Columns.Column はタブ文字の最後の列を返す
// （ノーマルモードのカーソルの列）が、Vim と Neovim はインサートモードのカーソルをタブ文字の前に置き、その列を望みの列にする
// （Vim 9.1 の getvcol() は、ノーマルモードのときだけタブ文字の最後の列をカーソルの列にする）。
// Neovim 0.12.5 では、"\tab" の先頭の i<Down> は、下の行の先頭へ移る（#347）。
func (s *State) updateWant() {
	n := s.cursor.Line
	line, b := s.line(n), s.cursor.Byte
	if s.mode == ModeInsert && b < len(line) && line[b] == '\t' {
		s.want = s.columns().Column(n, line[:b], b)
		return
	}
	s.want = s.columns().Column(n, line, b)
}

// setCursorNormal は、カーソルをノーマルモードの位置に丸めて置き、望みの列を更新する。
// ビジュアルモードの中（拡張機能の操作の後など）でカーソルが動いたなら、head をカーソルに合わせる（SetCursor と同じ）。
func (s *State) setCursorNormal(p buffer.Position) {
	old := s.cursor
	s.cursor = s.clampNormal(s.clampPos(p))
	if s.cursor != old {
		s.followCursor()
	}
	s.updateWant()
}

// lineEnd は、行 l の行末の位置を返す。
func (s *State) lineEnd(l int) buffer.Position { return pos(l, len(s.line(l))) }

// runeCount は、文字列の文字数を返す。
func runeCount(str string) int { return utf8.RuneCountInString(str) }

// advanceChars は、行 line の b から n 文字進んだ位置を返す。行末で止まる。進めた文字数も返す。
func advanceChars(line string, b, n int) (end, moved int) {
	for moved < n && b < len(line) {
		b = nextChar(line, b)
		moved++
	}
	return b, moved
}
