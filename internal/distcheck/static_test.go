package distcheck

import (
	"debug/buildinfo"
	"debug/elf"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// EnvBuildDir は、`make build` が実行ファイルを置いたディレクトリを渡す環境変数の名前である。
// 指定が無ければ、生成物を読むテストは飛ばす。
const EnvBuildDir = "VI_IMITATION_BUILD_DIR"

// buildDir は、検査する実行ファイルのディレクトリを返す。
// 環境変数の指定が無ければテストを飛ばす。
func buildDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(EnvBuildDir)
	if dir == "" {
		t.Skipf("%s が指定されていない（make build-check で実行する）", EnvBuildDir)
	}
	return dir
}

// buildSetting は、実行ファイルに埋め込まれた build settings から key の値を返す。
func buildSetting(t *testing.T, path, key string) (string, bool) {
	t.Helper()
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		t.Fatalf("%s の build info を読めない: %v", path, err)
	}
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value, true
		}
	}
	return "", false
}

// assertStaticELF は、path の ELF が動的リンカー（PT_INTERP）と共有ライブラリ（DT_NEEDED）を要求しないことを確かめる。
// どちらも無ければ、C の標準ライブラリを含め、実行時に他のファイルを読み込まずに起動する。
func assertStaticELF(t *testing.T, path string) {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatalf("%s を ELF として開けない: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			t.Errorf("%s は動的リンカーを要求する（PT_INTERP がある）", path)
		}
	}
	libs, err := f.ImportedLibraries()
	if err != nil {
		t.Fatalf("%s の共有ライブラリの一覧を読めない: %v", path, err)
	}
	if len(libs) > 0 {
		t.Errorf("%s は共有ライブラリ %v を要求する", path, libs)
	}
}

// TestBuildMainStatic は、`make build` の本体が CGO_ENABLED=0 でビルドされ、
// 静的にリンクされていることを確かめる。
//
// req: NFR-02
func TestBuildMainStatic(t *testing.T) {
	path := filepath.Join(buildDir(t), "vi-imitation")

	cgo, ok := buildSetting(t, path, "CGO_ENABLED")
	if !ok || cgo != "0" {
		t.Errorf("%s の CGO_ENABLED = %q（ok=%v）, want \"0\"", path, cgo, ok)
	}
	assertStaticELF(t, path)
}

// TestBuildMainSize は、`make build` の本体の実行ファイルの大きさを、ログに出す。
// `make build` はシンボルとデバッグ情報を削らないので、削らない形の大きさである。
//
// 大きさでは失敗させない。
// 本体を小さく保つことは、2026-11-26 のオーナーの決めで、要件ではなく努力目標になった（要件定義の NFR-05 の注。#442）。
// CI の `make build-check` のログに毎回出るので、大きさの推移はログで追える。
func TestBuildMainSize(t *testing.T) {
	path := filepath.Join(buildDir(t), "vi-imitation")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s の大きさ: %d バイト（%.2f MiB）", path, fi.Size(), float64(fi.Size())/(1024*1024))
}

// TestBuildAllStatic は、`make build` の、cgo の拡張機能を除く全ての実行ファイルが静的にリンクされていることを確かめる。
// 動的リンカーと共有ライブラリを要求しないので、glibc の版や musl かどうかによらず、ファイルを置くだけで起動する。
// cgo の拡張機能（tree-sitter）は glibc に動的にリンクする（TestBuildCgoGlibcDynamic）。
//
// req: NFR-01
func TestBuildAllStatic(t *testing.T) {
	dir := buildDir(t)
	for _, name := range pureGoBinaries(t) {
		t.Run(name, func(t *testing.T) {
			assertStaticELF(t, filepath.Join(dir, name))
		})
	}
}

// glibcLibraries は、cgo の拡張機能が要求してよい共有ライブラリ（glibc のもの）である。
// glibc 2.34 より前では libdl と libpthread が libc と別のファイルなので、それらも含める。
var glibcLibraries = map[string]bool{
	"libc.so.6": true, "libdl.so.2": true, "libpthread.so.0": true, "libm.so.6": true,
}

// assertGlibcDynamicELF は、path の ELF が glibc の動的リンカーを要求し、glibc の共有ライブラリだけを要求することを確かめる。
func assertGlibcDynamicELF(t *testing.T, path string) {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatalf("%s を ELF として開けない: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	interp := ""
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		b := make([]byte, p.Filesz)
		if _, err = p.ReadAt(b, 0); err != nil {
			t.Fatalf("%s の PT_INTERP を読めない: %v", path, err)
		}
		interp = strings.TrimRight(string(b), "\x00")
	}
	if !strings.Contains(filepath.Base(interp), "ld-linux") {
		t.Errorf("%s の動的リンカー = %q, want glibc の ld-linux", path, interp)
	}
	libs, err := f.ImportedLibraries()
	if err != nil {
		t.Fatalf("%s の共有ライブラリの一覧を読めない: %v", path, err)
	}
	if !slices.Contains(libs, "libc.so.6") {
		t.Errorf("%s は libc.so.6 を要求しない: %v", path, libs)
	}
	for _, l := range libs {
		if !glibcLibraries[l] {
			t.Errorf("%s は glibc でない共有ライブラリ %s を要求する", path, l)
		}
	}
}

// TestBuildCgoGlibcDynamic は、cgo の拡張機能が glibc に動的にリンクされていることを確かめる（NFR-01、2026-11-20 のオーナーの裁定、#277）。
// 利用者の手元でビルドしたパーサを dlopen で読む（FR-142）には、動的にリンクする必要がある。
// glibc の共有ライブラリだけを要求するので、最近の glibc の Linux ではファイルを置くだけで起動する。
//
// req: NFR-01
func TestBuildCgoGlibcDynamic(t *testing.T) {
	dir := buildDir(t)
	for _, name := range cgoBinaries(t) {
		t.Run(name, func(t *testing.T) {
			assertGlibcDynamicELF(t, filepath.Join(dir, name))
		})
	}
}

// TestBuildPureGo は、cgo の拡張機能以外が CGO_ENABLED=0 でビルドされていることを確かめる。
// cgo を使うのは tree-sitter の拡張機能だけに限る（Makefile）。
func TestBuildPureGo(t *testing.T) {
	dir := buildDir(t)
	for _, name := range pureGoBinaries(t) {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			if cgo, ok := buildSetting(t, path, "CGO_ENABLED"); !ok || cgo != "0" {
				t.Errorf("%s の CGO_ENABLED = %q（ok=%v）, want \"0\"", path, cgo, ok)
			}
		})
	}
}
