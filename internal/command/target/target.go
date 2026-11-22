// Package target は、編集コマンドの「対象」（基本設計 3 章）であるモーションとテキストオブジェクトを、
// バッファの上の位置と範囲に解決する。
//
// 基本設計 2 章の internal/command の一部で、internal/buffer を読むだけの純粋な関数の集まりである。
// バッファを変更せず、カーソル、望みの列、直前の f の検索などの状態も持たない。
// 状態は呼び出し側（コマンドの実行）が持ち、Request で渡して Result で受け取る。
//
// 振る舞いは Vim 9 の既定の設定（'cpoptions' の既定値、'whichwrap' は "b,s"、'startofline' はオン、'selection' は inclusive）に合わせる。
// Vim と違う点は、各関数の doc コメントに書く。
//
// # 位置の表し方
//
// 位置は buffer.Position（行の番号と、行の先頭からのバイト数）で表す。
// 行の長さ（行末）を指す位置を「行末の位置」と呼ぶ。
// Vim は行末の NUL にカーソルを置いて単語の境界を数えるので、この実装も行末の位置を空白と同じ分類の 1 文字として数える。
// オペレーターの無いモーションの結果は、空の行を除いて行末の位置を指さない。
package target

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// Kind は、範囲の端の扱い（Vim の exclusive、inclusive、linewise。`:help exclusive`、`:help linewise`）を表す。
type Kind int

const (
	// Exclusive は、終わりの位置の文字を範囲に含まないことを表す。
	Exclusive Kind = iota
	// Inclusive は、終わりの位置の文字を範囲に含むことを表す。
	Inclusive
	// Linewise は、始まりの行から終わりの行までの行全体を範囲にすることを表す。
	Linewise
)

// String は、デバッグとテストのために種類の名前を返す。
func (k Kind) String() string {
	switch k {
	case Exclusive:
		return "exclusive"
	case Inclusive:
		return "inclusive"
	case Linewise:
		return "linewise"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Operator は、モーションがどのオペレーターの後に使われたかを表す。
// オペレーターの有無で、w の行末の扱い（`:help w` の特例）や、l が行末の位置まで進めるかどうかが変わる。
type Operator int

const (
	// NoOperator は、オペレーターの無い移動を表す。
	NoOperator Operator = iota
	// AnyOperator は、c 以外のオペレーター（d、y など）の後であることを表す。
	AnyOperator
	// ChangeOperator は、c の後であることを表す。cw と cW は ce と cE のように動く（`:help cw`）。
	ChangeOperator
)

// WantEOL は、望みの列が「行末」であることを表す。$ の後の j と k は、各行の最後の文字へ移る。
const WantEOL = math.MaxInt

// Find は、直前の f、F、t、T の検索を表す。; と , はこれを繰り返す。
// Char が 0 のゼロ値は「検索していない」を表す。
type Find struct {
	Char     rune
	Backward bool // F と T のとき true
	Till     bool // t と T のとき true
}

// Columns は、望みの列（Vim の curswant）の数え方である。
//
// 望みの列は本来、表示の列（タブや全角文字の幅を数えた列）で数える。
// 表示幅の計算は internal/ui の担当で、internal/ui は internal/command と同じ層なので、このパッケージからは import できない（基本設計 2 章）。
// このため、数え方を呼び出し側から差し込めるようにする。
// Request.Columns が nil なら、仮の数え方として CharColumns を使う。
type Columns interface {
	// Column は、行 line の byteOff の位置の文字の列を返す。
	Column(line string, byteOff int) int
	// ByteAt は、行 line で列 col にある文字の位置を返す。
	// col が行の幅を超えるときは、最後の文字の位置を返す。空の行では 0 を返す。
	ByteAt(line string, col int) int
}

// CharColumns は、行頭からの文字数を列とする、仮の数え方である。
type CharColumns struct{}

// Column は、行の先頭から byteOff までの文字数を返す。
func (CharColumns) Column(line string, byteOff int) int {
	return charColumn(line, byteOff)
}

// ByteAt は、行の先頭から col 文字目の位置を返す。行の文字数を超える col は最後の文字にする。
func (CharColumns) ByteAt(line string, col int) int {
	return byteOfColumn(line, col)
}

// ScreenLines は、行を画面の幅で折り返して描くときの、見た目の行の数え方である（gj と gk。FR-157）。
//
// 折り返しの位置は表示幅と画面の幅で決まり、その計算は internal/ui の担当なので、Columns と同じく呼び出し側から差し込む。
//
// 折り返すとき、呼び出し側は Request.Columns も、Vim の vcol と同じく、見た目の行の n 番目の行の左の端を n × Width の列とする
// 数え方（全角文字を次の見た目の行へ送った ">" の列と、続きの行の記号の列も数える）にする。gj と gk は、この列の上で
// Vim の nv_screengo と同じ計算をする。
type ScreenLines interface {
	// Wrap は、行を折り返して描くかを返す。false なら gj と gk は j と k と同じに動く。
	Wrap() bool
	// Width は、見た目の行の列数（本文の幅）を返す。
	Width() int
	// BreakWidth は、続きの見た目の行の頭に描く記号（Vim の 'showbreak'）の列数を返す。付けないなら 0 である。
	BreakWidth() int
	// Rows は、行 line を折り返した見た目の行の数（1 以上）を返す。ページ単位のスクロールが行の高さに使う。
	Rows(line string) int
}

// Request は、モーションとテキストオブジェクトへの入力である。
//
// Count と CountGiven は、internal/input の Command の同じ名前の欄をそのまま渡せる形にしてある。
type Request struct {
	// Cursor は、現在のカーソルの位置である。バッファの中の文字の境界を指す必要がある。
	Cursor buffer.Position
	// Count は回数である。省略した場合は 1 を渡す。0 も 1 として扱う。
	Count int
	// CountGiven は、回数を明示したかどうかである。gg、G、% は、回数の省略と 1 で意味が変わる。
	CountGiven bool
	// Char は、f、F、t、T で検索する文字である。
	Char rune
	// Want は、j と k で使う望みの列（Vim の curswant）である。Columns で数える。
	Want int
	// Columns は、望みの列の数え方である。nil なら CharColumns を使う。
	Columns Columns
	// Screen は、見た目の行の数え方である（gj と gk）。nil なら行を折り返さないとして扱う。
	Screen ScreenLines
	// Last は、; と , が繰り返す直前の検索である。
	Last Find
	// Operator は、このモーションの前に入力されたオペレーターである。テキストオブジェクトは見ない。
	Operator Operator
	// Search は、検索のモーションのパターンと向きである。
	// / と ? は Pattern を検索し（向きは見ない）、n と N は Pattern を Backward の向き（N は逆の向き）に検索する。
	// * と # は見ない。
	Search Search
	// SearchSteps は、/ と ? のパターンを打っている間に、<C-g>（次の一致へ）を打った数から <C-t>（前の一致へ）を打った数を引いたものである（FR-153）。
	// / と ? は、回数の分だけ検索した一致から、回数の一致を 1 単位にして、正ならバッファの末尾の方へ、負なら先頭の方へ動かした一致へ移る（FindMatch）。
	// 向き（/ か ?）によらず、正はバッファの末尾の方である。n と N は見ない。
	SearchSteps int
	// CommentString は、位置 at で使う行のコメントの形（設定 comment.string。"// %s" など）を返す関数である（FR-169）。
	// コメントのまとまりのテキストオブジェクト（gc）だけが、カーソルの位置で呼ぶ。nil なら空の形として扱う。
	// 形を決めるのは呼び出し側（internal/command の State.commentString）で、後に tree-sitter の拡張機能でカーソルの位置の言語の形を
	// 使うときのために位置を渡す。そのときに前もって要る口（言語の範囲を先に持っておく口と、別の種類の形を段から決める口）は、基本設計 3.3 にある。
	CommentString func(at buffer.Position) string
	// Selection は、ビジュアルモードの今の選択の範囲（半開区間）である。本体の表のテキストオブジェクトは見ない。
	// 拡張機能のテキストオブジェクトへ渡し（ext の TextObjectResolveParams.Selection）、選択を外へ広げるテキストオブジェクト（tree-sitter の an と in）が使う。
	// ビジュアルモードでないときと、文字単位で 1 文字だけを選んでいるときは nil である（internal/command の State.extSelection）。
	Selection *buffer.Range
}

// count1 は、回数を 1 以上にして返す。
func (r Request) count1() int {
	return max(r.Count, 1)
}

// columns は、望みの列の数え方を返す。
func (r Request) columns() Columns {
	if r.Columns == nil {
		return CharColumns{}
	}
	return r.Columns
}

// Result は、モーションの結果である。
type Result struct {
	// Pos は、移動先の位置である。
	Pos buffer.Position
	// Kind は、オペレーターと組み合わせたときの範囲の扱いである。
	Kind Kind
	// Want は、移動した後の望みの列である。呼び出し側はこの値で望みの列を置き換える。
	// j と k は Request.Want をそのまま返し、$ は WantEOL を返す。
	Want int
	// Find は、f、F、t、T のときに、; と , のために記録する検索である。それ以外のモーションではゼロ値である。
	// f、F、t、T は、文字が見つからずに ErrNoTarget を返すときも Find を返す。
	// Vim と同じく、呼び出し側は失敗した検索も記録する。
	Find Find
	// Search は、検索のモーション（/、?、n、N、*、#）が検索したパターンと向きである。それ以外のモーションではゼロ値である。
	// パターンが見つからずに ErrPatternNotFound を返すときも返す（Vim と同じく、見つからなかったパターンも直前のパターンになる）。
	// 正規表現が正しくないときは返さない。
	Search Search
	// Wrapped は、検索がバッファの端を越えて反対の端から続けたことを表す（Vim の 'wrapscan'）。
	Wrapped bool
}

// Span は、オペレーターが働く範囲、またはテキストオブジェクトの範囲である。
// Start は End より後に無い。
type Span struct {
	Start buffer.Position
	End   buffer.Position
	Kind  Kind
	// ReplaceSelection は、ビジュアルモードで、選択を広げずに、選択をこの範囲で置き換えるかどうかである。
	// 拡張機能のテキストオブジェクトが Request.Selection に答えたときだけ true になる（ext の TextObjectResolveResult.ReplaceSelection）。
	ReplaceSelection bool
}

// Range は、範囲を、buffer.Slice と buffer.Edit で使える半開区間に直して返す。
//
//   - Exclusive は、Start から End の直前まで
//   - Inclusive は、Start から End の文字の直後まで（End の文字に続く結合文字も含める）。End が行末の位置なら、改行は含めず行末まで
//   - Linewise は、Start の行の先頭から End の行の行末まで。行の後の改行をどう扱うかは、オペレーターが決める
func (s Span) Range(b *buffer.Buffer) buffer.Range {
	switch s.Kind {
	case Exclusive:
	case Inclusive:
		line := b.Line(s.End.Line)
		if s.End.Byte < len(line) {
			return buffer.Range{Start: s.Start, End: buffer.Position{Line: s.End.Line, Byte: nextCell(line, s.End.Byte)}}
		}
	case Linewise:
		return buffer.Range{
			Start: buffer.Position{Line: s.Start.Line},
			End:   buffer.Position{Line: s.End.Line, Byte: len(b.Line(s.End.Line))},
		}
	}
	return buffer.Range{Start: s.Start, End: s.End}
}

// MotionFunc は、モーション 1 つを解決する関数である。
// 該当が無い場合（f で文字が無い、% で括弧が無い、など）は ErrNoTarget を返す。
// ErrNoTarget を返すときの Result は、Find の欄だけが意味を持つ。
type MotionFunc func(b *buffer.Buffer, req Request) (Result, error)

// TextObjectFunc は、テキストオブジェクト 1 つを解決する関数である。
// 返す範囲は AdjustExclusive を適用する前のものである。
type TextObjectFunc func(b *buffer.Buffer, req Request) (Span, error)

// ErrNoTarget は、モーションの移動先、またはテキストオブジェクトの範囲が見つからないことを表す。
var ErrNoTarget = errors.New("target: no target")

// ErrInvalidRequest は、Request の Cursor がバッファの中の文字の境界を指していないか、Count が負であることを表す。
var ErrInvalidRequest = errors.New("target: invalid request")

// motions は、モーションの ID と、それを解決する関数の表である。
// ID は internal/input の本体の操作の表（builtin.go）の ID と同じで、Command.Target.Ref.ID でそのまま引ける。
// 1 つのモーションを 1 つの関数にし、対象の増減はこの表の行の増減で行う。
//
// req: FR-03
var motions = map[string]MotionFunc{
	"left":                left,                   // h
	"right":               right,                  // l
	"down":                down,                   // j
	"up":                  up,                     // k
	"display-down":        displayVertical(true),  // gj
	"display-up":          displayVertical(false), // gk
	"word-next":           wordForward(false),     // w
	"bigword-next":        wordForward(true),      // W
	"word-prev":           wordBackward(false),    // b
	"bigword-prev":        wordBackward(true),     // B
	"word-end":            wordEnd(false),         // e
	"bigword-end":         wordEnd(true),          // E
	"line-start":          lineStart,              // 0
	"line-first-nonblank": firstNonBlank,          // ^
	"line-end":            lineEnd,                // $
	"file-start":          gotoLine(false),        // gg
	"file-end":            gotoLine(true),         // G
	"find-forward":        findChar(false, false), // f
	"find-backward":       findChar(true, false),  // F
	"till-forward":        findChar(false, true),  // t
	"till-backward":       findChar(true, true),   // T
	"find-repeat":         repeatFind(false),      // ;
	"find-repeat-reverse": repeatFind(true),       // ,
	"match-pair":          percent,                // %
	"paragraph-prev":      paragraph(false),       // {
	"paragraph-next":      paragraph(true),        // }
	"search-forward":      searchMotion(false),    // /
	"search-backward":     searchMotion(true),     // ?
	"search-next":         searchRepeat(false),    // n
	"search-prev":         searchRepeat(true),     // N
}

// textObjects は、テキストオブジェクトの ID と、それを解決する関数の表である。
// ID は motions と同じく internal/input の表の ID である。
// Vim の別名（ib、ab、iB、aB、i)、a] など）は、キーの側（internal/input の表）で同じ ID に割り当てる。
//
// req: FR-03
var textObjects = map[string]TextObjectFunc{
	"inner-word":         word(false, false),     // iw
	"a-word":             word(true, false),      // aw
	"inner-bigword":      word(false, true),      // iW
	"a-bigword":          word(true, true),       // aW
	"inner-double-quote": quote(false, '"'),      // i"
	"a-double-quote":     quote(true, '"'),       // a"
	"inner-single-quote": quote(false, '\''),     // i'
	"a-single-quote":     quote(true, '\''),      // a'
	"inner-paren":        block(false, '(', ')'), // i( i) ib
	"a-paren":            block(true, '(', ')'),  // a( a) ab
	"inner-bracket":      block(false, '[', ']'), // i[ i]
	"a-bracket":          block(true, '[', ']'),  // a[ a]
	"inner-brace":        block(false, '{', '}'), // i{ i} iB
	"a-brace":            block(true, '{', '}'),  // a{ a} aB
	"inner-paragraph":    paragraphObject(false), // ip
	"a-paragraph":        paragraphObject(true),  // ap
	"commented-lines":    commentedLines,         // オペレーターの後の gc
}

// LookupMotion は、ID が name のモーションを返す。
// 返す関数は、Request を確かめてから解決し、正しくなければ ErrInvalidRequest を返す。
func LookupMotion(name string) (MotionFunc, bool) {
	f, ok := motions[name]
	if !ok {
		return nil, false
	}
	return func(b *buffer.Buffer, req Request) (Result, error) {
		if err := validate(b, req); err != nil {
			return Result{}, err
		}
		return f(b, req)
	}, true
}

// LookupTextObject は、ID が name のテキストオブジェクトを返す。
// 返す関数は、Request を確かめてから解決し、正しくなければ ErrInvalidRequest を返す。
func LookupTextObject(name string) (TextObjectFunc, bool) {
	f, ok := textObjects[name]
	if !ok {
		return nil, false
	}
	return func(b *buffer.Buffer, req Request) (Span, error) {
		if err := validate(b, req); err != nil {
			return Span{}, err
		}
		return f(b, req)
	}, true
}

// MotionNames は、表にあるモーションの ID を並べ替えて返す。
func MotionNames() []string {
	return sortedKeys(motions)
}

// TextObjectNames は、表にあるテキストオブジェクトの ID を並べ替えて返す。
func TextObjectNames() []string {
	return sortedKeys(textObjects)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// validate は、Cursor がバッファの中の文字の境界を指し、Count が負でないことを確かめる。
func validate(b *buffer.Buffer, req Request) error {
	if req.Count < 0 {
		return fmt.Errorf("%w: negative count %d", ErrInvalidRequest, req.Count)
	}
	if _, err := b.Slice(buffer.Range{Start: req.Cursor, End: req.Cursor}); err != nil {
		return fmt.Errorf("%w: cursor: %w", ErrInvalidRequest, err)
	}
	return nil
}

// OperatorSpan は、from（オペレーターを入力したときのカーソル）からモーションの結果 res までを、オペレーターが働く範囲にして返す。
// 始まりと終わりを並べ替えてから、AdjustExclusive を適用する。
//
// req: FR-03
func OperatorSpan(b *buffer.Buffer, from buffer.Position, res Result) Span {
	start, end := from, res.Pos
	if end.Less(start) {
		start, end = end, start
	}
	return AdjustExclusive(b, Span{Start: start, End: end, Kind: res.Kind})
}

// AdjustExclusive は、`:help exclusive` の 2 つの規則を、オペレーターが働く範囲に適用する。
//
//  1. Exclusive で、終わりが行の先頭にあり、範囲が 2 行以上にわたる場合、終わりを前の行の最後の文字へ移して Inclusive にする
//  2. 1 の場合に、始まりが行の最初の空白でない文字かそれより前（字下げの中）にあれば、Linewise にする
//
// 例えば d} は、段落の後の空の行の先頭までの Exclusive な範囲になり、2 の規則で行単位の削除になる。
// 前の行が空なら、終わりはその空の行の先頭で Exclusive のままにする（Vim と同じ）。
//
// req: FR-03
func AdjustExclusive(b *buffer.Buffer, s Span) Span {
	if s.Kind != Exclusive || s.End.Byte != 0 || s.End.Line <= s.Start.Line {
		return s
	}
	s.End.Line--
	if strings.Trim(b.Line(s.Start.Line)[:s.Start.Byte], " \t") == "" {
		s.Kind = Linewise
		return s
	}
	line := b.Line(s.End.Line)
	s.End.Byte = len(line)
	if len(line) > 0 {
		s.End.Byte = lastCharByte(line)
		s.Kind = Inclusive
	}
	return s
}
