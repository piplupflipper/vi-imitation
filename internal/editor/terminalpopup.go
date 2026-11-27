package editor

import (
	"fmt"

	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/command/ex"
	"github.com/piplupflipper/vi-imitation/internal/option"
)

// :terminal.popup {cmd}（基本設計 1.3、4.9。FR-168）。
//
// ex は :terminal.popup を、:!{cmd} と同じ外部のコマンドの求め（ex.ShellRequest の Terminal）に Popup を付けて返し、
// startShell が受ける。端末多重化ソフトの中（コマンドを打った client のペインを知っていて、Config.RunPopup がある）なら、
// startPopupShell が端末多重化ソフトのポップアップでコマンドを動かす。外なら、Popup の無い :!{cmd} と同じ経路（startTerminalShell）で、
// 端末を子プロセスへ渡して動かす。外で動かしたことは知らせない（:! と同じ見た目なので）。
//
// ポップアップは、端末多重化ソフトが動かす別の端末なので、エディタは止めない。外部のコマンドの実行（shellJob）として持たず、
// 起動を頼んだらすぐ、:! が終わったときと同じく ex.ShellRequest.Finish を通して続きへ進む（マクロの再生の残りを続ける。
// キーマップの展開の残りは、外部のコマンドの実行として持たないので止めていない）。
// コマンドが変えたファイルは、外での変更の確認（autoread と checkIntervalMs。基本設計 6.7）で読み直す。この Ex コマンドのための契機は足さない。

// startPopupShell は、:terminal.popup の求め req を、コマンドを打った client のペインの上の端末多重化ソフトのポップアップで動かし始め、true を返す。
// 端末多重化ソフトの外（Config.RunPopup が無いか、client のペインを知らない）か、ポップアップを今開けない（Config.PopupReady が false。
// herdr app に mod が無いか古い。FR-326）なら、何もせずに false を返し、呼び出し側が :! と同じく動かす。
//
// コマンドは、:{range}! と同じシェル（shellPath。環境変数 SHELL、無ければ /bin/sh）に -c で渡す。作業ディレクトリは :! と同じく
// コマンドを打った client の作業ディレクトリ（workDir）である。環境変数は端末多重化ソフトがポップアップに与えるもので、
// FR-82 の VI_IMITATION_SOCKET と EDITOR、GIT_EDITOR は足さない（ポップアップの中で起動した vi-imitation が待つだけの client になると、
// タブがポップアップの下の画面に開き、ポップアップを閉じるまで編集できないため）。
// ポップアップの幅は、ファインダーの窓と同じく設定 preview.windowWidthPercent の百分率で、高さは server の既定（部品のポップアップと同じ）である。
// ポップアップは Config.RunPopup が別の goroutine で開き、開けなければ（herdr のプラグインを登録していない、
// 利用者の別のポップアップが開いたまま。mux.ErrPopupBusy の扱いは部品の client と同じ）誤りをその client のメッセージの行に出す。
//
// 起動を頼んだら、:! が終わったときと同じく command.State.FinishShell で求めを終え、止めていた再生の残りを続ける。
//
// req: FR-168, FR-326
func (e *Editor) startPopupShell(bs *bufferState, req *ex.ShellRequest, replay []command.ReplaySegment) bool {
	c := e.op
	if e.cfg.RunPopup == nil || c == nil || c.info.Pane == "" || (e.cfg.PopupReady != nil && !e.cfg.PopupReady(c.info.Pane)) {
		return false
	}
	run, pane, dir := e.cfg.RunPopup, c.info.Pane, e.workDir()
	argv := []string{shellPath(), "-c", req.Cmd}
	opts := PopupOptions{Width: e.options.Get(option.PreviewWindowWidthPercent).Int()}
	go func() {
		defer e.recoverPanic()
		err := run(pane, argv, dir, opts)
		if err == nil {
			return
		}
		e.log.Warn("端末多重化ソフトのポップアップでコマンドを動かせない", "cmd", req.Cmd, "err", err)
		e.later(func() {
			if c.attached() {
				e.withView(c, func() {
					e.showError(fmt.Errorf("端末多重化ソフトのポップアップを開けません: %w", err))
				})
			}
		})
	}()
	// :! が終わったとき（shellFinished）と同じ手順で、求めを終えてから再生の残りを続ける。
	// 外部のコマンドの実行（e.shell）として持たないので、他の client の :{range}! の実行中でも動かせ、キーマップの展開の残りも止めていない
	// （resumeInput で続けるものが無い）。
	e.finishShellRequest(bs, req, nil, 0, nil, replay)
	return true
}
