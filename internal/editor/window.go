package editor

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 窓の変化の知らせ（window/change。基本設計 6.7 の「窓の変化の知らせ」。FR-235、FR-236）と、窓の上端の帯（band/set。FR-237〜FR-239）。
//
// # 窓の変化の知らせ
//
// Neovim の WinScrolled と CursorMoved に当たる契機である。カーソルが止まった契機（cursorhold.go）には載せない
// （PR #333 のレビューの裁定要へのオーケストレータの決定）。キーでない出来事（gd の応答での移動、端末の大きさの変更、
// 別の client や拡張機能の編集）でも、描いた結果が変われば知らせる。
//
//   - 窓は、編集の client の画面のバッファの表示領域で、client ごとに 1 つである（ext.WindowID は "client/<client の番号>"）。
//     分割したペインの client も、見比べのために分割したペインの client も、別の client なので別の窓である。
//     ポップアップの中のバッファと、部品の client の画面は知らせない
//   - 中身（ext.WindowChangeParams）は、描画の後に、その client の描画の部品（ui.Renderer）の最初の行、最後まで描いた行の次の行
//     （Botline）、表示領域の大きさと、カーソルのあるタブ（フォーカスのあるポップアップを開いていれば、開いたときのタブ）の
//     バッファ、バージョン番号、カーソル、モード、見比べの片側かから作る
//   - 送る相手は、宣言ファイルの windowEvents が true で稼働中の拡張機能だけである（exthost の WindowEventExtensions）
//   - 描くたびに（render の最後。notifyWindows）、拡張機能と窓の組ごとに前に送った中身（windowNotifier.sent）と比べ、違う窓だけを送る。
//     状態の変化で送るので、どの client のキーか（ペインの部品の client のキーで元の client のタブが動いたときを含む）によらない
//   - 送ったら ext.WindowNotifyIntervalMs のミリ秒の間は送らず、その間に描いたら、時間が来た時点で比べ直して違う窓を送る
//     （nvim-treesitter-context の throttle_by_id と同じく、続けて変わっても、その時間のうちに 2 回までにする）。
//     時間の待ちは、一時的な色付け（flash.go）と同じく、イベントループの外のタイマー（Editor.afterFunc）で行う
//   - client が終わった窓には、Closed を true にした知らせを送る。拡張機能の状態が変わったら（再起動を含む）、その拡張機能へ送った
//     中身を忘れ、次に稼働中になったときにすべての窓を送り直す
//
// # 窓の上端の帯
//
// 拡張機能が band/set で送った帯を、窓と拡張機能の組ごとに持ち（Editor.bands）、その窓の client の画面（bufferFrame）にだけ
// 重ねて描く。窓のバッファが帯の Buffer と違えば描かない。1 つの窓の帯は、拡張機能の ID の順に上から並べる。
// 帯を作ったときから本文が変わった（バージョン番号が違う）帯の行は、描くが、押してもその行へ移らない（ui.BandLine.Stale）。
// 拡張機能が止まったら（clearExtension）、その拡張機能の帯を消す。client が終わったら（removeClient）、その窓の帯を消す。
// 帯の行数をカーソルの行で抑えることと、帯を押したときの移動は、描画（ui.Renderer.drawBand、HitTest）と
// 操作（internal/command の mouseBandJump）が受け持つ。

// windowNotifyInterval は、window/change を続けて送るときの間隔の下限である（ext.WindowNotifyIntervalMs）。
const windowNotifyInterval = ext.WindowNotifyIntervalMs * time.Millisecond

// windowNotifier は、窓の変化の知らせを送った中身と、間引きの状態である（Editor.windows）。
type windowNotifier struct {
	// sent は、拡張機能の ID ごとの、窓ごとに最後に送った中身である。
	sent map[string]map[ext.WindowID]ext.WindowChangeParams
	// armed は、送った後の間引きの時間の途中であることを表す。dirty は、その間に描いたことを表す（時間が来たら比べ直す）。
	armed, dirty bool
	// gen は、間引きのタイマーの世代である。止めた後に来たタイマーを捨てる。
	gen uint64
	// exts と send は、送る相手の一覧と送り方である。nil なら exthost の WindowEventExtensions と Notify を使う。
	// テストが、拡張機能のプロセスを起動せずに送ったものを確かめるために差し替える。
	exts func() []string
	send func(extID string, p ext.WindowChangeParams) error
}

// windowIDOf は、client c の窓の識別子を返す。
func windowIDOf(c *clientView) ext.WindowID {
	return ext.WindowID(fmt.Sprintf("client/%d", c.info.ID))
}

// windowExtensions は、window/change を送る相手の拡張機能の ID を返す。
func (e *Editor) windowExtensions() []string {
	if f := e.windows.exts; f != nil {
		return f()
	}
	return e.host.WindowEventExtensions()
}

// sendWindow は、拡張機能 extID へ window/change の p を送る。
func (e *Editor) sendWindow(extID string, p ext.WindowChangeParams) {
	send := e.windows.send
	if send == nil {
		send = func(id string, p ext.WindowChangeParams) error { return e.host.Notify(id, ext.MethodWindowChange, p) }
	}
	if err := send(extID, p); err != nil {
		e.log.Warn("window/change を送れない", "extension", extID, "err", err)
	}
}

// notifyWindows は、描いた後に、窓の変化を知らせる（このファイルの doc）。間引きの時間の途中なら、時間が来るまで送らない。
//
// req: FR-235, FR-236
func (e *Editor) notifyWindows() {
	w := &e.windows
	if w.armed {
		w.dirty = true
		return
	}
	if e.flushWindows() {
		e.armWindows()
	}
}

// armWindows は、間引きの時間のタイマーを掛ける。
func (e *Editor) armWindows() {
	w := &e.windows
	w.armed, w.dirty = true, false
	w.gen++
	gen, stop := w.gen, e.stop
	e.afterFunc(windowNotifyInterval, func() {
		select {
		case e.inspect <- func() { e.windowsDue(gen) }:
		case <-stop:
		}
	})
}

// windowsDue は、間引きの時間が来たことである。その間に描いていれば比べ直して送り、送ったら次の間引きの時間を掛ける。
//
// req: FR-236
func (e *Editor) windowsDue(gen uint64) {
	w := &e.windows
	if gen != w.gen || !w.armed {
		return
	}
	w.armed = false
	if !w.dirty {
		return
	}
	w.dirty = false
	if e.flushWindows() {
		e.armWindows()
	}
}

// flushWindows は、拡張機能と窓の組ごとに、今の中身が前に送った中身と違えば送り、無くなった窓には閉じた知らせを送る。
// 何か送ったら true を返す。送る相手でなくなった（稼働中でなくなった、windowEvents を持たない）拡張機能の送った中身は忘れる。
//
// req: FR-235, FR-236
func (e *Editor) flushWindows() bool {
	w := &e.windows
	exts := e.windowExtensions()
	for id := range w.sent {
		if !slices.Contains(exts, id) {
			delete(w.sent, id)
		}
	}
	if len(exts) == 0 {
		return false
	}
	snaps := e.windowSnapshots()
	ids := slices.Sorted(maps.Keys(snaps))
	sent := false
	for _, x := range exts {
		m := w.sent[x]
		if m == nil {
			m = map[ext.WindowID]ext.WindowChangeParams{}
			if w.sent == nil {
				w.sent = map[string]map[ext.WindowID]ext.WindowChangeParams{}
			}
			w.sent[x] = m
		}
		for _, id := range ids {
			p := snaps[id]
			if old, ok := m[id]; ok && old == p {
				continue
			}
			m[id] = p
			e.sendWindow(x, p)
			sent = true
		}
		for _, id := range slices.Sorted(maps.Keys(m)) {
			if _, ok := snaps[id]; !ok {
				delete(m, id)
				e.sendWindow(x, ext.WindowChangeParams{Window: id, Closed: true})
				sent = true
			}
		}
	}
	return sent
}

// windowSnapshots は、繋いでいる編集の client ごとの、窓の今の中身を返す。まだ描いていない client の窓は入れない。
func (e *Editor) windowSnapshots() map[ext.WindowID]ext.WindowChangeParams {
	out := map[ext.WindowID]ext.WindowChangeParams{}
	e.eachView(func(c *clientView) {
		r := e.renderer
		if e.cur == nil || r == nil || r.Width <= 0 || r.Height <= 0 {
			return
		}
		bs := e.tab()
		s, _ := e.diffOf(bs)
		id := windowIDOf(c)
		out[id] = ext.WindowChangeParams{
			Window: id, Buffer: bs.id, Version: ext.Version(bs.buf.Version()),
			Top: r.Top(), Bottom: r.Botline(), Rows: r.TextRows(), Columns: r.TextWidth(),
			Cursor: toExtPos(bs.state.Cursor()), Mode: modeName(e.cur.state.Mode()), Diff: s != nil,
		}
	})
	return out
}

// forgetWindows は、拡張機能 extID へ送った窓の中身を忘れる。次に送る相手になったら、すべての窓を送り直す（stateChange）。
func (e *Editor) forgetWindows(extID string) {
	delete(e.windows.sent, extID)
}

// setBand は、拡張機能 extID が band/set で送った帯を、窓と拡張機能の組の帯にする。行が空なら、その組の帯を消す。
// 繋いでいる client の窓でなければ捨てる（終わった client の窓への遅れた知らせ）。
//
// req: FR-237
func (e *Editor) setBand(extID string, p ext.BandSetParams) {
	if len(p.Lines) == 0 {
		if m := e.bands[p.Window]; m != nil {
			delete(m, extID)
			if len(m) == 0 {
				delete(e.bands, p.Window)
			}
		}
		return
	}
	if !slices.ContainsFunc(e.clients, func(c *clientView) bool { return !c.detached && windowIDOf(c) == p.Window }) {
		return
	}
	if e.bands == nil {
		e.bands = map[ext.WindowID]map[string]ext.BandSetParams{}
	}
	m := e.bands[p.Window]
	if m == nil {
		m = map[string]ext.BandSetParams{}
		e.bands[p.Window] = m
	}
	m[extID] = p
}

// clearBands は、拡張機能 extID の帯をすべて消す（clearExtension）。
func (e *Editor) clearBands(extID string) {
	for id, m := range e.bands {
		delete(m, extID)
		if len(m) == 0 {
			delete(e.bands, id)
		}
	}
}

// uiBand は、読み込んでいる client の窓の帯のうち、バッファ bs の帯を、拡張機能の ID の順に並べた描画の入力にする（ui.Frame.Band）。
// 無ければ nil を返す。帯を作ったときのバージョン番号が今と違う帯の行は、押してもその行へ移らない印（Stale）を付ける。
//
// req: FR-237, FR-238
func (e *Editor) uiBand(bs *bufferState) *ui.Band {
	if e.view == nil {
		return nil
	}
	m := e.bands[windowIDOf(e.view)]
	if len(m) == 0 {
		return nil
	}
	var lines []ui.BandLine
	for _, x := range slices.Sorted(maps.Keys(m)) {
		p := m[x]
		if p.Buffer != bs.id {
			continue
		}
		stale := p.Version != ext.Version(bs.buf.Version())
		for _, l := range p.Lines {
			bl := ui.BandLine{Text: l.Text, Line: -1, Stale: stale}
			if l.Line != nil {
				bl.Line = *l.Line
			}
			for _, h := range l.Highlights {
				bl.Spans = append(bl.Spans, ui.BandSpan{Start: h.Start, End: h.End, Style: h.Style})
			}
			lines = append(lines, bl)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return &ui.Band{Lines: lines}
}
