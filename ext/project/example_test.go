package project_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/piplupflipper/vi-imitation/ext/project"
)

// debugTarget は、デバッグ対象を動かす python と、作業ディレクトリを決める（呼び出す側の決めの例）。
// 作業ディレクトリは、設定に書いた値 configured を先にし、無ければ仮想環境のあるディレクトリ、Git の根、ファイルのディレクトリの順にする。
func debugTarget(file, configured string) (python, cwd string, err error) {
	python = "python3"
	v, err := project.PythonVenv(file)
	venvOK := err == nil
	if err != nil && !errors.Is(err, project.ErrNotFound) {
		return "", "", err // 読めない仮想環境など。見つからないこととは別に知らせる
	}
	if venvOK {
		python = v.Python
	}
	root, rootErr := project.GitRoot(file)
	switch {
	case configured != "":
		cwd = configured
	case venvOK:
		cwd = v.Parent()
	case rootErr == nil:
		cwd = root
	default:
		cwd = filepath.Dir(file)
	}
	return python, cwd, nil
}

// デバッグ対象の python と作業ディレクトリと、デバッグアダプターを、別々の問い合わせとして引く。
// uv のワークスペース（根に .venv、メンバーに無い）のメンバーのファイルで、根の .venv の python と、根の作業ディレクトリになる。
func Example_debugPython() {
	dir, err := os.MkdirTemp("", "project-example-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	mustWrite := func(rel string, mode os.FileMode) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, nil, mode)
		_ = os.Chmod(p, mode)
	}
	mustWrite("ws/.git/HEAD", 0o600)
	mustWrite("ws/.venv/bin/python", 0o755)
	mustWrite("tools/debugpy-adapter", 0o755)
	oldPath, oldVenv := os.Getenv("PATH"), os.Getenv("VIRTUAL_ENV")
	_ = os.Setenv("PATH", filepath.Join(dir, "tools"))
	_ = os.Setenv("VIRTUAL_ENV", "")
	defer func() { _ = os.Setenv("PATH", oldPath); _ = os.Setenv("VIRTUAL_ENV", oldVenv) }()
	rel := func(p string) string { r, _ := filepath.Rel(dir, p); return r }

	file := filepath.Join(dir, "ws", "packages", "api", "main.py")
	python, cwd, err := debugTarget(file, "")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("python:", rel(python))
	fmt.Println("cwd:", rel(cwd))

	// デバッグアダプターは、デバッグ対象の仮想環境に依らず PATH で探す。
	adapter, err := project.Find("debugpy-adapter", file, project.KindPath)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("adapter:", rel(adapter.Path), "from", adapter.Source)
	// Output:
	// python: ws/.venv/bin/python
	// cwd: ws
	// adapter: tools/debugpy-adapter from PATH
}
