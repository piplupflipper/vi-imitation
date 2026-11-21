package input

import (
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// LSP と DAP の操作の既定のキー（要件定義 4.11 の 2 つの表）は、leader に従って、提供者へ振り分ける操作として登録する。
// gq はオペレーターで、ノーマルモードとビジュアルモードで使える。
//
// req: FR-95, FR-96
func TestProviderKeys(t *testing.T) {
	want := map[string]string{
		"<Leader>lh": "hover", "K": "hover",
		"<Leader>ld": "peek.definition", "<Leader>lD": "definition", "<C-]>": "definition",
		"<Leader>li": "peek.implementation", "<Leader>lI": "implementation",
		"<Leader>lt": "peek.typeDefinition", "<Leader>lT": "typeDefinition",
		"<Leader>lr": "references", "<Leader>ln": "rename", "<Leader>la": "codeAction",
		"<Leader>ls": "documentSymbols", "<Leader>lS": "workspaceSymbols",
		"<Leader>le": "diagnostic.list", "<Leader>lE": "diagnostic.listAll",
		"<Leader>lj": "diagnostic.next", "<Leader>lk": "diagnostic.prev",
		"<Leader>dc": "debug.continue", "<Leader>dp": "debug.pause", "<Leader>dt": "debug.terminate",
		"<Leader>dr": "debug.restart", "<Leader>db": "breakpoint.toggle", "<Leader>dB": "breakpoint.conditional",
		"<Leader>dX": "breakpoint.clear", "<Leader>dn": "debug.stepOver", "<Leader>di": "debug.stepIn",
		"<Leader>do": "debug.stepOut", "<Leader>dl": "debug.runLast", "<Leader>du": "debug.toggleUI",
	}
	for _, leader := range []string{"<Space>", ","} {
		lk := editcmd.MustParseKeys(leader)
		km, errs := DefaultKeymapLeader(lk)
		if len(errs) != 0 {
			t.Fatalf("leader %s: %v", leader, errs)
		}
		for keys, id := range want {
			ks, err := editcmd.ParseKeysLeader(keys, lk)
			if err != nil {
				t.Fatal(err)
			}
			b, ok := km.Lookup(MapNormal, ks)
			if !ok || b.Kind != BindAction || b.Ref != (editcmd.Ref{ID: id, Extension: ProviderExtension}) {
				t.Errorf("leader %s, %s: got %+v, %v, want %s", leader, keys, b, ok, id)
			}
		}
		for _, mode := range []MapMode{MapNormal, MapVisual} {
			b, ok := km.Lookup(mode, editcmd.MustParseKeys("gq"))
			if !ok || b.Kind != BindOperator || b.Ref != (editcmd.Ref{ID: "format", Extension: ProviderExtension}) {
				t.Errorf("gq (mode %b): got %+v, %v", mode, b, ok)
			}
		}
	}
	// <Space> は既定の leader なので、DefaultKeymap でも同じに引ける。
	if b, ok := DefaultKeymap().Lookup(MapNormal, editcmd.MustParseKeys("<Space>dc")); !ok || b.Ref.ID != "debug.continue" {
		t.Errorf("DefaultKeymap <Space>dc = %+v, %v", b, ok)
	}
	// 本体の操作と接頭辞を共有する leader でも、本体の操作は残る。
	km, errs := DefaultKeymapLeader(editcmd.MustParseKeys("g"))
	if len(errs) != 0 {
		t.Errorf("leader g: %v", errs)
	}
	if b, ok := km.Lookup(MapNormal, editcmd.MustParseKeys("gg")); !ok || !b.Ref.Builtin() {
		t.Errorf("leader g: gg = %+v, %v", b, ok)
	}
}

// 本体の操作の表の <Leader>a（select-all）は、leader に従って、ノーマルモードとビジュアルモードの本体の操作として登録する。
// 利用者の leader で先に登録した本体のキーと同じ列になれば（leader が s なら sa）、登録せずに誤りを返し、先のキーを残す。
//
// req: FR-130
func TestLeaderSelectAll(t *testing.T) {
	for _, leader := range []string{"<Space>", ",", "g", "<C-x>"} {
		lk := editcmd.MustParseKeys(leader)
		km, errs := DefaultKeymapLeader(lk)
		if len(errs) != 0 {
			t.Fatalf("leader %s: %v", leader, errs)
		}
		keys, err := editcmd.ParseKeysLeader("<Leader>a", lk)
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []MapMode{MapNormal, MapVisual} {
			b, ok := km.Lookup(mode, keys)
			if !ok || b.Kind != BindAction || b.Ref != (editcmd.Ref{ID: "select-all"}) || b.Modes != MapNormal|MapVisual {
				t.Errorf("leader %s, %s: <Leader>a = %+v, %v", leader, ModeName(mode), b, ok)
			}
		}
	}
	for _, mode := range []MapMode{MapNormal, MapVisual} {
		if b, ok := DefaultKeymap().Lookup(mode, editcmd.MustParseKeys("<Space>a")); !ok || b.Ref.ID != "select-all" {
			t.Errorf("DefaultKeymap %s <Space>a = %+v, %v", ModeName(mode), b, ok)
		}
	}
	km, errs := DefaultKeymapLeader(editcmd.MustParseKeys("s"))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "select-all") {
		t.Errorf("leader s: errs = %v", errs)
	}
	if b, ok := km.Lookup(MapNormal, editcmd.MustParseKeys("sa")); !ok || b.Ref.ID != "surround-add" {
		t.Errorf("leader s: sa = %+v, %v", b, ok)
	}
}

// 設定の leader として受け付けるキーで始まる列は、どのモードでも操作の表に登録でき（先に読むキーの表を共有する）、
// 先に読むキーで始まる leader は誤りになる。本体の既定の表と拡張機能の <Leader> のキーが、leader の値で登録できなくならない。
//
// req: FR-97
func TestLeaderNotReserved(t *testing.T) {
	ref := editcmd.Ref{ID: "x.op", Extension: "x"}
	for r := rune(0x21); r < 0x7f; r++ {
		k := editcmd.Rune(r)
		_, leaderErr := editcmd.ParseLeader(editcmd.KeysString([]editcmd.Key{k}))
		reserved := reservedFirstKey(k, MapNormal|MapVisual|MapOperatorPending)
		if reserved != (leaderErr != nil) {
			t.Errorf("%q: 先に読むキー = %v, ParseLeader の誤り = %v", r, reserved, leaderErr)
		}
		if leaderErr != nil {
			continue
		}
		keys, err := editcmd.ParseKeysLeader("<Leader>zz", []editcmd.Key{k})
		if err != nil {
			t.Fatal(err)
		}
		km := NewKeymap()
		if err := km.Bind(Binding{Kind: BindMotion, Keys: keys, Ref: ref}); err != nil {
			t.Errorf("leader %q: %v", r, err)
		}
	}
}
