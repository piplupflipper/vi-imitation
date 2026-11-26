package input

import (
	"slices"

	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// MaxCount は、回数の上限である。これを超える回数は MaxCount に丸める。
const MaxCount = 999_999_999

type state int

const (
	stNormal   state = iota // 初期状態。回数、レジスター、モーション、オペレーター、操作を待つ
	stRegister              // " の後。レジスターの名前を待つ
	stOperator              // オペレーターの後。回数、モーション、テキストオブジェクト、重ね押しを待つ
	stChar                  // f、r、q、@ などの後。文字の引数を待つ
)

// charFor は、stChar で待つ文字の引数の行き先である。
type charFor int

const (
	charTarget   charFor = iota // Target.Char（f、t と、文字を取るテキストオブジェクト）
	charCommand                 // Command.Char（r と、対象の後に文字を取るオペレーター）
	charCommand2                // Command.Char2（sr の後の文字）
	charRecord                  // Command.Char に入るレジスターの名前（q）
	charPlay                    // Command.Char に入るレジスターの名前（@）
)

// Parser は、キーを 1 個ずつ受け取り、編集コマンドの値を組み立てる状態機械である（基本設計 3 章）。
// ノーマルモードとビジュアルモードのキーを扱う。インサートモードとコマンドラインモードのキーは、呼び出し元が別に扱う。
// ビジュアルモードの読み方は SetVisual を参照。
//
// 入力の文法は次のとおりである。
//
//	[count]["x][count]{operator[count]{motion|text-object|operator の重ね押し}[char]|motion|action}
//	q{reg}、q（記録中）、[count]@{reg}、[count]@@、[count].
//
// f、t、r の後と、TakesChar のオペレーターの対象の後には、文字の引数が 1 つ付く。
// TakesSecondChar の操作（sr）の後には、文字の引数が 2 つ付く。
// 回数を 2 か所に書いた場合は積を回数にする（2d3w は 6 回）。
//
// あるキーの列が登録済みの操作であり、かつより長い登録の接頭辞でもある場合（y と、拡張機能の ys）、
// 次のキーを待ってから決める。次のキーで長い登録に進めなければ、短い方に決めて、残りのキーを続けて解釈する。
// 次のキーが来ないまま決める方法は Flush を参照。
//
// 並行に呼ばれる前提は置かない（基本設計 2.1）。
//
// req: FR-02
type Parser struct {
	km        *Keymap
	recording bool // マクロの記録中か。reset で消さない
	visual    bool // ビジュアルモードか。reset で消さない

	st          state
	seq         []editcmd.Key // 操作を引くために溜めているキーの列
	opKeys      []editcmd.Key // stOperator のときのオペレーターのキーの列
	opTakesChar bool          // オペレーターが対象の後に文字を取り、まだ受け取っていないか
	takesChar2  bool          // 操作が 1 つ目の文字の後に 2 つ目の文字を取り、まだ受け取っていないか
	charFor     charFor

	count int // 終わった回数の積。0 は回数の指定なし
	cur   int // 入力の途中の回数。0 は入力中でない
	cmd   editcmd.Command
}

// NewParser は、km を引く Parser を返す。km に後から登録した操作も、次のキーから引かれる。
func NewParser(km *Keymap) *Parser { return &Parser{km: km} }

// SetRecording は、マクロの記録中かを Parser に知らせる。
// 記録中は、ノーマルモードの初期状態の q を、記録の停止（KindRecordStop）として読む。
// 記録を始める値（KindRecordStart）や止める値を実行した呼び出し元が、その都度呼ぶ。
func (p *Parser) SetRecording(on bool) { p.recording = on }

// SetVisual は、ビジュアルモードかを Parser に知らせる。
// ビジュアルモードでは、MapVisual の登録を引く。オペレーターは選択を対象にするので、対象を待たずに値を完成させる
// （Target.Kind は TargetNone）。テキストオブジェクトは、オペレーターの後と同じく引き、KindMotion の値にする。
// @ と . は、マクロとドットリピートとして先に読まず、キーマップから引く。q も、記録中の停止を除いてキーマップから引く。
// モードを切り替えた呼び出し元が、その都度呼ぶ。解釈の途中の状態は捨てる。
func (p *Parser) SetVisual(on bool) {
	if p.visual != on {
		p.reset()
		p.visual = on
	}
}

// Pending は、解釈の途中であるか（初期状態でないか）を返す。
func (p *Parser) Pending() bool {
	return p.st != stNormal || len(p.seq) > 0 || p.count > 0 || p.cur > 0 || p.cmd.Register != 0
}

// PendingRegister は、解釈の途中の入力が、回数の無い " と名前だけ（"a）であれば、その名前と true を返す。
// 呼び出し側（command.State）は、この間に打ったキーを拡張機能のポップアップのキーに当てるかを決めるのに使う（FR-315）。
// 名前は検査していない（stRegister と同じく、command が確かめる）。
func (p *Parser) PendingRegister() (rune, bool) {
	if p.st != stNormal || len(p.seq) > 0 || p.count > 0 || p.cur > 0 || p.cmd.Register == 0 || p.cmd.Kind != 0 {
		return 0, false
	}
	return p.cmd.Register, true
}

// TakeRegister は、PendingRegister が true のとき、その名前を返して解釈の途中の状態を捨てる。そうでなければ何もせず 0 を返す。
func (p *Parser) TakeRegister() rune {
	r, ok := p.PendingRegister()
	if !ok {
		return 0
	}
	p.reset()
	return r
}

// Feed は、キーを 1 個受け取り、その時点で決まった結果を返す。
// 解釈の途中なら空のスライスを返す。
// 短い登録に決めて残りのキーを続けて解釈した場合（Parser の説明を参照）、結果は 2 件以上になりうる。
//
// Esc は、どの状態でも解釈を捨てて StatusCanceled を返す。
// ただし、より長い登録を待っている短い登録があれば（拡張機能の xyz を登録した後の x<Esc>）、
// Vim と同じく先に Flush と同じ手順で短い方に決め、その結果の後に StatusCanceled を返す。
// オペレーターの後の Esc（y<Esc>、拡張機能の ys があっても同じ）は、オペレーターごと捨てる。
//
// req: FR-02
func (p *Parser) Feed(k editcmd.Key) []editcmd.Result {
	if k.Code == editcmd.CodeEsc {
		rs := p.Flush()
		p.reset()
		return append(rs, editcmd.Result{Status: editcmd.StatusCanceled})
	}
	return p.run([]editcmd.Key{k})
}

// Flush は、時間切れ（Vim の timeoutlen に当たる）で、待っている曖昧なキーの列を確定させる。
// イベントループは、キーが一定の時間来なかったときと、マクロの再生を終えたときに呼ぶ。
//
// 確定させるのは、溜めた列の接頭辞に完成した登録があり、かつより長い登録を待っている場合だけである
// （本体の y と拡張機能の ys の y）。このときは最長の完成した登録に決め、残りのキーを続けて解釈する。
// 本体の操作の途中（g、di、ci、3、"a、f）のように完成した候補が無い場合は、何もせず nil を返し、
// 次のキーを待ち続ける。本体の操作だけの表で、完成した候補とより長い候補が両方ある列は、ノーマルモードの gc（オペレーター）と
// gcc（comment-line）だけである。gc の後に時間が切れると gc のオペレーターに決め、続く c は重ね押しとして行単位の対象になるので、
// 結果は gcc と同じになる（見比べの do と dp は、表の列ではなく d のオペレーターの後の o と p として読む。match）。
func (p *Parser) Flush() []editcmd.Result {
	n := p.longestExact()
	if n == 0 {
		return nil
	}
	rs, rest := p.resolve(n)
	return append(rs, p.run(rest)...)
}

func (p *Parser) run(queue []editcmd.Key) []editcmd.Result {
	var out []editcmd.Result
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		rs, rest := p.step(k)
		out = append(out, rs...)
		queue = append(rest, queue...)
	}
	return out
}

// step は、キーを 1 個解釈する。rest は、短い登録に決めたために解釈し直すキーの列である。
func (p *Parser) step(k editcmd.Key) (results []editcmd.Result, rest []editcmd.Key) {
	switch p.st {
	case stRegister:
		// 名前は検査せずに値へ入れ、command が確かめて、使えない名前ならメッセージを出す（基本設計 4.5。FR-50）。
		if !k.Plain() {
			return p.fail(), nil
		}
		p.cmd.Register = k.Rune
		p.st = stNormal
		return nil, nil
	case stChar:
		return p.takeChar(k), nil
	case stNormal, stOperator:
	}

	if len(p.seq) == 0 && k.Plain() {
		if d, ok := countDigit(k.Rune, p.cur > 0); ok {
			p.cur = min(p.cur*10+d, MaxCount)
			return nil, nil
		}
		if p.st == stNormal && (!p.visual || k.Rune == '"' || k.Rune == 'q' && p.recording) {
			if rs, ok := p.special(k.Rune); ok {
				return rs, nil
			}
		}
	}

	p.seq = append(p.seq, k)
	exact, longer := p.match(p.seq)
	if longer {
		return nil, nil
	}
	if exact != nil {
		p.seq = nil
		return p.apply(*exact), nil
	}
	if n := p.longestExact(); n > 0 {
		return p.resolve(n)
	}
	return p.fail(), nil
}

// special は、キーマップより先に読むキー（"、q、@、.）を扱う。扱ったかを ok で返す。
func (p *Parser) special(r rune) (results []editcmd.Result, ok bool) {
	switch r {
	case '"':
		p.endCount()
		p.st = stRegister
		return nil, true
	case '.':
		p.cmd.Kind = editcmd.KindRepeat
		return p.complete(), true
	case 'q':
		if p.recording {
			p.cmd.Kind = editcmd.KindRecordStop
			return p.complete(), true
		}
		p.cmd.Kind = editcmd.KindRecordStart
		p.st, p.charFor = stChar, charRecord
		return nil, true
	case '@':
		p.cmd.Kind = editcmd.KindPlay
		p.st, p.charFor = stChar, charPlay
		return nil, true
	}
	return nil, false
}

// takeChar は、stChar で文字の引数を受け取る。
func (p *Parser) takeChar(k editcmd.Key) []editcmd.Result {
	switch p.charFor {
	case charRecord, charPlay:
		// " の後と同じく、名前は command が確かめる（FR-50）。
		if !k.Plain() {
			return p.fail()
		}
		p.cmd.Char = k.Rune
	case charTarget, charCommand, charCommand2:
		r, ok := charArg(k)
		if !ok {
			return p.fail()
		}
		switch p.charFor {
		case charTarget:
			p.cmd.Target.Char = r
		case charCommand2:
			p.cmd.Char2 = r
		case charCommand, charRecord, charPlay:
			p.cmd.Char = r
			if p.takesChar2 {
				p.takesChar2 = false
				p.charFor = charCommand2
				return nil
			}
		}
	}
	p.st = stNormal
	return p.complete()
}

// longestExact は、溜めた列の接頭辞のうち、現在の状態で登録に一致する最長のものの長さを返す。無ければ 0。
func (p *Parser) longestExact() int {
	for n := len(p.seq); n > 0; n-- {
		if exact, _ := p.match(p.seq[:n]); exact != nil {
			return n
		}
	}
	return 0
}

// resolve は、溜めた列の先頭 n 個（登録に一致する）に決め、残りを rest で返す。
func (p *Parser) resolve(n int) (results []editcmd.Result, rest []editcmd.Key) {
	exact, _ := p.match(p.seq[:n])
	rest = slices.Clone(p.seq[n:])
	p.seq = nil
	return p.apply(*exact), rest
}

// match は、現在の状態で引く操作のうち、seq に一致するものと、より長い登録があるかを返す。
// オペレーターの後では、重ね押し（オペレーターのキーの列そのものか、その最後のキー）も候補にする。
// 重ね押しとモーションの両方に一致する場合は重ね押しを優先する。
// ただし、2 キー以上のオペレーターのキーの列そのものが、テキストオブジェクトのキーの列でもあれば、テキストオブジェクトを優先する。
// gc の後の gc（gcgc）は、Neovim 0.12.5 と同じく、コメントのまとまりのテキストオブジェクト（commented-lines）に gc を働かせる
// （2026-11-21 に確かめた。行単位の重ね押しは gcc で打つ）。
//
// 本体の d のオペレーターの後の o と p は、どの登録にも一致しなければ、見比べの do と dp（FR-101）の操作にする。
// Vim 9.1 の nv_open と nv_put_opt（src/normal.c）が、オペレーターが d のときに nv_diffgetput を呼ぶのと同じで、
// キーの列ではないので、d の後に時間を置いても do と dp になる。
func (p *Parser) match(seq []editcmd.Key) (exact *Binding, longer bool) {
	if p.st == stNormal {
		if p.visual {
			return p.km.match(MapVisual, seq)
		}
		return p.km.match(MapNormal, seq)
	}
	exact, longer = p.km.match(MapOperatorPending, seq)
	textObject := exact != nil && exact.Kind == BindTextObject
	for i, form := range [][]editcmd.Key{p.opKeys, p.opKeys[len(p.opKeys)-1:]} {
		switch {
		case slices.Equal(form, seq) && i == 0 && len(form) > 1 && textObject:
		case slices.Equal(form, seq):
			exact = &Binding{Kind: bindLine}
		case len(form) > len(seq) && slices.Equal(form[:len(seq)], seq):
			longer = true
		}
	}
	if exact == nil && !longer && len(seq) == 1 && seq[0].Plain() &&
		p.cmd.Operator.Builtin() && p.cmd.Operator.ID == "delete" {
		switch seq[0].Rune {
		case 'o':
			exact = &Binding{Kind: BindAction, Ref: editcmd.Ref{ID: DiffObtainID}}
		case 'p':
			exact = &Binding{Kind: BindAction, Ref: editcmd.Ref{ID: DiffPutID}}
		}
	}
	return exact, longer
}

// 見比べの do と dp の操作の ID（match）。実行の側（internal/command）がこの ID で実装を引く。
const (
	DiffObtainID = "diff-obtain"
	DiffPutID    = "diff-put"
)

// apply は、引いた操作を組み立て中の値に反映する。値が完成すれば結果を返す。
func (p *Parser) apply(b Binding) []editcmd.Result {
	p.endCount()
	switch b.Kind {
	case BindOperator:
		p.cmd.Kind = editcmd.KindOperator
		p.cmd.Operator = b.Ref
		p.cmd.Mode = b.NextMode
		p.opKeys = b.Keys
		p.opTakesChar = b.TakesChar
		if p.visual {
			return p.complete()
		}
		p.st = stOperator
		return nil
	case BindMotion, BindTextObject:
		tk := editcmd.TargetMotion
		if b.Kind == BindTextObject {
			tk = editcmd.TargetTextObject
		}
		p.cmd.Target = editcmd.Target{Kind: tk, Ref: b.Ref}
		if p.st == stNormal {
			p.cmd.Kind = editcmd.KindMotion
			p.cmd.Mode = b.NextMode
		}
		if b.TakesChar {
			p.st, p.charFor = stChar, charTarget
			return nil
		}
	case BindAction:
		p.cmd.Kind = editcmd.KindAction
		p.cmd.Operator = editcmd.Ref{} // d の後の o と p（do と dp。match）は、オペレーターを捨てて操作にする
		p.cmd.Action = b.Ref
		p.cmd.Mode = b.NextMode
		if b.TakesChar {
			p.st, p.charFor = stChar, charCommand
			p.takesChar2 = b.TakesSecondChar
			return nil
		}
	case bindLine:
		p.cmd.Target = editcmd.Target{Kind: editcmd.TargetLine}
	}
	return p.complete()
}

// complete は、対象までそろった値を仕上げる。
// 対象の後に文字を取るオペレーターなら、その文字を待つ。そうでなければ値を返す。
func (p *Parser) complete() []editcmd.Result {
	if p.cmd.Kind == editcmd.KindOperator && p.opTakesChar {
		p.opTakesChar = false
		p.st, p.charFor = stChar, charCommand
		return nil
	}
	p.endCount()
	cmd := p.cmd
	cmd.Count, cmd.CountGiven = 1, false
	if p.count > 0 {
		cmd.Count, cmd.CountGiven = p.count, true
	}
	p.reset()
	return []editcmd.Result{{Command: cmd, Status: editcmd.StatusDone}}
}

// endCount は、入力の途中の回数を、終わった回数の積に掛ける。
func (p *Parser) endCount() {
	if p.cur == 0 {
		return
	}
	if p.count == 0 {
		p.count = p.cur
	} else {
		p.count = min(p.count*p.cur, MaxCount) // どちらも MaxCount 以下なので、64 ビットの int では積が溢れない
	}
	p.cur = 0
}

func (p *Parser) fail() []editcmd.Result {
	p.reset()
	return []editcmd.Result{{Status: editcmd.StatusInvalid}}
}

func (p *Parser) reset() {
	*p = Parser{km: p.km, recording: p.recording, visual: p.visual}
}

// countDigit は、r が回数の数字であれば、その値を返す。
// 0 は、回数の入力の途中（inCount）でだけ数字で、そうでなければモーションである。
func countDigit(r rune, inCount bool) (int, bool) {
	if r >= '1' && r <= '9' || (r == '0' && inCount) {
		return int(r - '0'), true
	}
	return 0, false
}

// charArg は、f や r の文字の引数として受け付けるキーを文字にする。
// 修飾キーの無い文字のほか、<CR> を '\n'、<Tab> を '\t' として受け付ける（Vim の r<CR>、f<Tab>）。
func charArg(k editcmd.Key) (rune, bool) {
	if k.Mod != 0 {
		return 0, false
	}
	switch k.Code {
	case editcmd.CodeRune:
		return k.Rune, true
	case editcmd.CodeEnter:
		return '\n', true
	case editcmd.CodeTab:
		return '\t', true
	case editcmd.CodeEsc, editcmd.CodeBackspace, editcmd.CodeDelete, editcmd.CodeUp, editcmd.CodeDown, editcmd.CodeLeft, editcmd.CodeRight,
		editcmd.CodeHome, editcmd.CodeEnd, editcmd.CodePageUp, editcmd.CodePageDown, editcmd.CodeInsert, editcmd.CodeF1,
		editcmd.CodeLeftMouse, editcmd.CodeLeftDrag, editcmd.CodeLeftRelease, editcmd.CodeMiddleMouse, editcmd.CodeMiddleDrag,
		editcmd.CodeMiddleRelease, editcmd.CodeRightMouse, editcmd.CodeRightDrag, editcmd.CodeRightRelease,
		editcmd.CodeScrollWheelUp, editcmd.CodeScrollWheelDown, editcmd.CodeScrollWheelLeft, editcmd.CodeScrollWheelRight:
	}
	return 0, false
}
