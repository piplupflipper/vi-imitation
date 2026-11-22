package server

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// ErrMessageTooLarge は、本体の受信の上限を超えるので送らなかったことを表す（SetVirtualTexts）。
// 上限を超えたメッセージを本体は読まずに接続を閉じるので、送る前に断る。接続は切れず、ほかの通知とリクエストはそのまま使える。
var ErrMessageTooLarge = errors.New("server: message exceeds the host's receive limit")

// maxVirtualTextParams は、virtualText/set の params の JSON の大きさの上限（バイト）である。
// 本体の受信の上限（rpc.DefaultMaxMessageSize。1 件の本文の上限）から、JSON-RPC の包み（jsonrpc と method の項目）の分を引いた。
const maxVirtualTextParams = rpc.DefaultMaxMessageSize - 1024

// SetVirtualTexts は、本体へ virtualText/set を送り、バッファの位置に本文に無い文字列（仮想テキスト）を出させる
// （基本設計 6.1 の「仮想テキスト」。FR-231）。一覧はバッファ全体の分で、同じ拡張機能とバッファの前の一覧を置き換える。
// 空の一覧で、その組の仮想テキストを消す。応答は待たない。どこから呼んでもよい。
//
// 位置は、p.Version のバッファの位置で書く。本体は、その後の編集に合わせて位置をずらす（診断と同じ）。
//
// 1 つの仮想テキストは JSON で 100 バイトほど（色を 1 つ付けた ": int" のインレイヒントで約 110 バイト）になり、
// 本体の受信の上限（64 MiB）には 60 万個ほどで当たる。一覧の JSON が上限を超えるときは送らずに ErrMessageTooLarge を返す
// （前の一覧はそのまま残る）。そのときは、範囲を絞った一覧を送るか、空の一覧で消す。
func (s *Server) SetVirtualTexts(p ext.VirtualTextSetParams) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(b) > maxVirtualTextParams {
		return fmt.Errorf("%w: virtualText/set params are %d bytes (limit %d)", ErrMessageTooLarge, len(b), maxVirtualTextParams)
	}
	return s.notify(ext.MethodVirtualTextSet, json.RawMessage(b))
}
