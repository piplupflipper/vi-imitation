package main

import "testing"

// TestCSSFunctions は、rgb()、rgba()、hsl()、hsla() の読み方を、CSS Color Module Level 4 の文法と値の扱い（cssfunc.go の doc）から
// 求めた範囲と色で確かめる。色は仕様の変換の式を手で計算し、8 ビットへは同じ近さなら大きい側へ丸めた。
//
// req: FR-260
func TestCSSFunctions(t *testing.T) {
	cases := []struct {
		line, want string
	}{
		// 古い書き方（コンマ）
		{"rgb(255, 0, 0)", "0-14:ff0000"},
		{"rgba(255, 0, 0, 0.5)", "0-20:ff0000"}, // 不透明度は色に掛けない
		{"rgb(1, 2, 3, 4)", "0-15:010203"},      // rgb() にも不透明度を書ける
		{"rgb(50%, 50%, 50%)", "0-18:808080"},   // 127.5 は 128 へ丸める
		{"rgb(50%, 50, 50)", ""},                // 古い書き方では数と百分率を混ぜられない
		{"rgb(none, 0, 0)", ""},                 // 古い書き方に none は無い
		{"rgb(300, -5, 1e2)", "0-17:ff0064"},    // 範囲の外は切り詰める。指数の書き方
		{"rgb(1, 2, 3", ""},
		{"rgb(1, 2, 3, 4, 5)", ""},
		// 新しい書き方（空白と /）
		{"rgb(255 0 0 / 50%)", "0-18:ff0000"},
		{"rgb(10% 20 none)", "0-16:1a1400"}, // 混ぜてよい。none は 0
		{"rgba(1 2 3 / none)", "0-18:010203"},
		{"rgb(1 2 3 4)", ""},
		{"rgb(1,2 3)", ""},
		{"rgb(1 2 3 / 4 / 5)", ""},
		// 関数の名前の前後
		{"color: rgb(0 128 0);", "7-19:008000"},
		{"xrgb(1,2,3)", ""},
		{"-rgb(1,2,3)", ""},
		{"RGB(1,2,3)", ""},
		{"rgb (1,2,3)", ""},
		{"rgb(ff0000)", ""},
		{"rgb(1 /* c */ 2 3)", ""},
		// hsl
		{"hsl(120, 100%, 50%)", "0-19:00ff00"},
		{"hsl(120, 50%, 50%)", "0-18:40bf40"},
		{"hsl(-120, 100%, 50%)", "0-20:0000ff"}, // 色相は [0, 360) に戻す
		{"hsl(120, 150%, 50%)", "0-19:00ff00"},  // 彩度を 100% に切り詰める
		{"hsl(90, 100, 50)", ""},                // 古い書き方の彩度と明度は百分率だけ
		{"hsl(0.25turn 100% 50%)", "0-22:80ff00"},
		{"hsl(90deg 100 50)", "0-17:80ff00"},
		{"hsl(200grad 100% 50%)", "0-21:00ffff"},
		{"hsl(90DEG 100% 50%)", "0-19:80ff00"},
		{"hsl(none 0% 50%)", "0-16:808080"},
		{"hsla(120 100% 50% / 0.3)", "0-24:00ff00"},
		{"hsl(1.2.3, 100%, 50%)", ""}, // 1.2 と .3 の 2 つの数になる
		{"hsl(120px, 100%, 50%)", ""},
		{"hsl(50%, 100%, 50%)", ""},
	}
	s := newScanner(allFormats())
	for _, c := range cases {
		if got := spansString(s.scanLine(c.line)); got != c.want {
			t.Errorf("%q = %q, want %q", c.line, got, c.want)
		}
	}
	// 関数の形を認識しない設定では、rgb( の位置から色の名前を試す（rgb は名前でないので何も塗らない）
	if got := spansString(newScanner(defaultFormats()).scanLine("rgb(255, 0, 0) red")); got != "15-18:ff0000" {
		t.Errorf("既定 = %q", got)
	}
}

// TestCSSFunctionLength は、maxCodeBytes を超える関数の形を認めないことを確かめる。
func TestCSSFunctionLength(t *testing.T) {
	s := newScanner(allFormats())
	pad := make([]byte, maxCodeBytes)
	for i := range pad {
		pad[i] = ' '
	}
	if got := s.scanLine("rgb(" + string(pad) + "1 2 3)"); got != nil {
		t.Errorf("長すぎる関数を認めた: %v", got)
	}
}
