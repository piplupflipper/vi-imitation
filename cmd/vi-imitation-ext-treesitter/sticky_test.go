package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	userconfig "github.com/piplupflipper/vi-imitation/internal/config"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// stickySource は、見出しの帯を試す Go のソースである。行の番号（0 から）は次のとおりである。
//
//	2  func outer(        関数の見出しは 2〜5 行の 4 行
//	6  	for ... {
//	7  		if i > 0 {
//	10 		} else {
//	12 			_ = y
//	15 	return nil
const stickySource = `package main

func outer(
	a int,
	b string,
) error {
	for i := 0; i < a; i++ {
		if i > 0 {
			x := 1
			_ = x
		} else {
			y := 2
			_ = y
		}
	}
	return nil
}`

// headingRows は、見出しを、行の番号の組の列にする。
func headingRows(hs []heading) [][2]int {
	out := [][2]int{}
	for _, h := range hs {
		out = append(out, [2]int{h.start, h.end})
	}
	return out
}

// TestContextHeadingsGo は、窓の最初の行とカーソルから、帯に出す見出しの行が
// nvim-treesitter-context の get（mode の既定の cursor）と同じ規則で決まることを確かめる。
//
// req: FR-245
func TestContextHeadingsGo(t *testing.T) {
	h := newTestHighlighter(t)
	d := parse(t, h, "go", stickySource)
	def := defaultContextSettings
	one := contextSettings{enable: true, multilineThreshold: 1}
	tests := []struct {
		name   string
		top    int
		cursor ext.Position
		c      contextSettings
		want   [][2]int
	}{
		{"見出しが見えていれば出さない", 0, ext.Position{Line: 9, Byte: 3}, def, [][2]int{}},
		{"カーソルが最初の行なら出さない", 9, ext.Position{Line: 9, Byte: 3}, def, [][2]int{}},
		{"カーソルの上の 1 行に収めるため外側から削る", 8, ext.Position{Line: 9, Byte: 3}, def, [][2]int{{7, 8}}},
		{"else の中は if と else の節の行", 9, ext.Position{Line: 12, Byte: 3}, def, [][2]int{{6, 7}, {7, 8}, {10, 11}}},
		{"1 行に切り詰めれば関数の見出しも入る", 8, ext.Position{Line: 12, Byte: 3}, one, [][2]int{{2, 3}, {6, 7}, {7, 8}, {10, 11}}},
		{"複数行の見出しは閾値まで", 10, ext.Position{Line: 15, Byte: 1}, def, [][2]int{{2, 6}}},
		{"複数行の見出しを 1 行に", 10, ext.Position{Line: 15, Byte: 1}, one, [][2]int{{2, 3}}},
		// 関数、for、if を採った時点で、else の節の行（10）は帯の 2 行の下に見えるので採らない。
		{"上限を超えたら外側から削る", 8, ext.Position{Line: 12, Byte: 3}, contextSettings{enable: true, maxLines: 2, multilineThreshold: 1}, [][2]int{{6, 7}, {7, 8}}},
		{"外側の見出しの後ろの行を削る", 13, ext.Position{Line: 15, Byte: 1}, def, [][2]int{{2, 4}}},
		{"for の外の行では for を出さない", 7, ext.Position{Line: 15, Byte: 1}, def, [][2]int{{2, 6}}},
		{"関数の外では出さない", 3, ext.Position{Line: 0, Byte: 0}, def, [][2]int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := headingRows(d.contextHeadings(tt.top, tt.cursor, tt.c))
			if !slices.Equal(got, tt.want) {
				t.Errorf("contextHeadings(top %d, cursor %+v) = %v, want %v", tt.top, tt.cursor, got, tt.want)
			}
		})
	}
}

// TestContextHeadingsShapes は、見出しの形（1 行の本体、空の本体、本体の前の空行、else if の連なり、JSON）ごとに、見出しの行を確かめる。
//
// req: FR-245
func TestContextHeadingsShapes(t *testing.T) {
	h := newTestHighlighter(t)
	def := defaultContextSettings
	tests := []struct {
		name, fileType, src string
		top                 int
		cursor              ext.Position
		want                [][2]int
	}{
		{
			name: "見出しと本体の間の空行は出さない", fileType: "go",
			src: "package p\n\nfunc f(\n\ta int,\n\n) {\n\n\tx := 1\n\t_ = x\n\t_ = a\n}",
			top: 4, cursor: ext.Position{Line: 9, Byte: 1},
			want: [][2]int{{2, 6}},
		},
		{
			name: "見出しの上のカーソルでは、カーソルの行より前まで", fileType: "go",
			src: "package p\n\nfunc f(\n\ta int,\n\tb int,\n) {\n\t_ = a\n}",
			top: 3, cursor: ext.Position{Line: 5, Byte: 1},
			want: [][2]int{{2, 4}},
		},
		{
			name: "else if の連なりは今の節の行", fileType: "go",
			src: "package p\n\nfunc f(a int) {\n\tif a > 0 {\n\t\ta++\n\t} else if a < 0 {\n\t\ta--\n\t\ta--\n\t} else {\n\t\ta = 0\n\t\ta = 1\n\t}\n}",
			top: 4, cursor: ext.Position{Line: 7, Byte: 2},
			want: [][2]int{{2, 3}, {3, 4}, {5, 6}},
		},
		{
			name: "最後の else の中", fileType: "go",
			src: "package p\n\nfunc f(a int) {\n\tif a > 0 {\n\t\ta++\n\t} else if a < 0 {\n\t\ta--\n\t\ta--\n\t} else {\n\t\ta = 0\n\t\ta = 1\n\t}\n}",
			top: 7, cursor: ext.Position{Line: 10, Byte: 2},
			want: [][2]int{{2, 3}, {3, 4}, {8, 9}},
		},
		{
			name: "関数リテラルは代入の行", fileType: "go",
			src: "package p\n\nfunc f() {\n\tg := func() {\n\t\tx := 1\n\t\t_ = x\n\t}\n\tg()\n}",
			top: 3, cursor: ext.Position{Line: 5, Byte: 2},
			want: [][2]int{{2, 3}, {3, 4}},
		},
		{
			name: "switch の case", fileType: "go",
			src: "package p\n\nfunc f(a int) {\n\tswitch a {\n\tcase 1:\n\t\ta++\n\t\ta++\n\tdefault:\n\t\ta--\n\t\ta--\n\t}\n}",
			top: 6, cursor: ext.Position{Line: 9, Byte: 2},
			want: [][2]int{{2, 3}, {3, 4}, {7, 8}},
		},
		{
			name: "select の case", fileType: "go",
			src: "package p\n\nfunc f(c chan int) {\n\tselect {\n\tcase v := <-c:\n\t\t_ = v\n\t\t_ = v\n\t}\n}",
			top: 5, cursor: ext.Position{Line: 6, Byte: 2},
			want: [][2]int{{4, 5}},
		},
		{
			name: "JSON の配列の中のオブジェクト", fileType: "json",
			src: "[\n  {\n    \"a\": 1,\n    \"b\": 2\n  }\n]",
			top: 2, cursor: ext.Position{Line: 3, Byte: 4},
			want: [][2]int{{1, 2}},
		},
		{
			name: "JSON の入れ子の対", fileType: "json",
			src: "{\n  \"a\": {\n    \"b\": [\n      1,\n      2\n    ]\n  }\n}",
			top: 1, cursor: ext.Position{Line: 4, Byte: 6},
			want: [][2]int{{0, 1}, {1, 2}, {2, 3}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := parse(t, h, tt.fileType, tt.src)
			got := headingRows(d.contextHeadings(tt.top, tt.cursor, def))
			if !slices.Equal(got, tt.want) {
				t.Errorf("contextHeadings(top %d, cursor %+v) = %v, want %v", tt.top, tt.cursor, got, tt.want)
			}
		})
	}
}

// TestPaintRangeMatchesWhole は、行ごとに決めた色（paint）が、全文で決めた色と同じことを確かめる。帯の行に本文と同じ色を付けるための前提である。
//
// req: FR-246
func TestPaintRangeMatchesWhole(t *testing.T) {
	h := newTestHighlighter(t)
	for _, tt := range []struct{ fileType, src string }{{"go", goSource}, {"go", stickySource}, {"json", jsoncSource}} {
		d := parse(t, h, tt.fileType, tt.src)
		whole := d.paint(0, len(d.src))
		for row := range d.lineStarts {
			from := d.lineStarts[row]
			to := from + d.lineLen(row)
			if got, want := d.paint(from, to), whole[from:to]; !slices.Equal(got, want) {
				t.Errorf("%s line %d: paint = %v, want %v", tt.fileType, row, got, want)
			}
		}
	}
}

// TestBandLines は、帯の行が、バッファの行の全体と、行に当たるバッファの行と、本文と同じ構文の色を持つことを確かめる。
//
// req: FR-245, FR-246
func TestBandLines(t *testing.T) {
	h := newTestHighlighter(t)
	d := parse(t, h, "go", stickySource)
	lines := d.bandLines([]heading{{2, 3}, {6, 7}})
	if len(lines) != 2 {
		t.Fatalf("bandLines = %+v, want 2 lines", lines)
	}
	if lines[0].Text != "func outer(" || lines[0].Line == nil || *lines[0].Line != 2 {
		t.Errorf("line 0 = %+v, want func outer( at line 2", lines[0])
	}
	want := []ext.BandHighlight{{Start: 0, End: 4, Style: "keyword"}, {Start: 5, End: 10, Style: "function"}}
	if !slices.Equal(lines[0].Highlights, want) {
		t.Errorf("line 0 highlights = %+v, want %+v", lines[0].Highlights, want)
	}
	if lines[1].Text != "\tfor i := 0; i < a; i++ {" || *lines[1].Line != 6 {
		t.Errorf("line 1 = %+v", lines[1])
	}
	if !slices.Contains(lines[1].Highlights, ext.BandHighlight{Start: 1, End: 4, Style: "keyword"}) {
		t.Errorf("line 1 highlights = %+v, want for as keyword", lines[1].Highlights)
	}
	band := ext.BandSetParams{Window: "client/1", Lines: lines}
	if err := band.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
}

// TestParseContext は、設定の context の既定と、誤りの扱いを確かめる。
//
// req: FR-247
func TestParseContext(t *testing.T) {
	s, problems := parseSettings(nil)
	if len(problems) != 0 || s.context != defaultContextSettings {
		t.Errorf("parseSettings(nil).context = %+v, %q", s.context, problems)
	}
	if defaultContextSettings != (contextSettings{enable: true, maxLines: 0, multilineThreshold: 20}) {
		t.Errorf("defaultContextSettings = %+v, want nvim-treesitter-context の既定", defaultContextSettings)
	}
	s, problems = parseSettings(json.RawMessage(`{"context": {"enable": false, "maxLines": 4, "multilineThreshold": 1}}`))
	if len(problems) != 0 || s.context != (contextSettings{enable: false, maxLines: 4, multilineThreshold: 1}) {
		t.Errorf("context = %+v, %q", s.context, problems)
	}
	for _, tt := range []struct {
		raw  string
		want contextSettings
		msg  string
	}{
		{`{"context": {"maxLines": -1}}`, defaultContextSettings, "maxLines"},
		{`{"context": {"multilineThreshold": 0, "maxLines": 3}}`, contextSettings{enable: true, maxLines: 3, multilineThreshold: 20}, "multilineThreshold"},
		{`{"context": {"lines": 3}}`, defaultContextSettings, "context"},
		{`{"context": 3}`, defaultContextSettings, "context"},
	} {
		s, problems := parseSettings(json.RawMessage(tt.raw))
		if s.context != tt.want || len(problems) != 1 || !strings.Contains(problems[0], tt.msg) {
			t.Errorf("parseSettings(%s).context = %+v, %q; want %+v and 1 problem about %s", tt.raw, s.context, problems, tt.want, tt.msg)
		}
	}
}

// TestConfigExampleContext は、同梱する設定の例（config.example.jsonc）の tree-sitter の部分が誤り無く読め、
// 見出しの帯の設定がオーナーの nvim の nvim-treesitter-context の値（max_lines = 4、multiline_threshold = 1）であることを確かめる。
//
// req: FR-247
func TestConfigExampleContext(t *testing.T) {
	data, err := os.ReadFile("../../config.example.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	ucfg, errs := userconfig.Parse("config.example.jsonc", data)
	if len(errs) != 0 {
		t.Fatalf("設定の例の誤り: %v", errs)
	}
	s, problems := parseSettings(ucfg.ExtensionSettings["treesitter"])
	if len(problems) != 0 {
		t.Fatalf("problems = %q", problems)
	}
	if want := (contextSettings{enable: true, maxLines: 4, multilineThreshold: 1}); s.context != want {
		t.Errorf("context = %+v, want %+v", s.context, want)
	}
}

// stickyHost は、見出しの帯を試すための本体の役の接続である。
type stickyHost struct {
	conn  *rpc.Conn
	decs  chan ext.DecorationSetParams
	bands chan ext.BandSetParams
}

// connectSticky は、highlighter の Server を io.Pipe の上で動かし、settings を initialize の設定として渡して、本体の役の接続を返す。
func connectSticky(t *testing.T, settings json.RawMessage) *stickyHost {
	t.Helper()
	h, err := newHighlighter()
	if err != nil {
		t.Fatalf("newHighlighter: %v", err)
	}
	hostR, extW := io.Pipe()
	extR, hostW := io.Pipe()
	served := make(chan error, 1)
	s := h.server()
	go func() { served <- s.Serve(context.Background(), extR, extW) }()
	host := &stickyHost{decs: make(chan ext.DecorationSetParams, 64), bands: make(chan ext.BandSetParams, 64)}
	host.conn = rpc.NewConn(hostR, hostW, rpc.HandlerFuncs{
		Notification: func(_ context.Context, n *rpc.Notification) {
			switch n.Method {
			case ext.MethodDecorationSet:
				var p ext.DecorationSetParams
				if err := json.Unmarshal(n.Params, &p); err != nil {
					t.Errorf("decoration/set: %v", err)
				}
				host.decs <- p
			case ext.MethodBandSet:
				var p ext.BandSetParams
				if err := json.Unmarshal(n.Params, &p); err != nil {
					t.Errorf("band/set: %v", err)
				}
				host.bands <- p
			}
		},
	})
	t.Cleanup(func() {
		_ = host.conn.Close()
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
	if err := host.conn.Call(ctx, ext.MethodInitialize, ext.InitializeParams{APIVersion: ext.APIVersion, Settings: settings}, &init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return host
}

func (h *stickyHost) notify(t *testing.T, method string, params any) {
	t.Helper()
	if err := h.conn.Notify(method, params); err != nil {
		t.Fatal(err)
	}
}

func (h *stickyHost) band(t *testing.T) ext.BandSetParams {
	t.Helper()
	select {
	case p := <-h.bands:
		return p
	case <-time.After(testTimeout):
		t.Fatal("band/set did not arrive")
		return ext.BandSetParams{}
	}
}

func (h *stickyHost) toggle(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var res ext.CommandExecuteResult
	if err := h.conn.Call(ctx, ext.MethodCommandExecute, ext.CommandExecuteParams{Buffer: 1, Version: 1, ID: stickyToggleCommand}, &res); err != nil || !res.OK {
		t.Fatalf("command/execute %s = %+v, %v", stickyToggleCommand, res, err)
	}
}

// bandTexts は、帯の行の文字列と、行に当たるバッファの行を返す。
func bandTexts(p ext.BandSetParams) []string {
	out := []string{}
	for _, l := range p.Lines {
		line := -1
		if l.Line != nil {
			line = *l.Line
		}
		out = append(out, strings.TrimSpace(l.Text)+"@"+strconv.Itoa(line))
	}
	return out
}

// TestServeSticky は、本体の役から window/change を送ると、窓の上端の帯が届き、
// 切り替えのコマンドで消えて戻り、見出しが見えると消え、編集の後の解析で作り直されることを確かめる。
//
// req: FR-245, FR-248
func TestServeSticky(t *testing.T) {
	h := connectSticky(t, nil)
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "go", Text: stickySource})
	<-h.decs
	win := ext.WindowChangeParams{Window: "client/1", Buffer: 1, Version: 1, Top: 9, Bottom: 13, Rows: 4, Columns: 80, Cursor: ext.Position{Line: 12, Byte: 3}, Seq: 1}
	h.notify(t, ext.MethodWindowChange, win)
	p := h.band(t)
	want := []string{"for i := 0; i < a; i++ {@6", "if i > 0 {@7", "} else {@10"}
	if p.Window != "client/1" || p.Buffer != 1 || p.Version != 1 || p.Seq != 1 || !slices.Equal(bandTexts(p), want) {
		t.Fatalf("band = %+v %v, want %v", p, bandTexts(p), want)
	}

	h.toggle(t)
	if p := h.band(t); len(p.Lines) != 0 || p.Window != "client/1" {
		t.Fatalf("band after toggle = %+v, want an empty band", p)
	}
	h.toggle(t)
	if p := h.band(t); !slices.Equal(bandTexts(p), want) {
		t.Fatalf("band after the second toggle = %v, want %v", bandTexts(p), want)
	}

	// 見出しの行が見えれば、帯を消す。
	win.Top, win.Seq = 0, 2
	h.notify(t, ext.MethodWindowChange, win)
	if p := h.band(t); len(p.Lines) != 0 || p.Seq != 2 {
		t.Fatalf("band with the headings visible = %+v, want an empty band of seq 2", p)
	}

	// 先頭に 2 行を足すと、解析の後に、新しい行の番号とバージョン番号で作り直す。
	win.Top, win.Seq = 9, 3
	h.notify(t, ext.MethodWindowChange, win)
	if p := h.band(t); !slices.Equal(bandTexts(p), want) {
		t.Fatalf("band = %v, want %v", bandTexts(p), want)
	}
	h.notify(t, ext.MethodBufferChange, ext.BufferChangeParams{Buffer: 1, FromVersion: 1, ToVersion: 2, Edits: []ext.Edit{
		{Range: ext.Range{Start: ext.Position{Line: 0, Byte: 12}, End: ext.Position{Line: 0, Byte: 12}}, Text: "\n\n// c"},
	}})
	win.Version, win.Top, win.Cursor.Line, win.Seq = 2, 11, 14, 4
	h.notify(t, ext.MethodWindowChange, win)
	var last ext.BandSetParams
	for last.Version != 2 {
		last = h.band(t)
	}
	if want := []string{"for i := 0; i < a; i++ {@8", "if i > 0 {@9", "} else {@12"}; !slices.Equal(bandTexts(last), want) {
		t.Errorf("band after the edit = %v, want %v", bandTexts(last), want)
	}
}

// TestServeStickySettings は、設定で帯を出さないにすると、窓の知らせでは帯を送らず、切り替えのコマンドで出し、
// 上限と 1 行への切り詰めの設定が帯の行に効くことを確かめる。
//
// req: FR-247, FR-248
func TestServeStickySettings(t *testing.T) {
	h := connectSticky(t, json.RawMessage(`{"context": {"enable": false, "maxLines": 2, "multilineThreshold": 1}}`))
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "go", Text: stickySource})
	<-h.decs
	h.notify(t, ext.MethodWindowChange, ext.WindowChangeParams{Window: "client/1", Buffer: 1, Version: 1, Top: 7, Bottom: 13, Rows: 6, Columns: 80, Cursor: ext.Position{Line: 12, Byte: 3}, Seq: 1})
	h.toggle(t)
	p := h.band(t)
	// 出さない間の窓の知らせでは帯を送らないので、最初に届くのは切り替えの後の帯である。
	// 上限が無ければ関数、for、if の 3 行だが、上限の 2 行に収めるため外側の関数を削る。
	if want := []string{"for i := 0; i < a; i++ {@6", "if i > 0 {@7"}; !slices.Equal(bandTexts(p), want) {
		t.Errorf("band = %v, want %v", bandTexts(p), want)
	}
}

// TestServeStickyOtherBuffer は、窓のバッファが解析しないバッファに替わると、帯を消すことを確かめる。
//
// req: FR-245
func TestServeStickyOtherBuffer(t *testing.T) {
	h := connectSticky(t, nil)
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "go", Text: stickySource})
	<-h.decs
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 2, Version: 1, FileType: "text", Text: strings.Repeat("a\n", 20)})
	win := ext.WindowChangeParams{Window: "client/1", Buffer: 1, Version: 1, Top: 9, Bottom: 13, Rows: 4, Columns: 80, Cursor: ext.Position{Line: 12, Byte: 3}, Seq: 1}
	h.notify(t, ext.MethodWindowChange, win)
	if p := h.band(t); len(p.Lines) == 0 {
		t.Fatalf("band = %+v, want lines", p)
	}
	win.Buffer, win.Seq = 2, 2
	h.notify(t, ext.MethodWindowChange, win)
	if p := h.band(t); len(p.Lines) != 0 || p.Buffer != 2 {
		t.Fatalf("band for the text buffer = %+v, want an empty band", p)
	}
}

// TestContextHeadingsLanguages は、設定で足す言語（Python、TypeScript、Rust、bash、C、Lua）で、言語ごとの表を持たずに、
// クラス、関数、if、else と elif の節、match の腕、switch の case の見出しが帯に出ることを確かめる。
// 文法は testParsersEnv の下のものを使うので、CI では飛ばす（block_lang_test.go）。
//
// req: FR-245
func TestContextHeadingsLanguages(t *testing.T) {
	def := defaultContextSettings
	tests := []struct {
		name, lang, src string
		top             int
		cursor          ext.Position
		want            [][2]int
	}{
		{
			name: "python の elif", lang: "python",
			src: "class A:\n    def f(self, a):\n        if a > 0:\n            x = 1\n            y = 2\n        elif a < 0:\n            x = 2\n            y = 3\n        else:\n            x = 3\n            y = 4\n",
			top: 4, cursor: ext.Position{Line: 7, Byte: 12},
			want: [][2]int{{1, 2}, {2, 3}, {5, 6}},
		},
		{
			name: "python の else", lang: "python",
			src: "class A:\n    def f(self, a):\n        if a > 0:\n            x = 1\n            y = 2\n        elif a < 0:\n            x = 2\n            y = 3\n        else:\n            x = 3\n            y = 4\n",
			top: 6, cursor: ext.Position{Line: 10, Byte: 12},
			want: [][2]int{{0, 1}, {1, 2}, {2, 3}, {8, 9}},
		},
		{
			name: "typescript のクラスのメソッド", lang: "typescript",
			src: "class A {\n  m(a: number) {\n    if (a > 0) {\n      return 1;\n    }\n    return 0;\n  }\n}\n",
			top: 1, cursor: ext.Position{Line: 3, Byte: 6},
			want: [][2]int{{1, 2}, {2, 3}},
		},
		{
			name: "rust の impl と match の腕", lang: "rust",
			src: "impl A {\n    fn f(&self) -> i32 {\n        match self.x {\n            1 => {\n                let y = 2;\n                y\n            }\n            _ => 0,\n        }\n    }\n}\n",
			top: 1, cursor: ext.Position{Line: 5, Byte: 16},
			want: [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 4}},
		},
		{
			name: "bash の関数と if", lang: "bash",
			src: "f() {\n  if [ -n \"$1\" ]; then\n    echo one\n    echo two\n  fi\n}\n",
			top: 1, cursor: ext.Position{Line: 3, Byte: 4},
			want: [][2]int{{0, 1}, {1, 2}},
		},
		{
			name: "c の switch の case", lang: "c",
			src: "int f(int a) {\n  switch (a) {\n  case 1:\n    a++;\n    a++;\n    break;\n  }\n  return a;\n}\n",
			top: 1, cursor: ext.Position{Line: 4, Byte: 4},
			want: [][2]int{{0, 1}, {1, 2}, {2, 3}},
		},
		{
			name: "lua の else", lang: "lua",
			src: "local function f(a)\n  if a > 0 then\n    print(\"pos\")\n  else\n    print(\"neg\")\n    print(\"x\")\n  end\nend\n",
			top: 2, cursor: ext.Position{Line: 5, Byte: 4},
			want: [][2]int{{0, 1}, {1, 2}, {3, 4}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := parseWith(t, tt.lang, tt.src)
			got := headingRows(d.contextHeadings(tt.top, tt.cursor, def))
			if !slices.Equal(got, tt.want) {
				t.Errorf("contextHeadings(top %d, cursor %+v) = %v, want %v", tt.top, tt.cursor, got, tt.want)
			}
		})
	}
}
