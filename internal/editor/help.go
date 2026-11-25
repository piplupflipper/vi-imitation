package editor

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/docs"
	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/config"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/input"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// ヘルプ（:help。FR-89）とキーの一覧（FR-90）。基本設計 7.2。
//
// ヘルプは、テキストのバッファの部品（編集を禁止したファイルでないバッファ。FR-88）で、画面の中央のポップアップに出す（#236）。
// 長い行は窓の幅で折り返す。:tabopen でタブへ広げられる。
// キーの一覧は、一覧の部品で画面の中央のポップアップに出し、<CR> で選んだキーを、そのキーを打ったのと同じく実行する（selectKeyRow）。
// どちらも、端末多重化ソフトのポップアップの部品の client（FR-66、FR-67）ができるまでは、編集の client の画面の中央に描く。
//
// :help と :keys は本体のコマンドだが、:ext（extsync.go）と同じく ex パッケージの表には置かず、
// 拡張機能のコマンド（ex.State.ExtCommand）と同じ口から呼ぶ。コマンドラインの表（exCommands）に helpExtension を入れ、
// extQuery.Action が helpAction へ渡す。引数を行末まで取ること、範囲と ! を取らないことは、拡張機能のコマンドと同じである。
//
// 接頭辞で絞ったキーの一覧は、接頭辞の後に ? を打って出す（<leader>l? で <leader>l に続くキー）。
// <leader>l? と <leader>d? は本体の既定のキー（input の keyListKeys）で、接頭辞に加えて LSP と DAP の操作のキー（K、gq など）も出す。
// 他の接頭辞の ? は addKeyListBindings が作る。
// どちらも Ref.Extension を input.ProviderExtension、ID を input.KeyListPrefix で始まる値にした操作で、helpAction が受け取る。

// helpExtension は、コマンドラインの表（exCommands）で :help と :keys に入れる拡張機能の ID の代わりの値である。
// 空白を含むので、宣言ファイルの拡張機能の ID（空白を含めない。ext.Manifest.Validate）と重ならない。
const helpExtension = " help"

// helpCommandNames は、:help のコマンドの名前である。Vim と同じく :h でも呼べる。
// 拡張機能のコマンドの口から呼ぶので、Vim のような途中までの省略（:he、:hel）はできない。
var helpCommandNames = []string{"help", "h"}

// keysCommandName は、キーの一覧を出すコマンドの名前（:keys {接頭辞}。名前は仮）である。
const keysCommandName = "keys"

// helpTitle と keyListTitle は、ヘルプとキーの一覧のタブの名前である（仮）。
const (
	helpTitle    = "[ヘルプ]"
	keyListTitle = "[キーの一覧]"
)

// addHelpCommands は、コマンドラインの表に :help、:h、:keys を足す。
// 拡張機能が同じ ID のコマンドを宣言していれば、本体のコマンドを優先し、誤りを返す（addExtManagerCommand と同じ）。
func addHelpCommands(cmds map[string]string) []error {
	var errs []error
	for _, name := range append(slices.Clone(helpCommandNames), keysCommandName) {
		if prev, ok := cmds[name]; ok {
			errs = append(errs, fmt.Errorf("拡張機能 %s のコマンド %s は本体の :%s と重なるので、コマンドラインからは呼べない", prev, name, name))
		}
		cmds[name] = helpExtension
	}
	return errs
}

// helpAction は、ref が :help、:keys、キーの一覧のキー（<leader>l? など）なら、それを行い、true と誤りを返す。
// それ以外の操作なら false を返す。text は、コマンドラインから呼んだときの引数である。
// bs は、操作を呼んだバッファである。出したポップアップへフォーカスが移ったら command.ErrBufferSwitched を返し、
// マクロの再生の残りをポップアップのバッファで続けさせる（navigator.Navigate の <Tab> と同じ）。
func (e *Editor) helpAction(bs *bufferState, ref editcmd.Ref, text string) (bool, error) {
	var err error
	switch {
	case ref.Extension == helpExtension && slices.Contains(helpCommandNames, ref.ID):
		err = e.openHelp(text)
	case ref.Extension == helpExtension && ref.ID == keysCommandName:
		prefix, perr := editcmd.ParseKeysLeader(strings.TrimSpace(text), e.leader)
		if perr != nil {
			return true, fmt.Errorf("E475: 無効な引数です: %s", text)
		}
		err = e.openKeyList("", prefix)
	case ref.Provider() && strings.HasPrefix(ref.ID, input.KeyListPrefix):
		group, prefix, ok := input.ParseKeyListID(ref.ID)
		if !ok {
			return true, fmt.Errorf("内部の誤り: キーの一覧の操作 %q を読めない", ref.ID)
		}
		err = e.openKeyList(group, prefix)
	default:
		return false, nil
	}
	if err == nil && e.cur != bs {
		err = command.ErrBufferSwitched
	}
	return true, err
}

// openHelpFloat は、ヘルプの文書 text を、画面の中央のテキストのポップアップに、フォーカスを入れて出し、
// カーソルを元の文書の line 行目（0 始まり）を折り返した最初の行の先頭に置く。
// 長い行は窓の幅で空白の位置で折り返す（ui.WrapText と同じ）。バッファは編集を禁止したファイルでないバッファである（FR-88）。
//
// req: FR-88, FR-89
func (e *Editor) openHelpFloat(text string, line int) {
	width := ui.PopupTextWidth(e.renderer.Width)
	var lines []string
	at := 0
	for i, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if i == line {
			at = len(lines)
		}
		w := ui.WrapText(l, width, e.scratchTabWidth())
		if len(w) == 0 {
			w = []string{""}
		}
		lines = append(lines, w...)
	}
	f := &floatState{floatContent: &floatContent{kind: floatText}, title: helpTitle, scratch: true, center: true, focused: true}
	f.bs = e.newPopupBuffer(helpTitle, strings.Join(lines, "\n"), "", true)
	f.bs.state.SetCursor(buffer.Position{Line: min(at, f.bs.buf.LineCount()-1)})
	e.openFloat(f)
}

// bufferTitle は、ステータス行に出すバッファの名前（パス。無ければファイルでないバッファの名前。どちらも無ければ空）を返す。
func bufferTitle(bs *bufferState) string {
	if bs.path != "" {
		return bs.path
	}
	return bs.title
}

// openHelp は、:help の引数 word の説明を出す。word が空なら先頭を出す。
// 説明は docs/usage.md（docs.Usage）に、本体のキーの節と、読み込んだ拡張機能の操作の節を足したものである（helpText）。
// 引き方は helpLine にある。見つからなければ E149 を返す（Vim と同じ番号）。
//
// req: FR-89
func (e *Editor) openHelp(word string) error {
	text := e.helpText()
	line := 0
	if w := strings.TrimSpace(word); w != "" {
		l, ok := helpLine(strings.Split(text, "\n"), w)
		if !ok {
			return fmt.Errorf("E149: %s のヘルプがありません", w)
		}
		line = l
	}
	e.openHelpFloat(text, line)
	return nil
}

// helpText は、:help に出す文書である。docs/usage.md の後に、本体のキーの節（helpKeyLines）と、
// 設定ファイルの Ex コマンドの別名の節（FR-150。足した別名があるときだけ）と、
// 読み込んだ拡張機能の操作の節（宣言ファイルの contributes の ID、種類、キー、description）を足す。コマンドの ID は : を付けて書く。
// 拡張機能のキーの <Leader> は、キーの一覧と同じく設定の leader に置き換えて出す。
//
// req: FR-150
func (e *Editor) helpText() string {
	var sb strings.Builder
	sb.WriteString(strings.TrimRight(docs.Usage, "\n"))
	sb.WriteString("\n\n## 本体のキー\n\n")
	sb.WriteString("本体の既定のキーと、LSP と DAP の操作の既定のキーの一覧である（キーの一覧の説明文と同じ）。")
	sb.WriteString("括弧の中はモード（n はノーマル、v はビジュアル、o はオペレーターの後）である。\n\n")
	sb.WriteString(strings.Join(helpKeyLines(e.keymap), "\n"))
	sb.WriteString("\n")
	if added := e.aliases.Added(); len(added) > 0 {
		sb.WriteString("\n## 設定ファイルの Ex コマンドの別名\n\n")
		sb.WriteString("設定ファイルの commandAliases で足した別名である。括弧の中は、打てる最も短い省略形である。\n\n")
		for _, a := range added {
			fmt.Fprintf(&sb, "- `:%s`: `:%s` の別名（`:%s`）\n", a.Name, a.Target, a.Name[:a.Min])
		}
	}
	var lines []string
	for _, s := range e.cfg.Extensions {
		m := s.Manifest
		groups := []struct {
			kind string
			cs   []ext.Contribution
			// colon は、ID の前に : を付けて書くことを表す。コマンドはコマンドラインから :ID で呼ぶので、
			// 本体の Ex コマンドと同じく :ID の形で書き、:help :ID でも :help ID でも引けるようにする（PR #309 のレビューの必須 1）。
			colon string
		}{
			{"オペレーター", m.Contributes.Operators, ""},
			{"モーション", m.Contributes.Motions, ""},
			{"テキストオブジェクト", m.Contributes.TextObjects, ""},
			{"コマンド", m.Contributes.Commands, ":"},
		}
		for _, g := range groups {
			for _, c := range g.cs {
				line := fmt.Sprintf("- `%s%s`（拡張機能 %s の%s", g.colon, c.ID, m.ID, g.kind)
				if c.Keys != "" {
					keys := c.Keys
					if ks, err := editcmd.ParseKeysLeader(c.Keys, e.leader); err == nil {
						keys = editcmd.KeysString(ks)
					}
					line += fmt.Sprintf("。キーは `%s`", keys)
				}
				line += "）"
				if c.Description != "" {
					line += ": " + c.Description
				}
				lines = append(lines, line)
			}
		}
	}
	if len(lines) > 0 {
		sb.WriteString("\n## 読み込んだ拡張機能の操作\n\n")
		sb.WriteString("本体が起動したときに読んだ宣言ファイルの contributes の一覧である。説明は宣言ファイルの description による。\n\n")
		sb.WriteString(strings.Join(lines, "\n"))
		sb.WriteString("\n")
	}
	return sb.String()
}

// helpKeyLines は、:help の「本体のキー」の節の行を、キーの表記の順（compareKeyLabels）に返す。
// 1 つのキーの表記に 1 行で、モードごとの説明を並べる（- `x`: 選択を消す（v）。カーソルの文字を消す（n））。
// 他のキーの接頭辞になるノーマルモードのキー（s、g、<Space>）には、続くキーと、その一覧を出すキー（s?）を添える。
// :help s が、囲みの sa、sd、sr へ案内する行に当たるようにするためである。
func helpKeyLines(km *input.Keymap) []string {
	descs := map[string][]string{}
	next := map[string][]string{}
	// 設定の keymap の操作が取り除いた既定のキー（Overridden）も、本体のキーとして出す。
	for _, b := range append(km.Bindings(), km.Overridden()...) {
		// 設定の keymap に操作の ID で書いた割り当て（b.User）は、本体のキーではないので出さない。
		if b.User || !b.Ref.Builtin() && !b.Ref.Provider() || strings.HasPrefix(b.Ref.ID, input.KeyListPrefix) {
			continue
		}
		label := editcmd.KeysString(b.Keys)
		descs[label] = append(descs[label], fmt.Sprintf("%s（%s）", b.Description, modeLetters(b.Modes)))
		if b.Modes&input.MapNormal == 0 {
			continue
		}
		for n := 1; n < len(b.Keys); n++ {
			p, c := editcmd.KeysString(b.Keys[:n]), editcmd.KeysString(b.Keys[:n+1])
			if !slices.Contains(next[p], c) {
				next[p] = append(next[p], c)
			}
			if _, ok := descs[p]; !ok {
				descs[p] = nil
			}
		}
	}
	// Parser が表を引く前に読むキー（q、@、.、" など）も足す。:help q が :q でなくマクロの記録に当たるようにする（#233）。
	// 引数の名前（{レジスター}）の前までを見出しのキーにし、引数を含む形は説明に添える。
	for _, k := range input.ParserKeys() {
		short, _, _ := strings.Cut(k.Keys, "{")
		if short == "" {
			short = k.Keys
		}
		desc := fmt.Sprintf("%s（%s）", k.Description, modeLetters(k.Modes))
		if short != k.Keys {
			desc += fmt.Sprintf("。`%s` と打つ", k.Keys)
		}
		descs[short] = append(descs[short], desc)
	}
	labels := make([]string, 0, len(descs))
	for l := range descs {
		labels = append(labels, l)
	}
	slices.SortFunc(labels, compareKeyLabels)
	lines := make([]string, 0, len(labels))
	for _, l := range labels {
		parts := slices.Clone(descs[l])
		if cs := next[l]; len(cs) > 0 {
			slices.SortFunc(cs, compareKeyLabels)
			parts = append(parts, fmt.Sprintf("続くキーは `%s`（`%s?` で一覧）", strings.Join(cs, "`、`"), l))
		}
		lines = append(lines, fmt.Sprintf("- `%s`: %s", l, strings.Join(parts, "。")))
	}
	return lines
}

// helpLine は、:help の文書の行 lines から、語 word の説明の行を探し、その行番号（0 始まり）を返す（基本設計 7.2）。
//
// 語は、キーの列（w、<C-o>）、Ex コマンドの名前（:w、:readonly）、設定の項目の名前（registers.historySize）である。
// Vim の :help と同じく、: の無い語はまずキーや設定の名前として、次に : を足した Ex コマンドとして引く。
// : で始まる語は Ex コマンドとしてだけ引く。
// 文書の中の `…`（コードの区画の外）を語の候補とし、次の順に探す。各段で、語の形の順に、最初に当たった行を返す（仮。#227）。
//
//  1. 定義の行（見出し、表の最初の列、「- `…`: 」で始まる箇条書き）のうち、`語` が完全に一致する行。
//     語の後に空白が続く形（`:acp.prompt <プロンプト>`）も完全な一致とみなす
//  2. `語` が完全に一致する行
//  3. 設定の例の JSON のキー（"語"）を含む行。点で区切った名前は、最後の部分（"historySize"）でも探す
//  4. 定義の行のうち、語で始まる `…` を含む行（:help reg で `:registers`）
//  5. 語が 2 文字以上なら、語を含む見出し
//  6. 操作の ID の一覧（docs/usage.md の action-ids の印の間）の行のうち、最初の列の `語` が完全に一致する行。
//     一覧の行は 1〜5 の候補にしない。rename や hover のように説明の節がある語は、その節に当てる（#257 のレビューの推奨 2）。
//     点で区切った名前（insert.right）は、3 の最後の部分（"right"）で探す前に、この段で探す。
//     どの語も、4 と 5 より先にこの段で探す（設定の名前 undo.persist が、操作の ID の undo の前方一致で当たらないように。#319）
//
// 語が操作の ID（一覧の行の ID と完全に一致する語）なら、操作の ID の節の本文（actionIDsSection の見出しから一覧の終わりまで。
// 書き方の規則の箇条書きや tmux の prefix の注記）と、設定の例で ID を値に書いた行（"action": "語"）は、1〜5 の候補にしない。
// このため ID の語は、その節の本文の言及より先に一覧の行に当たる。ただし、それより前の段で節の外の行に当たる語
// （rename、hover、delete、left など、main で説明の行に当たっていた語）は、その行に当てる（PR #258 の再レビューの推奨 2）。
//
// 語の一部だけを含む見出し（:help w が「同じ window の…」）には当てない（PR #230 のレビュー）。
func helpLine(lines []string, word string) (int, bool) {
	return newHelpIndex(lines).find(word)
}

// helpMDLine は、:help の文書の 1 行を、語を探すために読んだもの（helpIndex）である。
type helpMDLine struct {
	// spans は行の `…` の中身、defs はそのうち定義の行（見出し、表の最初の列、「- `…`: 」の箇条書き）の名前である。
	spans   []string
	defs    []string
	heading bool
}

// helpIndex は、:help の文書の行を、語を探すために 1 回だけ読んだものである（helpLine の 1〜6 の段の材料）。
// ヘルプの語の一覧（listHelpWords。FR-148）は、多くの語をこの 1 つの読みで引く。
type helpIndex struct {
	lines  []string
	parsed []helpMDLine
	// idRows は、操作の ID の一覧（docs/usage.md の action-ids の印の間）の行の、最初の列の `…` である。
	// 一覧の行は、他の段で当たる所が無い語のときだけ当てる（helpLine の 6。#257 のレビューの推奨 2）。
	idRows map[int][]string
	// idSection は、操作の ID の節（actionIDsSection の見出しの次から一覧の終わりの印まで）の、見出しでない行である。
	idSection map[int]bool
}

// newHelpIndex は、:help の文書の行 lines を読む。コードの区画の中の行は、語の候補にしない。
func newHelpIndex(lines []string) *helpIndex {
	parsed := make([]helpMDLine, len(lines))
	idRows := map[int][]string{}
	idSection := map[int]bool{}
	inCode, inIDs, inSection := false, false, false
	for i, l := range lines {
		if strings.HasPrefix(l, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		switch l {
		case actionIDsSection:
			inSection = true
		case actionIDsBegin:
			inIDs = true
			continue
		case actionIDsEnd:
			inIDs, inSection = false, false
			continue
		}
		if inSection && !strings.HasPrefix(l, "#") {
			idSection[i] = true
		}
		if inIDs {
			if cell, _, ok := strings.Cut(strings.TrimPrefix(l, "|"), " |"); ok {
				idRows[i] = codeSpans(cell)
			}
			continue
		}
		p := helpMDLine{spans: codeSpans(l), heading: strings.HasPrefix(l, "#")}
		switch {
		case p.heading:
			p.defs = p.spans
		case strings.HasPrefix(l, "|"):
			if cell, _, ok := strings.Cut(l[1:], " |"); ok && namesOnly(cell) {
				p.defs = codeSpans(cell)
			}
		case len(p.spans) > 0 && strings.HasPrefix(l, "- `"+p.spans[0]+"`:"):
			// 定義の箇条書きは「- `語`: 説明」の形（本体のキーの節）に限る。docs/usage.md の注記の箇条書き
			// （- `sa` の前の回数は…）を定義として扱わないためである（#233）。
			p.defs = p.spans[:1]
		}
		parsed[i] = p
	}
	return &helpIndex{lines: lines, parsed: parsed, idRows: idRows, idSection: idSection}
}

// find は、語 word の説明の行の行番号を返す（helpLine の 1〜6 の段）。
func (h *helpIndex) find(word string) (int, bool) {
	lines := h.lines
	forms := []string{word}
	if !strings.HasPrefix(word, ":") {
		forms = append(forms, ":"+word)
	}
	// 語が操作の ID（一覧の行の ID と完全に一致する）なら、操作の ID の節の本文（書き方の規則の箇条書きなど）と、
	// 設定の例で ID を値に書いた行（"action": "語"）を 2〜5 の候補にしない。その語は、節の本文より一覧の行に当てる。
	isID := false
	for _, ids := range h.idRows {
		if slices.Contains(ids, word) {
			isID = true
			break
		}
	}
	actionValue := `"action": "` + word + `"`
	skip := func(i int) bool { return isID && (h.idSection[i] || strings.Contains(lines[i], actionValue)) }
	line := func(i int) helpMDLine {
		if skip(i) {
			return helpMDLine{}
		}
		return h.parsed[i]
	}
	exact := func(span, f string) bool { return span == f || strings.HasPrefix(span, f+" ") }
	find := func(pick func(helpMDLine) []string, match func(string, string) bool) (int, bool) {
		for _, f := range forms {
			for i := range h.parsed {
				if slices.ContainsFunc(pick(line(i)), func(s string) bool { return match(s, f) }) {
					return i, true
				}
			}
		}
		return 0, false
	}
	defs := func(p helpMDLine) []string { return p.defs }
	spans := func(p helpMDLine) []string { return p.spans }
	if i, ok := find(defs, exact); ok {
		return i, true
	}
	if i, ok := find(spans, exact); ok {
		return i, true
	}
	keys := []string{`"` + word + `"`}
	if j := strings.LastIndexByte(word, '.'); j >= 0 && j+1 < len(word) {
		keys = append(keys, `"`+word[j+1:]+`"`)
	}
	idRow := func() (int, bool) {
		for i := range lines {
			if slices.Contains(h.idRows[i], word) {
				return i, true
			}
		}
		return 0, false
	}
	for n, k := range keys {
		if n == 1 {
			// 点で区切った名前の最後の部分（"left"）より、操作の ID の一覧の行（insert.left）を先に当てる。
			if i, ok := idRow(); ok {
				return i, true
			}
		}
		for i, l := range lines {
			if !skip(i) && strings.Contains(l, k) {
				return i, true
			}
		}
	}
	// 操作の ID の語は、前方一致（4）と見出し（5）より先に一覧の行を当てる。設定の名前（undo.persist）が操作の ID（undo）で
	// 始まるとき、前方一致でその設定の行に当たらないようにする（#319）。
	if i, ok := idRow(); ok {
		return i, true
	}
	if i, ok := find(defs, strings.HasPrefix); ok {
		return i, true
	}
	if utf8.RuneCountInString(word) >= 2 {
		for i := range h.parsed {
			if line(i).heading && strings.Contains(lines[i], word) {
				return i, true
			}
		}
	}
	return 0, false
}

// listHelpWords は、:help で引ける語の一覧（editor/list の help。FR-148）を返す。
//
// 語は、次の順に集め、:help でその語の説明の行が見つかる（helpLine）ものだけを残す。同じ語は最初の 1 つだけにする。
//
//   - キー: :help の「本体のキー」の節のキー（helpKeyLines。LSP と DAP の操作の既定のキーと、回数などの Parser が先に読むキーを含む）と、
//     読み込んだ拡張機能の宣言ファイルの操作の既定のキー（keys。<Leader> は設定の leader にする）
//   - Ex コマンド: コマンドの一覧（listCommands。本体の Ex コマンド、別名、拡張機能のコマンド）の名前に : を付けたもの。
//     拡張機能のコマンドは、:help の「読み込んだ拡張機能の操作」の節に :ID の形で書く（helpText）ので、同梱でない拡張機能のものも引ける
//   - 設定の名前: 実行中に変えられる設定の表（internal/option）の名前と、設定ファイルの最上位の項目の名前（config.TopLevelNames）
//   - 操作の ID: 設定の keymap に書ける操作の ID（input.Actions）
//   - 拡張機能の操作の ID: 読み込んだ拡張機能のオペレーター、モーション、テキストオブジェクトの ID（:help の「読み込んだ拡張機能の操作」の節）
//
// 説明は、その語を定めた所の説明文である。キーは「本体のキー」の節の行の説明か宣言ファイルの description、拡張機能のコマンドと操作は
// 宣言ファイルの description（無ければ「拡張機能 <ID> の<種類>」）、操作の ID は操作の表の説明文である。
// 定めた所に説明文が無い語（本体の Ex コマンド、設定の名前）だけ、:help で見つかった行を 1 行の要約にしたもの（helpSummary）にする。
// 設定ファイルの最上位の名前は、docs/usage.md の「設定ファイルの項目」の表の行に当たる。
// :help で見つかった行は、語の説明でない行（プレビューの表の行など）のことがあるためである（PR #309 のレビューの推奨 3）。
// 文書は :help と同じもの（helpText）を 1 回だけ読む。
//
// req: FR-148
func (e *Editor) listHelpWords() []ext.ListHelpWord {
	lines := strings.Split(e.helpText(), "\n")
	idx := newHelpIndex(lines)
	var out []ext.ListHelpWord
	seen := map[string]bool{}
	// add は、語 word を、説明 desc（空なら :help で見つかった行の要約）で一覧に足す。
	add := func(word, kind, desc string) {
		if word == "" || seen[word] {
			return
		}
		seen[word] = true
		i, ok := idx.find(word)
		if !ok {
			return
		}
		if desc == "" {
			desc = helpSummary(lines[i], word)
		}
		out = append(out, ext.ListHelpWord{Word: word, Kind: kind, Description: desc})
	}
	for _, l := range helpKeyLines(e.keymap) {
		if spans := codeSpans(l); len(spans) > 0 {
			_, desc, _ := strings.Cut(l, "`"+spans[0]+"`: ")
			add(spans[0], ext.HelpKindKey, desc)
		}
	}
	// 拡張機能の操作の説明は宣言ファイルの description で、書いていなければ「拡張機能 <ID> の<種類>」にする
	// （:help の「読み込んだ拡張機能の操作」の節の括弧の中と同じ。語そのものを説明に残さない。PR #309 の再レビューの推奨 4）。
	type contribution struct {
		c       ext.Contribution
		desc    string
		command bool
	}
	var contribs []contribution
	for _, s := range e.cfg.Extensions {
		m := s.Manifest
		for _, g := range []struct {
			kind string
			cs   []ext.Contribution
		}{
			{"オペレーター", m.Contributes.Operators},
			{"モーション", m.Contributes.Motions},
			{"テキストオブジェクト", m.Contributes.TextObjects},
			{"コマンド", m.Contributes.Commands},
		} {
			for _, x := range g.cs {
				desc := x.Description
				if desc == "" {
					desc = fmt.Sprintf("拡張機能 %s の%s", m.ID, g.kind)
				}
				contribs = append(contribs, contribution{c: x, desc: desc, command: g.kind == "コマンド"})
			}
		}
	}
	for _, x := range contribs {
		if ks, err := editcmd.ParseKeysLeader(x.c.Keys, e.leader); err == nil && len(ks) > 0 {
			add(editcmd.KeysString(ks), ext.HelpKindKey, x.desc)
		}
	}
	for _, c := range e.listCommands() {
		desc := c.Description
		if desc == "" && c.Extension != "" {
			desc = fmt.Sprintf("拡張機能 %s のコマンド", c.Extension)
		}
		add(":"+c.Name, ext.HelpKindCommand, desc)
	}
	for _, d := range option.Defs() {
		add(d.Name, ext.HelpKindOption, "")
	}
	for _, n := range config.TopLevelNames() {
		add(n, ext.HelpKindOption, "")
	}
	for _, a := range input.Actions() {
		add(a.ID, ext.HelpKindAction, a.Description)
	}
	for _, x := range contribs {
		if !x.command {
			add(x.c.ID, ext.HelpKindOperation, x.desc)
		}
	}
	return out
}

// mdLink は、Markdown のリンク（[文](#見出し)）である。helpSummary が文だけを残す。
var mdLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// helpSummaryMax は、ヘルプの語の説明（helpSummary）の文字数の上限である。超えた分は … にする（仮）。
const helpSummaryMax = 160

// helpSummary は、:help の文書の行 line（語 word の説明の行）を、ヘルプの語の一覧に出す 1 行の説明にする（FR-148）。
//
//   - 見出しは、# を除いた見出しの文
//   - 表の行は、最初の列が語を含めば 2 つ目からの列を、含まなければすべての列を、空白 2 つで区切って並べたもの
//   - 「- `語`: 説明」の箇条書き（本体のキーの節、拡張機能の操作の節）は、説明
//   - ほかの行は、行の文（箇条書きの - を除く）
//
// どれも、` を除き、リンクは文だけを残し、\| は | にする。
func helpSummary(line, word string) string {
	s := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(s, "#"):
		s = strings.TrimLeft(s, "# ")
	case strings.HasPrefix(s, "|"):
		cells := strings.Split(strings.Trim(s, "| "), " | ")
		if len(cells) > 1 && slices.ContainsFunc(codeSpans(cells[0]), func(c string) bool {
			return c == word || strings.HasPrefix(c, word+" ") || c == ":"+word
		}) {
			cells = cells[1:]
		}
		s = strings.Join(cells, "  ")
	default:
		s = strings.TrimPrefix(s, "- ")
		if spans := codeSpans(s); len(spans) > 0 {
			if rest, ok := strings.CutPrefix(s, "`"+spans[0]+"`"); ok {
				if rest, ok = strings.CutPrefix(rest, ":"); ok {
					s = strings.TrimSpace(rest)
				}
			}
		}
	}
	s = mdLink.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "`", ""), `\|`, "|")
	if r := []rune(s); len(r) > helpSummaryMax {
		s = string(r[:helpSummaryMax]) + "…"
	}
	return s
}

// 操作の ID の一覧の印（docs/usage.md。internal/input の TestActionIDDoc が同じ印で表を照合する）。
const (
	actionIDsBegin = "<!-- action-ids:begin -->"
	actionIDsEnd   = "<!-- action-ids:end -->"
	// actionIDsSection は、操作の ID の節の見出しである。この見出しから actionIDsEnd までの本文を、操作の ID の語の候補から外す（helpLine）。
	actionIDsSection = "### 操作の ID"
)

// namesOnly は、表の列 cell が `…` の名前を並べただけ（`:w`、`:write`（`:w`）、`<C-]>` か `<leader>lD`）かを返す。
// 名前の外に文が書かれた列（`dw`、`x` など、名前を省略した削除）は、その名前の定義の行として扱わない。
func namesOnly(cell string) bool {
	rest := cell
	for {
		i := strings.IndexByte(rest, '`')
		if i < 0 {
			break
		}
		j := strings.IndexByte(rest[i+1:], '`')
		if j < 0 {
			return false
		}
		rest = rest[:i] + " " + rest[i+1+j+1:]
	}
	return strings.Trim(rest, " 、/（）()か") == "" && strings.Contains(cell, "`")
}

// codeSpans は、Markdown の行 l の `…` の中身を、左から順に返す。“ ` “ のように 2 つ以上の ` で囲んだものも扱う。
func codeSpans(l string) []string {
	var out []string
	for {
		i := strings.IndexByte(l, '`')
		if i < 0 {
			return out
		}
		n := 1
		for i+n < len(l) && l[i+n] == '`' {
			n++
		}
		fence := l[i : i+n]
		rest := l[i+n:]
		j := strings.Index(rest, fence)
		if j < 0 {
			return out
		}
		out = append(out, strings.TrimSpace(rest[:j]))
		l = rest[j+n:]
	}
}

// keySource は、キーの一覧の行の出どころである。
type keySource int

const (
	// sourceBuiltin は、本体の既定のキー（LSP と DAP の既定のキーと、回数などの Parser が先に読むキーを含む）である。
	sourceBuiltin keySource = iota
	// sourceUser は、利用者のキーマップ（設定の keymap）である。
	sourceUser
	// sourceExtension は、拡張機能の宣言ファイルの contributes である。
	sourceExtension
)

// keyRow は、キーの一覧の 1 行である。表示の文字列（keyListLines）とは別に、キーの列と操作を持つ。
// 一覧の部品（基本設計 1.4）で選んだキーを実行するとき（selectKeyRow）と、拡張機能が本体のキーの一覧を問い合わせるとき（FR-107）に使う。
type keyRow struct {
	// Keys は、キーの列である。Parser が先に読むキー（{回数} など）では nil で、Label だけを持つ。
	Keys []editcmd.Key
	// Label は、キーの表記である（editcmd.KeysString。{回数} のような引数の名前を含むことがある）。
	Label string
	// Modes は、キーが効くモードである。CommandLine は、利用者のキーマップのコマンドラインモードの項目であることを表す。
	Modes       input.MapMode
	CommandLine bool
	// Source は出どころで、Extension は sourceExtension のときの拡張機能の ID である。
	Source    keySource
	Extension string
	// Kind と Ref は、キーマップの登録の操作である。利用者のキーマップの行では使わない。
	Kind input.BindingKind
	Ref  editcmd.Ref
	// RHS は、利用者のキーマップの展開先のキーの列である。
	RHS []editcmd.Key
	// Description は、説明文である。
	Description string
	// Overridden は、利用者のキーマップが、同じキーの列かその手前の部分（s に対する sa など）を割り当てていて、
	// この行の操作が効かないモードの集合である。0 なら上書きされていない。
	// キーマップの展開は次のキーを待たないので、手前の部分を割り当てると、続くキーは打てなくなる（docs/usage.md。#233）。
	Overridden input.MapMode
}

// openKeyList は、接頭辞 prefix で始まるキーと、分類 group（input.KeyGroupLSP など。空なら無し）の操作のキーの一覧を出す。
// どちらも空ならすべてのキーを出す。該当するキーが無ければ誤りを返す。
//
// req: FR-90
func (e *Editor) openKeyList(group string, prefix []editcmd.Key) error {
	rows := e.keyRows(group, prefix)
	if len(rows) == 0 {
		return fmt.Errorf("%s で始まるキーはありません", editcmd.KeysString(prefix))
	}
	head, lines := keyListLines(group, prefix, rows)
	f := &floatState{floatContent: &floatContent{kind: floatList}, title: head, scratch: true, center: true, focused: true}
	f.bs = e.newPopupBuffer(keyListTitle, strings.Join(lines, "\n"), "", true)
	f.rows = make([]floatRow, len(rows))
	f.onSelect = func(i int) { e.selectKeyRow(rows[i]) }
	e.openFloat(f)
	return nil
}

// selectKeyRow は、キーの一覧で選んだ行 r のキーを、ポップアップを閉じてから、そのキーを打ったのと同じく実行する（FR-90。#236）。
// 利用者のキーマップを通すので、設定の keymap が上書きした本体の行を選ぶと、上書きした方（打ったときに効く方）を実行する
// （PR #230 の未確認事項へのこの PR の決定）。ノーマルモードのキーでない行（インサートモードだけのキーなど）と、
// 操作の表に無い Parser のキー（回数、レジスター、マクロ、ドットリピート、do と dp）は、実行せずに理由を知らせる。
//
// req: FR-90
func (e *Editor) selectKeyRow(r keyRow) {
	e.closeFloat(true)
	switch {
	case len(r.Keys) == 0:
		e.message = infoMessage(fmt.Sprintf("%s は操作の表に無いキー（回数、レジスター、マクロなど）なので、一覧からは実行できません", r.Label))
		return
	case r.CommandLine || r.Modes&input.MapNormal == 0:
		e.message = infoMessage(fmt.Sprintf("%s はノーマルモードのキーではないので、一覧からは実行できません", r.Label))
		return
	}
	// 選んだ <CR> はマクロに記録されている（popupStateKey）ので、選んだキーは記録せずに渡す。再生では <CR> がもう一度選ぶ。
	e.noRecord = true
	defer func() { e.noRecord = false }()
	for _, k := range r.Keys {
		e.feedMapped(e.remap.feed(e.mapMode(), k))
		if e.suspension() != nil {
			return
		}
	}
	e.feedMapped(e.remap.flush())
}

// keyRows は、接頭辞 prefix で始まるキーと、分類 group の操作のキーの一覧の行を返す（基本設計 7.2）。
//
// 行は次の 3 つから作る。
//
//   - キーマップ（本体の既定のキー、LSP と DAP の既定のキー、拡張機能の contributes のキー）。説明は input.Binding.Description で、
//     拡張機能の項目に説明文が無ければ操作の ID を出す。group が空でなければ、その分類の LSP か DAP の操作のキー（K、gq、<C-]> など）も、
//     接頭辞で始まらなくても出す（input.ProviderGroup。#227 のオーケストレータの決定）
//   - 利用者のキーマップ（設定の keymap）。右辺をキーの列で書いた項目の説明は展開先のキーの列である。説明文を書く項目は設定に足していない（仮）。
//     右辺に操作の ID を書いた項目（input.Binding.User。FR-126）はキーマップに登録してあり、出どころを「設定」、説明を操作の表の説明文にする。
//     その項目が取り除いた既定のキー（input.Keymap.Overridden）は、取り除いたモードを Overridden にして出す
//   - Parser が表を引く前に読むキー（回数、レジスター、マクロ、ドットリピート。input.ParserKeys）。prefix と group が空のときだけ出す
//
// 利用者のキーマップが本体か拡張機能のキーと同じキーの列か、その手前の部分を同じモードで割り当てていれば、その行の Overridden に
// そのモードを入れる（効くのは利用者のキーマップ。#233）。
// 並びは、キーの表記を大文字と小文字を区別せずに比べた順（同じなら小文字が先）で、キーの一覧の操作（? の行）は末尾に回す。
// Parser が先に読むキーは、さらにその後に置く。
func (e *Editor) keyRows(group string, prefix []editcmd.Key) []keyRow {
	var rows []keyRow
	user := map[input.MapMode][][]editcmd.Key{}
	if r := e.remap; r != nil {
		for mode, mm := range map[mapMode]input.MapMode{mapNormal: input.MapNormal, mapVisual: input.MapVisual, mapInsert: input.MapInsert} {
			for _, m := range r.maps[mode] {
				user[mm] = append(user[mm], m.lhs)
			}
		}
	}
	overridden := e.keymap.Overridden()
	bindings := append(e.keymap.Bindings(), overridden...)
	for i, b := range bindings {
		inGroup := group != "" && b.Ref.Provider() && input.ProviderGroup(b.Ref.ID) == group
		if !hasKeyPrefix(b.Keys, prefix) && !inGroup {
			continue
		}
		row := keyRow{
			Keys: b.Keys, Label: editcmd.KeysString(b.Keys), Modes: b.Modes,
			Kind: b.Kind, Ref: b.Ref, Description: b.Description,
		}
		switch {
		case b.User:
			// 設定の keymap に操作の ID で書いた割り当て（FR-126）。説明文は操作の表の説明文である。
			row.Source = sourceUser
		case !b.Ref.Builtin() && !b.Ref.Provider():
			row.Source, row.Extension = sourceExtension, b.Ref.Extension
			if row.Description == "" {
				row.Description = b.Ref.ID
			}
		}
		if i >= len(bindings)-len(overridden) {
			// 設定の keymap の操作が取り除いた登録（input.Keymap.Overridden）。取り除いたモードでは設定が効く。
			row.Overridden = b.Modes
		}
		for mm, lhss := range user {
			if b.Modes&mm != 0 && slices.ContainsFunc(lhss, func(lhs []editcmd.Key) bool { return hasKeyPrefix(b.Keys, lhs) }) {
				row.Overridden |= mm
			}
		}
		rows = append(rows, row)
	}
	if r := e.remap; r != nil {
		for _, mode := range []mapMode{mapNormal, mapVisual, mapInsert, mapCommandLine} {
			for _, m := range r.maps[mode] {
				if !hasKeyPrefix(m.lhs, prefix) {
					continue
				}
				row := keyRow{
					Keys: m.lhs, Label: editcmd.KeysString(m.lhs), Source: sourceUser, RHS: m.rhs,
					Description: "→ " + editcmd.KeysString(m.rhs), CommandLine: mode == mapCommandLine,
				}
				switch mode {
				case mapNormal:
					row.Modes = input.MapNormal
				case mapVisual:
					row.Modes = input.MapVisual
				case mapInsert:
					row.Modes = input.MapInsert
				case mapNone, mapCommandLine:
				}
				rows = append(rows, row)
			}
		}
	}
	slices.SortStableFunc(rows, func(a, b keyRow) int {
		ak, bk := strings.HasPrefix(a.Ref.ID, input.KeyListPrefix), strings.HasPrefix(b.Ref.ID, input.KeyListPrefix)
		if ak != bk {
			if ak {
				return 1
			}
			return -1
		}
		return compareKeyLabels(a.Label, b.Label)
	})
	if len(prefix) == 0 && group == "" {
		for _, k := range input.ParserKeys() {
			rows = append(rows, keyRow{Label: k.Keys, Modes: k.Modes, Description: k.Description})
		}
	}
	return rows
}

// compareKeyLabels は、キーの表記を、大文字と小文字を区別せずに比べ、同じなら小文字を先にする順で比べる（<Space>ld、<Space>lD、<Space>le の順）。
func compareKeyLabels(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return -strings.Compare(a, b)
}

// keyListLines は、キーの一覧の行 rows を、列をそろえた 1 行ずつの文字列にし、一覧の見出しと一緒に返す。
// 列はキー、モード（n、v、o、i、c）、出どころ（本体、設定、拡張機能の ID）、説明文の順である。
func keyListLines(group string, prefix []editcmd.Key, rows []keyRow) (head string, lines []string) {
	switch {
	case group != "":
		head = fmt.Sprintf("%s のキーの一覧（%s で始まるものと %s の操作。%d 件）", strings.ToUpper(group), editcmd.KeysString(prefix), strings.ToUpper(group), len(rows))
	case len(prefix) > 0:
		head = fmt.Sprintf("キーの一覧（%s で始まるもの。%d 件）", editcmd.KeysString(prefix), len(rows))
	default:
		head = fmt.Sprintf("キーの一覧（すべて。%d 件）", len(rows))
	}
	head += "。<CR> で実行、<Esc> で閉じる"
	cells := make([][3]string, len(rows))
	width := [3]int{}
	for i, r := range rows {
		modes := modeLetters(r.Modes)
		if r.CommandLine {
			modes += "c"
		}
		origin := "本体"
		switch r.Source {
		case sourceUser:
			origin = "設定"
		case sourceExtension:
			origin = "拡張機能 " + r.Extension
		case sourceBuiltin:
		}
		cells[i] = [3]string{r.Label, modes, origin}
		for j, c := range cells[i] {
			width[j] = max(width[j], ui.StringWidth(c, 8))
		}
	}
	pad := func(s string, w int) string { return s + strings.Repeat(" ", w-ui.StringWidth(s, 8)+2) }
	lines = make([]string, len(rows))
	for i, r := range rows {
		desc := r.Description
		if r.Overridden == r.Modes && r.Modes != 0 {
			desc += "（設定の keymap が優先）"
		} else if r.Overridden != 0 {
			// 一部のモードだけを上書きしたとき（{"normal": {"gq": …}} の gq の n）は、上書きしたモードを書く（#233）。
			desc += fmt.Sprintf("（%s では設定の keymap が優先）", modeLetters(r.Overridden))
		}
		lines[i] = strings.TrimRight(pad(cells[i][0], width[0])+pad(cells[i][1], width[1])+pad(cells[i][2], width[2])+desc, " ")
	}
	return head, lines
}

// hasKeyPrefix は、keys が prefix で始まるかを返す。
func hasKeyPrefix(keys, prefix []editcmd.Key) bool {
	return len(keys) >= len(prefix) && slices.Equal(keys[:len(prefix)], prefix)
}

// modeLetters は、モードの集合を n、v、o、i の文字の列にする。
func modeLetters(m input.MapMode) string {
	var sb strings.Builder
	for _, x := range []struct {
		mode input.MapMode
		c    byte
	}{{input.MapNormal, 'n'}, {input.MapVisual, 'v'}, {input.MapOperatorPending, 'o'}, {input.MapInsert, 'i'}} {
		if m&x.mode != 0 {
			sb.WriteByte(x.c)
		}
	}
	return sb.String()
}

// addKeyListBindings は、ノーマルモードのキーの登録の接頭辞のうち、それ自体は操作でないもの（<Space>、<Space>l、g、<C-w> など）に、
// ? を続けたキーの一覧の操作（input.KeyListBinding）を足す（基本設計 7.2。仮）。
// 接頭辞は、km の登録（本体、LSP と DAP、拡張機能）と、利用者のキーマップ remap の左辺から集める。
// 既に登録のあるキーの列（既定の <leader>l? など）と、Parser が先に読むキーで始まるものは足さない。
// g? は Vim の rot13 のオペレーターと重なるが、本体に rot13 は無い（docs/vim-differences.md）。
//
// req: FR-90
func addKeyListBindings(km *input.Keymap, remap *remapper) {
	var seqs [][]editcmd.Key
	for _, b := range km.Bindings() {
		if b.Modes&input.MapNormal != 0 {
			seqs = append(seqs, b.Keys)
		}
	}
	userLHS := map[string]bool{}
	if remap != nil {
		for _, m := range remap.maps[mapNormal] {
			seqs = append(seqs, m.lhs)
			userLHS[editcmd.KeysString(m.lhs)] = true
		}
	}
	prefixes := map[string][]editcmd.Key{}
	for _, s := range seqs {
		for n := 1; n < len(s); n++ {
			prefixes[editcmd.KeysString(s[:n])] = s[:n]
		}
	}
	names := make([]string, 0, len(prefixes))
	for name := range prefixes {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		p := prefixes[name]
		if _, ok := km.Lookup(input.MapNormal, p); ok || userLHS[name] {
			continue
		}
		// 既にあるキーの列（既定の <leader>l?）と先に読むキーは、Bind が断る。断られたら足さない。
		_ = km.Bind(input.KeyListBinding(p, "", name+" で始まるキーの一覧"))
	}
}
