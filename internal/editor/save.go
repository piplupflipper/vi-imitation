package editor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command/ex"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// 画面に出す誤り（Vim のエラー番号を先頭に付ける）。
var (
	errNoFileName = errors.New("E32: ファイル名がありません")
	errFileExists = errors.New("E13: ファイルが存在します (! を付けると上書きします)")
	errNoWrite    = errors.New("E37: 最後の変更が保存されていません（! を付けると変更を捨てます）")
)

// checkOthersSaved は、bs 以外に未保存の変更のあるバッファが bufs にあれば、最初のものへ移り、名前を入れた E162 を返す
// （Vim 9.1 の :q と :qa が、未保存のバッファへ移って知らせるのと同じ）。
// 別の client のタブのバッファは、移らずにその client のペインへフォーカスを移す（enter）。
func (e *Editor) checkOthersSaved(bs *bufferState, bufs []*bufferState) error {
	for _, o := range bufs {
		if o != bs && o.buf.Modified() {
			e.enter(o)
			return fmt.Errorf("E162: バッファ \"%s\" の変更は保存されていません (! を付けると破棄します)", e.bufferName(o))
		}
	}
	return nil
}

// endsServer は、操作中の client の最後のタブを閉じると server が終わるか（繋いでいる client がこの 1 つだけか）を返す。
// server が終わると、どのタブでもないバッファの変更も失うので、:q と :qa はそれも確かめる。
func (e *Editor) endsServer() bool {
	return len(e.clients) == 1
}

// quitTab は、:q と :wq と :x の終わりの動作である。カーソルのあるタブ bs を閉じ、そのバッファも閉じる（FR-64）。
// 最後のタブなら client を終える（FR-114）。未保存の変更の確認（E37）は ex が行う。
// ! が無く、閉じると server が終わるとき（最後の client の最後のタブ）は、Vim 9.1 の :q と同じく、
// 他に未保存の変更のあるバッファがあれば E162 で閉じない。
//
// req: FR-64, FR-114
func (e *Editor) quitTab(bs *bufferState, force bool) error {
	if !force && len(e.view.tabs) <= 1 && e.endsServer() {
		if err := e.checkOthersSaved(bs, e.bufs); err != nil {
			return err
		}
	}
	e.closeBufferState(bs)
	return nil
}

// quitAll は、:qa、:wqa、:xa の終わりの動作である。操作中の client のタブをすべて閉じ、client を終える（FR-114、FR-124）。
// 他の client のタブは閉じない。! が無ければ、閉じるタブ（閉じると server が終わるときは、どのタブでもないバッファも）に
// 未保存の変更があれば、そのタブへ移って E37 か E162 で閉じない（Vim 9.1 と同じ）。
//
// req: FR-114, FR-124
func (e *Editor) quitAll(bs *bufferState, force bool) error {
	if !force {
		if bs.buf.Modified() {
			return errNoWrite
		}
		check := e.view.tabs
		if e.endsServer() {
			check = e.bufs
		}
		if err := e.checkOthersSaved(bs, slices.Clone(check)); err != nil {
			return err
		}
	}
	for _, o := range slices.Clone(e.view.tabs) {
		e.closeBufferState(o)
	}
	return nil
}

// writeAll は、:wa を行う。未保存の変更のあるバッファを、番号の順に保存の流れ（save）で書き出す。
// clientOnly なら操作中の client のタブだけ（:wqa、:xa）を、そうでなければ server のすべてのバッファ（:wa。仮）を書き出す。
// 名前の無いバッファは書き出さずに E141 にし、他のバッファは書き出す（Vim 9.1 と同じ）。最初の誤りを返す。
// force が false なら、:w と同じく外で変わったファイルを確かめ（writeConflict）、変わっていれば errChangedOnDisk を返す。
//
// req: FR-08
func (e *Editor) writeAll(force, clientOnly bool) error {
	var first error
	bufs := e.bufs
	if clientOnly {
		bufs = e.view.tabs
	}
	for _, o := range slices.Clone(bufs) {
		if !o.buf.Modified() {
			continue
		}
		var err error
		switch {
		case o.path == "":
			err = fmt.Errorf("E141: バッファ %d にファイル名がありません", o.id)
		case !force && writeConflict(o) != nil:
			// :w と同じく、外で変わったファイルへは書き込むかを問う（errChangedOnDisk なら runActions が問い、y なら ! を付けて
			// :wa をやり直す。書き出し済みのバッファは変更が無いので、やり直しでは書かない）。
			err = writeConflict(o)
			if errors.Is(err, errChangedOnDisk) {
				return err
			}
		default:
			err = e.save(o, o.path, false)
		}
		if first == nil {
			first = err
		}
	}
	return first
}

// displayName は、バッファの名前（パス、無ければ "[No Name]"）を返す。
func displayName(bs *bufferState) string {
	if bs.path == "" {
		return "[No Name]"
	}
	return bs.path
}

// doAction は、Ex コマンドが求めるエディタの動作を 1 つ行う（:w、:q、:e）。
// ファイルの引数（:w、:e、:tab.new、:diffsplit の File）の先頭の "~" と環境変数は、ex が展開している（ex.State.FileEnv。FR-200）。
func (e *Editor) doAction(bs *bufferState, a ex.Action) error {
	if f := e.float; f != nil && f.focused && (bs == f.bs || (bs == f.preview.bs && f.preview.focus) || (f.out != nil && bs == f.out)) {
		// フォーカスのあるポップアップの中で打った、タブに関わるコマンド（popup.go）。
		switch a.Kind {
		case ex.ActionQuit:
			// Vim で浮かべた窓を :q で閉じるのと同じく、ポップアップを閉じる。:popup で縮めたタブは、閉じずにタブの並びへ戻す。
			e.closeFloat(true)
			return nil
		case ex.ActionBufferDelete:
			if a.Count == 0 && a.Text == "" {
				e.closeFloat(true)
				return nil
			}
		case ex.ActionQuitAll, ex.ActionTabMove, ex.ActionDiff, ex.ActionTabNew:
			// タブを対象にするコマンドは、ポップアップを閉じてから、開いたときのタブで行う。
			e.closeFloat(true)
			bs = e.cur
		case ex.ActionEdit:
			if a.File != "" {
				e.closeFloat(true)
				bs = e.cur
			}
		case ex.ActionWrite, ex.ActionWriteAll, ex.ActionOutput, ex.ActionCheckTime, ex.ActionBuffer,
			ex.ActionJump, ex.ActionJumpList, ex.ActionRegisterList, ex.ActionExtCommand, ex.ActionPopup:
		}
	}
	switch a.Kind {
	case ex.ActionPopup:
		return e.floatAction(bs, a)
	case ex.ActionWrite:
		return e.write(bs, a)
	case ex.ActionQuit:
		return e.quitTab(bs, a.Force)
	case ex.ActionQuitAll:
		return e.quitAll(bs, a.Force)
	case ex.ActionWriteAll:
		return e.writeAll(a.Force, a.ClientOnly)
	case ex.ActionTabMove:
		return e.tabMove(bs, a)
	case ex.ActionDiff:
		return e.diffCommand(bs, a)
	case ex.ActionBufferDelete:
		return e.deleteBuffer(bs, a)
	case ex.ActionEdit:
		return e.edit(bs, a)
	case ex.ActionTabNew:
		e.newTab()
		return nil
	case ex.ActionOutput:
		return e.outputAction(a)
	case ex.ActionCheckTime:
		e.checkTime()
		return nil
	case ex.ActionBuffer:
		return e.bufferAction(bs, a)
	case ex.ActionJumpList:
		e.message = infoMessage(e.jumpListText())
		return nil
	case ex.ActionRegisterList:
		return e.openRegisterList(bs)
	case ex.ActionJump:
		// command.State が Navigator で行うので、Result.Actions には入らない。来たら内部の誤りである。
		return errors.New("内部の誤り: ジャンプリストの移動が本体の動作として渡された")
	case ex.ActionExtCommand:
		// command.State が問い合わせるので、Result.Actions には入らない。来たら内部の誤りである。
		return fmt.Errorf("内部の誤り: 拡張機能のコマンド %s が本体の動作として渡された", a.Command)
	}
	return fmt.Errorf("未知の動作 %d", a.Kind)
}

// write は、:w を行う。
// 引数が無いか、バッファのファイルと同じなら保存の流れ（基本設計 6.7）を通す。
// 名前の無いバッファに名前を付けて書くと、そのファイルのバッファにする（Vim と同じ）。
// ファイルの種類は、空のときだけ新しい名前から判定する（:set filetype= で決めた種類は残す。Vim 9.1 と同じ）。
// 別のファイルへ書くときは、保存の流れを通さずに書き出すだけにし、バッファは保存済みにしない。
// バッファのファイルへ書くときは、! が無ければ、読み込んだ後にファイルが外で変わっていないかを先に確かめる（writeConflict）。
// 変わっていれば errChangedOnDisk を返し、呼び出し側が書き込むかを問う。
//
// req: FR-08
func (e *Editor) write(bs *bufferState, a ex.Action) error {
	path := bs.path
	if a.File != "" {
		path = e.absPath(a.File)
	}
	if path == "" {
		return errNoFileName
	}
	if path != bs.path {
		if _, err := os.Stat(path); err == nil && !a.Force {
			return errFileExists
		}
		if bs.path != "" {
			made, err := makeParentDir(path)
			if err != nil {
				return forBuffer(err, bs, false)
			}
			if err := writeBuffer(bs.buf, path); err != nil {
				if pe := asPermissionError(path, err); pe != nil {
					return forBuffer(pe, bs, false)
				}
				return writeError(path, made, err)
			}
			e.message = infoMessage(writtenText(path, made))
			return nil
		}
		bs.path = path
		// ファイルの種類を :set filetype= などで決めてあれば、それを残す（Vim 9.1 も、'filetype' が空のときだけ判定し直す）。
		if bs.fileType() == "" {
			bs.setFileType(e.detectFileType(path))
		}
		e.renameBufferOptions(bs)
		e.notifyBufferAttrs(bs)
	}
	if !a.Force {
		if err := writeConflict(bs); err != nil {
			return err
		}
	}
	return e.save(bs, path, false)
}

// save は、バッファを保存の流れ（基本設計 6.7）で path へ書き出す。path はバッファのファイルである。
//
//  1. 宣言ファイルの saveHook が true の稼働中の拡張機能へ、1 つずつ順に buffer/willSave を送る（skipHooks なら飛ばす）
//  2. 応答のトランザクションの BaseVersion が現在と一致すれば適用し、次の拡張機能には適用した後のバージョン番号で問い合わせる
//  3. 期限を超えた拡張機能は待たずに次へ進み、1 行のメッセージで知らせる（exthost が $/cancel を送る）
//  4. 書き出し、稼働中の拡張機能すべてへ buffer/didSave を送る
//
// 応答を待つ間は入れ子の待ち（wait）なので、画面の描画は続き、キー入力と拡張機能からのリクエストは待ちが終わってから処理する。
func (e *Editor) save(bs *bufferState, path string, skipHooks bool) error {
	var notes []string
	// 編集を禁止したバッファ（FR-85）では、保存の前の編集を適用できないので、buffer/willSave を送らない（整形のたびの警告と無駄な問い合わせを避ける）。
	if !skipHooks && !bs.buf.ReadOnly() {
		for _, id := range e.host.SaveHookExtensions() {
			e.tagSeq++
			tag := queryTag(e.tagSeq)
			q, err := e.host.SendWillSave(id, ext.BufferWillSaveParams{Buffer: bs.id, Version: ext.Version(bs.buf.Version())}, exthost.SendOptions{Tag: tag})
			if err != nil {
				continue
			}
			r := e.wait(q, tag)
			var res ext.BufferWillSaveResult
			if err := r.Decode(&res); err != nil {
				notes = append(notes, e.willSaveNote(id, err))
				continue
			}
			if res.Transaction == nil {
				continue
			}
			if _, err := e.applyExternal(bs, fromExtTx(*res.Transaction)); err != nil {
				notes = append(notes, fmt.Sprintf("拡張機能 %s の保存の前の編集を適用できません: %v", id, err))
			}
		}
	}
	made, err := makeParentDir(path)
	if err != nil {
		return forBuffer(err, bs, true)
	}
	if err := writeBuffer(bs.buf, path); err != nil {
		// 権限の不足なら、呼び出し側（runActions）が権限を上げて書くかを問う（FR-79）。
		if pe := asPermissionError(path, err); pe != nil {
			return forBuffer(pe, bs, true)
		}
		return writeError(path, made, err)
	}
	if st, err := statFile(path); err == nil {
		bs.setDisk(st)
	}
	bs.buf.MarkSaved()
	// アンドゥの履歴は、Vim の 'undofile' と同じく、バッファのファイルへ書いたときに残す（FR-165）。書けなくても保存は失敗にしない。
	undoNote := e.saveUndo(bs)
	if err := e.host.DidSave(ext.BufferDidSaveParams{Buffer: bs.id, Version: ext.Version(bs.buf.Version()), Path: path}); err != nil {
		e.log.Error("buffer/didSave を送れない", "err", err)
	}
	// 拡張機能の誤りとアンドゥの履歴を残せなかったことの両方があれば、どちらも 1 行に出す（履歴が残っていないことに気付けるように）。
	extra := ""
	if undoNote != "" {
		extra = "。" + undoNote
	}
	switch {
	case len(notes) > 0 && made != "":
		e.message = ui.Message{Text: fmt.Sprintf("%s（ディレクトリ \"%s\" を作りました%s）", notes[0], made, extra), Level: ui.MessageWarning}
	case len(notes) > 0 && undoNote != "":
		e.message = ui.Message{Text: fmt.Sprintf("%s（%s）", notes[0], undoNote), Level: ui.MessageWarning}
	case len(notes) > 0:
		e.message = ui.Message{Text: notes[0], Level: ui.MessageWarning}
	case undoNote != "":
		e.message = ui.Message{Text: fmt.Sprintf("%s（%s）", writtenText(path, made), undoNote), Level: ui.MessageWarning}
	default:
		e.message = infoMessage(writtenText(path, made))
	}
	return nil
}

// willSaveNote は、拡張機能 id の buffer/willSave の応答が誤り err だったときに、メッセージの行へ出す 1 行を返す。
//
// 期限（exthost.ErrTimeout）を超えたときは、内部の誤りの文字列を出さずに、どの拡張機能の整形を待たずに保存したかと、
// 期限の値と、期限を変える設定（extensions.willSaveTimeoutMs。FR-177）を出す（#342）。
// それ以外の誤り（拡張機能が誤りを応答した、終わったなど）は、原因が分かるように誤りの文字列を付ける。
func (e *Editor) willSaveNote(id string, err error) string {
	if errors.Is(err, exthost.ErrTimeout) {
		timeout := e.cfg.Host.WillSaveTimeout
		if timeout <= 0 {
			timeout = exthost.DefaultWillSaveTimeout
		}
		return fmt.Sprintf("拡張機能 %s の整形が期限（%d ms）を超えたので、整形を待たずに保存しました（期限は extensions.willSaveTimeoutMs）", id, timeout.Milliseconds())
	}
	return fmt.Sprintf("拡張機能 %s の保存の前の処理が失敗しました: %v", id, err)
}

// makeParentDir は、書き出す先 path の親のディレクトリが無ければ、利用者に聞かずに作る（FR-78。基本設計 4.7）。
// 作ったら、path の親のディレクトリを返す（途中のディレクトリも作ったときでも、最も深いもの。:w a/b/c/f で a から作っても a/b/c）。
// あれば何もせずに空を返す。
// 権限は os.MkdirAll に 0o777 を渡して umask に任せる（シェルの mkdir -p と同じ）。
// 作れなければ E212 の誤りを返す。権限の不足（EACCES）で作れないときは permissionError を返し、
// 呼び出し側が権限を上げて mkdir -p してから tee するかを問う（FR-116。handover.go）。
// 親が無いか分からない（stat が ENOENT 以外の誤りを返した。途中がファイルなど）ときは作らず、書き出しの誤りに任せる。
// 途中のディレクトリに検索の権限が無いと stat は EACCES を返し、親が無くても作らずに書き出しが EACCES で失敗する。
// その EACCES は asPermissionError が「親のディレクトリがあるか分からない」として扱い、権限を上げて mkdir -p してから tee する。
//
// req: FR-78, FR-116
func makeParentDir(path string) (string, error) {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // 基本設計 4.7 で、mkdir -p と同じく umask に任せると決めている
		if pe := asPermissionError(path, err); pe != nil {
			return "", pe
		}
		return "", fmt.Errorf("E212: ディレクトリ %s を作れません: %w", dir, err)
	}
	return dir, nil
}

// writeError は、書き出しの失敗の E212 を返す。made が空でなければ、作ったディレクトリ（消さずに残す）も同じ文で知らせる（FR-78）。
func writeError(path, made string, err error) error {
	if made != "" {
		return fmt.Errorf("E212: %s を書けません（ディレクトリ \"%s\" は作りました）: %w", path, made, err)
	}
	return fmt.Errorf("E212: %s を書けません: %w", path, err)
}

// writtenText は、書き出した後のメッセージの行の文言である。made が空でなければ、作ったディレクトリを同じ 1 行に入れる（FR-78）。
func writtenText(path, made string) string {
	if made != "" {
		return fmt.Sprintf("ディレクトリ \"%s\" を作り、\"%s\" を書き込みました", made, path)
	}
	return fmt.Sprintf("\"%s\" を書き込みました", path)
}

// edit は、:e を行う。引数が無ければ、バッファのファイルを読み直す（未保存の変更の確認は ex が済ませている）。
func (e *Editor) edit(bs *bufferState, a ex.Action) error {
	if a.File != "" {
		_, err := e.openFile(a.File, true)
		return err
	}
	if bs.path == "" {
		return errNoFileName
	}
	// Vim 9.1 と同じく、:e の読み直しもジャンプとして記録する。
	e.markJumpAt(bs, bs.state.Cursor())
	if err := e.reloadFile(bs); err != nil {
		return err
	}
	// ファイルを開き直したので、バッファごとの設定の項目を初めの値に戻す（Vim 9.1 の :e と :e! も 'filetype' を判定し直す）。
	// 外での変更の読み直し（autoread と W11 の (L)oad File）では戻さない（Vim 9.1 の buf_reload も 'filetype' を残す）。
	e.resetBufferOptions(bs)
	e.notifyBufferAttrs(bs)
	return nil
}

// reload は、バッファ bs の内容を、読み直した b の内容にする。バッファの番号は変えない（Vim の :e と同じ）。
//
// 読み直しは、前後の共通の行を除いた、変わった行の範囲だけを置き換える 1 つの編集（reloadEdits）として適用する（#143）。
// アンドゥの単位と拡張機能へ送る差分（buffer/change）の大きさは変わった行の数に比例し、変わっていない行の印と診断は動かない。
// 末尾の改行の有無の記録も同じ単位で変える（buffer.Buffer.ApplyReload）ので、アンドゥ 1 回で、末尾の改行も含めて読み直す前へ戻れる
// （Vim 9.1 の 'undoreload' の範囲の読み直しと同じ）。
// 外での変更を問わずに読み直す（autoread）ときに毎秒呼ばれても、ログの追記のような変更ならアンドゥの履歴は追記した行の分しか増えない。
func (e *Editor) reload(bs *bufferState, b *buffer.Buffer) {
	cur := bs.buf
	cursor := bs.state.Cursor()
	ch, err := cur.ApplyReload(buffer.Transaction{BaseVersion: cur.Version(), Edits: reloadEdits(cur, b)}, b.HasTrailingNewline())
	if err != nil {
		e.showError(err)
		return
	}
	if ch.Before != ch.After {
		cur.SetUndoCursor(cursor)
		e.commitChange(bs, ch)
	}
	// 読み直したファイルが空なら、Vim と同じく行の無いバッファにする（書き出すと空のファイルになる）。
	// 空の行 1 つ（"\n"）なら、テキストが同じでも行の無いバッファの記録を外す。
	if b.NoLines() {
		cur.MarkNoLines()
	} else {
		cur.MarkHasLines()
	}
	e.syncFinalNewline(bs)
	cur.MarkSaved()
	bs.state.SetCursor(cursor)
}

// reloadEdits は、バッファ cur の行を、読み直した b の行にする最小の編集を返す。
// 先頭と末尾から同じ行を数えて除き、残った行の範囲を 1 つの編集で置き換える。行が同じなら空を返す。
func reloadEdits(cur, b *buffer.Buffer) []buffer.Edit {
	n, m := cur.LineCount(), b.LineCount()
	p := 0
	for p < n && p < m && cur.Line(p) == b.Line(p) {
		p++
	}
	s := 0
	for s < n-p && s < m-p && cur.Line(n-1-s) == b.Line(m-1-s) {
		s++
	}
	if n == m && p == n {
		return nil
	}
	lines := make([]string, 0, m-p-s)
	for i := p; i < m-s; i++ {
		lines = append(lines, b.Line(i))
	}
	text := strings.Join(lines, "\n")
	endOf := func(i int) buffer.Position { return buffer.Position{Line: i, Byte: len(cur.Line(i))} }
	switch {
	case n-s > p && m-s > p:
		// 両方に違う行がある: cur の p〜n-s-1 行を置き換える。
		return []buffer.Edit{{Range: buffer.Range{Start: buffer.Position{Line: p}, End: endOf(n - s - 1)}, Text: text}}
	case m-s > p && p < n:
		// 行を足すだけ（p 行目の前）。
		at := buffer.Position{Line: p}
		return []buffer.Edit{{Range: buffer.Range{Start: at, End: at}, Text: text + "\n"}}
	case m-s > p:
		// 末尾に行を足すだけ。
		at := endOf(n - 1)
		return []buffer.Edit{{Range: buffer.Range{Start: at, End: at}, Text: "\n" + text}}
	case n-s < n:
		// 行を消すだけ（後ろに行が残る）。
		return []buffer.Edit{{Range: buffer.Range{Start: buffer.Position{Line: p}, End: buffer.Position{Line: n - s}}}}
	default:
		// 末尾の行を消すだけ（p > 0。行は少なくとも 1 つ残る）。
		return []buffer.Edit{{Range: buffer.Range{Start: endOf(p - 1), End: endOf(n - 1)}}}
	}
}
