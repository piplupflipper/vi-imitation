package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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

const testTimeout = 10 * time.Second

// fakeLS は、テストの前に testdata/fakels をビルドした実行ファイルのパスである。
var fakeLS string

// envRunExtension は、テストの実行ファイルを拡張機能そのものとして動かす環境変数である（TestKillLeavesNoServer）。
const envRunExtension = "VI_IMITATION_LSP_TEST_RUN_EXTENSION"

func TestMain(m *testing.M) {
	if os.Getenv(envRunExtension) == "1" {
		if err := run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "fakels")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeLS = filepath.Join(dir, "fakels")
	build := exec.Command("go", "build", "-o", fakeLS, "./testdata/fakels") //nolint:gosec // テスト用の言語サーバーを一時ディレクトリへビルドする
	out, err := build.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build fakels: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// host は、テストで本体の役をする接続と、拡張機能から届いたメッセージの通り道である。
type host struct {
	t       *testing.T
	x       *extension
	conn    *rpc.Conn
	dir     string
	logPath string

	diags   chan ext.DiagnosticSetParams
	popups  chan ext.PopupShowParams
	msgs    chan ext.MessageShowParams
	prog    chan ext.ProgressReportParams
	opens   chan ext.EditorOpenParams
	edits   chan ext.BufferEditParams
	choices chan ext.ChoiceAskParams
	saves   chan ext.BufferSaveParams
	vtexts  chan ext.VirtualTextSetParams

	// mu は、本体の役が editor/open（focus が false）で開いたファイルの記録（offscreen）と、editor/state で答える
	// バッファの状態（bufs）を守る。開いたファイルの番号は offscreenBase から振る。
	mu        sync.Mutex
	offscreen map[string]ext.BufferID
	bufs      map[ext.BufferID]*simBuffer
	// sim は、buffer/edit を本体の役のバッファ（bufs）に適用し、buffer/change を拡張機能へ送るかである（hostOptions.sim）。
	sim bool
	// readOnly と failEdit は、hostOptions の同じ名前の項目である。
	readOnly map[string]bool
	failEdit map[string]bool

	// choose は、choice/ask に答える選択肢の ID である。
	choose string
	// texts は、本体の役が持つバッファの全文である。
	texts map[ext.BufferID]string
	vers  map[ext.BufferID]ext.Version
}

// hostOptions は、startHost の設定である。
type hostOptions struct {
	// args は、fakels に渡す引数である（-log は startHost が足す）。
	args []string
	// extra は、servers.go に足す設定の項目である。
	extra map[string]any
	// maxRestarts は、settings の maxRestarts である。負なら省く。
	maxRestarts int
	choose      string
	// resetAfter は、正なら client の resetAfter を置き換える。
	resetAfter time.Duration
	// sim は、本体の役が buffer/edit を自分のバッファに適用してバージョン番号を進め、buffer/change を送るかである。
	// 名前の変更を元に戻す流れ（バッファの複製の版を待つ）を試すのに使う。
	sim bool
	// readOnly は、編集を禁止したバッファにするファイルの名前（パスの最後の要素）である。
	// editor/state の readOnly で答え、buffer/edit を E21 で断る。
	readOnly []string
	// failEdit は、buffer/edit を断るファイルの名前である（editor/state では編集できると答える）。
	failEdit []string
	// secondFileType は、空でなければ、go とは別の client で fakels を言語サーバーにするファイルの種類である。
	// その fakels は args を受け取り、ログを secondLogPath に書く。
	secondFileType string
	// secondExtra は、secondFileType の言語サーバーの設定に足す項目である。
	secondExtra map[string]any
	// settings は、settings の最上位に足す項目である。
	settings map[string]any
	// servers は、servers に足す言語サーバーの設定である（鍵は言語サーバーの名前）。
	servers map[string]any
	// mise は、空でなければ client の mise（mise の実行ファイル）を置き換える。
	mise string
	// inlayInterval は、正なら client の inlayInterval（インレイヒントの問い合わせの間隔）を置き換える。
	inlayInterval time.Duration
}

// simBuffer は、本体の役が持つバッファの状態である（editor/state、sim の buffer/edit）。
type simBuffer struct {
	path    string
	text    string
	version ext.Version
}

// startHost は、拡張機能を io.Pipe の上で動かし、fakels を go の言語サーバーにする設定で initialize する。
// 作業のディレクトリには go.mod を置き、ワークスペースの根の目印にする。
func startHost(t *testing.T, o hostOptions) *host {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &host{
		t: t, dir: dir, logPath: filepath.Join(dir, "fakels.log"),
		diags: make(chan ext.DiagnosticSetParams, 100), popups: make(chan ext.PopupShowParams, 100),
		msgs: make(chan ext.MessageShowParams, 100), prog: make(chan ext.ProgressReportParams, 100), opens: make(chan ext.EditorOpenParams, 100),
		edits: make(chan ext.BufferEditParams, 100), choices: make(chan ext.ChoiceAskParams, 100),
		saves: make(chan ext.BufferSaveParams, 100), vtexts: make(chan ext.VirtualTextSetParams, 100), offscreen: map[string]ext.BufferID{},
		bufs: map[ext.BufferID]*simBuffer{}, sim: o.sim, readOnly: map[string]bool{}, failEdit: map[string]bool{},
		choose: o.choose, texts: map[ext.BufferID]string{}, vers: map[ext.BufferID]ext.Version{},
	}
	for _, n := range o.readOnly {
		h.readOnly[n] = true
	}
	for _, n := range o.failEdit {
		h.failEdit[n] = true
	}
	gocfg := map[string]any{
		"command":  fakeLS,
		"args":     append([]string{"-log", h.logPath}, o.args...),
		"settings": map[string]any{"fake": map[string]any{"x": 1}},
	}
	for k, v := range o.extra {
		gocfg[k] = v
	}
	servers := map[string]any{"go": gocfg}
	if o.secondFileType != "" {
		second := map[string]any{
			"command":    fakeLS,
			"args":       append([]string{"-log", h.secondLogPath()}, o.args...),
			"languageId": o.secondFileType,
		}
		maps.Copy(second, o.secondExtra)
		servers[o.secondFileType] = second
	}
	maps.Copy(servers, o.servers)
	settings := map[string]any{"servers": servers}
	maps.Copy(settings, o.settings)
	if o.maxRestarts >= 0 {
		settings["maxRestarts"] = o.maxRestarts
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}

	hostR, extW := io.Pipe()
	extR, hostW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	x := newExtension(ctx)
	h.x = x
	x.log.SetOutput(io.Discard)
	x.requestTimeout = testTimeout
	x.tune = func(c *client) {
		c.restartDelay = 10 * time.Millisecond
		if o.resetAfter > 0 {
			c.resetAfter = o.resetAfter
		}
		if o.mise != "" {
			c.mise = o.mise
		}
		if o.inlayInterval > 0 {
			c.inlayInterval = o.inlayInterval
		}
	}
	served := make(chan error, 1)
	go func() { served <- x.srv.Serve(ctx, extR, extW) }()
	h.conn = rpc.NewConn(hostR, hostW, rpc.HandlerFuncs{
		Request:      h.request,
		Notification: h.notification,
	})
	t.Cleanup(func() {
		_ = h.conn.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve = %v, want nil", err)
			}
		case <-time.After(testTimeout):
			t.Error("Serve did not return after the host closed")
		}
		x.stop()
		cancel()
	})
	cctx, ccancel := context.WithTimeout(context.Background(), testTimeout)
	defer ccancel()
	var res ext.InitializeResult
	if err := h.conn.Call(cctx, ext.MethodInitialize, ext.InitializeParams{APIVersion: ext.APIVersion, Settings: raw}, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return h
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Errorf("decode %s: %v", raw, err)
	}
	return v
}

func (h *host) request(_ context.Context, req *rpc.Request, reply rpc.Replier) {
	switch req.Method {
	case ext.MethodEditorOpen:
		p := decode[ext.EditorOpenParams](h.t, req.Params)
		h.opens <- p
		if p.Path != "" && !p.Focus {
			// 本体と同じく、画面に出さないバッファとして開き、応答の前に buffer/open を送る（FR-109）。
			id, err := h.openOffscreen(p.Path)
			_ = reply(ext.EditorOpenResult{Buffer: id, Version: 1}, err)
			return
		}
		_ = reply(ext.EditorOpenResult{Moved: true}, nil)
	case ext.MethodEditorState:
		// 本体の役が開いたバッファ（テストの open と、editor/open で開いたもの）を答える。
		res := ext.EditorStateResult{Buffers: []ext.BufferInfo{}}
		h.mu.Lock()
		for _, id := range slices.Sorted(maps.Keys(h.bufs)) {
			b := h.bufs[id]
			res.Buffers = append(res.Buffers, ext.BufferInfo{
				Buffer: id, Path: b.path, FileType: "go", Version: b.version, ReadOnly: h.readOnly[filepath.Base(b.path)],
			})
		}
		h.mu.Unlock()
		_ = reply(res, nil)
	case ext.MethodBufferSave:
		p := decode[ext.BufferSaveParams](h.t, req.Params)
		h.saves <- p
		v := ext.Version(0)
		if p.Version != nil {
			v = *p.Version
		}
		_ = reply(ext.BufferSaveResult{Saved: true, Version: v}, nil)
	case ext.MethodBufferEdit:
		p := decode[ext.BufferEditParams](h.t, req.Params)
		h.edits <- p
		res, err := h.simEdit(p)
		_ = reply(res, err)
	case ext.MethodChoiceAsk:
		p := decode[ext.ChoiceAskParams](h.t, req.Params)
		h.choices <- p
		_ = reply(ext.ChoiceAskResult{Selected: h.choose}, nil)
	default:
		_ = reply(nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: req.Method})
	}
}

func (h *host) notification(_ context.Context, n *rpc.Notification) {
	switch n.Method {
	case ext.MethodDiagnosticSet:
		h.diags <- decode[ext.DiagnosticSetParams](h.t, n.Params)
	case ext.MethodPopupShow:
		h.popups <- decode[ext.PopupShowParams](h.t, n.Params)
	case ext.MethodMessageShow:
		h.msgs <- decode[ext.MessageShowParams](h.t, n.Params)
	case ext.MethodProgressReport:
		h.prog <- decode[ext.ProgressReportParams](h.t, n.Params)
	case ext.MethodVirtualTextSet:
		h.vtexts <- decode[ext.VirtualTextSetParams](h.t, n.Params)
	}
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(testTimeout):
		t.Fatalf("%s did not arrive", what)
		var zero T
		return zero
	}
}

// open は、ファイル name を書き、その全文 text（末尾の改行を除いたもの）でバッファ id を開く。
func (h *host) open(id ext.BufferID, name, text string) string {
	h.t.Helper()
	return h.openAs(id, name, "go", text)
}

// openAs は、ファイルの種類を fileType にした open である。name の途中のディレクトリは作る。
func (h *host) openAs(id ext.BufferID, name, fileType, text string) string {
	h.t.Helper()
	path := filepath.Join(h.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text+"\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.texts[id], h.vers[id] = text, 1
	h.mu.Lock()
	h.bufs[id] = &simBuffer{path: path, text: text, version: 1}
	h.mu.Unlock()
	if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: id, Path: path, FileType: fileType, Version: 1, Text: text}); err != nil {
		h.t.Fatal(err)
	}
	return path
}

// offscreenBase は、本体の役が editor/open（focus が false）で開いたファイルに振る番号の始まりである。
const offscreenBase ext.BufferID = 100

// openOffscreen は、本体の役が path を画面に出さないバッファとして開き、buffer/open を送って番号を返す（FR-109）。
// 拡張機能の要求の処理の中から呼ぶ。
func (h *host) openOffscreen(path string) (ext.BufferID, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	}
	text := strings.TrimSuffix(string(data), "\n")
	h.mu.Lock()
	id, ok := h.offscreen[path]
	if !ok {
		id = offscreenBase + ext.BufferID(len(h.offscreen))
		h.offscreen[path] = id
		h.bufs[id] = &simBuffer{path: path, text: text, version: 1}
	}
	h.mu.Unlock()
	if !ok {
		if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: id, Path: path, FileType: "go", Version: 1, Text: text}); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// change は、バッファ id に編集の列を適用し、buffer/change を送る。
func (h *host) change(id ext.BufferID, edits ...ext.Edit) {
	h.t.Helper()
	from := h.vers[id]
	h.texts[id] = applyEdits(h.texts[id], edits)
	h.vers[id] = from + 1
	h.mu.Lock()
	if b := h.bufs[id]; b != nil {
		b.text, b.version = h.texts[id], from+1
	}
	h.mu.Unlock()
	if err := h.conn.Notify(ext.MethodBufferChange, ext.BufferChangeParams{Buffer: id, FromVersion: from, ToVersion: from + 1, Edits: edits}); err != nil {
		h.t.Fatal(err)
	}
}

// simEdit は、本体の役の buffer/edit の応答を作る。readOnly のファイルは E21 で、failEdit のファイルは誤りで断る。
// sim なら、バッファの状態に適用してバージョン番号を進め、buffer/change を拡張機能へ送る。
// sim でなければ、適用したものとして BaseVersion の次の版を答える（状態のバージョン番号だけを進める）。
func (h *host) simEdit(p ext.BufferEditParams) (ext.BufferEditResult, error) {
	h.mu.Lock()
	b := h.bufs[p.Buffer]
	if b == nil {
		h.mu.Unlock()
		return ext.BufferEditResult{Applied: true, Version: p.Transaction.BaseVersion + 1}, nil
	}
	name := filepath.Base(b.path)
	switch {
	case h.readOnly[name]:
		h.mu.Unlock()
		return ext.BufferEditResult{}, &rpc.Error{Code: -32001, Message: "E21: 編集を禁止したバッファは変更できません"}
	case h.failEdit[name]:
		h.mu.Unlock()
		return ext.BufferEditResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "rejected by the test"}
	case !h.sim:
		h.mu.Unlock()
		return ext.BufferEditResult{Applied: true, Version: p.Transaction.BaseVersion + 1}, nil
	case p.Transaction.BaseVersion != b.version:
		v := b.version
		h.mu.Unlock()
		return ext.BufferEditResult{Applied: false, Version: v}, nil
	}
	from := b.version
	b.version++
	b.text = applyEdits(b.text, p.Transaction.Edits)
	to := b.version
	h.mu.Unlock()
	if err := h.conn.Notify(ext.MethodBufferChange, ext.BufferChangeParams{Buffer: p.Buffer, FromVersion: from, ToVersion: to, Edits: p.Transaction.Edits}); err != nil {
		return ext.BufferEditResult{}, err
	}
	return ext.BufferEditResult{Applied: true, Version: to}, nil
}

// simText は、本体の役のバッファ id の全文を返す（sim のとき、適用した buffer/edit を含む）。
func (h *host) simText(id ext.BufferID) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if b := h.bufs[id]; b != nil {
		return b.text
	}
	return ""
}

// テストで問い合わせる本体の操作。段階 2 のコマンド（lsp.definition など）を置き換えたもの（#203）。
const (
	cmdDefinition = ext.OpDefinition
	cmdHover      = ext.OpHover
	cmdFormat     = ext.OpFormat
)

// command は、本体の操作 op の provider/invoke を送り、応答を返す。
func (h *host) command(id ext.BufferID, op ext.Operation, cursor ext.Position, sync bool) ext.ProviderInvokeResult {
	h.t.Helper()
	return h.invoke(ext.ProviderInvokeParams{Operation: op, Buffer: id, Version: h.vers[id], Cursor: cursor, Sync: sync})
}

// invoke は、provider/invoke を p で送り、応答を返す。
func (h *host) invoke(p ext.ProviderInvokeParams) ext.ProviderInvokeResult {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var res ext.ProviderInvokeResult
	if err := h.conn.Call(ctx, ext.MethodProviderInvoke, p, &res); err != nil {
		h.t.Fatalf("provider/invoke %s: %v", p.Operation, err)
	}
	return res
}

// diagnosticsAt は、バッファ id の版 v の diagnostic/set が届くまで待つ。
func (h *host) diagnosticsAt(id ext.BufferID, v ext.Version) ext.DiagnosticSetParams {
	h.t.Helper()
	for {
		p := receive(h.t, h.diags, "diagnostic/set")
		if p.Buffer == id && p.Version == v {
			return p
		}
	}
}

// logEntry は、fakels のログの 1 行である。
type logEntry struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
	// Pid は、その行を書いた fakels のプロセス ID である。
	Pid int `json:"pid"`
}

// secondLogPath は、hostOptions.secondFileType の fakels のログのパスである。
func (h *host) secondLogPath() string { return filepath.Join(h.dir, "fakels-second.log") }

// readLog は、fakels のログを読む。
func (h *host) readLog() []logEntry {
	h.t.Helper()
	return readLogFile(h.logPath)
}

// readLogFile は、パス path の fakels のログを読む。
func readLogFile(path string) []logEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var es []logEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var e logEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err == nil {
			es = append(es, e)
		}
	}
	return es
}

// waitLog は、fakels のログに cond を満たす行が現れるまで待ち、その行を返す。
func (h *host) waitLog(what string, cond func(logEntry) bool) logEntry {
	h.t.Helper()
	return h.waitLogFile(h.logPath, what, cond)
}

// waitLogFile は、パス path の fakels のログについての waitLog である。
func (h *host) waitLogFile(path, what string, cond func(logEntry) bool) logEntry {
	h.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		for _, e := range readLogFile(path) {
			if cond(e) {
				return e
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("fakels log: %s did not appear", what)
	return logEntry{}
}

type loggedText struct {
	URI     string `json:"uri"`
	Version int64  `json:"version"`
	Text    string `json:"text"`
}

// waitText は、fakels が版 v の全文を持った時点のログを待ち、その全文を返す。
func (h *host) waitText(v ext.Version) string {
	h.t.Helper()
	e := h.waitLog(fmt.Sprintf("text of version %d", v), func(e logEntry) bool {
		if e.Kind != "text" {
			return false
		}
		var lt loggedText
		return json.Unmarshal(e.Value, &lt) == nil && lt.Version == lspVersion(v)
	})
	return decode[loggedText](h.t, e.Value).Text
}

// applyEdits は、逐次の数え方の編集の列を全文に適用する（検証用）。
func applyEdits(text string, edits []ext.Edit) string {
	for _, e := range edits {
		s, en := byteOffset(text, e.Range.Start), byteOffset(text, e.Range.End)
		text = text[:s] + e.Text + text[en:]
	}
	return text
}

func byteOffset(text string, p ext.Position) int {
	off := 0
	for range p.Line {
		off += strings.IndexByte(text[off:], '\n') + 1
	}
	return off + p.Byte
}

// positionOf は、text の中で n 番目（0 始まり）に現れる word の位置を、本体の位置で返す。
func positionOf(text, word string, n int) ext.Position {
	off := -1
	for range n + 1 {
		off += 1 + strings.Index(text[off+1:], word)
	}
	line := strings.Count(text[:off], "\n")
	return ext.Position{Line: line, Byte: off - (strings.LastIndex(text[:off], "\n") + 1)}
}

// wordRanges は、text の中の word の出現の範囲を、本体の位置で返す。
func wordRanges(text, word string) []ext.Range {
	var rs []ext.Range
	for i := range strings.Count(text, word) {
		s := positionOf(text, word, i)
		rs = append(rs, ext.Range{Start: s, End: ext.Position{Line: s.Line, Byte: s.Byte + len(word)}})
	}
	return rs
}

func pos(line, b int) ext.Position { return ext.Position{Line: line, Byte: b} }

func edit(sl, sb, el, eb int, text string) ext.Edit {
	return ext.Edit{Range: ext.Range{Start: pos(sl, sb), End: pos(el, eb)}, Text: text}
}

// checkDiagnostics は、診断の範囲が、全文 text の中の ERR と WARN の出現と一致することを確かめる。
func checkDiagnostics(t *testing.T, p ext.DiagnosticSetParams, text string) {
	t.Helper()
	var errs, warns []ext.Range
	for _, d := range p.Diagnostics {
		switch d.Severity {
		case ext.SeverityError:
			errs = append(errs, d.Range)
			if d.Source != "fakels" || d.Message != "err" {
				t.Errorf("error diagnostic = %+v", d)
			}
		case ext.SeverityWarning:
			warns = append(warns, d.Range)
			if d.Code != "42" {
				t.Errorf("warning code = %q, want 42", d.Code)
			}
		case ext.SeverityInfo, ext.SeverityHint:
			t.Errorf("unexpected severity %q", d.Severity)
		}
	}
	if want := wordRanges(text, "ERR"); !slices.Equal(errs, want) {
		t.Errorf("version %d: error ranges = %v, want %v (text %q)", p.Version, errs, want, text)
	}
	if want := wordRanges(text, "WARN"); !slices.Equal(warns, want) {
		t.Errorf("version %d: warning ranges = %v, want %v (text %q)", p.Version, warns, want, text)
	}
}

// syncText は、多バイト文字（3 バイトの「日」と、UTF-16 でサロゲートペアになる 4 バイトの「𝄞」）を含む全文である。
const syncText = "package a\n\n// 日本𝄞 ERR\nfunc f() {}"

// TestInitializeAndSync は、initialize の params、didOpen と増分の didChange の内容（UTF-16 と UTF-8 の位置）、
// 診断の位置の変換、workspace/configuration の応答を確かめる。
// fakels は、受け取った contentChanges を自分の数え方で適用した全文をログに書くので、
// それが本体の全文に改行を 1 つ足したものと一致すれば、didChange の範囲が正しく変換されている。
//
// req: FR-30
func TestInitializeAndSync(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"utf-16", nil},
		{"utf-8", []string{"-encoding", "utf-8"}},
		{"omitted encoding is utf-16", []string{"-encoding", "none"}},
		{"full sync", []string{"-sync", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startHost(t, hostOptions{args: tc.args, maxRestarts: -1})
			h.open(1, "a.go", syncText)

			init := h.waitLog("initialize", func(e logEntry) bool { return e.Kind == "initialize" })
			var ip struct {
				RootURI      string `json:"rootUri"`
				Capabilities struct {
					General struct {
						PositionEncodings []string `json:"positionEncodings"`
					} `json:"general"`
				} `json:"capabilities"`
			}
			if err := json.Unmarshal(init.Value, &ip); err != nil {
				t.Fatal(err)
			}
			if ip.RootURI != pathToURI(h.dir) {
				t.Errorf("rootUri = %q, want %q", ip.RootURI, pathToURI(h.dir))
			}
			if want := []string{"utf-8", "utf-16"}; !slices.Equal(ip.Capabilities.General.PositionEncodings, want) {
				t.Errorf("positionEncodings = %v, want %v", ip.Capabilities.General.PositionEncodings, want)
			}

			if got := h.waitText(1); got != syncText+"\n" {
				t.Errorf("didOpen text = %q, want %q", got, syncText+"\n")
			}
			checkDiagnostics(t, h.diagnosticsAt(1, 1), h.texts[1])

			// 1 つの buffer/change の中の複数の編集は、逐次の数え方で並ぶ。
			// 順に、サロゲートペアの直後への挿入、3 バイトの文字の置き換え、行をまたぐ削除、改行を含む挿入である。
			h.change(1,
				edit(2, len("// 日本𝄞"), 2, len("// 日本𝄞"), " WARN"),
				edit(2, len("// "), 2, len("// 日"), "ERR"),
				edit(0, 0, 1, 0, ""),
				edit(2, 0, 2, 0, "x𝄞\nERR "),
			)
			if got, want := h.waitText(2), h.texts[1]+"\n"; got != want {
				t.Errorf("text after didChange = %q, want %q", got, want)
			}
			checkDiagnostics(t, h.diagnosticsAt(1, 2), h.texts[1])

			// 最後の行の行末への挿入と、全体の削除。
			last := strings.Count(h.texts[1], "\n")
			lastLen := len(h.texts[1]) - (strings.LastIndex(h.texts[1], "\n") + 1)
			h.change(1, edit(last, lastLen, last, lastLen, "\n日ERR"))
			if got, want := h.waitText(3), h.texts[1]+"\n"; got != want {
				t.Errorf("text after appending = %q, want %q", got, want)
			}
			checkDiagnostics(t, h.diagnosticsAt(1, 3), h.texts[1])

			cfg := h.waitLog("configuration result", func(e logEntry) bool { return e.Kind == "configuration result" })
			if got := strings.Join(strings.Fields(string(cfg.Value)), ""); got != `[{"x":1},null]` {
				t.Errorf("workspace/configuration result = %s", cfg.Value)
			}
		})
	}
}

// TestCloseAndReopen は、buffer/close で didClose を送り、送り直し（Reopen）で全文の didChange を送ることを確かめる。
//
// req: FR-30
func TestCloseAndReopen(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: -1})
	path := h.open(1, "a.go", "a ERR")
	h.diagnosticsAt(1, 1)
	// 同じバッファの buffer/open は送り直しで、全文の didChange になる。
	if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Path: path, FileType: "go", Version: 5, Text: "b\nERR"}); err != nil {
		t.Fatal(err)
	}
	if got := h.waitText(5); got != "b\nERR\n" {
		t.Errorf("text after reopen = %q", got)
	}
	checkDiagnostics(t, h.diagnosticsAt(1, 5), "b\nERR")
	if err := h.conn.Notify(ext.MethodBufferClose, ext.BufferCloseParams{Buffer: 1}); err != nil {
		t.Fatal(err)
	}
	h.waitLog("didClose", func(e logEntry) bool { return e.Kind == lspDidClose })
	// 言語サーバーの無いファイルの種類とパスの無いバッファは同期しない。
	for _, p := range []ext.BufferOpenParams{
		{Buffer: 2, Path: filepath.Join(h.dir, "b.txt"), FileType: "text", Version: 1, Text: "ERR"},
		{Buffer: 3, FileType: "go", Version: 1, Text: "ERR"},
	} {
		if err := h.conn.Notify(ext.MethodBufferOpen, p); err != nil {
			t.Fatal(err)
		}
	}
	h.vers[3] = 1
	res := h.command(3, cmdHover, pos(0, 0), false)
	if res.OK {
		t.Error("hover on a buffer without a path: ok = true, want false")
	}
	for _, e := range h.readLog() {
		if e.Kind == lspDidOpen && strings.Contains(string(e.Value), "b.txt") {
			t.Errorf("didOpen sent for a text file: %s", e.Value)
		}
	}
}

// TestFileTypeAndPathChange は、本体がファイルの種類かパスを変えて buffer/open を送り直したとき（:set filetype=、別名での保存）、
// 前の文書の didClose を送り、空の diagnostic/set で診断を消すことを確かめる（#267）。
// ファイルの種類を戻すと didOpen を送り直して診断が付き直すことと、パスが変わったら新しい URI の文書として開き直すことも確かめる。
//
// req: FR-30
func TestFileTypeAndPathChange(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: -1})
	path := h.open(1, "a.go", "a ERR")
	checkDiagnostics(t, h.diagnosticsAt(1, 1), "a ERR")
	reopen := func(p ext.BufferOpenParams) {
		t.Helper()
		if err := h.conn.Notify(ext.MethodBufferOpen, p); err != nil {
			t.Fatal(err)
		}
	}
	didClose := func(uri string) {
		t.Helper()
		h.waitLog("didClose of "+uri, func(e logEntry) bool {
			return e.Kind == lspDidClose && decode[didCloseParams](t, e.Value).TextDocument.URI == uri
		})
	}
	opened := func(uri string) int {
		n := 0
		for _, e := range h.readLog() {
			if e.Kind == lspDidOpen && decode[didOpenParams](t, e.Value).TextDocument.URI == uri {
				n++
			}
		}
		return n
	}

	// 言語サーバーの無いファイルの種類に変えると、診断を消す。
	reopen(ext.BufferOpenParams{Buffer: 1, Path: path, FileType: "text", Version: 1, Text: "a ERR"})
	if got := h.diagnosticsAt(1, 1); len(got.Diagnostics) != 0 {
		t.Errorf("diagnostics after changing the file type to text = %+v, want cleared", got.Diagnostics)
	}
	didClose(pathToURI(path))

	// go に戻すと、新しい文書として didOpen を送り、診断が付き直す。
	reopen(ext.BufferOpenParams{Buffer: 1, Path: path, FileType: "go", Version: 1, Text: "a ERR"})
	checkDiagnostics(t, h.diagnosticsAt(1, 1), "a ERR")
	if n := opened(pathToURI(path)); n != 2 {
		t.Errorf("didOpen of %s sent %d times, want 2", path, n)
	}

	// 別名で保存してパスが変わると、前の URI の文書を閉じて診断を消し、新しい URI の文書を開く。
	moved := filepath.Join(h.dir, "b.go")
	reopen(ext.BufferOpenParams{Buffer: 1, Path: moved, FileType: "go", Version: 1, Text: "a ERR"})
	if got := h.diagnosticsAt(1, 1); len(got.Diagnostics) != 0 {
		t.Errorf("diagnostics after changing the path = %+v, want cleared", got.Diagnostics)
	}
	didClose(pathToURI(path))
	checkDiagnostics(t, h.diagnosticsAt(1, 1), "a ERR")
	if n := opened(pathToURI(moved)); n != 1 {
		t.Errorf("didOpen of %s sent %d times, want 1", moved, n)
	}

	// 本体が閉じたバッファ（buffer/close）には、空の診断を送らない（本体が自分で消す。基本設計 5.3）。
	if err := h.conn.Notify(ext.MethodBufferClose, ext.BufferCloseParams{Buffer: 1}); err != nil {
		t.Fatal(err)
	}
	didClose(pathToURI(moved))
	select {
	case p := <-h.diags:
		t.Errorf("diagnostic/set after buffer/close: %+v", p)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestFileTypeToAnotherServer は、言語サーバーのある別のファイルの種類（別の client）へ移したとき、
// 前の client の言語サーバーへ didClose を送り、空の診断の後に新しい client の言語サーバーの診断が届くことを確かめる（#267）。
// 前の言語サーバーが didClose の後に送る診断（fakels の -publish-on-close）は捨てる。
//
// req: FR-30
func TestFileTypeToAnotherServer(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: -1, args: []string{"-publish-on-close"}, secondFileType: "go2"})
	path := h.open(1, "a.go", "a ERR")
	checkDiagnostics(t, h.diagnosticsAt(1, 1), "a ERR")
	uri := pathToURI(path)

	if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Path: path, FileType: "go2", Version: 1, Text: "a ERR"}); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, h.diags, "diagnostic/set"); got.Buffer != 1 || got.Version != 1 || len(got.Diagnostics) != 0 {
		t.Errorf("first diagnostic/set after moving to go2 = %+v, want cleared", got)
	}
	h.waitLog("published on close", func(e logEntry) bool { return e.Kind == "published on close" })
	open := h.waitLogFile(h.secondLogPath(), "didOpen in the second server", func(e logEntry) bool { return e.Kind == lspDidOpen })
	if p := decode[didOpenParams](t, open.Value); p.TextDocument.URI != uri || p.TextDocument.LanguageID != "go2" {
		t.Errorf("didOpen in the second server = %s", open.Value)
	}
	got := receive(t, h.diags, "diagnostic/set")
	if got.Buffer != 1 || got.Version != 1 {
		t.Fatalf("diagnostic/set from the second server = %+v", got)
	}
	checkDiagnostics(t, got, "a ERR")
	// 前の言語サーバーが閉じた後に送った診断が届いていれば、もう 1 つ届く。
	select {
	case p := <-h.diags:
		t.Errorf("extra diagnostic/set after moving to go2: %+v", p)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestDidSave は、buffer/didSave で、保存した時点の全文を含む textDocument/didSave を送ることを確かめる。
//
// req: FR-30
func TestDidSave(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: -1})
	path := h.open(2, "b.go", "a")
	h.diagnosticsAt(2, 1)
	if err := h.conn.Notify(ext.MethodBufferDidSave, ext.BufferDidSaveParams{Buffer: 2, Version: 1, Path: path}); err != nil {
		t.Fatal(err)
	}
	e := h.waitLog("didSave", func(e logEntry) bool { return e.Kind == lspDidSave })
	p := decode[didSaveParams](t, e.Value)
	if p.TextDocument.URI != pathToURI(path) || p.Text == nil || *p.Text != "a\n" {
		t.Errorf("didSave = %s", e.Value)
	}
}

// TestDefinition は、定義へ移動の候補が 1 つなら、多バイト文字の後ろの位置を本体の位置に変換して editor/open で移動することと、
// sync が true なら応答の open で返すことを、Location と LocationLink の両方で確かめる。
//
// req: FR-30
func TestDefinition(t *testing.T) {
	for _, links := range []bool{false, true} {
		t.Run(fmt.Sprintf("links=%v", links), func(t *testing.T) {
			var args []string
			if links {
				args = append(args, "-links")
			}
			h := startHost(t, hostOptions{args: args, maxRestarts: -1})
			text := "日𝄞 use TARGET"
			h.open(1, "a.go", text)
			h.diagnosticsAt(1, 1)

			// 候補が 1 つ。
			if res := h.command(1, cmdDefinition, pos(0, 0), false); !res.OK {
				t.Fatal("definition: ok = false")
			}
			o := receive(t, h.opens, "editor/open")
			if o.Buffer == nil || *o.Buffer != 1 || o.Version == nil || *o.Version != 1 || o.Position == nil ||
				*o.Position != positionOf(text, "TARGET", 0) || !o.Focus {
				t.Errorf("editor/open = %+v", o)
			}

			// sync が true なら、最初の候補を応答の open で返す。
			res := h.command(1, cmdDefinition, pos(0, 0), true)
			if !res.OK || res.Open == nil || res.Open.Position == nil || *res.Open.Position != positionOf(text, "TARGET", 0) {
				t.Errorf("sync definition = %+v", res)
			}
		})
	}
}

// TestDefinitionPeek は、args の peek が true なら、移らずに、移動先を editor/open の peek で本体に見せることを確かめる（FR-110）。
// sync が true なら、peek を付けた移動先を応答の open で返す（#210 と同じく、再生の次の操作の前に本体が行う）。
//
// req: FR-94, FR-110
func TestDefinitionPeek(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: -1})
	text := "日𝄞 use TARGET\nnext line"
	h.open(1, "a.go", text)
	h.diagnosticsAt(1, 1)
	at := positionOf(text, "TARGET", 0)
	check := func(what string, o *ext.EditorOpenParams) {
		t.Helper()
		if o == nil || !o.Peek || o.Buffer == nil || *o.Buffer != 1 || o.Position == nil || *o.Position != at {
			t.Errorf("%s: open = %+v, want peek at %v", what, o, at)
		}
	}
	res := h.invoke(ext.ProviderInvokeParams{
		Operation: ext.OpDefinition, Buffer: 1, Version: h.vers[1], Cursor: pos(1, 2), Args: json.RawMessage(`{"peek":true}`),
	})
	if !res.OK || res.Open != nil {
		t.Fatalf("peek = %+v", res)
	}
	o := receive(t, h.opens, "editor/open")
	check("editor/open", &o)
	res = h.invoke(ext.ProviderInvokeParams{
		Operation: ext.OpDefinition, Buffer: 1, Version: h.vers[1], Cursor: pos(1, 2), Args: json.RawMessage(`{"peek":true}`), Sync: true,
	})
	check("sync", res.Open)
	if len(h.popups) != 0 {
		t.Errorf("peek sent popup/show")
	}
}

// TestDefinitionChoice は、候補が複数なら choice/ask で選ばせ、選んだ候補へ移動することと、
// 開いていないファイルの位置を、ファイルを読んで変換することを確かめる。
//
// req: FR-30
func TestDefinitionChoice(t *testing.T) {
	other := filepath.Join(t.TempDir(), "other.go")
	if err := os.WriteFile(other, []byte("x\n// 𝄞日 TARGET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := startHost(t, hostOptions{args: []string{"-defpath", other}, maxRestarts: -1, choose: "2"})
	text := "日TARGET𝄞 use TARGET"
	h.open(1, "a.go", text)
	h.diagnosticsAt(1, 1)
	if res := h.command(1, cmdDefinition, pos(0, 0), false); !res.OK {
		t.Fatal("definition: ok = false")
	}
	ask := receive(t, h.choices, "choice/ask")
	if len(ask.Choices) != 3 {
		t.Fatalf("choices = %+v, want 3", ask.Choices)
	}
	o := receive(t, h.opens, "editor/open")
	want := pos(1, len("// 𝄞日 "))
	if o.Path != other || o.Buffer != nil || o.Version != nil || o.Position == nil || *o.Position != want {
		t.Errorf("editor/open = %+v, want path %s position %+v", o, other, want)
	}
	if !strings.HasSuffix(ask.Choices[2].Label, fmt.Sprintf("other.go:%d:%d", want.Line+1, want.Byte+1)) {
		t.Errorf("label = %q", ask.Choices[2].Label)
	}

	// 候補が無ければ message/show で知らせる。
	h2 := startHost(t, hostOptions{maxRestarts: -1})
	h2.open(1, "a.go", "none")
	h2.diagnosticsAt(1, 1)
	if res := h2.command(1, cmdDefinition, pos(0, 0), false); !res.OK {
		t.Fatal("definition: ok = false")
	}
	if m := receive(t, h2.msgs, "message/show"); m.Text != "定義が見つからない" {
		t.Errorf("message = %+v", m)
	}
	if res := h2.command(1, cmdDefinition, pos(0, 0), true); res.OK {
		t.Error("sync definition without candidates: ok = true, want false")
	}
}

// TestHover は、カーソル位置を UTF-16 の位置に変換して textDocument/hover を送り、結果を popup/show で送ることを確かめる。
// sync が true（マクロの再生中）なら、popup/show を送ってから応答する（#210）。
//
// req: FR-30, FR-19
func TestHover(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: -1})
	text := "a𝄞b\n日本"
	h.open(1, "a.go", text)
	h.diagnosticsAt(1, 1)
	for _, tc := range []struct {
		cursor ext.Position
		want   string
		sync   bool
	}{
		{pos(0, 1), "char=𝄞", false},
		{pos(0, 5), "char=b", false},
		{pos(1, 3), "char=本", false},
		{pos(0, 5), "char=b", true},
	} {
		if res := h.command(1, cmdHover, tc.cursor, tc.sync); !res.OK || res.Open != nil || res.Transaction != nil {
			t.Fatalf("hover (sync %v) = %+v", tc.sync, res)
		}
		if tc.sync && len(h.popups) == 0 {
			t.Error("sync hover replied before popup/show")
		}
		p := receive(t, h.popups, "popup/show")
		if p.Buffer != 1 || p.Version != 1 || p.Position != tc.cursor || p.Text != tc.want || p.Format != ext.PopupMarkdown {
			t.Errorf("cursor %+v: popup/show = %+v, want text %q", tc.cursor, p, tc.want)
		}
	}
}

// TestReplayReferences は、sync が true（マクロの再生中）の参照の一覧が、choice/ask で選ばせず、
// 候補が 1 つならその位置を応答の open で返し、複数なら知らせて ok を false にする（再生を止める）ことを確かめる。
//
// req: FR-94, FR-19
func TestReplayReferences(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: -1, choose: "1"})
	h.open(1, "a.go", "one TARGET")
	h.diagnosticsAt(1, 1)
	res := h.command(1, ext.OpReferences, pos(0, 0), true)
	if want := pos(0, 4); !res.OK || res.Open == nil || res.Open.Position == nil || *res.Open.Position != want || res.Open.Buffer == nil || *res.Open.Buffer != 1 {
		t.Errorf("sync references with one candidate = %+v, want open at %+v", res, want)
	}

	h.change(1, ext.Edit{Range: ext.Range{Start: pos(0, 0), End: pos(0, 0)}, Text: "TARGET "})
	if res := h.command(1, ext.OpReferences, pos(0, 0), true); res.OK {
		t.Errorf("sync references with two candidates = %+v, want ok = false", res)
	}
	if m := receive(t, h.msgs, "message/show"); !strings.Contains(m.Text, "再生中は候補を選べない") {
		t.Errorf("message/show = %q", m.Text)
	}
	if len(h.choices) != 0 || len(h.opens) != 0 {
		t.Errorf("sync references asked or moved: choices %d, opens %d", len(h.choices), len(h.opens))
	}

	// 再生中でなければ、選ばせて移る。
	if res := h.command(1, ext.OpReferences, pos(0, 0), false); !res.OK || res.Open != nil {
		t.Fatalf("references = %+v", res)
	}
	receive(t, h.choices, "choice/ask")
	if o := receive(t, h.opens, "editor/open"); o.Position == nil || *o.Position != pos(0, 11) {
		t.Errorf("editor/open = %+v, want the second candidate", o)
	}
}

// TestReplayRename は、sync が true（マクロの再生中）の名前の変更が、問い合わせたバッファの編集を応答の transaction で返し、
// 他のバッファの編集を応答の後に buffer/edit で送ることを確かめる。
//
// req: FR-94, FR-19
func TestReplayRename(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: -1})
	h.open(1, "a.go", "var TARGET = 1")
	h.diagnosticsAt(1, 1)
	h.open(2, "b.go", "x := TARGET")
	h.diagnosticsAt(2, 1)
	res := h.invoke(ext.ProviderInvokeParams{
		Operation: ext.OpRename, Buffer: 1, Version: h.vers[1], Cursor: pos(0, 4), Args: json.RawMessage(`{"text":"renamed"}`), Sync: true,
	})
	if !res.OK || res.Transaction == nil || res.Open != nil {
		t.Fatalf("sync rename = %+v, want ok with a transaction", res)
	}
	if got := applyEdits(h.texts[1], res.Transaction.Edits); got != "var renamed = 1" || res.Transaction.BaseVersion != h.vers[1] {
		t.Errorf("transaction = %+v (applied %q)", *res.Transaction, got)
	}
	e := receive(t, h.edits, "buffer/edit")
	if got := applyEdits(h.texts[2], e.Transaction.Edits); e.Buffer != 2 || got != "x := renamed" {
		t.Errorf("buffer/edit = %+v (applied %q)", e, got)
	}
	if m := receive(t, h.msgs, "message/show"); m.Text != "名前を renamed に変えた（2 ファイル）" {
		t.Errorf("message/show = %q", m.Text)
	}
}

// TestRenameUnopened は、名前の変更が本体で開いていないファイルに及ぶとき、そのファイルを editor/open（focus が false）で
// 本体の画面に出さないバッファとして開き、届いた複製の版で buffer/edit を送り、知らせの後に skipHooks の buffer/save で書き出させることを確かめる
// （FR-109。基本設計 4.4、6.3）。開いていたファイル（a.go）は書き出させない。sync が true（再生中）でも同じである。
//
// req: FR-109
func TestRenameUnopened(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprintf("sync=%v", sync), func(t *testing.T) {
			other := filepath.Join(t.TempDir(), "other.go")
			if err := os.WriteFile(other, []byte("package p\n\nvar _ = TARGET\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			h := startHost(t, hostOptions{args: []string{"-defpath", other}, maxRestarts: -1})
			h.open(1, "a.go", "var TARGET = 1")
			h.diagnosticsAt(1, 1)
			res := h.invoke(ext.ProviderInvokeParams{
				Operation: ext.OpRename, Buffer: 1, Version: h.vers[1], Cursor: pos(0, 4), Args: json.RawMessage(`{"text":"renamed"}`), Sync: sync,
			})
			if !res.OK {
				t.Fatalf("rename = %+v", res)
			}
			o := receive(t, h.opens, "editor/open")
			if o.Path != other || o.Focus {
				t.Errorf("editor/open = %+v, want path %s without focus", o, other)
			}
			var edits []ext.BufferEditParams
			if !sync {
				edits = append(edits, receive(t, h.edits, "buffer/edit"))
			} else if res.Transaction == nil {
				t.Fatal("sync rename: no transaction for the queried buffer")
			}
			edits = append(edits, receive(t, h.edits, "buffer/edit"))
			var off *ext.BufferEditParams
			for i := range edits {
				if edits[i].Buffer == offscreenBase {
					off = &edits[i]
				}
			}
			if off == nil {
				t.Fatalf("buffer/edit = %+v, want an edit of buffer %d", edits, offscreenBase)
			}
			if got := applyEdits("package p\n\nvar _ = TARGET", off.Transaction.Edits); got != "package p\n\nvar _ = renamed" || off.Transaction.BaseVersion != 1 {
				t.Errorf("buffer/edit of other.go = %+v (applied %q)", *off, got)
			}
			if m := receive(t, h.msgs, "message/show"); m.Text != "名前を renamed に変えた（2 ファイル）" {
				t.Errorf("message/show = %q", m.Text)
			}
			s := receive(t, h.saves, "buffer/save")
			if s.Buffer != offscreenBase || !s.SkipHooks || s.Version == nil || *s.Version != 2 {
				t.Errorf("buffer/save = %+v, want buffer %d at version 2 with skipHooks", s, offscreenBase)
			}
			select {
			case s := <-h.saves:
				t.Errorf("extra buffer/save = %+v", s)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

// TestRenameAllOrNothing は、名前の変更を、全部のファイルを変えるか何も変えないかにすることを確かめる（#237。PR #240 の再レビュー）。
// 編集を禁止したファイル（ro/q.go）に及ぶときは、編集を始める前に確かめて何も変えず、理由のファイルを知らせ、開いたファイルを閉じさせる。
// 確かめた後の適用の途中で断られたら、それまでに変えたバッファを元の全文に戻し、開いたファイルを閉じさせる。
//
// req: FR-109
func TestRenameAllOrNothing(t *testing.T) {
	newFiles := func(t *testing.T, files map[string]string) []string {
		t.Helper()
		dir := t.TempDir()
		var paths []string
		for _, name := range slices.Sorted(maps.Keys(files)) {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(files[name]), 0o600); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, p)
		}
		return paths
	}
	// noEdits は、届いた buffer/edit が無いことを確かめる。
	noEdits := func(t *testing.T, h *host) {
		t.Helper()
		select {
		case e := <-h.edits:
			t.Errorf("buffer/edit = %+v, want none", e)
		default:
		}
	}
	// closes は、開いたファイルのすべてに buffer/save が届くことを確かめる（本体は、ファイルと同じ中身のバッファを書かずに閉じる）。
	closes := func(t *testing.T, h *host, n int) {
		t.Helper()
		got := map[ext.BufferID]bool{}
		for range n {
			got[receive(t, h.saves, "buffer/save").Buffer] = true
		}
		for i := range n {
			if !got[offscreenBase+ext.BufferID(i)] {
				t.Errorf("buffer/save = %v, want buffers %d..%d", got, offscreenBase, offscreenBase+ext.BufferID(n-1))
			}
		}
	}

	t.Run("readonly", func(t *testing.T) {
		paths := newFiles(t, map[string]string{"other.go": "var _ = TARGET\n", "ro/q.go": "func TARGET() {}\n"})
		h := startHost(t, hostOptions{
			args: []string{"-renamepaths", strings.Join(paths, ",")}, maxRestarts: -1, sim: true, readOnly: []string{"q.go"},
		})
		h.open(1, "a.go", "var TARGET = 1")
		h.diagnosticsAt(1, 1)
		res := h.invoke(ext.ProviderInvokeParams{
			Operation: ext.OpRename, Buffer: 1, Version: h.vers[1], Cursor: pos(0, 4), Args: json.RawMessage(`{"text":"renamed"}`),
		})
		if !res.OK {
			t.Fatalf("rename = %+v", res)
		}
		m := receive(t, h.msgs, "message/show")
		if !strings.HasPrefix(m.Text, "名前の変更を適用しなかった（どのファイルも変えていない）: ") || !strings.Contains(m.Text, "q.go（編集の禁止）") || strings.Contains(m.Text, "other.go") {
			t.Errorf("message/show = %q", m.Text)
		}
		closes(t, h, 2)
		noEdits(t, h)
		if got := h.simText(1); got != "var TARGET = 1" {
			t.Errorf("a.go = %q, want unchanged", got)
		}
	})

	t.Run("rejected while applying", func(t *testing.T) {
		paths := newFiles(t, map[string]string{"other.go": "var _ = TARGET\n"})
		h := startHost(t, hostOptions{
			args: []string{"-renamepaths", strings.Join(paths, ",")}, maxRestarts: -1, sim: true, failEdit: []string{"b.go"},
		})
		h.open(1, "a.go", "var TARGET = 1")
		h.diagnosticsAt(1, 1)
		h.open(2, "b.go", "x := TARGET")
		h.diagnosticsAt(2, 1)
		res := h.invoke(ext.ProviderInvokeParams{
			Operation: ext.OpRename, Buffer: 1, Version: h.vers[1], Cursor: pos(0, 4), Args: json.RawMessage(`{"text":"renamed"}`),
		})
		if !res.OK {
			t.Fatalf("rename = %+v", res)
		}
		m := receive(t, h.msgs, "message/show")
		if !strings.Contains(m.Text, "b.go を変えられなかった") || !strings.Contains(m.Text, "変えた 1 個のファイルを元に戻した") {
			t.Errorf("message/show = %q", m.Text)
		}
		closes(t, h, 1)
		if got := h.simText(1); got != "var TARGET = 1" {
			t.Errorf("a.go = %q, want restored", got)
		}
		if got := h.simText(offscreenBase); got != "var _ = TARGET" {
			t.Errorf("other.go = %q, want unchanged", got)
		}
	})
}

// TestFormat は、textDocument/formatting の TextEdit の列を、同じ開始の位置の挿入を含めて逐次の数え方のトランザクションにし、
// コマンドでは buffer/edit で、sync が true なら応答で、保存の前は buffer/willSave の応答で返すことを確かめる。
// 文書の全体を置き換える整形（末尾の改行の後ろの空の行まで）でも、本体の全文に空の行を足さないことを確かめる。
//
// req: FR-30
func TestFormat(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(fmt.Sprintf("whole=%v", whole), func(t *testing.T) {
			var args []string
			if whole {
				args = []string{"-fmt-whole"}
			}
			h := startHost(t, hostOptions{args: args, maxRestarts: -1})
			text := "𝄞fmtme 日\nfmtme\n\nx fmtme"
			want := strings.ReplaceAll(text, "fmtme", "<FMTME")
			h.open(1, "a.go", text)
			h.diagnosticsAt(1, 1)

			if res := h.command(1, cmdFormat, pos(0, 0), false); !res.OK {
				t.Fatal("format: ok = false")
			}
			e := receive(t, h.edits, "buffer/edit")
			if e.Buffer != 1 || e.Transaction.BaseVersion != 1 {
				t.Errorf("buffer/edit = %+v", e)
			}
			if got := applyEdits(text, e.Transaction.Edits); got != want {
				t.Errorf("formatted = %q, want %q", got, want)
			}

			res := h.command(1, cmdFormat, pos(0, 0), true)
			if !res.OK || res.Transaction == nil || applyEdits(text, res.Transaction.Edits) != want {
				t.Errorf("sync format = %+v", res)
			}

			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			var ws ext.BufferWillSaveResult
			if err := h.conn.Call(ctx, ext.MethodBufferWillSave, ext.BufferWillSaveParams{Buffer: 1, Version: 1}, &ws); err != nil {
				t.Fatal(err)
			}
			if ws.Transaction == nil || ws.Transaction.BaseVersion != 1 || applyEdits(text, ws.Transaction.Edits) != want {
				t.Errorf("willSave = %+v", ws)
			}
		})
	}
}

// TestNoFinalNewline は、本体が書き出したときに末尾に改行が付かないバッファ（finalNewline が false）では、
// 言語サーバーへ送る全文に改行を足さず、整形の結果をそのまま本体の編集にすることを確かめる。
//
// req: FR-30
func TestNoFinalNewline(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(fmt.Sprintf("whole=%v", whole), func(t *testing.T) {
			var args []string
			if whole {
				args = []string{"-fmt-whole"}
			}
			h := startHost(t, hostOptions{args: args, maxRestarts: -1})
			text := "fmtme\nx fmtme"
			path := filepath.Join(h.dir, "a.go")
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			h.texts[1], h.vers[1] = text, 1
			no := false
			if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{
				Buffer: 1, Path: path, FileType: "go", Version: 1, Text: text, FinalNewline: &no,
			}); err != nil {
				t.Fatal(err)
			}
			if got := h.waitText(1); got != text {
				t.Errorf("didOpen text = %q, want %q", got, text)
			}

			want := strings.ReplaceAll(text, "fmtme", "<FMTME")
			res := h.command(1, cmdFormat, pos(0, 0), true)
			if !res.OK || res.Transaction == nil {
				t.Fatalf("sync format = %+v", res)
			}
			if got := applyEdits(text, res.Transaction.Edits); got != want {
				t.Errorf("formatted = %q, want %q", got, want)
			}

			h.change(1, edit(1, 0, 1, 0, "y"))
			if got := h.waitText(2); got != h.texts[1] {
				t.Errorf("text after didChange = %q, want %q", got, h.texts[1])
			}

			// 送り直しで finalNewline が true になれば、改行を足して送る。
			if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Path: path, FileType: "go", Version: 3, Text: "b"}); err != nil {
				t.Fatal(err)
			}
			if got := h.waitText(3); got != "b\n" {
				t.Errorf("text after reopen = %q, want %q", got, "b\n")
			}
		})
	}
}

// TestWillSaveDisabledAndCancel は、formatOnSave が false なら編集無しを答えることと、
// 本体が buffer/willSave を取り消したら、言語サーバーへ $/cancelRequest を送ることを確かめる。
//
// req: FR-30
func TestWillSaveDisabledAndCancel(t *testing.T) {
	h := startHost(t, hostOptions{extra: map[string]any{"formatOnSave": false}, maxRestarts: -1})
	h.open(1, "a.go", "fmtme")
	h.diagnosticsAt(1, 1)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var ws ext.BufferWillSaveResult
	if err := h.conn.Call(ctx, ext.MethodBufferWillSave, ext.BufferWillSaveParams{Buffer: 1, Version: 1}, &ws); err != nil {
		t.Fatal(err)
	}
	if ws.Transaction != nil {
		t.Errorf("willSave with formatOnSave false = %+v", ws)
	}

	h2 := startHost(t, hostOptions{args: []string{"-fmt-delay", "1m"}, maxRestarts: -1})
	h2.open(1, "a.go", "fmtme")
	h2.diagnosticsAt(1, 1)
	cctx, ccancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer ccancel()
	if err := h2.conn.Call(cctx, ext.MethodBufferWillSave, ext.BufferWillSaveParams{Buffer: 1, Version: 1}, &ws); err == nil {
		t.Fatal("willSave returned before the formatting finished")
	}
	h2.waitLog("formatting cancelled", func(e logEntry) bool { return e.Kind == "formatting cancelled" })
}

// TestServerCrash は、言語サーバーが異常終了しても拡張機能は動き続け、診断を消し、
// 再起動して現在の全文を didOpen で送り直すことと、回数の上限に達したらもう起動しないことを確かめる。
//
// req: FR-30
func TestServerCrash(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: 2})
	h.open(1, "a.go", "a ERR")
	h.diagnosticsAt(1, 1)

	for n := 1; n <= 3; n++ {
		h.change(1, edit(0, 0, 0, 0, "CRASH "))
		v := h.vers[1]
		cleared := h.diagnosticsAt(1, v)
		if len(cleared.Diagnostics) != 0 {
			t.Fatalf("crash %d: diagnostics = %+v, want cleared", n, cleared.Diagnostics)
		}
		m := receive(t, h.msgs, "message/show")
		if n <= 2 {
			if m.Level != ext.MessageWarning || !strings.Contains(m.Text, fmt.Sprintf("%d/2", n)) {
				t.Errorf("crash %d: message = %+v", n, m)
			}
			// 再起動した言語サーバーは、現在の全文を didOpen で受け取る（その版の診断が届く）。
			checkDiagnostics(t, h.diagnosticsAt(1, v), h.texts[1])
			continue
		}
		if m.Level != ext.MessageError || !strings.Contains(m.Text, "上限") {
			t.Errorf("crash %d: message = %+v", n, m)
		}
	}
	inits := 0
	for _, e := range h.readLog() {
		if e.Kind == lspInitialize {
			inits++
		}
	}
	if inits != 3 {
		t.Errorf("initialize sent %d times, want 3", inits)
	}
	// 拡張機能は動き続け、言語サーバーの要るコマンドは ok が false になる。
	if res := h.command(1, cmdHover, pos(0, 0), false); res.OK {
		t.Error("hover after giving up: ok = true, want false")
	}
	if m := receive(t, h.msgs, "message/show"); !strings.Contains(m.Text, "稼働していない") {
		t.Errorf("message = %+v", m)
	}
}

// TestCommandBeforeReady は、言語サーバーが起動できないとき、コマンドが ok を false にし、拡張機能が動き続けることを確かめる。
func TestCommandBeforeReady(t *testing.T) {
	h := startHost(t, hostOptions{extra: map[string]any{"command": filepath.Join(t.TempDir(), "no-such-server")}, maxRestarts: -1})
	h.open(1, "a.go", "a")
	if m := receive(t, h.msgs, "message/show"); m.Level != ext.MessageError || !strings.Contains(m.Text, "起動できない") {
		t.Errorf("message = %+v", m)
	}
	if res := h.command(1, cmdFormat, pos(0, 0), false); res.OK {
		t.Error("format without a server: ok = true, want false")
	}
}

// TestStopShutsDown は、拡張機能が終わるときに、言語サーバーへ shutdown と exit を送ることを確かめる。
func TestStopShutsDown(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: -1})
	h.open(1, "a.go", "a")
	h.diagnosticsAt(1, 1)
	_ = h.conn.Close()
	h.x.stop()
	h.waitLog("exit", func(e logEntry) bool { return e.Kind == lspExit })
	h.waitLog("shutdown", func(e logEntry) bool { return e.Kind == lspShutdown })
}

// hostShutdownTimeout は、本体が shutdown を送ってから、拡張機能のプロセスグループへ SIGTERM を送るまでの期限である
// （internal/exthost の DefaultShutdownTimeout。基本設計 5.2。値は仮）。
const hostShutdownTimeout = 3 * time.Second

// shutdown は、本体の役から shutdown を送り、拡張機能が接続を閉じる（Serve が戻る）までの時間を返す。
func (h *host) shutdown() time.Duration {
	h.t.Helper()
	start := time.Now()
	if err := h.conn.Notify(ext.MethodShutdown, ext.ShutdownParams{}); err != nil {
		h.t.Fatalf("shutdown: %v", err)
	}
	select {
	case <-h.conn.Done():
	case <-time.After(testTimeout):
		h.t.Fatal("the extension did not close the connection after shutdown")
	}
	return time.Since(start)
}

// serverProc は、言語サーバー go の、作業のディレクトリを根にした client の言語サーバーのプロセスを返す。
func (h *host) serverProc() *proc {
	h.t.Helper()
	h.x.mu.Lock()
	c := h.x.clients[clientKey{server: "go", root: h.dir}]
	h.x.mu.Unlock()
	if c == nil {
		h.t.Fatal("no client for the language server go")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc == nil {
		h.t.Fatal("the language server is not running")
	}
	return c.proc
}

// TestShutdownStopsServers は、本体から shutdown を受け取ると、言語サーバーへ shutdown と exit を送り、
// 言語サーバーが終わってから接続を閉じること、それが本体の期限の中で終わることを確かめる（基本設計 5.2 の正常な終了）。
//
// req: FR-30
func TestShutdownStopsServers(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: -1})
	h.open(1, "a.go", "a")
	h.diagnosticsAt(1, 1)
	p := h.serverProc()
	if d := h.shutdown(); d >= hostShutdownTimeout {
		t.Errorf("shutdown took %s, want < %s", d, hostShutdownTimeout)
	}
	// 接続を閉じた時点で、言語サーバーは終わっている。待たずに確かめる。
	var kinds []string
	for _, e := range h.readLog() {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Contains(kinds, lspShutdown) || !slices.Contains(kinds, lspExit) {
		t.Errorf("fakels log = %v, want shutdown and exit", kinds)
	}
	select {
	case <-p.exited:
	default:
		t.Fatal("the language server remains after the connection is closed")
	}
	if !p.cmd.ProcessState.Success() {
		t.Errorf("language server exit = %v, want success (exited by exit, not killed)", p.cmd.ProcessState)
	}
}

// TestShutdownHungServer は、言語サーバーが shutdown に応答しないとき、stopTimeout で打ち切って強制終了し、
// 本体の期限（SIGTERM を送るまで）の中で接続を閉じることを確かめる。
//
// req: FR-30
func TestShutdownHungServer(t *testing.T) {
	h := startHost(t, hostOptions{args: []string{"-hang-on-shutdown"}, maxRestarts: -1})
	h.open(1, "a.go", "a")
	h.diagnosticsAt(1, 1)
	p := h.serverProc()
	d := h.shutdown()
	if d < stopTimeout || d >= hostShutdownTimeout {
		t.Errorf("shutdown took %s, want in [%s, %s)", d, stopTimeout, hostShutdownTimeout)
	}
	select {
	case <-p.exited:
	default:
		t.Fatal("the language server remains after the connection is closed")
	}
	if p.cmd.ProcessState.Success() {
		t.Errorf("language server exit = %v, want killed", p.cmd.ProcessState)
	}
}

// ping は、本体の役から ping を送り、期限までに応答が届くことを確かめる。
func (h *host) ping(within time.Duration) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	start := time.Now()
	if err := h.conn.Call(ctx, ext.MethodPing, nil, nil); err != nil {
		h.t.Fatalf("ping: %v (after %s)", err, time.Since(start))
	}
}

// TestPingWhileWaiting は、言語サーバーの応答を待つ間（sync が false と true の lsp.format、buffer/willSave）も、
// 拡張機能が ping に答え、buffer/change を言語サーバーへ送り続けることを確かめる（issue の契約）。
//
// req: FR-30
func TestPingWhileWaiting(t *testing.T) {
	h := startHost(t, hostOptions{args: []string{"-fmt-delay", "1m"}, maxRestarts: -1})
	h.open(2, "a.go", "fmtme")
	h.diagnosticsAt(2, 1)
	if res := h.command(2, cmdFormat, pos(0, 0), false); !res.OK {
		t.Fatal("format: ok = false")
	}
	// 応答を待ったままにする問い合わせ。取り消しは Cleanup で接続を閉じるときに起きる。
	for _, req := range []struct {
		method string
		params any
	}{
		{ext.MethodProviderInvoke, ext.ProviderInvokeParams{Operation: cmdFormat, Buffer: 2, Version: 1, Sync: true}},
		{ext.MethodBufferWillSave, ext.BufferWillSaveParams{Buffer: 2, Version: 1}},
	} {
		f, err := h.conn.Go(req.method, req.params)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Cancel() })
	}
	for range 5 {
		h.ping(time.Second)
	}
	h.change(2, edit(0, 0, 0, 0, "x "))
	if got := h.waitText(2); got != "x fmtme\n" {
		t.Errorf("text after change = %q", got)
	}
	h.ping(time.Second)
}

// TestRestartCountResets は、resetAfter より長く稼働した後の異常終了では再起動の回数を数え直し、上限で止めないことを確かめる。
//
// req: FR-30
func TestRestartCountResets(t *testing.T) {
	h := startHost(t, hostOptions{maxRestarts: 1, resetAfter: 100 * time.Millisecond})
	h.open(1, "a.go", "a")
	h.diagnosticsAt(1, 1)
	for n := 1; n <= 3; n++ {
		time.Sleep(200 * time.Millisecond)
		h.change(1, edit(0, 0, 0, 0, "CRASH "))
		v := h.vers[1]
		h.diagnosticsAt(1, v) // 消した診断
		if m := receive(t, h.msgs, "message/show"); m.Level != ext.MessageWarning || !strings.Contains(m.Text, "1/1") {
			t.Fatalf("crash %d: message = %+v, want a restart", n, m)
		}
		h.diagnosticsAt(1, v) // 再起動した言語サーバーの診断
	}
}

// TestWillSaveBeforeReady は、言語サーバーの起動中の保存では、整形せずに編集無しを答え、そのことを message/show で知らせることを確かめる。
//
// req: FR-30
func TestWillSaveBeforeReady(t *testing.T) {
	h := startHost(t, hostOptions{args: []string{"-init-delay", "1m"}, maxRestarts: -1})
	h.open(1, "a.go", "fmtme")
	h.waitLog("initialize", func(e logEntry) bool { return e.Kind == lspInitialize })
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var ws ext.BufferWillSaveResult
	if err := h.conn.Call(ctx, ext.MethodBufferWillSave, ext.BufferWillSaveParams{Buffer: 1, Version: 1}, &ws); err != nil {
		t.Fatal(err)
	}
	if ws.Transaction != nil {
		t.Errorf("willSave before ready = %+v, want no edits", ws)
	}
	if m := receive(t, h.msgs, "message/show"); m.Level != ext.MessageWarning || !strings.Contains(m.Text, "保存の前の整形") {
		t.Errorf("message = %+v", m)
	}
}
