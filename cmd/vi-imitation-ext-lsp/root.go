package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/project"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// cmdRoot は、カーソルのあるバッファの言語サーバーの根を見るか、指定し直すコマンドの ID である（FR-322）。
// 宣言ファイルの contributes.commands に書く。
const cmdRoot = "lsp.root"

// rootArgMarkers は、lsp.root の引数のうち、指定した根をやめて目印（rootMarkers）で探す根へ戻すことを表すものである。
// ディレクトリの名前と重ならないように、"-" で始まる形にする。
const rootArgMarkers = "--markers"

// rootCommand は、コマンド lsp.root を処理する（FR-322）。ループの上で動く。
//
//   - 引数なし: カーソルのあるバッファの言語サーバーの名前、根、根の決め方（指定か目印か）、仮想環境の python を message/show で出す
//   - ディレクトリ: その言語サーバーの根に指定する。相対パスはバッファのファイルのディレクトリから、"~/" はホームディレクトリから解く。
//     バッファのファイルは、そのディレクトリの下になければならない
//   - --markers: そのバッファのファイルを含む、指定した根をやめ、目印で探す根へ戻す
//
// 根を変えたら、同じ言語サーバーで開いているすべてのバッファの根を決め直し、根の変わったバッファを、新しい根の client へ移す（reassign）。
// 受け持つバッファが残らなくなった client の言語サーバーは止める。
// 呼んだバッファの client が前からあったもの（今と同じ根を指定したときを含む）なら、作り直して仮想環境を探し直す（reassignAndRenew）。
// 指定した根は、拡張機能が動いている間、後から開くファイルにも当てる（そのディレクトリの下のファイルは、目印に依らずその根になる）。
//
// req: FR-322
func (x *extension) rootCommand(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
	c := x.byBuffer[p.Buffer]
	if c == nil {
		x.showMessage(ext.MessageWarning, "このバッファには言語サーバーが無い")
		return ext.CommandExecuteResult{OK: false}, nil
	}
	path, _ := c.pathOf(p.Buffer)
	var args struct {
		Text string `json:"text"`
	}
	if len(p.Args) > 0 {
		_ = json.Unmarshal(p.Args, &args)
	}
	arg := strings.TrimSpace(args.Text)
	switch arg {
	case "":
		x.showMessage(ext.MessageInfo, x.describeRoot(c))
		return ext.CommandExecuteResult{OK: true}, nil
	case rootArgMarkers:
		pins := x.pins[c.server]
		x.pins[c.server] = slices.DeleteFunc(slices.Clone(pins), func(r string) bool { return within(path, r) })
		if len(x.pins[c.server]) == 0 {
			delete(x.pins, c.server)
		}
		moved, renewed := x.reassignAndRenew(c.server, p.Buffer)
		nc := x.byBuffer[p.Buffer]
		x.showMessage(ext.MessageInfo, fmt.Sprintf("言語サーバー %s の根を、目印で探した %s にした%s", c.server, nc.root, movedText(moved, renewed)))
		return ext.CommandExecuteResult{OK: true}, nil
	}
	dir, err := resolveRootArg(arg, filepath.Dir(path))
	if err != nil {
		x.showMessage(ext.MessageError, err.Error())
		return ext.CommandExecuteResult{OK: false}, nil
	}
	if !within(path, dir) {
		x.showMessage(ext.MessageError, fmt.Sprintf("%s は %s の下に無いので、根にできない", path, dir))
		return ext.CommandExecuteResult{OK: false}, nil
	}
	if !slices.Contains(x.pins[c.server], dir) {
		x.pins[c.server] = append(x.pins[c.server], dir)
	}
	moved, renewed := x.reassignAndRenew(c.server, p.Buffer)
	x.showMessage(ext.MessageInfo, fmt.Sprintf("言語サーバー %s の根を %s にした%s", c.server, dir, movedText(moved, renewed)))
	return ext.CommandExecuteResult{OK: true}, nil
}

// reassignAndRenew は、言語サーバー name のバッファの根を決め直して移し（reassign）、
// コマンドを呼んだバッファ buf の client が前からあったものなら、それを作り直す（renew）。移したバッファの数と、作り直したかを返す。
//
// 前からある client を作り直すのは、根が変わらなくても、言語サーバーを起動し直して仮想環境を探し直させるためである
// （後から uv sync で .venv を作ったときに、根を変えない :lsp.root --markers で拾える）。新しく作った client は、作ったときに探しているので作り直さない。
//
// req: FR-322
func (x *extension) reassignAndRenew(name string, buf ext.BufferID) (moved int, renewed bool) {
	x.mu.Lock()
	before := make(map[*client]bool, len(x.clients))
	for _, c := range x.clients {
		before[c] = true
	}
	x.mu.Unlock()
	moved = x.reassign(name)
	if c := x.byBuffer[buf]; c != nil && before[c] {
		x.renew(c)
		renewed = true
	}
	return moved, renewed
}

// renew は、client c を、同じ言語サーバーと根の新しい client に置き換える。ループから呼ぶ。
// c の文書をすべて新しい client へ移し（take と adopt）、c の言語サーバーを止める（retire）。
// 新しい client は、作るときに仮想環境を探し直し、言語サーバーを起動し直す。
//
// req: FR-322
func (x *extension) renew(c *client) {
	key := clientKey{server: c.server, root: c.root}
	x.mu.Lock()
	if x.clients[key] == c {
		delete(x.clients, key)
	}
	x.mu.Unlock()
	nc := x.clientAt(c.server, c.root)
	for _, id := range c.bufferIDs() {
		if d := c.take(id); d != nil {
			nc.adopt(d)
			x.byBuffer[id] = nc
		}
	}
	x.retire(c)
}

// movedText は、移したバッファの数と、言語サーバーを起動し直したかを、メッセージに添える形にする。
func movedText(n int, renewed bool) string {
	text := "（移したバッファは無い"
	if n > 0 {
		text = fmt.Sprintf("（バッファ %d 個を移した", n)
	}
	if renewed {
		text += "。言語サーバーを起動し直した"
	}
	return text + "）"
}

// resolveRootArg は、lsp.root の引数 arg を、根にするディレクトリの絶対パスにする。
// "~/" で始まればホームディレクトリから、相対パスなら base から解く。ディレクトリでなければ誤りを返す。
func resolveRootArg(arg, base string) (string, error) {
	switch {
	case arg == "~" || strings.HasPrefix(arg, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("ホームディレクトリが分からない: %w", err)
		}
		arg = filepath.Join(home, strings.TrimPrefix(arg, "~"))
	case !filepath.IsAbs(arg):
		arg = filepath.Join(base, arg)
	}
	dir := filepath.Clean(arg)
	fi, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("根にするディレクトリ %s を読めない: %w", dir, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s はディレクトリでないので、根にできない", dir)
	}
	return dir, nil
}

// describeRoot は、client の言語サーバーの名前、根、根の決め方、仮想環境を 1 行にする（lsp.root の引数なし）。
func (x *extension) describeRoot(c *client) string {
	how := "目印で探した"
	if slices.Contains(x.pins[c.server], c.root) {
		how = "指定した"
	}
	text := fmt.Sprintf("言語サーバー %s（%s）、根 %s（%s）", c.server, c.name(), c.root, how)
	switch {
	case c.cfg.PythonVenvSetting == "":
	case c.venv.Python != "" && c.venv.Source == project.FromVirtualEnv:
		// 根の中に無く、本体を起動したシェルで有効にした仮想環境を使っている。別のプロジェクトの仮想環境でありうるので、そう分かるように出す。
		text += fmt.Sprintf("、仮想環境 %s（根の中に無いので VIRTUAL_ENV のもの。%s に入れた）", c.venv.Python, c.cfg.PythonVenvSetting)
	case c.venv.Python != "":
		text += fmt.Sprintf("、仮想環境 %s（%s に入れた）", c.venv.Python, c.cfg.PythonVenvSetting)
	default:
		text += "、仮想環境は見つからない"
	}
	return text
}

// reassign は、言語サーバー name で同期しているすべてのバッファの根を決め直し、根の変わったバッファを新しい根の client へ移す。
// ループから呼ぶ。移したバッファの数を返す。
// 移した後、受け持つバッファが残らなくなった元の client は、x.clients から外して止める（retire）。
//
// req: FR-322
func (x *extension) reassign(name string) int {
	sc := x.cfg.servers[name]
	moved := 0
	var left []*client
	for _, id := range slices.Sorted(maps.Keys(x.byBuffer)) {
		c := x.byBuffer[id]
		if c.server != name {
			continue
		}
		path, ok := c.pathOf(id)
		if !ok {
			continue
		}
		root := x.rootFor(name, sc, path)
		if root == c.root {
			continue
		}
		d := c.take(id)
		if d == nil {
			continue
		}
		nc := x.clientAt(name, root)
		nc.adopt(d)
		x.byBuffer[id] = nc
		moved++
		if !slices.Contains(left, c) {
			left = append(left, c)
		}
	}
	for _, c := range left {
		if c.idle() {
			x.retire(c)
		}
	}
	return moved
}

// scheduleIdleStop は、client c が受け持つ文書が無ければ、設定の idleStop の後に c を止める予約をする。ループから呼ぶ（FR-323）。
// 予約は、その前に c が文書を受け持てば（clientAt が c を返せば）無効になる。
// 止める時点でも、文書が無く、x.clients の c のままで、予約が新しいものかを、mu を持って確かめる。
// clientAt も mu を持って予約を無効にするので、clientAt が c を返した後に止めることは無い。
//
// req: FR-323
func (x *extension) scheduleIdleStop(c *client) {
	if x.cfg == nil || x.cfg.idleStop <= 0 || !c.idle() {
		return
	}
	key := clientKey{server: c.server, root: c.root}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.clients[key] != c || x.closing {
		return
	}
	c.idleGen++
	gen := c.idleGen
	if c.idleTimer != nil {
		c.idleTimer.Stop()
	}
	c.idleTimer = time.AfterFunc(x.cfg.idleStop, func() {
		x.mu.Lock()
		ok := c.idleGen == gen && x.clients[key] == c && c.idle() && x.retireLocked(c)
		x.mu.Unlock()
		if ok {
			c.logf("%s: no documents for %v; stopping", c.name(), x.cfg.idleStop)
			x.stopRetired(c)
		}
	})
}

// retire は、受け持つ文書の無くなった client を x.clients から外し、言語サーバーを止める。止めるのは別の goroutine で待つ。
// 拡張機能が終わるとき（stop）は、止め終えるのを待つ。
func (x *extension) retire(c *client) {
	x.mu.Lock()
	ok := x.retireLocked(c)
	x.mu.Unlock()
	if ok {
		x.stopRetired(c)
	}
}

// retireLocked は、mu を持った状態で、client c を x.clients から外し、止めることにする。
// 拡張機能が終わる途中（closing）なら何もせずに false を返す（stop が集めた client を止める）。
// true を返したら、呼ぶ側は mu を放してから stopRetired を呼ぶ。
func (x *extension) retireLocked(c *client) bool {
	key := clientKey{server: c.server, root: c.root}
	if x.clients[key] == c {
		delete(x.clients, key)
	}
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
	if x.closing {
		return false
	}
	x.retiring.Add(1)
	return true
}

// stopRetired は、retireLocked で外した client の言語サーバーを、別の goroutine で止める。
func (x *extension) stopRetired(c *client) {
	go func() {
		defer x.retiring.Done()
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		c.stop(ctx)
	}()
}
