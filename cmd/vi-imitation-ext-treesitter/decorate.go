package main

import (
	"slices"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/piplupflipper/vi-imitation/ext"
)

// capture は、ハイライトのクエリが捕まえた 1 つの範囲である。
type capture struct {
	start, end int // 全文の中のバイト位置
	pattern    uint
	style      styleID
}

// decorations は、構文木にハイライトのクエリを流し、色を付ける範囲の一覧を返す。
//
// 範囲は互いに重ならず、全文の先頭から順に並ぶ。隣り合う同じ色の範囲は 1 つにつなぐ。
// 範囲は行をまたぐことがある（複数行のコメントや raw 文字列）。
//
// 1 つの位置に複数のキャプチャが重なるときは、tree-sitter の公式のハイライト（tree-sitter-highlight）に合わせて次のように決める。
//
//   - 同じ範囲を捕まえたキャプチャが複数あれば、クエリの中で先に書かれたパターンのものを採る。
//     Go のクエリでは、関数の呼び出しの名前は (identifier) @variable より前の @function が採られる。
//     採ったキャプチャが色を付けない名前（styleFor が noStyle を返す）なら、その範囲にはそのキャプチャの色を付けない（外側のキャプチャの色が残る）
//   - 範囲が入れ子になっていれば、内側のキャプチャの色を採る（文字列の中のエスケープなど）
//
// req: FR-35
func (d *document) decorations() []ext.Decoration {
	paint := d.paint(0, len(d.src))
	decs := []ext.Decoration{}
	for i := 0; i < len(paint); {
		j := i + 1
		for j < len(paint) && paint[j] == paint[i] {
			j++
		}
		if paint[i] != noStyle {
			decs = append(decs, ext.Decoration{
				Range: ext.Range{Start: d.position(i), End: d.position(j)},
				Style: paint[i].String(),
			})
		}
		i = j
	}
	return decs
}

// paint は、全文の中のバイト位置の範囲 [from, to) の 1 バイトごとの色を、decorations の doc の規則で決めて返す。
// 返す列の添字 i は、バイト位置 from+i の色である。
// 範囲に掛かるキャプチャだけを使うが、範囲に掛かるキャプチャの重なりの決め方は全文と同じなので、全文で決めた色と同じになる。
// 見出しの帯（sticky.go）の行に、本文と同じ色を付けるのに使う。
func (d *document) paint(from, to int) []styleID {
	caps := d.captures(from, to)
	paint := make([]styleID, max(to-from, 0))
	// 外側の範囲を先に、内側の範囲を後に塗る。
	slices.SortFunc(caps, func(a, b capture) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return b.end - a.end
	})
	for _, c := range caps {
		if c.style == noStyle {
			continue
		}
		for i := max(c.start, from); i < min(c.end, to); i++ {
			paint[i-from] = c.style
		}
	}
	return paint
}

// captures は、ハイライトのクエリのキャプチャのうち、範囲 [from, to) に掛かるものを、同じ範囲については先に書かれたパターンの 1 つだけにして返す。
// 範囲が全文なら、すべてのキャプチャである。幅が 0 の範囲（構文の誤りを補った MISSING のノードなど）は除く。
//
// QueryCursor.Captures ではなく Matches を使うのは、go-tree-sitter v0.25.0 の QueryCaptures.Next が
// 1 つのキャプチャごとに C.malloc した領域を解放しないためである。
func (d *document) captures(from, to int) []capture {
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	if from > 0 || to < len(d.src) {
		qc.SetByteRange(uint(from), uint(to))
	}
	type span struct{ start, end int }
	best := map[span]capture{}
	matches := qc.Matches(d.lang.query, d.tree.RootNode(), d.src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			s := span{int(c.Node.StartByte()), int(c.Node.EndByte())}
			if s.start >= s.end || s.end <= from || s.start >= to {
				continue
			}
			if prev, ok := best[s]; ok && prev.pattern <= m.PatternIndex {
				continue
			}
			best[s] = capture{start: s.start, end: s.end, pattern: m.PatternIndex, style: d.lang.styles[c.Index]}
		}
	}
	caps := make([]capture, 0, len(best))
	for _, c := range best {
		caps = append(caps, c)
	}
	return caps
}
