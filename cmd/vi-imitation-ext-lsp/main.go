// Command vi-imitation-ext-lsp は、言語サーバーを子プロセスとして起動し、LSP で話す拡張機能である（基本設計 6.3）。
//
// 本体（internal/exthost）が、言語サーバーのあるファイルの種類のファイルを開いたときに起動する（宣言ファイル manifest.json の filetype の契機）。
// 標準入出力で本体と JSON-RPC のメッセージをやり取りするので、ログは標準エラー出力へ書く。
// 言語サーバーの標準エラー出力も、そのまま標準エラー出力へ流す。
//
// # 言語サーバーの設定
//
// どのファイルの種類にどの言語サーバーを使うかは、利用者の設定のうちこの拡張機能に向けた部分（initialize の settings）に書く。
// 形は settings 型にある。既定では Go（ファイルの種類 "go"）に gopls を使う。
// 1 つのプロセスで、ファイルの種類ごとに言語サーバーを 1 つ起動する（client 型）。
// 言語サーバーは、そのファイルの種類の最初のファイルを開いたときに起動する。
// 利用者が設定で言語を足したときの起動の契機は、利用者の設定から宣言ファイルの activation へ足す（基本設計 6.7）。
//
// 言語サーバーの実行ファイルは PATH から探す。見つからず、設定に mise の道具の ID（serverConfig.Mise）があれば、
// 入れ方を一度だけ案内するか、利用者が autoInstall を選んでいれば mise に入れさせてから mise exec で起動する（FR-143。mise.go）。
// 言語サーバーと mise は、拡張機能が強制終了されても残らないように起動する（startProcess）。
//
// # 機能
//
//   - 文書の同期: buffer/open、buffer/change、buffer/close を textDocument/didOpen、didChange（増分）、didClose にする
//   - 診断: textDocument/publishDiagnostics を diagnostic/set にする
//   - 保存の前の整形: buffer/willSave（宣言ファイルの saveHook）。textDocument/formatting の結果をトランザクションにする
//   - 保存の通知: buffer/didSave を textDocument/didSave にする
//   - インレイヒント: 設定の inlayHints で出すことにした言語サーバーで、textDocument/inlayHint の結果を virtualText/set の inline の
//     仮想テキストにする。文書を開いたとき、変えたとき（150 ミリ秒ごとに間引く）、workspace/inlayHint/refresh のときに問い合わせる。
//     コマンド lsp.inlayHints.toggle で出し入れを切り替える（inlay.go。FR-255〜FR-257）
//
// 次の本体の操作を、宣言ファイルの provides で引き受ける（基本設計 5.5。FR-94）。本体は provider/invoke で問い合わせる。
// キーは本体の既定のキー（<leader>lh、K、gq、<C-]> など。要件定義 4.11）で、この拡張機能はキーを宣言しない。
// 段階 2 のコマンド（lsp.definition、lsp.hover、lsp.format）は、この操作に置き換えた（#203）。
//
//   - definition、implementation、typeDefinition: textDocument/definition、implementation、typeDefinition の結果へ editor/open で移動する。
//     候補が複数なら choice/ask で選ばせる。args の peek が true なら、editor/open の peek で移動先の写しを本体のポップアップに出させる
//   - hover: textDocument/hover の結果を popup/show で表示させる
//   - references、documentSymbols、workspaceSymbols: 結果の一覧を choice/ask で選ばせ、選んだ場所へ移る（一覧の部品ができるまでの仮の形）
//   - format: textDocument/formatting（範囲があり言語サーバーが対応すれば textDocument/rangeFormatting）の結果をトランザクションにする
//   - rename: args の text を新しい名前にして textDocument/rename を送り、結果を buffer/edit で適用する。
//     同期していないファイルに及ぶ変更は適用しない
//
// コードアクションと補完は、まだ引き受けない。
//
// # 位置の変換
//
// 本体の位置は行とバイト数で、LSP の位置は行と、サーバーが選んだ数え方（既定は UTF-16）の文字の位置である。
// 変換はこの拡張機能の中で行う（基本設計 4.1）。initialize では utf-8 を先に申し出る（基本設計 6.3）。
//
// # 言語サーバーの異常終了
//
// 言語サーバーが異常終了しても、この拡張機能は動き続ける。
// 回数の上限（既定は 3 回。仮）までは、待ってから再起動し、開いている文書の全文を送り直す。
// 上限に達したら、そのファイルの種類の言語サーバーをもう起動せず、message/show で知らせる。
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("vi-imitation-ext-lsp: %v", err)
	}
}

// run は、本体から shutdown を受け取るか、本体が標準入力を閉じるか、シグナルを受け取るまで、メッセージを処理する。
// 終わるときに、起動した言語サーバーを止める。
// shutdown のときは、接続を閉じる前に OnShutdown のフックで止める（extension.stop）。
// それ以外のときは接続が閉じているので、Serve が戻った後で止める。
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	x := newExtension(ctx)
	err := x.srv.Run(ctx)
	x.stop()
	return err
}
