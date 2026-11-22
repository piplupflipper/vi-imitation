package server

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// WindowChangeEvent は、本体から window/change を受け取ったことを表す（ext.MethodWindowChange の doc。FR-235）。
type WindowChangeEvent struct {
	// Params は、受け取った知らせである。Params.Closed が true なら、窓が無くなった。
	// 帯を送るときは、Params.Seq を ext.BandSetParams.Seq に書く（本体が、古い知らせから作った帯を捨てる）。
	Params ext.WindowChangeParams
	// Buffer は、窓のバッファの複製である。持っていない（拡張機能へ送っていないバッファ、窓が無くなった）なら nil である。
	// フックが戻るまでの間だけ内容を保つ（Buffer の doc）。
	// 本体は window/change より前にそれまでの buffer/change を送るので、複製のバージョン番号はふつう Params.Version と同じである。
	// 差分が欠けて buffer/open の送り直しを待っている間は、複製が古いことがある（Buffer.Version で確かめる）。
	Buffer *Buffer
}

// OnWindowChange は、window/change を受け取ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
// 本体が送るのは、宣言ファイルの windowEvents が true の拡張機能だけである。
//
// ループが f を呼ぶ前に、同じ窓の新しい知らせが届いていれば、古い知らせでは呼ばずに、最後の知らせでだけ呼ぶ。
// f の処理が本体の送る間隔（ext.WindowNotifyIntervalMs）より遅くても、知らせが溜まって ping の応答が遅れないようにするためである。
// このため f は、同じ窓の途中の知らせを受け取らないことがある。順序は保つ（f を呼ぶ時点で、それより前に届いた buffer/change は適用してある）。
//
// 他のフックと同じくループの上で呼ばれるので、f の中で本体の応答を待つ関数（Edit など）をこの ctx で呼ぶと ErrEditInHandler になる。
// 応答を待たない SetBand は呼んでよい。構文の解析のように時間のかかる処理は、Buffer を Clone して別の goroutine で行い
// （ループを止めると ping に応答できない）、作った帯に Params.Seq を書いて送る。
func (s *Server) OnWindowChange(f func(ctx context.Context, e *WindowChangeEvent)) {
	s.mustNotStarted()
	s.onWindow = f
}

// SetBand は、本体へ band/set を送り、窓の上端に行を重ねて描く帯を置く（ext.MethodBandSet の doc。FR-237）。
// p.Lines が空なら帯を消す。p.Seq には、帯を作る元にした知らせの WindowChangeEvent.Params.Seq を書く。
// 応答は待たない。どこから呼んでもよい。
// p.Validate を通らない帯は、送らずに誤りを返す（本体も捨てる）。
func (s *Server) SetBand(p ext.BandSetParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Lines == nil {
		p.Lines = []ext.BandLine{}
	}
	return s.notify(ext.MethodBandSet, p)
}

// windowLatest は、窓ごとに最後に届いた window/change の番号である（Server.windows）。
type windowLatest struct {
	mu sync.Mutex
	// arrived は、届いた window/change の数である。届いた順の番号に使う。
	arrived uint64
	// latest は、窓ごとの、最後に届いた知らせの番号である。
	latest map[ext.WindowID]uint64
}

// windowArrived は、接続の読み取りの goroutine で、届いた通知 n が window/change なら、届いた順の番号を振り、
// その窓の最後の知らせとして覚えて返す。ほかの通知と、窓の読めない知らせでは 0 を返す。
func (s *Server) windowArrived(n *rpc.Notification) uint64 {
	if n.Method != ext.MethodWindowChange {
		return 0
	}
	var p struct {
		Window ext.WindowID `json:"window"`
	}
	if err := json.Unmarshal(n.Params, &p); err != nil {
		return 0
	}
	w := &s.windows
	w.mu.Lock()
	defer w.mu.Unlock()
	w.arrived++
	if w.latest == nil {
		w.latest = map[ext.WindowID]uint64{}
	}
	w.latest[p.Window] = w.arrived
	return w.arrived
}

// superseded は、番号 seq の窓 id の知らせより後に、同じ窓の知らせが届いたかを返す。最後の知らせなら、覚えた番号を消す。
func (s *Server) superseded(id ext.WindowID, seq uint64) bool {
	w := &s.windows
	w.mu.Lock()
	defer w.mu.Unlock()
	if seq != 0 && w.latest[id] != seq {
		return true
	}
	delete(w.latest, id)
	return false
}

// windowChange は、window/change の n を WindowChangeEvent にして OnWindowChange のフックへ渡す。
// seq は n の届いた順の番号（windowArrived）で、同じ窓の後の知らせが届いていれば渡さない。
func (s *Server) windowChange(ctx context.Context, n *rpc.Notification, seq uint64) {
	var p ext.WindowChangeParams
	if err := decodeParams(n, &p); err != nil {
		s.logf("%v", err)
		return
	}
	if s.superseded(p.Window, seq) || s.onWindow == nil {
		return
	}
	e := &WindowChangeEvent{Params: p}
	if !p.Closed {
		e.Buffer = s.buffers[p.Buffer]
	}
	s.hook("window change hook", func() { s.onWindow(ctx, e) })
}
