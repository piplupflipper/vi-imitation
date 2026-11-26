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
}
