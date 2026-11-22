package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
)

// gitOut は、テストの一時のリポジトリ dir で git を実行し、標準出力の前後の空白を除いて返す。
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // テストの一時のリポジトリで git を実行する
	cmd.Dir = dir
	cmd.Env = testGitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// newBranchRepo は、main の上に feature のブランチを作ったリポジトリを作り、feature を取り出した状態で根を返す。
//
//   - main の最初のコミット: keep.txt、mod.txt、gone.txt、old.txt（名前を変えるもの）
//   - feature のコミット: mod.txt を変え、gone.txt を消し、old.txt を renamed.txt にし、added.txt を足す
//   - feature を作った後の main のコミット: mainonly.txt を足す（merge-base より後なので、merge-base との一覧には入らない）
//   - 作業ツリー: keep.txt を変え（コミットしない）、untracked.txt を置き、ignored.log（.gitignore で無視）を置く
func newBranchRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t, map[string]string{
		"keep.txt": "keep\n", "mod.txt": "one\ntwo\n", "gone.txt": "gone\n",
		"old.txt": "a\nb\nc\nd\ne\nf\n", ".gitignore": "*.log\n",
	})
	runGit(t, dir, "checkout", "-q", "-b", "feature")
	writeTestFiles(t, dir, map[string]string{"mod.txt": "one\nTWO\n", "added.txt": "added\n"})
	runGit(t, dir, "rm", "-q", "gone.txt")
	runGit(t, dir, "mv", "old.txt", "renamed.txt")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "feature")
	runGit(t, dir, "checkout", "-q", "main")
	writeTestFiles(t, dir, map[string]string{"mainonly.txt": "main\n"})
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "main")
	runGit(t, dir, "checkout", "-q", "feature")
	writeTestFiles(t, dir, map[string]string{"keep.txt": "KEEP\n", "untracked.txt": "u\n", "ignored.log": "x\n"})
	return dir
}

// labels は、一覧の行の表示の文字列を並べる。
func labels(cs []change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.label())
	}
	return out
}

// 引数を省いた比べる元は、既定のブランチ（ここでは origin が無いので main）と HEAD の merge-base で、一覧は merge-base と作業ツリーの差分である。
// 変更の種類（足した、変えた、消した、名前を変えた、未追跡）を出し、無視したファイルと、merge-base より後の main の変更は入れない。
//
// req: FR-250
func TestListChangesMergeBase(t *testing.T) {
	isolateGit(t)
	dir := newBranchRepo(t)
	cmp, err := resolveBase(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if cmp.label != "main...HEAD" || cmp.rev != gitOut(t, dir, "merge-base", "main", "HEAD") {
		t.Errorf("comparison = %+v", cmp)
	}
	got, err := listChanges(context.Background(), cmp)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A  added.txt", "D  gone.txt", "M  keep.txt", "M  mod.txt", "R  old.txt -> renamed.txt", "?  untracked.txt"}
	if !slices.Equal(labels(got), want) {
		t.Errorf("一覧 = %q, want %q", labels(got), want)
	}
}

// 比べる元の書き方（HEAD、ブランチ、コミット、<ブランチ>...、<ブランチ>...HEAD）と、使えない書き方の誤り。
//
// req: FR-250, FR-252
func TestResolveBaseSpecs(t *testing.T) {
	isolateGit(t)
	dir := newBranchRepo(t)
	ctx := context.Background()
	mainTip := gitOut(t, dir, "rev-parse", "main")
	base := gitOut(t, dir, "merge-base", "main", "HEAD")
	head := gitOut(t, dir, "rev-parse", "HEAD")
	for _, c := range []struct{ spec, rev, label string }{
		{"HEAD", head, "HEAD"},
		{"main", mainTip, "main"},
		{mainTip, mainTip, mainTip},
		{"main...", base, "main...HEAD"},
		{"main...HEAD", base, "main...HEAD"},
		{"  HEAD  ", head, "HEAD"},
	} {
		cmp, err := resolveBase(ctx, dir, c.spec)
		if err != nil {
			t.Errorf("%q: %v", c.spec, err)
			continue
		}
		if cmp.rev != c.rev || cmp.label != c.label || cmp.root != dir {
			t.Errorf("%q: %+v, want rev %s label %s", c.spec, cmp, c.rev, c.label)
		}
	}
	// HEAD との一覧は、作業ツリーの変更だけである（git.changesHead）。
	cmp, _ := resolveBase(ctx, dir, "HEAD")
	got, err := listChanges(ctx, cmp)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"M  keep.txt", "?  untracked.txt"}; !slices.Equal(labels(got), want) {
		t.Errorf("HEAD との一覧 = %q, want %q", labels(got), want)
	}
	// main の先との一覧には、main にだけある mainonly.txt が「消した」で入る。
	cmp, _ = resolveBase(ctx, dir, "main")
	got, _ = listChanges(ctx, cmp)
	if !slices.Contains(labels(got), "D  mainonly.txt") {
		t.Errorf("main との一覧 = %q, want D  mainonly.txt を含む", labels(got))
	}

	for _, c := range []struct{ spec, msg string }{
		{"main..HEAD", "範囲 main..HEAD は使えません"},
		{"main...feature", "右の側は HEAD だけにできます"},
		{"...HEAD", "左の側がありません"},
		{"-p", "- で始められません"},
		{"main HEAD", "1 つだけ"},
		{"nosuch", "比べる元 nosuch のコミットが見つかりません"},
		{"nosuch...", "比べる元 nosuch のコミットが見つかりません"},
		{"HEAD^{tree}", "のコミットが見つかりません"},
	} {
		if _, err := resolveBase(ctx, dir, c.spec); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%q: %v, want %q を含む誤り", c.spec, err, c.msg)
		}
	}
}

// git の作業ツリーの外、まだコミットの無いリポジトリ、既定のブランチの候補が無いリポジトリ、共通の祖先の無いブランチは、日本語の誤りにする。
//
// req: FR-252
func TestResolveBaseErrors(t *testing.T) {
	isolateGit(t)
	ctx := context.Background()
	if _, err := resolveBase(ctx, t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), "git の作業ツリーの中ではありません") {
		t.Errorf("リポジトリの外: %v", err)
	} else if _, ok := errors.AsType[*notWorkTreeError](err); !ok {
		t.Errorf("リポジトリの外の誤りが notWorkTreeError でない: %T", err)
	}
	// .git の中（COMMIT_EDITMSG の置き場所）も作業ツリーの外である。呼び出し側は作業ディレクトリで試し直す。
	inGitDir := newRepo(t, map[string]string{"a.txt": "a\n"})
	if _, err := resolveBase(ctx, filepath.Join(inGitDir, ".git"), "HEAD"); err == nil {
		t.Error(".git の中で一覧を作れた")
	} else if _, ok := errors.AsType[*notWorkTreeError](err); !ok {
		t.Errorf(".git の中の誤りが notWorkTreeError でない: %v", err)
	}
	empty := t.TempDir()
	runGit(t, empty, "init", "-q", "-b", "main")
	if _, err := resolveBase(ctx, empty, ""); err == nil || !strings.Contains(err.Error(), "HEAD のコミットがありません") {
		t.Errorf("コミットの無いリポジトリ: %v", err)
	}
	trunk := newRepo(t, map[string]string{"a.txt": "a\n"})
	runGit(t, trunk, "branch", "-m", "main", "trunk")
	if _, err := resolveBase(ctx, trunk, ""); err == nil || !strings.Contains(err.Error(), "比べる元のブランチ") {
		t.Errorf("既定のブランチが無い: %v", err)
	}
	runGit(t, trunk, "checkout", "-q", "--orphan", "other")
	runGit(t, trunk, "commit", "-q", "-m", "orphan")
	if _, err := resolveBase(ctx, trunk, "trunk..."); err == nil || !strings.Contains(err.Error(), "共通の祖先がありません") {
		t.Errorf("共通の祖先が無い: %v", err)
	}
}

// clone したリポジトリでは、既定の比べる元は origin/HEAD（origin の既定のブランチ）との merge-base で、見出しにはそのブランチの名前を出す。
//
// req: FR-250
func TestResolveBaseOriginHead(t *testing.T) {
	isolateGit(t)
	upstream := newRepo(t, map[string]string{"a.txt": "a\n"})
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(clone), "clone", "-q", upstream, clone)
	runGit(t, clone, "checkout", "-q", "-b", "topic")
	writeTestFiles(t, clone, map[string]string{"a.txt": "A\n"})
	runGit(t, clone, "commit", "-q", "-am", "topic")
	cmp, err := resolveBase(context.Background(), clone, "")
	if err != nil {
		t.Fatal(err)
	}
	if cmp.label != "origin/main...HEAD" || cmp.rev != gitOut(t, clone, "rev-parse", "origin/main") {
		t.Errorf("comparison = %+v", cmp)
	}
}

// 浅いクローンで merge-base が見えないときは、浅いことと履歴の取り方を案内する。名前は見出しと同じく origin/HEAD の指すブランチにする
// （PR #382 のレビューの推奨 1）。
//
// req: FR-252
func TestResolveBaseShallow(t *testing.T) {
	isolateGit(t)
	upstream := newRepo(t, map[string]string{"a.txt": "a\n"})
	runGit(t, upstream, "checkout", "-q", "-b", "feature")
	writeTestFiles(t, upstream, map[string]string{"f.txt": "f\n"})
	runGit(t, upstream, "add", "-A")
	runGit(t, upstream, "commit", "-q", "-m", "feature")
	runGit(t, upstream, "checkout", "-q", "main")
	writeTestFiles(t, upstream, map[string]string{"m.txt": "m\n"})
	runGit(t, upstream, "add", "-A")
	runGit(t, upstream, "commit", "-q", "-m", "main")
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(clone), "clone", "-q", "--depth", "1", "--no-single-branch", "file://"+upstream, clone)
	runGit(t, clone, "checkout", "-q", "feature")
	_, err := resolveBase(context.Background(), clone, "")
	if err == nil || !strings.Contains(err.Error(), "origin/main と HEAD の共通の祖先が見つかりません") || !strings.Contains(err.Error(), "git fetch --unshallow") {
		t.Errorf("浅いクローン: %v", err)
	}
}

// 追跡していない入れ子のリポジトリは、末尾に / の付いた行で出して印を付け、モードだけを変えたファイルは、行にモードの変化を出す
// （PR #382 のレビューの推奨 2、3）。
//
// req: FR-250, FR-252
func TestListChangesNestedAndMode(t *testing.T) {
	isolateGit(t)
	dir := newRepo(t, map[string]string{"exec.sh": "echo\n", "plain.txt": "p\n"})
	runGit(t, dir, "update-index", "--chmod=+x", "exec.sh")
	if err := os.Chmod(filepath.Join(dir, "exec.sh"), 0o700); err != nil { //nolint:gosec // テストの一時のファイルに実行の権限を付ける
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-q", "-m", "chmod")
	runGit(t, dir, "init", "-q", "-b", "main", "nested")
	cmp, err := resolveBase(context.Background(), dir, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := listChanges(context.Background(), cmp)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"M  exec.sh（モード 100644 -> 100755）", "?  nested/"}
	if !slices.Equal(labels(got), want) {
		t.Fatalf("一覧 = %q, want %q", labels(got), want)
	}
	if got[0].nested || !got[1].nested {
		t.Errorf("nested = %v, %v", got[0].nested, got[1].nested)
	}
}

// worktree（git worktree add）の中では、その worktree の根と HEAD で一覧を作る。本体の環境に別のリポジトリの GIT_DIR などが残っていても
// （本体を git commit の GIT_EDITOR にしたとき）、ディレクトリを含む作業ツリーを読む。
//
// req: FR-250, FR-252
func TestResolveBaseWorktree(t *testing.T) {
	isolateGit(t)
	dir := newBranchRepo(t)
	runGit(t, dir, "checkout", "-q", "main")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, dir, "worktree", "add", "-q", wt, "feature")
	writeTestFiles(t, wt, map[string]string{"sub/wt.txt": "w\n"})
	other := newRepo(t, map[string]string{"x.txt": "x\n"})
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	cmp, err := resolveBase(context.Background(), filepath.Join(wt, "sub"), "")
	if err != nil {
		t.Fatal(err)
	}
	if cmp.root != wt || cmp.label != "main...HEAD" {
		t.Errorf("comparison = %+v, want root %s", cmp, wt)
	}
	got, err := listChanges(context.Background(), cmp)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A  added.txt", "D  gone.txt", "M  mod.txt", "R  old.txt -> renamed.txt", "?  sub/wt.txt"}
	if !slices.Equal(labels(got), want) {
		t.Errorf("一覧 = %q, want %q", labels(got), want)
	}
}

// git diff --raw -z の、サブモジュール（モード 160000）とシンボリックリンク（120000）の行、名前の変更とコピーの 2 つのパスを読む。
// 利用者の git の設定 diff.relative でサブディレクトリの中だけになったり、パスがそこからの相対になったりしない。
func TestParseRaw(t *testing.T) {
	raw := ":160000 160000 1111111 2222222 M\x00sub\x00" +
		":000000 120000 0000000 3333333 A\x00link\x00" +
		":100644 100644 4444444 4444444 R087\x00a\x00b\x00" +
		":100644 100644 4444444 4444444 C100\x00c\x00d\x00" +
		":100644 120000 5555555 6666666 T\x00t\x00"
	got, err := parseRaw([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := []change{
		{kind: kindModified, path: "sub", old: "sub", submodule: true},
		{kind: kindAdded, path: "link", symlink: true},
		{kind: kindRenamed, path: "b", old: "a"},
		{kind: kindAdded, path: "d", old: "c"},
		{kind: kindType, path: "t", old: "t", symlink: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("parseRaw = %+v, want %+v", got, want)
	}
	for _, bad := range []string{":100644 M\x00a\x00", ":100644 100644 1 2 R100\x00a\x00"} {
		if _, perr := parseRaw([]byte(bad)); perr == nil {
			t.Errorf("%q を読めた", bad)
		}
	}

	isolateGit(t)
	dir := newRepo(t, map[string]string{"top.txt": "t\n", "sub/a.txt": "a\n"})
	runGit(t, dir, "config", "diff.relative", "true")
	writeTestFiles(t, dir, map[string]string{"top.txt": "T\n", "sub/a.txt": "A\n"})
	cmp, err := resolveBase(context.Background(), filepath.Join(dir, "sub"), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := listChanges(context.Background(), cmp)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"M  sub/a.txt", "M  top.txt"}; !slices.Equal(labels(cs), want) {
		t.Errorf("diff.relative の下の一覧 = %q, want %q", labels(cs), want)
	}
}

// 比べる元の版は、作業ツリーへ取り出すときと同じ変換（.gitattributes の eol）を通し、末尾の改行を 1 つ除いて読む。
// 不正な UTF-8 を含む版と、比べる元に無いパスは、日本語の誤りにする。
//
// req: FR-251
func TestReadBlob(t *testing.T) {
	isolateGit(t)
	dir := newRepo(t, map[string]string{".gitattributes": "*.crlf text eol=crlf\n", "w.crlf": "one\ntwo\n", "bad.txt": "a\xffb\n", "sub/x.txt": "x\n\n"})
	cmp, err := resolveBase(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"w.crlf": "one\r\ntwo\r", "sub/x.txt": "x\n"} {
		got, err := readBlob(context.Background(), cmp, name)
		if err != nil || got != want {
			t.Errorf("%s: %q, %v, want %q", name, got, err, want)
		}
	}
	if _, err := readBlob(context.Background(), cmp, "bad.txt"); err == nil || !strings.Contains(err.Error(), "不正な UTF-8") {
		t.Errorf("bad.txt: %v", err)
	}
	if _, err := readBlob(context.Background(), cmp, "none.txt"); err == nil || !strings.Contains(err.Error(), "HEAD の none.txt を読めません") {
		t.Errorf("none.txt: %v", err)
	}
}

// 絞り込みは、パスの部分に、入力を空白で区切ったすべての語の文字が順に現れる行を残し、一致した文字に色を付ける。
// 大文字の無い語は大文字と小文字を区別しない。変更の種類の文字（行の先頭）には一致させない。
//
// req: FR-250
func TestMatchLabel(t *testing.T) {
	for _, c := range []struct {
		label, query string
		ok           bool
		spans        [][2]int
	}{
		{"M  cmd/git/picker.go", "", true, nil},
		{"M  cmd/git/picker.go", "pick", true, [][2]int{{11, 15}}},
		{"M  cmd/git/picker.go", "cg pk", true, [][2]int{{3, 4}, {7, 8}, {11, 12}, {14, 15}}},
		{"M  cmd/git/picker.go", "PICK", false, nil},
		{"M  cmd/Git/x.go", "Git", true, [][2]int{{7, 10}}},
		{"M  cmd/git/x.go", "M", false, nil},
		{"M  日本語/ファイル.txt", "ファイル", true, [][2]int{{13, 25}}},
		{"M  a.go", "a b", false, nil},
	} {
		spans, ok := matchLabel(c.label, parseQuery(c.query))
		if ok != c.ok {
			t.Errorf("%q %q: ok = %v", c.label, c.query, ok)
			continue
		}
		var got [][2]int
		for _, s := range spans {
			got = append(got, [2]int{s.Start, s.End})
			if s.Style != "finder.match" {
				t.Errorf("style = %q", s.Style)
			}
		}
		if !slices.Equal(got, c.spans) {
			t.Errorf("%q %q: spans = %v, want %v", c.label, c.query, got, c.spans)
		}
	}
}

// 一覧の部品は、見出しに比べる元と「一致した数/候補の数」を出し、項目に、作業ツリーの側のファイル（アイコン）と、
// 作業ツリーにあるファイルのプレビューを付ける。送る項目は pickerLimit までである。
//
// req: FR-250
func TestPickerComponent(t *testing.T) {
	cmp := comparison{root: "/r", rev: "abc", label: "main...HEAD"}
	p := &changesPicker{cmp: cmp}
	for i := range pickerLimit + 5 {
		p.changes = append(p.changes, change{kind: kindModified, path: fmt.Sprintf("f%03d.go", i), old: fmt.Sprintf("f%03d.go", i)})
	}
	p.changes = append(p.changes, change{kind: kindDeleted, path: "zz-gone.txt", old: "zz-gone.txt"})
	for _, c := range p.changes {
		p.labels = append(p.labels, c.label())
	}
	c := p.component("")
	if c.Kind != ext.ViewFinder || !c.Preview || len(c.Items) != pickerLimit || c.Title != fmt.Sprintf("git の差分 main...HEAD  %d/%d", pickerLimit+6, pickerLimit+6) {
		t.Fatalf("component = kind %s preview %v items %d title %q", c.Kind, c.Preview, len(c.Items), c.Title)
	}
	if it := c.Items[0]; it.File == nil || it.File.Path != "/r/f000.go" || it.Preview == nil || it.Preview.Path != "/r/f000.go" || it.ID != "0" {
		t.Errorf("items[0] = %+v", it)
	}
	c = p.component("gone")
	if len(c.Items) != 1 || c.Items[0].Preview != nil || c.Items[0].Label != "D  zz-gone.txt" || c.Title != fmt.Sprintf("git の差分 main...HEAD  1/%d", pickerLimit+6) {
		t.Errorf("消したファイルの項目 = %+v, title %q", c.Items, c.Title)
	}
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
}

// 改行と不正な UTF-8 を含むパスも、部品に入れられる表示の文字列にする。
func TestCleanLabel(t *testing.T) {
	if got := cleanLabel("M  a\nb\r\xff.txt"); got != "M  a b �.txt" {
		t.Errorf("cleanLabel = %q", got)
	}
	if err := (&ext.ViewComponent{Kind: ext.ViewFinder, Items: []ext.ViewItem{{ID: "0", Label: cleanLabel("M  a\nb")}}}).Validate(); err != nil {
		t.Error(err)
	}
}
