package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/project"
	"github.com/piplupflipper/vi-imitation/ext/server"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// errNotReady は、言語サーバーが稼働していないので問い合わせられないことを表す。
var errNotReady = errors.New("language server is not running")

// proc は、起動した 1 つの言語サーバーのプロセスと接続である。
// 異常終了から再起動すると、別の proc を作る。古い proc から届いたメッセージは捨てる。
type proc struct {
	cmd  *exec.Cmd
	conn *rpc.Conn
	// exited は、プロセスが終了し、接続を閉じた後に閉じる。
	exited chan struct{}

	// 以下は client.mu で守る。
	// ready は、initialize の応答を受け取り、initialized と開いている文書の didOpen を送った後に true になる。
	ready bool
	// stopping は、拡張機能が自分から止めようとしていることを表す。終了しても再起動しない。
	stopping bool
	// readySince は、ready になった時刻である。
	readySince time.Time
	enc        encoding
	caps       serverCapabilities
	sync       textDocumentSyncOptions
	save       bool
	saveText   bool
	// gen は、言語サーバーのプロセスを起動するたびに振る番号である。進み具合の識別子に含め、再起動の前後で重ならないようにする。
	gen uint64
	// progress は、$/progress で始まり、まだ終わっていない進み具合の、本体へ送った識別子の集合である（progress.go）。
	progress map[string]bool
}

// client は、1 つの言語サーバーの設定（名前）と 1 つのワークスペースの根の組について、言語サーバーのプロセスを起動し、
// 文書を同期し、診断を本体へ送る（FR-320）。
// 同じ言語サーバーでも根が違えば別の client で、同じ根のファイルはファイルの種類が違っても同じ client が受け持つ
// （Neovim の vim.lsp が、設定の名前と root_dir ごとに 1 つの client を起動するのと同じ）。
//
// 文書の写し（docs）と言語サーバーの状態は mu で守る。
// mu は、ext/server のループ（フックとハンドラー）、言語サーバーの接続の読み取りの goroutine（診断など）、
// 起動と監視の goroutine から取る。どこも mu を持ったまま待たない。
// 言語サーバーへの送信（rpc.Conn の Notify と Go）と本体への通知（Server の SetDiagnostics など）は列に積むだけで待たないので、mu を持ったまま呼ぶ。
// mu を持ったまま送ることで、didOpen と didChange の順序が、文書の写しを更新した順序と一致する。
// rpc.Conn の Close は、実行中のハンドラー（mu を待ちうる）が戻るのを待つので、mu を持ったまま呼ばない。
//
// 言語サーバーは、最初の文書を開いたときに起動する。起動するまでと再起動を待つ間は、文書の写しだけを更新し、何も送らない。
// 起動したら（initialize の応答の後）、その時点の写しの全文を didOpen で送る。
type client struct {
	x *extension
	// server は、言語サーバーの名前（設定の servers の鍵）である。
	server string
	cfg    *serverConfig
	// root は、ワークスペースの根のディレクトリである。client を作るときに決め、変えない。
	root string
	// venv は、根から探した Python の仮想環境である。設定の PythonVenvSetting が無いか、見つからなければ空である（FR-321）。
	venv project.Venv
	// settings は、workspace/configuration に答える値である。設定の Settings に、仮想環境の python を入れたものである。
	settings map[string]json.RawMessage
	// idleTimer と idleGen は、受け持つ文書が無くなった後に言語サーバーを止める予約である（extension.scheduleIdleStop。FR-323）。
	// x.mu で守る。idleGen は、予約するたびと、clientAt でこの client を返すたびに増やし、古い予約を無効にする。
	idleTimer *time.Timer
	idleGen   uint64

	// maxRestarts は、異常終了したときに再起動する回数の上限である。
	maxRestarts int
	// restartDelay は、1 回目の再起動までの待ち時間である。n 回目は n 倍待つ。
	restartDelay time.Duration
	// initTimeout は、initialize の応答を待つ時間の上限である。
	initTimeout time.Duration
	// resetAfter は、稼働中になってからこの時間より長く動いた後の異常終了では、再起動の回数を数え直すという時間である。
	resetAfter time.Duration
	// mise は、mise の実行ファイルである。"/" を含まなければ PATH から探す（FR-143）。
	mise string
	// installTimeout は、mise に言語サーバーを入れさせるときに待つ時間の上限である（FR-143）。
	installTimeout time.Duration
	// inlayInterval は、1 つの文書の textDocument/inlayHint を続けて送るときの間隔の下限である（inlay.go。FR-256）。
	inlayInterval time.Duration
	// ctx は、client の寿命の ctx で、stop で取り消す。mise に入れさせている途中で拡張機能が終わるときに、mise を終わらせる。
	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	docs map[ext.BufferID]*document
	proc *proc
	// starting は、起動の goroutine が動いているか、再起動を待っていることを表す。
	starting bool
	// restarts は、異常終了から再起動した回数である。resetAfter より長く動いた後の異常終了では 0 から数え直す。
	restarts int
	// failed は、再起動の回数が上限に達したか、実行ファイルを起動できなかったので、もう起動しないことを表す。
	failed bool
	// stopped は、拡張機能が終わるので、もう起動しないことを表す。
	stopped bool
	// miseDone は、動いている mise where か mise install（runMise）が終わったときに閉じる。動いていなければ nil か閉じている。
	miseDone chan struct{}
}

// newClient は、言語サーバー server（設定 cfg）の、根 root の client を作る。言語サーバーは、最初の文書を開いたときに起動する。
//
// 設定に PythonVenvSetting があれば、根から Python の仮想環境を探し、見つかればその python を workspace/configuration の答えに入れる。
// 見つからないことは誤りにしない（PATH の python のまま）。探すときの権限の誤りと、入れる場所の誤りはログに書き、入れない。
//
// req: FR-321
func newClient(x *extension, server, root string, cfg *serverConfig, maxRestarts int) *client {
	ctx, cancel := context.WithCancel(x.ctx)
	c := &client{
		x:              x,
		server:         server,
		cfg:            cfg,
		root:           root,
		settings:       cfg.Settings,
		maxRestarts:    maxRestarts,
		restartDelay:   defaultRestartDelay,
		initTimeout:    defaultInitTimeout,
		resetAfter:     defaultResetAfter,
		mise:           "mise",
		installTimeout: defaultInstallTimeout,
		inlayInterval:  inlayInterval,
		ctx:            ctx,
		cancel:         cancel,
		docs:           make(map[ext.BufferID]*document),
	}
	if cfg.PythonVenvSetting == "" {
		return c
	}
	v, err := project.PythonVenv(root)
	switch {
	case errors.Is(err, project.ErrNotFound):
		return c
	case err != nil:
		c.logf("%s: Python virtual environment: %v", c.name(), err)
		return c
	}
	settings, err := cfg.effectiveSettings(v.Python)
	if err != nil {
		c.logf("%s: %v", c.name(), err)
		return c
	}
	c.venv, c.settings = v, settings
	return c
}

// name は、利用者へのメッセージに使う言語サーバーの名前である。
func (c *client) name() string { return filepath.Base(c.cfg.Command) }

// logf は、拡張機能のログに、根を前に付けて書く。同じ言語サーバーの、根の違う client のログを見分けるためである。
func (c *client) logf(format string, args ...any) {
	c.x.logf("[%s] "+format, append([]any{c.root}, args...)...)
}

// ready は、mu を持った状態で、稼働中の proc を返す。稼働中でなければ nil である。
func (c *client) readyProc() *proc {
	if c.proc != nil && c.proc.ready {
		return c.proc
	}
	return nil
}

// open は、バッファ b の写しを作るか（reopen が false）、全文で置き換える（reopen が true）。
// ループから呼ぶ。言語サーバーが起動していなければ起動を始める。
// 言語サーバーが稼働中なら、送った文書のインレイヒントの問い合わせを予約する（inlay.go）。
//
// req: FR-30, FR-256
func (c *client) open(b *server.Buffer, reopen bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, held := c.docs[b.ID()]
	if reopen && held {
		d.reset(b)
		if p := c.readyProc(); p != nil && p.sync.Change != syncNone {
			c.notify(p, lspDidChange, didChangeParams{
				TextDocument:   versionedTextDocumentIdentifier{URI: d.uri, Version: lspVersion(b.Version())},
				ContentChanges: []contentChange{{Text: d.lspText()}},
			})
			c.scheduleInlay(p, d)
		}
		return
	}
	c.addLocked(newDocument(b, c.cfg.languageID(b.FileType())))
}

// addLocked は、mu を持った状態で、文書 d を受け持つ。言語サーバーが稼働中なら didOpen を送り、インレイヒントの問い合わせを予約する。
// 起動していなければ起動を始める。
func (c *client) addLocked(d *document) {
	c.docs[d.buffer] = d
	if p := c.readyProc(); p != nil {
		c.didOpen(p, d)
		c.scheduleInlay(p, d)
		return
	}
	if c.proc == nil && !c.starting && !c.failed && !c.stopped {
		c.starting = true
		go c.start()
	}
}

// take は、バッファ id の文書を、この client から外して返す。ループから呼ぶ。持っていなければ nil を返す。
// detach と同じく textDocument/didClose を送り、この client が送った診断とインレイヒントを、文書の現在の版で消す。
// 返した文書は、別の client の adopt に渡す（根を指定し直したとき。FR-322）。
//
// req: FR-322
func (c *client) take(id ext.BufferID) *document {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.docs[id]
	if !ok {
		return nil
	}
	c.resetInlay(d, true)
	c.closeLocked(id)
	v := d.current().version
	c.x.setDiagnostics(ext.DiagnosticSetParams{Buffer: id, Version: v, Diagnostics: []ext.Diagnostic{}})
	return d
}

// adopt は、別の client から外した文書 d を受け持つ。ループから呼ぶ。
// 文書の写しは本体のバッファの現在の版のままなので、そのまま didOpen で送る。
//
// req: FR-322
func (c *client) adopt(d *document) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addLocked(d)
}

// pathOf は、バッファ id の文書のパスを返す。受け持っていなければ ok は false である。
func (c *client) pathOf(id ext.BufferID) (path string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.docs[id]
	if !ok {
		return "", false
	}
	return d.path, true
}

// bufferIDs は、受け持っている文書のバッファの番号を、小さい順に返す。
func (c *client) bufferIDs() []ext.BufferID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Sorted(maps.Keys(c.docs))
}

// idle は、受け持つ文書が無いかを返す。
func (c *client) idle() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.docs) == 0
}

// didOpen は、mu を持った状態で、d の textDocument/didOpen を送る。
func (c *client) didOpen(p *proc, d *document) {
	if !p.sync.OpenClose {
		return
	}
	cur := d.current()
	c.notify(p, lspDidOpen, didOpenParams{TextDocument: textDocumentItem{
		URI: d.uri, LanguageID: d.languageID, Version: lspVersion(cur.version), Text: d.lspText(),
	}})
}

// change は、buffer/change の編集を写しに適用し、言語サーバーへ textDocument/didChange を送る。ループから呼ぶ。
// 言語サーバーが増分の同期に対応していれば編集ごとの範囲を、そうでなければ全文を送る。
// 送ったら、インレイヒントの問い合わせを予約する（inlay.go）。
//
// req: FR-30, FR-256
func (c *client) change(e *server.BufferChangeEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.docs[e.Buffer.ID()]
	if !ok {
		return
	}
	p := c.readyProc()
	incremental := p != nil && p.sync.Change == syncIncremental
	var enc encoding
	if p != nil {
		enc = p.enc
	}
	changes, err := d.apply(e.Edits, e.ToVersion, enc, incremental)
	if err != nil {
		c.logf("%s: buffer %d: %v; resending the whole text", c.name(), d.buffer, err)
		d.reset(e.Buffer)
		incremental = false
	}
	if p == nil || p.sync.Change == syncNone {
		return
	}
	if !incremental {
		changes = []contentChange{{Text: d.lspText()}}
	}
	c.notify(p, lspDidChange, didChangeParams{
		TextDocument:   versionedTextDocumentIdentifier{URI: d.uri, Version: lspVersion(e.ToVersion)},
		ContentChanges: changes,
	})
	c.scheduleInlay(p, d)
}

// close は、バッファ id の写しを捨て、言語サーバーへ textDocument/didClose を送る。ループから呼ぶ。
// 本体が buffer/close を送ったときに使う。本体はそのときバッファの診断を自分で消すので（基本設計 5.3）、空の診断は送らない。
func (c *client) close(id ext.BufferID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked(id)
}

// closeLocked は、mu を持った状態で、バッファ id の写しを捨てて textDocument/didClose を送る。写しを持っていたかを返す。
// インレイヒントの予約と、応答を待っている問い合わせも取り消す。ヒントを消す一覧は送らない（呼ぶ側が決める）。
func (c *client) closeLocked(id ext.BufferID) bool {
	d, ok := c.docs[id]
	if !ok {
		return false
	}
	c.resetInlay(d, false)
	delete(c.docs, id)
	if p := c.readyProc(); p != nil && p.sync.OpenClose {
		c.notify(p, lspDidClose, didCloseParams{TextDocument: textDocumentIdentifier{URI: d.uri}})
	}
	return true
}

// detach は、本体が送り直したバッファ b（buffer/open。ファイルの種類かパスが変わったもの）を、この client から外す。ループから呼ぶ。
// close と同じく写しを捨てて textDocument/didClose を送り、この client が送った診断を消すために、空の diagnostic/set を b の版で送る。
//
// 本体は、buffer/close と違い、送り直したバッファの診断を自分では消さない。
// 空の一覧を送らないと、言語サーバーの無いファイルの種類に変えた後も、古い診断の印と下線が残り続ける（#267）。
// 空の一覧は mu を持ったまま送るので、それより前に接続の読み取りの goroutine が送った診断（publishDiagnostics）の後に届く。
// 写しを捨てた後に届く診断は、docByPath で文書が見つからないので捨てる。
// インレイヒントも、同じ理由で空の virtualText/set を送って消す（本体は送り直したバッファの仮想テキストを自分では消さない）。
//
// req: FR-30, FR-255
func (c *client) detach(b *server.Buffer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closeLocked(b.ID()) {
		return
	}
	c.x.setDiagnostics(ext.DiagnosticSetParams{Buffer: b.ID(), Version: b.Version(), Diagnostics: []ext.Diagnostic{}})
	if err := c.x.srv.SetVirtualTexts(ext.VirtualTextSetParams{Buffer: b.ID(), Version: b.Version(), VirtualTexts: []ext.VirtualText{}}); err != nil {
		c.logf("virtualText/set: %v", err)
	}
}

// syncs は、バッファ id を、パス path、ファイルの種類 fileType の文書として同期しているかを返す。ループから呼ぶ。
// 1 つの言語サーバーが複数のファイルの種類を受け持つので、languageId の変わる種類の変更（typescript から typescriptreact）は、
// 外して開き直す（言語サーバーの文書の languageId は didOpen でしか伝えられない）。
func (c *client) syncs(id ext.BufferID, path, fileType string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.docs[id]
	return ok && d.path == path && d.languageID == c.cfg.languageID(fileType)
}

// didSave は、言語サーバーが保存の通知を求めていれば、textDocument/didSave を送る。ループから呼ぶ。
//
// req: FR-30
func (c *client) didSave(e *server.BufferDidSaveEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.docs[e.Params.Buffer]
	p := c.readyProc()
	if !ok || p == nil || !p.save {
		return
	}
	params := didSaveParams{TextDocument: textDocumentIdentifier{URI: d.uri}}
	if p.saveText {
		if cur := d.current(); cur.version == e.Params.Version {
			text := d.lspText()
			params.Text = &text
		}
	}
	c.notify(p, lspDidSave, params)
}

// lspVersion は、本体のバージョン番号を LSP の文書の版の番号にする。
// 本体のバージョン番号は 1 ずつ増えるので、int64 の範囲を超えることは無い前提である。
func lspVersion(v ext.Version) int64 {
	return int64(v) //nolint:gosec // 本体のバージョン番号は編集ごとに 1 ずつ増えるだけなので、int64 の範囲を超えない
}

// notify は、言語サーバーへ通知を送る。送れなければログに書く（接続が終わっていれば、監視の goroutine が後始末をする）。
func (c *client) notify(p *proc, method string, params any) {
	if err := p.conn.Notify(method, params); err != nil {
		c.logf("%s: %s: %v", c.name(), method, err)
	}
}

// start は、言語サーバーを起動し、initialize を送り、応答が届いたら開いている文書を送る。
// 起動の goroutine で動く。
//
// 起動するコマンドは prepare で決める。実行ファイルが見つからず、mise の道具の ID があれば、
// 入れ方を案内するか、mise に入れさせてから mise exec で起動する（FR-143）。入れている間は initialize の期限を数えない。
// 実行ファイルを起動できなければ、再起動しても同じなので、もう起動しない。
// ただし、入れ方を案内しただけのときは、次にファイルを開いたときに探し直す（利用者が案内に従って入れた後に起動できるように）。
// initialize が失敗するか期限を超えたら、プロセスを止める。その後は異常終了と同じく、監視の goroutine が再起動を決める。
//
// req: FR-30
func (c *client) start() {
	root := c.root
	c.mu.Lock()
	stopped := c.stopped
	c.mu.Unlock()
	if stopped {
		c.mu.Lock()
		c.starting = false
		c.mu.Unlock()
		return
	}
	cmd, retry, err := c.prepare(c.ctx, root)
	var p *proc
	if err == nil {
		p, err = c.spawn(root, cmd)
	}
	c.mu.Lock()
	c.starting = false
	switch {
	case err != nil && (c.stopped || retry):
		c.mu.Unlock()
		return
	case err != nil:
		c.failed = true
		c.mu.Unlock()
		c.logf("%s: %v", c.name(), err)
		if !errors.Is(err, errReported) {
			c.x.showMessage(ext.MessageError, fmt.Sprintf("言語サーバー %s を起動できない: %v", c.name(), err))
		}
		return
	case c.stopped:
		p.stopping = true
		c.mu.Unlock()
		_ = p.cmd.Process.Kill()
		go c.watch(p)
		return
	}
	c.proc = p
	c.mu.Unlock()
	go c.watch(p)

	params := initializeParams{
		ProcessID:             os.Getpid(),
		ClientInfo:            clientInfo{Name: "vi-imitation"},
		RootURI:               pathToURI(root),
		InitializationOptions: c.cfg.InitializationOptions,
		Capabilities:          json.RawMessage(clientCapabilities),
		WorkspaceFolders:      []workspaceFolder{{URI: pathToURI(root), Name: filepath.Base(root)}},
	}
	ctx, cancel := context.WithTimeout(c.x.ctx, c.initTimeout)
	defer cancel()
	var res initializeResult
	if err := p.conn.Call(ctx, lspInitialize, params, &res); err != nil {
		c.logf("%s: initialize: %v", c.name(), err)
		_ = p.cmd.Process.Kill()
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc != p || p.stopping {
		return
	}
	p.caps = res.Capabilities
	p.enc = parseEncoding(res.Capabilities.PositionEncoding)
	p.sync, p.save, p.saveText = res.Capabilities.syncOptions()
	p.ready = true
	p.readySince = time.Now()
	c.notify(p, lspInitialized, struct{}{})
	for _, d := range c.docs {
		c.didOpen(p, d)
		c.scheduleInlay(p, d)
	}
}

// spawn は、言語サーバーのプロセスを起動し、その標準入出力の上の接続を作る。
//
// 標準入出力のパイプは os.Pipe で自分で作る。exec.Cmd の StdoutPipe は Wait が閉じるので、
// 監視の goroutine の Wait と、接続の読み取りが競合するからである。
// 言語サーバーの標準エラー出力は、拡張機能の標準エラー出力へそのまま流す。本体はそれをログとして記録する（基本設計 5.3）。
// 起動は startProcess で行い、拡張機能が強制終了されても言語サーバーが残らないようにする。
func (c *client) spawn(root string, l command) (*proc, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, err
	}
	cmd := exec.Command(l.path, l.args...) //nolint:gosec // 起動するのは利用者が設定した言語サーバーである
	cmd.Dir = root
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = os.Stderr
	err = startProcess(cmd)
	_ = inR.Close()
	_ = outW.Close()
	if err != nil {
		_ = inW.Close()
		_ = outR.Close()
		return nil, err
	}
	p := &proc{cmd: cmd, exited: make(chan struct{}), gen: procGen.Add(1), progress: map[string]bool{}}
	p.conn = rpc.NewConn(outR, inW, rpc.HandlerFuncs{
		Request: func(_ context.Context, req *rpc.Request, reply rpc.Replier) {
			c.serverRequest(req, reply)
		},
		Notification: func(_ context.Context, n *rpc.Notification) {
			c.serverNotification(p, n)
		},
	}, rpc.WithCancelMethod(lspCancelRequest))
	return p, nil
}

// watch は、言語サーバーのプロセスが終了するまで待ち、終了したら接続を閉じる。
// 拡張機能が止めたのでなければ異常終了として扱い、回数の上限までは待ってから再起動する。
// 回数は、短い間に失敗を繰り返す言語サーバーだけを止めるためのもので、resetAfter より長く稼働した後の異常終了では数え直す。
// 異常終了した言語サーバーの診断は古くなるので、開いている文書の診断を消す。インレイヒントも消す。
// 再起動した言語サーバーには、開いている文書を送り直した後に、インレイヒントを問い合わせ直す（start）。
//
// req: FR-30, FR-255
func (c *client) watch(p *proc) {
	err := p.cmd.Wait()
	_ = p.conn.Close()
	close(p.exited)

	c.mu.Lock()
	c.endProgress(p)
	if c.proc != p {
		c.mu.Unlock()
		return
	}
	c.proc = nil
	if p.stopping || c.stopped {
		c.mu.Unlock()
		return
	}
	for _, d := range c.docs {
		c.x.setDiagnostics(ext.DiagnosticSetParams{Buffer: d.buffer, Version: d.current().version, Diagnostics: []ext.Diagnostic{}})
		c.resetInlay(d, true)
	}
	if !p.readySince.IsZero() && time.Since(p.readySince) >= c.resetAfter {
		c.restarts = 0
	}
	c.restarts++
	n := c.restarts
	if n > c.maxRestarts {
		c.failed = true
		c.mu.Unlock()
		c.logf("%s exited (%v); giving up after %d restarts", c.name(), err, c.maxRestarts)
		c.x.showMessage(ext.MessageError, fmt.Sprintf("言語サーバー %s が終了した。再起動の回数の上限（%d 回）に達したので、もう起動しない", c.name(), c.maxRestarts))
		return
	}
	c.starting = true
	c.mu.Unlock()
	c.logf("%s exited (%v); restarting (%d/%d)", c.name(), err, n, c.maxRestarts)
	c.x.showMessage(ext.MessageWarning, fmt.Sprintf("言語サーバー %s が終了した。再起動する（%d/%d 回目）", c.name(), n, c.maxRestarts))
	time.AfterFunc(c.restartDelay*time.Duration(n), c.start)
}

// stop は、稼働中の言語サーバーへ shutdown と exit を送って止める。ctx の期限までに終わらなければ強制終了する。
// 起動中（initialize の応答の前）の言語サーバーは、すぐに強制終了する。
// mise に入れさせている途中なら、mise を終わらせ（c.ctx の取り消し）、mise が終わるのを ctx の期限まで待つ。
// これより後は起動しない。拡張機能が終わるときに呼ぶ。
func (c *client) stop(ctx context.Context) {
	c.cancel()
	c.mu.Lock()
	c.stopped = true
	p := c.proc
	ready := p != nil && p.ready
	if p != nil {
		p.stopping = true
	}
	miseDone := c.miseDone
	c.mu.Unlock()
	if miseDone != nil {
		// 入れている途中の mise は、c.ctx の取り消しで SIGINT を受け取り、npm などを終わらせてから終わる（runMise）。
		// それを ctx の期限まで待つ。待たずに戻ると、拡張機能が先に終わり、mise の後始末の途中で本体に強制終了されうる。
		select {
		case <-miseDone:
		case <-ctx.Done():
		}
	}
	if p == nil {
		return
	}
	if ready {
		// shutdown の応答を待ってから exit を送る（LSP 3.17 の仕様）。
		if err := p.conn.Call(ctx, lspShutdown, nil, nil); err == nil {
			_ = p.conn.Notify(lspExit, nil)
			_ = p.conn.Flush(ctx)
		}
	} else {
		// initialize の前の言語サーバーは shutdown を受け付けないので、待たずに止める。
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.exited:
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		<-p.exited
	}
}

// serverRequest は、言語サーバーから届いたリクエストに答える。接続の読み取りの goroutine から呼ばれるので、待たない。
//
// クライアントの能力として申し出た範囲と、申し出なくても送ってくる言語サーバーがあるものに、最小限の応答を返す。
// それ以外は MethodNotFound で答える。
func (c *client) serverRequest(req *rpc.Request, reply rpc.Replier) {
	var result any
	var rerr error
	switch req.Method {
	case lspConfiguration:
		var p configurationParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			rerr = &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
			break
		}
		items := make([]json.RawMessage, len(p.Items))
		for i, it := range p.Items {
			items[i] = lookupSection(c.settings, it.Section)
		}
		result = items
	case lspRegisterCapability, lspUnregisterCapability:
		// 動的な登録には対応しないが、受け付けたことにする。
	case lspWorkDoneProgress:
		// 進み具合の識別子を受け付ける。識別子は、続く $/progress の begin で覚える（progress.go）ので、ここでは覚えない。
		// LSP 3.17 では、作ることを断ったら（誤りで答えたら）サーバーはその識別子で送らないので、断らずに null で答える。
	case lspShowMessageRequest:
		// 選択肢は示さず、メッセージだけを表示する。結果の null は、何も選ばなかったことを表す。
		var p showMessageParams
		if json.Unmarshal(req.Params, &p) == nil {
			c.showServerMessage(p)
		}
	case lspApplyEdit:
		result = map[string]any{"applied": false, "failureReason": "vi-imitation does not support workspace/applyEdit"}
	case lspWorkspaceFolders:
		result = []workspaceFolder{{URI: pathToURI(c.root), Name: filepath.Base(c.root)}}
	case lspInlayHintRefresh:
		// 開いている文書のインレイヒントを問い合わせ直す。予約するだけなので、応答を遅らせない（inlay.go。FR-256）。
		c.refreshInlay()
	default:
		rerr = &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "method not found: " + req.Method}
	}
	if err := reply(result, rerr); err != nil {
		c.logf("%s: reply to %s: %v", c.name(), req.Method, err)
	}
}

// serverNotification は、言語サーバーから届いた通知を処理する。接続の読み取りの goroutine から呼ばれるので、待たない。
func (c *client) serverNotification(p *proc, n *rpc.Notification) {
	switch n.Method {
	case lspPublishDiagnostics:
		var params publishDiagnosticsParams
		if err := json.Unmarshal(n.Params, &params); err != nil {
			c.logf("%s: %s: %v", c.name(), n.Method, err)
			return
		}
		c.publishDiagnostics(p, &params)
	case lspShowMessage:
		var params showMessageParams
		if json.Unmarshal(n.Params, &params) == nil {
			c.showServerMessage(params)
		}
	case lspLogMessage:
		var params showMessageParams
		if json.Unmarshal(n.Params, &params) == nil {
			c.logf("%s: %s", c.name(), params.Message)
		}
	case lspProgress:
		var params progressParams
		if err := json.Unmarshal(n.Params, &params); err != nil {
			c.logf("%s: %s: %v", c.name(), n.Method, err)
			return
		}
		c.progress(p, params)
	}
}

// showServerMessage は、window/showMessage を本体の message/show で表示する。ログ（type 4 以上）はログに書くだけにする。
func (c *client) showServerMessage(p showMessageParams) {
	var level ext.MessageLevel
	switch p.Type {
	case messageError:
		level = ext.MessageError
	case messageWarning:
		level = ext.MessageWarning
	case messageInfo:
		level = ext.MessageInfo
	default:
		c.logf("%s: %s", c.name(), p.Message)
		return
	}
	c.x.showMessage(level, c.name()+": "+p.Message)
}

// publishDiagnostics は、textDocument/publishDiagnostics の診断を本体の位置に変換し、diagnostic/set で送る（基本設計 6.3）。
//
// 診断の version は、didOpen と didChange で送った本体のバージョン番号なので、そのまま diagnostic/set の version にする。
// 位置の変換には、その版の行を使う。その版をもう覚えていなければ、診断を捨てる（言語サーバーは新しい版の診断を後で送る）。
// version の無い診断は、受け取った時点の写しの版の番号を付け、その版の行で変換する。
// 開いていないファイルの診断は捨てる。
//
// req: FR-30
func (c *client) publishDiagnostics(p *proc, params *publishDiagnosticsParams) {
	path, ok := uriToPath(params.URI)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc != p || !p.ready {
		return
	}
	d := c.docByPath(path)
	if d == nil {
		return
	}
	v := d.current().version
	if params.Version != nil {
		if *params.Version < 0 {
			return
		}
		v = ext.Version(*params.Version)
	}
	ls, err := d.at(v)
	if err != nil {
		c.logf("%s: diagnostics for %s: %v; dropped", c.name(), path, err)
		return
	}
	diags := make([]ext.Diagnostic, 0, len(params.Diagnostics))
	for _, ld := range params.Diagnostics {
		diags = append(diags, ext.Diagnostic{
			Range:    ls.rangeFromLSP(ld.Range, p.enc),
			Severity: severity(ld.Severity),
			Message:  ld.Message,
			Source:   ld.Source,
			Code:     codeString(ld.Code),
		})
	}
	c.x.setDiagnostics(ext.DiagnosticSetParams{Buffer: d.buffer, Version: v, Diagnostics: diags})
}

// severity は、LSP の DiagnosticSeverity を本体の重要度にする。
// LSP 3.17 は、省いたときの解釈をクライアントに任せているので、省略と知らない値はエラーとして扱う。
func severity(s int) ext.DiagnosticSeverity {
	switch s {
	case severityWarning:
		return ext.SeverityWarning
	case severityInformation:
		return ext.SeverityInfo
	case severityHint:
		return ext.SeverityHint
	default:
		return ext.SeverityError
	}
}

// docByPath は、mu を持った状態で、パスが path の文書を返す。無ければ nil である。
func (c *client) docByPath(path string) *document {
	for _, d := range c.docs {
		if d.path == path {
			return d
		}
	}
	return nil
}

// request は、言語サーバーへのリクエストの送り先と、その時点の文書の版である。
type request struct {
	c       *client
	future  *rpc.Future
	enc     encoding
	buffer  ext.BufferID
	version ext.Version
	path    string
	lines   lines
	// finalNewline は、lines を言語サーバーへ送ったときに末尾に "\n" を足したかどうか（document の finalNewline）である。
	finalNewline bool
}

// send は、バッファ id の版 version の文書について、言語サーバーへリクエスト method を送る。ループから呼ぶ。
// 稼働中でないか、文書を持っていないか、写しの版が version でないか、言語サーバーがその機能を持たない（has が false）なら送らない。
// params は、文書の URI と、位置を変換する関数から作る。
func (c *client) send(
	id ext.BufferID, version ext.Version, method string,
	has func(*serverCapabilities) bool,
	params func(uri string, ls lines, enc encoding) any,
) (*request, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.docs[id]
	switch {
	case !ok:
		return nil, fmt.Errorf("buffer %d is not synchronized with %s", id, c.name())
	case d.current().version != version:
		return nil, fmt.Errorf("buffer %d: version %d does not match the replica %d", id, version, d.current().version)
	}
	p := c.readyProc()
	if p == nil {
		return nil, errNotReady
	}
	if !has(&p.caps) {
		return nil, errUnsupported
	}
	cur := d.current()
	f, err := p.conn.Go(method, params(d.uri, cur.lines, p.enc))
	if err != nil {
		return nil, err
	}
	return &request{c: c, future: f, enc: p.enc, buffer: id, version: version, path: d.path, lines: cur.lines, finalNewline: d.finalNewline}, nil
}

// errUnsupported は、言語サーバーがその機能を持たないことを表す。
var errUnsupported = errors.New("language server does not support the request")

// wait は、応答を待ち、結果を out へ読み込む。ctx が終わったら、言語サーバーへ $/cancelRequest を送って待つのをやめる。
func (r *request) wait(ctx context.Context, out any) error {
	select {
	case <-r.future.Ready():
	case <-ctx.Done():
		_ = r.future.Cancel()
		return ctx.Err()
	}
	return r.future.Result(out)
}
