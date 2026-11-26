package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// コマンドの ID（manifest.json）。
const (
	// cmdDiffHead は、バッファのファイルを git の HEAD の版と見比べるコマンドである。
	cmdDiffHead = "git.diffHead"
	// cmdChanges は、比べる元（引数。省けば既定のブランチとの merge-base）との差分があるファイルの一覧を開くコマンドである。
	cmdChanges = "git.changes"
	// cmdChangesHead は、HEAD との差分（作業ツリーの変更）があるファイルの一覧を開くコマンドである（:git.changes HEAD と同じ）。
	cmdChangesHead = "git.changesHead"
)

// extension は、git 連携の拡張機能である。
type extension struct {
	server *server.Server
	// ctx は、ハンドラーから起こした goroutine が git を実行し、本体へリクエストを送るときの ctx である。
	// ハンドラーの ctx は応答で終わり、それで本体へ送ると ErrEditInHandler になる（ext/server の Edit の doc）。
	ctx context.Context

	// mu は、pickers と early を守る。
	mu sync.Mutex
	// pickers は、開いている差分の一覧である（ポップアップの ID から）。
	pickers map[ext.ViewID]*changesPicker
	// early は、OpenPopup の応答より先に届いたポップアップの知らせである。
	early map[ext.ViewID]*earlyEvents

	// openMu は、一覧で選んだファイルを開く処理（openChange）を 1 つずつにし、lastBase を守る。
	openMu sync.Mutex
	// lastBase は、前に一覧から開いた比べる元の版のバッファである。無ければ 0 である。
	lastBase ext.BufferID
}

// newExtension は、コマンドを登録した拡張機能を作る。
func newExtension() *extension {
	x := &extension{
		server: server.New(), ctx: context.Background(),
		pickers: map[ext.ViewID]*changesPicker{}, early: map[ext.ViewID]*earlyEvents{},
	}
	x.server.Command(cmdDiffHead, x.diffHead)
	x.server.CommandAsync(cmdChanges, x.changesCommand(cmdChanges, ""))
	x.server.CommandAsync(cmdChangesHead, x.changesCommand(cmdChangesHead, "HEAD"))
	x.server.OnView(x.onView)
	return x
}

// diffHead は、git.diffHead を受け付ける。git の実行と本体へのリクエストは時間がかかりうるので、別の goroutine で行い、
// 受け付けたことだけを応答する（基本設計 6 章の冒頭。ext/server の Edit の doc のとおり、ハンドラーの中では本体の応答を待てない）。
// 結果は見比べの画面で、誤りは message/show で知らせる。
//
// マクロとドットリピートの再生中（params の sync）も、受け付けたことだけを応答する。
// このコマンドはバッファのテキストもカーソルも変えないので、後から見比べが始まっても、再生の結果は変わらない。
//
// req: FR-104
func (x *extension) diffHead(_ context.Context, b *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
	// コマンドラインから引数を付けて呼んだ（:git.diffHead foo）ら、引数を取らないことを知らせて失敗させる（基本設計 6.7）。
	if hasArgs(p.Args) {
		_ = x.server.ShowMessage(ext.MessageWarning, cmdDiffHead+": 引数を取らない")
		return ext.CommandExecuteResult{OK: false}, nil
	}
	if b == nil {
		// 不正な UTF-8 を含むバッファは、本体が拡張機能へ送らない（基本設計 5.3）。
		_ = x.server.ShowMessage(ext.MessageWarning, "このバッファは不正な UTF-8 を含むので、HEAD と見比べられません")
		return ext.CommandExecuteResult{OK: false}, nil
	}
	if b.Path() == "" {
		_ = x.server.ShowMessage(ext.MessageWarning, "ファイルでないバッファは、HEAD と見比べられません")
		return ext.CommandExecuteResult{OK: false}, nil
	}
	id, path, fileType := b.ID(), b.Path(), b.FileType()
	go x.startDiff(x.ctx, id, path, fileType)
	return ext.CommandExecuteResult{OK: true}, nil
}

// hasArgs は、command/execute の args（基本設計 6.7 の {"text": "..."} の形）に、空白でない引数があるかを返す。読めない args も引数があるとみなす。
func hasArgs(args json.RawMessage) bool {
	if len(args) == 0 {
		return false
	}
	var a struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(args, &a) != nil {
		return true
	}
	return strings.TrimSpace(a.Text) != ""
}

// startDiff は、path の HEAD の版をファイルでないバッファにし（buffer/create）、バッファ id と見比べさせる（diff/start）。
// ループの外の goroutine で呼ぶ。
func (x *extension) startDiff(ctx context.Context, id ext.BufferID, path, fileType string) {
	// 端末多重化ソフトの外では見比べを使えない（基本設計 6.8、10 章の仮）ので、git を実行してバッファを作る前に断る。
	st, err := x.server.EditorState(ctx)
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageError, fmt.Sprintf("編集の状態を問い合わせられません: %s", remoteMessage(err)))
		return
	}
	if !st.CanSplit {
		_ = x.server.ShowMessage(ext.MessageWarning, "端末多重化ソフト（tmux、herdr）の外では HEAD と見比べられません（見比べには 2 つ目の編集の client が要ります）")
		return
	}
	head, err := readHead(ctx, path)
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageWarning, err.Error())
		return
	}
	created, err := x.server.CreateBuffer(ctx, ext.BufferCreateParams{Title: "HEAD:" + head.Name, Text: head.Text, FileType: fileType})
	if err != nil {
		_ = x.server.ShowMessage(ext.MessageError, fmt.Sprintf("HEAD の版のバッファを作れません: %s", remoteMessage(err)))
		return
	}
	// ファイルのバッファはタブで、HEAD の版のバッファはタブでないので、本体はファイルのタブの client のペインを分割し、
	// HEAD の版を新しい client のタブにする。diffview.nvim と同じく、見比べの左の側（A）を HEAD の版、右の側（B）をファイルにする（#437）。
	// 本体は、タブでない A を元のペインの左に開き、フォーカスを右のファイルのペインに残す（ext.DiffStartParams の doc）。
	// Wait で、見比べが始まるまで応答を待つ（#447。ループの外の goroutine なので待ってよい）。
	if _, err := x.server.StartDiff(ctx, ext.DiffStartParams{A: created.Buffer, B: id, Wait: true}); err != nil {
		_ = x.server.ShowMessage(ext.MessageWarning, remoteMessage(err))
	}
}

// remoteMessage は、本体が誤りで応答したならその文言を、そうでなければ err の文言を返す。
func remoteMessage(err error) string {
	if re, ok := errors.AsType[*rpc.Error](err); ok {
		return re.Message
	}
	return err.Error()
}
