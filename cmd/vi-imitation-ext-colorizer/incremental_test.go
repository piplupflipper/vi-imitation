package main

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
)

// incPieces は、長い行を作る断片である。色コードと、その前後の区切りになる文字とならない文字を混ぜる。
var incPieces = []string{
	"#fff", "#ff0000", "#f008", "red", "Blue", "x", " ", "-", "_", ";", "(", ")", "rgb(1, 2, 3)", "hsl(120 50% 50%)",
	"rgb(", "0x80ff0000", "1", "a", ",", "/", "%", "é", "#", "lightblue", "white", "text-red-500",
}

func randomText(r *rand.Rand, n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(incPieces[r.IntN(len(incPieces))])
	}
	return b.String()
}

// randomLineEdit は、行 line の中の範囲を、乱数の断片の並びで置き換えた行と、編集の位置を返す。
// 位置は文字の境目にする（本体は文字の途中の位置の編集を送らない）。
func randomLineEdit(r *rand.Rand, line string) (string, int, int, int) {
	boundary := func(p int) int {
		for p < len(line) && !utf8.RuneStart(line[p]) {
			p--
		}
		return p
	}
	start := boundary(r.IntN(len(line) + 1))
	end := max(start, boundary(min(len(line), start+r.IntN(8))))
	text := ""
	for range r.IntN(3) {
		text += incPieces[r.IntN(len(incPieces))]
	}
	return line[:start] + text + line[end:], start, end, start + len(text)
}

// TestRescanAroundMatchesScanLine は、編集の周りだけを数え直した結果が、行の全体を数えた結果と同じになることを確かめる。
// 手がかりを合わせた編集（数えていない行への続けての編集。hintEdit）も確かめる。
//
// req: FR-262
func TestRescanAroundMatchesScanLine(t *testing.T) {
	s := newScanner(allFormats())
	r := rand.New(rand.NewPCG(395, 1)) //nolint:gosec // 再現できる無作為な編集の列を作るためで、暗号の用途ではない
	for trial := range 2000 {
		line := randomText(r, 50+r.IntN(1500))
		base := &lineState{spans: s.scanLine(line), scanned: true}
		cur := base
		text := line
		for k := range 1 + r.IntN(4) {
			next, start, oldEnd, newEnd := randomLineEdit(r, text)
			l := &lineState{}
			hintEdit(l, cur, start, oldEnd, newEnd)
			if l.hint == nil {
				t.Fatalf("%d: 手がかりが無い", trial)
			}
			h := l.hint
			got := spansString(s.rescanAround(next, h.base.spans, h.start, h.oldEnd, h.newEnd))
			if want := spansString(s.scanLine(next)); got != want {
				t.Fatalf("%d 回目の %d 番目の編集: rescanAround = %s\nwant %s\n行 %q", trial, k, got, want, next)
			}
			cur, text = l, next
		}
	}
}

// TestLongLineEditsInExtension は、longLineBytes より長い行への乱数の編集（1 回の変更に 1〜3 個）を拡張機能に送り、
// 変更のたびに、送った一覧が行の全体を数えたものと同じになることを確かめる。
//
// req: FR-262
func TestLongLineEditsInExtension(t *testing.T) {
	x := newExtension()
	h := connect(t, x, ext.APIVersion, `{"fileTypes": ["css"], "rgb": true, "hsl": true, "hex": {"aarrggbb": true}}`)
	r := rand.New(rand.NewPCG(395, 2)) //nolint:gosec // 再現できる無作為な編集の列を作るためで、暗号の用途ではない
	line := randomText(r, longLineBytes+5000)
	h.open(1, "css", line)
	s := newScanner(x.cfg.formats)
	v := ext.Version(1)
	for step := range 60 {
		var edits []ext.Edit
		for range 1 + r.IntN(3) {
			next, start, oldEnd, _ := randomLineEdit(r, line)
			edits = append(edits, edit(0, start, 0, oldEnd, next[start:len(next)-(len(line)-oldEnd)]))
			line = next
		}
		h.change(1, v, edits...)
		v++
		var want []string
		for _, sp := range s.scanLine(line) {
			want = append(want, spansString([]span{sp}))
		}
		var got []string
		for _, d := range h.last().Decorations {
			got = append(got, spansString([]span{{start: d.Range.Start.Byte, end: d.Range.End.Byte, rgb: parseHex(t, d.Attr.Bg)}}))
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%d 回目の変更の後の一覧が違う（%d 個と %d 個）", step, len(got), len(want))
		}
	}
}

func parseHex(t *testing.T, s string) uint32 {
	t.Helper()
	var v uint32
	for k := 1; k < len(s); k++ {
		d, ok := hexDigit(s[k])
		if !ok {
			t.Fatalf("色 %q", s)
		}
		v = v<<4 | d
	}
	return v
}

// TestPingWithLongLine は、1 行が 1 MB の対象のファイルを開いた直後と、その行に 1 文字ずつ 20 回続けて入れた後に、
// ping の応答が本体の期限（2 秒）より前に届くことを確かめる。行はメッセージを処理する goroutine の外で数えるので、
// 数える時間は ping の応答を遅らせない（PR #395 のレビューの必須 1）。送り終えた一覧は、最後の版の行を数えたものになる。
//
// req: FR-262
func TestPingWithLongLine(t *testing.T) {
	unit := `function a0(b,c){return b.color==="#fff"?c+0:red(b)};var x0={bg:"#1e1e2e",fg:white};`
	line := strings.Repeat(unit, 1<<20/len(unit))
	x := newExtension()
	h := connect(t, x, ext.APIVersion, `{"fileTypes": ["javascript"]}`)
	start := time.Now()
	if err := h.conn.Notify(ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, FileType: "javascript", Version: 1, Text: line}); err != nil {
		t.Fatal(err)
	}
	if err := h.call(ext.MethodPing, nil, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("開いてから ping の応答まで %v", d)
	}
	start = time.Now()
	for i := range 20 {
		v := ext.Version(1 + i)
		if err := h.conn.Notify(ext.MethodBufferChange, ext.BufferChangeParams{
			Buffer: 1, FromVersion: v, ToVersion: v + 1, Edits: []ext.Edit{edit(0, 0, 0, 0, "x")},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.call(ext.MethodPing, nil, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("20 回の変更の後の ping の応答まで %v", d)
	}
	h.sync()
	p := h.last()
	if p.Version != 21 {
		t.Errorf("最後の一覧の version = %d, want 21", p.Version)
	}
	want := len(newScanner(x.cfg.formats).scanLine(strings.Repeat("x", 20) + line))
	if len(p.Decorations) != want {
		t.Errorf("範囲の数 = %d, want %d", len(p.Decorations), want)
	}
}
