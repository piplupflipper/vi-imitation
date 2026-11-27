// Package muxtest は、internal/mux の herdr app の実装（mux.HerdrApp）を試すための、偽の hc と、
// herdr app の mod（internal/mux/herdr-app-mod/main.js）の依頼の処理を真似た偽の係である（#440）。テストからだけ使う。
//
// 偽の hc は、テストの実行ファイルそのものを hc という名前のシンボリックリンクから起動したものである。
// テストのパッケージの TestMain の最初で RunIfHC を呼ぶと、hc として起動されたときだけ偽の hc として動いて終わる。
// ラベルは、環境変数 FAKE_HC_DIR のディレクトリのファイルに覚える。本物の hc（herdr-core）には繋がない。
//
// このパッケージは internal/mux を import しない（mux のテストからも使うため）。依頼と結果の JSON の形は、mod と同じものをここにも書く。
package muxtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// EnvDir は、偽の hc がラベルと呼び出しの記録を置くディレクトリを渡す環境変数の名前である。
const EnvDir = "FAKE_HC_DIR"

// ラベルの名前は mod（herdr-app-mod/main.js）と internal/mux と同じである。
const (
	requestPrefix = "vi-imitation.req."
	replyPrefix   = "vi-imitation.res."
	claimPrefix   = "vi-imitation.claim."
	pendingLabel  = "vi-imitation.pending"
)

// HC は、偽の hc である。
type HC struct {
	// Dir は、ラベルと呼び出しの記録を置くディレクトリ（EnvDir）である。
	Dir string
	// BinDir は、hc という名前のシンボリックリンクを置いたディレクトリである。PATH の先頭に足す。
	BinDir string

	mu     sync.Mutex
	served []Served
}

// Served は、偽の係が処理した依頼と、依頼のあった端末である。
type Served struct {
	Terminal string
	Request  Request
}

// Served は、偽の係（ServeMod）が処理した依頼を、処理した順に返す。
func (h *HC) Served() []Served {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.served)
}

// Killed は、偽の hc が kill で終わらせた端末の番地を、受けた順に返す。
func (h *HC) Killed(t *testing.T) []string {
	t.Helper()
	var killed []string
	err := withStore(h.Dir, func(s *store) error {
		killed = slices.Clone(s.Killed)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return killed
}

// Install は、偽の hc を用意し、PATH の先頭と EnvDir を t.Setenv で設定する。
// 呼んだテストのパッケージの TestMain は RunIfHC を呼んでいなければならない。
func Install(t *testing.T) *HC {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := &HC{Dir: t.TempDir(), BinDir: t.TempDir()}
	if err := os.Symlink(exe, filepath.Join(h.BinDir, "hc")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", h.BinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvDir, h.Dir)
	// -race で作ったテストの実行ファイルは、終わるときに既定で 1 秒待つ。偽の hc の 1 回の呼び出しが 1 秒かからないように、待たせない。
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	return h
}

// RunIfHC は、このプロセスが hc という名前で起動されていれば、偽の hc として動いて os.Exit で終わる。
// そうでなければ何もしない。テストのパッケージの TestMain の最初で呼ぶ。
func RunIfHC() {
	if filepath.Base(os.Args[0]) != "hc" {
		return
	}
	if err := runHC(os.Getenv(EnvDir), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Call は、偽の hc の 1 回の呼び出しの引数である。
type Call []string

// Calls は、偽の hc が受けた呼び出しを、受けた順に返す。
func (h *HC) Calls(t *testing.T) []Call {
	t.Helper()
	var calls []Call
	err := withStore(h.Dir, func(s *store) error {
		calls = slices.Clone(s.Calls)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

// Labels は、端末 terminal の今のラベルを返す。
func (h *HC) Labels(t *testing.T, terminal string) map[string]string {
	t.Helper()
	var labels map[string]string
	err := withStore(h.Dir, func(s *store) error {
		labels = maps.Clone(s.Labels[terminal])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return labels
}

// SetLabels は、端末 terminal にラベルを付ける（herdr app が付ける herdr.tab など）。
func (h *HC) SetLabels(t *testing.T, terminal string, labels map[string]string) {
	t.Helper()
	err := withStore(h.Dir, func(s *store) error {
		s.terminal(terminal)
		maps.Copy(s.Labels[terminal], labels)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Request は、mod への依頼である（internal/mux の herdrAppRequest と同じ形）。
type Request struct {
	ID        string   `json:"id"`
	Op        string   `json:"op"`
	Direction string   `json:"direction,omitempty"`
	Argv      []string `json:"argv,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	Width     int      `json:"width,omitempty"`
	Height    int      `json:"height,omitempty"`
	Focus     bool     `json:"focus"`
	Terminal  string   `json:"terminal,omitempty"`
}

// Reply は、mod が書き返す結果である（internal/mux の herdrAppReply と同じ形）。
type Reply struct {
	ID         string   `json:"id"`
	Error      string   `json:"error,omitempty"`
	Terminal   string   `json:"terminal,omitempty"`
	FocusError string   `json:"focus_error,omitempty"`
	Focused    bool     `json:"focused,omitempty"`
	Busy       bool     `json:"busy,omitempty"`
	Ops        []string `json:"ops,omitempty"`
}

// ServeMod は、mod の依頼の処理を真似た偽の係を、テストが終わるまで別の goroutine で動かす。
// 係は、印のラベルのある端末の依頼のラベルを読み、handle の結果を結果のラベルとして書き返し、依頼のラベルを消す。
// 依頼が残っていなければ印のラベルも消す（mod と同じ手順）。handle は係の goroutine から呼ぶ。
func (h *HC) ServeMod(t *testing.T, handle func(terminal string, req Request) Reply) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	go func() {
		defer close(done)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			// 依頼をロックの下で取り出し、handle はロックの外で呼ぶ（handle の中で偽の hc を動かせるように。#444）。
			type job struct {
				terminal, id, raw string
			}
			var jobs []job
			_ = withStore(h.Dir, func(s *store) error {
				for _, terminal := range slices.Sorted(maps.Keys(s.Labels)) {
					labels := s.Labels[terminal]
					if labels[pendingLabel] != "1" {
						continue
					}
					for _, key := range slices.Sorted(maps.Keys(labels)) {
						if id, ok := strings.CutPrefix(key, requestPrefix); ok {
							jobs = append(jobs, job{terminal, id, labels[key]})
							delete(labels, key)
							// mod と同じく、処理する間は引き受けの印を付けておく。
							labels[claimPrefix+id] = "fake"
						}
					}
					delete(labels, pendingLabel)
				}
				return nil
			})
			for _, j := range jobs {
				var req Request
				rep := Reply{ID: j.id}
				if err := json.Unmarshal([]byte(j.raw), &req); err != nil {
					rep.Error = err.Error()
				} else {
					h.mu.Lock()
					h.served = append(h.served, Served{Terminal: j.terminal, Request: req})
					h.mu.Unlock()
					rep = handle(j.terminal, req)
					rep.ID = j.id
				}
				b, _ := json.Marshal(rep)
				_ = withStore(h.Dir, func(s *store) error {
					s.terminal(j.terminal)[replyPrefix+j.id] = string(b)
					delete(s.terminal(j.terminal), claimPrefix+j.id)
					return nil
				})
			}
		}
	}()
}

// splitLabel は、split で開いた新しい端末が自分に付けるラベルの名前である（internal/mux の HerdrAppSplitLabel と同じ。#444）。
const splitLabel = "vi-imitation.split"

// RunSplitScript は、split の依頼の argv を、新しいペインの端末 terminal の中で動いたものとして sh で動かす（HC_TERMINAL_ID に terminal）。
// 起動するコマンド（argv の env と環境変数の後ろ）は true に替える。
// 包んだスクリプトは、偽の hc で自分の端末にラベルを付け、コマンドの後に自分の端末を hc kill する。
// herdr app の mod が pane.split で開いた端末の中で、本体の包んだスクリプトが動くことを真似る。偽の係の handle から呼ぶ。
func (h *HC) RunSplitScript(t *testing.T, argv []string, terminal string) {
	t.Helper()
	i := slices.Index(argv, "env")
	if i < 0 {
		t.Errorf("argv = %q, want env を含む", argv)
		return
	}
	i++
	for i < len(argv) && strings.Contains(argv[i], "=") {
		i++
	}
	args := append(slices.Clone(argv[:i]), "true")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // テストで包んだスクリプトを動かす
	cmd.Env = append(os.Environ(), "HC_TERMINAL_ID="+terminal)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("包んだスクリプト: %v\n%s", err, out)
	}
}

// FindSplit は、依頼の番号 id のラベル（splitLabel）を付けた端末の番地を返す。無ければ空を返す。
// mod が hc ls --label vi-imitation.split=<id> で新しい端末を探すのに当たる。
func (h *HC) FindSplit(t *testing.T, id string) string {
	t.Helper()
	var found string
	err := withStore(h.Dir, func(s *store) error {
		for _, term := range slices.Sorted(maps.Keys(s.Labels)) {
			if s.Labels[term][splitLabel] == id {
				found = term
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// store は、偽の hc の状態（ファイル store.json）である。
type store struct {
	// Labels は、端末の番地ごとのラベルである。
	Labels map[string]map[string]string `json:"labels"`
	// Calls は、受けた呼び出しの引数である。
	Calls []Call `json:"calls"`
	// Killed は、kill で終わらせた端末の番地である。
	Killed []string `json:"killed"`
}

func (s *store) terminal(t string) map[string]string {
	if s.Labels == nil {
		s.Labels = map[string]map[string]string{}
	}
	if s.Labels[t] == nil {
		s.Labels[t] = map[string]string{}
	}
	return s.Labels[t]
}

// withStore は、dir の状態をファイルのロックの下で読み、f を呼んでから書き戻す。
func withStore(dir string, f func(*store) error) error {
	if dir == "" {
		return errors.New("fake hc: " + EnvDir + " is not set")
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // テストの一時ディレクトリのファイル
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	path := filepath.Join(dir, "store.json")
	var s store
	b, err := os.ReadFile(path) //nolint:gosec // テストの一時ディレクトリのファイル
	switch {
	case err == nil:
		if err = json.Unmarshal(b, &s); err != nil {
			return err
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err = f(&s); err != nil {
		return err
	}
	if b, err = json.Marshal(&s); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600) //nolint:gosec // テストの一時ディレクトリのファイル
}

// runHC は、偽の hc の 1 回の呼び出しである。label、get、ls、kill を受ける。
func runHC(dir string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("fake hc: no command")
	}
	var res any
	err := withStore(dir, func(s *store) error {
		s.Calls = append(s.Calls, slices.Clone(args))
		switch args[0] {
		case "label":
			if len(args) < 3 {
				return errors.New("fake hc label: want a terminal and k=v")
			}
			labels := s.terminal(args[1])
			for _, kv := range args[2:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("fake hc label: %q is not k=v", kv)
				}
				if v == "" {
					delete(labels, k)
				} else {
					labels[k] = v
				}
			}
			return nil
		case "get":
			if len(args) < 2 {
				return errors.New("fake hc get: want <terminal>")
			}
			res = map[string]any{"address": args[1], "labels": s.terminal(args[1])}
			return nil
		case "ls":
			var want []string
			for i := 1; i < len(args); i++ {
				if args[i] == "--label" && i+1 < len(args) {
					want = append(want, args[i+1])
					i++
				}
			}
			terms := []map[string]any{}
			for _, t := range slices.Sorted(maps.Keys(s.Labels)) {
				if hasAll(s.Labels[t], want) {
					terms = append(terms, map[string]any{"address": t})
				}
			}
			res = map[string]any{"terminals": terms}
			return nil
		case "kill":
			if len(args) < 2 {
				return errors.New("fake hc kill: want <terminal>")
			}
			// ラベルは消さずに残す（終わった端末のラベルを、テストが後から読めるように）。
			s.Killed = append(s.Killed, args[1])
			return nil
		}
		return fmt.Errorf("fake hc: unknown command %q", args[0])
	})
	if err != nil || res == nil {
		return err
	}
	return json.NewEncoder(out).Encode(res)
}

// hasAll は、labels が want の k=v をすべて持つかを返す。
func hasAll(labels map[string]string, want []string) bool {
	for _, kv := range want {
		k, v, _ := strings.Cut(kv, "=")
		if labels[k] != v {
			return false
		}
	}
	return true
}
