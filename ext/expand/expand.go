// Package expand は、拡張機能の設定の文字列の中の変数（${名前} と ${名前:引数}）を展開する（FR-276。基本設計 6.7 の「設定の文字列の変数の展開」）。
//
// 拡張機能の側の共有の部品で、最初に DAP の拡張機能のデバッグの構成で使う。
// 基本設計 2 章の土台の層（ext と同じ層）に置き、標準ライブラリだけを import する（ext も import しない）。
//
// 変数の意味（${file}、${workspaceFolder}、${input:…} など）はこのパッケージに持たせない。
// 呼び出す側が Resolver で名前ごとの項目を渡す。このパッケージが持つのは ${env:名前} の読み方だけである。
//
// 書き方と展開の手順は、VS Code の文書「Variables reference」（https://code.visualstudio.com/docs/reference/variables-reference）に合わせた（2026-11-25 に読んだ）。
//
// # 書き方
//
//   - 変数は ${名前} か ${名前:引数} である。名前は ASCII の英字と数字と / の 1 文字以上（${/} のため）。引数は最初の : から最初の } の手前までで、: を含んでよく、} を含まない（${config:editor.fontSize}、${workspaceFolder:Client}）
//   - $ の後が { でないもの、閉じていない ${、名前が空のもの（${}）、名前に英字と数字と / 以外の文字があるもの（${foo-bar}、${ foo }）は、変数でなく文字のまま残す。名前の文字の種類は文書から決まらないので仮に決めた
//   - $ を書き表す方法（$$ など）は持たない。文書にエスケープの記述が無いためである。$${x} は、$ の後に ${x} を展開したものになる
//   - ${名前:}（引数が空）は ${名前} と同じ変数として扱う（仮）
//
// # 展開の手順
//
// 1 回の展開の単位は、Expand に渡した値の全体である。文書の「Details of variable substitution」の 2 段の手順に従う。
//
//  1. 値の全体に書かれた変数を、名前と引数の組ごとに重ねずに集める（Vars）
//  2. どの変数も評価する前に、表に無い名前と、引数の確かめ（Entry.Check）の誤りを、すべて並べる（Check）。あれば、何も評価せずに誤りを返す
//  3. 集めた変数を、組ごとに 1 回だけ評価する（文書の「If a variable occurs more than once, it is only evaluated once.」）。評価の順は値の中の現れた順だが、意味を持たせない
//  4. すべてを評価し終えてから置き換える。置き換えた値の中の ${…} は展開し直さない（文書の「It only sees the original variables.」）
//
// 3 で評価が誤りを返したら、残りの変数を評価せずに止め、どの値も置き換えずにその誤りを返す（仮。値を聞く変数で、取り消した後に別の問いを出さないため）。
//
// # JSON の値
//
// Expand と Vars は、encoding/json で any に読んだ値（map[string]any、[]any、string、数、真偽、nil）を受ける。
// オブジェクトの値と配列の要素の文字列を、入れ子をたどって展開する。
// オブジェクトのキーは展開しない（基本設計の決め。文書は「some key and value strings」で置き換えるとしているが、どのキーかを書いていない）。
// 数、真偽、nil、それ以外の型（map[string]string など）はそのまま返す（仮）。
// 配列の 1 つの要素は、展開した値が空白を含んでも 1 つの要素のまま（文書の「It doesn't add quotes or split the result on spaces.」）。
// 文字列の全体が 1 つの変数でも、展開した結果は文字列で、数にしない。
// Expand は、渡した値を書き換えず、新しい map と slice を作って返す。
package expand

import (
	"errors"
	"os"
	"sort"
	"strings"
)

// Var は、値に書かれた 1 つの変数（名前と引数の組）である。${名前} と ${名前:} は、どちらも Arg が空の同じ Var になる。
type Var struct {
	Name string
	Arg  string
}

// String は、変数の書き方（${名前} か ${名前:引数}）を返す。誤りの文に使う。
func (v Var) String() string {
	if v.Arg == "" {
		return "${" + v.Name + "}"
	}
	return "${" + v.Name + ":" + v.Arg + "}"
}

// Handling は、Resolver の Unknown と Env に書く扱いである。
// 0 の値は既定（Unknown は Fail、Env は Builtin）を表す。
type Handling int

const (
	// Fail は、変数を誤りにする（Unknown の既定）。Env に書くと、${env:…} は知らない名前と同じ誤りになる。
	Fail Handling = iota + 1
	// Defer は、変数を置き換えずに文字のまま残す。後の段階で別の展開にかけるためである。
	Defer
	// Builtin は、このパッケージが環境変数を読んで ${env:名前} を置き換える（Env の既定）。
	// 値の無い環境変数は空の文字列にする（FR-276 の仮）。Unknown に書くと Fail と同じに扱う（仮）。
	Builtin
)

// Entry は、Resolver.Vars の 1 つの名前の項目である。
type Entry struct {
	// Value は、引数を受けて変数の値を返す。決められなければ誤りを返す。
	// 利用者に聞いてよく、時間がかかってよい。取り消されたら誤りを返す（Expand はその誤りを包んで返す）。
	// Defer が偽で Value が nil の項目は、決められない変数として Check の誤りになる。
	Value func(arg string) (string, error)
	// Check は、引数を評価せずに確かめ、扱えなければ誤りを返す。省ける。
	// 利用者に聞いてはならない。Check と Expand の確かめの段で、どの Value よりも前に呼ぶ。
	Check func(arg string) error
	// Defer が真なら、この名前の変数は置き換えずに文字のまま残す（Value と Check は呼ばない）。
	Defer bool
}

// Resolver は、展開する変数の意味を、呼び出す側が渡す口である。
type Resolver struct {
	// Vars は、名前ごとの項目の表である。名前 env の項目は見ない（${env:…} は Env で扱う。仮）。
	Vars map[string]Entry
	// Unknown は、Vars に無い名前（env を除く）の扱いである。0 は Fail。
	Unknown Handling
	// Env は、${env:名前} の扱いである。0 は Builtin。
	Env Handling
}

// envName は、このパッケージが読み方を持つ唯一の名前である。
const envName = "env"

// Problem は、展開できない 1 つの変数と、その理由である。
type Problem struct {
	Var Var
	Err error
}

// Error は、展開できない変数を並べた誤りである。Check と Expand が返す。
// Problems は、値の中に現れた順である。
type Error struct {
	Problems []Problem
}

// Error は、「変数を展開できません: ${foo}（知らない変数）、…」の形の文を返す。
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("変数を展開できません: ")
	for i, p := range e.Problems {
		if i > 0 {
			b.WriteString("、")
		}
		b.WriteString(p.Var.String())
		b.WriteString("（")
		b.WriteString(p.Err.Error())
		b.WriteString("）")
	}
	return b.String()
}

// Unwrap は、変数ごとの理由を返す。errors.Is で Value が返した取り消しの誤りを見分けられる。
func (e *Error) Unwrap() []error {
	errs := make([]error, len(e.Problems))
	for i, p := range e.Problems {
		errs[i] = p.Err
	}
	return errs
}

// ErrUnknown は、知らない名前の変数の理由である。
var ErrUnknown = errors.New("知らない変数")

// errNoValue は、Value を持たない項目の理由である。
var errNoValue = errors.New("値を決められない変数")

// errEnvName は、${env} と ${env:} の理由である。
var errEnvName = errors.New("環境変数の名前が無い")

// token は、文字列を変数と地の文に分けた 1 つの断片である。
// isVar が偽なら text は地の文で、真なら text は変数の原文（Defer で残すため）である。
type token struct {
	text  string
	v     Var
	isVar bool
}

// isNameByte は、名前に使える文字かを返す。
func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '/'
}

// parse は、文字列を変数と地の文の断片に分ける。変数でない ${ は地の文に残す。
func parse(s string) []token {
	var toks []token
	start := 0 // まだ断片にしていない地の文の始まり
	i := 0
	for i < len(s) {
		if s[i] != '$' || i+1 >= len(s) || s[i+1] != '{' {
			i++
			continue
		}
		v, end, ok := parseVar(s, i)
		if !ok {
			// 変数でない。$ を地の文にして、{ から読み続ける（${a ${b} の ${b} を拾うため）。
			i++
			continue
		}
		if start < i {
			toks = append(toks, token{text: s[start:i]})
		}
		toks = append(toks, token{text: s[i:end], v: v, isVar: true})
		i = end
		start = end
	}
	if start < len(s) {
		toks = append(toks, token{text: s[start:]})
	}
	return toks
}

// parseVar は、s[i:] の先頭の ${ から 1 つの変数を読み、変数と、その後の位置を返す。
func parseVar(s string, i int) (Var, int, bool) {
	j := i + 2
	for j < len(s) && isNameByte(s[j]) {
		j++
	}
	if j == i+2 || j >= len(s) {
		return Var{}, 0, false
	}
	name := s[i+2 : j]
	switch s[j] {
	case '}':
		return Var{Name: name}, j + 1, true
	case ':':
		k := strings.IndexByte(s[j+1:], '}')
		if k < 0 {
			return Var{}, 0, false
		}
		return Var{Name: name, Arg: s[j+1 : j+1+k]}, j + 1 + k + 1, true
	default:
		return Var{}, 0, false
	}
}

// walk は、値の全体の文字列を、map のキーの順（辞書順）と配列の順でたどる。
func walk(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case []any:
		for _, e := range x {
			walk(e, f)
		}
	case map[string]any:
		for _, k := range sortedKeys(x) {
			walk(x[k], f)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Vars は、値の全体に書かれた変数を、評価せずに重ねずに並べる。
// 並びは現れた順で、map はキーの辞書順にたどる。
func Vars(v any) []Var {
	var out []Var
	seen := map[Var]bool{}
	walk(v, func(s string) {
		for _, t := range parse(s) {
			if t.isVar && !seen[t.v] {
				seen[t.v] = true
				out = append(out, t.v)
			}
		}
	})
	return out
}

// action は、1 つの変数をどう扱うかである。
type action int

const (
	actResolve action = iota // Entry.Value で評価する
	actEnv                   // 環境変数を読む
	actDefer                 // 文字のまま残す
)

// classify は、変数の扱いを決める。扱えなければ誤りを返す。Entry.Check もここで呼ぶ。
func classify(v Var, r Resolver) (action, Entry, error) {
	if v.Name == envName {
		switch r.Env {
		case Defer:
			return actDefer, Entry{}, nil
		case Fail:
			return 0, Entry{}, ErrUnknown
		case Builtin:
		default: // 0 は既定の Builtin
		}
		if v.Arg == "" {
			return 0, Entry{}, errEnvName
		}
		return actEnv, Entry{}, nil
	}
	e, ok := r.Vars[v.Name]
	if !ok {
		if r.Unknown == Defer {
			return actDefer, Entry{}, nil
		}
		return 0, Entry{}, ErrUnknown
	}
	if e.Defer {
		return actDefer, e, nil
	}
	if e.Value == nil {
		return 0, e, errNoValue
	}
	if e.Check != nil {
		if err := e.Check(v.Arg); err != nil {
			return 0, e, err
		}
	}
	return actResolve, e, nil
}

// check は、変数ごとの扱いを決め、扱えない変数をすべて並べる。
func check(vars []Var, r Resolver) (map[Var]action, map[Var]Entry, error) {
	acts := make(map[Var]action, len(vars))
	entries := make(map[Var]Entry, len(vars))
	var probs []Problem
	for _, v := range vars {
		a, e, err := classify(v, r)
		if err != nil {
			probs = append(probs, Problem{Var: v, Err: err})
			continue
		}
		acts[v] = a
		entries[v] = e
	}
	if len(probs) > 0 {
		return nil, nil, &Error{Problems: probs}
	}
	return acts, entries, nil
}

// Check は、値の全体の変数を、評価せず利用者に聞かずに確かめる。
// 表に無い名前（Unknown が Fail のとき）、Env が Fail のときの ${env:…}、名前の無い ${env}、Value の無い項目、
// Check を持つ項目の引数ごとの Check の誤りを、すべて並べた 1 つの *Error を返す。どれも無ければ nil を返す。
// Defer の変数は確かめない。
//
// req: FR-276
func Check(v any, r Resolver) error {
	_, _, err := check(Vars(v), r)
	return err
}

// Expand は、値の全体を 1 回の展開として展開した新しい値を返す。
// まず Check と同じ確かめをし、誤りがあればどの Value も呼ばずに返す。
// 通れば、変数を名前と引数の組ごとに 1 回だけ評価し、すべてを評価し終えてから置き換える。
// Defer の変数は文字のまま残す。置き換えた値の中の ${…} は展開し直さない。
// Value が誤りを返したら、残りを評価せずに、その変数の *Error を返す（どの値も置き換えない）。
//
// req: FR-276
func Expand(v any, r Resolver) (any, error) {
	vars := Vars(v)
	acts, entries, err := check(vars, r)
	if err != nil {
		return nil, err
	}
	values := make(map[Var]string, len(vars))
	for _, x := range vars {
		switch acts[x] {
		case actResolve:
			s, err := entries[x].Value(x.Arg)
			if err != nil {
				return nil, &Error{Problems: []Problem{{Var: x, Err: err}}}
			}
			values[x] = s
		case actEnv:
			values[x] = os.Getenv(x.Arg)
		case actDefer:
		}
	}
	return replace(v, acts, values), nil
}

// replace は、評価した値で置き換えた新しい値を作る。
func replace(v any, acts map[Var]action, values map[Var]string) any {
	switch x := v.(type) {
	case string:
		return replaceString(x, acts, values)
	case []any:
		if x == nil {
			return x
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = replace(e, acts, values)
		}
		return out
	case map[string]any:
		if x == nil {
			return x
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = replace(e, acts, values)
		}
		return out
	default:
		return v
	}
}

func replaceString(s string, acts map[Var]action, values map[Var]string) string {
	toks := parse(s)
	var b strings.Builder
	for _, t := range toks {
		if t.isVar && acts[t.v] != actDefer {
			b.WriteString(values[t.v])
			continue
		}
		b.WriteString(t.text)
	}
	return b.String()
}
