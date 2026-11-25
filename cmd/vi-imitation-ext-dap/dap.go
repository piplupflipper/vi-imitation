package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// この節は、デバッグアダプターとの DAP の送受信である。
//
// DAP のメッセージの区切りは、本体との JSON-RPC と同じ Content-Length ヘッダーの形式なので、
// 区切りの読み書きは internal/rpc の ReadMessage と WriteMessage を使う。
// 中身は JSON-RPC ではなく、DAP 独自の request、response、event の 3 種類で、
// どれも seq と type を持つ（DAP の仕様の Base Protocol の ProtocolMessage）。
// 仕様: https://microsoft.github.io/debug-adapter-protocol/specification

// maxDAPMessageSize は、デバッグアダプターから受け取る 1 件のメッセージの本文の上限である。
// 本体との接続の既定値（rpc.DefaultMaxMessageSize）に揃える。
const maxDAPMessageSize = rpc.DefaultMaxMessageSize

// reverseRequestEvent は、対応しない逆向きの request を受けたことを onEvent へ渡すときの名前の前置きである
// （後ろに request の command を付ける。DAP の event の名前と重ならない）。body は、その request の引数である。
const reverseRequestEvent = "$reverseRequest:"

// errDAPClosed は、デバッグアダプターとの接続が終わっていることを表す。
var errDAPClosed = errors.New("dap: connection closed")

// dapMessage は、受け取った 1 件のメッセージである。type によって使う項目が違う。
type dapMessage struct {
	Seq  int    `json:"seq"`
	Type string `json:"type"`
	// Command は、request と response のコマンドの名前である。
	Command string `json:"command,omitempty"`
	// Arguments は、request の引数である。
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// RequestSeq は、response が答える request の seq である。
	RequestSeq int `json:"request_seq,omitempty"`
	// Success は、response が成功かどうかである。
	Success bool `json:"success,omitempty"`
	// Message は、失敗の response の短い説明（"cancelled"、"notStopped" など）である。
	Message string `json:"message,omitempty"`
	// Body は、response と event の本体である。
	Body json.RawMessage `json:"body,omitempty"`
	// Event は、event の種類である。
	Event string `json:"event,omitempty"`
}

// dapRequest は、送る request である。
type dapRequest struct {
	Seq       int    `json:"seq"`
	Type      string `json:"type"`
	Command   string `json:"command"`
	Arguments any    `json:"arguments,omitempty"`
}

// dapResponse は、デバッグアダプターからの逆向きの request（runInTerminal など）へ送る response である。
type dapResponse struct {
	Seq        int    `json:"seq"`
	Type       string `json:"type"`
	RequestSeq int    `json:"request_seq"`
	Success    bool   `json:"success"`
	Command    string `json:"command"`
	Message    string `json:"message,omitempty"`
}

// dapError は、デバッグアダプターが success が false の response を返したことを表す。
type dapError struct {
	Command string
	// Message は、response の message である。
	Message string
	// Format は、body.error.format（仕様の ErrorResponse の Message）である。無ければ空。
	Format string
}

func (e *dapError) Error() string {
	text := e.Format
	if text == "" {
		text = e.Message
	}
	if text == "" {
		text = "request failed"
	}
	return fmt.Sprintf("dap: %s: %s", e.Command, text)
}

// dapConn は、デバッグアダプターとの 1 本の接続である。
//
// 読み取りは、newDAPConn が起動する 1 つの goroutine が行う。
// event は受け取った順に onEvent へ渡す。onEvent は読み取りの goroutine から呼ばれるので、
// その中で call の応答を待ってはならない（応答を読む goroutine が止まる）。
// 逆向きの request（runInTerminal、startDebugging）には、最初の範囲では対応しないので、
// 読み取りの goroutine が success を false にした response を返す。
//
// call は複数の goroutine から並行に呼んでよい。書き込みは mu で直列にする。
type dapConn struct {
	w       io.Writer
	closer  io.Closer
	onEvent func(event string, body json.RawMessage)

	mu      sync.Mutex
	seq     int
	pending map[int]chan *dapMessage
	err     error
	done    chan struct{}
}

// newDAPConn は、r から読み、w へ書く接続を作り、読み取りを始める。
// closer は、close で閉じるもの（子プロセスの標準入力やソケット）で、nil でもよい。
func newDAPConn(r io.Reader, w io.Writer, closer io.Closer, onEvent func(string, json.RawMessage)) *dapConn {
	c := &dapConn{
		w:       w,
		closer:  closer,
		onEvent: onEvent,
		pending: make(map[int]chan *dapMessage),
		done:    make(chan struct{}),
	}
	go c.readLoop(bufio.NewReader(r))
	return c
}

// done を閉じた後は err を書き換えない。
func (c *dapConn) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return
	default:
	}
	c.err = err
	close(c.done)
	for seq, ch := range c.pending {
		close(ch)
		delete(c.pending, seq)
	}
}

func (c *dapConn) readLoop(r *bufio.Reader) {
	for {
		body, err := rpc.ReadMessage(r, maxDAPMessageSize)
		if err != nil {
			c.finish(err)
			return
		}
		var m dapMessage
		if err := json.Unmarshal(body, &m); err != nil {
			c.finish(fmt.Errorf("dap: invalid message: %w", err))
			return
		}
		switch m.Type {
		case "response":
			c.mu.Lock()
			ch, ok := c.pending[m.RequestSeq]
			delete(c.pending, m.RequestSeq)
			c.mu.Unlock()
			if ok {
				ch <- &m
			}
		case "event":
			if c.onEvent != nil {
				c.onEvent(m.Event, m.Body)
			}
		case "request":
			// 逆向きの request には対応しない（runInTerminal は作らない。FR-288。startDebugging は子のセッションの issue で作る）。
			// 何を求められたかをログに残せるよう、reverseRequestEvent の名前で onEvent へも渡す。
			if c.onEvent != nil {
				c.onEvent(reverseRequestEvent+m.Command, m.Arguments)
			}
			_ = c.write(func(seq int) any {
				return dapResponse{
					Seq: seq, Type: "response", RequestSeq: m.Seq, Success: false,
					Command: m.Command, Message: "not supported by vi-imitation-ext-dap",
				}
			})
		default:
			// 知らない種類のメッセージは捨てる。
		}
	}
}

// write は、次の seq を振ってメッセージを書く。
func (c *dapConn) write(build func(seq int) any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(build)
}

func (c *dapConn) writeLocked(build func(seq int) any) error {
	select {
	case <-c.done:
		return errDAPClosed
	default:
	}
	c.seq++
	body, err := json.Marshal(build(c.seq))
	if err != nil {
		return err
	}
	return rpc.WriteMessage(c.w, body)
}

// call は、request を送り、response を待つ。
// 成功なら body を result へ読み込む（result が nil なら読まない）。
// 失敗の response なら *dapError を、接続が終わったら errDAPClosed を包んだ誤りを、ctx が終わったら ctx.Err() を返す。
func (c *dapConn) call(ctx context.Context, command string, args, result any) error {
	ch := make(chan *dapMessage, 1)
	seq := 0
	c.mu.Lock()
	err := c.writeLocked(func(s int) any {
		seq = s
		c.pending[s] = ch
		return dapRequest{Seq: s, Type: "request", Command: command, Arguments: args}
	})
	if err != nil {
		if seq != 0 {
			delete(c.pending, seq)
		}
		c.mu.Unlock()
		return fmt.Errorf("dap: send %s: %w", command, err)
	}
	c.mu.Unlock()

	select {
	case m, ok := <-ch:
		if !ok {
			return fmt.Errorf("dap: %s: %w", command, errDAPClosed)
		}
		if !m.Success {
			e := &dapError{Command: command, Message: m.Message}
			var body struct {
				Error *struct {
					Format string `json:"format"`
				} `json:"error"`
			}
			if json.Unmarshal(m.Body, &body) == nil && body.Error != nil {
				e.Format = body.Error.Format
			}
			return e
		}
		if result == nil || len(m.Body) == 0 {
			return nil
		}
		if err := json.Unmarshal(m.Body, result); err != nil {
			return fmt.Errorf("dap: %s: invalid body: %w", command, err)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, seq)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// close は、接続を閉じる。読み取りの goroutine は、相手の終了か読み取りの誤りで終わる。
// 書き込みが相手の読み取りを待って mu を持ったまま止まっていることがあるので、先に closer を閉じて書き込みを終わらせる。
func (c *dapConn) close() {
	if c.closer != nil {
		_ = c.closer.Close()
	}
	c.finish(errDAPClosed)
}

// doneCh は、接続が終わると閉じる。
func (c *dapConn) doneCh() <-chan struct{} { return c.done }
