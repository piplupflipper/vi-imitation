package ui

// 切り替えの部品（Float.Switcher。FR-269）の見出しの行と、子ごとの窓の中の子の Renderer。

// floatKid は、切り替えの部品の子 1 つの窓の中を描く子の Renderer である（Renderer.floatKids）。
type floatKid struct {
	float, list *Renderer
}

// useFloatSubs は、窓 fl の中を描く子の Renderer（Renderer.float と Renderer.floatList）を決める。
// 窓（Float.ID）が替わったか、前の描画で窓を描かなかったなら作り直す。切り替えの部品なら、子（Switcher の選んでいる子の ID）ごとに
// 持ち、選んでいる子のものを float と floatList に入れる。子を切り替えて戻っても、その子のスクロールの位置を保つためである。
func (r *Renderer) useFloatSubs(fl *Float) {
	if r.float == nil || r.floatID != fl.ID {
		r.floatKids = nil
		r.floatID = fl.ID
	}
	key := ""
	if sw := fl.Switcher; sw != nil && sw.Selected >= 0 && sw.Selected < len(sw.Children) {
		key = sw.Children[sw.Selected].ID
	}
	if r.floatKids == nil {
		r.floatKids = map[string]*floatKid{}
	}
	k := r.floatKids[key]
	if k == nil {
		k = &floatKid{float: &Renderer{}, list: &Renderer{}}
		r.floatKids[key] = k
	}
	r.float, r.floatList = k.float, k.list
}

// FloatChildRenderers は、直前の Render が窓 id の切り替えの部品の子 child を描いたときの、窓の中の子の Renderer（text）と、
// 書き足すテキストと入力の行の組の出力を描いた子の Renderer（list）を返す。その子をまだ描いていないか、窓 id を描いていなければ nil である。
// 見えていない子のページ単位のスクロールと、書き足しで古い行を捨てたときの見ている行のずらしを、その子の表示領域につなぐのに使う
// （internal/editor。FR-269）。
func (r *Renderer) FloatChildRenderers(id uint64, child string) (text, list *Renderer) {
	if r.float == nil || r.floatID != id {
		return nil, nil
	}
	k := r.floatKids[child]
	if k == nil {
		return nil, nil
	}
	return k.float, k.list
}

// switcherLabel は、見出しの行に出す子 c の見出しの文字列を返す。名前の前後に空白を置き、キーがあれば "[キー] " を続ける
// （nvim-dap-view の lua/dap-view/options/winbar/init.lua の " " .. label .. " " .. "[" .. keymap .. "]" .. " "）。
func switcherLabel(c SwitcherChild) string {
	s := " " + c.Title + " "
	if c.Key != "" {
		s += "[" + c.Key + "] "
	}
	return s
}

// childSpan は、見出しの行に描いた子の見出し 1 つの場所（行 row の列 [from, to)）と、Switcher.Children の添字である。
type childSpan struct{ row, from, to, index int }

// drawSwitcher は、切り替えの部品の見出しの行 sw を、row 行目の列 x0 から列 x1 の手前まで描く（FR-269）。
// 子の見出し（switcherLabel）を左から並べ、選んでいる子を StyleViewChildSelected、ほかを StyleViewChild で描き、
// 残りの列は base で埋める。見出しが幅に収まらなければ、選んでいる子の見出しの右の端までが収まるように左の見出しを省いて左の端に "<" を置き、
// 右に収まらない見出しは収まる列で切って右の端に ">" を置く（タブの行の drawTabs と同じ考え方）。
// マウスで子を切り替えるために、描いた見出しの場所を残す（HitTest の HitChild。FR-185 の例外）。
//
// req: FR-269
func (r *Renderer) drawSwitcher(g *Grid, theme Theme, sw *Switcher, row, x0, x1 int, base Attr) {
	clearSplit(g, row, x0, x1)
	for c := x0; c < x1; c++ {
		g.set(row, c, blank(base))
	}
	avail := x1 - x0
	if avail <= 0 || len(sw.Children) == 0 {
		return
	}
	cur := min(max(sw.Selected, 0), len(sw.Children)-1)
	labels := make([]string, len(sw.Children))
	widths := make([]int, len(sw.Children))
	for i, c := range sw.Children {
		labels[i] = switcherLabel(c)
		widths[i] = StringWidth(labels[i], r.tabStop)
	}
	first := 0
	for first < cur {
		lead := 0
		if first > 0 {
			lead = 1
		}
		if sumTo(widths, first, cur)+lead <= avail {
			break
		}
		first++
	}
	col := x0
	if first > 0 {
		g.set(row, col, Cell{Text: "<", Width: 1, Attr: base.Over(theme.attr(StyleViewChild))})
		col++
	}
	clipped := sumTo(widths, first, len(widths)-1)+col-x0 > avail
	limit := x1
	if clipped && avail >= 2 && cur < len(widths)-1 {
		// 右に収まらない見出しがあれば、右の端の 1 列を ">" に使う。選んでいる子が最後の子なら、その名前を残すために使わない。
		limit = x1 - 1
		g.set(row, limit, Cell{Text: ">", Width: 1, Attr: base.Over(theme.attr(StyleViewChild))})
	}
	want := col
	for i := first; i < len(labels) && col < limit; i++ {
		a := base.Over(theme.attr(StyleViewChild))
		if i == cur {
			a = base.Over(theme.attr(StyleViewChildSelected))
		}
		want += widths[i]
		from := col
		col = r.drawText(g, row, col, limit, labels[i], a)
		r.hits.children = append(r.hits.children, childSpan{row: row, from: from, to: col, index: i})
		if col < want {
			break // 収まらずに切れた
		}
	}
}
