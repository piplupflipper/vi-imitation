package editor

import (
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// registerSetRequest は、拡張機能の register/set を処理する（ext.MethodRegisterSet の doc。基本設計 5.3 の「#432 のレジスターへ入れる口」）。
// 書き方と知らせは writeRegister にある。使えない名前は E354 をメッセージの行に出し、何も書かずに誤りで応答する。
// メッセージとクリップボードの送り先は、params の Origin のキーを打った client で、Origin が無いか覚えていなければ操作中の client
// である（originClient。PR #433 のレビューの推奨 2）。
//
// req: FR-315
func (e *Editor) registerSetRequest(ev *exthost.RegisterSetRequest) {
	c := e.originClient(ev.Params.Origin)
	if c == nil {
		_ = ev.ReplyError(invalidParams("register/set: no client"))
		return
	}
	e.withView(c, func() {
		if e.cur == nil {
			_ = ev.ReplyError(invalidParams("register/set: no buffer"))
			return
		}
		if err := e.writeRegister(ev.Params); err != nil {
			e.showError(err)
			_ = ev.ReplyError(invalidParams("register/set: %v", err))
			return
		}
		_ = ev.Reply()
	})
}

// writeRegister は、名前を指定した本体の y と同じく、p をレジスターへ書き（command.Registers.Yank。FR-50〜FR-53）、履歴の先頭が
// 変わったらホストのクリップボードへ送る（syncClipboard。FR-56）。読み込んでいる client の画面と端末に対して行う。
// 知らせも y と同じで、書いたことは知らせず、履歴に積まなかったこと（FR-53）と、クリップボードへ送るには大きすぎたことだけを
// メッセージの行に出す。使えない名前なら何も書かずに *command.InvalidRegisterError（E354）を返す（知らせは呼び出し側が出す）。
//
// レジスターは client の間で共有する（FR-58）ので、最後にそれを持った State（e.shared）から、読み込んでいる client のカーソルの
// あるタブの State へ引き継いでから書く（useClient と同じ）。syncClipboard は、そのタブの State の履歴を読むためである。
// 行単位のテキストの最後の改行は 1 つだけ取り除く（command.Register は最後の行の後の改行を含めない）。
//
// req: FR-315
func (e *Editor) writeRegister(p ext.RegisterSetParams) error {
	if e.shared != nil && e.shared != e.cur.state {
		e.cur.state.TakeOverShared(e.shared)
	}
	e.shared = e.cur.state
	var name rune
	for _, r := range p.Register {
		name = r
	}
	reg := command.Register{Text: p.Text, Linewise: p.Linewise}
	if reg.Linewise {
		reg.Text = strings.TrimSuffix(reg.Text, "\n")
	}
	notice, err := e.cur.state.Registers().Yank(name, reg)
	if err != nil {
		return err
	}
	if notice != "" {
		e.message = ui.Message{Text: notice, Level: ui.MessageWarning}
	}
	e.syncClipboard()
	return nil
}
