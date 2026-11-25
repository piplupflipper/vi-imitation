package ext

import (
	"strings"
	"testing"
)

// 部品は、種類、項目の ID の空と重複、改行、色を付ける範囲を検査する。popup/open の置き場所も検査する。
//
// req: FR-72
func TestViewComponentValidate(t *testing.T) {
	item := func(id, label string, children ...ViewItem) ViewItem {
		return ViewItem{ID: id, Label: label, Children: children}
	}
	tests := []struct {
		name string
		p    PopupOpenParams
		want string // 誤りの文の一部。空なら誤りにしない
	}{
		{"テキスト", PopupOpenParams{Component: ViewComponent{Kind: ViewText, Text: "a\nb"}}, ""},
		{"一覧", PopupOpenParams{Placement: PopupCenter, Component: ViewComponent{Kind: ViewList, Items: []ViewItem{item("a", "A"), item("b", "B")}}}, ""},
		{"木", PopupOpenParams{Component: ViewComponent{Kind: ViewTree, Items: []ViewItem{item("d", "D", item("d/a", "A"))}}}, ""},
		{"入力の行", PopupOpenParams{Component: ViewComponent{Kind: ViewInput, Text: "x"}}, ""},
		{"知らない種類", PopupOpenParams{Component: ViewComponent{Kind: "grid"}}, "知らない部品"},
		{"知らない置き場所", PopupOpenParams{Placement: "side", Component: ViewComponent{Kind: ViewText}}, "知らない置き場所"},
		{"入力の行の改行", PopupOpenParams{Component: ViewComponent{Kind: ViewInput, Text: "a\nb"}}, "改行"},
		{"ファインダー", PopupOpenParams{Placement: PopupCenter, Component: ViewComponent{Kind: ViewFinder, Text: "q", Items: []ViewItem{item("a", "A")}}}, ""},
		{"ファインダーの入力の改行", PopupOpenParams{Component: ViewComponent{Kind: ViewFinder, Text: "a\nb"}}, "改行"},
		{"ファインダーの子", PopupOpenParams{Component: ViewComponent{Kind: ViewFinder, Items: []ViewItem{item("d", "D", item("a", "A"))}}}, "tree でだけ"},
		{"空の ID", PopupOpenParams{Component: ViewComponent{Kind: ViewList, Items: []ViewItem{item("", "A")}}}, "id が空"},
		{"木の中の重複", PopupOpenParams{Component: ViewComponent{Kind: ViewTree, Items: []ViewItem{item("d", "D", item("d", "A"))}}}, "重複"},
		{"一覧の子", PopupOpenParams{Component: ViewComponent{Kind: ViewList, Items: []ViewItem{item("d", "D", item("a", "A"))}}}, "tree でだけ"},
		{"表示の文字列の改行", PopupOpenParams{Component: ViewComponent{Kind: ViewList, Items: []ViewItem{item("a", "A\nB")}}}, "改行"},
		{"範囲の外", PopupOpenParams{Component: ViewComponent{Kind: ViewList, Items: []ViewItem{
			{ID: "a", Label: "abc", Highlights: []ViewSpan{{Start: 1, End: 4, Style: "keyword"}}},
		}}}, "label の外"},
		{"書き足すテキストと入力の行の組", PopupOpenParams{Placement: PopupCenter, Component: ViewComponent{
			Kind: ViewConsole, Text: "out\nput\n", Input: "x", Prompt: "> ", History: []string{"a", "b"},
		}}, ""},
		{"組の入力の改行", PopupOpenParams{Component: ViewComponent{Kind: ViewConsole, Input: "a\nb"}}, "input が改行"},
		{"組の前置きの改行", PopupOpenParams{Component: ViewComponent{Kind: ViewConsole, Prompt: ">\n"}}, "prompt が改行"},
		{"組の履歴の改行", PopupOpenParams{Component: ViewComponent{Kind: ViewConsole, History: []string{"a", "b\rc"}}}, "history[1]"},
		{"組の項目", PopupOpenParams{Component: ViewComponent{Kind: ViewConsole, Items: []ViewItem{item("a", "A")}}}, "items"},
	}
	for _, tt := range tests {
		err := tt.p.Validate()
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: %v", tt.name, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
}

// ポップアップのメッセージの JSON の形（基本設計 5.3）。
func TestViewMessageJSON(t *testing.T) {
	roundTrip(t, PopupOpenParams{Placement: PopupCenter, Focus: true, Component: ViewComponent{
		Kind: ViewList, Title: "t", Items: []ViewItem{{ID: "a", Label: "A", Highlights: []ViewSpan{{Start: 0, End: 1, Style: "keyword"}}}},
	}}, `{"component":{"kind":"list","title":"t","items":[{"id":"a","label":"A","highlights":[{"start":0,"end":1,"style":"keyword"}]}]},"placement":"center","focus":true}`)
	roundTrip(t, PopupOpenResult{View: 3}, `{"view":3}`)
	roundTrip(t, ViewSelectParams{View: 3, Item: "a"}, `{"view":3,"item":"a"}`)
	roundTrip(t, ViewKeyParams{View: 3, ID: "k"}, `{"view":3,"id":"k"}`)
	roundTrip(t, InputChangeParams{View: 3, Text: "x"}, `{"view":3,"text":"x"}`)
	roundTrip(t, InputChangeParams{View: 3, Text: "x", Seq: 2}, `{"view":3,"text":"x","seq":2}`)
	roundTrip(t, ViewUpdateParams{View: 3, Seq: 2, Component: ViewComponent{Kind: ViewFinder, Items: []ViewItem{{ID: "a", Label: "A"}}}},
		`{"view":3,"component":{"kind":"finder","items":[{"id":"a","label":"A"}]},"seq":2}`)
	roundTrip(t, PaneOpenParams{Component: ViewComponent{Kind: ViewConsole, Text: "o", Input: "i", Prompt: "$ ", History: []string{"h"}}},
		`{"component":{"kind":"console","text":"o","input":"i","prompt":"$ ","history":["h"]}}`)
	roundTrip(t, ViewSelectParams{View: 3, Item: "a", Input: "q"}, `{"view":3,"item":"a","input":"q"}`)
	roundTrip(t, ViewKeyParams{View: 3, ID: "k", Item: "a", Input: "q"}, `{"view":3,"id":"k","item":"a","input":"q"}`)
	roundTrip(t, EditorListParams{List: ListTabs}, `{"list":"tabs"}`)
	roundTrip(t, EditorListResult{Tabs: []ListTab{{Buffer: 1, Path: "/a", Name: "a", Client: 1, Own: true, Current: true}}},
		`{"tabs":[{"buffer":1,"path":"/a","name":"a","client":1,"own":true,"current":true}]}`)
	roundTrip(t, EditorListResult{Keys: []ListKey{{Keys: "gq", Modes: "n", Source: "extension", Extension: "finder"}}},
		`{"keys":[{"keys":"gq","modes":"n","source":"extension","extension":"finder"}]}`)
	roundTrip(t, EditorListResult{Jumps: []ListJump{{Buffer: 1, Name: "a", Position: Position{Line: 2}, Current: true}}},
		`{"jumps":[{"buffer":1,"name":"a","position":{"line":2,"byte":0},"current":true}]}`)
	roundTrip(t, EditorListParams{List: ListHelp}, `{"list":"help"}`)
	roundTrip(t, EditorListResult{Help: []ListHelpWord{{Word: ":write", Kind: HelpKindCommand, Description: "保存する"}, {Word: "w", Kind: HelpKindKey}}},
		`{"help":[{"word":":write","kind":"command","description":"保存する"},{"word":"w","kind":"key"}]}`)
}

// 書き足すテキストと入力の行の組の履歴は、空の中身と直前と同じ中身を積まず、上限を超えたら古いものから捨てる。
// 本体と拡張機能が同じ関数で積むので、開き直したときの履歴は、本体が閉じる前に持っていた履歴と同じになる。
func TestAppendConsoleHistory(t *testing.T) {
	var h []string
	for _, s := range []string{"a", "", "a", "b", "a"} {
		h = AppendConsoleHistory(h, s)
	}
	if strings.Join(h, ",") != "a,b,a" {
		t.Fatalf("history = %q", h)
	}
	for i := range ConsoleHistoryMax + 5 {
		h = AppendConsoleHistory(h, strings.Repeat("x", i%7+1)+string(rune('A'+i%26)))
	}
	if len(h) != ConsoleHistoryMax {
		t.Fatalf("len = %d, want %d", len(h), ConsoleHistoryMax)
	}
	if want := strings.Repeat("x", (ConsoleHistoryMax+4)%7+1) + string(rune('A'+(ConsoleHistoryMax+4)%26)); h[len(h)-1] != want {
		t.Errorf("last = %q, want %q", h[len(h)-1], want)
	}
}

// editor/list の params は、知っている一覧の種類だけを通す（FR-107）。
func TestEditorListParamsValidate(t *testing.T) {
	for _, k := range []EditorListKind{ListTabs, ListCommands, ListKeys, ListRegisters, ListJumps, ListHelp} {
		p := EditorListParams{List: k}
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
	p := EditorListParams{List: "marks"}
	if err := p.Validate(); err == nil {
		t.Error("marks: want error")
	}
	for _, tt := range []struct {
		p  EditorExecuteParams
		ok bool
	}{
		{EditorExecuteParams{Selection: 1, Keys: "dd"}, true},
		{EditorExecuteParams{Selection: 1, Command: "write"}, true},
		{EditorExecuteParams{Keys: "dd"}, false},
		{EditorExecuteParams{Selection: 1}, false},
		{EditorExecuteParams{Selection: 1, Keys: "dd", Command: "write"}, false},
		{EditorExecuteParams{Selection: 1, Command: "w\nq"}, false},
		{EditorExecuteParams{Selection: 1, Help: ":write"}, true},
		{EditorExecuteParams{Selection: 1, Help: "w", Keys: "w"}, false},
		{EditorExecuteParams{Selection: 1, Help: "a\nb"}, false},
	} {
		if err := tt.p.Validate(); (err == nil) != tt.ok {
			t.Errorf("%+v: %v", tt.p, err)
		}
	}
	roundTrip(t, EditorExecuteParams{Selection: 2, Command: "write"}, `{"selection":2,"command":"write"}`)
	roundTrip(t, EditorExecuteParams{Selection: 2, Help: ":write"}, `{"selection":2,"help":":write"}`)
	roundTrip(t, ViewSelectParams{View: 3, Item: "a", Selection: 2}, `{"view":3,"item":"a","selection":2}`)
	roundTrip(t, EditorOpenParams{Path: "/a", Position: &Position{Line: 1}, Peek: true}, `{"path":"/a","position":{"line":1,"byte":0},"peek":true}`)
}

// contributes.popupKeys は、ID とキーが空でなく、ID が popupKeys の中で重複しないことを検査する（FR-69）。
func TestManifestPopupKeys(t *testing.T) {
	base := Manifest{ID: "x", Command: "./x", Provides: []Operation{OpHover}}
	ok := base
	ok.Contributes.PopupKeys = []Contribution{{ID: "a", Keys: "o"}, {ID: "b", Keys: "<C-x>"}}
	if _, err := ok.Validate(); err != nil {
		t.Errorf("valid popupKeys: %v", err)
	}
	for _, tt := range []struct {
		keys []Contribution
		want string
	}{
		{[]Contribution{{ID: "", Keys: "o"}}, "id が空"},
		{[]Contribution{{ID: "a", Keys: ""}}, "keys が空"},
		{[]Contribution{{ID: "a", Keys: "o"}, {ID: "a", Keys: "p"}}, "重複"},
	} {
		m := base
		m.Contributes.PopupKeys = tt.keys
		if _, err := m.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("popupKeys %+v: err = %v, want %q", tt.keys, err, tt.want)
		}
	}
	// action は select を知っていて、知らない操作は誤りにせずに警告にする（後の版で操作を足しても、古い本体が拒まないため）。
	m := base
	m.Contributes.PopupKeys = []Contribution{{ID: "a", Keys: "l", Action: PopupKeySelect}, {ID: "b", Keys: "m", Action: "rename"}}
	warns, err := m.Validate()
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0].Error(), "未知の操作") {
		t.Errorf("popupKeys action: warnings %v, err %v", warns, err)
	}
	roundTrip(t, Contribution{ID: "a", Keys: "l", Action: PopupKeySelect}, `{"id":"a","keys":"l","action":"select"}`)
}

// pane/open の出す場所と、選んだら閉じる指定（FR-147）、部品のカーソルの項目、view/tabChange の JSON の形と検査。
func TestPaneOpenPlacement(t *testing.T) {
	c := ViewComponent{Kind: ViewTree, Cursor: "a", Items: []ViewItem{{ID: "a", Label: "A"}}}
	for _, tt := range []struct {
		p  PaneOpenParams
		ok bool
	}{
		{PaneOpenParams{Component: c}, true},
		{PaneOpenParams{Component: c, Placement: PaneSplit}, true},
		{PaneOpenParams{Component: c, Placement: PaneCenter, CloseOnSelect: true}, true},
		{PaneOpenParams{Component: c, Placement: "float"}, false},
	} {
		if err := tt.p.Validate(); (err == nil) != tt.ok {
			t.Errorf("%+v: %v", tt.p, err)
		}
	}
	roundTrip(t, PaneOpenParams{Component: c, Placement: PaneCenter, CloseOnSelect: true},
		`{"component":{"kind":"tree","items":[{"id":"a","label":"A"}],"cursor":"a"},"placement":"center","closeOnSelect":true}`)
	roundTrip(t, ViewTabChangeParams{View: 3, Buffer: 2, Path: "/a"}, `{"view":3,"buffer":2,"path":"/a"}`)
}

// 項目のプレビューに出すもの（FR-149）の JSON の形と検査と、部品がプレビューの欄を持つかの判定。
func TestViewItemPreview(t *testing.T) {
	line, neg, buf := 4, -1, BufferID(2)
	for _, tt := range []struct {
		pv   *ViewPreview
		want string // 誤りの文の一部。空なら誤りにしない
	}{
		{&ViewPreview{Path: "/a"}, ""},
		{&ViewPreview{Path: "a.go", Line: &line}, ""},
		{&ViewPreview{Buffer: &buf}, ""},
		{&ViewPreview{}, "path も buffer も無い"},
		{&ViewPreview{Path: "/a", Line: &neg}, "負"},
	} {
		c := ViewComponent{Kind: ViewTree, Items: []ViewItem{{ID: "d", Label: "D", Children: []ViewItem{{ID: "a", Label: "A", Preview: tt.pv}}}}}
		err := c.Validate()
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%+v: %v", tt.pv, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%+v: err = %v, want %q", tt.pv, err, tt.want)
		}
	}
	roundTrip(t, ViewComponent{Kind: ViewFinder, Preview: true, Items: []ViewItem{{ID: "a", Label: "A", Preview: &ViewPreview{Path: "/a", Line: &line}}}},
		`{"kind":"finder","items":[{"id":"a","label":"A","preview":{"path":"/a","line":4}}],"preview":true}`)
	roundTrip(t, ViewPreview{Buffer: &buf}, `{"buffer":2}`)

	for _, tt := range []struct {
		c    ViewComponent
		want bool
	}{
		{ViewComponent{Kind: ViewList, Items: []ViewItem{{ID: "a", Label: "A"}}}, false},
		{ViewComponent{Kind: ViewFinder, Preview: true}, true},
		{ViewComponent{Kind: ViewTree, Items: []ViewItem{{ID: "d", Label: "D", Children: []ViewItem{{ID: "a", Label: "A", Preview: &ViewPreview{Path: "/a"}}}}}}, true},
	} {
		if got := tt.c.HasPreview(); got != tt.want {
			t.Errorf("HasPreview(%+v) = %v, want %v", tt.c, got, tt.want)
		}
	}
}

// 項目の指すファイル（FR-152 のアイコンのため）の JSON の形と検査。
// ファイルは Dir を省けて、ディレクトリは "dir":true を持つ。パスの無いものは誤りにする。
func TestViewItemFile(t *testing.T) {
	roundTrip(t, ViewComponent{Kind: ViewTree, Items: []ViewItem{
		{ID: "d", Label: "d", File: &ViewFile{Path: "/w/d", Dir: true}, HasChildren: true},
		{ID: "a", Label: "a.go", File: &ViewFile{Path: "/w/a.go"}},
	}}, `{"kind":"tree","items":[{"id":"d","label":"d","hasChildren":true,"file":{"path":"/w/d","dir":true}},`+
		`{"id":"a","label":"a.go","file":{"path":"/w/a.go"}}]}`)
	for _, tt := range []struct {
		f    *ViewFile
		want string // 誤りの文の一部。空なら誤りにしない
	}{
		{&ViewFile{Path: "a.go"}, ""},
		{&ViewFile{Path: "d", Dir: true}, ""},
		{&ViewFile{}, "file.path が空"},
		{&ViewFile{Dir: true}, "file.path が空"},
	} {
		c := ViewComponent{Kind: ViewFinder, Items: []ViewItem{{ID: "a", Label: "A", File: tt.f}}}
		err := c.Validate()
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%+v: %v", tt.f, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%+v: err = %v, want %q", tt.f, err, tt.want)
		}
	}
}
