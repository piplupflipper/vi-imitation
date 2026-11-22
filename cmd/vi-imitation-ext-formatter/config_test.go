package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	userconfig "github.com/piplupflipper/vi-imitation/internal/config"
)

// 設定の formatters は、ファイルの種類ごとに、書いた順に整形ツールを選ぶ。省いた項目は既定値になる。
//
// req: FR-175
func TestParseSettings(t *testing.T) {
	cfg, err := parseSettings(nil)
	if err != nil || len(cfg.formatters) != 0 || !cfg.formatOnSave || cfg.timeout != defaultTimeout || !reflect.DeepEqual(cfg.localBinDirs, defaultLocalBinDirs) {
		t.Fatalf("parseSettings(nil) = %+v, %v", cfg, err)
	}

	cfg, err = parseSettings(json.RawMessage(`{
		"formatters": [
			{ "command": "biome", "args": ["check", "--write", "--stdin-file-path", "{path}"], "fileTypes": ["json", "typescript"] },
			{ "name": "imports", "command": "/opt/bin/isort", "fileTypes": ["python"] },
			{ "command": "~/bin/ruff", "args": ["format", "-"], "fileTypes": ["python"] }
		],
		"formatOnSave": false,
		"localBinDirs": [],
		"timeoutMs": 1500
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.formatOnSave || cfg.timeout != 1500*time.Millisecond || cfg.localBinDirs == nil || len(cfg.localBinDirs) != 0 {
		t.Errorf("cfg = %+v", cfg)
	}
	names := func(ft string) []string {
		var out []string
		for _, f := range cfg.forFileType(ft) {
			out = append(out, f.Name)
		}
		return out
	}
	for ft, want := range map[string][]string{"python": {"imports", "ruff"}, "json": {"biome"}, "go": nil, "": nil} {
		if got := names(ft); !reflect.DeepEqual(got, want) {
			t.Errorf("forFileType(%q) = %v, want %v", ft, got, want)
		}
	}
}

// 同梱する設定の例（config.example.jsonc）の formatter の部分は、この拡張機能の設定として誤り無く読め、
// オーナーの nvim の conform.nvim の表（#339 の本文）のファイルの種類に整形ツールを当てる。
// 同梱の宣言ファイルと合わせて読むと、formatter の拡張機能は lsp より先に並び（ディレクトリの名前の順）、
// 設定に書いたファイルの種類（python）でだけ format の提供者の候補になる。Go は LSP の拡張機能が整形する。
//
// req: FR-175, FR-176
func TestConfigExample(t *testing.T) {
	data, err := os.ReadFile("../../config.example.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	ucfg, errs := userconfig.Parse("config.example.jsonc", data)
	if len(errs) != 0 {
		t.Fatalf("設定の例の誤り: %v", errs)
	}
	if ucfg.WillSaveTimeout != 2*time.Second {
		t.Errorf("willSaveTimeoutMs = %v, want 2s（conform.nvim の timeout_ms）", ucfg.WillSaveTimeout)
	}
	cfg, err := parseSettings(ucfg.ExtensionSettings["formatter"])
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"json": "biome", "jsonc": "biome", "typescript": "biome", "typescriptreact": "biome", "javascript": "biome",
		"javascriptreact": "biome", "css": "biome", "graphql": "biome", "toml": "taplo", "python": "ruff", "lua": "stylua",
		"sh": "shfmt", "bash": "shfmt", "fish": "fish_indent", "c": "clang-format", "cpp": "clang-format", "rust": "rustfmt",
	}
	for ft, name := range want {
		fs := cfg.forFileType(ft)
		if len(fs) != 1 || fs[0].Name != name {
			t.Errorf("%s の整形ツール = %+v, want %s", ft, fs, name)
		}
	}
	if !cfg.formatOnSave {
		t.Error("formatOnSave = false, want 保存の前に整形する")
	}

	// 同梱の宣言ファイルを、配布物と同じ extensions/<名前>/ の並びに置いて読む。
	// 設定の例は treesitter の設定（見出しの帯。#380）と colorizer の設定（色コードの着色。#388）も持つので、
	// その宣言ファイルも置く（置かないと、設定の先の拡張機能が無い誤りになる）。
	dir := t.TempDir()
	for name, src := range map[string]string{
		"colorizer": "../vi-imitation-ext-colorizer", "formatter": ".", "lsp": "../vi-imitation-ext-lsp", "treesitter": "../vi-imitation-ext-treesitter",
	} {
		m, err := os.ReadFile(filepath.Join(src, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name, "manifest.json"), m, 0o600); err != nil { //nolint:gosec // G703: テストの一時ディレクトリの中に、テストが決めた名前で書く
			t.Fatal(err)
		}
	}
	exts, problems := userconfig.LoadExtensions([]string{dir}, ucfg)
	if len(problems) != 0 {
		t.Fatalf("宣言ファイルの誤り: %v", problems)
	}
	if len(exts) != 4 || exts[0].Manifest.ID != "colorizer" || exts[1].Manifest.ID != "formatter" || exts[2].Manifest.ID != "lsp" ||
		exts[3].Manifest.ID != "treesitter" {
		t.Fatalf("拡張機能の並び = %+v, want colorizer, formatter, lsp, treesitter", exts)
	}
	formatter, lsp := exts[1].Manifest, exts[2].Manifest
	for ft := range want {
		if !formatter.HandlesFileType(ft) || !formatter.ProvidesOp(ext.OpFormat) {
			t.Errorf("formatter が %s の format の提供者の候補にならない", ft)
		}
	}
	if formatter.HandlesFileType("go") || !lsp.HandlesFileType("go") || !lsp.ProvidesOp(ext.OpFormat) {
		t.Errorf("go の format の候補: formatter %v, lsp %v, want lsp だけ", formatter.HandlesFileType("go"), lsp.HandlesFileType("go"))
	}
}

// 設定の誤り（知らない項目、空の command、パスの形の誤り、空の fileTypes、絶対パスの localBinDirs、負の timeoutMs）は、
// すべてまとめて 1 つの誤りにし、initialize を失敗させる。
func TestParseSettingsErrors(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{`{"formatter": []}`, `unknown field "formatter"`},
		{`{"formatters": [{"command": "x", "fileType": ["go"]}]}`, `unknown field "fileType"`},
		{`{"formatters": [{"fileTypes": ["go"]}]}`, "formatters[0] の command が空"},
		{`{"formatters": [{"command": "bin/x", "fileTypes": ["go"]}]}`, `command "bin/x" は、名前か絶対パス`},
		{`{"formatters": [{"command": "x"}]}`, "formatters[0] の fileTypes に"},
		{`{"formatters": [{"command": "x", "fileTypes": [""]}]}`, "formatters[0] の fileTypes に"},
		{`{"localBinDirs": ["/abs"]}`, `localBinDirs の "/abs"`},
		{`{"timeoutMs": -1}`, "timeoutMs は 0 以上"},
	} {
		if _, err := parseSettings(json.RawMessage(c.src)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q を含む誤り", c.src, err, c.want)
		}
	}
}
