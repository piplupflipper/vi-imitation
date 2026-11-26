package server

import (
	"context"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、ファイルでないバッファの作成と、拡張機能から始める見比べ（ext の diffstart.go。基本設計 6.8、6.9）を扱う。

// CreateBuffer は、本体へ buffer/create を送り、p のテキストでファイルでないバッファを作らせる。応答は作ったバッファの番号である。
// p が ext.BufferCreateParams.Validate を通らなければ、送らずにその誤りを返す。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
//
// 本体は応答より先に buffer/open を送るが、OpenEditor と同じく、戻った時点では複製がまだできていないことがある。
// 複製を読むときは、応答の Buffer と Version で WaitBuffer を呼んでから読む。
func (s *Server) CreateBuffer(ctx context.Context, p ext.BufferCreateParams) (ext.BufferCreateResult, error) {
	if err := p.Validate(); err != nil {
		return ext.BufferCreateResult{}, err
	}
	return call[ext.BufferCreateResult](ctx, s, ext.MethodBufferCreate, p)
}

// UpdateBuffer は、本体へ buffer/update を送り、CreateBuffer で作ったバッファの中身を置き換えさせる。応答は置き換えた後のバージョン番号である。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
func (s *Server) UpdateBuffer(ctx context.Context, p ext.BufferUpdateParams) (ext.BufferUpdateResult, error) {
	return call[ext.BufferUpdateResult](ctx, s, ext.MethodBufferUpdate, p)
}

// DisposeBuffer は、本体へ buffer/dispose を送り、CreateBuffer で作ったバッファを閉じさせる。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
func (s *Server) DisposeBuffer(ctx context.Context, id ext.BufferID) error {
	_, err := call[ext.BufferDisposeResult](ctx, s, ext.MethodBufferDispose, ext.BufferDisposeParams{Buffer: id})
	return err
}

// StartDiff は、本体へ diff/start を送り、2 つのバッファの見比べを始めさせる。
// p が ext.DiffStartParams.Validate を通らなければ、送らずにその誤りを返す。
// 見比べを始められなければ（端末多重化ソフトの外、同じ client の 2 つのタブなど）、本体は誤りで応答する。誤りの文言は画面に出せる日本語である。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
//
// p.Wait（API 0.27.0 から）を付けると、見比べが始まるまで応答が来ない。新しい client が繋ぐのを待つ期限（30 秒）か、
// herdr app の mod が分割を終えるのを待つ期限（40 秒）まで遅れうるので、それより短い期限の ctx を渡さない。
// 古い本体（API 0.26.0 まで）は Wait を読まずに捨て、分割を頼んだ時点で応答する（ext.DiffStartParams.Wait の doc）。
func (s *Server) StartDiff(ctx context.Context, p ext.DiffStartParams) (ext.DiffStartResult, error) {
	if err := p.Validate(); err != nil {
		return ext.DiffStartResult{}, err
	}
	return call[ext.DiffStartResult](ctx, s, ext.MethodDiffStart, p)
}
