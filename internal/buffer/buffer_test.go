package buffer

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func pos(line, b int) Position { return Position{Line: line, Byte: b} }

func rng(sl, sb, el, eb int) Range { return Range{Start: pos(sl, sb), End: pos(el, eb)} }

func TestNewLines(t *testing.T) {
	tests := []struct {
		text  string
		lines []string
		eol   bool
	}{
		{"", []string{""}, true}, // Vim と同じく、空のファイルは末尾の改行があるものとして扱う
		{"\n", []string{""}, true},
		{"abc", []string{"abc"}, false},
		{"abc\n", []string{"abc"}, true},
		{"a\nb", []string{"a", "b"}, false},
		{"a\nb\n", []string{"a", "b"}, true},
		{"a\n\n", []string{"a", ""}, true},
		{"a\r\nb", []string{"a\r", "b"}, false}, // CR は行の中のバイトとして残す
	}
	for _, tt := range tests {
		b := New(tt.text)
		var got []string
		for i := range b.LineCount() {
			got = append(got, b.Line(i))
		}
		if !slices.Equal(got, tt.lines) {
			t.Errorf("New(%q) lines = %q, want %q", tt.text, got, tt.lines)
		}
		if b.HasTrailingNewline() != tt.eol {
			t.Errorf("New(%q).HasTrailingNewline() = %v, want %v", tt.text, b.HasTrailingNewline(), tt.eol)
		}
		// 末尾の改行を補わない設定では、読み込んだ内容をそのまま復元する。
		b.SetFixEndOfLine(false)
		var out bytes.Buffer
		if _, err := b.WriteTo(&out); err != nil || out.String() != tt.text {
			t.Errorf("New(%q).WriteTo() = %q, %v", tt.text, out.String(), err)
		}
		if b.Version() != 0 {
			t.Errorf("New(%q).Version() = %d, want 0", tt.text, b.Version())
		}
	}
}

// req: FR-15
func TestApply(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		edits   []Edit
		want    string
		wantErr error
	}{
		{
			name:  "行の途中への挿入",
			text:  "hello world",
			edits: []Edit{{Range: rng(0, 5, 0, 5), Text: ","}},
			want:  "hello, world",
		},
		{
			name:  "空のバッファへの挿入",
			text:  "",
			edits: []Edit{{Range: rng(0, 0, 0, 0), Text: "abc"}},
			want:  "abc",
		},
		{
			name:  "行の途中の削除",
			text:  "hello world",
			edits: []Edit{{Range: rng(0, 5, 0, 11)}},
			want:  "hello",
		},
		{
			name:  "置き換え",
			text:  "hello world",
			edits: []Edit{{Range: rng(0, 6, 0, 11), Text: "there"}},
			want:  "hello there",
		},
		{
			name:  "複数行の挿入",
			text:  "ad",
			edits: []Edit{{Range: rng(0, 1, 0, 1), Text: "b\nc\n"}},
			want:  "ab\nc\nd",
		},
		{
			name:  "行をまたぐ削除",
			text:  "one\ntwo\nthree",
			edits: []Edit{{Range: rng(0, 2, 2, 2)}},
			want:  "onree",
		},
		{
			name:  "改行の削除による行の結合",
			text:  "one\ntwo",
			edits: []Edit{{Range: rng(0, 3, 1, 0)}},
			want:  "onetwo",
		},
		{
			name:  "行をまたぐ置き換え",
			text:  "one\ntwo\nthree",
			edits: []Edit{{Range: rng(0, 1, 1, 1), Text: "X\nY"}},
			want:  "oX\nYwo\nthree",
		},
		{
			name:  "空行の削除",
			text:  "a\n\nb\n",
			edits: []Edit{{Range: rng(0, 1, 1, 0)}},
			want:  "a\nb",
		},
		{
			name:  "不正なバイトの直後への挿入",
			text:  "a\x80b",
			edits: []Edit{{Range: rng(0, 2, 0, 2), Text: "X"}},
			want:  "a\x80Xb",
		},
		{
			name:  "不正なバイトだけの削除",
			text:  "a\x80b",
			edits: []Edit{{Range: rng(0, 1, 0, 2)}},
			want:  "ab",
		},
		{
			name:  "切れた多バイト文字の 2 バイト目の削除",
			text:  "\xe3\x81b",
			edits: []Edit{{Range: rng(0, 1, 0, 2)}},
			want:  "\xe3b",
		},
		{
			name:    "不正なバイトの後の多バイト文字の途中",
			text:    "\x80あ",
			edits:   []Edit{{Range: rng(0, 2, 0, 2), Text: "x"}},
			want:    "\x80あ",
			wantErr: ErrInvalidPosition,
		},
		{
			name: "複数の編集は前の編集の後のテキストに対して順に適用する",
			text: "abc",
			edits: []Edit{
				{Range: rng(0, 0, 0, 0), Text: "x\n"},
				{Range: rng(1, 3, 1, 3), Text: "y"},
			},
			want: "x\nabcy",
		},
		{
			name:  "多バイト文字の後への挿入",
			text:  "あい",
			edits: []Edit{{Range: rng(0, 3, 0, 3), Text: "う"}},
			want:  "あうい",
		},
		{
			name:  "多バイト文字の置き換え",
			text:  "日本語",
			edits: []Edit{{Range: rng(0, 3, 0, 6), Text: "a"}},
			want:  "日a語",
		},
		{
			name:    "多バイト文字の途中を指す位置",
			text:    "あい",
			edits:   []Edit{{Range: rng(0, 1, 0, 1), Text: "x"}},
			want:    "あい",
			wantErr: ErrInvalidPosition,
		},
		{
			name:    "行の数を超える位置",
			text:    "abc",
			edits:   []Edit{{Range: rng(1, 0, 1, 0), Text: "x"}},
			want:    "abc",
			wantErr: ErrInvalidPosition,
		},
		{
			name:    "行の長さを超える位置",
			text:    "abc",
			edits:   []Edit{{Range: rng(0, 4, 0, 4), Text: "x"}},
			want:    "abc",
			wantErr: ErrInvalidPosition,
		},
		{
			name:    "負の位置",
			text:    "abc",
			edits:   []Edit{{Range: rng(0, -1, 0, 0), Text: "x"}},
			want:    "abc",
			wantErr: ErrInvalidPosition,
		},
		{
			name:    "終わりが始まりより前の範囲",
			text:    "abc",
			edits:   []Edit{{Range: rng(0, 2, 0, 1)}},
			want:    "abc",
			wantErr: ErrInvalidRange,
		},
		{
			name: "途中で失敗したトランザクションは全く適用しない",
			text: "one\ntwo",
			edits: []Edit{
				{Range: rng(0, 0, 1, 1), Text: "A\nB\nC"},
				{Range: rng(0, 0, 0, 0), Text: "x"},
				{Range: rng(9, 0, 9, 0), Text: "y"},
			},
			want:    "one\ntwo",
			wantErr: ErrInvalidPosition,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(tt.text)
			change, err := b.Apply(Transaction{BaseVersion: 0, Edits: tt.edits})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Apply() error = %v, want %v", err, tt.wantErr)
			}
			if got := b.Text(); got != tt.want {
				t.Errorf("Text() = %q, want %q", got, tt.want)
			}
			wantVersion := uint64(1)
			if tt.wantErr != nil {
				wantVersion = 0
			}
			if b.Version() != wantVersion {
				t.Errorf("Version() = %d, want %d", b.Version(), wantVersion)
			}
			if tt.wantErr != nil {
				if b.CanUndo() {
					t.Error("失敗したトランザクションがアンドゥの履歴に残っている")
				}
				return
			}
			if change.Before != 0 || change.After != 1 || !slices.Equal(change.Edits, tt.edits) {
				t.Errorf("Change = %+v, want Before 0, After 1, Edits %+v", change, tt.edits)
			}
		})
	}
}

// req: FR-15
func TestApplyVersionMismatch(t *testing.T) {
	b := New("abc")
	if _, err := b.Apply(Transaction{BaseVersion: 0, Edits: []Edit{{Range: rng(0, 3, 0, 3), Text: "d"}}}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		base uint64
	}{
		{"古い番号", 0},
		{"未来の番号", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := b.Apply(Transaction{BaseVersion: tt.base, Edits: []Edit{{Range: rng(0, 0, 0, 0), Text: "x"}}})
			if !errors.Is(err, ErrVersionMismatch) {
				t.Fatalf("Apply() error = %v, want ErrVersionMismatch", err)
			}
			if b.Text() != "abcd" || b.Version() != 1 {
				t.Errorf("不一致のトランザクションでバッファが変わった: Text() = %q, Version() = %d", b.Text(), b.Version())
			}
		})
	}
}

func TestApplyEmptyTransaction(t *testing.T) {
	b := New("abc")
	change, err := b.Apply(Transaction{BaseVersion: 0})
	if err != nil {
		t.Fatal(err)
	}
	if change.Before != 0 || change.After != 0 || b.Version() != 0 || b.CanUndo() {
		t.Errorf("空のトランザクションで状態が変わった: Change = %+v, Version() = %d", change, b.Version())
	}
	if _, err := b.Apply(Transaction{BaseVersion: 3}); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("空のトランザクションでも番号を確かめる: error = %v", err)
	}
}

// TestUndoRedoRoundTrip は、トランザクションを順に適用した後、全てアンドゥすると元に戻り、
// 全てリドゥすると適用後に戻ることを確かめる。
//
// req: FR-07
func TestUndoRedoRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		text string
		txs  [][]Edit
	}{
		{
			name: "1 行の挿入と削除",
			text: "hello",
			txs: [][]Edit{
				{{Range: rng(0, 5, 0, 5), Text: " world"}},
				{{Range: rng(0, 0, 0, 1), Text: "H"}},
				{{Range: rng(0, 5, 0, 11)}},
			},
		},
		{
			name: "複数行と複数の編集",
			text: "one\ntwo\nthree\n\n",
			txs: [][]Edit{
				{{Range: rng(0, 1, 2, 3), Text: "X\nY"}, {Range: rng(1, 0, 1, 0), Text: "\n\n"}},
				{{Range: rng(0, 0, 3, 0)}},
				{{Range: rng(0, 0, 0, 0), Text: "z\n"}},
			},
		},
		{
			name: "多バイト文字",
			text: "日本語\nかな",
			txs: [][]Edit{
				{{Range: rng(0, 3, 1, 3), Text: "🙂"}},
				{{Range: rng(0, 0, 0, 3), Text: "にほん\n"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(tt.text)
			texts := []string{b.Text()}
			for _, edits := range tt.txs {
				if _, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: edits}); err != nil {
					t.Fatal(err)
				}
				texts = append(texts, b.Text())
			}
			version := b.Version()

			for i := len(tt.txs) - 1; i >= 0; i-- {
				if _, ok := b.Undo(); !ok {
					t.Fatalf("Undo() = false at %d", i)
				}
				version++
				if b.Text() != texts[i] || b.Version() != version {
					t.Errorf("Undo の後: Text() = %q, Version() = %d, want %q, %d", b.Text(), b.Version(), texts[i], version)
				}
			}
			if _, ok := b.Undo(); ok {
				t.Error("履歴の先頭で Undo() = true")
			}

			for i := 1; i < len(texts); i++ {
				if _, ok := b.Redo(); !ok {
					t.Fatalf("Redo() = false at %d", i)
				}
				version++
				if b.Text() != texts[i] || b.Version() != version {
					t.Errorf("Redo の後: Text() = %q, Version() = %d, want %q, %d", b.Text(), b.Version(), texts[i], version)
				}
			}
			if _, ok := b.Redo(); ok {
				t.Error("履歴の末尾で Redo() = true")
			}
		})
	}
}

// req: FR-07
func TestApplyClearsRedo(t *testing.T) {
	b := New("a")
	if _, err := b.Apply(Transaction{BaseVersion: 0, Edits: []Edit{{Range: rng(0, 1, 0, 1), Text: "b"}}}); err != nil {
		t.Fatal(err)
	}
	b.Undo()
	if !b.CanRedo() {
		t.Fatal("Undo の後に CanRedo() = false")
	}
	if _, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: []Edit{{Range: rng(0, 1, 0, 1), Text: "c"}}}); err != nil {
		t.Fatal(err)
	}
	if b.CanRedo() {
		t.Error("新しい編集の後にリドゥの履歴が残っている")
	}
	if b.Text() != "ac" {
		t.Errorf("Text() = %q, want %q", b.Text(), "ac")
	}
}

// TestChangeReplay は、Apply、Undo、Redo が返す Change を、別のバッファ（拡張機能の側の写し）へ
// 順に適用すると、同じテキストとバージョン番号の対応になることを確かめる。
//
// req: FR-16
func TestChangeReplay(t *testing.T) {
	b := New("alpha\nbeta\n\n")
	mirror := New("alpha\nbeta\n\n")

	ops := []func() (Change, bool){
		func() (Change, bool) {
			c, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: []Edit{
				{Range: rng(0, 0, 0, 5), Text: "ALPHA"},
				{Range: rng(1, 4, 2, 0), Text: "!\ngamma\n"},
			}})
			return c, err == nil
		},
		b.Undo,
		b.Redo,
		func() (Change, bool) {
			c, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: []Edit{{Range: rng(0, 2, 2, 2)}}})
			return c, err == nil
		},
		b.Undo,
		b.Undo,
	}
	for i, op := range ops {
		before := b.Version()
		c, ok := op()
		if !ok {
			t.Fatalf("op %d failed", i)
		}
		if c.Before != before || c.After != b.Version() || c.After != c.Before+1 {
			t.Errorf("op %d: Change の版 = %d -> %d, バッファ %d -> %d", i, c.Before, c.After, before, b.Version())
		}
		for _, e := range c.Edits {
			if _, err := mirror.apply(e); err != nil {
				t.Fatalf("op %d: 写しへの適用に失敗: %v", i, err)
			}
		}
		if mirror.Text() != b.Text() {
			t.Errorf("op %d: 写し = %q, バッファ = %q", i, mirror.Text(), b.Text())
		}
	}
}

func TestChangeIsIndependentOfHistory(t *testing.T) {
	b := New("abc")
	edits := []Edit{{Range: rng(0, 0, 0, 0), Text: "x"}}
	c, err := b.Apply(Transaction{BaseVersion: 0, Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	// 呼び出し側が渡した列や返された列を書き換えても、履歴は変わらない。
	edits[0].Text = "EVIL"
	c.Edits[0].Text = "EVIL"
	b.Undo()
	b.Redo()
	if b.Text() != "xabc" {
		t.Errorf("Text() = %q, want %q", b.Text(), "xabc")
	}
}

func TestSlice(t *testing.T) {
	b := New("one\nあいう\nthree\n\n")
	tests := []struct {
		r       Range
		want    string
		wantErr error
	}{
		{rng(0, 0, 0, 3), "one", nil},
		{rng(0, 1, 0, 1), "", nil},
		{rng(1, 3, 1, 6), "い", nil},
		{rng(0, 2, 2, 2), "e\nあいう\nth", nil},
		{rng(0, 3, 1, 0), "\n", nil},
		{rng(0, 0, 3, 0), "one\nあいう\nthree\n", nil},
		{rng(3, 1, 3, 1), "", ErrInvalidPosition},
		{rng(1, 2, 1, 3), "", ErrInvalidPosition},
		{rng(0, 0, 4, 0), "", ErrInvalidPosition},
		{rng(1, 0, 0, 0), "", ErrInvalidRange},
	}
	for _, tt := range tests {
		got, err := b.Slice(tt.r)
		if !errors.Is(err, tt.wantErr) || got != tt.want {
			t.Errorf("Slice(%v-%v) = %q, %v; want %q, %v", tt.r.Start, tt.r.End, got, err, tt.want, tt.wantErr)
		}
	}
}

// TestBoundaryMatchesRange は、受け付ける位置が、文字列に対する range で止まる位置と行末に一致することを確かめる。
func TestBoundaryMatchesRange(t *testing.T) {
	lines := []string{
		"",
		"abc",
		"あい🙂",
		"a\x80b",
		"\xe3\x81b",
		"\x80あ\xff",
		"\xf0\x9f\x99", // 切れた 4 バイト文字
		"\xef\xbf\xbd", // U+FFFD そのもの
		"\x83\x5c",     // Shift_JIS の「ソ」
	}
	for _, line := range lines {
		b := New(line)
		want := map[int]bool{len(line): true}
		for i := range line {
			want[i] = true
		}
		for i := 0; i <= len(line); i++ {
			err := b.checkPosition(pos(0, i))
			if (err == nil) != want[i] {
				t.Errorf("%q の位置 %d: error = %v, 境界 = %v", line, i, err, want[i])
			}
		}
	}
}

// req: FR-07
func TestUndoGroup(t *testing.T) {
	insert := func(b *Buffer, at int, text string) {
		t.Helper()
		if _, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: []Edit{{Range: rng(0, at, 0, at), Text: text}}}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("グループの中の Apply を 1 回で戻す", func(t *testing.T) {
		b := New("x")
		insert(b, 1, "a")
		b.BeginUndoGroup()
		insert(b, 2, "b")
		insert(b, 3, "c")
		b.BeginUndoGroup() // 入れ子は外側にまとめる
		insert(b, 4, "\nd")
		b.EndUndoGroup()
		insert(b, 1, "e")
		b.EndUndoGroup()
		insert(b, 0, "f")
		if b.Version() != 6 {
			t.Errorf("Version() = %d, want 6（グループの中でも Apply ごとに進む）", b.Version())
		}

		b.Undo()
		if b.Text() != "xeabc\nd" {
			t.Fatalf("1 回目の Undo の後 Text() = %q", b.Text())
		}
		b.Undo()
		if b.Text() != "xa" {
			t.Fatalf("2 回目の Undo の後 Text() = %q, want %q", b.Text(), "xa")
		}
		b.Redo()
		if b.Text() != "xeabc\nd" {
			t.Fatalf("Redo の後 Text() = %q", b.Text())
		}
	})

	t.Run("Undo はグループを閉じる", func(t *testing.T) {
		b := New("")
		insert(b, 0, "a")
		b.BeginUndoGroup()
		insert(b, 1, "b")
		b.Undo()
		insert(b, 1, "c")
		insert(b, 2, "d")
		b.EndUndoGroup() // 閉じた後の EndUndoGroup は何もしない
		b.Undo()
		if b.Text() != "ac" {
			t.Errorf("Text() = %q, want %q", b.Text(), "ac")
		}
	})

	t.Run("空のグループは単位を作らない", func(t *testing.T) {
		b := New("")
		b.BeginUndoGroup()
		b.EndUndoGroup()
		b.EndUndoGroup()
		if b.CanUndo() {
			t.Error("空のグループでアンドゥの単位ができた")
		}
	})

	t.Run("JoinUndoGroup は末尾が変わっていなければ同じ単位にまとめる", func(t *testing.T) {
		b := New("x")
		insert(b, 1, "a")
		b.BeginUndoGroup()
		insert(b, 2, "b")
		b.EndUndoGroup()
		tip := b.UndoTip()
		if !b.JoinUndoGroup(tip) {
			t.Fatal("JoinUndoGroup = false")
		}
		insert(b, 3, "c")
		b.EndUndoGroup()
		b.Undo()
		if b.Text() != "xa" {
			t.Errorf("まとめた後の Undo: %q, want %q", b.Text(), "xa")
		}

		// 間に別の単位が入ったら、まとめない。
		tip = b.UndoTip()
		insert(b, 0, "y")
		if b.JoinUndoGroup(tip) {
			t.Error("末尾が変わったのに JoinUndoGroup = true")
		}
		insert(b, 0, "z")
		b.EndUndoGroup()
		b.Undo()
		if b.Text() != "yxa" {
			t.Errorf("まとめない Undo: %q, want %q", b.Text(), "yxa")
		}
	})
}

func TestModified(t *testing.T) {
	insert := func(b *Buffer, text string) {
		t.Helper()
		if _, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: []Edit{{Range: rng(0, 0, 0, 0), Text: text}}}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(b *Buffer, step string, want bool) {
		t.Helper()
		if b.Modified() != want {
			t.Errorf("%s: Modified() = %v, want %v", step, b.Modified(), want)
		}
	}

	b := New("x")
	check(b, "作った直後", false)
	insert(b, "a")
	check(b, "編集の後", true)
	b.MarkSaved()
	check(b, "保存の後", false)
	insert(b, "b")
	check(b, "保存の後の編集", true)
	b.Undo()
	check(b, "保存した時点へのアンドゥ", false)
	b.Undo()
	check(b, "保存した時点より前へのアンドゥ", true)
	b.Redo()
	check(b, "保存した時点へのリドゥ", false)
	b.Undo()
	insert(b, "a") // 同じテキストでも、履歴が分かれたので保存した時点ではない
	check(b, "履歴が分かれた後", true)
	b.MarkSaved()
	check(b, "保存し直した後", false)

	u := New("x")
	u.MarkUnsaved()
	check(u, "MarkUnsaved の直後", true)
	if u.CanUndo() {
		t.Error("MarkUnsaved の後に CanUndo() = true, want false")
	}
	insert(u, "a")
	u.Undo()
	check(u, "MarkUnsaved の後の編集をアンドゥした", true)
	u.MarkSaved()
	check(u, "MarkUnsaved の後に保存した", false)

	g := New("")
	g.BeginUndoGroup()
	insert(g, "a")
	g.MarkSaved()
	insert(g, "b")
	check(g, "グループの途中で保存した後に同じグループへ足した", true)
	g.EndUndoGroup()
	g.Undo()
	check(g, "グループを戻した", true)
}

func TestFileRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		lines int
		eol   bool
	}{
		{"空のファイル", "", 1, true},
		{"改行だけ", "\n", 1, true},
		{"末尾の改行あり", "a\nb\n", 2, true},
		{"末尾の改行なし", "a\nb", 2, false},
		{"空行で終わる", "a\n\n", 2, true},
		{"多バイト文字", "日本語\n", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "f.txt")
			if err := os.WriteFile(path, []byte(tt.text), 0o600); err != nil {
				t.Fatal(err)
			}
			b, err := ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if b.LineCount() != tt.lines || b.HasTrailingNewline() != tt.eol {
				t.Errorf("LineCount() = %d, HasTrailingNewline() = %v, want %d, %v", b.LineCount(), b.HasTrailingNewline(), tt.lines, tt.eol)
			}
			b.SetFixEndOfLine(false)
			if err = b.WriteFile(path); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.text {
				t.Errorf("WriteFile の内容 = %q, want %q", got, tt.text)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("許可属性 = %v, want 0600（元のファイルに合わせる）", info.Mode().Perm())
			}
			assertOnlyFiles(t, dir, "f.txt")
		})
	}
}

func TestWriteFileNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	if err := New("a\n").WriteFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "a\n" {
		t.Errorf("ReadFile() = %q, %v", got, err)
	}
	assertOnlyFiles(t, dir, "new.txt")
}

func TestWriteFileSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", link); err != nil {
		t.Fatal(err)
	}
	if err := New("new\n").WriteFile(link); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("リンクがリンクでなくなった: %v, %v", info, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new\n" {
		t.Errorf("リンク先の内容 = %q, %v", got, err)
	}
	assertOnlyFiles(t, dir, "link.txt", "real.txt")
}

func TestWriteFileStatError(t *testing.T) {
	// 通常のファイルの下のパスは、Stat が ErrNotExist ではないエラー（ENOTDIR）を返す。
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := New("x").WriteFile(filepath.Join(file, "child"))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("WriteFile() error = %v, want ErrNotExist 以外のエラー", err)
	}
}

// assertOnlyFiles は、dir の中に names のファイルだけがあること（一時ファイルが残っていないこと）を確かめる。
func assertOnlyFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, names) {
		t.Errorf("%s の中身 = %q, want %q", dir, got, names)
	}
}

// TestWriteFinalNewline は、末尾の改行の書き出しが、Vim 9.1 の 'fixendofline' の有効と無効で Vim と一致することを確かめる。
// 期待値は、手元の Vim 9.1 で vim -Nu NONE -n --not-a-term -c 'set [no]fixeol' -c '<操作>' -c 'wq! out' in を実行して得た（2026-11-16）。
// 操作は、Vim のノーマルモードのコマンドを、同じテキストになる Buffer の編集に置き換えた。
func TestWriteFinalNewline(t *testing.T) {
	tests := []struct {
		name string
		in   string
		// edits は、Vim で行った操作に当たる編集である。nil なら編集しない。
		edits []Edit
		// mark は、edits の適用の後に MarkNoLines を呼ぶかどうか（Vim の行単位の削除で全ての行を消した操作）である。
		mark bool
		// undo は、編集の後にアンドゥするかどうかである。
		undo bool
		// after は、mark と undo の後に適用する編集である。
		after []Edit
		fix   string // fixendofline が有効のときの書き出しの結果
		nofx  string // fixendofline が無効のときの書き出しの結果
	}{
		{name: "末尾の改行なし", in: "a\nb", fix: "a\nb\n", nofx: "a\nb"},
		{name: "末尾の改行あり", in: "a\nb\n", fix: "a\nb\n", nofx: "a\nb\n"},
		{name: "空のファイル", in: "", fix: "", nofx: ""},
		{name: "改行だけ", in: "\n", fix: "\n", nofx: "\n"},
		{name: "CR で終わる行", in: "a\r", fix: "a\r\n", nofx: "a\r"},
		// ix
		{name: "空のファイルに挿入", in: "", edits: []Edit{{Range: rng(0, 0, 0, 0), Text: "x"}}, fix: "x\n", nofx: "x\n"},
		// ix<Esc>x
		{name: "空のファイルに挿入して消す", in: "", edits: []Edit{{Range: rng(0, 0, 0, 0), Text: "x"}, {Range: rng(0, 0, 0, 1)}}, fix: "\n", nofx: "\n"},
		// ix<Esc>u
		{name: "空のファイルに挿入して戻す", in: "", edits: []Edit{{Range: rng(0, 0, 0, 0), Text: "x"}}, undo: true, fix: "", nofx: ""},
		// ix
		{name: "改行だけのファイルに挿入", in: "\n", edits: []Edit{{Range: rng(0, 0, 0, 0), Text: "x"}}, fix: "x\n", nofx: "x\n"},
		// ox
		{name: "末尾の改行なしに行を足す", in: "a", edits: []Edit{{Range: rng(0, 1, 0, 1), Text: "\nx"}}, fix: "a\nx\n", nofx: "a\nx"},
		// 0D
		{name: "末尾の改行なしの行を空にする", in: "abc", edits: []Edit{{Range: rng(0, 0, 0, 3)}}, fix: "\n", nofx: ""},
		// 0D
		{name: "末尾の改行ありの行を空にする", in: "abc\n", edits: []Edit{{Range: rng(0, 0, 0, 3)}}, fix: "\n", nofx: "\n"},
		// dd
		{name: "改行だけのファイルで dd", in: "\n", edits: []Edit{}, mark: true, fix: "", nofx: ""},
		{name: "末尾の改行なしで dd", in: "a", edits: []Edit{{Range: rng(0, 0, 0, 1)}}, mark: true, fix: "", nofx: ""},
		// ggdG
		{name: "末尾の改行ありで ggdG", in: "a\nb\n", edits: []Edit{{Range: rng(0, 0, 1, 1)}}, mark: true, fix: "", nofx: ""},
		{name: "末尾の改行なしで ggdG", in: "a\nb", edits: []Edit{{Range: rng(0, 0, 1, 1)}}, mark: true, fix: "", nofx: ""},
		// ddu
		{name: "dd を戻す", in: "a", edits: []Edit{{Range: rng(0, 0, 0, 1)}}, mark: true, undo: true, fix: "a\n", nofx: "a"},
		// ggdGix
		{name: "ggdG の後に入力", in: "a\nb", edits: []Edit{{Range: rng(0, 0, 1, 1)}}, mark: true, after: []Edit{{Range: rng(0, 0, 0, 0), Text: "x"}}, fix: "x\n", nofx: "x"},
		// ddo<Esc>
		{name: "dd の後に o", in: "a", edits: []Edit{{Range: rng(0, 0, 0, 1)}}, mark: true, after: []Edit{{Range: rng(0, 0, 0, 0), Text: "\n"}}, fix: "\n\n", nofx: "\n"},
	}
	for _, tt := range tests {
		for _, fix := range []bool{true, false} {
			b := New(tt.in)
			b.SetFixEndOfLine(fix)
			if tt.edits != nil {
				if _, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: tt.edits}); err != nil {
					t.Fatalf("%s: Apply() error = %v", tt.name, err)
				}
			}
			if tt.mark {
				b.MarkNoLines()
			}
			if tt.undo {
				b.Undo()
			}
			if tt.after != nil {
				if _, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: tt.after}); err != nil {
					t.Fatalf("%s: Apply() error = %v", tt.name, err)
				}
			}
			want := tt.nofx
			if fix {
				want = tt.fix
			}
			var out bytes.Buffer
			if _, err := b.WriteTo(&out); err != nil || out.String() != want {
				t.Errorf("%s (fixendofline=%v): WriteTo() = %q, %v; want %q", tt.name, fix, out.String(), err, want)
			}
		}
	}
}

// MarkNoLines の記録は、アンドゥの履歴の位置に付く。編集で外れ、アンドゥとリドゥで戻ると付く。テキストが空でなければ付かない。
func TestMarkNoLines(t *testing.T) {
	b := New("a\nb")
	b.MarkNoLines()
	if b.NoLines() {
		t.Fatal("空でないテキストで NoLines() = true")
	}
	if _, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: []Edit{{Range: rng(0, 0, 1, 1)}}}); err != nil {
		t.Fatal(err)
	}
	if b.NoLines() {
		t.Fatal("MarkNoLines の前に NoLines() = true")
	}
	b.MarkNoLines()
	v := b.Version()
	if !b.NoLines() || b.Version() != v || !b.Modified() {
		t.Fatalf("MarkNoLines の後: NoLines() = %v, Version() = %d（前は %d）, Modified() = %v", b.NoLines(), b.Version(), v, b.Modified())
	}
	b.Undo()
	if b.NoLines() {
		t.Error("アンドゥの後に NoLines() = true")
	}
	b.Redo()
	if !b.NoLines() {
		t.Error("リドゥの後に NoLines() = false")
	}
	if _, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: []Edit{{Range: rng(0, 0, 0, 0), Text: "x"}}}); err != nil {
		t.Fatal(err)
	}
	b.Undo()
	if !b.NoLines() {
		t.Error("入力を戻した後に NoLines() = false（Vim は行の無いバッファに戻る）")
	}
	v = b.Version()
	b.MarkHasLines()
	if b.NoLines() || b.Version() != v {
		t.Errorf("MarkHasLines の後: NoLines() = %v, Version() = %d", b.NoLines(), b.Version())
	}
	e := New("")
	e.MarkHasLines()
	if e.NoLines() {
		t.Error(`New("") に MarkHasLines の後も NoLines() = true`)
	}
	if !New("").NoLines() || New("\n").NoLines() {
		t.Error(`New("") は行の無いバッファで、New("\n") は空の行 1 つのバッファである`)
	}
}

func TestFinalNewline(t *testing.T) {
	b := New("a")
	if !b.FixEndOfLine() || !b.FinalNewline() || b.HasTrailingNewline() {
		t.Errorf("New(%q): FixEndOfLine() = %v, FinalNewline() = %v, HasTrailingNewline() = %v; want true, true, false",
			"a", b.FixEndOfLine(), b.FinalNewline(), b.HasTrailingNewline())
	}
	b.SetFixEndOfLine(false)
	if b.FixEndOfLine() || b.FinalNewline() {
		t.Errorf("SetFixEndOfLine(false) の後: FixEndOfLine() = %v, FinalNewline() = %v; want false, false", b.FixEndOfLine(), b.FinalNewline())
	}
	if b.Modified() || b.Version() != 0 {
		t.Errorf("SetFixEndOfLine は変更として扱わない: Modified() = %v, Version() = %d", b.Modified(), b.Version())
	}
	if c := New("a\n"); func() bool { c.SetFixEndOfLine(false); return !c.FinalNewline() }() {
		t.Errorf("末尾の改行のあるバッファの FinalNewline() = false, want true")
	}
}

// SetTrailingNewline は、末尾の改行の記録だけを変え、書き出しの結果に効く。変更としては扱わず、アンドゥでも戻らない。
func TestSetTrailingNewline(t *testing.T) {
	b := New("a")
	b.SetFixEndOfLine(false)
	b.SetTrailingNewline(true)
	if !b.HasTrailingNewline() || !b.FinalNewline() || b.Modified() || b.Version() != 0 {
		t.Errorf("SetTrailingNewline(true) の後: HasTrailingNewline() = %v, FinalNewline() = %v, Modified() = %v, Version() = %d",
			b.HasTrailingNewline(), b.FinalNewline(), b.Modified(), b.Version())
	}
	var out bytes.Buffer
	if _, err := b.WriteTo(&out); err != nil || out.String() != "a\n" {
		t.Errorf("WriteTo() = %q, %v; want %q", out.String(), err, "a\n")
	}
	if _, err := b.Apply(Transaction{BaseVersion: b.Version(), Edits: []Edit{{Range: rng(0, 1, 0, 1), Text: "b"}}}); err != nil {
		t.Fatal(err)
	}
	b.SetTrailingNewline(false)
	b.Undo()
	if b.HasTrailingNewline() || b.FinalNewline() {
		t.Errorf("アンドゥの後: HasTrailingNewline() = %v, FinalNewline() = %v; want false, false（アンドゥで戻らない）", b.HasTrailingNewline(), b.FinalNewline())
	}
}

func TestWriteTo(t *testing.T) {
	b, err := Read(bytes.NewBufferString("x\ny\n"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	n, err := b.WriteTo(&out)
	if err != nil || n != 4 || out.String() != "x\ny\n" {
		t.Errorf("WriteTo() = %d, %v, %q", n, err, out.String())
	}
}

func TestReadFileNotExist(t *testing.T) {
	_, err := ReadFile(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadFile() error = %v, want os.ErrNotExist", err)
	}
}

// ApplyReload は、編集と末尾の改行の記録の変更を 1 つのアンドゥの単位にし、アンドゥとリドゥで記録も戻す。
// 編集が無くても記録が変われば単位を作り、どちらも変わらなければ何もしない。
func TestApplyReload(t *testing.T) {
	b := New("a\nb\n")
	ch, err := b.ApplyReload(Transaction{BaseVersion: 0, Edits: []Edit{{Range: rng(1, 0, 1, 1), Text: "B"}}}, false)
	if err != nil || ch.After != 1 || b.Text() != "a\nB" || b.HasTrailingNewline() {
		t.Fatalf("ApplyReload() = %+v, %v; text = %q, eol = %v", ch, err, b.Text(), b.HasTrailingNewline())
	}
	if _, ok := b.Undo(); !ok || b.Text() != "a\nb" || !b.HasTrailingNewline() {
		t.Fatalf("Undo: text = %q, eol = %v", b.Text(), b.HasTrailingNewline())
	}
	if _, ok := b.Redo(); !ok || b.Text() != "a\nB" || b.HasTrailingNewline() {
		t.Fatalf("Redo: text = %q, eol = %v", b.Text(), b.HasTrailingNewline())
	}
	// 記録だけが変わる。
	ch, err = b.ApplyReload(Transaction{BaseVersion: b.Version()}, true)
	if err != nil || ch.Before == ch.After || !b.HasTrailingNewline() {
		t.Fatalf("記録だけの ApplyReload() = %+v, %v", ch, err)
	}
	if _, ok := b.Undo(); !ok || b.HasTrailingNewline() {
		t.Fatalf("記録だけの Undo: eol = %v", b.HasTrailingNewline())
	}
	// どちらも変わらない。
	v := b.Version()
	if ch, err := b.ApplyReload(Transaction{BaseVersion: v}, false); err != nil || ch.Before != ch.After || b.Version() != v {
		t.Fatalf("変わらない ApplyReload() = %+v, %v", ch, err)
	}
	if _, err := b.ApplyReload(Transaction{BaseVersion: v + 1}, true); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("版の不一致: %v", err)
	}
}

// ApplyUnrecorded は、編集を禁止したバッファにも当て、アンドゥの履歴に積まない。前の履歴は捨て、変更ありかどうかは保つ。
func TestApplyUnrecorded(t *testing.T) {
	b := New("a")
	if _, err := b.Apply(Transaction{BaseVersion: 0, Edits: []Edit{{Range: rng(0, 1, 0, 1), Text: "b"}}}); err != nil {
		t.Fatal(err)
	}
	b.MarkSaved()
	b.SetReadOnly(true)
	ch, err := b.ApplyUnrecorded(Transaction{BaseVersion: 1, Edits: []Edit{
		{Range: rng(0, 2, 0, 2), Text: "\nc\nd"},
		{Range: rng(0, 0, 1, 0), Text: ""}, // 逐次の数え方で、先頭の行を捨てる
	}})
	if err != nil || ch.Before != 1 || ch.After != 2 || b.Text() != "c\nd" {
		t.Fatalf("ApplyUnrecorded() = %+v, %v; text = %q", ch, err, b.Text())
	}
	if b.CanUndo() || b.CanRedo() || b.Modified() {
		t.Errorf("after ApplyUnrecorded: undo %v redo %v modified %v", b.CanUndo(), b.CanRedo(), b.Modified())
	}
	b.SetReadOnly(false)
	if _, err := b.Apply(Transaction{BaseVersion: 2, Edits: []Edit{{Range: rng(0, 0, 0, 0), Text: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Undo(); !ok || b.Text() != "c\nd" || b.Modified() {
		t.Errorf("undo after ApplyUnrecorded: text %q modified %v", b.Text(), b.Modified())
	}
	if _, err := b.ApplyUnrecorded(Transaction{BaseVersion: 0, Edits: []Edit{{Text: "y"}}}); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("版の不一致: %v", err)
	}
	if _, err := b.ApplyUnrecorded(Transaction{BaseVersion: b.Version(), Edits: []Edit{{Range: rng(9, 0, 9, 0), Text: "y"}}}); err == nil || b.Text() != "c\nd" {
		t.Errorf("範囲の外: %v, text %q", err, b.Text())
	}
}
