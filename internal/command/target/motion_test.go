package target

import (
	"errors"
	"testing"
)

// motionCase は、モーション 1 回の入力と期待する結果である。
// text と want は、"|" でカーソルの位置を示す。want が空なら ErrNoTarget を期待する。
type motionCase struct {
	motion string
	text   string
	count  int
	want   string
	kind   Kind
	op     Operator
	char   rune // f F t T の文字
	last   Find // ; , の直前の検索
	wantCo int  // j k の望みの列
}

// motionTests は、TestMotions の表である。
var motionTests = []motionCase{
	// h
	{motion: "h", text: "ab|c", want: "a|bc", kind: Exclusive},
	{motion: "h", text: "a|bc", count: 5, want: "|abc", kind: Exclusive},
	{motion: "h", text: "あい|う", want: "あ|いう", kind: Exclusive},
	{motion: "h", text: "|abc"},
	{motion: "h", text: "ab\n|cd"}, // 前の行へは移らない
	{motion: "h", text: "|"},
	{motion: "h", text: "|abc", op: AnyOperator, want: "|abc", kind: Exclusive}, // オペレーターの後は失敗しない（Vim の yh、ch）
	{motion: "h", text: "|", op: ChangeOperator, want: "|", kind: Exclusive},

	// l
	{motion: "l", text: "|abc", want: "a|bc", kind: Exclusive},
	{motion: "l", text: "|abc", count: 5, want: "ab|c", kind: Exclusive},
	{motion: "l", text: "|abc", count: 5, op: AnyOperator, want: "abc|", kind: Exclusive},
	{motion: "l", text: "|あいう", want: "あ|いう", kind: Exclusive},
	{motion: "l", text: "ab|c"},
	{motion: "l", text: "ab|c\nd"}, // 次の行へは移らない
	{motion: "l", text: "|"},
	{motion: "l", text: "|", op: AnyOperator, want: "|", kind: Exclusive}, // オペレーターの後は失敗しない（Vim の cl、dl）

	// j k
	{motion: "j", text: "|abc\nde", want: "abc\n|de", kind: Linewise},
	{motion: "j", text: "|abc\nde", wantCo: 2, want: "abc\nd|e", kind: Linewise},
	{motion: "j", text: "|あいう\nかきく", wantCo: 2, want: "あいう\nかき|く", kind: Linewise},
	{motion: "j", text: "|abc\nde", wantCo: WantEOL, want: "abc\nd|e", kind: Linewise},
	{motion: "j", text: "|abc\n\nx", wantCo: 2, want: "abc\n|\nx", kind: Linewise},
	{motion: "j", text: "|a\nb\nc", count: 5, want: "a\nb\n|c", kind: Linewise},
	{motion: "j", text: "a\n|b"},
	{motion: "j", text: "|"},
	{motion: "k", text: "ab\nc|d", wantCo: 1, want: "a|b\ncd", kind: Linewise},
	{motion: "k", text: "a\nb\n|c", count: 5, want: "|a\nb\nc", kind: Linewise},
	{motion: "k", text: "|ab\ncd"},

	// w W
	{motion: "w", text: "|foo bar", want: "foo |bar", kind: Exclusive},
	{motion: "w", text: "|foo.bar", want: "foo|.bar", kind: Exclusive},
	{motion: "w", text: "|a b c d", count: 3, want: "a b c |d", kind: Exclusive},
	{motion: "w", text: "|foo\n  bar", want: "foo\n  |bar", kind: Exclusive},
	{motion: "w", text: "|foo\n\nbar", want: "foo\n|\nbar", kind: Exclusive}, // 空の行に止まる
	{motion: "w", text: "foo\n|\nbar", want: "foo\n\n|bar", kind: Exclusive},
	{motion: "w", text: "|日本語 abc", want: "日本語 |abc", kind: Exclusive},
	{motion: "w", text: "|あ.い", want: "あ|.い", kind: Exclusive},
	{motion: "w", text: "foo |bar", want: "foo ba|r", kind: Inclusive}, // 最後の単語からは最後の文字へ
	{motion: "w", text: "foo ba|r"},
	{motion: "e", text: "a |b\nc", count: 3, want: "a b\n|c", kind: Inclusive}, // 回数の途中で末尾に着いても、動けば成功
	{motion: "w", text: "|"},
	{motion: "w", text: "|a b\nc d", count: 2, op: AnyOperator, want: "a |b\nc d", kind: Inclusive}, // d2w は行末を越えない
	{motion: "W", text: "|foo.bar baz", want: "foo.bar |baz", kind: Exclusive},
	{motion: "w", text: "a |b\n  c", want: "a b\n  |c", kind: Exclusive},
	{motion: "w", text: "a |b\n  c", count: 2, want: "a b\n  |c", kind: Inclusive}, // 回数の途中で末尾に着いても、動けば成功
	{motion: "w", text: "|a\n\n\nb", count: 2, want: "a\n\n|\nb", kind: Exclusive},
	{motion: "w", text: "|a\tb", want: "a\t|b", kind: Exclusive},
	{motion: "W", text: "|a.b c.d e", count: 2, want: "a.b c.d |e", kind: Exclusive},

	// ASCII 以外の文字は Vim の utf_class() の分類で区切る（期待値は Vim 9.1 で確かめた）
	{motion: "w", text: "|これは、テストです。", want: "これは|、テストです。", kind: Exclusive},
	{motion: "w", text: "|漢字かなカナ abc", want: "漢字|かなカナ abc", kind: Exclusive},
	{motion: "w", text: "|東京タワーは333mです", count: 3, want: "東京タワーは|333mです", kind: Exclusive},
	{motion: "w", text: "日本|　語 x", want: "日本　|語 x", kind: Exclusive},     // 全角空白は空白
	{motion: "w", text: "|a\u00a0b", want: "a\u00a0|b", kind: Exclusive}, // NBSP も空白
	{motion: "w", text: "|ＡＢＣ１２３！？", want: "ＡＢＣ１２３|！？", kind: Exclusive},
	{motion: "w", text: "|絵文字😀😀テスト", want: "絵文字|😀😀テスト", kind: Exclusive},
	{motion: "W", text: "|これは、テスト です", want: "これは、テスト |です", kind: Exclusive},
	{motion: "W", text: "|日本　語", want: "日本　|語", kind: Exclusive}, // W でも全角空白は空白
	{motion: "b", text: "これは、テス|ト", want: "これは、|テスト", kind: Exclusive},
	{motion: "b", text: "「こんにちは|」と", want: "「|こんにちは」と", kind: Exclusive},
	{motion: "e", text: "|これは、テスト", want: "これ|は、テスト", kind: Inclusive},
	{motion: "e", text: "|한국어 텍스트", want: "한국|어 텍스트", kind: Inclusive},

	// 結合文字は前の文字と合わせて 1 文字、不正な UTF-8 のバイトはその値で分類する（期待値は Vim 9.1 で確かめた）
	{motion: "w", text: "|カ\u3099イト\u3099 x", want: "カ\u3099イト\u3099 |x", kind: Exclusive},                     // NFD の「ガイド」
	{motion: "w", text: "|カ\u3099イト\u3099 x", op: ChangeOperator, want: "カ\u3099イ|ト\u3099 x", kind: Inclusive}, // cw は ce
	{motion: "w", text: "|\u2764\ufe0fabc d", want: "\u2764\ufe0f|abc d", kind: Exclusive},
	{motion: "w", text: "|a \u0301b c", want: "a \u0301|b c", kind: Exclusive}, // 空白の後の結合文字は空白
	{motion: "e", text: "|cafe\u0301 x", want: "caf|e\u0301 x", kind: Inclusive},
	{motion: "e", text: "caf|e\u0301 x", want: "cafe\u0301 |x", kind: Inclusive},
	{motion: "w", text: "|foo\x80bar baz", want: "foo|\x80bar baz", kind: Exclusive}, // 0x80 は記号
	{motion: "w", text: "|foo\xe3bar baz", want: "foo\xe3bar |baz", kind: Exclusive}, // 0xe3 は単語の文字
	{motion: "w", text: "|\xffabc d", want: "\xffabc |d", kind: Exclusive},
	{motion: "l", text: "|カ\u3099イ", want: "カ\u3099|イ", kind: Exclusive},
	{motion: "h", text: "カ\u3099|イ", want: "|カ\u3099イ", kind: Exclusive},
	{motion: "$", text: "|カ\u3099イト\u3099", want: "カ\u3099イ|ト\u3099", kind: Inclusive},

	// b B
	{motion: "b", text: "foo |bar", want: "|foo bar", kind: Exclusive},
	{motion: "b", text: "foo.b|ar", want: "foo.|bar", kind: Exclusive},
	{motion: "b", text: "foo\n|bar", want: "|foo\nbar", kind: Exclusive},
	{motion: "b", text: "foo\n\n|bar", want: "foo\n|\nbar", kind: Exclusive},
	{motion: "b", text: "a b c |d", count: 2, want: "a |b c d", kind: Exclusive},
	{motion: "b", text: "a b |c", count: 9, want: "|a b c", kind: Exclusive},
	{motion: "b", text: "日本 語|x", want: "日本 |語x", kind: Exclusive},
	{motion: "b", text: "|foo"},
	{motion: "b", text: "|"},
	{motion: "B", text: "foo.b|ar", want: "|foo.bar", kind: Exclusive},

	// e E
	{motion: "e", text: "|foo bar", want: "fo|o bar", kind: Inclusive},
	{motion: "e", text: "fo|o bar", want: "foo ba|r", kind: Inclusive},
	{motion: "e", text: "fo|o\n\nbar", want: "foo\n\nba|r", kind: Inclusive}, // 空の行には止まらない
	{motion: "e", text: "|a b c", count: 2, want: "a b |c", kind: Inclusive}, // a は既に単語の終わり
	{motion: "e", text: "|日本語 x", want: "日本|語 x", kind: Inclusive},
	{motion: "e", text: "foo ba|r"},
	{motion: "e", text: "|"},
	{motion: "E", text: "|foo.bar baz", want: "foo.ba|r baz", kind: Inclusive},

	// 0 ^ $
	{motion: "0", text: "ab|c", want: "|abc", kind: Exclusive},
	{motion: "0", text: "|", want: "|", kind: Exclusive},
	{motion: "^", text: "  a|bc", want: "  |abc", kind: Exclusive},
	{motion: "^", text: "|   ", want: "  | ", kind: Exclusive}, // 空白だけの行は最後の文字
	{motion: "^", text: "|", want: "|", kind: Exclusive},
	{motion: "$", text: "|abc", want: "ab|c", kind: Inclusive},
	{motion: "$", text: "|あい", want: "あ|い", kind: Inclusive},
	{motion: "$", text: "|ab\ncd", count: 2, want: "ab\nc|d", kind: Inclusive},
	{motion: "$", text: "|", want: "|", kind: Inclusive},
	{motion: "$", text: "|ab\ncd", count: 3, want: "ab\nc|d", kind: Inclusive}, // 最後の行で止める
	{motion: "$", text: "ab\n|cd", count: 2},

	// gg G
	{motion: "gg", text: "  ab\nc|d", want: "  |ab\ncd", kind: Linewise},
	{motion: "gg", text: "|ab\n  cd\nef", count: 2, want: "ab\n  |cd\nef", kind: Linewise},
	{motion: "gg", text: "|ab\ncd", count: 9, want: "ab\n|cd", kind: Linewise},
	{motion: "gg", text: "|", want: "|", kind: Linewise},
	{motion: "G", text: "|ab\n  cd", want: "ab\n  |cd", kind: Linewise},
	{motion: "G", text: "ab\n|cd", count: 1, want: "|ab\ncd", kind: Linewise},

	// f F t T
	{motion: "f", char: 'b', text: "|abcb", want: "a|bcb", kind: Inclusive},
	{motion: "f", char: 'b', text: "|abcb", count: 2, want: "abc|b", kind: Inclusive},
	{motion: "f", char: 'a', text: "|aa", want: "a|a", kind: Inclusive}, // カーソルの位置の文字は数えない
	{motion: "f", char: 'う', text: "|あいう", want: "あい|う", kind: Inclusive},
	{motion: "f", char: 'x', text: "|abc"},
	{motion: "f", char: 'b', text: "|abc", count: 2},
	{motion: "f", char: 'c', text: "ab|c"},
	{motion: "f", char: 'b', text: "|a\nb"}, // 次の行は探さない
	{motion: "f", char: 'a', text: "|"},
	{motion: "F", char: 'b', text: "abc|b", want: "a|bcb", kind: Exclusive},
	{motion: "F", char: 'あ', text: "あい|う", want: "|あいう", kind: Exclusive},
	{motion: "F", char: 'x', text: "ab|c"},
	{motion: "t", char: 'c', text: "|abcb", want: "a|bcb", kind: Inclusive},
	{motion: "t", char: 'b', text: "|ab", want: "|ab", kind: Inclusive}, // すぐ隣なら動かない
	{motion: "t", char: 'う', text: "|あいう", want: "あ|いう", kind: Inclusive},
	{motion: "T", char: 'a', text: "abc|d", want: "a|bcd", kind: Exclusive},
	{motion: "T", char: 'あ', text: "あい|う", want: "あ|いう", kind: Exclusive},

	// ; ,
	{motion: ";", last: Find{Char: 'b'}, text: "a|bcb", want: "abc|b", kind: Inclusive},
	{motion: ";", last: Find{Char: '.', Till: true}, text: "|a.b.c", want: "a.|b.c", kind: Inclusive}, // t の後の ; は隣を飛ばす
	{motion: ";", last: Find{Char: '.', Till: true}, text: "|a.b.c", count: 2, want: "a.|b.c", kind: Inclusive},
	{motion: ";", last: Find{Char: 'b', Backward: true}, text: "abc|b", want: "a|bcb", kind: Exclusive},
	{motion: ",", last: Find{Char: 'b'}, text: "abc|b", want: "a|bcb", kind: Exclusive},
	{motion: ",", last: Find{Char: 'b', Backward: true}, text: "a|bcb", want: "abc|b", kind: Inclusive},
	{motion: ";", text: "|abc"}, // 直前の検索が無い
	{motion: ";", last: Find{Char: 'x'}, text: "|abc"},

	// %
	{motion: "%", text: "|(a(b)c)", want: "(a(b)c|)", kind: Inclusive},
	{motion: "%", text: "(a(b)c|)", want: "|(a(b)c)", kind: Inclusive},
	{motion: "%", text: "(a|(b)c)", want: "(a(b|)c)", kind: Inclusive},
	{motion: "%", text: "x|y[a]", want: "xy[a|]", kind: Inclusive}, // 後ろの括弧を探す
	{motion: "%", text: "|{\n  x\n}", want: "{\n  x\n|}", kind: Inclusive},
	{motion: "%", text: "あ|い(う)", want: "あい(う|)", kind: Inclusive},
	{motion: "%", text: "(a \\(| b)", want: "|(a \\( b)", kind: Inclusive},         // エスケープした括弧は数えない
	{motion: "%", text: "|\\(a (b) c\\)", want: "\\(a (b) c\\|)", kind: Inclusive}, // エスケープした括弧どうしを対にする
	{motion: "%", text: "|abc"},
	{motion: "%", text: "|(abc"},
	{motion: "%", text: "(|a"},
	{motion: "%", text: "|"},
	{motion: "%", text: "|a\nb\n  c\nd", count: 50, want: "a\n|b\n  c\nd", kind: Linewise},
	{motion: "%", text: "|a\nb\n  c\nd", count: 75, want: "a\nb\n  |c\nd", kind: Linewise},
	{motion: "%", text: "|a\nb", count: 101},

	// { }
	{motion: "}", text: "|a\nb\n\nc", want: "a\nb\n|\nc", kind: Exclusive},
	{motion: "}", text: "|a\nb\n\nc", count: 2, want: "a\nb\n\n|c", kind: Inclusive}, // 最後の行は最後の文字
	{motion: "}", text: "|a\nb\n\nc", count: 3},
	{motion: "}", text: "|a\n  \nb", want: "a\n  \n|b", kind: Inclusive}, // 空白だけの行は境界にしない
	{motion: "}", text: "|", want: "|", kind: Exclusive},
	{motion: "}", text: "a\n|bc", want: "a\nb|c", kind: Inclusive},
	{motion: "{", text: "a\n\n|b", want: "a\n|\nb", kind: Exclusive},
	{motion: "{", text: "a\n\nb\n\n|c", count: 2, want: "a\n|\nb\n\nc", kind: Exclusive},
	{motion: "{", text: "a\n|b", want: "|a\nb", kind: Exclusive},
	{motion: "{", text: "|a", want: "|a", kind: Exclusive},
	{motion: "{", text: "a\n\n|b", count: 3},
}

// req: FR-03
func TestMotions(t *testing.T) {
	for _, tt := range motionTests {
		b, cur := parse(t, tt.text)
		f, ok := LookupMotion(motionID(t, tt.motion))
		if !ok {
			t.Fatalf("motion %q not found", tt.motion)
		}
		n, given := count(tt.count)
		req := Request{Cursor: cur, Count: n, CountGiven: given, Operator: tt.op, Char: tt.char, Last: tt.last, Want: tt.wantCo}
		res, err := f(b, req)
		if tt.want == "" {
			if !errors.Is(err, ErrNoTarget) {
				t.Errorf("%d%s on %q: got %q, %v, want ErrNoTarget", tt.count, tt.motion, tt.text, mark(b, res.Pos), err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%d%s on %q: error = %v", tt.count, tt.motion, tt.text, err)
			continue
		}
		if got := mark(b, res.Pos); got != tt.want || res.Kind != tt.kind {
			t.Errorf("%d%s on %q (op %d) = %q (%v), want %q (%v)", tt.count, tt.motion, tt.text, tt.op, got, res.Kind, tt.want, tt.kind)
		}
	}
}

// req: FR-03
func TestMotionWantAndFind(t *testing.T) {
	tests := []struct {
		motion   string
		text     string
		req      Request
		wantWant int
		wantFind Find
	}{
		{motion: "l", text: "あ|いう", wantWant: 2},
		{motion: "$", text: "|abc", wantWant: WantEOL},
		{motion: "j", text: "|abc\nd", req: Request{Want: 7}, wantWant: 7}, // j と k は望みの列を変えない
		{motion: "k", text: "abc\n|d", req: Request{Want: WantEOL}, wantWant: WantEOL},
		{motion: "w", text: "|ab cd", wantWant: 3},
		{motion: "G", text: "|a\n  b", wantWant: 2},
		{motion: "f", text: "|abc", req: Request{Char: 'c'}, wantWant: 2, wantFind: Find{Char: 'c'}},
		{motion: "T", text: "ab|c", req: Request{Char: 'a'}, wantWant: 1, wantFind: Find{Char: 'a', Backward: true, Till: true}},
		{motion: ";", text: "|abc", req: Request{Last: Find{Char: 'c'}}, wantWant: 2}, // ; は記録を変えない
	}
	for _, tt := range tests {
		b, cur := parse(t, tt.text)
		f, _ := LookupMotion(motionID(t, tt.motion))
		tt.req.Cursor = cur
		res, err := f(b, tt.req)
		if err != nil {
			t.Errorf("%s on %q: error = %v", tt.motion, tt.text, err)
			continue
		}
		if res.Want != tt.wantWant || res.Find != tt.wantFind {
			t.Errorf("%s on %q: Want = %d, Find = %+v, want %d, %+v", tt.motion, tt.text, res.Want, res.Find, tt.wantWant, tt.wantFind)
		}
	}
}

// req: FR-03
func TestFailedFindIsRecorded(t *testing.T) {
	// Vim と同じく、見つからなかった f も ; のために記録する。
	b, cur := parse(t, "|abc")
	f, _ := LookupMotion("find-forward")
	res, err := f(b, Request{Cursor: cur, Count: 1, Char: 'z'})
	if !errors.Is(err, ErrNoTarget) || res.Find != (Find{Char: 'z'}) {
		t.Errorf("fz = %+v, %v, want Find{z} and ErrNoTarget", res, err)
	}
}

// req: FR-03
func TestCountGiven(t *testing.T) {
	// internal/input の Command は回数の省略を Count 1、CountGiven false で表す。G はこれを最後の行への移動として扱う。
	b, cur := parse(t, "a\nb\n|c")
	tests := []struct {
		id    string
		req   Request
		want  string
		fails bool
	}{
		{id: "file-end", req: Request{Count: 1}, want: "a\nb\n|c"},
		{id: "file-end", req: Request{Count: 1, CountGiven: true}, want: "|a\nb\nc"},
		{id: "file-start", req: Request{Count: 2, CountGiven: true}, want: "a\n|b\nc"},
		{id: "match-pair", req: Request{Count: 1}, fails: true}, // 回数の省略は括弧の対応（括弧が無い）
		{id: "match-pair", req: Request{Count: 1, CountGiven: true}, want: "|a\nb\nc"},
		{id: "down", req: Request{Count: 0}, fails: true}, // 0 も 1 として扱う。最後の行なので失敗する
	}
	for _, tt := range tests {
		f, _ := LookupMotion(tt.id)
		tt.req.Cursor = cur
		res, err := f(b, tt.req)
		if tt.fails {
			if !errors.Is(err, ErrNoTarget) {
				t.Errorf("%s %+v: error = %v, want ErrNoTarget", tt.id, tt.req, err)
			}
			continue
		}
		if err != nil || mark(b, res.Pos) != tt.want {
			t.Errorf("%s %+v = %q, %v, want %q", tt.id, tt.req, mark(b, res.Pos), err, tt.want)
		}
	}
}

// wideColumns は、ASCII 以外の文字の幅を 2 として数える、テスト用の表示の列の数え方である。
type wideColumns struct{}

func (wideColumns) Column(_ int, line string, byteOff int) int {
	col := 0
	for _, r := range line[:byteOff] {
		col += width(r)
	}
	return col
}

func (wideColumns) ByteAt(_ int, line string, col int) int {
	c := 0
	for off, r := range line {
		if c+width(r) > col {
			return off
		}
		c += width(r)
	}
	return lastCharByte(line)
}

func width(r rune) int {
	if r < 0x80 {
		return 1
	}
	return 2
}

// req: FR-03
func TestColumns(t *testing.T) {
	// 望みの列の数え方を差し込むと、j と k はその列で移り、他のモーションはその列を返す。
	b, cur := parse(t, "abc|d\nあいう")
	j, _ := LookupMotion("down")
	res, err := j(b, Request{Cursor: cur, Count: 1, Want: 3, Columns: wideColumns{}})
	if err != nil || mark(b, res.Pos) != "abcd\nあ|いう" || res.Want != 3 {
		t.Errorf("j = %q, Want %d, %v", mark(b, res.Pos), res.Want, err)
	}
	b, cur = parse(t, "|あいう")
	l, _ := LookupMotion("right")
	res, err = l(b, Request{Cursor: cur, Count: 2, Columns: wideColumns{}})
	if err != nil || res.Want != 4 {
		t.Errorf("2l Want = %d, %v, want 4", res.Want, err)
	}
	res, err = l(b, Request{Cursor: cur, Count: 2})
	if err != nil || res.Want != 2 {
		t.Errorf("2l with default columns Want = %d, %v, want 2", res.Want, err)
	}
}
