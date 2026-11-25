package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext/expand"
)

// この節は、設定（initialize の settings）の読み方である（基本設計 6.4 の「#399 の DAP の拡張機能」の「設定の形」。FR-279、FR-280）。
//
// 設定は、デバッグアダプターの表 adapters と、ファイルの種類ごとの構成の一覧 configurations を持つ。
//
//	{
//	  "adapters": {
//	    "go": { "transport": "clientSocket", "command": "dlv", "args": ["dap", "--client-addr=${clientAddr}"], "cwd": "${fileDirname}" },
//	    "python": { "transport": "stdio", "command": "debugpy-adapter", "aliases": ["debugpy"], "connectFrom": "connect" },
//	    "pwa-node": { "transport": "serverPort", "command": "node", "args": ["/path/to/dapDebugServer.js", "${port}"] },
//	    "remote-go": { "transport": "server", "host": "127.0.0.1", "port": 40000 }
//	  },
//	  "configurations": {
//	    "go": [{ "name": "パッケージ", "type": "go", "request": "launch", "mode": "debug", "program": "${fileDirname}" }]
//	  }
//	}
//
// 今までの 1 組の設定（adapter、adapterID、launch）も読み続ける（legacySettings）。
// ${input:ID} の問いの表 inputs（FR-298）は、.vscode/launch.json の inputs と同じ形で読む（inputs.go）。
// ペインと区画の項目 ui（FR-108、FR-277、FR-278）は uiSettings で読む（pane.go）。

// トランスポート（アダプターのつなぎ方。FR-279）の名前である。
const (
	// transportStdio は、拡張機能がアダプターを起動し、その標準入出力で話す。
	transportStdio = "stdio"
	// transportClientSocket は、拡張機能が Unix ドメインソケットで待ち受け、起動したアダプターに接続させる（dlv dap --client-addr）。
	transportClientSocket = "clientSocket"
	// transportServerPort は、拡張機能が空いたポートを決めてアダプターを起動し、アダプターが待ち受けたポートへ接続する
	// （vscode-js-debug の dapDebugServer.js）。
	transportServerPort = "serverPort"
	// transportServer は、既に動いているアダプターの host と port へ接続する。拡張機能は起動しない。
	transportServer = "server"
)

// launchKeys は、アダプターの起動の方法の項目である。構成と一緒に変数を展開し、外部のコマンドが置き換えられる（FR-282）。
var launchKeys = []string{"command", "args", "env", "cwd", "host", "port"}

// adapterDef は、設定の adapters の 1 項目（デバッグアダプターの起動の方法）である。
type adapterDef struct {
	// name は、表のキーである。
	name string
	// transport は、つなぎ方（transportStdio など）である。
	transport string
	// adapterID は、initialize の adapterID である。省けば name。
	adapterID string
	// connectFrom は、構成のキーの名前である。値を当て終えた構成がそのキーのオブジェクトを持てば、transport に依らず、
	// アダプターを起動せずにそのオブジェクトの host と port へ直に繋ぐ（FR-279）。空なら使わない。
	connectFrom string
	// launch は、command、args、env、cwd、host、port のうち書いた項目である。値は JSON の値で、変数を含みうる（展開の前）。
	launch map[string]any
}

// settings は、利用者の設定のうち、この拡張機能に向けた部分（initialize の settings）を読んだものである。
type settings struct {
	// adapters は、アダプターの名前（aliases を含む）から、アダプターへの対応である。
	adapters map[string]*adapterDef
	// configurations は、ファイルの種類から、構成の列への対応である。構成は JSON のオブジェクトで、書いた順に並ぶ。
	configurations map[string][]map[string]any
	// written は、configurations と同じ並びの、構成ごとの利用者に聞く変数の書かれた順である（writtenVars）。
	written map[string][][]expand.Var
	// inputs は、設定の ${input:ID} の問いの表である（FR-298）。
	inputs inputTable
	// legacy は、今までの 1 組の設定の構成である。adapters か configurations を書き、1 組の設定を書かなければ nil。
	legacy *legacySettings
	// ui は、ペインと区画の設定である（FR-108、FR-277、FR-278）。
	ui uiSettings
}

// legacySettings は、今までの 1 組の設定（adapter、adapterID、launch）である。
// 名前を「既定」、種類をその 1 組のアダプター、request を launch にした構成 1 つとして読み、どのファイルの種類でも候補にする（基本設計 6.4）。
//
// 1 組の設定の構成は、今までの振る舞いを保つため、ほかの構成に無い補いを持つ。
// launch に program が無ければ、デバッグを始めたバッファのファイルのあるディレクトリを program にする。
// adapter に cwd が無ければ、同じディレクトリをアダプターの作業ディレクトリにする。
// dlv dap は、program のビルドを自分の作業ディレクトリで行い、本体の作業ディレクトリが program のモジュールの外にあると
// ビルドに失敗する（2026-11-16 に dlv 1.27.2 で確かめた。https://github.com/go-delve/delve/blob/master/Documentation/usage/dlv_dap.md）。
type legacySettings struct {
	adapter *adapterDef
	launch  map[string]any
}

// legacyAdapter は、今までの 1 組の設定の adapter の項目である。
type legacyAdapter struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
}

// defaultLegacy は、設定を何も書かなかったときの構成である。最初のデバッグアダプターは delve（dlv dap）にする（#51）。
// mode の debug は、program のパッケージをビルドしてデバッグする delve の launch の種類である
// （https://github.com/go-delve/delve/blob/master/Documentation/api/dap/README.md）。
func defaultLegacy() *legacySettings {
	return &legacySettings{
		adapter: &adapterDef{
			name:      "go",
			transport: transportClientSocket,
			adapterID: "go",
			launch:    map[string]any{"command": "dlv", "args": []any{"dap", "--client-addr=" + clientAddrPlaceholder}},
		},
		launch: map[string]any{"mode": "debug"},
	}
}

// defaultSettings は、設定を省いたときの値である。
func defaultSettings() settings {
	return settings{
		adapters: map[string]*adapterDef{}, configurations: map[string][]map[string]any{}, written: map[string][][]expand.Var{},
		legacy: defaultLegacy(), ui: defaultUISettings(),
	}
}

// decodeJSON は、data を UseNumber で any へ読む（2^53 を超える整数を丸めないため。ext/expand の doc）。
func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("extra data after the JSON value")
	}
	return v, nil
}

// parseSettings は、設定を読む。空か null なら既定の値を返す。
// 形の誤り（知らない transport、文字列でない command など）は、項目の名前を添えた誤りにする。
//
// req: FR-279, FR-280
func parseSettings(data []byte) (settings, error) {
	s := defaultSettings()
	if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
		return s, nil
	}
	v, err := decodeJSON(data)
	if err != nil {
		return s, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return s, errors.New("settings is not an object")
	}
	_, hasAdapters := top["adapters"]
	_, hasConfigs := top["configurations"]
	_, hasAdapter := top["adapter"]
	_, hasAdapterID := top["adapterID"]
	_, hasLaunch := top["launch"]
	if hasAdapters || hasConfigs {
		s.legacy = nil
	}
	if hasAdapter || hasAdapterID || hasLaunch {
		l, err := parseLegacy(top)
		if err != nil {
			return s, err
		}
		s.legacy = l
	}
	if raw, ok := top["adapters"]; ok {
		obj, ok := raw.(map[string]any)
		if !ok {
			return s, errors.New("adapters is not an object")
		}
		for _, name := range slices.Sorted(maps.Keys(obj)) {
			a, aliases, err := parseAdapter(name, obj[name])
			if err != nil {
				return s, err
			}
			for _, n := range append([]string{name}, aliases...) {
				if prev, dup := s.adapters[n]; dup && prev.name != name {
					return s, fmt.Errorf("adapters.%s: the name %q is also used by adapters.%s", name, n, prev.name)
				}
				s.adapters[n] = a
			}
		}
	}
	if raw, ok := top["configurations"]; ok {
		obj, ok := raw.(map[string]any)
		if !ok {
			return s, errors.New("configurations is not an object")
		}
		for ft, list := range obj {
			arr, ok := list.([]any)
			if !ok {
				return s, fmt.Errorf("configurations.%s is not an array", ft)
			}
			for i, c := range arr {
				m, ok := c.(map[string]any)
				if !ok {
					return s, fmt.Errorf("configurations.%s[%d] is not an object", ft, i)
				}
				s.configurations[ft] = append(s.configurations[ft], m)
			}
		}
	}
	inputs, ierr := parseInputs(top["inputs"], true)
	if ierr != nil {
		return s, ierr
	}
	s.inputs = inputs
	if raw, ok := top["ui"]; ok && raw != nil {
		ui, err := parseUISettings(raw)
		if err != nil {
			return s, err
		}
		s.ui = ui
	}
	var raws struct {
		Configurations map[string][]json.RawMessage `json:"configurations"`
	}
	if json.Unmarshal(data, &raws) == nil {
		for ft, list := range raws.Configurations {
			for _, r := range list {
				s.written[ft] = append(s.written[ft], writtenVars(r))
			}
		}
	}
	return s, nil
}

// parseLegacy は、今までの 1 組の設定を読む。省いた項目は defaultLegacy の値にする。
func parseLegacy(top map[string]any) (*legacySettings, error) {
	l := defaultLegacy()
	if raw, ok := top["adapter"]; ok && raw != nil {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var a legacyAdapter
		if err := json.Unmarshal(b, &a); err != nil {
			return nil, fmt.Errorf("adapter: %w", err)
		}
		if a.Command != "" {
			launch := map[string]any{"command": a.Command}
			transport := transportStdio
			if len(a.Args) > 0 {
				args := make([]any, len(a.Args))
				for i, x := range a.Args {
					args[i] = x
					// 今までは、引数に ${clientAddr} を含めるとソケットで話した（session.go の spawn）。
					if strings.Contains(x, clientAddrPlaceholder) {
						transport = transportClientSocket
					}
				}
				launch["args"] = args
			}
			if a.Cwd != "" {
				launch["cwd"] = a.Cwd
			}
			l.adapter.launch = launch
			l.adapter.transport = transport
		}
	}
	if raw, ok := top["adapterID"]; ok && raw != nil {
		id, ok := raw.(string)
		if !ok {
			return nil, errors.New("adapterID is not a string")
		}
		if id != "" {
			l.adapter.adapterID = id
			l.adapter.name = id
		}
	}
	if raw, ok := top["launch"]; ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("launch is not an object")
		}
		l.launch = m
	}
	return l, nil
}

// parseAdapter は、adapters の 1 項目を読み、アダプターと aliases を返す。
func parseAdapter(name string, raw any) (*adapterDef, []string, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("adapters.%s is not an object", name)
	}
	a := &adapterDef{name: name, adapterID: name, transport: transportStdio, launch: map[string]any{}}
	str := func(key string) (string, error) {
		v, ok := obj[key]
		if !ok || v == nil {
			return "", nil
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("adapters.%s.%s is not a string", name, key)
		}
		return s, nil
	}
	var err error
	if t, e := str("transport"); e != nil {
		return nil, nil, e
	} else if t != "" {
		a.transport = t
	}
	switch a.transport {
	case transportStdio, transportClientSocket, transportServerPort, transportServer:
	default:
		return nil, nil, fmt.Errorf("adapters.%s.transport: unknown transport %q (stdio, clientSocket, serverPort, server)", name, a.transport)
	}
	if id, e := str("adapterID"); e != nil {
		return nil, nil, e
	} else if id != "" {
		a.adapterID = id
	}
	if a.connectFrom, err = str("connectFrom"); err != nil {
		return nil, nil, err
	}
	var aliases []string
	if v, ok := obj["aliases"]; ok && v != nil {
		arr, ok := v.([]any)
		if !ok {
			return nil, nil, fmt.Errorf("adapters.%s.aliases is not an array", name)
		}
		for _, x := range arr {
			s, ok := x.(string)
			if !ok || s == "" {
				return nil, nil, fmt.Errorf("adapters.%s.aliases has a non-string or empty name", name)
			}
			aliases = append(aliases, s)
		}
	}
	for _, key := range []string{"command", "cwd", "host"} {
		if _, err := str(key); err != nil {
			return nil, nil, err
		}
	}
	if v, ok := obj["args"]; ok && v != nil {
		arr, ok := v.([]any)
		if !ok || slices.ContainsFunc(arr, func(x any) bool { _, s := x.(string); return !s }) {
			return nil, nil, fmt.Errorf("adapters.%s.args is not an array of strings", name)
		}
	}
	if v, ok := obj["env"]; ok && v != nil {
		m, ok := v.(map[string]any)
		if !ok || slices.ContainsFunc(slices.Collect(maps.Values(m)), func(x any) bool { _, s := x.(string); return !s }) {
			return nil, nil, fmt.Errorf("adapters.%s.env is not an object of strings", name)
		}
	}
	if v, ok := obj["port"]; ok && v != nil {
		switch v.(type) {
		case json.Number, string:
		default:
			return nil, nil, fmt.Errorf("adapters.%s.port is not a number or a string", name)
		}
	}
	for _, key := range launchKeys {
		if v, ok := obj[key]; ok && v != nil {
			a.launch[key] = v
		}
	}
	switch a.transport {
	case transportServer:
		if _, ok := a.launch["port"]; !ok {
			return nil, nil, fmt.Errorf("adapters.%s: transport server needs port", name)
		}
	default:
		if c, _ := a.launch["command"].(string); c == "" {
			return nil, nil, fmt.Errorf("adapters.%s: transport %s needs command", name, a.transport)
		}
	}
	return a, aliases, nil
}

// 構成の出どころ（FR-283 の候補の一覧の出どころ）である。一覧の項目の `<名前>  (<出どころ>)` に出す。
const (
	sourceLaunchJSON = "launch.json"
	sourceSettings   = "設定"
	sourceDefault    = "既定"
)

// candidate は、デバッグを始めるときの構成の候補 1 つである。
type candidate struct {
	// name は、構成の name である。
	name string
	// source は、出どころ（sourceLaunchJSON など）である。
	source string
	// config は、構成（JSON のオブジェクト）である。書き換えない（使うときは deepCopy する）。
	config map[string]any
	// adapter は、1 組の設定の構成のときのアダプターである。ほかの出どころでは nil で、構成の type で設定の adapters から引く。
	adapter *adapterDef
	// legacy は、1 組の設定の構成であることを表す（legacySettings の補い）。
	legacy bool
	// folder は、.vscode/launch.json の構成のときの ${workspaceFolder}（.vscode のあるディレクトリ）である。ほかの出どころでは空。
	folder string
	// inputs は、.vscode/launch.json の構成のときの、その launch.json の inputs である。ほかの出どころでは nil。
	inputs inputTable
	// written は、構成に利用者に聞く変数が書かれた順である（聞く順。FR-298）。1 組の設定の構成では nil。
	written []expand.Var
}

// label は、一覧の項目の文字列である。
func (c candidate) label() string { return c.name + "  (" + c.source + ")" }

// legacyCandidate は、1 組の設定を候補にする。
func (l *legacySettings) candidate() candidate {
	cfg := deepCopy(l.launch).(map[string]any)
	cfg["name"] = sourceDefault
	cfg["type"] = l.adapter.name
	cfg["request"] = "launch"
	return candidate{name: sourceDefault, source: sourceDefault, config: cfg, adapter: l.adapter, legacy: true}
}

// settingsCandidates は、設定の configurations のうち、ファイルの種類 fileType の構成を、書いた順に候補にする。
func (s *settings) settingsCandidates(fileType string) []candidate {
	var out []candidate
	for i, c := range s.configurations[fileType] {
		name, _ := c["name"].(string)
		var w []expand.Var
		if ws := s.written[fileType]; i < len(ws) {
			w = ws[i]
		}
		out = append(out, candidate{name: name, source: sourceSettings, config: c, written: w})
	}
	return out
}

// lookupAdapter は、構成の候補 c のアダプターを返す。1 組の設定の構成なら、そのアダプターである。
// ほかの構成は、type で設定の adapters（aliases を含む）から引く。
func (s *settings) lookupAdapter(c candidate) (*adapterDef, error) {
	if c.adapter != nil {
		return c.adapter, nil
	}
	t, _ := c.config["type"].(string)
	if t == "" {
		return nil, fmt.Errorf("構成 %s に type が無い", c.name)
	}
	a, ok := s.adapters[t]
	if !ok {
		return nil, fmt.Errorf("構成 %s の type %s のアダプターが設定の adapters に無い", c.name, t)
	}
	return a, nil
}

// deepCopy は、JSON の値（map[string]any、[]any とそれ以外の値）の写しを作る。
func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = deepCopy(e)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = deepCopy(e)
		}
		return a
	default:
		return v
	}
}
