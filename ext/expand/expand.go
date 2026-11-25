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
//   - 入れ子（${env:${input:x}}）は持たない。文書は「Nesting of input variables is not supported.」とだけ書き、どう扱うかを書いていない。
//     引数は最初の } で終わるので、${env:${input:x}} は引数が ${input:x の変数 env と、文字の } になる。
//     評価する変数（Defer でないもの）の引数が ${ を含めば、Check の誤り（ErrNested）にする（仮）
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
// # 残した変数と 2 段目の展開
//
// Defer で残した変数を含む文字列と、置き換えた結果を読み直すと変数に見える ${…} を含む文字列（${file} の値が /tmp/${port}/a.go のとき）は、
// Expand の結果で string でなく Text になる。それ以外の文字列は string になる。
// Text は、残した変数の位置を覚え、置き換えた値と地の文を変数として読み直さない。
// Expand に Text を含む値を渡すと、残した変数の位置だけを変数として扱う（2 段目の展開）。
// このため、1 段目で置き換えた値の中の ${port}（${file} が /tmp/${port}/a.go のとき）は、2 段目でも置き換わらない。
// 1 段目を通らない値（外部のコマンドが返した値）は string のまま Expand に渡せば、その中の変数を読む。
// 1 段目の結果の表に外部のコマンドの値を重ねてから、1 回の Expand で 2 段目を当てられる。
// Text は JSON では元の書き方の文字列（${port} を含む）になる（MarshalJSON）。
// Text を JSON に書いて読み直すと string に戻り、位置を失うので、位置を覚えておくには Expand の結果の値をそのまま持つ。
//
// # JSON の値
//
// Expand、Vars、Check は、encoding/json で any に読んだ値（map[string]any、[]any、string、float64、json.Number、bool、nil）と、
// Expand が返した Text と、Go の数の型を受ける。
// それ以外の型（[]string、map[string]string、構造体など）は、中の変数を見落とさないよう、経路と型を添えた誤り（*TypeError）にする（仮）。
// 構造体で持つ設定は、JSON に書いて any に読み直してから渡す。
//
// オブジェクトの値と配列の要素の文字列を、入れ子をたどって展開する。
// オブジェクトのキーは展開しない（基本設計の決め。文書は「some key and value strings」で置き換えるとしているが、どのキーかを書いていない）。
// 配列の 1 つの要素は、展開した値が空白を含んでも 1 つの要素のまま（文書の「It doesn't add quotes or split the result on spaces.」）。
// 文字列の全体が 1 つの変数でも、展開した結果は文字列で、数にしない。
// Expand は、渡した値を書き換えず、新しい map と slice を作って返す。
package expand

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
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
// 下の定数でない値と、Unknown の Builtin は、Check と Expand が誤りにする。
type Handling int

const (
	// Fail は、変数を誤りにする（Unknown の既定）。Env に書くと、${env:…} は知らない名前と同じ誤りになる。
	Fail Handling = iota + 1
	// Defer は、変数を置き換えずに残す。後の段階で別の展開にかけるためである（Text）。
	Defer
	// Builtin は、このパッケージが環境変数を読んで ${env:名前} を置き換える（Env の既定。Env にだけ書ける）。
	// 値の無い環境変数は空の文字列にする（FR-276 の仮）。
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
	// Defer が真なら、この名前の変数は置き換えずに残す（Value と Check は呼ばない）。
	Defer bool
}

// Resolver は、展開する変数の意味を、呼び出す側が渡す口である。
type Resolver struct {
	// Vars は、名前ごとの項目の表である。名前 env の項目は見ない（${env:…} は Env で扱う。仮）。
	Vars map[string]Entry
	// Unknown は、Vars に無い名前（env を除く）の扱いである。0 は Fail。Fail か Defer を書ける。
	Unknown Handling
	// Env は、${env:名前} の扱いである。0 は Builtin。
	Env Handling
}

// validate は、Resolver の Unknown と Env が扱える値かを確かめる。
func (r Resolver) validate() error {
	switch r.Unknown {
	case 0, Fail, Defer:
	case Builtin:
		return errors.New("expand: Resolver.Unknown に Builtin は書けない")
	default:
		return fmt.Errorf("expand: Resolver.Unknown の値 %d は扱えない", int(r.Unknown))
	}
	switch r.Env {
	case 0, Fail, Defer, Builtin:
	default:
		return fmt.Errorf("expand: Resolver.Env の値 %d は扱えない", int(r.Env))
	}
	return nil
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

// TypeError は、値の中に扱えない型があったことの誤りである。Vars、Check、Expand が返す。
type TypeError struct {
	// Path は、値の根からの経路（$.adapter.args[0] の形）である。
	Path string
	// Type は、扱えない値の型の名前（%T）である。
	Type string
}

func (e *TypeError) Error() string {
	return fmt.Sprintf("expand: %s の型 %s は扱えない（map[string]any、[]any、string に直して渡す）", e.Path, e.Type)
}

// ErrUnknown は、知らない名前の変数の理由である。
var ErrUnknown = errors.New("知らない変数")

// ErrNested は、評価する変数の引数が ${ を含む（入れ子の）ことの理由である。
var ErrNested = errors.New("入れ子の変数は扱えない")

// errNoValue は、Value を持たない項目の理由である。
var errNoValue = errors.New("値を決められない変数")

// errEnvName は、${env} と ${env:} の理由である。
var errEnvName = errors.New("環境変数の名前が無い")

// frag は、文字列を変数と地の文に分けた 1 つの断片である。
// isVar が偽なら text は地の文で、真なら text は変数の原文（残すときに書くため）である。
type frag struct {
	text  string
	v     Var
	isVar bool
}

// Text は、Defer で残した変数の位置を覚えた文字列である。Expand が返す。
// 地の文（置き換えた値を含む）は、後の Expand でも変数として読まない。
// 0 の値は空の文字列である。
type Text struct {
	frags []frag
}

// String は、残した変数を元の書き方に戻した文字列を返す。
func (t Text) String() string {
	var b strings.Builder
	for _, f := range t.frags {
		b.WriteString(f.text)
	}
	return b.String()
}

// MarshalJSON は、String の文字列を JSON の文字列として書く。
func (t Text) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// Vars は、Text に残した変数を、重ねずに現れた順に返す。
func (t Text) Vars() []Var {
	var out []Var
	seen := map[Var]bool{}
	for _, f := range t.frags {
		if f.isVar && !seen[f.v] {
			seen[f.v] = true
			out = append(out, f.v)
		}
	}
	return out
}

// isNameByte は、名前に使える文字かを返す。
func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '/'
}

// parse は、文字列を変数と地の文の断片に分ける。変数でない ${ は地の文に残す。
// 読む時間は文字列の長さに比例する（後ろに } の無い ${ を見たら、その後に変数は無いので読み終える）。
func parse(s string) []frag {
	var out []frag
	start := 0 // まだ断片にしていない地の文の始まり
	i := 0
	for {
		k := strings.Index(s[i:], "${")
		if k < 0 {
			break
		}
		i += k
		v, end, st := parseVar(s, i)
		if st == varNoClose {
			break
		}
		if st == varNot {
			// 変数でない。$ を地の文にして、{ から読み続ける（${a ${b} の ${b} を拾うため）。
			i++
			continue
		}
		if start < i {
			out = append(out, frag{text: s[start:i]})
		}
		out = append(out, frag{text: s[i:end], v: v, isVar: true})
		i = end
		start = end
	}
	if start < len(s) {
		out = append(out, frag{text: s[start:]})
	}
	return out
}

// parseVar の結果の状態である。
const (
	varOK      = iota
	varNot     // ${ の後が変数の書き方でない
	varNoClose // ${ の後ろに } が無い（その後に変数は無い）
)

// parseVar は、s[i:] の先頭の ${ から 1 つの変数を読み、変数と、その後の位置を返す。
func parseVar(s string, i int) (Var, int, int) {
	j := i + 2
	for j < len(s) && isNameByte(s[j]) {
		j++
	}
	if j >= len(s) {
		return Var{}, 0, varNoClose
	}
	if j == i+2 {
		return Var{}, 0, varNot
	}
	name := s[i+2 : j]
	switch s[j] {
	case '}':
		return Var{Name: name}, j + 1, varOK
	case ':':
		k := strings.IndexByte(s[j+1:], '}')
		if k < 0 {
			return Var{}, 0, varNoClose
		}
		return Var{Name: name, Arg: s[j+1 : j+1+k]}, j + 1 + k + 1, varOK
	default:
		return Var{}, 0, varNot
	}
}

// fragments は、文字列を 1 回だけ読んだ断片の列である。compile が string と Text をこれにする。
type fragments []frag

// compile は、値の全体を、文字列を断片の列にした新しい値にする。扱えない型は *TypeError にする。
func compile(v any, path string) (any, error) {
	switch x := v.(type) {
	case string:
		return fragments(parse(x)), nil
	case Text:
		return fragments(x.frags), nil
	case []any:
		if x == nil {
			return x, nil
		}
		out := make([]any, len(x))
		for i, e := range x {
			c, err := compile(e, path+"["+strconv.Itoa(i)+"]")
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	case map[string]any:
		if x == nil {
			return x, nil
		}
		out := make(map[string]any, len(x))
		for _, k := range sortedKeys(x) {
			c, err := compile(x[k], path+"."+k)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case nil, bool, json.Number, float64, float32,
		int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return v, nil
	default:
		return nil, &TypeError{Path: path, Type: fmt.Sprintf("%T", v)}
	}
}

// collect は、compile した値の変数を、map のキーの辞書順と配列の順で、重ねずに並べる。
func collect(c any) []Var {
	var out []Var
	seen := map[Var]bool{}
	var walk func(any)
	walk = func(c any) {
		switch x := c.(type) {
		case fragments:
			for _, f := range x {
				if f.isVar && !seen[f.v] {
					seen[f.v] = true
					out = append(out, f.v)
				}
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, k := range sortedKeys(x) {
				walk(x[k])
			}
		}
	}
	walk(c)
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Vars は、値の全体に書かれた変数（Text では残した変数だけ）を、評価せずに重ねずに並べる。
// 並びは現れた順で、map はキーの辞書順にたどる。扱えない型があれば *TypeError を返す。
func Vars(v any) ([]Var, error) {
	c, err := compile(v, "$")
	if err != nil {
		return nil, err
	}
	return collect(c), nil
}

// action は、1 つの変数をどう扱うかである。
type action int

const (
	actResolve action = iota // Entry.Value で評価する
	actEnv                   // 環境変数を読む
	actDefer                 // 残す
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
		default: // 0 は既定の Builtin（範囲の外は validate が先に断る）
		}
		if v.Arg == "" {
			return 0, Entry{}, errEnvName
		}
		if strings.Contains(v.Arg, "${") {
			return 0, Entry{}, ErrNested
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
	if strings.Contains(v.Arg, "${") {
		return 0, e, ErrNested
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

// prepare は、Resolver と値を確かめ、compile した値と変数ごとの扱いを返す。
func prepare(v any, r Resolver) (any, []Var, map[Var]action, map[Var]Entry, error) {
	if err := r.validate(); err != nil {
		return nil, nil, nil, nil, err
	}
	c, err := compile(v, "$")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	vars := collect(c)
	acts, entries, err := check(vars, r)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return c, vars, acts, entries, nil
}

// Check は、値の全体の変数を、評価せず利用者に聞かずに確かめる。
// 表に無い名前（Unknown が Fail のとき）、Env が Fail のときの ${env:…}、名前の無い ${env}、Value の無い項目、
// 評価する変数の入れ子（ErrNested）、Check を持つ項目の引数ごとの Check の誤りを、すべて並べた 1 つの *Error を返す。
// Defer の変数は確かめない。
// Resolver の Unknown か Env が扱えない値なら、値に扱えない型があれば（*TypeError）、変数を確かめる前にその誤りを返す。
// どれも無ければ nil を返す。
//
// req: FR-276
func Check(v any, r Resolver) error {
	_, _, _, _, err := prepare(v, r)
	return err
}

// Expand は、値の全体を 1 回の展開として展開した新しい値を返す。
// まず Check と同じ確かめをし、誤りがあればどの Value も呼ばずに返す。
// 通れば、変数を名前と引数の組ごとに 1 回だけ評価し、すべてを評価し終えてから置き換える。
// Defer の変数を含む文字列と、読み直すと変数に見える ${…} を含む結果は、その位置を覚えた Text にする。それ以外の文字列は string にする。
// 置き換えた値の中の ${…} は、この展開でも後の展開でも変数として読まない。
// Value が誤りを返したら、残りを評価せずに、その変数の *Error を返す（どの値も置き換えない）。
//
// req: FR-276
func Expand(v any, r Resolver) (any, error) {
	c, vars, acts, entries, err := prepare(v, r)
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
	return render(c, acts, values), nil
}

// render は、compile した値を、評価した値で置き換えた新しい値にする。
func render(c any, acts map[Var]action, values map[Var]string) any {
	switch x := c.(type) {
	case fragments:
		return renderString(x, acts, values)
	case []any:
		if x == nil {
			return x
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = render(e, acts, values)
		}
		return out
	case map[string]any:
		if x == nil {
			return x
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = render(e, acts, values)
		}
		return out
	default:
		return c
	}
}

// renderString は、残した変数が無ければ string を、あれば Text を返す。隣り合う地の文は 1 つにまとめる。
func renderString(fs fragments, acts map[Var]action, values map[Var]string) any {
	var out []frag
	var lit strings.Builder
	for _, f := range fs {
		switch {
		case !f.isVar:
			lit.WriteString(f.text)
		case acts[f.v] != actDefer:
			lit.WriteString(values[f.v])
		default:
			if lit.Len() > 0 {
				out = append(out, frag{text: lit.String()})
				lit.Reset()
			}
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		s := lit.String()
		// 置き換えた値が ${…} を含み、読み直すと変数に見えるなら、地の文だけの Text にして、後の展開で読ませない。
		for _, f := range parse(s) {
			if f.isVar {
				return Text{frags: []frag{{text: s}}}
			}
		}
		return s
	}
	if lit.Len() > 0 {
		out = append(out, frag{text: lit.String()})
	}
	return Text{frags: out}
}
