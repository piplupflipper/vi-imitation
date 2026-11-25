package editor

import (
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
)

// テキストの部品の末尾への書き足し（view/append。基本設計 1.4 の「#399 の切り替えの部品と書き足し」の「末尾への書き足し」。FR-272）。

// appendLostNote は、本体が拡張機能の応答を待つ間に取っておく列が上限を超え、書き足しを捨てた部品の末尾に足す行である
// （基本設計 1.4 の「待ちの間」。仮）。拡張機能の写し（ext/server の TextLog）は欠けないので、閉じて開き直せば戻る。
const appendLostNote = "（書き足しの一部を捨てました）"

// viewAppend は、拡張機能の view/append を処理する。テキストの部品と、書き足すテキストと入力の行の組の出力（FR-274）に書き足す。
// 知らない部品、閉じた部品、それ以外の部品への書き足しと、切り替えの部品の子（Child）を指す書き足しは捨てる
// （切り替えの部品はまだ無いので、子はどれも知らない子である）。
func (e *Editor) viewAppend(ev *exthost.ViewAppend) {
	ev.Flush() // 取っておく列でつないだ中身を params に入れる
	if ev.Params.Child != "" {
		return
	}
	c, f := e.findViewFloat(ev.Extension, ev.Params.View)
	if f == nil || !f.appendable() {
		return
	}
	e.withView(c, func() { e.appendFloatText(f, ev.Params.Text, ev.Params.Highlights) })
}

// appendFloatText は、テキストの部品 f の中身（書き足すテキストと入力の行の組なら出力。floatState.textBuf）の末尾へ
// text（その中の色の範囲が spans）を書き足す。組の入力の行の中身、カーソル、モードは変えない（FR-274）。
//
// 行の分け方と古い行の捨て方は ext.AppendViewText と ext.ViewTextOverflow で決め、拡張機能の写し（TextLog）とそろえる。
// 中身は、アンドゥの履歴に積まない本体の中の書き込み（buffer.Buffer.ApplyUnrecorded）で替える。編集の禁止の判定は通らない。
//
// 書き足す前にカーソルが最後の行にあれば、書き足した後の最後の行へカーソルを動かす（末尾に追従する）。
// 最後の行より上にあれば、カーソルと見ている行を動かさない。古い行を捨てたときは、カーソルの行と窓の最初の行を捨てた行数だけずらし、
// カーソルの行が捨てた範囲にあれば最初の行に置く。
// ビジュアルモードの間と、キーの列の途中（オペレーターの後のモーションを待つ間など）は、最後の行にあっても追従しない。
// 選択とオペレーターの範囲が、書き足した分だけ広がらないようにするためである（PR #405 のレビューの推奨 2）。
// そのときは、止める前にカーソルが最後の行にあったことを覚え、抜けた時点でカーソルがまだその行にあれば、最後の行へ戻して追従を続ける
// （resumeFollow。PR #405 の再レビューの推奨へのオーケストレータの決め）。利用者が自分で上へ動かしたときだけ、追従を止めたままにする。
//
// 色の範囲は、部品の一覧（decorationSet）の後ろへ足し、先頭の行を捨てたときは一覧を作り直さずに shift を増やす。
// 1 件の手間が、部品の色の範囲の数でなく、書き足した量と捨てた範囲の数に比例するようにするためである（同じレビューの推奨 3）。
//
// req: FR-272, FR-274
func (e *Editor) appendFloatText(f *floatState, text string, spans []ext.ViewSpan) {
	bs := f.textBuf()
	b := bs.buf
	lastIdx := b.LineCount() - 1
	last := b.Line(lastIdx)
	e.resumeFollow(f) // 抜けた後の最初の書き足しより前に、キーを経ずに抜けていた（時間切れなど）ときも戻す
	cursor := bs.state.Cursor()
	atTail := cursor.Line >= lastIdx
	follow := atTail && !holdingFollow(bs)
	if atTail && !follow && !f.followHeld {
		f.followHeld, f.heldLine = true, cursor.Line
	}
	ds := bs.decorations[popupDecorationOwner]
	decs, shift := ds.decorations, ds.shift
	if !f.decsOwned {
		// 開いたときと view/update の一覧は、ほかと共有しうる列なので、最初に足すときに写す。以後は自分の列の後ろへ足す。
		// 後ろへ足しても、描き方の表を作る側が持つ前の一覧（長さが短い）の中は書き換えない。
		decs = slices.Clip(decs)
		f.decsOwned = true
	}
	changedDecs := false

	// 最後の行が 1 行の上限より長い（開いたときの中身のまま）ときだけ、続けた行を分ける位置が最後の行の中に来るので、
	// 最後の行の色の範囲も分け直す。上限以下なら、分ける位置は書き足したテキストの中に来て、最後の行の色の位置は変わらない。
	var lastSpans []ext.ViewSpan
	if !f.textEOL && len(last) > ext.ViewTextMaxLineBytes && len(decs) > 0 {
		decs, lastSpans = splitLastLineDecorations(unshiftDecorations(decs, shift), b, lastIdx)
		shift = 0
		changedDecs = true
	}
	lines, lineSpans, eolAfter := ext.AppendViewText(last, lastSpans, f.textEOL, text, spans)
	if len(lines) == 0 {
		return
	}
	first := lastIdx // lines[0] を置く行
	edit := buffer.Edit{Range: buffer.Range{Start: buffer.Position{Line: lastIdx}, End: buffer.Position{Line: lastIdx, Byte: len(last)}}, Text: strings.Join(lines, "\n")}
	if f.textEOL {
		first = lastIdx + 1
		edit = buffer.Edit{Range: buffer.Range{Start: edit.Range.End, End: edit.Range.End}, Text: "\n" + edit.Text}
	}
	edits := []buffer.Edit{edit}
	drop := ext.ViewTextOverflow(first + len(lines))
	if drop > 0 {
		edits = append(edits, buffer.Edit{Range: buffer.Range{End: buffer.Position{Line: drop}}})
	}
	ch, err := b.ApplyUnrecorded(buffer.Transaction{BaseVersion: b.Version(), Edits: edits})
	if err != nil {
		e.log.Error("view/append を当てられない", "err", err)
		return
	}
	e.commitChange(bs, ch)
	f.textEOL = eolAfter

	for i, ss := range lineSpans {
		line := first + i + shift // 一覧の行の番号は、shift を足した番号で持つ
		for _, s := range ss {
			decs = append(decs, ext.Decoration{
				Range: ext.Range{Start: ext.Position{Line: line, Byte: s.Start}, End: ext.Position{Line: line, Byte: s.End}},
				Style: s.Style,
			})
			changedDecs = true
		}
	}
	if drop > 0 && len(decs) > 0 {
		// 捨てた行の中だけの範囲を先頭から外す。書き足した範囲は行の順に並ぶので、外す手間は捨てた範囲の数に比例する。
		// 開いたときの一覧の中で順に並ばない範囲が残っても、表を作るときに捨てる（buildDecorations）。
		shift += drop
		for len(decs) > 0 && decs[0].Range.End.Line < shift {
			decs = decs[1:]
		}
		changedDecs = true
	}
	if changedDecs {
		if len(decs) == 0 {
			bs.removeDecorations(popupDecorationOwner)
		} else {
			bs.decorations[popupDecorationOwner] = decorationSet{version: ext.Version(b.Version()), decorations: decs, shift: shift}
		}
		e.rebuildDecorations(bs)
	}

	n := b.LineCount()
	switch {
	case follow:
		cursor.Line = n - 1
	case cursor.Line < drop:
		cursor = buffer.Position{}
	default:
		cursor.Line -= drop
	}
	cursor.Byte = min(cursor.Byte, len(b.Line(cursor.Line)))
	bs.state.SetCursor(cursor)
	if f.followHeld {
		if f.heldLine -= drop; f.heldLine < 0 {
			f.followHeld = false
		}
	}
	if vp := bs.state.Viewport; drop > 0 && !follow && vp != nil {
		// 見ている行を動かさない。窓の最初の行の番号を、捨てた行数だけずらす。
		// 窓は、テキストの部品なら floatViewport、組の出力なら consoleOutViewport である（開いたときに State に付けたもの）。
		top, skip := vp.Top()-drop, vp.Skip()
		if top < 0 {
			top, skip = 0, 0
		}
		vp.SetView(top, skip, -1)
	}
}

// holdingFollow は、テキストの部品のバッファ bs が、ビジュアルモードかキーの列の途中（オペレーターの後のモーションを待つ間など）で、
// 書き足しで追従しない状態かを返す（FR-272）。
func holdingFollow(bs *bufferState) bool {
	mode := bs.state.Mode()
	return mode == command.ModeVisual || mode == command.ModeVisualLine || bs.state.Pending()
}

// resumeFollow は、テキストの部品 f が、ビジュアルモードかキーの列の途中で追従を止めていて（floatState.followHeld）、
// それを抜けていれば、カーソルがまだ止めたときの行にあるときだけ最後の行へ戻して追従を続ける。
// 抜けた後にカーソルが別の行にあれば（利用者が動かした、ビジュアルモードの選択の先頭へ戻った）、追従を止めたままにする。
// キーを処理した後（afterKey）と、書き足す前に呼ぶ。
//
// req: FR-272
func (e *Editor) resumeFollow(f *floatState) {
	if f == nil || !f.followHeld || !f.appendable() || holdingFollow(f.textBuf()) {
		return
	}
	bs := f.textBuf()
	f.followHeld = false
	if c := bs.state.Cursor(); c.Line == f.heldLine {
		last := bs.buf.LineCount() - 1
		bs.state.SetCursor(buffer.Position{Line: last, Byte: min(c.Byte, len(bs.buf.Line(last)))})
	}
}

// splitLastLineDecorations は、色を付ける範囲 decs を、最後の行 lastIdx より前に収まる範囲と、最後の行の中の範囲に分ける。
// 最後の行より前から続く範囲は、前の行の終わりで切り、最後の行の中の部分を最後の行の範囲に入れる。
// 最後の行の範囲は、直に付ける色（Attr）を持たない形（ext.ViewSpan）にする。
func splitLastLineDecorations(decs []ext.Decoration, b *buffer.Buffer, lastIdx int) (before []ext.Decoration, last []ext.ViewSpan) {
	for _, d := range decs {
		s, en := d.Range.Start, d.Range.End
		switch {
		case en.Line < lastIdx:
			before = append(before, d)
		case s.Line >= lastIdx:
			if s.Line == lastIdx {
				last = append(last, ext.ViewSpan{Start: s.Byte, End: en.Byte, Style: d.Style})
			}
		default:
			cut := d
			cut.Range.End = ext.Position{Line: lastIdx - 1, Byte: len(b.Line(lastIdx - 1))}
			before = append(before, cut)
			last = append(last, ext.ViewSpan{Start: 0, End: en.Byte, Style: d.Style})
		}
	}
	return before, last
}

// unshiftDecorations は、行の番号に drop を足して持つ色の範囲 decs（decorationSet.shift）を、バッファの行の番号にした新しい列を返す。
// 負になる行の中だけの範囲は捨て、負の行から続く範囲は最初の行の先頭から始める（buildDecorations と同じ規則）。
func unshiftDecorations(decs []ext.Decoration, drop int) []ext.Decoration {
	out := make([]ext.Decoration, 0, len(decs))
	for _, d := range decs {
		if d.Range.End.Line < drop {
			continue
		}
		if d.Range.Start.Line < drop {
			d.Range.Start = ext.Position{Line: drop}
		}
		d.Range.Start.Line -= drop
		d.Range.End.Line -= drop
		out = append(out, d)
	}
	return out
}

// noteAppendLost は、取っておく列から書き足しを捨てた部品 key を覚える。取っておいたイベントを処理し終えたら、
// その部品の末尾に appendLostNote の行を足す（flushAppendLost）。
func (e *Editor) noteAppendLost(a *exthost.ViewAppend) {
	k := appendKey{ext: a.Extension, view: a.Params.View, child: a.Params.Child}
	for _, l := range e.appendLost {
		if l == k {
			return
		}
	}
	e.appendLost = append(e.appendLost, k)
}

// flushAppendLost は、取っておく列から書き足しを捨てた部品の末尾に、捨てたことを表す行を足す。
// 取っておいたイベントをすべて処理した後に呼ぶ。部品が閉じていれば何もしない。
func (e *Editor) flushAppendLost() {
	lost := e.appendLost
	e.appendLost = nil
	for _, k := range lost {
		f := e.floatOrNil(k)
		if f == nil {
			continue
		}
		text := appendLostNote + "\n"
		if !f.textEOL {
			text = "\n" + text
		}
		e.viewAppend(&exthost.ViewAppend{Extension: k.ext, Params: ext.ViewAppendParams{View: k.view, Child: k.child, Text: text, Highlights: []ext.ViewSpan{{Start: strings.Index(text, appendLostNote), End: strings.Index(text, appendLostNote) + len(appendLostNote), Style: "comment"}}}})
	}
}

// floatOrNil は、部品 k を出しているテキストの部品を返す。無ければ nil である。
func (e *Editor) floatOrNil(k appendKey) *floatState {
	if k.child != "" {
		return nil
	}
	_, f := e.findViewFloat(k.ext, k.view)
	if f == nil || !f.appendable() {
		return nil
	}
	return f
}

// appendKey は、書き足す先の部品（拡張機能、部品の ID、切り替えの部品の子の ID の組）である。
type appendKey struct {
	ext   string
	view  ext.ViewID
	child string
}
