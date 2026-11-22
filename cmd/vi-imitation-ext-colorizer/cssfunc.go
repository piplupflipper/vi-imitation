package main

import (
	"math"
	"strconv"
	"strings"
)

// この節の読み方は、W3C の CSS Color Module Level 4（Candidate Recommendation Draft、2026-11-18。
// https://www.w3.org/TR/2026/CRD-css-color-4-20261118/）の rgb() と hsl() の文法と値の扱いだけから書いた。
// nvim-colorizer.lua のコードは使っていない（違いは docs/usage.md の「色コードの着色」）。
//
// 仕様から採ったもの:
//
//   - rgb() と rgba() は同じ文法で、古い書き方 rgb( <percentage>#{3} , <alpha-value>? ) と rgb( <number>#{3} , <alpha-value>? )、
//     新しい書き方 rgb( [ <number> | <percentage> | none ]{3} [ / [ <alpha-value> | none ] ]? ) のどちらかである
//   - hsl() と hsla() も同じ文法で、古い書き方 hsl( <hue>, <percentage>, <percentage>, <alpha-value>? )、
//     新しい書き方は色相が [ <hue> | none ]、彩度と明度が [ <percentage> | <number> | none ]、不透明度が [ / [ <alpha-value> | none ] ]? である
//   - <alpha-value> は <number> か <percentage>、<hue> は <number> か <angle> である
//   - none は、その成分を 0 として扱う
//   - 範囲の外の値は誤りでなく、範囲に切り詰める（r、g、b は 0% = 0.0、100% = 255.0）。彩度は 0% より小さければ 0% にする
//   - 色相は [0, 360) に戻す
//   - HSL から sRGB への変換は、仕様の見本のコードの式（f(n) = light - a * max(-1, min(k - 3, 9 - k, 1))、
//     k = (n + hue / 30) mod 12、a = sat * min(light, 1 - light)、結果は [f(0), f(8), f(4)]）である
//   - 8 ビットの値への丸めは、16.2.2 の「切り捨てず、+∞ の側へ丸める」に従い、最も近い整数（同じ近さなら大きい側）にする
//
// 仕様に無いので、この拡張機能が決めたもの:
//
//   - 不透明度は読むが、色に掛けない（塗る範囲の下に合成する背景の色が無いため）
//   - 明度も 0% から 100% に切り詰める。変換の後の各成分も 0 から 255 に切り詰める
//   - 関数の名前は小文字だけを認め、単位（deg、grad、rad、turn）は大文字と小文字を区別しない
//   - 関数の名前の前が、CSS の識別子の一部になる文字（英字、数字、_、-、ASCII の外の文字）なら、関数と認めない
//   - 関数の全体が maxCodeBytes を超えるものと、コメント（/* */）を含むものは認めない
//   - 数は CSS の数の書き方（符号、小数、指数。1e2、+.5）で読む。<angle> の単位は deg、grad、rad、turn である

// cssTokenKind は、関数の引数の中の字句の種類である。
type cssTokenKind int

const (
	tokNumber  cssTokenKind = iota // <number>
	tokPercent                     // <percentage>
	tokAngle                       // <angle>（値は度に直したもの）
	tokNone                        // none
	tokComma                       // ,
	tokSlash                       // /
)

// cssToken は、関数の引数の中の 1 つの字句である。
type cssToken struct {
	kind cssTokenKind
	v    float64
}

// isIdentByte は、c が CSS の識別子の一部になる文字（のバイト）かを返す。
func isIdentByte(c byte) bool {
	return isAlnum(c) || c == '_' || c == '-' || c >= 0x80
}

// isCSSSpace は、c が CSS の空白（空白、タブ、改行、復帰、改ページ）かを返す。
func isCSSSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// cssFuncAt は、位置 i から始まる rgb()、rgba()、hsl()、hsla() を読み、そのバイト数と色を返す。
//
// req: FR-260
func cssFuncAt(line string, i int) (int, uint32, bool) {
	if i > 0 && isIdentByte(line[i-1]) {
		return 0, 0, false
	}
	rest := line[i:min(len(line), i+maxCodeBytes)]
	var isHSL bool
	var open int
	switch {
	case strings.HasPrefix(rest, "rgba("), strings.HasPrefix(rest, "hsla("):
		open = 5
	case strings.HasPrefix(rest, "rgb("), strings.HasPrefix(rest, "hsl("):
		open = 4
	default:
		return 0, 0, false
	}
	isHSL = rest[0] == 'h'
	toks, n, ok := cssArgs(rest, open)
	if !ok {
		return 0, 0, false
	}
	var rgb uint32
	if isHSL {
		rgb, ok = hslColor(toks)
	} else {
		rgb, ok = rgbColor(toks)
	}
	if !ok {
		return 0, 0, false
	}
	return n, rgb, true
}

// cssArgs は、s の位置 p（開き括弧の次）から閉じ括弧までの字句を読み、字句の列と、閉じ括弧の次の位置を返す。
// 数、百分率、角度、none、,、/ のほかの字句（知らない単位、ほかの識別子、コメント）があれば false を返す。
func cssArgs(s string, p int) ([]cssToken, int, bool) {
	var toks []cssToken
	for {
		for p < len(s) && isCSSSpace(s[p]) {
			p++
		}
		if p >= len(s) {
			return nil, 0, false
		}
		switch c := s[p]; {
		case c == ')':
			return toks, p + 1, true
		case c == ',':
			toks = append(toks, cssToken{kind: tokComma})
			p++
		case c == '/':
			if p+1 < len(s) && s[p+1] == '*' {
				return nil, 0, false
			}
			toks = append(toks, cssToken{kind: tokSlash})
			p++
		case strings.HasPrefix(strings.ToLower(s[p:min(len(s), p+4)]), "none") && (p+4 >= len(s) || !isIdentByte(s[p+4])):
			toks = append(toks, cssToken{kind: tokNone})
			p += 4
		default:
			t, next, ok := cssNumeric(s, p)
			if !ok {
				return nil, 0, false
			}
			toks = append(toks, t)
			p = next
		}
	}
}

// cssNumeric は、s の位置 p から CSS の数を読み、続く % か単位で、数、百分率、角度の字句にする。
func cssNumeric(s string, p int) (cssToken, int, bool) {
	v, next, ok := cssNumber(s, p)
	if !ok {
		return cssToken{}, 0, false
	}
	if next < len(s) && s[next] == '%' {
		return cssToken{kind: tokPercent, v: v}, next + 1, true
	}
	end := next
	for end < len(s) && isIdentByte(s[end]) {
		end++
	}
	if end == next {
		return cssToken{kind: tokNumber, v: v}, next, true
	}
	switch strings.ToLower(s[next:end]) {
	case "deg":
	case "grad":
		v *= 0.9
	case "rad":
		v *= 180 / math.Pi
	case "turn":
		v *= 360
	default:
		return cssToken{}, 0, false
	}
	return cssToken{kind: tokAngle, v: v}, end, true
}

// cssNumber は、s の位置 p から CSS の数（[+-]? の後に、数字の並びと . と数字の並び、続く e と符号と数字の並び）を読む。
// 整数の部分と小数の部分の少なくとも一方に数字が要る。e は、後に数字（か符号と数字）が続くときだけ数の一部にする。
func cssNumber(s string, p int) (float64, int, bool) {
	start := p
	if p < len(s) && (s[p] == '+' || s[p] == '-') {
		p++
	}
	digits := func() int {
		n := 0
		for p < len(s) && '0' <= s[p] && s[p] <= '9' {
			p++
			n++
		}
		return n
	}
	intDigits := digits()
	fracDigits := 0
	if p+1 < len(s) && s[p] == '.' && '0' <= s[p+1] && s[p+1] <= '9' {
		p++
		fracDigits = digits()
	}
	if intDigits+fracDigits == 0 {
		return 0, 0, false
	}
	if p < len(s) && s[p]|0x20 == 'e' {
		q := p + 1
		if q < len(s) && (s[q] == '+' || s[q] == '-') {
			q++
		}
		if q < len(s) && '0' <= s[q] && s[q] <= '9' {
			p = q
			digits()
		}
	}
	v, err := strconv.ParseFloat(s[start:p], 64)
	return v, p, err == nil
}

// splitArgs は、引数の字句の列を、3 つの成分に分ける。コンマがあれば古い書き方（legacy を true にする）、無ければ新しい書き方として読む。
//
//   - 新しい書き方: c c c か c c c / a。成分は数、百分率、角度、none のどれか（関数ごとの種類の検査は rgbColor と hslColor）、
//     a は数、百分率、none のどれか
//   - 古い書き方: c , c , c か c , c , c , a。成分に none は書けない。a は数か百分率
//
// どちらの形でもなければ false を返す。不透明度は読むだけで、色には使わない。
func splitArgs(toks []cssToken) (c [3]cssToken, legacy, ok bool) {
	isValue := func(t cssToken) bool { return t.kind != tokComma && t.kind != tokSlash }
	hasComma := false
	for _, t := range toks {
		hasComma = hasComma || t.kind == tokComma
	}
	if !hasComma {
		if len(toks) != 3 && len(toks) != 5 {
			return c, false, false
		}
		if !isValue(toks[0]) || !isValue(toks[1]) || !isValue(toks[2]) {
			return c, false, false
		}
		if len(toks) == 5 && (toks[3].kind != tokSlash || !isAlpha(toks[4], true)) {
			return c, false, false
		}
		return [3]cssToken{toks[0], toks[1], toks[2]}, false, true
	}
	if len(toks) != 5 && len(toks) != 7 {
		return c, false, false
	}
	for k := 1; k < len(toks); k += 2 {
		if toks[k].kind != tokComma {
			return c, false, false
		}
	}
	for k := 0; k < len(toks); k += 2 {
		if !isValue(toks[k]) || toks[k].kind == tokNone {
			return c, false, false
		}
	}
	if len(toks) == 7 && !isAlpha(toks[6], false) {
		return c, false, false
	}
	return [3]cssToken{toks[0], toks[2], toks[4]}, true, true
}

// isAlpha は、t が <alpha-value>（数か百分率）か、noneOK なら none かを返す。
func isAlpha(t cssToken, noneOK bool) bool {
	return t.kind == tokNumber || t.kind == tokPercent || noneOK && t.kind == tokNone
}

// round8 は、0 から 255 の値を 8 ビットに丸める（同じ近さなら大きい側）。範囲の外は切り詰める。
func round8(v float64) uint32 {
	return uint32(math.Floor(min(max(v, 0), 255) + 0.5))
}

// rgbColor は、rgb() と rgba() の字句の列から色を求める。
func rgbColor(toks []cssToken) (uint32, bool) {
	c, legacy, ok := splitArgs(toks)
	if !ok {
		return 0, false
	}
	if legacy && (c[0].kind != c[1].kind || c[1].kind != c[2].kind) {
		return 0, false
	}
	var out uint32
	for _, t := range c {
		var v float64
		switch t.kind {
		case tokNumber:
			v = t.v
		case tokPercent:
			v = t.v / 100 * 255
		case tokNone:
		case tokAngle, tokComma, tokSlash:
			return 0, false
		}
		out = out<<8 | round8(v)
	}
	return out, true
}

// hslColor は、hsl() と hsla() の字句の列から色を求める。
func hslColor(toks []cssToken) (uint32, bool) {
	c, legacy, ok := splitArgs(toks)
	if !ok {
		return 0, false
	}
	var hue float64
	switch c[0].kind {
	case tokNumber, tokAngle:
		hue = c[0].v
	case tokNone:
	case tokPercent, tokComma, tokSlash:
		return 0, false
	}
	var sl [2]float64
	for k, t := range c[1:] {
		switch t.kind {
		case tokPercent:
			sl[k] = t.v
		case tokNumber:
			if legacy {
				return 0, false
			}
			sl[k] = t.v
		case tokNone:
		case tokAngle, tokComma, tokSlash:
			return 0, false
		}
		sl[k] = min(max(sl[k], 0), 100)
	}
	hue = math.Mod(hue, 360)
	if hue < 0 {
		hue += 360
	}
	sat, light := sl[0]/100, sl[1]/100
	a := sat * min(light, 1-light)
	f := func(n float64) uint32 {
		k := math.Mod(n+hue/30, 12)
		return round8(255 * (light - a*max(-1, min(k-3, 9-k, 1))))
	}
	return f(0)<<16 | f(8)<<8 | f(4), true
}
