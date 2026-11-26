package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// 差分があるファイルの一覧のファインダーと、選んだファイルの見比べ（FR-250、FR-251。基本設計 6.9）。
//
// 一覧は、ファジーファインダーの拡張機能と同じ部品（ext.ViewFinder）を中央のポップアップに出す。
// 本体からの知らせ（OnView）は ext/server のループの上で呼ばれるので、そこでは picker を引いて goroutine を起こすだけにし、
// 絞り込みと、選んだファイルを開く本体へのリクエストは、ループの外で行う（dev-cycle 2.2）。

// pickerLimit は、一覧に送る項目の数の上限である（ファジーファインダーの既定の limit と同じ。仮）。
const pickerLimit = 300

// matchStyle は、一覧の、入力に一致した文字に付ける色の名前である（本体の ui.StyleFinderMatch）。
const matchStyle = "finder.match"

// changesPicker は、開いた差分の一覧 1 つである。changes と labels は開いた後に変えない。
type changesPicker struct {
	x       *extension
	cmp     comparison
	changes []change
	labels  []string
	view    ext.ViewID
}

// earlyEvents は、OpenPopup の応答より先に届いた、まだ picker に結び付けていないポップアップの知らせである。
// ext/server は応答を待っている goroutine へ応答を渡し、知らせはループで処理するので、応答と知らせの順は保たれない。
type earlyEvents struct {
	input    *inputState
	selected *string
	closed   bool
}

// inputState は、入力の行の中身と番号（ext.InputChangeParams.Seq）である。
type inputState struct {
	text string
	seq  uint64
}

// changesCommand は、差分の一覧を開くコマンドのハンドラーを返す。fixed が空でなければ、比べる元をそれに決め、引数を取らない
// （git.changesHead）。空なら、コマンドラインの引数（:git.changes main）を比べる元にする（git.changes）。
//
// git の実行と本体へのリクエストは時間がかかりうるので、別の goroutine で行う。
// マクロとドットリピートの再生中（params の sync）は、ポップアップを開いてから応答する。本体は応答を待つ間に popup/open を処理して
// ポップアップへフォーカスを移し、応答の後の再生の残りのキーをポップアップへ渡す（ファジーファインダーと同じ。PR #249 のレビューの必須 2）。
func (x *extension) changesCommand(id, fixed string) server.AsyncCommandHandler {
	return func(_ context.Context, b *server.Buffer, p *ext.CommandExecuteParams, reply func(ext.CommandExecuteResult, error)) {
		spec := fixed
		if fixed != "" {
			if hasArgs(p.Args) {
				_ = x.server.ShowMessage(ext.MessageWarning, id+": 引数を取らない")
				reply(ext.CommandExecuteResult{OK: false}, nil)
				return
			}
		} else {
			text, err := argText(p.Args)
			if err != nil {
				_ = x.server.ShowMessage(ext.MessageWarning, id+": "+err.Error())
				reply(ext.CommandExecuteResult{OK: false}, nil)
				return
			}
			spec = text
		}
		// バッファがファイルなら、そのファイルを含む作業ツリーの一覧にする（git.diffHead と同じく、ファイルのリポジトリを読む）。
		// ファイルでないバッファ（HEAD の版など）と不正な UTF-8 を含むバッファでは、操作中の client の作業ディレクトリにする。
		dir := ""
		if b != nil && b.Path() != "" {
			path := b.Path()
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				path = resolved
			}
			dir = filepath.Dir(path)
		}
		var once sync.Once
		answer := func(ok bool) { once.Do(func() { reply(ext.CommandExecuteResult{OK: ok}, nil) }) }
		go func() { answer(x.openChanges(x.ctx, dir, spec)) }()
		if !p.Sync {
			answer(true)
		}
	}
}

// argText は、command/execute の args（基本設計 6.7 の {"text": "..."} の形）の text を、前後の空白を除いて返す。
func argText(args json.RawMessage) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	var a struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("引数を読めない: %w", err)
	}
	return strings.TrimSpace(a.Text), nil
}

// openChanges は、dir を含む作業ツリーの、spec の比べる元との差分があるファイルの一覧を、中央のポップアップのファインダーに出す。
// ポップアップを開いたか、差分が無いことを知らせたら true を返す。開けない理由は message/show で知らせる。ループの外で呼ぶ。
//
// req: FR-250, FR-252
func (x *extension) openChanges(ctx context.Context, dir, spec string) bool {
	st, err := x.server.EditorState(ctx)
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageError, fmt.Sprintf("編集の状態を問い合わせられません: %s", remoteMessage(err)))
		return false
	}
	// 見比べには 2 つ目の編集の client が要る（基本設計 6.8）ので、一覧を出す前に断る（git.diffHead と同じ）。
	if !st.CanSplit {
		_ = x.server.ShowMessage(ext.MessageWarning, "端末多重化ソフト（tmux、herdr）の外では比べる元と見比べられません（見比べには 2 つ目の編集の client が要ります）")
		return false
	}
	// バッファのファイルが作業ツリーの外（git commit の COMMIT_EDITMSG、/tmp のメモ）なら、作業ディレクトリのリポジトリで試し直す
	// （diffview.nvim と同じく、バッファから決められなければ作業ディレクトリで決める。PR #382 のレビューの必須 2）。
	fallback := st.WorkDir
	if fallback == "" {
		if d, werr := os.Getwd(); werr == nil {
			fallback = d
		}
	}
	if dir == "" {
		dir = fallback
	}
	cmp, err := resolveBase(ctx, dir, spec)
	if _, outside := errors.AsType[*notWorkTreeError](err); outside && fallback != "" && fallback != dir {
		cmp, err = resolveBase(ctx, fallback, spec)
	}
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageWarning, err.Error())
		return false
	}
	changes, err := listChanges(ctx, cmp)
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageWarning, err.Error())
		return false
	}
	if len(changes) == 0 {
		_ = x.server.ShowMessage(ext.MessageInfo, cmp.label+" との差分はありません")
		return true
	}
	p := &changesPicker{x: x, cmp: cmp, changes: changes}
	for _, c := range changes {
		p.labels = append(p.labels, cleanLabel(c.label()))
	}
	r, err := x.server.OpenPopup(ctx, ext.PopupOpenParams{Placement: ext.PopupCenter, Component: p.component("")})
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageError, fmt.Sprintf("差分の一覧を開けません: %s", remoteMessage(err)))
		return false
	}
	p.view = r.View
	x.mu.Lock()
	early := x.early[r.View]
	delete(x.early, r.View)
	if early == nil || !early.closed {
		x.pickers[r.View] = p
	}
	x.mu.Unlock()
	if early != nil && !early.closed {
		if early.input != nil {
			go p.send(*early.input)
		}
		if early.selected != nil {
			go p.choose(*early.selected)
		}
	}
	return true
}

// onView は、本体からのポップアップの知らせを、その picker へ渡す。ループの上で呼ばれるので、重い処理はしない。
func (x *extension) onView(_ context.Context, e *server.ViewEvent) {
	x.mu.Lock()
	p := x.pickers[e.View]
	if p == nil {
		ev := x.early[e.View]
		if ev == nil {
			ev = &earlyEvents{}
			x.early[e.View] = ev
		}
		switch e.Kind {
		case server.ViewInputChanged:
			if ev.input == nil || e.Seq >= ev.input.seq {
				ev.input = &inputState{text: e.Item, seq: e.Seq}
			}
		case server.ViewSelected:
			item := e.Item
			ev.selected = &item
		case server.ViewClosed:
			ev.closed = true
		case server.ViewKey, server.ViewExpanded, server.ViewTabChanged, server.ViewChildSelected:
		}
		x.mu.Unlock()
		return
	}
	if e.Kind == server.ViewClosed {
		delete(x.pickers, e.View)
	}
	x.mu.Unlock()
	switch e.Kind {
	case server.ViewInputChanged:
		// 本体は、最後に送った input/change より古い Seq の一覧を捨てるので、goroutine の終わる順が前後してもよい。
		go p.send(inputState{text: e.Item, seq: e.Seq})
	case server.ViewSelected:
		go p.choose(e.Item)
	case server.ViewClosed, server.ViewKey, server.ViewExpanded, server.ViewTabChanged, server.ViewChildSelected:
	}
}

// send は、入力 in で絞り込んだ一覧を view/update で送る。
func (p *changesPicker) send(in inputState) {
	if err := p.x.server.UpdateView(ext.ViewUpdateParams{View: p.view, Seq: in.seq, Component: p.component(in.text)}); err != nil {
		fmt.Fprintf(os.Stderr, "view/update: %v\n", err)
	}
}

// component は、入力 query で絞り込んだ一覧の部品である。項目の ID は changes の添字である。
//
// req: FR-250
func (p *changesPicker) component(query string) ext.ViewComponent {
	terms := parseQuery(query)
	var items []ext.ViewItem
	matched := 0
	for i, label := range p.labels {
		spans, ok := matchLabel(label, terms)
		if !ok {
			continue
		}
		matched++
		if len(items) >= pickerLimit {
			continue
		}
		c := p.changes[i]
		it := ext.ViewItem{ID: strconv.Itoa(i), Label: label, Highlights: spans, File: &ext.ViewFile{Path: p.cmp.absPath(c.path), Dir: c.nested}}
		if c.kind != kindDeleted && !c.submodule {
			it.Preview = &ext.ViewPreview{Path: p.cmp.absPath(c.path)}
		}
		items = append(items, it)
	}
	title := fmt.Sprintf("git の差分 %s  %d/%d", p.cmp.label, matched, len(p.labels))
	return ext.ViewComponent{Kind: ext.ViewFinder, Title: title, Text: query, Items: items, Preview: true}
}

// choose は、一覧で選ばれた項目 id のファイルを開いて見比べる。ポップアップを閉じてから開く（開いた先のタブへカーソルを移すため）。
// ループの外で呼ぶ。一覧が空のときの <CR>（id が空）は、ポップアップを閉じるだけにする。
func (p *changesPicker) choose(id string) {
	_ = p.x.server.CloseView(p.view)
	p.x.mu.Lock()
	delete(p.x.pickers, p.view)
	p.x.mu.Unlock()
	i, err := strconv.Atoi(id)
	if err != nil || i < 0 || i >= len(p.changes) {
		return
	}
	p.x.openChange(p.x.ctx, p.cmp, p.changes[i])
}

// openChange は、変更 c のファイルを操作中の client のタブに開き、比べる元の版を編集を禁止したファイルでないバッファにして見比べる。
// 比べる元に無いファイル（足した、未追跡）は、空のバッファと見比べる。作業ツリーに無いファイル（消した）は、比べる元の版だけをタブに開く。
// サブモジュールとシンボリックリンクは開かずに知らせる。
//
// 前に一覧から開いた比べる元の版のバッファは、閉じてから次の見比べを始める（buffer/dispose）。見比べのペインの client は、
// そのバッファだけをタブに持つので一緒に終わり、ファイルを選ぶたびにペインが増えない（diffview.nvim が 1 つの画面で見比べるファイルを替えるのに当たる）。
//
// 閉じる時機は、操作中の client のカーソルのあるバッファがその版かどうかで分ける。
//
//   - その版のとき（左の版のペインから一覧を開いた）は、ファイルを開く前に閉じる。
//     そのペインの client が終わり、ファイルは右のペインの client のタブに開く。開いた後に閉じると、ファイルがそのペインの client のタブになり、
//     そのペインがもう一度分割されてペインが増える（PR #382 のレビューの必須 1）
//   - それ以外（右のファイルのペインから開いた。選んだ後もカーソルはそのペインに残るので、続けて選ぶとこちらになる）は、
//     ファイルを開いた後に閉じる。版のペインの client が終わり、新しい版はファイルのペインの左に開く
//
// #437 で版を左のペインに移した。どちらの時機でも、閉じるのは版だけをタブに持つ client なので、ペインは左右の 2 つのままである。
//
// req: FR-251, FR-252
func (x *extension) openChange(ctx context.Context, cmp comparison, c change) {
	x.openMu.Lock()
	defer x.openMu.Unlock()
	switch {
	case c.submodule:
		_ = x.server.ShowMessage(ext.MessageWarning, c.path+" はサブモジュールなので見比べられません")
		return
	case c.nested:
		_ = x.server.ShowMessage(ext.MessageWarning, c.path+" は追跡していない入れ子のリポジトリなので見比べられません")
		return
	case c.symlink:
		_ = x.server.ShowMessage(ext.MessageWarning, c.path+" はシンボリックリンクなので見比べられません")
		return
	}
	if x.lastBase != 0 {
		if st, err := x.server.EditorState(ctx); err == nil && st.Buffer == x.lastBase {
			x.disposeLastBase(ctx)
		}
	}
	base, name := "", c.path
	if c.old != "" {
		name = c.old
		text, err := readBlob(ctx, cmp, c.old)
		if err != nil {
			_ = x.server.ShowMessage(ext.MessageWarning, err.Error())
			return
		}
		base = text
	}
	title := cmp.label + ":" + name
	if c.kind == kindDeleted {
		x.disposeLastBase(ctx)
		created, err := x.server.CreateBuffer(ctx, ext.BufferCreateParams{Title: title, Text: base})
		if err != nil {
			_ = x.server.ShowMessage(ext.MessageError, fmt.Sprintf("%s の版のバッファを作れません: %s", cmp.label, remoteMessage(err)))
			return
		}
		if _, err := x.server.OpenEditor(ctx, ext.EditorOpenParams{Buffer: &created.Buffer, Focus: true}); err != nil {
			_ = x.server.DisposeBuffer(ctx, created.Buffer)
			_ = x.server.ShowMessage(ext.MessageError, fmt.Sprintf("%s を開けません: %s", title, remoteMessage(err)))
			return
		}
		x.lastBase = created.Buffer
		_ = x.server.ShowMessage(ext.MessageInfo, fmt.Sprintf("%s は作業ツリーに無いので、%s の版だけを開きました", c.path, cmp.label))
		return
	}
	opened, err := x.server.OpenEditor(ctx, ext.EditorOpenParams{Path: cmp.absPath(c.path), Focus: true})
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageError, fmt.Sprintf("%s を開けません: %s", c.path, remoteMessage(err)))
		return
	}
	fileType := ""
	if st, serr := x.server.EditorState(ctx); serr == nil {
		for _, b := range st.Buffers {
			if b.Buffer == opened.Buffer {
				fileType = b.FileType
			}
		}
	}
	x.disposeLastBase(ctx)
	created, err := x.server.CreateBuffer(ctx, ext.BufferCreateParams{Title: title, Text: base, FileType: fileType})
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageError, fmt.Sprintf("%s の版のバッファを作れません: %s", cmp.label, remoteMessage(err)))
		return
	}
	x.lastBase = created.Buffer
	// ファイルのバッファは操作中の client のタブで、比べる元の版はタブでないので、本体はファイルのタブの client のペインを分割する。
	// diffview.nvim と同じく、比べる元の版を左の側（A）、ファイルを右の側（B）にする（#437）。本体は版のペインを左に開き、
	// フォーカスを右のファイルのペインに残す。見比べが始まらなければ、本体がまだタブでない比べる元の版を閉じる（ext.DiffStartParams の doc）。
	// Wait で、ペインの分割（入れ替えとフォーカスの移し直しを含む）が終わり、版が新しい client のタブになるまで応答を待つ（#447）。
	// openMu を持ったまま待つので、一覧から続けて選んでも、次の選択は前の版のペインが出来上がってから前の版を閉じる。
	// 分割の途中のペインを閉じて、端末多重化ソフトの操作（herdr app の mod の pane.swap など）を失敗させない。
	if _, err := x.server.StartDiff(ctx, ext.DiffStartParams{A: created.Buffer, B: opened.Buffer, Wait: true}); err != nil {
		_ = x.server.ShowMessage(ext.MessageWarning, remoteMessage(err))
	}
}

// disposeLastBase は、前に一覧から開いた比べる元の版のバッファを閉じる。利用者が先に閉じていれば、本体は誤りで応答するので捨てる。
func (x *extension) disposeLastBase(ctx context.Context) {
	if x.lastBase == 0 {
		return
	}
	_ = x.server.DisposeBuffer(ctx, x.lastBase)
	x.lastBase = 0
}

// cleanLabel は、一覧の表示の文字列から、部品に入れられない改行を除き（\r と \n を空白にする）、不正な UTF-8 を置き換える。
// git はパスのバイトをそのまま出すので、どちらも起こりうる。置き換えた後もバイトの長さは変わりうるが、ID は添字なので選べる。
func cleanLabel(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

// term は、入力の 1 つの語である。
type term struct {
	runes []rune
	// exact は、大文字と小文字を区別することを表す（語に大文字がある）。
	exact bool
}

// parseQuery は、入力 q を空白で区切った語の列にする（ファジーファインダーの parseQuery と同じ）。空白だけなら空である。
func parseQuery(q string) []term {
	var out []term
	for _, f := range strings.Fields(q) {
		t := term{runes: []rune(f)}
		for _, r := range t.runes {
			if unicode.IsUpper(r) {
				t.exact = true
				break
			}
		}
		if !t.exact {
			for i, r := range t.runes {
				t.runes[i] = unicode.ToLower(r)
			}
		}
		out = append(out, t)
	}
	return out
}

// matchLabel は、一覧の行 label のパスの部分（先頭の labelPrefix バイトより後）が、語の列 terms のすべてに一致するかを返し、
// 一致したら一致した文字の範囲（label の中のバイトの位置）を返す。
// 語は、語の文字がパスの中に順に（間に他の文字を挟んでよい）現れれば一致する。大文字の無い語は大文字と小文字を区別しない（smart case）。
// ファジーファインダーと違い、点数で並べ替えず、パスの順のままにする（変わったファイルの一覧は、ディレクトリごとに並んで見えるほうが読みやすいため。仮）。
//
// req: FR-250
func matchLabel(label string, terms []term) ([]ext.ViewSpan, bool) {
	if len(terms) == 0 {
		return nil, true
	}
	path := label[min(labelPrefix, len(label)):]
	hit := make([]bool, len(path))
	for _, t := range terms {
		k := 0
		for i, r := range path {
			if k == len(t.runes) {
				break
			}
			size := utf8.RuneLen(r)
			if !t.exact {
				r = unicode.ToLower(r)
			}
			if r == t.runes[k] {
				for j := range size {
					hit[i+j] = true
				}
				k++
			}
		}
		if k < len(t.runes) {
			return nil, false
		}
	}
	var spans []ext.ViewSpan
	for i := 0; i < len(hit); {
		if !hit[i] {
			i++
			continue
		}
		j := i
		for j < len(hit) && hit[j] {
			j++
		}
		spans = append(spans, ext.ViewSpan{Start: labelPrefix + i, End: labelPrefix + j, Style: matchStyle})
		i = j
	}
	return spans, true
}
