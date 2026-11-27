package ui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

var update = flag.Bool("update", false, "testdata の .golden を書き直す")

// testTheme は、ゴールデンで属性を読み分けられるように、名前ごとに違う色を割り当てたテーマである。
func testTheme() Theme {
	return Theme{
		StyleComment:           {Fg: Indexed(2)},
		StyleKeyword:           {Fg: Indexed(3)},
		StyleString:            {Fg: Indexed(4)},
		StyleNonText:           {Fg: Indexed(5)},
		StyleSpecialKey:        {Fg: Indexed(6)},
		StyleMessageError:      {Fg: Indexed(1)},
		StyleMessageWarning:    {Fg: Indexed(7)},
		StyleVisual:            {Reverse: true},
		StyleStatusLine:        {Reverse: true, Bold: true},
		StyleDiagnosticError:   {Undercurl: true, Sp: Indexed(9)},
		StyleDiagnosticWarning: {Underline: true, Sp: Indexed(10)},
		// タブの行は、カーソルのあるタブを反転と太字（'S'）、他のタブを下線（'_'）、空いた列を反転（'v'）で読み分ける。
		StyleTabLine:         {Underline: true},
		StyleTabLineSelected: {Reverse: true, Bold: true},
		StyleTabLineFill:     {Reverse: true},
	}
}

// fgCode は、testTheme の文字の色の番号から、ゴールデンに書く文字への対応である。
var fgCode = map[uint8]byte{1: 'E', 2: 'c', 3: 'k', 4: 's', 5: 'n', 6: 'x', 7: 'W'}

// cellCodes は、1 つのセルの属性を、文字の色の 1 文字と、飾りの 1 文字にする。
//
// 文字の色: '.' は既定、それ以外は fgCode。
// 飾り: ' ' は無し、'S' は反転と太字（ステータス行）、'v' は反転（選択）、'~' は波線、'_' は下線、'*' はそれらの組み合わせ。
func cellCodes(a Attr) (fg, fx byte) {
	fg = '.'
	if n, ok := a.Fg.Index(); ok {
		fg = '?'
		if c, ok := fgCode[n]; ok {
			fg = c
		}
	}
	fx = ' '
	marks := 0
	if a.Reverse && a.Bold {
		fx, marks = 'S', marks+1
	} else if a.Reverse {
		fx, marks = 'v', marks+1
	}
	if a.Undercurl {
		fx, marks = '~', marks+1
	}
	if a.Underline {
		fx, marks = '_', marks+1
	}
	if marks > 1 {
		fx = '*'
	}
	return fg, fx
}

// dump は、画面を、行ごとの文字と属性の行にした文字列を返す。
// 属性の行は、既定でない属性のセルがある行だけに出す。
func dump(s Screen) string {
	g := s.Grid
	var b strings.Builder
	fmt.Fprintf(&b, "size %dx%d cursor %d,%d %s\n", g.Width(), g.Height(), s.Cursor.Row, s.Cursor.Col, cursorDesc(s.Cursor))
	for row := range g.Height() {
		fmt.Fprintf(&b, "   |%s|\n", g.RowText(row))
		fgs, fxs := make([]byte, g.Width()), make([]byte, g.Width())
		plainFg, plainFx := true, true
		for col, c := range g.Row(row) {
			fgs[col], fxs[col] = cellCodes(c.Attr)
			plainFg = plainFg && fgs[col] == '.'
			plainFx = plainFx && fxs[col] == ' '
		}
		if !plainFg {
			fmt.Fprintf(&b, "fg |%s|\n", fgs)
		}
		if !plainFx {
			fmt.Fprintf(&b, "fx |%s|\n", fxs)
		}
	}
	return b.String()
}

// cursorDesc は、カーソルの形と表示の有無をゴールデンに書く語にする。
func cursorDesc(c Cursor) string {
	if !c.Visible {
		return "hidden"
	}
	switch c.Shape {
	case CursorBlock:
		return "block"
	case CursorBar:
		return "bar"
	case CursorUnderline:
		return "underline"
	}
	return "?"
}

// golden は、got を testdata/<name>.golden と比べる。-update なら書き直す。
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s が一致しない\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// step は、ゴールデンの 1 つの画面である。同じ Renderer で順に描き、スクロールの位置を引き継ぐ。
type step struct {
	title string
	frame Frame
}

// renderSteps は、steps を順に描いた画面をつないだ文字列を返す。
func renderSteps(r *Renderer, steps []step) string {
	var b strings.Builder
	for i, s := range steps {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## %s\n", s.title)
		b.WriteString(dump(r.Render(&s.frame)))
	}
	return b.String()
}

func newRenderer(w, h int) *Renderer {
	return &Renderer{Width: w, Height: h, Theme: testTheme()}
}

func pos(line, b int) buffer.Position {
	return buffer.Position{Line: line, Byte: b}
}

func TestGoldenWide(t *testing.T) {
	b := buffer.New("あいうえおかきくけこさしすせそ\nxあいうえおかきくけこさしすせそ\nabc日本語def\n")
	r := newRenderer(11, 6)
	golden(t, "wide", renderSteps(r, []step{
		{"右の端に半分だけ掛かる全角文字は > にする", Frame{Text: b, Name: "a.txt", Cursor: pos(0, 0)}},
		{"全角文字の上のカーソル", Frame{Text: b, Name: "a.txt", Cursor: pos(2, 6)}},
		{"横のスクロールで左の端が欠けた全角文字は <、右の端に掛かる全角文字は >", Frame{Text: b, Name: "a.txt", Cursor: pos(0, 27)}},
		{"左の端より左へ戻るとスクロールを戻す", Frame{Text: b, Name: "a.txt", Cursor: pos(1, 1)}},
	}))
}

// TestGoldenGutter は、表示領域の左に欄を空けたときの描画とカーソルの形を確かめる。
func TestGoldenGutter(t *testing.T) {
	b := buffer.New("abcdefghijklmnopqrstuvwxyz\nあいうえおかきくけこ\n")
	r := &Renderer{Width: 14, Height: 5, GutterWidth: 3, Theme: testTheme()}
	golden(t, "gutter", renderSteps(r, []step{
		{"欄の右からテキストを描く", Frame{Text: b, Cursor: pos(0, 0)}},
		{"横のスクロールは欄を除いた幅で数える", Frame{Text: b, Cursor: pos(0, 12)}},
		{"右の端の全角文字", Frame{Text: b, Cursor: pos(1, 0)}},
		{"インサートモードは縦線", Frame{Text: b, Mode: ModeInsert, Cursor: pos(1, 3)}},
		{"オペレーターの後は下線", Frame{Text: b, OperatorPending: true, Cursor: pos(1, 3)}},
		{"コマンドラインは縦線", Frame{Text: b, Mode: ModeCommandLine, CommandLine: "w", Cursor: pos(1, 3)}},
	}))
	if got := (&Renderer{Width: 10, Height: 2}).Render(&Frame{}).Cursor; got.Visible {
		t.Errorf("表示領域が 0 行ならカーソルを出さない: %+v", got)
	}
}

func TestColor(t *testing.T) {
	if _, ok := ColorDefault.Index(); ok {
		t.Error("ColorDefault.Index が ok")
	}
	if n, ok := Indexed(0).Index(); !ok || n != 0 {
		t.Errorf("Indexed(0).Index() = %d, %v", n, ok)
	}
	if n, ok := Indexed(255).Index(); !ok || n != 255 {
		t.Errorf("Indexed(255).Index() = %d, %v", n, ok)
	}
	c := RGB(0x12, 0x34, 0x56)
	if r, g, b, ok := c.RGB(); !ok || r != 0x12 || g != 0x34 || b != 0x56 {
		t.Errorf("RGB = %x %x %x %v", r, g, b, ok)
	}
	if _, ok := c.Index(); ok {
		t.Error("RGB の色の Index が ok")
	}
	if RGB(0, 0, 0) == ColorDefault || Indexed(0) == ColorDefault || RGB(0, 0, 0) == Indexed(0) {
		t.Error("黒と既定の色が区別できない")
	}
	a := Attr{Fg: Indexed(1)}.Over(Attr{Fg: RGB(1, 2, 3), Underline: true})
	if a.Fg != RGB(1, 2, 3) || !a.Underline {
		t.Errorf("Over = %+v", a)
	}
}

func TestGoldenTab(t *testing.T) {
	b := buffer.New("\tx\na\tb\nabcdefgh\tz\n")
	r := newRenderer(20, 6)
	golden(t, "tab", renderSteps(r, []step{
		{"ノーマルモードではタブの最後の列", Frame{Text: b, Cursor: pos(0, 0)}},
		{"インサートモードではタブの最初の列", Frame{Text: b, Mode: ModeInsert, Cursor: pos(1, 1)}},
		{"8 列ちょうどの後のタブ", Frame{Text: b, Cursor: pos(2, 8)}},
	})+"\n"+renderSteps(&Renderer{Width: 20, Height: 6, Theme: testTheme()}, []step{
		{"タブ幅 4", Frame{Text: b, Cursor: pos(1, 1), TabStop: 4}},
	}))
}

func TestGoldenLongLine(t *testing.T) {
	var line strings.Builder
	for i := range 60 {
		line.WriteByte(byte('a' + i%26))
	}
	b := buffer.New(line.String() + "\nshort\n")
	r := newRenderer(20, 4)
	golden(t, "longline", renderSteps(r, []step{
		{"右の端まで", Frame{Text: b, Cursor: pos(0, 19)}},
		{"1 列はみ出すと、カーソルを幅の半分に置く", Frame{Text: b, Cursor: pos(0, 20)}},
		{"画面の中の移動ではスクロールしない", Frame{Text: b, Cursor: pos(0, 12)}},
		{"行末", Frame{Text: b, Cursor: pos(0, 59)}},
		{"短い行へ移ると左へ戻る", Frame{Text: b, Cursor: pos(1, 4)}},
	}))
}

func TestGoldenLastLines(t *testing.T) {
	var s strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&s, "line %d\n", i)
	}
	b := buffer.New(s.String())
	r := newRenderer(16, 7)
	golden(t, "lastlines", renderSteps(r, []step{
		{"先頭", Frame{Text: b, Name: "n.txt", Cursor: pos(0, 0)}},
		{"1 行下へはみ出す", Frame{Text: b, Name: "n.txt", Cursor: pos(5, 0)}},
		{"最終行へ飛ぶと最終行を下の端に置く", Frame{Text: b, Name: "n.txt", Cursor: pos(19, 0)}},
		{"最終行の付近で上へ", Frame{Text: b, Name: "n.txt", Cursor: pos(15, 3)}},
		{"短いバッファは ~ の行", Frame{Text: buffer.New("one\ntwo\n"), Name: "n.txt", Cursor: pos(1, 0), Modified: true}},
	}))
}

func TestGoldenVisual(t *testing.T) {
	b := buffer.New("abc\n\ndef\nあいう\n")
	r := newRenderer(12, 7)
	golden(t, "visual", renderSteps(r, []step{
		{"文字単位。行をまたぐと行末の 1 セルも選ぶ。カーソルの文字は反転しない", Frame{
			Text: b, Mode: ModeVisual, Cursor: pos(2, 0),
			Selection: Selection{Active: true, Start: pos(0, 1), End: pos(2, 0)},
		}},
		{"文字単位で後ろから選ぶ", Frame{
			Text: b, Mode: ModeVisual, Cursor: pos(0, 1),
			Selection: Selection{Active: true, Start: pos(2, 1), End: pos(0, 1)},
		}},
		{"行単位", Frame{
			Text: b, Mode: ModeVisualLine, Cursor: pos(3, 3),
			Selection: Selection{Active: true, Start: pos(2, 1), End: pos(3, 3), Linewise: true},
		}},
		{"全角文字の選択は 2 セル", Frame{
			Text: b, Mode: ModeVisual, Cursor: pos(3, 6),
			Selection: Selection{Active: true, Start: pos(3, 0), End: pos(3, 6)},
		}},
	}))
}

// req: FR-35
func TestGoldenDecorations(t *testing.T) {
	b := buffer.New("func f() {\n\t// note\n\treturn \"s\"\n}\n")
	r := newRenderer(20, 6)
	decos := NewDecorations([]Highlight{
		{Range: buffer.Range{Start: pos(0, 0), End: pos(0, 4)}, Style: StyleKeyword},
		{Range: buffer.Range{Start: pos(1, 1), End: pos(1, 8)}, Style: StyleComment},
		{Range: buffer.Range{Start: pos(2, 1), End: pos(2, 7)}, Style: StyleKeyword},
		{Range: buffer.Range{Start: pos(2, 8), End: pos(2, 11)}, Style: StyleString},
		{Range: buffer.Range{Start: pos(0, 5), End: pos(0, 6)}, Style: "unknown"},
		{Range: buffer.Range{Start: pos(3, 0), End: pos(3, 1)}, Style: StyleNumber},  // テーマに無い名前は色を付けない
		{Range: buffer.Range{Start: pos(1, 5), End: pos(2, 3)}, Style: StyleString},  // 行をまたぐ。行末の 1 セルは含めない
		{Range: buffer.Range{Start: pos(1, 6), End: pos(1, 7)}, Style: StyleKeyword}, // 後の要素を上に重ねる
	}, b.LineCount())
	diags := []Diagnostic{
		{Range: buffer.Range{Start: pos(0, 2), End: pos(0, 7)}, Severity: SeverityError},
		{Range: buffer.Range{Start: pos(2, 11), End: pos(2, 11)}, Severity: SeverityWarning}, // 行末の空の範囲
		{Range: buffer.Range{Start: pos(1, 4), End: pos(2, 2)}, Severity: SeverityWarning},   // 行をまたぐ
	}
	golden(t, "decorations", renderSteps(r, []step{
		{"構文の色と診断", Frame{Text: b, Cursor: pos(3, 0), Decorations: decos, Diagnostics: diags}},
		{"選択を重ねる", Frame{
			Text: b, Mode: ModeVisual, Cursor: pos(0, 9), Decorations: decos,
			Selection: Selection{Active: true, Start: pos(0, 2), End: pos(0, 9)},
		}},
	}))
}

func TestGoldenSpecialChars(t *testing.T) {
	b := buffer.New("a\x01b\x7fc\n\xe3\x81d\u0085e\n\u200bcafe\u0301!\n\u0301x\n")
	r := newRenderer(20, 6)
	golden(t, "special", renderSteps(r, []step{
		{"制御文字と不正な UTF-8 と結合文字", Frame{Text: b, Cursor: pos(2, 3)}},
	}))
}

func TestGoldenStatusAndCommandLine(t *testing.T) {
	b := buffer.New("hello\n")
	r := newRenderer(24, 4)
	golden(t, "statusline", renderSteps(r, []step{
		{"名前なし", Frame{Text: b, Cursor: pos(0, 4)}},
		{"変更ありと記録中", Frame{Text: b, Name: "dir/file.go", Modified: true, Recording: 'q', Mode: ModeInsert, Cursor: pos(0, 5)}},
		{"幅が足りなければ左の部分を切る", Frame{Text: b, Name: "very/long/path/to/日本語.go", Mode: ModeVisualLine, Cursor: pos(0, 0)}},
		{"コマンドライン", Frame{Text: b, Mode: ModeCommandLine, CommandLine: "s/a/b/g", Cursor: pos(0, 0)}},
		{"長いコマンドラインは末尾を見せる", Frame{Text: b, Mode: ModeCommandLine, CommandLine: "s/abcdefghijklmnop/qrstuvwxyz/g", Cursor: pos(0, 0)}},
		{"エラーのメッセージ", Frame{Text: b, Cursor: pos(0, 0), Message: Message{Text: "E37: 変更が保存されていません\n2 行目", Level: MessageError}}},
		{"空の行のルーラー", Frame{Text: buffer.New(""), Cursor: pos(0, 0)}},
	}))
}

// メッセージの行の右に寄せる文字列（Message.Right。検索の一致の件数）は、右の端の 2 列を空けた位置で終わるように置く
// （Neovim 0.12.5 の 'showcmd' がオフのときの検索の件数と同じ位置）。左の文字列は、右の文字列の手前の 1 列を空けて収まるところまで描く。
// 右の文字列も収まらないほど狭ければ、右の文字列を行の先頭から描く。左の文字列が改行を含むときは、右の文字列を出さない。
//
// req: FR-167
func TestGoldenMessageRight(t *testing.T) {
	b := buffer.New("hello\n")
	wide := renderSteps(newRenderer(24, 4), []step{
		{"件数", Frame{Text: b, Cursor: pos(0, 0), Message: Message{Text: "/foo", Right: "[2/4]"}}},
		{"折り返しの W", Frame{Text: b, Cursor: pos(0, 0), Message: Message{Text: "/foo", Right: "W [1/>999]"}}},
		{"長いパターンは後ろを描かない", Frame{Text: b, Cursor: pos(0, 0), Message: Message{Text: "/abcdefghijklmnopqrstuvwxyz", Right: "[2/4]"}}},
		{"全角の文字のパターン", Frame{Text: b, Cursor: pos(0, 0), Message: Message{Text: "/あいうえおかきく", Right: "[2/4]"}}},
		{"改行を含むメッセージには出さない", Frame{Text: b, Cursor: pos(0, 0), Message: Message{Text: "a\nb", Right: "[2/4]"}}},
	})
	narrow := renderSteps(newRenderer(6, 3), []step{
		{"右の文字列も収まらない幅", Frame{Text: b, Cursor: pos(0, 0), Message: Message{Text: "/foo", Right: "[2/4]"}}},
	})
	golden(t, "message-right", wide+"\n"+narrow)
}

// drawMessageRight は、描く範囲の左の列と幅を受け、範囲の中で同じ規則（右の端の 2 列を空ける、左の文字列の後ろを切る）で描く。
// 範囲の外のセルには描かない。後にコマンドラインをポップアップに出すときに、枠の中で使う。
//
// req: FR-167
func TestMessageRightRange(t *testing.T) {
	r := newRenderer(30, 1)
	for _, tc := range []struct {
		name, text, right string
		col, width        int
		want              string
	}{
		{"範囲の中で右に寄せる", "/foo", "[2/4]", 5, 20, "     /foo         [2/4]       "},
		{"左の文字列の後ろを切る", "/abcdefghijklmnop", "[2/4]", 5, 20, "     /abcdefghijk [2/4]       "},
		{"右の文字列も収まらない範囲", "/foo", "[2/4]", 5, 6, "     [2/4]                    "},
	} {
		g := NewGrid(30, 1)
		r.drawMessageRight(g, 0, tc.col, tc.width, tc.text, tc.right, Attr{})
		if got := g.RowText(0); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestScrollVertical は、縦のスクロールの位置を、端末の Vim 9.1 で測った値と比べる。
// 測り方: 100 行のファイルを `vim -Nu NONE` で開き、表示領域の高さ h の端末で、gg の後に nG（または G の後に nG）を打ち、line('w0') を読んだ。
func TestScrollVertical(t *testing.T) {
	var s strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&s, "%d\n", i)
	}
	b := buffer.New(s.String())
	tests := []struct {
		h        int
		from, to int // 1 から数えた行（Vim の nG）
		wantTop  int // 1 から数えた line('w0')
	}{
		{11, 1, 12, 2},
		{11, 1, 17, 7},
		{11, 1, 18, 13},
		{11, 1, 50, 45},
		{11, 1, 100, 90},
		{11, 100, 89, 89},
		{11, 100, 87, 87},
		{11, 100, 86, 81},
		{11, 100, 85, 80},
		{10, 1, 15, 6},
		{10, 1, 16, 11},
		{10, 100, 88, 88},
		{10, 100, 87, 83},
		{12, 1, 18, 7},
		{12, 1, 19, 13},
		{12, 100, 85, 85},
		{12, 100, 84, 79},
		{21, 1, 32, 12},
		{21, 1, 33, 23},
		{21, 100, 72, 72},
		{21, 100, 71, 61},
		{4, 1, 6, 3},
		{4, 1, 7, 5},
		{4, 100, 96, 96},
		{4, 100, 95, 94},
		{3, 100, 97, 97},
		{3, 100, 96, 95},
	}
	for _, tt := range tests {
		r := &Renderer{Width: 20, Height: tt.h + 2}
		r.Render(&Frame{Text: b, Cursor: pos(tt.from-1, 0)})
		r.Render(&Frame{Text: b, Cursor: pos(tt.to-1, 0)})
		if got := r.Top() + 1; got != tt.wantTop {
			t.Errorf("h=%d %dG→%dG: top %d, want %d", tt.h, tt.from, tt.to, got, tt.wantTop)
		}
	}
}

// TestScrollFitHeight は、表示領域の行数が変わったとき（出力の欄の開け閉め、端末の大きさの変更）の最初の行を、
// 端末の Vim 9.1 で測った値と比べる（#135）。
// 測り方: tmux の 60 列の端末で、Vim（'laststatus' は 2）とこのエディタに同じファイルを開き、同じ操作の後の画面の 1 行目を読んだ。
// 出力の欄は、Vim では :copen 8 と :wincmd p、このエディタでは :output で開いた（高さ 20 の画面で、どちらも表示領域が 9 行になる）。
// zt と zz を使う 2 件は、'laststatus' が 1 の Vim で測り、このエディタでは SetScroll で同じ最初の行にした。
func TestScrollFitHeight(t *testing.T) {
	lines := func(n int) *buffer.Buffer {
		var s strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&s, "line %d\n", i)
		}
		return buffer.New(s.String())
	}
	type op struct {
		height  int  // 画面の行数
		output  bool // 出力の欄を開いているか
		wantTop int  // 1 から数えた最初の行
		cursor  int  // 0 でなければ、同じ描画の中でカーソルをこの行（1 から数える）へ動かす
	}
	tests := []struct {
		name   string
		text   *buffer.Buffer
		height int // 最初の画面の行数
		cursor int // 1 から数えたカーソルの行（Vim の nG）
		top    int // 0 でなければ、最初の描画の後に SetScroll で置く最初の行（1 から数える）
		ops    []op
	}{
		// PR #133 のレビューの例。閉じると全体が出る（閉じても 6 行目からのままにしない）。
		{"14 行で G、欄の開け閉め", lines(14), 20, 14, 0, []op{{20, true, 8, 0}, {20, false, 1, 0}}},
		// G と :output を続けて打ち、1 回の描画にまとまっても、G の後に開いたのと同じ位置にする。
		{"14 行で G と開くのが 1 回の描画", lines(14), 20, 1, 0, []op{{20, true, 8, 14}, {20, false, 1, 0}}},
		{"100 行で G、欄の開け閉め", lines(100), 20, 100, 0, []op{{20, true, 92, 0}, {20, false, 83, 0}}},
		// Vim は G の後の 30G で 22 行目を 1 行目に置いた。中ほどに置く位置が Vim と 1 行ずれることがある（scroll の doc）ので、SetScroll で揃える。
		{"100 行で 30G、端末の大きさの変更", lines(100), 20, 30, 22, []op{{30, false, 17, 0}, {12, false, 26, 0}, {20, false, 22, 0}}},
		// 最終行を表示領域の 1 行目に置いた後（Vim の zt）は、欄を閉じても "~" の行が残る。
		{"最終行が 1 行目", lines(100), 21, 100, 100, []op{{21, true, 100, 0}, {21, false, 100, 0}}},
		// 25 行で 25Gzz の後に大きくすると、"~" の行が残る位置に置く（全体を出すように詰めない）。
		{"25 行で zz の後に大きくする", lines(25), 21, 25, 16, []op{{31, false, 11, 0}}},
	}
	for _, tt := range tests {
		r := &Renderer{Width: 60, Height: tt.height}
		f := &Frame{Text: tt.text, Cursor: pos(tt.cursor-1, 0)}
		r.Render(f)
		if tt.top > 0 {
			r.SetScroll(tt.top-1, 0)
			r.Render(f)
		}
		for i, o := range tt.ops {
			r.Height = o.height
			f.Output = nil
			if o.output {
				f.Output = &Output{Title: "acp", Bottom: -1}
			}
			if o.cursor > 0 {
				f.Cursor = pos(o.cursor-1, 0)
			}
			r.Render(f)
			if got := r.Top() + 1; got != o.wantTop {
				t.Errorf("%s: %d 番目の操作の後の最初の行 = %d, want %d", tt.name, i+1, got, o.wantTop)
			}
		}
	}
}

// TestScrollHorizontal は、横のスクロールの位置を、端末の Vim 9.1（'nowrap'）で測った値と比べる。
// 測り方: 300 文字の行を幅 w の端末で開き、0 の後に n|（または 100| の後に n|）を打ち、winsaveview().leftcol を読んだ。
func TestScrollHorizontal(t *testing.T) {
	b := buffer.New(strings.Repeat("x", 300) + "\n")
	tests := []struct {
		w        int
		from, to int // 1 から数えた列（Vim の n|）
		wantLeft int
	}{
		{40, 1, 40, 0},
		{40, 1, 41, 20},
		{40, 1, 100, 79},
		{40, 100, 99, 79},
		{40, 100, 79, 58},
		{40, 100, 30, 9},
		{40, 100, 20, 0},
		{40, 100, 120, 99},
		{41, 1, 41, 0},
		{41, 1, 42, 21},
		{41, 100, 120, 79},
		{41, 100, 121, 100},
	}
	for _, tt := range tests {
		r := &Renderer{Width: tt.w, Height: 5}
		r.Render(&Frame{Text: b, Cursor: pos(0, tt.from-1)})
		r.Render(&Frame{Text: b, Cursor: pos(0, tt.to-1)})
		if got := r.Left(); got != tt.wantLeft {
			t.Errorf("w=%d %d|→%d|: left %d, want %d", tt.w, tt.from, tt.to, got, tt.wantLeft)
		}
	}
}

// TestRenderSmall は、小さな画面と空の入力で落ちないことを確かめる。
func TestRenderSmall(t *testing.T) {
	for _, size := range [][2]int{{0, 0}, {1, 1}, {1, 2}, {3, 3}, {2, 5}} {
		r := &Renderer{Width: size[0], Height: size[1]}
		frames := []Frame{
			{},
			{Text: buffer.New("あいう\tx\n"), Cursor: pos(5, 99), Mode: ModeCommandLine, CommandLine: "w"},
			{
				Text: buffer.New("abc\n"), Mode: ModeVisual,
				Selection: Selection{Active: true, Start: pos(-1, -1), End: pos(9, 9)},
				Decorations: NewDecorations([]Highlight{
					{Range: buffer.Range{Start: pos(9, 9), End: pos(0, 1)}, Style: StyleKeyword},
					{Range: buffer.Range{Start: pos(-3, -1), End: pos(-1, 0)}, Style: StyleKeyword},
				}, 1),
			},
		}
		for _, f := range frames {
			g := r.Render(&f).Grid
			if g.Width() != size[0] || g.Height() != size[1] {
				t.Errorf("size %v: grid %dx%d", size, g.Width(), g.Height())
			}
		}
	}
}

// TestRenderDoesNotModify は、描画が入力の Frame とバッファを変えないことを確かめる。
func TestRenderDoesNotModify(t *testing.T) {
	b := buffer.New("abc\n")
	f := Frame{Text: b, Cursor: pos(5, 9), Diagnostics: []Diagnostic{{Range: buffer.Range{Start: pos(0, 2), End: pos(0, 0)}, Severity: SeverityError}}}
	before := fmt.Sprintf("%+v", f)
	v := b.Version()
	(&Renderer{Width: 10, Height: 4}).Render(&f)
	if after := fmt.Sprintf("%+v", f); after != before {
		t.Errorf("Frame が変わった: %s → %s", before, after)
	}
	if b.Version() != v || b.Modified() {
		t.Error("バッファが変わった")
	}
}

func TestDefaultTheme(t *testing.T) {
	th := DefaultTheme()
	for _, name := range []string{StyleKeyword, StyleVisual, StyleStatusLine, StyleDiagnosticError, StyleMessageError} {
		if _, ok := th[name]; !ok {
			t.Errorf("DefaultTheme に %s が無い", name)
		}
	}
	if th[StyleVisual] != (Attr{Reverse: true}) {
		t.Errorf("選択は反転で表示する: %+v", th[StyleVisual])
	}
}

// TestTagAndAttributeStyles は、JSX のタグの名前と属性の名前の色の種類（tag、attribute）が、設定の colors に書ける名前で、
// 既定の対応では Vim 9.1 の syntax/html.vim のリンクと同じく keyword と type の色になることを確かめる（FR-331）。
//
// req: FR-331
func TestTagAndAttributeStyles(t *testing.T) {
	th := DefaultTheme()
	for name, like := range map[string]string{StyleTag: StyleKeyword, StyleAttribute: StyleType} {
		if !IsStyleName(name) {
			t.Errorf("IsStyleName(%q) = false", name)
		}
		if a, ok := th[name]; !ok || a != th[like] {
			t.Errorf("DefaultTheme の %s = %+v, want %s と同じ %+v", name, a, like, th[like])
		}
	}
}

// 行をまたぐ範囲が重なり合っても、行をまたがない範囲と同じ順に重ねる。
// 表示の上の端より前の行から始まる範囲、下の端より後の行で終わる範囲、行の途中で終わる範囲を含む。
// 飾りだけの名前（下線）と色だけの名前を重ね、下の範囲の飾りが上の範囲の色の下に残ることも見る。
//
// req: FR-35
func TestGoldenMultilineDecorations(t *testing.T) {
	var text strings.Builder
	for n := range 16 {
		fmt.Fprintf(&text, "line %02d abcdefgh\n", n)
	}
	b := buffer.New(text.String())
	theme := testTheme()
	theme[StyleType] = Attr{Underline: true}
	theme[StyleNumber] = Attr{Fg: Indexed(7)}
	r := &Renderer{Width: 20, Height: 8, Theme: theme}
	hs := []Highlight{
		{Range: buffer.Range{Start: pos(0, 3), End: pos(15, 4)}, Style: StyleType},    // ほぼ全体に下線
		{Range: buffer.Range{Start: pos(1, 5), End: pos(9, 2)}, Style: StyleComment},  // 長い範囲
		{Range: buffer.Range{Start: pos(3, 0), End: pos(4, 0)}, Style: StyleKeyword},  // 次の行の先頭で終わる
		{Range: buffer.Range{Start: pos(4, 9), End: pos(4, 11)}, Style: StyleString},  // 1 行の中
		{Range: buffer.Range{Start: pos(6, 12), End: pos(2, 6)}, Style: StyleString},  // 終わりが開始より前
		{Range: buffer.Range{Start: pos(5, 2), End: pos(5, 6)}, Style: StyleKeyword},  // 長い範囲の下に隠れる
		{Range: buffer.Range{Start: pos(7, 0), End: pos(30, 0)}, Style: StyleNumber},  // 最終行より後で終わる
		{Range: buffer.Range{Start: pos(8, 1), End: pos(11, 3)}, Style: StyleComment}, // 後の要素を上に重ねる
		{Range: buffer.Range{Start: pos(10, 0), End: pos(12, 0)}, Style: "unknown"},   // テーマに無い名前
		{Range: buffer.Range{Start: pos(-5, 0), End: pos(1, 2)}, Style: StyleKeyword}, // 0 行目より前から
		{Range: buffer.Range{Start: pos(13, 4), End: pos(13, 8)}, Style: StyleString},
	}
	decos := NewDecorations(hs, b.LineCount())
	diags := []Diagnostic{
		{Range: buffer.Range{Start: pos(2, 2), End: pos(8, 3)}, Severity: SeverityError},
	}
	golden(t, "multiline", renderSteps(r, []step{
		{"先頭", Frame{Text: b, Cursor: pos(0, 0), Decorations: decos, Diagnostics: diags}},
		{"中ほど", Frame{Text: b, Cursor: pos(10, 0), Decorations: decos, Diagnostics: diags}},
		{"末尾", Frame{Text: b, Cursor: pos(15, 0), Decorations: decos}},
		{"選択を重ねる", Frame{
			Text: b, Mode: ModeVisual, Cursor: pos(12, 3), Decorations: decos,
			Selection: Selection{Active: true, Start: pos(11, 2), End: pos(12, 3)},
		}},
	}))
}
