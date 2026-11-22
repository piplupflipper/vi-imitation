package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// 既定値。どれも仮である。
const (
	// defaultMaxRestarts は、言語サーバーが異常終了したときに再起動する回数の上限である。
	defaultMaxRestarts = 3
	// defaultInitTimeout は、言語サーバーの initialize の応答を待つ時間の上限である。
	defaultInitTimeout = 30 * time.Second
	// defaultRequestTimeout は、コマンド（定義へ移動、ホバー、整形）が言語サーバーの応答を待つ時間の上限である。
	// 本体が応答を待たないとき（sync が false）の上限で、sync が true のときは本体の期限に従う。
	defaultRequestTimeout = 10 * time.Second
	// defaultResetAfter は、言語サーバーがこの時間より長く稼働した後に異常終了したら、再起動の回数を数え直すという時間である。
	// internal/exthost の DefaultResetAfter と同じ考え方である。
	defaultResetAfter = time.Minute
	// defaultRestartDelay は、異常終了から再起動までの待ち時間の 1 回目である。n 回目は n 倍にする。
	defaultRestartDelay = 500 * time.Millisecond
	// defaultInstallTimeout は、mise に言語サーバーを入れさせるとき（mise where と mise install）に待つ時間の上限である（FR-143）。
	// 入れている間は initialize の期限（defaultInitTimeout）を数えないので、別に上限を置く。
	defaultInstallTimeout = 10 * time.Minute
)

// serverConfig は、1 つのファイルの種類に使う言語サーバーの設定である。
// 利用者の設定のうち LSP の拡張機能に向けた部分（initialize の settings）の servers の値である（基本設計 6.3）。
// 項目の名前は仮である。
type serverConfig struct {
	// Command は、言語サーバーの実行ファイルである。"/" を含まなければ PATH から探す。
	Command string `json:"command"`
	// Args は、Command に渡す引数の列である。
	Args []string `json:"args,omitempty"`
	// Mise は、Command を入れる mise の道具の ID で、版を含めて書く（例: "npm:pyright@1.1.410"。FR-143）。
	// Command が見つからないとき、空でなければ、入れ方を案内するか、mise に入れさせてから起動する（AutoInstall）。
	// 空なら、見つからないときは起動できないことを知らせるだけである。
	Mise string `json:"mise,omitempty"`
	// AutoInstall は、Command が見つからないとき、mise に Mise の道具を入れさせてから mise exec で起動するかどうかである。
	// false なら、入れ方（mise use -g <Mise>）を案内するだけで、何も入れない。
	// 省くと、settings の autoInstall に従う。Mise が空なら効かない。
	AutoInstall *bool `json:"autoInstall,omitempty"`
	// LanguageID は、textDocument/didOpen の languageId である。省くとファイルの種類の名前を使う。
	LanguageID string `json:"languageId,omitempty"`
	// RootMarkers は、ワークスペースの根を探す目印のファイル名である。
	// 最初に開いたファイルのディレクトリから親へたどり、目印のどれかがあるディレクトリを根にする。
	// 見つからなければ、そのファイルのディレクトリを根にする。
	RootMarkers []string `json:"rootMarkers,omitempty"`
	// InitializationOptions は、initialize の initializationOptions にそのまま渡す値である。
	InitializationOptions json.RawMessage `json:"initializationOptions,omitempty"`
	// Settings は、言語サーバーの workspace/configuration に答える値である。キーは section の名前である。
	// 知らない section と、section の無い問い合わせには null を返す。
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	// FormatOnSave は、保存の前に textDocument/formatting で整形するかどうかである。省くと true である。
	FormatOnSave *bool `json:"formatOnSave,omitempty"`
	// TabSize と InsertSpaces は、textDocument/formatting の FormattingOptions に渡す値である。
	// LSP 3.17 ではどちらも省けない項目なので、省いたときは TabSize を 4、InsertSpaces を false にする。
	TabSize      int  `json:"tabSize,omitempty"`
	InsertSpaces bool `json:"insertSpaces,omitempty"`
	// InlayHints は、言語サーバーが textDocument/inlayHint に対応していれば、インレイヒントを仮想テキストで出すかどうかである
	// （inlay.go。FR-255）。省くと、settings の inlayHints に従う。
	// 言語サーバーの側でもヒントを有効にする設定が要ることがある（gopls は Settings の gopls.hints）。
	InlayHints *bool `json:"inlayHints,omitempty"`
}

// formatOnSave は、保存の前に整形するかどうかを返す。
func (c *serverConfig) formatOnSave() bool {
	return c.FormatOnSave == nil || *c.FormatOnSave
}

// autoInstall は、Command が見つからないときに mise に入れさせるかどうかを返す。
// parseSettings が、省いた AutoInstall を settings の autoInstall で埋める。
func (c *serverConfig) autoInstall() bool {
	return c.Mise != "" && c.AutoInstall != nil && *c.AutoInstall
}

// inlayHints は、設定でインレイヒントを出すことにしているかを返す。parseSettings が、省いた InlayHints を settings の inlayHints で埋める。
// 実行中の切り替え（lsp.inlayHints.toggle）は、この値より優先する（client.inlayEnabled）。
func (c *serverConfig) inlayHints() bool {
	return c.InlayHints != nil && *c.InlayHints
}

// tabSize は、FormattingOptions の tabSize を返す。
func (c *serverConfig) tabSize() int {
	if c.TabSize <= 0 {
		return 4
	}
	return c.TabSize
}

// settings は、LSP の拡張機能の設定の全体である。
type settings struct {
	// Servers は、ファイルの種類の名前から言語サーバーの設定への対応である。
	// 利用者の設定は、既定（defaultServers）に、ファイルの種類ごとに上書きする。
	// 値を null にすると、そのファイルの種類の既定の言語サーバーを使わない。
	Servers map[string]*serverConfig `json:"servers,omitempty"`
	// MaxRestarts は、言語サーバーが異常終了したときに再起動する回数の上限である。省くと defaultMaxRestarts。
	// 0 なら再起動しない。負の値は 0 と同じに扱う。
	MaxRestarts *int `json:"maxRestarts,omitempty"`
	// AutoInstall は、servers の autoInstall を省いた言語サーバーに使う値である。省くと false（入れ方を案内するだけ）である。
	AutoInstall bool `json:"autoInstall,omitempty"`
	// InlayHints は、servers の inlayHints を省いた言語サーバーに使う値である。省くと false（出さない。Neovim 0.12.5 の
	// vim.lsp.inlay_hint の既定と同じ）である（FR-255）。
	InlayHints bool `json:"inlayHints,omitempty"`
}

// defaultServers は、既定の言語サーバーの表である。最初は Go（gopls）だけを持つ。
// ほかの言語の言語サーバーと、その mise の道具の ID は、利用者が設定に書く（FR-143。#278 の決め）。
// 表に足すと、言語の側の更新（言語サーバーの名前や版）にこのリポジトリが引っ張られるためである。
func defaultServers() map[string]*serverConfig {
	return map[string]*serverConfig{
		"go": {
			Command:     "gopls",
			LanguageID:  "go",
			RootMarkers: []string{"go.work", "go.mod", ".git"},
		},
	}
}

// config は、設定を読んで既定値を埋めた後の値である。
type config struct {
	servers     map[string]*serverConfig
	maxRestarts int
}

// parseSettings は、initialize の settings を読み、既定値を埋めた設定を返す。
// raw が空なら既定値だけの設定を返す。
func parseSettings(raw json.RawMessage) (*config, error) {
	c := &config{servers: defaultServers(), maxRestarts: defaultMaxRestarts}
	if len(raw) == 0 {
		return c, nil
	}
	var s settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("LSP の拡張機能の設定を読めない: %w", err)
	}
	maps.Copy(c.servers, s.Servers)
	var errs []error
	for ft, sc := range c.servers {
		if sc == nil {
			delete(c.servers, ft)
			continue
		}
		if sc.Command == "" {
			errs = append(errs, fmt.Errorf("servers.%s.command が空である", ft))
		}
		if sc.LanguageID == "" {
			sc.LanguageID = ft
		}
		// mise の引数に置くので、オプションと読まれる形と空白を含む形は受け付けない。
		switch {
		case sc.Mise == "":
		case strings.HasPrefix(sc.Mise, "-") || strings.ContainsFunc(sc.Mise, unicode.IsSpace):
			errs = append(errs, fmt.Errorf("servers.%s.mise は mise の道具の ID（例: npm:pyright@1.1.410）でない: %q", ft, sc.Mise))
		case !hasMiseVersion(sc.Mise):
			errs = append(errs, fmt.Errorf("servers.%s.mise に固定した版が無い（@latest も不可。例: npm:pyright@1.1.410）: %q", ft, sc.Mise))
		}
		if sc.AutoInstall == nil {
			sc.AutoInstall = &s.AutoInstall
		}
		if sc.InlayHints == nil {
			sc.InlayHints = &s.InlayHints
		}
	}
	if s.MaxRestarts != nil {
		c.maxRestarts = max(*s.MaxRestarts, 0)
	}
	return c, errors.Join(errs...)
}

// hasMiseVersion は、mise の道具の ID が版（"@" の後ろ）を含むかを返す。
//
// 版を省くと、mise install はその時点の最新版を入れる（PR #281 のレビューで、mise 2026.5.2 の mise install --dry-run で確かめた）。
// 道具の版を固定して、サプライチェーン攻撃に備える方針（#278 の背景）に合わせ、版の無い ID を設定の誤りにする。
// "@latest" も、mise は最新版と読むので、版を固定していない ID として誤りにする（PR #281 の再レビューの推奨 1）。
// "@1" のような版の範囲は、利用者が意図して書くものとして受け付ける。
// npm の scope の "@"（"npm:@vtsls/language-server" の ":" と "/" の直後の "@"）は版の区切りとして数えない。
func hasMiseVersion(tool string) bool {
	i := strings.LastIndexByte(tool, '@')
	return i > 0 && i < len(tool)-1 && tool[i-1] != ':' && tool[i-1] != '/' && tool[i+1:] != "latest"
}

// findRoot は、ファイル path のディレクトリから親へたどり、markers のどれかを含む最初のディレクトリを返す。
// 見つからなければ path のディレクトリを返す。
func findRoot(path string, markers []string) string {
	start := filepath.Dir(path)
	for dir := start; ; {
		for _, m := range markers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}
