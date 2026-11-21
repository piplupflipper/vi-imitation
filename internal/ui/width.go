package ui

//go:generate go run ./internal/widthgen

import (
	"fmt"
	"sort"
	"unicode"
	"unicode/utf8"
)

// DefaultTabStop は、タブ幅の既定値である（Vim の 'tabstop' の既定と同じ 8）。
// バッファのテキストは、設定の tab.width（FR-161）を Frame.TabStop、Columns.TabStop、Renderer.WrapLines の引数で渡して数える。
// 記号とアイコンの文字列の幅のように、バッファのテキストでない文字列は、この値で数える。
const DefaultTabStop = 8

// glyphKind は、1 つの文字を画面にどう出すかの種類である。
type glyphKind int

const (
	// glyphText は、文字をそのまま出す（幅 1 か 2）。
	glyphText glyphKind = iota
	// glyphTab は、タブである。次のタブ位置までを空白で埋める。
	glyphTab
	// glyphSpecial は、そのままでは出せない文字を ^A や <80> の形で出す。
	glyphSpecial
	// glyphWhitespace は、空白の記号を描くとき（FR-170）の、タブ文字と空白の文字である。text の記号で描く（cell）。
	// lineRule.glyph がタブ文字を、drawer.look が空白とノーブレークスペースを、この種類にする。
	glyphWhitespace
)

// glyph は、行の中の 1 文字（後に続く結合文字を含む）の、画面上の配置である。
type glyph struct {
	off   int       // 行の先頭からのバイト位置
	size  int       // バイト数。後に続く結合文字を含む
	col   int       // 画面上の開始の列（行の先頭を 0 とする）
	width int       // 画面上の列数
	kind  glyphKind // 出し方
	// text は、出す文字列である。glyphText では文字と結合文字、glyphSpecial では "^A" などの表記、glyphTab では空、
	// glyphWhitespace では空白の記号（タブ文字なら 2 文字か 3 文字、空白なら 1 文字。どの文字も幅 1）。
	text string
	// virt は、inline の仮想テキストの文字なら、その文字列の色の添字（VirtualTexts.styles）に 1 を足したもので、本文の文字なら 0 である
	// （lineWalk。FR-231）。仮想テキストの文字の size は 0 で、off は後ろの本文の文字の位置である。
	virt int
}

// cell は、セルごとに描く種類（glyphTab、glyphSpecial、glyphWhitespace）の文字の、左から i 番目（0 から数える）のセルの文字を返す。
//
//   - glyphTab: 空白
//   - glyphSpecial: 表記の i 文字目（表記は ASCII）
//   - glyphWhitespace: 記号が 1 文字ならその文字。2 文字（Vim の 'listchars' の tab:xy）なら、最初のセルは x、ほかは y。
//     3 文字（tab:xyz）なら、最後のセルは z、それより前の最初のセルは x、ほかは y（幅 1 のタブ文字は z だけになる。Vim と同じ）
func (g glyph) cell(i int) string {
	switch g.kind {
	case glyphSpecial:
		return g.text[i : i+1]
	case glyphWhitespace:
		var chars [3]string
		n := 0
		for off := 0; off < len(g.text) && n < len(chars); n++ {
			_, size := utf8.DecodeRuneInString(g.text[off:])
			chars[n] = g.text[off : off+size]
			off += size
		}
		switch {
		case n == 3 && i == g.width-1:
			return chars[2]
		case n >= 2 && i > 0:
			return chars[1]
		}
		return chars[0]
	case glyphText, glyphTab:
	}
	return " "
}

// nextGlyph は、行 line の off から始まる 1 文字を、列 col に置いたときの配置を返す。
// off は文字の境界（buffer.Position の Byte と同じ条件）を指し、len(line) より小さい必要がある。
//
// 表示幅は Vim 9.1 の 'ambiwidth' が single の既定に合わせる（width_test.go の TestRuneWidthVim を参照）。
//   - タブ: 次のタブ位置（tabStop の倍数）まで
//   - U+0000〜U+001F と U+007F: ^@ 〜 ^_ と ^? の 2 列
//   - U+0080〜U+009F と、不正な UTF-8 の 1 バイト: <80> の形の 4 列（16 進は小文字）
//   - Vim が表示できない文字として扱う U+200B などの書式文字: <200b> の形
//   - 結合文字（Unicode の Mn と Me）: 0 列。前の文字と合わせて 1 文字にする（internal/command/target の扱いと同じ）
//   - wideTable にある文字: 2 列
//   - それ以外: 1 列
//
// 行頭の結合文字は前の文字が無いので、空白に重ねて出す。幅は Vim の strdisplaywidth と同じく、
// 多くは 1 列、全角の結合文字（U+3099 など）は 2 列で、U+180B〜U+180D は <180b> の形にする（loneComposingTable）。
// タブと ^A などの後の結合文字は、幅 0 で、画面には出さない。
//
// glyphText の text は line の部分文字列で、文字ごとに割り当てをしない（行頭の結合文字だけは空白を足すので割り当てる）。
func nextGlyph(line string, off, col, tabStop int) glyph {
	// 速い道: 表示できる ASCII の文字で、後に結合文字が続かない（次のバイトが ASCII か行末）。
	if c := line[off]; c >= 0x20 && c < 0x7f && (off+1 == len(line) || line[off+1] < 0x80) {
		return glyph{off: off, size: 1, col: col, width: 1, kind: glyphText, text: line[off : off+1]}
	}
	r, size := utf8.DecodeRuneInString(line[off:])
	g := glyph{off: off, size: size, col: col}
	switch {
	case r == utf8.RuneError && size == 1:
		g.kind, g.text = glyphSpecial, fmt.Sprintf("<%02x>", line[off])
	case r == '\t':
		g.kind = glyphTab
		g.width = tabStop - col%tabStop
	case r < 0x20:
		g.kind, g.text = glyphSpecial, "^"+string(r+'@')
	case r == 0x7f:
		g.kind, g.text = glyphSpecial, "^?"
	case r < 0xa0 && r >= 0x80:
		g.kind, g.text = glyphSpecial, fmt.Sprintf("<%02x>", r)
	case unprintable(r):
		g.kind, g.text = glyphSpecial, fmt.Sprintf("<%04x>", r)
	case isComposing(r):
		g.kind, g.width = glyphText, loneComposingWidth(r)
		if g.width > 2 {
			g.kind, g.text = glyphSpecial, fmt.Sprintf("<%04x>", r)
		} else {
			g.text = " " + line[off:off+size]
		}
	default:
		g.kind, g.text = glyphText, line[off:off+size]
		g.width = runeWidth(r)
	}
	if g.kind == glyphSpecial {
		g.width = len(g.text)
	}
	// 後に続く結合文字を、同じ 1 文字に含める。
	for g.off+g.size < len(line) {
		c, n := utf8.DecodeRuneInString(line[g.off+g.size:])
		if (c == utf8.RuneError && n == 1) || !isComposing(c) {
			break
		}
		if g.kind == glyphText {
			if g.text[0] == ' ' && isComposing(r) {
				g.text += line[g.off+g.size : g.off+g.size+n]
			} else {
				g.text = line[g.off : g.off+g.size+n]
			}
		}
		g.size += n
	}
	return g
}

// isComposing は、r が結合文字（前の文字と合わせて 1 文字として扱う文字）かどうかを返す。
// internal/command/target と同じく、Unicode の Mn と Me を結合文字とする（Vim の utf_iscomposing() に当たる）。
func isComposing(r rune) bool {
	if r < 0x300 {
		return false // Mn と Me の最初の文字は U+0300
	}
	if r < bmpSize {
		return bmpComposing.has(r)
	}
	return unicode.In(r, unicode.Mn, unicode.Me)
}

// bmpSize は、bmpBits で引く文字の数（U+0000〜U+FFFF）である。
const bmpSize = 0x10000

// bmpBits は、U+0000〜U+FFFF の文字ごとの 1 ビットの表である。
type bmpBits [bmpSize / 64]uint64

func (b *bmpBits) set(r rune)      { b[r/64] |= 1 << (r % 64) }
func (b *bmpBits) has(r rune) bool { return b[r/64]&(1<<(r%64)) != 0 }

// bmpComposing と bmpWide は、U+0000〜U+FFFF の文字が結合文字（isComposing）か、表示幅が 2 列（wideTable）かを、
// 1 ビットで引く表である。nextGlyph が全角文字の 1 文字ごとに unicode.In と wideTable の二分探索を呼ぶと、
// 数十万文字の行を折り返して数えるときに時間の半分以上を占めた（PR #310 のレビューの推奨 2 のプロファイル）。
// 表は起動時に unicode.Mn、unicode.Me、wideTable から作るので、引いた結果は前と変わらない。
var bmpComposing, bmpWide = func() (c, w *bmpBits) {
	c, w = new(bmpBits), new(bmpBits)
	for _, t := range []*unicode.RangeTable{unicode.Mn, unicode.Me} {
		for _, r := range t.R16 {
			for x := int(r.Lo); x <= int(r.Hi); x += int(r.Stride) {
				c.set(rune(x))
			}
		}
	}
	for _, r := range wideTable {
		for x := r[0]; x <= r[1] && x < bmpSize; x++ {
			w.set(x)
		}
	}
	return c, w
}()

// unprintable は、Vim 9.1 が表示できない文字として <200b> の形で出す文字かどうかを返す
// （Vim の utf_printable() が偽を返す文字。U+D800〜U+DFFF は Go の文字列の復号で現れないので除く）。
func unprintable(r rune) bool {
	switch {
	case r == 0x070f, r == 0x180e, r == 0xfeff:
		return true
	case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2060 && r <= 0x206f:
		return true
	case r >= 0xfff9 && r <= 0xfffb, r == 0xfffe, r == 0xffff:
		return true
	}
	return false
}

// loneComposingWidth は、行頭に単独で置いた結合文字 r の表示幅を返す。2 より大きければ <180b> の形で出す幅である。
func loneComposingWidth(r rune) int {
	i := sort.Search(len(loneComposingTable), func(i int) bool { return loneComposingTable[i].hi >= r })
	if i < len(loneComposingTable) && loneComposingTable[i].lo <= r {
		return loneComposingTable[i].width
	}
	return 1
}

// runeWidth は、そのまま出せる文字 r の表示幅（0、1、2）を返す。
func runeWidth(r rune) int {
	if isComposing(r) {
		return 0
	}
	if r < wideTable[0][0] {
		return 1
	}
	if r < bmpSize {
		if bmpWide.has(r) {
			return 2
		}
		return 1
	}
	i := sort.Search(len(wideTable), func(i int) bool { return wideTable[i][1] >= r })
	if i < len(wideTable) && wideTable[i][0] <= r {
		return 2
	}
	return 1
}

// StringWidth は、行 s を列 0 から並べたときの表示幅を返す。タブ幅は tabStop（0 以下なら DefaultTabStop）で数える。
func StringWidth(s string, tabStop int) int {
	tabStop = tabStopOr(tabStop)
	col := 0
	for off := 0; off < len(s); {
		g := nextGlyph(s, off, col, tabStop)
		col += g.width
		off += g.size
	}
	return col
}

// tabStopOr は、0 以下のタブ幅を DefaultTabStop にして返す。
func tabStopOr(tabStop int) int {
	if tabStop <= 0 {
		return DefaultTabStop
	}
	return tabStop
}

// Columns は、望みの列（Vim の curswant）を表示幅で数える数え方である。
// internal/command の State.Columns（target.Columns）に差し込む（Column と ByteAt の 2 つのメソッドを持つ）。
// internal/command とは同じ層なので、インターフェースを import せずに、同じ形のメソッドを持たせる。
//
// ゼロ値は、タブ幅を DefaultTabStop で数え、空白の記号を描かないときの数え方である。
type Columns struct {
	// LineLayout は、行の文字の並べ方（タブ幅と、空白の記号を描くか。FR-161、FR-170）である。描く Frame の LineLayout と同じ値を渡す。
	// Whitespace が true なら、タブの上の列はタブの最初の列にし、タブ文字の記号（Symbols の SymbolWhitespaceTab）が空ならタブ文字を ^I の 2 列で数える（lineRule）。
	LineLayout
	// Symbols は、記号の対応（Renderer.Symbols と同じもの）である。Whitespace が true のときだけ読む。
	Symbols Symbols
}

// rule は、c の数え方の規則を返す。
func (c Columns) rule() lineRule {
	return newLineRule(c.LineLayout, c.Symbols.value)
}

// Column は、行 line の byteOff の位置の文字の列を、表示幅で数えて返す。
//
// タブの上では、タブが占める最後の列を返す。
// Vim はノーマルモードでタブの上のカーソルをタブの最後の列に置き、その列を望みの列にするので、これに合わせた
// （"\tx" の 1 文字目で j を押すと、下の行の 8 列目へ移る）。
// ただし空白の記号を描くとき（c.Whitespace）は、タブの最初の列を返す（Vim の 'list' と同じ。lineRule の doc）。
// 全角文字と ^A などの表記は、最初の列を返す。
// byteOff が文字の途中（結合文字の位置など）を指すときは、その文字の列を返す。行の長さ以上なら行の表示幅を返す。
//
// n は line の行の番号で、その行の inline の仮想テキスト（LineLayout.Virtual。FR-232）の列も数える。文字の前の仮想テキストの列は、
// その文字より左の列になる（ノーマルモードのカーソルは仮想テキストの後ろに置く）。行の長さ以上の byteOff は、行末の位置の仮想テキストを
// 数えない列（行末のカーソルは仮想テキストの前に置く）を返す。line は行の先頭からの部分の文字列でもよく、そのときは部分の後ろの位置の
// 仮想テキストを数えない。
//
// req: FR-232
func (c Columns) Column(n int, line string, byteOff int) int {
	rule := c.rule()
	w := rule.walk(n, line)
	col := 0
	for w.more() {
		g := w.next(col)
		switch {
		case g.virt != 0:
			if g.off >= len(line) && byteOff >= len(line) {
				return g.col
			}
		case byteOff < g.off+g.size:
			if g.kind == glyphTab {
				return g.col + g.width - 1
			}
			return g.col
		}
		col += g.width
	}
	return col
}

// ByteAt は、行 line で列 col を占める文字の位置（バイト）を返す。
// col が行の表示幅以上なら最後の文字の位置を、空の行では 0 を返す。負の col は 0 列として扱う。
// タブや全角文字の途中の列は、その文字の位置を返す（Vim の coladvance() と同じ）。
// n は line の行の番号で、inline の仮想テキストの列は、その後ろの文字の位置を返す（行末の位置の仮想テキストなら最後の文字の位置。
// Neovim 0.12.5 の j と k とマウスのクリックと同じ。FR-232）。
//
// req: FR-232
func (c Columns) ByteAt(n int, line string, col int) int {
	rule := c.rule()
	w := rule.walk(n, line)
	last := 0
	cur := 0
	for w.more() {
		g := w.next(cur)
		if col < g.col+g.width {
			if g.virt == 0 || g.off < len(line) {
				return g.off
			}
			return last
		}
		if g.virt == 0 {
			last = g.off
		}
		cur += g.width
	}
	return last
}
