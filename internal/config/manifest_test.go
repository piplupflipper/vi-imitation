package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
)

// writeManifest は、dir/name/manifest.json に src を書き、そのパスを返す。
func writeManifest(t *testing.T, dir, name, src string) string {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, ManifestFileName)
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// surroundManifest は、基本設計 5.1 の例の宣言ファイルである。
const surroundManifest = `{
  // 拡張機能の ID。ドットリピートの記録と設定で使う
  "id": "surround",
  "command": "./vi-imitation-ext-surround",
  // 起動の契機。どれか 1 つが成り立った時点で起動する
  "activation": [
    { "on": "operator", "id": "surround.add" },
    { "on": "filetype", "pattern": "markdown" },
  ],
  // 追加する操作。キーとの対応は利用者の設定で上書きできる
  "contributes": {
    "operators": [{ "id": "surround.add", "keys": "ys" }],
    "textObjects": [{ "id": "surround.inner", "keys": "is" }],
  },
}
`

// コメントと末尾のカンマを含む宣言ファイルを読み、検査を通す。
//
// req: FR-40
func TestLoadManifestJSONC(t *testing.T) {
	p := writeManifest(t, t.TempDir(), "surround", surroundManifest)
	m, warns, err := LoadManifest(p)
	if err != nil || len(warns) != 0 {
		t.Fatalf("LoadManifest: warns = %v, err = %v", warns, err)
	}
	want := ext.Manifest{
		ID:      "surround",
		Command: "./vi-imitation-ext-surround",
		Activation: []ext.Activation{
			{On: ext.ActivationOperator, ID: "surround.add"},
			{On: ext.ActivationFileType, Pattern: "markdown"},
		},
		Contributes: ext.Contributes{
			Operators:   []ext.Contribution{{ID: "surround.add", Keys: "ys"}},
			TextObjects: []ext.Contribution{{ID: "surround.inner", Keys: "is"}},
		},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("Manifest = %+v, want %+v", m, want)
	}
}

// provides を読み、activation の無い宣言ファイルも提供者として通す。未知の操作は警告にする。
//
// req: FR-94
func TestLoadManifestProvides(t *testing.T) {
	p := writeManifest(t, t.TempDir(), "dap", `{
  "id": "dap",
  "command": "./vi-imitation-ext-dap",
  // 本体の操作のうち、この拡張機能が引き受けるもの
  "provides": ["debug.continue", "breakpoint.toggle", "debug.future"],
}`)
	m, warns, err := LoadManifest(p)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	want := []ext.Operation{ext.OpDebugContinue, ext.OpBreakpointToggle, "debug.future"}
	if !reflect.DeepEqual(m.Provides, want) {
		t.Errorf("Provides = %v, want %v", m.Provides, want)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Error(), `"debug.future" は未知の操作`) {
		t.Errorf("warnings = %v", warns)
	}
	if !m.ProvidesOp(ext.OpDebugContinue) || m.ProvidesOp("debug.future") || m.ProvidesOp(ext.OpHover) {
		t.Errorf("ProvidesOp が provides と食い違う")
	}
}

// リポジトリの標準の拡張機能の宣言ファイルは、すべて読めて検査を通る。
func TestLoadManifestRepository(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "cmd", "*", ManifestFileName))
	if err != nil || len(paths) == 0 {
		t.Fatalf("宣言ファイルが見つからない: %v", err)
	}
	for _, p := range paths {
		if _, warns, err := LoadManifest(p); err != nil || len(warns) != 0 {
			t.Errorf("%s: warns = %v, err = %v", p, warns, err)
		}
	}
}

func TestLoadManifestErrors(t *testing.T) {
	cases := []struct {
		name, src string
		// line と col は誤りの位置で、line が 0 なら位置を問わない。型の違いは値の先頭を指す。
		line, col int
		msg       string
	}{
		{"構文", "{\n  \"id\": \"a\"\n  \"command\": \"x\"\n}", 3, 3, "JSONC として読めない"},
		{"最上位が配列", "[]", 1, 1, "最上位はオブジェクト"},
		{"型の違い", "{\n  \"id\": \"a\",\n  \"command\": 1,\n  \"activation\": []\n}", 3, 14, "command の型が違う"},
		{"文字列の型の違い", "{\n  \"id\": \"a\",\n  \"saveHook\": \"yes-please\",\n}", 3, 15, "saveHook の型が違う"},
		{"複数行の値の型の違い", "{\n  \"id\": \"a\",\n  \"args\": {\n    \"x\": 1\n  }\n}", 3, 11, "args の型が違う"},
		{"入れ子の型の違い", "{\n  \"activation\": [\n    { \"on\": 1 },\n  ]\n}", 3, 13, "activation.0.on の型が違う"},
		{"BOM の後の型の違い", "\xef\xbb\xbf{\"id\": 1}", 1, 8, "id の型が違う"},
		{"検査の誤り", `{"id": "a", "command": "x", "activation": []}`, 0, 0, "宣言ファイルの誤り"},
		{"空のファイル", "", 1, 1, "JSONC として読めない"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeManifest(t, t.TempDir(), "a", c.src)
			_, _, err := LoadManifest(p)
			var pr *Error
			if !errors.As(err, &pr) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if (c.line != 0 && (pr.Line != c.line || pr.Column != c.col)) || !strings.Contains(pr.Msg, c.msg) || pr.Path != p {
				t.Errorf("err = %v, want %d:%d に %q", err, c.line, c.col, c.msg)
			}
		})
	}
}

// 未知の項目と未知の契機は、誤りにせず警告にする。
func TestLoadManifestWarnings(t *testing.T) {
	src := `{
  "id": "a",
  "command": "x",
  "future": true,
  "activation": [{ "on": "idle" }, { "on": "command", "id": "a.run" }],
  "contributes": { "commands": [{ "id": "a.run" }] }
}`
	p := writeManifest(t, t.TempDir(), "a", src)
	m, warns, err := LoadManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 2 || !strings.HasPrefix(warns[0].Error(), p+":4:3: 未知の項目 future") || !strings.Contains(warns[1].Error(), "idle") {
		t.Errorf("warns = %v", warns)
	}
	if m.ID != "a" {
		t.Errorf("ID = %q", m.ID)
	}
}

func TestLoadExtensions(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeManifest(t, first, "lsp", `{"id": "lsp", "command": "./lsp", "activation": [{"on": "filetype", "pattern": "go"}],}`)
	writeManifest(t, first, "broken", `{"id": "broken", "command": "x", "activation": []}`)
	if err := os.MkdirAll(filepath.Join(first, "no-manifest"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dup := writeManifest(t, second, "lsp2", `{"id": "lsp", "command": "./other", "activation": [{"on": "filetype", "pattern": "c"}]}`)
	writeManifest(t, second, "acp", `{"id": "acp", "command": "/usr/bin/acp", "activation": [{"on": "command", "id": "acp.ask"}], "contributes": {"commands": [{"id": "acp.ask"}]}}`)

	cfg, errs := Parse("config.jsonc", []byte(`{
  "extensions": {
    "settings": { "lsp": { "servers": [] }, "nothing": {} },
    "activation": { "lsp": [{ "on": "filetype", "pattern": "python" }] },
  },
}`))
	if len(errs) != 0 {
		t.Fatal(errs)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	exts, problems := LoadExtensions([]string{first, missing, second}, cfg)

	if len(exts) != 2 {
		t.Fatalf("exts = %+v", exts)
	}
	lsp, acp := exts[0], exts[1]
	if lsp.Manifest.ID != "lsp" || lsp.Dir != filepath.Join(first, "lsp") || string(lsp.Settings) != `{"servers":[]}` {
		t.Errorf("lsp = %+v", lsp)
	}
	wantAct := []ext.Activation{{On: ext.ActivationFileType, Pattern: "go"}, {On: ext.ActivationFileType, Pattern: "python"}}
	if !reflect.DeepEqual(lsp.Manifest.Activation, wantAct) {
		t.Errorf("lsp の契機 = %+v, want %+v", lsp.Manifest.Activation, wantAct)
	}
	if acp.Manifest.ID != "acp" || acp.Dir != filepath.Join(second, "acp") || acp.Settings != nil {
		t.Errorf("acp = %+v", acp)
	}

	// broken の検査の誤り、lsp2 の ID の重なり、設定にある見つからない ID の 3 件。
	ps := problemsOf(t, problems)
	if len(ps) != 3 {
		t.Fatalf("problems = %v", problems)
	}
	if !strings.Contains(ps[0].Path, "broken") || ps[1].Path != dup || !strings.Contains(ps[1].Msg, "重なる") {
		t.Errorf("problems = %v", problems)
	}
	if ps[2].Path != "config.jsonc" || ps[2].Line != 3 || ps[2].Column != 45 || !strings.Contains(ps[2].Msg, "nothing") {
		t.Errorf("見つからない ID の誤り = %v", ps[2])
	}
}

// extensions.disabled に ID を書いた拡張機能は、利用者のディレクトリのものも同梱のものも一覧に入れない。
// 見つからない ID と、文字列でない要素と空の ID は、設定の誤りとして知らせる。
//
// req: FR-226
func TestLoadExtensionsDisabled(t *testing.T) {
	user, bundled := t.TempDir(), t.TempDir()
	writeManifest(t, user, "res", `{"id": "res", "command": "./res", "activation": [{"on": "startup"}]}`)
	writeManifest(t, bundled, "res", `{"id": "res", "command": "./res", "activation": [{"on": "startup"}]}`)
	writeManifest(t, bundled, "mem", `{"id": "mem", "command": "./mem", "contributes": {"statusline": [{"id": "mem.rss"}]}}`)
	writeManifest(t, bundled, "lsp", `{"id": "lsp", "command": "./lsp", "activation": [{"on": "filetype", "pattern": "go"}]}`)

	cfg, errs := Parse("config.jsonc", []byte(`{
  "extensions": { "disabled": ["res", "mem", "res", "nothing", 3, ""] },
}`))
	if len(errs) != 2 || !strings.Contains(errs[0].Error(), "extensions.disabled の要素 は文字列にする") ||
		!strings.Contains(errs[1].Error(), "空の ID") {
		t.Fatalf("Parse の誤り = %v", errs)
	}
	if want := map[string]bool{"res": true, "mem": true, "nothing": true}; !reflect.DeepEqual(cfg.ExtensionDisabled, want) {
		t.Errorf("ExtensionDisabled = %v, want %v", cfg.ExtensionDisabled, want)
	}

	exts, problems := LoadExtensions([]string{user, bundled}, cfg)
	if len(exts) != 1 || exts[0].Manifest.ID != "lsp" {
		t.Fatalf("exts = %+v", exts)
	}
	// 同梱の res の ID の重なり（利用者のものを使うときと同じ）と、見つからない ID の 2 件。
	ps := problemsOf(t, problems)
	if len(ps) != 2 || !strings.Contains(ps[0].Msg, "重なる") || ps[1].Line != 2 || !strings.Contains(ps[1].Msg, "extensions.disabled の拡張機能 nothing") {
		t.Errorf("problems = %v", problems)
	}
}

// 宣言ファイルの settingsFileType の契機は、その拡張機能に向けた extensions.settings の中のファイルの種類から filetype の契機を作る。
// 利用者は、設定に書いたファイルの種類を extensions.activation にもう一度書かずに済む（#277）。
func TestLoadExtensionsSettingsFileType(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "treesitter", `{"id": "treesitter", "command": "./ts", "activation": [
  {"on": "filetype", "pattern": "go"},
  {"on": "settingsFileType", "pattern": "/parsers/*/fileTypes"},
]}`)
	writeManifest(t, dir, "plain", `{"id": "plain", "command": "./plain", "activation": [{"on": "settingsFileType", "pattern": "/fileType"}]}`)
	writeManifest(t, dir, "nosettings", `{"id": "nosettings", "command": "./n", "activation": [{"on": "settingsFileType", "pattern": "/a"}]}`)

	cfg, errs := Parse("config.jsonc", []byte(`{
  "extensions": {
    "settings": {
      "treesitter": { "parsers": {
        "rust": { "fileTypes": ["rust"] },
        "python": { "fileTypes": ["python", "", 3, "python"] },
        "broken": { "fileTypes": "notalist" },
        "nofield": {},
        "empty": { "fileTypes": [] },
        "numbers": { "fileTypes": [3] },
      } },
      "plain": { "fileType": "toml" },
    },
    "activation": { "treesitter": [{ "on": "filetype", "pattern": "lua" }] },
  },
}`))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	exts, problems := LoadExtensions([]string{dir}, cfg)
	// "*" が当たった nofield に fileTypes が無いこと、empty と numbers の fileTypes が文字列を 1 つも含まないことを、
	// 設定の treesitter の位置で、項目の名前の順に知らせる（綴りや値の誤りで起動しないことに気づけるように）。
	ps := problemsOf(t, problems)
	if len(ps) != 3 {
		t.Fatalf("problems = %v", problems)
	}
	for i, name := range []string{"empty", "nofield", "numbers"} {
		if ps[i].Path != "config.jsonc" || ps[i].Line != 4 ||
			!strings.Contains(ps[i].Msg, "extensions.settings.treesitter.parsers."+name+".fileTypes が無いか、文字列か文字列を含む配列でない") {
			t.Errorf("problems[%d] = %v", i, ps[i])
		}
	}
	got := map[string][]ext.Activation{}
	for _, e := range exts {
		got[e.Manifest.ID] = e.Manifest.Activation
	}
	settings := ext.Activation{On: ext.ActivationSettingsFileType, Pattern: "/parsers/*/fileTypes"}
	want := map[string][]ext.Activation{
		// 項目の名前の順（broken、empty、nofield、numbers、python、rust）に、文字列だけを 1 回ずつ足す。指した値が文字列（broken）なら、その 1 つを足す。
		"treesitter": {
			{On: ext.ActivationFileType, Pattern: "go"},
			settings,
			{On: ext.ActivationFileType, Pattern: "notalist"},
			{On: ext.ActivationFileType, Pattern: "python"},
			{On: ext.ActivationFileType, Pattern: "rust"},
			{On: ext.ActivationFileType, Pattern: "lua"},
		},
		"plain": {
			{On: ext.ActivationSettingsFileType, Pattern: "/fileType"},
			{On: ext.ActivationFileType, Pattern: "toml"},
		},
		// 設定が無ければ足さず、元の契機だけが残る（activation が空にならない）。
		"nosettings": {{On: ext.ActivationSettingsFileType, Pattern: "/a"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("契機 = %+v, want %+v", got, want)
	}
}

// pointerStrings は、JSON Pointer の参照トークンの ~0 と ~1 を戻し、配列の添字と "*" を辿る。
func TestPointerStrings(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(`{"a/b": {"m~n": ["x"]}, "list": [{"t": "p"}, {"t": "q"}], "n": 1}`), &v); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		ptr  string
		want []string
	}{
		{"/a~1b/m~0n", []string{"x"}},
		{"/list/*/t", []string{"p", "q"}},
		{"/list/1/t", []string{"q"}},
		{"/list/01/t", nil},
		{"/list/9/t", nil},
		{"/n", nil},
		{"/missing", nil},
	} {
		if got := pointerStrings(v, tt.ptr); !slices.Equal(got, tt.want) {
			t.Errorf("pointerStrings(%q) = %q, want %q", tt.ptr, got, tt.want)
		}
	}
}

// Extension の項目は exthost.Spec と同じ並びと型にする（呼び出し側が型の変換で写せるように）。
// exthost は同じ層なので import できず、ここでは項目の名前と型を固定して確かめる。
func TestExtensionFields(t *testing.T) {
	typ := reflect.TypeFor[Extension]()
	want := []struct {
		name string
		typ  reflect.Type
	}{
		{"Manifest", reflect.TypeFor[ext.Manifest]()},
		{"Dir", reflect.TypeFor[string]()},
		{"Settings", reflect.TypeFor[json.RawMessage]()},
	}
	if typ.NumField() != len(want) {
		t.Fatalf("項目の数 = %d", typ.NumField())
	}
	for i, w := range want {
		f := typ.Field(i)
		if f.Name != w.name || f.Type != w.typ {
			t.Errorf("項目 %d = %s %v, want %s %v", i, f.Name, f.Type, w.name, w.typ)
		}
	}
}

// 標準の拡張機能の宣言ファイルは、警告なく読め、contributes の各項目に説明文（description）を持つ（基本設計 5.1）。
// 説明文はキーの一覧とヘルプに出る。
//
// req: FR-90
func TestStandardManifestDescriptions(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "cmd", "vi-imitation-ext-*", ManifestFileName))
	if err != nil || len(paths) == 0 {
		t.Fatalf("Glob = %v, %v", paths, err)
	}
	for _, p := range paths {
		m, warns, err := LoadManifest(p)
		if err != nil || len(warns) > 0 {
			t.Errorf("%s: %v %v", p, warns, err)
			continue
		}
		c := m.Contributes
		for _, x := range slices.Concat(c.Operators, c.Motions, c.TextObjects, c.Commands) {
			if x.Description == "" {
				t.Errorf("%s: %s has no description", p, x.ID)
			}
		}
	}
}
