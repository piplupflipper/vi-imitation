package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// この節は、止まっている間に、選んでいるフレームの変数の値を、ソースの行に仮想テキスト（virtualText/set。基本設計 6.1 の「仮想テキスト」）で
// 出す処理である（FR-296。基本設計 6.4 の「#426 の行末の変数の値」）。オーナーの nvim の nvim-dap-virtual-text（コミット
// fbdb48c2ed45f4a8293d0d483f7730d24467ccb6）に当たる。
//
// nvim-dap-virtual-text は、tree-sitter の locals のクエリで変数の定義の位置を探し、止まった行を含むスコープの中の、同じ名前の最初の定義の
// 行に値を出す（lua/nvim-dap-virtual-text/virtual_text.lua の set_virtual_text。only_first_definition が既定の true、all_references が
// 既定の false）。DAP の拡張機能には構文の情報を得る口が無い（拡張機能 API に、tree-sitter の拡張機能の構文木を問い合わせる口が無い）ので、
// 名前の一致で通す。止まった行を含む関数の範囲を字下げと関数の見出しの行の形で見積もり（frameRegion）、その中で変数の名前が定義の形
// （関数の見出しの引数、代入の左辺、var や let や for の後ろ。isDefinition）で最初に現れる行に出す（firstOccurrences）。
// 中の入れ子の関数の本体は飛ばす。
//
// 変数の値は、スコープの区画と同じ読み込み（loadScopes と varsOf。セッションと世代ごとの写し）を使い、同じ scopes と variables を 2 回送らない。
// 区画が見えていなくても、ペインを閉じていても、選んでいるフレームの expensive でないスコープの最上位の変数だけは読む。
// 読み始める時機は区画と同じ規則に従う（止まった位置を出し終えるまで読まない、続けて打っている間は settleDelay だけ待つ、動き出してから
// runningShowDelay の間は消さない。pane.refresh）。

// vtKey は、行末の変数の値の作り直しの印（pane.markDirty）の名前である。区画の ID と重ならないよう、区切りの文字で始める。
const vtKey = "\x1fvirtualText"

// vtSettings は、設定の virtualText（FR-296）である。既定は出さない（#317 の方針。本体の既定は出さず、同梱の設定の例にオーナーの nvim の値を書く）。
// 出すときの既定は nvim-dap-virtual-text の既定に合わせる。
type vtSettings struct {
	// enabled は、出すかである。既定は false。
	enabled bool
	// placement は、置き方（行末の eol か、変数の名前の直後の inline）である。既定は inline（nvim-dap-virtual-text の virt_text_pos の
	// Neovim 0.10 からの既定）。
	placement ext.VirtualTextPlacement
	// commented は、文字列をバッファのコメントの形（設定 comment.string）で包むかである（nvim-dap-virtual-text の commented）。既定は false。
	commented bool
	// maxLength は、値の文字数の上限である。0 なら設定では切らない（nvim-dap-virtual-text は切らない）。どちらでも maxLabelRunes で切る。
	maxLength int
	// highlightChanged は、前の停止から値の変わった変数を diagnostic.warning の色で出すかである（nvim-dap-virtual-text の
	// highlight_changed_variables。既定は true）。
	highlightChanged bool
}

func defaultVTSettings() vtSettings {
	return vtSettings{placement: ext.VirtualTextInline, highlightChanged: true}
}

// parseVTSettings は、設定の virtualText を読む。知らない項目と型の誤りは誤りにする。
//
// req: FR-296
func parseVTSettings(raw any) (vtSettings, error) {
	v := defaultVTSettings()
	obj, ok := raw.(map[string]any)
	if !ok {
		return v, errors.New("virtualText is not an object")
	}
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range obj {
			if !yield(k) {
				return
			}
		}
	}) {
		x := obj[k]
		switch k {
		case "enabled", "commented", "highlightChanged":
			b, ok := x.(bool)
			if !ok {
				return v, fmt.Errorf("virtualText.%s is not a boolean", k)
			}
			switch k {
			case "enabled":
				v.enabled = b
			case "commented":
				v.commented = b
			default:
				v.highlightChanged = b
			}
		case "position":
			// overlay（本文の上に重ねる）は、本文を隠すので受けない。
			s, _ := x.(string)
			pl := ext.VirtualTextPlacement(s)
			if pl != ext.VirtualTextEOL && pl != ext.VirtualTextInline {
				return v, fmt.Errorf("virtualText.position %v is not \"eol\" or \"inline\"", x)
			}
			v.placement = pl
		case "maxLength":
			n, ok := x.(json.Number)
			i, err := n.Int64()
			if !ok || err != nil || i < 0 {
				return v, fmt.Errorf("virtualText.maxLength %v is not a non-negative integer", x)
			}
			v.maxLength = int(i)
		default:
			return v, fmt.Errorf("virtualText.%s is not a known item", k)
		}
	}
	return v, nil
}

// frameLoc は、フレームのソースの位置（本体の 0 から数える行）と関数の名前である（session.frameAt）。作った後は変えない（ずらすときは作り直す）。
type frameLoc struct {
	path string
	line int
	name string
}

// frameLocOf は、フレーム f のソースの位置を返す。ソースのパスが無ければ nil。
func frameLocOf(f stackFrame) *frameLoc {
	if f.Source == nil || f.Source.Path == "" {
		return nil
	}
	return &frameLoc{path: f.Source.Path, line: fromDAPLine(f.Line, linesStartAt1), name: f.Name}
}

// vtSent は、1 つのバッファへ最後に送った仮想テキストの一覧と、そのもとにしたバージョン番号である。
type vtSent struct {
	version ext.Version
	texts   []ext.VirtualText
}

// vtFrameKey は、値の変化を比べるフレームの鍵（セッション、ファイル、関数の名前）である。nvim-dap-virtual-text はフレームの ID で比べるが、
// アダプターは止まるたびにフレームの ID を振り直すので、関数で比べる。
type vtFrameKey struct {
	s          *session
	path, name string
}

// vtState は、行末の変数の値の状態である。pane.run だけが触る。
type vtState struct {
	// shown は、空でない一覧を送ったバッファから、送ったものへの対応である。
	shown map[ext.BufferID]vtSent
	// comments は、バッファの設定 comment.string の値である（commented のとき、option/get で読んだもの）。buffer/open で捨てる。
	comments map[ext.BufferID]string
	// key と stop は、cur を読んだフレームと停止（止まった位置を出し始めた時刻）である。base は、同じフレームの前の停止の値である。
	key       vtFrameKey
	stop      time.Time
	base, cur map[string]string
}

// vtVar は、行末に出す候補の変数（選んでいるフレームのスコープの最上位の変数）である。
type vtVar struct {
	name, value string
	// locals は、presentationHint が locals のスコープの変数かである。
	locals bool
}

// bufferOpened は、バッファ id が開かれた（ファイルの種類かパスを変えて開き直されたときも）ことを知らせる。debugger.mu を持ったまま呼ぶ。
// enabled は、設定の virtualText.enabled である。
func (p *pane) bufferOpened(id ext.BufferID, enabled bool) {
	if !enabled {
		return
	}
	p.post(func() { delete(p.vt.comments, id) })
	p.markDirty(vtKey)
}

// bufferClosed は、バッファ id が閉じられたことを知らせる。本体はバッファごと仮想テキストを消すので、送ったものを捨てる。
func (p *pane) bufferClosed(id ext.BufferID, enabled bool) {
	if !enabled {
		return
	}
	p.post(func() {
		delete(p.vt.shown, id)
		delete(p.vt.comments, id)
	})
}

// settleHold は、止まっているセッションのうち、止まった位置を出し始めてから settleDelay が経っていないものの残りの時間を返す（無ければ 0）。
// 行末の変数の値は、区画と違い、続けて打っているかに依らず、止まってから settleDelay だけ待って読む。区画の規則（続けて打っているときだけ
// 待つ）では、続けて打ち始めた最初のステップ実行の停止で読み始め、0.1 秒の間で打った次の next がその後ろに並んで、ペインを閉じていても
// 1〜2 回遅れた（#426 の測り）。読むのは scopes と数個の variables で、vscode-js-debug でも約 0.12 秒で読み終えるので、待っても 0.4 秒の間の
// 次の next とは重ならない。
func (d *debugger) settleHold() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	var hold time.Duration
	for _, s := range d.sessions {
		if s.halted && !s.shownAt.IsZero() {
			hold = max(hold, settleDelay-time.Since(s.shownAt))
		}
	}
	return hold
}

// vtSettings は、設定の virtualText を返す。
func (d *debugger) vtSettings() vtSettings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settings.vt
}

// refreshVirtualText は、印（vtKey）が付いていれば、行末の変数の値を出し直す（FR-296）。run で呼ぶ。
// 止まった位置を出し終えるまでと、続けて打っている間の settleDelay と、動き出してから runningShowDelay の間は、印を残して待つ
// （区画と同じ。ステップ実行のたびに消してから出し直すと、値がちらつき、本体の描き直しの分だけ止まった位置への移動が遅れる）。
// 止まっていなければ（動いている、終わった）、出したものを消す。
//
// req: FR-296
func (p *pane) refreshVirtualText() {
	if !p.isDirty(vtKey) {
		return
	}
	cfg := p.d.vtSettings()
	if !cfg.enabled && len(p.vt.shown) == 0 {
		p.takeDirty(vtKey)
		return
	}
	if p.d.stoppingAny() {
		return
	}
	if hold := max(p.d.runningHold(), p.d.settleHold()); hold > 0 {
		p.holdOnce(hold)
		return
	}
	p.takeDirty(vtKey)
	want, x, err := p.virtualTextsFor(cfg)
	if err != nil {
		// 読んでいる間に止まった状態が変わった。変えた変わり目が印を付け直すが、念のため残す。
		p.markDirty(vtKey)
		return
	}
	if x != nil && x.s.gen.Load() != x.gen {
		// 作り終えた後に動き出したか選び直した。古い値を送らない。
		p.markDirty(vtKey)
		return
	}
	p.sendVirtualTexts(want)
}

// virtualTextsFor は、今のセッションの選んでいるフレームの変数の値の仮想テキストを、バッファごとに作る。止まっていない、フレームの
// ソースのファイルを開いていない、などで出すものが無ければ空を返す。作ったときのセッションの写しを x で返す（無ければ nil）。
// 読む前と読んでいる間に止まった状態が変わったら errStale を返す。
func (p *pane) virtualTextsFor(cfg vtSettings) (want map[ext.BufferID]vtSent, x *sessionSnap, err error) {
	if !cfg.enabled {
		return nil, nil, nil
	}
	ss, cur, _ := p.d.snapshot()
	if anyStopping(ss) {
		return nil, nil, errStale
	}
	c, ok := current(ss, cur)
	if !ok || !c.halted || !c.hasFrame || c.at == nil {
		return nil, nil, nil
	}
	p.d.mu.Lock()
	buf, opened := p.d.buffers[c.at.path]
	p.d.mu.Unlock()
	if !opened {
		// 開いていないファイルのフレーム。開いたとき（bufferOpened）に出す。
		return nil, nil, nil
	}
	vars, err := p.frameVariables(c)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := p.ctx()
	defer cancel()
	b, err := p.d.srv.WaitBuffer(ctx, buf.id, buf.version)
	if err != nil {
		p.d.logf("virtual text: wait buffer %d: %v", buf.id, err)
		return nil, nil, nil
	}
	comment := ""
	if cfg.commented {
		comment = p.commentString(ctx, b.ID())
	}
	lines := make([]string, b.LineCount())
	for i := range lines {
		lines[i] = b.Line(i)
	}
	var changed map[string]bool
	if cfg.highlightChanged {
		changed = p.vt.changes(c, vars)
	}
	texts := placeVirtualTexts(lines, c.at.line, vars, cfg, comment, changed)
	return map[ext.BufferID]vtSent{b.ID(): {version: b.Version(), texts: texts}}, &c, nil
}

// frameVariables は、止まっているセッション x の選んでいるフレームの、expensive でないスコープの最上位の変数を、名前の重ならない列にする。
// スコープの区画と同じ写し（p.scopes）から読み、無ければ読む。同じ名前が複数のスコープにあれば、先のスコープの変数を採るが、
// 後のスコープが presentationHint が locals のスコープで、先のスコープがそうでなければ、後のものを採る（nvim-dap-virtual-text の
// variables_from_scopes の「prefer "locals"」）。識別子でない名前（dlv の (x)、debugpy の special variables、vscode-js-debug の
// [[Prototype]]）と this と、モジュールの値（vtSkipValue）は除く。読めなかったスコープは飛ばす。世代が進んだら errStale を返す。
func (p *pane) frameVariables(x sessionSnap) ([]vtVar, error) {
	sv := &p.scopes
	if err := p.loadScopes(x); errors.Is(err, errStale) {
		return nil, err
	} else if err != nil {
		p.d.logf("virtual text: scopes: %v", err)
		return nil, nil
	}
	var out []vtVar
	at := map[string]int{}
	for _, sc := range sv.scopes {
		if sc.Expensive || sc.VariablesReference <= 0 {
			continue
		}
		vs, err := p.varsOf(&sv.varTree, x, varsKey{ref: sc.VariablesReference})
		if errors.Is(err, errStale) {
			sv.vars = nil
			return nil, err
		}
		if err != nil {
			continue
		}
		locals := sc.PresentationHint == "locals"
		for _, v := range vs {
			if !isIdentifier(v.Name) || v.Name == "this" || vtSkipValue(v.Value) {
				continue
			}
			value := v.Value
			// スコープの区画で書き換えた値（setVariable と setExpression の応答の値）を、同じ止まった状態の間は使う（paneviews.go の setValue）。
			if val, ok := sv.setValues[oneLine(sc.Name)+"\x1f"+oneLine(v.Name)]; ok && sv.setFor == x.s && sv.setGen == x.gen {
				value = val
			}
			out = appendVTVar(out, at, vtVar{name: v.Name, value: value, locals: locals})
		}
	}
	return out, nil
}

// appendVTVar は、変数 nv を out に足す。at は、out の名前から添字への対応である。同じ名前が既にあれば、前のものが locals のスコープの
// 変数でなく、nv がそうなら置き換え、ほかは足さない。
func appendVTVar(out []vtVar, at map[string]int, nv vtVar) []vtVar {
	if i, dup := at[nv.name]; dup {
		if nv.locals && !out[i].locals {
			out[i] = nv
		}
		return out
	}
	at[nv.name] = len(out)
	return append(out, nv)
}

// vtSkipValue は、値 v を行末に出さないか（モジュールか）を返す。nvim-dap-virtual-text の既定の filter_references_pattern（'<module'）と同じ。
func vtSkipValue(v string) bool {
	return strings.Contains(v, "<module")
}

// isIdentRune は、識別子に使える文字か（文字、数字、_、$）を返す。
func isIdentRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isIdentifier は、s が識別子（数字で始まらない、識別子の文字だけの列）かを返す。
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !isIdentRune(r) || (i == 0 && unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

// changes は、x の選んでいるフレームの変数 vars の値が、同じフレーム（vtFrameKey）の前の停止から変わったかを返す。前の停止に無かった
// 変数は、変わったことにしない（nvim-dap-virtual-text の highlight_new_as_changed の既定の false）。同じ停止の間に出し直したとき
// （フレームの選び直し、値の書き換え）は、前の停止と比べ続ける。
func (st *vtState) changes(x sessionSnap, vars []vtVar) map[string]bool {
	values := make(map[string]string, len(vars))
	for _, v := range vars {
		values[v.name] = v.value
	}
	key := vtFrameKey{s: x.s, path: x.at.path, name: x.at.name}
	switch {
	case key != st.key:
		st.key, st.stop, st.base = key, x.shownAt, nil
	case !x.shownAt.Equal(st.stop):
		st.stop, st.base = x.shownAt, st.cur
	}
	st.cur = values
	changed := map[string]bool{}
	for name, v := range values {
		if old, ok := st.base[name]; ok && old != v {
			changed[name] = true
		}
	}
	return changed
}

// commentString は、バッファ id の設定 comment.string（`// %s` の形）を返す。option/get で読み、バッファを開き直すまで覚える。
// 読めなければ空を返す（包まない）。
func (p *pane) commentString(ctx context.Context, id ext.BufferID) string {
	if cs, ok := p.vt.comments[id]; ok {
		return cs
	}
	res, err := p.d.srv.GetOptions(ctx, ext.OptionGetParams{Buffer: id, Names: []string{"comment.string"}})
	if err != nil {
		p.d.logf("virtual text: option/get comment.string: %v", err)
		return ""
	}
	var cs string
	if raw, ok := res.Values["comment.string"]; ok {
		_ = json.Unmarshal(raw, &cs)
	}
	p.vt.comments[id] = cs
	return cs
}

// sendVirtualTexts は、want のバッファへ仮想テキストの一覧を送り、want に無いバッファの前に送ったものを空の一覧で消す。
// 前に送ったものと同じなら送らない。一覧が本体の受信の上限を超えて送れなければ、空の一覧を送ってログに書く（LSP のインレイヒントと同じ）。
func (p *pane) sendVirtualTexts(want map[ext.BufferID]vtSent) {
	for id, prev := range p.vt.shown {
		if w, ok := want[id]; ok && len(w.texts) > 0 {
			continue
		}
		p.setVirtualTexts(id, prev.version, nil)
		delete(p.vt.shown, id)
	}
	for id, w := range want {
		if len(w.texts) == 0 {
			continue
		}
		if prev, ok := p.vt.shown[id]; ok && prev.version == w.version && slices.EqualFunc(prev.texts, w.texts, sameVirtualText) {
			continue
		}
		if p.setVirtualTexts(id, w.version, w.texts) {
			p.vt.shown[id] = w
		} else {
			delete(p.vt.shown, id)
		}
	}
}

// setVirtualTexts は、virtualText/set を送り、空でない一覧を送れたかを返す。
func (p *pane) setVirtualTexts(id ext.BufferID, v ext.Version, texts []ext.VirtualText) bool {
	if texts == nil {
		texts = []ext.VirtualText{}
	}
	err := p.d.srv.SetVirtualTexts(ext.VirtualTextSetParams{Buffer: id, Version: v, VirtualTexts: texts})
	if errors.Is(err, server.ErrMessageTooLarge) {
		p.d.logf("virtual text: %d values for buffer %d: %v; cleared", len(texts), id, err)
		texts = []ext.VirtualText{}
		err = p.d.srv.SetVirtualTexts(ext.VirtualTextSetParams{Buffer: id, Version: v, VirtualTexts: texts})
	}
	if err != nil {
		p.d.logf("virtualText/set: %v", err)
		return false
	}
	return len(texts) > 0
}

// sameVirtualText は、2 つの仮想テキストが同じかを返す。
func sameVirtualText(a, b ext.VirtualText) bool {
	return a.Position == b.Position && a.Placement == b.Placement && slices.EqualFunc(a.Chunks, b.Chunks, func(x, y ext.VirtualTextChunk) bool {
		return x.Text == y.Text && x.Style == y.Style && x.Attr == nil && y.Attr == nil
	})
}

// 行末の変数の値の色の名前（7.1）。nvim-dap-virtual-text の NvimDapVirtualText（Comment へのリンク）と、値の変わった変数の
// NvimDapVirtualTextChanged（DiagnosticVirtualTextWarn へのリンク）に当たる。
const (
	styleVTValue   = "comment"
	styleVTChanged = "diagnostic.warning"
)

// placeVirtualTexts は、バッファの行 lines の、止まった行 at（選んでいるフレームの行）を含む関数の範囲（frameRegion）で、変数 vars の名前が
// 定義の形で最初に現れる位置（firstOccurrences）に、値の仮想テキストを作る（nvim-dap-virtual-text の set_virtual_text の、名前の一致による代わり）。
//
//   - eol: 文字列は「名前 = 値」。位置はその行の、名前の現れた位置のバイト（本体は同じ行の eol をバイトの順に 1 列ずつ空けて並べる）。
//     同じ行に複数あれば、最後のもの以外の後ろに , を足す（nvim-dap-virtual-text の separator）
//   - inline: 文字列は「 = 値」。位置は名前の直後
//
// 値の空白の並び（改行を含む）は空白 1 つにし（nvim-dap-virtual-text の display_callback の既定）、設定の maxLength と maxLabelRunes の
// 小さい方の文字数で切って … を足す。comment が空でなければ、その %s を文字列に置き換えたもので包む（commented）。
// 色は comment、changed の変数は diagnostic.warning である。
//
// req: FR-296
func placeVirtualTexts(lines []string, at int, vars []vtVar, cfg vtSettings, comment string, changed map[string]bool) []ext.VirtualText {
	if len(lines) == 0 || len(vars) == 0 {
		return nil
	}
	names := make(map[string]bool, len(vars))
	for _, v := range vars {
		names[v.name] = true
	}
	occ := firstOccurrences(lines, at, names)
	type placed struct {
		o occurrence
		v vtVar
	}
	var ps []placed
	for _, v := range vars {
		if o, ok := occ[v.name]; ok {
			ps = append(ps, placed{o, v})
		}
	}
	slices.SortStableFunc(ps, func(a, b placed) int {
		if a.o.line != b.o.line {
			return a.o.line - b.o.line
		}
		return a.o.start - b.o.start
	})
	limit := maxLabelRunes
	if cfg.maxLength > 0 {
		limit = min(limit, cfg.maxLength)
	}
	eol := cfg.placement == ext.VirtualTextEOL
	out := make([]ext.VirtualText, 0, len(ps))
	for i, x := range ps {
		value := collapseSpace(x.v.value)
		if r := []rune(value); len(r) > limit {
			value = string(r[:limit]) + "…"
		}
		text := " = " + value
		pos := ext.Position{Line: x.o.line, Byte: x.o.end}
		if eol {
			text = x.v.name + text
			pos.Byte = x.o.start
		}
		if comment != "" && strings.Contains(comment, "%s") {
			text = strings.ReplaceAll(comment, "%s", text)
		}
		if eol && i+1 < len(ps) && ps[i+1].o.line == x.o.line {
			text += ","
		}
		style := styleVTValue
		if changed[x.v.name] {
			style = styleVTChanged
		}
		out = append(out, ext.VirtualText{Position: pos, Placement: cfg.placement, Chunks: []ext.VirtualTextChunk{{Text: text, Style: style}}})
	}
	return out
}

// spaceRun は、空白の並び（Lua のパターンの %s+ に当たる）である。
var spaceRun = regexp.MustCompile(`\s+`)

// collapseSpace は、空白の並び（改行とタブ文字を含む）を空白 1 つにする。
func collapseSpace(s string) string { return spaceRun.ReplaceAllString(s, " ") }

// occurrence は、識別子の現れた位置（行と、その行のバイトの範囲）である。
type occurrence struct {
	line, start, end int
}

// firstOccurrences は、止まった行 at を含む関数の範囲（frameRegion）で、names の名前が定義の形（isDefinition）で最初に現れる位置を返す
// （nvim-dap-virtual-text は、tree-sitter の locals のクエリの定義だけに出す）。
// 範囲の中の、at を含まない入れ子の関数（見出しの行と本体）は飛ばす（nvim-dap-virtual-text は、止まった行を含まないスコープの中の定義を
// 出さない）。コメントの行（vtCommentLine）と、直前が . の名前（メンバーの参照）は数えない。
func firstOccurrences(lines []string, at int, names map[string]bool) map[string]occurrence {
	out := map[string]occurrence{}
	at = max(0, min(at, len(lines)-1))
	start, end, header := frameRegion(lines, at)
	skipping, skipIndent := false, 0
	for i := start; i <= end && len(out) < len(names); i++ {
		line := lines[i]
		if isBlank(line) {
			continue
		}
		ind := indentWidth(line)
		if skipping && (ind > skipIndent || (ind == skipIndent && closesBlock(line))) {
			continue
		}
		skipping = false
		if i != header && looksLikeFuncHeader(line) && !blockContains(lines, i, at) {
			skipping, skipIndent = true, ind
			continue
		}
		if vtCommentLine(line) {
			continue
		}
		identifiers(line, func(s, e int) {
			name := line[s:e]
			if !names[name] {
				return
			}
			if _, seen := out[name]; !seen && (i == header || isDefinition(line, s, e)) {
				out[name] = occurrence{line: i, start: s, end: e}
			}
		})
	}
	return out
}

// defKeywords は、直後の名前を定義する語である（Go の var、JavaScript の let、const、var、Python の for、as、import、global、nonlocal など）。
var defKeywords = []string{"var", "let", "const", "for", "as", "import", "global", "nonlocal"}

// isDefinition は、line の s から e までの名前が、定義の形で現れているか（名前の一致のための見積もり。仮）を返す。
//
//   - 直前の語が defKeywords のどれか（var x、let x、for i in、import os、as f）
//   - 行が for で始まり、in、of、;、:=、代入の = のどれよりも左にある（for a, b in …、for i := 0; i < n; …）。右の名前（n）は定義にしない
//   - 閉じていない ( の中でなく（呼び出しのキーワード引数 f(x=1) を除く）、直後が := か、= の 1 文字の代入（==、=> でない）か、
//     行の先頭の名前の型の注記（x: int = 1）
//   - 閉じていない ( の中でなく、行の最初の := か代入の = より左が、名前、空白、カンマ、括弧だけで、その中にある
//     （a, b = …、x, err := …、const { a, b } = …）
//
// 関数の見出しの行の名前（引数）は、呼ぶ側（firstOccurrences）で定義として扱う。
func isDefinition(line string, s, e int) bool {
	before := strings.TrimRight(line[:s], " \t")
	w := len(before)
	for w > 0 {
		r, n := utf8.DecodeLastRuneInString(before[:w])
		if !isIdentRune(r) {
			break
		}
		w -= n
	}
	if slices.Contains(defKeywords, before[w:]) {
		return true
	}
	t := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(t, "for ") || strings.HasPrefix(t, "for(") {
		end := len(line)
		for _, stop := range []string{" in ", " of ", ";"} {
			if i := strings.Index(line, stop); i >= 0 {
				end = min(end, i)
			}
		}
		if k := assignIndex(line); k >= 0 {
			end = min(end, k)
		}
		return e <= end
	}
	if parenDepth(line[:s]) > 0 {
		// 括弧の中（呼び出しのキーワード引数の f(x=1) など）は定義にしない。
		return false
	}
	after := strings.TrimLeft(line[e:], " \t")
	switch {
	case strings.HasPrefix(after, ":="):
		return true
	case strings.HasPrefix(after, "=") && !strings.HasPrefix(after, "==") && !strings.HasPrefix(after, "=>"):
		return true
	case strings.HasPrefix(after, ":") && strings.TrimSpace(line[:s]) == "" && strings.Contains(after, "="):
		return true
	}
	k := assignIndex(line)
	return k > e && onlyNames(line[:k])
}

// parenDepth は、s の中で閉じていない ( の数を返す。
func parenDepth(s string) int {
	return strings.Count(s, "(") - strings.Count(s, ")")
}

// assignIndex は、line の最初の := か、代入の = （==、=>、<=、>=、!=、+= などでない 1 文字の =）の位置を返す。無ければ -1。
func assignIndex(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] != '=' {
			continue
		}
		if i+1 < len(line) && (line[i+1] == '=' || line[i+1] == '>') {
			i++
			continue
		}
		if i > 0 && line[i-1] == ':' {
			return i - 1
		}
		if i > 0 && strings.IndexByte("=!<>+-*/%&|^~", line[i-1]) >= 0 {
			continue
		}
		return i
	}
	return -1
}

// onlyNames は、s が名前、空白、カンマ、括弧、* と、var、let、const だけからなるか（代入の左辺が名前の並びか）を返す。
func onlyNames(s string) bool {
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(" \t,()[]{}*", r) }) {
		if !isIdentifier(w) {
			return false
		}
	}
	return strings.TrimSpace(s) != ""
}

// identifiers は、line の識別子ごとに、そのバイトの範囲で f を呼ぶ。数字で始まる並びと、直前が . のもの（メンバーの参照）は飛ばす。
func identifiers(line string, f func(start, end int)) {
	prev := rune(0)
	for i := 0; i < len(line); {
		r, n := utf8.DecodeRuneInString(line[i:])
		if !isIdentRune(r) {
			prev = r
			i += n
			continue
		}
		s := i
		for i < len(line) {
			r2, n2 := utf8.DecodeRuneInString(line[i:])
			if !isIdentRune(r2) {
				break
			}
			i += n2
		}
		if !unicode.IsDigit(r) && prev != '.' {
			f(s, i)
		}
		prev = 'a'
	}
}

// frameRegion は、行 at を含む関数の範囲（start から end までの行）と、その見出しの行を返す。at から上へ、字下げの浅くなる行
// （at を囲む行）をたどり、関数の見出しの形の行（looksLikeFuncHeader）があれば、最も内側のものを見出しにし、その行から、
// 見出しの字下げ以下の次の行の手前（閉じ括弧で始まる行なら、その行）までにする。見つからなければ（関数の外で止まった）、ファイルの全体にし、
// 見出しは -1 にする。
func frameRegion(lines []string, at int) (start, end, header int) {
	limit := -1
	header = -1
	for i := at; i >= 0; i-- {
		if isBlank(lines[i]) {
			continue
		}
		ind := indentWidth(lines[i])
		if limit >= 0 && ind >= limit {
			continue
		}
		limit = ind
		if looksLikeFuncHeader(lines[i]) {
			header = i
			break
		}
		if ind == 0 && i != at {
			break
		}
	}
	if header < 0 {
		return 0, len(lines) - 1, -1
	}
	hIndent := indentWidth(lines[header])
	end = len(lines) - 1
	for i := max(at, header) + 1; i < len(lines); i++ {
		if isBlank(lines[i]) || indentWidth(lines[i]) > hIndent {
			continue
		}
		end = i - 1
		if closesBlock(lines[i]) {
			end = i
		}
		break
	}
	return header, end, header
}

// blockContains は、行 h を見出しにする本体が行 at を含むか（at が h か、h より下で、間の行がどれも h より深く字下げされているか）を返す。
func blockContains(lines []string, h, at int) bool {
	if at == h {
		return true
	}
	if at < h {
		return false
	}
	hIndent := indentWidth(lines[h])
	for i := h + 1; i <= at; i++ {
		if isBlank(lines[i]) {
			continue
		}
		if indentWidth(lines[i]) <= hIndent {
			return false
		}
	}
	return true
}

// funcKeyword は、関数の見出しの行に現れる語（Go の func、Python の def、JavaScript の function、Rust の fn）か、矢印の関数の => である。
var funcKeyword = regexp.MustCompile(`(^|[^\p{L}\p{N}_$])(func|def|function|fn)([^\p{L}\p{N}_$]|$)|=>`)

// methodHeader は、JavaScript と TypeScript のクラスのメソッドの見出し（名前と括弧と { だけの行）である。
var methodHeader = regexp.MustCompile(`^(?:(?:async|static|get|set|public|private|protected)\s+)*([\p{L}_$][\p{L}\p{N}_$]*)\s*\(.*\)\s*(?::\s*[^{]*)?\{$`)

// controlWords は、methodHeader の形に合うが関数でない文の語である。
var controlWords = []string{"if", "for", "while", "switch", "catch", "with", "else", "return"}

// looksLikeFuncHeader は、line が関数の本体を開く見出しの行の形か（行の終わりが { か : で、関数の語を含むか、メソッドの形か）を返す。
// 名前の一致のための見積もり（仮）で、構文は見ない。
func looksLikeFuncHeader(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasSuffix(t, "{") && !strings.HasSuffix(t, ":") {
		return false
	}
	if funcKeyword.MatchString(t) {
		return true
	}
	if m := methodHeader.FindStringSubmatch(t); m != nil {
		return !slices.Contains(controlWords, m[1])
	}
	return false
}

// closesBlock は、line が閉じ括弧で始まるか（ブロックの終わりの行か）を返す。
func closesBlock(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "}") || strings.HasPrefix(t, ")") || strings.HasPrefix(t, "]")
}

// vtCommentLine は、line がコメントだけの行（//、#、/*、*、-- で始まる行）かを返す。名前の一致で、コメントの中の語を数えないための見積もり（仮）。
func vtCommentLine(line string) bool {
	t := strings.TrimSpace(line)
	for _, p := range []string{"//", "#", "/*", "*/", "--"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return t == "*" || strings.HasPrefix(t, "* ")
}

// isBlank は、line が空白だけかを返す。
func isBlank(line string) bool { return strings.TrimSpace(line) == "" }

// indentWidth は、行頭の空白の幅である（タブ文字は次の 8 の倍数まで進める）。
func indentWidth(line string) int {
	w := 0
	for _, r := range line {
		switch r {
		case ' ':
			w++
		case '\t':
			w = (w/8 + 1) * 8
		default:
			return w
		}
	}
	return w
}
