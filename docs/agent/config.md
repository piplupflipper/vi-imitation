<!-- AI エージェント向けの文書（vi-imitation --docs config）。印の間は make docs が生成する。印の間を手で編集しない。 -->
# 設定ファイル

```jsonc
// $XDG_CONFIG_HOME/vi-imitation/config.jsonc（XDG_CONFIG_HOME が空なら ~/.config/vi-imitation/config.jsonc）。JSONC で書く
{
  // 設定の項目は、点で区切った名前を入れ子のキーにして書く（lineNumber.show）
  "lineNumber": { "show": true },
  "search": { "highlight": true, "showCount": true },
  "indent": { "useSpaces": true, "width": 4, "auto": true },
  "tab": { "width": 4 },
  // ファイルの種類ごとの値。キーは filetype の値で、中は最上位と同じ形で書く。範囲がバッファごとの項目だけを書ける
  "fileTypeOptions": {
    "go": { "indent": { "useSpaces": false, "width": 0 } },
  },
  // 拡張子かファイル名から、ファイルの種類への対応
  "fileTypes": { ".tmpl": "html" },
  "leader": "<Space>",
  "keymap": {
    "normal": {
      "<Leader>w": ":w<CR>",
      "<Leader>n": { "command": "option.toggle", "args": "lineNumber.show" },
    },
    "insert": {
      "<C-a>": { "action": "insert.line-start" },
    },
  },
  // 拡張機能の ID ごとの設定。中身は拡張機能ごとに違う
  "extensions": {
    "settings": {
      "acp": { "agent": { "command": "opencode", "args": ["acp"] } },
    },
  },
}
```

配布物の `config.example.jsonc` も例である。
`keymap` は `vi-imitation --docs keymap` に、設定の誤りの知らせ方は `vi-imitation --docs usage 拡張機能の設定と設定の誤り` にある。

## 最上位の項目

設定の項目（下の表）のほかに、最上位に次の項目を書ける。

<!-- generated:config-keys:begin -->
`keymap`、`leader`、`fixendofline`、`autoread`、`checkIntervalMs`、`fileTypes`、`extensions`、`registers`、`readonlyPaths`、`colors`、`colorscheme`、`symbols`、`icons`、`statusline`、`tabline`、`elevateCommand`、`commandAliases`、`fileTypeOptions`
<!-- generated:config-keys:end -->

| 知りたいこと | 読む節 |
|---|---|
| 各項目の値の形と既定値 | `vi-imitation --docs usage 設定ファイルの項目` |
| `extensions.settings` と設定の誤り | `vi-imitation --docs usage 拡張機能の設定と設定の誤り` |
| `colorscheme`、`colors`、`symbols`、`icons`、`statusline`、`tabline` | `vi-imitation --docs usage 配色の選択`、`配色`、`記号`、`アイコン`、`ステータス行の並び`、`タブの行` |
| `registers` | `vi-imitation --docs usage レジスターと履歴` |
| `readonlyPaths` | `vi-imitation --docs usage 編集の禁止` |
| `elevateCommand` | `vi-imitation --docs usage 権限の足りないファイルの保存` |
| `commandAliases` | `vi-imitation --docs usage Ex コマンドの別名を足す` |
| `fileTypeOptions` と値の決まり方 | `vi-imitation --docs usage 値の決まり方` |
| `extensions.install` | `vi-imitation --docs usage 拡張機能の取得（:ext sync）` |
| `extensions.disabled`（使わない拡張機能の ID の列） | `vi-imitation --docs usage 拡張機能を使わない` |

標準の拡張機能の `extensions.settings` は、拡張機能ごとの節（`vi-imitation --docs usage LSP`、`DAP によるデバッグ`、`ACP のエージェント`、`MCP のサーバー`、`tree-sitter による構文ハイライト`、`git の HEAD の版と見比べる`、`ファジーファインダー`、`ファイラ`、`整形ツール`、`メモリの使用量`）にある。
保存の前の整形を待つ期限は `extensions.willSaveTimeoutMs`（ミリ秒。既定は 1000）で、`vi-imitation --docs usage 整形ツール` にある。

## 設定の項目

実行中は、`:option.set 名前 値`、`:option.get 名前`、`:option.toggle 名前` で変える、読む（`vi-imitation --docs usage 実行中に設定を変える`）。

<!-- generated:options:begin -->
| 名前 | 型 | 範囲 | 既定値 | 取れる値と注意 |
|---|---|---|---|---|
| `filetype` | 文字列 | バッファごと | `""` | 設定ファイルに書けない（開いたファイルから判定する） |
| `lineNumber.show` | 真偽 | エディタ全体 | `false` |  |
| `lineNumber.minWidth` | 整数 | エディタ全体 | `4` | 1 から 20 |
| `lineWrap.enabled` | 真偽 | エディタ全体 | `false` |  |
| `whitespace.show` | 真偽 | エディタ全体 | `false` |  |
| `scroll.margin` | 整数 | エディタ全体 | `0` | 0 から 9999 |
| `scroll.sideMargin` | 整数 | エディタ全体 | `0` | 0 から 9999 |
| `search.highlight` | 真偽 | エディタ全体 | `false` |  |
| `search.incrementalTimeoutMs` | 整数 | エディタ全体 | `500` | 1 から 10000 |
| `search.showCount` | 真偽 | エディタ全体 | `false` |  |
| `yank.highlightMs` | 整数 | エディタ全体 | `0` | 0 から 10000 |
| `preview.minWindowWidth` | 整数 | エディタ全体 | `100` | 0 から 10000 |
| `preview.widthPercent` | 整数 | エディタ全体 | `60` | 1 から 99 |
| `preview.windowWidthPercent` | 整数 | エディタ全体 | `90` | 10 から 100 |
| `icons.show` | 真偽 | エディタ全体 | `true` |  |
| `indent.useSpaces` | 真偽 | バッファごと | `false` |  |
| `indent.width` | 整数 | バッファごと | `8` | 0 から 9999 |
| `tab.width` | 整数 | バッファごと | `8` | 1 から 9999 |
| `indent.auto` | 真偽 | バッファごと | `false` |  |
| `editorConfig.enabled` | 真偽 | エディタ全体 | `true` |  |
| `undo.persist` | 真偽 | エディタ全体 | `false` |  |
| `cursor.restore` | 真偽 | エディタ全体 | `false` |  |
| `comment.string` | 文字列 | バッファごと | `""` |  |
| `cursorHold.delayMs` | 整数 | エディタ全体 | `4000` | 0 から 60000 |
| `diagnostic.popup` | 真偽 | エディタ全体 | `false` |  |
| `progress.show` | 真偽 | エディタ全体 | `true` |  |
| `progress.doneMs` | 整数 | エディタ全体 | `3000` | 0 から 60000 |
| `mouse.enabled` | 真偽 | エディタ全体 | `false` |  |
| `mouse.scrollLines` | 整数 | エディタ全体 | `3` | 0 から 9999 |
| `mouse.scrollColumns` | 整数 | エディタ全体 | `6` | 0 から 9999 |
| `scrollbar.show` | 真偽 | エディタ全体 | `false` |  |
| `scrollbar.currentOnly` | 真偽 | エディタ全体 | `false` |  |
| `scrollbar.marks` | 文字列 | エディタ全体 | `"diagnostic,search"` |  |
| `scrollbar.excludedFileTypes` | 文字列 | エディタ全体 | `""` |  |
| `commandLine.completeMode` | 文字列 | エディタ全体 | `"full"` |  |
| `commandLine.completeIgnoreCase` | 真偽 | エディタ全体 | `false` |  |
| `commandLine.popup` | 真偽 | エディタ全体 | `false` |  |
| `completionMenu.height` | 整数 | エディタ全体 | `0` | 0 から 9999 |
| `intro.show` | 真偽 | エディタ全体 | `true` |  |
<!-- generated:options:end -->

各項目の説明と、`:set` で使える Vim の名前は `vi-imitation --docs usage 設定の名前の一覧` にある。
