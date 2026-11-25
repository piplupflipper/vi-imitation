<!-- AI エージェント向けの文書（vi-imitation --docs keymap）。印の間は make docs が生成する。印の間を手で編集しない。 -->
# キーの割り当て

設定ファイル（`vi-imitation --docs config`）の `keymap` に、モードの名前ごとに、左辺のキーの列から右辺への対応を書く。

<!-- generated:keymap-modes:begin -->
モードの名前は `normal`、`visual`、`insert`、`commandLine` である。
<!-- generated:keymap-modes:end -->

```jsonc
{
  "keymap": {
    "normal": {
      // キーの列に展開する（Vim の nnoremap）
      "<Leader>w": ":w<CR>",
      // 本体の操作を ID で割り当てる（下の表の ID）
      "Y": { "action": "yank-line" },
      // Ex コマンドを、コマンドラインを出さずに実行する。args は省ける
      "<C-l>": { "command": "search.clearHighlight" },
      "<Leader>n": { "command": "option.toggle", "args": "lineNumber.show" },
    },
    "insert": {
      "jk": "<Esc>",
      "<C-a>": { "action": "insert.line-start" },
    },
  },
}
```

書き方の説明は、次の節にある。

| 知りたいこと | 読む節 |
|---|---|
| 右辺の 3 つの形（キーの列、`action`、`command`）と展開の規則 | `vi-imitation --docs usage keymap の書き方` |
| `action` の左辺とモードの規則、Ctrl-H、誤りの扱い | `vi-imitation --docs usage 操作の ID` |
| `command` に書ける名前、`args`、モードごとの動き | `vi-imitation --docs usage keymap から Ex コマンドを呼ぶ` |
| `<Leader>` と `leader` | `vi-imitation --docs usage <Leader> と leader` |
| ID の名前の付け方 | `vi-imitation --docs usage ID の書き方` |
| インサートモードの操作（`insert.` で始まる ID）の補足 | `vi-imitation --docs usage インサートモードの操作` |
| `select-all`（既定の `<Leader>a`）の補足 | `vi-imitation --docs usage select-all` |

`command` に書ける Ex コマンドの名前の一覧は `vi-imitation --docs commands` にある。

## キーの表記

<!-- generated:key-names:begin -->
`<` と `>` で囲む名前: `<Esc>`、`<CR>`、`<Tab>`、`<BS>`、`<Del>`、`<Up>`、`<Down>`、`<Left>`、`<Right>`、`<Home>`、`<End>`、`<PageUp>`、`<PageDown>`、`<Insert>`、`<F1>`〜`<F20>`、`<Space>`、`<lt>`、`<Bar>`、`<Bslash>`（`<lt>` は `<` そのもの）。
修飾キー: `C-`（Ctrl）、`A-` か `M-`（Alt）、`S-`（Shift）を名前の前に付ける（`<C-w>`、`<A-x>`、`<S-Tab>`）。名前と修飾キーの大文字と小文字は区別しない。
マウスの出来事（設定の `mouse.enabled` が真の間だけ届く）: `<LeftMouse>`、`<LeftDrag>`、`<LeftRelease>`、`<MiddleMouse>`、`<MiddleDrag>`、`<MiddleRelease>`、`<RightMouse>`、`<RightDrag>`、`<RightRelease>`、`<ScrollWheelUp>`、`<ScrollWheelDown>`、`<ScrollWheelLeft>`、`<ScrollWheelRight>`。続けて押したボタンの 2 回目から 4 回目は、名前の前に `2-` から `4-` を付けて書く（`<2-LeftMouse>`）。
<!-- generated:key-names:end -->

## 操作の ID の一覧

「既定のキー」は、本体の既定のキーマップでその操作に割り当てたキーである。
キーの後の括弧は、そのキーが効くモードが「モード」の列の一部だけであることを表す（`lowercase` の `u` は `visual` だけで、`normal` の `u` は `undo`）。
「オペレーターの後」は、オペレーターに続けて打ったとき（`d` の後など）だけ効くことを表す。
`<Leader>` で始まるキーは、`leader` を変えると、そのキーで始まる。
LSP と DAP の操作（`hover`、`debug.continue` など）は、その操作を引き受ける拡張機能（LSP、DAP）を読み込んだときだけ働く。

<!-- generated:actions:begin -->
| ID | 種類 | モード | 既定のキー | 説明 |
|---|---|---|---|---|
| `left` | モーション | normal、visual | `h` `<Left>` | 左へ |
| `down` | モーション | normal、visual | `j` `<Down>` | 下の行へ |
| `up` | モーション | normal、visual | `k` `<Up>` | 上の行へ |
| `right` | モーション | normal、visual | `l` `<Right>` | 右へ |
| `display-down` | モーション | normal、visual | `gj` | 見た目の行で 1 行下へ（行を折り返さないときは j と同じ） |
| `display-up` | モーション | normal、visual | `gk` | 見た目の行で 1 行上へ（行を折り返さないときは k と同じ） |
| `word-next` | モーション | normal、visual | `w` | 次の単語の先頭へ |
| `word-prev` | モーション | normal、visual | `b` | 前の単語の先頭へ |
| `word-end` | モーション | normal、visual | `e` | 単語の末尾へ |
| `bigword-next` | モーション | normal、visual | `W` | 次の空白区切りの単語の先頭へ |
| `bigword-prev` | モーション | normal、visual | `B` | 前の空白区切りの単語の先頭へ |
| `bigword-end` | モーション | normal、visual | `E` | 空白区切りの単語の末尾へ |
| `line-start` | モーション | normal、visual | `0` `<Home>` | 行の先頭へ |
| `line-first-nonblank` | モーション | normal、visual | `^` | 行の最初の空白でない文字へ |
| `line-end` | モーション | normal、visual | `$` `<End>` | 行の末尾へ |
| `file-start` | モーション | normal、visual | `gg` | 最初の行へ（回数があればその行へ） |
| `file-end` | モーション | normal、visual | `G` | 最後の行へ（回数があればその行へ） |
| `find-forward` | モーション | normal、visual | `f` | 行の中で右の {文字} へ |
| `find-backward` | モーション | normal、visual | `F` | 行の中で左の {文字} へ |
| `till-forward` | モーション | normal、visual | `t` | 行の中で右の {文字} の手前へ |
| `till-backward` | モーション | normal、visual | `T` | 行の中で左の {文字} の手前へ |
| `find-repeat` | モーション | normal、visual | `;` | 直前の f、F、t、T を繰り返す |
| `find-repeat-reverse` | モーション | normal、visual | `,` | 直前の f、F、t、T を逆向きに繰り返す |
| `match-pair` | モーション | normal、visual | `%` | 対応する括弧へ |
| `paragraph-prev` | モーション | normal、visual | `{` | 前の段落へ |
| `paragraph-next` | モーション | normal、visual | `}` | 次の段落へ |
| `search-forward` | モーション | normal、visual | `/` | 前方へ検索する |
| `search-backward` | モーション | normal、visual | `?` | 後方へ検索する |
| `search-next` | モーション | normal、visual | `n` | 直前の検索を同じ向きに繰り返す |
| `search-prev` | モーション | normal、visual | `N` | 直前の検索を逆向きに繰り返す |
| `delete` | オペレーター | normal、visual | `d` `x`（visual） | 消す |
| `change` | オペレーター | normal、visual | `c` `s`（visual） | 消して入力する |
| `yank` | オペレーター | normal、visual | `y` | ヤンクする |
| `toggle-case` | オペレーター | normal、visual | `g~` `~`（visual） | 大文字と小文字を入れ替える |
| `lowercase` | オペレーター | normal、visual | `gu` `u`（visual） | 小文字にする |
| `uppercase` | オペレーター | normal、visual | `gU` `U`（visual） | 大文字にする |
| `comment` | オペレーター | normal、visual | `gc` `<C-_>`（visual） `<C-/>`（visual） | 行のコメントを切り替える |
| `surround-add` | オペレーター | normal | `sa` | 対象を {文字} で囲む（sa{対象}{文字}） |
| `indent` | オペレーター | visual | `>` | 選んだ行を右へずらす（選択は残す） |
| `dedent` | オペレーター | visual | `<lt>` | 選んだ行を左へずらす（選択は残す） |
| `delete-lines` | オペレーター | visual | `D` | 選んだ行を消す |
| `change-lines` | オペレーター | visual | `C` `S` | 選んだ行を消して入力する |
| `yank-lines` | オペレーター | visual | `Y` | 選んだ行をヤンクする |
| `inner-word` | テキストオブジェクト | visual | `iw` | 単語 |
| `a-word` | テキストオブジェクト | visual | `aw` | 単語と後ろの空白 |
| `inner-bigword` | テキストオブジェクト | visual | `iW` | 空白区切りの単語 |
| `a-bigword` | テキストオブジェクト | visual | `aW` | 空白区切りの単語と後ろの空白 |
| `inner-double-quote` | テキストオブジェクト | visual | `i"` | "…" の中 |
| `a-double-quote` | テキストオブジェクト | visual | `a"` | "…" の全体 |
| `inner-single-quote` | テキストオブジェクト | visual | `i'` | '…' の中 |
| `a-single-quote` | テキストオブジェクト | visual | `a'` | '…' の全体 |
| `inner-paren` | テキストオブジェクト | visual | `i(` `i)` `ib` | (…) の中 |
| `a-paren` | テキストオブジェクト | visual | `a(` `a)` `ab` | (…) の全体 |
| `inner-bracket` | テキストオブジェクト | visual | `i[` `i]` | […] の中 |
| `a-bracket` | テキストオブジェクト | visual | `a[` `a]` | […] の全体 |
| `inner-brace` | テキストオブジェクト | visual | `i{` `i}` `iB` | {…} の中 |
| `a-brace` | テキストオブジェクト | visual | `a{` `a}` `aB` | {…} の全体 |
| `inner-paragraph` | テキストオブジェクト | visual | `ip` | 段落 |
| `a-paragraph` | テキストオブジェクト | visual | `ap` | 段落と後ろの空行 |
| `commented-lines` | テキストオブジェクト | visual | `gc`（オペレーターの後） | 続くコメントの行 |
| `toggle-case-char` | 操作 | normal | `~` | カーソルの文字の大文字と小文字を入れ替える |
| `put-after` | 操作 | normal | `p` | カーソルの後に貼る |
| `put-before` | 操作 | normal | `P` | カーソルの前に貼る |
| `delete-char` | 操作 | normal | `x` | カーソルの文字を消す |
| `delete-char-before` | 操作 | normal | `X` | カーソルの前の文字を消す |
| `replace-char` | 操作 | normal | `r` | カーソルの文字を {文字} に置き換える |
| `delete-to-line-end` | 操作 | normal | `D` | 行の末尾まで消す（d$） |
| `change-to-line-end` | 操作 | normal | `C` | 行の末尾まで消して入力する（c$） |
| `substitute-line` | 操作 | normal | `S` | 行を消して入力する（cc） |
| `yank-to-line-end` | 操作 | normal | `Y` | 行の末尾までヤンクする（y$） |
| `yank-line` | 操作 | normal |  | 行をヤンクする（yy） |
| `indent-line` | 操作 | normal | `>` | 行を右へずらす（>>。回数は行数） |
| `dedent-line` | 操作 | normal | `<lt>` | 行を左へずらす（<<。回数は行数） |
| `surround-delete` | 操作 | normal | `sd` | {文字} の囲みを消す（sd{文字}） |
| `surround-replace` | 操作 | normal | `sr` | 囲みを替える（sr{前の文字}{後の文字}） |
| `comment-line` | 操作 | normal | `gcc` `<C-_>` `<C-/>` | 行のコメントを切り替える（gcc。回数は行数） |
| `join-lines` | 操作 | normal | `J` | 行をつなげる |
| `open-below` | 操作 | normal | `o` | 下に行を足して入力する |
| `open-above` | 操作 | normal | `O` | 上に行を足して入力する |
| `insert` | 操作 | normal | `i` | カーソルの前から入力する |
| `append` | 操作 | normal | `a` | カーソルの後から入力する |
| `insert-line-start` | 操作 | normal、visual | `I` | 行の最初の空白でない文字の前から入力する（ビジュアルモードでは選んだ行ごと） |
| `append-line-end` | 操作 | normal、visual | `A` | 行の末尾から入力する（ビジュアルモードでは選んだ行ごと） |
| `undo` | 操作 | normal | `u` | アンドゥ |
| `redo` | 操作 | normal | `U` `<C-r>` | リドゥ |
| `command-line` | 操作 | normal、visual | `:` | コマンドラインに入る |
| `visual` | 操作 | normal、visual | `v` | 文字単位のビジュアルモード |
| `visual-line` | 操作 | normal、visual | `V` | 行単位のビジュアルモード |
| `visual-swap` | 操作 | visual | `o` | 選択の反対の端へ移る |
| `select-all` | 操作 | normal、visual | `<Leader>a` | カーソルを動かさずにバッファの全体を選ぶ |
| `page-down` | 操作 | normal、visual | `<PageDown>` `<C-f>` | 1 画面下へスクロールする |
| `page-up` | 操作 | normal、visual | `<PageUp>` `<C-b>` | 1 画面上へスクロールする |
| `half-page-down` | 操作 | normal、visual | `<C-d>` | 半画面（回数があれば回数の行）下へスクロールし、カーソルも同じ行数動かす |
| `half-page-up` | 操作 | normal、visual | `<C-u>` | 半画面（回数があれば回数の行）上へスクロールし、カーソルも同じ行数動かす |
| `scroll-line-down` | 操作 | normal、visual | `<C-e>` | 表示領域を 1 行（回数の行）下へスクロールする |
| `scroll-line-up` | 操作 | normal、visual | `<C-y>` | 表示領域を 1 行（回数の行）上へスクロールする |
| `scroll-cursor-top` | 操作 | normal、visual | `zt` | カーソルの行（回数があればその行）を画面の上の端に置く |
| `scroll-cursor-top-first-nonblank` | 操作 | normal、visual | `z<CR>` | zt の後に、行の最初の空白でない文字へ移る |
| `scroll-cursor-center` | 操作 | normal、visual | `zz` | カーソルの行（回数があればその行）を画面の中ほどに置く |
| `scroll-cursor-center-first-nonblank` | 操作 | normal、visual | `z.` | zz の後に、行の最初の空白でない文字へ移る |
| `scroll-cursor-bottom` | 操作 | normal、visual | `zb` | カーソルの行（回数があればその行）を画面の下の端に置く |
| `scroll-cursor-bottom-first-nonblank` | 操作 | normal、visual | `z-` | zb の後に、行の最初の空白でない文字へ移る |
| `output-next` | 操作 | normal | `<C-w>P` | 出力の欄を開く、次の欄へ切り替える |
| `output-close` | 操作 | normal | `<C-w>z` | 出力の欄を閉じる |
| `output-scroll-up` | 操作 | normal | `<C-w><PageUp>` | 出力の欄を上へスクロールする |
| `output-scroll-down` | 操作 | normal | `<C-w><PageDown>` | 出力の欄を下へスクロールする |
| `popup-focus` | 操作 | normal | `<C-w>w` | ポップアップへフォーカスを入れる、元のタブへ戻る |
| `jump-older` | 操作 | normal | `<C-o>` | ジャンプリストを戻る |
| `jump-newer` | 操作 | normal | `<C-i>` | ジャンプリストを進む |
| `alternate-buffer` | 操作 | normal | `<C-^>` | 裏のタブへ移る |
| `tab-next` | 操作 | normal | `<Tab>` | 次のタブへ移る |
| `tab-prev` | 操作 | normal | `<S-Tab>` | 前のタブへ移る |
| `put-older` | 操作 | normal | `<C-p>` | 貼った直後に、履歴の古い項目に貼り直す |
| `put-newer` | 操作 | normal | `<C-n>` | 貼った直後に、履歴の新しい項目に貼り直す |
| `diff-next` | 操作 | normal | `]c` | 見比べで、次の差分へ |
| `diff-prev` | 操作 | normal | `[c` | 見比べで、前の差分へ |
| `search-word-forward` | 操作 | normal、visual | `*` | カーソルの単語を前方への検索のパターンにする（移らない） |
| `search-word-backward` | 操作 | normal、visual | `#` | カーソルの単語を後方への検索のパターンにする（移らない） |
| `mouse.click` | 操作 | normal、visual、insert | `<LeftMouse>` `<4-LeftMouse>` | マウスの位置へ移る（タブの行ならそのタブへ、スクロールバーの印ならその行へ移り、つまみならドラッグを始める） |
| `mouse.drag` | 操作 | normal、visual、insert | `<LeftDrag>` | ボタンを押した位置からマウスの位置までを選ぶ（スクロールバーのつまみなら表示領域を動かす） |
| `mouse.release` | 操作 | normal、visual、insert | `<LeftRelease>` | ドラッグを終える（つまみの上の印を動かさずに離したら、その行へ移る） |
| `mouse.select-word` | 操作 | normal、visual、insert | `<2-LeftMouse>` | マウスの位置の単語を選ぶ |
| `mouse.select-line` | 操作 | normal、visual、insert | `<3-LeftMouse>` | マウスの位置の行を選ぶ |
| `mouse.scroll-down` | 操作 | normal、visual、insert | `<ScrollWheelDown>` | 表示領域を mouse.scrollLines の行だけ下へ送る |
| `mouse.scroll-up` | 操作 | normal、visual、insert | `<ScrollWheelUp>` | 表示領域を mouse.scrollLines の行だけ上へ送る |
| `mouse.scroll-right` | 操作 | normal、visual、insert | `<ScrollWheelRight>` | 行を折り返さないとき、表示領域を mouse.scrollColumns の列だけ右へ送る |
| `mouse.scroll-left` | 操作 | normal、visual、insert | `<ScrollWheelLeft>` | 行を折り返さないとき、表示領域を mouse.scrollColumns の列だけ左へ送る |
| `insert.left` | 操作 | insert | `<Left>` | 左へ |
| `insert.right` | 操作 | insert | `<Right>` | 右へ |
| `insert.up` | 操作 | insert | `<Up>` | 上の行へ |
| `insert.down` | 操作 | insert | `<Down>` | 下の行へ |
| `insert.line-start` | 操作 | insert | `<Home>` | 行の先頭へ |
| `insert.line-end` | 操作 | insert | `<End>` | 行末の位置へ |
| `insert.page-up` | 操作 | insert | `<PageUp>` | 1 画面上へスクロールする |
| `insert.page-down` | 操作 | insert | `<PageDown>` | 1 画面下へスクロールする |
| `insert.delete-prev-char` | 操作 | insert | `<BS>` | 前の文字を消す |
| `insert.delete-char` | 操作 | insert | `<Del>` | カーソルの文字を消す |
| `insert.delete-to-line-end` | 操作 | insert |  | カーソルから行末まで消す（行末では次の行をつなげる） |
| `insert.delete-prev-word` | 操作 | insert |  | 前の単語を消す（行の先頭では前の行とつなげる） |
| `insert.delete-to-line-start` | 操作 | insert |  | カーソルから行の先頭まで消す |
| `hover` | 操作 | normal | `<Leader>lh` `K` | ホバー（LSP） |
| `peek.definition` | 操作 | normal | `<Leader>ld` | 定義をポップアップで見る（LSP） |
| `definition` | 操作 | normal | `<Leader>lD` `<C-]>` | 定義へ移る（LSP） |
| `peek.implementation` | 操作 | normal | `<Leader>li` | 実装をポップアップで見る（LSP） |
| `implementation` | 操作 | normal | `<Leader>lI` | 実装へ移る（LSP） |
| `peek.typeDefinition` | 操作 | normal | `<Leader>lt` | 型の定義をポップアップで見る（LSP） |
| `typeDefinition` | 操作 | normal | `<Leader>lT` | 型の定義へ移る（LSP） |
| `references` | 操作 | normal | `<Leader>lr` | 参照の一覧（LSP） |
| `rename` | 操作 | normal | `<Leader>ln` | 名前を変える（LSP） |
| `codeAction` | 操作 | normal | `<Leader>la` | コードアクション（LSP） |
| `documentSymbols` | 操作 | normal | `<Leader>ls` | ファイルのシンボルの一覧（LSP） |
| `workspaceSymbols` | 操作 | normal | `<Leader>lS` | ワークスペースのシンボルの一覧（LSP） |
| `diagnostic.list` | 操作 | normal | `<Leader>le` | ファイルの診断の一覧 |
| `diagnostic.listAll` | 操作 | normal | `<Leader>lE` | ワークスペースの診断の一覧 |
| `diagnostic.next` | 操作 | normal | `<Leader>lj` | 次の診断へ移る |
| `diagnostic.prev` | 操作 | normal | `<Leader>lk` | 前の診断へ移る |
| `format` | オペレーター | normal、visual | `gq` | 整形する（LSP） |
| `debug.continue` | 操作 | normal | `<Leader>dc` | デバッグを始める、続行する（DAP） |
| `debug.pause` | 操作 | normal | `<Leader>dp` | 一時停止する（DAP） |
| `debug.terminate` | 操作 | normal | `<Leader>dt` | デバッグを終える（DAP） |
| `debug.restart` | 操作 | normal | `<Leader>dr` | デバッグをやり直す（DAP） |
| `breakpoint.toggle` | 操作 | normal | `<Leader>db` | ブレークポイントを切り替える（DAP） |
| `breakpoint.conditional` | 操作 | normal | `<Leader>dB` | 条件付きのブレークポイントを置く（DAP） |
| `breakpoint.clear` | 操作 | normal | `<Leader>dX` | ブレークポイントをすべて消す（DAP） |
| `debug.stepOver` | 操作 | normal | `<Leader>dn` | ステップオーバー（DAP） |
| `debug.stepIn` | 操作 | normal | `<Leader>di` | ステップイン（DAP） |
| `debug.stepOut` | 操作 | normal | `<Leader>do` | ステップアウト（DAP） |
| `debug.runLast` | 操作 | normal | `<Leader>dl` | 前回の設定で実行する（DAP） |
| `debug.toggleUI` | 操作 | normal | `<Leader>du` | DAP のペインを切り替える（DAP） |
| `preview.page-down` | 操作 | normal、insert | `<S-Down>` | プレビューを 1 画面下へスクロールする |
| `preview.page-up` | 操作 | normal、insert | `<S-Up>` | プレビューを 1 画面上へスクロールする |
| `preview.down` | 操作 | normal、insert | `<A-S-Down>` | プレビューを 1 行下へスクロールする |
| `preview.up` | 操作 | normal、insert | `<A-S-Up>` | プレビューを 1 行上へスクロールする |
| `view.child-next` | 操作 | normal | `]v` | 切り替えの部品の次の子へ切り替える |
| `view.child-prev` | 操作 | normal | `[v` | 切り替えの部品の前の子へ切り替える |
| `view.child-first` | 操作 | normal | `[V` | 切り替えの部品の最初の子へ切り替える |
| `view.child-last` | 操作 | normal | `]V` | 切り替えの部品の最後の子へ切り替える |
<!-- generated:actions:end -->
