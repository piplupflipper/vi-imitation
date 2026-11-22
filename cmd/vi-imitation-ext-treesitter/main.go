// Command vi-imitation-ext-treesitter は、tree-sitter で構文を解析し、色を付ける範囲の一覧を本体へ送り、
// 構文に沿ったテキストオブジェクト（関数、型、引数）に答える拡張機能である（基本設計 6.1）。
//
// 本体（internal/exthost）が、文法を持つ種類のファイルを開いたときに子プロセスとして起動する（宣言ファイル manifest.json の filetype の契機）。
// 標準入出力で JSON-RPC のメッセージをやり取りするので、ログは標準エラー出力へ書く。
//
// 動きは次のとおりである。
//
//   - buffer/open で全文を解析し、色を付ける範囲の一覧を decoration/set で送る
//   - buffer/change の編集を 1 つずつ構文木（Tree.Edit）と手元の全文に適用し、前の構文木を渡して増分で解析し直し、一覧を送り直す
//   - buffer/close で構文木を捨てる
//
// 解析と一覧の計算は、メッセージを処理する goroutine ではなく 1 つの解析の goroutine で行う。
// 解析の間に溜まった差分はまとめて 1 回で解析し直し、途中のバージョンの一覧は送らない（highlighter の doc）。
// 一覧が大きすぎるバッファ（maxDecorationSetBytes）には色を付けない。
//
// テキストオブジェクト（af、if、ac、ic、aa、ia）は、textObject/resolve に答える（textobject.go）。
// 解析の goroutine が最後に解析した構文木の複製を使い、問い合わせのバージョンの構文木が無ければ、
// 問い合わせの goroutine で全文から解析する。どちらもメッセージを処理する goroutine の上では行わない。
//
// 同梱する言語は Go（ファイルの種類 "go"）と JSON（"json" と "jsonc"）で、表は languages にある。
// 同梱する文法は公式の Go のモジュール（github.com/tree-sitter/tree-sitter-go など）を cgo で一緒にコンパイルする。
// cgo を使うのはこの実行ファイルだけで、本体は cgo を使わない（NFR-02）。
//
// 窓の変化の知らせ（window/change）を受け取り、カーソルを含む関数やクラスの見出しの行を、窓の上端の帯（band/set）に出す（sticky.go。
// nvim-treesitter-context に当たる）。帯は、解析の goroutine が最後に解析した構文木の複製から、帯を作る goroutine で作る。
// 出すかどうかは、設定の context と、コマンド treesitter.context.toggle で変える。
//
// ほかの言語は、利用者の設定（extensions.settings.treesitter.parsers。settings.go の parserSpec）に並べたパーサを、
// そのファイルの種類のバッファを初めて開いたときに git で取得し、C コンパイラで共有ライブラリにビルドし、dlopen で読み込む（FR-142。parsers.go）。
// 取得したコミットはロックファイルに記録し、次からは同じコミットとビルドしたものを使う。
// 同じファイルの種類を同梱の言語と利用者の設定の両方が持てば、利用者の設定のパーサを使う。
// dlopen を使うので、この実行ファイルは glibc に動的にリンクする（NFR-01。Makefile）。
// 取得とビルドは、メッセージを処理する goroutine とも解析の goroutine とも別の、用意の goroutine で行う（userparser.go）。
//
// ハイライトのクエリは、各文法の公式の queries/highlights.scm を queries/ の下に写して埋め込む。
// キャプチャ名から decoration/set の色の種類の名前への対応は styleFor にある。
// テキストオブジェクトのクエリ（queries/<言語>/textobjects.scm）は、このリポジトリで書いたものである。
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
		log.Fatalf("vi-imitation-ext-treesitter: %v", err)
	}
}

// run は、本体が標準入力を閉じるか、シグナルを受け取るまで、メッセージを処理する。
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h, err := newHighlighter()
	if err != nil {
		return err
	}
	defer h.close()
	return h.server().Run(ctx)
}
