package server

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// MotionHandler は、motion/resolve に答えるハンドラーである。
// b は p.Buffer の複製で、そのバージョン番号は p.Version と一致する。
// 返した result の Version は、Server が p.Version で上書きする。
type MotionHandler func(ctx context.Context, b *Buffer, p *ext.MotionResolveParams) (ext.MotionResolveResult, error)

// TextObjectHandler は、textObject/resolve に答えるハンドラーである。
// b は p.Buffer の複製で、そのバージョン番号は p.Version と一致する。
// 返した result の Version は、Server が p.Version で上書きする。
type TextObjectHandler func(ctx context.Context, b *Buffer, p *ext.TextObjectResolveParams) (ext.TextObjectResolveResult, error)

// OperatorHandler は、operator/apply に答えるハンドラーである。
// b は p.Buffer の複製で、そのバージョン番号は p.Version と一致する。
// 返した result の Transaction.BaseVersion は、Server が p.Version で上書きする。
type OperatorHandler func(ctx context.Context, b *Buffer, p *ext.OperatorApplyParams) (ext.OperatorApplyResult, error)

// CommandHandler は、command/execute に答えるハンドラーである。
// b は p.Buffer の複製で、そのバージョン番号は p.Version と一致する。
// ただし p.Withheld が true（本体が不正な UTF-8 のためにバッファを送っていない）なら b は nil である。
// バッファを使うコマンドは、そのとき OK を false で返す。
// 宣言ファイルに noBufferSync を書いた拡張機能では、本体がどのバッファも送らず、p.Withheld をいつも立てるので、b はいつも nil である
// （ext.Manifest.NoBufferSync。#367）。そうした拡張機能では、バッファの複製を待つ WaitBuffer も、待たずに ErrNoBufferSync を返す。
// 返した result に Transaction があれば、その BaseVersion は、Server が p.Version で上書きする。
type CommandHandler func(ctx context.Context, b *Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error)

// 非同期のハンドラー（AsyncMotionHandler など）は、同期のハンドラーと同じくループの上で呼ばれ、速やかに戻る。
// 結果は、戻った後に別の goroutine から reply で返してよい（戻る前に返してもよい）。
// その間、ループは次のメッセージ（buffer/change、ping、他の問い合わせ）を処理する。
//
// 守ること:
//
//   - reply は、ctx が取り消された後も含めて、必ず 1 回呼ぶ。取り消されたら ctx.Err() を誤りにして呼べばよい。
//     2 回目以降の呼び出しは何も送らず、ErrorLog に書く。接続が終わった後の呼び出しは何も送らない
//   - b は、ハンドラーが戻るまでの間だけ内容を保つ。戻った後に読むなら、戻る前に b.Clone() を取る
//   - 結果の Version（Transaction.BaseVersion）は、同期のハンドラーと同じく Server が p.Version で上書きする。
//     reply までに本体のバッファが変わっていれば、本体は結果を破棄する（基本設計 5.4）
//   - ハンドラーが戻る前に panic したら、Server が失敗を応答する。reply を呼んだ別の goroutine の panic は回復しない

// AsyncMotionHandler は、motion/resolve に後から答えるハンドラーである。
type AsyncMotionHandler func(ctx context.Context, b *Buffer, p *ext.MotionResolveParams, reply func(ext.MotionResolveResult, error))

// AsyncTextObjectHandler は、textObject/resolve に後から答えるハンドラーである。
type AsyncTextObjectHandler func(ctx context.Context, b *Buffer, p *ext.TextObjectResolveParams, reply func(ext.TextObjectResolveResult, error))

// AsyncOperatorHandler は、operator/apply に後から答えるハンドラーである。
type AsyncOperatorHandler func(ctx context.Context, b *Buffer, p *ext.OperatorApplyParams, reply func(ext.OperatorApplyResult, error))

// AsyncCommandHandler は、command/execute に後から答えるハンドラーである。
// b が nil になる場合は CommandHandler と同じである。
type AsyncCommandHandler func(ctx context.Context, b *Buffer, p *ext.CommandExecuteParams, reply func(ext.CommandExecuteResult, error))

// entry は、登録したハンドラーを、同期と非同期の区別なく呼ぶための形である。
type entry[P, R any] func(ctx context.Context, b *Buffer, p *P, reply func(R, error))

func syncEntry[P, R any, H ~func(context.Context, *Buffer, *P) (R, error)](h H) entry[P, R] {
	return func(ctx context.Context, b *Buffer, p *P, reply func(R, error)) {
		reply(h(ctx, b, p))
	}
}

func asyncEntry[P, R any, H ~func(context.Context, *Buffer, *P, func(R, error))](h H) entry[P, R] {
	return entry[P, R](h)
}

// Motion は、宣言ファイルの contributes.motions に書いた id のモーションのハンドラーを登録する。
// 同じ id を 2 回登録するか（MotionAsync との重複を含む）、Serve の後に呼ぶと panic する。
//
// req: FR-11
func (s *Server) Motion(id string, h MotionHandler) {
	register(s, s.motions, "motion", id, syncEntry[ext.MotionResolveParams, ext.MotionResolveResult](h))
}

// MotionAsync は、Motion の非同期版である。
func (s *Server) MotionAsync(id string, h AsyncMotionHandler) {
	register(s, s.motions, "motion", id, asyncEntry[ext.MotionResolveParams, ext.MotionResolveResult](h))
}

// TextObject は、宣言ファイルの contributes.textObjects に書いた id のテキストオブジェクトのハンドラーを登録する。
// 同じ id を 2 回登録するか（TextObjectAsync との重複を含む）、Serve の後に呼ぶと panic する。
//
// req: FR-11
func (s *Server) TextObject(id string, h TextObjectHandler) {
	register(s, s.textObjects, "text object", id, syncEntry[ext.TextObjectResolveParams, ext.TextObjectResolveResult](h))
}

// TextObjectAsync は、TextObject の非同期版である。
func (s *Server) TextObjectAsync(id string, h AsyncTextObjectHandler) {
	register(s, s.textObjects, "text object", id, asyncEntry[ext.TextObjectResolveParams, ext.TextObjectResolveResult](h))
}

// Operator は、宣言ファイルの contributes.operators に書いた id のオペレーターのハンドラーを登録する。
// 同じ id を 2 回登録するか（OperatorAsync との重複を含む）、Serve の後に呼ぶと panic する。
//
// req: FR-11
func (s *Server) Operator(id string, h OperatorHandler) {
	register(s, s.operators, "operator", id, syncEntry[ext.OperatorApplyParams, ext.OperatorApplyResult](h))
}

// OperatorAsync は、Operator の非同期版である。
func (s *Server) OperatorAsync(id string, h AsyncOperatorHandler) {
	register(s, s.operators, "operator", id, asyncEntry[ext.OperatorApplyParams, ext.OperatorApplyResult](h))
}

// Command は、宣言ファイルの contributes.commands に書いた id のコマンドのハンドラーを登録する。
// 同じ id を 2 回登録するか（CommandAsync との重複を含む）、Serve の後に呼ぶと panic する。
//
// req: FR-11
func (s *Server) Command(id string, h CommandHandler) {
	register(s, s.commands, "command", id, syncEntry[ext.CommandExecuteParams, ext.CommandExecuteResult](h))
}

// CommandAsync は、Command の非同期版である。
func (s *Server) CommandAsync(id string, h AsyncCommandHandler) {
	register(s, s.commands, "command", id, asyncEntry[ext.CommandExecuteParams, ext.CommandExecuteResult](h))
}

// ProviderHandler は、provider/invoke に答えるハンドラーである（基本設計 5.5。FR-94）。
// b が nil になる場合と、結果の Transaction の BaseVersion の扱いは CommandHandler と同じである。
type ProviderHandler func(ctx context.Context, b *Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, error)

// AsyncProviderHandler は、provider/invoke に後から答えるハンドラーである。
type AsyncProviderHandler func(ctx context.Context, b *Buffer, p *ext.ProviderInvokeParams, reply func(ext.ProviderInvokeResult, error))

// Provider は、宣言ファイルの provides に書いた本体の操作 op のハンドラーを登録する（FR-94）。
// 同じ op を 2 回登録するか（ProviderAsync との重複を含む）、Serve の後に呼ぶと panic する。
// 登録していない操作の provider/invoke には、JSON-RPC のエラー（InvalidParams）を応答する。
//
// req: FR-94
func (s *Server) Provider(op ext.Operation, h ProviderHandler) {
	register(s, s.providers, "provider", string(op), syncEntry[ext.ProviderInvokeParams, ext.ProviderInvokeResult](h))
}

// ProviderAsync は、Provider の非同期版である。
func (s *Server) ProviderAsync(op ext.Operation, h AsyncProviderHandler) {
	register(s, s.providers, "provider", string(op), asyncEntry[ext.ProviderInvokeParams, ext.ProviderInvokeResult](h))
}

func register[P, R any](s *Server, m map[string]entry[P, R], kind, id string, h entry[P, R]) {
	s.mustNotStarted()
	if _, dup := m[id]; dup {
		panic(fmt.Sprintf("server: %s %q registered twice", kind, id))
	}
	m[id] = h
}

// query は、キー入力ごとの問い合わせ（とコマンド）を、id のハンドラーへ渡す。
// バッファの複製が params の Version と一致する場合だけハンドラーを呼び、結果のバージョン番号を stamp で埋めて応答する。
// withheld が nil でなく true を返したら（本体がバッファを送っていない command/execute）、複製を探さずに nil を渡す。
func query[P, R any](
	ctx context.Context, s *Server, req *rpc.Request, reply rpc.Replier,
	handlers map[string]entry[P, R],
	meta func(*P) (ext.BufferID, ext.Version, string),
	withheld func(*P) bool,
	stamp func(*R, ext.Version),
) {
	var p P
	if err := decodeParams(req, &p); err != nil {
		s.reply(req.Method, reply, nil, err)
		return
	}
	bufID, version, id := meta(&p)
	h, ok := handlers[id]
	if !ok {
		s.reply(req.Method, reply, nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("%s: unknown id %q", req.Method, id)})
		return
	}
	var b *Buffer
	if withheld == nil || !withheld(&p) {
		var err error
		if b, err = s.bufferAt(bufID, version); err != nil {
			s.reply(req.Method, reply, nil, err)
			return
		}
	}
	name := req.Method + " " + id
	var replied atomic.Bool
	respond := func(res R, err error) {
		if !replied.CompareAndSwap(false, true) {
			s.logf("%s: reply called more than once", name)
			return
		}
		if err != nil {
			s.reply(req.Method, reply, nil, err)
			return
		}
		stamp(&res, version)
		s.reply(req.Method, reply, res, nil)
	}
	err := s.protect(name, func() error {
		h(ctx, b, &p, respond)
		return nil
	})
	if err != nil && !replied.Load() {
		var zero R
		respond(zero, err)
	}
}
