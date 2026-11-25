package project

import (
	"errors"
	"os"
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

// LSP の拡張機能の findRoot（cmd/vi-imitation-ext-lsp の TestFindRoot）と同じ場面で、同じ根を返す。
// 見つからないときは ok が false で、findRoot の結果（ファイルのディレクトリ）は呼び出す側が filepath.Dir で作る。
//
// req: FR-300
func TestRootMatchesLSPFindRoot(t *testing.T) {
	dir := tempDir(t)
	sub := filepath.Join(dir, "a", "b")
	mkdir(t, sub)
	write(t, filepath.Join(dir, "a", "go.mod"), 0o600)

	if got, ok := Root(filepath.Join(sub, "x.go"), []string{"go.mod"}); !ok || got != filepath.Join(dir, "a") {
		t.Errorf("Root = %q, %v; want %q, true", got, ok, filepath.Join(dir, "a"))
	}
	if got, ok := Root(filepath.Join(sub, "x.go"), []string{noMarker}); ok {
		t.Errorf("Root without markers = %q, true; want not found", got)
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
		if got, ok := Root(file, markers); !ok || got != filepath.Join(dir, "m") {
			t.Errorf("Root(%v) = %q, %v; want %q", markers, got, ok, filepath.Join(dir, "m"))
		}
	}
	if got, ok := Root(file, []string{"go.work"}); !ok || got != dir {
		t.Errorf("Root(go.work) = %q, %v; want %q", got, ok, dir)
	}
}

// まだ無いディレクトリの新しいファイルからも、在る上のディレクトリの目印に届く。空のパスと空の目印は見つからない。
//
// req: FR-300
func TestRootMissingAndEmpty(t *testing.T) {
	dir := tempDir(t)
	write(t, filepath.Join(dir, "go.mod"), 0o600)
	if got, ok := Root(filepath.Join(dir, "new", "dir", "x.go"), []string{"go.mod"}); !ok || got != dir {
		t.Errorf("Root(new file) = %q, %v; want %q", got, ok, dir)
	}
	if got, ok := Root("", []string{"go.mod"}); ok {
		t.Errorf("Root(\"\") = %q, true; want not found", got)
	}
	if got, ok := Root(filepath.Join(dir, "x.go"), nil); ok {
		t.Errorf("Root(no markers) = %q, true; want not found", got)
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

	got, ok := Root(filepath.Join(dir, "link", "x.go"), []string{"go.mod", "package.json"})
	if !ok || got != filepath.Join(dir, "link") {
		t.Errorf("Root(link) = %q, %v; want %q", got, ok, filepath.Join(dir, "link"))
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

	if got, ok := GitRoot(filepath.Join(wt, "sub", "x.go")); !ok || got != wt {
		t.Errorf("GitRoot(worktree) = %q, %v; want %q", got, ok, wt)
	}
	if got, ok := GitRoot(filepath.Join(dir, "x.go")); !ok || got != dir {
		t.Errorf("GitRoot(repo) = %q, %v; want %q", got, ok, dir)
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

	if got, ok := Root(filepath.Join(home, "p", "x.go"), []string{"go.mod"}); !ok || got != dir {
		t.Errorf("Root = %q, %v; want %q (above HOME)", got, ok, dir)
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
//
// req: FR-300
func TestFindNodeOutsideGit(t *testing.T) {
	isolate(t)
	dir := tempDir(t)
	write(t, filepath.Join(dir, "node_modules", ".bin", "biome"), 0o755)
	pathDir := filepath.Join(dir, "path")
	write(t, filepath.Join(pathDir, "biome"), 0o755)
	t.Setenv("PATH", pathDir)

	got, err := Find("biome", filepath.Join(dir, "x.ts"), KindNode)
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
	if got, err := Find("noexec", file, KindPath); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find(noexec) = %+v, %v; want ErrNotFound", got, err)
	}
	if got, err := Find("missing", file, KindPath); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find(missing) = %+v, %v; want ErrNotFound", got, err)
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

	v, ok := PythonVenv(file)
	if !ok {
		t.Fatal("PythonVenv: not found")
	}
	want := Venv{Dir: filepath.Join(ws, ".venv"), Python: filepath.Join(ws, ".venv", "bin", "python"), Source: FromProjectVenv}
	if v != want {
		t.Errorf("PythonVenv = %+v, want %+v", v, want)
	}
	if v.Parent() != ws {
		t.Errorf("Parent = %q, want %q", v.Parent(), ws)
	}
	if root, ok := Root(file, []string{"pyproject.toml"}); !ok || root != member {
		t.Errorf("Root = %q, %v; want %q (cwd の候補は Parent と別)", root, ok, member)
	}
}

// .venv がファイル（仮想環境の名前を書く流儀）か、bin/python に実行の権限が無ければ、同じディレクトリの venv、親の順に進む。
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
	for _, tt := range tests {
		if v, ok := PythonVenv(filepath.Join(tt.dir, "x.py")); !ok || v.Dir != tt.want {
			t.Errorf("PythonVenv(%s) = %+v, %v; want %q", tt.dir, v, ok, tt.want)
		}
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

	v, ok := PythonVenv(filepath.Join(proj, "x.py"))
	if !ok || v.Python != filepath.Join(proj, ".venv", "bin", "python") || v.Parent() != proj {
		t.Errorf("PythonVenv = %+v, %v; want unresolved %q", v, ok, filepath.Join(proj, ".venv", "bin", "python"))
	}
}

// 有効にした仮想環境（絶対パスの VIRTUAL_ENV）を、ファイルの近くの仮想環境より先にする。ファイルが空でも使う。
// 相対パスと、bin/python の無い VIRTUAL_ENV は使わない。
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

	t.Setenv("VIRTUAL_ENV", active+"/")
	if v, ok := PythonVenv(file); !ok || v.Dir != active || v.Source != FromVirtualEnv {
		t.Errorf("PythonVenv(VIRTUAL_ENV) = %+v, %v; want %q", v, ok, active)
	}
	if v, ok := PythonVenv(""); !ok || v.Dir != active {
		t.Errorf("PythonVenv(\"\") = %+v, %v; want %q", v, ok, active)
	}
	if got, err := Find("ruff", file, KindPython); err != nil || got.Path != filepath.Join(active, "bin", "ruff") || got.Source != FromVirtualEnv {
		t.Errorf("Find(ruff) = %+v, %v; want VIRTUAL_ENV", got, err)
	}

	for _, env := range []string{"active", filepath.Join(dir, "none")} {
		t.Setenv("VIRTUAL_ENV", env)
		if v, ok := PythonVenv(file); !ok || v.Dir != filepath.Join(proj, ".venv") || v.Source != FromProjectVenv {
			t.Errorf("PythonVenv(VIRTUAL_ENV=%q) = %+v, %v; want project venv", env, v, ok)
		}
	}
	t.Setenv("VIRTUAL_ENV", "")
	if v, ok := PythonVenv(""); ok {
		t.Errorf("PythonVenv(\"\") = %+v, true; want not found", v)
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
	if v, ok := PythonVenv(filepath.Join(home, "p", "x.py")); !ok || v.Dir != filepath.Join(dir, ".venv") {
		t.Errorf("PythonVenv(above HOME) = %+v, %v; want %q", v, ok, filepath.Join(dir, ".venv"))
	}

	wt := filepath.Join(home, "worktrees", "feat")
	write(t, filepath.Join(wt, ".git"), 0o600)
	write(t, filepath.Join(wt, ".venv", "bin", "python"), 0o755)
	if v, ok := PythonVenv(filepath.Join(wt, "src", "x.py")); !ok || v.Dir != filepath.Join(wt, ".venv") {
		t.Errorf("PythonVenv(worktree) = %+v, %v; want %q", v, ok, filepath.Join(wt, ".venv"))
	}
}
