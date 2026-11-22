package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// maxDecorationSetBytes は、1 回の decoration/set の本文（JSON）の大きさの上限である。
// 本体は一覧を受け取るたびに、イベントループの上で描画用の表を全体から作り直す（internal/editor の rebuildDecorations）。
// 範囲を整数の列で送る形（基本設計 6.1）では 1 つの範囲が 10 バイトほどで、上限の 10 MiB は約 10 万行の JSON（約 80 万の範囲）に当たり、
// そのときの本体のイベントループの停止は 83〜122 ms だった（PR #73 のレビューの実測。40 万行では 445〜459 ms）。
// 値は仮である（基本設計 10 章）。
const maxDecorationSetBytes = 10 << 20

// tooLargeMessage は、一覧が maxDecorationSetBytes を超えたバッファについて、利用者へ 1 回だけ知らせる文言である。
const tooLargeMessage = "tree-sitter: 色を付ける範囲の一覧が大きすぎるので、このバッファには色を付けない"

// job は、まだ解析していない、1 つのバッファへの指示である。
// ループ（Server がメッセージを処理する goroutine）が積み、解析の goroutine が取り出す。
// 取り出す前に同じバッファの指示が続けて届いたら、1 つの job にまとめる。
type job struct {
	// reset は、全文から解析し直すための複製である。nil なら、解析の goroutine が持つ構文木に edits を適用する。
	reset *server.Buffer
	// lang は、reset から解析するときの言語である。
	lang *language
	// edits は、reset（nil なら前回の解析）の後に届いた buffer/change の編集を、届いた順に並べたものである。
	edits []ext.Edit
	// latest は、最後に届いた buffer/change の後の複製である。edits を適用できなかったときに全文から解析し直すのに使う。
	latest *server.Buffer
	// version は、edits をすべて適用した後のバージョン番号である。送る一覧にはこの番号を付ける。
	version ext.Version
	// drop は、構文木を捨てることを表す。clear が true なら、空の一覧を送って色を消す。
	drop, clear bool
}

// highlighter は、バッファごとの構文木を持ち、buffer/open と buffer/change のたびに色を付ける範囲の一覧を本体へ送る。
//
// 解析と一覧の計算は時間がかかる（1 MB の JSON で 1 回 0.3 秒ほど）ので、ループの上では行わない（server.Server の doc の「処理の順序」）。
// フックは、Buffer.Clone した複製と編集を job に積んで速やかに戻り、1 つの解析の goroutine（work）が job を取り出して解析する。
// 解析の間に同じバッファの差分が溜まれば、次の 1 回でまとめて適用して 1 回だけ解析し直し、途中のバージョンの一覧は送らない。
// 本体は、バージョンの古い一覧でも描画できる（基本設計 6.1）。
type highlighter struct {
	// langs は、ファイルの種類から言語への表である。同じ言語を使う種類（"json" と "jsonc"）は、同じ *language を指す。作った後は変えない
	langs map[string]*language
	// all は、languages の順に並べた言語の一覧で、言語ごとに 1 つずつ持つ。解放と、言語ごとに 1 回だけ行う検査に使う。作った後は変えない
	all []*language
	log *log.Logger
	// maxBytes は、decoration/set の本文の大きさの上限である。maxDecorationSetBytes を入れる。テストは小さくしてよい。
	maxBytes int

	// active は、ループが、解析の対象にしているバッファとその言語を覚える表である。ループだけが触る。
	// 利用者の設定のパーサの用意を待っているバッファ（waiting にあるもの）の言語は nil である。
	active map[ext.BufferID]*language

	// env は、利用者の設定に並べたパーサ（FR-142）の置き場所と子プロセスの設定である。newHighlighter が既定の値を入れ、テストは server の前に変える。
	env parserEnv
	// user は、ファイルの種類から、利用者の設定に並べたパーサへの対応である。
	// initialize の設定から作り（configure）、その後は変えない。ループだけが読む。
	user map[string]*userParser
	// problems は、設定の誤りである。最初の buffer/open で知らせる。ループだけが触る。
	problems []string
	// prep は、パーサを用意する道具である。configure が作り、用意の goroutine（prepareLoop）が使う。
	prep *preparer

	mu      sync.Mutex
	pending map[ext.BufferID]*job // mu で守る
	wake    chan struct{}         // job を積んだことを解析の goroutine に知らせる。容量 1
	stop    chan struct{}
	done    chan struct{} // 解析の goroutine が終わったら閉じる。start の前は nil
	// waiting は、利用者の設定のパーサの用意を待っているバッファと、その最新の複製である。mu で守る。
	waiting map[ext.BufferID]*waitingBuffer
	// prepQueue は、用意を待つパーサの列である。mu で守る。prepWake は、積んだことを用意の goroutine に知らせる。容量 1
	prepQueue []*userParser
	prepWake  chan struct{}
	// userLangs は、読み込んだ利用者の設定のパーサの言語である。close で解放する。mu で守る。
	userLangs []*language
	// prepCancel は、用意の goroutine の取得とビルドを取り消す。prepDone は、その goroutine が終わったら閉じる。start の前は nil
	prepCancel context.CancelFunc
	prepDone   chan struct{}

	// trees は、解析の goroutine が解析した構文木の複製を、テキストオブジェクトの問い合わせ（objects）へ渡す。
	trees   *treeStore
	objects *textObjects
	// sticky は、見出しの帯（sticky.go）を作って送る。trees の構文木を使う。
	sticky *sticky

	// 以下は解析の goroutine だけが触る。
	docs map[ext.BufferID]*document
	// warned は、一覧が大きすぎることを知らせたバッファの集合である。
	warned map[ext.BufferID]bool
	// send は decoration/set を、notify は message/show を、report は progress/report を送る関数である。
	// server が Server のメソッドを入れる。
	send   func(ext.DecorationSetParams) error
	notify func(ext.MessageLevel, string) error
	report func(ext.ProgressReportParams) error
}

// newHighlighter は、languages のすべての言語のパーサーとクエリを 1 つずつ用意し、言語の fileTypes のどの種類からも引けるようにする。
// クエリを解釈できないなど、用意できない言語があるか、1 つの種類が表に 2 回以上（別の言語にでも、同じ言語の中にでも）書かれていれば誤りを返す。
func newHighlighter() (*highlighter, error) {
	h := &highlighter{
		langs:    map[string]*language{},
		log:      log.New(os.Stderr, "vi-imitation-ext-treesitter: ", log.LstdFlags),
		maxBytes: maxDecorationSetBytes,
		active:   map[ext.BufferID]*language{},
		pending:  map[ext.BufferID]*job{},
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		docs:     map[ext.BufferID]*document{},
		warned:   map[ext.BufferID]bool{},
		env:      defaultParserEnv(),
		user:     map[string]*userParser{},
		waiting:  map[ext.BufferID]*waitingBuffer{},
		prepWake: make(chan struct{}, 1),
	}
	for _, spec := range languages {
		l, err := newLanguage(spec)
		if err != nil {
			h.close()
			return nil, err
		}
		h.all = append(h.all, l)
		for _, ft := range spec.fileTypes {
			if _, dup := h.langs[ft]; dup {
				h.close()
				return nil, fmt.Errorf("file type %q is listed more than once in languages", ft)
			}
			h.langs[ft] = l
		}
	}
	h.trees = newTreeStore()
	h.objects = newTextObjects(h.langs, h.trees, func(ft string) *language {
		h.mu.Lock()
		defer h.mu.Unlock()
		l, _ := h.languageForLocked(ft)
		return l
	})
	h.sticky = newSticky(h.trees, h.log)
	return h, nil
}

// close は、解析の goroutine を止めて終わりを待ち、すべての構文木とパーサーとクエリの C のメモリを解放する。
// Serve が戻った後に呼ぶ。
func (h *highlighter) close() {
	if h.done != nil {
		close(h.stop)
		<-h.done
		h.done = nil
	}
	if h.prepDone != nil {
		h.prepCancel()
		<-h.prepDone
		h.prepDone = nil
	}
	if h.objects != nil {
		h.objects.close()
	}
	// 帯を作る goroutine は trees の構文木を使うので、trees より先に止める。
	if h.sticky != nil {
		h.sticky.close()
	}
	if h.trees != nil {
		h.trees.close()
	}
	for _, d := range h.docs {
		d.close()
	}
	clear(h.docs)
	// langs は同じ言語を複数の種類から指すので、言語ごとに 1 つずつ持つ all から解放する。
	for _, l := range h.all {
		l.close()
	}
	h.all = nil
	clear(h.langs)
	h.mu.Lock()
	for _, l := range h.userLangs {
		l.close()
	}
	h.userLangs = nil
	h.mu.Unlock()
}

// server は、フックを登録した Server を返し、解析の goroutine を動かし始める。
func (h *highlighter) server() *server.Server {
	s := server.New()
	s.ErrorLog = h.log
	s.OnInitialize(func(_ context.Context, e *server.InitializeEvent) error {
		h.configure(e.Settings)
		return nil
	})
	s.OnBufferOpen(func(_ context.Context, e *server.BufferOpenEvent) { h.open(e.Buffer) })
	s.OnBufferChange(func(_ context.Context, e *server.BufferChangeEvent) { h.change(e) })
	s.OnBufferClose(func(_ context.Context, e *server.BufferCloseEvent) { h.closeBuffer(e.Buffer) })
	h.objects.register(s)
	h.sticky.register(s)
	h.send = s.SetDecorations
	h.notify = s.ShowMessage
	h.report = s.ReportProgress
	h.done = make(chan struct{})
	go h.work()
	ctx, cancel := context.WithCancel(context.Background())
	h.prepCancel = cancel
	h.prepDone = make(chan struct{})
	go h.prepareLoop(ctx)
	return s
}

// open は、buffer/open で受け取った全文を、解析の goroutine で全文から解析させる。ループの上で呼ぶ。
// 文法を持たない種類のファイルは解析しない。
// 送り直された全文（buffer/resync の応答）でファイルの種類が文法を持たないものに変わっていたら、構文木を捨て、空の一覧を送って色を消させる。
//
// 利用者の設定のパーサ（FR-142）がそのファイルの種類を持てば、同梱の言語より先にそれを使う（languageFor）。
// パーサがまだ用意できていなければ、用意の goroutine に用意を頼み、バッファの複製を waiting に置いて戻る。
// 用意ができたら、用意の goroutine がその複製から解析させる（parserDone）。用意の間も編集は続けられる。
//
// req: FR-35, FR-142
func (h *highlighter) open(b *server.Buffer) {
	h.reportProblems()
	id := b.ID()
	_, was := h.active[id]
	h.mu.Lock()
	delete(h.waiting, id)
	lang, p := h.languageForLocked(b.FileType())
	if p != nil {
		// 前の種類の言語で色を付けていたら、用意ができるまでの間は色を消す。
		// 用意の goroutine が waiting から積む job に上書きされないように、waiting に置くのと同じ mu の中で積む。
		if was {
			h.pending[id] = &job{drop: true, clear: true, version: b.Version()}
		}
		h.waiting[id] = &waitingBuffer{parser: p, buf: b.Clone()}
		if p.state == parserIdle {
			p.state = parserPreparing
			h.prepQueue = append(h.prepQueue, p)
		}
	}
	h.mu.Unlock()
	switch {
	case p != nil:
		h.active[id] = nil
		h.signal()
		h.signalPrepare()
	case lang == nil:
		if was {
			delete(h.active, id)
			h.put(id, &job{drop: true, clear: true, version: b.Version()})
		}
	default:
		h.active[id] = lang
		h.put(id, &job{reset: b.Clone(), lang: lang, version: b.Version()})
	}
}

// languageForLocked は、ファイルの種類 ft に使う言語を返す。mu を取って呼ぶ。
//
// 利用者の設定のパーサが ft を持てば、用意できていればその言語を、用意を待つなら nil とそのパーサを返す。
// 用意に失敗していれば、同梱の言語にその種類があればそれを使う。どちらも無ければ nil, nil を返す。
//
// req: FR-142
func (h *highlighter) languageForLocked(ft string) (*language, *userParser) {
	if p, ok := h.user[ft]; ok {
		switch p.state {
		case parserReady:
			return p.lang, nil
		case parserIdle, parserPreparing:
			return nil, p
		case parserFailed:
		}
	}
	return h.langs[ft], nil
}

// reportProblems は、設定の誤りを、最初の 1 回だけメッセージの行に出す。ループの上で呼ぶ。
func (h *highlighter) reportProblems() {
	if len(h.problems) == 0 {
		return
	}
	for _, p := range h.problems {
		h.log.Printf("settings: %s", p)
	}
	text := "tree-sitter: " + h.problems[0]
	if n := len(h.problems) - 1; n > 0 {
		text += fmt.Sprintf("（ほか %d 件）", n)
	}
	h.problems = nil
	h.message(ext.MessageWarning, text)
}

// message は、message/show を送る。送れなければログに書く。
func (h *highlighter) message(level ext.MessageLevel, text string) {
	if err := h.notify(level, text); err != nil {
		h.log.Printf("message/show: %v", err)
	}
}

// progress は、progress/report を送る。送れなければログに書く。
func (h *highlighter) progress(p ext.ProgressReportParams) {
	if err := h.report(p); err != nil {
		h.log.Printf("progress/report: %v", err)
	}
}

// change は、buffer/change の編集を、まだ取り出されていない同じバッファの job に足す。ループの上で呼ぶ。
// パーサの用意を待っているバッファなら、待っている複製を最新のものに替える。
//
// req: FR-35
func (h *highlighter) change(e *server.BufferChangeEvent) {
	if _, ok := h.active[e.Buffer.ID()]; !ok {
		return
	}
	latest := e.Buffer.Clone()
	h.mu.Lock()
	if w, ok := h.waiting[e.Buffer.ID()]; ok {
		w.buf = latest
		h.mu.Unlock()
		return
	}
	j, ok := h.pending[e.Buffer.ID()]
	if !ok {
		j = &job{}
		h.pending[e.Buffer.ID()] = j
	}
	j.edits = append(j.edits, e.Edits...)
	j.latest = latest
	j.version = e.ToVersion
	h.mu.Unlock()
	h.signal()
}

// closeBuffer は、buffer/close を受け取ったバッファの構文木を捨てさせる。ループの上で呼ぶ。
func (h *highlighter) closeBuffer(id ext.BufferID) {
	if _, ok := h.active[id]; !ok {
		return
	}
	delete(h.active, id)
	h.mu.Lock()
	delete(h.waiting, id)
	h.mu.Unlock()
	h.put(id, &job{drop: true})
}

// put は、バッファの job を j で置き換える。まだ取り出されていない前の job は、j が全文から解析し直すか捨てるので要らない。
func (h *highlighter) put(id ext.BufferID, j *job) {
	h.mu.Lock()
	h.pending[id] = j
	h.mu.Unlock()
	h.signal()
}

func (h *highlighter) signal() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// work は、解析の goroutine の本体である。stop が閉じるまで、積まれた job を取り出して処理する。
func (h *highlighter) work() {
	defer close(h.done)
	for {
		select {
		case <-h.stop:
			return
		case <-h.wake:
		}
		h.mu.Lock()
		jobs := h.pending
		h.pending = map[ext.BufferID]*job{}
		h.mu.Unlock()
		for id, j := range jobs {
			h.process(id, j)
		}
	}
}

// process は、1 つのバッファの job を処理し、一覧を送る。
// 処理の間に同じバッファの次の job が積まれていれば、この結果は古いので送らない。
func (h *highlighter) process(id ext.BufferID, j *job) {
	if j.drop {
		if d, ok := h.docs[id]; ok {
			d.close()
			delete(h.docs, id)
		}
		delete(h.warned, id)
		h.trees.drop(id)
		h.sticky.parsed(id)
		if j.clear {
			h.publish(id, j.version, []ext.Decoration{})
		}
		return
	}
	d, ok := h.docs[id]
	switch {
	case j.reset != nil:
		if ok && d.lang == j.lang {
			d.reset(j.reset.Text())
		} else {
			if ok {
				d.close()
			}
			d = newDocument(j.lang, j.reset.Text())
			h.docs[id] = d
		}
	case !ok:
		// 全文を受け取る前の差分は無い（open が先に job を積む）。念のため捨てる。
		return
	}
	if len(j.edits) > 0 {
		if err := d.applyEdits(j.edits); err != nil {
			h.log.Printf("buffer %d: %v; reparsing the whole text", id, err)
			d.reset(j.latest.Text())
		}
	}
	h.trees.put(id, j.version, d)
	h.sticky.parsed(id)
	decs := d.decorations()
	if h.superseded(id) {
		return
	}
	if size := (ext.DecorationSetParams{Buffer: id, Version: j.version, Decorations: decs}).JSONSize(); size > h.maxBytes {
		h.log.Printf("buffer %d: decoration/set would be %d bytes (limit %d); sending an empty list", id, size, h.maxBytes)
		decs = []ext.Decoration{}
		if !h.warned[id] {
			h.warned[id] = true
			if err := h.notify(ext.MessageWarning, tooLargeMessage); err != nil {
				h.log.Printf("buffer %d: message/show: %v", id, err)
			}
		}
	}
	h.publish(id, j.version, decs)
}

// superseded は、バッファの次の job が積まれているかどうかを返す。
func (h *highlighter) superseded(id ext.BufferID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.pending[id]
	return ok
}

// publish は、decoration/set を送る。送れなければログに書く。
func (h *highlighter) publish(id ext.BufferID, version ext.Version, decs []ext.Decoration) {
	if err := h.send(ext.DecorationSetParams{Buffer: id, Version: version, Decorations: decs}); err != nil {
		h.log.Printf("buffer %d: decoration/set: %v", id, err)
	}
}
