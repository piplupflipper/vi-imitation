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

	"github.com/piplupflipper/vi-imitation/ext"
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
		moved := x.reassign(c.server)
		nc := x.byBuffer[p.Buffer]
		x.showMessage(ext.MessageInfo, fmt.Sprintf("言語サーバー %s の根を、目印で探した %s にした%s", c.server, nc.root, movedText(moved)))
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
	moved := x.reassign(c.server)
	x.showMessage(ext.MessageInfo, fmt.Sprintf("言語サーバー %s の根を %s にした%s", c.server, dir, movedText(moved)))
	return ext.CommandExecuteResult{OK: true}, nil
}

// movedText は、移したバッファの数を、メッセージに添える形にする。
func movedText(n int) string {
	if n == 0 {
		return "（移したバッファは無い）"
	}
	return fmt.Sprintf("（バッファ %d 個を移した）", n)
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

// retire は、受け持つ文書の無くなった client を x.clients から外し、言語サーバーを止める。止めるのは別の goroutine で待つ。
// 拡張機能が終わるとき（stop）は、止め終えるのを待つ。
func (x *extension) retire(c *client) {
	x.mu.Lock()
	key := clientKey{server: c.server, root: c.root}
	if x.clients[key] == c {
		delete(x.clients, key)
	}
	x.mu.Unlock()
	x.retiring.Add(1)
	go func() {
		defer x.retiring.Done()
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		c.stop(ctx)
	}()
}
