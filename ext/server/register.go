package server

import (
	"context"

	"github.com/piplupflipper/vi-imitation/ext"
)

// SetRegister は、本体へ register/set を送り、本体のレジスターへテキストを入れる（ext.MethodRegisterSet の doc。API 0.25.0 から。FR-315）。
// 入れ方と知らせ（履歴、ホストのクリップボード、上限を超えたときのメッセージ）は、本体の y と同じである。
// 利用者がキーの前にレジスターの名前を付けたなら、ViewEvent.Register を p.Register に入れる。
// 使えない名前なら、本体は E354 をメッセージの行に出して誤りで応答する。
// p が ext.RegisterSetParams.Validate を通らなければ、送らずにその誤りを返す。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す（OnView のフックからは goroutine を起こして呼ぶ）。
func (s *Server) SetRegister(ctx context.Context, p ext.RegisterSetParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	_, err := call[ext.RegisterSetResult](ctx, s, ext.MethodRegisterSet, p)
	return err
}
