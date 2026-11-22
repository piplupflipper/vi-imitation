package main

import (
	"slices"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// 構文のまとまり（block）は、中に文や要素を持つ構文の単位である（関数、if、for、オブジェクトのリテラルなど。基本設計 6.1、#373）。
// テキストオブジェクトの an（全体）と in（中身）が選ぶ単位で、後で作るブロックの範囲の罫線が描く単位と同じにする。
//
// まとまりは、言語ごとのクエリや構文の種類の名前の表を使わず、構文木の形だけで決める。
// 同梱の言語（Go、JSON）と、利用者が設定で足した言語に、同じ規則で当てる。
//
//   - 容れ物（container）は、名前を持つノードで、次のどちらかに当たるものである。根のノードは容れ物にしない
//     1. 括弧の容れ物: 子に開き括弧 { か [ があり、その前の子がどれも名前を持たない（予約語だけ）で、
//     最後の子が対応する閉じ括弧であるもの（Go の block、literal_value、interface_type、JSON の object、array）
//     2. 本体の容れ物: 親の body のフィールドの子であるもの（Python の関数の本体の block のように、括弧を持たない言語の本体）
//   - まとまりの全体は、容れ物から親へ、親が容れ物でも根でもなく、親が今のノードと同じ位置で終わるか、
//     同じ行で始まる間たどった、いちばん外のノードである（joins）。
//     ただし、今のノードと同じ位置で始まって後の行へ続く親（今のノードを最初の要素に持つ文の並び）には広げない。
//     Go の関数の本体の block は function_declaration に、if の両方の block は if_statement に、
//     JSON の "a": {...} の object は pair になる
//   - まとまりの中身（in）は、容れ物の中身である。括弧の容れ物は括弧の内側（innerSpan）、本体の容れ物はノードそのものである
//
// 1 つのまとまりが複数の容れ物を持つことがある（if と else の 2 つの block、型引数と本体を持つ関数）。

// block は、構文のまとまりの 1 つである。
type block struct {
	// node は、まとまりの全体のノードである。
	node sitter.Node
	// bodies は、まとまりの容れ物で、始まりの位置の順に並ぶ。少なくとも 1 つある。
	bodies []sitter.Node
}

// outer は、まとまりの全体の範囲である。
func (b block) outer() span { return nodeSpan(&b.node) }

func nodeSpan(n *sitter.Node) span { return span{int(n.StartByte()), int(n.EndByte())} }

// openers は、括弧の容れ物の開き括弧である。( はまとまりにしない（引数の並びと式の括弧を、まとまりの単位にしないため）。
var openers = map[string]string{"{": "}", "[": "]"}

// isRoot は、n が構文木の根のノードかどうかを返す。
func isRoot(n *sitter.Node) bool { return n.Parent() == nil }

// isContainer は、n が容れ物かどうかを返す（この節の初めのコメント）。
func isContainer(n *sitter.Node) bool {
	if !n.IsNamed() || n.IsExtra() || isRoot(n) {
		return false
	}
	return isBracketed(n) || isBodyField(n)
}

// isBracketed は、n が括弧の容れ物の形（開き括弧の前が名前を持たない子だけで、最後の子が対応する閉じ括弧）かどうかを返す。
func isBracketed(n *sitter.Node) bool {
	count := n.ChildCount()
	if count < 2 {
		return false
	}
	last := lastNonExtraChild(n)
	if last == nil || last.IsNamed() {
		return false
	}
	for i := range count {
		c := n.Child(i)
		if c.IsExtra() {
			continue
		}
		if c.IsNamed() {
			return false
		}
		if want, ok := openers[c.Kind()]; ok {
			return last.Kind() == want && c.Id() != last.Id()
		}
	}
	return false
}

// isBodyField は、n が親の body のフィールドの子かどうかを返す。
func isBodyField(n *sitter.Node) bool {
	p := n.Parent()
	if p == nil {
		return false
	}
	b := p.ChildByFieldName("body")
	return b != nil && b.Id() == n.Id()
}

// lastNonExtraChild は、n の子のうち、コメントのような extra でない最後のものを返す。無ければ nil である。
func lastNonExtraChild(n *sitter.Node) *sitter.Node {
	for i := n.ChildCount(); i > 0; i-- {
		if c := n.Child(i - 1); !c.IsExtra() {
			return c
		}
	}
	return nil
}

// joins は、ノード cur から親 p へたどって、まとまりの全体を広げてよいかを返す（この節の初めのコメント）。
// 同じ行で始まる親のうち、cur と同じ位置で始まって cur の後の行へ続く親（Go の statement_list のような、cur を最初の要素に持つ並び）には広げない。
// cur と同じ位置で始まり、cur と同じ行で終わる親（defer func() {...}() の呼び出し）には広げる。
func joins(p, cur *sitter.Node) bool {
	if isRoot(p) || isContainer(p) {
		return false
	}
	if p.EndByte() == cur.EndByte() {
		return true
	}
	if p.StartPosition().Row != cur.StartPosition().Row {
		return false
	}
	return p.StartByte() < cur.StartByte() || p.EndPosition().Row == cur.EndPosition().Row
}

// blockNodeOf は、容れ物 c を持つまとまりの全体のノードを返す。
func blockNodeOf(c *sitter.Node) sitter.Node {
	cur := *c
	for p := cur.Parent(); p != nil && joins(p, &cur); p = cur.Parent() {
		cur = *p
	}
	return cur
}

// blocksAt は、構文木 tree で範囲 q を全体に含むまとまりを、内側から外側の順に返す。
// q が空の範囲なら、その位置を含むまとまりである。
//
// 範囲 q を含む最も小さいノードから根の手前までの祖先をたどり、祖先が容れ物ならそれを、
// 容れ物でなければ、その祖先を全体とする容れ物（容れ物の前の見出し、if の条件の上にカーソルがあるとき）を集める。
// テキストオブジェクトの an と in のほか、ブロックの範囲の罫線（#373 の後段）が使う。
func blocksAt(tree *sitter.Tree, q span) []block {
	root := tree.RootNode()
	leaf := root.DescendantForByteRange(uint(q.start), uint(q.end))
	var blocks []block
	index := map[uintptr]int{}
	add := func(c *sitter.Node) {
		u := blockNodeOf(c)
		if s := nodeSpan(&u); s.start > q.start || s.end < q.end {
			return
		}
		i, ok := index[u.Id()]
		if !ok {
			i = len(blocks)
			index[u.Id()] = i
			blocks = append(blocks, block{node: u})
		}
		if !slices.ContainsFunc(blocks[i].bodies, func(b sitter.Node) bool { return b.Id() == c.Id() }) {
			blocks[i].bodies = append(blocks[i].bodies, *c)
		}
	}
	var prev *sitter.Node
	for n := leaf; n != nil && !isRoot(n); prev, n = n, n.Parent() {
		if isContainer(n) {
			add(n)
			continue
		}
		containersUnder(n, prev, add)
	}
	for i := range blocks {
		slices.SortFunc(blocks[i].bodies, func(a, b sitter.Node) int { return int(a.StartByte()) - int(b.StartByte()) })
	}
	slices.SortFunc(blocks, func(a, b block) int {
		as, bs := a.outer(), b.outer()
		return (as.end - as.start) - (bs.end - bs.start)
	})
	return blocks
}

// containersUnder は、容れ物でないノード n を全体に含みうる容れ物（n から子へ、joins の逆にたどって着く容れ物）を add に渡す。
// skip は、n の子のうち、祖先として別にたどる子で、たどらない。nil なら全部の子をたどる。
func containersUnder(n, skip *sitter.Node, add func(*sitter.Node)) {
	count := n.ChildCount()
	row := n.StartPosition().Row
	for i := uint(0); i < count; i++ {
		c := n.Child(i)
		if c.StartPosition().Row != row && i < count-1 {
			// 子は始まりの順に並ぶので、ここから最後の子の前までは n と同じ行で始まらない。
			// 最後の子だけが、n と同じ位置で終わりうる。
			i = count - 2
			continue
		}
		if c.IsExtra() || skip != nil && c.Id() == skip.Id() || !joins(n, c) {
			continue
		}
		if isContainer(c) {
			add(c)
			continue
		}
		containersUnder(c, nil, add)
	}
}
