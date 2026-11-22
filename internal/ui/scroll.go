package ui

import "github.com/piplupflipper/vi-imitation/internal/scroll"

// renderLines は、描画の行の高さ（Renderer.height）を、縦の位置の数え方（internal/scroll）の Lines にしたものである。
type renderLines struct {
	r    *Renderer
	text Text
}

// LineCount は、scroll.Lines の LineCount である。
func (l renderLines) LineCount() int { return l.text.LineCount() }

// Rows は、scroll.Lines の Rows である。行を折り返さなければ 1 である。
func (l renderLines) Rows(n int) int { return l.r.height(l.text, n) }

// scrollView は、表示領域を h 行として、今の縦の位置と余白から、縦の位置の数え方（scroll.View）を作る。
// 数え方が決めた位置は、呼び出し側が Top と Skip を Renderer に戻す（setView）。
func (r *Renderer) scrollView(text Text, h int) scroll.View {
	return scroll.View{
		Lines: renderLines{r: r, text: text}, Height: h, Width: r.textWidth(),
		Margin: r.margins.Rows, Top: r.top, Skip: r.skip, Marker: r.markerOverlap(),
	}
}

// markerOverlap は、最初の行の途中から描くときの表示領域の最初の行の "<<<"（画面の左の端から 3 列。wrap.go）が、本文に重なる列の数を返す
// （Neovim 0.12.5 の sms_marker_overlap）。印の列と行番号の列が 3 列以上あれば重ならない。続きの行の記号（lineWrap.showBreak）を付けるときは、
// Neovim と同じく 0 にする。行を折り返さないときは 0 である。
func (r *Renderer) markerOverlap() int {
	if !r.wrap || r.wrapper().sbw > 0 {
		return 0
	}
	return max(3-r.textLeft(), 0)
}

// setView は、縦の位置の数え方が決めた最初の行と隠す見た目の行を、Renderer に戻す。
func (r *Renderer) setView(v scroll.View) {
	r.top, r.skip = max(v.Top, 0), max(v.Skip, 0)
}

// topKey は、縦の位置をカーソルに合わせて決めたときの入力である（Renderer.valid）。次の描画でどれかが変わっていれば、決め直す。
// Vim の w_valid の VALID_TOPLINE が、カーソルの行の移動（check_cursor_moved）、テキストの変更、窓の大きさと表示の設定の変更で
// 消えるのに当たる。余白（Margins）は入れない。Neovim 0.12.5 も、:set scrolloff= の後はカーソルの行が動くまで決め直さない
// （2026-11-21 に tmux で、表示領域の最後の行にカーソルがある画面で :set so=5 を打って確かめた）。
type topKey struct {
	ok      bool
	line    int    // カーソルの行
	lines   int    // バッファの行の数
	version uint64 // バッファのバージョン番号（Text が Version を持つとき）
	rows    int    // 表示領域の行数
	width   int    // 本文の幅
	wrap    bool
	rule    lineRule // 行の文字の並べ方（タブ文字の幅と空白の記号。見た目の行の数を変える）
	// virtGen は、行の仮想テキスト（rule.virt。同じポインターのまま中身が変わる）の世代の番号である（VirtualTexts.Gen）。
	virtGen uint64
}

// versioned は、バージョン番号を持つ Text（buffer.Buffer）である。
type versioned interface{ Version() uint64 }

// topKeyOf は、今の描画の入力の topKey を、カーソルの行を line として返す。
func (r *Renderer) topKeyOf(text Text, line int) topKey {
	k := topKey{
		ok: true, line: line, lines: text.LineCount(), rows: r.textRows(), width: r.textWidth(),
		wrap: r.wrap, rule: r.rule, virtGen: r.rule.virt.Gen(),
	}
	if v, ok := text.(versioned); ok {
		k.version = v.Version()
	}
	return k
}

// SetScrollHeld は、縦の位置を最初の行 top、隠す見た目の行 skip にし、カーソルが行 line にある間（テキストと表示の設定が変わるまで）、
// 描画がカーソルに合わせて縦の位置を決め直さないようにする（Neovim の scroll_redraw や scroll_cursor_top が VALID_TOPLINE を立てるのと同じ）。
// 横の位置は変えない。line が負なら、次の描画で決め直す（SetScroll と同じ）。
// スクロールの操作（internal/command の zt、<C-e>、<C-d> など）が余白を数えて決めた位置を、描画の数え直しで動かさないために使う。
// バッファの末尾の近くで、余白をすべては残せない位置に決めたとき（Neovim の「the screen jumps back at the end of the file」）に効く。
// まだ描いていなければ、次の描画で決め直す。
func (r *Renderer) SetScrollHeld(top, skip, line int) {
	r.top, r.skip, r.topFill = max(top, 0), max(skip, 0), 0
	if line < 0 {
		r.valid = topKey{}
		return
	}
	if r.valid.ok {
		r.valid.line = line
	}
}

// Skip は、最初の行を折り返した見た目の行のうち、表示領域より上に隠している行の数を返す（Vim の w_skipcol を見た目の行で数えたもの）。
func (r *Renderer) Skip() int { return r.skip }

// Botline は、直前の描画で、表示領域に最後まで描いた行の次の行の番号（0 から数える。Vim の w_botline）を返す。
// 行を折り返すときは、見た目の行をすべて描いた行だけを数え、最初の行を最後まで描けなかったときは最初の行の次の行にする
// （Neovim 0.12.5 のルーラーと同じ）。最後の行まで描いていれば、行の数である。見えている範囲は Top から Botline の前の行までで、
// 最後に途中まで描いた行があればそれも見えている。表示の範囲を数え直さずに読むための口で、右の端の位置の印（スクロールバー）が使う。
// 値は直前の Render が決めたもので、その後に動かした位置（SetScroll、SetScrollHeld など）や、次の Render の中で決める位置には追いつかない。
// 同じ描画の中で、その描画の範囲として使うと、1 回前の描画の範囲になる。Render が返った後に読む。まだ描いていなければ 0 を返す。
func (r *Renderer) Botline() int { return r.bot }

// scroll は、カーソルが余白（Frame.Margins）を残して表示領域に入るように、縦と横のスクロールの位置を更新する。
//
// 縦は internal/scroll の View.Follow（Neovim 0.12.5 の update_topline）で決める。行を折り返すときは、行の高さを見た目の行の数で数える。
// 前の描画から、カーソルの行、テキスト、表示領域の大きさ、行の並べ方がどれも変わっていなければ（行の中だけでカーソルが動いたとき）、
// 最初の行は決め直さず、最初の行の途中から見せる位置だけを決め直す（View.KeepTop。Neovim は VALID_TOPLINE が立っている間、
// update_topline を飛ばして curs_columns だけで w_skipcol を決める）。SetScrollHeld で決めた位置も同じく決め直さない。
//
// 横は、行を折り返さないときだけ動かす。'sidescroll' が 0 の既定に合わせ、カーソルの文字が、左右に余白の列（Margins.Columns）を
// 残した範囲から出たら、カーソルを画面の幅の半分の位置に置く（Neovim の curs_columns）。行を折り返すときは横にスクロールしない。
//
// req: FR-174
func (r *Renderer) scroll(text Text, cur cursorPlace) {
	v := r.scrollView(text, r.textRows())
	if k := r.topKeyOf(text, cur.line); r.valid == k {
		v.KeepTop(cur.line, cur.row)
	} else {
		v.Follow(cur.line, cur.row, cur.x)
		r.valid = k
	}
	r.setView(v)
	if r.wrap {
		r.left = 0
		return
	}
	w := r.textWidth()
	if w <= 0 {
		return
	}
	siso := max(r.margins.Columns, 0)
	offLeft := cur.start - r.left - siso
	offRight := cur.end - r.left - (w - siso)
	if offLeft < 0 || offRight > 0 {
		r.left = max(cur.col-w/2, 0)
	}
}
