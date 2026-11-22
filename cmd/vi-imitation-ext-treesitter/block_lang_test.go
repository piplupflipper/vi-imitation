package main

import (
	"os"
	"path/filepath"
	"testing"
)

// testParsersEnv は、同梱していない言語の文法の共有ライブラリ（<言語>.so。シンボルは tree_sitter_<言語>）を置いたディレクトリを指す環境変数である。
// nvim-treesitter がビルドしたもの（Neovim の site/parser）をそのまま使える。
// 設定で足したパーサ（FR-142）と同じく dlopen で読む。CI には文法を置かないので、設定しなければテストを飛ばす。
// 設定したのに読めない（.so が無い、ABI の版が合わない）ときは、通ったつもりで飛ばさないように、テストを落とす。
const testParsersEnv = "VI_IMITATION_TEST_PARSERS"

// parseWith は、testParsersEnv の下の name の文法で text を解析した document を返す。環境変数が無ければテストを飛ばす。
func parseWith(t *testing.T, name, text string) *document {
	t.Helper()
	dir := os.Getenv(testParsersEnv)
	if dir == "" {
		t.Skipf("%s が無いので %s の文法を読まない", testParsersEnv, name)
	}
	grammar, err := loadGrammar(filepath.Join(dir, name+".so"), "tree_sitter_"+name)
	if err != nil {
		t.Fatalf("%s を設定したが %s の文法を読めない: %v", testParsersEnv, name, err)
	}
	lang, err := newGrammarLanguage(name, grammar, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lang.close)
	d := newDocument(lang, text)
	t.Cleanup(d.close)
	return d
}

// TestBlockObjectLanguages は、設定で足す言語（Python、bash、TypeScript、Lua）で、構文のまとまりの an と in が、
// カーソルを含む範囲だけを選び、else と elif の節を if の全体にまとめ、予約語の区切り（do、done、then、fi）を中身に含めないことを確かめる
// （PR #377 のレビューの必須 2 と推奨 1、4）。
//
// req: FR-11, FR-240
func TestBlockObjectLanguages(t *testing.T) {
	t.Run("python", func(t *testing.T) {
		d := parseWith(t, "python", "def f(a):\n    try:\n        risky()\n    except ValueError as e:\n        handle(e)\n    finally:\n        done()\n    if a > 0:\n        x = 1\n    elif a < 0:\n        x = 2\n    else:\n        x = 3\n    for i in range(3):\n        a += i\n")
		ifStmt := "    if a > 0:\n        x = 1\n    elif a < 0:\n        x = 2\n    else:\n        x = 3"
		try := "    try:\n        risky()\n    except ValueError as e:\n        handle(e)\n    finally:\n        done()"
		runBlockCases(t, d, []blockCase{
			{name: "an: except の中は try の全体", around: true, marker: "handle(e)", count: 1, ok: true, want: try, linewise: true},
			{name: "an: finally の中は try の全体", around: true, marker: "done()", count: 1, ok: true, want: try, linewise: true},
			{name: "in: except の中はカーソルを含む関数の本体", marker: "handle(e)", count: 1, ok: true, want: "    try:\n        risky()\n    except ValueError as e:\n        handle(e)\n    finally:\n        done()\n" + ifStmt + "\n    for i in range(3):\n        a += i", linewise: true},
			{name: "an: elif の中は if の全体", around: true, marker: "x = 2", count: 1, ok: true, want: ifStmt, linewise: true},
			{name: "an: else の中は if の全体", around: true, marker: "x = 3", count: 1, ok: true, want: ifStmt, linewise: true},
			{name: "in: elif の中身", marker: "x = 2", count: 1, ok: true, want: "        x = 2", linewise: true},
			{name: "an: for", around: true, marker: "a += i", count: 1, ok: true, want: "    for i in range(3):\n        a += i", linewise: true},
		})
	})
	t.Run("bash", func(t *testing.T) {
		d := parseWith(t, "bash", "f() {\n  if [ -n \"$1\" ]; then\n    echo one\n  elif [ -z \"$2\" ]; then\n    echo two\n  fi\n  for x in *.txt; do\n    echo \"$x\"\n  done\n  case \"$1\" in\n    a) echo a ;;\n  esac\n}\n")
		ifStmt := "  if [ -n \"$1\" ]; then\n    echo one\n  elif [ -z \"$2\" ]; then\n    echo two\n  fi"
		runBlockCases(t, d, []blockCase{
			{name: "an: if の本体の中は if の全体", around: true, marker: "echo one", count: 1, ok: true, want: ifStmt, linewise: true},
			{name: "an: elif の本体の中も if の全体", around: true, marker: "echo two", count: 1, ok: true, want: ifStmt, linewise: true},
			{name: "in: if の中身は条件と elif の節を含まない", marker: "echo one", count: 1, ok: true, want: "    echo one", linewise: true},
			{name: "in: elif の中身はその節の本体", marker: "echo two", count: 1, ok: true, want: "    echo two", linewise: true},
			{name: "in: for の中身は do と done を含まない", marker: "echo \"$x\"", count: 1, ok: true, want: "    echo \"$x\"", linewise: true},
			{name: "an: for の全体", around: true, marker: "echo \"$x\"", count: 1, ok: true, want: "  for x in *.txt; do\n    echo \"$x\"\n  done", linewise: true},
			{name: "an: case の全体", around: true, marker: "echo a", count: 1, ok: true, want: "  case \"$1\" in\n    a) echo a ;;\n  esac", linewise: true},
			{name: "in: case の中身", marker: "echo a", count: 1, ok: true, want: "    a) echo a ;;", linewise: true},
		})
	})
	t.Run("typescript", func(t *testing.T) {
		d := parseWith(t, "typescript", "function f() {\n  try {\n    risky();\n  } catch (e) {\n    console.error(e);\n  } finally {\n    done();\n  }\n}\n")
		runBlockCases(t, d, []blockCase{
			{name: "in: catch の中身", marker: "console.error", count: 1, ok: true, want: "    console.error(e);", linewise: true},
			{name: "2in: 後ろの finally を選ばずに関数の本体", marker: "console.error", count: 2, ok: true, want: "  try {\n    risky();\n  } catch (e) {\n    console.error(e);\n  } finally {\n    done();\n  }", linewise: true},
		})
	})
	t.Run("lua", func(t *testing.T) {
		d := parseWith(t, "lua", "if a > 0 then\n  print(\"pos\")\nelseif a < 0 then\n  print(\"neg\")\nelse\n  print(\"zero\")\nend\n")
		ifStmt := "if a > 0 then\n  print(\"pos\")\nelseif a < 0 then\n  print(\"neg\")\nelse\n  print(\"zero\")\nend"
		runBlockCases(t, d, []blockCase{
			{name: "an: elseif の中は if の全体", around: true, marker: "neg", count: 1, ok: true, want: ifStmt, linewise: true},
			{name: "in: elseif の中身", marker: "neg", count: 1, ok: true, want: "  print(\"neg\")", linewise: true},
			{name: "in: then の枝の中身は elseif の見出しを含まない", marker: "pos", count: 1, ok: true, want: "  print(\"pos\")", linewise: true},
			{name: "2in: then の側の本体も elseif の見出しも選ばない", marker: "neg", count: 2},
		})
		d2 := parseWith(t, "lua", "local function f(a)\n  repeat\n    a = a + 1\n  until a > 3\n  return a\nend\n")
		runBlockCases(t, d2, []blockCase{
			{name: "an: until の行は repeat の全体", around: true, marker: "until", count: 1, ok: true, want: "  repeat\n    a = a + 1\n  until a > 3", linewise: true},
			{name: "2an: 外は関数", around: true, marker: "until", count: 2, ok: true, want: "local function f(a)\n  repeat\n    a = a + 1\n  until a > 3\n  return a\nend", linewise: true},
		})
	})
}
