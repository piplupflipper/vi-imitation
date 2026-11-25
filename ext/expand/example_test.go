package expand_test

import (
	"fmt"

	"github.com/piplupflipper/vi-imitation/ext/expand"
)

// 1 段だけの展開。置き換えた値が ${…} を含むと Text が返るので、Strings（または Plain）で string に戻してから使う。
func Example_oneStage() {
	r := expand.Resolver{Vars: map[string]expand.Entry{
		"file": {Value: func(string) (string, error) { return "/tmp/${x}/a.go", nil }},
	}}
	v, err := expand.Expand([]any{"--file", "${file}"}, r)
	if err != nil {
		fmt.Println(err)
		return
	}
	args, err := expand.Strings(v) // exec.Cmd の引数
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("%q\n", args)
	// Output: ["--file" "/tmp/${x}/a.go"]
}

// 2 段の展開。1 段目は ${port} を残し、2 段目は残した位置だけを置き換える。
// 1 段目で置き換えた値の中の ${port} は置き換わらない。外部のコマンドが返した値（string）の中の ${port} は置き換わる。
// 2 段目の後に Vars で、置き換えずに残った変数が無いかを確かめる。
func Example_twoStages() {
	first := expand.Resolver{Vars: map[string]expand.Entry{
		"port": {Defer: true},
		"file": {Value: func(string) (string, error) { return "/tmp/${port}/a.go", nil }},
	}}
	v, err := expand.Expand(map[string]any{"args": []any{"--listen=${port}", "${file}"}}, first)
	if err != nil {
		fmt.Println(err)
		return
	}
	m := v.(map[string]any)
	m["extra"] = "${port}${clientAddr}" // 外部のコマンドが返した値を重ねる

	second := expand.Resolver{
		Vars:    map[string]expand.Entry{"port": {Value: func(string) (string, error) { return "5678", nil }}},
		Unknown: expand.Defer,
		Env:     expand.Defer,
	}
	v, err = expand.Expand(m, second)
	if err != nil {
		fmt.Println(err)
		return
	}
	left, _ := expand.Vars(v)
	fmt.Println("残った変数:", left)
	m = v.(map[string]any)
	args, _ := expand.Strings(m["args"])
	fmt.Printf("%q %q\n", args, expand.Plain(m["extra"]))
	// Output:
	// 残った変数: [${clientAddr}]
	// ["--listen=5678" "/tmp/${port}/a.go"] "5678${clientAddr}"
}
