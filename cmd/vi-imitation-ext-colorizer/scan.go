package main

import (
	"math"
	"regexp"
	"strconv"
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
	// rgbFn は rgb() と rgba()、hslFn は hsl() と hsla() の関数の形である。
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

// scanner は、1 行の中の色コードを探す。作った後は変えないので、goroutine の間で共有してよい。
//
// 認識する形と、範囲の区切り方は、nvim-colorizer.lua（上のコミット）の既定の振る舞いに合わせた。
// nvim-colorizer.lua は GPL-3.0 なので、そのコードは写さず、実物に同じ行を与えて出した範囲と色が一致するように書いた
// （scan_test.go の表は、2026-11-22 に Neovim 0.12.5 でその実物から取った値である）。
// 行の先頭から 1 バイトずつ進み、各位置で次の順に試して、最初に認めた形の分だけ進む。どれも認めなければ 1 バイト進む。
// 行の最後のバイトの位置では試さない（nvim-colorizer.lua の buffer.lua の parse_lines と同じ）。
//
//  1. # で始まる形（hexAt）。# の位置では、認めなくても 2 と 3 を試さない
//  2. 0x で始まる形（argbAt）
//  3. rgb(、rgba(、hsl(、hsla( で始まる関数の形（rgbAt、hslAt）。rgba( と hsla( は、rgb( と hsl( より先に当てる
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
	if s.f.rgbFn && strings.HasPrefix(line[i:], "rgb") {
		if n, rgb, ok := rgbAt(line, i); ok {
			return n, rgb, true
		}
	}
	if s.f.hslFn && strings.HasPrefix(line[i:], "hsl") {
		if n, rgb, ok := hslAt(line, i); ok {
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
// 色の名前を認識するときは、- も含める。nvim-colorizer.lua は、色の名前の表を作るときに extra_word_chars の - を
// 英字や数字と同じ文字の種類に足し、# と 0x の形の区切りの判定もその種類で行うためである（#fff- と -#fff を認めない）。
func (s *scanner) wordChar(c byte) bool {
	return isAlnum(c) || c == '-' && s.f.namesEnabled()
}

// isNameChar は、c が色の名前の一部になりうる文字かを返す。名前の前後がこの文字なら、名前と認めない。
// 英字と数字のほかに - を含める（nvim-colorizer.lua の names の extra_word_chars の既定。text-red-500 の red を名前にしない）。
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
// 背景の色との合成はせず、黒に寄せる（nvim-colorizer.lua の #RGBA、#RRGGBBAA、0xAARRGGBB と同じ）。
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
// 前の文字は問わず、後ろが英字か数字なら認めない（nvim-colorizer.lua の AARRGGBB と同じ）。
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

// cssSpace は、関数の形の中の空白である（Lua のパターンの %s と同じ文字。nvim-colorizer.lua は Lua のパターンで読む）。
const cssSpace = `[\t\n\v\f\r ]`

// rgbPattern と hslPattern は、関数の形の引数の並びである。数は数字と . の並びで、成分ごとに区切りの空白 1 つと , を拾う。
// Go の regexp は、先頭に寄せた上で、後戻りで探す実装と同じ部分一致を選ぶ（regexp の doc）ので、
// nvim-colorizer.lua の Lua のパターンと同じ区切り方になる（rgb(255, 200,) の 200 を 20 と 0 に分ける、など）。
var (
	rgbPattern = regexp.MustCompile(`^(rgba?)\(` + cssSpace + `*` +
		`([.\d]+)(%?)(` + cssSpace + `?)` + cssSpace + `*(,?)` + cssSpace + `*` +
		`([.\d]+)(%?)(` + cssSpace + `?)` + cssSpace + `*(,?)` + cssSpace + `*` +
		`([.\d]+)(%?)` + cssSpace + `*(/?,?)` + cssSpace + `*` +
		`([.\d]*)(%?)` + cssSpace + `*\)`)
	rgbHexPattern = regexp.MustCompile(`^rgb\(` + cssSpace + `*([0-9a-fA-F]{6})` + cssSpace + `*\)`)
	// rgbaHexPattern は rgba(RRGGBBAA) の形（Hyprland の設定の書き方）である。
	rgbaHexPattern = regexp.MustCompile(`^rgba\(` + cssSpace + `*([0-9a-fA-F]{8})` + cssSpace + `*\)`)
	hslPattern     = regexp.MustCompile(`^(hsla?)\(` + cssSpace + `*` +
		`([.\d]+)([deg]*)([turn]*)(` + cssSpace + `?)` + cssSpace + `*(,?)` + cssSpace + `*` +
		`([.\d]+)%?(` + cssSpace + `?)` + cssSpace + `*(,?)` + cssSpace + `*` +
		`([.\d]+)%?` + cssSpace + `*(/?,?)` + cssSpace + `*` +
		`([.\d]*)(%?)` + cssSpace + `*\)`)
)

// validSeparators は、関数の形の区切りが正しいかを返す。commas は成分の後ろの , と / の並び、spaces は成分の後ろの空白の並びである。
// , があれば、, の数が成分の間の数（不透明度があれば 3、無ければ 2）と同じでなければならない。
// , が無ければ、最初の 2 つの成分の後ろに空白が要り、不透明度は / で区切る（rgb(1 2 3 / 50%)）。
func validSeparators(commas, spaces string, hasAlpha bool) bool {
	want := 2
	if hasAlpha {
		want = 3
	}
	if strings.Contains(commas, ",") {
		return strings.Count(commas, ",") == want
	}
	if len(spaces) < 2 {
		return false
	}
	return !hasAlpha || commas == "/"
}

// number は、数字と . の並びを数として読む。読めなければ（. だけ、. が 2 つ以上）false を返す。
func number(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// truncRGB は、0 から 255 の小数の成分を切り捨てて色にする（nvim-colorizer.lua は LuaJIT の string.format の %02x で切り捨てる）。
func truncRGB(r, g, b float64) uint32 {
	return uint32(r)<<16 | uint32(g)<<8 | uint32(b)
}

// rgbAt は、位置 i から始まる rgb() と rgba() の形を読む。前の文字は問わない（xrgb(1,2,3) の rgb( から認める）。
// 成分は 0 から 255 の数か、3 つとも % の付いた数である。255 と 100% を超える値は上限に切り詰める。
// 不透明度は書いてよいが、色には掛けない（nvim-colorizer.lua と同じ）。rgb() と rgba() のどちらにも、不透明度を書いても書かなくてもよい。
// 引数の並びとして読めなければ、rgb(RRGGBB) と rgba(RRGGBBAA) の形を試す。
func rgbAt(line string, i int) (int, uint32, bool) {
	rest := line[i:]
	m := rgbPattern.FindStringSubmatch(rest)
	if m == nil || m[5] != m[9] {
		if h := rgbHexPattern.FindStringSubmatch(rest); h != nil {
			v, _ := strconv.ParseUint(h[1], 16, 32)
			return len(h[0]), uint32(v), true
		}
		if h := rgbaHexPattern.FindStringSubmatch(rest); h != nil {
			v, _ := strconv.ParseUint(h[1], 16, 32)
			return len(h[0]), withAlpha(uint32(v>>24), uint32(v>>16&0xff), uint32(v>>8&0xff), float64(v&0xff)/255), true
		}
		return 0, 0, false
	}
	units := m[3] + m[7] + m[11]
	if strings.Contains(units, "%") && strings.Count(units, "%") != 3 {
		return 0, 0, false
	}
	hasAlpha := m[13] != ""
	if !validSeparators(m[5]+m[9]+m[12], m[4]+m[8], hasAlpha) {
		return 0, 0, false
	}
	if hasAlpha {
		if _, ok := number(m[13]); !ok {
			return 0, 0, false
		}
	}
	var c [3]float64
	for k, idx := range []int{2, 6, 10} {
		v, ok := number(m[idx])
		if !ok {
			return 0, 0, false
		}
		if m[3] == "%" {
			if v > 100 {
				v = 255
			} else {
				v = v / 100 * 255
			}
		} else {
			v = min(v, 255)
		}
		c[k] = v
	}
	return len(m[0]), truncRGB(c[0], c[1], c[2]), true
}

// hslAt は、位置 i から始まる hsl() と hsla() の形を読む。前の文字は問わない。
// 色相は度で、deg か turn を付けてよい。彩度と明度の % は省いてよい。360 度と 100% を超える値は、色相は 1 周の中へ戻し、ほかは上限に切り詰める。
// 不透明度は色に掛けない（nvim-colorizer.lua と同じ）。
func hslAt(line string, i int) (int, uint32, bool) {
	m := hslPattern.FindStringSubmatch(line[i:])
	if m == nil || m[6] != m[9] {
		return 0, 0, false
	}
	hasAlpha := m[12] != ""
	if m[3] != "" && m[3] != "deg" && m[4] != "turn" {
		return 0, 0, false
	}
	if !validSeparators(m[6]+m[9]+m[11], m[5]+m[8], hasAlpha) {
		return 0, 0, false
	}
	if hasAlpha {
		if _, ok := number(m[12]); !ok {
			return 0, 0, false
		}
	}
	h, ok := number(m[2])
	if !ok {
		h = 1
	}
	if m[4] == "turn" {
		h *= 360
	}
	if h > 360 {
		turns := h / 360
		h = 360 * (turns - math.Floor(turns))
	}
	sat, ok1 := number(m[7])
	light, ok2 := number(m[10])
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	r, g, b := hslToRGB(h/360, min(sat, 100)/100, min(light, 100)/100)
	return len(m[0]), truncRGB(r, g, b), true
}

// hslToRGB は、0 から 1 の色相、彩度、明度を、0 から 255 の小数の成分にする（CSS Color Module Level 3 の HSL の変換）。
func hslToRGB(h, s, l float64) (float64, float64, float64) {
	if s == 0 {
		return l * 255, l * 255, l * 255
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	return 255 * hueToRGB(p, q, h+1.0/3), 255 * hueToRGB(p, q, h), 255 * hueToRGB(p, q, h-1.0/3)
}

func hueToRGB(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6:
		return p + (q-p)*6*t
	case t < 1.0/2:
		return q
	case t < 2.0/3:
		return p + (q-p)*(2.0/3-t)*6
	}
	return p
}
