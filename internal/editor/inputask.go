package editor

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/target"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 拡張機能の問いの列（Editor.choices）と、1 行の入力を聞く問い（input/ask。基本設計 1.4 の「1 行の入力を聞くリクエスト」。FR-275）。
//
// choice/ask と input/ask は 1 本の列に届いた順に並べ、先頭の 1 つだけを、問いを出した client（asker）に出す。
// 問いの間は、問いを出した client のキーと貼り付けをすべて問いへの答えにし、キーマップを通さず、command.State へも渡さない。
// このため答えのキーは、マクロにもドットリピートにも記録されない（choice/ask と同じ）。
// 他の client のキーは、確認の問いと同じく答えるまで待たせる（keyAllowed）。
// 問いを出した拡張機能が再起動したら、その問いの Done が閉じるので、列から外して取り下げる（activeChoice）。
// 利用者の応答を待つので、キー入力ごとの問い合わせの期限（FR-17）は当てない（応答の期限は exthost が持たない）。

// extQuestion は、拡張機能の問いの列の 1 件である。choice と input のどちらか一方だけを持つ。
type extQuestion struct {
	choice *exthost.ChoiceAskRequest
	input  *exthost.InputAskRequest
	// text は、input/ask の入力の今の中身である。初めは params の default である。
	text string
}

// Done は、問いのリクエストが取り消されたか、拡張機能の接続が閉じたか、応答した後に閉じるチャネルを返す（request）。
func (q *extQuestion) Done() <-chan struct{} {
	if q.input != nil {
		return q.input.Done()
	}
	return q.choice.Done()
}

// ReplyError は、拡張機能へ失敗の応答を返す（request）。
func (q *extQuestion) ReplyError(err error) error {
	if q.input != nil {
		return q.input.ReplyError(err)
	}
	return q.choice.ReplyError(err)
}

// cancel は、問いを取り消したことを拡張機能へ返す。拡張機能が先に取り下げていれば届かない。
func (q *extQuestion) cancel() {
	if q.input != nil {
		_ = q.input.Reply(ext.InputAskResult{Cancelled: true})
		return
	}
	_ = q.choice.Reply(ext.ChoiceAskResult{Cancelled: true})
}

// enqueueQuestion は、拡張機能 extID から届いた問い q を列の末尾に足す。
// 問いを出していなければ、問いはその拡張機能へ問い合わせを始めた client に出す（基本設計 1.2。extView）。
func (e *Editor) enqueueQuestion(extID string, q *extQuestion) {
	if e.asker == nil && !e.asking() && e.activeChoice() == nil {
		e.asker = e.extView(extID)
	}
	e.choices = append(e.choices, q)
}

// inputKey は、表示中の 1 行の入力の問いへのキーを処理する（FR-275）。
//
// :（コマンドライン）と同じく、文字はカーソル（常に中身の末尾）の前に入れ、<Tab> はタブ文字として入れる（補完はしない）。
// <BS> は 1 文字を、<C-w> はカーソルの前の単語を、<C-u> はカーソルの前のすべてを消す。<C-w> の単語の区切りは、
// インサートモードの <C-w>（insert.delete-prev-word）と同じく、ノーマルモードの b と同じ区切りにする。
// 中身が空でも <BS> で取り消さない（: と違う。空の答えを返せるようにするため。基本設計 1.4）。
// <CR> で中身を確定し、<Esc> と <C-c>（handleKey が <Esc> に替える）で取り消す。
// ほかの修飾キー付きのキーと特殊キーは、: と同じく何もしない（捨てた数は出さない）。
//
// req: FR-275
func (e *Editor) inputKey(k editcmd.Key) {
	q := e.choices[0]
	switch {
	case k == editcmd.Esc:
		_ = q.input.Reply(ext.InputAskResult{Cancelled: true})
		e.answered(e.questionName() + "を取り消しました")
	case k == editcmd.Special(editcmd.CodeEnter):
		_ = q.input.Reply(ext.InputAskResult{Value: q.text})
		if q.text == "" {
			e.answered(e.questionName() + "に空の文字列で答えました")
		} else {
			e.answered(fmt.Sprintf("%sに「%s」と答えました", e.questionName(), q.text))
		}
	case k == editcmd.Special(editcmd.CodeBackspace):
		if q.text != "" {
			r := []rune(q.text)
			q.text = string(r[:len(r)-1])
		}
	case k == editcmd.Key{Code: editcmd.CodeRune, Rune: 'w', Mod: editcmd.ModCtrl}:
		q.text = q.text[:prevWordStart(q.text)]
	case k == editcmd.Key{Code: editcmd.CodeRune, Rune: 'u', Mod: editcmd.ModCtrl}:
		q.text = ""
	case k == editcmd.Special(editcmd.CodeTab):
		q.text += "\t"
	case k.Plain():
		q.text += string(k.Rune)
	}
}

// inputPaste は、表示中の 1 行の入力の問いへ、端末から貼り付けたテキストを入れる（FR-275）。
// 改行は空白にし、タブ文字の他の制御文字は捨てる（答えを 1 行に保つため。: の貼り付けと同じく制御文字は入れない）。
func (e *Editor) inputPaste(text string) {
	q := e.choices[0]
	var b strings.Builder
	for _, r := range strings.ReplaceAll(text, "\r\n", "\n") {
		switch {
		case r == '\n' || r == '\r':
			b.WriteByte(' ')
		case r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	q.text += b.String()
}

// prevWordStart は、1 行の text の末尾にカーソルを置いて <C-w> を打ったときに消し始める位置（バイトの位置）を返す。
// インサートモードの <C-w>（command.State の prevWordStart）と同じく、ノーマルモードの b（word-prev）で戻る位置で、
// カーソルの前の空白を飛ばし、その前の単語の先頭まで戻る。
func prevWordStart(text string) int {
	if text == "" {
		return 0
	}
	f, ok := target.LookupMotion("word-prev")
	if !ok {
		return 0
	}
	// b はカーソルを文字の上に置くので、末尾に空白を 1 つ足してその上から戻る（空白は b が飛ばすので、戻る位置は変わらない）。
	c := buffer.Position{Byte: len(text)}
	res, err := f(buffer.New(text+" "), target.Request{Cursor: c, Count: 1})
	if err != nil || res.Pos.Line != 0 || res.Pos.Byte > len(text) {
		return 0
	}
	return res.Pos.Byte
}

// uiInput は、表示中の 1 行の入力の問いを描画の入力にする。何件目かと、前の問いへの答えと待たせている問いの数の知らせは、
// 選択肢の問い（uiChoice）と同じく添える（#151）。
func (e *Editor) uiInput() *ui.Input {
	q := e.choices[0]
	return &ui.Input{
		Prompt: q.input.Params.Title,
		Text:   q.text,
		Index:  e.questionIndex(),
		Note:   strings.Join(e.questionNotes(), "\n"),
	}
}
