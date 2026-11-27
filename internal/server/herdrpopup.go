package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/mux"
)

// herdr app の中の中央のポップアップ（#452。FR-325、FR-326）。
//
// herdr app の中では、中央のポップアップ（部品の client と :terminal.popup）を、mod に頼んで herdr app の窓に出す（mux.HerdrApp.OpenPopup）。
// mod が入っていないか、ポップアップの依頼を知らない古い mod なら、今までどおり部品を client の画面の中に描き、:terminal.popup を :! と同じく動かす。
// editor は、ポップアップを開くか :! で動かすかを、イベントループの上で待たずに決める（editor.Config.PopupReady）。
// このため server は、mod がポップアップを知っているかを前もって mod に問い（mux.HerdrApp.PopupSupported）、答えを覚えておく（herdrPopups）。
//
// 問うのは、ペインを知らせた編集の client が繋いだときと、覚えた答えが「開けない」でポップアップを開こうとしたときである。
// 「開けない」の答えは herdrPopupRetry の間は問い直さない（mod の無い herdr app で、開くたびに mux.HerdrAppWait の待ちと hc の呼び出しを重ねない）。
// 問い終えるまでは「開けない」として扱う。server を起動した直後に繋いだ client が、問いの終わる前（mod が依頼を探す間隔の 0.5〜2 秒）に
// ポップアップを開くと、client の画面の中に描く。

// herdrTraceDir は、herdr app のポップアップを調べるための記録（herdrTrace）の置き場である。空なら ~/.local/state/vi-imitation である。
// テストは一時ディレクトリにする（利用者の記録に書かない）。テストが server を動かしたまま替えるので、atomic で持つ。
var herdrTraceDir atomic.Pointer[string]

// herdrTraceMax は、記録のファイルの大きさの上限である。超えたら .1 に移して書き直す。
const herdrTraceMax = 1 << 20

// herdrTrace は、目印のファイル herdr-app-mod.debug（mod の記録と同じ。使い方の「herdr app に mod を入れる」）があるときだけ、
// server が herdr app のポップアップを開くかを決めた箇所の記録を、同じディレクトリの herdr-app-server.log に 1 行の JSON で追記する
// （2026-11-27 にオーナーの herdr app で、2 回目からポップアップの依頼が mod に届かなかった原因を、実物で調べるため。#452）。
// kv は名前と値の組で、誤りは文言にする。書けなくても何もしない。
func herdrTrace(event string, kv ...any) {
	dir := ""
	if p := herdrTraceDir.Load(); p != nil {
		dir = *p
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir = filepath.Join(home, ".local", "state", "vi-imitation")
	}
	if _, err := os.Stat(filepath.Join(dir, "herdr-app-mod.debug")); err != nil {
		return
	}
	m := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "pid": os.Getpid(), "event": event}
	for i := 0; i+1 < len(kv); i += 2 {
		v := kv[i+1]
		if err, ok := v.(error); ok {
			v = err.Error()
		}
		m[fmt.Sprint(kv[i])] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	path := filepath.Join(dir, "herdr-app-server.log")
	if fi, serr := os.Stat(path); serr == nil && fi.Size() > herdrTraceMax {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

// herdrPopupRetry は、herdr app の mod がポップアップを知らないと分かった後に、問い直さない間である（仮）。
// 利用者が mod を入れ直したら、この間の後に開こうとしたときに問い直して、窓に出すようになる。
const herdrPopupRetry = time.Minute

// herdrPopups は、herdr app の mod がポップアップの依頼を知っているかの、覚えた答えである。メソッドは並行に呼んでよい。
type herdrPopups struct {
	// probe は、端末 target に書いた依頼で mod に問う関数である（mux.HerdrApp.PopupSupported）。テストでは偽物にする。
	probe func(ctx context.Context, target string) (bool, error)
	// ctx は、問いに渡す ctx の元を返す（Server.muxContext。server の終わりで待ちを打ち切る）。
	ctx func() context.Context
	log *slog.Logger

	mu sync.Mutex
	// ready は、mod がポップアップを知っていると答えたことを表す。
	ready bool
	// asking は、問いの途中であることを表す。
	asking bool
	// failed は、最後に「開けない」と分かった時刻である。ゼロなら、まだ問い終えていない。
	failed time.Time
	// done は、問いを終えるたびに閉じて作り直すチャネルである（テストが問いの終わりを待つ）。
	done chan struct{}
}

// newHerdrPopups は、probe で問う herdrPopups を作る。
func newHerdrPopups(log *slog.Logger, ctx func() context.Context, probe func(context.Context, string) (bool, error)) *herdrPopups {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &herdrPopups{probe: probe, ctx: ctx, log: log, done: make(chan struct{})}
}

// Ready は、editor.Config.PopupReady である。mod がポップアップを知っていると覚えていれば true を返す。
// そうでなければ、問いの途中でなく、「開けない」と分かってから herdrPopupRetry が経っていれば、端末 from に書いた依頼で問い直し始めて、false を返す。
// イベントループから呼ぶので待たない。
//
// req: FR-326
func (p *herdrPopups) Ready(from string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	herdrTrace("ready", "from", from, "ready", p.ready, "asking", p.asking, "failed", p.failed.UTC().Format(time.RFC3339Nano))
	if p.ready {
		return true
	}
	p.askLocked(from)
	return false
}

// prime は、ペインを知らせた編集の client が繋いだときに呼び、まだ答えを得ていなければ、端末 from に書いた依頼で問い始める。
// 最初のポップアップの前に答えを得ておくためである。
func (p *herdrPopups) prime(from string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ready && p.failed.IsZero() {
		p.askLocked(from)
	}
}

// askLocked は、問いの途中でなく、問い直してよければ、別の goroutine で問い始める。p.mu を持って呼ぶ。
func (p *herdrPopups) askLocked(from string) {
	if p.asking || from == "" || (!p.failed.IsZero() && time.Since(p.failed) < herdrPopupRetry) {
		return
	}
	p.asking = true
	go func() {
		ctx, cancel := context.WithTimeout(p.ctx(), mux.HerdrAppSplitWait)
		ok, err := p.probe(ctx, from)
		cancel()
		p.mu.Lock()
		defer p.mu.Unlock()
		herdrTrace("probe", "from", from, "ok", ok, "err", err)
		p.asking = false
		p.ready = ok
		if !ok {
			// 問えなかった（mod が無い、server が終わった）ときも「開けない」として覚える。
			p.failed = time.Now()
			p.log.Info("herdr app の mod の窓にポップアップを出さず、client の画面の中に描く", "terminal", from, "err", err)
		}
		close(p.done)
		p.done = make(chan struct{})
	}()
}

// fail は、ポップアップを開けなかった誤り err が、mod が無いか古いことを表すなら、「開けない」と覚え直す。
// 開いた後で利用者が mod を外したか、古い mod に入れ替えたときに、次からは client の画面の中に描くためである。
func (p *herdrPopups) fail(err error) {
	if !errors.Is(err, mux.ErrHerdrAppNoReply) && !errors.Is(err, mux.ErrHerdrAppModOutdated) {
		return
	}
	herdrTrace("fail", "err", err)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ready = false
	p.failed = time.Now()
}

// waitAsked は、今の問いが終わるまで待つ（テスト用）。問いの途中でなければすぐ戻る。
func (p *herdrPopups) waitAsked(ctx context.Context) error {
	p.mu.Lock()
	if !p.asking {
		p.mu.Unlock()
		return nil
	}
	done := p.done
	p.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
