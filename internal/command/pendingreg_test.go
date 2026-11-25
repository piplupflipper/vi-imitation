package command

import (
	"slices"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// 回数の無い " と名前の後も KeyHook を呼び、KeyHook は PendingRegister で名前を読み、TakeRegister で受け取る。
// 受け取らずに返したキー（held を含む）は、名前を付けた通常のキーとして解釈する。回数を付けた "a と、" の後のキーでは呼ばない。
// 使えない名前も検査せずに渡す（FR-50 の検査は呼び出し側）。Registers.Yank は y と同じく書き、使えない名前を E354 で断る。
//
// req: FR-315
func TestPendingRegisterHook(t *testing.T) {
	s := setup(t, "|abc")
	hookKey := editcmd.Rune('Z')
	var calls []string
	var took []rune
	s.KeyHook = func(k editcmd.Key, _ bool) (bool, []editcmd.Key, error) {
		r, ok := s.PendingRegister()
		if ok {
			calls = append(calls, string(r)+k.String())
		} else {
			calls = append(calls, k.String())
		}
		if k == hookKey {
			took = append(took, s.TakeRegister())
			return true, nil, nil
		}
		return false, nil, nil
	}
	feedKeys(s, `"aZ`)
	if len(took) != 1 || took[0] != 'a' {
		t.Fatalf(`"aZ: took %q, calls %q`, took, calls)
	}
	if s.parser.Pending() {
		t.Errorf(`"aZ left the parser pending`)
	}
	// 当てないキーは、名前を付けたまま通常のキーとして解釈する（"byl は b に書く）。
	calls = nil
	feedKeys(s, `"byl`)
	if r, _ := s.registers.Get('b'); r.Text != "a" {
		t.Errorf(`"byl: b = %q`, r.Text)
	}
	// " は途中でないので呼ぶ。名前の b は " の後なので呼ばない。y の後の l はオペレーターの途中なので呼ばない。
	if want := []string{`"`, "by"}; !slices.Equal(calls, want) {
		t.Errorf(`"byl: calls %q, want %q`, calls, want)
	}
	// 回数を付けた "a の後では呼ばない。
	calls = nil
	feedKeys(s, `2"cZ`)
	for _, c := range calls {
		if c == "cZ" {
			t.Errorf(`2"cZ passed the name to the hook: %q`, calls)
		}
	}
	feedKeys(s, "<Esc>")
	// 使えない名前も渡す。
	took = nil
	feedKeys(s, `".Z`)
	if len(took) != 1 || took[0] != '.' {
		t.Errorf(`".Z: took %q`, took)
	}

	// Registers.Yank は、名前を指定した y と同じく書き、使えない名前は E354 で断る。
	var regs Registers
	if _, err := regs.Yank('.', Register{Text: "x"}); err == nil {
		t.Error("Yank to . succeeded")
	}
	if _, err := regs.Yank('a', Register{Text: "値"}); err != nil {
		t.Errorf("Yank to a: %v", err)
	}
	if r, _ := regs.Get(0); r.Text != "値" {
		t.Errorf("unnamed after Yank to a = %q", r.Text)
	}
	regs.SetConfig(RegisterConfig{MaxEntryBytes: 4})
	if notice, err := regs.Yank(0, Register{Text: "12345"}); err != nil || notice == "" {
		t.Errorf("Yank over the limit: notice %q, err %v", notice, err)
	}
}
