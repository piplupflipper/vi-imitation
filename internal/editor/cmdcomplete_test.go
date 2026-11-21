package editor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// completionFiles は、ファイルの補完を試すディレクトリの中身である。Neovim 0.12.5 と比べたディレクトリ（PR の説明）と同じ形にした。
var completionFiles = map[string]string{
	"Alpha.txt": "", "alpha2.go": "", "alpine.md": "", ".hidden": "", "sp ace.txt": "",
	"foo.c": "", "foo.h": "", "foo.o": "", "beta/one.txt": "", "beta/two.txt": "", ".hdir/x": "",
}

// selAttr は、試験で色の名前 popup.selected に付ける色である。
var selAttr = ui.Attr{Bg: ui.RGB(9, 9, 9)}

// startCompletion は、files を置いた作業ディレクトリで、40 列 10 行の偽の端末で起動する。options は設定の値である。
func startCompletion(t *testing.T, files map[string]string, options map[string]option.Value) *harness {
	t.Helper()
	dir := writeFiles(t, files)
	term := newFakeTerm()
	term.w, term.h = 40, 10
	h := startTerm(t, term, Config{WorkDir: dir, Options: options, Colors: map[string]ui.Attr{ui.StylePopupSelected: selAttr, ui.StylePopupMatch: {}}})
	h.settle()
	return h
}

// cmdline は、カーソルのあるバッファのコマンドラインの文字列を返す。
func (h *harness) cmdline() string {
	var s string
	h.e.do(func() { s = h.e.cur.state.CommandLine() })
	return s
}

// menu は、出している候補の一覧の項目と選んでいる項目を、"a [b] c" の形で返す。出していなければ空を返す。
func (h *harness) menu() string {
	var s string
	h.e.do(func() {
		m := h.e.wildMenuFrame(h.e.cur)
		if m == nil {
			return
		}
		items := slices.Clone(m.Items)
		if m.Selected >= 0 {
			items[m.Selected] = "[" + items[m.Selected] + "]"
		}
		s = strings.Join(items, " ")
	})
	return s
}

// waitCmdline は、コマンドラインが line に、候補の一覧が menu になるまで待つ。
func (h *harness) waitCmdline(what, line, menu string) {
	h.t.Helper()
	h.settle()
	h.eventually(what, func() bool {
		m := h.e.wildMenuFrame(h.e.cur)
		got := ""
		if m != nil {
			items := slices.Clone(m.Items)
			if m.Selected >= 0 {
				items[m.Selected] = "[" + items[m.Selected] + "]"
			}
			got = strings.Join(items, " ")
		}
		return h.e.cur.state.CommandLine() == line && got == menu
	})
}

// 本体の既定（commandLine.completeMode が full。Vim と Neovim の既定）では、<Tab> で最初の候補を入れて一覧を出し、
// 続く <Tab> と <C-n> で次の候補、<S-Tab> と <C-p> で前の候補を入れる。最後の候補の次は打った語に戻る。
// <C-e> は打った語に戻して一覧を閉じ、<C-y> は入れている候補のまま閉じる。候補が 1 つなら、入れて一覧を出さない。
// ファイルの候補は名前のバイトの順で、隠しファイルは語が "." で始まるときだけ出し、Vim の 'suffixes' の既定に当たるファイルは後ろへ回す。
// ディレクトリには "/" を付け、名前の空白の前には "\" を付けて入れ、<CR> で開くファイルは空白を含む名前になる（Neovim 0.12.5 と同じ）。
//
// req: FR-195, FR-196, FR-198, FR-199
func TestCmdlineCompleteFull(t *testing.T) {
	h := startCompletion(t, completionFiles, nil)
	h.keys(":e al<Tab>")
	h.waitCmdline(":e al<Tab>", "e alpha2.go", "[alpha2.go] alpine.md")
	for _, tt := range []struct{ keys, line, menu string }{
		{"<Tab>", "e alpine.md", "alpha2.go [alpine.md]"},
		{"<Tab>", "e al", "alpha2.go alpine.md"},
		{"<C-n>", "e alpha2.go", "[alpha2.go] alpine.md"},
		{"<C-p>", "e al", "alpha2.go alpine.md"},
		{"<S-Tab>", "e alpine.md", "alpha2.go [alpine.md]"},
		{"<Up>", "e alpha2.go", "[alpha2.go] alpine.md"},
		{"<Down>", "e alpine.md", "alpha2.go [alpine.md]"},
		{"<Left>", "e alpha2.go", "[alpha2.go] alpine.md"},
		{"<Right>", "e alpine.md", "alpha2.go [alpine.md]"},
		{"<C-e>", "e al", ""},
		{"<Tab><C-y>", "e alpha2.go", ""},
		{"x", "e alpha2.gox", ""},
	} {
		h.keys(tt.keys)
		h.waitCmdline(tt.keys, tt.line, tt.menu)
	}
	h.keys("<Esc>:e b<Tab>")
	h.waitCmdline(":e b<Tab>", "e beta/", "")
	h.keys("<Tab>")
	h.waitCmdline(":e beta/<Tab>", "e beta/one.txt", "[one.txt] two.txt")
	h.keys("<Esc>:e .<Tab>")
	h.waitCmdline(":e .<Tab>", "e ./", "[./] ../ .hdir/ .hidden")
	h.keys("<Esc>:e f<Tab>")
	h.waitCmdline(":e f<Tab>", "e foo.c", "[foo.c] foo.h foo.o")
	h.keys("<Esc>:e <Tab>")
	h.waitCmdline(":e <Tab>", "e Alpha.txt", "[Alpha.txt] alpha2.go alpine.md beta/ foo.c sp\\ ace.txt foo.h foo.o")
	h.keys("<Esc>:e s<Tab>")
	h.waitCmdline(":e s<Tab>", `e sp\ ace.txt`, "")
	h.keys("<CR>")
	h.settle()
	var name string
	h.e.do(func() { name = h.e.bufferName(h.e.cur) })
	if name != "sp ace.txt" {
		t.Errorf("開いたバッファ = %q", name)
	}
	// 候補の無い語では何も入れず、補うものの無い所（:q の引数）ではタブ文字が入る（Neovim と同じ）。
	h.keys(":e zz<Tab>")
	h.waitCmdline(":e zz<Tab>", "e zz", "")
	h.keys("<Esc>:q <Tab>")
	h.waitCmdline(":q <Tab>", "q \t", "")
	h.keys("<Esc>")
}

// オーナーの nvim の設定（longest:full,full と、大文字と小文字を区別しない）では、1 回目の <Tab> で共通の先頭まで入れて、
// 何も選んでいない一覧を出し、2 回目からの <Tab> で候補を順に入れる。共通の先頭は大文字と小文字を区別して数えるので、
// a に Alpha.txt と alpha2.go が当たると、語は伸びない（Neovim 0.12.5 と同じ）。
// 続けて打った <Tab><Tab><C-y> は、1 回目の <Tab> の候補を集め終える前に届いても、集め終えてから順に効く。
//
// req: FR-196, FR-198, FR-199
func TestCmdlineCompleteLongest(t *testing.T) {
	h := startCompletion(t, completionFiles, map[string]option.Value{
		option.CommandLineCompleteMode:       option.StringValue("longest:full,full"),
		option.CommandLineCompleteIgnoreCase: option.BoolValue(true),
	})
	h.keys(":e a<Tab>")
	h.waitCmdline(":e a<Tab>", "e a", "Alpha.txt alpha2.go alpine.md")
	h.keys("<Tab>")
	h.waitCmdline(":e a<Tab><Tab>", "e Alpha.txt", "[Alpha.txt] alpha2.go alpine.md")
	h.keys("<Esc>:e alp<Tab>")
	h.waitCmdline(":e alp<Tab>", "e alp", "Alpha.txt alpha2.go alpine.md")
	h.keys("<Esc>:e B<Tab>")
	h.waitCmdline(":e B<Tab>", "e beta/", "")
	h.keys("<Tab><Tab><C-y>")
	h.waitCmdline(":e beta/<Tab><Tab><C-y>", "e beta/one.txt", "")
	h.keys("<Esc>")
}

// 一覧を出していて候補を選んでいる間の <CR> は、選んだ候補で決めて一覧を閉じ、コマンドを実行しない。もう一度の <CR> で実行する。
// 一覧を出していない、何も選んでいないときの <CR> は、そのまま実行する。ファイルの引数の語が "/" で終わるときの <CR> は、
// 補完で入れた語でも打った語でも実行せず、コマンドラインに残って続けて中を補える（#363。Vim と Neovim は実行する）。
// 画面の上の中央のポップアップに描くとき（commandLine.popup）も同じに動く。
//
// req: FR-198
func TestCmdlineCompleteEnter(t *testing.T) {
	for _, popup := range []bool{false, true} {
		t.Run(fmt.Sprintf("popup=%v", popup), func(t *testing.T) {
			h := startCompletion(t, completionFiles, map[string]option.Value{option.CommandLinePopup: option.BoolValue(popup)})
			state := func() (mode command.Mode, name string) {
				h.settle()
				h.e.do(func() { mode, name = h.e.cur.state.Mode(), h.e.bufferName(h.e.cur) })
				return mode, name
			}
			wantOpened := func(what, want string) {
				t.Helper()
				if mode, name := state(); mode != command.ModeNormal || name != want {
					t.Errorf("%s: モード = %v、バッファ = %q, want ノーマルモードで %q", what, mode, name, want)
				}
			}
			wantKept := func(what, line string) {
				t.Helper()
				h.waitCmdline(what, line, "")
				if mode, _ := state(); mode != command.ModeCommandLine {
					t.Errorf("%s: モード = %v, want コマンドラインモード", what, mode)
				}
			}

			// <Tab> で選んだ候補の <CR> は決めるだけで、もう一度の <CR> で開く。
			h.keys(":e al<Tab>")
			h.waitCmdline(":e al<Tab>", "e alpha2.go", "[alpha2.go] alpine.md")
			h.keys("<CR>")
			wantKept(":e al<Tab><CR>", "e alpha2.go")
			h.keys("<CR>")
			wantOpened(":e al<Tab><CR><CR>", "alpha2.go")

			// <C-n> と <Down> で選んだ候補も同じ。
			h.keys(":e al<Tab><Down>")
			h.waitCmdline(":e al<Tab><Down>", "e alpine.md", "alpha2.go [alpine.md]")
			h.keys("<CR>")
			wantKept(":e al<Tab><Down><CR>", "e alpine.md")
			h.keys("<CR>")
			wantOpened(":e al<Tab><Down><CR><CR>", "alpine.md")

			// 打った語に戻して何も選んでいない一覧の <CR> は、打った語で実行する。
			h.keys(":e al<Tab><Tab><Tab>")
			h.waitCmdline(":e al<Tab><Tab><Tab>", "e al", "alpha2.go alpine.md")
			h.keys("<CR>")
			wantOpened(":e al<Tab><Tab><Tab><CR>", "al")

			// 候補が 1 つで一覧を出さなかったときの <CR> は実行する。
			h.keys(":e alpha2<Tab>")
			h.waitCmdline(":e alpha2<Tab>", "e alpha2.go", "")
			h.keys("<CR>")
			wantOpened(":e alpha2<Tab><CR>", "alpha2.go")

			// ディレクトリの候補を決めた後の <CR> は実行せず、続けて中を補える。
			h.keys(":e b<Tab>")
			h.waitCmdline(":e b<Tab>", "e beta/", "")
			h.keys("<CR>")
			wantKept(":e b<Tab><CR>", "e beta/")
			h.keys("<Tab>")
			h.waitCmdline(":e b<Tab><CR><Tab>", "e beta/one.txt", "[one.txt] two.txt")
			h.keys("<CR><CR>")
			wantOpened(":e b<Tab><CR><Tab><CR><CR>", "beta/one.txt")

			// 一覧で選んだディレクトリも、決めた後の <CR> で実行しない。打った "/" で終わる語と :w も同じ。
			h.keys(":e <Tab><Tab><Tab><Tab>")
			h.waitCmdline(":e <Tab>×4", "e beta/", `Alpha.txt alpha2.go alpine.md [beta/] foo.c sp\ ace.txt foo.h foo.o`)
			h.keys("<CR>")
			wantKept(":e <Tab>×4<CR>", "e beta/")
			h.keys("<CR>")
			wantKept(":e <Tab>×4<CR><CR>", "e beta/")
			h.keys("<Esc>:w beta/<CR>")
			wantKept(":w beta/<CR>", "w beta/")
			h.keys("<Esc>")

			// コマンドの名前の候補も、選んでいれば決めるだけである。
			h.keys(":set nu<Tab>")
			h.waitCmdline(":set nu<Tab>", "set number", "[number] numberwidth")
			h.keys("<CR>")
			wantKept(":set nu<Tab><CR>", "set number")
			h.keys("<CR>")
			wantOpened(":set nu<Tab><CR><CR>", "beta/one.txt")
		})
	}
}

// オーナーの nvim の設定（longest:full,full）では、1 回目の <Tab> の後の一覧は何も選んでいないので、<CR> は共通の先頭のまま実行する。
// 2 回目の <Tab> で選んだ後の <CR> は、選んだ候補で決めるだけである（#363）。
//
// req: FR-198
func TestCmdlineCompleteEnterLongest(t *testing.T) {
	h := startCompletion(t, completionFiles, map[string]option.Value{
		option.CommandLineCompleteMode: option.StringValue("longest:full,full"),
	})
	h.keys(":e alp<Tab>")
	h.waitCmdline(":e alp<Tab>", "e alp", "alpha2.go alpine.md")
	h.keys("<Tab><CR>")
	h.waitCmdline(":e alp<Tab><Tab><CR>", "e alpha2.go", "")
	h.keys("<Esc>:e alp<Tab><CR>")
	h.settle()
	var name string
	h.e.do(func() { name = h.e.bufferName(h.e.cur) })
	if name != "alp" {
		t.Errorf(":e alp<Tab><CR> のバッファ = %q", name)
	}
}

// マクロを記録している間に補うと、補った後の文字が記録に入り（<Tab> は入らない）、再生すると同じコマンドラインになる。
// 再生の中の <Tab> は補わずにタブ文字を入れる（Vim と同じ）。
//
// req: FR-195
func TestCmdlineCompleteMacro(t *testing.T) {
	h := startCompletion(t, map[string]string{"alpha.txt": "a\n", "beta.txt": "b\n"}, nil)
	h.keys("qa:e al<Tab>")
	h.waitCmdline(":e al<Tab>", "e alpha.txt", "")
	h.keys("<CR>q")
	h.settle()
	var reg string
	h.e.do(func() {
		r, _ := h.e.cur.state.Registers().Get('a')
		reg = r.Text
	})
	if want := ":e al" + "pha.txt" + "\r"; reg != want {
		t.Errorf(`"a = %q, want %q`, reg, want)
	}
	h.keys(":e beta.txt<CR>@a")
	h.settle()
	h.e.do(func() { reg = h.e.bufferName(h.e.cur) })
	if reg != "alpha.txt" {
		t.Errorf("@a の後のバッファ = %q", reg)
	}
}

// コマンドの名前、:set と :option.set の設定の名前と値、バッファ、:help の語、:ext の引数を補う。
// コマンドの名前には、本体が同じコマンドのために足した短い名前（:h、:mes、:int）を出さない。
//
// req: FR-195
func TestCmdlineCompleteKinds(t *testing.T) {
	h := startCompletion(t, map[string]string{"alpha.txt": "a\n", "dir/alpine.txt": "b\n"}, nil)
	h.keys(":e alpha.txt<CR>:e dir/alpine.txt<CR>")
	h.settle()
	for _, tt := range []struct{ keys, line, menu string }{
		{":he<Tab>", "help", ""},
		{":mes<Tab>", "messages", ""},
		{":int<Tab>", "intro", ""},
		{":ex<Tab>", "ext", ""},
		{":ext <Tab>", "ext sync", "[sync] update"},
		{":set nu<Tab>", "set number", "[number] numberwidth"},
		{":set wildm<Tab>", "set wildmode", ""},
		{":set wildmode=<Tab>", "set wildmode=full", "[full] longest noselect"},
		{":option.set commandLine.completeMode l<Tab>", "option.set commandLine.completeMode longest", ""},
		{":option.toggle mouse.<Tab>", "option.toggle mouse.enabled", ""},
		{":b al<Tab>", "b alpha.txt", "[alpha.txt] dir/alpine.txt"},
		{":b pine<Tab>", "b dir/alpine.txt", ""},
		{":help :option.se<Tab>", "help :option.set", ""},
	} {
		h.keys(tt.keys)
		h.waitCmdline(tt.keys, tt.line, tt.menu)
		h.keys("<Esc>")
	}
}

// 候補の一覧は、コマンドラインの行の真上に、補う語の列から並べ、その 1 列左に空白を置く。選んでいる項目は popup.selected の色にする。
// 一覧の幅は、項目の幅と 15（Vim の 'pumwidth'）の大きい方である。completionMenu.height で行の数を限る。
//
// req: FR-197
func TestCmdlineCompleteScreen(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"a1", "a2", "a3", "a4", "a5"} {
		files[n] = ""
	}
	h := startCompletion(t, files, map[string]option.Value{option.CompletionMenuHeight: option.IntValue(3)})
	h.keys(":e a<Tab>")
	h.waitCmdline(":e a<Tab>", "e a1", "[a1] a2 a3 a4 a5")
	h.eventually("一覧を描く", func() bool { return strings.HasPrefix(h.screenRow(6), "~  a1") })
	for row, want := range map[int]string{6: "~  a1", 7: "~  a2"} {
		if got := h.screenRow(row); !strings.HasPrefix(got, want) {
			t.Errorf("%d 行目 = %q, want %q で始まる", row, got, want)
		}
	}
	if got := h.screenRow(8); !strings.HasPrefix(got[2:], " a3") {
		t.Errorf("8 行目 = %q", got)
	}
	if got := searchMarks(h.term, 6, selAttr); got != "  "+strings.Repeat("^", 16) {
		t.Errorf("選んでいる項目の色 = %q", got)
	}
	if got := h.screenRow(9); got != ":e a1" {
		t.Errorf("コマンドラインの行 = %q", got)
	}
	h.keys("<Esc>")
}

// :e ~/a と :w $名前/b は、ホームディレクトリと環境変数を展開したパスを開いて書く。
//
// req: FR-200
func TestEditExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VI_IMITATION_TEST_DIR", home)
	if err := os.WriteFile(filepath.Join(home, "a.txt"), []byte("home\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := startCompletion(t, nil, nil)
	h.keys(":e ~/a.txt<CR>")
	h.settle()
	if got := h.text(); got != "home" {
		t.Fatalf("text = %q", got)
	}
	h.keys(":w $VI_IMITATION_TEST_DIR/b.txt<CR>")
	h.settle()
	if data, err := os.ReadFile(filepath.Join(home, "b.txt")); err != nil || string(data) != "home\n" {
		t.Errorf("b.txt = %q, %v", data, err)
	}
	// :e ~/ の後の <Tab> は "~/" を残してホームディレクトリの中を補う。
	h.keys(":e ~/a<Tab>")
	h.waitCmdline(":e ~/a<Tab>", "e ~/a.txt", "")
	h.keys("<Esc>")
}

// 補ったファイルの名前は、:e と :w がもう一度展開しない形（"$" と先頭の "~" の前に "\"）で入れるので、名前に "$" があっても、
// その名前のファイルを開いて書く（Neovim 0.12.5 と同じ）。"\$" と "\~" も、その文字のままの名前を開く。
//
// req: FR-195, FR-200
func TestCmdlineCompleteEscapedNames(t *testing.T) {
	t.Setenv("MYVAR", "sub")
	h := startCompletion(t, map[string]string{"note$MYVAR.txt": "keep\n", "~": "tilde\n", "bad\xff.txt": "", "badx.txt": "x\n"}, nil)
	h.keys(":e note<Tab>")
	h.waitCmdline(":e note<Tab>", `e note\$MYVAR.txt`, "")
	h.keys("<CR>")
	h.settle()
	if got := h.text(); got != "keep" {
		t.Fatalf("補った名前で開いたバッファ = %q", got)
	}
	h.keys("ohello<Esc>:w<CR>")
	h.settle()
	var dir string
	h.e.do(func() { dir = h.e.workDir() })
	if _, err := os.Stat(filepath.Join(dir, "notesub.txt")); err == nil {
		t.Error(":w が展開した名前のファイルを作った")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "note$MYVAR.txt")); string(data) != "keep\nhello\n" {
		t.Errorf("note$MYVAR.txt = %q", data)
	}
	h.keys(`:e \~<CR>`)
	h.settle()
	if got := h.text(); got != "tilde" {
		t.Errorf(`:e \~ で開いたバッファ = %q`, got)
	}
	// UTF-8 として正しくない名前は候補にしない（入れると別の名前になる）。
	h.keys(":e bad<Tab>")
	h.waitCmdline(":e bad<Tab>", "e badx.txt", "")
	h.keys("<Esc>")
}

// <Tab> と同じ読み込みで届いた <CR>（貼り付け、tmux の send-keys）と、キーマップの展開の中の <Tab><CR> は、補った後の
// コマンドラインで働く（Neovim 0.12.5 と同じ）。候補を集めている間、その client の残りのキーとキーマップの展開を止めておくためである。
//
// req: FR-199
func TestCmdlineCompleteTypeahead(t *testing.T) {
	files := map[string]string{"alpine.md": "pine\n", "alpha.txt": "", "empty/x": ""}
	dir := writeFiles(t, files)
	h := startTerm(t, newFakeTerm(), Config{WorkDir: dir, Keymap: map[string]map[string]string{
		"normal": {"<Space>e": ":e alpi<Tab><CR>"},
	}})
	h.settle()
	h.keys(":e alpi<Tab><CR>")
	h.settle()
	var name string
	h.e.do(func() { name = h.e.bufferName(h.e.cur) })
	if name != "alpine.md" || h.text() != "pine" {
		t.Fatalf("まとめて送った :e alpi<Tab><CR> のバッファ = %q（%q）", name, h.text())
	}
	h.keys(":tab.new<CR>iscratch<Esc>:w empt<Tab><CR>")
	h.settle()
	if _, err := os.Stat(filepath.Join(dir, "empt")); err == nil {
		t.Error(":w empt<Tab><CR> が補う前の名前のファイルを作った")
	}
	h.keys(":tab.new<CR><Space>e")
	h.settle()
	h.e.do(func() { name = h.e.bufferName(h.e.cur) })
	if name != "alpine.md" {
		t.Errorf("キーマップの :e alpi<Tab><CR> のバッファ = %q", name)
	}
}

// ファイルの候補を集めている間は、その client のキーを列に残し、集め終えて補ってから順に処理する（<Tab> の後の <CR> は、
// 補った後の一覧で働き、選んだ候補で決める。もう一度の <CR> で実行する）。集めている間に <C-c> を打つと、集めるのを取り消し、<C-c> までのキーを捨てて、
// 補う前の語のままコマンドラインに残る（Neovim 0.12.5 の補完の途中の <C-c> と同じ）。
//
// req: FR-199
func TestCmdlineCompleteSlowFiles(t *testing.T) {
	h := startCompletion(t, completionFiles, nil)
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	h.e.do(func() {
		h.e.wildCollect = func(ctx context.Context, q fileQuery) ([]string, []string, error) {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			return collectFiles(context.Background(), q)
		}
	})
	h.keys(":e al<Tab><CR>")
	<-started
	h.eventually("集めている間は <CR> を列に残す", func() bool {
		return h.e.cur.state.CommandLine() == "e al" && h.e.keys.has(h.e.op)
	})
	release <- struct{}{}
	h.waitCmdline("集め終えた後の <CR> は候補を決める", "e alpha2.go", "")
	h.keys("<CR>")
	h.settle()
	var name string
	h.e.do(func() { name = h.e.bufferName(h.e.cur) })
	if name != "alpha2.go" {
		t.Fatalf("集め終えた後に実行した <CR> のバッファ = %q", name)
	}
	h.keys(":e al<Tab>x")
	<-started
	h.keys("<C-c>")
	h.settle()
	if got, menu := h.cmdline(), h.menu(); got != "e al" || menu != "" {
		t.Errorf("<C-c> で取り消した後 = %q、一覧 = %q", got, menu)
	}
	h.keys("<Esc>")
}

// パスの ".." は字面で畳まずに OS に解かせるので、シンボリックリンクの先の親を読み、無いディレクトリを通るパスは読めない
// （Neovim 0.12.5 と同じ）。打ったディレクトリの部分はそのまま候補に残し、環境変数を含めば展開したパスにする。
//
// req: FR-199
func TestCollectFilesPaths(t *testing.T) {
	dir := writeFiles(t, map[string]string{"beta/one.txt": "", "beta/gamma/x": "", "top.txt": ""})
	if err := os.Symlink(filepath.Join(dir, "beta", "gamma"), filepath.Join(dir, "deep")); err != nil {
		t.Fatal(err)
	}
	env := fileEnvCopy{home: dir, env: []string{"D=beta"}}
	for _, tt := range []struct {
		word         string
		items, shown []string
	}{
		{"deep/../", []string{"deep/../gamma/", "deep/../one.txt"}, []string{"gamma/", "one.txt"}},
		{"$D/o", []string{"beta/one.txt"}, []string{"one.txt"}},
		{`\$D/o`, nil, nil},
		{"~/t", []string{"~/top.txt"}, []string{"top.txt"}},
		{"d", []string{"deep/"}, []string{"deep/"}},
	} {
		items, shown, _ := collectFiles(context.Background(), fileQuery{word: tt.word, workDir: dir, env: env})
		if !slices.Equal(items, tt.items) || !slices.Equal(shown, tt.shown) {
			t.Errorf("collectFiles(%q) = %q, %q, want %q, %q", tt.word, items, shown, tt.items, tt.shown)
		}
	}
	if _, _, err := collectFiles(context.Background(), fileQuery{word: "nothere/../", workDir: dir, env: env}); err == nil {
		t.Error("無いディレクトリを通るパスを読めた")
	}
}

// 集める関数が取り消しに応じない（止まったファイルシステムの読み込み）ときも、<C-c> を打てば、集める関数が返るのを待たずに
// 補いを打ち切り、その client のキーの処理に戻る。後で集める関数が返っても、その候補は使わない。
//
// req: FR-199
func TestCmdlineCompleteStuckFiles(t *testing.T) {
	h := startCompletion(t, completionFiles, nil)
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	h.e.do(func() {
		h.e.wildCollect = func(_ context.Context, q fileQuery) ([]string, []string, error) {
			started <- struct{}{}
			<-release // 取り消しを見ない
			return collectFiles(context.Background(), q)
		}
	})
	h.keys(":e al<Tab>")
	<-started
	h.keys("<C-c>")
	h.keys("x")
	h.waitCmdline("<C-c> の後に打った x", "e alx", "")
	h.keys("<Esc>")
	h.settle()
	var mode bool
	h.e.do(func() { mode = h.e.cur.state.Mode() == command.ModeNormal && h.e.op.completionJob == nil })
	if !mode {
		t.Fatal("<C-c> の後の <Esc> でノーマルモードに戻らない")
	}
	close(release)
	h.keys("ia<Esc>")
	h.settle()
	if got := h.text(); got != "a" {
		t.Errorf("集める関数が後で返った後のバッファ = %q", got)
	}
}

// ファイルの候補を集める途中で取り消されたら、誤りを返して候補を返さない。読めないディレクトリも誤りにする。
//
// req: FR-199
func TestCollectFilesCancel(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a": "", "b": ""})
	q := fileQuery{word: "", workDir: dir, env: fileEnvCopy{}}
	items, _, err := collectFiles(context.Background(), q)
	if err != nil || !slices.Equal(items, []string{"a", "b"}) {
		t.Fatalf("collectFiles = %q, %v", items, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if items, _, err := collectFiles(ctx, q); err == nil || items != nil {
		t.Errorf("取り消した collectFiles = %q, %v", items, err)
	}
	q.word = "nosuch/"
	if _, _, err := collectFiles(context.Background(), q); err == nil {
		t.Error("無いディレクトリで誤りにならない")
	}
}
