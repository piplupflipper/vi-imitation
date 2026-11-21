package target

import (
	"strings"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// この file の関数は、表 motions に登録するモーションである。
// 各関数は Request が正しいこと（validate を通ったこと）を前提にする。

// at は、p へ移動した結果を、p の文字の列（req.Columns で数える）を望みの列にして返す。
func at(b *buffer.Buffer, req Request, p buffer.Position, kind Kind) Result {
	return Result{Pos: p, Kind: kind, Want: req.columns().Column(p.Line, b.Line(p.Line), p.Byte)}
}

// left は h である。回数の文字だけ左へ移る。行の先頭を越えない。
// オペレーターが無く、行の先頭にいれば失敗する。
// オペレーターの後では失敗せず、行の先頭では空の範囲になる（Vim 9.1 の nv_left()。空の行や行頭の yh は空のテキストをレジスターに書き、
// ch はそのままインサートモードへ移る。先行入力も捨てない。#86）。
func left(b *buffer.Buffer, req Request) (Result, error) {
	p := req.Cursor
	if p.Byte == 0 && req.Operator == NoOperator {
		return Result{}, ErrNoTarget
	}
	line := b.Line(p.Line)
	for range req.count1() {
		if p.Byte == 0 {
			break
		}
		p.Byte = prevCell(line, p.Byte)
	}
	return at(b, req, p, Exclusive), nil
}

// right は l である。回数の文字だけ右へ移る。
// オペレーターが無ければ行の最後の文字を、あれば行末の位置を越えない（dl で最後の文字を消せるように）。
// オペレーターが無く、それ以上進めなければ失敗する。
// オペレーターの後では失敗しない（Vim の nv_right()）。空の行では空の範囲になり、cl（s）はそのままインサートモードへ移る。
func right(b *buffer.Buffer, req Request) (Result, error) {
	p := req.Cursor
	line := b.Line(p.Line)
	limit := lastCharByte(line)
	if req.Operator != NoOperator {
		limit = len(line)
	} else if p.Byte >= limit {
		return Result{}, ErrNoTarget
	}
	for range req.count1() {
		if p.Byte >= limit {
			break
		}
		p.Byte = nextCell(line, p.Byte)
	}
	return at(b, req, p, Exclusive), nil
}

// down は j である。回数の行だけ下へ、行単位で移る。
// 最後の行を越える回数は最後の行で止める。最後の行にいれば失敗する。
func down(b *buffer.Buffer, req Request) (Result, error) {
	last := b.LineCount() - 1
	if req.Cursor.Line >= last {
		return Result{}, ErrNoTarget
	}
	return vertical(b, req, min(req.Cursor.Line+req.count1(), last)), nil
}

// up は k である。回数の行だけ上へ、行単位で移る。
// 最初の行を越える回数は最初の行で止める。最初の行にいれば失敗する。
func up(b *buffer.Buffer, req Request) (Result, error) {
	if req.Cursor.Line == 0 {
		return Result{}, ErrNoTarget
	}
	return vertical(b, req, max(req.Cursor.Line-req.count1(), 0)), nil
}

// vertical は、line 行の、望みの列に当たる文字へ移った結果を返す。望みの列は変えない。
// 行の幅が望みの列に届かなければ、最後の文字へ移る。
func vertical(b *buffer.Buffer, req Request, line int) Result {
	p := buffer.Position{Line: line, Byte: req.columns().ByteAt(line, b.Line(line), req.Want)}
	return Result{Pos: p, Kind: Linewise, Want: req.Want}
}

// displayVertical は、gj（forward が true）と gk を返す（FR-157）。回数の見た目の行だけ下（上）へ動く。
//
// 行を折り返して描かないとき（req.Screen が nil か Wrap が false）は、j と k と同じく行単位で動く（Vim 9.1 の nv_down の 'nowrap' の扱い）。
// 折り返して描くときは、Neovim 0.12.5 の nv_screengo（src/nvim/normal.c）をそのまま写した。列は req.Columns の列で、
// 呼び出し側は折り返すとき、見た目の n 番目の行の左の端を n × Width とする数え方を渡す（ScreenLines の doc）。W は sl.Width() である。
//
//   - 望みの列が行末（WantEOL。$ の後）なら、望みの列をカーソルの見た目の行の右の端の列にする。そうでなければ、行の最後の見た目の行の
//     右の端の列で止める
//   - 下へは、望みの列に W を足して行の最後の見た目の行を越えなければ足し、越えるなら次の行へ移って望みの列を W で割った余りにする。
//     上へは、望みの列が W 以上なら W を引き、そうでなければ前の行へ移って、望みの列にその行の最後の見た目の行の左の端の列を足す
//   - 望みの列の文字へ移る（Columns.ByteAt。">" の列は、送った全角文字の列として数える）
//   - 着いた文字の列（タブは最後の列）が望みの列より右で、望みの列が見た目の行の右半分にあるなら、1 つ前の文字へ移る
//     （nv_screengo の末尾の補正。送った全角文字の ">" の列に着いたときに、その見た目の行に留まる）。続きの行の記号の列は引いて比べる
//   - 望みの列は、動いた後の望みの列にする（文字より右でもそのまま保つ）。WantEOL は WantEOL のまま
//   - 範囲は文字単位の Exclusive で、WantEOL なら Inclusive である（Vim と同じ）
//
// nv_screengo の、表示できない全角の文字（c > 255）に着いたときに 1 つ右へ移す補正は写していない（本体はその文字を <200b> の形で描く）。
//
// バッファの端で 1 行も動けなければ失敗する。回数の途中でバッファの端に着いたときは、着いた位置へ移って成功する。
// Vim は着いた位置へ移ってから失敗する（ビープを鳴らし、オペレーターを取り消し、マクロを止める）ので、ここは Vim と違う（docs/vim-differences.md）。
//
// req: FR-157
func displayVertical(forward bool) MotionFunc {
	return func(b *buffer.Buffer, req Request) (Result, error) {
		sl := req.Screen
		if sl == nil || !sl.Wrap() {
			if forward {
				return down(b, req)
			}
			return up(b, req)
		}
		cols := req.columns()
		w1 := max(sl.Width(), 1)
		w2 := w1
		line := req.Cursor.Line
		s := b.Line(line)
		// linelen は、行の最後の文字の次の列（Vim の linetabsize）で、行を移ったときだけ数え直す（数えるのは行の長さに比例する）。
		linelen := cols.Column(line, s, len(s))
		// limit は、行の最後の見た目の行の右の端の次の列である。
		limit := func(linelen int) int {
			if linelen > w1 {
				return ((linelen-w1-1)/w2+1)*w2 + w1
			}
			return w1
		}
		atend := req.Want == WantEOL
		want := req.Want
		if atend {
			want = w1 - 1
			if vc := cols.Column(line, s, req.Cursor.Byte); vc > want {
				want += ((vc-want-1)/w2 + 1) * w2
			}
		} else {
			want = min(want, limit(linelen)-1)
		}
		moved := 0
		for range req.count1() {
			if forward {
				if want+w2 < limit(linelen) {
					want += w2
				} else {
					if line+1 >= b.LineCount() {
						break
					}
					line++
					s = b.Line(line)
					want %= w2
					if want >= w1 {
						want -= w2
					}
					linelen = cols.Column(line, s, len(s))
				}
			} else {
				if want >= w1 {
					want -= w2
				} else {
					if line == 0 {
						break
					}
					line--
					s = b.Line(line)
					linelen = cols.Column(line, s, len(s))
					if linelen > w1 {
						want += ((linelen-w1-1)/w2 + 1) * w2
					}
				}
			}
			moved++
		}
		if moved == 0 {
			return Result{}, ErrNoTarget
		}
		p := buffer.Position{Line: line, Byte: cols.ByteAt(line, s, max(want, 0))}
		var right bool
		if want < w1 {
			right = want > w1/2
		} else {
			right = (want-w1)%w2 > w2/2
		}
		// 着いた文字の列は、望みの列が右半分のときだけ数える（数えるのは行の長さに比例する）。
		if p.Byte > 0 && right {
			vc := cols.Column(line, s, p.Byte)
			if sbw := sl.BreakWidth(); vc > w1 && sbw > 0 {
				vc -= sbw
			}
			if vc > want {
				p.Byte = prevCell(s, p.Byte)
			}
		}
		if atend {
			return Result{Pos: p, Kind: Inclusive, Want: WantEOL}, nil
		}
		return Result{Pos: p, Kind: Exclusive, Want: want}, nil
	}
}

// wordForward は、w（big が true なら W）を返す。
//
// 単語の先頭へ進む。空の行も 1 つの単語として止まる。Exclusive である。
// オペレーターがあるとき、最後に越えた単語が行末にあれば、次の行へは進まずにその行末で止める（`:help w` の特例）。
// このとき、および最後の単語の後でバッファの末尾に着いたときは、最後の文字で Inclusive にする（Vim の adjust_cursor()）。
// c の後では、カーソルが空白の上になければ ce（cE）と同じく単語の終わりまでにする（`:help cw`）。
//
// オペレーターが無い場合、バッファの末尾に着いて進めなければ失敗する。
// 回数の途中で末尾に着いたときは、Vim と同じく、着いた位置へ移って成功する（Vim はビープを鳴らす）。
// オペレーターがある場合は失敗せず、dw はバッファの最後の単語も消せる。
func wordForward(big bool) MotionFunc {
	return func(b *buffer.Buffer, req Request) (Result, error) {
		c := cursor{b: b, Position: req.Cursor}
		kind := Exclusive
		var ok bool
		if req.Operator == ChangeOperator && !c.atEOL() && !isBlank(c.char()) {
			kind = Inclusive
			ok = endWord(&c, req.count1(), big, true, false)
		} else {
			ok = fwdWord(&c, req.count1(), big, req.Operator != NoOperator)
		}
		if req.Cursor.Less(c.Position) && adjustEOL(&c) {
			kind = Inclusive
		}
		if !ok && req.Operator == NoOperator && c.Position == req.Cursor {
			return Result{}, ErrNoTarget
		}
		return at(b, req, c.Position, kind), nil
	}
}

// wordEnd は、e（big が true なら E）を返す。
// 単語の終わりへ進む。空の行には止まらない。Inclusive である。
// オペレーターが無い場合、バッファの末尾に着いて進めなければ失敗する（w と同じ）。
func wordEnd(big bool) MotionFunc {
	return func(b *buffer.Buffer, req Request) (Result, error) {
		c := cursor{b: b, Position: req.Cursor}
		ok := endWord(&c, req.count1(), big, false, false)
		if req.Cursor.Less(c.Position) {
			adjustEOL(&c)
		}
		if !ok && req.Operator == NoOperator && c.Position == req.Cursor {
			return Result{}, ErrNoTarget
		}
		return at(b, req, c.Position, Inclusive), nil
	}
}

// wordBackward は、b（big が true なら B）を返す。
// 単語の先頭へ戻る。空の行にも止まる。Exclusive である。バッファの先頭にいれば失敗する。
func wordBackward(big bool) MotionFunc {
	return func(b *buffer.Buffer, req Request) (Result, error) {
		c := cursor{b: b, Position: req.Cursor}
		if !bckWord(&c, req.count1(), big) {
			return Result{}, ErrNoTarget
		}
		return at(b, req, c.Position, Exclusive), nil
	}
}

// adjustEOL は、空でない行の行末の位置にいれば最後の文字へ戻り、true を返す（Vim の adjust_cursor()）。
func adjustEOL(c *cursor) bool {
	if c.Byte > 0 && c.atEOL() {
		c.dec()
		return true
	}
	return false
}

// fwdWord は、Vim の fwd_word() である。count 個先の単語の先頭へ進む。
// eol が true なら、最後の 1 回で行末に着いた時点で止まる。
// 最初からバッファの最後の文字（最後の行の行末の位置を含む）にいて進めなければ false を返す。
func fwdWord(c *cursor, count int, big, eol bool) bool {
	for count > 0 {
		count--
		sclass := c.cls(big)
		lastLine := c.Line == c.b.LineCount()-1
		i := c.inc()
		if i == stuck || i >= crossed && lastLine {
			return false
		}
		if i >= crossed && eol && count == 0 {
			return true
		}
		// 今の単語の後まで進む。
		if sclass != classBlank {
			for c.cls(big) == sclass {
				i = c.inc()
				if i == stuck || i >= crossed && eol && count == 0 {
					return true
				}
			}
		}
		// 次の空白でない文字まで進む。空の行には止まる。
		for c.cls(big) == classBlank {
			if c.Byte == 0 && c.lineEmpty() {
				break
			}
			i = c.inc()
			if i == stuck || i >= crossed && eol && count == 0 {
				return true
			}
		}
	}
	return true
}

// endWord は、Vim の end_word() である。count 個先の単語の終わりへ進む。
// stop が true なら、単語の終わりにいるときは 1 回目をその場で済ませる（cw のため）。
// empty が true なら、空の行に止まる（iw のため）。
// バッファの末尾に着いて終わりが決まらなければ、false を返す。
func endWord(c *cursor, count int, big, stop, empty bool) bool {
	for ; count > 0; count-- {
		sclass := c.cls(big)
		if c.inc() == stuck {
			return false
		}
		switch {
		case c.cls(big) == sclass && sclass != classBlank:
			// 単語の途中なので、その単語の終わりまで進む。
			if c.skipClass(sclass, big, true) {
				return false
			}
		case !stop || sclass == classBlank:
			// 単語の終わりにいたので、空白を飛ばして次の単語の終わりまで進む。
			if skipBlankForward(c, big, empty) {
				stop = false
				continue
			}
			if c.cls(big) == classBlank {
				return false
			}
			if c.skipClass(c.cls(big), big, true) {
				return false
			}
		}
		c.dec() // 1 文字行き過ぎたので戻る
		stop = false
	}
	return true
}

// skipBlankForward は、空白を飛ばして進む。empty が true で空の行に着いたら、そこで止まって true を返す。
// バッファの末尾に着いたときは、空白の上にいるまま false を返す。
func skipBlankForward(c *cursor, big, empty bool) bool {
	for c.cls(big) == classBlank {
		if c.Byte == 0 && c.lineEmpty() && empty {
			return true
		}
		if c.inc() == stuck {
			return false
		}
	}
	return false
}

// bckWord は、Vim の bck_word() である。count 個前の単語の先頭へ戻る。
// 最初からバッファの先頭にいれば false を返す。途中でバッファの先頭に着いたときは、そこで止まって true を返す。
func bckWord(c *cursor, count int, big bool) bool {
	for ; count > 0; count-- {
		if c.dec() == stuck {
			return false
		}
		atEmpty := false
		for c.cls(big) == classBlank {
			if c.Byte == 0 && c.lineEmpty() {
				atEmpty = true
				break
			}
			if c.dec() == stuck {
				return true
			}
		}
		if atEmpty {
			continue
		}
		if c.skipClass(c.cls(big), big, false) {
			return true
		}
		c.inc() // 1 文字行き過ぎたので戻る
	}
	return true
}

// lineStart は 0 である。行の先頭へ移る。Exclusive である。
func lineStart(b *buffer.Buffer, req Request) (Result, error) {
	return at(b, req, buffer.Position{Line: req.Cursor.Line}, Exclusive), nil
}

// firstNonBlank は ^ である。行の最初の空白でない文字へ移る。回数は使わない。Exclusive である。
// 空白だけの行では最後の文字へ移る。
func firstNonBlank(b *buffer.Buffer, req Request) (Result, error) {
	line := req.Cursor.Line
	return at(b, req, buffer.Position{Line: line, Byte: firstNonBlankByte(b.Line(line))}, Exclusive), nil
}

// lineEnd は $ である。回数から 1 を引いた行だけ下の行の、最後の文字へ移る。Inclusive で、望みの列を WantEOL にする。
// j と同じく、最後の行を越える回数は最後の行で止め、回数が 2 以上で最後の行にいれば失敗する。
func lineEnd(b *buffer.Buffer, req Request) (Result, error) {
	last := b.LineCount() - 1
	n := req.count1() - 1
	if n > 0 && req.Cursor.Line >= last {
		return Result{}, ErrNoTarget
	}
	line := min(req.Cursor.Line+n, last)
	p := buffer.Position{Line: line, Byte: lastCharByte(b.Line(line))}
	return Result{Pos: p, Kind: Inclusive, Want: WantEOL}, nil
}

// gotoLine は、gg（last が true なら G）を返す。
// 回数を明示すればその行（1 から数える。行数を超えれば最後の行）、省略すれば最初の行（G は最後の行）の、
// 最初の空白でない文字へ、行単位で移る。失敗しない。
func gotoLine(last bool) MotionFunc {
	return func(b *buffer.Buffer, req Request) (Result, error) {
		line := 0
		switch {
		case req.CountGiven:
			line = min(req.count1(), b.LineCount()) - 1
		case last:
			line = b.LineCount() - 1
		}
		return lineFirstNonBlank(b, req, line), nil
	}
}

// lineFirstNonBlank は、line 行の最初の空白でない文字へ、行単位で移った結果を返す。
func lineFirstNonBlank(b *buffer.Buffer, req Request, line int) Result {
	return at(b, req, buffer.Position{Line: line, Byte: firstNonBlankByte(b.Line(line))}, Linewise)
}

// findChar は、f、F、t、T を返す。backward は F と T、till は t と T である。
//
// 今の行の中で、Request.Char の回数番目の出現を探す。
// f は出現の位置へ、t はその 1 文字手前へ移る。F と T は逆向きである。
// 前向きは Inclusive、後ろ向きは Exclusive である。
// 見つからなければ失敗する。Vim と同じく、失敗したときも ; のための検索を Result.Find に入れて返す。
func findChar(backward, till bool) MotionFunc {
	return func(b *buffer.Buffer, req Request) (Result, error) {
		f := Find{Char: req.Char, Backward: backward, Till: till}
		if f.Char == 0 {
			return Result{}, ErrNoTarget
		}
		res, err := searchChar(b, req, f, req.count1(), true)
		res.Find = f
		return res, err
	}
}

// repeatFind は、;（reverse が true なら ,）を返す。Request.Last の検索を、, は逆向きに繰り返す。
// 直前の検索が無ければ失敗する。
//
// t と T の繰り返しで回数が 1 なら、すぐ隣の出現を飛ばす（Vim の 'cpoptions' に ; が無い既定の動作）。
// こうしないと、t の後の ; がその場から動かない。
// 直前の検索の記録は変えないので、Result.Find はゼロ値である。
func repeatFind(reverse bool) MotionFunc {
	return func(b *buffer.Buffer, req Request) (Result, error) {
		f := req.Last
		if f.Char == 0 {
			return Result{}, ErrNoTarget
		}
		f.Backward = f.Backward != reverse
		n := req.count1()
		return searchChar(b, req, f, n, n != 1 || !f.Till)
	}
}

// searchChar は、Vim の searchc() である。stop が false なら、最初に調べる 1 文字を飛ばす。
func searchChar(b *buffer.Buffer, req Request, f Find, count int, stop bool) (Result, error) {
	from := req.Cursor
	line := b.Line(from.Line)
	col := from.Byte
	for range count {
		for {
			if f.Backward {
				if col == 0 {
					return Result{}, ErrNoTarget
				}
				_, size := utf8.DecodeLastRuneInString(line[:col])
				col -= size
			} else {
				_, size := utf8.DecodeRuneInString(line[col:])
				col += size
				if col >= len(line) {
					return Result{}, ErrNoTarget
				}
			}
			r, _ := utf8.DecodeRuneInString(line[col:])
			if r == f.Char && stop {
				break
			}
			stop = true
		}
	}
	if f.Till {
		if f.Backward {
			_, size := utf8.DecodeRuneInString(line[col:])
			col += size
		} else {
			_, size := utf8.DecodeLastRuneInString(line[:col])
			col -= size
		}
	}
	kind := Inclusive
	if f.Backward {
		kind = Exclusive
	}
	return at(b, req, buffer.Position{Line: from.Line, Byte: col}, kind), nil
}

// brackets は、% が対応を探す括弧である。
const brackets = "()[]{}"

// percent は % である。
//
// 回数を省略すれば、カーソルの位置かそれより後で今の行にある最初の括弧（brackets）を探し、それに対応する括弧へ移る。Inclusive である。
// 括弧が無いか、対応する括弧が無ければ失敗する。
// バックスラッシュが奇数個続いた後の括弧（\( など）は、始めの括弧と同じくエスケープされた括弧とだけ対にする（Vim の 'cpoptions' に M が無い既定の動作）。
// 回数を明示すれば、バッファの回数パーセントの行の最初の空白でない文字へ、行単位で移る。回数が 100 を超えれば失敗する。
//
// Vim と違い、引用符の中の括弧を区別しない。C のコメント（/* */）と #if の対応も探さない。
func percent(b *buffer.Buffer, req Request) (Result, error) {
	if req.CountGiven {
		count := req.count1()
		if count > 100 {
			return Result{}, ErrNoTarget
		}
		n := b.LineCount()
		line := (count*n + 99) / 100
		return lineFirstNonBlank(b, req, min(max(line, 1), n)-1), nil
	}
	line := b.Line(req.Cursor.Line)
	i := strings.IndexAny(line[req.Cursor.Byte:], brackets)
	if i < 0 {
		return Result{}, ErrNoTarget
	}
	c := cursor{b: b, Position: buffer.Position{Line: req.Cursor.Line, Byte: req.Cursor.Byte + i}}
	k := strings.IndexRune(brackets, c.char())
	open, closing := rune(brackets[k&^1]), rune(brackets[k|1])
	escaped := isEscaped(line, c.Byte)
	found := false
	if k&1 == 0 {
		found = findPair(&c, closing, open, true, escaped)
	} else {
		found = findPair(&c, open, closing, false, escaped)
	}
	if !found {
		return Result{}, ErrNoTarget
	}
	return at(b, req, c.Position, Inclusive), nil
}

// findPair は、c の位置の次（forward が false なら前）から探して、入れ子になっていない want へ移る（Vim の findmatchlimit()）。
// nest に出会うたびに入れ子を 1 段深くし、want に出会うたびに 1 段浅くする。
// escaped が true ならバックスラッシュでエスケープされた括弧だけを、false ならエスケープされていない括弧だけを数える（Vim の 'cpoptions' に M が無い既定の動作）。
// 見つからなければ c を元に戻して false を返す。
func findPair(c *cursor, want, nest rune, forward, escaped bool) bool {
	save := c.Position
	step := c.inc
	if !forward {
		step = c.dec
	}
	depth := 0
	for step() != stuck {
		ch := c.char()
		if ch != want && ch != nest || isEscaped(c.line(), c.Byte) != escaped {
			continue
		}
		if ch == nest {
			depth++
			continue
		}
		if depth == 0 {
			return true
		}
		depth--
	}
	c.Position = save
	return false
}

// isEscaped は、行の i の位置の直前に、バックスラッシュが奇数個続くかどうかを返す。
func isEscaped(line string, i int) bool {
	n := 0
	for i-n > 0 && line[i-n-1] == '\\' {
		n++
	}
	return n%2 == 1
}

// paragraph は、}（forward が false なら {）を返す。
//
// 回数の数だけ、空でない行を 1 行以上越えた先の空の行（段落の境界）へ、行の先頭へ移る。Exclusive である。
// 空白だけの行は境界にしない（Vim と同じ）。
// 最後の回でバッファの端に着いたら、そこで止まる。} で最後の行に止まったときは、その行の最後の文字で Inclusive にする。
// 最後の回より前にバッファの端に着いたら失敗する。
func paragraph(forward bool) MotionFunc {
	return func(b *buffer.Buffer, req Request) (Result, error) {
		dir := -1
		if forward {
			dir = 1
		}
		n := b.LineCount()
		curr := req.Cursor.Line
		for count := req.count1(); count > 0; {
			count--
			didSkip := false
			for first := true; ; first = false {
				if b.Line(curr) != "" {
					didSkip = true
				}
				if !first && didSkip && b.Line(curr) == "" {
					break
				}
				curr += dir
				if curr < 0 || curr >= n {
					if count > 0 {
						return Result{}, ErrNoTarget
					}
					curr -= dir
					break
				}
			}
		}
		p := buffer.Position{Line: curr}
		if forward && curr == n-1 && b.Line(curr) != "" {
			p.Byte = lastCharByte(b.Line(curr))
			return at(b, req, p, Inclusive), nil
		}
		return at(b, req, p, Exclusive), nil
	}
}
