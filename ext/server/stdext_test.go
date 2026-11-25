package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// stdHost は、標準の拡張機能のリクエストに決まった結果で答え、受け取った params を記録する本体の役である。
type stdHost struct {
	params chan string
}

func (sh *stdHost) next(t *testing.T) string {
	t.Helper()
	select {
	case p := <-sh.params:
		return p
	case <-time.After(timeout):
		t.Fatal("no request reached the host")
		return ""
	}
}

func (sh *stdHost) handle(req *rpc.Request, reply rpc.Replier) bool {
	var res any
	switch req.Method {
	case ext.MethodEditorState:
		res = ext.EditorStateResult{Buffer: 1, Mode: ext.ModeInsert, Buffers: []ext.BufferInfo{{Buffer: 1, Path: "/a", Version: 3}}}
	case ext.MethodEditorOpen:
		res = ext.EditorOpenResult{Buffer: 2, Version: 1, Moved: true}
	case ext.MethodBufferSave:
		res = ext.BufferSaveResult{Saved: true, Version: 3}
	case ext.MethodDiagnosticList:
		res = ext.DiagnosticListResult{Entries: []ext.DiagnosticEntry{{Buffer: 1, Extension: "lsp", Version: 3}}}
	case ext.MethodChoiceAsk:
		res = ext.ChoiceAskResult{Selected: "yes"}
	case ext.MethodInputAsk:
		res = ext.InputAskResult{Value: ""}
	case ext.MethodEditorList:
		res = ext.EditorListResult{Tabs: []ext.ListTab{{Buffer: 1, Name: "a", Client: 1}}}
	case ext.MethodEditorExecute:
		res = ext.EditorExecuteResult{}
	case ext.MethodOptionSet:
		res = ext.OptionSetResult{}
	case ext.MethodRegisterSet:
		res = ext.RegisterSetResult{}
	case ext.MethodOptionGet:
		res = ext.OptionGetResult{Values: map[string]json.RawMessage{"indent.width": json.RawMessage("2")}}
	default:
		return false
	}
	sh.params <- req.Method + " " + string(req.Params)
	_ = reply(res, nil)
	return true
}

// 拡張機能から本体へのリクエストを送り、本体の結果を受け取る。
func TestStdExtRequests(t *testing.T) {
	s := server.New()
	sh := &stdHost{params: make(chan string, 8)}
	startWith(t, s, sh.handle).ping(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	st, err := s.EditorState(ctx)
	if err != nil || st.Mode != ext.ModeInsert || len(st.Buffers) != 1 || st.Buffers[0].Path != "/a" {
		t.Errorf("EditorState = %+v, %v", st, err)
	}
	if got := sh.next(t); got != "editor/state {}" {
		t.Errorf("host received %s", got)
	}

	pos := ext.Position{Line: 4}
	op, err := s.OpenEditor(ctx, ext.EditorOpenParams{Path: "/b", Position: &pos, Focus: true})
	if err != nil || op != (ext.EditorOpenResult{Buffer: 2, Version: 1, Moved: true}) {
		t.Errorf("OpenEditor = %+v, %v", op, err)
	}
	if got := sh.next(t); got != `editor/open {"path":"/b","position":{"line":4,"byte":0},"focus":true}` {
		t.Errorf("host received %s", got)
	}

	sv, err := s.SaveBuffer(ctx, ext.BufferSaveParams{Buffer: 1, SkipHooks: true})
	if err != nil || sv != (ext.BufferSaveResult{Saved: true, Version: 3}) {
		t.Errorf("SaveBuffer = %+v, %v", sv, err)
	}
	if got := sh.next(t); got != `buffer/save {"buffer":1,"skipHooks":true}` {
		t.Errorf("host received %s", got)
	}

	dl, err := s.ListDiagnostics(ctx, ext.DiagnosticListParams{})
	if err != nil || len(dl.Entries) != 1 || dl.Entries[0].Extension != "lsp" {
		t.Errorf("ListDiagnostics = %+v, %v", dl, err)
	}
	if got := sh.next(t); got != "diagnostic/list {}" {
		t.Errorf("host received %s", got)
	}

	ch, err := s.AskChoice(ctx, ext.ChoiceAskParams{Title: "q", Choices: []ext.Choice{{ID: "yes", Label: "Yes"}}})
	if err != nil || ch.Selected != "yes" {
		t.Errorf("AskChoice = %+v, %v", ch, err)
	}
	if got := sh.next(t); got != `choice/ask {"title":"q","choices":[{"id":"yes","label":"Yes"}]}` {
		t.Errorf("host received %s", got)
	}

	// 空の文字列の確定は、取り消しと区別できる（Cancelled が false）。
	in, err := s.AskInput(ctx, ext.InputAskParams{Title: "式: ", Default: "a+b"})
	if err != nil || in != (ext.InputAskResult{}) {
		t.Errorf("AskInput = %+v, %v", in, err)
	}
	if got := sh.next(t); got != `input/ask {"title":"式: ","default":"a+b"}` {
		t.Errorf("host received %s", got)
	}

	ls, err := s.ListEditor(ctx, ext.ListTabs)
	if err != nil || len(ls.Tabs) != 1 || ls.Tabs[0].Name != "a" {
		t.Errorf("ListEditor = %+v, %v", ls, err)
	}
	if got := sh.next(t); got != `editor/list {"list":"tabs"}` {
		t.Errorf("host received %s", got)
	}

	if err := s.Execute(ctx, ext.EditorExecuteParams{Selection: 1, Keys: "dd"}); err != nil {
		t.Errorf("Execute = %v", err)
	}
	if got := sh.next(t); got != `editor/execute {"selection":1,"keys":"dd"}` {
		t.Errorf("host received %s", got)
	}
	if err := s.Execute(ctx, ext.EditorExecuteParams{}); err == nil {
		t.Error("Execute with no keys and command succeeded")
	}

	if err := s.SetOptions(ctx, ext.OptionSetParams{Buffer: 1, Options: map[string]json.RawMessage{"indent.width": json.RawMessage("2")}}); err != nil {
		t.Errorf("SetOptions = %v", err)
	}
	if got := sh.next(t); got != `option/set {"buffer":1,"options":{"indent.width":2}}` {
		t.Errorf("host received %s", got)
	}
	if err := s.SetOptions(ctx, ext.OptionSetParams{Buffer: 1}); err == nil {
		t.Error("SetOptions with no options succeeded")
	}
	if og, err := s.GetOptions(ctx, ext.OptionGetParams{Buffer: 1, Names: []string{"indent.width"}}); err != nil || string(og.Values["indent.width"]) != "2" {
		t.Errorf("GetOptions = %+v, %v", og, err)
	}
	if got := sh.next(t); got != `option/get {"buffer":1,"names":["indent.width"]}` {
		t.Errorf("host received %s", got)
	}

	// レジスターへ入れる口（FR-315）。2 文字以上の名前は送らない。
	if err := s.SetRegister(ctx, ext.RegisterSetParams{Register: "a", Text: "値\n2", Linewise: true}); err != nil {
		t.Errorf("SetRegister = %v", err)
	}
	if got := sh.next(t); got != `register/set {"register":"a","text":"値\n2","linewise":true}` {
		t.Errorf("host received %s", got)
	}
	if err := s.SetRegister(ctx, ext.RegisterSetParams{Register: "ab"}); err == nil {
		t.Error("SetRegister with a two-character name succeeded")
	}

	// 誤りのある editor/open と editor/list の params は送らない。
	if _, err := s.OpenEditor(ctx, ext.EditorOpenParams{Path: "relative"}); err == nil {
		t.Error("OpenEditor with a relative path succeeded")
	}
	if _, err := s.ListEditor(ctx, "marks"); err == nil {
		t.Error("ListEditor with an unknown list succeeded")
	}
	// 改行を含む input/ask の問いの文と初めの中身も送らない。
	for _, p := range []ext.InputAskParams{{Title: "a\nb"}, {Title: "q", Default: "x\r"}} {
		if _, err := s.AskInput(ctx, p); err == nil {
			t.Errorf("AskInput(%+v) succeeded", p)
		}
	}
	select {
	case got := <-sh.params:
		t.Errorf("host received %s", got)
	default:
	}
}

// ハンドラーの中から、渡された ctx で本体の応答を待つ関数を呼ぶと、待たずに ErrEditInHandler を返す。
func TestStdExtRequestsInHandler(t *testing.T) {
	s := server.New()
	errs := make(chan []error, 1)
	s.Command("c", func(ctx context.Context, _ *server.Buffer, _ *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
		var got []error
		_, err := s.EditorState(ctx)
		got = append(got, err)
		_, err = s.OpenEditor(ctx, ext.EditorOpenParams{Path: "/a"})
		got = append(got, err)
		_, err = s.SaveBuffer(ctx, ext.BufferSaveParams{Buffer: 1})
		got = append(got, err)
		_, err = s.ListDiagnostics(ctx, ext.DiagnosticListParams{})
		got = append(got, err)
		_, err = s.AskChoice(ctx, ext.ChoiceAskParams{})
		got = append(got, err)
		_, err = s.AskInput(ctx, ext.InputAskParams{Title: "q"})
		got = append(got, err)
		_, err = s.WaitBuffer(ctx, 1, 1)
		got = append(got, err)
		errs <- got
		return ext.CommandExecuteResult{OK: true}, nil
	})
	h := start(t, s)
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1})
	var res ext.CommandExecuteResult
	if err := h.call(t, ext.MethodCommandExecute, ext.CommandExecuteParams{Buffer: 1, Version: 1, ID: "c"}, &res); err != nil || !res.OK {
		t.Fatalf("command/execute = %+v, %v", res, err)
	}
	for i, err := range <-errs {
		if !errors.Is(err, server.ErrEditInHandler) {
			t.Errorf("call %d in handler = %v, want ErrEditInHandler", i, err)
		}
	}
}

func TestStdExtNotifications(t *testing.T) {
	s := server.New()
	if err := s.AppendOutput("dap", "x"); !errors.Is(err, server.ErrNotRunning) {
		t.Errorf("AppendOutput before Serve = %v, want ErrNotRunning", err)
	}
	h := start(t, s)
	h.ping(t)

	diag := ext.DiagnosticSetParams{Buffer: 1, Version: 2, Diagnostics: []ext.Diagnostic{{Severity: ext.SeverityError, Message: "e"}}}
	signs := ext.SignSetParams{Buffer: 1, Version: 2, Signs: []ext.Sign{{Line: 3, Kind: ext.SignBreakpoint}}}
	popup := ext.PopupShowParams{Buffer: 1, Version: 2, Text: "doc", Format: ext.PopupMarkdown}
	for _, f := range []func() error{
		func() error { return s.SetDiagnostics(diag) },
		func() error { return s.SetSigns(signs) },
		func() error { return s.ShowPopup(popup) },
		func() error { return s.AppendOutput("dap", "line\n") },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	checkNote(t, h.nextNote(t), ext.MethodDiagnosticSet, &ext.DiagnosticSetParams{}, &diag)
	checkNote(t, h.nextNote(t), ext.MethodSignSet, &ext.SignSetParams{}, &signs)
	checkNote(t, h.nextNote(t), ext.MethodPopupShow, &ext.PopupShowParams{}, &popup)
	checkNote(t, h.nextNote(t), ext.MethodOutputAppend, &ext.OutputAppendParams{}, &ext.OutputAppendParams{Channel: "dap", Text: "line\n"})
}

func checkNote[T any](t *testing.T, n *rpc.Notification, method string, got, want *T) {
	t.Helper()
	if n.Method != method {
		t.Fatalf("notification %s, want %s", n.Method, method)
	}
	if err := json.Unmarshal(n.Params, got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s params = %+v, want %+v", method, got, want)
	}
}

func willSave(h *host, t *testing.T, v ext.Version) (ext.BufferWillSaveResult, error) {
	t.Helper()
	var res ext.BufferWillSaveResult
	err := h.call(t, ext.MethodBufferWillSave, ext.BufferWillSaveParams{Buffer: 1, Version: v}, &res)
	return res, err
}

// ハンドラーを登録していない拡張機能は、buffer/willSave に編集無しを応答する。
func TestBufferWillSaveWithoutHandler(t *testing.T) {
	h := start(t, server.New())
	res, err := willSave(h, t, 1)
	if err != nil || res.Transaction != nil {
		t.Errorf("buffer/willSave = %+v, %v", res, err)
	}
}

// buffer/willSave のハンドラーは複製を読み、返した編集の BaseVersion は問い合わせのバージョン番号になる。
// 複製のバージョン番号が一致しなければ、ハンドラーを呼ばずに失敗を応答する。
func TestBufferWillSave(t *testing.T) {
	s := server.New()
	s.BufferWillSave(func(_ context.Context, b *server.Buffer, _ *ext.BufferWillSaveParams) (ext.BufferWillSaveResult, error) {
		if b.Text() == "formatted" {
			return ext.BufferWillSaveResult{}, nil
		}
		end := ext.Position{Line: b.LineCount() - 1, Byte: len(b.Line(b.LineCount() - 1))}
		return ext.BufferWillSaveResult{Transaction: &ext.Transaction{
			BaseVersion: 999, Edits: []ext.Edit{{Range: ext.Range{End: end}, Text: "formatted"}},
		}}, nil
	})
	h := start(t, s)
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 4, Text: "a\nb"})

	res, err := willSave(h, t, 4)
	if err != nil || res.Transaction == nil || res.Transaction.BaseVersion != 4 || res.Transaction.Edits[0].Range.End != (ext.Position{Line: 1, Byte: 1}) {
		t.Fatalf("buffer/willSave = %+v, %v", res, err)
	}
	if _, err := willSave(h, t, 3); err == nil {
		t.Error("buffer/willSave with a stale version succeeded")
	}
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 5, Text: "formatted"})
	if res, err := willSave(h, t, 5); err != nil || res.Transaction != nil {
		t.Errorf("buffer/willSave after formatting = %+v, %v", res, err)
	}
}

func TestBufferWillSaveAsync(t *testing.T) {
	s := server.New()
	s.BufferWillSaveAsync(func(_ context.Context, _ *server.Buffer, _ *ext.BufferWillSaveParams, reply func(ext.BufferWillSaveResult, error)) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			reply(ext.BufferWillSaveResult{Transaction: &ext.Transaction{Edits: []ext.Edit{{Text: "x"}}}}, nil)
		}()
	})
	h := start(t, s)
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 2})
	res, err := willSave(h, t, 2)
	if err != nil || res.Transaction == nil || res.Transaction.BaseVersion != 2 {
		t.Errorf("buffer/willSave = %+v, %v", res, err)
	}
}

func TestBufferDidSave(t *testing.T) {
	s := server.New()
	got := make(chan string, 2)
	s.OnBufferDidSave(func(_ context.Context, e *server.BufferDidSaveEvent) {
		text := "<nil>"
		if e.Buffer != nil {
			text = e.Buffer.Text()
		}
		got <- e.Params.Path + " " + text
	})
	h := start(t, s)
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 2, Text: "abc"})
	h.notify(t, ext.MethodBufferDidSave, ext.BufferDidSaveParams{Buffer: 1, Version: 2, Path: "/a"})
	h.notify(t, ext.MethodBufferDidSave, ext.BufferDidSaveParams{Buffer: 9, Version: 1, Path: "/b"})
	h.ping(t)
	for _, want := range []string{"/a abc", "/b <nil>"} {
		if g := <-got; g != want {
			t.Errorf("did save hook got %q, want %q", g, want)
		}
	}
}

// 本体がバッファの同期を送らない（initialize の NoBufferSync。宣言ファイルの noBufferSync）拡張機能では、
// WaitBuffer は ctx の期限まで待たずに、すぐ ErrNoBufferSync を返す（PR #375 の再レビューの推奨）。InitializeEvent にも同じ値を渡す。
//
// req: FR-229
func TestWaitBufferNoBufferSync(t *testing.T) {
	s := server.New()
	var got atomic.Bool
	s.OnInitialize(func(_ context.Context, e *server.InitializeEvent) error {
		got.Store(e.NoBufferSync)
		return nil
	})
	h := start(t, s)
	var res ext.InitializeResult
	if err := h.call(t, ext.MethodInitialize, ext.InitializeParams{APIVersion: ext.APIVersion, NoBufferSync: true}, &res); err != nil {
		t.Fatal(err)
	}
	if !got.Load() {
		t.Error("InitializeEvent.NoBufferSync = false, want true")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	begin := time.Now()
	if _, err := s.WaitBuffer(ctx, 1, 1); !errors.Is(err, server.ErrNoBufferSync) {
		t.Errorf("WaitBuffer = %v, want ErrNoBufferSync", err)
	}
	if d := time.Since(begin); d > time.Second {
		t.Errorf("WaitBuffer が %s 待った", d)
	}
}

// WaitBuffer は、複製が指定のバージョン番号以降になるまで待ち、その時点の Clone を返す。
func TestWaitBuffer(t *testing.T) {
	s := server.New()
	if _, err := s.WaitBuffer(context.Background(), 1, 1); !errors.Is(err, server.ErrNotRunning) {
		t.Errorf("WaitBuffer before Serve = %v, want ErrNotRunning", err)
	}
	h := start(t, s)
	h.ping(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type result struct {
		b   *server.Buffer
		err error
	}
	wait := func(id ext.BufferID, v ext.Version) chan result {
		ch := make(chan result, 1)
		go func() {
			b, err := s.WaitBuffer(ctx, id, v)
			ch <- result{b, err}
		}()
		return ch
	}
	pending := func(ch chan result) {
		t.Helper()
		h.ping(t)
		time.Sleep(20 * time.Millisecond)
		select {
		case r := <-ch:
			t.Fatalf("WaitBuffer returned early: %+v", r)
		default:
		}
	}

	// まだ開いていないバッファは、buffer/open が届くまで待つ。
	opened := wait(1, 3)
	newer := wait(1, 4)
	pending(opened)
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 3, Text: "a"})
	if r := <-opened; r.err != nil || r.b.Version() != 3 || r.b.Text() != "a" {
		t.Fatalf("WaitBuffer after open = %+v", r)
	}
	pending(newer)
	h.notify(t, ext.MethodBufferChange, ext.BufferChangeParams{Buffer: 1, FromVersion: 3, ToVersion: 5, Edits: []ext.Edit{{Text: "b"}}})
	r := <-newer
	if r.err != nil || r.b.Version() != 5 || r.b.Text() != "ba" {
		t.Fatalf("WaitBuffer after change = %+v", r)
	}
	// 返した Clone は、その後の変更の影響を受けない。
	h.notify(t, ext.MethodBufferChange, ext.BufferChangeParams{Buffer: 1, FromVersion: 5, ToVersion: 6, Edits: []ext.Edit{{Text: "c"}}})
	h.ping(t)
	if r.b.Text() != "ba" {
		t.Errorf("clone changed to %q", r.b.Text())
	}

	// 既に満たしていれば待たない。
	if b, err := s.WaitBuffer(ctx, 1, 2); err != nil || b.Version() != 6 || b.Text() != "cba" {
		t.Errorf("WaitBuffer satisfied = %+v, %v", b, err)
	}

	// 待つ間に閉じたら ErrBufferClosed を返す。
	closed := wait(1, 100)
	pending(closed)
	h.notify(t, ext.MethodBufferClose, ext.BufferCloseParams{Buffer: 1})
	if r := <-closed; !errors.Is(r.err, server.ErrBufferClosed) {
		t.Errorf("WaitBuffer after close = %+v, want ErrBufferClosed", r)
	}

	// 待ち始める前に閉じていても ErrBufferClosed を返す。
	// 本体は、不正な UTF-8 を含むバッファへの editor/open の応答より先に、持っていないバッファの buffer/close を送る（基本設計 6.7）。
	h.notify(t, ext.MethodBufferClose, ext.BufferCloseParams{Buffer: 9})
	h.ping(t)
	if _, err := s.WaitBuffer(ctx, 9, 0); !errors.Is(err, server.ErrBufferClosed) {
		t.Errorf("WaitBuffer after earlier close = %v, want ErrBufferClosed", err)
	}
	// その後に buffer/open が届けば、閉じた記録を消して複製を返す。
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 9, Version: 2, Text: "z"})
	h.ping(t)
	if b, err := s.WaitBuffer(ctx, 9, 2); err != nil || b.Text() != "z" {
		t.Errorf("WaitBuffer after reopen = %+v, %v", b, err)
	}
	reopened := wait(9, 3)
	pending(reopened)
	h.notify(t, ext.MethodBufferChange, ext.BufferChangeParams{Buffer: 9, FromVersion: 2, ToVersion: 3, Edits: []ext.Edit{{Text: "y"}}})
	if r := <-reopened; r.err != nil || r.b.Text() != "yz" {
		t.Errorf("WaitBuffer after reopen and change = %+v", r)
	}

	// ctx が終わったら ctx の誤りを返す。
	short, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	if _, err := s.WaitBuffer(short, 7, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitBuffer with deadline = %v", err)
	}

	// 接続が終わったら ErrNotRunning を返す。
	gone := wait(8, 1)
	pending(gone)
	_ = h.conn.Close()
	if r := <-gone; !errors.Is(r.err, server.ErrNotRunning) {
		t.Errorf("WaitBuffer after the host closed = %+v, want ErrNotRunning", r)
	}
}

// 差分が欠けて送り直しを待っている複製は、次の buffer/open まで WaitBuffer の条件を満たさない。
func TestWaitBufferStale(t *testing.T) {
	s := server.New()
	h := start(t, s)
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, Text: "a"})
	h.notify(t, ext.MethodBufferChange, ext.BufferChangeParams{Buffer: 1, FromVersion: 9, ToVersion: 10})
	if n := h.nextNote(t); n.Method != ext.MethodBufferResync {
		t.Fatalf("got %s", n.Method)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ch := make(chan *server.Buffer, 1)
	go func() {
		b, _ := s.WaitBuffer(ctx, 1, 1)
		ch <- b
	}()
	h.ping(t)
	time.Sleep(20 * time.Millisecond)
	select {
	case b := <-ch:
		t.Fatalf("WaitBuffer returned a stale replica %+v", b)
	default:
	}
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 10, Text: "z"})
	if b := <-ch; b == nil || b.Text() != "z" {
		t.Errorf("WaitBuffer after resync = %+v", b)
	}
}
