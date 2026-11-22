package main

import (
	"math"
	"strings"
)

// span は、1 行の中の色コードの範囲と、その色である。
type span struct {
	// start と end は、行の先頭から数えたバイトの位置である。start を含み、end を含まない。
	start, end int
	// rgb は、0xrrggbb の形の 24 ビットの色である。
	rgb uint32
}

// formats は、認識する色コードの形である（設定の hex、rgb、hsl、names。config.go）。
// 既定は nvim-colorizer.lua の既定と同じにする（defaultFormats）。
type formats struct {
	// hexRGB、hexRGBA、hexRRGGBB、hexRRGGBBAA は、# の後に 16 進の数字が 3、4、6、8 個続く形である。
	hexRGB, hexRGBA, hexRRGGBB, hexRRGGBBAA bool
	// hexAARRGGBB は、0x の後に 16 進の数字が 3、6、8 個続く形（0xRGB、0xRRGGBB、0xAARRGGBB）である。
	hexAARRGGBB bool
	// rgbFn は rgb() と rgba()、hslFn は hsl() と hsla() の関数の形である（cssfunc.go）。
	rgbFn, hslFn bool
	// namesLower、namesCamel、namesUpper は、色の名前（Neovim の nvim_get_color_map() の名前。names_table.go）を、
	// 小文字にした綴り（red、lightblue）、表のままの綴り（Red、LightBlue）、大文字にした綴り（RED、LIGHTBLUE）で認識するかである。
	namesLower, namesCamel, namesUpper bool
}

// defaultFormats は、nvim-colorizer.lua の既定の形である。
// オーナーの nvim の nvim-colorizer.lua（catgoose/nvim-colorizer.lua のコミット 72a05f62c52241bc7441c820eb53946f92b2e6a4）は
// filetypes だけを渡すので、lua/colorizer/config.lua の plugin_user_default_options が既定になる。
// そこでは names、RGB、RGBA、RRGGBB が true で、RRGGBBAA、AARRGGBB、rgb_fn、hsl_fn が false、
// names_opts は lowercase と camelcase が true で uppercase が false である。
func defaultFormats() formats {
	return formats{hexRGB: true, hexRGBA: true, hexRRGGBB: true, namesLower: true, namesCamel: true}
}

// hexEnabled は、# で始まる形のどれかを認識するかを返す。
func (f *formats) hexEnabled() bool {
	return f.hexRGB || f.hexRGBA || f.hexRRGGBB || f.hexRRGGBBAA
}

// namesEnabled は、色の名前を認識するかを返す。
func (f *formats) namesEnabled() bool {
	return f.namesLower || f.namesCamel || f.namesUpper
}

// maxCodeBytes は、1 つの色コードの長さの上限である。関数の形（cssfunc.go）は、これより長いものを認めない。
// # の形は最大 9 バイト、0x の形は 10 バイト、色の名前は 20 バイトなので、どの形もこの長さに収まる。
// 長い行を編集の周りだけ数え直すとき（incremental.go）の、編集の前に遡る幅にも使う。
const maxCodeBytes = 256

// scanner は、1 行の中の色コードを探す。作った後は変えないので、goroutine の間で共有してよい。
//
// # の形、0x の形、色の名前の認識と、範囲の区切り方は、nvim-colorizer.lua（上のコミット）の既定の振る舞いに合わせた。
// nvim-colorizer.lua は GPL-3.0 なので、そのコードは写さず、実物に同じ行を与えて出した範囲と色が一致するように書いた。
// rgb() と hsl() の関数の形は、CSS Color Module Level 4 の文法から書いた（cssfunc.go。nvim-colorizer.lua との違いは docs/usage.md）。
// 行の先頭から 1 バイトずつ進み、各位置で次の順に試して、最初に認めた形の分だけ進む。どれも認めなければ 1 バイト進む。
// 行の最後のバイトの位置では試さない（1 バイトの色コードは無い）。
//
//  1. # で始まる形（hexAt）。# の位置では、認めなくても 2 と 3 を試さない
//  2. 0x で始まる形（argbAt）
//  3. rgb(、rgba(、hsl(、hsla( で始まる関数の形（cssFuncAt）
//  4. 色の名前（nameAt）
type scanner struct {
	f formats
	// names は、認識する綴りから色への対応である。小文字にした綴り、大文字にした綴り、表のままの綴りの順に、先に入れたものを残す。
	names map[string]uint32
	// nameMin と nameMax は、names の綴りの長さの最小と最大である。
	nameMin, nameMax int
}

// newScanner は、f の形を認識する scanner を作る。
func newScanner(f formats) *scanner {
	s := &scanner{f: f, names: map[string]uint32{}}
	add := func(name string, rgb uint32) {
		if _, ok := s.names[name]; ok {
			return
		}
		s.names[name] = rgb
		if s.nameMin == 0 || len(name) < s.nameMin {
			s.nameMin = len(name)
		}
		s.nameMax = max(s.nameMax, len(name))
	}
	for _, n := range colorNames {
		if f.namesLower {
			add(strings.ToLower(n.name), n.rgb)
		}
	}
	for _, n := range colorNames {
		if f.namesUpper {
			add(strings.ToUpper(n.name), n.rgb)
		}
	}
	for _, n := range colorNames {
		if f.namesCamel {
			add(n.name, n.rgb)
		}
	}
	return s
}

// scanLine は、行 line の中の色コードの範囲を、前から順に返す。無ければ nil を返す。
//
// req: FR-260
func (s *scanner) scanLine(line string) []span {
	var out []span
	for i := 0; i < len(line)-1; {
		n, rgb, ok := s.at(line, i)
		if !ok {
			i++
			continue
		}
		out = append(out, span{start: i, end: i + n, rgb: rgb})
		i += n
	}
	return out
}

// at は、line の位置 i から始まる色コードを探し、そのバイト数と色を返す。
func (s *scanner) at(line string, i int) (int, uint32, bool) {
	c := line[i]
	if c == '#' && s.f.hexEnabled() {
		return s.hexAt(line, i)
	}
	if c == '0' && s.f.hexAARRGGBB {
		if n, rgb, ok := s.argbAt(line, i); ok {
			return n, rgb, true
		}
	}
	if (c == 'r' && s.f.rgbFn) || (c == 'h' && s.f.hslFn) {
		if n, rgb, ok := cssFuncAt(line, i); ok {
			return n, rgb, true
		}
	}
	if s.f.namesEnabled() {
		return s.nameAt(line, i)
	}
	return 0, 0, false
}

// isAlnum は、c が ASCII の英字か数字かを返す。
func isAlnum(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c|0x20 && c|0x20 <= 'z'
}

// wordChar は、# と 0x の形の前後にあれば、その形を認めない文字かを返す。英字と数字である。
// 色の名前を認識するときは、- も含める（-#fff と #fff- を認めない。nvim-colorizer.lua に行を与えて確かめた振る舞い）。
func (s *scanner) wordChar(c byte) bool {
	return isAlnum(c) || c == '-' && s.f.namesEnabled()
}

// isNameChar は、c が色の名前の一部になりうる文字かを返す。名前の前後がこの文字なら、名前と認めない。
// 英字と数字のほかに - を含める（text-red-500 の red を名前にしない）。
func isNameChar(c byte) bool {
	return isAlnum(c) || c == '-'
}

// hexDigit は、c が 16 進の数字ならその値を返す。
func hexDigit(c byte) (uint32, bool) {
	switch {
	case '0' <= c && c <= '9':
		return uint32(c - '0'), true
	case 'a' <= c|0x20 && c|0x20 <= 'f':
		return uint32(c|0x20-'a') + 10, true
	}
	return 0, false
}

// withAlpha は、各成分に不透明度 alpha（0 から 1）を掛けて切り捨てた色を返す。
// 背景の色との合成はせず、黒に寄せる（#RGBA、#RRGGBBAA、0xAARRGGBB。nvim-colorizer.lua に行を与えて確かめた色と同じ）。
func withAlpha(r, g, b uint32, alpha float64) uint32 {
	f := func(c uint32) uint32 { return uint32(math.Floor(float64(c) * alpha)) }
	return f(r)<<16 | f(g)<<8 | f(b)
}

// hexAt は、位置 i の # から始まる #RGB、#RGBA、#RRGGBB、#RRGGBBAA を読む。
// 前と後ろが英字か数字なら認めない（a#fff、#fffg）。認める数字の個数の最大まで読んだ後に英字か数字が続くときも認めない
// （#RRGGBBAA を認めない設定の #ff000080 は、#ff0000 の後に 8 が続くので、どの形とも認めない）。
func (s *scanner) hexAt(line string, i int) (int, uint32, bool) {
	if i > 0 && s.wordChar(line[i-1]) {
		return 0, 0, false
	}
	maxDigits := 0
	for _, d := range []struct {
		n  int
		on bool
	}{{3, s.f.hexRGB}, {4, s.f.hexRGBA}, {6, s.f.hexRRGGBB}, {8, s.f.hexRRGGBBAA}} {
		if d.on {
			maxDigits = d.n
		}
	}
	var digits [8]uint32
	n := 0
	for j := i + 1; j < len(line) && n < maxDigits; j++ {
		v, ok := hexDigit(line[j])
		if !ok {
			break
		}
		digits[n] = v
		n++
	}
	if end := i + 1 + n; end < len(line) && s.wordChar(line[end]) {
		return 0, 0, false
	}
	byte2 := func(k int) uint32 { return digits[k]<<4 | digits[k+1] }
	switch {
	case n == 3 && s.f.hexRGB:
		return 4, (digits[0]*17)<<16 | (digits[1]*17)<<8 | digits[2]*17, true
	case n == 4 && s.f.hexRGBA:
		return 5, withAlpha(digits[0]*17, digits[1]*17, digits[2]*17, float64(digits[3])/15), true
	case n == 6 && s.f.hexRRGGBB:
		return 7, byte2(0)<<16 | byte2(2)<<8 | byte2(4), true
	case n == 8 && s.f.hexRRGGBBAA:
		return 9, withAlpha(byte2(0), byte2(2), byte2(4), float64(byte2(6))/255), true
	}
	return 0, 0, false
}

// argbAt は、位置 i の 0x（0X）から始まる 0xRGB、0xRRGGBB、0xAARRGGBB を読む。
// 前の文字は問わず、後ろが英字か数字なら認めない。
func (s *scanner) argbAt(line string, i int) (int, uint32, bool) {
	if i+1 >= len(line) || line[i] != '0' || line[i+1]|0x20 != 'x' {
		return 0, 0, false
	}
	var v uint64
	n := 0
	for j := i + 2; j < len(line) && n < 10; j++ {
		d, ok := hexDigit(line[j])
		if !ok {
			break
		}
		v = v<<4 | uint64(d)
		n++
	}
	if end := i + 2 + n; end < len(line) && s.wordChar(line[end]) {
		return 0, 0, false
	}
	switch n {
	case 3:
		r, g, b := uint32((v>>8)&0xf)*17, uint32((v>>4)&0xf)*17, uint32(v&0xf)*17
		return 5, r<<16 | g<<8 | b, true
	case 6:
		return 8, uint32(v), true
	case 8:
		return 10, withAlpha(uint32(v>>16&0xff), uint32(v>>8&0xff), uint32(v&0xff), float64(v>>24&0xff)/255), true
	}
	return 0, 0, false
}

// nameAt は、位置 i から始まる色の名前を読む。
// 前の文字が名前の一部になりうる文字（isNameChar）なら認めない。i から始まる綴りのうち最も長いものを採り、
// その後ろが名前の一部になりうる文字なら、より短い綴りを試さずに認めない（redish の red、Blue3x の Blue を名前にしない）。
//
// 表の名前はどれも英字と数字だけからなる（TestNameTable）ので、この規則で認めるのは、i から続く英字と数字の並びの全体が
// 名前であり、その後ろが - でないときだけである。並びより短い綴りは、後ろに英字か数字が続くので認めない。
// このため、並びを 1 回だけ表から引く。
func (s *scanner) nameAt(line string, i int) (int, uint32, bool) {
	if i > 0 && isNameChar(line[i-1]) {
		return 0, 0, false
	}
	j := i
	for j < len(line) && isAlnum(line[j]) {
		j++
	}
	if n := j - i; n < s.nameMin || n > s.nameMax || j < len(line) && line[j] == '-' {
		return 0, 0, false
	}
	rgb, ok := s.names[line[i:j]]
	return j - i, rgb, ok
}
