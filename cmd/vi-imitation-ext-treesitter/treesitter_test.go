package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

const testTimeout = 5 * time.Second

// bigTimeout は、大きなファイルの一覧を待つ時間の上限である。
// 一覧の計算は -race の下で遅く、CI の ubuntu-latest では 2 万行の JSON で 5 秒を超えた。
const bigTimeout = 60 * time.Second

// goSource は、Go の文法の主な要素を含むソースである。
// 行 4 に多バイト文字を置き、位置がバイト数で数えられることを確かめる。
const goSource = `package main

import "fmt"

// 挨拶を返す
func greet(name string) string {
	var n int = 42
	if name == "" && n > 0 {
		return "a\tb"
	}
	x := len(name)
	fmt.Println(x, nil, true)
	return name
}`

// newTestHighlighter は、言語の表を用意した highlighter を作る。document を直接作って試すためのもので、Server は動かさない。
func newTestHighlighter(t *testing.T) *highlighter {
	t.Helper()
	h, err := newHighlighter()
	if err != nil {
		t.Fatalf("newHighlighter: %v", err)
	}
	t.Cleanup(h.close)
	return h
}

// parse は、言語 fileType で text を全文から解析した document を返す。
func parse(t *testing.T, h *highlighter, fileType, text string) *document {
	t.Helper()
	d := newDocument(h.langs[fileType], text)
	t.Cleanup(d.close)
	return d
}

// styleOf は、text の中で n 番目（0 始まり）に現れる token をちょうど覆う範囲の色を返す。そのような範囲が無ければ空である。
func styleOf(t *testing.T, d *document, decs []ext.Decoration, token string, n int) string {
	t.Helper()
	off := -1
	for range n + 1 {
		i := strings.Index(string(d.src[off+1:]), token)
		if i < 0 {
			t.Fatalf("token %q #%d not found", token, n)
		}
		off += 1 + i
	}
	want := ext.Range{Start: d.position(off), End: d.position(off + len(token))}
	for _, dec := range decs {
		if dec.Range == want {
			return dec.Style
		}
	}
	return ""
}

// TestDecorationsGo は、Go のソースの各要素に 6.1 の表の論理名の範囲が付くことを確かめる。
//
// req: FR-35
func TestDecorationsGo(t *testing.T) {
	h := newTestHighlighter(t)
	d := parse(t, h, "go", goSource)
	decs := d.decorations()
	tests := []struct {
		token string
		n     int
		want  string
	}{
		{"package", 0, "keyword"},
		{"import", 0, "keyword"},
		{`"fmt"`, 0, "string"},
		{"// 挨拶を返す", 0, "comment"},
		{"func", 0, "keyword"},
		{"greet", 0, "function"},
		{"name", 0, "variable"},
		{"string", 0, "type"},
		{"var", 0, "keyword"},
		{"int", 0, "type"},
		{"=", 0, "operator"},
		{"42", 0, "number"},
		{"==", 0, "operator"},
		{`""`, 0, "string"},
		{"&&", 0, "operator"},
		{"return", 0, "keyword"},
		{":=", 0, "operator"},
		{"len", 0, "function"},     // function.builtin
		{"Println", 0, "function"}, // function.method
		{"fmt", 1, "variable"},
		{"nil", 0, "constant"},  // constant.builtin
		{"true", 0, "constant"}, // constant.builtin
	}
	for _, tt := range tests {
		if got := styleOf(t, d, decs, tt.token, tt.n); got != tt.want {
			t.Errorf("%q #%d: style = %q, want %q", tt.token, tt.n, got, tt.want)
		}
	}
	// 文字列の中のエスケープは文字列の色でつながり、"a\tb" 全体が 1 つの範囲になる。
	if got := styleOf(t, d, decs, `"a\tb"`, 0); got != "string" {
		t.Errorf(`"a\tb": style = %q, want "string"`, got)
	}
	// 多バイト文字を含むコメントの範囲は、バイト数で数えた行末までである。
	comment := ext.Range{Start: ext.Position{Line: 4, Byte: 0}, End: ext.Position{Line: 4, Byte: len("// 挨拶を返す")}}
	if !slices.Contains(decs, ext.Decoration{Range: comment, Style: "comment"}) {
		t.Errorf("comment range %+v not found in %+v", comment, decs)
	}
	checkWellFormed(t, d, decs)
}

// TestDecorationsJSON は、JSON の各要素に論理名の範囲が付くことを確かめる。
//
// req: FR-35
func TestDecorationsJSON(t *testing.T) {
	h := newTestHighlighter(t)
	d := parse(t, h, "json", `{"a": [1, -2.5e3, "x\n", true, null]}`)
	decs := d.decorations()
	tests := []struct {
		token string
		want  string
	}{
		{`"a"`, "string"}, // string.special.key
		{"1", "number"},
		{"-2.5e3", "number"},
		{`"x\n"`, "string"},
		{"true", "constant"},
		{"null", "constant"},
	}
	for _, tt := range tests {
		if got := styleOf(t, d, decs, tt.token, 0); got != tt.want {
			t.Errorf("%q: style = %q, want %q", tt.token, got, tt.want)
		}
	}
	checkWellFormed(t, d, decs)
}

// jsoncSource は、JSON の文法の外にあるコメントと末尾のカンマ（配列の中、オブジェクトの中、最後の項目の後）を含む JSONC の内容である。
const jsoncSource = `{
  // comment
  "a": [1, 2,],
  "b": { "c": "x", },
  "d": true,
}`

// TestDecorationsJSONC は、種類が "jsonc" のバッファに、"json" と同じ文法とクエリで、同じ色の範囲が付くことを確かめる。
// コメントと末尾のカンマを含む内容で、その後ろの要素にも色が付くことを確かめる。
// tree-sitter-json（v0.24.8）の文法は、コメントを extras に持つが、末尾のカンマを持たない。
// 末尾のカンマの所には ERROR と MISSING のノードができるが、パーサーが立ち直るので、後ろの要素の色は変わらない。
//
// req: FR-35
func TestDecorationsJSONC(t *testing.T) {
	h := newTestHighlighter(t)
	if h.langs["jsonc"] == nil || h.langs["jsonc"] != h.langs["json"] {
		t.Fatalf(`langs["jsonc"] = %p, want the same language as langs["json"] = %p`, h.langs["jsonc"], h.langs["json"])
	}
	d := parse(t, h, "jsonc", jsoncSource)
	decs := d.decorations()
	if want := parse(t, h, "json", jsoncSource).decorations(); !slices.Equal(decs, want) {
		t.Fatalf("jsonc decorations = %+v, want the same as json %+v", decs, want)
	}
	tests := []struct {
		token string
		want  string
	}{
		{"// comment", "comment"},
		{`"a"`, "string"},
		{"1", "number"},
		{"2", "number"},
		{`"b"`, "string"},
		{`"c"`, "string"},
		{`"x"`, "string"},
		{`"d"`, "string"},
		{"true", "constant"},
	}
	for _, tt := range tests {
		if got := styleOf(t, d, decs, tt.token, 0); got != tt.want {
			t.Errorf("%q: style = %q, want %q", tt.token, got, tt.want)
		}
	}
	checkWellFormed(t, d, decs)
}

// checkWellFormed は、一覧の範囲が空でなく、互いに重ならず先頭から順に並び、名前が 6.1 の表にあることを確かめる。
func checkWellFormed(t *testing.T, d *document, decs []ext.Decoration) {
	t.Helper()
	prevEnd := 0
	for i, dec := range decs {
		s, err := d.offset(dec.Range.Start)
		if err != nil {
			t.Fatalf("decs[%d]: start: %v", i, err)
		}
		e, err := d.offset(dec.Range.End)
		if err != nil {
			t.Fatalf("decs[%d]: end: %v", i, err)
		}
		if s >= e || s < prevEnd {
			t.Fatalf("decs[%d] = %+v: empty, or overlaps or precedes the previous range", i, dec)
		}
		if _, ok := styleByName(dec.Style); !ok {
			t.Errorf("decs[%d]: style %q is not in the table", i, dec.Style)
		}
		prevEnd = e
	}
}

// TestStyleFor は、キャプチャ名から論理名への対応を確かめる。
func TestStyleFor(t *testing.T) {
	tests := map[string]string{
		"comment":            "comment",
		"keyword":            "keyword",
		"function.method":    "function",
		"function.builtin":   "function",
		"constant.builtin":   "constant",
		"string.special.key": "string",
		"escape":             "string",
		"punctuation.delim":  "punctuation",
		"property":           "",
		"variable.parameter": "variable",
		"embedded":           "",
	}
	for capture, want := range tests {
		if got := styleFor(capture).String(); got != want {
			t.Errorf("styleFor(%q) = %q, want %q", capture, got, want)
		}
	}
}

// TestNewHighlighterDuplicateFileType は、1 つのファイルの種類を表（languages）に 2 回以上書くと、
// newHighlighter が種類の名前を含む誤りを返し、途中まで用意した言語を解放する経路（h.close）が落ちないことを確かめる。
// 別の言語に書いた重なりと、同じ言語の中の重なりの両方を確かめる。
func TestNewHighlighterDuplicateFileType(t *testing.T) {
	orig := languages
	t.Cleanup(func() { languages = orig })
	goSpec, jsonSpec := orig[0], orig[1]
	if goSpec.name != "go" || jsonSpec.name != "json" {
		t.Fatalf("languages = %v, %v; want go and json first", goSpec.name, jsonSpec.name)
	}
	tests := []struct {
		name      string
		fileTypes []string
		dup       string
	}{
		{"across languages", []string{"json", "go"}, "go"},
		{"within a language", []string{"json", "json"}, "json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := jsonSpec
			spec.fileTypes = tt.fileTypes
			languages = []languageSpec{goSpec, spec}
			h, err := newHighlighter()
			if err == nil {
				h.close()
				t.Fatalf("newHighlighter with fileTypes %v = nil error, want an error for %q", tt.fileTypes, tt.dup)
			}
			if want := fmt.Sprintf("file type %q", tt.dup); !strings.Contains(err.Error(), want) {
				t.Errorf("newHighlighter error = %q, want it to contain %q", err, want)
			}
		})
	}
}

// TestQueriesMapCaptures は、公式のクエリのキャプチャ名のうち、色を付けないものが想定のものだけであることを確かめる。
// 文法の版を上げてキャプチャ名が変わったときに気付くためである。
func TestQueriesMapCaptures(t *testing.T) {
	h := newTestHighlighter(t)
	want := map[string][]string{"go": {"property"}, "json": nil}
	for _, l := range h.all {
		var unmapped []string
		for i, n := range l.query.CaptureNames() {
			if l.styles[i] == noStyle {
				unmapped = append(unmapped, n)
			}
		}
		if !slices.Equal(unmapped, want[l.name]) {
			t.Errorf("%s: unmapped captures = %v, want %v", l.name, unmapped, want[l.name])
		}
	}
}

// applyText は、逐次の数え方の編集を、行の列に素朴に適用した全文を返す（検証用の参照実装）。
func applyText(text string, edits []ext.Edit) string {
	lines := strings.Split(text, "\n")
	for _, e := range edits {
		s, en := e.Range.Start, e.Range.End
		joined := lines[s.Line][:s.Byte] + e.Text + lines[en.Line][en.Byte:]
		lines = slices.Replace(lines, s.Line, en.Line+1, strings.Split(joined, "\n")...)
	}
	return strings.Join(lines, "\n")
}

// randomPosition は、text の中の文字の境界の位置（行末を含む）を 1 つ選ぶ。多バイト文字の途中は避ける。
func randomPosition(r *rand.Rand, text string) ext.Position {
	lines := strings.Split(text, "\n")
	for {
		l := r.IntN(len(lines))
		b := r.IntN(len(lines[l]) + 1)
		if b == len(lines[l]) || utf8.RuneStart(lines[l][b]) {
			return ext.Position{Line: l, Byte: b}
		}
	}
}

func lessPos(p, q ext.Position) bool {
	return p.Line < q.Line || (p.Line == q.Line && p.Byte < q.Byte)
}

// fragments は、無作為な編集で挿入するテキストの候補である。構文を壊すものと直すものを混ぜる。
var fragments = []string{"", "x", "\n", "{", "}", "(", ")", `"`, "`", "/*", "*/", "// c\n", "func f() {\n\treturn 1\n}\n", "\t", "日本", ":= 3", "if a {", "\n\n", "𝄞", "あ\nい", "\"い\n"}

// TestIncrementalMatchesFull は、buffer/change の編集を増分で適用して解析し直した結果が、
// 編集後の全文を全文から解析し直した結果と、構文木も色の一覧も一致することを確かめる。
// 1 回の buffer/change に複数の編集を逐次の数え方で並べたものも含める。
//
// req: FR-35
func TestIncrementalMatchesFull(t *testing.T) {
	h := newTestHighlighter(t)
	r := rand.New(rand.NewPCG(46, 1)) //nolint:gosec // 再現できる無作為な編集の列を作るためで、暗号の用途ではない
	for _, ft := range []string{"go", "json"} {
		text := goSource
		if ft == "json" {
			text = `{"a": [1, 2, {"b": "c"}], "d": null}`
		}
		inc := parse(t, h, ft, text)
		for step := range 300 {
			edits := make([]ext.Edit, 1+r.IntN(3))
			cur := text
			for i := range edits {
				a, b := randomPosition(r, cur), randomPosition(r, cur)
				if lessPos(b, a) {
					a, b = b, a
				}
				switch r.IntN(50) {
				case 0: // 全文を置き換える
					lines := strings.Split(cur, "\n")
					a, b = ext.Position{}, ext.Position{Line: len(lines) - 1, Byte: len(lines[len(lines)-1])}
				default:
					if r.IntN(2) == 0 {
						b = a // 挿入だけ
					}
				}
				edits[i] = ext.Edit{Range: ext.Range{Start: a, End: b}, Text: fragments[r.IntN(len(fragments))]}
				cur = applyText(cur, edits[i:i+1])
			}
			if err := inc.applyEdits(edits); err != nil {
				t.Fatalf("%s step %d: applyEdits(%+v): %v", ft, step, edits, err)
			}
			text = cur
			if string(inc.src) != text {
				t.Fatalf("%s step %d: text = %q, want %q", ft, step, inc.src, text)
			}
			full := newDocument(h.langs[ft], text)
			if g, w := dumpTree(inc.tree), dumpTree(full.tree); g != w {
				t.Fatalf("%s step %d: tree differs after %+v\n got: %s\nwant: %s", ft, step, edits, g, w)
			}
			if g, w := inc.decorations(), full.decorations(); !slices.Equal(g, w) {
				t.Fatalf("%s step %d: decorations differ after %+v\n got: %+v\nwant: %+v", ft, step, edits, g, w)
			}
			full.close()
		}
	}
}

// dumpTree は、構文木のすべてのノードの種類と、バイト位置と行・列の範囲を、深さ優先の順に並べた文字列を返す。
// ToSexp は位置を含まないので、Tree.Edit に渡す行・列の誤りを見つけるために位置も並べる。
func dumpTree(tree *sitter.Tree) string {
	var sb strings.Builder
	c := tree.Walk()
	defer c.Close()
	for {
		n := c.Node()
		fmt.Fprintf(&sb, "%s[%d-%d %v-%v]", n.Kind(), n.StartByte(), n.EndByte(), n.StartPosition(), n.EndPosition())
		if n.IsMissing() {
			sb.WriteString("MISSING")
		}
		if c.GotoFirstChild() {
			sb.WriteByte('(')
			continue
		}
		for !c.GotoNextSibling() {
			if !c.GotoParent() {
				return sb.String()
			}
			sb.WriteByte(')')
		}
		sb.WriteByte(' ')
	}
}

// TestApplyEditsOutOfRange は、全文の外を指す編集を誤りにすることを確かめる。
func TestApplyEditsOutOfRange(t *testing.T) {
	h := newTestHighlighter(t)
	for _, e := range []ext.Edit{
		{Range: ext.Range{Start: ext.Position{Line: 5, Byte: 0}, End: ext.Position{Line: 5, Byte: 0}}},
		{Range: ext.Range{Start: ext.Position{Line: 0, Byte: 4}, End: ext.Position{Line: 0, Byte: 4}}},
		{Range: ext.Range{Start: ext.Position{Line: 1, Byte: 0}, End: ext.Position{Line: 0, Byte: 0}}},
	} {
		d := parse(t, h, "json", "[1,\n2]")
		if err := d.applyEdits([]ext.Edit{e}); err == nil {
			t.Errorf("applyEdits(%+v) = nil, want error", e)
		}
	}
}

// connect は、highlighter の Server を io.Pipe の上で動かし、本体の役の接続と、受け取った decoration/set の通り道を返す。
func connect(t *testing.T) (*rpc.Conn, <-chan ext.DecorationSetParams) {
	conn, got, _ := connectWithMessages(t)
	return conn, got
}

// connectWithMessages は、connect に加えて、受け取った message/show の通り道を返す。
// 通り道の容量を大きく取り、受け取りの goroutine が詰まって ping の応答の受信が遅れないようにする。
func connectWithMessages(t *testing.T) (*rpc.Conn, <-chan ext.DecorationSetParams, <-chan ext.MessageShowParams) {
	t.Helper()
	return connectLimited(t, maxDecorationSetBytes)
}

// connectLimited は、decoration/set の大きさの上限を maxBytes にして connectWithMessages と同じものを返す。
func connectLimited(t *testing.T, maxBytes int) (*rpc.Conn, <-chan ext.DecorationSetParams, <-chan ext.MessageShowParams) {
	t.Helper()
	return connectWith(t, func(h *highlighter) { h.maxBytes = maxBytes }, nil)
}

// connectWith は、setup で変えた highlighter の Server を動かし、settings を initialize の設定として渡して、connectWithMessages と同じものを返す。
func connectWith(t *testing.T, setup func(*highlighter), settings json.RawMessage) (*rpc.Conn, <-chan ext.DecorationSetParams, <-chan ext.MessageShowParams) {
	t.Helper()
	conn, got, msgs, _ := connectWithProgress(t, setup, settings)
	return conn, got, msgs
}

// connectWithProgress は、connectWith に加えて、受け取った progress/report の通り道を返す。
func connectWithProgress(t *testing.T, setup func(*highlighter), settings json.RawMessage) (
	*rpc.Conn, <-chan ext.DecorationSetParams, <-chan ext.MessageShowParams, <-chan ext.ProgressReportParams,
) {
	t.Helper()
	h, err := newHighlighter()
	if err != nil {
		t.Fatalf("newHighlighter: %v", err)
	}
	setup(h)
	hostR, extW := io.Pipe()
	extR, hostW := io.Pipe()
	served := make(chan error, 1)
	s := h.server()
	go func() { served <- s.Serve(context.Background(), extR, extW) }()
	got := make(chan ext.DecorationSetParams, 1024)
	msgs := make(chan ext.MessageShowParams, 1024)
	progress := make(chan ext.ProgressReportParams, 1024)
	conn := rpc.NewConn(hostR, hostW, rpc.HandlerFuncs{
		Notification: func(_ context.Context, n *rpc.Notification) {
			if n.Method == ext.MethodProgressReport {
				var p ext.ProgressReportParams
				if err := json.Unmarshal(n.Params, &p); err != nil {
					t.Errorf("progress/report: %v", err)
				}
				progress <- p
				return
			}
			if n.Method == ext.MethodMessageShow {
				var m ext.MessageShowParams
				if err := json.Unmarshal(n.Params, &m); err != nil {
					t.Errorf("message/show: %v", err)
				}
				msgs <- m
				return
			}
			if n.Method != ext.MethodDecorationSet {
				return
			}
			var p ext.DecorationSetParams
			if err := json.Unmarshal(n.Params, &p); err != nil {
				t.Errorf("decoration/set: %v", err)
				return
			}
			got <- p
		},
	})
	t.Cleanup(func() {
		_ = conn.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve = %v, want nil", err)
			}
		case <-time.After(testTimeout):
			t.Error("Serve did not return after the host closed")
		}
		h.close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var init ext.InitializeResult
	if err := conn.Call(ctx, ext.MethodInitialize, ext.InitializeParams{APIVersion: ext.APIVersion, Settings: settings}, &init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return conn, got, msgs, progress
}

func receive(t *testing.T, ch <-chan ext.DecorationSetParams) ext.DecorationSetParams {
	t.Helper()
	return receiveWithin(t, ch, testTimeout)
}

func receiveWithin(t *testing.T, ch <-chan ext.DecorationSetParams, timeout time.Duration) ext.DecorationSetParams {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(timeout):
		t.Fatal("decoration/set did not arrive")
		return ext.DecorationSetParams{}
	}
}

// TestServeOpenAndChange は、本体の役から buffer/open と buffer/change を JSON-RPC で送り、
// それぞれのバージョンの decoration/set が届き、差分の後の一覧が全文から解析した一覧と一致することを確かめる。
//
// req: FR-35
func TestServeOpenAndChange(t *testing.T) {
	conn, got := connect(t)
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "go", Text: goSource}); err != nil {
		t.Fatal(err)
	}
	p := receive(t, got)
	h := newTestHighlighter(t)
	if want := parse(t, h, "go", goSource).decorations(); p.Buffer != 1 || p.Version != 1 || !slices.Equal(p.Decorations, want) {
		t.Fatalf("open: got buffer %d version %d, decorations equal = %v", p.Buffer, p.Version, slices.Equal(p.Decorations, want))
	}
	// 1 回の buffer/change で、行 6 の 42 を 7 に、続けて（逐次の数え方で）行 0 の後ろにコメントの行を足す。
	edits := []ext.Edit{
		{Range: ext.Range{Start: ext.Position{Line: 6, Byte: 13}, End: ext.Position{Line: 6, Byte: 15}}, Text: "7"},
		{Range: ext.Range{Start: ext.Position{Line: 0, Byte: 12}, End: ext.Position{Line: 0, Byte: 12}}, Text: "\n/* x */"},
	}
	if err := conn.Notify(ext.MethodBufferChange, ext.BufferChangeParams{Buffer: 1, FromVersion: 1, ToVersion: 2, Edits: edits}); err != nil {
		t.Fatal(err)
	}
	p = receive(t, got)
	after := applyText(goSource, edits)
	if !strings.Contains(after, "var n int = 7") {
		t.Fatalf("test edit is wrong: %q", after)
	}
	want := parse(t, h, "go", after).decorations()
	if p.Version != 2 || !slices.Equal(p.Decorations, want) {
		t.Errorf("change: version %d, decorations %+v, want version 2, %+v", p.Version, p.Decorations, want)
	}
	if !slices.Contains(p.Decorations, ext.Decoration{Range: ext.Range{Start: ext.Position{Line: 1, Byte: 0}, End: ext.Position{Line: 1, Byte: 7}}, Style: "comment"}) {
		t.Errorf("inserted comment is not decorated: %+v", p.Decorations)
	}
}

// TestServeJSONC は、本体の役から種類が "jsonc" のバッファを buffer/open で送ると、
// "json" として解析したものと同じ一覧の decoration/set が届くことを確かめる。
//
// req: FR-35
func TestServeJSONC(t *testing.T) {
	conn, got := connect(t)
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "jsonc", Text: jsoncSource}); err != nil {
		t.Fatal(err)
	}
	p := receive(t, got)
	h := newTestHighlighter(t)
	want := parse(t, h, "json", jsoncSource).decorations()
	if p.Buffer != 1 || p.Version != 1 || !slices.Equal(p.Decorations, want) {
		t.Fatalf("open: buffer %d version %d, decorations %+v, want buffer 1 version 1, %+v", p.Buffer, p.Version, p.Decorations, want)
	}
	if !slices.Contains(p.Decorations, ext.Decoration{Range: ext.Range{Start: ext.Position{Line: 1, Byte: 2}, End: ext.Position{Line: 1, Byte: 12}}, Style: "comment"}) {
		t.Errorf("comment is not decorated: %+v", p.Decorations)
	}
}

// TestServeFileTypes は、文法を持たない種類のファイルでは一覧を送らず、
// 送り直しで種類が文法を持たないものに変わったら空の一覧を送って色を消すことを確かめる。
func TestServeFileTypes(t *testing.T) {
	conn, got := connect(t)
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "markdown", Text: "# a"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 2, Version: 1, FileType: "json", Text: "[1]"}); err != nil {
		t.Fatal(err)
	}
	// 通知は順に処理されるので、最初に届くのはバッファ 2 の一覧である。
	if p := receive(t, got); p.Buffer != 2 || len(p.Decorations) != 1 {
		t.Fatalf("first decoration/set = %+v, want buffer 2 with 1 decoration", p)
	}
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 2, Version: 2, FileType: "", Text: "[1]"}); err != nil {
		t.Fatal(err)
	}
	if p := receive(t, got); p.Buffer != 2 || p.Version != 2 || p.Decorations == nil || len(p.Decorations) != 0 {
		t.Fatalf("decoration/set after file type change = %+v, want an empty list for buffer 2 version 2", p)
	}
}

// TestManifest は、宣言ファイルが検査を通り、filetype の契機が languages と一致することを確かめる。
func TestManifest(t *testing.T) {
	data, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m ext.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if warnings, verr := m.Validate(); verr != nil || len(warnings) > 0 {
		t.Fatalf("Validate = %v, %v; want no warnings and no error", warnings, verr)
	}
	if m.Command != "./vi-imitation-ext-treesitter" {
		t.Errorf("command = %q, want ./vi-imitation-ext-treesitter", m.Command)
	}
	var patterns, want []string
	for _, a := range m.Activation {
		if a.On == ext.ActivationFileType {
			patterns = append(patterns, a.Pattern)
		}
	}
	for _, l := range languages {
		want = append(want, l.fileTypes...)
	}
	slices.Sort(patterns)
	slices.Sort(want)
	if !slices.Equal(patterns, want) {
		t.Errorf("filetype patterns = %v, want %v", patterns, want)
	}
	var ids, wantIDs []string
	for _, c := range m.Contributes.TextObjects {
		ids = append(ids, c.ID)
	}
	for _, s := range textObjectSpecs {
		wantIDs = append(wantIDs, s.id)
	}
	if !slices.Equal(ids, wantIDs) {
		t.Errorf("contributes.textObjects = %v, want %v", ids, wantIDs)
	}
	// 見出しの帯は、窓の変化の知らせを受け取り、切り替えのコマンドを持つ（FR-245、FR-248）。
	if !m.WindowEvents {
		t.Error("windowEvents = false, want true")
	}
	if len(m.Contributes.Commands) != 1 || m.Contributes.Commands[0].ID != stickyToggleCommand {
		t.Errorf("contributes.commands = %+v, want only %s", m.Contributes.Commands, stickyToggleCommand)
	}
}

// pointAt は、src の中のバイト位置 off の行と列を、document を使わずに数える（検証用の参照実装）。
func pointAt(src string, off int) sitter.Point {
	before := src[:off]
	row := strings.Count(before, "\n")
	return sitter.Point{Row: uint(row), Column: uint(off - (strings.LastIndexByte(before, "\n"[0]) + 1))}
}

// offsetOf は、text の中の位置 p のバイト位置を、document を使わずに数える（検証用の参照実装）。
func offsetOf(text string, p ext.Position) int {
	lines := strings.Split(text, "\n")
	off := 0
	for _, l := range lines[:p.Line] {
		off += len(l) + 1
	}
	return off + p.Byte
}

// checkInputEdit は、text に対する編集 e から作った InputEdit の 6 つの値を、参照実装と比べる。
// 新しい終わりの行と列は、編集を適用した後の全文で、開始のバイト位置に挿入したテキストの長さを足した位置を数え直して求める。
func checkInputEdit(t *testing.T, h *highlighter, text string, e ext.Edit) {
	t.Helper()
	d := newDocument(h.langs["go"], text)
	defer d.close()
	got, err := d.inputEdit(e)
	if err != nil {
		t.Fatalf("inputEdit(%q, %+v): %v", text, e, err)
	}
	start, oldEnd := offsetOf(text, e.Range.Start), offsetOf(text, e.Range.End)
	after := text[:start] + e.Text + text[oldEnd:]
	want := sitter.InputEdit{
		StartByte:      uint(start),
		OldEndByte:     uint(oldEnd),
		NewEndByte:     uint(start + len(e.Text)),
		StartPosition:  pointAt(text, start),
		OldEndPosition: pointAt(text, oldEnd),
		NewEndPosition: pointAt(after, start+len(e.Text)),
	}
	if got != want {
		t.Fatalf("inputEdit(%q, %+v)\n got: %+v\nwant: %+v", text, e, got, want)
	}
}

// TestInputEdit は、tree-sitter に渡す InputEdit の開始・古い終わり・新しい終わりの位置（バイト位置と行・列）を直接確かめる。
// 構文木は再解析で位置を求め直すので、行・列の誤りは解析の結果に現れず、TestIncrementalMatchesFull では見つからない。
func TestInputEdit(t *testing.T) {
	h := newTestHighlighter(t)
	pos := func(l, b int) ext.Position { return ext.Position{Line: l, Byte: b} }
	edit := func(sl, sb, el, eb int, text string) ext.Edit {
		return ext.Edit{Range: ext.Range{Start: pos(sl, sb), End: pos(el, eb)}, Text: text}
	}
	const text = "ab\nあい\n\n𝄞x"
	tests := []struct {
		name string
		e    ext.Edit
	}{
		{"insert ascii", edit(0, 1, 0, 1, "zz")},
		{"insert multi-line with multibyte last line", edit(0, 2, 0, 2, "q\nr\nいう")},
		{"insert before multibyte", edit(1, 3, 1, 3, "𝄞")},
		{"replace multibyte across lines", edit(1, 3, 3, 4, "え")},
		{"delete across lines", edit(0, 1, 2, 0, "")},
		{"insert at end of last line", edit(3, 5, 3, 5, "\n")},
		{"replace whole text with empty", edit(0, 0, 3, 5, "")},
		{"replace whole text with lines", edit(0, 0, 3, 5, "a\nb\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { checkInputEdit(t, h, text, tt.e) })
	}
	// 無作為な編集でも確かめる。
	r := rand.New(rand.NewPCG(46, 2)) //nolint:gosec // 再現できる無作為な編集の列を作るためで、暗号の用途ではない
	cur := goSource
	for range 500 {
		a, b := randomPosition(r, cur), randomPosition(r, cur)
		if lessPos(b, a) {
			a, b = b, a
		}
		e := ext.Edit{Range: ext.Range{Start: a, End: b}, Text: fragments[r.IntN(len(fragments))]}
		checkInputEdit(t, h, cur, e)
		cur = applyText(cur, []ext.Edit{e})
	}
}

// bigJSON は、n 行の JSON の配列を返す。各行は {"id": N, "name": "itemN", "ok": true, "v": null}, の形で、8 つの範囲に色が付く。
func bigJSON(n int) string {
	var sb strings.Builder
	sb.WriteString("[\n")
	for i := range n {
		fmt.Fprintf(&sb, "{\"id\": %d, \"name\": \"item%d\", \"ok\": true, \"v\": null},\n", i, i)
	}
	sb.WriteString("{}\n]")
	return sb.String()
}

// ping は、本体の役から ping を送り、応答までの時間を返す。
func ping(t *testing.T, conn *rpc.Conn) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	start := time.Now()
	if err := conn.Call(ctx, ext.MethodPing, nil, nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return time.Since(start)
}

// typeChars は、行 10 の先頭へ 1 文字ずつ n 回、interval の間隔で挿入する buffer/change を送る。バージョンは 2 から n+1 になる。
// 送った後の全文を返す。
func typeChars(t *testing.T, conn *rpc.Conn, text string, n int, interval time.Duration) string {
	t.Helper()
	for i := range n {
		e := ext.Edit{Range: ext.Range{Start: ext.Position{Line: 10}, End: ext.Position{Line: 10}}, Text: " "}
		p := ext.BufferChangeParams{Buffer: 1, FromVersion: ext.Version(i + 1), ToVersion: ext.Version(i + 2), Edits: []ext.Edit{e}}
		if err := conn.Notify(ext.MethodBufferChange, p); err != nil {
			t.Fatal(err)
		}
		text = applyText(text, []ext.Edit{e})
		time.Sleep(interval)
	}
	return text
}

// receiveVersion は、want のバージョンの decoration/set が届くまで受け取り、それを返す。
// 届いた一覧のバージョンが増え続けること（古いバージョンの一覧を後から送らないこと）も確かめる。
// 受け取った件数も返す。
func receiveVersion(t *testing.T, got <-chan ext.DecorationSetParams, want ext.Version) (ext.DecorationSetParams, int) {
	t.Helper()
	var prev ext.Version
	for n := 1; ; n++ {
		p := receiveWithin(t, got, bigTimeout)
		if p.Version <= prev {
			t.Fatalf("decoration/set version %d after %d", p.Version, prev)
		}
		prev = p.Version
		if p.Version == want {
			return p, n
		}
		if p.Version > want {
			t.Fatalf("decoration/set version %d, want up to %d", p.Version, want)
		}
	}
}

// TestServeStaysResponsive は、解析に時間のかかる大きなファイルを打鍵しても、ping が本体の期限（internal/exthost の DefaultPingTimeout、2 秒）より十分に早く返ることを確かめる。
// レビューの再現の手順（20,003 行・約 1.2 MB の JSON に 100 ms の間隔で 15 文字を挿入した直後に ping）と同じである。
// 解析をループの上で同期に行っていたときは 3.25 秒かかった。
// 範囲を整数の列で送る形（基本設計 6.1）では、このファイルの一覧（16 万の範囲）は約 1.6 MB で上限に収まり、
// 最後のバージョンの一覧が全文から解析した一覧と一致することも確かめる（#53 の受入条件）。
//
// req: FR-35
func TestServeStaysResponsive(t *testing.T) {
	conn, got, msgs := connectWithMessages(t)
	text := bigJSON(20000)
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "json", Text: text}); err != nil {
		t.Fatal(err)
	}
	text = typeChars(t, conn, text, 15, 100*time.Millisecond)
	if d := ping(t, conn); d > 500*time.Millisecond {
		t.Errorf("ping took %v, want at most 500ms", d)
	} else {
		t.Logf("ping took %v", d)
	}
	p, n := receiveVersion(t, got, 16)
	t.Logf("received %d decoration/set for 16 versions", n)
	h := newTestHighlighter(t)
	want := parse(t, h, "json", text).decorations()
	if len(want) < 160000 || !slices.Equal(p.Decorations, want) {
		t.Errorf("decorations of version 16 differ from a full parse (%d vs %d)", len(p.Decorations), len(want))
	}
	if len(msgs) != 0 {
		t.Errorf("message/show arrived %d times, want none", len(msgs))
	}
}

// TestServeLargeGo は、1 万行の Go のファイルに色が付く（一覧が上限に収まり、全文から解析した一覧と一致する）ことを確かめる（#53 の受入条件）。
//
// req: FR-35
func TestServeLargeGo(t *testing.T) {
	conn, got := connect(t)
	text := bigGo(10000)
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "go", Text: text}); err != nil {
		t.Fatal(err)
	}
	p := receiveWithin(t, got, bigTimeout)
	h := newTestHighlighter(t)
	want := parse(t, h, "go", text).decorations()
	if len(want) < 10000 || !slices.Equal(p.Decorations, want) {
		t.Errorf("decorations differ from a full parse (%d vs %d)", len(p.Decorations), len(want))
	}
}

// bigGo は、goSource の関数を名前を変えて並べた、n 行以上の Go のソースを返す。
func bigGo(n int) string {
	var sb strings.Builder
	sb.WriteString("package main\n\nimport \"fmt\"\n")
	// 前置きが 3 行、関数 1 つが 12 行である。
	for i := 0; 3+12*i < n; i++ {
		fmt.Fprintf(&sb, "\n// f%d は、例の関数である。\nfunc f%d(name string) (int, error) {\n\tx := len(name) + %d\n\tif x > 0x1f {\n\t\treturn 0, fmt.Errorf(\"too long: %%q\", name)\n\t}\n\tvar s = []string{\"a\", `b`}\n\tfor _, v := range s {\n\t\tfmt.Println(v, nil, true, 1.5)\n\t}\n\treturn x, nil\n}\n", i, i, i)
	}
	return sb.String()
}

// TestServeTooLarge は、一覧が上限を超えるバッファには空の一覧を送り、message/show で 1 回だけ知らせることを確かめる。
// 上限を小さくして試す。
func TestServeTooLarge(t *testing.T) {
	conn, got, msgs := connectLimited(t, 4096)
	text := bigJSON(1000)
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "json", Text: text}); err != nil {
		t.Fatal(err)
	}
	typeChars(t, conn, text, 3, 0)
	p, _ := receiveVersion(t, got, 4)
	if len(p.Decorations) != 0 {
		t.Errorf("decoration/set has %d decorations, want an empty list over the size limit", len(p.Decorations))
	}
	select {
	case m := <-msgs:
		if m.Level != ext.MessageWarning || m.Text != tooLargeMessage {
			t.Errorf("message/show = %+v, want the warning %q", m, tooLargeMessage)
		}
	case <-time.After(bigTimeout):
		t.Fatal("message/show did not arrive")
	}
	if len(msgs) != 0 {
		t.Errorf("message/show arrived %d more times, want once", len(msgs))
	}
}

// TestServeCoalescesChanges は、解析の間に溜まった buffer/change をまとめて 1 回で解析し直し、
// 最後のバージョンの一覧が全文から解析した一覧と一致することを確かめる。途中のバージョンは飛ばしてよい。
//
// req: FR-35
func TestServeCoalescesChanges(t *testing.T) {
	conn, got := connect(t)
	text := bigJSON(3000)
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "json", Text: text}); err != nil {
		t.Fatal(err)
	}
	text = typeChars(t, conn, text, 15, 0)
	p, n := receiveVersion(t, got, 16)
	t.Logf("received %d decoration/set for 16 versions", n)
	h := newTestHighlighter(t)
	want := parse(t, h, "json", text).decorations()
	if len(want) == 0 || !slices.Equal(p.Decorations, want) {
		t.Errorf("decorations of version 16 differ from a full parse (%d vs %d)", len(p.Decorations), len(want))
	}
}
