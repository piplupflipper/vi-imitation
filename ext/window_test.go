package ext

import (
	"encoding/json"
	"strings"
	"testing"
)

// window/change と band/set の params の JSON の形（基本設計 5.3）。
func TestWindowParamsJSON(t *testing.T) {
	b, err := json.Marshal(WindowChangeParams{
		Window: "client/1", Buffer: 2, Version: 3, Top: 4, Bottom: 30, Rows: 26, Columns: 80, Left: 6,
		Cursor: Position{Line: 10, Byte: 2}, Mode: ModeNormal, Seq: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"window":"client/1","buffer":2,"version":3,"top":4,"bottom":30,"rows":26,"columns":80,"left":6,"cursor":{"line":10,"byte":2},"mode":"normal","seq":9}`
	if got := string(b); got != want {
		t.Errorf("window/change = %s, want %s", got, want)
	}
	b, err = json.Marshal(WindowChangeParams{Window: "client/1", Closed: true})
	if err != nil {
		t.Fatal(err)
	}
	want = `{"window":"client/1","closed":true,"top":0,"bottom":0,"rows":0,"columns":0,"left":0,"cursor":{"line":0,"byte":0},"seq":0}`
	if got := string(b); got != want {
		t.Errorf("closed = %s, want %s", got, want)
	}
	p := BandSetParams{Window: "client/1", Buffer: 2, Version: 3, Seq: 9, Lines: []BandLine{
		{Text: "func f() {", Line: SourceLine(0), Highlights: []BandHighlight{{Start: 0, End: 4, Style: "keyword"}}},
		{Text: "---"},
	}}
	b, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"window":"client/1","buffer":2,"version":3,"seq":9,"lines":[{"text":"func f() {","line":0,"highlights":[{"start":0,"end":4,"style":"keyword"}]},{"text":"---"}]}`
	if got := string(b); got != want {
		t.Errorf("band/set = %s, want %s", got, want)
	}
	var back BandSetParams
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Lines[0].Line == nil || *back.Lines[0].Line != 0 || back.Lines[1].Line != nil {
		t.Errorf("Unmarshal の Line = %v, %v", back.Lines[0].Line, back.Lines[1].Line)
	}
}

// band/set の params の検査。
func TestBandSetValidate(t *testing.T) {
	many := make([]BandLine, MaxBandLines+1)
	for _, tc := range []struct {
		name string
		p    BandSetParams
		ok   bool
	}{
		{"空の帯は消す指示", BandSetParams{Window: "client/1"}, true},
		{"範囲が行の全体", BandSetParams{Window: "w", Lines: []BandLine{{Text: "ab", Highlights: []BandHighlight{{Start: 0, End: 2, Style: "x"}}}}}, true},
		{"窓が空", BandSetParams{Lines: []BandLine{{Text: "a"}}}, false},
		{"行が多すぎる", BandSetParams{Window: "w", Lines: many}, false},
		{"改行", BandSetParams{Window: "w", Lines: []BandLine{{Text: "a\nb"}}}, false},
		{"不正な UTF-8", BandSetParams{Window: "w", Lines: []BandLine{{Text: "a\xff"}}}, false},
		{"負の行", BandSetParams{Window: "w", Lines: []BandLine{{Text: "a", Line: SourceLine(-1)}}}, false},
		{"範囲が行の外", BandSetParams{Window: "w", Lines: []BandLine{{Text: "ab", Highlights: []BandHighlight{{Start: 1, End: 3}}}}}, false},
		{"逆順の範囲", BandSetParams{Window: "w", Lines: []BandLine{{Text: "ab", Highlights: []BandHighlight{{Start: 2, End: 1}}}}}, false},
	} {
		err := tc.p.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("%s: Validate = %v, ok = %v", tc.name, err, tc.ok)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "band/set: ") {
			t.Errorf("%s: 誤りの文言 = %q", tc.name, err)
		}
	}
}
