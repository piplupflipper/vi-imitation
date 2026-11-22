package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	userconfig "github.com/piplupflipper/vi-imitation/internal/config"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

const testTimeout = 5 * time.Second

// host は、本体の役である。届いた decoration/set と message/show を記録する。
type host struct {
	t    testing.TB
	x    *extension
	conn *rpc.Conn

	mu    sync.Mutex
	decos []ext.DecorationSetParams
	msgs  []ext.MessageShowParams
}

// connect は、x を io.Pipe の上で動かし、apiVersion と settings で initialize を済ませた本体の役を返す。
func connect(t testing.TB, x *extension, apiVersion, settings string) *host {
	t.Helper()
	h := &host{t: t, x: x}
	hostR, extW := io.Pipe()
	extR, hostW := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- x.srv.Serve(context.Background(), extR, extW) }()
	h.conn = rpc.NewConn(hostR, hostW, rpc.HandlerFuncs{
		Notification: func(_ context.Context, n *rpc.Notification) {
			h.mu.Lock()
			defer h.mu.Unlock()
			switch n.Method {
			case ext.MethodDecorationSet:
				var p ext.DecorationSetParams
				if err := json.Unmarshal(n.Params, &p); err != nil {
					t.Errorf("decoration/set: %v", err)
				}
				h.decos = append(h.decos, p)
			case ext.MethodMessageShow:
				var p ext.MessageShowParams
				_ = json.Unmarshal(n.Params, &p)
				h.msgs = append(h.msgs, p)
			}
		},
	})
	t.Cleanup(func() {
		_ = h.conn.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve = %v, want nil", err)
			}
		case <-time.After(testTimeout):
			t.Error("Serve did not return after the host closed")
		}
	})
	params := ext.InitializeParams{APIVersion: apiVersion}
	if settings != "" {
		params.Settings = json.RawMessage(settings)
	}
	var res ext.InitializeResult
	if err := h.call(ext.MethodInitialize, params, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return h
}

func (h *host) call(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	return h.conn.Call(ctx, method, params, result)
}

// notify は、通知を送り、拡張機能がそれを処理し、送り直しの一覧を送り終えるまで待つ（sync）。
func (h *host) notify(method string, params any) {
	h.t.Helper()
	if err := h.conn.Notify(method, params); err != nil {
		h.t.Fatalf("%s: %v", method, err)
	}
	h.sync()
}

// sync は、拡張機能が受け取った通知を処理し終え、送る goroutine が一覧を送り終え、本体の役がそれを記録し終えるまで待つ。
//
// 拡張機能は届いた順に 1 件ずつ処理するので、ping の応答が届けば、前の通知の処理は終わっている。
// その後に送る goroutine が手を空けるのを待ち、もう一度 ping を送る。送る goroutine が書いた通知は、
// 後の ping の応答より前に書かれ、rpc.Conn は届いた順に 1 件ずつハンドラーを呼ぶので、記録も終わっている。
func (h *host) sync() {
	h.t.Helper()
	if err := h.call(ext.MethodPing, nil, nil); err != nil {
		h.t.Fatalf("ping: %v", err)
	}
	deadline := time.Now().Add(testTimeout)
	for !h.x.idle() {
		if time.Now().After(deadline) {
			h.t.Fatal("送る goroutine が送り終えない")
		}
		time.Sleep(100 * time.Microsecond)
	}
	if err := h.call(ext.MethodPing, nil, nil); err != nil {
		h.t.Fatalf("ping: %v", err)
	}
}

// open は、バージョン番号 1 の buffer/open を送る。
func (h *host) open(id ext.BufferID, fileType, text string) {
	h.t.Helper()
	h.notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: id, FileType: fileType, Version: 1, Text: text})
}

func (h *host) change(id ext.BufferID, from ext.Version, edits ...ext.Edit) {
	h.t.Helper()
	h.notify(ext.MethodBufferChange, ext.BufferChangeParams{Buffer: id, FromVersion: from, ToVersion: from + 1, Edits: edits})
}

// sets は、届いた decoration/set の数を返す。
func (h *host) sets() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.decos)
}

// last は、最後に届いた decoration/set を返す。
func (h *host) last() ext.DecorationSetParams {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.decos) == 0 {
		h.t.Fatal("decoration/set が届いていない")
	}
	return h.decos[len(h.decos)-1]
}

// rangesString は、一覧を「行:始まり-終わり bg/fg」を空白で区切った形にする。
func rangesString(p ext.DecorationSetParams) string {
	parts := make([]string, 0, len(p.Decorations))
	for _, d := range p.Decorations {
		s := fmt.Sprintf("%d:%d-%d", d.Range.Start.Line, d.Range.Start.Byte, d.Range.End.Byte)
		if a := d.Attr; a != nil {
			s += fmt.Sprintf(" fg=%s bg=%s sp=%s u=%v", a.Fg, a.Bg, a.Sp, a.Underline)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func edit(sl, sb, el, eb int, text string) ext.Edit {
	return ext.Edit{Range: ext.Range{Start: ext.Position{Line: sl, Byte: sb}, End: ext.Position{Line: el, Byte: eb}}, Text: text}
}

// TestPaintAndIncrementalChange は、対象のファイルの種類のバッファの色コードを背景の色で塗り、
// 編集のたびに変わった行だけを数え直して送り直すことを確かめる。
//
// req: FR-260, FR-261, FR-262
func TestPaintAndIncrementalChange(t *testing.T) {
	x := newExtension()
	h := connect(t, x, ext.APIVersion, `{"fileTypes": ["css"]}`)
	h.open(1, "css", "a { color: #ff0000; }\nb { color: yellow; }\nplain")
	want := "0:11-18 fg=#ffffff bg=#ff0000 sp= u=false, 1:11-17 fg=#000000 bg=#ffff00 sp= u=false"
	if got := rangesString(h.last()); got != want {
		t.Fatalf("開いたとき = %s, want %s", got, want)
	}
	if v := h.last().Version; v != 1 {
		t.Errorf("version = %d, want 1", v)
	}
	scanned := x.scanned.Load()

	// 先頭に行を足すと、範囲のある行がずれるので送り直す。数え直すのは置き換わった 2 行だけ
	h.change(1, 1, edit(0, 0, 0, 0, "x\n"))
	want = "1:11-18 fg=#ffffff bg=#ff0000 sp= u=false, 2:11-17 fg=#000000 bg=#ffff00 sp= u=false"
	if got := rangesString(h.last()); got != want {
		t.Errorf("行を足した後 = %s, want %s", got, want)
	}
	if v := h.last().Version; v != 2 {
		t.Errorf("version = %d, want 2", v)
	}
	if d := x.scanned.Load() - scanned; d != 2 {
		t.Errorf("数え直した行 = %d, want 2", d)
	}

	// 色コードの無い行の中の編集は、数え直すのが 1 行で、一覧が変わらないので送らない
	n := h.sets()
	scanned = x.scanned.Load()
	h.change(1, 2, edit(3, 5, 3, 5, "!"))
	if h.sets() != n {
		t.Errorf("色コードの無い行の編集で送った: %s", rangesString(h.last()))
	}
	if d := x.scanned.Load() - scanned; d != 1 {
		t.Errorf("数え直した行 = %d, want 1", d)
	}

	// 色コードを打つと送る
	h.change(1, 3, edit(3, 0, 3, 0, "#000 "))
	if got := rangesString(h.last()); !strings.HasSuffix(got, "3:0-4 fg=#ffffff bg=#000000 sp= u=false") {
		t.Errorf("色コードを打った後 = %s", got)
	}

	// 色コードを消すと、空の一覧まで送る
	h.change(1, 4, edit(0, 0, 3, 11, ""))
	if got := rangesString(h.last()); got != "" {
		t.Errorf("全部消した後 = %s, want 空", got)
	}
	n = h.sets()
	h.change(1, 5, edit(0, 0, 0, 0, "y"))
	if h.sets() != n {
		t.Error("空の一覧を続けて送った")
	}
}

// TestFileTypes は、設定の fileTypes のファイルの種類のバッファだけを塗り、ファイルの種類が変わったら色を消すことを確かめる。
//
// req: FR-261
func TestFileTypes(t *testing.T) {
	x := newExtension()
	h := connect(t, x, ext.APIVersion, `{"fileTypes": ["css", "lua"]}`)
	h.open(1, "go", "#fff")
	h.open(2, "", "#fff")
	if n := h.sets(); n != 0 {
		t.Fatalf("対象でないバッファに送った: %d", n)
	}
	h.change(1, 1, edit(0, 0, 0, 0, "red "))
	if n := h.sets(); n != 0 {
		t.Fatalf("対象でないバッファの編集で送った: %d", n)
	}
	h.open(3, "lua", "#fff")
	if p := h.last(); p.Buffer != 3 || len(p.Decorations) != 1 {
		t.Fatalf("lua のバッファ = %+v", p)
	}
	// :set filetype=text に当たる buffer/open の送り直し
	h.open(3, "text", "#fff")
	if p := h.last(); p.Buffer != 3 || len(p.Decorations) != 0 {
		t.Errorf("ファイルの種類を変えた後 = %+v, want 空の一覧", p)
	}
	n := h.sets()
	h.change(3, 1, edit(0, 0, 0, 0, "red "))
	if h.sets() != n {
		t.Error("対象から外れたバッファの編集で送った")
	}
}

// TestModes は、塗り方の設定を確かめる。
//
// req: FR-260
func TestModes(t *testing.T) {
	for _, c := range []struct {
		settings, want string
	}{
		{`{"fileTypes": ["css"], "mode": "foreground"}`, "0:0-4 fg=#ffffff bg= sp= u=false"},
		{`{"fileTypes": ["css"], "mode": "underline"}`, "0:0-4 fg= bg= sp=#ffffff u=true"},
		{`{"fileTypes": ["css"], "background": {"brightFg": "#111111", "darkFg": "#eeeeee"}}`, "0:0-4 fg=#111111 bg=#ffffff sp= u=false"},
	} {
		x := newExtension()
		h := connect(t, x, ext.APIVersion, c.settings)
		h.open(1, "css", "#fff")
		if got := rangesString(h.last()); got != c.want {
			t.Errorf("%s: %s, want %s", c.settings, got, c.want)
		}
	}
}

// TestOldHost は、範囲に直の色を付けられない古い本体には一覧を送らず、1 回だけ知らせることを確かめる。
func TestOldHost(t *testing.T) {
	x := newExtension()
	h := connect(t, x, "0.18.0", `{"fileTypes": ["css"]}`)
	h.open(1, "css", "#fff")
	h.open(2, "css", "#000")
	if n := h.sets(); n != 0 {
		t.Errorf("古い本体に送った: %d", n)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.msgs) != 1 {
		t.Errorf("知らせ = %+v, want 1 件", h.msgs)
	}
}

// TestRandomEdits は、乱数の編集（1 回の変更に 1〜3 個の編集）を続けたとき、変更のたびに、本体の役が最後に受け取った一覧が、
// その時点のテキストを全部数え直したものと同じになることを確かめる（送らなかった変更でも、前の一覧のままで正しい）。
//
// req: FR-262
func TestRandomEdits(t *testing.T) {
	x := newExtension()
	h := connect(t, x, ext.APIVersion, `{"fileTypes": ["css"], "rgb": true}`)
	pieces := []string{"#fff", "red", " ", "\n", "x", "rgb(1,2,3)", "#00ff00", "-", "Blue", "\n\n", "#f"}
	r := rand.New(rand.NewPCG(388, 1)) //nolint:gosec // 再現できる無作為な編集の列を作るためで、暗号の用途ではない
	lines := []string{"#abc red", "", "plain #123456 text"}
	h.open(1, "css", strings.Join(lines, "\n"))
	s := newScanner(x.cfg.formats)
	v := ext.Version(1)
	for step := range 300 {
		var edits []ext.Edit
		for range 1 + r.IntN(3) {
			// 行とバイトの位置を選び、範囲を断片の並びで置き換える。位置は前の編集を適用した後のテキストで数える
			sl := r.IntN(len(lines))
			el := min(sl+r.IntN(3), len(lines)-1)
			sb := r.IntN(len(lines[sl]) + 1)
			eb := r.IntN(len(lines[el]) + 1)
			if el == sl && eb < sb {
				sb, eb = eb, sb
			}
			var text strings.Builder
			for range r.IntN(3) {
				text.WriteString(pieces[r.IntN(len(pieces))])
			}
			joined := strings.Join(lines[sl:el+1], "\n")
			startOff, endOff := sb, len(joined)-(len(lines[el])-eb)
			repl := joined[:startOff] + text.String() + joined[endOff:]
			lines = append(append(append([]string{}, lines[:sl]...), strings.Split(repl, "\n")...), lines[el+1:]...)
			edits = append(edits, edit(sl, sb, el, eb, text.String()))
		}
		h.change(1, v, edits...)
		v++
		var want []string
		for i, l := range lines {
			for _, sp := range s.scanLine(l) {
				want = append(want, fmt.Sprintf("%d:%d-%d", i, sp.start, sp.end))
			}
		}
		var got []string
		if h.sets() > 0 {
			for _, d := range h.last().Decorations {
				got = append(got, fmt.Sprintf("%d:%d-%d", d.Range.Start.Line, d.Range.Start.Byte, d.Range.End.Byte))
			}
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%d 回目の変更（%+v）の後の一覧 = %v\nwant %v", step, edits, got, want)
		}
	}
}

// TestConfigExample は、同梱の設定の例の colorizer の設定を読めて、オーナーの nvim の nvim-colorizer.lua の filetypes
// （lua/plugins/colorizer.lua）と同じファイルの種類を対象にし、その種類を本体がファイルの名前から決められることを確かめる。
//
// req: FR-261
func TestConfigExample(t *testing.T) {
	data, err := os.ReadFile("../../config.example.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	ucfg, errs := userconfig.Parse("config.example.jsonc", data)
	if len(errs) != 0 {
		t.Fatalf("設定の例の誤り: %v", errs)
	}
	cfg, err := parseSettings(ucfg.ExtensionSettings["colorizer"])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"css", "scss", "html", "javascript", "typescript", "javascriptreact", "typescriptreact",
		"lua", "vim", "fish", "tmux", "toml", "yaml", "conf",
	}
	if !slices.Equal(cfg.fileTypes, want) {
		t.Errorf("fileTypes = %v, want %v", cfg.fileTypes, want)
	}
	if cfg.formats != defaultFormats() || cfg.mode != modeBackground {
		t.Errorf("形と塗り方 = %+v %v, want nvim-colorizer.lua の既定", cfg.formats, cfg.mode)
	}
	// 本体の既定の表（.js、.ts、.toml、.yaml）に無い種類は、設定の例の fileTypes で決める
	for name, ft := range map[string]string{
		"a.scss": "scss", "a.html": "html", "a.vim": "vim", ".tmux.conf": "tmux", "tmux.conf": "tmux", "a.conf": "conf",
		"a.css": "css", "a.lua": "lua", "a.fish": "fish", "a.tsx": "typescriptreact", "a.jsx": "javascriptreact",
	} {
		got, ok := ucfg.FileTypes[name]
		if !ok {
			got = ucfg.FileTypes[filepath.Ext(name)]
		}
		if got != ft {
			t.Errorf("%s の種類 = %q, want %q", name, got, ft)
		}
	}
}

// TestParseSettings は、設定の誤りを確かめる。
func TestParseSettings(t *testing.T) {
	for _, s := range []string{
		`{"fileTypes": ["css"], "mode": "virtualtext"}`,
		`{"fileTypes": ["css", ""]}`,
		`{"fileTypes": ["css"], "background": {"darkFg": "white"}}`,
		`{"fileTypes": ["css"], "hex": {"rgb": true, "rrggbbaaa": true}}`,
		`{"fileTypes": ["css"], "colors": true}`,
	} {
		if _, err := parseSettings(json.RawMessage(s)); err == nil {
			t.Errorf("parseSettings(%s) = nil, want 誤り", s)
		}
	}
	c, err := parseSettings(json.RawMessage(`{"fileTypes": ["css"], "hex": {"rgb": false, "rrggbbaa": true}, "hsl": true, "names": {"enable": false, "uppercase": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := formats{hexRGBA: true, hexRRGGBB: true, hexRRGGBBAA: true, hslFn: true}
	if c.formats != want {
		t.Errorf("formats = %+v, want %+v", c.formats, want)
	}
	if c, err := parseSettings(nil); err != nil || c.formats != defaultFormats() || c.targets("css") {
		t.Errorf("設定なし = %+v, %v", c, err)
	}
}

// BenchmarkChange は、大きな CSS のバッファの 1 行の中に 1 文字を入れたときの、拡張機能の処理の時間を測る。
// バッファは 10 行に 1 行が 2 つの色コード（#rrggbb と色の名前）を持つ CSS で、#rrggbb は 64 色を使い回す。
// colored は色コードのある行に、plain は色コードの無い行に入れる（plain は一覧が変わらないので送らない）。
// wait=loop は、入力のたびにメッセージを処理する goroutine の処理（変わった行の数え直し）の後の ping の応答まで待つ。
// これが ping の応答の遅れになる。wait=sent は、さらに送る goroutine が一覧を組み立てて符号化し、本体の役が復号するまで待つ。
func BenchmarkChange(b *testing.B) {
	for _, n := range []int{1_000, 100_000} {
		for _, target := range []string{"colored", "plain"} {
			for _, wait := range []string{"loop", "sent"} {
				b.Run(fmt.Sprintf("lines=%d/%s/wait=%s", n, target, wait), func(b *testing.B) {
					benchmarkChange(b, n, target == "colored", wait == "sent")
				})
			}
		}
	}
}

func benchmarkChange(b *testing.B, n int, colored, waitSent bool) {
	var sb strings.Builder
	for i := range n {
		if i%10 == 0 {
			fmt.Fprintf(&sb, ".c%d { color: #%06x; background: white; }\n", i, (i/10%64)*0x030507)
		} else {
			fmt.Fprintf(&sb, ".c%d { margin: %dpx; padding: 0 4px; }\n", i, i%17)
		}
	}
	x := newExtension()
	h := connect(b, x, ext.APIVersion, `{"fileTypes": ["css"]}`)
	h.open(1, "css", strings.TrimSuffix(sb.String(), "\n"))
	v := ext.Version(1)
	line := n / 2
	if !colored {
		line++
	}
	for b.Loop() {
		if err := h.conn.Notify(ext.MethodBufferChange, ext.BufferChangeParams{
			Buffer: 1, FromVersion: v, ToVersion: v + 1, Edits: []ext.Edit{edit(line, 0, line, 0, "a")},
		}); err != nil {
			b.Fatal(err)
		}
		if waitSent {
			h.sync()
		} else if err := h.call(ext.MethodPing, nil, nil); err != nil {
			b.Fatal(err)
		}
		v++
	}
	// 接続を閉じる前に送り終えさせる（閉じた後に送ると、送る goroutine が誤りをログに書く）
	h.sync()
}

// TestBurstOfChanges は、色コードの多いバッファで、送り終える前に変更が続いても、ping の応答が本体の期限（2 秒）より前に届き、
// 送った一覧が最後のバージョン番号のものになる（送っている間の変更をまとめる）ことを確かめる。
// 本体の役は一覧を届いた順に復号してから ping の応答を読むので、-race の下でも復号が期限に収まる大きさ（1 万の範囲）にした。
//
// req: FR-262
func TestBurstOfChanges(t *testing.T) {
	var sb strings.Builder
	for i := range 10_000 {
		fmt.Fprintf(&sb, "a { color: #%06x; }\n", i%64*0x030507)
	}
	x := newExtension()
	h := connect(t, x, ext.APIVersion, `{"fileTypes": ["css"]}`)
	h.open(1, "css", sb.String())
	base := h.sets()
	const n = 200
	start := time.Now()
	for i := range n {
		v := ext.Version(1 + i)
		if err := h.conn.Notify(ext.MethodBufferChange, ext.BufferChangeParams{
			Buffer: 1, FromVersion: v, ToVersion: v + 1, Edits: []ext.Edit{edit(100, 0, 100, 0, "x")},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.call(ext.MethodPing, nil, nil); err != nil {
		t.Fatal(err)
	}
	// 1 回の変更の処理は変わった 1 行の数え直しだけなので、200 回でも ping の期限（2 秒）より短い
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("200 回の変更の後の ping の応答まで %v", d)
	}
	h.sync()
	if p := h.last(); p.Version != 1+n {
		t.Errorf("最後の一覧の version = %d, want %d", p.Version, 1+n)
	}
	if sent := h.sets() - base; sent >= n {
		t.Errorf("送った一覧 = %d 回、変更の回数 %d より少なくまとまっていない", sent, n)
	}
}
