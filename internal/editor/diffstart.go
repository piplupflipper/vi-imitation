package editor

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
)

// 拡張機能が作るファイルでないバッファ（buffer/create、buffer/update、buffer/dispose）と、拡張機能から始める見比べ（diff/start）
// （基本設計 5.3、6.8、6.9。FR-64、FR-88、FR-99、FR-104）。
//
// git 連携の拡張機能（cmd/vi-imitation-ext-git）は、HEAD の版を buffer/create で編集を禁止したファイルでないバッファにし、
// diff/start でファイルのバッファと見比べさせる。
//
// buffer/create で作ったバッファは、どのタブにもせず、作った拡張機能（bufferState.creator）を覚える。
// その拡張機能だけが、buffer/update で中身を置き換え（編集を禁止していても置き換える）、buffer/dispose で閉じられる。
// 隠れたバッファを持たない（FR-64）ため、diff/start で見比べが始まらなかったとき（誤りで応答した、分割したペインの client が
// 期限までに繋がなかった、分割に失敗した）は、まだタブでない作ったバッファを閉じる（discardCreated）。
//
// diff/start で、片方のバッファだけがタブなら、:diffsplit と同じくそのタブの client のペインを分割し、ファイルを渡さずに
// 新しい編集の client を起動する。新しい client は、server のソケットを指定して繋ぎ（cmd/vi-imitation の -connect）、
// 環境変数 EnvSplit の印を Hello で知らせる。印が待ちと同じ client が繋いだら、名前の無い空のタブを作る代わりに、
// もう片方のバッファをタブにして（diffAdopt）、見比べを始める（diffAttached）。

// EnvSplit は、server が見比べのために分割したペインで起動する編集の client の、待ちの印を入れる環境変数の名前である（仮）。
// client は Hello の Env でこの値を知らせ、attach がそれを取り出して、ClientInfo.Env から外す（外部のコマンドへ渡さない）。
const EnvSplit = "VI_IMITATION_SPLIT"

// takeSplitToken は、info.Env から EnvSplit の値を取り出して外し、その値を返す。無ければ空を返す。
func takeSplitToken(info *ClientInfo) string {
	token := ""
	info.Env = slices.DeleteFunc(slices.Clone(info.Env), func(kv string) bool {
		v, ok := strings.CutPrefix(kv, EnvSplit+"=")
		if ok {
			token = v
		}
		return ok
	})
	return token
}

// bufferCreateRequest は、buffer/create を処理する。ファイルでないバッファを作り、稼働中の拡張機能へ buffer/open を送ってから応答する。
// Writable が false なら（既定）、編集を禁止した状態で始める（FR-88）。
//
// req: FR-88
func (e *Editor) bufferCreateRequest(ev *exthost.BufferCreateRequest) {
	p := ev.Params
	b := buffer.New(p.Text)
	b.SetFixEndOfLine(e.fixEOL)
	b.SetReadOnly(!p.Writable)
	bs := e.newBuffer(b, "", p.FileType)
	bs.title = p.Title
	bs.creator = ev.Extension
	_ = ev.Reply(ext.BufferCreateResult{Buffer: bs.id, Version: ext.Version(b.Version())})
}

// createdBy は、番号 id のバッファが、拡張機能 extID が buffer/create で作ったものなら返す。そうでなければ誤りを返す。
func (e *Editor) createdBy(extID string, id ext.BufferID) (*bufferState, error) {
	bs := e.findBuffer(id)
	switch {
	case bs == nil:
		return nil, invalidParams("unknown buffer %d", id)
	case bs.creator != extID:
		return nil, invalidParams("buffer %d was not created by this extension", id)
	}
	return bs, nil
}

// bufferUpdateRequest は、buffer/update を処理する。作った拡張機能のバッファの中身を、読み直しと同じ入り口（reload）で置き換える。
// 編集を禁止していても置き換え（基本設計 4.6 の、本体が中身を正当に書き換える場合）、変わった行の差分を buffer/change で送る。
func (e *Editor) bufferUpdateRequest(ev *exthost.BufferUpdateRequest) {
	bs, err := e.createdBy(ev.Extension, ev.Params.Buffer)
	if err != nil {
		_ = ev.ReplyError(err)
		return
	}
	cursor := bs.state.Cursor()
	e.reload(bs, buffer.New(ev.Params.Text))
	line := min(cursor.Line, bs.buf.LineCount()-1)
	bs.state.SetCursor(buffer.Position{Line: line, Byte: min(cursor.Byte, len(bs.buf.Line(line)))})
	_ = ev.Reply(ext.BufferUpdateResult{Version: ext.Version(bs.buf.Version())})
}

// bufferDisposeRequest は、buffer/dispose を処理する。作った拡張機能のバッファを閉じる。タブならタブも閉じる（FR-64）。
//
// そのバッファが最後の client の最後のタブ（最後の窓）なら、閉じる前に名前の無い空のバッファをその client のタブにして移り、
// server と client を終わらせない（Vim の :bd で最後のバッファを消したときと同じ）。
// 拡張機能から本体を終わらせる経路を作らず、:q の E162 の確かめを通らずに未保存の変更を失わないためである
// （PR #382 の再レビューの必須。git 連携の拡張機能が、左のペインを閉じた後に右の比べる元の版を閉じた）。
// 最後の client でなければ、:q で最後のタブを閉じたのと同じくその client を終える（見比べのペインを閉じるのに使う）。
//
// req: FR-64
func (e *Editor) bufferDisposeRequest(ev *exthost.BufferDisposeRequest) {
	bs, err := e.createdBy(ev.Extension, ev.Params.Buffer)
	if err != nil {
		_ = ev.ReplyError(err)
		return
	}
	if o := bs.owner; o != nil && len(e.clients) == 1 && e.clients[0] == o && len(o.tabs) == 1 {
		e.withView(o, func() { e.enter(e.newUnnamedBuffer("")) })
	}
	e.closeBufferState(bs)
	_ = ev.Reply()
}

// discardCreated は、bs が拡張機能 extID の buffer/create で作ったバッファで、まだどのタブでもなければ閉じる（FR-64）。
// nil か、他の拡張機能が作ったバッファなら何もしない（diff/start に他の拡張機能のバッファを渡しても、そのバッファは閉じない）。
//
// req: FR-64
func (e *Editor) discardCreated(bs *bufferState, extID string) {
	if bs != nil && extID != "" && bs.creator == extID && bs.owner == nil && slices.Contains(e.bufs, bs) {
		e.closeBufferState(bs)
	}
}

// diffStartRequest は、diff/start を処理する。始められなければ、まだタブでない作ったバッファを閉じ、画面に出せる日本語の誤りで応答する。
func (e *Editor) diffStartRequest(ev *exthost.DiffStartRequest) {
	res, err := e.diffStart(ev.Extension, ev.Params)
	if err != nil {
		e.discardCreated(e.findBuffer(ev.Params.A), ev.Extension)
		e.discardCreated(e.findBuffer(ev.Params.B), ev.Extension)
		_ = ev.ReplyError(err)
		return
	}
	_ = ev.Reply(res)
}

// errNoTab は、diff/start の 2 つのバッファのどちらもタブでないことを表す。
var errNoTab = errors.New("見比べる 2 つのバッファのどちらもタブではありません")

// diffStart は、diff/start の params の 2 つのバッファの見比べを始める（ext.DiffStartParams の doc の場合分け）。
//
// req: FR-99
// extID は diff/start を送った拡張機能で、待ちを捨てるときに閉じてよいのは、その拡張機能が作ったバッファだけである。
func (e *Editor) diffStart(extID string, p ext.DiffStartParams) (ext.DiffStartResult, error) {
	a, b := e.findBuffer(p.A), e.findBuffer(p.B)
	for i, bs := range []*bufferState{a, b} {
		id := []ext.BufferID{p.A, p.B}[i]
		if bs == nil {
			return ext.DiffStartResult{}, invalidParams("unknown buffer %d", id)
		}
		if bs.popup {
			return ext.DiffStartResult{}, invalidParams("buffer %d is a popup buffer", id)
		}
	}
	switch {
	case a.owner != nil && b.owner != nil:
		if a.owner == b.owner {
			return ext.DiffStartResult{}, fmt.Errorf("%s と %s は同じ client のタブです（見比べるには別の client のタブにしてください）", e.bufferName(a), e.bufferName(b))
		}
		e.startDiff(a, b)
		return ext.DiffStartResult{}, nil
	case a.owner == nil && b.owner == nil:
		return ext.DiffStartResult{}, errNoTab
	}
	from, other, swap := a, b, false
	if a.owner == nil {
		from, other, swap = b, a, true
	}
	c := from.owner
	if err := e.splitForDiff(c, "", &diffPending{from: from, buf: other, swap: swap, ext: extID}); err != nil {
		return ext.DiffStartResult{}, err
	}
	text := fmt.Sprintf("%s を別のペインで開いて見比べます", e.bufferName(other))
	e.withView(c, func() { e.showLaterMessage(infoMessage(text)) })
	return ext.DiffStartResult{Split: true}, nil
}

// diffAdopt は、繋ぐ client（読み込んでいる client）が、diff/start で分割したペインの client（印 token が待ちのものと同じ）なら、
// 待っているバッファをその client のタブにする。attach が startClient の前に呼ぶので、名前の無い空のタブは作られない。
func (e *Editor) diffAdopt(token string) {
	p := e.diff.pending
	if p == nil || p.buf == nil || token == "" || token != p.token {
		return
	}
	if p.buf.owner != nil || !slices.Contains(e.bufs, p.buf) {
		e.dropPending(p)
		return
	}
	e.enter(p.buf)
}
