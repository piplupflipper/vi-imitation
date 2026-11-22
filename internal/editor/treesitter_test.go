package editor

import (
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
)

// 実物の vi-imitation-ext-treesitter と宣言ファイルで、構文のまとまりの an と in を、ビジュアルモードでの繰り返し、回数、
// オペレーターの後、ドットリピート、マクロから使う（#373）。
//
// req: FR-240
func TestTreesitterBlockObject(t *testing.T) {
	if testing.Short() {
		t.Skip("拡張機能のビルドが要る")
	}
	spec := treesitterExtension(t)
	const src = "package main\n\nfunc f(a int) int {\n\tif a > 0 {\n\t\ta++\n\t}\n\treturn a\n}\n"
	const ifStmt = "\tif a > 0 {\n\t\ta++\n\t}"
	const fn = "func f(a int) int {\n" + ifStmt + "\n\treturn a\n}"
	const text = "package main\n\n" + fn // 変えていないバッファの Text（末尾の改行を含めない）
	tests := []struct {
		name  string
		keys  string
		macro string // 空でなければ、keys の前にレジスター q に入れる
		text  string // 打った後のバッファ
		reg   string // 打った後のレジスター a
	}{
		{name: "van は if の全体", keys: `5Gvan"ay`, text: text, reg: ifStmt},
		{name: "vanan は外へ広げて関数の全体", keys: `5Gvanan"ay`, text: text, reg: fn},
		{name: "v2an は回数で関数の全体", keys: `5Gv2an"ay`, text: text, reg: fn},
		{name: "van2an は広げる先が足りなければ失敗して選択を変えない", keys: `5Gvan2an"ay`, text: text, reg: ifStmt},
		{name: "vinin は外側の中身", keys: `5Gvinin"ay`, text: text, reg: ifStmt + "\n\treturn a"},
		{name: "din は中身の行を消す", keys: "5Gdin", text: "package main\n\nfunc f(a int) int {\n\tif a > 0 {\n\t}\n\treturn a\n}"},
		{name: "dan の後の . は外側を消す", keys: "5Gdan.", text: "package main\n"},
		{name: "マクロの中の vanand", macro: "vanand", keys: "5G@q", text: "package main\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeFiles(t, map[string]string{"a.go": src})
			h := start(t, Config{WorkDir: dir, Extensions: []exthost.Spec{spec}}, "a.go")
			if tt.macro != "" {
				h.e.do(func() { h.e.cur.state.Registers().Set('q', command.Register{Text: tt.macro}) })
			}
			h.keys(tt.keys)
			h.settle()
			if got := h.text(); got != tt.text {
				t.Errorf("text = %q, want %q (message %q)", got, tt.text, h.message())
			}
			var reg command.Register
			h.e.do(func() { reg, _ = h.e.cur.state.Registers().Get('a') })
			if reg.Text != tt.reg {
				t.Errorf("register a = %q, want %q", reg.Text, tt.reg)
			}
		})
	}
}
