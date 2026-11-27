package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
)

// fakeServer は、fakels を、ファイルの種類 fileTypes を受け持つ言語サーバーにする設定である。ログは logPath に書く。
func fakeServer(logPath string, fileTypes []string, extra map[string]any, args ...string) map[string]any {
	sc := map[string]any{
		"command":     fakeLS,
		"args":        append([]string{"-log", logPath}, args...),
		"fileTypes":   fileTypes,
		"rootMarkers": []string{"package.json"},
	}
	for k, v := range extra {
		sc[k] = v
	}
	return sc
}

// touch は、h.dir の下にファイル name を作る。途中のディレクトリも作る。
func (h *host) touch(name string, mode os.FileMode) string {
	h.t.Helper()
	path := filepath.Join(h.dir, name)
	writeScript(h.t, path, mode)
	return path
}

// writeScript は、path に中身が "#!/bin/sh" だけのファイルを、権限 mode で作る。途中のディレクトリも作る。
func writeScript(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

// startedPids は、ログ path に "start" を書いた fakels のプロセス ID を、書いた順に返す。
func startedPids(path string) []int {
	var pids []int
	for _, e := range readLogFile(path) {
		if e.Kind == "start" {
			pids = append(pids, e.Pid)
		}
	}
	return pids
}

// didOpenOf は、ログ path から、URI が uri の didOpen を待って返す。
func (h *host) didOpenOf(path, uri string) (didOpenParams, int) {
	h.t.Helper()
	e := h.waitLogFile(path, "didOpen of "+uri, func(e logEntry) bool {
		return e.Kind == lspDidOpen && decode[didOpenParams](h.t, e.Value).TextDocument.URI == uri
	})
	return decode[didOpenParams](h.t, e.Value), e.Pid
}

// rootURIOf は、ログ path から、プロセス pid が受け取った initialize の rootUri を返す。
func (h *host) rootURIOf(path string, pid int) string {
	h.t.Helper()
	e := h.waitLogFile(path, "initialize", func(e logEntry) bool { return e.Kind == lspInitialize && e.Pid == pid })
	var ip struct {
		RootURI string `json:"rootUri"`
	}
	if err := json.Unmarshal(e.Value, &ip); err != nil {
		h.t.Fatal(err)
	}
	return ip.RootURI
}

// TestServerPerRoot は、1 つの言語サーバーの設定が、同じ根の 2 つのファイルの種類を 1 つのプロセスで受け持ち、
// languageId をファイルの種類ごとに送ること、根の違うファイルには別のプロセスを起動することを確かめる。
//
// req: FR-320
func TestServerPerRoot(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "ts.log")
	h := startHost(t, hostOptions{maxRestarts: -1, servers: map[string]any{
		"vtsls": fakeServer(logPath, []string{"typescript", "typescriptreact"}, nil),
	}})
	h.touch("p1/package.json", 0o600)
	h.touch("p2/package.json", 0o600)
	a := h.openAs(1, "p1/src/a.ts", "typescript", "a")
	b := h.openAs(2, "p1/b.tsx", "typescriptreact", "b")

	openA, pidA := h.didOpenOf(logPath, pathToURI(a))
	openB, pidB := h.didOpenOf(logPath, pathToURI(b))
	if pidA != pidB {
		t.Errorf("a.ts and b.tsx under the same root are sent to different processes %d and %d", pidA, pidB)
	}
	if openA.TextDocument.LanguageID != "typescript" || openB.TextDocument.LanguageID != "typescriptreact" {
		t.Errorf("languageId = %q, %q; want typescript, typescriptreact", openA.TextDocument.LanguageID, openB.TextDocument.LanguageID)
	}
	if got, want := h.rootURIOf(logPath, pidA), pathToURI(filepath.Join(h.dir, "p1")); got != want {
		t.Errorf("rootUri = %q, want %q", got, want)
	}

	c := h.openAs(3, "p2/c.ts", "typescript", "c")
	_, pidC := h.didOpenOf(logPath, pathToURI(c))
	if pidC == pidA {
		t.Errorf("c.ts under another root is sent to the process of p1")
	}
	if got, want := h.rootURIOf(logPath, pidC), pathToURI(filepath.Join(h.dir, "p2")); got != want {
		t.Errorf("rootUri of the second process = %q, want %q", got, want)
	}
	if pids := startedPids(logPath); len(pids) != 2 {
		t.Errorf("started processes = %v, want 2", pids)
	}
	// 既定の go の言語サーバー（startHost の fakels）は起動しない。
	if es := h.readLog(); len(es) != 0 {
		t.Errorf("go server log = %d entries, want none", len(es))
	}

	// 同じプロセスの中で、ファイルの種類だけを変えると、新しい languageId で開き直す。
	if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Path: a, FileType: "typescriptreact", Version: 1, Text: "a"}); err != nil {
		t.Fatal(err)
	}
	h.waitLogFile(logPath, "didOpen of a.ts as typescriptreact", func(e logEntry) bool {
		if e.Kind != lspDidOpen || e.Pid != pidA {
			return false
		}
		p := decode[didOpenParams](t, e.Value)
		return p.TextDocument.URI == pathToURI(a) && p.TextDocument.LanguageID == "typescriptreact"
	})
}

// TestParseSettingsServers は、言語サーバーの名前と fileTypes の読み方を確かめる。
//
// req: FR-320
func TestParseSettingsServers(t *testing.T) {
	c, err := parseSettings(json.RawMessage(`{"servers": {
		"vtsls": {"command": "vtsls", "fileTypes": ["typescript", "typescriptreact"]},
		"gopls": {"command": "gopls", "fileTypes": ["go", "gomod"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"typescript": "vtsls", "typescriptreact": "vtsls", "go": "gopls", "gomod": "gopls"}
	if len(c.byFileType) != len(want) {
		t.Errorf("byFileType = %v, want %v", c.byFileType, want)
	}
	for ft, name := range want {
		if c.byFileType[ft] != name {
			t.Errorf("byFileType[%s] = %q, want %q", ft, c.byFileType[ft], name)
		}
	}
	// 既定の go は、利用者の gopls が go を受け持つので使わない。
	if c.servers["go"] != nil {
		t.Errorf("default go server is kept: %+v", c.servers["go"])
	}
	if got := c.servers["vtsls"].languageID("typescriptreact"); got != "typescriptreact" {
		t.Errorf("languageID = %q", got)
	}
	if _, err := parseSettings(json.RawMessage(`{"servers": {"a": {"command": "a", "fileTypes": ["x"]}, "b": {"command": "b", "fileTypes": ["x"]}}}`)); err == nil {
		t.Error("two servers for one file type: err = nil")
	}
	for _, bad := range []string{"python", ".pythonPath", "python.", "python..x"} {
		raw := `{"servers": {"py": {"command": "p", "pythonVenvSetting": "` + bad + `"}}}`
		if _, err := parseSettings(json.RawMessage(raw)); err == nil {
			t.Errorf("pythonVenvSetting %q: err = nil", bad)
		}
	}
}

// TestLookupSection は、workspace/configuration の section を、鍵そのものと "." で区切った入れ子から引くことを確かめる。
//
// req: FR-321
func TestLookupSection(t *testing.T) {
	s := map[string]json.RawMessage{
		"python":          json.RawMessage(`{"pythonPath":"/v/bin/python","analysis":{"typeCheckingMode":"basic","n":12345678901234567890}}`),
		"python.analysis": json.RawMessage(`{"own":true}`),
		"gopls":           json.RawMessage(`{"hints":{"x":true}}`),
	}
	for _, tc := range []struct{ section, want string }{
		{"python.analysis", `{"own":true}`},
		{"python.pythonPath", `"/v/bin/python"`},
		{"python.analysis.typeCheckingMode", `"basic"`},
		{"gopls.hints", `{"x":true}`},
		{"gopls.none", `null`},
		{"none", `null`},
		{"", `null`},
	} {
		if got := string(lookupSection(s, tc.section)); got != tc.want {
			t.Errorf("lookupSection(%q) = %s, want %s", tc.section, got, tc.want)
		}
	}
	delete(s, "python.analysis")
	if got := string(lookupSection(s, "python.analysis")); !strings.Contains(got, `12345678901234567890`) {
		t.Errorf("lookupSection(python.analysis) = %s, want the large number kept", got)
	}
}

// TestPythonVenvSetting は、根から探した仮想環境の python が、設定の場所に入って workspace/configuration の答えになること、
// uv のワークスペースのメンバー（自分の .venv を持たない）から、ワークスペースの根の .venv に届くこと、
// 見つからなければ入れないこと、lsp.root の引数なしで仮想環境を出すことを確かめる。
//
// req: FR-321, FR-322
func TestPythonVenvSetting(t *testing.T) {
	t.Setenv("VIRTUAL_ENV", "")
	logPath := filepath.Join(t.TempDir(), "py.log")
	h := startHost(t, hostOptions{maxRestarts: -1, servers: map[string]any{
		"pyright": fakeServer(logPath, []string{"python"}, map[string]any{
			"rootMarkers":       []string{"pyproject.toml"},
			"pythonVenvSetting": "python.pythonPath",
			"settings":          map[string]any{"python": map[string]any{"analysis": map[string]any{"x": 1}}},
		}, "-config-sections", "python,python.analysis"),
	}})
	h.touch("ws/pyproject.toml", 0o600)
	h.touch("ws/pkg/pyproject.toml", 0o600)
	python := h.touch("ws/.venv/bin/python", 0o700)
	h.touch("other/pyproject.toml", 0o600)

	m := h.openAs(1, "ws/pkg/m.py", "python", "m")
	_, pid := h.didOpenOf(logPath, pathToURI(m))
	if got, want := h.rootURIOf(logPath, pid), pathToURI(filepath.Join(h.dir, "ws/pkg")); got != want {
		t.Errorf("rootUri = %q, want %q", got, want)
	}
	cfg := h.waitLogFile(logPath, "configuration result", func(e logEntry) bool { return e.Kind == "configuration result" && e.Pid == pid })
	var items []map[string]any
	if err := json.Unmarshal(cfg.Value, &items); err != nil || len(items) != 2 {
		t.Fatalf("configuration result = %s, %v", cfg.Value, err)
	}
	if items[0]["pythonPath"] != python || items[0]["analysis"] == nil || items[1]["x"] != float64(1) {
		t.Errorf("configuration result = %s, want pythonPath %s", cfg.Value, python)
	}

	if res := h.runRoot(1, ""); !res.OK {
		t.Errorf("lsp.root = %+v", res)
	}
	msg := receive(t, h.msgs, "message/show")
	if !strings.Contains(msg.Text, python) || !strings.Contains(msg.Text, filepath.Join(h.dir, "ws/pkg")) || !strings.Contains(msg.Text, "pyright") {
		t.Errorf("lsp.root message = %q", msg.Text)
	}

	o := h.openAs(2, "other/o.py", "python", "o")
	_, pidO := h.didOpenOf(logPath, pathToURI(o))
	cfg = h.waitLogFile(logPath, "configuration result", func(e logEntry) bool { return e.Kind == "configuration result" && e.Pid == pidO })
	if strings.Contains(string(cfg.Value), "pythonPath") {
		t.Errorf("configuration result without a virtual environment = %s", cfg.Value)
	}
	if res := h.runRoot(2, ""); !res.OK {
		t.Errorf("lsp.root = %+v", res)
	}
	if msg := receive(t, h.msgs, "message/show"); !strings.Contains(msg.Text, "仮想環境は見つからない") {
		t.Errorf("lsp.root message without a virtual environment = %q", msg.Text)
	}

	// 後から仮想環境を作り、今と同じ根を指定し直すと、言語サーバーを起動し直して仮想環境を探し直す。
	otherPython := h.touch("other/.venv/bin/python", 0o700)
	if res := h.runRoot(2, "."); !res.OK {
		t.Fatalf("lsp.root . = %+v", res)
	}
	if msg := receive(t, h.msgs, "message/show"); !strings.Contains(msg.Text, "起動し直した") {
		t.Errorf("lsp.root . message = %q", msg.Text)
	}
	h.waitLogFile(logPath, "shutdown of the old process", func(e logEntry) bool { return e.Kind == lspShutdown && e.Pid == pidO })
	reopened := h.waitLogFile(logPath, "didOpen in the restarted process", func(e logEntry) bool {
		return e.Kind == lspDidOpen && e.Pid != pidO && e.Pid != pid && decode[didOpenParams](t, e.Value).TextDocument.URI == pathToURI(o)
	})
	cfg = h.waitLogFile(logPath, "configuration result", func(e logEntry) bool {
		return e.Kind == "configuration result" && e.Pid == reopened.Pid
	})
	if !strings.Contains(string(cfg.Value), otherPython) {
		t.Errorf("configuration result after re-specifying the root = %s, want pythonPath %s", cfg.Value, otherPython)
	}
}

// TestPythonVenvFromVirtualEnv は、根の中に仮想環境が無く、VIRTUAL_ENV の仮想環境を使ったときに、
// lsp.root の表示でそれが分かることを確かめる。
//
// req: FR-321, FR-322
func TestPythonVenvFromVirtualEnv(t *testing.T) {
	venv := filepath.Join(t.TempDir(), "active")
	writeScript(t, filepath.Join(venv, "bin", "python"), 0o700)
	t.Setenv("VIRTUAL_ENV", venv)
	logPath := filepath.Join(t.TempDir(), "py.log")
	h := startHost(t, hostOptions{maxRestarts: -1, servers: map[string]any{
		"pyright": fakeServer(logPath, []string{"python"}, map[string]any{
			"rootMarkers": []string{"pyproject.toml"}, "pythonVenvSetting": "python.pythonPath",
		}),
	}})
	h.touch("p/pyproject.toml", 0o600)
	m := h.openAs(1, "p/m.py", "python", "m")
	h.didOpenOf(logPath, pathToURI(m))
	if res := h.runRoot(1, ""); !res.OK {
		t.Fatalf("lsp.root = %+v", res)
	}
	if msg := receive(t, h.msgs, "message/show"); !strings.Contains(msg.Text, filepath.Join(venv, "bin", "python")) || !strings.Contains(msg.Text, "VIRTUAL_ENV") {
		t.Errorf("lsp.root message = %q, want the python of VIRTUAL_ENV marked as such", msg.Text)
	}
}

// TestIdleStop は、受け持つ文書が無くなった言語サーバーを、設定の idleStopMs の後に止めること、
// その前に同じ根のファイルを開き直せば止めないことを確かめる。
//
// req: FR-323
func TestIdleStop(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "ts.log")
	h := startHost(t, hostOptions{maxRestarts: -1, settings: map[string]any{"idleStopMs": 300}, servers: map[string]any{
		"vtsls": fakeServer(logPath, []string{"typescript"}, nil),
	}})
	h.touch("p/package.json", 0o600)
	a := h.openAs(1, "p/a.ts", "typescript", "a")
	_, pid := h.didOpenOf(logPath, pathToURI(a))
	closeBuf := func(id ext.BufferID) {
		t.Helper()
		if err := h.conn.Notify(ext.MethodBufferClose, ext.BufferCloseParams{Buffer: id}); err != nil {
			t.Fatal(err)
		}
	}

	// 閉じてすぐに同じ根のファイルを開けば、同じプロセスが受け持ち、止めない。
	closeBuf(1)
	b := h.openAs(2, "p/b.ts", "typescript", "b")
	if _, got := h.didOpenOf(logPath, pathToURI(b)); got != pid {
		t.Errorf("b.ts went to %d, want the same process %d", got, pid)
	}
	time.Sleep(600 * time.Millisecond)
	for _, e := range readLogFile(logPath) {
		if e.Kind == lspShutdown {
			t.Fatalf("the language server was stopped while it had a document")
		}
	}

	// すべて閉じると、idleStopMs の後に止め、次に開くと新しいプロセスを起動する。
	closeBuf(2)
	h.waitLogFile(logPath, "shutdown after the idle time", func(e logEntry) bool { return e.Kind == lspShutdown && e.Pid == pid })
	c := h.openAs(3, "p/c.ts", "typescript", "c")
	if _, got := h.didOpenOf(logPath, pathToURI(c)); got == pid {
		t.Errorf("c.ts went to the stopped process %d", got)
	}
}

// runRoot は、コマンド lsp.root を、コマンドラインの引数 text（空なら引数なし）を付けて、バッファ buf で送る。
func (h *host) runRoot(buf ext.BufferID, text string) ext.CommandExecuteResult {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	p := ext.CommandExecuteParams{Buffer: buf, Version: h.vers[buf], ID: cmdRoot}
	if text != "" {
		args, err := json.Marshal(map[string]string{"text": text})
		if err != nil {
			h.t.Fatal(err)
		}
		p.Args = args
	}
	var res ext.CommandExecuteResult
	if err := h.conn.Call(ctx, ext.MethodCommandExecute, p, &res); err != nil {
		h.t.Fatalf("command/execute %s: %v", cmdRoot, err)
	}
	return res
}

// TestRootCommand は、lsp.root で根を指定すると、その下で開いているバッファが新しい根のプロセスへ移り、
// 前のプロセスへ didClose を送り、前のプロセスの診断とインレイヒントを消してから新しいプロセスが付け直すこと、
// 受け持つバッファの無くなったプロセスを止めること、--markers で目印の根へ戻すことを確かめる。
//
// req: FR-322
func TestRootCommand(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "ts.log")
	h := startHost(t, hostOptions{maxRestarts: -1, servers: map[string]any{
		"vtsls": fakeServer(logPath, []string{"typescript"}, map[string]any{"inlayHints": true}, "-inlay"),
	}})
	h.touch("mono/package.json", 0o600)
	h.touch("mono/a/package.json", 0o600)
	h.touch("mono/b/package.json", 0o600)
	xPath := h.openAs(1, "mono/a/x.ts", "typescript", "x ERR HINT")
	yPath := h.openAs(2, "mono/b/y.ts", "typescript", "y ERR HINT")
	_, pidA := h.didOpenOf(logPath, pathToURI(xPath))
	_, pidB := h.didOpenOf(logPath, pathToURI(yPath))
	if pidA == pidB {
		t.Fatal("x.ts and y.ts under different roots share a process")
	}
	h.waitDiagnostics(func(p ext.DiagnosticSetParams) bool { return len(p.Diagnostics) == 1 }, 1, 2)
	h.waitVirtualTexts(func(p ext.VirtualTextSetParams) bool { return len(p.VirtualTexts) > 0 }, 1, 2)

	// バッファのファイルの下に無いディレクトリは根にできない。
	if res := h.runRoot(1, "../../mono/b"); res.OK {
		t.Error("lsp.root to a directory that does not contain the file: ok")
	}
	receive(t, h.msgs, "message/show")
	if res := h.runRoot(1, "no-such-dir"); res.OK {
		t.Error("lsp.root to a missing directory: ok")
	}
	receive(t, h.msgs, "message/show")

	// 相対パスは、バッファのファイルのディレクトリから解く。
	if res := h.runRoot(1, ".."); !res.OK {
		t.Fatalf("lsp.root .. = %+v", res)
	}
	if msg := receive(t, h.msgs, "message/show"); !strings.Contains(msg.Text, "2 個") {
		t.Errorf("lsp.root message = %q", msg.Text)
	}
	mono := filepath.Join(h.dir, "mono")
	var pidM int
	for _, p := range []string{xPath, yPath} {
		h.waitLogFile(logPath, "didOpen in the new process", func(e logEntry) bool {
			if e.Kind != lspDidOpen || e.Pid == pidA || e.Pid == pidB || decode[didOpenParams](t, e.Value).TextDocument.URI != pathToURI(p) {
				return false
			}
			pidM = e.Pid
			return true
		})
	}
	if got := h.rootURIOf(logPath, pidM); got != pathToURI(mono) {
		t.Errorf("rootUri of the new process = %q, want %q", got, pathToURI(mono))
	}
	for _, pid := range []int{pidA, pidB} {
		h.waitLogFile(logPath, "shutdown of the old process", func(e logEntry) bool { return e.Kind == lspShutdown && e.Pid == pid })
	}
	// 前のプロセスには、移したバッファの didClose を送る。
	for pid, p := range map[int]string{pidA: xPath, pidB: yPath} {
		h.waitLogFile(logPath, "didClose in the old process", func(e logEntry) bool {
			return e.Kind == lspDidClose && e.Pid == pid && decode[didCloseParams](t, e.Value).TextDocument.URI == pathToURI(p)
		})
	}
	// 移したバッファの診断とインレイヒントは、まず前のプロセスの分を空の一覧で消し、その後に新しいプロセスが付け直す。
	waitClearedThenSet(h, func(p ext.DiagnosticSetParams) int { return len(p.Diagnostics) }, h.diags, "diagnostic/set", 1, 2)
	waitClearedThenSet(h, func(p ext.VirtualTextSetParams) int { return len(p.VirtualTexts) }, h.vtexts, "virtualText/set", 1, 2)

	// 指定した根の下で後から開くファイルも、その根になる。
	zPath := h.openAs(3, "mono/a/z.ts", "typescript", "z")
	if _, pid := h.didOpenOf(logPath, pathToURI(zPath)); pid != pidM {
		t.Errorf("z.ts under the pinned root went to %d, want %d", pid, pidM)
	}
	if res := h.runRoot(3, ""); !res.OK {
		t.Errorf("lsp.root = %+v", res)
	}
	if msg := receive(t, h.msgs, "message/show"); !strings.Contains(msg.Text, mono+"（指定した）") {
		t.Errorf("lsp.root message = %q", msg.Text)
	}

	// 目印の根へ戻すと、a と b の 2 つのプロセスに分かれ、mono のプロセスを止める。
	if res := h.runRoot(1, rootArgMarkers); !res.OK {
		t.Fatalf("lsp.root --markers = %+v", res)
	}
	receive(t, h.msgs, "message/show")
	h.waitLogFile(logPath, "shutdown of the pinned process", func(e logEntry) bool { return e.Kind == lspShutdown && e.Pid == pidM })
	// x.ts と y.ts が、a、b、mono のどれでもない新しいプロセスへ開き直されるまで待ってから数える。
	newPids := map[int]bool{}
	for _, p := range []string{xPath, yPath} {
		h.waitLogFile(logPath, "didOpen in the process of the marker root", func(e logEntry) bool {
			if e.Kind != lspDidOpen || e.Pid == pidA || e.Pid == pidB || e.Pid == pidM ||
				decode[didOpenParams](t, e.Value).TextDocument.URI != pathToURI(p) {
				return false
			}
			newPids[e.Pid] = true
			return true
		})
	}
	if len(newPids) != 2 {
		t.Errorf("x.ts and y.ts after --markers went to %v, want 2 processes", newPids)
	}
	if pids := startedPids(logPath); len(pids) != 5 {
		t.Errorf("started processes = %v, want 5 (a, b, mono, a, b)", pids)
	}
	h.x.mu.Lock()
	var roots []string
	for k := range h.x.clients {
		roots = append(roots, k.root)
	}
	h.x.mu.Unlock()
	slices.Sort(roots)
	if want := []string{filepath.Join(mono, "a"), filepath.Join(mono, "b")}; !slices.Equal(roots, want) {
		t.Errorf("client roots = %v, want %v", roots, want)
	}

	// 言語サーバーの無いバッファでは、使えないことを知らせる。
	h.openAs(4, "n.txt", "text", "n")
	if res := h.runRoot(4, ""); res.OK {
		t.Error("lsp.root without a language server: ok")
	}
	receive(t, h.msgs, "message/show")
}

// waitVirtualTexts は、バッファ bufs のそれぞれに、cond を満たす virtualText/set が届くまで待つ。
func (h *host) waitVirtualTexts(cond func(ext.VirtualTextSetParams) bool, bufs ...ext.BufferID) {
	h.t.Helper()
	deadline := time.After(testTimeout)
	for len(bufs) > 0 {
		select {
		case p := <-h.vtexts:
			if cond(p) {
				bufs = slices.DeleteFunc(bufs, func(b ext.BufferID) bool { return b == p.Buffer })
			}
		case <-deadline:
			h.t.Fatalf("virtualText/set of buffers %v did not arrive", bufs)
		}
	}
}

// bufferParams は、バッファの番号を持つ通知の params である。
type bufferParams interface {
	ext.DiagnosticSetParams | ext.VirtualTextSetParams
}

// waitClearedThenSet は、ch に届く通知のうち、バッファ bufs のそれぞれについて、最初の 1 つが空の一覧（count が 0）で、
// その後に空でない一覧が届くまで待つ。最初の 1 つが空でなければ失敗にする。
func waitClearedThenSet[T bufferParams](h *host, count func(T) int, ch <-chan T, what string, bufs ...ext.BufferID) {
	h.t.Helper()
	cleared := map[ext.BufferID]bool{}
	pending := slices.Clone(bufs)
	deadline := time.After(testTimeout)
	for len(pending) > 0 {
		select {
		case p := <-ch:
			var id ext.BufferID
			switch v := any(p).(type) {
			case ext.DiagnosticSetParams:
				id = v.Buffer
			case ext.VirtualTextSetParams:
				id = v.Buffer
			}
			if !slices.Contains(pending, id) {
				continue
			}
			switch {
			case !cleared[id] && count(p) != 0:
				h.t.Fatalf("%s of buffer %d: the first one after moving is not empty: %+v", what, id, p)
			case !cleared[id]:
				cleared[id] = true
			case count(p) > 0:
				pending = slices.DeleteFunc(pending, func(b ext.BufferID) bool { return b == id })
			}
		case <-deadline:
			h.t.Fatalf("%s of buffers %v did not arrive", what, pending)
		}
	}
}

// waitDiagnostics は、バッファ bufs のそれぞれに、cond を満たす診断が届くまで待つ。
func (h *host) waitDiagnostics(cond func(ext.DiagnosticSetParams) bool, bufs ...ext.BufferID) {
	h.t.Helper()
	deadline := time.After(testTimeout)
	for len(bufs) > 0 {
		select {
		case p := <-h.diags:
			if cond(p) {
				bufs = slices.DeleteFunc(bufs, func(b ext.BufferID) bool { return b == p.Buffer })
			}
		case <-deadline:
			h.t.Fatalf("diagnostics of buffers %v did not arrive", bufs)
		}
	}
}
