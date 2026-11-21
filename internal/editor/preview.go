package editor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/input"
	"github.com/piplupflipper/vi-imitation/internal/option"
	"github.com/piplupflipper/vi-imitation/internal/scroll"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 一覧、木、ファインダーのカーソルの項目のプレビュー（基本設計 1.4 の「#288 のプレビュー」。FR-149）。
//
// # 汎用の口
//
// 拡張機能は、項目ごとにプレビューに出すもの（ext.ViewItem.Preview。ファイルと行、ディレクトリ、開いているバッファ）の場所だけを付ける。
// 中身は本体が読み、ファインダーとファイラのための処理は持たない。窓に出すかは、窓の幅の規則（ui.PreviewRule.Layout）だけで決め、
// 出す場所（中央のポップアップ、端末多重化ソフトのポップアップ、分割したペイン）では分けない。
//
// # 開く時点
//
// 描く前（render）に、client ごとの部品のカーソルの項目から、出したいもの（previewState.pending）を決める。
// 今出しているものと違えば、previewDelay の間を置くタイマーを掛け直し、時間が来たらイベントループで開く（loadPreview）。
// 続けてカーソルを動かしている間は掛け直し続けるので、止まってから開く。プレビューのバッファを開くと拡張機能（tree-sitter など）が
// 起動して解析するので、通り過ぎる項目ごとに開き直さないためである。ファイルとディレクトリは、イベントループの外の goroutine で読む。
// 同じファイル（同じバッファ）の別の行へ移ったときは、読み直さずにカーソルを移す。
//
// # バッファ
//
// ファイルとディレクトリの中身は、編集を禁止したファイルでないバッファ（ポップアップのバッファと同じく :ls に出さない）にして出す。
// ファイルの種類はファイルの名前から決めるので、tree-sitter が構文の色を付ける。開いているバッファ（ファイルのバッファを含む）は、
// 写さずにそのバッファを出し、カーソルとスクロールだけをプレビューの側に持つ（previewState.owned が false）。
// プレビューのバッファは、別の項目のプレビューに替えたときと、ポップアップを閉じたときに閉じる。
//
// # フォーカス
//
// プレビューを出している窓で <C-w>w を打つと、フォーカスをプレビューへ移し、ノーマルモードのキー（/、j、k、<C-d> など）で読める（FR-71）。
// <C-w>w か <Esc> で一覧（ファインダーの入力の行）へ戻る。開いているバッファを出しているときは、移る前に編集を禁止した写しに替える
// （プレビューの中のカーソルの移動で、タブのカーソルを動かさないため）。

// プレビューの読み込みの決まり（仮）。
const (
	// previewDelay は、カーソルの項目が替わってから、プレビューを開くまでの間である。
	previewDelay = 100 * time.Millisecond
	// previewMaxBytes は、中身を出すファイルの大きさの上限である。超えるファイルは、中身を出さずに理由を出す。
	previewMaxBytes = 1 << 20
	// previewBinaryScan は、バイナリのファイルかを決めるために NUL のバイトを探す、先頭のバイト数である（git の判定と同じ 8000）。
	previewBinaryScan = 8000
	// previewMaxEntries は、ディレクトリの中身の一覧に出す数の上限である。
	previewMaxEntries = 1000
)

// previewState は、ポップアップかペインの部品 1 つの、カーソルの項目のプレビューである（floatState.preview）。
type previewState struct {
	// key は、出しているもの（previewKey）である。空なら何も出していない。
	key string
	// pending は、出そうとしている（タイマーを掛けたか、読んでいる）もので、loading はそれを持っていることを表す。
	pending string
	loading bool
	// gen は、出そうとするものを替えるたびに進める番号である。古いタイマーと読み込みの結果を捨てる。
	gen uint64
	// stop は、掛けたタイマーを止める。
	stop func() bool
	// bs は、出しているバッファである。owned なら、このプレビューのために作ったバッファで、替えるときと閉じるときに閉じる。
	// owned でなければ、開いているバッファをそのまま出している。source は、bs の中身の出どころ（previewSource）である。
	bs     *bufferState
	owned  bool
	source string
	// line は、色を付ける行（0 から）で、負なら付けない。title は見出し、note は中身を出さない理由である。
	line  int
	title string
	note  string
	// id は、出すものを替えるたびに付け直す番号（ui.Preview.ID）である。描く側は、替わったら line を真ん中へスクロールし直す。
	id uint64
	// focus は、フォーカスをプレビューへ移したことを表す。キーは bs の State へ入る。
	focus bool
	// anchor は、開いているバッファを出しているとき（owned でない）に、スクロール（scrollPreview）で動かした中身のカーソルの行で、
	// anchored がそれを持っていることを表す。持っていなければ、中身のカーソルは色を付ける行（line）に置く。
	// 開いているバッファのカーソルはタブのカーソルなので動かさず、プレビューの側に持つ。出すものを替えたら捨てる。
	anchor   int
	anchored bool
}

// forget は、閉じたバッファ bs をプレビューに出していれば、出していないことにする。次の描画で出し直す。
func (p *previewState) forget(bs *bufferState) {
	if p.bs != bs {
		return
	}
	p.bs, p.owned, p.source, p.key, p.focus = nil, false, "", "", false
}

// previewRule は、設定の表の preview.* の今の値を返す（基本設計 7.3 の「エディタ全体」の項目は、使うたびに読む）。
func (e *Editor) previewRule() ui.PreviewRule {
	return ui.PreviewRule{
		MinWindowWidth:     e.options.Get(option.PreviewMinWindowWidth).Int(),
		WidthPercent:       e.options.Get(option.PreviewWidthPercent).Int(),
		WindowWidthPercent: e.options.Get(option.PreviewWindowWidthPercent).Int(),
	}
}

// previewCapable は、部品 f がプレビューの欄を持つ一覧、木、ファインダーかを返す。
func (e *Editor) previewCapable(f *floatState) bool {
	return f.hasPreview && (f.kind == floatList || f.kind == floatTree || f.kind == floatFinder)
}

// previewShown は、部品 f の窓にプレビューを出すかを返す。ui の描き方と同じ規則（ui.PreviewRule.Layout）で決める。
//
//   - 中央のポップアップ: 窓を置く画面の幅の preview.windowWidthPercent % を窓の幅（枠線を含む）として決める。
//     画面は、編集の client の画面に描くときはその画面、端末多重化ソフトのポップアップに出すときは、ポップアップの幅の百分率を数える端末である
//     （floatState.screen。requestPopup が問い合わせ、同じ列数からポップアップの幅も決める）。問い合わせている間は出さない。
//     出す場所で分けず、同じ設定の項目と同じ規則で、実際の窓の幅と同じ値から決めるためである
//   - 分割したペイン: そのペインの部品の client の画面の全体が窓である
//   - カーソルの近くの窓: 出さない
//
// 出すと決めたものだけを ui へ渡し（floatFrame）、ui は判定し直さずに窓の中を分ける（ui.PreviewRule.Split）。
// 読み込んでいる client が f を開いた client であるときに呼ぶ（e.renderer がその画面の描画の部品である）。
//
// req: FR-149
func (e *Editor) previewShown(f *floatState) bool {
	if !e.previewCapable(f) {
		return false
	}
	switch {
	case f.pane && f.remote != nil:
		_, _, ok := e.previewRule().Layout(f.remote.r.Width, true)
		return ok
	case f.center && !f.screenPending:
		width := e.renderer.Width
		if f.screen > 0 {
			width = f.screen
		}
		_, _, ok := e.previewRule().Layout(width, false)
		return ok
	}
	return false
}

// cursorPreview は、部品 f のカーソルの項目のプレビューに出すものを返す。無ければ nil である。
func cursorPreview(f *floatState) *ext.ViewPreview {
	src := f.bs
	if f.kind == floatFinder {
		src = f.list
	}
	if row := src.state.Cursor().Line; row < len(f.rows) {
		return f.rows[row].preview
	}
	return nil
}

// previewSource は、プレビューに出すもの pv の中身の出どころを、開いているバッファなら "b:<番号>"、それ以外は "p:<絶対パス>" で返す。
// 相対パスは、読み込んでいる client の作業ディレクトリから数える。
func (e *Editor) previewSource(pv *ext.ViewPreview) string {
	if pv.Buffer != nil {
		return "b:" + strconv.FormatUint(uint64(*pv.Buffer), 10)
	}
	return "p:" + e.absPath(pv.Path)
}

// previewKey は、プレビューに出すもの pv を見分ける文字列（出どころと行）を返す。pv が nil なら空である。
func (e *Editor) previewKey(pv *ext.ViewPreview) string {
	if pv == nil {
		return ""
	}
	line := "-"
	if pv.Line != nil {
		line = strconv.Itoa(*pv.Line)
	}
	return e.previewSource(pv) + "#" + line
}

// syncPreviews は、読み込んでいる client のポップアップとペインの部品のプレビューを、カーソルの項目に合わせる。描く前に呼ぶ。
func (e *Editor) syncPreviews() {
	if f := e.float; f != nil {
		e.syncPreview(f)
	}
	for _, p := range e.panes {
		e.syncPreview(p)
	}
}

// syncPreview は、部品 f に出したいプレビュー（窓に出さないか、カーソルの項目にプレビューが無ければ空）が、出しているものと違えば、
// previewDelay の後に開くタイマーを掛ける（パッケージの doc の「開く時点」）。既に同じものを開こうとしていれば何もしない。
// 出したいものが出しているものに戻ったら、掛けたタイマーを止める。バッファの開閉はここでは行わず、タイマーの先（loadPreview）で行う。
//
// req: FR-149
func (e *Editor) syncPreview(f *floatState) {
	p := &f.preview
	var pv *ext.ViewPreview
	if e.previewShown(f) {
		pv = cursorPreview(f)
	}
	want := e.previewKey(pv)
	if p.loading && want == p.pending {
		return
	}
	if want == p.key {
		e.cancelPreview(p)
		return
	}
	e.cancelPreview(p)
	p.gen++
	p.pending, p.loading = want, true
	gen, c := p.gen, e.view
	stop := e.stop
	p.stop = e.afterFunc(previewDelay, func() {
		select {
		case e.inspect <- func() { e.loadPreview(c, f, gen, pv) }:
		case <-stop:
		}
	})
}

// cancelPreview は、プレビューを開くタイマーを止め、読み込み中の結果を捨てさせる。
func (e *Editor) cancelPreview(p *previewState) {
	if !p.loading {
		return
	}
	if p.stop != nil {
		p.stop()
		p.stop = nil
	}
	p.gen++
	p.pending, p.loading = "", false
}

// previewAlive は、client c の部品 f がまだ開いているかを返す。
func (e *Editor) previewAlive(c *clientView, f *floatState) bool {
	if !c.attached() {
		return false
	}
	v := e.viewOf(c)
	return v.float == f || slices.Contains(v.panes, f)
}

// loadPreview は、タイマーの時間が来たときに、client c の部品 f のプレビューを pv にする（イベントループで呼ぶ）。
// gen が今の番号でなければ（その後にカーソルの項目が替わった）捨てる。
//
//   - pv が nil: プレビューを空にする
//   - 出しているバッファと同じ出どころ: 読み直さずに行だけを移す
//   - 開いているバッファ（Buffer か、開いているファイルの Path）: そのバッファを出す
//   - それ以外: イベントループの外でファイルかディレクトリを読み、読み終えたらイベントループで出す（installPreview）
//
// req: FR-149
func (e *Editor) loadPreview(c *clientView, f *floatState, gen uint64, pv *ext.ViewPreview) {
	if !e.previewAlive(c, f) || f.preview.gen != gen {
		return
	}
	e.withView(c, func() {
		p := &f.preview
		p.stop = nil
		key := e.previewKey(pv)
		if pv == nil {
			e.setPreview(f, key, previewContent{}, nil, "")
			return
		}
		line := -1
		if pv.Line != nil {
			line = *pv.Line
		}
		source := e.previewSource(pv)
		if p.bs != nil && p.source == source && slices.Contains(e.bufs, p.bs) {
			e.movePreviewLine(f, key, line, pv)
			return
		}
		var open *bufferState
		if pv.Buffer != nil {
			open = e.findBuffer(*pv.Buffer)
			if open == nil {
				e.setPreview(f, key, previewContent{note: fmt.Sprintf("バッファ %d は開いていません", *pv.Buffer)}, nil, source)
				return
			}
		} else if bs := e.findPath(e.absPath(pv.Path)); bs != nil && !bs.popup {
			open = bs
		}
		if open != nil {
			e.setPreview(f, key, previewContent{line: line, title: e.previewTitle(e.bufferName(open), open.path, line)}, open, source)
			return
		}
		path := e.absPath(pv.Path)
		title := e.previewTitle(path, path, line)
		go func() {
			defer e.recoverPanic()
			content := readPreview(path)
			content.line, content.title = line, title
			if content.dir {
				content.title = strings.TrimSuffix(title, string(filepath.Separator)) + string(filepath.Separator)
			}
			e.later(func() {
				if !e.previewAlive(c, f) || f.preview.gen != gen {
					return
				}
				e.withView(c, func() {
					if !content.dir && content.note == "" {
						content.fileType = e.detectFileType(path)
					}
					e.setPreview(f, key, content, nil, source)
				})
			})
		}()
	})
}

// previewTitle は、プレビューの見出しを返す。name は出す名前で、path が作業ディレクトリの下ならそこからの相対パスにする（「ポップアップで見る」と同じ）。
// line が負でなければ、":行" を足す。
func (e *Editor) previewTitle(name, path string, line int) string {
	if path != "" {
		name = path
		if rel, err := filepath.Rel(e.workDir(), path); err == nil && filepath.IsLocal(rel) {
			name = rel
		}
	}
	if line >= 0 {
		return fmt.Sprintf("%s:%d", name, line+1)
	}
	return name
}

// movePreviewLine は、出しているバッファのまま、色を付ける行を line にする（同じファイルの grep の別の一致）。
func (e *Editor) movePreviewLine(f *floatState, key string, line int, pv *ext.ViewPreview) {
	p := &f.preview
	p.key, p.pending, p.loading = key, "", false
	p.line, p.anchored = line, false
	name := ""
	if p.bs.path == "" {
		name = e.bufferName(p.bs)
	}
	path := p.bs.path
	if p.owned && pv.Path != "" {
		path = e.absPath(pv.Path)
	}
	p.title = e.previewTitle(name, path, line)
	if p.owned && p.bs.buf.LineCount() > 0 {
		at := max(line, 0)
		p.bs.state.SetCursor(buffer.Position{Line: min(at, p.bs.buf.LineCount()-1)})
	}
	e.previewSeq++
	p.id = e.previewSeq
}

// previewContent は、プレビューに出す中身である。
type previewContent struct {
	// text は、ファイルの中身かディレクトリの一覧である。note が空でなければ読まない。
	text     string
	fileType string
	// dir は、ディレクトリの一覧であることを表す。
	dir bool
	// note は、中身を出さない理由（大きなファイル、読めないファイル、バイナリのファイル、空のディレクトリ）である。
	note  string
	line  int
	title string
}

// setPreview は、部品 f のプレビューを、key の中身にする。前に作ったバッファは閉じる。
// open が nil でなければ、開いているバッファ open をそのまま出す。そうでなく content.note が空なら、content.text を中身にした
// 編集を禁止したファイルでないバッファを作る（ファイルの種類 content.fileType で拡張機能が起動して構文の色を付ける）。
// フォーカスがプレビューにあれば、一覧へ戻してから替える。
func (e *Editor) setPreview(f *floatState, key string, content previewContent, open *bufferState, source string) {
	p := &f.preview
	e.dropPreviewBuffer(f)
	p.key, p.pending, p.loading = key, "", false
	p.line, p.title, p.note, p.source = -1, content.title, content.note, source
	p.anchored = false
	e.previewSeq++
	p.id = e.previewSeq
	switch {
	case key == "":
		p.title = ""
	case open != nil:
		p.bs, p.owned, p.line = open, false, content.line
	case content.note == "":
		bs := e.newPopupBuffer(previewBufferName(content.title), content.text, content.fileType, true)
		bs.state.Viewport = previewViewport{f: f, r: e.renderer}
		p.bs, p.owned, p.line = bs, true, content.line
		if content.dir {
			p.line = -1
		}
		if p.line >= 0 {
			bs.state.SetCursor(buffer.Position{Line: min(p.line, bs.buf.LineCount()-1)})
		}
	}
}

// previewBufferName は、プレビューのバッファの名前（ステータス行に出る）を返す。見出しが空なら決まった名前にする。
func previewBufferName(title string) string {
	if title == "" {
		return "[プレビュー]"
	}
	return title
}

// dropPreviewBuffer は、部品 f のプレビューのバッファを外す。このプレビューのために作ったバッファなら閉じる。
// フォーカスがプレビューにあれば、先に一覧（ファインダーの入力の行）へ戻す。
func (e *Editor) dropPreviewBuffer(f *floatState) {
	p := &f.preview
	if p.focus {
		e.leavePreview(f)
	}
	bs, owned := p.bs, p.owned
	p.bs, p.owned, p.source = nil, false, ""
	if owned && bs != nil && slices.Contains(e.bufs, bs) {
		e.closeBufferState(bs)
	}
}

// closePreview は、部品 f を閉じるときに、プレビューのタイマーを止め、プレビューのために作ったバッファを閉じる（FR-149）。
//
// req: FR-149
func (e *Editor) closePreview(f *floatState) {
	p := &f.preview
	e.cancelPreview(p)
	p.focus = false
	e.dropPreviewBuffer(f)
	p.key = ""
}

// togglePreviewFocus は、<C-w>w で、部品 f の一覧（ファインダーの入力の行）とプレビューの間でフォーカスを移す（FR-149、FR-71）。
// 開いているバッファを出していれば、移る前に編集を禁止した写しに替える。中身の無いプレビューへは移らずに誤りを返す。
//
// req: FR-149
func (e *Editor) togglePreviewFocus(f *floatState) error {
	p := &f.preview
	if p.focus {
		e.leavePreview(f)
		return nil
	}
	if p.bs == nil || !slices.Contains(e.bufs, p.bs) {
		return errors.New("プレビューに中身がありません")
	}
	if !p.owned {
		src := p.bs
		lines := make([]string, src.buf.LineCount())
		for i := range lines {
			lines[i] = src.buf.Line(i)
		}
		bs := e.newPopupBuffer(previewBufferName(p.title), strings.Join(lines, "\n"), src.fileType(), true)
		bs.state.Viewport = previewViewport{f: f, r: e.renderer}
		if n := bs.buf.LineCount(); n > 0 {
			bs.state.SetCursor(buffer.Position{Line: min(max(p.line, 0), n-1)})
		}
		p.bs, p.owned = bs, true
	}
	p.focus = true
	e.switchFocus(p.bs)
	return nil
}

// leavePreview は、プレビューのフォーカスを、部品 f の一覧（ファインダーの入力の行）へ戻す。
func (e *Editor) leavePreview(f *floatState) {
	p := &f.preview
	p.focus = false
	if p.bs != nil && e.cur == p.bs {
		e.switchFocus(f.bs)
	}
}

// uiPreview は、部品 f のプレビューの描画の入力を作る。
func (e *Editor) uiPreview(f *floatState) *ui.Preview {
	p := &f.preview
	out := &ui.Preview{ID: p.id, Title: p.title, Note: p.note, Line: -1, Focused: p.focus}
	bs := p.bs
	if bs == nil || !slices.Contains(e.bufs, bs) {
		out.Focused = false
		return out
	}
	n := bs.buf.LineCount()
	if p.line >= 0 && n > 0 {
		out.Line = min(p.line, n-1)
	}
	view := &ui.Frame{Text: bs.buf, LineLayout: e.lineLayout(bs, false), Margins: e.margins(), Decorations: bs.decoView}
	switch {
	case p.owned:
		st := bs.state
		view.Cursor = st.Cursor()
		if p.focus {
			view.Mode = uiMode(st.Mode())
			view.OperatorPending = st.Pending()
			if start, end, linewise, ok := st.Selection(); ok {
				view.Selection = ui.Selection{Active: true, Start: start, End: end, Linewise: linewise}
			}
		}
	default:
		// 開いているバッファは、そのカーソルを動かさず、色を付ける行（スクロールした後はプレビューの側に持つ行）にカーソルを置いた形で描く。
		line := max(out.Line, 0)
		if p.anchored {
			line = min(p.anchor, max(n-1, 0))
		}
		view.Cursor = buffer.Position{Line: line}
	}
	out.View = view
	return out
}

// previewScrollAction は、ref がプレビューをスクロールする操作（input.PreviewPrefix で始まる ID。FR-159）なら、
// バッファ bs でキーを受けている部品（ポップアップかペイン。フォーカスをプレビューへ移していればそのプレビューのバッファ）の
// プレビューを n 回スクロールし、true を返す。n が 0 なら 1 回である（回数を付けていない）。
// bs でキーを受けている部品が無いか、その部品がプレビューを出していなければ、何もせずに true を返す（編集のバッファの <S-Down>）。
// それ以外の操作なら false を返す。
//
// ノーマルモードのキー（ファイラの木）は、ほかの提供者へ振り分ける操作と同じく extQuery.Action から呼ぶ。
// インサートモードのキー（ファインダーの入力の行）は、インサートモードの State が提供者へ振り分ける操作を実行しないので、
// ポップアップのキーの口（previewInsertKey）から呼ぶ。
//
// req: FR-159
func (e *Editor) previewScrollAction(bs *bufferState, ref editcmd.Ref, n int) bool {
	how, ok := strings.CutPrefix(ref.ID, input.PreviewPrefix)
	if !ref.Provider() || !ok {
		return false
	}
	f := e.float
	if f == nil || !f.focused || (f.bs != bs && (!f.preview.focus || f.preview.bs != bs)) {
		return true
	}
	e.scrollPreview(f, how, max(n, 1))
	return true
}

// previewInsertKey は、フォーカスのあるポップアップ f の入力の行（インサートモード）のキー k が、
// 操作の表のインサートモードでプレビューをスクロールする操作に割り当てたキーなら、スクロールして true を返す（FR-159）。
// 設定の keymap で割り当てを変えたときも、その割り当てを引く（FR-126）。
//
// req: FR-159
func (e *Editor) previewInsertKey(f *floatState, k editcmd.Key) bool {
	b, ok := e.keymap.Lookup(input.MapInsert, []editcmd.Key{k})
	if !ok {
		return false
	}
	return e.previewScrollAction(f.bs, b.Ref, 1)
}

// scrollPreview は、部品 f のプレビューを、how（page-down、page-up、down、up）の向きと単位で n 回スクロールする（FR-159）。
// プレビューを出していないとき、中身が無いとき（理由だけを出している）、まだ描いていないときは何もしない。
//
// 1 画面は、プレビューの中身の行数から 2 を引いた行数（Vim の <C-f> と同じ。1 行より少なければ 1 行）である。
// 下へは、最後の行がプレビューの最後の行に来る位置で止め、最後の行の後に空の行を出さない（fzf-lua の preview-page-down が
// 最後の行を窓の下の端に合わせる（zb）のと同じ）。上へは最初の行で止める。端では何もしない（失敗にしない）。
// フォーカスは一覧（ファインダーの入力の行）に置いたままで、一覧のカーソルとプレビューに出すものは変えない。
//
// 中身を描く子の Renderer（previewViewport）のスクロールの位置を動かし、中身のカーソル（フォーカスが無ければ見えない）を
// 窓の中に入るように動かす。子の Renderer は次の描画でカーソルが窓の外ならスクロールし直すためである。
// プレビューのために作ったバッファはそのカーソルを、開いているバッファはプレビューの側の行（previewState.anchor）を動かす。
// 色を付ける行（grep の一致した行）は変えない。別の項目へ移ると、新しい項目の行を真ん中に置き直す（ui の drawPreview）。
//
// req: FR-159
func (e *Editor) scrollPreview(f *floatState, how string, n int) {
	p := &f.preview
	if !e.previewShown(f) || p.bs == nil || !slices.Contains(e.bufs, p.bs) {
		return
	}
	sub := previewViewport{f: f, r: e.renderer}.sub()
	if sub == nil {
		return
	}
	rows := max(sub.TextRows(), 1)
	lines := p.bs.buf.LineCount()
	var step int
	switch how {
	case "page-down":
		step = max(rows-2, 1)
	case "page-up":
		step = -max(rows-2, 1)
	case "down":
		step = 1
	case "up":
		step = -1
	default:
		return
	}
	top := sub.Top()
	next := top + step*n
	if step > 0 {
		// 行を真ん中に置いた位置（最後の行の後に空の行が出ている）からは、上へ戻さない。
		next = max(min(next, max(lines-rows, 0)), top)
	} else {
		next = max(next, 0)
	}
	if next == top {
		return
	}
	sub.SetScroll(next, sub.Left())
	// カーソルは、窓の上と下に余白（scroll.margin。FR-174）を残した範囲に入れる。窓の端に置くと、次の描画が余白を残すために
	// スクロールを戻すためである。行を折り返すときは、描画と同じく折り返した行の高さで数える（窓の外に置くと、描画が最初の行を
	// 動かす。PR #338 のレビューの推奨 3）。送る量（1 画面）は、#308 のとおり行の数で数える。
	var ls scroll.Lines = uniformLines(lines)
	if e.lineWrap() && sub.TextWidth() > 0 {
		ls = previewLines{buf: p.bs.buf, wl: sub.WrapLines(e.lineLayout(p.bs, false))}
	}
	v := scroll.View{Lines: ls, Height: rows, Margin: e.margins().Rows, Top: next}
	if p.owned {
		cur := p.bs.state.Cursor()
		if l := v.CursorCorrect(cur.Line); l != cur.Line {
			p.bs.state.SetCursor(buffer.Position{Line: l})
		}
		return
	}
	cur := max(p.line, 0)
	if p.anchored {
		cur = p.anchor
	}
	p.anchor, p.anchored = v.CursorCorrect(cur), true
}

// previewLines は、プレビューの中身のバッファの行の高さを、折り返した見た目の行の数で数える行の並びである（scroll.Lines）。
type previewLines struct {
	buf *buffer.Buffer
	wl  ui.WrapLines
}

// LineCount は、scroll.Lines の LineCount である。
func (l previewLines) LineCount() int { return l.buf.LineCount() }

// Rows は、scroll.Lines の Rows である。
func (l previewLines) Rows(line int) int { return max(l.wl.Rows(line, l.buf.Line(line)), 1) }

// uniformLines は、どの行の高さも 1 とした、行の数が int の値の行の並びである（scroll.Lines）。
type uniformLines int

// LineCount は、scroll.Lines の LineCount である。
func (n uniformLines) LineCount() int { return max(int(n), 1) }

// Rows は、scroll.Lines の Rows である。
func (uniformLines) Rows(int) int { return 1 }

// previewViewport は、プレビューのバッファのページ単位のスクロール（command.State.Viewport）を、プレビューの表示領域につなぐ口である。
type previewViewport struct {
	f *floatState
	r *ui.Renderer
}

// sub は、プレビューの中を描く子の Renderer を返す。まだ描いていなければ nil である。
func (v previewViewport) sub() *ui.Renderer {
	if c := v.f.remote; c != nil {
		return c.r.PreviewRenderer()
	}
	return v.r.PreviewRenderer()
}

func (v previewViewport) Top() int {
	if s := v.sub(); s != nil {
		return s.Top()
	}
	return 0
}

func (v previewViewport) Rows() int {
	if s := v.sub(); s != nil {
		return max(s.TextRows(), 1)
	}
	return 1
}

func (v previewViewport) Skip() int {
	if s := v.sub(); s != nil {
		return s.Skip()
	}
	return 0
}

func (v previewViewport) SetView(top, skip, hold int) {
	if s := v.sub(); s != nil {
		s.SetScrollHeld(top, skip, hold)
	}
}

// readPreview は、path のファイルかディレクトリを、プレビューの中身として読む。イベントループの外で呼ぶ。
// ディレクトリなら、中身をディレクトリを先に名前の順で並べ、ディレクトリの名前の後に / を付けた一覧にする。
// 読めないもの、通常のファイルでないもの（FIFO などは読むと止まりうる）、大きなファイル（previewMaxBytes を超える）、
// バイナリのファイル（先頭の previewBinaryScan バイトに NUL を含む）は、中身を出さずに理由（note）を返す。
//
// req: FR-149
func readPreview(path string) previewContent {
	fi, err := os.Stat(path)
	if err != nil {
		return previewContent{note: "読めません: " + reason(err)}
	}
	if fi.IsDir() {
		return readPreviewDir(path)
	}
	if !fi.Mode().IsRegular() {
		return previewContent{note: "通常のファイルでないので、中身を出しません"}
	}
	if fi.Size() > previewMaxBytes {
		return previewContent{note: fmt.Sprintf("大きなファイル（%d バイト）なので、中身を出しません（上限は %d バイト）", fi.Size(), previewMaxBytes)}
	}
	fp, err := os.Open(path)
	if err != nil {
		return previewContent{note: "読めません: " + reason(err)}
	}
	defer func() { _ = fp.Close() }() // 読むだけなので、閉じたときの誤りは使わない
	data, err := io.ReadAll(io.LimitReader(fp, previewMaxBytes+1))
	switch {
	case err != nil:
		return previewContent{note: "読めません: " + reason(err)}
	case len(data) > previewMaxBytes:
		return previewContent{note: fmt.Sprintf("大きなファイル（%d バイトを超える）なので、中身を出しません", previewMaxBytes)}
	case bytes.IndexByte(data[:min(len(data), previewBinaryScan)], 0) >= 0:
		return previewContent{note: "バイナリのファイルなので、中身を出しません"}
	}
	return previewContent{text: strings.TrimSuffix(string(data), "\n")}
}

// readPreviewDir は、ディレクトリ dir の中身の一覧を読む（readPreview）。
func readPreviewDir(dir string) previewContent {
	des, err := os.ReadDir(dir)
	if err != nil && len(des) == 0 {
		return previewContent{dir: true, note: "読めません: " + reason(err)}
	}
	type entry struct {
		name string
		dir  bool
	}
	es := make([]entry, 0, len(des))
	for _, d := range des {
		isDir := d.IsDir()
		if d.Type()&fs.ModeSymlink != 0 {
			if fi, serr := os.Stat(filepath.Join(dir, d.Name())); serr == nil {
				isDir = fi.IsDir()
			}
		}
		es = append(es, entry{name: d.Name(), dir: isDir})
	}
	if len(es) == 0 {
		return previewContent{dir: true, note: "空のディレクトリです"}
	}
	slices.SortFunc(es, func(a, b entry) int {
		if a.dir != b.dir {
			if a.dir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	var lines []string
	for i, x := range es {
		if i == previewMaxEntries {
			lines = append(lines, fmt.Sprintf("（ほか %d 個）", len(es)-i))
			break
		}
		name := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' {
				return ' '
			}
			return r
		}, x.name)
		if x.dir {
			name += string(filepath.Separator)
		}
		lines = append(lines, name)
	}
	return previewContent{dir: true, text: strings.Join(lines, "\n")}
}

// reason は、ファイルを読めなかった理由 err から、パスを除いた文を返す（見出しにパスを出すため）。
func reason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
