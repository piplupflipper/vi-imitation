package ext

import (
	"fmt"
	"unicode/utf8"
)

// 書き足すテキストと入力の行の組（ViewConsole）の入力の行の補完（FR-297。API 0.24.0 で足した。基本設計 1.4 の
// 「#427 の入力の行の補完」）。
//
// 部品を開くときに ViewComponent.Complete を真にした ViewConsole では、本体は、インサートモードで入力の行の中身が変わるたびと、
// 補完のキー（候補の一覧を出していないときの <Tab>、<C-n>、<C-p>、<C-Space>）で、console/complete（本体 → 拡張機能、リクエスト）を
// 問い合わせる。拡張機能は、入力の行の中身（Text）とカーソル（Cursor）に対する候補を返し、本体は補完の候補の一覧
// （コマンドラインの補完と同じ見た目。設定の completionMenu.height）で出す。
// 問い合わせはキーの処理を止めない（ファインダーの一覧と同じく、FR-17 の期限で待たない）。本体は、前の問い合わせの答えを待たずに
// 次を送り（前のものは $/cancel で取り消す）、最後に送った Seq でない答えと、送った後に入力の行の中身かカーソルが変わった答えを捨てる。

// MethodConsoleComplete は、本体から拡張機能へのリクエストである。params は ConsoleCompleteParams、result は ConsoleCompleteResult。
const MethodConsoleComplete = "console/complete"

// ConsoleCompleteParams は、console/complete の params である。
type ConsoleCompleteParams struct {
	View ViewID `json:"view"`
	// Child は、切り替えの部品（ViewSwitcher）の子の ViewConsole なら、その子の ID である。ほかの部品では省く。
	Child string `json:"child,omitempty"`
	// Text は、入力の行の中身である（前置き Prompt を含めない。改行を含まない）。
	Text string `json:"text"`
	// Cursor は、入力の行のカーソルの位置（Text の中のバイトの位置。0 から len(Text)）である。
	Cursor int `json:"cursor"`
	// Seq は、その部品の補完の問い合わせごとに 1 から 1 ずつ増やす番号である（切り替えの部品の子では子ごとに数える）。
	// 本体は、最後に送った Seq の答えだけを使う。拡張機能は、古い Seq の計算を捨ててよい。
	Seq uint64 `json:"seq"`
}

// ConsoleCompleteResult は、console/complete の result である。
type ConsoleCompleteResult struct {
	// OK は、候補を求められたかである。求められなかった（デバッグのセッションが無い、問い合わせ先が失敗した）なら false にする。
	// 本体は、false の答えでは一覧を出さない。候補が 0 件のときは、OK を true にして Items を空にする。
	OK bool `json:"ok"`
	// Items は、候補である。並べた順に出す。本体は Validate を通らない候補を捨てて、残りを出す。
	Items []ConsoleCompletionItem `json:"items,omitempty"`
}

// ConsoleCompletionItem は、入力の行の補完の候補 1 つである。
type ConsoleCompletionItem struct {
	// Label は、一覧に出す文字列である。改行を含めない。
	Label string `json:"label"`
	// Text は、選んだときに入れる文字列である。空なら Label を入れる。本体は、改行を空白に替え、タブ文字の他の制御文字を捨てて入れる
	// （入力の行の貼り付けと同じ）。
	Text string `json:"text,omitempty"`
	// Start と End は、選んだときに Text で置き換える、問い合わせの Text の中のバイトの範囲 [Start, End) である。
	// Start は Cursor 以下にする。カーソルの前の語の一部だけを置き換えるなら、その語の始まりを Start に、Cursor を End にする。
	// 一覧は Start の文字の列から並べ、Text の [Start, Cursor) に当たる候補の先頭を色で示す。
	Start int `json:"start"`
	End   int `json:"end"`
	// Kind は、候補の種類（DAP の CompletionItemType の "function"、"variable" など）である。省ける。一覧の項目の右に出す。改行を含めない。
	Kind string `json:"kind,omitempty"`
}

// Insert は、候補 it を選んだときに入れる文字列（Text、空なら Label）を返す。
func (it *ConsoleCompletionItem) Insert() string {
	if it.Text != "" {
		return it.Text
	}
	return it.Label
}

// Validate は、候補 it が、入力の行の中身 text とカーソル cursor への問い合わせの答えとして使えるかを検査する。
// Label と Kind が改行を含まないこと、範囲が 0 <= Start <= End <= len(text) で Start <= cursor であること、
// 範囲の両端が UTF-8 の文字の境目にあることを確かめる。
func (it *ConsoleCompletionItem) Validate(text string, cursor int) error {
	switch {
	case containsNewline(it.Label):
		return fmt.Errorf("label が改行を含む")
	case containsNewline(it.Kind):
		return fmt.Errorf("kind が改行を含む")
	case it.Start < 0 || it.End < it.Start || it.End > len(text):
		return fmt.Errorf("範囲 [%d, %d) が入力の行の外を指す", it.Start, it.End)
	case it.Start > cursor:
		return fmt.Errorf("start %d がカーソル %d より後ろにある", it.Start, cursor)
	case !runeBoundary(text, it.Start) || !runeBoundary(text, it.End):
		return fmt.Errorf("範囲 [%d, %d) が文字の途中を指す", it.Start, it.End)
	}
	return nil
}

// runeBoundary は、バイトの位置 i が s の中の文字の境目（先頭と末尾を含む）かを返す。
func runeBoundary(s string, i int) bool {
	return i == 0 || i == len(s) || (i > 0 && i < len(s) && utf8.RuneStart(s[i]))
}
