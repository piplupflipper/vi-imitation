//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/expand"
	userconfig "github.com/piplupflipper/vi-imitation/internal/config"
)

// この節は、設定の形（アダプターの表と構成の一覧）、つなぎ方、構成の出どころと値の当て方のテストである（基本設計 6.4。#408）。
// sh と /proc を使うので Linux でだけ動かす。

// mustJSON は、v を JSON の文字列にする。
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeDef は、テスト用のデバッグアダプターを transport で使う adapters の項目である。
func fakeDef(transport string, args ...string) map[string]any {
	a := map[string]any{"transport": transport, "command": fakeAdapter}
	if len(args) > 0 {
		a["args"] = args
	}
	return a
}

// openFT は、中身が srcText でファイルの種類が fileType のバッファを、番号 1（mustExec が使うバッファ）で開く。
func (h *host) openFT(path, fileType string) {
	const text = srcText
	h.t.Helper()
	const id ext.BufferID = 1
	h.mu.Lock()
	h.versions[id] = 1
	h.ids[path] = id
	h.mu.Unlock()
	p := ext.BufferOpenParams{Buffer: id, Path: path, FileType: fileType, Version: 1, Text: text}
	if err := h.conn.Notify(ext.MethodBufferOpen, p); err != nil {
		h.t.Fatalf("buffer/open: %v", err)
	}
}

// waitPane は、pane/open が届くまで待つ。
func (h *host) waitPane() openedPane {
	h.t.Helper()
	select {
	case p := <-h.panes:
		return p
	case <-time.After(testTimeout):
		h.t.Fatal("pane/open did not arrive")
		return openedPane{}
	}
}

// labels は、部品の項目の文字列を並べる。
func (p openedPane) labels() []string {
	var out []string
	for _, it := range p.params.Component.Items {
		out = append(out, it.Label)
	}
	return out
}

// itemID は、文字列が label の項目の ID を返す。
func (p openedPane) itemID(t *testing.T, label string) string {
	t.Helper()
	for _, it := range p.params.Component.Items {
		if it.Label == label {
			return it.ID
		}
	}
	t.Fatalf("item %q not in %q", label, p.labels())
	return ""
}

// selectItem は、部品 view の項目 item を選んだことを知らせる。
func (h *host) selectItem(view ext.ViewID, item string) {
	h.t.Helper()
	if err := h.conn.Notify(ext.MethodViewSelect, ext.ViewSelectParams{View: view, Item: item, Selection: 1}); err != nil {
		h.t.Fatal(err)
	}
}

// closeView は、部品 view を利用者が閉じたことを知らせる。
func (h *host) closeView(view ext.ViewID) {
	h.t.Helper()
	if err := h.conn.Notify(ext.MethodViewClosed, ext.ViewClosedParams{View: view}); err != nil {
		h.t.Fatal(err)
	}
}

// waitArgs は、テスト用のデバッグアダプターが出した "<command> args <JSON>" の行を待ち、引数を返す。
func (h *host) waitArgs(command string) map[string]any {
	h.t.Helper()
	prefix := command + " args "
	for _, s := range h.waitOutput(prefix) {
		for line := range strings.SplitSeq(s, "\n") {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				var m map[string]any
				if err := json.Unmarshal([]byte(rest), &m); err != nil {
					h.t.Fatalf("args %q: %v", rest, err)
				}
				return m
			}
		}
	}
	h.t.Fatalf("%s args not found", command)
	return nil
}

// stopAndWait は、debug.terminate でデバッグを終え、状態が消えるまで待つ。
func (h *host) stopAndWait() {
	h.t.Helper()
	h.mustExec(cmdStop, 0)
	h.waitEnded()
}

// startFakeServer は、テスト用のデバッグアダプターを TCP で待ち受けさせて起動し（既に動いているアダプターの代役）、ポートとプロセスを返す。
func startFakeServer(t *testing.T) (int, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command(fakeAdapter, "-listen", "127.0.0.1:0")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(line, "listening %d", &port); err != nil {
		t.Fatalf("listening line %q: %v", line, err)
	}
	return port, cmd
}

// killAtCleanup は、テストの終わりに pid のプロセスを止める（attach で残したデバッグ対象の代役）。
func killAtCleanup(t *testing.T, pid int) {
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
}

// TestParseSettings は、設定の形の誤りを項目の名前を添えて断り、aliases で同じアダプターを引け、
// adapters か configurations を書いた設定では 1 組の設定の既定（dlv dap）を足さないことを確かめる。
//
// req: FR-279, FR-280
func TestParseSettings(t *testing.T) {
	bad := map[string]string{
		"unknown transport": `{"adapters": {"a": {"transport": "pipe", "command": "x"}}}`,
		"no command":        `{"adapters": {"a": {"transport": "stdio"}}}`,
		"server no port":    `{"adapters": {"a": {"transport": "server", "host": "h"}}}`,
		"args not strings":  `{"adapters": {"a": {"command": "x", "args": [1]}}}`,
		"env not strings":   `{"adapters": {"a": {"command": "x", "env": {"A": 1}}}}`,
		"alias clash":       `{"adapters": {"a": {"command": "x", "aliases": ["b"]}, "b": {"command": "y"}}}`,
		"configs not array": `{"configurations": {"go": {}}}`,
		"legacy launch":     `{"launch": []}`,
	}
	for name, s := range bad {
		if _, err := parseSettings([]byte(s)); err == nil {
			t.Errorf("%s: parseSettings(%s) = nil error", name, s)
		}
	}

	s, err := parseSettings([]byte(`{"adapters": {"python": {"command": "debugpy-adapter", "aliases": ["debugpy"], "connectFrom": "connect"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.adapters["debugpy"] != s.adapters["python"] || s.adapters["python"].transport != transportStdio || s.adapters["python"].adapterID != "python" {
		t.Errorf("adapters = %+v", s.adapters)
	}
	if s.legacy != nil {
		t.Error("the default dlv dap remains though adapters is set")
	}

	// 何も書かなければ dlv dap の構成 1 つ。1 組の設定を書けば、adapters と一緒でも候補になる。
	s, _ = parseSettings(nil)
	if s.legacy == nil || s.legacy.adapter.transport != transportClientSocket {
		t.Errorf("default = %+v", s.legacy)
	}
	s, err = parseSettings([]byte(`{"adapters": {"a": {"command": "x"}}, "adapter": {"command": "y", "args": ["--stdio"]}, "adapterID": "z"}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.legacy == nil || s.legacy.adapter.transport != transportStdio || s.legacy.adapter.adapterID != "z" {
		t.Errorf("legacy = %+v", s.legacy)
	}
}

// TestTransports は、標準入出力、拡張機能のソケットへの接続、アダプターが開いたポートへの接続の 3 つのつなぎ方で、
// 設定の構成（type、request、name を含めて送る）で始めて止まり、拡張機能が起動したアダプターを終わらせることを確かめる。
// アダプターの env の値の変数も展開する。
//
// req: FR-279, FR-280, FR-284
func TestTransports(t *testing.T) {
	for _, tc := range []struct {
		transport string
		args      []string
	}{
		{transportStdio, nil},
		{transportClientSocket, []string{"-client-addr=" + clientAddrPlaceholder}},
		{transportServerPort, []string{"-listen", "127.0.0.1:${port}"}},
	} {
		t.Run(tc.transport, func(t *testing.T) {
			def := fakeDef(tc.transport, tc.args...)
			def["env"] = map[string]any{"FAKEADAPTER_ENV": "${fileBasename}"}
			h := newHost(t, mustJSON(t, map[string]any{
				"adapters": map[string]any{"fake": def},
				"configurations": map[string]any{"go": []any{
					map[string]any{"name": "n", "type": "fake", "request": "launch", "program": "${fileDirname}", "fake": map[string]any{"child": true}},
				}},
			}))
			h.openFT(srcPath, "go")
			h.mustExec(cmdToggleBreakpoint, 2)
			h.mustExec(cmdStart, 0)
			args := h.waitArgs("launch")
			if args["type"] != "fake" || args["request"] != "launch" || args["name"] != "n" || args["program"] != "/src/app" {
				t.Errorf("launch args = %v", args)
			}
			h.waitOutput("adapterenv main.go")
			adapter := h.waitPID("adapter")
			child := h.waitPID("child")
			h.waitSigns(bp2, exec2)
			h.waitMessage("デバッグを開始した（n、launch）")
			h.stopAndWait()
			h.waitOutput("disconnect terminateDebuggee=true")
			waitGone(t, "adapter", adapter)
			waitGone(t, "debuggee", child)
		})
	}
}

// TestServerAttach は、既に動いているアダプターへの接続（server）で attach し、終えるときに terminate を送らず、
// terminateDebuggee を false にした disconnect を送り、アダプターもデバッグ対象も止めないことを確かめる。
//
// req: FR-279, FR-284
func TestServerAttach(t *testing.T) {
	port, server := startFakeServer(t)
	h := newHost(t, mustJSON(t, map[string]any{
		"adapters": map[string]any{"remote": map[string]any{"transport": "server", "host": "127.0.0.1", "port": port}},
		"configurations": map[string]any{"go": []any{
			map[string]any{"name": "attach", "type": "remote", "request": "attach", "fake": map[string]any{"child": true}},
		}},
	}))
	h.openFT(srcPath, "go")
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	if args := h.waitArgs("attach"); args["request"] != "attach" {
		t.Errorf("attach args = %v", args)
	}
	child := h.waitPID("child")
	killAtCleanup(t, child)
	h.waitSigns(bp2, exec2)
	h.stopAndWait()
	h.waitOutput("disconnect terminateDebuggee=false")
	time.Sleep(100 * time.Millisecond)
	if gone(child) {
		t.Error("the attached debuggee was terminated")
	}
	if gone(server.Process.Pid) {
		t.Error("the running adapter was stopped")
	}
	// 同じアダプターへもう一度繋げる。
	h.mustExec(cmdStart, 0)
	h.waitArgs("attach")
	h.waitSigns(bp2, exec2)
}

// TestConnectFrom は、アダプターの connectFrom のキーを構成が持てば、アダプターを起動せずにそのつなぐ先へ直に繋ぎ
// （port は数として読める文字列でもよい）、持たなければアダプターを起動することを確かめる（debugpy の connect の形）。
//
// req: FR-279, FR-284
func TestConnectFrom(t *testing.T) {
	port, _ := startFakeServer(t)
	h := newHost(t, mustJSON(t, map[string]any{
		"adapters": map[string]any{"py": map[string]any{"transport": "stdio", "command": "/nonexistent/adapter", "connectFrom": "connect"}},
		"configurations": map[string]any{"python": []any{
			map[string]any{"name": "direct", "type": "py", "request": "attach", "connect": map[string]any{"port": strconv.Itoa(port)}},
			map[string]any{"name": "spawn", "type": "py", "request": "launch"},
		}},
	}))
	h.openFT(srcPath, "python")
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	p := h.waitPane()
	h.selectItem(p.id, p.itemID(t, "direct  (設定)"))
	if args := h.waitArgs("attach"); args["connect"] == nil {
		t.Errorf("attach args = %v, want connect sent as is", args)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()
	h.waitOutput("disconnect terminateDebuggee=false")

	h.mustExec(cmdStart, 0)
	p = h.waitPane()
	h.selectItem(p.id, p.itemID(t, "spawn  (設定)"))
	h.waitMessage("デバッグアダプター py を起動できない")
}

// TestCandidatePick は、候補が 2 つ以上なら中央のポップアップの一覧（選んだら閉じる）で選ばせ、閉じたら始めず、
// 選んでいる間の debug.terminate で一覧を閉じて始めないことを確かめる。
//
// req: FR-280, FR-283
func TestCandidatePick(t *testing.T) {
	h := newHost(t, mustJSON(t, map[string]any{
		"adapters": map[string]any{"fake": fakeDef(transportStdio)},
		"configurations": map[string]any{"go": []any{
			map[string]any{"name": "a", "type": "fake", "request": "launch"},
			map[string]any{"name": "a", "type": "fake", "request": "launch", "stopOnEntry": true},
		}},
		"adapter": map[string]any{"command": fakeAdapter},
	}))
	h.openFT(srcPath, "go")
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	p := h.waitPane()
	if want := []string{"a  (設定)", "a  (設定)", "既定  (既定)"}; !slices.Equal(p.labels(), want) {
		t.Errorf("items = %q, want %q", p.labels(), want)
	}
	if p.params.Placement != ext.PaneCenter || !p.params.CloseOnSelect {
		t.Errorf("pane/open = %+v, want center with closeOnSelect", p.params)
	}
	// 選んでいる間は、もう一度始められない。
	if res := h.exec(1, cmdStart, 0); res.OK {
		t.Error("start while picking: ok = true")
	}
	h.selectItem(p.id, p.params.Component.Items[1].ID)
	if args := h.waitArgs("launch"); args["stopOnEntry"] != true {
		t.Errorf("launch args = %v, want the second configuration", args)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	// 閉じたら始めない。
	h.mustExec(cmdStart, 0)
	p = h.waitPane()
	h.closeView(p.id)
	h.waitMessage("デバッグの開始を取り消した")

	// 選んでいる間の debug.terminate は、一覧を閉じて始めない。
	h.mustExec(cmdStart, 0)
	p = h.waitPane()
	h.mustExec(cmdStop, 0)
	select {
	case id := <-h.closes:
		if id != p.id {
			t.Errorf("view/close %d, want %d", id, p.id)
		}
	case <-time.After(testTimeout):
		t.Fatal("view/close did not arrive")
	}
	h.waitMessage("デバッグの開始を取り消した")
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	if h.d.rootLocked() != nil || h.d.preparing != nil {
		t.Error("a session started after cancelling")
	}
}

// TestReplayStartWithoutLast は、再生中（sync）の開始は一覧を出さず、前回の構成が無ければ失敗することを確かめる。
//
// req: FR-280
func TestReplayStartWithoutLast(t *testing.T) {
	h := newHost(t, fakeSettings(t, nil, nil))
	h.open(1, srcPath, srcText)
	if res := h.replay(cmdStart, 0); res.OK {
		t.Errorf("start (sync) without a previous run = %+v, want ok = false", res)
	}
	h.waitMessage("前回のデバッグが無い")
	select {
	case p := <-h.panes:
		t.Errorf("pane/open %+v while replaying", p.params)
	default:
	}
}

// writeFile は、path に text を書く（ディレクトリも作る）。
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLaunchJSON は、.vscode/launch.json（JSONC）の構成を設定の構成より先に候補にし、linux の上書き、envFile、console の置き換えを当て、
// presentation.hidden を除き、compounds を 1 回だけ知らせ、無視できる項目は聞いて続け、扱えない変数は聞かずに断ることを確かめる。
//
// req: FR-281, FR-283, FR-288
func TestLaunchJSON(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".vscode", "launch.json"), `{
  // コメントと末尾のカンマを書ける
  "version": "0.2.0",
  "configurations": [
    {
      "name": "L1", "type": "fake", "request": "launch", "program": "win",
      "linux": { "program": "${workspaceFolder}/lin" },
      "envFile": "${workspaceFolder}/.env", "env": { "A": "keep" },
      "console": "integratedTerminal",
    },
    { "name": "hidden", "type": "fake", "request": "launch", "presentation": { "hidden": true } },
    { "name": "task", "type": "fake", "request": "launch", "preLaunchTask": "build" },
    { "name": "cmd", "type": "fake", "request": "launch", "program": "${command:pickFile}" },
    { "name": "rc", "type": "fake", "request": "attach", "resolveCommand": { "command": ["touch", "${workspaceFolder}/ran"] } },
  ],
  "compounds": [{ "name": "c", "configurations": ["L1", "task"] }],
}`)
	writeFile(t, filepath.Join(root, ".env"), "# comment\nA=fromfile\n\nB=\"quoted\"\n")
	file := filepath.Join(root, "sub", "main.go")
	writeFile(t, file, srcText)
	h := newHost(t, mustJSON(t, map[string]any{
		"adapters":       map[string]any{"fake": fakeDef(transportStdio)},
		"configurations": map[string]any{"go": []any{map[string]any{"name": "S", "type": "fake", "request": "launch"}}},
	}))
	h.openFT(file, "go")
	h.mustExec(cmdToggleBreakpoint, 2)

	h.mustExec(cmdStart, 0)
	h.waitMessage("compounds は扱えない")
	p := h.waitPane()
	if want := []string{"L1  (launch.json)", "task  (launch.json)", "cmd  (launch.json)", "rc  (launch.json)", "S  (設定)"}; !slices.Equal(p.labels(), want) {
		t.Fatalf("items = %q, want %q", p.labels(), want)
	}
	h.selectItem(p.id, p.itemID(t, "L1  (launch.json)"))
	h.waitMessage("console integratedTerminal は扱えない")
	args := h.waitArgs("launch")
	env, _ := args["env"].(map[string]any)
	if args["program"] != root+"/lin" || args["console"] != "internalConsole" || env["A"] != "keep" || env["B"] != "quoted" {
		t.Errorf("launch args = %v", args)
	}
	for _, k := range []string{"linux", "envFile"} {
		if _, ok := args[k]; ok {
			t.Errorf("launch args has %s: %v", k, args)
		}
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	// 無視できる項目は聞いて、続ければ送らずに始める。compounds は 2 回目には知らせない。
	h.mustExec(cmdStart, 0)
	p = h.waitPane()
	h.selectItem(p.id, p.itemID(t, "task  (launch.json)"))
	select {
	case q := <-h.asks:
		if !strings.Contains(q.Title, "preLaunchTask") {
			t.Errorf("choice/ask = %+v", q)
		}
	case <-time.After(testTimeout):
		t.Fatal("choice/ask did not arrive")
	}
	h.answers <- ext.ChoiceAskResult{Selected: "continue"}
	if args := h.waitArgs("launch"); args["preLaunchTask"] != nil {
		t.Errorf("launch args = %v, want preLaunchTask dropped", args)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	// 扱えない変数は、何も聞かずに知らせて始めない。
	h.mustExec(cmdStart, 0)
	p = h.waitPane()
	h.selectItem(p.id, p.itemID(t, "cmd  (launch.json)"))
	h.waitMessage("${command:pickFile}")
	select {
	case q := <-h.asks:
		t.Errorf("choice/ask %+v for an unsupported variable", q)
	case <-time.After(100 * time.Millisecond):
	}
	for {
		select {
		case m := <-h.msgs:
			if strings.Contains(m.Text, "compounds") {
				t.Errorf("compounds was shown again: %q", m.Text)
			}
			continue
		default:
		}
		break
	}

	// launch.json の resolveCommand は動かさずに始めない（#408 のレビューの裁定要 1）。
	h.mustExec(cmdStart, 0)
	p = h.waitPane()
	h.selectItem(p.id, p.itemID(t, "rc  (launch.json)"))
	h.waitMessage("resolveCommand は .vscode/launch.json では扱えない")
	if _, err := os.Stat(filepath.Join(root, "ran")); err == nil {
		t.Error("the resolveCommand in launch.json ran")
	}
}

// TestVariables は、構成の変数（ファイル、カーソル、ワークスペースの根、環境変数）を展開し、
// まだ扱えない ${input:…} と、つなぐ段階の変数を使えない所に書いたものは、始めずに知らせることを確かめる。
//
// req: FR-283, FR-279
func TestVariables(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "sub", "main.go")
	writeFile(t, file, srcText)
	t.Setenv("FAKE_TEST_VAR", "val")
	h := newHost(t, mustJSON(t, map[string]any{
		"adapters": map[string]any{
			"fake":    fakeDef(transportStdio),
			"badport": fakeDef(transportStdio, "-x=${port}"),
		},
		"configurations": map[string]any{"go": []any{
			map[string]any{
				"name": "vars", "type": "fake", "request": "launch",
				"program": "${fileBasenameNoExtension}:${lineNumber}:${columnNumber}:${relativeFile}:${workspaceFolderBasename}:${env:FAKE_TEST_VAR}",
			},
			map[string]any{"name": "input", "type": "fake", "request": "launch", "program": "${input:x}"},
			map[string]any{"name": "port", "type": "fake", "request": "launch", "program": "${port}"},
			map[string]any{"name": "adapterport", "type": "badport", "request": "launch"},
		}},
	}))
	h.openFT(file, "go")
	h.mustExec(cmdToggleBreakpoint, 2)
	pick := func(name string) {
		t.Helper()
		// カーソルは 0 から数えて 2 行目（${lineNumber} は 3）。
		if res := h.exec(1, cmdStart, 2); !res.OK {
			t.Fatal("start: ok = false")
		}
		p := h.waitPane()
		h.selectItem(p.id, p.itemID(t, name+"  (設定)"))
	}
	pick("vars")
	want := fmt.Sprintf("main:3:1:%s:%s:val", filepath.Join("sub", "main.go"), filepath.Base(root))
	if args := h.waitArgs("launch"); args["program"] != want {
		t.Errorf("program = %v, want %s", args["program"], want)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	pick("input")
	h.waitMessage("${input:x}")
	pick("port")
	h.waitMessage("でだけ使える")
	pick("adapterport")
	h.waitMessage("つなぎ方 stdio では ${port} を使えない")
}

// TestResolveCommand は、構成の resolveCommand の標準出力の JSON を構成に重ね（値の中の ${…} は文字のまま送る）、
// アダプターの起動の方法を置き換える adapter と、失敗（0 でない終了コード、不正な JSON、扱えない項目、期限）では始めずに理由を知らせ、
// 標準出力を開いたままの子を残しても期限と取り消しが効き、動いている間の debug.terminate でコマンドを止めて始めないことを確かめる。
//
// req: FR-282, FR-283
func TestResolveCommand(t *testing.T) {
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin.json")
	pidFile := filepath.Join(dir, "pid")
	// 出力の ${x} は、構成に書くと 1 段目の展開で知らない変数になるので、printf の \044（$）で書く。
	ok := `cat > ` + stdin + `; printf '{"configuration":{"program":"/resolved","stopOnEntry":null,"env":{"P":"pa\044{x}ss"}}}'`
	// setsid で、プロセスグループの外に、標準出力を開いたままの子を残す（#408 のレビューの必須 4）。
	bgPidFiles := []string{filepath.Join(dir, "bg1"), filepath.Join(dir, "bg2")}
	leakyN := 0
	leaky := func() string {
		f := bgPidFiles[leakyN]
		leakyN++
		return `setsid sh -c 'echo $$ > ` + f + `; exec sleep 30' & `
	}
	cases := []map[string]any{
		{"name": "ok", "stopOnEntry": true, "resolveCommand": map[string]any{"command": []any{"sh", "-c", ok}}},
		{"name": "exit", "resolveCommand": map[string]any{"command": []any{"sh", "-c", "echo first >&2; echo boom >&2; exit 3"}}},
		{"name": "badjson", "resolveCommand": map[string]any{"command": []any{"sh", "-c", "echo '{'"}}},
		{"name": "badkey", "resolveCommand": map[string]any{"command": []any{"sh", "-c", `echo '{"configuration":{"envFile":"x"},"other":1}'`}}},
		{"name": "adapter", "resolveCommand": map[string]any{"command": []any{"sh", "-c", `echo '{"adapter":{"command":"/bin/sh"}}'`}}},
		{"name": "typekey", "resolveCommand": map[string]any{"command": []any{"sh", "-c", `echo '{"configuration":{"type":"other"}}'`}}},
		{"name": "toattach", "resolveCommand": map[string]any{"command": []any{"sh", "-c", `echo '{"configuration":{"request":"attach"}}'`}}},
		{"name": "timeout", "resolveCommand": map[string]any{"command": []any{"sh", "-c", "sleep 10"}, "timeoutMs": 300}},
		{"name": "leaky", "resolveCommand": map[string]any{"command": []any{"sh", "-c", leaky() + `echo '{"configuration":{"program":"/leaky"}}'`}, "timeoutMs": 3000}},
		{"name": "leakytimeout", "resolveCommand": map[string]any{"command": []any{"sh", "-c", leaky() + "sleep 10"}, "timeoutMs": 300}},
		{"name": "cancel", "resolveCommand": map[string]any{"command": []any{"sh", "-c", "echo $$ > " + pidFile + "; exec sleep 30"}}},
	}
	t.Cleanup(func() {
		for _, f := range bgPidFiles {
			if b, err := os.ReadFile(f); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
	})
	var configs []any
	for _, c := range cases {
		c["type"] = "fake"
		c["request"] = "launch"
		configs = append(configs, c)
	}
	h := newHost(t, mustJSON(t, map[string]any{
		"adapters":       map[string]any{"fake": fakeDef(transportClientSocket, "-client-addr="+clientAddrPlaceholder)},
		"configurations": map[string]any{"go": configs},
	}))
	h.openFT(srcPath, "go")
	h.mustExec(cmdToggleBreakpoint, 2)
	pick := func(name string) {
		t.Helper()
		h.mustExec(cmdStart, 0)
		p := h.waitPane()
		h.selectItem(p.id, p.itemID(t, name+"  (設定)"))
	}

	pick("ok")
	args := h.waitArgs("launch")
	env, _ := args["env"].(map[string]any)
	if args["program"] != "/resolved" || args["stopOnEntry"] != nil || args["resolveCommand"] != nil || env["P"] != "pa${x}ss" {
		t.Errorf("launch args = %v", args)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()
	data, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatalf("stdin %s: %v", data, err)
	}
	adapter, _ := in["adapter"].(map[string]any)
	cfg, _ := in["configuration"].(map[string]any)
	if in["file"] != srcPath || in["fileType"] != "go" || cfg["name"] != "ok" ||
		!slices.Contains(adapter["args"].([]any), any("-client-addr="+clientAddrPlaceholder)) {
		t.Errorf("stdin = %s, want the file, the configuration and ${clientAddr} left as is", data)
	}

	pick("exit")
	if m := h.waitMessage("失敗した"); !strings.Contains(m.Text, "boom") || strings.Contains(m.Text, "first") {
		t.Errorf("message = %q, want the last stderr line", m.Text)
	}
	pick("badjson")
	h.waitMessage("JSON でない")
	pick("badkey")
	h.waitMessage("扱えない項目がある: configuration.envFile, other")
	pick("adapter")
	h.waitMessage("扱えない項目がある: adapter")
	// type はアダプターを差し替えるので扱えない。request は返した値で送り、終え方もそれで決める（PR #410 の再レビューの推奨 C）。
	pick("typekey")
	h.waitMessage("扱えない項目がある: configuration.type")
	pick("toattach")
	if args := h.waitArgs("attach"); args["request"] != "attach" {
		t.Errorf("attach args = %v", args)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()
	h.waitOutput("disconnect terminateDebuggee=false")
	pick("timeout")
	h.waitMessage("期限")

	// 標準出力を開いたままの子が残っても、コマンドが終われば出力を使い、期限も効く。
	start := time.Now()
	pick("leaky")
	if args := h.waitArgs("launch"); args["program"] != "/leaky" {
		t.Errorf("launch args = %v", args)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()
	pick("leakytimeout")
	h.waitMessage("期限")
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("leaky commands took %v", d)
	}

	pick("cancel")
	var pid int
	deadline := time.Now().Add(testTimeout)
	for pid == 0 && time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil && len(b) > 0 && b[len(b)-1] == '\n' {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the resolve command did not start")
	}
	h.mustExec(cmdStop, 0)
	h.waitMessage("デバッグの開始を取り消した")
	waitGone(t, "resolve command", pid)
}

// TestPickProcess は、${pickProcess} で利用者のプロセスの一覧を中央のポップアップに出し、選んだプロセス ID を、
// 値の全体が ${pickProcess} だけなら数で、ほかの文字と混ざれば文字列で入れ、閉じたら始めないことを確かめる。
//
// req: FR-299
func TestPickProcess(t *testing.T) {
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	})
	pid := strconv.Itoa(sleeper.Process.Pid)
	h := newHost(t, mustJSON(t, map[string]any{
		"adapters": map[string]any{"fake": fakeDef(transportStdio)},
		"configurations": map[string]any{"go": []any{
			map[string]any{"name": "pick", "type": "fake", "request": "attach", "processId": "${pickProcess}", "label": "pid ${pickProcess}"},
		}},
	}))
	h.openFT(srcPath, "go")
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	p := h.waitPane()
	if p.params.Placement != ext.PaneCenter || !slices.ContainsFunc(p.labels(), func(l string) bool { return strings.HasPrefix(l, pid+"  sleep 30") }) {
		t.Fatalf("items = %q, want %s sleep 30", p.labels(), pid)
	}
	h.selectItem(p.id, pid)
	// dlv の attach は processId を数でしか受けない（#408 のレビューの必須 3）。
	if args := h.waitArgs("attach"); args["processId"] != float64(sleeper.Process.Pid) || args["label"] != "pid "+pid {
		t.Errorf("attach args = %v, want processId %s as a number and label as a string", args, pid)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	h.mustExec(cmdStart, 0)
	p = h.waitPane()
	h.closeView(p.id)
	h.waitMessage("デバッグの開始を取り消した")
}

// TestConfigExample は、同梱する設定の例（config.example.jsonc）の DAP の部分が、この拡張機能の設定として誤り無く読め、
// オーナーの nvim の Python（debugpy）、JS と TS（vscode-js-debug）と、Go（dlv）のアダプターと構成を持つことを確かめる。
func TestConfigExample(t *testing.T) {
	data, err := os.ReadFile("../../config.example.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	ucfg, errs := userconfig.Parse("config.example.jsonc", data)
	if len(errs) != 0 {
		t.Fatalf("設定の例の誤り: %v", errs)
	}
	s, err := parseSettings(ucfg.ExtensionSettings["dap"])
	if err != nil {
		t.Fatal(err)
	}
	if s.legacy != nil {
		t.Error("the example keeps the default dlv dap")
	}
	for name, transport := range map[string]string{"go": transportClientSocket, "python": transportStdio, "debugpy": transportStdio, "pwa-node": transportServerPort, "pwa-chrome": transportServerPort} {
		if a := s.adapters[name]; a == nil || a.transport != transport {
			t.Errorf("adapters[%s] = %+v, want %s", name, a, transport)
		}
	}
	for _, ft := range []string{"go", "python", "javascript", "typescript"} {
		cands := s.settingsCandidates(ft)
		if len(cands) == 0 {
			t.Errorf("no configurations for %s", ft)
		}
		for _, c := range cands {
			if _, err := s.lookupAdapter(c); err != nil {
				t.Errorf("%s: %v", ft, err)
			}
			unit := map[string]any{"configuration": c.config}
			r := (&debugger{}).resolver(t.Context(), invocation{}, "/w")
			if err := expand.Check(unit, r); err != nil {
				t.Errorf("%s %s: %v", ft, c.name, err)
			}
		}
	}
}

// TestOldHost は、本体の拡張機能 API の版が minAPIVersion より古ければ、どの操作も断り、要る版を 1 回だけ知らせることを確かめる。
func TestOldHost(t *testing.T) {
	h := newHost(t, "")
	h.d.mu.Lock()
	h.d.apiVersion = "0.13.0"
	h.d.mu.Unlock()
	h.open(1, srcPath, srcText)
	for range 2 {
		if res := h.exec(1, cmdToggleBreakpoint, 2); res.OK {
			t.Error("toggle on an old host: ok = true")
		}
	}
	h.waitMessage("本体の拡張機能 API " + minAPIVersion + " 以上が要ります（この本体は 0.13.0）")
	select {
	case m := <-h.msgs:
		t.Errorf("second message %q", m.Text)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestApplyEnvFile は、envFile の読み方（コメントと空の行を飛ばし、引用符を外し、env にある名前を上書きしない）を確かめる。
//
// req: FR-281
func TestApplyEnvFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "e"), "# c\nA=1\n B = ' 2 ' \nC=x=y\n")
	cfg := map[string]any{"envFile": "e", "env": map[string]any{"A": "keep"}}
	if err := applyEnvFile(cfg, dir); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"A": "keep", "B": " 2 ", "C": "x=y"}
	if got := cfg["env"]; mustJSON(t, got) != mustJSON(t, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
	if err := applyEnvFile(map[string]any{"envFile": "missing"}, dir); err == nil {
		t.Error("missing envFile: nil error")
	}
	writeFile(t, filepath.Join(dir, "bad"), "novalue\n")
	if err := applyEnvFile(map[string]any{"envFile": "bad"}, dir); err == nil {
		t.Error("a line without =: nil error")
	}
}
