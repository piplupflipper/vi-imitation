package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// SetRegisterAsync は、フックから続けて呼んだ register/set を、本体の応答が遅れても呼んだ順に 1 つずつ送る（PR #433 のレビューの必須 1）。
// 64 MiB の上限を超える値は送らずに ErrRegisterTooLarge を返す（同じレビューの推奨 1）。
//
// req: FR-315
func TestSetRegisterOrder(t *testing.T) {
	s := server.New()
	got := make(chan string, 256)
	inflight := make(chan struct{}, 1)
	handle := func(req *rpc.Request, reply rpc.Replier) bool {
		if req.Method != ext.MethodRegisterSet {
			return false
		}
		select {
		case inflight <- struct{}{}:
		default:
			t.Error("register/set was sent before the previous reply")
		}
		var p ext.RegisterSetParams
		_ = json.Unmarshal(req.Params, &p)
		got <- p.Register + ":" + p.Text
		go func() {
			time.Sleep(time.Duration(rand.IntN(3)) * time.Millisecond) //nolint:gosec // 応答の遅れをばらつかせるだけで、暗号には使わない
			<-inflight
			_ = reply(ext.RegisterSetResult{}, nil)
		}()
		return true
	}
	s.OnView(func(_ context.Context, e *server.ViewEvent) {
		if err := s.SetRegisterAsync(ext.RegisterSetParams{Register: e.Register, Text: e.Item, Origin: e.Origin}, nil); err != nil {
			t.Errorf("SetRegisterAsync = %v", err)
		}
	})
	h := startWith(t, s, handle)
	h.ping(t)
	const n = 50
	var want []string
	for i := range n {
		reg := "A"
		if i == 0 {
			reg = "a"
		}
		item := fmt.Sprint(i)
		want = append(want, reg+":"+item)
		h.notify(t, ext.MethodViewKey, ext.ViewKeyParams{View: 1, ID: "copy", Item: item, Register: reg, Origin: uint64(i + 1)})
	}
	for i := range n {
		select {
		case g := <-got:
			if g != want[i] {
				t.Fatalf("register/set %d = %q, want %q", i, g, want[i])
			}
		case <-time.After(timeout):
			t.Fatalf("register/set %d did not arrive", i)
		}
	}

	// 上限を超える値は送らない。ループの上から呼ぶ SetRegister は ErrEditInHandler を返す。
	big := ext.RegisterSetParams{Text: strings.Repeat("<", server.MaxRegisterSetBytes/6+1)}
	if err := s.SetRegisterAsync(big, nil); !errors.Is(err, server.ErrRegisterTooLarge) {
		t.Errorf("SetRegisterAsync(big) = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.SetRegister(ctx, big); !errors.Is(err, server.ErrRegisterTooLarge) {
		t.Errorf("SetRegister(big) = %v", err)
	}
	select {
	case g := <-got:
		t.Errorf("register/set %q was sent", g)
	default:
	}
}

// view/keySync は、Sync の ViewKey としてフックへ渡り、フックが Defer で返した関数の結果で応答する。
// Defer を呼ばずに戻れば空の結果で応答する。上限を超える Register は省いて応答し、誤りを返す。
//
// req: FR-19, FR-315
func TestViewKeySync(t *testing.T) {
	s := server.New()
	replyErr := make(chan error, 1)
	s.OnView(func(_ context.Context, e *server.ViewEvent) {
		if !e.Sync || e.Kind != server.ViewKey {
			t.Errorf("event = %+v", e)
		}
		switch e.Item {
		case "later":
			reply := e.Defer()
			p := ext.RegisterSetParams{Register: e.Register, Text: "v"}
			go func() { replyErr <- reply(ext.ViewKeyResult{Register: &p}) }()
		case "big":
			reply := e.Defer()
			p := ext.RegisterSetParams{Text: strings.Repeat("x", server.MaxRegisterSetBytes)}
			go func() { replyErr <- reply(ext.ViewKeyResult{Register: &p}) }()
		}
	})
	h := startWith(t, s, nil)
	h.ping(t)
	var res ext.ViewKeyResult
	if err := h.call(t, ext.MethodViewKeySync, ext.ViewKeyParams{View: 1, ID: "copy", Item: "later", Register: "b"}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Register == nil || *res.Register != (ext.RegisterSetParams{Register: "b", Text: "v"}) {
		t.Errorf("later = %+v", res.Register)
	}
	if err := <-replyErr; err != nil {
		t.Errorf("reply = %v", err)
	}
	res = ext.ViewKeyResult{}
	if err := h.call(t, ext.MethodViewKeySync, ext.ViewKeyParams{View: 1, ID: "copy", Item: "now"}, &res); err != nil || res.Register != nil {
		t.Errorf("no Defer = %+v, %v", res, err)
	}
	if err := h.call(t, ext.MethodViewKeySync, ext.ViewKeyParams{View: 1, ID: "copy", Item: "big"}, &res); err != nil || res.Register != nil {
		t.Errorf("big = %+v, %v", res, err)
	}
	if err := <-replyErr; !errors.Is(err, server.ErrRegisterTooLarge) {
		t.Errorf("reply(big) = %v", err)
	}
}
