<!-- AI エージェント向けの文書（vi-imitation --docs extensions）。印の間は make docs が生成する。印の間を手で編集しない。 -->
# 拡張機能

拡張機能は、本体が子プロセスとして起動する別の実行ファイルである。
本体と拡張機能は、子プロセスの標準入力と標準出力で JSON-RPC 2.0 のメッセージをやり取りする。
メッセージの区切りは LSP と同じ `Content-Length: <バイト数>\r\n\r\n` のヘッダーである。
拡張機能の標準エラー出力は、本体の記録（`--log` のファイル）に 1 行ずつ残る（`vi-imitation --docs debugging`）。

<!-- generated:api-version:begin -->
この本体の拡張機能 API の版は `0.17.0` である（`initialize` の `apiVersion`）。
<!-- generated:api-version:end -->

Go で書くときは、Go のモジュール `github.com/piplupflipper/vi-imitation` の公開パッケージ `ext`（メッセージの型）と `ext/server`（メッセージのループ、バッファの写し、ハンドラーの登録。`vi-imitation --docs script` に一覧がある）を使える。
Go のソースを 1 つ置いて、コンパイルせずに動かすスクリプトの拡張機能もある（`vi-imitation --docs script`）。

## 置き場所

```jsonc
// $XDG_DATA_HOME/vi-imitation/extensions/hello/manifest.json（XDG_DATA_HOME が空なら ~/.local/share/vi-imitation/extensions/hello/）。
// 本体の実行ファイルの隣の extensions/<名前>/manifest.json も読む。同じ ID なら前者を使う
{
  "id": "hello",
  // 相対パスは宣言ファイルのディレクトリから解決する。PATH からは探さない
  "command": "./hello",
  "activation": [
    { "on": "command", "id": "hello.say" },
    { "on": "filetype", "pattern": "markdown" },
  ],
  "contributes": {
    "commands": [{ "id": "hello.say", "description": "挨拶をメッセージの行に出す" }],
  },
}
```

本体は起動のときに宣言ファイルを読むだけで、起動の契機（`activation`）が成り立つまで拡張機能を起動しない。
宣言ファイルを変えたら、本体を起動し直す。
利用者の設定ファイルの `extensions.settings.<ID>` は、`initialize` の `settings` で拡張機能に渡る。
`extensions.activation.<ID>` に書いた `filetype` の契機は、宣言ファイルの `activation` に足される。

## 動き

<!-- generated:lifecycle:begin -->
1. 本体は、起動の契機が成り立ったら `command` を `args` と一緒に起動する。作業ディレクトリは本体のものを引き継ぐ
2. 本体は `initialize` を送る。5 秒 以内に応答が無ければ異常とみなす
3. 本体は、開いているバッファの全文を `buffer/open` で送り、その後の変更を `buffer/change` で送る
4. 本体は 5 秒 ごとに `ping` を送る。2 秒 以内に応答が無ければ異常とみなす。時間のかかる処理は、メッセージを読む処理と別に動かす
5. 本体は、エディタを終えるときに `shutdown` を送る。3 秒 で終わらなければ SIGTERM を、さらに 1 秒 で SIGKILL を送る

異常（プロセスの終了、期限切れ、解釈できないメッセージ）のときは、プロセスグループに SIGKILL を送り、500 ms から 30 秒 まで倍に延ばしながら起動し直す。
5 回続けて失敗すると無効にする（1 分 続けて動けば、失敗を数え直す）。
起動し直したときは、開いているバッファの全文を送り直す。

キー入力ごとに結果が要る問い合わせ（`motion/resolve`、`textObject/resolve`、`operator/apply`）は、期限（設定の `extensions.syncTimeoutMs`。既定は 100 ms）までに応答が無ければ、その操作を失敗にする。
期限を過ぎて届いた応答は適用しない。
<!-- generated:lifecycle:end -->

異常と無効は、エディタのメッセージの行と `:messages` に出る。

## 共通の取り決め

<!-- generated:ext-package:begin -->
### 位置の数え方

Transaction と buffer/change の Edits は、どちらも逐次の数え方で位置を書く。
各編集の位置は、同じ列の中で直前の編集までを適用した後のテキストで数える（LSP の textDocument/didChange と同じ）。
詳しくは `vi-imitation --docs extension-types Transaction` にある。

### キー入力ごとの問い合わせ

motion/resolve、textObject/resolve、operator/apply の params と result は、次の項目を共通に持つ。

Count は回数である。
0 は、利用者が回数を省略したことを表す。
Vim の G と 1G のように、省略と 1 を区別する操作のために、本体は省略を 1 に読み替えずに送る。
区別しない操作は、0 を 1 として扱えばよい。

Args は、操作が受け取る引数である（例: surround の囲む文字）。
本体はドットリピートとマクロのために、拡張機能の ID、操作の ID と一緒に Args を記録し、再実行のときに同じ値を渡す。
引数が無ければ省く。
いまの本体が送る形は次の 2 つである（どちらも仮）。

  - {"char":"x"}: キーで呼んだ操作の後に取った 1 文字
  - {"text":"..."}: コマンドラインから呼んだコマンド（command/execute）の、ID の後の引数の文字列（:acp.prompt 説明して の「説明して」）。
    ID の後の空白を除いた行末までで、"|" も含む。引数を書かなければ args を省く

引数を取らないコマンドは、text が空でなければ ok を false にして、引数を取らないことを message/show で知らせる
（標準の拡張機能の dap.* と acp.cancel はそうする）。

result の OK は、操作が成り立ったかどうかである。
OK が false（項目が無い場合を含む）なら、本体は操作を失敗として扱い、他の項目を読まない。
f の対象の文字が見つからない場合のように該当が無いときは、OK を false にした result を返す。
未知の ID や拡張機能の内部の誤りは JSON-RPC のエラーで返してもよく、本体はそれも操作の失敗として扱う。
<!-- generated:ext-package:end -->

## 環境変数

本体は、拡張機能の子プロセスに次の環境変数を渡す。

<!-- generated:env:begin -->
- `VI_IMITATION_EXTENSION_DIR`: 本体が拡張機能の子プロセスに渡す環境変数の名前である（名前は仮）。
  値は、宣言ファイルのあるディレクトリの絶対パスである。
  子プロセスの作業ディレクトリは本体のものを引き継ぐので、宣言ファイルの隣のファイルを探すときはこの値を使う。
  スクリプトの拡張機能の実行ファイルは、args の相対パスのスクリプトをここから解決する。
- `VI_IMITATION_MCP_SOCKET`: 本体がすべての拡張機能の子プロセスに渡す環境変数の名前である（名前は仮）。
  値は、MCP の中継コマンドが接続する Unix ドメインソケットの絶対パスで、本体が起動ごとに一意に決める。
  本体は、起動の契機 ActivationSocket（socket） を宣言した拡張機能のために、このパスで起動時から待ち受ける。
  ACP の拡張機能はこのパスを中継コマンドの引数にしてエージェントへ渡す。
- `VI_IMITATION_SOCKET_FDS`: 本体が ActivationSocket（socket） の拡張機能へ引き継いだファイル記述子の数を渡す環境変数の名前である（名前は仮）。
  値が n（1 以上）なら、ファイル記述子 SocketFDStart（3） が待ち受けているソケットで、
  SocketFDStart（3）+1 から SocketFDStart（3）+n-1 までが、本体が受け付けて拡張機能へ渡す接続である。
  値が空か変数が無ければ、何も引き継いでいない。
  本体は、すべての拡張機能の子プロセスでこの変数を上書きするので、本体の環境の古い値は引き継がない。
  拡張機能は ext/server の InheritedSocket で受け取る。
<!-- generated:env:end -->

## 宣言ファイル

宣言ファイルの型と、そこから辿れる型である。
型は Go の書き方で書く（`[]T` は配列、`*T` は省ける値、`json.RawMessage` は任意の JSON）。
「省ける」は、書かなくてよい（空の値と同じ）ことを表す。

<!-- generated:manifest:begin -->
#### `Manifest`

拡張機能の宣言ファイルの内容である。
本体は、拡張機能を起動せずに宣言ファイルだけを読み、追加する操作と起動の契機を知る。
項目の名前は仮である。

宣言ファイルは JSONC で書く。

- `id`（Go では `ID`、`string`）: 拡張機能の ID である。ドットリピートの記録と設定で使う。
- `command`（`string`）: 拡張機能の実行ファイルのパスである。
  相対パスは、宣言ファイルのあるディレクトリから解決する。
- `args`（`[]string`、省ける）: Command に渡す引数の列である。
  スクリプトの拡張機能が、共通の実行ファイルにスクリプトのパスを渡すのに使う。無ければ省く。
  本体は解決せずにそのまま渡す。相対パスの引数は、拡張機能が EnvExtensionDir（VI_IMITATION_EXTENSION_DIR） のディレクトリから解決する。
- `saveHook`（`bool`、省ける）: true なら、稼働中のときに保存の直前の buffer/willSave を受け取る。
  保存の前に整形する LSP の拡張機能が使う。
- `noBufferSync`（`bool`、省ける）: true なら、本体がこの拡張機能へバッファの同期（buffer/open、buffer/change、buffer/close、buffer/didSave）を
  送らないことを表す（API 0.16.0 で足した）。
  バッファのテキストを使わずに常に動く拡張機能（メモリの使用量など）が、開いたすべてのバッファの複製を持たないためのものである。
  本体は、この拡張機能への command/execute と provider/invoke の params の Withheld を立てるので、ext/server はハンドラーへ
  バッファの代わりに nil を渡す。バッファの複製を要る操作（operators、motions、textObjects）と SaveHook とは併せて書けない（Validate）。
  古い本体（0.15.0 まで）は未知の項目として無視し、バッファを送る。
- `windowEvents`（`bool`、省ける）: true なら、稼働中のときに窓の変化の知らせ window/change を受け取る。
  関数の見出しを窓の上端に固定する拡張機能（nvim-treesitter-context に当たる）が使う。起動の契機にはならない。
  足したのは API 0.17.0 である。古い本体は未知の項目として警告にし、知らせを送らない。
- `activation`（`[]Activation`）: 起動の契機の一覧である。どれか 1 つが成り立った時点で起動する。
- `contributes`（`Contributes`）: 拡張機能が追加する操作である。
- `provides`（`[]Operation`、省ける）: 拡張機能が引き受ける本体の操作（提供者として名乗る操作）の列である。
  本体は、操作が呼ばれたら、カーソルのあるバッファのファイルの種類で提供者を選び、provider/invoke で問い合わせる。
  選び方は次のとおりである（仮）。
    - その操作を名乗った拡張機能のうち、ファイルの種類の契機（ActivationFileType（filetype）。設定で足したものを含む）と
      設定のファイルの種類の契機（ActivationSettingsFileType（settingsFileType））のどちらも持たないか、
      ファイルの種類の契機の Pattern のどれかがバッファのファイルの種類と一致するものを候補にする
    - 候補のうち、未起動か起動中か稼働中のものを、本体が宣言ファイルを読んだ順に探し、最初のものを使う。
      同じ操作を複数の拡張機能が名乗ったときの選び方は未決で、それまでは先に読んだものを使う
    - 未起動なら、問い合わせで起動する。provides だけを持つ拡張機能は、Activation を空にしてよい
  この版の API の知らない操作の名前は、Validate が警告にし、本体は問い合わせない。

#### `Activation`

1 つの起動の契機である。

- `on`（`ActivationKind`）: 契機の種類である。
- `id`（Go では `ID`、`string`、省ける）: 操作が呼ばれたときの契機で、その操作の ID である。それ以外の契機では空。
- `pattern`（`string`、省ける）: ActivationFileType（filetype） の契機で、ファイルの種類の名前である。
  ActivationSettingsFileType（settingsFileType） の契機では、設定の中の値を指す JSON Pointer である。それ以外の契機では空。

#### `ActivationKind`

JSON の文字列である。

起動の契機の種類である。

ActivationOperator（operator）、ActivationMotion（motion）、ActivationTextObject（textObject）、ActivationCommand（command） は、
同じ拡張機能が追加した、その種類の操作が呼ばれたときに成り立つ。
ActivationFileType（filetype） は、Pattern と一致する種類のファイルを開いたときに成り立つ。
ActivationSocket（socket） は、本体が EnvMCPSocket（VI_IMITATION_MCP_SOCKET） のパスで待ち受けるソケットへ、最初の接続が来たときに成り立つ。
ID と Pattern は使わない。本体は、待ち受けているソケットと受け付けた接続を、子プロセスへ引き継ぐ（EnvSocketFDs（VI_IMITATION_SOCKET_FDS））。
1 つの本体で待ち受けるソケットは 1 つなので、この契機を宣言した拡張機能が複数あれば、本体は最初の 1 つだけに使う。

ActivationSettingsFileType（settingsFileType） は、利用者の設定のうちこの拡張機能に向けた部分（extensions.settings.<ID>）から、
ファイルの種類の契機（ActivationFileType（filetype））を作ることを表す。
Pattern は、設定の中の値を指す JSON Pointer（RFC 6901）で、参照トークン "*" はオブジェクトのすべての項目か配列のすべての要素に当たる。
指した値が文字列ならその 1 つを、配列ならその中の文字列のすべてを、ファイルの種類の名前とする。それ以外の値は無視する。
利用者が設定に書いたファイルの種類を、設定の extensions.activation にもう一度書かずに済ませるためのものである。
"*" が当たった項目の下にその先の値が無いか、文字列か文字列を含む配列でなければ、本体はその項目を設定の誤りとして知らせる（項目の名前の綴りの誤りのため）。
例: tree-sitter の拡張機能の {"on": "settingsFileType", "pattern": "/parsers/*/fileTypes"}。

ActivationStartup（startup） は、本体が起動して最初の画面を描き終えた後に成り立つ（API 0.16.0 で足した）。
ID と Pattern は使わない。利用者の操作もファイルを開くことも待たずに、起動している間は常に動かしておく拡張機能
（開いたバッファすべてに色を付けるものなど）が使う。最初の画面を描く前には起動しないので、本体の起動の速さを損なわない。
利用者は、設定の extensions.disabled にその拡張機能の ID を書けば、起動させない（宣言ファイルを読まない）。
古い本体（0.15.0 まで）は知らない契機として警告にして無視するので、この契機だけを持つ拡張機能は起動しない。

- `"operator"`（ActivationOperator）
- `"motion"`（ActivationMotion）
- `"textObject"`（ActivationTextObject）
- `"command"`（ActivationCommand）
- `"filetype"`（ActivationFileType）
- `"socket"`（ActivationSocket）
- `"settingsFileType"`（ActivationSettingsFileType）
- `"startup"`（ActivationStartup）

#### `Contributes`

拡張機能が追加する操作の一覧である。

- `operators`（`[]Contribution`、省ける）
- `motions`（`[]Contribution`、省ける）
- `textObjects`（`[]Contribution`、省ける）
- `commands`（`[]Contribution`、省ける）
- `statusline`（Go では `StatusLine`、`[]StatusLinePart`、省ける）: ステータス行とタブ行に足す部品の一覧である。
  部品の中身は statusline/set で送る（statusline/set）。足したのは API 0.5.0 である。
- `popupKeys`（`[]Contribution`、省ける）: この拡張機能が popup/open で開いたポップアップの中だけで効くキーである。
  各項目の Keys は空にできない。フォーカスを入れたポップアップのノーマルモードで打つと、本体は view/key で ID を知らせる
  （ViewKeyParams）。本体の既定のキーより先に引く。ID は popupKeys の中で一意にし、他の操作の ID とは別に数える。
  操作ではないので、起動の契機（Activation）の対象にはならない。

#### `Contribution`

拡張機能が追加する 1 つの操作である。

- `id`（Go では `ID`、`string`）: 操作の ID である。拡張機能の中で一意にする。
  本体はドットリピートで、拡張機能の ID とこの ID を記録する。
- `keys`（`string`、省ける）: 既定のキーの列（"ys" など）である。利用者の設定で上書きできる。
  既定のキーを持たない操作では空。
- `description`（`string`、省ける）: 操作の説明文である。本体はキーの一覧とヘルプ（:help）に出す。無ければ省く。
  足したのは API 0.4.0 の後だが、版は上げていない。
- `action`（`PopupKeyAction`、省ける）: Contributes.PopupKeys の項目で、キーを打ったときに view/key を送る代わりに本体がする操作である（API 0.10.0 で足した）。
  省くと view/key を送る。ほかの種類の操作では読まない。

#### `PopupKeyAction`

JSON の文字列である。

contributes.popupKeys のキーで、view/key の代わりに本体がする操作の名前である。

- `"select"`（PopupKeySelect）: 一覧と木の <CR> と同じく、カーソルの行の項目を選ぶ（view/select）。木の子を持つ項目なら開閉する（view/expand）。
  ファイラの l（ディレクトリを開閉し、ファイルを開く。nvim の neo-tree の open）に使う。
  <CR> と同じ経路なので、選んだら閉じる中央のポップアップ（PaneOpenParams.CloseOnSelect）を閉じ、
  分割したペインで選んだ後の editor/open は端末多重化ソフトのフォーカスを元の client へ戻す。

#### `StatusLinePart`

宣言ファイルの contributes.statusline の 1 つの部品である。

- `id`（Go では `ID`、`string`）: 部品の ID である。拡張機能の ID と "." を前に付け、その後には "." を含めない（拡張機能の ID が "mrreview" なら "mrreview.status"）。
  利用者は、設定の statusline と tabline の left と right にこの ID を書いて並べる。
  本体の部品の ID は "." を含まないので、本体の部品と重ならない。拡張機能の ID は一意で、最後の "." より前が拡張機能の ID に決まるので、
  拡張機能の ID が "." を含んでも、他の拡張機能の部品と重ならない。
- `description`（`string`、省ける）: 部品の説明文である。本体はヘルプとキーの一覧に出さないが、利用者が宣言ファイルを読んで並べる部品を選べるように書く。

#### `Operation`

JSON の文字列である。

本体の操作の名前である（名前は仮）。
本体は LSP と DAP のプロトコルを知らず、操作の名前と引数だけを提供者へ渡す。

移動の操作（OpDefinition（definition）、OpImplementation（implementation）、OpTypeDefinition（typeDefinition））は、Args の peek が true なら、
移らずに移動先の周りをポップアップに出す。
OpFormat（format） は、gq{モーション} から呼ばれたときは ProviderInvokeParams.Range に範囲が入る。範囲が無ければバッファの全体を整形する。
OpRename（rename） は、Args の text が新しい名前である（コマンドラインの :rename 新しい名前）。
OpBreakpointConditional（breakpoint.conditional） は、Args の text が条件の式である（:breakpoint.conditional i > 3）。

診断の一覧と移動（<leader>le、<leader>lj など）は、本体が diagnostic/set で受け取った診断から作るので、操作の名前を持たない。

- `"definition"`（OpDefinition）
- `"implementation"`（OpImplementation）
- `"typeDefinition"`（OpTypeDefinition）
- `"hover"`（OpHover）
- `"references"`（OpReferences）
- `"documentSymbols"`（OpDocumentSymbols）
- `"workspaceSymbols"`（OpWorkspaceSymbols）
- `"format"`（OpFormat）
- `"rename"`（OpRename）
- `"codeAction"`（OpCodeAction）
- `"completion"`（OpCompletion）: 名前だけを決めた操作である。補完の取り決めは未決で、本体はまだ問い合わせない。
- `"breakpoint.toggle"`（OpBreakpointToggle）
- `"breakpoint.conditional"`（OpBreakpointConditional）
- `"breakpoint.clear"`（OpBreakpointClear）
- `"debug.continue"`（OpDebugContinue）
- `"debug.pause"`（OpDebugPause）
- `"debug.terminate"`（OpDebugTerminate）
- `"debug.restart"`（OpDebugRestart）
- `"debug.runLast"`（OpDebugRunLast）
- `"debug.stepOver"`（OpDebugStepOver）
- `"debug.stepIn"`（OpDebugStepIn）
- `"debug.stepOut"`（OpDebugStepOut）
- `"debug.toggleUI"`（OpDebugToggleUI）

<!-- generated:manifest:end -->

## メソッド

params と result の型の項目は、`vi-imitation --docs extension-types <型の名前>`（`vi-imitation --docs extension-types BufferOpenParams`）で 1 つずつ出す。
位置（`Position`）は、0 から数えた行の番号と、行の先頭から数えた UTF-8 のバイト数の組である。

<!-- generated:methods:begin -->
#### 本体から拡張機能へ

- `$/cancel`: 通知。params は `CancelParams`。
- `buffer/change`: 通知。params は `BufferChangeParams`。
- `buffer/close`: 通知。params は `BufferCloseParams`。
- `buffer/didSave`: 通知。params は `BufferDidSaveParams`。
- `buffer/open`: 通知。params は `BufferOpenParams`。
- `buffer/willSave`: リクエスト。params は `BufferWillSaveParams`、result は `BufferWillSaveResult`。宣言ファイルの saveHook が true の稼働中の拡張機能にだけ、保存の直前に送る。
- `command/execute`: リクエスト。params は `CommandExecuteParams`、result は `CommandExecuteResult`。宣言ファイルの contributes.commands に書いたコマンドを実行する（仮）。
- `initialize`: リクエスト。params は `InitializeParams`、result は `InitializeResult`。応答が届いた時点で、拡張機能は稼働中になる。
- `input/change`: 通知。params は `InputChangeParams`。
- `motion/resolve`: リクエスト。params は `MotionResolveParams`、result は `MotionResolveResult`。
- `operator/apply`: リクエスト。params は `OperatorApplyParams`、result は `OperatorApplyResult`。
- `ping`: リクエスト。params は `PingParams`、result は `PingResult`。
- `provider/invoke`: リクエスト。params は `ProviderInvokeParams`、result は `ProviderInvokeResult`。本体の操作（Operation）を、宣言ファイルの provides で名乗った拡張機能（提供者）へ問い合わせる。本体は、利用者が操作のキー（<leader>lh など）を打つか、コマンドラインから操作の名前で呼んだ（:rename 新しい名前）ときに、カーソルのあるバッファのファイルの種類で提供者を選んで送る（選び方は `vi-imitation --docs extensions Manifest`）。応答の扱いは command/execute と同じで、言語サーバーやデバッグアダプターの応答を待つ操作は、受け付けたことだけを ok で返し、結果を後から editor/open、popup/show、buffer/edit などで届けてよい。
- `shutdown`: 通知。params は `ShutdownParams`。本体が拡張機能を正常に止めるとき（エディタの終了、拡張機能の無効化）に送る。
- `textObject/resolve`: リクエスト。params は `TextObjectResolveParams`、result は `TextObjectResolveResult`。
- `view/closed`: 通知。params は `ViewClosedParams`。
- `view/expand`: 通知。params は `ViewExpandParams`。
- `view/key`: 通知。params は `ViewKeyParams`。
- `view/select`: 通知。params は `ViewSelectParams`。
- `view/tabChange`: 通知。params は `ViewTabChangeParams`。
- `window/change`: 通知。params は `WindowChangeParams`。窓の見えている行の範囲、カーソルの位置、モード、表示領域の大きさが変わったことを知らせる（Neovim の WinScrolled と CursorMoved に当たる）。本体が送るのは、宣言ファイルの windowEvents が true で、稼働中の拡張機能だけである。起動の契機にはならない（未起動の拡張機能は起動しない）。稼働中になった拡張機能には、その時点のすべての窓の状態を送る。キーを打つたびには送らない。本体は、画面を描いた後に、窓ごとに前に送った中身と比べ、違う窓だけを送る。前に送ってから WindowNotifyIntervalMs（150） のミリ秒が経っていなければ、その間は送らずに、時間が来た時点の最後の中身を 1 回だけ送る（nvim-treesitter-context の throttle_by_id と同じく、続けて変わっても、その時間のうちに 2 回までにする）。このため、拡張機能が受け取る中身は、途中の中身を飛ばすことがある。ext/server は、さらに、フックが前の知らせを処理している間に同じ窓の知らせが続けて届いたら、最後の 1 件だけをフックへ渡す（拡張機能の処理が間隔より遅くても、知らせが溜まって ping の応答が遅れないようにする）。窓が無くなった（client が終わった）ときは、Closed を true にした知らせを 1 回送る。その WindowID は使い回さない。古い本体（0.16.0 まで）は送らない。

#### 拡張機能から本体へ

- `band/set`: 通知。params は `BandSetParams`。窓の表示領域の上の端に、何行かを重ねて描く帯を置く（nvim-treesitter-context の窓に当たる）。本体は、拡張機能と窓の組ごとに最後に届いた帯を持ち、窓のバッファが Buffer のときだけ描く。行の無い帯（Lines が空）は、その組の帯を消す。拡張機能が異常で終わるか止まったら、その拡張機能の帯をすべて消す。1 つの窓に複数の拡張機能の帯があれば、拡張機能の ID の順に上から並べる。帯はカーソルの行を隠さない。本体は、帯の行数がカーソルの表示領域の中の行（上から数えた行数）を超えるとき、帯の上の行から削って、カーソルの行より上に収める（nvim-treesitter-context の calc_max_lines と trim_scope の既定の outer と同じ）。帯の下に隠れた本文の行のために、スクロールの余白は足さない（nvim-treesitter-context と同じ）。古い本体（0.16.0 まで）は、知らない通知として捨てる（ログに残すだけ）。
- `buffer/create`: リクエスト。params は `BufferCreateParams`、result は `BufferCreateResult`。
- `buffer/dispose`: リクエスト。params は `BufferDisposeParams`、result は `BufferDisposeResult`。
- `buffer/edit`: リクエスト。params は `BufferEditParams`、result は `BufferEditResult`。編集を禁止したバッファへの要求は、result ではなく、code が ErrorCodeReadOnly（-32001） の誤りの応答で断る。
- `buffer/resync`: 通知。params は `BufferResyncParams`。差分が欠けたと判断した拡張機能が、全文の送り直しを求める。
- `buffer/save`: リクエスト。params は `BufferSaveParams`、result は `BufferSaveResult`。
- `buffer/update`: リクエスト。params は `BufferUpdateParams`、result は `BufferUpdateResult`。
- `choice/ask`: リクエスト。params は `ChoiceAskParams`、result は `ChoiceAskResult`。利用者の応答を待つので、本体はキー入力ごとの問い合わせの期限を適用しない。
- `decoration/set`: 通知。params は `DecorationSetParams`。
- `diagnostic/list`: リクエスト。params は `DiagnosticListParams`、result は `DiagnosticListResult`。
- `diagnostic/set`: 通知。params は `DiagnosticSetParams`。
- `diff/start`: リクエスト。params は `DiffStartParams`、result は `DiffStartResult`。
- `editor/execute`: リクエスト。params は `EditorExecuteParams`、result は `EditorExecuteResult`。editor/list の keys と commands の項目を、利用者が打ったのと同じく実行させる（ファインダーで選んだキーとコマンド）。help の項目は、その語の :help を開かせる（API 0.13.0）。
- `editor/list`: リクエスト。params は `EditorListParams`、result は `EditorListResult`。
- `editor/open`: リクエスト。params は `EditorOpenParams`、result は `EditorOpenResult`。
- `editor/state`: リクエスト。params は `EditorStateParams`、result は `EditorStateResult`。
- `message/show`: 通知。params は `MessageShowParams`。
- `option/get`: リクエスト。params は `OptionGetParams`、result は `OptionGetResult`。本体の設定の項目の今の値（利用者が :set で変えた値、拡張機能が入れた値、初めの値の段を重ねた、本体が使う値）を読む。古い本体（0.14.0 まで）は method not found で応答する。
- `option/set`: リクエスト。params は `OptionSetParams`、result は `OptionSetResult`。本体の設定の項目の値を、拡張機能から変える。項目の名前は、設定ファイルと :option.set と同じ正規の名前（"indent.width" など）で書く。値は項目の型の JSON の値（真偽は true か false、整数は 10 進の整数、文字列は JSON の文字列）で書き、null はその項目の値を取り下げる。本体は、表に無い名前、範囲の合わない項目（下の Buffer）、ファイルから判定して決める項目（filetype）、型の違う値、範囲の外の値を、:option.set と同じ検査で確かめ、1 つでも通らなければどの値も入れずに、code が CodeInvalidParams の誤りで応答する。# バッファごとの項目Buffer を書くと、そのバッファの範囲がバッファごとの項目を変える。値はバッファの初めの値の段の最後の段（.editorconfig の後）に入り、利用者が :set と :option.set で変えた値には勝たない。拡張機能ごとに値を分けて持ち、同じ項目に複数の拡張機能が値を入れたら、宣言ファイルを先に読んだ拡張機能の値が勝つ（本体の操作の提供者の選び方と同じ順）。後から届いた順にしないのは、拡張機能の起動の速さで値が変わらないようにするためである。値は Options に書いた項目だけを変え（書かなかった項目は前に入れた値のまま）、null で取り下げる。ファイルの種類が変わっても、本体は値を捨てない（buffer/open を新しいファイルの種類で送り直すので、拡張機能が送り直す）。送り直した値に、その種類を OptionSetParams.FileType で付けると、本体のファイルの種類ごとの段と同じく :set の値を隠す。# エディタ全体の項目Buffer を省く（0）と、範囲がエディタ全体の項目を変える。値は設定ファイルの値に勝ち、利用者が :set と :option.set で変えた値には勝たない。複数の拡張機能の勝ち負けは、バッファごとの項目と同じである。# 拡張機能が止まったとき拡張機能が異常で終わった、無効になった、:ext sync で止めたときは、本体はその拡張機能が入れた値をすべて取り下げる（他の表示のデータと同じ）。起動し直した拡張機能は、buffer/open を受けて値を送り直す。古い本体（0.14.0 まで）は method not found で応答する。
- `output/append`: 通知。params は `OutputAppendParams`。
- `pane/open`: リクエスト。params は `PaneOpenParams`、result は `PaneOpenResult`。
- `popup/open`: リクエスト。params は `PopupOpenParams`、result は `PopupOpenResult`。
- `popup/show`: 通知。params は `PopupShowParams`。
- `progress/report`: 通知。params は `ProgressReportParams`。時間のかかる処理（言語サーバーの読み込み、パーサのビルド、道具の入れ込み）の進み具合を知らせる。中身は LSP 3.17 の WorkDoneProgress の begin、report、end に対応させた。1 つの処理を、拡張機能が決めた識別子（Token）で表し、begin で始め、report で中身を変え、end で終える。本体は、続いている進み具合と、終わってから設定 progress.doneMs のミリ秒が過ぎていないものを、編集の client の画面の右下に重ねて出す（設定 progress.show が false なら出さない）。本体が入れ子の待ち（保存の前の整形の buffer/willSave の応答など）をしている間も、取っておかずにその場で出す。識別子は拡張機能ごとに分けて扱うので、他の拡張機能の識別子と重なってもよい。続いている識別子の begin は、その進み具合を始め直す（表題と本文と百分率を置き換える）。begin の無い（終えた後を含む）識別子の report と end は捨てる。拡張機能が異常で終わるか止まったら、本体はその拡張機能の進み具合をすべて消す（他の表示のデータと同じ）。拡張機能は、終えずに止まる処理（取り消し、失敗）も、Failed を true にした end で終える。失敗の理由は message/show で知らせる。本体は、その拡張機能が後から届けるもの（editor/open、message/show、choice/ask）を出す client と同じ client の画面にだけ出す（その拡張機能へ最後に問い合わせを始めた client。問い合わせていなければ操作中の client）。分割したペインのどの client にも出すと、同じ進み具合が 2 回出るためである。古い本体（0.13.0 まで）は、知らない通知として捨てる（ログに残すだけ）。
- `sign/set`: 通知。params は `SignSetParams`。
- `statusline/set`: 通知。params は `StatusLineSetParams`。宣言ファイルの contributes.statusline で足した部品の中身を送る。本体は、部品の ID ごとに最後に届いた中身を持ち、利用者が設定の statusline か tabline に並べた部品だけを描く。並べていない部品の中身も持つので、拡張機能は設定を知らずに送ってよい。宣言ファイルで足していない部品の ID と、他の拡張機能が足した部品の ID の通知は捨てる。拡張機能が異常で終わるか止まったら、その拡張機能の部品の中身をすべて消す（他の表示のデータと同じ）。中身はバッファに依らず、どの client のどのバッファのステータス行にも同じものを出す（仮。lualine の mrreview の状態と、本体のメモリの使用量のような、エディタ全体の状態を出す部品を想定した）。
- `view/close`: 通知。params は `ViewCloseParams`。
- `view/update`: 通知。params は `ViewUpdateParams`。
<!-- generated:methods:end -->
