package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
)

// inlayText は、多バイト文字（3 バイトの「日」と、UTF-16 でサロゲートペアになる 4 バイトの「𝄞」）の後ろにヒントの出る全文である。
// fakels（-inlay）は、HINT の後ろに ": T<世代>" を、PARAM の前に "p:" を返す。
const inlayText = "x := 日本HINT\n\t𝄞f(PARAM)"

// inlayWant は、inlayText の世代 gen のヒントを、本体の位置の仮想テキストにしたものである。
func inlayWant(gen string) []ext.VirtualText {
	return []ext.VirtualText{
		{Position: pos(0, 15), Placement: ext.VirtualTextInline, Chunks: []ext.VirtualTextChunk{{Text: " "}, {Text: ": T" + gen, Style: styleInlayHint}}},
		{Position: pos(1, 7), Placement: ext.VirtualTextInline, Chunks: []ext.VirtualTextChunk{{Text: "p:", Style: styleInlayHint}, {Text: " "}}},
	}
}

// virtualTextsAt は、バッファ id の版 v の virtualText/set が届くまで待つ。
func (h *host) virtualTextsAt(id ext.BufferID, v ext.Version) ext.VirtualTextSetParams {
	h.t.Helper()
	for {
		p := receive(h.t, h.vtexts, "virtualText/set")
		if p.Buffer == id && p.Version == v {
			return p
		}
	}
}

// noVirtualTexts は、d の間に virtualText/set が届かないことを確かめる。
func (h *host) noVirtualTexts(what string, d time.Duration) {
	h.t.Helper()
	select {
	case p := <-h.vtexts:
		h.t.Errorf("%s: virtualText/set = %+v, want none", what, p)
	case <-time.After(d):
	}
}

// execute は、コマンド id の command/execute をバッファ buf で送り、応答を返す。
func (h *host) execute(buf ext.BufferID, id string) ext.CommandExecuteResult {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var res ext.CommandExecuteResult
	if err := h.conn.Call(ctx, ext.MethodCommandExecute, ext.CommandExecuteParams{Buffer: buf, Version: h.vers[buf], ID: id}, &res); err != nil {
		h.t.Fatalf("command/execute %s: %v", id, err)
	}
	return res
}

// inlayRequests は、fakels が受け取った textDocument/inlayHint の数を返す。
func (h *host) inlayRequests() int {
	n := 0
	for _, e := range h.readLog() {
		if e.Kind == lspInlayHint {
			n++
		}
	}
	return n
}

// TestInlayHints は、設定の inlayHints で出すことにした言語サーバーのインレイヒントを、問い合わせた版の番号の virtualText/set で、
// inline の仮想テキストとして送ることを、UTF-16 と UTF-8 の位置の数え方で確かめる。
//
//   - 問い合わせの範囲は文書の全体（最初の行の先頭から最後の行の行末まで）である
//   - ラベルは文字列と部品の配列のどちらも読み、paddingLeft と paddingRight を色の名前の無い空白にする
//   - 文書を開いたときと、変えたときに問い合わせる
//
// req: FR-255, FR-256
func TestInlayHints(t *testing.T) {
	for _, enc := range []string{"utf-16", "utf-8"} {
		t.Run(enc, func(t *testing.T) {
			h := startHost(t, hostOptions{args: []string{"-inlay", "-encoding", enc}, maxRestarts: -1, settings: map[string]any{"inlayHints": true}})
			h.open(1, "a.go", inlayText)
			got := h.virtualTextsAt(1, 1)
			if !reflect.DeepEqual(got.VirtualTexts, inlayWant("0")) {
				t.Errorf("virtual texts = %+v, want %+v", got.VirtualTexts, inlayWant("0"))
			}
			e := h.waitLog("inlayHint", func(e logEntry) bool { return e.Kind == lspInlayHint })
			p := decode[inlayHintParams](t, e.Value)
			wantEnd := lspPosition{Line: 1, Character: 11}
			if enc == "utf-8" {
				wantEnd.Character = 13
			}
			if p.Range != (lspRange{End: wantEnd}) || !strings.HasSuffix(p.TextDocument.URI, "/a.go") {
				t.Errorf("inlayHint params = %s, want the whole document up to %+v", e.Value, wantEnd)
			}

			// 1 行目の先頭に入れると、ヒントの位置も 1 行目の分だけ後ろへずれた結果を、新しい版の番号で送る。
			h.change(1, edit(0, 0, 0, 0, "ab"))
			got = h.virtualTextsAt(1, 2)
			want := inlayWant("0")
			want[0].Position.Byte += 2
			if !reflect.DeepEqual(got.VirtualTexts, want) {
				t.Errorf("virtual texts after the edit = %+v, want %+v", got.VirtualTexts, want)
			}
		})
	}
}

// TestInlayHintsDisabled は、インレイヒントを既定では問い合わせないこと（Neovim 0.12.5 の既定と同じ）と、全体で出すことにしても
// 言語サーバーごとの inlayHints が false なら問い合わせないことと、言語サーバーが対応しなければ問い合わせないことを確かめる。
//
// req: FR-255
func TestInlayHintsDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    hostOptions
	}{
		{"既定", hostOptions{args: []string{"-inlay"}, maxRestarts: -1}},
		{"言語サーバーごとに出さない", hostOptions{
			args: []string{"-inlay"}, maxRestarts: -1, settings: map[string]any{"inlayHints": true}, extra: map[string]any{"inlayHints": false},
		}},
		{"言語サーバーが対応しない", hostOptions{maxRestarts: -1, settings: map[string]any{"inlayHints": true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startHost(t, tc.o)
			h.open(1, "a.go", inlayText)
			h.diagnosticsAt(1, 1)
			h.change(1, edit(0, 0, 0, 0, "ab"))
			h.diagnosticsAt(1, 2)
			h.noVirtualTexts(tc.name, 3*inlayInterval)
			if n := h.inlayRequests(); n != 0 {
				t.Errorf("textDocument/inlayHint sent %d times, want 0", n)
			}
		})
	}
}

// TestInlayHintsToggle は、lsp.inlayHints.toggle で、出していなければ出すことに、出していれば出さないことに切り替え、
// 出さないことにしたら空の一覧でヒントを消すことを確かめる。切り替えた値は、後から開く文書にも当てる。
//
// req: FR-257
func TestInlayHintsToggle(t *testing.T) {
	h := startHost(t, hostOptions{args: []string{"-inlay"}, maxRestarts: -1})
	h.open(1, "a.go", inlayText)
	h.diagnosticsAt(1, 1)

	if res := h.execute(1, cmdInlayHintsToggle); !res.OK {
		t.Fatalf("toggle = %+v", res)
	}
	if m := receive(t, h.msgs, "message/show"); m.Text != "インレイヒントを出す" {
		t.Errorf("message = %+v", m)
	}
	if got := h.virtualTextsAt(1, 1); !reflect.DeepEqual(got.VirtualTexts, inlayWant("0")) {
		t.Errorf("virtual texts after toggling on = %+v", got.VirtualTexts)
	}
	// 切り替えた後に開いた文書にも出す。
	h.open(2, "b.go", "HINT")
	if got := h.virtualTextsAt(2, 1); len(got.VirtualTexts) != 1 {
		t.Errorf("virtual texts of the buffer opened after toggling = %+v", got.VirtualTexts)
	}

	h.execute(1, cmdInlayHintsToggle)
	if m := receive(t, h.msgs, "message/show"); m.Text != "インレイヒントを出さない" {
		t.Errorf("message = %+v", m)
	}
	cleared := map[ext.BufferID]bool{}
	for range 2 {
		p := receive(t, h.vtexts, "virtualText/set")
		if len(p.VirtualTexts) != 0 {
			t.Errorf("virtualText/set after toggling off = %+v, want empty", p)
		}
		cleared[p.Buffer] = true
	}
	if !cleared[1] || !cleared[2] {
		t.Errorf("cleared buffers = %v, want 1 and 2", cleared)
	}
	n := h.inlayRequests()
	h.change(1, edit(0, 0, 0, 0, "ab"))
	h.diagnosticsAt(1, 2)
	h.noVirtualTexts("after toggling off", 3*inlayInterval)
	if got := h.inlayRequests(); got != n {
		t.Errorf("textDocument/inlayHint sent %d times after toggling off, want %d", got, n)
	}
}

// TestInlayHintsRefresh は、言語サーバーの workspace/inlayHint/refresh に null で答え、開いている文書のヒントを問い合わせ直すことを確かめる。
// fakels は didSave でヒントの世代を進めてから refresh を送るので、文書を変えずに新しい世代のヒントが届く。
//
// req: FR-256
func TestInlayHintsRefresh(t *testing.T) {
	h := startHost(t, hostOptions{args: []string{"-inlay"}, maxRestarts: -1, settings: map[string]any{"inlayHints": true}})
	path := h.open(1, "a.go", inlayText)
	h.virtualTextsAt(1, 1)
	if err := h.conn.Notify(ext.MethodBufferDidSave, ext.BufferDidSaveParams{Buffer: 1, Version: 1, Path: path}); err != nil {
		t.Fatal(err)
	}
	if got := h.virtualTextsAt(1, 1); !reflect.DeepEqual(got.VirtualTexts, inlayWant("1")) {
		t.Errorf("virtual texts after refresh = %+v, want %+v", got.VirtualTexts, inlayWant("1"))
	}
	e := h.waitLog("refresh result", func(e logEntry) bool { return e.Kind == "refresh result" })
	if string(e.Value) != "null" {
		t.Errorf("refresh result = %s, want null", e.Value)
	}
}

// TestInlayHintsOneInFlight は、応答を待っている間に文書が何度変わっても、1 つの文書の問い合わせを 1 つまでにし、
// 応答が届いた後に最後の版を問い合わせ直すことを確かめる。遅い言語サーバーに問い合わせを溜めず、取り消し続けることもない。
//
// req: FR-256
func TestInlayHintsOneInFlight(t *testing.T) {
	h := startHost(t, hostOptions{
		args: []string{"-inlay", "-inlay-delay", "300ms"}, maxRestarts: -1, settings: map[string]any{"inlayHints": true},
		inlayInterval: 10 * time.Millisecond,
	})
	h.open(1, "a.go", "HINT")
	h.waitLog("inlayHint", func(e logEntry) bool { return e.Kind == lspInlayHint })
	for range 5 {
		h.change(1, edit(0, 0, 0, 0, "a"))
	}
	if got := h.virtualTextsAt(1, 1); len(got.VirtualTexts) != 1 || got.VirtualTexts[0].Position != pos(0, 4) {
		t.Errorf("first virtual texts = %+v", got)
	}
	if got := h.virtualTextsAt(1, 6); len(got.VirtualTexts) != 1 || got.VirtualTexts[0].Position != pos(0, 9) {
		t.Errorf("last virtual texts = %+v", got)
	}
	if n := h.inlayRequests(); n != 2 {
		t.Errorf("textDocument/inlayHint sent %d times, want 2", n)
	}
	for _, e := range h.readLog() {
		if e.Kind == "inlayHint cancelled" {
			t.Error("an inlayHint request was cancelled")
		}
	}
}

// TestInlayHintsServerExit は、言語サーバーが異常終了したら、出していたヒントを空の一覧で消し、再起動した言語サーバーに問い合わせ直すことと、
// ファイルの種類を言語サーバーの無いものに変えたら（buffer/open の送り直し）、ヒントを消すことを確かめる。
//
// req: FR-255
func TestInlayHintsServerExit(t *testing.T) {
	h := startHost(t, hostOptions{args: []string{"-inlay"}, maxRestarts: 1, settings: map[string]any{"inlayHints": true}})
	path := h.open(1, "a.go", "HINT")
	h.virtualTextsAt(1, 1)
	h.change(1, edit(0, 0, 0, 0, "CRASH "))
	if got := h.virtualTextsAt(1, 2); len(got.VirtualTexts) != 0 {
		// 終わる前の言語サーバーが版 2 のヒントを返すことはない（didChange の直後に終わる）ので、最初に届く版 2 の一覧は消すためのもの。
		t.Fatalf("virtual texts after the crash = %+v, want cleared", got.VirtualTexts)
	}
	if got := h.virtualTextsAt(1, 2); len(got.VirtualTexts) != 1 || got.VirtualTexts[0].Position != pos(0, 10) {
		t.Errorf("virtual texts from the restarted server = %+v", got.VirtualTexts)
	}

	if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Path: path, FileType: "text", Version: 2, Text: h.texts[1]}); err != nil {
		t.Fatal(err)
	}
	if got := h.virtualTextsAt(1, 2); len(got.VirtualTexts) != 0 {
		t.Errorf("virtual texts after changing the file type = %+v, want cleared", got.VirtualTexts)
	}
}

// TestInlayTexts は、InlayHint の結果の変換を確かめる。ラベルの空なヒントは捨て、仮想の行（言語サーバーへ送った末尾の改行の後ろ）を
// 指す位置は最後の行の行末にし、kind で色を分けない。
//
// req: FR-255
func TestInlayTexts(t *testing.T) {
	var hints []inlayHint
	raw := `[
		{"position": {"line": 0, "character": 1}, "label": "a", "kind": 1},
		{"position": {"line": 0, "character": 1}, "label": [{"value": "b"}, {"value": "c", "location": {"uri": "file:///x"}}], "kind": 2, "paddingLeft": true, "paddingRight": true},
		{"position": {"line": 0, "character": 0}, "label": ""},
		{"position": {"line": 0, "character": 0}, "label": 3},
		{"position": {"line": 1, "character": 0}, "label": "end"}
	]`
	if err := json.Unmarshal([]byte(raw), &hints); err != nil {
		t.Fatal(err)
	}
	got := inlayTexts(lines{"日x"}, encUTF16, hints)
	want := []ext.VirtualText{
		{Position: pos(0, 3), Placement: ext.VirtualTextInline, Chunks: []ext.VirtualTextChunk{{Text: "a", Style: styleInlayHint}}},
		{Position: pos(0, 3), Placement: ext.VirtualTextInline, Chunks: []ext.VirtualTextChunk{{Text: " "}, {Text: "bc", Style: styleInlayHint}, {Text: " "}}},
		{Position: pos(0, 4), Placement: ext.VirtualTextInline, Chunks: []ext.VirtualTextChunk{{Text: "end", Style: styleInlayHint}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("inlayTexts = %+v, want %+v", got, want)
	}
}

// TestParseSettingsInlayHints は、inlayHints の既定が false で、最上位の値を言語サーバーごとの値が上書きすることを確かめる。
//
// req: FR-255
func TestParseSettingsInlayHints(t *testing.T) {
	c, err := parseSettings(nil)
	if err != nil || c.servers["go"].inlayHints() {
		t.Errorf("default inlayHints = %v, %v; want false", c.servers["go"].inlayHints(), err)
	}
	c, err = parseSettings(json.RawMessage(`{"inlayHints": true, "servers": {"a": {"command": "a"}, "b": {"command": "b", "inlayHints": false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.servers["a"].inlayHints() || c.servers["b"].inlayHints() || !c.servers["go"].inlayHints() {
		t.Errorf("inlayHints = a %v, b %v, go %v; want true, false, true",
			c.servers["a"].inlayHints(), c.servers["b"].inlayHints(), c.servers["go"].inlayHints())
	}
}
