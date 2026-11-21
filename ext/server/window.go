package server

import (
	"context"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// WindowChangeEvent は、本体から window/change を受け取ったことを表す（ext.MethodWindowChange の doc。FR-235）。
type WindowChangeEvent struct {
	// Params は、受け取った知らせである。Params.Closed が true なら、窓が無くなった。
	Params ext.WindowChangeParams
	// Buffer は、窓のバッファの複製である。持っていない（拡張機能へ送っていないバッファ、窓が無くなった）なら nil である。
	// フックが戻るまでの間だけ内容を保つ（Buffer の doc）。
	// 本体は window/change より前にそれまでの buffer/change を送るので、複製のバージョン番号はふつう Params.Version と同じである。
	// 差分が欠けて buffer/open の送り直しを待っている間は、複製が古いことがある（Buffer.Version で確かめる）。
	Buffer *Buffer
}

// OnWindowChange は、window/change を受け取ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
// 本体が送るのは、宣言ファイルの windowEvents が true の拡張機能だけである。
// 他のフックと同じくループの上で呼ばれるので、f の中で本体の応答を待つ関数（Edit など）をこの ctx で呼ぶと ErrEditInHandler になる。
// 応答を待たない SetBand は呼んでよい。構文の解析のように時間のかかる処理は、Buffer を Clone して別の goroutine で行う
// （ループを止めると ping に応答できない）。
func (s *Server) OnWindowChange(f func(ctx context.Context, e *WindowChangeEvent)) {
	s.mustNotStarted()
	s.onWindow = f
}

// SetBand は、本体へ band/set を送り、窓の上端に行を重ねて描く帯を置く（ext.MethodBandSet の doc。FR-237）。
// p.Lines が空なら帯を消す。応答は待たない。どこから呼んでもよい。
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

// windowChange は、window/change の n を WindowChangeEvent にして OnWindowChange のフックへ渡す。
func (s *Server) windowChange(ctx context.Context, n *rpc.Notification) {
	var p ext.WindowChangeParams
	if err := decodeParams(n, &p); err != nil {
		s.logf("%v", err)
		return
	}
	if s.onWindow == nil {
		return
	}
	e := &WindowChangeEvent{Params: p}
	if !p.Closed {
		e.Buffer = s.buffers[p.Buffer]
	}
	s.hook("window change hook", func() { s.onWindow(ctx, e) })
}
