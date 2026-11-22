package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeColorScheme は、設定のディレクトリ dir の colors の下に、名前 name の配色のファイルを置く。
func writeColorScheme(t *testing.T, dir, name, text string) {
	t.Helper()
	d := filepath.Join(dir, ColorSchemeDir)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, name+colorSchemeExt), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// colorscheme の名前は英数字と _ と - だけで、設定ディレクトリの外を指す名前（"/" と ".." を含むもの）は位置付きの誤りにして使わない。
//
// req: FR-125
func TestParseColorSchemeName(t *testing.T) {
	cfg, errs := Parse("config.jsonc", []byte(`{ "colorscheme": "my-scheme_2" }`))
	if len(errs) != 0 || cfg.ColorScheme != "my-scheme_2" {
		t.Errorf("ColorScheme = %q, 誤り %v", cfg.ColorScheme, errs)
	}
	for _, name := range []string{`"../iceberg"`, `"a/b"`, `"iceberg.jsonc"`, `""`, `"/etc/passwd"`, `1`} {
		cfg, errs := Parse("config.jsonc", []byte(`{ "colorscheme": `+name+` }`))
		if cfg.ColorScheme != "" {
			t.Errorf("%s: ColorScheme = %q", name, cfg.ColorScheme)
		}
		wantErrors(t, errs, []struct {
			line, col int
			msg       string
		}{{1, 18, "colorscheme"}})
	}
}

// 配色は設定ディレクトリの colors/<名前>.jsonc を先に探し、無ければ同梱の配色を使う。同じ名前なら利用者のファイルが勝つ。
// colorscheme を書かなければ配色を読まない。同梱の iceberg のヤンクの色は、選択と見分けられる背景の色を持つ。
//
// req: FR-125, FR-140
func TestLoadColorScheme(t *testing.T) {
	dir := t.TempDir()
	writeColorScheme(t, dir, "iceberg", `// 利用者の iceberg
{ "keyword": { "fg": 1 } }`)
	writeColorScheme(t, dir, "mine", `{ "comment": { "fg": "#010203", "italic": true }, }`)

	load := func(name, d string) Config {
		t.Helper()
		cfg, errs := Parse("config.jsonc", []byte(`{ "colorscheme": "`+name+`" }`))
		errs = append(errs, cfg.LoadColorScheme(d, AppearanceCheck{})...)
		if len(errs) != 0 {
			t.Fatalf("%s の誤り: %v", name, errs)
		}
		return cfg
	}
	if got := load("iceberg", dir).SchemeColors; len(got) != 1 || got["keyword"] != (Style{Fg: Color{Kind: ColorIndexed, Index: 1}}) {
		t.Errorf("利用者の iceberg = %+v", got)
	}
	if got := load("mine", dir).SchemeColors; got["comment"] != (Style{Fg: Color{Kind: ColorRGB, R: 1, G: 2, B: 3}, Italic: true}) {
		t.Errorf("mine = %+v", got)
	}
	// 利用者のファイルが無ければ同梱の iceberg を使う。設定ディレクトリが決まらないとき（空）も同じ。
	for _, d := range []string{t.TempDir(), ""} {
		got := load("iceberg", d).SchemeColors
		if got["keyword"] != (Style{Fg: Color{Kind: ColorRGB, R: 0x84, G: 0xa0, B: 0xc6}}) || len(got) < 10 {
			t.Errorf("同梱の iceberg（dir %q）= %+v", d, got)
		}
	}

	// 同梱の iceberg のヤンクの色は、反転でなく背景の色を持つ。選択（背景の色だけ）と重なったところで、選択の背景が上に出る
	// （PR #287 のレビューの推奨 1。FR-140）。
	if got := load("iceberg", "").SchemeColors; got["yank"].Reverse || got["yank"].Bg.Kind != ColorRGB || got["yank"].Bg == got["visual"].Bg {
		t.Errorf("同梱の iceberg の yank = %+v、visual = %+v", got["yank"], got["visual"])
	}
	// 同梱の iceberg は帯の色（band、band.linenr）を書かない。書くと、設定の colors で popup や linenr を変えても帯が付いてこない
	// （本体は書いていない帯の色を popup と linenr から引き継ぐ。PR #376 の再レビューの推奨）。
	for _, name := range []string{"band", "band.linenr"} {
		if s, ok := load("iceberg", "").SchemeColors[name]; ok {
			t.Errorf("同梱の iceberg が %s を書いている: %+v", name, s)
		}
	}

	// 同梱の iceberg のインレイヒントの色は、nontext（#242940）を引き継がず、オーナーの nvim の LspInlayHint の薄紫にする（#387。FR-258）。
	if got := load("iceberg", "").SchemeColors["inlayHint"]; got != (Style{Fg: Color{Kind: ColorRGB, R: 0x8b, G: 0x80, B: 0xa8}}) {
		t.Errorf("同梱の iceberg の inlayHint = %+v", got)
	}

	var cfg Config
	if errs := cfg.LoadColorScheme(dir, AppearanceCheck{}); errs != nil || cfg.SchemeColors != nil {
		t.Errorf("colorscheme なし: %v, %v", errs, cfg.SchemeColors)
	}
}

// 見つからない名前は、設定ファイルの colorscheme の位置の誤りにして配色を使わない。
// 配色のファイルの中の誤り（読めない色、本体の知らない名前）は、配色のファイルの位置の誤りにして、その名前だけを使わない。
//
// req: FR-125
func TestLoadColorSchemeErrors(t *testing.T) {
	dir := t.TempDir()
	cfg, errs := Parse("config.jsonc", []byte("{\n  \"colorscheme\": \"nothing\"\n}"))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	errs = cfg.LoadColorScheme(dir, AppearanceCheck{})
	wantErrors(t, errs, []struct {
		line, col int
		msg       string
	}{{2, 18, "E185: 配色 nothing が見つからない"}})
	if cfg.SchemeColors != nil {
		t.Errorf("SchemeColors = %v", cfg.SchemeColors)
	}

	writeColorScheme(t, dir, "bad", `{
  "keyword": { "fg": 1 },
  "keywrod": { "fg": 2 },
  "string": { "fg": "#12345" },
  "diagnostic.error": { "blink": true, "sp": 3 }
}`)
	cfg = Config{ColorScheme: "bad"}
	errs = cfg.LoadColorScheme(dir, AppearanceCheck{Color: func(n string) bool { return n != "keywrod" }})
	wantErrors(t, errs, []struct {
		line, col int
		msg       string
	}{
		{3, 3, "配色 bad の keywrod は未知の色の名前"},
		{4, 21, `string.fg は "#rrggbb"`},
		{5, 25, `未知の項目 ["diagnostic.error"].blink`},
	})
	var p *Error
	if !errors.As(errs[0], &p) || filepath.Base(p.Path) != "bad.jsonc" {
		t.Errorf("誤りのパス = %v", errs[0])
	}
	names := make([]string, 0, len(cfg.SchemeColors))
	for n := range cfg.SchemeColors {
		names = append(names, n)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"diagnostic.error", "keyword"}) {
		t.Errorf("SchemeColors の名前 = %v", names)
	}

	writeColorScheme(t, dir, "array", `[]`)
	cfg = Config{ColorScheme: "array"}
	wantErrors(t, cfg.LoadColorScheme(dir, AppearanceCheck{}), []struct {
		line, col int
		msg       string
	}{{1, 1, "最上位はオブジェクト"}})
}

// 同梱の配色は、名前の順に一覧でき、どれも誤り無く読める。色の名前が本体の名前かは cmd/vi-imitation のテストで確かめる。
func TestBundledColorSchemes(t *testing.T) {
	names := BundledColorSchemes()
	if !slices.Contains(names, "iceberg") {
		t.Fatalf("BundledColorSchemes = %v", names)
	}
	for _, name := range names {
		cfg := Config{ColorScheme: name}
		if errs := cfg.LoadColorScheme("", AppearanceCheck{}); len(errs) != 0 || len(cfg.SchemeColors) == 0 {
			t.Errorf("%s: %v, %d 件", name, errs, len(cfg.SchemeColors))
		}
	}
}
