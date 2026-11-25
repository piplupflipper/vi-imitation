package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/expand"
)

// この節は、デバッグの構成の変数 ${input:ID}（VS Code の launch.json の inputs）の読み方と聞き方である（基本設計 6.4。FR-298、FR-307）。
// VS Code の文書「Variables reference」の「Input variables」（https://code.visualstudio.com/docs/reference/variables-reference）に合わせた。
//
// inputs は、.vscode/launch.json と利用者の設定（DAP の拡張機能の設定の inputs）に書ける。
// .vscode/launch.json の構成の ${input:ID} は、その launch.json の inputs を先に引き、無ければ設定の inputs を引く。
// 設定の構成と、アダプターの起動の方法の ${input:ID} は、設定の inputs だけを引く。
// 1 つの表の中に同じ id が 2 つあれば、後のものを使う（VS Code と同じ）。
//
// 項目の形は VS Code と同じで、id、type（promptString、pickString、command）、description、default、password、options を読む。
// command の種類の command と args は読まない（扱えない変数にする）。
// 利用者の設定の inputs にだけ、VS Code に無い項目 split（真偽）を書ける（FR-307。spliceSplit）。

// 入力の種類（inputs の type）である。
const (
	inputPromptString = "promptString"
	inputPickString   = "pickString"
	inputCommand      = "command"
)

// maxChoiceAsk は、choice/ask で聞く選択肢の数の上限である。choice/ask は番号のキーで選ぶ 9 つまでの問いなので、
// それより多い pickString の options は、中央のポップアップの一覧（pick）で選ばせる（基本設計 6.4 の #408 の決め）。
const maxChoiceAsk = 9

// errSplitInLaunchJSON は、.vscode/launch.json の inputs に split が書かれていることを表す（FR-307）。
var errSplitInLaunchJSON = errors.New("split は .vscode/launch.json の inputs では扱えない（利用者の設定の inputs にだけ書ける）")

// inputOption は、pickString の選択肢 1 つである。文字列の選択肢は label と value が同じになる。
type inputOption struct {
	label, value string
}

// display は、選択肢を利用者に示す文字列である。label と value が違えば「label: value」にする（文書の「The dropdown will display label: value.」）。
func (o inputOption) display() string {
	if o.label == "" || o.label == o.value {
		return o.value
	}
	return o.label + ": " + o.value
}

// inputDef は、inputs の項目 1 つである。
type inputDef struct {
	id          string
	kind        string
	description string
	// def は default の値で、hasDefault は default を書いたかである。
	def        string
	hasDefault bool
	password   bool
	options    []inputOption
	// split は、答えを ASCII の空白で分けて配列の複数の要素にするかである（VS Code に無い項目。FR-307）。
	split bool
	// invalid は、項目の形の誤りである。その ID の ${input:…} を使ったときに、扱えない変数として知らせる。
	invalid error
}

// inputTable は、1 つの出どころ（.vscode/launch.json か設定）の inputs を、書いた順に並べたものである。
type inputTable []inputDef

// find は、ID が id の項目を返す。同じ id が 2 つ以上あれば、後のものを返す（VS Code の
// baseConfigurationResolverService.ts の showUserInput が inputInfos.filter(...).pop() で後のものを使うのに合わせた）。
func (t inputTable) find(id string) (inputDef, bool) {
	for i := len(t) - 1; i >= 0; i-- {
		if t[i].id == id {
			return t[i], true
		}
	}
	return inputDef{}, false
}

// parseInputs は、inputs の値（配列）を読む。配列でない、要素がオブジェクトでない、id が文字列でないか空のときは誤りを返す。
// ほかの形の誤り（type が文字列でない、options が配列でない など）は、項目の invalid に覚え、その ID を使ったときに知らせる。
// allowSplit が偽（.vscode/launch.json の inputs）なら、split を書いた項目を扱えない項目にする（FR-307）。
//
// req: FR-298
func parseInputs(raw any, allowSplit bool) (inputTable, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.New("inputs is not an array")
	}
	var out inputTable
	for i, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("inputs[%d] is not an object", i)
		}
		id, _ := m["id"].(string)
		if id == "" {
			return nil, fmt.Errorf("inputs[%d].id is not a non-empty string", i)
		}
		out = append(out, parseInput(id, m, allowSplit))
	}
	return out, nil
}

// parseInput は、inputs の項目 1 つを読む。
func parseInput(id string, m map[string]any, allowSplit bool) inputDef {
	in := inputDef{id: id}
	bad := func(err error) {
		if in.invalid == nil {
			in.invalid = err
		}
	}
	badf := func(format string, args ...any) { bad(fmt.Errorf(format, args...)) }
	str := func(key string) string {
		v, ok := m[key]
		if !ok || v == nil {
			return ""
		}
		s, ok := v.(string)
		if !ok {
			badf("%s が文字列でない", key)
		}
		return s
	}
	flag := func(key string) bool {
		v, ok := m[key]
		if !ok || v == nil {
			return false
		}
		b, ok := v.(bool)
		if !ok {
			badf("%s が真偽でない", key)
		}
		return b
	}
	if _, has := m["split"]; has && !allowSplit {
		bad(errSplitInLaunchJSON)
	}
	in.kind = str("type")
	in.description = str("description")
	if v, ok := m["default"]; ok && v != nil {
		in.def, in.hasDefault = str("default"), true
	}
	in.password = flag("password")
	if allowSplit {
		in.split = flag("split")
	}
	if v, ok := m["options"]; ok && v != nil {
		arr, ok := v.([]any)
		if !ok {
			badf("options が配列でない")
		}
		for i, x := range arr {
			switch o := x.(type) {
			case string:
				in.options = append(in.options, inputOption{label: o, value: o})
			case map[string]any:
				value, vok := o["value"].(string)
				label, lok := o["label"].(string)
				if _, has := o["label"]; has && !lok {
					badf("options[%d].label が文字列でない", i)
				}
				if !vok {
					badf("options[%d].value が文字列でない", i)
				}
				in.options = append(in.options, inputOption{label: label, value: value})
			default:
				badf("options[%d] が文字列でもオブジェクトでもない", i)
			}
		}
	}
	return in
}

// check は、項目を利用者に聞けるかを、聞かずに確かめる（expand.Entry の Check）。
// command の種類と、password が真の promptString は扱えない（入力を隠して聞く口が無いため。基本設計 6.4）。
//
// req: FR-298
func (in inputDef) check() error {
	if in.invalid != nil {
		return in.invalid
	}
	switch in.kind {
	case inputPromptString:
		if in.password {
			return errors.New("password が真の promptString は扱えない（入力を隠して聞けない）")
		}
		if strings.ContainsAny(in.def, "\r\n") {
			return errors.New("default が改行を含む")
		}
	case inputPickString:
		if len(in.options) == 0 {
			return errors.New("pickString に options が無い")
		}
		if in.hasDefault && !slices.ContainsFunc(in.options, func(o inputOption) bool { return o.value == in.def }) {
			return fmt.Errorf("default %q が options の値に無い", in.def)
		}
	case inputCommand:
		return errors.New("command の種類は扱えない")
	case "":
		return errors.New("type が無い")
	default:
		return fmt.Errorf("type %q は扱えない", in.kind)
	}
	return nil
}

// title は、問いの文である。description（無ければ ID）の末尾に「: 」を足す。改行は空白にする（input/ask の title は改行を含められない）。
func (in inputDef) title() string {
	t := in.description
	if strings.TrimSpace(t) == "" {
		t = in.id
	}
	t = strings.TrimRight(strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(t), " ")
	if !strings.HasSuffix(t, ":") && !strings.HasSuffix(t, "：") {
		t += ":"
	}
	return t + " "
}

// 利用者に聞く変数の印である。
var (
	varPickProcess = expand.Var{Name: "pickProcess"}
)

// inputAsker は、1 回の開始の、利用者に聞く変数（${input:ID} と ${pickProcess}）を聞いて覚える。
// 同じ変数は 1 回だけ聞く（FR-298。文書の「If a variable occurs more than once, it is only evaluated once.」）。
// 1 回の開始の中だけで使い、debug.runLast と debug.restart は、聞き終えて値を当てた構成（runSpec）を使うので聞き直さない。
type inputAsker struct {
	d   *debugger
	ctx context.Context
	// launch は .vscode/launch.json の inputs（設定の構成では nil）、settings は設定の inputs である。
	launch, settings inputTable
	answers          map[expand.Var]string
}

// newInputAsker は、launch、settings の順に引いて聞く inputAsker を作る。d が nil なら聞けない（設定の例を確かめるテストの Check のため）。
func newInputAsker(ctx context.Context, d *debugger, launch, settings inputTable) *inputAsker {
	return &inputAsker{d: d, ctx: ctx, launch: launch, settings: settings, answers: map[expand.Var]string{}}
}

// lookup は、ID が id の項目を、.vscode/launch.json の inputs、設定の inputs の順に引く。
func (a *inputAsker) lookup(id string) (inputDef, bool) {
	if in, ok := a.launch.find(id); ok {
		return in, true
	}
	return a.settings.find(id)
}

// check は、${input:ID} の引数 id を、聞かずに確かめる（expand.Entry の Check）。
//
// req: FR-298
func (a *inputAsker) check(id string) error {
	if id == "" {
		return errors.New("ID が無い")
	}
	in, ok := a.lookup(id)
	if !ok {
		return fmt.Errorf("inputs に ID %q が無い", id)
	}
	return in.check()
}

// checkAdapter は、アダプターの起動の方法 adapter の ${input:ID} が、.vscode/launch.json の inputs に無いことを確かめる。
// アダプターの起動の方法は利用者の設定だけが決めるので（#399 の決め）、設定の inputs だけを引く。
// 構成と同じ 1 回の展開で同じ変数を 1 回だけ評価するので、launch.json の inputs に同じ ID があれば、どちらの項目で聞くかを決められず、誤りにする。
func (a *inputAsker) checkAdapter(adapter any) error {
	vars, err := expand.Vars(adapter)
	if err != nil {
		return err
	}
	var bad []string
	for _, v := range vars {
		if v.Name != "input" {
			continue
		}
		if _, ok := a.launch.find(v.Arg); ok {
			bad = append(bad, v.String())
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("アダプターの起動の方法の %s は、設定の inputs で引くが、.vscode/launch.json の inputs にも同じ ID がある", strings.Join(bad, "、"))
	}
	return nil
}

// inputValue と pickProcessValue は、expand.Entry の Value である。聞いていなければ聞く。
func (a *inputAsker) inputValue(id string) (string, error) {
	return a.get(expand.Var{Name: "input", Arg: id})
}

func (a *inputAsker) pickProcessValue(string) (string, error) { return a.get(varPickProcess) }

// get は、利用者に聞く変数 v の値を返す。聞いていなければ聞いて覚える。
func (a *inputAsker) get(v expand.Var) (string, error) {
	if s, ok := a.answers[v]; ok {
		return s, nil
	}
	if a.d == nil {
		return "", errors.New("聞けない")
	}
	var s string
	var err error
	if v == varPickProcess {
		s, err = a.d.pickProcess(a.ctx)
	} else {
		in, ok := a.lookup(v.Arg)
		if !ok {
			return "", fmt.Errorf("inputs に ID %q が無い", v.Arg)
		}
		s, err = a.ask(in)
	}
	if err != nil {
		return "", err
	}
	a.answers[v] = s
	return s, nil
}

// interactive は、v が利用者に聞く変数（${input:ID} か ${pickProcess}）かを返す。
func interactive(v expand.Var) bool { return v.Name == "input" || v == varPickProcess }

// writtenVars は、構成の JSON の文字列 raw に、利用者に聞く変数が書かれた順（重ねない）を返す。
// JSON に読むと map のキーの順が失われるので、読む前の文字列から取る。
func writtenVars(raw []byte) []expand.Var {
	vars, err := expand.Vars(string(raw))
	if err != nil {
		return nil
	}
	var out []expand.Var
	for _, v := range vars {
		if interactive(v) {
			out = append(out, v)
		}
	}
	return out
}

// askAll は、値 unit の利用者に聞く変数を、展開の前に聞いて覚える。
// 聞く順は、構成に書いた順 written（VS Code が構成を書いた順にたどって変数を集め、出てきた順に聞くのに合わせた。
// configurationResolverExpression.ts と baseConfigurationResolverService.ts の resolveWithInteraction）で、
// written に無いもの（アダプターの起動の方法、1 組の設定の構成）はその後に聞く。
// 取り消したら errPickCancelled を返し、残りを聞かない。
//
// req: FR-298
func (a *inputAsker) askAll(unit any, written []expand.Var) error {
	vars, err := expand.Vars(unit)
	if err != nil {
		return err
	}
	var order []expand.Var
	for _, v := range append(slices.Clone(written), vars...) {
		if interactive(v) && slices.Contains(vars, v) && !slices.Contains(order, v) {
			order = append(order, v)
		}
	}
	for _, v := range order {
		if _, err := a.get(v); err != nil {
			return fmt.Errorf("%s: %w", v, err)
		}
	}
	return nil
}

// ask は、項目 in を利用者に聞く。promptString は input/ask（FR-275）で、description を問いの文、default を初めの中身にして聞く。
// pickString は、default の選択肢を先頭に置き、9 つまでなら choice/ask で、それより多ければ中央のポップアップの一覧で選ばせる。
// 取り消したら errPickCancelled を返す。
//
// req: FR-298
func (a *inputAsker) ask(in inputDef) (string, error) {
	switch in.kind {
	case inputPromptString:
		res, err := a.d.srv.AskInput(a.ctx, ext.InputAskParams{Title: in.title(), Default: in.def})
		if err != nil {
			return "", err
		}
		if res.Cancelled {
			return "", errPickCancelled
		}
		return res.Value, nil
	case inputPickString:
		opts := slices.Clone(in.options)
		if in.hasDefault {
			if i := slices.IndexFunc(opts, func(o inputOption) bool { return o.value == in.def }); i > 0 {
				o := opts[i]
				opts = slices.Insert(slices.Delete(opts, i, i+1), 0, o)
			}
		}
		title := strings.TrimSuffix(in.title(), " ")
		var id string
		if len(opts) <= maxChoiceAsk {
			choices := make([]ext.Choice, len(opts))
			for i, o := range opts {
				choices[i] = ext.Choice{ID: strconv.Itoa(i), Label: o.display()}
			}
			res, err := a.d.srv.AskChoice(a.ctx, ext.ChoiceAskParams{Title: title, Choices: choices})
			if err != nil {
				return "", err
			}
			if res.Cancelled {
				return "", errPickCancelled
			}
			id = res.Selected
		} else {
			items := make([]ext.ViewItem, len(opts))
			for i, o := range opts {
				items[i] = ext.ViewItem{ID: strconv.Itoa(i), Label: o.display()}
			}
			var err error
			if id, err = a.d.pick(a.ctx, strings.TrimSuffix(title, ":"), items); err != nil {
				return "", err
			}
		}
		i, err := strconv.Atoi(id)
		if err != nil || i < 0 || i >= len(opts) {
			return "", fmt.Errorf("選んだ選択肢が分からない: %q", id)
		}
		return opts[i].value, nil
	default:
		return "", in.check()
	}
}

// splitASCII は、答え s を ASCII の空白（U+0020）とその連続で分ける。空の要素は捨てる（nvim の trimempty と同じ）。
// 全角の空白とタブでは分けず、引用符とエスケープは解かない（FR-307。オーナーの nvim の vim.split(…, " ") と同じ）。
func splitASCII(s string) []string {
	var out []string
	for f := range strings.SplitSeq(s, " ") {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// spliceSplit は、展開の前の値 before で、配列の要素の全体が split の項目の ${input:ID} だけだった所を、
// 展開した値 after の中で、答えを ASCII の空白で分けた複数の要素に置き換える（空白だけの答えは要素を消す）。
// 配列の要素でない所と、ほかの文字と混ざった所は、答えを 1 つの文字列のまま入れる（VS Code と同じ）。
//
// req: FR-307
func (a *inputAsker) spliceSplit(after, before any) any {
	switch b := before.(type) {
	case map[string]any:
		m, ok := after.(map[string]any)
		if !ok {
			return after
		}
		for k, v := range m {
			if bv, ok := b[k]; ok {
				m[k] = a.spliceSplit(v, bv)
			}
		}
		return m
	case []any:
		arr, ok := after.([]any)
		if !ok || len(arr) != len(b) {
			return after
		}
		out := make([]any, 0, len(arr))
		for i := range arr {
			if s, ok := b[i].(string); ok && a.splitVar(s) {
				if v, ok := expand.Plain(arr[i]).(string); ok {
					for _, f := range splitASCII(v) {
						out = append(out, f)
					}
					continue
				}
			}
			out = append(out, a.spliceSplit(arr[i], b[i]))
		}
		return out
	default:
		return after
	}
}

// splitVar は、s の全体が、split が真の項目を指す ${input:ID} だけかを返す。
func (a *inputAsker) splitVar(s string) bool {
	id, ok := strings.CutPrefix(s, "${input:")
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, "}")
	if !ok || strings.ContainsAny(id, "{}") {
		return false
	}
	in, found := a.lookup(id)
	return found && in.split
}
