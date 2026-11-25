//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/expand"
)

// この節は、デバッグの構成の ${input:ID}（inputs）のテストである（基本設計 6.4。FR-298。#417）。

// waitInputAsk は、input/ask が届くまで待つ。
func (h *host) waitInputAsk() ext.InputAskParams {
	h.t.Helper()
	select {
	case p := <-h.inputAsks:
		return p
	case <-time.After(testTimeout):
		h.t.Fatal("input/ask did not arrive")
		return ext.InputAskParams{}
	}
}

// waitChoiceAsk は、choice/ask が届くまで待つ。
func (h *host) waitChoiceAsk() ext.ChoiceAskParams {
	h.t.Helper()
	select {
	case p := <-h.asks:
		return p
	case <-time.After(testTimeout):
		h.t.Fatal("choice/ask did not arrive")
		return ext.ChoiceAskParams{}
	}
}

// noAsk は、しばらくの間、input/ask も choice/ask も届かないことを確かめる。
func (h *host) noAsk(what string) {
	h.t.Helper()
	select {
	case p := <-h.inputAsks:
		h.t.Errorf("%s: input/ask %+v", what, p)
	case p := <-h.asks:
		h.t.Errorf("%s: choice/ask %+v", what, p)
	case <-time.After(150 * time.Millisecond):
	}
}

// inputSettings は、テスト用のデバッグアダプターと、inputs と、go の構成 configs を持つ設定である。
func inputSettings(t *testing.T, configs ...map[string]any) string {
	t.Helper()
	many := make([]any, 10)
	for i := range many {
		many[i] = fmt.Sprintf("m%d", i)
	}
	var cs []any
	for _, c := range configs {
		c["type"] = "fake"
		if _, ok := c["request"]; !ok {
			c["request"] = "launch"
		}
		cs = append(cs, c)
	}
	return mustJSON(t, map[string]any{
		"adapters":       map[string]any{"fake": fakeDef(transportStdio)},
		"configurations": map[string]any{"go": cs},
		"inputs": []any{
			map[string]any{"id": "args", "type": "promptString", "description": "引数", "split": true},
			map[string]any{"id": "p", "type": "promptString", "description": "プログラム", "default": "/def"},
			map[string]any{"id": "pick", "type": "pickString", "description": "モード", "options": []any{"a", map[string]any{"label": "B", "value": "b"}}, "default": "b"},
			map[string]any{"id": "secret", "type": "promptString", "password": true},
			map[string]any{"id": "cmd", "type": "command", "command": "extension.pick"},
			map[string]any{"id": "many", "type": "pickString", "options": many},
		},
	})
}

// TestInputs は、${input:ID} を inputs の表の順に聞き（promptString は既定値つきの input/ask、pickString は既定の選択肢を先頭にした
// choice/ask か、10 以上なら一覧）、同じ ID は 1 回だけ聞き、split の項目は配列の要素を空白で分け、空の答えも使い、
// 取り消しと debug.terminate では残りを聞かずに始めず、扱えない種類は何も聞かずに知らせ、
// debug.runLast は聞き直さず、外部のコマンドが返した値の中の ${input:…} は展開しないことを確かめる。
//
// req: FR-298
func TestInputs(t *testing.T) {
	// 出力の ${input:p} は、構成に書くと 1 段目の展開で聞くので、printf の \044（$）で書く。
	rc := `printf '{"configuration":{"program":"\044{input:p}"}}'`
	h := newHost(t, inputSettings(t,
		map[string]any{"name": "all", "program": "${input:p}", "label": "x ${input:p}", "args": []any{"-s", "${input:args}", "${input:p}"}, "mode": "${input:pick}"},
		map[string]any{"name": "empty", "program": "${input:p}", "args": []any{"${input:args}"}},
		map[string]any{"name": "cancel", "program": "${input:p}", "args": []any{"${input:args}"}},
		map[string]any{"name": "unsupported", "program": "${input:secret}", "x": "${input:cmd}", "y": "${input:p}", "z": "${input:none}"},
		map[string]any{"name": "many", "mode": "${input:many}"},
		map[string]any{"name": "rc", "resolveCommand": map[string]any{"command": []any{"sh", "-c", rc}}},
	))
	h.openFT(srcPath, "go")
	h.mustExec(cmdToggleBreakpoint, 2)
	pick := func(name string) {
		t.Helper()
		h.mustExec(cmdStart, 0)
		p := h.waitPane()
		h.selectItem(p.id, p.itemID(t, name+"  (設定)"))
	}

	// 表の順（args、p、pick）に聞き、p は 3 か所にあっても 1 回だけ聞く。
	pick("all")
	if q := h.waitInputAsk(); q.Title != "引数: " || q.Default != "" {
		t.Errorf("1st input/ask = %+v, want args", q)
	}
	h.inputAnswers <- ext.InputAskResult{Value: " a  b c "}
	if q := h.waitInputAsk(); q.Title != "プログラム: " || q.Default != "/def" {
		t.Errorf("2nd input/ask = %+v, want p with the default", q)
	}
	h.inputAnswers <- ext.InputAskResult{Value: "/def"}
	q := h.waitChoiceAsk()
	labels := make([]string, len(q.Choices))
	for i, c := range q.Choices {
		labels[i] = c.Label
	}
	if q.Title != "モード:" || !slices.Equal(labels, []string{"B: b", "a"}) {
		t.Errorf("choice/ask = %q %q, want the default first", q.Title, labels)
	}
	h.answers <- ext.ChoiceAskResult{Selected: q.Choices[0].ID}
	args := h.waitArgs("launch")
	if args["program"] != "/def" || args["label"] != "x /def" || args["mode"] != "b" ||
		mustJSON(t, args["args"]) != `["-s","a","b","c","/def"]` {
		t.Errorf("launch args = %v", args)
	}
	h.noAsk("after the answers")
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	// debug.runLast は、前回の答えで始め、聞き直さない。
	h.mustExec(ext.OpDebugRunLast, 0)
	if args := h.waitArgs("launch"); args["program"] != "/def" || mustJSON(t, args["args"]) != `["-s","a","b","c","/def"]` {
		t.Errorf("runLast launch args = %v", args)
	}
	h.noAsk("debug.runLast")
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	// 空の答え（既定値を消した）は空の文字列で、split の項目は要素を作らない。
	pick("empty")
	h.waitInputAsk()
	h.inputAnswers <- ext.InputAskResult{Value: ""}
	h.waitInputAsk()
	h.inputAnswers <- ext.InputAskResult{Value: ""}
	if args := h.waitArgs("launch"); args["program"] != "" || mustJSON(t, args["args"]) != `[]` {
		t.Errorf("launch args = %v, want the empty answers", args)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	// 取り消したら、残りを聞かずに始めない。
	pick("cancel")
	h.waitInputAsk()
	h.inputAnswers <- ext.InputAskResult{Cancelled: true}
	h.waitMessage("デバッグの開始を取り消した")
	h.noAsk("after the cancel")
	h.waitEnded()

	// 聞いている間の debug.terminate で、始めない。
	pick("cancel")
	h.waitInputAsk()
	h.mustExec(cmdStop, 0)
	h.waitMessage("デバッグの開始を取り消した")
	h.inputAnswers <- ext.InputAskResult{Value: "late"}
	h.noAsk("after debug.terminate")
	h.waitEnded()

	// password、command の種類、無い ID は、何も聞かずに並べて知らせる。
	pick("unsupported")
	m := h.waitMessage("構成 unsupported を始めない")
	for _, want := range []string{"${input:secret}", "password", "${input:cmd}", "command の種類", "${input:none}"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("message %q does not contain %q", m.Text, want)
		}
	}
	h.noAsk("unsupported inputs")

	// 10 以上の選択肢は、中央のポップアップの一覧で選ばせる。
	pick("many")
	p := h.waitPane()
	if p.params.Placement != ext.PaneCenter || len(p.labels()) != 10 || p.labels()[3] != "m3" {
		t.Fatalf("pickString list = %q", p.labels())
	}
	h.selectItem(p.id, p.itemID(t, "m3"))
	if args := h.waitArgs("launch"); args["mode"] != "m3" {
		t.Errorf("launch args = %v", args)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	// 外部のコマンドが返した値の中の ${input:…} は、聞かずに文字のまま送る。
	pick("rc")
	if args := h.waitArgs("launch"); args["program"] != "${input:p}" {
		t.Errorf("launch args = %v, want ${input:p} left as is", args)
	}
	h.noAsk("the resolveCommand output")
	h.waitSigns(bp2, exec2)
	h.stopAndWait()
}

// TestInputsLaunchJSON は、.vscode/launch.json の構成の ${input:ID} が、その launch.json の inputs を先に引き、無ければ設定の inputs を引き、
// 設定の構成は launch.json の inputs を引かないことを確かめる。
//
// req: FR-298
func TestInputsLaunchJSON(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".vscode", "launch.json"), `{
  "configurations": [
    { "name": "L", "type": "fake", "request": "launch", "program": "${input:p}", "port": "${input:onlySettings}" },
    { "name": "LS", "type": "fake", "request": "launch", "args": ["${input:sp}"] },
    { "name": "LA", "type": "fakein", "request": "launch" },
  ],
  "inputs": [
    { "id": "p", "type": "promptString", "description": "launch.json の前の p" },
    { "id": "p", "type": "promptString", "description": "launch.json の p" },
    { "id": "sp", "type": "promptString", "split": true },
  ],
}`)
	file := filepath.Join(root, "main.go")
	writeFile(t, file, srcText)
	h := newHost(t, mustJSON(t, map[string]any{
		"adapters":       map[string]any{"fake": fakeDef(transportStdio), "fakein": fakeDef(transportStdio, "${input:p}")},
		"configurations": map[string]any{"go": []any{map[string]any{"name": "S", "type": "fake", "request": "launch", "program": "${input:p}"}}},
		"inputs": []any{
			map[string]any{"id": "onlySettings", "type": "promptString", "description": "設定の onlySettings"},
			map[string]any{"id": "p", "type": "promptString", "description": "設定の p"},
		},
	}))
	h.openFT(file, "go")
	h.mustExec(cmdToggleBreakpoint, 2)

	h.mustExec(cmdStart, 0)
	p := h.waitPane()
	h.selectItem(p.id, p.itemID(t, "L  (launch.json)"))
	if q := h.waitInputAsk(); q.Title != "launch.json の p: " {
		t.Errorf("1st input/ask = %+v, want the launch.json p first", q)
	}
	h.inputAnswers <- ext.InputAskResult{Value: "/lj"}
	if q := h.waitInputAsk(); q.Title != "設定の onlySettings: " {
		t.Errorf("2nd input/ask = %+v", q)
	}
	h.inputAnswers <- ext.InputAskResult{Value: "5678"}
	// ${input:…} の値は文字列のまま送る（数に直すかは基本設計 10 章の未決）。
	if args := h.waitArgs("launch"); args["program"] != "/lj" || args["port"] != "5678" {
		t.Errorf("launch args = %v", args)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	h.mustExec(cmdStart, 0)
	p = h.waitPane()
	h.selectItem(p.id, p.itemID(t, "S  (設定)"))
	if q := h.waitInputAsk(); q.Title != "設定の p: " {
		t.Errorf("input/ask = %+v, want the settings p", q)
	}
	h.inputAnswers <- ext.InputAskResult{Value: "/s"}
	if args := h.waitArgs("launch"); args["program"] != "/s" {
		t.Errorf("launch args = %v", args)
	}
	h.waitSigns(bp2, exec2)
	h.stopAndWait()

	// launch.json の inputs の split は、聞かずに知らせて始めない（FR-307）。
	h.mustExec(cmdStart, 0)
	p = h.waitPane()
	h.selectItem(p.id, p.itemID(t, "LS  (launch.json)"))
	h.waitMessage("split は .vscode/launch.json の inputs では扱えない")
	h.noAsk("split in launch.json")

	// アダプターの起動の方法の ${input:…} は設定の inputs で引くので、launch.json の inputs に同じ ID があれば始めない。
	h.mustExec(cmdStart, 0)
	p = h.waitPane()
	h.selectItem(p.id, p.itemID(t, "LA  (launch.json)"))
	h.waitMessage("アダプターの起動の方法の ${input:p}")
	h.noAsk("adapter input shadowed by launch.json")
}

// TestInputOrder は、${input:ID} と ${pickProcess} を、inputs の表の順でもキーの辞書順でもなく、構成に書いた順に聞くことを確かめる
// （VS Code が構成を書いた順にたどって聞くのに合わせた）。
//
// req: FR-298
func TestInputOrder(t *testing.T) {
	settings := `{
  "adapters": {"fake": ` + mustJSON(t, fakeDef(transportStdio)) + `},
  "configurations": {"go": [
    {"name": "O", "type": "fake", "request": "attach", "z": "${input:b}", "args": ["${input:c}", "${input:a}"], "pid": "${pickProcess}", "y": "${input:a}"}
  ]},
  "inputs": [
    {"id": "a", "type": "promptString", "description": "a"},
    {"id": "b", "type": "promptString", "description": "b"},
    {"id": "c", "type": "promptString", "description": "c"}
  ]
}`
	h := newHost(t, settings)
	h.openFT(srcPath, "go")
	h.mustExec(cmdToggleBreakpoint, 2)
	h.mustExec(cmdStart, 0)
	for _, want := range []string{"b: ", "c: ", "a: "} {
		if q := h.waitInputAsk(); q.Title != want {
			t.Errorf("input/ask = %q, want %q", q.Title, want)
		}
		h.inputAnswers <- ext.InputAskResult{Value: want[:1]}
	}
	p := h.waitPane()
	h.selectItem(p.id, p.params.Component.Items[0].ID)
	args := h.waitArgs("attach")
	if args["z"] != "b" || args["y"] != "a" || mustJSON(t, args["args"]) != `["c","a"]` {
		t.Errorf("attach args = %v", args)
	}
	h.noAsk("after the answers")
	h.waitSigns(bp2, exec2)
	h.stopAndWait()
}

// TestParseInputs は、inputs の形の誤りのうち、表を作れないもの（配列でない、id が無い）は読む誤りにし、
// ほかの誤りと扱えない種類は、その ID を使ったときの Check の誤りにすることを確かめる。
//
// req: FR-298
func TestParseInputs(t *testing.T) {
	for _, raw := range []string{
		`{"inputs": {}}`,
		`{"inputs": [1]}`,
		`{"inputs": [{"type": "promptString"}]}`,
		`{"inputs": [{"id": 3}]}`,
	} {
		if _, err := parseSettings([]byte(raw)); err == nil {
			t.Errorf("parseSettings(%s) = nil error", raw)
		}
	}
	s, err := parseSettings([]byte(`{"inputs": [
		{"id": "ok", "type": "promptString"},
		{"id": "pick", "type": "pickString", "options": ["a", {"label": "L", "value": "b"}], "default": "b"},
		{"id": "nodefault", "type": "pickString", "options": ["a"], "default": "z"},
		{"id": "noopts", "type": "pickString"},
		{"id": "badopts", "type": "pickString", "options": [1]},
		{"id": "pw", "type": "promptString", "password": true},
		{"id": "cmd", "type": "command", "command": "x"},
		{"id": "nl", "type": "promptString", "default": "a\nb"},
		{"id": "badsplit", "type": "promptString", "split": "yes"},
		{"id": "notype"},
		{"id": "other", "type": "pickFile"},
		{"id": "dup", "type": "command"},
		{"id": "dup", "type": "promptString"},
		{"id": "split", "type": "promptString", "split": true}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	a := newInputAsker(t.Context(), nil, nil, s.inputs)
	for id, ok := range map[string]bool{
		"ok": true, "pick": true, "nodefault": false, "noopts": false, "badopts": false, "pw": false, "cmd": false,
		"nl": false, "badsplit": false, "notype": false, "other": false, "missing": false, "": false, "dup": true, "split": true,
	} {
		if err := a.check(id); (err == nil) != ok {
			t.Errorf("check(%q) = %v, want ok %v", id, err, ok)
		}
	}
	// 同じ表の同じ ID は、後のもの（promptString）を使う（VS Code と同じ）。
	if in, _ := s.inputs.find("dup"); in.kind != inputPromptString {
		t.Errorf("find(dup) = %+v, want the last", in)
	}
	if in, _ := s.inputs.find("pick"); !slices.Equal(in.options, []inputOption{{"a", "a"}, {"L", "b"}}) {
		t.Errorf("pick options = %+v", in.options)
	}
	if in, _ := s.inputs.find("split"); !in.split {
		t.Errorf("settings split = %+v, want split", in)
	}
	// 聞けない asker（設定の例の確かめ）は、聞かずに誤りを返す。
	if _, err := a.inputValue("ok"); err == nil {
		t.Error("value without a debugger: nil error")
	}

	// .vscode/launch.json の inputs の split は、扱えない項目にする（FR-307）。
	lt, lerr := parseInputs([]any{map[string]any{"id": "s", "type": "promptString", "split": true}}, false)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if err := newInputAsker(t.Context(), nil, lt, nil).check("s"); !errors.Is(err, errSplitInLaunchJSON) {
		t.Errorf("check(launch.json split) = %v, want errSplitInLaunchJSON", err)
	}
}

// TestInputTitle は、問いの文を description（無ければ ID）に「: 」を足して作り、改行を空白にすることを確かめる。
func TestInputTitle(t *testing.T) {
	for _, c := range []struct {
		in   inputDef
		want string
	}{
		{inputDef{id: "x"}, "x: "},
		{inputDef{id: "x", description: "ポート"}, "ポート: "},
		{inputDef{id: "x", description: "ポート: "}, "ポート: "},
		{inputDef{id: "x", description: "ポート："}, "ポート： "},
		{inputDef{id: "x", description: "a\nb"}, "a b: "},
	} {
		if got := c.in.title(); got != c.want {
			t.Errorf("title(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSpliceSplit は、split の項目の ${input:ID} が配列の要素の全体のときだけ、答えを ASCII の空白で分けた要素にし、
// 全角の空白、タブ、引用符では分けないことを確かめる。
//
// req: FR-307
func TestSpliceSplit(t *testing.T) {
	a := newInputAsker(t.Context(), nil, nil, inputTable{{id: "s", kind: inputPromptString, split: true}, {id: "n", kind: inputPromptString}, {id: "q", kind: inputPromptString, split: true}})
	before := map[string]any{
		"args":  []any{"-x", "${input:s}", "pre ${input:s}", "${input:n}", []any{"${input:s}"}, "${input:q}"},
		"whole": "${input:s}",
	}
	values := map[string]string{"s": " a  b ", "n": "c d", "q": `"x y"` + " d　e\tf"}
	r := expand.Resolver{Vars: map[string]expand.Entry{"input": {Value: func(id string) (string, error) {
		v, ok := values[id]
		if !ok {
			return "", errors.New("no")
		}
		return v, nil
	}}}}
	after, err := expand.Expand(before, r)
	if err != nil {
		t.Fatal(err)
	}
	got := a.spliceSplit(after, before)
	want := `{"args":["-x","a","b","pre  a  b ","c d",["a","b"],"\"x","y\"","d　e\tf"],"whole":" a  b "}`
	if s := mustJSON(t, got); s != want {
		t.Errorf("spliceSplit = %s, want %s", s, want)
	}
}
