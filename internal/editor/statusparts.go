package editor

import (
	"fmt"
	"maps"
	"slices"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// この節は、拡張機能がステータス行とタブ行に足す部品（宣言ファイルの contributes.statusline と statusline/set）を持つ
// （基本設計 7.1。FR-92）。

// buildStatusOwners は、拡張機能が宣言した部品の ID から、宣言した拡張機能の ID への表を作る。
// 部品の ID は拡張機能の ID で始まる（ext.Manifest.Validate）ので、2 つの拡張機能の部品は重ならない。
func buildStatusOwners(specs []exthost.Spec) map[string]string {
	owners := map[string]string{}
	for _, s := range specs {
		for _, p := range s.Manifest.Contributes.StatusLine {
			owners[p.ID] = s.Manifest.ID
		}
	}
	return owners
}

// layoutExtParts は、設定のステータス行とタブ行の並びに書いた、本体の部品でない ID（拡張機能の部品の ID）を、並べた順に重複なく返す。
func layoutExtParts(status, tab *ui.StatusLine) []string {
	sl, tl := ui.DefaultStatusLine(), ui.DefaultTabLineParts()
	if status != nil {
		sl = *status
	}
	if tab != nil {
		tl = *tab
	}
	var out []string
	seen := map[string]bool{}
	for _, ids := range [][]string{sl.Left, sl.Right, tl.Left, tl.Right} {
		for _, id := range ids {
			if ui.IsTabLinePart(id) || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// collectStatusExtensions は、設定の並びに部品を書いた拡張機能の ID を statusExts に集める。
// 部品の中身は拡張機能が送るまで無いので、並べた部品を出すために、起動の契機を待たずに起動時に動かす
// （lualine の部品が描くたびに評価されるのに当たる）。起動するのは最初の画面を描いた後（startResidentExtensions）である。
// 並べた ID を足す拡張機能が無ければ、誤りとして返す（部品は出さない）。
// statusExts の拡張機能は、:ext sync の入れ替えの後にも起動し直す（restartStatusExtension）。
func (e *Editor) collectStatusExtensions() []error {
	var errs []error
	e.statusExts = map[string]bool{}
	for _, id := range layoutExtParts(e.cfg.StatusLine, e.cfg.TabLineParts) {
		owner, ok := e.statusOwners[id]
		if !ok {
			errs = append(errs, fmt.Errorf("statusline か tabline の部品 %s は、本体の部品でも拡張機能の部品（contributes.statusline）でもないので出さない", id))
			continue
		}
		e.statusExts[owner] = true
	}
	return errs
}

// startResidentExtensions は、最初の画面を描き終えた後に、本体の起動で起動する拡張機能を起動する。
// 設定の並びに部品を置いた拡張機能（collectStatusExtensions）と、起動の契機 startup の拡張機能（exthost.Host.ActivateStartup）である。
// 最初の画面を描く前に子プロセスを起動しないので、本体の起動の速さ（NFR-05）を損なわない（#367）。
// 起動は非同期で、待たずに戻る（NFR-04）。
//
// req: FR-225
func (e *Editor) startResidentExtensions() {
	for _, owner := range slices.Sorted(maps.Keys(e.statusExts)) {
		if err := e.host.Start(owner); err != nil {
			e.log.Warn("部品の拡張機能を起動できない", "extension", owner, "err", err)
		}
	}
	e.host.ActivateStartup()
}

// restartStatusExtension は、未起動へ戻った拡張機能 extID（:ext sync の入れ替え。exthost.Host.Reload）が、
// 並びに部品を置いた拡張機能なら起動し直す。起動し直した拡張機能は、initialize の後に部品を送り直す。
// 既に起動の契機で起動していれば、Start は何もしない。
func (e *Editor) restartStatusExtension(extID string) {
	if !e.statusExts[extID] {
		return
	}
	if err := e.host.Start(extID); err != nil {
		e.log.Warn("部品の拡張機能を起動し直せない", "extension", extID, "err", err)
	}
}

// maxStatusPartRunes は、拡張機能の部品の中身として持つ文字の数の上限である（仮）。超えた分は捨てる。
const maxStatusPartRunes = 256

// setStatusPart は、statusline/set の部品の中身を持つ（ext.MethodStatusLineSet）。
// 送った拡張機能が宣言していない部品は捨てる。Text が空なら部品の中身を消す（部品を出さない）。
// 中身は maxStatusPartRunes 文字までにする。
//
// req: FR-92
func (e *Editor) setStatusPart(extID string, p ext.StatusLineSetParams) {
	if e.statusOwners[p.Part] != extID {
		e.log.Warn("宣言していない部品の statusline/set を捨てる", "extension", extID, "part", p.Part)
		return
	}
	text := ext.StatusLineText(p.Text)
	if r := []rune(text); len(r) > maxStatusPartRunes {
		// 描くたびに全体の幅を数えるので、受け取る時点で切っておく（PR #239 のレビューの推奨 1）。
		text = string(r[:maxStatusPartRunes])
	}
	if text == "" {
		delete(e.extParts, p.Part)
		return
	}
	e.extParts[p.Part] = ui.ExtPart{Text: text, Style: p.Style}
}

// clearStatusParts は、拡張機能 extID の部品の中身をすべて消す。拡張機能が異常で終わったか止まったときに呼ぶ（clearExtension）。
func (e *Editor) clearStatusParts(extID string) {
	for id := range e.extParts {
		if e.statusOwners[id] == extID {
			delete(e.extParts, id)
		}
	}
}
