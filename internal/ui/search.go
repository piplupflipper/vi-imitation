package ui

// searchBatch は、searchSpans が 1 回目に探す一致の数である。表示の範囲の右の端に届かなければ、倍にして探し直す。
const searchBatch = 64

// searchSpans は、バッファの 1 行のテキスト s のうち、Frame.Search に一致する範囲を、一致の色の範囲として dst に足して返す（FR-137）。
// [from, to) は、その行で表示の範囲に掛かる文字のバイトの範囲である（drawer.visibleBytes）。
//
//   - 一致は、行の先頭から重ならないように並べたもの（regexp の FindAllStringIndex）である。1 行に複数の一致があれば、どれにも付ける
//   - パターンは行ごとに当てるので、行をまたぐ一致は無い（検索の n と同じ。target の search）
//   - 空の一致（^、$、x* など）は、その位置の 1 文字に付ける。行末の位置なら行末の 1 セルに付ける（Vim 9.1 で :set hlsearch の後の
//     /^ と /$ の画面を 2026-11-20 に tmux で確かめた）。Go の regexp は、直前の一致の終わりに接する空の一致を返さないので、
//     axxb の /x* は a、xx、行末のセルに付き、b には付かない（Vim は b にも付ける）
//
// 呼ぶのは表示領域に描く行（drawLine）だけで、バッファの全体は走査しない。
// 行の中でも、一致を集めるのは表示の範囲の右の端（to）までにする。パターンは行の全体（s）に当てるので、
// ^、$、\b のような前後の文字に依る書き方の一致は、右の端で行を切ったときと違って変わらない。
// FindAllStringIndex は件数の上限を渡すと上限で探すのをやめるので、最後の一致の先頭が to に届くまで、上限を広げて探し直す
// （広げ方は、見つけた一致の密度から右の端までの件数を見積もり、少なくとも倍にする）。
// 時間は、行の先頭から右の端までの長さと、そこまでの一致の数に比例する（drawLine が行の先頭から文字を数え直すのと同じく、
// 右へ横にスクロールした行では左の端より左の一致も数える。PR #276 のレビューの推奨 1）。
// 一致の無い長い行は、一致を探して行末まで走査する。左の端（from）より左で終わる一致は、範囲にしない。
//
// req: FR-137
func (d *drawer) searchSpans(dst []span, s string, from, to int) []span {
	re := d.f.Search
	if re == nil {
		return dst
	}
	a := d.theme.attr(StyleSearch)
	if a == (Attr{}) {
		return dst
	}
	var ms [][]int
	for n := searchBatch; ; {
		ms = re.FindAllStringIndex(s, n)
		last := 0
		if len(ms) > 0 {
			last = ms[len(ms)-1][0]
		}
		if len(ms) < n || last >= to {
			break
		}
		// 見つけた一致の密度から右の端までの件数を見積もり、少し多めに探し直す（少なくとも倍にする）。
		n = max(2*n, int(float64(n)*float64(to)/float64(last+1)*1.1)+searchBatch)
	}
	for _, m := range ms {
		start, end := m[0], m[1]
		if start > to || start == to && to < len(s) {
			break
		}
		if start == end {
			// span の end が行の長さより大きければ、行末のセルを含む（span の doc）。文字の途中を指すことは無い（regexp は文字の境界で一致する）。
			end = start + 1
		}
		if end <= from {
			continue
		}
		dst = append(dst, span{start: start, end: end, order: searchOrder, attr: a})
	}
	return dst
}

// visibleBytes は、行 s のうち、表示の範囲（左の端 r.left から textWidth 列）に掛かる文字のバイトの範囲 [from, to) を返す。
// drawLine の文字の走査と同じく、行の先頭から表示の列を数え、右の端に掛かる文字の次で止める。行の全体が右の端より左に収まれば to は len(s) である。
// n は s の行の番号で、inline の仮想テキスト（FR-231）の列も drawLine と同じく数える。
func (d *drawer) visibleBytes(n int, s string) (from, to int) {
	left, right := d.r.left, d.r.left+d.r.textWidth()
	col := 0
	from = -1
	wk := d.r.rule.walk(n, s)
	for wk.more() && col < right {
		gl := wk.next(col)
		if gl.virt == 0 {
			if from < 0 && gl.col+gl.width > left {
				from = gl.off
			}
			to = gl.off + gl.size
		}
		col += gl.width
	}
	if from < 0 {
		from = to
	}
	return from, to
}
