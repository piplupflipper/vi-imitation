# タスクランナー。`make` または `make help` でターゲットの一覧を表示する。

# 開発ツールはバージョンを固定し、`go run` で実行する。
# 初回の実行時だけ、ツールのビルドに数分かかる。
GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.9.0

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK   := go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

BIN_DIR := bin
BINARY  := $(BIN_DIR)/vi-imitation
# MCP の中継コマンド。MCP のクライアントが標準入出力のサーバーとして起動する（FR-33）。
MCP_BRIDGE := $(BIN_DIR)/vi-imitation-mcp-bridge
# 標準の拡張機能。本体とは別の実行ファイルとして作る（NFR-01）。
EXTENSIONS := vi-imitation-ext-line vi-imitation-ext-script vi-imitation-ext-dap vi-imitation-ext-lsp vi-imitation-ext-acp vi-imitation-ext-mcp vi-imitation-ext-git vi-imitation-ext-finder vi-imitation-ext-filer vi-imitation-ext-formatter vi-imitation-ext-memusage vi-imitation-ext-colorizer
# cgo を使う標準の拡張機能。C コンパイラが要る。cgo を使うのはこれだけで、本体は CGO_ENABLED=0 で作る（NFR-02）。
# cgo の拡張機能（tree-sitter）は、glibc に動的にリンクする（NFR-01。2026-11-20 のオーナーの裁定、#277）。
# 利用者の手元でビルドしたパーサの共有ライブラリを dlopen で読む（FR-142）ためで、musl を静的にリンクした実行ファイルは dlopen を使えない。
# このため、ビルドしたマシンより古い glibc の環境（AlmaLinux 8 の glibc 2.28 など）と musl の環境（Alpine）では起動せず、
# そこでは構文の色付けを使えない。本体と、ほかの拡張機能は静的なままである（docs/measurements/nfr-dist.md）。
#
# req: NFR-01
CGO_EXTENSIONS := vi-imitation-ext-treesitter
VERSION :=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/piplupflipper/vi-imitation/internal/version.version=$(VERSION)

# 配布物（NFR-01、NFR-06）。ビルド済みの実行ファイルと宣言ファイルを 1 つのディレクトリと tar.gz にまとめる。
# 宣言ファイルのある拡張機能は extensions/<名前>/ に実行ファイルと並べ（FR-13）、
# 宣言ファイルの無い実行ファイル（スクリプトの実行環境）は本体と同じ階層に置く。
# 配布するのはビルド済みの実行ファイルだけなので、利用者は C コンパイラも Go も要しない。
# 設定に並べた tree-sitter のパーサを手元でビルドする機能（FR-142）を使うときだけ、git と C コンパイラが要る（NFR-06 の注）。
# 実行ファイルに静的にリンクするサードパーティのライセンスの文面は licenses/ に置く。
# herdr の中でポップアップとペインを開くための同梱のプラグインの宣言ファイルは herdr-plugin/ に置く。
# 利用者は herdr plugin link <配布物>/herdr-plugin で 1 回登録する（基本設計 1.3）。
#
# req: NFR-01, NFR-06
DIST_ROOT := dist
DIST_NAME := vi-imitation-$(VERSION)-$(shell go env GOOS)-$(shell go env GOARCH)
DIST_DIR  := $(DIST_ROOT)/$(DIST_NAME)
# 配布する実行ファイルのパッケージ。依存するモジュールのライセンスの文面を集めるのに使う。
DIST_PACKAGES := ./cmd/vi-imitation ./cmd/vi-imitation-mcp-bridge $(addprefix ./cmd/,$(EXTENSIONS) $(CGO_EXTENSIONS))
# 本体にまだつないでいないが、つなぐことが決まっているパッケージ。ライセンスの文面を集める対象に足す。
# internal/diff は go-udiff（BSD-3-Clause と MIT。LICENSE-BSD と LICENSE-MIT）を使い、本体の server から使う（基本設計 6.8）。
# 本体が import すると DIST_PACKAGES の依存に含まれるので、そのときにここから外してよい（残しても結果は変わらない）。
DIST_LICENSE_PACKAGES := $(DIST_PACKAGES) ./internal/diff
# Go のモジュールでなく、データを写して本体に入れたもののライセンスの文面。licenses/ の下に同じ並びで写す。
# 既定のアイコンの表（internal/ui の icons_default.go。FR-152）は、nvim-web-devicons と neo-tree.nvim（どちらも MIT）から写した。
# 色コードの着色の拡張機能の色の名前の表（cmd/vi-imitation-ext-colorizer の names_table.go。FR-260）は、Neovim（Apache-2.0）から写した。
DIST_DATA_LICENSES := internal/ui/licenses cmd/vi-imitation-ext-colorizer/licenses
# 配布物を起動して確かめるコンテナのイメージ。ビルド用の道具が無く、C の標準ライブラリが異なるものを選ぶ。
# 結果が docs/measurements/nfr-dist.md の記録と同じものを指すように、版のタグで固定する。
DIST_SMOKE_IMAGES := debian:13.7-slim almalinux:8.10-minimal alpine:3.24.1 busybox:1.36.1
# DIST_SMOKE_IMAGES のうち、glibc に動的にリンクした tree-sitter の拡張機能が起動して解析することまで確かめるイメージ。
# 残りのイメージ（古い glibc と musl）では、tree-sitter の拡張機能が起動しなくても失敗にしない（NFR-01）。
DIST_SMOKE_TREESITTER_IMAGES := debian:13.7-slim busybox:1.36.1

.DEFAULT_GOAL := help

.PHONY: help
help: ## ターゲットの一覧を表示する
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-12s %s\n", $$1, $$2}'

.PHONY: fmt
fmt: ## コードを整形する（gofumpt と goimports）
	$(GOLANGCI_LINT) fmt

.PHONY: fmt-check
fmt-check: ## 整形されていないファイルがあれば失敗する
	$(GOLANGCI_LINT) fmt --diff

.PHONY: typecheck
typecheck: ## 型チェックをする（テストコードを含む全パッケージをコンパイルし、go vet を実行）
	go build ./...
	go vet ./...

.PHONY: lint
lint: ## リントを実行する
	$(GOLANGCI_LINT) run

.PHONY: test
test: ## テストを実行する（データ競合の検出つき）
	go test -race -cover ./...

.PHONY: tidy-check
tidy-check: ## go.mod と go.sum が整理済みでなければ失敗する
	go mod tidy -diff

.PHONY: vuln
vuln: ## 依存ライブラリの既知の脆弱性を調べる
	$(GOVULNCHECK) ./...

.PHONY: progress
progress: ## 要件ごとの進捗（docs/progress.md）を生成する
	go run ./cmd/reqtrace

.PHONY: progress-check
progress-check: ## docs/progress.md が最新でなければ失敗する
	go run ./cmd/reqtrace -check

# 本体に埋め込む AI エージェント向けの文書（docs/agent/*.md）の一覧の部分を、コードの表から生成する（FR-172）。
# 設定の表、操作の表、Ex コマンドの表、拡張機能 API を変えたら make docs を打つ。打ち忘れると make docs-check（make check）が失敗する。
.PHONY: docs
docs: ## 本体に埋め込む文書（docs/agent/*.md）の一覧を生成する
	go generate ./docs

.PHONY: docs-check
docs-check: ## docs/agent/*.md の一覧がコードの表とずれていれば失敗する
	go run ./cmd/docgen -check

.PHONY: check
check: fmt-check typecheck lint test tidy-check progress-check docs-check ## コミット前の検査を全て実行する（CI と同じ内容）

# 本体を CGO_ENABLED=0 で作る（NFR-02）。make build-check が検査する。
# 本体はシンボルとデバッグ情報を削らずに作る（-ldflags に -s -w を足さない）。デバッガーで変数を読めるようにするためで、
# NFR-05 の実行ファイルの大きさの上限（12 MiB。2026-11-21 に 10 MiB から上げた）は、この形で満たす。make build-check と make footprint-check が検査する。
#
# req: NFR-02, NFR-05
.PHONY: build
build: ## 本体、MCP の中継コマンド、標準の拡張機能の実行ファイルを bin/ に作る
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/vi-imitation
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(MCP_BRIDGE) ./cmd/vi-imitation-mcp-bridge
	for e in $(EXTENSIONS); do CGO_ENABLED=0 go build -trimpath -o $(BIN_DIR)/$$e ./cmd/$$e || exit 1; done
	for e in $(CGO_EXTENSIONS); do CGO_ENABLED=1 go build -trimpath -o $(BIN_DIR)/$$e ./cmd/$$e || exit 1; done

.PHONY: build-check
build-check: build ## make build の生成物を検査する（本体の静的リンクと CGO_ENABLED=0、本体の大きさなど。NFR-02、NFR-05）
	VI_IMITATION_BUILD_DIR=$(abspath $(BIN_DIR)) go test -count=1 -v ./internal/distcheck

# 起動から最初の画面までの時間と本体の RSS を、make build の本体で測る（NFR-05）。
# テストの実行ファイルは -race と -cover の計測を含むので測らない。測り方は docs/measurements/nfr05-footprint.md に合わせる。
# 他のテストと CPU を取り合わないよう、このテストだけを単独で実行する。
.PHONY: footprint-check
footprint-check: build ## make build の本体の起動時間と RSS を検査する（10,000 行のファイルで 20 回の中央値。NFR-05）
	VI_IMITATION_BUILD_DIR=$(abspath $(BIN_DIR)) go test -count=1 -v -run '^TestFootprintInPTY$$' ./cmd/vi-imitation

.PHONY: dist
dist: build ## 配布物を dist/ に作る（実行ファイルと宣言ファイルのディレクトリと tar.gz。NFR-01）
	rm -rf $(DIST_ROOT)
	mkdir -p $(DIST_DIR)/extensions $(DIST_DIR)/licenses/go $(DIST_DIR)/herdr-plugin
	cp $(BINARY) $(MCP_BRIDGE) config.example.jsonc $(DIST_DIR)/
	cp internal/mux/herdr-plugin/herdr-plugin.toml $(DIST_DIR)/herdr-plugin/
	for e in $(EXTENSIONS) $(CGO_EXTENSIONS); do \
		if [ -f cmd/$$e/manifest.json ]; then \
			d=$(DIST_DIR)/extensions/$${e#vi-imitation-ext-}; \
			mkdir -p $$d && cp $(BIN_DIR)/$$e cmd/$$e/manifest.json $$d/ || exit 1; \
		else \
			cp $(BIN_DIR)/$$e $(DIST_DIR)/ || exit 1; \
		fi; \
	done
	cp $$(go env GOROOT)/LICENSE $$(go env GOROOT)/PATENTS $(DIST_DIR)/licenses/go/
	go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Dir}}{{end}}{{end}}' $(DIST_LICENSE_PACKAGES) | sort -u | \
		while read -r p d; do \
			mkdir -p $(DIST_DIR)/licenses/$$p && cp $$d/LICENSE* $(DIST_DIR)/licenses/$$p/ || exit 1; \
		done
	for d in $(DIST_DATA_LICENSES); do cp -R $$d/. $(DIST_DIR)/licenses/ || exit 1; done
	tar -C $(DIST_ROOT) -czf $(DIST_DIR).tar.gz $(DIST_NAME)

.PHONY: dist-check
dist-check: dist ## 配布物の形を検査する（宣言ファイルの command が実行ファイルを指し、静的にリンクされていること）
	VI_IMITATION_DIST_DIR=$(abspath $(DIST_DIR)) go test -count=1 ./internal/distcheck

.PHONY: dist-smoke-images
dist-smoke-images: ## make dist-smoke のイメージを取得する（取得の失敗は 1 回だけ再試行する）
	for img in $(DIST_SMOKE_IMAGES); do docker pull -q $$img || { sleep 10; docker pull -q $$img; } || exit 1; done

.PHONY: dist-smoke
dist-smoke: dist ## ビルド用の道具の無いコンテナで配布物を起動する（Docker が要る。NFR-01、NFR-06）
	for img in $(DIST_SMOKE_IMAGES); do \
		echo "== $$img"; \
		ts=0; case " $(DIST_SMOKE_TREESITTER_IMAGES) " in *" $$img "*) ts=1 ;; esac; \
		docker run --rm --network none -e SMOKE_TREESITTER=$$ts \
			-v $(abspath $(DIST_DIR)):/opt/vi-imitation:ro \
			-v $(abspath internal/distcheck/testdata/smoke.sh):/smoke.sh:ro \
			$$img /bin/sh /smoke.sh /opt/vi-imitation || exit 1; \
	done

.PHONY: run
run: ## ビルドせずに実行する
	go run ./cmd/vi-imitation

.PHONY: hooks
hooks: ## コミット前に make check を実行する Git フックを有効にする
	git config core.hooksPath .githooks

.PHONY: clean
clean: ## ビルドの生成物を削除する
	rm -rf $(BIN_DIR) $(DIST_ROOT)
