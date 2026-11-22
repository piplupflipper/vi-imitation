package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// parserState は、利用者の設定に並べたパーサ（FR-142）の用意の状態である。
type parserState int

const (
	// parserIdle は、まだ用意を始めていないことを表す。そのファイルの種類のバッファを初めて開いたときに用意を始める。
	parserIdle parserState = iota
	// parserPreparing は、用意の goroutine が用意している（取得、ビルド、読み込み）か、その順番を待っていることを表す。
	parserPreparing
	// parserReady は、読み込んで使えることを表す。
	parserReady
	// parserFailed は、用意に失敗したことを表す。拡張機能が終わるまで、このパーサは使わない。
	parserFailed
)

// userParser は、利用者の設定に並べた 1 つのパーサである。
type userParser struct {
	spec parserSpec
	// state と lang は highlighter.mu で守る。lang は parserReady のときだけ nil でない。
	state parserState
	lang  *language
}

// waitingBuffer は、パーサの用意を待っているバッファである。
type waitingBuffer struct {
	parser *userParser
	// buf は、最後に受け取った全文の複製である。用意ができたら、ここから全文を解析する。
	buf *server.Buffer
}

// configure は、initialize で受け取った設定から、利用者の設定に並べたパーサの表を作る。ループの上で呼ぶ。
// 設定の誤りは problems に置き、最初の buffer/open で知らせる（initialize の応答より前に知らせを送らないため）。
// 誤りがあっても initialize は失敗にしない（同梱の言語は使える）。
//
// req: FR-142
func (h *highlighter) configure(raw json.RawMessage) {
	s, problems := parseSettings(raw)
	h.problems = problems
	h.sticky.configure(s.context)
	h.prep = &preparer{
		env:      h.env,
		compiler: s.compiler,
		listed:   s.listed,
		progress: h.progress,
	}
	clear(h.user)
	for _, spec := range s.parsers {
		p := &userParser{spec: spec}
		for _, ft := range spec.fileTypes {
			h.user[ft] = p
		}
	}
}

func (h *highlighter) signalPrepare() {
	select {
	case h.prepWake <- struct{}{}:
	default:
	}
}

// prepareLoop は、用意の goroutine の本体である。ctx が終わるまで、積まれたパーサを 1 つずつ用意する。
// 取得とビルドは数秒から数分かかるので、ループ（メッセージを処理する goroutine）の上では行わない（dev-cycle 2.2。ping の応答を遅らせない）。
// 解析の goroutine とも分けるので、用意の間も、ほかの言語のバッファの解析と色付けは続く。
//
// req: FR-142
func (h *highlighter) prepareLoop(ctx context.Context) {
	defer close(h.prepDone)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.prepWake:
		}
		for {
			h.mu.Lock()
			if len(h.prepQueue) == 0 {
				h.mu.Unlock()
				break
			}
			p := h.prepQueue[0]
			h.prepQueue = h.prepQueue[1:]
			prep := h.prep
			h.mu.Unlock()
			lang, err := prep.prepare(ctx, p.spec)
			if ctx.Err() != nil {
				if lang != nil {
					lang.close()
				}
				return
			}
			if err != nil {
				h.log.Printf("parser %s: %v", p.spec.name, err)
				h.message(ext.MessageError, fmt.Sprintf("tree-sitter: パーサ %s を使えない: %s", p.spec.name, firstLine(err.Error())))
			}
			h.parserDone(p, lang, err)
		}
	}
}

// parserDone は、パーサ p の用意の結果を記録し、用意を待っていたバッファを解析させる。用意の goroutine の上で呼ぶ。
// 失敗したら、待っていたバッファは、同梱の言語にそのファイルの種類があればそれで解析させ、無ければ色を付けない。
//
// req: FR-142
func (h *highlighter) parserDone(p *userParser, lang *language, err error) {
	h.mu.Lock()
	if err != nil {
		p.state = parserFailed
	} else {
		p.state = parserReady
		p.lang = lang
		h.userLangs = append(h.userLangs, lang)
	}
	for id, w := range h.waiting {
		if w.parser != p {
			continue
		}
		delete(h.waiting, id)
		l := lang
		if err != nil {
			l = h.langs[w.buf.FileType()]
		}
		if l != nil {
			h.pending[id] = &job{reset: w.buf, lang: l, version: w.buf.Version()}
		}
	}
	h.mu.Unlock()
	h.signal()
}

// firstLine は、s の最初の行を返す。メッセージの行は 1 行なので、C コンパイラの出力などの続きはログにだけ書く。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimRight(s[:i], ":") + "（続きはログ）"
	}
	return s
}
