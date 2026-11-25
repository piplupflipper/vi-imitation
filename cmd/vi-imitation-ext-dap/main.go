// Command vi-imitation-ext-dap は、DAP（Debug Adapter Protocol）のクライアントとしてデバッグを行う拡張機能である（基本設計 6.4、FR-31）。
//
// 本体（internal/exthost）が、宣言ファイル manifest.json の provides で名乗った本体の操作が呼ばれたときに、子プロセスとして起動する。
// 本体とは標準入出力で JSON-RPC のメッセージをやり取りするので、ログは標準エラー出力へ書く。
//
// debug.continue（デバッグが始まっていないとき）で、設定に書いたデバッグアダプター（既定は delve の dlv dap）を子プロセスとして起動し、DAP で話す。
// DAP のメッセージの区切りは Content-Length ヘッダーの形式だが、中身は JSON-RPC ではなく DAP 独自の
// request、response、event の形なので、送受信は dap.go に書く。
// 仕様: https://microsoft.github.io/debug-adapter-protocol/specification
//
// 引き受ける本体の操作（基本設計 5.5。FR-94）は次のとおりで、どれも provider/invoke には受け付けたかどうかだけを ok で返し、
// 結果は後から sign/set、editor/open、message/show と、デバッグの情報のペイン（pane.go）で届ける（基本設計 6 章の冒頭）。
// キーは本体の既定のキー（<leader>dc など。要件定義 4.11）である。
// 段階 2 のコマンド（dap.start、dap.toggleBreakpoint など）は、この操作に置き換えた（#203）。
//
//   - breakpoint.toggle: カーソルの行のブレークポイントを切り替える
//   - breakpoint.conditional: カーソルの行に条件付きのブレークポイントを置く。条件の式は args の text（:breakpoint.conditional i > 3）
//   - breakpoint.clear: すべてのブレークポイントを消す
//   - debug.continue: 続行する。デバッグが始まっていなければ始める
//   - debug.pause、debug.terminate: 一時停止する、デバッグを終える
//   - debug.restart、debug.runLast: 終えてから前回の設定で始め直す、前回の設定で始める
//   - debug.stepOver、debug.stepIn、debug.stepOut: ステップ実行
//   - debug.toggleUI: デバッグの情報のペイン（スコープ、スレッド、ブレークポイント、出力などの区画。FR-108）を開け閉めする
//
// breakpoint.conditional のほかは引数を取らない。本体のコマンドラインから引数を付けて呼んだ（:debug.continue foo）ら、
// ok を false で返して知らせる（noArgs）。
// デバッグ対象の出力は、ペインの出力の区画に出す（段階 2 の output/append の dap の欄には書かない）。
//
// 状態の持ち方と、本体との取り決めの詳細は debugger の doc に、DAP の手順は session の doc にある。
// 設定（initialize の settings）の項目は settings の doc にある。
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// アダプターの見張り（guard_linux.go）として起動されたなら、見張りとして動いて終わる。
	maybeRunGuard()
	if err := run(); err != nil {
		log.Fatalf("vi-imitation-ext-dap: %v", err)
	}
}

// run は、本体から shutdown を受け取るか、本体が標準入力を閉じるか、シグナルを受け取るまで、メッセージを処理する。
// 終わるときに、デバッグ中ならデバッグアダプターを終わらせる。
// shutdown のときは、接続を閉じる前に OnShutdown のフックで終わらせる（debugger.shutdown）。
// それ以外のときは、Serve が戻った後で終わらせる。
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d := newDebugger()
	defer d.shutdown()
	return d.srv.Run(ctx)
}
