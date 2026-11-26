# vi-imitation

Vim のキーバインドを持つ、ターミナル用のモーダルエディタ。
本体は端末で動く。

- 要件定義: [docs/requirements.md](docs/requirements.md)
- 基本設計: [docs/basic-design.md](docs/basic-design.md)
- 要件ごとの進捗: [docs/progress.md](docs/progress.md)（`make progress` が生成する）
- Vim との違い（直さないと決めた食い違いと、見つけた未実装の機能）: [docs/vim-differences.md](docs/vim-differences.md)
- 設定と標準の拡張機能の使い方: [docs/usage.md](docs/usage.md)
- デバッグの案内（動かないとき、画面が崩れるときに調べる手順と、不具合の報告に添えるもの）: [docs/debugging.md](docs/debugging.md)

詳細設計の文書は作らず、コードと doc コメントを詳細設計として扱う。

## 必要なもの

- Go（バージョンは [go.mod](go.mod) を参照）
- make
- C コンパイラ（gcc など）。tree-sitter の拡張機能（`cmd/vi-imitation-ext-treesitter/`）が cgo で文法をコンパイルするので、`make check`、`make test`、`make build` に要る。本体は cgo を使わない。`make build` は tree-sitter の拡張機能をビルドしたマシンの glibc に動的にリンクする（NFR-01）
- git。tree-sitter の拡張機能のテストが、手元に作った git のリポジトリからパーサを取得してビルドする（FR-142）

配布物の利用者には、C コンパイラも Go も要らない。
設定に並べた tree-sitter のパーサを使うときだけ、git と C コンパイラが要る（[使い方](docs/usage.md)）。

リンターなどの開発ツールは、`make` が `go run` でバージョンを固定して実行するので、インストールは不要である。
初回の実行時だけ、ツールのビルドに数分かかる。

## 使い方

```sh
make build
bin/vi-imitation [ファイル ...]   # 最初のファイルを表示して起動する。ファイルを省くと空のバッファで起動する
bin/vi-imitation --log ed.log a.go # ログを ed.log に追記する（既定ではログを書かない）
bin/vi-imitation -R a.go           # 引数のファイルを、編集を禁止した状態で開く（:readonly で解ける）
bin/vi-imitation --version         # バージョン（ビルドしたコミット）を表示して終わる
bin/vi-imitation --docs            # 本体に埋め込んだ文書の話題の一覧を出して終わる
bin/vi-imitation --docs config     # 話題（設定ファイル）の文書を標準出力に出す
```

`--docs` は、設定、キーの割り当て、Ex コマンド、拡張機能の宣言ファイルと API の参照と、デバッグの案内を、ソースを取らずに読むための文書を出す（要件定義 FR-172）。
AI エージェントに設定や拡張機能を書かせるときは、要る話題だけを `vi-imitation --docs <話題>` で読ませる。
`vi-imitation --docs <話題> <節>` は、見出しが `<節>` の節だけを出す（`vi-imitation --docs usage 'Ex コマンドの一覧'`）。見出しの一覧は `vi-imitation --docs --headings <話題>` で出す。
端末を使わないので、標準出力をパイプやファイルにしてよい。
文書は `docs/agent/` と `docs/debugging.md` と `docs/usage.md` にある。

`vi` で起動したいときは、シェルの設定に `alias vi=vi-imitation` を書く。

標準入力と標準出力が端末のときだけ起動する。
端末でなければ、`vi-imitation: 標準入力と標準出力が端末のときだけ起動できる` と出して終了コード 1 で終わる。

起動するとノーマルモードになり、Vim と同じキーで編集する。
`:w` で保存し、`:q` で終わる。
変更を保存していなければ `:q` は `E37` のエラーを出すので、変更を捨てて終わるには `:q!` を打つ。
Vim との違いは [docs/vim-differences.md](docs/vim-differences.md) にある。

起動時に、設定ファイル（`$XDG_CONFIG_HOME/vi-imitation/config.jsonc`）と、拡張機能の宣言ファイル（`$XDG_DATA_HOME/vi-imitation/extensions/<名前>/manifest.json` と、本体の隣の `extensions/<名前>/manifest.json`）を読む。
置き場所と項目は仮で、`go doc ./internal/config` にある。
設定の誤りは、起動後にコマンドラインの行へ位置と内容を 1 行で出す（例: `config.jsonc:2:19: fixendofline は true か false にする`）。

`make build` は `bin/` に実行ファイルだけを作り、宣言ファイルを置かないので、`bin/vi-imitation` は標準の拡張機能を読まない（行のテキストオブジェクトの `dil` を打っても何も起きない）。
標準の拡張機能も使うときは、`make dist` で作る配布物の本体（`dist/vi-imitation-<コミット>-<OS>-<アーキテクチャ>/vi-imitation`）を起動する。
配布物では、宣言ファイルと実行ファイルが本体の隣の `extensions/<名前>/` に並ぶ。
設定ファイルの書き方、拡張機能のコマンドのキーへの割り当て、標準の拡張機能（LSP、DAP、ACP、MCP など）ごとのコマンドと使い方は [docs/usage.md](docs/usage.md) にある。
端末は xterm の制御シーケンスで制御する（代替画面、bracketed paste、24 ビット色、カーソルの形）。terminfo は読まない。
実際の端末で動きを確かめたのは、2026-11-16 時点で tmux 3.4 の中だけである。
F キーと Insert キーは、キーの型（`internal/editcmd`）に対応する種類が無いので、押しても何もしない。

## 開発

```sh
make help    # ターゲットの一覧
make hooks   # コミット前に make check を実行する Git フックを有効にする（clone 後に 1 回）
make check   # 整形の検査、型チェック、リント、テストをまとめて実行（CI と同じ内容）
```

| コマンド | 内容 |
|---|---|
| `make fmt` | コードを整形する（gofumpt と goimports） |
| `make fmt-check` | 整形されていないファイルがあれば失敗する |
| `make typecheck` | 全パッケージをコンパイルし、`go vet` を実行する |
| `make lint` | golangci-lint を実行する。設定は [.golangci.yml](.golangci.yml) |
| `make progress` | 要件ごとの進捗（`docs/progress.md`）を生成する。注釈の書き方は[基本設計](docs/basic-design.md)の「要件との対応」を参照 |
| `make docs` | 本体に埋め込む文書（`docs/agent/*.md`）の一覧の部分を、コードの表（設定、操作、Ex コマンド、拡張機能 API）から生成する。表を変えたら打つ |
| `make docs-check` | `docs/agent/*.md` の一覧がコードの表とずれていれば失敗する |
| `make test` | テストを実行する（データ競合の検出つき） |
| `make vuln` | 依存ライブラリの既知の脆弱性を調べる |
| `make build` | 実行ファイルを `bin/` に作る |
| `make build-check` | `make build` の後に、本体の `CGO_ENABLED=0` と全ての実行ファイルの静的リンクを検査する。本体の大きさはログに出すだけで、判定しない |
| `make dist` | 配布物（実行ファイルと宣言ファイル）を `dist/` にディレクトリと `tar.gz` で作る |
| `make dist-check` | `make dist` の後に、配布物の形を検査する |
| `make dist-smoke` | `make dist` の後に、ビルド用の道具の無いコンテナで配布物を起動する。Docker が要る |

Go はコンパイル言語なので、型チェックはコンパイルの一部として行われる。
`make typecheck` は、実行ファイルを残さずにコンパイルだけを行う。

## ディレクトリ構成

| パス | 内容 |
|---|---|
| `cmd/vi-imitation/` | エディタ本体の実行ファイルの入口 |
| `cmd/vi-imitation-mcp-bridge/` | MCP の中継コマンド。標準入出力と起動中のエディタの Unix ドメインソケットの間でメッセージを転送する |
| `cmd/vi-imitation-ext-line/` | 標準の拡張機能（行のテキストオブジェクト）の実行ファイルと宣言ファイル |
| `cmd/vi-imitation-ext-script/` | スクリプトの拡張機能（利用者が置く Go のソースファイル）を yaegi で実行する実行ファイル。宣言ファイルの `args` でスクリプトのパスを受け取る。スクリプトの書き方と yaegi の制限は [docs/usage.md](docs/usage.md) の「スクリプトの拡張機能」にあり、`vi-imitation --docs script` でも読める |
| `cmd/vi-imitation-ext-treesitter/` | 標準の拡張機能（tree-sitter による構文ハイライト）の実行ファイルと宣言ファイル。cgo を使い、ビルドに C コンパイラが要る |
| `cmd/vi-imitation-ext-dap/` | 標準の拡張機能（DAP によるデバッグ）の実行ファイルと宣言ファイル。デバッグアダプター（既定は delve の `dlv dap`）を子プロセスとして起動する |
| `cmd/vi-imitation-ext-lsp/` | 標準の拡張機能（LSP のクライアント。診断、定義へ移動、ホバー、整形）の実行ファイルと宣言ファイル。言語サーバー（既定は Go の gopls）を子プロセスとして起動する |
| `cmd/vi-imitation-ext-acp/` | 標準の拡張機能（ACP のクライアント。プロンプトの送信、返答の表示、権限の確認、エディタの内容を通したファイルの読み書き）の実行ファイルと宣言ファイル。設定に書いたコーディングエージェントを子プロセスとして起動する |
| `cmd/vi-imitation-ext-mcp/` | 標準の拡張機能（MCP のサーバー。バッファの一覧と内容、カーソル位置、選択範囲、診断の取得、編集、ファイルを開く、保存のツール）の実行ファイルと宣言ファイル。中継コマンドからの接続を Unix ドメインソケットで受ける。本体が環境変数 `VI_IMITATION_MCP_SOCKET` のパスで起動時から待ち受け、最初の接続で起動してソケットを引き継ぐ（起動の契機 `socket`）。使い方はパッケージの doc（`go doc ./cmd/vi-imitation-ext-mcp`）にある |
| `cmd/vi-imitation-ext-git/` | 標準の拡張機能（git 連携。バッファのファイルを git の HEAD の版と見比べる）の実行ファイルと宣言ファイル。PATH の `git` を子プロセスとして起動する |
| `cmd/reqtrace/` | 要件ごとの進捗を生成するツール |
| `cmd/docgen/` | 本体に埋め込む文書（`docs/agent/*.md`）の一覧の部分を生成するツール（`make docs`） |
| `internal/distcheck/` | `make build` と `make dist` の生成物を検査するテスト |
| `internal/` | このリポジトリの中だけで使うパッケージ |
| `docs/` | 要件定義などの文書 |
| `docs/agent/` | 本体に埋め込み、`vi-imitation --docs` で出す AI エージェント向けの文書 |
| `docs/measurements/` | 計測の手順と結果 |
