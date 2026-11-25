package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、REPL の区画（FR-293）である（pane.go）。
//
// 区画は、書き足すテキストと入力の行の組（ext.ViewConsole。FR-274）で、出力の写しは拡張機能に 1 つ（pane.repl）を持ち、
// セッションをまたいで残す（nvim-dap の REPL のバッファと同じ）。入力の行で <CR> を打つと（view/select）、打った式を
// 「> 式」の行で書き足し、今のセッションの選んでいるフレームで evaluate（context は repl）した結果を書き足す。
// 止まっていなければ、フレームを付けずに評価する（FR-293）。評価は打った順に 1 つずつ、ペインの処理とは別の goroutine で行い、
// 応答を待つ間もペインの区画の作り直しを止めない。
//
// デバッグ対象の出力（output の event）は REPL に書かず、出力の区画（FR-291）にだけ書く。nvim-dap-view は、デバッグ対象の端末
// （runInTerminal）を console の区画に、nvim-dap の REPL（output の event と評価）を repl の区画に出す。この拡張機能は runInTerminal を
// 作らず（FR-288）、出力の区画がデバッグ対象の出力を受け持つので、REPL は評価だけにした（基本設計 6.4 の「#424 のウォッチ式、REPL、例外」）。

// replPrompt は、REPL の入力の行の前置きである（nvim-dap の REPL の dap> に当たる）。
const replPrompt = "> "

// maxReplChildren は、構造を持つ結果の子を REPL に書き足す数の上限である（仮。ウォッチ式の区画の 100 個ずつの読み込みと同じ数）。
const maxReplChildren = pageSize

// sessionHalted は、s が動いているセッションで、止まっているかを返す。
func (d *debugger) sessionHalted(s *session) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return s.halted && slices.Contains(d.sessions, s)
}

// replWrite は、REPL の写しに text を書き足す。どの goroutine から呼んでもよい。style は 7.1 の色の名前（空なら色を付けない）である。
func (p *pane) replWrite(text, style string) {
	text = strings.ToValidUTF8(normalizeNewlines(text), "�")
	if err := p.repl.Append(text, style); err != nil {
		p.d.logf("repl: %v", err)
	}
}

// replSubmit は、REPL の入力の行で送った中身 text を評価する（FR-293）。空なら、前に評価した式を評価し直す
// （nvim-dap の REPL の execute と同じ）。入力の履歴は、本体と同じ規則で拡張機能も積み、開き直したときに渡す。
// 前の評価が終わるまで待ってから評価する（書き足す順を打った順にする）。run で呼ぶ。
//
// req: FR-293
func (p *pane) replSubmit(text string) {
	if text == "" {
		text = p.replLast
		if text == "" {
			return
		}
	} else {
		p.replHistory = ext.AppendConsoleHistory(p.replHistory, text)
		p.replLast = text
	}
	// 式は、打った時点の今のセッションと、その停止（世代）とフレームに対して評価する（PR #425 のレビューの推奨 2）。前の評価を待つ間に
	// ステップ実行すると、止まっていた間に打った式が、動いているデバッグ対象で大域として評価されていた。
	ss, cur, _ := p.d.snapshot()
	x, ok := current(ss, cur)
	prev := p.replDone
	done := make(chan struct{})
	p.replDone = done
	go func() {
		defer close(done)
		if prev != nil {
			<-prev
		}
		p.replEval(text, x, ok)
	}()
}

// replEval は、式 text を、打った時点のセッションの写し x（ok が false ならセッションが無かった）に対して評価し、式と結果を REPL に書き足す。
// 打った時点で止まっていて、評価を始める前にその停止が無くなっていれば（世代が進んだ）、評価せずにそのことを書き足す。
// 結果が構造を持てば（variablesReference が 0 でない）、子を 1 段だけ読んで字下げした行で書き足す（nvim-dap の REPL の
// evaluate_handler も、子を先に読んでから木で出す。この区画はテキストなので開閉はできない）。
// 止まっている間に評価を送った後で止まった状態が変わったら（続けた、選び直した）、結果を出さずにそのことを書き足す（FR-305）。
func (p *pane) replEval(text string, x sessionSnap, ok bool) {
	p.replWrite(replPrompt+text+"\n", "keyword")
	if !ok || !x.configured {
		p.replWrite("デバッグしていない\n", "comment")
		return
	}
	if x.halted && x.s.gen.Load() != x.gen {
		msg := "（打った後に、止まり直したかフレームを選び直したので、評価しなかった）\n"
		if !p.d.sessionHalted(x.s) {
			msg = "（止まっていない。打ったときの停止はもう無いので、評価しなかった）\n"
		}
		p.replWrite(msg, "comment")
		return
	}
	args := evaluateArguments{Expression: text, Context: "repl"}
	if x.halted && x.hasFrame {
		f := x.frame
		args.FrameID = &f
	}
	ctx, cancel := p.ctx()
	defer cancel()
	var res evaluateResponse
	var err error
	if x.halted {
		err = x.s.callGen(ctx, x.gen, "evaluate", args, &res)
	} else if c := x.s.getConn(); c != nil {
		err = c.call(ctx, "evaluate", args, &res)
	} else {
		err = errDAPClosed
	}
	switch {
	case errors.Is(err, errStale):
		p.replWrite("（止まった状態が変わったので、結果を出さない）\n", "comment")
		return
	case err != nil:
		// 末尾の改行は除く（vscode-js-debug の誤りの文は改行で終わり、空の行が挟まるため）。
		p.replWrite(strings.TrimRight(adapterMessage(err), "\r\n")+"\n", "diagnostic.error")
		return
	}
	p.replWrite(strings.TrimRight(res.Result, "\r\n")+"\n", "")
	if res.VariablesReference <= 0 || !x.halted {
		return
	}
	vargs := variablesArguments{VariablesReference: res.VariablesReference}
	if res.IndexedVariables > maxReplChildren {
		// 名前の子を読まず、最初の添字の子だけを読む（大きな列をすべて読まない。PR #423 のレビューの推奨 5）。
		vargs.Filter, vargs.Count = "indexed", maxReplChildren
	}
	var vres variablesResponse
	err = x.s.callGen(ctx, x.gen, "variables", vargs, &vres)
	switch {
	case errors.Is(err, errStale):
		return
	case err != nil:
		p.replWrite("  variables に失敗した: "+adapterMessage(err)+"\n", "diagnostic.error")
		return
	}
	var b strings.Builder
	for i, v := range vres.Variables {
		if i == maxReplChildren {
			break
		}
		label, _ := variableLabel(v)
		b.WriteString("  " + label + "\n")
	}
	total := len(vres.Variables)
	if res.IndexedVariables > maxReplChildren {
		total = res.IndexedVariables + res.NamedVariables
	}
	if rest := total - min(len(vres.Variables), maxReplChildren); rest > 0 {
		fmt.Fprintf(&b, "  …（ほかに %d 個。ウォッチ式の区画に足すと、開いて読める）\n", rest)
	}
	p.replWrite(b.String(), "")
}
