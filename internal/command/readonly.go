package command

import (
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// keepsTextActions は、バッファのテキストを変えず、インサートモードへも移らない本体の操作である。
// 編集を禁止したバッファ（buffer.Buffer.ReadOnly）でも実行できる（基本設計 4.6）。
// ここに無い本体の操作は、テキストを変えうるものとして禁止中は断る。
// 操作を足したときに入れ忘れると、禁止中に使えないだけで、テキストが変わることは無い（断る側に倒す）。
var keepsTextActions = map[string]bool{
	"yank-line":        true,
	"yank-to-line-end": true,
	"command-line":     true,
	"visual":           true,
	"visual-line":      true,
	"visual-swap":      true,
	"select-all":       true,
	"page-down":        true,
	"page-up":          true,
	// スクロールの操作（FR-173）は、表示領域とカーソルだけを動かす。
	"half-page-down":                      true,
	"half-page-up":                        true,
	"scroll-line-down":                    true,
	"scroll-line-up":                      true,
	"scroll-cursor-top":                   true,
	"scroll-cursor-top-first-nonblank":    true,
	"scroll-cursor-center":                true,
	"scroll-cursor-center-first-nonblank": true,
	"scroll-cursor-bottom":                true,
	"scroll-cursor-bottom-first-nonblank": true,
	"output-next":                         true,
	"output-close":                        true,
	"output-scroll-up":                    true,
	"output-scroll-down":                  true,
	"jump-older":                          true,
	"jump-newer":                          true,
	"alternate-buffer":                    true,
	"tab-next":                            true,
	"tab-prev":                            true,
	"popup-focus":                         true,
	// * と # は、検索のパターンを決めるだけである（FR-139）。
	"search-word-forward":  true,
	"search-word-backward": true,
	// dp はこのバッファでなく相手のバッファを変える。相手が編集を禁止していれば、呼び出し側が断る（基本設計 6.8）。
	"diff-next": true,
	"diff-prev": true,
	"diff-put":  true,
}

// checkReadOnly は、編集を禁止したバッファで、cmd がテキストを変える操作かインサートモードへ移る操作なら、
// 実行の前に buffer.ErrReadOnly を返す（FR-85）。
//
// 断るのは、Vim 9.1 で 'modifiable' を切ったときに E21 になる操作に当たるものである。
// 本体のオペレーターは y 以外、本体の操作は keepsTextActions に無いもの、ドットリピート（.）を断る。
// 実行の前に断るので、d や x がレジスターを書き換えることも無い（Vim と同じ）。
// 拡張機能のオペレーターと操作は、テキストを変えるかを本体が知らないので、インサートモードへ移るもの（宣言の mode が insert）だけを断り、
// それ以外は問い合わせて、返ったトランザクションを buffer.Buffer.Apply が断る。
// マクロの再生（@a）は断らず、再生の中のテキストを変える操作が断られた時点で、FR-19 と同じく再生が止まる。
//
// req: FR-85
func (s *State) checkReadOnly(cmd editcmd.Command) error {
	if !s.buf.ReadOnly() || !changesText(cmd) {
		return nil
	}
	return buffer.ErrReadOnly
}

// changesText は、cmd がテキストを変えうる操作か、インサートモードへ移る操作かを返す。
func changesText(cmd editcmd.Command) bool {
	switch cmd.Kind {
	case editcmd.KindOperator:
		if !cmd.Operator.Builtin() {
			return cmd.Mode == editcmd.ModeInsert
		}
		return !isYank(cmd.Operator)
	case editcmd.KindAction:
		if !cmd.Action.Builtin() {
			return cmd.Mode == editcmd.ModeInsert
		}
		return !keepsTextActions[cmd.Action.ID]
	case editcmd.KindRepeat:
		return true
	case editcmd.KindMotion, editcmd.KindRecordStart, editcmd.KindRecordStop, editcmd.KindPlay:
	}
	return false
}
