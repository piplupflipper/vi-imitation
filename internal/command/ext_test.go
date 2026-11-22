package command

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/target"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/input"
)

// fakeExt は、拡張機能の問い合わせを記録し、決まった結果を返す。
//
//	surround.add（オペレーター）  範囲を、対象の後の文字の括弧で囲む
//	jump.x（モーション）          次の 'x' へ移る
//	line.inner（テキストオブジェクト）行の内容全体
//	grow.lines（テキストオブジェクト）選択を上下に 1 行ずつ広げる（選択を置き換える）
//	grow.chars（テキストオブジェクト）選択の行の 2 文字目から最後の文字の前まで（選択を置き換える）
//	dup.line（操作）              行を下に複製する
type fakeExt struct {
	calls      []string
	replays    []bool          // 問い合わせごとの QueryInfo.Replaying
	selections []*buffer.Range // テキストオブジェクトの問い合わせごとの target.Request.Selection
	deferring  bool            // true なら、再生中でない問い合わせに ErrDeferred を返す
	stale      bool            // true なら古いバージョン番号のトランザクションを返す
	failing    bool
}

func (f *fakeExt) Motion(ref editcmd.Ref, b *buffer.Buffer, req target.Request, info QueryInfo) (target.Result, error) {
	f.calls = append(f.calls, "motion "+ref.ID)
	f.replays = append(f.replays, info.Replaying)
	if f.deferring && !info.Replaying {
		return target.Result{}, ErrDeferred
	}
	line := b.Line(req.Cursor.Line)
	i := strings.IndexByte(line[req.Cursor.Byte+1:], 'x')
	if i < 0 {
		return target.Result{}, target.ErrNoTarget
	}
	return target.Result{Pos: pos(req.Cursor.Line, req.Cursor.Byte+1+i), Kind: target.Inclusive}, nil
}

func (f *fakeExt) TextObject(ref editcmd.Ref, b *buffer.Buffer, req target.Request, info QueryInfo) (target.Span, error) {
	f.calls = append(f.calls, "textObject "+ref.ID)
	f.replays = append(f.replays, info.Replaying)
	f.selections = append(f.selections, req.Selection)
	if f.deferring && !info.Replaying {
		return target.Span{}, ErrDeferred
	}
	switch ref.ID {
	case "lines":
		// 選択が無ければカーソルの行、あれば選択の上と下に 1 行ずつ広げた行（tree-sitter の an の代役）。
		if req.Selection == nil {
			l := req.Cursor.Line
			return target.Span{Start: pos(l, 0), End: pos(l, len(b.Line(l))), Kind: target.Linewise}, nil
		}
		first, last := max(req.Selection.Start.Line-1, 0), min(req.Selection.End.Line+1, b.LineCount()-1)
		if first == req.Selection.Start.Line && last == req.Selection.End.Line {
			return target.Span{}, target.ErrNoTarget
		}
		return target.Span{Start: pos(first, 0), End: pos(last, len(b.Line(last))), Kind: target.Linewise, ReplaceSelection: true}, nil
	case "chars":
		// 選択の最初の行の 2 文字目から、最後の行の最後の文字の前まで（文字単位。選択を置き換える）。
		if req.Selection == nil {
			return target.Span{}, target.ErrNoTarget
		}
		last := req.Selection.End.Line
		return target.Span{Start: pos(req.Selection.Start.Line, 1), End: pos(last, len(b.Line(last))-1), Kind: target.Exclusive, ReplaceSelection: true}, nil
	}
	n := len(b.Line(req.Cursor.Line))
	return target.Span{Start: pos(req.Cursor.Line, 0), End: pos(req.Cursor.Line, n), Kind: target.Exclusive}, nil
}

func (f *fakeExt) Operator(ref editcmd.Ref, b *buffer.Buffer, q OperatorQuery, info QueryInfo) (buffer.Transaction, error) {
	f.calls = append(f.calls, "operator "+ref.ID+" "+string(q.Char))
	f.replays = append(f.replays, info.Replaying)
	if f.deferring && !info.Replaying {
		return buffer.Transaction{}, ErrDeferred
	}
	if f.failing {
		return buffer.Transaction{}, errors.New("timeout")
	}
	r := q.Span.Range(b)
	open := map[rune]string{')': "(", ']': "["}[q.Char]
	tx := buffer.Transaction{BaseVersion: b.Version(), Edits: []buffer.Edit{
		{Range: emptyRange(r.End), Text: string(q.Char)},
		{Range: emptyRange(r.Start), Text: open},
	}}
	if f.stale {
		tx.BaseVersion--
	}
	return tx, nil
}

func (f *fakeExt) Action(ref editcmd.Ref, b *buffer.Buffer, q ActionQuery, info QueryInfo) (buffer.Transaction, error) {
	call := "action " + ref.ID
	if q.Text != "" {
		call += " " + q.Text
	}
	f.calls = append(f.calls, call)
	f.replays = append(f.replays, info.Replaying)
	if f.deferring && !info.Replaying {
		return buffer.Transaction{}, ErrDeferred
	}
	l := q.Cursor.Line
	return buffer.Transaction{BaseVersion: b.Version(), Edits: []buffer.Edit{
		{Range: emptyRange(pos(l, len(b.Line(l)))), Text: "\n" + b.Line(l)},
	}}, nil
}

// extKeymap は、fakeExt の操作を登録したキーマップを返す。
func extKeymap(t *testing.T) *input.Keymap {
	t.Helper()
	km := input.DefaultKeymap()
	for _, b := range []input.Binding{
		{Kind: input.BindOperator, Keys: editcmd.MustParseKeys("ys"), Ref: editcmd.Ref{Extension: "surround", ID: "add"}, TakesChar: true},
		{Kind: input.BindMotion, Keys: editcmd.MustParseKeys("gx"), Ref: editcmd.Ref{Extension: "jump", ID: "x"}},
		{Kind: input.BindTextObject, Keys: editcmd.MustParseKeys("il"), Ref: editcmd.Ref{Extension: "line", ID: "inner"}},
		{Kind: input.BindTextObject, Keys: editcmd.MustParseKeys("an"), Ref: editcmd.Ref{Extension: "grow", ID: "lines"}},
		{Kind: input.BindTextObject, Keys: editcmd.MustParseKeys("ic"), Ref: editcmd.Ref{Extension: "grow", ID: "chars"}},
		{Kind: input.BindAction, Keys: editcmd.MustParseKeys("gD"), Ref: editcmd.Ref{Extension: "dup", ID: "line"}},
	} {
		if err := km.Bind(b); err != nil {
			t.Fatal(err)
		}
	}
	return km
}

// 拡張機能の操作は、ドットリピートとマクロの再生のたびに、同じ拡張機能へ同じ引数で問い合わせ直す。
//
// req: FR-06, FR-12
func TestExtensionRepeat(t *testing.T) {
	tests := []struct {
		keys  string
		want  string
		calls []string
	}{
		{"ysiw)W.", "(ab) (cd) ex f", []string{"operator add )", "operator add )"}},
		{"ysgx]", "[ab cd ex] f", []string{"motion x", "operator add ]"}},
		{"ysil)", "(ab cd ex f)", []string{"textObject inner", "operator add )"}},
		{"qaysiw)Wq@a", "(ab) (cd) ex f", []string{"operator add )", "operator add )"}},
		{"gDj.", "ab cd ex f\nab cd ex f\nab cd ex f", []string{"action line", "action line"}},
		{"ysiw)u", "ab cd ex f", []string{"operator add )"}},
	}
	for _, tt := range tests {
		ext := &fakeExt{}
		s := New(newBuffer("ab cd ex f"), input.NewParser(extKeymap(t)))
		s.Extensions = ext
		if err := errOf(feedKeys(s, tt.keys)); err != nil {
			t.Errorf("%s: err = %v", tt.keys, err)
		}
		if got := s.buf.Text(); got != tt.want {
			t.Errorf("%s: text = %q, want %q", tt.keys, got, tt.want)
		}
		if strings.Join(ext.calls, ",") != strings.Join(tt.calls, ",") {
			t.Errorf("%s: calls = %q, want %q", tt.keys, ext.calls, tt.calls)
		}
	}
}

// 拡張機能の問い合わせの失敗、古いバージョン番号、問い合わせ先が無い場合は、バッファを変えずに失敗する。
//
// req: FR-12
func TestExtensionFailure(t *testing.T) {
	for _, tt := range []struct {
		name string
		ext  Extensions
		want error
	}{
		{"失敗", &fakeExt{failing: true}, nil},
		{"古いバージョン番号", &fakeExt{stale: true}, buffer.ErrVersionMismatch},
		{"問い合わせ先が無い", nil, ErrNoExtensions},
	} {
		s := New(newBuffer("ab cd"), input.NewParser(extKeymap(t)))
		s.Extensions = tt.ext
		err := errOf(feedKeys(s, "ysiw)"))
		if err == nil || tt.want != nil && !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
		if got := s.buf.Text(); got != "ab cd" {
			t.Errorf("%s: text = %q", tt.name, got)
		}
		// 失敗した操作はドットリピートに残らない。
		if err := errOf(feedKeys(s, ".")); !errors.Is(err, ErrFailed) {
			t.Errorf("%s: . err = %v", tt.name, err)
		}
	}
}

// マクロとドットリピートの再生中の問い合わせには、QueryInfo.Replaying が true で渡る（FR-19 を editor が実現するため）。
func TestExtensionReplaying(t *testing.T) {
	for _, tt := range []struct {
		keys string
		want []bool
	}{
		{"ysiw)W.", []bool{false, true}},
		{"qaysiw)Wq@a", []bool{false, true}},
		{"ysiw)Wqa.q@a", []bool{false, true, true}},
	} {
		ext := &fakeExt{}
		s := New(newBuffer("ab cd ex f"), input.NewParser(extKeymap(t)))
		s.Extensions = ext
		feedKeys(s, tt.keys)
		if fmt.Sprint(ext.replays) != fmt.Sprint(tt.want) {
			t.Errorf("%s: replays = %v, want %v", tt.keys, ext.replays, tt.want)
		}
		if s.Replaying() {
			t.Errorf("%s: 再生の後も Replaying", tt.keys)
		}
	}
}

// ビジュアルモードの拡張機能のテキストオブジェクトには今の選択が渡り、拡張機能が選択を置き換えると答えたら、
// 選択をその範囲にする（tree-sitter の an と in を繰り返して外へ広げる。#373）。
// カーソルが選択の始まりの側にあれば始まりに置き、行単位の答えなら V に、文字単位の答えなら v にする。
//
// req: FR-240
func TestVisualTextObjectReplaceSelection(t *testing.T) {
	sel := func(sl, sb, el, eb int) string {
		return fmt.Sprint(&buffer.Range{Start: pos(sl, sb), End: pos(el, eb)})
	}
	tests := []struct {
		name, text, keys string
		mode             Mode
		cursor           buffer.Position
		reg              string // 最後に "ad を足して打ったときのレジスター a の中身。空なら "ad を打たない
		text2            string // 空でなければ、打った後のバッファの中身
		sels             []string
		err              bool
	}{
		{name: "1 回目は選択を渡さない", text: "a\nb\nc\nd\ne", keys: "jjvan", mode: ModeVisualLine, cursor: pos(2, 0), reg: "c", sels: []string{"<nil>"}},
		{name: "繰り返すと外へ広げる", text: "a\nb\nc\nd\ne", keys: "jjvanan", mode: ModeVisualLine, cursor: pos(3, 0), reg: "b\nc\nd", sels: []string{"<nil>", sel(2, 0, 2, 1)}},
		{name: "3 回", text: "a\nb\nc\nd\ne", keys: "jjvananan", mode: ModeVisualLine, cursor: pos(4, 0), reg: "a\nb\nc\nd\ne"},
		{name: "広げられなければ失敗して選択を変えない", text: "a\nb\nc", keys: "jvananan", mode: ModeVisualLine, cursor: pos(2, 0), reg: "a\nb\nc", err: true},
		{name: "カーソルが始まりの側なら始まりに置く", text: "a\nb\nc\nd\ne", keys: "jjvananoan", mode: ModeVisualLine, cursor: pos(0, 0), reg: "a\nb\nc\nd\ne"},
		{name: "広げた選択に d", text: "a\nb\nc\nd\ne", keys: "jjvanand", mode: ModeNormal, cursor: pos(1, 0), text2: "a\ne"},
		{name: "選択がバッファの終わりまででも広げる", text: "a\nbc", keys: "jvlan", mode: ModeVisualLine, cursor: pos(1, 1), reg: "a\nbc", sels: []string{sel(1, 0, 1, 2)}},
		{name: "置き換えない答えはバッファの終わりで失敗", text: "a\nbc", keys: "jvlil", mode: ModeVisual, cursor: pos(1, 1), reg: "bc", err: true},
		{name: "文字単位の選択を渡す", text: "abcdefg", keys: "lllvlic", mode: ModeVisual, cursor: pos(0, 5), reg: "bcdef", sels: []string{sel(0, 3, 0, 5)}},
		{name: "行単位の選択は行の全体を渡し、文字単位の答えで v にする", text: "abcdefg\nxyz", keys: "Vic", mode: ModeVisual, cursor: pos(0, 5), reg: "bcdef", sels: []string{sel(0, 0, 0, 7)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := &fakeExt{}
			s := New(newBuffer(tt.text), input.NewParser(extKeymap(t)))
			s.Extensions = ext
			err := errOf(feedKeys(s, tt.keys))
			if (err != nil) != tt.err {
				t.Errorf("err = %v, want error %v", err, tt.err)
			}
			if s.Mode() != tt.mode || s.cursor != tt.cursor {
				t.Errorf("mode %v cursor %v, want %v %v", s.Mode(), s.cursor, tt.mode, tt.cursor)
			}
			if tt.sels != nil {
				var got []string
				for _, r := range ext.selections {
					if r == nil {
						got = append(got, "<nil>")
						continue
					}
					got = append(got, fmt.Sprint(r))
				}
				if strings.Join(got, ",") != strings.Join(tt.sels, ",") {
					t.Errorf("selections = %v, want %v", got, tt.sels)
				}
			}
			if tt.text2 != "" {
				if got := s.buf.Text(); got != tt.text2 {
					t.Errorf("text = %q, want %q", got, tt.text2)
				}
			}
			if tt.reg != "" {
				// y は、キーマップの ys（surround.add）の続きを待つので、d で選択を取り出す。
				if err := errOf(feedKeys(s, `"ad`)); err != nil {
					t.Fatal(err)
				}
				if reg, _ := s.registers.Get('a'); reg.Text != tt.reg {
					t.Errorf("register = %q, want %q", reg.Text, tt.reg)
				}
			}
		})
	}
}

// 期限 0 で結果を保留した操作は、失敗にせず Deferred で返り、届いた結果で ApplyDeferred すると、
// ふだんの経路と同じく適用、カーソルの移動、ドットリピートへの記録が行われる。
// 問い合わせの後にバッファかカーソルが変わっていれば破棄する。
//
// req: FR-18
func TestApplyDeferred(t *testing.T) {
	tests := []struct {
		name, text, keys, between, after string
		want, cursor                     string
		stale                            bool
	}{
		{"オペレーター", "|ab cd", "ysiw)", "", "W.", "(ab) (cd)", "0:5", false},
		{"モーション", "|ab cd ex", "ysgx)", "", "", "(ab cd ex)", "0:0", false},
		{"ビジュアルモード", "|ab cd", "veys]", "", "", "[ab] cd", "0:0", false},
		{"カーソルが動いた", "|ab cd", "ysiw)", "l", "", "ab cd", "0:1", true},
		{"バッファが変わった", "|ab cd", "ysiw)", "x", "", "b cd", "0:0", true},
		{"インサートモードにいる", "|ab cd", "ysiw)", "i", "<Esc>", "ab cd", "0:0", true},
	}
	for _, tt := range tests {
		ext := &fakeExt{deferring: true}
		s := New(newBuffer(strings.Replace(tt.text, "|", "", 1)), input.NewParser(extKeymap(t)))
		s.Extensions = ext
		var d *Deferred
		for _, r := range feedKeys(s, tt.keys) {
			if r.Err != nil {
				t.Fatalf("%s: err = %v", tt.name, r.Err)
			}
			if r.Deferred != nil {
				d = r.Deferred
			}
		}
		if d == nil || s.buf.Text() != strings.Replace(tt.text, "|", "", 1) {
			t.Fatalf("%s: Deferred = %v, text = %q", tt.name, d, s.buf.Text())
		}
		feedKeys(s, tt.between)
		r := s.ApplyDeferred(*d, &fakeExt{})
		if tt.stale != errors.Is(r.Err, ErrStale) {
			t.Errorf("%s: err = %v", tt.name, r.Err)
		}
		feedKeys(s, tt.after)
		if got := s.buf.Text(); got != tt.want {
			t.Errorf("%s: text = %q, want %q", tt.name, got, tt.want)
		}
		if got := s.Cursor().String(); got != tt.cursor {
			t.Errorf("%s: cursor = %s, want %s", tt.name, got, tt.cursor)
		}
	}
}

// 再生中に ErrDeferred が返った場合は、保留せずに失敗として扱い、再生を止める（FR-19）。
func TestDeferredWhileReplaying(t *testing.T) {
	s := New(newBuffer("ab cd"), input.NewParser(extKeymap(t)))
	s.Extensions = &fakeExt{}
	feedKeys(s, "ysiw)")
	s.Extensions = alwaysDefer{}
	r := s.Feed(editcmd.Rune('.'))
	if r.Deferred != nil || !errors.Is(r.Err, ErrDeferred) {
		t.Errorf("Result = %+v", r)
	}
}

// alwaysDefer は、再生中でも ErrDeferred を返す誤った実装である。
type alwaysDefer struct{}

func (alwaysDefer) Motion(editcmd.Ref, *buffer.Buffer, target.Request, QueryInfo) (target.Result, error) {
	return target.Result{}, ErrDeferred
}

func (alwaysDefer) TextObject(editcmd.Ref, *buffer.Buffer, target.Request, QueryInfo) (target.Span, error) {
	return target.Span{}, ErrDeferred
}

func (alwaysDefer) Operator(editcmd.Ref, *buffer.Buffer, OperatorQuery, QueryInfo) (buffer.Transaction, error) {
	return buffer.Transaction{}, ErrDeferred
}

func (alwaysDefer) Action(editcmd.Ref, *buffer.Buffer, ActionQuery, QueryInfo) (buffer.Transaction, error) {
	return buffer.Transaction{}, ErrDeferred
}
