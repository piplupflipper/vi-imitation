package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// この節は、本体のレジスターへテキストを入れる口（register/set と view/keySync。ext の register.go。FR-315）を扱う。

// MaxRegisterSetBytes は、register/set の params（と、view/keySync の result）を JSON にした後の大きさの上限（バイト）である。
// 本体は、拡張機能から届く 1 件のメッセージの本文を rpc.DefaultMaxMessageSize（64 MiB）までしか読まず、超えたら接続を切って
// 拡張機能を止める（PR #433 のレビューの推奨 1）。JSON-RPC の封筒（jsonrpc、id、method）の分として 4 KiB を引いた。
// 大きさは、送るときと同じ encoding/json で数える（"<" は 6 バイトの "<" になる）。
const MaxRegisterSetBytes = rpc.DefaultMaxMessageSize - 4<<10

// ErrRegisterTooLarge は、register/set の params が MaxRegisterSetBytes を超えたので送らなかったことを表す。errors.Is で見分ける。
var ErrRegisterTooLarge = errors.New("server: register/set exceeds the message size limit")

// registerSendTimeout は、SetRegisterAsync が 1 件の register/set の応答を待つ期限である（仮）。
// 本体はイベントループで書いてすぐ応答するので、期限は本体が止まったときに列が詰まり続けないためだけにある。
const registerSendTimeout = 30 * time.Second

// CheckRegisterSet は、p が ext.RegisterSetParams.Validate を通り、JSON にした大きさが MaxRegisterSetBytes 以下かを確かめる。
// 大きすぎれば、JSON にした大きさを添えて ErrRegisterTooLarge を包んだ誤りを返す。
// SetRegister、SetRegisterAsync、ViewEvent.Defer の応答は、これで確かめてから送る。
func CheckRegisterSet(p ext.RegisterSetParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(b) > MaxRegisterSetBytes {
		return fmt.Errorf("%w (%d bytes > %d)", ErrRegisterTooLarge, len(b), MaxRegisterSetBytes)
	}
	return nil
}

// registerQueue は、送る register/set の列である。1 本の goroutine が、積んだ順に 1 件ずつ送り、応答を待ってから次を送る。
type registerQueue struct {
	mu      sync.Mutex
	items   []registerItem
	sending bool
}

type registerItem struct {
	p    ext.RegisterSetParams
	done func(error)
}

// SetRegisterAsync は、本体へ送る register/set（ext.MethodRegisterSet の doc。API 0.25.0 から。FR-315）を列に積み、応答を待たずに戻る。
// 積んだ順に 1 件ずつ送り、前の応答を待ってから次を送るので、本体は呼んだ順に書く（"ac の後の "Ac が入れ替わらない。PR #433 の
// レビューの必須 1）。順は、この関数を呼んだ順である。続けて入れるものは、同じ goroutine（OnView のフックか、拡張機能が持つ 1 本の
// goroutine）から順に呼ぶ。別々の goroutine を起こしてから呼ぶと、呼ぶ順が決まらない。
// ループの上（フックの中）からも呼べる。done は、応答か誤りを受けたときに、送る goroutine から呼ぶ（nil でよい）。
// p が CheckRegisterSet を通らなければ、積まずにその誤りを返す（done は呼ばない）。
//
// 入れ方と知らせ（履歴、ホストのクリップボード、上限を超えたときのメッセージ）は、本体の y と同じである。
// 利用者がキーの前にレジスターの名前を付けたなら ViewEvent.Register を p.Register に、キーの番号 ViewEvent.Origin を p.Origin に入れる。
// 使えない名前なら、本体は E354 をメッセージの行に出して誤りで応答する。
func (s *Server) SetRegisterAsync(p ext.RegisterSetParams, done func(error)) error {
	if err := CheckRegisterSet(p); err != nil {
		return err
	}
	q := &s.registers
	q.mu.Lock()
	q.items = append(q.items, registerItem{p: p, done: done})
	start := !q.sending
	q.sending = true
	q.mu.Unlock()
	if start {
		go s.sendRegisters()
	}
	return nil
}

// SetRegister は、SetRegisterAsync と同じく列に積み、その応答を待つ。順は SetRegisterAsync と同じく呼んだ順である。
// ctx が先に終われば ctx の誤りを返すが、積んだものは送る。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す（積まない）。フックの中からは SetRegisterAsync を使う。
func (s *Server) SetRegister(ctx context.Context, p ext.RegisterSetParams) error {
	if onLoop(ctx, s) {
		return ErrEditInHandler
	}
	ch := make(chan error, 1)
	if err := s.SetRegisterAsync(p, func(err error) { ch <- err }); err != nil {
		return err
	}
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendRegisters は、列が空になるまで、先頭から 1 件ずつ register/set を送る。
func (s *Server) sendRegisters() {
	q := &s.registers
	for {
		q.mu.Lock()
		if len(q.items) == 0 {
			q.sending = false
			q.mu.Unlock()
			return
		}
		it := q.items[0]
		q.items[0] = registerItem{}
		q.items = q.items[1:]
		q.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), registerSendTimeout)
		_, err := call[ext.RegisterSetResult](ctx, s, ext.MethodRegisterSet, it.p)
		cancel()
		if err != nil {
			s.logf("%s: %v", ext.MethodRegisterSet, err)
		}
		if it.done != nil {
			it.done(err)
		}
	}
}

// ViewKeyReply は、view/keySync に応答する関数である（ViewEvent.Defer）。1 回だけ呼ぶ。どの goroutine から呼んでもよい。
// res.Register が CheckRegisterSet を通らなければ、Register を省いた結果で応答し（本体は再生を続ける）、その誤りを返す。
type ViewKeyReply func(res ext.ViewKeyResult) error

// viewKeySync は、view/keySync（ext.MethodViewKeySync）を、Sync を真にした ViewKey の ViewEvent にして OnView のフックへ渡す。
// フックが ViewEvent.Defer を呼ばずに戻ったら、空の結果で応答する。Defer を呼んだら、返した関数が応答する。
func (s *Server) viewKeySync(ctx context.Context, req *rpc.Request, reply rpc.Replier) {
	var p ext.ViewKeyParams
	if err := decodeParams(req, &p); err != nil {
		s.reply(req.Method, reply, nil, err)
		return
	}
	var replied atomic.Bool
	respond := func(res ext.ViewKeyResult) error {
		if !replied.CompareAndSwap(false, true) {
			s.logf("%s: reply called more than once", req.Method)
			return nil
		}
		var err error
		if res.Register != nil {
			if err = CheckRegisterSet(*res.Register); err != nil {
				res.Register = nil
			}
		}
		s.reply(req.Method, reply, res, nil)
		return err
	}
	e := &ViewEvent{
		Kind: ViewKey, View: p.View, Item: p.Item, Key: p.ID, Input: p.Input, Child: p.Child,
		Register: p.Register, Origin: p.Origin, Sync: true, respond: respond,
	}
	if s.onView != nil {
		s.hook("view hook", func() { s.onView(ctx, e) })
	}
	if !e.deferred {
		_ = respond(ext.ViewKeyResult{})
	}
}

// Defer は、Sync の ViewKey（view/keySync）の応答を後に回し、応答する関数を返す。フックの中で呼ぶ。
// Sync でない知らせでは nil を返す（拡張機能は、そのときは SetRegisterAsync で入れる）。
// 返した関数は必ず 1 回呼ぶ。呼ばないと、本体は再生の期限まで待ち、その位置で再生を止める。
func (e *ViewEvent) Defer() ViewKeyReply {
	if !e.Sync || e.respond == nil {
		return nil
	}
	e.deferred = true
	return e.respond
}
