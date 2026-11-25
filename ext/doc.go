// Package ext は、本体と拡張機能の間でやり取りするデータの型を定める、拡張機能 API の公開パッケージである。
//
// 拡張機能の作者は、このパッケージの型で JSON-RPC 2.0 のメッセージの params と result を読み書きする。
// 本体の内部の型（internal の下）は含めず、このパッケージは他のパッケージを import しない（基本設計 2 章）。
//
// 型は次の 3 つに分かれる。
//
//   - 位置、範囲、編集、トランザクションの、通信の上の表現（protocol.go。基本設計 4.1、4.2）
//   - メソッドの名前と、メソッドごとの params と result（message.go。基本設計 5.3）
//   - 宣言ファイルの型と、読み込んだ後の値の検査（manifest.go。基本設計 5.1）
//
// 拡張機能の側で使う補助の関数（メッセージループ、バッファの複製、ハンドラーの登録）は、サブパッケージ ext/server にある。
//
// # 位置の数え方
//
// Transaction と buffer/change の Edits は、どちらも逐次の数え方で位置を書く。
// 各編集の位置は、同じ列の中で直前の編集までを適用した後のテキストで数える（LSP の textDocument/didChange と同じ）。
// 詳しくは Transaction の doc にある。
//
// # キー入力ごとの問い合わせ
//
// motion/resolve、textObject/resolve、operator/apply の params と result は、次の項目を共通に持つ。
//
// Count は回数である。
// 0 は、利用者が回数を省略したことを表す。
// Vim の G と 1G のように、省略と 1 を区別する操作のために、本体は省略を 1 に読み替えずに送る。
// 区別しない操作は、0 を 1 として扱えばよい。
//
// Args は、操作が受け取る引数である（FR-12。例: surround の囲む文字）。
// 本体はドットリピートとマクロのために、拡張機能の ID、操作の ID と一緒に Args を記録し、再実行のときに同じ値を渡す。
// 引数が無ければ省く。
// いまの本体が送る形は次の 2 つである（どちらも仮。基本設計 6.7）。
//
//   - {"char":"x"}: キーで呼んだ操作の後に取った 1 文字
//   - {"text":"..."}: コマンドラインから呼んだコマンド（command/execute）の、ID の後の引数の文字列（:acp.prompt 説明して の「説明して」）。
//     ID の後の空白を除いた行末までで、"|" も含む。引数を書かなければ args を省く
//
// 引数を取らないコマンドは、text が空でなければ ok を false にして、引数を取らないことを message/show で知らせる
// （標準の拡張機能の dap.* と acp.cancel はそうする）。
//
// result の OK は、操作が成り立ったかどうかである。
// OK が false（項目が無い場合を含む）なら、本体は操作を失敗として扱い、他の項目を読まない。
// f の対象の文字が見つからない場合のように該当が無いときは、OK を false にした result を返す。
// 未知の ID や拡張機能の内部の誤りは JSON-RPC のエラーで返してもよく、本体はそれも操作の失敗として扱う。
package ext

// APIVersion は、拡張機能 API のバージョンである。
// 本体は initialize で拡張機能へ渡し、拡張機能は応答で自分が前提にしたバージョンを返す。
//
// 値は仮である。
// バージョンの付け方は基本設計 10 章で、本体と拡張機能のバージョンが食い違った場合の扱いは要件定義 7 章で、未決である。
// 0.2.0 で、decoration/set の範囲の一覧の JSON を整数の列（DecorationSetParams の MarshalJSON）に変えた（#53）。
// 0.3.0 で、buffer/edit に noLines と endOfLine を足した（BufferEditParams。#98）。
// 古い本体はこの 2 つを黙って捨てるので、ACP の拡張機能の書き込みの結果が Vim と揃わない。
// #198 で編集を禁止したバッファへの buffer/edit を誤りの応答（ErrorCodeReadOnly）で断るようにしたが、メッセージの JSON の形は変えていないので、バージョンは上げていない。
// 0.4.0 で、宣言ファイルの provides と、本体の操作を提供者へ問い合わせる provider/invoke を足した（ProviderInvokeParams。#203）。
// 古い本体は activation の空の宣言ファイルを誤りとして拒むので、provides だけで操作を引き受ける拡張機能（activation の無いもの）を読まない。
// activation を持つ拡張機能は読むが、provides の項目は未知の項目として無視し、その操作を呼ばない。
// #227 で宣言ファイルの contributes の各項目に description（Contribution.Description）を足したが、バージョンは上げていない。
// 省いてよい項目で、メッセージの形は変えておらず、古い本体は contributes の中の未知の項目を黙って捨てる（encoding/json）ので、
// 説明文の無い一覧になるだけで動きは変わらないためである。
// 0.5.0 で、宣言ファイルの contributes.statusline と、部品の中身を送る statusline/set を足した（StatusLineSetParams。#238）。
// 古い本体は statusline/set を知らない通知として捨て（ログに残すだけ）、contributes.statusline を未知の項目として捨てるので、部品は出ない。
// また、activation と provides の無い、部品だけを足す宣言ファイルを誤りとして拒む。
// 0.6.0 で、ポップアップと表示の部品のメソッド（popup/open、view/update、view/close、view/closed、view/select、input/change、view/key。
// view.go）と、宣言ファイルの contributes.popupKeys と、editor/open の peek を足した（#236）。
// 古い本体は popup/open に method not found で応答し、peek を黙って捨てて本物のファイルを開く。
// 0.7.0 で、ファイルでないバッファを作る buffer/create と、2 つのバッファの見比べを始める diff/start を足した（diffstart.go。#245）。
// 古い本体はどちらにも method not found で応答する。
// 0.8.0 で、端末多重化ソフトのペインに部品を開く pane/open を足した（PaneOpenParams。#244）。
// 古い本体は pane/open に method not found で応答するので、拡張機能は popup/open の PopupCenter に代えられる。
// 0.9.0 で、入力の行と一覧を組にした部品 finder（ViewFinder）、input/change と view/update の seq、view/select と view/key の input、
// view/select の selection、本体が持つ一覧を問い合わせる editor/list と、選んだキーとコマンドを実行させる editor/execute（list.go）、
// editor/state の workDir を足した（#246。FR-106、FR-107）。
// 古い本体は finder の popup/open を知らない部品として誤りで断り、editor/list と editor/execute に method not found で応答する。
// #248 が先に 0.8.0 でマージされたので、0.9.0 にした。
// #277 で宣言ファイルの起動の契機の種類 settingsFileType（ActivationSettingsFileType）を足したが、バージョンは上げていない。
// メッセージの形は変えておらず、古い本体は知らない契機を警告にして無視し、宣言ファイルを拒まない（Manifest.Validate）ためである。
// ただし古い本体では、この契機から作るファイルの種類の契機が無いので、利用者が設定で足したファイルの種類では拡張機能を起動しない。
// 0.10.0 で、pane/open の placement（分割したペインか中央のポップアップか）と closeOnSelect（中央のポップアップで選んだら閉じる）、
// 部品の cursor（カーソルを置く項目）、本体からの view/tabChange、宣言ファイルの contributes.popupKeys の action を足した
// （#286。FR-105、FR-147）。
// 古い本体は placement と closeOnSelect と cursor を黙って捨て、分割したペインに出してカーソルを先頭に置く。
// popupKeys の action も捨てて view/key を送る。view/tabChange は送らない。
// 0.11.0 で、一覧と木とファインダーの項目のプレビューに出すもの（ViewItem.Preview、ViewPreview）と、
// 部品がプレビューの欄を持つ印（ViewComponent.Preview）を足した（#288。FR-149）。
// 古い本体はどちらも黙って捨て、プレビューを出さずに今までの窓の幅で一覧だけを出す。
// 0.12.0 で、一覧と木とファインダーの項目が指すファイルかディレクトリ（ViewItem.File、ViewFile）を足した（#295。FR-152）。
// 本体はこれを持つ項目の前にファイルの種類のアイコンを付ける。古い本体は黙って捨て、アイコンを付けずに出す。
// 0.13.0 で、editor/list の一覧の種類 help（:help で引ける語。ListHelp）と、その語の :help を開かせる editor/execute の help を
// 足した（#308。FR-148）。古い本体は help の editor/list を知らない一覧として誤りで断り、help だけの editor/execute も誤りで断る。
// 0.14.0 で、時間のかかる処理の進み具合を知らせる progress/report（ProgressReportParams。progress.go）を足した（#340。FR-178）。
// 古い本体は知らない通知として捨てる（ログに残すだけ）ので、進み具合は出ない。
// 0.15.0 で、本体の設定の項目の値を拡張機能から変える option/set と、今の値を読む option/get を足した（option.go。#349。FR-190〜FR-192）。
// 古い本体はどちらにも method not found で応答する。
// 0.16.0 で、宣言ファイルの起動の契機の種類 startup（本体の起動で起動する。ActivationStartup）と、
// バッファの同期を受け取らない印 noBufferSync（Manifest.NoBufferSync）を足した（#367。FR-225、FR-229）。
// 古い本体は noBufferSync を未知の項目として無視し、バッファを送る。
// メッセージの形は変えていないが、settingsFileType（#277）と違って版を上げた。古い本体は startup を知らない契機として無視し、
// この契機だけを持つ拡張機能を一度も起動しない。startup と他の契機を併せて書いた拡張機能は、古い本体では他の契機で起動するので、
// initialize の apiVersion で、本体の起動では起動されないことを知れる。
// 0.17.0 で、窓の見えている行の範囲とカーソルの変化を知らせる window/change と、それを受け取る宣言ファイルの windowEvents と、
// 窓の上端に行を重ねて描く band/set を足した（window.go。#369。FR-235〜FR-239）。
// 古い本体は windowEvents を未知の項目として警告にして window/change を送らず、band/set を知らない通知として捨てる。
// 0.18.0 で、textObject/resolve の params の selection（ビジュアルモードの今の選択）と、result の replaceSelection（選択をその範囲で置き換える）を
// 足した（#373。FR-240）。古い本体は selection を送らず replaceSelection を捨てるので、tree-sitter の an と in を繰り返しても選択が外へ広がらない。
// 0.19.0 で、decoration/set の範囲に直に付ける色（Decoration.Attr、TextAttr）と、仮想テキストを出す virtualText/set
// （VirtualTextSetParams。virtualtext.go）を足した（#368。FR-230〜FR-234）。
// 古い本体は、Attr を付けた範囲のある decoration/set の通知の全体を誤りとして捨て（styles の項目を文字列として読めないため）、
// virtualText/set を知らない通知として捨てる。直に付ける色を使う拡張機能は、initialize の apiVersion を見てから付ける。
// 0.20.0 で、テキストの部品の末尾へ書き足す view/append（ViewAppendParams。view.go）を足した（#402。FR-272、FR-273）。
// 古い本体は view/append を知らない通知として捨てる。使う拡張機能は、要る版を VersionLess で apiVersion と比べる。
// 0.21.0 で、利用者に 1 行の文字列を聞く input/ask（InputAskParams、InputAskResult。message.go）を足した（#409。FR-275）。
// 古い本体は input/ask に method not found で応答する。使う拡張機能は、要る版を VersionLess で apiVersion と比べる。
// 0.22.0 で、書き足すテキストと入力の行を組にした部品の種類 console（ViewConsole）と、部品の input、prompt、history を足し、
// view/append を console の部品の出力にも当てるようにした（#413。FR-274）。古い本体は console を知らない部品の種類として、
// その popup/open と pane/open を誤りで断る。使う拡張機能は、要る版を VersionLess で apiVersion と比べる。
// 0.23.0 で、複数の子の部品を見出しの行で切り替える部品の種類 switcher（ViewSwitcher）と、部品の children、selected と、
// 子を指す view/update と view/append の child、子を指す知らせ（view/select、view/key、view/expand、input/change）の child、
// 利用者が子を切り替えた知らせ view/childSelect（ViewChildSelectParams）、宣言ファイルの popupKeys の children と、
// 子を切り替える action（PopupKeyChildNext など）を足した（#416。FR-269〜FR-271、FR-301）。
// 古い本体は switcher を知らない部品の種類として、その popup/open と pane/open を誤りで断り、popupKeys の children を読まずに
// どの部品でも効かせ、知らない action は警告にして view/key を送る。使う拡張機能は、要る版を VersionLess で apiVersion と比べる。
// 0.24.0 で、console の部品の入力の行の補完を引き受ける印（ViewComponent.Complete）と、その候補を問い合わせる console/complete
// （ConsoleCompleteParams、ConsoleCompleteResult。consolecomplete.go）を足した（#427。FR-297）。
// 古い本体は Complete を読まずに捨て、console/complete を送らない（補完の一覧を出さない）。
// 0.25.0 で、拡張機能が本体のレジスターへテキストを入れる register/set（RegisterSetParams。register.go）と、
// 利用者がキーの前に付けたレジスターの名前を知らせる view/key の Register（ViewKeyParams）を足した（#432。FR-315）。
// 古い本体は register/set に method not found で応答し、"a の後のキーを popupKeys に当てずに本体のキーとして解釈する。
// 使う拡張機能は、要る版を VersionLess で apiVersion と比べる。
const APIVersion = "0.25.0"
