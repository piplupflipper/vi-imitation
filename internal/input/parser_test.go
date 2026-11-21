package input

import (
	"errors"
	"reflect"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// feed は、表記 s のキーを順に Feed し、全ての結果を返す。
func feed(p *Parser, s string) []editcmd.Result {
	var out []editcmd.Result
	for _, k := range editcmd.MustParseKeys(s) {
		out = append(out, p.Feed(k)...)
	}
	return out
}

func builtin(id string) editcmd.Ref { return editcmd.Ref{ID: id} }

func done(c editcmd.Command) editcmd.Result {
	return editcmd.Result{Command: c, Status: editcmd.StatusDone}
}

// req: FR-02, FR-03, FR-06
func TestParserBuiltin(t *testing.T) {
	invalid := editcmd.Result{Status: editcmd.StatusInvalid}
	canceled := editcmd.Result{Status: editcmd.StatusCanceled}
	tests := []struct {
		name string
		keys string
		want []editcmd.Result
	}{
		{"d2w", "d2w", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 2, CountGiven: true, Operator: builtin("delete"),
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("word-next")},
		})}},
		{"回数は前と後の積", "2d3w", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 6, CountGiven: true, Operator: builtin("delete"),
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("word-next")},
		})}},
		{"レジスターと重ね押し", `"ayy`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Register: 'a', Count: 1, Operator: builtin("yank"),
			Target: editcmd.Target{Kind: editcmd.TargetLine},
		})}},
		{"レジスターの前後の回数", `2"a3yy`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Register: 'a', Count: 6, CountGiven: true, Operator: builtin("yank"),
			Target: editcmd.Target{Kind: editcmd.TargetLine},
		})}},
		{"テキストオブジェクト", `ci"`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("change"), Mode: editcmd.ModeInsert,
			Target: editcmd.Target{Kind: editcmd.TargetTextObject, Ref: builtin("inner-double-quote")},
		})}},
		{"dd", "dd", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete"), Target: editcmd.Target{Kind: editcmd.TargetLine},
		})}},
		{"3dd", "3dd", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 3, CountGiven: true, Operator: builtin("delete"),
			Target: editcmd.Target{Kind: editcmd.TargetLine},
		})}},
		{"cc はインサートモードへ", "cc", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("change"), Mode: editcmd.ModeInsert,
			Target: editcmd.Target{Kind: editcmd.TargetLine},
		})}},
		{"3<Down> は 3j", "3<Down>", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 3, CountGiven: true, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("down")},
		})}},
		{"d<End> は d$", "d<End>", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete"),
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("line-end")},
		})}},
		{"2<PageDown>", "2<PageDown>", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 2, CountGiven: true, Action: builtin("page-down"),
		})}},
		{"オペレーターの後の <PageUp> は誤り", "d<PageUp>", []editcmd.Result{invalid}},
		{"<F1> と <Insert> は既定で何もしない", "<F1><Insert>", []editcmd.Result{invalid, invalid}},
		{"fx", "fx", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("find-forward"), Char: 'x'},
		})}},
		{"f の引数の数字は回数でない", "2f3", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 2, CountGiven: true,
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("find-forward"), Char: '3'},
		})}},
		{"dtx", "dtx", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete"),
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("till-forward"), Char: 'x'},
		})}},
		{"3j", "3j", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 3, CountGiven: true, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("down")},
		})}},
		{"0 はモーション", "0", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("line-start")},
		})}},
		{"10 の 0 は回数", "10j", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 10, CountGiven: true, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("down")},
		})}},
		{"d0", "d0", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete"),
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("line-start")},
		})}},
		{"gg は 2 キーのモーション", "dgg", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete"),
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("file-start")},
		})}},
		{"rx", "3rx", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 3, CountGiven: true, Action: builtin("replace-char"), Char: 'x',
		})}},
		{"x", `"_x`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Register: '_', Count: 1, Action: builtin("delete-char"),
		})}},
		{"省略形の D は回数とレジスターを持つ操作", `"a3D`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Register: 'a', Count: 3, CountGiven: true, Action: builtin("delete-to-line-end"),
		})}},
		{"省略形の C はインサートモードへ", "2C", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 2, CountGiven: true, Action: builtin("change-to-line-end"), Mode: editcmd.ModeInsert,
		})}},
		{"省略形の S はインサートモードへ", "S", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 1, Action: builtin("substitute-line"), Mode: editcmd.ModeInsert,
		})}},
		{"省略形の Y は y$（FR-146）", `"a3Y`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Register: 'a', Count: 3, CountGiven: true, Action: builtin("yank-to-line-end"),
		})}},
		{"U はリドゥ（FR-145）", "2U", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 2, CountGiven: true, Action: builtin("redo"),
		})}},
		{"<C-r> もリドゥ", "<C-r>", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 1, Action: builtin("redo"),
		})}},
		{"i はインサートモードへ", "i", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 1, Action: builtin("insert"), Mode: editcmd.ModeInsert,
		})}},
		{"r<CR> は改行", "r<CR>", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 1, Action: builtin("replace-char"), Char: '\n',
		})}},
		{"f<Tab> はタブ", "f<Tab>", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("find-forward"), Char: '\t'},
		})}},
		{"f<Space>", "f<Space>", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("find-forward"), Char: ' '},
		})}},
		{"マクロの記録", "qa", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindRecordStart, Count: 1, Char: 'a'})}},
		{"マクロの再生", "3@a", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindPlay, Count: 3, CountGiven: true, Char: 'a'})}},
		{"直前のマクロの再生", "@@", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindPlay, Count: 1, Char: '@'})}},
		{"ドットリピート", ".", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindRepeat, Count: 1})}},
		{"回数つきのドットリピート", "3.", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindRepeat, Count: 3, CountGiven: true})}},
		{": はコマンドラインモードへ", ":", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 1, Action: builtin("command-line"), Mode: editcmd.ModeCommandLine,
		})}},

		// 不正な列
		{"dq", "dq", []editcmd.Result{invalid}},
		{"dy", "dy", []editcmd.Result{invalid}},
		{"g の後に無い文字", "gz", []editcmd.Result{invalid}},
		{"オペレーターの後のレジスター", `d"`, []editcmd.Result{invalid}},
		{"レジスターの名前でない", `"<C-a>`, []editcmd.Result{invalid}},
		{"テキストオブジェクトはオペレーターの後だけ", "iw", []editcmd.Result{
			done(editcmd.Command{Kind: editcmd.KindAction, Count: 1, Action: builtin("insert"), Mode: editcmd.ModeInsert}),
			done(editcmd.Command{Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("word-next")}}),
		}},
		{"f の引数に特殊キー", "f<Up>", []editcmd.Result{invalid}},
		{"r の引数に修飾キーつきの文字", "r<C-a>", []editcmd.Result{invalid}},
		// レジスターの名前は検査せずに値へ入れる。使えない名前は command が失敗させてメッセージを出す（基本設計 4.5）。
		{"q の後の名前は検査しない", "q_", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindRecordStart, Count: 1, Char: '_'})}},
		{"@ の後の名前は検査しない", "@0", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindPlay, Count: 1, Char: '0'})}},
		{`" の後の名前は検査しない`, `"0p`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Register: '0', Count: 1, Action: builtin("put-after"),
		})}},
		{"@ の引数に特殊キー", "@<CR>", []editcmd.Result{invalid}},
		{"不正な列の後は初期状態", "dqj", []editcmd.Result{invalid, done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("down")},
		})}},

		// 途中の Esc
		{"オペレーターの後の Esc", "d<Esc>", []editcmd.Result{canceled}},
		{"回数の後の Esc", "3<Esc>j", []editcmd.Result{canceled, done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("down")},
		})}},
		{"テキストオブジェクトの途中の Esc", "di<Esc>", []editcmd.Result{canceled}},
		{"f の引数の代わりの Esc", "f<Esc>", []editcmd.Result{canceled}},
		{"レジスター名の代わりの Esc", `"<Esc>`, []editcmd.Result{canceled}},
		{"q の後の Esc", "q<Esc>", []editcmd.Result{canceled}},
		{"初期状態の Esc", "<Esc>", []editcmd.Result{canceled}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewParser(DefaultKeymap())
			got := feed(p, tt.keys)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s:\n got  %+v\n want %+v", tt.keys, got, tt.want)
			}
			if p.Pending() {
				t.Errorf("%s: 結果を返した後も解釈の途中である", tt.keys)
			}
		})
	}
}

// ノーマルモードの > と < は次のキーを待たない操作で、>> は 2 回ずらす（FR-76）。
// 囲みの sa は対象の後に文字を 1 つ取るオペレーター、sd は文字を 1 つ、sr は文字を 2 つ取る操作である（FR-73〜FR-75）。
// ノーマルモードの s は、それだけでは何にも当たらない（要件定義 1.1）。
//
// req: FR-73, FR-74, FR-75, FR-76
func TestParserShiftAndSurround(t *testing.T) {
	invalid := editcmd.Result{Status: editcmd.StatusInvalid}
	shift := func(id string, n int) editcmd.Result {
		return done(editcmd.Command{Kind: editcmd.KindAction, Count: n, CountGiven: n != 1, Action: builtin(id)})
	}
	tests := []struct {
		name string
		keys string
		want []editcmd.Result
	}{
		{"> は待たずに完成する", ">", []editcmd.Result{shift("indent-line", 1)}},
		{"3< は 3 行", "3<lt>", []editcmd.Result{shift("dedent-line", 3)}},
		{">> は 2 回", ">>", []editcmd.Result{shift("indent-line", 1), shift("indent-line", 1)}},
		{"<< は 2 回", "<lt><lt>", []editcmd.Result{shift("dedent-line", 1), shift("dedent-line", 1)}},
		{"d> は誤り", "d>", []editcmd.Result{invalid}},
		{"saiw)", "saiw)", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("surround-add"), Char: ')',
			Target: editcmd.Target{Kind: editcmd.TargetTextObject, Ref: builtin("inner-word")},
		})}},
		{"2sa3w(", "2sa3w(", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 6, CountGiven: true, Operator: builtin("surround-add"), Char: '(',
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("word-next")},
		})}},
		{"sd(", "sd(", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 1, Action: builtin("surround-delete"), Char: '(',
		})}},
		{"2sd\"", `2sd"`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 2, CountGiven: true, Action: builtin("surround-delete"), Char: '"',
		})}},
		{"sr\"'", `sr"'`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 1, Action: builtin("surround-replace"), Char: '"', Char2: '\'',
		})}},
		{"sr の後の文字に <Tab>", "sr(<Tab>", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindAction, Count: 1, Action: builtin("surround-replace"), Char: '(', Char2: '\t',
		})}},
		{"sr の後の文字に矢印は誤り", "sr(<Up>", []editcmd.Result{invalid}},
		{"sr の途中の Esc", "sr(<Esc>", []editcmd.Result{{Status: editcmd.StatusCanceled}}},
		{"s の後の x は誤り", "sx", []editcmd.Result{invalid}},
		{"s の後の Esc", "s<Esc>", []editcmd.Result{{Status: editcmd.StatusCanceled}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewParser(DefaultKeymap())
			got := feed(p, tt.keys)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s:\n got  %+v\n want %+v", tt.keys, got, tt.want)
			}
			if len(tt.want) > 0 && p.Pending() {
				t.Errorf("%s: 結果を返した後も解釈の途中である", tt.keys)
			}
		})
	}
	// sa の対象の後と、sr の 1 つ目の文字の後は、解釈の途中である。
	p := NewParser(DefaultKeymap())
	for _, keys := range []string{"saiw", "sr(", "s"} {
		if rs := feed(p, keys); len(rs) != 0 || !p.Pending() {
			t.Errorf("%s: got %+v, Pending %v", keys, rs, p.Pending())
		}
		feed(p, "<Esc>")
	}
}

// req: FR-02
func TestParserPending(t *testing.T) {
	p := NewParser(DefaultKeymap())
	for _, s := range []string{"2", "d", "3", "i"} {
		if rs := feed(p, s); len(rs) != 0 {
			t.Fatalf("%q の時点で結果 %+v を返した", s, rs)
		}
		if !p.Pending() {
			t.Fatalf("%q の後で Pending が false", s)
		}
	}
}

// req: FR-02
func TestParserCountClamp(t *testing.T) {
	p := NewParser(DefaultKeymap())
	rs := feed(p, "99999999999d99999999999w")
	if len(rs) != 1 || rs[0].Command.Count != MaxCount {
		t.Errorf("got %+v, want Count %d", rs, MaxCount)
	}
}

// 記録中は q だけで記録の停止になり、記録中でなければレジスターの名前を待つ。
func TestParserRecording(t *testing.T) {
	p := NewParser(DefaultKeymap())
	p.SetRecording(true)
	if got, want := feed(p, "q"), []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindRecordStop, Count: 1})}; !reflect.DeepEqual(got, want) {
		t.Errorf("記録中の q:\n got  %+v\n want %+v", got, want)
	}
	// 記録中の状態は、値を返した後や Esc の後も残る。
	feed(p, "<Esc>")
	if got, want := feed(p, "q"), []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindRecordStop, Count: 1})}; !reflect.DeepEqual(got, want) {
		t.Errorf("Esc の後の記録中の q:\n got  %+v\n want %+v", got, want)
	}
	// オペレーターの後の q は記録の停止ではない（q はモーションに無いので不正）。
	if got := feed(p, "dq"); !reflect.DeepEqual(got, []editcmd.Result{{Status: editcmd.StatusInvalid}}) {
		t.Errorf("記録中の dq = %+v", got)
	}
	p.SetRecording(false)
	if rs := feed(p, "q"); len(rs) != 0 {
		t.Errorf("記録中でない q が %+v を返した", rs)
	}
}

// ビジュアルモードでは、オペレーターは対象を待たずに完成し、テキストオブジェクトは移動の値になり、
// ビジュアルモードだけの登録（x、o、行単位の D、C、S、Y）を引く。q と . はマクロとドットリピートとして読まない。
//
// req: FR-01
func TestParserVisual(t *testing.T) {
	p := NewParser(DefaultKeymap())
	p.SetVisual(true)
	tests := []struct {
		keys string
		want []editcmd.Result
	}{
		{"d", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete")})}},
		{`"a3>`, []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindOperator, Register: 'a', Count: 3, CountGiven: true, Operator: builtin("indent")})}},
		{"x", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete")})}},
		{"s", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("change"), Mode: editcmd.ModeInsert})}},
		{"gU", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("uppercase")})}},
		{"D", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete-lines")})}},
		{`"aC`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Register: 'a', Count: 1, Operator: builtin("change-lines"), Mode: editcmd.ModeInsert,
		})}},
		{"S", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("change-lines"), Mode: editcmd.ModeInsert})}},
		{"Y", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("yank-lines")})}},
		// ノーマルモードの U（リドゥ。FR-145）は、ビジュアルモードの u と U（小文字と大文字にする）を変えない。
		{"u", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("lowercase")})}},
		{"U", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("uppercase")})}},
		{"iw", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetTextObject, Ref: builtin("inner-word")},
		})}},
		{"ib", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetTextObject, Ref: builtin("inner-paren")},
		})}},
		{"2w", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindMotion, Count: 2, CountGiven: true, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("word-next")},
		})}},
		{"o", []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindAction, Count: 1, Action: builtin("visual-swap")})}},
		{"q", []editcmd.Result{{Status: editcmd.StatusInvalid}}},
		{".", []editcmd.Result{{Status: editcmd.StatusInvalid}}},
	}
	for _, tt := range tests {
		if got := feed(p, tt.keys); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q:\n got  %+v\n want %+v", tt.keys, got, tt.want)
		}
	}
	// 記録中の q は、ビジュアルモードでも記録の停止である。
	p.SetRecording(true)
	if got, want := feed(p, "q"), []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindRecordStop, Count: 1})}; !reflect.DeepEqual(got, want) {
		t.Errorf("記録中の q:\n got  %+v\n want %+v", got, want)
	}
	p.SetRecording(false)
	// ノーマルモードへ戻ると、o は行を開く操作、d はオペレーターに戻る。
	p.SetVisual(false)
	if got := feed(p, "o"); len(got) != 1 || got[0].Command.Action.ID != "open-below" {
		t.Errorf("ノーマルモードの o = %+v", got)
	}
	if got := feed(p, "d"); len(got) != 0 || !p.Pending() {
		t.Errorf("ノーマルモードの d = %+v", got)
	}
	feed(p, "<Esc>")
	// ノーマルモードの D は、ビジュアルモードの D（delete-lines）ではなく省略形の操作である。
	if got := feed(p, "D"); len(got) != 1 || got[0].Command.Action.ID != "delete-to-line-end" {
		t.Errorf("ノーマルモードの D = %+v", got)
	}
}

// 本体の操作の途中では、Flush は何もせず次のキーを待ち続ける。
//
// req: FR-02
func TestParserFlushKeepsBuiltinPrefix(t *testing.T) {
	tests := []struct {
		before, after string
		want          editcmd.Command
	}{
		{"g", "g", editcmd.Command{Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("file-start")}}},
		{"3g", "g", editcmd.Command{Kind: editcmd.KindMotion, Count: 3, CountGiven: true, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("file-start")}}},
		{"di", "w", editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete"), Target: editcmd.Target{Kind: editcmd.TargetTextObject, Ref: builtin("inner-word")}}},
		{"ci", `"`, editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("change"), Mode: editcmd.ModeInsert, Target: editcmd.Target{Kind: editcmd.TargetTextObject, Ref: builtin("inner-double-quote")}}},
		{`"a`, "x", editcmd.Command{Kind: editcmd.KindAction, Register: 'a', Count: 1, Action: builtin("delete-char")}},
		{"f", "x", editcmd.Command{Kind: editcmd.KindMotion, Count: 1, Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("find-forward"), Char: 'x'}}},
		{"d", "w", editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete"), Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("word-next")}}},
	}
	for _, tt := range tests {
		p := NewParser(DefaultKeymap())
		feed(p, tt.before)
		if rs := p.Flush(); rs != nil {
			t.Errorf("%s の後の Flush が %+v を返した", tt.before, rs)
		}
		if got, want := feed(p, tt.after), []editcmd.Result{done(tt.want)}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s, Flush, %s:\n got  %+v\n want %+v", tt.before, tt.after, got, want)
		}
	}
}

// 拡張機能の宣言（基本設計 5.1 の surround の例）と同じ操作を登録して解釈する。
// 拡張機能の ID、コマンドの ID、オペレーターの文字の引数が値に残る。
//
// req: FR-11, FR-12
func TestParserExtension(t *testing.T) {
	km := DefaultKeymap()
	add := editcmd.Ref{ID: "surround.add", Extension: "surround"}
	inner := editcmd.Ref{ID: "surround.inner", Extension: "surround"}
	if err := km.Bind(Binding{Kind: BindOperator, Keys: editcmd.MustParseKeys("ys"), Ref: add, TakesChar: true}); err != nil {
		t.Fatal(err)
	}
	if err := km.Bind(Binding{Kind: BindTextObject, Keys: editcmd.MustParseKeys("is"), Ref: inner}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		keys string
		want []editcmd.Result
	}{
		{"拡張機能のオペレーターと本体のテキストオブジェクトと文字の引数", "ysiw)", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: add, Char: ')',
			Target: editcmd.Target{Kind: editcmd.TargetTextObject, Ref: builtin("inner-word")},
		})}},
		{"対象の文字の引数とオペレーターの文字の引数", `ystx"`, []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: add, Char: '"',
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("till-forward"), Char: 'x'},
		})}},
		{"拡張機能のオペレーターの重ね押し", "2yss]", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 2, CountGiven: true, Operator: add, Char: ']', Target: editcmd.Target{Kind: editcmd.TargetLine},
		})}},
		{"オペレーターの文字の引数の代わりの Esc", "ysiw<Esc>", []editcmd.Result{{Status: editcmd.StatusCanceled}}},
		{"本体のオペレーターと拡張機能のテキストオブジェクト", "dis", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("delete"),
			Target: editcmd.Target{Kind: editcmd.TargetTextObject, Ref: inner},
		})}},
		{"y は ys の接頭辞だが、次のキーで y に決まる", "yw", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("yank"),
			Target: editcmd.Target{Kind: editcmd.TargetMotion, Ref: builtin("word-next")},
		})}},
		{"yy も y に決まる", "yy", []editcmd.Result{done(editcmd.Command{
			Kind: editcmd.KindOperator, Count: 1, Operator: builtin("yank"), Target: editcmd.Target{Kind: editcmd.TargetLine},
		})}},
		{"y の後の Esc はオペレーターごと捨てる", "y<Esc>", []editcmd.Result{{Status: editcmd.StatusCanceled}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := feed(NewParser(km), tt.keys)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s:\n got  %+v\n want %+v", tt.keys, got, tt.want)
			}
		})
	}
}

// 短い登録に決めた後、残りのキーを続けて解釈する。
//
// req: FR-02, FR-11
func TestParserShorterMatchAndFlush(t *testing.T) {
	km := DefaultKeymap()
	xyz := editcmd.Ref{ID: "x.y", Extension: "ext"}
	if err := km.Bind(Binding{Kind: BindAction, Keys: editcmd.MustParseKeys("xyz"), Ref: xyz}); err != nil {
		t.Fatal(err)
	}
	delChar := done(editcmd.Command{Kind: editcmd.KindAction, Count: 1, Action: builtin("delete-char")})
	yankLine := done(editcmd.Command{Kind: editcmd.KindOperator, Count: 1, Operator: builtin("yank"), Target: editcmd.Target{Kind: editcmd.TargetLine}})
	canceled := editcmd.Result{Status: editcmd.StatusCanceled}

	tests := []struct {
		name  string
		keys  string
		flush bool // keys の後に Flush を呼ぶ
		want  []editcmd.Result
	}{
		{"続かないキーで短い方に決まる", "xyy", false, []editcmd.Result{delChar, yankLine}},
		{"長い登録", "xyz", false, []editcmd.Result{done(editcmd.Command{Kind: editcmd.KindAction, Count: 1, Action: xyz})}},
		{"x の後の Flush は x に決める", "x", true, []editcmd.Result{delChar}},
		{"xy の後の Flush は x に決め、y はオペレーターとして待つ", "xy", true, []editcmd.Result{delChar}},
		{"x の後の Esc は x を実行してから取り消す（Vim と同じ）", "x<Esc>", false, []editcmd.Result{delChar, canceled}},
		{"xy の後の Esc は x を実行し、y を取り消す", "xy<Esc>", false, []editcmd.Result{delChar, canceled}},
		{"回数は短い方に付く", "3x<Esc>", false, []editcmd.Result{
			done(editcmd.Command{Kind: editcmd.KindAction, Count: 3, CountGiven: true, Action: builtin("delete-char")}), canceled,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewParser(km)
			got := feed(p, tt.keys)
			if tt.flush {
				got = append(got, p.Flush()...)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s:\n got  %+v\n want %+v", tt.keys, got, tt.want)
			}
		})
	}

	// xy の Flush の後、y はオペレーターとして待っている。
	p := NewParser(km)
	feed(p, "xy")
	p.Flush()
	if got, want := feed(p, "y"), []editcmd.Result{yankLine}; !reflect.DeepEqual(got, want) {
		t.Errorf("Flush の後の y:\n got  %+v\n want %+v", got, want)
	}
	if rs := p.Flush(); rs != nil {
		t.Errorf("待っていないときの Flush が %+v を返した", rs)
	}
}

// req: FR-11
func TestKeymapBind(t *testing.T) {
	km := DefaultKeymap()
	ext := editcmd.Ref{ID: "e", Extension: "ext"}
	keys := editcmd.MustParseKeys

	conflicts := []Binding{
		{Kind: BindMotion, Keys: keys("x"), Ref: ext},     // 本体の操作 x とノーマルモードで重なる
		{Kind: BindTextObject, Keys: keys("w"), Ref: ext}, // 本体のモーション w とオペレーターの後で重なる
		{Kind: BindOperator, Keys: keys("d"), Ref: ext},   // 本体の d を置き換えない
		{Kind: BindMotion, Keys: keys("gg"), Ref: ext},    // 同じ種類でも置き換えない
	}
	for _, b := range conflicts {
		if err := km.Bind(b); !errors.Is(err, ErrConflict) {
			t.Errorf("Bind(%v %s): err = %v, want ErrConflict", b.Kind, editcmd.KeysString(b.Keys), err)
		}
	}
	// 拡張機能どうしの衝突も拒む。
	if err := km.Bind(Binding{Kind: BindOperator, Keys: keys("ys"), Ref: editcmd.Ref{ID: "a", Extension: "one"}}); err != nil {
		t.Fatal(err)
	}
	if err := km.Bind(Binding{Kind: BindOperator, Keys: keys("ys"), Ref: editcmd.Ref{ID: "b", Extension: "two"}}); !errors.Is(err, ErrConflict) {
		t.Errorf("拡張機能どうしの ys: err = %v, want ErrConflict", err)
	}
	// モードが重ならなければ、同じキーでも登録できる。
	if err := km.Bind(Binding{Kind: BindTextObject, Modes: MapOperatorPending, Keys: keys("d"), Ref: ext}); err != nil {
		t.Errorf("オペレーターの後だけのテキストオブジェクト d: %v", err)
	}
	if err := km.Bind(Binding{Kind: BindAction, Modes: MapInsert, Keys: keys("<C-w>"), Ref: ext}); err != nil {
		t.Errorf("インサートモードの操作: %v", err)
	}
	for _, b := range []Binding{
		{Kind: BindMotion, Ref: ext},
		{Kind: BindMotion, Keys: keys("Q")},
		{Kind: BindMotion, Keys: keys("1Q"), Ref: ext},
		{Kind: BindAction, Keys: keys(`"Q`), Ref: ext},
		{Kind: BindAction, Keys: keys("qQ"), Ref: ext},
		{Kind: BindAction, Keys: keys("@Q"), Ref: ext},
		{Kind: BindAction, Keys: keys(".Q"), Ref: ext},
		{Kind: BindTextObject, Modes: MapNormal, Keys: keys("Q"), Ref: ext},
		{Kind: BindOperator, Modes: MapOperatorPending, Keys: keys("Q"), Ref: ext},
		{Kind: bindLine, Keys: keys("Q"), Ref: ext},
	} {
		if err := km.Bind(b); err == nil || errors.Is(err, ErrConflict) {
			t.Errorf("Bind(%+v): err = %v, want 衝突以外のエラー", b, err)
		}
	}
	// インサートモードの左辺は、表を引く 1 つのキーに限る（FR-127）。文字の列、修飾キーの付かない文字、<Esc>、<CR>、<Tab> は登録できない。
	for _, k := range []string{"qq", "q", "<Esc>", "<CR>", "<Tab>", "<C-w>x"} {
		if err := km.Bind(Binding{Kind: BindAction, Modes: MapInsert, Keys: keys(k), Ref: ext}); err == nil || errors.Is(err, ErrConflict) {
			t.Errorf("インサートモードの %s: err = %v, want 衝突以外のエラー", k, err)
		}
	}
	if err := km.Bind(Binding{Kind: BindAction, Modes: MapInsert, Keys: keys("<S-Tab>"), Ref: ext}); err != nil {
		t.Errorf("インサートモードの <S-Tab>: %v", err)
	}

	// 置き換えは Unbind してから Bind する（利用者の設定による上書き）。
	if !km.Unbind(MapNormal|MapVisual|MapOperatorPending, keys("w")) {
		t.Fatal("Unbind(w) = false")
	}
	if err := km.Bind(Binding{Kind: BindMotion, Keys: keys("w"), Ref: ext}); err != nil {
		t.Fatal(err)
	}
	if b, ok := km.Lookup(MapNormal, keys("w")); !ok || b.Ref != ext {
		t.Errorf("Lookup(w) = %+v, %v", b, ok)
	}
	// 一部のモードだけ取り除く。
	if !km.Unbind(MapNormal, keys("w")) {
		t.Error("Unbind(normal, w) = false")
	}
	if got := feed(NewParser(km), "w"); !reflect.DeepEqual(got, []editcmd.Result{{Status: editcmd.StatusInvalid}}) {
		t.Errorf("Unbind の後の w = %+v", got)
	}
	if got := feed(NewParser(km), "dw"); len(got) != 1 || got[0].Command.Target.Ref != ext {
		t.Errorf("オペレーターの後の w = %+v", got)
	}
	if km.Unbind(MapNormal, keys("w")) {
		t.Error("2 回目の Unbind(normal, w) = true")
	}
}

// 本体の操作の表に誤りが無く、#9 の案の一覧を全て引ける。
//
// req: FR-03
func TestDefaultKeymap(t *testing.T) {
	km := DefaultKeymap()
	tables := []struct {
		mode  MapMode
		kind  BindingKind
		specs []spec
	}{
		{MapNormal, BindMotion, builtinMotions},
		{MapOperatorPending, BindMotion, builtinMotions},
		{MapNormal, BindOperator, builtinOperators},
		{MapOperatorPending, BindTextObject, builtinTextObjects},
		{MapNormal, BindAction, builtinActions},
	}
	for _, tt := range tables {
		for _, s := range tt.specs {
			if s.keys == "" {
				continue // 既定のキーの無い操作（select-all）は登録しない（builtinKeymap）
			}
			mode := tt.mode
			if s.modes != 0 && s.modes&mode == 0 {
				mode = MapVisual // ビジュアルモードだけの登録
			}
			keys, err := editcmd.ParseKeysLeader(s.keys, nil)
			if err != nil {
				t.Fatal(err)
			}
			b, ok := km.Lookup(mode, keys)
			if !ok || b.Kind != tt.kind || b.Ref.ID != s.id || !b.Ref.Builtin() {
				t.Errorf("%v %q: got %+v, %v", tt.kind, s.keys, b, ok)
			}
		}
	}
	// DefaultKeymap は呼ぶたびに別の値を返す。
	if err := km.Bind(Binding{Kind: BindMotion, Keys: editcmd.MustParseKeys("Q"), Ref: editcmd.Ref{ID: "q"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := DefaultKeymap().Lookup(MapNormal, editcmd.MustParseKeys("Q")); ok {
		t.Error("DefaultKeymap が登録を共有している")
	}
}
