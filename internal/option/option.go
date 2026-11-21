// Package option は、設定の項目の表（名前、型、効く範囲、既定値）と、項目の値を持つ（基本設計 7.3。FR-131）。
//
// 基本設計 2 章の土台の層に置き、他のパッケージを import しない。
// 設定ファイルを読む internal/config と、Ex コマンド（:option.set、:option.get、:set）を解析する internal/command/ex は、
// どちらも機能の層で互いに import できないので、両方が引く表をこの層に置く。
// 値を持つのは internal/editor である。範囲がエディタ全体の項目の値は Editor に 1 つ、バッファごとの項目の値はバッファごとに 1 つの Values に持つ。
//
// # 名前
//
// 正規の名前は、点で区切った camelCase で、何の設定かが名前で分かるものにする（lineNumber.show など）。
// Vim の短い名前（number）は正規の名前にせず、:set の別名の層（internal/command/ex）でだけ受け付ける。
// 設定ファイルでは、点で区切った名前を入れ子のキーとして書く（"lineNumber": { "show": true }）。
//
// # 値の使い方
//
// 範囲がエディタ全体の項目は、使う側（描画など）が使うたびに今の値を読む。値が変わったときに、その項目のためだけの処理は呼ばない。
// 範囲がバッファごとの項目を変えたときは、項目の名前によらず、拡張機能へバッファの属性を知らせ直す（internal/editor）。
// 範囲がバッファごとの項目も、使う側（インサートモードの <Tab>、描画など）が使うたびにそのバッファの今の値を読む。
//
// # バッファごとの項目の初めの値
//
// バッファを開くときに、範囲がバッファごとの項目（Def.Detected のものを除く）の初めの値を、次の段を順に当てて決める。
// 後に当てた段の値が勝つ（#318 のオーナーの決め。基本設計 7.3。FR-163、FR-164）。
//
//  1. 表の既定値（Def.Default）
//  2. 設定ファイルの最上位に書いた値
//  3. 本体のファイルの種類ごとの既定（FileTypeDefaults）
//  4. 設定ファイルのファイルの種類ごとの値（fileTypeOptions）
//  5. 開いたファイルに当たる .editorconfig の値
//  6. 拡張機能が option/set で入れた値（整形ツールの拡張機能が読んだ biome.json などの値。#349。FR-190）
//
// 3 を 2 の後に置くのは、Neovim が利用者の設定（init.lua の :set）の後にファイルの種類ごとの ftplugin を当てるのと同じにするためである。
// 段を当てるのは internal/editor で、このパッケージは 1 と 3 の表を持つ。
//
// # JSON の値
//
// 拡張機能 API（option/set と option/get。#349）は、値を項目の型の JSON の値（true、整数、文字列）で受け渡す。
// JSON から読むのは Def.ParseJSON で、:option.set と同じく Def.Check を通す。JSON に書くのは Value.MarshalJSON である。
package option

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Type は、項目の値の型である。
type Type int

// 値の型。
const (
	// Bool は真偽で、文字列では true と false で書く。
	Bool Type = iota + 1
	// Int は整数で、文字列では 10 進で書く（先頭の - を許す）。
	Int
	// String は文字列である。
	String
)

// String は、型の名前（"bool"、"int"、"string"）を返す。
func (t Type) String() string {
	switch t {
	case Bool:
		return "bool"
	case Int:
		return "int"
	case String:
		return "string"
	}
	return "Type(" + strconv.Itoa(int(t)) + ")"
}

// Scope は、項目の効く範囲である。
type Scope int

// 効く範囲。
const (
	// ScopeEditor は、エディタ全体に 1 つの値を持つ項目である。設定ファイルで初めの値を書ける。
	ScopeEditor Scope = iota + 1
	// ScopeBuffer は、バッファごとに値を持つ項目である。初めの値はバッファを開くときに、パッケージの doc の段を順に当てて決まる。
	// 設定ファイルの最上位に書いた値は、バッファを開くときの初めの値（2 の段）になる（FR-163）。
	// ただし Def.Detected の項目は、バッファを開くときにファイルから判定して決め、設定ファイルには書けない。
	ScopeBuffer
)

// Value は、項目の 1 つの値である。ゼロ値は、どの型でもない値（Valid が false）である。
type Value struct {
	typ Type
	b   bool
	n   int
	s   string
}

// BoolValue は、真偽の値を返す。
func BoolValue(b bool) Value { return Value{typ: Bool, b: b} }

// IntValue は、整数の値を返す。
func IntValue(n int) Value { return Value{typ: Int, n: n} }

// StringValue は、文字列の値を返す。
func StringValue(s string) Value { return Value{typ: String, s: s} }

// Type は、値の型を返す。ゼロ値では 0 である。
func (v Value) Type() Type { return v.typ }

// Valid は、値がどれかの型の値であるかを返す。
func (v Value) Valid() bool { return v.typ != 0 }

// Bool は、真偽の値を返す。真偽でない値では false である。
func (v Value) Bool() bool { return v.b }

// Int は、整数の値を返す。整数でない値では 0 である。
func (v Value) Int() int { return v.n }

// String は、値を文字列で返す。真偽は true か false、整数は 10 進、文字列はそのままである。
// Def.Parse で読み戻すと同じ値になる。:option.get が出す値もこの形である。
func (v Value) String() string {
	switch v.typ {
	case Bool:
		return strconv.FormatBool(v.b)
	case Int:
		return strconv.Itoa(v.n)
	case String:
		return v.s
	}
	return ""
}

// Def は、設定の表の 1 行である。
type Def struct {
	// Name は、正規の名前である。
	Name string
	// Type は、値の型である。
	Type Type
	// Scope は、効く範囲である。
	Scope Scope
	// Default は、既定値である。型は Type と同じにする。
	Default Value
	// Range は、整数の項目が取れる値の範囲である。nil なら範囲を限らない。整数でない項目では nil にする。
	// 範囲は Check で確かめるので、値を項目に入れるどの経路（設定ファイル、:option.set、:set）にも効く。
	Range *IntRange
	// Contains は、文字列の項目の値が空でなければ含まなければならない文字列である。空なら制約を置かない。文字列でない項目では空にする。
	// 値は Check で確かめるので、値を項目に入れるどの経路（設定ファイル、:option.set、:set）にも効く。
	// comment.string の %s に使う（Vim 9.1 と Neovim 0.12.5 の 'commentstring' の E537 と同じ制約）。
	Contains string
	// Words は、文字列の項目の値を、語の列（WordList の書き方）として読むときの語の候補である。nil なら制約を置かない。
	// 文字列でない項目では nil にする。値は Check で確かめるので、値を項目に入れるどの経路（設定ファイル、:option.set、:set）にも効く。
	// commandLine.completeMode に使う（Vim 9.1 と Neovim 0.12.5 の 'wildmode' の書き方と E474）。
	// コマンドラインの補完（internal/command/ex の Complete）は、この語を値の候補にする。
	Words *WordList
	// Detected は、範囲がバッファごとの項目のうち、初めの値をバッファを開くときにファイルから判定して決める項目であることを表す（filetype）。
	// この項目は、設定ファイル（最上位と fileTypeOptions）にも .editorconfig にも書けず、初めの値の段（パッケージの doc）を当てない。
	// 範囲がエディタ全体の項目では false にする。
	Detected bool
}

// IntRange は、整数の項目が取れる値の範囲 [Min, Max] である（両端を含む）。
type IntRange struct {
	Min, Max int
}

// WordList は、文字列の値を、コンマで区切った部分の列とし、各部分をコロンで区切った語の列として読むときの規則である
// （Vim 9.1 と Neovim 0.12.5 の 'wildmode' の書き方。Def.Words）。
//
// 空の値は、部分が 1 つも無い値として受け付ける。部分は MaxParts 個までで、どの語も Words のどれかでなければならない。
// 値の末尾のコンマとコロンは受け付けるが、空の語（先頭の区切り、続けた区切り）と、MaxParts 個目の後のコンマは受け付けない
// （Neovim 0.12.5 の :set wildmode=full: と :set wildmode=longest:full, は通り、:set wildmode=:full、:set wildmode=full,,full、
// :set wildmode=full,full,full,full, は E474 になる。2026-11-22 に確かめた。Vim 9.1 の check_opt_wim と同じ読み方である）。
type WordList struct {
	// Words は、語の候補である。並びは、コマンドラインの補完で候補を出す順である。
	Words []string
	// MaxParts は、部分の数の上限である。
	MaxParts int
}

// Parse は、値 s を部分ごとの語の列に分けて返す。s の末尾のコンマで終わった部分は、空の部分として返す。
// 読めなければ ErrInvalidWords を返す。
func (l WordList) Parse(s string) ([][]string, error) {
	if s == "" {
		return nil, nil
	}
	parts := [][]string{nil}
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] != ',' && s[j] != ':' {
			j++
		}
		if !slices.Contains(l.Words, s[i:j]) {
			return nil, ErrInvalidWords
		}
		last := len(parts) - 1
		parts[last] = append(parts[last], s[i:j])
		if j == len(s) {
			break
		}
		if s[j] == ',' {
			if len(parts) == l.MaxParts {
				return nil, ErrInvalidWords
			}
			parts = append(parts, nil)
		}
		i = j + 1
		if i == len(s) {
			break
		}
	}
	return parts, nil
}

// 項目の正規の名前。値を読む側は、名前を文字列で書かずにこの定数を使う。
const (
	// FileType は、バッファのファイルの種類である（基本設計 6.7 のファイルの種類の判定。FR-134）。
	// バッファを開くときに、設定の fileTypes の対応表と自動の判定で初めの値が決まる。ステータス行の部品の名前と同じにした。
	FileType = "filetype"
	// LineNumberShow は、バッファの表示領域の左に行番号の列を出すかである（FR-135。Vim の 'number'）。
	LineNumberShow = "lineNumber.show"
	// LineNumberMinWidth は、行番号の列の最小の列数である（FR-136。Vim の 'numberwidth'）。
	// 列の幅は、この値と、バッファの最後の行の番号の桁数に 1 を足した数の大きい方になる。範囲は Vim と同じく 1 から 20 である。
	LineNumberMinWidth = "lineNumber.minWidth"
	// LineWrapEnabled は、長い行を本文の幅で折り返して描くかである（FR-156。Vim の 'wrap'）。
	// 本体の既定は折り返さない（Vim と Neovim の既定は折り返す。#300 のオーナーの決め）。描画が、描くたびにこの値を読む。
	LineWrapEnabled = "lineWrap.enabled"
	// WhitespaceShow は、タブ文字、空白、ノーブレークスペース、行末を記号で描くかである（FR-170。Vim の 'list'）。
	// 記号は設定の symbols の "whitespace." で始まる名前で決まる。本体の既定は Vim と Neovim の既定と同じく描かない（#328 のオーナーの決め）。
	// Vim の 'list' は窓ごとの設定だが、本体には窓が無く、同じく窓ごとの 'number' と 'wrap' に当たる項目と同じくエディタ全体の範囲にした。
	// 描画と、カーソルの列の数え方が、使うたびにこの値を読む。
	WhitespaceShow = "whitespace.show"
	// ScrollMargin は、カーソルの行の上と下に残す余白の行数である（FR-174。Vim の 'scrolloff'）。範囲はエディタ全体で、
	// 既定は Vim 9.1 と Neovim 0.12.5 の既定と同じく 0 である（Vim の defaults.vim は 5 にする）。行を折り返すときは見た目の行で数える。
	// 表示領域の半分以上にすると、カーソルの行を常に中ほどに置く（Vim の 999 と同じ）。範囲は 0 から 9999 である。
	// 描画と、スクロールの操作と、プレビューのスクロールが、使うたびに読む。Vim の 'scrolloff' は窓ごとにも持てるが、本体には窓が無いので、
	// 同じく窓ごとの 'wrap' に当たる lineWrap.enabled と同じくエディタ全体の範囲にした。
	ScrollMargin = "scroll.margin"
	// ScrollSideMargin は、行を折り返さないときに、カーソルの左と右に残す余白の列数である（FR-174。Vim の 'sidescrolloff'）。
	// 範囲はエディタ全体で、既定は Vim と Neovim の既定と同じく 0 である。範囲は 0 から 9999 である。描画が、描くたびに読む。
	ScrollSideMargin = "scroll.sideMargin"
	// SearchHighlight は、直前の検索のパターンに一致する範囲に色を付けるかである（FR-137。Vim の 'hlsearch'）。
	// 描画が、描くたびにこの値と直前の検索のパターンを読む。
	SearchHighlight = "search.highlight"
	// SearchIncrementalTimeoutMs は、/ と ? でパターンを打っている間の、打つたびの検索の時間の上限のミリ秒である（FR-153）。
	// 上限を超えたら、その回は一致を出さない。<CR> で決めたときの検索には上限を付けない。範囲は 1 から 10000 である。
	// 既定の 500 は、Vim 9.1 の 'incsearch' が検索を約 0.5 秒で打ち切るのと同じである（:help 'incsearch'。'redrawtime' の既定の 2000 は
	// 'hlsearch' の色付けの上限で、打つたびの検索には使わない）。打つたびの検索をしない設定は持たない（#296 のオーナーの決め）。
	SearchIncrementalTimeoutMs = "search.incrementalTimeoutMs"
	// SearchShowCount は、検索で動いたときに、メッセージの行の右に一致の件数（[2/5] の形）を出すかである（FR-167。
	// Vim の 'shortmess' に S が無いとき）。本体の既定は Vim 9.1 の既定（'shortmess' に S があり、出さない）に合わせて出さない。
	// 検索するたびにこの値を読む。'shortmess' は多くのフラグをまとめた設定なので、:set の別名は作らない（#320 のオーナーの決め）。
	SearchShowCount = "search.showCount"
	// YankHighlightMs は、本体の y でヤンクした範囲に色を付けておくミリ秒である（FR-140。nvim の vim.hl.on_yank の timeout）。
	// 0 なら付けない。範囲は 0 から 10000 である。Vim にこの設定は無い。
	YankHighlightMs = "yank.highlightMs"
	// PreviewMinWindowWidth は、項目のプレビュー（FR-149）を出す窓の幅の境目の列数である。プレビューの欄を持つ中央のポップアップか
	// ペインの窓の幅（PreviewWindowWidthPercent で広げた幅）がこの列数を超えるときだけ、プレビューを右に出す。
	// 既定の 100 は、fzf-lua の flip_columns の既定と同じである。範囲は 0 から 10000 である。Vim にこの設定は無い。
	PreviewMinWindowWidth = "preview.minWindowWidth"
	// PreviewWidthPercent は、プレビューを出すときに、窓の幅のうちプレビューに使う百分率である。
	// 既定の 60 は、fzf-lua の winopts.preview.horizontal の既定（right:60%）と同じである。範囲は 1 から 99 である。
	PreviewWidthPercent = "preview.widthPercent"
	// PreviewWindowWidthPercent は、プレビューを出すときの中央のポップアップの窓の幅の、画面の幅に対する百分率である。
	// 既定の 90 は、オーナーの nvim の fzf-lua の winopts.width と同じである。範囲は 10 から 100 である。
	PreviewWindowWidthPercent = "preview.windowWidthPercent"
	// IconsShow は、一覧と木の、ファイルかディレクトリを指す項目の前に、ファイルの種類のアイコンを付けるかである（FR-152）。
	// 既定は付ける（#295 のオーナーの決め）。アイコンは Nerd Font のグリフなので、Nerd Font の入っていない端末では false にする。
	// 一覧と木を作り直すとき（開いたときと、拡張機能が中身を送り直したとき）に読む。Vim にこの設定は無い。
	IconsShow = "icons.show"
	// IndentUseSpaces は、字下げをスペースで作るかである（FR-160。Vim の 'expandtab'）。範囲はバッファごとで、既定は Vim と同じくタブ文字で作る。
	// true なら、インサートモードの <Tab> と <BS> は Neovim 0.12.5 の expandtab shiftwidth=N softtabstop=N（N は IndentWidth の幅）と同じに、
	// false なら noexpandtab shiftwidth=N softtabstop=0 と同じに動く。どちらも Neovim の既定の 'smarttab' がオンの動きである。
	// Vim の 'softtabstop' と 'smarttab' は、別の設定にしない（#318 の決め）。
	IndentUseSpaces = "indent.useSpaces"
	// IndentWidth は、字下げの 1 段の幅である（FR-160。Vim の 'shiftwidth'）。範囲はバッファごとで、既定は Vim と同じく 8 である。
	// 0 なら TabWidth の値を使う（Vim の shiftwidth=0 と同じ）。範囲は 0 から 9999 である（上限は TabWidth と同じにした）。
	IndentWidth = "indent.width"
	// TabWidth は、タブ文字の表示幅である（FR-161。Vim の 'tabstop'）。範囲はバッファごとで、既定は Vim と同じく 8 である。
	// 描画、カーソルの列の数え方、gj と gk、字下げの幅の数え方が、使うたびにバッファの今の値を読む。範囲は Vim と同じく 1 から 9999 である。
	TabWidth = "tab.width"
	// IndentAuto は、自動インデントを行うかである（FR-162。Vim の 'autoindent'）。範囲はバッファごとで、既定は Vim と同じく行わない。
	IndentAuto = "indent.auto"
	// EditorConfigEnabled は、開いたファイルに当たる .editorconfig を読むかである（FR-164。Neovim の g:editorconfig）。
	// 範囲はエディタ全体で、既定は Neovim と同じく読む。バッファを開くときに読むので、変えた後に開いたバッファから効く。
	EditorConfigEnabled = "editorConfig.enabled"
	// UndoPersist は、ファイルのアンドゥの履歴を、そのファイルを保存するときに状態の置き場（$XDG_STATE_HOME/vi-imitation/undo）へ残し、
	// 次にそのファイルを開いたときに読み戻すかである（FR-165。Vim の 'undofile'）。
	// 既定は Vim と同じく残さない（#319 のオーナーの決め）。false の間は、読みも書きもせず、置き場に何も作らない。
	// 保存するときと開くときに読む。
	UndoPersist = "undo.persist"
	// CursorRestore は、ファイルを閉じたときのカーソルの位置を状態の置き場（$XDG_STATE_HOME/vi-imitation/state.json）へ残し、
	// 次にそのファイルを開いたときにその位置へカーソルを置くかである（FR-166。Vim の defaults.vim の BufReadPost の自動コマンドに当たる）。
	// 既定は Vim の既定（defaults.vim を読まない）と同じく残さない（#319 のオーナーの決め）。false の間は、読みも書きもしない。
	// ファイルを閉じるときと開くときに読む。
	CursorRestore = "cursor.restore"
	// CommentString は、行のコメントの形である（FR-169。Vim の 'commentstring'）。範囲はバッファごとで、%s の前がコメントの前に、
	// 後ろがコメントの後ろに付く（"// %s"、"/* %s */"）。値は空か、%s を含む（Def.Contains。Vim の E537）。
	// 既定は Neovim 0.12.5 と同じく空で、ファイルの種類ごとの値は fileTypeDefaults にある。
	// Vim 9.1 の既定は "/* %s */" だが、Vim 9.1 に既定の gc は無く、本体の gc は Neovim の gc に合わせるので、既定も Neovim に合わせた
	// （空なら gc はコメントを付けずに知らせる。種類の分からないファイルに C のコメントを付けない）。
	// gc とコメントのテキストオブジェクトが、使うたびにバッファの今の値を読む（internal/command の State.commentString）。
	CommentString = "comment.string"
	// CursorHoldDelayMs は、利用者がキーを打たずにこのミリ秒が経ったら、カーソルが止まったとする時間である
	// （FR-171 のカーソルが止まった契機。Vim の 'updatetime' の CursorHold の時間）。範囲はエディタ全体で、既定は Vim と Neovim と同じく 4000 である。
	// 範囲は 0 から 60000 である（下限は Neovim 0.12.5 の :set ut=0 が通るのと同じ。上限は 1 分にした）。キーを打つたびに読む。
	CursorHoldDelayMs = "cursorHold.delayMs"
	// DiagnosticPopup は、カーソルが止まったときに、カーソルの行の診断をカーソルの近くの浮かべた窓に出すかである（FR-171。
	// Neovim の CursorHold の自動コマンドで vim.diagnostic.open_float を開く設定に当たる）。範囲はエディタ全体で、既定は出さない
	// （Vim と Neovim の既定は出さない）。出すときは、メッセージの行に 1 行で出す診断の本文（FR-30）を出さない。カーソルが止まるたびに読む。
	DiagnosticPopup = "diagnostic.popup"
	// ProgressShow は、拡張機能が progress/report で知らせた進み具合を、編集の client の画面の右下に重ねて出すかである（FR-178。
	// Neovim の fidget.nvim の表示に当たる）。範囲はエディタ全体で、描くたびに読む。Vim と Neovim に対応する設定は無い。
	// 既定は出す（#340 の決め）。0.14.0 より前の標準の拡張機能は、同じ進み具合（パーサの用意、mise での入れ込み）をメッセージの行に
	// 出していたので、既定で出さないと、既定の設定のままの利用者から進み具合が見えなくなるためである。
	ProgressShow = "progress.show"
	// ProgressDoneMs は、終わった進み具合（progress/report の end）を、終わった印を付けて出しておくミリ秒である（FR-178。
	// fidget.nvim の progress.display.done_ttl に当たる）。0 なら終わったらすぐに消す。範囲は 0 から 60000 で、既定は 3000 である
	// （fidget.nvim の done_ttl の既定の 3 秒）。進み具合が終わるたびに読む。
	ProgressDoneMs = "progress.doneMs"
	// MouseEnabled は、マウスを使うかである（FR-180。Vim の 'mouse'）。範囲はエディタ全体で、既定は Vim 9.1 の既定（'mouse' が空）と同じく
	// 使わない（#317 の既定値の方針。Neovim 0.12.5 の既定は nvi）。true の間は、どの client の端末にもマウスの報告を求め、
	// マウスの出来事のキー（<LeftMouse> など。FR-184）が届く。false の間は報告を求めず、端末の側でテキストを選べる。
	// Vim の 'mouse' はモードごとのフラグの文字列だが、本体はどのモードでも使うか使わないかだけを選ぶ（オーナーの nvim は mouse=a）。
	// フラグの文字列と値の形が合わないので、:set の別名は作らない（:set mouse=a が誤りになり、:set mouse が値を出さずに入れることになるため）。
	// 描くたびに読む。
	MouseEnabled = "mouse.enabled"
	// MouseScrollLines は、ホイールを 1 回回したときに表示領域を送る行数である（FR-182。Neovim 0.12.5 の 'mousescroll' の ver）。
	// 既定は Neovim の既定と同じく 3 で、0 なら縦に送らない。範囲は 0 から 9999 である（scroll.margin と同じ上限）。
	MouseScrollLines = "mouse.scrollLines"
	// MouseScrollColumns は、行を折り返さないときに、ホイールを 1 回傾けたときに表示領域を送る列数である
	// （FR-182。'mousescroll' の hor）。既定は Neovim の既定と同じく 6 で、0 なら横に送らない。範囲は 0 から 9999 である。
	MouseScrollColumns = "mouse.scrollColumns"
	// ScrollbarShow は、バッファの表示領域の右端の 1 列に、スクロールバー（見えている行の範囲の位置と大きさを表すつまみ）と、
	// 診断と検索の一致の位置の印を出すかである（FR-186。Neovim の nvim-scrollview に当たる）。範囲はエディタ全体で、描くたびに読む。
	// 既定は Vim と Neovim と同じく出さない（#317 の方針）。出すのは、表示領域にバッファの全体が収まらないときだけである。
	ScrollbarShow = "scrollbar.show"
	// ScrollbarCurrentOnly は、スクロールバーを、操作中の client（最後にキーを受けた編集の client）の画面にだけ出すかである
	// （FR-186。nvim-scrollview の current_only）。false なら、繋いでいるすべての編集の client の画面に出す。
	// 既定は nvim-scrollview と同じく false である。描くたびに読む。
	ScrollbarCurrentOnly = "scrollbar.currentOnly"
	// ScrollbarMarks は、スクロールバーに出す印の種類の名前を、コンマで区切って並べたものである（FR-187。nvim-scrollview の signs_on_startup）。
	// 名前は diagnostic（診断。重要度ごとに印と色を分ける）と search（検索の一致）で、知らない名前は読み飛ばす（後に拡張機能が足す種類の名前を、
	// 拡張機能が起動する前の設定に書けるようにするため）。空なら印を出さない。名前の前後の空白は除く。
	// 既定は "diagnostic,search" である。描くたびに読む。
	ScrollbarMarks = "scrollbar.marks"
	// ScrollbarExcludedFileTypes は、スクロールバーを出さないバッファのファイルの種類（filetype の値）を、コンマで区切って並べたものである
	// （FR-186。nvim-scrollview の excluded_filetypes）。名前の前後の空白は除く。既定は空（どのファイルの種類にも出す）。描くたびに読む。
	ScrollbarExcludedFileTypes = "scrollbar.excludedFileTypes"
	// CommandLineCompleteMode は、コマンドラインの <Tab> の補い方である（FR-196。Vim の 'wildmode'）。範囲はエディタ全体で、
	// 値は Vim と同じ書き方（WordList）で、語は full、longest、noselect である。既定は Vim 9.1 と Neovim 0.12.5 の既定と同じく full である。
	// Vim の list と lastused は持たない（候補は常にポップアップの一覧に出し、バッファの候補はバッファの番号の順に出す）。
	// <Tab> を打つたびに読む。
	CommandLineCompleteMode = "commandLine.completeMode"
	// CommandLineCompleteIgnoreCase は、コマンドラインの補完で、ファイルとディレクトリの名前の大文字と小文字を区別しないかである
	// （FR-196。Vim の 'wildignorecase'）。既定は Vim と同じく区別する。補う候補を集めるたびに読む。
	CommandLineCompleteIgnoreCase = "commandLine.completeIgnoreCase"
	// CommandLinePopup は、コマンドライン（:）と検索（/、?）の入力を、画面の最後の行の代わりに、画面の上の中央のポップアップの枠の中に
	// 描くかである（FR-215。noice.nvim の cmdline_popup と command_palette に当たる）。範囲はエディタ全体で、既定は Vim と Neovim と同じく
	// 最後の行に描く。補完の候補の一覧は、入力の枠のすぐ下に、枠と同じ幅で描く（FR-217）。描くたびに読む。
	// Vim と Neovim に対応する設定は無いので、:set の別名は作らない。
	CommandLinePopup = "commandLine.popup"
	// CompletionMenuHeight は、補完の候補の一覧（ポップアップの一覧）に出す行の数の上限である（FR-197。Vim の 'pumheight'）。
	// 0 なら画面に収まるだけ出す（Vim と同じ）。範囲は 0 から 9999 である（scroll.margin と同じ上限）。描くたびに読む。
	// 名前をコマンドラインの下に置かないのは、後に作るインサートモードの補完の一覧も同じ上限を使うためである（Vim の 'pumheight' と同じ）。
	CompletionMenuHeight = "completionMenu.height"
	// IntroShow は、引数なしで起動して名前の無い空のバッファだけを開いているときに、編集の client の画面の中央に起動の案内を出すかである
	// （FR-206。Vim と Neovim の 'shortmess' に I が無いとき）。範囲はエディタ全体で、既定は Vim と Neovim と同じく出す。
	// 描くたびに読み、偽を読んだら、その後に真へ戻しても起動の案内は出さない（Neovim 0.12.5 の :set shortmess+=I の後の :set shortmess-=I と同じ）。
	// :intro（FR-207）は、この値によらず出す。
	IntroShow = "intro.show"
)

// CompleteModeWords は、commandLine.completeMode の語の候補である（Def.Words）。
var CompleteModeWords = &WordList{Words: []string{"full", "longest", "noselect"}, MaxParts: 4}

// defs は、設定の表である。項目を足すときは、この表に行を足す。
// 順は、Defs が返す一覧の順である。
var defs = []Def{
	{Name: FileType, Type: String, Scope: ScopeBuffer, Default: StringValue(""), Detected: true},
	{Name: LineNumberShow, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	{Name: LineNumberMinWidth, Type: Int, Scope: ScopeEditor, Default: IntValue(4), Range: &IntRange{Min: 1, Max: 20}},
	{Name: LineWrapEnabled, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	{Name: WhitespaceShow, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	// 上限は indent.width と同じにした。Vim は上限を持たない（大きな値は表示領域の半分と同じに働く）。
	{Name: ScrollMargin, Type: Int, Scope: ScopeEditor, Default: IntValue(0), Range: &IntRange{Min: 0, Max: 9999}},
	{Name: ScrollSideMargin, Type: Int, Scope: ScopeEditor, Default: IntValue(0), Range: &IntRange{Min: 0, Max: 9999}},
	{Name: SearchHighlight, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	// 下限は、打つたびの検索をしない値（0）を作らないため 1 にした。上限は yank.highlightMs と同じ 10 秒にした。
	{Name: SearchIncrementalTimeoutMs, Type: Int, Scope: ScopeEditor, Default: IntValue(500), Range: &IntRange{Min: 1, Max: 10000}},
	{Name: SearchShowCount, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	// 上限は、色を一瞬だけ付ける設定なので 10 秒にした（ミリ秒を時間の長さにしたときに溢れないためでもある）。
	{Name: YankHighlightMs, Type: Int, Scope: ScopeEditor, Default: IntValue(0), Range: &IntRange{Min: 0, Max: 10000}},
	// 境目の上限は、端末の列数として現実に無い大きさにした。0 にすると、プレビューの欄を持つ窓では常に出す。
	{Name: PreviewMinWindowWidth, Type: Int, Scope: ScopeEditor, Default: IntValue(100), Range: &IntRange{Min: 0, Max: 10000}},
	{Name: PreviewWidthPercent, Type: Int, Scope: ScopeEditor, Default: IntValue(60), Range: &IntRange{Min: 1, Max: 99}},
	// 下限は、窓が一覧とプレビューに分けられないほど狭くならないように 10 にした。
	{Name: PreviewWindowWidthPercent, Type: Int, Scope: ScopeEditor, Default: IntValue(90), Range: &IntRange{Min: 10, Max: 100}},
	{Name: IconsShow, Type: Bool, Scope: ScopeEditor, Default: BoolValue(true)},
	{Name: IndentUseSpaces, Type: Bool, Scope: ScopeBuffer, Default: BoolValue(false)},
	{Name: IndentWidth, Type: Int, Scope: ScopeBuffer, Default: IntValue(8), Range: &IntRange{Min: 0, Max: 9999}},
	{Name: TabWidth, Type: Int, Scope: ScopeBuffer, Default: IntValue(8), Range: &IntRange{Min: 1, Max: 9999}},
	{Name: IndentAuto, Type: Bool, Scope: ScopeBuffer, Default: BoolValue(false)},
	{Name: EditorConfigEnabled, Type: Bool, Scope: ScopeEditor, Default: BoolValue(true)},
	{Name: UndoPersist, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	{Name: CursorRestore, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	{Name: CommentString, Type: String, Scope: ScopeBuffer, Default: StringValue(""), Contains: "%s"},
	{Name: CursorHoldDelayMs, Type: Int, Scope: ScopeEditor, Default: IntValue(4000), Range: &IntRange{Min: 0, Max: 60000}},
	{Name: DiagnosticPopup, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	{Name: ProgressShow, Type: Bool, Scope: ScopeEditor, Default: BoolValue(true)},
	// 上限は cursorHold.delayMs と同じ 1 分にした。
	{Name: ProgressDoneMs, Type: Int, Scope: ScopeEditor, Default: IntValue(3000), Range: &IntRange{Min: 0, Max: 60000}},
	{Name: MouseEnabled, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	{Name: MouseScrollLines, Type: Int, Scope: ScopeEditor, Default: IntValue(3), Range: &IntRange{Min: 0, Max: 9999}},
	{Name: MouseScrollColumns, Type: Int, Scope: ScopeEditor, Default: IntValue(6), Range: &IntRange{Min: 0, Max: 9999}},
	{Name: ScrollbarShow, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	{Name: ScrollbarCurrentOnly, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	{Name: ScrollbarMarks, Type: String, Scope: ScopeEditor, Default: StringValue("diagnostic,search")},
	{Name: ScrollbarExcludedFileTypes, Type: String, Scope: ScopeEditor, Default: StringValue("")},
	{Name: CommandLineCompleteMode, Type: String, Scope: ScopeEditor, Default: StringValue("full"), Words: CompleteModeWords},
	{Name: CommandLineCompleteIgnoreCase, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	{Name: CommandLinePopup, Type: Bool, Scope: ScopeEditor, Default: BoolValue(false)},
	{Name: CompletionMenuHeight, Type: Int, Scope: ScopeEditor, Default: IntValue(0), Range: &IntRange{Min: 0, Max: 9999}},
	{Name: IntroShow, Type: Bool, Scope: ScopeEditor, Default: BoolValue(true)},
}

// fileTypeDefaults は、本体のファイルの種類ごとの既定（パッケージの doc の 3 の段）である。
//
// 字下げ: Vim 9.1 と Neovim 0.12.5 の runtime の ftplugin/make.vim と ftplugin/go.vim は、どちらも
// setlocal noexpandtab softtabstop=0 shiftwidth=0 とする（Makefile のレシピはタブ文字で始める。Go は gofmt がタブ文字で字下げする）。
// 2026-11-21 に読んで写した。タブ文字の表示幅（tab.width）は ftplugin が変えないので、ここでも変えない。
//
// コメントの形（comment.string。FR-169）: 本体のファイルの種類の判定（internal/editor の defaultFileTypes）が決める種類と lua について、
// Vim 9.1 と Neovim 0.12.5 の runtime の ftplugin/<種類>.vim（Neovim は <種類>.lua も）が setlocal commentstring で決める値を写した
// （2026-11-21。setf <種類> の後の &commentstring を両方で読んで確かめた）。
// lua は本体が判定しないが、設定の fileTypes で足すことの多い種類（ui のアイコンの表と同じ）で、#327 の受入条件にあるので入れた。
// Vim と Neovim で値が違うのは c だけで、Vim 9.1 の ftplugin/c.vim は "/* %s */"、Neovim 0.12.5 は ftplugin/c.vim の後に読む
// ftplugin/c.lua が "// %s" にする。本体の gc は Neovim に合わせるので、Neovim の値を写した。
// json の ftplugin（Vim 9.1 と Neovim 0.12.5 の ftplugin/json.vim）は setlocal commentstring= で空にするので、空を入れた
// （設定ファイルの最上位に書いた値も、json では空になる。Neovim の ftplugin と同じ）。
var fileTypeDefaults = map[string]map[string]Value{
	"make":       {IndentUseSpaces: BoolValue(false), IndentWidth: IntValue(0), CommentString: StringValue("# %s")},
	"go":         {IndentUseSpaces: BoolValue(false), IndentWidth: IntValue(0), CommentString: StringValue("// %s")},
	"gomod":      {CommentString: StringValue("// %s")},
	"markdown":   {CommentString: StringValue("<!-- %s -->")},
	"json":       {CommentString: StringValue("")},
	"jsonc":      {CommentString: StringValue("// %s")},
	"python":     {CommentString: StringValue("# %s")},
	"c":          {CommentString: StringValue("// %s")},
	"javascript": {CommentString: StringValue("// %s")},
	"typescript": {CommentString: StringValue("// %s")},
	"rust":       {CommentString: StringValue("// %s")},
	"sh":         {CommentString: StringValue("# %s")},
	"yaml":       {CommentString: StringValue("# %s")},
	"toml":       {CommentString: StringValue("# %s")},
	"lua":        {CommentString: StringValue("-- %s")},
}

// FileTypeDefaults は、本体のファイルの種類 fileType の既定の値（正規の名前から値への対応）の写しを返す。無ければ nil を返す。
// バッファを開くとき（と、ファイルの種類が変わったとき）に、設定ファイルの最上位の値の後に当てる（パッケージの doc の 3 の段）。
func FileTypeDefaults(fileType string) map[string]Value {
	return maps.Clone(fileTypeDefaults[fileType])
}

// Defs は、設定の表の写しを表の順に返す（設定ファイルの読み込みと、コマンドラインの補完の候補。FR-195）。
func Defs() []Def {
	out := make([]Def, len(defs))
	copy(out, defs)
	return out
}

// Lookup は、正規の名前が name の項目を返す。無ければ ok は false である。名前の省略は受け付けない。
func Lookup(name string) (Def, bool) {
	for _, d := range defs {
		if d.Name == name {
			return d, true
		}
	}
	return Def{}, false
}

// 値を読むときの誤り。プログラムの内部の誤りなので文言は英語にし、画面に出す呼び出し側が errors.Is で見分けて、
// 日本語の文言と Vim のエラー番号を付ける。
var (
	// ErrInvalidBool は、真偽の項目の値が true でも false でもないことを表す（Vim の E474 に当たる）。
	ErrInvalidBool = errors.New("option: value must be true or false")
	// ErrNumberRequired は、整数の項目の値が 10 進の整数でないことを表す（Vim の E521 に当たる）。
	ErrNumberRequired = errors.New("option: value must be a decimal integer")
	// ErrTypeMismatch は、Values.Set に渡した値の型が項目の型と違うことを表す（呼び出し側の誤り）。
	ErrTypeMismatch = errors.New("option: value type does not match the option type")
	// ErrOutOfRange は、整数の項目の値が範囲（Def.Range）の外にあることを表す。ErrBelowMin と ErrAboveMax はこれを包む。
	ErrOutOfRange = errors.New("option: value is out of range")
	// ErrBelowMin は、整数の項目の値が範囲の下限より小さいことを表す（下限が 1 の項目では、Vim の E487 に当たる）。
	ErrBelowMin = fmt.Errorf("%w: below the minimum", ErrOutOfRange)
	// ErrAboveMax は、整数の項目の値が範囲の上限より大きいことを表す（Vim の 'numberwidth' の 21 以上と同じく E474 に当たる）。
	ErrAboveMax = fmt.Errorf("%w: above the maximum", ErrOutOfRange)
	// ErrMissingContains は、文字列の項目の空でない値が、含まなければならない文字列（Def.Contains）を含まないことを表す
	// （comment.string では、Vim 9.1 と Neovim 0.12.5 の 'commentstring' の E537 に当たる）。
	ErrMissingContains = errors.New("option: value must be empty or contain the required text")
	// ErrStringRequired は、文字列の項目の JSON の値が文字列でないことを表す（Def.ParseJSON）。
	ErrStringRequired = errors.New("option: value must be a string")
	// ErrInvalidWords は、文字列の項目の値が、語の列（Def.Words）として読めないことを表す
	// （commandLine.completeMode では、Vim 9.1 と Neovim 0.12.5 の 'wildmode' の E474 に当たる）。
	ErrInvalidWords = errors.New("option: value must be a list of known words")
)

// Check は、値 v が項目 d に入れられる値かを確かめる。型が項目の型と違えば ErrTypeMismatch を返す。
// 整数の項目に範囲（d.Range）があれば、下限より小さい値に ErrBelowMin を、上限より大きい値に ErrAboveMax を返す（FR-136）。
// 文字列の項目に含まなければならない文字列（d.Contains）があれば、それを含まない空でない値に ErrMissingContains を返す（FR-169）。
// 文字列の項目に語の候補（d.Words）があれば、語の列として読めない値に ErrInvalidWords を返す（FR-196）。
// 値を項目に入れる経路（コマンドラインの文字列から読む Parse、設定ファイルの JSON の値から読む internal/config、Values.Set）は、
// どれも型の変換の後にこれを通る（FR-131）。項目ごとの値の制約を表に足すときは、項目の名前で分けず、表の欄にしてここで確かめる。
//
// req: FR-131, FR-136, FR-169, FR-196
func (d Def) Check(v Value) error {
	if v.Type() != d.Type {
		return ErrTypeMismatch
	}
	if d.Contains != "" && v.Type() == String && v.String() != "" && !strings.Contains(v.String(), d.Contains) {
		return ErrMissingContains
	}
	if d.Words != nil && v.Type() == String {
		if _, err := d.Words.Parse(v.String()); err != nil {
			return err
		}
	}
	if r := d.Range; r != nil && v.Type() == Int {
		switch {
		case v.Int() < r.Min:
			return ErrBelowMin
		case v.Int() > r.Max:
			return ErrAboveMax
		}
	}
	return nil
}

// Parse は、文字列 text を項目の型の値に変換し、Check で確かめて返す。
// 真偽は true と false だけ、整数は 10 進（先頭の - を許し、+ と 0x は許さない）、文字列は text をそのまま値にする（空も許す）。
// 変換できなければ ErrInvalidBool か ErrNumberRequired を（int に収まらない整数は ParseInt の ErrBelowMin か ErrAboveMax を）、
// 確かめて誤りなら Check の誤りを返す。
func (d Def) Parse(text string) (Value, error) {
	v, err := d.convert(text)
	if err != nil {
		return Value{}, err
	}
	if err := d.Check(v); err != nil {
		return Value{}, err
	}
	return v, nil
}

// ParseJSON は、JSON の値 raw を項目の型の値に変換し、Check で確かめて返す（拡張機能 API の option/set。#349。FR-190）。
// 真偽は JSON の true と false、整数は JSON の数のうち 10 進の整数の書き方のもの（ParseInt で読む。1.0 と 1e3 は断る）、
// 文字列は JSON の文字列だけを受け付ける。型が違えば ErrInvalidBool、ErrNumberRequired、ErrStringRequired を
// （int に収まらない整数は ParseInt の ErrBelowMin か ErrAboveMax を）、確かめて誤りなら Check の誤りを返す。
// 変換の規則は設定ファイル（internal/config の source.convertOption）と同じで、値の検査は :option.set と同じ Check である。
//
// req: FR-190
func (d Def) ParseJSON(raw []byte) (Value, error) {
	v, err := d.convertJSON(bytes.TrimSpace(raw))
	if err != nil {
		return Value{}, err
	}
	if err := d.Check(v); err != nil {
		return Value{}, err
	}
	return v, nil
}

// convertJSON は、JSON の値 raw を項目の型の値に変換する。値の検査はしない（Check）。
func (d Def) convertJSON(raw []byte) (Value, error) {
	switch d.Type {
	case Bool:
		switch string(raw) {
		case "true":
			return BoolValue(true), nil
		case "false":
			return BoolValue(false), nil
		}
		return Value{}, ErrInvalidBool
	case Int:
		n, err := ParseInt(string(raw))
		if err != nil {
			return Value{}, err
		}
		return IntValue(n), nil
	case String:
		if len(raw) == 0 || raw[0] != '"' {
			return Value{}, ErrStringRequired
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return Value{}, ErrStringRequired
		}
		return StringValue(s), nil
	}
	return Value{}, ErrTypeMismatch
}

// MarshalJSON は、値を型の JSON の値（true か false、整数、文字列）にする（拡張機能 API の option/get。#349）。
// どの型でもない値（ゼロ値）は null にする。
func (v Value) MarshalJSON() ([]byte, error) {
	switch v.typ {
	case Bool:
		return []byte(strconv.FormatBool(v.b)), nil
	case Int:
		return []byte(strconv.Itoa(v.n)), nil
	case String:
		return json.Marshal(v.s)
	}
	return []byte("null"), nil
}

// convert は、文字列 text を項目の型の値に変換する。値の検査はしない（Check）。
func (d Def) convert(text string) (Value, error) {
	switch d.Type {
	case Bool:
		switch text {
		case "true":
			return BoolValue(true), nil
		case "false":
			return BoolValue(false), nil
		}
		return Value{}, ErrInvalidBool
	case Int:
		n, err := ParseInt(text)
		if err != nil {
			return Value{}, err
		}
		return IntValue(n), nil
	case String:
		return StringValue(text), nil
	}
	return Value{}, ErrTypeMismatch
}

// ParseInt は、整数の項目の値の文字列 text を読む。10 進（先頭の - を許し、+ と 0x は許さない）で、読めなければ ErrNumberRequired を返す。
// 数字だけの文字列が int に収まらなければ、符号で、正なら ErrAboveMax、負なら ErrBelowMin を返す（どちらも ErrOutOfRange を包む）。
// 収まらない値はどの範囲の外でもあるとして扱うためで、Vim 9.1 も :set numberwidth=99999999999999999999 を E474、
// -99999999999999999999 を E487 にする（2026-11-20 に確かめた。PR #272 のレビューの推奨 2）。
// コマンドラインの文字列（Def.Parse）と設定ファイルの JSON の数（internal/config）は、どちらもこれで読む。項目の名前では分けない。
//
// req: FR-136
func ParseInt(text string) (int, error) {
	if text == "" || text[0] == '+' {
		return 0, ErrNumberRequired
	}
	n, err := strconv.Atoi(text)
	switch {
	case errors.Is(err, strconv.ErrRange) && text[0] == '-':
		return 0, ErrBelowMin
	case errors.Is(err, strconv.ErrRange):
		return 0, ErrAboveMax
	case err != nil:
		return 0, ErrNumberRequired
	}
	return n, nil
}

// Values は、1 つの範囲（エディタ全体か、1 つのバッファ）の項目の今の値である。
// 値を設定していない項目は、表の既定値を持つ。ゼロ値は、すべての項目が既定値の状態である。
// 範囲は呼び出し側が決める（エディタ全体の Values には ScopeEditor の項目だけを、バッファの Values には ScopeBuffer の項目だけを入れる）。
type Values struct {
	m map[string]Value
	// sets は、項目ごとに Set で値を入れた回数である（Revision）。
	sets map[string]uint64
}

// Get は、項目 name の今の値を返す。表に無い名前ならゼロ値を返す。
func (vs *Values) Get(name string) Value {
	if v, ok := vs.m[name]; ok {
		return v
	}
	d, _ := Lookup(name)
	return d.Default
}

// Lookup は、項目 name に Set で値を入れてあれば、その値と true を返す。入れていなければ（表の既定値のままなら）、ゼロ値と false を返す。
// 2 つの Values を重ねて読む使い方（利用者が :set で変えた値を、初めの値の段の値より優先する。internal/editor）に使う。
func (vs *Values) Lookup(name string) (Value, bool) {
	v, ok := vs.m[name]
	return v, ok
}

// Set は、項目 d の値を v にする。v が d に入れられない値（Def.Check。型が違うなど）なら、値を変えずにその誤りを返す。
func (vs *Values) Set(d Def, v Value) error {
	if err := d.Check(v); err != nil {
		return err
	}
	if vs.m == nil {
		vs.m, vs.sets = map[string]Value{}, map[string]uint64{}
	}
	vs.m[d.Name] = v
	vs.sets[d.Name]++
	return nil
}

// Revision は、項目 name に Set で値を入れた回数を返す。前と同じ値を入れても数える。誤りで入れなかった Set は数えない。
// 値が同じままでも「設定し直した」ことを、使う側が見分けるための数である。
// 一致の色を消した状態（internal/command/ex の :search.clearHighlight。FR-138）は、消したときの search.highlight の回数を覚えておき、
// 回数が変われば（:set hlsearch を打ち直せば）消した状態を効かなくする。項目の名前によらず、どの項目も数える。
//
// req: FR-138
func (vs *Values) Revision(name string) uint64 {
	return vs.sets[name]
}
