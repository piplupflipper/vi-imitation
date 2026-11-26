package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"

	"github.com/piplupflipper/vi-imitation/internal/client"
	"github.com/piplupflipper/vi-imitation/internal/protocol"
	"github.com/piplupflipper/vi-imitation/internal/term"
)

// component は、-popup と -pane のときの本体で、部品の client として動く（基本設計 1.2、1.3。FR-67、FR-115）。
// server が端末多重化ソフトのポップアップかペインの中で起動する。sock（-connect）の server に繋ぎ、
// 部品 view を描き、受けたキーを server へ送る。server が部品を閉じるか、端末の入力が終わったら、端末を戻して終わる。
//
// req: FR-67, FR-115
func component(kind protocol.ClientKind, view uint64, in, out *os.File, logPath, sock string) error {
	if sock == "" {
		return errors.New("-connect が無いので、部品の client として繋げない")
	}
	logger, closeLog, err := openLog(logPath)
	if err != nil {
		return err
	}
	defer closeLog()
	ctx, stop, renotify := signalContext()
	defer stop()
	conn, err := protocol.Dial(sock)
	if err != nil {
		return fmt.Errorf("server に繋げない: %w", err)
	}
	tm, err := term.Open(in, out, term.Options{})
	if errors.Is(err, term.ErrNotTerminal) {
		_ = conn.Close()
		return errNotTerminal
	}
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("端末を開けない: %w", err)
	}
	tm.SetInterruptRestore(renotify)
	defer func() { _ = tm.Close() }() // Close は誤りを返さない
	return guard(tm.Restore, func() error {
		// 部品の client のペイン（tmux の分割したペインには TMUX_PANE が、herdr のプラグインのペインには HERDR_PANE_ID が入る）。
		// herdr のポップアップの中には入らない（#177 の「参考」）が、ポップアップではフォーカスを戻さないので要らない。
		// herdr app のペインには、その端末の番地が HC_TERMINAL_ID に入る（#440）。
		pane := cmp.Or(os.Getenv("TMUX_PANE"), os.Getenv("HERDR_PANE_ID"), os.Getenv("HC_TERMINAL_ID"))
		return clientUntil(ctx, conn, func() error { return client.RunComponent(tm, conn, kind, view, pane, logger) })
	})
}
