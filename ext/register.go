package ext

import (
	"errors"
	"unicode/utf8"
)

// MethodRegisterSet は、拡張機能から本体へのリクエストである。params は RegisterSetParams、result は RegisterSetResult。
// 拡張機能が本体のレジスターへテキストを入れる（API 0.25.0 で足した。#432。FR-315）。
//
// 拡張機能は、レジスターの名前、テキスト、文字単位か行単位かを渡す。
// 本体は、名前を指定した y と同じくレジスターへ書く（基本設計 4.5 の表。FR-50〜FR-53）。
// 名前を省けば無名のレジスター（履歴の先頭に積む）、a〜z はそのレジスターに書いて履歴にも積み、A〜Z は追記した後の内容を積み、
// _ には何も書かない。
// 履歴の先頭が変わったら、本体の y と同じく、本体の設定（registers.clipboard）に従ってホストのクリップボードへ送る（FR-56）。
// 知らせも本体の y と同じにする。書いたことは知らせず、履歴の 1 件の上限（registers.maxEntryBytes）を超えて積まなかったこと（FR-53）と、
// クリップボードへ送るには大きすぎたことだけを、メッセージの行に出す。
// 使えない名前（FR-50 の a〜z、A〜Z、_ の他。Vim の読み取り専用のレジスター . : % なども含む）は、本体の y と同じく E354 を
// メッセージの行に出し、何も書かずに、code が CodeInvalidParams の誤りで応答する。
// メッセージは、その拡張機能へ最後に問い合わせを始めた client に出す（message/show と同じ）。
//
// 利用者がレジスターの名前を前に付けて contributes.popupKeys のキーを打ったとき（"ac）は、本体はその名前を view/key の
// Register（ViewKeyParams）に入れて知らせる。拡張機能は、それを RegisterSetParams.Register に入れて送れば、利用者の指定どおりに入れられる。
//
// 古い本体（0.24.0 まで）は register/set に method not found で応答し、view/key の Register を送らない
// （古い本体では、"a の後のキーは popupKeys に当たらず、本体のキーとして解釈する）。
const MethodRegisterSet = "register/set"

// RegisterSetParams は、register/set の params である。
type RegisterSetParams struct {
	// Register は、入れる先のレジスターの名前（1 文字）である。省く（空）と無名のレジスターに入れる。
	// view/key の Register（利用者が指定した名前）をそのまま入れてよい。使えない名前は、本体が E354 で断る。
	Register string `json:"register,omitempty"`
	// Text は、入れるテキストである。改行は "\n" で書く。空でもよい（空のテキストが入る。本体の空の行の yl と同じ）。
	Text string `json:"text"`
	// Linewise は、行単位のテキストとして入れるかである。省く（false）と文字単位である。
	// 行単位では、Text の "\n" で区切った各部分を 1 行とする。最後の行の後の改行は書いても書かなくてもよい（書けば 1 つだけ取り除く）。
	Linewise bool `json:"linewise,omitempty"`
}

// Validate は、register/set の params の形を確かめる。Register は 1 文字以下でなければならない。
// 名前が使えるか（FR-50）は、本体が確かめて E354 をメッセージの行に出す。
func (p RegisterSetParams) Validate() error {
	if utf8.RuneCountInString(p.Register) > 1 {
		return errors.New("register/set: register must be a single character")
	}
	return nil
}

// RegisterSetResult は、register/set の result である。中身は無いが、後から項目を足せるように構造体にしている。
type RegisterSetResult struct{}
