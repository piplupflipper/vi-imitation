package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// buildWait は、パーサの取得とビルドを待つ時間の上限である。C コンパイラは -race の影響を受けないが、CI の機械は遅いことがある。
const buildWait = 120 * time.Second

// gitTestEnv は、テストの git の子プロセスに渡す環境変数である。利用者の git の設定（署名やフック）を読まない。
var gitTestEnv = []string{
	"GIT_CONFIG_GLOBAL=" + os.DevNull,
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_AUTHOR_NAME=test",
	"GIT_AUTHOR_EMAIL=test@example.com",
	"GIT_COMMITTER_NAME=test",
	"GIT_COMMITTER_EMAIL=test@example.com",
}

// TestMain は、XDG_CONFIG_HOME と XDG_DATA_HOME を一時ディレクトリに向けてからテストを動かす。
// 既定の置き場所（defaultParserEnv）を使うテストが、利用者のロックファイルとパーサの置き場所に触れないようにする。
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "treesitter-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir) //nolint:errcheck // テストの一時ディレクトリの後始末
	for k, v := range map[string]string{"XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_DATA_HOME": filepath.Join(dir, "data")} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return m.Run()
}

// TestParseSettings は、設定の parsers の読み方と、誤りのある項目だけを使わないことを確かめる。
//
// req: FR-142
func TestParseSettings(t *testing.T) {
	s, problems := parseSettings(json.RawMessage(`{
  "compiler": "clang",
  "unknown": 1,
  "parsers": {
    "python": { "repo": "https://github.com/tree-sitter/tree-sitter-python", "version": "v0.25.0", "fileTypes": ["python", "python"] },
    "ts": { "repo": "/srv/tree-sitter-typescript", "version": "main", "fileTypes": ["typescript", "shared"], "path": "typescript", "queries": ["queries/highlights.scm", "extra.scm"] },
    "other": { "repo": "https://example.com/other", "version": "v1", "fileTypes": ["shared", "other"] },
    "norepo": { "version": "v1", "fileTypes": ["x"] },
    "relative": { "repo": "src/grammar", "version": "v1", "fileTypes": ["x"] },
    "dash": { "repo": "/r", "version": "--upload-pack=x", "fileTypes": ["x"] },
    "notypes": { "repo": "/r", "version": "v1", "fileTypes": [] },
    "emptytype": { "repo": "/r", "version": "v1", "fileTypes": [""] },
    "escape": { "repo": "/r", "version": "v1", "fileTypes": ["x"], "path": "../up" },
    "absquery": { "repo": "/r", "version": "v1", "fileTypes": ["x"], "queries": ["/etc/passwd"] },
    "field": { "repo": "/r", "version": "v1", "fileTypes": ["x"], "tag": "v1" },
    "bad/name": { "repo": "/r", "version": "v1", "fileTypes": ["x"] }
  }
}`))
	if s.compiler != "clang" {
		t.Errorf("compiler = %q, want clang", s.compiler)
	}
	var names []string
	got := map[string]parserSpec{}
	for _, p := range s.parsers {
		names = append(names, p.name)
		got[p.name] = p
	}
	if want := []string{"other", "python", "ts"}; !slices.Equal(names, want) {
		t.Fatalf("parsers = %v, want %v", names, want)
	}
	// "shared" は other と ts の両方が持つので、どちらにも使わない。重なった名前は 1 つにする。
	if ft := got["python"].fileTypes; !slices.Equal(ft, []string{"python"}) {
		t.Errorf("python.fileTypes = %v", ft)
	}
	if ft := got["ts"].fileTypes; !slices.Equal(ft, []string{"typescript"}) {
		t.Errorf("ts.fileTypes = %v", ft)
	}
	if ft := got["other"].fileTypes; !slices.Equal(ft, []string{"other"}) {
		t.Errorf("other.fileTypes = %v", ft)
	}
	if ts := got["ts"]; ts.path != "typescript" || !slices.Equal(ts.queries, []querySource{{file: "queries/highlights.scm"}, {file: "extra.scm"}}) || ts.repo != "/srv/tree-sitter-typescript" || ts.version != "main" {
		t.Errorf("ts = %+v", ts)
	}
	// 誤りのある項目の名前も listed に入れ、ロックファイルの記録を消さない。
	for _, n := range []string{"python", "norepo", "field", "bad/name"} {
		if !s.listed[n] {
			t.Errorf("listed に %s が無い", n)
		}
	}
	want := []string{
		"知らない項目 unknown",
		"パーサ absquery を使わない",
		"パーサ bad/name を使わない",
		"パーサ dash を使わない",
		"パーサ emptytype を使わない",
		"パーサ escape を使わない",
		"パーサ field を使わない",
		"パーサ norepo を使わない",
		"パーサ notypes を使わない",
		"パーサ relative を使わない",
		"ファイルの種類 shared を複数のパーサ（other、ts）が持つ",
	}
	if len(problems) != len(want) {
		t.Fatalf("problems = %q", problems)
	}
	for i, w := range want {
		if !strings.Contains(problems[i], w) {
			t.Errorf("problems[%d] = %q, want it to contain %q", i, problems[i], w)
		}
	}

	for _, raw := range []string{``, `null`, `{}`} {
		s, problems := parseSettings(json.RawMessage(raw))
		if len(problems) != 0 || len(s.parsers) != 0 || s.compiler != defaultCompiler {
			t.Errorf("parseSettings(%q) = %+v, %q", raw, s, problems)
		}
	}
	for _, raw := range []string{`[]`, `{"parsers": []}`, `{"compiler": ""}`} {
		if _, problems := parseSettings(json.RawMessage(raw)); len(problems) != 1 {
			t.Errorf("parseSettings(%q) problems = %q, want 1", raw, problems)
		}
	}
}

// TestParseSettingsInherits は、queries にほかのパーサのファイルを {"parser", "file"} で書けることと、
// 継ぐパーサが誤り無く書かれていなければ、継ぐ側のパーサを（継ぐ側を継ぐパーサも続けて）使わないことを確かめる。
//
// req: FR-328
func TestParseSettingsInherits(t *testing.T) {
	s, problems := parseSettings(json.RawMessage(`{
  "parsers": {
    "javascript": { "repo": "/r/js", "version": "v1", "fileTypes": ["javascript"] },
    "typescript": { "repo": "/r/ts", "version": "v1", "fileTypes": ["typescript"], "path": "typescript",
      "queries": ["queries/highlights.scm", { "parser": "javascript", "file": "queries/highlights.scm" }] },
    "tsx": { "repo": "/r/ts", "version": "v1", "fileTypes": ["typescriptreact"], "path": "tsx",
      "queries": ["queries/highlights.scm", { "parser": "javascript", "file": "queries/highlights-jsx.scm" }, { "parser": "javascript", "file": "queries/highlights.scm" }] },
    "missing": { "repo": "/r/m", "version": "v1", "fileTypes": ["m"], "queries": [{ "parser": "nothere", "file": "q.scm" }] },
    "broken": { "repo": "/r/b", "version": "v1", "fileTypes": [] },
    "onbroken": { "repo": "/r/c", "version": "v1", "fileTypes": ["c1"], "queries": [{ "parser": "broken", "file": "q.scm" }] },
    "chain": { "repo": "/r/d", "version": "v1", "fileTypes": ["d1"], "queries": [{ "parser": "onbroken", "file": "q.scm" }] },
    "self": { "repo": "/r/s", "version": "v1", "fileTypes": ["s1"], "queries": [{ "parser": "self", "file": "q.scm" }] },
    "escape": { "repo": "/r/e", "version": "v1", "fileTypes": ["e1"], "queries": [{ "parser": "javascript", "file": "../x.scm" }] },
    "extra": { "repo": "/r/x", "version": "v1", "fileTypes": ["x1"], "queries": [{ "parser": "javascript", "file": "q.scm", "tag": "v1" }] },
    "number": { "repo": "/r/n", "version": "v1", "fileTypes": ["n1"], "queries": [1] }
  }
}`))
	var names []string
	got := map[string]parserSpec{}
	for _, p := range s.parsers {
		names = append(names, p.name)
		got[p.name] = p
	}
	if want := []string{"javascript", "tsx", "typescript"}; !slices.Equal(names, want) {
		t.Fatalf("parsers = %v, want %v (problems %q)", names, want, problems)
	}
	if q := got["tsx"].queries; !slices.Equal(q, []querySource{
		{file: "queries/highlights.scm"},
		{parser: "javascript", file: "queries/highlights-jsx.scm"},
		{parser: "javascript", file: "queries/highlights.scm"},
	}) {
		t.Errorf("tsx.queries = %+v", q)
	}
	if in := got["tsx"].inherits(); !slices.Equal(in, []string{"javascript"}) {
		t.Errorf("tsx.inherits() = %v", in)
	}
	want := []string{
		"パーサ broken を使わない",
		"パーサ escape を使わない",
		"パーサ extra を使わない",
		"パーサ number を使わない",
		"パーサ self を使わない",
		"パーサ missing を使わない: queries の継ぐパーサ nothere",
		"パーサ onbroken を使わない: queries の継ぐパーサ broken",
		"パーサ chain を使わない: queries の継ぐパーサ onbroken",
	}
	if len(problems) != len(want) {
		t.Fatalf("problems = %q", problems)
	}
	for i, w := range want {
		if !strings.Contains(problems[i], w) {
			t.Errorf("problems[%d] = %q, want it to contain %q", i, problems[i], w)
		}
	}
}

// TestBuildKeyWithoutInherits は、継ぐパーサの無いパーサのビルドの鍵が、FR-328 より前と同じ値であることを確かめる。
// 変わると、利用者が前にビルドしたパーサ（Python など）が、版を変えていなくても黙ってビルドし直しになる。
// 期待値は、FR-328 より前の buildKey（コミット、path、queries の文字列を NUL で区切って sha256 にし、16 進の先頭 16 文字）で、
// シェルの printf 'commit\0path\0...\0' | sha256sum で求めた。
// 継ぐパーサがあれば、そのコミットで鍵が変わることも確かめる（FR-329）。
//
// req: FR-329
func TestBuildKeyWithoutInherits(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		spec parserSpec
		want string
	}{
		{parserSpec{name: "python", path: "."}, "9d3cb9554df66184"},
		{parserSpec{name: "ts", path: "typescript", queries: []querySource{{file: "queries/highlights.scm"}, {file: "extra.scm"}}}, "3c0ad4dd242d7be8"},
	} {
		if got := buildKey(c.spec, commit, nil); got != c.want {
			t.Errorf("buildKey(%+v) = %s, want %s", c.spec, got, c.want)
		}
	}
	spec := parserSpec{name: "ts", path: "typescript", queries: []querySource{{file: "queries/highlights.scm"}, {parser: "javascript", file: "queries/highlights.scm"}}}
	k1 := buildKey(spec, commit, map[string]string{"javascript": strings.Repeat("a", 40)})
	k2 := buildKey(spec, commit, map[string]string{"javascript": strings.Repeat("b", 40)})
	if k1 == k2 {
		t.Errorf("継ぐパーサのコミットが違っても鍵が同じ: %s", k1)
	}
}

var (
	jsonGrammarOnce sync.Once
	jsonGrammarDir  string
	errJSONGrammar  error
)

// jsonGrammar は、テストの取得元に写す文法のソースとして、同梱の tree-sitter-json のモジュールのディレクトリを返す。
// モジュールは src/parser.c と src/grammar.json と queries/highlights.scm を含む。
func jsonGrammar(t *testing.T) string {
	t.Helper()
	jsonGrammarOnce.Do(func() {
		out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/tree-sitter/tree-sitter-json").Output()
		jsonGrammarDir, errJSONGrammar = strings.TrimSpace(string(out)), err
	})
	if errJSONGrammar != nil || jsonGrammarDir == "" {
		t.Fatalf("tree-sitter-json のモジュールが見つからない: %v", errJSONGrammar)
	}
	return jsonGrammarDir
}

// grammarRepo は、テストの取得元の git のリポジトリである（手元の一時ディレクトリ）。
type grammarRepo struct {
	t   *testing.T
	dir string
}

// newGrammarRepo は、tree-sitter-json の文法を sub の下の src/ に、ハイライトのクエリを最上位の queries/ に置いてコミットし、
// タグ v1 を付けたリポジトリを作る。files は、その上に足すか置き換えるファイル（リポジトリの中のパスから中身）である。
func newGrammarRepo(t *testing.T, sub string, files map[string]string) *grammarRepo {
	t.Helper()
	r := &grammarRepo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet", "--initial-branch=main")
	g := jsonGrammar(t)
	for from, to := range map[string]string{
		"src/parser.c":             filepath.Join(sub, "src", "parser.c"),
		"src/grammar.json":         filepath.Join(sub, "src", "grammar.json"),
		"src/tree_sitter/parser.h": filepath.Join(sub, "src", "tree_sitter", "parser.h"),
		"src/tree_sitter/alloc.h":  filepath.Join(sub, "src", "tree_sitter", "alloc.h"),
		"src/tree_sitter/array.h":  filepath.Join(sub, "src", "tree_sitter", "array.h"),
		"queries/highlights.scm":   filepath.Join("queries", "highlights.scm"),
	} {
		data, err := os.ReadFile(filepath.Join(g, from))
		if err != nil {
			t.Fatal(err)
		}
		r.write(to, string(data))
	}
	r.commit(files)
	r.git("tag", "v1")
	return r
}

func (r *grammarRepo) write(path, body string) {
	r.t.Helper()
	p := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil { //nolint:gosec // G703: テストの一時ディレクトリの中に、テストが決めた名前で書く
		r.t.Fatal(err)
	}
}

// commit は、files を書いてコミットし、コミットのハッシュを返す。
func (r *grammarRepo) commit(files map[string]string) string {
	r.t.Helper()
	for path, body := range files {
		r.write(path, body)
	}
	r.git("add", "-A")
	r.git("commit", "--quiet", "--allow-empty", "-m", "commit")
	return r.git("rev-parse", "HEAD")
}

func (r *grammarRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: テストの取得元を作る git の引数はテストが組み立てたもの
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), gitTestEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// testParserEnv は、置き場所とロックファイルを一時ディレクトリに置く parserEnv を返す。
func testParserEnv(t *testing.T) parserEnv {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	return parserEnv{
		dataDir:      filepath.Join(dir, "data", "treesitter"),
		lockPath:     filepath.Join(dir, "config", ParserLockFileName),
		env:          gitTestEnv,
		fetchTimeout: buildWait,
		buildTimeout: buildWait,
	}
}

// settingsJSON は、parsers の設定の JSON を作る。
func settingsJSON(t *testing.T, compiler string, parsers map[string]map[string]any) json.RawMessage {
	t.Helper()
	v := map[string]any{"parsers": parsers}
	if compiler != "" {
		v["compiler"] = compiler
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// connectParsers は、env の置き場所と settings の設定で Server を動かす。
func connectParsers(t *testing.T, env parserEnv, settings json.RawMessage) (
	*rpc.Conn, <-chan ext.DecorationSetParams, <-chan ext.MessageShowParams, <-chan ext.ProgressReportParams,
) {
	t.Helper()
	return connectWithProgress(t, func(h *highlighter) { h.env = env }, settings)
}

// waitProgress は、本文が text を含む progress/report が届くまで待ち、それまでに届いたものを返す。
func waitProgress(t *testing.T, ch <-chan ext.ProgressReportParams, text string) []ext.ProgressReportParams {
	t.Helper()
	var seen []ext.ProgressReportParams
	deadline := time.After(buildWait)
	for {
		select {
		case p := <-ch:
			seen = append(seen, p)
			if strings.Contains(p.Message, text) {
				return seen
			}
		case <-deadline:
			t.Fatalf("progress/report containing %q did not arrive; got %+v", text, seen)
			return nil
		}
	}
}

// openBuffer は、buffer/open を送る。
func openBuffer(t *testing.T, conn *rpc.Conn, id ext.BufferID, fileType, text string) {
	t.Helper()
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: id, Version: 1, FileType: fileType, Text: text}); err != nil {
		t.Fatal(err)
	}
}

// receiveBuffer は、バッファ id の decoration/set が届くまで待って返す。ほかのバッファの一覧は others に足す。
func receiveBuffer(t *testing.T, got <-chan ext.DecorationSetParams, id ext.BufferID, others *[]ext.DecorationSetParams) ext.DecorationSetParams {
	t.Helper()
	deadline := time.After(buildWait)
	for {
		select {
		case p := <-got:
			if p.Buffer == id {
				return p
			}
			if others != nil {
				*others = append(*others, p)
			}
		case <-deadline:
			t.Fatalf("buffer %d: decoration/set did not arrive", id)
			return ext.DecorationSetParams{}
		}
	}
}

// waitMessage は、text を含む message/show が届くまで待ち、それまでに届いたものを返す。
func waitMessage(t *testing.T, msgs <-chan ext.MessageShowParams, text string) []ext.MessageShowParams {
	t.Helper()
	var seen []ext.MessageShowParams
	deadline := time.After(buildWait)
	for {
		select {
		case m := <-msgs:
			seen = append(seen, m)
			if strings.Contains(m.Text, text) {
				return seen
			}
		case <-deadline:
			t.Fatalf("message/show containing %q did not arrive; got %+v", text, seen)
			return nil
		}
	}
}

// drainMessages は、いま届いている message/show をすべて返す。
func drainMessages(msgs <-chan ext.MessageShowParams) []ext.MessageShowParams {
	var out []ext.MessageShowParams
	for {
		select {
		case m := <-msgs:
			out = append(out, m)
		default:
			return out
		}
	}
}

// styleAt は、decs のうち、位置 p を含む範囲の色を返す。無ければ空である。
func styleAt(decs []ext.Decoration, p ext.Position) string {
	for _, d := range decs {
		if !lessPos(p, d.Range.Start) && lessPos(p, d.Range.End) {
			return d.Style
		}
	}
	return ""
}

// readLockFile は、パーサのロックファイルを読む。
func readLockFile(t *testing.T, path string) parserLock {
	t.Helper()
	l, err := readParserLock(path)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestUserParserFetchBuildAndReuse は、設定に並べたパーサを、そのファイルの種類のバッファを初めて開いたときに、
// 手元の git のリポジトリから取得し、C コンパイラでビルドして色を付けることを確かめる。
// 取得したコミットはロックファイルに記録し、次の起動（新しい highlighter）では、git も C コンパイラも使わずに同じものを読み込む。
// 版を変えたら取得し直し、ロックファイルの記録を替え、前のビルドを消す。
// 文法は src/ のあるディレクトリ（path）の下に置き、外部スキャナー（scanner.c）も一緒にコンパイルする。
// 用意したパーサで、構文のまとまりのテキストオブジェクトも使える。
//
// req: FR-142, FR-240
func TestUserParserFetchBuildAndReuse(t *testing.T) {
	repo := newGrammarRepo(t, "grammar", map[string]string{
		// parser.c は外部スキャナーを使わないので、関数の中身は呼ばれない。コンパイルできることだけを試す。
		"grammar/src/scanner.c": "int vi_imitation_test_scanner(void) { return 1; }\n",
	})
	v1 := repo.git("rev-parse", "v1^{commit}")
	env := testParserEnv(t)
	settings := settingsJSON(t, "", map[string]map[string]any{
		"myjson": {"repo": repo.dir, "version": "v1", "fileTypes": []string{"myjson"}, "path": "grammar"},
	})
	text := `{"a": 1}`
	numberAt := ext.Position{Line: 0, Byte: strings.Index(text, "1")}
	keyAt := ext.Position{Line: 0, Byte: strings.Index(text, `"a"`)}

	conn, got, msgs, progress := connectParsers(t, env, settings)
	openBuffer(t, conn, 1, "myjson", text)
	p := receiveBuffer(t, got, 1, nil)
	if styleAt(p.Decorations, numberAt) != "number" || styleAt(p.Decorations, keyAt) != "string" {
		t.Errorf("decorations = %+v, want number at %v and string at %v", p.Decorations, numberAt, keyAt)
	}
	// 設定で足した言語でも、構文のまとまりのテキストオブジェクト（in）を、クエリなしに構文木の形で選べる（FR-240）。
	{
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		var r ext.TextObjectResolveResult
		err := conn.Call(ctx, ext.MethodTextObjectResolve, ext.TextObjectResolveParams{Buffer: 1, Version: 1, ID: "treesitter.block.inner", Cursor: numberAt}, &r)
		cancel()
		want := ext.TextObjectResolveResult{OK: true, Version: 1, Range: ext.Range{Start: keyAt, End: ext.Position{Byte: len(text) - 1}}}
		if err != nil || r != want {
			t.Errorf("textObject/resolve = %+v, %v; want %+v", r, err, want)
		}
	}
	// 取得、ビルド、用意を、1 つの識別子の begin、report、end で知らせる（FR-178）。メッセージの行には出さない。
	seen := waitProgress(t, progress, "パーサ myjson を用意した")
	var kinds []string
	for _, p := range seen {
		kinds = append(kinds, string(p.Kind)+":"+p.Token+":"+p.Title+":"+p.Message)
	}
	if len(seen) != 3 || seen[0].Kind != ext.ProgressBegin || seen[0].Title != progressTitle || !strings.Contains(seen[0].Message, "パーサ myjson を取得している") ||
		seen[1].Kind != ext.ProgressReport || !strings.Contains(seen[1].Message, "パーサ myjson をビルドしている") ||
		seen[2].Kind != ext.ProgressEnd || seen[2].Failed || seen[0].Token != seen[1].Token || seen[1].Token != seen[2].Token {
		t.Errorf("progress = %q", kinds)
	}
	if ms := drainMessages(msgs); len(ms) != 0 {
		t.Errorf("message/show = %+v, want none", ms)
	}
	lock := readLockFile(t, env.lockPath)
	if e := lock.Parsers["myjson"]; e.Commit != v1 || e.Version != "v1" || e.Repo != repo.dir {
		t.Errorf("lock = %+v, want commit %s", lock, v1)
	}
	built, err := filepath.Glob(filepath.Join(env.dataDir, "parsers", "myjson", "*", libraryFileName))
	if err != nil || len(built) != 1 {
		t.Fatalf("built = %v, %v", built, err)
	}

	// 次の起動では、ロックファイルの記録とビルドしたものを使う。git と C コンパイラが無くても読み込める。
	env2 := env
	env2.git = "no-such-git-for-test"
	conn2, got2, msgs2, progress2 := connectParsers(t, env2, settingsJSON(t, "no-such-cc-for-test", map[string]map[string]any{
		"myjson": {"repo": repo.dir, "version": "v1", "fileTypes": []string{"myjson"}, "path": "grammar"},
	}))
	openBuffer(t, conn2, 1, "myjson", text)
	if p := receiveBuffer(t, got2, 1, nil); styleAt(p.Decorations, numberAt) != "number" {
		t.Errorf("2 回目の decorations = %+v", p.Decorations)
	}
	if ms := drainMessages(msgs2); len(ms) != 0 {
		t.Errorf("2 回目の message/show = %+v, want none", ms)
	}
	select {
	case p := <-progress2:
		t.Errorf("2 回目の progress/report = %+v, want none", p)
	default:
	}

	// 版を変えたら、取得し直してビルドし、ロックファイルを替え、前のビルドを消す。
	// 拡張機能が SIGKILL で終わって残ったビルドの一時ディレクトリも、次の用意で消す。
	stale := filepath.Join(env.dataDir, "parsers", "myjson", ".build-stale")
	if err = os.MkdirAll(stale, 0o750); err != nil {
		t.Fatal(err)
	}
	v2 := repo.commit(map[string]string{"README": "v2\n"})
	repo.git("tag", "v2")
	conn3, got3, _, progress3 := connectParsers(t, env, settingsJSON(t, "", map[string]map[string]any{
		"myjson": {"repo": repo.dir, "version": "v2", "fileTypes": []string{"myjson"}, "path": "grammar"},
	}))
	openBuffer(t, conn3, 1, "myjson", text)
	receiveBuffer(t, got3, 1, nil)
	waitProgress(t, progress3, "パーサ myjson を用意した")
	if e := readLockFile(t, env.lockPath).Parsers["myjson"]; e.Commit != v2 || e.Version != "v2" {
		t.Errorf("lock after v2 = %+v, want commit %s", e, v2)
	}
	built2, err := filepath.Glob(filepath.Join(env.dataDir, "parsers", "myjson", "*", libraryFileName))
	if err != nil || len(built2) != 1 || built2[0] == built[0] {
		t.Errorf("built after v2 = %v, before %v", built2, built)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("残ったビルドの一時ディレクトリを消していない: %v", err)
	}
}

// TestRunChildCancelSendsTerm は、C コンパイラの取り消しで、プロセスグループ全体へ SIGTERM を送ることを確かめる。
// gcc の driver は SIGTERM を受けると一時ファイルを消す（SIGKILL では消せない）。
// スクリプトは、SIGTERM を受けたら印のファイルを書き、待っている子（sleep）も同じプロセスグループなので SIGTERM で終わる。
//
// req: FR-142
func TestRunChildCancelSendsTerm(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cc")
	mark := filepath.Join(dir, "mark")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntrap 'echo term > \"$1\"; exit 1' TERM\nsleep 30 &\nwait\n"), 0o700); err != nil { //nolint:gosec // G306: テストが実行するスクリプト
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	start := time.Now()
	err := runChild(ctx, dir, nil, script, mark)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("runChild = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("runChild took %v after the cancel; SIGTERM did not stop the group", d)
	}
	if b, err := os.ReadFile(mark); err != nil || strings.TrimSpace(string(b)) != "term" {
		t.Errorf("SIGTERM を受け取っていない: %q, %v", b, err)
	}
}

// messageTexts は、message/show の文言を改行でつなぐ。
func messageTexts(ms []ext.MessageShowParams) string {
	texts := make([]string, len(ms))
	for i, m := range ms {
		texts[i] = string(m.Level) + ": " + m.Text
	}
	return strings.Join(texts, "\n")
}

// TestUserParserInheritsQueries は、queries に書いたほかのパーサ（継ぐパーサ）のファイルを、自身のクエリの後ろにつなぎ、
// 継ぐパーサの定義の色（予約語）が付くことと、同じ範囲には先に書いた自身の定義の色が付くことを確かめる（FR-328）。
// 継ぐパーサのリポジトリは、そのファイルの種類のバッファを開いていなくても取得し、ロックファイルに記録する（ビルドはしない）。
// 次の起動では git も C コンパイラも使わずに読み込み、継ぐパーサの版を変えたら、つないだクエリを読み直す（FR-329）。
//
// req: FR-328, FR-329
func TestUserParserInheritsQueries(t *testing.T) {
	// 継ぐパーサの定義は true を予約語に、数値を文字列にする。継ぐ側の定義は数値を数値にするだけである。
	base := newGrammarRepo(t, ".", map[string]string{"queries/highlights.scm": "(true) @keyword\n(number) @string\n"})
	child := newGrammarRepo(t, "grammar", map[string]string{"queries/highlights.scm": "(number) @number\n"})
	baseV1 := base.git("rev-parse", "v1^{commit}")
	env := testParserEnv(t)
	parsers := func(baseVersion string) map[string]map[string]any {
		return map[string]map[string]any{
			"base": {"repo": base.dir, "version": baseVersion, "fileTypes": []string{"basejson"}},
			"main": {
				"repo": child.dir, "version": "v1", "fileTypes": []string{"mainjson"}, "path": "grammar",
				"queries": []any{"queries/highlights.scm", map[string]string{"parser": "base", "file": "queries/highlights.scm"}},
			},
		}
	}
	text := `{"a": true, "b": 1}`
	trueAt := ext.Position{Line: 0, Byte: strings.Index(text, "true")}
	numberAt := ext.Position{Line: 0, Byte: strings.Index(text, "1")}

	conn, got, msgs, progress := connectParsers(t, env, settingsJSON(t, "", parsers("v1")))
	openBuffer(t, conn, 1, "mainjson", text)
	p := receiveBuffer(t, got, 1, nil)
	if styleAt(p.Decorations, trueAt) != "keyword" || styleAt(p.Decorations, numberAt) != "number" {
		t.Errorf("decorations = %+v, want keyword at %v and number at %v", p.Decorations, trueAt, numberAt)
	}
	seen := waitProgress(t, progress, "パーサ main を用意した")
	var texts []string
	for _, s := range seen {
		texts = append(texts, s.Message)
	}
	if !slices.ContainsFunc(texts, func(s string) bool {
		return strings.Contains(s, "パーサ main が継ぐクエリのパーサ base を取得している")
	}) {
		t.Errorf("progress = %q", texts)
	}
	if ms := drainMessages(msgs); len(ms) != 0 {
		t.Errorf("message/show = %+v, want none", ms)
	}
	if e := readLockFile(t, env.lockPath).Parsers["base"]; e.Commit != baseV1 || e.Version != "v1" || e.Repo != base.dir {
		t.Errorf("lock の base = %+v, want commit %s", e, baseV1)
	}
	if built, _ := filepath.Glob(filepath.Join(env.dataDir, "parsers", "base", "*")); len(built) != 0 {
		t.Errorf("継ぐパーサをビルドした: %v", built)
	}

	// 次の起動では、ロックファイルの記録とビルドしたものを使う。git と C コンパイラが無くても、継いだ定義の色が付く。
	env2 := env
	env2.git = "no-such-git-for-test"
	conn2, got2, msgs2, _ := connectParsers(t, env2, settingsJSON(t, "no-such-cc-for-test", parsers("v1")))
	openBuffer(t, conn2, 1, "mainjson", text)
	if p := receiveBuffer(t, got2, 1, nil); styleAt(p.Decorations, trueAt) != "keyword" {
		t.Errorf("2 回目の decorations = %+v", p.Decorations)
	}
	if ms := drainMessages(msgs2); len(ms) != 0 {
		t.Errorf("2 回目の message/show = %+v, want none", ms)
	}

	// 継ぐパーサの版を変えたら、継ぐ側の版が同じでも、つないだクエリを読み直す。
	baseV2 := base.commit(map[string]string{"queries/highlights.scm": "(true) @constant\n"})
	base.git("tag", "v2")
	conn3, got3, _, progress3 := connectParsers(t, env, settingsJSON(t, "", parsers("v2")))
	openBuffer(t, conn3, 1, "mainjson", text)
	if p := receiveBuffer(t, got3, 1, nil); styleAt(p.Decorations, trueAt) != "constant" || styleAt(p.Decorations, numberAt) != "number" {
		t.Errorf("base v2 の decorations = %+v, want constant at %v", p.Decorations, trueAt)
	}
	waitProgress(t, progress3, "パーサ main を用意した")
	lock := readLockFile(t, env.lockPath)
	if e := lock.Parsers["base"]; e.Commit != baseV2 || e.Version != "v2" {
		t.Errorf("lock の base = %+v, want commit %s", e, baseV2)
	}
	if _, ok := lock.Parsers["main"]; !ok {
		t.Errorf("lock に main が無い: %+v", lock)
	}
	if built, _ := filepath.Glob(filepath.Join(env.dataDir, "parsers", "main", "*", libraryFileName)); len(built) != 1 {
		t.Errorf("main のビルド = %v, want 1（前のビルドを消す）", built)
	}
}

// TestUserParserFailureIsolated は、用意できないパーサ（ビルドできない、取得できない）は、そのパーサだけを使わず、
// 拡張機能の全体と同梱の言語は使い続けられることを確かめる。設定の誤りは、最初の buffer/open で知らせる。
//
// req: FR-142
func TestUserParserFailureIsolated(t *testing.T) {
	broken := newGrammarRepo(t, ".", map[string]string{"src/parser.c": "this is not C\n"})
	env := testParserEnv(t)
	settings := settingsJSON(t, "", map[string]map[string]any{
		"broken":  {"repo": broken.dir, "version": "v1", "fileTypes": []string{"broken"}},
		"missing": {"repo": filepath.Join(t.TempDir(), "no-such-repo"), "version": "v1", "fileTypes": []string{"missing"}},
		"invalid": {"repo": "relative/path", "version": "v1", "fileTypes": []string{"invalid"}},
	})
	conn, got, msgs, progress := connectParsers(t, env, settings)

	openBuffer(t, conn, 1, "broken", `{"a": 1}`)
	// 用意できなくても、始めた進み具合は終える（FR-178）。
	if seen := waitProgress(t, progress, "パーサ broken を用意できない"); seen[len(seen)-1].Kind != ext.ProgressEnd || !seen[len(seen)-1].Failed {
		t.Errorf("progress = %+v", seen)
	}
	seen := waitMessage(t, msgs, "パーサ broken を使えない")
	if texts := messageTexts(seen); !strings.Contains(texts, "warning: tree-sitter: パーサ invalid を使わない") {
		t.Errorf("設定の誤りを知らせていない: %s", texts)
	}
	if last := seen[len(seen)-1]; last.Level != ext.MessageError || !strings.Contains(last.Text, "ビルドできない") {
		t.Errorf("message = %+v", last)
	}
	openBuffer(t, conn, 2, "missing", `{}`)
	if seen := waitMessage(t, msgs, "パーサ missing を使えない"); !strings.Contains(seen[len(seen)-1].Text, "取得できない") {
		t.Errorf("message = %+v", seen[len(seen)-1])
	}
	if d := ping(t, conn); d > 500*time.Millisecond {
		t.Errorf("ping took %v", d)
	}
	var others []ext.DecorationSetParams
	openBuffer(t, conn, 3, "go", goSource)
	if p := receiveBuffer(t, got, 3, &others); len(p.Decorations) == 0 {
		t.Error("go のバッファに色が付かない")
	}
	if len(others) != 0 {
		t.Errorf("用意できないパーサのバッファに decoration/set が届いた: %+v", others)
	}
	// 失敗したパーサの記録は、ロックファイルに書かない。
	if _, err := os.Stat(env.lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ロックファイルを作った: %v", err)
	}
}

// TestUserParserOverridesBuiltin は、同梱の言語と利用者の設定のパーサが同じファイルの種類を持つとき、利用者の設定のパーサを使い、
// 利用者の設定のパーサを用意できなければ、同梱の言語を使うことを確かめる。
//
// req: FR-142
func TestUserParserOverridesBuiltin(t *testing.T) {
	// 文字列を keyword の色にするクエリで、利用者の設定のパーサが使われたことを見分ける。
	repo := newGrammarRepo(t, ".", map[string]string{"queries/highlights.scm": "(string) @keyword\n"})
	env := testParserEnv(t)
	settings := settingsJSON(t, "", map[string]map[string]any{
		"myjson":  {"repo": repo.dir, "version": "v1", "fileTypes": []string{"json"}},
		"missing": {"repo": filepath.Join(t.TempDir(), "no-such-repo"), "version": "v1", "fileTypes": []string{"jsonc"}},
	})
	conn, got, msgs, _ := connectParsers(t, env, settings)
	text := `{"a": "b"}`
	at := ext.Position{Line: 0, Byte: strings.Index(text, `"b"`)}

	openBuffer(t, conn, 1, "json", text)
	if p := receiveBuffer(t, got, 1, nil); styleAt(p.Decorations, at) != "keyword" {
		t.Errorf("json: decorations = %+v, want keyword at %v（利用者の設定のパーサ）", p.Decorations, at)
	}
	openBuffer(t, conn, 2, "jsonc", text)
	waitMessage(t, msgs, "パーサ missing を使えない")
	if p := receiveBuffer(t, got, 2, nil); styleAt(p.Decorations, at) != "string" {
		t.Errorf("jsonc: decorations = %+v, want string at %v（同梱の言語）", p.Decorations, at)
	}
}

// TestUserParserKeepsResponding は、パーサのビルドを待つ間も、ping にすぐ応答し、編集を受け取り、
// ビルドの後に最新の全文の一覧を送ることを確かめる（dev-cycle 2.2。本体の生存確認の期限は 2 秒）。
// C コンパイラを、3 秒待ってから cc を実行するスクリプトに替える。
//
// req: FR-142
func TestUserParserKeepsResponding(t *testing.T) {
	cc, err := exec.LookPath(defaultCompiler)
	if err != nil {
		t.Fatalf("C コンパイラ %s が無い: %v", defaultCompiler, err)
	}
	slow := filepath.Join(t.TempDir(), "slow-cc")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 3\nexec "+cc+" \"$@\"\n"), 0o700); err != nil { //nolint:gosec // G306: テストが実行するスクリプト
		t.Fatal(err)
	}
	repo := newGrammarRepo(t, ".", nil)
	env := testParserEnv(t)
	conn, got, _, progress := connectParsers(t, env, settingsJSON(t, slow, map[string]map[string]any{
		"myjson": {"repo": repo.dir, "version": "v1", "fileTypes": []string{"myjson"}},
	}))
	text := `{"a": 1}`
	openBuffer(t, conn, 1, "myjson", text)
	waitProgress(t, progress, "パーサ myjson をビルドしている")
	start := time.Now()
	for time.Since(start) < 2*time.Second {
		if d := ping(t, conn); d > 500*time.Millisecond {
			t.Errorf("ping took %v during the build", d)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// ビルドの間の編集は、ビルドの後の一覧に入る。
	e := ext.Edit{Range: ext.Range{Start: ext.Position{Line: 0, Byte: 6}, End: ext.Position{Line: 0, Byte: 7}}, Text: `"x"`}
	if err := conn.Notify(ext.MethodBufferChange, ext.BufferChangeParams{Buffer: 1, FromVersion: 1, ToVersion: 2, Edits: []ext.Edit{e}}); err != nil {
		t.Fatal(err)
	}
	// 編集がビルドの終わりより後に届けば、バージョン 1 の一覧が先に届く。
	p := receiveBuffer(t, got, 1, nil)
	if p.Version == 1 {
		p = receiveBuffer(t, got, 1, nil)
	}
	if p.Version != 2 || styleAt(p.Decorations, ext.Position{Line: 0, Byte: 6}) != "string" {
		t.Errorf("decorations = version %d %+v, want version 2 with a string at 0:6", p.Version, p.Decorations)
	}
	if d := time.Since(start); d < 500*time.Millisecond {
		t.Errorf("decoration/set arrived %v after the build started; the slow compiler was not used", d)
	}
}
