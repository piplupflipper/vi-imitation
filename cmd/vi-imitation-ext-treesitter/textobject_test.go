package main

import (
	"context"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
)

// objectSource は、引数のテキストオブジェクトの対象（宣言の引数、可変長の引数、呼び出しの引数、型引数）を含む Go のソースである。
const objectSource = `package main

type Point struct {
	X, Y int
}

type (
	A int
	B interface {
		M()
	}
)

// add は和を返す。
func add(a int, b int) int {
	f := func(x int) int { return x + 1 }
	return f(a) + b
}

func (p Point) Sum(xs ...int) int {
	return p.X + call(1, 2,
		3,
	)
}

func empty() {}

func multi[T any, U comparable](t T) {
}`

// blockSource は、構文のまとまり（関数、if と else、for、関数リテラル、defer、構造体、インターフェース）を含む Go のソースである。
const blockSource = `package main

type Point struct {
	X, Y int
}

type (
	A int
	B interface {
		M()
	}
)

// add は和を返す。
func add(a int, b int) int {
	f := func(x int) int { return x + 1 }
	if a > 0 {
		for i := 0; i < b; i++ {
			a += i
		}
	} else {
		b = 2
	}
	return f(a) + b
}

func empty() {}

func multi[T any](t T) {
	defer func() {
		_ = t
	}()
}`

// specByID は、textObjectSpecs から id の定義を引く。
func specByID(t *testing.T, id string) textObjectSpec {
	t.Helper()
	for _, s := range textObjectSpecs {
		if s.id == id {
			return s
		}
	}
	t.Fatalf("text object %q not found", id)
	return textObjectSpec{}
}

// cursorAt は、src の中で marker が最初に現れる位置を返す。
func cursorAt(t *testing.T, d *document, marker string) ext.Position {
	t.Helper()
	i := strings.Index(string(d.src), marker)
	if i < 0 {
		t.Fatalf("marker %q not found", marker)
	}
	return d.position(i)
}

// sliceRange は、範囲 r のテキストを返す。linewise なら開始の行の先頭から終わりの行の末尾までである。
func sliceRange(d *document, r ext.Range, linewise bool) string {
	if linewise {
		return string(d.src[d.lineStarts[r.Start.Line] : d.lineStarts[r.End.Line]+d.lineLen(r.End.Line)])
	}
	start, _ := d.offset(r.Start)
	end, _ := d.offset(r.End)
	return string(d.src[start:end])
}

// TestTextObjectParameterGo は、Go のソースの各位置で、引数のテキストオブジェクト（aa、ia）の範囲と行単位かどうかが期待どおりであることを確かめる。
//
// req: FR-11, FR-241
func TestTextObjectParameterGo(t *testing.T) {
	h := newTestHighlighter(t)
	d := parse(t, h, "go", objectSource)
	tests := []struct {
		name     string
		id       string
		marker   string
		count    int
		ok       bool
		want     string
		linewise bool
	}{
		{"ia: 引数", "treesitter.parameter.inner", "b int", 1, true, "b int", false},
		{"aa: 最初の引数は後ろの区切りまで", "treesitter.parameter.around", "a int", 1, true, "a int, ", false},
		{"aa: 最後の引数は前の区切りから", "treesitter.parameter.around", "b int", 1, true, ", b int", false},
		{"ia: 可変長の引数", "treesitter.parameter.inner", "xs", 1, true, "xs ...int", false},
		{"aa: 呼び出しの引数は次の行の要素の直前まで", "treesitter.parameter.around", "2,", 1, true, "2,\n\t\t", false},
		{"aa: 複数行の最後の引数は後ろの区切りまでで、行を占めれば行単位", "treesitter.parameter.around", "3,", 1, true, "\t\t3,", true},
		{"ia: 後ろに区切りのある引数は文字単位", "treesitter.parameter.inner", "3,", 1, true, "3", false},
		{"aa: 区切りの上のカーソル", "treesitter.parameter.around", ", 2", 1, true, "1, ", false},
		{"2ia: 外側に並びが無い", "treesitter.parameter.inner", "a) + b", 2, false, "", false},
		{"ia: 型引数", "treesitter.parameter.inner", "U comparable", 1, true, "U comparable", false},
		{"aa: 型引数", "treesitter.parameter.around", "T any", 1, true, "T any, ", false},
		{"ia: 引数の外", "treesitter.parameter.inner", "package", 1, false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, linewise, ok, err := d.textObject(specByID(t, tt.id), cursorAt(t, d, tt.marker), tt.count)
			if err != nil {
				t.Fatalf("textObject: %v", err)
			}
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (range %+v)", ok, tt.ok, r)
			}
			if !ok {
				return
			}
			if got := sliceRange(d, r, linewise); got != tt.want || linewise != tt.linewise {
				t.Errorf("got %q linewise %v, want %q linewise %v", got, linewise, tt.want, tt.linewise)
			}
		})
	}
}

// blockCase は、構文のまとまりのテキストオブジェクトの 1 つの場合である。
type blockCase struct {
	name   string
	around bool
	marker string
	// sel は、選択の範囲のテキストである。空なら選択の無い問い合わせ（オペレーターの後）にする。
	// selLines が true なら、sel を含む行の全体を選択にする（行単位のビジュアルモード）。
	sel      string
	selLines bool
	count    int
	ok       bool
	want     string
	linewise bool
}

// runBlockCases は、d で cases のそれぞれを blockObject に渡し、範囲と行単位かどうかを確かめる。
func runBlockCases(t *testing.T, d *document, cases []blockCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var sel *ext.Range
			if tt.sel != "" {
				i := strings.Index(string(d.src), tt.sel)
				if i < 0 {
					t.Fatalf("selection %q not found", tt.sel)
				}
				r := ext.Range{Start: d.position(i), End: d.position(i + len(tt.sel))}
				if tt.selLines {
					r = ext.Range{Start: ext.Position{Line: r.Start.Line}, End: ext.Position{Line: r.End.Line, Byte: d.lineLen(r.End.Line)}}
				}
				sel = &r
			}
			r, linewise, ok, err := d.blockObject(tt.around, cursorAt(t, d, tt.marker), sel, tt.count)
			if err != nil {
				t.Fatalf("blockObject: %v", err)
			}
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (range %+v %q)", ok, tt.ok, r, sliceRange(d, r, linewise))
			}
			if !ok {
				return
			}
			if got := sliceRange(d, r, linewise); got != tt.want || linewise != tt.linewise {
				t.Errorf("got %q linewise %v, want %q linewise %v", got, linewise, tt.want, tt.linewise)
			}
		})
	}
}

// TestBlockObjectGo は、Go のソースで、構文のまとまりの全体（an）と中身（in）の範囲が、
// 回数と選択（ビジュアルモードで繰り返したとき）に応じて内側から外側へ広がることを確かめる。
//
// req: FR-11, FR-240
func TestBlockObjectGo(t *testing.T) {
	h := newTestHighlighter(t)
	d := parse(t, h, "go", blockSource)
	forStmt := "\t\tfor i := 0; i < b; i++ {\n\t\t\ta += i\n\t\t}"
	ifStmt := "\tif a > 0 {\n" + forStmt + "\n\t} else {\n\t\tb = 2\n\t}"
	addBody := "\tf := func(x int) int { return x + 1 }\n" + ifStmt + "\n\treturn f(a) + b"
	addFunc := "func add(a int, b int) int {\n" + addBody + "\n}"
	runBlockCases(t, d, []blockCase{
		{name: "an: for の全体は行単位", around: true, marker: "a += i", count: 1, ok: true, want: forStmt, linewise: true},
		{name: "2an: if と else の全体", around: true, marker: "a += i", count: 2, ok: true, want: ifStmt, linewise: true},
		{name: "3an: 関数の全体", around: true, marker: "a += i", count: 3, ok: true, want: addFunc, linewise: true},
		{name: "4an: 外側が無い", around: true, marker: "a += i", count: 4},
		{name: "an: else の中も if の全体", around: true, marker: "b = 2", count: 1, ok: true, want: ifStmt, linewise: true},
		{name: "an: if の条件の上のカーソル", around: true, marker: "if a > 0", count: 1, ok: true, want: ifStmt, linewise: true},
		{name: "an: 関数の見出しの上のカーソル", around: true, marker: "func add", count: 1, ok: true, want: addFunc, linewise: true},
		{name: "an: 関数リテラルは代入の文ごと", around: true, marker: "x + 1", count: 1, ok: true, want: "\tf := func(x int) int { return x + 1 }", linewise: true},
		{name: "an: defer の関数リテラルは defer の文ごと", around: true, marker: "_ = t", count: 1, ok: true, want: "\tdefer func() {\n\t\t_ = t\n\t}()", linewise: true},
		{name: "an: 型引数の上は関数の全体", around: true, marker: "T any", count: 1, ok: true, want: "func multi[T any](t T) {\n\tdefer func() {\n\t\t_ = t\n\t}()\n}", linewise: true},
		{name: "an: 構造体の宣言", around: true, marker: "X, Y", count: 1, ok: true, want: "type Point struct {\n\tX, Y int\n}", linewise: true},
		{name: "an: まとめた宣言の中のインターフェース", around: true, marker: "M()", count: 1, ok: true, want: "\tB interface {\n\t\tM()\n\t}", linewise: true},
		{name: "an: まとめた宣言の括弧はまとまりにしない", around: true, marker: "A int", count: 1},
		{name: "an: まとまりの外", around: true, marker: "package", count: 1},
		{name: "an: 関数の前のコメントは含めない", around: true, marker: "和を返す", count: 1},

		{name: "in: for の本体は間の行", marker: "a += i", count: 1, ok: true, want: "\t\t\ta += i", linewise: true},
		{name: "2in: if の本体", marker: "a += i", count: 2, ok: true, want: forStmt, linewise: true},
		{name: "in: else の本体", marker: "b = 2", count: 1, ok: true, want: "\t\tb = 2", linewise: true},
		{name: "in: if の条件の上は後ろの本体", marker: "if a > 0", count: 1, ok: true, want: forStmt, linewise: true},
		{name: "in: 関数の見出しの上は関数の本体", marker: "func add", count: 1, ok: true, want: addBody, linewise: true},
		{name: "in: 1 行の本体は空白を除いた内側", marker: "x + 1", count: 1, ok: true, want: "return x + 1"},
		{name: "in: 空の本体は幅 0", marker: "empty", count: 1, ok: true, want: ""},
		{name: "in: 型引数の上は型引数の括弧の内側", marker: "T any", count: 1, ok: true, want: "T any"},
		{name: "in: インターフェースの内側", marker: "M()", count: 1, ok: true, want: "\t\tM()", linewise: true},

		{name: "an を繰り返す: 行単位の for の選択から if へ", around: true, marker: "for i", sel: forStmt, selLines: true, count: 1, ok: true, want: ifStmt, linewise: true},
		{name: "an を回数で繰り返す: for の選択から関数へ", around: true, marker: "for i", sel: forStmt, count: 2, ok: true, want: addFunc, linewise: true},
		{name: "an を繰り返す: 文字単位の関数リテラルの選択から代入の文へ", around: true, marker: "func(x", sel: "func(x int) int { return x + 1 }", count: 1, ok: true, want: "\tf := func(x int) int { return x + 1 }", linewise: true},
		{name: "an を繰り返す: 関数の全体からは広げられない", around: true, marker: "func add", sel: addFunc, count: 1},
		{name: "an を繰り返す: 文の選択から for へ", around: true, marker: "a += i", sel: "a += i", count: 1, ok: true, want: forStmt, linewise: true},
		{name: "in を繰り返す: for の本体から if の本体へ", marker: "a += i", sel: "a += i", selLines: true, count: 1, ok: true, want: forStmt, linewise: true},
		{name: "in を繰り返す: if の本体から関数の本体へ", marker: "for i", sel: forStmt, count: 1, ok: true, want: addBody, linewise: true},
		{name: "in を繰り返す: an の後の in は外側の中身", marker: "for i", sel: ifStmt, count: 1, ok: true, want: addBody, linewise: true},
	})
}

// TestBlockObjectOwnLine は、同じ位置で終わる親でも、行の最初から始まるノードからは広げないこと（block.go の joins の 2）を確かめる。
// 字下げで本体を書く言語（Python）で入れ子の本体をまとめないための規則で、Go では行を改めて書いた式に当たる。
//
// req: FR-11, FR-240
func TestBlockObjectOwnLine(t *testing.T) {
	h := newTestHighlighter(t)
	d := parse(t, h, "go", "package main\n\nvar x = map[string]int{\n\t\"a\": 1,\n}\n\nfunc g() {\n\ty :=\n\t\t[]int{\n\t\t\t1,\n\t\t}\n\t_ = y\n}")
	runBlockCases(t, d, []blockCase{
		{name: "an: 行を改めた式は式だけ", around: true, marker: "1,\n\t\t}", count: 1, ok: true, want: "\t\t[]int{\n\t\t\t1,\n\t\t}", linewise: true},
		{name: "2an: 外は関数", around: true, marker: "1,\n\t\t}", count: 2, ok: true, want: "func g() {\n\ty :=\n\t\t[]int{\n\t\t\t1,\n\t\t}\n\t_ = y\n}", linewise: true},
		{name: "an: 同じ行で始まる式は宣言ごと", around: true, marker: `"a"`, count: 1, ok: true, want: "var x = map[string]int{\n\t\"a\": 1,\n}", linewise: true},
	})
}

// TestBlockObjectJSON は、テキストオブジェクトのクエリを持たない JSON でも、構文のまとまりを選べることを確かめる。
//
// req: FR-11, FR-240
func TestBlockObjectJSON(t *testing.T) {
	h := newTestHighlighter(t)
	d := parse(t, h, "json", `{"a": {"b": [1, 2]}, "c": 3}`)
	runBlockCases(t, d, []blockCase{
		{name: "an: 配列は対ごと", around: true, marker: "1, 2", count: 1, ok: true, want: `"b": [1, 2]`},
		{name: "in: 配列の内側", marker: "1, 2", count: 1, ok: true, want: "1, 2"},
		{name: "2an: 外側のオブジェクトの対", around: true, marker: "1, 2", count: 2, ok: true, want: `"a": {"b": [1, 2]}`},
		{name: "3an: いちばん外のオブジェクトは行を占める", around: true, marker: "1, 2", count: 3, ok: true, want: `{"a": {"b": [1, 2]}, "c": 3}`, linewise: true},
		{name: "2in: オブジェクトの内側", marker: "1, 2", count: 2, ok: true, want: `"b": [1, 2]`},
		{name: "an を繰り返す: 対の選択から外の対へ", around: true, marker: `"b"`, sel: `"b": [1, 2]`, count: 1, ok: true, want: `"a": {"b": [1, 2]}`},
		{name: "in を繰り返す: 配列の内側からオブジェクトの内側へ", marker: "1, 2", sel: "1, 2", count: 1, ok: true, want: `"b": [1, 2]`},
	})
}

// TestServeTextObject は、本体の役から textObject/resolve を JSON-RPC で送り、
// 編集の後のバージョンでも、構文木を持たない（解析の前の）バージョンでも、そのバージョンのテキストで範囲が返ることと、
// 選択を渡すと選択を置き換える答え（replaceSelection）になることを確かめる。
//
// req: FR-11, FR-240
func TestServeTextObject(t *testing.T) {
	conn, got := connect(t)
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "go", Text: blockSource}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 2, Version: 1, FileType: "json", Text: `{"a": [1, 2]}`}); err != nil {
		t.Fatal(err)
	}
	h := newTestHighlighter(t)
	d := parse(t, h, "go", blockSource)
	resolve := func(p ext.TextObjectResolveParams) ext.TextObjectResolveResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		var r ext.TextObjectResolveResult
		if err := conn.Call(ctx, ext.MethodTextObjectResolve, p, &r); err != nil {
			t.Fatalf("textObject/resolve: %v", err)
		}
		return r
	}
	at := func(buf ext.BufferID, version ext.Version, id string, cursor ext.Position) ext.TextObjectResolveParams {
		return ext.TextObjectResolveParams{Buffer: buf, Version: version, ID: id, Cursor: cursor}
	}
	// 一覧が届く前（構文木を持たないかもしれない）と後の両方で、同じ範囲が返る。
	cursor := cursorAt(t, d, "return x + 1")
	inner := ext.Range{Start: cursor, End: cursorAt(t, d, " }\n\tif")}
	want := ext.TextObjectResolveResult{OK: true, Version: 1, Range: inner}
	if r := resolve(at(1, 1, "treesitter.block.inner", cursor)); r != want {
		t.Errorf("before decoration/set: %+v, want %+v", r, want)
	}
	receive(t, got)
	receive(t, got)
	if r := resolve(at(1, 1, "treesitter.block.inner", cursor)); r != want {
		t.Errorf("after decoration/set: %+v, want %+v", r, want)
	}
	// 選択を渡すと、選択より大きい範囲を、選択を置き換える答えで返す。
	p := at(1, 1, "treesitter.block.inner", cursor)
	p.Selection = &inner
	line := cursor.Line
	wantSel := ext.TextObjectResolveResult{OK: true, Version: 1, Range: ext.Range{Start: ext.Position{Line: line}, End: ext.Position{Line: line + 8, Byte: len("\treturn f(a) + b")}}, Linewise: true, ReplaceSelection: true}
	if r := resolve(p); r != wantSel {
		t.Errorf("with selection: %+v, want %+v", r, wantSel)
	}
	// 1 行目に行を足すと、範囲は 1 行下がる。
	edit := ext.Edit{Range: ext.Range{Start: ext.Position{Line: 0, Byte: 12}, End: ext.Position{Line: 0, Byte: 12}}, Text: "\n"}
	if err := conn.Notify(ext.MethodBufferChange, ext.BufferChangeParams{Buffer: 1, FromVersion: 1, ToVersion: 2, Edits: []ext.Edit{edit}}); err != nil {
		t.Fatal(err)
	}
	down := func(p ext.Position) ext.Position { return ext.Position{Line: p.Line + 1, Byte: p.Byte} }
	want2 := ext.TextObjectResolveResult{OK: true, Version: 2, Range: ext.Range{Start: down(inner.Start), End: down(inner.End)}}
	if r := resolve(at(1, 2, "treesitter.block.inner", down(cursor))); r != want2 {
		t.Errorf("after change: %+v, want %+v", r, want2)
	}
	// 引数のクエリを持たない言語では、引数は失敗し、構文のまとまりは選べる。
	if r := resolve(at(2, 1, "treesitter.parameter.inner", ext.Position{Line: 0, Byte: 8})); r.OK {
		t.Errorf("json parameter: %+v, want ok false", r)
	}
	wantJSON := ext.TextObjectResolveResult{OK: true, Version: 1, Range: ext.Range{Start: ext.Position{Byte: 7}, End: ext.Position{Byte: 11}}}
	if r := resolve(at(2, 1, "treesitter.block.inner", ext.Position{Line: 0, Byte: 8})); r != wantJSON {
		t.Errorf("json block: %+v, want %+v", r, wantJSON)
	}
	// 引数は、選択を渡しても選択を置き換えない。
	p = at(1, 2, "treesitter.parameter.inner", down(cursorAt(t, d, "b int")))
	p.Selection = &ext.Range{Start: p.Cursor, End: ext.Position{Line: p.Cursor.Line, Byte: p.Cursor.Byte + 1}}
	if r := resolve(p); !r.OK || r.ReplaceSelection {
		t.Errorf("parameter with selection: %+v, want ok and no replaceSelection", r)
	}
}

// TestParseObjectCapture は、キャプチャ名が対象と部分に分かれ、知らない名前は対象にならないことを確かめる。
func TestParseObjectCapture(t *testing.T) {
	tests := []struct {
		name string
		want objectCapture
	}{
		{"parameter.outer", objectCapture{objectParameter, partOuter}},
		{"parameter.inner", objectCapture{objectParameter, partInner}},
		{"function.outer", objectCapture{}},
		{"block.inner", objectCapture{}},
		{"parameter", objectCapture{}},
		{"parameter.body", objectCapture{}},
	}
	for _, tt := range tests {
		if got := parseObjectCapture(tt.name); got != tt.want {
			t.Errorf("parseObjectCapture(%q) = %+v, want %+v", tt.name, got, tt.want)
		}
	}
	// 書いたクエリのキャプチャ名は、どれも対象と部分に分かれる。
	h := newTestHighlighter(t)
	for _, l := range h.all {
		if l.objects == nil {
			continue
		}
		for i, n := range l.objects.CaptureNames() {
			if l.objectCaptures[i].part == partNone {
				t.Errorf("%s: capture @%s is not a text object", l.name, n)
			}
		}
	}
}
