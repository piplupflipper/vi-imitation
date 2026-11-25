//go:build linux

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、行末の変数の値（virtualtext.go。FR-296）のテストである。

// vtGoSrc は、行末の変数の値のテストの Go のソースである。行の番号（0 から）はテストの期待値に使う。
const vtGoSrc = `package main

func helper(x int) {
	y := x
	_ = y
}

func main() {
	x := 0
	s := "long"
	値 := "日本語"
	big := make([]int, 3000)
	// i を回す
	for i := 0; i < 3; i++ {
		x++
		fmt.Println(x, s, 値, big[0], obj.s)
	}
	go func() {
		s := 1
		_ = s
	}()
}`

// vtPath は、vtGoSrc を開くパスである。
const vtPath = "/src/app/vt.go"

// vtLines は、仮想テキストの一覧を「行:バイト:置き方:文字列:色」の行にする。
func vtLines(vts []ext.VirtualText) string {
	var b strings.Builder
	for _, v := range vts {
		for _, c := range v.Chunks {
			fmt.Fprintf(&b, "%d:%d:%s:%s:%s\n", v.Position.Line, v.Position.Byte, v.Placement, c.Text, c.Style)
		}
	}
	return b.String()
}

// TestPlaceVirtualTexts は、名前の一致で値を出す行を決める規則（止まった行を含む関数の範囲で、名前が識別子として最初に現れる位置。
// 入れ子の関数の本体、コメントの行、メンバーの参照は数えない）と、文字列の形（eol と inline、commented、区切りの ,、空白の並びと
// 改行を空白 1 つにする、長い値を切る、変わった値の色、多バイト文字のバイトの位置）を確かめる。
//
// req: FR-296
func TestPlaceVirtualTexts(t *testing.T) {
	lines := strings.Split(vtGoSrc, "\n")
	eol := vtSettings{enabled: true, placement: ext.VirtualTextEOL, maxLength: 20, highlightChanged: true}
	vars := []vtVar{
		{name: "x", value: "3"}, {name: "big", value: "[]int len: 3000"}, {name: "s", value: strings.Repeat("long ", 400)},
		{name: "nested", value: "{...}"}, {name: "値", value: "日本語\n二行目"}, {name: "i", value: "2"},
	}
	got := vtLines(placeVirtualTexts(lines, 14, vars, eol, "// %s", map[string]bool{"x": true}))
	// helper の x（2 行目）と go func の s（18 行目）は範囲の外か入れ子の関数の中、12 行目のコメントの i と 15 行目の obj.s は数えない。
	want := "8:1:eol:// x = 3:diagnostic.warning\n" +
		"9:1:eol:// s = long long long long …:comment\n" +
		"10:1:eol:// 値 = 日本語 二行目:comment\n" +
		"11:1:eol:// big = []int len: 3000:comment\n" +
		"13:5:eol:// i = 2:comment\n"
	if got != want {
		t.Errorf("eol =\n%s\nwant\n%s", got, want)
	}

	// inline は、名前の直後に「 = 値」を出す。同じ行の区切りは付けない。値の改行は空白にする。maxLength が 0 なら区画と同じ上限で切る。
	inline := vtSettings{enabled: true, placement: ext.VirtualTextInline}
	got = vtLines(placeVirtualTexts(lines, 3, []vtVar{{name: "x", value: "1"}, {name: "y", value: "a\nb"}, {name: "s", value: strings.Repeat("あ", 600)}}, inline, "", nil))
	want = "2:13:inline: = 1:comment\n3:2:inline: = a b:comment\n"
	if got != want {
		t.Errorf("inline in helper =\n%s\nwant\n%s", got, want)
	}
	long := placeVirtualTexts(lines, 18, []vtVar{{name: "s", value: strings.Repeat("あ", 600)}}, inline, "", nil)
	if len(long) != 1 || long[0].Position != (ext.Position{Line: 18, Byte: 3}) || len([]rune(long[0].Chunks[0].Text)) != len(" = ")+maxLabelRunes+1 {
		t.Errorf("closure s = %+v", long)
	}

	// Python のメソッドの中。self と n は def の行に並び（, で区切る）、メンバーの参照の self.total は total に数えない。
	// 前のメソッドの self と、関数の外の行は使わない。
	py := strings.Split(`import os

class Counter:
    def __init__(self):
        self.total = 0

    def add(self, n):
        total = self.total
        for i in range(n):
            total += i
        self.total = total
        return total`, "\n")
	pyVars := []vtVar{{name: "self", value: "<Counter>"}, {name: "n", value: "3"}, {name: "total", value: "1"}, {name: "i", value: "1"}}
	got = vtLines(placeVirtualTexts(py, 9, pyVars, eol, "# %s", nil))
	want = "6:12:eol:# self = <Counter>,:comment\n6:18:eol:# n = 3:comment\n7:8:eol:# total = 1:comment\n8:12:eol:# i = 1:comment\n"
	if got != want {
		t.Errorf("python method =\n%s\nwant\n%s", got, want)
	}

	// 関数の外（トップレベル）で止まったら、ファイルの全体を見るが、関数の見出しと本体は飛ばす。
	top := strings.Split("def f(a):\n    b = a\n    return b\n\na = 1\nb = f(a)", "\n")
	got = vtLines(placeVirtualTexts(top, 5, []vtVar{{name: "a", value: "1"}, {name: "b", value: "1"}}, eol, "", nil))
	if want = "4:0:eol:a = 1:comment\n5:0:eol:b = 1:comment\n"; got != want {
		t.Errorf("top level =\n%s\nwant\n%s", got, want)
	}

	// JavaScript のクラスのメソッドの見出しと、入れ子の矢印の関数の本体。
	js := strings.Split("class A {\n  run(items) {\n    let sum = 0;\n    items.forEach((it) => {\n      sum += it;\n    });\n    return sum;\n  }\n}", "\n")
	got = vtLines(placeVirtualTexts(js, 6, []vtVar{{name: "items", value: "Array(2)"}, {name: "sum", value: "3"}, {name: "it", value: "2"}}, eol, "", nil))
	if want = "1:6:eol:items = Array(2):comment\n2:8:eol:sum = 3:comment\n"; got != want {
		t.Errorf("javascript =\n%s\nwant\n%s", got, want)
	}
}

// TestParseVTSettings は、設定の virtualText の既定と誤りを確かめる。
//
// req: FR-296
func TestParseVTSettings(t *testing.T) {
	s, err := parseSettings(nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.vt.enabled || s.vt.placement != ext.VirtualTextInline || s.vt.commented || s.vt.maxLength != 0 || !s.vt.highlightChanged {
		t.Errorf("default = %+v", s.vt)
	}
	s, err = parseSettings([]byte(`{"virtualText": {"enabled": true, "position": "eol", "commented": true, "maxLength": 80, "highlightChanged": false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := (vtSettings{enabled: true, placement: ext.VirtualTextEOL, commented: true, maxLength: 80}); s.vt != want {
		t.Errorf("vt = %+v, want %+v", s.vt, want)
	}
	for _, bad := range []string{
		`{"virtualText": true}`,
		`{"virtualText": {"enabled": "yes"}}`,
		`{"virtualText": {"position": "overlay"}}`,
		`{"virtualText": {"maxLength": -1}}`,
		`{"virtualText": {"maxLength": 1.5}}`,
		`{"virtualText": {"nope": 1}}`,
	} {
		if _, err := parseSettings([]byte(bad)); err == nil {
			t.Errorf("%s: nil error", bad)
		}
	}
}

// vtHost は、テスト用のアダプター（launch の fake の項目が fake）と、設定の virtualText（vt。nil なら書かない）と ui（nil なら書かない）で
// 拡張機能を初期化し、vtGoSrc を番号 1 のバッファで開いた本体の役を返す。
func vtHost(t *testing.T, fake, vt, ui map[string]any) *host {
	t.Helper()
	if fake == nil {
		fake = map[string]any{}
	}
	if _, ok := fake["extraVars"]; !ok {
		fake["extraVars"] = []any{map[string]any{"name": "値", "value": "日本語\n二行目", "variablesReference": 0}}
	}
	s := map[string]any{
		"adapters":       map[string]any{"fake": fakeDef(transportStdio)},
		"configurations": map[string]any{"go": []any{map[string]any{"name": "parent", "type": "fake", "request": "launch", "fake": fake}}},
	}
	if vt != nil {
		s["virtualText"] = vt
	}
	if ui != nil {
		s["ui"] = ui
	}
	h := newHost(t, mustJSON(t, s))
	h.openText(1, vtPath, "go", vtGoSrc)
	return h
}

// openText は、中身が text でファイルの種類が fileType のバッファを、番号 id で開く。
func (h *host) openText(id ext.BufferID, path, fileType, text string) {
	h.t.Helper()
	h.mu.Lock()
	h.versions[id] = 1
	h.ids[path] = id
	h.mu.Unlock()
	if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: id, Path: path, FileType: fileType, Version: 1, Text: text}); err != nil {
		h.t.Fatalf("buffer/open: %v", err)
	}
}

// waitVT は、バッファ buf の virtualText/set のうち、ok を満たすものが届くまで待つ。途中のものは seen に足す（nil なら捨てる）。
func (h *host) waitVT(buf ext.BufferID, ok func(ext.VirtualTextSetParams) bool, seen *[]ext.VirtualTextSetParams) ext.VirtualTextSetParams {
	h.t.Helper()
	deadline := time.After(testTimeout)
	var last string
	for {
		select {
		case p := <-h.vtexts:
			if p.Buffer != buf {
				if seen != nil {
					*seen = append(*seen, p)
				}
				continue
			}
			if ok(p) {
				return p
			}
			if seen != nil {
				*seen = append(*seen, p)
			}
			last = vtLines(p.VirtualTexts)
		case <-deadline:
			h.t.Fatalf("virtualText/set for buffer %d did not arrive; last = %q", buf, last)
			return ext.VirtualTextSetParams{}
		}
	}
}

// vtHas は、一覧に文字列 text の仮想テキストがあるかを返す。
func vtHas(text string) func(ext.VirtualTextSetParams) bool {
	return func(p ext.VirtualTextSetParams) bool { return strings.Contains(vtLines(p.VirtualTexts), text) }
}

// vtEmpty は、一覧が空かを返す。
func vtEmpty(p ext.VirtualTextSetParams) bool { return len(p.VirtualTexts) == 0 }

// drainOutputs は、届いているデバッグ対象の出力を、少し待ってから読み捨てずにつないで返す。
func (h *host) drainOutputs(wait time.Duration) string {
	var b strings.Builder
	deadline := time.After(wait)
	for {
		select {
		case s := <-h.outputs:
			b.WriteString(s)
		case <-deadline:
			return b.String()
		}
	}
}

// TestVirtualText は、止まったときに、ペインを閉じたまま、選んでいるフレームの変数の値を行末に出し（コメントの記号は option/get で
// 1 回だけ読む）、ステップ実行で値が替わり（前の停止から変わった値を diagnostic.warning にし、間に消さない）、スコープの区画を出しても
// scopes と variables を 2 回送らず、終了で消すことを確かめる。
//
// req: FR-296
func TestVirtualText(t *testing.T) {
	h := vtHost(t, nil, map[string]any{"enabled": true, "position": "eol", "commented": true, "maxLength": 20}, map[string]any{"defaultSection": "scopes"})
	h.mustExec(cmdToggleBreakpoint, 9)
	h.waitSigns(ext.Sign{Line: 9, Kind: ext.SignBreakpoint})
	h.mustExec(cmdStart, 0)
	p := h.waitVT(1, vtHas("// x = 1"), nil)
	want := "8:1:eol:// x = 1:comment\n" +
		"9:1:eol:// s = long long long long …:comment\n" +
		"10:1:eol:// 値 = 日本語 二行目:comment\n" +
		"11:1:eol:// big = []int len: 3000:comment\n"
	if got := vtLines(p.VirtualTexts); got != want {
		t.Errorf("first stop =\n%s\nwant\n%s", got, want)
	}
	if p.Version != 1 {
		t.Errorf("version = %d", p.Version)
	}
	// ペインを閉じていても、Locals（expensive でないスコープ）だけを 1 回ずつ読む。Globals（expensive）は読まない。
	out := h.drainOutputs(200 * time.Millisecond)
	if strings.Count(out, "scopes 1000\n") != 1 || strings.Count(out, "variables 1 ") != 1 || strings.Contains(out, "variables 2 ") {
		t.Errorf("requests at the first stop = %q", out)
	}

	// ステップ実行（すぐ止まる）では、消さずに新しい値に替え、変わった x だけを diagnostic.warning にする。
	h.mustExec(cmdNext, 0)
	var seen []ext.VirtualTextSetParams
	p = h.waitVT(1, vtHas("// x = 2"), &seen)
	for _, s := range seen {
		if vtEmpty(s) {
			t.Errorf("cleared while stepping: %+v", seen)
		}
	}
	if got := vtLines(p.VirtualTexts); !strings.Contains(got, "8:1:eol:// x = 2:diagnostic.warning\n") || !strings.Contains(got, "9:1:eol:// s = long long long long …:comment\n") {
		t.Errorf("after next =\n%s", got)
	}
	if n := len(h.optionGets); n != 1 {
		t.Errorf("option/get = %d times, want 1 (cached)", n)
	}

	// スコープの区画を出すと、同じ停止の scopes と variables を読み直さない。次の停止でも、区画と行末の値で 1 回ずつにする。
	h.drainOutputs(100 * time.Millisecond)
	h.toggleUI()
	if l := labels(childOf(t, h.waitPane().params.Component, secScopes).Items); !strings.Contains(l, "x: int = 2") {
		t.Errorf("scopes in pane/open = %q", l)
	}
	if out := h.drainOutputs(200 * time.Millisecond); strings.Contains(out, "scopes ") || strings.Contains(out, "variables 1 ") {
		t.Errorf("requests after opening the scopes section = %q", out)
	}
	h.mustExec(cmdNext, 0)
	h.waitVT(1, vtHas("// x = 3"), nil)
	h.waitLabel(secScopes, "x: int = 3")
	if out := h.drainOutputs(200 * time.Millisecond); strings.Count(out, "scopes 1000\n") != 1 || strings.Count(out, "variables 1 ") != 1 {
		t.Errorf("requests at a stop with the scopes section = %q", out)
	}

	// 終えると消す。
	h.mustExec(cmdStop, 0)
	h.waitVT(1, vtEmpty, nil)
	h.waitEnded()
}

// TestVirtualTextContinue は、続行して動いている間（runningShowDelay の後）に消し、次に止まったら出し直すことを確かめる。
//
// req: FR-296
func TestVirtualTextContinue(t *testing.T) {
	h := vtHost(t, map[string]any{"stopDelayMs": 800}, map[string]any{"enabled": true}, nil)
	h.mustExec(cmdToggleBreakpoint, 9)
	h.mustExec(cmdToggleBreakpoint, 14)
	h.waitSigns(ext.Sign{Line: 9, Kind: ext.SignBreakpoint}, ext.Sign{Line: 14, Kind: ext.SignBreakpoint})
	h.mustExec(cmdStart, 0)
	// 既定の置き方は inline（名前の直後）。
	p := h.waitVT(1, vtHas(" = 1"), nil)
	if got := vtLines(p.VirtualTexts); !strings.HasPrefix(got, "8:2:inline: = 1:comment\n") {
		t.Errorf("inline =\n%s", got)
	}
	start := time.Now()
	h.mustExec(cmdContinue, 0)
	h.waitVT(1, vtEmpty, nil)
	if d := time.Since(start); d < runningShowDelay || d > 700*time.Millisecond {
		t.Errorf("cleared after %v, want after %v and before the stop", d, runningShowDelay)
	}
	h.waitVT(1, vtHas(" = 2"), nil)
}

// TestVirtualTextFrames は、フレームを選び直すと、そのフレームの変数を出し直し、開いていないファイルのフレームでは前のものを消し、
// そのファイルを開いたときに出すことを確かめる。
//
// req: FR-296
func TestVirtualTextFrames(t *testing.T) {
	const other = "/src/app/other.go"
	h := vtHost(t, nil, map[string]any{"enabled": true, "position": "eol"}, nil)
	h.mustExec(cmdToggleBreakpoint, 14)
	h.waitSigns(ext.Sign{Line: 14, Kind: ext.SignBreakpoint})
	h.mustExec(cmdStart, 0)
	h.waitVT(1, vtHas("x = 1"), nil)
	s := h.current()
	// 呼んだ側のフレーム（helper の見出しの行）を選ぶと、そのフレームの y を出す。
	if !h.d.selectFrame(s, stackFrame{ID: 1002, Name: "main.caller", Line: 3, Source: &source{Path: vtPath}}) {
		t.Fatal("selectFrame = false")
	}
	p := h.waitVT(1, vtHas("y = 7"), nil)
	if got, want := vtLines(p.VirtualTexts), "3:1:eol:y = 7:comment\n"; got != want {
		t.Errorf("caller frame =\n%s\nwant\n%s", got, want)
	}
	// 開いていないファイルのフレームでは、前のバッファのものを消し、開いたら出す。
	if !h.d.selectFrame(s, stackFrame{ID: 1002, Name: "main.caller", Line: 3, Source: &source{Path: other}}) {
		t.Fatal("selectFrame = false")
	}
	h.waitVT(1, vtEmpty, nil)
	h.openText(2, other, "go", "package main\n\nfunc caller() {\n\ty := helper(1)\n}")
	p = h.waitVT(2, vtHas("y = 7"), nil)
	if got, want := vtLines(p.VirtualTexts), "3:1:eol:y = 7:comment\n"; got != want {
		t.Errorf("frame in a file opened later =\n%s\nwant\n%s", got, want)
	}
	// 先頭のフレームに戻すと、ほかのバッファのものを消して、元のバッファに出す。
	if !h.d.selectFrame(s, stackFrame{ID: 1000, Name: "main.main", Line: 15, Source: &source{Path: vtPath}}) {
		t.Fatal("selectFrame = false")
	}
	h.waitVT(2, vtEmpty, nil)
}

// TestVirtualTextEdited は、止まっている間に止まった行より上を編集してから出し直すと、編集でずらしたフレームの行で関数の範囲を決め、
// 今のバッファの行に出すことを確かめる（ずらさないと、10 行の挿入で helper の範囲に入り、x を helper の見出しの行に出す）。
//
// req: FR-296
func TestVirtualTextEdited(t *testing.T) {
	h := vtHost(t, nil, map[string]any{"enabled": true, "position": "eol"}, nil)
	h.mustExec(cmdToggleBreakpoint, 14)
	h.waitSigns(ext.Sign{Line: 14, Kind: ext.SignBreakpoint})
	h.mustExec(cmdStart, 0)
	h.waitVT(1, vtHas("x = 1"), nil)
	h.change(1, ext.Edit{Range: ext.Range{}, Text: strings.Repeat("// pad\n", 10)})
	// 編集の後の出し直しの契機（フレームの選び直しなど）の代わりに、印を付ける。
	waitFor(t, func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return h.d.buffers[vtPath].version == 2
	})
	h.d.pane.markDirty(vtKey)
	p := h.waitVT(1, func(p ext.VirtualTextSetParams) bool { return p.Version == 2 }, nil)
	if got := vtLines(p.VirtualTexts); !strings.HasPrefix(got, "18:1:eol:x = 1:comment\n") {
		t.Errorf("after editing above =\n%s", got)
	}
}

// TestVirtualTextDisabled は、既定（出さない）では、ペインを閉じている間の停止で scopes と variables を送らず、virtualText/set も送らない
// ことを確かめる（ステップ実行の応答を、行末の値を出さないときと変えない）。
//
// req: FR-296
func TestVirtualTextDisabled(t *testing.T) {
	h := vtHost(t, nil, nil, nil)
	h.mustExec(cmdToggleBreakpoint, 14)
	h.waitSigns(ext.Sign{Line: 14, Kind: ext.SignBreakpoint})
	h.mustExec(cmdStart, 0)
	h.waitOpen()
	h.mustExec(cmdNext, 0)
	h.waitOpen()
	if out := h.drainOutputs(400 * time.Millisecond); strings.Contains(out, "scopes ") || strings.Contains(out, "variables ") {
		t.Errorf("requests without virtualText = %q", out)
	}
	if n := len(h.vtexts); n != 0 {
		t.Errorf("virtualText/set sent %d times", n)
	}
}

// TestVirtualTextScopes は、expensive でないスコープをすべて読み、同じ名前の変数が複数のスコープにあれば、先のスコープの値を採るが、
// 先のスコープが locals でなく、後のスコープが locals なら後のものを採ることを確かめる（nvim-dap-virtual-text の「prefer "locals"」）。
// 識別子でない名前、this、関数とモジュールの値は除く。
//
// req: FR-296
func TestVirtualTextScopes(t *testing.T) {
	at := map[string]int{}
	var out []vtVar
	for _, v := range []vtVar{{name: "x", value: "global"}, {name: "x", value: "local", locals: true}, {name: "x", value: "later"}, {name: "g", value: "1"}} {
		out = appendVTVar(out, at, v)
	}
	if len(out) != 2 || out[0].value != "local" || out[1].name != "g" {
		t.Errorf("merged = %+v", out)
	}
	for _, name := range []string{"(x)", "special variables", "[[Prototype]]", "1x", ""} {
		if isIdentifier(name) {
			t.Errorf("isIdentifier(%q) = true", name)
		}
	}
	for _, name := range []string{"x", "_x1", "$el", "値"} {
		if !isIdentifier(name) {
			t.Errorf("isIdentifier(%q) = false", name)
		}
	}
	for _, v := range []string{"<module 'os' from '/usr/lib/python3/os.py'>", "<function bump at 0x7f>", "<class '__main__.A'>"} {
		if !vtSkipValue(v) {
			t.Errorf("vtSkipValue(%q) = false", v)
		}
	}

	// 実物の形: Globals を expensive にしないアダプター（debugpy）では Globals も読み、Locals と同じ名前の x は Locals の値を出す。
	h := vtHost(t, map[string]any{"cheapGlobals": true, "localsHint": "locals"}, map[string]any{"enabled": true, "position": "eol"}, nil)
	h.mustExec(cmdToggleBreakpoint, 14)
	h.waitSigns(ext.Sign{Line: 14, Kind: ext.SignBreakpoint})
	h.mustExec(cmdStart, 0)
	p := h.waitVT(1, vtHas("x = "), nil)
	if got := vtLines(p.VirtualTexts); !strings.HasPrefix(got, "8:1:eol:x = 1:comment\n") || strings.Contains(got, "global x") {
		t.Errorf("locals preferred =\n%s", got)
	}
	if out := h.drainOutputs(200 * time.Millisecond); !strings.Contains(out, "variables 2 ") {
		t.Errorf("cheap Globals was not read: %q", out)
	}
}

// waitFor は、cond が真になるまで待つ。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not hold")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
