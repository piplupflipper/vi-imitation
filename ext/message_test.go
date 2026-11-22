package ext

import (
	"encoding/json"
	"reflect"
	"testing"
)

// roundTrip は、v を JSON にして want と比べ、読み戻した値が v と等しいことを確かめる。
func roundTrip[T any](t *testing.T, v T, want string) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(data); got != want {
		t.Errorf("Marshal = %s, want %s", got, want)
	}
	var back T
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, v) {
		t.Errorf("Unmarshal = %#v, want %#v", back, v)
	}
}

var (
	testRange = Range{Start: Position{Line: 1, Byte: 2}, End: Position{Line: 3, Byte: 4}}
	testRJSON = `{"start":{"line":1,"byte":2},"end":{"line":3,"byte":4}}`
)

func TestTransactionJSON(t *testing.T) {
	roundTrip(t, Transaction{
		BaseVersion: 7,
		Edits: []Edit{
			{Range: testRange, Text: "あ"},
			{Range: Range{}, Text: ""},
		},
	}, `{"baseVersion":7,"edits":[{"range":`+testRJSON+`,"text":"あ"},{"range":{"start":{"line":0,"byte":0},"end":{"line":0,"byte":0}},"text":""}]}`)
}

func TestMessageJSON(t *testing.T) {
	tx := Transaction{BaseVersion: 3, Edits: []Edit{{Range: testRange, Text: "x"}}}
	txJSON := `{"baseVersion":3,"edits":[{"range":` + testRJSON + `,"text":"x"}]}`

	t.Run("initialize", func(t *testing.T) {
		roundTrip(t, InitializeParams{APIVersion: APIVersion, Settings: json.RawMessage(`{"a":1}`)},
			`{"apiVersion":"0.18.0","settings":{"a":1}}`)
		roundTrip(t, InitializeParams{APIVersion: APIVersion}, `{"apiVersion":"0.18.0"}`)
		roundTrip(t, InitializeResult{APIVersion: APIVersion}, `{"apiVersion":"0.18.0"}`)
	})
	t.Run("provider/invoke", func(t *testing.T) {
		rg := testRange
		roundTrip(t, ProviderInvokeParams{
			Operation: OpFormat, Buffer: 2, Version: 5, Cursor: Position{Line: 1, Byte: 2}, Range: &rg,
			Args: json.RawMessage(`{"peek":true}`), Sync: true,
		}, `{"operation":"format","buffer":2,"version":5,"cursor":{"line":1,"byte":2},"range":`+testRJSON+
			`,"count":0,"args":{"peek":true},"sync":true}`)
		roundTrip(t, ProviderInvokeParams{Operation: OpDebugContinue, Buffer: 1, Withheld: true},
			`{"operation":"debug.continue","buffer":1,"version":0,"cursor":{"line":0,"byte":0},"count":0,"withheld":true}`)
	})
	t.Run("buffer/open", func(t *testing.T) {
		roundTrip(t, BufferOpenParams{Buffer: 1, Path: "/a.md", FileType: "markdown", Version: 0, Text: "a\nb"},
			`{"buffer":1,"path":"/a.md","fileType":"markdown","version":0,"text":"a\nb"}`)
		roundTrip(t, BufferOpenParams{Buffer: 2}, `{"buffer":2,"version":0,"text":""}`)
		no, yes := false, true
		roundTrip(t, BufferOpenParams{Buffer: 3, Text: "a", FinalNewline: &no}, `{"buffer":3,"version":0,"text":"a","finalNewline":false}`)
		roundTrip(t, BufferOpenParams{Buffer: 3, Text: "a", FinalNewline: &yes}, `{"buffer":3,"version":0,"text":"a","finalNewline":true}`)
	})
	t.Run("buffer/open の FinalNewline", func(t *testing.T) {
		no, yes := false, true
		for _, tt := range []struct {
			v    *bool
			want bool
		}{{nil, true}, {&yes, true}, {&no, false}} {
			p := BufferOpenParams{FinalNewline: tt.v}
			if got := p.HasFinalNewline(); got != tt.want {
				t.Errorf("HasFinalNewline() with %v = %v, want %v", tt.v, got, tt.want)
			}
		}
	})
	t.Run("buffer/change", func(t *testing.T) {
		roundTrip(t, BufferChangeParams{Buffer: 1, FromVersion: 4, ToVersion: 5, Edits: []Edit{{Range: testRange, Text: "x"}}},
			`{"buffer":1,"fromVersion":4,"toVersion":5,"edits":[{"range":`+testRJSON+`,"text":"x"}]}`)
	})
	t.Run("buffer/close", func(t *testing.T) {
		roundTrip(t, BufferCloseParams{Buffer: 9}, `{"buffer":9}`)
	})
	t.Run("motion/resolve", func(t *testing.T) {
		roundTrip(t, MotionResolveParams{Buffer: 1, Version: 2, ID: "m", Cursor: Position{Line: 5, Byte: 6}, Count: 3},
			`{"buffer":1,"version":2,"id":"m","cursor":{"line":5,"byte":6},"count":3}`)
		// 回数の省略は 0、引数は JSON のまま運ぶ。
		roundTrip(t, MotionResolveParams{Buffer: 1, Version: 2, ID: "m", Args: json.RawMessage(`{"char":"x"}`)},
			`{"buffer":1,"version":2,"id":"m","cursor":{"line":0,"byte":0},"count":0,"args":{"char":"x"}}`)
		roundTrip(t, MotionResolveResult{OK: true, Version: 2, Position: Position{Line: 7, Byte: 8}},
			`{"ok":true,"version":2,"position":{"line":7,"byte":8}}`)
		roundTrip(t, MotionResolveResult{OK: true, Version: 2, Linewise: true, Inclusive: true},
			`{"ok":true,"version":2,"position":{"line":0,"byte":0},"linewise":true,"inclusive":true}`)
	})
	t.Run("textObject/resolve", func(t *testing.T) {
		roundTrip(t, TextObjectResolveParams{Buffer: 1, Version: 2, ID: "o", Cursor: Position{Line: 5, Byte: 6}, Count: 1},
			`{"buffer":1,"version":2,"id":"o","cursor":{"line":5,"byte":6},"count":1}`)
		roundTrip(t, TextObjectResolveResult{OK: true, Version: 2, Range: testRange, Linewise: true},
			`{"ok":true,"version":2,"range":`+testRJSON+`,"linewise":true}`)
		sel := testRange
		roundTrip(t, TextObjectResolveParams{Buffer: 1, Version: 2, ID: "o", Count: 1, Selection: &sel},
			`{"buffer":1,"version":2,"id":"o","cursor":{"line":0,"byte":0},"count":1,"selection":`+testRJSON+`}`)
		roundTrip(t, TextObjectResolveResult{OK: true, Version: 2, Range: testRange, ReplaceSelection: true},
			`{"ok":true,"version":2,"range":`+testRJSON+`,"replaceSelection":true}`)
	})
	t.Run("operator/apply", func(t *testing.T) {
		roundTrip(t, OperatorApplyParams{Buffer: 1, Version: 3, ID: "op", Range: testRange, Count: 1, Args: json.RawMessage(`"\""`)},
			`{"buffer":1,"version":3,"id":"op","range":`+testRJSON+`,"count":1,"args":"\""}`)
		roundTrip(t, OperatorApplyResult{OK: true, Transaction: tx}, `{"ok":true,"transaction":`+txJSON+`}`)
	})
	t.Run("ping", func(t *testing.T) {
		roundTrip(t, PingParams{}, `{}`)
		roundTrip(t, PingResult{}, `{}`)
	})
	t.Run("$/cancel", func(t *testing.T) {
		roundTrip(t, CancelParams{ID: json.RawMessage(`12`)}, `{"id":12}`)
		roundTrip(t, CancelParams{ID: json.RawMessage(`"req-1"`)}, `{"id":"req-1"}`)
	})
	t.Run("buffer/edit", func(t *testing.T) {
		roundTrip(t, BufferEditParams{Buffer: 1, Transaction: tx}, `{"buffer":1,"transaction":`+txJSON+`}`)
		yes, no := true, false
		roundTrip(t, BufferEditParams{Buffer: 1, Transaction: tx, NoLines: &yes, EndOfLine: &no},
			`{"buffer":1,"transaction":`+txJSON+`,"noLines":true,"endOfLine":false}`)
		roundTrip(t, BufferEditResult{Applied: false, Version: 4}, `{"applied":false,"version":4}`)
	})
	t.Run("decoration/set", func(t *testing.T) {
		roundTrip(t, DecorationSetParams{Buffer: 1, Version: 3, Decorations: []Decoration{{Range: testRange, Style: "keyword"}}},
			`{"buffer":1,"version":3,"styles":["keyword"],"data":[1,2,2,4,0]}`)
	})
	t.Run("message/show", func(t *testing.T) {
		roundTrip(t, MessageShowParams{Level: MessageWarning, Text: "注意"}, `{"level":"warning","text":"注意"}`)
	})
	t.Run("buffer/resync", func(t *testing.T) {
		roundTrip(t, BufferResyncParams{Buffer: 1, Version: 4}, `{"buffer":1,"version":4}`)
	})
}

// TestResultFailureByDefault は、ok の項目が無い result が成功と読まれないことを確かめる。
// 古い形の応答や、ok を書き忘れた応答を、本体が操作の失敗として扱えるようにするためである。
func TestResultFailureByDefault(t *testing.T) {
	const data = `{"version":2}`
	var m MotionResolveResult
	var o TextObjectResolveResult
	var a OperatorApplyResult
	for _, v := range []any{&m, &o, &a} {
		if err := json.Unmarshal([]byte(data), v); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
	}
	if m.OK || o.OK || a.OK {
		t.Errorf("OK = %v, %v, %v, want all false", m.OK, o.OK, a.OK)
	}
	// 文字単位で exclusive が既定である。
	if m.Linewise || m.Inclusive || o.Linewise {
		t.Errorf("Linewise/Inclusive の既定値が false でない: %+v %+v", m, o)
	}
}

// TestMethodNames は、メソッドの名前が基本設計 5.3 の表（と本文の送り直しの要求）と一致することを確かめる。
func TestMethodNames(t *testing.T) {
	got := []string{
		MethodInitialize, MethodBufferOpen, MethodBufferChange, MethodBufferClose,
		MethodMotionResolve, MethodTextObjectResolve, MethodOperatorApply,
		MethodPing, MethodCancel, MethodShutdown, MethodBufferEdit, MethodDecorationSet, MethodMessageShow,
		MethodBufferResync,
		MethodBufferWillSave, MethodBufferDidSave, MethodEditorState, MethodEditorOpen, MethodBufferSave,
		MethodDiagnosticSet, MethodDiagnosticList, MethodSignSet, MethodPopupShow, MethodOutputAppend, MethodChoiceAsk,
	}
	want := []string{
		"initialize", "buffer/open", "buffer/change", "buffer/close",
		"motion/resolve", "textObject/resolve", "operator/apply",
		"ping", "$/cancel", "shutdown", "buffer/edit", "decoration/set", "message/show",
		"buffer/resync",
		"buffer/willSave", "buffer/didSave", "editor/state", "editor/open", "buffer/save",
		"diagnostic/set", "diagnostic/list", "sign/set", "popup/show", "output/append", "choice/ask",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("method names = %q, want %q", got, want)
	}
}

// TestStdExtMessageJSON は、標準の拡張機能のために足したメッセージの JSON の形を確かめる（基本設計 6.7）。
func TestStdExtMessageJSON(t *testing.T) {
	tx := Transaction{BaseVersion: 3, Edits: []Edit{{Range: testRange, Text: "x"}}}
	txJSON := `{"baseVersion":3,"edits":[{"range":` + testRJSON + `,"text":"x"}]}`
	buf := BufferID(4)
	ver := Version(9)
	pos := Position{Line: 1, Byte: 2}

	t.Run("command/execute", func(t *testing.T) {
		roundTrip(t, CommandExecuteParams{Buffer: 1, Version: 2, ID: "c", Sync: true},
			`{"buffer":1,"version":2,"id":"c","cursor":{"line":0,"byte":0},"count":0,"sync":true}`)
		roundTrip(t, CommandExecuteResult{OK: true, Open: &EditorOpenParams{Buffer: &buf, Position: &pos, Focus: true}},
			`{"ok":true,"open":{"buffer":4,"position":{"line":1,"byte":2},"focus":true}}`)
	})
	t.Run("buffer/willSave", func(t *testing.T) {
		roundTrip(t, BufferWillSaveParams{Buffer: 1, Version: 2}, `{"buffer":1,"version":2}`)
		roundTrip(t, BufferWillSaveResult{}, `{}`)
		roundTrip(t, BufferWillSaveResult{Transaction: &tx}, `{"transaction":`+txJSON+`}`)
	})
	t.Run("buffer/didSave", func(t *testing.T) {
		roundTrip(t, BufferDidSaveParams{Buffer: 1, Version: 2, Path: "/a.go"}, `{"buffer":1,"version":2,"path":"/a.go"}`)
	})
	t.Run("editor/state", func(t *testing.T) {
		roundTrip(t, EditorStateParams{}, `{}`)
		roundTrip(t, EditorStateResult{
			Buffer: 1, Cursor: pos, Mode: ModeVisualBlock,
			Selections: []Range{testRange, testRange},
			Buffers:    []BufferInfo{{Buffer: 1, Path: "/a.go", FileType: "go", Version: 3, Modified: true}, {Buffer: 2}},
		}, `{"buffer":1,"cursor":{"line":1,"byte":2},"mode":"visualBlock","selections":[`+testRJSON+`,`+testRJSON+`],`+
			`"buffers":[{"buffer":1,"path":"/a.go","fileType":"go","version":3,"modified":true},{"buffer":2,"version":0,"modified":false}]}`)
		// ビジュアルモードでなければ選択範囲を省く。
		roundTrip(t, EditorStateResult{Mode: ModeNormal, Buffers: []BufferInfo{}},
			`{"buffer":0,"cursor":{"line":0,"byte":0},"mode":"normal","buffers":[]}`)
	})
	t.Run("editor/open", func(t *testing.T) {
		roundTrip(t, EditorOpenParams{Path: "/a.go", Position: &pos, Version: &ver, Focus: true},
			`{"path":"/a.go","position":{"line":1,"byte":2},"version":9,"focus":true}`)
		// バッファの番号 0 とバージョン番号 0 も、省略と区別して運ぶ。
		zb, zv := BufferID(0), Version(0)
		roundTrip(t, EditorOpenParams{Buffer: &zb, Position: &Position{}, Version: &zv},
			`{"buffer":0,"position":{"line":0,"byte":0},"version":0}`)
		roundTrip(t, EditorOpenResult{Buffer: 4, Version: 1, Moved: true}, `{"buffer":4,"version":1,"moved":true}`)
	})
	t.Run("buffer/save", func(t *testing.T) {
		roundTrip(t, BufferSaveParams{Buffer: 1}, `{"buffer":1}`)
		roundTrip(t, BufferSaveParams{Buffer: 1, Version: &ver, SkipHooks: true}, `{"buffer":1,"version":9,"skipHooks":true}`)
		roundTrip(t, BufferSaveResult{Saved: true, Version: 9}, `{"saved":true,"version":9}`)
	})
	t.Run("diagnostic/set", func(t *testing.T) {
		roundTrip(t, DiagnosticSetParams{Buffer: 1, Version: 2, Diagnostics: []Diagnostic{
			{Range: testRange, Severity: SeverityError, Message: "m", Source: "gopls", Code: "E1"},
			{Range: testRange, Severity: SeverityHint, Message: "h"},
		}}, `{"buffer":1,"version":2,"diagnostics":[`+
			`{"range":`+testRJSON+`,"severity":"error","message":"m","source":"gopls","code":"E1"},`+
			`{"range":`+testRJSON+`,"severity":"hint","message":"h"}]}`)
		roundTrip(t, DiagnosticSetParams{Buffer: 1, Version: 2, Diagnostics: []Diagnostic{}},
			`{"buffer":1,"version":2,"diagnostics":[]}`)
	})
	t.Run("diagnostic/list", func(t *testing.T) {
		roundTrip(t, DiagnosticListParams{}, `{}`)
		roundTrip(t, DiagnosticListParams{Buffer: &buf}, `{"buffer":4}`)
		roundTrip(t, DiagnosticListResult{Entries: []DiagnosticEntry{{
			Buffer: 4, Extension: "lsp", Version: 9,
			Diagnostics: []Diagnostic{{Range: testRange, Severity: SeverityWarning, Message: "w"}},
		}}}, `{"entries":[{"buffer":4,"extension":"lsp","version":9,"diagnostics":[{"range":`+testRJSON+`,"severity":"warning","message":"w"}]}]}`)
	})
	t.Run("sign/set", func(t *testing.T) {
		roundTrip(t, SignSetParams{Buffer: 1, Version: 2, Signs: []Sign{
			{Line: 3, Kind: SignBreakpoint}, {Line: 4, Kind: SignBreakpointUnverified}, {Line: 5, Kind: SignExecutionPoint},
		}}, `{"buffer":1,"version":2,"signs":[{"line":3,"kind":"breakpoint"},{"line":4,"kind":"breakpointUnverified"},{"line":5,"kind":"executionPoint"}]}`)
	})
	t.Run("popup/show", func(t *testing.T) {
		roundTrip(t, PopupShowParams{Buffer: 1, Version: 2, Position: pos, Text: "**doc**", Format: PopupMarkdown},
			`{"buffer":1,"version":2,"position":{"line":1,"byte":2},"text":"**doc**","format":"markdown"}`)
	})
	t.Run("output/append", func(t *testing.T) {
		roundTrip(t, OutputAppendParams{Channel: "dap", Text: "a\nb"}, `{"channel":"dap","text":"a\nb"}`)
	})
	t.Run("choice/ask", func(t *testing.T) {
		roundTrip(t, ChoiceAskParams{Title: "許可するか", Choices: []Choice{{ID: "allow", Label: "許可"}, {ID: "deny", Label: "拒否"}}},
			`{"title":"許可するか","choices":[{"id":"allow","label":"許可"},{"id":"deny","label":"拒否"}]}`)
		roundTrip(t, ChoiceAskResult{Selected: "allow"}, `{"selected":"allow"}`)
		roundTrip(t, ChoiceAskResult{Cancelled: true}, `{"cancelled":true}`)
	})
}

func TestEditorOpenParamsValidate(t *testing.T) {
	buf := BufferID(0)
	tests := []struct {
		name string
		p    EditorOpenParams
		ok   bool
	}{
		{"path", EditorOpenParams{Path: "/a.go"}, true},
		{"buffer 0", EditorOpenParams{Buffer: &buf}, true},
		{"neither", EditorOpenParams{Focus: true}, false},
		{"both", EditorOpenParams{Path: "/a.go", Buffer: &buf}, false},
		{"relative", EditorOpenParams{Path: "a.go"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.p.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}
