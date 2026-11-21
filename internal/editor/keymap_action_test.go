package editor

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/input"
)

// emacsInsertKeys は、config.example.jsonc の keymap.insert と同じ、インサートモードの Emacs 風のキーである（#257、#260）。
// Ctrl-H は端末から <BS> として届くので、例には書かない（<BS> の既定の insert.delete-prev-char で前の文字を消す）。
// テストでは、Ctrl-H を打ったときに届く <BS> を打つ。
var emacsInsertKeys = map[string]string{
	"<C-a>": "insert.line-start", "<C-e>": "insert.line-end",
	"<C-f>": "insert.right", "<C-b>": "insert.left",
	"<C-n>": "insert.down", "<C-p>": "insert.up",
	"<C-d>": "insert.delete-char", "<C-k>": "insert.delete-to-line-end",
	"<C-w>": "insert.delete-prev-word", "<C-u>": "insert.delete-to-line-start",
}

// insertCase は、text（"|" がカーソル）で keys を打った後のテキストとカーソル（行:バイト）である。
type insertCase struct {
	text, keys   string
	want, cursor string
}

// runInsertCases は、操作の表 km を引く command.State で cases を確かめる。
func runInsertCases(t *testing.T, km *input.Keymap, cases []insertCase) {
	t.Helper()
	for _, tc := range cases {
		lines := strings.Split(tc.text, "\n")
		var at buffer.Position
		for i, l := range lines {
			if j := strings.Index(l, "|"); j >= 0 {
				at = buffer.Position{Line: i, Byte: j}
				lines[i] = l[:j] + l[j+1:]
			}
		}
		buf := buffer.New(strings.Join(lines, "\n") + "\n")
		st := command.New(buf, input.NewParser(km))
		st.SetCursor(at)
		for _, k := range editcmd.MustParseKeys(tc.keys) {
			st.Feed(k)
		}
		got := strings.TrimSuffix(buf.Text(), "\n")
		c := st.Cursor()
		if cursor := fmt.Sprintf("%d:%d", c.Line, c.Byte); got != tc.want || cursor != tc.cursor || st.Mode() != command.ModeNormal {
			t.Errorf("%q %s: text %q cursor %s mode %v, want %q %s", tc.text, tc.keys, got, cursor, st.Mode(), tc.want, tc.cursor)
		}
		if n := st.Registers().Pushes(); n != 0 {
			t.Errorf("%q %s: レジスターの履歴に %d 件積んだ", tc.text, tc.keys, n)
		}
	}
}

// emacsKeymap は、本体の操作の表に emacsInsertKeys を割り当てた表を返す。
func emacsKeymap(t *testing.T) *input.Keymap {
	t.Helper()
	km := input.DefaultKeymap()
	if errs := applyKeymapActions(km, userKeymap{actions: map[string]map[string]string{"insert": emacsInsertKeys}}, editcmd.MustParseKeys("<Space>"), nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	return km
}

// インサートモードの <Del>（insert.delete-char）は、Vim 9.1 のインサートモードの <Del> と同じ結果になる。
// アンドゥの単位、ドットリピート、回数による繰り返し、マクロ、カーソルキーで区切った後の扱いは <BS> と同じである。
//
// 期待値は、Vim 9.1（パッチ 1-16）を `vim -Nu NONE -n --not-a-term` で起動し、:set backspace=indent,eol,start の後に
// text を置いてアンドゥの履歴を消し、keys を feedkeys(keys, "xt") で入力した後のテキストとカーソル（行:バイト）である（PR の説明に方法を書いた）。
//
// req: FR-127
func TestInsertDeleteCharVim(t *testing.T) {
	runInsertCases(t, input.DefaultKeymap(), []insertCase{
		{"a|bc", "i<Del><Esc>", "ac", "0:0"},
		{"a|bc", "i<Del><Del><Del><Esc>", "a", "0:0"},
		{"a|bc", "A<Del><Esc>", "abc", "0:2"},
		{"ab|c\nde", "A<Del><Esc>", "abcde", "0:2"},
		{"ab|c\n  de", "A<Del><Esc>", "abc  de", "0:2"},
		{"ab|c\n\nde", "A<Del><Esc>", "abc\nde", "0:2"},
		{"|\nde", "i<Del><Esc>", "de", "0:0"},
		{"|", "i<Del><Esc>", "", "0:0"},
		{"|", "i<Del>x<Esc>", "x", "0:0"},
		{"ab|c\nde", "jA<Del><Esc>", "abc\nde", "1:1"},
		{"ab|c\nde", "jA<Del>x<Esc>", "abc\ndex", "1:2"},
		{"あ|いう", "i<Del><Esc>", "あう", "0:0"},
		{"あい|う", "a<Del><Esc>", "あいう", "0:6"},
		{"あい|う\nえ", "A<Del><Esc>", "あいうえ", "0:6"},
		{"a|bc", "ix<Del>y<Esc>", "axyc", "0:2"},
		{"a|bcdef", "ix<Del>y<Esc>u", "abcdef", "0:1"},
		{"a|bcdef", "ix<Del>y<Esc>l.", "axyxydef", "0:4"},
		{"a|bcdef", "i<Del><Esc>l.", "adef", "0:0"},
		{"a|bcdef", "i<Del><Del><Esc>u", "abcdef", "0:1"},
		{"a|bcdef", "i<Del><Esc>u", "abcdef", "0:1"},
		{"a|bcdef", "3ix<Del><Esc>", "axxxef", "0:3"},
		{"a|bcdefgh", "3i<Del><Esc>", "aefgh", "0:0"},
		{"ab|c\nde\nfg", "A<Del><Esc>j.", "abcde\nfg", "1:1"},
		{"a|bcdef\nabcdef", "qai<Del>x<Esc>qj0l@a", "axcdef\naxcdef", "1:1"},
		{"a|bcdef", "ix<Left><Del><Esc>", "abcdef", "0:0"},
		{"a|bcdef", "ix<Left><Del><Esc>u", "axbcdef", "0:1"},
		{"a|bcdef", "ix<Left><Del><Esc>l.", "acdef", "0:0"},
		{"a|bcdef", "ix<Del><Left><Esc>u", "abcdef", "0:1"},
		{"a|bc\nd", "A<Del><Del><Esc>", "abc", "0:2"},
		{"ab|c\nde", "A<Del><Esc>u", "abc\nde", "0:2"},
	})
}

// insert.delete-to-line-end は、カーソルから行末まで消し、行末の位置では次の行をつなげる（Emacs の C-k）。
// 消したテキストはレジスターと履歴に入れない。アンドゥの単位、ドットリピート、回数による繰り返し、マクロは <BS> と同じ扱いである。
//
// req: FR-127
func TestInsertDeleteToLineEnd(t *testing.T) {
	runInsertCases(t, emacsKeymap(t), []insertCase{
		{"a|bcdef", "i<C-k><Esc>", "a", "0:0"},
		{"ab|c\nde", "A<C-k><Esc>", "abcde", "0:2"},
		{"ab|c\n  de", "A<C-k><Esc>", "abc  de", "0:2"},
		{"a|bc", "A<C-k><Esc>", "abc", "0:2"},
		{"|", "i<C-k>x<Esc>", "x", "0:0"},
		{"あ|いう", "i<C-k><Esc>", "あ", "0:0"},
		{"a|bcdef", "i<C-k><Esc>u", "abcdef", "0:1"},
		{"a|bc\nabc", "i<C-k>x<Esc>j0l.", "ax\nax", "1:1"},
		{"a|bc\nabc", "qai<C-k>x<Esc>qj0l@a", "ax\nax", "1:1"},
		{"a|bc\nd\ne", "3i<C-k><Esc>", "a\ne", "0:0"},
	})
}

// insert.delete-prev-word（<C-w>）は、カーソルの前の単語を db と同じ区切りで消し、行の先頭では前の行とつなげる。行をまたいでは消さない。
// insert.delete-to-line-start（<C-u>）は、カーソルから列 0 まで消し（字下げの手前で止めない）、行の先頭では何もしない。
// どちらも、このインサートで入力した文字より前の文字も消す。消したテキストはレジスターと履歴に入れない。
// アンドゥの単位、ドットリピート、回数による繰り返し、マクロは <BS> と同じ扱いである。
//
// 期待値は、TestInsertDeleteCharVim と同じ方法で Vim 9.1（パッチ 1-16）の <C-w> と <C-u> から取った。
// 「Vim と違う」とした行は #260 の決めによる値で、Vim の値をコメントに書いた（docs/vim-differences.md）。
//
// req: FR-128
func TestInsertDeleteWordAndLineStart(t *testing.T) {
	runInsertCases(t, emacsKeymap(t), []insertCase{
		// <C-w>。
		{"foo b|ar", "A<C-w><Esc>", "foo ", "0:3"},
		{"foo bar baz|x", "i<C-w><Esc>", "foo bar x", "0:7"},
		{"foo bar  |", "A<C-w><Esc>", "foo ", "0:3"},
		{"foo.bar|", "A<C-w><Esc>", "foo.", "0:3"},
		{"foo..|", "A<C-w><Esc>", "foo", "0:2"},
		{"abc\n|def", "i<C-w><Esc>", "abcdef", "0:2"},
		{"abc\n|def", "i<C-w><C-w><Esc>", "def", "0:0"},
		{"|abc", "i<C-w><Esc>", "abc", "0:0"},
		{"  |foo", "i<C-w><Esc>", "foo", "0:0"},
		{"  foo|", "A<C-w><Esc>", "  ", "0:1"},
		{"  foo|", "A<C-w><C-w><Esc>", "", "0:0"},
		{"abc\n  |foo", "i<C-w><C-w><Esc>", "abcfoo", "0:2"},
		{"|", "i<C-w><Esc>", "", "0:0"},
		{"abc\n|", "i<C-w><Esc>", "abc", "0:2"},
		{"abc\n   |\nx", "A<C-w><Esc>", "abc\n\nx", "1:0"},
		// 多バイト文字、全角空白、結合文字。
		{"あいう|", "A<C-w><Esc>", "", "0:0"},
		{"漢字かな|", "A<C-w><Esc>", "漢字", "0:3"},
		{"abcあいう|", "A<C-w><Esc>", "abc", "0:2"},
		{"あ　い|", "A<C-w><Esc>", "あ　", "0:3"},
		// 全角空白（U+3000）の直後では、db と同じく全角空白を飛ばしてその前の単語まで消す（Vim と違う。Vim の <C-w> は
		// 全角空白を空白と見ず、全角空白だけを消して "あ" と "foo" になる）。
		{"あ　|", "A<C-w><Esc>", "", "0:0"},
		{"foo　|", "A<C-w><Esc>", "", "0:0"},
		{"x éé|", "A<C-w><Esc>", "x ", "0:1"},
		// このインサートで入力した文字より前も消す（Vim は入力の始まりで 1 度止まり、どちらも "abc def" になる）。
		{"abc def|", "Ax<C-w><Esc>", "abc ", "0:3"},
		{"abc def|", "A x<C-w><C-w><Esc>", "abc ", "0:3"},
		// <C-u>。
		{"abc def|", "A<C-u><Esc>", "", "0:0"},
		{"abc d|ef", "i<C-u><Esc>", "ef", "0:0"},
		{"  abc def|", "A<C-u><Esc>", "", "0:0"},
		{"|", "i<C-u><Esc>", "", "0:0"},
		{"abc\n   |\nx", "A<C-u><Esc>", "abc\n\nx", "1:0"},
		{"  |abc", "i<C-u><Esc>", "abc", "0:0"},
		{"あい|う", "i<C-u><Esc>", "う", "0:0"},
		// 行の先頭では何もしない（Vim は前の行とつなぎ、"abcdef" になる）。
		{"abc\n|def", "i<C-u><Esc>", "abc\ndef", "1:0"},
		{"|abc", "i<C-u>x<Esc>", "xabc", "0:0"},
		// このインサートで入力した文字より前も消す（Vim は入力の始まりで 1 度止まり、"abc def" になる）。
		{"abc def|", "Axy<C-u><Esc>", "", "0:0"},
		// ドットリピート、マクロ、回数による繰り返し、アンドゥ。
		{"a|bc def\nabc def", "A<C-w><Esc>j.", "abc \nabc ", "1:3"},
		{"a|bc def\nabc def", "A<C-u>x<Esc>j.", "x\nx", "1:0"},
		{"a|bc def", "A<C-w><Esc>u", "abc def", "0:6"},
		{"a|bc def", "A<C-u>x<Esc>u", "abc def", "0:6"},
		{"a|bc def\nabc def", "qaA<C-w><Esc>qj@a", "abc \nabc ", "1:3"},
		{"a|bc def\nabc def", "qaA<C-u>x<Esc>qj@a", "x\nx", "1:0"},
		{"a|bc def", "3A x<C-w><Esc>", "abc def   ", "0:9"},
		// 回数による繰り返しでも、入力した文字より前を消す（Vim は "abc def"）。
		{"a|bc def", "3Ax<C-u><Esc>", "", "0:0"},
		{"a|bc def", "3Ax<C-u>y<Esc>", "y", "0:0"},
	})
}

// レジスターのテキストから再生するマクロでも、2 つの操作が働く（PR #261 のレビューの推奨 1）。
// "ay$ で取った行の生の 0x17 と 0x15 は、@a で <C-w> と <C-u> として読む（internal/command の keysFromText）。u で再生の前に戻る。
// 期待値は Vim 9.1 の <C-w> と <C-u> で取った値と同じである。"ay$ がレジスターの履歴に 1 件積むので、runInsertCases は使わない。
//
// req: FR-128
func TestInsertDeleteWordFromRegisterText(t *testing.T) {
	for _, tc := range []struct{ line, keys, want, cursor string }{
		{"A\x17\x1b", `"ay$j@a`, "abc ", "1:3"},
		{"A\x17\x1b", `"ay$j@au`, "abc def", "1:6"},
		{"A\x15x\x1b", `"ay$j@a`, "x", "1:0"},
		{"A\x15x\x1b", `"ay$j@au`, "abc def", "1:6"},
	} {
		buf := buffer.New(tc.line + "\nabc def\n")
		st := command.New(buf, input.NewParser(emacsKeymap(t)))
		for _, k := range editcmd.MustParseKeys(tc.keys) {
			st.Feed(k)
		}
		c := st.Cursor()
		want := tc.line + "\n" + tc.want
		if got, cursor := strings.TrimSuffix(buf.Text(), "\n"), fmt.Sprintf("%d:%d", c.Line, c.Byte); got != want || cursor != tc.cursor || st.Mode() != command.ModeNormal {
			t.Errorf("%q %s: text %q cursor %s mode %v, want %q %s", tc.line, tc.keys, got, cursor, st.Mode(), want, tc.cursor)
		}
	}
}

// config.example.jsonc の Emacs 風の 10 個のキーと、<BS> として届く Ctrl-H は、#257 と #260 の表のとおりに働く。行頭、行末、最初と最後の行、空のバッファ、全角文字の上では、
// 動けない移動と消すものの無い削除は何もしない。
//
// req: FR-126, FR-127
func TestEmacsInsertKeys(t *testing.T) {
	runInsertCases(t, emacsKeymap(t), []insertCase{
		{"ab|c def", "i<C-a>X<Esc>", "Xabc def", "0:0"},
		{"ab|c def", "i<C-e>X<Esc>", "abc defX", "0:7"},
		{"ab|c", "i<C-f>X<Esc>", "abcX", "0:3"},
		{"ab|c", "i<C-b>X<Esc>", "aXbc", "0:1"},
		{"abc\nd|ef", "i<C-p>X<Esc>", "aXbc\ndef", "0:1"},
		{"a|bc\ndef", "i<C-n>X<Esc>", "abc\ndXef", "1:1"},
		{"ab|c", "i<BS><Esc>", "ac", "0:0"},
		{"ab|c", "i<C-d><Esc>", "ab", "0:1"},
		{"a|bc", "i<C-k><Esc>", "a", "0:0"},
		// 行頭、行末、最初と最後の行。
		{"|abc", "i<C-b>X<Esc>", "Xabc", "0:0"},
		{"|abc", "i<C-a>X<Esc>", "Xabc", "0:0"},
		{"ab|c", "A<C-f>X<Esc>", "abcX", "0:3"},
		{"ab|c", "A<C-e>X<Esc>", "abcX", "0:3"},
		{"a|bc", "i<C-n>X<Esc>", "aXbc", "0:1"},
		{"a|bc", "i<C-p>X<Esc>", "aXbc", "0:1"},
		{"abc\n|de", "i<BS><Esc>", "abcde", "0:2"},
		{"|abc", "i<BS>X<Esc>", "Xabc", "0:0"},
		// 空のバッファ。
		{"|", "i<C-a><C-e><C-f><C-b><C-n><C-p><BS><C-d><C-k><C-w><C-u>X<Esc>", "X", "0:0"},
		// 全角文字の上。
		{"あ|いう", "i<C-f>X<Esc>", "あいXう", "0:6"},
		{"あ|いう", "i<C-b>X<Esc>", "Xあいう", "0:0"},
		{"あ|いう", "i<BS><Esc>", "いう", "0:0"},
		{"あ|いう", "i<C-d><Esc>", "あう", "0:0"},
		{"あいう\nか|きく", "i<C-p>X<Esc>", "あXいう\nかきく", "0:3"},
		{"あ|いう\nかきく", "i<C-n>X<Esc>", "あいう\nかXきく", "1:3"},
	})
}

// インサートモードで入力した制御文字（キーの列の展開、マクロの再生、レジスターと端末からの貼り付け）は、削除の操作と取り違えず、
// 文字として入る（PR #258 のレビューの推奨 1）。ドットリピートと回数による繰り返しでも文字のまま入る。
// 削除の操作のキー（<C-k>、<Del>、<BS>）を割り当てていても変わらない。
//
// req: FR-127
func TestInsertControlChars(t *testing.T) {
	cfg := Config{
		Keymap: map[string]map[string]string{
			"insert": {"<F2>": "[\u000b]", "<F3>": "[\u007f]", "<F4>": "[\b]"},
		},
		KeymapActions: map[string]map[string]string{"insert": emacsInsertKeys},
	}
	dir := writeFiles(t, map[string]string{"a.txt": "hello world\nxy\n"})
	cfg.WorkDir = dir
	h := start(t, cfg, "a.txt")
	ctrl := "[\v][\x7f][\b]"
	h.keys("lli<F2><F3><F4><Esc>")
	h.settle()
	if got, want := h.text(), "he"+ctrl+"llo world\nxy"; got != want {
		t.Fatalf("キーの列の展開: text %q, want %q", got, want)
	}
	h.keys("0.")
	h.settle()
	if got, want := h.text(), ctrl+"he"+ctrl+"llo world\nxy"; got != want {
		t.Errorf("ドットリピート: text %q, want %q", got, want)
	}
	// マクロの記録と再生、回数による繰り返し。
	h.keys("jqa0i<F2><Esc>qu@a2i<F3><Esc>")
	h.settle()
	if got, want := h.text(), ctrl+"he"+ctrl+"llo world\n[\v[\x7f][\x7f]]xy"; got != want {
		t.Errorf("マクロと回数: text %q, want %q", got, want)
	}
	// レジスターからの貼り付け（p）と、インサートモードでの端末からの貼り付け。端末からの貼り付けは、main と同じく制御文字を捨てる。
	h.e.do(func() { h.e.cur.state.Registers().Set('a', command.Register{Text: "<\v\x7f\b>"}) })
	h.keys(`0"aP`)
	h.settle()
	h.keys("A")
	h.paste("{\v\x7f\b}")
	h.keys("<Esc>")
	h.settle()
	if got, want := h.text(), ctrl+"he"+ctrl+"llo world\n<\v\x7f\b>[\v[\x7f][\x7f]]xy{}"; got != want {
		t.Errorf("貼り付け: text %q, want %q", got, want)
	}
}

// 設定を書かない状態では、インサートモードの修飾キーの付いたキーは何もしない（今までどおり）。
//
// req: FR-127
func TestInsertUnmappedKeys(t *testing.T) {
	runInsertCases(t, input.DefaultKeymap(), []insertCase{
		{"a|bc", "i<C-a><C-e><C-k><C-d><C-h><S-Left><Insert>X<Esc>", "aXbc", "0:1"},
	})
}

// 操作の表のインサートモードの操作の ID（internal/input）と、実装のある ID（internal/command）は同じである。
// 提供者へ振り分ける操作（プレビューのスクロール。FR-159）は、internal/editor がポップアップのキーの口で実行するので除く。
//
// req: FR-127
func TestInsertActionIDs(t *testing.T) {
	var table []string
	for _, a := range input.Actions() {
		if a.Modes&input.MapInsert != 0 && !a.Provider {
			table = append(table, a.ID)
		}
	}
	impl := command.InsertActionIDs()
	slices.Sort(table)
	slices.Sort(impl)
	if !slices.Equal(table, impl) {
		t.Errorf("操作の表 %v, 実装 %v", table, impl)
	}
}

// 設定の keymap の右辺の操作は、ノーマルモード、ビジュアルモード、インサートモードのキーに割り当てられ、文字列の右辺と並べて使える。
// 操作を割り当てたキーを含むマクロの記録と再生、ドットリピートは同じ結果になる。テキストのバッファのポップアップの中でも効く（FR-71）。
// 使えない項目は設定の誤りとして知らせ、その項目だけを使わない。キーの一覧では出どころを「設定」にする。
//
// req: FR-126, FR-127
func TestKeymapActionConfig(t *testing.T) {
	// <C-h> は、Ctrl-H を区別して送る端末（偽の端末）での割り当てとして足す。
	insert := map[string]string{"<BS>": "insert.delete-char", "<C-h>": "insert.delete-prev-char"}
	for k, v := range emacsInsertKeys {
		insert[k] = v
	}
	cfg := Config{
		Keymap: map[string]map[string]string{"normal": {"Q": "dd"}, "insert": {"jk": "<Esc>"}},
		KeymapActions: map[string]map[string]string{
			"normal":      {"<C-e>": "line-end", "Q": "line-start", "<Leader>x": "no-such"},
			"visual":      {"<C-e>": "line-end"},
			"insert":      insert,
			"commandLine": {"<C-a>": "line-start"},
		},
	}
	dir := writeFiles(t, map[string]string{"a.txt": "abc def\nxyz\nfoo\n", "b.txt": "bar\n"})
	cfg.WorkDir = dir
	h := start(t, cfg, "a.txt")
	if msg := h.message(); !strings.HasPrefix(msg, "設定の誤り 3 件") {
		t.Errorf("message = %q", msg)
	}
	// ノーマルモードとビジュアルモードの操作。文字列の右辺（Q の dd）は操作の項目（Q）より先に効く。
	h.keys("<C-e>")
	h.settle()
	var c buffer.Position
	h.e.do(func() { c = h.e.cur.state.Cursor() })
	if c != (buffer.Position{Line: 0, Byte: 6}) {
		t.Errorf("normal <C-e>: cursor %v", c)
	}
	h.keys("0v<C-e>")
	h.settle()
	var mode command.Mode
	h.e.do(func() { c, mode = h.e.cur.state.Cursor(), h.e.cur.state.Mode() })
	if c.Line != 0 || c.Byte < 6 || mode != command.ModeVisual {
		t.Errorf("visual <C-e>: cursor %v, mode %v", c, mode)
	}
	h.keys("<Esc>")
	h.settle()
	// インサートモードの操作と、文字列の右辺（jk）。
	h.keys("i<C-e>!jk")
	h.settle()
	if got := h.text(); got != "abc def!\nxyz\nfoo" {
		t.Errorf("i<C-e>!jk: text %q", got)
	}
	// 上書きした <BS> は insert.delete-char、<C-h> は insert.delete-prev-char である。
	h.keys("0i<BS><Right><C-h>X<Esc>")
	h.settle()
	if got := h.text(); got != "Xc def!\nxyz\nfoo" {
		t.Errorf("<BS> と <C-h>: text %q", got)
	}
	// マクロの記録と再生、ドットリピート。
	h.keys("jqa0i<C-e>-<C-a>+<Esc>qj@a")
	h.settle()
	if got := h.text(); got != "Xc def!\n+xyz-\n+foo-" {
		t.Errorf("マクロ: text %q", got)
	}
	h.keys("0i<C-k>Z<Esc>k.")
	h.settle()
	if got := h.text(); got != "Xc def!\nZ\nZ" {
		t.Errorf("ドットリピート: text %q", got)
	}
	// キーの一覧。
	var rows []keyRow
	h.e.do(func() { rows = h.e.keyRows("", nil) })
	var userCA, builtinBS, defaultBS bool
	for _, r := range rows {
		switch {
		case r.Label == "<C-a>" && r.Modes == input.MapInsert:
			userCA = r.Source == sourceUser && r.Description == "行の先頭へ" && r.Ref.ID == "insert.line-start"
		case r.Label == "<BS>" && r.Modes == input.MapInsert && r.Source == sourceUser:
			builtinBS = r.Ref.ID == "insert.delete-char"
		case r.Label == "<BS>" && r.Modes == input.MapInsert:
			defaultBS = r.Ref.ID == "insert.delete-prev-char" && r.Overridden == input.MapInsert
		}
	}
	if !userCA || !builtinBS || !defaultBS {
		t.Errorf("キーの一覧: <C-a> %v, 設定の <BS> %v, 既定の <BS> %v", userCA, builtinBS, defaultBS)
	}

	// テキストのバッファのポップアップの中のインサートモード（FR-71）。
	h.keys(":w<CR>:e b.txt<CR>:popup<CR>A<C-a>P<C-e>Q<C-b><C-d><Esc>")
	h.settle()
	if fi, ok := h.float(); !ok || fi.text != "Pbar" {
		t.Errorf("ポップアップの中: %+v %v", fi, ok)
	}
}

// 設定の keymap.normal で <C-a> に select-all を割り当てると、<C-a> と y で、カーソルも画面も動かさずに
// バッファの全体をヤンクし、ホストのクリップボードへ送る（#263 の「この上に何が載るか」）。ジャンプリストにも記録しない。
// 既定の <Leader>a（#364）は、TestSelectAllDefaultKey が確かめる。
//
// req: FR-129, FR-130
func TestSelectAllConfig(t *testing.T) {
	text := numberedLines("line", 60, "")
	dir := writeFiles(t, map[string]string{"a.txt": text})
	cfg := Config{WorkDir: dir, KeymapActions: map[string]map[string]string{"normal": {"<C-a>": "select-all"}}}
	h := start(t, cfg, "a.txt")
	h.keys("40Gw")
	h.settle()
	var top int
	var cur buffer.Position
	var jumps string
	h.e.do(func() { top, cur, jumps = h.e.renderer.Top(), h.e.cur.state.Cursor(), fst(h.e.jumpList()) })
	if top == 0 || cur.Line != 39 {
		t.Fatalf("前提: top %d cursor %v", top, cur)
	}
	h.keys("<C-a>")
	h.settle()
	h.e.do(func() {
		st := h.e.cur.state
		start, end, linewise, ok := st.Selection()
		if !ok || linewise || st.Mode() != command.ModeVisual || start != (buffer.Position{}) || end != (buffer.Position{Line: 59, Byte: 7}) {
			t.Errorf("selection = %v-%v linewise %v ok %v mode %v", start, end, linewise, ok, st.Mode())
		}
		if st.Cursor() != cur || h.e.renderer.Top() != top {
			t.Errorf("cursor %v top %d, want %v %d", st.Cursor(), h.e.renderer.Top(), cur, top)
		}
	})
	h.keys("y")
	h.waitClipboard([]string{strings.TrimSuffix(text, "\n")})
	h.e.do(func() {
		st := h.e.cur.state
		if st.Mode() != command.ModeNormal || st.Cursor() != cur || h.e.renderer.Top() != top || fst(h.e.jumpList()) != jumps {
			t.Errorf("y の後: mode %v cursor %v top %d jumps %q, want %v %d %q", st.Mode(), st.Cursor(), h.e.renderer.Top(), fst(h.e.jumpList()), cur, top, jumps)
		}
	})
}

// fst は、jumpList の最初の戻り値（ジャンプリストの並び）を返す。
func fst(list string, _ int, _ string) string { return list }

// 本体の既定の Y は行の末尾までをヤンクし、U はリドゥする（#280）。設定の keymap.normal で Y に yank-line を割り当てると、
// Y は Vim と同じく行の全体をヤンクする。U と Y を割り当てても、ビジュアルモードの U（大文字にする）と Y（行をヤンクする）は変わらない。
//
// req: FR-145, FR-146
func TestCaseKeysConfig(t *testing.T) {
	head := func(h *harness) (r command.Register) {
		h.settle()
		h.e.do(func() { r, _ = h.e.cur.state.Registers().Head() })
		return r
	}
	t.Run("既定", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{"a.txt": "alpha beta\ngamma\n"})
		h := start(t, Config{WorkDir: dir}, "a.txt")
		h.keys("wY")
		if r := head(h); r.Text != "beta" || r.Linewise {
			t.Errorf("Y の後のレジスター = %+v", r)
		}
		h.keys("xxuuU")
		h.settle()
		if got := h.text(); got != "alpha eta\ngamma" {
			t.Errorf("xxuuU の後 = %q", got)
		}
		h.keys("2U")
		h.settle()
		if got := h.text(); got != "alpha ta\ngamma" {
			t.Errorf("2U の後 = %q", got)
		}
		h.keys("u<C-r>")
		h.settle()
		if got := h.text(); got != "alpha ta\ngamma" {
			t.Errorf("u<C-r> の後 = %q", got)
		}
		h.keys("0vlU<Esc>")
		h.settle()
		if got := h.text(); got != "ALpha ta\ngamma" {
			t.Errorf("vlU の後 = %q", got)
		}
		h.keys("vY")
		if r := head(h); r.Text != "ALpha ta" || !r.Linewise {
			t.Errorf("vY の後のレジスター = %+v", r)
		}
	})
	t.Run("Y を yank-line に戻す", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{"a.txt": "alpha beta\ngamma\n"})
		h := start(t, Config{WorkDir: dir, KeymapActions: map[string]map[string]string{"normal": {"Y": "yank-line"}}}, "a.txt")
		h.keys("wY")
		if r := head(h); r.Text != "alpha beta" || !r.Linewise {
			t.Errorf("Y の後のレジスター = %+v", r)
		}
		h.keys("jY")
		if r := head(h); r.Text != "gamma" || !r.Linewise {
			t.Errorf("jY の後のレジスター = %+v", r)
		}
	})
}
