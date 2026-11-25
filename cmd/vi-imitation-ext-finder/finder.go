package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// 拡張機能の状態と、本体とのやり取り。
//
// ファインダー 1 つ（開いたポップアップ 1 つ）を picker で持つ。本体からの知らせ（OnView）は ext/server のループの上で呼ばれるので、
// そこでは picker の入力を書き換えて起こすだけにし、絞り込みと rg の起動は picker ごとの goroutine（picker.loop）で行う。
// ループの上で重い計算をすると、ping の応答が遅れて本体に強制終了されるためである（dev-cycle 2.2）。
//
// 入力の番号（ext.InputChangeParams.Seq）は、届いた最新のものを picker が持ち、送る一覧にはその計算に使った入力の番号を付ける。
// 本体は古い番号の一覧を捨てるので、計算の途中で次の入力が届いても、古い計算の結果を送ってかまわない。

// 既定の設定。
const (
	defaultLimit = 300
	// refreshInterval は、候補を読みながら（ファイルの一覧、rg の結果）一覧を送り直す間隔である。
	refreshInterval = 100 * time.Millisecond
)

// settings は、initialize の settings である（main の doc）。
type settings struct {
	Hidden *bool `json:"hidden"`
	Limit  int   `json:"limit"`
}

// finder は、拡張機能 1 つの状態である。
type finder struct {
	srv *server.Server
	// ctx は、拡張機能が終わるまで続く ctx である。本体へのリクエスト（OpenPopup など）はループの外からこの ctx で送る。
	ctx context.Context

	mu      sync.Mutex
	hidden  bool
	limit   int
	pickers map[ext.ViewID]*picker
	// early は、OpenPopup の応答より先に届いた、まだ picker に結び付けていないポップアップの知らせである。
	// ext/server は応答を待っている goroutine へ、知らせをループへ渡すので、応答と知らせの順は保たれない。
	early map[ext.ViewID]*earlyEvents
	// last は、最後に開いたファインダーの種類と入力である（finder.resume）。
	last *resumeState
}

// inputState は、入力の行の中身と番号である。
type inputState struct {
	text string
	seq  uint64
}

// earlyEvents は、picker に結び付ける前に届いた知らせ（最新の入力、選んだ項目、閉じたこと）である（PR #249 のレビューの推奨 5）。
type earlyEvents struct {
	input     *inputState
	selected  *string
	selection uint64
	closed    bool
}

// resumeState は、finder.resume で開き直すための、最後のファインダーの種類と場所と入力である。
type resumeState struct {
	kind  string
	dir   string
	query string
	// static は、作り直せない候補（finder.lines の行）である。
	static []item
}

// newFinder は、拡張機能を作り、コマンドと知らせのハンドラーを登録する。
func newFinder(ctx context.Context) *finder {
	f := &finder{
		srv: server.New(), ctx: ctx, hidden: true, limit: defaultLimit,
		pickers: map[ext.ViewID]*picker{}, early: map[ext.ViewID]*earlyEvents{},
	}
	f.srv.OnInitialize(func(_ context.Context, e *server.InitializeEvent) error {
		var s settings
		if len(e.Settings) > 0 {
			if err := json.Unmarshal(e.Settings, &s); err != nil {
				return fmt.Errorf("finder の設定を読めない: %w", err)
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if s.Hidden != nil {
			f.hidden = *s.Hidden
		}
		if s.Limit > 0 {
			f.limit = s.Limit
		}
		return nil
	})
	// コマンドのハンドラーは、ポップアップを開き終えたら（開けなかったときも）done を 1 回呼ぶ。
	for id, h := range map[string]commandFunc{
		"finder.files": func(_ *server.Buffer, _ *ext.CommandExecuteParams, done func()) error {
			return f.openFiles("", "", done)
		},
		"finder.grep": func(_ *server.Buffer, _ *ext.CommandExecuteParams, done func()) error {
			return f.openGrep("", "", done)
		},
		"finder.grepDir":   f.grepDir,
		"finder.grepWord":  f.grepWord,
		"finder.buffers":   f.editorListCommand(ext.ListTabs),
		"finder.keys":      f.editorListCommand(ext.ListKeys),
		"finder.commands":  f.editorListCommand(ext.ListCommands),
		"finder.registers": f.editorListCommand(ext.ListRegisters),
		"finder.jumps":     f.editorListCommand(ext.ListJumps),
		"finder.help":      f.editorListCommand(ext.ListHelp),
		"finder.lines":     f.lines,
		"finder.resume":    func(_ *server.Buffer, _ *ext.CommandExecuteParams, done func()) error { return f.resume(done) },
	} {
		f.srv.CommandAsync(id, func(_ context.Context, b *server.Buffer, p *ext.CommandExecuteParams, reply func(ext.CommandExecuteResult, error)) {
			if err := noArgs(p); err != nil {
				_ = f.srv.ShowMessage(ext.MessageError, err.Error())
				reply(ext.CommandExecuteResult{}, nil)
				return
			}
			// 再生中（sync）は、ポップアップを開いてから応答する。本体は応答を待つ間に popup/open を処理してポップアップへフォーカスを移し、
			// 応答の後の再生の残りのキーをポップアップへ渡す（PR #249 のレビューの必須 2）。再生中でなければ、すぐに応答する。
			var once sync.Once
			answer := func(ok bool) { once.Do(func() { reply(ext.CommandExecuteResult{OK: ok}, nil) }) }
			done := func() {}
			if p.Sync {
				done = func() { answer(true) }
			}
			if err := h(b, p, done); err != nil {
				_ = f.srv.ShowMessage(ext.MessageError, err.Error())
				answer(false)
				return
			}
			if !p.Sync {
				answer(true)
			}
		})
	}
	f.srv.OnView(f.onView)
	return f
}

// commandFunc は、ファインダーを開くコマンドの中身である。b と p はハンドラーが戻るまでの間だけ読める。
// ポップアップを開き終えたか、開けなかったら done を呼ぶ。誤りを返したときは done を呼ばない。
type commandFunc func(b *server.Buffer, p *ext.CommandExecuteParams, done func()) error

// noArgs は、コマンドラインから引数を付けて呼ばれたら誤りを返す（ext の doc の「引数を取らないコマンド」）。
func noArgs(p *ext.CommandExecuteParams) error {
	if len(p.Args) == 0 {
		return nil
	}
	var a struct{ Text string }
	if err := json.Unmarshal(p.Args, &a); err == nil && a.Text != "" {
		return fmt.Errorf("%s は引数を取りません", p.ID)
	}
	return nil
}

// workDir は、操作中の client の作業ディレクトリ（editor/state の workDir）を返す。
// 本体が知らせなければ（古い本体）、拡張機能のプロセスの作業ディレクトリである。ループの外で呼ぶ。
func (f *finder) workDir() string {
	if st, err := f.srv.EditorState(f.ctx); err == nil && st.WorkDir != "" {
		return st.WorkDir
	}
	if d, err := os.Getwd(); err == nil {
		return d
	}
	return "."
}

// closeAll は、すべての picker の goroutine と子プロセスを止める。
func (f *finder) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, p := range f.pickers {
		p.cancel()
		delete(f.pickers, id)
	}
}

// onView は、本体からのポップアップの知らせを、その picker へ渡す。ループの上で呼ばれるので、重い処理はしない。
func (f *finder) onView(_ context.Context, e *server.ViewEvent) {
	f.mu.Lock()
	p := f.pickers[e.View]
	if p == nil {
		ev := f.early[e.View]
		if ev == nil {
			ev = &earlyEvents{}
			f.early[e.View] = ev
		}
		switch e.Kind {
		case server.ViewInputChanged:
			if ev.input == nil || e.Seq >= ev.input.seq {
				ev.input = &inputState{text: e.Item, seq: e.Seq}
			}
		case server.ViewSelected:
			item := e.Item
			ev.selected, ev.selection = &item, e.Selection
		case server.ViewClosed:
			ev.closed = true
		case server.ViewKey, server.ViewExpanded, server.ViewTabChanged, server.ViewChildSelected:
		}
		f.mu.Unlock()
		return
	}
	if e.Kind == server.ViewClosed {
		delete(f.pickers, e.View)
	}
	f.mu.Unlock()
	switch e.Kind {
	case server.ViewInputChanged:
		p.setInput(inputState{text: e.Item, seq: e.Seq})
	case server.ViewSelected:
		go p.choose(e.Item, e.Selection)
	case server.ViewClosed:
		p.cancel()
	case server.ViewKey, server.ViewExpanded, server.ViewTabChanged, server.ViewChildSelected:
	}
}

// item は、ファインダーの候補 1 つである。
type item struct {
	id    string
	label string
	// spans は、label の中で、絞り込みの一致の前から付けておく色の範囲である（rg の一致）。
	spans []ext.ViewSpan
	// open は、選んだときに editor/open で開く先である。
	open *ext.EditorOpenParams
	// reg は、レジスターの候補の中身である。選んだらカーソルの後に貼る。
	reg *ext.ListRegister
	// exec は、キーとコマンドの候補で、選んだときに editor/execute で本体に実行させるものである。
	exec *ext.EditorExecuteParams
	// preview は、カーソルがあるときにプレビューに出すもの（FR-149）である。無ければ出さない。
	preview *ext.ViewPreview
	// file は、候補が指すファイル（FR-152）である。本体がファイルの種類のアイコンを付ける。ファイルを指さない候補では nil。
	file *ext.ViewFile
}

// openFile は、選んだときに開く先 op が指すファイル（FR-152 のアイコンを付けるもの）を返す。
// ファイルでないバッファ（op.Path が空）なら nil を返す。
//
// req: FR-152
func openFile(op *ext.EditorOpenParams) *ext.ViewFile {
	if op.Path == "" {
		return nil
	}
	return &ext.ViewFile{Path: op.Path}
}

// openPreview は、選んだときに開く先 op を、プレビューに出すもの（op のファイルかバッファの、op の位置の行）にする。
//
// req: FR-149
func openPreview(op *ext.EditorOpenParams) *ext.ViewPreview {
	pv := &ext.ViewPreview{Path: op.Path, Buffer: op.Buffer}
	if op.Buffer != nil {
		pv.Path = ""
	}
	if op.Position != nil {
		line := op.Position.Line
		pv.Line = &line
	}
	return pv
}

// previewKinds は、候補にプレビューを付けるファインダーの種類である（#288 のオーナーの決めの表）。
var previewKinds = map[string]bool{"files": true, "grep": true, "lines": true, string(ext.ListJumps): true}

// picker は、開いたファインダー 1 つである。
type picker struct {
	f     *finder
	kind  string
	title string
	// dir は、ファイルと検索の場所（作業ディレクトリかバッファのディレクトリ）である。
	dir  string
	view ext.ViewID
	// grep は、入力を絞り込みに使わず、入力が変わるたびに rg で検索し直すことを表す。
	grep bool

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}

	mu      sync.Mutex
	items   []item
	labels  []string
	loading bool
	input   inputState
	// shown は、最後に送った一覧の項目である（ID から）。選んだ項目を引く。
	shown map[string]item
	// err は、候補を読めなかった理由である。見出しに出す。
	err error
	// grepGen は、検索（grep）で rg を起動するたびに 1 つ進める番号で、grepSeq は今の検索の入力の番号（input/change の seq）である。
	// 前の検索の goroutine が、替わった後の候補に足したり、新しい番号で一覧を送ったりしないために使う。
	grepGen, grepSeq uint64
}

// newPicker は、種類 kind の picker を作る。候補は呼び出し側が入れる。
func (f *finder) newPicker(kind, title, dir, query string) *picker {
	ctx, cancel := context.WithCancel(f.ctx)
	return &picker{
		f: f, kind: kind, title: title, dir: dir, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), input: inputState{text: query}, shown: map[string]item{},
	}
}

// show は、p をポップアップに開き、picker の goroutine を始める。開く前に、今の候補で最初の一覧を作って一緒に送る。
// 開き終えたか開けなかったら done を呼ぶ。ループの外から呼ぶ。
func (p *picker) show(done func()) error {
	defer done()
	p.f.mu.Lock()
	p.f.last = &resumeState{kind: p.kind, dir: p.dir, query: p.input.text}
	if p.kind == "lines" {
		p.f.last.static = p.items
	}
	p.f.mu.Unlock()
	c, _ := p.component()
	r, err := p.f.srv.OpenPopup(p.f.ctx, ext.PopupOpenParams{Placement: ext.PopupCenter, Component: c})
	if err != nil {
		p.cancel()
		return fmt.Errorf("ファインダーを開けません: %w", err)
	}
	p.f.mu.Lock()
	p.view = r.View
	early := p.f.early[r.View]
	delete(p.f.early, r.View)
	if early != nil && early.closed {
		// 開いた応答より先に閉じた知らせが届いた。goroutine と子プロセスを始めない（PR #249 のレビューの推奨 5）。
		p.f.mu.Unlock()
		p.cancel()
		return nil
	}
	p.f.pickers[r.View] = p
	p.f.mu.Unlock()
	if early != nil && early.input != nil {
		p.setInput(*early.input)
	}
	go p.loop()
	p.poke()
	if early != nil && early.selected != nil {
		go p.choose(*early.selected, early.selection)
	}
	return nil
}

// poke は、picker の goroutine を起こす。既に起こしていれば何もしない。
func (p *picker) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// setInput は、入力が in に変わったことを覚えて、picker の goroutine を起こす。古い番号の入力は捨てる。
func (p *picker) setInput(in inputState) {
	p.mu.Lock()
	if in.seq < p.input.seq {
		p.mu.Unlock()
		return
	}
	p.input = in
	p.mu.Unlock()
	p.f.mu.Lock()
	if p.f.last != nil && p.f.last.kind == p.kind {
		p.f.last.query = in.text
	}
	p.f.mu.Unlock()
	p.poke()
}

// loop は、起こされるたびに、今の入力で一覧を作って送る。検索（grep）では、入力が変わったら rg を起動し直す。
func (p *picker) loop() {
	// 検索の ctx は p.ctx の子なので、p.ctx が終われば（ポップアップを閉じたら）rg も止まる。
	grepCancel := context.CancelFunc(func() {})
	grepQuery := ""
	started := false
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.wake:
		}
		if !p.grep {
			p.send()
			continue
		}
		p.mu.Lock()
		in := p.input
		p.mu.Unlock()
		if started && in.text == grepQuery {
			p.send()
			continue
		}
		grepCancel()
		started, grepQuery = true, in.text
		ctx, cancel := context.WithCancel(p.ctx)
		grepCancel = cancel
		// 検索の番号は、goroutine を始める前にここで進める。goroutine の中で進めると、取り消した前の入力の goroutine が
		// 後から動いたときに大きい番号を取って今の検索になり、新しい入力の検索の結果を捨てる（#262）。
		gen := p.beginGrep(in)
		go func() {
			defer cancel()
			if h := grepHook; h != nil {
				defer h(in)()
			}
			p.runGrep(ctx, in, gen)
		}()
	}
}

// grepHook は、テストから検索の goroutine の動く順を決める口である。nil でなければ、検索の goroutine の最初に入力を渡して呼び、
// 返った関数を goroutine の最後に呼ぶ。テストは、picker の goroutine（loop）を始める前に設定する。
var grepHook func(in inputState) (done func())

// send は、今の候補を今の入力で絞り込み、view/update で送る。
func (p *picker) send() {
	c, seq := p.component()
	if err := p.f.srv.UpdateView(ext.ViewUpdateParams{View: p.view, Seq: seq, Component: c}); err != nil {
		fmt.Fprintf(os.Stderr, "view/update: %v\n", err)
	}
}

// component は、今の候補を今の入力で絞り込んだ部品と、その入力の番号を返す。検索（grep）では候補を絞らずに並べる。
func (p *picker) component() (ext.ViewComponent, uint64) {
	p.f.mu.Lock()
	limit := p.f.limit
	p.f.mu.Unlock()
	p.mu.Lock()
	in, labels, items, loading, perr := p.input, p.labels, p.items, p.loading, p.err
	if p.grep {
		// 検索の一覧は、届いた最新の入力でなく、その候補を探した rg の入力の番号を付ける（PR #249 のレビューの推奨 4）。
		in.seq = p.grepSeq
	}
	p.mu.Unlock()
	var out []ext.ViewItem
	shown := map[string]item{}
	matched := len(items)
	if p.grep {
		for _, it := range items[:min(len(items), limit)] {
			out = append(out, ext.ViewItem{ID: it.id, Label: it.label, Highlights: it.spans, Preview: it.preview, File: it.file})
			shown[it.id] = it
		}
	} else {
		var rs []result
		rs, matched = filter(labels, in.text, limit)
		for _, r := range rs {
			it := items[r.index]
			out = append(out, ext.ViewItem{ID: it.id, Label: it.label, Highlights: r.spans, Preview: it.preview, File: it.file})
			shown[it.id] = it
		}
	}
	p.mu.Lock()
	p.shown = shown
	p.mu.Unlock()
	title := fmt.Sprintf("%s  %d/%d", p.title, matched, len(items))
	switch {
	case perr != nil:
		title += "  " + perr.Error()
	case loading:
		title += "（読み込み中）"
	}
	// プレビューを付ける種類では、候補が届く前（検索の入力が空のとき）から欄を持たせ、最初の候補で窓の幅が変わらないようにする。
	c := ext.ViewComponent{Kind: ext.ViewFinder, Title: title, Text: in.text, Items: out, Preview: previewKinds[p.kind]}
	return c, in.seq
}

// add は、候補を足す。
func (p *picker) add(its ...item) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.addLocked(its)
}

// addLocked は、p.mu を持ったまま候補を足す。
func (p *picker) addLocked(its []item) {
	for _, it := range its {
		it.label = cleanLabel(it.label)
		p.items = append(p.items, it)
		p.labels = append(p.labels, it.label)
	}
}

// cleanLabel は、候補の表示の文字列から、部品に入れられない改行を除き（\r と \n を空白にする）、不正な UTF-8 を置き換える。
func cleanLabel(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

// choose は、一覧で選ばれた項目 id を開く。ポップアップを閉じてから開く（開いた先のタブへカーソルを移すため）。
// selection は view/select の選択の番号で、キーとコマンドの実行（editor/execute）に付けて返す。ループの外で呼ぶ。
func (p *picker) choose(id string, selection uint64) {
	p.mu.Lock()
	it, ok := p.shown[id]
	p.mu.Unlock()
	if !ok {
		return
	}
	_ = p.f.srv.CloseView(p.view)
	p.f.mu.Lock()
	delete(p.f.pickers, p.view)
	p.f.mu.Unlock()
	p.cancel()
	var err error
	switch {
	case it.open != nil:
		_, err = p.f.srv.OpenEditor(p.f.ctx, *it.open)
	case it.reg != nil:
		err = p.f.paste(*it.reg)
	case it.exec != nil:
		x := *it.exec
		x.Selection = selection
		// 断られたときは、本体が理由をメッセージの行に出すので、ここでは出さない。
		if xerr := p.f.srv.Execute(p.f.ctx, x); xerr != nil {
			fmt.Fprintf(os.Stderr, "editor/execute: %v\n", xerr)
		}
	}
	if err != nil {
		_ = p.f.srv.ShowMessage(ext.MessageError, fmt.Sprintf("開けません: %v", err))
	}
}

// openFiles は、dir の下のファイルのファインダーを開く（finder.files）。dir が空なら、操作中の client の作業ディレクトリである。
func (f *finder) openFiles(dir, query string, done func()) error {
	f.mu.Lock()
	hidden := f.hidden
	f.mu.Unlock()
	name, lister := pickFileLister()
	go func() {
		if dir == "" {
			dir = f.workDir()
		}
		p := f.newPicker("files", "ファイル", dir, query)
		p.loading = true
		if err := p.show(done); err != nil {
			_ = f.srv.ShowMessage(ext.MessageError, err.Error())
			return
		}
		var batch []item
		last := time.Now()
		err := lister(p.ctx, dir, hidden, func(rel string) {
			abs := filepath.Join(dir, rel)
			batch = append(batch, item{
				id: rel, label: rel, open: &ext.EditorOpenParams{Path: abs, Focus: true},
				preview: &ext.ViewPreview{Path: abs}, file: &ext.ViewFile{Path: abs},
			})
			if time.Since(last) >= refreshInterval {
				p.add(batch...)
				batch, last = nil, time.Now()
				p.poke()
			}
		})
		p.add(batch...)
		p.mu.Lock()
		p.loading = false
		if err != nil && p.ctx.Err() == nil {
			p.err = fmt.Errorf("%s: %w", name, err)
		}
		p.mu.Unlock()
		p.poke()
	}()
	return nil
}

// openGrep は、dir の下を rg で検索するファインダーを開く（finder.grep、finder.grepDir、finder.grepWord）。
// dir が空なら、操作中の client の作業ディレクトリである。見出しには、作業ディレクトリでなければ探す場所を書く。
func (f *finder) openGrep(dir, query string, done func()) error {
	if _, err := lookPath("rg"); err != nil {
		return errNoRG
	}
	go func() {
		wd := f.workDir()
		if dir == "" {
			dir = wd
		}
		title := "検索"
		if dir != wd {
			title = "検索 " + dir
		}
		p := f.newPicker("grep", title, dir, query)
		p.grep = true
		if err := p.show(done); err != nil {
			_ = f.srv.ShowMessage(ext.MessageError, err.Error())
		}
	}()
	return nil
}

// beginGrep は、入力 in の検索を今の検索にし、その番号を返す。前の検索の候補を消す。
// 入力が届いた順に picker の goroutine（loop）から呼ぶので、新しい入力の検索ほど大きい番号になる。
func (p *picker) beginGrep(in inputState) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.grepGen++
	p.grepSeq = in.seq
	p.items, p.labels, p.err, p.loading = nil, nil, nil, in.text != ""
	return p.grepGen
}

// runGrep は、beginGrep で番号 gen を振った入力 in で rg を起動し、一致を候補にして送る。ctx が終わったら（次の入力が届いたら）止める。
// 候補は上限の数で打ち切る。空の入力では検索しない。
func (p *picker) runGrep(ctx context.Context, in inputState, gen uint64) {
	p.f.mu.Lock()
	hidden, limit := p.f.hidden, p.f.limit
	p.f.mu.Unlock()
	if in.text == "" {
		p.sendIfCurrent(gen)
		return
	}
	var batch []item
	last := time.Now()
	n := 0
	err := runGrep(ctx, p.dir, in.text, hidden, func(m grepMatch) bool {
		batch = append(batch, grepItem(p.dir, m))
		n++
		if time.Since(last) >= refreshInterval {
			p.addIfCurrent(gen, batch)
			batch, last = nil, time.Now()
			p.sendIfCurrent(gen)
		}
		return n < limit
	})
	if ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	if p.grepGen == gen {
		p.addLocked(batch)
		p.loading = false
		p.err = err
	}
	p.mu.Unlock()
	p.sendIfCurrent(gen)
}

// addIfCurrent は、検索 gen がまだ今の検索なら（次の入力の検索に替わっていなければ）候補を足す。
// 比べるのと足すのを同じ鍵の中で行うので、替わった直後の検索の候補に、前の検索の一致が混ざらない（PR #249 のレビューの推奨 4）。
func (p *picker) addIfCurrent(gen uint64, its []item) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.grepGen == gen {
		p.addLocked(its)
	}
}

// sendIfCurrent は、検索 gen がまだ今の検索なら一覧を送る。
func (p *picker) sendIfCurrent(gen uint64) {
	p.mu.Lock()
	current := p.grepGen == gen
	p.mu.Unlock()
	if current {
		p.send()
	}
}

// grepItem は、rg の一致 m を候補にする。表示は "パス:行:列: テキスト"（行の先頭の空白は除く）で、一致した文字に色を付ける。
func grepItem(dir string, m grepMatch) item {
	text := strings.TrimLeftFunc(m.Text, unicode.IsSpace)
	cut := len(m.Text) - len(text)
	prefix := fmt.Sprintf("%s:%d:%d: ", m.Path, m.Line, m.Col+1)
	var spans []ext.ViewSpan
	for _, s := range m.Spans {
		start, end := s[0]-cut, s[1]-cut
		if start < 0 {
			continue
		}
		spans = append(spans, ext.ViewSpan{Start: len(prefix) + start, End: len(prefix) + end, Style: matchStyle})
	}
	label := prefix + text
	if cl := cleanLabel(label); cl != label {
		// 置き換えで長さが変わると範囲がずれるので、色を付けない。
		label, spans = cl, nil
	}
	pos := ext.Position{Line: m.Line - 1, Byte: m.Col}
	op := &ext.EditorOpenParams{Path: filepath.Join(dir, m.Path), Position: &pos, Focus: true}
	return item{
		id: fmt.Sprintf("%s:%d:%d", m.Path, m.Line, m.Col), label: label, spans: spans,
		open: op, preview: openPreview(op), file: openFile(op),
	}
}

// grepDir は、カーソルのあるバッファのファイルのディレクトリの下を検索する（finder.grepDir）。ファイルでないバッファなら作業ディレクトリである。
func (f *finder) grepDir(b *server.Buffer, _ *ext.CommandExecuteParams, done func()) error {
	dir := ""
	if b != nil && b.Path() != "" {
		dir = filepath.Dir(b.Path())
	}
	return f.openGrep(dir, "", done)
}

// grepWord は、カーソルの下の語（英数字と _）を、正規表現の記号を逃がして入力に入れた検索を開く（finder.grepWord）。
func (f *finder) grepWord(b *server.Buffer, p *ext.CommandExecuteParams, done func()) error {
	if b == nil || p.Cursor.Line >= b.LineCount() {
		return fmt.Errorf("%s: バッファを読めません", p.ID)
	}
	w := wordAt(b.Line(p.Cursor.Line), p.Cursor.Byte)
	if w == "" {
		return fmt.Errorf("%s: カーソルの下に語がありません", p.ID)
	}
	return f.openGrep("", regexp.QuoteMeta(w), done)
}

// wordAt は、line のバイトの位置 at にある語（文字、数字、_ の続き）を返す。at が語の上に無ければ、行の中の後ろの最初の語である（Vim の * と同じ）。
func wordAt(line string, at int) string {
	isWord := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	at = min(max(at, 0), len(line))
	start := at
	for start < len(line) {
		r, n := utf8.DecodeRuneInString(line[start:])
		if isWord(r) {
			break
		}
		start += n
	}
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:start])
		if !isWord(r) {
			break
		}
		start -= n
	}
	end := start
	for end < len(line) {
		r, n := utf8.DecodeRuneInString(line[end:])
		if !isWord(r) {
			break
		}
		end += n
	}
	return line[start:end]
}

// lines は、カーソルのあるバッファの行のファインダーを開く（finder.lines）。行は開いた時点の写しである。
func (f *finder) lines(b *server.Buffer, p *ext.CommandExecuteParams, done func()) error {
	if b == nil {
		return fmt.Errorf("%s: バッファを読めません", p.ID)
	}
	id := b.ID()
	its := make([]item, 0, b.LineCount())
	for i := range b.LineCount() {
		pos := ext.Position{Line: i}
		op := &ext.EditorOpenParams{Buffer: &id, Position: &pos, Focus: true}
		its = append(its, item{id: strconv.Itoa(i), label: fmt.Sprintf("%d: %s", i+1, b.Line(i)), open: op, preview: openPreview(op)})
	}
	return f.openStatic("lines", "行", its, "", done)
}

// openStatic は、候補が開いた時点で決まっているファインダーを開く。
func (f *finder) openStatic(kind, title string, its []item, query string, done func()) error {
	p := f.newPicker(kind, title, "", query)
	p.add(its...)
	go func() {
		if err := p.show(done); err != nil {
			_ = f.srv.ShowMessage(ext.MessageError, err.Error())
		}
	}()
	return nil
}

// editorListCommand は、本体の一覧 kind（FR-107）を editor/list で問い合わせて、そのファインダーを開くコマンドのハンドラーを返す。
// 問い合わせはループの外で行う（ハンドラーの中で本体の応答を待つと ErrEditInHandler になる）。
func (f *finder) editorListCommand(kind ext.EditorListKind) commandFunc {
	return func(_ *server.Buffer, _ *ext.CommandExecuteParams, done func()) error {
		go f.openEditorListAsync(kind, "", done)
		return nil
	}
}

// openEditorListAsync は、openEditorList の誤りをメッセージの行に出す。ループの外で呼ぶ。
func (f *finder) openEditorListAsync(kind ext.EditorListKind, query string, done func()) {
	if err := f.openEditorList(kind, query, done); err != nil {
		_ = f.srv.ShowMessage(ext.MessageError, err.Error())
	}
}

// openEditorList は、本体の一覧 kind を問い合わせて、そのファインダーを開く。開き終えたか開けなかったら done を呼ぶ。ループの外で呼ぶ。
func (f *finder) openEditorList(kind ext.EditorListKind, query string, done func()) error {
	r, err := f.srv.ListEditor(f.ctx, kind)
	if err != nil {
		done()
		return fmt.Errorf("本体の一覧 %s を読めません: %w", kind, err)
	}
	title, its := listItems(kind, r)
	p := f.newPicker(string(kind), title, "", query)
	p.add(its...)
	return p.show(done)
}

// listItems は、editor/list の結果 r を候補にし、見出しと一緒に返す。
func listItems(kind ext.EditorListKind, r ext.EditorListResult) (string, []item) {
	var its []item
	switch kind {
	case ext.ListTabs:
		for _, t := range r.Tabs {
			label := t.Name
			if t.Modified {
				label += " [+]"
			}
			if t.ReadOnly {
				label += " [RO]"
			}
			if !t.Own {
				label += fmt.Sprintf("  (client %d)", t.Client)
			}
			buf := t.Buffer
			it := item{id: strconv.FormatUint(uint64(t.Buffer), 10), label: label, open: &ext.EditorOpenParams{Buffer: &buf, Focus: true}}
			if t.Path != "" {
				// ファイルのタブだけにアイコンを付ける（FR-152）。ファイルでないバッファの見出しは、本体が空白でそろえる。
				it.file = &ext.ViewFile{Path: t.Path}
			}
			its = append(its, it)
		}
		return "タブ", its
	case ext.ListCommands:
		for i, c := range r.Commands {
			label := ":" + c.Name
			if c.Short != "" && c.Short != c.Name {
				label += fmt.Sprintf(" (:%s)", c.Short)
			}
			if c.Extension != "" {
				label += "  [" + c.Extension + "]"
			}
			if c.Description != "" {
				label += "  " + c.Description
			}
			its = append(its, item{id: strconv.Itoa(i), label: label, exec: &ext.EditorExecuteParams{Command: c.Name}})
		}
		return "コマンド", its
	case ext.ListKeys:
		for i, k := range r.Keys {
			origin := k.Source
			if k.Extension != "" {
				origin = k.Extension
			}
			label := fmt.Sprintf("%-14s %-4s %-10s %s", k.Keys, k.Modes, origin, k.Description)
			if k.Overridden != "" {
				label += fmt.Sprintf("（%s では設定の keymap が優先）", k.Overridden)
			}
			its = append(its, item{id: strconv.Itoa(i), label: strings.TrimRight(label, " "), exec: &ext.EditorExecuteParams{Keys: k.Keys}})
		}
		return "キー", its
	case ext.ListRegisters:
		for i, reg := range r.Registers {
			name := strconv.Itoa(reg.Index + 1)
			if reg.Name != "" {
				name = `"` + reg.Name
			}
			kind := "c"
			if reg.Linewise {
				kind = "l"
			}
			first, rest, more := strings.Cut(reg.Text, "\n")
			if more {
				first += fmt.Sprintf(" …（+%d 行）", strings.Count(rest, "\n")+1)
			}
			its = append(its, item{id: strconv.Itoa(i), label: fmt.Sprintf("%3s  %s  %s", name, kind, first), reg: &reg})
		}
		return "レジスター", its
	case ext.ListJumps:
		for i := len(r.Jumps) - 1; i >= 0; i-- {
			j := r.Jumps[i]
			label := fmt.Sprintf("%s:%d: %s", j.Name, j.Position.Line+1, j.Text)
			if j.Current {
				label = "> " + label
			}
			pos := j.Position
			op := &ext.EditorOpenParams{Position: &pos, Focus: true}
			if j.Path != "" {
				op.Path = j.Path
			} else {
				buf := j.Buffer
				op.Buffer = &buf
			}
			its = append(its, item{id: strconv.Itoa(i), label: label, open: op, preview: openPreview(op), file: openFile(op)})
		}
		return "ジャンプ", its
	case ext.ListHelp:
		// 語、種類、説明の順に並べる（finder.keys と同じく、語の列を空白でそろえる）。選ぶと本体がその語の :help を開く（FR-148）。
		// プレビューは付けない（#288 のオーナーの決めの表。ヘルプは一覧が窓の幅を全部使う）。
		//
		// req: FR-148
		for i, w := range r.Help {
			label := strings.TrimRight(fmt.Sprintf("%-24s %-8s %s", w.Word, w.Kind, w.Description), " ")
			its = append(its, item{id: strconv.Itoa(i), label: label, exec: &ext.EditorExecuteParams{Help: w.Word}})
		}
		return "ヘルプ", its
	}
	return string(kind), its
}

// paste は、レジスターの候補 reg を、カーソルのあるバッファのカーソルの後に貼る（Vim の p と同じ位置。行単位なら下の行）。
// ループの外で呼ぶ。
func (f *finder) paste(reg ext.ListRegister) error {
	st, err := f.srv.EditorState(f.ctx)
	if err != nil {
		return err
	}
	var version ext.Version
	for _, bi := range st.Buffers {
		if bi.Buffer == st.Buffer {
			version = bi.Version
		}
	}
	b, err := f.srv.WaitBuffer(f.ctx, st.Buffer, version)
	if err != nil {
		return err
	}
	at, text := pastePosition(b, st.Cursor, reg)
	_, err = f.srv.Edit(f.ctx, ext.BufferEditParams{Buffer: st.Buffer, Transaction: ext.Transaction{
		BaseVersion: version, Edits: []ext.Edit{{Range: ext.Range{Start: at, End: at}, Text: text}},
	}})
	return err
}

// lineSource は、行を読めるバッファ（server.Buffer）である。
type lineSource interface {
	LineCount() int
	Line(i int) string
}

// pastePosition は、バッファ b のカーソル cur の後に reg を貼る位置と、入れるテキストを返す。
// 文字単位ならカーソルの文字の後、行単位ならカーソルの行の下である。最後の行の下には、行の末尾に改行を足してから入れる。
func pastePosition(b lineSource, cur ext.Position, reg ext.ListRegister) (ext.Position, string) {
	line := min(max(cur.Line, 0), max(b.LineCount()-1, 0))
	if reg.Linewise {
		text := strings.TrimSuffix(reg.Text, "\n")
		if line+1 < b.LineCount() {
			return ext.Position{Line: line + 1}, text + "\n"
		}
		return ext.Position{Line: line, Byte: len(b.Line(line))}, "\n" + text
	}
	l := ""
	if line < b.LineCount() {
		l = b.Line(line)
	}
	at := min(max(cur.Byte, 0), len(l))
	if at < len(l) {
		_, n := utf8.DecodeRuneInString(l[at:])
		at += n
	}
	return ext.Position{Line: line, Byte: at}, reg.Text
}

// resume は、最後に開いたファインダーを、最後の入力で開き直す（finder.resume）。
func (f *finder) resume(done func()) error {
	f.mu.Lock()
	last := f.last
	f.mu.Unlock()
	if last == nil {
		return fmt.Errorf("finder.resume: まだファインダーを開いていません")
	}
	switch last.kind {
	case "files":
		return f.openFiles(last.dir, last.query, done)
	case "grep":
		return f.openGrep(last.dir, last.query, done)
	case "lines":
		return f.openStatic("lines", "行", last.static, last.query, done)
	}
	go f.openEditorListAsync(ext.EditorListKind(last.kind), last.query, done)
	return nil
}
