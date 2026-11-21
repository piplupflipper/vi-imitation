package input

import (
	"fmt"
	"strings"

	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// spec は、本体の操作の表の 1 行である。
type spec struct {
	keys      string // editcmd.ParseKeysLeader の表記。<Leader> を含んでよい（builtinKeymap）
	id        string
	takesChar bool
	// takesChar2 は、操作が 2 つ目の文字の引数を取るかである（Binding.TakesSecondChar）。
	takesChar2 bool
	next       editcmd.Mode // 実行の後のモード
	modes      MapMode      // 有効なモード。0 なら種類ごとの既定（defaultModes）
	// desc は、キーの一覧（FR-90）に出す説明文である（Binding.Description）。
	desc string
}

// 本体の操作の一覧。
// 一覧は issue #9 の「3.」の案（段階 1）で、裁定待ちである。
// 追加・削除はこの表の行の変更だけで済み、Parser には手を入れなくてよい。
// 実行の側（internal/command）がこの ID で実装を引く。
// ID は公開のインターフェイスである。利用者は設定の keymap の右辺に { "action": "<ID>" } と書いて、キーに操作を割り当てる
// （FR-126。UserBinding）。ID の名前を変えると利用者の設定が壊れるので、変えない。消すときは要件定義の ID と同じく欠番にする。
// ID の一覧は docs/usage.md の「操作の ID の一覧」にあり、テスト（TestActionIDDoc）が表と照合する。
// desc はキーの一覧（FR-90。基本設計 7.2）に出す説明文で、どの行にも書く（テストが確かめる）。
//
// ~ は Vim の既定（tildeop が off）に合わせ、対象を取らない操作にした。大文字と小文字のオペレーターは g~、gu、gU である。
// マクロ（q、@）とドットリピート（.）は、Parser が記録中かどうかで読み方を変えるので、表ではなく Parser が持つ。
// その説明文は parserKeys にある。
// 有効なモードは、modes を書かない行では種類ごとの既定（defaultModes）である。
// ビジュアルモードでは、オペレーター（d、c、y など）は選択を対象にする。ビジュアルモードだけの登録は modes に MapVisual を書く。
//
// 英字 1 文字の大文字のキーは、同じモードの小文字のキーの操作の、向きを逆にしたもの（n と N）、単位を大きくしたもの（w と W）、
// 行の端まで広げたもの（d と D）のどれかにする（FR-144）。例外は J（行をつなげる）と K（ホバー）だけである。
// 大文字のキーを足すときは、TestUppercaseKeys の表にその関係を書く。
//
// req: FR-03, FR-144
var (
	builtinMotions = []spec{
		{keys: "h", id: "left", desc: "左へ"},
		{keys: "j", id: "down", desc: "下の行へ"},
		{keys: "k", id: "up", desc: "上の行へ"},
		{keys: "l", id: "right", desc: "右へ"},
		{keys: "gj", id: "display-down", desc: "見た目の行で 1 行下へ（行を折り返さないときは j と同じ）"},
		{keys: "gk", id: "display-up", desc: "見た目の行で 1 行上へ（行を折り返さないときは k と同じ）"},
		{keys: "w", id: "word-next", desc: "次の単語の先頭へ"},
		{keys: "b", id: "word-prev", desc: "前の単語の先頭へ"},
		{keys: "e", id: "word-end", desc: "単語の末尾へ"},
		{keys: "W", id: "bigword-next", desc: "次の空白区切りの単語の先頭へ"},
		{keys: "B", id: "bigword-prev", desc: "前の空白区切りの単語の先頭へ"},
		{keys: "E", id: "bigword-end", desc: "空白区切りの単語の末尾へ"},
		{keys: "0", id: "line-start", desc: "行の先頭へ"},
		{keys: "^", id: "line-first-nonblank", desc: "行の最初の空白でない文字へ"},
		{keys: "$", id: "line-end", desc: "行の末尾へ"},
		{keys: "gg", id: "file-start", desc: "最初の行へ（回数があればその行へ）"},
		{keys: "G", id: "file-end", desc: "最後の行へ（回数があればその行へ）"},
		{keys: "f", id: "find-forward", takesChar: true, desc: "行の中で右の {文字} へ"},
		{keys: "F", id: "find-backward", takesChar: true, desc: "行の中で左の {文字} へ"},
		{keys: "t", id: "till-forward", takesChar: true, desc: "行の中で右の {文字} の手前へ"},
		{keys: "T", id: "till-backward", takesChar: true, desc: "行の中で左の {文字} の手前へ"},
		{keys: ";", id: "find-repeat", desc: "直前の f、F、t、T を繰り返す"},
		{keys: ",", id: "find-repeat-reverse", desc: "直前の f、F、t、T を逆向きに繰り返す"},
		{keys: "%", id: "match-pair", desc: "対応する括弧へ"},
		{keys: "{", id: "paragraph-prev", desc: "前の段落へ"},
		{keys: "}", id: "paragraph-next", desc: "次の段落へ"},
		// 検索。/ と ? は、実行の側（internal/command）がコマンドラインでパターンを受け取ってから移る。
		// n と N は直前の検索を繰り返す。* と # は移らない操作なので、操作の表（builtinActions）にある。
		{keys: "/", id: "search-forward", desc: "前方へ検索する"},
		{keys: "?", id: "search-backward", desc: "後方へ検索する"},
		{keys: "n", id: "search-next", desc: "直前の検索を同じ向きに繰り返す"},
		{keys: "N", id: "search-prev", desc: "直前の検索を逆向きに繰り返す"},
		// 矢印キーと Home、End は、Vim 9.1 の既定（'whichwrap' に < > が無い）と同じく h、j、k、l、0、$ と同じ働きにする。
		// 修飾キーの付いたもの（<S-Right> の w、<C-Home> の gg など）は登録しない。
		{keys: "<Left>", id: "left", desc: "左へ"},
		{keys: "<Down>", id: "down", desc: "下の行へ"},
		{keys: "<Up>", id: "up", desc: "上の行へ"},
		{keys: "<Right>", id: "right", desc: "右へ"},
		{keys: "<Home>", id: "line-start", desc: "行の先頭へ"},
		{keys: "<End>", id: "line-end", desc: "行の末尾へ"},
	}

	builtinOperators = []spec{
		{keys: "d", id: "delete", desc: "消す"},
		{keys: "c", id: "change", next: editcmd.ModeInsert, desc: "消して入力する"},
		{keys: "y", id: "yank", desc: "ヤンクする"},
		{keys: "g~", id: "toggle-case", desc: "大文字と小文字を入れ替える"},
		{keys: "gu", id: "lowercase", desc: "小文字にする"},
		{keys: "gU", id: "uppercase", desc: "大文字にする"},
		// 行のコメントを切り替える（Neovim 0.12.5 の gc。FR-169）。ビジュアルモードでは選んだ行を切り替える。
		// ビジュアルモードの <C-_> と <C-/> は、オーナーの nvim の割り当て（ビジュアルモードでは gc）に合わせた。
		// 端末は Ctrl-/ を多くの場合 0x1f で送り、internal/term は <C-_> と読む。<C-/> は CSI u で区別して送る端末のための登録である。
		// req: FR-169
		{keys: "gc", id: "comment", desc: "行のコメントを切り替える"},
		{keys: "<C-_>", id: "comment", modes: MapVisual, desc: "選んだ行のコメントを切り替える（gc）"},
		{keys: "<C-/>", id: "comment", modes: MapVisual, desc: "選んだ行のコメントを切り替える（gc）"},
		// 囲みを足す（vim-sandwich の sa。FR-73、基本設計 4.8）。対象の後に囲みの文字を 1 つ取る。
		// 囲みのキーはノーマルモードだけに置く。ビジュアルモードの s は、選択を消して入力するオペレーターのまま残す。
		{keys: "sa", id: "surround-add", takesChar: true, modes: MapNormal, desc: "対象を {文字} で囲む（sa{対象}{文字}）"},
		// > と < は、ビジュアルモードだけのオペレーターである。ノーマルモードの > と < は、モーションを待たずに
		// 行をずらす操作（操作の表の indent-line と dedent-line。FR-76、要件定義 1.1）にする。
		{keys: ">", id: "indent", modes: MapVisual, desc: "選んだ行を右へずらす（選択は残す）"},
		{keys: "<", id: "dedent", modes: MapVisual, desc: "選んだ行を左へずらす（選択は残す）"},
		// ビジュアルモードだけの別名。x は d、s は c に当たる。~、u、U は選択の大文字と小文字を変える。
		{keys: "x", id: "delete", modes: MapVisual, desc: "選択を消す"},
		{keys: "s", id: "change", next: editcmd.ModeInsert, modes: MapVisual, desc: "選択を消して入力する"},
		{keys: "~", id: "toggle-case", modes: MapVisual, desc: "選択の大文字と小文字を入れ替える"},
		{keys: "u", id: "lowercase", modes: MapVisual, desc: "選択を小文字にする"},
		{keys: "U", id: "uppercase", modes: MapVisual, desc: "選択を大文字にする"},
		// ビジュアルモードの D、C、S、Y は、選択を行単位にしてから d、c、c、y を働かせる（Vim の v_D、v_C、v_S、v_Y）。
		// 文字単位の選択でも、選択した行の全体を対象にする。
		{keys: "D", id: "delete-lines", modes: MapVisual, desc: "選んだ行を消す"},
		{keys: "C", id: "change-lines", next: editcmd.ModeInsert, modes: MapVisual, desc: "選んだ行を消して入力する"},
		{keys: "S", id: "change-lines", next: editcmd.ModeInsert, modes: MapVisual, desc: "選んだ行を消して入力する"},
		{keys: "Y", id: "yank-lines", modes: MapVisual, desc: "選んだ行をヤンクする"},
	}

	builtinTextObjects = []spec{
		{keys: "iw", id: "inner-word", desc: "単語"},
		{keys: "aw", id: "a-word", desc: "単語と後ろの空白"},
		{keys: "iW", id: "inner-bigword", desc: "空白区切りの単語"},
		{keys: "aW", id: "a-bigword", desc: "空白区切りの単語と後ろの空白"},
		{keys: `i"`, id: "inner-double-quote", desc: `"…" の中`},
		{keys: `a"`, id: "a-double-quote", desc: `"…" の全体`},
		{keys: "i'", id: "inner-single-quote", desc: "'…' の中"},
		{keys: "a'", id: "a-single-quote", desc: "'…' の全体"},
		// 括弧のテキストオブジェクトは、Vim と同じく閉じ括弧と b、B の別名を持つ。
		{keys: "i(", id: "inner-paren", desc: "(…) の中"},
		{keys: "i)", id: "inner-paren", desc: "(…) の中"},
		{keys: "ib", id: "inner-paren", desc: "(…) の中"},
		{keys: "a(", id: "a-paren", desc: "(…) の全体"},
		{keys: "a)", id: "a-paren", desc: "(…) の全体"},
		{keys: "ab", id: "a-paren", desc: "(…) の全体"},
		{keys: "i[", id: "inner-bracket", desc: "[…] の中"},
		{keys: "i]", id: "inner-bracket", desc: "[…] の中"},
		{keys: "a[", id: "a-bracket", desc: "[…] の全体"},
		{keys: "a]", id: "a-bracket", desc: "[…] の全体"},
		{keys: "i{", id: "inner-brace", desc: "{…} の中"},
		{keys: "i}", id: "inner-brace", desc: "{…} の中"},
		{keys: "iB", id: "inner-brace", desc: "{…} の中"},
		{keys: "a{", id: "a-brace", desc: "{…} の全体"},
		{keys: "a}", id: "a-brace", desc: "{…} の全体"},
		{keys: "aB", id: "a-brace", desc: "{…} の全体"},
		{keys: "ip", id: "inner-paragraph", desc: "段落"},
		{keys: "ap", id: "a-paragraph", desc: "段落と後ろの空行"},
		// カーソルの行を含む、続くコメントの行（Neovim 0.12.5 のオペレーターの後の gc。FR-169）。
		// ビジュアルモードの gc はオペレーターなので、既定のキーはオペレーターの後だけに置き、ビジュアルモードには既定のキーの無い行を置く
		// （設定の keymap の visual で割り当てられる）。
		// req: FR-169
		{keys: "gc", id: "commented-lines", modes: MapOperatorPending, desc: "続くコメントの行"},
		{keys: "", id: "commented-lines", modes: MapVisual, desc: "続くコメントの行"},
	}

	builtinActions = []spec{
		// ID は、オペレーターの g~（toggle-case）と分ける（#257 のレビューの必須 1。操作の ID は一意にする）。
		{keys: "~", id: "toggle-case-char", desc: "カーソルの文字の大文字と小文字を入れ替える"},
		{keys: "p", id: "put-after", desc: "カーソルの後に貼る"},
		{keys: "P", id: "put-before", desc: "カーソルの前に貼る"},
		{keys: "x", id: "delete-char", desc: "カーソルの文字を消す"},
		{keys: "X", id: "delete-char-before", desc: "カーソルの前の文字を消す"},
		{keys: "r", id: "replace-char", takesChar: true, desc: "カーソルの文字を {文字} に置き換える"},
		// 省略形のコマンド。D は d$、C は c$、S は cc、Y は y$ に当たり、実行の側（internal/command）が
		// オペレーターと対象の組に置き換えて実行する。オペレーターの表ではなく操作の表に置くのは、
		// 対象を待たずに 1 キーで完結するからである。ビジュアルモードの D、C、S、Y は、オペレーターの表のビジュアルモードだけの行である。
		// ノーマルモードの s（cl）は、囲みの操作（sa、sd、sr）に使うので割り当てない（要件定義 1.1）。
		// Y は、D と C にそろえて行の末尾までヤンクする（FR-146。Vim の Y は yy で、Neovim の既定は y$）。
		// yy に当たる yank-line は既定のキーを持たず、設定の keymap で Y に割り当てれば Vim の Y に戻る。
		// req: FR-146
		{keys: "D", id: "delete-to-line-end", desc: "行の末尾まで消す（d$）"},
		{keys: "C", id: "change-to-line-end", next: editcmd.ModeInsert, desc: "行の末尾まで消して入力する（c$）"},
		{keys: "S", id: "substitute-line", next: editcmd.ModeInsert, desc: "行を消して入力する（cc）"},
		{keys: "Y", id: "yank-to-line-end", desc: "行の末尾までヤンクする（y$）"},
		{keys: "", id: "yank-line", desc: "行をヤンクする（yy）"},
		// ノーマルモードの > と < は、Vim の >> と << と同じく、回数の行数だけ行をずらす（FR-76）。次のキーを待たないので、
		// >> と打つと 2 回ずれる（#177 のコメントの決定）。
		{keys: ">", id: "indent-line", desc: "行を右へずらす（>>。回数は行数）"},
		{keys: "<", id: "dedent-line", desc: "行を左へずらす（<<。回数は行数）"},
		// 囲みを消す（sd{文字}）と替える（sr{前の文字}{後の文字}）。vim-sandwich の sd と sr に当たる（FR-74、FR-75、基本設計 4.8）。
		{keys: "sd", id: "surround-delete", takesChar: true, desc: "{文字} の囲みを消す（sd{文字}）"},
		{keys: "sr", id: "surround-replace", takesChar: true, takesChar2: true, desc: "囲みを替える（sr{前の文字}{後の文字}）"},
		// gcc は、Neovim 0.12.5 と同じく gc の後の行単位の対象（回数は行数）に当たる省略形にする（FR-169）。
		// gc（オペレーター）と gcc の両方があるので、gc の後は次のキーを待つ（Parser の説明。Neovim の gc と gcc の割り当てと同じ）。
		// <C-_> と <C-/> は、オーナーの nvim の割り当て（ノーマルモードでは gcc）に合わせた。
		// req: FR-169
		{keys: "gcc", id: "comment-line", modes: MapNormal, desc: "行のコメントを切り替える（gcc。回数は行数）"},
		{keys: "<C-_>", id: "comment-line", modes: MapNormal, desc: "行のコメントを切り替える（gcc。回数は行数）"},
		{keys: "<C-/>", id: "comment-line", modes: MapNormal, desc: "行のコメントを切り替える（gcc。回数は行数）"},
		{keys: "J", id: "join-lines", desc: "行をつなげる"},
		{keys: "o", id: "open-below", next: editcmd.ModeInsert, desc: "下に行を足して入力する"},
		{keys: "O", id: "open-above", next: editcmd.ModeInsert, desc: "上に行を足して入力する"},
		{keys: "i", id: "insert", next: editcmd.ModeInsert, desc: "カーソルの前から入力する"},
		{keys: "a", id: "append", next: editcmd.ModeInsert, desc: "カーソルの後から入力する"},
		// I と A は、ビジュアルモードでは選んだ行ごとに同じ位置から同時に入力する（FR-141）。文字単位と行単位で同じである。
		{keys: "I", id: "insert-line-start", next: editcmd.ModeInsert, modes: MapNormal | MapVisual, desc: "行の最初の空白でない文字の前から入力する（ビジュアルモードでは選んだ行ごと）"},
		{keys: "A", id: "append-line-end", next: editcmd.ModeInsert, modes: MapNormal | MapVisual, desc: "行の末尾から入力する（ビジュアルモードでは選んだ行ごと）"},
		// U は、u の向きを逆にしたリドゥである（FR-145）。Vim の U（最後に変えた 1 行をまとめて戻す）は本体に無い。
		// ビジュアルモードの U（選択を大文字にする）は、オペレーターの表のビジュアルモードだけの行で、変えない。
		// req: FR-145
		{keys: "u", id: "undo", desc: "アンドゥ"},
		{keys: "U", id: "redo", desc: "リドゥ"},
		{keys: "<C-r>", id: "redo", desc: "リドゥ"},
		// Ex コマンド（:w など）の一覧と解釈はコマンドラインの担当で、ここでは切り替えだけを持つ。
		// ビジュアルモードの : は、選択した行の範囲（'<,'>）を入力した状態で始める。
		{keys: ":", id: "command-line", next: editcmd.ModeCommandLine, modes: MapNormal | MapVisual, desc: "コマンドラインに入る"},
		// ビジュアルモードへの出入り。ビジュアルモードの中では、同じ種類なら抜け、違う種類なら切り替える。
		{keys: "v", id: "visual", modes: MapNormal | MapVisual, desc: "文字単位のビジュアルモード"},
		{keys: "V", id: "visual-line", modes: MapNormal | MapVisual, desc: "行単位のビジュアルモード"},
		// ビジュアルモードの o は、カーソルを選択の反対の端へ移す。
		{keys: "o", id: "visual-swap", modes: MapVisual, desc: "選択の反対の端へ移る"},
		// バッファの全体を文字単位のビジュアルモードで選ぶ。カーソルも画面も動かさない（FR-130。#263）。
		// 既定のキーは無く、設定の keymap で割り当てたときだけ使える。
		{keys: "", id: "select-all", modes: MapNormal | MapVisual, desc: "カーソルを動かさずにバッファの全体を選ぶ"},
		// バッファの全体を、ノーマルモードの ggVG と同じく行単位のビジュアルモードで選ぶ（FR-220。#364）。
		// 既定のキーは、オーナーの nvim の割り当て（ノーマルモードの <Leader>a を ggVG にする）に合わせた。
		// <Leader> は、LSP と DAP の既定のキー（providerKeys）と同じく、キーマップを作る時点の leader にする（builtinKeymap）。
		// req: FR-220
		{keys: "<Leader>a", id: "select-all-lines", modes: MapNormal, desc: "最初の行（回数があればその行）から最後の行までを、行単位のビジュアルモードで選ぶ（ggVG）"},
		// スクロールの操作（FR-173）。画面の表示領域を動かすので、モーションではなく操作にする。
		// Vim と同じく、オペレーターの後では使えない（d<PageDown>、d<C-d>、dzt は誤りになる）。
		// <PageDown> と <C-f>、<PageUp> と <C-b> は、Vim 9.1 と Neovim 0.12.5 と同じく同じ操作である（Neovim の nv_page。#337）。
		// req: FR-173
		{keys: "<PageDown>", id: "page-down", modes: MapNormal | MapVisual, desc: "1 画面下へスクロールする"},
		{keys: "<C-f>", id: "page-down", modes: MapNormal | MapVisual, desc: "1 画面下へスクロールする"},
		{keys: "<PageUp>", id: "page-up", modes: MapNormal | MapVisual, desc: "1 画面上へスクロールする"},
		{keys: "<C-b>", id: "page-up", modes: MapNormal | MapVisual, desc: "1 画面上へスクロールする"},
		{keys: "<C-d>", id: "half-page-down", modes: MapNormal | MapVisual, desc: "半画面（回数があれば回数の行）下へスクロールし、カーソルも同じ行数動かす"},
		{keys: "<C-u>", id: "half-page-up", modes: MapNormal | MapVisual, desc: "半画面（回数があれば回数の行）上へスクロールし、カーソルも同じ行数動かす"},
		{keys: "<C-e>", id: "scroll-line-down", modes: MapNormal | MapVisual, desc: "表示領域を 1 行（回数の行）下へスクロールする"},
		{keys: "<C-y>", id: "scroll-line-up", modes: MapNormal | MapVisual, desc: "表示領域を 1 行（回数の行）上へスクロールする"},
		{keys: "zt", id: "scroll-cursor-top", modes: MapNormal | MapVisual, desc: "カーソルの行（回数があればその行）を画面の上の端に置く"},
		{keys: "z<CR>", id: "scroll-cursor-top-first-nonblank", modes: MapNormal | MapVisual, desc: "zt の後に、行の最初の空白でない文字へ移る"},
		{keys: "zz", id: "scroll-cursor-center", modes: MapNormal | MapVisual, desc: "カーソルの行（回数があればその行）を画面の中ほどに置く"},
		{keys: "z.", id: "scroll-cursor-center-first-nonblank", modes: MapNormal | MapVisual, desc: "zz の後に、行の最初の空白でない文字へ移る"},
		{keys: "zb", id: "scroll-cursor-bottom", modes: MapNormal | MapVisual, desc: "カーソルの行（回数があればその行）を画面の下の端に置く"},
		{keys: "z-", id: "scroll-cursor-bottom-first-nonblank", modes: MapNormal | MapVisual, desc: "zb の後に、行の最初の空白でない文字へ移る"},
		// 出力の欄（拡張機能の output/append の行）の操作（名前とキーは仮。基本設計 6.7）。欄にカーソルは入らない。
		// <C-w>P は、閉じていれば開き、開いていれば次の欄へ切り替える（Vim ではプレビューの窓へ移るキー）。
		// <C-w>z は閉じる（Vim でもプレビューの窓を閉じるキー）。<C-w><PageUp> と <C-w><PageDown> は、欄を半分の高さずつスクロールする。
		// スクロールのキーは、Vim 9.1 と Neovim 0.12.5 のヘルプの tags に CTRL-W_<PageUp> と CTRL-W_<PageDown> が無いことを確かめて選んだ。
		// <C-w>d は、Vim では :dsplit、Neovim では診断の浮動窓を開く既定のキーなので使わない（PR #133 のレビュー）。
		{keys: "<C-w>P", id: "output-next", modes: MapNormal, desc: "出力の欄を開く、次の欄へ切り替える"},
		{keys: "<C-w>z", id: "output-close", modes: MapNormal, desc: "出力の欄を閉じる"},
		{keys: "<C-w><PageUp>", id: "output-scroll-up", modes: MapNormal, desc: "出力の欄を上へスクロールする"},
		{keys: "<C-w><PageDown>", id: "output-scroll-down", modes: MapNormal, desc: "出力の欄を下へスクロールする"},
		// カーソルの近くのポップアップ（ホバーなど）へフォーカスを入れ、入っていれば元のタブへ戻す（FR-68。基本設計 1.4。キーは仮）。
		// Vim で次のウィンドウへ移るキーである。
		{keys: "<C-w>w", id: "popup-focus", modes: MapNormal, desc: "ポップアップへフォーカスを入れる、元のタブへ戻る"},
		// ジャンプリスト（Vim の CTRL-O と CTRL-I）と、裏のバッファへの切り替え（CTRL-^）。
		// 端末は CTRL-I を Tab と同じ 0x09 で送り、internal/term は <Tab> に読む。<Tab> はタブの移動に使うので（FR-98）、
		// 既定では端末から CTRL-I でジャンプリストを進められない（要件定義 1.1）。
		// <C-i> は、CTRL-I を区別して送る端末（CSI u）と、設定の keymap で別のキーを <C-i> に展開したときのための登録である。
		{keys: "<C-o>", id: "jump-older", modes: MapNormal, desc: "ジャンプリストを戻る"},
		{keys: "<C-i>", id: "jump-newer", modes: MapNormal, desc: "ジャンプリストを進む"},
		{keys: "<C-^>", id: "alternate-buffer", modes: MapNormal, desc: "裏のタブへ移る"},
		// client の中の次と前のタブへ移る（FR-98。基本設計 4.4 の段階 3 のタブとバッファ）。
		{keys: "<Tab>", id: "tab-next", modes: MapNormal, desc: "次のタブへ移る"},
		{keys: "<S-Tab>", id: "tab-prev", modes: MapNormal, desc: "前のタブへ移る"},
		// 貼り付けの直後に、貼ったテキストを履歴の古いほう（<C-p>）、新しいほう（<C-n>）の項目に置き換える（FR-55。キーは仮。基本設計 4.5）。
		// Vim のノーマルモードの CTRL-P と CTRL-N は k と j と同じ移動で、ここでは移動に割り当てていない。
		{keys: "<C-p>", id: "put-older", modes: MapNormal, desc: "貼った直後に、履歴の古い項目に貼り直す"},
		{keys: "<C-n>", id: "put-newer", modes: MapNormal, desc: "貼った直後に、履歴の新しい項目に貼り直す"},
		// 見比べ（基本設計 6.8。FR-101）。Vim と同じキーで、次と前の差分へ移る。
		// do と dp は、Vim と同じく d のオペレーターの後の o と p として Parser が読む（Parser.match）ので、表には置かない。
		{keys: "]c", id: "diff-next", modes: MapNormal, desc: "見比べで、次の差分へ"},
		{keys: "[c", id: "diff-prev", modes: MapNormal, desc: "見比べで、前の差分へ"},
		// * と # は、カーソルの単語を検索のパターンと向きにするだけで、カーソルを動かさない（FR-139）。次の一致へは n で移る。
		// 移らないのでモーションでなく操作にし、オペレーターの後には使えない（d* は誤りになる）。ID はモーションだったときのまま変えない。
		{keys: "*", id: "search-word-forward", modes: MapNormal | MapVisual, desc: "カーソルの単語を前方への検索のパターンにする（移らない）"},
		{keys: "#", id: "search-word-backward", modes: MapNormal | MapVisual, desc: "カーソルの単語を後方への検索のパターンにする（移らない）"},
		// マウスの出来事（FR-181〜FR-184）。設定の mouse.enabled が真の間だけ端末から届く。Neovim 0.12.5 の mouse=a の既定の働きに合わせ、
		// ノーマルモード、ビジュアルモード、インサートモードに同じ操作の ID で置く（インサートモードの操作の ID の insert. は付けない。
		// どのモードでも「マウスの位置へ移る」などの同じ操作で、モードごとの違いは実装の側が持つ。internal/command の mouse.go）。
		// オペレーターの後には置かない（d<LeftMouse> は誤りになる）。<4-LeftMouse> は、矩形のビジュアルモードが無いので 1 回目と同じにした。
		// 中と右のボタンの出来事（<MiddleMouse>、<RightMouse> など）と、修飾キーの付いた出来事は、既定では割り当てない。
		// 右端のスクロールバーのつまみのドラッグと印のクリック（FR-210、FR-211）は、別の ID にせず、mouse.click、mouse.drag、mouse.release が
		// 位置で分ける（Neovim の <LeftMouse> が、押した位置で窓、ステータス行、タブの行の働きを分けるのと同じ）。
		// req: FR-181, FR-182, FR-183, FR-184
		{keys: "<LeftMouse>", id: "mouse.click", modes: MapNormal | MapVisual | MapInsert, desc: "マウスの位置へ移る（タブの行ならそのタブへ、スクロールバーの印ならその行へ移り、つまみならドラッグを始める）"},
		{keys: "<4-LeftMouse>", id: "mouse.click", modes: MapNormal | MapVisual | MapInsert, desc: "マウスの位置へ移る（タブの行ならそのタブへ、スクロールバーの印ならその行へ移り、つまみならドラッグを始める）"},
		{keys: "<LeftDrag>", id: "mouse.drag", modes: MapNormal | MapVisual | MapInsert, desc: "ボタンを押した位置からマウスの位置までを選ぶ（スクロールバーのつまみなら表示領域を動かす）"},
		{keys: "<LeftRelease>", id: "mouse.release", modes: MapNormal | MapVisual | MapInsert, desc: "ドラッグを終える（つまみの上の印を動かさずに離したら、その行へ移る）"},
		{keys: "<2-LeftMouse>", id: "mouse.select-word", modes: MapNormal | MapVisual | MapInsert, desc: "マウスの位置の単語を選ぶ"},
		{keys: "<3-LeftMouse>", id: "mouse.select-line", modes: MapNormal | MapVisual | MapInsert, desc: "マウスの位置の行を選ぶ"},
		{keys: "<ScrollWheelDown>", id: "mouse.scroll-down", modes: MapNormal | MapVisual | MapInsert, desc: "表示領域を mouse.scrollLines の行だけ下へ送る"},
		{keys: "<ScrollWheelUp>", id: "mouse.scroll-up", modes: MapNormal | MapVisual | MapInsert, desc: "表示領域を mouse.scrollLines の行だけ上へ送る"},
		{keys: "<ScrollWheelRight>", id: "mouse.scroll-right", modes: MapNormal | MapVisual | MapInsert, desc: "行を折り返さないとき、表示領域を mouse.scrollColumns の列だけ右へ送る"},
		{keys: "<ScrollWheelLeft>", id: "mouse.scroll-left", modes: MapNormal | MapVisual | MapInsert, desc: "行を折り返さないとき、表示領域を mouse.scrollColumns の列だけ左へ送る"},
	}

	// builtinInsert は、インサートモードのカーソルの移動と削除の操作である（FR-127）。internal/command の State.insertAction が実装を引く。
	// ID には接頭辞 insert. を付け、ノーマルモードのモーションの ID と分ける（$ と <End> は止まる位置が違うため。#257 の決定）。
	// keys が空の行は、既定のキーを持たない操作で、設定の keymap で割り当てたときだけ使える。
	// 文字の入力、<CR>、<Tab>、<Esc> は、1 文字の入力の速さに触れないために表を通さない（要件定義 8.1。checkInsertKeys）。
	// 前の単語と行の先頭までを消す 2 つの操作（insert.delete-prev-word、insert.delete-to-line-start）は #260 で足した（FR-128）。
	//
	// req: FR-127, FR-128
	builtinInsert = []spec{
		{keys: "<Left>", id: "insert.left", modes: MapInsert, desc: "左へ"},
		{keys: "<Right>", id: "insert.right", modes: MapInsert, desc: "右へ"},
		{keys: "<Up>", id: "insert.up", modes: MapInsert, desc: "上の行へ"},
		{keys: "<Down>", id: "insert.down", modes: MapInsert, desc: "下の行へ"},
		{keys: "<Home>", id: "insert.line-start", modes: MapInsert, desc: "行の先頭へ"},
		{keys: "<End>", id: "insert.line-end", modes: MapInsert, desc: "行末の位置へ"},
		{keys: "<PageUp>", id: "insert.page-up", modes: MapInsert, desc: "1 画面上へスクロールする"},
		{keys: "<PageDown>", id: "insert.page-down", modes: MapInsert, desc: "1 画面下へスクロールする"},
		{keys: "<BS>", id: "insert.delete-prev-char", modes: MapInsert, desc: "前の文字を消す"},
		{keys: "<Del>", id: "insert.delete-char", modes: MapInsert, desc: "カーソルの文字を消す"},
		{keys: "", id: "insert.delete-to-line-end", modes: MapInsert, desc: "カーソルから行末まで消す（行末では次の行をつなげる）"},
		{keys: "", id: "insert.delete-prev-word", modes: MapInsert, desc: "前の単語を消す（行の先頭では前の行とつなげる）"},
		{keys: "", id: "insert.delete-to-line-start", modes: MapInsert, desc: "カーソルから行の先頭まで消す"},
	}
)

// parserKeys は、Parser が表を引く前に読むキー（回数、レジスター、マクロ、ドットリピート）の、キーの一覧（FR-90）に出す説明である。
// これらは Keymap に登録しないので（Keymap.Bind の doc）、一覧を作る側が Bindings の後に足す。
var parserKeys = []KeyHelp{
	{Keys: "{回数}", Modes: MapNormal | MapVisual | MapOperatorPending, Description: "1〜9 で始まる数字。続く操作を繰り返す回数"},
	{Keys: `"{レジスター}`, Modes: MapNormal | MapVisual, Description: "続く操作で使うレジスターを選ぶ"},
	{Keys: "q{レジスター}", Modes: MapNormal, Description: "マクロの記録を始める（記録中の q で終える）"},
	{Keys: "@{レジスター}", Modes: MapNormal, Description: "マクロを再生する（@@ は直前のマクロ）"},
	{Keys: ".", Modes: MapNormal, Description: "直前の変更を繰り返す"},
	// do と dp は、d のオペレーターの後の o と p として Parser が読む（Parser.match）。
	{Keys: "do", Modes: MapNormal, Description: "見比べで、カーソルの差分を相手の内容で置き換える"},
	{Keys: "dp", Modes: MapNormal, Description: "見比べで、相手の差分をカーソルの差分の内容で置き換える"},
}

// KeyHelp は、Keymap に登録しないキーの、キーの一覧（FR-90）に出す説明である。
type KeyHelp struct {
	// Keys は、キーの表記である。{回数} のような引数の名前を含む。
	Keys string
	// Modes は、キーが効くモードである。
	Modes MapMode
	// Description は、説明文である。
	Description string
}

// ParserKeys は、Parser が表を引く前に読むキー（回数、レジスター、マクロ、ドットリピート）の説明を返す。
func ParserKeys() []KeyHelp {
	return append([]KeyHelp(nil), parserKeys...)
}

// ProviderExtension は、提供者へ振り分ける本体の操作（基本設計 5.5。FR-94）の Binding.Ref.Extension に入れる値である（editcmd.ProviderExtension）。
//
// LSP と DAP の操作は、どの拡張機能が引き受けるかを、呼ばれた時点のバッファのファイルの種類で決める（exthost.Host.Provider）。
// そのためキーマップには拡張機能の ID を書けず、この値を入れておき、問い合わせる側（internal/editor）が提供者に置き換える。
// 実行の側（internal/command）からは拡張機能の操作に見えるので、問い合わせ（command.Extensions）を通る。
// ただしドットリピートは、対象を取らない操作（K、<leader>db など）を記録しない（command の repeatable。FR-06）。
const ProviderExtension = editcmd.ProviderExtension

// 提供者へ振り分ける操作の ID のうち、ext.Operation の名前でないもの。
const (
	// ProviderPeekPrefix は、移動の操作の「ポップアップで見る」を表す ID の接頭辞である（peek.definition）。
	// 後ろは ext.OpDefinition などの操作の名前で、提供者へは Args の peek を true にして問い合わせる。
	ProviderPeekPrefix = "peek."
	// 診断の一覧と移動は、本体が受け取った診断から作り、提供者へ問い合わせない（基本設計 5.5）。
	ProviderDiagnosticList    = "diagnostic.list"
	ProviderDiagnosticListAll = "diagnostic.listAll"
	ProviderDiagnosticNext    = "diagnostic.next"
	ProviderDiagnosticPrev    = "diagnostic.prev"
	// KeyListPrefix は、キーの一覧（FR-90）を出す操作の ID の接頭辞である。
	// 後ろは「操作の分類（KeyGroupLSP などか空）:一覧を絞る接頭辞のキーの列（editcmd.KeysString の表記）」である（ParseKeyListID）。
	// <leader>l? なら "keys.lsp:<Space>l"、g? なら "keys.:g" になる。提供者へは問い合わせず、internal/editor が一覧を出す。
	// ProviderExtension の操作にするのは、ドットリピートに記録せず（command の repeatable）、
	// 編集を禁止したバッファでも断らない（command の changesText）扱いを、テキストを変えない LSP の操作と揃えるためである。
	KeyListPrefix = "keys."
)

// providerSpec は、提供者へ振り分ける操作の既定のキーの表の 1 行である。
type providerSpec struct {
	keys string // editcmd.ParseKeysLeader の表記。<Leader> を含んでよい
	id   string // ext.Operation の名前か、上の定数
	kind BindingKind
	// modes は、有効なモードである。0 なら種類ごとの既定（defaultModes）である。
	modes MapMode
	// desc は、キーの一覧（FR-90）に出す説明文である。
	desc string
}

// providerKeys は、LSP と DAP の操作の既定のキーである（要件定義 4.11 の 2 つの表。FR-95、FR-96）。
// K、gq、<C-]> は Vim の標準の操作と重なるが、nvim が LSP の有効なバッファで同じキーを LSP の操作にしているのに合わせる（基本設計 6.3）。
// <leader>l? と <leader>d?（キーの一覧。FR-90）は keyListKeys にある。補完は、まだ割り当てない。
//
// req: FR-95, FR-96
var providerKeys = []providerSpec{
	{keys: "<Leader>lh", id: "hover", kind: BindAction, desc: "ホバー（LSP）"},
	{keys: "K", id: "hover", kind: BindAction, desc: "ホバー（LSP）"},
	{keys: "<Leader>ld", id: ProviderPeekPrefix + "definition", kind: BindAction, desc: "定義をポップアップで見る（LSP）"},
	{keys: "<Leader>lD", id: "definition", kind: BindAction, desc: "定義へ移る（LSP）"},
	{keys: "<C-]>", id: "definition", kind: BindAction, desc: "定義へ移る（LSP）"},
	{keys: "<Leader>li", id: ProviderPeekPrefix + "implementation", kind: BindAction, desc: "実装をポップアップで見る（LSP）"},
	{keys: "<Leader>lI", id: "implementation", kind: BindAction, desc: "実装へ移る（LSP）"},
	{keys: "<Leader>lt", id: ProviderPeekPrefix + "typeDefinition", kind: BindAction, desc: "型の定義をポップアップで見る（LSP）"},
	{keys: "<Leader>lT", id: "typeDefinition", kind: BindAction, desc: "型の定義へ移る（LSP）"},
	{keys: "<Leader>lr", id: "references", kind: BindAction, desc: "参照の一覧（LSP）"},
	{keys: "<Leader>ln", id: "rename", kind: BindAction, desc: "名前を変える（LSP）"},
	{keys: "<Leader>la", id: "codeAction", kind: BindAction, desc: "コードアクション（LSP）"},
	{keys: "<Leader>ls", id: "documentSymbols", kind: BindAction, desc: "ファイルのシンボルの一覧（LSP）"},
	{keys: "<Leader>lS", id: "workspaceSymbols", kind: BindAction, desc: "ワークスペースのシンボルの一覧（LSP）"},
	{keys: "<Leader>le", id: ProviderDiagnosticList, kind: BindAction, desc: "ファイルの診断の一覧"},
	{keys: "<Leader>lE", id: ProviderDiagnosticListAll, kind: BindAction, desc: "ワークスペースの診断の一覧"},
	{keys: "<Leader>lj", id: ProviderDiagnosticNext, kind: BindAction, desc: "次の診断へ移る"},
	{keys: "<Leader>lk", id: ProviderDiagnosticPrev, kind: BindAction, desc: "前の診断へ移る"},
	// gq はオペレーターで、範囲を format の引数にする（基本設計 5.5）。ビジュアルモードでは選択を範囲にする。
	{keys: "gq", id: "format", kind: BindOperator, desc: "整形する（LSP）"},

	{keys: "<Leader>dc", id: "debug.continue", kind: BindAction, desc: "デバッグを始める、続行する（DAP）"},
	{keys: "<Leader>dp", id: "debug.pause", kind: BindAction, desc: "一時停止する（DAP）"},
	{keys: "<Leader>dt", id: "debug.terminate", kind: BindAction, desc: "デバッグを終える（DAP）"},
	{keys: "<Leader>dr", id: "debug.restart", kind: BindAction, desc: "デバッグをやり直す（DAP）"},
	{keys: "<Leader>db", id: "breakpoint.toggle", kind: BindAction, desc: "ブレークポイントを切り替える（DAP）"},
	{keys: "<Leader>dB", id: "breakpoint.conditional", kind: BindAction, desc: "条件付きのブレークポイントを置く（DAP）"},
	{keys: "<Leader>dX", id: "breakpoint.clear", kind: BindAction, desc: "ブレークポイントをすべて消す（DAP）"},
	{keys: "<Leader>dn", id: "debug.stepOver", kind: BindAction, desc: "ステップオーバー（DAP）"},
	{keys: "<Leader>di", id: "debug.stepIn", kind: BindAction, desc: "ステップイン（DAP）"},
	{keys: "<Leader>do", id: "debug.stepOut", kind: BindAction, desc: "ステップアウト（DAP）"},
	{keys: "<Leader>dl", id: "debug.runLast", kind: BindAction, desc: "前回の設定で実行する（DAP）"},
	{keys: "<Leader>du", id: "debug.toggleUI", kind: BindAction, desc: "DAP のペインを切り替える（DAP）"},
}

// 操作の分類（ProviderGroup）。<leader>l? と <leader>d? の一覧は、接頭辞に加えてこの分類の操作のキーを出す
// （K、gq、<C-]> のような <leader>l で始まらない LSP のキーを含めるため。#227 のオーケストレータの決定）。
const (
	KeyGroupLSP = "lsp"
	KeyGroupDAP = "dap"
)

// keyListKeys は、キーの一覧（FR-90）を出す既定のキーである（要件定義 4.11 の表の <leader>l? と <leader>d?）。
// keys の最後の ? の手前が一覧を絞る接頭辞で、group はその一覧に含める操作の分類である。
// 他の接頭辞の ? は、internal/editor が登録の接頭辞から作る（基本設計 7.2）。
var keyListKeys = []struct {
	keys, group, desc string
}{
	{keys: "<Leader>l?", group: KeyGroupLSP, desc: "LSP のキーの一覧"},
	{keys: "<Leader>d?", group: KeyGroupDAP, desc: "DAP のキーの一覧"},
}

// PreviewPrefix は、ポップアップとペインの項目のプレビュー（FR-149）をスクロールする操作の ID の接頭辞である（FR-159）。
// 後ろは向きと単位（page-down、page-up、down、up）である。提供者へは問い合わせず、internal/editor が、
// プレビューを出している一覧、木、ファインダーの中で、フォーカスを一覧（ファインダーの入力の行）に置いたままスクロールする。
// プレビューを出していないところ（編集のバッファ、プレビューの無いポップアップ）では何もしない。
// ProviderExtension の操作にするのは、キーの一覧（KeyListPrefix）と同じく、ドットリピートに記録せず、
// 編集を禁止したバッファ（ファイラの木、プレビュー）でも断らない扱いにするためである。
const PreviewPrefix = "preview."

// previewKeys は、プレビューをスクロールする操作の既定のキーである（FR-159）。
// キーは fzf-lua の既定（keymap.builtin の <S-down>、<S-up>、<M-S-down>、<M-S-up>）に合わせた（#308 の決まったこと）。
// ファインダーは入力の行がインサートモードで、ファイラの木はノーマルモードなので、どちらのモードにも割り当てる。
// Vim の <S-Down> と <S-Up>（1 画面のスクロール）は、本体では今まで割り当てていなかった。
//
// req: FR-159
var previewKeys = []providerSpec{
	{keys: "<S-Down>", id: PreviewPrefix + "page-down", kind: BindAction, modes: MapNormal | MapInsert, desc: "プレビューを 1 画面下へスクロールする"},
	{keys: "<S-Up>", id: PreviewPrefix + "page-up", kind: BindAction, modes: MapNormal | MapInsert, desc: "プレビューを 1 画面上へスクロールする"},
	{keys: "<A-S-Down>", id: PreviewPrefix + "down", kind: BindAction, modes: MapNormal | MapInsert, desc: "プレビューを 1 行下へスクロールする"},
	{keys: "<A-S-Up>", id: PreviewPrefix + "up", kind: BindAction, modes: MapNormal | MapInsert, desc: "プレビューを 1 行上へスクロールする"},
}

// KeyListBinding は、接頭辞 prefix で始まるキーと、分類 group（空なら無し）の操作のキーの一覧（FR-90）を出す、
// prefix の後に ? を続けたキーの登録を返す。Ref.ID は KeyListPrefix で始まり（ParseKeyListID）、Ref.Extension は ProviderExtension である。
func KeyListBinding(prefix []editcmd.Key, group, desc string) Binding {
	keys := append(append([]editcmd.Key(nil), prefix...), editcmd.Rune('?'))
	return Binding{
		Kind: BindAction, Modes: MapNormal, Keys: keys, Description: desc,
		Ref: editcmd.Ref{ID: KeyListPrefix + group + ":" + editcmd.KeysString(prefix), Extension: ProviderExtension},
	}
}

// ParseKeyListID は、キーの一覧の操作の ID（KeyListBinding）から、分類と接頭辞を取り出す。キーの一覧の ID でなければ ok は false である。
func ParseKeyListID(id string) (group string, prefix []editcmd.Key, ok bool) {
	rest, ok := strings.CutPrefix(id, KeyListPrefix)
	if !ok {
		return "", nil, false
	}
	group, keys, ok := strings.Cut(rest, ":")
	if !ok {
		return "", nil, false
	}
	prefix, err := editcmd.ParseKeys(keys)
	if err != nil {
		return "", nil, false
	}
	return group, prefix, true
}

// ProviderGroup は、提供者へ振り分ける操作の ID の分類（KeyGroupLSP か KeyGroupDAP）を返す。
// debug. と breakpoint. で始まる操作は DAP、それ以外の providerKeys の操作（診断を含む）は LSP である（要件定義 4.11 の 2 つの表）。
// キーの一覧の操作はその一覧の分類を返す。どれでもなければ空を返す。
func ProviderGroup(id string) string {
	if g, _, ok := ParseKeyListID(id); ok {
		return g
	}
	if strings.HasPrefix(id, "debug.") || strings.HasPrefix(id, "breakpoint.") {
		return KeyGroupDAP
	}
	for _, s := range providerKeys {
		if s.id == id {
			return KeyGroupLSP
		}
	}
	return ""
}

// ProviderOperations は、既定のキーで呼べる、提供者へ振り分ける操作の ID の一覧を、表の順に重なりなく返す。
// ProviderPeekPrefix の付いたものと診断のものを含む。キーの一覧（keyListKeys）は含まない。
func ProviderOperations() []string {
	var ids []string
	seen := map[string]bool{}
	for _, s := range providerKeys {
		if !seen[s.id] {
			seen[s.id] = true
			ids = append(ids, s.id)
		}
	}
	return ids
}

// DefaultKeymap は、本体の操作を登録したキーマップを返す。<Leader> は既定の leader（editcmd.DefaultLeader）にする。
// 呼ぶたびに新しい値を返すので、拡張機能の操作を足しても他の呼び出し元に影響しない。
//
// req: FR-03
func DefaultKeymap() *Keymap {
	m, _ := DefaultKeymapLeader(nil)
	return m
}

// DefaultKeymapLeader は、DefaultKeymap と同じだが、本体の操作の表（builtinActions の <Leader>a など）と、
// LSP と DAP の操作の既定のキー（providerKeys）と、キーの一覧の既定のキー（keyListKeys）の <Leader> を leader にする。
// leader が nil なら既定の leader を使う。
// 利用者の leader によって、先に登録した既定のキーと同じキーの列になった既定のキーは登録せず、errs に返す
// （<Leader> を含まない本体の操作のキーを優先する）。
//
// req: FR-03, FR-95, FR-96, FR-97
func DefaultKeymapLeader(leader []editcmd.Key) (m *Keymap, errs []error) {
	m, errs = builtinKeymap(leader)
	for _, s := range providerKeys {
		keys, err := editcmd.ParseKeysLeader(s.keys, leader)
		if err == nil {
			err = m.Bind(Binding{Kind: s.kind, Modes: s.modes, Keys: keys, Ref: editcmd.Ref{ID: s.id, Extension: ProviderExtension}, Description: s.desc})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("本体の操作 %s の既定のキー %s を登録できない: %w", s.id, s.keys, err))
		}
	}
	for _, s := range keyListKeys {
		keys, err := editcmd.ParseKeysLeader(s.keys, leader)
		if err == nil {
			err = m.Bind(KeyListBinding(keys[:len(keys)-1], s.group, s.desc))
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("キーの一覧の既定のキー %s を登録できない: %w", s.keys, err))
		}
	}
	for _, s := range previewKeys {
		err := m.Bind(Binding{
			Kind: s.kind, Modes: s.modes, Keys: editcmd.MustParseKeys(s.keys),
			Ref: editcmd.Ref{ID: s.id, Extension: ProviderExtension}, Description: s.desc,
		})
		if err != nil {
			// 表の誤りはプログラムの誤りで、テストで検出する（builtinKeymap と同じ）。
			panic(fmt.Sprintf("プレビューの操作の表の誤り: %v", err))
		}
	}
	return m, errs
}

// builtinTables は、本体の操作の表と、その種類である。順は、ID から操作を引くとき（UserBinding）の優先の順でもある。
var builtinTables = []struct {
	kind  BindingKind
	specs []spec
}{
	{BindMotion, builtinMotions},
	{BindOperator, builtinOperators},
	{BindTextObject, builtinTextObjects},
	{BindAction, builtinActions},
	{BindAction, builtinInsert},
}

// builtinKeymap は、本体の操作の表（builtinMotions など）だけを登録したキーマップを返す。
// keys が空の行（既定のキーの無い操作）は登録しない。
// keys の <Leader> は leader（nil なら既定の leader）にする（editcmd.ParseKeysLeader）。
// <Leader> を含む行は、利用者の leader によっては先に登録した行と同じキーの列になる（leader が s なら <Leader>a は sa になる）ので、
// 登録できなかった行は panic せずに errs に返す（providerKeys と同じ扱い）。
// 既定の leader で errs が空であること（表の誤りが無いこと）は、テスト（TestProviderKeys、TestLeaderSelectAll）が確かめる。
func builtinKeymap(leader []editcmd.Key) (m *Keymap, errs []error) {
	m = NewKeymap()
	for _, t := range builtinTables {
		for _, s := range t.specs {
			if s.keys == "" {
				continue
			}
			keys, err := editcmd.ParseKeysLeader(s.keys, leader)
			if err == nil {
				err = m.Bind(Binding{
					Kind:            t.kind,
					Modes:           s.modes,
					Keys:            keys,
					Ref:             editcmd.Ref{ID: s.id},
					TakesChar:       s.takesChar,
					TakesSecondChar: s.takesChar2,
					NextMode:        s.next,
					Description:     s.desc,
				})
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("本体の操作 %s の既定のキー %s を登録できない: %w", s.id, s.keys, err))
			}
		}
	}
	return m, errs
}
