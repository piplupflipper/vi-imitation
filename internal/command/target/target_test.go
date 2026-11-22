package target

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// parse は、"|" でカーソルの位置を示したテキストから、"|" を除いたバッファと、カーソルの位置を返す。
func parse(t *testing.T, s string) (*buffer.Buffer, buffer.Position) {
	t.Helper()
	before, after, ok := strings.Cut(s, "|")
	if !ok || strings.Contains(after, "|") {
		t.Fatalf("text %q must have exactly one cursor marker", s)
	}
	p := buffer.Position{Line: strings.Count(before, "\n"), Byte: len(before) - strings.LastIndexByte(before, '\n') - 1}
	return buffer.New(before + after), p
}

// mark は、バッファのテキストに、位置 p を表す "|" を入れて返す。
func mark(b *buffer.Buffer, p buffer.Position) string {
	lines := make([]string, b.LineCount())
	for i := range lines {
		lines[i] = b.Line(i)
	}
	lines[p.Line] = lines[p.Line][:p.Byte] + "|" + lines[p.Line][p.Byte:]
	return strings.Join(lines, "\n")
}

// inputTable は、internal/input/builtin.go の本体の操作の表 name（builtinMotions など）から、キーの列と ID の対応を読む。
//
// internal/input は internal/command と同じ層なので、テストでも import しない（基本設計 2 章）。
// 代わりにソースを構文解析して、表の keys と id の欄を取り出す。
func inputTable(t *testing.T, name string) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "..", "input", "builtin.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse internal/input/builtin.go: %v", err)
	}
	table := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != name || len(vs.Values) != 1 {
			return true
		}
		lit, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			t.Fatalf("%s is not a composite literal", name)
		}
		for _, elt := range lit.Elts {
			fields := map[string]string{}
			row, ok := elt.(*ast.CompositeLit)
			if !ok {
				t.Fatalf("%s has a row that is not a composite literal", name)
			}
			for _, f := range row.Elts {
				kv, ok := f.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok1 := kv.Key.(*ast.Ident)
				val, ok2 := kv.Value.(*ast.BasicLit)
				if ok1 && ok2 && val.Kind == token.STRING {
					fields[key.Name], _ = strconv.Unquote(val.Value)
				}
			}
			table[fields["keys"]] = fields["id"]
		}
		return false
	})
	if len(table) == 0 {
		t.Fatalf("table %s not found in internal/input/builtin.go", name)
	}
	return table
}

// motionID は、モーションのキーの列を、internal/input の表で ID に直す。
func motionID(t *testing.T, keys string) string {
	t.Helper()
	id, ok := inputTable(t, "builtinMotions")[keys]
	if !ok {
		t.Fatalf("motion %q is not in internal/input", keys)
	}
	return id
}

// textObjectAliases は、Vim の別名のキーを、internal/input の表にあるキーへ直す。
// 別名をキーに割り当てるのは internal/input の担当で、この表はテストの読みやすさのためだけにある。
var textObjectAliases = map[string]string{"i)": "i(", "a)": "a(", "ib": "i(", "ab": "a(", "i]": "i[", "a]": "a[", "i}": "i{", "a}": "a{", "iB": "i{", "aB": "a{"}

// textObjectID は、テキストオブジェクトのキーの列を、internal/input の表で ID に直す。
func textObjectID(t *testing.T, keys string) string {
	t.Helper()
	if k, ok := textObjectAliases[keys]; ok {
		keys = k
	}
	id, ok := inputTable(t, "builtinTextObjects")[keys]
	if !ok {
		t.Fatalf("text object %q is not in internal/input", keys)
	}
	return id
}

// count は、テストの表の回数（0 は省略）を、Request の Count と CountGiven にする。
func count(n int) (int, bool) {
	return max(n, 1), n > 0
}

// TestLookupInputIDs は、internal/input の表の全ての ID（Command.Target.Ref.ID に入る値）で、このパッケージの表を引けることと、
// このパッケージの表に internal/input に無い ID が無いことを確かめる。
//
// req: FR-03
func TestLookupInputIDs(t *testing.T) {
	tests := []struct {
		table  string
		lookup func(string) bool
		names  []string
	}{
		{"builtinMotions", func(id string) bool { _, ok := LookupMotion(id); return ok }, MotionNames()},
		{"builtinTextObjects", func(id string) bool { _, ok := LookupTextObject(id); return ok }, TextObjectNames()},
	}
	for _, tt := range tests {
		ids := map[string]bool{}
		for keys, id := range inputTable(t, tt.table) {
			ids[id] = true
			if !tt.lookup(id) {
				t.Errorf("%s: %q (%s) cannot be looked up", tt.table, id, keys)
			}
		}
		if !slices.IsSorted(tt.names) {
			t.Errorf("names %q are not sorted", tt.names)
		}
		for _, name := range tt.names {
			if !ids[name] {
				t.Errorf("%q is not in internal/input %s", name, tt.table)
			}
		}
	}
	if _, ok := LookupMotion("w"); ok {
		t.Error(`LookupMotion("w") found; the table must use IDs, not keys`)
	}
	if _, ok := LookupTextObject("inner-zzz"); ok {
		t.Error(`LookupTextObject("inner-zzz") found`)
	}
}

func TestInvalidRequest(t *testing.T) {
	b := buffer.New("あい\nb")
	w, _ := LookupMotion("word-next")
	iw, _ := LookupTextObject("inner-word")
	tests := []Request{
		{Cursor: buffer.Position{Line: 2}},
		{Cursor: buffer.Position{Line: -1}},
		{Cursor: buffer.Position{Line: 0, Byte: 1}}, // あ の途中
		{Cursor: buffer.Position{Line: 1, Byte: 2}},
		{Count: -1},
	}
	for _, req := range tests {
		if _, err := w(b, req); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("w(%+v) error = %v, want ErrInvalidRequest", req, err)
		}
		if _, err := iw(b, req); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("iw(%+v) error = %v, want ErrInvalidRequest", req, err)
		}
	}
}

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{Exclusive: "exclusive", Inclusive: "inclusive", Linewise: "linewise", Kind(9): "Kind(9)"} {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}

// span は、start から end までの kind の範囲を返す。
func span(start, end buffer.Position, kind Kind) Span {
	return Span{Start: start, End: end, Kind: kind}
}

func TestSpanRange(t *testing.T) {
	b := buffer.New("abc\nあい\n")
	tests := []struct {
		span Span
		want string
	}{
		{span(pos(0, 1), pos(0, 2), Exclusive), "b"},
		{span(pos(0, 1), pos(0, 2), Inclusive), "bc"},
		{span(pos(0, 1), pos(0, 3), Inclusive), "bc"}, // 行末の位置は改行を含めない
		{span(pos(0, 2), pos(1, 3), Inclusive), "c\nあい"},
		{span(pos(0, 2), pos(1, 0), Linewise), "abc\nあい"},
	}
	for _, tt := range tests {
		got, err := b.Slice(tt.span.Range(b))
		if err != nil || got != tt.want {
			t.Errorf("%+v.Range() text = %q, %v, want %q", tt.span, got, err, tt.want)
		}
	}
}

// req: FR-03
func TestAdjustExclusive(t *testing.T) {
	tests := []struct {
		name string
		text string
		span Span
		want Span
	}{
		{
			name: "字下げの中から次の行の先頭までは行単位",
			text: "  a\nb\n",
			span: span(pos(0, 1), pos(1, 0), Exclusive),
			want: span(pos(0, 1), pos(0, 0), Linewise),
		},
		{
			name: "行の途中から次の行の先頭までは前の行の最後の文字まで",
			text: "a あ\nb",
			span: span(pos(0, 1), pos(1, 0), Exclusive),
			want: span(pos(0, 1), pos(0, 2), Inclusive),
		},
		{
			name: "前の行が空なら空の行の先頭まで",
			text: "a b\n\nc",
			span: span(pos(0, 2), pos(2, 0), Exclusive),
			want: span(pos(0, 2), pos(1, 0), Exclusive),
		},
		{
			name: "同じ行の中は変えない",
			text: "abc",
			span: span(pos(0, 0), pos(0, 0), Exclusive),
			want: span(pos(0, 0), pos(0, 0), Exclusive),
		},
		{
			name: "Inclusive は変えない",
			text: "a\nb",
			span: span(pos(0, 0), pos(1, 0), Inclusive),
			want: span(pos(0, 0), pos(1, 0), Inclusive),
		},
	}
	for _, tt := range tests {
		if got := AdjustExclusive(buffer.New(tt.text), tt.span); got != tt.want {
			t.Errorf("%s: AdjustExclusive(%+v) = %+v, want %+v", tt.name, tt.span, got, tt.want)
		}
	}
}

// req: FR-03
func TestOperatorSpan(t *testing.T) {
	tests := []struct {
		name   string
		motion string
		text   string
		op     Operator
		want   string // オペレーターが働く範囲のテキスト
		kind   Kind
	}{
		{"dw は次の単語の手前まで", "w", "|foo bar", AnyOperator, "foo ", Exclusive},
		{"dw は行末を越えない", "w", "foo |bar\nbaz", AnyOperator, "bar", Inclusive},
		{"dw はバッファの最後の単語を消せる", "w", "foo |bar", AnyOperator, "bar", Inclusive},
		{"dw は最後の 1 文字を消せる", "w", "ab|c", AnyOperator, "c", Inclusive},
		{"cw は ce と同じ", "w", "|foo bar", ChangeOperator, "foo", Inclusive},
		{"cw は単語の終わりではその 1 文字", "w", "fo|o bar", ChangeOperator, "o", Inclusive},
		{"cw は空白の上では dw と同じ", "w", "foo|  bar", ChangeOperator, "  ", Exclusive},
		{"db は後ろへ", "b", "foo |bar", AnyOperator, "foo ", Exclusive},
		{"dl は最後の文字を消せる", "l", "ab|c", AnyOperator, "c", Exclusive},
		{"d} は行単位", "}", "|a\nb\n\nc", AnyOperator, "a\nb", Linewise},
		{"d} は行の途中からなら前の行の終わりまで", "}", "x |a\nb\n\nc", AnyOperator, "a\nb", Inclusive},
		{"de", "e", "|foo bar", AnyOperator, "foo", Inclusive},
		{"de は最後の 1 文字を消せる", "e", "fo|o", AnyOperator, "o", Inclusive},
		{"d$", "$", "a|bc\nd", AnyOperator, "bc", Inclusive},
		{"dj は行単位", "j", "a|b\ncd\ne", AnyOperator, "ab\ncd", Linewise},
	}
	for _, tt := range tests {
		b, cur := parse(t, tt.text)
		f, _ := LookupMotion(motionID(t, tt.motion))
		res, err := f(b, Request{Cursor: cur, Count: 1, Operator: tt.op})
		if err != nil {
			t.Errorf("%s: %s error = %v", tt.name, tt.motion, err)
			continue
		}
		span := OperatorSpan(b, cur, res)
		got, err := b.Slice(span.Range(b))
		if err != nil || got != tt.want || span.Kind != tt.kind {
			t.Errorf("%s: text = %q (%v), %v, want %q (%v)", tt.name, got, span.Kind, err, tt.want, tt.kind)
		}
	}
}

func pos(line, b int) buffer.Position { return buffer.Position{Line: line, Byte: b} }
