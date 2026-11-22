package main

import (
	"fmt"
	"strings"
	"testing"
)

// allFormats は、色の名前の大文字の綴りのほかのすべての形を認識する設定である。
func allFormats() formats {
	return formats{
		hexRGB: true, hexRGBA: true, hexRRGGBB: true, hexRRGGBBAA: true, hexAARRGGBB: true,
		rgbFn: true, hslFn: true, namesLower: true, namesCamel: true,
	}
}

// spansString は、範囲の列を「始まり-終わり:rrggbb」を空白で区切った形にする。
func spansString(spans []span) string {
	parts := make([]string, 0, len(spans))
	for _, sp := range spans {
		parts = append(parts, fmt.Sprintf("%d-%d:%06x", sp.start, sp.end, sp.rgb))
	}
	return strings.Join(parts, " ")
}

// TestScanLineMatchesColorizer は、行の中の色コードの範囲と色が、nvim-colorizer.lua と同じになることを確かめる。
// 期待値は、2026-11-22 に Neovim 0.12.5 と nvim-colorizer.lua（catgoose/nvim-colorizer.lua のコミット
// 72a05f62c52241bc7441c820eb53946f92b2e6a4）の lua/colorizer/buffer.lua の parse_lines に同じ行を与えて取った。
// def は setup に filetypes だけを渡したとき（オーナーの nvim と同じ既定）、all は user_default_options で
// RRGGBBAA、AARRGGBB、rgb_fn、hsl_fn を true にしたときの結果である。
//
// req: FR-260
func TestScanLineMatchesColorizer(t *testing.T) {
	cases := []struct {
		line     string
		def, all string
	}{
		{"color: #fff;", "7-11:ffffff", "7-11:ffffff"},
		{"color: #ffff;", "7-12:ffffff", "7-12:ffffff"},
		{"color: #ff0000;", "7-14:ff0000", "7-14:ff0000"},
		// #RRGGBBAA を認めない既定では、#ff0000 の後に 8 が続くので、どの形とも認めない
		{"color: #ff000080;", "", "7-16:800000"},
		{"#abcde x", "", ""},
		{"a#fff", "", ""},
		{"#fffg", "", ""},
		{"#FF00ff", "0-7:ff00ff", "0-7:ff00ff"},
		{"#f008 #0f0f", "0-5:880000 6-11:00ff00", "0-5:880000 6-11:00ff00"},
		{"#fff#000", "0-4:ffffff", "0-4:ffffff"},
		{`"#123" '#abc'`, "1-5:112233 8-12:aabbcc", "1-5:112233 8-12:aabbcc"},
		// 名前を認識するときは、- も英字や数字と同じく区切りにならない
		{"#fff-)", "", ""},
		{"x -#fff", "", ""},
		{"rgb(255, 0, 0)", "", "0-14:ff0000"},
		{"rgba(255, 0, 0, 0.5)", "", "0-20:ff0000"},
		{"rgb(255 0 0 / 50%)", "", "0-18:ff0000"},
		{"rgb(50%, 50%, 50%)", "", "0-18:7f7f7f"},
		{"rgb(50%, 50, 50)", "", ""},
		{"rgb(1,2 3)", "", ""},
		{"rgba(1 2 3 0.5)", "", ""},
		{"rgb(255, 200,)", "", ""},
		{"rgb(ff0000)", "", "0-11:ff0000"},
		{"rgba(ff000080)", "", "0-14:800000"},
		{"xrgb(1,2,3)", "", "1-11:010203"},
		{"rgb(300, 0, 0)", "", "0-14:ff0000"},
		{"hsl(120, 50%, 50%)", "", "0-18:3fbf3f"},
		{"hsl(400, 100%, 50%)", "", "0-19:ffaa00"},
		{"hsl(0.25turn, 100%, 50%)", "", "0-24:7fff00"},
		{"hsl(90deg 100% 50%)", "", "0-19:7fff00"},
		{"hsla(120 100% 50% / 0.3)", "", "0-24:00ff00"},
		{"0xFFF 0xFF0000 0x80FF0000 0xF00F 0X123", "", "0-5:ffffff 6-14:ff0000 15-25:800000 33-38:112233"},
		{"red blue Red RED LightBlue lightblue light-blue", "0-3:ff0000 4-8:0000ff 9-12:ff0000 17-26:add8e6 27-36:add8e6", "0-3:ff0000 4-8:0000ff 9-12:ff0000 17-26:add8e6 27-36:add8e6"},
		{"redish xred red1 Blue3 NvimDarkGrey1 nvimdarkgrey1", "12-16:ff0000 17-22:0000cd 23-36:07080d 37-50:07080d", "12-16:ff0000 17-22:0000cd 23-36:07080d 37-50:07080d"},
		{"text-red-500 red_x x-red", "13-16:ff0000", "13-16:ff0000"},
		{"  white\tblack", "2-7:ffffff 8-13:000000", "2-7:ffffff 8-13:000000"},
		{"é red", "3-6:ff0000", "3-6:ff0000"},
		{"", "", ""},
		{"#", "", ""},
	}
	def, all := newScanner(defaultFormats()), newScanner(allFormats())
	for _, c := range cases {
		if got := spansString(def.scanLine(c.line)); got != c.def {
			t.Errorf("既定 %q = %q, want %q", c.line, got, c.def)
		}
		if got := spansString(all.scanLine(c.line)); got != c.all {
			t.Errorf("すべての形 %q = %q, want %q", c.line, got, c.all)
		}
	}
}

// TestScanNamesOptions は、色の名前の綴りの設定を確かめる。
func TestScanNamesOptions(t *testing.T) {
	upper := defaultFormats()
	upper.namesUpper = true
	if got := spansString(newScanner(upper).scanLine("RED LIGHTBLUE")); got != "0-3:ff0000 4-13:add8e6" {
		t.Errorf("大文字の綴り = %q", got)
	}
	none := defaultFormats()
	none.namesLower, none.namesCamel = false, false
	s := newScanner(none)
	if got := spansString(s.scanLine("red Blue -#fff")); got != "10-14:ffffff" {
		// 名前を認識しないときは、- は # の前の区切りになる
		t.Errorf("名前なし = %q", got)
	}
}

// TestNameTable は、色の名前の表を確かめる。nvim-colorizer.lua は nvim_get_color_map() の 707 個の名前を使う（Neovim 0.12.5）。
func TestNameTable(t *testing.T) {
	if len(colorNames) != 707 {
		t.Errorf("len(colorNames) = %d, want 707", len(colorNames))
	}
	for i, n := range colorNames {
		if i > 0 && colorNames[i-1].name >= n.name {
			t.Fatalf("名前の順でない: %q, %q", colorNames[i-1].name, n.name)
		}
		// scanner の nameAt は、名前が英字と数字だけからなることを前提にする
		for k := range len(n.name) {
			if !isAlnum(n.name[k]) {
				t.Errorf("名前 %q に英字と数字でない文字がある", n.name)
			}
		}
	}
}

// BenchmarkScanLine は、CSS のような 1 行を数える時間を測る。
func BenchmarkScanLine(b *testing.B) {
	s := newScanner(allFormats())
	line := "  .button { color: #ff0000; background: rgba(0, 0, 0, 0.5); border: 1px solid darkgray; } /* hover */"
	b.SetBytes(int64(len(line)))
	for b.Loop() {
		s.scanLine(line)
	}
}
