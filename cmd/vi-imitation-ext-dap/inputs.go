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

// この節は、デバッグの構成の変数 ${input:ID}（VS Code の launch.json の inputs）の読み方と聞き方である（基本設計 6.4。FR-298）。
// VS Code の文書「Variables reference」の「Input variables」（https://code.visualstudio.com/docs/reference/variables-reference）に合わせた。
//
// inputs は、.vscode/launch.json と利用者の設定（DAP の拡張機能の設定の inputs）に書ける。
// .vscode/launch.json の構成の ${input:ID} は、その launch.json の inputs を先に引き、無ければ設定の inputs を引く。
// 設定の構成は、設定の inputs だけを引く。
//
// 項目の形は VS Code と同じで、id、type（promptString、pickString、command）、description、default、password、options を読む。
// command の種類の command と args は読まない（扱えない変数にする）。
// VS Code に無い項目 split（真偽）を足した。真なら、配列の要素の全体が ${input:ID} だけのとき、答えを空白で分けた複数の要素にする（spliceSplit）。

// 入力の種類（inputs の type）である。
const (
	inputPromptString = "promptString"
	inputPickString   = "pickString"
	inputCommand      = "command"
)

// maxChoiceAsk は、choice/ask で聞く選択肢の数の上限である。choice/ask は番号のキーで選ぶ 9 つまでの問いなので、
// それより多い pickString の options は、中央のポップアップの一覧（pick）で選ばせる（基本設計 6.4 の #408 の決め）。
const maxChoiceAsk = 9

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
	// split は、答えを空白で分けて配列の複数の要素にするかである（VS Code に無い項目）。
	split bool
	// invalid は、項目の形の誤りである。その ID の ${input:…} を使ったときに、扱えない変数として知らせる。
	invalid error
}

// inputTable は、引く順に並べた inputs である。同じ ID が 2 つあれば、前のものを使う。
type inputTable []inputDef

// find は、ID が id の項目と、表の中の位置を返す。
func (t inputTable) find(id string) (inputDef, int, bool) {
	for i, in := range t {
		if in.id == id {
			return in, i, true
		}
	}
	return inputDef{}, -1, false
}

// parseInputs は、inputs の値（配列）を読む。配列でない、要素がオブジェクトでない、id が文字列でないか空のときは誤りを返す。
// ほかの形の誤り（type が文字列でない、options が配列でない など）は、項目の invalid に覚え、その ID を使ったときに知らせる。
//
// req: FR-298
func parseInputs(raw any) (inputTable, error) {
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
		out = append(out, parseInput(id, m))
	}
	return out, nil
}

// parseInput は、inputs の項目 1 つを読む。
func parseInput(id string, m map[string]any) inputDef {
	in := inputDef{id: id}
	bad := func(format string, args ...any) {
		if in.invalid == nil {
			in.invalid = fmt.Errorf(format, args...)
		}
	}
	str := func(key string) string {
		v, ok := m[key]
		if !ok || v == nil {
			return ""
		}
		s, ok := v.(string)
		if !ok {
			bad("%s が文字列でない", key)
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
			bad("%s が真偽でない", key)
		}
		return b
	}
	in.kind = str("type")
	in.description = str("description")
	if v, ok := m["default"]; ok && v != nil {
		in.def, in.hasDefault = str("default"), true
	}
	in.password = flag("password")
	in.split = flag("split")
	if v, ok := m["options"]; ok && v != nil {
		arr, ok := v.([]any)
		if !ok {
			bad("options が配列でない")
		}
		for i, x := range arr {
			switch o := x.(type) {
			case string:
				in.options = append(in.options, inputOption{label: o, value: o})
			case map[string]any:
				value, vok := o["value"].(string)
				label, lok := o["label"].(string)
				if _, has := o["label"]; has && !lok {
					bad("options[%d].label が文字列でない", i)
				}
				if !vok {
					bad("options[%d].value が文字列でない", i)
				}
				in.options = append(in.options, inputOption{label: label, value: value})
			default:
				bad("options[%d] が文字列でもオブジェクトでもない", i)
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

// inputAsker は、1 回の開始の ${input:ID} を聞いて覚える。同じ ID は 1 回だけ聞く（FR-298。文書の「If a variable occurs more than once, it is only evaluated once.」）。
// 1 回の開始の中だけで使い、debug.runLast と debug.restart は、聞き終えて値を当てた構成（runSpec）を使うので聞き直さない。
type inputAsker struct {
	d       *debugger
	ctx     context.Context
	table   inputTable
	answers map[string]string
}

// newInputAsker は、表 table を引いて聞く inputAsker を作る。d が nil なら聞けない（設定の例を確かめるテストの Check のため）。
func newInputAsker(ctx context.Context, d *debugger, table inputTable) *inputAsker {
	return &inputAsker{d: d, ctx: ctx, table: table, answers: map[string]string{}}
}

// check は、${input:ID} の引数 id を、聞かずに確かめる（expand.Entry の Check）。
//
// req: FR-298
func (a *inputAsker) check(id string) error {
	if id == "" {
		return errors.New("ID が無い")
	}
	in, _, ok := a.table.find(id)
	if !ok {
		return fmt.Errorf("inputs に ID %q が無い", id)
	}
	return in.check()
}

// value は、${input:ID} の値を返す（expand.Entry の Value）。聞いていなければ聞く。
func (a *inputAsker) value(id string) (string, error) {
	if v, ok := a.answers[id]; ok {
		return v, nil
	}
	in, _, ok := a.table.find(id)
	if !ok {
		return "", fmt.Errorf("inputs に ID %q が無い", id)
	}
	v, err := a.ask(in)
	if err != nil {
		return "", err
	}
	a.answers[id] = v
	return v, nil
}

// askAll は、値 unit の ${input:ID} を、inputs の表に書いた順に聞いて覚える（展開の前に呼ぶ）。
// 展開の口（ext/expand）の評価の順は値のキーの辞書順で意味を持たないので、利用者が順を決められるよう、表の順で先に聞く（仮）。
// 取り消したら errPickCancelled を返し、残りを聞かない。
//
// req: FR-298
func (a *inputAsker) askAll(unit any) error {
	vars, err := expand.Vars(unit)
	if err != nil {
		return err
	}
	var ids []string
	for _, v := range vars {
		if v.Name == "input" && !slices.Contains(ids, v.Arg) {
			ids = append(ids, v.Arg)
		}
	}
	slices.SortStableFunc(ids, func(x, y string) int {
		_, i, _ := a.table.find(x)
		_, j, _ := a.table.find(y)
		return i - j
	})
	for _, id := range ids {
		if _, err := a.value(id); err != nil {
			return fmt.Errorf("${input:%s}: %w", id, err)
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
	if a.d == nil {
		return "", errors.New("聞けない")
	}
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

// spliceSplit は、展開の前の値 before で、配列の要素の全体が split の項目の ${input:ID} だけだった所を、
// 展開した値 after の中で、答えを空白で分けた複数の要素に置き換える（空白だけの答えは要素を消す）。
// オーナーの nvim の vim.split(vim.fn.input("Args: "), " ", { trimempty = true }) に当たる。引用符は解かない（仮）。
// 配列の要素でない所と、ほかの文字と混ざった所は、答えを 1 つの文字列のまま入れる（VS Code と同じ）。
//
// req: FR-298
func spliceSplit(after, before any, table inputTable) any {
	switch b := before.(type) {
	case map[string]any:
		a, ok := after.(map[string]any)
		if !ok {
			return after
		}
		for k, v := range a {
			if bv, ok := b[k]; ok {
				a[k] = spliceSplit(v, bv, table)
			}
		}
		return a
	case []any:
		a, ok := after.([]any)
		if !ok || len(a) != len(b) {
			return after
		}
		out := make([]any, 0, len(a))
		for i := range a {
			if s, ok := b[i].(string); ok && splitVar(s, table) {
				if v, ok := expand.Plain(a[i]).(string); ok {
					for _, f := range strings.Fields(v) {
						out = append(out, f)
					}
					continue
				}
			}
			out = append(out, spliceSplit(a[i], b[i], table))
		}
		return out
	default:
		return after
	}
}

// splitVar は、s の全体が、split が真の項目を指す ${input:ID} だけかを返す。
func splitVar(s string, table inputTable) bool {
	id, ok := strings.CutPrefix(s, "${input:")
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, "}")
	if !ok || strings.ContainsAny(id, "{}") {
		return false
	}
	in, _, found := table.find(id)
	return found && in.split
}
