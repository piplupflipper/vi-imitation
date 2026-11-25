package editor

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 端末多重化ソフトのポップアップとペインの部品の client（基本設計 1.2、1.3、1.4。FR-66、FR-67、FR-115、FR-123）。
//
// # 部品の client
//
// 部品の client は、端末多重化ソフト（tmux、herdr）のポップアップかペインの中で動く、端末を持つ client である。
// 描くのは部品（floatState。popup.go）1 つだけで、タブの行を持たない。部品はどれも、それを開いた編集の client（元の client）の
// viewFields に置いたまま（中央のポップアップは float、ペインは panes）で、部品の client は描く場所だけを替える（floatState.remote）。
//
//   - 中央のポップアップ: 元の client の float の中央のポップアップを開いたら、Config.OpenPopup で端末多重化ソフトのポップアップを開き、
//     その中で vi-imitation -popup <部品の ID> を起動させる（requestPopup）。部品の client が繋ぐまでと、開けなかったときは、
//     これまでどおり元の client の画面の中央に描く（FR-67 の「どちらも使えないときは、client が自分のペインの中央に描く」）
//   - ペイン: 拡張機能の pane/open で、Config.OpenPane で元の client のペインを分割し、その中で vi-imitation -pane <部品の ID> を起動させる
//     （paneOpenRequest）。端末多重化ソフトの外と、ペインを開けなかったときは、ペインに代えて中央のポップアップに出す（FR-123）
//
// 部品の client が server に繋ぐと、server が AttachComponent を呼び、部品に部品の client の端末をつなぐ。
// 部品の client を開いた元の client は、部品が元の client の viewFields にあることで決まり、部品の client には知らせない
// （herdr のポップアップの中のプロセスには、自分のペインを知る環境変数が入らない。#177 の「参考」）。
//
// # キーと選んだ結果
//
// 部品の client で受けたキーは、元の client が受けたキーとして列に積み（keyInput.comp）、元の client の操作として処理する（基本設計 1.2）。
// 中央のポップアップは元の client のフォーカスのあるポップアップなので、キーはそのまま元の client のキーとして処理する。
// ペインは元の client のフォーカスの外にあるので、キーを処理する間だけ、ペインの部品を元の client のフォーカスのあるポップアップにする
// （paneInput）。処理し終えたら、元のタブ（とフォーカスのあったポップアップ）へ戻す。
// 一覧と木で選んだ結果の view/select も、元の client の操作の後に拡張機能へ届くので、拡張機能が続けて送る editor/open は元の client のタブに開く。
//
// # 閉じる
//
// 部品を閉じたら（<Esc>、:q、view/close、元の client が終わった）、部品の client に理由の無い終わりの知らせを送る（ClientExiter）。
// 部品の client が終わると、端末多重化ソフトのポップアップとペインも閉じる（tmux の display-popup -E、herdr の plugin.pane.open）。
// 部品の client の接続が先に切れたら（利用者が端末多重化ソフトでペインを閉じた）、部品を閉じ、拡張機能の部品なら view/closed を送る（componentGone）。
// 部品の client を server の外から閉じる手段（mux.Mux.ClosePopup と ClosePane）は使わない（herdr の popup.close は他のプラグインのポップアップも閉じうる。
// 基本設計 1.3）。
//
// # id 付き通知の組の取り決め
//
// 部品の ID は floatState.id（ext.ViewID と同じ番号）で、server の中で一意で、使い回さない（基本設計 1.2 の取り決めの id の範囲）。
// 部品の client と server の間には問いと応答の組が無い（Hello の View で部品を指し、閉じるのは終わりの知らせ）。
// 部品の client の接続が閉じたら、その部品を閉じたものとして片付ける（取り決めの「応答しない相手」）。
//
// # 端末多重化ソフトの外
//
// 端末多重化ソフトの外（FR-60）では、Config.OpenPopup と OpenPane を呼ばず（server は端末多重化ソフトの層を持たない）、
// 中央のポップアップは元の client の画面の中央に描き、ペインは中央のポップアップに出す（FR-123。基本設計 1.2）。
// 1 つの拡張機能の複数の pane/open を 1 枚のポップアップにまとめる形（基本設計 1.2）は、まだ作っていない。
// 1 つの client のポップアップは 1 つなので、後の pane/open は前のものを閉じて置き換える（前のものの持ち主へ view/closed を送る）。

// PaneOptions は、Config.OpenPane に渡す、ペインの開き方である（ext.PaneOpenParams の Direction、Size、Focus）。
type PaneOptions struct {
	// Down は、元のペインの下に開くことを表す。false なら右に開く。
	Down bool
	// Size は、新しいペインの大きさの百分率である。0 なら端末多重化ソフトの既定である。
	Size int
	// Focus は、新しいペインへフォーカスを移すことを表す。
	Focus bool
}

// PopupOptions は、Config.OpenPopup に渡す、端末多重化ソフトのポップアップの開き方である。
type PopupOptions struct {
	// Width は、ポップアップの幅の、端末の幅に対する百分率である。0 なら server の既定（80%）である。
	// プレビュー（FR-149）を出す部品では、設定 preview.windowWidthPercent の値にする（requestPopup）。
	Width int
}

// component は、部品を描く部品の client の端末と、その画面を描く描画の部品である。
type component struct {
	// f は、この client が描く部品である。イベントループだけが読む。
	f    *floatState
	term Terminal
	// muxPane は、部品の client のいる端末多重化ソフトのペインの識別子（Hello の Pane）である。知らなければ空である。
	muxPane string
	r       *ui.Renderer
	// size は、部品の client の端末から届いた最新の大きさ（[2]int）である。入力を読む goroutine が置き、イベントループが読む。
	size atomic.Value
	// mouseDown は、この部品の client の端末でマウスのボタンを最後に押した位置の部品の種類である（mouse.go）。イベントループだけが使う。
	mouseDown ui.HitArea
}

// exit は、部品の client に理由の無い終わりの知らせを送らせる（ClientExiter）。送り終えるのを待たない。
func (c *component) exit() {
	if x, ok := c.term.(ClientExiter); ok {
		x.Exit("")
	}
}

// ErrUnknownView は、AttachComponent の部品が無い（もう閉じた）か、既に別の部品の client が描いていることを表す。
var ErrUnknownView = errors.New("editor: unknown view for a component client")

// ComponentInfo は、部品の client が server に知らせる情報である（protocol.Hello の View、Kind、Pane）。
type ComponentInfo struct {
	// View は、描く部品の ID である。
	View uint64
	// Pane は、ペインの部品の client（protocol.KindPane）であることを表す。false ならポップアップの部品の client である。
	// ペインの部品にはペインの部品の client だけを、中央のポップアップにはポップアップの部品の client だけを繋ぐ（PR #248 のレビューの推奨 3。
	// ペインを開けずに中央のポップアップへ移した後に、遅れて開いたペインの部品の client が繋がないため）。
	Pane bool
	// MuxPane は、部品の client のいる端末多重化ソフトのペインの識別子（TMUX_PANE、HERDR_PANE_ID）である。知らなければ空である。
	// ペインで選んだ後にフォーカスを戻す前に、利用者がまだそのペインにいるかを確かめるのに使う（focusAfterPane）。
	MuxPane string
}

// AttachComponent は、部品 info.View を描く部品の client の端末 term を繋ぐ（基本設計 1.2。FR-67、FR-115）。
// Run を動かしている間に、他の goroutine から呼ぶ。イベントループが、部品を開いた元の client を探し、以後 term のキーを
// 元の client の操作として処理し、部品を term の画面に描く。
// 部品が無いか、種類が合わないか、既に繋いでいれば ErrUnknownView を、Run が終わっていれば ErrStopped を返す。
// term は、部品が閉じるか（ClientExiter で終わりを知らせる）、term の入力が閉じるか、Run が戻るまで使う。
//
// req: FR-67, FR-115
func (e *Editor) AttachComponent(term Terminal, info ComponentInfo) error {
	done := make(chan error, 1)
	f := func() { done <- e.attachComponent(term, info) }
	select {
	case e.inspect <- f:
	case <-e.finished:
		return ErrStopped
	}
	select {
	case err := <-done:
		return err
	case <-e.finished:
		return ErrStopped
	}
}

// attachComponent は、イベントループで、AttachComponent の部品の client を繋ぐ。
func (e *Editor) attachComponent(term Terminal, info ComponentInfo) error {
	view, pane := info.View, info.Pane
	for _, c := range e.clients {
		if c.detached {
			continue
		}
		v := e.viewOf(c)
		var f *floatState
		if g := v.float; !pane && g != nil && g.center && g.id == view {
			f = g
		} else if i := slices.IndexFunc(v.panes, func(p *floatState) bool { return p.id == view }); pane && i >= 0 {
			f = v.panes[i]
		}
		if f == nil {
			continue
		}
		if f.remote != nil {
			return ErrUnknownView
		}
		comp := &component{f: f, term: term, r: e.newRenderer(), muxPane: info.MuxPane}
		w, h := term.Size()
		comp.r.Width, comp.r.Height = max(w, 0), max(h, 0)
		f.remote = comp
		e.log.Info("部品の client を繋いだ", "view", view, "client", c.info.ID, "pane", f.pane)
		go e.forwardInput(c, term, comp)
		return nil
	}
	return ErrUnknownView
}

// componentGone は、部品の client comp の入力が閉じたときに、イベントループで呼ぶ。
// 部品がまだその client で描いていれば（利用者が端末多重化ソフトでペインかポップアップを閉じた）、部品を閉じ、
// 拡張機能の部品なら view/closed を送る（基本設計 1.2）。部品を先に閉じていれば（閉じたので部品の client が終わった）何もしない。
func (e *Editor) componentGone(c *clientView, comp *component) {
	f := comp.f
	if !c.attached() || f.remote != comp {
		return
	}
	e.withView(c, func() {
		f.remote = nil
		e.closeAnyFloat(f, true)
	})
}

// closeAnyFloat は、読み込んでいる client の部品 f を閉じる。f がポップアップなら closeFloat、ペインなら closePane である。
// どちらでもなければ（もう閉じた）何もしない。
func (e *Editor) closeAnyFloat(f *floatState, notify bool) {
	switch {
	case e.float == f:
		e.closeFloat(notify)
	case slices.Contains(e.panes, f):
		e.closePane(f, notify)
	}
}

// closePane は、読み込んでいる client のペインの部品 p を閉じる（closeFloat と同じ後始末をする）。
// フォーカスのある元の client のポップアップは、そのまま残す。
func (e *Editor) closePane(p *floatState, notify bool) {
	saved := e.float
	if p != e.paneActive {
		e.float = p
	}
	e.closeFloat(notify)
	if saved != p {
		e.float = saved
	}
}

// componentInput は、部品の client comp で受けたキーか貼り付け in を、元の client（読み込んで操作中にした client）の操作として処理する。
// 部品がもう閉じたか、別の部品の client に替わっていれば捨てる。
func (e *Editor) componentInput(in keyInput) {
	f := in.comp.f
	switch {
	case f.remote != in.comp:
		e.afterKey() // 中断の印を下ろす
	case f.pane && slices.Contains(e.panes, f):
		e.paneInput(f, in)
	case e.float == f:
		e.feedInput(in)
		e.afterKey()
	default:
		e.afterKey()
	}
}

// feedInput は、キーか貼り付け in を、カーソルのあるバッファ（フォーカスのあるポップアップのバッファ）へ渡す。
func (e *Editor) feedInput(in keyInput) {
	if in.paste != "" {
		e.handlePaste(in.paste)
		return
	}
	e.handleKey(in.key)
}

// paneInput は、ペインの部品 p の client で受けたキーか貼り付け in を処理する（基本設計 1.2）。
// 処理する間だけ、p を元の client のフォーカスのあるポップアップにし（元の client のポップアップは退避する）、キーをペインのバッファへ渡す。
// このため、ポップアップと同じく、一覧と木の <CR> で選び、popupKeys のキーが効き、:q でペインを閉じる。<Esc> では閉じない（floatKey）。
// マクロの記録とレジスターは、元の client のタブとの間で :b と同じく引き継ぐ（switchFocus）。
// 処理し終えたら、元のタブ（とフォーカスのあったポップアップ）へ戻す（leavePane）。
//
// req: FR-115
func (e *Editor) paneInput(p *floatState, in keyInput) {
	// 利用者はペインを見ているので、元の client の画面の診断のポップアップを閉じ、カーソルが止まった契機を知らせない（cursorhold.go）。
	e.leaveFocus()
	e.paneSaved, e.paneActive = e.float, p
	p.base = e.cur
	p.focused = false
	e.float = p
	e.focusFloat()
	e.feedInput(in)
	e.afterKey()
	e.leavePane()
}

// leavePane は、paneInput の途中なら、キーを受けるバッファをペインのバッファから元のタブ（フォーカスのあったポップアップ）へ戻し、
// 退避していた元の client のポップアップを戻す。ペインは閉じない。
func (e *Editor) leavePane() {
	p, saved := e.paneActive, e.paneSaved
	if p == nil {
		return
	}
	e.paneActive, e.paneSaved = nil, nil
	if e.float != p {
		return
	}
	e.float = saved
	p.focused = false
	base := p.base
	switch {
	case saved != nil && saved.focused:
		base = saved.bs
	case base == nil || !slices.Contains(e.view.tabs, base):
		base = e.firstTab()
	}
	if base != nil {
		e.switchFocus(base)
	}
}

// noteViewClient は、部品 f の持ち主の拡張機能へ利用者の操作の知らせ（view/select、view/key、view/expand、input/change、view/closed）を
// 送る前に、その拡張機能が後から届けるもの（editor/open、message/show など）の届け先を、部品を開いた client（読み込んでいる client）にする
// （extView。基本設計 1.2 の「部品の client の中の選択の結果は、元の client の操作として扱う」。PR #248 のレビューの必須）。
// ペインを開いた後に別の client がその拡張機能を使っても、ペインで選んだファイルはペインを開いた client のタブに開く。
// selected は、知らせが一覧か木の項目を選んだこと（view/select）であることを表す。ペインの部品の client のキーで項目を選んだときだけ、
// 続く editor/open でペインから端末多重化ソフトのフォーカスを戻す印を立てる（focusAfterPane）。
// ペインの中だけのキー（view/key。DAP のペインのステップ実行など）、開閉（view/expand）、入力の行の中身（input/change）では立てない
// （オーケストレータの決定。キーを打つたびにフォーカスがペインから出ないため）。
func (e *Editor) noteViewClient(f *floatState, selected bool) {
	if f.ext == "" || e.view == nil {
		return
	}
	e.extClient[f.ext] = e.view
	if f.pane {
		e.notePane(f.ext, f)
	} else {
		e.notePane(f.ext, e.paneActive)
	}
	if selected && f.pane && f == e.paneActive {
		mp := ""
		if f.remote != nil {
			mp = f.remote.muxPane
		}
		e.extFromPane[f.ext] = paneMark{at: time.Now(), pane: mp}
	} else {
		delete(e.extFromPane, f.ext)
	}
}

// paneMark は、ペインの部品の client のキーで項目を選んだ印である（noteViewClient、focusAfterPane）。
type paneMark struct {
	// at は、選んだキーを処理した時刻である。
	at time.Time
	// pane は、その部品の client のペインの識別子（component.muxPane）である。空なら知らない（フォーカスを移さない）。
	pane string
}

// paneFocusWindow は、ペインで選んでから、その応答の editor/open でフォーカスを戻すまでの上限である（仮。focusAfterPane）。
// ファイラが選んだファイルを開くのは、ふつうこれより早い。遅れたときはフォーカスを動かさずにタブだけを替える。
const paneFocusWindow = time.Second

// paneFocusLimit は、フォーカスを戻すまでの上限を返す。テストが替えていなければ paneFocusWindow である。
func (e *Editor) paneFocusLimit() time.Duration {
	if e.paneFocus > 0 {
		return e.paneFocus
	}
	return paneFocusWindow
}

// focusAfterPane は、拡張機能 extID の editor/open を開いた後に呼ぶ。move は、focus が true で、読み込んでいる client のタブに開いたことを表す。
// その拡張機能への最後の操作がペインの部品の client のキー（ファイラで選んだなど）で、その後に利用者がどの client でもキーを打っていなければ、
// 端末多重化ソフトのフォーカスをそのペインから、開いたタブの client のペインへ移す（FR-105。PR #248 のレビューの推奨 1）。
// 拡張機能は端末多重化ソフトを触れないためである（FR-115）。
//
// 印（extFromPane）は、ペインで選んだ後の最初の editor/open 1 回だけに使い、move に依らず下ろす。利用者がキーを打っても下ろす（handleKeys）。
// 遅れて届いた editor/open（DAP の止まった位置など）が、別のペインや別の window へ移った利用者を引き戻さないためである（PR #248 の再レビューの必須）。
// 印の無い editor/open は、端末多重化ソフトのフォーカスを動かさない（その client のタブを替えるだけ）。
// キーを打たずに別の window へ移った利用者を引き戻さないよう、選んでから paneFocusWindow を過ぎて届いた editor/open でも移さない（仮）。
//
// req: FR-115
func (e *Editor) focusAfterPane(extID string, move bool) {
	m, ok := e.extFromPane[extID]
	if !ok {
		return
	}
	delete(e.extFromPane, extID)
	if !move || time.Since(m.at) > e.paneFocusLimit() {
		return
	}
	to := e.view.info.Pane
	if e.cfg.FocusPane == nil || e.cfg.PaneFocused == nil || to == "" || m.pane == "" {
		return
	}
	focus, focused, log := e.cfg.FocusPane, e.cfg.PaneFocused, e.log
	go func() {
		defer e.recoverPanic()
		// 利用者がまだ部品のペインにいるときだけ移す。キーを打たずに別のペインや window へ移った利用者を引き戻さない
		// （PR #248 の 3 回目の確認の推奨 1）。確かめられなければ移さない。
		if ok, err := focused(m.pane); err != nil || !ok {
			if err != nil {
				log.Warn("部品のペインがアクティブかを確かめられない", "pane", m.pane, "err", err)
			}
			return
		}
		if err := focus(to, to); err != nil {
			log.Warn("ペインの部品から元の client のペインへフォーカスを移せない", "pane", to, "err", err)
		}
	}()
}

// requestPopup は、読み込んでいる client に開いた中央のポップアップ f を、端末多重化ソフトのポップアップに出すよう頼む（FR-67）。
// 別の goroutine で Config.OpenPopup を呼ぶ。開けなければ、誤りをその client のメッセージの行に出す（ポップアップは画面の中央に描いたまま）。
// プレビューの欄を持つ部品（previewCapable）なら、先にポップアップを出す端末の列数を問い合わせ（Config.PopupScreenWidth）、
// その列数の preview.windowWidthPercent % が境目を超えるなら、その百分率の幅でポップアップを開かせる（FR-149）。
// 出すかの判定（previewShown）も、同じ列数（floatState.screen）から同じ規則で行う。
// ポップアップの百分率は端末の幅に対するものなので、開いた client のペインの幅で判定すると、分割した狭いペインから開いたときに、
// 判定と実際の窓の幅が食い違う（PR #298 のレビューの必須 1）。問い合わせられなければ、元の client の画面の幅で代える。
//
// req: FR-67, FR-149
func (e *Editor) requestPopup(f *floatState) {
	pane := e.view.info.Pane
	if e.cfg.OpenPopup == nil || pane == "" {
		return
	}
	capable := e.previewCapable(f)
	if capable {
		f.screenPending = true
	}
	rule, fallback, screenWidth := e.previewRule(), e.renderer.Width, e.cfg.PopupScreenWidth
	open, id, dir, c := e.cfg.OpenPopup, f.id, e.workDir(), e.view
	go func() {
		defer e.recoverPanic()
		var opts PopupOptions
		if capable {
			width := fallback
			if screenWidth != nil {
				if w, werr := screenWidth(pane); werr == nil && w > 0 {
					width = w
				} else {
					e.log.Info("ポップアップを出す端末の幅を得られないので、ペインの幅で代える", "view", id, "err", werr)
				}
			}
			if _, _, ok := rule.Layout(width, false); ok {
				opts.Width = rule.WindowWidthPercent
			}
			e.later(func() { f.screen, f.screenPending = width, false })
		}
		err := open(pane, id, dir, opts)
		if err == nil {
			return
		}
		e.log.Warn("端末多重化ソフトのポップアップを開けない", "view", id, "err", err)
		e.later(func() {
			// 編集の client の画面の中に描くので、その画面の幅で判定する。
			f.screen = 0
			if c.attached() && c.replay != nil {
				// マクロの再生の途中で開けなかった（端末多重化ソフトが消えたなど）。再生を続けると、開けないポップアップを開き直し続けるので、
				// <C-c> と同じく再生を止める（PR #249 の 4 回目の確認の必須）。
				e.interrupt.Store(true)
				c.intr.Store(true)
				e.interruptReplay()
			}
			if c.attached() && e.viewOf(c).float == f && f.remote == nil {
				e.withView(c, func() {
					e.showError(fmt.Errorf("端末多重化ソフトのポップアップを開けないので、この画面の中に出します: %w", err))
				})
			}
		})
	}()
}

// later は、f をイベントループの goroutine で実行させる。待たない。Run が終わっていれば捨てる。
func (e *Editor) later(f func()) {
	select {
	case e.inspect <- f:
	case <-e.stop:
	}
}

// paneOpenRequest は、拡張機能の pane/open を処理する（FR-115、FR-123、FR-147）。その拡張機能へ最後に問い合わせを始めた client
// （元の client。呼び出し側が読み込む）のペインを分割して部品を開き、部品の ID を応答する。
// 出す場所（ext.PaneOpenParams.Placement）が中央のポップアップか、端末多重化ソフトの外か、元の client のペインを知らなければ、
// ペインに代えて中央のポップアップに出す（FR-123、FR-147）。中央のポップアップは popup/open の PopupCenter と同じものである
// （tmux と herdr の中では端末多重化ソフトのポップアップに出す。openFloat）。
// ペインは別の goroutine で開き（Config.OpenPane）、開けなければ、同じ ID のまま中央のポップアップへ移して、理由をメッセージの行に出す。
// CloseOnSelect は、中央のポップアップに出したときに、項目を選んだら閉じることを表す（closeSelected）。
//
// req: FR-115, FR-123, FR-147
func (e *Editor) paneOpenRequest(ev *exthost.PaneOpenRequest) {
	p := ev.Params
	if e.cur == nil {
		_ = ev.ReplyError(errors.New("no client to show the pane"))
		return
	}
	if err := checkComponentKeys(p.Component); err != nil {
		_ = ev.ReplyError(invalidParams("%v", err))
		return
	}
	f := e.newComponentFloat(p.Component, ev.Extension)
	f.closeOnSelect = p.CloseOnSelect
	pane := e.view.info.Pane
	if p.Placement == ext.PaneCenter || e.cfg.OpenPane == nil || pane == "" {
		f.center, f.focused = true, true
		e.openFloat(f)
		e.startConsoleInsert(f)
		_ = ev.Reply(ext.PaneOpenResult{View: ext.ViewID(f.id)})
		return
	}
	e.floatSeq++
	f.id = e.floatSeq
	f.pane = true
	f.base = e.cur
	f.shownTab = e.tab()
	f.shownPath = f.shownTab.path
	e.bindViewports(f)
	e.panes = append(e.panes, f)
	e.startConsoleInsert(f)
	_ = ev.Reply(ext.PaneOpenResult{View: ext.ViewID(f.id)})

	open, dir, c := e.cfg.OpenPane, e.workDir(), e.view
	opts := PaneOptions{Down: p.Direction == ext.PaneDown, Size: p.Size, Focus: p.Focus}
	if p.Focus {
		// フォーカスは新しいペインへ移るので、元の client の画面の診断のポップアップを閉じ、カーソルが止まった契機を知らせない
		// （cursorhold.go の leaveFocus。PR #333 の再レビューの推奨 1）。開けなかったら、中央のポップアップが元の client のフォーカスを取る。
		e.leaveFocus()
	}
	go func() {
		defer e.recoverPanic()
		err := open(pane, f.id, dir, opts)
		if err == nil {
			return
		}
		e.log.Warn("pane/open のペインを開けない", "view", f.id, "err", err)
		e.later(func() {
			if !c.attached() || !slices.Contains(e.viewOf(c).panes, f) || f.remote != nil {
				return
			}
			e.withView(c, func() {
				e.panes = slices.DeleteFunc(e.panes, func(o *floatState) bool { return o == f })
				f.pane = false
				f.center, f.focused = true, true
				e.openFloat(f)
				e.showError(fmt.Errorf("ペインを開けないので、中央のポップアップに出します: %w", err))
			})
		})
	}()
}

// notifyPaneTabs は、読み込んでいる client のカーソルのあるタブ（tab）が、分割したペインの部品ごとに最後に知らせたタブから替わっていれば、
// その部品の持ち主の拡張機能へ view/tabChange で知らせる（ext.ViewTabChangeParams。FR-105 のファイラの追従）。描く前に呼ぶ。
// 描くのは溜まったキーを処理し終えてから（renderIdle）なので、キーの列の途中のタブの行き来は知らせない。
// ペインを開いた時点のタブは知らせない（shownTab をそのタブにして開く）。
// noteViewClient は呼ばない。利用者の操作の知らせではなく、ペインで選んだ後のフォーカスを戻す印（extFromPane）を下ろさないためである。
//
// req: FR-105
func (e *Editor) notifyPaneTabs() {
	t := e.tab()
	if t == nil {
		return
	}
	for _, p := range e.panes {
		// 同じバッファでもパスが替われば知らせる（名前の無いバッファで :e を打つと、そのバッファにファイルを読む）。
		if p.ext == "" || (p.shownTab == t && p.shownPath == t.path) {
			continue
		}
		p.shownTab, p.shownPath = t, t.path
		params := ext.ViewTabChangeParams{View: ext.ViewID(p.id), Buffer: t.id, Path: t.path}
		if err := e.host.Notify(p.ext, ext.MethodViewTabChange, params); err != nil {
			e.log.Warn("view/tabChange を送れない", "extension", p.ext, "err", err)
		}
	}
}

// applyComponentSizes は、読み込んでいる client の部品の client の端末から届いた最新の大きさを、その画面の描画の部品に反映する。
func (e *Editor) applyComponentSizes() {
	each := func(f *floatState) {
		if c := f.remote; c != nil {
			if v, ok := c.size.Load().([2]int); ok {
				c.r.Width, c.r.Height = max(v[0], 0), max(v[1], 0)
			}
		}
	}
	if f := e.float; f != nil {
		each(f)
	}
	for _, p := range e.panes {
		each(p)
	}
}

// renderComponents は、読み込んでいる client の部品のうち、部品の client が描くものを、その client の画面に描く。
func (e *Editor) renderComponents() {
	if f := e.float; f != nil && f.remote != nil && e.cur != nil {
		e.drawComponent(f.remote, e.popupComponentFrame(f))
	}
	for _, p := range e.panes {
		if p.remote != nil {
			e.drawComponent(p.remote, e.paneComponentFrame(p))
		}
	}
}

// drawComponent は、部品の client c の画面に fr を描く。
func (e *Editor) drawComponent(c *component, fr *ui.Frame) {
	if c.r.Width <= 0 || c.r.Height <= 0 {
		return
	}
	scr := c.r.Render(fr)
	scr.Mouse = e.mouseEnabled() // 部品の client の端末にも、編集の client と同じくマウスの報告を求める（FR-180、FR-185）
	if err := c.term.Draw(scr); err != nil {
		e.log.Error("部品の client の描画に失敗", "err", err)
	}
}

// popupComponentFrame は、中央のポップアップ f を描くポップアップの部品の client の画面を作る。
// 元の client の画面（frame）から、バッファの表示領域とタブの行を除き、部品を表示領域の全体に広げたもの（ui.Float.Fill）である。
// ステータス行、コマンドライン、メッセージ、問いは、元の client の画面と同じものを出す（元の client のペインは端末多重化ソフトの
// ポップアップの下に隠れるため）。
func (e *Editor) popupComponentFrame(f *floatState) *ui.Frame {
	fr := e.frame()
	fr.Text, fr.Decorations, fr.Diagnostics, fr.Signs = nil, nil, nil, nil
	fr.Tabs, fr.Output, fr.Diff = nil, nil, nil
	fr.Selection = ui.Selection{}
	fr.Cursor = buffer.Position{}
	if fr.List == nil && fr.Choice == nil {
		fl := e.floatFrame(f)
		fl.Fill = true
		fr.Float = fl
	}
	return fr
}

// paneComponentFrame は、ペインの部品 p を描くペインの部品の client の画面を作る。
// 部品を表示領域の全体に広げ、ステータス行とコマンドラインには、ペインのバッファの名前、モード、打ちかけのコマンドラインを出す。
// メッセージは元の client の画面に出す（ペインの横に見えている）。
// ただし、このペインのキーから頼んだ拡張機能の問い（choice/ask、input/ask）は、元の client の画面と一緒に、このペインの画面にも出す
// （questionPane。端末多重化ソフトでペインを拡大していても問いが見えるようにする。PR #411 のレビューの裁定要 1）。
//
// プレビューにフォーカスを移している（previewState.focus）なら、ステータス行とコマンドラインは、キーを受けるプレビューのバッファのもの
// にする（FR-149、FR-71。中央のポップアップで、ui がステータス行をプレビューのバッファのものにするのと同じ。PR #298 のレビューの必須 2）。
//
// req: FR-149
func (e *Editor) paneComponentFrame(p *floatState) *ui.Frame {
	bs := p.bs
	if pv := p.preview.bs; p.preview.focus && pv != nil && slices.Contains(e.bufs, pv) {
		bs = pv
	}
	if p.con != nil && p.con.outFocus && slices.Contains(e.bufs, p.con.out) {
		// 書き足すテキストと入力の行の組で、フォーカスを出力へ移している（FR-274）。
		bs = p.con.out
	}
	st := bs.state
	focused := p.focused
	p.focused = true // モードと選択範囲を窓に描く
	fl := e.floatFrame(p)
	p.focused = focused
	fl.Fill = true
	var input *ui.Input
	var choice *ui.Choice
	if e.showsQuestion() && e.questionPane() == p {
		// このペインのキーから頼んだ拡張機能の問いは、このペインの画面にも出す（inputask.go の questionPane。PR #411 の裁定要 1）。
		if e.choices[0].input != nil {
			input = e.uiInput()
		} else {
			choice = e.uiChoice()
		}
	}
	return &ui.Frame{
		Input:            input,
		Choice:           choice,
		Name:             bufferTitle(bs),
		Modified:         bs.buf.Modified(),
		ReadOnly:         bs.buf.ReadOnly(),
		FileType:         bs.fileType(),
		Mode:             uiMode(st.Mode()),
		CommandLine:      st.CommandLine(),
		CommandLineType:  st.CommandLineType(),
		Completion:       e.wildMenuFrame(bs),
		CommandLinePopup: e.cmdlinePopup(),
		Recording:        st.Recording(),
		OperatorPending:  st.Mode() == command.ModeNormal && st.Pending(),
		Float:            fl,
	}
}
