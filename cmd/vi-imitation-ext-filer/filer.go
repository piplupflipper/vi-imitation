package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// 拡張機能の状態と、本体とのやり取り。
//
// 開いた木（pane/open の部品）ごとに tree を持つ。本体からの知らせ（OnView）は ext/server のループの上で呼ばれるので、
// そこでは木の列に操作を積むだけにする（tree.post）。

// paneSize は、分割したペインの大きさ（元のペインの幅に対する百分率）である（仮。ext.PaneOpenParams.Size）。
const paneSize = 30

// popupKey の ID（manifest.json の contributes.popupKeys）。filer.open は action が select なので、本体は view/key を送らない。
const (
	keyClose         = "filer.close"
	keyToggleIgnored = "filer.toggleIgnored"
)

// filer は、拡張機能 1 つの状態である。
type filer struct {
	srv *server.Server
	// ctx は、拡張機能が終わるまで続く ctx である。本体へのリクエスト（OpenPane など）はループの外からこの ctx で送る。
	ctx context.Context

	mu    sync.Mutex
	trees map[ext.ViewID]*tree
	// early は、OpenPane の応答より先に届いた、まだ木に結び付けていない部品の知らせである。
	// ext/server は応答を待っている goroutine へ、知らせをループへ渡すので、応答と知らせの順は保たれない（cmd/vi-imitation-ext-finder と同じ）。
	early map[ext.ViewID][]server.ViewEvent
	// closed は、この拡張機能が view/close で閉じた部品の ID である。閉じた後に届いた知らせを early に溜めないために覚える。
	closed map[ext.ViewID]bool
}

// newFiler は、拡張機能を作り、コマンドと知らせのハンドラーを登録する。
func newFiler(ctx context.Context) *filer {
	f := &filer{
		srv: server.New(), ctx: ctx,
		trees: map[ext.ViewID]*tree{}, early: map[ext.ViewID][]server.ViewEvent{}, closed: map[ext.ViewID]bool{},
	}
	for id, split := range map[string]bool{"filer.popup": false, "filer.pane": true} {
		f.srv.CommandAsync(id, func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams, reply func(ext.CommandExecuteResult, error)) {
			if err := noArgs(p); err != nil {
				_ = f.srv.ShowMessage(ext.MessageError, err.Error())
				reply(ext.CommandExecuteResult{}, nil)
				return
			}
			// 再生中（sync）は、木を開いてから応答する。本体は応答を待つ間に pane/open を処理して中央のポップアップへフォーカスを移し、
			// 再生の残りのキーをポップアップへ渡す（cmd/vi-imitation-ext-finder と同じ。PR #249 のレビューの必須 2）。
			replay := p.Sync
			if !replay {
				reply(ext.CommandExecuteResult{OK: true}, nil)
			}
			go func() {
				err := f.toggle(split)
				if err != nil {
					_ = f.srv.ShowMessage(ext.MessageError, err.Error())
				}
				if replay {
					reply(ext.CommandExecuteResult{OK: err == nil}, nil)
				}
			}()
		})
	}
	f.srv.OnView(f.onView)
	return f
}

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

// toggle は、filer.popup（split が false）と filer.pane（true）の中身である。同じコマンドで開いた木があれば閉じ、無ければ開く
// （neo-tree の toggle。オーナーの nvim の <leader>e と <leader>E は toggle = true）。ループの外で呼ぶ。
//
// req: FR-105
func (f *filer) toggle(split bool) error {
	f.mu.Lock()
	var open []*tree
	for id, t := range f.trees {
		if t.split == split {
			open = append(open, t)
			delete(f.trees, id)
			f.closed[id] = true
		}
	}
	f.mu.Unlock()
	if len(open) > 0 {
		for _, t := range open {
			t.end()
			_ = f.srv.CloseView(t.view)
		}
		return nil
	}
	return f.open(split)
}

// open は、操作中の client の作業ディレクトリを根にした木を、カーソルのあるファイルの場所まで開いて、pane/open で出す。
// split が false なら中央のポップアップに出し、選んだら閉じさせる。true なら分割したペインに出す。
// 分割したペインにも closeOnSelect を付けるのは、端末多重化ソフトの外とペインを開けなかったときに、本体が代わりに出す中央のポップアップを
// 選んだら閉じさせるためである（分割したペインは選んでも閉じない。ext.PaneOpenParams.CloseOnSelect）。
//
// req: FR-105, FR-147
func (f *filer) open(split bool) error {
	st, err := f.srv.EditorState(f.ctx)
	if err != nil {
		return fmt.Errorf("ファイラを開けません: %w", err)
	}
	root := st.WorkDir
	if root == "" {
		if root, err = os.Getwd(); err != nil {
			return fmt.Errorf("ファイラを開けません: %w", err)
		}
	}
	current := ""
	for _, b := range st.Buffers {
		if b.Buffer == st.Buffer {
			current = b.Path
		}
	}
	t := newTree(f, root, split)
	t.ensure(t.root)
	cursor, opened := t.reveal(current)
	force := map[string]bool{}
	for _, d := range opened {
		force[d] = true
	}
	p := ext.PaneOpenParams{Component: t.component(cursor, force), CloseOnSelect: true, Focus: true, Size: paneSize}
	if !split {
		p.Placement = ext.PaneCenter
	}
	r, err := f.srv.OpenPane(f.ctx, p)
	if err != nil {
		t.cancel()
		return fmt.Errorf("ファイラを開けません: %w", err)
	}
	f.register(t, r.View)
	return nil
}

// register は、開いた木 t に部品の ID id を結び付け、木の goroutine を始める。応答より先に届いていた知らせを、届いた順に渡す。
func (f *filer) register(t *tree, id ext.ViewID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t.view = id
	evs := f.early[id]
	delete(f.early, id)
	f.trees[id] = t
	t.start()
	for _, e := range evs {
		f.dispatch(t, e)
	}
}

// closeAll は、すべての木の goroutine と子プロセスを止める。
func (f *filer) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, t := range f.trees {
		t.end()
		delete(f.trees, id)
	}
}

// onView は、本体からの部品の知らせを、その木の列に積む。ループの上で呼ばれるので、重い処理はしない。
func (f *filer) onView(_ context.Context, e *server.ViewEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed[e.View] {
		return
	}
	t := f.trees[e.View]
	if t == nil {
		f.early[e.View] = append(f.early[e.View], *e)
		return
	}
	f.dispatch(t, *e)
}

// dispatch は、知らせ e を木 t の操作にして列に積む。f.mu を持って呼ぶ。
func (f *filer) dispatch(t *tree, e server.ViewEvent) {
	switch e.Kind {
	case server.ViewClosed:
		delete(f.trees, t.view)
		t.end()
	case server.ViewExpanded:
		t.post(func() { t.expand(e.Item, e.Expanded) })
	case server.ViewSelected:
		// 読み込みの列（post）に積まない（tree の doc の goroutine）。
		t.opens.post(func() { t.choose(e.Item) })
	case server.ViewKey:
		switch e.Key {
		case keyClose:
			t.post(func() { t.collapse(e.Item) })
		case keyToggleIgnored:
			t.post(t.toggleIgnored)
		}
	case server.ViewTabChanged:
		if t.split {
			t.post(func() { t.follow(e.Item) })
		}
	case server.ViewInputChanged, server.ViewChildSelected:
	}
}
