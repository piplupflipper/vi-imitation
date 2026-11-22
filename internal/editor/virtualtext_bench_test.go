package editor

import (
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 仮想テキストの多いバッファでの、1 文字の入力と、別の拡張機能の一覧の送り直しの時間を測る（PR #378 のレビューの必須 1）。
// 行は "\tv1 := compute(alpha, beta, gamma)" で、1 行にインレイヒントの inline を 2 つ（": int" と "a: "）置く。
// 1 回は、行の中への 1 文字の入力（または一覧の受け取り）と、80 列 40 行の画面を 1 回描くことである。

const vtBenchLine = "\tv1 := compute(alpha, beta, gamma)"

// vtBenchEditor は、n 行のバッファに、拡張機能 lsp のインレイヒントを perLine 個ずつ置いたエディタを返す。
func vtBenchEditor(b *testing.B, n, perLine int) (*Editor, *bufferState) {
	b.Helper()
	bs := &bufferState{id: 1, buf: buffer.New(strings.Repeat(vtBenchLine+"\n", n)), decorations: map[string]decorationSet{}}
	e := &Editor{bufs: []*bufferState{bs}}
	hint := &ext.TextAttr{Fg: "#8b80a8"}
	vts := make([]ext.VirtualText, 0, n*perLine)
	for i := range n {
		for j := range perLine {
			at := []int{3, 15}[j%2]
			vts = append(vts, ext.VirtualText{
				Position: ext.Position{Line: i, Byte: at}, Placement: ext.VirtualTextInline,
				Chunks: []ext.VirtualTextChunk{{Text: []string{": int", "a: "}[j%2], Attr: hint}},
			})
		}
	}
	if perLine > 0 && !e.setVirtualTexts("lsp", ext.VirtualTextSetParams{Buffer: 1, Version: ext.Version(bs.buf.Version()), VirtualTexts: vts}) {
		b.Fatal("仮想テキストを持たない")
	}
	return e, bs
}

// vtBenchRender は、cur の位置にカーソルを置いた画面を描く。
func vtBenchRender(r *ui.Renderer, bs *bufferState, cur buffer.Position) {
	r.Render(&ui.Frame{Text: bs.buf, LineLayout: ui.LineLayout{TabStop: 8, Virtual: bs.virtView}, Cursor: cur, Mode: ui.ModeInsert})
}

// benchVirtualTextInsert は、n 行のバッファの中ほどの行に 1 文字ずつ入れる時間を測る。
func benchVirtualTextInsert(b *testing.B, n, perLine int) {
	_, bs := vtBenchEditor(b, n, perLine)
	r := &ui.Renderer{Width: 80, Height: 40}
	at := buffer.Position{Line: n / 2, Byte: 8}
	vtBenchRender(r, bs, at)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ed := buffer.Edit{Range: buffer.Range{Start: at, End: at}, Text: "x"}
		ch, err := bs.buf.Apply(buffer.Transaction{BaseVersion: bs.buf.Version(), Edits: []buffer.Edit{ed}})
		if err != nil {
			b.Fatal(err)
		}
		bs.shiftAllVirtualTexts(ch.Edits)
		at.Byte++
		vtBenchRender(r, bs, at)
	}
}

func BenchmarkVirtualTextInsertNone30k(b *testing.B) { benchVirtualTextInsert(b, 30000, 0) }
func BenchmarkVirtualTextInsert30k(b *testing.B)     { benchVirtualTextInsert(b, 30000, 2) }
func BenchmarkVirtualTextInsert100k(b *testing.B)    { benchVirtualTextInsert(b, 100000, 3) }

// BenchmarkVirtualTextNewLine30k は、行の数の変わる入力（改行）の時間を測る。後ろの行の仮想テキストの行の番号を足す。
func BenchmarkVirtualTextNewLine30k(b *testing.B) {
	_, bs := vtBenchEditor(b, 30000, 2)
	r := &ui.Renderer{Width: 80, Height: 40}
	at := buffer.Position{Line: 100, Byte: 0}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ed := buffer.Edit{Range: buffer.Range{Start: at, End: at}, Text: "\n"}
		ch, err := bs.buf.Apply(buffer.Transaction{BaseVersion: bs.buf.Version(), Edits: []buffer.Edit{ed}})
		if err != nil {
			b.Fatal(err)
		}
		bs.shiftAllVirtualTexts(ch.Edits)
		at.Line++
		vtBenchRender(r, bs, at)
	}
}

// BenchmarkVirtualTextOtherExtSet30k は、インレイヒント 6 万個のバッファで、別の拡張機能（ブロックの範囲の罫線）が overlay を 20 個
// 送り直す時間を測る。送り直した拡張機能の分だけを作り直す。
func BenchmarkVirtualTextOtherExtSet30k(b *testing.B) {
	e, bs := vtBenchEditor(b, 30000, 2)
	r := &ui.Renderer{Width: 80, Height: 40}
	cur := buffer.Position{Line: 15000}
	vts := make([]ext.VirtualText, 20)
	for i := range vts {
		vts[i] = ext.VirtualText{Position: ext.Position{Line: 14990 + i}, Placement: ext.VirtualTextOverlay, Chunks: []ext.VirtualTextChunk{{Text: "│"}}}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !e.setVirtualTexts("chunk", ext.VirtualTextSetParams{Buffer: 1, Version: ext.Version(bs.buf.Version()), VirtualTexts: vts}) {
			b.Fatal("持たない")
		}
		vtBenchRender(r, bs, cur)
	}
}
