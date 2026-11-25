package editor

import (
	"strings"

	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// registerSetRequest は、拡張機能の register/set を処理する（ext.MethodRegisterSet の doc。基本設計 5.3 の「#432 のレジスターへ入れる口」）。
// 名前を指定した本体の y と同じく、レジスターへ書き（command.Registers.Yank。FR-50〜FR-53）、履歴の先頭が変わったらホストのクリップボードへ
// 送る（syncClipboard。FR-56）。知らせも y と同じで、書いたことは知らせず、履歴に積まなかったこと（FR-53）と、クリップボードへ送るには
// 大きすぎたことだけをメッセージの行に出す。使えない名前は E354 をメッセージの行に出し、何も書かずに誤りで応答する。
//
// メッセージとクリップボードの送り先は、その拡張機能へ最後に問い合わせを始めた client（extView。message/show と同じ）である。
// レジスターは client の間で共有する（FR-58）ので、最後にそれを持った State（e.shared）から、その client のカーソルのあるタブの
// State へ引き継いでから書く（useClient と同じ）。syncClipboard は、そのタブの State の履歴を読むためである。
// 行単位のテキストの最後の改行は 1 つだけ取り除く（command.Register は最後の行の後の改行を含めない）。
//
// req: FR-315
func (e *Editor) registerSetRequest(ev *exthost.RegisterSetRequest) {
	e.withView(e.extView(ev.Extension), func() {
		if e.cur == nil {
			_ = ev.ReplyError(invalidParams("register/set: no buffer"))
			return
		}
		if e.shared != nil && e.shared != e.cur.state {
			e.cur.state.TakeOverShared(e.shared)
		}
		e.shared = e.cur.state
		var name rune
		for _, r := range ev.Params.Register {
			name = r
		}
		reg := command.Register{Text: ev.Params.Text, Linewise: ev.Params.Linewise}
		if reg.Linewise {
			reg.Text = strings.TrimSuffix(reg.Text, "\n")
		}
		notice, err := e.cur.state.Registers().Yank(name, reg)
		if err != nil {
			e.showError(err)
			_ = ev.ReplyError(invalidParams("register/set: %v", err))
			return
		}
		if notice != "" {
			e.message = ui.Message{Text: notice, Level: ui.MessageWarning}
		}
		e.syncClipboard()
		_ = ev.Reply()
	})
}
