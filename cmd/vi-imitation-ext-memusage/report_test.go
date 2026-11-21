package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
)

// fakeSource は、テスト用のプロセスの木である。
type fakeSource struct {
	procs map[int]proc
	kids  map[int][]int
}

func (s fakeSource) children(pid int) []int { return s.kids[pid] }

func (s fakeSource) info(pid int) (proc, bool) {
	p, ok := s.procs[pid]
	return p, ok
}

// newFakeTree は、本体 100 の下に、LSP の拡張機能（言語サーバー 2 つ）、memusage の拡張機能、:! で起動したシェルを持つ木を作る。
// LSP の拡張機能は環境変数で宣言ファイルのディレクトリを持ち、memusage は持たない（ps の出力から作ったときの形）。
// 199 は子の一覧にあるが終わったプロセスで、101 は孫の一覧に重ねて現れる（輪）。
func newFakeTree(t *testing.T) fakeSource {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "lsp-dir")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	// 宣言ファイルは JSONC で読む。
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{ "id": "lsp", /* c */ "command": "x", }`), 0o600); err != nil {
		t.Fatal(err)
	}
	return fakeSource{
		procs: map[int]proc{
			100: {pid: 100, rssKB: 18 * 1024, args: []string{"/opt/vi/vi-imitation", "-server"}},
			101: {pid: 101, rssKB: 12 * 1024, args: []string{"/opt/vi/extensions/lsp/vi-imitation-ext-lsp"}, extDir: dir},
			102: {pid: 102, rssKB: 390 * 1024, args: []string{"/home/u/go/bin/gopls", "serve"}},
			103: {pid: 103, rssKB: 100 * 1024, args: []string{"pyright-langserver", "--stdio"}},
			104: {pid: 104, rssKB: 1024, args: []string{"node", "worker.js"}},
			110: {pid: 110, rssKB: 6 * 1024, args: []string{"/opt/vi/extensions/memusage/vi-imitation-ext-memusage"}},
			120: {pid: 120, rssKB: 3 * 1024, args: []string{"/bin/sh", "-c", "make"}},
		},
		kids: map[int][]int{
			100: {101, 110, 120, 199},
			101: {102, 103},
			103: {104, 101},
		},
	}
}

// 本体を根に子孫を辿り、本体の子（拡張機能）ごとに、その子孫（言語サーバーなど）の RSS をまとめる。
// 拡張機能の名前は、宣言ファイルのディレクトリの環境変数があれば宣言ファイルの ID、無ければ実行ファイルの名前から付ける。
//
// req: FR-227, FR-228
func TestCollect(t *testing.T) {
	r, ok := collect(newFakeTree(t), 100, manifestIDs{})
	if !ok {
		t.Fatal("collect の ok = false")
	}
	if r.selfKB != 18*1024 || r.selfCmd != "vi-imitation -server" {
		t.Errorf("本体 = %d KiB %q", r.selfKB, r.selfCmd)
	}
	var names []string
	for _, g := range r.groups {
		names = append(names, g.name)
	}
	if want := []string{"lsp", "memusage", "sh"}; !slices.Equal(names, want) {
		t.Fatalf("まとまり = %v, want %v", names, want)
	}
	lsp := r.groups[0]
	if !lsp.ext || lsp.rssKB != (12+390+100+1)*1024 {
		t.Errorf("lsp = %+v", lsp)
	}
	var rows []string
	for _, rw := range lsp.rows {
		rows = append(rows, strings.Repeat(">", rw.depth)+rw.cmd)
	}
	if want := []string{"vi-imitation-ext-lsp", ">gopls serve", ">pyright-langserver --stdio", ">>node worker.js"}; !slices.Equal(rows, want) {
		t.Errorf("lsp の行 = %v, want %v", rows, want)
	}
	if !r.groups[1].ext || r.groups[2].ext {
		t.Errorf("拡張機能かどうか: memusage %v, sh %v", r.groups[1].ext, r.groups[2].ext)
	}
	if got := r.descendantsKB(); got != (503+6+3)*1024 {
		t.Errorf("子孫の合計 = %d", got)
	}

	if got, want := statusText(&r), "vi 18M · ext 512M"; got != want {
		t.Errorf("statusText = %q, want %q", got, want)
	}
	detail := detailText(&r)
	for _, want := range []string{
		"合計 530M（本体 18M、拡張機能とその子 512M）",
		"(本体)            100     18M  vi-imitation -server",
		"lsp               101     12M  vi-imitation-ext-lsp",
		"                  102    390M    gopls serve",
		"                  104      1M      node worker.js",
		strings.Repeat(" ", 25) + "503M  （lsp の合計）",
		"(sh)              120      3M  sh -c make",
	} {
		if !strings.Contains(detail, want+"\n") {
			t.Errorf("内訳に %q が無い:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "memusage の合計") {
		t.Errorf("子の無い拡張機能に合計の行を出した:\n%s", detail)
	}

	if _, ok := collect(newFakeTree(t), 999, manifestIDs{}); ok {
		t.Error("読めない根で ok = true")
	}
}

func TestFormatKB(t *testing.T) {
	for _, c := range []struct {
		kb   int64
		want string
	}{{0, "0M"}, {511, "0M"}, {512, "1M"}, {56 * 1024, "56M"}, {1023*1024 + 600, "1024M"}, {1024 * 1024, "1.0G"}, {1536 * 1024, "1.5G"}} {
		if got := formatKB(c.kb); got != c.want {
			t.Errorf("formatKB(%d) = %q, want %q", c.kb, got, c.want)
		}
	}
	long := strings.Repeat("あ", 100)
	if got := commandLine([]string{"/x/y", long}); len([]rune(got)) != maxCommandRunes || !strings.HasSuffix(got, "…") || !strings.HasPrefix(got, "y あ") {
		t.Errorf("commandLine の長い引数 = %q", got)
	}
}

// ps の出力から木を作る。集計のために起動した ps 自身は数えない。
//
// req: FR-227
func TestParsePS(t *testing.T) {
	out := []byte(`    1     0   100 /sbin/init
  100     1  18432 vi-imitation -server
  101   100  12288 /opt/vi/extensions/lsp/vi-imitation-ext-lsp
  102   101 399360 gopls serve
  110   100   6144 vi-imitation-ext-memusage
  111   110   2048 ps -A -o pid=,ppid=,rss=,args=
broken line
`)
	src := parsePS(out, 110)
	r, ok := collect(src, 100, manifestIDs{})
	if !ok {
		t.Fatal("ok = false")
	}
	var names []string
	for _, g := range r.groups {
		names = append(names, g.name)
	}
	if !slices.Equal(names, []string{"lsp", "memusage"}) || r.groups[0].rssKB != 12288+399360 || len(r.groups[1].rows) != 1 {
		t.Errorf("groups = %+v", r.groups)
	}
}

// Linux の /proc から、テストのプロセスの子孫だけを辿る。子の環境変数に宣言ファイルのディレクトリがあれば、その ID を名前にする。
//
// req: FR-227
func TestProcSource(t *testing.T) {
	if !hasProcChildren() {
		t.Skip("/proc/<pid>/task/<tid>/children が無い")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep が無い")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id": "probe", "command": "x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sleep, "30")
	cmd.Env = append(os.Environ(), ext.EnvExtensionDir+"="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	pid := cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, ok := collect(procSource{root: "/proc"}, os.Getpid(), manifestIDs{})
		if !ok {
			t.Fatal("テストのプロセスを読めない")
		}
		if r.selfKB <= 0 {
			t.Errorf("テストのプロセスの RSS = %d", r.selfKB)
		}
		for _, g := range r.groups {
			// exec の直後は、まだ環境変数とコマンドラインが sleep のものになっていないことがあるので、待つ。
			if g.rows[0].pid == pid && g.name == "probe" && g.ext && strings.HasPrefix(g.rows[0].cmd, "sleep 30") {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("子の sleep（%d）が probe として見つからない: %+v", pid, r.groups)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
