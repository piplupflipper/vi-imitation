// Command vi-imitation-ext-memusage は、メモリの使用量を出す標準の拡張機能である（要件定義 4.14。FR-227、FR-228。基本設計 6.9）。
//
// 本体（internal/exthost）が子プロセスとして起動し、標準入出力で JSON-RPC のメッセージをやり取りする。
// 標準出力はメッセージに使うので、ログは標準エラー出力へ書く。宣言ファイルは同じディレクトリの manifest.json である。
// オーナーの nvim の lualine の部品（lua/core/memusage.lua。"nvim 56M · LSP 390M" と :MemUsage）を取り込んだものである（#367）。
//
// 足すものは次の 2 つである（名前は仮）。
//
//   - ステータス行の部品 memusage.rss: 本体（server のプロセス。この拡張機能の親）の RSS と、本体の子孫（拡張機能と、拡張機能が起動した
//     言語サーバー、デバッグアダプターなど）の RSS の合計を、"vi 18M · ext 410M" の形で出す。設定の statusline か tabline に並べると、
//     本体が起動時（最初の画面を描いた後）にこの拡張機能を起動する（基本設計 7.1）。本体の既定の並びには入れていない
//   - コマンド memusage.show: 内訳を画面の中央のポップアップに出す。本体の子ごと（拡張機能ごと）に、拡張機能のプロセスと、
//     それが起動したプロセス（言語サーバーごと）を字下げして並べる
//
// 更新の間隔は、設定の extensions.settings.memusage.intervalMs（ミリ秒。500 以上 3600000 以下。既定は 5000）で変える。
// 部品の中身は、変わったときだけ送る。
//
// Linux では /proc/<pid>/task/<tid>/children で本体の子孫だけを辿り、全プロセスの一覧を作らない。
// /proc から子を辿れない環境（Linux 以外）では、ps -A -o pid=,ppid=,rss=,args= の出力から木を作る。
// 拡張機能のプロセスは、本体が渡す環境変数 VI_IMITATION_EXTENSION_DIR（ext.EnvExtensionDir）の宣言ファイルの ID で名前を付ける。
// 環境変数を読めない ps の出力では、実行ファイルの名前から vi-imitation-ext- を除いたものを名前にする。
//
// バッファのテキストは使わないので、宣言ファイルに noBufferSync を書き、本体からバッファの全文と差分を受け取らない
// （ext.Manifest.NoBufferSync。PR #375 のレビューの必須 1。受け取ると、開いたファイルの大きさに比例してこの拡張機能の RSS が膨らむ）。
//
// 宣言ファイルの起動の契機は、コマンドの契機だけにした。起動の契機 startup（ext.ActivationStartup）にしないのは、
// 部品を並べていない利用者（本体の既定）の本体でも常に動いて、使わないメモリを使うためである。
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
		log.Fatalf("vi-imitation-ext-memusage: %v", err)
	}
}

// run は、本体が標準入力を閉じるか、シグナルを受け取るまで、メッセージを処理する。
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newExtension().server.Run(ctx)
}
