package input

import (
	"fmt"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// Action は、設定の keymap の右辺に ID で書ける本体の操作の 1 件である（FR-126）。
// ID は公開のインターフェイスで、名前を変えると利用者の設定が壊れる（builtinMotions などの表の doc）。
type Action struct {
	// ID は、操作の ID である（line-start、insert.delete-char、hover など）。
	ID string
	// Kind は、操作の種類である。
	Kind BindingKind
	// Modes は、操作を割り当てられるモードの集合である。本体の操作の表で、その ID の行が有効なモードを合わせたものである。
	Modes MapMode
	// Provider は、提供者（LSP と DAP の拡張機能）へ振り分ける操作であるかを表す（ProviderExtension）。
	Provider bool
	// Description は、操作の説明文である。同じ ID の行が複数あれば、表の最初の行の説明文である。
	Description string

	takesChar, takesChar2 bool
	next                  editcmd.Mode
}

// Actions は、設定の keymap の右辺に書ける本体の操作を、ID の一覧の順（本体の操作の表の順、提供者へ振り分ける操作の表の順）に返す。
// 同じ ID の行（0 と <Home> の line-start、d とビジュアルモードの x の delete など。同じ操作を複数のキーに載せたもの）は 1 件にまとめる。
// ID は種類をまたいで一意である（TestActionIDUnique。#257 のレビューの必須 1 で、操作の ~ を toggle-case-char に分けた）。
// キーの一覧の操作（KeyListPrefix で始まる ID）は、接頭辞ごとに作るので含めない。
//
// req: FR-126
func Actions() []Action {
	var out []Action
	index := map[string]int{} // ID と種類から out の添字へ
	add := func(a Action) {
		key := fmt.Sprintf("%s\x00%d", a.ID, a.Kind)
		if i, ok := index[key]; ok {
			out[i].Modes |= a.Modes
			return
		}
		index[key] = len(out)
		out = append(out, a)
	}
	for _, t := range builtinTables {
		for _, s := range t.specs {
			modes := s.modes
			if modes == 0 {
				modes = defaultModes[t.kind]
			}
			add(Action{ID: s.id, Kind: t.kind, Modes: modes, Description: s.desc, takesChar: s.takesChar, takesChar2: s.takesChar2, next: s.next})
		}
	}
	for _, ss := range [][]providerSpec{providerKeys, previewKeys, viewChildKeys} {
		for _, s := range ss {
			modes := s.modes
			if modes == 0 {
				modes = defaultModes[s.kind]
			}
			add(Action{ID: s.id, Kind: s.kind, Modes: modes, Provider: true, Description: s.desc})
		}
	}
	return out
}

// UserBinding は、設定の keymap のモード mode（MapNormal、MapVisual、MapInsert のどれか 1 つ）の、左辺が keys で右辺が
// { "action": id } の割り当てを、操作の表への登録（Keymap.Override に渡す値）にする（FR-126）。
//
// id は Actions の ID である。
// 次の場合はエラーを返す。
//   - id が本体の操作の ID でない（拡張機能が宣言したコマンドの ID は書けない。キーマップは拡張機能の起動を待たずに効くため。FR-41）
//   - id の操作が mode で使えない（ノーマルモードのモーションをインサートモードに、インサートモードの操作をノーマルモードに割り当てたなど）
//   - keys が Keymap.Bind の規則に反する（回数などとして先に読まれるキーで始まる、インサートモードで 1 つのキーでないなど）
//
// ノーマルモードに割り当てたモーションは、オペレーターの後（MapOperatorPending）には効かない（Vim の nmap と同じ。
// 設定の keymap の右辺をキーの列で書いた割り当ても、オペレーターの後では展開しない）。
//
// req: FR-126
func UserBinding(mode MapMode, keys []editcmd.Key, id string) (Binding, error) {
	if mode != MapNormal && mode != MapVisual && mode != MapInsert {
		return Binding{}, fmt.Errorf("操作を割り当てられないモードである: %04b", mode)
	}
	actions := Actions()
	found := false
	for _, a := range actions {
		if a.ID != id {
			continue
		}
		found = true
		if a.Modes&mode == 0 {
			continue
		}
		b := Binding{
			Kind: a.Kind, Modes: mode, Keys: slices.Clone(keys), Ref: editcmd.Ref{ID: a.ID},
			TakesChar: a.takesChar, TakesSecondChar: a.takesChar2, NextMode: a.next, Description: a.Description, User: true,
		}
		if a.Provider {
			b.Ref.Extension = ProviderExtension
		}
		if err := normalizeBinding(&b); err != nil {
			return Binding{}, err
		}
		return b, nil
	}
	if !found {
		return Binding{}, fmt.Errorf("未知の操作の ID %q", id)
	}
	return Binding{}, fmt.Errorf("操作 %q は %s モードでは使えない", id, ModeName(mode))
}

// ModeName は、mode（1 つのモード）の、設定の keymap のモードの名前（normal、visual、insert）を返す。
// オペレーターの後は設定のモードに無いので operatorPending を返す。
func ModeName(mode MapMode) string {
	switch mode {
	case MapNormal:
		return "normal"
	case MapVisual:
		return "visual"
	case MapInsert:
		return "insert"
	case MapOperatorPending:
		return "operatorPending"
	}
	return fmt.Sprintf("MapMode(%04b)", mode)
}

// actionIDTable は、docs/usage.md の「操作の ID の一覧」の表（Markdown）を、Actions から作って返す。
// テスト（TestActionIDDoc）が文書の表と照合する。
func actionIDTable() string {
	kinds := map[BindingKind]string{BindMotion: "モーション", BindOperator: "オペレーター", BindTextObject: "テキストオブジェクト", BindAction: "操作"}
	var sb strings.Builder
	sb.WriteString("| ID | 種類 | モード | 説明 |\n|---|---|---|---|\n")
	for _, a := range Actions() {
		var modes []string
		for _, m := range []MapMode{MapNormal, MapVisual, MapInsert} {
			if a.Modes&m != 0 {
				modes = append(modes, "`"+ModeName(m)+"`")
			}
		}
		fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n", a.ID, kinds[a.Kind], strings.Join(modes, "、"),
			strings.ReplaceAll(a.Description, "|", `\|`))
	}
	return sb.String()
}

// InsertAction は、インサートモードのキー k に割り当てた操作を返す（FR-127）。割り当てが無ければ ok は false である。
// internal/command の State.insertKey が、文字、<CR>、<Tab>、<Esc> の他のキーで呼ぶ（InsertTableKey）。
// 解釈の途中の状態は持たず、読みも変えない。
//
// req: FR-127
func (p *Parser) InsertAction(k editcmd.Key) (editcmd.Ref, bool) {
	b, ok := p.km.Lookup(MapInsert, []editcmd.Key{k})
	if !ok {
		return editcmd.Ref{}, false
	}
	return b.Ref, true
}
