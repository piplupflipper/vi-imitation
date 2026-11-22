# 設定と標準の拡張機能の使い方

設定ファイルの書き方と、配布物に入っている標準の拡張機能の使い方を書く。
本体の起動と基本の操作は [README](../README.md) の「使い方」にある。

この文書の手順は、2026-11-16 に `make dist` で作った配布物の本体を tmux 3.4 の中で起動し、上から順にたどって確かめた。
外部のプログラムは、gopls v0.24.0、Delve 1.27.2、opencode 1.19.4（`opencode acp`）、Claude Code 2.1.325 を使った。
設定の項目の名前とコマンドの名前は、どれも仮である（基本設計 7 章、6.7）。

## 配布物の本体を起動する

標準の拡張機能は、`make dist` が作る配布物の本体からだけ使える。
`make build` の `bin/vi-imitation` は宣言ファイルを読まないので、標準の拡張機能が動かない（README の「使い方」）。

```sh
make dist
dist/vi-imitation-<コミット>-<OS>-<アーキテクチャ>/vi-imitation main.go
```

配布物のディレクトリは、本体、MCP の中継コマンド（`vi-imitation-mcp-bridge`）、スクリプトの拡張機能を動かす実行ファイル（`vi-imitation-ext-script`）と、標準の拡張機能ごとのディレクトリ（`extensions/<ID>/`）と、herdr の同梱のプラグイン（`herdr-plugin/`。[herdr のプラグインを登録する](#herdr-のプラグインを登録する)）からなる。
本体は、自分の実行ファイルの隣の `extensions/` から宣言ファイルを探す（`internal/config` の `ExtensionDirs`）。

本体と、tree-sitter 以外の標準の拡張機能は静的にリンクした実行ファイルで、C の標準ライブラリの種類と版に依らずに動く。
tree-sitter の拡張機能（`extensions/treesitter/`）だけは glibc に動的にリンクしている（要件定義の NFR-01）。
2026-11-20 に Ubuntu の glibc 2.43 の上で作った配布物では、この拡張機能が要求する glibc のシンボルの版は `GLIBC_2.34` までだった（`objdump -T`）。
古い glibc（AlmaLinux 8 の 2.28 など）と musl（Alpine など）の環境では、この拡張機能が起動せず、構文の色付けと tree-sitter のテキストオブジェクトを使えない。本体とほかの拡張機能は動く。

### 標準入力から読んだ中身を編集する

Vim と同じく、起動の引数に `-` を書くと、標準入力から読んだ中身を名前の無いバッファで開く（要件定義 FR-158）。

```sh
git log --oneline | vi-imitation -
printf 'hoge\nあいう\n' | vi-imitation -R -
```

- 標準入力を終わりまで読んでから画面を開く。読む間は「vi-imitation: 標準入力から読んでいます...」を 1 行出す。読み終えた後のキーは、端末（`/dev/tty`）から読む
- 読んだバッファは未保存の変更がある扱いで、`:q` は `E37` で断られる。`:w ファイル` で名前を付けて保存するか、`:q!` で捨てて閉じる。`:set filetype=` でファイルの種類を決められる
- `-R` と一緒に書くと、編集を禁止して開き、変更の無い扱いにする（`:q` で閉じられる）。空の入力も変更の無い扱いにする
- 末尾の改行の有り無しと正しくない UTF-8 のバイトは、ファイルを開くときと同じく扱う
- 読む間に `<C-c>` を打つと、読んだ中身を捨て、空のバッファで開く（Vim と同じ）
- tmux と herdr の中では、ファイルを開くときと同じく、同じ window の server のタブとして開く。`:!` の中で起動したときは、`:!` を打った client のタブに開き、そのタブを閉じるまで待つ
- `-` は、ほかのファイルと一緒に書けない。名前が `-` のファイルは `./-` と書く
- 標準出力が端末でないとき（`vi-imitation - > out`）は、標準入力を読まずに誤りで終わる。`-` を書かずに標準入力をパイプにしたときも、今までどおり誤りで終わる

## 設定ファイル

設定ファイルは `$XDG_CONFIG_HOME/vi-imitation/config.jsonc` に置く。
`XDG_CONFIG_HOME` が空なら `~/.config/vi-imitation/config.jsonc` である。
ファイルは JSONC（コメントと末尾のカンマを許す JSON）で書く。
ファイルが無ければ、すべての項目が既定値になる。
設定を変えたら、本体を起動し直す。

### keymap の書き方

次の例は、F キーの代わりに使うキーを LSP と DAP の操作に割り当て、ACP のエージェントに opencode を使う設定である。
LSP と DAP の操作には既定のキー（[LSP と DAP の既定のキー](#lsp-と-dap-の既定のキー)）があり、この例のキーは足したものである。

```jsonc
{
  // 拡張機能のコマンドと LSP と DAP の操作は、Ex コマンドと同じく : で呼べる
  "keymap": {
    "normal": {
      "<Space>h": ":hover<CR>",
      "<Space>c": ":debug.continue<CR>",
      // 右辺をオブジェクトにすると、キーの列ではなく本体の操作を ID で割り当てる
      "<Space>H": { "action": "hover" },
      // command には正規の Ex コマンドの名前を、args にはその引数を書く。コマンドラインを出さずに実行する
      "<C-l>": { "command": "search.clearHighlight" },
      "<Space>n": { "command": "option.toggle", "args": "lineNumber.show" },
    },
    "insert": {
      "<C-a>": { "action": "insert.line-start" },
      "<C-e>": { "action": "insert.line-end" },
    },
  },
  "extensions": {
    "settings": {
      "acp": {
        "agent": { "command": "opencode", "args": ["acp"] },
      },
    },
  },
}
```

`keymap` は、モードの名前から、キーの列とその割り当ての対応である。
モードの名前は `normal`、`visual`、`insert`、`commandLine` で、この文書が確かめたのは `normal` と `insert` である（`insert` は 2026-11-19 に同梱の `config.example.jsonc` の Emacs 風のキーで確かめた）。
右辺には、キーの列か、本体の操作（[操作の ID](#操作の-id)）か、Ex コマンド（[keymap から Ex コマンドを呼ぶ](#keymap-から-ex-コマンドを呼ぶ)）を書く。
キーの列は、Vim の `:map` の右辺と同じく展開先のキーの列である。
展開した列は、もう一度 `keymap` の文字列の項目で展開しない（Vim の `noremap` と同じ）。
ただし、展開した列は、`keymap` で操作を割り当てた後のキーとして解釈する。`"x": { "action": "line-end" }` と `"<C-x>": "x"` を書くと、`<C-x>` は行末へ移る（Vim の `nnoremap <C-x> x` は本体の `x` を使うので、この点が違う）。
`":hover<CR>"` は、`:` でコマンドラインに入り、`hover` を打って `<CR>` で実行するキーの列である。
`{ "action": "hover" }` は、ホバーの操作そのものを割り当てる（FR-126）。
`{ "command": "search.clearHighlight" }` は、コマンドラインを出さずに `:search.clearHighlight` を実行する（FR-154）。

Ex コマンドの別名（`commandAliases`）の書き方は、[Ex コマンドの別名を足す](#ex-コマンドの別名を足す)にある。

### 設定ファイルの項目

設定ファイルの最上位に書ける項目は次のとおりである。
このほかに、[設定の名前の一覧](#設定の名前の一覧)の項目（`filetype` を除く）を、点で区切った名前を入れ子のキーにして書ける（`"lineNumber": { "show": true }`）。
範囲がバッファごとの項目を書くと、バッファを開くときの初めの値になる（[字下げとタブの幅](#字下げとタブの幅)）。
知らない項目は、誤りとして知らせて無視する。

| 項目 | 内容 |
|---|---|
| `keymap` | モードごとのキーの割り当て（この節の上の例。[操作の ID](#操作の-id)、[keymap から Ex コマンドを呼ぶ](#keymap-から-ex-コマンドを呼ぶ)） |
| `leader` | キーの表記の `<Leader>` に当たるキー。既定は `<Space>`（[`<Leader>` と `leader`](#leader-と-leader)） |
| `fixendofline` | 書き出すときに、最後の行の後に改行が無ければ補うか。既定は `true`（Vim の `'fixendofline'` と同じ） |
| `autoread` | 未変更のバッファのファイルが外で変わったときに、問わずに読み直すか。既定は `true`（Vim の `'autoread'` と同じ） |
| `checkIntervalMs` | 開いているバッファのファイルが外で変わっていないかを確かめる間隔のミリ秒。既定は `1000`。`0` は定期的に確かめない |
| `fileTypes` | 拡張子（`.tmpl`）かファイル名（`Justfile`）から、ファイルの種類の名前への対応 |
| `extensions` | 拡張機能の設定（`settings`）、起動の契機の追加（`activation`）、取得する拡張機能（`install`。[拡張機能の取得](#拡張機能の取得ext-sync)）、問い合わせの期限（`syncTimeoutMs`、`startSyncTimeoutMs`）、保存の前の処理（整形）の期限（`willSaveTimeoutMs`。既定は `1000`。[整形ツール](#整形ツール)）、メモリの上限（`memoryLimitMb`）、使わない拡張機能（`disabled`。[拡張機能を使わない](#拡張機能を使わない)） |
| `registers` | レジスターの履歴の件数（`historySize`）、1 件の最大のバイト数（`maxEntryBytes`）、削除を履歴に積むか（`deleteToHistory`）、ホストのクリップボードへ送る手段（`clipboard`）（[レジスターと履歴](#レジスターと履歴)） |
| `readonlyPaths` | 開いた時点で編集を禁止にするファイルの置き場所（[編集の禁止](#編集の禁止)） |
| `colorscheme` | 使う配色の名前（[配色の選択](#配色の選択)） |
| `colors` | 色の名前ごとの色（[配色](#配色)） |
| `symbols` | 印と枠線の記号（[記号](#記号)） |
| `icons` | ファイルの種類のアイコンの表と、出すか（`show`）（[アイコン](#アイコン)） |
| `statusline` | ステータス行に並べる部品（[ステータス行の並び](#ステータス行の並び)） |
| `tabline` | タブの行を出す場合と、並べる部品（[タブの行](#タブの行)） |
| `elevateCommand` | 権限の足りないファイルを書くときに使うコマンド。既定は `["sudo"]`（[権限の足りないファイルの保存](#権限の足りないファイルの保存)） |
| `commandAliases` | Ex コマンドの別名（[Ex コマンドの別名を足す](#ex-コマンドの別名を足す)） |
| `fileTypeOptions` | ファイルの種類ごとの、範囲がバッファごとの設定の項目の値（[字下げとタブの幅](#字下げとタブの幅)） |

### 操作の ID

`keymap` の右辺に `{ "action": "<ID>" }` と書くと、キーの列に展開せずに、本体の操作をそのキーに割り当てる（FR-126）。
同じモードの同じキーに本体の既定の割り当てがあれば、設定の割り当てが勝つ（`"insert": { "<BS>": { "action": "insert.delete-char" } }` と書くと、`<BS>` はカーソルの文字を消す）。
書ける ID は、下の[操作の ID の一覧](#操作の-id-の一覧)にあるものだけである。
拡張機能が宣言したコマンドの ID は書けない。拡張機能のコマンドは、`command`（[keymap から Ex コマンドを呼ぶ](#keymap-から-ex-コマンドを呼ぶ)）で割り当てる。
オブジェクトに書ける項目は `action` と、Ex コマンドを書く `command` と `args` だけで、他の項目を書くと誤りになる。

ノーマルモードの既定の `Y` は行末までのヤンク（`yank-to-line-end`。`y$`）、`U` はリドゥ（`redo`。`<C-r>` と同じ）で、Vim と違う（要件定義 FR-145、FR-146）。
Vim と同じく `Y` で行をヤンクするには、`"normal": { "Y": { "action": "yank-line" } }` と書く。
ビジュアルモードの `U`（選択を大文字にする）と `Y`（選んだ行をヤンクする）は Vim と同じである。

ID は、名前を変えずに保つ（利用者の設定が壊れるため）。
回数、レジスター、マクロ、ドットリピート（`3`、`"a`、`q`、`@`、`.`）には ID が無く、割り当てられない。

割り当てられるモードは、一覧の「モード」の列にあるものである。
`commandLine` には操作を書けない。
`normal` に割り当てたモーションは、オペレーターの後（`d` の後など）には効かない（Vim の `nmap` と同じ）。

インサートモードの割り当ての左辺は、1 つのキーにする。
修飾キーの付かない文字、`<Esc>`、`<CR>`、`<Tab>` は左辺に書けない。
これらのキーは、1 文字の入力を速く保つために操作の表を通さず、今までどおり文字の入力とモードの切り替えに使う。
`jk` で抜けるようなキーの列は、右辺をキーの列で書く（`"insert": { "jk": "<Esc>" }`）。
同梱の `config.example.jsonc` には、インサートモードの Emacs 風のキー（`<C-a>`、`<C-e>`、`<C-f>`、`<C-b>`、`<C-n>`、`<C-p>`、`<C-d>`、`<C-k>`、`<C-w>`、`<C-u>`）の例がある。
`<C-w>` はノーマルモードではウィンドウの操作の前置キーだが、`keymap.insert` の割り当てはインサートモードでだけ効くので、ノーマルモードの `<C-w>` は変わらない。
Ctrl-H は、端末からも tmux の中でも `^H`（0x08）で届き、本体は `<BS>` と読む（本体は、Ctrl-H を区別して送る端末の機能を有効にしない）。
このため `<C-h>` に操作を割り当てても効かず、Ctrl-H は `<BS>` の割り当てのとおりに働く。既定では `insert.delete-prev-char` で前の文字を消す。
`<BS>` を別の操作に割り当てると（`"<BS>": { "action": "insert.delete-char" }`）、Ctrl-H も同じ操作に変わる。
tmux の prefix が既定の `C-b` のままなら、Ctrl-B は tmux が受け取るので、例の `<C-b>`（`insert.left`）は本体に届かない。

左辺の表記が違っても同じキーになる項目（`<Leader>w` と `<Space>w`）を、キーの列と操作の両方に書くと、キーの列の項目を使い、誤りとして知らせる。
知らない ID、そのモードで使えない操作、書けない左辺も、誤りとして位置と内容を知らせ、その項目だけを使わない。

### 操作の ID の一覧

次の表は、本体の操作の表から作る（`internal/input` のテストが照合する）。
ID は一意で、1 つの ID は 1 つの操作を指す（`0` と `<Home>` のように、同じ操作を複数のキーに載せているものはある）。
「モード」は、`keymap` のどのモードに割り当てられるかである。

#### ID の書き方

ID の書き方は、今は次のとおりである。

- 本体の操作は、小文字の英単語を `-` でつなぐ（`line-start`、`page-down`）
- LSP と DAP の操作は、Ex コマンドの名前と同じ（`typeDefinition`、`debug.stepOver`）。語のつなぎは `-` ではなく大文字（camelCase）である
- `.` は、前の部分で分類を表す。`peek.`（ポップアップで見る）、`diagnostic.`（診断）、`debug.` と `breakpoint.`（DAP）、`insert.`（インサートモードの操作）、`preview.`（[項目のプレビュー](#項目のプレビュー)のスクロール）がある
- `preview.` で始まる ID は、ノーマルモードとインサートモードの両方に割り当てられる（ファインダーの入力の行はインサートモード、ファイラの木はノーマルモードのため）
- `insert.` で始まる ID はインサートモードの操作である。ID `insert`（`.` が無いもの）は、ノーマルモードの `i`（カーソルの前から入力を始める）で、インサートモードの操作ではない
- ノーマルモードの `~`（カーソルの文字を入れ替える）は `toggle-case-char`、オペレーターの `g~` は `toggle-case` である

#### インサートモードの操作

インサートモードの操作（`insert.` で始まるもの）の既定のキーは、一覧の順に `<Left>`、`<Right>`、`<Up>`、`<Down>`、`<Home>`、`<End>`、`<PageUp>`、`<PageDown>`、`<BS>`、`<Del>` である。
`insert.delete-to-line-end` は既定のキーが無く、行末の位置で使うと次の行をつなげる（Emacs の `C-k`）。
`insert.delete-prev-word` と `insert.delete-to-line-start` も既定のキーが無い（同梱の `config.example.jsonc` では `<C-w>` と `<C-u>` に割り当てている）。
`insert.delete-prev-word` は、ノーマルモードの `db` と同じ単語の区切りで、カーソルの前の空白とその前の単語を消す。
行の先頭で使うと `<BS>` と同じく前の行とつなげ、それ以外では行をまたいで消さない。
`insert.delete-to-line-start` は、カーソルから行の先頭までを、字下げも含めて消す。行の先頭では何もしない。
どちらも、このインサートで入力した文字より前の文字も消す（Vim の `<C-w>` と `<C-u>` との違いは [vim-differences.md](vim-differences.md) にある）。
インサートモードで消した文字は、`<BS>` と同じくレジスターにも履歴にも入らない。

#### select-all

`select-all` は、文字単位のビジュアルモードでバッファの全体（最初の行の最初の文字から、最後の行の最後の文字まで）を選ぶ。
既定のキーは、ノーマルモードとビジュアルモードの `<Leader>a`（`leader` が既定なら `<Space>a`）である。
別のキーにも割り当てるには、次のように書く。

```jsonc
{
  "keymap": {
    "normal": {
      "<C-a>": { "action": "select-all" },
    },
  },
}
```

Vim の `ggVG` と違い、カーソルは動かず、画面もスクロールせず、ジャンプリストにも記録しない（Windows の Ctrl+A に近い）。
続けて `y` を打つと全体をヤンクし、カーソルは `<Leader>a` を打った位置のままである（ビジュアルモードの `y` はカーソルを動かさない。[vim-differences.md](vim-differences.md)）。
ヤンクは履歴の先頭に積むので、`clipboard` の設定（既定は `osc52`）のとおりにホストのクリップボードへも送る（[ホストのクリップボードへの送り出し](#ホストのクリップボードへの送り出し)）。
最後の行の改行は選ばないので、`y` で取ったテキストの最後に改行は付かない（Vim の `ggvG$y` と同じ）。
その後のキーは、普段のビジュアルモードと同じ規則で働く。

- `d`、`x`、`c` は全体を消す（`c` はその後インサートモードに入る）
- `>`、`<` は全体をずらし、選択とカーソルの位置を保つ。選択の両端は、ずらす前の行とバイトの位置に戻る（Vim の `>gv` と同じ）。このため `>` を重ねると、2 回目から字下げ 1 段ごとに最後の行の最後の文字が 1 文字ずつ選択から外れる（字下げはすべての行に入る）
- `j`、`w` などのモーションは、カーソルから動く。選択は 1 行目の最初の文字から動いた先までになる
- `o` は、カーソルを 1 行目の最初の文字へ移す
- `<Esc>` はノーマルモードへ戻る。カーソルは元の位置のままである
- `:` は、`'<,'>` がバッファの全体を指す

既定の `<Leader>a` は、ビジュアルモードの中でも `select-all` である。
行単位のビジュアルモードの中で使ったときは、文字単位のビジュアルモードに切り替えて全体を選ぶ。
上の例の `<C-a>` は `keymap.normal` にだけ割り当てているので、ビジュアルモードの中の `<C-a>` は `select-all` にならない。
ビジュアルモードの中でも使うには、`keymap.visual` にも同じ行を書く。

`select-all` の後に拡張機能がバッファを書き換えたとき（MCP の `edit_buffer`、ACP の書き込みなど）は、選択の両端がそれぞれ編集に合わせてずれる。
バッファの先頭に足した行は選択の外になり、途中に足した行は選択の中になる。

#### 操作の ID の表

<!-- action-ids:begin -->
| ID | 種類 | モード | 説明 |
|---|---|---|---|
| `left` | モーション | `normal`、`visual` | 左へ |
| `down` | モーション | `normal`、`visual` | 下の行へ |
| `up` | モーション | `normal`、`visual` | 上の行へ |
| `right` | モーション | `normal`、`visual` | 右へ |
| `display-down` | モーション | `normal`、`visual` | 見た目の行で 1 行下へ（行を折り返さないときは j と同じ） |
| `display-up` | モーション | `normal`、`visual` | 見た目の行で 1 行上へ（行を折り返さないときは k と同じ） |
| `word-next` | モーション | `normal`、`visual` | 次の単語の先頭へ |
| `word-prev` | モーション | `normal`、`visual` | 前の単語の先頭へ |
| `word-end` | モーション | `normal`、`visual` | 単語の末尾へ |
| `bigword-next` | モーション | `normal`、`visual` | 次の空白区切りの単語の先頭へ |
| `bigword-prev` | モーション | `normal`、`visual` | 前の空白区切りの単語の先頭へ |
| `bigword-end` | モーション | `normal`、`visual` | 空白区切りの単語の末尾へ |
| `line-start` | モーション | `normal`、`visual` | 行の先頭へ |
| `line-first-nonblank` | モーション | `normal`、`visual` | 行の最初の空白でない文字へ |
| `line-end` | モーション | `normal`、`visual` | 行の末尾へ |
| `file-start` | モーション | `normal`、`visual` | 最初の行へ（回数があればその行へ） |
| `file-end` | モーション | `normal`、`visual` | 最後の行へ（回数があればその行へ） |
| `find-forward` | モーション | `normal`、`visual` | 行の中で右の {文字} へ |
| `find-backward` | モーション | `normal`、`visual` | 行の中で左の {文字} へ |
| `till-forward` | モーション | `normal`、`visual` | 行の中で右の {文字} の手前へ |
| `till-backward` | モーション | `normal`、`visual` | 行の中で左の {文字} の手前へ |
| `find-repeat` | モーション | `normal`、`visual` | 直前の f、F、t、T を繰り返す |
| `find-repeat-reverse` | モーション | `normal`、`visual` | 直前の f、F、t、T を逆向きに繰り返す |
| `match-pair` | モーション | `normal`、`visual` | 対応する括弧へ |
| `paragraph-prev` | モーション | `normal`、`visual` | 前の段落へ |
| `paragraph-next` | モーション | `normal`、`visual` | 次の段落へ |
| `search-forward` | モーション | `normal`、`visual` | 前方へ検索する |
| `search-backward` | モーション | `normal`、`visual` | 後方へ検索する |
| `search-next` | モーション | `normal`、`visual` | 直前の検索を同じ向きに繰り返す |
| `search-prev` | モーション | `normal`、`visual` | 直前の検索を逆向きに繰り返す |
| `delete` | オペレーター | `normal`、`visual` | 消す |
| `change` | オペレーター | `normal`、`visual` | 消して入力する |
| `yank` | オペレーター | `normal`、`visual` | ヤンクする |
| `toggle-case` | オペレーター | `normal`、`visual` | 大文字と小文字を入れ替える |
| `lowercase` | オペレーター | `normal`、`visual` | 小文字にする |
| `uppercase` | オペレーター | `normal`、`visual` | 大文字にする |
| `comment` | オペレーター | `normal`、`visual` | 行のコメントを切り替える |
| `surround-add` | オペレーター | `normal` | 対象を {文字} で囲む（sa{対象}{文字}） |
| `indent` | オペレーター | `visual` | 選んだ行を右へずらす（選択は残す） |
| `dedent` | オペレーター | `visual` | 選んだ行を左へずらす（選択は残す） |
| `delete-lines` | オペレーター | `visual` | 選んだ行を消す |
| `change-lines` | オペレーター | `visual` | 選んだ行を消して入力する |
| `yank-lines` | オペレーター | `visual` | 選んだ行をヤンクする |
| `inner-word` | テキストオブジェクト | `visual` | 単語 |
| `a-word` | テキストオブジェクト | `visual` | 単語と後ろの空白 |
| `inner-bigword` | テキストオブジェクト | `visual` | 空白区切りの単語 |
| `a-bigword` | テキストオブジェクト | `visual` | 空白区切りの単語と後ろの空白 |
| `inner-double-quote` | テキストオブジェクト | `visual` | "…" の中 |
| `a-double-quote` | テキストオブジェクト | `visual` | "…" の全体 |
| `inner-single-quote` | テキストオブジェクト | `visual` | '…' の中 |
| `a-single-quote` | テキストオブジェクト | `visual` | '…' の全体 |
| `inner-paren` | テキストオブジェクト | `visual` | (…) の中 |
| `a-paren` | テキストオブジェクト | `visual` | (…) の全体 |
| `inner-bracket` | テキストオブジェクト | `visual` | […] の中 |
| `a-bracket` | テキストオブジェクト | `visual` | […] の全体 |
| `inner-brace` | テキストオブジェクト | `visual` | {…} の中 |
| `a-brace` | テキストオブジェクト | `visual` | {…} の全体 |
| `inner-paragraph` | テキストオブジェクト | `visual` | 段落 |
| `a-paragraph` | テキストオブジェクト | `visual` | 段落と後ろの空行 |
| `commented-lines` | テキストオブジェクト | `visual` | 続くコメントの行 |
| `toggle-case-char` | 操作 | `normal` | カーソルの文字の大文字と小文字を入れ替える |
| `put-after` | 操作 | `normal` | カーソルの後に貼る |
| `put-before` | 操作 | `normal` | カーソルの前に貼る |
| `delete-char` | 操作 | `normal` | カーソルの文字を消す |
| `delete-char-before` | 操作 | `normal` | カーソルの前の文字を消す |
| `replace-char` | 操作 | `normal` | カーソルの文字を {文字} に置き換える |
| `delete-to-line-end` | 操作 | `normal` | 行の末尾まで消す（d$） |
| `change-to-line-end` | 操作 | `normal` | 行の末尾まで消して入力する（c$） |
| `substitute-line` | 操作 | `normal` | 行を消して入力する（cc） |
| `yank-to-line-end` | 操作 | `normal` | 行の末尾までヤンクする（y$） |
| `yank-line` | 操作 | `normal` | 行をヤンクする（yy） |
| `indent-line` | 操作 | `normal` | 行を右へずらす（>>。回数は行数） |
| `dedent-line` | 操作 | `normal` | 行を左へずらす（<<。回数は行数） |
| `surround-delete` | 操作 | `normal` | {文字} の囲みを消す（sd{文字}） |
| `surround-replace` | 操作 | `normal` | 囲みを替える（sr{前の文字}{後の文字}） |
| `comment-line` | 操作 | `normal` | 行のコメントを切り替える（gcc。回数は行数） |
| `join-lines` | 操作 | `normal` | 行をつなげる |
| `open-below` | 操作 | `normal` | 下に行を足して入力する |
| `open-above` | 操作 | `normal` | 上に行を足して入力する |
| `insert` | 操作 | `normal` | カーソルの前から入力する |
| `append` | 操作 | `normal` | カーソルの後から入力する |
| `insert-line-start` | 操作 | `normal`、`visual` | 行の最初の空白でない文字の前から入力する（ビジュアルモードでは選んだ行ごと） |
| `append-line-end` | 操作 | `normal`、`visual` | 行の末尾から入力する（ビジュアルモードでは選んだ行ごと） |
| `undo` | 操作 | `normal` | アンドゥ |
| `redo` | 操作 | `normal` | リドゥ |
| `command-line` | 操作 | `normal`、`visual` | コマンドラインに入る |
| `visual` | 操作 | `normal`、`visual` | 文字単位のビジュアルモード |
| `visual-line` | 操作 | `normal`、`visual` | 行単位のビジュアルモード |
| `visual-swap` | 操作 | `visual` | 選択の反対の端へ移る |
| `select-all` | 操作 | `normal`、`visual` | カーソルを動かさずにバッファの全体を選ぶ |
| `page-down` | 操作 | `normal`、`visual` | 1 画面下へスクロールする |
| `page-up` | 操作 | `normal`、`visual` | 1 画面上へスクロールする |
| `half-page-down` | 操作 | `normal`、`visual` | 半画面（回数があれば回数の行）下へスクロールし、カーソルも同じ行数動かす |
| `half-page-up` | 操作 | `normal`、`visual` | 半画面（回数があれば回数の行）上へスクロールし、カーソルも同じ行数動かす |
| `scroll-line-down` | 操作 | `normal`、`visual` | 表示領域を 1 行（回数の行）下へスクロールする |
| `scroll-line-up` | 操作 | `normal`、`visual` | 表示領域を 1 行（回数の行）上へスクロールする |
| `scroll-cursor-top` | 操作 | `normal`、`visual` | カーソルの行（回数があればその行）を画面の上の端に置く |
| `scroll-cursor-top-first-nonblank` | 操作 | `normal`、`visual` | zt の後に、行の最初の空白でない文字へ移る |
| `scroll-cursor-center` | 操作 | `normal`、`visual` | カーソルの行（回数があればその行）を画面の中ほどに置く |
| `scroll-cursor-center-first-nonblank` | 操作 | `normal`、`visual` | zz の後に、行の最初の空白でない文字へ移る |
| `scroll-cursor-bottom` | 操作 | `normal`、`visual` | カーソルの行（回数があればその行）を画面の下の端に置く |
| `scroll-cursor-bottom-first-nonblank` | 操作 | `normal`、`visual` | zb の後に、行の最初の空白でない文字へ移る |
| `output-next` | 操作 | `normal` | 出力の欄を開く、次の欄へ切り替える |
| `output-close` | 操作 | `normal` | 出力の欄を閉じる |
| `output-scroll-up` | 操作 | `normal` | 出力の欄を上へスクロールする |
| `output-scroll-down` | 操作 | `normal` | 出力の欄を下へスクロールする |
| `popup-focus` | 操作 | `normal` | ポップアップへフォーカスを入れる、元のタブへ戻る |
| `jump-older` | 操作 | `normal` | ジャンプリストを戻る |
| `jump-newer` | 操作 | `normal` | ジャンプリストを進む |
| `alternate-buffer` | 操作 | `normal` | 裏のタブへ移る |
| `tab-next` | 操作 | `normal` | 次のタブへ移る |
| `tab-prev` | 操作 | `normal` | 前のタブへ移る |
| `put-older` | 操作 | `normal` | 貼った直後に、履歴の古い項目に貼り直す |
| `put-newer` | 操作 | `normal` | 貼った直後に、履歴の新しい項目に貼り直す |
| `diff-next` | 操作 | `normal` | 見比べで、次の差分へ |
| `diff-prev` | 操作 | `normal` | 見比べで、前の差分へ |
| `search-word-forward` | 操作 | `normal`、`visual` | カーソルの単語を前方への検索のパターンにする（移らない） |
| `search-word-backward` | 操作 | `normal`、`visual` | カーソルの単語を後方への検索のパターンにする（移らない） |
| `mouse.click` | 操作 | `normal`、`visual`、`insert` | マウスの位置へ移る（タブの行ならそのタブへ、スクロールバーの印ならその行へ移り、つまみならドラッグを始める） |
| `mouse.drag` | 操作 | `normal`、`visual`、`insert` | ボタンを押した位置からマウスの位置までを選ぶ（スクロールバーのつまみなら表示領域を動かす） |
| `mouse.release` | 操作 | `normal`、`visual`、`insert` | ドラッグを終える（つまみの上の印を動かさずに離したら、その行へ移る） |
| `mouse.select-word` | 操作 | `normal`、`visual`、`insert` | マウスの位置の単語を選ぶ |
| `mouse.select-line` | 操作 | `normal`、`visual`、`insert` | マウスの位置の行を選ぶ |
| `mouse.scroll-down` | 操作 | `normal`、`visual`、`insert` | 表示領域を mouse.scrollLines の行だけ下へ送る |
| `mouse.scroll-up` | 操作 | `normal`、`visual`、`insert` | 表示領域を mouse.scrollLines の行だけ上へ送る |
| `mouse.scroll-right` | 操作 | `normal`、`visual`、`insert` | 行を折り返さないとき、表示領域を mouse.scrollColumns の列だけ右へ送る |
| `mouse.scroll-left` | 操作 | `normal`、`visual`、`insert` | 行を折り返さないとき、表示領域を mouse.scrollColumns の列だけ左へ送る |
| `insert.left` | 操作 | `insert` | 左へ |
| `insert.right` | 操作 | `insert` | 右へ |
| `insert.up` | 操作 | `insert` | 上の行へ |
| `insert.down` | 操作 | `insert` | 下の行へ |
| `insert.line-start` | 操作 | `insert` | 行の先頭へ |
| `insert.line-end` | 操作 | `insert` | 行末の位置へ |
| `insert.page-up` | 操作 | `insert` | 1 画面上へスクロールする |
| `insert.page-down` | 操作 | `insert` | 1 画面下へスクロールする |
| `insert.delete-prev-char` | 操作 | `insert` | 前の文字を消す |
| `insert.delete-char` | 操作 | `insert` | カーソルの文字を消す |
| `insert.delete-to-line-end` | 操作 | `insert` | カーソルから行末まで消す（行末では次の行をつなげる） |
| `insert.delete-prev-word` | 操作 | `insert` | 前の単語を消す（行の先頭では前の行とつなげる） |
| `insert.delete-to-line-start` | 操作 | `insert` | カーソルから行の先頭まで消す |
| `hover` | 操作 | `normal` | ホバー（LSP） |
| `peek.definition` | 操作 | `normal` | 定義をポップアップで見る（LSP） |
| `definition` | 操作 | `normal` | 定義へ移る（LSP） |
| `peek.implementation` | 操作 | `normal` | 実装をポップアップで見る（LSP） |
| `implementation` | 操作 | `normal` | 実装へ移る（LSP） |
| `peek.typeDefinition` | 操作 | `normal` | 型の定義をポップアップで見る（LSP） |
| `typeDefinition` | 操作 | `normal` | 型の定義へ移る（LSP） |
| `references` | 操作 | `normal` | 参照の一覧（LSP） |
| `rename` | 操作 | `normal` | 名前を変える（LSP） |
| `codeAction` | 操作 | `normal` | コードアクション（LSP） |
| `documentSymbols` | 操作 | `normal` | ファイルのシンボルの一覧（LSP） |
| `workspaceSymbols` | 操作 | `normal` | ワークスペースのシンボルの一覧（LSP） |
| `diagnostic.list` | 操作 | `normal` | ファイルの診断の一覧 |
| `diagnostic.listAll` | 操作 | `normal` | ワークスペースの診断の一覧 |
| `diagnostic.next` | 操作 | `normal` | 次の診断へ移る |
| `diagnostic.prev` | 操作 | `normal` | 前の診断へ移る |
| `format` | オペレーター | `normal`、`visual` | 整形する（LSP） |
| `debug.continue` | 操作 | `normal` | デバッグを始める、続行する（DAP） |
| `debug.pause` | 操作 | `normal` | 一時停止する（DAP） |
| `debug.terminate` | 操作 | `normal` | デバッグを終える（DAP） |
| `debug.restart` | 操作 | `normal` | デバッグをやり直す（DAP） |
| `breakpoint.toggle` | 操作 | `normal` | ブレークポイントを切り替える（DAP） |
| `breakpoint.conditional` | 操作 | `normal` | 条件付きのブレークポイントを置く（DAP） |
| `breakpoint.clear` | 操作 | `normal` | ブレークポイントをすべて消す（DAP） |
| `debug.stepOver` | 操作 | `normal` | ステップオーバー（DAP） |
| `debug.stepIn` | 操作 | `normal` | ステップイン（DAP） |
| `debug.stepOut` | 操作 | `normal` | ステップアウト（DAP） |
| `debug.runLast` | 操作 | `normal` | 前回の設定で実行する（DAP） |
| `debug.toggleUI` | 操作 | `normal` | DAP のペインを切り替える（DAP） |
| `preview.page-down` | 操作 | `normal`、`insert` | プレビューを 1 画面下へスクロールする |
| `preview.page-up` | 操作 | `normal`、`insert` | プレビューを 1 画面上へスクロールする |
| `preview.down` | 操作 | `normal`、`insert` | プレビューを 1 行下へスクロールする |
| `preview.up` | 操作 | `normal`、`insert` | プレビューを 1 行上へスクロールする |
<!-- action-ids:end -->

### 拡張機能の設定と設定の誤り

`extensions.settings` は、拡張機能の ID から、その拡張機能に渡す設定への対応である。
中身は拡張機能ごとに違い、それぞれの設定の型の doc にある（[標準の拡張機能](#標準の拡張機能)の各節）。

設定ファイルの他の項目（`fixendofline`、`fileTypes`、`extensions.syncTimeoutMs` など）の一覧は[設定ファイルの項目](#設定ファイルの項目)に、詳しい値の形は `go doc ./internal/config Config` にある。
設定に誤りがあると、起動後にコマンドラインの行へ位置と内容を出し、誤りのある項目だけを既定値にする。
誤りが複数あれば、行には最初の 1 件と件数を出す。
`:messages`（`:mes`）で、すべての誤りと、拡張機能がメッセージの行に出した知らせ（言語サーバーの `window/showMessage`、DAP の止まった理由など）を古い順に読める。
拡張機能の進み具合（[拡張機能の進み具合](#拡張機能の進み具合)）は、画面の右下に出し、`:messages` には残さない。
行に出た誤りは、次のキーを打つまで、拡張機能の知らせで上書きされない。

### 拡張機能を使わない

`extensions.disabled` に拡張機能の ID を並べると、本体はその拡張機能の宣言ファイルを使わない。
どの契機でも起動せず、その拡張機能のキー、コマンド、ステータス行の部品も無くなる。
本体の起動で起動する拡張機能（起動の契機 `startup`。[スクリプトの拡張機能](#スクリプトの拡張機能)の宣言ファイルにも書ける）を止めるときに使う。

```jsonc
{
  "extensions": {
    "disabled": ["memusage", "git"],
  },
}
```

利用者の拡張機能のディレクトリと配布物の `extensions/` の両方に同じ ID があれば、どちらも使わない。
どの拡張機能にも無い ID を書くと、起動時に設定の誤りとして知らせる。
止めた拡張機能の部品を `statusline` や `tabline` に並べたままでも、そのコマンドを `keymap` の `command` や `commandAliases` に書いたままでも、設定の誤りにはならず、黙って使わない（`config.example.jsonc` の並びのまま `memusage` を止めてよい）。

### keymap から Ex コマンドを呼ぶ

`keymap` の右辺に `{ "command": "<正規の名前>", "args": "<引数>" }` と書くと、そのキーは、コマンドラインを出さずにその Ex コマンドを実行する（FR-154）。
`args` は省ける。

```jsonc
{
  "keymap": {
    "normal": {
      // 検索の一致の色を消す（:search.clearHighlight。Vim の :nohlsearch）
      "<C-l>": { "command": "search.clearHighlight" },
      // 行番号の列を出す、消すを切り替える（:option.toggle lineNumber.show。Vim の :set number!）
      "<Leader>n": { "command": "option.toggle", "args": "lineNumber.show" },
      // 拡張機能のコマンドも呼べる
      "<Leader>f": { "command": "finder.files" },
      // 外部の TUI を端末多重化ソフトのポップアップで開く（:terminal.popup keifu-wt）
      "<Leader>gk": { "command": "terminal.popup", "args": "keifu-wt" },
    },
  },
}
```

実行は、`:` で `search.clearHighlight` と打って `<CR>` を押したときと同じである。
違うのは、コマンドラインを出さないことと、打った行として残さないことだけである。
誤りは、`:` で打ったときと同じ文言でメッセージの行に出る。

`command` に書けるのは次の名前で、省略形は書けない。

- 本体の Ex コマンドの正規の名前（点を含む名前。`option.set`、`option.get`、`option.toggle`、`search.clearHighlight`、`tab.new`、`terminal.popup`）
- 拡張機能のコマンドの ID（`finder.files`、`acp.prompt`）と、LSP と DAP の操作の名前（`hover` など。[LSP と DAP の既定のキー](#lsp-と-dap-の既定のキー)の表の ID）

別名（`set`、`nohlsearch`、`commandAliases` で足した別名）は書けない。別名は、コマンドラインで打つための入口である。
正規の名前がまだ無い Ex コマンド（`:w`、`:q`、`:help` など）も書けない。これらはキーの列（`":w<CR>"`）で割り当てる。
`args` は、コマンドラインで名前の後に打つ引数と同じである。
実行するのは 1 つのコマンドだけなので、`|` で次のコマンドへ続けるコマンド（`option.toggle` など）の `args` に `|` を書くと、設定の誤りになる（`|` そのものは `\|` と書く）。
`|` を値の一部として読む `option.set` と `terminal.popup`、拡張機能のコマンドの `args` には、`|` をそのまま書ける。

割り当てられるモードは `normal`、`visual`、`insert` で、`commandLine` には書けない。
左辺の規則と、同じキーになる項目の扱いは、[操作の ID](#操作の-id) と同じである。
モードは変えずに実行する（Vim の `<Cmd>` の割り当てと同じ）。

- `visual` では、選択を残したまま実行する。`:` で打つと入る `'<,'>` は付けない（書ける名前のコマンドは、どれも範囲を取らないため）
- `insert` では、インサートモードのまま実行する。入力は区切らないので、`ifoo<C-l>bar<Esc>` の後の `u` は `foobar` を一度に戻し、`.` は `foobar` を入れる
- コマンドが別のタブへ移ったとき（`tab.new`）は、`insert` なら移った先でインサートモードが続き、元のタブはノーマルモードに戻る。`visual` なら元のタブの選択を抜ける（Vim の `<Cmd>tabnew<CR>` と同じ）
- `insert` でファインダーなどのポップアップを開くと、元のタブの入力はそこで終わる。ポップアップで選んだファイルへは、ノーマルモードで移る
- `insert` の割り当ては、`action` と同じく、ファインダーの入力欄の中でも効く
- 外から開かれたファイル（`git commit` などが開く、待つだけの client のファイル）へ移ったときは、入力の途中でも、元のタブと開いたファイルのどちらもノーマルモードになる（Vim の `--remote-wait-silent` と同じ）
- `.` では繰り返さない（コマンドラインで打った Ex コマンドと同じ）。マクロには打ったキーが入り、再生すると同じコマンドを実行する

`action` と `command` を 1 つのオブジェクトに両方書く、`command` を書かずに `args` を書く、書けない名前を書くと、設定の誤りとして知らせ、その割り当てを使わない。

### `<Leader>` と `leader`

`keymap` の左辺と右辺には、`<Leader>` と書ける（FR-97）。
`<Leader>` は、設定の `leader` に書いたキーの列に置き換わる。
`leader` の既定は `<Space>` なので、`"<Leader>d"` は `"<Space>d"` と同じである。
`<Leader>` の大文字と小文字は区別しない（`<leader>` でもよい）。

```jsonc
{
  // <Leader> を , にする。既定は "<Space>"
  "leader": ",",
  "keymap": {
    "normal": {
      "<Leader>w": ":w<CR>",
    },
  },
}
```

`leader` は `keymap` と同じ表記のキーの列で、`","`、`"<Space>"`、`"<C-k>"` のように書く。
次の値は誤りで、起動後に誤りの位置と内容を出し、既定の `<Space>` を使う。

- 空の値と、`<Leader>` を含む値
- 回数、レジスター、マクロ、ドットリピートとして先に読まれるキー（`1`〜`9`、`"`、`q`、`@`、`.`）で始まる値。`<Leader>` で始まるキーを割り当てられなくなるため
- `<Esc>` か `<CR>` で始まる値。モードを抜ける、コマンドラインを実行するキーが、leader の続きを待つようになるため

`<Leader>w`、`<leader>w`、`<Space>w`（leader が既定のとき）のように、表記は違っても同じキーの列になる左辺を同じモードに書くと、誤りとして知らせる。
効くのは、左辺の表記を文字コードの順に並べて最初の項目（この例では `<Leader>w`）で、残りは使わない。
拡張機能の宣言ファイル（`manifest.json`）の `contributes` の `keys` に書いた `<Leader>` も、同じく置き換わる。

`leader` を変えると、`<Leader>` を書いたキーの割り当てがすべて新しい `leader` で始まる。
設定のどこに `leader` を書いても結果は同じである。
Vim の `mapleader` は、マッピングを定義した時点の値で置き換えるので、この点が違う（[vim-differences.md](vim-differences.md)）。

## 見た目の設定

設定ファイルの `colorscheme`、`colors`、`symbols`、`icons`、`statusline` で、配色、記号、ファイルの種類のアイコン、ステータス行の並びを変えられる（要件定義 FR-91〜FR-93、FR-125、FR-152、基本設計 7.1）。
同梱の配色 iceberg を選び、その上に `colors` を重ねる例を、リポジトリの直下と配布物に `config.example.jsonc` として同梱している。
例をそのまま使うときは、`config.example.jsonc` を設定ファイルの場所へ写す。

```jsonc
{
  "colorscheme": "iceberg",
  "colors": {
    "nontext": { "fg": "#444b71" },
    "diagnostic.error": { "sp": "#e27878", "undercurl": true },
    "statusline.mode": { "bold": true },
  },
  "symbols": { "readonly": "[読]", "sign.breakpoint": "●", "popup.border": "╭─╮│╯─╰│" },
  "statusline": {
    "left": ["mode", "name", "modified", "readonly"],
    "right": ["filetype", "ruler", "position"],
  },
}
```

設定に誤り（知らない名前、読めない色、使えない記号）があると、起動後にコマンドラインの行へ位置と内容を出し、その項目だけを使わない。

### 配色の選択

`colorscheme` は、配色の名前の文字列である。
選んだ配色は本体の既定の色を名前ごとに置き換え、`colors` に書いた名前はその上でさらに置き換わる。
`colorscheme` を書かなければ、本体の既定の色（端末の 16 色の番号）に `colors` を重ねるだけである。

配色は、次の順に探す。

1. 設定ファイルと同じディレクトリの `colors/<名前>.jsonc`（`$XDG_CONFIG_HOME/vi-imitation/colors/<名前>.jsonc`。未設定なら `~/.config/vi-imitation/colors/<名前>.jsonc`）。設定ファイルが symlink でも、symlink の先ではなく symlink を置いた側のディレクトリを探す
2. 本体に同梱した配色

同じ名前なら 1 のファイルを使うので、同梱の配色を同じ名前のファイルで置き換えられる。
同梱しているのは `iceberg`（[iceberg.vim](https://github.com/cocopon/iceberg.vim) の暗い背景の色）だけで、値と写した元は `internal/config/colors/iceberg.jsonc` にある。
同梱の iceberg は、iceberg.vim の配色そのもの（`colors/iceberg.vim`）の色だけを持つ。
`colors/iceberg.vim` に対応するハイライトグループが無い名前（`message.info`、`popup.border`、`statusline.` で始まる部品の色、`sign.executionPoint`、`sign.breakpoint`、`sign.breakpointUnverified`）は、同梱の iceberg に無いので本体の既定の色になる。
ステータス行は Vim の iceberg と同じく `StatusLine` の反転（明るい灰色の帯）になる。
`config.example.jsonc` は、iceberg.vim のリポジトリの lightline のテーマの色を `colors` に書き、ステータス行を lightline に近い見た目にしている。
色は 24 ビットの `#rrggbb` で書いてあるので、24 ビットの色を出せる端末で使う。

配色のファイルは、`colors` の値と同じ形（色を付ける対象の名前から属性のオブジェクトへの対応）を最上位に書いた JSONC である。

```jsonc
// ~/.config/vi-imitation/colors/mine.jsonc
{
  "keyword": { "fg": "#84a0c6" },
  "statusline": { "fg": "#c6c8d1", "bg": "#0f1117" },
}
```

名前に使えるのは英数字と `_` と `-` だけである。
見つからない名前と使えない名前は、起動後に `colorscheme` の位置の誤りとして知らせ、配色を使わずに起動する。
配色のファイルの中の誤り（知らない名前、読めない色）は、配色のファイルの位置で知らせ、その名前だけを使わない。
`:colorscheme` に当たるコマンドは無く、配色を変えたら起動し直す。

### 配色

`colors` は、色を付ける対象の名前から、属性のオブジェクトへの対応である。
属性のオブジェクトには次の項目を書ける。

| 項目 | 値 |
|---|---|
| `fg`、`bg`、`sp` | 文字の色、背景の色、下線と波線の色。`"#rrggbb"` の 24 ビットの色か、0〜255 の 256 色の番号。書かなければ端末の既定の色。本体は端末が 24 ビットの色を出せるかを確かめずに送るので、24 ビットの色を出せない端末では 256 色の番号で書く |
| `bold`、`italic`、`underline`、`undercurl`、`reverse` | 太字、斜体、下線、波線、文字と背景の色の入れ替え。真偽値 |

名前ごとに既定の属性を置き換え、既定の属性とは混ぜない。
例えば既定の `statusline` は反転と太字なので、`{ "bg": "#0f1117" }` だけを書くと反転と太字は消える。
ステータス行の部品の色（`statusline.` で始まる名前）だけは、`statusline` の属性の上に重ねる。
部品の色に `fg` か `bg` を書くと、`statusline` の反転は外す（既定の `statusline` は反転なので、外さないと部品の文字の色と背景の色が入れ替わって出る）。
このため、`statusline` に `bg` を書いていれば、部品の色に `fg` だけを書いても背景は `statusline` の色のままになる。
既定の `statusline`（反転だけで `bg` が無い）のまま部品の色に `fg` だけを書くと、その部品の背景は端末の既定の背景になる。
`statusline` の太字は外せないので、部品を太字にしたくないときは `statusline` 自体を書き換える。

色を付ける対象の名前は次のとおりである。
構文の色の 10 個の名前は、拡張機能（tree-sitter など）が `decoration/set` の `style` で使う名前と同じで、前に `syntax.` を付けない。
表に無い名前の範囲には色を付けない。
拡張機能は、名前の色の上に、拡張機能が決めた色（インレイヒントの薄い色、色コードの文字の範囲のその色など）を直に重ねることもある。直に重ねた色は `colors` では変えられない（拡張機能の設定で変える）。

| 名前 | 対象 | 既定 |
|---|---|---|
| `comment`、`keyword`、`string`、`number`、`constant`、`function`、`type`、`variable`、`operator`、`punctuation` | 構文の色（基本設計 6.1） | 端末の 16 色の一部。`variable` と `punctuation` は色なし |
| `visual` | ビジュアルモードの選択の範囲 | 反転 |
| `statusline` | ステータス行 | 反転と太字 |
| `statuslinenc` | 出力の欄の下の行 | 反転 |
| `statusline.<部品の ID>` | ステータス行の部品（`statusline.mode`、`statusline.name`、`statusline.modified`、`statusline.readonly`、`statusline.filetype`、`statusline.recording`、`statusline.ruler`、`statusline.position`）。`statusline` の上に重ねる | なし |
| `statusline.mode.normal`、`statusline.mode.insert`、`statusline.mode.visual`、`statusline.mode.commandline` | モードごとのモードの部品。`statusline.mode` の上に重ねる。`visual` は文字単位と行単位の両方 | なし |
| `statusline.ext` | 拡張機能が足した部品。`statusline`（タブの行では `tabline.fill`）の上に重ね、その上に拡張機能が部品ごとに選んだ色を重ねる | なし |
| `nontext` | バッファの最終行より後の `~`。長い行を折り返したときの `>`、`@@@`、`<<<` と続きの行の記号（[長い行の折り返し](#長い行の折り返し)）。行末の記号 `whitespace.eol`（[空白の記号](#空白の記号)） | 端末の 16 色の 12 |
| `specialkey` | `^A` や `<80>` の形で出した文字。空白の記号を描くときに、タブ文字の記号が空なら描く `^I` | 端末の 16 色の 12 |
| `whitespace` | 空白の記号（[空白の記号](#空白の記号)）のうち、タブ文字、空白、ノーブレークスペースの記号（Neovim の `Whitespace`） | 端末の 16 色の 12 |
| `linenr` | 行番号の列（[行番号](#行番号)）。番号の右の空けた 1 列を含む列の全体 | 端末の 16 色の 11（Vim の暗い背景の既定と同じ番号） |
| `search` | 直前の検索のパターンに一致する範囲（[検索の一致の色付け](#検索の一致の色付け)）。構文の色と診断の上、`visual` の下に重ねる | 文字は端末の 16 色の 0、背景は 11（Vim の暗い背景の既定と同じ番号） |
| `incsearch` | `/` と `?` でパターンを打っている間の、今の一致（[打つたびの検索](#打つたびの検索)）。`search` と `yank` の上、`visual` の下に重ねる | 反転（Vim の暗い背景の既定と同じ）。`search.highlight` が `true` のときは、下の `search` の文字と背景を入れ替えた色に見える。同梱の iceberg では iceberg.vim の `Normal` の文字と背景を入れ替えた色 |
| `yank` | `y` でヤンクした範囲に一時的に付ける色（[ヤンクした範囲の色付け](#ヤンクした範囲の色付け)）。`search` の上、`visual` の下に重ねる。`visual` と重なったところで選択の色を上に見せたいときは、反転でなく背景の色で書く | 背景が端末の 16 色の 6（Vim の暗い背景の `MatchParen` の既定と同じ番号）。同梱の iceberg では iceberg.vim の `PmenuSel` の色 |
| `message.info`、`message.warning`、`message.error` | コマンドラインの行のメッセージ | 順に色なし、黄色、赤の背景 |
| `diagnostic.error`、`diagnostic.warning`、`diagnostic.info`、`diagnostic.hint` | 診断の範囲 | 波線か下線 |
| `popup` | 浮かべる窓（ホバーの説明、`:registers` の一覧）と、コマンドラインの補完の候補の一覧（[コマンドラインの補完](#コマンドラインの補完)） | 灰色の背景 |
| `popup.selected` | `:registers` の一覧と補完の候補の一覧の選んでいる項目 | 水色の背景 |
| `popup.border` | 浮かべる窓の枠線。書かなければ `popup` の色 | なし |
| `popup.match` | 補完の候補の一覧の項目の、打った語に当たる先頭の文字（Neovim の `PmenuMatch`）。`popup` か `popup.selected` の上に重ねる | 太字 |
| `popup.scrollbar`、`popup.thumb` | 補完の候補の一覧のスクロールバーの溝とつまみ（Vim の `PmenuSbar` と `PmenuThumb`） | 順に灰色（`popup` と同じ）、明るい灰色の背景 |
| `commandLine.popup` | コマンドラインを画面の上に出すとき（[コマンドラインを画面の上に出す](#コマンドラインを画面の上に出す)）の、入力の枠の中と、その下の候補の一覧の項目（noice.nvim の `NoiceCmdlinePopup`）。一覧の選んでいる項目と打った語に当たる文字は `popup.selected` と `popup.match` で描く | なし（端末の既定の文字と背景の色。noice.nvim の既定のリンク先の `Normal` と同じ） |
| `commandLine.popup.border`、`commandLine.popup.border.search` | コマンドラインを画面の上に出すときの枠線と題の、コマンドライン（`:`。候補の一覧の枠線も）と検索（`/`、`?`）（noice.nvim の `NoiceCmdlinePopupBorder` と `NoiceCmdlinePopupBorderSearch`） | 順に端末の 16 色の 12、11（noice.nvim の既定のリンク先の `DiagnosticSignInfo` と `DiagnosticSignWarn` に当たる `sign.diagnostic.info` と `sign.diagnostic.warning` と同じ番号）。同梱の iceberg では iceberg.vim の `DiagnosticSignInfo` と `DiagnosticSignWarn` の文字の色 |
| `finder.match` | ファジーファインダーの一覧の、入力に一致した文字 | 太字と下線 |
| `preview.line` | 項目のプレビュー（[項目のプレビュー](#項目のプレビュー)）の、項目の指す行（検索の一致した行など）。行の全体に付け、構文の色をその上に重ねる | 背景が端末の 16 色の 0。同梱の iceberg では iceberg.vim の `CursorLine` の背景 |
| `progress` | 画面の右下に重ねる拡張機能の進み具合（[拡張機能の進み具合](#拡張機能の進み具合)）の本文と百分率 | なし（端末の既定の文字の色） |
| `progress.title`、`progress.done` | 進み具合の表題と印の、続いている間と終わった後 | 順に端末の 16 色の 11、13（fidget.nvim の既定の `WarningMsg` と `Constant` に当てた本体の `message.warning` と `constant` と同じ番号） |
| `progress.failed` | 失敗して終わった進み具合の表題と印 | 端末の 16 色の 9（`sign.diagnostic.error` と同じ） |
| `scrollbar` | 右端のスクロールバー（[スクロールバー](#スクロールバー)）のつまみ。つまみのセルの文字と色の上に重ねるので、背景の色だけを書くと下の文字が残る | 反転（下の文字と背景の色を入れ替える。本体の既定の `visual` と同じで、端末の背景が暗くても明るくても読める）。同梱の iceberg では iceberg.vim の `Visual` の背景（nvim-scrollview の既定の `ScrollView` のリンク先。暗い背景の配色） |
| `scrollbar.diagnostic.error`、`scrollbar.diagnostic.warning`、`scrollbar.diagnostic.info`、`scrollbar.diagnostic.hint` | スクロールバーの診断の印。つまみの上では `scrollbar` の上に重ねる（既定では反転で、印の色の地に記号が出る） | 印の列の `sign.diagnostic.*` と同じ、端末の 16 色の 9、11、12、8 |
| `scrollbar.search` | スクロールバーの検索の一致の印 | 端末の 16 色の 14（情報の印の 12 と色で見分けられるようにした）。同梱の iceberg では iceberg.vim の `Normal` の文字の色 |
| `scrollbar.<種類>` | 拡張機能が後から足すスクロールバーの印の種類の色。`scrollbar.` の後に、`scrollbar.marks` に書ける形の名前（空でなく、コンマと空白を含まない）を書く。本体の表に無い名前でも誤りにしない。ただし、本体の種類（`diagnostic`、`search`）と同じか似た名前（`scrollbar.diagnostics.error`、`scrollbar.serch` など。点までの部分の編集距離が 2 以下）で本体の表に無いものは、書き間違いとして設定の誤りにする | なし |
| `band` | 拡張機能が窓の上端に重ねる帯（[窓の上端の帯](#窓の上端の帯)）の本文の列。帯の行の範囲の色はこの上に重ねる | 書かなければ `popup` の今の色を引き継ぐ（nvim-treesitter-context の既定の `TreesitterContext` は浮かべた窓の `NormalFloat` へのリンク）。同梱の iceberg も書かないので、`popup`（iceberg.vim の `Pmenu`）の色になる |
| `band.linenr` | 帯の行の、印の列と行番号の列 | 書かなければ `linenr` の今の色を引き継ぐ（nvim-treesitter-context の既定の `TreesitterContextLineNumber` は `LineNr` へのリンク）。同梱の iceberg も書かないので、`linenr`（iceberg.vim の `LineNr`）の色になる |
| `band.bottom` | 帯の最後の行の全体に重ねる色（帯と本文の境目に下線を引くときに書く。nvim-treesitter-context の `TreesitterContextBottom`） | なし |
| `sign.executionPoint`、`sign.breakpoint`、`sign.breakpointUnverified` | 印の列のデバッガーの印 | 順に黄色、赤、灰色 |
| `sign.diagnostic.error`、`sign.diagnostic.warning`、`sign.diagnostic.info`、`sign.diagnostic.hint` | 印の列の診断の印 | 順に赤、黄色、青、灰色 |
| `tabline`、`tabline.selected`、`tabline.fill` | タブの行の、ほかのタブ、いまのタブ、タブの右の空いた列 | 順に灰色の背景と下線、太字、反転 |
| `diff.add`、`diff.change`、`diff.delete`、`diff.text` | 見比べの、片側にだけある行、内容が違う行、もう片側にだけある行の位置の詰め物、違う行の中の違う文字 | 順に紺、紫、水色の背景（文字は青）、赤の背景と太字（Vim の暗い背景の既定と同じ番号） |

既定の属性の値は `internal/ui/theme.go` の `DefaultTheme` にある。

### 記号

`symbols` は、本体が描く記号の名前から、文字列への対応である。
書かなかった名前は既定の文字を使う。

| 名前 | 対象 | 既定 | 書ける文字列 |
|---|---|---|---|
| `sign.executionPoint` | 止まった位置の印 | `=>` | 表示幅が 1 か 2 の文字列 |
| `sign.breakpoint`、`sign.breakpointUnverified` | ブレークポイントの印 | `B`、`b` | 同上 |
| `sign.diagnostic.error`、`sign.diagnostic.warning`、`sign.diagnostic.info`、`sign.diagnostic.hint` | 診断の印 | `E`、`W`、`I`、`H` | 同上 |
| `readonly` | ステータス行の編集の禁止の印 | `[RO]` | 任意の文字列 |
| `modified` | ステータス行の未保存の印 | `[+]` | 任意の文字列 |
| `popup.border` | 浮かべる窓（編集の画面の中に描く窓。端末多重化ソフトのポップアップの枠線は端末多重化ソフトが描くので効かない）と、画面の上に出すコマンドライン（[コマンドラインを画面の上に出す](#コマンドラインを画面の上に出す)）の枠線 | 空（浮かべる窓は枠線なし。画面の上に出すコマンドラインは ASCII の枠線 `+-+|+-+|`） | 空か、表示幅が 1 の文字を 8 つ。並びは左上、上、右上、右、右下、下、左下、左 |
| `commandLine.title.command`、`commandLine.title.searchForward`、`commandLine.title.searchBackward` | 画面の上に出すコマンドラインの枠の題の、コマンドライン（`:`）、前方の検索（`/`）、後方の検索（`?`）。色は `commandLine.popup.border` と `commandLine.popup.border.search` | `Cmdline`、`Search forward`、`Search backward` | 任意の文字列。空なら題を出さない |
| `lineWrap.showBreak` | 長い行を折り返したとき（[長い行の折り返し](#長い行の折り返し)）に、続きの見た目の行の頭に付ける記号（Vim の `'showbreak'`）。色は `nontext` | 空（付けない） | 任意の文字列。本文の幅から 2 を引いた列数より広ければ付けない |
| `whitespace.tab` | 空白の記号を描くとき（[空白の記号](#空白の記号)）の、タブ文字の記号（Vim の `'listchars'` の `tab`）。色は `whitespace` | `> ` | 空か、表示幅が 1 の文字を 2 つか 3 つ。空ならタブ文字を `^I` で描く |
| `whitespace.space` | 行頭と行末でない空白の記号（`space`）。色は `whitespace` | 空（描かない） | 空か、表示幅が 1 の文字を 1 つ |
| `whitespace.lead` | 行頭の空白の記号（`lead`）。空なら `whitespace.space` を使う。色は `whitespace` | 空 | 同上 |
| `whitespace.trail` | 行末の空白の記号（`trail`）。空なら `whitespace.space` を使う。色は `whitespace` | `-` | 同上 |
| `whitespace.nbsp` | ノーブレークスペース（U+00A0 と U+202F）の記号（`nbsp`）。色は `whitespace` | `+` | 同上 |
| `whitespace.eol` | 行末の記号（`eol`）。色は `nontext` | 空（描かない） | 同上 |
| `progress.running`、`progress.done`、`progress.failed` | 画面の右下の進み具合（[拡張機能の進み具合](#拡張機能の進み具合)）の行の右の端に付ける、続いている印、成り立って終わった印、失敗して終わった印。色は `progress.title`、`progress.done`、`progress.failed` | `...`、`done`、`failed` | 任意の文字列。空なら付けない |
| `scrollbar.diagnostic.error`、`scrollbar.diagnostic.warning`、`scrollbar.diagnostic.info`、`scrollbar.diagnostic.hint` | 右端のスクロールバー（[スクロールバー](#スクロールバー)）の診断の印。色は同じ名前の色 | `E`、`W`、`I`、`H` | 空か、表示幅が 1 の文字を 1 つ。空ならその印を描かない |
| `scrollbar.search` | スクロールバーの検索の一致の印。色は `scrollbar.search` | `=` | 同上 |
| `scrollbar.<種類>` | 拡張機能が後から足すスクロールバーの印の種類の記号。名前の規則（本体の種類の書き間違いを誤りにすることを含む）は色の `scrollbar.<種類>` と同じで、本体の表に無い名前でも誤りにしない | 空（描かない） | 同上 |

どの記号にも、タブと制御文字は書けない。
印の列は 2 列なので、表示幅が 1 の印は右に空白を置いて描く。
既定の記号は ASCII にしてある。
罫線（`─`）や `●` のような、Unicode で幅が曖昧と定められた文字は、本体は 1 列と数える（Vim の `'ambiwidth'` の既定の `single` と同じ）。
端末がそれらの文字を 2 列で描く設定だと、列がずれる。

### アイコン

[ファジーファインダー](#ファジーファインダー)と[ファイラ](#ファイラ)の、ファイルとディレクトリの項目の前に、ファイルの種類のアイコンを付ける（nvim の neo-tree と fzf-lua が使う nvim-web-devicons と同じ見た目）。
アイコンは [Nerd Font](https://www.nerdfonts.com/) のグリフなので、端末のフォントに Nerd Font を使っていない環境では、アイコンの位置に四角や `?` が出る。
そのときは、設定ファイルに次のように書いて切る（同梱の `config.example.jsonc` にも書いてある）。

```jsonc
"icons": { "show": false }
```

実行中に `:option.set icons.show false` でも切れる。
本体が一覧を作り直したとき（開いたとき、ファイラで木を開閉したとき、拡張機能が中身を送り直したとき）から効く。

アイコンは、ファイルの拡張子でなく、本体が決めたファイルの種類（ステータス行の `filetype` に出る名前。設定の `fileTypes` で足した種類を含む）ごとに決まる。
ほかに、閉じたディレクトリ、開いたディレクトリ、種類の分からないファイルのアイコンがある。
既定の表は、本体が判定できる種類の全部と、`fileTypes` で足すことの多い種類（`lua`、`html`、`dockerfile` など）を持つ。
文字と色は nvim-web-devicons（ディレクトリは neo-tree.nvim）から写したもので、値と写した元は `internal/ui/icons_default.go` にある。
キー、コマンド、レジスター、行の一覧には付けない。

`icons` の `show` のほかの項目で、表のアイコンを名前ごとに書き換えられる。

```jsonc
"icons": {
  "directory": { "text": "D", "fg": "#73cef4" },
  "fileTypes": {
    "go": { "text": "G", "fg": "#00add8", "bold": true },
    "lua": { "text": "L", "fg": 74 },
  },
}
```

| 名前 | 対象 |
|---|---|
| `directory` | 閉じたディレクトリ（ファイラで開いていないもの） |
| `directoryOpen` | 開いたディレクトリ |
| `file` | 種類の分からないファイル（種類が決まらないか、表に無い種類） |
| `fileTypes` の下の種類の名前 | その種類のファイル。表に無い種類も書ける |

1 つのアイコンは、文字列の `text` と、[配色](#配色)の `colors` と同じ属性の項目（`fg`、`bg`、`sp`、`bold` など）を持つ。
`text` は文字そのものでも、JSON の `\u` の書き方でも書ける（上の例は、どの端末でも読めるように ASCII にした）。
ただし `\u` の後の 16 進は 4 桁なので、U+FFFF より大きいグリフ（Nerd Font の `md-` の多く）は、サロゲートの組か文字そのものを書く。
例えば U+F0868 は、`\u` の後に `db82` を書いたものと、`\u` の後に `dc68` を書いたものを並べる。
`\u` の後に 5 桁の `f0868` を書くと、U+F086 と `8` の 2 文字になり、誤りとして使わない。
書いた名前は既定のアイコンを置き換え、既定の文字と色を混ぜないので、色だけを変えるときも `text` を書く（`text` の無いアイコンは誤りとして使わない）。
`text` は 1 つの文字（後に続く結合文字を含む）で、表示幅が 1 か 2 のものにする。
幅 2 のアイコンを書くと、どの行のアイコンも 2 列の幅にそろえて置く。
Nerd Font のグリフは 1 列と数える（記号と同じく Vim の `'ambiwidth'` の既定の `single` と同じ）。
端末を「曖昧な幅の文字を 2 列にする」設定にしていると、記号と同じくアイコンの右の列がずれる。
アイコンの色は窓の背景の上に重なり、配色（`colorscheme`）では変わらない。

### ステータス行の並び

`statusline` は、`left` と `right` の 2 つの項目を持つオブジェクトで、どちらも部品の ID の配列である。
`left` の部品は左の端から、`right` の部品は右の端に寄せて、配列の順に並べる。
並べなかった部品は出さない。
同じ部品を 2 回並べると 2 回出す。
`statusline` を書かなければ、次の既定の並びを使う。
`statusline` を書いて `left` か `right` を省くと、省いた側には何も出さない。

```jsonc
{
  "statusline": {
    "left": ["mode", "name", "modified", "readonly"],
    "right": ["recording", "ruler", "position"],
  },
}
```

| 部品の ID | 中身 |
|---|---|
| `mode` | モードの名前（`NORMAL`、`INSERT`、`VISUAL`、`VISUAL LINE`、`COMMAND`） |
| `name` | バッファの名前（ファイルのパス）。無ければ `[No Name]` |
| `modified` | 未保存の変更があるときの印（`symbols` の `modified`） |
| `readonly` | 編集を禁止しているときの印（`symbols` の `readonly`） |
| `filetype` | ファイルの種類（`go` など）。判定できなければ出さない |
| `recording` | マクロの記録中の表示（`recording @q`） |
| `ruler` | カーソルの位置。Vim のルーラーと同じ「行,バイトの列-表示の列」 |
| `position` | 表示している範囲の位置（`All`、`Top`、`Bot`、`NN%`） |

部品は、中身の左右に空白を 1 つずつ置き、部品の色で描く。
`modified` と `readonly` は、前に部品があれば左の空白を置かずに続ける（`a.go [+]`）。
幅が足りなければ、右の部品を優先する。
左の部品が収まらなければ、Vim と同じく名前の先頭を `<` で切り、名前の後の `[+]` と `[RO]` を残す（`<4030-b80ff77dac4f/find.go [RO]`）。
名前を `<` だけにしても収まらなければ、名前を出さずに印を残し、それでも収まらなければ左の部品を右の端で切る。

### 拡張機能の部品

拡張機能は、宣言ファイルの `contributes.statusline` で部品を足せる（基本設計 7.1）。
部品の ID は拡張機能の ID と `.` で始まり、その後に `.` を含まない（`mrreview.status`）。
本体の部品と同じく、`statusline` と `tabline` の `left` と `right` に ID を書いて並べる。

```jsonc
{
  "statusline": {
    "left": ["mode", "mrreview.status", "name", "modified", "readonly"],
    "right": ["memusage.rss", "ruler", "position"],
  },
}
```

並べた部品の拡張機能は、起動の契機を待たずに本体の起動時（最初の画面を描いた後）に起動する。
標準の拡張機能の部品は、メモリの使用量の `memusage.rss`（[メモリの使用量](#メモリの使用量)）である。
部品の中身は拡張機能が送る。
中身が届くまでと、拡張機能が中身を空にした間は、その部品を出さない。
拡張機能が異常で終わるか止まると、その拡張機能の部品は消え、再起動した拡張機能が送り直すまで出ない。
中身はバッファに依らず、どのバッファのステータス行にも同じものを出す。
`:ext sync` で部品の拡張機能を入れ替えたときも、起動し直して部品を送り直させる。
幅が足りなければ、本体の部品（モード、名前の末尾の 20 列、位置など）より先に、拡張機能の部品を幅の広いものから切り詰め（末尾を `>` にする）、それでも足りなければ落とす。
1 つの部品の中身は 256 文字までで、超えた分は捨てる。
`.` を含む ID を、どの拡張機能も足していなければ、起動時に設定の誤りとして知らせて出さない。

### タブの行

タブの行は、既定ではタブが 1 つでも出る。タブが 2 つ以上のときだけ出すには、`tabline` の `show` を `"multiple"` にする（既定は `"always"`。[タブとジャンプ](#タブとジャンプ)）。

```jsonc
{ "tabline": { "show": "multiple" } }
```

タブの行も、`statusline` と同じく `left` と `right` で部品を並べられる。
タブの見出しの並びは部品 `tabs` で、他の部品を描いた残りの幅を使う。
`left` も `right` も書かなければ、`tabs` だけを左に置く（`{ "left": ["tabs"], "right": [] }`）。
どちらかを書くと、省いた側には何も出さない。`tabs` を並べなければタブの見出しは出ない。
`tabs` の他に、ステータス行の本体の部品（`mode`、`name` など。いまのバッファのもの）と拡張機能の部品を並べられる。
部品の色は `tabline.fill` の上に重ねる。

```jsonc
{ "tabline": { "show": "always", "left": ["tabs"], "right": ["memusage.rss"] } }
```

`tabs` は 2 回並べても、左、右の順で最初の 1 つだけを出す。
`tabs` をステータス行に並べると、未知の部品として設定の誤りにする。

## Ex コマンドの一覧

本体の Ex コマンドは次のとおりである（`internal/command/ex` の表と、`:ext`、`:help`、`:keys`、`:messages`、`:intro`）。
括弧の中は、打てる最も短い省略形である。
範囲（`:3,5d` の `3,5`、`%`、`'<,'>`）の書き方は Vim と同じで、パターンは Go の `regexp` の文法で書く（[vim-differences.md](vim-differences.md)）。
`|` で続けて書ける。

| コマンド | 動作 |
|---|---|
| `:w`、`:write`（`:w`） | バッファを書き出す。`:w ファイル` は別のファイルへ書く。`:w!` は既にあるファイルへも書く。範囲はバッファの全体（`:%w`）だけを書ける |
| `:wq`（`:wq`） | 書き出してタブを閉じる |
| `:x`、`:xit`（`:x`） | 変更があるか、ファイルを書いたときだけ書き出し、タブを閉じる |
| `:q`、`:quit`（`:q`） | タブを閉じる。未保存の変更があれば E37 で断り、`:q!` は変更を捨てる。client の最後のタブを閉じると、その client が終わる |
| `:wa`、`:wall`（`:wa`） | 未保存の変更のあるバッファをすべて書き出す |
| `:qa`、`:qall`（`:qa`） | この client のタブをすべて閉じる。`:qa!` は変更を捨てる |
| `:wqa`、`:wqall`、`:xa`、`:xall`（`:wqa`、`:xa`） | この client のタブの未保存の変更を書き出して、タブをすべて閉じる |
| `:e`、`:edit`（`:e`） | `:e ファイル` は、ファイルを新しいタブで開く。引数を省くと、ファイルを読み直す（`:e!` は変更を捨てる） |
| `:b`、`:buffer`（`:b`） | `:b 番号` か `:b 名前の一部` で、そのバッファのタブへ移る |
| `:bn`、`:bnext`、`:bp`、`:bprevious`、`:bN`（`:bn`、`:bp`、`:bN`） | 番号の順に次、前のバッファのタブへ移る。`:bn 3` は 3 個先 |
| `:bd`、`:bdelete`（`:bd`） | バッファを閉じる。引数はバッファの番号か名前の一部 |
| `:ls`、`:buffers`（`:ls`） | 開いているバッファの一覧を出す |
| `:tabmove`（`:tabm`） | タブを動かす。`:tabm 0` は先頭、`:tabm` は末尾、`:tabm +1` と `:tabm -1` は 1 つ右と左 |
| `:tab.new`（省略できない） | 名前の無い空のバッファを、いまのタブの右の新しいタブで開く。`:tab.new ファイル` は `:e ファイル` と同じ。Vim の `:tabnew` の名前は、設定ファイルの別名で付ける（[Ex コマンドの別名を足す](#ex-コマンドの別名を足す)） |
| `:popup`（`:popu`） | カーソルのあるタブをポップアップへ縮める（[ポップアップ](#ポップアップ)） |
| `:tabopen`（`:tabop`） | ポップアップをタブへ広げる（[ポップアップ](#ポップアップ)） |
| `:jumps`（`:ju`） | ジャンプリストを出す |
| `:registers`（`:reg`） | レジスターと履歴の一覧を出す（[レジスターと履歴](#レジスターと履歴)） |
| `:readonly`（`:reado`） | バッファの編集の禁止を切り替える（[編集の禁止](#編集の禁止)） |
| `:s`、`:substitute`（`:s`） | `:[範囲]s/パターン/置換/[フラグ]` で置き換える。範囲を省くとカーソルの行 |
| `:g`、`:global`、`:v`、`:vglobal`（`:g`、`:v`） | `:[範囲]g/パターン/コマンド` で、パターンに一致する行（`:v` は一致しない行）ごとにコマンドを実行する |
| `:d`、`:delete`（`:d`） | `:[範囲]d [レジスター] [回数]` で行を消す |
| `:!`（`:!コマンド`） | 端末をコマンドに渡して実行する（[外部のコマンドとエディタの中のエディタ](#外部のコマンドとエディタの中のエディタ)） |
| `:terminal.popup`（省略できない） | `:terminal.popup コマンド` で、コマンドを端末多重化ソフトのポップアップで動かす。外では `:!` と同じに動く（[コマンドをポップアップで動かす](#コマンドをポップアップで動かす)） |
| `:{範囲}!コマンド` | 範囲の行を外部のコマンドの標準入力に渡し、出力で置き換える |
| `:r !コマンド`、`:read !コマンド`（`:r`） | 外部のコマンドの出力を、カーソルの行（`:0r` は先頭）の後に入れる |
| `:{行番号}` | その行へ移る（`:$` は最後の行） |
| `:output`（`:out`）、`:outclose`（`:outc`） | 出力の欄を開く、閉じる（[出力の欄](#出力の欄)） |
| `:checktime`（`:checkt`） | 開いているバッファのファイルが外で変わっていないかを確かめる |
| `:option.set`（省略できない） | `:option.set 名前 値` で設定の項目の値を変える（[実行中に設定を変える](#実行中に設定を変える)） |
| `:option.get`（省略できない） | `:option.get 名前` で設定の項目の今の値を出す（[実行中に設定を変える](#実行中に設定を変える)） |
| `:option.toggle`（省略できない） | `:option.toggle 名前` で真偽の設定の項目を反転する（[実行中に設定を変える](#実行中に設定を変える)） |
| `:set`（`:se`） | Vim の `:set` の書き方の一部を、`:option.set`、`:option.get`、`:option.toggle` に変換して実行する別名（[実行中に設定を変える](#実行中に設定を変える)） |
| `:search.clearHighlight`（省略できない） | 次の検索まで、検索の一致の色を消す（[検索の一致の色付け](#検索の一致の色付け)） |
| `:nohlsearch`（`:noh`） | `:search.clearHighlight` の別名 |
| `:ext`（`:ext`） | `:ext sync`、`:ext update` で拡張機能を取得する（[拡張機能の取得](#拡張機能の取得ext-sync)） |
| `:help`、`:h`（`:h`） | この文書を開く（[ヘルプとキーの一覧](#ヘルプとキーの一覧)） |
| `:keys`（`:keys`） | キーの一覧を出す（[ヘルプとキーの一覧](#ヘルプとキーの一覧)） |
| `:intro`（`:int`） | 起動の案内を出し、次のキーを 1 つ受けて消す（[起動の案内](#起動の案内)） |

拡張機能のコマンドと、LSP と DAP の操作の名前（`:hover` など）も、`:` の後に打って呼べる（[拡張機能のコマンドを呼ぶ](#拡張機能のコマンドを呼ぶ)）。

### Ex コマンドの別名を足す

設定ファイルの `commandAliases` に、Ex コマンドの名前を付け替えるだけの別名を書ける。
同梱の `config.example.jsonc` は、Vim の名前の `:tabedit`（`:tabe` まで省略できる）と `:tabnew` を、どちらも `:tab.new` の別名にしている。
`:tab.new ファイル` は `:e ファイル` と同じく新しいタブでファイルを開き、引数なしの `:tab.new` は空のタブを開くので、どちらの名前も Vim と同じに使える。

```jsonc
{
  "commandAliases": {
    // :tabe ファイル は :e ファイル と同じく、ファイルを新しいタブで開く。引数なしの :tabe は空のタブを開く
    "tabedit": { "command": "tab.new", "min": 4 },
    // :tabnew は空のタブを開く
    "tabnew": { "command": "tab.new" },
    // 拡張機能のコマンドにも別名を付けられる（:ff か :f でファイルを探す）
    "ff": { "command": "finder.files", "min": 1 },
  },
}
```

キーは別名の名前で、英字だけで書く。
`command` は付け替える先で、本体の Ex コマンドの省略しない名前（`edit`、`tab.new`）か、拡張機能のコマンドの ID（`finder.files`、`help`）を書く。
`min` は省略できる最短の長さで、省くと省略できない（名前をすべて打つ）。
範囲、`!`、引数の扱いは、付け替える先のコマンドと同じである。
足した別名は、コマンドの一覧（`:finder.commands`）と `:help` の「設定ファイルの Ex コマンドの別名」の節に出る。

次の別名は設定の誤りとして知らせ、その別名だけを使わない。

- 付け替える先が、本体の Ex コマンドの名前でも拡張機能のコマンドの ID でもない（省略形の `e` と、別名の `set` も書けない）
- 名前か省略形が、本体の Ex コマンド、別名（`:set`、`:nohlsearch` と、先に書いた別名）、拡張機能のコマンドと重なる。例えば `"editor": { "command": "write", "min": 1 }` は、`:e` が `:edit` と重なる
- `min` が名前の長さより大きい、名前に英字でない文字がある

### コマンドラインの補完

`:` のコマンドラインで `<Tab>` を打つと、行末の語を補い、候補をコマンドラインの真上の一覧に出す（Neovim の `wildmenu` と `wildoptions=pum` と同じ見た目）。
補うのは次のものである。

| 語の位置 | 補うもの | 例 |
|---|---|---|
| コマンドの名前 | 本体の Ex コマンド、別名、拡張機能のコマンド、LSP と DAP の操作の名前 | `:he<Tab>` は `:help`、`:finder.<Tab>` はファインダーのコマンドの一覧 |
| `:e`、`:w`、`:wq`、`:x`、`:tab.new`、`:diffsplit`（と、それらの別名）の引数 | ファイルとディレクトリ | `:e ~/<Tab>`、`:e src/ma<Tab>` |
| `:set` の引数 | 設定の Vim の名前と、`=` の後の値 | `:set nu<Tab>` は `:set number`、`:set no<Tab>` は真偽の設定の一覧、`:set ts=<Tab>` は今の値 |
| `:option.set`、`:option.get`、`:option.toggle` の引数 | 設定の名前と、`:option.set` の値 | `:option.set lineWrap.enabled <Tab>` は `true` と `false` |
| `:b`、`:bd` の引数 | 開いているバッファの名前 | `:b mai<Tab>` |
| `:help` の引数 | `:help` で引ける語 | `:help :option<Tab>` |
| `:ext` の引数 | `sync`、`update` | `:ext <Tab>` |

`|` で続けて書いたコマンドは、最後のコマンドを補う（`:w | e ma<Tab>`）。
補うものの無い所（`:q` の引数、`:s` のパターン、拡張機能のコマンドの引数）では、`<Tab>` はタブ文字として入る（Vim と同じ）。
検索（`/`、`?`）の入力では補わない。

一覧を出している間のキーは次のとおりである。

| キー | 動作 |
|---|---|
| `<Tab>`、`<C-n>`、`<Down>`、`<Right>` | 次の候補を入れる。最後の候補の次は、打った語に戻る |
| `<S-Tab>`、`<C-p>`、`<Up>`、`<Left>` | 前の候補を入れる。最初の候補の前は、打った語に戻る。一覧を出していないときの `<S-Tab>` は、補い始めて最後の候補を入れる |
| `<PageDown>`、`<PageUp>` | 一覧の高さだけ先と前の候補を入れる |
| `<C-e>` | 打った語に戻して一覧を閉じる |
| `<C-y>` | 入れている候補のまま一覧を閉じる |
| `<CR>` | 候補を選んでいれば、`<C-y>` と同じく入れている候補のまま一覧を閉じ、コマンドは実行しない。もう一度 `<CR>` を押すと実行する。何も選んでいない（打った語に戻した、`longest` で共通の先頭まで入れた）ときは実行する |
| ほかのキー | 入れている候補のまま一覧を閉じ、そのキーとして働く |

一覧を出していないとき（候補が 1 つで、入れて一覧を出さなかったときなど）の `<CR>` は、これまでどおり実行する。
ただし、ファイルを補うコマンド（`:e`、`:w`、`:wq`、`:x`、`:tab.new`、`:diffsplit` と、それらの別名）の引数が `/` で終わるとき（`:e src/`）の `<CR>` は、何もしない（Vim と違う）。
ディレクトリの候補を決めた後に `<CR>` を押しても、コマンドラインに残るので、続けて `<Tab>` で中を補える。
補完で入れたパスでも、打ったパスでも同じである。

補い方は設定の `commandLine.completeMode`（Vim の `'wildmode'`）で選ぶ。
本体の既定は Vim と同じ `full`（1 回目の `<Tab>` で最初の候補を入れて一覧を出し、続けて押すと次の候補を入れる）である。
同梱の `config.example.jsonc` は、オーナーの nvim と同じく、1 回目の `<Tab>` で候補の共通の先頭まで入れて何も選ばずに一覧を出し、2 回目から候補を順に入れる設定にしている。

```jsonc
{
  "commandLine": {
    // 1 回目の <Tab> で共通の先頭まで補って一覧を出し、2 回目から順に入れる（nvim の wildmode=longest:full,full）
    "completeMode": "longest:full,full",
    // ファイルの名前の大文字と小文字を区別しない（nvim の wildignorecase）
    "completeIgnoreCase": true,
  },
  // 一覧は 12 行まで（nvim の pumheight=12）
  "completionMenu": { "height": 12 },
}
```

`completeMode` は、コンマで区切った部分（4 つまで）を、`<Tab>` を打った回数で順に使う（最後の部分はその後も続けて使う）。
部分は、次の語をコロンで区切って並べる。

| 語 | 動作 |
|---|---|
| `full` | 次の候補を入れる。一覧を出す |
| `longest` | 候補の共通の先頭まで入れる。`full` が無ければ一覧を出さない（共通の先頭で語が伸びなかったときは、次の部分を使う） |
| `noselect` | 何も入れずに、何も選んでいない一覧を出す |

候補が 1 つだけなら、その候補を入れて一覧を出さない。
ディレクトリを補った後（`:e src/`）にもう一度 `<Tab>` を打つと、そのディレクトリの中を補う。

ファイルの候補は、名前の順に並べ、`.bak`、`~`、`.o`、`.h`、`.info`、`.swp`、`.obj` で終わるファイルを後ろへ回す（Vim の `'suffixes'` の既定）。
`.` で始まる名前は、語が `.` で始まるときだけ出す。
名前の空白、タブ文字、`|`、`$`、`\` と、先頭の `~` の前には `\` を付けて入れる（`:e my\ file.txt`、`:e note\$1.txt`）。
`:e`、`:w`、`:wq`、`:x`、`:tab.new`、`:diffsplit` は、`\` とこれらの文字の組を、その文字そのものとして読むので、補った候補はその名前のファイルを開く。
`:e ~/a` と `:e $HOME/a` の、先頭の `~` と `\` の付かない環境変数は、開くときに展開する（`:e \~` は作業ディレクトリの `~` という名前のファイルを開く）。値の無い環境変数と `~ユーザー名` は展開しない。
補った候補は `~` を残し、環境変数は展開したパスにする（Neovim と同じ）。
大きなディレクトリの候補を集めている間も、ほかのペインのエディタと画面は動く。そのペインで打ったキーは、集め終えて補った後に順に効くので、`<Tab>` のすぐ後に打った `<CR>` は、補った後の一覧で上の表のとおりに働く（候補を選んでいれば決めるだけで、候補が 1 つなら実行する）。`<C-c>` を打つと、補うのをやめる（それまでに打ったキーは捨てる）。
UTF-8 として正しくない名前のファイルは、候補に出さない。

マクロを記録している間に補うと、補った後の文字が記録に入る。
再生すると、`<Tab>` を打たずに同じコマンドラインになる（Vim は `<Tab>` を記録し、再生ではタブ文字が入る）。

### コマンドラインを画面の上に出す

設定の `commandLine.popup` を `true` にすると、`:` と `/` と `?` の入力を、画面の最後の行の代わりに、画面の上の中央の枠に出す（nvim の noice.nvim の `cmdline_popup` と `presets.command_palette` と同じ置き方）。
補完の候補の一覧は、入力の枠のすぐ下に、入力の枠と同じ幅で出す。
本体の既定は `false`（Vim と同じく最後の行に出す）で、同梱の `config.example.jsonc` は `true` にしている。

```jsonc
{
  "commandLine": { "popup": true },
  "symbols": {
    // 枠線（浮かべる窓と同じ記号）。書かなければ ASCII の枠線（+、-、|）で描く
    "popup.border": "╭─╮│╯─╰│",
  },
}
```

| 項目 | 出し方 |
|---|---|
| 入力の枠 | 枠の上の端を画面の上から 3 行目（タブの行を出していても同じ行）に置き、横は画面の中央に置く。入力の欄は 60 列で、入力が長くなれば画面の幅まで広げ、それでも収まらなければ先頭を隠す。入力の行の頭には、最後の行に出すときと同じく `:`、`/`、`?` を出す |
| 題 | 枠の上の枠線の中央に、入力の種類の記号（`commandLine.title.command`、`commandLine.title.searchForward`、`commandLine.title.searchBackward`。既定は `Cmdline`、`Search forward`、`Search backward`）を出す。色は、コマンドラインが `commandLine.popup.border`、検索が `commandLine.popup.border.search` で、枠線も同じ色で描く |
| 候補の一覧 | 入力の枠のすぐ下の枠に、15 行まで出す。`completionMenu.height` を書いていれば、その行の数までにする。画面が低ければ、ステータス行と最後の行の上にも重ねて、入る行の数だけ出す（noice.nvim と同じ）。選び方のキーは[コマンドラインの補完](#コマンドラインの補完)と同じ |
| 最後の行 | 入力の間は空ける。入力を終えた後のメッセージ（誤り、`:w` の結果、検索の一致の件数）は、今までどおり最後の行に出す |
| 狭い画面 | 最後の行の上に枠を置けないほど低い画面（タブの行を除いて 4 行より低い画面。入力の枠の 3 行と最後の行が要る）と、入力の欄が 2 列も取れないほど狭い画面では、最後の行に出す |

枠は、浮かべる窓、進み具合、スクロールバー、ステータス行の上に重ねて出す。
枠の上でマウスのボタンを押しても何もしない。
打つたびの検索の一致が、画面を送らずに見える行（画面の上から 3 行目から 5 行目。一覧を出していればその下も）にあると、一致は枠の下に隠れる（枠の行を避けて画面を送ることはしない。noice.nvim の nvim も送らない）。
画面の外の遠くの一致（200 行のファイルの 150 行目など）へ画面を送るときは、一致が画面の中ほどに出るので隠れない。

## 実行中に設定を変える

[設定の名前の一覧](#設定の名前の一覧)にある設定の項目は、実行中に Ex コマンドで変えたり読んだりできる。

| コマンド | 動作 |
|---|---|
| `:option.set 名前 値` | 項目の値を変える。真偽は `true` か `false`、整数は 10 進、文字列は名前の後の空白の後の行末まで（`\|` も値に含む。空も書ける）。値の末尾の空白は、型によらず除く |
| `:option.get 名前` | 今の値を `名前=値` の形でコマンドラインの行に出す（`filetype=go`） |
| `:option.toggle 名前` | 真偽の項目の値を反転する（`true` なら `false`、`false` なら `true`）。真偽でない項目は `E488` のエラーになる |

名前は一覧の正規の名前で書き、省略できない。
範囲がバッファごとの項目は、カーソルのあるバッファの値を変える、または読む。
知らない名前は `E518`、真偽の項目に `true` と `false` でない値を書くと `E474`、整数の項目に整数でない値を書くと `E521` のエラーになる。
範囲のある整数の項目（`lineNumber.minWidth`）に範囲の外の値を書くと、下限より小さければ `E487`、上限より大きければ `E474` のエラーになる。

`:set`（`:se`）は、Vim の `:set` の書き方の一部を受け付ける別名で、`:option.set`、`:option.get`、`:option.toggle` の呼び出しに変換して実行する。
名前は、一覧の「`:set` の名前」の列の Vim の名前で書く。

| 書き方 | 変換先 |
|---|---|
| `:set 名前` | 真偽の項目なら `:option.set 正規の名前 true`。それ以外の項目は今の値を出す（`:option.get 正規の名前`。Vim と同じ） |
| `:set no名前` | `:option.set 正規の名前 false`。真偽の項目だけに書ける |
| `:set 名前!` | `:option.toggle 正規の名前`。真偽の項目だけに書ける |
| `:set 名前=値` | `:option.set 正規の名前 値` |
| `:set 名前?` | `:option.get 正規の名前` |
| `:set 名前 名前 …` | 空白で区切った引数ごとに上のどれかへ変換し、左から順に実行する。途中の引数が誤りなら、そこで止まる（前の引数の設定は残る） |

`:set` は Vim の書き方をすべて受け付けるわけではない。
Vim の短い名前（`ft`）、`inv` の接頭辞、`+=`、`-=`、`^=`、`&`（既定値に戻す）は `E518`（知らない設定）、引数の無い `:set` は `E471`、`:setlocal` は `E492`（コマンドではない）になる（[vim-differences.md](vim-differences.md)）。
ただし字下げの 4 項目は、短い名前（`et`、`sw`、`ts`、`ai`）も受け付ける（`:set et sw=4 ts=4`）。
コメントの形も、短い名前（`cms`）を受け付ける。
カーソルの上下と左右の余白も、短い名前（`so`、`siso`）を受け付ける（`:set so=5 siso=8`）。
`:set` の値の中の `\` は、次の 1 文字をそのまま値に入れる（Vim と同じ）。
空白を含む値は `:set cms=//\ %s` と書き、`\` そのものは `\\` と書く。値の末尾の `\` は、そのまま値に残る（`:set cms=\` は `%s` を含まないので `E537`。Vim と同じ）。
ただし値の末尾の空白は `:option.set` と同じく除くので、`:set cms=%s\ ` は `%s` になる（Vim は `%s ` にする）。

例えば、空のバッファから Go のコードを書き始めるときに `:set filetype=go` を打つと、ステータス行の `filetype` の部品（[ステータス行の並び](#ステータス行の並び)）が `go` を出し、ファイルの種類を契機にする拡張機能（LSP、tree-sitter）が起動する。
`:option.set filetype go` も同じである。
ただし、LSP の拡張機能は名前の無いバッファを言語サーバーへ送らないので、診断、ホバーなどの言語サーバーの機能は、`:w ファイル` で名前を付けた後に使える（名前の無いバッファで `:hover` を打つと「このバッファには言語サーバーが無い」と出る）。
構文の色（tree-sitter）は、名前の無いバッファでも付く。

### 設定の名前の一覧

| 名前 | 型 | 範囲 | `:set` の名前 | 内容 |
|---|---|---|---|---|
| `filetype` | 文字列 | バッファごと | `filetype` | バッファのファイルの種類。初めの値は、開いたときに設定の `fileTypes` の対応表と自動の判定で決まる。名前の無いバッファで決めた種類は、`:w ファイル` で名前を付けても残る。`:e` と `:e!` でファイルを開き直すと、開いたときの判定に戻る（外での変更の読み直しでは戻らない。Vim と同じ） |
| `lineNumber.show` | 真偽 | エディタ全体 | `number` | バッファの表示領域の左に行番号の列を出すか。既定は `false`（出さない。Vim と同じ）。列は印の列の右、本文の左に置き、色の名前は `linenr` である（[配色](#配色)） |
| `lineNumber.minWidth` | 整数 | エディタ全体 | `numberwidth` | 行番号の列の最小の列数（番号の右の空けた 1 列を含む）。既定は `4`、範囲は 1 から 20。列の幅は、この値と、バッファの最後の行の番号の桁数に 1 を足した数の大きい方になる（Vim の `'numberwidth'` と同じ） |
| `lineWrap.enabled` | 真偽 | エディタ全体 | `wrap` | 長い行を本文の幅で折り返して描くか（[長い行の折り返し](#長い行の折り返し)）。既定は `false`（折り返さずに横にスクロールする。Vim と Neovim の既定は折り返す）。同梱の `config.example.jsonc` は `true` にしている |
| `whitespace.show` | 真偽 | エディタ全体 | `list` | タブ文字、空白、ノーブレークスペース、行末を記号で描くか（[空白の記号](#空白の記号)）。既定は `false`（描かない。Vim と同じ）。記号は `symbols` の `whitespace.` で始まる名前で変える（[記号](#記号)）。同梱の `config.example.jsonc` は `true` にしている |
| `scroll.margin` | 整数 | エディタ全体 | `scrolloff`、`so` | カーソルの行の上と下に残す余白の行数（[スクロール](#スクロール)）。行を折り返すときは見た目の行で数える。既定は `0`（Vim と Neovim と同じ）、範囲は 0 から 9999。表示領域の半分以上にすると、カーソルの行を常に中ほどに置く。同梱の `config.example.jsonc` は `5` にしている |
| `scroll.sideMargin` | 整数 | エディタ全体 | `sidescrolloff`、`siso` | 行を折り返さないときに、カーソルの左と右に残す余白の列数（[スクロール](#スクロール)）。既定は `0`（Vim と Neovim と同じ）、範囲は 0 から 9999。同梱の `config.example.jsonc` は `8` にしている |
| `search.highlight` | 真偽 | エディタ全体 | `hlsearch` | 直前の検索のパターンに一致する範囲に色を付けるか（[検索の一致の色付け](#検索の一致の色付け)）。既定は `false`（付けない。Vim と同じ）。色の名前は `search` である（[配色](#配色)） |
| `search.incrementalTimeoutMs` | 整数 | エディタ全体 | 無し | `/` と `?` でパターンを打っている間の、打つたびの検索（[打つたびの検索](#打つたびの検索)）の時間の上限のミリ秒。超えたらその回は一致を出さない。既定は `500`（Vim の `'incsearch'` が約 0.5 秒で打ち切るのと同じ）、範囲は 1 から 10000。Vim にこの設定は無いので、`:set` では変えられず、`:option.set search.incrementalTimeoutMs 1000` で変える |
| `search.showCount` | 真偽 | エディタ全体 | 無し | 検索で動いたときに、メッセージの行の右に、一致が何件目かと件数（`[2/5]`）を出すか（[検索の一致の件数](#検索の一致の件数)）。既定は `false`（出さない。Vim と同じ）。同梱の `config.example.jsonc` は `true` にしている。Vim の `'shortmess'` の `S` に当たるが、`:set shortmess` の別名は無いので、`:option.set search.showCount true` か `:option.toggle search.showCount` で変える |
| `yank.highlightMs` | 整数 | エディタ全体 | 無し | `y` でヤンクした範囲に色を付けておくミリ秒（[ヤンクした範囲の色付け](#ヤンクした範囲の色付け)）。既定は `0`（付けない）、範囲は 0 から 10000。色の名前は `yank` である（[配色](#配色)）。Vim にこの設定は無いので、`:set` では変えられず、`:option.set yank.highlightMs 300` で変える |
| `preview.minWindowWidth` | 整数 | エディタ全体 | 無し | ファジーファインダーとファイラの項目のプレビュー（[項目のプレビュー](#項目のプレビュー)）を出す窓の幅の境目の列数。窓の幅がこの列数を超えるときだけ、右にプレビューを出す。既定は `100`（fzf-lua の `flip_columns` の既定と同じ）、範囲は 0 から 10000 |
| `preview.widthPercent` | 整数 | エディタ全体 | 無し | プレビューを出すときに、窓の幅のうちプレビューに使う百分率。既定は `60`（fzf-lua の `right:60%` と同じ）、範囲は 1 から 99 |
| `preview.windowWidthPercent` | 整数 | エディタ全体 | 無し | プレビューを出すときの、中央のポップアップの窓の幅の、画面の幅に対する百分率。既定は `90`、範囲は 10 から 100。tmux と herdr のポップアップに出すときも、この幅でポップアップを開く。分割したペインでは読まない（ペインの全体が窓である） |
| `icons.show` | 真偽 | エディタ全体 | 無し | ファジーファインダーとファイラの、ファイルとディレクトリの項目の前に、ファイルの種類のアイコンを付けるか（[アイコン](#アイコン)）。既定は `true`（付ける）。Nerd Font の入っていない環境では `false` にする。アイコンの表は `icons` のほかの項目で書き換える |
| `indent.useSpaces` | 真偽 | バッファごと | `expandtab`、`et` | 字下げをスペースで作るか（[字下げとタブの幅](#字下げとタブの幅)）。既定は `false`（タブ文字で作る。Vim と同じ）。インサートモードの `<Tab>` と `<BS>`、`>` と `<`、自動インデントの字下げの作り方が従う |
| `indent.width` | 整数 | バッファごと | `shiftwidth`、`sw` | 字下げの 1 段の幅。既定は `8`（Vim と同じ）、範囲は 0 から 9999。`0` なら `tab.width` の値を使う（Vim の `shiftwidth=0` と同じ） |
| `tab.width` | 整数 | バッファごと | `tabstop`、`ts` | タブ文字の表示幅。既定は `8`（Vim と同じ）、範囲は 1 から 9999（Vim と同じ）。描画、カーソルの列、`gj` と `gk`、字下げの幅の数え方が使う |
| `indent.auto` | 真偽 | バッファごと | `autoindent`、`ai` | 自動インデントを行うか。インサートモードの `<CR>`、`o`、`O`、行単位の `c`（`cc`、`S`）で、前の行の字下げを写す。既定は `false`（Vim と同じ） |
| `editorConfig.enabled` | 真偽 | エディタ全体 | 無し | 開いたファイルに当たる `.editorconfig` を読むか。既定は `true`（読む。Neovim と同じ）。変えた後に開いたバッファから効く |
| `undo.persist` | 真偽 | エディタ全体 | `undofile`、`udf` | ファイルのアンドゥの履歴を、保存するときに残し、次に開いたときに読み戻すか（[アンドゥの履歴とカーソルの位置を残す](#アンドゥの履歴とカーソルの位置を残す)）。既定は `false`（残さない。Vim と同じ）。同梱の `config.example.jsonc` は `true` にしている |
| `cursor.restore` | 真偽 | エディタ全体 | 無し | ファイルを閉じたときのカーソルの位置を残し、次に開いたときにその位置へ置くか（[アンドゥの履歴とカーソルの位置を残す](#アンドゥの履歴とカーソルの位置を残す)）。既定は `false`（戻さない）。同梱の `config.example.jsonc` は `true` にしている |
| `comment.string` | 文字列 | バッファごと | `commentstring`、`cms` | 行のコメントの形（[コメントの切り替え](#コメントの切り替え)）。`%s` の前がコメントの前に、後ろがコメントの後ろに付く（`// %s`、`/* %s */`）。既定は空（Neovim と同じ）で、ファイルの種類ごとの既定がある。空でなく `%s` を含まない値は `E537` のエラーになる |
| `cursorHold.delayMs` | 整数 | エディタ全体 | 無し | キーを打たずにこのミリ秒が経ったら、カーソルが止まったとする時間（Vim の `'updatetime'` の `CursorHold` の時間。[診断のポップアップ](#診断のポップアップ)）。既定は `4000`（Vim と同じ）、範囲は 0 から 60000。同梱の `config.example.jsonc` は `100` にしている。`:set updatetime` の別名は無いので、`:option.set cursorHold.delayMs 100` で変える |
| `diagnostic.popup` | 真偽 | エディタ全体 | 無し | カーソルが止まったときに、カーソルの行の診断をカーソルの近くのポップアップに出すか（[診断のポップアップ](#診断のポップアップ)）。既定は `false`（出さない。Vim と Neovim と同じ）。`true` なら、メッセージの行には診断の本文を出さない。同梱の `config.example.jsonc` は `true` にしている |
| `progress.show` | 真偽 | エディタ全体 | 無し | 拡張機能の進み具合を、画面の右下に重ねて出すか（[拡張機能の進み具合](#拡張機能の進み具合)）。既定は `true`（出す）。Vim と Neovim に対応する設定は無い |
| `progress.doneMs` | 整数 | エディタ全体 | 無し | 終わった進み具合を、終わった印を付けて出しておくミリ秒。既定は `3000`（fidget.nvim の `done_ttl` の既定と同じ）、範囲は 0 から 60000。`0` なら終わったらすぐに消す |
| `mouse.enabled` | 真偽 | エディタ全体 | 無し | マウスを使うか（[マウス](#マウス)）。既定は `false`（使わない。Vim と同じ）。同梱の `config.example.jsonc` は `true`（Neovim の `mouse=a` に当たる）にしている。Vim の `'mouse'` はモードの文字の並びなので、`:set mouse` の別名は無い。`:option.toggle mouse.enabled` で切り替える |
| `mouse.scrollLines` | 整数 | エディタ全体 | 無し | ホイールを 1 回回したときに送る行数（[マウス](#マウス)）。既定は `3`（Neovim の `'mousescroll'` の `ver:3`）、範囲は 0 から 9999。`0` なら送らない |
| `mouse.scrollColumns` | 整数 | エディタ全体 | 無し | 行を折り返さないときに、ホイールを 1 回傾けたときに横に送る列数（[マウス](#マウス)）。既定は `6`（`'mousescroll'` の `hor:6`）、範囲は 0 から 9999。`0` なら送らない |
| `scrollbar.show` | 真偽 | エディタ全体 | 無し | 画面の右端の列に、スクロールバーと、診断と検索の一致の位置の印を出すか（[スクロールバー](#スクロールバー)）。既定は `false`（出さない。Vim と Neovim と同じ）。同梱の `config.example.jsonc` は `true` にしている |
| `scrollbar.currentOnly` | 真偽 | エディタ全体 | 無し | スクロールバーを、最後にキーを打った client の画面にだけ出すか。既定は `false`（どの client の画面にも出す。nvim-scrollview の `current_only` の既定と同じ）。同梱の `config.example.jsonc` は `true` にしている |
| `scrollbar.marks` | 文字列 | エディタ全体 | 無し | スクロールバーに出す印の種類を、コンマで区切って並べたもの。`diagnostic`（診断）と `search`（検索の一致）を書ける。知らない名前は読み飛ばし、空なら印を出さない。既定は `diagnostic,search` |
| `scrollbar.excludedFileTypes` | 文字列 | エディタ全体 | 無し | スクロールバーを出さないバッファのファイルの種類（`filetype` の値）を、コンマで区切って並べたもの。既定は空（どのファイルの種類にも出す） |
| `commandLine.completeMode` | 文字列 | エディタ全体 | `wildmode` | コマンドラインの `<Tab>` の補い方（[コマンドラインの補完](#コマンドラインの補完)）。Vim の `'wildmode'` と同じ書き方で、語は `full`、`longest`、`noselect`。既定は `full`（Vim と同じ）。同梱の `config.example.jsonc` は `longest:full,full` にしている |
| `commandLine.completeIgnoreCase` | 真偽 | エディタ全体 | `wildignorecase` | コマンドラインの補完で、ファイルとディレクトリの名前の大文字と小文字を区別しないか。既定は `false`（区別する。Vim と同じ）。同梱の `config.example.jsonc` は `true` にしている |
| `commandLine.popup` | 真偽 | エディタ全体 | 無し | `:` と `/` と `?` の入力を、画面の最後の行の代わりに、画面の上の中央の枠に出すか（[コマンドラインを画面の上に出す](#コマンドラインを画面の上に出す)）。既定は `false`（最後の行に出す。Vim と Neovim と同じ）。同梱の `config.example.jsonc` は `true` にしている |
| `completionMenu.height` | 整数 | エディタ全体 | `pumheight` | 補完の候補の一覧に出す行の数の上限。既定は `0`（画面に収まるだけ出す。Vim と同じ）、範囲は 0 から 9999。同梱の `config.example.jsonc` は `12` にしている |
| `intro.show` | 真偽 | エディタ全体 | 無し | 引数なしで起動したときに、画面の中央に起動の案内を出すか（[起動の案内](#起動の案内)）。既定は `true`（出す。Vim と Neovim と同じ）。Vim の `'shortmess'` の `I` に当たるが、`:set shortmess` の別名は無いので、設定ファイルに `"intro": { "show": false }` と書く。実行中に `false` にすると案内が消え、`true` に戻しても出ない |

設定ファイルには、`filetype` のほかの項目を、点で区切った名前を入れ子のキーにして書ける（名前が `a.b` の項目なら `"a": { "b": 値 }`）。
範囲がエディタ全体の項目はエディタの初めの値に、範囲がバッファごとの項目はバッファを開くときの初めの値になる。
`filetype` は、初めの値をバッファを開くときにファイルから判定して決めるので、設定ファイルには書けない（書くと誤りとして知らせる）。
範囲がバッファごとの項目は、[字下げとタブの幅](#字下げとタブの幅)の `fileTypeOptions` でファイルの種類ごとにも書ける。

### 行番号

行番号の列は、設定ファイルに次のように書くと、起動したときから出る。
同梱の `config.example.jsonc` もこの値にしている。

```jsonc
{
  "lineNumber": { "show": true, "minWidth": 5 },
}
```

実行中は `:set number`（出す）、`:set nonumber`（消す）、`:set number!`（切り替える）で変え、`:set numberwidth=5` で最小の列数を変える。
`:option.set lineNumber.show true` と `:option.set lineNumber.minWidth 5` も同じである。
変えた値は、次に画面を描くときから効く。

列の幅は、画面に見えている行ではなく、バッファの最後の行の番号で決まる。
このため、スクロールしても幅は変わらず、行数の桁が上がったとき（`minWidth` が `4` なら 999 行から 1000 行になったとき）に 1 列広がって本文の位置がずれる。
ずれを避けたいときは `minWidth` を大きくする（`5` なら 9999 行まで幅が変わらない）。

`minWidth` の範囲の外の値は誤りにして、値を変えない。
`:set numberwidth=0` のように 1 より小さい値は `E487`、21 以上の値は `E474` のエラーになる（Vim と同じ番号）。
整数に収まらない大きな値（`:set numberwidth=99999999999999999999`）も、正なら `E474`、負なら `E487` になる。
設定ファイルに範囲の外の値を書くと、起動後に位置と内容を知らせ、既定値の `4` を使う。

行番号を出すのはバッファの表示領域だけで、浮かべた窓（ホバーの説明、ファインダーなど）、出力の欄、拡張機能のペインには出さない。
見比べている間（[2 つのファイルを見比べる](#2-つのファイルを見比べる)）の詰め物の行と、最終行より後の `~` の行にも番号を出さない（詰め物の行の行番号の列は、Vim と同じく `linenr` の色の空白で埋める）。
長い行を折り返したとき（[長い行の折り返し](#長い行の折り返し)）の続きの見た目の行にも番号を出さない（同じく `linenr` の色の空白で埋める）。

### 長い行の折り返し

長い行を、本文の幅（印の列と行番号の列を除いた幅）で折り返して描ける（Vim の `'wrap'`）。
本体の既定は折り返さず、長い行は横にスクロールする（Vim と Neovim の既定は折り返す）。
設定ファイルに次のように書くと、起動したときから折り返す。
同梱の `config.example.jsonc` もこの値にしている。

```jsonc
{
  "lineWrap": { "enabled": true },
}
```

実行中は `:set wrap`（折り返す）、`:set nowrap`（折り返さない）、`:set wrap!`（切り替える）で変える。
`:option.set lineWrap.enabled true` と `:option.toggle lineWrap.enabled` も同じである。
変えた値は、次に画面を描くときから効く。

折り返す位置は文字の切れ目で、単語の切れ目では折り返さない（Vim の `'linebreak'` がオフのときと同じ）。
見せ方は Neovim の既定に合わせてある。

- 全角の文字が見た目の行の最後の 1 列に入らなければ、その列に `>` を出し、文字を次の見た目の行へ送る
- タブは、見た目の行の右の端で分けて、残りを次の見た目の行に描く
- 続きの見た目の行の頭には、記号 `lineWrap.showBreak`（[記号](#記号)。既定は空）を付ける
- 行番号と印は、行の最初の見た目の行にだけ出す
- 画面の最後に収まりきらない行は、入るところまで描き、最後の行の右の端に `@@@` を出す
- 画面より高い行にカーソルがあるときは、カーソルの見た目の行が見えるように、行の途中から描き、画面の最初の行の左の端に `<<<` を出す
- 折り返している間は、横にスクロールしない

構文の色、検索の一致の色、ヤンクした範囲の色、選択の色、診断の下線は、見た目の行をまたいでも続けて付く。
ファジーファインダーとファイラの項目のプレビュー（[項目のプレビュー](#項目のプレビュー)）の中身も、同じ設定で折り返す。
ホバーとヘルプのポップアップは、設定に依らず窓の幅で折り返す（今までと同じ）。

見た目の行で上下に動くキーは次のとおりである（Vim の `gj` と `gk`）。

| キー | 操作の ID | 動き |
|---|---|---|
| `gj` | `display-down` | 見た目の行で 1 行下へ。回数があれば、その数の見た目の行だけ下へ |
| `gk` | `display-up` | 見た目の行で 1 行上へ。回数があれば、その数の見た目の行だけ上へ |

`gj` と `gk` は、見た目の行の左の端から同じ列の文字へ動き、その列に文字が無ければ見た目の行の最後の文字へ動く。
`$` の後は、見た目の行の右の端の文字へ動く。
オペレーターの後（`dgj`）では、文字単位の範囲になる（`$` の後は最後の文字を含む）。
折り返さないときは、`j` と `k` と同じに動く（オペレーターの後は行単位）。
`j` と `k` は、折り返していても実際の 1 行ずつ動き、`$`、`0`、`^` は実際の行の端へ動く（Vim と同じ）。
`j` と `k` を見た目の行で動かすには、`keymap` に `"normal": { "j": { "action": "display-down" }, "k": { "action": "display-up" } }` と書く（ビジュアルモードでも使うなら `visual` にも書く）。
スクロールのキー（[スクロール](#スクロール)）は、折り返した行の高さ（見た目の行の数）を数えて動く。

### 空白の記号

タブ文字、空白、ノーブレークスペース（U+00A0 と U+202F）、行末を、記号で描ける（Vim の `'list'` と `'listchars'`）。
本体の既定は描かない（Vim と同じ）。
設定ファイルに次のように書くと、起動したときから描く。
同梱の `config.example.jsonc` もこの値にしている。

```jsonc
{
  "whitespace": { "show": true },
  "symbols": {
    "whitespace.tab": "→ ",
    "whitespace.lead": "·",
    "whitespace.trail": "·",
    "whitespace.eol": "↲",
    "whitespace.nbsp": "",
  },
}
```

実行中は `:set list`（描く）、`:set nolist`（描かない）、`:set list!`（切り替える）で変える。
`:option.set whitespace.show true` と `:option.toggle whitespace.show` も同じである。
変えた値は、次に画面を描くときから効く。

記号は `symbols` の 6 つの名前で変える（[記号](#記号)）。
書かなかった名前は既定の記号（Neovim の `'listchars'` の既定の `tab:> ,trail:-,nbsp:+` と同じ）のままである。
Vim の `:set listchars=...` と違い、書かなかった項目は無くならないので、描きたくない項目には空の文字列を書く（上の例の `whitespace.nbsp`）。

| 名前 | Vim の `'listchars'` | 描くもの |
|---|---|---|
| `whitespace.tab` | `tab` | タブ文字。2 文字（`→ `）なら、最初の列に 1 文字目、残りの列に 2 文字目を描く。3 文字（`<->`）なら、最後の列に 3 文字目、最初の列に 1 文字目、間に 2 文字目を描く。空ならタブ文字を `^I` で描き、タブ文字の幅を 2 列として数える（Vim と同じ） |
| `whitespace.lead` | `lead` | 行頭の空白（行の先頭から続く空白とタブ文字の中の空白）。空なら `whitespace.space` を使う |
| `whitespace.trail` | `trail` | 行末の空白（空白でもタブ文字でもない最後の文字より後ろの空白）。空なら `whitespace.space` を使う。空白とタブ文字だけの行の空白は、行末の空白として描く |
| `whitespace.space` | `space` | ほかの空白 |
| `whitespace.nbsp` | `nbsp` | ノーブレークスペース |
| `whitespace.eol` | `eol` | 行末（行の最後の文字の右の 1 列）。空の行にも描く |

どの記号も、表示幅が 1 の文字で書く（Vim と同じ）。
`·`、`→`、`↲` のような、Unicode で幅が曖昧と定められた文字は、本体は 1 列と数える（[記号](#記号)の注意と同じ）。
端末がそれらの文字を 2 列で描く設定だと、列がずれる。

記号を描いている間は、次のように動く（Vim と同じ）。

- ノーマルモードでも、タブ文字の上のカーソルはタブの最初の列に出る（描かないときは最後の列に出る）。`j` と `k` で動くときの列と、ルーラーの列もその列になる
- 長い行を折り返すとき（[長い行の折り返し](#長い行の折り返し)）は、行末の記号も 1 列として数える。ちょうど本文の幅で終わる行は、行末の記号を次の見た目の行に描き、その行を使う
- 折り返さずに横にスクロールしたときは、画面の左の端より短い行（空の行を含む）の行末の記号を、画面の左の端に描く

色の名前は、行末の記号が `nontext`、ほかの記号が `whitespace`、`^I` が `specialkey` である（[配色](#配色)）。
コメントや文字列の中の記号も、構文の色でなく記号の色で描く（Vim と Neovim と同じ）。
診断の下線、検索の一致、選択の色は、記号の上に重ねて付く。
検索の一致の文字の色は記号の色より上に出る（Vim と同じ。Neovim は記号の色を出す。[vim-differences.md](vim-differences.md)）。
記号を描くのはバッファの表示領域だけで、浮かべた窓（ホバーの説明、ファインダー、プレビュー）と出力の欄には描かない。

### スクロール

Vim のスクロールのキーは、次のとおりである。
どれもノーマルモードとビジュアルモードで使え、オペレーターの後では誤りになる（`d<C-d>` は何も消さない）。
動きは Neovim の既定に合わせてあり、カーソルの列は、`j` と `k` と同じく望みの列で決まる（Neovim の既定の `'nostartofline'`）。

| キー | 操作の ID | 動き |
|---|---|---|
| `<C-e>` / `<C-y>` | `scroll-line-down` / `scroll-line-up` | 表示領域を 1 行（回数があればその行数）下 / 上へ送る。カーソルは、画面から出るときだけ画面の端の行へ動く。バッファの端では何もしない（誤りにしない） |
| `<C-d>` / `<C-u>` | `half-page-down` / `half-page-up` | 表示領域とカーソルを、半画面（Vim の `'scroll'`）だけ下 / 上へ送る。回数を付けると、送る行数をその回数にし、端末の大きさが変わるまで、回数の無い `<C-d>` と `<C-u>` もその行数で送る（Vim と同じ）。下へは、最後の行の後に `~` の行が出るほどは送らない。バッファの端で何も動かなければ誤りになる |
| `<C-f>` か `<PageDown>` / `<C-b>` か `<PageUp>` | `page-down` / `page-up` | 1 画面（回数があればその画面数）下 / 上へ送る。前の画面の 2 行を重ねて残す。カーソルは、下へなら画面の最初の行、上へなら最後の行へ移る |
| `zt` / `zz` / `zb` | `scroll-cursor-top` / `scroll-cursor-center` / `scroll-cursor-bottom` | カーソルの行を、画面の上の端 / 中ほど / 下の端に置く。回数を付けると、カーソルをその行へ移してから置く（移る前の位置はジャンプリストに残る） |
| `z<CR>` / `z.` / `z-` | `scroll-cursor-top-first-nonblank` / `scroll-cursor-center-first-nonblank` / `scroll-cursor-bottom-first-nonblank` | `zt` / `zz` / `zb` と同じに置き、カーソルを行の最初の空白でない文字へ移す |

行を折り返すとき（[長い行の折り返し](#長い行の折り返し)）は、`<C-d>`、`<C-u>`、`<C-f>`、`<C-b>` が送る量を見た目の行で数え、`<C-d>` と `<C-u>` はカーソルも見た目の行で動かす（`gj` と `gk` と同じ）。
`<C-e>` と `<C-y>` は、折り返していても実際の行で送る（Neovim の既定の `'nosmoothscroll'` と同じ）。
フォーカスを入れたポップアップ（[ポップアップ](#ポップアップ)）と、フォーカスを移したプレビュー（[項目のプレビュー](#項目のプレビュー)）の中でも、同じキーで同じに動く。
キーは、設定の `keymap` に操作の ID を書いて変えられる（[操作の ID](#操作の-id)）。

カーソルの上下に余白を残してスクロールさせるには、`scroll.margin`（Vim の `'scrolloff'`）に行数を書く。
行を折り返さないときに、カーソルの左右に余白を残して横にスクロールさせるには、`scroll.sideMargin`（Vim の `'sidescrolloff'`）に列数を書く。
本体の既定はどちらも `0` で、余白を残さない（Vim と Neovim と同じ）。
同梱の `config.example.jsonc` は次の値にしている。

```jsonc
{
  "scroll": { "margin": 5, "sideMargin": 8 },
}
```

実行中は `:set scrolloff=5`（`:set so=5`）と `:set sidescrolloff=8`（`:set siso=8`）で変える。
`:option.set scroll.margin 5` も同じである。
変えた値は、次に画面を描くときから効く。

余白は、カーソルをどのキーで動かしたとき（`j`、`k`、`/` と `n`、`G`、`<C-o>`、`gj`、スクロールのキー）も同じに効く。
バッファの先頭と末尾の近くでは、残せる分だけ残す。
行を折り返すときは、見た目の行で数える。
表示領域の半分以上の値（`999` など）を書くと、カーソルの行を常に画面の中ほどに置く（Vim と同じ）。
横の余白は、カーソルが余白から出たら、カーソルを画面の幅の半分の位置まで送る（Vim の既定の `'sidescroll'` が `0` のときと同じ。Neovim の既定の `1` と違い、1 列ずつは送らない）。

### マウス

マウスを使うには、設定に `mouse.enabled` を書く（本体の既定は Vim と同じく使わない）。
同梱の `config.example.jsonc` は使う設定にしている（オーナーの nvim の `mouse=a` に当たる）。

```jsonc
{
  "mouse": { "enabled": true },
}
```

実行中は `:option.toggle mouse.enabled` か `:option.set mouse.enabled true` で切り替え、次に画面を描くときから効く。
使っている間は、端末がマウスの出来事をエディタへ送るので、端末の側でテキストを選んでコピーするには、端末ごとの決まりの操作を使う（xterm の既定では、Shift を押しながら操作すると、出来事をエディタへ送らずに端末が扱う）。
`:!` でコマンドを動かしている間と、エディタを終えた後は、端末にマウスの出来事を送らせない。

動きは Neovim の `mouse=a` に合わせてある。どれもノーマルモード、ビジュアルモード、インサートモードで使える。

| 操作 | キー | 操作の ID | 動き |
|---|---|---|---|
| 左のクリック | `<LeftMouse>` | `mouse.click` | その位置へカーソルを移す。行末より右は最後の文字（インサートモードでは行末）、行番号の列は行の先頭、最後の行より下の `~` の行は最後の行にする。ビジュアルモードでは選択を終えてから移る。インサートモードではインサートモードのまま移る。タブの行のタブの見出しなら、そのタブへ移る |
| 左のドラッグ | `<LeftDrag>`、`<LeftRelease>` | `mouse.drag`、`mouse.release` | 押した位置から離した位置までを、文字単位のビジュアルモードで選ぶ。行末より右へ動かすと改行まで選ぶ。ステータス行やタブの行まで動かすと、その分だけ画面を送る。インサートモードでは、インサートモードを抜けてから選ぶ |
| ダブルクリック | `<2-LeftMouse>` | `mouse.select-word` | その位置の単語を選ぶ（`viw` と同じ範囲） |
| トリプルクリック | `<3-LeftMouse>` | `mouse.select-line` | その位置の行を行単位で選ぶ（`V` と同じ） |
| ホイール | `<ScrollWheelDown>`、`<ScrollWheelUp>` | `mouse.scroll-down`、`mouse.scroll-up` | 画面を `mouse.scrollLines`（既定は 3）行だけ送る。カーソルは、`<C-e>` と `<C-y>` と同じく、画面から出るときだけ動く |
| 横のホイール | `<ScrollWheelRight>`、`<ScrollWheelLeft>` | `mouse.scroll-right`、`mouse.scroll-left` | 行を折り返さないとき、画面を `mouse.scrollColumns`（既定は 6）列だけ横に送る |

続けて押したとみなすのは、同じ位置を 0.5 秒より短い間に押したときである（Neovim の既定の `'mousetime'` と同じ）。
4 回目のクリックは 1 回目と同じに働く（Neovim の矩形の選択は本体に無い）。
ホイールは、ステータス行とコマンドラインの行の上で回しても画面を送る（Neovim と同じ）。
フォーカスを入れたポップアップ（ヘルプ、キーの一覧など）の中も、同じクリックとホイールで動かせる。
ファイラのペインとポップアップ（[ファイラ](#ファイラ)）でも、クリックした項目へカーソルが移る。項目を開くのは、今までどおり `<CR>` や `l` で行う。
右端のスクロールバー（[スクロールバー](#スクロールバー)）を出しているときは、同じ左のクリックとドラッグで、つまみを動かし、印を押してその行へ移れる。

マウスの出来事は、ほかのキーと同じく設定の `keymap` で割り当てを変えられる（[操作の ID](#操作の-id)）。
既定で割り当てていない右と中のボタン（`<RightMouse>`、`<MiddleMouse>` など）と、修飾キーを付けた出来事（`<S-LeftMouse>`、`<C-ScrollWheelUp>` など）も割り当てられる。

```jsonc
{
  "keymap": {
    "normal": {
      // 右のクリックでもカーソルを移す
      "<RightMouse>": { "action": "mouse.click" },
      // ホイールを 1 回回すと 1 行だけ動く
      "<ScrollWheelDown>": "<C-e>",
      "<ScrollWheelUp>": "<C-y>",
    },
  },
}
```

### スクロールバー

画面の右端の 1 列に、今見ている範囲の位置と大きさを表すつまみと、診断と検索の一致の位置の印を出せる（Neovim の nvim-scrollview に当たる）。
本体の既定は Vim と Neovim と同じく出さない。
同梱の `config.example.jsonc` は次の値にしている。

```jsonc
{
  "scrollbar": { "show": true, "currentOnly": true },
}
```

- 出すのは、ファイルが画面に収まらない（スクロールできる）ときだけである。出す場所は、編集の画面のバッファの表示領域だけで、ポップアップ（ヘルプ、キーの一覧、ホバーなど）、プレビュー、出力の欄、ファイラとファインダーのポップアップとペインには出さない。nvim-scrollview の `excluded_filetypes` に書くことの多い窓（ファイラ、ヘルプ）は、本体ではどれもこの「出さない場所」に当たる
- つまみと印は、本文の最後の列の上に重ねて描く。本文の幅は変わらない。つまみのセルは下の文字を残して `scrollbar` の色を重ねる。nvim-scrollview の `winblend` の透かしは端末のセルでは出せないので、代わりに下の文字を残す。本体の既定は反転なので、端末の背景の明暗を問わずに読める。主張を抑えたいときは、`colors` の `scrollbar` に背景の色だけを書く（同梱の iceberg は暗い背景向けの背景の色にしてある）。印のセルは下の文字を印の記号に替える
- 位置は、見た目の行でなく本文の行で数える。長い行を折り返していても、1 行は 1 行として数える
- 印は、診断の範囲の始まりの行と、検索の一致のある行に出す。同じセルに重なったら、誤り、警告、検索の一致、情報、ヒントの順に優先して 1 つだけ出す。記号は `symbols` の `scrollbar.` で始まる名前で、色は `colors` の同じ名前で変える（[配色](#配色)、[記号](#記号)）
- 検索の一致の印は、一致に色を付けている間（`search.highlight` が `true` で、`:nohlsearch` で消していない間。[検索の一致の色付け](#検索の一致の色付け)）だけ出す。一致のある行は、画面を描くのと別に集め、集め終えたら印を出す。バッファの全体から集めるのに 2 秒を超えたら、そのパターンでは印を出さない（パターンを替えるか、バッファを編集すると集め直す）
- `scrollbar.currentOnly` を `true` にすると、同じ window に複数の client を開いたとき（[同じ window の複数の client](#同じ-window-の複数の-client)）に、最後にキーを打った client の画面にだけ出す。本体がフォーカスを別のペインへ移したとき（別のペインのタブのファイルを `:e` で開いたとき）は、移した先の client の画面へバーも移る。端末多重化ソフトの操作でペインを移っただけ（キーを打っていない）では移らない
- `scrollbar.excludedFileTypes` に書いたファイルの種類のバッファには出さない
- マウスを使うとき（[マウス](#マウス)）は、nvim-scrollview と同じくつまみと印を操作できる。同じ列でも、つまみも印も描いていないセルは本文で、押すとその行の最後の文字へカーソルが移る（nvim-scrollview と同じ）。ホイールは、本文の上と同じく画面を送る
  - つまみを左のボタンで押して上下にドラッグすると、画面を送る。つまみの中の押した位置を保って動き、つまみを一番上まで動かすとファイルの先頭、一番下まで動かすとファイルの最後の行が画面の一番上に来る。カーソルは、押したときと同じ画面の中の位置に置く（`scroll.margin` の余白の中になるときは、余白の外の行に置く）。ジャンプリストには入らない。押して離しただけと、押したまま横へ動かしただけ（つまみの行が変わらない）では何も動かない。ドラッグの途中でキーを打つと、ドラッグはそこで終わる（キーはそのまま働く）
  - 印を左のボタンで押すと、その印の行へ移る。1 つのセルに同じ種類の印の行が複数あるときは、カーソルより後の行へ順に移り、最後の行の次は最初の行に戻る。移る前の位置はジャンプリストに入るので、`<C-o>` で戻れる。移った先の列は、`G` と同じく行の最初の空白でない文字である
  - つまみの上に重なった印は、押してつまみを動かさずに離すと、その印の行へ移る。押したまま動かすと、つまみを動かす
  - ビジュアルモードでは選択を終えてから動き、インサートモードではインサートモードのまま動く。印の上をすばやく続けて押しても、1 回ずつのクリックとして順に移る
  - 操作は左のクリックとドラッグの操作の ID（`mouse.click`、`mouse.drag`、`mouse.release`）の中にある。ほかのボタンでつまみと印を操作したいときは、`keymap` でそのボタンにこれらの ID を割り当てる（nvim-scrollview の `scrollview_mouse_primary` に当たる）

実行中は `:option.toggle scrollbar.show` で出し入れできる。
出す印は `:option.set scrollbar.marks search` のように変える。

### 検索の一致の色付け

直前の検索のパターンに一致する範囲に色を付けられる（Vim の `'hlsearch'`）。
設定ファイルに次のように書くと、起動したときから付ける。
同梱の `config.example.jsonc` もこの値にしている。本体の既定は Vim と同じく付けない。

```jsonc
{
  "search": { "highlight": true },
}
```

実行中は `:set hlsearch`（付ける）、`:set nohlsearch`（付けない）、`:set hlsearch!`（切り替える）で変える。
`:option.set search.highlight true` も同じである。
変えた値は、次に画面を描くときから効く。

色を付けるパターンは、直前の検索のパターン（`/`、`?`、`*`、`#`、`:s`、`:g` で使ったもの）で、文法は検索と同じ Go の `regexp` である。
色の名前は `search` である（[配色](#配色)）。

- 画面に見えている行のうち、パターンに一致する範囲のすべてに付ける。1 行に複数の一致があれば、どれにも付ける
- パターンは行ごとに当てるので、行をまたいで一致することは無い
- 空の一致（`/^`、`/$` など）は、その位置の 1 文字に付ける。行末の位置なら、行末の 1 列に付ける
- ビジュアルモードの選択と重なったところは、選択の色（`visual`）を上にする
- 付けるのはバッファの表示領域だけで、行番号の列、浮かべた窓（ホバーの説明、ファインダーなど）、出力の欄、拡張機能のペインには付けない

`:search.clearHighlight`（別名 `:nohlsearch`、`:noh`）は、次の検索まで一致の色を消す。
消しても設定の値は変わらない（`:set hlsearch?` は `search.highlight=true` を出す）。
消した色は、次のどれかで戻る（Vim の `:nohlsearch` と同じ）。

- 検索のパターンを使う操作（`/`、`?`、`n`、`N`、`*`、`#`、`:s`、`:g`、`:/foo/` のようなパターンの範囲）。パターンが見つからなかったときも戻る
- `:set hlsearch` などで `search.highlight` を設定し直したとき（同じ `true` を設定し直しても戻る）

カーソルを動かしただけと、`search.highlight` でない設定（`:set number` など）を変えただけでは戻らない。
正しくないパターン（`/(`）を打ったときは、パターンを記録しないので、色も戻らない。
`<C-l>` で色を消すには、設定ファイルの `keymap` に `"normal": { "<C-l>": { "command": "search.clearHighlight" } }` と書く（[keymap から Ex コマンドを呼ぶ](#keymap-から-ex-コマンドを呼ぶ)）。

`*` と `#` は、カーソルの単語を、その単語の全体に一致するパターン（`foo` なら `\bfoo\b`）にして、直前の検索にする。
向きは、`*` が前向き、`#` が後ろ向きである。
カーソルは動かさず、ジャンプリストにも記録しない（Vim の `*` と `#` は次の一致へ移る。[vim-differences.md](vim-differences.md)）。
`search.highlight` が `true` なら、その単語の一致にすぐ色が付き、`:nohlsearch` で消していた色も戻る。
次の一致へは `n`、前の一致へは `N` で移る（`#` の後の `n` は後ろへ移る）。
カーソルが単語の途中にあると、後ろ向きの最初の一致はカーソルのある単語そのものなので、`#` の後の `n` と `*` の後の `N` は、その単語の先頭へ移る。
例えば `foo bar foo baz` の 3 文字目（`fo` の後の `o`）で `#` を打ってから `n` を打つと、1 文字目へ移る。
Vim で同じ位置から `*N` を打ったときと同じ位置である（Vim の `#` は単語の先頭から探すので、`#` だけで 9 文字目の `foo` へ移る。2026-11-20 に Vim 9.1 で確かめた）。
回数は使わない（`2*` も `*` と同じ）。
`*` と `#` はモーションではないので、オペレーターの後には使えない（`d*` は何もしない）。
ビジュアルモードでは、選択を変えずにパターンだけを決める。

### 打つたびの検索

`/` と `?` でパターンを打っている間は、打つたびに、カーソルから次の一致を探して、画面をその一致へ動かし、一致に色を付ける（Vim の `'incsearch'`）。
設定で切り替えることはできず、常に行う（Vim 9.1 の本体の既定はオフ。[vim-differences.md](vim-differences.md)）。
見せ方は Neovim に合わせている。

- 今の一致には `incsearch` の色を付ける（[配色](#配色)）。ステータス行のルーラーも一致の位置を出す
- `search.highlight` が `true` なら、ほかの一致にも `search` の色を付ける。`:nohlsearch` で消した後でも、打っている間は付ける。一致が無い間は、直前の検索の一致の色も消える
- 回数を付けたとき（`3/foo`）は、その数だけ先の一致を出す
- 一致が無いときと、打っている途中のパターンが正規表現として正しくないとき（`foo(` まで打ったとき）は、誤りを出さず、一致を出さずに、画面を打つ前のままにする。`<CR>` で決めたときに、今までと同じ誤り（`E486`、`E383`）を出す
- 打っている間の `<C-g>` は次の一致へ、`<C-t>` は前の一致へ、出す一致を動かす（回数を付けたときは、その数だけ動かす）。`/` と `?` のどちらでも、`<C-g>` がファイルの末尾の方である。パターンをまだ打っていない間は何もしない
- `<CR>` で一致へ移って決める（`<C-g>` と `<C-t>` で動かした一致へ移る）。ジャンプリスト、`n` が繰り返す直前の検索、`:nohlsearch` で消した色を戻すことは、`<CR>` で決めたときだけ行う
- `<Esc>` と `<C-c>` で取り消すと、カーソルと画面は打つ前に戻り、ジャンプリストと直前の検索も変わらない
- オペレーターの後の `/`（`d/foo`）でも同じく出す。ビジュアルモードの `/` では、選択を一致の先頭まで広げて出し、一致には `incsearch` の色を付けない（Neovim と同じ）
- マクロと `.` の再生の中では打つたびの検索をせず、`<CR>` を押したときと同じ結果になる

打つたびの検索は、打つ手を止めずに後ろで行う。
大きなファイルで検索に時間が掛かっても、打った文字はすぐコマンドラインの行に出て、前に打った分の結果が後から届いたら捨てる。
1 回の検索の時間の上限は `search.incrementalTimeoutMs`（既定は 500 ミリ秒）で、超えたらその回は一致を出さない。
`<CR>` で決めたときの検索には上限を付けない。
ポップアップの中のバッファで打った `/` は、打つたびの検索をしない。

### 検索の一致の件数

検索で動いたときに、メッセージの行の右に、カーソルの一致が全体の何件目かと一致の件数を出せる（Neovim の既定の表示。Vim の `'shortmess'` に `S` が無いとき）。
設定ファイルに次のように書くと出す。
同梱の `config.example.jsonc` もこの値にしている。本体の既定は Vim と同じく出さない。

```jsonc
{
  "search": { "showCount": true },
}
```

実行中は `:option.set search.showCount true`（出す）、`:option.set search.showCount false`（出さない）、`:option.toggle search.showCount`（切り替える）で変える。
`:set` の名前は無い。
変えた値は、次の検索から効く。

```text
/foo                                                    [2/5]
```

- 出すのは、`/` と `?` の `<CR>`、`n`、`N`、`*`、`#` の後である。オペレーターの後の `/`（`d/foo`）とビジュアルモードの `/` でも出す
- `*` と `#` はカーソルを動かさないので、パターンにした単語の先頭の一致の番号を出す（単語の手前の空白で打ったときも、その単語の番号）
- バッファの端で折り返したときは、「末尾まで検索したので先頭から続けました」の代わりに、件数の前に `W` を付ける（`W [1/5]`）
- 一致が 999 件を超えたら `[2/>999]` と出し、カーソルの一致も 999 件目より先なら `[>999/>999]` と出す
- 数えるのに 40 ミリ秒を超えたら（とても大きなファイルなど）、件数の代わりに `[?/??]` と出す
- 一致が無いとき（`E486`）と、パターンを打っている間は出さない

一致の数え方は、検索の一致の色付けと `n` と同じである。
1 行の中の一致は行の先頭から重ならないように数え、行をまたぐ一致は無い。
件数は `n` で止まる位置の数と同じで、行末の空の一致（`/\b`、`/$` など）が前の一致と同じ位置に止まるときは 1 件と数える。

### ヤンクした範囲の色付け

`y` でヤンクした範囲に、決まった時間だけ色を付けられる（nvim の `vim.hl.on_yank` に当たる。Vim には無い）。
設定ファイルに次のように書くと、ヤンクした範囲に 300 ミリ秒だけ色が付いて消える。
同梱の `config.example.jsonc` もこの値にしている。本体の既定は `0` で付けない。

```jsonc
{
  "yank": { "highlightMs": 300 },
}
```

実行中は `:option.set yank.highlightMs 300` で変え、`0` にすると付けない。
変えた値は、次のヤンクから効く。
範囲（0 から 10000）の外の値は誤りにして、値を変えない（負の値は `E487`、10001 以上は `E474`）。
色の名前は `yank` である（[配色](#配色)）。

- 色を付けるのは `y` のオペレーターでヤンクしたときだけで、`yy`、`Y`、`yiw`、ビジュアルモードの `y` と `Y` を含む。`d`、`c`、`x` で消したテキストには付けない
- マクロの再生と `.` の繰り返しの中のヤンクには付けない（nvim の `vim.hl.on_yank` の既定と同じ）
- ブラックホールのレジスターへのヤンク（`"_yy`）には付けない（nvim と同じ）
- 色は、ヤンクした範囲の文字に付ける。行単位のヤンク（`yy`）は行の文字の全体に付き、空の行と行末より右には付かない（nvim と同じ）
- 時間が過ぎる前でも、そのバッファが変わったら（編集、拡張機能による編集、アンドゥ）消える。続けてヤンクすると、別のタブのバッファでのヤンクでも、前のヤンクの色は消えて新しい範囲に付く（色はエディタの全体で 1 つだけ。nvim と同じ）
- 検索の一致の色（`search`）と重なったところは、ヤンクした範囲の色を上にし、ビジュアルモードの選択の色（`visual`）は、ヤンクした範囲の色の上にする
- 付けるのはバッファの表示領域だけで、浮かべた窓（ホバーの説明、ファインダーなど）の中には付けない

### 字下げとタブの幅

字下げの文字と幅、タブ文字の表示幅、自動インデントは、バッファごとの設定で決める（Vim の `'expandtab'`、`'shiftwidth'`、`'tabstop'`、`'autoindent'`）。
本体の既定は Vim と同じく、タブ文字で字下げし、字下げの幅とタブ文字の幅は 8、自動インデントは行わない。
同梱の `config.example.jsonc` は、次のように 4 列のスペースで字下げし、自動インデントを行う設定にしている。

```jsonc
{
  "indent": { "useSpaces": true, "width": 4, "auto": true },
  "tab": { "width": 4 },
  // ファイルの種類ごとの値。キーは filetype の値で、中は最上位と同じ形で書く
  "fileTypeOptions": {
    "python": { "indent": { "width": 4 } },
  },
}
```

| 名前 | `:set` の名前 | 内容 |
|---|---|---|
| `indent.useSpaces` | `expandtab`、`et` | `true` なら字下げをスペースで、`false` ならタブ文字で作る |
| `indent.width` | `shiftwidth`、`sw` | 字下げの 1 段の幅。`0` なら `tab.width` と同じ幅 |
| `tab.width` | `tabstop`、`ts` | タブ文字の表示幅 |
| `indent.auto` | `autoindent`、`ai` | 自動インデントを行うか |

実行中は `:set et sw=4 ts=4 ai` のように変える（短い名前も書ける）。`:option.set indent.width 2` も同じである。
範囲がバッファごとなので、カーソルのあるバッファにだけ効く。変えた値は、次の入力と次の描画から効く。

#### 値の決まり方

バッファを開くと、次の順に値を当て、後に当てた値が勝つ。

1. 本体の既定（上の Vim と同じ値）
2. 設定ファイルの最上位に書いた値（`"indent": { "width": 4 }`）
3. 本体のファイルの種類ごとの既定。`go` と `make` は、タブ文字で、`tab.width` と同じ幅で字下げする（Vim と Neovim の ftplugin と同じ）
4. 設定ファイルの `fileTypeOptions` に、ファイルの種類ごとに書いた値
5. 開いたファイルに当たる `.editorconfig` の値
6. 拡張機能が入れた値。標準の拡張機能では、整形ツールの拡張機能が `biome.json`、`.prettierrc`、`pyproject.toml` などから読んだ値（下の「整形ツール」の「整形ツールの設定からのインデント」）

3 は 2 より後なので、最上位で `indent.useSpaces` を `true` にしても、Go のファイルと Makefile はタブ文字で字下げする（Neovim で `init.lua` に `expandtab` を書いたときと同じ）。
Go のファイルもスペースにしたいときは、`"fileTypeOptions": { "go": { "indent": { "useSpaces": true, "width": 4 } } }` と書く。
`fileTypeOptions` に書けるのは、範囲がバッファごとの項目（`filetype` を除く）だけで、範囲がエディタ全体の項目を書くと誤りとして知らせる。

`:set sw=2` のように実行中に変えた値は、1 から 6 のどの値よりも勝つ。名前の無いバッファに `:w ファイル` で名前を付けて `.editorconfig` を読み直しても残る。
`:set filetype=go` のようにファイルの種類を変えると（`:w n.go` のように種類の決まる名前を付けたときも）、新しい種類で 1 から 6 を当て直す。
このとき、新しい種類の 3 と 4 が値を持つ項目（go なら `indent.useSpaces` と `indent.width`）だけは `:set` で変えた値よりその値を使い、ほかの項目の `:set` の値は残す（Neovim の ftplugin と同じ）。
`:e` でファイルを開き直したときも、1 から 6 を当て直す（`.editorconfig` も読み直す）。
6 の値は、同じ項目に複数の拡張機能が入れたら、宣言ファイルを先に読んだ拡張機能の値を使う。値を入れた拡張機能が異常で終わるか止まると、その値は消える。
ファイルの種類が変わった後に拡張機能が新しい種類で送った 6 の値は、3 と 4 と同じく、`:set` で変えた値よりその値を使う（変わった後に `:set` で変えた項目を除く）。

#### `.editorconfig`

開いたファイルのディレクトリから親のディレクトリへ `.editorconfig` を探して読む（[EditorConfig](https://editorconfig.org/) の仕様と同じ探し方。`root = true` のファイルで止まる）。
読む項目は次の 3 つで、Neovim と同じく写す。

| `.editorconfig` の項目 | 写し方 |
|---|---|
| `indent_style` | `space` なら `indent.useSpaces` を `true`、`tab` なら `false` にする。`tab` で `indent_size` が無ければ、`indent.width` を `0`（`tab.width` と同じ幅）にする |
| `indent_size` | `indent.width` にする。`tab_width` が無ければ `tab.width` も同じ値にする。`tab` なら `indent.width` を `0` にする |
| `tab_width` | `tab.width` にする |

`end_of_line`、`charset`、`trim_trailing_whitespace`、`insert_final_newline`、`max_line_length` などのほかの項目は読まない。
値が `unset` の項目は、書かなかったものとして扱う。正しくない値（`indent_size = x`、`tab_width = 0`）の項目は使わない（ログに残す）。
`.editorconfig` を読むのは、バッファを開いたとき、`:e` で開き直したとき、名前の無いバッファに `:w ファイル` で名前を付けたときである。
開いた後に `.editorconfig` を書き換えたら、`:e` で開き直すと効く。
読まないようにするには、設定ファイルに `"editorConfig": { "enabled": false }` と書く（Neovim の `vim.g.editorconfig = false`）。

#### 入力と字下げ

`<Tab>` と `<BS>` は、Neovim の既定（`smarttab` がオン）で、`indent.useSpaces` に応じて次の組にしたときと同じに動く。
Vim の `softtabstop` と `smarttab` は別の設定にしていない（`:set sts=4` は `E518` になる。[vim-differences.md](vim-differences.md)）。

| `indent.useSpaces` | Neovim の組 | `<Tab>` | `<BS>` |
|---|---|---|---|
| `true` | `expandtab shiftwidth=N softtabstop=N` | 次の N の倍数の列までスペースを入れる | 前が空白なら、N の倍数の列まで空白を消す。ただし行頭の字下げの外では、そのインサートでスペースを打った後のスペースを 1 つずつ消す |
| `false` | `noexpandtab shiftwidth=N softtabstop=0` | タブ文字を入れる。ただし行頭の字下げの中で、N と `tab.width` が違うときは、N の倍数の列まで字下げし、タブ文字を使えるだけ使う | 行頭の字下げの中なら、N の倍数の列まで空白を消す。それ以外は 1 文字消す |

N は `indent.width`（`0` なら `tab.width`）である。

ノーマルモードの `>` と `<`、ビジュアルモードの `>` と `<` は、字下げを N の幅だけ増やすか減らし、`indent.useSpaces` に従って字下げを作り直す。

`indent.auto` が `true` なら、次のように前の行の字下げを写す。

- インサートモードの `<CR>` は、カーソルより前の字下げを新しい行に写し、カーソルより後ろの先頭の空白を消す
- `o` と `O` は、カーソルの行の字下げを開いた行に入れる
- `cc` と `S`（行単位の `c`）は、行の字下げを残す
- 写した字下げのまま、`<BS>` で減らすほかに何も打たずに `<Esc>` で抜けるか、`<CR>` を打つか、カーソルキーで別の行へ移ると、残った字下げは消える（空の行が残る）。ただし `<BS>` の後にタブ文字 1 つだけが残ったときは消えない（Vim と Neovim と同じ）

端末から貼り付けたテキストのタブ文字と改行には、字下げの設定と自動インデントを当てない。

#### タブ文字の表示

タブ文字は、そのバッファの `tab.width` の幅で描く。
カーソルの列、`j` と `k` の列、長い行を折り返したときの見た目の行の分け方、`gj` と `gk` も、同じ幅で数える。
ポップアップとプレビューの中身は、そのバッファの値で描く。

### アンドゥの履歴とカーソルの位置を残す

設定ファイルで `undo.persist`（Vim の `'undofile'`）と `cursor.restore`（Vim の `defaults.vim` の「最後の位置へ戻る」）を `true` にすると、エディタを終えた後も、ファイルのアンドゥの履歴とカーソルの位置が残る。
点で区切った名前は、入れ子のキーにして書く（[設定の名前の一覧](#設定の名前の一覧)）。
同梱の `config.example.jsonc` も両方を `true` にしている。
本体の既定は、Vim と同じくどちらも残さない。

置き場は `$XDG_STATE_HOME/vi-imitation/` で、`XDG_STATE_HOME` が空なら `~/.local/state/vi-imitation/` である（Neovim が undo と shada を置く場所と同じ規則）。
ディレクトリは 0700、ファイルは 0600 で作る。
設定が `false` の間は、置き場に何も作らない。

| 置き場の中 | 中身 |
|---|---|
| `undo/%home%u%a.txt` | `/home/u/a.txt` のアンドゥの履歴。名前は、ファイルの絶対パスの `/` を `%` に替えたもの（Vim の `undodir` と同じ）。シンボリックリンクで開いたファイルは、指す先のパスで決める |
| `state.json` | ファイルごとの、最後に閉じたときのカーソルの位置（最大 100 件。古いものから捨てる）。版の違う本体（build し直す前から動いている tmux の window の server など）が交互に書いても、互いの分を消さない |

アンドゥの履歴は、ファイルへ保存（`:w`、`:wq`、`:wa`）したときに書き、ファイルを開いたときに読み戻す。
`u` の後に保存したときは、開き直した後に `<C-r>`（`U`）でやり直せる。
保存した後にファイルが外で変わっていたら、履歴を読まずに捨てる（書いたときの中身のハッシュと比べる）。
本体の版が変わって履歴の形式が変わったときも、読まずに捨てる。
変更を取り消しきった後など、履歴の無いバッファを保存すると、古い履歴のファイルを消す（Vim と同じ）。
`:w 別の名前` と、名前の無いバッファと、`:help` のようなファイルでないバッファの履歴は残さない。
実行中は `:set undofile`、`:set noundofile`（`udf` も可）か `:option.set undo.persist true` で切り替える。

カーソルの位置は、ファイルのタブを閉じたとき（`:q`、`:bd`、`:qa`）に残し、次にそのファイルを開いたときにその位置へ置く。
タブを閉じずに終わったとき（端末を閉じた、tmux の window を閉じた、tmux の server を終えた）も、開いていたファイルの位置を残すので、`:q` で閉じずに window を閉じて終えてよい。
動きは Vim の `defaults.vim` と同じで、次のときは位置へ置かない。

- 残した行が、今のファイルの行数を超えるとき（1 行目のまま）。列が行の長さを超えるときは、行の最後の文字へ寄せる
- `git commit` のメッセージ（`COMMIT_EDITMSG` など、ファイルの種類が `gitcommit` のもの）
- 定義へ移動のように、位置を指定して開いたとき（指定した位置へ置く）

tmux の window ごとの server が同時に閉じても、互いに残した位置は消えない。

置き場に書けないとき（ディスクがいっぱい、権限が無い）も、保存と閉じることは止めない。
保存のときは、メッセージの行に `"a.txt" を書き込みました（アンドゥの履歴を残せません: ...）` と出す。

## 拡張機能のコマンドを呼ぶ

拡張機能のコマンドは、コマンドラインで `:` の後にコマンドの ID を打って呼ぶ（`:acp.prompt`、`:mcp.status`）。
ID の後に空白を挟んで書いた文字列は、引数としてコマンドに渡る（`:acp.prompt この関数を説明して`）。
キーに割り当てるときは、[設定ファイル](#設定ファイル)の例のとおり `keymap` に書く。

LSP と DAP の操作（定義へ移動、ホバー、ブレークポイントなど）は、拡張機能のコマンドではなく本体の操作である（#203）。
拡張機能は宣言ファイルの `provides` で操作を引き受け、本体は、カーソルのあるバッファのファイルの種類で引き受ける拡張機能を選んで問い合わせる。
操作は既定のキー（次の節）で呼ぶほか、コマンドラインから操作の名前で呼べる（`:hover`、`:rename 新しい名前`、`:breakpoint.conditional i == 3`）。
コマンドラインから呼べるのは、どれかの拡張機能が `provides` で引き受けた操作だけである。
引き受ける拡張機能が無いバッファで呼ぶと、「hover の提供者がありません（ファイルの種類 text）」のように出る。

### LSP と DAP の既定のキー

`<leader>` は設定の `leader`（既定は `<Space>`）である。
どのキーも、設定の `keymap` で別のキーの列に変えられる。

| キー | 操作の名前 | 操作 |
|---|---|---|
| `<leader>lh`、`K` | `hover` | カーソルの位置の識別子の説明をポップアップで出す |
| `<leader>ld` / `<leader>lD`、`<C-]>` | `definition` | 定義をポップアップで見る / 定義へ移る |
| `<leader>li` / `<leader>lI` | `implementation` | 実装をポップアップで見る / 実装へ移る |
| `<leader>lt` / `<leader>lT` | `typeDefinition` | 型の定義をポップアップで見る / 型の定義へ移る |
| `<leader>lr` | `references` | 参照の一覧から選んで移る |
| `<leader>ln` | `rename` | 名前の変更。新しい名前は `:rename 新しい名前` で渡す。キーで呼ぶと渡し方を知らせる |
| `<leader>la` | `codeAction` | コードアクション（標準の LSP の拡張機能はまだ引き受けない） |
| `<leader>ls`、`<leader>lS` | `documentSymbols`、`workspaceSymbols` | シンボルの一覧（ファイル、ワークスペース）から選んで移る。ワークスペースはカーソルの位置の単語か `:workspaceSymbols 名前` の名前で探す |
| `<leader>le`、`<leader>lE` | （本体） | 診断の一覧（ファイル、開いているすべてのファイル）をポップアップで出す |
| `<leader>lj`、`<leader>lk` | （本体） | 次の診断、前の診断へ移る。端で折り返す |
| `gq{モーション}` | `format` | 範囲を整形する。言語サーバーが範囲の整形に対応しなければ、全体を整形する。整形ツールを設定したファイルの種類では、整形ツールで全体を整形し、範囲と重なるところだけを書き換える（[整形ツール](#整形ツール)） |
| `<leader>dc` | `debug.continue` | 続行。デバッグが始まっていなければ始める |
| `<leader>dp`、`<leader>dt`、`<leader>dr` | `debug.pause`、`debug.terminate`、`debug.restart` | 一時停止、終了、終了してから前回の設定で始め直す |
| `<leader>db` / `<leader>dB` | `breakpoint.toggle` / `breakpoint.conditional` | ブレークポイントの切り替え / 条件付きのブレークポイント。条件は `:breakpoint.conditional 式` で渡す |
| `<leader>dX` | `breakpoint.clear` | ブレークポイントをすべて消す |
| `<leader>dn`、`<leader>di`、`<leader>do` | `debug.stepOver`、`debug.stepIn`、`debug.stepOut` | ステップオーバー、ステップイン、ステップアウト |
| `<leader>dl` | `debug.runLast` | 前回の設定で始める |
| `<leader>du` | `debug.toggleUI` | DAP のペインの開け閉め（標準の DAP の拡張機能はまだ引き受けない） |
| `<leader>l?`、`<leader>d?` | （本体） | LSP、DAP のキーの一覧を出す（[ヘルプとキーの一覧](#ヘルプとキーの一覧)） |

### 段階 2 のコマンドからの移行

段階 2 の LSP と DAP の拡張機能のコマンドは、#203 で本体の操作に置き換えたので、もう無い。
古いコマンドをコマンドラインで打つと、`E492: エディタのコマンドではありません: lsp.definition` のように出る。
設定の `keymap` の右辺に古いコマンドを書いていれば、起動したときに設定の誤りとして知らせる。
そのまま使うと、左辺のキーが既定のキーを隠す。
たとえば `"<Space>d": ":lsp.definition<CR>"` があると、`<Space>db` の `<Space>d` が先に展開されて E492 になり、残りの `b` は単語の後退として動く。
その項目は消すか、右辺を新しい名前に書き換える。

| 段階 2 のコマンド | 操作の名前（`:` で呼ぶ） | 既定のキー |
|---|---|---|
| `lsp.definition` | `definition` | `<C-]>`、`<leader>lD` |
| `lsp.hover` | `hover` | `K`、`<leader>lh` |
| `lsp.format` | `format` | `gq{モーション}` |
| `dap.start` | `debug.continue`（始まっていなければ始める） | `<leader>dc` |
| `dap.stop` | `debug.terminate` | `<leader>dt` |
| `dap.toggleBreakpoint` | `breakpoint.toggle` | `<leader>db` |
| `dap.continue` | `debug.continue` | `<leader>dc` |
| `dap.next` | `debug.stepOver` | `<leader>dn` |
| `dap.stepIn` | `debug.stepIn` | `<leader>di` |
| `dap.stepOut` | `debug.stepOut` | `<leader>do` |

### 操作の補足

「ポップアップで見る」は、移らずに、移動先の行の 3 行前から 20 行の写しをカーソルの近くに出す（[ポップアップ](#ポップアップ)）。
写しは編集できない。`<C-w>w` でフォーカスを入れて `:tabopen` を打つと、本物のファイルを開いて移動先へ移る。
ドットリピート（`.`）は、`gq` を除く LSP と DAP の操作を記録しない。`x` の後に `K` を打ってから `.` を打つと、`x` を繰り返す。
一覧（参照、シンボル）は、選択肢の問いで番号を打って選ぶ。番号を振れるのは 9 件までである。
補完は、まだ無い。

## 出力の欄

デバッグ対象の出力やエージェントの返答のように、拡張機能が後から書き足すテキストは、画面の下に開く出力の欄に出る。
欄は拡張機能ごとに名前を持つ（DAP は `dap`、ACP は `acp`）。
欄を開いていなければ、書き足されても画面には出ない。

| コマンド、キー | 動作 |
|---|---|
| `:output [名前]`（`:out`） | 欄を開く。名前を省くと、最後に書き足された欄を出す |
| `:outclose`（`:outc`） | 欄を閉じる |
| `<C-w>P` | 閉じていれば欄を開き、開いていれば次の欄に切り替える |
| `<C-w>z` | 欄を閉じる |
| `<C-w><PageUp>`、`<C-w><PageDown>` | 欄を上と下へスクロールする |

欄の下の行に、`[出力] dap` のように欄の名前が出る。
`:output` の後には、`|` で拡張機能のコマンドを続けられる（`:output|acp.prompt ...`）。
欄の動作の詳細は基本設計 6.7 の「表示のデータ」にある。

## 拡張機能の進み具合

言語サーバーの読み込み、tree-sitter のパーサの用意、mise での言語サーバーの入れ込みのような、時間のかかる処理の進み具合は、編集の画面の右下に重ねて出る（オーナーの nvim の fidget.nvim の形）。
次の例は、このリポジトリの Go のファイルを開いたときの、画面の最後の行の右の端である（2026-11-21 に gopls v0.21.0 で、tmux の中で取った）。
読み込んでいる間は 1 行目の形で、読み込み終えると 2 行目の形になり、約 3 秒の後に消えた。

```text
        Loading packages... gopls: Setting up workspace ...
 Finished loading packages. gopls: Setting up workspace done
```

- 1 つの処理を 1 行にし、「本文 (百分率%) 表題 印」の形で右に寄せる。いくつも続いていれば、新しいものを下にして積む
- 続いている間は表題と印を `progress.title` の色で描き、印は `progress.running`（既定は `...`）である。終わったら `progress.done` の色と印（既定は `done`）に替わり、`progress.doneMs`（既定は 3000）ミリ秒の後に消える。失敗して終わったもの（パーサを用意できなかった、mise で入れられなかった、言語サーバーが終了した）は、`progress.failed` の色と印（既定は `failed`）になる
- 端末多重化ソフトの同じ window に複数の編集の client があるときは、その拡張機能を最後に使った client（使っていなければ、最後にキーを打った client）の画面にだけ出る
- 保存の前の整形の間のように、本体が拡張機能の応答を待っている間も出る
- 出すのは、バッファの表示領域の半分の行までである。それより多ければ古いものを出さない。幅に収まらなければ本文から削り、次に表題を削る。印は残す
- ポップアップ（ホバー、ファインダーなど）と、複数の行に広がったメッセージは、進み具合の上に出る。端末多重化ソフトのペインとポップアップの中の画面（ファイラ、ファインダー）には出ない
- 処理を知らせた拡張機能が異常で終わるか止まったら、その拡張機能の進み具合は消える。終わりを知らせない処理の進み具合は、拡張機能が止まるまで残る。消す操作は無いので、`:option.toggle progress.show` で表示ごと切る
- 進み具合は `:messages` には残らない。失敗の理由は、今までどおりメッセージの行に出る

出さないときは、設定ファイルに `"progress": { "show": false }` と書くか、実行中に `:option.set progress.show false` と打つ。
色の名前は[配色](#配色)、印の名前は[記号](#記号)にある。
拡張機能から知らせる口は、`vi-imitation --docs extensions` の `progress/report` にある。

## 窓の上端の帯

拡張機能は、編集の画面のバッファの表示領域の上の端に、何行かを重ねて描ける（オーナーの nvim の nvim-treesitter-context の窓の形）。
関数とクラスの見出しを窓の上端に固定する拡張機能が使う。本体は帯の中身を決めず、拡張機能が送った行を描く。

- 帯の行の左の、印の列と行番号の列には、行番号の列を出していれば、その帯の行に当たるファイルの行の番号が出る
- 帯はカーソルの行を隠さない。カーソルが表示領域の上の方にあると、帯の上の行から削って縮む。カーソルが最初の行にあれば出ない
- 帯の下に隠れた本文の行は、スクロールの余白（`scroll.margin`）を足さずに隠れたままにする
- ノーマルモードで帯の行を左のボタンで押すと、その帯の行に当たるファイルの行の先頭へ移る。移る前の位置はジャンプリストに残るので、`<C-o>` で戻れる。帯を作った後にファイルを書き換えた（拡張機能が帯を作り直す前の）帯の行と、行に当たらない帯の行を押したときと、インサートモードとビジュアルモードで押したときは、帯の下に隠れた本文を押したことになる
- 右端のスクロールバー、拡張機能の進み具合、ポップアップは、帯の上に出る。ポップアップやペインの中の画面には帯は出ない
- 帯を出した拡張機能が異常で終わるか止まったら、帯は消える

色の名前は[配色](#配色)の `band`、`band.linenr`、`band.bottom` にある。
拡張機能から使う口は、`vi-imitation --docs extensions` の `window/change`（見えている行の範囲とカーソルの変化の知らせ。宣言ファイルの `windowEvents` で受け取る）と `band/set` にある。
スクリプトの拡張機能からは、`server.Server` の `OnWindowChange` と `SetBand` で使える（[スクリプトの拡張機能](#スクリプトの拡張機能)）。

## タブとジャンプ

開いたファイルは、それぞれ 1 つのタブになる。
`:e <パス>` や定義へ移動で開いたファイルは、新しいタブとしていまのタブの右に開き、元のタブも残る。
画面の一番上の行にタブの行が出る（設定の `tabline.show` で、タブが 2 つ以上のときだけにもできる。未保存のタブには `[+]`、編集を禁止したタブには `[RO]` が付く）。
タブを閉じると、そのファイルのバッファも閉じる（隠れたバッファは無い）。

| コマンド、キー | 動作 |
|---|---|
| `<Tab>`、`<S-Tab>` | 次と前のタブへ移る。端の次は反対の端へ回る。`2<Tab>` のように回数を書ける |
| `:bn`、`:bp`（`:bN`） | `<Tab>` と `<S-Tab>` と同じ。`:bn 2` のように回数を書ける |
| `:b <番号>`、`:b <名前>` | そのバッファのタブへ移る。名前は一部でよく、1 つに決まらなければ E93 になる。名前の空白は `\ ` とも書け、末尾の空白は `\ ` で書く（`:b sp\ ` は `sp ` を含む名前。Vim と同じ）。`<Tab>` で補った名前はこの形で入る |
| `<C-^>` | 直前のタブへ移る。`2<C-^>` は 2 番のバッファのタブへ移る |
| `:tabmove`（`:tabm`） | いまのタブを動かす。引数なしか `$` で末尾へ、`0` で先頭へ、`N` で N 番目のタブの後ろへ、`+N` と `-N` で右と左へ動かす（Vim と同じ） |
| `:tab.new` | 名前の無い空のバッファを、いまのタブの右の新しいタブで開く。`:w ファイル` で名前を付けて書ける。同梱の設定の例では `:tabnew` と `:tabe` でも打てる |
| `:ls`（`:buffers`） | 開いているバッファの一覧を出す。`%` はいまのタブ、`#` は直前のタブ（`<C-^>` の移り先）、`a` はどれかの client で表示しているタブ、`h` はどの client でも表示していないタブ（タブの行には残っている。隠れたバッファではない）かどのタブでもないバッファ、`+` は未保存の変更があるバッファである。別の client のタブには `[client N]` が、どのタブでもないバッファには `[タブなし]` が付く |
| `<C-o>`、`<C-i>` | ジャンプの前の位置へ戻り、戻った位置から進む。別のタブへもまたがる。`<Tab>` はタブの移動に使うので、ジャンプを進めるには `<C-i>` を区別して送る端末を使うか、`keymap` で別のキーを `<C-i>` に割り当てる |
| `:jumps`（`:ju`） | ジャンプリストを出す |
| `:q`、`:bd` | いまのタブを閉じる。`:bd <番号>`、`:bd <名前>` は指定したバッファのタブを閉じる。未保存の変更があれば E37（`:q`）か E89（`:bd`）を出し、`!` を付けると変更を捨てて閉じる |
| `:wa` | 未保存の変更のあるバッファを、別の client のタブも含めてすべて書き出す |
| `:qa`、`:qa!` | この client のタブをすべて閉じて終わる。`:qa!` は未保存の変更を捨てる |
| `:wqa`（`:xa`） | この client のタブのうち未保存の変更のあるものを書き出してから終わる |

最後のタブを閉じると、その client（ペインの `vi-imitation`）が終わる。
タブを移るときは、未保存の変更があっても E37 を出さない（離れたタブもタブの行に残るため）。
最後のタブを閉じると server も終わるとき（同じ window にほかの client が無いとき）は、未保存の変更のある別のバッファがあれば、`:q` と `:qa` は E162 でそのバッファの名前を出し、そのタブへ移る。
レジスターとその履歴、ドットリピート（`.`）、`;`、`@@`、マクロの記録は、タブを切り替えても引き継ぐ（`qa:bn<CR>...q` で、複数のファイルにまたがる編集を記録できる）。

### 同じ window の複数の client

tmux の同じ window（herdr の同じタブ）の別のペインで `vi-imitation` を起動すると、同じ server の 2 つ目の client になる。
client ごとに自分のタブの行を持ち、1 つのファイルはどれか 1 つの client のタブになる。

- 別の client のタブになっているファイルを `:e` や `:b` で開こうとすると、開かずに、その client のペインへフォーカスを移し、そのタブを表示する。その client が入力の途中（インサートモードなど）なら、タブは替えずにフォーカスだけを移す
- 拡張機能の結果（定義へ移動で開くファイル、メッセージ、選択肢の問い）は、その操作を打った client に出る
- レジスターとその履歴、直前の検索、マクロ、ドットリピートは client の間で共有する。あるペインでヤンクしたテキストを、別のペインで `p` で貼れる
- マクロの記録は、記録を始めた client のキーだけを記録する。記録中の表示（`recording @q`）も、その client のステータス行にだけ出る
- ジャンプリストは client ごとに持つ
- 確認の問い（`:3,1d` の範囲の入れ替えの問い、外で変わったファイルへの `:w` の問いなど）と、拡張機能の選択肢の問いは、問いを出した client にだけ出る。答えるまで、ほかの client のキーは処理しない（ほかの client には、待っていることが出る）。問いを出した client が終わったら、問いは取り下げる。
- 外部のコマンドは同時に 1 つだけ動かせる。ほかの client の実行中に `:!`、`:{範囲}!`、`:r !` を打つか、権限を上げた保存をすると E319 になる
- `:!`、`:{範囲}!`、`:r !` の外部のコマンドの実行中は、そのコマンドを打った client のキーだけを待たせる。`:{範囲}!` と `:r !` を `<C-c>` で中断できるのも、その client だけである（`:!` の `<C-c>` は端末からコマンドへ届く）
- ペインを閉じるなどで client が異常に終わったときは、その client のタブのうち未保存の変更のあるバッファを、どのタブでもないバッファとして残す。ほかの client で `:ls` で確かめ、`:b` でタブにできる

ジャンプとして記録するのは、`G`、`gg`、検索（`/`、`?`、`n`、`N`）、`%`、`{`、`}`、`:s`、`:g`、`:d`、バッファの切り替え、定義へ移動などの拡張機能による移動である。
`*` と `#` はカーソルを動かさないので記録しない（[検索の一致の色付け](#検索の一致の色付け)）。
`j` や `w` のような短い移動は記録しない。

### 2 つのファイルを見比べる

2 つの client のタブを並べて見比べる（Vim の `vim -d` に当たる）。
見比べのための専用のタブは作らず、2 つのペインの client のタブに、それぞれ差分の色と詰め物（もう片側にだけある行の位置の `-` の行）を出す。

| コマンド、キー | 動作 |
|---|---|
| `:diffsplit <パス>`（`:diffs`） | いまのペインの右に新しいペインを開き、そこで起動した client にファイルのタブを開いて、いまのタブと見比べる。ファイルが既に別の client のタブなら、ペインを開かずにそのタブと見比べる |
| `:diffthis`（`:difft`） | 2 つの client のタブで打つと、その 2 つを見比べる |
| `:diffoff`（`:diffo`） | いまのタブの見比べを終える（相手のタブも終わる） |
| `:diffupdate`（`:diffu`） | 差分を計算し直す（ふだんは編集のたびに計算し直す） |
| `]c`、`[c` | 次と前の差分の始まりへ移る。`3]c` のように回数を書ける |
| `do` | カーソルの差分を、相手の内容で置き換える（`u` で戻せる） |
| `dp` | 相手の差分を、カーソルの差分の内容で置き換える。相手のタブが編集を禁止していれば E21 になる |

- 片方をスクロールすると、もう片方も同じ位置へそろい、カーソルも対応する行へ移る（Vim の `scrollbind` と `cursorbind`）。もう片方の client が入力の途中（インサートモードなど）なら、そろえるのは入力を終えてからになる
- 端末多重化ソフト（tmux、herdr）の外では、client が 1 つなので見比べられない（`:diffsplit` と `:diffthis` は誤りになる）
- `do` と `dp` は、Vim と同じく `d` の後の `o` と `p` として読むので、`d` の後に時間を置いてもよい。見比べていないタブでは E99 になる
- 1 つのタブは 1 つの組でだけ見比べる。見比べているタブを別のタブと見比べ始めると、前の組は終わり、前の相手の client にそのことが出る
- 差分の位置が Vim と違うことがある（基本設計 6.8）

## 囲みと行のずらし

囲みの操作は、vim-sandwich と同じキーで本体が持つ（要件定義 FR-73〜FR-75、基本設計 4.8）。
どれもノーマルモードのキーで、ドットリピート（`.`）とマクロで繰り返せ、1 回の `u` で取り消せる。

| キー | 動作 | 例（`\|` はカーソル） |
|---|---|---|
| `sa{モーションかテキストオブジェクト}{文字}` | 範囲の前と後ろに囲みを足す | `foo b\|ar` で `saiw)` → `foo (bar)` |
| `sd{文字}` | カーソルを含む最も内側のその文字の囲みを消す。`2sd(` は内側から 2 番目 | `(fo\|o)` で `sd)` → `foo` |
| `sr{前の文字}{後の文字}` | カーソルを含む最も内側の前の文字の囲みを、後の文字の囲みに替える | `"fo\|o"` で `sr"'` → `'foo'` |

- `(` と `)`、`[` と `]`、`{` と `}`、`<` と `>` は、開きと閉じのどちらの文字でも同じ組を指し、空白は足さない
- それ以外の文字は、同じ文字で前と後ろを囲む。引用符（`"`、`'`、`` ` ``）の囲みは 1 行の中だけで探し、`\"` のように逃がした引用符を数えない
- `sd<Space>` と `sr<Space>…` は、空白とタブの続きを 1 つの囲みとして扱う
- `saj(` や `saip(` のように行単位の対象では、開きと閉じをそれぞれ独立した行として足す。引用符（`saj"`）は行にせず、最初の行の字下げの後と最後の行の末尾に足す
- `sa` の前の回数は、他のオペレーターと同じく対象の回数に掛ける（`2saiw(` は `sa2iw(` と同じ）

ノーマルモードの `s`（1 文字を消して入力する）は、既定では何にも割り当てない（要件定義 1.1）。
使いたいときは、設定のキーマップで `s` を `cl` に割り当てる。
キーマップの展開は次のキーを待たないので、`s` を割り当てると `s` を打った時点で `cl` に展開され、`sa`、`sd`、`sr` は打てなくなる。
ビジュアルモードの `s`（選択を消して入力する）は残る。

ノーマルモードの `>` と `<` は、次のキーを待たずに、Vim の `>>` と `<<` と同じく行をずらす（FR-76）。
`3>` は 3 行をずらし、`>>` と打つと 2 回ずれる。
`>j` のようにモーションを続けるオペレーターは無いので、範囲をずらすときはビジュアルモードで選んでから `>` を打つ。
ビジュアルモードの `>` と `<` は、ずらした後もビジュアルモードと選択の範囲を保つ（FR-77。Vim で `>gv` と打ったときと同じ）。
続けて `>` を打てばさらにずれ、`<Esc>` で抜ける。

## コメントの切り替え

行のコメントは、Neovim の組み込みの `gc` と同じキーで切り替える（要件定義 FR-169、基本設計 3.3）。

| キー | 操作の ID | 動作 |
|---|---|---|
| `gc{モーションかテキストオブジェクト}` | `comment` | 範囲の行のコメントを切り替える（`gcip` は段落、`gcj` は 2 行） |
| `gcc`、ノーマルモードの `<C-_>`、`<C-/>` | `comment-line` | カーソルの行のコメントを切り替える。`3gcc` は 3 行 |
| ビジュアルモードの `gc`、`<C-_>`、`<C-/>` | `comment` | 選んだ行のコメントを切り替える |
| オペレーターの後の `gc` | `commented-lines` | カーソルの行を含む、続くコメントの行（`dgc` で消す、`ygc` でヤンクする、`gcgc` でまとめて外す） |

- 範囲の行が、空白だけの行を除いてすべてコメントなら外し、そうでなければすべての行をコメントにする
- コメントの記号は、範囲の行の最も浅い字下げの後に置く。空白だけの行は、字下げと記号だけの行（`//`）にする
- 字下げの深さは、表示の幅でなく先頭の空白のバイト数で比べ、最も浅い行の字下げ（タブ文字かスペース）をほかの行にも写す。このため、タブ文字の行とスペースの行が混ざった範囲では、ほかの行の字下げの文字が変わることがある（`  a` と `\tb` の 2 行は `\t//  a` と `\t// b` になる。Neovim と同じ）
- 外すときは、`// %s` の `// ` が無くても `//` で外す（`//foo` は `foo` になる）
- アンドゥは 1 回で戻り、ドットリピート（`.`）とマクロで繰り返せる。`gcc` の後の `2.` は 2 行に働く

端末は Ctrl-/ を多くの場合 0x1f で送り、本体はこれを `<C-_>` と読む。
Ctrl-/ を区別して送る端末（CSI u）では `<C-/>` として届くので、どちらにも割り当ててある。

コメントの形は、バッファごとの設定 `comment.string`（[設定の名前の一覧](#設定の名前の一覧)）で決める。
本体のファイルの種類ごとの既定は次のとおりで、Neovim 0.12.5 の ftplugin の値と同じである。

| ファイルの種類 | `comment.string` |
|---|---|
| `go`、`gomod`、`jsonc`、`c`、`javascript`、`typescript`、`rust` | `// %s` |
| `python`、`sh`、`yaml`、`toml`、`make` | `# %s` |
| `markdown` | `<!-- %s -->` |
| `lua`（設定の `fileTypes` で `.lua` を足したとき） | `-- %s` |
| `json` | 空 |

ほかの種類では、既定は空である。
`comment.string` が空のバッファで `gc` を打つと、`comment.string が空です` と知らせ、空白だけの行を空の行にするほかは何も変えない（Neovim と同じ）。
空のときのオペレーターの後の `gc` は、どの行にも当たらない（Neovim はバッファの全体を選ぶ。[vim-differences.md](vim-differences.md)）。
値は、ほかのバッファごとの設定と同じ順（[値の決まり方](#値の決まり方)）で決まる。
ファイルの種類ごとに変えるときは `fileTypeOptions` に書く。

```jsonc
{
  "fileTypes": { ".lua": "lua", ".html": "html" },
  "fileTypeOptions": {
    "html": { "comment": { "string": "<!-- %s -->" } },
    "c": { "comment": { "string": "/* %s */" } },
  },
}
```

実行中は `:set cms=#\ %s` か `:option.set comment.string # %s` で変える。
Markdown のコードの囲みの中のように、1 つのファイルに複数の言語があっても、バッファの `comment.string` を使う（Neovim はカーソルの位置の言語の記号を tree-sitter で決めるが、本体にはまだ無い）。

## 選んだ行にまとめて入力する

ビジュアルモード（`v` と `V` のどちらでも）の `I` と `A` は、選んだ行のすべてに同時に入力する（要件定義 FR-141、基本設計 3.1）。
`I` は各行の最初の空白でない文字の前（空の行と空白だけの行では行末）から、`A` は各行の行末から入力する。
どちらも、その行でノーマルモードの `I` と `A` を打ったときと同じ位置である。

| キー（`\|` はカーソル） | 結果 |
|---|---|
| `\|foo` と `  bar` と `baz` の 3 行で `VjjI// <Esc>` | `// foo`、`  // bar`、`// baz` |
| `\|a` と `b` と `c` の 3 行で `VjjA,<Esc>` | `a,`、`b,`、`c,` |

- 打っている間から、すべての行にテキストが出る。カーソルは選んだ範囲の最初の行にだけ出る
- `<BS>`、`insert.delete-prev-word`、`insert.delete-to-line-start` のように、入力の位置から見た相対の位置を消す操作は、すべての行で同じように消す
- `insert.delete-to-line-start`（同梱の例では `<C-u>`）は、各行の先頭まで消す。入力したテキストだけでなく、もとの行の中身と字下げも消す（`  ab`、`  cd` で `VjA<C-u>X<Esc>` は `X`、`X`）。入力したテキストだけを打ち直すときは `<BS>` か `insert.delete-prev-word` を使う
- カーソルキー（`<Left>`、`<Home>` など）でカーソルを動かすと、まとめての入力を終える。それまでに入れたテキストはすべての行に残り、その後はカーソルの位置だけに入る
- カーソルが動かないとき（`A` の直後の `<End>` や、同梱の例の `<C-e>`）は、まとめての入力を続ける
- 改行（`<CR>`）、行の先頭の `<BS>`（前の行とつなぐ）、行末の `<Del>`（次の行をつなぐ）も、行をまたぐのでまとめての入力を終え、カーソルの位置だけで行う
- 端末から貼ったテキストは、打った文字と同じく 1 文字ずつ入る。改行の前まではすべての行に入り、改行の後はカーソルの位置だけに入る
- `2I` のように回数を付けると、各行で入力を繰り返す
- `<Esc>` の後の `u` は、すべての行の入力を 1 回で戻す
- `.` は、カーソルの行から同じ数の行に、同じ規則で入力し直す。行が足りなければバッファの最後の行で止めるが、次の `.` はまた同じ数の行に入れる
- 入力の途中に拡張機能がバッファを書き換えたとき（MCP の `edit_buffer`、ACP の書き込みなど）は、各行の入力の位置がそれぞれ編集に合わせてずれる。消された範囲や書き換えられた範囲の中（範囲の始まりより後ろ）にあった位置は無くなり、その行にはそれ以降の入力が入らない（カーソルの行を除く）

Vim では、矩形選択でないビジュアルモードの `I` と `A` は 1 か所にしか入力しない（[vim-differences.md](vim-differences.md)）。

## 編集の禁止

バッファごとに編集を禁止できる（要件定義 FR-85、FR-86、FR-119、FR-120、基本設計 4.6）。
禁止中も、移動、検索、ヤンク、ビジュアルモードの選択、`:w` での保存はできる。
テキストを変える操作（`x`、`d`、`p`、`u`、`:s`、`:d` など）とインサートモードへ移る操作（`i`、`o` など）は、何も変えずに `E21: 編集を禁止したバッファは変更できません` を出す。
マクロの再生は、テキストを変える操作が断られた時点で止まる。

| 禁止にする方法 | 対象 |
|---|---|
| `:readonly`（`:reado` まで省略できる） | カーソルのあるバッファ。打つたびに禁止と解除が切り替わる |
| 起動のオプション `-R`（`vi-imitation -R a.go`） | 起動の引数のファイル。`:e` で後から開くファイルは禁止しない |
| 設定の `readonlyPaths` | 並べたディレクトリの下のファイル。開いた時点で禁止にする。`/` か `~/` で始まるパスはそのディレクトリの下、`/` を含まない名前（`node_modules`）はどの階層のディレクトリの名前と一致しても当たる |

```jsonc
{
  // 開いた時点で編集を禁止にするディレクトリ。絶対パス、~/ で始まるパス、/ を含まないディレクトリの名前で書く
  "readonlyPaths": ["~/go/pkg/mod", "/usr/include", "node_modules"]
}
```

禁止中のバッファでは、ステータス行の名前の後に `[RO]` が出る（FR-87）。
印の文字と出す位置は、[見た目の設定](#見た目の設定)の `symbols` の `readonly` と `statusline` で変えられる。
`-R` と `readonlyPaths` で禁止したバッファも、`:readonly` で解けば編集できる。
禁止の前のアンドゥの履歴は残るので、解いた後に `u` で戻れる。
拡張機能の編集（`buffer/edit`）も断り、拡張機能へは誤りの応答を返す。
Vim との違い（Vim の `-R` は編集を許して保存を断る、など）は [vim-differences.md](vim-differences.md) にある。

## 外部のコマンドとエディタの中のエディタ

`:!{コマンド}` は、Vim と同じく端末をコマンドに渡して動かす（要件定義 FR-80、基本設計 4.9）。
画面は代替画面に入る前の端末に戻り、コマンドの出力が出る。
コマンドが終わると `続けるには ENTER を押してください` が出る。
`<CR>`、`<Space>`、`<Esc>` で画面に戻り、それ以外のキーは画面に戻った後にコマンドとして使う（`:` ならコマンドラインに入る）。
終了コードが 0 でなければ、その前に `シェルが N を返しました` が出る。シグナルで終わったときは `コマンドが中断されました` が出る（Vim の `Command terminated`）。
`:!sh` のような対話のコマンドも使え、`<C-c>` はコマンドに届き、エディタは終わらない。

`:!` などでエディタが起動したコマンドには、環境変数 `VI_IMITATION_SOCKET`（server のソケット）、`EDITOR` と `GIT_EDITOR`（`vi-imitation` の実行ファイル）が入る（FR-82）。
このもとで `vi-imitation` を起動すると、新しい画面を作らずに、渡したファイルを `:!` を打った client のタブで開き、タブが閉じられるまで待つ（FR-83、FR-117）。
たとえば `:!git commit` では、コミットのメッセージがタブで開く。
書いて `:wq` でタブを閉じると、git がコミットを続け、終わると `続けるには ENTER を押してください` が出る。
`:!sh` の中で `git commit` を打っても同じに動く。

入れ子のエディタとして別に使いたいときは、`vi-imitation --new <ファイル>` と打つ（FR-84）。
親の server に繋がずに、渡された端末で新しいエディタが動く。

### コマンドをポップアップで動かす

`:terminal.popup {コマンド}` は、コマンドを端末多重化ソフト（tmux、herdr）の画面の中央のポップアップで動かす（FR-168）。
`lazygit` や `keifu-wt` のような TUI を、エディタの画面の上に開くのに使う。

```text
:terminal.popup git log --oneline
```

- コマンドは `:!` と同じに読む。`|` を含めて行末までがコマンドで、`%` などは展開しない。シェルは `:{範囲}!` と同じく環境変数 `SHELL`（無ければ `/bin/sh`）である
- 作業ディレクトリは `:!` と同じく、打った client の作業ディレクトリである
- ポップアップの幅はファインダーの窓と同じく `preview.windowWidthPercent`（既定 90）% で、高さは 80% である
- コマンドが 0 で終わるとポップアップが閉じる。0 でない終了コードで終わったときは、出力を読めるように開いたまま残し、`シェルが N を返しました` と `閉じるには何かキーを押してください` を出す。tmux でも herdr でも、どのキー（`q`、Enter、Escape など）でも閉じる。コマンドの実行中に打ったキーは捨てるので、出力を読む前には閉じない
- ポップアップが開いている間も、エディタは止まらない。コマンドが変えたファイルは、外での変更の確認（`autoread` と `checkIntervalMs`）が読み直し、ポップアップの外に見えている画面にも出る（2026-11-21 に tmux 3.6 で、ポップアップを開いたまま下のペインが描き直されることを確かめた）
- ポップアップの中のコマンドには、`:!` と違い `VI_IMITATION_SOCKET`、`EDITOR`、`GIT_EDITOR` を入れない。ポップアップの中で `vi-imitation` を起動すると、下のペインで起動したときと同じ server に繋ぎ、ポップアップの中に画面が出る（バッファとレジスターを下のエディタと共有する）
- 端末多重化ソフトの外では、`:!` と同じく端末をコマンドに渡して動かし、終わると `続けるには ENTER を押してください` が出る。外で動かしたことは知らせない
- 利用者の別のポップアップが開いていて開けないときと、herdr のプラグインを登録していないとき（[herdr のプラグインを登録する](#herdr-のプラグインを登録する)）は、メッセージの行に理由を出す
- コマンドを書かないと `E471` になる

キーには、`keymap` の Ex コマンド（[keymap から Ex コマンドを呼ぶ](#keymap-から-ex-コマンドを呼ぶ)）で割り当てる。

```jsonc
{
  "keymap": {
    "normal": {
      "<Leader>gk": { "command": "terminal.popup", "args": "keifu-wt" },
      "<Leader>gl": { "command": "terminal.popup", "args": "lazygit" },
    },
  },
}
```

### 権限の足りないファイルの保存

権限の足りないファイル（`/etc/hosts` など）への `:w` は、`権限を上げて書き込みますか (y/n)?` と問う（FR-79）。
シンボリックリンクなら、問いにはたどった先のパスが出て、先のファイルへ書く。
ファイルには書けて、ディレクトリにだけ書き込みの権限が無いときは、問わずにそのまま書く（Vim と同じ）。
`y` で答えると、画面を一時的に通常の端末へ戻し、`sudo tee <パス>` にバッファの中身を渡して書き込む。
パスワードは `sudo` が端末から直接聞き、エディタはパスワードを読まない。
書き込めたら、バッファは保存済みになる（`:wq` なら続けてタブを閉じる）。
書き込む先のディレクトリが無く、権限が足りずに作れないときも同じく問い、`sudo mkdir -p <ディレクトリ>` で作ってから書き込む（FR-116）。

`sudo` の代わりに使うコマンドは、設定の `elevateCommand` で変えられる。
値はコマンドと引数の配列で、この後に `tee <パス>` か `mkdir -p <ディレクトリ>` を付けて、シェルを通さずに起動する。

```jsonc
{
  "elevateCommand": ["doas"]
}
```

`doas` も、標準入力がパイプのときにパスワードを端末から読むことを、OpenDoas のソースで確かめた（[基本設計 4.7](basic-design.md#47-保存の先のディレクトリと権限の不足)）。
実物の `doas` では動かしていない。

## レジスターと履歴

レジスターの名前は、`a`〜`z`、`_`、名前の省略の 3 種類だけである（要件定義 FR-50）。
Vim の数字のレジスター（`"0`〜`"9`）、`"-`、`""`、`".`、`":`、`"+`、`"*`、`"/` などは無い。
無い名前を指定した操作（`"0p`、`"+yy`、`q1`、`@-`）は、何もせずに `E354` をメッセージの行に出す。

ヤンクと削除で書いたテキストは、レジスターの履歴に新しい順に積む（FR-51）。
名前を省略した `p` と `P` は、履歴の先頭（最後に積んだもの）を貼る。
貼っても履歴の順は変わらない。

| 操作 | 名前付きのレジスター | 履歴 |
|---|---|---|
| `yw`、`yy` など、名前を省略したヤンク | 変えない | 先頭に積む |
| `dw`、`x`、`cw`、`:d` など、名前を省略した削除と変更 | 変えない | `deleteToHistory` が真（既定）なら先頭に積む。偽なら何もしない |
| `"ayw`、`"add` など、名前を指定した書き込み | `a` に書く | 書いた内容を先頭に積む |
| `"Ayw` など、大文字での追記 | `a` に追記する | 追記した後の `a` の内容全体を先頭に積む |
| `"_dd` など、`_` を指定した操作 | 変えない | 変えない |
| `qa`〜`q` のマクロの記録 | `a` に書く（`qA` は追記） | 変えない |
| ビジュアルモードでの端末からの貼り付けで置き換えたテキスト | 変えない | 変えない |

履歴の件数と、1 件あたりの最大の大きさは、設定ファイルの `registers` で変えられる（FR-52、FR-53）。

```jsonc
{
  "registers": {
    // 履歴の件数。超えたら最も古いものから捨てる。1 以上 10000 以下（既定 100）
    "historySize": 100,
    // 履歴に積む 1 件の最大のバイト数。1 以上（既定 1048576 = 1 MiB）
    "maxEntryBytes": 1048576,
    // 名前を省略した削除と変更を履歴に積むか（既定 true）
    "deleteToHistory": true,
    // 履歴の先頭をホストのクリップボードへ送る手段。"osc52" か "off"（既定 "osc52"）
    "clipboard": "osc52",
  },
}
```

`maxEntryBytes` を超えたテキストは履歴に積まず、積まなかったことをメッセージの行に出す。
そのテキストは、次に履歴に積むまでの間だけ、名前を省略した `p` で貼れる。
名前を指定した書き込み（`"ayG`）は、大きさによらずそのレジスターに書く。

### ホストのクリップボードへの送り出し

`clipboard` が `"osc52"`（既定）なら、履歴の先頭が変わるたびに、その内容を OSC 52 の制御シーケンスで端末へ送る（FR-56）。
端末が OSC 52 に対応していれば、ホストのクリップボードに入る。
SSH の先で動かしても、手元の端末のクリップボードに入る。
行単位のテキスト（`yy`、`dd`）は、最後に改行を付けて送る。
`maxEntryBytes` を超えて履歴に積まなかったテキストは送らない。
base64 にして 1,000,000 バイト（元のテキストでおよそ 750,000 バイト）を超えるテキストも送らず、送らなかったことをメッセージの行に出す（仮の上限。tmux 3.4 は base64 で 1 MiB を超える OSC 52 を黙って捨てる）。
貼り付けと `"_` への書き込みは履歴の先頭を変えないので、送らない。

ホストのクリップボードは読まない。
ホストでコピーしたテキストは、端末の貼り付け（bracketed paste）で入れる。

tmux の中で動かすときは、tmux の `set-clipboard` を `on` にする。
tmux は、`on` のときに OSC 52 を受け取って tmux のバッファに入れ、外側の端末のクリップボードにも入れようとする（外側の端末の terminfo に `Ms` があるとき）。
既定の `external` では、tmux の中のアプリケーションが送った OSC 52 を受け付けない（tmux 3.4 の man の `set-clipboard`）。

```sh
# ~/.tmux.conf
set -g set-clipboard on
```

送ったテキストが tmux に届いたかは、`tmux show-buffer` で確かめられる。

### 履歴の一覧から貼る

`:registers`（`:reg`）で、履歴と名前付きのレジスターの一覧を、画面の上の端に浮かべる窓に出す（FR-54）。
窓の行は、上から履歴の項目を新しい順に、続けて中身のある名前付きのレジスターを `a` から順に並べる。
各行は、番号（履歴）か `"a` の形の名前、種類、テキストの 1 行目の順に並ぶ。
種類は、行単位なら `l`、文字単位なら `c` で、2 行以上にわたるテキストなら `l+2` のように 2 行目以降の行数を続ける。
画面が小さく窓を描けないとき（バッファの表示領域が 2 行に満たないとき）は、窓を開かずにメッセージの行に知らせる。

| キー | 動作 |
|---|---|
| `j`、`<Down>` | 次の項目を選ぶ |
| `k`、`<Up>` | 前の項目を選ぶ |
| `<CR>` | 選んだ項目を、`p` と同じくカーソルの後に貼って窓を閉じる |
| `<Esc>` | 貼らずに窓を閉じる |

他のキーは、窓を出している間は捨てる。
選んで貼った履歴の項目は、履歴の先頭へ移る。
その後の `p` と `.` は、同じテキストを貼る。
名前付きのレジスターを選んだときは、`"ap` と同じく貼り、履歴の順は変えない。

マクロの中に `:reg<CR>` を記録しても、窓での選択（`j`、`<CR>` など）は記録されない。
再生すると、`:reg<CR>` の後に記録したキーを先に実行し、窓は再生が終わってから開く。

キーで開きたいときは、設定ファイルの `keymap` で割り当てる。

```jsonc
{
  "keymap": {
    "normal": {
      "<Space>r": ":registers<CR>",
    },
  },
}
```

### 貼った直後に履歴の別の項目へ貼り直す

`p`、`P`、一覧からの貼り付けの直後に `<C-p>` を打つと、貼ったテキストを、履歴の 1 つ古い項目に置き換える（FR-55）。
続けて打つと、さらに古い項目に置き換える。
`<C-n>` は新しいほうへ戻る。
`3<C-p>` のように回数を付けると、その数だけ進む。

- 置き換えは、元の貼り付けと同じ位置に、同じ向き（`p` か `P`）と同じ回数で入れる
- 置き換えは、元の貼り付けとアンドゥの 1 単位にまとまる。`u` の 1 回で、貼り付けの前に戻る
- 置き換えても、履歴の順は変わらない
- いま貼っているテキストと同じ内容の項目は飛ばす。`"ayiw` は `a` と履歴の先頭に同じテキストを書くので、その後の `"ap` の直後の `<C-p>` は、履歴の 2 番目に置き換える
- `"ap` の直後の `<C-p>` は、履歴の先頭（`a` と同じ内容なら、その次）に置き換える。そこから `<C-n>` を打つと、`a` の内容に戻る
- 置き換える項目が貼り付けの上限（64 MiB）を超えるときは、元の貼り付けを残したまま、メッセージの行に知らせる
- 貼り付けの後に別の操作（カーソルの移動、`u`、ヤンクなど）を挟むと、`<C-p>` と `<C-n>` は何もしない
- 履歴の端で打っても何もしない。反対の向きのキーは続けて使える
- `.` は、置き換えた後も元の貼り付け（`p` なら履歴の先頭を貼る）を繰り返す

## ポップアップ

ホバーの説明、「ポップアップで見る」の写し、ヘルプ、キーの一覧、拡張機能が出す一覧は、画面の上に浮かべたポップアップに出る（FR-68）。
ポップアップの中身はバッファなので、フォーカスを入れると `j`、`k`、`<C-d>`、`/`、`y` などのノーマルモードのキーが効く。
`q` は、ポップアップの中でもマクロの記録である。
ホバー、写し、ヘルプ、キーの一覧のバッファは編集できない（[編集の禁止](#編集の禁止)）。

置き場所は 2 つある。

| 置き場所 | 出るもの | フォーカス |
|---|---|---|
| カーソルの近く | ホバー、「ポップアップで見る」の写し、診断の一覧、[診断のポップアップ](#診断のポップアップ) | 入っていない。次のキーかカーソルの移動で閉じる。`<C-w>w` で入れる |
| 画面の中央 | ヘルプ、キーの一覧、`:popup` で縮めたタブ、`:ext sync` の内訳 | 開いたときから入っている。閉じるまで外れない |

フォーカスの入ったポップアップでは、次のキーとコマンドが使える。

| キー、コマンド | 動作 |
|---|---|
| `<Esc>`（ノーマルモード） | ポップアップを閉じる |
| `<C-w>w` | カーソルの近くのポップアップから元のタブへ戻る。ポップアップは残り、もう一度 `<C-w>w` で入る |
| `<CR>`（一覧） | カーソルの行の項目を選ぶ |
| `:tabopen`（`:tabop`） | ポップアップのバッファをタブへ広げる。写しなら、本物のファイルを開いて移動先へ移る |
| `:q` | ポップアップを閉じる |

`:popup`（`:popu`）は、カーソルのあるタブを中央のポップアップへ縮める（FR-70）。
縮めたタブは、ポップアップの中で通常どおり編集でき、未保存の変更もそのまま残る。
`:tabopen` でタブへ戻し、`<Esc>` で閉じるとタブの並びへ戻る。
タブが 1 つだけのときは縮められない。

tmux と herdr の中では、中央のポップアップを端末多重化ソフトのポップアップ（tmux の `display-popup`、herdr のプラグインのポップアップ）に出す（FR-67）。
ポップアップの中では本体が部品だけを描き、打ったキーは、ポップアップを開いたペインの client の操作になる。
端末多重化ソフトの外と、端末多重化ソフトのポップアップを開けなかったとき（herdr のプラグインを登録していないなど）は、エディタの画面の中央に描き、開けなかった理由をメッセージの行に出す。

拡張機能は、一覧や木を端末多重化ソフトのペインに開ける（`pane/open`。FR-115）。
ペインは、拡張機能へ最後に問い合わせたペインを分割して開き、中のキーと選んだ項目は、そのペインの client の操作になる。
ペインの中の `<Esc>` はペインを閉じない。閉じるには、ペインの中で `:q` を打つか、端末多重化ソフトでペインを閉じる。
端末多重化ソフトの外では、ペインに代えて画面の中央のポップアップに出し、`<Esc>` で閉じる（FR-123）。
拡張機能は、端末多重化ソフトの中でもペインに代えて中央のポップアップに出すことと、中央のポップアップで項目を選んだら閉じることを指定できる（FR-147。[ファイラ](#ファイラ)の `<Space>e`）。

### herdr のプラグインを登録する

herdr の中で中央のポップアップとペインを開くには、配布物の `herdr-plugin/` にある同梱のプラグインを、herdr に 1 回登録する。

```sh
herdr plugin link dist/vi-imitation-<コミット>-<OS>-<アーキテクチャ>/herdr-plugin
```

登録していないと、herdr の中でもエディタの画面の中央に描き、メッセージの行に「herdr のプラグインを登録してください」と出す。
プラグインは、本体が起動を頼んだコマンドを動かすだけで、本体の実行ファイルの場所を持たないので、配布物を入れ替えても登録し直さなくてよい。
外すときは `herdr plugin unlink vi-imitation` を打つ。

## ヘルプとキーの一覧

`:help {語}`（`:h {語}`）は、この文書を本体の中で開き、語の説明のある行へカーソルを置く（FR-89）。
文書は本体に埋め込んであるので、配布物の外でも読める。
語には、キーの列（`:help w`、`:help <C-o>`）、Ex コマンドの名前（`:help :w`、`:help :tabmove`）、設定の項目の名前（`:help registers.historySize`）を書く。
Vim と同じく、`:` の無い語はまずキーや設定の名前として引き、無ければ `:` を足したコマンドとして引く（`:help w` は `w` のキー、`:help :w` は `:w` のコマンド）。
`` `語` `` と書いた箇所のうち、見出し、表の最初の列、箇条書きの先頭を先に探し、次に文書のどこかの `` `語` ``、その次に語で始まる名前（`:help reg` は `:registers`）を探す。
語を省くと先頭を開く。
説明が見つからなければ、`E149: {語} のヘルプがありません` と出る。
文書の末尾には、本体のキーの一覧（キーの一覧と同じ説明文）と、読み込んだ拡張機能の操作の一覧（宣言ファイルの `contributes` と説明文）が付く。
拡張機能のコマンドはこの一覧に `:` 付きで載るので、`:help :acp.prompt` でも `:help acp.prompt` でも引ける。
引ける語は、ファジーファインダーの `<Space>fh`（`:finder.help`）で絞り込んで選べる（FR-148。[ファジーファインダー](#ファジーファインダー)）。

キーの一覧は、接頭辞の後に `?` を打って出す（FR-90）。
`<leader>l?` は `<leader>l` で始まるキーと LSP の操作のキー（`K`、`gq`、`<C-]>` を含む）、`<leader>d?` は `<leader>d` で始まるキーと DAP の操作のキーの一覧である。
`<leader>?`、`g?`、`<C-w>?`、`s?` のように、それ自体は操作でない接頭辞の後の `?` でも、その接頭辞で始まるキーの一覧が出る。
`d` のように、それ自体が操作のキーの後の `?` は、これまでどおり `?` の検索になる（`d?foo<CR>`）。
`:keys {接頭辞}` は、接頭辞で始まるキーの一覧を出す（`:keys <Leader>d`）。
`<leader>l?` と違い、`:keys <Leader>l` は LSP の操作のキー（`K`、`gq`、`<C-]>`）を出さない。
接頭辞を省くと、すべてのキーと、回数、レジスター、マクロ、ドットリピートのキーを出す。

一覧の各行は、キー、モード（`n` はノーマル、`v` はビジュアル、`o` はオペレーターの後、`i` はインサート、`c` はコマンドライン）、出どころ、説明である。
出どころは、本体の既定のキーが `本体`、設定の `keymap` が `設定`、拡張機能が `拡張機能 <ID>` である。
設定の `keymap` の項目の説明は、`→ ` の後の展開先のキーの列である。
右辺に操作の ID を書いた項目の説明は、その操作の説明（[操作の ID の一覧](#操作の-id-の一覧)の説明の列）である。
設定の `keymap` が本体か拡張機能のキーと同じキーを割り当てていると、両方の行を出し、効かないほうの行の説明の後に `（設定の keymap が優先）` と付ける。
`keymap` で `s` を割り当てたときの `sa` のように、手前の部分を割り当てて打てなくなったキーにも同じ印を付ける。
一部のモードだけを割り当てたときは、`（v では設定の keymap が優先）` のように効かないモードを書く。
並びは、キーを大文字と小文字を区別せずに比べた順（`<Space>ld`、`<Space>lD`、`<Space>le` の順）で、`?` の行は末尾に置く。
拡張機能の操作の説明は、宣言ファイルの `contributes` の各項目に書いた `description` で、書いていなければ操作の ID を出す（[スクリプトの拡張機能](#スクリプトの拡張機能)の例）。

ヘルプとキーの一覧は、画面の中央のポップアップに出す（[ポップアップ](#ポップアップ)）。
どちらも編集を禁止したバッファ（[編集の禁止](#編集の禁止)）で、移動、検索、ヤンクはできる。
ヘルプの長い行は、窓の幅で折り返す。`:tabopen` でタブへ広げられる。
キーの一覧で `<CR>` を打つと、ポップアップを閉じてから、カーソルの行のキーを打ったのと同じく実行する。
設定の `keymap` を通すので、上書きされた本体の行を選ぶと、上書きした方が動く。
回数、レジスター、マクロ、ドットリピートの行と、ノーマルモードでないキーの行は、選んでも実行しない。
閉じるときは `<Esc>` を打つ。

### 起動の案内

起動の引数にファイルを書かずに起動すると、Vim と Neovim と同じく、画面の中央に起動の案内を出す（FR-205）。
案内には、名前と版（`vi-imitation --version` と同じ文字列）、`:q` で終わること、`:help` でこの文書を開けること、設定ファイルの場所を書く。
設定ファイルは、起動のときに読む `$XDG_CONFIG_HOME/vi-imitation/config.jsonc`（未設定なら `~/.config/vi-imitation/config.jsonc`）の場所を出す。
まだ無ければ `設定ファイル（まだ無い）` の下に、置く場所として出す。
ホームディレクトリの下のパスは、`~/` からの形で出す。
文面は仮で、後で変わる。

案内は、Neovim 0.12.5 と同じく、次のどれかが起きた後は出さない。
一度消えたら、元の状態へ戻しても（打った文字を消す、`u` で戻す、開いたタブを閉じる）出ない。

- バッファに文字を打つか、行を足す
- ファイルを開くか、別のタブへ移るか、`:w ファイル` でバッファに名前を付ける
- ヘルプ、ファジーファインダーなど、フォーカスを入れるポップアップを開く
- 出力の欄を開くか、拡張機能が端末多重化ソフトのペインに部品を開く
- 2 つ目の client が繋ぐ（同じ server に、別のペインの `vi-imitation` が繋いだときなど）
- 設定 `intro.show` を `false` にする

インサートモードに入る、Ex コマンドを打つ、メッセージが出る、端末の大きさを変える、ホバーのようなフォーカスを入れないポップアップが出るだけでは消えない。
案内は `~` の行の上に描き、ステータス行とコマンドラインの行は覆わない。
案内の全体が、上と下に 1 行、左と右に 1 列の余りを残して画面に収まらないときは、出さない。
設定ファイルのパスの行だけは収まるかの判定に数えず、幅に収まらなければ左を `…` にして詰める。
ホバー、拡張機能の進み具合、メッセージの行は、案内の上に重なる。

`:intro`（`:int`）は、いつでも、画面のバッファの表示領域を消してから案内を出す（FR-207）。
次のキーを 1 つ打つと消える。そのキーは案内を消すだけで、コマンドにはならない（Vim と Neovim と同じ）。
`:intro` のたびに、設定ファイルがあるかを確かめ直す（案内を見て置いた設定ファイルを確かめられる）。
キーマップの右辺に `:intro<CR>` と続くキーを書くと、続くキーの最初の 1 つで消える。
マクロの再生の中の `:intro` は、案内を出さない（[Vim との違い](vim-differences.md)）。

起動したときに出さないときは、設定ファイルに `"intro": { "show": false }` と書く（FR-206。Vim と Neovim の `:set shortmess+=I` に当たる）。
`:intro` は、この設定によらず出す。

### 本体に埋め込んだ文書（`--docs`）

`vi-imitation --docs` は、エディタを開かずに、本体に埋め込んだ文書を標準出力に出して終わる（要件定義 FR-172）。
配布物だけで、ソースを取らずに読める。
AI エージェントに設定、キーの割り当て、拡張機能を書かせるときは、要る話題だけを読ませる。

```sh
vi-imitation --docs                                  # 話題の一覧（名前、バイト数、内容）
vi-imitation --docs config                           # 話題の文書の全体
vi-imitation --docs usage 'Ex コマンドの一覧'          # 見出しが「Ex コマンドの一覧」の節だけ
vi-imitation --docs extension-types BufferOpenParams # 拡張機能 API の型を 1 つだけ
vi-imitation --docs --headings usage                 # 話題の文書の見出しの一覧（コードの囲みの中の # の行は含まない）
```

話題は、設定（`config`）、キーの割り当て（`keymap`）、Ex コマンド（`commands`）、拡張機能（`extensions`、`extension-types`）、スクリプトの拡張機能（`script`、`script-symbols`）、デバッグの案内（`debugging`。[docs/debugging.md](debugging.md)）、この文書（`usage`）に分けてある。
話題ごとの内容と大きさは `vi-imitation --docs` の一覧にある。
`<節>` は、見出しの `#` の後の文字列で、`` ` `` は省いてよい。
知らない話題と節は、誤りを出して終了コード 1 で終わる。
一覧の部分（設定の項目、操作の ID、Ex コマンド、拡張機能 API）は、本体の表から生成したもので、その本体の表と食い違わない。

## 標準の拡張機能

配布物に入っている標準の拡張機能は次のとおりである。
「起動の契機」は、本体が拡張機能のプロセスを起動するきっかけで、宣言ファイル（`extensions/<ID>/manifest.json`）の `activation` に書いてある。

| ID | 機能 | 起動の契機 | コマンド、キー | 別に要るもの |
|---|---|---|---|---|
| `treesitter` | 構文ハイライト（同梱は Go、JSON、JSONC。設定でほかの言語のパーサを足せる）、構文に沿ったテキストオブジェクト（構文のまとまりはすべての言語、引数は Go） | Go か JSON か JSONC（`.jsonc`）のファイルか、設定でパーサを足したファイルの種類のファイルを開いたとき | `an`、`in`、`aa`、`ia` | 同梱の言語はなし。パーサを足すなら PATH の `git` と C コンパイラ（`cc`） |
| `line` | 行のテキストオブジェクト | `il`、`al` を打ったとき | `il`、`al` | なし |
| `lsp` | 診断と、LSP の操作（定義へ移動、ホバー、参照、シンボル、整形、名前の変更） | Go のファイルを開いたとき | [LSP と DAP の既定のキー](#lsp-と-dap-の既定のキー) | PATH の `gopls`。ほかの言語の言語サーバーは、設定で足して入れる（[LSP](#lsp)） |
| `dap` | デバッグ（DAP の操作） | 操作を呼んだとき | [LSP と DAP の既定のキー](#lsp-と-dap-の既定のキー) | PATH の `dlv` |
| `acp` | コーディングエージェントとの対話（ACP のクライアント） | コマンドを呼んだとき | `:acp.prompt`、`:acp.cancel` | ACP で話すエージェントと、その設定 |
| `mcp` | エディタの状態と編集を MCP のツールとして提供する | MCP のクライアントが最初に接続したとき | `:mcp.start` | MCP のクライアント |
| `git` | バッファのファイルを git の HEAD の版と見比べる。ブランチとの差分があるファイルの一覧から開いて見比べる | コマンドを呼んだとき | `<Leader>gd`、`:git.diffHead`、`<Leader>gV`、`:git.changes`、`<Leader>gv`、`:git.changesHead` | PATH の `git`、端末多重化ソフト（tmux、herdr） |
| `finder` | ファジーファインダー（ファイル、検索、タブ、行、キー、コマンド、レジスター、ジャンプリスト） | コマンドを呼んだとき | [ファジーファインダー](#ファジーファインダー) | 検索には PATH の `rg`。ファイルの一覧には `fd`（`fdfind`）か `rg` があれば使う |
| `filer` | ツリー型のファイラ | コマンドを呼んだとき | `<Leader>e`、`<Leader>E`、[ファイラ](#ファイラ) | gitignore されたものを隠すには PATH の `git`。分割したペインに出すには端末多重化ソフト（tmux、herdr） |
| `formatter` | 外部の整形ツールで、保存の前と `gq`、`:format` で整形する | 設定に整形ツールを書いたファイルの種類のファイルを開いたとき | `:w`、`gq`、`:format`（[整形ツール](#整形ツール)） | 設定に書いた整形ツール（biome、ruff など） |
| `memusage` | 本体と、拡張機能と言語サーバーなどのメモリの使用量（RSS）をステータス行に出す | 部品 `memusage.rss` を `statusline` か `tabline` に並べたとき（本体の起動時）か、コマンドを呼んだとき | `:memusage.show`（[メモリの使用量](#メモリの使用量)） | Linux 以外では PATH の `ps` |

### tree-sitter による構文ハイライト

Go と JSON と JSONC（ファイルの種類 `jsonc`。拡張子 `.jsonc` のファイルで、設定ファイル `config.jsonc` を含む）のファイルを開くと、予約語、型、文字列、数値などに色が付く。
JSONC のファイルは JSON と同じ規則で色を付け、コメントにも色が付く。
コメントと末尾のカンマを含んでいても、その後ろの文字列や数値に色が付く。
編集すると、その場で色が付け直される。
同梱のこれらの言語には、設定は要らない。

`an`/`in` は、構文のまとまり（関数、`if` と `else`、`for`、型の宣言、オブジェクトや配列など、中に文や要素を持つもの）の全体と中身（`{}` や `[]` の内側、関数の本体）を選ぶ。
同梱の言語と、パーサを足した言語のどれでも使える。
`v`、`d`、`c` などと組み合わせて使え、回数を付ける（`2an`）と 1 段ずつ外側のまとまりを選ぶ。
ビジュアルモードで繰り返す（`vanan`、`vinin`）と、今の選択より 1 段外側のまとまりへ選択を広げる。
`in` は、カーソルを含む中身だけを選ぶ。関数の見出しや `if` の条件の上で `in` を打つと、その本体ではなく、外側のまとまりの中身を選ぶ。カーソルを含む中身が無ければ何もしない。
`an` は、まとまりの後ろに区切りの `,` があればそれまで含める（JSON の要素や Go のテーブル駆動のテストの要素を `dan` で消しても `,` が残らない）。
`else`、`elif` の節の中の `an` は `if` の全体を、`in` はその節の本体だけを選ぶ。
Go のファイルでは、ほかに `aa`/`ia`（引数と区切りの `,`、引数 1 つ）も使える。
ドットリピートとマクロでも繰り返せる。

まとまりは、言語ごとの表を使わず、構文木の形（括弧、`body` などのフィールド、`fi` や `end` のような閉じる予約語）で決める（[基本設計](basic-design.md) 6.1）。
このため、次の書き方はまとまりにならず、中で `an` と `in` を打つと、外側のまとまりを選ぶか何もしない。

| 書き方 | まとまりにならない理由 |
|---|---|
| TOML の表（`[server]` から次の表まで） | 表の名前の後にキーと値の対が続くだけで、閉じる記号が無い |
| TSX の JSX の要素（`<ul>` から `</ul>` まで） | 開きの要素と閉じの要素で囲む形で、括弧でも予約語でもない。中の `{...}` と複数の行の `(...)` はまとまりになる |
| Python の `except` と `finally` の本体 | 文法が本体にフィールドの名前を付けていない。中の `an` は `try` の文の全体を、`in` は外側のまとまりの中身を選ぶ |
| Lua の中身の無い関数（`function empty() end`） | 本体のノードが無い |

#### パーサを足す

同梱していない言語は、その言語の tree-sitter のパーサ（文法）の git のリポジトリと版を、設定ファイルの `extensions.settings.treesitter.parsers` に書いて足す（FR-142）。
例は tree-sitter-python の v0.25.0 を足すものである。

```jsonc
{
  "extensions": {
    "settings": {
      "treesitter": {
        "parsers": {
          // 名前は、取得したソースとビルドしたものを置くディレクトリと、ロックファイルの記録の名前になる
          "python": {
            "repo": "https://github.com/tree-sitter/tree-sitter-python",
            // タグ、ブランチ、コミットのハッシュ
            "version": "v0.25.0",
            // このパーサを使うファイルの種類（ステータス行の filetype の部品が出す名前）
            "fileTypes": ["python"],
          },
        },
      },
    },
  },
}
```

| 項目（仮） | 内容 | 省いたとき |
|---|---|---|
| `parsers.<名前>.repo` | パーサの git のリポジトリ。URL か絶対パス | 省けない |
| `parsers.<名前>.version` | 取り出す版。タグ、ブランチ、コミットのハッシュの順に探す | 省けない |
| `parsers.<名前>.fileTypes` | このパーサを使うファイルの種類の列 | 省けない |
| `parsers.<名前>.path` | `src/parser.c` のある `src/` の親のディレクトリ（リポジトリの中の相対パス）。1 つのリポジトリに複数の文法があるとき（tree-sitter-typescript の `typescript` と `tsx` など）に書く | リポジトリの最上位 |
| `parsers.<名前>.queries` | ハイライトのクエリのファイル（リポジトリの中の相対パス）の列。複数あれば順につなぐ | `path` の下の `queries/highlights.scm`、無ければ最上位の `queries/highlights.scm` |
| `compiler` | パーサをビルドする C コンパイラ | PATH の `cc` |

パーサを足したファイルの種類のファイルを初めて開くと、次の順に用意する。
用意の間も編集を続けられる。
進み具合は画面の右下（[拡張機能の進み具合](#拡張機能の進み具合)）に表題 `tree-sitter` で出て、失敗の理由はメッセージの行に出る。

1. `git` でリポジトリを `$XDG_DATA_HOME/vi-imitation/treesitter/src/<名前>/` に取得し、`version` のコミットを取り出す（進み具合「パーサ python を取得している」）
2. C コンパイラで `src/parser.c` と、あれば `src/scanner.c` を共有ライブラリにして、ハイライトのクエリと一緒に `$XDG_DATA_HOME/vi-imitation/treesitter/parsers/<名前>/` の下に置く（「パーサ python をビルドしている」）。tree-sitter の CLI は要らない
3. 取り出したコミットを、設定ファイルと同じディレクトリのロックファイル `treesitter.lock.json` に記録し、読み込んで色を付ける（「パーサ python を用意した」）

2026-11-20 に、`make dist` の配布物の本体を tmux の中で起動し、上の例の設定で Python のファイルを開いて、取得とビルドの後に色が付くことと、起動し直して開いたときはビルドせずに色が付くことを確かめた（tmux 3.6、git 2.53.0、gcc 15.2.0）。

2 回目からは、ロックファイルの記録が設定の `repo` と `version` に合い、そのコミットのビルドしたものがあれば、取得もビルドもせずに読み込む。
ロックファイルを設定ファイルと一緒に dotfiles のリポジトリに置けば、別のマシンでも同じコミットを取得する。
`version` を変えると、次に開いたときに取得し直してビルドし、ロックファイルの記録を替える。
設定に並べなくなったパーサの記録は、次にどれかのパーサを取得したときにロックファイルから消す。
設定を変えたら、本体の server を起動し直す（tree-sitter の拡張機能は起動したときに設定を読む）。

色の付け方のクエリには、パーサのリポジトリの公式の `queries/highlights.scm` を使う。
nvim-treesitter のクエリは、Neovim だけが解釈する述語（`#lua-match?`）を使うので、そのままでは色を誤る。

次のときは、そのパーサだけを使わない。
ほかのパーサと同梱の言語は、そのまま使える。

- 設定の誤り（`repo` が相対パス、知らない項目など）。最初にファイルを開いたときにメッセージの行で知らせる
- 取得できない、ビルドできない、パーサの ABI の版が合わない（tree-sitter 0.25 が読めるのは 13 から 15）、共有ライブラリを読めない。理由をメッセージの行に出し、詳しい出力（C コンパイラの誤りなど）は拡張機能のログに書く
- 2 つ以上のパーサに同じファイルの種類を書いたとき。その種類にはどれも使わない

用意に失敗したパーサは、server を起動し直すまで試し直さない。
ネットワークに出られなくて取得できなかったときも、出られるようになってから同じ server でファイルを開き直すだけでは使えないので、server を起動し直す。

同梱の言語（`go`、`json`、`jsonc`）と同じファイルの種類をパーサに書くと、パーサのほうを使う。
そのパーサを用意できなければ、同梱の言語を使う。
パーサを足したファイルの種類でも、構文のまとまりの `an` と `in` は、そのパーサの構文木で動く。
同梱の言語に無いファイルの種類には、引数のテキストオブジェクト（`aa` と `ia`）は無い。
`go` をパーサに書いたときは、色と `an`、`in` はパーサで、`aa`、`ia` は同梱の Go の文法とクエリで動く。
足したファイルの種類のために、設定の `extensions.activation` を書く必要はない（本体が `fileTypes` から起動の契機を足す）。
パーサの項目に `fileTypes` が無い（`filetypes` と綴りを誤ったなど）と、そのパーサでは拡張機能が起動しないので、本体が起動したときに設定の誤りとして知らせる。
拡張子からファイルの種類への既定の対応に無い言語（`.lua` など）は、設定の `fileTypes` の対応表（`{ ".lua": "lua" }`）にも書く。パーサの `fileTypes` には、その対応表の右辺の名前を書く。
C++ で書いた外部スキャナー（`src/scanner.cc`）を持つパーサはビルドしない。

### 行のテキストオブジェクト

`il` はカーソルの行から行頭と行末の空白を除いた範囲、`al` は行全体（行末の改行を含めない）を指すテキストオブジェクトである。
`dil` で行の中身を消し、行そのものは残す。

### LSP

Go のファイルを開くと、gopls を子プロセスとして起動する。
gopls は PATH から探す。
設定は要らない。

| 操作 | 結果 |
|---|---|
| 編集して誤りを作る | 行の左に `E`、誤りの箇所に下線が出る。カーソルをその行に置くと、最下行に `E compiler: declared and not used: y` のように内容が出る |
| `K` か `<leader>lh` | カーソルの位置の識別子の説明をポップアップで出す。次のキーで閉じる。`<C-w>w` でフォーカスを入れるとスクロールでき、`<Esc>` で閉じる |
| `<C-]>` か `<leader>lD` | 定義へ移動する。別のファイル（標準ライブラリの `fmt/print.go` など）の定義なら、そのファイルを開く。定義のファイルは新しいタブで開く。`<C-o>` で元の位置へ戻り、`<C-i>` で定義へ進み直す（[タブとジャンプ](#タブとジャンプ)） |
| `<leader>ld` | 定義の周りの 20 行の写しを、移らずにポップアップで出す。`<C-w>w` の後の `:tabopen` で定義へ移る |
| `gqq`、`gqip` など | 整形する。gopls（v0.24.0）は範囲の整形に対応しないので、全体を整形する。`u` で整形の前に戻せる |
| `:rename 新しい名前` | カーソルの位置の識別子の名前を変える。タブで開いていないファイルは、タブを増やさずに変えて書き出し、変えたファイルの一覧を最下行に出す。編集を禁止したファイルなど、変えられないファイルが 1 つでもあれば、どのファイルも変えずにそのファイルを知らせる。`u` で戻るのはカーソルのあるタブのファイルだけで、書き出したファイルは戻らない（戻すには、もう一度 `:rename 元の名前` を打つ） |
| `:w` | 書き出す前に整形する |
| （開いた直後） | gopls がワークスペースを読み込む間の進み具合（`$/progress`）を、画面の右下に `gopls: ` を付けた表題で出す（[拡張機能の進み具合](#拡張機能の進み具合)） |

ほかの操作は [LSP と DAP の既定のキー](#lsp-と-dap-の既定のキー) にある。
カーソルの行の診断を、最下行ではなくカーソルの近くのポップアップに出すこともできる（[診断のポップアップ](#診断のポップアップ)）。

言語サーバーを変える、保存の前の整形をやめるといった設定は、`extensions.settings.lsp` に書く。
項目は `go doc -u ./cmd/vi-imitation-ext-lsp settings` と `serverConfig` にある。

#### 診断のポップアップ

カーソルを止めると、カーソルの行の診断をすべて、カーソルの近くのポップアップに出せる（nvim の `CursorHold` の自動コマンドで `vim.diagnostic.open_float` を開く設定に当たる）。
設定ファイルに次のように書くと、キーを打たずに 100 ミリ秒が経ったら出す。
同梱の `config.example.jsonc` もこの値にしている。
本体の既定は Vim と同じく出さず、止まったとする時間は 4000 ミリ秒（Vim の `'updatetime'` の既定）である。

```jsonc
{
  "diagnostic": { "popup": true },
  "cursorHold": { "delayMs": 100 },
}
```

実行中は `:option.set diagnostic.popup true`（出す）、`:option.toggle diagnostic.popup`（切り替える）、`:option.set cursorHold.delayMs 100` で変える。
変えた値は、次のキーから効く。

同梱の `config.example.jsonc` の設定で、gopls（v0.21.0）が型の誤りを出した Go のファイルの 6 行目にカーソルを止めると、次のように出る（2026-11-21 に tmux の中で確かめた）。

```text
✘    6     x := f()
✘    7     ╭─診断──────────────────────────────────────────────╮
     8 }   │ 1. compiler: declared and not used: x (UnusedVar) │
~          │ 2. compiler: not enough arguments in call to f    │
~          │     have ()                                       │
~          │     want (int) (WrongArgCount)                    │
~          ╰───────────────────────────────────────────────────╯
```

- 1 つの診断は「番号. 出どころ: 本文 (コード)」で、出どころとコードが無ければ省く。重要度の重い順（誤り、警告、情報、ヒント）に並べ、診断が 1 つでも番号を付ける
- 本文は、印の列の印と同じ色の名前（`sign.diagnostic.error` など。[配色](#配色)）で色を付ける
- 出すのはノーマルモードで、キーの列の途中でなく、マクロを記録していないときだけである。インサートモードの間、`d` などの後、ビジュアルモードとコマンドラインモードの間は出さない
- 止まったと知らせるのはキーを打ってから 1 回だけで、出した後は、次のキーを打ってまた止まるまで出し直さない。起動した直後も、キーを打つまでは出さない
- ホバーなど、ほかのポップアップが出ている間は出さない
- 出したポップアップは、ホバーと同じく、次のキー、カーソルの移動、タブの移動で閉じる。ファイラのペインを開いてフォーカスがそちらへ移ったときと、ペインでキーを打ったときも閉じ、元の画面でキーを打つまでは出さない。診断が変わっただけでは閉じない。`<C-w>w` でフォーカスを入れるとスクロールできる
- 出す設定にすると、最下行には診断の本文を出さない

nvim と違う点は次のとおりである。

- nvim の `open_float` は、コードを持つ診断の行末に ` [コード]` も付ける（コードを本文に付ける `format` と合わせると 2 回出る）。ここでは「本文 (コード)」の 1 回にしている
- 窓の幅はホバーと同じく 80 列までで、nvim のように画面の幅までは広げない
- nvim の窓は `i` や `v` を打っても、文字を打つかカーソルを動かすまで残る。ここでは次のキーで閉じる

印の列の診断の印を nvim と同じ記号にするには、`symbols` に `"sign.diagnostic.error": "✘"` などと書く（[記号](#記号)。同梱の `config.example.jsonc` に例がある）。

#### 言語を足し、言語サーバーを mise で入れる

既定の言語サーバーは Go の gopls だけである。
ほかの言語は、`extensions.settings.lsp.servers` にファイルの種類（`python`、`sh` など）ごとの言語サーバーを書き、`extensions.activation.lsp` にそのファイルの種類の起動の契機を足す。
言語サーバーの実行ファイルは、vi-imitation が取ってこない。
[mise](https://mise.jdx.dev/) で入れる前提で、言語サーバーごとに mise の道具の ID（`mise`）を書いておくと、見つからないときに入れ方を案内する。

```jsonc
{
  "extensions": {
    "settings": {
      "lsp": {
        "servers": {
          "python": {
            "command": "pyright-langserver",
            "args": ["--stdio"],
            "rootMarkers": ["pyproject.toml", "setup.py", ".git"],
            // 版は、自分の mise の設定（~/.config/mise/config.toml）に書いた版に合わせる
            "mise": "npm:pyright@1.1.410",
          },
        },
        // true にすると、見つからないときに mise に入れさせてから起動する（既定は false で、案内だけ）
        // "autoInstall": true,
      },
    },
    "activation": { "lsp": [{ "on": "filetype", "pattern": "python" }] },
  },
}
```

`mise` には固定した版を書く。`npm:pyright` のように版を省くか、`npm:pyright@latest` と書くと、設定の誤りになる（どちらも、自動で入れたときにその時点の最新版が入るため）。

`command` が PATH で見つかれば、`mise` と `autoInstall` に関わらず、そのまま起動する。
ただし、見つかったのが mise の shim で、`mise which <command>` が失敗する（版が決まっていない）ときは、見つからなかったものとして扱う。
見つからないときの動きは、`autoInstall` で選ぶ。

| `autoInstall` | 見つからないときの動き |
|---|---|
| `false`（既定） | メッセージの行に `言語サーバー pyright-langserver が見つからない。mise use -g npm:pyright@1.1.410 で入れられる` と出し、何も入れない。同じ言語サーバーの案内は、LSP の拡張機能が動いている間に 2 回出さない（拡張機能が異常終了から起動し直すと、もう一度出しうる）。案内のとおりに入れた後、そのファイルの種類の別のファイルを開くと、言語サーバーを起動する（本体を起動し直してもよい） |
| `true` | mise で道具を入れてから、`mise exec npm:pyright@1.1.410 -- pyright-langserver --stdio` で起動する。入れている間は、画面の右下（[拡張機能の進み具合](#拡張機能の進み具合)）に、表題 `mise` で `npm:pyright@1.1.410 を入れている` と mise の出力の行を出す。mise が PATH に無ければ、案内に戻る |

`autoInstall` は、`servers` の言語サーバーごとにも書け、そちらが優先する。
`autoInstall` で入れた道具は mise の設定ファイルに書かれない。
mise の shim のディレクトリを PATH に置いていると、その道具の shim は PATH で見つかるが、版が決まっていないので起動できない（shim は `No version is set for shim` で終わる）。
vi-imitation は上のとおり、そのような shim を使わずに `mise exec` で起動する。
入れるのに失敗したとき（ネットワークに出られない、プロジェクトの `mise.toml` を信頼していない、など）は、mise と、mise が起動した npm などの誤りの行をメッセージの行に出す。
信頼していない `mise.toml` で止まったときは、内容を確かめてから `mise trust` を打つ。vi-imitation は、利用者の代わりに信頼しない。

### 整形ツール

外部の整形ツール（biome、ruff、stylua、shfmt など）で、`:w` の前と、`gq{モーション}`、`:format` で整形する（nvim の conform.nvim に当たる）。
整形ツールは、`extensions.settings.formatter.formatters` に、コマンド、引数、使うファイルの種類を書いて足す。
既定では何も書いていないので、設定を書くまでは何もしない。
同梱の `config.example.jsonc` に、biome、taplo、ruff、stylua、shfmt、fish_indent、clang-format、rustfmt の例がある。

```jsonc
{
  "extensions": {
    // 保存の前の整形を待つ時間の上限のミリ秒（既定は 1000）。超えたら整形せずに書き出す
    "willSaveTimeoutMs": 2000,
    "settings": {
      "formatter": {
        "formatters": [
          { "command": "ruff", "args": ["format", "--force-exclude", "--stdin-filename", "{path}", "-"], "fileTypes": ["python"] },
          { "command": "shfmt", "args": ["-filename", "{path}"], "fileTypes": ["sh", "bash"] },
        ],
      },
    },
  },
}
```

| 項目 | 内容 |
|---|---|
| `formatters[].command` | 整形ツールの実行ファイルの名前か絶対パス（`~/` で始めてよい） |
| `formatters[].args` | 引数の列。引数の中の `{path}` は、ファイルの絶対パスになる |
| `formatters[].fileTypes` | この整形ツールを使うファイルの種類の名前（`:set filetype?` で出る名前）の列。ここに書いたファイルの種類のファイルを開くと、拡張機能を起動する |
| `formatters[].name` | 知らせに使う名前。省くと `command` のファイル名 |
| `formatOnSave` | `false` にすると、`:w` の前に整形しない（`gq` と `:format` は使える）。既定は `true` |
| `localBinDirs` | 実行ファイルを PATH より先に探す、プロジェクトの依存の置き場所の相対パスの列。既定は `["node_modules/.bin", ".venv/bin"]`。`venv` の仮想環境も使うなら `"venv/bin"` を足す。`[]` にするとプロジェクトの依存を探さない |
| `timeoutMs` | 1 つの整形ツールを待つ時間の上限のミリ秒。既定は `10000`。`:w` の前の整形は、`extensions.willSaveTimeoutMs` でも打ち切る |

整形ツールには、ファイルに書き出すときと同じ中身を標準入力で渡し、標準出力を整形の結果にする。
作業ディレクトリはファイルのディレクトリである（まだ無いディレクトリの新しいファイルでは、在る一番近い上のディレクトリ）。
2026-11-21 のレビューで、biome と ruff がこの形でプロジェクトの `biome.json` と `pyproject.toml` を読むことを確かめた。
実行ファイルの名前は、次の順に探す。

1. ファイルのディレクトリから上へたどった、各ディレクトリの `node_modules/.bin` と `.venv/bin`（`localBinDirs`）。Git の根（`.git` のあるディレクトリ。worktree の `.git` のファイルも含む）より上へはたどらない
2. 有効にした Python の仮想環境の `$VIRTUAL_ENV/bin`
3. PATH

プロジェクトの中の依存を、有効にした仮想環境より先に使う。

同じファイルの種類に整形ツールを 2 つ以上書くと、書いた順に走らせ、前の出力を次の入力にする。

| 操作 | 結果 |
|---|---|
| `:w` | 整形してから書き出す。変わった行だけを書き換えるので、変わっていない行のカーソルはその行に残る。整形ツールだけが整形するときは、`u` 1 回で整形の前に戻る（言語サーバーも整形するときは下の「LSP の整形との関係」） |
| `gqq`、`gqip` など | 整形ツールは範囲を受け取らないので、全体を整形し、範囲の行と重なる差分の塊だけを書き換える。離れた行の整形は書き換えない。コマンドラインの `:format` は全体を書き換える |
| 整形ツールが失敗した | 書き換えずに書き出し、メッセージの行に `整形ツール biome が失敗した（終了コード 1）: × Code formatting aborted due to parsing errors. …` のように、標準エラー出力の `error` を含む最初の行（無ければ最初の行）を出す |
| 整形ツールが見つからない | 書き換えずに書き出し、`整形ツール clang-format が見つからない（node_modules/.bin、.venv/bin、PATH を探した）` と出す。そのファイルの種類を保存するたびに出る |
| `willSaveTimeoutMs` を超えた | 待たずに書き換えずに書き出し、`拡張機能 formatter の整形が期限（1000 ms）を超えたので、整形を待たずに保存しました（期限は extensions.willSaveTimeoutMs）` と出す。括弧の中の値は設定した期限 |

2026-11-21 に、同梱の `config.example.jsonc` の設定で、biome 2.4.15、taplo 0.10.0、ruff 0.15.16、stylua 2.5.2、shfmt v3.12.0、fish_indent 4.9.3、rustfmt 1.9.0 を tmux の中で使い、`:w` で整形されることを確かめた。

#### 整形ツールの設定からのインデント

ファイルの種類に biome、prettier、ruff を書いたファイルでは、その整形ツールの設定ファイルからインデントの文字（タブかスペースか）と幅を読み、そのバッファの `indent.useSpaces`、`indent.width`、`tab.width` にする。
`.editorconfig` より勝ち、`:set` には負ける（上の「字下げとタブの幅」の「値の決まり方」の 6）。
ただし、`:set filetype=` などでファイルの種類が変わったときに新しい種類で読んだ値は、本体のファイルの種類ごとの既定と同じく、`:set` で変えた値にも勝つ（変わった後に `:set` で変えた項目を除く）。
読むのは、そのファイルの種類に書いた整形ツールの設定だけである（biome で整形する JavaScript のファイルでは、`.prettierrc` を読まない）。

どの整形ツールかは、`name`（省けば `command` のファイル名）、`command` のファイル名、`command` が `npx`、`pnpx`、`bunx`、`uvx`、`pnpm`、`yarn`、`bun`、`uv`、`pipx` のときは `args` の最初の整形ツールの名前（`npx prettier@3.6.2`、`pnpm dlx @biomejs/biome`、`uvx ruff`、`uv tool run ruff`）の順に見て決める。
これでも分からない起動の仕方（ラッパーのスクリプトなど）では、`"name": "prettier"` のように `name` に整形ツールの名前（`biome`、`prettier`、`ruff`）を書く。

| 整形ツール | 探す設定ファイル（1 つのディレクトリの中の順） | 読む項目 |
|---|---|---|
| biome | `biome.json`、`biome.jsonc`、`.biome.json`、`.biome.jsonc` | `formatter.indentStyle`（`"tab"` か `"space"`）、`formatter.indentWidth`。ファイルの種類の言語の `javascript.formatter`、`json.formatter`、`css.formatter`、`graphql.formatter`、`html.formatter` に書いた値がそれに勝つ |
| prettier | `package.json` の `"prettier"`、`.prettierrc`（JSON か YAML）、`.prettierrc.json`、`.prettierrc.yaml`、`.prettierrc.yml`、`.prettierrc.toml` | `useTabs`、`tabWidth` |
| ruff | `.ruff.toml`、`ruff.toml`、`pyproject.toml` の `[tool.ruff]` | `indent-width`、`[format]`（`pyproject.toml` では `[tool.ruff.format]`）の `indent-style`（`"space"` か `"tab"`） |

ファイルのディレクトリから上へ（Git の根を越えてファイルシステムの根まで）探し、最初に見つけた設定ファイルを使う（近い設定ファイルは上の設定ファイルを引き継がない）。
次の引き継ぎは、それぞれの整形ツールと同じく読む。

- biome の `"extends": "//"`: 上の根の設定（`"root": false` でない設定）に近い設定を重ねる
- biome の `"extends": ["./base.json"]`: 相対パスか絶対パスの設定ファイルを書いた順に当て、その上に自分の設定を重ねる
- ruff の `extend = "../pyproject.toml"`: その設定ファイルを先に当て、自分の設定を重ねる（`extend` の先の `extend` もたどる）
- biome と prettier の `overrides`: パターンが `*.js` か `**/*.js` の形なら、そのファイルに当たるものを順に重ねる（biome の `*.js` は設定ファイルのディレクトリの直下のファイルにだけ、prettier の `*.js` はどの深さのファイルにも当たる）。prettier の `*.{js,jsx}` のような中かっこは展開して当てる。当たらないものは読み飛ばす
- biome の重ね方: `extends` で重ねた設定は、トップの `formatter` と言語ごとの `javascript.formatter` などを分けて後のものが勝ち、重ねた後で言語ごとの値がトップの値より強い（根の `javascript.formatter` は、近い設定のトップの `formatter` に勝つ）。`overrides` は、その上に順に重ねる

YAML の形の `.prettierrc` は、`{ useTabs: true, tabWidth: 4 }` のような流れの形でもよい。値の前のアンカー（`tabWidth: &w 4`）も読む。
タブにして幅を書かなければ、`indent.width` を 0（`tab.width` と同じ幅）にする。幅は `indent.width` と `tab.width` の両方にする。
設定ファイルに書いていない項目は変えない（整形ツールの既定値は使わない）。

次の書き方は読めないので、整形ツールと違う字下げにならないように、そのファイルではインデントを変えず、メッセージの行に 1 回だけ知らせる。

| 整形ツール | 読めない書き方 |
|---|---|
| biome | `overrides` のパターンが `*.js` と `**/*.js` のほかの形（`src/**`、`!**/gen/*.js` など）で、字下げを変えているもの |
| biome | 共有の設定（パッケージの名前）の `extends`、`extends` の先の設定の `extends`、`extends` の先と根の設定の `overrides` で字下げを変えているもの |
| biome | 近い設定と根の設定の間に、ほかの `"root": false` の設定がある 3 段の入れ子 |
| prettier | JSON の形の `overrides` のパターン（`files` と `excludeFiles`）が、中かっこを展開した後で `*.md` と `**/*.md` のほかの形のもので、字下げを変えているもの |
| prettier | YAML と TOML の形の `overrides` で、字下げ（`useTabs`、`tabWidth`）を変えているもの |
| prettier | JavaScript と TypeScript の設定ファイル（`prettier.config.js` など）、`.prettierrc.json5`、`package.yaml`、共有の設定の名前（`"prettier": "@company/prettier-config"`） |
| ruff | 文字列でない `extend`、8 段より深い（輪になった）`extend` |

読むのは、ファイルを開いたとき、別名で保存したとき、`:set filetype=` でファイルの種類を変えたとき、`:w` で保存したときである。
`biome.json` などの設定ファイルをこのエディタで保存すると、開いているすべてのバッファで読み直す。
エディタの外で設定ファイルを変えたときは、そのファイルを `:w` で保存すると効く。
読めない設定ファイル（壊れた JSON、`"indentStyle": "tabs"` のような値、上の読めない書き方）は、メッセージの行に `整形ツールの設定 /path/biome.json を読めないので、インデントに使わない: …` と 1 回だけ出す。
その設定ファイルから前に読んだ値があれば、その値のまま変えない。ファイルの種類が変わって別の整形ツールの設定を読むようになったときは、前の整形ツールの値を外す。

#### LSP の整形との関係

LSP の拡張機能も、`gq`、`:format`、`:w` の前に整形する。
同じファイルの種類で両方を使うと、次のとおりになる。

- `gq` と `:format` は、`formatters` に書いたファイルの種類では整形ツールで、書いていないファイルの種類（Go など）では言語サーバーで整形する
- `:w` の前は、整形ツールで整形した後に、言語サーバーも整形する。2 つの整形は別の変更になるので、`u` 1 回では言語サーバーの整形だけが戻り、整形の前に戻すには `u` を 2 回打つ。整形ツールだけで整形するには、その言語サーバーの設定に `"formatOnSave": false` を書く（同梱の `config.example.jsonc` の Python の pyright がこの形である）

```jsonc
{
  "extensions": {
    "settings": {
      "lsp": { "servers": { "python": { "command": "pyright-langserver", "args": ["--stdio"], "formatOnSave": false } } },
    },
  },
}
```

nvim の conform.nvim と違う点は次のとおりである。

- 範囲を受け取る整形ツール（ruff の `--range`、clang-format の `--offset` など）にも範囲を渡さず、全体を整形して範囲と重なるところだけを書き換える（conform.nvim は `range_args` のある整形ツールに範囲を渡す）
- 作業ディレクトリは、いつもファイルのディレクトリである（conform.nvim は整形ツールごとに `biome.json` などのある上のディレクトリを探し、無ければ nvim の作業ディレクトリにする）
- rustfmt の `--edition` は設定に書いた値で、`Cargo.toml` から読まない

次の点は、nvim の conform.nvim と同じである。

- 末尾に改行の無いファイル（`fixendofline` が `false`）は、整形ツールが末尾に改行を付けても、末尾に改行の無いまま書き出す（出力の最後の空の行を除く）
- シンボリックリンクのファイルは、リンクを解かないパスで整形ツールに渡す（nvim のバッファの名前もリンクを解かない）。リンクの先のリポジトリの設定ファイル（`.stylua.toml` など）は、整形ツールが `{path}` から探す形では読まれないことがある

### DAP によるデバッグ

デバッグアダプターは、既定で Delve（`dlv dap`）を使う。
`dlv` は PATH から探す。
設定は要らない。

1. 止めたい行で `<leader>db` を打つ。行の左に `B` が出る。もう一度打つと消える。条件を付けるときは `:breakpoint.conditional i == 3` を打つ
2. `<leader>dc` を打つ。デバッグを始めたバッファのファイルのあるディレクトリのパッケージをビルドして実行し、ブレークポイントで止まる。止まった行の左に `=>` が出て、最下行に「停止した（breakpoint）」が出る
3. `<leader>dn`、`<leader>di`、`<leader>do` で 1 行ずつ進む。`=>` とカーソルが止まった位置へ移る
4. `<leader>dc` で続ける。デバッグ対象が終わると「デバッグを終了した」が出る
5. 途中でやめるときは `<leader>dt` を打つ。`<leader>dr` は終えてから始め直し、`<leader>dl` は前回と同じ設定で始める

デバッグ対象の標準出力（`fmt.Println` の出力など）と Delve の出力は、`dap` の欄に出る（`:output dap`）。
デバッグが失敗したときの理由も、この欄で読む。

デバッグアダプターや launch の引数を変える設定は、`extensions.settings.dap` に書く。
項目と例は `go doc -u ./cmd/vi-imitation-ext-dap settings` にある。

### ACP のエージェント

ACP（Agent Client Protocol）で話すコーディングエージェントを子プロセスとして起動し、プロンプトを送る。
エージェントには既定が無いので、`extensions.settings.acp.agent` に起動のコマンドを書く（[設定ファイル](#設定ファイル)の例）。
`agent` には `command`、`args`、`env` を書ける（`go doc -u ./cmd/vi-imitation-ext-acp settings`）。
書かずに `:acp.prompt` を打つと、「エージェントが設定されていない（設定の agent.command に書く）」と出る。
この `agent.command` は、設定ファイルの `extensions.settings.acp.agent.command` のことである。

| 操作 | 結果 |
|---|---|
| `:acp.prompt <プロンプト>` | プロンプトを送る。最初の 1 回でエージェントを起動する |
| `:acp.cancel` | 処理中のプロンプトを取り消す。`acp` の欄に `[cancelled]` が出る |

返答は `acp` の欄に書き足され、欄を開いていなければ画面に出ない。
欄を開いてから送るときは、`:output|acp.prompt notes.txt の2行目の内容をそのまま答えて` のように続けて打つ。
欄には、送ったプロンプト（`> ` で始まる行）、エージェントが使ったツールの要約（`[tool]` で始まる行）、返答、終わりの印（`[end_turn]`）が順に出る。

エージェントがツールの実行の許可を求めると、画面の下に問いと `[1] Allow once` のような選択肢が出る。
番号のキーで選び、`<Esc>` で取り消す。
選んだ答えは、最下行に「問いに「Allow once」と答えました」のように出る。
エージェントがツールを続けて呼ぶと、問いも続けて届く。
2 件目からは最下行の先頭に `問い 2:` のように何件目かが出て、前の問いへの答えが問いの上に出る。
問いを出している間は、番号と `<Esc>` 以外のキー（`:output` など）は捨てられ、捨てた数が問いの上に出る。
答え終わってから打ち直す。

### git の HEAD の版と見比べる

`<Leader>gd`（`:git.diffHead`）を打つと、カーソルのあるバッファのファイルの、git の HEAD の版を、いまのペインの右に開いた新しいペインの client のタブに出し、ファイルのタブと見比べる（[2 つのファイルを見比べる](#2-つのファイルを見比べる)）。
HEAD の版のタブの名前は `HEAD:<リポジトリの根からのパス>` で、編集を禁止したファイルでないバッファである。
`do` で HEAD の内容をファイルへ取り込める。HEAD の版への `dp` は E21 になる。
見比べるのは保存していない変更を含むバッファの中身で、ファイルを書き出さなくてよい。
HEAD の版は、作業ツリーへ取り出すときと同じ改行の変換（`.gitattributes` の `eol`、`core.autocrlf`）を通して読む。シンボリックリンクは、リンクの先のファイルの HEAD の版と見比べる。

- 見比べる相手は HEAD（最後のコミット）で、index（`git add` した内容）ではない。gitsigns の `:Gitsigns diffthis`（nvim でよく `<leader>gd` に割り当てる）は、既定では index と見比べるので、`git add` した後の結果が違う
- 右のペインの client が繋ぐのを待っている間（最長 30 秒）にもう一度打つと、待っていることを知らせて断る

- 端末多重化ソフトの外では見比べられない（メッセージの行にそのことが出る）
- git のリポジトリの外のファイル、HEAD に無いファイル（まだコミットしていないファイル）、ファイルでないバッファでは使えない
- 本体を `git commit` の `GIT_EDITOR` にして起動したときも、環境変数の `GIT_DIR` などは使わず、ファイルを含むリポジトリの HEAD を読む
- 行の印、blame などは持たない（要件定義 7 章の未決事項）

### ブランチとの差分があるファイルを見比べる

`<Leader>gV`（`:git.changes`）を打つと、既定のブランチとの merge-base と作業ツリーの間で違うファイルの一覧を、画面の中央のファインダーに出す（nvim の diffview.nvim の `:DiffviewOpen origin/main...HEAD` に当たる。MR の差分と同じ範囲に、まだコミットしていない変更を足したもの）。
`<Leader>gv`（`:git.changesHead`）は、HEAD との差分（作業ツリーの変更。`:DiffviewOpen HEAD` に当たる）の一覧を出す。

一覧の行は `<種類>  <パス>` で、種類は次のとおりである。
打った文字でパスを絞り込み（ファジーファインダーと同じく、語の文字が順に現れる行を残す）、`<C-n>`、`<C-p>` で動いて `<CR>` で選ぶ。
一覧はパスの順のままで、点数で並べ替えない。

| 種類 | 意味 | 選んだとき |
|---|---|---|
| `M` | 変えた（モードだけを変えたものを含む） | ファイルを左のペインのタブに開き、比べる元の版を右に開いたペインで見比べる |
| `A` | 足した（比べる元に無い） | ファイルを開き、空の版と見比べる |
| `?` | 追跡していない（`.gitignore` などで無視したものは出ない） | `A` と同じ |
| `R` | 名前を変えた（`<元> -> <先>`） | 先のファイルを開き、比べる元の元の名前の版と見比べる |
| `T` | ファイルの種類を変えた | `M` と同じ |
| `D` | 消した（作業ツリーに無い） | 比べる元の版だけを、左のペインのタブに開く |

比べる元の版のタブの名前は `<比べる元>:<パス>`（`origin/main...HEAD:cmd/foo.go` など）で、編集を禁止したファイルでないバッファである。
[HEAD の版と見比べる](#git-の-head-の版と見比べる)ときと同じく、`do` で比べる元の内容を取り込め、`dp` は E21 になる。
選んだ後のカーソルは、右のペイン（比べる元の版）にある。
そのまま続けて一覧を開き、次のファイルを選んでよい。前に開いた比べる元の版のタブとその見比べのペインは閉じ、次のファイルは左のペインに開いて、新しい比べる元の版が右に開く。ペインは左右の 2 つのままである。
左がファイル、右が比べる元の版の並びは、diffview.nvim（左が比べる元、右が作業ツリーで、カーソルは右）と逆である。

比べる元は `:git.changes` の引数で替えられる。

| 引数 | 比べる元 |
|---|---|
| 省く | `origin/HEAD`（clone したときの既定のブランチ）、`origin/main`、`origin/master`、`main`、`master` の最初に在るものと HEAD の merge-base |
| `<ブランチ>...` か `<ブランチ>...HEAD` | そのブランチと HEAD の merge-base（`:git.changes origin/develop...`） |
| `HEAD`、ブランチ、タグ、コミット | そのコミット（`:git.changes main` は main の先と比べるので、merge-base の後の main の変更も出る） |

右の側はいつも作業ツリーのファイルなので、`main..HEAD` のような 2 点の範囲と、`main...feature` のような右の側が HEAD でない範囲は使えない。
よく使う比べる元は、キーマップの `args` で割り当てる（[keymap から Ex コマンドを呼ぶ](#keymap-から-ex-コマンドを呼ぶ)）。

```jsonc
{
  "keymap": {
    "normal": {
      "<Leader>gm": { "command": "git.changes", "args": "origin/develop..." },
    },
  },
}
```

- 一覧を作るリポジトリは、カーソルのあるバッファのファイルを含む作業ツリーである。ファイルでないバッファと、ファイルが作業ツリーの外（`git commit` のメッセージの `COMMIT_EDITMSG`、`/tmp` のメモ）のときは、いまの client の作業ディレクトリを含む作業ツリーである。worktree の中ではその worktree のブランチの差分になる
- 本体を `git commit` の `GIT_EDITOR` にして起動したときも、環境変数の `GIT_DIR` などは使わない。コミットのメッセージを書きながら、コミットする変更の一覧を見られる
- 名前の変更は、git が索引で見つけたもの（`git mv` したもの）だけを `R` にする。`git mv` せずに名前を変えたファイルは `D` と `?` の 2 行になる
- モードだけを変えたファイル（`chmod +x`）は、行の末尾に `（モード 100644 -> 100755）` が出る。中身が同じなら、見比べても違いは出ない
- サブモジュール、追跡していない入れ子のリポジトリ（`?  nested/` のように末尾に `/` の付いた行）、シンボリックリンクは一覧に出るが、選んでも開かずに知らせる
- 端末多重化ソフトの外、git の作業ツリーの外、まだコミットの無いリポジトリ、比べる元が見つからないときは、一覧を開かずにメッセージの行に理由が出る。差分が無ければ「<比べる元> との差分はありません」と出る
- 浅いクローン（`git clone --depth`）で比べる元との共通の祖先が履歴に無いときは、そのことと `git fetch --unshallow` が出る
- 比べる元と索引（`git add` した内容）の比較（diffview.nvim の `--cached`）と、索引と作業ツリーの比較（引数なしの `:DiffviewOpen`）は持たない
- 消したファイルの版には、構文の色が付かない

### MCP のサーバー

MCP（Model Context Protocol）のクライアント（Claude Code などのコーディングエージェント）から、起動中のエディタのバッファを読み書きできる。
ツールの一覧は `go doc ./cmd/vi-imitation-ext-mcp` と基本設計 6.2 にある。

本体は、起動時から Unix ドメインソケットで MCP のクライアントの接続を待つ。
ソケットのパスは起動ごとに変わる。
`:mcp.start` を打つと、最下行にパスが出る。

```text
MCP のサーバーは /tmp/vi-imitation-2518129-2071416057/mcp.sock で接続を待っています
```

パスは `$XDG_RUNTIME_DIR/vi-imitation/` の下に作り、そこに作れないときは一時ディレクトリ（`TMPDIR` か `/tmp`）の下に作る（`internal/exthost` の `NewMCPSocket`）。
上の例は、`XDG_RUNTIME_DIR` が長くてソケットのパスの上限を超えたので、`/tmp` の下に作ったものである。

MCP のクライアントには、配布物の中継コマンド `vi-imitation-mcp-bridge` を、標準入出力で話す MCP のサーバーとして登録する。
中継コマンドの `--socket` に、`:mcp.start` が出したパスを渡す。
Claude Code では、次の JSON を `--mcp-config` に渡して確かめた。

```json
{
  "mcpServers": {
    "vi-imitation": {
      "type": "stdio",
      "command": "/path/to/vi-imitation-<コミット>-<OS>-<アーキテクチャ>/vi-imitation-mcp-bridge",
      "args": ["--socket", "/tmp/vi-imitation-2518129-2071416057/mcp.sock"]
    }
  }
}
```

```sh
echo 'vi-imitation の MCP ツールで notes.txt の 1 行目の先頭に "MCP: " を挿入して' |
  claude -p --mcp-config mcp.json \
    --allowedTools mcp__vi-imitation__list_buffers mcp__vi-imitation__edit_buffer
```

`--allowedTools` で、使わせる MCP のツールを許可しておく。
`--allowedTools` は後に続く引数をすべて値として取るので、プロンプトは引数でなく標準入力で渡す（引数で渡すと「Input must be provided either through stdin or as a prompt argument」で失敗した）。
Claude Code が `edit_buffer` のツールを呼ぶと、エディタの画面のバッファが変わる。
ディスクのファイルは変わらないので、エディタで `:w` を打って保存する。
ソケットのパスはエディタを起動し直すと変わるので、そのたびに `--socket` を書き換える。

### ファジーファインダー

画面の中央に、入力の行と一覧の窓を開き、打つたびに一覧を絞り込む（fzf と同じ使い方）。
キーは `<Leader>` が既定の `<Space>` のときのものである。

| キー | コマンド | 一覧 | 選んだとき |
|---|---|---|---|
| `<Space>ff` | `:finder.files` | 作業ディレクトリの下のファイル | 開く |
| `<Space>fg` | `:finder.grep` | 作業ディレクトリの下の `rg` の検索の結果。入力は `rg` の正規表現で、打つたびに検索し直す | その行と列へ移る |
| `<Space>f.` | `:finder.grepDir` | いまのファイルのディレクトリの下の検索の結果 | 同上 |
| `<Space>fw` | `:finder.grepWord` | カーソルの下の語を入れた検索の結果 | 同上 |
| `<Space>fb` | `:finder.buffers` | すべての client のタブ | そのタブへ移る（別の client のタブならそのペインへ） |
| `<Space>fl` | `:finder.lines` | いまのバッファの行 | その行へ移る |
| `<Space>fj` | `:finder.jumps` | ジャンプリスト（新しい順） | その位置へ移る |
| `<Space>fr` | `:finder.registers` | レジスターと履歴（`:registers` と同じ） | カーソルの後に貼る（`p` と同じ位置） |
| `<Space>fk` | `:finder.keys` | キーの一覧 | そのキーを打ったのと同じく実行する（[キーの一覧](#ヘルプとキーの一覧)の `<CR>` と同じ） |
| `<Space>fc` | `:finder.commands` | コマンド | `:名前<CR>` を打ったのと同じく実行する（引数の要るコマンドは誤りになる） |
| `<Space>fh` | `:finder.help` | `:help` で引ける語（キー、Ex コマンド、設定の名前、操作の ID、拡張機能の操作の ID）と、その説明の 1 行 | その語の `:help` を開く（[ヘルプとキーの一覧](#ヘルプとキーの一覧)） |
| `<Space>fp` | `:finder.resume` | 最後に開いた一覧を、最後の入力で開き直す | |

窓の中のキーは次のとおりである。
開いた時点でインサートモードになっている。

| キー | 動作 |
|---|---|
| 文字のキー、`<BS>` など | 入力の行を編集する |
| `<C-n>`、`<C-j>`、`<Down>` / `<C-p>`、`<C-k>`、`<Up>` | 一覧の次の項目 / 前の項目へ |
| `<CR>` | 一覧のカーソルの項目を選ぶ。打った入力への一覧がまだ届いていなければ、届いてから選ぶ |
| `<S-Down>` / `<S-Up>` | プレビューを 1 画面下 / 上へスクロールする（[項目のプレビュー](#項目のプレビュー)） |
| `<A-S-Down>` / `<A-S-Up>` | プレビューを 1 行下 / 上へスクロールする |
| `<Esc>` | 閉じる（インサートモードでも 1 回で閉じる） |

ファインダーで打ったキーはマクロに記録され、再生するとファインダーを開いて同じく打つ（`qa<Space>ffbeta<CR>q` を `@a` で再生すると、また `beta` で絞って開く）。

入力は空白で区切った語の並びで、すべての語を含む項目が残る。
語の文字は、項目の中に順に現れればよい（間に他の文字があってよい）。
語に大文字が無ければ、大文字と小文字を区別しない。
一致した文字には `finder.match` の色（既定は太字と下線）が付く。
見出しの「12/345」は、一致した数と候補の数である。
一覧に出すのは上位 300 件までである。
ファイル、検索の結果、ファイルのタブ、ファイルのジャンプの項目の前には、ファイルの種類のアイコンが付く（[アイコン](#アイコン)。`icons.show` で切れる）。
絞り込みはアイコンを除いた項目の文字で行う。

ファイルの一覧と検索は、隠しファイル（`.` で始まるもの）を含め、`.git` の下を除く。
`fd` と `rg` は `.gitignore` に書いたファイルを除くが、どちらも無いときは除かない。
設定は `extensions.settings` の `finder` に書く（どれも省いてよい）。

```jsonc
"finder": { "hidden": false, "limit": 1000 }
```

### ファイラ

作業ディレクトリの下のディレクトリとファイルを木で出し、選んだファイルを開く（nvim の neo-tree と同じ使い方）。
キーは `<Leader>` が既定の `<Space>` のときのものである。

| キー | コマンド | 出す場所 | ファイルを選んだとき |
|---|---|---|---|
| `<Space>e` | `:filer.popup` | 画面の中央のポップアップ | ポップアップを閉じて、元のタブでそのファイルを開く |
| `<Space>E` | `:filer.pane` | 端末多重化ソフトで右に分割したペイン（元のペインの幅の 30%） | ペインを残して、ペインを開いた client のタブでそのファイルを開き、フォーカスをその client のペインへ戻す |

どちらも、開いたときに、カーソルのあるファイルまで木を開いて、その行にカーソルを置く。
開いているときにもう一度打つと閉じる。
`<Space>E` のペインは、`:q` か端末多重化ソフトで閉じるまで残り、元の client で別のファイルへ移るたびに、そのファイルまで木を開いてカーソルを置く。
端末多重化ソフトの外では、`<Space>E` も画面の中央のポップアップに出し、ファイルを選んだら閉じる。

木の中のキーは次のとおりである。
ほかのノーマルモードのキー（`j`、`k`、`gg`、`/`、`n` など）もそのまま効くので、絞り込むときは `/` で探す。

| キー | 動作 |
|---|---|
| `l`、`<CR>` | ディレクトリなら開く（開いていれば閉じる）。ファイルなら選ぶ |
| `h` | 開いたディレクトリなら閉じる。それ以外なら、親のディレクトリを閉じてそこへ移る |
| `H` | gitignore されたものを見せるかを切り替える（見せている間は見出しの先頭に `[H]` の印が出る。見出しが窓の幅で切れても、この印は残る） |
| `<S-Down>`、`<S-Up>`、`<A-S-Down>`、`<A-S-Up>` | プレビューを 1 画面か 1 行スクロールする（[項目のプレビュー](#項目のプレビュー)） |
| `<Esc>` | 中央のポップアップを閉じる（ペインは閉じない） |

隠しファイル（`.` で始まるもの。`.git` を含む）は見せ、gitignore されたものは隠す。
gitignore の判定には PATH の `git` を使い、git のリポジトリの外と、`git` が無いときは、何も隠さない。
並びは、ディレクトリが先で、その中は名前の順（大文字が小文字より先）である。
ディレクトリには開閉の印（`▸`、`▾`）の後に開いたものと閉じたもののアイコンが、ファイルにはファイルの種類のアイコンが付く（[アイコン](#アイコン)。`icons.show` で切れる）。
アイコンも木の行の一部なので、`/` の検索の対象に入る。
ディレクトリの中身は開いたときに読むので、大きなディレクトリ（`node_modules` など）も開ける。
木を開いた後にファイラの外で作ったり消したりしたファイルは、そのディレクトリを閉じて開き直しても木に入らない（ファイラを開き直すと入る）。
ただし、`<Space>E` のペインで、元の client が木に無いファイルへ移ったときは、そのファイルのディレクトリを読み直す。
ファイルの作成、名前の変更、削除は、まだできない。

`<Space>e` の窓が十分に広ければ、右にカーソルの項目のプレビューを出す（[項目のプレビュー](#項目のプレビュー)）。
ファイルなら先頭からの中身を、ディレクトリなら中身の一覧を出す。
`<Space>E` のペインは横に狭いので、ふつうは出ない（出すかは窓の幅だけで決まる）。

### 項目のプレビュー

ファジーファインダーと `<Space>e` のファイラの窓が十分に広ければ、窓を広げて右に、カーソルのある項目のプレビューを出す（nvim の fzf-lua の `flex` の右の並びと同じ使い方）。

| 一覧 | プレビューに出すもの |
|---|---|
| `:finder.files`、ファイラのファイル | ファイルの先頭から |
| `:finder.grep`、`:finder.grepDir`、`:finder.grepWord`、`:finder.lines`、`:finder.jumps` | そのファイル（バッファ）の、その行を真ん中にした範囲。その行に `preview.line` の色が付く |
| ファイラのディレクトリ | 中身の一覧（ディレクトリが先で、名前の後に `/`）。gitignore されたものも出る |
| `:finder.keys`、`:finder.commands`、`:finder.registers`、`:finder.buffers`、`:finder.help` | 出さない。窓は今までどおり一覧だけ |

出すかは窓の幅だけで決まる。
窓の幅が `preview.minWindowWidth`（既定 100）列を超えるときだけ出し、窓の幅の `preview.widthPercent`（既定 60）% をプレビューに使う。
窓の幅は、ファインダーと `<Space>e` の中央のポップアップでは、画面の幅の `preview.windowWidthPercent`（既定 90）% である。
端末多重化ソフトの外で画面の中央に描くときも、tmux と herdr のポップアップに出すときも同じで、ポップアップに出すときは、その幅でポップアップを開く（200 列の画面なら 180 列の窓の 108 列がプレビュー）。
分割したペインでは、ペインの全体の幅である。
超えないときは出さず、窓の幅も今までと同じである（ポップアップは端末の 80% の大きさで開く。狭いときに縦に並べることはしない）。
tmux のポップアップでは、画面の幅は端末の幅で数えるので、window を分割した狭いペインから開いても、端末が広ければ出る。
herdr のポップアップでは、端末の幅を得られないので、開いたペインの幅で数える（狭いペインから開くと出ないことがある）。
3 つの値は設定の表の項目で、設定ファイルに書くか、`:option.set preview.minWindowWidth 80` のように変える（[設定の名前の一覧](#設定の名前の一覧)）。

```jsonc
"preview": { "minWindowWidth": 100, "widthPercent": 60, "windowWidthPercent": 90 }
```

プレビューの中身は、編集を禁止したバッファとして開き、ファイルの種類で構文の色（tree-sitter）が付く。
開いているファイルなら、ファイルでなくそのバッファの今の中身（保存していない変更を含む）を出す。
1 MiB を超えるファイル、読めないファイル、バイナリのファイル（先頭の 8000 バイトに NUL を含む）、FIFO などの通常のファイルでないものは、中身を出さずに理由を出す。
カーソルを続けて動かしている間は開かず、止まってから 100 ミリ秒の後に開く（開くたびに tree-sitter が解析するため）。
同じファイルの別の行へ移ったときは、読み直さずにその行へ移る。
プレビューに開いたバッファは、別の項目に移ったときと、窓を閉じたときに閉じる。

フォーカスを一覧（ファインダーの入力の行、ファイラの木）に置いたまま、次のキーでプレビューをスクロールできる（fzf-lua の既定のキーと同じ）。

| キー | 操作の ID | 動作 |
|---|---|---|
| `<S-Down>` | `preview.page-down` | 1 画面（プレビューの中身の行数から 2 を引いた行数）下へ |
| `<S-Up>` | `preview.page-up` | 1 画面上へ |
| `<A-S-Down>` | `preview.down` | 1 行下へ |
| `<A-S-Up>` | `preview.up` | 1 行上へ |

ファインダーではインサートモードのまま、ファイラではノーマルモードで打つ。入力の行、一覧のカーソル、フォーカスは変わらない。
ファイラの木では、`3<S-Down>` のように回数を付けるとその回数だけ動く。
下へは、最後の行がプレビューの最後の行に来たところで止まり、上へは最初の行で止まる。
別の項目へ移ると、その項目のプレビューを最初の位置（行を付けたものはその行が真ん中）から出し直す。
プレビューを出していない窓と、タブのバッファでは何もしない。
キーは、設定の `keymap` の `normal` と `insert` に操作の ID を書いて変えられる（`"insert": { "<C-f>": { "action": "preview.page-down" } }`。[操作の ID](#操作の-id)）。
`<A-S-Down>` は、Alt と Shift の付いた矢印のキーを区別して送る端末でだけ効く（tmux の中で、tmux が `M-S-Down` として送ったキーが届くことは 2026-11-20 に確かめた）。

プレビューを出している窓で `<C-w>w` を打つと、フォーカスがプレビューへ移り、ノーマルモードのキー（`j`、`k`、`<C-d>`、`/`、`n` など）で読める。
`<C-w>w` か `<Esc>` で一覧へ戻り、`:q` で窓を閉じる。
ファジーファインダーは開いた時点でインサートモードで、`<Esc>` で閉じるので、`<C-w>w` を打てない（インサートモードの `<C-w>` は前の語を消す）。
ファインダーでは、上のスクロールのキーで読む。ファイラの窓では `<C-w>w` を打てる。

### メモリの使用量

本体のプロセスの RSS と、本体が起動したプロセス（拡張機能と、拡張機能が起動した言語サーバー、デバッグアダプター、エージェントなど）の RSS の合計を、ステータス行に出す（nvim の lualine に自分で足す `nvim 56M · LSP 390M` の部品と同じ使い方）。
本体の既定のステータス行には出ないので、部品 `memusage.rss` を `statusline` か `tabline` に並べる。

```jsonc
{
  "statusline": { "right": ["memusage.rss", "filetype", "ruler", "position"] },
  "extensions": {
    // 更新の間隔のミリ秒（500 以上 3600000 以下。既定は 5000）
    "settings": { "memusage": { "intervalMs": 5000 } },
  },
}
```

部品は `vi 18M · ext 410M` の形で、前が本体、後ろが本体の起動したプロセスの合計である。
並べると、本体が最初の画面を描いた後に拡張機能を起動し、すぐに 1 回と、その後は間隔ごとに数え直す。

`:memusage.show` で、内訳を画面の中央のポップアップに出す（nvim の設定の `:MemUsage` に当たる）。
拡張機能ごとに、拡張機能のプロセスと、それが起動したプロセス（LSP の拡張機能なら言語サーバーごと）を字下げして並べ、PID、RSS、コマンドラインを出す。
`:!` で動かしているコマンドなど、拡張機能でないプロセスは、名前を括弧で囲んで出す。
キーに割り当てるには、`keymap` に `{ "command": "memusage.show" }` を書く（[keymap から Ex コマンドを呼ぶ](#keymap-から-ex-コマンドを呼ぶ)）。

RSS は共有ライブラリを重ねて数えるので、PSS より少し大きい。
この拡張機能は、開いているバッファの中身を受け取らない（宣言ファイルの `noBufferSync`）ので、大きなファイルを開いても、この拡張機能の RSS は増えない。
Linux では `/proc` から本体の子孫だけを読む。
Linux 以外（`/proc` から子のプロセスを辿れない環境）では、間隔ごとに `ps` を起動して数える。

## 拡張機能の取得（`:ext sync`）

git のリポジトリにある拡張機能を、設定ファイルに並べて取得できる（基本設計 5.6）。
取得したソースを `go build` でビルドするので、PATH に `go` と `git` が要る。

```jsonc
{
  "extensions": {
    "install": [
      // name は $XDG_DATA_HOME/vi-imitation/extensions/<name>/ のディレクトリの名前になる
      { "name": "surround", "repo": "https://github.com/example/vi-imitation-surround", "version": "v0.1.0" }
    ]
  }
}
```

`version` には、タグ、ブランチ、コミットのハッシュを書ける。
`repo` は URL か絶対パスにする。
取得元のリポジトリは、最上位に宣言ファイル `manifest.json` と `main` パッケージを置き、宣言ファイルの `command` を `./<実行ファイルの名前>` にする。

| コマンド | 働き |
|---|---|
| `:ext sync` | 設定ファイルを読み直し、並べた拡張機能を取得してビルドし、入れる。版を変えたものは入れ替え、並べなくなったものは削除する |
| `:ext update` | `:ext sync` と同じだが、ロックファイルの記録を使わずに版を解決し直す。ブランチの版を最新にするときに使う |

取得したコミットは、設定ファイルの隣の `extensions.lock.json` に記録する。
設定ファイルと一緒に dotfiles のリポジトリで管理すると、別のマシンで `:ext sync` を打ったときに同じコミットを取り出す。

取得の間も編集を続けられる。
進み具合はメッセージの行に出て、終わると件数が出る。
失敗の理由と警告は、画面の中央のポップアップに出る（`<Esc>` で閉じる）。

入れたものがいつから使えるかは、次のとおりである。

- 新しく入れた拡張機能は、本体を起動し直すと使える
- 版を変えて入れ替えた拡張機能は、動いていれば止まり、新しい版が起動し直す。開いているファイルの種類で起動する拡張機能（LSP、構文の色）はすぐに、それ以外は次にその拡張機能の操作を呼んだときに起動する
- 宣言ファイル（キー、コマンド、起動の契機）が変わった版に入れ替えたときは、本体を起動し直すまで前の版が動き続ける
- 削除した拡張機能は、動いていれば止まる
- 止めて入れ替えるのは、`:ext sync` を打った server の拡張機能だけである。他の tmux の window の server は、起動し直すまで前の版を使う

設定ファイルの `extensions.install` に誤りがあるとき（JSONC の構文の誤りを含む）は、その回の `:ext sync` は何も削除しない。
`extensions/<name>/` に手で置いたディレクトリは、上書きも削除もしない。

## スクリプトの拡張機能

Go のソースファイル 1 つを、コンパイルせずに拡張機能として動かせる。
配布物の `vi-imitation-ext-script` が、Go のインタープリター yaegi でスクリプトを実行する。

`$XDG_DATA_HOME/vi-imitation/extensions/<ID>/`（`XDG_DATA_HOME` が空なら `~/.local/share/vi-imitation/extensions/<ID>/`）に、宣言ファイルとスクリプトを置く。
次の例は、カーソルの行の前に `// hello` の行を入れるコマンド `hello.insert` である。

`extensions/hello/manifest.json`:

```json
{
  "id": "hello",
  "command": "/path/to/vi-imitation-<コミット>-<OS>-<アーキテクチャ>/vi-imitation-ext-script",
  "args": ["hello.go"],
  "activation": [{ "on": "command", "id": "hello.insert" }],
  "contributes": { "commands": [{ "id": "hello.insert", "description": "カーソルの行の前に // hello の行を入れる" }] }
}
```

`contributes` の各項目の `description` は、キーの一覧と `:help` に出る説明文である。省いてもよい。

`extensions/hello/hello.go`:

```go
package main

import (
	"context"
	"log"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

func main() {
	s := server.New()
	s.Command("hello.insert", insert)
	if err := s.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

// insert は、カーソルの行の前に "// hello" の行を入れる。
func insert(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
	pos := ext.Position{Line: p.Cursor.Line, Byte: 0}
	edit := ext.Edit{Range: ext.Range{Start: pos, End: pos}, Text: "// hello\n"}
	return ext.CommandExecuteResult{OK: true, Transaction: &ext.Transaction{Edits: []ext.Edit{edit}}}, nil
}
```

本体を起動し直して `:hello.insert` を打つと、カーソルの行の前に `// hello` が入る。

`command` には、配布物の `vi-imitation-ext-script` の絶対パスを書く。
配布物の場所を書かずに指す方法は未決である（基本設計 10 章）。
`args` の相対パスは、宣言ファイルのディレクトリから解決される。

### スクリプトから使えるもの

スクリプトが import できるのは、標準ライブラリのうち yaegi が記号の表を持つパッケージと、`github.com/piplupflipper/vi-imitation/ext` と `github.com/piplupflipper/vi-imitation/ext/server` だけである。
外部のモジュールは import できない。
`os.Getenv`、`os/exec`、`os.Exit`、`log.Fatal` は、コンパイルした Go と同じく動く。

ハンドラーは、`server.New` で作った `Server` に、`Command`、`Motion`、`TextObject`、`Operator`（とその `Async` の版）で操作の ID ごとに登録し、`Run` を呼ぶ。
拡張機能から本体へ送るもの（メッセージ、編集、ポップアップ、診断など）は `Server` のメソッド（`ShowMessage`、`Edit`、`OpenPopup`、`SetDiagnostics` など）で送る。
`ext/server` の関数とメソッドの一覧は `vi-imitation --docs script` に、使える標準ライブラリのパッケージと記号は `vi-imitation --docs script-symbols <import のパス>`（`vi-imitation --docs script-symbols strings`）にある。

### 色と仮想テキストを出す

範囲に色を付けるのは `SetDecorations`、本文に無い文字列（仮想テキスト）を出すのは `SetVirtualTexts` である（拡張機能 API の 0.19.0 から）。
どちらもバッファ全体の一覧を毎回送り、同じ拡張機能とバッファの前の一覧を置き換える。空の一覧で消える。
色は、色を付ける対象の名前（`Style`。「見た目の設定」の表の名前）と、直に付ける色（`Attr`。`ext.TextAttr` の `Fg`、`Bg`、`Sp` の `"#rrggbb"` と、`Bold` などの飾り）のどちらか、または両方で付ける。両方なら名前の色の上に直の色を重ねる。

仮想テキストの置き方（`Placement`）は次の 3 つで、Neovim の `nvim_buf_set_extmark` の `virt_text_pos` と同じ働きをする。

| 置き方 | 働き |
|---|---|
| `ext.VirtualTextInline` | 位置の文字の前に差し込み、後ろの本文を右へずらす（インレイヒント）。カーソルは仮想テキストの上に止まらず、`j` と `k` の列は仮想テキストの列も数える |
| `ext.VirtualTextEOL` | 行末の後ろに 1 列空けて出す（行末の注記）。画面の右の端で切る |
| `ext.VirtualTextOverlay` | 位置の文字の上に、本文をずらさずに重ねる（行頭の空白の上の罫線）。画面の右の端で切る |

仮想テキストの位置は、送った後の編集に合わせて本体がずらす（診断と同じ）。
次の例は、`:=` の前にインレイヒント `: int` を、`//` で始まる行の行末に注記を、4 つの空白で始まる行の先頭に罫線を出し、`#rrggbb` の文字の範囲をその色で塗るコマンド `vtdemo.show` である（宣言ファイルは「スクリプトの拡張機能」の例と同じ形で、`id` と `args` とコマンドの ID を替える）。

```go
s.Command("vtdemo.show", func(_ context.Context, b *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
	var vts []ext.VirtualText
	var decs []ext.Decoration
	hint := &ext.TextAttr{Fg: "#8b80a8", Italic: true}
	for i := 0; i < b.LineCount(); i++ {
		line := b.Line(i)
		if j := strings.Index(line, " :="); j >= 0 {
			vts = append(vts, ext.VirtualText{Position: ext.Position{Line: i, Byte: j}, Placement: ext.VirtualTextInline,
				Chunks: []ext.VirtualTextChunk{{Text: ": int", Attr: hint}}})
		}
		if strings.HasPrefix(line, "//") {
			vts = append(vts, ext.VirtualText{Position: ext.Position{Line: i, Byte: len(line)}, Placement: ext.VirtualTextEOL,
				Chunks: []ext.VirtualTextChunk{{Text: "eol note", Style: "comment"}}})
		}
		if strings.HasPrefix(line, "    ") {
			vts = append(vts, ext.VirtualText{Position: ext.Position{Line: i, Byte: 0}, Placement: ext.VirtualTextOverlay,
				Chunks: []ext.VirtualTextChunk{{Text: "│", Attr: &ext.TextAttr{Fg: "#ff8800"}}}})
		}
		for k := 0; k+7 <= len(line); k++ {
			if line[k] == '#' && ext.IsHexColor(line[k:k+7]) {
				decs = append(decs, ext.Decoration{Range: ext.Range{Start: ext.Position{Line: i, Byte: k}, End: ext.Position{Line: i, Byte: k + 7}},
					Attr: &ext.TextAttr{Bg: line[k : k+7]}})
			}
		}
	}
	if err := s.SetVirtualTexts(ext.VirtualTextSetParams{Buffer: b.ID(), Version: b.Version(), VirtualTexts: vts}); err != nil {
		return ext.CommandExecuteResult{}, err
	}
	if err := s.SetDecorations(ext.DecorationSetParams{Buffer: b.ID(), Version: b.Version(), Decorations: decs}); err != nil {
		return ext.CommandExecuteResult{}, err
	}
	return ext.CommandExecuteResult{OK: true}, nil
})
```

### yaegi の制限

同じソースは `go build` でも通るが、yaegi v0.16.3 では次の点が Go と違う（2026-11-16 に確かめた）。

- 組み込み関数の `max`、`min`、`clear` が無い（`undefined` の誤りになる）。`if a > b` で書く
- 標準ライブラリの、型の引数を取る関数（`slices.Sort`、`slices.Sorted`、`slices.Contains` など）は記号の表に無く、`undefined selector` の誤りになる。`sort.Strings`、`sort.Slice`、`for` の繰り返しで書く。使える記号は `vi-imitation --docs script-symbols <import のパス>` で確かめる
- ハンドラーに渡す関数リテラルの引数に名前を付けず、本体で変数を宣言すると、呼ばれたときに「reflect.Set: value of type context.Context is not assignable to type int」のような panic になる。使わない引数も `_` と名前を付ける（`func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams)`）

### スクリプトの出力と誤り

スクリプトの `os.Stdout` と `fmt.Print` などへの出力は、標準エラー出力へ書かれ、本体の記録（`--log`）に `extension stderr` の行として残る（[デバッグの案内](debugging.md)）。
`os.Stdin` は空で、読むとすぐ EOF になる。
本体との通信は `Run` が行うので、`Serve` に `os.Stdin` と `os.Stdout` を渡さない。

スクリプトを読み込めない（ファイルが無い、構文の誤り、`package main` でない、`func main` が無い、import できない、`max` のような使えない記号）ときは、理由を記録に書いて終わる。
構文の誤りは `パス:行:列: 内容` の形で書く。
本体はこれを拡張機能の異常として扱い、メッセージの行に「拡張機能 <ID> が異常終了しました」と出して起動し直し、失敗が続けば無効にする。
ハンドラーの中の panic は、そのリクエストの誤りの応答になり、スクリプトは動き続ける。
