<!-- AI エージェント向けの文書（vi-imitation --docs extension-types）。印の間は make docs が生成する。印の間を手で編集しない。 -->
# 拡張機能 API の型

拡張機能 API のメソッド（`vi-imitation --docs extensions`）の params と result の型と、そこから辿れる型である。
宣言ファイルの型は `vi-imitation --docs extensions` にある。
型は Go の書き方で書く（`[]T` は配列、`*T` は省ける値、`json.RawMessage` は任意の JSON）。
「省ける」は、書かなくてよい（空の値と同じ）ことを表す。
1 つの型だけを読むときは、`vi-imitation --docs extension-types <型の名前>` を打つ。

<!-- generated:types:begin -->
#### `CancelParams`

$/cancel の params である。
本体は、期限を超えたリクエストについて送る。
拡張機能が取り消しに応じなくても、本体は後から届いた結果を破棄する。

- `id`（Go では `ID`、`json.RawMessage`）: 取り消すリクエストの JSON-RPC の id である。
  id は数値と文字列のどちらでもよいので、受け取った形のまま持つ。

#### `BufferChangeParams`

buffer/change の params である。
本体は、バッファの変更（トランザクションの適用、アンドゥ、リドゥ）のたびに送る。

Edits の位置は、Transaction と同じ逐次の数え方で書く。
先頭の編集は FromVersion の時点のテキストの位置で、以後の編集は直前の編集までを適用した後のテキストの位置で数える。
拡張機能は、Edits を先頭から順に適用すると、ToVersion の時点のテキストを得る。

FromVersion が拡張機能の持つバージョン番号と一致しない場合、拡張機能は差分が欠けたと判断し、
Edits を適用せずに buffer/resync を送る。
一致した場合は、Edits を適用した後に ToVersion を自分の番号にする。
ToVersion は FromVersion より大きい。拡張機能は差が 1 であることを前提にしない。

- `buffer`（`BufferID`）
- `fromVersion`（`Version`）: Edits を適用する前のバージョン番号である。
- `toVersion`（`Version`）: Edits を適用した後のバージョン番号である。
- `edits`（`[]Edit`）: 適用した編集の列である。位置は逐次の数え方で書く。

#### `BufferID`

JSON の整数である。

本体が開いているバッファを識別する番号である。
本体がバッファを開くときに割り当て、閉じるまで変えない。
閉じたバッファの番号を、別のバッファに使い回すかどうかは保証しない。


#### `Version`

JSON の整数である。

バッファのバージョン番号である。
バッファが変更されるたびに 1 増える。


#### `Edit`

1 つの範囲の置き換えである。
挿入は空の Range に Text を置く場合、削除は Text を空にする場合として表す。

- `range`（`Range`）: 置き換える範囲である。
- `text`（`string`）: 置き換えた後のテキストである。

#### `Range`

バッファの中の範囲である。
Start を含み、End を含まない。
Start と End が同じ位置なら、空の範囲（挿入の位置）を表す。

- `start`（`Position`）
- `end`（`Position`）

#### `Position`

バッファの中の位置である。
行の番号と、行の先頭からのバイト数の組で表し、どちらも 0 から数える。
テキストは UTF-8 で数え、LSP の UTF-16 の単位には変換しない。

- `line`（`int`）: 0 から数えた行の番号である。
- `byte`（`int`）: 行の先頭から数えたバイト数である。

#### `BufferCloseParams`

buffer/close の params である。
本体は、バッファを閉じたときのほか、送ったバッファに不正な UTF-8 が入ったとき（[BufferOpenParams]）にも送る。
拡張機能が editor/open で不正な UTF-8 を含むバッファを開いた場合は、その拡張機能へ応答より先に送る。
このため、拡張機能は持っていないバッファの buffer/close を受け取ることがあり、そのときは何もしなくてよい。

- `buffer`（`BufferID`）

#### `BufferDidSaveParams`

buffer/didSave の params である。
本体は、バッファをファイルへ書き出した後に、そのバッファを持っている稼働中の拡張機能すべてへ送る。

- `buffer`（`BufferID`）
- `version`（`Version`）: 書き出した時点のバージョン番号である。
- `path`（`string`）: 書き出したファイルの絶対パスである。

#### `BufferOpenParams`

buffer/open の params である。
本体は、拡張機能が稼働中になった時点で開いているバッファと、その後に開いたバッファについて送る。
再起動した拡張機能にも、開いているバッファの全文を送り直す。
buffer/resync を受け取ったときも、そのバッファについて送り直す。
拡張機能は、既に持っているバッファについて受け取った場合、持っている内容を Text で置き換える。

本体は、不正な UTF-8 を含むバッファを拡張機能へ送らない。
JSON の文字列は不正なバイトを表せず、送ると本体と拡張機能で行頭からのバイト数がずれるためである。
そうしたバッファには buffer/open も buffer/change も届かず、送った後に不正なバイトが入ったら buffer/close が届く。
不正なバイトが無くなったら、新しい全文の buffer/open が届く。
このため Text と、buffer/change の Edit の Text は、常に正しい UTF-8 である。

- `buffer`（`BufferID`）
- `path`（`string`、省ける）: バッファに対応するファイルの絶対パスである。ファイルに対応しないバッファでは空。
  本体は、利用者が相対パスで開いたファイルを、本体の作業ディレクトリをもとに絶対パスにして渡す。
  シンボリックリンクは解決しない。
  拡張機能は、パスの文字列の一致で自分の複製を探せる。
- `fileType`（`string`、省ける）: ファイルの種類（"markdown" など）である。決まらなければ空。
- `version`（`Version`）: Text の時点のバージョン番号である。
- `text`（`string`）: バッファの全文である。
  位置を数えるもとのテキスト（行を "\n" でつないだもの）で、末尾の改行の有無は含めない。
- `finalNewline`（`*bool`、省ける）: 本体がこのバッファをファイルへ書き出したときに、最後の行の後に改行が付くかどうかである。
  ファイルの末尾の改行の有無と、末尾の改行を補う設定（Vim の 'fixendofline'）から本体が決める。
  行の無いバッファ（Vim の --No lines in buffer--。Text は空）は空のファイルとして書き出すので false にする。
  値が変わると（全ての行を消した、行の無いバッファに入力した、など）、本体は buffer/open を送り直す。
  LSP の拡張機能は、true のときだけ、言語サーバーへ送る全文の末尾に改行を足す。
  省いた場合（nil）は、改行が付くものとして扱う。本体は常に送る。値は HasFinalNewline で読む。

#### `BufferWillSaveParams`

buffer/willSave の params である。
本体は、バッファを保存する直前に、そのバッファを持っている稼働中の拡張機能のうち、
宣言ファイルの saveHook が true のものへ、1 つずつ順に送る。
未起動の拡張機能は起動しない。

- `buffer`（`BufferID`）
- `version`（`Version`）: 問い合わせた時点のバージョン番号である。
  前の拡張機能の編集を適用した後なら、適用した後の番号である。

#### `BufferWillSaveResult`

buffer/willSave の result である。

- `transaction`（`*Transaction`、省ける）: 保存する前に適用する編集（整形など）である。編集しないなら省く。
  本体は、BaseVersion が現在のバージョン番号と一致した場合だけ適用する。
  期限（仮に 1000 ms）を超えた応答は待たずに次へ進み、後から届いた応答は捨てる。

#### `Transaction`

1 回の操作でまとめて適用する編集の列と、元にしたバージョン番号である。

拡張機能 API の契約として、次のとおりに扱う。

  - 本体は、BaseVersion が現在のバージョン番号と一致した場合だけ Edits を適用し、一致しなければ全体を破棄する。
    本体が Edits を新しい内容に合わせて書き換えることはしない
  - Edits は先頭から順に適用する。各編集の範囲は、同じ列の中で直前の編集までを適用した後のテキストの位置で数える
    （逐次の数え方。LSP の textDocument/didChange の contentChanges と同じ）。
    先頭の編集は BaseVersion の時点のテキストの位置で数える。
    このため、範囲の重なりという状態は生じない
  - いずれかの編集の範囲がその時点のテキストの外を指すか、UTF-8 の文字の途中を指すか、終わりが始まりより前にある場合、
    本体はトランザクション全体を破棄する。一部の編集だけを適用することは無い
  - Edits が 1 つ以上なら、適用に成功するとその数に関わらずバージョン番号は 1 増える。
    Edits が空なら、テキストを何も変えず、バージョン番号も増えない（buffer/edit では result の Applied が false になる）

buffer/change の Edits も同じ数え方で書く（BufferChangeParams）。
このため、本体は適用したトランザクションの Edits を、そのまま他の拡張機能へ送れる。

- `baseVersion`（`Version`）: 編集を計算するもとにしたバッファのバージョン番号である。
- `edits`（`[]Edit`）: 適用する編集の列である。位置は逐次の数え方で書く。

#### `CommandExecuteParams`

command/execute の params である。
本体は、利用者がコマンドを呼んだときに送る。
応答を待つかどうかと期限は、キー入力ごとの問い合わせと同じに扱ってよい。

- `buffer`（`BufferID`）
- `version`（`Version`）: 問い合わせた時点のバージョン番号である。
- `id`（Go では `ID`、`string`）: 宣言ファイルの contributes.commands に書いたコマンドの ID である。
- `cursor`（`Position`）: 問い合わせた時点のカーソル位置である。
- `count`（`int`）: 回数である。0 は省略を表す。
- `args`（`json.RawMessage`、省ける）: コマンドの引数である。無ければ省く。
  コマンドラインから引数を付けて呼んだとき（:acp.prompt 説明して）は {"text":"説明して"}、
  キーで呼んで後に 1 文字を取ったときは {"char":"x"} である（`vi-imitation --docs extensions キー入力ごとの問い合わせ`）。
- `sync`（`bool`、省ける）: 本体がマクロかドットリピートを再生していることを表す。
  true なら、拡張機能は結果がそろうまで応答を返さず、編集を応答の Transaction で、カーソルの移動を応答の Open で返す。
  後から buffer/edit で届けた編集は、再生の次の操作でバージョン番号が進んだ後に届いて破棄されるためである。
  再生の結果を変えない表示（popup/show、output/append、message/show）は、true でも後から届けてよい。
- `withheld`（`bool`、省ける）: 本体が Buffer を不正な UTF-8 のために拡張機能へ送っていない（[BufferOpenParams]）ことを表す。
  拡張機能は Buffer の複製を持っていないが、バッファを使わないコマンド（デバッグの再開など）は実行できる。
  バッファを使うコマンドは、OK を false で返す。本体は、送っていないために使えないことを利用者へ知らせる。
  ext/server は、true のとき、ハンドラーへ複製の代わりに nil を渡す。

#### `CommandExecuteResult`

command/execute の result である。

- `ok`（Go では `OK`、`bool`）: コマンドが成り立ったかどうかである。false なら操作は失敗し、本体はバッファを変えない。
- `transaction`（`*Transaction`、省ける）: コマンドの編集である。編集しないコマンドでは省く。
  本体は、BaseVersion が現在のバージョン番号と一致した場合だけ適用する。
  編集を buffer/edit でなく応答で返すのは、本体が応答を待っている間に buffer/edit を処理できないからである。
- `open`（`*EditorOpenParams`、省ける）: カーソルの移動である。形は editor/open の params と同じで、本体は Transaction を適用した後に移動する。
  主に params の Sync が true のときに、後から editor/open で送る代わりに使う。移動しなければ省く。
  Open.Version を指定するなら、Transaction を適用した後のバージョン番号で書く。
  本体は、Open が EditorOpenParams.Validate を通らなければ、操作を失敗として扱う。

#### `EditorOpenParams`

editor/open の params である。
ファイルかバッファを開き、カーソルを指定の位置へ動かす。
command/execute の result の Open も同じ形である。

本体は、既に開いているファイルなら新しく開かずにそのバッファを使う。
要求した拡張機能がそのバッファの複製を持っていなければ、応答より先にその拡張機能へ buffer/open を送る。
拡張機能の側では、応答を受け取った時点で複製がまだできていないことがあるので、
ext/server の WaitBuffer で、応答のバージョン番号以降の複製ができるまで待ってから読む。

- `path`（`string`、省ける）: 開くファイルの絶対パスである。Buffer とどちらか一方を指定する。
- `buffer`（`*BufferID`、省ける）: 開いているバッファの番号である。Path とどちらか一方を指定する。
- `position`（`*Position`、省ける）: カーソルの移動先である。省くとカーソルを動かさない。
- `version`（`*Version`、省ける）: Position を計算するもとにしたバージョン番号である。
  指定すると、現在のバージョン番号と一致したときだけカーソルを動かす（バッファは一致しなくても開く）。
  まだ開いていないファイルでは使わない。
- `focus`（`bool`、省ける）: true ならそのバッファをカーソルのあるバッファにする。false なら開くだけでカーソルを移さない。
- `peek`（`bool`、省ける）: 開かずに「ポップアップで見る」ことを表す。
  本体は、Path か Buffer の Position の周りの行の写しを、編集を禁止したファイルでないバッファとして、
  問い合わせを始めた client のカーソルの近くのポップアップに出す。Position は省けない。Focus は読まない。
  写しのポップアップを :tabopen でタブへ広げると、本物のファイルを開いて Position へ移る。
  応答の Buffer は写しのバッファの番号、Moved は false である。

#### `InitializeParams`

initialize の params である。

- `apiVersion`（Go では `APIVersion`、`string`）: 本体が実装している拡張機能 API のバージョンである。
- `settings`（`json.RawMessage`、省ける）: 利用者の設定ファイルのうち、この拡張機能に向けた部分である。
  形は拡張機能ごとに決まるので、本体は解釈せずに JSON のまま渡す。設定が無ければ省く。
- `noBufferSync`（`bool`、省ける）: 宣言ファイルの noBufferSync（Manifest.NoBufferSync）のとおり、本体がこの拡張機能へバッファの同期を送らないことを表す
  （API 0.16.0 で足した）。ext/server は、true なら WaitBuffer をすぐ誤りで返す（待っても buffer/open が届かないため）。

#### `InitializeResult`

initialize の result である。

- `apiVersion`（Go では `APIVersion`、`string`）: 拡張機能が前提にした拡張機能 API のバージョンである。
  本体のバージョンと食い違った場合の扱いは未決である。

#### `InputChangeParams`

input/change の params である。

- `view`（`ViewID`）
- `text`（`string`）: 変わった後の入力の行の中身である。
- `seq`（`uint64`、省ける）: そのポップアップの入力の行が変わるたびに 1 から 1 ずつ増やす番号である（API 0.9.0 で足した）。
  ViewFinder（finder） の拡張機能は、この入力への一覧を view/update の Seq に同じ値を入れて返す。
  本体は、前の入力への一覧が届いていなくても次の input/change を送るので、拡張機能は古い入力への計算を捨ててよい。

#### `ViewID`

JSON の整数である。

本体が開いたポップアップに付けた番号である。本体の中で一意で、使い回さない。0 は使わない。


#### `MotionResolveParams`

motion/resolve の params である。
キー入力ごとに結果が要る問い合わせで、本体は期限付きで応答を待つ。

- `buffer`（`BufferID`）
- `version`（`Version`）: 問い合わせた時点のバージョン番号である。
- `id`（Go では `ID`、`string`）: 宣言ファイルの contributes.motions に書いたモーションの ID である。
- `cursor`（`Position`）: 問い合わせた時点のカーソル位置である。
- `count`（`int`）: 回数である。0 は省略を表す。
- `args`（`json.RawMessage`、省ける）: 操作の引数である。無ければ省く。

#### `MotionResolveResult`

motion/resolve の result である。

- `ok`（Go では `OK`、`bool`）: 移動先が見つかったかどうかである。false なら操作は失敗する。
- `version`（`Version`）: 計算のもとにしたバージョン番号である。
  本体は、現在のバージョン番号と一致しなければ結果を破棄する。
- `position`（`Position`）: 移動先の位置である。
- `linewise`（`bool`、省ける）: オペレーターと組み合わせたときに行単位で扱うかどうかである（Vim の j や G）。
  false（省略を含む）なら文字単位で扱う。
- `inclusive`（`bool`、省ける）: 文字単位で扱うときに、移動先の文字を範囲に含めるかどうかである（Vim の e や f）。
  false（省略を含む）なら含めない（exclusive）。Linewise が true なら読まない。

#### `OperatorApplyParams`

operator/apply の params である。
キー入力ごとに結果が要る問い合わせで、本体は期限付きで応答を待つ。

- `buffer`（`BufferID`）
- `version`（`Version`）: 問い合わせた時点のバージョン番号である。
- `id`（Go では `ID`、`string`）: 宣言ファイルの contributes.operators に書いたオペレーターの ID である。
- `range`（`Range`）: オペレーターを適用する範囲である。
- `count`（`int`）: 回数である。0 は省略を表す。
- `args`（`json.RawMessage`、省ける）: 操作の引数である。無ければ省く。

#### `OperatorApplyResult`

operator/apply の result である。

- `ok`（Go では `OK`、`bool`）: オペレーターが編集を返したかどうかである。false なら操作は失敗し、本体はバッファを変えない。
- `transaction`（`Transaction`）: オペレーターの編集である。
  本体は、BaseVersion が現在のバージョン番号と一致した場合だけ適用する。

#### `PingParams`

ping の params である。中身は無い。

項目は無い（`{}`）。

#### `PingResult`

ping の result である。中身は無い。

項目は無い（`{}`）。

#### `ProviderInvokeParams`

provider/invoke の params である。
項目の意味は、Operation と Range を除いて CommandExecuteParams と同じである。

- `operation`（`Operation`）: 問い合わせる本体の操作である。提供者が provides で名乗ったものに限る。
- `buffer`（`BufferID`）
- `version`（`Version`）: 問い合わせた時点のバージョン番号である。
- `cursor`（`Position`）: 問い合わせた時点のカーソル位置である。
- `range`（`*Range`、省ける）: 範囲を取る操作（gq{モーション} の OpFormat（format））の範囲である。範囲の無い呼び出しでは省く。
- `count`（`int`）: 回数である。0 は省略を表す。
- `args`（`json.RawMessage`、省ける）: 操作の引数である。無ければ省く。形は `vi-imitation --docs extensions Operation` にある（{"peek":true}、{"text":"..."}）。
- `sync`（`bool`、省ける）: CommandExecuteParams.Sync と同じく、本体がマクロかドットリピートを再生していることを表す。
- `withheld`（`bool`、省ける）: CommandExecuteParams.Withheld と同じく、本体が Buffer を拡張機能へ送っていないことを表す。
  デバッグの続行のように、バッファを使わない操作はそのまま行える。

#### `ProviderInvokeResult`

Go の型は `CommandExecuteResult` である。

provider/invoke の result である。形と扱いは command/execute の result と同じである。


#### `ShutdownParams`

shutdown の params である。中身は無い。

拡張機能は、shutdown を受け取ったら、自分が起動した子プロセス（言語サーバー、デバッグアダプター、デバッグ対象など）を
正しい手順（LSP の shutdown と exit、DAP の disconnect など）で終わらせてから、自分も終了する。
本体は shutdown を送った後、その拡張機能へ問い合わせと生存確認を送らない。
期限（本体の設定。仮の値）までに終わらなければ、本体はプロセスグループへ SIGTERM を送り、
さらに期限を過ぎたら SIGKILL を送る。
拡張機能は、終わるまでの間に本体へ通知を送ってよいが、本体がそれを処理する保証は無い。

項目は無い（`{}`）。

#### `TextObjectResolveParams`

textObject/resolve の params である。
キー入力ごとに結果が要る問い合わせで、本体は期限付きで応答を待つ。

- `buffer`（`BufferID`）
- `version`（`Version`）: 問い合わせた時点のバージョン番号である。
- `id`（Go では `ID`、`string`）: 宣言ファイルの contributes.textObjects に書いたテキストオブジェクトの ID である。
- `cursor`（`Position`）: 問い合わせた時点のカーソル位置である。
- `count`（`int`）: 回数である。0 は省略を表す。
- `args`（`json.RawMessage`、省ける）: 操作の引数である。無ければ省く。

#### `TextObjectResolveResult`

textObject/resolve の result である。

- `ok`（Go では `OK`、`bool`）: 範囲が見つかったかどうかである。false なら操作は失敗する。
- `version`（`Version`）: 計算のもとにしたバージョン番号である。
  本体は、現在のバージョン番号と一致しなければ結果を破棄する。
- `range`（`Range`）: テキストオブジェクトの範囲である。
- `linewise`（`bool`、省ける）: 範囲を行単位で扱うかどうかである（Vim の ip）。false（省略を含む）なら文字単位で扱う。

#### `ViewClosedParams`

view/closed の params である。

- `view`（`ViewID`）

#### `ViewExpandParams`

view/expand の params である。
木の子を持つ項目（Children か HasChildren のある項目）を、利用者が <CR> で開いたか閉じたかを知らせる。

- `view`（`ViewID`）
- `item`（`string`）: 開閉した項目の ID である。
- `expanded`（`bool`）: 開いたなら true、閉じたなら false である。

#### `ViewKeyParams`

view/key の params である。

- `view`（`ViewID`）
- `id`（Go では `ID`、`string`）: 打ったキーの contributes.popupKeys の項目の ID である。
- `item`（`string`、省ける）: 一覧、木、ViewFinder（finder） ではカーソルの行の項目の ID、入力の行では中身、テキストでは空である。
- `input`（`string`、省ける）: ViewFinder（finder） の入力の行の中身である。他の部品では省く。

#### `ViewSelectParams`

view/select の params である。

- `view`（`ViewID`）
- `item`（`string`）: 選んだ項目の ID（ViewList（list）、ViewTree（tree）、ViewFinder（finder））か、入力の行の中身（ViewInput（input））である。
  ViewFinder（finder） で一覧が空なら空である。
- `input`（`string`、省ける）: ViewFinder（finder） の入力の行の中身である。他の部品では省く。
- `selection`（`uint64`、省ける）: ViewList（list）、ViewTree（tree）、ViewFinder（finder） の項目の選択に本体が付ける番号である（1 から。API 0.9.0 で足した）。
  拡張機能は、この選択への応答として、同じ番号を付けた editor/execute（EditorExecuteParams.Selection）を 1 回だけ送れる。
  ViewInput（input） では省く。

#### `ViewTabChangeParams`

view/tabChange の params である（API 0.10.0 で足した）。
本体は、pane/open で分割したペインに出した部品の持ち主へ、そのペインを開いた元の client のカーソルのあるタブ
（中央のポップアップにフォーカスがあれば、それを開いたときのタブ）が替わるたびに、替わった後のタブを知らせる。
ペインを開いた時点のタブは知らせない（拡張機能は editor/state で知る）。
ファイラが、出しっぱなしにしたペインの木を今のファイルへ追従させる（nvim の neo-tree の follow_current_file）のに使う。
中央のポップアップ（ペインに代えて出したものを含む）では送らない。開いている間はそれだけを操作するので、タブが替わらないためである。

- `view`（`ViewID`）
- `buffer`（`BufferID`）: 替わった後のタブのバッファの番号である。
- `path`（`string`、省ける）: そのバッファのファイルの絶対パスである。ファイルでないバッファでは空である。

#### `WindowChangeParams`

window/change の params である。

- `window`（`WindowID`）: 窓の識別子である。
- `closed`（`bool`、省ける）: 窓が無くなった（client が終わった）ことを表す。true なら、ほかの項目は読まない。
- `buffer`（`BufferID`、省ける）: 窓に出しているバッファ（その client のカーソルのあるタブ。フォーカスのあるポップアップを開いていれば、
  開いたときのタブ）の番号である。拡張機能へ送っていないバッファ（不正な UTF-8 を含むもの）のこともある。
- `version`（`Version`、省ける）: 送った時点のバッファのバージョン番号である。band/set の Version にそのまま書くと、
  本体は、帯を押したときに、帯を作ったときから本文が変わっていないかを確かめられる（BandSetParams.Version）。
- `top`（`int`）: 表示領域の最初の行（0 から数える。Vim の line('w0') - 1）である。
  行を折り返して描くとき、最初の行を途中から見せていることがある。
- `bottom`（`int`）: 直前の描画で表示領域に最後まで描いた行の次の行である（Vim の w_botline。0 から数える）。
  見えている行は Top から Bottom の前の行までで、最後に途中まで描いた行があればそれも見えている。
- `rows`（`int`）: Rows と Columns は、表示領域の行数と、本文の列数（印の列と行番号の列を除く）である。
- `columns`（`int`）
- `left`（`int`）: 行を折り返さない窓の、表示領域の左の端に当たる表示の列（横のスクロールの位置。0 から数える。Vim の leftcol）である。
  行を折り返す窓では 0 である。窓の列で位置を決める拡張機能（ブロックの罫線など）が、行の表示の列から引いて窓の列にする。
  横のスクロール（ホイールの横の送り、カーソルが左右の端から出たときの送り）だけで変わったときも知らせる。
- `cursor`（`Position`）: 窓のバッファのカーソル位置である。
- `mode`（`Mode`、省ける）: 窓の client のモードである（ポップアップにフォーカスがあれば、ポップアップのバッファのモード）。
- `diff`（`bool`、省ける）: 窓のバッファが見比べ（diff/start、:diffsplit）の片側であることを表す。
- `seq`（`uint64`）: 知らせの通し番号である。本体が window/change を送るたびに 1 から増やす（窓と拡張機能をまたいで 1 つの数え方）。
  拡張機能は、この知らせを元に作った帯の BandSetParams.Seq に書く。

#### `WindowID`

JSON の文字列である。

本体が窓ごとに決める識別子である。拡張機能は値の形に頼らず、window/change で受け取った値をそのまま band/set に書く。

今の本体の窓は、編集の client の画面のバッファの表示領域（client ごとに 1 つ）で、識別子は "client/<client の番号>" である。
端末多重化ソフトで分割したペインの client と、見比べ（diff/start）のために分割したペインの client は別の client なので、窓も別である。
ポップアップの中のバッファ（ホバー、ポップアップで見る、ファインダー）と部品の client の画面は、今は窓として知らせない。
後で知らせるときは、"client/" で始まらない識別子にする。


#### `Mode`

JSON の文字列である。

本体のモードの名前である。

- `"normal"`（ModeNormal）
- `"insert"`（ModeInsert）
- `"visual"`（ModeVisual）
- `"visualLine"`（ModeVisualLine）
- `"visualBlock"`（ModeVisualBlock）
- `"commandLine"`（ModeCommandLine）

#### `BandSetParams`

band/set の params である。

- `window`（`WindowID`）: 帯を描く窓（window/change の Window）である。
- `buffer`（`BufferID`）: 帯を作ったときの窓のバッファである。窓のバッファが替わったら、本体は描かない。
- `version`（`Version`）: 帯を作ったときのバッファのバージョン番号である。本体は、今のバージョン番号がこれと違う間も帯を描くが、
  帯を押してもその行へ移らない（下の本文を押したことにする。nvim-treesitter-context の get_source_line と同じ）。
- `seq`（`uint64`、省ける）: 帯を作る元にした window/change の Seq である。本体は、同じ拡張機能と窓の組の今の帯の Seq より小さい帯を、
  古い知らせから作ったものとして捨てる（別の goroutine で作った帯が、作り終える順で入れ替わって届いても、新しい帯を上書きしない）。
  0（省く）なら比べずに置き換える。
- `lines`（`[]BandLine`）: 帯の行を上から並べたものである。空なら帯を消す。MaxBandLines（64） を超えられない。

#### `BandLine`

帯の 1 行である。

- `text`（`string`）: 行の中身の文字列である。改行を含められない。本体は折り返さず、窓の横のスクロールの位置に合わせて、
  本文と同じ列から描く（行を折り返して描く窓では、左の端から描く）。タブ文字は窓のバッファのタブ幅（tab.width）で広げる。
- `line`（`*int`、省ける）: 行に当たるバッファの行（0 から数える）である。本体は、行番号の列にこの行の番号を描き、
  帯のこの行を押したらこの行へ移る。省くと（nil）、行番号を描かず、押しても下の本文を押したことにする。
- `highlights`（`[]BandHighlight`、省ける）: 行の中の範囲ごとの色である。後の範囲ほど上に重ねる。

#### `BandHighlight`

帯の行の中の、色を付ける範囲である。

- `start`（`int`）: Start と End は、Text の中のバイトの範囲 [Start, End) である。
- `end`（`int`）
- `style`（`string`）: 色の名前（本体の色の名前のどれか。decoration/set の style と同じ）である。
  本体は、帯の色（band）の上に、この名前の色を重ねる。本体の知らない名前なら、色を重ねない。

#### `BufferCreateParams`

buffer/create の params である。

作るバッファは、ファイルを持たない（保存の先が無い）。
FR-88 のとおり、既定では編集を禁止した状態で始める。Writable を true にしたときだけ編集できる状態で始める。

- `title`（`string`）: バッファの名前である。タブの行、ステータス行、:ls にパスの代わりに出す。空にはできない。
- `text`（`string`）: バッファの中身である。位置を数えるもとのテキストで、末尾の改行を含めない。
- `fileType`（`string`、省ける）: ファイルの種類である（構文の色付けなどに使う）。省けば種類の無いバッファにする。
- `writable`（`bool`、省ける）: true なら編集できる状態で始めることを表す。省けば編集を禁止した状態で始める。

#### `BufferCreateResult`

buffer/create の result である。

- `buffer`（`BufferID`）: 作ったバッファの番号である。
- `version`（`Version`）: 作った時点のバージョン番号である。

#### `BufferDisposeParams`

buffer/dispose の params である。

- `buffer`（`BufferID`）: buffer/create で作ったバッファの番号である。

#### `BufferDisposeResult`

buffer/dispose の result である。項目は無い。

項目は無い（`{}`）。

#### `BufferEditParams`

buffer/edit の params である。
補完の確定や整形など、拡張機能の側から始める編集に使う。

NoLines と EndOfLine は、テキストの編集では表せない、書き出したときの末尾の改行を決める状態を変える。
外から書き換えたファイルを読み直したのと同じ状態にするために使う（ACP の fs/write_text_file）。
本体は、BaseVersion が現在のバージョン番号と一致した場合だけ、Edits を適用した後にこの 2 つを反映する。
Edits が空でも反映する。そのときバージョン番号は変わらず、result の Applied は false になる。
反映して FinalNewline（BufferOpenParams）が変わったら、本体は buffer/open を送り直す。

- `buffer`（`BufferID`）
- `transaction`（`Transaction`）
- `noLines`（`*bool`、省ける）: Edits を適用した後のバッファを、行の無いバッファ（Vim の --No lines in buffer--）にするかどうかである。
  true なら行の無いバッファにし、書き出すと空のファイルになる。適用した後のテキストが空でなければ、true は無視する。
  false なら、テキストが空でも行の無いバッファでなくする（空の行 1 つとして書き出す）。
  行の無いバッファの記録はアンドゥの履歴の位置に付き、アンドゥとリドゥでその位置へ戻ると、また行の無いバッファになる。
  省いた場合（nil）は変えない。
- `endOfLine`（`*bool`、省ける）: 読み込んだファイルの最後の行の後に改行があったものとして扱うかどうか（Vim の 'endofline'）である。
  本体は、これが true か、末尾の改行を補う設定（Vim の 'fixendofline'）が有効なら、書き出すときに最後の行の後に改行を付ける。
  アンドゥでは戻らない。省いた場合（nil）は変えない。

#### `BufferEditResult`

buffer/edit の result である。
編集を禁止したバッファへの要求では result を返さず、ErrorCodeReadOnly（-32001） の誤りの応答を返す。

- `applied`（`bool`）: 本体がトランザクションを適用したかどうかである。
  BaseVersion が現在のバージョン番号と一致しなければ false。
- `version`（`Version`）: 応答の時点のバージョン番号である。

#### `BufferResyncParams`

buffer/resync の params である。
buffer/change の FromVersion が自分の番号と一致しなかった拡張機能が送る。
本体は、そのバッファの現在の全文を buffer/open で送り直す。
本体が既に閉じたバッファについて受け取った場合は、何もしない。

- `buffer`（`BufferID`）
- `version`（`Version`）: 拡張機能が持っているバージョン番号である。本体はログに記録するだけで、送り直しの判断には使わない。

#### `BufferSaveParams`

buffer/save の params である。
buffer/save による保存も、利用者の保存と同じ保存の流れ（buffer/willSave から buffer/didSave まで）を通る。

- `buffer`（`BufferID`）
- `version`（`*Version`、省ける）: Version を指定すると、現在のバージョン番号と一致したときだけ書き出す。
- `skipHooks`（`bool`、省ける）: true なら保存の流れの buffer/willSave を飛ばし、どの拡張機能にも問い合わせずに書き出す。
  ACP の拡張機能が、エージェントの書いた内容をそのまま書き出すために使う。

#### `BufferSaveResult`

buffer/save の result である。

- `saved`（`bool`）: 書き出したかどうかである。
- `version`（`Version`）: 応答の時点のバージョン番号である。

#### `BufferUpdateParams`

buffer/update の params である。

- `buffer`（`BufferID`）: buffer/create で作ったバッファの番号である。
- `text`（`string`）: 新しい中身である。末尾の改行を含めない。

#### `BufferUpdateResult`

buffer/update の result である。

- `version`（`Version`）: 置き換えた後のバージョン番号である。中身が同じなら変わらない。

#### `ChoiceAskParams`

choice/ask の params である。
本体は一度に 1 つの問いだけを表示し、表示中に届いた問いは順に待たせる。
問いを出した拡張機能が再起動した場合、その拡張機能の問いを取り下げる。

- `title`（`string`）: 問いの文である。
- `choices`（`[]Choice`）

#### `Choice`

choice/ask の 1 つの選択肢である。

- `id`（Go では `ID`、`string`）: 選択肢の ID である。問いの中で一意にする。
- `label`（`string`）: 利用者に示す文である。

#### `ChoiceAskResult`

choice/ask の result である。

- `selected`（`string`、省ける）: 選んだ選択肢の ID である。取り消した場合は空。
- `cancelled`（`bool`、省ける）: 利用者が選ばずに取り消したかどうかである。

#### `DecorationSetParams`

decoration/set の params である。
本体は、受け取り済みの一覧で描画し、描画のたびに拡張機能へ問い合わせない。

一覧は、送った拡張機能とバッファの組ごとに持つ。
本体は、同じ拡張機能から同じバッファについて前に受け取った一覧だけを置き換え、
他の拡張機能が同じバッファに付けた一覧は変えない。
空の一覧を送ると、その拡張機能がそのバッファに付けた色を消す。

JSON では、範囲の一覧を範囲ごとのオブジェクトではなく、整数の列と色の種類の名前の表で表す（MarshalJSON）。
大きなファイルでも本体の受信の上限に収めるためである。Go の値としては Decorations の一覧のまま扱える。

JSON の形は、下の項目の並びではなく、この説明のとおりである（MarshalJSON か UnmarshalJSON を持つ）。

JSON の形:

decoration/set の JSON の形。

範囲の一覧は、範囲ごとのオブジェクトの配列ではなく、整数の列 data と色の種類の名前の表 styles で表す。
LSP の semantic tokens と同じく、範囲の開始を直前の範囲の開始からの差で書き、1 つの範囲に 5 つの整数を使う。
オブジェクトの配列では 1 つの範囲が 90 バイトほどになり、約 6 MB の JSON の一覧が 74 MB になって本体の受信の上限（64 MiB）を超えた。

	{"buffer":1,"version":3,"styles":["keyword","string"],"data":[0,0,0,7,0, 1,4,0,5,1]}

data の i 番目の範囲（0 から数える）は data[5i:5i+5] の次の 5 つである。直前の範囲の開始は、i が 0 なら行 0 のバイト 0 とする。

	deltaLine  開始の行 - 直前の範囲の開始の行
	deltaStart deltaLine が 0 なら 開始のバイト - 直前の範囲の開始のバイト、そうでなければ 開始のバイト
	lineSpan   終わりの行 - 開始の行
	end        lineSpan が 0 なら 終わりのバイト - 開始のバイト（範囲の長さ）、そうでなければ 終わりのバイト
	style      styles の添字

整数は符号付きで、範囲の順序と値に制約は無い（並んでいなくても、行をまたいでも、終わりが開始より前でも表せる）。
送る側の一覧（DecorationSetParams.Decorations）は、順序を含めてそのまま読み戻せる。
本体は、重なった範囲を一覧の後ろのものほど上に描くので、順序には意味がある。

- `buffer`（`BufferID`）
- `version`（`Version`）: 一覧を計算するもとにしたバージョン番号である。

#### `Decoration`

色を付ける 1 つの範囲である。

- `range`（`Range`）
- `style`（`string`）: 色の種類の名前（"keyword" など）である。
  名前から色への対応は本体が持ち、利用者は設定の colors で名前ごとに変えられる。
  構文の色の名前は comment、keyword、string、number、constant、function、type、variable、operator、punctuation の 10 個で、
  前に "syntax." を付けない。構文の色の他に、本体の色の名前（`vi-imitation --docs usage 見た目の設定`の表。診断の diagnostic.error、
  画面の部品の statusline など）のどれを使ってもよい。本体が知らない名前の範囲には色を付けない。

#### `DiagnosticListParams`

diagnostic/list の params である。

- `buffer`（`*BufferID`、省ける）: 対象のバッファの番号である。省くと開いているバッファすべてを対象にする。

#### `DiagnosticListResult`

diagnostic/list の result である。

- `entries`（`[]DiagnosticEntry`）: バッファと、診断を送った拡張機能の組ごとの一覧である。

#### `DiagnosticEntry`

1 つのバッファについて、1 つの拡張機能から受け取った診断の一覧である。

- `buffer`（`BufferID`）
- `extension`（`string`）: 診断を送った拡張機能の ID である。
- `version`（`Version`）: そのバッファの現在のバージョン番号である。
  Diagnostics の位置は、本体が編集に合わせてずらした位置で、この番号の時点のテキストを指す。
- `diagnostics`（`[]Diagnostic`）: 診断の一覧である。

#### `Diagnostic`

1 つの診断である。

- `range`（`Range`）: 診断の範囲である。
- `severity`（`DiagnosticSeverity`）
- `message`（`string`）: 本文である。
- `source`（`string`、省ける）: 出どころ（"gopls" など）である。無ければ省く。
- `code`（`string`、省ける）: 診断の種類を表す文字列である。無ければ省く。

#### `DiagnosticSeverity`

JSON の文字列である。

診断の重要度である。

- `"error"`（SeverityError）
- `"warning"`（SeverityWarning）
- `"info"`（SeverityInfo）
- `"hint"`（SeverityHint）

#### `DiagnosticSetParams`

diagnostic/set の params である。

本体は、送った拡張機能とバッファの組ごとに診断の一覧を持ち、同じ組の次の一覧で置き換える。
空の一覧を送ると、その組の一覧を消す。
decoration/set と違い、Version が現在より古くても、本体が覚えている編集の範囲にあれば、その後の編集に合わせて位置をずらしてから持つ。
それより古いか、現在より新しい一覧は捨てる。

- `buffer`（`BufferID`）
- `version`（`Version`）: 一覧を計算するもとにしたバージョン番号である。
- `diagnostics`（`[]Diagnostic`）: このバッファについて、この拡張機能が持つ診断のすべてである。

#### `DiffStartParams`

diff/start の params である。

本体は、A と B の 2 つのバッファを、A を左（diff の A の側）にして見比べる。
2 つのバッファは別々の編集の client のタブでなければならない。

  - どちらも別々の client のタブなら、その場で見比べを始める
  - どちらも同じ client のタブなら、誤りにする（同じ client の 2 つのタブは同時に画面に出ない）
  - 片方だけがタブなら、そのタブの client のペインを端末多重化ソフトの層で分割し、新しい編集の client を起動して、
    もう片方のバッファをそのタブにしてから見比べを始める（:diffsplit と同じ）。応答は分割を頼んだ時点で返す
  - どちらもタブでなければ、誤りにする

端末多重化ソフトの外では、分割できないので誤りにする。
拡張機能は、editor/state の CanSplit で、分割できるかを buffer/create の前に確かめられる。
分割したペインの client を待っている間に届いた、分割の要る diff/start も誤りにする。

- `a`（`BufferID`）: 見比べの左の側のバッファの番号である。
- `b`（`BufferID`）: 見比べの右の側のバッファの番号である。

#### `DiffStartResult`

diff/start の result である。

- `split`（`bool`、省ける）: ペインを分割して新しい編集の client を起動したことを表す。
  true のとき、見比べは新しい client が繋いだ後に始まる（繋がなければ始まらない）。false なら応答の時点で始まっている。

#### `EditorExecuteParams`

editor/execute の params である。Keys、Command、Help のどれか 1 つを指定する。

呼べる条件（満たさなければ、本体は実行せずに JSON-RPC の誤りで応答し、利用者のメッセージの行にも理由を出す）:

  - その拡張機能が popup/open で開いた一覧、木、ファインダーで、利用者が項目を選んだ（view/select）ことへの応答であること。
    view/select の Selection と同じ値を Selection に入れる。1 回の選択に 1 回だけで、後の選択があったとき、選んだ client が
    次のキーを打ったとき、ポップアップを利用者か本体が閉じたとき（拡張機能の view/close を除く）は、その番号は使えない
  - 選んだ client が、ノーマルモードで入力の途中でないこと（操作の途中、キーマップの展開の途中、hit-enter の問い、フォーカスのある
    ポップアップ、外部のコマンドや再生中の拡張機能の結果の待ちが無いこと）。ポップアップは、送る前に view/close で閉じておく
  - Command は、コマンドの一覧（ListCommand）の Name か Short と完全に一致すること。範囲、引数、"|" は付けられない

本体は、選んだ client で、利用者が打ったのと同じく実行する。応答は実行の前に返す。
実行はマクロに記録しない（選んだ <CR> などが記録されている。キーの一覧で選んだキーと同じ扱い）。
打ったのと同じなので、ドットリピートの値（.）は実行した操作に替わる。

- `selection`（`uint64`）: この実行が応える view/select の Selection である。
- `keys`（`string`、省ける）: キーの一覧の項目の Keys（ListKey.Keys）である。本体はキーの一覧からその表記の行を引き直し、
  利用者のキーマップを通して打ったのと同じく実行する（設定の keymap が上書きした行なら、上書きした方が動く）。
  ノーマルモードの行でないもの（インサートモードだけのキー）と、回数やマクロのように操作の表に無いキーは、理由を出して実行しない。
- `command`（`string`、省ける）: コマンドの一覧の項目の Name（ListCommand.Name）である。本体は :Name<CR> を打ったのと同じく実行する。
  引数が要るコマンドは、打ったときと同じ誤りになる。
- `help`（`string`、省ける）: ヘルプの語の一覧の項目の Word（ListHelpWord.Word）である。本体は :help Word を打ったのと同じく、
  その語の説明の行を開いたヘルプを出す（API 0.13.0）。説明が見つからない語は、:help と同じ誤り（E149）を出す。

#### `EditorExecuteResult`

editor/execute の result である。受け付けたことだけを表す（実行の失敗はメッセージの行に出る）。

項目は無い（`{}`）。

#### `EditorListParams`

editor/list の params である。

- `list`（`EditorListKind`）

#### `EditorListKind`

JSON の文字列である。

editor/list で問い合わせる一覧の種類である。

- `"tabs"`（ListTabs）: すべての編集の client のタブである（EditorListResult.Tabs）。
- `"commands"`（ListCommands）: コマンドラインから呼べるコマンド（本体の Ex コマンドと拡張機能のコマンド）である（EditorListResult.Commands）。
- `"keys"`（ListKeys）: キーの一覧である（EditorListResult.Keys）。
- `"registers"`（ListRegisters）: レジスターと履歴（:registers の一覧）である（EditorListResult.Registers）。
- `"jumps"`（ListJumps）: 問い合わせを始めた client のジャンプリストである（EditorListResult.Jumps）。
- `"help"`（ListHelp）: :help で引ける語（キー、Ex コマンド、設定の名前、操作の ID）である（EditorListResult.Help。API 0.13.0）。

#### `EditorListResult`

editor/list の result である。問い合わせた List の欄だけを埋め、他の欄は省く。

- `tabs`（`[]ListTab`、省ける）
- `commands`（`[]ListCommand`、省ける）
- `keys`（`[]ListKey`、省ける）
- `registers`（`[]ListRegister`、省ける）
- `jumps`（`[]ListJump`、省ける）
- `help`（`[]ListHelpWord`、省ける）

#### `ListTab`

タブ 1 つである。並びは、client の古い順に、各 client のタブの並びの順である。

- `buffer`（`BufferID`）
- `path`（`string`、省ける）: ファイルの絶対パスである。ファイルでないバッファでは空。
- `name`（`string`）: タブの行に出す名前（作業ディレクトリからの相対パス、ファイルでないバッファの見出しなど）である。
- `client`（`uint64`）: そのタブを持つ編集の client の番号である。本体がメッセージに出す「client N」の N と同じで、
  server が接続ごとに割り当て、使い回さない（古い client が終わっても、残った client の番号は変わらない）。
- `own`（`bool`、省ける）: 問い合わせを始めた client のタブかどうかである。
- `current`（`bool`、省ける）: その client のカーソルのあるタブかどうかである。
- `modified`（`bool`、省ける）
- `readOnly`（`bool`、省ける）

#### `ListCommand`

コマンドラインから呼べるコマンド 1 つである。

- `name`（`string`）: 省略しない名前である（"write"、"lsp.rename" など）。
- `short`（`string`、省ける）: 本体の Ex コマンドの省略できる最短の形（"w"）である。拡張機能のコマンドでは空。
- `extension`（`string`、省ける）: 拡張機能のコマンドなら、その拡張機能の ID である。本体のコマンドでは空。
- `description`（`string`、省ける）: 宣言ファイルの説明文である。無ければ空。

#### `ListKey`

キーの一覧の 1 行である。

- `keys`（`string`）: キーの表記（"<Space>ff"）で、editor/execute の keys に渡すとその行を実行する。回数やレジスターのように Parser が先に読むキーでは、引数の名前を含む（"{回数}"）。
- `modes`（`string`）: キーが効くモードの文字（n、v、o、i、c）の列である。
- `source`（`string`）: 出どころ（"builtin"、"user"、"extension"）である。
- `extension`（`string`、省ける）: Source が "extension" のときの拡張機能の ID である。
- `description`（`string`、省ける）: 説明文である。利用者のキーマップでは展開先のキーの列（"→ ..."）である。
- `overridden`（`string`、省ける）: 利用者のキーマップがこの行の操作を上書きしているモードの文字の列である。上書きされていなければ空。

#### `ListRegister`

レジスターか履歴の 1 項目である。並びは :registers の一覧と同じである。

- `name`（`string`、省ける）: 名前付きのレジスターの名前（"a"、"+" など）である。履歴の項目では空。
- `index`（`int`、省ける）: 履歴の項目の、新しい順の番号（0 から）である。名前付きのレジスターでは 0。
- `text`（`string`）
- `linewise`（`bool`、省ける）

#### `ListJump`

ジャンプリストの 1 項目である。並びは古い順で、:jumps と同じく同じ行の古い項目を除いた後のものである。

- `buffer`（`BufferID`）
- `path`（`string`、省ける）: ファイルの絶対パスである。ファイルでないバッファでは空。
- `name`（`string`）
- `position`（`Position`）
- `text`（`string`、省ける）: その行のテキストの、先頭の空白を除いたものである。バッファが閉じていれば空。
- `current`（`bool`、省ける）: <C-o> と <C-i> が次に読む位置の項目かどうかである。

#### `ListHelpWord`

:help で引ける語 1 つである。本体は、:help でその語の説明の行が見つかる語だけを返す。
並びは、種類の順（キー、Ex コマンド、設定の名前、操作の ID）で、同じ種類の中は本体の一覧の順である。同じ語は 1 つにまとめる。

- `word`（`string`）: :help に渡す語（"w"、"<C-o>"、":write"、"lineNumber.show"、"page-down"）である。
  editor/execute の help に渡すと、その語の :help を開く。
- `kind`（`string`）: 語の種類である。HelpKindKey（key）（キー）、HelpKindCommand（command）（Ex コマンドと拡張機能のコマンド。Word は : で始まる）、
  HelpKindOption（option）（:option.set で変える設定の名前と、設定ファイルの最上位の項目の名前）、HelpKindAction（action）（設定の keymap に書く操作の ID）、
  HelpKindOperation（operation）（拡張機能のオペレーター、モーション、テキストオブジェクトの ID）のどれかである。
- `description`（`string`、省ける）: 語の 1 行の説明である。キー、操作の ID、拡張機能のコマンドと操作は、それを定めた所の説明文
  （キーの一覧、操作の表、宣言ファイルの description。description の無い拡張機能の操作は「拡張機能 <ID> の<種類>」）で、
  それが無い語（本体の Ex コマンド、設定の名前）は、:help でその語を引いたときの説明の行から
  Markdown の記号と語そのものを除いた 1 行である。無ければ空。

#### `EditorOpenResult`

editor/open の result である。

- `buffer`（`BufferID`）: 開いたバッファの番号である。
- `version`（`Version`）: 応答の時点のバージョン番号である。
- `moved`（`bool`）: カーソルを動かしたかどうかである。
  Position を省いたときと、Version の照合で動かさなかったときは false。

#### `EditorStateParams`

editor/state の params である。中身は無い。

項目は無い（`{}`）。

#### `EditorStateResult`

editor/state の result である。

- `buffer`（`BufferID`）: カーソルのあるバッファの番号である。
- `cursor`（`Position`）: カーソル位置である。
- `mode`（`Mode`）: モードである。
- `selections`（`[]Range`、省ける）: ビジュアルモードの選択範囲である。ビジュアルモードでなければ省く。
  ModeVisual（visual） では範囲 1 つ。
  ModeVisualLine（visualLine） では範囲 1 つで、最初の行の先頭から最後の行の終わりまでを指す。
  ModeVisualBlock（visualBlock） では行ごとに 1 つの範囲を、上の行から順に並べる。
- `buffers`（`[]BufferInfo`）: 開いているバッファの一覧である。
- `canSplit`（`bool`、省ける）: カーソルのあるタブの client のペインを、端末多重化ソフトの層で分割できるかである（diff/start）。
  端末多重化ソフトの外では false で、省く。
- `workDir`（`string`、省ける）: 操作中の client の作業ディレクトリ（:e の相対パスを解く場所）の絶対パスである（API 0.9.0 で足した）。
  段階 3 では client ごとに作業ディレクトリが違いうるので、拡張機能のプロセスの作業ディレクトリと一致するとは限らない。
  ファジーファインダーがファイルと検索の場所に使う。

#### `BufferInfo`

editor/state で返す、開いているバッファ 1 つの情報である。

- `buffer`（`BufferID`）
- `path`（`string`、省ける）: バッファに対応するファイルの絶対パスである。ファイルに対応しないバッファでは空。
- `fileType`（`string`、省ける）: ファイルの種類である。決まらなければ空。
- `version`（`Version`）: 現在のバージョン番号である。
- `modified`（`bool`）: 保存していない変更があるかどうかである。
- `readOnly`（`bool`、省ける）: 編集を禁止したバッファ（buffer/edit を断る）かどうかである。
  複数のバッファへの編集を、全部か何もしないかで適用する拡張機能（LSP の名前の変更）が、前もって確かめるのに使う。
- `changedOnDisk`（`bool`、省ける）: 読み込んだ後にファイルが外で変わった（buffer/save が断られる）かどうかである。

#### `MessageShowParams`

message/show の params である。

- `level`（`MessageLevel`）
- `text`（`string`）: 表示するメッセージである。

#### `MessageLevel`

JSON の文字列である。

利用者へのメッセージの重要度である。

- `"info"`（MessageInfo）
- `"warning"`（MessageWarning）
- `"error"`（MessageError）

#### `OptionGetParams`

option/get の params である。

- `buffer`（`BufferID`、省ける）: 範囲がバッファごとの項目を読むバッファである。省く（0）と、エディタ全体の項目だけを読める。
- `names`（`[]string`、省ける）: 読む項目の正規の名前である。空なら、Buffer を書いたときはすべての項目を、省いたときはエディタ全体のすべての項目を読む。
  表に無い名前と、Buffer を省いたときのバッファごとの項目は、誤りで断る。

#### `OptionGetResult`

option/get の result である。

- `values`（`map[string]json.RawMessage`）: 項目の正規の名前から今の値（項目の型の JSON の値）への対応である。

#### `OptionSetParams`

option/set の params である。

- `buffer`（`BufferID`、省ける）: 範囲がバッファごとの項目を変えるバッファである。省く（0）と、範囲がエディタ全体の項目を変える。
  Buffer を書いたときにエディタ全体の項目を、省いたときにバッファごとの項目を書くと、誤りで断る。
- `options`（`map[string]json.RawMessage`）: 項目の正規の名前から値への対応である。値は項目の型の JSON の値か、取り下げる null である。空にできない。
- `fileType`（`string`、省ける）: バッファごとの項目の値を、どのファイルの種類のバッファ（buffer/open の fileType）として決めたかである。省ける。
  バッファのファイルの種類が変わった（:set filetype=、種類の決まる名前での保存）後に、この拡張機能がそのバッファへ送る最初の
  option/set で、FileType が今のファイルの種類と同じなら、本体は Options に値を書いた項目の :set の値を隠し、この値を使う
  （本体のファイルの種類ごとの段と同じ。Neovim の ftplugin が :set の値を上書きするのと同じ考え方）。
  ファイルの種類の変更と関係なく送った値（設定ファイルが変わったなど）と、FileType を省いた値は、:set の値を隠さない。
  FileType が今のファイルの種類と違う値（種類が変わる前に決めた、遅れて届いた値）は、本体は成功で応答して捨てる。
  :set の値を隠させたい拡張機能は、前と種類の違う buffer/open を受けるたびに、値が前に送ったものと同じでも送る
  （A→B→A と続けて変わったとき、本体は A に戻った後の最初の値を待つ）。
  拡張機能が異常で終わって起動し直したときは、起動し直した後の最初の値（種類が今と同じもの）で、同じ規則のとおりに隠し直す。

#### `OptionSetResult`

option/set の result である。中身は無いが、後から項目を足せるように構造体にしている。

項目は無い（`{}`）。

#### `OutputAppendParams`

output/append の params である。
本体は、拡張機能と欄の名前の組ごとにテキストを持ち、行数が上限（仮に 10000 行）を超えたら古い行から捨てる。
欄はバッファではないので、拡張機能へ buffer/open で送らない。

- `channel`（`string`）: 欄の名前（"dap"、"acp" など）である。
- `text`（`string`）: 書き足すテキストである。改行を含んでよい。

#### `PaneOpenParams`

pane/open の params である。

本体は、その拡張機能へ最後に問い合わせを始めた client（元の client）のペインを、端末多重化ソフト（tmux、herdr）で分割し、
新しいペインの中で部品を描く client（ペインの部品の client）を起動する。拡張機能は端末多重化ソフトを直接操作しない。
ペインの中で打ったキーと選んだ項目は、元の client の操作として扱い、view/select などで知らせる。
ペインの中の <Esc> はペインを閉じない（ポップアップと違う）。利用者が端末多重化ソフトでペインを閉じるか、ペインの中で :q を打つと閉じ、
view/closed を送る。

Placement が PaneCenter（center） のときと、端末多重化ソフトの外と、ペインを開けなかったとき（herdr のプラグインを登録していないなど）は、
ペインに代えて元の client の画面の中央のポップアップに出す。このときは PopupCenter（center） のポップアップと同じく <Esc> で閉じる。
どちらでも応答の View は同じ扱いで、拡張機能は違いを知らずに view/update と view/close を送れる。
分割したペインに出したときは、元の client のカーソルのあるタブが替わるたびに view/tabChange を送る（ViewTabChangeParams）。

- `component`（`ViewComponent`）
- `placement`（`PanePlacement`、省ける）: 出す場所である。省くと PaneSplit（split） である（API 0.10.0 で足した。古い本体は読まずに分割したペインに出す）。
- `closeOnSelect`（`bool`、省ける）: 中央のポップアップに出したとき（Placement が PaneCenter（center） か、ペインに代えて中央のポップアップに出したとき）に、
  一覧と木の項目を選んだら（view/select を送った後に）本体がポップアップを閉じることを表す（API 0.10.0 で足した）。
  閉じたら view/closed を送る。木の子を持つ項目の <CR> は開閉なので閉じない。
  分割したペインは選んでも閉じない（ファイラのように、出しっぱなしにしたペインから選び続けるため）。
  閉じるのは選んだキーの処理の中なので、マクロの再生でも、選んだ後のキーは元の client のタブで続く。
- `direction`（`PaneDirection`、省ける）: 分割の向きである。省くと PaneRight（right） である。
- `size`（`int`、省ける）: 新しいペインの大きさを、元のペインの分割の向きの大きさに対する百分率（1〜99）で表す。0 なら端末多重化ソフトの既定である。
  herdr 0.9.0 は分割の大きさを受け付けないので、herdr では使わない。
- `focus`（`bool`、省ける）: 開いた新しいペインへフォーカスを移すかである。

#### `ViewComponent`

ポップアップに出す表示の部品 1 つである。Kind によって使う項目が違う。

- `kind`（`ViewKind`）
- `title`（`string`、省ける）: ポップアップの枠の上に出す見出しである。空なら出さない。
- `text`（`string`、省ける）: ViewText（text） の本文と、ViewInput（input） と ViewFinder（finder） の入力の行の最初の中身である。ViewInput（input） と ViewFinder（finder） では改行を含めない。
- `format`（`PopupFormat`、省ける）: ViewText（text） の本文の形式である。PopupMarkdown（markdown） なら、本体はコードの囲みの行を除いて出す（popup/show と同じ）。
- `highlights`（`[]Decoration`、省ける）: ViewText（text） の本文に色を付ける範囲である。位置は Format の整形の前の Text を指す（4.1 の位置）。
  Markdown で本体が除くコードの囲みの行の中の範囲（始まりか終わりがその行にあるもの）は捨てる。
  Style は 7.1 の色の名前（"keyword"、"diagnostic.error" など）で、知らない名前には色を付けない。
- `items`（`[]ViewItem`、省ける）: ViewList（list）、ViewTree（tree）、ViewFinder（finder） の項目である。ViewTree（tree） では Children で木にする。
- `cursor`（`string`、省ける）: ViewList（list） と ViewTree（tree） で、カーソルを置く項目の ID である（API 0.10.0 で足した）。
  開いたとき（popup/open、pane/open）と view/update で、その項目の行へカーソルを置く。
  空のときと、その項目が画面に出ていない（ID が無いか、閉じた木の項目の子にある）ときは、カーソルを置く行を変えない
  （view/update では、前にカーソルのあった項目を追う）。
  ファイラの「今のファイルの場所まで開いてカーソルを置く」と、h で親の項目へ移るのに使う。他の種類では読まない。
- `preview`（`bool`、省ける）: ViewList（list）、ViewTree（tree）、ViewFinder（finder） で、項目のプレビュー（ViewItem.Preview）を出す欄を持つことを表す
  （API 0.11.0 で足した）。省いても、項目のどれかが Preview を持てば欄を持つ。
  項目が後から届く部品（ファインダー）は、開いたときから true にしておくと、最初の項目が届いたときに窓の幅が変わらない。
  欄を持つ部品の窓は、幅が設定の境目を超えるときだけ広げて右にプレビューを出す。

#### `ViewKind`

JSON の文字列である。

表示の部品の種類である。

- `"text"`（ViewText）: テキストのバッファである。本体は、編集を禁止したファイルでないバッファとして出し、通常のノーマルモードのキー
  （移動、検索、ヤンク）が効く。
- `"list"`（ViewList）: 項目の一覧である。1 項目を 1 行に出し、<CR> で選ぶと view/select で項目の ID を知らせる。
- `"tree"`（ViewTree）: 項目の木である。子のある項目（HasChildren を含む）の <CR> は開閉で view/expand を送り、
  子の無い項目の <CR> は view/select で ID を知らせる。view/update では、項目の ID で開閉とカーソルの項目を保つ。
- `"input"`（ViewInput）: 1 行の入力である。中身が変わるたびに input/change で知らせ、<CR> で view/select（Item は中身）を送る。
- `"finder"`（ViewFinder）: 入力の行と一覧を組にした部品である（ファジーファインダー。API 0.9.0 で足した）。
  Text は入力の行の最初の中身、Items は一覧の項目（子を持たない）である。
  フォーカスは入力の行に置き、開いた時点でインサートモードにする。入力の行が変わるたびに input/change（Seq 付き）で知らせ、
  拡張機能は絞り込んだ一覧を view/update（同じ Seq 付き）で返す。本体は、最後に知らせた Seq より古い Seq の一覧を出さない。
  view/update は一覧（Items）と見出し（Title）だけを置き換え、入力の行の中身（Text）は読まない（利用者が打ち続けているため）。
  一覧の中のカーソルは <C-n>、<C-p>、<C-j>、<C-k>、<Down>、<Up> で動かし、<CR> で view/select（Item は一覧の項目の ID、
  Input は入力の行の中身）を送る。<Esc> はインサートモードでも閉じる。

#### `PopupFormat`

JSON の文字列である。

popup/show の本文の形式である。

- `"plaintext"`（PopupPlainText）
- `"markdown"`（PopupMarkdown）: Markdown である。

#### `ViewItem`

一覧か木の項目 1 つである。

- `id`（Go では `ID`、`string`）: 選んだときに view/select で返す値である。部品の中で一意にする。
- `label`（`string`）: 表示の文字列である。改行を含めない。
- `highlights`（`[]ViewSpan`、省ける）: Label の中の色を付ける範囲である。
- `children`（`[]ViewItem`、省ける）: ViewTree（tree） の子の項目である。ViewList（list） では使わない。
- `hasChildren`（`bool`、省ける）: ViewTree（tree） で、Children が空でも子を持つ項目（子を後から読む項目）であることを表す。
  本体は開閉の印を付け、<CR> で開くと view/expand で知らせる。拡張機能は子を読んで view/update で送る
  （ファイラのディレクトリ、DAP の変数の variablesReference）。ViewList（list） では使わない。
- `expanded`（`*bool`、省ける）: ViewTree（tree） で子を開いて出すかの指定である。開閉は本体が項目の ID ごとに持つ。
  省くと、前の木にあった ID の項目は利用者が開閉した状態を保ち、新しく現れた ID の項目は閉じて出す。
  書くと、前の木にあった項目もその状態にする（ファイラの「今のファイルの場所まで開く」、DAP の止まった位置のスコープを開く）。
- `preview`（`*ViewPreview`、省ける）: この項目にカーソルがあるときにプレビューに出すものである（API 0.11.0 で足した）。
  無ければ、プレビューの欄を空にする。古い本体は読まずに捨てる。
- `file`（`*ViewFile`、省ける）: この項目が指すファイルかディレクトリである（API 0.12.0 で足した）。
  あれば、本体は Label の前にファイルの種類のアイコンを付けて描く。キー、コマンド、レジスターのような、
  ファイルを指さない項目では省く。古い本体は読まずに捨て、アイコンを付けない。

#### `ViewSpan`

項目の表示の文字列の中の、色を付けるバイトの範囲 [Start, End) である。

- `start`（`int`）
- `end`（`int`）
- `style`（`string`）: 7.1 の色の名前である。

#### `ViewPreview`

一覧、木、ファインダーの項目 1 つの、プレビューに出すものである（API 0.11.0 で足した）。
拡張機能は出すものの場所だけを渡し、中身は本体が読む。拡張機能が、一覧の全部の項目の中身を先に読まなくてよいためである
（ファイラの 4 万個の項目のディレクトリ）。

  - Buffer があれば、その開いているバッファ（ファイルでないバッファを含む）の今の中身を出す。Path は読まない
  - Path がファイルなら、その中身を出す。開いているバッファのファイルなら、ファイルでなくバッファの今の中身を出す
  - Path がディレクトリなら、その中身の一覧を出す

Line は、出す行（0 から数える）である。あればその行を窓の真ん中に置いて色を付け、無ければ先頭から出す。
本体は、中身を編集を禁止したバッファとして開き、ファイルの種類で構文の色を付ける。大きなファイル、読めないファイル、
バイナリのファイルは、中身を出さずに理由を出す。

- `path`（`string`、省ける）: ファイルかディレクトリのパスである。相対パスは、操作中の client の作業ディレクトリから数える。
- `buffer`（`*BufferID`、省ける）: 開いているバッファの番号である。
- `line`（`*int`、省ける）: 出す行の番号（0 から数える）である。

#### `ViewFile`

一覧、木、ファインダーの項目 1 つが指すファイルかディレクトリである（API 0.12.0 で足した）。
本体は、これを持つ項目の表示の文字列の前に、ファイルの種類ごとのアイコンを付けて描く。
アイコンの文字と色は本体が決めるので、拡張機能はファイルの種類もアイコンも知らなくてよい。
項目の Highlights は、これまでどおり Label の中のバイトの位置で書く（本体がアイコンの分をずらす）。

ViewItem.Preview（カーソルがあるときに何を見せるか）とは別に持つ。プレビューの無い項目（ファイラの木を狭い窓で出すとき）と、
ファイルを指さないプレビュー（ファイルでないバッファのジャンプ）があるためである。

- `path`（`string`）: ファイルかディレクトリのパスである。本体は、名前と拡張子からファイルの種類を決める（設定の fileTypes を含む）。
  本体はパスの先を調べないので、相対パスでも、無いファイルでもよい。
- `dir`（`bool`、省ける）: ディレクトリなら true である。木では、開いたディレクトリと閉じたディレクトリでアイコンを分ける。

#### `PanePlacement`

JSON の文字列である。

pane/open で部品を出す場所である（API 0.10.0 で足した）。

- `"split"`（PaneSplit）: 元の client のペインを端末多重化ソフトで分割したペインである（既定）。
  端末多重化ソフトの外と、ペインを開けなかったときは、中央のポップアップに出す。
- `"center"`（PaneCenter）: 中央のポップアップである。popup/open の PopupCenter（center） と同じく、tmux と herdr の中では端末多重化ソフトの
  ポップアップの中の部品の client が描き、外では元の client の画面の中央に描く。開いている間はそれだけを操作し、<Esc> で閉じる。

#### `PaneDirection`

JSON の文字列である。

pane/open でペインを分割する向きである。

- `"right"`（PaneRight）: 元の client のペインの右に開く（既定）。
- `"down"`（PaneDown）: 元の client のペインの下に開く。

#### `PaneOpenResult`

pane/open の result である。

- `view`（`ViewID`）: 開いたペイン（端末多重化ソフトの外では、代わりのポップアップ）の ID である。

#### `PopupOpenParams`

popup/open の params である。
本体は、その拡張機能へ最後に問い合わせを始めた clientの画面に出す。
1 つの client に出すポップアップは 1 つで、新しいポップアップは前のものを置き換える（前のものの持ち主へ view/closed を送る）。

- `component`（`ViewComponent`）
- `placement`（`PopupPlacement`、省ける）: 置き場所である。省くと PopupAtCursor（cursor） である。
- `focus`（`bool`、省ける）: PopupAtCursor（cursor） のポップアップに、開いた時点でフォーカスを入れるかである。PopupCenter（center） では常に入れる。

#### `PopupPlacement`

JSON の文字列である。

ポップアップの置き場所である。

- `"cursor"`（PopupAtCursor）: 編集の client の画面の中の、カーソルの近くに浮かべる窓である（既定）。
  Focus が false なら、ホバーと同じく次のキーかカーソルの移動で閉じ、<C-w>w でフォーカスを入れられる。
- `"center"`（PopupCenter）: 中央のポップアップである。開いている間はそれだけを操作する。
  tmux と herdr の中では、端末多重化ソフトのポップアップの中の部品の client が描き、
  外と、端末多重化ソフトのポップアップを開けないときは、編集の client の画面の中央に描く。

#### `PopupOpenResult`

popup/open の result である。

- `view`（`ViewID`）: 開いたポップアップの ID である。

#### `PopupShowParams`

popup/show の params である。
本体は、Buffer がカーソルのあるバッファで、Version が現在のバージョン番号と一致し、カーソルが Position にある場合だけ表示する。
表示は、次のキー入力かカーソルの移動で消す。届いた時点でカーソルが Position から動いているか、
最後の拡張機能の操作の問い合わせの後に次のキーを解釈していれば、既に消えたものとして出さない。
新しい popup/show は、前の表示を置き換える。

- `buffer`（`BufferID`）: 表示の基準にするバッファの番号である。
- `version`（`Version`）: 本文を計算するもとにしたバージョン番号である。
- `position`（`Position`）: 表示の基準にする位置である。
- `text`（`string`）: 本文である。
- `format`（`PopupFormat`）

#### `ProgressReportParams`

progress/report の params である。

- `token`（`string`）: 拡張機能が決めた、1 つの処理を表す識別子である。空にできない。
- `kind`（`ProgressKind`）: 知らせの種類である。
- `title`（`string`、省ける）: 処理の表題（"gopls" の "Loading packages" に当たる短い語）である。begin でだけ読み、report と end では捨てる。
- `message`（`string`、省ける）: 本文（"3/10 files" など）である。report と end で空なら、前の本文のままにする（LSP と同じ）。
- `percentage`（`*int`、省ける）: 0 から 100 の百分率である。省くと（nil）、begin では出さず、report では前の値のままにする。end では読まない。
  範囲の外の値は、本体が 0 か 100 に寄せる。
- `failed`（`bool`、省ける）: end で、処理が成り立たずに終わった（失敗した、取り消した、処理していたプロセスが終わった）ことを表す。
  本体は、終わった印の代わりに失敗の印（symbols の progress.failed）と色（progress.failed）で描く。begin と report では読まない。

#### `ProgressKind`

JSON の文字列である。

progress/report の種類である（LSP の WorkDoneProgressBegin、WorkDoneProgressReport、WorkDoneProgressEnd の kind）。

- `"begin"`（ProgressBegin）: 進み具合を始める。Title を読む。
- `"report"`（ProgressReport）: 続いている進み具合の本文か百分率を変える。
- `"end"`（ProgressEnd）: 進み具合を終える。本体は終わった印を付けて、設定 progress.doneMs のミリ秒の後に消す。

#### `SignSetParams`

sign/set の params である。
本体の持ち方とバージョン番号の扱いは、diagnostic/set と同じである（DiagnosticSetParams）。
1 つの行に複数の印があれば、本体が表示の優先順位を決める。

- `buffer`（`BufferID`）
- `version`（`Version`）: 一覧を計算するもとにしたバージョン番号である。
- `signs`（`[]Sign`）: このバッファについて、この拡張機能が付ける印のすべてである。

#### `Sign`

行に付ける 1 つの印である。

- `line`（`int`）: 0 から数えた行の番号である。
- `kind`（`SignKind`）

#### `SignKind`

JSON の文字列である。

行に付ける印の種類である。名前は仮である。

- `"breakpoint"`（SignBreakpoint）: ブレークポイントである。
- `"breakpointUnverified"`（SignBreakpointUnverified）: デバッグアダプターが検証できなかったブレークポイントである。
- `"executionPoint"`（SignExecutionPoint）: デバッグ対象が停止した位置である。

#### `StatusLineSetParams`

statusline/set の params である。

- `part`（`string`）: 部品の ID である。宣言ファイルの contributes.statusline で足した ID を書く。
- `text`（`string`）: 部品の中身の文字列である。空なら部品を出さない（lualine の cond が偽の部品と同じ）。
  本体は 1 行で描くので、改行とタブなどの制御文字は空白 1 つに置き換える（StatusLineText）。
- `style`（`string`、省ける）: 部品を描く色の名前（本体の色の名前のどれか。`vi-imitation --docs usage 見た目の設定`）である。
  本体は、ステータス行の色の上に、この名前の色を重ねる（本体の部品の色と同じ規則）。
  空なら、利用者の設定の statusline.ext の色を使う。本体の知らない名前なら、色を重ねない。
  状態に応じて色を変える部品（lualine の mrreview の drift の警告）は、送るたびに名前を変える。

#### `ViewCloseParams`

view/close の params である。

- `view`（`ViewID`）

#### `ViewUpdateParams`

view/update の params である。
本体は、部品の種類が開いたときと同じ場合だけ置き換える。
一覧と木では、カーソルのあった項目の ID が新しい中身にもあれば、その項目の行へカーソルを置く。無ければ、行の番号を行数に収まる範囲で保つ。
テキストと入力の行では、行の番号を保つ。ただしテキストでカーソルが最後の行にあれば、新しい中身の最後の行へ置く
（書き足した出力の末尾を追う）。木の開閉の状態は、項目の ID ごとに保つ（ViewItem.Expanded）。

ViewFinder（finder） では、一覧（Items）と見出し（Title）だけを置き換え、Component.Text は読まない。
Seq は、この一覧がどの入力に答えたものかを、input/change の Seq（開いたときの中身なら 0）で表す。
本体は、最後に送った input/change の Seq より小さい Seq の view/update を捨てる（古い入力への一覧を出さない）。
同じ Seq の view/update は何度送ってもよい（候補を読みながら一覧を送り直すとき）。
前の view/update より大きい Seq の一覧では、カーソルを先頭の項目へ置き、同じ Seq なら項目の ID で保つ。
ViewFinder（finder） 以外では Seq を読まない。

- `view`（`ViewID`）
- `component`（`ViewComponent`）
- `seq`（`uint64`、省ける）

#### その他の定数

- `key`（HelpKindKey）
- `command`（HelpKindCommand）
- `option`（HelpKindOption）
- `action`（HelpKindAction）
- `operation`（HelpKindOperation）: 読み込んだ拡張機能のオペレーター、モーション、テキストオブジェクトの ID である（宣言ファイルの contributes）。
- `3`（SocketFDStart）: EnvSocketFDs（VI_IMITATION_SOCKET_FDS） で引き継ぐ最初のファイル記述子の番号である。
  0〜2 は標準入出力で、本体との通信に使う。
- `-32001`（ErrorCodeReadOnly）: 編集を禁止したバッファへの buffer/edit を本体が断るときの、誤りの応答の code である。
  JSON-RPC 2.0 で実装が定める誤りの範囲（-32000〜-32099）の値である。message は画面に出すものと同じ E21 の文言である。
  編集の無い（NoLines と EndOfLine だけの）要求も断る。
  版の不一致（BufferEditResult の Applied が false）と違い、新しい版で作り直しても通らないので、拡張機能は繰り返さない。
  ext/server を使う拡張機能は、server.IsReadOnly で見分けられる。
- `150`（WindowNotifyIntervalMs）: 本体が window/change を同じ拡張機能へ続けて送るときの、送る間隔の下限のミリ秒である（仮）。
  nvim-treesitter-context（オーナーの nvim に入っているコミット 8decd73460d26ef0a0befb745d022dd1fc2c38b8）の
  lua/treesitter-context.lua の throttle_by_id の 150 ミリ秒に合わせた。
- `64`（MaxBandLines）: band/set の 1 つの帯に書ける行の数の上限である（仮）。超えた帯は、本体がログに残して捨てる。
<!-- generated:types:end -->
