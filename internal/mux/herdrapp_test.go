package mux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/mux/muxtest"
)

// TestMain は、このテストの実行ファイルが偽の hc として起動されたら、偽の hc として動いて終わる（muxtest.RunIfHC）。
func TestMain(m *testing.M) {
	muxtest.RunIfHC()
	os.Exit(m.Run())
}

// newHerdrApp は、偽の hc を PATH に置き、端末 t7 にいる herdr app の層を Detect で作る。
func newHerdrApp(t *testing.T) (*HerdrApp, *muxtest.HC) {
	t.Helper()
	hc := muxtest.Install(t)
	m := Detect(envOf(map[string]string{"HC_TERMINAL_ID": "t7", "HC_TERMINAL_SOCKET": "/run/hc.sock", "PATH": os.Getenv("PATH")}))
	a, ok := m.(*HerdrApp)
	if !ok {
		t.Fatalf("Detect = %#v, want *HerdrApp", m)
	}
	return a, hc
}

// herdr app の中のペインの分割は、元の端末に依頼のラベルを書き、mod の結果のラベルを待って新しい端末の番地を返す。
// 依頼には向き、フォーカス、作業ディレクトリと環境変数を包んだコマンドが入り、終わった後には依頼も結果も印も残らない。
//
// req: FR-316
func TestHerdrAppSplitPane(t *testing.T) {
	for _, tc := range []struct {
		dir   Direction
		want  string
		focus bool
	}{
		{Right, "right", true},
		{Left, "left", false},
		{Down, "down", true},
	} {
		t.Run(tc.want, func(t *testing.T) {
			a, hc := newHerdrApp(t)
			hc.ServeMod(t, func(string, muxtest.Request) muxtest.Reply { return muxtest.Reply{Terminal: "t9"} })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			pane, err := a.SplitPane(ctx, SplitOptions{
				Target:    "t8",
				Direction: tc.dir,
				Command:   Command{Args: []string{"/bin/vi-imitation", "-connect", "/s"}, Env: map[string]string{"B": "2", "A": "1"}, Dir: "/work"},
				Focus:     tc.focus,
				Size:      30,
			})
			if err != nil || pane != "t9" {
				t.Fatalf("SplitPane = %q, %v; want t9, nil", pane, err)
			}
			got := hc.Served()
			if len(got) != 1 || got[0].Terminal != "t8" {
				t.Fatalf("依頼 = %+v, want 端末 t8 の依頼 1 つ", got)
			}
			want := muxtest.Request{
				ID: got[0].Request.ID, Op: "split", Direction: tc.want, Focus: tc.focus,
				Argv: []string{"sh", "-c", herdrAppScript, "sh", "/work", a.Bin, "t8", got[0].Request.ID, "env", "A=1", "B=2", "/bin/vi-imitation", "-connect", "/s"},
			}
			if !reflect.DeepEqual(got[0].Request, want) {
				t.Errorf("依頼 = %+v, want %+v", got[0].Request, want)
			}
			if labels := hc.Labels(t, "t8"); len(labels) != 0 {
				t.Errorf("終わった後のラベル = %v, want なし", labels)
			}
		})
	}
}

// mod がペインを割った後にフォーカスだけを移せなかったら、SplitPane は新しい端末の番地と ErrFocusAfterSplit を返す。
// mod の誤りは、その文言を含む誤りになる。
func TestHerdrAppSplitPaneErrors(t *testing.T) {
	a, hc := newHerdrApp(t)
	var mu sync.Mutex
	reply := muxtest.Reply{Terminal: "t9", FocusError: "pane not found"}
	hc.ServeMod(t, func(string, muxtest.Request) muxtest.Reply {
		mu.Lock()
		defer mu.Unlock()
		return reply
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := SplitOptions{Target: "t8", Direction: Left, Command: Command{Args: []string{"true"}}}
	pane, err := a.SplitPane(ctx, opts)
	if pane != "t9" || !errors.Is(err, ErrFocusAfterSplit) {
		t.Errorf("SplitPane = %q, %v; want t9 と ErrFocusAfterSplit", pane, err)
	}

	mu.Lock()
	reply = muxtest.Reply{Error: "pane.split failed"}
	mu.Unlock()
	if pane, err = a.SplitPane(ctx, opts); pane != "" || err == nil || !strings.Contains(err.Error(), "pane.split failed") {
		t.Errorf("SplitPane = %q, %v; want mod の誤り", pane, err)
	}
}

// mod が入っていない（結果が届かない）ときは、期限で ErrHerdrAppNoReply を返し、依頼のラベルを取り下げる。
//
// req: FR-316
func TestHerdrAppNoMod(t *testing.T) {
	a, hc := newHerdrApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := a.SplitPane(ctx, SplitOptions{Target: "t8", Command: Command{Args: []string{"true"}}})
	if !errors.Is(err, ErrHerdrAppNoReply) {
		t.Fatalf("SplitPane = %v, want ErrHerdrAppNoReply", err)
	}
	for k := range hc.Labels(t, "t8") {
		if strings.HasPrefix(k, HerdrAppRequestPrefix) {
			t.Errorf("依頼のラベル %s が残った", k)
		}
	}
}

// 新しいペインのスクリプトは、自分の端末に依頼の番号のラベルを付け（mod が pane.split の戻り値に頼らずに見つける。#444）、
// 作業ディレクトリでコマンドを動かし、終わったら自分の端末を hc kill で終わらせて、同じ終了コードで終わる
// （pane.split の argv が対話のシェルの中で動いても、ペインを残さない。PR #441 のレビューの必須 3）。
// 自分の端末が依頼した元の端末と同じなら終わらせない。
//
// req: FR-316
func TestHerdrAppArgvClosesOwnTerminal(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "hc.log")
	bin := filepath.Join(dir, "hc")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> '"+log+"'\n"), 0o700); err != nil { //nolint:gosec // テストの偽の hc
		t.Fatal(err)
	}
	work := t.TempDir()
	for _, tc := range []struct {
		self string
		want string
	}{
		// 自分の端末に依頼の番号のラベルを付けてから動かし（#444）、終わったら自分の端末を終わらせる。
		{"t9", "label t9 " + HerdrAppSplitLabel + "=5-1\nkill t9\n"},
		{"t8", ""},
		{"", ""},
	} {
		_ = os.Remove(log)
		argv := herdrAppArgv(Command{Args: []string{"sh", "-c", `pwd > out; echo "$X" >> out; exit 3`}, Env: map[string]string{"X": "x"}, Dir: work}, bin, "t8", "5-1")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // テストで包んだコマンドを動かす
		cmd.Env = append(os.Environ(), "HC_TERMINAL_ID="+tc.self)
		err := cmd.Run()
		cancel()
		if ee, ok := errors.AsType[*exec.ExitError](err); !ok || ee.ExitCode() != 3 {
			t.Errorf("HC_TERMINAL_ID=%q: 終わり = %v, want 終了コード 3", tc.self, err)
		}
		if b, _ := os.ReadFile(filepath.Join(work, "out")); string(b) != work+"\nx\n" {
			t.Errorf("HC_TERMINAL_ID=%q: 出力 = %q, want 作業ディレクトリと環境変数", tc.self, b)
		}
		got, _ := os.ReadFile(log)
		if string(got) != tc.want {
			t.Errorf("HC_TERMINAL_ID=%q: hc の呼び出し = %q, want %q", tc.self, got, tc.want)
		}
	}
}

// 待ちを打ち切って取り下げるときに、mod が既に結果を書いていれば、開いたペインを hc kill で閉じ、依頼と結果のラベルを消す
// （PR #441 のレビューの推奨 3）。
func TestHerdrAppWithdrawClosesLatePane(t *testing.T) {
	a, hc := newHerdrApp(t)
	hc.SetLabels(t, "t8", map[string]string{
		HerdrAppRequestPrefix + "1-1": "{}", HerdrAppReplyPrefix + "1-1": `{"id":"1-1","terminal":"t9"}`,
		herdrAppClaimPrefix + "1-1": "other", "x": "1",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.withdraw(ctx, "t8", HerdrAppRequestPrefix+"1-1", HerdrAppReplyPrefix+"1-1")
	if killed := hc.Killed(t); !slices.Equal(killed, []string{"t9"}) {
		t.Errorf("hc kill = %v, want [t9]", killed)
	}
	if labels := hc.Labels(t, "t8"); !reflect.DeepEqual(labels, map[string]string{"x": "1"}) {
		t.Errorf("ラベル = %v, want x だけ", labels)
	}
}

// hc は、Detect のときに環境の PATH から決めた絶対パスで呼び、後で PATH が変わっても引き直さない（PR #441 のレビューの必須 1）。
// PATH に hc が無ければ、どの操作も hc を呼ばずに誤りを返す。
func TestHerdrAppBinFixedAtDetect(t *testing.T) {
	a, hc := newHerdrApp(t)
	if want := filepath.Join(hc.BinDir, "hc"); a.Bin != want {
		t.Fatalf("Bin = %q, want %q", a.Bin, want)
	}
	t.Setenv("PATH", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.ClosePane(ctx, "t9"); err != nil {
		t.Fatalf("ClosePane = %v", err)
	}
	if killed := hc.Killed(t); !slices.Equal(killed, []string{"t9"}) {
		t.Errorf("hc kill = %v, want [t9]（Detect の hc で呼ぶ）", killed)
	}
	none, ok := Detect(envOf(map[string]string{"HC_TERMINAL_ID": "t7", "HC_TERMINAL_SOCKET": "/s", "PATH": t.TempDir()})).(*HerdrApp)
	if !ok || none.Bin != "" {
		t.Fatalf("Detect（PATH に hc なし）= %#v, want Bin が空の HerdrApp", none)
	}
	if err := none.ClosePane(ctx, "t9"); err == nil {
		t.Error("ClosePane（hc なし）= nil, want 誤り")
	}
}

// 待つ間に印のラベルが消えても（mod が別の依頼を片付けて消した）、本体が付け直し、mod が依頼を見つける。
func TestHerdrAppRemarksPending(t *testing.T) {
	a, hc := newHerdrApp(t)
	// 最初の依頼の直後に印だけを消す。偽の係はその後に動かす。
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := a.SplitPane(ctx, SplitOptions{Target: "t8", Command: Command{Args: []string{"true"}}})
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for hc.Labels(t, "t8")[HerdrAppPendingLabel] == "" {
		if time.Now().After(deadline) {
			t.Fatal("依頼の印が付かない")
		}
		time.Sleep(10 * time.Millisecond)
	}
	hc.SetLabels(t, "t8", map[string]string{HerdrAppPendingLabel: "0"})
	hc.ServeMod(t, func(string, muxtest.Request) muxtest.Reply { return muxtest.Reply{Terminal: "t9"} })
	if err := <-done; err != nil {
		t.Errorf("SplitPane = %v, want nil（印を付け直して mod が見つける）", err)
	}
}

// フォーカスの移動と、フォーカスがあるかの問い合わせは mod に頼み、ペインを閉じるのは hc kill で行う。
// Unit は、herdr app が付けるタブのラベル（herdr.tab）から作り、無ければ端末の番地で代える。中央のポップアップは持たない。
//
// req: FR-316
func TestHerdrAppOtherOperations(t *testing.T) {
	a, hc := newHerdrApp(t)
	hc.ServeMod(t, func(_ string, req muxtest.Request) muxtest.Reply {
		return muxtest.Reply{Focused: req.Op == "focused" && req.Terminal == "t3"}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := a.FocusPane(ctx, "t7", "t3"); err != nil {
		t.Fatalf("FocusPane = %v", err)
	}
	if err := a.FocusPane(ctx, "", "t3"); !errors.Is(err, ErrNoTarget) {
		t.Errorf("FocusPane(from が空) = %v, want ErrNoTarget", err)
	}
	if f, err := a.PaneFocused(ctx, "t3"); err != nil || !f {
		t.Errorf("PaneFocused(t3) = %v, %v; want true", f, err)
	}
	if f, err := a.PaneFocused(ctx, "t4"); err != nil || f {
		t.Errorf("PaneFocused(t4) = %v, %v; want false", f, err)
	}
	var ops []string
	for _, s := range hc.Served() {
		ops = append(ops, s.Terminal+": "+s.Request.Op+" "+s.Request.Terminal)
	}
	if want := []string{"t7: focus t3", "t3: focused t3", "t4: focused t4"}; !slices.Equal(ops, want) {
		t.Errorf("依頼 = %v, want %v", ops, want)
	}

	if err := a.ClosePane(ctx, "t9"); err != nil {
		t.Fatalf("ClosePane = %v", err)
	}
	if killed := hc.Killed(t); !slices.Equal(killed, []string{"t9"}) {
		t.Errorf("hc kill = %v, want [t9]", killed)
	}

	if p, err := a.CurrentPane(ctx); err != nil || p != "t7" {
		t.Errorf("CurrentPane = %q, %v; want t7", p, err)
	}
	if u, err := a.Unit(ctx); err != nil || u != "/run/hc.sock:t7" {
		t.Errorf("Unit（タブのラベルなし）= %q, %v; want /run/hc.sock:t7", u, err)
	}
	hc.SetLabels(t, "t7", map[string]string{"herdr.tab": "tb1"})
	if u, err := a.Unit(ctx); err != nil || u != "/run/hc.sock:tb1" {
		t.Errorf("Unit = %q, %v; want /run/hc.sock:tb1", u, err)
	}
	if err := a.OpenPopup(ctx, PopupOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("OpenPopup = %v, want ErrUnsupported", err)
	}
	if err := a.ClosePopup(ctx, "t7"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("ClosePopup = %v, want ErrUnsupported", err)
	}
}
