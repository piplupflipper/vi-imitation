package ext

import (
	"strings"
	"unicode/utf8"
)

// テキストの部品の末尾への書き足し（view/append。基本設計 1.4 の「#399 の切り替えの部品と書き足し」の「末尾への書き足し」。
// FR-272、FR-273）の、行の数え方と上限の処理である。
//
// 本体（internal/editor）と、拡張機能の側の写し（ext/server の TextLog）の両方がこの関数を呼ぶ。
// 同じ関数で行を分けて古い行を捨てるので、閉じて開き直したときの写しの中身は、本体が出していた中身と食い違わない。
//
// 中身は、行の列と、末尾が改行で終わっているか（eol）の組で表す。行は Vim と同じく数え、"a\nb\n" は 2 行で eol が true、
// "a\nb" は 2 行で eol が false である。空の中身は空の行 1 つで eol が false である（SplitViewText）。
// eol が false なら、次の書き足しは最後の行に続け、true なら新しい行から始める（段階 2 の出力の欄の output/append と同じ）。

const (
	// ViewTextMaxLines は、書き足すテキストの部品 1 つが持つ行数の上限（仮に 10000）である（FR-272）。
	// 超えたら古い行から捨てる。段階 2 の出力の欄（基本設計 6.7）と同じ値である。
	ViewTextMaxLines = 10000
	// ViewTextMaxLineBytes は、書き足した 1 行のバイト数の上限（仮に 4096）である（FR-272）。
	// 超えたら文字の境目で次の行へ送る。改行の無い出力で 1 行が際限なく伸び、描画のたびの折り返しが重くなるのを避ける（NFR-03）。
	ViewTextMaxLineBytes = 4096
)

// SplitViewText は、テキスト text を、行の列と末尾が改行で終わっているか（eol）に分ける。
// 末尾の改行は 1 つだけを行の区切りでなく eol として扱う（"a\n\n" は "a" と "" の 2 行で eol が true）。
// 1 行の上限（ViewTextMaxLineBytes）では分けない（開いたときの中身は、拡張機能が渡したとおりに出す）。
func SplitViewText(text string) (lines []string, eol bool) {
	eol = strings.HasSuffix(text, "\n")
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n"), eol
}

// JoinViewText は、SplitViewText の逆で、行の列 lines と eol からテキストを作る。
func JoinViewText(lines []string, eol bool) string {
	s := strings.Join(lines, "\n")
	if eol {
		s += "\n"
	}
	return s
}

// AppendViewText は、最後の行が last（その中の色の範囲が lastSpans）で末尾の改行の有無が eol の中身へ、
// text（その中の色の範囲が spans）を書き足した結果を返す。
//
// eol が false なら、返す lines は最後の行 last を置き換える行の列で、lines[0] は last で始まる。
// eol が true なら、返す lines は最後の行の後ろへ足す行の列で、last と lastSpans は読まない。
// lineSpans は lines と同じ長さで、各行の中の色の範囲（その行の中のバイトの位置）である。
// 範囲は改行と 1 行の上限で分けた行ごとに切り分け、空になった範囲は捨てる。
// eolAfter は、書き足した後の中身の末尾が改行で終わっているかである。
//
// 書き足した後の行（last を続けた行を含む）は、ViewTextMaxLineBytes を超えたら UTF-8 の文字の境目で次の行へ送る。
// 書き足す前の中身の行は分けないので、改行で終わった中身へ書き足しても、前の行は変わらない。
// text が空なら、何も変えない（eol が false なら lines は last 1 行、true なら空）。
// 書き足した後の行数が上限を超えるかは、呼び出し側が ViewTextOverflow で数える。
func AppendViewText(last string, lastSpans []ViewSpan, eol bool, text string, spans []ViewSpan) (lines []string, lineSpans [][]ViewSpan, eolAfter bool) {
	if text == "" {
		if eol {
			return nil, nil, true
		}
		return []string{last}, [][]ViewSpan{clipSpans(lastSpans, 0, len(last))}, false
	}
	base := ""
	var all []ViewSpan
	if !eol {
		base = last
		all = append(all, lastSpans...)
	}
	for _, sp := range spans {
		all = append(all, ViewSpan{Start: sp.Start + len(base), End: sp.End + len(base), Style: sp.Style})
	}
	s := base + text
	eolAfter = strings.HasSuffix(s, "\n")
	body := strings.TrimSuffix(s, "\n")
	off := 0
	for {
		end := strings.IndexByte(body[off:], '\n')
		if end < 0 {
			end = len(body)
		} else {
			end += off
		}
		for _, p := range splitLine(off, end, body) {
			lines = append(lines, body[p[0]:p[1]])
			lineSpans = append(lineSpans, clipSpans(all, p[0], p[1]))
		}
		if end == len(body) {
			break
		}
		off = end + 1
	}
	return lines, lineSpans, eolAfter
}

// ViewTextOverflow は、行数が n の中身から捨てる古い行の数（上限 ViewTextMaxLines を超えた分）を返す。
func ViewTextOverflow(n int) int {
	return max(n-ViewTextMaxLines, 0)
}

// splitLine は、s[start:end]（改行を含まない 1 行）を、ViewTextMaxLineBytes バイト以下の範囲に分ける。
// 分ける位置は UTF-8 の文字の境目にし、境目が無い（不正な UTF-8）ならバイトの数で分ける。空の行は空の範囲 1 つにする。
func splitLine(start, end int, s string) [][2]int {
	var out [][2]int
	for end-start > ViewTextMaxLineBytes {
		i := start + ViewTextMaxLineBytes
		for i > start && !utf8.RuneStart(s[i]) {
			i--
		}
		if i == start {
			i = start + ViewTextMaxLineBytes
		}
		out = append(out, [2]int{start, i})
		start = i
	}
	return append(out, [2]int{start, end})
}

// clipSpans は、spans のうち [start, end) と重なる範囲を、その中に切り詰め、start からの位置にずらして返す。
// 重なりが空の範囲は捨てる。重なるものが無ければ nil を返す。
func clipSpans(spans []ViewSpan, start, end int) []ViewSpan {
	var out []ViewSpan
	for _, sp := range spans {
		s, e := max(sp.Start, start), min(sp.End, end)
		if s >= e {
			continue
		}
		out = append(out, ViewSpan{Start: s - start, End: e - start, Style: sp.Style})
	}
	return out
}
