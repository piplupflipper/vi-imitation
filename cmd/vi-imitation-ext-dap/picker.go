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
// early に積むのは、応答を待っている pane/open があるとき（opening が 0 でない）だけにし、pick が受けなかった ID の知らせは
// 一覧を閉じたときに捨てる（取り消しの後の view/closed などを溜め続けないため。#408 のレビューの推奨 6）。
type pickers struct {
	waiting map[ext.ViewID]chan server.ViewEvent
	early   map[ext.ViewID][]server.ViewEvent
	opening int
}

// onView は、本体からの部品の知らせを、その一覧の受け口か、デバッグのペイン（pane.go）へ渡す。ループの上で呼ばれるので待たない。
// ペインを開いている最中（pane/open の応答の前）の知らせは、ペインのものかもしれないので、ペインの列にも積む（ペインは ID で見分ける）。
func (d *debugger) onView(_ context.Context, e *server.ViewEvent) {
	if d.pane.owns(e) {
		// 再生中の c（view/keySync）は、応答をペインの処理の後に回す（ViewEvent.Defer は受けたポインターに対して呼ぶ）。
		reply := e.Defer()
		ev := *e
		d.pane.post(func() { d.pane.onEvent(ev, reply) })
		if !d.pane.opening.Load() {
			return
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if ch, ok := d.pickers.waiting[e.View]; ok {
		select {
		case ch <- *e:
		default:
		}
		return
	}
	if d.pickers.opening > 0 && (e.Kind == server.ViewSelected || e.Kind == server.ViewClosed) {
		d.pickers.early[e.View] = append(d.pickers.early[e.View], *e)
	}
}

// pick は、items を中央のポップアップの一覧に出し、利用者が選んだ項目の ID を返す。
// 閉じたら errPickCancelled を、ctx が終わったら一覧を閉じて ctx.Err() を返す。
// ext/server のループの外から呼ぶ。
func (d *debugger) pick(ctx context.Context, title string, items []ext.ViewItem) (string, error) {
	d.mu.Lock()
	d.pickers.opening++
	d.mu.Unlock()
	res, err := d.srv.OpenPane(ctx, ext.PaneOpenParams{
		Component:     ext.ViewComponent{Kind: ext.ViewList, Title: title, Items: items},
		Placement:     ext.PaneCenter,
		CloseOnSelect: true,
		Focus:         true,
	})
	ch := make(chan server.ViewEvent, 8)
	d.mu.Lock()
	d.pickers.opening--
	var early []server.ViewEvent
	if err == nil {
		early = d.pickers.early[res.View]
		d.pickers.waiting[res.View] = ch
	}
	if d.pickers.opening == 0 {
		clear(d.pickers.early)
	}
	d.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("一覧を開けない: %w", err)
	}
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
			case server.ViewInputChanged, server.ViewKey, server.ViewExpanded, server.ViewTabChanged, server.ViewChildSelected:
				// 一覧では使わない知らせである。
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
		data, err := os.ReadFile(filepath.Join(dir, "cmdline"))
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
