package exthost

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// ErrBufferState は、バッファの開閉や版の並びが食い違っていることを表す（開いていないバッファの変更、同じバッファを 2 回開くなど）。
// 呼び出し側の誤りであり、このとき拡張機能へは何も送らない。
var ErrBufferState = errors.New("exthost: inconsistent buffer state")

// BufferSnapshot は、バッファを開いた時点の全文と付随する情報である。
type BufferSnapshot struct {
	ID ext.BufferID
	// Path は、バッファに対応するファイルのパスである。ファイルに対応しないバッファでは空。
	Path string
	// FileType は、ファイルの種類（"markdown" など）である。決まらなければ空。
	FileType string
	// Version は、Text の時点のバージョン番号である。
	Version uint64
	// Text は、バッファの全文である。internal/buffer の Buffer.Text が返すもの（末尾の改行を含まない）を渡す。
	Text string
	// NoFinalNewline は、ファイルへ書き出したときに最後の行の後に改行が付かないことを表す。
	// internal/buffer の Buffer.FinalNewline が false を返すバッファと、行の無いバッファ（Buffer.NoLines）で true にする。
	// ゼロ値（false）は改行が付くことを表し、拡張機能へは ext.BufferOpenParams の FinalNewline を true にして送る。
	NoFinalNewline bool
}

// document は、Host が持つバッファの写しである。
// 再起動した拡張機能と buffer/resync に全文を送るために、本体のバッファと同じ内容を保つ。
type document struct {
	id       ext.BufferID
	path     string
	fileType string
	version  ext.Version
	// lines は、テキストを "\n" で分けた行である。少なくとも 1 要素を持つ。
	lines []string
	// finalNewline は、書き出したときに最後の行の後に改行が付くかどうかである。
	finalNewline bool
	// invalid は、lines のうち不正な UTF-8 を含む行の数である。
	// 0 より大きい間は、拡張機能へバッファを送らない（withheld）。
	invalid int
}

// withheld は、写しが不正な UTF-8 を含み、拡張機能へ送らないバッファかどうかを返す（基本設計 4.1、5.3）。
//
// JSON の文字列は不正なバイトを表せず、encoding/json は U+FFFD（3 バイト）に置き換えて送るので、
// 送ると本体と拡張機能の複製で行頭からのバイト数がずれる。
// 送らなければ、拡張機能はそのバッファの複製を持たないので、位置が食い違うことは無い。
func (d *document) withheld() bool { return d.invalid > 0 }

// countInvalid は、lines のうち不正な UTF-8 を含む行の数を返す。
// "\n" は多バイト文字の一部にならないので、テキスト全体が正しい UTF-8 であることと、各行が正しいことは同じである。
func countInvalid(lines []string) int {
	n := 0
	for _, l := range lines {
		if !utf8.ValidString(l) {
			n++
		}
	}
	return n
}

func (d *document) openParams() ext.BufferOpenParams {
	finalNewline := d.finalNewline
	return ext.BufferOpenParams{
		Buffer:       d.id,
		Path:         d.path,
		FileType:     d.fileType,
		Version:      d.version,
		Text:         strings.Join(d.lines, "\n"),
		FinalNewline: &finalNewline,
	}
}

// Withheld は、開いているバッファ id が不正な UTF-8 を含み、拡張機能へ送っていないかどうかを返す（基本設計 4.1、5.3）。
// 開いていないバッファなら false を返す。
//
// イベントループは、送っていないバッファについて、拡張機能の表示のデータ（色、診断、印）を消して受け取らず、
// motion/resolve などの問い合わせを送らずに失敗させ、command/execute には params の Withheld を立てて送る。
func (h *Host) Withheld(id ext.BufferID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.docs[id]
	return ok && d.withheld()
}

// OpenBuffer は、バッファを開いたことを知らせる。
// 稼働中の拡張機能へ buffer/open で全文を送り、ファイルの種類の契機が成り立つ未起動の拡張機能を起動する（FR-14）。
// 起動中の拡張機能には、稼働中になった時点で全文を送る。
// 全文が不正な UTF-8 を含むなら、buffer/open を送らない（[document.withheld]。基本設計 4.1、5.3）。
// 起動の契機は、送るかどうかに依らず同じに扱う。
// 同じ ID のバッファが既に開いていれば、何もせずに [ErrBufferState] を包んだ誤りを返す。
//
// req: FR-14, FR-16
func (h *Host) OpenBuffer(s BufferSnapshot) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return ErrClosed
	}
	if _, ok := h.docs[s.ID]; ok {
		return fmt.Errorf("%w: buffer %d is already open", ErrBufferState, s.ID)
	}
	d := &document{
		id:       s.ID,
		path:     s.Path,
		fileType: s.FileType,
		version:  ext.Version(s.Version),
		lines:    strings.Split(s.Text, "\n"),

		finalNewline: !s.NoFinalNewline,
	}
	d.invalid = countInvalid(d.lines)
	h.docs[s.ID] = d
	if !d.withheld() {
		h.broadcastLocked(ext.MethodBufferOpen, d.openParams())
	}
	h.activateLocked(ext.ActivationFileType, s.FileType)
	return nil
}

// ChangeBuffer は、バッファに変更を適用したことを知らせ、稼働中の拡張機能へ buffer/change で差分を送る（FR-16）。
// c は、internal/buffer の Apply、Undo、Redo が返したものをそのまま渡す。
// 写しへの適用は、編集が触る行だけを置き換える。行の数が変わらない編集（行の中の挿入と削除）の手間は、バッファの大きさに依らない。
//
// c.Before が Host の知っているバージョン番号と一致しないか、c.Edits を写しに適用できなければ、
// 何も送らずに [ErrBufferState] を包んだ誤りを返す。
// c.Before と c.After が等しい（何も変わらなかった）なら何もしない。
//
// 不正な UTF-8 を含むバッファは拡張機能へ送らない（[document.withheld]。基本設計 4.1、5.3）ので、送るものは変更の前後で次のように変わる。
//
//   - 前後とも正しい UTF-8: buffer/change で差分を送る。ただし途中の編集の Text が不正なバイトを含むなら、buffer/open で全文を送る
//   - 変更で不正なバイトが入った: buffer/close を送り、拡張機能に複製を捨てさせる
//   - 変更で不正なバイトが無くなった: buffer/open で全文を送る
//   - 前後とも不正なバイトを含む: 何も送らない
//
// req: FR-16
func (h *Host) ChangeBuffer(id ext.BufferID, c buffer.Change) error {
	edits := make([]ext.Edit, len(c.Edits))
	for i, e := range c.Edits {
		edits[i] = toExtEdit(e)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return ErrClosed
	}
	d, ok := h.docs[id]
	if !ok {
		return fmt.Errorf("%w: buffer %d is not open", ErrBufferState, id)
	}
	if ext.Version(c.Before) != d.version {
		return fmt.Errorf("%w: buffer %d: change from version %d, host has %d", ErrBufferState, id, c.Before, d.version)
	}
	if c.Before == c.After {
		return nil
	}
	if c.After < c.Before {
		return fmt.Errorf("%w: buffer %d: version goes back from %d to %d", ErrBufferState, id, c.Before, c.After)
	}
	// 失敗しても applyEditsCounting は内容を戻すが、配列を取り替えていることがあるので、戻り値を常に写しにする。
	wasWithheld := d.withheld()
	lines, delta, err := applyEditsCounting(d.lines, edits)
	d.lines = lines
	if err != nil {
		return fmt.Errorf("%w: buffer %d: %w", ErrBufferState, id, err)
	}
	d.invalid += delta
	d.version = ext.Version(c.After)
	switch {
	case wasWithheld && d.withheld():
		// 送っていないバッファのまま。何も送らない。
	case wasWithheld:
		// 不正なバイトが無くなった。拡張機能は複製を持っていないので、全文を送る。
		h.broadcastLocked(ext.MethodBufferOpen, d.openParams())
	case d.withheld():
		// 不正なバイトが入った。拡張機能に複製を捨てさせる。
		h.broadcastLocked(ext.MethodBufferClose, ext.BufferCloseParams{Buffer: id})
	case !validEditTexts(edits):
		// 前後とも正しいが、途中の編集が不正なバイトを挿入して後の編集で消している。
		// その編集の Text は JSON で置き換わるので、差分ではなく全文を送る。
		h.broadcastLocked(ext.MethodBufferOpen, d.openParams())
	default:
		h.broadcastLocked(ext.MethodBufferChange, ext.BufferChangeParams{
			Buffer:      id,
			FromVersion: ext.Version(c.Before),
			ToVersion:   ext.Version(c.After),
			Edits:       edits,
		})
	}
	return nil
}

// validEditTexts は、編集の列のすべての Text が正しい UTF-8 かどうかを返す。
func validEditTexts(edits []ext.Edit) bool {
	for _, e := range edits {
		if !utf8.ValidString(e.Text) {
			return false
		}
	}
	return true
}

// CloseBuffer は、バッファを閉じたことを知らせ、稼働中の拡張機能へ buffer/close を送る。
// 開いていないバッファなら [ErrBufferState] を包んだ誤りを返す。
// 拡張機能へ送っていないバッファ（不正な UTF-8 を含むもの）なら、buffer/close も送らない。
func (h *Host) CloseBuffer(id ext.BufferID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return ErrClosed
	}
	d, ok := h.docs[id]
	if !ok {
		return fmt.Errorf("%w: buffer %d is not open", ErrBufferState, id)
	}
	delete(h.docs, id)
	if d.withheld() {
		return nil
	}
	h.broadcastLocked(ext.MethodBufferClose, ext.BufferCloseParams{Buffer: id})
	return nil
}

// UpdateBuffer は、開いているバッファのパスとファイルの種類が変わったことを知らせる（別名での保存、:set filetype= など）。
// 稼働中の拡張機能へ、新しいパスとファイルの種類で buffer/open を送り直す（拡張機能は持っている内容を置き換える。[ext.BufferOpenParams]）。
// 新しいファイルの種類の契機が成り立つ未起動の拡張機能を起動する（FR-14）。
// 拡張機能へ送っていないバッファ（不正な UTF-8 を含むもの）なら、記録だけ変えて buffer/open は送らない。
// 開いていないバッファなら [ErrBufferState] を包んだ誤りを返す。
//
// req: FR-14
func (h *Host) UpdateBuffer(id ext.BufferID, path, fileType string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return ErrClosed
	}
	d, ok := h.docs[id]
	if !ok {
		return fmt.Errorf("%w: buffer %d is not open", ErrBufferState, id)
	}
	if d.path == path && d.fileType == fileType {
		return nil
	}
	d.path, d.fileType = path, fileType
	if !d.withheld() {
		h.broadcastLocked(ext.MethodBufferOpen, d.openParams())
	}
	h.activateLocked(ext.ActivationFileType, fileType)
	return nil
}

// SetFinalNewline は、開いているバッファを書き出したときに最後の行の後に改行が付くかどうかが変わったことを知らせる
// （末尾の改行を補う設定を利用者が切り替えた場合など）。
// finalNewline は、BufferSnapshot の NoFinalNewline の逆で、行の無いバッファになったとき、そうでなくなったときにも変わる。
// 値が変わったなら、稼働中の拡張機能へ buffer/open を送り直す（拡張機能は持っている内容を置き換える。[ext.BufferOpenParams]）。
// 変わらなければ何もしない。拡張機能へ送っていないバッファ（不正な UTF-8 を含むもの）なら、記録だけ変えて送らない。
// 開いていないバッファなら [ErrBufferState] を包んだ誤りを返す。
func (h *Host) SetFinalNewline(id ext.BufferID, finalNewline bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return ErrClosed
	}
	d, ok := h.docs[id]
	if !ok {
		return fmt.Errorf("%w: buffer %d is not open", ErrBufferState, id)
	}
	if d.finalNewline == finalNewline {
		return nil
	}
	d.finalNewline = finalNewline
	if !d.withheld() {
		h.broadcastLocked(ext.MethodBufferOpen, d.openParams())
	}
	return nil
}

// resync は、拡張機能からの buffer/resync に応じ、そのバッファの全文を buffer/open で送り直す。
// 閉じたバッファか、拡張機能へ送っていないバッファ（不正な UTF-8 を含むもの）か、既に終わらせたプロセスからの要求なら何もしない。
// 送っていないバッファへの要求は、不正なバイトが入る前の差分の欠けで、buffer/close と行き違ったものである。
//
// req: FR-16
func (h *Host) resync(e *extension, r *run, p ext.BufferResyncParams) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e.run != r || e.state != StateRunning {
		return
	}
	d, ok := h.docs[p.Buffer]
	if !ok || d.withheld() || !e.syncsBuffers() {
		return
	}
	id := e.spec.Manifest.ID
	h.log.Info("extension requested resync", "extension", id, "buffer", p.Buffer, "extension_version", p.Version, "host_version", d.version)
	h.notifyLocked(id, r, ext.MethodBufferOpen, d.openParams())
}

// broadcastLocked は、稼働中のすべての拡張機能へ、バッファの同期の通知（buffer/open、buffer/change、buffer/close、buffer/didSave）を送る。
// 宣言ファイルの noBufferSync の拡張機能（[extension.syncsBuffers] が false）へは送らない（#367）。
// 送信は列に積むだけで待たないので、Host.mu を持ったまま呼んでよい。
// 積む順が Host.mu の中で決まるので、稼働中になったときの全文と、その後の差分の順が入れ替わることは無い。
//
// req: FR-229
func (h *Host) broadcastLocked(method string, params any) {
	for _, e := range h.order {
		if e.state == StateRunning && e.run != nil && e.syncsBuffers() {
			h.notifyLocked(e.spec.Manifest.ID, e.run, method, params)
		}
	}
}

// notifyLocked は、プロセス r の拡張機能へ通知を送る。失敗（接続の終了）は記録だけする。監視の goroutine が異常として扱う。
//
// 送る前に、送信の列に残っている量が [Config.MaxOutboxBytes] を超えていたら、送らずに r を異常とする（[ErrOutboxFull]）。
// 判断は拡張機能ごとに行うので、標準入力を読まない拡張機能が、他の拡張機能を巻き込むことは無い。
// 列が空なら上限より大きいメッセージも送るので、大きなバッファの全文で、読んでいる拡張機能を異常とすることは無い。
func (h *Host) notifyLocked(id string, r *run, method string, params any) {
	if queued := r.conn.OutboxBytes(); queued > h.cfg.MaxOutboxBytes {
		r.fail(fmt.Errorf("%w: %d bytes queued, limit %d", ErrOutboxFull, queued, h.cfg.MaxOutboxBytes))
		return
	}
	if err := r.conn.Notify(method, params); err != nil {
		h.log.Debug("notify extension failed", "extension", id, "method", method, "error", err)
	}
}

// sortedDocsLocked は、開いているバッファの写しを ID の順に返す。
func (h *Host) sortedDocsLocked() []*document {
	docs := make([]*document, 0, len(h.docs))
	for _, d := range h.docs {
		docs = append(docs, d)
	}
	slices.SortFunc(docs, func(a, b *document) int {
		switch {
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		}
		return 0
	})
	return docs
}

func toExtEdit(e buffer.Edit) ext.Edit {
	return ext.Edit{
		Range: ext.Range{
			Start: ext.Position{Line: e.Range.Start.Line, Byte: e.Range.Start.Byte},
			End:   ext.Position{Line: e.Range.End.Line, Byte: e.Range.End.Byte},
		},
		Text: e.Text,
	}
}

// applyEdits は、行の配列 lines に、逐次の数え方の編集の列を先頭から順に適用した結果を返す。
// 誤りの扱いと lines の配列の扱いは [applyEditsCounting] と同じである。
func applyEdits(lines []string, edits []ext.Edit) ([]string, error) {
	lines, _, err := applyEditsCounting(lines, edits)
	return lines, err
}

// applyEditsCounting は、行の配列 lines に、逐次の数え方の編集の列を先頭から順に適用した結果と、
// 不正な UTF-8 を含む行の数の増減を返す。
// lines の配列をその場で書き換える（返す値は lines と同じ配列を指すことがある）。
// 編集ごとに、触る行だけを置き換えるので、手間は触る行の数と、行の数が変わるときの後ろの行の移動に比例する。
// 増減も、置き換えた行と置き換える前の行だけを調べて数える。
//
// 位置が行の外を指すか、終わりが始まりより前にあれば、それまでの編集を戻して誤りを返す。
// このとき lines の内容は呼んだ前と同じで、増減は 0 である。
// UTF-8 の文字の途中かどうかは検査しない（写しに送る変更は、本体のバッファが検査済みである）。
func applyEditsCounting(lines []string, edits []ext.Edit) ([]string, int, error) {
	type undo struct {
		at, n int
		old   []string
	}
	var undos []undo
	delta := 0
	for i, e := range edits {
		s, end := e.Range.Start, e.Range.End
		var err error
		switch {
		case !validPos(lines, s) || !validPos(lines, end):
			err = fmt.Errorf("edit %d: position out of range", i)
		case end.Line < s.Line || (end.Line == s.Line && end.Byte < s.Byte):
			err = fmt.Errorf("edit %d: range end before start", i)
		}
		if err != nil {
			for k := len(undos) - 1; k >= 0; k-- {
				u := undos[k]
				lines = slices.Replace(lines, u.at, u.at+u.n, u.old...)
			}
			return lines, 0, err
		}
		joined := lines[s.Line][:s.Byte] + e.Text + lines[end.Line][end.Byte:]
		repl := strings.Split(joined, "\n")
		old := slices.Clone(lines[s.Line : end.Line+1])
		delta += countInvalid(repl) - countInvalid(old)
		undos = append(undos, undo{at: s.Line, n: len(repl), old: old})
		lines = slices.Replace(lines, s.Line, end.Line+1, repl...)
	}
	return lines, delta, nil
}

func validPos(lines []string, p ext.Position) bool {
	return p.Line >= 0 && p.Line < len(lines) && p.Byte >= 0 && p.Byte <= len(lines[p.Line])
}
