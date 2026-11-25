package ui

// このファイルは、マウスの出来事の画面の位置を、直前の Render が描いた部品の位置に解く（FR-181、FR-185）。
// 位置を「どの部品のどこか」に解く処理はここ 1 か所に置く。後から部品（右の端の位置の印の列、窓の上端の帯など）を足すときは、
// HitArea に種類を足し、その部品を描く関数が場所を hitState に残して、HitTest がそれを引く。

// HitArea は、画面の位置にある部品の種類である（Renderer.HitTest）。
type HitArea int

const (
	// HitNone は、マウスで操作する部品が無い位置である（出力の欄、浮かべた一覧の窓、ポップアップの枠線と見出し、プレビュー、
	// ファインダーの窓、画面の外など）。
	HitNone HitArea = iota
	// HitText は、バッファの表示領域である。印の列、行番号の列、最後の行より下の "~" の行、見比べの詰め物の行を含む。
	HitText
	// HitFloat は、ポップアップの窓（Frame.Float）の中のテキストである。部品の client の画面では、窓が表示領域の全体である（Float.Fill）。
	HitFloat
	// HitTabLine は、タブの行である。
	HitTabLine
	// HitStatusLine は、ステータス行である（ホイールでその上の表示領域を送る。FR-182）。
	HitStatusLine
	// HitCommandLine は、コマンドラインの行（メッセージの行）である（ホイールで今の表示領域を送る。FR-182）。
	HitCommandLine
	// HitScrollbar は、表示領域の右端のスクロールバー（Frame.Scrollbar。FR-186、FR-189）の、つまみか印を描いたセルである。
	// 同じ列でも、つまみも印も描いていないセルと、スクロールバーを描いていない（表示領域に収まる、設定で出さない）ときは HitText である。
	HitScrollbar
	// HitBand は、表示領域の上の端の帯（Frame.Band。FR-237、FR-238）の、バッファの行に当たる行の上である。
	// Line、Col、Below は、帯の下に隠れた本文の位置（HitText と同じ）で、BandLine は帯の行に当たるバッファの行である。
	// 帯の行に当たる行が無いか、帯を作った後に本文が変わった行（BandLine.Stale）は HitText である。
	HitBand
	// HitChild は、切り替えの部品（Float.Switcher。FR-269）の見出しの行の、子の見出しの上である。Child はその子の添字である。
	// 見出しの行の見出しの無い列は HitNone である（FR-185 の例外は子の見出しだけ）。
	HitChild
)

// Hit は、画面の位置にあるもの（Renderer.HitTest の結果）である。
type Hit struct {
	Area HitArea
	// Line は、Area が HitText か HitFloat か HitBand のときの、バッファの行（0 から数える）である。HitBand では、帯の下に隠れた本文の行である。
	// 最後の行より下の "~" の行は最後の行に、見比べの詰め物の行はその下の行（無ければ上の行）にする。
	// Area が HitScrollbar のときは、その行のセルに割り付けたバッファの行の範囲の最初の行である（drawScrollbar の割り付け。
	// 範囲は [Line, LineEnd)）。
	Line int
	// LineEnd は、Area が HitScrollbar のときの、その行のセルに割り付けたバッファの行の範囲の終わり（含まない）である。
	// バッファの行の数が表示領域の行数より少ない（長い行を折り返している）ときは、どの行も割り付けていないセルがあり、
	// そのセルでは Line と LineEnd が同じになる。
	LineEnd int
	// Thumb は、Area が HitScrollbar のときに、その行のセルがつまみの上かである。
	Thumb bool
	// Mark は、Area が HitScrollbar のときに、そのセルに描いた印の種類の、Frame.Scrollbar.Marks の添字である。印が無ければ -1 である。
	// 描くときと同じ選び方（優先の最も大きい種類）で決めたもので、画面に出ている印と同じである。
	Mark int
	// MarkFirst と MarkLast は、Mark の種類の行（ScrollbarMarks.Lines）のうち、そのセルに割り付けた範囲 [Line, LineEnd) に入る
	// 最初の行と最後の行である（印を押して、その行へ移るときに使う）。印が無ければどちらも -1 である。
	// Hit を == で比べられるように、行の一覧そのものは持たない（一覧は Renderer.ScrollbarMarkLines で引く）。
	MarkFirst, MarkLast int
	// Row は、Area が HitScrollbar のときの、位置の表示領域の中の行（0 から数える）である。
	// HitTest の clamp が HitScrollbar（つまみのドラッグの間）なら、表示領域の上（負）か下（表示領域の行数以上）へはみ出した行もそのまま数える。
	Row int
	// ThumbRow は、Area が HitScrollbar のときの、直前の Render が描いたつまみの最初の行（表示領域の中の行）である。
	// つまみを押した位置とつまみの最初の行の差を、ドラッグの間に保つために使う（FR-210）。
	ThumbRow int
	// Col は、Area が HitText か HitFloat か HitBand のときの、行の表示の列（0 から数える）である。
	// 行を折り返さないときは行の先頭からの列（横のスクロールの位置を足したもの）、折り返すときは WrapLines の列
	// （見た目の行の番号 × 本文の幅 + 見た目の行の中の列）で、どちらも望みの列（internal/command の State の want）と同じ数え方である。
	// 印の列と行番号の列は、本文の左の端の列として数える。
	Col int
	// Below は、最後の行より下の "~" の行かである。
	Below bool
	// Tab は、Area が HitTabLine のときの、Frame.Tabs の添字である。タブの見出しの上でなければ -1 である。
	Tab int
	// Over は、HitTest を部品の中へ寄せて解いたときに、位置が部品の上（負）か下（正）へはみ出していた行の数である。
	Over int
	// BandLine は、Area が HitBand のときの、押した帯の行に当たるバッファの行（0 から数える。ui.BandLine.Line）である。
	BandLine int
	// Child は、Area が HitChild のときの、切り替えの部品の子の添字（Switcher.Children の添字）である。
	Child int
}

// rect は、画面の矩形（行と列は 0 から数える。タブの行を除いた座標）である。
type rect struct{ top, left, h, w int }

func (b rect) contains(row, col int) bool {
	return row >= b.top && row < b.top+b.h && col >= b.left && col < b.left+b.w
}

// tabSpan は、タブの行に描いたタブの見出し 1 つの列の範囲 [from, to) と、Frame.Tabs の添字である。
type tabSpan struct{ from, to, index int }

// hitState は、直前の Render が描いた部品の場所である（HitTest が引く）。Render の始めに空にする。
type hitState struct {
	// lines は、描いたバッファの行の数である。
	lines int
	// float は、ポップアップの窓の中のテキストの場所で、子の Renderer（Renderer.float）の表示領域に当たる。floatOK が false なら無い。
	float   rect
	floatOK bool
	// covers は、表示領域の上に重ねて描いた窓の場所である（枠線、見出し、プレビュー、浮かべた一覧、ファインダー）。
	covers []rect
	// tabs は、タブの行に描いたタブの見出しの場所である。
	tabs []tabSpan
	// bar は、描いたスクロールバーの列の場所（幅は 1 列）である。barOK が false なら描いていない。
	// barCells は、bar の行ごとのセルに割り付けたものと描いたもの（drawScrollbar が残す）である。
	// barThumb は描いたつまみの最初の行（表示領域の中の行）、barLines はスクロールバーを描いたバッファの行の数である。
	bar      rect
	barOK    bool
	barCells []scrollbarCell
	barThumb int
	barLines int
	// pin は、つまみのドラッグの間に、つまみを描く行（Renderer.HoldScrollbarThumb）である。Render の始めに空にしない。
	pin scrollbarPin
	// band は、描いた帯の場所（高さ 0 なら描いていない）で、bandLines は、その行ごとの、押したら移るバッファの行（無ければ -1）である
	// （drawBand が残す）。
	band      rect
	bandLines []int
	// children は、切り替えの部品の見出しの行に描いた子の見出しの場所である（drawSwitcher）。
	children []childSpan
}

// reset は、h を空にする。
func (h *hitState) reset() {
	h.band, h.bandLines = rect{}, h.bandLines[:0]
	h.lines, h.floatOK, h.barOK = 0, false, false
	h.covers, h.tabs, h.children = h.covers[:0], h.tabs[:0], h.children[:0]
}

// cover は、row 行目の x 列目から幅 w 列、高さ n 行の窓を、表示領域の上に重ねて描いたことを残す。
func (h *hitState) cover(row, x, n, w int) {
	if n > 0 && w > 0 {
		h.covers = append(h.covers, rect{top: row, left: x, h: n, w: w})
	}
}

// HitTest は、直前の Render が描いた画面の row 行目の col 列目（0 から数える）にあるものを返す（FR-181、FR-185）。
//
// clamp が HitText か HitFloat なら、位置をその部品の中へ寄せて解く（ドラッグの間の位置。Over にはみ出した行の数を入れる）。
// 寄せる前に、位置を画面の中へ寄せる。端末が画面の外の位置を報告しても、Neovim 0.12.5 と同じく画面の端の行として数える
// （PR #346 のレビューの推奨 2）。その部品を描いていなければ HitNone を返す。clamp が HitNone なら寄せずに、位置にある部品を返す。
// まだ描いていなければ HitNone を返す。
// 直前の Render が右端のスクロールバー（FR-186）のつまみか印を描いたセルは、浮かべた窓の下でなければ HitScrollbar にする（FR-189）。
// clamp が HitScrollbar なら（つまみのドラッグの間。FR-210）、位置を画面の中へ寄せてから、列に依らずスクロールバーの行として解き、
// 表示領域の外の行もそのまま Hit.Row に入れる。スクロールバーを描いていなければ HitNone を返す。
// 直前の Render が描いた帯（FR-237）の、バッファの行に当たる行は、浮かべた窓とスクロールバーのつまみと印の下でなければ
// HitBand にする（FR-238）。clamp が HitText なら、帯の上も本文として解く。
// 直前の Render が描いた切り替えの部品の見出しの行の子の見出しは、窓の中でも HitChild にする（FR-269。FR-185 の例外）。
//
// req: FR-181, FR-182, FR-185, FR-189, FR-210, FR-238
func (r *Renderer) HitTest(row, col int, clamp HitArea) Hit {
	none := Hit{Area: HitNone, Tab: -1}
	if !r.rendered || r.Width <= 0 || r.Height <= 0 {
		return none
	}
	if clamp == HitText || clamp == HitFloat || clamp == HitScrollbar {
		row, col = min(max(row, 0), r.Height-1), min(max(col, 0), r.Width-1)
	}
	if r.tabRows > 0 && row >= 0 && row < r.tabRows && clamp == HitNone {
		return r.hitTab(col)
	}
	row -= r.tabRows
	switch clamp {
	case HitText:
		return r.hitText(row, col, true)
	case HitFloat:
		if h, ok := r.hitFloat(row, col, true); ok {
			return h
		}
		return none
	case HitScrollbar:
		if !r.hits.barOK {
			return none
		}
		return Hit{Area: HitScrollbar, Tab: -1, Mark: -1, MarkFirst: -1, MarkLast: -1, Row: row - r.hits.bar.top, ThumbRow: r.hits.barThumb}
	case HitNone, HitTabLine, HitStatusLine, HitCommandLine, HitBand, HitChild:
	}
	if h, ok := r.hitFloat(row, col, false); ok {
		return h
	}
	for _, s := range r.hits.children {
		// 切り替えの部品の見出しの行の子の見出しは、窓（covers）の中でも、押すとその子に切り替える場所として返す（FR-185 の例外）。
		if row == s.row && col >= s.from && col < s.to {
			return Hit{Area: HitChild, Tab: -1, Child: s.index}
		}
	}
	for _, b := range r.hits.covers {
		if b.contains(row, col) {
			return none
		}
	}
	if b := r.hits.bar; r.hits.barOK && b.contains(row, col) {
		if h, ok := r.hitScrollbar(row - b.top); ok {
			return h
		}
	}
	// 帯はスクロールバーの下に描く（layers.go）ので、スクロールバーのつまみと印のセルの後に引く。
	if h, ok := r.hitBand(row, col); ok {
		return h
	}
	if col >= 0 && col < r.Width {
		// ステータス行は表示領域のすぐ下、コマンドラインの行は画面の最後の行である（render）。出力の欄の行はどちらでもない。
		body := r.Height - r.tabRows
		switch {
		case row == body-1:
			return Hit{Area: HitCommandLine, Tab: -1}
		case row == r.textRows() && body >= 2:
			return Hit{Area: HitStatusLine, Tab: -1}
		}
	}
	return r.hitText(row, col, false)
}

// hitScrollbar は、直前の Render が描いたスクロールバーの i 行目（表示領域の行）のセルが、つまみか印のセルなら、
// そのセルに割り付けたもの（drawScrollbar が描いたときに決めた scrollbarCell）を ok を true にして返す。
// つまみも印も描いていないセルは、ok を false にして本文に渡す（nvim-scrollview も、つまみと印の窓の上だけを自分で扱う）。
//
// req: FR-189
func (r *Renderer) hitScrollbar(i int) (Hit, bool) {
	if i < 0 || i >= len(r.hits.barCells) {
		return Hit{}, false
	}
	c := r.hits.barCells[i]
	if !c.thumb && c.mark < 0 {
		return Hit{}, false
	}
	h := Hit{Area: HitScrollbar, Tab: -1, Line: c.lo, LineEnd: c.hi, Thumb: c.thumb, Mark: c.mark, MarkFirst: -1, MarkLast: -1, Row: i, ThumbRow: r.hits.barThumb}
	if len(c.lines) > 0 {
		h.MarkFirst, h.MarkLast = c.lines[0], c.lines[len(c.lines)-1]
	}
	return h, true
}

// hitTab は、タブの行の col 列目にあるタブの見出しを返す。
func (r *Renderer) hitTab(col int) Hit {
	for _, s := range r.hits.tabs {
		if col >= s.from && col < s.to {
			return Hit{Area: HitTabLine, Tab: s.index}
		}
	}
	return Hit{Area: HitTabLine, Tab: -1}
}

// hitFloat は、row 行目の col 列目（タブの行を除いた座標）がポップアップの窓の中のテキストの上なら、その位置を ok を true にして返す。
// clamp なら、位置を窓の中のテキストへ寄せる。
func (r *Renderer) hitFloat(row, col int, clamp bool) (Hit, bool) {
	b := r.hits.float
	if !r.hits.floatOK || r.float == nil {
		return Hit{}, false
	}
	over := 0
	if clamp {
		switch {
		case row < b.top:
			over, row = row-b.top, b.top
		case row >= b.top+b.h:
			over, row = row-(b.top+b.h-1), b.top+b.h-1
		}
		col = min(max(col, b.left), b.left+b.w-1)
	}
	if !b.contains(row, col) {
		return Hit{}, false
	}
	h := r.float.hitText(row-b.top, col-b.left, false)
	if h.Area != HitText {
		return Hit{Area: HitNone, Tab: -1}, true
	}
	h.Area, h.Over = HitFloat, over
	return h, true
}

// hitText は、表示領域の row 行目の col 列目にあるバッファの位置を返す。clamp なら、位置を表示領域の中へ寄せる。
func (r *Renderer) hitText(row, col int, clamp bool) Hit {
	none := Hit{Area: HitNone, Tab: -1}
	rows := min(r.textRows(), len(r.view))
	if rows <= 0 || r.hits.lines <= 0 {
		return none
	}
	over := 0
	if clamp {
		switch {
		case row < 0:
			over, row = row, 0
		case row >= rows:
			over, row = row-(rows-1), rows-1
		}
		col = min(max(col, 0), r.Width-1)
	}
	if row < 0 || row >= rows || col < 0 || col >= r.Width {
		return none
	}
	vr := r.view[row]
	if vr.line < 0 {
		// 見比べの詰め物の行は、その下の行（無ければ上の行）として解く。
		vr = viewRow{line: -1}
		for i := row + 1; i < rows && vr.line < 0; i++ {
			vr = viewRow{line: r.view[i].line}
		}
		for i := row - 1; i >= 0 && vr.line < 0; i-- {
			vr = viewRow{line: r.view[i].line}
		}
		if vr.line < 0 {
			return none
		}
	}
	x := max(col-r.textLeft(), 0)
	h := Hit{Area: HitText, Line: vr.line, Tab: -1, Over: over}
	if vr.line >= r.hits.lines {
		h.Line, h.Below = r.hits.lines-1, true
		vr.k = 0
	}
	if r.wrap {
		h.Col = vr.k*r.wrapper().width + x
	} else {
		h.Col = r.left + x
	}
	return h
}
