package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tailscale/hujson"

	"github.com/piplupflipper/vi-imitation/ext"
)

// proc は、1 つのプロセスの、集計に使う情報である。
type proc struct {
	pid int
	// rssKB は、常駐の大きさ（RSS）で、単位は KiB である。カーネルのスレッドとゾンビは 0 である。
	rssKB int64
	// args は、コマンドラインの引数の列である。読めなければ、プロセスの名前を [ ] で囲んだもの 1 つである。
	args []string
	// extDir は、本体が拡張機能の子プロセスに渡す環境変数 ext.EnvExtensionDir の値である。拡張機能でないか、読めなければ空。
	extDir string
}

// source は、プロセスの木を読む先である（Linux の /proc か、ps の出力）。
type source interface {
	// children は、pid の子のプロセス ID の列を返す。
	children(pid int) []int
	// info は、pid のプロセスの情報を返す。読めない（終わった）プロセスなら ok は false である。
	info(pid int) (p proc, ok bool)
}

// procSource は、Linux の /proc から、根の子孫だけを読む source である。
// /proc/<pid>/task/<tid>/children（Linux 3.5 以降の CONFIG_PROC_CHILDREN）で子を辿るので、全プロセスの一覧を作らない。
// オーナーの nvim の memusage.lua が、ps -A の出力を 5 秒ごとに読んでヒープが膨らんだ反省と同じ形である（#367）。
type procSource struct {
	// root は、/proc のディレクトリである。テストでは偽の木を指す。
	root string
}

// hasProcChildren は、/proc から子のプロセスを辿れるかを返す。
func hasProcChildren() bool {
	pid := os.Getpid()
	_, err := os.Stat(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
	return err == nil
}

func (s procSource) children(pid int) []int {
	base := filepath.Join(s.root, strconv.Itoa(pid), "task")
	tasks, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []int
	for _, t := range tasks {
		data, err := os.ReadFile(filepath.Join(base, t.Name(), "children"))
		if err != nil {
			continue
		}
		for f := range strings.FieldsSeq(string(data)) {
			if n, err := strconv.Atoi(f); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func (s procSource) info(pid int) (proc, bool) {
	dir := filepath.Join(s.root, strconv.Itoa(pid))
	status, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return proc{}, false
	}
	p := proc{pid: pid}
	var name string
	for l := range strings.SplitSeq(string(status), "\n") {
		if v, ok := strings.CutPrefix(l, "VmRSS:"); ok {
			// "VmRSS:	  123456 kB" の形である。カーネルのスレッドとゾンビには、この行が無い。
			if f := strings.Fields(v); len(f) > 0 {
				p.rssKB, _ = strconv.ParseInt(f[0], 10, 64)
			}
		} else if v, ok := strings.CutPrefix(l, "Name:"); ok {
			name = strings.TrimSpace(v)
		}
	}
	if cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		for a := range strings.SplitSeq(strings.TrimRight(string(cmdline), "\x00"), "\x00") {
			if a != "" {
				p.args = append(p.args, a)
			}
		}
	}
	if len(p.args) == 0 {
		p.args = []string{"[" + name + "]"}
	}
	// 環境変数は、同じ利用者のプロセスなら読める。読めなければ、拡張機能かどうかをコマンドの名前で見分ける（groupName）。
	if env, err := os.ReadFile(filepath.Join(dir, "environ")); err == nil {
		for kv := range strings.SplitSeq(string(env), "\x00") {
			if v, ok := strings.CutPrefix(kv, ext.EnvExtensionDir+"="); ok {
				p.extDir = v
				break
			}
		}
	}
	return p, true
}

// psTimeout は、ps の実行を待つ上限である（仮）。
const psTimeout = 5 * time.Second

// psSource は、/proc から子を辿れない環境（Linux 以外）で、ps の出力から作る source である。
// ps -A は全プロセスを並べるので、1 回ごとに全プロセスの数の大きさのデータを作っては捨てる。
// Go の GC で回収されるので、memusage.lua の LuaJIT のヒープのようには膨らまないと見込んで（計測はしていない）、この形で済ませる。
// ps は環境変数を出さないので、extDir は空である。
type psSource struct {
	procs map[int]proc
	kids  map[int][]int
}

// newPSSource は、ps -A -o pid=,ppid=,rss=,args= を実行して、その出力から source を作る。
// macOS と BSD の ps と、Linux の procps の ps が、どれもこの書き方を受け付ける。
func newPSSource(ctx context.Context) (*psSource, error) {
	ctx, cancel := context.WithTimeout(ctx, psTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,rss=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps を実行できない: %w", err)
	}
	return parsePS(out, os.Getpid()), nil
}

// parsePS は、ps の出力を読む。self（集計のために ps を起動したプロセス）の子の ps 自身は数えない。
func parsePS(out []byte, self int) *psSource {
	s := &psSource{procs: map[int]proc{}, kids: map[int][]int{}}
	type row struct{ pid, ppid int }
	var rows []row
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		rss, err3 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		if ppid == self && filepath.Base(f[3]) == "ps" {
			continue
		}
		s.procs[pid] = proc{pid: pid, rssKB: rss, args: f[3:]}
		rows = append(rows, row{pid, ppid})
	}
	for _, r := range rows {
		s.kids[r.ppid] = append(s.kids[r.ppid], r.pid)
	}
	return s
}

func (s *psSource) children(pid int) []int { return s.kids[pid] }

func (s *psSource) info(pid int) (proc, bool) {
	p, ok := s.procs[pid]
	return p, ok
}

// manifestIDs は、宣言ファイルのディレクトリから拡張機能の ID への対応を覚える。拡張機能のプロセスは入れ替わるが、ディレクトリは変わらない。
type manifestIDs map[string]string

// id は、ディレクトリ dir の宣言ファイル manifest.json の id を返す。読めなければ空を返す（覚えておき、読み直さない）。
func (m manifestIDs) id(dir string) string {
	if id, ok := m[dir]; ok {
		return id
	}
	var id string
	if data, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
		if std, err := hujson.Standardize(data); err == nil {
			var v struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(std, &v) == nil {
				id = v.ID
			}
		}
	}
	m[dir] = id
	return id
}
