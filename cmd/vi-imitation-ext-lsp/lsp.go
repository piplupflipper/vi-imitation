package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// この節は、使う範囲の LSP 3.17 のメッセージの型である。
// 名前と形は LSP 3.17 の仕様（https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/）に合わせる。

// LSP のメソッドの名前。
const (
	lspInitialize           = "initialize"
	lspInitialized          = "initialized"
	lspShutdown             = "shutdown"
	lspExit                 = "exit"
	lspCancelRequest        = "$/cancelRequest"
	lspDidOpen              = "textDocument/didOpen"
	lspDidChange            = "textDocument/didChange"
	lspDidClose             = "textDocument/didClose"
	lspDidSave              = "textDocument/didSave"
	lspPublishDiagnostics   = "textDocument/publishDiagnostics"
	lspDefinition           = "textDocument/definition"
	lspHover                = "textDocument/hover"
	lspFormatting           = "textDocument/formatting"
	lspRangeFormatting      = "textDocument/rangeFormatting"
	lspImplementation       = "textDocument/implementation"
	lspTypeDefinition       = "textDocument/typeDefinition"
	lspReferences           = "textDocument/references"
	lspDocumentSymbol       = "textDocument/documentSymbol"
	lspWorkspaceSymbol      = "workspace/symbol"
	lspRename               = "textDocument/rename"
	lspShowMessage          = "window/showMessage"
	lspLogMessage           = "window/logMessage"
	lspShowMessageRequest   = "window/showMessageRequest"
	lspWorkDoneProgress     = "window/workDoneProgress/create"
	lspProgress             = "$/progress"
	lspConfiguration        = "workspace/configuration"
	lspRegisterCapability   = "client/registerCapability"
	lspUnregisterCapability = "client/unregisterCapability"
	lspApplyEdit            = "workspace/applyEdit"
	lspWorkspaceFolders     = "workspace/workspaceFolders"
	lspInlayHint            = "textDocument/inlayHint"
	lspInlayHintRefresh     = "workspace/inlayHint/refresh"
)

// LSP の TextDocumentSyncKind。
const (
	syncNone        = 0
	syncFull        = 1
	syncIncremental = 2
)

// LSP の MessageType。
const (
	messageError   = 1
	messageWarning = 2
	messageInfo    = 3
)

// LSP の DiagnosticSeverity。
const (
	severityError       = 1
	severityWarning     = 2
	severityInformation = 3
	severityHint        = 4
)

type initializeParams struct {
	ProcessID             int               `json:"processId"`
	ClientInfo            clientInfo        `json:"clientInfo"`
	RootURI               string            `json:"rootUri"`
	InitializationOptions json.RawMessage   `json:"initializationOptions,omitempty"`
	Capabilities          json.RawMessage   `json:"capabilities"`
	WorkspaceFolders      []workspaceFolder `json:"workspaceFolders"`
}

type clientInfo struct {
	Name string `json:"name"`
}

type workspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// clientCapabilities は、initialize で申し出るクライアントの能力である。
// 位置の数え方は utf-8 を先に申し出る（基本設計 6.3）。サーバーが選ばなければ utf-16 で数える。
// window.workDoneProgress を申し出て、サーバーの $/progress を本体の progress/report に写す（client.progress。FR-179）。
// textDocument.inlayHint と workspace.inlayHint.refreshSupport を申し出て、インレイヒントを問い合わせ、
// workspace/inlayHint/refresh に答える（inlay.go。FR-255、FR-256）。inlayHint/resolve は使わないので resolveSupport は申し出ない。
const clientCapabilities = `{
  "general": {"positionEncodings": ["utf-8", "utf-16"]},
  "window": {"workDoneProgress": true},
  "workspace": {
    "configuration": true, "workspaceFolders": true, "symbol": {}, "workspaceEdit": {"documentChanges": true},
    "inlayHint": {"refreshSupport": true}
  },
  "textDocument": {
    "synchronization": {"dynamicRegistration": false, "willSave": false, "willSaveWaitUntil": false, "didSave": true},
    "publishDiagnostics": {"versionSupport": true},
    "hover": {"contentFormat": ["markdown", "plaintext"]},
    "definition": {"linkSupport": true},
    "implementation": {"linkSupport": true},
    "typeDefinition": {"linkSupport": true},
    "references": {},
    "documentSymbol": {"hierarchicalDocumentSymbolSupport": true},
    "rename": {},
    "formatting": {},
    "rangeFormatting": {},
    "inlayHint": {"dynamicRegistration": false}
  }
}`

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
}

type serverCapabilities struct {
	PositionEncoding           string          `json:"positionEncoding"`
	TextDocumentSync           json.RawMessage `json:"textDocumentSync"`
	DefinitionProvider         json.RawMessage `json:"definitionProvider"`
	HoverProvider              json.RawMessage `json:"hoverProvider"`
	DocumentFormattingProvider json.RawMessage `json:"documentFormattingProvider"`
	// 以下は、段階 3 の本体の操作（基本設計 5.5）のために読む。
	DocumentRangeFormattingProvider json.RawMessage `json:"documentRangeFormattingProvider"`
	ImplementationProvider          json.RawMessage `json:"implementationProvider"`
	TypeDefinitionProvider          json.RawMessage `json:"typeDefinitionProvider"`
	ReferencesProvider              json.RawMessage `json:"referencesProvider"`
	DocumentSymbolProvider          json.RawMessage `json:"documentSymbolProvider"`
	WorkspaceSymbolProvider         json.RawMessage `json:"workspaceSymbolProvider"`
	RenameProvider                  json.RawMessage `json:"renameProvider"`
	// InlayHintProvider は、textDocument/inlayHint の能力である（inlay.go）。
	InlayHintProvider json.RawMessage `json:"inlayHintProvider"`
}

// textDocumentSyncOptions は、textDocumentSync を読んだ結果である。
type textDocumentSyncOptions struct {
	OpenClose bool            `json:"openClose"`
	Change    int             `json:"change"`
	Save      json.RawMessage `json:"save"`
}

// syncOptions は、textDocumentSync を読む。
// 数値の形（TextDocumentSyncKind）なら、開く・閉じるの通知を送り、変更はその種類で送り、保存の通知は送らない。
// 省かれていれば何も同期しない（LSP 3.17 の既定は TextDocumentSyncKind.None）。
func (c *serverCapabilities) syncOptions() (opts textDocumentSyncOptions, save, includeText bool) {
	raw := bytes.TrimSpace(c.TextDocumentSync)
	if len(raw) == 0 || string(raw) == "null" {
		return opts, false, false
	}
	if n, err := strconv.Atoi(string(raw)); err == nil {
		return textDocumentSyncOptions{OpenClose: true, Change: n}, false, false
	}
	if err := json.Unmarshal(raw, &opts); err != nil {
		return textDocumentSyncOptions{}, false, false
	}
	switch s := strings.TrimSpace(string(opts.Save)); s {
	case "", "null", "false":
	case "true":
		save = true
	default:
		var so struct {
			IncludeText bool `json:"includeText"`
		}
		_ = json.Unmarshal(opts.Save, &so)
		save, includeText = true, so.IncludeText
	}
	return opts, save, includeText
}

// provides は、xxxProvider の値が、能力があることを表すかどうかを返す。
// 値は boolean か、オプションのオブジェクトである。省略、null、false なら無い。
func provides(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "false"
}

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int64  `json:"version"`
	Text       string `json:"text"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int64  `json:"version"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

// contentChange は、TextDocumentContentChangeEvent である。Range が nil なら Text は全文である。
type contentChange struct {
	Range *lspRange `json:"range,omitempty"`
	Text  string    `json:"text"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []contentChange                 `json:"contentChanges"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type didSaveParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"`
}

type textDocumentPositionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     lspPosition            `json:"position"`
}

type formattingOptions struct {
	TabSize      int  `json:"tabSize"`
	InsertSpaces bool `json:"insertSpaces"`
}

type documentFormattingParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Options      formattingOptions      `json:"options"`
}

type textEdit struct {
	Range   lspRange `json:"range"`
	NewText string   `json:"newText"`
}

type publishDiagnosticsParams struct {
	URI         string          `json:"uri"`
	Version     *int64          `json:"version"`
	Diagnostics []lspDiagnostic `json:"diagnostics"`
}

type lspDiagnostic struct {
	Range    lspRange        `json:"range"`
	Severity int             `json:"severity"`
	Code     json.RawMessage `json:"code"`
	Source   string          `json:"source"`
	Message  string          `json:"message"`
}

// codeString は、Diagnostic の code（integer か string）を文字列にする。無ければ空である。
func codeString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return s
}

type showMessageParams struct {
	Type    int    `json:"type"`
	Message string `json:"message"`
}

// progressParams は、$/progress の params である。Token は integer か string で、そのままの JSON で持つ。
type progressParams struct {
	Token json.RawMessage `json:"token"`
	Value json.RawMessage `json:"value"`
}

// workDoneProgress は、$/progress の value のうち、WorkDoneProgressBegin、WorkDoneProgressReport、WorkDoneProgressEnd の項目である。
// 部分の結果（partialResultToken）の $/progress は、value に kind が無いので見分けられる。
type workDoneProgress struct {
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Message string `json:"message"`
	// Percentage は uinteger だが、小数を送る言語サーバーがあっても読めるように数で受ける。
	Percentage *float64 `json:"percentage"`
}

type configurationParams struct {
	Items []struct {
		Section string `json:"section"`
	} `json:"items"`
}

// location は、Location と LocationLink の両方を読むための型である。
// Location は uri と range を、LocationLink は targetUri、targetRange、targetSelectionRange を持つ。
type location struct {
	URI                  string    `json:"uri"`
	Range                *lspRange `json:"range"`
	TargetURI            string    `json:"targetUri"`
	TargetSelectionRange *lspRange `json:"targetSelectionRange"`
	TargetRange          *lspRange `json:"targetRange"`
}

// target は、移動先の URI と位置を返す。
// LocationLink では、名前などを指す targetSelectionRange の始まりを使い、無ければ targetRange の始まりを使う。
func (l *location) target() (uri string, pos lspPosition, ok bool) {
	switch {
	case l.TargetURI != "" && l.TargetSelectionRange != nil:
		return l.TargetURI, l.TargetSelectionRange.Start, true
	case l.TargetURI != "" && l.TargetRange != nil:
		return l.TargetURI, l.TargetRange.Start, true
	case l.URI != "" && l.Range != nil:
		return l.URI, l.Range.Start, true
	}
	return "", lspPosition{}, false
}

// parseLocations は、textDocument/definition の結果（Location、Location[]、LocationLink[]、null）を読む。
func parseLocations(raw json.RawMessage) ([]location, error) {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0 || string(raw) == "null":
		return nil, nil
	case raw[0] == '[':
		var ls []location
		err := json.Unmarshal(raw, &ls)
		return ls, err
	default:
		var l location
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, err
		}
		return []location{l}, nil
	}
}

// hoverResult は、textDocument/hover の結果である。
type hoverResult struct {
	Contents json.RawMessage `json:"contents"`
}

// markedString は、MarkedString のうちオブジェクトの形（{ language, value }）である。
// MarkupContent（{ kind, value }）も同じ型で読む。
type markedString struct {
	Language string `json:"language"`
	Kind     string `json:"kind"`
	Value    string `json:"value"`
}

// hoverText は、Hover の contents（MarkedString、MarkedString[]、MarkupContent）を、本文と形式にする。
//
// MarkupContent は kind をそのまま形式にする（plaintext か markdown）。
// MarkedString は、文字列なら Markdown、{ language, value } なら Markdown のコードブロックとして扱う（LSP 3.17 の MarkedString の doc）。
// 配列は、各要素を Markdown にして空行でつなぐ。
func hoverText(raw json.RawMessage) (text, format string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	one := func(r json.RawMessage) (string, string) {
		var s string
		if json.Unmarshal(r, &s) == nil {
			return s, "markdown"
		}
		var m markedString
		if json.Unmarshal(r, &m) != nil {
			return "", ""
		}
		if m.Kind != "" {
			if m.Kind == "plaintext" {
				return m.Value, "plaintext"
			}
			return m.Value, "markdown"
		}
		return "```" + m.Language + "\n" + m.Value + "\n```", "markdown"
	}
	if raw[0] != '[' {
		return one(raw)
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return "", ""
	}
	var parts []string
	for _, it := range items {
		if s, _ := one(it); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n"), "markdown"
}

// pathToURI は、絶対パスを file の URI にする。
func pathToURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// uriToPath は、file の URI をパスにする。file の URI でなければ ok は false である。
// 言語サーバーと拡張機能とで URI の符号化の仕方が違ってもよいように、URI どうしではなくパスどうしで比べる（LSP 3.17 の仕様は、互いの符号化の仕方を前提にしないよう求めている）。
func uriToPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", false
	}
	return u.Path, true
}
