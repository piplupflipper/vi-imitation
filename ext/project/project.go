// Package project は、ファイルのパスから、プロジェクトの根と、プロジェクトの中の実行ファイルを探す（FR-300。基本設計 6.7 の「プロジェクトの根と実行ファイルの探索」）。
//
// 拡張機能の側の共有の部品で、DAP の拡張機能（デバッグの構成の変数と作業ディレクトリの既定）と、
// 後で LSP と整形ツールの拡張機能（コマンドの探索）が使う。
// 基本設計 2 章の土台の層（ext と同じ層）に置き、標準ライブラリだけを import する（ext も import しない）。
//
// 次の 3 つを、別々の関数として持つ。どれも読むだけで、ファイルを作らず、外部のコマンドを起動しない。
//
//   - 根の探索（Root、GitRoot）
//   - 実行ファイルの探索（Find）
//   - Python の仮想環境の探索（PythonVenv）
//
// 入れ方の案内（mise の道具の ID で案内するか入れること）は持たない。持たせるかは別の issue で決める（#406）。
// 作業ディレクトリをどれにするか（根か、仮想環境のあるディレクトリか、設定に書いた値か）も決めない。
// 呼び出す側が、Root と Venv.Parent などから選ぶ。
//
// 探し方は、オーナーの nvim の lsp/resolver.lua（LSP、整形ツール、DAP に共通の解決の層）の順に合わせた（2026-11-25 に読んだ）。
// 合わせなかった点は、関数の doc に「resolver.lua と違う」と書いた。
//
// # 共通の決め
//
//   - 探索の起点は、渡したファイルのパスの filepath.Dir である（LSP の拡張機能の findRoot と同じ）。
//     ファイルもディレクトリも在らなくてよい（まだ保存していない新しいファイル）。在らないディレクトリでは何も当たらず、上へたどり続ける
//   - シンボリックリンクは解かない。リンクのファイルは、リンクの置き場所のディレクトリから探す。
//     目印、仮想環境のディレクトリ、実行ファイルは、リンクの先を見て在るかと種類を決める（os.Stat）。返すパスもリンクを解かない
//     （仮想環境の bin/python は、ふつう元の python へのリンクなので、解くと仮想環境の外を指す）
//   - ホームディレクトリで止めず、ファイルシステムの根までたどる（Node の node_modules/.bin を除く。Find を見よ）
//   - ファイルのパスは絶対パスを渡す。相対パスは、そのパスの中の上のディレクトリまでしかたどらない
//   - ファイルのパスが空（ファイルの無いバッファ）なら、ファイルから探す段を飛ばす
//   - 実行ファイルは、実行の権限（どれかの x のビット）を持つ通常のファイル（リンクの先を含む）だけを数える。権限の無いものは、無いものとして次を探す
package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Root は、ファイル file のディレクトリから親へたどり、markers のどれかの名前を含む最初のディレクトリを返す。
// 名前は、ファイルでもディレクトリでもよい（worktree と submodule の .git はファイルである）。
// 1 つのディレクトリでは markers の並びに意味を持たせず、近いディレクトリを先にする（LSP の拡張機能の findRoot と同じ）。
// 見つからなければ ok が false である。
//
// LSP の拡張機能の findRoot は、見つからなければファイルのディレクトリを返す。同じ結果が要る呼び出す側は、ok が false のときに filepath.Dir(file) を使う。
// resolver.lua の project_root は、file がディレクトリならそのディレクトリから探す。Root は findRoot に合わせ、常に filepath.Dir(file) から探す。
//
// req: FR-300
func Root(file string, markers []string) (root string, ok bool) {
	if file == "" || len(markers) == 0 {
		return "", false
	}
	for dir := filepath.Dir(file); ; {
		for _, m := range markers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// GitRoot は、Root(file, []string{".git"}) である。Git の作業ツリーの根（worktree なら、その worktree の根）を返す。
//
// req: FR-300
func GitRoot(file string) (root string, ok bool) {
	return Root(file, []string{".git"})
}

// Kind は、Find で、PATH より先にプロジェクトの中のどこを探すかである。
type Kind int

const (
	// KindPath は、PATH だけを探す。デバッグアダプターのように、プロジェクトの外に 1 つ入れた道具に使う。
	KindPath Kind = iota
	// KindPython は、Python の仮想環境（PythonVenv）の bin を、PATH より先に探す。
	KindPython
	// KindNode は、ファイルのディレクトリから Git の根までの node_modules/.bin を、PATH より先に探す。
	KindNode
)

// Source は、Find と PythonVenv が見つけた場所の種類である。
type Source int

const (
	// FromPath は、環境変数 PATH で見つけたことを表す。
	FromPath Source = iota
	// FromVirtualEnv は、環境変数 VIRTUAL_ENV（有効にした Python の仮想環境）で見つけたことを表す。
	FromVirtualEnv
	// FromProjectVenv は、ファイルのディレクトリから親へたどった Python の仮想環境（.venv、venv）で見つけたことを表す。
	FromProjectVenv
	// FromNodeModules は、ファイルのディレクトリから Git の根までの node_modules/.bin で見つけたことを表す。
	FromNodeModules
)

// String は、利用者へ知らせる文に入れる、見つけた場所の名前を返す。
func (s Source) String() string {
	switch s {
	case FromPath:
		return "PATH"
	case FromVirtualEnv:
		return "VIRTUAL_ENV"
	case FromProjectVenv:
		return "venv"
	case FromNodeModules:
		return "node_modules/.bin"
	}
	return "unknown"
}

// Executable は、Find が見つけた実行ファイルである。
type Executable struct {
	// Path は、実行ファイルのパスである。シンボリックリンクは解かない。
	// PATH で見つけたときは exec.LookPath が返したパスである。
	Path string
	// Source は、見つけた場所の種類である。
	Source Source
}

// ErrNotFound は、Find が実行ファイルを見つけられなかったことを表す。
var ErrNotFound = errors.New("project: executable not found")

// ErrInvalidName は、Find に渡した名前が、パスの区切りを含むなど、実行ファイルの名前でないことを表す。
// パスで書いた実行ファイルは探さずに使うものなので、呼び出す側が扱う。
var ErrInvalidName = errors.New("project: invalid executable name")

// Find は、実行ファイルの名前 name を、ファイル file のプロジェクトの中から先に探し、無ければ PATH で探す。
// 探す順は kind で決まる。
//
//   - KindPython: PythonVenv(file) の仮想環境の bin/name、PATH の順。
//     仮想環境が見つかって、その bin に name が無ければ、ほかの仮想環境を探さずに PATH へ進む（resolver.lua の python_bin と同じ）
//   - KindNode: ファイルのディレクトリから GitRoot(file) まで親へたどり、各ディレクトリの node_modules/.bin/name、PATH の順。
//     近いディレクトリ（monorepo のメンバー）を先にする。Git の根より上へはたどらない。
//     Git の作業ツリーの外のファイルでは node_modules/.bin を探さない（resolver.lua の node_bin と同じ。
//     整形ツールの拡張機能の今の探し方は、外ではファイルシステムの根までたどる）
//   - KindPath: PATH だけ
//
// 見つからなければ ErrNotFound、name が空か、パスの区切りを含むか、. か .. なら ErrInvalidName を返す。
// PATH で見つけた mise の shim の版が決まっているかは確かめない（LSP の拡張機能の FR-143 の扱いは、呼び出す側が持つ）。
//
// resolver.lua の resolve は、PATH で見つけたときにパスでなく名前を返す。Find は exec.LookPath のパスを返す。
//
// req: FR-300
func Find(name, file string, kind Kind) (Executable, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) || strings.ContainsRune(name, '/') {
		return Executable{}, ErrInvalidName
	}
	switch kind {
	case KindPython:
		if v, ok := PythonVenv(file); ok {
			if p := filepath.Join(v.Dir, "bin", name); isExecutable(p) {
				return Executable{Path: p, Source: v.Source}, nil
			}
		}
	case KindNode:
		if p, ok := nodeBin(name, file); ok {
			return Executable{Path: p, Source: FromNodeModules}, nil
		}
	case KindPath:
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return Executable{}, ErrNotFound
	}
	return Executable{Path: p, Source: FromPath}, nil
}

// nodeBin は、ファイルのディレクトリから Git の根まで親へたどり、最初に見つかった node_modules/.bin/name を返す。
func nodeBin(name, file string) (string, bool) {
	root, ok := GitRoot(file)
	if !ok {
		return "", false
	}
	for dir := filepath.Dir(file); ; {
		if p := filepath.Join(dir, "node_modules", ".bin", name); isExecutable(p) {
			return p, true
		}
		parent := filepath.Dir(dir)
		if dir == root || parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Venv は、PythonVenv が見つけた Python の仮想環境である。
type Venv struct {
	// Dir は、仮想環境のディレクトリ（.venv など）である。シンボリックリンクは解かない。
	Dir string
	// Python は、仮想環境の python（Dir/bin/python）である。デバッグ対象を動かす python に使う。
	// シンボリックリンクを解かない（解くと、仮想環境の外の元の python を指し、仮想環境の外の site-packages を読む）。
	Python string
	// Source は、FromVirtualEnv か FromProjectVenv である。
	Source Source
}

// Parent は、仮想環境のあるディレクトリ（Dir の親）を返す。
// uv のワークスペースでは、ワークスペースの根になる（メンバーは自分の仮想環境を持たないため）。
// デバッグの作業ディレクトリの候補の 1 つである（オーナーの nvim の DAP の設定は、これを作業ディレクトリにする）。
// 根（Root）と同じとは限らない。どちらを使うかは呼び出す側が決める。
func (v Venv) Parent() string {
	return filepath.Dir(v.Dir)
}

// venvNames は、1 つのディレクトリで仮想環境を探す名前を、優先の順に並べたものである（resolver.lua の python_venv と同じ）。
var venvNames = []string{".venv", "venv"}

// PythonVenv は、ファイル file の Python の仮想環境を、次の順に探して返す（resolver.lua の python_venv と同じ順）。
//
//  1. 環境変数 VIRTUAL_ENV（有効にした仮想環境）。絶対パスのときだけ使う
//  2. ファイルのディレクトリから親へ、各ディレクトリの .venv、venv の順。最初に見つかったもの
//
// どちらも、bin/python が実行できるものだけを仮想環境とする。
// .venv が通常のファイル（仮想環境の名前を書く道具の流儀）か、bin/python の無いディレクトリなら、次の候補へ進む。
// 親へたどるのは Git の根で止めない。uv のワークスペースのメンバーは自分の .venv を持たないので、
// メンバーのファイルからワークスペースの根の .venv へ届く。
// 見つからなければ ok が false である。
//
// デバッグアダプターを動かす python（debugpy のアダプター）は、デバッグ対象の仮想環境とは別に、Find(名前, file, KindPath) で引く。
//
// resolver.lua は VIRTUAL_ENV を相対パスでも使う。PythonVenv は、拡張機能の作業ディレクトリに依って意味が変わるので、絶対パスだけを使う
// （整形ツールの拡張機能の今の扱いと同じ）。
//
// req: FR-300
func PythonVenv(file string) (Venv, bool) {
	if env := os.Getenv("VIRTUAL_ENV"); filepath.IsAbs(env) {
		if v, ok := venvAt(filepath.Clean(env), FromVirtualEnv); ok {
			return v, true
		}
	}
	if file == "" {
		return Venv{}, false
	}
	for dir := filepath.Dir(file); ; {
		for _, n := range venvNames {
			if v, ok := venvAt(filepath.Join(dir, n), FromProjectVenv); ok {
				return v, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Venv{}, false
		}
		dir = parent
	}
}

// venvAt は、dir の bin/python が実行できれば、dir を仮想環境として返す。
func venvAt(dir string, src Source) (Venv, bool) {
	py := filepath.Join(dir, "bin", "python")
	if !isExecutable(py) {
		return Venv{}, false
	}
	return Venv{Dir: dir, Python: py, Source: src}, true
}

// isExecutable は、path が実行の権限を持つ通常のファイル（シンボリックリンクの先を含む）かを返す。
func isExecutable(path string) bool {
	fi, err := os.Stat(path) //nolint:gosec // G703: 利用者の環境（VIRTUAL_ENV）とファイルのパスから作った実行ファイルの候補のパスを、読まずに調べるだけ
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}
