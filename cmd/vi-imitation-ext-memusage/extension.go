package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// 宣言ファイル manifest.json の ID。
const (
	// partRSS は、ステータス行の部品の ID である。
	partRSS = "memusage.rss"
	// cmdShow は、内訳をポップアップに出すコマンドの ID である（オーナーの nvim の :MemUsage に当たる）。
	cmdShow = "memusage.show"
)

// 設定の既定値と範囲。
const (
	// defaultInterval は、部品を更新する間隔の既定値である（memusage.lua の INTERVAL_MS と同じ）。
	defaultInterval = 5 * time.Second
	// minIntervalMs は、intervalMs に書ける最小の値である（仮）。/proc を読む回数が増えすぎないようにする。
	minIntervalMs = 500
	// maxIntervalMs は、intervalMs に書ける最大の値である（仮。1 時間）。
	maxIntervalMs = 60 * 60 * 1000
)

// settings は、extensions.settings.memusage の値である。
type settings struct {
	// IntervalMs は、部品を更新する間隔のミリ秒である。省くと defaultInterval である。
	IntervalMs *int `json:"intervalMs"`
}

// parseSettings は、設定を読み、更新の間隔を返す。読めない設定と範囲の外の値は、既定値を使い、その旨を warn に返す。
//
// req: FR-227
func parseSettings(raw json.RawMessage) (interval time.Duration, warn error) {
	if len(raw) == 0 {
		return defaultInterval, nil
	}
	var s settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return defaultInterval, fmt.Errorf("extensions.settings.memusage を読めないので既定の値を使う: %w", err)
	}
	if s.IntervalMs == nil {
		return defaultInterval, nil
	}
	if n := *s.IntervalMs; n < minIntervalMs || n > maxIntervalMs {
		return defaultInterval, fmt.Errorf("extensions.settings.memusage.intervalMs は %d 以上 %d 以下にする（既定の %d を使う）",
			minIntervalMs, maxIntervalMs, defaultInterval.Milliseconds())
	}
	return time.Duration(*s.IntervalMs) * time.Millisecond, nil
}

// extension は、メモリの使用量の拡張機能である。
type extension struct {
	server *server.Server
	// ctx は、ハンドラーから起こした goroutine が本体へリクエスト（popup/open）を送るときの ctx である。
	// ハンドラーの ctx は応答で終わり、それで本体へ送ると ErrEditInHandler になる（ext/server の Edit の doc）。
	ctx context.Context
	// root は、集計の根（本体の server のプロセス ID）を返す。拡張機能は本体の子なので、既定は os.Getppid である。
	root func() int
	// newSource は、1 回の集計に使う source を作る。
	newSource func(ctx context.Context) (source, error)

	// mu は、ここから下を守る。
	mu sync.Mutex
	// ids は、宣言ファイルのディレクトリから拡張機能の ID への対応である。
	ids manifestIDs
	// last は、最後に送った部品の中身である。変わったときだけ送る。
	last string
	// cancel は、更新の goroutine を止める。
	cancel context.CancelFunc
}

// newExtension は、コマンドとフックを登録した拡張機能を作る。
func newExtension() *extension {
	x := &extension{
		server: server.New(), ctx: context.Background(), root: os.Getppid, ids: manifestIDs{},
	}
	x.newSource = defaultSource()
	x.server.OnInitialize(x.initialize)
	x.server.OnShutdown(func(context.Context, *server.ShutdownEvent) { x.stop() })
	x.server.Command(cmdShow, x.show)
	return x
}

// defaultSource は、Linux で /proc から子を辿れれば、使い回す 1 つの procSource（見分けた拡張機能を覚える）を返す関数を、
// そうでなければ集計のたびに ps を実行する関数を返す。
func defaultSource() func(ctx context.Context) (source, error) {
	if hasProcChildren() {
		src := newProcSource("/proc")
		return func(context.Context) (source, error) { return src, nil }
	}
	return func(ctx context.Context) (source, error) { return newPSSource(ctx) }
}

// initialize は、設定を読み、部品を更新する goroutine を始める。設定の誤りは message/show で知らせる。
func (x *extension) initialize(_ context.Context, e *server.InitializeEvent) error {
	interval, warn := parseSettings(e.Settings)
	ctx, cancel := context.WithCancel(context.Background())
	x.mu.Lock()
	x.cancel = cancel
	x.mu.Unlock()
	go func() {
		if warn != nil {
			_ = x.server.ShowMessage(ext.MessageWarning, "memusage: "+warn.Error())
		}
		x.loop(ctx, interval)
	}()
	return nil
}

// stop は、更新の goroutine を止める。
func (x *extension) stop() {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.cancel != nil {
		x.cancel()
	}
}

// loop は、すぐに 1 回と、その後は interval ごとに集計し、部品の中身が変わったら statusline/set で送る。ctx が終わると戻る。
//
// req: FR-227
func (x *extension) loop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		x.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refresh は、1 回集計して、部品の中身が変わっていれば送る。集計できなければ部品を消す（空の中身を送る）。
func (x *extension) refresh(ctx context.Context) {
	text := ""
	if r, err := x.collect(ctx); err == nil {
		text = statusText(&r)
	}
	x.mu.Lock()
	changed := text != x.last
	x.last = text
	x.mu.Unlock()
	if changed {
		_ = x.server.SetStatusLine(partRSS, text, "")
	}
}

// collect は、本体を根に集計する。
func (x *extension) collect(ctx context.Context) (report, error) {
	src, err := x.newSource(ctx)
	if err != nil {
		return report{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	r, ok := collect(src, x.root(), x.ids)
	if p, isProc := src.(*procSource); isProc {
		p.prune()
	}
	if !ok {
		return report{}, errors.New("本体のプロセスを読めない")
	}
	return r, nil
}

// show は、memusage.show を受け付け、別の goroutine で集計して内訳をポップアップに出す。
// ポップアップを開く popup/open は本体の応答を待つので、ハンドラーの上では送れない（ext/server の OpenPopup の doc）。
// バッファもカーソルも変えないので、マクロの再生中（sync）も受け付けたことだけを返す。
//
// req: FR-228
func (x *extension) show(_ context.Context, _ *server.Buffer, _ *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
	go x.showDetail(x.ctx)
	return ext.CommandExecuteResult{OK: true}, nil
}

// showDetail は、集計して、内訳を画面の中央のポップアップに出す。集計できなければ message/show で知らせる。
func (x *extension) showDetail(ctx context.Context) {
	r, err := x.collect(ctx)
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageError, "memusage: "+err.Error())
		return
	}
	_, err = x.server.OpenPopup(ctx, ext.PopupOpenParams{
		Component: ext.ViewComponent{Kind: ext.ViewText, Title: "メモリの使用量（RSS）", Text: detailText(&r)},
		Placement: ext.PopupCenter,
	})
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageError, "memusage: 内訳を出せない: "+err.Error())
	}
}
