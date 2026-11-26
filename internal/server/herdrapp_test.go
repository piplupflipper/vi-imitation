package server

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/editor"
	"github.com/piplupflipper/vi-imitation/internal/mux"
	"github.com/piplupflipper/vi-imitation/internal/mux/muxtest"
	"github.com/piplupflipper/vi-imitation/internal/protocol"
)

// TestMain は、このテストの実行ファイルが偽の hc として起動されたら、偽の hc として動いて終わる（muxtest.RunIfHC）。
func TestMain(m *testing.M) {
	muxtest.RunIfHC()
	os.Exit(m.Run())
}

// inHerdrApp は、環境変数を herdr app の端末 t7 の中にし、偽の hc を PATH に置く（#440）。
func inHerdrApp(t *testing.T) *muxtest.HC {
	t.Helper()
	hc := muxtest.Install(t)
	for _, name := range []string{"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_PANE_ID", "HERDR_SOCKET_PATH"} {
		t.Setenv(name, "")
	}
	t.Setenv("HC_TERMINAL_ID", "t7")
	t.Setenv("HC_TERMINAL_SOCKET", filepath.Join(t.TempDir(), "hc.sock"))
	return hc
}

// splitArgv は、mod への split の依頼の argv（mux の herdrAppArgv の形）から、起動するコマンドの環境変数と引数を取り出す。
func splitArgv(t *testing.T, argv []string) (env map[string]string, args []string) {
	t.Helper()
	i := slices.Index(argv, "env")
	if i < 0 {
		t.Fatalf("argv = %q, want env を含む", argv)
	}
	env = map[string]string{}
	for i++; i < len(argv); i++ {
		k, v, ok := strings.Cut(argv[i], "=")
		if !ok {
			break
		}
		env[k] = v
	}
	return env, argv[i:]
}

// modClients は、偽の係が新しいペインの client の代わりに繋いだ接続である。テストの終わりに閉じる。
type modClients struct {
	mu    sync.Mutex
	conns []protocol.ClientConn
}

// connect は、split の依頼の argv の -connect のソケットへ、ペイン pane の編集の client として繋ぐ（新しいペインで起動した本体の代わり）。
// 引数の -- の後のファイルと、環境変数の見比べの印（editor.EnvSplit）を Hello で知らせる。
func (m *modClients) connect(t *testing.T, argv []string, pane string) {
	env, args := splitArgv(t, argv)
	i := slices.Index(args, "-connect")
	if i < 0 || i+1 >= len(args) {
		t.Errorf("起動するコマンド = %q, want -connect <ソケット>", args)
		return
	}
	hello := protocol.Hello{Kind: protocol.KindEdit, Width: 200, Height: 10, Pane: pane, Env: []string{editor.EnvSplit + "=" + env[editor.EnvSplit]}}
	if j := slices.Index(args, "--"); j >= 0 {
		hello.Files = args[j+1:]
	}
	c, err := protocol.Dial(args[i+1])
	if err != nil {
		t.Errorf("Dial: %v", err)
		return
	}
	m.mu.Lock()
	m.conns = append(m.conns, c)
	m.mu.Unlock()
	if err := c.Send(hello); err != nil {
		t.Errorf("Send: %v", err)
		return
	}
	go func() {
		for {
			if _, err := c.Recv(); err != nil {
				return
			}
		}
	}()
}

func (m *modClients) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns {
		_ = c.Close()
	}
}

// screens は、c に届いた画面を文字列にして送るチャネルを返す。
func screens(c protocol.ClientConn) <-chan string {
	out := make(chan string, 64)
	go func() {
		defer close(out)
		for {
			m, err := c.Recv()
			if err != nil {
				return
			}
			if d, ok := m.(protocol.Draw); ok {
				select {
				case out <- d.Screen.Grid.String():
				default:
				}
			}
		}
	}()
	return out
}

// waitScreenText は、want を含む画面が届くまで待つ。
func waitScreenText(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	var last string
	for {
		select {
		case s, ok := <-ch:
			if !ok {
				t.Fatalf("接続が閉じた。最後の画面:\n%s", last)
			}
			last = s
			if strings.Contains(s, want) {
				return
			}
		case <-timeout:
			t.Fatalf("%q を含む画面が届かない。最後の画面:\n%s", want, last)
		}
	}
}

// startHerdrAppServer は、herdr app の中で server を起動し、ペイン t7 の編集の client として a.txt を開いて繋ぐ。
func startHerdrAppServer(t *testing.T) (protocol.ClientConn, <-chan string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"a.txt": "one\ntwo\n", "b.txt": "one\nTWO\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv := newServer(t)
	sock := filepath.Join(privateDir(t), "s", SocketName)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, ln, nil)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	c, err := protocol.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	hello := protocol.Hello{Kind: protocol.KindEdit, Width: 200, Height: 10, Pane: "t7", WorkDir: dir, Files: []string{filepath.Join(dir, "a.txt")}}
	if err := c.Send(hello); err != nil {
		t.Fatal(err)
	}
	ch := screens(c)
	waitScreenText(t, ch, "a.txt")
	return c, ch
}

// herdr app の中の :diffsplit は、元の client の端末（t7）に右へ割る依頼のラベルを書き、mod の結果を受け取る。
// 新しいペインで起動した client が繋ぐと、見比べを始める（:diffthis が、既に見比べていると答える）。
//
// req: FR-316
func TestDiffSplitHerdrApp(t *testing.T) {
	hc := inHerdrApp(t)
	clients := &modClients{}
	t.Cleanup(clients.close)
	hc.ServeMod(t, func(_ string, req muxtest.Request) muxtest.Reply {
		if req.Op != "split" {
			return muxtest.Reply{Error: "unexpected op " + req.Op}
		}
		clients.connect(t, req.Argv, "t9")
		return muxtest.Reply{Terminal: "t9"}
	})
	c, ch := startHerdrAppServer(t)
	sendKeys(t, c, ":diffsplit b.txt<CR>")
	waitScreenText(t, ch, "b.txt を別のペインで開いて見比べます")
	// 見比べが始まると、:diffthis は既に見比べていると答える。始まる前なら相手を待つ。
	deadline := time.Now().Add(10 * time.Second)
	for !slices.ContainsFunc(hc.Served(), func(s muxtest.Served) bool { return s.Request.Op == "split" }) {
		if time.Now().After(deadline) {
			t.Fatal("mod に依頼が届かない")
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitDiffing(t, c, ch, "b.txt と既に見比べています")
	// 係は結果を書く前に client を繋ぐので、見比べが始まった時点では、本体はまだ結果を待っている。
	// 本体が結果を読んでラベルを片付ける（待ちが終わる）まで待ってから終わる（PR #441 のレビューの必須 1）。
	waitNoLabels(t, hc, "t7")

	served := hc.Served()
	if len(served) != 1 || served[0].Terminal != "t7" || served[0].Request.Direction != "right" || !served[0].Request.Focus {
		t.Errorf("mod への依頼 = %+v, want 端末 t7 に右へ割ってフォーカスを移す依頼 1 つ", served)
	}
}

// waitNoLabels は、端末 terminal の vi-imitation のラベル（依頼、結果、印）が無くなるまで待つ。
func waitNoLabels(t *testing.T, hc *muxtest.HC, terminal string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		left := false
		for k := range hc.Labels(t, terminal) {
			if strings.HasPrefix(k, "vi-imitation.") {
				left = true
			}
		}
		if !left {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("端末 %s のラベルが残った: %v", terminal, hc.Labels(t, terminal))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// trapHC は、呼ばれたら引数を記録して失敗する hc を PATH の先頭に置き、記録のファイルのパスを返す。
// テストの層が、偽の hc（muxtest.Install。この後に置く）を使い終えた後に、PATH の hc（打った人の本物の hc に当たる）を呼ばないことを確かめる。
func trapHC(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "called")
	script := "#!/bin/sh\necho \"$@\" >> '" + log + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "hc"), []byte(script), 0o700); err != nil { //nolint:gosec // テストの罠の hc
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// herdr app の中の見比べのテストが終わった後に、server と層が、PATH の hc（打った人の本物の hc に当たる罠）を一度も呼ばない
// （PR #441 のレビューの必須 1）。hc は層を作ったときの偽の hc の絶対パスで呼び、待ちは server の Close で打ち切る。
// mod の無いときの待ちの打ち切り（期限と Close）の後も同じである。
func TestHerdrAppNeverCallsPathHC(t *testing.T) {
	log := trapHC(t)
	t.Run("diffsplit", TestDiffSplitHerdrApp)
	t.Run("nomod", TestDiffSplitHerdrAppNoMod)
	t.Run("closed", func(t *testing.T) {
		// 結果を待つ途中で server を閉じる。待ちは打ち切られ、片付けは偽の hc で行う。
		inHerdrApp(t)
		c, ch := startHerdrAppServer(t)
		sendKeys(t, c, ":diffsplit b.txt<CR>")
		waitScreenText(t, ch, "b.txt を別のペインで開いて見比べます")
	})
	// テストの片付けで PATH は罠を先頭にした値に戻る。残った待ちがあれば、ここで罠を呼ぶ。
	time.Sleep(1500 * time.Millisecond)
	if b, err := os.ReadFile(log); err == nil {
		t.Errorf("PATH の hc が呼ばれた:\n%s", b)
	}
}

// waitDiffing は、:diffthis を打ち、want（既に見比べている知らせ）が出るまで繰り返す。
func waitDiffing(t *testing.T, c protocol.ClientConn, ch <-chan string, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		sendKeys(t, c, ":diffthis<CR>")
		timeout := time.After(500 * time.Millisecond)
	recv:
		for {
			select {
			case s, ok := <-ch:
				if !ok {
					t.Fatal("接続が閉じた")
				}
				if strings.Contains(s, want) {
					return
				}
			case <-timeout:
				break recv
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q が出ない（見比べが始まらない）", want)
		}
	}
}

// herdr app の中で diff/start のペインを開く（Config.SplitPane。left は比べる元の版を左に開く。#437）と、
// 左なら左へ割ってフォーカスを元のペインに残す依頼を、右なら右へ割ってフォーカスを移す依頼を、元の client の端末に書く。
// 新しいペインのコマンドは、この server に -connect で繋ぎ、見比べの印を環境変数で受け取る。
//
// req: FR-316
func TestSplitPaneHerdrApp(t *testing.T) {
	for _, tc := range []struct {
		left      bool
		direction string
		focus     bool
	}{
		{true, "left", false},
		{false, "right", true},
	} {
		t.Run(tc.direction, func(t *testing.T) {
			hc := inHerdrApp(t)
			hc.ServeMod(t, func(string, muxtest.Request) muxtest.Reply { return muxtest.Reply{Terminal: "t9"} })
			s := &Server{log: slog.New(slog.DiscardHandler)}
			sock := filepath.Join(t.TempDir(), "server.sock")
			s.sock.Store(sock)
			dir := t.TempDir()
			if err := s.splitPane("t8", "", dir, "5", tc.left); err != nil {
				t.Fatalf("splitPane = %v", err)
			}
			served := hc.Served()
			if len(served) != 1 {
				t.Fatalf("mod への依頼 = %+v, want 1 つ", served)
			}
			req := served[0].Request
			if served[0].Terminal != "t8" || req.Op != "split" || req.Direction != tc.direction || req.Focus != tc.focus {
				t.Errorf("mod への依頼 = %+v, want 端末 t8 に %s へ割る依頼（focus %v）", served[0], tc.direction, tc.focus)
			}
			env, args := splitArgv(t, req.Argv)
			if env[editor.EnvSplit] != "5" || !slices.Contains(args, "-connect") || !slices.Contains(args, sock) || !slices.Contains(req.Argv, dir) {
				t.Errorf("argv = %q, want 印 5、-connect %s、作業ディレクトリ %s", req.Argv, sock, dir)
			}
		})
	}
}

// herdr app の中のペインの分割（Config.SplitPane。diff/start の Wait はこれが戻るのを待つ。#447）は、mod の結果
// （入れ替えとフォーカスの移し直しを終えた後に書く結果のラベル）が届いてから戻る。偽の係が結果を書くのを遅らせると、
// splitPane もその後まで戻らない。
//
// req: FR-317
func TestSplitPaneHerdrAppReturnsAfterModResult(t *testing.T) {
	hc := inHerdrApp(t)
	var mu sync.Mutex
	var replied time.Time
	hc.ServeMod(t, func(string, muxtest.Request) muxtest.Reply {
		// mod の pane.split、pane.swap、pane.focus に掛かる時間の代わり。
		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		replied = time.Now()
		mu.Unlock()
		return muxtest.Reply{Terminal: "t9"}
	})
	s := &Server{log: slog.New(slog.DiscardHandler)}
	s.sock.Store(filepath.Join(t.TempDir(), "server.sock"))
	if err := s.splitPane("t8", "", t.TempDir(), "5", true); err != nil {
		t.Fatalf("splitPane = %v", err)
	}
	returned := time.Now()
	mu.Lock()
	defer mu.Unlock()
	if replied.IsZero() || returned.Before(replied) {
		t.Errorf("splitPane が mod の結果（%v）より前（%v）に戻った", replied, returned)
	}
}

// herdr app の mod の run("pane.split") が新しいペインと端末を返さなくても（#444）、新しいペインの端末で動く包んだスクリプトが、
// 自分の端末に依頼の番号のラベルを付けるので、mod はそのラベルから新しい端末を見つけて結果に書ける。左と右のどちらでも同じである。
// 偽の係は、pane.split の戻り値を使わずに、包んだスクリプトを新しい端末 t9 で動かし（muxtest.HC.RunSplitScript）、
// ラベルで端末を探す（muxtest.HC.FindSplit）。
//
// req: FR-316
func TestSplitPaneHerdrAppFindsTerminalByLabel(t *testing.T) {
	for _, left := range []bool{true, false} {
		name := "right"
		if left {
			name = "left"
		}
		t.Run(name, func(t *testing.T) {
			hc := inHerdrApp(t)
			hc.ServeMod(t, func(_ string, req muxtest.Request) muxtest.Reply {
				hc.RunSplitScript(t, req.Argv, "t9")
				found := hc.FindSplit(t, req.ID)
				if found == "" {
					return muxtest.Reply{Error: "no terminal with the split label"}
				}
				return muxtest.Reply{Terminal: found}
			})
			s := &Server{log: slog.New(slog.DiscardHandler)}
			s.sock.Store(filepath.Join(t.TempDir(), "server.sock"))
			if err := s.splitPane("t8", "", t.TempDir(), "5", left); err != nil {
				t.Fatalf("splitPane = %v, want nil（ラベルから新しい端末を見つける）", err)
			}
			served := hc.Served()
			if len(served) != 1 || served[0].Request.Direction != name {
				t.Fatalf("mod への依頼 = %+v, want %s へ割る依頼 1 つ", served, name)
			}
			if got := hc.Labels(t, "t9")["vi-imitation.split"]; got != served[0].Request.ID {
				t.Errorf("新しい端末 t9 のラベル = %q, want 依頼の番号 %q", got, served[0].Request.ID)
			}
			// コマンド（ここでは true）が終わると、包んだスクリプトは自分の端末だけを終わらせる。
			if killed := hc.Killed(t); !slices.Equal(killed, []string{"t9"}) {
				t.Errorf("hc kill = %v, want [t9]", killed)
			}
		})
	}
}

// herdr app に mod が入っていない（結果が届かない）ときは、:diffsplit の誤りに、mod を入れるよう促す文言が出る。
//
// req: FR-316
func TestDiffSplitHerdrAppNoMod(t *testing.T) {
	inHerdrApp(t)
	old := herdrAppSplitWait
	herdrAppSplitWait = 500 * time.Millisecond
	t.Cleanup(func() { herdrAppSplitWait = old })
	c, ch := startHerdrAppServer(t)
	sendKeys(t, c, ":diffsplit b.txt<CR>")
	waitScreenText(t, ch, "herdr app に vi-imitation の mod が入っていない")
}

// herdr app の中の server は、中央のポップアップの関数（OpenPopup、RunPopup）を入れない。
// ポップアップは client の画面の中に描き、:terminal.popup は :! と同じく動かす（mux.HerdrApp.OpenPopup は持たない）。
// ペインの部品（OpenPane）と見比べ（SplitPane）は入れる。
func TestWithMuxHerdrApp(t *testing.T) {
	inHerdrApp(t)
	if mux.Detect(os.Getenv).Kind() != mux.KindHerdrApp {
		t.Fatal("herdr app と判定しない")
	}
	s := &Server{log: slog.New(slog.DiscardHandler)}
	got := s.withMux(editor.Config{})
	if got.OpenPopup != nil || got.RunPopup != nil || got.PopupScreenWidth != nil {
		t.Error("herdr app の中で、ポップアップの関数が入った")
	}
	if got.OpenPane == nil || got.SplitPane == nil {
		t.Error("herdr app の中で、ペインの関数が入らない")
	}
}
