package distcheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
)

// EnvDistDir は、`make dist` が配布物をまとめたディレクトリを渡す環境変数の名前である。
// 指定が無ければ、配布物を読むテストは飛ばす。
const EnvDistDir = "VI_IMITATION_DIST_DIR"

// distDir は、検査する配布物のディレクトリを返す。
// 環境変数の指定が無ければテストを飛ばす。
func distDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(EnvDistDir)
	if dir == "" {
		t.Skipf("%s が指定されていない（make dist-check で実行する）", EnvDistDir)
	}
	return dir
}

// TestDistLayout は、配布物が本体と拡張機能を別のファイルとして含み、
// 拡張機能ごとに宣言ファイルと、その command が指す実行ファイルを同じディレクトリに置いていることを確かめる。
// tree-sitter の拡張機能を除く実行ファイルは静的にリンクされていて、ファイルを置くだけで起動する。
// tree-sitter の拡張機能は glibc の共有ライブラリだけを要求する（NFR-01）。
//
// req: NFR-01, NFR-06
func TestDistLayout(t *testing.T) {
	dir := distDir(t)

	for _, name := range []string{"vi-imitation", "vi-imitation-mcp-bridge", "vi-imitation-ext-script"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			assertExecutable(t, path)
			assertStaticELF(t, path)
		})
	}

	manifests, err := filepath.Glob(filepath.Join(dir, "extensions", "*", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 宣言ファイルを持つ標準の拡張機能（Makefile の拡張機能のうち cmd/<名前>/manifest.json があるもの）が、すべて入っている。
	if want := len(manifestExtensions(t)); len(manifests) != want {
		t.Errorf("宣言ファイルの数 = %d, want %d: %v", len(manifests), want, manifests)
	}
	for _, m := range manifests {
		t.Run(filepath.Base(filepath.Dir(m)), func(t *testing.T) {
			data, err := os.ReadFile(m)
			if err != nil {
				t.Fatal(err)
			}
			var manifest struct {
				ID      string `json:"id"`
				Command string `json:"command"`
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatalf("%s を読めない: %v", m, err)
			}
			if manifest.ID == "" || manifest.Command == "" {
				t.Fatalf("%s の id か command が空: %+v", m, manifest)
			}
			if filepath.IsAbs(manifest.Command) {
				t.Fatalf("%s の command が絶対パス %q（配布物を置く場所に依存する）", m, manifest.Command)
			}
			// command の相対パスは、宣言ファイルのあるディレクトリから解決する（基本設計 5.1）。
			path := filepath.Join(filepath.Dir(m), manifest.Command)
			assertExecutable(t, path)
			// cgo の拡張機能（tree-sitter）は glibc に動的にリンクし、ほかは静的にリンクする（NFR-01）。
			if slices.Contains(cgoBinaries(t), filepath.Base(path)) {
				assertGlibcDynamicELF(t, path)
			} else {
				assertStaticELF(t, path)
			}
		})
	}
}

// TestDistLicenses は、配布物が、実行ファイルに静的にリンクするサードパーティのライセンスの文面を含むことを確かめる。
// Go の標準ライブラリとランタイム、tree-sitter とその文法、本体に写したアイコンの表の出どころは、
// 配布するときに著作権の表示とライセンスの文面を添える必要がある。
// tree-sitter の拡張機能は glibc に動的にリンクし、配布物は C の標準ライブラリを含まないので、その文面は無い。
func TestDistLicenses(t *testing.T) {
	dir := distDir(t)
	for _, rel := range []string{
		"licenses/go/LICENSE",
		"licenses/github.com/tree-sitter/go-tree-sitter/LICENSE",
		"licenses/github.com/tree-sitter/tree-sitter-go/LICENSE",
		"licenses/github.com/tree-sitter/tree-sitter-json/LICENSE",
		"licenses/github.com/traefik/yaegi/LICENSE",
		// go-udiff は、Go の作者の BSD-3-Clause と、go-udiff の作者の MIT の 2 つの文面を持つ（基本設計 6.8）。
		"licenses/github.com/aymanbagabas/go-udiff/LICENSE-BSD",
		"licenses/github.com/aymanbagabas/go-udiff/LICENSE-MIT",
		// 本体の既定のアイコンの表は、nvim-web-devicons と neo-tree.nvim から写した（FR-152。internal/ui の icons_default.go）。
		"licenses/github.com/nvim-tree/nvim-web-devicons/LICENSE",
		"licenses/github.com/nvim-neo-tree/neo-tree.nvim/LICENSE",
		// 色コードの着色の拡張機能の色の名前の表は、Neovim から写した（FR-260。cmd/vi-imitation-ext-colorizer の names.go）。
		"licenses/github.com/neovim/neovim/LICENSE.txt",
	} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			t.Errorf("%s が無い: %v", rel, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s が空", rel)
		}
	}
}

// TestSmokeAPIVersion は、make dist-smoke の確認のスクリプトが送る initialize の apiVersion が、
// 拡張機能 API のバージョンと一致することを確かめる。
// API のバージョンを上げたときに、スクリプトの直し忘れを make check で見つける。
func TestSmokeAPIVersion(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "smoke.sh"))
	if err != nil {
		t.Fatal(err)
	}
	want := `"apiVersion":"` + ext.APIVersion + `"`
	if !strings.Contains(string(data), want) {
		t.Errorf("testdata/smoke.sh に %s が無い", want)
	}
}

// assertExecutable は、path が実行の許可のある通常のファイルであることを確かめる。
func assertExecutable(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s が無い: %v", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s が実行できる通常のファイルでない（mode=%v）", path, info.Mode())
	}
}
