package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// インレイヒント（LSP 3.17 の textDocument/inlayHint）を、本体の仮想テキスト（virtualText/set の inline。基本設計 6.1 の「仮想テキスト」）で
// 出す（基本設計 6.3 の「インレイヒント」。FR-255〜FR-257）。
//
// 問い合わせる範囲は文書の全体で、Neovim 0.12.5 の vim.lsp.inlay_hint と同じである（runtime/lua/vim/lsp/inlay_hint.lua の refresh は、
// バッファの最初の行から最後の行までを問い合わせる）。見えている範囲だけを問い合わせる形（window/change を受け取る形）にしなかったのは、
// 本体が編集のたびに仮想テキストの位置をずらすので、見えていない行のヒントも、スクロールしたときに問い合わせを待たずに出せるためである。
//
// 問い合わせる契機は、文書を言語サーバーへ開いたとき（didOpen）、変えたとき（didChange）、言語サーバーが workspace/inlayHint/refresh を
// 送ったとき、利用者が出すことに切り替えたとき（lsp.inlayHints.toggle）である。どれも文書ごとに inlayInterval の間隔で間引く。
// 前の問い合わせから間隔が空いていればすぐに送り、空いていなければ間隔が経つまで待ってから 1 回だけ送る。
// Neovim 0.12.5 の didChange の間引き（_changetracking.lua の next_debounce。前に送ってから debounce_text_changes の既定の 150 ミリ秒が
// 経っていればすぐに送り、経っていなければ残りの時間だけ待つ）と同じ間隔で、Neovim はその didChange のたびに textDocument/inlayHint を送る。
// 1 つの文書について、応答を待っている問い合わせは 1 つまでにする。待っている間に契機が来たら、応答が届いた後にもう一度問い合わせる。
// 遅い言語サーバーに問い合わせが溜まらず、取り消し続けて一度も出ないこともない。
//
// 結果は、問い合わせた版の番号で virtualText/set に送る。その後の編集は、本体が仮想テキストの位置をずらす（診断と同じ）ので、
// 問い合わせた後に利用者が編集していても捨てない（Neovim は版の違う結果を捨てる）。

// inlayInterval は、1 つの文書の textDocument/inlayHint を続けて送るときの間隔の下限である（仮）。
const inlayInterval = 150 * time.Millisecond

// styleInlayHint は、インレイヒントの文字列に付ける色の種類の名前である（internal/ui の StyleInlayHint。FR-258）。
// 本体の表に名前が無ければ（古い本体）、色を付けずに描く。
const styleInlayHint = "inlayHint"

// cmdInlayHintsToggle は、インレイヒントを出すかどうかを切り替えるコマンドの ID である（FR-257）。宣言ファイルの contributes.commands に書く。
const cmdInlayHintsToggle = "lsp.inlayHints.toggle"

// inlay の切り替えの状態（extension.inlayOverride）。ゼロ値は、切り替えていない（設定の inlayHints に従う）ことを表す。
const (
	// inlayOn と inlayOff は、lsp.inlayHints.toggle で、どの言語サーバーでも出す、出さないに切り替えたことを表す。
	inlayOn int32 = iota + 1
	inlayOff
)

// inlayHintParams は、textDocument/inlayHint の params である（LSP 3.17）。
type inlayHintParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Range        lspRange               `json:"range"`
}

// inlayHint は、textDocument/inlayHint の結果の InlayHint のうち、描くのに使う項目である（LSP 3.17）。
//
// kind（1 が型、2 が引数）は読まない。LSP 3.17 は kind を省けるとし、そのときクライアントは妥当な既定に戻るとしている。
// Neovim 0.12.5 も kind で描き分けず、どのヒントも LspInlayHint の色で描くので、それに合わせて 1 つの色の名前（styleInlayHint）で描く。
// textEdits、tooltip、ラベルの部品の location と command は、仮想テキストで扱えない（押せない）ので読まない。
type inlayHint struct {
	Position lspPosition `json:"position"`
	// Label は、string か InlayHintLabelPart の配列である。
	Label json.RawMessage `json:"label"`
	// PaddingLeft と PaddingRight は、ラベルの前と後ろに空白を 1 つずつ足すかどうかである。
	// LSP 3.17 は、空白を足すときはヒントの背景の色でなく文書の背景の色を使うよう求めているので、空白には色を付けない（Neovim と同じ）。
	PaddingLeft  bool `json:"paddingLeft"`
	PaddingRight bool `json:"paddingRight"`
}

// inlayLabel は、InlayHint の label（string か InlayHintLabelPart の配列）を、描く文字列にする。部品の配列は value をつなぐ。
// 読めなければ空を返す。
func inlayLabel(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] != '[' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var parts []struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Value)
	}
	return b.String()
}

// inlayTexts は、textDocument/inlayHint の結果を、問い合わせた版の行 ls の本体の位置の仮想テキストにする。
// 1 つのヒントを 1 つの inline の仮想テキストにし、言語サーバーが返した順に並べる（本体は、同じ位置のものを一覧の順に描く）。
// ラベルが空のヒントは捨てる。位置が仮想の行（lines の doc）を指すときは、最後の行の行末に置く。
//
// req: FR-255
func inlayTexts(ls lines, enc encoding, hints []inlayHint) []ext.VirtualText {
	out := make([]ext.VirtualText, 0, len(hints))
	for _, h := range hints {
		label := inlayLabel(h.Label)
		if label == "" {
			continue
		}
		at, _ := ls.fromLSP(h.Position, enc)
		chunks := make([]ext.VirtualTextChunk, 0, 3)
		if h.PaddingLeft {
			chunks = append(chunks, ext.VirtualTextChunk{Text: " "})
		}
		chunks = append(chunks, ext.VirtualTextChunk{Text: label, Style: styleInlayHint})
		if h.PaddingRight {
			chunks = append(chunks, ext.VirtualTextChunk{Text: " "})
		}
		out = append(out, ext.VirtualText{Position: at, Placement: ext.VirtualTextInline, Chunks: chunks})
	}
	return out
}

// inlayState は、1 つの文書のインレイヒントの問い合わせの状態である（document.inlay）。client.mu で守る。
type inlayState struct {
	// timer は、予約した問い合わせのタイマーである。予約していなければ nil である。
	timer *time.Timer
	// timerGen は、予約するたびに増やす番号である。止め損ねたタイマー（Stop の前に動き始めたもの）を見分ける。
	timerGen uint64
	// last は、最後に問い合わせを送った時刻である。
	last time.Time
	// future は、応答を待っている問い合わせである。待っていなければ nil である。
	future *rpc.Future
	// dirty は、応答を待っている間に問い合わせの契機が来たことを表す。応答が届いたら、もう一度問い合わせる。
	dirty bool
	// seq は、問い合わせを送るたびと、ヒントを消すたび（resetInlay）に増やす番号である。応答は、送ったときの番号が今の番号と同じときだけ使う。
	seq uint64
	// shown は、空でない一覧を本体へ送ったまま、消していないことを表す。
	shown bool
}

// inlayEnabled は、この言語サーバーの文書にインレイヒントを出すかどうかを返す。
// lsp.inlayHints.toggle で切り替えていればその値、切り替えていなければ設定の inlayHints である。mu を持たずに呼べる。
func (c *client) inlayEnabled() bool {
	switch c.x.inlayOverride.Load() {
	case inlayOn:
		return true
	case inlayOff:
		return false
	}
	return c.cfg.inlayHints()
}

// supportsInlay は、言語サーバー p が textDocument/inlayHint に対応するかどうかを返す。
func supportsInlay(p *proc) bool { return provides(p.caps.InlayHintProvider) }

// scheduleInlay は、mu を持った状態で、稼働中の言語サーバー p への文書 d のインレイヒントの問い合わせを予約する。
// 出さない設定か、言語サーバーが対応しなければ何もしない。応答を待っている間なら、応答の後に問い合わせ直す印だけを付ける。
// 前の問い合わせから inlayInterval が経っていればすぐに、経っていなければ経った時点で送る。
//
// req: FR-256
func (c *client) scheduleInlay(p *proc, d *document) {
	if !c.inlayEnabled() || !supportsInlay(p) || !p.sync.OpenClose {
		return
	}
	st := &d.inlay
	if st.future != nil {
		st.dirty = true
		return
	}
	if st.timer != nil {
		return
	}
	st.timerGen++
	gen := st.timerGen
	delay := max(c.inlayInterval-time.Since(st.last), 0)
	st.timer = time.AfterFunc(delay, func() { c.fireInlay(d, gen) })
}

// fireInlay は、予約した時刻に、文書 d の全体の textDocument/inlayHint を送る。タイマーの goroutine で動く。
// 予約を取り消した後に動き始めたタイマー（gen が今の予約でない）、閉じた文書、稼働していない言語サーバーでは何もしない。
//
// req: FR-256
func (c *client) fireInlay(d *document, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := &d.inlay
	if st.timerGen != gen {
		return
	}
	st.timer = nil
	if c.docs[d.buffer] != d {
		return
	}
	p := c.readyProc()
	if p == nil || !c.inlayEnabled() || !supportsInlay(p) || !p.sync.OpenClose {
		return
	}
	cur := d.current()
	last := len(cur.lines) - 1
	end := cur.lines.toLSP(ext.Position{Line: last, Byte: len(cur.lines[last])}, p.enc)
	f, err := p.conn.Go(lspInlayHint, inlayHintParams{TextDocument: textDocumentIdentifier{URI: d.uri}, Range: lspRange{End: end}})
	st.last = time.Now()
	if err != nil {
		c.logf("%s: %s: %v", c.name(), lspInlayHint, err)
		return
	}
	st.seq++
	st.future = f
	r := &request{c: c, future: f, enc: p.enc, buffer: d.buffer, version: cur.version, path: d.path, lines: cur.lines, finalNewline: d.finalNewline}
	go c.awaitInlay(d, r, st.seq)
}

// awaitInlay は、文書 d のインレイヒントの問い合わせ r の応答を待ち、問い合わせた版の番号で virtualText/set を送る。
// 送った後にヒントを消したか（seq が進んだ）、文書を閉じたら、捨てる。
// 待っている間に契機が来ていれば（dirty）、もう一度問い合わせる。
// 取り消し（RequestCancelled）と、言語サーバーが文書の変更で計算をやめた応答（ContentModified）は、ログに書かずに捨てる。
//
// req: FR-255, FR-256
func (c *client) awaitInlay(d *document, r *request, seq uint64) {
	ctx, cancel := c.x.background()
	defer cancel()
	var hints []inlayHint
	err := r.wait(ctx, &hints)
	var vts []ext.VirtualText
	if err == nil {
		vts = inlayTexts(r.lines, r.enc, hints)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	st := &d.inlay
	if st.future != r.future {
		// resetInlay が、この問い合わせを捨てた。
		return
	}
	st.future = nil
	live := c.docs[d.buffer] == d && st.seq == seq
	var rerr *rpc.Error
	switch {
	case err != nil && errors.As(err, &rerr) && (rerr.Code == codeRequestCancelled || rerr.Code == codeContentModified):
	case err != nil:
		c.logf("%s: %s: %v", c.name(), lspInlayHint, err)
	case live && c.inlayEnabled():
		c.sendInlay(d, r.version, vts)
	}
	if live && st.dirty {
		st.dirty = false
		if p := c.readyProc(); p != nil {
			c.scheduleInlay(p, d)
		}
	}
}

// LSP の ErrorCodes のうち、インレイヒントの応答で捨てるもの（LSP 3.17）。
const (
	codeRequestCancelled = -32800
	codeContentModified  = -32801
)

// sendInlay は、mu を持った状態で、文書 d の版 v の仮想テキストの一覧を virtualText/set で送る。
// 一覧が本体の受信の上限を超えて送れなければ、前の一覧が残らないように空の一覧を送る。
func (c *client) sendInlay(d *document, v ext.Version, vts []ext.VirtualText) {
	if vts == nil {
		vts = []ext.VirtualText{}
	}
	err := c.x.srv.SetVirtualTexts(ext.VirtualTextSetParams{Buffer: d.buffer, Version: v, VirtualTexts: vts})
	if errors.Is(err, server.ErrMessageTooLarge) {
		c.logf("%s: %d inlay hints for buffer %d: %v; cleared", c.name(), len(vts), d.buffer, err)
		vts = []ext.VirtualText{}
		err = c.x.srv.SetVirtualTexts(ext.VirtualTextSetParams{Buffer: d.buffer, Version: v, VirtualTexts: vts})
	}
	if err != nil {
		c.logf("virtualText/set: %v", err)
		return
	}
	d.inlay.shown = len(vts) > 0
}

// resetInlay は、mu を持った状態で、文書 d の予約と、応答を待っている問い合わせを取り消す（言語サーバーへ $/cancelRequest を送る）。
// erase が true で、ヒントを出していれば、空の一覧を送って消す。
// 出さないことに切り替えたとき、言語サーバーが終わったとき、文書を言語サーバーから外したときに呼ぶ。
//
// req: FR-257
func (c *client) resetInlay(d *document, erase bool) {
	st := &d.inlay
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.timerGen++
	if st.future != nil {
		_ = st.future.Cancel()
		st.future = nil
	}
	st.dirty = false
	st.seq++
	if erase && st.shown {
		c.sendInlay(d, d.current().version, nil)
	}
}

// refreshInlay は、言語サーバーの workspace/inlayHint/refresh を受けて、この client のすべての文書のインレイヒントを問い合わせ直す。
// 接続の読み取りの goroutine から呼ぶ。予約するだけで待たない。
//
// req: FR-256
func (c *client) refreshInlay() {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.readyProc()
	if p == nil {
		return
	}
	for _, d := range c.docs {
		c.scheduleInlay(p, d)
	}
}

// applyInlay は、出すかどうかを切り替えた後に、この client のすべての文書に当てる。
// 出すなら問い合わせを予約し、出さないなら予約と問い合わせを取り消してヒントを消す。ループから呼ぶ。
//
// req: FR-257
func (c *client) applyInlay() {
	c.mu.Lock()
	defer c.mu.Unlock()
	on := c.inlayEnabled()
	p := c.readyProc()
	for _, d := range c.docs {
		switch {
		case !on:
			c.resetInlay(d, true)
		case p != nil:
			c.scheduleInlay(p, d)
		}
	}
}

// toggleInlayHints は、コマンド lsp.inlayHints.toggle を処理する（FR-257。Neovim の
// vim.lsp.inlay_hint.enable(not vim.lsp.inlay_hint.is_enabled()) に当たる）。
//
// コマンドを呼んだバッファの言語サーバーで出しているなら、どの言語サーバーでも出さないことに、出していないなら出すことに切り替える。
// 言語サーバーの無いバッファで呼んだときは、どれか 1 つの言語サーバーで出していれば出さないことに、どれでも出していなければ出すことに切り替える。
// 切り替えた値は、設定の inlayHints（言語サーバーごとの値も）より優先し、拡張機能が動いている間、後から開く文書にも当てる。
// 切り替えた結果を message/show で知らせる。出すことにしたバッファの言語サーバーがインレイヒントに対応しなければ、そのことを添える。
//
// req: FR-257
func (x *extension) toggleInlayHints(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
	x.mu.Lock()
	cs := make([]*client, 0, len(x.clients))
	for _, c := range x.clients {
		cs = append(cs, c)
	}
	x.mu.Unlock()
	cur := x.byBuffer[p.Buffer]
	shown := false
	if cur != nil {
		shown = cur.inlayEnabled()
	} else {
		for _, c := range cs {
			shown = shown || c.inlayEnabled()
		}
	}
	if shown {
		x.inlayOverride.Store(inlayOff)
	} else {
		x.inlayOverride.Store(inlayOn)
	}
	for _, c := range cs {
		c.applyInlay()
	}
	text := "インレイヒントを出す"
	if shown {
		text = "インレイヒントを出さない"
	} else if cur != nil && !cur.supportsInlayNow() {
		text += fmt.Sprintf("（言語サーバー %s は稼働していないか、インレイヒントに対応していない）", cur.name())
	}
	x.showMessage(ext.MessageInfo, text)
	return ext.CommandExecuteResult{OK: true}, nil
}

// supportsInlayNow は、稼働中の言語サーバーが textDocument/inlayHint に対応するかどうかを返す。稼働していなければ false である。
func (c *client) supportsInlayNow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.readyProc()
	return p != nil && supportsInlay(p)
}
