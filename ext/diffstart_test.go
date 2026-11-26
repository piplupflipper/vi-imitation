package ext

import "testing"

func TestDiffStartMessageJSON(t *testing.T) {
	roundTrip(t, BufferCreateParams{Title: "HEAD:a.go", Text: "x\ny", FileType: "go"},
		`{"title":"HEAD:a.go","text":"x\ny","fileType":"go"}`)
	roundTrip(t, BufferCreateParams{Title: "t", Text: "", Writable: true}, `{"title":"t","text":"","writable":true}`)
	roundTrip(t, BufferCreateResult{Buffer: 3, Version: 1}, `{"buffer":3,"version":1}`)
	roundTrip(t, DiffStartParams{A: 1, B: 3}, `{"a":1,"b":3}`)
	roundTrip(t, DiffStartParams{A: 1, B: 3, Wait: true}, `{"a":1,"b":3,"wait":true}`)
	roundTrip(t, DiffStartResult{Split: true}, `{"split":true}`)
	roundTrip(t, DiffStartResult{}, `{}`)
	title, ft, empty := "HEAD:b.go", "go", ""
	roundTrip(t, BufferUpdateParams{Buffer: 2, Text: "x"}, `{"buffer":2,"text":"x"}`)
	roundTrip(t, BufferUpdateParams{Buffer: 2, Text: "x", Title: &title, FileType: &ft},
		`{"buffer":2,"text":"x","title":"HEAD:b.go","fileType":"go"}`)
	// 空の fileType は省いたのと区別する（種類の無いバッファにする）。
	roundTrip(t, BufferUpdateParams{Buffer: 2, Text: "", FileType: &empty}, `{"buffer":2,"text":"","fileType":""}`)
}

func TestDiffStartValidate(t *testing.T) {
	if err := (&BufferCreateParams{}).Validate(); err == nil {
		t.Error("空の title を通した")
	}
	if err := (&BufferCreateParams{Title: "t"}).Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
	if err := (&DiffStartParams{A: 2, B: 2}).Validate(); err == nil {
		t.Error("同じバッファの組を通した")
	}
	if err := (&DiffStartParams{A: 1, B: 2}).Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
	empty, title := "", "t"
	if err := (&BufferUpdateParams{Title: &empty}).Validate(); err == nil {
		t.Error("buffer/update の空の title を通した")
	}
	if err := (&BufferUpdateParams{Title: &title}).Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
	if err := (&BufferUpdateParams{}).Validate(); err != nil {
		t.Errorf("title を省いた Validate = %v", err)
	}
}
