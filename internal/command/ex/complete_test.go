package ex

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/option"
)

// Complete は、行末の語の位置と種類を、Execute と同じ表で読んで決める。範囲は読み飛ばし、"|" で続けたコマンドは最後のコマンドを読む。
// 補う引数を持たないコマンドと知らないコマンドは、補うものが無い（<Tab> は文字として入る）。
// 拡張機能のコマンドの口から呼ぶコマンドは、別名を付け替えた後の ID で返す。
//
// req: FR-195
func TestCompleteContext(t *testing.T) {
	aliases := NewAliases()
	if err := aliases.Add(RenameAlias{Name: "tabedit", Min: 4, Target: tabNewName}, nil); err != nil {
		t.Fatal(err)
	}
	if err := aliases.Add(RenameAlias{Name: "ff", Min: 2, Target: "finder.files"}, []string{"finder.files"}); err != nil {
		t.Fatal(err)
	}
	st := &State{
		Aliases: aliases,
		ExtCommand: func(id string) (string, bool) {
			switch id {
			case "finder.files", "acp.prompt":
				return strings.Split(id, ".")[0], true
			case "help", "h":
				return " help", true
			}
			return "", false
		},
	}
	for _, tt := range []struct {
		line    string
		kind    CompletionKind
		start   int
		command string
	}{
		{"", CompleteCommand, 0, ""},
		{"e", CompleteCommand, 0, ""},
		{"  :e", CompleteCommand, 3, ""},
		{"%s", CompleteCommand, 1, ""},
		{"'<,'>s", CompleteCommand, 5, ""},
		{"3,", CompleteCommand, 2, ""},
		{"finder.f", CompleteCommand, 0, ""},
		{"3|d", CompleteCommand, 2, ""},
		{"e ", CompleteFile, 2, ""},
		{"e a", CompleteFile, 2, ""},
		{"e! ~/a", CompleteFile, 3, ""},
		{"edit dir/sp\\ a", CompleteFile, 5, ""},
		{"w a b", CompleteFile, 4, ""},
		{"wq a", CompleteFile, 3, ""},
		{"x a", CompleteFile, 2, ""},
		{"tab.new a", CompleteFile, 8, ""},
		{"tabe a", CompleteFile, 5, ""},
		{"diffs a", CompleteFile, 6, ""},
		{"w | e a", CompleteFile, 6, ""},
		{"w a\\|b", CompleteFile, 2, ""},
		{"b a", CompleteBuffer, 2, ""},
		{"bd foo bar", CompleteBuffer, 3, ""},
		{"he ", CompleteNothing, 0, ""},
		{"help fo", CompleteExtCommand, 5, "help"},
		{"h", CompleteCommand, 0, ""},
		{"h  x", CompleteExtCommand, 3, "h"},
		{"acp.prompt a|b", CompleteExtCommand, 11, "acp.prompt"},
		{"ff x", CompleteExtCommand, 3, "finder.files"},
		{"w !ls", CompleteNothing, 0, ""},
		{"w >>a", CompleteNothing, 0, ""},
		{"!ls", CompleteNothing, 0, ""},
		{"q ", CompleteNothing, 0, ""},
		{"s/a/b", CompleteNothing, 0, ""},
		{"g/a/e x", CompleteNothing, 0, ""},
		{"nosuch a", CompleteNothing, 0, ""},
		{"e3", CompleteCommand, 0, ""},
		{"e3 ", CompleteNothing, 0, ""},
		{"q! ", CompleteNothing, 0, ""},
		{"d! ", CompleteNothing, 0, ""},
		{`" e a`, CompleteNothing, 0, ""},
	} {
		c := Complete(st, tt.line)
		if c.Kind != tt.kind || c.Start != tt.start || c.Command != tt.command {
			t.Errorf("Complete(%q) = %+v, want kind %d start %d command %q", tt.line, c, tt.kind, tt.start, tt.command)
		}
	}
}

// 設定の名前は、:option.get、:option.set、:option.toggle（真偽の項目だけ）では正規の名前を、:set では Vim の名前を、名前の順に補う。
// :set の名前は、長い名前か短い名前が語で始まる項目の長い名前である。"no" の後には真偽の項目の名前を補う。
// 値は、真偽なら true と false、語の列なら最後の区切りの後の語、それ以外は今の値（:set では "\" を付けた形）である。
//
// req: FR-195
func TestCompleteOptions(t *testing.T) {
	opts := &fakeOptions{vals: map[string]option.Value{
		option.CommentString:           option.StringValue("// %s"),
		option.CommandLineCompleteMode: option.StringValue("longest:full,full"),
	}}
	st := &State{Options: opts}
	for _, tt := range []struct {
		line  string
		start int
		want  []string
	}{
		{"option.get lineNumber.", 11, []string{option.LineNumberMinWidth, option.LineNumberShow}},
		{"option.toggle lineNumber.", 14, []string{option.LineNumberShow}},
		{"option.set commandLine.", 11, []string{option.CommandLineCompleteIgnoreCase, option.CommandLineCompleteMode, option.CommandLinePopup}},
		{"option.set lineNumber.show ", 27, []string{"true", "false"}},
		{"option.set lineNumber.show f", 27, []string{"false"}},
		{"option.set tab.width ", 21, []string{"8"}},
		{"option.set tab.width 4", 21, nil},
		{"option.set comment.string ", 26, []string{"// %s"}},
		{"option.set commandLine.completeMode ", 36, []string{"longest:full,full", "full", "longest", "noselect"}},
		{"option.set commandLine.completeMode longest:", 44, []string{"full", "longest", "noselect"}},
		{"option.set commandLine.completeMode full,l", 41, []string{"longest"}},
		{"set nu", 4, []string{"number", "numberwidth"}},
		{"set ts", 4, []string{"tabstop"}},
		{"se s", 3, []string{"scrolloff", "shiftwidth", "sidescrolloff"}},
		{"set nu ts", 7, []string{"tabstop"}},
		{"set no", 6, []string{"autoindent", "expandtab", "hlsearch", "list", "number", "undofile", "wildignorecase", "wrap"}},
		{"set nonu", 6, []string{"number"}},
		{"set wild", 4, []string{"wildignorecase", "wildmode"}},
		{"set ts=", 7, []string{"8"}},
		{"set cms=", 8, []string{`//\ %s`}},
		{"set wildmode=", 13, []string{"longest:full,full", "full", "longest", "noselect"}},
		{"set wildmode=longest:f", 21, []string{"full"}},
		{"set w|set nu", 10, []string{"number", "numberwidth"}},
	} {
		c := Complete(st, tt.line)
		if c.Kind != CompleteList || c.Start != tt.start || !slices.Equal(c.Candidates, tt.want) {
			t.Errorf("Complete(%q) = %+v, want start %d candidates %q", tt.line, c, tt.start, tt.want)
		}
	}
	for _, line := range []string{"set number=", "set nosuch=", "option.set nosuch ", "option.get a b"} {
		if c := Complete(st, line); c.Kind != CompleteNothing {
			t.Errorf("Complete(%q) = %+v, want 補うものが無い", line, c)
		}
	}
	// "?"、"!"、"&" の後は、候補の無い補う所である（<Tab> は何も入れない。Neovim 0.12.5 と同じ）。
	for _, line := range []string{"set nu!", "set ts?", "set ts&"} {
		if c := Complete(st, line); c.Kind != CompleteList || len(c.Candidates) != 0 || c.Start != len(line) {
			t.Errorf("Complete(%q) = %+v, want 候補の無い補う所", line, c)
		}
	}
}

// :set wildmode と :option.set commandLine.completeMode は、語の列として読める値だけを入れ、読めない値は E474 にする
// （Neovim 0.12.5 の :set wildmode=foo と同じ番号）。:set wildignorecase と :set pumheight も別名で使える。
//
// req: FR-196
func TestSetCompleteMode(t *testing.T) {
	opts := &fakeOptions{}
	st := &State{Options: opts}
	for _, line := range []string{"set wildmode=longest:full,full", "option.set commandLine.completeMode noselect:full", "set wildignorecase pumheight=12"} {
		if _, err := Execute(buffer.New("x\n"), st, 0, line); err != nil {
			t.Errorf("%s: %v", line, err)
		}
	}
	want := []string{
		option.CommandLineCompleteMode + "=longest:full,full", option.CommandLineCompleteMode + "=noselect:full",
		option.CommandLineCompleteIgnoreCase + "=true", option.CompletionMenuHeight + "=12",
	}
	if !slices.Equal(opts.sets, want) {
		t.Errorf("sets = %q, want %q", opts.sets, want)
	}
	for _, line := range []string{"set wildmode=list:full", "option.set commandLine.completeMode full,,full"} {
		_, err := Execute(buffer.New("x\n"), st, 0, line)
		if e := (*Error)(nil); !errors.As(err, &e) || e.Code != codeInvalidArg {
			t.Errorf("%s = %v, want E474", line, err)
		}
	}
}

// testEnv は、ファイルの引数を展開する口の偽物である。ホームディレクトリは /h、環境変数は MYVAR=sub と D=x y である。
type testEnv struct{}

func (testEnv) Home() string { return "/h" }

func (testEnv) LookupEnv(name string) (string, bool) {
	switch name {
	case "MYVAR":
		return "sub", true
	case "D":
		return "x y", true
	}
	return "", false
}

// 補完の候補に入れる形（EscapeFileArg）は、:e と :w が開く名前（ExpandFileArg）に戻る。"~" と環境変数の展開の文字、空白、
// "|"、"\" の前に "\" を付けるので、名前に "$" や先頭の "~" があっても、名前そのもののファイルを開く（Neovim 0.12.5 と同じ）。
//
// req: FR-195, FR-200
func TestEscapeFileArg(t *testing.T) {
	st := &State{FileEnv: testEnv{}}
	for _, tt := range []struct{ name, arg string }{
		{"a.txt", "a.txt"},
		{"sp ace.txt", `sp\ ace.txt`},
		{"a|b", `a\|b`},
		{"日本 語/x", `日本\ 語/x`},
		{"note$MYVAR.txt", `note\$MYVAR.txt`},
		{"Outer$Inner.class", `Outer\$Inner.class`},
		{`back\slash.txt`, `back\\slash.txt`},
		{"~", `\~`},
		{"~foo/~bar", `\~foo/~bar`},
		{"a\tb", "a\\\tb"},
	} {
		if got := EscapeFileArg(tt.name); got != tt.arg {
			t.Errorf("EscapeFileArg(%q) = %q, want %q", tt.name, got, tt.arg)
		}
		// :e と :w で開くファイルの名前も同じになる。
		for _, cmd := range []string{"e ", "w "} {
			res, err := Execute(buffer.New("x\n"), st, 0, cmd+tt.arg)
			if err != nil || len(res.Actions) != 1 || res.Actions[0].File != tt.name {
				t.Errorf(":%s%s = %+v, %v", cmd, tt.arg, res.Actions, err)
			}
		}
	}
	for _, tt := range []struct{ name, arg string }{
		{"a b|c", `a\ b\|c`},
		{"$x", "$x"},
	} {
		if got := EscapeBufferArg(tt.name); got != tt.arg {
			t.Errorf("EscapeBufferArg(%q) = %q, want %q", tt.name, got, tt.arg)
		}
		if got := UnescapeBufferArg(tt.arg); got != tt.name {
			t.Errorf("UnescapeBufferArg(%q) = %q, want %q", tt.arg, got, tt.name)
		}
	}
}

// ファイルの引数の先頭の "~" と、"\" の付かない環境変数を展開する。"\$" と "\~" は、その文字そのものにする。
// 値の無い環境変数と "~ユーザー名" は、そのまま残す（Vim と違う。docs/vim-differences.md）。展開の口が無ければ展開しない。
//
// req: FR-200
func TestExpandFileArg(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"~", "/h"},
		{"~/a", "/h/a"},
		{"~x/a", "~x/a"},
		{"a~/b", "a~/b"},
		{`\~`, "~"},
		{`\~/a`, "~/a"},
		{"$MYVAR/a", "sub/a"},
		{"${MYVAR}/a", "sub/a"},
		{"note$MYVAR.txt", "notesub.txt"},
		{`note\$MYVAR.txt`, "note$MYVAR.txt"},
		{"$D/$NOSUCH/a", "x y/$NOSUCH/a"},
		{"a$", "a$"},
		{"${MYVAR", "${MYVAR"},
		{"${}", "${}"},
		{`back\slash`, `back\slash`},
		{`back\\slash`, `back\slash`},
		{`sp\ ace`, "sp ace"},
		{`end\`, `end\`},
	} {
		if got := ExpandFileArg(tt.in, testEnv{}); got != tt.want {
			t.Errorf("ExpandFileArg(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := ExpandFileArg(`~/$MYVAR\ x`, nil); got != "~/$MYVAR x" {
		t.Errorf("展開の口の無い ExpandFileArg = %q", got)
	}
	if !HasEnvRef("a$b") || HasEnvRef(`a\$b`) || HasEnvRef("ab") {
		t.Error("HasEnvRef")
	}
}
