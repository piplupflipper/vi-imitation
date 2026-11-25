package editor

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/ex"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// decorationSet は、1 つの拡張機能が 1 つのバッファに付けた構文の色の一覧である（decoration/set）。
type decorationSet struct {
	version     ext.Version
	decorations []ext.Decoration
	// shift は、decorations の行の番号から差し引く行数である。テキストの部品の先頭の行を捨てたとき（view/append。FR-272）に、
	// 一覧の全体を作り直さずに位置をずらすのに使う（viewappend.go）。差し引いて負になる範囲は、表を作るときに捨てるか切り詰める。
	// 拡張機能の decoration/set では 0 である。
	shift int
}

// outputKey は、出力の欄を見分ける、拡張機能と欄の名前の組である（output/append）。
type outputKey struct {
	ext, channel string
}

var noMessage = ui.Message{}

func infoMessage(text string) ui.Message { return ui.Message{Text: text, Level: ui.MessageInfo} }

func warnMessage(text string) ui.Message { return ui.Message{Text: text, Level: ui.MessageWarning} }

// syncChanges は、changes のうち、まだ exthost へ知らせていないものを知らせる（FR-16）。
func (e *Editor) syncChanges(bs *bufferState, changes []buffer.Change) {
	for _, ch := range changes {
		e.commitChange(bs, ch)
	}
}

// commitChange は、バッファに適用した変更 ch を、exthost へ差分で送り（FR-16）、診断と印をずらし、履歴に覚える。
// 既に知らせた変更（ch.After がバージョン番号の記録 sent 以下）は何もしない。
// マクロの再生の途中で問い合わせる前に送った変更が、Feed の結果にもう一度入っているためである。
//
// req: FR-16
func (e *Editor) commitChange(bs *bufferState, ch buffer.Change) {
	if ch.After <= bs.sent || ch.Before == ch.After {
		return
	}
	if err := e.host.ChangeBuffer(bs.id, ch); err != nil {
		e.log.Error("buffer/change を送れない", "buffer", bs.id, "err", err)
	}
	bs.sent = ch.After
	e.syncFinalNewline(bs)
	if e.host.Withheld(bs.id) {
		// 不正な UTF-8 が入り、拡張機能へ送らなくなった（基本設計 5.3）。拡張機能は新しい一覧を送ってこないので、古い表示のデータを消す。
		e.clearWithheld(bs)
	}
	for id, ds := range bs.diagnostics {
		bs.diagnostics[id] = shiftDiagnostics(ds, ch.Edits)
	}
	for id, ss := range bs.signs {
		bs.signs[id] = shiftSigns(ss, ch.Edits)
	}
	bs.shiftAllVirtualTexts(ch)
	e.shiftJumps(bs, ch)
	bs.history = append(bs.history, ch)
	if n := len(bs.history); n > maxHistory {
		bs.history = append([]buffer.Change(nil), bs.history[n-maxHistory:]...)
	}
}

// editsSince は、バージョン番号 v から現在までの編集を、適用した順に返す。
// v が覚えている変更の範囲より古いか、現在より新しければ ok が false である。
func (bs *bufferState) editsSince(v uint64) (eds []buffer.Edit, ok bool) {
	cur := bs.buf.Version()
	switch {
	case v == cur:
		return nil, true
	case v > cur:
		return nil, false
	}
	for i, ch := range bs.history {
		if ch.Before == v {
			for _, c := range bs.history[i:] {
				eds = append(eds, c.Edits...)
			}
			return eds, true
		}
	}
	return nil, false
}

// shiftPos は、位置 p を、編集 ed の後のテキストの位置へずらす（基本設計 6.7 の表示のデータ）。
//
// 編集の範囲より後ろの位置は、編集で増減した行数だけ動かし、編集の終わりと同じ行にあれば列のバイト数も動かす。
// 範囲の終わりと同じ位置は「後ろ」に数える。範囲と重なる位置（範囲の開始と同じで、終わりとは違う位置を含む）は、
// 編集の後のテキストの範囲の開始に寄せる。範囲より前の位置は動かさない。
func shiftPos(p buffer.Position, ed buffer.Edit) buffer.Position {
	s, end := ed.Range.Start, ed.Range.End
	switch {
	case p.Less(s):
		return p
	case p.Less(end):
		return s
	}
	nl := strings.Count(ed.Text, "\n")
	newEnd := buffer.Position{Line: s.Line + nl}
	if nl == 0 {
		newEnd.Byte = s.Byte + len(ed.Text)
	} else {
		newEnd.Byte = len(ed.Text) - strings.LastIndex(ed.Text, "\n") - 1
	}
	if p.Line == end.Line {
		return buffer.Position{Line: newEnd.Line, Byte: newEnd.Byte + p.Byte - end.Byte}
	}
	return buffer.Position{Line: p.Line + newEnd.Line - end.Line, Byte: p.Byte}
}

// shiftAll は、位置 p を編集の列 eds で順にずらす。
func shiftAll(p buffer.Position, eds []buffer.Edit) buffer.Position {
	for _, ed := range eds {
		p = shiftPos(p, ed)
	}
	return p
}

func shiftExtRange(r ext.Range, eds []buffer.Edit) ext.Range {
	return ext.Range{Start: toExtPos(shiftAll(fromExtPos(r.Start), eds)), End: toExtPos(shiftAll(fromExtPos(r.End), eds))}
}

// shiftDiagnostics は、診断の範囲を編集の列でずらした写しを返す。
func shiftDiagnostics(ds []ext.Diagnostic, eds []buffer.Edit) []ext.Diagnostic {
	out := make([]ext.Diagnostic, len(ds))
	for i, d := range ds {
		d.Range = shiftExtRange(d.Range, eds)
		out[i] = d
	}
	return out
}

// shiftSigns は、行の印を編集の列でずらした写しを返す。印は行の先頭の位置として扱う。
func shiftSigns(ss []ext.Sign, eds []buffer.Edit) []ext.Sign {
	out := make([]ext.Sign, len(ss))
	for i, s := range ss {
		s.Line = shiftAll(buffer.Position{Line: s.Line}, eds).Line
		out[i] = s
	}
	return out
}

// clearWithheld は、拡張機能へ送っていないバッファ bs（不正な UTF-8 を含むもの。基本設計 5.3）の、
// すべての拡張機能の色と診断と印を消す。
// 送らない間は拡張機能から新しい一覧が届かず、古い一覧が残ると、色は位置をずらさずに描くので行がずれて見える。
func (e *Editor) clearWithheld(bs *bufferState) {
	clear(bs.diagnostics)
	clear(bs.signs)
	bs.clearVirtualTexts()
	if len(bs.decorations) == 0 {
		return
	}
	for id := range bs.decorations {
		bs.removeDecorations(id)
	}
	e.rebuildDecorations(bs)
}

// withheldBuffer は、id のバッファを拡張機能へ送っていない（不正な UTF-8 を含む）かを返す。
// 送っていない間に届いた表示のデータは、送る前の版か、buffer/close と行き違ったものなので捨てる（基本設計 5.3）。
// 拡張機能を持たない Editor（表示のデータだけを試すテスト）では host が nil なので、送っていないものは無い。
func (e *Editor) withheldBuffer(id ext.BufferID) bool {
	return e.host != nil && e.host.Withheld(id)
}

// displayEdits は、拡張機能から届いた表示のデータ（診断、印）の一覧を持つかを、バージョン番号 v で決める（基本設計 6.7 の表）。
// 持つなら、v から現在までの編集（ずらしに使う）と true を返す。
//
// 現在より新しい v は、空の一覧でも捨てる（拡張機能の誤り）。
// 空の一覧（empty が true）は、v が覚えている編集の範囲より古くても受け付け、編集は返さない。
// 空の一覧はずらす位置を持たないので、どの版から計算しても結果は同じだからである。
// 捨てると、拡張機能は空の一覧を送り直さないので、古い一覧が残り続ける
// （#267。:set filetype=text の直後にマクロで 100 を超える編集が続くと、LSP の拡張機能が送る空の診断が古い版になった）。
// 空でない一覧は、覚えている編集の範囲より古ければ捨てる（editsSince）。
//
// req: FR-30
func (bs *bufferState) displayEdits(v ext.Version, empty bool) ([]buffer.Edit, bool) {
	if uint64(v) > bs.buf.Version() {
		return nil, false
	}
	if empty {
		return nil, true
	}
	return bs.editsSince(uint64(v))
}

// setDiagnostics は、diagnostic/set の一覧を、バージョン番号に応じてずらすか捨てて持つ（displayEdits、基本設計 6.7 の表）。
// 空の一覧はその組の一覧を消す。持ったかを返す。拡張機能へ送っていないバッファの一覧は捨てる（withheldBuffer）。
//
// req: FR-30
func (e *Editor) setDiagnostics(extID string, p ext.DiagnosticSetParams) bool {
	bs := e.findBuffer(p.Buffer)
	if bs == nil || e.withheldBuffer(p.Buffer) {
		return false
	}
	eds, ok := bs.displayEdits(p.Version, len(p.Diagnostics) == 0)
	if !ok {
		return false
	}
	if len(p.Diagnostics) == 0 {
		delete(bs.diagnostics, extID)
		return true
	}
	bs.diagnostics[extID] = shiftDiagnostics(p.Diagnostics, eds)
	return true
}

// setSigns は、sign/set の一覧を、diagnostic/set と同じ規則で持つ（displayEdits）。
func (e *Editor) setSigns(extID string, p ext.SignSetParams) bool {
	bs := e.findBuffer(p.Buffer)
	if bs == nil || e.withheldBuffer(p.Buffer) {
		return false
	}
	eds, ok := bs.displayEdits(p.Version, len(p.Signs) == 0)
	if !ok {
		return false
	}
	if len(p.Signs) == 0 {
		delete(bs.signs, extID)
		return true
	}
	bs.signs[extID] = shiftSigns(p.Signs, eds)
	return true
}

// setDecorations は、decoration/set の一覧を持つ。現在より新しいバージョン番号の一覧は捨てる。
// 基本設計 6.1 のとおり、古い一覧も置き換えずに持ち、新しい一覧が届くまで前の色のまま描く（位置はずらさない）。
// 描画に使う表は、イベントループの外で作り直す（rebuildDecorations）。
// 拡張機能へ送っていないバッファの一覧は捨てる（withheldBuffer）。
func (e *Editor) setDecorations(extID string, p ext.DecorationSetParams) {
	bs := e.findBuffer(p.Buffer)
	if bs == nil || uint64(p.Version) > bs.buf.Version() || e.withheldBuffer(p.Buffer) {
		return
	}
	if len(p.Decorations) == 0 {
		bs.removeDecorations(extID)
	} else {
		bs.decorations[extID] = decorationSet{version: p.Version, decorations: p.Decorations}
	}
	e.rebuildDecorations(bs)
}

// removeDecorations は、拡張機能 extID の一覧を消し、その世代の番号を進める。
// 消す前の一覧から作り始めた表を、同じ拡張機能が一覧を送り直した後に受け取っても、世代の違いで使わずに済む。
func (bs *bufferState) removeDecorations(extID string) {
	if _, ok := bs.decorations[extID]; !ok {
		return
	}
	delete(bs.decorations, extID)
	if bs.decoGen == nil {
		bs.decoGen = map[string]uint64{}
	}
	bs.decoGen[extID]++
}

// decoSource は、表を作る元にした、1 つの拡張機能の一覧である。
type decoSource struct {
	ext  string
	decs []ext.Decoration
	// shift は、decorationSet.shift である。
	shift int
	// gen は、作り始めたときの、この拡張機能の一覧の世代の番号（bufferState.decoGen）である。
	gen uint64
}

// decoBuilt は、イベントループの外で作り終えた表である。
type decoBuilt struct {
	bs *bufferState
	// buf と src は、作り始めたときの bs.buf と一覧である。届いたときにまだ現在のものかを確かめる。
	buf  *buffer.Buffer
	src  []decoSource
	view *ui.Decorations
}

// decoSources は、拡張機能ごとの一覧を、拡張機能の ID の順に並べて返す。
// 重なった範囲は後ろのものほど上に描くので、並べる順を決めておく。
func (bs *bufferState) decoSources() []decoSource {
	src := make([]decoSource, 0, len(bs.decorations))
	for id, ds := range bs.decorations {
		src = append(src, decoSource{ext: id, decs: ds.decorations, shift: ds.shift, gen: bs.decoGen[id]})
	}
	sort.Slice(src, func(i, j int) bool { return src[i].ext < src[j].ext })
	return src
}

// sameSources は、a と b が同じ拡張機能の同じ一覧（同じスライス）からなるかを返す。
// 一覧は受け取った後に変えないので、スライスの先頭の位置と長さで比べれば足りる。
func sameSources(a, b []decoSource) bool {
	return slices.EqualFunc(a, b, func(x, y decoSource) bool {
		return x.ext == y.ext && x.gen == y.gen && x.shift == y.shift && len(x.decs) == len(y.decs) && (len(x.decs) == 0 || &x.decs[0] == &y.decs[0])
	})
}

// rebuildDecorations は、拡張機能ごとの一覧を合わせて、描画に渡す ui.Decorations を作り直す。
//
// 表は別の goroutine で作り、作り終えたら e.decoDone からイベントループへ渡す（decorationsBuilt）。
// 10 万行の JSON の一覧（80 万の範囲）で、表を作るのに数十 ms かかり、その間キー入力を処理できなかったためである（#74）。
// 別の goroutine は、受け取った後に変えない一覧のスライスを読むだけで、バッファの状態には触らない（基本設計 2.1）。
// 1 つのバッファで同時に作るのは 1 つまでにし、作っている間に一覧が変わったら、作り終えた後にもう一度作る。
// 作り終えるまでは、前の表で描く。一覧が空になったときだけは、待たずに色を消す。
func (e *Editor) rebuildDecorations(bs *bufferState) {
	if len(bs.decorations) == 0 {
		bs.decoView = nil
		return
	}
	if bs.decoBuilding {
		return
	}
	bs.decoBuilding = true
	r := decoBuilt{bs: bs, buf: bs.buf, src: bs.decoSources()}
	lines := bs.buf.LineCount()
	done, stop := e.decoDone, e.stop
	go func() {
		defer e.recoverPanic()
		r.view = buildDecorations(r.src, lines)
		select {
		case done <- r:
		case <-stop:
		}
	}()
}

// buildDecorations は、src の一覧を合わせた、lines 行のテキストの ui.Decorations を作る。イベントループの外で呼ぶ。
// 範囲に直に付けた色（ext.Decoration.Attr。FR-230）は、名前の色の上に重ねる色（ui.Highlight.Attr）にする。
// 表は 2 回なめる（ui.NewDecorationsSeq）ので、色を読んだ結果は 1 回目に覚えて 2 回目にも使う。
//
// req: FR-230
func buildDecorations(src []decoSource, lines int) *ui.Decorations {
	attrs := map[*ext.TextAttr]*ui.Attr{}
	return ui.NewDecorationsSeq(func(yield func(ui.Highlight) bool) {
		for _, s := range src {
			for _, d := range s.decs {
				if s.shift > 0 {
					if d.Range.End.Line < s.shift {
						continue
					}
					if d.Range.Start.Line < s.shift {
						d.Range.Start = ext.Position{Line: s.shift}
					}
					d.Range.Start.Line -= s.shift
					d.Range.End.Line -= s.shift
				}
				h := ui.Highlight{Range: fromExtRange(d.Range), Style: d.Style}
				if d.Attr != nil {
					h.Attr = cachedAttr(attrs, d.Attr)
				}
				if !yield(h) {
					return
				}
			}
		}
	}, lines)
}

// decorationsBuilt は、別の goroutine で作り終えた表を受け取る。
// 作っている間に一覧が変わっていなければ、そのまま描画に使う。
// 変わっていれば、もう一度作る。そのとき、作り終えた表が今の表より新しい一覧から作ったもので、
// 作り始めた後に消した一覧を含まず（拡張機能ごとの世代の番号が同じ）、バッファを読み込み直してもいなければ、
// 作り直しを待つ間はそれで描く。一覧が届き続けても、色が古いまま止まらないようにするためである。
// 消した後に同じ拡張機能が一覧を送り直しても、消す前の一覧から作った表は世代の番号が違うので使わない。
func (e *Editor) decorationsBuilt(r decoBuilt) {
	bs := r.bs
	bs.decoBuilding = false
	if len(bs.decorations) == 0 {
		return
	}
	cur := bs.decoSources()
	if sameSources(r.src, cur) {
		bs.decoView = r.view
		return
	}
	if r.buf == bs.buf && !slices.ContainsFunc(r.src, func(s decoSource) bool {
		_, ok := bs.decorations[s.ext]
		return !ok || bs.decoGen[s.ext] != s.gen
	}) {
		bs.decoView = r.view
	}
	e.rebuildDecorations(bs)
}

// clearExtension は、異常で終わった拡張機能の表示のデータを、すべてのバッファから消す（基本設計 6.7）。
// ステータス行の部品の中身と、窓の上端の帯（window.go。FR-237）も消す。進み具合は、状態の変化を取っておいたかで消す時が違うので、呼び出し側（stateChange）が消す。
func (e *Editor) clearExtension(extID string) {
	e.clearStatusParts(extID)
	e.clearBands(extID)
	for _, bs := range e.bufs {
		delete(bs.diagnostics, extID)
		delete(bs.signs, extID)
		bs.removeVirtualTexts(extID)
		if _, ok := bs.decorations[extID]; ok {
			bs.removeDecorations(extID)
			e.rebuildDecorations(bs)
		}
	}
}

// appendOutput は、出力の欄へテキストを書き足す。行数が上限を超えたら古い行から捨てる。
// 書き足した欄を、名前を指定せずに開いた欄（outputView.key が空）に出す欄にする。
// 上へスクロールして見ている欄の行を捨てたら、見ている位置が同じ行を指し続けるように番号をずらす。
func (e *Editor) appendOutput(extID string, p ext.OutputAppendParams) {
	key := outputKey{ext: extID, channel: p.Channel}
	lines, ok := e.outputs[key]
	if !ok {
		e.outputOrder = append(e.outputOrder, key)
	}
	e.outputLast = key
	parts := strings.Split(p.Text, "\n")
	if n := len(lines); n > 0 {
		last := lines[n-1] + parts[0]
		lines = lines[:n-1]
		parts[0] = last
	}
	for _, part := range parts {
		lines = append(lines, splitLongLine(part)...)
	}
	if n := len(lines); n > maxOutputLines {
		dropped := n - maxOutputLines
		lines = append([]string(nil), lines[dropped:]...)
		// 欄は client ごとに開くので、それぞれの見ている位置をずらす。
		e.eachView(func(*clientView) {
			if v := e.outView; v != nil && v.bottom >= 0 && e.shownOutput() == key {
				if v.bottom -= dropped; v.bottom <= 0 {
					v.bottom, v.skip = 0, 0 // 見ていた行が捨てられた。欄は先頭の行から出す
				}
			}
		})
	}
	e.outputs[key] = lines
}

// splitLongLine は、s を maxOutputLineBytes バイト以下の行に分ける。分ける位置は UTF-8 の文字の境目にする。
// 改行の無い出力で 1 行が際限なく伸び、描画のたびの折り返しが重くなるのを避ける（NFR-03）。
func splitLongLine(s string) []string {
	var out []string
	for len(s) > maxOutputLineBytes {
		i := maxOutputLineBytes
		for i > 0 && !utf8.RuneStart(s[i]) {
			i--
		}
		if i == 0 {
			i = maxOutputLineBytes // 文字の境目が無い（不正な UTF-8）なら、バイトの数で分ける
		}
		out = append(out, s[:i])
		s = s[i:]
	}
	return append(out, s)
}

// outputView は、開いている出力の欄の表示の状態である。
type outputView struct {
	// key は、利用者が名前で選んだ欄（または上へスクロールした時点の欄）である。空なら、最後に書き足された欄を出す。
	key outputKey
	// bottom と skip は、ui.Output.Bottom と Skip である。欄の下の端は、bottom の手前の行を折り返した画面の行の、
	// 下から skip 行を除いたところにある。bottom が負なら末尾に追従する。
	// skip は、bottom の手前の行の画面の行数より小さく保つ（normalize）。行を書き足しても、見ている位置が動かない。
	bottom, skip int
}

// shownLines は、開いている欄の行を、描画と同じく改行で終わった後の空の行を除いて返す。
func (e *Editor) shownLines() []string {
	lines := e.outputs[e.shownOutput()]
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// scrollOutput は、開いている欄を、画面の行で delta 行だけ動かす（正は上、負は下）。
// 画面の行は ui.Renderer.OutputLineRows で、直前の描画の幅で折り返して数えるので、長い行の途中でも止まり、先頭の行まで戻れる。
// 上の端より上へは動かさず、下の端まで戻したら末尾に追従する。
func (e *Editor) scrollOutput(delta, rows int) {
	v := e.outView
	lines := e.shownLines()
	n := len(lines)
	if v.bottom < 0 || v.bottom > n {
		v.bottom, v.skip = n, 0
	}
	rowsOf := func(i int) int { return e.renderer.OutputLineRows(lines[i]) }
	v.skip += delta
	// 下へ: skip が負なら、bottom を下の行へ進める。
	for v.skip < 0 {
		if v.bottom >= n {
			v.bottom, v.skip = -1, 0
			return
		}
		v.bottom++
		v.skip += rowsOf(v.bottom - 1)
	}
	// 上へ: skip を bottom の手前の行の画面の行数より小さくする。
	for v.bottom > 0 && v.skip >= rowsOf(v.bottom-1) {
		v.skip -= rowsOf(v.bottom - 1)
		v.bottom--
	}
	// 欄の上の端が先頭の行の 1 行目より上へ出るなら、欄の上の端が先頭の行の 1 行目に来る位置（先頭から rows 行目が下の端）へ寄せる。
	above := 0 // bottom の手前の行の画面の行数の合計（rows+skip に達したら数えるのをやめる）
	for i := v.bottom - 1; i >= 0 && above < rows+v.skip; i-- {
		above += rowsOf(i)
	}
	if above < rows+v.skip {
		acc := 0
		v.bottom, v.skip = -1, 0
		for i := range n {
			acc += rowsOf(i)
			if acc >= rows {
				v.bottom, v.skip = i+1, acc-rows
				break
			}
		}
	}
	if v.bottom >= n && v.skip == 0 {
		v.bottom = -1
	}
}

// shownOutput は、開いている欄に出す欄を返す。欄を開いていないか、出す欄がまだ無ければ空である。
func (e *Editor) shownOutput() outputKey {
	if e.outView == nil {
		return outputKey{}
	}
	if e.outView.key != (outputKey{}) {
		return e.outView.key
	}
	return e.outputLast
}

// outputTitle は、欄の名前を返す。拡張機能の ID と欄の名前が同じなら（acp の acp）欄の名前だけ、違えば "拡張機能/欄" にする。
// :output の引数には、どちらの形も書ける（findOutput）。
func outputTitle(k outputKey) string {
	if k == (outputKey{}) {
		return "(まだ出力はありません)"
	}
	if k.ext == k.channel {
		return k.channel
	}
	return k.ext + "/" + k.channel
}

// findOutput は、:output の引数の名前の欄を返す。名前は欄の名前か "拡張機能/欄" である。
// 欄の名前が同じ欄が複数の拡張機能にあれば、先に書き足された欄を返す。
func (e *Editor) findOutput(name string) (outputKey, bool) {
	for _, k := range e.outputOrder {
		if k.channel == name || k.ext+"/"+k.channel == name {
			return k, true
		}
	}
	return outputKey{}, false
}

// outputAction は、出力の欄の動作（:output、:outclose、<C-w>P などのキー。ex.ActionOutput）を行う（基本設計 6.7）。
//
// 欄はバッファではなく、カーソルは入らない。読むためのスクロールは <C-w><PageUp> と <C-w><PageDown> で、
// 欄の高さの半分の画面の行ずつ動かす（scrollOutput）。下の端までスクロールすると、また末尾に追従する。
// 名前を省いて開いた欄は、上へスクロールした時点の欄に固定する。
// DAP のデバッグ対象の出力（6.4）と ACP のエージェントの返答（6.5）は、この欄で読む。
//
// req: FR-31, FR-34
func (e *Editor) outputAction(a ex.Action) error {
	switch a.Output {
	case ex.OutputOpen:
		v := &outputView{bottom: -1}
		if a.Text != "" {
			k, ok := e.findOutput(a.Text)
			if !ok {
				return fmt.Errorf("出力の欄 %s はありません", a.Text)
			}
			v.key = k
		}
		e.outView = v
	case ex.OutputClose:
		e.outView = nil
	case ex.OutputNext:
		if e.outView == nil {
			e.outView = &outputView{bottom: -1}
			return nil
		}
		cur := e.shownOutput()
		next := cur
		if i := slices.Index(e.outputOrder, cur); i >= 0 {
			next = e.outputOrder[(i+1)%len(e.outputOrder)]
		}
		e.outView = &outputView{key: next, bottom: -1}
	case ex.OutputScrollUp, ex.OutputScrollDown:
		v := e.outView
		rows := e.renderer.OutputRows()
		if v == nil || rows == 0 {
			return nil
		}
		if v.key == (outputKey{}) {
			// 名前を省いて開いた欄は、読み返している間に別の欄へ書き足されても替えないように、今の欄に固定する。
			v.key = e.shownOutput()
		}
		half := max(rows/2, 1)
		if a.Output == ex.OutputScrollDown {
			half = -half
		}
		e.scrollOutput(half, rows)
	}
	return nil
}

// uiOutput は、開いている出力の欄を描画の形にする。欄を開いていなければ nil である。
func (e *Editor) uiOutput() *ui.Output {
	if e.outView == nil {
		return nil
	}
	k := e.shownOutput()
	return &ui.Output{Title: outputTitle(k), Lines: e.outputs[k], Bottom: e.outView.bottom, Skip: e.outView.skip}
}

// severityRank は、診断の重要度の順位である。大きいほど重い。
var severityRank = map[ext.DiagnosticSeverity]int{
	ext.SeverityError: 4, ext.SeverityWarning: 3, ext.SeverityInfo: 2, ext.SeverityHint: 1,
}

// cursorDiagnostic は、カーソルの行の診断の本文を、メッセージの行に出す形で返す。無ければ ok が false である。
//
// 診断の本文（diagnostic/set の message）は、下線と印だけでは読めないので、カーソルをその行へ動かしたときに
// メッセージの行へ出す（Vim の ALE や Neovim の vim.diagnostic.open_float に当たる表示。名前は仮。#132）。
// 行に複数の診断があれば、カーソルの位置を範囲に含むものを先に、その中で重いものを選ぶ。
// 行に掛かるかは、ポップアップ（FR-171）と同じ diagnosticSpanOf で決める（行の先頭で終わる範囲は、終わりの行に掛からない）。
// 本文は "E gopls: 本文" の形で、先頭の文字は印の列の表記と同じ重要度の頭文字である。
//
// req: FR-30
func (bs *bufferState) cursorDiagnostic(cur ext.Position) (ui.Message, bool) {
	var best *ext.Diagnostic
	bestKey := [2]int{}
	for _, id := range slices.Sorted(maps.Keys(bs.diagnostics)) {
		for i := range bs.diagnostics[id] {
			d := &bs.diagnostics[id][i]
			span := diagnosticSpanOf(d.Range)
			s, e := span.r.Start, span.r.End
			if cur.Line < span.first || cur.Line > span.last {
				continue
			}
			covers := 0
			afterStart := cur.Line > s.Line || cur.Byte >= s.Byte
			beforeEnd := cur.Line < e.Line || cur.Byte < e.Byte || (s == e && cur.Byte == e.Byte)
			if afterStart && beforeEnd {
				covers = 1
			}
			key := [2]int{covers, severityRank[d.Severity]}
			if best == nil || key[0] > bestKey[0] || (key[0] == bestKey[0] && key[1] > bestKey[1]) {
				best, bestKey = d, key
			}
		}
	}
	if best == nil {
		return ui.Message{}, false
	}
	// 本文の空白の並び（改行と、続く行の頭のタブを含む）は、空白 1 つに詰めて 1 行にする。gopls の型検査の診断は
	// "not enough arguments in call to f\n\thave ()\n\twant (int)" のように改行を含み、そのまま出すと
	// メッセージの行が上へ広がり、カーソルを動かすたびにステータス行とカーソルの行を覆う（PR #134 のレビュー）。
	text := strings.Join(strings.Fields(best.Message), " ")
	if best.Source != "" {
		text = best.Source + ": " + text
	}
	sev := strings.ToUpper(string(best.Severity))
	if sev != "" {
		text = sev[:1] + " " + text
	}
	level := ui.MessageInfo
	if severityRank[best.Severity] >= severityRank[ext.SeverityWarning] {
		// error も警告の色にする。カーソルを動かすたびに出る表示なので、操作の失敗（赤の背景）と見分ける。
		level = ui.MessageWarning
	}
	return ui.Message{Text: text, Level: level}, true
}

// uiSigns は、バッファの行の印を描画の形にする。1 つの行の印の優先順位は ui が決める。
func (bs *bufferState) uiSigns() []ui.Sign {
	var out []ui.Sign
	for _, id := range slices.Sorted(maps.Keys(bs.signs)) {
		for _, s := range bs.signs[id] {
			out = append(out, ui.Sign{Line: s.Line, Kind: ui.SignKind(s.Kind)})
		}
	}
	return out
}

// uiDiagnostics は、バッファの診断を描画の形にする。
func (bs *bufferState) uiDiagnostics() []ui.Diagnostic {
	var out []ui.Diagnostic
	ids := make([]string, 0, len(bs.diagnostics))
	for id := range bs.diagnostics {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, d := range bs.diagnostics[id] {
			out = append(out, ui.Diagnostic{Range: fromExtRange(d.Range), Severity: ui.Severity(d.Severity)})
		}
	}
	return out
}
