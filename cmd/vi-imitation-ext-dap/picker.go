package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// この節は、中央のポップアップの一覧で利用者に 1 つ選ばせる口（構成の候補と ${pickProcess}。FR-280、FR-299）である。
// 一覧は pane/open の center と closeOnSelect で開き（基本設計 6.4）、view/select か view/closed で結果を受ける。
// choice/ask は番号のキーで選ぶ 9 つまでの問いで、プロセスの一覧のような長い一覧に向かないので使わない。

// errPickCancelled は、利用者が一覧を選ばずに閉じたことを表す。
var errPickCancelled = errors.New("選ばずに閉じた")

// pickers は、開いている一覧の、部品の ID から知らせの受け口への対応である。
// pane/open の応答より先に届いた知らせは early に積み、受け口を置いたときに渡す（ファイラの拡張機能と同じ）。
type pickers struct {
	waiting map[ext.ViewID]chan server.ViewEvent
	early   map[ext.ViewID][]server.ViewEvent
}

// onView は、本体からの部品の知らせを、その一覧の受け口へ渡す。ループの上で呼ばれるので待たない。
func (d *debugger) onView(_ context.Context, e *server.ViewEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ch, ok := d.pickers.waiting[e.View]; ok {
		select {
		case ch <- *e:
		default:
		}
		return
	}
	if e.Kind == server.ViewSelected || e.Kind == server.ViewClosed {
		d.pickers.early[e.View] = append(d.pickers.early[e.View], *e)
	}
}

// pick は、items を中央のポップアップの一覧に出し、利用者が選んだ項目の ID を返す。
// 閉じたら errPickCancelled を、ctx が終わったら一覧を閉じて ctx.Err() を返す。
// ext/server のループの外から呼ぶ。
func (d *debugger) pick(ctx context.Context, title string, items []ext.ViewItem) (string, error) {
	res, err := d.srv.OpenPane(ctx, ext.PaneOpenParams{
		Component:     ext.ViewComponent{Kind: ext.ViewList, Title: title, Items: items},
		Placement:     ext.PaneCenter,
		CloseOnSelect: true,
		Focus:         true,
	})
	if err != nil {
		return "", fmt.Errorf("一覧を開けない: %w", err)
	}
	ch := make(chan server.ViewEvent, 8)
	d.mu.Lock()
	early := d.pickers.early[res.View]
	delete(d.pickers.early, res.View)
	d.pickers.waiting[res.View] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.pickers.waiting, res.View)
		d.mu.Unlock()
	}()
	for _, e := range early {
		ch <- e
	}
	for {
		select {
		case e := <-ch:
			switch e.Kind {
			case server.ViewSelected:
				return e.Item, nil
			case server.ViewClosed:
				return "", errPickCancelled
			default:
			}
		case <-ctx.Done():
			_ = d.srv.CloseView(res.View)
			return "", ctx.Err()
		}
	}
}

// process は、${pickProcess} の一覧の 1 つのプロセスである。
type process struct {
	pid     int
	cmdline string
}

// maxCmdlineRunes は、一覧に出すコマンドラインの文字数の上限である（仮）。
const maxCmdlineRunes = 200

// listProcesses は、/proc から、拡張機能と同じ利用者のプロセス（拡張機能のプロセスを除く）を、プロセス ID の順に返す。
// コマンドラインの読めないプロセス（カーネルのスレッド、読む間に終わったもの）は除く。
// /proc の無い OS では誤りを返す。
func listProcesses() ([]process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("プロセスの一覧を読めない: %w", err)
	}
	uid := os.Getuid()
	self := os.Getpid()
	var out []process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		dir := filepath.Join("/proc", e.Name())
		fi, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "cmdline")) //nolint:gosec // /proc の決まったファイルを読む
		if err != nil || len(data) == 0 {
			continue
		}
		cmdline := strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
		if r := []rune(cmdline); len(r) > maxCmdlineRunes {
			cmdline = string(r[:maxCmdlineRunes]) + "…"
		}
		out = append(out, process{pid: pid, cmdline: cmdline})
	}
	slices.SortFunc(out, func(a, b process) int { return a.pid - b.pid })
	return out, nil
}

// pickProcess は、${pickProcess} の値を決める。利用者のプロセスの一覧を中央のポップアップに出し、選んだプロセス ID を 10 進の文字列で返す。
// 取り消したら errPickCancelled を返す（始めない）。
//
// req: FR-299
func (d *debugger) pickProcess(ctx context.Context) (string, error) {
	procs, err := listProcesses()
	if err != nil {
		return "", err
	}
	if len(procs) == 0 {
		return "", errors.New("選べるプロセスが無い")
	}
	items := make([]ext.ViewItem, len(procs))
	for i, p := range procs {
		id := strconv.Itoa(p.pid)
		items[i] = ext.ViewItem{ID: id, Label: id + "  " + p.cmdline}
	}
	return d.pick(ctx, "attach するプロセス", items)
}
