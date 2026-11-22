package editor

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/command/ex"
	"github.com/piplupflipper/vi-imitation/internal/diff"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 見比べ（diff。基本設計 6.8。FR-99〜FR-101）。
//
// 見比べは、2 つの編集の client のタブ（2 つのバッファ）の組（diffSession）で行う。diff 専用のタブは作らない。
// 1 つのバッファは高々 1 つの組に入る。同じ client の 2 つのタブは同時に画面に出ないので、組の 2 つのバッファの持ち主は別の client にする。
//
// # 始め方
//
// :diffsplit {file} は、端末多重化ソフトの層（Config.SplitPane）でカーソルのあるタブの client のペインを分割し、
// そこで起動した新しい編集の client が file のタブを開いて繋いだら（attach）、2 つのタブを見比べ始める（diffPending）。
// file が既に別の client のタブなら、ペインを分割せずにそのタブと見比べる。
// :diffthis は、Vim と同じく 2 つのタブで打つと、その 2 つを見比べ始める。
// :diffoff は、カーソルのあるタブの組を終える。どちらかのバッファを閉じても終わる。
// 拡張機能は、diff/start で 2 つのバッファの見比べを始めさせる（diffstart.go）。
// 端末多重化ソフトの外（FR-60）では編集の client が 1 つなので、:diffsplit は誤りにする（基本設計 10 章の仮）。
//
// # 差分の計算
//
// 差分（internal/diff）は、どちらかのバッファが変わったら、写したテキスト（diff.FromBuffer）で、イベントループの外の goroutine で計算し直す
// （基本設計 2.1。描く前に refreshDiffs が始める）。結果が届くまでは前の結果で描く。
// ]c、[c、do、dp は、古い結果で行と塊を取り違えないよう、バッファが計算の後に変わっていれば、その場で計算し直してから行う。
//
// # スクロールとカーソルのそろえ（FR-100）
//
// 描いた後に、操作中の client の側（無ければ、最初の行が変わった側）に合わせて、もう片側の表示の最初の行を、同じ画面の行
// （diff.Layout の行。詰め物を含めて数える）になるようにそろえる。Vim の diff の 'scrollbind' に当たる。
// カーソルの行が変わったら、もう片側のカーソルを対応する行（diff.Result.MapLine）へ動かす（Vim の diff の 'cursorbind' に当たる）。
// もう片側の client が入力の途中（viewIdle が false）なら、そろえずに待つ（そのキーが見ていない位置のコマンドにならないため）。

// diffState は、見比べの全体の状態である。
type diffState struct {
	// sessions は、見比べている組である。
	sessions []*diffSession
	// this は、:diffthis を打って、相手を待っているタブのバッファである。無ければ nil。
	this *bufferState
	// pending は、:diffsplit で開いたペインの client が繋ぐのを待っている見比べである。無ければ nil。
	pending *diffPending
	// seq は、最後に分割したペインの client に渡した印（diffPending.token）の番号である。
	seq uint64
	// done は、goroutine で計算した差分を、イベントループへ送る。
	done chan diffBuilt
}

// diffPendingTimeout は、:diffsplit で開いたペインの client が繋ぐのを待つ上限である（仮）。
// 過ぎたら待ちを捨て（diff/start のバッファは閉じる）、後から繋いだ client とは見比べを始めない。
const diffPendingTimeout = 30 * time.Second

// errDiffPending は、分割したペインの client を待っている間に、次の分割を頼まれたことを表す。
var errDiffPending = errors.New("前の見比べのペインの client が繋ぐのを待っています（30 秒まで）")

// diffPending は、:diffsplit と diff/start の、分割したペインの相手の client を待っている見比べである。
type diffPending struct {
	// from は、:diffsplit を打ったタブのバッファ（diff/start ではタブの側のバッファ）である。
	from *bufferState
	// path は、新しい client が開くファイルの絶対パスである。buf を使うときは空である。
	path string
	// buf は、拡張機能の diff/start で、新しい client のタブにするバッファ（どのタブでもないもの）である。:diffsplit では nil。
	// 新しい client はファイルを渡さずに起動し、繋いだときに（diffAdopt）このバッファをタブにする（diffstart.go）。
	buf *bufferState
	// swap は、見比べの左の側（diff.A）を buf にすることを表す（diff/start の A がタブでなかった）。
	swap bool
	// ext は、diff/start を送った拡張機能の ID である。:diffsplit では空。待ちを捨てるときは、この拡張機能が作った buf だけを閉じる。
	ext string
	// token は、新しい client を見分ける印である。server は、新しい client を環境変数 EnvSplit にこの値を入れて起動し、
	// client は Hello の Env でそれを知らせる（ClientInfo.Env。attach が取り出して外す）。
	token string
}

// diffSession は、見比べている 2 つのバッファの組である。bs[0] が diff.A の側、bs[1] が diff.B の側である。
type diffSession struct {
	bs [2]*bufferState
	// res、layout、view は、最後に受け取った計算の結果と、両側の画面の行の並び、描画に渡す色と詰め物である。計算の前は nil。
	res    *diff.Result
	layout [2]diff.Layout
	view   [2]*ui.Diff
	// buf と ver は、res を計算したときの両側のバッファとバージョン番号である。バッファの内容を読み直すと buf が変わる（replaceBuffer）。
	buf [2]*buffer.Buffer
	ver [2]uint64
	// gen は、計算を始めるたびに進める番号で、computing は計算中かである。古い計算の結果は捨てる。
	gen       uint64
	computing bool
	// lastRow と lastCur は、最後にそろえたときの両側の表示の最初の画面の行と、カーソルの行である。-1 はまだそろえていない。
	lastRow [2]int
	lastCur [2]int
}

// diffBuilt は、goroutine で計算した差分の結果である。
type diffBuilt struct {
	s      *diffSession
	gen    uint64
	res    *diff.Result
	layout [2]diff.Layout
	view   [2]*ui.Diff
	buf    [2]*buffer.Buffer
	ver    [2]uint64
}

// newDiffState は、見比べの状態を作る。
func newDiffState() *diffState {
	return &diffState{done: make(chan diffBuilt)}
}

// diffOf は、bs が入っている見比べの組と、その中の側を返す。入っていなければ nil を返す。
func (e *Editor) diffOf(bs *bufferState) (*diffSession, diff.Side) {
	for _, s := range e.diff.sessions {
		for i, o := range s.bs {
			if o == bs {
				return s, diff.Side(i)
			}
		}
	}
	return nil, diff.A
}

// stale は、計算の後に、どちらかのバッファが変わったかを返す。
func (s *diffSession) stale() bool {
	for i, bs := range s.bs {
		if s.res == nil || bs.buf != s.buf[i] || bs.buf.Version() != s.ver[i] {
			return true
		}
	}
	return false
}

// snapshot は、両側のバッファを写したテキストと、そのときのバッファとバージョン番号を返す。イベントループで呼ぶ。
func (s *diffSession) snapshot() (texts [2]diff.Text, bufs [2]*buffer.Buffer, vers [2]uint64) {
	for i, bs := range s.bs {
		texts[i], bufs[i], vers[i] = diff.FromBuffer(bs.buf), bs.buf, bs.buf.Version()
	}
	return texts, bufs, vers
}

// computeDiff は、2 つのテキストの差分と、両側の画面の行の並びと、描画に渡す色と詰め物を作る。イベントループの外で呼んでよい。
// 変えた行の中の違う文字の範囲（diff.Inline）も、ここで計算する。
func computeDiff(texts [2]diff.Text) (*diff.Result, [2]diff.Layout, [2]*ui.Diff) {
	res := diff.Compute(texts[0], texts[1])
	var layouts [2]diff.Layout
	var views [2]*ui.Diff
	for i := range layouts {
		layouts[i] = res.Layout(diff.Side(i))
		views[i] = &ui.Diff{Lines: map[int]ui.DiffLine{}, Fill: layouts[i].Fills()}
	}
	for i := range layouts {
		for _, m := range layouts[i].Marks {
			style := ui.StyleDiffChange
			if m.Kind == diff.LineOnlyHere {
				style = ui.StyleDiffAdd
			}
			views[i].Lines[m.Line] = ui.DiffLine{Style: style}
		}
	}
	// 変えた行の組ごとに 1 回だけ比べ、両側の行に入れる。
	for _, m := range layouts[0].Marks {
		if m.Kind != diff.LineChanged || m.Other < 0 {
			continue
		}
		sa, sb := diff.Inline(texts[0].Lines[m.Line], texts[1].Lines[m.Other])
		la, lb := views[0].Lines[m.Line], views[1].Lines[m.Other]
		la.Text, lb.Text = byteRanges(sa), byteRanges(sb)
		views[0].Lines[m.Line], views[1].Lines[m.Other] = la, lb
	}
	return res, layouts, views
}

// byteRanges は、diff.Span の列を ui.ByteRange の列にする。長さ 0 の範囲（もう片側にだけある文字の位置）は除く。
func byteRanges(spans []diff.Span) []ui.ByteRange {
	var out []ui.ByteRange
	for _, s := range spans {
		if s.Len() > 0 {
			out = append(out, ui.ByteRange{Start: s.Start, End: s.End})
		}
	}
	return out
}

// setResult は、計算の結果を組に入れる。
func (s *diffSession) setResult(r diffBuilt) {
	s.res, s.layout, s.view, s.buf, s.ver = r.res, r.layout, r.view, r.buf, r.ver
}

// refreshDiffs は、計算の後にバッファが変わった組の差分を、goroutine で計算し直し始める。描く前に呼ぶ。
func (e *Editor) refreshDiffs() {
	for _, s := range e.diff.sessions {
		if s.computing || !s.stale() {
			continue
		}
		s.gen++
		s.computing = true
		texts, bufs, vers := s.snapshot()
		gen, done, stop := s.gen, e.diff.done, e.stop
		go func() {
			defer e.recoverPanic()
			res, layouts, views := computeDiff(texts)
			select {
			case done <- diffBuilt{s: s, gen: gen, res: res, layout: layouts, view: views, buf: bufs, ver: vers}:
			case <-stop:
			}
		}()
	}
}

// diffBuilt は、goroutine で計算した差分を受け取る。組が終わっていれば捨てる。
// 計算の後に始めた計算（freshDiff の同期の計算を含む）があれば、この結果は古いので捨てる。
func (e *Editor) diffBuilt(r diffBuilt) {
	s := r.s
	if r.gen == s.gen {
		s.computing = false
	}
	if r.gen != s.gen || !slices.Contains(e.diff.sessions, s) {
		return
	}
	s.setResult(r)
}

// freshDiff は、計算の後にバッファが変わっていれば、その場で計算し直す（]c、[c、do、dp の前と後）。
// 計算中の goroutine の結果は、gen を進めて捨てさせる。
func (e *Editor) freshDiff(s *diffSession) {
	if !s.stale() {
		return
	}
	s.gen++
	s.computing = false
	texts, bufs, vers := s.snapshot()
	res, layouts, views := computeDiff(texts)
	s.setResult(diffBuilt{res: res, layout: layouts, view: views, buf: bufs, ver: vers})
}

// uiDiff は、bs の側の差分の色と詰め物を返す（ui.Frame.Diff）。見比べていないか、まだ計算していなければ nil を返す。
//
// req: FR-99
func (e *Editor) uiDiff(bs *bufferState) *ui.Diff {
	s, side := e.diffOf(bs)
	if s == nil || s.res == nil {
		return nil
	}
	return s.view[side]
}

// startDiff は、a と b のバッファの見比べを始める。どちらかが別の組に入っていれば、その組を終える。
// 差分はその場で計算し、次の描画で両側の最初の行をそろえる。
func (e *Editor) startDiff(a, b *bufferState) {
	// 前の組の、a と b でない側の client には、組が終わったことを知らせる（PR #232 のレビューの推奨 4）。
	const why = "相手のタブが別のタブと見比べ始めました"
	e.endDiff(a, why)
	e.endDiff(b, why)
	if t := e.diff.this; t == a || t == b {
		e.diff.this = nil
	}
	s := &diffSession{bs: [2]*bufferState{a, b}, lastRow: [2]int{-1, -1}, lastCur: [2]int{-1, -1}}
	e.diff.sessions = append(e.diff.sessions, s)
	e.freshDiff(s)
	text := fmt.Sprintf("%s と %s（client %d）を見比べます", e.bufferName(a), e.bufferName(b), b.owner.info.ID)
	// どちらの client も、利用者のキーで始めたとは限らない（diff/start で分割したペインの client が後から繋いだときなど）ので、
	// hit-enter の問いで待っている一覧を上書きしない（showLaterMessage）。キーで始めたなら、キーでメッセージが消えているので出る。
	e.withView(a.owner, func() { e.showLaterMessage(infoMessage(text)) })
	text = fmt.Sprintf("%s と %s（client %d）を見比べます", e.bufferName(b), e.bufferName(a), a.owner.info.ID)
	e.withView(b.owner, func() { e.showLaterMessage(infoMessage(text)) })
}

// endDiff は、bs が入っている組を終える。入っていなければ何もしない。終えたかを返す。
// why が空でなければ、組の相手のタブ（bs でない側）の client のメッセージの行に、終わったことと why を出す。
func (e *Editor) endDiff(bs *bufferState, why string) bool {
	s, side := e.diffOf(bs)
	if s == nil {
		return false
	}
	s.gen++
	e.diff.sessions = slices.DeleteFunc(e.diff.sessions, func(o *diffSession) bool { return o == s })
	if o := s.bs[side.Other()]; why != "" && o.owner != nil && o.owner.attached() {
		text := fmt.Sprintf("%s の見比べが終わりました（%s）", e.bufferName(o), why)
		e.withView(o.owner, func() { e.showLaterMessage(infoMessage(text)) })
	}
	return true
}

// diffClosed は、閉じるバッファ bs の見比べを終え、:diffthis と :diffsplit の待ちから外す（closeBufferState）。
func (e *Editor) diffClosed(bs *bufferState) {
	e.endDiff(bs, "相手のタブが閉じました")
	if e.diff.this == bs {
		e.diff.this = nil
	}
	if p := e.diff.pending; p != nil && (p.from == bs || p.buf == bs) {
		e.dropPending(p)
	}
}

// dropPending は、待ちの見比べ p を捨てる。p が diff/start のもので、待っていたバッファがまだどのタブでもなければ閉じる（FR-64）。
func (e *Editor) dropPending(p *diffPending) {
	if e.diff.pending == p {
		e.diff.pending = nil
	}
	e.discardCreated(p.buf, p.ext)
}

// diffAttached は、繋いだ client c が、:diffsplit か diff/start で分割したペインの client（印 token が待ちのものと同じ）なら、
// そのタブと、分割を頼んだタブの見比べを始める。
func (e *Editor) diffAttached(c *clientView, token string) {
	p := e.diff.pending
	if p == nil || token == "" || token != p.token {
		return
	}
	e.diff.pending = nil
	var other *bufferState
	if p.buf != nil {
		// 拡張機能の diff/start のバッファは、繋ぐ前に diffAdopt がこの client のタブにしている。
		if slices.Contains(c.tabs, p.buf) {
			other = p.buf
		}
	} else {
		for _, bs := range c.tabs {
			if bs.path == p.path {
				other = bs
				break
			}
		}
	}
	if other == nil || p.from.owner == nil || p.from.owner == c || !slices.Contains(e.bufs, p.from) {
		e.discardCreated(p.buf, p.ext)
		return
	}
	if p.swap {
		e.startDiff(other, p.from)
	} else {
		e.startDiff(p.from, other)
	}
}

// diffCommand は、見比べの Ex コマンド（:diffsplit、:diffthis、:diffoff、:diffupdate）を行う。bs はカーソルのあるタブのバッファである。
//
// req: FR-99
func (e *Editor) diffCommand(bs *bufferState, a ex.Action) error {
	switch a.Diff {
	case ex.DiffSplit:
		return e.diffSplit(bs, a.File)
	case ex.DiffThis:
		return e.diffThis(bs)
	case ex.DiffOff:
		if !e.endDiff(bs, "相手のタブで :diffoff を打ちました") && e.diff.this == bs {
			e.diff.this = nil
		}
		return nil
	case ex.DiffUpdate:
		if s, _ := e.diffOf(bs); s != nil {
			s.res = nil // stale にして、その場で計算し直す
			e.freshDiff(s)
		}
		return nil
	case ex.DiffNext, ex.DiffPrev, ex.DiffObtain, ex.DiffPut:
		// command.State が Navigator で行うので、Result.Actions には入らない。来たら内部の誤りである。
		return errors.New("内部の誤り: 差分への移動と取り込みが本体の動作として渡された")
	}
	return fmt.Errorf("未知の見比べの操作 %d", a.Diff)
}

// errOutsideMux は、端末多重化ソフトの外で見比べを始めようとしたことを表す（基本設計 6.8、10 章の仮）。
var errOutsideMux = errors.New("端末多重化ソフト（tmux、herdr）の外では見比べを使えません（見比べには 2 つ目の編集の client が要ります）")

// errNotInDiff は、見比べていないタブで do と dp を打ったことを表す（Vim 9.1 の E99）。
var errNotInDiff = errors.New("E99: このタブは見比べていません")

// diffSplit は、:diffsplit を行う（基本設計 6.8）。
func (e *Editor) diffSplit(bs *bufferState, file string) error {
	abs := e.absPath(file)
	if abs == bs.path {
		return fmt.Errorf("%s はこのタブのファイルです（別のファイルを指定してください）", file)
	}
	if o := e.findPath(abs); o != nil && o.owner != nil {
		if o.owner == bs.owner {
			// 同じ client の 2 つのタブは同時に画面に出ないので、見比べられない（基本設計 6.8）。
			return fmt.Errorf("%s はこの client のタブです（見比べるには別の client のタブにしてください）", e.bufferName(o))
		}
		e.startDiff(bs, o)
		return nil
	}
	if err := e.splitForDiff(e.view, abs, &diffPending{from: bs, path: abs}); err != nil {
		return err
	}
	e.message = infoMessage(fmt.Sprintf("%s を別のペインで開いて見比べます", filepath.Base(abs)))
	return nil
}

// splitForDiff は、client c のペインを端末多重化ソフトの層（Config.SplitPane）で分割し、そこでこの server に繋ぐ編集の client を
// 起動するよう頼んで、p を相手の client を待つ見比べにする。file は新しい client に渡すファイルで、空ならファイルを渡さない（diff/start）。
// 端末多重化ソフトの外（ペインを知らない、SplitPane が無い）なら errOutsideMux を、前の待ちがあれば errDiffPending を返す。
// 分割の誤りは、別の goroutine で知り、イベントループで待ちを捨てて（dropPending）、c のメッセージの行に出す。
// diffPendingTimeout を過ぎても新しい client が繋がなければ、待ちを捨てる。
func (e *Editor) splitForDiff(c *clientView, file string, p *diffPending) error {
	pane := c.info.Pane
	if !e.canSplit(c) {
		return errOutsideMux
	}
	if e.diff.pending != nil {
		return errDiffPending
	}
	e.diff.seq++
	p.token = strconv.FormatUint(e.diff.seq, 10)
	e.diff.pending = p
	split, abs, token := e.cfg.SplitPane, file, p.token
	var dir string
	e.withView(c, func() { dir = e.workDir() })
	// later は、f をイベントループで動かす。
	later := func(f func()) {
		select {
		case e.inspect <- f:
		case <-e.stop:
		}
	}
	time.AfterFunc(diffPendingTimeout, func() {
		later(func() {
			if e.diff.pending == p {
				e.dropPending(p)
			}
		})
	})
	go func() {
		defer e.recoverPanic()
		err := split(pane, abs, dir, token)
		if err == nil {
			return
		}
		e.log.Warn("見比べのペインを開けない", "file", abs, "err", err)
		// 誤りは、イベントループで、分割を頼んだ client に知らせる。
		later(func() {
			if e.diff.pending == p {
				e.dropPending(p)
			}
			if c.attached() {
				e.withView(c, func() { e.showError(fmt.Errorf("見比べのペインを開けません: %w", err)) })
			}
		})
	}()
	return nil
}

// canSplit は、client c のペインを、見比べのために分割できるか（端末多重化ソフトの中か）を返す。
func (e *Editor) canSplit(c *clientView) bool {
	return e.cfg.SplitPane != nil && c != nil && c.info.Pane != ""
}

// diffThis は、:diffthis を行う。相手を待っているタブが別の client にあれば、その 2 つを見比べ始める。
// 既に見比べているタブではそのことを知らせ、端末多重化ソフトの外で client が 1 つのときは、相手が現れないので断る（PR #232 のレビューの推奨 3、4）。
func (e *Editor) diffThis(bs *bufferState) error {
	if s, side := e.diffOf(bs); s != nil {
		e.message = infoMessage(fmt.Sprintf("%s と既に見比べています（終えるには :diffoff）", e.bufferName(s.bs[side.Other()])))
		return nil
	}
	if e.view.info.Pane == "" && len(e.clients) < 2 {
		return errOutsideMux
	}
	t := e.diff.this
	if t == nil || !slices.Contains(e.bufs, t) || t.owner == nil || t.owner == bs.owner {
		// 同じ client の 2 つのタブは見比べられないので、同じ client で打ち直したら、後のタブを待つ側にする。
		e.diff.this = bs
		e.message = infoMessage("見比べる相手のタブ（別の client）でも :diffthis を打ってください")
		return nil
	}
	e.diff.this = nil
	e.startDiff(t, bs)
	return nil
}

// diffNavigate は、]c、[c、do、dp を行う（command.Navigator.Navigate）。bs はカーソルのあるタブのバッファ、cursor はそのカーソルである。
// ]c と [c は、移る先の位置と true を返す（State がカーソルを動かす）。移る先が無ければ command.ErrFailed を包んだエラーを返す
// （Vim と同じくメッセージは出さない）。回数が残りの差分の数より多ければ、最後の差分へ移る（Vim 9.1 と同じ）。
// do と dp は、Vim の範囲を付けない :diffget と :diffput と同じ塊（diff.Result.HunkAt）を、アンドゥの 1 単位として置き換える。
// 対象の塊が無ければ何もせず command.ErrFailed を包んだエラーを返す。dp の相手のバッファが編集を禁止していれば断る（E21）。
//
// req: FR-101
func (e *Editor) diffNavigate(bs *bufferState, a ex.Action, cursor buffer.Position) (buffer.Position, bool, error) {
	s, side := e.diffOf(bs)
	if s == nil {
		if a.Diff == ex.DiffObtain || a.Diff == ex.DiffPut {
			return buffer.Position{}, false, errNotInDiff
		}
		return buffer.Position{}, false, fmt.Errorf("%w: not in diff mode", command.ErrFailed)
	}
	// マクロの再生の途中なら、まだ知らせていない変更を先に送る（do と dp の変更をその後に送るため）。
	e.syncChanges(bs, bs.state.PendingChanges())
	e.freshDiff(s)
	res := s.res
	switch a.Diff {
	case ex.DiffNext, ex.DiffPrev:
		line, moved := cursor.Line, false
		for range max(a.Count, 1) {
			var i int
			var ok bool
			if a.Diff == ex.DiffNext {
				i, ok = res.Next(side, line)
			} else {
				i, ok = res.Prev(side, line)
			}
			if !ok {
				break
			}
			line, moved = res.Start(i, side), true
		}
		if !moved {
			return buffer.Position{}, false, fmt.Errorf("%w: no more changes", command.ErrFailed)
		}
		return buffer.Position{Line: line}, true, nil
	case ex.DiffObtain, ex.DiffPut:
		op := diff.OpGet
		if a.Diff == ex.DiffPut {
			op = diff.OpPut
		}
		i, ok := res.HunkAt(side, cursor.Line, op)
		if !ok {
			return buffer.Position{}, false, fmt.Errorf("%w: no change at the cursor", command.ErrFailed)
		}
		p := res.Obtain(i, side)
		if op == diff.OpPut {
			p = res.Put(i, side)
		}
		target := s.bs[p.Side]
		if _, err := e.applyExternal(target, buffer.Transaction{BaseVersion: target.buf.Version(), Edits: []buffer.Edit{p.Edit}}); err != nil {
			return buffer.Position{}, false, err
		}
		e.freshDiff(s)
		return buffer.Position{}, false, nil
	case ex.DiffSplit, ex.DiffThis, ex.DiffOff, ex.DiffUpdate:
	}
	return buffer.Position{}, false, fmt.Errorf("内部の誤り: 移動として渡せない見比べの操作 %d", a.Diff)
}

// syncDiffScroll は、描いた後に、見比べている組の両側の表示の最初の行とカーソルの行をそろえ、そろえた側を描き直す（FR-100）。
// 両側のタブが、それぞれの client のカーソルのあるタブのときだけそろえる。
//
// req: FR-100
func (e *Editor) syncDiffScroll() {
	for _, s := range e.diff.sessions {
		if s.res != nil {
			e.syncSession(s)
		}
	}
}

// syncSession は、組 s の両側をそろえる（syncDiffScroll）。
func (e *Editor) syncSession(s *diffSession) {
	var row, cur [2]int
	for i, bs := range s.bs {
		c := bs.owner
		if c == nil || !c.attached() {
			return
		}
		v := e.viewOf(c)
		if v.cur != bs {
			return
		}
		top, fill := v.renderer.DiffTop()
		row[i] = s.layout[i].Row(min(top, s.layout[i].Lines-1)) - fill
		cur[i] = bs.state.Cursor().Line
	}
	// 合わせる側（driver）は、操作中の client の側。どちらでもなければ、最初の行かカーソルの行が変わった側。
	d := -1
	for i, bs := range s.bs {
		if bs.owner == e.op {
			d = i
		}
	}
	if d < 0 {
		for i := range s.bs {
			if row[i] != s.lastRow[i] || cur[i] != s.lastCur[i] {
				d = i
				break
			}
		}
	}
	if d < 0 {
		return
	}
	o := 1 - d
	if row[o] == row[d] && cur[d] == s.lastCur[d] && s.lastRow[d] >= 0 {
		s.lastRow, s.lastCur = row, cur
		return
	}
	ob := s.bs[o]
	moved := false
	e.withView(ob.owner, func() {
		if !e.viewIdle() || e.cur != ob {
			return
		}
		ol := s.layout[o]
		line, fill := ol.LineAtRow(row[d])
		e.renderer.SetDiffTop(line, fill)
		// カーソルを、合わせる側のカーソルの行に対応する行へ動かし（Vim の diff の 'cursorbind'）、表示の範囲に収める。
		want := ob.state.Cursor().Line
		if cur[d] != s.lastCur[d] || s.lastCur[d] < 0 {
			want = s.res.MapLine(diff.Side(d), cur[d])
		}
		last, lf := ol.LineAtRow(row[d] + max(e.renderer.TextRows(), 1) - 1)
		if lf > 0 && last > line {
			last--
		}
		want = min(max(want, line), max(last, line))
		if p := ob.state.Cursor(); p.Line != want {
			ob.state.SetCursor(buffer.Position{Line: want, Byte: p.Byte}) // SetCursor が行の中の文字の位置に丸める
		}
		e.renderView()
		moved = true
	})
	if !moved {
		return
	}
	top, fill := e.viewOf(ob.owner).renderer.DiffTop()
	row[o] = s.layout[o].Row(min(top, s.layout[o].Lines-1)) - fill
	cur[o] = ob.state.Cursor().Line
	s.lastRow, s.lastCur = row, cur
}
