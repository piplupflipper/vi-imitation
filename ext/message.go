package ext

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// メソッドの名前（基本設計 5.3）。名前は仮である。
//
// 「リクエスト」は応答を返し、「通知」は応答を返さない（JSON-RPC 2.0）。
const (
	// MethodInitialize は、本体から拡張機能へのリクエストである。
	// params は InitializeParams、result は InitializeResult。
	// 応答が届いた時点で、拡張機能は稼働中になる。
	MethodInitialize = "initialize"

	// MethodBufferOpen は、本体から拡張機能への通知である。params は BufferOpenParams。
	MethodBufferOpen = "buffer/open"
	// MethodBufferChange は、本体から拡張機能への通知である。params は BufferChangeParams。
	MethodBufferChange = "buffer/change"
	// MethodBufferClose は、本体から拡張機能への通知である。params は BufferCloseParams。
	MethodBufferClose = "buffer/close"

	// MethodMotionResolve は、本体から拡張機能へのリクエストである。
	// params は MotionResolveParams、result は MotionResolveResult。
	MethodMotionResolve = "motion/resolve"
	// MethodTextObjectResolve は、本体から拡張機能へのリクエストである。
	// params は TextObjectResolveParams、result は TextObjectResolveResult。
	MethodTextObjectResolve = "textObject/resolve"
	// MethodOperatorApply は、本体から拡張機能へのリクエストである。
	// params は OperatorApplyParams、result は OperatorApplyResult。
	MethodOperatorApply = "operator/apply"
	// MethodCommandExecute は、本体から拡張機能へのリクエストである。
	// params は CommandExecuteParams、result は CommandExecuteResult。
	// 宣言ファイルの contributes.commands に書いたコマンドを実行する（基本設計 5.3 の表には無い。仮）。
	MethodCommandExecute = "command/execute"

	// MethodPing は、本体から拡張機能への生存確認のリクエストである。
	// params は PingParams、result は PingResult。
	MethodPing = "ping"
	// MethodCancel は、本体から拡張機能への通知である。params は CancelParams。
	MethodCancel = "$/cancel"
	// MethodShutdown は、本体から拡張機能への通知である。params は ShutdownParams。
	// 本体が拡張機能を正常に止めるとき（エディタの終了、拡張機能の無効化）に送る（基本設計 5.2）。
	MethodShutdown = "shutdown"

	// MethodBufferEdit は、拡張機能から本体へのリクエストである。
	// params は BufferEditParams、result は BufferEditResult。
	// 編集を禁止したバッファ（基本設計 4.6）への要求は、result ではなく、code が ErrorCodeReadOnly の誤りの応答で断る。
	MethodBufferEdit = "buffer/edit"
	// MethodDecorationSet は、拡張機能から本体への通知である。params は DecorationSetParams。
	MethodDecorationSet = "decoration/set"
	// MethodMessageShow は、拡張機能から本体への通知である。params は MessageShowParams。
	MethodMessageShow = "message/show"
	// MethodBufferResync は、拡張機能から本体への通知である。params は BufferResyncParams。
	// 差分が欠けたと判断した拡張機能が、全文の送り直しを求める（基本設計 5.3 の本文）。
	MethodBufferResync = "buffer/resync"

	// 以下は、標準の拡張機能（基本設計 6 章）のために足したメソッドである（基本設計 5.3 の 2 つ目の表）。

	// MethodBufferWillSave は、本体から拡張機能へのリクエストである。
	// params は BufferWillSaveParams、result は BufferWillSaveResult。
	// 宣言ファイルの saveHook が true の稼働中の拡張機能にだけ、保存の直前に送る（基本設計 6.7 の保存の流れ）。
	MethodBufferWillSave = "buffer/willSave"
	// MethodBufferDidSave は、本体から拡張機能への通知である。params は BufferDidSaveParams。
	MethodBufferDidSave = "buffer/didSave"

	// MethodEditorState は、拡張機能から本体へのリクエストである。
	// params は EditorStateParams（中身は無い）、result は EditorStateResult。
	MethodEditorState = "editor/state"
	// MethodEditorOpen は、拡張機能から本体へのリクエストである。
	// params は EditorOpenParams、result は EditorOpenResult。
	MethodEditorOpen = "editor/open"
	// MethodBufferSave は、拡張機能から本体へのリクエストである。
	// params は BufferSaveParams、result は BufferSaveResult。
	MethodBufferSave = "buffer/save"
	// MethodDiagnosticSet は、拡張機能から本体への通知である。params は DiagnosticSetParams。
	MethodDiagnosticSet = "diagnostic/set"
	// MethodDiagnosticList は、拡張機能から本体へのリクエストである。
	// params は DiagnosticListParams、result は DiagnosticListResult。
	MethodDiagnosticList = "diagnostic/list"
	// MethodSignSet は、拡張機能から本体への通知である。params は SignSetParams。
	MethodSignSet = "sign/set"
	// MethodPopupShow は、拡張機能から本体への通知である。params は PopupShowParams。
	MethodPopupShow = "popup/show"
	// MethodOutputAppend は、拡張機能から本体への通知である。params は OutputAppendParams。
	MethodOutputAppend = "output/append"
	// MethodChoiceAsk は、拡張機能から本体へのリクエストである。
	// params は ChoiceAskParams、result は ChoiceAskResult。
	// 利用者の応答を待つので、本体はキー入力ごとの問い合わせの期限（基本設計 5.4）を適用しない。
	MethodChoiceAsk = "choice/ask"
	// MethodInputAsk は、拡張機能から本体へのリクエストである（API 0.21.0 で足した。FR-275）。
	// params は InputAskParams、result は InputAskResult。
	// choice/ask と同じく利用者の応答を待つので、本体はキー入力ごとの問い合わせの期限（基本設計 5.4）を適用しない。
	// 古い本体は method not found で応答する。
	MethodInputAsk = "input/ask"
)

// InitializeParams は、initialize の params である。
type InitializeParams struct {
	// APIVersion は、本体が実装している拡張機能 API のバージョンである。
	APIVersion string `json:"apiVersion"`
	// Settings は、利用者の設定ファイルのうち、この拡張機能に向けた部分である。
	// 形は拡張機能ごとに決まるので、本体は解釈せずに JSON のまま渡す。設定が無ければ省く。
	Settings json.RawMessage `json:"settings,omitempty"`
	// NoBufferSync は、宣言ファイルの noBufferSync（Manifest.NoBufferSync）のとおり、本体がこの拡張機能へバッファの同期を送らないことを表す
	// （API 0.16.0 で足した。#367）。ext/server は、true なら WaitBuffer をすぐ誤りで返す（待っても buffer/open が届かないため）。
	NoBufferSync bool `json:"noBufferSync,omitempty"`
}

// InitializeResult は、initialize の result である。
type InitializeResult struct {
	// APIVersion は、拡張機能が前提にした拡張機能 API のバージョンである。
	// 本体のバージョンと食い違った場合の扱いは未決である（要件定義 7 章）。
	APIVersion string `json:"apiVersion"`
}

// BufferOpenParams は、buffer/open の params である。
// 本体は、拡張機能が稼働中になった時点で開いているバッファと、その後に開いたバッファについて送る。
// 再起動した拡張機能にも、開いているバッファの全文を送り直す（基本設計 5.2）。
// buffer/resync を受け取ったときも、そのバッファについて送り直す。
// 拡張機能は、既に持っているバッファについて受け取った場合、持っている内容を Text で置き換える。
//
// 本体は、不正な UTF-8 を含むバッファを拡張機能へ送らない（基本設計 4.1、5.3）。
// JSON の文字列は不正なバイトを表せず、送ると本体と拡張機能で行頭からのバイト数がずれるためである。
// そうしたバッファには buffer/open も buffer/change も届かず、送った後に不正なバイトが入ったら buffer/close が届く。
// 不正なバイトが無くなったら、新しい全文の buffer/open が届く。
// このため Text と、buffer/change の Edit の Text は、常に正しい UTF-8 である。
type BufferOpenParams struct {
	Buffer BufferID `json:"buffer"`
	// Path は、バッファに対応するファイルの絶対パスである。ファイルに対応しないバッファでは空。
	// 本体は、利用者が相対パスで開いたファイルを、本体の作業ディレクトリをもとに絶対パスにして渡す。
	// シンボリックリンクは解決しない（基本設計 6.7 のバッファのパス）。
	// 拡張機能は、パスの文字列の一致で自分の複製を探せる。
	Path string `json:"path,omitempty"`
	// FileType は、ファイルの種類（"markdown" など）である。決まらなければ空。
	FileType string `json:"fileType,omitempty"`
	// Version は、Text の時点のバージョン番号である。
	Version Version `json:"version"`
	// Text は、バッファの全文である。
	// 位置を数えるもとのテキスト（行を "\n" でつないだもの）で、末尾の改行の有無は含めない（基本設計 4.1）。
	Text string `json:"text"`
	// FinalNewline は、本体がこのバッファをファイルへ書き出したときに、最後の行の後に改行が付くかどうかである。
	// ファイルの末尾の改行の有無と、末尾の改行を補う設定（Vim の 'fixendofline'）から本体が決める（基本設計 4.1）。
	// 行の無いバッファ（Vim の --No lines in buffer--。Text は空）は空のファイルとして書き出すので false にする。
	// 値が変わると（全ての行を消した、行の無いバッファに入力した、など）、本体は buffer/open を送り直す。
	// LSP の拡張機能は、true のときだけ、言語サーバーへ送る全文の末尾に改行を足す。
	// 省いた場合（nil）は、改行が付くものとして扱う。本体は常に送る。値は HasFinalNewline で読む。
	FinalNewline *bool `json:"finalNewline,omitempty"`
}

// HasFinalNewline は、FinalNewline の値を返す。FinalNewline が nil なら true を返す。
func (p *BufferOpenParams) HasFinalNewline() bool {
	return p.FinalNewline == nil || *p.FinalNewline
}

// BufferChangeParams は、buffer/change の params である。
// 本体は、バッファの変更（トランザクションの適用、アンドゥ、リドゥ）のたびに送る。
//
// Edits の位置は、Transaction と同じ逐次の数え方で書く。
// 先頭の編集は FromVersion の時点のテキストの位置で、以後の編集は直前の編集までを適用した後のテキストの位置で数える。
// 拡張機能は、Edits を先頭から順に適用すると、ToVersion の時点のテキストを得る。
//
// FromVersion が拡張機能の持つバージョン番号と一致しない場合、拡張機能は差分が欠けたと判断し、
// Edits を適用せずに buffer/resync を送る（基本設計 5.3）。
// 一致した場合は、Edits を適用した後に ToVersion を自分の番号にする。
// ToVersion は FromVersion より大きい。拡張機能は差が 1 であることを前提にしない。
type BufferChangeParams struct {
	Buffer BufferID `json:"buffer"`
	// FromVersion は、Edits を適用する前のバージョン番号である。
	FromVersion Version `json:"fromVersion"`
	// ToVersion は、Edits を適用した後のバージョン番号である。
	ToVersion Version `json:"toVersion"`
	// Edits は、適用した編集の列である。位置は逐次の数え方で書く。
	Edits []Edit `json:"edits"`
}

// BufferCloseParams は、buffer/close の params である。
// 本体は、バッファを閉じたときのほか、送ったバッファに不正な UTF-8 が入ったとき（[BufferOpenParams]）にも送る。
// 拡張機能が editor/open で不正な UTF-8 を含むバッファを開いた場合は、その拡張機能へ応答より先に送る。
// このため、拡張機能は持っていないバッファの buffer/close を受け取ることがあり、そのときは何もしなくてよい。
type BufferCloseParams struct {
	Buffer BufferID `json:"buffer"`
}

// MotionResolveParams は、motion/resolve の params である。
// キー入力ごとに結果が要る問い合わせで、本体は期限付きで応答を待つ（基本設計 5.4）。
type MotionResolveParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、問い合わせた時点のバージョン番号である。
	Version Version `json:"version"`
	// ID は、宣言ファイルの contributes.motions に書いたモーションの ID である。
	ID string `json:"id"`
	// Cursor は、問い合わせた時点のカーソル位置である。
	Cursor Position `json:"cursor"`
	// Count は、回数である。0 は省略を表す。
	Count int `json:"count"`
	// Args は、操作の引数である。無ければ省く。
	Args json.RawMessage `json:"args,omitempty"`
}

// MotionResolveResult は、motion/resolve の result である。
type MotionResolveResult struct {
	// OK は、移動先が見つかったかどうかである。false なら操作は失敗する。
	OK bool `json:"ok"`
	// Version は、計算のもとにしたバージョン番号である。
	// 本体は、現在のバージョン番号と一致しなければ結果を破棄する。
	Version Version `json:"version"`
	// Position は、移動先の位置である。
	Position Position `json:"position"`
	// Linewise は、オペレーターと組み合わせたときに行単位で扱うかどうかである（Vim の j や G）。
	// false（省略を含む）なら文字単位で扱う。
	Linewise bool `json:"linewise,omitempty"`
	// Inclusive は、文字単位で扱うときに、移動先の文字を範囲に含めるかどうかである（Vim の e や f）。
	// false（省略を含む）なら含めない（exclusive）。Linewise が true なら読まない。
	Inclusive bool `json:"inclusive,omitempty"`
}

// TextObjectResolveParams は、textObject/resolve の params である。
// キー入力ごとに結果が要る問い合わせで、本体は期限付きで応答を待つ（基本設計 5.4）。
type TextObjectResolveParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、問い合わせた時点のバージョン番号である。
	Version Version `json:"version"`
	// ID は、宣言ファイルの contributes.textObjects に書いたテキストオブジェクトの ID である。
	ID string `json:"id"`
	// Cursor は、問い合わせた時点のカーソル位置である。
	Cursor Position `json:"cursor"`
	// Count は、回数である。0 は省略を表す。
	Count int `json:"count"`
	// Args は、操作の引数である。無ければ省く。
	Args json.RawMessage `json:"args,omitempty"`
	// Selection は、文字単位か行単位のビジュアルモードで問い合わせたときの、今の選択の範囲（半開区間）である。
	// 文字単位なら選択の始まりから最後の文字の直後まで（最後の文字が行末の後なら次の行の先頭まで）、
	// 行単位なら最初の行の先頭から最後の行の行末までである。
	// 文字単位で 1 文字だけを選んでいるとき（v を打った直後）と、ビジュアルモードでないとき（オペレーターの後）は省く。
	// 選択を広げるテキストオブジェクト（tree-sitter の an。基本設計 6.1）は、これより真に大きい範囲を返し、ReplaceSelection を true にする。
	// 足したのは API 0.18.0 である（#373）。古い本体は送らないので、拡張機能は省かれた場合をカーソルからの問い合わせとして扱う。
	Selection *Range `json:"selection,omitempty"`
}

// TextObjectResolveResult は、textObject/resolve の result である。
type TextObjectResolveResult struct {
	// OK は、範囲が見つかったかどうかである。false なら操作は失敗する。
	OK bool `json:"ok"`
	// Version は、計算のもとにしたバージョン番号である。
	// 本体は、現在のバージョン番号と一致しなければ結果を破棄する。
	Version Version `json:"version"`
	// Range は、テキストオブジェクトの範囲である。
	Range Range `json:"range"`
	// Linewise は、範囲を行単位で扱うかどうかである（Vim の ip）。false（省略を含む）なら文字単位で扱う。
	Linewise bool `json:"linewise,omitempty"`
	// ReplaceSelection は、ビジュアルモードで、今の選択を Range で置き換えるかどうかである。
	// true なら、本体は選択の両端を Range の両端にし（カーソルが選択の始まりの側にあれば始まりの側に置き）、
	// Linewise なら行単位の、そうでなければ文字単位のビジュアルモードにする。
	// false（省略を含む）なら、Vim の組み込みのテキストオブジェクトと同じく、選択の動く側の端から Range の終わりまで選択を広げる。
	// オペレーターの後では読まない。足したのは API 0.18.0 である（#373）。古い本体は読まずに捨て、選択を広げる。
	ReplaceSelection bool `json:"replaceSelection,omitempty"`
}

// OperatorApplyParams は、operator/apply の params である。
// キー入力ごとに結果が要る問い合わせで、本体は期限付きで応答を待つ（基本設計 5.4）。
type OperatorApplyParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、問い合わせた時点のバージョン番号である。
	Version Version `json:"version"`
	// ID は、宣言ファイルの contributes.operators に書いたオペレーターの ID である。
	ID string `json:"id"`
	// Range は、オペレーターを適用する範囲である。
	Range Range `json:"range"`
	// Count は、回数である。0 は省略を表す。
	Count int `json:"count"`
	// Args は、操作の引数である。無ければ省く。
	Args json.RawMessage `json:"args,omitempty"`
}

// OperatorApplyResult は、operator/apply の result である。
type OperatorApplyResult struct {
	// OK は、オペレーターが編集を返したかどうかである。false なら操作は失敗し、本体はバッファを変えない。
	OK bool `json:"ok"`
	// Transaction は、オペレーターの編集である。
	// 本体は、BaseVersion が現在のバージョン番号と一致した場合だけ適用する。
	Transaction Transaction `json:"transaction"`
}

// CommandExecuteParams は、command/execute の params である。
// 本体は、利用者がコマンドを呼んだときに送る。
// 応答を待つかどうかと期限は、キー入力ごとの問い合わせ（基本設計 5.4）と同じに扱ってよい。
type CommandExecuteParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、問い合わせた時点のバージョン番号である。
	Version Version `json:"version"`
	// ID は、宣言ファイルの contributes.commands に書いたコマンドの ID である。
	ID string `json:"id"`
	// Cursor は、問い合わせた時点のカーソル位置である。
	Cursor Position `json:"cursor"`
	// Count は、回数である。0 は省略を表す。
	Count int `json:"count"`
	// Args は、コマンドの引数である。無ければ省く。
	// コマンドラインから引数を付けて呼んだとき（:acp.prompt 説明して）は {"text":"説明して"}、
	// キーで呼んで後に 1 文字を取ったときは {"char":"x"} である（package の doc の「キー入力ごとの問い合わせ」）。
	Args json.RawMessage `json:"args,omitempty"`
	// Sync は、本体がマクロかドットリピートを再生している（FR-19）ことを表す（基本設計 6 章の冒頭）。
	// true なら、拡張機能は結果がそろうまで応答を返さず、編集を応答の Transaction で、カーソルの移動を応答の Open で返す。
	// 後から buffer/edit で届けた編集は、再生の次の操作でバージョン番号が進んだ後に届いて破棄されるためである。
	// 再生の結果を変えない表示（popup/show、output/append、message/show）は、true でも後から届けてよい。
	Sync bool `json:"sync,omitempty"`
	// Withheld は、本体が Buffer を不正な UTF-8 のために拡張機能へ送っていない（[BufferOpenParams]）ことを表す。
	// 拡張機能は Buffer の複製を持っていないが、バッファを使わないコマンド（デバッグの再開など）は実行できる。
	// バッファを使うコマンドは、OK を false で返す。本体は、送っていないために使えないことを利用者へ知らせる。
	// ext/server は、true のとき、ハンドラーへ複製の代わりに nil を渡す。
	Withheld bool `json:"withheld,omitempty"`
}

// CommandExecuteResult は、command/execute の result である。
type CommandExecuteResult struct {
	// OK は、コマンドが成り立ったかどうかである。false なら操作は失敗し、本体はバッファを変えない。
	OK bool `json:"ok"`
	// Transaction は、コマンドの編集である。編集しないコマンドでは省く。
	// 本体は、BaseVersion が現在のバージョン番号と一致した場合だけ適用する。
	// 編集を buffer/edit でなく応答で返すのは、本体が応答を待っている間に buffer/edit を処理できないからである。
	Transaction *Transaction `json:"transaction,omitempty"`
	// Open は、カーソルの移動である。形は editor/open の params と同じで、本体は Transaction を適用した後に移動する。
	// 主に params の Sync が true のときに、後から editor/open で送る代わりに使う。移動しなければ省く。
	// Open.Version を指定するなら、Transaction を適用した後のバージョン番号で書く。
	// 本体は、Open が EditorOpenParams.Validate を通らなければ、操作を失敗として扱う。
	Open *EditorOpenParams `json:"open,omitempty"`
}

// PingParams は、ping の params である。中身は無い。
type PingParams struct{}

// PingResult は、ping の result である。中身は無い。
type PingResult struct{}

// CancelParams は、$/cancel の params である。
// 本体は、期限を超えたリクエストについて送る。
// 拡張機能が取り消しに応じなくても、本体は後から届いた結果を破棄する。
type CancelParams struct {
	// ID は、取り消すリクエストの JSON-RPC の id である。
	// id は数値と文字列のどちらでもよいので、受け取った形のまま持つ。
	ID json.RawMessage `json:"id"`
}

// ShutdownParams は、shutdown の params である。中身は無い。
//
// 拡張機能は、shutdown を受け取ったら、自分が起動した子プロセス（言語サーバー、デバッグアダプター、デバッグ対象など）を
// 正しい手順（LSP の shutdown と exit、DAP の disconnect など）で終わらせてから、自分も終了する。
// 本体は shutdown を送った後、その拡張機能へ問い合わせと生存確認を送らない。
// 期限（本体の設定。仮の値）までに終わらなければ、本体はプロセスグループへ SIGTERM を送り、
// さらに期限を過ぎたら SIGKILL を送る（基本設計 5.2）。
// 拡張機能は、終わるまでの間に本体へ通知を送ってよいが、本体がそれを処理する保証は無い。
type ShutdownParams struct{}

// BufferEditParams は、buffer/edit の params である。
// 補完の確定や整形など、拡張機能の側から始める編集に使う。
//
// NoLines と EndOfLine は、テキストの編集では表せない、書き出したときの末尾の改行を決める状態（基本設計 4.1）を変える。
// 外から書き換えたファイルを読み直したのと同じ状態にするために使う（ACP の fs/write_text_file。基本設計 6.5）。
// 本体は、BaseVersion が現在のバージョン番号と一致した場合だけ、Edits を適用した後にこの 2 つを反映する。
// Edits が空でも反映する。そのときバージョン番号は変わらず、result の Applied は false になる。
// 反映して FinalNewline（BufferOpenParams）が変わったら、本体は buffer/open を送り直す。
type BufferEditParams struct {
	Buffer      BufferID    `json:"buffer"`
	Transaction Transaction `json:"transaction"`
	// NoLines は、Edits を適用した後のバッファを、行の無いバッファ（Vim の --No lines in buffer--）にするかどうかである。
	// true なら行の無いバッファにし、書き出すと空のファイルになる。適用した後のテキストが空でなければ、true は無視する。
	// false なら、テキストが空でも行の無いバッファでなくする（空の行 1 つとして書き出す）。
	// 行の無いバッファの記録はアンドゥの履歴の位置に付き、アンドゥとリドゥでその位置へ戻ると、また行の無いバッファになる。
	// 省いた場合（nil）は変えない。
	NoLines *bool `json:"noLines,omitempty"`
	// EndOfLine は、読み込んだファイルの最後の行の後に改行があったものとして扱うかどうか（Vim の 'endofline'）である。
	// 本体は、これが true か、末尾の改行を補う設定（Vim の 'fixendofline'）が有効なら、書き出すときに最後の行の後に改行を付ける。
	// アンドゥでは戻らない。省いた場合（nil）は変えない。
	EndOfLine *bool `json:"endOfLine,omitempty"`
}

// ErrorCodeReadOnly は、編集を禁止したバッファ（基本設計 4.6。FR-118）への buffer/edit を本体が断るときの、誤りの応答の code である。
// JSON-RPC 2.0 で実装が定める誤りの範囲（-32000〜-32099）の値である。message は画面に出すものと同じ E21 の文言である。
// 編集の無い（NoLines と EndOfLine だけの）要求も断る。
// 版の不一致（BufferEditResult の Applied が false）と違い、新しい版で作り直しても通らないので、拡張機能は繰り返さない。
// ext/server を使う拡張機能は、server.IsReadOnly で見分けられる。
const ErrorCodeReadOnly int64 = -32001

// BufferEditResult は、buffer/edit の result である。
// 編集を禁止したバッファへの要求では result を返さず、ErrorCodeReadOnly の誤りの応答を返す。
type BufferEditResult struct {
	// Applied は、本体がトランザクションを適用したかどうかである。
	// BaseVersion が現在のバージョン番号と一致しなければ false。
	Applied bool `json:"applied"`
	// Version は、応答の時点のバージョン番号である。
	Version Version `json:"version"`
}

// Decoration は、色を付ける 1 つの範囲である。
type Decoration struct {
	Range Range `json:"range"`
	// Style は、色の種類の名前（"keyword" など）である（基本設計 6.1、7.1。FR-91）。
	// 名前から色への対応は本体が持ち、利用者は設定の colors で名前ごとに変えられる。
	// 構文の色の名前は comment、keyword、string、number、constant、function、type、variable、operator、punctuation の 10 個で、
	// 前に "syntax." を付けない。構文の色の他に、本体の色の名前（docs/usage.md の「見た目の設定」の表。診断の diagnostic.error、
	// 画面の部品の statusline など）のどれを使ってもよい。本体が知らない名前の範囲には色を付けない。
	Style string `json:"style"`
	// Attr は、名前の色の上に重ねて直に付ける色と飾りである（FR-230。TextAttr）。nil なら付けない。
	// 名前を空にして Attr だけを付けてもよい。0.18.0 までの本体は、Attr を付けた範囲のある通知の全体を誤りとして捨てる（decoration.go）。
	// 受け取った一覧では、styles の表の同じ項目を指す範囲が同じ値を共有するので、読むだけにする。
	Attr *TextAttr `json:"attr,omitempty"`
}

// DecorationSetParams は、decoration/set の params である。
// 本体は、受け取り済みの一覧で描画し、描画のたびに拡張機能へ問い合わせない（基本設計 6.1）。
//
// 一覧は、送った拡張機能とバッファの組ごとに持つ。
// 本体は、同じ拡張機能から同じバッファについて前に受け取った一覧だけを置き換え、
// 他の拡張機能が同じバッファに付けた一覧は変えない。
// 空の一覧を送ると、その拡張機能がそのバッファに付けた色を消す。
//
// JSON では、範囲の一覧を範囲ごとのオブジェクトではなく、整数の列と色の種類の名前の表で表す（MarshalJSON。形は decoration.go の decorationFields の doc）。
// 大きなファイルでも本体の受信の上限に収めるためである。Go の値としては Decorations の一覧のまま扱える。
type DecorationSetParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、一覧を計算するもとにしたバージョン番号である。
	Version Version `json:"version"`
	// Decorations は、バッファ全体について、この拡張機能が色を付ける範囲の一覧である。
	// JSON では styles と data の 2 つの項目になる（MarshalJSON）ので、タグは使わない。
	Decorations []Decoration `json:"-"`
}

// MessageLevel は、利用者へのメッセージの重要度である。
type MessageLevel string

// メッセージの重要度。
const (
	MessageInfo    MessageLevel = "info"
	MessageWarning MessageLevel = "warning"
	MessageError   MessageLevel = "error"
)

// MessageShowParams は、message/show の params である。
type MessageShowParams struct {
	Level MessageLevel `json:"level"`
	// Text は、表示するメッセージである。
	Text string `json:"text"`
}

// BufferResyncParams は、buffer/resync の params である。
// buffer/change の FromVersion が自分の番号と一致しなかった拡張機能が送る（FR-16 の差分が欠けた場合の回復）。
// 本体は、そのバッファの現在の全文を buffer/open で送り直す。
// 本体が既に閉じたバッファについて受け取った場合は、何もしない。
type BufferResyncParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、拡張機能が持っているバージョン番号である。本体はログに記録するだけで、送り直しの判断には使わない。
	Version Version `json:"version"`
}

// BufferWillSaveParams は、buffer/willSave の params である。
// 本体は、バッファを保存する直前に、そのバッファを持っている稼働中の拡張機能のうち、
// 宣言ファイルの saveHook が true のものへ、1 つずつ順に送る（基本設計 6.7 の保存の流れ）。
// 未起動の拡張機能は起動しない。
type BufferWillSaveParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、問い合わせた時点のバージョン番号である。
	// 前の拡張機能の編集を適用した後なら、適用した後の番号である。
	Version Version `json:"version"`
}

// BufferWillSaveResult は、buffer/willSave の result である。
type BufferWillSaveResult struct {
	// Transaction は、保存する前に適用する編集（整形など）である。編集しないなら省く。
	// 本体は、BaseVersion が現在のバージョン番号と一致した場合だけ適用する。
	// 期限（仮に 1000 ms）を超えた応答は待たずに次へ進み、後から届いた応答は捨てる。
	Transaction *Transaction `json:"transaction,omitempty"`
}

// BufferDidSaveParams は、buffer/didSave の params である。
// 本体は、バッファをファイルへ書き出した後に、そのバッファを持っている稼働中の拡張機能すべてへ送る。
type BufferDidSaveParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、書き出した時点のバージョン番号である。
	Version Version `json:"version"`
	// Path は、書き出したファイルの絶対パスである。
	Path string `json:"path"`
}

// EditorStateParams は、editor/state の params である。中身は無い。
type EditorStateParams struct{}

// Mode は、本体のモードの名前である。
type Mode string

// モードの名前（基本設計 6.7 の編集の状態とバッファの操作）。
const (
	ModeNormal      Mode = "normal"
	ModeInsert      Mode = "insert"
	ModeVisual      Mode = "visual"
	ModeVisualLine  Mode = "visualLine"
	ModeVisualBlock Mode = "visualBlock"
	ModeCommandLine Mode = "commandLine"
)

// EditorStateResult は、editor/state の result である。
type EditorStateResult struct {
	// Buffer は、カーソルのあるバッファの番号である。
	Buffer BufferID `json:"buffer"`
	// Cursor は、カーソル位置である。
	Cursor Position `json:"cursor"`
	// Mode は、モードである。
	Mode Mode `json:"mode"`
	// Selections は、ビジュアルモードの選択範囲である。ビジュアルモードでなければ省く。
	// ModeVisual では範囲 1 つ。
	// ModeVisualLine では範囲 1 つで、最初の行の先頭から最後の行の終わりまでを指す。
	// ModeVisualBlock では行ごとに 1 つの範囲を、上の行から順に並べる。
	Selections []Range `json:"selections,omitempty"`
	// Buffers は、開いているバッファの一覧である。
	Buffers []BufferInfo `json:"buffers"`
	// CanSplit は、カーソルのあるタブの client のペインを、端末多重化ソフトの層で分割できるかである（diff/start。#245）。
	// 端末多重化ソフトの外（FR-60）では false で、省く。
	CanSplit bool `json:"canSplit,omitempty"`
	// WorkDir は、操作中の client の作業ディレクトリ（:e の相対パスを解く場所）の絶対パスである（API 0.9.0 で足した）。
	// 段階 3 では client ごとに作業ディレクトリが違いうるので、拡張機能のプロセスの作業ディレクトリと一致するとは限らない。
	// ファジーファインダーがファイルと検索の場所に使う（基本設計 6.9）。
	WorkDir string `json:"workDir,omitempty"`
}

// BufferInfo は、editor/state で返す、開いているバッファ 1 つの情報である。
type BufferInfo struct {
	Buffer BufferID `json:"buffer"`
	// Path は、バッファに対応するファイルの絶対パスである。ファイルに対応しないバッファでは空。
	Path string `json:"path,omitempty"`
	// FileType は、ファイルの種類である。決まらなければ空。
	FileType string `json:"fileType,omitempty"`
	// Version は、現在のバージョン番号である。
	Version Version `json:"version"`
	// Modified は、保存していない変更があるかどうかである。
	Modified bool `json:"modified"`
	// ReadOnly は、編集を禁止したバッファ（buffer/edit を断る）かどうかである。
	// 複数のバッファへの編集を、全部か何もしないかで適用する拡張機能（LSP の名前の変更）が、前もって確かめるのに使う。
	ReadOnly bool `json:"readOnly,omitempty"`
	// ChangedOnDisk は、読み込んだ後にファイルが外で変わった（buffer/save が断られる）かどうかである。
	ChangedOnDisk bool `json:"changedOnDisk,omitempty"`
}

// EditorOpenParams は、editor/open の params である。
// ファイルかバッファを開き、カーソルを指定の位置へ動かす。
// command/execute の result の Open も同じ形である。
//
// 本体は、既に開いているファイルなら新しく開かずにそのバッファを使う。
// 要求した拡張機能がそのバッファの複製を持っていなければ、応答より先にその拡張機能へ buffer/open を送る。
// 拡張機能の側では、応答を受け取った時点で複製がまだできていないことがあるので、
// ext/server の WaitBuffer で、応答のバージョン番号以降の複製ができるまで待ってから読む。
type EditorOpenParams struct {
	// Path は、開くファイルの絶対パスである。Buffer とどちらか一方を指定する。
	Path string `json:"path,omitempty"`
	// Buffer は、開いているバッファの番号である。Path とどちらか一方を指定する。
	Buffer *BufferID `json:"buffer,omitempty"`
	// Position は、カーソルの移動先である。省くとカーソルを動かさない。
	Position *Position `json:"position,omitempty"`
	// Version は、Position を計算するもとにしたバージョン番号である。
	// 指定すると、現在のバージョン番号と一致したときだけカーソルを動かす（バッファは一致しなくても開く）。
	// まだ開いていないファイルでは使わない。
	Version *Version `json:"version,omitempty"`
	// Focus は、true ならそのバッファをカーソルのあるバッファにする。false なら開くだけでカーソルを移さない。
	Focus bool `json:"focus,omitempty"`
	// Peek は、開かずに「ポップアップで見る」ことを表す（FR-110。基本設計 1.4）。
	// 本体は、Path か Buffer の Position の周りの行の写しを、編集を禁止したファイルでないバッファとして、
	// 問い合わせを始めた client のカーソルの近くのポップアップに出す。Position は省けない。Focus は読まない。
	// 写しのポップアップを :tabopen でタブへ広げると、本物のファイルを開いて Position へ移る。
	// 応答の Buffer は写しのバッファの番号、Moved は false である。
	Peek bool `json:"peek,omitempty"`
}

// Validate は、Path と Buffer のどちらか一方だけを指定していることと、Path が絶対パスであることを検査する。
// 本体は、誤りのある params のリクエストに JSON-RPC のエラーで応答する。
//
// 絶対パスの判定は、"/" で始まるかどうかで行う（このパッケージは他のパッケージを import しないので、path/filepath を使わない）。
func (p *EditorOpenParams) Validate() error {
	switch {
	case p.Path == "" && p.Buffer == nil:
		return errors.New("path と buffer のどちらも指定していない")
	case p.Path != "" && p.Buffer != nil:
		return errors.New("path と buffer の両方を指定している")
	case p.Path != "" && !strings.HasPrefix(p.Path, "/"):
		return fmt.Errorf("path %q が絶対パスでない", p.Path)
	case p.Peek && p.Position == nil:
		return errors.New("peek では position を省けない")
	}
	return nil
}

// EditorOpenResult は、editor/open の result である。
type EditorOpenResult struct {
	// Buffer は、開いたバッファの番号である。
	Buffer BufferID `json:"buffer"`
	// Version は、応答の時点のバージョン番号である。
	Version Version `json:"version"`
	// Moved は、カーソルを動かしたかどうかである。
	// Position を省いたときと、Version の照合で動かさなかったときは false。
	Moved bool `json:"moved"`
}

// BufferSaveParams は、buffer/save の params である。
// buffer/save による保存も、利用者の保存と同じ保存の流れ（buffer/willSave から buffer/didSave まで）を通る。
type BufferSaveParams struct {
	Buffer BufferID `json:"buffer"`
	// Version を指定すると、現在のバージョン番号と一致したときだけ書き出す。
	Version *Version `json:"version,omitempty"`
	// SkipHooks は、true なら保存の流れの buffer/willSave を飛ばし、どの拡張機能にも問い合わせずに書き出す。
	// ACP の拡張機能が、エージェントの書いた内容をそのまま書き出すために使う（基本設計 6.5）。
	SkipHooks bool `json:"skipHooks,omitempty"`
}

// BufferSaveResult は、buffer/save の result である。
type BufferSaveResult struct {
	// Saved は、書き出したかどうかである。
	Saved bool `json:"saved"`
	// Version は、応答の時点のバージョン番号である。
	Version Version `json:"version"`
}

// DiagnosticSeverity は、診断の重要度である。
type DiagnosticSeverity string

// 診断の重要度。
const (
	SeverityError   DiagnosticSeverity = "error"
	SeverityWarning DiagnosticSeverity = "warning"
	SeverityInfo    DiagnosticSeverity = "info"
	SeverityHint    DiagnosticSeverity = "hint"
)

// Diagnostic は、1 つの診断である。
type Diagnostic struct {
	// Range は、診断の範囲である（基本設計 4.1 の位置）。
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity"`
	// Message は、本文である。
	Message string `json:"message"`
	// Source は、出どころ（"gopls" など）である。無ければ省く。
	Source string `json:"source,omitempty"`
	// Code は、診断の種類を表す文字列である。無ければ省く。
	Code string `json:"code,omitempty"`
}

// DiagnosticSetParams は、diagnostic/set の params である。
//
// 本体は、送った拡張機能とバッファの組ごとに診断の一覧を持ち、同じ組の次の一覧で置き換える。
// 空の一覧を送ると、その組の一覧を消す。
// decoration/set と違い、Version が現在より古くても、本体が覚えている編集の範囲にあれば、その後の編集に合わせて位置をずらしてから持つ。
// それより古いか、現在より新しい一覧は捨てる（基本設計 6.7 の表示のデータ）。
type DiagnosticSetParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、一覧を計算するもとにしたバージョン番号である。
	Version Version `json:"version"`
	// Diagnostics は、このバッファについて、この拡張機能が持つ診断のすべてである。
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// DiagnosticListParams は、diagnostic/list の params である。
type DiagnosticListParams struct {
	// Buffer は、対象のバッファの番号である。省くと開いているバッファすべてを対象にする。
	Buffer *BufferID `json:"buffer,omitempty"`
}

// DiagnosticListResult は、diagnostic/list の result である。
type DiagnosticListResult struct {
	// Entries は、バッファと、診断を送った拡張機能の組ごとの一覧である。
	Entries []DiagnosticEntry `json:"entries"`
}

// DiagnosticEntry は、1 つのバッファについて、1 つの拡張機能から受け取った診断の一覧である。
type DiagnosticEntry struct {
	Buffer BufferID `json:"buffer"`
	// Extension は、診断を送った拡張機能の ID である。
	Extension string `json:"extension"`
	// Version は、そのバッファの現在のバージョン番号である。
	// Diagnostics の位置は、本体が編集に合わせてずらした位置で、この番号の時点のテキストを指す。
	Version Version `json:"version"`
	// Diagnostics は、診断の一覧である。
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// SignKind は、行に付ける印の種類である。名前は仮である。
type SignKind string

// 印の種類。
const (
	// SignBreakpoint は、ブレークポイントである。
	SignBreakpoint SignKind = "breakpoint"
	// SignBreakpointUnverified は、デバッグアダプターが検証できなかったブレークポイントである。
	SignBreakpointUnverified SignKind = "breakpointUnverified"
	// SignExecutionPoint は、デバッグ対象が停止した位置である。
	SignExecutionPoint SignKind = "executionPoint"
)

// Sign は、行に付ける 1 つの印である。
type Sign struct {
	// Line は、0 から数えた行の番号である。
	Line int      `json:"line"`
	Kind SignKind `json:"kind"`
}

// SignSetParams は、sign/set の params である。
// 本体の持ち方とバージョン番号の扱いは、diagnostic/set と同じである（DiagnosticSetParams）。
// 1 つの行に複数の印があれば、本体が表示の優先順位を決める。
type SignSetParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、一覧を計算するもとにしたバージョン番号である。
	Version Version `json:"version"`
	// Signs は、このバッファについて、この拡張機能が付ける印のすべてである。
	Signs []Sign `json:"signs"`
}

// PopupFormat は、popup/show の本文の形式である。
type PopupFormat string

// 本文の形式。
const (
	PopupPlainText PopupFormat = "plaintext"
	// PopupMarkdown は、Markdown である。本体が整形して表示するかどうかは internal/ui が決め、整形しなければそのまま表示する。
	PopupMarkdown PopupFormat = "markdown"
)

// PopupShowParams は、popup/show の params である。
// 本体は、Buffer がカーソルのあるバッファで、Version が現在のバージョン番号と一致し、カーソルが Position にある場合だけ表示する。
// 表示は、次のキー入力かカーソルの移動で消す。届いた時点でカーソルが Position から動いているか、
// 最後の拡張機能の操作の問い合わせの後に次のキーを解釈していれば、既に消えたものとして出さない。
// 新しい popup/show は、前の表示を置き換える。
type PopupShowParams struct {
	// Buffer は、表示の基準にするバッファの番号である。
	Buffer BufferID `json:"buffer"`
	// Version は、本文を計算するもとにしたバージョン番号である。
	Version Version `json:"version"`
	// Position は、表示の基準にする位置である。
	Position Position `json:"position"`
	// Text は、本文である。
	Text   string      `json:"text"`
	Format PopupFormat `json:"format"`
}

// OutputAppendParams は、output/append の params である。
// 本体は、拡張機能と欄の名前の組ごとにテキストを持ち、行数が上限（仮に 10000 行）を超えたら古い行から捨てる。
// 欄はバッファではないので、拡張機能へ buffer/open で送らない。
type OutputAppendParams struct {
	// Channel は、欄の名前（"dap"、"acp" など）である。
	Channel string `json:"channel"`
	// Text は、書き足すテキストである。改行を含んでよい。
	Text string `json:"text"`
}

// Choice は、choice/ask の 1 つの選択肢である。
type Choice struct {
	// ID は、選択肢の ID である。問いの中で一意にする。
	ID string `json:"id"`
	// Label は、利用者に示す文である。
	Label string `json:"label"`
}

// ChoiceAskParams は、choice/ask の params である。
// 本体は一度に 1 つの問いだけを表示し、表示中に届いた問いは順に待たせる。
// 問いを出した拡張機能が再起動した場合、その拡張機能の問いを取り下げる。
type ChoiceAskParams struct {
	// Title は、問いの文である。
	Title   string   `json:"title"`
	Choices []Choice `json:"choices"`
}

// ChoiceAskResult は、choice/ask の result である。
type ChoiceAskResult struct {
	// Selected は、選んだ選択肢の ID である。取り消した場合は空。
	Selected string `json:"selected,omitempty"`
	// Cancelled は、利用者が選ばずに取り消したかどうかである。
	Cancelled bool `json:"cancelled,omitempty"`
}

// InputAskParams は、input/ask の params である（基本設計 1.4 の「1 行の入力を聞くリクエスト」。FR-275。API 0.21.0 で足した）。
// 本体は、問いを出した client のコマンドラインの行（設定 commandLine.popup が真なら画面の上のポップアップ）に、Title を前置きにして
// Default を入力の初めの中身として出し、利用者に 1 行を編集させる。
// choice/ask と同じ 1 本の列に並べ、一度に 1 つの問いだけを出し、表示中に届いた問いは順に待たせる。
// 問いを出した拡張機能が再起動した場合、その拡張機能の問いを取り下げる。
// 答えのキーはマクロに記録しないので、再生中（sync が true）の操作の中では送らない（choice/ask と同じ取り決め）。
type InputAskParams struct {
	// Title は、問いの文である。入力の前置きとして出す（"式: " のように、区切りの空白も含めて渡す）。改行を含めない。
	Title string `json:"title"`
	// Default は、入力の初めの中身（既定値）である。省ける。改行を含めない。
	Default string `json:"default,omitempty"`
}

// Validate は、params が本体の受け付ける形かを確かめる。Title と Default は改行を含めない（1 行に出すため）。
// 本体は、通らない input/ask を JSON-RPC の誤り（invalid params）で断る。
func (p *InputAskParams) Validate() error {
	if strings.ContainsAny(p.Title, "\r\n") {
		return errors.New("title は改行を含められない")
	}
	if strings.ContainsAny(p.Default, "\r\n") {
		return errors.New("default は改行を含められない")
	}
	return nil
}

// InputAskResult は、input/ask の result である。
// 確定（<CR>）と取り消し（<Esc> か <C-c>）を Cancelled で分ける。空の文字列を確定したときは、Value が空で Cancelled が false である。
// choice/ask と同じく、取り消しを JSON-RPC の誤りにしない。
type InputAskResult struct {
	// Value は、確定した中身である。空の文字列もある。取り消したら空である。
	Value string `json:"value"`
	// Cancelled は、利用者が確定せずに取り消したかどうかである。
	Cancelled bool `json:"cancelled,omitempty"`
}
