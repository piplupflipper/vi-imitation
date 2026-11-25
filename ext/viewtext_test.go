package ext

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// textModel は、テストで書き足しを当てる中身である。本体（internal/editor）と写し（ext/server の TextLog）と同じ手順で当てる。
type textModel struct {
	lines []string
	spans [][]ViewSpan
	eol   bool
}

func newTextModel(text string) *textModel {
	lines, eol := SplitViewText(text)
	return &textModel{lines: lines, spans: make([][]ViewSpan, len(lines)), eol: eol}
}

func (m *textModel) append(text string, hs []ViewSpan) {
	last := len(m.lines) - 1
	lines, spans, eol := AppendViewText(m.lines[last], m.spans[last], m.eol, text, hs)
	if !m.eol {
		m.lines, m.spans = m.lines[:last], m.spans[:last]
	}
	m.lines = append(m.lines, lines...)
	m.spans = append(m.spans, spans...)
	m.eol = eol
	if d := ViewTextOverflow(len(m.lines)); d > 0 {
		m.lines, m.spans = m.lines[d:], m.spans[d:]
	}
}

// styles は、中身の各バイトの色の名前を行ごとに返す。範囲の切り方が違っても、同じ色の付き方なら等しくなる。
func (m *textModel) styles() [][]string {
	out := make([][]string, len(m.lines))
	for i, l := range m.lines {
		out[i] = make([]string, len(l))
		for _, s := range m.spans[i] {
			for b := s.Start; b < s.End; b++ {
				out[i][b] = s.Style
			}
		}
	}
	return out
}

func (m *textModel) equal(n *textModel) bool {
	if !slices.Equal(m.lines, n.lines) || m.eol != n.eol {
		return false
	}
	a, b := m.styles(), n.styles()
	for i := range a {
		if !slices.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// 中身は Vim と同じく行を数え、末尾の改行は 1 つだけを eol として扱う。
//
// req: FR-272
func TestSplitViewText(t *testing.T) {
	for _, tc := range []struct {
		text  string
		lines []string
		eol   bool
	}{
		{"", []string{""}, false},
		{"\n", []string{""}, true},
		{"a\nb\n", []string{"a", "b"}, true},
		{"a\nb", []string{"a", "b"}, false},
		{"a\n\n", []string{"a", ""}, true},
	} {
		lines, eol := SplitViewText(tc.text)
		if !slices.Equal(lines, tc.lines) || eol != tc.eol {
			t.Errorf("SplitViewText(%q) = %q %v, want %q %v", tc.text, lines, eol, tc.lines, tc.eol)
		}
		if got := JoinViewText(lines, eol); got != tc.text {
			t.Errorf("JoinViewText(SplitViewText(%q)) = %q", tc.text, got)
		}
	}
}

// 書き足しは、改行で終わっていない最後の行に続け、改行で終わっていれば新しい行から始める。
// 色の範囲は改行と 1 行の上限で切り分け、1 行の上限は UTF-8 の文字の境目で分ける。
//
// req: FR-272
func TestAppendViewText(t *testing.T) {
	m := newTextModel("a\nb")
	m.append("c\nd\n", []ViewSpan{{Start: 0, End: 3, Style: "x"}})
	if want := []string{"a", "bc", "d"}; !slices.Equal(m.lines, want) || !m.eol {
		t.Fatalf("lines = %q eol %v", m.lines, m.eol)
	}
	// "c\nd" の範囲は、"bc" の c と "d" に分かれる。
	if !slices.Equal(m.spans[1], []ViewSpan{{Start: 1, End: 2, Style: "x"}}) || !slices.Equal(m.spans[2], []ViewSpan{{Start: 0, End: 1, Style: "x"}}) {
		t.Errorf("spans = %v", m.spans)
	}
	m.append("e", nil)
	if want := []string{"a", "bc", "d", "e"}; !slices.Equal(m.lines, want) || m.eol {
		t.Fatalf("lines = %q eol %v", m.lines, m.eol)
	}
	m.append("", nil)
	if len(m.lines) != 4 {
		t.Errorf("empty append changed lines: %q", m.lines)
	}
	m.append("\n\n", nil)
	if want := []string{"a", "bc", "d", "e", ""}; !slices.Equal(m.lines, want) || !m.eol {
		t.Errorf("lines = %q eol %v", m.lines, m.eol)
	}

	// 1 行の上限を超えたら次の行へ送る。3 バイトの文字が上限をまたぐときは、その文字の前で分ける。
	long := strings.Repeat("a", ViewTextMaxLineBytes-1) + "あ" + "z"
	l := newTextModel("")
	l.append(long, nil)
	if len(l.lines) != 2 || l.lines[0] != strings.Repeat("a", ViewTextMaxLineBytes-1) || l.lines[1] != "あz" {
		t.Errorf("long line split = %d lines, %d bytes", len(l.lines), len(l.lines[0]))
	}
}

// 書き足しをどこで区切って送っても、つないで 1 回で送っても、中身と色の付き方は同じになる（本体と写しが食い違わない）。
//
// req: FR-272
func TestAppendViewTextChunks(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // 再現できる試験の入力を作る
	pieces := []string{"a", "bc", "\n", "あい", "\n\n", strings.Repeat("x", 3000), "é"}
	for range 200 {
		var sb strings.Builder
		var spans []ViewSpan
		for range r.IntN(20) {
			p := pieces[r.IntN(len(pieces))]
			if r.IntN(2) == 0 {
				spans = append(spans, ViewSpan{Start: sb.Len(), End: sb.Len() + len(p), Style: "s"})
			}
			sb.WriteString(p)
		}
		text := sb.String()
		start := []string{"", "x", "x\n"}[r.IntN(3)]
		whole := newTextModel(start)
		whole.append(text, spans)
		chunked := newTextModel(start)
		for pos := 0; pos < len(text); {
			n := min(1+r.IntN(5000), len(text)-pos)
			for n < len(text)-pos && !utf8RuneStart(text[pos+n]) {
				n++
			}
			chunked.append(text[pos:pos+n], clipSpans(spans, pos, pos+n))
			pos += n
		}
		if !whole.equal(chunked) {
			t.Fatalf("chunked append differs: start %q text %q\nwhole %q\nchunked %q", start, text, whole.lines, chunked.lines)
		}
	}
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// 取っておく列で同じ部品への書き足しをつないだ 1 件を当てた結果は、つながずに順に当てた結果と同じである。
// つないだテキストが行数の上限を超えて古い行を捨てても同じになる（PR #401 のレビューの必須 4）。
// 取っておく列でつなぐのは基本設計 1.4 と 5.3 の取り決めで、FR-272 の文の振る舞いではないので、req の注釈は付けない。
func TestViewAppendMerge(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // 再現できる試験の入力を作る
	lines := func(n int, tag string) string {
		var sb strings.Builder
		for i := range n {
			sb.WriteString(tag)
			if i%7 == 0 && n > 1 {
				sb.WriteString(strings.Repeat("y", 5000))
			}
			sb.WriteByte('\n')
		}
		return sb.String()
	}
	many := make([]int, 21000) // 1 行ずつ 21000 回つなぐ（上限の 2 倍を超えたところで途中の切り詰めが起きる）
	for i := range many {
		many[i] = 1
	}
	for _, sizes := range [][]int{{1, 2}, {6000, 6000}, {12000, 3}, {3, 12000}, {5000, 5000, 5000}, {11000, 11000}, many} {
		for _, start := range []string{"", "open", "done\n"} {
			var parts []ViewAppendParams
			for i, n := range sizes {
				text := lines(n, string(rune('a'+i%26)))
				if r.IntN(2) == 0 {
					text = strings.TrimSuffix(text, "\n") // 改行で終わらない書き足しも混ぜる
				}
				parts = append(parts, ViewAppendParams{Text: text, Highlights: []ViewSpan{{Start: 0, End: min(3, len(text)), Style: "s"}, {Start: len(text) / 2, End: len(text), Style: "t"}}})
			}
			seq := newTextModel(start)
			for _, p := range parts {
				seq.append(p.Text, p.Highlights)
			}
			var j ViewAppendJoin
			for _, p := range parts {
				j.Add(p.Text, p.Highlights)
			}
			merged := j.Params(1, "")
			if err := merged.Validate(); err != nil {
				t.Fatalf("merged: %v", err)
			}
			one := newTextModel(start)
			one.append(merged.Text, merged.Highlights)
			if !seq.equal(one) {
				t.Errorf("sizes %v start %q: merged differs (%d lines vs %d)", sizes, start, len(one.lines), len(seq.lines))
			}
			if n := strings.Count(merged.Text, "\n"); n > ViewTextMaxLines+1 {
				t.Errorf("sizes %v: merged text keeps %d lines", sizes, n)
			}
		}
	}
	// つないだ件は、呼び出し側の列の後ろへ書き込まない。
	base := make([]ViewSpan, 1, 4)
	base[0] = ViewSpan{Start: 0, End: 1, Style: "a"}
	var j ViewAppendJoin
	j.Add("x", base[:1])
	j.Add("y", []ViewSpan{{Start: 0, End: 1, Style: "b"}})
	if got := base[:2][1]; got != (ViewSpan{}) {
		t.Errorf("Add wrote into the caller's array: %v", got)
	}
	if p := j.Params(3, "c"); p.Text != "xy" || p.View != 3 || p.Child != "c" || len(p.Highlights) != 2 || p.Highlights[1].Start != 1 {
		t.Errorf("Params = %+v", p)
	}
}

// つないだ 1 件の大きさは ViewAppendJoinMaxBytes を超えない。改行の無い出力は行数の上限で捨てられないので、
// 大きさで新しい件に分ける（PR #405 の再レビュー）。空の件には、上限を超える 1 件も入る。
func TestViewAppendJoinFits(t *testing.T) {
	var j ViewAppendJoin
	chunk := strings.Repeat("x", 1000)
	n := 0
	for j.Fits(chunk, nil) {
		j.Add(chunk, nil)
		n++
	}
	if j.Len() > ViewAppendJoinMaxBytes || j.Len()+len(chunk) <= ViewAppendJoinMaxBytes {
		t.Errorf("joined %d bytes in %d adds", j.Len(), n)
	}
	if j.Fits("", make([]ViewSpan, ViewAppendJoinMaxBytes/64)) {
		t.Errorf("highlights are not counted")
	}
	var empty ViewAppendJoin
	if !empty.Fits(strings.Repeat("y", ViewAppendJoinMaxBytes+1), nil) {
		t.Errorf("an empty join does not accept a large append")
	}
}

// つなぐ手間は、つないだ全体でなく、足した量に比例する（PR #405 のレビューの推奨 4）。
// 1 行ずつ 3 万回つなぐ間の 1 回あたりの時間が、最初の 1 万回と最後の 1 万回で大きく変わらないことで確かめる。
func TestViewAppendJoinLinear(t *testing.T) {
	line := strings.Repeat("x", 999) + "\n"
	hs := []ViewSpan{{Start: 0, End: 3, Style: "s"}}
	var j ViewAppendJoin
	measure := func(n int) time.Duration {
		start := time.Now()
		for range n {
			j.Add(line, hs)
		}
		return time.Since(start)
	}
	first := measure(10000)
	_ = measure(10000)
	last := measure(10000)
	if last > 10*first+50*time.Millisecond {
		t.Errorf("join slows down: first 10000 adds %v, last 10000 adds %v", first, last)
	}
}

// 範囲が text の外を指すものと、不正な UTF-8 は検査で断る。
func TestViewAppendValidate(t *testing.T) {
	for _, p := range []ViewAppendParams{
		{Text: "\xff"},
		{Text: "ab", Highlights: []ViewSpan{{Start: 0, End: 3}}},
		{Text: "ab", Highlights: []ViewSpan{{Start: 2, End: 1}}},
		{Text: "ab", Highlights: []ViewSpan{{Start: -1, End: 1}}},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil", p)
		}
	}
	if err := (&ViewAppendParams{Text: "ab", Highlights: []ViewSpan{{Start: 0, End: 2, Style: "s"}}}).Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
}

func TestVersionLess(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"0.19.0", "0.20.0", true},
		{"0.20.0", "0.20.0", false},
		{"0.20.1", "0.20.0", false},
		{"1.0.0", "0.20.0", false},
		{"0.9.0", "0.10.0", true},
		{"", "0.20.0", true},
		{"x", "0.20.0", true},
		{"0.20.0", "bad", false},
	} {
		if got := VersionLess(tc.a, tc.b); got != tc.want {
			t.Errorf("VersionLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
