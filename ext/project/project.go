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
// 合わせなかった点は、下の「共通の決め」と関数の doc に「resolver.lua と違う」と書いた。
//
// # 共通の決め
//
//   - 探索の起点は、パスがディレクトリならそのディレクトリ、それ以外（ファイル、まだ無いパス）なら filepath.Dir である。
//     ファイルのパスでは LSP の拡張機能の findRoot と同じ起点で、ディレクトリでは resolver.lua の start_dir と同じ起点である（PR #407 のレビューの推奨 1）。
//     ファイルもディレクトリも在らなくてよい（まだ保存していない新しいファイル）。在らないディレクトリでは何も当たらず、上へたどり続ける
//   - 親へたどる途中で、他の利用者が書けるディレクトリ（パーミッションの 0o002 が立つもの。/tmp など）に当たったら、
//     そのディレクトリの中の候補（目印、.venv、venv、node_modules/.bin）を見ずに、そこで止める。
//     他の利用者がそこに置いた実行ファイルを、自分の権限で起動しないためである（PR #407 のレビューの必須 1）。
//     /tmp/proj/.venv のように、誰でも書けるディレクトリの下の、そうでないディレクトリの中の候補は採る。
//     resolver.lua はこの守りを持たず、/tmp の .venv も返す
//   - シンボリックリンクは解かない。リンクのファイルは、リンクの置き場所のディレクトリから探す。
//     目印、仮想環境のディレクトリ、実行ファイル、たどるディレクトリの権限は、リンクの先を見て決める（os.Stat）。返すパスもリンクを解かない
//     （仮想環境の bin/python は、ふつう元の python へのリンクなので、解くと仮想環境の外を指す）
//   - ホームディレクトリでは止めない（上の他の利用者が書けるディレクトリと、Node の node_modules/.bin の Git の根を除き、ファイルシステムの根までたどる）
//   - パスは絶対パスを渡す。相対パスは、そのパスの中の上のディレクトリまでしかたどらない
//   - パスが空（ファイルの無いバッファ）なら、パスから探す段を飛ばす
//   - 実行ファイルは、通常のファイル（リンクの先を含む）で、今の利用者が実行できる（Linux と macOS では access(2) の X_OK）ものだけを数える。
//     nvim の executable() に合わせた。実行できないものは、無いものとして次を探す
//
// # 誤り
//
// 見つからなければ、ErrNotFound を包んだ誤りを返す。他の利用者が書けるディレクトリで止めたときと、PATH で見つからなかったときは、理由を包む。
// 候補を調べるときに権限の誤り（fs.ErrPermission。読めない .venv など）が起きたら、外側の候補へ進まずに止め、その誤りを包んで返す。
// 呼び出す側は、errors.Is(err, ErrNotFound) と errors.Is(err, fs.ErrPermission) で見分けられる（PR #407 のレビューの推奨 5）。
// 在らないことと権限の誤りのほかの誤り（ENOTDIR、シンボリックリンクの輪など）は、在らないものとして扱う（resolver.lua と同じ）。
package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotFound は、根、仮想環境、実行ファイルが見つからなかったことを表す。
var ErrNotFound = errors.New("project: not found")

// ErrInvalidName は、Find に渡した名前が、パスの区切りを含むなど、実行ファイルの名前でないことを表す。
// パスで書いた実行ファイルは探さずに使うものなので、呼び出す側が扱う。
var ErrInvalidName = errors.New("project: invalid executable name")

// stat は、path の情報を返す。在らないもの（と、権限の誤りでない誤り）は nil を返す。権限の誤りだけを誤りとして返す。
func stat(path string) (fs.FileInfo, error) {
	fi, err := os.Stat(path) //nolint:gosec // G703: 利用者の環境（VIRTUAL_ENV）とファイルのパスから作った候補のパスを、読まずに調べるだけ
	if err == nil {
		return fi, nil
	}
	if errors.Is(err, fs.ErrPermission) {
		return nil, fmt.Errorf("project: %w", err)
	}
	return nil, nil
}

// startDir は、探索の起点のディレクトリを返す。path がディレクトリならそのもの、それ以外なら filepath.Dir(path) である。
func startDir(path string) string {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return filepath.Clean(path)
	}
	return filepath.Dir(path)
}

// walk は、start から親へたどり、各ディレクトリで visit を呼ぶ。visit が true か誤りを返したら止め、その値を返す。
// stopAt が空でなければ、stopAt を調べた後で止める。
// 他の利用者が書けるディレクトリに当たったら、visit を呼ばずに、理由を包んだ ErrNotFound を返す。
// ファイルシステムの根まで当たらなければ ErrNotFound を返す。
func walk(start, stopAt string, visit func(dir string) (bool, error)) error {
	for dir := start; ; {
		fi, err := stat(dir)
		if err != nil {
			return err
		}
		if fi != nil && fi.IsDir() && fi.Mode().Perm()&0o002 != 0 {
			return fmt.Errorf("%w: stopped at world-writable directory %s", ErrNotFound, dir)
		}
		found, err := visit(dir)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
		parent := filepath.Dir(dir)
		if dir == stopAt || parent == dir {
			return ErrNotFound
		}
		dir = parent
	}
}

// Root は、パス path（ファイルかディレクトリ。共通の決めの起点）から親へたどり、markers のどれかの名前を含む最初のディレクトリを返す。
// 名前は、ファイルでもディレクトリでもよい（worktree と submodule の .git はファイルである）。
// 1 つのディレクトリでは markers の並びに意味を持たせず、近いディレクトリを先にする（LSP の拡張機能の findRoot と同じ）。
// 見つからなければ ErrNotFound を包んだ誤りを返す。
//
// LSP の拡張機能の findRoot は、見つからなければファイルのディレクトリを返す。
// 同じ結果が要る呼び出す側は、ErrNotFound のときに filepath.Dir(file) を使う（他の利用者が書けるディレクトリの下を除き、ファイルのパスでは findRoot と同じ結果になる）。
//
// req: FR-300
func Root(path string, markers []string) (string, error) {
	if path == "" || len(markers) == 0 {
		return "", ErrNotFound
	}
	var root string
	err := walk(startDir(path), "", func(dir string) (bool, error) {
		for _, m := range markers {
			fi, err := stat(filepath.Join(dir, m))
			if err != nil {
				return false, err
			}
			if fi != nil {
				root = dir
				return true, nil
			}
		}
		return false, nil
	})
	return root, err
}

// GitRoot は、Root(path, []string{".git"}) である。Git の作業ツリーの根（worktree なら、その worktree の根）を返す。
// 壊れたシンボリックリンクの .git は、在らないものとして扱う。
//
// req: FR-300
func GitRoot(path string) (string, error) {
	return Root(path, []string{".git"})
}

// Kind は、Find で、PATH より先にプロジェクトの中のどこを探すかである。
type Kind int

const (
	// KindPath は、PATH だけを探す。デバッグアダプターのように、プロジェクトの外に 1 つ入れた道具に使う。
	KindPath Kind = iota
	// KindPython は、Python の仮想環境（PythonVenv）の bin を、PATH より先に探す。
	KindPython
	// KindNode は、起点のディレクトリから Git の根までの node_modules/.bin を、PATH より先に探す。
	KindNode
)

// Source は、Find と PythonVenv が見つけた場所の種類である。
type Source int

const (
	// FromPath は、環境変数 PATH で見つけたことを表す。
	FromPath Source = iota
	// FromVirtualEnv は、環境変数 VIRTUAL_ENV（有効にした Python の仮想環境）で見つけたことを表す。
	FromVirtualEnv
	// FromProjectVenv は、起点のディレクトリから親へたどった Python の仮想環境（.venv、venv）で見つけたことを表す。
	FromProjectVenv
	// FromNodeModules は、起点のディレクトリから Git の根までの node_modules/.bin で見つけたことを表す。
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

// Find は、実行ファイルの名前 name を、パス path（ファイルかディレクトリ）のプロジェクトの中から先に探し、無ければ PATH で探す。
// 探す順は kind で決まる。
//
//   - KindPython: PythonVenv(path) の仮想環境の bin/name、PATH の順。
//     仮想環境が見つかって、その bin に name が無ければ、ほかの仮想環境を探さずに PATH へ進む（resolver.lua の python_bin と同じ）
//   - KindNode: 起点のディレクトリから GitRoot(path) まで親へたどり、各ディレクトリの node_modules/.bin/name、PATH の順。
//     近いディレクトリ（monorepo のメンバー）を先にする。Git の根より上へはたどらない。
//     Git の作業ツリーの外では node_modules/.bin を探さない（resolver.lua の node_bin と同じ。
//     整形ツールの拡張機能の今の探し方は、外ではファイルシステムの根までたどる）
//   - KindPath: PATH だけ
//
// name が空か、パスの区切りを含むか、. か .. なら ErrInvalidName を返す。
// プロジェクトの中を調べるときに権限の誤りが起きたら、PATH へ進まずにその誤りを返す。
// PATH でも見つからなければ、exec.LookPath の誤りを包んだ ErrNotFound を返す。
// PATH で見つけた mise の shim の版が決まっているかは確かめない（LSP の拡張機能の FR-143 の扱いは、呼び出す側が持つ）。
//
// resolver.lua の resolve は、PATH で見つけたときにパスでなく名前を返す。Find は exec.LookPath のパスを返す。
//
// req: FR-300
func Find(name, path string, kind Kind) (Executable, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) || strings.ContainsRune(name, '/') {
		return Executable{}, ErrInvalidName
	}
	switch kind {
	case KindPython:
		v, err := PythonVenv(path)
		switch {
		case err == nil:
			p := filepath.Join(v.Dir, "bin", name)
			ok, xerr := isExecutable(p)
			if xerr != nil {
				return Executable{}, xerr
			}
			if ok {
				return Executable{Path: p, Source: v.Source}, nil
			}
		case !errors.Is(err, ErrNotFound):
			return Executable{}, err
		}
	case KindNode:
		p, err := nodeBin(name, path)
		switch {
		case err == nil:
			return Executable{Path: p, Source: FromNodeModules}, nil
		case !errors.Is(err, ErrNotFound):
			return Executable{}, err
		}
	case KindPath:
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return Executable{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return Executable{Path: p, Source: FromPath}, nil
}

// nodeBin は、起点のディレクトリから Git の根まで親へたどり、最初に見つかった node_modules/.bin/name を返す。
func nodeBin(name, path string) (string, error) {
	if path == "" {
		return "", ErrNotFound
	}
	root, err := GitRoot(path)
	if err != nil {
		return "", err
	}
	var found string
	err = walk(startDir(path), root, func(dir string) (bool, error) {
		p := filepath.Join(dir, "node_modules", ".bin", name)
		ok, xerr := isExecutable(p)
		if ok {
			found = p
		}
		return ok, xerr
	})
	return found, err
}

// Venv は、PythonVenv が見つけた Python の仮想環境である。
type Venv struct {
	// Dir は、仮想環境のディレクトリ（.venv など）である。シンボリックリンクは解かない。
	Dir string
	// Python は、仮想環境の python（Dir/bin/python）である。デバッグ対象を動かす python に使う。
	// シンボリックリンクを解かない（解くと、仮想環境の外の元の python を指し、仮想環境の外の site-packages を読む）。
	Python string
	// Source は、FromProjectVenv か FromVirtualEnv である。
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

// PythonVenv は、パス path（ファイルかディレクトリ）の Python の仮想環境を、次の順に探して返す。
//
//  1. 起点のディレクトリから親へ、各ディレクトリの .venv、venv の順。最初に見つかったもの
//  2. 環境変数 VIRTUAL_ENV（有効にした仮想環境）。絶対パスのときだけ使う
//
// どちらも、bin/python が実行できるものだけを仮想環境とする。
// .venv が通常のファイル（仮想環境の名前を書く道具の流儀）か、bin/python の無いディレクトリなら、次の候補へ進む。
// 親へたどるのは Git の根で止めない。uv のワークスペースのメンバーは自分の .venv を持たないので、
// メンバーのファイルからワークスペースの根の .venv へ届く。
// 1 で他の利用者が書けるディレクトリに当たって止めたときも、2 へ進む。
// 見つからなければ ErrNotFound を包んだ誤りを返す。1 で権限の誤りが起きたら、2 へ進まずにその誤りを返す。
//
// デバッグアダプターを動かす python（debugpy のアダプター）は、デバッグ対象の仮想環境とは別に、Find(名前, path, KindPath) で引く。
//
// resolver.lua と違う点は次のとおりである。
// resolver.lua は VIRTUAL_ENV を先にするが、PythonVenv はプロジェクトの中を先にする。
// 拡張機能の環境は server の環境で、server を起動したシェルの VIRTUAL_ENV が、同じ server で開いた別のプロジェクトのファイルにも効くためである
// （整形ツールの拡張機能の PR #341 の再レビューの推奨 2 の決めと同じ順。PR #407 のレビューの推奨 2）。
// resolver.lua は VIRTUAL_ENV を相対パスでも使うが、PythonVenv は、拡張機能の作業ディレクトリに依って意味が変わるので、絶対パスだけを使う。
//
// req: FR-300
func PythonVenv(path string) (Venv, error) {
	walkErr := ErrNotFound
	if path != "" {
		var found Venv
		walkErr = walk(startDir(path), "", func(dir string) (bool, error) {
			for _, n := range venvNames {
				v, ok, err := venvAt(filepath.Join(dir, n), FromProjectVenv)
				if err != nil || ok {
					found = v
					return ok, err
				}
			}
			return false, nil
		})
		if walkErr == nil {
			return found, nil
		}
		if !errors.Is(walkErr, ErrNotFound) {
			return Venv{}, walkErr
		}
	}
	if env := os.Getenv("VIRTUAL_ENV"); filepath.IsAbs(env) {
		v, ok, err := venvAt(filepath.Clean(env), FromVirtualEnv)
		if err != nil {
			return Venv{}, err
		}
		if ok {
			return v, nil
		}
	}
	return Venv{}, walkErr
}

// venvAt は、dir の bin/python が実行できれば、dir を仮想環境として返す。
func venvAt(dir string, src Source) (Venv, bool, error) {
	py := filepath.Join(dir, "bin", "python")
	ok, err := isExecutable(py)
	if !ok || err != nil {
		return Venv{}, false, err
	}
	return Venv{Dir: dir, Python: py, Source: src}, true, nil
}

// isExecutable は、path が通常のファイル（シンボリックリンクの先を含む）で、今の利用者が実行できるかを返す。
// 調べるときの権限の誤りだけを誤りとして返す。
func isExecutable(path string) (bool, error) {
	fi, err := stat(path)
	if err != nil || fi == nil {
		return false, err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return false, nil
	}
	return canExecute(path), nil
}
