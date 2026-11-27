package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
)

// 言語サーバーの実行ファイルが見つからないときの扱い（FR-143）。
//
// 言語サーバーと整形の道具を取ってくる仕組みは、vi-imitation には持たせず、mise に任せる（#278 のオーナーの決め）。
// 設定の servers の 1 つに mise の道具の ID（serverConfig.Mise）があり、Command が見つからないとき、次のとおりにする。
//
//   - 自動で入れない設定（既定）: 入れ方（mise use -g <道具の ID>）をメッセージで案内する。同じ案内は、拡張機能が動いている間に 2 回出さない
//   - 自動で入れる設定（serverConfig.AutoInstall）: mise where で入っているかを確かめ、入っていなければ mise install で入れてから、
//     mise exec <道具の ID> -- <Command> <Args> で起動する。mise が見つからなければ、案内に戻る
//
// PATH で見つかった Command が mise の shim（mise の実行ファイルへのシンボリックリンク）なら、mise which で版が決まっているかを確かめ、
// 決まっていなければ見つからなかったものとして扱う（PR #281 のレビューの必須 2）。
// mise install は shim を作るが、mise の設定ファイルに版を書かないので、その shim は "No version is set" で終わる。
// shim のディレクトリを PATH に置く使い方で、自動で入れた後の起動が shim に当たって失敗し続けないためである。
//
// 入れる段（mise where と mise install）と起動する段（mise exec）を分けるのは、入れるのに時間がかかる（数秒から数十秒）ためである。
// 入れている間は initialize の期限を数えず、別の期限（installTimeout）で待ち、進み具合を progress/report で出す（FR-178）。
// どちらも起動の goroutine（client.start）の上で行い、ext/server のメッセージを処理する goroutine を止めない。
//
// mise に MISE_YES を渡さず、標準入力も渡さない。
// 作業のディレクトリ（ワークスペースの根）に信頼していない mise.toml があると、mise は止まるので、その出力をそのまま知らせる。
// MISE_YES で答えると、プロジェクトの設定を利用者の確認なしに信頼することになるためである（#278）。
//
// 言語の名前で分けた処理は持たない。どの言語サーバーも、設定の Command と Mise だけで扱う。

// errReported は、起動できない理由を利用者へもう知らせたので、改めて知らせないことを表す。
var errReported = errors.New("already reported to the user")

// miseLineInterval は、mise install の出力の行を進み具合の本文に出す間隔の下限である。
// それより短い間に届いた行はログにだけ書く。値は仮。
const miseLineInterval = time.Second

// miseTailLines は、mise install の出力のうち、末尾から覚えておく行の数である。値は仮。
const miseTailLines = 5

// miseInterruptGrace は、mise を止めるときに SIGINT を送ってから、終わらなければ SIGKILL を送るまでの時間である。
// mise は SIGINT で npm などを終わらせてから終わる。拡張機能の stopTimeout より短くする。値は仮。
const miseInterruptGrace = 1500 * time.Millisecond

// command は、言語サーバーを起動する実行ファイルと引数である。
type command struct {
	path string
	args []string
}

// prepare は、言語サーバーを起動するコマンドを決める。起動の goroutine から呼ぶ。
//
// Command が見つかれば、それをそのまま使う。見つからず、Mise があれば、FR-143 の扱い（このファイルの冒頭）にする。
// 起動できないときは誤りを返す。利用者へ理由を知らせ終えたときは errReported を返す。
// 案内だけを出したとき（自動で入れない設定、mise が無い）は、retry を true にする。
// 利用者が案内に従って入れた後、次にファイルを開いたときに探し直すためである。
//
// req: FR-143
func (c *client) prepare(ctx context.Context, root string) (cmd command, retry bool, err error) {
	var notes []string
	path, err := exec.LookPath(c.cfg.Command)
	switch {
	case err == nil && c.cfg.Mise == "":
		return command{path: path, args: c.cfg.Args}, false, nil
	case err == nil:
		shim, ok := miseShim(path)
		if !ok || c.shimResolves(ctx, shim, root) {
			return command{path: path, args: c.cfg.Args}, false, nil
		}
		notes = append(notes, fmt.Sprintf("PATH の %s は mise の shim で、版が決まっていない", path))
	case c.cfg.Mise == "" || !errors.Is(err, exec.ErrNotFound) && !errors.Is(err, fs.ErrNotExist):
		return command{}, false, err
	}
	if !c.cfg.autoInstall() {
		c.guide(notes...)
		return command{}, true, errReported
	}
	mise, merr := exec.LookPath(c.mise)
	if merr != nil {
		c.x.logf("%s: mise: %v", c.name(), merr)
		c.guide(append(notes, "mise が見つからないので、自動では入れない（mise を入れてから打つ）")...)
		return command{}, true, errReported
	}
	if !c.miseInstalled(ctx, mise, root) {
		if err := c.miseInstall(ctx, mise, root); err != nil {
			return command{}, false, err
		}
	}
	args := append([]string{"exec", c.cfg.Mise, "--", c.cfg.Command}, c.cfg.Args...)
	return command{path: mise, args: args}, false, nil
}

// guide は、言語サーバーの入れ方を message/show で案内する。notes は、入れ方の後ろに置く説明である。
// メッセージの行は画面の幅で切れるので、利用者が打つ入れ方（mise use -g …）を前に置き、長くなりうる説明（shim のパスなど）を後ろに置く
// （PR #281 の再レビューの推奨 2）。
// 同じ実行ファイルと道具の ID の案内は、拡張機能が動いている間に 2 回出さない（別の言語サーバーの名前や根の client からも）。
//
// req: FR-143
func (c *client) guide(notes ...string) {
	if !c.x.firstGuide(c.cfg.Command + "\x00" + c.cfg.Mise) {
		return
	}
	text := fmt.Sprintf("言語サーバー %s が見つからない。mise use -g %s で入れられる", c.name(), c.cfg.Mise)
	for _, n := range notes {
		text += "。" + n
	}
	c.x.showMessage(ext.MessageWarning, text)
}

// miseShim は、path が mise の shim（mise の実行ファイルへのシンボリックリンク）なら、リンクの先の mise の実行ファイルを返す。
// mise は shim を、既定で mise の実行ファイルへのシンボリックリンクとして作る（2026-11-20 に mise 2026.5.2 で確かめた）。
func miseShim(path string) (string, bool) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil || target == path || filepath.Base(target) != "mise" {
		return "", false
	}
	return target, true
}

// shimResolves は、mise which で、Command の shim の版が決まっているか（shim で起動できるか）を返す。
// 作業のディレクトリはワークスペースの根で、そのディレクトリの mise の設定で決まる版を見る。
//
// req: FR-143
func (c *client) shimResolves(ctx context.Context, mise, root string) bool {
	out, err := c.runMiseQuiet(ctx, mise, root, "which", c.cfg.Command)
	if err != nil {
		c.x.logf("%s: mise which %s: %v: %s", c.name(), c.cfg.Command, err, out)
		return false
	}
	return true
}

// miseInstalled は、mise where で、Mise の道具が入っているかを返す。
// mise where は、入っていなければ終了コード 1 で終わる。設定の誤り（信頼していない mise.toml など）でも 1 で終わるので、
// 入っていないと区別せずに false を返し、mise install の誤りとして知らせる。
func (c *client) miseInstalled(ctx context.Context, mise, root string) bool {
	out, err := c.runMiseQuiet(ctx, mise, root, "where", c.cfg.Mise)
	if err != nil {
		c.x.logf("%s: mise where %s: %v: %s", c.name(), c.cfg.Mise, err, out)
		return false
	}
	return true
}

// runMiseQuiet は、mise を args で起動し、出力をまとめて返す。期限は installTimeout である。
func (c *client) runMiseQuiet(ctx context.Context, mise, root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.installTimeout)
	defer cancel()
	cmd := exec.Command(mise, args...) //nolint:gosec // 起動するのは mise で、引数は利用者の設定である
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := c.runMise(ctx, cmd)
	return strings.TrimSpace(out.String()), err
}

// runMise は、mise の子プロセス cmd を startMise で起動し、終わるまで待つ。起動の goroutine から呼ぶ。
//
// ctx が終わったら（拡張機能が止まるか期限を超えたら）、mise のプロセスグループへ SIGINT を送り、
// mise が npm などを終わらせてから終わるのを miseInterruptGrace まで待つ。終わらなければ SIGKILL を送る。
// exec.CommandContext の既定の取り消し（mise だけへの SIGKILL）では、mise が別のプロセスグループで起動した npm が孤児で残る（PR #281 のレビュー）。
// 動いている間は c.miseDone を置き、client.stop が mise の終わりを待てるようにする。
// 拡張機能がもう止まっていれば、起動しない。
func (c *client) runMise(ctx context.Context, cmd *exec.Cmd) error {
	// mise が終わった後も、孫プロセス（npm など）が出力を持ち続けたときに、待ち続けないようにする。
	cmd.WaitDelay = time.Second
	finished := make(chan struct{})
	defer close(finished)
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return errStopped
	}
	c.miseDone = finished
	c.mu.Unlock()
	if err := startMise(cmd); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	interruptMise(cmd)
	select {
	case err := <-done:
		return err
	case <-time.After(miseInterruptGrace):
	}
	killMise(cmd)
	return <-done
}

// errStopped は、拡張機能が止まるので、mise を起動しなかったことを表す。
var errStopped = errors.New("the extension is stopping")

// miseInstall は、mise install で Mise の道具を入れる。起動の goroutine から呼ぶ。
// 入れている間の進み具合を、progress/report で知らせる（FR-143、FR-178）。始めたときに begin、mise の出力の行
// （miseLineInterval ごとに 1 行まで）を report の本文、終えたときに end を送る。失敗と取り消しの end は failed にする。
// 失敗したら、mise の出力の原因の行と末尾の行（miseDetail）を含めて message/show で知らせ、errReported を返す。
// 拡張機能が止まるとき（ctx の取り消し）は、mise を終わらせ（runMise）、message/show で知らせずに誤りを返す。
//
// req: FR-143
func (c *client) miseInstall(ctx context.Context, mise, root string) error {
	ctx, cancel := context.WithTimeout(ctx, c.installTimeout)
	defer cancel()
	token := c.miseToken()
	c.x.reportProgress(ext.ProgressReportParams{
		Token: token, Kind: ext.ProgressBegin, Title: "mise",
		Message: fmt.Sprintf("言語サーバー %s が見つからないので、%s を入れている", c.name(), c.cfg.Mise),
	})
	endMessage, failed := "", true
	defer func() {
		c.x.reportProgress(ext.ProgressReportParams{Token: token, Kind: ext.ProgressEnd, Message: endMessage, Failed: failed})
	}()
	cmd := exec.Command(mise, "install", c.cfg.Mise) //nolint:gosec // 起動するのは mise で、道具の ID は利用者の設定である
	cmd.Dir = root
	w := &lineWriter{line: c.miseLine}
	cmd.Stdout = w
	cmd.Stderr = w
	err := c.runMise(ctx, cmd)
	w.flush()
	switch {
	case err == nil:
		c.x.logf("%s: mise install %s: done", c.name(), c.cfg.Mise)
		endMessage, failed = fmt.Sprintf("%s を入れた。言語サーバー %s を起動する", c.cfg.Mise, c.name()), false
		return nil
	case errors.Is(err, errStopped) || errors.Is(ctx.Err(), context.Canceled):
		endMessage = "取り消した"
		return fmt.Errorf("mise install %s: %w", c.cfg.Mise, context.Canceled)
	}
	endMessage = fmt.Sprintf("%s を入れられない", c.cfg.Mise)
	c.x.logf("%s: mise install %s: %v", c.name(), c.cfg.Mise, err)
	detail := strings.Join(miseDetail(w.cause(), w.tail()), " ")
	if ctx.Err() != nil {
		detail = fmt.Sprintf("%s 以内に終わらなかった", c.installTimeout)
	} else if detail == "" {
		detail = err.Error()
	}
	c.x.showMessage(ext.MessageError, fmt.Sprintf("mise で %s を入れられない: %s", c.cfg.Mise, detail))
	return errReported
}

// miseVerboseHint は、mise が誤りの最後に付ける、どの誤りでも同じ行に含まれる文字列である（mise 2026.5.2）。
const miseVerboseHint = "Run with --verbose or MISE_VERBOSE=1"

// miseWarnPrefix は、mise の警告の行の始まりである。失敗の知らせには含めない（警告の後に誤りの行が続くため）。
const miseWarnPrefix = "mise WARN"

// miseDetailLines は、mise install が失敗したときに、メッセージの行に含める mise の出力の行の数（末尾から）である。
// メッセージの行は 1 行で、画面の幅で切れるので、誤りの中身を含む末尾の行を残す。値は仮。
const miseDetailLines = 2

// miseDetail は、mise install が失敗したときにメッセージの行に含める、mise の出力の行を返す。
// 原因の行（cause。lineWriter.cause）があれば先頭に置き、その後に、末尾の行から、どの誤りでも同じ案内の行（miseVerboseHint）と
// mise の警告の行と、mise が起動した道具の誤りの行（isCauseLine。npm の "A complete log of this run …" など、原因の後に続く行）を除いた
// 末尾の miseDetailLines 行を置く。
// 信頼していない mise.toml のときは、"Config files in … are not trusted." と "Trust them with `mise trust`. …" の 2 行が残る
// （その前の "error parsing config file: …" の行は、ログにだけ残る）。
// mise が起動した npm が registry に接続できないときは、mise の行は "Failed to install …: command [\"npm\", …] exited with code 1" のように
// 理由を含まないので、npm の "npm error code ECONNREFUSED" の行を原因として先頭に置く（PR #281 のレビューの推奨 2）。
func miseDetail(cause string, lines []string) []string {
	var out []string
	for _, l := range lines {
		if !strings.Contains(l, miseVerboseHint) && !strings.HasPrefix(l, miseWarnPrefix) && !isCauseLine(l) {
			out = append(out, l)
		}
	}
	out = out[max(len(out)-miseDetailLines, 0):]
	if cause != "" && !slices.Contains(out, cause) {
		out = append([]string{cause}, out...)
	}
	return out
}

// isCauseLine は、mise install の出力の行が、mise が起動した道具（npm など）の誤りの行かを返す。
// mise 自身の行（"mise " で始まる）は、末尾の行として別に含めるので除く。
// どの道具かで分けず、"error" を含むか（大文字と小文字を区別しない）だけで決める。
func isCauseLine(s string) bool {
	return !strings.HasPrefix(s, "mise ") && strings.Contains(strings.ToLower(s), "error")
}

// miseLine は、mise install の出力の 1 行をログに書き、前に出してから miseLineInterval が過ぎていれば、
// 進み具合（progress/report の report）の本文にする。
func (c *client) miseLine(line string, last time.Time) bool {
	c.x.logf("%s: mise: %s", c.name(), line)
	if time.Since(last) < miseLineInterval {
		return false
	}
	c.x.reportProgress(ext.ProgressReportParams{Token: c.miseToken(), Kind: ext.ProgressReport, Message: line})
	return true
}

// miseToken は、mise に言語サーバーを入れさせる間の進み具合の識別子である。入れるのは client ごとに 1 つずつなので、
// 言語サーバーの名前と根で分ける（同じ言語サーバーの、根の違う client が同時に入れさせうる。FR-320）。
func (c *client) miseToken() string {
	return "mise/" + c.server + "/" + c.root
}

// lineWriter は、子プロセスの出力を行に分け、空でない行ごとに line を呼ぶ io.Writer である。
// 末尾の miseTailLines 行と、最初の原因の行（isCauseLine）を覚える。
// exec.Cmd が Stdout と Stderr に同じ値を渡すので、Write は並行に呼ばれないが、tail と cause は Wait の後に読むので、mu で守る。
type lineWriter struct {
	// line は、行と、前に line が true を返した時刻を受け取り、行を利用者へ出したかを返す。
	line func(line string, last time.Time) bool

	mu    sync.Mutex
	buf   []byte
	lines []string
	first string
	shown time.Time
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// flush は、改行で終わっていない最後の行を出す。
func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

// emit は、w.mu を持った状態で、1 行を扱う。進み具合の表示（"\r" で上書きする行）は、最後の部分だけを使う。
func (w *lineWriter) emit(s string) {
	if i := strings.LastIndexByte(strings.TrimRight(s, "\r"), '\r'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if w.first == "" && isCauseLine(s) {
		w.first = s
	}
	w.lines = append(w.lines, s)
	if len(w.lines) > miseTailLines {
		w.lines = w.lines[len(w.lines)-miseTailLines:]
	}
	if w.line(s, w.shown) {
		w.shown = time.Now()
	}
}

// tail は、覚えている末尾の行を返す。
func (w *lineWriter) tail() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.lines...)
}

// cause は、最初の原因の行（isCauseLine）を返す。無ければ空である。
func (w *lineWriter) cause() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.first
}
