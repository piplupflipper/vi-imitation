package ui

import (
	"cmp"
	"iter"
	"math"
	"slices"
)

// Decorations は、構文の色の範囲の一覧を、行で引けるように分けたものである。
//
// decoration/set はバッファ全体の一覧を送る（基本設計 6.1）。描画のたびに全体をなめないように、
// internal/editor は一覧を受け取ったときに NewDecorations で 1 回だけ作り、描画ではそれを Frame に渡す。
// 作った後は変更しないので、同じ値を何度の描画に渡してもよく、作った goroutine と別の goroutine で読んでもよい。
//
// 範囲は、すべての行の分を 1 つの列 spans に行の順に詰め、行ごとの開始の位置 first で引く。
// 行ごとにスライスを持つと、一覧の大きさに比例する数の割り当てになり、作る時間の大半がガベージコレクションになった（#74）。
// spans はポインターを持たない（色の種類の名前は styles の添字で持つ）ので、ガベージコレクションが中を走査しない。
//
// 行をまたぐ範囲は、開始の行と終わりの行の部分だけを spans に入れ、間の行（行の全体）は行の区間 1 つとして rows に持つ。
// 前は間の行にも 1 つずつ入れたので、表は「行をまたぐ範囲の数 × 掛かる行の数」に膨らんだ（#80）。
// 10 万行のほぼ全体にまたがる範囲 100 個で 1,000 万要素（346 MB）になり、不具合のある拡張機能が全体にまたがる範囲を
// 多数送ると、本体のメモリを使い切るおそれがあった。いまの表の大きさは、行数と範囲の数の和に比例する。
type Decorations struct {
	first  []int       // n 行目の範囲は spans[first[n]:first[n+1]]。長さは行数 + 1
	spans  []decoSpan  // 行の順、行の中では開始のバイトの順に並べる
	styles []decoStyle // 色の種類の名前と直に重ねる色の組の表。decoSpan.style が添字
	rows   []decoRows  // 行をまたぐ範囲の、間の行の区間。lo の順に並べる
	// rowsMax は、rows を、[l, r) の中央 m を根とする暗黙の二分探索木と見たときの、m を根とする部分木の hi の最大である。
	// ある行に掛かる区間を、全部をなめずに探すのに使う（rowsAt）。
	rowsMax []int
}

// decoSpan は、1 行の中の構文の色の範囲 [start, end) である。end が math.MaxInt なら行末までを表す。
type decoSpan struct {
	start, end int
	order      int // 元の一覧での位置。重なったときは大きい方を上に重ねる
	style      int // Decorations.styles の添字
}

// decoRows は、行をまたぐ範囲のうち、lo 行目から hi 行目までの、行の全体に掛かる部分である。
type decoRows struct {
	lo, hi int // 行の区間 [lo, hi]
	order  int // decoSpan.order と同じ
	style  int // decoSpan.style と同じ
}

// NewDecorations は、hs を行ごとに分けた Decorations を返す。lines は、描画するテキストの行数である。
//
// 行をまたぐ範囲は、またいだ各行に分けたものとして引ける（途中の行は行の全体。行末の 1 セルは含めない）。
// End が Start より前の範囲は、2 つを入れ替えて扱う。空の範囲は捨てる。
// 位置は描画する時点のテキストを指す必要がある。行の長さを超えるバイト位置は、描画のときに行末に詰める。
// lines 行目以降の部分は捨てる。
//
// 作る表の大きさは、lines と範囲の数の和で決まり、範囲の行番号の大きさにも、範囲が掛かる行の数にも比例しない。
// 前は終わりの行まで表を伸ばしたので、不具合のある拡張機能が送った終わりが 20 億行目の範囲 1 つで、
// 本体が 20 億要素の表を確保しようとした（#75）。その後も、行をまたぐ範囲を各行に展開していたので、
// 表は「範囲の数 × 掛かる行の数」まで膨らんだ（#80）。
func NewDecorations(hs []Highlight, lines int) *Decorations {
	return NewDecorationsSeq(slices.Values(hs), lines)
}

// NewDecorationsSeq は、NewDecorations の、範囲の列を iter.Seq で受け取る形である。
// hs を 2 回なめるので、hs は 2 回とも同じ列を返す必要がある。
// 呼ぶ側が持つ別の型の一覧から、[]Highlight の写しを作らずに表を作れる。
// 10 万行の JSON の一覧（80 万の範囲）では、写しが 38 MB になり、その走査でガベージコレクションの時間が増えた（#74）。
//
// req: FR-35, NFR-03
func NewDecorationsSeq(hs iter.Seq[Highlight], lines int) *Decorations {
	d := &Decorations{}
	if lines <= 0 {
		return d
	}
	styleIndex := styleTable{}
	// 1 回目に行ごとの範囲の数を数え、間の行の区間を集める。2 回目に行ごとの範囲をその位置へ詰める。
	d.first = make([]int, lines+1)
	count := d.first[1:]
	forEachLineSpan(hs, lines, func(n int, _ decoSpan, _ decoStyle) { count[n]++ }, func(r decoRows, style decoStyle) {
		r.style = styleIndex.index(style)
		d.rows = append(d.rows, r)
	})
	total := 0
	for n, c := range count {
		count[n] = total
		total += c
	}
	// ここで count[n]（= first[n+1]）は n 行目の開始の位置である。詰めるたびに 1 つずつ進め、詰め終えると n+1 行目の開始の位置になる。
	d.spans = make([]decoSpan, total)
	forEachLineSpan(hs, lines, func(n int, sp decoSpan, style decoStyle) {
		sp.style = styleIndex.index(style)
		d.spans[count[n]] = sp
		count[n]++
	}, nil)
	d.styles = styleIndex.names
	for n := range lines {
		l := d.spans[d.first[n]:d.first[n+1]]
		// 構文の色の一覧はたいてい開始の順に並んでいるので、並んでいれば並べ替えない。
		if !slices.IsSortedFunc(l, compareStart) {
			slices.SortStableFunc(l, compareStart)
		}
	}
	if len(d.rows) > 0 {
		d.rows = slices.Clip(d.rows)
		slices.SortStableFunc(d.rows, func(a, b decoRows) int { return cmp.Compare(a.lo, b.lo) })
		d.rowsMax = make([]int, len(d.rows))
		d.indexRows(0, len(d.rows))
	}
	return d
}

func compareStart(a, b decoSpan) int { return cmp.Compare(a.start, b.start) }

// indexRows は、rows[l:r] の rowsMax を埋め、その中の hi の最大を返す。r <= l なら -1 を返す。
func (d *Decorations) indexRows(l, r int) int {
	if l >= r {
		return -1
	}
	m := int(uint(l+r) >> 1)
	v := max(d.rows[m].hi, d.indexRows(l, m), d.indexRows(m+1, r))
	d.rowsMax[m] = v
	return v
}

// rowsAt は、rows[l:r] のうち n 行目に掛かる区間を、lo の順に dst へ足す。
// 時間は、rows の長さの対数と、掛かる区間の数に比例する。
func (d *Decorations) rowsAt(dst []decoSpan, n, l, r int) []decoSpan {
	for l < r {
		m := int(uint(l+r) >> 1)
		if d.rowsMax[m] < n {
			return dst
		}
		dst = d.rowsAt(dst, n, l, m)
		rw := d.rows[m]
		if rw.lo > n {
			// m より後の区間は、lo が m 以上なので n 行目に掛からない。
			return dst
		}
		if rw.hi >= n {
			dst = append(dst, decoSpan{start: 0, end: math.MaxInt, order: rw.order, style: rw.style})
		}
		l = m + 1
	}
	return dst
}

// forEachLineSpan は、hs の範囲を行ごとに分け、0 行目から lines-1 行目に掛かる部分を渡す。
// 1 行の中の部分（行をまたぐ範囲の開始の行と終わりの行の部分を含む）は f に、行をまたぐ範囲の間の行（行の全体）は
// 行の区間にまとめて rows に渡す。rows が nil なら間の行は捨てる。
// decoSpan と decoRows の order には hs での位置を入れ、style は入れずに名前と直に重ねる色の組を引数で渡す。
func forEachLineSpan(hs iter.Seq[Highlight], lines int, f func(n int, sp decoSpan, style decoStyle), rows func(r decoRows, style decoStyle)) {
	i := -1
	for h := range hs {
		i++
		st := decoStyle{name: h.Style, attr: h.Attr}
		s, e := h.Range.Start, h.Range.End
		if e.Less(s) {
			s, e = e, s
		}
		if s == e || e.Line < 0 || s.Line >= lines {
			continue
		}
		if s.Line == e.Line {
			if sp := (decoSpan{start: max(s.Byte, 0), end: e.Byte, order: i}); sp.start < sp.end {
				f(s.Line, sp, st)
			}
			continue
		}
		if s.Line >= 0 {
			f(s.Line, decoSpan{start: max(s.Byte, 0), end: math.MaxInt, order: i}, st)
		}
		if lo, hi := max(s.Line+1, 0), min(e.Line-1, lines-1); lo <= hi && rows != nil {
			rows(decoRows{lo: lo, hi: hi, order: i}, st)
		}
		if e.Line < lines && e.Byte > 0 {
			f(e.Line, decoSpan{start: 0, end: e.Byte, order: i}, st)
		}
	}
}

// decoStyle は、範囲の色の種類の名前と、その上に直に重ねる色（Highlight.Attr。FR-230。nil なら重ねない）の組である。
// 色はポインターのまま比べるので、同じ値の色でも違うポインターなら表の別の項目になる（描く色は同じになる）。
// internal/editor は同じ色の範囲に同じポインターを渡すので、表は色の種類の数で収まる。値で比べると、数十万の範囲を表に入れるたびに
// 色の項目まで写して比べることになり、表づくりが遅くなった。
type decoStyle struct {
	name string
	attr *Attr
}

// over は、theme の名前の色の上に、直に重ねる色を重ねた属性を返す。
func (st decoStyle) over(theme Theme) Attr {
	a := theme.attr(st.name)
	if st.attr != nil {
		a = a.Over(*st.attr)
	}
	return a
}

// styleTable は、色の種類の名前と直に重ねる色の組に、現れた順に添字を振る。
// 組の種類はふつう十に満たないので、直前の組と表を先頭から比べる。
// 同じ表から復号した名前は文字列の中身を共有するので、比べるのは速い。
// 不具合のある拡張機能が多くの種類を送っても比べる回数が増え続けないように、種類が smallStyles を超えたら map で引く。
type styleTable struct {
	names  []decoStyle
	last   int
	byName map[decoStyle]int
}

const smallStyles = 16

func (t *styleTable) index(name decoStyle) int {
	if t.last < len(t.names) && t.names[t.last] == name {
		return t.last
	}
	var i int
	if t.byName != nil {
		var ok bool
		if i, ok = t.byName[name]; !ok {
			i = -1
		}
	} else {
		i = slices.Index(t.names, name)
	}
	if i < 0 {
		i = len(t.names)
		t.names = append(t.names, name)
		if t.byName == nil && len(t.names) > smallStyles {
			t.byName = make(map[decoStyle]int, len(t.names))
			for j, n := range t.names {
				t.byName[n] = j
			}
		} else if t.byName != nil {
			t.byName[name] = i
		}
	}
	t.last = i
	return i
}

// appendLine は、n 行目の範囲を、開始のバイトの順（同じ開始では元の一覧の順）に dst へ足して返す。d が nil でもよい。
//
// 行をまたぐ範囲の間の行の部分（行の全体）は、同じ色の種類の名前のものが複数あれば、元の一覧で最も後のものだけを足す。
// 同じ名前は同じ属性になり、行の全体に掛かる後の範囲がすべてのセルで上に重なるので、前の範囲は描画の結果を変えない
// （Attr.Over は、色は上の既定でない色、飾りはどちらかにあれば付ける）。
// これで、全体にまたがる範囲が多数あっても、1 行あたりの範囲は色の種類の数で抑えられる。
func (d *Decorations) appendLine(dst []decoSpan, n int) []decoSpan {
	if d == nil || n < 0 || n >= len(d.first)-1 {
		return dst
	}
	base := len(dst)
	dst = d.rowsAt(dst, n, 0, len(d.rows))
	dst = dedupRows(dst, base)
	full := len(dst) > base
	dst = append(dst, d.spans[d.first[n]:d.first[n+1]]...)
	if full {
		slices.SortFunc(dst[base:], func(a, b decoSpan) int {
			return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.order, b.order))
		})
	}
	return dst
}

// dedupRows は、dst[base:] の行の全体の範囲を、色の種類ごとに order の最も大きいものだけにして返す。
func dedupRows(dst []decoSpan, base int) []decoSpan {
	if len(dst)-base < 2 {
		return dst
	}
	l := dst[base:]
	slices.SortFunc(l, func(a, b decoSpan) int {
		return cmp.Or(cmp.Compare(a.style, b.style), cmp.Compare(b.order, a.order))
	})
	k := 1
	for _, sp := range l[1:] {
		if sp.style != l[k-1].style {
			l[k] = sp
			k++
		}
	}
	return dst[:base+k]
}

// line は、n 行目の範囲を appendLine で新しいスライスにして返す。範囲が無ければ nil を返す。
func (d *Decorations) line(n int) []decoSpan {
	return d.appendLine(nil, n)
}

// style は、sp の色の種類の名前を返す。sp は d.line が返したものである必要がある。
func (d *Decorations) style(sp decoSpan) string {
	return d.styles[sp.style].name
}

// attr は、sp の属性（theme の名前の色の上に、直に重ねる色を重ねたもの。FR-230）を返す。sp は d.line が返したものである必要がある。
//
// req: FR-230
func (d *Decorations) attr(theme Theme, sp decoSpan) Attr {
	return d.styles[sp.style].over(theme)
}
