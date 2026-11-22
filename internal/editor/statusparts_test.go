package editor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// statusScript は、ステータス行に部品を足すスクリプトの拡張機能である（テスト用）。
// オーナーの nvim の lualine の部品（mrreview の状態、本体と子プロセスのメモリの使用量）に当たるものを出す。
//
//   - st.mem: 起動した時点で、自分のプロセスの VmRSS を "RSS <n>M" の形で送る
//   - st.review: コマンド st.review の引数の文字列を送る。"drift" を含めば diagnostic.warning の色にする（lualine の drift の警告）
//   - コマンド st.crash は、拡張機能を異常で終わらせる
const statusScript = `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

func rssMB() int {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "VmRSS:") {
			var kb int
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(l, "VmRSS:")), "%d", &kb)
			return kb / 1024
		}
	}
	return 0
}

func main() {
	s := server.New()
	s.OnInitialize(func(_ context.Context, _ *server.InitializeEvent) error {
		go func() { _ = s.SetStatusLine("st.mem", fmt.Sprintf("RSS %dM", rssMB()), "") }()
		return nil
	})
	s.Command("st.review", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
		var a struct{ Text string ` + "`json:\"text\"`" + ` }
		_ = json.Unmarshal(p.Args, &a)
		style := ""
		if strings.Contains(a.Text, "drift") {
			style = "diagnostic.warning"
		}
		go func() { _ = s.SetStatusLine("st.review", a.Text, style) }()
		return ext.CommandExecuteResult{OK: true}, nil
	})
	s.Command("st.crash", func(_ context.Context, _ *server.Buffer, _ *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
		os.Exit(3)
		return ext.CommandExecuteResult{}, nil
	})
	if err := s.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
`

// buildStatusScript は、スクリプトの拡張機能の実行ファイルを一時ディレクトリへビルドし、statusScript を置いて、拡張機能の定義を返す。
// 宣言ファイルは activation を持たず、contributes.statusline の部品だけで起動される。
func buildStatusScript(t *testing.T) exthost.Spec {
	t.Helper()
	if testing.Short() {
		t.Skip("拡張機能のビルドが要る")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go が無い")
	}
	dir := t.TempDir()
	src := filepath.Join("..", "..", "cmd", "vi-imitation-ext-script")
	//nolint:gosec // 引数は定数と、テストが作った一時ディレクトリのパスだけである
	cmd := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(dir, "vi-imitation-ext-script"), "./"+filepath.ToSlash(src))
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build extension: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "st.go"), []byte(statusScript), 0o600); err != nil {
		t.Fatal(err)
	}
	m := ext.Manifest{
		ID: "st", Command: "./vi-imitation-ext-script", Args: []string{"st.go"},
		Contributes: ext.Contributes{
			Commands: []ext.Contribution{{ID: "st.review"}, {ID: "st.crash"}},
			StatusLine: []ext.StatusLinePart{
				{ID: "st.mem", Description: "拡張機能のプロセスの RSS"},
				{ID: "st.review", Description: "レビューの状態"},
			},
		},
	}
	if _, err := ext.ValidateManifests([]ext.Manifest{m}); err != nil {
		t.Fatal(err)
	}
	return exthost.Spec{Manifest: m, Dir: dir}
}

// statusRow は、ステータス行（コマンドラインの行の 1 つ上）の文字列を返す。
func (h *harness) statusRow() string {
	lines := strings.Split(h.term.screen(), "\n")
	if len(lines) < 2 {
		return ""
	}
	return lines[len(lines)-2]
}

// 拡張機能が宣言ファイルの contributes.statusline で足した部品を、設定の statusline に並べると、
// 本体は起動の契機を待たずにその拡張機能を起動し、statusline/set で届いた中身を並べた位置に出す。
// 中身を変えれば出し直し、拡張機能が異常で終わればその拡張機能の部品を消す。
//
// req: FR-92
func TestExtensionStatusLineParts(t *testing.T) {
	spec := buildStatusScript(t)
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n"})
	h := start(t, Config{
		WorkDir: dir, Extensions: []exthost.Spec{spec}, TabLine: ui.TabLineMultiple,
		StatusLine: &ui.StatusLine{Left: []string{ui.StatusPartMode, "st.review", ui.StatusPartName}, Right: []string{"st.mem", ui.StatusPartRuler}},
	}, "a.txt")
	name := filepath.Join(dir, "a.txt")
	h.eventually("起動の契機なしで起動して RSS を出す", func() bool { return strings.Contains(h.statusRow(), " RSS ") })
	if row := h.statusRow(); !strings.HasPrefix(row, " NORMAL  "+name+" ") {
		t.Errorf("中身の届いていない st.review を出した: %q", row)
	}

	h.keys(":st.review !12 3/10<CR>")
	h.eventually("レビューの状態を名前の前に出す", func() bool { return strings.HasPrefix(h.statusRow(), " NORMAL  !12 3/10  "+name+" ") })
	h.keys(":st.review base drift<CR>")
	h.eventually("出し直す", func() bool { return strings.HasPrefix(h.statusRow(), " NORMAL  base drift  "+name+" ") })
	var part ui.ExtPart
	h.e.do(func() { part = h.e.extParts["st.review"] })
	if part.Style != ui.StyleDiagnosticWarning {
		t.Errorf("st.review の色 = %q, want %q", part.Style, ui.StyleDiagnosticWarning)
	}

	// 宣言していない部品と、他の拡張機能の部品の中身は捨てる。
	h.e.do(func() {
		h.e.setStatusPart("st", ext.StatusLineSetParams{Part: "st.other", Text: "x"})
		h.e.setStatusPart("other", ext.StatusLineSetParams{Part: "st.review", Text: "y"})
	})
	var other bool
	h.e.do(func() {
		part = h.e.extParts["st.review"]
		_, other = h.e.extParts["st.other"]
	})
	if other || part.Text != "base drift" {
		t.Errorf("宣言していない部品を持った: st.other %v, st.review %+v", other, part)
	}
	// 長い中身は maxStatusPartRunes 文字で切って持つ。
	h.e.do(func() {
		h.e.setStatusPart("st", ext.StatusLineSetParams{Part: "st.review", Text: strings.Repeat("あ", 300)})
		part = h.e.extParts["st.review"]
		h.e.setStatusPart("st", ext.StatusLineSetParams{Part: "st.review", Text: "base drift"})
	})
	if n := len([]rune(part.Text)); n != maxStatusPartRunes {
		t.Errorf("長い中身の文字数 = %d, want %d", n, maxStatusPartRunes)
	}

	// 異常で終わると部品を消す。再起動の後は、拡張機能が送り直したものだけを出す。
	h.keys(":st.crash<CR>")
	h.eventually("異常の後に st.review を消す", func() bool {
		return strings.HasPrefix(h.statusRow(), " NORMAL  "+name+" ")
	})
	h.eventually("再起動の後に RSS を出し直す", func() bool { return strings.Contains(h.statusRow(), " RSS ") })

	// :ext sync の入れ替え（exthost.Host.Reload）で未起動へ戻っても、activation の無い部品の拡張機能を起動し直し、
	// 部品を送り直させる（PR #239 のレビューの必須 1）。入れ替えの前の中身（st.review）は消える。
	h.eventually("稼働中", func() bool { return h.state("st") == exthost.StateRunning })
	h.keys(":st.review before<CR>")
	h.eventually("入れ替えの前のレビューの状態", func() bool { return strings.Contains(h.statusRow(), " before ") })
	h.e.do(func() { h.e.stopExtension("st", h.e.host.Reload) })
	h.eventually("入れ替えの後に起動し直して RSS を出し直す", func() bool {
		row := h.statusRow()
		return h.state("st") == exthost.StateRunning && strings.Contains(row, " RSS ") && !strings.Contains(row, " before ")
	})
}

// 起動の契機 startup の拡張機能は、Run が最初の画面を描いた後に、キーもファイルの種類も待たずに起動する。
// Run の前（New とファイルを開いた後）は起動せず、他の契機の拡張機能は起動しない。
//
// req: FR-225
func TestStartupExtensionAfterFirstRender(t *testing.T) {
	specs := fakeSpecs(t)
	specs[1].Manifest.Activation = append(specs[1].Manifest.Activation, ext.Activation{On: ext.ActivationStartup})
	// slow は部品だけを足し、設定の並びに置く（#238 の、並べた部品の拡張機能を起動時に起動する経路）。
	specs[2].Manifest.Contributes.StatusLine = []ext.StatusLinePart{{ID: "slow.part"}}
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n"})

	// 最初の Draw の時点の状態を記録する。Draw はイベントループの goroutine で描いた直後に呼ばれるので、
	// 最初の画面より前に起動を始めていれば（Run の先頭や New で起動すれば）、ここで起動中か稼働中になっている（PR #375 のレビューの推奨 2）。
	term := newFakeTerm()
	var ed *Editor
	var (
		mu    sync.Mutex
		first map[string]exthost.State
		draws int
	)
	term.onDraw = func() {
		mu.Lock()
		defer mu.Unlock()
		draws++
		if first == nil {
			first = map[string]exthost.State{}
			for _, id := range []string{"ftx", "slow"} {
				first[id], _ = ed.host.State(id)
			}
		}
	}
	h := startTermWith(t, term, Config{
		WorkDir: dir, Extensions: specs, StatusLine: &ui.StatusLine{Left: []string{ui.StatusPartName}, Right: []string{"slow.part"}},
	}, func(e *Editor) error {
		ed = e
		return e.Open("a.txt")
	})
	h.eventually("startup の拡張機能が稼働中", func() bool { return h.state("ftx") == exthost.StateRunning })
	h.eventually("部品の拡張機能が起動", func() bool { return h.state("slow") != exthost.StateNotStarted })
	mu.Lock()
	defer mu.Unlock()
	if draws == 0 {
		t.Fatal("Draw が呼ばれていない")
	}
	for id, st := range first {
		if st != exthost.StateNotStarted {
			t.Errorf("最初の画面を描いた時点の %s の状態 = %s, want not-started", id, st)
		}
	}
	if st := h.state("fake"); st != exthost.StateNotStarted {
		t.Errorf("fake の状態 = %s, want not-started", st)
	}
}

// 設定の並びに書いた "." を含む ID を、どの拡張機能も足していなければ、設定の誤りとして知らせて出さない。
//
// req: FR-92
func TestUnknownExtensionStatusPart(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "abc\n"})
	h := start(t, Config{
		WorkDir:      dir,
		StatusLine:   &ui.StatusLine{Left: []string{ui.StatusPartName, "nope.part"}},
		TabLineParts: &ui.StatusLine{Right: []string{"nope.part", "nope.tab"}},
	}, "a.txt")
	msg := h.message()
	if !strings.Contains(msg, "設定の誤り 2 件") || !strings.Contains(msg, "部品 nope.part は、本体の部品でも拡張機能の部品") {
		t.Errorf("メッセージ = %q", msg)
	}
}
