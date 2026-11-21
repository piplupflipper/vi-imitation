package server

import "github.com/piplupflipper/vi-imitation/ext"

// SetVirtualTexts は、本体へ virtualText/set を送り、バッファの位置に本文に無い文字列（仮想テキスト）を出させる
// （基本設計 6.1 の「仮想テキスト」。FR-231）。一覧はバッファ全体の分で、同じ拡張機能とバッファの前の一覧を置き換える。
// 空の一覧で、その組の仮想テキストを消す。応答は待たない。どこから呼んでもよい。
//
// 位置は、p.Version のバッファの位置で書く。本体は、その後の編集に合わせて位置をずらす（診断と同じ）。
func (s *Server) SetVirtualTexts(p ext.VirtualTextSetParams) error {
	return s.notify(ext.MethodVirtualTextSet, p)
}
