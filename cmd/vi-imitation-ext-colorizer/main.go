// Command vi-imitation-ext-colorizer は、バッファの本文の色コード（#ff0000、rgb(255, 0, 0)、red など）の範囲を、
// その色で塗る標準の拡張機能である（要件定義 4.14。FR-260〜FR-262。基本設計 6.12）。
// オーナーの nvim の nvim-colorizer.lua（catgoose/nvim-colorizer.lua）に当たる。
//
// 本体（internal/exthost）が子プロセスとして起動し、標準入出力で JSON-RPC のメッセージをやり取りする。
// 標準出力はメッセージに使うので、ログは標準エラー出力へ書く。宣言ファイルは同じディレクトリの manifest.json である。
//
// # 何をするか
//
// buffer/open と buffer/change で受け取ったバッファのうち、設定の fileTypes のファイルの種類のものについて、
// 行ごとに色コードを探し（scan.go）、範囲に直の色（ext.Decoration.Attr）を付けた一覧を decoration/set で送る。
// 認識する形と塗り方の既定は、nvim-colorizer.lua の既定に合わせた（scan.go の defaultFormats、config.go）。
// 編集のたびには、編集で置き換わった行だけを数え直す（extension.go の change）。
//
// # 起動の契機
//
// 宣言ファイルの起動の契機は、設定のファイルの種類の契機（settingsFileType。基本設計 6.7）の /fileTypes だけである。
// 本体は、設定の fileTypes に書いたファイルの種類のバッファを開いたときに、この拡張機能を起動する。
// 設定に何も書いていなければ起動しない。
//
// # 設定
//
// 拡張機能の ID は colorizer で、設定は extensions.settings.colorizer に書く（形は config.go の settings の doc）。例:
//
//	"colorizer": {
//	  "fileTypes": ["css", "scss", "html", "lua", "toml", "yaml"],
//	  "mode": "background",
//	  "hex": { "rrggbbaa": true },
//	  "rgb": true,
//	  "hsl": true
//	}
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
		log.Fatalf("vi-imitation-ext-colorizer: %v", err)
	}
}

// run は、本体が標準入力を閉じるか、シグナルを受け取るまで、メッセージを処理する。
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newExtension().srv.Run(ctx)
}
