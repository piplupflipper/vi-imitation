// Package buffer は、テキストの保持、編集の適用、バージョン番号、アンドゥの履歴を受け持つ（基本設計 2 章、4 章）。
//
// 基本設計 2 章の土台の層にあり、このリポジトリの他のパッケージを import しない。
// カーソル、モード、画面は持たない。
//
// Buffer は並行に呼ばれる前提を置かない。
// 編集の状態はイベントループの goroutine 1 つだけが触る（基本設計 2.1）ので、排他制御をしない。
//
// # 行の数え方
//
// テキストは UTF-8 で、改行は LF を前提にする。
// 行の数え方は Vim に合わせ、改行は行の終わりを表すものとして数える。
// このため "a\nb\n" と "a\nb" はどちらも "a" と "b" の 2 行で、違いは末尾の改行の有無（HasTrailingNewline）だけである。
// 空のファイル "" と "\n" は、どちらも空の行 1 つである。
//
// # 末尾の改行の書き出し
//
// 末尾の改行の扱いは、Vim 9.1 の 'endofline' と 'fixendofline' に合わせる（基本設計 4.1）。
// 末尾の改行の有無は読み込み時に記録する（HasTrailingNewline。Vim の 'endofline'）。
// 書き出し時（WriteTo、WriteFile）は、記録した改行があるか、末尾の改行を補う設定（SetFixEndOfLine。Vim の 'fixendofline'）が有効なら、最後の行の後に改行を付ける。
// 設定の既定は有効で、末尾の改行の無いファイルを開いて保存すると改行が付く。
// 無効にすると、読み込んだときの末尾の改行の有無を復元する。
//
// Vim の「行の無いバッファ」（NoLines）は、設定によらず空のファイルに書き出す。
// 空のファイル "" から作り、編集する前（アンドゥで作った時点に戻った場合を含む）のバッファは、行の無いバッファである。
// 編集した後は、末尾の改行があるものとして書き出す（Vim は空のファイルの 'endofline' を有効のままにする）。
// 行単位の削除で全ての行を消したバッファも、Vim では行の無いバッファになる。
// Buffer は行の削除と行の中の削除をテキストから見分けられないので、編集した側が MarkNoLines で知らせる。
//
// 末尾の改行の記録は読み込み時に決まるが、外から書き換えたファイルを読み直したのと同じ状態にするために、
// SetTrailingNewline、MarkNoLines、MarkHasLines で後から変えられる（拡張機能の buffer/edit の endOfLine と noLines）。
// バージョン番号はテキストの変更だけを数え、これらの状態の変更では進まない。
// このため、テキストを変えずにこれらだけを変えると、バージョン番号が同じまま書き出す内容が変わる（"\n" が "" になる、など）。
// 拡張機能の buffer/edit でこれらだけを変えたときは、result の Applied が false になる。
//
// 位置と編集（Position、Edit）は、行を "\n" でつないだテキスト（Text が返すもの）に対して数える。
// 末尾の改行はこのテキストに含まれず、編集で消したり足したりすることはできない。
// CR（"\r"）は特別に扱わず、行の中の 1 バイトとして保持する。
package buffer

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// Buffer は、1 つのテキストと、そのバージョン番号とアンドゥの履歴を保持する。
// ゼロ値は使わず、New か Read で作る。
type Buffer struct {
	// lines は少なくとも 1 要素を持つ。
	// テキストの保持の方法は外に見せない（基本設計 4.3）ので、構造を取り替えても公開の操作は変えない。
	lines []string
	// eol は、読み込んだテキストが末尾の改行を持っていたかどうか（Vim の 'endofline'）である。空のテキストでは true にする。
	eol bool
	// fixEOL は、書き出すときに末尾の改行を補うかどうか（Vim の 'fixendofline'）である。
	fixEOL bool
	// noLinesAt は、行の無いバッファ（NoLines）であるアンドゥの履歴の位置（stateID）の集合である。
	// 空のテキストから作ったバッファでは、作った時点の位置 0 を含む。
	// 位置で覚えるので、アンドゥとリドゥでその位置へ戻れば、また行の無いバッファになる。
	noLinesAt map[uint64]bool
	version   uint64
	undo      []step
	redo      []step

	// groupDepth は、BeginUndoGroup の入れ子の深さである。
	groupDepth int
	// groupStarted は、開いているグループの最初の Apply が済み、undo の末尾がそのグループの単位であることを表す。
	groupStarted bool

	// nextID は、次に作る、またはまとめ直したアンドゥの単位に付ける識別子である。
	nextID uint64
	// savedID は、MarkSaved を呼んだ時点の stateID である。
	savedID uint64

	// lastUndo は、直前の Undo か Redo の情報である（LastUndo）。
	lastUndo UndoInfo

	// readOnly は、編集を禁止しているかである（SetReadOnly。FR-85）。
	readOnly bool
}

// step は、アンドゥとリドゥの 1 単位である。
// inverse は edits の各編集を取り消す逆の編集を、edits と同じ順（適用した順）に並べたものである。
// inverse を末尾から先頭へ逆順に適用すると、edits を適用する前のテキストに戻る。
// 逆順で持たないのは、グループの中の Apply をまとめるときに末尾へ足すだけで済ませるためである（#29）。
// 先頭へ足す形にすると、まとめるたびに全体を写し直し、グループの中の Apply の回数の 2 乗に比例して遅くなる。
// id は、この単位を適用し終えた状態を見分けるための、履歴の中で一意な番号（1 以上）である。
// cursor は、SetUndoCursor で記録した、この単位の変更の前のカーソルの位置である。hasCursor が false なら記録が無い。
type step struct {
	id      uint64
	edits   []Edit
	inverse []Edit

	cursor    Position
	hasCursor bool

	// eolSet は、この単位が末尾の改行の記録（eol）も変えたことを表す（ApplyReload）。
	// アンドゥで eolBefore に、リドゥで eolAfter に戻す。
	eolSet    bool
	eolBefore bool
	eolAfter  bool
}

// New は、text を内容とするバッファを、バージョン番号 0 で作る。
// text の末尾に改行があれば、それを末尾の改行として記録し、行には含めない。
// text が空なら、末尾の改行があるものとして記録する（パッケージの doc の「末尾の改行の書き出し」）。
// 末尾の改行を補う設定（SetFixEndOfLine）は有効にする。
// 作った直後のバッファは、保存済み（Modified が false）として扱う。
func New(text string) *Buffer {
	empty := text == ""
	eol := empty || strings.HasSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\n")
	b := &Buffer{lines: strings.Split(text, "\n"), eol: eol, fixEOL: true}
	if empty {
		b.noLinesAt = map[uint64]bool{0: true}
	}
	return b
}

// Read は、r の内容を全て読み、それを内容とするバッファを New と同じ規則で作る。
func Read(r io.Reader) (*Buffer, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("buffer: read: %w", err)
	}
	return New(string(data)), nil
}

// ReadFile は、path のファイルを読み、それを内容とするバッファを作る。
func ReadFile(path string) (*Buffer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("buffer: %w", err)
	}
	defer f.Close() //nolint:errcheck // 読み込みだけなので、閉じるときの失敗は内容に影響しない
	return Read(f)
}

// WriteTo は、バッファの内容を w へ書き出す。
// 最後の行の後には、FinalNewline が true なら改行を付ける。
// ただし行の無いバッファ（NoLines）では、何も書かない（パッケージの doc の「末尾の改行の書き出し」）。
// 書き出しはバージョン番号、アンドゥの履歴、保存済みの記録を変えない。
func (b *Buffer) WriteTo(w io.Writer) (int64, error) {
	var data string
	if !b.NoLines() {
		data = b.Text()
		if b.FinalNewline() {
			data += "\n"
		}
	}
	n, err := io.WriteString(w, data)
	if err != nil {
		return int64(n), fmt.Errorf("buffer: write: %w", err)
	}
	return int64(n), nil
}

// WriteFile は、バッファの内容を path のファイルへ書き出す。
//
// ファイルが既にあれば、同じディレクトリの一時ファイルへ書いてから rename で置き換えるので、途中で失敗しても元のファイルは壊れない。
// 一時ファイルには元のファイルの許可属性を付ける。
// path がシンボリックリンクなら、リンクの指す先のファイルを置き換える。
// 置き換えたファイルは新しいファイルになるので、ハードリンクは切れ、所有者は書き出したプロセスの利用者になる。
//
// ファイルが無ければ、許可属性 0644 に umask を適用して新しく作り、直接書き込む。守る元の内容が無いので、rename は使わない。
//
// WriteFile は保存済みの記録を変えない。編集中のファイルを保存したときは、呼び出し側が MarkSaved を呼ぶ。
func (b *Buffer) WriteFile(path string) error {
	target, perm, exists, err := writeTarget(path)
	if err != nil {
		return err
	}
	if !exists {
		return b.createFile(path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp*")
	if err != nil {
		return fmt.Errorf("buffer: %w", err)
	}
	tmpName := tmp.Name()
	if err := writeAndClose(tmp, b, perm); err != nil {
		_ = os.Remove(tmpName) // 一時ファイルの後始末に失敗しても、元のエラーを返すほうが大事である
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName) // 同上
		return fmt.Errorf("buffer: %w", err)
	}
	return nil
}

// writeTarget は、WriteFile が置き換えるファイルのパス（シンボリックリンクを解決したもの）と、その許可属性を返す。
// ファイルが無ければ exists を false にして返す。それ以外の Stat の失敗はエラーとして返す。
func writeTarget(path string) (target string, perm fs.FileMode, exists bool, err error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		target, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", 0, false, fmt.Errorf("buffer: %w", err)
		}
		return target, info.Mode().Perm(), true, nil
	case errors.Is(err, fs.ErrNotExist):
		return "", 0, false, nil
	default:
		return "", 0, false, fmt.Errorf("buffer: %w", err)
	}
}

// createFile は、path に新しいファイルを作って内容を書く。既にあれば失敗する。
func (b *Buffer) createFile(path string) error {
	//nolint:gosec // G302: 利用者が編集するテキストファイルなので、一般的なエディタと同じく 0644 に umask を適用した許可属性で作る
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("buffer: %w", err)
	}
	return writeAndClose(f, b, 0)
}

// writeAndClose は、f にバッファの内容を書き、perm が 0 でなければその許可属性を付け、ディスクへ同期してから閉じる。
func writeAndClose(f *os.File, b *Buffer, perm fs.FileMode) error {
	_, err := b.WriteTo(f)
	if err == nil && perm != 0 {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("buffer: %w", err)
	}
	return nil
}

// Version は、現在のバージョン番号を返す。
// 番号は、編集の適用、アンドゥ、リドゥのたびに 1 増え、減ることは無い。
func (b *Buffer) Version() uint64 {
	return b.version
}

// LineCount は、行の数を返す。末尾の改行は行として数えない。空のバッファでも 1 以上である。
func (b *Buffer) LineCount() int {
	return len(b.lines)
}

// Line は、i 番目（0 始まり）の行を、改行を含まずに返す。
// i が 0 以上 LineCount() 未満でなければ panic する。
func (b *Buffer) Line(i int) string {
	return b.lines[i]
}

// HasTrailingNewline は、読み込んだテキストの最後の行の後に改行があったかどうか（Vim の 'endofline'）を返す。
// 空のテキストから作ったバッファでは true を返す。
// 書き出すときに改行を付けるかどうかは FinalNewline が返す。
func (b *Buffer) HasTrailingNewline() bool {
	return b.eol
}

// SetTrailingNewline は、読み込んだテキストの最後の行の後に改行があったかどうか（HasTrailingNewline。Vim の 'endofline'）を設定する。
// テキストを変えずに、ファイルを読み直したのと同じ末尾の改行の記録にするときに呼ぶ（拡張機能の buffer/edit の endOfLine）。
// 設定はアンドゥの履歴に記録しない（Vim の 'endofline' も、アンドゥで戻らない）。
// 設定はバージョン番号、アンドゥの履歴、保存済みの記録を変えない。
func (b *Buffer) SetTrailingNewline(on bool) {
	b.eol = on
}

// SetFixEndOfLine は、書き出すときに末尾の改行を補うかどうか（Vim の 'fixendofline'）を設定する。
// New で作ったバッファでは有効である。
// 設定はバージョン番号、アンドゥの履歴、保存済みの記録を変えない。
func (b *Buffer) SetFixEndOfLine(on bool) {
	b.fixEOL = on
}

// FixEndOfLine は、書き出すときに末尾の改行を補うかどうか（SetFixEndOfLine で設定したもの）を返す。
func (b *Buffer) FixEndOfLine() bool {
	return b.fixEOL
}

// FinalNewline は、書き出すときに最後の行の後に改行を付けるかどうかを返す。
// 読み込んだテキストが末尾の改行を持っていたか、末尾の改行を補う設定が有効なら true である。
// 行の無いバッファ（NoLines）では、true を返しても WriteTo は何も書かない。
// 拡張機能へ送る ext.BufferOpenParams の FinalNewline は、行の無いバッファ（NoLines）では false、それ以外ではこの値にする。
func (b *Buffer) FinalNewline() bool {
	return b.eol || b.fixEOL
}

// MarkNoLines は、現在の内容を、Vim の「行の無いバッファ」（--No lines in buffer--）として記録する。
// 行単位の削除（dd、:d など）で全ての行を消した直後に、編集した側が呼ぶ。
// Buffer は、行の削除で全ての行を消したことと、行の中を消して空の行 1 つにしたことを、テキストから見分けられないためである。
// テキストが空でなければ何もしない。
//
// 記録はアンドゥの履歴の現在の位置に付く。この後に編集すると外れ、アンドゥやリドゥでこの位置へ戻ると、また付く。
// MarkNoLines はバージョン番号、アンドゥの履歴、保存済みの記録を変えない。
func (b *Buffer) MarkNoLines() {
	if !b.emptyText() {
		return
	}
	if b.noLinesAt == nil {
		b.noLinesAt = map[uint64]bool{}
	}
	b.noLinesAt[b.stateID()] = true
}

// MarkHasLines は、現在の内容に付いた行の無いバッファの記録（MarkNoLines、空のテキストから作ったこと）を外す。
// 読み直したファイルが空の行 1 つ（"\n"）だった場合のように、テキストを変えずに行の無いバッファでなくする（Vim の :e）ときに呼ぶ。
// MarkHasLines はバージョン番号、アンドゥの履歴、保存済みの記録を変えない。
func (b *Buffer) MarkHasLines() {
	delete(b.noLinesAt, b.stateID())
}

// NoLines は、Vim の「行の無いバッファ」に当たるかどうかを返す。
// 空のテキストから作り、作った時点の内容のままであるか、MarkNoLines で記録した内容のままなら true である。
// 行の無いバッファは、書き出すと空のファイルになる（パッケージの doc の「末尾の改行の書き出し」）。
// 表示と位置の数え方は、空の行 1 つのバッファと同じである。
func (b *Buffer) NoLines() bool {
	return b.noLinesAt[b.stateID()] && b.emptyText()
}

// emptyText は、テキストが空（空の行 1 つ）かどうかを返す。
func (b *Buffer) emptyText() bool {
	return len(b.lines) == 1 && b.lines[0] == ""
}

// Text は、行を "\n" でつないだテキストを返す。末尾の改行は含まない。
// 位置と編集はこのテキストに対して数える。ファイルとしての内容は WriteTo で得る。
func (b *Buffer) Text() string {
	return strings.Join(b.lines, "\n")
}

// Slice は、範囲 r のテキストを返す（基本設計 4.3 の「範囲を読む」）。
// 返すテキストは行と別に確保した文字列なので、受け取った側が持ち続けても、バッファの行を保持しない（slice）。
// 範囲がバッファの外にあるか、文字の途中を指す場合は ErrInvalidPosition を、
// 終わりが始まりより前にある場合は ErrInvalidRange を返す。
func (b *Buffer) Slice(r Range) (string, error) {
	if err := b.checkRange(r); err != nil {
		return "", err
	}
	return b.slice(r), nil
}

// Apply は、トランザクションを適用し、適用した変更を返す。
//
// tx.BaseVersion が現在のバージョン番号と一致しなければ、何も変えずに ErrVersionMismatch を返す（FR-15）。
// 編集の位置が正しくない場合も、何も変えずに ErrInvalidPosition か ErrInvalidRange を返す。
// 途中の編集で失敗しても、それより前の編集は取り消され、トランザクションは全体として適用されないか、全て適用されるかのどちらかになる。
//
// 適用に成功すると、バージョン番号を 1 増やし、リドゥの履歴を捨てる。
// トランザクションはアンドゥの 1 単位として記録する。
// ただし BeginUndoGroup でグループを開いている間は、グループの中の 2 回目以降の Apply を、最初の Apply の単位にまとめる。
// Edits が空のトランザクションは、バージョン番号が一致していれば何もせず、Before と After が等しい Change を返す。
//
// 編集を禁止したバッファ（ReadOnly）では、Edits が空でなければ、何も変えずに ErrReadOnly を返す（FR-85、FR-118）。
// Apply は、利用者の操作と拡張機能の編集が通る入り口なので、ここで断れば操作ごとの判定の漏れが無くなる（基本設計 4.6）。
// バージョン番号の不一致は、禁止より先に判定する（拡張機能が古い版で問い合わせ直す手順を変えない）。
//
// req: FR-07, FR-15, FR-16, FR-85, FR-118
func (b *Buffer) Apply(tx Transaction) (Change, error) {
	if tx.BaseVersion != b.version {
		return Change{}, fmt.Errorf("%w: base %d, current %d", ErrVersionMismatch, tx.BaseVersion, b.version)
	}
	if len(tx.Edits) == 0 {
		return Change{Before: b.version, After: b.version}, nil
	}
	if b.readOnly {
		return Change{}, ErrReadOnly
	}
	edits := slices.Clone(tx.Edits)
	inverse, err := b.applyAll(edits)
	if err != nil {
		return Change{}, err
	}
	b.record(edits, inverse)
	b.redo = nil
	return b.commit(edits), nil
}

// ApplyReload は、ファイルを読み直したときの変更（Vim の :e）を、アンドゥの 1 単位として適用する。
// Apply と同じく tx を適用し、加えて末尾の改行の記録（HasTrailingNewline）を eol にする。
// 末尾の改行の記録の変更も同じ単位に記録するので、アンドゥで読み直す前の記録へ、リドゥで eol へ戻る（Vim 9.1 で、
// 末尾の改行の有無が違うファイルを読み直した後の u と同じ）。
//
// tx.Edits が空でも、末尾の改行の記録が変わるなら、編集の無い単位を記録してバージョン番号を 1 増やす。
// 編集も記録の変化も無ければ、Apply と同じく何もしない。
// 開いているアンドゥのグループは閉じる（読み直しを利用者の編集の単位にまとめない）。
//
// 編集を禁止したバッファ（ReadOnly）でも断らない。ApplyReload は、本体がファイルの中身へ合わせ直す入り口で、利用者の編集ではない（基本設計 4.6）。
func (b *Buffer) ApplyReload(tx Transaction, eol bool) (Change, error) {
	if tx.BaseVersion != b.version {
		return Change{}, fmt.Errorf("%w: base %d, current %d", ErrVersionMismatch, tx.BaseVersion, b.version)
	}
	if len(tx.Edits) == 0 && eol == b.eol {
		return Change{Before: b.version, After: b.version}, nil
	}
	b.closeUndoGroup()
	edits := slices.Clone(tx.Edits)
	inverse, err := b.applyAll(edits)
	if err != nil {
		return Change{}, err
	}
	b.record(edits, inverse)
	s := &b.undo[len(b.undo)-1]
	s.eolSet, s.eolBefore, s.eolAfter = true, b.eol, eol
	b.eol = eol
	b.redo = nil
	return b.commit(edits), nil
}

// ApplyUnrecorded は、tx をアンドゥの履歴に記録せずに適用する。
// 本体が中身を持つファイルでないバッファ（表示の部品のテキスト）の末尾へ書き足し続ける入り口である（view/append。FR-272）。
// ApplyReload で書き足すと、書き足したテキストと捨てた行がアンドゥの履歴に残り続け、出力の量に比例してメモリが増えるためである。
//
// 適用した後は、履歴の編集の位置がテキストと合わなくなるので、アンドゥとリドゥの履歴と、行の無いバッファの記録（MarkNoLines）を捨てる。
// 開いているアンドゥのグループは閉じる。Modified は呼ぶ前の値を保つ。
// Apply と同じくバージョン番号の一致を確かめ、適用に成功するとバージョン番号を 1 増やす。編集を禁止したバッファ（ReadOnly）でも断らない
// （ApplyReload と同じく、利用者の編集ではないため）。Edits が空なら何もしない。
func (b *Buffer) ApplyUnrecorded(tx Transaction) (Change, error) {
	if tx.BaseVersion != b.version {
		return Change{}, fmt.Errorf("%w: base %d, current %d", ErrVersionMismatch, tx.BaseVersion, b.version)
	}
	if len(tx.Edits) == 0 {
		return Change{Before: b.version, After: b.version}, nil
	}
	edits := slices.Clone(tx.Edits)
	if _, err := b.applyAll(edits); err != nil {
		return Change{}, err
	}
	modified := b.Modified()
	b.closeUndoGroup()
	b.undo, b.redo, b.noLinesAt = nil, nil, nil
	b.savedID = 0 // stateID は 0 になる
	if modified {
		b.savedID = unsavedID
	}
	return b.commit(edits), nil
}

// record は、適用した編集をアンドゥの履歴に記録する。
// グループの中の 2 回目以降なら末尾の単位にまとめ、まとめた単位には新しい id を付ける。
// id を付け直すのは、まとめる前の状態で MarkSaved した場合に、まとめた後を変更ありと判定するためである。
func (b *Buffer) record(edits, inverse []Edit) {
	b.nextID++
	if b.groupDepth > 0 && b.groupStarted {
		last := &b.undo[len(b.undo)-1]
		last.id = b.nextID
		last.edits = append(last.edits, edits...)
		last.inverse = append(last.inverse, inverse...)
		return
	}
	b.undo = append(b.undo, step{id: b.nextID, edits: edits, inverse: inverse})
	if b.groupDepth > 0 {
		b.groupStarted = true
	}
}

// BeginUndoGroup は、アンドゥのグループを開く。
// EndUndoGroup で閉じるまでの Apply は、アンドゥとリドゥで 1 つの単位として扱う（FR-07）。
// インサートモードで 1 文字ごとに Apply し、モードを抜けるまでを 1 回のアンドゥで戻す、といった使い方を想定する。
//
// グループは入れ子にでき、最も外側の EndUndoGroup で閉じる。
// グループの中でも、バージョン番号と Change は Apply ごとに進み、返る。
// グループを開いている間に Undo か Redo を呼ぶと、グループは閉じる。
//
// req: FR-07
func (b *Buffer) BeginUndoGroup() {
	b.groupDepth++
}

// EndUndoGroup は、BeginUndoGroup で開いたグループを 1 段閉じる。
// 開いているグループが無ければ何もしない。
//
// req: FR-07
func (b *Buffer) EndUndoGroup() {
	if b.groupDepth == 0 {
		return
	}
	b.groupDepth--
	if b.groupDepth == 0 {
		b.groupStarted = false
	}
}

// UndoTip は、アンドゥの履歴の末尾の単位の識別子を返す。履歴が空なら 0 である。
// 末尾の単位に編集をまとめるたびに識別子は変わるので、ある時点の後に末尾の単位が変わっていないかを比べるのに使う（JoinUndoGroup）。
func (b *Buffer) UndoTip() uint64 { return b.stateID() }

// JoinUndoGroup は、BeginUndoGroup と同じくグループを開く。加えて、グループを開いていない状態から開き、
// アンドゥの履歴の末尾の単位の識別子が tip（0 でないもの）と同じなら、このグループの編集を末尾の単位にまとめ、true を返す
// （Vim の :undojoin に当たる）。違えば BeginUndoGroup と同じく新しい単位を作り、false を返す。
// 閉じるのは EndUndoGroup である。
//
// 閉じたグループの続きを、間に入った他の操作で末尾が変わっていないときだけ同じ単位にするためのもので、
// 拡張機能の結果を待つために止めたマクロの再生を続けるときに使う（internal/command の ResumeReplay）。
//
// req: FR-07
func (b *Buffer) JoinUndoGroup(tip uint64) bool {
	b.groupDepth++
	if b.groupDepth != 1 || tip == 0 || b.stateID() != tip {
		return false
	}
	b.groupStarted = true
	return true
}

// closeUndoGroup は、開いているグループを全て閉じる。
func (b *Buffer) closeUndoGroup() {
	b.groupDepth = 0
	b.groupStarted = false
}

// MarkSaved は、現在の内容を保存した内容として記録する。
// 以後、編集、アンドゥ、リドゥでこの内容に戻ったときは、Modified が false を返す。
func (b *Buffer) MarkSaved() {
	b.savedID = b.stateID()
}

// unsavedID は、MarkUnsaved が savedID に入れる値である。アンドゥの単位の識別子は 1 から数えるので、どの stateID とも等しくならない。
const unsavedID = ^uint64(0)

// MarkUnsaved は、現在の内容を、保存していない変更がある内容として記録する。
// 以後、次に MarkSaved を呼ぶまで、編集、アンドゥ、リドゥでどの内容になっても Modified は true を返す。
// アンドゥの履歴には何も足さないので、作った直後に呼べば、アンドゥで取り消せる変更は無いまま Modified が true になる。
// 標準入力から読んだバッファ（FR-158）のように、ファイルに保存していない中身で作ったバッファに使う
// （Vim が - で読んだバッファに changed() を呼ぶのと同じ）。
func (b *Buffer) MarkUnsaved() {
	b.savedID = unsavedID
}

// Modified は、現在の内容が、最後に MarkSaved した時点（呼んでいなければ作った時点）の内容から変わっているかどうかを返す。
//
// 判定はアンドゥの履歴の位置で行う。
// 保存した後に編集し、アンドゥで保存した時点まで戻れば false になる。
// 保存した時点より前へアンドゥしてから別の編集をすると、保存した時点へは戻れなくなるので、次に MarkSaved するまで true になる。
// 別々の編集の結果がたまたま保存した内容と同じテキストになっても、true を返す。
func (b *Buffer) Modified() bool {
	return b.stateID() != b.savedID
}

// stateID は、アンドゥの履歴の現在の位置を表す番号を返す。履歴が空なら 0 である。
func (b *Buffer) stateID() uint64 {
	if len(b.undo) == 0 {
		return 0
	}
	return b.undo[len(b.undo)-1].id
}

// CanUndo は、アンドゥできる単位が残っているかどうかを返す。
func (b *Buffer) CanUndo() bool {
	return len(b.undo) > 0
}

// CanRedo は、リドゥできる単位が残っているかどうかを返す。
func (b *Buffer) CanRedo() bool {
	return len(b.redo) > 0
}

// Undo は、アンドゥの単位を 1 つ取り消し、その変更を返す（FR-07）。
// 取り消しも変更の 1 つとして扱い、バージョン番号を 1 増やす。
// 取り消せるものが無ければ、何もせずに false を返す。
// 開いているアンドゥのグループは閉じる。
// 取り消した単位のカーソルの記録と変わった行の範囲は、LastUndo で読める。
// 編集を禁止したバッファ（ReadOnly）では、何もせずに false を返す（Vim の 'modifiable' が切れたときの u と同じく、変更として断る）。
// 呼び出し側は、取り消すものが無いこととの違いを ReadOnly で見分ける。
//
// req: FR-07, FR-16, FR-85
func (b *Buffer) Undo() (Change, bool) {
	b.closeUndoGroup()
	if len(b.undo) == 0 || b.readOnly {
		return Change{}, false
	}
	s := b.undo[len(b.undo)-1]
	b.undo = b.undo[:len(b.undo)-1]
	undoEdits := slices.Clone(s.inverse)
	slices.Reverse(undoEdits)
	b.applyTracked(undoEdits, s)
	if s.eolSet {
		b.eol = s.eolBefore
	}
	b.redo = append(b.redo, s)
	return b.commit(undoEdits), true
}

// Redo は、最後に取り消した単位を 1 つやり直し、その変更を返す（FR-07）。
// やり直しも変更の 1 つとして扱い、バージョン番号を 1 増やす。
// やり直せるものが無ければ、何もせずに false を返す。
// 開いているアンドゥのグループは閉じる。
// やり直した単位のカーソルの記録と変わった行の範囲は、LastUndo で読める。
// 編集を禁止したバッファ（ReadOnly）では、Undo と同じく何もせずに false を返す。
//
// req: FR-07, FR-16, FR-85
func (b *Buffer) Redo() (Change, bool) {
	b.closeUndoGroup()
	if len(b.redo) == 0 || b.readOnly {
		return Change{}, false
	}
	s := b.redo[len(b.redo)-1]
	b.redo = b.redo[:len(b.redo)-1]
	b.applyTracked(s.edits, s)
	if s.eolSet {
		b.eol = s.eolAfter
	}
	b.undo = append(b.undo, s)
	return b.commit(s.edits), true
}

// commit は、バージョン番号を 1 増やし、edits を適用した変更の記録を返す。
// 呼び出し側が記録を書き換えても履歴に影響しないように、edits を複製する。
func (b *Buffer) commit(edits []Edit) Change {
	before := b.version
	b.version++
	return Change{Before: before, After: b.version, Edits: slices.Clone(edits)}
}

// applyAll は、edits を順に適用し、各編集を取り消す逆の編集を edits と同じ順に並べて返す。
// 適用前に戻すには、返した列を逆順に適用する。
// 途中で失敗した場合は、それまでの編集を取り消してからエラーを返す。
func (b *Buffer) applyAll(edits []Edit) ([]Edit, error) {
	inverse := make([]Edit, 0, len(edits))
	for i, e := range edits {
		inv, err := b.apply(e)
		if err != nil {
			slices.Reverse(inverse)
			b.mustApplyAll(inverse)
			return nil, fmt.Errorf("edit %d: %w", i, err)
		}
		inverse = append(inverse, inv)
	}
	return inverse, nil
}

// mustApplyAll は、適用できることが分かっている編集の列（履歴に記録した列）を順に適用する。
// 失敗した場合は履歴とテキストが食い違っているので、panic する。
func (b *Buffer) mustApplyAll(edits []Edit) {
	for _, e := range edits {
		if _, err := b.apply(e); err != nil {
			panic(fmt.Sprintf("buffer: history is inconsistent with text: %v", err))
		}
	}
}

// apply は、1 つの編集を適用し、それを取り消す逆の編集を返す。
// 位置が正しくなければ、何も変えずにエラーを返す。
//
// 行の部分文字列は、元の行の全体を指したまま残る（空の部分文字列でも、元の行を指すことがある）。
// 部分文字列を、作り直した後の別の行や、行の外（アンドゥの記録）に残すと、編集の前の行の全体を保持し続ける。
// 長い行を 1 文字ずつ変えると、行の長さと編集の数の積のメモリが残り、100000afoo xy <Esc> で server のメモリが 13GB を超えた（#279）。
// このため次のようにする。
//
//   - 逆の編集のテキスト（消したテキスト）は、写した文字列にする（slice）
//   - 編集で行が 2 行以上に分かれるときは、最初の行と最後の行を、それぞれ新しく確保した文字列にする（concat）。
//     前の行と、入れたテキストの最初と最後の行をつないで 1 つの文字列にしてから分けると、分けた行のどれもがつないだ全体を指す。
//     その後で一方の行を作り直しても、もう一方（短い行や空の行）が全体を保持し続ける（PR #285 のレビュー）。
//     Go の文字列の連結は、片方が空なら、もう片方をそのまま返すので、+ では新しく確保されないことがある
//   - 1 行に収まる編集は、+ でつなぐ。結果は前の行の部分文字列になりうるが、前の行はその行で置き換えて手放すので、保持する量は増えない
//
// 間の行は e.Text の部分文字列になる。e.Text は呼び出し側が作った文字列で、バッファの行を指さない。
func (b *Buffer) apply(e Edit) (Edit, error) {
	if err := b.checkRange(e.Range); err != nil {
		return Edit{}, err
	}
	start, end := e.Range.Start, e.Range.End
	removed := b.slice(e.Range)

	head, tail := b.lines[start.Line][:start.Byte], b.lines[end.Line][end.Byte:]
	lines := strings.Split(e.Text, "\n")
	if n := len(lines); n == 1 {
		lines[0] = head + e.Text + tail
	} else {
		lines[0] = concat(head, lines[0])
		lines[n-1] = concat(lines[n-1], tail)
	}
	b.lines = slices.Replace(b.lines, start.Line, end.Line+1, lines...)

	return Edit{Range: Range{Start: start, End: endOf(start, e.Text)}, Text: removed}, nil
}

// concat は、a と b をつないだ文字列を、新しく確保して返す（apply）。
// a か b が空でも、もう一方をそのまま返さずに写す。結果が空なら、どの文字列も指さない "" を返す。
func concat(a, b string) string {
	var sb strings.Builder
	sb.Grow(len(a) + len(b))
	sb.WriteString(a)
	sb.WriteString(b)
	return sb.String()
}

// endOf は、start に text を挿入したとき、挿入したテキストの直後に当たる位置を返す。
func endOf(start Position, text string) Position {
	n := strings.Count(text, "\n")
	if n == 0 {
		return Position{Line: start.Line, Byte: start.Byte + len(text)}
	}
	return Position{Line: start.Line + n, Byte: len(text) - strings.LastIndexByte(text, '\n') - 1}
}

// slice は、正しいことを確かめた範囲 r のテキストを、行と別に確保した文字列で返す。
// 行の部分文字列を返すと、受け取った側（アンドゥの記録、ヤンクしたレジスターの履歴）が元の行の全体を保持する。
// 長い行から 1 文字をヤンクしても、履歴の 1 件の上限（FR-53。テキストの長さで比べる）に掛からず、件数 × 行の長さまで残った（PR #285 のレビュー）。
// 空の範囲は、どの文字列も指さない "" を返す。
func (b *Buffer) slice(r Range) string {
	s, e := r.Start, r.End
	if s.Line == e.Line {
		return strings.Clone(b.lines[s.Line][s.Byte:e.Byte])
	}
	var sb strings.Builder
	sb.WriteString(b.lines[s.Line][s.Byte:])
	for _, l := range b.lines[s.Line+1 : e.Line] {
		sb.WriteByte('\n')
		sb.WriteString(l)
	}
	sb.WriteByte('\n')
	sb.WriteString(b.lines[e.Line][:e.Byte])
	return sb.String()
}

// checkRange は、範囲の両端がバッファの中の文字の境界を指し、終わりが始まりより前に無いことを確かめる。
func (b *Buffer) checkRange(r Range) error {
	if err := b.checkPosition(r.Start); err != nil {
		return err
	}
	if err := b.checkPosition(r.End); err != nil {
		return err
	}
	if r.End.Less(r.Start) {
		return fmt.Errorf("%w: end %v is before start %v", ErrInvalidRange, r.End, r.Start)
	}
	return nil
}

// checkPosition は、p がバッファの中の文字の境界（行末を含む）を指すことを確かめる。
// 文字の境界の定義は Position の doc コメントにある。
func (b *Buffer) checkPosition(p Position) error {
	if p.Line < 0 || p.Line >= len(b.lines) {
		return fmt.Errorf("%w: %v: line out of range [0, %d)", ErrInvalidPosition, p, len(b.lines))
	}
	line := b.lines[p.Line]
	if p.Byte < 0 || p.Byte > len(line) {
		return fmt.Errorf("%w: %v: byte out of range [0, %d]", ErrInvalidPosition, p, len(line))
	}
	if insideRune(line, p.Byte) {
		return fmt.Errorf("%w: %v: inside a UTF-8 character", ErrInvalidPosition, p)
	}
	return nil
}

// insideRune は、i が line の中の正しい UTF-8 の多バイト文字の途中を指すかどうかを返す。
//
// 正しい多バイト文字の 2 バイト目以降は継続バイトなので、別の正しい文字の先頭にはならない。
// このため、i の前 utf8.UTFMax-1 バイト以内から始まる正しい多バイト文字が i をまたぐかだけを見れば、
// 行の先頭から utf8.DecodeRuneInString で進めたときの刻みと一致する。
func insideRune(line string, i int) bool {
	for j := max(0, i-(utf8.UTFMax-1)); j < i; j++ {
		_, size := utf8.DecodeRuneInString(line[j:])
		// 不正なバイトは size が 1 になる。正しい文字なら size は 1 以上で、U+FFFD そのものも 3 になる。
		if size > 1 && j+size > i {
			return true
		}
	}
	return false
}
