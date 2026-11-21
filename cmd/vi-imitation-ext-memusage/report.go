package main

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// extCommandPrefix は、標準の拡張機能の実行ファイルの名前の前置きである。環境変数を読めないとき、拡張機能の ID の代わりに使う。
const extCommandPrefix = "vi-imitation-ext-"

// maxDepth は、子孫を辿る深さの上限である（仮）。プロセス ID の使い回しで木が輪になっても止まるようにする。
const maxDepth = 32

// maxCommandRunes は、内訳に出すコマンドラインの文字の数の上限である（仮）。超えた分は「…」にする。
const maxCommandRunes = 80

// row は、内訳の 1 行（1 つのプロセス）である。
type row struct {
	pid   int
	rssKB int64
	// depth は、拡張機能のプロセス（本体の子）からの深さである。拡張機能のプロセスは 0、その子（言語サーバーなど）は 1 である。
	depth int
	cmd   string
}

// group は、本体の子 1 つと、その子孫をまとめたものである。本体の子が拡張機能なら、拡張機能 1 つに当たる。
type group struct {
	// name は、拡張機能の ID である。拡張機能でない子（:! で起動したコマンドなど）なら、コマンドの名前である。
	name string
	// ext は、本体の子が拡張機能か（宣言ファイルのディレクトリの環境変数を持つか、実行ファイルの名前が extCommandPrefix で始まるか）である。
	ext   bool
	rssKB int64
	rows  []row
}

// report は、本体（server）と、その子孫の RSS の集計である。
type report struct {
	selfPID int
	selfKB  int64
	selfCmd string
	// groups は、本体の子ごとのまとまりで、RSS の合計の大きい順（同じなら名前の順）に並ぶ。
	groups []group
}

// descendantsKB は、本体の子孫の RSS の合計を返す。
func (r *report) descendantsKB() int64 {
	var n int64
	for _, g := range r.groups {
		n += g.rssKB
	}
	return n
}

// collect は、root（本体の server のプロセス）を根に、src から子孫だけを辿って集計する。
// root を読めなければ ok は false である。子の一覧を読んだ後に終わったプロセスは飛ばす。
//
// req: FR-227
func collect(src source, root int, ids manifestIDs) (r report, ok bool) {
	me, ok := src.info(root)
	if !ok {
		return report{}, false
	}
	r = report{selfPID: root, selfKB: me.rssKB, selfCmd: commandLine(me.args)}
	seen := map[int]bool{root: true}
	for _, child := range src.children(root) {
		if seen[child] {
			continue
		}
		seen[child] = true
		p, ok := src.info(child)
		if !ok {
			continue
		}
		g := group{name: groupName(p, ids), ext: isExtension(p)}
		g.rows = append(g.rows, row{pid: p.pid, rssKB: p.rssKB, cmd: commandLine(p.args)})
		g.rssKB = p.rssKB
		walk(src, child, 1, seen, &g)
		r.groups = append(r.groups, g)
	}
	slices.SortStableFunc(r.groups, func(a, b group) int {
		if c := cmp.Compare(b.rssKB, a.rssKB); c != 0 {
			return c
		}
		return cmp.Compare(a.name, b.name)
	})
	return r, true
}

// walk は、pid の子孫を、深さ優先で g に足す。
func walk(src source, pid, depth int, seen map[int]bool, g *group) {
	if depth > maxDepth {
		return
	}
	for _, child := range src.children(pid) {
		if seen[child] {
			continue
		}
		seen[child] = true
		p, ok := src.info(child)
		if !ok {
			continue
		}
		g.rows = append(g.rows, row{pid: p.pid, rssKB: p.rssKB, depth: depth, cmd: commandLine(p.args)})
		g.rssKB += p.rssKB
		walk(src, child, depth+1, seen, g)
	}
}

// isExtension は、本体の子 p が拡張機能のプロセスかを返す。
func isExtension(p proc) bool {
	return p.extDir != "" || (len(p.args) > 0 && strings.HasPrefix(filepath.Base(p.args[0]), extCommandPrefix))
}

// groupName は、本体の子 p のまとまりの名前を返す。
// 拡張機能なら、宣言ファイルのディレクトリ（環境変数 ext.EnvExtensionDir）の宣言ファイルの ID を使う。
// 環境変数を読めなければ（ps の出力から作ったとき）、実行ファイルの名前から extCommandPrefix を除いたもの（標準の拡張機能の ID と同じ）を使う。
func groupName(p proc, ids manifestIDs) string {
	if p.extDir != "" {
		if id := ids.id(p.extDir); id != "" {
			return id
		}
		return filepath.Base(p.extDir)
	}
	if len(p.args) == 0 {
		return "?"
	}
	name := filepath.Base(p.args[0])
	if s, ok := strings.CutPrefix(name, extCommandPrefix); ok && s != "" {
		return s
	}
	return name
}

// commandLine は、引数の列を内訳に出す 1 行にする。実行ファイルはパスを除いた名前にし、maxCommandRunes 文字で切る。
func commandLine(args []string) string {
	if len(args) == 0 {
		return ""
	}
	s := strings.Join(append([]string{filepath.Base(args[0])}, args[1:]...), " ")
	if utf8.RuneCountInString(s) > maxCommandRunes {
		s = string([]rune(s)[:maxCommandRunes-1]) + "…"
	}
	return s
}

// formatKB は、KiB の量を、MiB を四捨五入した "56M" か、1 GiB 以上なら小数 1 桁の "1.2G" にする（memusage.lua の fmt と同じ）。
func formatKB(kb int64) string {
	mb := float64(kb) / 1024
	if mb >= 1024 {
		return fmt.Sprintf("%.1fG", mb/1024)
	}
	return fmt.Sprintf("%dM", int64(mb+0.5))
}

// statusText は、ステータス行の部品の中身を返す。本体の RSS と、子孫（拡張機能と、拡張機能が起動した言語サーバーなど）の RSS の合計を、
// オーナーの nvim の lualine（"nvim 56M · LSP 390M"）と同じ形で並べる。
//
// req: FR-227
func statusText(r *report) string {
	return fmt.Sprintf("vi %s · ext %s", formatKB(r.selfKB), formatKB(r.descendantsKB()))
}

// detailText は、内訳のポップアップの本文を返す。拡張機能ごとに、拡張機能のプロセスと、それが起動したプロセス（言語サーバー、
// デバッグアダプターなど）を字下げして並べる。
//
// req: FR-228
func detailText(r *report) string {
	var b strings.Builder
	desc := r.descendantsKB()
	fmt.Fprintf(&b, "合計 %s（本体 %s、拡張機能とその子 %s）\n\n", formatKB(r.selfKB+desc), formatKB(r.selfKB), formatKB(desc))
	const nameWidth = 12
	fmt.Fprintf(&b, "%s  %7s  %6s  %s\n", padRight("拡張機能", nameWidth), "PID", "RSS", "コマンド")
	fmt.Fprintf(&b, "%s  %7d  %6s  %s\n", padRight("(本体)", nameWidth), r.selfPID, formatKB(r.selfKB), r.selfCmd)
	for _, g := range r.groups {
		name := g.name
		if !g.ext {
			name = "(" + name + ")"
		}
		for i, rw := range g.rows {
			label := ""
			if i == 0 {
				label = name
			}
			fmt.Fprintf(&b, "%s  %7d  %6s  %s%s\n", padRight(label, nameWidth), rw.pid, formatKB(rw.rssKB), strings.Repeat("  ", rw.depth), rw.cmd)
		}
		if len(g.rows) > 1 {
			fmt.Fprintf(&b, "%s  %7s  %6s  %s\n", padRight("", nameWidth), "", formatKB(g.rssKB), "（"+g.name+" の合計）")
		}
	}
	b.WriteString("\nRSS は共有ライブラリを重ねて数えるので、PSS より少し大きい")
	return b.String()
}

// padRight は、s の後ろに空白を足して、表示の幅を w にする。全角の文字（U+1100 以上）は幅 2 と数える（見出しの日本語のための近似）。
func padRight(s string, w int) string {
	n := 0
	for _, c := range s {
		if c >= 0x1100 {
			n += 2
		} else {
			n++
		}
	}
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}
