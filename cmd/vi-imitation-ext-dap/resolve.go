package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/expand"
	"github.com/piplupflipper/vi-imitation/ext/project"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// この節は、デバッグを始めるときに構成を選んで値を決める手順である（基本設計 6.4 の「構成の出どころと値の当て方」と「開始の流れ」の 2〜5。
// FR-280〜FR-283、FR-288、FR-299）。
// 決めた結果（runSpec）は、つなぐ段階（transport.go の spawn）と debug.runLast と debug.restart が使う。
// 手順は、セッションに閉じた値（invocation と runSpec）だけを読み書きし、debugger の今のセッションに寄りかからない
// （子のセッションを足す後の issue のため）。

// invocation は、デバッグを始める操作を受けたときの文脈である。ハンドラーの中で、バッファの複製から作る。
type invocation struct {
	// file は、カーソルのあるバッファのファイルの絶対パスである。ファイルに対応しなければ空。
	file string
	// fileType は、そのバッファのファイルの種類である。
	fileType string
	// line と column は、カーソルの位置を 1 から数えたものである。列は文字の数で数える（${lineNumber}、${columnNumber}）。
	line, column int
}

// newInvocation は、provider/invoke のバッファ b（nil もありうる）とカーソルから文脈を作る。
func newInvocation(b *server.Buffer, p *ext.ProviderInvokeParams) invocation {
	inv := invocation{line: p.Cursor.Line + 1, column: 1}
	if b == nil {
		return inv
	}
	inv.file = b.Path()
	inv.fileType = b.FileType()
	if p.Cursor.Line >= 0 && p.Cursor.Line < b.LineCount() {
		text := b.Line(p.Cursor.Line)
		n := min(max(p.Cursor.Byte, 0), len(text))
		inv.column = utf8.RuneCountInString(text[:n]) + 1
	}
	return inv
}

// endpoint は、TCP でつなぐ先である。
type endpoint struct {
	host string
	port int
}

func (e endpoint) String() string { return net.JoinHostPort(e.host, strconv.Itoa(e.port)) }

// adapterRun は、値を決め終えたアダプターの起動の方法である。
type adapterRun struct {
	// name は、アダプターの表のキーである。
	name      string
	transport string
	adapterID string
	// launch は、command、args、env、cwd、host、port である。${port} と ${clientAddr} は expand.Text で残す（つなぐ段階で置き換える）。
	launch map[string]any
	// direct は、構成が connectFrom のキーを持つときのつなぐ先である。あれば、アダプターを起動せずにここへ直に繋ぐ。
	direct *endpoint
}

// runSpec は、選んで値を決め終えた構成と、アダプターの起動の方法である（debug.runLast と debug.restart の「前回の設定」）。
// 書き換えない。
type runSpec struct {
	name string
	// request は、launch か attach である。
	request string
	// config は、アダプターへ launch か attach の引数として送る構成である（拡張機能が読む項目を除いたもの）。
	config  map[string]any
	adapter adapterRun
}

// startError は、始めなかった理由を利用者へ知らせる誤りである。cancelled なら、利用者が取り消した。
type startError struct {
	msg       string
	cancelled bool
}

func (e *startError) Error() string { return e.msg }

func failStart(format string, args ...any) error {
	return &startError{msg: fmt.Sprintf(format, args...)}
}

// errStartCancelled は、利用者が選択か問いを取り消したか、debug.terminate で準備を取り消したことを表す。
var errStartCancelled = &startError{msg: "デバッグの開始を取り消した", cancelled: true}

// ignorableKeys は、動きを変えずに無視できる、扱えない項目である（FR-281）。選んだ構成が含めば、続けるかを聞く。
var ignorableKeys = []string{"preLaunchTask", "postDebugTask", "serverReadyAction"}

// prepare は、デバッグを始めるために、構成を選び、値を決める（基本設計 6.4 の開始の流れの 2〜5）。
// 利用者に聞く（一覧、choice/ask、${pickProcess}）ので、ext/server のループの外の goroutine から呼ぶ。
// ctx は debug.terminate と shutdown で取り消す。
// 始めないときは *startError を返す。
//
// req: FR-280, FR-281, FR-283, FR-288
func (d *debugger) prepare(ctx context.Context, inv invocation) (*runSpec, error) {
	workDir := d.workDir(ctx)
	cands := d.candidates(inv, workDir)
	var cand candidate
	switch len(cands) {
	case 0:
		return nil, failStart("デバッグの構成が無い（ファイルの種類 %q。設定の configurations か .vscode/launch.json に書く）", inv.fileType)
	case 1:
		cand = cands[0]
	default:
		items := make([]ext.ViewItem, len(cands))
		for i, c := range cands {
			items[i] = ext.ViewItem{ID: strconv.Itoa(i), Label: c.label()}
		}
		id, err := d.pick(ctx, "デバッグの構成", items)
		if err != nil {
			return nil, pickError(err)
		}
		i, err := strconv.Atoi(id)
		if err != nil || i < 0 || i >= len(cands) {
			return nil, failStart("選んだ構成が分からない: %q", id)
		}
		cand = cands[i]
	}
	return d.resolveCandidate(ctx, inv, workDir, cand)
}

// pickError は、一覧と問いの誤りを、始めない理由にする。閉じたか取り消したなら errStartCancelled にする。
func pickError(err error) error {
	if errors.Is(err, errPickCancelled) || errors.Is(err, context.Canceled) {
		return errStartCancelled
	}
	var se *startError
	if errors.As(err, &se) {
		return se
	}
	return failStart("%v", err)
}

// workDir は、操作中の client の作業ディレクトリ（editor/state の workDir）を返す。分からなければ拡張機能の作業ディレクトリにする。
func (d *debugger) workDir(ctx context.Context) string {
	rctx, cancel := context.WithTimeout(ctx, d.timeouts.request)
	defer cancel()
	if st, err := d.srv.EditorState(rctx); err == nil && st.WorkDir != "" {
		return st.WorkDir
	} else if err != nil {
		d.logf("editor/state: %v", err)
	}
	wd, _ := os.Getwd()
	return wd
}

// candidates は、構成の候補を、.vscode/launch.json の構成（書いた順）、設定のファイルの種類の構成（書いた順）、
// 今までの 1 組の設定の順に並べる（FR-283）。
// launch.json は始めるたびに読み直す。読めなければ、その誤りを知らせて、設定の構成だけを候補にする。
//
// req: FR-280, FR-281, FR-283
func (d *debugger) candidates(inv invocation, workDir string) []candidate {
	var out []candidate
	start := inv.file
	if start == "" {
		start = workDir
	}
	folder, err := findLaunchJSON(start)
	if err != nil {
		d.showError(fmt.Sprintf(".vscode/launch.json を探せない: %v", err))
	}
	if folder != "" {
		lf, err := readLaunchJSON(folder)
		switch {
		case err != nil:
			d.showError(fmt.Sprintf("launch.json を読めない（設定の構成だけを候補にする）: %v", err))
		case lf != nil:
			if lf.hasCompounds {
				path := filepath.Join(folder, launchJSONPath)
				d.mu.Lock()
				first := !d.compoundsShown[path]
				d.compoundsShown[path] = true
				d.mu.Unlock()
				if first {
					d.showInfo(fmt.Sprintf("%s の compounds は扱えないので、候補に出さない", path))
				}
			}
			for _, c := range lf.configurations {
				name, _ := c["name"].(string)
				out = append(out, candidate{name: name, source: sourceLaunchJSON, config: c, folder: folder})
			}
		}
	}
	d.mu.Lock()
	s := d.settings
	d.mu.Unlock()
	out = append(out, s.settingsCandidates(inv.fileType)...)
	if s.legacy != nil {
		out = append(out, s.legacy.candidate())
	}
	return out
}

// workspaceFolder は、設定の構成と 1 組の設定の構成の ${workspaceFolder} を決める。
// バッファのファイル（無ければ作業ディレクトリ）の Git の根（ext/project の GitRoot）で、Git の作業ツリーの外なら
// 操作中の client の作業ディレクトリにする（仮。基本設計 10 章の未決を、この形に決めた）。
func workspaceFolder(inv invocation, workDir string) string {
	start := inv.file
	if start == "" {
		start = workDir
	}
	if root, err := project.GitRoot(start); err == nil {
		return root
	}
	return workDir
}

// resolveCandidate は、選んだ候補 cand の値を、構成に書いた値、linux の上書き、変数の展開、envFile、resolveCommand の順に当てて決める（FR-283）。
func (d *debugger) resolveCandidate(ctx context.Context, inv invocation, workDir string, cand candidate) (*runSpec, error) {
	d.mu.Lock()
	s := d.settings
	d.mu.Unlock()
	a, err := s.lookupAdapter(cand)
	if err != nil {
		return nil, failStart("%v", err)
	}
	cfg := deepCopy(cand.config).(map[string]any)
	request, _ := cfg["request"].(string)
	if request != "launch" && request != "attach" {
		return nil, failStart("構成 %s の request が launch か attach でない: %v", cand.name, cfg["request"])
	}
	// 2. linux の上書き（.vscode/launch.json の構成だけ）。
	linuxType := false
	if cand.source == sourceLaunchJSON {
		linuxType = applyPlatform(cfg)
		// resolveCommand は利用者の設定の構成にだけ書ける。リポジトリの launch.json に書かれた外部のコマンドは、
		// 構成を選んだだけで動くので、動かさずに始めない（#408 のレビューの裁定要 1 へのオーケストレータの決め）。
		if _, ok := cfg["resolveCommand"]; ok {
			return nil, failStart("構成 %s を始めない: resolveCommand は .vscode/launch.json では扱えない（利用者の設定の構成にだけ書ける）", cand.name)
		}
	}
	folder := cand.folder
	if folder == "" {
		folder = workspaceFolder(inv, workDir)
	}

	// 3. 変数の展開。構成とアダプターの起動の方法の全体を 1 回の展開にする。
	unit := map[string]any{"configuration": cfg, "adapter": deepCopy(a.launch)}
	r := d.resolver(ctx, inv, folder)
	if err = expand.Check(unit, r); err != nil {
		return nil, failStart("構成 %s を始めない: %v", cand.name, err)
	}
	var ignored []string
	for _, k := range ignorableKeys {
		if _, ok := cfg[k]; ok {
			ignored = append(ignored, k)
		}
	}
	if linuxType {
		ignored = append(ignored, "linux.type")
	}
	if len(ignored) > 0 {
		if err = d.askIgnore(ctx, cand.name, ignored); err != nil {
			return nil, err
		}
	}
	if c, _ := cfg["console"].(string); c == "integratedTerminal" || c == "externalTerminal" {
		cfg["console"] = "internalConsole"
		d.showInfo(fmt.Sprintf("構成 %s の console %s は扱えないので、internalConsole に替えて送る", cand.name, c))
	}
	cfgBefore := deepCopy(cfg)
	v, err := expand.Expand(unit, r)
	if err != nil {
		// expand.Error の Unwrap は変数ごとの理由を返すので、${pickProcess} の取り消しを見分けられる。
		if errors.Is(err, errPickCancelled) || errors.Is(err, context.Canceled) {
			return nil, errStartCancelled
		}
		return nil, failStart("構成 %s を始めない: %v", cand.name, err)
	}
	unit = v.(map[string]any)
	cfg = numberWholeVar(unit["configuration"], cfgBefore, "pickProcess").(map[string]any)
	launch := unit["adapter"].(map[string]any)

	// 4. envFile。
	if err = applyEnvFile(cfg, folder); err != nil {
		return nil, failStart("構成 %s を始めない: %v", cand.name, err)
	}
	if cand.legacy {
		legacyDefaults(cfg, launch, inv)
	}

	// 5. resolveCommand。
	rc, err := parseResolveCommand(cfg["resolveCommand"])
	if err != nil {
		return nil, failStart("構成 %s を始めない: %v", cand.name, err)
	}
	if rc != nil {
		in := resolveInput{Configuration: cfg, Adapter: launch, File: inv.file, FileType: inv.fileType, WorkspaceFolder: folder, WorkDir: workDir}
		token := "dap.resolve"
		_ = d.srv.ReportProgress(ext.ProgressReportParams{Token: token, Kind: ext.ProgressBegin, Title: "構成を決めています: " + cand.name})
		var c2 map[string]any
		c2, err = runResolveCommand(ctx, rc, folder, in, d.logf)
		_ = d.srv.ReportProgress(ext.ProgressReportParams{Token: token, Kind: ext.ProgressEnd, Failed: err != nil})
		if errors.Is(err, errResolveCancelled) {
			return nil, errStartCancelled
		}
		if err != nil {
			return nil, failStart("構成 %s を始めない: %v", cand.name, err)
		}
		overlay(cfg, c2)
	}
	for _, k := range extensionKeys {
		delete(cfg, k)
	}
	// ${port} と ${clientAddr} は、アダプターの command、args、env、cwd でだけ使える。
	// ほかの ${…} が残るのは外部のコマンドが返した値の中だけで、展開し直さずに文字のまま送る（基本設計 6.7。#408 のレビューの推奨 1）。
	left, err := connectVars(cfg)
	if err != nil {
		return nil, failStart("構成 %s を始めない: %v", cand.name, err)
	}
	if len(left) > 0 {
		return nil, failStart("構成 %s を始めない: %s はアダプターの command、args、env、cwd でだけ使える", cand.name, varList(left))
	}
	spec := &runSpec{
		name:    cand.name,
		request: request,
		config:  cfg,
		adapter: adapterRun{name: a.name, transport: a.transport, adapterID: a.adapterID, launch: launch},
	}
	if a.connectFrom != "" {
		if raw, ok := cfg[a.connectFrom]; ok {
			ep, err := parseEndpoint(raw)
			if err != nil {
				return nil, failStart("構成 %s の %s: %v", cand.name, a.connectFrom, err)
			}
			spec.adapter.direct = &ep
		}
	}
	return spec, nil
}

// numberWholeVar は、展開の前の値 before で、文字列の全体が変数 ${name} だけだった所を、展開した値 after の中で数（json.Number）にする
// （#408 のレビューの必須 3）。展開した値が 10 進の整数として読めないときは、文字列のまま残す。
//
// 展開の口（6.7）は、文字列の全体が 1 つの変数でも結果を文字列にする。
// ${pickProcess} のプロセス ID を、dlv 1.27.2 の attach は数でしか受けない（service/dap/types.go の ProcessID は int。レビューで確かめた）。
// オーナーの nvim の dap.utils.pick_process も数を返すので、${pickProcess} だけを数にする。
// VS Code の ${command:pickProcess} の値がアダプターへどの型で届くかは確かめていない（仮）。
func numberWholeVar(after, before any, name string) any {
	switch b := before.(type) {
	case map[string]any:
		a, ok := after.(map[string]any)
		if !ok {
			return after
		}
		for k, v := range a {
			if bv, ok := b[k]; ok {
				a[k] = numberWholeVar(v, bv, name)
			}
		}
		return a
	case []any:
		a, ok := after.([]any)
		if !ok || len(a) != len(b) {
			return after
		}
		for i := range a {
			a[i] = numberWholeVar(a[i], b[i], name)
		}
		return a
	case string:
		if b != "${"+name+"}" {
			return after
		}
		s, ok := expand.Plain(after).(string)
		if !ok {
			return after
		}
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return after
		}
		return json.Number(s)
	default:
		return after
	}
}

// connectVars は、値 v に書かれた変数のうち、つなぐ段階の変数（${port} と ${clientAddr}）だけを返す。
func connectVars(v any) ([]expand.Var, error) {
	vars, err := expand.Vars(v)
	if err != nil {
		return nil, err
	}
	var out []expand.Var
	for _, x := range vars {
		if x.Name == "port" || x.Name == "clientAddr" {
			out = append(out, x)
		}
	}
	return out, nil
}

// varList は、変数の一覧を「${port}、${clientAddr}」の形にする。
func varList(vs []expand.Var) string {
	s := make([]string, len(vs))
	for i, v := range vs {
		s[i] = v.String()
	}
	return strings.Join(s, "、")
}

// askIgnore は、扱えない項目 keys を挙げて、無視して続けるかを choice/ask で聞く（FR-281）。やめたら errStartCancelled を返す。
//
// req: FR-281
func (d *debugger) askIgnore(ctx context.Context, name string, keys []string) error {
	res, err := d.srv.AskChoice(ctx, ext.ChoiceAskParams{
		Title:   fmt.Sprintf("構成 %s の %s は扱えない。無視して続けるか", name, strings.Join(keys, "、")),
		Choices: []ext.Choice{{ID: "continue", Label: "無視して続ける"}, {ID: "cancel", Label: "やめる"}},
	})
	if err != nil {
		return pickError(err)
	}
	if res.Cancelled || res.Selected != "continue" {
		return errStartCancelled
	}
	return nil
}

// legacyDefaults は、今までの 1 組の設定の補い（program と、アダプターの作業ディレクトリ）を当てる（legacySettings の doc）。
func legacyDefaults(cfg, launch map[string]any, inv invocation) {
	if inv.file == "" {
		return
	}
	dir := filepath.Dir(inv.file)
	if _, ok := cfg["program"]; !ok {
		cfg["program"] = dir
	}
	if _, ok := launch["cwd"]; !ok {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			launch["cwd"] = dir
		}
	}
}

// parseEndpoint は、connectFrom のキーの値（host と port を持つオブジェクト）を読む。host を省けば 127.0.0.1。
// port は数か、数として読める文字列を受ける（基本設計 6.4 の「つなぎ方」）。
//
// req: FR-279
func parseEndpoint(raw any) (endpoint, error) {
	m, ok := expand.Plain(raw).(map[string]any)
	if !ok {
		return endpoint{}, errors.New("オブジェクトでない")
	}
	ep := endpoint{host: "127.0.0.1"}
	if h, ok := m["host"]; ok && h != nil {
		s, ok := h.(string)
		if !ok {
			return endpoint{}, errors.New("host が文字列でない")
		}
		if s != "" {
			ep.host = s
		}
	}
	port, err := parsePort(m["port"])
	if err != nil {
		return endpoint{}, err
	}
	ep.port = port
	return ep, nil
}

// parsePort は、ポートの値（数か、数として読める文字列）を読む。
func parsePort(v any) (int, error) {
	var s string
	switch x := expand.Plain(v).(type) {
	case json.Number:
		s = x.String()
	case string:
		s = strings.TrimSpace(x)
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return 0, errors.New("port が無い")
	default:
		return 0, fmt.Errorf("port が数でない: %v", x)
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("port が 1〜65535 の数でない: %q", s)
	}
	return n, nil
}

// applyEnvFile は、構成の envFile を読み、env に無い名前だけを env に足す（FR-281）。envFile はアダプターへ送らない。
// 1 行に 1 つの 名前=値 を読み、# で始まる行と空の行を読み飛ばし、値の前後の " か ' を外す。
// export の前置き、値の中の変数の展開、複数の行の値は扱わない（仮。基本設計 6.4）。相対パスはワークスペースの根から解く。
// 読めなければ誤りを返す（始めない）。
//
// req: FR-281
func applyEnvFile(cfg map[string]any, folder string) error {
	raw, ok := cfg["envFile"]
	if !ok || raw == nil {
		return nil
	}
	path, ok := expand.Plain(raw).(string)
	if !ok {
		return errors.New("envFile が文字列でない")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(folder, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("envFile を読めない: %w", err)
	}
	defer f.Close() //nolint:errcheck // 読むだけのファイルを閉じる
	env := map[string]any{}
	if e, ok := cfg["env"]; ok && e != nil {
		m, ok := e.(map[string]any)
		if !ok {
			return errors.New("env がオブジェクトでない")
		}
		env = maps.Clone(m)
	}
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return fmt.Errorf("envFile %s の %d 行目が 名前=値 でない", path, n)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if _, exists := env[name]; !exists {
			env[name] = value
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("envFile を読めない: %w", err)
	}
	cfg["env"] = env
	return nil
}

// resolver は、構成の展開（基本設計 6.4 の値の当て方の 3）の変数の意味である（基本設計 6.4 の「変数」の表）。
// ${port} と ${clientAddr} は残し、つなぐ段階で置き換える。${input:ID}（FR-298）は次の issue で聞くので、今は扱えない変数にする。
//
// req: FR-283, FR-299
func (d *debugger) resolver(ctx context.Context, inv invocation, folder string) expand.Resolver {
	noArg := func(f func() (string, error)) expand.Entry {
		return expand.Entry{
			Value: func(string) (string, error) { return f() },
			Check: func(arg string) error {
				if arg != "" {
					return errors.New("引数を取らない")
				}
				return nil
			},
		}
	}
	val := func(s string) func() (string, error) { return func() (string, error) { return s, nil } }
	fileVar := func(f func(string) string) func() (string, error) {
		return func() (string, error) {
			if inv.file == "" {
				return "", errors.New("ファイルに対応しないバッファでは決められない")
			}
			return f(inv.file), nil
		}
	}
	rel := func(p string) string {
		if r, err := filepath.Rel(folder, p); err == nil {
			return r
		}
		return p
	}
	sep := string(filepath.Separator)
	vars := map[string]expand.Entry{
		"workspaceFolder":         noArg(val(folder)),
		"workspaceFolderBasename": noArg(val(filepath.Base(folder))),
		"file":                    noArg(fileVar(func(f string) string { return f })),
		"fileBasename":            noArg(fileVar(filepath.Base)),
		"fileBasenameNoExtension": noArg(fileVar(func(f string) string { b := filepath.Base(f); return strings.TrimSuffix(b, filepath.Ext(b)) })),
		"fileExtname":             noArg(fileVar(filepath.Ext)),
		"fileDirname":             noArg(fileVar(filepath.Dir)),
		"fileDirnameBasename":     noArg(fileVar(func(f string) string { return filepath.Base(filepath.Dir(f)) })),
		"relativeFile":            noArg(fileVar(rel)),
		"relativeFileDirname":     noArg(fileVar(func(f string) string { return rel(filepath.Dir(f)) })),
		"fileWorkspaceFolder":     noArg(fileVar(func(string) string { return folder })),
		"lineNumber":              noArg(val(strconv.Itoa(inv.line))),
		"columnNumber":            noArg(val(strconv.Itoa(inv.column))),
		"cwd":                     noArg(os.Getwd),
		"userHome":                noArg(os.UserHomeDir),
		"pathSeparator":           noArg(val(sep)),
		"/":                       noArg(val(sep)),
		"pickProcess":             noArg(func() (string, error) { return d.pickProcess(ctx) }),
		"input": {
			Value: func(string) (string, error) { return "", errors.New("まだ扱えない") },
			Check: func(string) error { return errors.New("${input:…} はまだ扱えない") },
		},
		"port":       {Defer: true},
		"clientAddr": {Defer: true},
	}
	return expand.Resolver{Vars: vars, Unknown: expand.Fail, Env: expand.Builtin}
}
