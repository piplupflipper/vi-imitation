package ext

import "fmt"

// MethodVirtualTextSet は、拡張機能から本体への通知である。params は VirtualTextSetParams。
// バッファの位置に、本文に無い文字列（仮想テキスト）を色付きで出す（基本設計 6.1 の「仮想テキスト」。FR-231〜FR-234）。
// インレイヒント（LSP の textDocument/inlayHint）、行末の注記、行頭の空白の上に重ねるブロックの範囲の罫線などに使う。
//
// 一覧は、送った拡張機能とバッファの組ごとに持ち、同じ組の前の一覧だけを置き換える。空の一覧を送ると、その組の仮想テキストを消す。
// 位置は、診断（diagnostic/set）と同じく、本体がバッファへ編集を適用するたびにその編集に合わせてずらす（基本設計 6.7 の表示のデータ）。
// Version は一覧を計算するもとにしたバージョン番号で、扱いも診断と同じである（現在より新しい番号の一覧は捨て、本体が覚えている編集より
// 古い番号の空でない一覧も捨てる）。拡張機能が異常で終わるか止まったら、本体はその拡張機能の仮想テキストをすべて消す。
//
// 古い本体（0.18.0 まで）は、知らない通知として捨てる（ログに残すだけ）ので、仮想テキストは出ない。
const MethodVirtualTextSet = "virtualText/set"

// VirtualTextPlacement は、仮想テキストの置き方である（Neovim 0.12.5 の nvim_buf_set_extmark の virt_text_pos）。
type VirtualTextPlacement string

// 仮想テキストの置き方。
const (
	// VirtualTextInline は、位置の文字の前に差し込み、後ろの本文を右へずらす（virt_text_pos の "inline"）。
	// 位置が行の長さ以上なら、行の最後の文字の後ろに出す。本文と同じく、行を折り返すときは折り返し、横のスクロールでは一緒に送る。
	// カーソルは仮想テキストの上に置かない。
	VirtualTextInline VirtualTextPlacement = "inline"
	// VirtualTextEOL は、行末の後ろに 1 列空けて出す（virt_text_pos の "eol"）。位置の行だけを読み、バイトは並べる順にだけ使う。
	// 同じ行に複数あれば、位置のバイトの順（同じなら一覧の順）に 1 列ずつ空けて並べる。行を折り返すときも見た目の行を増やさず、
	// 右の端で切る。
	VirtualTextEOL VirtualTextPlacement = "eol"
	// VirtualTextOverlay は、位置の文字の列から、本文を右へずらさずに重ねて描く（virt_text_pos の "overlay"）。
	// 行末より右にもはみ出して描き、見た目の行の右の端で切る。
	VirtualTextOverlay VirtualTextPlacement = "overlay"
)

// VirtualTextChunk は、仮想テキストの、同じ色で描く 1 つの文字列である（Neovim の virt_text の [text, hl_group] の組）。
type VirtualTextChunk struct {
	// Text は、描く文字列である。改行、復帰、タブ文字は空白 1 つとして描く。ほかの制御文字は、本文と同じく ^A の形で描く。
	Text string `json:"text"`
	// Style は、色の種類の名前（Decoration.Style と同じ名前の表）である。空なら名前の色を付けない。
	Style string `json:"style,omitempty"`
	// Attr は、名前の色の上に重ねて直に付ける色と飾りである（TextAttr）。nil なら付けない。
	Attr *TextAttr `json:"attr,omitempty"`
}

// VirtualText は、バッファの 1 つの位置に出す仮想テキストである。
// 本体は、仮想テキストのセルに、本文の構文の色、診断、検索の一致、範囲の色、選択の色を重ねず、Chunks の色だけで描く
// （Neovim の hl_mode の既定の "replace"）。
type VirtualText struct {
	// Position は、仮想テキストを付ける位置である。Line がバッファの外なら描かない。
	// Byte が文字の途中を指すときは、その文字の位置として扱う。行の長さを超えれば行末の位置にする。
	Position Position `json:"position"`
	// Placement は、置き方である。
	Placement VirtualTextPlacement `json:"placement"`
	// Chunks は、描く文字列の並びである。
	Chunks []VirtualTextChunk `json:"chunks"`
}

// VirtualTextSetParams は、virtualText/set の params である。
type VirtualTextSetParams struct {
	Buffer BufferID `json:"buffer"`
	// Version は、一覧を計算するもとにしたバージョン番号である。
	Version Version `json:"version"`
	// VirtualTexts は、バッファ全体について、この拡張機能が出す仮想テキストの一覧である。同じ位置の inline と overlay は、一覧の順に並べて描く。
	// 空（nil を含む）なら、その組の仮想テキストを消す。
	VirtualTexts []VirtualText `json:"virtualTexts"`
}

// Validate は、置き方が 3 つのどれかで、色が "#rrggbb" か空であることを確かめる。
// 本体は、誤りのある通知をログに書いて捨てる（前の一覧を残す）。
func (p *VirtualTextSetParams) Validate() error {
	for i, v := range p.VirtualTexts {
		switch v.Placement {
		case VirtualTextInline, VirtualTextEOL, VirtualTextOverlay:
		default:
			return fmt.Errorf("virtualTexts[%d]: unknown placement %q", i, v.Placement)
		}
		for j, c := range v.Chunks {
			if c.Attr == nil {
				continue
			}
			if err := c.Attr.Validate(); err != nil {
				return fmt.Errorf("virtualTexts[%d].chunks[%d]: %w", i, j, err)
			}
		}
	}
	return nil
}
