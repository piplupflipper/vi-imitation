package editor

import (
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// このファイルは、マウスの出来事（FR-180〜FR-185）の位置を、client の画面の部品に解いて、command.State へ渡す口である。
//
// マウスの出来事は、端末の解読（internal/term）が <LeftMouse> などのキーにし、位置をキーと一緒に届ける。
// キーは他のキーと同じく利用者のキーマップと操作の表を通り（FR-184）、操作の実装（internal/command の mouse.go）が、
// State.Mouse（mouseLocator）に位置を解かせる。位置は、そのキーを受けた端末の画面の座標で、
// 編集の client なら client の画面の描画の部品（viewFields.renderer）、部品の client（ポップアップとペイン）ならその部品の
// 画面の描画の部品（component.r）で解く（FR-185）。見比べのために分割したペインの client も、ほかの編集の client と同じく自分の画面で解く。
// 解く処理（画面の位置から部品への対応）は ui.Renderer.HitTest の 1 か所にある。

// mouseEvent は、client が最後に受けたマウスの出来事の位置である（handleKeys が noteMouse で clientView.mouse に入れる）。
// client ごとに持つ。マクロの再生とキーマップの展開で実行するマウスのキーは、その再生か展開を行う client
// （読み込んでいる client）が最後に受けた出来事の位置で解く（Vim と同じく、最後に届いた位置を使う）。
// 別の client が後から受けた出来事の位置を、この client の画面の座標として解かないためである（PR #346 のレビューの必須 1）。
type mouseEvent struct {
	ok       bool
	row, col int
	// comp は、出来事を受けた部品の client である。nil なら、その client 自身の端末で受けた。
	comp *component
}

// noteMouse は、client c（部品の client なら comp）の端末で受けたマウスの出来事のキー k の位置を、c の最後の位置として覚える。
// ボタンを押したことのキーなら、押した位置の部品の種類を、続くドラッグを寄せる部品として覚える（mouseDown）。
//
// req: FR-185
func (e *Editor) noteMouse(c *clientView, comp *component, k editcmd.Key, row, col int) {
	c.mouse = mouseEvent{ok: true, row: row, col: col, comp: comp}
	if !k.IsMousePress() {
		return
	}
	r, down := e.mouseScreen(c)
	*down = r.HitTest(row, col, ui.HitNone).Area
}

// mouseScreen は、client c が最後に受けたマウスの出来事の画面の描画の部品と、その画面でボタンを押した部品の種類の置き場を返す。
func (e *Editor) mouseScreen(c *clientView) (*ui.Renderer, *ui.HitArea) {
	if comp := c.mouse.comp; comp != nil {
		return comp.r, &comp.mouseDown
	}
	return e.viewOf(c).renderer, &c.mouseDown
}

// mouseEnabled は、端末にマウスの報告を求めるか（設定の mouse.enabled。FR-180）を返す。描くたびに読む。
//
// req: FR-180
func (e *Editor) mouseEnabled() bool {
	return e.options.Get(option.MouseEnabled).Bool()
}

// mouseLocator は、バッファ bs の State の Mouse（command.Mouse）である。読み込んでいる client が最後に受けたマウスの出来事
// （clientView.mouse）の位置を、出来事を受けた画面で解き、その位置にある部品が bs のテキストなら、その行と列を返す。
type mouseLocator struct {
	e  *Editor
	bs *bufferState
}

// hit は、読み込んでいる client が最後に受けたマウスの出来事の位置にあるものと、それを解いた画面の描画の部品を返す。
// drag なら、ボタンを押した部品（テキスト、ポップアップの窓、スクロールバー）の中へ寄せて解く。
func (m mouseLocator) hit(drag bool) (ui.Hit, *ui.Renderer, bool) {
	e := m.e
	c := e.view
	if c == nil || c.detached || !c.mouse.ok {
		return ui.Hit{}, nil, false
	}
	r, down := e.mouseScreen(c)
	clamp := ui.HitNone
	if drag {
		if *down != ui.HitText && *down != ui.HitFloat && *down != ui.HitScrollbar {
			return ui.Hit{}, nil, false
		}
		clamp = *down
	}
	return r.HitTest(c.mouse.row, c.mouse.col, clamp), r, true
}

// mouseComp は、読み込んでいる client が最後に受けたマウスの出来事の部品の client を返す。client 自身の端末で受けたなら nil である。
func (e *Editor) mouseComp() *component {
	if e.view == nil {
		return nil
	}
	return e.view.mouse.comp
}

// mouseFloat は、読み込んでいる client が最後に受けたマウスの出来事の画面のポップアップの窓の部品を返す。
// 部品の client の画面なら、その client が描く部品である（窓が画面の全体）。
func (e *Editor) mouseFloat() *floatState {
	if comp := e.mouseComp(); comp != nil {
		return comp.f
	}
	return e.float
}

// Target は、command.Mouse の Target である（FR-181、FR-185）。
//
//   - 編集の client の画面のバッファの表示領域は、そのタブのバッファ（tab）のテキストである。フォーカスのある
//     カーソルの近くのポップアップのバッファから見ると、ほかの部品（MouseElsewhere。Activate で元のタブへ戻る）である
//   - ポップアップの窓の中のテキストは、その窓のバッファのテキストである。部品の client の画面では、窓が画面の全体である
//   - タブの行のタブの見出しは、ほかの部品（MouseElsewhere。Activate でそのタブへ移る）である
//   - ステータス行とコマンドラインの行は、その画面でキーを受けるバッファ（cur。部品の client ならその部品のバッファ）の窓の、
//     テキストでない部分（MouseWindow。ホイールでその表示領域を送る）である。画面に窓は 1 つなので、Neovim のステータス行
//     （その上の窓）とコマンドラインの行（今の窓）の決め方は、どちらもこの窓になる
//   - 表示領域の右端のスクロールバー（FR-186）のつまみと印のセル（ui.HitScrollbar）は、キーを受けるバッファがタブのバッファなら、
//     そのスクロールバー（MouseScrollbar）である。押した行、つまみの最初の行、つまみの上か、描いた印の種類の行の一覧を入れる
//     （FR-189、FR-210、FR-211）。ホイールはその表示領域を送る。同じ列でも、つまみも印も描いていないセルは HitText なので、本文である。
//     スクロールバーで押したボタンのドラッグは、列に依らずスクロールバーの行として解く（つまみのドラッグ）
//   - ほか（枠線、ファインダー、閉じた窓の跡など）は MouseNone である
//
// req: FR-189, FR-210, FR-211
func (m mouseLocator) Target(drag bool) command.MouseTarget {
	none := command.MouseTarget{Kind: command.MouseNone}
	h, r, ok := m.hit(drag)
	if !ok {
		return none
	}
	e := m.e
	text := command.MouseTarget{Kind: command.MouseText, Line: h.Line, Col: h.Col, Over: h.Over}
	switch h.Area {
	case ui.HitStatusLine, ui.HitCommandLine:
		if e.cur == m.bs && !drag {
			return command.MouseTarget{Kind: command.MouseWindow}
		}
	case ui.HitText:
		if e.mouseComp() != nil {
			return none
		}
		if e.tab() == m.bs {
			return text
		}
		if f := e.float; f != nil && f.focused && !f.center && f.bs == m.bs && !drag {
			return command.MouseTarget{Kind: command.MouseElsewhere}
		}
	case ui.HitFloat:
		if f := e.mouseFloat(); f != nil && f.bs == m.bs {
			return text
		}
	case ui.HitTabLine:
		// 中央のポップアップを開いている間は、それだけを操作する（FR-68）。
		if f := e.float; f != nil && f.focused && f.center {
			return none
		}
		if h.Tab >= 0 && h.Tab < len(e.view.tabs) && e.mouseComp() == nil && !drag {
			return command.MouseTarget{Kind: command.MouseElsewhere}
		}
	case ui.HitScrollbar:
		// 右端のスクロールバー（FR-186）のつまみと印のセルは、タブのバッファの表示領域のスクロールバーである（FR-189）。
		// 押したらつまみのドラッグを始めるか印の行へ移り（FR-210、FR-211）、ホイールはステータス行の上と同じくその表示領域を送る。
		if e.tab() == m.bs && e.cur == m.bs && e.mouseComp() == nil {
			return command.MouseTarget{
				Kind: command.MouseScrollbar, Row: h.Row, ThumbRow: h.ThumbRow, Thumb: h.Thumb,
				Marks: r.ScrollbarMarkLines(h.Row),
			}
		}
	case ui.HitNone:
	}
	return none
}

// ScrollbarTop は、command.Mouse の ScrollbarTop である（FR-210）。読み込んでいる client が最後に受けたマウスの出来事の画面の、
// 直前の描画が描いたスクロールバーのつまみの最初の行を row 行目（表示領域の中の行）へ動かすときの、寄せた行と表示領域の最初の行を返す
// （ui.Renderer.ScrollbarTop）。
//
// req: FR-210
func (m mouseLocator) ScrollbarTop(row int) (at, line int, ok bool) {
	r := m.barRenderer()
	if r == nil {
		return 0, 0, false
	}
	return r.ScrollbarTop(row)
}

// HoldScrollbarThumb は、command.Mouse の HoldScrollbarThumb である（FR-210。ui.Renderer.HoldScrollbarThumb）。
//
// req: FR-210
func (m mouseLocator) HoldScrollbarThumb(row, line int) {
	if r := m.barRenderer(); r != nil {
		r.HoldScrollbarThumb(row, line)
	}
}

// barRenderer は、読み込んでいる client が最後に受けたマウスの出来事の画面の描画の部品を返す。スクロールバーを出さない画面
// （部品の client の画面）と、出来事の無いときは nil である。
func (m mouseLocator) barRenderer() *ui.Renderer {
	c := m.e.view
	if c == nil || c.detached || !c.mouse.ok || c.mouse.comp != nil {
		return nil
	}
	r, _ := m.e.mouseScreen(c)
	return r
}

// Activate は、command.Mouse の Activate である（FR-181）。
// タブの見出しならそのタブへ移る（:b と同じく、移るとマクロの再生の残りは移った先のバッファで続く）。
// フォーカスのあるカーソルの近くのポップアップの外のバッファの表示領域なら、フォーカスを元のタブへ戻し、その位置へカーソルを移す
// （Neovim でほかの窓を押したときと同じ）。中央のポップアップは、開いている間はそれだけを操作する（FR-68）ので、Target が外を返さない。
//
// req: FR-181
func (m mouseLocator) Activate() error {
	h, _, ok := m.hit(false)
	if !ok {
		return nil
	}
	e := m.e
	switch h.Area {
	case ui.HitTabLine:
		if h.Tab < 0 || h.Tab >= len(e.view.tabs) {
			return nil
		}
		if to := e.view.tabs[h.Tab]; to != e.tab() {
			e.gotoBuffer(m.bs, to)
		}
	case ui.HitText:
		f := e.float
		if f == nil || !f.focused || f.center || f.bs != m.bs {
			return nil
		}
		e.unfocusFloat()
		if base := e.cur; base != m.bs {
			base.state.MoveToMouse(command.MouseTarget{Kind: command.MouseText, Line: h.Line, Col: h.Col})
		}
	case ui.HitFloat, ui.HitNone, ui.HitStatusLine, ui.HitCommandLine, ui.HitScrollbar:
	}
	if e.cur != m.bs {
		return command.ErrBufferSwitched
	}
	return nil
}
