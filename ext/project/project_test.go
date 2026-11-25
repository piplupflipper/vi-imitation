package project

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// noMarker は、テストの一時ディレクトリから / までのどこにも無いはずの目印の名前である。
const noMarker = "vi-imitation-test-no-such-marker-406"

// write は、path にファイルを作る。親のディレクトリも作る。
func write(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	// umask で x のビットが落ちないよう、改めて付ける。
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// mkdir は、path のディレクトリを作る。
func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

// chmod は、path のパーミッションを mode にする。
func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// symlink は、newname から oldname へのシンボリックリンクを作る。
func symlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(newname), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldname, newname); err != nil {
		t.Fatal(err)
	}
}

// tempDir は、シンボリックリンクを解いた一時ディレクトリを返す（TMPDIR がリンクの環境でも、比べるパスをそろえる）。
func tempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// isolate は、テストの外の環境（有効にした仮想環境、PATH）が結果に入らないようにする。PATH は空のディレクトリにする。
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("VIRTUAL_ENV", "")
	t.Setenv("PATH", t.TempDir())
}

// skipIfRoot は、root ではパーミッションで読めなくできないので、テストを飛ばす。
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
}

// lspFindRoot は、LSP の拡張機能の findRoot（cmd/vi-imitation-ext-lsp/config.go。main の 03ceab4）の写しである。
// 同じ層どうしと cmd のパッケージは import できない（基本設計 2 章、dev-cycle の 2.2）ので、ソースを写して比べる。
// 元を変えたら、ここも写し直す。LSP の拡張機能を ext/project に移したら、この写しと比べるテストを消す。
func lspFindRoot(path string, markers []string) string {
	start := filepath.Dir(path)
	for dir := start; ; {
		for _, m := range markers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// ファイルのパスでは、Root は LSP の拡張機能の findRoot と同じ根を返す。見つからないときは ErrNotFound で、
// findRoot の結果（ファイルのディレクトリ）は呼び出す側が filepath.Dir で作る。同じ入力の表を両方に流して比べる。
// 入力は、cmd/vi-imitation-ext-lsp の TestFindRoot の 2 件に、境界を足したもの。
//
// req: FR-300
func TestRootMatchesLSPFindRoot(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, "a", "b"))
	write(t, filepath.Join(dir, "a", "go.mod"), 0o600)
	write(t, filepath.Join(dir, "go.work"), 0o600)
	write(t, filepath.Join(dir, "a", "b", "x.go"), 0o600)
	write(t, filepath.Join(dir, "wt", ".git"), 0o600)
	symlink(t, filepath.Join(dir, "nowhere"), filepath.Join(dir, "broken", ".git"))
	symlink(t, filepath.Join(dir, "a", "b", "x.go"), filepath.Join(dir, "link", "x.go"))

	tests := []struct {
		file    string
		markers []string
	}{
		{filepath.Join(dir, "a", "b", "x.go"), []string{"go.mod"}},
		{filepath.Join(dir, "a", "b", "x.go"), []string{noMarker}},
		{filepath.Join(dir, "a", "b", "x.go"), []string{"go.work", "go.mod"}},
		{filepath.Join(dir, "a", "b", "x.go"), []string{"go.work"}},
		{filepath.Join(dir, "a", "b", "new", "y.go"), []string{"go.mod"}},
		{filepath.Join(dir, "wt", "sub", "x.go"), []string{".git", "go.work"}},
		{filepath.Join(dir, "broken", "x.go"), []string{".git"}},
		{filepath.Join(dir, "link", "x.go"), []string{"go.mod"}},
	}
	for _, tt := range tests {
		want := lspFindRoot(tt.file, tt.markers)
		got, err := Root(tt.file, tt.markers)
		if errors.Is(err, ErrNotFound) {
			got = filepath.Dir(tt.file)
		} else if err != nil {
			t.Errorf("Root(%s, %v): %v", tt.file, tt.markers, err)
			continue
		}
		if got != want {
			t.Errorf("Root(%s, %v) = %q, findRoot = %q", tt.file, tt.markers, got, want)
		}
	}
}

// 近いディレクトリを先にし、1 つのディレクトリの中では markers の並びに意味を持たせない（findRoot と同じ）。
//
// req: FR-300
func TestRootNearestFirst(t *testing.T) {
	dir := tempDir(t)
	write(t, filepath.Join(dir, "go.work"), 0o600)
	write(t, filepath.Join(dir, "m", "go.mod"), 0o600)
	file := filepath.Join(dir, "m", "pkg", "x.go")

	for _, markers := range [][]string{{"go.work", "go.mod"}, {"go.mod", "go.work"}} {
		if got, err := Root(file, markers); err != nil || got != filepath.Join(dir, "m") {
			t.Errorf("Root(%v) = %q, %v; want %q", markers, got, err, filepath.Join(dir, "m"))
		}
	}
	if got, err := Root(file, []string{"go.work"}); err != nil || got != dir {
		t.Errorf("Root(go.work) = %q, %v; want %q", got, err, dir)
	}
}

// まだ無いディレクトリの新しいファイルからも、在る上のディレクトリの目印に届く。空のパスと空の目印は見つからない。
//
// req: FR-300
func TestRootMissingAndEmpty(t *testing.T) {
	dir := tempDir(t)
	write(t, filepath.Join(dir, "go.mod"), 0o600)
	if got, err := Root(filepath.Join(dir, "new", "dir", "x.go"), []string{"go.mod"}); err != nil || got != dir {
		t.Errorf("Root(new file) = %q, %v; want %q", got, err, dir)
	}
	if got, err := Root("", []string{"go.mod"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Root(\"\") = %q, %v; want ErrNotFound", got, err)
	}
	if got, err := Root(filepath.Join(dir, "x.go"), nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("Root(no markers) = %q, %v; want ErrNotFound", got, err)
	}
}

// ディレクトリを渡したら、そのディレクトリから探す（resolver.lua の start_dir と同じ。nvim の LSP の設定は根のディレクトリを渡して呼ぶ）。
//
// req: FR-300
func TestDirectoryPath(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	ws := filepath.Join(dir, "ws")
	mkdir(t, filepath.Join(ws, ".git"))
	write(t, filepath.Join(ws, ".venv", "bin", "python"), 0o755)
	write(t, filepath.Join(ws, "node_modules", ".bin", "biome"), 0o755)

	if got, err := GitRoot(ws); err != nil || got != ws {
		t.Errorf("GitRoot(dir) = %q, %v; want %q", got, err, ws)
	}
	if v, err := PythonVenv(ws + "/"); err != nil || v.Dir != filepath.Join(ws, ".venv") {
		t.Errorf("PythonVenv(dir) = %+v, %v; want %q", v, err, filepath.Join(ws, ".venv"))
	}
	if got, err := Find("biome", ws, KindNode); err != nil || got.Path != filepath.Join(ws, "node_modules", ".bin", "biome") {
		t.Errorf("Find(dir) = %+v, %v", got, err)
	}
}

// シンボリックリンクのファイルは、リンクの先でなく、リンクの置き場所から探す。
//
// req: FR-300
func TestRootSymlinkedFile(t *testing.T) {
	dir := tempDir(t)
	write(t, filepath.Join(dir, "real", "go.mod"), 0o600)
	write(t, filepath.Join(dir, "real", "x.go"), 0o600)
	write(t, filepath.Join(dir, "link", "package.json"), 0o600)
	symlink(t, filepath.Join(dir, "real", "x.go"), filepath.Join(dir, "link", "x.go"))

	got, err := Root(filepath.Join(dir, "link", "x.go"), []string{"go.mod", "package.json"})
	if err != nil || got != filepath.Join(dir, "link") {
		t.Errorf("Root(link) = %q, %v; want %q", got, err, filepath.Join(dir, "link"))
	}
}

// worktree と submodule の .git はファイルで、その worktree の根を返す。外側のリポジトリの根まで上らない。
//
// req: FR-300
func TestGitRootWorktree(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, ".git"))
	wt := filepath.Join(dir, "wt")
	write(t, filepath.Join(wt, ".git"), 0o600)

	if got, err := GitRoot(filepath.Join(wt, "sub", "x.go")); err != nil || got != wt {
		t.Errorf("GitRoot(worktree) = %q, %v; want %q", got, err, wt)
	}
	if got, err := GitRoot(filepath.Join(dir, "x.go")); err != nil || got != dir {
		t.Errorf("GitRoot(repo) = %q, %v; want %q", got, err, dir)
	}
}

// ホームディレクトリで止めず、その上の目印に届く。
//
// req: FR-300
func TestRootAboveHome(t *testing.T) {
	dir := tempDir(t)
	home := filepath.Join(dir, "home")
	mkdir(t, home)
	t.Setenv("HOME", home)
	write(t, filepath.Join(dir, "go.mod"), 0o600)

	if got, err := Root(filepath.Join(home, "p", "x.go"), []string{"go.mod"}); err != nil || got != dir {
		t.Errorf("Root = %q, %v; want %q (above HOME)", got, err, dir)
	}
}

// 他の利用者が書けるディレクトリ（/tmp のような 1777）の中の目印、.venv、node_modules/.bin は採らず、そこで止める。
// その下の、そうでないディレクトリの中の候補は採る（PR #407 のレビューの必須 1）。
//
// req: FR-300
func TestWorldWritableStops(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	shared := filepath.Join(dir, "shared")
	mkdir(t, shared)
	write(t, filepath.Join(shared, ".venv", "bin", "python"), 0o755)
	mkdir(t, filepath.Join(shared, ".git"))
	write(t, filepath.Join(shared, "node_modules", ".bin", "biome"), 0o755)
	write(t, filepath.Join(dir, ".venv", "bin", "python"), 0o755) // 誰でも書けるディレクトリより上
	mine := filepath.Join(shared, "me", "scratch")
	mkdir(t, mine)
	chmod(t, shared, 0o1777)

	for _, file := range []string{filepath.Join(mine, "t.py"), filepath.Join(shared, "t.py")} {
		if v, err := PythonVenv(file); !errors.Is(err, ErrNotFound) {
			t.Errorf("PythonVenv(%s) = %+v, %v; want ErrNotFound", file, v, err)
		}
		if got, err := GitRoot(file); !errors.Is(err, ErrNotFound) {
			t.Errorf("GitRoot(%s) = %q, %v; want ErrNotFound", file, got, err)
		}
		if got, err := Find("biome", file, KindNode); !errors.Is(err, ErrNotFound) {
			t.Errorf("Find(biome, %s) = %+v, %v; want ErrNotFound", file, got, err)
		}
	}

	// 誰でも書けるディレクトリの下の、自分のディレクトリの中のものは採る（/tmp/proj/.venv）。
	proj := filepath.Join(shared, "proj")
	write(t, filepath.Join(proj, ".venv", "bin", "python"), 0o755)
	mkdir(t, filepath.Join(proj, ".git"))
	write(t, filepath.Join(proj, "node_modules", ".bin", "biome"), 0o755)
	file := filepath.Join(proj, "src", "x.py")
	if v, err := PythonVenv(file); err != nil || v.Dir != filepath.Join(proj, ".venv") {
		t.Errorf("PythonVenv(proj) = %+v, %v; want %q", v, err, filepath.Join(proj, ".venv"))
	}
	if got, err := GitRoot(file); err != nil || got != proj {
		t.Errorf("GitRoot(proj) = %q, %v; want %q", got, err, proj)
	}
	if got, err := Find("biome", file, KindNode); err != nil || got.Path != filepath.Join(proj, "node_modules", ".bin", "biome") {
		t.Errorf("Find(biome, proj) = %+v, %v", got, err)
	}
}

// Node は、近い node_modules/.bin（monorepo のメンバー）を先にし、権限の無いものを飛ばし、Git の根より上へはたどらない。
//
// req: FR-300
func TestFindNode(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	repo := filepath.Join(dir, "repo")
	mkdir(t, filepath.Join(repo, ".git"))
	member := filepath.Join(repo, "packages", "a")
	write(t, filepath.Join(member, "node_modules", ".bin", "biome"), 0o755)
	write(t, filepath.Join(repo, "node_modules", ".bin", "biome"), 0o755)
	write(t, filepath.Join(member, "node_modules", ".bin", "tsx"), 0o644) // 実行の権限が無い
	write(t, filepath.Join(repo, "node_modules", ".bin", "tsx"), 0o755)
	write(t, filepath.Join(dir, "node_modules", ".bin", "eslint"), 0o755) // Git の根の外
	file := filepath.Join(member, "src", "x.ts")

	tests := []struct {
		name string
		want string
	}{
		{"biome", filepath.Join(member, "node_modules", ".bin", "biome")},
		{"tsx", filepath.Join(repo, "node_modules", ".bin", "tsx")},
	}
	for _, tt := range tests {
		got, err := Find(tt.name, file, KindNode)
		if err != nil || got.Path != tt.want || got.Source != FromNodeModules {
			t.Errorf("Find(%s) = %+v, %v; want %q from node_modules", tt.name, got, err, tt.want)
		}
	}
	if got, err := Find("eslint", file, KindNode); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find(eslint above git root) = %+v, %v; want ErrNotFound", got, err)
	}
}

// Git の作業ツリーの外のファイルでは node_modules/.bin を探さず、PATH へ進む（resolver.lua の node_bin と同じ）。
// 壊れたシンボリックリンクの .git は Git の根とみなさない。
//
// req: FR-300
func TestFindNodeOutsideGit(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	write(t, filepath.Join(dir, "node_modules", ".bin", "biome"), 0o755)
	symlink(t, filepath.Join(dir, "nowhere"), filepath.Join(dir, ".git"))
	pathDir := filepath.Join(dir, "path")
	write(t, filepath.Join(pathDir, "biome"), 0o755)
	t.Setenv("PATH", pathDir)

	got, err := Find("biome", filepath.Join(dir, "sub", "x.ts"), KindNode)
	if err != nil || got.Path != filepath.Join(pathDir, "biome") || got.Source != FromPath {
		t.Errorf("Find = %+v, %v; want PATH", got, err)
	}
}

// worktree の中のファイルは、その worktree の根で止まり、外側のリポジトリの node_modules/.bin を使わない。
//
// req: FR-300
func TestFindNodeWorktree(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, ".git"))
	write(t, filepath.Join(dir, "node_modules", ".bin", "biome"), 0o755)
	wt := filepath.Join(dir, "wt")
	write(t, filepath.Join(wt, ".git"), 0o600)

	if got, err := Find("biome", filepath.Join(wt, "x.ts"), KindNode); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find in worktree = %+v, %v; want ErrNotFound", got, err)
	}
}

// PATH で見つけたものは、そのパスと FromPath を返す。KindPath はプロジェクトの中を探さない。
// 見つからない誤りは、ErrNotFound と exec.LookPath の誤りを包む。
//
// req: FR-300
func TestFindPath(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, ".git"))
	write(t, filepath.Join(dir, "node_modules", ".bin", "tool"), 0o755)
	write(t, filepath.Join(dir, ".venv", "bin", "python"), 0o755)
	write(t, filepath.Join(dir, ".venv", "bin", "tool"), 0o755)
	pathDir := filepath.Join(dir, "path")
	write(t, filepath.Join(pathDir, "tool"), 0o755)
	write(t, filepath.Join(pathDir, "noexec"), 0o644)
	t.Setenv("PATH", pathDir)
	file := filepath.Join(dir, "x")

	got, err := Find("tool", file, KindPath)
	if err != nil || got.Path != filepath.Join(pathDir, "tool") || got.Source != FromPath {
		t.Errorf("Find(KindPath) = %+v, %v; want PATH", got, err)
	}
	if _, err = Find("noexec", file, KindPath); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find(noexec) = %v; want ErrNotFound", err)
	}
	got, err = Find("missing", file, KindPath)
	if !errors.Is(err, ErrNotFound) || !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("Find(missing) = %+v, %v; want ErrNotFound wrapping the LookPath error", got, err)
	}
}

// 名前でないもの（パスの区切りを含む、空、. と ..）は探さない。
//
// req: FR-300
func TestFindInvalidName(t *testing.T) {
	isolate(t)
	for _, name := range []string{"", ".", "..", "bin/tool", "../tool", "/usr/bin/env"} {
		if got, err := Find(name, "/tmp/x", KindNode); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Find(%q) = %+v, %v; want ErrInvalidName", name, got, err)
		}
	}
}

// Python は、仮想環境の bin を PATH より先に探す。仮想環境に名前が無ければ、ほかの仮想環境を探さずに PATH へ進む。
//
// req: FR-300
func TestFindPython(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	write(t, filepath.Join(dir, ".venv", "bin", "python"), 0o755)
	write(t, filepath.Join(dir, ".venv", "bin", "ruff"), 0o755)
	member := filepath.Join(dir, "member")
	write(t, filepath.Join(member, ".venv", "bin", "python"), 0o755)
	write(t, filepath.Join(member, ".venv", "bin", "mypy"), 0o755)
	pathDir := filepath.Join(dir, "path")
	write(t, filepath.Join(pathDir, "ruff"), 0o755)
	t.Setenv("PATH", pathDir)

	got, err := Find("ruff", filepath.Join(dir, "x.py"), KindPython)
	if err != nil || got.Path != filepath.Join(dir, ".venv", "bin", "ruff") || got.Source != FromProjectVenv {
		t.Errorf("Find(ruff, root) = %+v, %v; want root venv", got, err)
	}
	// member の仮想環境に ruff が無いので、外側の仮想環境でなく PATH のものを使う。
	got, err = Find("ruff", filepath.Join(member, "x.py"), KindPython)
	if err != nil || got.Path != filepath.Join(pathDir, "ruff") || got.Source != FromPath {
		t.Errorf("Find(ruff, member) = %+v, %v; want PATH", got, err)
	}
	got, err = Find("mypy", filepath.Join(member, "x.py"), KindPython)
	if err != nil || got.Path != filepath.Join(member, ".venv", "bin", "mypy") {
		t.Errorf("Find(mypy, member) = %+v, %v; want member venv", got, err)
	}
}

// uv のワークスペースのメンバー（自分の .venv を持たない）のファイルから、根の .venv に届く。
// Parent は根で、探索の根（Root）とは別に引ける。
//
// req: FR-300
func TestPythonVenvWorkspace(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	ws := filepath.Join(dir, "ws")
	write(t, filepath.Join(ws, ".venv", "bin", "python"), 0o755)
	write(t, filepath.Join(ws, "pyproject.toml"), 0o600)
	member := filepath.Join(ws, "packages", "api")
	write(t, filepath.Join(member, "pyproject.toml"), 0o600)
	file := filepath.Join(member, "src", "api", "main.py")

	v, err := PythonVenv(file)
	if err != nil {
		t.Fatal(err)
	}
	want := Venv{Dir: filepath.Join(ws, ".venv"), Python: filepath.Join(ws, ".venv", "bin", "python"), Source: FromProjectVenv}
	if v != want {
		t.Errorf("PythonVenv = %+v, want %+v", v, want)
	}
	if v.Parent() != ws {
		t.Errorf("Parent = %q, want %q", v.Parent(), ws)
	}
	if root, err := Root(file, []string{"pyproject.toml"}); err != nil || root != member {
		t.Errorf("Root = %q, %v; want %q (cwd の候補は Parent と別)", root, err, member)
	}
}

// .venv がファイル（仮想環境の名前を書く流儀）か、bin/python が実行できなければ、同じディレクトリの venv、親の順に進む。
// 実行できるかは今の利用者で見る（持ち主の x のビットが無く、グループの x のビットだけが立つ自分のファイルは実行できない）。
//
// req: FR-300
func TestPythonVenvSkipsInvalid(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	write(t, filepath.Join(dir, ".venv", "bin", "python"), 0o755)
	a := filepath.Join(dir, "a")
	write(t, filepath.Join(a, ".venv"), 0o600) // ファイル
	write(t, filepath.Join(a, "venv", "bin", "python"), 0o755)
	b := filepath.Join(dir, "b")
	write(t, filepath.Join(b, ".venv", "bin", "python"), 0o644) // 実行の権限が無い
	c := filepath.Join(dir, "c")
	mkdir(t, filepath.Join(c, ".venv", "bin")) // bin/python が無い

	tests := []struct {
		dir, want string
	}{
		{a, filepath.Join(a, "venv")},
		{b, filepath.Join(dir, ".venv")},
		{c, filepath.Join(dir, ".venv")},
	}
	if os.Geteuid() != 0 {
		d := filepath.Join(dir, "d")
		write(t, filepath.Join(d, ".venv", "bin", "python"), 0o610) // 自分には実行できない
		tests = append(tests, struct{ dir, want string }{d, filepath.Join(dir, ".venv")})
	}
	for _, tt := range tests {
		if v, err := PythonVenv(filepath.Join(tt.dir, "x.py")); err != nil || v.Dir != tt.want {
			t.Errorf("PythonVenv(%s) = %+v, %v; want %q", tt.dir, v, err, tt.want)
		}
	}
}

// 候補を調べるときの権限の誤り（読めない .venv）は、外側の仮想環境へ進まずに、fs.ErrPermission を包んで返す。
//
// req: FR-300
func TestPermissionError(t *testing.T) {
	skipIfRoot(t)
	isolate(t)
	dir := tempDir(t)
	write(t, filepath.Join(dir, ".venv", "bin", "python"), 0o755)
	proj := filepath.Join(dir, "proj")
	write(t, filepath.Join(proj, ".venv", "bin", "python"), 0o755)
	chmod(t, filepath.Join(proj, ".venv"), 0o000)
	t.Cleanup(func() { chmod(t, filepath.Join(proj, ".venv"), 0o700) })
	file := filepath.Join(proj, "x.py")

	v, err := PythonVenv(file)
	if !errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrNotFound) {
		t.Errorf("PythonVenv = %+v, %v; want fs.ErrPermission", v, err)
	}
	if got, err := Find("ruff", file, KindPython); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("Find = %+v, %v; want fs.ErrPermission", got, err)
	}
}

// .venv がシンボリックリンクのディレクトリで、bin/python が元の python へのリンクでも、仮想環境として使い、リンクを解かないパスを返す。
//
// req: FR-300
func TestPythonVenvSymlinks(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	write(t, filepath.Join(dir, "usr", "bin", "python3"), 0o755)
	store := filepath.Join(dir, "store", "env")
	symlink(t, filepath.Join(dir, "usr", "bin", "python3"), filepath.Join(store, "bin", "python"))
	proj := filepath.Join(dir, "proj")
	symlink(t, store, filepath.Join(proj, ".venv"))

	v, err := PythonVenv(filepath.Join(proj, "x.py"))
	if err != nil || v.Python != filepath.Join(proj, ".venv", "bin", "python") || v.Parent() != proj {
		t.Errorf("PythonVenv = %+v, %v; want unresolved %q", v, err, filepath.Join(proj, ".venv", "bin", "python"))
	}
}

// プロジェクトの中の仮想環境を、有効にした仮想環境（絶対パスの VIRTUAL_ENV）より先にする。
// プロジェクトの中に無いときと、パスが空のときは VIRTUAL_ENV を使う。相対パスと、bin/python の無い VIRTUAL_ENV は使わない。
//
// req: FR-300
func TestPythonVenvVirtualEnv(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	active := filepath.Join(dir, "active")
	write(t, filepath.Join(active, "bin", "python"), 0o755)
	write(t, filepath.Join(active, "bin", "ruff"), 0o755)
	proj := filepath.Join(dir, "proj")
	write(t, filepath.Join(proj, ".venv", "bin", "python"), 0o755)
	write(t, filepath.Join(proj, ".venv", "bin", "ruff"), 0o755)
	file := filepath.Join(proj, "x.py")
	other := filepath.Join(dir, "other", "y.py") // 仮想環境の無いプロジェクト

	t.Setenv("VIRTUAL_ENV", active+"/")
	if v, err := PythonVenv(file); err != nil || v.Dir != filepath.Join(proj, ".venv") || v.Source != FromProjectVenv {
		t.Errorf("PythonVenv(project) = %+v, %v; want project venv", v, err)
	}
	if got, err := Find("ruff", file, KindPython); err != nil || got.Path != filepath.Join(proj, ".venv", "bin", "ruff") || got.Source != FromProjectVenv {
		t.Errorf("Find(ruff, project) = %+v, %v; want project venv", got, err)
	}
	if v, err := PythonVenv(other); err != nil || v.Dir != active || v.Source != FromVirtualEnv {
		t.Errorf("PythonVenv(other) = %+v, %v; want %q", v, err, active)
	}
	if got, err := Find("ruff", other, KindPython); err != nil || got.Path != filepath.Join(active, "bin", "ruff") || got.Source != FromVirtualEnv {
		t.Errorf("Find(ruff, other) = %+v, %v; want VIRTUAL_ENV", got, err)
	}
	if v, err := PythonVenv(""); err != nil || v.Dir != active {
		t.Errorf("PythonVenv(\"\") = %+v, %v; want %q", v, err, active)
	}

	for _, env := range []string{"active", filepath.Join(dir, "none")} {
		t.Setenv("VIRTUAL_ENV", env)
		if v, err := PythonVenv(other); !errors.Is(err, ErrNotFound) {
			t.Errorf("PythonVenv(VIRTUAL_ENV=%q) = %+v, %v; want ErrNotFound", env, v, err)
		}
	}
	t.Setenv("VIRTUAL_ENV", "")
	if v, err := PythonVenv(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("PythonVenv(\"\") = %+v, %v; want ErrNotFound", v, err)
	}
}

// ホームディレクトリで止めず、その上の仮想環境に届く。worktree の中では、worktree の仮想環境を外側のものより先にする。
//
// req: FR-300
func TestPythonVenvAboveHomeAndWorktree(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	write(t, filepath.Join(dir, ".venv", "bin", "python"), 0o755)
	home := filepath.Join(dir, "home")
	t.Setenv("HOME", home)
	if v, err := PythonVenv(filepath.Join(home, "p", "x.py")); err != nil || v.Dir != filepath.Join(dir, ".venv") {
		t.Errorf("PythonVenv(above HOME) = %+v, %v; want %q", v, err, filepath.Join(dir, ".venv"))
	}

	wt := filepath.Join(home, "worktrees", "feat")
	write(t, filepath.Join(wt, ".git"), 0o600)
	write(t, filepath.Join(wt, ".venv", "bin", "python"), 0o755)
	if v, err := PythonVenv(filepath.Join(wt, "src", "x.py")); err != nil || v.Dir != filepath.Join(wt, ".venv") {
		t.Errorf("PythonVenv(worktree) = %+v, %v; want %q", v, err, filepath.Join(wt, ".venv"))
	}
}
