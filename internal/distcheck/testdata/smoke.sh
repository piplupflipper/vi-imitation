#!/bin/sh
# make dist-smoke がコンテナの中で実行する、配布物の起動の確認（NFR-01、NFR-06）。
# 使い方: smoke.sh <配布物のディレクトリ>
# 環境変数 SMOKE_TREESITTER が 1（既定）なら、tree-sitter の拡張機能が起動して Go のソースを解析することまで確かめる。
# 0 なら（古い glibc と musl の環境）、tree-sitter の拡張機能は glibc に動的にリンクしているので起動しなくてよく、失敗にしない（NFR-01）。
# busybox の sh でも動くように、POSIX の sh の範囲で書く。
set -eu
d=$1
treesitter=${SMOKE_TREESITTER:-1}

"$d/vi-imitation" --version
"$d/vi-imitation-mcp-bridge" --version

# 宣言ファイルのある拡張機能は、標準入力を閉じると接続が終わったとして終了コード 0 で終わる。
for m in "$d"/extensions/*; do
	name=${m##*/}
	if [ "$name" = treesitter ] && [ "$treesitter" != 1 ]; then
		if "$m/vi-imitation-ext-$name" </dev/null 2>/dev/null; then
			echo "$name: ok"
		else
			echo "$name: 起動しない（glibc に動的にリンクしているので、この環境では構文の色付けを使えない）"
		fi
		continue
	fi
	"$m/vi-imitation-ext-$name" </dev/null
	echo "$name: ok"
done

# スクリプトの実行環境は、引数が無いと使い方を表示する。
"$d/vi-imitation-ext-script" 2>&1 | grep -q '^usage:'
echo "script: ok"

if [ "$treesitter" != 1 ]; then
	exit 0
fi

# tree-sitter の拡張機能に Go のソースを 1 回解析させ、decoration/set が返ることを確かめる。
# 文法の C のコードと、動的にリンクした glibc（malloc など）を実行時に通す。
msg() {
	printf 'Content-Length: %d\r\n\r\n%s' "${#1}" "$1"
}
out=$(
	{
		msg '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"apiVersion":"0.16.0"}}'
		msg '{"jsonrpc":"2.0","method":"buffer/open","params":{"buffer":1,"version":1,"fileType":"go","text":"package main\nfunc main() {}"}}'
		# 解析は別の goroutine で行うので、応答を待ってから標準入力を閉じる。
		sleep 2
	} | "$d/extensions/treesitter/vi-imitation-ext-treesitter"
)
case $out in
*'"method":"decoration/set"'*'"styles":["keyword","function"]'*) echo "treesitter parse: ok" ;;
*)
	echo "treesitter parse: unexpected output: $out" >&2
	exit 1
	;;
esac
