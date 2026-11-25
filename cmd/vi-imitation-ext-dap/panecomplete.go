package main

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
)

// この節は、REPL の区画の入力の行の補完（FR-310）である（基本設計 6.4 の「#430 の REPL の補完」）。
//
// REPL の区画の console の部品を、補完を引き受けると宣言して開き（ext.ViewComponent.Complete。openPane）、本体の console/complete
// （FR-297）の問い合わせに、今のセッションの選んでいるフレームに対するアダプターの completions の候補で答える。
// アダプターへ送る text はカーソルまでの入力の行で、column はその末尾（nvim-dap の REPL の omnifunc と、オーナーの nvim の
// blink_dap.lua と同じ）である。候補の置き換える範囲は VS Code の REPL（getReplCompletionRange）に合わせて start と length から求め、
// どちらも無い候補は、nvim-dap の get_start と blink.cmp のキーワードの範囲に合わせてカーソルの前の語から求める（replCompletionItems）。
//
// 候補を返さない（ok を false にする）のは、ペインの REPL の区画でない、セッションが無い、止まっていない、アダプターが
// supportsCompletionsRequest を示さない、completions が失敗した、問い合わせの後に止まった状態が変わった、より新しい問い合わせが来た、
// のどれかのときである。誤りの応答は返さない（本体は ok が false の答えで一覧を出さないだけ）。

// replCompleter は、REPL の補完の問い合わせの状態である。どの goroutine から触ってもよい。
type replCompleter struct {
	// ticket は、届いた問い合わせに届いた順に振る番号の最後のものである。本体の seq はペインを開き直すと 1 に戻るので、自分で振る。
	ticket atomic.Uint64
	// sem は、アダプターへ送ってある completions を 1 つに限る（容量 1）。持つ間に届いた問い合わせは待ち、待つ間に新しい問い合わせが
	// 来れば送らずに答える。打つたびに completions をアダプターに積むと、その後ろにステップ実行の request が並んで遅れるためである
	// （vscode-js-debug は送ってある request の応答を返してから次に応える。基本設計 6.4 の「#424 のウォッチ式、REPL、例外」）。
	sem chan struct{}
}

func newReplCompleter() replCompleter {
	return replCompleter{sem: make(chan struct{}, 1)}
}

// completionPollInterval は、止まった位置を出し終えるのと、続けて打っている間の待ち（runningHold）が明けるのを待つ間に、状態を見直す間隔である（仮）。
const completionPollInterval = 10 * time.Millisecond

// completionsArguments は、completions の引数（DAP の仕様の CompletionsArguments）である。
// Column は Text の中のカーソルの位置で、UTF-16 の符号単位で数える（columnsStartAt1 なら 1 から）。Line は、入力の行が 1 行なので
// 最初の行（linesStartAt1 なら 1）にする。VS Code は position.lineNumber を送り、vscode-js-debug は line が 1 のときだけ cd の後の
// 実行コンテキストの候補を返す（PR #431 のレビューの推奨 2）。
type completionsArguments struct {
	FrameID *int   `json:"frameId,omitempty"`
	Text    string `json:"text"`
	Column  int    `json:"column"`
	Line    int    `json:"line"`
}

// completionsResponse は、completions の response の body（同仕様の CompletionsResponse）である。
type completionsResponse struct {
	Targets []completionItem `json:"targets"`
}

// completionItem は、同仕様の CompletionItem のうち、使う項目である。selectionStart、selectionLength、detail は本体へ写せないので読まない
// （本体はカーソルを入れた文字列の後ろに置き、一覧に詳しい説明の欄を持たない。基本設計 6.4 の capabilities の表の後の段落）。
// selectionStart を持つ候補（vscode-js-debug が配列の後ろに返す [index]）も、文字列のまま入れる。VS Code は選んだ部分を
// スニペットの置き場にして入れるが、nvim-dap の completions_to_items とオーナーの nvim の blink_dap.lua も selectionStart を読まず、
// 文字列のまま入れる（PR #431 のレビューの推奨 3。利用者は index を打ち直す）。
type completionItem struct {
	Label    string `json:"label"`
	Text     string `json:"text,omitempty"`
	SortText string `json:"sortText,omitempty"`
	Type     string `json:"type,omitempty"`
	Start    *int   `json:"start,omitempty"`
	Length   *int   `json:"length,omitempty"`
}

// onConsoleComplete は、console/complete のハンドラーである（server.ConsoleCompleteAsync で登録する）。ext/server のメッセージを処理する
// goroutine で呼ばれるので、アダプターの応答を待たずに戻り、別の goroutine で答える（dev-cycle 2.2）。
func (d *debugger) onConsoleComplete(ctx context.Context, p *ext.ConsoleCompleteParams, reply func(ext.ConsoleCompleteResult, error)) {
	d.pane.complete(ctx, p, reply)
}

// complete は、REPL の区画の入力の行への問い合わせ p に、別の goroutine で答える。ペインの REPL の区画でない問い合わせと、
// カーソルが入力の行の外か文字の途中を指す問い合わせには、すぐに ok が false の答えを返す。
func (p *pane) complete(ctx context.Context, prm *ext.ConsoleCompleteParams, reply func(ext.ConsoleCompleteResult, error)) {
	if prm.Child != secRepl || prm.View == 0 || uint64(prm.View) != p.view.Load() ||
		prm.Cursor < 0 || prm.Cursor > len(prm.Text) || !utf8.ValidString(prm.Text[:prm.Cursor]) {
		reply(ext.ConsoleCompleteResult{}, nil)
		return
	}
	t := p.comp.ticket.Add(1)
	prefix := prm.Text[:prm.Cursor]
	go func() { reply(p.completeRepl(ctx, t, prefix)) }()
}

// completeRepl は、届いた順の番号が ticket の問い合わせに、カーソルまでの入力の行 prefix への候補を求める。
// ctx は本体の問い合わせの ctx で、本体が $/cancel で取り消すと終わる。
//
// 次の順に行う。
//
//  1. アダプターへ送ってある completions の応答を待つ（replCompleter.sem）。待つ間に ctx が終わったら ctx.Err() を返す
//  2. 待った後に、より新しい問い合わせが届いていれば送らない（古い問い合わせ。本体も最後に送った seq でない答えを捨てる）
//  3. 今のセッションが止まっていて、止まった位置を出し終え、続けて打っている間の待ち（runningHold）が明けるまで待つ（PR #425 の取り決め。
//     止まった位置の stackTrace と、続けて打つステップ実行より先に completions をアダプターへ積まない）。待つ間に動き出したか、
//     続行かステップ実行を送ったか、今のセッションが替わったら送らない
//  4. 選んでいるフレームを付けて completions を送る。応答は本体の ctx が終わっても待ち（アダプターに積んである request を 1 つに保つ）、
//     その間に止まった状態が変わった（世代が進んだ）か、新しい問い合わせが届いていれば、候補を捨てる
//
// req: FR-310
func (p *pane) completeRepl(ctx context.Context, ticket uint64, prefix string) (ext.ConsoleCompleteResult, error) {
	select {
	case p.comp.sem <- struct{}{}:
	case <-ctx.Done():
		return ext.ConsoleCompleteResult{}, ctx.Err()
	}
	defer func() { <-p.comp.sem }()
	wait, cancel := p.ctx()
	defer cancel()
	var x sessionSnap
	var first *session
	var resumed time.Time
	for {
		if p.comp.ticket.Load() != ticket {
			return ext.ConsoleCompleteResult{}, nil
		}
		ss, cur, _ := p.d.snapshot()
		var ok bool
		x, ok = current(ss, cur)
		if !ok || !x.configured || !x.halted || !x.s.supports(func(c *capabilities) bool { return c.SupportsCompletionsRequest }) {
			return ext.ConsoleCompleteResult{}, nil
		}
		// 待つ間に今のセッションが替わったか、続行かステップ実行を送ったら、送らない。見直しの間に動いて止まり直すと halted だけでは
		// 気づけず、続けて打つステップ実行の間に completions を積むことになる（PR #431 のレビューの必須 1 のテストで見つけた）。
		r := p.d.lastResumed(x.s)
		if first == nil {
			first, resumed = x.s, r
		} else if x.s != first || !r.Equal(resumed) {
			return ext.ConsoleCompleteResult{}, nil
		}
		if !x.stopping && p.d.runningHold() <= 0 {
			break
		}
		select {
		case <-time.After(completionPollInterval):
		case <-ctx.Done():
			return ext.ConsoleCompleteResult{}, ctx.Err()
		case <-wait.Done():
			return ext.ConsoleCompleteResult{}, nil
		}
	}
	args := completionsArguments{Text: prefix, Column: utf16Len(prefix), Line: toDAPLine(0, linesStartAt1)}
	if linesStartAt1 {
		args.Column++
	}
	if x.hasFrame {
		f := x.frame
		args.FrameID = &f
	}
	var res completionsResponse
	err := x.s.callGen(wait, x.gen, "completions", args, &res)
	switch {
	case errors.Is(err, errStale):
		return ext.ConsoleCompleteResult{}, nil
	case err != nil:
		p.d.logf("completions: %v", err)
		return ext.ConsoleCompleteResult{}, nil
	case p.comp.ticket.Load() != ticket:
		return ext.ConsoleCompleteResult{}, nil
	}
	return ext.ConsoleCompleteResult{OK: true, Items: replCompletionItems(prefix, res.Targets)}, nil
}

// lastResumed は、セッション s に最後に続行かステップ実行を送った時刻（session.resumedAt）を返す。
func (d *debugger) lastResumed(s *session) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return s.resumedAt
}

// replCompletionItems は、カーソルまでの入力の行 prefix への completions の候補 targets を、本体の候補（ext.ConsoleCompletionItem）にする。
//
//   - 並びは sortText（無ければ label）の昇順で、同じものは届いた順（nvim-dap の completions_to_items。VS Code は sortText で並べる）
//   - label の無い候補は捨てる（VS Code の REPL と同じ）。label と type は oneLine で 1 行にする（本体の Validate は改行を断る）
//   - 入れる文字列は text、無ければ label（切る前のもの）。種類は type
//   - 置き換える範囲の終わりはカーソル（prefix の末尾）である。始まりは、start があれば、それを prefix の中の 0 から数えた UTF-16 の
//     符号単位の位置として読む（VS Code の getReplCompletionRange。columnsStartAt1 に依らない。debugpy と nvim-dap も 0 から数える）。
//     start がカーソルより後ろなら候補を捨てる（VS Code と同じ）。start が無く length があれば、カーソルから length の符号単位だけ前にする
//     （同じ関数）。どちらも無ければ guessCompletionStart で決める
//   - 入れる文字列と範囲の始まりが前の候補と同じ候補は捨てる（nvim-dap の completions_to_items が complete() の dup を 0 にして、
//     入れる文字列の同じ候補を足さないのに当たる）。debugpy 1.8.22 は、obj. に __class__ と __dict__ を 2 つずつ返す（#430 の実物の確かめ）
func replCompletionItems(prefix string, targets []completionItem) []ext.ConsoleCompletionItem {
	targets = slices.Clone(targets)
	slices.SortStableFunc(targets, func(a, b completionItem) int {
		return cmp.Compare(cmp.Or(a.SortText, a.Label), cmp.Or(b.SortText, b.Label))
	})
	units := utf16Len(prefix)
	items := make([]ext.ConsoleCompletionItem, 0, len(targets))
	type dupKey struct {
		text  string
		start int
	}
	seen := make(map[dupKey]bool, len(targets))
	for _, t := range targets {
		if t.Label == "" {
			continue
		}
		// 一覧に出す label は、ほかの区画の項目と同じく改行を空白にして長すぎるものを切る（oneLine）。入れる文字列は切らない。
		it := ext.ConsoleCompletionItem{Label: oneLine(t.Label), Text: cmp.Or(t.Text, t.Label), Kind: oneLine(t.Type), End: len(prefix)}
		switch {
		case t.Start != nil:
			if *t.Start > units {
				continue
			}
			it.Start = utf16ColumnToByte(prefix, *t.Start, false)
		case t.Length != nil && *t.Length > 0:
			it.Start = utf16ColumnToByte(prefix, max(units-*t.Length, 0), false)
		default:
			it.Start = guessCompletionStart(prefix, it.Insert())
		}
		k := dupKey{it.Text, it.Start}
		if seen[k] {
			continue
		}
		seen[k] = true
		items = append(items, it)
	}
	return items
}

// guessCompletionStart は、start と length の無い候補（入れる文字列 insert）が置き換える範囲の始まりを、カーソルまでの入力の行 prefix の
// 中のバイトの位置で返す。
//
// まずカーソルの前の語（文字、0〜9、_ の並び）の始まりにする（blink.cmp のコミット 78336bc89ee5365633bcf754d93df01678b5c08f の
// lua/blink/cmp/fuzzy/rust/keyword.rs の BACKWARD_REGEX。- は式の演算子なので語に入れない。nvim-dap の get_start の \k*$ に当たる）。
// そこから空白の手前まで 1 文字ずつ前へ戻り、その位置からカーソルまでが insert の先頭と同じなら、最も前のその位置にする
// （nvim-dap の get_start が、候補が入力の行のカーソルまでで始まれば 0 にするのと、blink.cmp の guess_keyword_range が語の前の
// 語でない文字まで候補に含まれれば範囲を延ばすのに当たる）。アダプターが obj.a に obj.alpha を返しても、obj.obj.alpha にしない。
func guessCompletionStart(prefix, insert string) int {
	start := len(prefix)
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(prefix[:start])
		if !unicode.IsLetter(r) && (r < '0' || r > '9') && r != '_' {
			break
		}
		start -= size
	}
	best := start
	for i := start; i > 0 && len(prefix)-i < len(insert); {
		r, size := utf8.DecodeLastRuneInString(prefix[:i])
		if unicode.IsSpace(r) {
			break
		}
		i -= size
		if strings.HasPrefix(insert, prefix[i:]) {
			best = i
		}
	}
	return best
}

// utf16Len は、s を UTF-16 で表したときの符号単位の数である（不正な UTF-8 のバイトは 1 つと数える。utf16ColumnToByte と同じ）。
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}
