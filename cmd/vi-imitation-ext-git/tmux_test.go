package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// testTmux は、テスト用に tmux -L で立てた別の tmux のサーバーである。利用者の tmux には触らない。
type testTmux struct {
	t    *testing.T
	bin  string
	name string
	env  []string
}

func (ts *testTmux) run(args ...string) string {
	ts.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ts.bin, append([]string{"-L", ts.name, "-f", "/dev/null"}, args...)...) //nolint:gosec // テスト用の tmux
	cmd.Env = ts.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		ts.t.Fatalf("tmux %v: %v: %s", args, err, out.String())
	}
	return strings.TrimSpace(out.String())
}

// waitPane は、ペインの画面が cond を満たすまで待ち、その画面を返す。
func (ts *testTmux) waitPane(pane, what string, cond func(screen string) bool) string {
	ts.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		s := ts.run("capture-pane", "-p", "-t", pane)
		if cond(s) {
			return s
		}
		if time.Now().After(deadline) {
			ts.t.Fatalf("ペイン %s が %s にならない。画面:\n%s", pane, what, s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (ts *testTmux) panes() []string {
	ts.t.Helper()
	return strings.Fields(ts.run("list-panes", "-a", "-F", "#{pane_id}"))
}

// buildDist は、本体と git の拡張機能を、配布物と同じ並び（<bin>/vi-imitation と <bin>/extensions/git/）でビルドし、本体のパスを返す。
func buildDist(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go が無い")
	}
	bin := filepath.Join(t.TempDir(), "bin")
	ext := filepath.Join(bin, "extensions", "git")
	for _, b := range []struct{ out, pkg string }{
		{filepath.Join(bin, "vi-imitation"), "../vi-imitation"},
		{filepath.Join(ext, "vi-imitation-ext-git"), "."},
	} {
		out, err := exec.Command("go", "build", "-o", b.out, b.pkg).CombinedOutput() //nolint:gosec // テストで使う実行ファイルを一時ディレクトリへビルドする
		if err != nil {
			t.Fatalf("build %s: %v\n%s", b.pkg, err, out)
		}
	}
	manifest, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "manifest.json"), manifest, 0o600); err != nil { //nolint:gosec // テストの一時ディレクトリへ、リポジトリの宣言ファイルを写す
		t.Fatal(err)
	}
	return filepath.Join(bin, "vi-imitation")
}

// testEnv は、tmux のサーバーとエディタに渡す環境である。
// 利用者の端末多重化ソフト、エディタの server、git のリポジトリと設定、エディタの設定と拡張機能の置き場所を引き継がない。
func testEnv(t *testing.T) []string {
	t.Helper()
	// XDG_RUNTIME_DIR には server のソケットを置くので、ソケットのパスの長さの上限（基本設計 1.2）に収まる短いパスにする。
	// os.MkdirTemp は権限 0700 で作る（server のディレクトリの検査に通る）。
	runtime, err := os.MkdirTemp("", "meg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	home := t.TempDir()
	drop := []string{"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_SOCKET_PATH", "VI_IMITATION_SOCKET", "HOME", "TERM"}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return strings.HasPrefix(name, "GIT_") || strings.HasPrefix(name, "XDG_") || slices.Contains(drop, name)
	})
	return append(env, "HOME="+home, "TERM=xterm-256color", "XDG_RUNTIME_DIR="+runtime,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_DATA_HOME="+filepath.Join(home, "data"),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
}

// TestDiffHeadInTmux は、tmux（tmux -L の別のサーバー）の中で、配布物と同じ並びの本体と git の拡張機能を起動し、
// 一時の git のリポジトリのファイルで <Leader>gd（git.diffHead）を打つと、HEAD の版が、編集を禁止したファイルでないバッファとして
// 左に分割したペインの新しい編集の client のタブに開き、ファイルのタブと見比べられることと、フォーカスが右のファイルのペインに
// 残ることを確かめる（FR-104。#437 で diffview.nvim と同じ並びにした）。
//
// req: FR-104
func TestDiffHeadInTmux(t *testing.T) {
	if testing.Short() {
		t.Skip("-short では実物の tmux を使うテストを飛ばす")
	}
	isolateGit(t)
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux が無い")
	}
	editor := buildDist(t)
	repo := newRepo(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	writeTestFiles(t, repo, map[string]string{"a.txt": "one\nTWO\nthree\nfour\n"})

	ts := &testTmux{t: t, bin: tmuxBin, name: fmt.Sprintf("vi-imitation-git-test-%d-%d", os.Getpid(), time.Now().UnixNano()), env: testEnv(t)}
	t.Cleanup(func() {
		q := exec.Command(tmuxBin, "-L", ts.name, "display-message", "-p", "#{socket_path}") //nolint:gosec // テスト用の tmux
		q.Env = ts.env
		sock, _ := q.Output()
		k := exec.Command(tmuxBin, "-L", ts.name, "kill-server") //nolint:gosec // テスト用の tmux
		k.Env = ts.env
		_ = k.Run()
		if s := strings.TrimSpace(string(sock)); s != "" {
			_ = os.Remove(s)
		}
	})
	// 最初のペインは、エディタが終わっても window を残すための待つだけのもの。
	p0 := ts.run("new-session", "-d", "-s", "t", "-x", "200", "-y", "30", "-c", repo, "-P", "-F", "#{pane_id}", "sleep 600")
	p1 := ts.run("split-window", "-d", "-h", "-t", p0, "-c", repo, "-P", "-F", "#{pane_id}", fmt.Sprintf("'%s' a.txt", editor))
	ts.waitPane(p1, "a.txt を開いた画面", func(s string) bool { return strings.Contains(s, "TWO") && strings.Contains(s, "a.txt") })
	// 利用者と同じく、ファイルのペインにフォーカスを置いて打つ。
	ts.run("select-pane", "-t", p1)

	// 続けて 2 回打つ。2 回目は、1 回目のペインの client を待っている間なので断られ、ペインもバッファも増えない（PR #247 のレビューの必須 1）。
	ts.run("send-keys", "-t", p1, "-l", " gd gd")
	var p2 string
	deadline := time.Now().Add(15 * time.Second)
	for p2 == "" {
		for _, p := range ts.panes() {
			if p != p0 && p != p1 {
				p2 = p
			}
		}
		if p2 == "" {
			if time.Now().After(deadline) {
				t.Fatalf("HEAD の版のペインが開かない。画面:\n%s", ts.run("capture-pane", "-p", "-t", p1))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// HEAD の版は、名前が HEAD:a.txt の、編集を禁止したバッファ（[RO]）になる。
	// ファイルにだけある four の行の位置には、詰め物の行（- で埋めた行）を描く。
	head := ts.waitPane(p2, "HEAD の版の画面", func(s string) bool {
		return strings.Contains(s, "HEAD:a.txt") && strings.Contains(s, "[RO]") && strings.Contains(s, "two") && hasFillerLine(s)
	})
	if strings.Contains(head, "TWO") || strings.Contains(head, "four") {
		t.Errorf("HEAD の版のペインに作業ツリーの行が出た:\n%s", head)
	}
	ts.waitPane(p1, "見比べを始めた知らせ", func(s string) bool { return strings.Contains(s, "見比べます") })
	ts.run("send-keys", "-t", p1, ":ls", "Enter")
	ls := ts.waitPane(p1, ":ls の一覧", func(s string) bool { return strings.Contains(s, `"HEAD:a.txt"`) })
	if n := strings.Count(ls, `"HEAD:a.txt"`); n != 1 {
		t.Errorf(":ls の HEAD:a.txt = %d 個, want 1:\n%s", n, ls)
	}
	if n := len(ts.panes()); n != 3 {
		t.Errorf("ペイン = %d, want 3（待つだけのもの、ファイル、HEAD の版）", n)
	}
	ts.checkBaseLeft(p2, p1)
	ts.run("send-keys", "-t", p1, "Enter")

	// 2 行目で、HEAD の版へは dp で送れず（E21）、do で HEAD の内容を取り込める（基本設計 6.8）。
	ts.run("send-keys", "-t", p1, "-l", "2Gdp")
	ts.waitPane(p1, "dp を断った画面", func(s string) bool { return strings.Contains(s, "E21") })
	ts.run("send-keys", "-t", p1, "-l", "do")
	ts.waitPane(p1, "do で取り込んだ画面", func(s string) bool { return strings.Contains(s, "two") && !strings.Contains(s, "TWO") })

	// HEAD の版のタブを閉じると、その client は終わり、ファイルの側には見比べが終わったことを知らせる。
	ts.run("send-keys", "-t", p2, ":q", "Enter")
	ts.waitPane(p1, "見比べの終わりの知らせ", func(s string) bool { return strings.Contains(s, "見比べが終わりました") })
	ts.run("send-keys", "-t", p1, ":q!", "Enter")
	deadline = time.Now().Add(15 * time.Second)
	for len(ts.panes()) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("エディタのペインが閉じない: %v", ts.panes())
		}
		time.Sleep(50 * time.Millisecond)
	}
	// ファイルは書き出していないので、作業ツリーの変更はそのまま残る。
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "one\nTWO\nthree\nfour\n" {
		t.Errorf("a.txt = %q", b)
	}
}

// TestDiffHeadOutsideMuxInTmux は、端末多重化ソフトの外（tmux のペインで TMUX と TMUX_PANE を外して起動した本体）で <Leader>gd を打つと、
// 見比べられないことを知らせ、HEAD の版のバッファを作らない（隠れたバッファを残さない。FR-64）ことを確かめる（PR #247 のレビューの必須 1）。
//
// req: FR-104
func TestDiffHeadOutsideMuxInTmux(t *testing.T) {
	if testing.Short() {
		t.Skip("-short では実物の tmux を使うテストを飛ばす")
	}
	isolateGit(t)
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux が無い")
	}
	editor := buildDist(t)
	repo := newRepo(t, map[string]string{"a.txt": "one\n"})
	writeTestFiles(t, repo, map[string]string{"a.txt": "ONE\n"})
	ts := &testTmux{t: t, bin: tmuxBin, name: fmt.Sprintf("vi-imitation-git-test-%d-%d", os.Getpid(), time.Now().UnixNano()), env: testEnv(t)}
	t.Cleanup(func() {
		k := exec.Command(tmuxBin, "-L", ts.name, "kill-server") //nolint:gosec // テスト用の tmux
		k.Env = ts.env
		_ = k.Run()
	})
	p := ts.run("new-session", "-d", "-s", "t", "-x", "160", "-y", "20", "-c", repo, "-P", "-F", "#{pane_id}",
		fmt.Sprintf("env -u TMUX -u TMUX_PANE '%s' a.txt; sleep 600", editor))
	ts.waitPane(p, "a.txt を開いた画面", func(s string) bool { return strings.Contains(s, "ONE") })
	for range 2 {
		ts.run("send-keys", "-t", p, "-l", " gd")
		ts.waitPane(p, "見比べられない知らせ", func(s string) bool { return strings.Contains(s, "外では HEAD と見比べられません") })
		ts.run("send-keys", "-t", p, "-l", "j")
	}
	ts.run("send-keys", "-t", p, ":ls", "Enter")
	ls := ts.waitPane(p, ":ls の一覧", func(s string) bool { return strings.Contains(s, `%a   "a.txt"`) })
	if strings.Contains(ls, "HEAD:") {
		t.Errorf(":ls に HEAD の版のバッファが残った:\n%s", ls)
	}
	if n := len(ts.panes()); n != 1 {
		t.Errorf("ペイン = %d, want 1", n)
	}
	ts.run("send-keys", "-t", p, "Enter", ":q!", "Enter")
}

// TestChangesInTmux は、tmux の中で <Leader>gV（git.changes）を打つと、既定のブランチ（main）との merge-base と作業ツリーで
// 違うファイルの一覧がファインダーに出て、選んだファイルが元のペインのタブに開き、merge-base の版が左に分割したペインで見比べられることを確かめる
// （#437 で diffview.nvim と同じ並びにした）。選んだ後もフォーカスは右のファイルのペインに残るので、次の一覧は利用者と同じくフォーカスのあるペインで開く。
// 右のファイルのペインから次のファイルを選んでも（ファイルのペインのタブに無いファイルでも）、左の版のペインから選んでも、前の見比べのペインは
// 閉じてから新しいペインが開き、ペインは左右の 2 つのままである。足したファイルは空の版と見比べ、消したファイルは merge-base の版だけを開く。tmux に繋いだ端末が無いので、中央のポップアップはエディタの画面の中に出る（FR-123 の代わりの描き方）。
//
// req: FR-250, FR-251
func TestChangesInTmux(t *testing.T) {
	if testing.Short() {
		t.Skip("-short では実物の tmux を使うテストを飛ばす")
	}
	isolateGit(t)
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux が無い")
	}
	editor := buildDist(t)
	repo := newRepo(t, map[string]string{"a.txt": "one\ntwo\nthree\n", "b.txt": "bee\n", "gone.txt": "gone line\n"})
	runGit(t, repo, "checkout", "-q", "-b", "feature")
	writeTestFiles(t, repo, map[string]string{"a.txt": "one\nTWO\nthree\n", "b.txt": "BEE\n", "fresh.txt": "fresh line\n"})
	runGit(t, repo, "rm", "-q", "gone.txt")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "feature")

	ts := &testTmux{t: t, bin: tmuxBin, name: fmt.Sprintf("vi-imitation-git-test-%d-%d", os.Getpid(), time.Now().UnixNano()), env: testEnv(t)}
	t.Cleanup(func() {
		k := exec.Command(tmuxBin, "-L", ts.name, "kill-server") //nolint:gosec // テスト用の tmux
		k.Env = ts.env
		_ = k.Run()
	})
	p0 := ts.run("new-session", "-d", "-s", "t", "-x", "200", "-y", "30", "-c", repo, "-P", "-F", "#{pane_id}", "sleep 600")
	// エディタのペインは、待つだけのペインの左に置く。tmux は閉じたペインの幅を左隣のペインへ渡すので、エディタの左にペインがあると、
	// 版のペインを閉じて開き直すたびにファイルのペインが狭くなる（#437 の PR の未確認事項）。ここでは並びの確かめに絞る。
	p1 := ts.run("split-window", "-h", "-b", "-t", p0, "-c", repo, "-P", "-F", "#{pane_id}", fmt.Sprintf("'%s' fresh.txt", editor))
	ts.waitPane(p1, "fresh.txt を開いた画面", func(s string) bool { return strings.Contains(s, "fresh line") })

	// pick は、利用者と同じく、いまフォーカスのあるペイン（tmux の active なペイン）で一覧を開き、query で 1 件に絞って選ぶ。
	pick := func(query string) {
		t.Helper()
		active := ts.run("display-message", "-p", "-t", "t", "#{pane_id}")
		ts.run("send-keys", "-t", active, "-l", " gV")
		ts.waitPane(active, "差分の一覧", func(s string) bool {
			return strings.Contains(s, "git の差分 main...HEAD  4/4") && strings.Contains(s, "M  a.txt") && strings.Contains(s, "M  b.txt") &&
				strings.Contains(s, "A  fresh.txt") && strings.Contains(s, "D  gone.txt")
		})
		ts.run("send-keys", "-t", active, "-l", query)
		ts.waitPane(active, "絞り込んだ一覧", func(s string) bool { return strings.Contains(s, "1/4") })
		ts.run("send-keys", "-t", active, "Enter")
	}
	// basePane は、待つだけのペインとファイルのペイン（p1）のほかのペインが 1 つだけになり、その画面が cond を満たすまで待って、その ID を返す。
	basePane := func(what string, cond func(string) bool) string {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			var others []string
			for _, p := range ts.panes() {
				if p != p0 && p != p1 {
					others = append(others, p)
				}
			}
			if len(others) == 1 {
				// 前の見比べのペインが閉じかけていると、一覧に出た後に無くなるので、取り出せなければ待ち直す。
				if s, ok := ts.tryCapture(others[0]); ok && cond(s) {
					return others[0]
				}
			}
			if time.Now().After(deadline) {
				var screens strings.Builder
				for _, p := range others {
					s, _ := ts.tryCapture(p)
					fmt.Fprintf(&screens, "== %s\n%s\n", p, s)
				}
				t.Fatalf("版のペインが %s にならない: %v\n%s", what, ts.panes(), screens.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// ファイルのペインから a.txt を選ぶ。版は左に開き、フォーカスは右のファイルのペインに残る。
	pick("a.t")
	p2 := basePane("a.txt の merge-base の版", func(s string) bool {
		return strings.Contains(s, "main...HEAD:a.txt") && strings.Contains(s, "[RO]") && strings.Contains(s, "two") && strings.Contains(s, "（client 1）を")
	})
	if s := ts.run("capture-pane", "-p", "-t", p2); strings.Contains(s, "TWO") {
		t.Errorf("merge-base の版のペインに作業ツリーの行が出た:\n%s", s)
	}
	ts.waitPane(p1, "a.txt のタブ", func(s string) bool { return strings.Contains(s, "TWO") })
	ts.checkBaseLeft(p2, p1)

	// そのまま右のファイルのペインから、どのタブでもない b.txt を選んでも、左の版のペインが入れ替わるだけで、
	// b.txt は右のファイルのペインに開く（PR #382 のレビューの必須 1）。
	pick("b.t")
	p3 := basePane("b.txt の merge-base の版", func(s string) bool {
		return strings.Contains(s, "main...HEAD:b.txt") && strings.Contains(s, "bee") && strings.Contains(s, "（client 1）を")
	})
	if p3 == p2 {
		t.Errorf("版のペインが入れ替わらない: %s", p3)
	}
	ts.waitPane(p1, "b.txt のタブ", func(s string) bool { return strings.Contains(s, "BEE") })
	ts.checkBaseLeft(p3, p1)

	// 足したファイル（ファイルのペインのタブにある fresh.txt）を、左の版のペインから選ぶ。版のペインは閉じてから開き直り、
	// fresh.txt は右のファイルのペインに開く。空の版と見比べる。
	// 空の版は、空の行 1 つを変えた行として描き、1 行のファイルに対する詰め物は 0 行になる（基本設計 6.8。Vim 9.1 と同じ）。
	ts.run("select-pane", "-t", p3)
	pick("fresh")
	p4 := basePane("fresh.txt の比べる元の版", func(s string) bool {
		return strings.Contains(s, "main...HEAD:fresh.txt") && strings.Contains(s, "[RO]") && !strings.Contains(s, "fresh line") && strings.Contains(s, "（client 1）を")
	})
	ts.waitPane(p1, "fresh.txt のタブ", func(s string) bool { return strings.Contains(s, "fresh line") })
	ts.checkBaseLeft(p4, p1)

	// 消したファイルを右のファイルのペインから選ぶと、見比べのペインを閉じ、merge-base の版だけをファイルのペインのタブに開く。
	pick("gone")
	ts.waitPane(p1, "消したファイルの版", func(s string) bool {
		return strings.Contains(s, "gone line") && strings.Contains(s, "main...HEAD:gone.txt") && strings.Contains(s, "作業ツリーに無いので")
	})
	ts.waitPanes(2)
	ts.run("send-keys", "-t", p1, ":qa!", "Enter")
	ts.waitPanes(1)
}

// TestChangesLastClientInTmux は、見比べのファイルのペインを未保存のまま閉じ、比べる元の版のペインしか残っていないときに一覧から選んでも、
// エディタが終わらず（本体は最後の窓を名前の無い空のバッファに替える）、未保存の変更が残ることを確かめる（PR #382 の再レビューの必須）。
//
// req: FR-251
func TestChangesLastClientInTmux(t *testing.T) {
	if testing.Short() {
		t.Skip("-short では実物の tmux を使うテストを飛ばす")
	}
	isolateGit(t)
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux が無い")
	}
	editor := buildDist(t)
	repo := newRepo(t, map[string]string{"a.txt": "aaa\n", "b.txt": "bbb\n"})
	runGit(t, repo, "checkout", "-q", "-b", "feature")
	writeTestFiles(t, repo, map[string]string{"a.txt": "AAA\n", "b.txt": "BBB\n"})
	runGit(t, repo, "commit", "-q", "-am", "feature")

	ts := &testTmux{t: t, bin: tmuxBin, name: fmt.Sprintf("vi-imitation-git-test-%d-%d", os.Getpid(), time.Now().UnixNano()), env: testEnv(t)}
	t.Cleanup(func() {
		k := exec.Command(tmuxBin, "-L", ts.name, "kill-server") //nolint:gosec // テスト用の tmux
		k.Env = ts.env
		_ = k.Run()
	})
	p0 := ts.run("new-session", "-d", "-s", "t", "-x", "200", "-y", "30", "-c", repo, "-P", "-F", "#{pane_id}", "sleep 600")
	p1 := ts.run("split-window", "-h", "-t", p0, "-c", repo, "-P", "-F", "#{pane_id}", fmt.Sprintf("'%s' a.txt", editor))
	ts.waitPane(p1, "a.txt を開いた画面", func(s string) bool { return strings.Contains(s, "AAA") })
	ts.run("send-keys", "-t", p1, "-l", " gV")
	ts.waitPane(p1, "差分の一覧", func(s string) bool { return strings.Contains(s, "git の差分 main...HEAD  2/2") })
	ts.run("send-keys", "-t", p1, "-l", "a.t")
	ts.waitPane(p1, "絞り込んだ一覧", func(s string) bool { return strings.Contains(s, "1/2") })
	ts.run("send-keys", "-t", p1, "Enter")
	var p2 string
	deadline := time.Now().Add(15 * time.Second)
	for p2 == "" {
		for _, p := range ts.panes() {
			if p != p0 && p != p1 {
				p2 = p
			}
		}
		if p2 == "" && time.Now().After(deadline) {
			t.Fatalf("比べる元の版のペインが開かない: %v", ts.panes())
		}
		time.Sleep(50 * time.Millisecond)
	}
	ts.waitPane(p2, "a.txt の比べる元の版", func(s string) bool {
		return strings.Contains(s, "main...HEAD:a.txt") && strings.Contains(s, "（client 1）を")
	})

	// ファイルのペインで保存せずに変え、ペインごと閉じる。
	ts.run("send-keys", "-t", p1, "-l", "Ounsaved")
	ts.run("send-keys", "-t", p1, "Escape")
	ts.waitPane(p1, "変えた画面", func(s string) bool { return strings.Contains(s, "unsaved") })
	ts.run("kill-pane", "-t", p1)
	ts.waitPanes(2)

	// 残った比べる元の版のペインから b.txt を選ぶ。エディタは終わらず、b.txt がこのペインに開き、その左に比べる元の版が開く。
	ts.run("send-keys", "-t", p2, "-l", " gV")
	ts.waitPane(p2, "差分の一覧", func(s string) bool { return strings.Contains(s, "git の差分 main...HEAD  2/2") })
	ts.run("send-keys", "-t", p2, "-l", "b.t")
	ts.waitPane(p2, "絞り込んだ一覧", func(s string) bool { return strings.Contains(s, "1/2") })
	ts.run("send-keys", "-t", p2, "Enter")
	ts.waitPane(p2, "b.txt のタブ", func(s string) bool { return strings.Contains(s, "BBB") })
	ts.waitPanes(3)
	// 左に開くペインの client が繋ぐ（見比べを始めた知らせが届く）前に :ls を打ってもよい。本体は、hit-enter の問いで待っている
	// :ls の一覧を、後から届いた知らせで上書きしない（#381 の続き。main の CI で、知らせが一覧を消して揺れた）。
	ts.run("send-keys", "-t", p2, ":ls", "Enter")
	ls := ts.waitPane(p2, ":ls の一覧", func(s string) bool { return strings.Contains(s, `"a.txt"`) })
	for l := range strings.Lines(ls) {
		if strings.Contains(l, `"a.txt"`) && !strings.Contains(l, "+") {
			t.Errorf("a.txt の未保存の変更が残っていない: %q", l)
		}
	}
}

// TestChangesNoDiffInTmux は、HEAD との差分の無い作業ツリーで <Leader>gv（git.changesHead）を打つと、一覧を開かずに差分が無いことを知らせ、
// 引数を付けて打つと断ることと、使えない比べる元の書き方と端末多重化ソフトの外では一覧を開かずに理由を出すことを確かめる。
//
// req: FR-250, FR-252
func TestChangesNoDiffInTmux(t *testing.T) {
	if testing.Short() {
		t.Skip("-short では実物の tmux を使うテストを飛ばす")
	}
	isolateGit(t)
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux が無い")
	}
	editor := buildDist(t)
	repo := newRepo(t, map[string]string{"a.txt": "one\n"})
	ts := &testTmux{t: t, bin: tmuxBin, name: fmt.Sprintf("vi-imitation-git-test-%d-%d", os.Getpid(), time.Now().UnixNano()), env: testEnv(t)}
	t.Cleanup(func() {
		k := exec.Command(tmuxBin, "-L", ts.name, "kill-server") //nolint:gosec // テスト用の tmux
		k.Env = ts.env
		_ = k.Run()
	})
	p := ts.run("new-session", "-d", "-s", "t", "-x", "160", "-y", "20", "-c", repo, "-P", "-F", "#{pane_id}", fmt.Sprintf("'%s' a.txt; sleep 600", editor))
	ts.waitPane(p, "a.txt を開いた画面", func(s string) bool { return strings.Contains(s, "one") })
	ts.run("send-keys", "-t", p, "-l", " gv")
	ts.waitPane(p, "差分が無い知らせ", func(s string) bool { return strings.Contains(s, "HEAD との差分はありません") })
	ts.run("send-keys", "-t", p, ":git.changesHead x", "Enter")
	ts.waitPane(p, "引数を断った知らせ", func(s string) bool { return strings.Contains(s, "git.changesHead: 引数を取らない") })
	ts.run("send-keys", "-t", p, ":git.changes main..HEAD", "Enter")
	ts.waitPane(p, "範囲を断った知らせ", func(s string) bool { return strings.Contains(s, "範囲 main..HEAD は使えません") })
	if n := len(ts.panes()); n != 1 {
		t.Errorf("ペイン = %d, want 1", n)
	}
	ts.run("send-keys", "-t", p, ":q!", "Enter")

	// 端末多重化ソフトの外（TMUX と TMUX_PANE を外して起動した本体）では、差分があっても一覧を開かない。
	writeTestFiles(t, repo, map[string]string{"a.txt": "ONE\n"})
	q := ts.run("split-window", "-d", "-t", p, "-c", repo, "-P", "-F", "#{pane_id}", fmt.Sprintf("env -u TMUX -u TMUX_PANE '%s' a.txt; sleep 600", editor))
	ts.waitPane(q, "a.txt を開いた画面", func(s string) bool { return strings.Contains(s, "ONE") })
	ts.run("send-keys", "-t", q, "-l", " gV")
	ts.waitPane(q, "見比べられない知らせ", func(s string) bool { return strings.Contains(s, "外では比べる元と見比べられません") })
	if s := ts.run("capture-pane", "-p", "-t", q); strings.Contains(s, "git の差分") {
		t.Errorf("端末多重化ソフトの外で一覧が開いた:\n%s", s)
	}
	ts.run("send-keys", "-t", q, ":q!", "Enter")
}

// TestChangesWorktreeInTmux は、worktree（git worktree add）の中で、別のリポジトリを指す GIT_DIR と GIT_WORK_TREE を残した本体を起動しても
// （本体を git commit の GIT_EDITOR にしたとき）、<Leader>gV がその worktree のブランチの差分を出すことと、
// カーソルのあるバッファが作業ツリーの外のファイル（COMMIT_EDITMSG）なら作業ディレクトリのリポジトリで一覧を作ることと、
// サブモジュールと追跡していない入れ子のリポジトリは一覧に出すが、選んでも開かずに知らせることを確かめる。
//
// req: FR-252
func TestChangesWorktreeInTmux(t *testing.T) {
	if testing.Short() {
		t.Skip("-short では実物の tmux を使うテストを飛ばす")
	}
	isolateGit(t)
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux が無い")
	}
	editor := buildDist(t)
	repo := newRepo(t, map[string]string{"a.txt": "one\n"})
	runGit(t, repo, "branch", "feature")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-q", wt, "feature")
	writeTestFiles(t, wt, map[string]string{"a.txt": "ONE\n"})
	// サブモジュールは、worktree の中に作ったリポジトリを gitlink（モード 160000）として足して作る（取り出したサブモジュールと同じ形）。
	sub := filepath.Join(wt, "sub")
	runGit(t, wt, "init", "-q", "-b", "main", "sub")
	writeTestFiles(t, sub, map[string]string{"s.txt": "s\n"})
	runGit(t, sub, "add", "-A")
	runGit(t, sub, "commit", "-q", "-m", "sub")
	runGit(t, wt, "-c", "advice.addEmbeddedRepo=false", "add", "sub")
	runGit(t, wt, "commit", "-q", "-m", "feature")
	// 追跡していない入れ子のリポジトリ（サブモジュールとして登録していないもの）。
	runGit(t, wt, "init", "-q", "-b", "main", "nested")
	other := newRepo(t, map[string]string{"x.txt": "x\n"})
	// git commit が GIT_EDITOR に開かせるコミットのメッセージ（worktree の git のディレクトリの中。作業ツリーの外）。
	msg := filepath.Join(repo, ".git", "worktrees", "wt", "COMMIT_EDITMSG")
	writeTestFiles(t, filepath.Dir(msg), map[string]string{"COMMIT_EDITMSG": "commit message\n"})

	ts := &testTmux{t: t, bin: tmuxBin, name: fmt.Sprintf("vi-imitation-git-test-%d-%d", os.Getpid(), time.Now().UnixNano()), env: testEnv(t)}
	t.Cleanup(func() {
		k := exec.Command(tmuxBin, "-L", ts.name, "kill-server") //nolint:gosec // テスト用の tmux
		k.Env = ts.env
		_ = k.Run()
	})
	p0 := ts.run("new-session", "-d", "-s", "t", "-x", "200", "-y", "30", "-c", wt, "-P", "-F", "#{pane_id}", "sleep 600")
	p1 := ts.run("split-window", "-d", "-h", "-t", p0, "-c", wt, "-P", "-F", "#{pane_id}",
		fmt.Sprintf("env GIT_DIR='%s' GIT_WORK_TREE='%s' '%s' '%s'", filepath.Join(other, ".git"), other, editor, msg))
	ts.waitPane(p1, "コミットのメッセージを開いた画面", func(s string) bool { return strings.Contains(s, "commit message") })
	// バッファのファイルは作業ツリーの外なので、作業ディレクトリ（worktree）の差分を出す（PR #382 のレビューの必須 2）。
	ts.run("send-keys", "-t", p1, "-l", " gV")
	ts.waitPane(p1, "差分の一覧", func(s string) bool {
		return strings.Contains(s, "git の差分 main...HEAD  3/3") && strings.Contains(s, "M  a.txt") && strings.Contains(s, "A  sub") &&
			strings.Contains(s, "?  nested/")
	})
	ts.run("send-keys", "-t", p1, "-l", "sub")
	ts.waitPane(p1, "絞り込んだ一覧", func(s string) bool { return strings.Contains(s, "1/3") })
	ts.run("send-keys", "-t", p1, "Enter")
	ts.waitPane(p1, "サブモジュールを断った知らせ", func(s string) bool {
		return strings.Contains(s, "sub はサブモジュールなので見比べられません")
	})
	ts.run("send-keys", "-t", p1, "-l", " gV")
	ts.waitPane(p1, "差分の一覧", func(s string) bool { return strings.Contains(s, "3/3") })
	ts.run("send-keys", "-t", p1, "-l", "nested")
	ts.waitPane(p1, "絞り込んだ一覧", func(s string) bool { return strings.Contains(s, "1/3") })
	ts.run("send-keys", "-t", p1, "Enter")
	ts.waitPane(p1, "入れ子のリポジトリを断った知らせ", func(s string) bool {
		return strings.Contains(s, "nested/ は追跡していない入れ子のリポジトリなので")
	})
	if n := len(ts.panes()); n != 2 {
		t.Errorf("ペイン = %d, want 2", n)
	}
	ts.run("send-keys", "-t", p1, ":q!", "Enter")
	ts.waitPanes(1)
}

// checkBaseLeft は、比べる元の版のペイン base が、ファイルのペイン file のすぐ左に同じ高さで並び、
// フォーカス（tmux の active なペイン）が file にあることを確かめる（#437。diffview.nvim と同じ並び）。
func (ts *testTmux) checkBaseLeft(base, file string) {
	ts.t.Helper()
	geo := func(p string) (left, width, top int) {
		_, _ = fmt.Sscanf(ts.run("display-message", "-p", "-t", p, "#{pane_left} #{pane_width} #{pane_top}"), "%d %d %d", &left, &width, &top)
		return left, width, top
	}
	bl, bw, bt := geo(base)
	fl, _, ft := geo(file)
	if bl+bw+1 != fl || bt != ft {
		ts.t.Errorf("版のペイン（左端 %d、幅 %d、上端 %d）がファイルのペイン（左端 %d、上端 %d）のすぐ左にない", bl, bw, bt, fl, ft)
	}
	if active := ts.run("display-message", "-p", "-t", "t", "#{pane_id}"); active != file {
		ts.t.Errorf("フォーカスのあるペイン = %s, want ファイルのペイン %s", active, file)
	}
}

// tryCapture は、ペインの画面を返す。ペインが無くなっていれば false を返す（テストを落とさない）。
func (ts *testTmux) tryCapture(pane string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ts.bin, "-L", ts.name, "-f", "/dev/null", "capture-pane", "-p", "-t", pane) //nolint:gosec // テスト用の tmux
	cmd.Env = ts.env
	out, err := cmd.Output()
	return string(out), err == nil
}

// waitPanes は、ペインの数が n になるまで待つ。
func (ts *testTmux) waitPanes(n int) {
	ts.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for len(ts.panes()) != n {
		if time.Now().After(deadline) {
			ts.t.Fatalf("ペインの数が %d にならない: %v", n, ts.panes())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// hasFillerLine は、画面に、- だけで埋めた行（見比べの詰め物の行）があるかを返す。
func hasFillerLine(screen string) bool {
	for l := range strings.Lines(screen) {
		l = strings.TrimSpace(l)
		if len(l) >= 10 && strings.Trim(l, "-") == "" {
			return true
		}
	}
	return false
}
