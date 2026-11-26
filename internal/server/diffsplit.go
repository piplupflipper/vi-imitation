package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/editor"
	"github.com/piplupflipper/vi-imitation/internal/mux"
)

// splitTimeout は、:diffsplit でペインを分割するときに、端末多重化ソフトの操作を待つ上限である（仮。FR-63 のフォーカスの移動と同じ値）。
const splitTimeout = 2 * time.Second

// splitPane は、editor.Config.SplitPane である（:diffsplit と diff/start。基本設計 6.8。FR-99）。
// 端末多重化ソフトの層（server を起動した環境で判定する。基本設計 1.3）で from のペインを右（left なら左。#437）に分割し、
// そこで、この server のソケットを -connect で指定して繋ぐ編集の client（本体の実行ファイルに file を渡したもの。空なら渡さない）を、
// dir で起動する。新しい client には、環境変数 editor.EnvSplit で待ちの印 token を渡す。client は Hello の Env でそれを知らせ、
// イベントループがその client のタブと見比べを始める。
//
// #228 は環境変数 VI_IMITATION_SOCKET でソケットを渡していたが、#226 の後はこの環境変数のもとで起動した vi-imitation が
// 待つだけの client（FR-83）になり、ファイルを from の client のタブに開いて新しいペインに何も出さなかった（#245 で見つけた）。
// ソケットのパスの規則（基本設計 1.2）に頼らずに -connect で繋ぐので、from の client が別の window へ動いた後（FR-112）も、
// herdr のペインに HERDR_PANE_ID が入らないときも、この server に繋ぐ。
func (s *Server) splitPane(from, file, dir, token string, left bool) error {
	sock, _ := s.sock.Load().(string)
	if sock == "" {
		return errors.New("server のソケットで待ち受けていない")
	}
	m := mux.Detect(os.Getenv)
	if m.Kind() == mux.KindNone {
		return mux.ErrUnsupported
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("本体の実行ファイルのパスを得られない: %w", err)
	}
	args := []string{exe, "-connect", sock}
	if file != "" {
		args = append(args, "--", file)
	}
	dirn := mux.Right
	if left {
		dirn = mux.Left
	}
	ctx, cancel := context.WithTimeout(context.Background(), splitTimeout)
	defer cancel()
	_, err = m.SplitPane(ctx, mux.SplitOptions{
		Target:    from,
		Direction: dirn,
		Command:   mux.Command{Args: args, Env: map[string]string{editor.EnvSplit: token}, Dir: dir},
		// フォーカスは右のペインに置く。右に開いたら新しいペインへ移し、左に開いたら元のペインに残す（#437。diffview.nvim と同じ）。
		Focus: !left,
	})
	if errors.Is(err, mux.ErrFocusAfterSplit) {
		// ペインは開いて client も起動しているので、見比べは続ける。フォーカスの位置だけが違う（PR #438 のレビューの推奨 2）。
		s.log.Warn("見比べのペインを開いた後にフォーカスを移せない", "from", from, "err", err)
		return nil
	}
	return err
}
