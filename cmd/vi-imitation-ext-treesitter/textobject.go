package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// objectKind は、構文に沿ったテキストオブジェクトの対象（構文のまとまり、引数）である。
// 引数は、テキストオブジェクトのクエリ（queries/<言語>/textobjects.scm）のキャプチャ名の前半と同じ名前を持つ。
type objectKind string

const (
	// objectBlock は、構文のまとまり（an、in。block.go）である。クエリを使わず、構文木の形で決める。
	objectBlock     objectKind = "block"
	objectParameter objectKind = "parameter"
)

// objectKinds は、テキストオブジェクトのクエリで決める対象の一覧である。
var objectKinds = []objectKind{objectParameter}

// textObjectSpec は、宣言ファイルの contributes.textObjects の 1 つの項目に対応する、テキストオブジェクトの定義である。
type textObjectSpec struct {
	// id は、宣言ファイルの contributes.textObjects の id である。
	id   string
	kind objectKind
	// around は、全体（aX）なら true、内側（iX）なら false である。
	around bool
}

// textObjectSpecs は、この拡張機能が答えるテキストオブジェクトの一覧である。宣言ファイル（manifest.json）と一致させる。
var textObjectSpecs = []textObjectSpec{
	{id: "treesitter.block.around", kind: objectBlock, around: true},
	{id: "treesitter.block.inner", kind: objectBlock},
	{id: "treesitter.parameter.around", kind: objectParameter, around: true},
	{id: "treesitter.parameter.inner", kind: objectParameter},
}

// objectPart は、テキストオブジェクトのクエリのキャプチャ名の後半（outer か inner）である。
type objectPart uint8

const (
	partNone objectPart = iota // 対象のキャプチャではない
	partOuter
	partInner
)

// objectCapture は、テキストオブジェクトのクエリの 1 つのキャプチャ名の意味である。
type objectCapture struct {
	kind objectKind
	part objectPart
}

// parseObjectCapture は、"function.outer" のようなキャプチャ名を対象と部分に分ける。
// 対象が objectKinds に無いか、部分が outer と inner のどちらでもない名前は、part が partNone の値を返す。
func parseObjectCapture(name string) objectCapture {
	kind, part, ok := strings.Cut(name, ".")
	if !ok || !slices.Contains(objectKinds, objectKind(kind)) {
		return objectCapture{}
	}
	switch part {
	case "outer":
		return objectCapture{kind: objectKind(kind), part: partOuter}
	case "inner":
		return objectCapture{kind: objectKind(kind), part: partInner}
	}
	return objectCapture{}
}

// treeStore は、解析の goroutine が最後に解析した構文木の複製を、バッファごとに持つ。
// テキストオブジェクトの問い合わせは、問い合わせのバージョン番号の構文木があればそれを使い、全文から解析し直さずに済ませる。
//
// tree-sitter の構文木は、複数の goroutine から同時に使えない。
// 解析の goroutine は自分の構文木の Clone を put で渡し、問い合わせの側は take でさらに Clone を受け取る。
// Clone は内部の節点を共有する浅い写しで、速い（tree-sitter の ts_tree_copy）。
type treeStore struct {
	mu    sync.Mutex
	trees map[ext.BufferID]storedTree
}

type storedTree struct {
	version ext.Version
	lang    *language
	tree    *sitter.Tree
	// src と lineStarts は、tree を作ったもとの全文と行の先頭の表である（document の同じ名前の項目）。
	// document は編集のたびに新しいスライスを作り、作ったものを書き換えないので、写さずに共有する。
	src        []byte
	lineStarts []int
}

func newTreeStore() *treeStore {
	return &treeStore{trees: map[ext.BufferID]storedTree{}}
}

// put は、バッファ id のバージョン version の構文木として、d の構文木の複製と全文を持つ。
func (s *treeStore) put(id ext.BufferID, version ext.Version, d *document) {
	tree := d.tree.Clone()
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.trees[id]; ok {
		old.tree.Close()
	}
	s.trees[id] = storedTree{version: version, lang: d.lang, tree: tree, src: d.src, lineStarts: d.lineStarts}
}

// latest は、バッファ id の最後に解析した構文木の複製と全文を document にして、そのバージョン番号と一緒に返す。
// 無ければ nil である。返した document は、呼び出し側が close する。
// 見出しの帯（sticky.go）が、窓の知らせのバージョン番号を待たずに、手元の最新の構文木で帯を作るのに使う。
func (s *treeStore) latest(id ext.BufferID) (*document, ext.Version) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.trees[id]
	if !ok {
		return nil, 0
	}
	return &document{lang: t.lang, tree: t.tree.Clone(), src: t.src, lineStarts: t.lineStarts}, t.version
}

// take は、バッファ id のバージョン version を lang で解析した構文木があれば、その複製を返す。無ければ nil である。
// 返した構文木は、呼び出し側が Close する。
func (s *treeStore) take(id ext.BufferID, version ext.Version, lang *language) *sitter.Tree {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.trees[id]
	if !ok || t.version != version || t.lang != lang {
		return nil
	}
	return t.tree.Clone()
}

// drop は、バッファ id の構文木を捨てる。
func (s *treeStore) drop(id ext.BufferID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.trees[id]; ok {
		old.tree.Close()
		delete(s.trees, id)
	}
}

// close は、すべての構文木を捨てる。
func (s *treeStore) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.trees {
		t.tree.Close()
	}
	clear(s.trees)
}

// textObjects は、textObject/resolve に答える。
//
// 構文木を全文から解析し直すことがあり、時間がかかるので、ループ（Server がメッセージを処理する goroutine）の上では行わない。
// ハンドラーは、バッファを Clone して問い合わせごとの goroutine に渡し、速やかに戻る（TextObjectAsync）。
// 問い合わせの goroutine は mu を取ってから処理するので、同時に処理する問い合わせは 1 つである。
// 本体はキー入力ごとに結果を待つので、問い合わせが重なることはほぼ無い。
type textObjects struct {
	langs map[string]*language // highlighter.langs と同じ表。close の後は読まない
	// highlighted は、ファイルの種類の色付けに使っている言語（利用者の設定のパーサが用意できていればそれ、無ければ同梱の言語）を返す。
	// 用意を待っているときは nil を返す。構文のまとまり（an、in）は、クエリを使わないので、利用者の設定のパーサでも使える。
	// 引数（aa、ia）は、同梱のクエリが同梱の文法に合わせて書いてあるので、langs の同梱の言語を使う。
	// highlighter.mu を取るので、highlighter.mu を持ったまま textObjects.mu を取らない（今は、そうする箇所は無い）。
	highlighted func(ft string) *language
	trees       *treeStore

	mu     sync.Mutex
	closed bool
	// parsers は、構文木が treeStore に無いときに全文から解析するためのパーサーで、言語ごとに 1 つ持つ。
	// highlighter のパーサーは解析の goroutine が使うので、別に持つ。mu で守る。
	parsers map[*language]*sitter.Parser
}

// errClosed は、拡張機能が終わる途中に届いた問い合わせの失敗の理由である。
var errClosed = errors.New("tree-sitter extension is shutting down")

func newTextObjects(langs map[string]*language, trees *treeStore, highlighted func(ft string) *language) *textObjects {
	return &textObjects{langs: langs, highlighted: highlighted, trees: trees, parsers: map[*language]*sitter.Parser{}}
}

// register は、textObjectSpecs のすべてのテキストオブジェクトのハンドラーを s に登録する。
func (o *textObjects) register(s *server.Server) {
	for _, spec := range textObjectSpecs {
		s.TextObjectAsync(spec.id, func(ctx context.Context, b *server.Buffer, p *ext.TextObjectResolveParams, reply func(ext.TextObjectResolveResult, error)) {
			b = b.Clone()
			go func() {
				if err := ctx.Err(); err != nil {
					reply(ext.TextObjectResolveResult{}, err)
					return
				}
				reply(o.resolve(spec, b, p))
			}()
		})
	}
}

// close は、パーサーを解放する。この後に処理を始める問い合わせは errClosed で失敗する。
func (o *textObjects) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	for _, p := range o.parsers {
		p.Close()
	}
	clear(o.parsers)
}

// resolve は、バッファ b のカーソルの位置にある、spec のテキストオブジェクトの範囲を返す。
// 文法を持たない種類のファイルと、カーソルを含む対象が無いときは、OK が false の結果を返す。
// 引数（aa、ia）は、テキストオブジェクトのクエリを持たない種類のファイルでも OK が false である。
// 構文のまとまり（an、in）は、選択（p.Selection）があれば、選択より真に大きい範囲を返し、選択を置き換えさせる（blockObject）。
func (o *textObjects) resolve(spec textObjectSpec, b *server.Buffer, p *ext.TextObjectResolveParams) (ext.TextObjectResolveResult, error) {
	// close の後は、highlighter.close が langs を空にしてクエリを解放するので、mu を取って closed を見てから触る。
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return ext.TextObjectResolveResult{}, errClosed
	}
	lang := o.langs[b.FileType()]
	if spec.kind == objectBlock {
		if l := o.highlighted(b.FileType()); l != nil {
			lang = l
		}
	}
	if lang == nil || spec.kind != objectBlock && lang.objects == nil {
		return ext.TextObjectResolveResult{}, nil
	}
	src := []byte(b.Text())
	tree := o.trees.take(b.ID(), b.Version(), lang)
	if tree == nil {
		parser, err := o.parser(lang)
		if err != nil {
			return ext.TextObjectResolveResult{}, err
		}
		tree = parser.Parse(src, nil)
	}
	defer tree.Close()
	d := &document{lang: lang, tree: tree, src: src, lineStarts: lineStartsOf(src)}
	if spec.kind == objectBlock {
		r, linewise, ok, err := d.blockObject(spec.around, p.Cursor, p.Selection, max(p.Count, 1))
		if err != nil || !ok {
			return ext.TextObjectResolveResult{}, err
		}
		return ext.TextObjectResolveResult{OK: true, Range: r, Linewise: linewise, ReplaceSelection: p.Selection != nil}, nil
	}
	r, linewise, ok, err := d.textObject(spec, p.Cursor, max(p.Count, 1))
	if err != nil || !ok {
		return ext.TextObjectResolveResult{}, err
	}
	return ext.TextObjectResolveResult{OK: true, Range: r, Linewise: linewise}, nil
}

// blockObject は、構文のまとまり（block.go）のテキストオブジェクトの範囲を返す。around なら全体（an）、そうでなければ中身（in）である。
// 該当が無ければ ok は false である。
//
// 全体は、まとまりの全体のノードの範囲である。後ろに区切りの "," があればそれまで、無く前にあれば前の要素の直後からを含める
// （aa と同じ。JSON とテーブル駆動のテストの要素を dan で消したときに "," を残さない。withSeparator）。
// 中身は、まとまりの容れ物の中身（bodySpan）のうち、条件に当たる最も小さいものである。
//
// 選択 sel が無ければ（オペレーターの後と、v を打った直後）、範囲がカーソルを含むものを、まとまりの内側から数えて count 番目に採る。
// 中身がカーソルを含まないまとまり（見出しの上のカーソル、容れ物として拾えない節の中のカーソル）は数えない。
// カーソルから離れた本体を選ばないためである（PR #377 のレビューの必須 2）。
// 中身は、カーソルが容れ物の後ろにあるまとまり（onBlock）でも数えない。全体（an）はこの判定をせず、
// 容れ物として拾えない節（Python の except）の中でも、カーソルを全体に含むまとまり（try の文）を選ぶ（PR #377 の再レビューの必須）。
//
// 選択 sel があれば（ビジュアルモードで繰り返したとき）、範囲が選択を含み、選択と同じでないものを、内側から数えて count 番目に採る。
// 範囲と選択とカーソルは、行単位で返す範囲なら行の全体に広げてから比べる（lineSpan）。
//
// 範囲が行の全体を占めるときは行単位にする（textObject と同じ）。
//
// req: FR-11, FR-240
func (d *document) blockObject(around bool, cursor ext.Position, sel *ext.Range, count int) (r ext.Range, linewise, ok bool, err error) {
	off, err := d.offset(cursor)
	if err != nil {
		return ext.Range{}, false, false, err
	}
	q := span{off, min(off+1, len(d.src))}
	fits := func(e span) bool { return e.start <= off && off < e.end }
	if sel != nil {
		ss, err := d.spanOf(*sel)
		if err != nil {
			return ext.Range{}, false, false, err
		}
		q = d.trimSpace(ss)
		fits = func(e span) bool { return e.start <= ss.start && ss.end <= e.end && e != ss }
	}
	var s span
	var last span
	n := 0
	for _, b := range blocksAt(d.tree, d.src, q) {
		if !around && !d.onBlock(b, q.start) {
			continue
		}
		var cands []span
		if around {
			cands = []span{d.withSeparator(&b.node)}
		} else {
			for i := range b.bodies {
				cands = append(cands, d.bodySpan(&b.bodies[i]))
			}
			slices.SortFunc(cands, func(a, b span) int { return (a.end - a.start) - (b.end - b.start) })
		}
		for _, c := range cands {
			e := d.lineSpan(c)
			if !fits(e) || n > 0 && e == last {
				continue
			}
			last = e
			if n++; n == count {
				s, ok = c, true
				break
			}
		}
		if ok {
			break
		}
	}
	if !ok {
		return ext.Range{}, false, false, nil
	}
	if first, last, whole := d.wholeLines(s); whole {
		return ext.Range{Start: ext.Position{Line: first}, End: ext.Position{Line: last, Byte: d.lineLen(last)}}, true, true, nil
	}
	return ext.Range{Start: d.position(s.start), End: d.position(s.end)}, false, true, nil
}

// onBlock は、位置 off がまとまり b の容れ物のどれかの中か、最後の容れ物より前（見出し）にあるかを返す。
// 容れ物の後ろ（bash の elif の [ ... ] の条件の後の本体のような、容れ物として拾えない部分）にあるなら false で、そのまとまりを数えない。
func (d *document) onBlock(b block, off int) bool {
	for i := range b.bodies {
		if nodeSpan(&b.bodies[i]).contains(off) {
			return true
		}
	}
	return off < int(b.bodies[len(b.bodies)-1].StartByte())
}

// trimSpace は、範囲 s の前後の空白と改行を除いた範囲を返す。空白だけなら s を返す。
// 選択からまとまりを探すノードを決めるときに使う（行単位の選択は字下げを含むので、そのままでは外側のノードになってしまう）。
func (d *document) trimSpace(s span) span {
	q := s
	for q.start < q.end && isSpaceByte(d.src[q.start]) {
		q.start++
	}
	for q.end > q.start && isSpaceByte(d.src[q.end-1]) {
		q.end--
	}
	if q.start == q.end {
		return s
	}
	return q
}

// withSeparator は、まとまりの全体のノード n の範囲に、区切りの "," を足した範囲を返す。
// 後ろの兄弟が "," ならそれまでを、そうでなく前の兄弟が "," なら、その前の要素の直後からを含める（引数の aa の listItemSpan と同じ向き）。
// 後ろの "," の後の次の要素までは含めないので、要素が行の全体を占めれば行単位になる。
func (d *document) withSeparator(n *sitter.Node) span {
	s := nodeSpan(n)
	if next := nextNonExtra(n); next != nil && next.Kind() == "," && !next.IsNamed() {
		s.end = int(next.EndByte())
		return s
	}
	if prev := prevNonExtra(n); prev != nil && prev.Kind() == "," && !prev.IsNamed() {
		if before := prevNonExtra(prev); before != nil {
			s.start = int(before.EndByte())
		} else {
			s.start = int(prev.StartByte())
		}
	}
	return s
}

// bodySpan は、容れ物 c の中身の範囲を返す（block.go の初めのコメント）。
// 括弧の容れ物なら括弧の内側（innerSpan）、予約語で閉じる容れ物なら最初の予約語の後から最初の節の前までの行、節ならその本体の行（ownLineChildren）、
// 本体の容れ物ならノードそのものである。
func (d *document) bodySpan(c *sitter.Node) span {
	if isBracketed(c) {
		return d.innerSpan(c)
	}
	keyword, clause := isKeywordDelimited(c, d.src), isClause(c, d.src)
	if keyword || clause {
		first, last, ok := ownLineChildren(c, d.src, keyword)
		if !ok {
			// 最初の枝の本体が無い（if の直後に elif がある）。カーソルを含まない幅 0 の範囲にする。
			return span{int(c.StartByte()), int(c.StartByte())}
		}
		return span{d.lineStarts[first], d.lineStarts[last] + d.lineLen(int(last))}
	}
	return nodeSpan(c)
}

// lineSpan は、範囲 s が行の全体を占めるなら最初の行の先頭から最後の行の行末までに広げた範囲を、そうでなければ s を返す。
// 行単位で返す範囲を、ビジュアルモードの選択（ext の TextObjectResolveParams.Selection）と比べるために使う。
func (d *document) lineSpan(s span) span {
	if first, last, whole := d.wholeLines(s); whole {
		return span{d.lineStarts[first], d.lineStarts[last] + d.lineLen(last)}
	}
	return s
}

// spanOf は、範囲 r を全文の中のバイト位置の半開区間にする。
func (d *document) spanOf(r ext.Range) (span, error) {
	start, err := d.offset(r.Start)
	if err != nil {
		return span{}, err
	}
	end, err := d.offset(r.End)
	if err != nil {
		return span{}, err
	}
	if end < start {
		return span{}, errors.New("selection ends before it starts")
	}
	return span{start, end}, nil
}

// parser は、lang の文法のパーサーを返す。無ければ作る。mu を取って呼ぶ。
func (o *textObjects) parser(lang *language) (*sitter.Parser, error) {
	if p, ok := o.parsers[lang]; ok {
		return p, nil
	}
	p := sitter.NewParser()
	if err := p.SetLanguage(lang.grammar); err != nil {
		p.Close()
		return nil, err
	}
	o.parsers[lang] = p
	return p, nil
}

// span は、全文の中のバイト位置の半開区間 [start, end) である。
type span struct{ start, end int }

func (s span) contains(off int) bool { return s.start <= off && off < s.end }

// candidate は、カーソルを含む 1 つの対象である。
type candidate struct {
	outer span
	// inner は、内側のもとになるノードである。内側を持たない対象（本体の無い関数の宣言）では nil である。
	inner   *sitter.Node
	pattern uint
}

// textObject は、カーソル cursor を含む kind の対象（クエリで決める対象。いまは引数だけ）のうち、
// 内側から数えて count 番目のものの範囲を返す。該当が無ければ ok は false である。
//
// 範囲の決め方は次のとおりである（基本設計 6.1）。
//
//   - 全体（aX）は、@X.outer のノードの範囲である。@X.outer を持たないパターン（引数）は、@X.inner のノードと、
//     後ろの "," から次の要素の直前まで（無ければ前の要素の直後から前の "," まで）を合わせた範囲である
//   - 内側（iX）は、@X.inner のノードが括弧（{ ( [）で始まり対応する閉じ括弧で終われば括弧の内側、そうでなければノードそのものである。
//     括弧の内側の前後の空白と改行は除く
//   - 範囲が行の全体を占める（範囲の前と後ろに空白しか無い）ときは、行単位（Linewise）にする。
//     複数の行の並びの最後の引数を daa で消すと、行が残らない
//
// カーソルを含むかどうかは、全体の範囲で見る。同じ全体を持つ候補が複数あればクエリで先に書いたパターンを、
// 同じ内側のノードを持つ候補が複数あれば全体の大きい方を採る。
//
// req: FR-11, FR-241
func (d *document) textObject(spec textObjectSpec, cursor ext.Position, count int) (r ext.Range, linewise, ok bool, err error) {
	off, err := d.offset(cursor)
	if err != nil {
		return ext.Range{}, false, false, err
	}
	cands := d.candidates(spec.kind, off)
	if count > len(cands) {
		return ext.Range{}, false, false, nil
	}
	c := cands[count-1]
	var s span
	if spec.around {
		s = c.outer
	} else {
		if c.inner == nil {
			return ext.Range{}, false, false, nil
		}
		s = d.innerSpan(c.inner)
	}
	if first, last, whole := d.wholeLines(s); whole {
		return ext.Range{Start: ext.Position{Line: first}, End: ext.Position{Line: last, Byte: d.lineLen(last)}}, true, true, nil
	}
	return ext.Range{Start: d.position(s.start), End: d.position(s.end)}, false, true, nil
}

// candidates は、バイト位置 off を全体に含む kind の対象を、全体の小さい順に返す（textObject の doc の重複の扱いをした後のもの）。
func (d *document) candidates(kind objectKind, off int) []candidate {
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	// 引数の全体は区切りの分だけノードより広く、カーソルが隣の行の要素の全体に入ることがあるので、カーソルの前後の行に掛かるノードを探す。
	line := d.position(off).Line
	from := d.lineStarts[max(line-1, 0)]
	to := len(d.src)
	if line+2 < len(d.lineStarts) {
		to = d.lineStarts[line+2]
	}
	qc.SetByteRange(uint(from), uint(max(to, from+1)))
	byOuter := map[span]candidate{}
	matches := qc.Matches(d.lang.objects, d.tree.RootNode(), d.src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		// m.Captures は次の Next で上書きされるので、ノードを写して持つ。
		var outer, inner *sitter.Node
		for _, c := range m.Captures {
			oc := d.lang.objectCaptures[c.Index]
			if oc.kind != kind {
				continue
			}
			n := c.Node
			switch oc.part {
			case partOuter:
				outer = &n
			case partInner:
				inner = &n
			case partNone:
			}
		}
		var s span
		switch {
		case outer != nil:
			s = span{int(outer.StartByte()), int(outer.EndByte())}
		case inner != nil && !inner.IsExtra():
			s = listItemSpan(inner)
		default:
			continue
		}
		if !s.contains(off) {
			continue
		}
		if prev, ok := byOuter[s]; ok && prev.pattern <= m.PatternIndex {
			continue
		}
		byOuter[s] = candidate{outer: s, inner: inner, pattern: m.PatternIndex}
	}
	// 同じ内側のノードを持つ候補は、全体の大きい方だけを残す。
	type innerKey struct {
		s    span
		kind string
	}
	byInner := map[innerKey]candidate{}
	var cands []candidate
	for _, c := range byOuter {
		if c.inner == nil {
			cands = append(cands, c)
			continue
		}
		k := innerKey{span{int(c.inner.StartByte()), int(c.inner.EndByte())}, c.inner.Kind()}
		if prev, ok := byInner[k]; ok && prev.outer.end-prev.outer.start >= c.outer.end-c.outer.start {
			continue
		}
		byInner[k] = c
	}
	for _, c := range byInner {
		cands = append(cands, c)
	}
	slices.SortFunc(cands, func(a, b candidate) int {
		if n := (a.outer.end - a.outer.start) - (b.outer.end - b.outer.start); n != 0 {
			return n
		}
		return a.outer.start - b.outer.start
	})
	return cands
}

// listItemSpan は、"," で区切った並び（引数の並び）の要素 n の全体を返す。
// 後ろに "," があり、その後に次の要素があれば、次の要素の直前までを含める（f(a, b) の a で "a, "）。
// 後ろに "," があり次の要素が無ければ（複数行の並びの最後の要素の後ろの ","）、その "," までを含める。
// 後ろに "," が無く前に "," があれば、前の要素の直後からを含める（f(a, b) の b で ", b"）。
func listItemSpan(n *sitter.Node) span {
	s := span{int(n.StartByte()), int(n.EndByte())}
	if next := nextNonExtra(n); next != nil && next.Kind() == "," {
		if after := nextNonExtra(next); after != nil && after.IsNamed() {
			s.end = int(after.StartByte())
		} else {
			s.end = int(next.EndByte())
		}
		return s
	}
	if prev := prevNonExtra(n); prev != nil && prev.Kind() == "," {
		if before := prevNonExtra(prev); before != nil {
			s.start = int(before.EndByte())
		} else {
			s.start = int(prev.StartByte())
		}
	}
	return s
}

// nextNonExtra は、n の後ろの兄弟のうち、コメントのような extra でない最初のものを返す。無ければ nil である。
func nextNonExtra(n *sitter.Node) *sitter.Node {
	for s := n.NextSibling(); s != nil; s = s.NextSibling() {
		if !s.IsExtra() {
			return s
		}
	}
	return nil
}

// prevNonExtra は、n の前の兄弟のうち、extra でない最後のものを返す。無ければ nil である。
func prevNonExtra(n *sitter.Node) *sitter.Node {
	for s := n.PrevSibling(); s != nil; s = s.PrevSibling() {
		if !s.IsExtra() {
			return s
		}
	}
	return nil
}

// closers は、開き括弧から閉じ括弧への対応である。
var closers = map[string]string{"{": "}", "(": ")", "[": "]"}

// innerSpan は、内側のもとになるノード n の内側の範囲を返す（textObject の doc）。
// n の子に開き括弧があり、最後の子が対応する閉じ括弧なら、その間から前後の空白と改行を除いた範囲である。
// 間が空白だけなら、開き括弧の直後の幅 0 の範囲である。
// ただし開き括弧が行の最後にあり、閉じ括弧が行の最初にあって、間に行があれば、間の行の全体である（wholeLines が行単位にする）。
func (d *document) innerSpan(n *sitter.Node) span {
	count := n.ChildCount()
	if count < 2 {
		return span{int(n.StartByte()), int(n.EndByte())}
	}
	last := n.Child(count - 1)
	var open *sitter.Node
	for i := range count - 1 {
		c := n.Child(i)
		if want, ok := closers[c.Kind()]; ok && !c.IsNamed() && last.Kind() == want {
			open = c
			break
		}
	}
	if open == nil {
		return span{int(n.StartByte()), int(n.EndByte())}
	}
	start, end := int(open.EndByte()), int(last.StartByte())
	openLine, closeLine := d.position(start).Line, d.position(end).Line
	if closeLine-openLine >= 2 && isBlankBytes(d.src[start:d.lineStarts[openLine+1]]) && isBlankBytes(d.src[d.lineStarts[closeLine]:end]) {
		// 間の行の全体。最後の行の改行は含めない。
		return span{d.lineStarts[openLine+1], d.lineStarts[closeLine] - 1}
	}
	for start < end && isSpaceByte(d.src[start]) {
		start++
	}
	for end > start && isSpaceByte(d.src[end-1]) {
		end--
	}
	return span{start, end}
}

// wholeLines は、範囲 s が行の全体を占めるかどうかと、占めるならその最初と最後の行を返す。
// 範囲の前（行の先頭から）と後ろ（行の終わりまで）が空白とタブだけなら、行の全体を占めるとする。空の範囲は占めない。
func (d *document) wholeLines(s span) (first, last int, whole bool) {
	if s.start >= s.end {
		return 0, 0, false
	}
	first, last = d.position(s.start).Line, d.position(s.end).Line
	lineEnd := d.lineStarts[last] + d.lineLen(last)
	if !isBlankBytes(d.src[d.lineStarts[first]:s.start]) || !isBlankBytes(d.src[s.end:lineEnd]) {
		return 0, 0, false
	}
	return first, last, true
}

// isBlankBytes は、b が空白とタブだけからなるかどうかを返す。改行を含めば false である。
func isBlankBytes(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}

// isSpaceByte は、c が空白、タブ、改行、復帰のどれかであるかを返す。
func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
