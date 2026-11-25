package command

import (
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// KeyHook の中の SubstituteKey は、受けたキーの代わりにキーの列を解釈し、マクロの記録の受けたキーをその列に置き換える。
// インサートモードの入力はドットリピートにも入る。nil を渡すと、受けたキーを記録から外すだけである（#427）。
func TestSubstituteKey(t *testing.T) {
	s := setup(t, "|")
	sub := editcmd.MustParseKeys("<C-y>")[0]
	drop := editcmd.MustParseKeys("<C-n>")[0]
	s.KeyHook = func(k editcmd.Key, insert bool) (bool, []editcmd.Key, error) {
		switch {
		case !insert || s.Replaying():
		case k == sub:
			s.SubstituteKey(editcmd.MustParseKeys("<BS>xyz"))
			return true, nil, nil
		case k == drop:
			s.SubstituteKey(nil)
			return true, nil, nil
		}
		return false, nil, nil
	}
	feedKeys(s, "qaAa<C-n><C-y><Esc>q")
	if got := s.buf.Text(); got != "xyz" {
		t.Fatalf("text = %q", got)
	}
	reg, _ := s.registers.Get('a')
	// <C-n> は記録から外れ、<C-y> は <BS>xyz に置き換わる（<BS> はレジスターの中で \b である）。
	if want := "Aa\bxyz\x1b"; reg.Text != want {
		t.Errorf("a = %q, want %q", reg.Text, want)
	}
	feedKeys(s, ".")
	if got := s.buf.Text(); got != "xyzxyz" {
		t.Errorf(". の後 = %q", got)
	}
	feedKeys(s, "@a")
	if got := s.buf.Text(); got != "xyzxyzxyz" {
		t.Errorf("@a の後 = %q", got)
	}
}
