package server

import (
	"context"
	"sync/atomic"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// この節は、書き足すテキストと入力の行の組（ext.ViewConsole）の入力の行の補完（console/complete。ext の consolecomplete.go。FR-297）を扱う。

// ConsoleCompleteHandler は、console/complete に答えるハンドラーである（API 0.24.0 から）。
// 部品を開くときに ext.ViewComponent.Complete を真にした console の部品で、本体が入力の行の中身 p.Text とカーソル p.Cursor への候補を問う。
// 候補を求められないとき（デバッグのセッションが無いなど）は、誤りでなく OK を false にした結果を返す。
type ConsoleCompleteHandler func(ctx context.Context, p *ext.ConsoleCompleteParams) (ext.ConsoleCompleteResult, error)

// AsyncConsoleCompleteHandler は、console/complete に後から答えるハンドラーである。
// デバッグアダプターの completions のように、答えに時間のかかる問い合わせ先へ問うときに使う。守ることは他の非同期のハンドラーと同じである。
// 本体は、入力の行が変わるたびに前の問い合わせを $/cancel で取り消すので、ctx が取り消されたら ctx.Err() で reply してよい。
type AsyncConsoleCompleteHandler func(ctx context.Context, p *ext.ConsoleCompleteParams, reply func(ext.ConsoleCompleteResult, error))

// ConsoleComplete は、console/complete のハンドラーを登録する（FR-297）。登録は 1 つだけで、拡張機能が開いたどの console の部品の
// 問い合わせも届く（p.View と p.Child で見分ける）。2 回登録するか（ConsoleCompleteAsync との重複を含む）、Serve の後に呼ぶと panic する。
// 登録していなければ、Server は OK を false にした結果を応答する。
func (s *Server) ConsoleComplete(h ConsoleCompleteHandler) {
	s.registerConsoleComplete(func(ctx context.Context, p *ext.ConsoleCompleteParams, reply func(ext.ConsoleCompleteResult, error)) {
		reply(h(ctx, p))
	})
}

// ConsoleCompleteAsync は、ConsoleComplete の非同期版である。
func (s *Server) ConsoleCompleteAsync(h AsyncConsoleCompleteHandler) {
	s.registerConsoleComplete(h)
}

func (s *Server) registerConsoleComplete(h AsyncConsoleCompleteHandler) {
	s.mustNotStarted()
	if s.consoleComplete != nil {
		panic("server: console/complete handler registered twice")
	}
	s.consoleComplete = h
}

// consoleCompleteRequest は、console/complete を、登録したハンドラーへ渡す。登録していなければ OK を false にした結果を応答する。
func (s *Server) consoleCompleteRequest(ctx context.Context, req *rpc.Request, reply rpc.Replier) {
	var p ext.ConsoleCompleteParams
	if err := decodeParams(req, &p); err != nil {
		s.reply(req.Method, reply, nil, err)
		return
	}
	h := s.consoleComplete
	if h == nil {
		s.reply(req.Method, reply, ext.ConsoleCompleteResult{}, nil)
		return
	}
	var replied atomic.Bool
	respond := func(res ext.ConsoleCompleteResult, err error) {
		if !replied.CompareAndSwap(false, true) {
			s.logf("%s: reply called more than once", req.Method)
			return
		}
		if err != nil {
			s.reply(req.Method, reply, nil, err)
			return
		}
		s.reply(req.Method, reply, res, nil)
	}
	err := s.protect(req.Method, func() error {
		h(ctx, &p, respond)
		return nil
	})
	if err != nil && !replied.Load() {
		respond(ext.ConsoleCompleteResult{}, err)
	}
}
