package expand

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// counter は、名前と引数ごとに Value を呼んだ回数を数える Resolver を作る。
type counter struct {
	calls map[Var]int
	order []Var
}

func newCounter() *counter { return &counter{calls: map[Var]int{}} }

// entry は、値として「<名前>=<引数>」を返す項目を作る。
func (c *counter) entry(name string) Entry {
	return Entry{Value: func(arg string) (string, error) {
		v := Var{Name: name, Arg: arg}
		c.calls[v]++
		c.order = append(c.order, v)
		return name + "=" + arg, nil
	}}
}

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 値の全体（入れ子の表と列）を 1 回の展開として扱い、同じ変数（名前と引数の組）を 2 回評価しない。
// キー、数、真偽、nil は展開しない。配列の要素は空白を含んでも 1 つの要素のまま。置き換えた値は展開し直さない。
//
// req: FR-276
func TestExpandWholeValueOnce(t *testing.T) {
	c := newCounter()
	r := Resolver{Vars: map[string]Entry{
		"file":  c.entry("file"),
		"input": c.entry("input"),
		"echo": {Value: func(string) (string, error) {
			c.calls[Var{Name: "echo"}]++
			return "${file} a b", nil
		}},
	}}
	in := decode(t, `{
		"program": "${file}",
		"${file}": "key",
		"args": ["${input:x}", "${input:x}-${input:y}", "${echo}", 3, true, null],
		"env": {"A": "${file}", "B": {"C": ["${input:x}"]}},
		"port": "${input:port}"
	}`)
	got, err := Expand(in, r)
	if err != nil {
		t.Fatal(err)
	}
	want := decode(t, `{
		"program": "file=",
		"${file}": "key",
		"args": ["input=x", "input=x-input=y", "${file} a b", 3, true, null],
		"env": {"A": "file=", "B": {"C": ["input=x"]}},
		"port": "input=port"
	}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Expand = %s\nwant %s", encode(t, got), encode(t, want))
	}
	for v, n := range c.calls {
		if n != 1 {
			t.Errorf("%s を %d 回評価した", v, n)
		}
	}
	if len(c.calls) != 5 {
		t.Errorf("評価した変数 = %v、5 つのはず", c.calls)
	}
	// 渡した値は書き換えない。
	if in.(map[string]any)["program"] != "${file}" {
		t.Error("渡した値が書き換わった")
	}
}

// Check は、評価も問い合わせもせずに、扱えない変数をすべて並べる。Expand も、そのときはどの Value も呼ばない。
//
// req: FR-276
func TestCheckListsAllWithoutEvaluating(t *testing.T) {
	called := false
	val := func(string) (string, error) { called = true; return "", nil }
	r := Resolver{Vars: map[string]Entry{
		"file": {Value: val},
		"input": {Value: val, Check: func(arg string) error {
			if arg != "ok" {
				return errors.New("inputs に無い ID")
			}
			return nil
		}},
		"nothing": {},
	}}
	in := decode(t, `{"a": "${file} ${command:pick} ${input:ok}", "b": ["${input:bad}", "${foo}", "${foo}", "${nothing}"]}`)
	err := Check(in, r)
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("Check = %v、*Error のはず", err)
	}
	var got []string
	for _, p := range e.Problems {
		got = append(got, p.Var.String())
	}
	want := []string{"${command:pick}", "${input:bad}", "${foo}", "${nothing}"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Problems = %v、want %v", got, want)
	}
	if !errors.Is(err, ErrUnknown) {
		t.Error("知らない変数の理由が ErrUnknown でない")
	}
	if !strings.Contains(err.Error(), "${input:bad}（inputs に無い ID）") {
		t.Errorf("誤りの文 = %q", err.Error())
	}
	if _, err := Expand(in, r); err == nil {
		t.Error("Expand が誤りを返さない")
	}
	if called {
		t.Error("確かめで止まるのに Value を呼んだ")
	}
	if err := Check(decode(t, `["${file}", "${input:ok}"]`), r); err != nil {
		t.Errorf("通るはずの Check = %v", err)
	}
}

// Defer の変数（Vars の項目、Unknown、Env）は文字のまま残り、Value も Check も呼ばない。
//
// req: FR-276
func TestExpandDefer(t *testing.T) {
	t.Setenv("EXPAND_TEST_HOME", "/home/x")
	bad := func(string) error { return errors.New("呼ばれてはならない") }
	r := Resolver{
		Vars: map[string]Entry{
			"port": {Defer: true, Check: bad},
			"file": {Value: func(string) (string, error) { return "/a.go", nil }},
		},
		Unknown: Defer,
	}
	got, err := Expand(decode(t, `["${file}:${port}", "${foo:a:b}", "${env:EXPAND_TEST_HOME}"]`), r)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"/a.go:${port}", "${foo:a:b}", "/home/x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Expand = %v、want %v", got, want)
	}
}

// 2 段目の展開（Unknown と Env が Defer）では、表に置いた ${port} だけが置き換わる。
// 1 段目で残した変数と、外部のコマンドが返した値の中の ${env:…} と ${foo} は文字のまま残る。
//
// req: FR-276
func TestExpandSecondStage(t *testing.T) {
	t.Setenv("HOME", "/home/x")
	first := Resolver{Vars: map[string]Entry{
		"port":       {Defer: true},
		"clientAddr": {Defer: true},
		"file":       {Value: func(string) (string, error) { return "/a.go", nil }},
	}}
	stage1, err := Expand(decode(t, `{"args": ["--listen", "127.0.0.1:${port}", "${file}"], "cwd": "${env:HOME}"}`), first)
	if err != nil {
		t.Fatal(err)
	}
	// 外部のコマンドが返した値を重ねた（FR-283）。この中の ${env:HOME} と ${foo} は展開しない。
	m := stage1.(map[string]any)
	m["extra"] = []any{"${env:HOME}", "${foo}", "${port}"}

	second := Resolver{
		Vars:    map[string]Entry{"port": {Value: func(string) (string, error) { return "5678", nil }}},
		Unknown: Defer,
		Env:     Defer,
	}
	got, err := Expand(m, second)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"args":  []any{"--listen", "127.0.0.1:5678", "/a.go"},
		"cwd":   "/home/x",
		"extra": []any{"${env:HOME}", "${foo}", "5678"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Expand = %v、want %v", got, want)
	}
}

// Value が誤り（取り消し）を返したら、残りを評価せずに止め、どの値も置き換えない。
//
// req: FR-276
func TestExpandStopsOnValueError(t *testing.T) {
	var asked []string
	r := Resolver{Vars: map[string]Entry{
		"input": {Value: func(arg string) (string, error) {
			asked = append(asked, arg)
			if arg == "b" {
				return "", context.Canceled
			}
			return arg, nil
		}},
	}}
	got, err := Expand([]any{"${input:a}", "${input:b}", "${input:c}"}, r)
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Expand = %v, %v", got, err)
	}
	if !reflect.DeepEqual(asked, []string{"a", "b"}) {
		t.Errorf("聞いた順 = %v、b で止まるはず", asked)
	}
	if !strings.Contains(err.Error(), "${input:b}") {
		t.Errorf("誤りの文に変数が無い: %q", err.Error())
	}
}

// ${env:…} は仕組みが読む。値の無い環境変数は空の文字列（仮）。Env が Fail なら知らない名前と同じ誤り。
//
// req: FR-276
func TestExpandEnv(t *testing.T) {
	t.Setenv("EXPAND_TEST_A", "x y")
	got, err := Expand("${env:EXPAND_TEST_A}|${env:EXPAND_TEST_UNSET_ZZZ}|", Resolver{})
	if err != nil || got != "x y||" {
		t.Errorf("Expand = %q, %v", got, err)
	}
	if err := Check("${env:EXPAND_TEST_A}", Resolver{Env: Fail}); !errors.Is(err, ErrUnknown) {
		t.Errorf("Env が Fail の Check = %v", err)
	}
	if err := Check("${env} ${env:}", Resolver{}); err == nil {
		t.Error("名前の無い ${env} が通った")
	}
	// Vars の env は見ない。
	r := Resolver{Vars: map[string]Entry{"env": {Value: func(string) (string, error) { return "no", nil }}}}
	if got, _ := Expand("${env:EXPAND_TEST_A}", r); got != "x y" {
		t.Errorf("Vars の env を使った: %q", got)
	}
}

// 書き方の境界。変数でない ${ は文字のまま残る。
func TestParseBoundaries(t *testing.T) {
	r := Resolver{Vars: map[string]Entry{
		"a": {Value: func(arg string) (string, error) { return "A(" + arg + ")", nil }},
		"/": {Value: func(string) (string, error) { return "/", nil }},
	}}
	cases := []struct{ in, want string }{
		{"", ""},
		{"$", "$"},
		{"$$", "$$"},
		{"a$b", "a$b"},
		{"${", "${"},
		{"x${a", "x${a"},
		{"${a:b", "${a:b"},
		{"${}", "${}"},
		{"${:x}", "${:x}"},
		{"${ a }", "${ a }"},
		{"${a-b}", "${a-b}"},
		{"${a ${a}", "${a A()"},
		{"$${a}", "$A()"},
		{"${a}", "A()"},
		{"${a:}", "A()"},
		{"${a:b:c}", "A(b:c)"},
		{"${a:editor.fontSize}", "A(editor.fontSize)"},
		{"${a:x ${a}", "A(x ${a)"},
		{"${a:}}", "A()}"},
		{"${/}", "/"},
		{"日本${a:語}の${a}", "日本A(語)のA()"},
		{"${日本}", "${日本}"},
	}
	for _, c := range cases {
		got, err := Expand(c.in, r)
		if err != nil {
			t.Errorf("Expand(%q) の誤り %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Expand(%q) = %q、want %q", c.in, got, c.want)
		}
	}
}

// Vars は、評価せずに重ねずに、現れた順に並べる（map はキーの辞書順）。
func TestVars(t *testing.T) {
	got := Vars(decode(t, `{"b": ["${x}", "${y:1}"], "a": "${y:1} ${x:} ${y:2}", "c": 1}`))
	want := []Var{{Name: "y", Arg: "1"}, {Name: "x"}, {Name: "y", Arg: "2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Vars = %v、want %v", got, want)
	}
	if got := Vars(42); got != nil {
		t.Errorf("Vars(42) = %v", got)
	}
}

// 文字列でない値と、JSON の型でない値はそのまま返す。
func TestExpandNonString(t *testing.T) {
	for _, in := range []any{nil, 1.5, true, map[string]string{"a": "${x}"}, []any(nil), map[string]any(nil)} {
		got, err := Expand(in, Resolver{})
		if err != nil || !reflect.DeepEqual(got, in) {
			t.Errorf("Expand(%#v) = %#v, %v", in, got, err)
		}
	}
}
