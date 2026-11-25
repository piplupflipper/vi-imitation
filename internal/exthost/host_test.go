package exthost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// waitTimeout は、テストが起こるはずのことを待つ上限である。これを超えたらテストを失敗させる。
const waitTimeout = 10 * time.Second

// ctx は、中断しない問い合わせに渡す ctx である。
var ctx = context.Background()

// callTimeout は、応答することが分かっている問い合わせの期限である。
const callTimeout = 5 * time.Second

// testConfig は、テスト用の短い間隔の設定である。
func testConfig(logw *syncBuffer) Config {
	return Config{
		InitTimeout:     3 * time.Second,
		PingInterval:    100 * time.Millisecond,
		PingTimeout:     time.Second,
		RestartDelay:    20 * time.Millisecond,
		MaxRestartDelay: 100 * time.Millisecond,
		MaxFailures:     3,
		Logger:          slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// syncBuffer は、複数の goroutine から書いてよい bytes.Buffer である。記録の出力先に使う。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeCommand は、テスト用の拡張機能を mode で起動するスクリプトを作り、そのパスを返す。
func fakeCommand(t *testing.T, mode string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fake-ext-"+mode)
	script := fmt.Sprintf("#!/bin/sh\nexec env %s=%s '%s' \"$@\"\n", fakeModeEnv, mode, exe)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // テストの一時ディレクトリに置く実行用のスクリプトである。
		t.Fatal(err)
	}
	return path
}

// fakeSpec は、テスト用の拡張機能の定義を作る。
// 操作 id+".obj" のテキストオブジェクトを追加し、その操作とファイルの種類 fileType を起動の契機にする。
func fakeSpec(t *testing.T, id, mode, fileType string) Spec {
	t.Helper()
	m := ext.Manifest{
		ID:          id,
		Command:     fakeCommand(t, mode),
		Activation:  []ext.Activation{{On: ext.ActivationTextObject, ID: id + ".obj"}},
		Contributes: ext.Contributes{TextObjects: []ext.Contribution{{ID: id + ".obj"}}},
	}
	if fileType != "" {
		m.Activation = append(m.Activation, ext.Activation{On: ext.ActivationFileType, Pattern: fileType})
	}
	if _, err := ext.ValidateManifests([]ext.Manifest{m}); err != nil {
		t.Fatal(err)
	}
	return Spec{Manifest: m}
}

// harness は、テスト対象の Host と、そのイベントを集めるイベントループの代役である。
type harness struct {
	t   *testing.T
	h   *Host
	log *syncBuffer

	mu       sync.Mutex
	events   []Event
	consumed []bool
}

// newHarness は、Host を作り、イベントを集める goroutine を起動する。collect が false なら集めない。
func newHarness(t *testing.T, specs []Spec, cfg func(*Config), collect bool) *harness {
	t.Helper()
	logw := &syncBuffer{}
	c := testConfig(logw)
	if cfg != nil {
		cfg(&c)
	}
	h, err := New(specs, c)
	if err != nil {
		t.Fatal(err)
	}
	hs := &harness{t: t, h: h, log: logw}
	t.Cleanup(func() {
		_ = h.Close()
		if t.Failed() {
			t.Logf("host log:\n%s", logw.String())
		}
	})
	if collect {
		go func() {
			for ev := range h.Events() {
				hs.mu.Lock()
				hs.events = append(hs.events, ev)
				hs.consumed = append(hs.consumed, false)
				hs.mu.Unlock()
			}
		}()
	}
	return hs
}

// waitEvent は、まだ返していないイベントのうち、pred を満たす最初のものを待って返す。
func (hs *harness) waitEvent(what string, pred func(Event) bool) Event {
	hs.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		hs.mu.Lock()
		for i, ev := range hs.events {
			if !hs.consumed[i] && pred(ev) {
				hs.consumed[i] = true
				hs.mu.Unlock()
				return ev
			}
		}
		hs.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	hs.t.Fatalf("timed out waiting for %s", what)
	return nil
}

// mark は、これまでに集めたイベントの数を返す。statesSince の起点に使う。
func (hs *harness) mark() int {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return len(hs.events)
}

// statesSince は、from 番目以降のイベントのうち、拡張機能 id の状態の変化を順に返す。
func (hs *harness) statesSince(from int, id string) []State {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	var states []State
	for _, ev := range hs.events[from:] {
		if sc, ok := ev.(*StateChange); ok && sc.Extension == id {
			states = append(states, sc.State)
		}
	}
	return states
}

// waitState は、拡張機能 id が st になったことを知らせる StateChange を待って返す。
func (hs *harness) waitState(id string, st State) *StateChange {
	hs.t.Helper()
	ev := hs.waitEvent(fmt.Sprintf("%s to become %s", id, st), func(ev Event) bool {
		sc, ok := ev.(*StateChange)
		return ok && sc.Extension == id && sc.State == st
	})
	return ev.(*StateChange)
}

// fakeState は、拡張機能 x の test/state を呼ぶ。
func (hs *harness) fakeState() fakeState {
	hs.t.Helper()
	var s fakeState
	if err := hs.h.Call(ctx, "x", methodState, nil, callTimeout, &s); err != nil {
		hs.t.Fatalf("test/state: %v", err)
	}
	return s
}

// waitLog は、Host の記録が substr を含むまで待つ。
func (hs *harness) waitLog(substr string) {
	hs.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if strings.Contains(hs.log.String(), substr) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	hs.t.Fatalf("log does not contain %q", substr)
}

func (hs *harness) state(id string) State {
	hs.t.Helper()
	st, ok := hs.h.State(id)
	if !ok {
		hs.t.Fatalf("unknown extension %q", id)
	}
	return st
}

// snapshot は、バッファ b を id の BufferSnapshot にする。
func snapshot(id ext.BufferID, fileType string, b *buffer.Buffer) BufferSnapshot {
	return BufferSnapshot{ID: id, FileType: fileType, Version: b.Version(), Text: b.Text()}
}

// processGone は、pid のプロセスが無くなる（ゾンビとして回収されるまで含む）まで待ち、無くなったかどうかを返す。
func processGone(pid int) bool {
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		// ゾンビは kill(pid, 0) に成功するので、/proc の状態も見る。
		if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			if f := strings.Fields(string(data)); len(f) > 2 && f[2] == "Z" {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// 宣言した契機が成り立つまで子プロセスを起動せず、契機が成り立ったら起動する。
//
// req: FR-14
func TestStartOnActivation(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "md", modeNormal, "markdown"), fakeSpec(t, "op", modeNormal, "")}, nil, true)

	b := buffer.New("# title\nbody\n")
	if err := hs.h.OpenBuffer(snapshot(1, "go", b)); err != nil {
		t.Fatal(err)
	}
	if got := hs.h.Activate(ext.ActivationTextObject, "unknown.obj"); len(got) != 0 {
		t.Fatalf("Activate(unknown) started %v", got)
	}
	time.Sleep(200 * time.Millisecond)
	for _, id := range []string{"md", "op"} {
		if st := hs.state(id); st != StateNotStarted {
			t.Fatalf("%s: state %s before activation, want not-started", id, st)
		}
	}

	if err := hs.h.OpenBuffer(snapshot(2, "markdown", b)); err != nil {
		t.Fatal(err)
	}
	hs.waitState("md", StateRunning)
	if st := hs.state("op"); st != StateNotStarted {
		t.Fatalf("op: state %s after markdown opened, want not-started", st)
	}

	if got := hs.h.Activate(ext.ActivationTextObject, "op.obj"); !slices.Equal(got, []string{"op"}) {
		t.Fatalf("Activate(op.obj) = %v, want [op]", got)
	}
	hs.waitState("op", StateRunning)
	// 2 回目の契機では起動し直さない。
	if got := hs.h.Activate(ext.ActivationTextObject, "op.obj"); len(got) != 0 {
		t.Fatalf("second Activate started %v", got)
	}
}

// 起動の契機 startup の拡張機能は、New でもバッファを開いても起動せず、ActivateStartup で起動する。
// 他の契機の拡張機能は ActivateStartup で起動しない。:ext sync の入れ替え（Reload）で未起動へ戻ると、すぐに起動し直す。
//
// req: FR-225
func TestStartupActivation(t *testing.T) {
	res := fakeSpec(t, "res", modeNormal, "")
	res.Manifest.Activation = append(res.Manifest.Activation, ext.Activation{On: ext.ActivationStartup})
	hs := newHarness(t, []Spec{fakeSpec(t, "op", modeNormal, "go"), res}, nil, true)

	if err := hs.h.OpenBuffer(snapshot(1, "markdown", buffer.New("x"))); err != nil {
		t.Fatal(err)
	}
	if got := hs.h.Activate(ext.ActivationStartup, "x"); len(got) != 0 {
		t.Fatalf("Activate(startup) started %v", got)
	}
	time.Sleep(200 * time.Millisecond)
	if st := hs.state("res"); st != StateNotStarted {
		t.Fatalf("res: state %s before ActivateStartup, want not-started", st)
	}

	if got := hs.h.ActivateStartup(); !slices.Equal(got, []string{"res"}) {
		t.Fatalf("ActivateStartup = %v, want [res]", got)
	}
	hs.waitState("res", StateRunning)
	if st := hs.state("op"); st != StateNotStarted {
		t.Errorf("op: state %s after ActivateStartup, want not-started", st)
	}
	if got := hs.h.ActivateStartup(); len(got) != 0 {
		t.Errorf("second ActivateStartup started %v", got)
	}

	if err := hs.h.Reload("res"); err != nil {
		t.Fatal(err)
	}
	hs.waitState("res", StateNotStarted)
	hs.waitState("res", StateRunning)
}

// 宣言ファイルの noBufferSync の拡張機能へは、稼働中になったときの全文も、その後のバッファの開閉と差分と保存も送らない。
// command/execute は Withheld を立てて送る。同じ Host の他の拡張機能へは、今までどおり送る。
//
// req: FR-229
func TestNoBufferSync(t *testing.T) {
	m := ext.Manifest{
		ID: "nb", Command: fakeCommand(t, modeNormal), NoBufferSync: true,
		Activation:  []ext.Activation{{On: ext.ActivationCommand, ID: "nb.cmd"}},
		Contributes: ext.Contributes{Commands: []ext.Contribution{{ID: "nb.cmd"}}},
	}
	if _, err := ext.ValidateManifests([]ext.Manifest{m}); err != nil {
		t.Fatal(err)
	}
	hs := newHarness(t, []Spec{{Manifest: m}, fakeSpec(t, "x", modeNormal, "")}, nil, true)
	b := buffer.New("abc\n")
	if err := hs.h.OpenBuffer(snapshot(1, "", b)); err != nil {
		t.Fatal(err)
	}
	hs.h.Activate(ext.ActivationCommand, "nb.cmd")
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("nb", StateRunning)
	hs.waitState("x", StateRunning)

	c, err := b.Apply(buffer.Transaction{BaseVersion: b.Version(), Edits: []buffer.Edit{{Text: "z"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := hs.h.ChangeBuffer(1, c); err != nil {
		t.Fatal(err)
	}
	if err := hs.h.DidSave(ext.BufferDidSaveParams{Buffer: 1, Version: ext.Version(b.Version())}); err != nil {
		t.Fatal(err)
	}
	if err := hs.h.OpenBuffer(snapshot(2, "", buffer.New("second"))); err != nil {
		t.Fatal(err)
	}
	if err := hs.h.CloseBuffer(2); err != nil {
		t.Fatal(err)
	}
	var res ext.CommandExecuteResult
	if err := hs.h.Call(ctx, "nb", ext.MethodCommandExecute, ext.CommandExecuteParams{Buffer: 1, Version: ext.Version(b.Version()), ID: "nb.cmd"}, callTimeout, &res); err != nil {
		t.Fatal(err)
	}

	var nb, x fakeState
	if err := hs.h.Call(ctx, "nb", methodState, nil, callTimeout, &nb); err != nil {
		t.Fatal(err)
	}
	if err := hs.h.Call(ctx, "x", methodState, nil, callTimeout, &x); err != nil {
		t.Fatal(err)
	}
	if msgs := bufferMessages(nb.Log); len(nb.Buffers) != 0 || len(msgs) != 0 {
		t.Errorf("noBufferSync の拡張機能がバッファを受け取った: %v %v", nb.Buffers, msgs)
	}
	if !slices.Contains(nb.Log, "command:nb.cmd:withheld=true") {
		t.Errorf("command/execute の Withheld が立っていない: %v", nb.Log)
	}
	if !slices.Contains(nb.Log, "initialize:noBufferSync") || slices.Contains(x.Log, "initialize:noBufferSync") {
		t.Errorf("initialize の NoBufferSync: nb %v, x %v", nb.Log, x.Log)
	}
	if got := bufferMessages(x.Log); len(got) != 5 {
		t.Errorf("他の拡張機能が受け取ったバッファの知らせ = %v, want 5 件", got)
	}
}

// 起動は非同期で、初期化が終わらなくても呼び出し側は待たされない。
//
// req: NFR-04
func TestStartIsAsync(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "slow", modeNoInit, "")}, func(c *Config) { c.InitTimeout = time.Minute }, true)

	start := time.Now()
	if got := hs.h.Activate(ext.ActivationTextObject, "slow.obj"); len(got) != 1 {
		t.Fatalf("Activate started %v", got)
	}
	if err := hs.h.OpenBuffer(snapshot(1, "", buffer.New("x"))); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Activate and OpenBuffer took %s", d)
	}
	time.Sleep(200 * time.Millisecond)
	if st := hs.state("slow"); st != StateStarting {
		t.Fatalf("state %s, want starting", st)
	}
}

// 開いているバッファの全文を稼働中になった時点で送り、以後は差分を送る。
// 稼働中になった後に開いたバッファは、開いた時点で全文を送る。
//
// req: FR-16
func TestBufferSync(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)

	b1 := buffer.New("héllo\nworld\n")
	if err := hs.h.OpenBuffer(snapshot(1, "", b1)); err != nil {
		t.Fatal(err)
	}
	apply := func(id ext.BufferID, b *buffer.Buffer, edits ...buffer.Edit) {
		t.Helper()
		c, err := b.Apply(buffer.Transaction{BaseVersion: b.Version(), Edits: edits})
		if err != nil {
			t.Fatal(err)
		}
		if err := hs.h.ChangeBuffer(id, c); err != nil {
			t.Fatal(err)
		}
	}
	pos := func(l, by int) buffer.Position { return buffer.Position{Line: l, Byte: by} }
	// 起動前の変更は写しにだけ適用する。
	apply(1, b1, buffer.Edit{Range: buffer.Range{Start: pos(0, 0), End: pos(0, 0)}, Text: "> "})

	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("x", StateRunning)

	apply(1, b1,
		buffer.Edit{Range: buffer.Range{Start: pos(0, 2), End: pos(1, 2)}, Text: "a\nb\nc"},
		buffer.Edit{Range: buffer.Range{Start: pos(2, 0), End: pos(2, 0)}, Text: "日本\n"},
	)
	b1.BeginUndoGroup()
	apply(1, b1, buffer.Edit{Range: buffer.Range{Start: pos(0, 0), End: pos(0, 0)}, Text: "1"})
	apply(1, b1, buffer.Edit{Range: buffer.Range{Start: pos(0, 1), End: pos(0, 1)}, Text: "2"})
	b1.EndUndoGroup()
	c, ok := b1.Undo()
	if !ok {
		t.Fatal("Undo failed")
	}
	if err := hs.h.ChangeBuffer(1, c); err != nil {
		t.Fatal(err)
	}

	b2 := buffer.New("")
	if err := hs.h.OpenBuffer(snapshot(2, "", b2)); err != nil {
		t.Fatal(err)
	}
	apply(2, b2, buffer.Edit{Range: buffer.Range{Start: pos(0, 0), End: pos(0, 0)}, Text: "new\n"})

	b3 := buffer.New("closed")
	if err := hs.h.OpenBuffer(snapshot(3, "", b3)); err != nil {
		t.Fatal(err)
	}
	if err := hs.h.CloseBuffer(3); err != nil {
		t.Fatal(err)
	}

	s := hs.fakeState()
	want := map[ext.BufferID]fakeBuffer{
		1: {Version: ext.Version(b1.Version()), Text: b1.Text()},
		2: {Version: ext.Version(b2.Version()), Text: b2.Text()},
	}
	if fmt.Sprint(s.Buffers) != fmt.Sprint(want) {
		t.Fatalf("extension buffers = %v, want %v", s.Buffers, want)
	}
	// 全文はバッファごとに 1 回だけ送り、以後は差分である。
	var msgs []string
	for _, l := range s.Log {
		if strings.HasPrefix(l, "open:") || strings.HasPrefix(l, "change:") || strings.HasPrefix(l, "close:") || strings.HasPrefix(l, "resync:") {
			msgs = append(msgs, l)
		}
	}
	wantMsgs := []string{"open:1:1", "change:1:1->2", "change:1:2->3", "change:1:3->4", "change:1:4->5", "open:2:0", "change:2:0->1", "open:3:0", "close:3"}
	if !slices.Equal(msgs, wantMsgs) {
		t.Fatalf("buffer messages = %v, want %v", msgs, wantMsgs)
	}
}

// 呼び出し側の誤り（開いていないバッファ、版の飛び）は、拡張機能へ何も送らずに誤りにする。
func TestBufferStateErrors(t *testing.T) {
	hs := newHarness(t, nil, nil, false)
	b := buffer.New("a")
	if err := hs.h.OpenBuffer(snapshot(1, "", b)); err != nil {
		t.Fatal(err)
	}
	if err := hs.h.OpenBuffer(snapshot(1, "", b)); !errors.Is(err, ErrBufferState) {
		t.Fatalf("second open: %v", err)
	}
	if err := hs.h.ChangeBuffer(2, buffer.Change{Before: 0, After: 1}); !errors.Is(err, ErrBufferState) {
		t.Fatalf("change of unopened buffer: %v", err)
	}
	if err := hs.h.ChangeBuffer(1, buffer.Change{Before: 5, After: 6}); !errors.Is(err, ErrBufferState) {
		t.Fatalf("change from wrong version: %v", err)
	}
	bad := buffer.Change{Before: 0, After: 1, Edits: []buffer.Edit{{Range: buffer.Range{End: buffer.Position{Line: 3}}}}}
	if err := hs.h.ChangeBuffer(1, bad); !errors.Is(err, ErrBufferState) {
		t.Fatalf("change out of range: %v", err)
	}
	if err := hs.h.CloseBuffer(2); !errors.Is(err, ErrBufferState) {
		t.Fatalf("close of unopened buffer: %v", err)
	}
}

// 差分が欠けた拡張機能からの buffer/resync に、全文で応じる。
//
// req: FR-16
func TestResync(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)
	b := buffer.New("one\ntwo")
	if err := hs.h.OpenBuffer(snapshot(7, "", b)); err != nil {
		t.Fatal(err)
	}
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("x", StateRunning)
	if err := hs.h.Call(ctx, "x", methodForget, ext.BufferCloseParams{Buffer: 7}, callTimeout, nil); err != nil {
		t.Fatal(err)
	}
	c, err := b.Apply(buffer.Transaction{BaseVersion: 0, Edits: []buffer.Edit{{Range: buffer.Range{Start: buffer.Position{Line: 1, Byte: 3}, End: buffer.Position{Line: 1, Byte: 3}}, Text: "!"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := hs.h.ChangeBuffer(7, c); err != nil {
		t.Fatal(err)
	}
	hs.waitLog("extension requested resync")
	s := hs.fakeState()
	if got := s.Buffers[7]; got.Text != "one\ntwo!" || got.Version != 1 {
		t.Fatalf("after resync: %+v", got)
	}
	if !slices.Contains(s.Log, "resync:7") || !slices.Contains(s.Log, "open:7:1") {
		t.Fatalf("log = %v", s.Log)
	}
}

// 期限付きの問い合わせは、未起動の拡張機能を起動し、同じ期限の中で起動と応答を待つ。
//
// req: FR-17
func TestCallStartsExtension(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)
	p := ext.TextObjectResolveParams{Buffer: 1, Version: 4, ID: "x.obj", Cursor: ext.Position{Line: 2, Byte: 1}, Count: 3}
	res, err := hs.h.ResolveTextObject(ctx, "x", p, callTimeout)
	if err != nil {
		t.Fatal(err)
	}
	want := ext.TextObjectResolveResult{OK: true, Version: 4, Range: ext.Range{Start: p.Cursor, End: ext.Position{Line: 2, Byte: 4}}}
	if res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	mres, err := hs.h.ResolveMotion(ctx, "x", ext.MotionResolveParams{Version: 4, Count: 2}, callTimeout)
	if err != nil || !mres.OK || mres.Position.Byte != 2 {
		t.Fatalf("motion = %+v, %v", mres, err)
	}
	ores, err := hs.h.ApplyOperator(ctx, "x", ext.OperatorApplyParams{Version: 4}, callTimeout)
	if err != nil || !ores.OK || ores.Transaction.BaseVersion != 4 || len(ores.Transaction.Edits) != 1 {
		t.Fatalf("operator = %+v, %v", ores, err)
	}

	if _, err := hs.h.ResolveMotion(ctx, "nope", ext.MotionResolveParams{}, callTimeout); !errors.Is(err, ErrUnknownExtension) {
		t.Fatalf("unknown extension: %v", err)
	}
	if err := hs.h.Call(ctx, "x", ext.MethodPing, nil, 0, nil); err == nil {
		t.Fatal("Call with timeout 0 succeeded")
	}
	var rpcErr *rpc.Error
	if err := hs.h.Call(ctx, "x", "no/such", nil, callTimeout, nil); !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeMethodNotFound {
		t.Fatalf("unknown method: %v", err)
	}
}

// 期限を超えた問い合わせは失敗し、$/cancel を送り、後から届いた応答は捨てる。
//
// req: FR-17
func TestCallTimeoutDiscardsLateResponse(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("x", StateRunning)

	slow := ext.TextObjectResolveParams{Version: 1, Count: 100, Args: json.RawMessage(`{"delayMs":400}`)}
	start := time.Now()
	_, err := hs.h.ResolveTextObject(ctx, "x", slow, 50*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("slow query: %v, want ErrTimeout", err)
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("slow query returned after %s", d)
	}

	// 遅れた応答（Count 100）が届く頃に次の問い合わせをしても、自分の応答（Count 7）を受け取る。
	time.Sleep(350 * time.Millisecond)
	for i := range 5 {
		res, err := hs.h.ResolveTextObject(ctx, "x", ext.TextObjectResolveParams{Version: 1, Count: 7 + i}, callTimeout)
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Range.End.Byte; got != 7+i {
			t.Fatalf("query %d got End.Byte %d, want %d (late response leaked)", i, got, 7+i)
		}
	}

	s := hs.fakeState()
	if !slices.Contains(s.Log, "cancelled:textObject/resolve:100") {
		t.Fatalf("extension did not receive $/cancel: %v", s.Log)
	}
	// 捨てた応答は、イベントとしても渡さない。
	hs.mu.Lock()
	defer hs.mu.Unlock()
	for _, ev := range hs.events {
		if _, ok := ev.(*QueryResult); ok {
			t.Fatalf("unexpected QueryResult event %+v", ev)
		}
	}
}

// 期限 0 の問い合わせは待たずに戻り、結果は届いた時点でイベントとして渡す。
//
// req: FR-18
func TestSendDeliversResultLater(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)

	type tag struct {
		version ext.Version
		cursor  ext.Position
	}
	p := ext.MotionResolveParams{Version: 9, Cursor: ext.Position{Line: 1, Byte: 2}, Count: 3, Args: json.RawMessage(`{"delayMs":200}`)}
	start := time.Now()
	if _, err := hs.h.Send("x", ext.MethodMotionResolve, p, SendOptions{Tag: tag{9, p.Cursor}}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("Send blocked for %s", d)
	}
	ev := hs.waitEvent("query result", func(ev Event) bool { _, ok := ev.(*QueryResult); return ok })
	qr := ev.(*QueryResult)
	if qr.Extension != "x" || qr.Method != ext.MethodMotionResolve || qr.Tag != (tag{9, p.Cursor}) {
		t.Fatalf("QueryResult = %+v", qr)
	}
	var res ext.MotionResolveResult
	if err := qr.Decode(&res); err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Version != 9 || res.Position != (ext.Position{Line: 1, Byte: 5}) {
		t.Fatalf("result = %+v", res)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatal("result arrived before the extension replied")
	}

	if _, err := hs.h.Send("nope", ext.MethodMotionResolve, p, SendOptions{}); !errors.Is(err, ErrUnknownExtension) {
		t.Fatalf("unknown extension: %v", err)
	}
}

// 拡張機能からの buffer/edit をイベントループへ渡し、その応答を拡張機能へ返す。
// decoration/set と message/show もイベントとして渡す。
func TestRequestsFromExtension(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)

	params := ext.BufferEditParams{Buffer: 3, Transaction: ext.Transaction{BaseVersion: 5, Edits: []ext.Edit{{Text: "a"}}}}
	if _, err := hs.h.Send("x", methodEdit, params, SendOptions{Tag: "edit"}); err != nil {
		t.Fatal(err)
	}
	ev := hs.waitEvent("edit request", func(ev Event) bool { _, ok := ev.(*EditRequest); return ok })
	er := ev.(*EditRequest)
	if er.Extension != "x" || er.Params.Buffer != 3 || er.Params.Transaction.BaseVersion != 5 {
		t.Fatalf("EditRequest = %+v", er)
	}
	if err := er.Reply(ext.BufferEditResult{Applied: true, Version: 6}); err != nil {
		t.Fatal(err)
	}
	qr := hs.waitEvent("edit result", func(ev Event) bool { _, ok := ev.(*QueryResult); return ok }).(*QueryResult)
	var res ext.BufferEditResult
	if err := qr.Decode(&res); err != nil || res != (ext.BufferEditResult{Applied: true, Version: 6}) {
		t.Fatalf("edit result = %+v, %v", res, err)
	}

	if err := hs.h.Call(ctx, "x", methodNotify, nil, callTimeout, nil); err != nil {
		t.Fatal(err)
	}
	ds := hs.waitEvent("decoration", func(ev Event) bool { _, ok := ev.(*DecorationSet); return ok }).(*DecorationSet)
	if ds.Extension != "x" || ds.Params.Version != 7 || len(ds.Params.Decorations) != 1 {
		t.Fatalf("DecorationSet = %+v", ds)
	}
	ms := hs.waitEvent("message", func(ev Event) bool { _, ok := ev.(*MessageShow); return ok }).(*MessageShow)
	if ms.Params.Text != "hello" {
		t.Fatalf("MessageShow = %+v", ms)
	}
	pr := hs.waitEvent("progress", func(ev Event) bool { _, ok := ev.(*ProgressReport); return ok }).(*ProgressReport)
	if pr.Extension != "x" || pr.Params.Token != "t" || pr.Params.Title != "load" || pr.Params.Percentage == nil || *pr.Params.Percentage != 5 {
		t.Fatalf("ProgressReport = %+v（識別子の無いものを捨てていない）", pr)
	}
	bs := hs.waitEvent("band", func(ev Event) bool { _, ok := ev.(*BandSet); return ok }).(*BandSet)
	if bs.Extension != "x" || bs.Params.Window != "client/1" || len(bs.Params.Lines) != 1 || *bs.Params.Lines[0].Line != 2 {
		t.Fatalf("BandSet = %+v（窓の無いものを捨てていない）", bs)
	}
	vs := hs.waitEvent("virtual text", func(ev Event) bool { _, ok := ev.(*VirtualTextSet); return ok }).(*VirtualTextSet)
	if vs.Extension != "x" || vs.Params.Version != 9 || len(vs.Params.VirtualTexts) != 1 {
		t.Fatalf("VirtualTextSet = %+v（置き方の誤ったものを捨てていない）", vs)
	}
}

// window/change を受け取ると名乗った（宣言ファイルの windowEvents）稼働中の拡張機能だけを返す。未起動のものは起動しない（FR-235）。
//
// req: FR-235
func TestWindowEventExtensions(t *testing.T) {
	watch := fakeSpec(t, "watch", modeNormal, "")
	watch.Manifest.WindowEvents = true
	idle := fakeSpec(t, "idle", modeNormal, "")
	idle.Manifest.WindowEvents = true
	hs := newHarness(t, []Spec{watch, idle, fakeSpec(t, "plain", modeNormal, "")}, nil, true)
	if got := hs.h.WindowEventExtensions(); len(got) != 0 {
		t.Errorf("起動の前 = %q, want なし", got)
	}
	hs.h.Activate(ext.ActivationTextObject, "watch.obj")
	hs.h.Activate(ext.ActivationTextObject, "plain.obj")
	hs.waitState("watch", StateRunning)
	hs.waitState("plain", StateRunning)
	if got := hs.h.WindowEventExtensions(); !slices.Equal(got, []string{"watch"}) {
		t.Errorf("WindowEventExtensions = %q, want [watch]", got)
	}
	if st, _ := hs.h.State("idle"); st != StateNotStarted {
		t.Errorf("idle の状態 = %v, want 未起動のまま", st)
	}
}

// 拡張機能の標準エラー出力を記録する。
func TestStderrLogged(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("x", StateRunning)
	hs.waitLog("fake extension started mode=normal")
}

// クラッシュ（exit と panic）した拡張機能を再起動し、開いているバッファの全文を送り直す。
// 本体（テストのプロセス）は動き続ける。
//
// req: NFR-03, FR-16
func TestRestartAfterCrash(t *testing.T) {
	for _, tc := range []struct {
		method  string
		logLine string
	}{
		{methodExit, "exit status 3"},
		{methodPanic, "fake extension panic"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)
			b := buffer.New("abc")
			if err := hs.h.OpenBuffer(snapshot(1, "", b)); err != nil {
				t.Fatal(err)
			}
			hs.h.Activate(ext.ActivationTextObject, "x.obj")
			hs.waitState("x", StateRunning)
			before := hs.fakeState().PID

			if err := hs.h.Call(ctx, "x", tc.method, nil, callTimeout, nil); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("call %s: %v, want ErrUnavailable", tc.method, err)
			}
			sc := hs.waitState("x", StateRestartWait)
			if !errors.Is(sc.Err, errExited) && !errors.Is(sc.Err, errConnClosed) {
				t.Fatalf("restart reason = %v", sc.Err)
			}
			// 再起動待ちの間の変更は写しに適用し、再起動した拡張機能には全文で届く。
			c, err := b.Apply(buffer.Transaction{BaseVersion: 0, Edits: []buffer.Edit{{Text: "new "}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := hs.h.ChangeBuffer(1, c); err != nil {
				t.Fatal(err)
			}
			hs.waitState("x", StateRunning)
			s := hs.fakeState()
			if s.PID == before {
				t.Fatal("extension was not restarted")
			}
			if got := s.Buffers[1]; got.Text != "new abc" || got.Version != 1 {
				t.Fatalf("after restart: %+v", got)
			}
			if !processGone(before) {
				t.Fatalf("old process %d is still alive", before)
			}
			hs.waitLog(tc.logLine)
		})
	}
}

// 無限ループに入った拡張機能は、生存確認の期限切れで強制終了して再起動する。
//
// req: NFR-03
func TestKillHungExtension(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, func(c *Config) { c.PingTimeout = 300 * time.Millisecond }, true)
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("x", StateRunning)
	before := hs.fakeState().PID

	if err := hs.h.Call(ctx, "x", methodHang, nil, 100*time.Millisecond, nil); !errors.Is(err, ErrTimeout) {
		t.Fatalf("call test/hang: %v, want ErrTimeout", err)
	}
	// 拡張機能が応答しない間も、本体の呼び出しは期限で戻る。
	if _, err := hs.h.ResolveMotion(ctx, "x", ext.MotionResolveParams{}, 50*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("query to hung extension: %v, want ErrTimeout", err)
	}
	sc := hs.waitState("x", StateRestartWait)
	if !errors.Is(sc.Err, errPingTimeout) {
		t.Fatalf("restart reason = %v, want ping timeout", sc.Err)
	}
	if !processGone(before) {
		t.Fatalf("hung process %d is still alive", before)
	}
	hs.waitState("x", StateRunning)
}

// 標準出力を継承した孫プロセスを持つ拡張機能が終了しても、プロセスグループごと終わらせて再起動する。
//
// req: NFR-03
func TestKillGrandchild(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)
	var pid int
	if err := hs.h.Call(ctx, "x", methodGrandchild, nil, callTimeout, &pid); err != nil {
		t.Fatal(err)
	}
	hs.waitState("x", StateRunning)
	if _, err := hs.h.Send("x", methodExit, nil, SendOptions{}); err != nil {
		t.Fatal(err)
	}
	sc := hs.waitState("x", StateRestartWait)
	if !errors.Is(sc.Err, errExited) {
		t.Fatalf("restart reason = %v, want exited", sc.Err)
	}
	if !processGone(pid) {
		t.Fatalf("grandchild %d is still alive", pid)
	}
	hs.waitState("x", StateRunning)
}

// JSON-RPC として解釈できないメッセージを送った拡張機能を異常と判断する。
//
// req: NFR-03
func TestProtocolError(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("x", StateRunning)
	if _, err := hs.h.Send("x", methodGarbage, nil, SendOptions{}); err != nil {
		t.Fatal(err)
	}
	sc := hs.waitState("x", StateRestartWait)
	if !errors.Is(sc.Err, rpc.ErrProtocol) {
		t.Fatalf("restart reason = %v, want protocol error", sc.Err)
	}
	hs.waitState("x", StateRunning)
}

// 初期化の応答が期限までに届かない拡張機能を異常と判断する。
//
// req: NFR-03
func TestInitTimeout(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNoInit, "")}, func(c *Config) { c.InitTimeout = 200 * time.Millisecond }, true)
	_, err := hs.h.ResolveMotion(ctx, "x", ext.MotionResolveParams{}, 100*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("query while starting: %v, want ErrTimeout", err)
	}
	sc := hs.waitState("x", StateRestartWait)
	if !errors.Is(sc.Err, errInitTimeout) {
		t.Fatalf("restart reason = %v, want init timeout", sc.Err)
	}
}

// 連続の失敗が上限に達したら無効にし、問い合わせは送らずに失敗させる。利用者の指示で起動し直せる。
//
// req: NFR-03
func TestDisableAfterRepeatedFailures(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeExit, "")}, nil, true)
	// MaxFailures は 3。図 4 のとおり、上限に達した失敗でも再起動待ちを経てから無効になる。
	want := []State{StateStarting, StateRestartWait, StateStarting, StateRestartWait, StateStarting, StateRestartWait, StateDisabled}

	from := hs.mark()
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	sc := hs.waitState("x", StateDisabled)
	if !errors.Is(sc.Err, errExited) {
		t.Fatalf("disable reason = %v", sc.Err)
	}
	if got := hs.statesSince(from, "x"); !slices.Equal(got, want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	if _, err := hs.h.ResolveMotion(ctx, "x", ext.MotionResolveParams{}, callTimeout); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("query to disabled extension: %v, want ErrUnavailable", err)
	}
	if _, err := hs.h.Send("x", ext.MethodMotionResolve, nil, SendOptions{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("send to disabled extension: %v, want ErrUnavailable", err)
	}
	if hs.h.Activate(ext.ActivationTextObject, "x.obj") != nil {
		t.Fatal("activation restarted a disabled extension")
	}

	// Restart は連続の失敗の数を 0 に戻すので、無効になるまでに再び MaxFailures 回の失敗を要する。
	from = hs.mark()
	if err := hs.h.Restart("x"); err != nil {
		t.Fatal(err)
	}
	hs.waitState("x", StateDisabled)
	if got := hs.statesSince(from, "x"); !slices.Equal(got, want) {
		t.Fatalf("states after Restart = %v, want %v", got, want)
	}
}

// 再起動の待ち時間は、失敗が続くたびに延ばし、上限で頭打ちにする。
func TestRestartDelay(t *testing.T) {
	h := &Host{cfg: Config{RestartDelay: 10 * time.Millisecond, MaxRestartDelay: 50 * time.Millisecond}}
	for i, want := range []time.Duration{10, 20, 40, 50, 50} {
		if got := h.restartDelay(i + 1); got != want*time.Millisecond {
			t.Errorf("restartDelay(%d) = %s, want %s", i+1, got, want*time.Millisecond)
		}
	}
}

// 通知を送り続ける拡張機能は、イベントの列の上限を超えた時点で異常と判断する。
//
// req: NFR-03
func TestEventQueueLimit(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, func(c *Config) { c.MaxQueuedEvents = 10 }, false)
	// イベントを受け取らないので、Send ではなく Call で送る。
	err := hs.h.Call(ctx, "x", methodFlood, 100, callTimeout, nil)
	if err != nil && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("flood: %v", err)
	}
	hs.waitLog("too many queued events")
}

// 同じ部品への view/append は、列の末尾で続けば 1 件につなぐので、出力の行の数で列の上限に当たらない。
// つないでも、書き足したテキストは欠けず、順も変わらない。
//
// req: FR-272
func TestViewAppendQueueMerge(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, func(c *Config) { c.MaxQueuedEvents = 10 }, false)
	if err := hs.h.Call(ctx, "x", methodAppendFlood, 500, callTimeout, nil); err != nil {
		t.Fatalf("append flood: %v", err)
	}
	var want strings.Builder
	for i := range 500 {
		fmt.Fprintf(&want, "%d\n", i)
	}
	var got strings.Builder
	timer := time.After(callTimeout)
	for got.Len() < want.Len() {
		select {
		case ev := <-hs.h.Events():
			if a, ok := ev.(*ViewAppend); ok && a.Extension == "x" && a.Params.View == 1 {
				got.WriteString(a.Params.Text)
			}
		case <-timer:
			t.Fatalf("appends: got %d bytes, want %d", got.Len(), want.Len())
		}
	}
	if got.String() != want.String() {
		t.Errorf("merged appends differ")
	}
	if strings.Contains(hs.log.String(), "too many queued events") {
		t.Errorf("view/append hit the queue limit")
	}
}

// Close はすべての拡張機能のプロセスを終わらせ、イベントのチャネルを閉じる。
func TestClose(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, false)
	var s fakeState
	if err := hs.h.Call(ctx, "x", methodState, nil, callTimeout, &s); err != nil {
		t.Fatal(err)
	}
	if err := hs.h.Close(); err != nil {
		t.Fatal(err)
	}
	if !processGone(s.PID) {
		t.Fatalf("process %d is still alive after Close", s.PID)
	}
	for range hs.h.Events() {
	}
	if err := hs.h.Call(ctx, "x", methodState, nil, callTimeout, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after Close: %v, want ErrClosed", err)
	}
	if err := hs.h.OpenBuffer(snapshot(1, "", buffer.New(""))); !errors.Is(err, ErrClosed) {
		t.Fatalf("open after Close: %v, want ErrClosed", err)
	}
	if err := hs.h.Close(); err != nil {
		t.Fatal(err)
	}
}

// 提供者は、操作を名乗り、ファイルの種類の候補になる拡張機能を、読んだ順に探す。無効になった拡張機能は飛ばす。
//
// req: FR-94
func TestProvider(t *testing.T) {
	goLSP := fakeSpec(t, "golsp", modeNormal, "go")
	goLSP.Manifest.Provides = []ext.Operation{ext.OpHover, ext.OpFormat}
	pyLSP := fakeSpec(t, "pylsp", modeNormal, "python")
	pyLSP.Manifest.Provides = []ext.Operation{ext.OpHover}
	broken := fakeSpec(t, "broken", modeExit, "")
	broken.Manifest.Provides = []ext.Operation{ext.OpDebugContinue}
	dap := fakeSpec(t, "dap", modeNormal, "")
	dap.Manifest.Provides = []ext.Operation{ext.OpDebugContinue, ext.OpHover}
	hs := newHarness(t, []Spec{goLSP, pyLSP, broken, dap}, nil, true)

	for _, c := range []struct {
		op   ext.Operation
		ft   string
		want string
	}{
		{ext.OpHover, "go", "golsp"},
		{ext.OpHover, "python", "pylsp"},
		// ファイルの種類の契機を持たない dap は、どのファイルの種類でも候補になる。
		{ext.OpHover, "markdown", "dap"},
		{ext.OpFormat, "go", "golsp"},
		{ext.OpFormat, "python", ""},
		{ext.OpDebugContinue, "go", "broken"},
		{ext.OpRename, "go", ""},
	} {
		got, ok := hs.h.Provider(c.op, c.ft)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("Provider(%s, %q) = %q, %v, want %q", c.op, c.ft, got, ok, c.want)
		}
	}
	// Provider は起動しない。
	if st, _ := hs.h.State("golsp"); st != StateNotStarted {
		t.Errorf("golsp state = %s after Provider", st)
	}

	// 無効になった拡張機能は飛ばし、次の候補を使う。
	if err := hs.h.Start("broken"); err != nil {
		t.Fatal(err)
	}
	hs.waitState("broken", StateDisabled)
	if got, ok := hs.h.Provider(ext.OpDebugContinue, "go"); got != "dap" || !ok {
		t.Errorf("Provider after broken is disabled = %q, %v, want dap", got, ok)
	}
}

func TestNewRejectsDuplicateID(t *testing.T) {
	s := Spec{Manifest: ext.Manifest{ID: "a"}}
	if _, err := New([]Spec{s, s}, Config{}); err == nil {
		t.Fatal("New accepted duplicate ids")
	}
}

// applyEdits は、internal/buffer の Apply と同じ結果を返す。
func TestApplyEditsMatchesBuffer(t *testing.T) {
	pos := func(l, b int) buffer.Position { return buffer.Position{Line: l, Byte: b} }
	cases := [][]buffer.Edit{
		{{Range: buffer.Range{Start: pos(0, 0), End: pos(0, 0)}, Text: "x"}},
		{{Range: buffer.Range{Start: pos(0, 1), End: pos(2, 1)}, Text: ""}},
		{{Range: buffer.Range{Start: pos(1, 0), End: pos(1, 3)}, Text: "a\nb\n"}, {Range: buffer.Range{Start: pos(3, 0), End: pos(4, 0)}, Text: "Z"}},
		{{Range: buffer.Range{Start: pos(2, 3), End: pos(2, 3)}, Text: "\n"}, {Range: buffer.Range{Start: pos(3, 0), End: pos(3, 0)}, Text: "end"}},
	}
	for i, edits := range cases {
		b := buffer.New("abc\ndéf\nghi\n")
		lines := strings.Split(b.Text(), "\n")
		c, err := b.Apply(buffer.Transaction{Edits: edits})
		if err != nil {
			t.Fatalf("case %d: buffer: %v", i, err)
		}
		var ee []ext.Edit
		for _, e := range c.Edits {
			ee = append(ee, toExtEdit(e))
		}
		got, err := applyEdits(lines, ee)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if strings.Join(got, "\n") != b.Text() {
			t.Errorf("case %d: got %q, want %q", i, strings.Join(got, "\n"), b.Text())
		}
	}
	if _, err := applyEdits([]string{"a"}, []ext.Edit{{Range: ext.Range{Start: ext.Position{Byte: 1}, End: ext.Position{Byte: 0}}}}); err == nil {
		t.Error("reversed range accepted")
	}
	if _, err := applyEdits([]string{"a"}, []ext.Edit{{Range: ext.Range{End: ext.Position{Byte: 2}}}}); err == nil {
		t.Error("out-of-range position accepted")
	}
}

// 利用者の中断（ctx の取り消し）で待つのをやめ、$/cancel を送る。
//
// req: FR-17
func TestCallContextCancel(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("x", StateRunning)

	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(50*time.Millisecond, cancel)
	p := ext.TextObjectResolveParams{Count: 11, Args: json.RawMessage(`{"delayMs":5000}`)}
	start := time.Now()
	_, err := hs.h.ResolveTextObject(cctx, "x", p, time.Minute)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrTimeout) {
		t.Fatalf("cancelled call: %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("cancelled call returned after %s", d)
	}
	deadline := time.Now().Add(waitTimeout)
	for !slices.Contains(hs.fakeState().Log, "cancelled:textObject/resolve:11") {
		if time.Now().After(deadline) {
			t.Fatal("extension did not receive $/cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Send の問い合わせは期限で打ち切り、Query.Cancel で取り消せる。どちらも結果のイベントを 1 回だけ渡す。
//
// req: FR-17, FR-18
func TestSendTimeoutAndCancel(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, func(c *Config) { c.SendTimeout = 200 * time.Millisecond }, true)
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("x", StateRunning)
	slow := func(count int) ext.MotionResolveParams {
		return ext.MotionResolveParams{Count: count, Args: json.RawMessage(`{"delayMs":100000}`)}
	}
	result := func(tag string) *QueryResult {
		return hs.waitEvent("query result "+tag, func(ev Event) bool {
			qr, ok := ev.(*QueryResult)
			return ok && qr.Tag == tag
		}).(*QueryResult)
	}

	if _, err := hs.h.Send("x", ext.MethodMotionResolve, slow(1), SendOptions{Tag: "explicit", Timeout: 50 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if qr := result("explicit"); !errors.Is(qr.Err, ErrTimeout) {
		t.Fatalf("explicit timeout: %v", qr.Err)
	}
	if _, err := hs.h.Send("x", ext.MethodMotionResolve, slow(2), SendOptions{Tag: "default"}); err != nil {
		t.Fatal(err)
	}
	if qr := result("default"); !errors.Is(qr.Err, ErrTimeout) {
		t.Fatalf("default timeout: %v", qr.Err)
	}

	// 新しい問い合わせで古いものを取り消す使い方。取り消した数だけ結果が届き、待ち受けは残らない。
	const n = 200
	for i := range n {
		q, err := hs.h.Send("x", ext.MethodMotionResolve, slow(100+i), SendOptions{Tag: fmt.Sprint("c", i), Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		q.Cancel()
		q.Cancel()
	}
	for i := range n {
		if qr := result(fmt.Sprint("c", i)); !errors.Is(qr.Err, context.Canceled) {
			t.Fatalf("cancelled query %d: %v", i, qr.Err)
		}
	}
	hs.mu.Lock()
	var results int
	for _, ev := range hs.events {
		if _, ok := ev.(*QueryResult); ok {
			results++
		}
	}
	hs.mu.Unlock()
	if results != n+2 {
		t.Fatalf("%d query results, want %d", results, n+2)
	}
}

// 標準入力を読まない拡張機能だけを、送信の列の上限で異常とする。
// 上限より大きいバッファの全文は、読んでいる拡張機能には送れる。
//
// req: NFR-03
func TestOutboxLimitIsolated(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, ""), fakeSpec(t, "y", modeNormal, "")}, func(c *Config) {
		c.MaxOutboxBytes = 64 << 10
		c.PingInterval = time.Minute
	}, true)
	// 全文（約 160 KiB）は上限より大きい。
	b := buffer.New(strings.Repeat("0123456789abcdef\n", 10000))
	if err := hs.h.OpenBuffer(snapshot(1, "", b)); err != nil {
		t.Fatal(err)
	}
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.h.Activate(ext.ActivationTextObject, "y.obj")
	hs.waitState("x", StateRunning)
	hs.waitState("y", StateRunning)

	if err := hs.h.Call(ctx, "x", methodHang, nil, 100*time.Millisecond, nil); !errors.Is(err, ErrTimeout) {
		t.Fatalf("call test/hang: %v", err)
	}
	// 読んでいる拡張機能 y が追いつける速さで差分を送り、読まない x の列だけを溜める。
	line := strings.Repeat("z", 1000) + "\n"
	for i := 0; hs.state("x") == StateRunning; i++ {
		if i == 3000 {
			t.Fatal("x was not stopped by the outbox limit")
		}
		time.Sleep(time.Millisecond)
		c, err := b.Apply(buffer.Transaction{BaseVersion: b.Version(), Edits: []buffer.Edit{{Text: line}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := hs.h.ChangeBuffer(1, c); err != nil {
			t.Fatal(err)
		}
	}
	sc := hs.waitState("x", StateRestartWait)
	if !errors.Is(sc.Err, ErrOutboxFull) {
		t.Fatalf("restart reason = %v, want ErrOutboxFull", sc.Err)
	}
	if st := hs.state("y"); st != StateRunning {
		t.Fatalf("y: state %s, want running", st)
	}
	var s fakeState
	if err := hs.h.Call(ctx, "y", methodState, nil, callTimeout, &s); err != nil {
		t.Fatal(err)
	}
	if got := s.Buffers[1]; got.Text != b.Text() || got.Version != ext.Version(b.Version()) {
		t.Fatalf("y: buffer version %d len %d, want version %d len %d", got.Version, len(got.Text), b.Version(), len(b.Text()))
	}
}

// 開いた後にファイルの種類とパスが変わったら、稼働中の拡張機能へ送り直し、新しいファイルの種類の契機で起動する。
//
// req: FR-14
func TestUpdateBuffer(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, ""), fakeSpec(t, "md", modeNormal, "markdown")}, nil, true)
	b := buffer.New("# t")
	if err := hs.h.OpenBuffer(BufferSnapshot{ID: 1, Path: "/tmp/a.txt", FileType: "text", Text: b.Text()}); err != nil {
		t.Fatal(err)
	}
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("x", StateRunning)
	if st := hs.state("md"); st != StateNotStarted {
		t.Fatalf("md: state %s, want not-started", st)
	}

	if err := hs.h.UpdateBuffer(1, "/tmp/a.md", "markdown"); err != nil {
		t.Fatal(err)
	}
	hs.waitState("md", StateRunning)
	var opens int
	for _, l := range hs.fakeState().Log {
		if l == "open:1:0" {
			opens++
		}
	}
	if opens != 2 {
		t.Fatalf("x received %d buffer/open, want 2", opens)
	}
	var s fakeState
	if err := hs.h.Call(ctx, "md", methodState, nil, callTimeout, &s); err != nil {
		t.Fatal(err)
	}
	if s.Buffers[1].Text != "# t" {
		t.Fatalf("md buffers = %v", s.Buffers)
	}
	if err := hs.h.UpdateBuffer(2, "", ""); !errors.Is(err, ErrBufferState) {
		t.Fatalf("update of unopened buffer: %v", err)
	}
}

// buffer/open の finalNewline は、BufferSnapshot の NoFinalNewline の逆で、既定（ゼロ値）は改行が付く。
// SetFinalNewline で値が変わったら、稼働中の拡張機能へ buffer/open を送り直す。
func TestFinalNewline(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)
	hs.h.Activate(ext.ActivationTextObject, "x.obj")
	hs.waitState("x", StateRunning)
	b := buffer.New("a")
	if err := hs.h.OpenBuffer(BufferSnapshot{ID: 1, Text: b.Text()}); err != nil {
		t.Fatal(err)
	}
	b.SetFixEndOfLine(false)
	if err := hs.h.OpenBuffer(BufferSnapshot{ID: 2, Text: b.Text(), NoFinalNewline: !b.FinalNewline()}); err != nil {
		t.Fatal(err)
	}
	state := func() fakeState {
		t.Helper()
		var s fakeState
		if err := hs.h.Call(ctx, "x", methodState, nil, callTimeout, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	check := func(s fakeState, id ext.BufferID, want bool) {
		t.Helper()
		if got := s.FinalNewline[id]; got == nil || *got != want {
			t.Errorf("buffer %d: finalNewline = %v, want %v", id, got, want)
		}
	}
	s := state()
	check(s, 1, true)
	check(s, 2, false)

	if err := hs.h.SetFinalNewline(2, true); err != nil {
		t.Fatal(err)
	}
	if err := hs.h.SetFinalNewline(1, true); err != nil { // 変わらないので送らない
		t.Fatal(err)
	}
	s = state()
	check(s, 1, true)
	check(s, 2, true)
	var opens int
	for _, l := range s.Log {
		if strings.HasPrefix(l, "open:") {
			opens++
		}
	}
	if opens != 3 {
		t.Errorf("received %d buffer/open, want 3: %v", opens, s.Log)
	}
	if err := hs.h.SetFinalNewline(3, false); !errors.Is(err, ErrBufferState) {
		t.Errorf("SetFinalNewline of unopened buffer: %v", err)
	}
}

// command/execute を問い合わせる。未起動の拡張機能は、この呼び出しで起動する。
func TestExecuteCommand(t *testing.T) {
	hs := newHarness(t, []Spec{fakeSpec(t, "x", modeNormal, "")}, nil, true)
	res, err := hs.h.ExecuteCommand(ctx, "x", ext.CommandExecuteParams{Version: 3, ID: "x.cmd"}, callTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Transaction == nil || res.Transaction.BaseVersion != 3 || res.Transaction.Edits[0].Text != "x.cmd" {
		t.Fatalf("result = %+v", res)
	}
}

// 子プロセスは本体の作業ディレクトリを引き継ぎ、宣言ファイルのディレクトリを環境変数 ext.EnvExtensionDir で受け取る（基本設計 5.1）。
// 相対パスの Dir は絶対パスにして渡す。Dir が空なら空の値を渡す。
func TestExtensionDirEnv(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	abs := t.TempDir()
	for _, tc := range []struct {
		name, dir, want string
	}{
		{"absolute", abs, abs},
		{"relative", "testdata", filepath.Join(wd, "testdata")},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 本体の環境にある同じ名前の変数は、宣言ファイルのディレクトリで上書きする。
			t.Setenv(ext.EnvExtensionDir, "/stale")
			spec := fakeSpec(t, "x", modeNormal, "") // command は絶対パスなので、Dir からは解決されない。
			spec.Dir = tc.dir
			hs := newHarness(t, []Spec{spec}, nil, true)
			var got fakeEnv
			if err := hs.h.Call(ctx, "x", methodEnv, nil, callTimeout, &got); err != nil {
				t.Fatal(err)
			}
			if !got.DirSet || got.Dir != tc.want {
				t.Errorf("%s = %q (set %v), want %q", ext.EnvExtensionDir, got.Dir, got.DirSet, tc.want)
			}
			if got.Wd != wd {
				t.Errorf("working directory = %q, want %q", got.Wd, wd)
			}
		})
	}
}
