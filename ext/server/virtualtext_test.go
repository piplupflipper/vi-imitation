package server_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// SetVirtualTexts は、本体の受信の上限を超える一覧を送らずに ErrMessageTooLarge を返す（PR #378 のレビューの推奨 4）。
// 上限を超えたメッセージは、本体が読まずに接続を閉じるので、診断なども持つ拡張機能ごと切れるためである。
// 送る前に断るので、本体と繋がっていなくても大きさの誤りを返し、上限の中の一覧は送ろうとする（繋がっていなければ ErrNotRunning）。
func TestSetVirtualTextsTooLarge(t *testing.T) {
	s := server.New()
	big := ext.VirtualTextSetParams{Buffer: 1, Version: 1, VirtualTexts: []ext.VirtualText{{
		Placement: ext.VirtualTextEOL, Chunks: []ext.VirtualTextChunk{{Text: strings.Repeat("a", 64<<20)}},
	}}}
	if err := s.SetVirtualTexts(big); !errors.Is(err, server.ErrMessageTooLarge) {
		t.Errorf("SetVirtualTexts(64 MiB) = %v, want ErrMessageTooLarge", err)
	}
	small := ext.VirtualTextSetParams{Buffer: 1, Version: 1, VirtualTexts: []ext.VirtualText{{
		Placement: ext.VirtualTextEOL, Chunks: []ext.VirtualTextChunk{{Text: "note"}},
	}}}
	if err := s.SetVirtualTexts(small); !errors.Is(err, server.ErrNotRunning) {
		t.Errorf("SetVirtualTexts(small) = %v, want ErrNotRunning", err)
	}
}
