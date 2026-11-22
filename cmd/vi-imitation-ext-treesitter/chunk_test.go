package main

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// chunkSource は、罫線を試す Go のソースである。行の番号（0 から）は次のとおりである。
//
//	2  func f(a int) int {
//	3  	if a > 0 {
//	4  		a++
//	5  	（空の行）
//	6  		a += 2
//	7  	}
//	8  	return a
//	9  }
const chunkSource = "package main\n\nfunc f(a int) int {\n\tif a > 0 {\n\t\ta++\n\n\t\ta += 2\n\t}\n\treturn a\n}"

// drawChunk は、src の行のタブ文字を tab の幅の空白に広げ、overlays を重ねた画面の行を返す（本体の overlay の描き方を写したもの）。
// 行末の空白は除く。
func drawChunk(t *testing.T, src string, overlays []ext.VirtualText, tab int) []string {
	t.Helper()
	lines := strings.Split(src, "\n")
	cells := make([][]string, len(lines))
	cols := make([][]int, len(lines)) // バイトごとの表示の列。最後は行末の列
	for i, l := range lines {
		col := 0
		for j := 0; j < len(l); {
			cols[i] = append(cols[i], col)
			if l[j] == '\t' {
				w := tab - col%tab
				for range w {
					cells[i] = append(cells[i], " ")
				}
				col += w
				j++
				continue
			}
			if l[j] == '\r' {
				// 本体は復帰を ^M の 2 列で描く。
				cells[i] = append(cells[i], "^", "M")
				col += crWidth
				j++
				continue
			}
			r := []rune(l[j:])[0]
			cells[i] = append(cells[i], string(r))
			col++
			for range len(string(r)) - 1 {
				cols[i] = append(cols[i], col-1)
			}
			j += len(string(r))
		}
		cols[i] = append(cols[i], col)
	}
	for _, v := range overlays {
		if v.Placement != ext.VirtualTextOverlay || len(v.Chunks) != 1 {
			t.Fatalf("overlay = %+v", v)
		}
		row := v.Position.Line
		col := cols[row][v.Position.Byte]
		for _, r := range v.Chunks[0].Text {
			for len(cells[row]) <= col {
				cells[row] = append(cells[row], " ")
			}
			cells[row][col] = string(r)
			col++
		}
	}
	out := make([]string, len(lines))
	for i := range cells {
		out[i] = strings.TrimRight(strings.Join(cells[i], ""), " ")
	}
	return out
}

// TestChunkAtGo は、カーソルを含むまとまりの罫線の形が、hlchunk.nvim（コミット 4ddc056e4ca45f1c7dc6aa21935df710b263b1aa）の
// get_chunk_data と同じになることを、本体の overlay の描き方で重ねた画面で確かめる。
// 期待値の形は、2026-11-22 に Neovim 0.12.5 で hlchunk.nvim を既定の設定で動かした画面（tmux の capture-pane）と同じである。
//
// req: FR-265
func TestChunkAtGo(t *testing.T) {
	h := newTestHighlighter(t)
	sym := defaultChunkSettings.symbols
	tests := []struct {
		name   string
		src    string
		cursor ext.Position
		opt    indentOptions
		want   []string // 罫線のある行だけ（行の番号から）
		from   int
	}{
		{
			name: "if の中。始まりと終わりの行の角と矢印、空の行にも縦線", src: chunkSource,
			cursor: ext.Position{Line: 4, Byte: 2}, opt: indentOptions{tab: 4, shift: 4}, from: 3,
			want: []string{"╭───if a > 0 {", "│       a++", "│", "│       a += 2", "╰──>}"},
		},
		{
			// 字下げの無い始まりと終わりの行には角と横線を描かない。
			name: "関数の本体。縦線だけ", src: chunkSource,
			cursor: ext.Position{Line: 8, Byte: 1}, opt: indentOptions{tab: 4, shift: 4}, from: 2,
			want: []string{"func f(a int) int {", "│   if a > 0 {", "│       a++", "│", "│       a += 2", "│   }", "│   return a", "}"},
		},
		{
			// タブ文字の幅 8、字下げの幅 4 では、罫線の列はタブ文字の途中の 4 列目になる。
			name: "タブ文字の途中の列", src: chunkSource,
			cursor: ext.Position{Line: 4, Byte: 2}, opt: indentOptions{tab: 8, shift: 4}, from: 3,
			want: []string{"    ╭───if a > 0 {", "    │           a++", "    │", "    │           a += 2", "    ╰──>}"},
		},
		{
			name: "見出しの行のカーソルも同じまとまり", src: chunkSource,
			cursor: ext.Position{Line: 3, Byte: 1}, opt: indentOptions{tab: 4, shift: 4}, from: 3,
			want: []string{"╭───if a > 0 {", "│       a++", "│", "│       a += 2", "╰──>}"},
		},
		{
			// 1 行の { } はまとまりにせず、外の関数の罫線を描く。行の最初から本文の始まる行（生の文字列の続き）には縦線を描かない。
			name: "1 行のまとまりと、行の最初から始まる本文", src: "package main\n\nfunc g() {\n\tx := []int{1, 2}\n\ts := `\nraw`\n\t_, _ = x, s\n}",
			cursor: ext.Position{Line: 3, Byte: 12}, opt: indentOptions{tab: 4, shift: 4}, from: 3,
			want: []string{"│   x := []int{1, 2}", "│   s := `", "raw`", "│   _, _ = x, s"},
		},
		{
			// 改行が CRLF のファイルの空の行（中身は \r だけ）にも縦線を描く。本体は \r を ^M の 2 列で描くので、縦線はその最初の列に重なる
			// （PR #396 のレビューの推奨 5）。
			name: "CRLF の空の行", src: strings.ReplaceAll(chunkSource, "\n", "\r\n"),
			cursor: ext.Position{Line: 4, Byte: 2}, opt: indentOptions{tab: 4, shift: 4}, from: 3,
			want: []string{"╭───if a > 0 {^M", "│       a++^M", "│M", "│       a += 2^M", "╰──>}^M"},
		},
		{
			// 字下げの幅が始まりの行の字下げより広ければ、罫線の列は 0 で、角の列が 1 つなら角だけを描く。
			name: "列が 1 つなら角だけ", src: "{\n \"a\": [\n  1,\n  2\n ]\n}",
			cursor: ext.Position{Line: 2, Byte: 2}, opt: indentOptions{tab: 8, shift: 4}, from: 1,
			want: []string{"╭\"a\": [", "│ 1,", "│ 2", "╰]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := "go"
			if strings.HasPrefix(tt.src, "{") {
				ft = "json"
			}
			d := parse(t, h, ft, tt.src)
			shape, ok := d.chunkAt(tt.cursor, allRows, tt.opt, sym, true)
			if !ok || shape.style != chunkStyle {
				t.Fatalf("chunkAt = %+v, %v", shape, ok)
			}
			got := drawChunk(t, tt.src, chunkOverlays(shape.cells, shape.style), tt.opt.tab)[tt.from:]
			got = got[:len(tt.want)]
			if !slices.Equal(got, tt.want) {
				t.Errorf("画面 =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// TestChunkAtOrder は、描く順（描く動きで置く順）が、始まりの行の本文の側から角へ、縦線を上から下へ、終わりの行の角から矢印へであること、
// 記号を設定で変えられること、まとまりの外では罫線が無いこと、構文の誤りを含むまとまりを chunk.error の色にすることを確かめる。
//
// req: FR-265, FR-267
func TestChunkAtOrder(t *testing.T) {
	h := newTestHighlighter(t)
	d := parse(t, h, "go", chunkSource)
	owner := chunkSymbols{horizontalLine: "═", verticalLine: "║", leftTop: "╔", leftBottom: "╚", leftArrow: "─", rightArrow: "▶"}
	shape, ok := d.chunkAt(ext.Position{Line: 4, Byte: 2}, allRows, indentOptions{tab: 4, shift: 4}, owner, true)
	if !ok {
		t.Fatal("chunkAt: no chunk")
	}
	var order []string
	for _, c := range shape.cells {
		order = append(order, c.text)
	}
	if got, want := strings.Join(order, ""), "─══╔║║║╚══▶"; got != want {
		t.Errorf("描く順 = %q, want %q", got, want)
	}
	if got := drawChunk(t, chunkSource, chunkOverlays(shape.cells, shape.style), 4)[3]; got != "╔══─if a > 0 {" {
		t.Errorf("始まりの行 = %q", got)
	}
	// 描く動きの途中（始まりの行の右の 2 つ）は、その 2 列だけを重ねる。
	if got := drawChunk(t, chunkSource, chunkOverlays(shape.cells[:2], shape.style), 4)[3]; got != "  ═─if a > 0 {" {
		t.Errorf("描く途中の始まりの行 = %q", got)
	}
	if _, ok := d.chunkAt(ext.Position{Line: 0, Byte: 0}, allRows, indentOptions{tab: 4, shift: 4}, owner, true); ok {
		t.Error("package の行で罫線がある")
	}

	broken := "package main\n\nfunc f() {\n\tif x {\n\t\ty := )\n\t}\n}"
	e := parse(t, h, "go", broken)
	if s, ok := e.chunkAt(ext.Position{Line: 4, Byte: 2}, allRows, indentOptions{tab: 4, shift: 4}, owner, true); !ok || s.style != chunkErrorStyle {
		t.Errorf("誤りを含むまとまり = %+v, %v, want the style %s", s, ok, chunkErrorStyle)
	}
	if s, ok := e.chunkAt(ext.Position{Line: 4, Byte: 2}, allRows, indentOptions{tab: 4, shift: 4}, owner, false); !ok || s.style != chunkStyle {
		t.Errorf("errorSign が false の誤りを含むまとまり = %+v, %v, want the style %s", s, ok, chunkStyle)
	}
}

// TestChunkAtLanguages は、設定で足す言語（Python、Lua）で、字下げで本体を書くまとまりと end で閉じるまとまりの罫線を確かめる。
// 文法は testParsersEnv の下のものを使うので、CI では飛ばす（block_lang_test.go）。
//
// req: FR-265
func TestChunkAtLanguages(t *testing.T) {
	tests := []struct {
		name, lang, src string
		cursor          ext.Position
		want            []string
	}{
		{
			// Python の関数の終わりの行は本体の最後の文で、その字下げの終わりまで横線と矢印を描く。
			name: "python の if", lang: "python",
			src:    "def f(a):\n    if a > 0:\n        x = 1\n\n        y = 2\n    return a",
			cursor: ext.Position{Line: 2, Byte: 8},
			want:   []string{"def f(a):", "╭───if a > 0:", "│       x = 1", "│", "╰──────>y = 2", "    return a"},
		},
		{
			name: "lua の if ... end", lang: "lua",
			src:    "local function f(a)\n    if a > 0 then\n        print(a)\n    end\nend",
			cursor: ext.Position{Line: 2, Byte: 8},
			want:   []string{"local function f(a)", "╭───if a > 0 then", "│       print(a)", "╰──>end", "end"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := parseWith(t, tt.lang, tt.src)
			shape, ok := d.chunkAt(tt.cursor, allRows, indentOptions{tab: 4, shift: 4}, defaultChunkSettings.symbols, true)
			if !ok {
				t.Fatal("chunkAt: no chunk")
			}
			if got := drawChunk(t, tt.src, chunkOverlays(shape.cells, shape.style), 4); !slices.Equal(got, tt.want) {
				t.Errorf("画面 =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// TestParseChunk は、設定の chunk の既定の値が hlchunk.nvim の既定と同じで、オーナーの値を読め、誤りの項目は既定の値のまま知らせることを確かめる。
//
// req: FR-267
func TestParseChunk(t *testing.T) {
	s, problems := parseSettings(nil)
	if len(problems) != 0 || s.chunk != defaultChunkSettings || s.chunk.enable || s.chunk.delay != 300 || s.chunk.duration != 200 || !s.chunk.errorSign {
		t.Errorf("既定 = %+v, %v", s.chunk, problems)
	}
	s, problems = parseSettings(json.RawMessage(`{"chunk": {"enable": true, "delay": 100, "duration": 100, "errorSign": false,
		"symbols": {"horizontalLine": "═", "verticalLine": "║", "leftTop": "╔", "leftBottom": "╚", "rightArrow": "▶"}}}`))
	want := chunkSettings{enable: true, delay: 100, duration: 100, errorSign: false, maxFileSize: 1 << 20, symbols: chunkSymbols{
		horizontalLine: "═", verticalLine: "║", leftTop: "╔", leftBottom: "╚", leftArrow: "─", rightArrow: "▶",
	}}
	if len(problems) != 0 || s.chunk != want {
		t.Errorf("オーナーの値 = %+v, %v, want %+v", s.chunk, problems, want)
	}
	s, problems = parseSettings(json.RawMessage(`{"chunk": {"enable": true, "delay": -1, "duration": 20000, "maxFileSize": -1, "symbols": {"leftTop": "ab", "verticalLine": "\t", "rightArrow": " ", "up": "^"}}}`))
	if !s.chunk.enable || s.chunk.delay != 300 || s.chunk.duration != 200 || s.chunk.maxFileSize != 1<<20 || s.chunk.symbols != defaultChunkSettings.symbols || len(problems) != 7 {
		t.Errorf("誤りのある値 = %+v, %q", s.chunk, problems)
	}
	if _, problems = parseSettings(json.RawMessage(`{"chunk": {"enabled": true}}`)); len(problems) != 1 {
		t.Errorf("知らない項目の誤り = %q", problems)
	}
}

// chunkHost は、罫線を試すための本体の役の接続である。option/get に tab.width と indent.width を答える。
type chunkHost struct {
	conn  *rpc.Conn
	decs  chan ext.DecorationSetParams
	virts chan ext.VirtualTextSetParams
}

// connectChunk は、highlighter の Server を io.Pipe の上で動かし、settings を initialize の設定として渡して、本体の役の接続を返す。
func connectChunk(t *testing.T, settings string, options map[string]json.RawMessage) *chunkHost {
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
	host := &chunkHost{decs: make(chan ext.DecorationSetParams, 64), virts: make(chan ext.VirtualTextSetParams, 256)}
	host.conn = rpc.NewConn(hostR, hostW, rpc.HandlerFuncs{
		Request: func(_ context.Context, req *rpc.Request, reply rpc.Replier) {
			if req.Method != ext.MethodOptionGet {
				_ = reply(nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: req.Method})
				return
			}
			_ = reply(ext.OptionGetResult{Values: options}, nil)
		},
		Notification: func(_ context.Context, n *rpc.Notification) {
			switch n.Method {
			case ext.MethodDecorationSet:
				var p ext.DecorationSetParams
				if err := json.Unmarshal(n.Params, &p); err != nil {
					t.Errorf("decoration/set: %v", err)
				}
				host.decs <- p
			case ext.MethodVirtualTextSet:
				var p ext.VirtualTextSetParams
				if err := json.Unmarshal(n.Params, &p); err != nil {
					t.Errorf("virtualText/set: %v", err)
				}
				host.virts <- p
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
	if err := host.conn.Call(ctx, ext.MethodInitialize, ext.InitializeParams{APIVersion: ext.APIVersion, Settings: json.RawMessage(settings)}, &init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return host
}

func (h *chunkHost) notify(t *testing.T, method string, params any) {
	t.Helper()
	if err := h.conn.Notify(method, params); err != nil {
		t.Fatal(err)
	}
}

func (h *chunkHost) virt(t *testing.T) ext.VirtualTextSetParams {
	t.Helper()
	select {
	case p := <-h.virts:
		return p
	case <-time.After(testTimeout):
		t.Fatal("virtualText/set did not arrive")
		return ext.VirtualTextSetParams{}
	}
}

// quiet は、d の間に virtualText/set が届かないことを確かめる。
func (h *chunkHost) quiet(t *testing.T, d time.Duration, why string) {
	t.Helper()
	select {
	case p := <-h.virts:
		t.Errorf("%s: virtualText/set = %+v, want none", why, p)
	case <-time.After(d):
	}
}

// open は、chunkSource のバッファ 1 を開いて、色の一覧が届くのを待つ。
func (h *chunkHost) open(t *testing.T) {
	t.Helper()
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "go", Text: chunkSource})
	<-h.decs
}

var tab4 = map[string]json.RawMessage{"tab.width": json.RawMessage("4"), "indent.width": json.RawMessage("0")}

// TestServeChunk は、window/change でカーソルを含むまとまりの罫線が届き、同じまとまりの中の移動とスクロールでは送り直さず、
// 別のまとまりへ移ると送り直し、編集の後の解析で新しいバージョン番号で送り直し、窓が無くなると消すことを確かめる。
// option/get の tab.width と indent.width（0 は tab.width と同じ）で罫線の列を数える。
//
// req: FR-265, FR-266
func TestServeChunk(t *testing.T) {
	h := connectChunk(t, `{"chunk": {"enable": true, "delay": 0, "duration": 0}}`, tab4)
	h.open(t)
	win := ext.WindowChangeParams{Window: "client/1", Buffer: 1, Version: 1, Top: 0, Bottom: 10, Rows: 20, Columns: 80, Cursor: ext.Position{Line: 4, Byte: 2}, Seq: 1}
	h.notify(t, ext.MethodWindowChange, win)
	p := h.virt(t)
	want := []string{"╭───if a > 0 {", "│       a++", "│", "│       a += 2", "╰──>}"}
	if got := drawChunk(t, chunkSource, p.VirtualTexts, 4)[3:8]; p.Buffer != 1 || p.Version != 1 || !slices.Equal(got, want) {
		t.Fatalf("virtualText/set = %+v\n%s", p, strings.Join(got, "\n"))
	}
	if s := p.VirtualTexts[0].Chunks[0].Style; s != chunkStyle {
		t.Errorf("style = %q", s)
	}

	win.Cursor, win.Seq = ext.Position{Line: 6, Byte: 3}, 2
	h.notify(t, ext.MethodWindowChange, win)
	win.Top, win.Seq = 1, 3
	h.notify(t, ext.MethodWindowChange, win)
	h.quiet(t, 100*time.Millisecond, "同じまとまりの中の移動とスクロール")

	win.Cursor, win.Seq = ext.Position{Line: 8, Byte: 1}, 4
	h.notify(t, ext.MethodWindowChange, win)
	if p := h.virt(t); len(p.VirtualTexts) != 6 {
		t.Errorf("関数の本体の罫線 = %+v, want 6 vertical lines", p.VirtualTexts)
	}

	// 先頭に 2 行を足すと、解析の後に、待たずに新しい構文木とバージョン番号で決め直す。カーソルは最後の知らせの位置（8 行目）のままなので、
	// 2 行下がった if の中（5 行目から 9 行目）の罫線になる（本体は、編集の後のカーソルを次の window/change で知らせる）。
	h.notify(t, ext.MethodBufferChange, ext.BufferChangeParams{Buffer: 1, FromVersion: 1, ToVersion: 2, Edits: []ext.Edit{
		{Range: ext.Range{Start: ext.Position{Line: 0, Byte: 12}, End: ext.Position{Line: 0, Byte: 12}}, Text: "\n\n// c"},
	}})
	<-h.decs
	if p := h.virt(t); p.Version != 2 || len(p.VirtualTexts) != 5 || p.VirtualTexts[0].Position.Line != 5 {
		t.Errorf("編集の後の罫線 = %+v", p)
	}

	h.notify(t, ext.MethodWindowChange, ext.WindowChangeParams{Window: "client/1", Closed: true, Seq: 5})
	if p := h.virt(t); len(p.VirtualTexts) != 0 || p.Buffer != 1 {
		t.Errorf("窓が無くなった後 = %+v, want an empty list", p)
	}
}

// TestServeChunkWindows は、同じバッファを出す 2 つの窓では、最後にカーソルが動いた窓のカーソルで罫線を決め、
// その窓が別のバッファへ移ったら、残った窓のカーソルで決め直すことを確かめる。
//
// req: FR-265
func TestServeChunkWindows(t *testing.T) {
	h := connectChunk(t, `{"chunk": {"enable": true, "delay": 0, "duration": 0}}`, tab4)
	h.open(t)
	one := ext.WindowChangeParams{Window: "client/1", Buffer: 1, Version: 1, Bottom: 10, Rows: 20, Columns: 80, Cursor: ext.Position{Line: 4, Byte: 2}, Seq: 1}
	h.notify(t, ext.MethodWindowChange, one)
	if p := h.virt(t); len(p.VirtualTexts) != 5 {
		t.Fatalf("窓 1 の罫線 = %+v", p.VirtualTexts)
	}
	two := one
	two.Window, two.Cursor, two.Seq = "client/2", ext.Position{Line: 8, Byte: 1}, 2
	h.notify(t, ext.MethodWindowChange, two)
	if p := h.virt(t); len(p.VirtualTexts) != 6 {
		t.Fatalf("窓 2 の罫線 = %+v", p.VirtualTexts)
	}
	two.Buffer, two.Seq = 2, 3
	h.notify(t, ext.MethodWindowChange, two)
	if p := h.virt(t); p.Buffer != 1 || len(p.VirtualTexts) != 5 {
		t.Errorf("窓 2 が離れた後の罫線 = %+v, want the chunk of the window 1", p)
	}
}

// TestServeChunkAnimation は、カーソルが止まって delay が過ぎてから、duration の間にこまを分けて罫線を描き、
// 前の罫線があれば消してから描き始め、編集の後は描く動きをせずに一度に描くことを確かめる。
// 設定で出さないにすると、窓の知らせでは何も送らないことも確かめる。
//
// req: FR-266, FR-267
func TestServeChunkAnimation(t *testing.T) {
	h := connectChunk(t, `{"chunk": {"enable": true, "delay": 20, "duration": 64}}`, tab4)
	h.open(t)
	win := ext.WindowChangeParams{Window: "client/1", Buffer: 1, Version: 1, Bottom: 10, Rows: 20, Columns: 80, Cursor: ext.Position{Line: 8, Byte: 1}, Seq: 1}
	start := time.Now()
	h.notify(t, ext.MethodWindowChange, win)
	// 関数の本体の 6 つの縦線を、64 ミリ秒を 16 ミリ秒ごとの 4 こまに分けて描く。
	for i, n := range []int{2, 3, 5, 6} {
		if p := h.virt(t); len(p.VirtualTexts) != n {
			t.Fatalf("こま %d = %d 個, want %d", i+1, len(p.VirtualTexts), n)
		}
	}
	if d := time.Since(start); d < 20*time.Millisecond+64*time.Millisecond {
		t.Errorf("描き終えるまで %v, want delay と duration の和の 84ms 以上", d)
	}
	win.Cursor, win.Seq = ext.Position{Line: 4, Byte: 2}, 2
	h.notify(t, ext.MethodWindowChange, win)
	if p := h.virt(t); len(p.VirtualTexts) != 0 {
		t.Fatalf("描き始めの前 = %+v, want an empty list", p.VirtualTexts)
	}
	for range 4 {
		h.virt(t)
	}
	h.notify(t, ext.MethodBufferChange, ext.BufferChangeParams{Buffer: 1, FromVersion: 1, ToVersion: 2, Edits: []ext.Edit{
		{Range: ext.Range{Start: ext.Position{Line: 4, Byte: 5}, End: ext.Position{Line: 4, Byte: 5}}, Text: "\n\t\ta++"},
	}})
	<-h.decs
	if p := h.virt(t); p.Version != 2 || len(p.VirtualTexts) != 6 {
		t.Errorf("編集の後 = %+v, want the whole chunk at once", p)
	}

	off := connectChunk(t, `{"chunk": {"delay": 0, "duration": 0}}`, tab4)
	off.open(t)
	off.notify(t, ext.MethodWindowChange, win)
	off.quiet(t, 100*time.Millisecond, "出さない設定")
}

// TestServeChunkDebounce は、delay より短い間隔でカーソルが動き続ける間は描かず、最後の移動から delay の後に、
// 最後のカーソルのまとまりの罫線だけを描くことを確かめる（hlchunk.nvim の debounce。PR #396 のレビューの推奨 3）。
//
// req: FR-266
func TestServeChunkDebounce(t *testing.T) {
	h := connectChunk(t, `{"chunk": {"enable": true, "delay": 80, "duration": 0}}`, tab4)
	h.open(t)
	win := ext.WindowChangeParams{Window: "client/1", Buffer: 1, Version: 1, Bottom: 10, Rows: 20, Columns: 80}
	// if の中、関数の本体、if の中の順に 50 ミリ秒ごとに動かす。待ち直さなければ、2 回目の移動の後の 80 ミリ秒の時点で関数の本体の罫線が届く。
	var last time.Time
	for i, line := range []int{4, 8, 4} {
		win.Cursor, win.Seq = ext.Position{Line: line, Byte: 2}, uint64(i+1)
		if i > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		last = time.Now()
		h.notify(t, ext.MethodWindowChange, win)
	}
	p := h.virt(t)
	if d := time.Since(last); d < 80*time.Millisecond {
		t.Errorf("最後の移動から %v で届いた。want delay の 80ms 以上", d)
	}
	if got := drawChunk(t, chunkSource, p.VirtualTexts, 4)[3:8]; !slices.Equal(got, []string{"╭───if a > 0 {", "│       a++", "│", "│       a += 2", "╰──>}"}) {
		t.Errorf("罫線 =\n%s\nwant the chunk of the if", strings.Join(got, "\n"))
	}
	h.quiet(t, 150*time.Millisecond, "最後の移動の罫線の後")
}

// TestChunkAtView は、セルを窓の見えている行とその前後（行の範囲）の中だけで作り、範囲の外の始まりと終わりの行の角も作らないこと、
// 範囲が違っても同じまとまりと分かることを確かめる（PR #396 のレビューの推奨 2）。
//
// req: FR-265
func TestChunkAtView(t *testing.T) {
	h := newTestHighlighter(t)
	src := "package main\n\nfunc f() {\n\tif true {\n" + strings.Repeat("\t\tx++\n", 300) + "\t}\n}"
	d := parse(t, h, "go", src)
	opt := indentOptions{tab: 4, shift: 4}
	view := rowRange{lo: 100, hi: 120}
	s, ok := d.chunkAt(ext.Position{Line: 110, Byte: 2}, view, opt, defaultChunkSettings.symbols, true)
	if !ok || len(s.cells) != 21 || s.cells[0].row != 100 || s.cells[20].row != 120 {
		t.Fatalf("範囲の中のセル = %d 個 %+v", len(s.cells), s)
	}
	all, _ := d.chunkAt(ext.Position{Line: 110, Byte: 2}, allRows, opt, defaultChunkSettings.symbols, true)
	if !s.sameChunk(all) || len(all.cells) != 4+300+4 {
		t.Errorf("すべての行のセル = %d 個, sameChunk = %v", len(all.cells), s.sameChunk(all))
	}
	if !s.covers(rowRange{105, 115}) || s.covers(rowRange{115, 125}) || !s.covers(rowRange{400, 410}) {
		t.Error("covers が見えている行とセルの行の範囲を比べていない")
	}
}

// TestServeChunkView は、送るセルを窓の見えている行とその前後に絞り、見えている行がその外へ出るスクロールで描き直し、
// 中でのスクロールでは送り直さないことと、maxFileSize 以上の大きさのバッファには描かないことを確かめる（PR #396 のレビューの推奨 2）。
//
// req: FR-265, FR-267
func TestServeChunkView(t *testing.T) {
	src := "package main\n\nfunc f() {\n" + strings.Repeat("\tx++\n", 300) + "}"
	h := connectChunk(t, `{"chunk": {"enable": true, "delay": 0, "duration": 0}}`, tab4)
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, FileType: "go", Text: src})
	<-h.decs
	win := ext.WindowChangeParams{Window: "client/1", Buffer: 1, Version: 1, Top: 0, Bottom: 20, Rows: 20, Columns: 80, Cursor: ext.Position{Line: 5, Byte: 1}, Seq: 1}
	h.notify(t, ext.MethodWindowChange, win)
	// 見えている 0〜20 行と、前後の 20 行（40 行目まで）の縦線。関数の行（2 行目）と閉じの行には字下げが無いので角を描かない。
	if p := h.virt(t); len(p.VirtualTexts) != 38 || p.VirtualTexts[0].Position.Line != 3 || p.VirtualTexts[37].Position.Line != 40 {
		t.Fatalf("最初の罫線 = %d 個", len(p.VirtualTexts))
	}
	win.Top, win.Bottom, win.Seq = 100, 120, 2
	h.notify(t, ext.MethodWindowChange, win)
	if p := h.virt(t); len(p.VirtualTexts) != 61 || p.VirtualTexts[0].Position.Line != 80 {
		t.Fatalf("送ったセルの外へのスクロールの後 = %d 個", len(p.VirtualTexts))
	}
	win.Top, win.Bottom, win.Seq = 110, 130, 3
	h.notify(t, ext.MethodWindowChange, win)
	h.quiet(t, 100*time.Millisecond, "送ったセルの中のスクロール")

	small := connectChunk(t, `{"chunk": {"enable": true, "delay": 0, "duration": 0, "maxFileSize": 50}}`, tab4)
	small.open(t)
	small.notify(t, ext.MethodWindowChange, ext.WindowChangeParams{Window: "client/1", Buffer: 1, Version: 1, Bottom: 10, Rows: 20, Columns: 80, Cursor: ext.Position{Line: 4, Byte: 2}, Seq: 1})
	small.quiet(t, 100*time.Millisecond, "maxFileSize 以上のバッファ")
}
