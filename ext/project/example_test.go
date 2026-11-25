package project_test

import (
	"fmt"
	"path/filepath"

	"github.com/piplupflipper/vi-imitation/ext/project"
)

// debugTarget は、デバッグ対象を動かす python と、作業ディレクトリを決める（呼び出す側の決めの例）。
// 作業ディレクトリは、設定に書いた値 configured を先にし、無ければ仮想環境のあるディレクトリ、Git の根、ファイルのディレクトリの順にする。
func debugTarget(file, configured string) (python, cwd string) {
	python = "python3"
	v, venvOK := project.PythonVenv(file)
	if venvOK {
		python = v.Python
	}
	switch root, rootOK := project.GitRoot(file); {
	case configured != "":
		cwd = configured
	case venvOK:
		cwd = v.Parent()
	case rootOK:
		cwd = root
	default:
		cwd = filepath.Dir(file)
	}
	return python, cwd
}

// デバッグ対象の python と作業ディレクトリと、デバッグアダプターを、別々の問い合わせとして引く。
func Example_debugPython() {
	file := "/srv/ws/packages/api/main.py"
	python, cwd := debugTarget(file, "")
	fmt.Println("python:", python, "cwd:", cwd)

	// デバッグアダプターは、デバッグ対象の仮想環境に依らず PATH で探す。
	adapter, err := project.Find("debugpy-adapter", file, project.KindPath)
	if err != nil {
		fmt.Println("adapter:", err)
		return
	}
	fmt.Println("adapter:", adapter.Path, "from", adapter.Source)
}
