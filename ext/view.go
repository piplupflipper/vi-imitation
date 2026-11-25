package ext

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// ポップアップと表示の部品（基本設計 1.4、5.3 の段階 3 の表。FR-68〜FR-72）。
//
// 拡張機能は、ポップアップに出す中身を表示の部品（テキスト、一覧、木、入力の行）のデータで渡す（FR-72）。
// 描き方は本体（internal/ui）が決め、拡張機能が端末のセルを直接描く手段は持たない（#177 の裁定）。
// 本体は、開いたポップアップに ID（ViewID）を付けて応答し、以後の更新、閉じる、利用者の操作の知らせは、この ID で指す。
//
// メソッドと向き（名前は仮）:
//
//   - popup/open（拡張機能 → 本体、リクエスト）: PopupOpenParams で部品を渡して開く。応答は PopupOpenResult
//   - pane/open（拡張機能 → 本体、リクエスト）: PaneOpenParams で部品を渡し、端末多重化ソフトで分割したペインに開く（FR-115）。
//     応答は PaneOpenResult。ペインもポップアップと同じ ID で指し、以下の通知はペインにも使う
//   - view/update（拡張機能 → 本体、通知）: 開いたポップアップの部品の中身を置き換える（ViewUpdateParams）
//   - view/close（拡張機能 → 本体、通知）: 開いたポップアップを閉じる（ViewCloseParams）。本体は view/closed を返さない
//   - view/closed（本体 → 拡張機能、通知）: 利用者が閉じたか、本体が閉じた（別のポップアップで置き換えた、client が終わった）ことを
//     知らせる（ViewClosedParams）。その ID はもう使えない
//   - view/select（本体 → 拡張機能、通知）: 一覧か木の項目を利用者が選んだ（<CR>）こと、入力の行で <CR> を打ったことを知らせる（ViewSelectParams）
//   - input/change（本体 → 拡張機能、通知）: 入力の行の中身が変わったことを知らせる（InputChangeParams）
//   - view/key（本体 → 拡張機能、通知）: 宣言ファイルの contributes.popupKeys のキーを、その拡張機能のポップアップの中で打ったことを
//     知らせる（ViewKeyParams。FR-69）
//   - view/expand（本体 → 拡張機能、通知）: 木の項目を利用者が開閉したことを知らせる（ViewExpandParams）
//   - view/tabChange（本体 → 拡張機能、通知）: 分割したペインを開いた元の client のカーソルのあるタブが替わったことを知らせる
//     （ViewTabChangeParams。API 0.10.0 で足した）
//   - view/append（拡張機能 → 本体、通知）: 開いたテキストの部品の末尾へテキストを書き足す（ViewAppendParams。API 0.20.0 で足した）。
//     API 0.22.0 から、書き足すテキストと入力の行の組（ViewConsole）の出力にも書き足せる
//   - view/childSelect（本体 → 拡張機能、通知）: 利用者が切り替えの部品（ViewSwitcher）の子を切り替えたことを知らせる
//     （ViewChildSelectParams。API 0.23.0 で足した）
//
// 本体は、閉じた後や知らない ID の view/update、view/append、view/close を黙って捨てる（閉じた知らせと行き違いうるため）。
//
// # 切り替えの部品（API 0.23.0。FR-269〜FR-271）
//
// ViewSwitcher は、ほかの種類の部品を子（ViewChild）として並べて持ち、窓の最初の行（見出しの行）に子の名前を並べ、
// 選んでいる 1 つの子だけをその下に出す。子は拡張機能が決めた変わらない ID（ViewChild.ID）で指す。
// view/update と view/append の Child で子を指すと、その子だけの中身を置き換えるか書き足す。見えていない子への置き換えと書き足しも、
// 本体がその場で子の中身に当てる。切り替えの部品の中で起きた知らせ（view/select、view/key、view/expand、input/change）には、
// 子の ID（Child）を付ける。ほかの部品の知らせには付けない（FR-271）。
const (
	// MethodPopupOpen は、拡張機能から本体へのリクエストである。params は PopupOpenParams、result は PopupOpenResult。
	MethodPopupOpen = "popup/open"
	// MethodPaneOpen は、拡張機能から本体へのリクエストである。params は PaneOpenParams、result は PaneOpenResult。
	MethodPaneOpen = "pane/open"
	// MethodViewUpdate は、拡張機能から本体への通知である。params は ViewUpdateParams。
	MethodViewUpdate = "view/update"
	// MethodViewClose は、拡張機能から本体への通知である。params は ViewCloseParams。
	MethodViewClose = "view/close"
	// MethodViewClosed は、本体から拡張機能への通知である。params は ViewClosedParams。
	MethodViewClosed = "view/closed"
	// MethodViewSelect は、本体から拡張機能への通知である。params は ViewSelectParams。
	MethodViewSelect = "view/select"
	// MethodInputChange は、本体から拡張機能への通知である。params は InputChangeParams。
	MethodInputChange = "input/change"
	// MethodViewKey は、本体から拡張機能への通知である。params は ViewKeyParams。
	MethodViewKey = "view/key"
	// MethodViewExpand は、本体から拡張機能への通知である。params は ViewExpandParams。
	MethodViewExpand = "view/expand"
	// MethodViewTabChange は、本体から拡張機能への通知である。params は ViewTabChangeParams。
	MethodViewTabChange = "view/tabChange"
	// MethodViewAppend は、拡張機能から本体への通知である。params は ViewAppendParams。
	MethodViewAppend = "view/append"
	// MethodViewChildSelect は、本体から拡張機能への通知である。params は ViewChildSelectParams。
	MethodViewChildSelect = "view/childSelect"
)

// ViewID は、本体が開いたポップアップに付けた番号である。本体の中で一意で、使い回さない。0 は使わない。
type ViewID uint64

// ViewKind は、表示の部品の種類である（基本設計 1.4）。
type ViewKind string

// 表示の部品の種類。
const (
	// ViewText は、テキストのバッファである。本体は、編集を禁止したファイルでないバッファとして出し、通常のノーマルモードのキー
	// （移動、検索、ヤンク）が効く（FR-71）。
	ViewText ViewKind = "text"
	// ViewList は、項目の一覧である。1 項目を 1 行に出し、<CR> で選ぶと view/select で項目の ID を知らせる。
	ViewList ViewKind = "list"
	// ViewTree は、項目の木である。子のある項目（HasChildren を含む）の <CR> は開閉で view/expand を送り、
	// 子の無い項目の <CR> は view/select で ID を知らせる。view/update では、項目の ID で開閉とカーソルの項目を保つ。
	ViewTree ViewKind = "tree"
	// ViewInput は、1 行の入力である。中身が変わるたびに input/change で知らせ、<CR> で view/select（Item は中身）を送る。
	ViewInput ViewKind = "input"
	// ViewFinder は、入力の行と一覧を組にした部品である（ファジーファインダー。FR-106。API 0.9.0 で足した）。
	// Text は入力の行の最初の中身、Items は一覧の項目（子を持たない）である。
	// フォーカスは入力の行に置き、開いた時点でインサートモードにする。入力の行が変わるたびに input/change（Seq 付き）で知らせ、
	// 拡張機能は絞り込んだ一覧を view/update（同じ Seq 付き）で返す。本体は、最後に知らせた Seq より古い Seq の一覧を出さない。
	// view/update は一覧（Items）と見出し（Title）だけを置き換え、入力の行の中身（Text）は読まない（利用者が打ち続けているため）。
	// 一覧の中のカーソルは <C-n>、<C-p>、<C-j>、<C-k>、<Down>、<Up> で動かし、<CR> で view/select（Item は一覧の項目の ID、
	// Input は入力の行の中身）を送る。<Esc> はインサートモードでも閉じる。
	ViewFinder ViewKind = "finder"
	// ViewConsole は、書き足すテキストと 1 行の入力の行を組にした部品である（デバッガーの REPL、ACP のプロンプトと返答の欄。
	// FR-274。API 0.22.0 で足した）。上に出力（Text と Highlights。view/append で書き足すテキストのバッファ）を、下に入力の行
	// （Prompt の後に Input）を置く。開いた時点で入力の行にフォーカスを置いてインサートモードにし、出力は最後の行から見せて末尾に追従する。
	// 入力の行の <CR>（ノーマルモードとインサートモード）で view/select（Item は入力の行の中身。空でも送る）を送り、
	// 空でない中身を入力の履歴に積んで入力の行を空にする（モードは変えない）。送ったときは出力を末尾へ戻して追従を続ける。
	// 入力の行は改行を持たず、貼り付けた改行は空白になる。インサートモードの <Up> と <Down> で履歴をたどる。
	// 履歴は部品ごとに本体が持ち、閉じたら捨てる。拡張機能は、開き直すときに受けた中身を History で渡す（AppendConsoleHistory）。
	// インサートモードの <Esc> はノーマルモードへ戻り、閉じない。<C-w>w で出力と入力の行の間でフォーカスを移す。
	// view/update は出力（Text、Highlights）と Prompt だけを置き換え、Input と History は読まない（利用者が打っているため）。
	// Complete を真にして開くと、入力の行を拡張機能が console/complete で返す候補で補う（API 0.24.0 から。consolecomplete.go）。
	ViewConsole ViewKind = "console"
	// ViewSwitcher は、ほかの種類の部品を子（Children）として並べ、見出しの行で選んだ 1 つの子だけを出す部品である
	// （FR-269。API 0.23.0 で足した。nvim-dap-view の見出しの行に当たる）。子にできるのは ViewText、ViewList、ViewTree、ViewConsole で、
	// ViewSwitcher、ViewInput、ViewFinder は子にできない。Selected は選ぶ子の ID で、省くと最初の子である。
	// 本体は子ごとに中身（カーソル、スクロール、木の開閉、入力の行と履歴）を持ち、切り替えても捨てない。
	// 子を切り替える本体の操作は view.child-next、view.child-prev、view.child-first、view.child-last（既定のキーは ]v、[v、[V、]V）で、
	// 子の Key、見出しの行の子の名前のクリック、contributes.popupKeys の子を切り替える action でも切り替える。
	// 利用者が切り替えたら view/childSelect で知らせる。拡張機能が view/update の Selected で替えたときは知らせない。
	// 置き場所は pane/open の split と center、popup/open の center で、popup/open の cursor は断る。
	ViewSwitcher ViewKind = "switcher"
)

// ViewChild は、切り替えの部品（ViewSwitcher）の子 1 つである（FR-269。API 0.23.0 で足した）。
type ViewChild struct {
	// ID は、部品の中で一意な空でない文字列で、改行を含めない。子を指す view/update と view/append、子を指す知らせ、
	// 宣言ファイルの popupKeys の children で使う。
	ID string `json:"id"`
	// Title は、見出しの行に出す子の名前である。改行を含めない。
	Title string `json:"title"`
	// Key は、その子へ直に切り替えるノーマルモードの 1 つのキー（"S"、"<C-x>" など。キーの表記は popupKeys と同じ）である。省ける。
	// 部品の中のノーマルモードでだけ効き、contributes.popupKeys と本体の既定のキーより先に引く。見出しの行の名前の後に [S] の形で出す。
	// 本体は、キーとして読めない表記、2 つ以上のキー、本体の部品の操作と重なるキー（<CR>、<Esc>、:、<C-w>）を持つ部品を、
	// popup/open と pane/open では誤りで断り、view/update では捨てる（キーの表記は本体が読むので、Validate では確かめない）。
	Key string `json:"key,omitempty"`
	// Component は、子の部品である。Title は読まない（子の名前は ViewChild.Title で出す）。
	Component ViewComponent `json:"component"`
}

// ChildKind は、k が切り替えの部品の子にできる部品の種類かを返す。
func (k ViewKind) ChildKind() bool {
	switch k {
	case ViewText, ViewList, ViewTree, ViewConsole:
		return true
	case ViewInput, ViewFinder, ViewSwitcher:
	}
	return false
}

// ConsoleHistoryMax は、ViewConsole の入力の履歴の件数の上限である（基本設計 1.4。仮）。超えたら古いものから捨てる。
const ConsoleHistoryMax = 1000

// AppendConsoleHistory は、ViewConsole の入力の履歴 h の末尾に、送った中身 s を積んだ履歴を返す（基本設計 1.4 の「書き足すテキストと
// 入力の行の組」）。s が空か、直前に積んだものと同じなら積まない。件数が ConsoleHistoryMax を超えたら古いものから捨てる。
// 本体は view/select を送るたびにこの規則で積む。拡張機能は、view/select で受けた中身を同じ関数で積んでおき、開き直すときに
// ViewComponent.History で渡すと、本体が閉じる前に持っていた履歴と同じになる。h の後ろの容量を使うことがある（append と同じ）。
func AppendConsoleHistory(h []string, s string) []string {
	if s == "" || (len(h) > 0 && h[len(h)-1] == s) {
		return h
	}
	h = append(h, s)
	if d := len(h) - ConsoleHistoryMax; d > 0 {
		h = append(h[:0:0], h[d:]...)
	}
	return h
}

// Known は、k がこの版の API の知っている部品の種類かを返す。
func (k ViewKind) Known() bool {
	switch k {
	case ViewText, ViewList, ViewTree, ViewInput, ViewFinder, ViewConsole, ViewSwitcher:
		return true
	}
	return false
}

// ViewComponent は、ポップアップに出す表示の部品 1 つである。Kind によって使う項目が違う。
type ViewComponent struct {
	Kind ViewKind `json:"kind"`
	// Title は、ポップアップの枠の上に出す見出しである。空なら出さない。
	Title string `json:"title,omitempty"`
	// Text は、ViewText の本文と、ViewConsole の出力の最初の中身と、ViewInput と ViewFinder の入力の行の最初の中身である。
	// ViewInput と ViewFinder では改行を含めない。
	Text string `json:"text,omitempty"`
	// Format は、ViewText の本文の形式である。PopupMarkdown なら、本体はコードの囲みの行を除いて出す（popup/show と同じ）。
	Format PopupFormat `json:"format,omitempty"`
	// Highlights は、ViewText の本文と ViewConsole の出力に色を付ける範囲である。位置は Format の整形の前の Text を指す（4.1 の位置）。
	// Markdown で本体が除くコードの囲みの行の中の範囲（始まりか終わりがその行にあるもの）は捨てる。
	// Style は 7.1 の色の名前（"keyword"、"diagnostic.error" など）で、知らない名前には色を付けない。
	Highlights []Decoration `json:"highlights,omitempty"`
	// Items は、ViewList、ViewTree、ViewFinder の項目である。ViewTree では Children で木にする。
	Items []ViewItem `json:"items,omitempty"`
	// Cursor は、ViewList と ViewTree で、カーソルを置く項目の ID である（API 0.10.0 で足した）。
	// 開いたとき（popup/open、pane/open）と view/update で、その項目の行へカーソルを置く。
	// 空のときと、その項目が画面に出ていない（ID が無いか、閉じた木の項目の子にある）ときは、カーソルを置く行を変えない
	// （view/update では、前にカーソルのあった項目を追う）。
	// ファイラの「今のファイルの場所まで開いてカーソルを置く」と、h で親の項目へ移るのに使う（FR-105）。他の種類では読まない。
	Cursor string `json:"cursor,omitempty"`
	// Preview は、ViewList、ViewTree、ViewFinder で、項目のプレビュー（ViewItem.Preview）を出す欄を持つことを表す
	// （FR-149。API 0.11.0 で足した）。省いても、項目のどれかが Preview を持てば欄を持つ。
	// 項目が後から届く部品（ファインダー）は、開いたときから true にしておくと、最初の項目が届いたときに窓の幅が変わらない。
	// 欄を持つ部品の窓は、幅が設定の境目を超えるときだけ広げて右にプレビューを出す（基本設計 1.4 の「#288 のプレビュー」）。
	Preview bool `json:"preview,omitempty"`
	// Tail は、ViewText で、開いたとき（popup/open、pane/open）と view/update で、カーソルを最後の行に置くことを表す
	// （API 0.20.0 で足した）。末尾に追従する表示（view/append で書き足す出力。FR-272）を、末尾から見せて追従させて始めるのに使う。
	// ext/server の TextLog は、開き直す部品と付け替える部品に立てる。省くと、開いたときは先頭の行に置き、
	// view/update では今までの規則（カーソルが最後の行にあれば最後の行）にする。他の種類では読まない。古い本体は読まずに捨てる。
	Tail bool `json:"tail,omitempty"`
	// Input は、ViewConsole の入力の行の最初の中身である（API 0.22.0 で足した）。改行を含めない。view/update では読まない。
	Input string `json:"input,omitempty"`
	// Prompt は、ViewConsole の入力の行の前に置く前置き（"> " など）である（API 0.22.0 で足した）。改行を含めない。空なら置かない。
	// view/update で置き換える。
	Prompt string `json:"prompt,omitempty"`
	// History は、ViewConsole の入力の履歴の最初の中身（古い順）である（API 0.22.0 で足した）。どの項目も改行を含めない。
	// ConsoleHistoryMax を超える分は、本体が古いものから捨てる。view/update では読まない。
	History []string `json:"history,omitempty"`
	// Complete は、ViewConsole で、入力の行の補完を拡張機能が引き受けることを表す（FR-297。API 0.24.0 で足した）。
	// 真なら、本体はインサートモードで入力の行が変わるたびと補完のキーで console/complete（ConsoleCompleteParams）を問い合わせ、
	// 返った候補を補完の候補の一覧に出す（consolecomplete.go）。開いたとき（popup/open、pane/open、切り替えの部品の新しい子）だけ読み、
	// view/update では読まない。偽か省いた部品は、今までどおり補完しない（<Tab> はタブ文字として入る）。他の種類では持てない。
	// 古い本体は読まずに捨て、問い合わせない。
	Complete bool `json:"complete,omitempty"`
	// Children は、ViewSwitcher の子の列である（API 0.23.0 で足した）。1 つ以上を持つ。他の種類では持てない。
	Children []ViewChild `json:"children,omitempty"`
	// Selected は、ViewSwitcher で選ぶ子の ID である（API 0.23.0 で足した）。開いたとき（popup/open、pane/open）に省くと最初の子、
	// 部品の全体を置き換える view/update で省くと、選んでいる子を保つ（選んでいた子が無くなったら最初の子）。他の種類では持てない。
	Selected string `json:"selected,omitempty"`
}

// ChildIndex は、ViewSwitcher の c の子のうち、ID が id の子の添字を返す。無ければ -1 である。
func (c *ViewComponent) ChildIndex(id string) int {
	return slices.IndexFunc(c.Children, func(ch ViewChild) bool { return ch.ID == id })
}

// ViewPreview は、一覧、木、ファインダーの項目 1 つの、プレビューに出すものである（FR-149。API 0.11.0 で足した）。
// 拡張機能は出すものの場所だけを渡し、中身は本体が読む。拡張機能が、一覧の全部の項目の中身を先に読まなくてよいためである
// （ファイラの 4 万個の項目のディレクトリ。PR #290 のレビューの推奨 5）。
//
//   - Buffer があれば、その開いているバッファ（ファイルでないバッファを含む）の今の中身を出す。Path は読まない
//   - Path がファイルなら、その中身を出す。開いているバッファのファイルなら、ファイルでなくバッファの今の中身を出す
//   - Path がディレクトリなら、その中身の一覧を出す
//
// Line は、出す行（0 から数える）である。あればその行を窓の真ん中に置いて色を付け、無ければ先頭から出す。
// 本体は、中身を編集を禁止したバッファとして開き、ファイルの種類で構文の色を付ける。大きなファイル、読めないファイル、
// バイナリのファイルは、中身を出さずに理由を出す。
type ViewPreview struct {
	// Path は、ファイルかディレクトリのパスである。相対パスは、操作中の client の作業ディレクトリから数える。
	Path string `json:"path,omitempty"`
	// Buffer は、開いているバッファの番号である。
	Buffer *BufferID `json:"buffer,omitempty"`
	// Line は、出す行の番号（0 から数える）である。
	Line *int `json:"line,omitempty"`
}

// ViewFile は、一覧、木、ファインダーの項目 1 つが指すファイルかディレクトリである（FR-152。API 0.12.0 で足した）。
// 本体は、これを持つ項目の表示の文字列の前に、ファイルの種類ごとのアイコンを付けて描く（基本設計 1.4 の「#295 のアイコン」）。
// アイコンの文字と色は本体が決めるので、拡張機能はファイルの種類もアイコンも知らなくてよい。
// 項目の Highlights は、これまでどおり Label の中のバイトの位置で書く（本体がアイコンの分をずらす）。
//
// ViewItem.Preview（カーソルがあるときに何を見せるか）とは別に持つ。プレビューの無い項目（ファイラの木を狭い窓で出すとき）と、
// ファイルを指さないプレビュー（ファイルでないバッファのジャンプ）があるためである（PR #298 のレビューの推奨 4）。
type ViewFile struct {
	// Path は、ファイルかディレクトリのパスである。本体は、名前と拡張子からファイルの種類を決める（設定の fileTypes を含む）。
	// 本体はパスの先を調べないので、相対パスでも、無いファイルでもよい。
	Path string `json:"path"`
	// Dir は、ディレクトリなら true である。木では、開いたディレクトリと閉じたディレクトリでアイコンを分ける。
	Dir bool `json:"dir,omitempty"`
}

// ViewItem は、一覧か木の項目 1 つである。
type ViewItem struct {
	// ID は、選んだときに view/select で返す値である。部品の中で一意にする。
	ID string `json:"id"`
	// Label は、表示の文字列である。改行を含めない。
	Label string `json:"label"`
	// Highlights は、Label の中の色を付ける範囲である。
	Highlights []ViewSpan `json:"highlights,omitempty"`
	// Children は、ViewTree の子の項目である。ViewList では使わない。
	Children []ViewItem `json:"children,omitempty"`
	// HasChildren は、ViewTree で、Children が空でも子を持つ項目（子を後から読む項目）であることを表す。
	// 本体は開閉の印を付け、<CR> で開くと view/expand で知らせる。拡張機能は子を読んで view/update で送る
	// （ファイラのディレクトリ、DAP の変数の variablesReference。FR-105、FR-108）。ViewList では使わない。
	HasChildren bool `json:"hasChildren,omitempty"`
	// Expanded は、ViewTree で子を開いて出すかの指定である。開閉は本体が項目の ID ごとに持つ。
	// 省くと、前の木にあった ID の項目は利用者が開閉した状態を保ち、新しく現れた ID の項目は閉じて出す。
	// 書くと、前の木にあった項目もその状態にする（ファイラの「今のファイルの場所まで開く」、DAP の止まった位置のスコープを開く）。
	Expanded *bool `json:"expanded,omitempty"`
	// Preview は、この項目にカーソルがあるときにプレビューに出すものである（FR-149。API 0.11.0 で足した）。
	// 無ければ、プレビューの欄を空にする。古い本体は読まずに捨てる。
	Preview *ViewPreview `json:"preview,omitempty"`
	// File は、この項目が指すファイルかディレクトリである（FR-152。API 0.12.0 で足した）。
	// あれば、本体は Label の前にファイルの種類のアイコンを付けて描く。キー、コマンド、レジスターのような、
	// ファイルを指さない項目では省く。古い本体は読まずに捨て、アイコンを付けない。
	File *ViewFile `json:"file,omitempty"`
}

// HasPreview は、c がプレビューの欄を持つか（Preview が true か、項目のどれかが Preview を持つか）を返す。
func (c *ViewComponent) HasPreview() bool {
	if c.Preview {
		return true
	}
	var has func(items []ViewItem) bool
	has = func(items []ViewItem) bool {
		for _, it := range items {
			if it.Preview != nil || has(it.Children) {
				return true
			}
		}
		return false
	}
	return has(c.Items)
}

// ViewExpandParams は、view/expand の params である。
// 木の子を持つ項目（Children か HasChildren のある項目）を、利用者が <CR> で開いたか閉じたかを知らせる。
type ViewExpandParams struct {
	View ViewID `json:"view"`
	// Item は、開閉した項目の ID である。
	Item string `json:"item"`
	// Expanded は、開いたなら true、閉じたなら false である。
	Expanded bool `json:"expanded"`
	// Child は、切り替えの部品（ViewSwitcher）の中の知らせなら、その子の ID である。ほかの部品では省く（FR-271。API 0.23.0 で足した）。
	Child string `json:"child,omitempty"`
}

// ViewChildSelectParams は、view/childSelect の params である（FR-270。API 0.23.0 で足した）。
// 利用者の操作（本体の操作 view.child-*、子の Key、見出しの行のクリック、popupKeys の子を切り替える action）で、
// 切り替えの部品の選ぶ子が替わったことを知らせる。拡張機能の view/update の Selected で替えたときは送らない。
// 拡張機能は、見えていない子の中身を作らずにおき、この知らせを受けてから作ってよい。
type ViewChildSelectParams struct {
	View ViewID `json:"view"`
	// Child は、選んだ子の ID である。
	Child string `json:"child"`
}

// ViewTabChangeParams は、view/tabChange の params である（API 0.10.0 で足した）。
// 本体は、pane/open で分割したペインに出した部品の持ち主へ、そのペインを開いた元の client のカーソルのあるタブ
// （中央のポップアップにフォーカスがあれば、それを開いたときのタブ）が替わるたびに、替わった後のタブを知らせる。
// ペインを開いた時点のタブは知らせない（拡張機能は editor/state で知る）。
// ファイラが、出しっぱなしにしたペインの木を今のファイルへ追従させる（nvim の neo-tree の follow_current_file。FR-105）のに使う。
// 中央のポップアップ（ペインに代えて出したものを含む）では送らない。開いている間はそれだけを操作するので、タブが替わらないためである。
type ViewTabChangeParams struct {
	View ViewID `json:"view"`
	// Buffer は、替わった後のタブのバッファの番号である。
	Buffer BufferID `json:"buffer"`
	// Path は、そのバッファのファイルの絶対パスである。ファイルでないバッファでは空である。
	Path string `json:"path,omitempty"`
}

// ViewSpan は、項目の表示の文字列の中の、色を付けるバイトの範囲 [Start, End) である。
type ViewSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
	// Style は、7.1 の色の名前である。
	Style string `json:"style"`
}

// Validate は、部品の種類が既知であることと、項目の ID が空でなく部品の中で重複しないこと、
// 表示の文字列と入力の行の中身が改行を含まないこと、範囲が表示の文字列の中にあること、
// プレビュー（Preview）と項目の指すファイル（File）の形を検査する。
// 本体は、誤りのある popup/open に JSON-RPC のエラーで応答し、誤りのある view/update は捨てる。
//
// ViewSwitcher では、子が 1 つ以上あること、子の ID が空でなく改行を含まず重複しないこと、子の Title が改行を含まないこと、
// 子の Key が重複しないこと、子の部品の種類が子にできるもの（ViewKind.ChildKind）であること、子の部品ごとの Validate
// （項目の ID の重複は子ごとに数える）、Selected が子の ID のどれか（空なら最初の子）であることを検査する。
// ViewSwitcher でない部品は、Children と Selected を持てない。
func (c *ViewComponent) Validate() error {
	if !c.Kind.Known() {
		return fmt.Errorf("kind %q は知らない部品の種類である", c.Kind)
	}
	if c.Kind == ViewSwitcher {
		return c.validateSwitcher()
	}
	if len(c.Children) > 0 || c.Selected != "" {
		return fmt.Errorf("children と selected は switcher でだけ使える")
	}
	if c.Complete && c.Kind != ViewConsole {
		return fmt.Errorf("complete は console でだけ使える")
	}
	if (c.Kind == ViewInput || c.Kind == ViewFinder) && containsNewline(c.Text) {
		return fmt.Errorf("%s の text が改行を含む", c.Kind)
	}
	if c.Kind == ViewConsole {
		switch {
		case containsNewline(c.Input):
			return fmt.Errorf("console の input が改行を含む")
		case containsNewline(c.Prompt):
			return fmt.Errorf("console の prompt が改行を含む")
		case len(c.Items) > 0:
			return fmt.Errorf("console は items を持てない")
		}
		for i, h := range c.History {
			if containsNewline(h) {
				return fmt.Errorf("console の history[%d] が改行を含む", i)
			}
		}
	}
	seen := map[string]bool{}
	var check func(items []ViewItem, path string) error
	check = func(items []ViewItem, path string) error {
		for i, it := range items {
			p := fmt.Sprintf("%s[%d]", path, i)
			switch {
			case it.ID == "":
				return fmt.Errorf("%s.id が空である", p)
			case seen[it.ID]:
				return fmt.Errorf("%s.id %q が重複する", p, it.ID)
			case containsNewline(it.Label):
				return fmt.Errorf("%s.label が改行を含む", p)
			case c.Kind != ViewTree && (len(it.Children) > 0 || it.HasChildren):
				return fmt.Errorf("%s.children と hasChildren は tree でだけ使える", p)
			}
			seen[it.ID] = true
			if pv := it.Preview; pv != nil {
				switch {
				case pv.Path == "" && pv.Buffer == nil:
					return fmt.Errorf("%s.preview に path も buffer も無い", p)
				case pv.Line != nil && *pv.Line < 0:
					return fmt.Errorf("%s.preview.line %d が負である", p, *pv.Line)
				}
			}
			if it.File != nil && it.File.Path == "" {
				return fmt.Errorf("%s.file.path が空である", p)
			}
			for j, s := range it.Highlights {
				if s.Start < 0 || s.End < s.Start || s.End > len(it.Label) {
					return fmt.Errorf("%s.highlights[%d] が label の外を指す", p, j)
				}
			}
			if err := check(it.Children, p+".children"); err != nil {
				return err
			}
		}
		return nil
	}
	return check(c.Items, "items")
}

// validateSwitcher は、ViewSwitcher の c を検査する（Validate）。
func (c *ViewComponent) validateSwitcher() error {
	switch {
	case len(c.Children) == 0:
		return fmt.Errorf("switcher の children が空である")
	case c.Text != "" || len(c.Highlights) > 0 || len(c.Items) > 0 || c.Input != "" || c.Prompt != "" || len(c.History) > 0 || c.Complete:
		return fmt.Errorf("switcher は text、highlights、items、input、prompt、history、complete を持てない（子の component に書く）")
	}
	ids, keys := map[string]bool{}, map[string]bool{}
	for i, ch := range c.Children {
		p := fmt.Sprintf("children[%d]", i)
		switch {
		case ch.ID == "":
			return fmt.Errorf("%s.id が空である", p)
		case containsNewline(ch.ID):
			return fmt.Errorf("%s.id が改行を含む", p)
		case ids[ch.ID]:
			return fmt.Errorf("%s.id %q が重複する", p, ch.ID)
		case containsNewline(ch.Title):
			return fmt.Errorf("%s.title が改行を含む", p)
		case ch.Key != "" && keys[ch.Key]:
			return fmt.Errorf("%s.key %q が重複する", p, ch.Key)
		case !ch.Component.Kind.ChildKind():
			return fmt.Errorf("%s.component.kind %q は switcher の子にできない", p, ch.Component.Kind)
		}
		ids[ch.ID] = true
		if ch.Key != "" {
			keys[ch.Key] = true
		}
		if err := ch.Component.Validate(); err != nil {
			return fmt.Errorf("%s.component: %w", p, err)
		}
	}
	if c.Selected != "" && !ids[c.Selected] {
		return fmt.Errorf("selected %q は children の id に無い", c.Selected)
	}
	return nil
}

func containsNewline(s string) bool {
	for i := range len(s) {
		if s[i] == '\n' || s[i] == '\r' {
			return true
		}
	}
	return false
}

// PopupPlacement は、ポップアップの置き場所である（基本設計 1.4）。
type PopupPlacement string

// ポップアップの置き場所。
const (
	// PopupAtCursor は、編集の client の画面の中の、カーソルの近くに浮かべる窓である（既定）。
	// Focus が false なら、ホバーと同じく次のキーかカーソルの移動で閉じ、<C-w>w でフォーカスを入れられる。
	PopupAtCursor PopupPlacement = "cursor"
	// PopupCenter は、中央のポップアップである。開いている間はそれだけを操作する（FR-68）。
	// tmux と herdr の中では、端末多重化ソフトのポップアップの中の部品の client が描き（FR-66、FR-67）、
	// 外と、端末多重化ソフトのポップアップを開けないときは、編集の client の画面の中央に描く。
	PopupCenter PopupPlacement = "center"
)

// PopupOpenParams は、popup/open の params である。
// 本体は、その拡張機能へ最後に問い合わせを始めた client（基本設計 1.2）の画面に出す。
// 1 つの client に出すポップアップは 1 つで、新しいポップアップは前のものを置き換える（前のものの持ち主へ view/closed を送る）。
type PopupOpenParams struct {
	Component ViewComponent `json:"component"`
	// Placement は、置き場所である。省くと PopupAtCursor である。
	Placement PopupPlacement `json:"placement,omitempty"`
	// Focus は、PopupAtCursor のポップアップに、開いた時点でフォーカスを入れるかである。PopupCenter では常に入れる。
	Focus bool `json:"focus,omitempty"`
}

// Validate は、Component と Placement を検査する。
func (p *PopupOpenParams) Validate() error {
	switch p.Placement {
	case "", PopupAtCursor, PopupCenter:
	default:
		return fmt.Errorf("placement %q は知らない置き場所である", p.Placement)
	}
	if p.Component.Kind == ViewSwitcher && p.Placement != PopupCenter {
		// カーソルの近くの 12 行の窓では、見出しの行と子を読みにくいので断る（基本設計 1.4）。
		return fmt.Errorf("switcher は placement が center のときだけ開ける")
	}
	return p.Component.Validate()
}

// PopupOpenResult は、popup/open の result である。
type PopupOpenResult struct {
	// View は、開いたポップアップの ID である。
	View ViewID `json:"view"`
}

// PaneDirection は、pane/open でペインを分割する向きである。
type PaneDirection string

// ペインを分割する向き。新しいペインは、元の client のペインの右か下に開く。
const (
	// PaneRight は、元の client のペインの右に開く（既定）。
	PaneRight PaneDirection = "right"
	// PaneDown は、元の client のペインの下に開く。
	PaneDown PaneDirection = "down"
)

// PanePlacement は、pane/open で部品を出す場所である（FR-147。API 0.10.0 で足した）。
type PanePlacement string

// pane/open で部品を出す場所。
const (
	// PaneSplit は、元の client のペインを端末多重化ソフトで分割したペインである（既定）。
	// 端末多重化ソフトの外と、ペインを開けなかったときは、中央のポップアップに出す（FR-123）。
	PaneSplit PanePlacement = "split"
	// PaneCenter は、中央のポップアップである。popup/open の PopupCenter と同じく、tmux と herdr の中では端末多重化ソフトの
	// ポップアップの中の部品の client が描き、外では元の client の画面の中央に描く。開いている間はそれだけを操作し、<Esc> で閉じる。
	PaneCenter PanePlacement = "center"
)

// PaneOpenParams は、pane/open の params である（基本設計 1.2、1.3、5.3。FR-115、FR-123、FR-147）。
//
// 本体は、その拡張機能へ最後に問い合わせを始めた client（元の client）のペインを、端末多重化ソフト（tmux、herdr）で分割し、
// 新しいペインの中で部品を描く client（ペインの部品の client）を起動する。拡張機能は端末多重化ソフトを直接操作しない。
// ペインの中で打ったキーと選んだ項目は、元の client の操作として扱い、view/select などで知らせる。
// ペインの中の <Esc> はペインを閉じない（ポップアップと違う）。利用者が端末多重化ソフトでペインを閉じるか、ペインの中で :q を打つと閉じ、
// view/closed を送る。
//
// Placement が PaneCenter のときと、端末多重化ソフトの外（FR-60）と、ペインを開けなかったとき（herdr のプラグインを登録していないなど）は、
// ペインに代えて元の client の画面の中央のポップアップに出す（FR-123、FR-147）。このときは PopupCenter のポップアップと同じく <Esc> で閉じる。
// どちらでも応答の View は同じ扱いで、拡張機能は違いを知らずに view/update と view/close を送れる。
// 分割したペインに出したときは、元の client のカーソルのあるタブが替わるたびに view/tabChange を送る（ViewTabChangeParams）。
type PaneOpenParams struct {
	Component ViewComponent `json:"component"`
	// Placement は、出す場所である。省くと PaneSplit である（API 0.10.0 で足した。古い本体は読まずに分割したペインに出す）。
	Placement PanePlacement `json:"placement,omitempty"`
	// CloseOnSelect は、中央のポップアップに出したとき（Placement が PaneCenter か、ペインに代えて中央のポップアップに出したとき）に、
	// 一覧と木の項目を選んだら（view/select を送った後に）本体がポップアップを閉じることを表す（FR-147。API 0.10.0 で足した）。
	// 閉じたら view/closed を送る。木の子を持つ項目の <CR> は開閉なので閉じない。
	// 分割したペインは選んでも閉じない（ファイラのように、出しっぱなしにしたペインから選び続けるため）。
	// 閉じるのは選んだキーの処理の中なので、マクロの再生でも、選んだ後のキーは元の client のタブで続く。
	CloseOnSelect bool `json:"closeOnSelect,omitempty"`
	// Direction は、分割の向きである。省くと PaneRight である。
	Direction PaneDirection `json:"direction,omitempty"`
	// Size は、新しいペインの大きさを、元のペインの分割の向きの大きさに対する百分率（1〜99）で表す。0 なら端末多重化ソフトの既定である。
	// herdr 0.9.0 は分割の大きさを受け付けないので、herdr では使わない（基本設計 1.3）。
	Size int `json:"size,omitempty"`
	// Focus は、開いた新しいペインへフォーカスを移すかである。
	Focus bool `json:"focus,omitempty"`
}

// Validate は、Component、Placement、Direction、Size を検査する。
func (p *PaneOpenParams) Validate() error {
	switch p.Placement {
	case "", PaneSplit, PaneCenter:
	default:
		return fmt.Errorf("placement %q は知らない出す場所である", p.Placement)
	}
	switch p.Direction {
	case "", PaneRight, PaneDown:
	default:
		return fmt.Errorf("direction %q は知らない向きである", p.Direction)
	}
	if p.Size < 0 || p.Size > 99 {
		return fmt.Errorf("size %d は 0〜99 でない", p.Size)
	}
	return p.Component.Validate()
}

// PaneOpenResult は、pane/open の result である。
type PaneOpenResult struct {
	// View は、開いたペイン（端末多重化ソフトの外では、代わりのポップアップ）の ID である。
	View ViewID `json:"view"`
}

// ViewUpdateParams は、view/update の params である。
// 本体は、部品の種類が開いたときと同じ場合だけ置き換える。
// 一覧と木では、カーソルのあった項目の ID が新しい中身にもあれば、その項目の行へカーソルを置く。無ければ、行の番号を行数に収まる範囲で保つ。
// テキストと入力の行では、行の番号を保つ。ただしテキストでカーソルが最後の行にあれば、新しい中身の最後の行へ置く
// （書き足した出力の末尾を追う。#244）。木の開閉の状態は、項目の ID ごとに保つ（ViewItem.Expanded）。
//
// ViewFinder では、一覧（Items）と見出し（Title）だけを置き換え、Component.Text は読まない。
// Seq は、この一覧がどの入力に答えたものかを、input/change の Seq（開いたときの中身なら 0）で表す。
// 本体は、最後に送った input/change の Seq より小さい Seq の view/update を捨てる（古い入力への一覧を出さない。基本設計 6.9）。
// 同じ Seq の view/update は何度送ってもよい（候補を読みながら一覧を送り直すとき）。
// 前の view/update より大きい Seq の一覧では、カーソルを先頭の項目へ置き、同じ Seq なら項目の ID で保つ。
// ViewFinder 以外では Seq を読まない。
//
// ViewConsole では、出力（Text、Highlights）と前置き（Prompt）だけを置き換え、入力の行（Input）と履歴（History）は読まない
// （利用者が打っているため）。出力のカーソルは、テキストと同じく最後の行にあれば（Tail なら常に）新しい中身の最後の行へ置く。
//
// ViewSwitcher（API 0.23.0）では、Child があれば Component をその子の部品として、その子だけを置き換える（見えていない子でもよい）。
// 子の部品の種類が開いたときと違うか、知らない子なら捨てる。Child が無ければ部品の全体を置き換え、同じ ID の子は状態を保ったまま
// 中身を置き換え、無くなった ID の子は捨て、新しい ID の子は作る。Selected を書けばその子を選び（view/childSelect は送らない）、
// 書かなければ選んでいる子を保つ。子を並べ直したときは、見出しの行の並びも替える。
// Component を children の無い switcher にして Selected だけを書くと、子の中身を変えずに選ぶ子だけを替える（SelectsOnly）。
// 全体の置き換えは、テキストと console の子の中身（書き足した出力）も送った中身で置き換えるので、選ぶ子だけを替えるときはこの形を使う。
type ViewUpdateParams struct {
	View      ViewID        `json:"view"`
	Component ViewComponent `json:"component"`
	Seq       uint64        `json:"seq,omitempty"`
	// Child は、ViewSwitcher の子の ID である（API 0.23.0 で足した）。あれば Component はその子の部品である。
	Child string `json:"child,omitempty"`
}

// Validate は、Component を検査する。Child があれば、Component が切り替えの部品の子にできる種類であることも検査する。
// 選ぶ子だけを替える形（SelectsOnly）は、Selected のほかを持たないことを検査する。
func (p *ViewUpdateParams) Validate() error {
	if p.Child != "" && !p.Component.Kind.ChildKind() {
		return fmt.Errorf("child のある view/update の component.kind %q は switcher の子にできない", p.Component.Kind)
	}
	if p.Child == "" && p.Component.Kind == ViewSwitcher && len(p.Component.Children) == 0 {
		c := p.Component
		switch {
		case c.Selected == "":
			return fmt.Errorf("children の無い switcher の view/update は selected が要る")
		case c.Title != "" || c.Text != "" || len(c.Highlights) > 0 || len(c.Items) > 0 || c.Input != "" || c.Prompt != "" || len(c.History) > 0:
			return fmt.Errorf("children の無い switcher の view/update は selected のほかを持てない")
		}
		return nil
	}
	return p.Component.Validate()
}

// SelectsOnly は、p が切り替えの部品の選ぶ子だけを替える view/update（Child が無く、Component が children の無い switcher で
// Selected だけを持つ）かを返す（FR-270。API 0.23.0）。本体は子の中身（出力、書き足した中身、打ちかけの入力）を変えずに、
// Selected の子を選ぶ（view/childSelect は送らない）。知らない子なら捨てる。
func (p *ViewUpdateParams) SelectsOnly() bool {
	return p.Child == "" && p.Component.Kind == ViewSwitcher && len(p.Component.Children) == 0
}

// ViewAppendParams は、view/append の params である（基本設計 1.4 の「末尾への書き足し」。FR-272。API 0.20.0 で足した）。
//
// 本体は、開いたテキストの部品（ViewText）の中身か、書き足すテキストと入力の行の組（ViewConsole。API 0.22.0 から）の出力の末尾へ
// Text を書き足す。部品の全体を送り直さない。ViewConsole では、入力の行の中身、カーソル、モードを変えない。
// 中身の末尾が改行で終わっていなければ、その行に続ける。行の数え方と上限は AppendViewText と ViewTextOverflow のとおりで、
// 行数が ViewTextMaxLines を超えたら古い行から捨て、1 行が ViewTextMaxLineBytes を超えたら文字の境目で次の行へ送る。
// 書き足す前に部品のカーソルが最後の行にあれば、書き足した後の最後の行へカーソルを動かして末尾に追従し、
// 利用者が最後の行より上へカーソルを動かしている間は、見ている行とカーソルを動かさない。
// 閉じた後、知らない ID、テキストと ViewConsole でない部品への書き足しは、view/update と同じく黙って捨てる。
// Format が Markdown の部品へ書き足したテキストは、コードの囲みの行を除かずにそのまま出す。
//
// 閉じた部品を開き直したときに中身を戻すには、拡張機能の側で写しを持つ（ext/server の TextLog。FR-273）。
type ViewAppendParams struct {
	View ViewID `json:"view"`
	// Child は、切り替えの部品（ViewSwitcher。FR-269）の子の ID である。見えていない子にも書き足す（API 0.23.0 から。
	// 古い本体は空でない Child の書き足しを捨てる）。知らない子と、テキストと ViewConsole でない子への書き足しは捨てる。
	Child string `json:"child,omitempty"`
	// Text は、書き足すテキストである。改行を含んでよい。不正な UTF-8 を含む通知は、本体がログに残して捨てる。
	Text string `json:"text"`
	// Highlights は、Text の中の色を付ける範囲である。位置は Text の中のバイトの位置で、Text の外を指せない。
	// 色の違う断片を 1 件の中に並べて持てる（DAP の stdout と stderr）。
	Highlights []ViewSpan `json:"highlights,omitempty"`
}

// Validate は、Text が正しい UTF-8 であることと、Highlights の範囲が Text の中にあることを検査する。
func (p *ViewAppendParams) Validate() error {
	if !utf8.ValidString(p.Text) {
		return fmt.Errorf("text が不正な UTF-8 を含む")
	}
	for i, s := range p.Highlights {
		if s.Start < 0 || s.End < s.Start || s.End > len(p.Text) {
			return fmt.Errorf("highlights[%d] が text の外を指す", i)
		}
	}
	return nil
}

// ViewAppendJoin は、同じ部品（View と Child の組）への続く書き足しを、1 件につなぐ途中の状態である（ゼロ値は空）。
// 本体が拡張機能の応答を待つ間に取っておく列と、拡張機能ごとの列と、ext/server の TextLog の開いている間の書き足しで使う
// （基本設計 1.4 の「待ちの間」）。
//
// Add は足したテキストの長さに比例する手間で済む（つないだテキストの全体を写し直さない）。
// つないだテキストの行数が上限の 2 倍を超えたときだけ古い行を捨てるので、捨てる手間も足した量にならして比例する。
// Params は、つないだ 1 件を返す。つないだテキストが、それだけで ViewTextMaxLines を超える行を持つなら、古い行を捨てる。
// 捨てた後のテキストは改行で始め、本体の中身に当てたときに、書き足す前の中身の行（と最後の行に続けた行）も捨てられるようにする。
// このため、つないで捨てた 1 件を当てた結果は、つながずに順に当てた結果と同じになる。
type ViewAppendJoin struct {
	text []byte
	hs   []ViewSpan
	// newlines は、text の中の改行の数である。
	newlines int
}

// Add は、書き足し 1 件（text と、その中の色の範囲 hs）を後ろへつなぐ。hs は text の長さだけずらして写すので、呼び出し側の列は変えない。
func (j *ViewAppendJoin) Add(text string, hs []ViewSpan) {
	shift := len(j.text)
	j.text = append(j.text, text...)
	for _, s := range hs {
		j.hs = append(j.hs, ViewSpan{Start: s.Start + shift, End: s.End + shift, Style: s.Style})
	}
	j.newlines += strings.Count(text, "\n")
	if j.newlines > 2*ViewTextMaxLines {
		j.trim()
	}
}

// Len は、つないだテキストのバイト数を返す。
func (j *ViewAppendJoin) Len() int { return len(j.text) }

// ViewAppendJoinMaxBytes は、つないだ 1 件の大きさ（ViewAppendSize）の上限（仮に 4 MiB）である。
// 改行の無い出力は行数の上限で捨てられないので、上限を置かないと 1 件が際限なく大きくなる（PR #405 の再レビュー）。
// 本体が受け取る 1 件のメッセージの上限（64 MiB。internal/rpc の DefaultMaxMessageSize）に対して、JSON の文字列のエスケープで
// 本文が最大 6 倍（制御文字の \u00XX）になっても収まる値にした。拡張機能の側（TextLog）がつないだ 1 件を送っても、上限に当たらない。
const ViewAppendJoinMaxBytes = 4194304

// ViewAppendSize は、text と色の範囲 hs の書き足し 1 件の大きさを、ViewAppendJoinMaxBytes と比べる値で返す。
// 色の範囲 1 つは、JSON で 64 バイトと数える。
func ViewAppendSize(text string, hs []ViewSpan) int { return len(text) + 64*len(hs) }

// Fits は、text と hs をつないでも、つないだ 1 件の大きさが ViewAppendJoinMaxBytes を超えないかを返す。
// 空のときは、1 件だけで上限を超えても true を返す（新しい件にしても大きさは同じなので）。
func (j *ViewAppendJoin) Fits(text string, hs []ViewSpan) bool {
	if len(j.text) == 0 && len(j.hs) == 0 {
		return true
	}
	return len(j.text)+64*len(j.hs)+ViewAppendSize(text, hs) <= ViewAppendJoinMaxBytes
}

// trim は、つないだテキストの行数が ViewTextMaxLines を超えていれば、古い行を捨てて ViewTextMaxLines 行を残す。
func (j *ViewAppendJoin) trim() {
	n := j.newlines + 1
	if len(j.text) > 0 && j.text[len(j.text)-1] == '\n' {
		n--
	}
	d := ViewTextOverflow(n)
	if d == 0 {
		return
	}
	// 捨てる d 行の最後の行の終わりの改行の位置から残す。
	pos := -1
	for range d {
		pos += 1 + bytes.IndexByte(j.text[pos+1:], '\n')
	}
	j.text = append([]byte(nil), j.text[pos:]...)
	j.newlines -= d - 1 // 残した先頭の改行は数えたまま
	var hs []ViewSpan
	for _, s := range j.hs {
		if s.End <= pos {
			continue
		}
		hs = append(hs, ViewSpan{Start: max(s.Start-pos, 0), End: s.End - pos, Style: s.Style})
	}
	j.hs = hs
}

// Params は、つないだ書き足しを、部品 view と子 child への 1 件の params にして返す。
func (j *ViewAppendJoin) Params(view ViewID, child string) ViewAppendParams {
	j.trim()
	return ViewAppendParams{View: view, Child: child, Text: string(j.text), Highlights: slices.Clone(j.hs)}
}

// ViewCloseParams は、view/close の params である。
type ViewCloseParams struct {
	View ViewID `json:"view"`
}

// ViewClosedParams は、view/closed の params である。
type ViewClosedParams struct {
	View ViewID `json:"view"`
}

// ViewSelectParams は、view/select の params である。
type ViewSelectParams struct {
	View ViewID `json:"view"`
	// Item は、選んだ項目の ID（ViewList、ViewTree、ViewFinder）か、入力の行の中身（ViewInput と ViewConsole）である。
	// ViewFinder で一覧が空なら空である。
	Item string `json:"item"`
	// Input は、ViewFinder の入力の行の中身である。他の部品では省く。
	Input string `json:"input,omitempty"`
	// Selection は、ViewList、ViewTree、ViewFinder の項目の選択に本体が付ける番号である（1 から。API 0.9.0 で足した）。
	// 拡張機能は、この選択への応答として、同じ番号を付けた editor/execute（EditorExecuteParams.Selection）を 1 回だけ送れる。
	// ViewInput では省く。
	Selection uint64 `json:"selection,omitempty"`
	// Child は、切り替えの部品（ViewSwitcher）の中の知らせなら、その子の ID である。ほかの部品では省く（FR-271。API 0.23.0 で足した）。
	Child string `json:"child,omitempty"`
}

// InputChangeParams は、input/change の params である。
type InputChangeParams struct {
	View ViewID `json:"view"`
	// Text は、変わった後の入力の行の中身である。
	Text string `json:"text"`
	// Seq は、そのポップアップの入力の行が変わるたびに 1 から 1 ずつ増やす番号である（API 0.9.0 で足した）。
	// ViewFinder の拡張機能は、この入力への一覧を view/update の Seq に同じ値を入れて返す。
	// 本体は、前の入力への一覧が届いていなくても次の input/change を送るので、拡張機能は古い入力への計算を捨ててよい。
	// 切り替えの部品の子では、子ごとに数える。
	Seq uint64 `json:"seq,omitempty"`
	// Child は、切り替えの部品（ViewSwitcher）の中の知らせなら、その子の ID である。ほかの部品では省く（FR-271。API 0.23.0 で足した）。
	Child string `json:"child,omitempty"`
}

// ViewKeyParams は、view/key の params である（FR-69）。
type ViewKeyParams struct {
	View ViewID `json:"view"`
	// ID は、打ったキーの contributes.popupKeys の項目の ID である。
	ID string `json:"id"`
	// Item は、一覧、木、ViewFinder ではカーソルの行の項目の ID、入力の行と ViewConsole では入力の行の中身、テキストでは空である。
	Item string `json:"item,omitempty"`
	// Input は、ViewFinder の入力の行の中身である。他の部品では省く。
	Input string `json:"input,omitempty"`
	// Child は、切り替えの部品（ViewSwitcher）の中の知らせなら、キーを打ったときに出していた子の ID である。
	// ほかの部品では省く（FR-271。API 0.23.0 で足した）。
	Child string `json:"child,omitempty"`
	// Register は、利用者がキーの前に付けたレジスターの名前（"ac の "a"）である。付けなければ省く（FR-315。API 0.25.0 で足した）。
	// 本体は、ノーマルモードで回数を付けずに " と名前だけを打った後のキーにだけ付ける。名前は FR-50 で使えるもの（a〜z、A〜Z、_）に限り、
	// 使えない名前を付けたキーは、本体が E354 をメッセージの行に出して view/key を送らない。
	// 拡張機能は、レジスターへ入れるキー（DAP のウォッチ式の c）では、これを register/set の Register に入れる（register.go）。
	Register string `json:"register,omitempty"`
}
