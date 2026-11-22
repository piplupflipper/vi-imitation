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
//   - 容れ物（container）は、名前を持つノード（根を除く）で、次のどれかに当たるものである（isContainer）
//     1. 括弧の容れ物: 最初の子が名前を持たない（予約語か括弧）で、子に開き括弧 { か [ があり、最後の子が対応する閉じ括弧であるもの。
//     Go の block、literal_value、interface_type、switch と select の文、JSON の object、array が当たる。
//     最初の子に名前を持つノード（a[i] の a）は、添え字の式をまとまりにしないために除く
//     2. 本体の容れ物: 親の body か consequence のフィールドの子であるもの（Python の関数や if の本体の block のように、
//     括弧を持たない言語の本体）。フィールドの名前は tree-sitter の文法の慣習で、言語によらない（bodyFields）
//     3. 予約語で閉じる容れ物: 最初の子と最後の子が名前を持たず、最後の子が後の行の最初から始まり（bash の fi、done、esac、Lua の end、
//     複数の行の ( ) の閉じ括弧）、間に行の最初から始まる名前を持つ子があるもの
//   - まとまりの全体は、容れ物から親へ、次のどれかに当たる親へたどった、いちばん外のノードである（joins）。根へは広げない
//     1. 今のノードが親の body、consequence、alternative のフィールドの子である（本体を持つ文や宣言と、else、elif の節。joinFields）
//     2. 親が容れ物でなく、親が今のノードと同じ位置で終わり、今のノードがその行の最初の文字から始まらない（Go の } else { の block）
//     3. 親が容れ物でなく、親が今のノードと同じ行で始まり、今のノードより前から始まるか、今のノードと同じ行で終わる
//     （Go の func の見出し、JSON の "a": の対）。今のノードと同じ位置で始まって後の行へ続く親（今のノードを最初の要素に持つ文の並び）には広げない
//     Go の関数の本体の block は function_declaration に、if と else の本体（Python の elif、else の節も）は if 文に、
//     JSON の "a": {...} の object は pair になる。
//     2 で行の最初から始まるノードを除くのは、字下げで本体を書く言語（Python）では、入れ子の本体がどれも同じ位置で終わるためである
//   - まとまりの中身（in）は、容れ物の中身である（bodySpan）。括弧の容れ物は括弧の内側、予約語で閉じる容れ物は最初と最後の予約語の間の行、
//     本体の容れ物はノードそのものである
//
// 1 つのまとまりが複数の容れ物を持つことがある（if と else の本体、型引数と本体を持つ関数）。

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

// openers は、括弧の容れ物の開き括弧である。( はまとまりにしない（1 行の引数の並びと式の括弧を、まとまりの単位にしないため）。
var openers = map[string]string{"{": "}", "[": "]"}

// bodyFields は、本体の容れ物にするフィールドの名前である。
// tree-sitter の文法は、関数、ループ、クラスなどの本体を body、if の本体を consequence と名づける（Go、Python、C、JavaScript、Rust、Lua の文法）。
var bodyFields = []string{"body", "consequence"}

// joinFields は、子から親へまとまりの全体を広げるフィールドの名前である。bodyFields と、if の else と elif の節の alternative である。
var joinFields = []string{"body", "consequence", "alternative"}

// isRoot は、n が構文木の根のノードかどうかを返す。
func isRoot(n *sitter.Node) bool { return n.Parent() == nil }

// isContainer は、n が容れ物かどうかを返す（この節の初めのコメント）。src は全文である。
func isContainer(n *sitter.Node, src []byte) bool {
	if !n.IsNamed() || n.IsExtra() || isRoot(n) {
		return false
	}
	return isBracketed(n) || inField(n, bodyFields) || isKeywordDelimited(n, src)
}

// isBracketed は、n が括弧の容れ物の形（最初の子が名前を持たず、開き括弧があり、最後の子が対応する閉じ括弧）かどうかを返す。
func isBracketed(n *sitter.Node) bool {
	first, last := firstNonExtraChild(n), lastNonExtraChild(n)
	if first == nil || last == nil || first.IsNamed() || last.IsNamed() || first.Id() == last.Id() {
		return false
	}
	for i := range n.ChildCount() {
		c := n.Child(i)
		if c.IsExtra() || c.IsNamed() {
			continue
		}
		if want, ok := openers[c.Kind()]; ok {
			return last.Kind() == want && c.Id() != last.Id()
		}
	}
	return false
}

// isKeywordDelimited は、n が予約語で閉じる容れ物の形かどうかを返す（この節の初めのコメントの 3）。
func isKeywordDelimited(n *sitter.Node, src []byte) bool {
	first, last := firstNonExtraChild(n), lastNonExtraChild(n)
	if first == nil || last == nil || first.IsNamed() || last.IsNamed() || first.Id() == last.Id() {
		return false
	}
	if last.StartPosition().Row <= n.StartPosition().Row || !startsLine(last, src) {
		return false
	}
	_, _, ok := keywordInner(n, src)
	return ok
}

// keywordInner は、予約語で閉じる容れ物 n の中身の行（最初と最後の行）を返す。
// 中身は、行の最初から始まる最初の名前を持つ子の行から、最後の名前を持つ子の行までである。そのような子が無ければ ok は false である。
func keywordInner(n *sitter.Node, src []byte) (first, last uint, ok bool) {
	cursor := n.Walk()
	defer cursor.Close()
	children := n.NamedChildren(cursor)
	for i := range children {
		c := &children[i]
		if c.IsExtra() {
			continue
		}
		if !ok {
			if !startsLine(c, src) || c.StartPosition().Row == n.StartPosition().Row {
				continue
			}
			first, ok = c.StartPosition().Row, true
		}
		last = c.EndPosition().Row
		if c.EndPosition().Column == 0 && last > c.StartPosition().Row {
			// 改行で終わるノード（bash の文の並び）は、次の行を含めない。
			last--
		}
	}
	return first, last, ok
}

// inField は、n が親の fields のどれかのフィールドの子かどうかを返す。
// 同じフィールドの子が複数ありうる（Python の elif と else の alternative）ので、最初の子が n でなければ、そのフィールドの子をすべて見る。
func inField(n *sitter.Node, fields []string) bool {
	p := n.Parent()
	if p == nil {
		return false
	}
	for _, f := range fields {
		b := p.ChildByFieldName(f)
		if b == nil {
			continue
		}
		if b.Id() == n.Id() {
			return true
		}
		cursor := p.Walk()
		found := slices.ContainsFunc(p.ChildrenByFieldName(f, cursor), func(c sitter.Node) bool { return c.Id() == n.Id() })
		cursor.Close()
		if found {
			return true
		}
	}
	return false
}

// firstNonExtraChild は、n の子のうち、コメントのような extra でない最初のものを返す。無ければ nil である。
func firstNonExtraChild(n *sitter.Node) *sitter.Node {
	for i := range n.ChildCount() {
		if c := n.Child(i); !c.IsExtra() {
			return c
		}
	}
	return nil
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

// joins は、ノード cur から親 p へたどって、まとまりの全体を広げてよいかを返す（この節の初めのコメント）。src は全文である。
// 同じ行で始まる親のうち、cur と同じ位置で始まって cur の後の行へ続く親（Go の statement_list のような、cur を最初の要素に持つ並び）には広げない。
// cur と同じ位置で始まり、cur と同じ行で終わる親（defer func() {...}() の呼び出し）には広げる。
func joins(p, cur *sitter.Node, src []byte) bool {
	if isRoot(p) {
		return false
	}
	if inField(cur, joinFields) {
		return true
	}
	if isContainer(p, src) {
		return false
	}
	if p.EndByte() == cur.EndByte() && !startsLine(cur, src) {
		return true
	}
	if p.StartPosition().Row != cur.StartPosition().Row {
		return false
	}
	return p.StartByte() < cur.StartByte() || p.EndPosition().Row == cur.EndPosition().Row
}

// startsLine は、n がその行の最初の文字（字下げの空白とタブの後）から始まるかどうかを返す。
func startsLine(n *sitter.Node, src []byte) bool {
	start := int(n.StartByte())
	for i := start - 1; i >= 0 && i < len(src); i-- {
		switch src[i] {
		case '\n':
			return true
		case ' ', '\t':
			continue
		}
		return false
	}
	return true
}

// blockNodeOf は、容れ物 c を持つまとまりの全体のノードを返す。
func blockNodeOf(c *sitter.Node, src []byte) sitter.Node {
	cur := *c
	for p := cur.Parent(); p != nil && joins(p, &cur, src); p = cur.Parent() {
		cur = *p
	}
	return cur
}

// blocksAt は、全文 src の構文木 tree で範囲 q を全体に含むまとまりを、内側から外側の順に返す。
// q が空の範囲なら、その位置を含むまとまりである。
//
// 範囲 q を含む最も小さいノードから根の手前までの祖先をたどり、祖先が容れ物ならそれを集め、
// さらに、その祖先を全体とする容れ物（容れ物の前の見出し、if の条件の上にカーソルがあるとき）を集める。
// テキストオブジェクトの an と in のほか、ブロックの範囲の罫線（#373 の後段）が使う。
func blocksAt(tree *sitter.Tree, src []byte, q span) []block {
	root := tree.RootNode()
	leaf := root.DescendantForByteRange(uint(q.start), uint(q.end))
	var blocks []block
	index := map[uintptr]int{}
	add := func(c *sitter.Node) {
		u := blockNodeOf(c, src)
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
		if isContainer(n, src) {
			add(n)
		}
		containersUnder(n, prev, src, add)
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

// containersUnder は、ノード n を全体に含みうる容れ物（n から子へ、joins の逆にたどって着く容れ物）を add に渡す。
// skip は、n の子のうち、祖先として別にたどる子で、たどらない。nil なら全部の子をたどる。src は全文である。
//
// 子の多いノード（文の並び）で全部の子を見ないように、joins の当たりうる子だけを見る。
// n と同じ行で始まる子（子は始まりの順に並ぶので先頭から続く）と、最後の子（n と同じ位置で終わりうる）と、joinFields のフィールドの子である。
func containersUnder(n, skip *sitter.Node, src []byte, add func(*sitter.Node)) {
	var cands []*sitter.Node
	count := n.ChildCount()
	row := n.StartPosition().Row
	for i := uint(0); i < count; i++ {
		c := n.Child(i)
		if c.StartPosition().Row != row {
			break
		}
		cands = append(cands, c)
	}
	if count > 0 {
		cands = append(cands, n.Child(count-1))
	}
	for _, f := range joinFields {
		// alternative は複数ありうる（Python の elif）。フィールドを持たないノードでは、子を数え直さずに済ませる。
		if n.ChildByFieldName(f) == nil {
			continue
		}
		cursor := n.Walk()
		fields := n.ChildrenByFieldName(f, cursor)
		for i := range fields {
			cands = append(cands, &fields[i])
		}
		cursor.Close()
	}
	seen := map[uintptr]bool{}
	for _, c := range cands {
		if seen[c.Id()] || c.IsExtra() || skip != nil && c.Id() == skip.Id() || !joins(n, c, src) {
			continue
		}
		seen[c.Id()] = true
		if isContainer(c, src) {
			add(c)
			continue
		}
		containersUnder(c, nil, src, add)
	}
}
