package ext

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// validManifest は、基本設計 5.1 の例と同じ内容の宣言ファイルを返す。
func validManifest() Manifest {
	return Manifest{
		ID:      "surround",
		Command: "./vi-imitation-ext-surround",
		Activation: []Activation{
			{On: ActivationOperator, ID: "surround.add"},
			{On: ActivationFileType, Pattern: "markdown"},
		},
		Contributes: Contributes{
			Operators:   []Contribution{{ID: "surround.add", Keys: "ys"}},
			TextObjects: []Contribution{{ID: "surround.inner", Keys: "is"}},
		},
	}
}

// req: FR-13
func TestManifestJSON(t *testing.T) {
	// 基本設計 5.1 の例からコメントを除いたもの。
	const doc = `{
  "id": "surround",
  "command": "./vi-imitation-ext-surround",
  "activation": [
    { "on": "operator", "id": "surround.add" },
    { "on": "filetype", "pattern": "markdown" }
  ],
  "contributes": {
    "operators": [{ "id": "surround.add", "keys": "ys" }],
    "textObjects": [{ "id": "surround.inner", "keys": "is" }]
  }
}`
	var got Manifest
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if want := validManifest(); !reflect.DeepEqual(got, want) {
		t.Errorf("Unmarshal = %#v, want %#v", got, want)
	}
	if ws, err := got.Validate(); err != nil || ws != nil {
		t.Errorf("Validate = %v, %v, want nil, nil", ws, err)
	}
	roundTrip(t, got, `{"id":"surround","command":"./vi-imitation-ext-surround",`+
		`"activation":[{"on":"operator","id":"surround.add"},{"on":"filetype","pattern":"markdown"}],`+
		`"contributes":{"operators":[{"id":"surround.add","keys":"ys"}],"textObjects":[{"id":"surround.inner","keys":"is"}]}}`)
}

// req: FR-13
func TestManifestValidate(t *testing.T) {
	tests := []struct {
		name   string
		modify func(m *Manifest)
		// want は、誤りのメッセージに含まれるべき文字列の一覧である。空なら誤りが無いことを期待する。
		want []string
		// warn は、警告に含まれるべき文字列の一覧である。空なら警告が無いことを期待する。
		warn []string
	}{
		{name: "正しい", modify: func(*Manifest) {}},
		{
			name: "全種類の操作の契機",
			modify: func(m *Manifest) {
				m.Contributes.Motions = []Contribution{{ID: "m"}}
				m.Contributes.Commands = []Contribution{{ID: "c"}}
				m.Activation = []Activation{
					{On: ActivationOperator, ID: "surround.add"},
					{On: ActivationMotion, ID: "m"},
					{On: ActivationTextObject, ID: "surround.inner"},
					{On: ActivationCommand, ID: "c"},
				}
			},
		},
		{name: "id が空", modify: func(m *Manifest) { m.ID = "" }, want: []string{"id が空"}},
		{name: "id が空白を含む", modify: func(m *Manifest) { m.ID = "sur round" }, want: []string{`id "sur round" が空白を含む`}},
		{name: "command が空", modify: func(m *Manifest) { m.Command = "" }, want: []string{"command が空"}},
		{name: "activation が空", modify: func(m *Manifest) { m.Activation = nil }, want: []string{"activation と provides と contributes.statusline がどれも空"}},
		{
			name:   "provides があれば activation は空でよい",
			modify: func(m *Manifest) { m.Activation = nil; m.Provides = []Operation{OpHover, OpDebugContinue} },
		},
		{
			name: "contributes.statusline があれば activation は空でよい",
			modify: func(m *Manifest) {
				m.Activation = nil
				m.Contributes.StatusLine = []StatusLinePart{{ID: "surround.count", Description: "囲んだ数"}}
			},
		},
		{
			name: "contributes.statusline の ID の誤り",
			modify: func(m *Manifest) {
				m.Contributes.StatusLine = []StatusLinePart{
					{ID: ""},
					{ID: "count"},
					{ID: "surround."},
					{ID: "other.count"},
					{ID: "surround.a b"},
					{ID: "surround.count"},
					{ID: "surround.count"},
					{ID: "surround.b.x"},
				}
			},
			want: []string{
				`contributes.statusline[7].id "surround.b.x" の拡張機能の ID "surround" と "." の後が "." を含む`,
				"contributes.statusline[0].id が空",
				`contributes.statusline[1].id "count" が拡張機能の ID "surround" と "." で始まらない`,
				`contributes.statusline[2].id "surround." が拡張機能の ID`,
				`contributes.statusline[3].id "other.count" が拡張機能の ID`,
				`contributes.statusline[4].id "surround.a b" が空白を含む`,
				`contributes.statusline[6].id "surround.count" が contributes.statusline[5] と重複する`,
			},
		},
		{
			name:   "provides の未知の操作は警告にする",
			modify: func(m *Manifest) { m.Provides = []Operation{OpHover, "peek"} },
			warn:   []string{`provides[1] "peek" は未知の操作なので無視する`},
		},
		{
			name: "noBufferSync とバッファの要る操作と saveHook は併せて書けない",
			modify: func(m *Manifest) {
				m.NoBufferSync = true
				m.SaveHook = true
			},
			want: []string{"noBufferSync とバッファの複製が要る saveHook", "noBufferSync とバッファの複製が要る contributes.operators", "contributes.textObjects"},
		},
		{
			name: "noBufferSync はコマンドと部品となら書ける",
			modify: func(m *Manifest) {
				m.NoBufferSync = true
				m.Contributes.Operators, m.Contributes.TextObjects = nil, nil
				m.Activation = []Activation{{On: ActivationCommand, ID: "c"}}
				m.Contributes.Commands = []Contribution{{ID: "c"}}
			},
		},
		{
			name:   "provides の重複は誤り",
			modify: func(m *Manifest) { m.Provides = []Operation{OpHover, OpFormat, OpHover} },
			want:   []string{`provides[2] "hover" が重複する`},
		},
		{
			name:   "未知の契機は警告にして誤りにしない",
			modify: func(m *Manifest) { m.Activation = append(m.Activation, Activation{On: "idle"}) },
			warn:   []string{`activation[2].on "idle" は未知の契機なので無視する`},
		},
		{
			name: "契機がすべて未知でも誤りにしない",
			modify: func(m *Manifest) {
				m.Activation = []Activation{{On: "idle"}, {On: "lsp", ID: "x"}}
			},
			warn: []string{`activation[0].on "idle"`, `activation[1].on "lsp"`},
		},
		{
			name: "未知の契機と誤りが両方ある",
			modify: func(m *Manifest) {
				m.Command = ""
				m.Activation = append(m.Activation, Activation{On: "idle"})
			},
			want: []string{"command が空"},
			warn: []string{`activation[2].on "idle"`},
		},
		{
			name:   "契機の id が空",
			modify: func(m *Manifest) { m.Activation[0].ID = "" },
			want:   []string{"activation[0].id が空"},
		},
		{
			name:   "契機の id が contributes に無い",
			modify: func(m *Manifest) { m.Activation[0].ID = "surround.del" },
			want:   []string{`activation[0].id "surround.del" が contributes の operator に無い`},
		},
		{
			name:   "契機の id が別の種類の操作を指す",
			modify: func(m *Manifest) { m.Activation[0].ID = "surround.inner" },
			want:   []string{`activation[0].id "surround.inner" が contributes の operator に無い`},
		},
		{
			name:   "filetype の pattern が空",
			modify: func(m *Manifest) { m.Activation[1].Pattern = "" },
			want:   []string{"activation[1].pattern が空"},
		},
		{
			name: "settingsFileType の pattern が / で始まらない",
			modify: func(m *Manifest) {
				m.Activation = append(m.Activation, Activation{On: ActivationSettingsFileType, Pattern: "parsers/*/fileTypes"})
			},
			want: []string{`activation[2].pattern "parsers/*/fileTypes" が / で始まる JSON Pointer でない`},
		},
		{
			name:   "操作の id が空",
			modify: func(m *Manifest) { m.Contributes.Motions = []Contribution{{Keys: "x"}} },
			want:   []string{"contributes.motions[0].id が空"},
		},
		{
			name: "操作の id が種類をまたいで重複",
			modify: func(m *Manifest) {
				m.Contributes.Commands = []Contribution{{ID: "surround.add"}}
			},
			want: []string{`contributes.commands[0].id "surround.add" が contributes.operators[0] と重複`},
		},
		{
			name: "操作の id が同じ種類で重複",
			modify: func(m *Manifest) {
				m.Contributes.TextObjects = append(m.Contributes.TextObjects, Contribution{ID: "surround.inner"})
			},
			want: []string{`contributes.textObjects[1].id "surround.inner" が contributes.textObjects[0] と重複`},
		},
		{
			name: "複数の誤りをすべて返す",
			modify: func(m *Manifest) {
				m.ID = ""
				m.Command = ""
				m.Activation[1].Pattern = ""
			},
			want: []string{"id が空", "command が空", "activation[1].pattern が空"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validManifest()
			tt.modify(&m)
			ws, err := m.Validate()
			if len(ws) != len(tt.warn) {
				t.Errorf("Validate の警告 = %q, want %d 件 %q", ws, len(tt.warn), tt.warn)
			} else {
				for i, w := range tt.warn {
					if !strings.Contains(ws[i].Error(), w) {
						t.Errorf("Validate の警告[%d] = %q, want containing %q", i, ws[i], w)
					}
				}
			}
			if len(tt.want) == 0 {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate = nil, want errors containing %q", tt.want)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("Validate = %q, want containing %q", err, w)
				}
			}
			if got, want := strings.Count(err.Error(), "\n")+1, len(tt.want); got != want {
				t.Errorf("Validate の誤りの数 = %d, want %d: %q", got, want, err)
			}
		})
	}
}

// req: FR-13
func TestKnownActivation(t *testing.T) {
	m := validManifest()
	m.Activation = []Activation{
		{On: "idle"},
		{On: ActivationOperator, ID: "surround.add"},
		{On: "lsp"},
		{On: ActivationFileType, Pattern: "markdown"},
		{On: ActivationSocket},
		{On: ActivationSettingsFileType, Pattern: "/parsers/*/fileTypes"},
		{On: ActivationStartup},
	}
	want := []Activation{
		{On: ActivationOperator, ID: "surround.add"},
		{On: ActivationFileType, Pattern: "markdown"},
		{On: ActivationSocket},
		{On: ActivationSettingsFileType, Pattern: "/parsers/*/fileTypes"},
		{On: ActivationStartup},
	}
	if _, err := m.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
	if got := m.KnownActivation(); !reflect.DeepEqual(got, want) {
		t.Errorf("KnownActivation = %#v, want %#v", got, want)
	}

	for _, k := range []ActivationKind{ActivationOperator, ActivationMotion, ActivationTextObject, ActivationCommand, ActivationFileType, ActivationSocket, ActivationSettingsFileType, ActivationStartup} {
		if !k.Known() {
			t.Errorf("%q.Known() = false, want true", k)
		}
	}
	for _, k := range []ActivationKind{"", "idle", "Operator"} {
		if k.Known() {
			t.Errorf("%q.Known() = true, want false", k)
		}
	}
}

// ファイルの種類の契機を持たない拡張機能は、どのファイルの種類でも提供者の候補になる。
// 設定のファイルの種類の契機を持つ拡張機能は、展開したファイルの種類の契機と一致するときだけ候補になり、
// 設定に何も書いていなければ（展開したものが無ければ）、どのファイルの種類でも候補にならない（#339）。
//
// req: FR-176
func TestHandlesFileType(t *testing.T) {
	settings := ActivationSettingsFileType
	for _, c := range []struct {
		name string
		acts []Activation
		ft   string
		want bool
	}{
		{"契機なし", nil, "go", true},
		{"操作の契機だけ", []Activation{{On: ActivationCommand, ID: "x"}}, "go", true},
		{"一致するファイルの種類", []Activation{{On: ActivationFileType, Pattern: "go"}}, "go", true},
		{"一致しないファイルの種類", []Activation{{On: ActivationFileType, Pattern: "go"}}, "python", false},
		{"展開していない設定の契機", []Activation{{On: settings, Pattern: "/formatters/*/fileTypes"}}, "go", false},
		{"展開した設定の契機", []Activation{{On: settings, Pattern: "/p"}, {On: ActivationFileType, Pattern: "python"}}, "python", true},
		{"展開した設定の契機に無い種類", []Activation{{On: settings, Pattern: "/p"}, {On: ActivationFileType, Pattern: "python"}}, "go", false},
	} {
		m := Manifest{Activation: c.acts}
		if got := m.HandlesFileType(c.ft); got != c.want {
			t.Errorf("%s: HandlesFileType(%q) = %v, want %v", c.name, c.ft, got, c.want)
		}
	}
}

// req: FR-13
func TestValidateManifests(t *testing.T) {
	a := validManifest()
	b := validManifest()
	b.ID = "other"
	if ws, err := ValidateManifests([]Manifest{a, b}); err != nil || ws != nil {
		t.Errorf("ValidateManifests = %v, %v, want nil, nil", ws, err)
	}

	dup := validManifest()
	_, err := ValidateManifests([]Manifest{a, b, dup})
	if err == nil || !strings.Contains(err.Error(), `宣言ファイル[2] の id "surround" が宣言ファイル[0] と重複する`) {
		t.Errorf("ValidateManifests = %v, want duplicate id error", err)
	}

	bad := validManifest()
	bad.Command = ""
	_, err = ValidateManifests([]Manifest{bad})
	if err == nil || !strings.Contains(err.Error(), `宣言ファイル[0]（id "surround"）: command が空`) {
		t.Errorf("ValidateManifests = %v, want command error", err)
	}

	future := validManifest()
	future.ID = "future"
	future.Activation = append(future.Activation, Activation{On: "idle"})
	ws, err := ValidateManifests([]Manifest{a, future})
	if err != nil {
		t.Errorf("ValidateManifests = %v, want nil", err)
	}
	if len(ws) != 1 || !strings.Contains(ws[0].Error(), `宣言ファイル[1]（id "future"）: activation[2].on "idle"`) {
		t.Errorf("ValidateManifests の警告 = %q, want 1 件", ws)
	}

	if ws, err := ValidateManifests(nil); err != nil || ws != nil {
		t.Errorf("ValidateManifests(nil) = %v, %v, want nil, nil", ws, err)
	}
}

// TestManifestStdExtFields は、標準の拡張機能のために足した項目（基本設計 6.7）を読めることを確かめる。
func TestManifestStdExtFields(t *testing.T) {
	const doc = `{"id":"lsp","command":"./lsp","args":["--stdio","x"],"saveHook":true,` +
		`"activation":[{"on":"filetype","pattern":"go"}]}`
	var got Manifest
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.SaveHook || !reflect.DeepEqual(got.Args, []string{"--stdio", "x"}) {
		t.Errorf("SaveHook, Args = %v, %q", got.SaveHook, got.Args)
	}
	if _, err := got.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
	// 省いた項目は、既定値（引数なし、saveHook は false）として読み、書き出しでも省く。
	roundTrip(t, Manifest{ID: "a", Command: "./a"}, `{"id":"a","command":"./a","activation":null,"contributes":{}}`)
}
