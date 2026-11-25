//go:build linux

package main

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// この節は、複数のセッションと子のセッション（startDebugging）、capabilities に応じた操作、遅れた応答を捨てる取り決めのテストである
// （基本設計 6.4 の「セッションと状態の分離」と「capabilities に応じた操作」。FR-285〜FR-287。#412）。

var (
	bp5Signs = []ext.Sign{bp2, bp5}
	exec3    = ext.Sign{Line: 3, Kind: ext.SignExecutionPoint}
)

// sessionHost は、アダプター def（adapters の項目）と、ファイルの種類 go の構成 cfg（name、type、request は足す）で拡張機能を初期化し、
// srcPath を開いて、2 行目と 5 行目にブレークポイントを置いた本体の役を返す。
func sessionHost(t *testing.T, def, cfg map[string]any) *host {
	t.Helper()
	c := map[string]any{"name": "parent", "type": "fake", "request": "launch"}
	for k, v := range cfg {
		c[k] = v
	}
	h := newHost(t, mustJSON(t, map[string]any{
		"adapters":       map[string]any{"fake": def},
		"configurations": map[string]any{"go": []any{c}},
	}))
	h.openFT(srcPath, "go")
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdToggleBreakpoint, 5)
	h.waitSigns(bp5Signs...)
	return h
}

// current は、今のセッションを返す。
func (h *host) current() *session {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return h.d.cur
}

// waitCurrent は、今のセッションが s になるまで待つ。
func (h *host) waitCurrent(s *session) {
	h.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if h.current() == s {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("current session = %v, want %d", h.current(), s.id)
}

// waitHalted は、s が止まった（stopped の event を受けて markStopped した）ところまで待つ。
// stopped の event の処理は stackTrace の応答を待つので、その後ろに並ぶ output の event では、止まったことを知れない。
func (h *host) waitHalted(s *session) {
	h.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		h.d.mu.Lock()
		halted := s.halted
		h.d.mu.Unlock()
		if halted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatal("the session did not stop")
}

// outputsUntil は、want のどの文字列も含むまで output/append を読み、つないで返す。
func (h *host) outputsUntil(want ...string) string {
	h.t.Helper()
	var all strings.Builder
	deadline := time.After(testTimeout)
	for {
		done := true
		for _, w := range want {
			if !strings.Contains(all.String(), w) {
				done = false
			}
		}
		if done {
			return all.String()
		}
		select {
		case s := <-h.outputs:
			all.WriteString(s)
		case <-deadline:
			h.t.Fatalf("outputs %q did not arrive; seen = %q", want, all.String())
			return ""
		}
	}
}

// argsLines は、出力 all の "<command> args <JSON>" の行を読み、引数の name から引数への対応にする。
func argsLines(t *testing.T, all, command string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for line := range strings.SplitSeq(all, "\n") {
		if rest, ok := strings.CutPrefix(line, command+" args "); ok {
			var m map[string]any
			if err := json.Unmarshal([]byte(rest), &m); err != nil {
				t.Fatalf("args %q: %v", rest, err)
			}
			name, _ := m["name"].(string)
			out[name] = m
		}
	}
	return out
}

// TestChildSessionStdio は、標準入出力のアダプターが startDebugging を送ると（vscode-js-debug の形）、同じ起動の方法でアダプターを
// もう 1 つ起動して子のセッションを始め、configuration をそのまま送り、受け付けたことを応答し、子のセッションで止まり、
// 続行が子のセッションへ届き、子のセッションだけが終わっても親のセッションは続き、最後に親を終えるとすべてが終わることを確かめる。
//
// req: FR-285
func TestChildSessionStdio(t *testing.T) {
	h := sessionHost(t, fakeDef(transportStdio), map[string]any{"fake": map[string]any{"startDebugging": true}})
	h.mustExec(cmdStart, 0)
	// 親と子のアダプターの出力は、どちらが先に届くかが決まらないので、つないでから調べる。
	all := h.outputsUntil("startDebugging success=true", `"__pendingTargetId"`, "initialize supportsStartDebuggingRequest=true")
	launches := argsLines(t, all, "launch")
	parent, childArgs := launches["parent"], launches["child"]
	if parent == nil || childArgs == nil {
		t.Fatalf("launch args = %v, want the parent's and the child's", launches)
	}
	if childArgs["__pendingTargetId"] != "t1" || childArgs["type"] != "fake" || childArgs["request"] != nil {
		t.Errorf("child launch args = %v, want the configuration as is", childArgs)
	}
	parentAdapter, childAdapter := int(parent["_pid"].(float64)), int(childArgs["_pid"].(float64))
	if parentAdapter == childAdapter {
		t.Error("the child session did not spawn another stdio adapter")
	}
	h.waitSigns(bp2, bp5, exec2)
	h.waitMessage("停止した（breakpoint、child）")
	ss := h.waitSessions(2)
	root, child := ss[0], ss[1]
	if child.parent != root || root.parent != nil {
		t.Fatalf("session tree: child.parent = %p, root = %p", child.parent, root)
	}
	h.waitCurrent(child)

	// 続行は今のセッション（子）へ届く。
	h.mustExec(cmdContinue, 0)
	h.waitSigns(bp2, bp5, exec5)
	// 子のデバッグ対象が終わると、子のセッションだけが終わり、親が今のセッションになる。
	h.mustExec(cmdContinue, 0)
	h.waitSessions(1)
	h.waitCurrent(root)
	waitGone(t, "child adapter", childAdapter)
	if gone(parentAdapter) {
		t.Error("the parent adapter ended with the child session")
	}
	h.stopAndWait()
	h.waitMessage("デバッグを終了した")
	waitGone(t, "parent adapter", parentAdapter)
}

// TestChildSessionServerPort は、serverPort のアダプターの子のセッションが、同じアダプターの同じポートへ新しく繋ぎ、
// 子が繋いでいる間はアダプターを止めず、親を終えると子を先に終えてから、アダプターを止めることを確かめる。
//
// req: FR-285, FR-284
func TestChildSessionServerPort(t *testing.T) {
	h := sessionHost(t, fakeDef(transportServerPort, "-listen", "127.0.0.1:${port}"), map[string]any{"fake": map[string]any{"startDebugging": true}})
	h.mustExec(cmdStart, 0)
	launches := argsLines(t, h.outputsUntil(`"__pendingTargetId"`, `"name":"parent"`), "launch")
	parentAdapter := int(launches["parent"]["_pid"].(float64))
	if a := int(launches["child"]["_pid"].(float64)); a != parentAdapter {
		t.Errorf("child adapter = %d, want the parent's %d (the same server)", a, parentAdapter)
	}
	h.waitSigns(bp2, bp5, exec2)
	ss := h.waitSessions(2)
	if ss[0].getProc() != ss[1].getProc() {
		t.Error("the child session does not share the adapter process")
	}
	h.mustExec(cmdStop, 0)
	seen := h.waitOutput("disconnect terminateDebuggee=true after launch")
	seen = append(seen, h.waitOutput("disconnect terminateDebuggee=true after launch")...)
	h.waitEnded()
	waitGone(t, "adapter", parentAdapter)
	if n := strings.Count(strings.Join(seen, ""), "disconnect terminateDebuggee=true"); n != 2 {
		t.Errorf("disconnect count = %d, want 2 (child and parent): %q", n, seen)
	}
}

// TestChildSessionConnectFrom は、子のセッションの configuration がアダプターの connectFrom のキー（debugpy の connect）を持てば、
// アダプターを起動せずにそこへ直に繋ぎ、startDebugging の request（attach）を送ることを確かめる。
// あわせて、親と子のどちらも止まっているときに実行位置の印をセッションごとに付け、今のセッションを選び直すと続行がそのセッションへ届き、
// ほかのセッションの印を消さないこと、終えるときは子を親より先に、親の request（launch）の手順で終えることを確かめる。
//
// req: FR-285, FR-284
func TestChildSessionConnectFrom(t *testing.T) {
	def := fakeDef(transportStdio)
	def["connectFrom"] = "connect"
	h := sessionHost(t, def, map[string]any{"fake": map[string]any{
		"startDebugging": true, "childListen": true, "parentRuns": true, "childRequest": "attach",
		// 子のデバッグ対象は、置いたブレークポイントの次の行で止まる。
		"childConfig": map[string]any{"name": "Subprocess 1", "type": "fake", "fake": map[string]any{"moveBy": 1}},
	}})
	h.mustExec(cmdStart, 0)
	all := h.outputsUntil("attach args ", `launch args {`)
	parentArgs, args := argsLines(t, all, "launch")["parent"], argsLines(t, all, "attach")["Subprocess 1"]
	if args == nil || args["connect"] == nil || parentArgs == nil {
		t.Fatalf("args: parent %v, child %v", parentArgs, args)
	}
	// 子は親のアダプターのプロセスの中で話すので、新しいアダプターは起動していない。
	if a, p := args["_pid"], parentArgs["_pid"]; a != p {
		t.Errorf("child adapter = %v, want the parent's process %v (connected directly)", a, p)
	}
	// 親は 2 行目、子は 3 行目で止まり、どちらの印も付く。ブレークポイントの印は、先に始めた親の応答の行に置く。
	h.waitSigns(bp2, bp5, exec2, exec3)
	ss := h.waitSessions(2)
	root, child := ss[0], ss[1]
	if root.owned() == child.owned() {
		t.Errorf("owned: root %v, child %v; want the child connected without spawning", root.owned(), child.owned())
	}

	// 親を今のセッションに選び直すと、続行は親へ届き、子の印は残る。
	if !h.d.selectSession(root) {
		t.Fatal("selectSession(root) = false")
	}
	h.mustExec(cmdContinue, 0)
	h.waitSigns(bp2, bp5, exec5, exec3)
	// 子を選び直して続けると、子だけが進む。
	if !h.d.selectSession(child) {
		t.Fatal("selectSession(child) = false")
	}
	h.mustExec(cmdContinue, 0)
	h.waitSigns(bp2, bp5, exec5, exec6)

	h.mustExec(cmdStop, 0)
	seen := h.waitOutput("disconnect terminateDebuggee=true after launch")
	out := strings.Join(seen, "")
	c, p := strings.Index(out, "disconnect terminateDebuggee=true after attach"), strings.Index(out, "after launch")
	if c < 0 || c > p {
		t.Errorf("the child session (attach, ended with the parent's launch procedure) was not disconnected before the parent: %q", seen)
	}
	h.waitEnded()
	h.waitSigns(bp2, bp5)
}

// connectFromChild は、親と、親が待ち受けて直に繋がせる子（debugpy の子のプロセスの形）の 2 つのセッションを作る本体の役を返す。
// parent と child は、親と子のテスト用のアダプターの振る舞い（fake の項目）に足すものである。
func connectFromChild(t *testing.T, parent, child map[string]any) *host {
	t.Helper()
	def := fakeDef(transportStdio)
	def["connectFrom"] = "connect"
	childFake := map[string]any{}
	for k, v := range child {
		childFake[k] = v
	}
	fake := map[string]any{
		"startDebugging": true, "childListen": true, "parentRuns": true, "childRequest": "attach",
		"childConfig": map[string]any{"name": "Subprocess 1", "type": "fake", "fake": childFake},
	}
	for k, v := range parent {
		fake[k] = v
	}
	return sessionHost(t, def, map[string]any{"fake": fake})
}

// TestContinueStoppedSession は、2 つのセッションが止まっているときに、後から止まったセッションが今のセッションを奪わず、
// 今のセッションを続けて動き出した後の続行が、止まっているもう一方のセッションへ届くことを確かめる
// （PR #414 のレビューの必須 1。debugpy の Popen の子と親が同時に止まる場面。nvim-dap の first_stopped_session と同じ扱い）。
//
// req: FR-285
func TestContinueStoppedSession(t *testing.T) {
	h := connectFromChild(t, map[string]any{"runForever": true}, map[string]any{"runForever": true})
	var mu sync.Mutex
	var sels []selection
	h.d.mu.Lock()
	h.d.onSelect = func(sel selection) {
		mu.Lock()
		defer mu.Unlock()
		sels = append(sels, sel)
	}
	h.d.mu.Unlock()
	h.mustExec(cmdStart, 0)
	ss := h.waitSessions(2)
	h.waitHalted(ss[0])
	h.waitHalted(ss[1])
	first := h.current()
	mu.Lock()
	for _, sel := range sels {
		if sel.session != first {
			t.Errorf("the current session moved to session %d though session %d was stopped", sel.session.id, first.id)
		}
	}
	mu.Unlock()
	other := ss[0]
	if first == ss[0] {
		other = ss[1]
	}

	// 今のセッションを続けると、それは動き続ける。もう一度続けると、止まっているもう一方へ届く。
	h.mustExec(cmdContinue, 0)
	h.waitOutput("continued after " + first.spec.request)
	h.mustExec(cmdContinue, 0)
	h.waitOutput("continued after " + other.spec.request)
	h.waitMessage("止まっているセッション " + other.label())
	if h.current() != other {
		t.Errorf("current session = %d, want %d", h.current().id, other.id)
	}
	h.stopAndWait()
}

// TestReplayOtherSessionStops は、再生中（sync）のステップ実行が今のセッションの停止を待つ間に、別のセッションが止まっても、
// カーソルを動かさず（editor/open を送らず）、今のセッションを替えず、待っていたセッションの停止で応答することを確かめる。
//
// req: FR-19, FR-285
func TestReplayOtherSessionStops(t *testing.T) {
	h := connectFromChild(t, map[string]any{"stopDelayMs": 1000}, map[string]any{"firstStopDelayMs": 400})
	h.mustExec(cmdStart, 0)
	ss := h.waitSessions(2)
	root, child := ss[0], ss[1]
	h.waitHalted(root)
	h.waitCurrent(root)
	h.drainOpens()
	res := h.replay(cmdNext, 0)
	if !res.OK || res.Open == nil || res.Open.Position == nil || res.Open.Position.Line != 3 {
		t.Fatalf("next (sync) = %+v (open %+v), want the parent's stop at line 3", res, res.Open)
	}
	h.d.mu.Lock()
	childHalted := child.halted
	h.d.mu.Unlock()
	if !childHalted {
		t.Fatal("the child did not stop during the replay")
	}
	h.noOpen("the child's stop during the replay")
	if h.current() != root {
		t.Errorf("current session = %d, want the parent", h.current().id)
	}
	h.stopAndWait()
}

// TestPauseBeforeStop は、一度も止まる前の一時停止を、スレッドを持つセッション（vscode-js-debug の子）へ送り、
// どのセッションにもスレッドが無ければ、そのことを知らせることを確かめる（PR #414 のレビューの推奨 1）。
//
// req: FR-285
func TestPauseBeforeStop(t *testing.T) {
	t.Run("child", func(t *testing.T) {
		h := sessionHost(t, fakeDef(transportStdio), map[string]any{"fake": map[string]any{
			"startDebugging": true, "noThreads": true,
			"childConfig": map[string]any{"name": "child", "type": "fake", "fake": map[string]any{"idle": true}},
		}})
		h.mustExec(cmdStart, 0)
		ss := h.waitSessions(2)
		h.waitOutput("startDebugging success=true")
		h.d.mu.Lock()
		configured := false
		for !configured {
			configured = ss[1].configured
			h.d.mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			h.d.mu.Lock()
		}
		h.d.mu.Unlock()
		h.mustExec(ext.OpDebugPause, 0)
		h.waitMessage("停止した（pause")
		h.waitHalted(ss[1])
		h.stopAndWait()
	})
	t.Run("no threads", func(t *testing.T) {
		h := sessionHost(t, fakeDef(transportStdio), map[string]any{"fake": map[string]any{"noThreads": true, "idle": true}})
		h.mustExec(cmdStart, 0)
		h.waitMessage("デバッグを開始した")
		h.mustExec(ext.OpDebugPause, 0)
		h.waitMessage("一時停止できるスレッドが無い")
		h.stopAndWait()
	})
}

// TestChildSessionEndsWithParent は、親のアダプターが terminated を送って終わったときも、子のセッションを終えることを確かめる。
//
// req: FR-285
func TestChildSessionEndsWithParent(t *testing.T) {
	h := sessionHost(t, fakeDef(transportStdio), map[string]any{"fake": map[string]any{"startDebugging": true, "parentRuns": true}})
	h.mustExec(cmdStart, 0)
	launches := argsLines(t, h.outputsUntil(`"__pendingTargetId"`, `"name":"parent"`), "launch")
	parentAdapter, childAdapter := int(launches["parent"]["_pid"].(float64)), int(launches["child"]["_pid"].(float64))
	ss := h.waitSessions(2)
	h.waitHalted(ss[0])
	h.waitHalted(ss[1])
	h.waitSigns(bp2, bp5, exec2)
	// 親を選んで最後まで続けると、親のアダプターは exited と terminated を送る。止まっている子も終わる。
	h.d.selectSession(ss[0])
	h.mustExec(cmdContinue, 0)
	h.waitSigns(bp2, bp5, exec5, exec2)
	h.mustExec(cmdContinue, 0)
	h.waitEnded()
	waitGone(t, "parent adapter", parentAdapter)
	waitGone(t, "child adapter", childAdapter)
}

// TestStaleStackTrace は、止まった後に送った stackTrace の応答が、続けた後か、選び直した後に届いたら、表示に使わずに捨てることを確かめる。
//
// req: FR-287
func TestStaleStackTrace(t *testing.T) {
	t.Run("continue", func(t *testing.T) {
		h := sessionHost(t, fakeDef(transportStdio), map[string]any{"fake": map[string]any{"stackTraceDelayMs": 500}})
		h.mustExec(cmdStart, 0)
		h.waitHalted(h.waitSessions(1)[0])
		h.mustExec(cmdContinue, 0)
		deadline := time.After(testTimeout)
		for {
			select {
			case p := <-h.signs:
				if slices.Contains(p.Signs, exec2) {
					t.Fatalf("sign/set %v: the stop before continuing was shown from a stale stackTrace", p.Signs)
				}
				if slices.Equal(p.Signs, []ext.Sign{bp2, bp5, exec5}) {
					return
				}
			case <-deadline:
				t.Fatal("the stop after continuing was not shown")
			}
		}
	})
	t.Run("select", func(t *testing.T) {
		h := sessionHost(t, fakeDef(transportStdio), map[string]any{"fake": map[string]any{"stackTraceDelayMs": 300}})
		h.mustExec(cmdStart, 0)
		s := h.waitSessions(1)[0]
		h.waitHalted(s)
		var mu sync.Mutex
		var sels []selection
		h.d.mu.Lock()
		h.d.onSelect = func(sel selection) {
			mu.Lock()
			defer mu.Unlock()
			sels = append(sels, sel)
		}
		h.d.mu.Unlock()
		if !h.d.selectThread(s, 1) {
			t.Fatal("selectThread = false")
		}
		time.Sleep(600 * time.Millisecond)
		mu.Lock()
		got := slices.Clone(sels)
		mu.Unlock()
		if len(got) == 0 || got[len(got)-1].session != s || got[len(got)-1].thread != 1 {
			t.Errorf("selections = %+v, want the last one for thread 1", got)
		}
		for {
			select {
			case p := <-h.signs:
				if slices.Contains(p.Signs, exec2) {
					t.Fatalf("sign/set %v: shown from a stackTrace sent before the selection changed", p.Signs)
				}
				continue
			default:
			}
			break
		}
		if sel := h.d.currentSelection(); sel.hasFrame {
			t.Errorf("selection = %+v, want no frame from the stale stackTrace", sel)
		}
		// フレームを選ぶと、選択に出て、世代が進む。
		gen := s.gen.Load()
		if !h.d.selectFrame(s, 1, 1000) {
			t.Fatal("selectFrame = false")
		}
		if sel := h.d.currentSelection(); !sel.hasFrame || sel.frame != 1000 || sel.thread != 1 || s.gen.Load() == gen {
			t.Errorf("selection after selectFrame = %+v (gen %d -> %d)", sel, gen, s.gen.Load())
		}
	})
}

// TestLateVerification は、setBreakpoints の応答で検証できなかったブレークポイントを、後から届いた breakpoint の event（reason が changed）と、
// 止まったときの stopped の hitBreakpointIds で、検証できたものにすることを確かめる。親のセッションの応答が検証できないままでも、
// 子のセッションが検証できたら検証できた印にする。スレッドの ID が 0 のアダプターでも続行できる（vscode-js-debug の形）。
//
// req: FR-285
func TestLateVerification(t *testing.T) {
	bp5u := ext.Sign{Line: 5, Kind: ext.SignBreakpointUnverified}
	t.Run("event", func(t *testing.T) {
		h := sessionHost(t, fakeDef(transportStdio), map[string]any{"fake": map[string]any{
			"startDebugging": true, "neverVerify": true,
			"childConfig": map[string]any{"name": "child", "type": "fake", "fake": map[string]any{"verifyLater": true, "threadZero": true}},
		}})
		h.mustExec(cmdStart, 0)
		h.waitSigns(bp2, bp5, exec2)
		h.mustExec(cmdContinue, 0)
		h.waitSigns(bp2, bp5, exec5)
		h.stopAndWait()
	})
	t.Run("hit", func(t *testing.T) {
		h := sessionHost(t, fakeDef(transportStdio), map[string]any{"fake": map[string]any{"neverVerify": true}})
		h.mustExec(cmdStart, 0)
		h.waitSigns(bp2, bp5u, exec2)
		h.stopAndWait()
	})
}

// TestRestartLateFailure は、restart に成功の応答を返した後に、同じ request へ失敗の応答が届いたら（dlv のビルドの失敗）、
// 理由を知らせ、続けて届く terminated の後の終わりの知らせにも理由を添えることを確かめる（PR #414 のレビューの推奨 3 と再レビューの推奨 B）。
//
// req: FR-286
func TestRestartLateFailure(t *testing.T) {
	h := newHost(t, fakeSettings(t, []string{"-restart", "-restart-fail"}, nil))
	h.open(1, srcPath, srcText)
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, exec2)
	h.mustExec(ext.OpDebugRestart, 0)
	m := h.waitMessage("restart に失敗した")
	if m.Level != ext.MessageError || !strings.Contains(m.Text, "build error in main.go") {
		t.Errorf("message = %+v", m)
	}
	m = h.waitMessage("デバッグを終了した")
	if m.Level != ext.MessageError || !strings.Contains(m.Text, "build error in main.go") {
		t.Errorf("end message = %+v, want the reason of the failed restart", m)
	}
	h.waitEnded()
}

// TestCapabilities は、アダプターが対応していない操作を、送らずに知らせるか、代わりの手順にすることを確かめる。
// 条件付きのブレークポイントは、置かずに（送らずに）知らせ、条件の無いものだけを置く。restart に対応していれば、終えずに restart を送る。
//
// req: FR-286
func TestCapabilities(t *testing.T) {
	t.Run("conditional", func(t *testing.T) {
		h := newHost(t, fakeSettings(t, []string{"-no-conditional"}, nil))
		h.open(1, srcPath, srcText)
		if res := h.invoke(1, ext.OpBreakpointConditional, 5, `{"text":"i > 3"}`); !res.OK {
			t.Fatal("conditional: ok = false")
		}
		h.mustExec(cmdToggleBreakpoint, 2)
		h.mustExec(cmdStart, 0)
		// 条件付きのものは置かず、条件の無いものだけを置く。置かなかったものは検証できなかった印にする。
		seen := h.waitOutput("setBreakpoints main.go [3]")
		for _, s := range seen {
			if strings.Contains(s, "condition ") {
				t.Errorf("the conditional breakpoint was sent to an adapter without supportsConditionalBreakpoints: %q", s)
			}
		}
		m := h.waitMessage("条件付きのブレークポイントを置かない")
		if m.Level != ext.MessageError {
			t.Errorf("message = %+v", m)
		}
		h.waitSigns(bp2, ext.Sign{Line: 5, Kind: ext.SignBreakpointUnverified}, exec2)
	})
	t.Run("restart", func(t *testing.T) {
		h := newHost(t, fakeSettings(t, []string{"-restart"}, map[string]any{"child": true}))
		h.open(1, srcPath, srcText)
		h.mustExec(cmdToggleBreakpoint, 2)
		h.mustExec(cmdToggleBreakpoint, 5)
		h.mustExec(cmdStart, 0)
		adapter := h.waitPID("adapter")
		h.waitSigns(bp2, bp5, exec2)
		h.mustExec(cmdContinue, 0)
		h.waitSigns(bp2, bp5, exec5)
		h.mustExec(ext.OpDebugRestart, 0)
		seen := h.waitOutput("restart args ")
		if all := strings.Join(seen, ""); strings.Contains(all, "disconnect") || !strings.Contains(all, `"arguments":{`) {
			t.Errorf("restart: %q, want restart with the configuration and without disconnect", seen)
		}
		h.waitSigns(bp2, bp5, exec2)
		h.waitMessage("デバッグを始め直した")
		if gone(adapter) {
			t.Error("the adapter was restarted though it supports restart")
		}
	})
}

// TestChildSessionReplay は、マクロの再生中（sync）の開始が、子のセッションの最初の停止まで応答を遅らせ、
// 続くステップ実行が今のセッション（子）の次の停止まで応答を遅らせることを確かめる（基本設計 5.5 の完了の決め方を、複数のセッションで保つ）。
//
// req: FR-19, FR-285
func TestChildSessionReplay(t *testing.T) {
	h := sessionHost(t, fakeDef(transportStdio), map[string]any{"fake": map[string]any{"startDebugging": true}})
	h.mustExec(cmdStart, 0)
	h.waitSigns(bp2, bp5, exec2)
	h.stopAndWait()
	h.drainOpens()

	res := h.replay(ext.OpDebugRunLast, 0)
	if !res.OK || res.Open == nil || res.Open.Position == nil || res.Open.Position.Line != 2 {
		t.Fatalf("runLast (sync) = %+v, want ok with open at the child's stop (line 2)", res)
	}
	ss := h.waitSessions(2)
	if h.current() != ss[1] {
		t.Error("the current session is not the child")
	}
	res = h.replay(cmdNext, 0)
	if !res.OK || res.Open == nil || res.Open.Position == nil || res.Open.Position.Line != 3 {
		t.Fatalf("next (sync) = %+v, want ok with open at line 3", res)
	}
	if res := h.replay(cmdStop, 0); !res.OK {
		t.Errorf("terminate (sync) = %+v", res)
	}
	h.d.mu.Lock()
	n := len(h.d.sessions)
	h.d.mu.Unlock()
	if n != 0 {
		t.Errorf("terminate (sync) replied with %d sessions left", n)
	}
	h.noOpen("replay")
}

// TestExtensionKilledWithChildSessions は、子のセッションが動いている間に拡張機能のプロセスが SIGKILL で終わっても、
// 親と子のどちらのアダプターとデバッグ対象も残らないことを確かめる（見張り。guard_linux.go）。
//
// req: FR-31, FR-285
func TestExtensionKilledWithChildSessions(t *testing.T) {
	h := newHostState(t)
	cmd := exec.Command(extBinary)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h.conn = rpc.NewConn(stdout, stdin, rpc.HandlerFuncs{Notification: h.notification, Request: h.request})
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = h.conn.Close()
		_ = cmd.Wait()
	})
	h.initialize(mustJSON(t, map[string]any{
		"adapters": map[string]any{"fake": fakeDef(transportStdio)},
		"configurations": map[string]any{"go": []any{map[string]any{
			"name": "parent", "type": "fake", "request": "launch",
			"fake": map[string]any{
				"startDebugging": true, "child": true,
				"childConfig": map[string]any{"name": "child", "type": "fake", "fake": map[string]any{"child": true}},
			},
		}}},
	}))
	h.openFT(srcPath, "go")
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	var pids []int
	for _, what := range []string{"adapter", "child", "adapter", "child"} {
		pids = append(pids, h.waitPID(what))
	}
	h.waitSigns(bp2, exec2)
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		waitGone(t, "adapter or debuggee", pid)
	}
}
