package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// gitTimeout は、git の 1 回の実行を待つ上限である（仮）。
const gitTimeout = 10 * time.Second

// repoEnvVars は、git の子プロセスに渡さない環境変数である。
//
// 本体を GIT_EDITOR にして git commit から起動すると、本体と拡張機能の環境に GIT_DIR や GIT_INDEX_FILE が残る。
// そのまま渡すと、git がバッファのファイルのリポジトリではなく、それらの変数が指すリポジトリを読む。
// 一覧は internal/extmgr の repoEnvVars と同じで（git 2.43.0 の git rev-parse --local-env-vars の出力に、
// git(1) の「The Git Repository」の節の GIT_NAMESPACE、GIT_CEILING_DIRECTORIES、GIT_DISCOVERY_ACROSS_FILESYSTEM を足したもの）、
// 拡張機能の実行ファイルに extmgr を入れないために写した。
var repoEnvVars = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_CONFIG":                       true,
	"GIT_CONFIG_PARAMETERS":            true,
	"GIT_CONFIG_COUNT":                 true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_IMPLICIT_WORK_TREE":           true,
	"GIT_GRAFT_FILE":                   true,
	"GIT_INDEX_FILE":                   true,
	"GIT_NO_REPLACE_OBJECTS":           true,
	"GIT_REPLACE_REF_BASE":             true,
	"GIT_PREFIX":                       true,
	"GIT_SHALLOW_FILE":                 true,
	"GIT_COMMON_DIR":                   true,
	"GIT_NAMESPACE":                    true,
	"GIT_CEILING_DIRECTORIES":          true,
	"GIT_DISCOVERY_ACROSS_FILESYSTEM":  true,
}

// gitEnv は、環境 env から repoEnvVars を除き、git がインデックスの任意のロックを取らないようにする GIT_OPTIONAL_LOCKS=0 を足したものを返す。
// 読むだけの git を、利用者が別の端末で打つ git と取り合わないためである。
func gitEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if repoEnvVars[name] || name == "GIT_OPTIONAL_LOCKS" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_OPTIONAL_LOCKS=0")
}

// git は、dir を作業ディレクトリにして git を args で実行し、標準出力を返す。
// 失敗したら、標準エラー出力の最初の行を含む誤りを返す。
//
// git は拡張機能と同じプロセスグループで起動する。本体が拡張機能を強制終了するとき（プロセスグループへの SIGKILL）に、git も一緒に終わる。
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // 実行するのは git で、引数は拡張機能が組み立てたもの（dir はバッファのファイルのディレクトリ）
	cmd.Env = gitEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return stdout.Bytes(), nil
}

// lookGit は、PATH に git が無ければ、画面に出せる日本語の誤りを返す。
func lookGit() error {
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git のコマンドが見つかりません")
	}
	return nil
}

// headVersion は、git の HEAD の版のファイルの中身である。
type headVersion struct {
	// Name は、リポジトリの根からの相対パスである（git の HEAD:<パス> の <パス>）。
	Name string
	// Text は、HEAD の版の中身である。位置を数えるもとのテキスト（基本設計 4.1）にするため、末尾の改行を 1 つ除いてある。
	Text string
}

// readHead は、絶対パス path のファイルの、そのファイルを含む git のリポジトリの HEAD の版を読む。
// 誤りの文言は、画面のメッセージの行に出せる日本語である。
func readHead(ctx context.Context, path string) (headVersion, error) {
	if err := lookGit(); err != nil {
		return headVersion{}, err
	}
	// シンボリックリンクは、リンクの先のファイルの HEAD の版と見比べる（バッファの中身はリンクの先の中身なので）。
	// git show はリンクそのもの（先のパスの文字列）を返すため、先に解く。解けなければ（まだ無いファイルなど）そのまま使う。
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir, base := filepath.Dir(path), filepath.Base(path)
	prefix, err := git(ctx, dir, "rev-parse", "--show-prefix")
	if err != nil {
		return headVersion{}, fmt.Errorf("%s は git のリポジトリの中のファイルではありません（%w）", path, err)
	}
	name := strings.TrimSpace(string(prefix)) + base
	// HEAD:./<名前> の ./ は、-C のディレクトリからの相対パスを表す（gitrevisions(7)）。
	// --filters は、作業ツリーへ取り出すときと同じ変換（.gitattributes の eol、core.autocrlf、smudge のフィルター）を通す
	// （git-cat-file(1)）。通さないと、改行が CRLF のファイルの全行が違って見える（PR #247 のレビュー）。
	// smudge のフィルター（git-lfs など）が遅いと、git の期限（gitTimeout）で失敗しうる。
	out, err := git(ctx, dir, "cat-file", "--filters", "HEAD:./"+base)
	if err != nil {
		return headVersion{}, fmt.Errorf("HEAD の %s を読めません（%w）", name, err)
	}
	if !utf8.Valid(out) {
		// 本体と拡張機能のメッセージは JSON で、不正な UTF-8 を表せない（基本設計 5.3）。
		return headVersion{}, fmt.Errorf("HEAD の %s は不正な UTF-8 を含むので開けません", name)
	}
	return headVersion{Name: name, Text: strings.TrimSuffix(string(out), "\n")}, nil
}
