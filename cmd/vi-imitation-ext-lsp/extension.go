package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// providedOps は、この拡張機能が引き受ける本体の操作である（基本設計 5.5。FR-94）。
// 宣言ファイル manifest.json の provides と一致させる（TestManifest）。
var providedOps = []ext.Operation{
	ext.OpDefinition, ext.OpImplementation, ext.OpTypeDefinition, ext.OpHover,
	ext.OpReferences, ext.OpDocumentSymbols, ext.OpWorkspaceSymbols,
	ext.OpFormat, ext.OpRename,
}

// stopTimeout は、拡張機能が終わるときに、言語サーバーの shutdown と exit を待つ時間の上限である。
// 超えたら言語サーバーを強制終了する。
//
// 本体は shutdown を送ってから期限（仮に 3 秒。基本設計 5.2）を過ぎると、この拡張機能のプロセスグループへ SIGTERM を送る。
// その期限の中で言語サーバーを止め終えるように、それより短くする。値は仮。
const stopTimeout = 2 * time.Second

// clientKey は、client を見分ける、言語サーバーの名前と根の組である（FR-320）。
type clientKey struct {
	server string
	root   string
}

// extension は、LSP の拡張機能の全体である。
// 言語サーバーの名前と根の組ごとの client を持ち、ext/server のフックとハンドラーを client へ振り分ける。
type extension struct {
	srv *server.Server
	log *log.Logger
	// ctx は、拡張機能の寿命の ctx である。本体の応答を待たない処理（sync が false のコマンドの続き）に使う。
	ctx context.Context
	// requestTimeout は、sync が false のコマンドが言語サーバーの応答を待つ時間の上限である。
	requestTimeout time.Duration
	// cfg は、initialize で読んだ設定である。その後は変えない。
	cfg *config

	// mu は clients と guided を守る。clients は、ファイルを開いて新しい組が要るときに足し（ループ）、
	// lsp.root で受け持つ文書が無くなった client を外す（ループ）。
	mu      sync.Mutex
	clients map[clientKey]*client
	// retiring は、lsp.root で受け持つ文書が無くなり、止めている途中の client の数である。stop が待つ。
	retiring sync.WaitGroup
	// pins は、言語サーバーの名前から、lsp.root で指定した根の一覧への対応である。ループだけが触る（FR-322）。
	pins map[string][]string
	// guided は、言語サーバーの入れ方を案内した実行ファイルと道具の ID の組である（client.guide。FR-143）。
	guided map[string]bool
	// tune は、テストが client の待ち時間などを変えるための関数である。nil なら何もしない。
	tune func(*client)

	// byBuffer は、バッファの番号から、そのバッファを同期している client への対応である。ループだけが触る。
	byBuffer map[ext.BufferID]*client
	// inlayOverride は、lsp.inlayHints.toggle で切り替えたインレイヒントの出し入れである（0 なら設定に従う。inlayOn、inlayOff。inlay.go）。
	inlayOverride atomic.Int32
}

// newExtension は、ハンドラーを登録した extension を作る。ctx は、拡張機能の寿命の ctx である。
func newExtension(ctx context.Context) *extension {
	x := &extension{
		srv:            server.New(),
		log:            log.New(os.Stderr, "vi-imitation-ext-lsp: ", log.LstdFlags),
		ctx:            ctx,
		requestTimeout: defaultRequestTimeout,
		byBuffer:       make(map[ext.BufferID]*client),
		clients:        make(map[clientKey]*client),
		pins:           make(map[string][]string),
	}
	x.srv.ErrorLog = x.log
	x.srv.OnInitialize(x.initialize)
	x.srv.OnBufferOpen(x.bufferOpen)
	x.srv.OnBufferChange(func(_ context.Context, e *server.BufferChangeEvent) {
		if c := x.byBuffer[e.Buffer.ID()]; c != nil {
			c.change(e)
		}
	})
	x.srv.OnBufferClose(func(_ context.Context, e *server.BufferCloseEvent) {
		if c := x.byBuffer[e.Buffer]; c != nil {
			c.close(e.Buffer)
			delete(x.byBuffer, e.Buffer)
		}
	})
	x.srv.OnBufferDidSave(func(_ context.Context, e *server.BufferDidSaveEvent) {
		if c := x.byBuffer[e.Params.Buffer]; c != nil {
			c.didSave(e)
		}
	})
	x.srv.ProviderAsync(ext.OpDefinition, x.locate(locDefinition))
	x.srv.ProviderAsync(ext.OpImplementation, x.locate(locImplementation))
	x.srv.ProviderAsync(ext.OpTypeDefinition, x.locate(locTypeDefinition))
	x.srv.ProviderAsync(ext.OpHover, x.hover)
	x.srv.ProviderAsync(ext.OpReferences, x.references)
	x.srv.ProviderAsync(ext.OpDocumentSymbols, x.documentSymbols)
	x.srv.ProviderAsync(ext.OpWorkspaceSymbols, x.workspaceSymbols)
	x.srv.ProviderAsync(ext.OpFormat, x.format)
	x.srv.ProviderAsync(ext.OpRename, x.rename)
	x.srv.BufferWillSaveAsync(x.willSave)
	x.srv.Command(cmdInlayHintsToggle, x.toggleInlayHints)
	x.srv.Command(cmdRoot, x.rootCommand)
	x.srv.OnShutdown(func(context.Context, *server.ShutdownEvent) { x.stop() })
	return x
}

// stop は、すべての言語サーバーへ shutdown と exit を送って止め、終わるまで待つ。
// 待つのは stopTimeout までで、それを過ぎた言語サーバーは強制終了する。
//
// 本体から shutdown を受け取ったとき（OnShutdown のフック。基本設計 5.2 の正常な終了）と、Serve が戻った後（run）に呼ぶ。
// 2 回目からは、止まっていない言語サーバーだけを待つ。
// フックの中では Server のループが止まるが、本体は shutdown の後に問い合わせと ping を送らないので、待ってよい。
// 言語サーバーからの診断は、止めている間も本体へ送る。
//
// req: FR-30
func (x *extension) stop() {
	x.mu.Lock()
	cs := make([]*client, 0, len(x.clients))
	for _, c := range x.clients {
		cs = append(cs, c)
	}
	x.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Go(func() { c.stop(ctx) })
	}
	wg.Wait()
	x.retiring.Wait()
}

func (x *extension) logf(format string, args ...any) { x.log.Printf(format, args...) }

// firstGuide は、key の案内をまだ出していなければ、出したことにして true を返す。
func (x *extension) firstGuide(key string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.guided[key] {
		return false
	}
	if x.guided == nil {
		x.guided = make(map[string]bool)
	}
	x.guided[key] = true
	return true
}

// showMessage は、本体へ message/show を送る。送れなければログに書く。
func (x *extension) showMessage(level ext.MessageLevel, text string) {
	if err := x.srv.ShowMessage(level, text); err != nil {
		x.logf("message/show: %v", err)
	}
}

// setDiagnostics は、本体へ diagnostic/set を送る。送れなければログに書く。
func (x *extension) setDiagnostics(p ext.DiagnosticSetParams) {
	if err := x.srv.SetDiagnostics(p); err != nil {
		x.logf("diagnostic/set: %v", err)
	}
}

// initialize は、initialize の settings を読む。client は、ファイルを開いたときに、言語サーバーの名前と根の組ごとに作る。
// 設定に誤りがあれば initialize に失敗を応答する。
func (x *extension) initialize(_ context.Context, e *server.InitializeEvent) error {
	cfg, err := parseSettings(e.Settings)
	if err != nil {
		return err
	}
	x.cfg = cfg
	return nil
}

// serverFor は、ファイルの種類 fileType を受け持つ言語サーバーの名前と設定を返す。無ければ nil を返す。
func (x *extension) serverFor(fileType string) (string, *serverConfig) {
	if x.cfg == nil {
		return "", nil
	}
	name, ok := x.cfg.byFileType[fileType]
	if !ok {
		return "", nil
	}
	return name, x.cfg.servers[name]
}

// rootFor は、言語サーバー name（設定 sc）の、パス path のファイルのワークスペースの根を返す。ループから呼ぶ。
// lsp.root で指定した根のうち path を含むものがあれば、いちばん深いものを返す（FR-322）。
// 無ければ、path のディレクトリから親へ設定の rootMarkers をたどって決める（findRoot）。
//
// req: FR-320, FR-322
func (x *extension) rootFor(name string, sc *serverConfig, path string) string {
	best := ""
	for _, r := range x.pins[name] {
		if within(path, r) && len(r) > len(best) {
			best = r
		}
	}
	if best != "" {
		return best
	}
	return findRoot(path, sc.RootMarkers)
}

// within は、パス path がディレクトリ dir の下（dir そのものを含む）にあるかを返す。どちらも filepath.Clean した絶対パスとして比べる。
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// clientFor は、ファイルの種類 fileType、パス path のファイルを受け持つ client を返す。ループから呼ぶ。
// 言語サーバーの名前と根の組の client が無ければ作る（言語サーバーは、最初の文書を開いたときに起動する）。
// ファイルの種類を受け持つ言語サーバーが無いか、パスが空なら nil を返す。
//
// req: FR-320
func (x *extension) clientFor(fileType, path string) *client {
	name, sc := x.serverFor(fileType)
	if sc == nil || path == "" {
		return nil
	}
	return x.clientAt(name, x.rootFor(name, sc, path))
}

// clientAt は、言語サーバー name の根 root の client を返す。無ければ作る。
func (x *extension) clientAt(name, root string) *client {
	key := clientKey{server: name, root: root}
	x.mu.Lock()
	defer x.mu.Unlock()
	if c := x.clients[key]; c != nil {
		return c
	}
	c := newClient(x, name, root, x.cfg.servers[name], x.cfg.maxRestarts)
	if x.tune != nil {
		x.tune(c)
	}
	x.clients[key] = c
	return c
}

// bufferOpen は、設定に言語サーバーのあるファイルの種類のバッファを、言語サーバーの名前と根の組の client で同期する。
// ファイルに対応しないバッファ（パスが空）は、言語サーバーへ URI で示せないので同期しない。
//
// 本体は、バッファのファイルの種類かパスが変わると、buffer/open を送り直す（:set filetype=、別名での保存。exthost の UpdateBuffer）。
// 同期していた client と違う client になったか、パスか languageId が変わったら、前の client から外し（didClose）、
// その client の診断を消す（client.detach）。
// その後、新しい client があれば、新しい文書として didOpen を送る。
// パスが変わったときに外し直すのは、言語サーバーの文書は URI で区別されるので、全文の送り直しでは新しいパスに移せないためである。
//
// req: FR-30, FR-320
func (x *extension) bufferOpen(_ context.Context, e *server.BufferOpenEvent) {
	b := e.Buffer
	c := x.clientFor(b.FileType(), b.Path())
	old := x.byBuffer[b.ID()]
	if old != nil && (old != c || !old.syncs(b.ID(), b.Path(), b.FileType())) {
		old.detach(b)
		delete(x.byBuffer, b.ID())
		old = nil
	}
	if c == nil {
		return
	}
	x.byBuffer[b.ID()] = c
	c.open(b, e.Reopen && old != nil)
}

// sendFor は、操作の対象のバッファについて、言語サーバーへリクエストを送る。ループから呼ぶ。
// 送れなければ、利用者に理由を表示して誤りを返す。
func (x *extension) sendFor(
	p *ext.ProviderInvokeParams, what, method string,
	has func(*serverCapabilities) bool,
	params func(uri string, ls lines, enc encoding) any,
) (*request, error) {
	if p.Withheld {
		// 本体が不正な UTF-8 のためにバッファを送っていない。理由は本体が利用者へ知らせるので、ここでは表示しない。
		return nil, errors.New("buffer is withheld by the editor")
	}
	c := x.byBuffer[p.Buffer]
	if c == nil {
		x.showMessage(ext.MessageWarning, "このバッファには言語サーバーが無い")
		return nil, errors.New("no language server for the buffer")
	}
	r, err := c.send(p.Buffer, p.Version, method, has, params)
	switch {
	case errors.Is(err, errNotReady):
		x.showMessage(ext.MessageWarning, fmt.Sprintf("言語サーバー %s が稼働していない", c.name()))
	case errors.Is(err, errUnsupported):
		x.showMessage(ext.MessageWarning, fmt.Sprintf("言語サーバー %s は%sに対応していない", c.name(), what))
	case err != nil:
		x.logf("%s: %v", method, err)
	}
	return r, err
}

// positionParams は、カーソル位置 cursor の textDocument/definition と textDocument/hover の params を作る関数を返す。
func positionParams(cursor ext.Position) func(uri string, ls lines, enc encoding) any {
	return func(uri string, ls lines, enc encoding) any {
		return textDocumentPositionParams{TextDocument: textDocumentIdentifier{URI: uri}, Position: ls.toLSP(cursor, enc)}
	}
}

// background は、本体の応答を待たない処理の ctx を返す。
func (x *extension) background() (context.Context, context.CancelFunc) {
	return context.WithTimeout(x.ctx, x.requestTimeout)
}

// locateKind は、移動の操作の種類（定義、実装、型の定義）である。
type locateKind struct {
	// what は、利用者へ出す操作の名前である（「定義」など）。
	what   string
	method string
	has    func(*serverCapabilities) bool
}

// 移動の操作の種類（基本設計 5.5 の「移動」）。
var (
	locDefinition = locateKind{
		what: "定義", method: lspDefinition,
		has: func(c *serverCapabilities) bool { return provides(c.DefinitionProvider) },
	}
	locImplementation = locateKind{
		what: "実装", method: lspImplementation,
		has: func(c *serverCapabilities) bool { return provides(c.ImplementationProvider) },
	}
	locTypeDefinition = locateKind{
		what: "型の定義", method: lspTypeDefinition,
		has: func(c *serverCapabilities) bool { return provides(c.TypeDefinitionProvider) },
	}
)

// locate は、移動の操作（definition、implementation、typeDefinition）のハンドラーを返す（基本設計 5.5、6.3）。
//
// カーソル位置で k.method を送る。
// sync が false なら、受け付けたことだけを ok で応答し、結果は後から送る。
// 候補が 1 つなら editor/open でその位置へ移動し、複数なら choice/ask で利用者に選ばせる。候補が無ければ message/show で知らせる。
// args の peek が true なら、移らずに、移動先を editor/open の peek で本体に見せる（要件定義 4.11 の「ポップアップで見る」。FR-110）。
// 本体が、移動先の周りの行の写しを、編集を禁止したバッファとしてカーソルの近くのポップアップに出す。
// sync が true（マクロとドットリピートの再生中）なら、結果がそろってから、最初の候補への移動（peek なら peek を付けたもの）を応答の open で返す。
// choice/ask への答えはマクロに記録されないので、再生中は選ばせない。候補が無ければ ok を false にする。
//
// req: FR-30, FR-94, FR-110
func (x *extension) locate(k locateKind) server.AsyncProviderHandler {
	return func(ctx context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams, reply func(ext.ProviderInvokeResult, error)) {
		r, err := x.sendFor(p, k.what+"へ移動", k.method, k.has, positionParams(p.Cursor))
		if err != nil {
			reply(ext.ProviderInvokeResult{OK: false}, nil)
			return
		}
		peek := providerArgs(p).Peek
		if p.Sync {
			go func() {
				targets, err := x.locationTargets(ctx, r)
				if err != nil || len(targets) == 0 {
					if err != nil {
						x.logf("%s: %v", k.method, err)
					}
					reply(ext.ProviderInvokeResult{OK: false}, nil)
					return
				}
				open := targets[0].open
				open.Peek = peek
				reply(ext.ProviderInvokeResult{OK: true, Open: &open}, nil)
			}()
			return
		}
		reply(ext.ProviderInvokeResult{OK: true}, nil)
		go func() {
			ctx, cancel := x.background()
			defer cancel()
			targets, err := x.locationTargets(ctx, r)
			if err != nil {
				x.logf("%s: %v", k.method, err)
				x.showMessage(ext.MessageError, fmt.Sprintf("%sへ移動できない: %v", k.what, err))
				return
			}
			if len(targets) == 0 {
				x.showMessage(ext.MessageInfo, k.what+"が見つからない")
				return
			}
			t, ok := x.pick(k.what+"の候補", targets)
			if !ok {
				return
			}
			t.open.Peek = peek
			x.open(t.open, k.what+"へ移動できない")
		}()
	}
}

// providerArgs は、provider/invoke の args を読む。読めなければ空の値を返す。
func providerArgs(p *ext.ProviderInvokeParams) ext.ProviderArgs {
	var a ext.ProviderArgs
	if len(p.Args) > 0 {
		_ = json.Unmarshal(p.Args, &a)
	}
	return a
}

// pick は、候補が 1 つならそれを、複数なら choice/ask で利用者に選ばせたものを返す。取り消されたら ok は false である。
// 利用者が選ぶのを待つので、requestTimeout の ctx ではなく拡張機能の寿命の ctx で待つ。
func (x *extension) pick(title string, targets []target) (target, bool) {
	if len(targets) == 1 {
		return targets[0], true
	}
	choices := make([]ext.Choice, len(targets))
	for i, t := range targets {
		choices[i] = ext.Choice{ID: fmt.Sprint(i), Label: t.label}
	}
	res, err := x.srv.AskChoice(x.ctx, ext.ChoiceAskParams{Title: title, Choices: choices})
	if err != nil || res.Cancelled {
		if err != nil {
			x.logf("choice/ask: %v", err)
		}
		return target{}, false
	}
	i := slices.IndexFunc(choices, func(c ext.Choice) bool { return c.ID == res.Selected })
	if i < 0 {
		return target{}, false
	}
	return targets[i], true
}

// open は、editor/open で移動する。失敗したら failText に理由を付けて知らせる。
func (x *extension) open(open ext.EditorOpenParams, failText string) {
	ctx, cancel := x.background()
	defer cancel()
	if _, err := x.srv.OpenEditor(ctx, open); err != nil {
		x.logf("editor/open: %v", err)
		x.showMessage(ext.MessageError, fmt.Sprintf("%s: %v", failText, err))
	}
}

// target は、移動の 1 つの候補である。
type target struct {
	open  ext.EditorOpenParams
	label string
	// lines は、移動先の行（1 行）である。参照の一覧のラベルに使う。ファイルを読めなければ nil。
	// 「ポップアップで見る」の周りの行は、本体が editor/open の peek で写す（FR-110）ので、ここでは持たない。
	lines []string
}

// locationTargets は、Location、Location[]、LocationLink[] を返すリクエストの応答を待ち、候補を本体の位置にして返す。
func (x *extension) locationTargets(ctx context.Context, r *request) ([]target, error) {
	var raw json.RawMessage
	if err := r.wait(ctx, &raw); err != nil {
		return nil, err
	}
	locs, err := parseLocations(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid definition result: %w", err)
	}
	var targets []target
	for _, l := range locs {
		uri, pos, ok := l.target()
		if !ok {
			continue
		}
		path, ok := uriToPath(uri)
		if !ok {
			continue
		}
		t := r.c.resolve(r, path, pos)
		if slices.ContainsFunc(targets, func(u target) bool { return u.label == t.label }) {
			continue
		}
		targets = append(targets, t)
	}
	return targets, nil
}

// resolve は、言語サーバーの示したファイル path の位置 pos を、editor/open の params にする。
//
//   - 問い合わせた文書なら、問い合わせた版の行で変換し、バッファの番号とその版の番号を指定する。
//     本体は、その間にバッファが変わっていれば、開くだけでカーソルを動かさない
//   - この client が同期している他の文書なら、その現在の版の行で変換し、バッファの番号と版の番号を指定する
//   - 開いていないファイルなら、ファイルを読んで変換し、パスを指定する。読めなければ位置を指定せずに開く
func (c *client) resolve(r *request, path string, pos lspPosition) target {
	t := target{open: ext.EditorOpenParams{Focus: true}}
	var ls lines
	var version ext.Version
	if path == r.path {
		ls, version = r.lines, r.version
		id := r.buffer
		t.open.Buffer = &id
	} else {
		c.mu.Lock()
		if d := c.docByPath(path); d != nil {
			cur := d.current()
			ls, version = cur.lines, cur.version
			id := d.buffer
			t.open.Buffer = &id
		}
		c.mu.Unlock()
	}
	if t.open.Buffer != nil {
		t.open.Version = &version
	} else {
		t.open.Path = path
		if data, err := os.ReadFile(path); err == nil {
			ls = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		}
	}
	t.label = fmt.Sprintf("%s:%d", path, pos.Line+1)
	if ls != nil {
		at, _ := ls.fromLSP(pos, r.enc)
		t.open.Position = &at
		t.label = fmt.Sprintf("%s:%d:%d", path, at.Line+1, at.Byte+1)
		if at.Line < len(ls) {
			t.lines = slices.Clone(ls[at.Line : at.Line+1])
		}
	}
	return t
}

// hover は、本体の操作 hover を処理する（基本設計 5.5、6.3）。
// カーソル位置で textDocument/hover を送り、受け付けたことだけを ok で応答する。
// 結果の本文は popup/show で、問い合わせた版の番号とカーソル位置と一緒に送る。
//
// sync が true（マクロとドットリピートの再生中）なら、popup/show を送り終えてから応答する（FR-19）。
// 本体は応答を待つ間に popup/show を出し、再生の続きのキーを解釈したら消す（基本設計 6.7）。
// このため、再生の途中のホバーは、記録したときと同じく次のキーで消え、再生の後に残らない（#210、#224）。
// 結果を得られなければ ok を false にして、再生を止める。
//
// req: FR-30, FR-94, FR-19
func (x *extension) hover(ctx context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams, reply func(ext.ProviderInvokeResult, error)) {
	r, err := x.sendFor(p, "ホバー", lspHover,
		func(c *serverCapabilities) bool { return provides(c.HoverProvider) }, positionParams(p.Cursor))
	if err != nil {
		reply(ext.ProviderInvokeResult{OK: false}, nil)
		return
	}
	cursor := p.Cursor
	if p.Sync {
		go func() {
			reply(ext.ProviderInvokeResult{OK: x.showHover(ctx, r, cursor)}, nil)
		}()
		return
	}
	reply(ext.ProviderInvokeResult{OK: true}, nil)
	go func() {
		ctx, cancel := x.background()
		defer cancel()
		x.showHover(ctx, r, cursor)
	}()
}

// showHover は、textDocument/hover の応答を待ち、本文を popup/show で出す。情報が無ければそのことを知らせる。
// 応答を得られなければ知らせて false を返す。
func (x *extension) showHover(ctx context.Context, r *request, cursor ext.Position) bool {
	var res *hoverResult
	if err := r.wait(ctx, &res); err != nil {
		x.logf("%s: %v", lspHover, err)
		x.showMessage(ext.MessageError, fmt.Sprintf("ホバーの情報を得られない: %v", err))
		return false
	}
	var text, format string
	if res != nil {
		text, format = hoverText(res.Contents)
	}
	if strings.TrimSpace(text) == "" {
		x.showMessage(ext.MessageInfo, "ホバーの情報が無い")
		return true
	}
	popup := ext.PopupShowParams{Buffer: r.buffer, Version: r.version, Position: cursor, Text: text, Format: ext.PopupFormat(format)}
	if err := x.srv.ShowPopup(popup); err != nil {
		x.logf("popup/show: %v", err)
	}
	return true
}

// formattingParams は、textDocument/formatting の params を作る関数を返す。
func formattingParams(cfg *serverConfig) func(uri string, ls lines, enc encoding) any {
	return func(uri string, _ lines, _ encoding) any {
		return documentFormattingParams{
			TextDocument: textDocumentIdentifier{URI: uri},
			Options:      formattingOptions{TabSize: cfg.tabSize(), InsertSpaces: cfg.InsertSpaces},
		}
	}
}

// formatTransaction は、textDocument/formatting の応答を待ち、トランザクションにする。整形の変更が無ければ nil を返す。
func formatTransaction(ctx context.Context, r *request) (*ext.Transaction, error) {
	var edits []textEdit
	if err := r.wait(ctx, &edits); err != nil {
		return nil, err
	}
	return formattingTransaction(r.lines, r.finalNewline, r.version, edits, r.enc), nil
}

// hasFormatting は、言語サーバーが textDocument/formatting に対応するかどうかを返す。
func hasFormatting(c *serverCapabilities) bool { return provides(c.DocumentFormattingProvider) }

// rangeFormattingParams は、範囲 rg の textDocument/rangeFormatting の params を作る関数を返す。
func rangeFormattingParams(cfg *serverConfig, rg ext.Range) func(uri string, ls lines, enc encoding) any {
	return func(uri string, ls lines, enc encoding) any {
		return documentRangeFormattingParams{
			TextDocument: textDocumentIdentifier{URI: uri},
			Range:        lspRange{Start: ls.toLSP(rg.Start, enc), End: ls.toLSP(rg.End, enc)},
			Options:      formattingOptions{TabSize: cfg.tabSize(), InsertSpaces: cfg.InsertSpaces},
		}
	}
}

// hasRangeFormatting は、言語サーバーが textDocument/rangeFormatting に対応するかどうかを返す。
func hasRangeFormatting(c *serverCapabilities) bool {
	return provides(c.DocumentRangeFormattingProvider)
}

// format は、本体の操作 format を処理する（基本設計 5.5、6.3）。
//
// 範囲（gq{モーション} の p.Range）があり、言語サーバーが textDocument/rangeFormatting に対応すれば、その範囲を整形する。
// 範囲が無いか、範囲の整形に対応しなければ、textDocument/formatting で全体を整形する。
// 範囲の整形に対応しないときに全体を整形したことは message/show で知らせる（gopls v0.24.0 は範囲の整形に対応しない）。
// sync が false なら、受け付けたことだけを ok で応答し、結果を buffer/edit で送る。
// その間に利用者が編集していれば、本体は適用しない（FR-15）ので、message/show でそのことを知らせる。
// sync が true なら、結果がそろってから、編集を応答の transaction で返す。
//
// req: FR-30, FR-94
func (x *extension) format(ctx context.Context, _ *server.Buffer, p *ext.ProviderInvokeParams, reply func(ext.ProviderInvokeResult, error)) {
	if p.Withheld {
		// sendFor と同じく、理由は本体が知らせる。
		reply(ext.ProviderInvokeResult{OK: false}, nil)
		return
	}
	c := x.byBuffer[p.Buffer]
	if c == nil {
		x.showMessage(ext.MessageWarning, "このバッファには言語サーバーが無い")
		reply(ext.ProviderInvokeResult{OK: false}, nil)
		return
	}
	var r *request
	var err error
	if p.Range != nil {
		r, err = c.send(p.Buffer, p.Version, lspRangeFormatting, hasRangeFormatting, rangeFormattingParams(c.cfg, *p.Range))
		if errors.Is(err, errUnsupported) {
			r, err = nil, nil
			x.showMessage(ext.MessageInfo, fmt.Sprintf("言語サーバー %s は範囲の整形に対応しないので、全体を整形する", c.name()))
		} else if err != nil {
			// 稼働していないなどの理由は、全体の整形でも同じなので、sendFor に知らせさせる。
			r = nil
		}
	}
	if r == nil {
		r, err = x.sendFor(p, "整形", lspFormatting, hasFormatting, formattingParams(c.cfg))
	}
	if err != nil {
		reply(ext.ProviderInvokeResult{OK: false}, nil)
		return
	}
	if p.Sync {
		go func() {
			t, err := formatTransaction(ctx, r)
			if err != nil {
				x.logf("%s: %v", lspFormatting, err)
				reply(ext.ProviderInvokeResult{OK: false}, nil)
				return
			}
			reply(ext.ProviderInvokeResult{OK: true, Transaction: t}, nil)
		}()
		return
	}
	reply(ext.ProviderInvokeResult{OK: true}, nil)
	go func() {
		ctx, cancel := x.background()
		defer cancel()
		t, err := formatTransaction(ctx, r)
		if err != nil {
			x.logf("%s: %v", lspFormatting, err)
			x.showMessage(ext.MessageError, fmt.Sprintf("整形できない: %v", err))
			return
		}
		if t == nil {
			return
		}
		res, err := x.srv.Edit(ctx, ext.BufferEditParams{Buffer: r.buffer, Transaction: *t})
		switch {
		case err != nil:
			x.logf("buffer/edit: %v", err)
			x.showMessage(ext.MessageError, fmt.Sprintf("整形を適用できない: %v", err))
		case !res.Applied:
			x.showMessage(ext.MessageWarning, "整形の間にバッファが変わったので、整形を適用しなかった")
		}
	}()
}

// willSave は、保存の直前の buffer/willSave に、textDocument/formatting の結果のトランザクションを答える（基本設計 6.3、6.7）。
// 保存の前の整形を設定で止めているか、言語サーバーが稼働していないか、整形に対応しなければ、編集無しを答える。
// 稼働していない（起動中か再起動を待っている）ときは、整形しなかったことを message/show で知らせる。
// 本体が期限を超えて $/cancel を送ったら、言語サーバーへ $/cancelRequest を送り、取り消しを応答する。
//
// req: FR-30
func (x *extension) willSave(ctx context.Context, _ *server.Buffer, p *ext.BufferWillSaveParams, reply func(ext.BufferWillSaveResult, error)) {
	c := x.byBuffer[p.Buffer]
	if c == nil || !c.cfg.formatOnSave() {
		reply(ext.BufferWillSaveResult{}, nil)
		return
	}
	r, err := c.send(p.Buffer, p.Version, lspFormatting, hasFormatting, formattingParams(c.cfg))
	if err != nil {
		if errors.Is(err, errNotReady) {
			x.showMessage(ext.MessageWarning, fmt.Sprintf("言語サーバー %s が稼働していないので、保存の前の整形をしなかった", c.name()))
		}
		reply(ext.BufferWillSaveResult{}, nil)
		return
	}
	go func() {
		t, err := formatTransaction(ctx, r)
		switch {
		case ctx.Err() != nil:
			reply(ext.BufferWillSaveResult{}, ctx.Err())
		case err != nil:
			x.logf("%s before saving: %v", lspFormatting, err)
			reply(ext.BufferWillSaveResult{}, nil)
		default:
			reply(ext.BufferWillSaveResult{Transaction: t}, nil)
		}
	}()
}
