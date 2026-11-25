package server_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// console/complete は登録したハンドラーへ届き、その結果を応答する。登録していなければ OK が false の結果を応答する（FR-297）。
func TestConsoleComplete(t *testing.T) {
	t.Run("登録した", func(t *testing.T) {
		s := server.New()
		s.ConsoleComplete(func(_ context.Context, p *ext.ConsoleCompleteParams) (ext.ConsoleCompleteResult, error) {
			return ext.ConsoleCompleteResult{OK: true, Items: []ext.ConsoleCompletionItem{
				{Label: p.Text[:p.Cursor] + "x", Start: 0, End: p.Cursor, Kind: p.Child},
			}}, nil
		})
		h := start(t, s)
		var res ext.ConsoleCompleteResult
		if err := h.call(t, ext.MethodConsoleComplete, ext.ConsoleCompleteParams{View: 1, Child: "repl", Text: "ab", Cursor: 1, Seq: 1}, &res); err != nil {
			t.Fatal(err)
		}
		want := ext.ConsoleCompleteResult{OK: true, Items: []ext.ConsoleCompletionItem{{Label: "ax", Start: 0, End: 1, Kind: "repl"}}}
		if !reflect.DeepEqual(res, want) {
			t.Errorf("result = %+v, want %+v", res, want)
		}
	})
	t.Run("登録していない", func(t *testing.T) {
		h := start(t, server.New())
		res := ext.ConsoleCompleteResult{OK: true}
		if err := h.call(t, ext.MethodConsoleComplete, ext.ConsoleCompleteParams{View: 1, Text: "a", Cursor: 1, Seq: 1}, &res); err != nil {
			t.Fatal(err)
		}
		if res.OK || len(res.Items) > 0 {
			t.Errorf("result = %+v, want OK false", res)
		}
	})
	t.Run("誤り", func(t *testing.T) {
		s := server.New()
		s.ConsoleComplete(func(context.Context, *ext.ConsoleCompleteParams) (ext.ConsoleCompleteResult, error) {
			return ext.ConsoleCompleteResult{}, errors.New("boom")
		})
		h := start(t, s)
		if err := h.call(t, ext.MethodConsoleComplete, ext.ConsoleCompleteParams{View: 1, Seq: 1}, nil); err == nil {
			t.Error("err = nil, want the handler's error")
		}
	})
}

// 非同期のハンドラーが答えるまでの間も、ループは ping を処理する。2 回目の登録は panic する。
func TestConsoleCompleteAsync(t *testing.T) {
	s := server.New()
	replies := make(chan func(ext.ConsoleCompleteResult, error), 1)
	s.ConsoleCompleteAsync(func(_ context.Context, _ *ext.ConsoleCompleteParams, reply func(ext.ConsoleCompleteResult, error)) {
		replies <- reply
	})
	func() {
		defer func() {
			if recover() == nil {
				t.Error("2 回目の登録が panic しない")
			}
		}()
		s.ConsoleComplete(func(context.Context, *ext.ConsoleCompleteParams) (ext.ConsoleCompleteResult, error) {
			return ext.ConsoleCompleteResult{}, nil
		})
	}()
	h := start(t, s)
	f, err := h.conn.Go(ext.MethodConsoleComplete, ext.ConsoleCompleteParams{View: 1, Text: "a", Cursor: 1, Seq: 1})
	if err != nil {
		t.Fatal(err)
	}
	reply := <-replies
	h.ping(t)
	select {
	case <-f.Ready():
		t.Fatal("answered before reply")
	default:
	}
	reply(ext.ConsoleCompleteResult{OK: true}, nil)
	var res ext.ConsoleCompleteResult
	if err := f.Result(&res); err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Errorf("result = %+v", res)
	}
}
