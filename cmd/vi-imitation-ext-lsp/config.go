package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

// serverConfig は、1 つの言語サーバーの設定である。
// 利用者の設定のうち LSP の拡張機能に向けた部分（initialize の settings）の servers の値である（基本設計 6.3）。
// servers の鍵が言語サーバーの名前で、受け持つファイルの種類は FileTypes に書く。
// 項目の名前は仮である。
type serverConfig struct {
	// FileTypes は、この言語サーバーが受け持つファイルの種類の一覧である（例: ["typescript", "typescriptreact"]）。
	// 省くと、servers の鍵をファイルの種類の名前として、その種類だけを受け持つ（#451 より前の書き方）。
	// 同じ根のファイルは、ファイルの種類が違っても 1 つのプロセスを共有する（FR-320）。
	FileTypes []string `json:"fileTypes,omitempty"`
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
	// LanguageID は、textDocument/didOpen の languageId である。省くと、開いたファイルの種類の名前を使う（languageID）。
	// FileTypes に複数の種類を書いた言語サーバーでは、省いて種類ごとの名前を送らせる（typescript と typescriptreact など）。
	LanguageID string `json:"languageId,omitempty"`
	// RootMarkers は、ワークスペースの根を探す目印のファイル名である。
	// ファイルを開くたびに、そのファイルのディレクトリから親へたどり、目印のどれかがあるディレクトリを根にする。
	// 見つからなければ、そのファイルのディレクトリを根にする。根が違えば、別のプロセスを起動する（FR-320）。
	// コマンド lsp.root で指定した根は、目印より優先する（FR-322）。
	RootMarkers []string `json:"rootMarkers,omitempty"`
	// PythonVenvSetting は、根の Python の仮想環境の python（bin/python）の絶対パスを入れる、Settings の中の場所である。
	// section の名前と項目の名前を "." でつないで書く（pyright なら "python.pythonPath"）。
	// 仮想環境は、根から ext/project の PythonVenv で探す（親へたどるので、uv のワークスペースのメンバーから根の .venv に届く）。
	// 見つかれば、Settings に同じ場所の値があっても置き換える。見つからなければ入れない。省くと探さない（FR-321）。
	PythonVenvSetting string `json:"pythonVenvSetting,omitempty"`
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

// languageID は、ファイルの種類 fileType の文書の textDocument/didOpen の languageId を返す。
// 設定の LanguageID があればそれ、無ければファイルの種類の名前である。
//
// req: FR-320
func (c *serverConfig) languageID(fileType string) string {
	if c.LanguageID != "" {
		return c.LanguageID
	}
	return fileType
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
	// Servers は、言語サーバーの名前から言語サーバーの設定への対応である。
	// 利用者の設定は、既定（defaultServers）に、名前ごとに上書きする。値を null にすると、その名前の既定の言語サーバーを使わない。
	// 利用者の言語サーバーが、既定の言語サーバーと同じファイルの種類を受け持つときは、その種類を既定の言語サーバーから外す
	// （fileTypes に "go" を書いた "gopls" を足すと、既定の "go" を使わない）。
	// 利用者の言語サーバーどうしが同じファイルの種類を受け持つのは、設定の誤りにする。
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
// 名前は、#451 より前の書き方（鍵がファイルの種類）で servers.go を書いた設定が既定を置き換えられるように、"go" のままにする。
func defaultServers() map[string]*serverConfig {
	return map[string]*serverConfig{
		"go": {
			Command:     "gopls",
			FileTypes:   []string{"go"},
			LanguageID:  "go",
			RootMarkers: []string{"go.work", "go.mod", ".git"},
		},
	}
}

// config は、設定を読んで既定値を埋めた後の値である。
type config struct {
	// servers は、言語サーバーの名前から設定への対応である。
	servers map[string]*serverConfig
	// byFileType は、ファイルの種類の名前から、それを受け持つ言語サーバーの名前への対応である。
	byFileType  map[string]string
	maxRestarts int
}

// parseSettings は、initialize の settings を読み、既定値を埋めた設定を返す。
// raw が空なら既定値だけの設定を返す。
func parseSettings(raw json.RawMessage) (*config, error) {
	c := &config{servers: defaultServers(), maxRestarts: defaultMaxRestarts}
	var s settings
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("LSP の拡張機能の設定を読めない: %w", err)
		}
	}
	maps.Copy(c.servers, s.Servers)
	var errs []error
	for name, sc := range c.servers {
		if sc == nil {
			delete(c.servers, name)
			continue
		}
		if sc.Command == "" {
			errs = append(errs, fmt.Errorf("servers.%s.command が空である", name))
		}
		if len(sc.FileTypes) == 0 {
			sc.FileTypes = []string{name}
		}
		// mise の引数に置くので、オプションと読まれる形と空白を含む形は受け付けない。
		switch {
		case sc.Mise == "":
		case strings.HasPrefix(sc.Mise, "-") || strings.ContainsFunc(sc.Mise, unicode.IsSpace):
			errs = append(errs, fmt.Errorf("servers.%s.mise は mise の道具の ID（例: npm:pyright@1.1.410）でない: %q", name, sc.Mise))
		case !hasMiseVersion(sc.Mise):
			errs = append(errs, fmt.Errorf("servers.%s.mise に固定した版が無い（@latest も不可。例: npm:pyright@1.1.410）: %q", name, sc.Mise))
		}
		if sc.PythonVenvSetting != "" {
			if parts := strings.Split(sc.PythonVenvSetting, "."); len(parts) < 2 || slices.Contains(parts, "") {
				errs = append(errs, fmt.Errorf("servers.%s.pythonVenvSetting は section と項目を . でつないだ形（例: python.pythonPath）でない: %q", name, sc.PythonVenvSetting))
			}
		}
		if sc.AutoInstall == nil {
			sc.AutoInstall = &s.AutoInstall
		}
		if sc.InlayHints == nil {
			sc.InlayHints = &s.InlayHints
		}
	}
	byFT, err := assignFileTypes(c.servers, s.Servers)
	if err != nil {
		errs = append(errs, err)
	}
	c.byFileType = byFT
	if s.MaxRestarts != nil {
		c.maxRestarts = max(*s.MaxRestarts, 0)
	}
	return c, errors.Join(errs...)
}

// assignFileTypes は、ファイルの種類ごとに受け持つ言語サーバーを決め、ファイルの種類から言語サーバーの名前への対応を返す。
// user は利用者が書いた servers である。
//
// 利用者の言語サーバーが受け持つ種類は、既定の言語サーバー（利用者が同じ名前で書いていないもの）から外す。
// 受け持つ種類が無くなった既定の言語サーバーは servers から消す。
// 利用者の言語サーバーどうしが同じ種類を受け持てば誤りを返す。名前の順に調べるので、誤りの文言は決まった形になる。
//
// req: FR-320
func assignFileTypes(servers, user map[string]*serverConfig) (map[string]string, error) {
	byFT := make(map[string]string)
	var errs []error
	isUser := func(name string) bool { _, ok := user[name]; return ok }
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		if !isUser(name) {
			continue
		}
		for _, ft := range servers[name].FileTypes {
			if other, ok := byFT[ft]; ok && other != name {
				errs = append(errs, fmt.Errorf("servers.%s と servers.%s が同じファイルの種類 %s を受け持つ", other, name, ft))
				continue
			}
			byFT[ft] = name
		}
	}
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		if isUser(name) {
			continue
		}
		sc := servers[name]
		sc.FileTypes = slices.DeleteFunc(slices.Clone(sc.FileTypes), func(ft string) bool { _, ok := byFT[ft]; return ok })
		if len(sc.FileTypes) == 0 {
			delete(servers, name)
			continue
		}
		for _, ft := range sc.FileTypes {
			byFT[ft] = name
		}
	}
	return byFT, errors.Join(errs...)
}

// effectiveSettings は、言語サーバーの Settings に、仮想環境の python（venvPython。空なら入れない）を
// PythonVenvSetting の場所へ入れたものを返す。Settings は変えない。
//
// req: FR-321
func (c *serverConfig) effectiveSettings(venvPython string) (map[string]json.RawMessage, error) {
	if c.PythonVenvSetting == "" || venvPython == "" {
		return c.Settings, nil
	}
	out := maps.Clone(c.Settings)
	if out == nil {
		out = make(map[string]json.RawMessage)
	}
	parts := strings.Split(c.PythonVenvSetting, ".")
	section := make(map[string]any)
	if raw, ok := out[parts[0]]; ok && string(raw) != "null" {
		if err := decodeJSON(raw, &section); err != nil {
			return nil, fmt.Errorf("settings.%s がオブジェクトでないので、%s に仮想環境の python を入れられない: %w", parts[0], c.PythonVenvSetting, err)
		}
	}
	m := section
	for i, p := range parts[1 : len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			if m[p] != nil {
				return nil, fmt.Errorf("settings.%s がオブジェクトでないので、%s に仮想環境の python を入れられない",
					strings.Join(parts[:i+2], "."), c.PythonVenvSetting)
			}
			next = make(map[string]any)
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = venvPython
	data, err := json.Marshal(section)
	if err != nil {
		return nil, err
	}
	out[parts[0]] = data
	return out, nil
}

// lookupSection は、workspace/configuration の問い合わせの section に答える値を settings から引く。
// 見つからなければ null を返す。section の無い問い合わせ（空）にも null を返す。
//
// settings に section と同じ名前の鍵があれば、その値を返す。
// 無ければ、section を "." で区切り、前の部分の鍵の値（オブジェクト）を残りの名前でたどる
// （"python.analysis" を、settings の python の analysis から引く。Neovim の vim.lsp の workspace/configuration の答え方と同じ）。
// 区切りの位置は、長い前の部分を先に試す。
//
// req: FR-321
func lookupSection(settings map[string]json.RawMessage, section string) json.RawMessage {
	null := json.RawMessage("null")
	if section == "" {
		return null
	}
	if v, ok := settings[section]; ok {
		return v
	}
	parts := strings.Split(section, ".")
	for i := len(parts) - 1; i >= 1; i-- {
		raw, ok := settings[strings.Join(parts[:i], ".")]
		if !ok {
			continue
		}
		var v any
		if decodeJSON(raw, &v) != nil {
			continue
		}
		found := true
		for _, p := range parts[i:] {
			m, ok := v.(map[string]any)
			if !ok {
				found = false
				break
			}
			if v, ok = m[p]; !ok {
				found = false
				break
			}
		}
		if !found {
			continue
		}
		data, err := json.Marshal(v)
		if err != nil {
			continue
		}
		return data
	}
	return null
}

// decodeJSON は、数を float64 にせずに（json.Number で）読む。値を書き戻すときに大きな整数の桁を失わないためである。
func decodeJSON(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
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
