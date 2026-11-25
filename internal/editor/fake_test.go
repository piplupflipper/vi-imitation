package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/exthost"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// テスト用の拡張機能は、テストの実行ファイル自身を、引数 fakeExtFlag と動き方の名前を付けて起動したものである。
// TestMain はこの引数を見たら、テストを実行せずに ext/server で書いた拡張機能として動く。
const fakeExtFlag = "-editor-fake-ext"

// 拡張機能の動き方。
const (
	// fakeMain は、テキストオブジェクトとコマンドを持つ拡張機能（ID は "fake"）である。
	fakeMain = "main"
	// fakeFileType は、ファイルの種類 "fakeft" の契機だけで起動する拡張機能（ID は "ftx"）である。
	fakeFileType = "ft"
	// fakeSlowInit は、initialize の応答を slowInitDelay だけ遅らせる拡張機能（ID は "slow"）である。
	fakeSlowInit = "slowinit"
	// fakeProvider は、本体の操作の提供者（provides）である拡張機能である（provider_test.go）。
	fakeProvider = "provider"
	// fakeChatty は、バッファを開くたびに message/show を続けて送る拡張機能である（gopls の進み具合の知らせの代役。messages_test.go）。
	fakeChatty = "chatty"
	// fakeWindow は、window/change を受けて、窓の上端にバッファの最初の行の帯を置く拡張機能である（window_test.go）。
	fakeWindow = "window"
	// fakeSlowWindow は、fakeWindow と同じだが、知らせのたびに slowWindowDelay だけループを止める拡張機能である（window_test.go）。
	fakeSlowWindow = "slowwindow"
)

const (
	slowWindowDelay = 400 * time.Millisecond
	slowDelay       = 300 * time.Millisecond
	slowInitDelay   = 1500 * time.Millisecond
)

func TestMain(m *testing.M) {
	for i, a := range os.Args {
		if a == fakeExtFlag && i+1 < len(os.Args) {
			os.Exit(runFakeExt(os.Args[i+1]))
		}
	}
	os.Exit(m.Run())
}

// lineRange は、カーソルの行の全体の範囲を返す。
func lineRange(b *server.Buffer, line int) ext.Range {
	return ext.Range{Start: ext.Position{Line: line}, End: ext.Position{Line: line, Byte: len(b.Line(line))}}
}

// fakeWrite は、path を editor/open（focus が false）で開き、全文を content に置き換えて、skipHooks の buffer/save で書き出させる
// （ACP の拡張機能の openAsBuffer と同じ手順。fake.write）。
func fakeWrite(ctx context.Context, s *server.Server, path, content string) error {
	res, err := s.OpenEditor(ctx, ext.EditorOpenParams{Path: path, Focus: false})
	if err != nil {
		return fmt.Errorf("editor/open: %w", err)
	}
	b, err := s.WaitBuffer(ctx, res.Buffer, res.Version)
	if err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	last := b.LineCount() - 1
	er, err := s.Edit(ctx, ext.BufferEditParams{Buffer: res.Buffer, Transaction: ext.Transaction{
		BaseVersion: b.Version(),
		Edits:       []ext.Edit{{Range: ext.Range{End: ext.Position{Line: last, Byte: len(b.Line(last))}}, Text: strings.TrimSuffix(content, "\n")}},
	}})
	if err != nil || !er.Applied {
		return fmt.Errorf("buffer/edit: applied=%v %w", er.Applied, err)
	}
	v := er.Version
	sr, err := s.SaveBuffer(ctx, ext.BufferSaveParams{Buffer: res.Buffer, Version: &v, SkipHooks: true})
	if err != nil || !sr.Saved {
		return fmt.Errorf("buffer/save: saved=%v %w", sr.Saved, err)
	}
	return nil
}

func runFakeExt(mode string) int {
	s := server.New()
	// hostCtx は、ハンドラーから起こした goroutine が本体へリクエストを送るときの ctx である。
	// ハンドラーの ctx で送ると ErrEditInHandler になる（ext/server の Edit の doc）。
	hostCtx := context.Background()
	var mu sync.Mutex
	opened := map[ext.BufferID]bool{}
	var bufLog []string
	switch mode {
	case fakeMain:
		s.TextObject("fake.line", func(_ context.Context, b *server.Buffer, p *ext.TextObjectResolveParams) (ext.TextObjectResolveResult, error) {
			return ext.TextObjectResolveResult{OK: true, Range: lineRange(b, p.Cursor.Line)}, nil
		})
		s.TextObjectAsync("fake.slow", func(_ context.Context, b *server.Buffer, p *ext.TextObjectResolveParams, reply func(ext.TextObjectResolveResult, error)) {
			r := ext.TextObjectResolveResult{OK: true, Version: p.Version, Range: lineRange(b, p.Cursor.Line)}
			go func() {
				time.Sleep(slowDelay)
				reply(r, nil)
			}()
		})
		s.TextObjectAsync("fake.hang", func(ctx context.Context, _ *server.Buffer, _ *ext.TextObjectResolveParams, reply func(ext.TextObjectResolveResult, error)) {
			go func() {
				<-ctx.Done()
				reply(ext.TextObjectResolveResult{}, ctx.Err())
			}()
		})
		s.Command("fake.upper", func(_ context.Context, b *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			l := p.Cursor.Line
			tx := ext.Transaction{Edits: []ext.Edit{{Range: lineRange(b, l), Text: strings.ToUpper(b.Line(l))}}}
			return ext.CommandExecuteResult{OK: true, Transaction: &tx}, nil
		})
		// fake.popup は、引数（list、tree、input、text）の部品のポップアップを popup/open で開く（#236）。
		// 利用者の操作の知らせは message/show で出し、select の項目が close なら view/close で、update なら view/update で応える。
		s.Command("fake.popup", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			var a struct{ Text string }
			_ = json.Unmarshal(p.Args, &a)
			go func() {
				r, err := s.OpenPopup(hostCtx, fakePopup(a.Text))
				_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("opened %d err=%v", r.View, err))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.headdiff は、buffer/create で編集を禁止した "HEAD:x" のバッファ（中身は one と TWO の 2 行）を作り、
		// diff/start でカーソルのあるバッファ（A）と見比べさせる（#245。git 連携の拡張機能の代役）。
		// 引数が数なら、作らずにその番号のバッファを B にし、notab なら、作った 2 つのバッファを見比べさせる。
		// 結果は message/show で "headdiff buf=<作った番号> split=<分割したか> err=<誤り>" と出す。
		// "update <番号> <テキスト>" なら buffer/update を、"dispose <番号>" なら buffer/dispose を送り、
		// "headdiff update version=<番号> err=<誤り>" か "headdiff dispose err=<誤り>" と出す。
		s.Command("fake.headdiff", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			var a struct{ Text string }
			_ = json.Unmarshal(p.Args, &a)
			cur := p.Buffer
			plain := func(err error) error {
				var re *rpc.Error
				if errors.As(err, &re) {
					return errors.New(re.Message)
				}
				return err
			}
			if f := strings.SplitN(a.Text, " ", 3); len(f) >= 2 && (f[0] == "update" || f[0] == "dispose") {
				n, _ := strconv.ParseUint(f[1], 10, 64)
				go func() {
					if f[0] == "dispose" {
						err := s.DisposeBuffer(hostCtx, ext.BufferID(n))
						_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("headdiff dispose err=%v", plain(err)))
						return
					}
					text := ""
					if len(f) == 3 {
						text = strings.ReplaceAll(f[2], `\n`, "\n")
					}
					r, err := s.UpdateBuffer(hostCtx, ext.BufferUpdateParams{Buffer: ext.BufferID(n), Text: text})
					_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("headdiff update version=%d err=%v", r.Version, plain(err)))
				}()
				return ext.CommandExecuteResult{OK: true}, nil
			}
			go func() {
				var created ext.BufferCreateResult
				var err error
				dp := ext.DiffStartParams{A: cur}
				if n, perr := strconv.ParseUint(a.Text, 10, 64); perr == nil {
					dp.B = ext.BufferID(n)
				} else {
					created, err = s.CreateBuffer(hostCtx, ext.BufferCreateParams{Title: "HEAD:x", Text: "one\nTWO"})
					dp.B = created.Buffer
					if err == nil && a.Text == "notab" {
						var other ext.BufferCreateResult
						other, err = s.CreateBuffer(hostCtx, ext.BufferCreateParams{Title: "other", Text: ""})
						dp.A = other.Buffer
					}
				}
				var r ext.DiffStartResult
				if err == nil {
					r, err = s.StartDiff(hostCtx, dp)
				}
				_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("headdiff buf=%d split=%v err=%v", created.Buffer, r.Split, plain(err)))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.pane は、引数（list、tree、input、text）の部品を pane/open でペインに開く（#244）。
		// 引数の前に "center " を付けると中央のポップアップに、"closing " を付けると分割したペインに、どちらも closeOnSelect を付けて開く（FR-147）。
		// "center " と "closing " の後に "focus " を書ける。
		s.Command("fake.pane", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			var a struct{ Text string }
			_ = json.Unmarshal(p.Args, &a)
			kind, center := strings.CutPrefix(a.Text, "center ")
			kind, closing := strings.CutPrefix(kind, "closing ")
			// "focus " を付けると、新しいペインへフォーカスを移して開く（ext.PaneOpenParams.Focus。#329）。
			kind, focus := strings.CutPrefix(kind, "focus ")
			params := ext.PaneOpenParams{Component: fakePopup(kind).Component, Direction: ext.PaneDown, Size: 30, CloseOnSelect: center || closing, Focus: focus}
			if center {
				params.Placement = ext.PaneCenter
			}
			go func() {
				r, err := s.OpenPane(hostCtx, params)
				_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("pane %d err=%v", r.View, err))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.exec は、引数の JSON（ext.EditorExecuteParams と、テストが送らせるまで待つかの "hold"、すぐ送るかの "now"、
		// 2 回送るかの "twice"）を覚え、次の view/select の後に、その選択の番号で editor/execute を送る（selection を書けばその番号で送る）。
		// hold なら、テストが fakeReleasePath(本体のプロセス ID, "exec") のファイルを作るまで送らない。時間で遅らせると、
		// 負荷でテストが打つキーと送る順が入れ替わるためである（#313）。
		// now なら、選択を待たずにすぐ送る。成功したら "exec ok" と出す（PR #249 の再レビューと 3 回目の確認の検証）。
		type fakeExec struct {
			ext.EditorExecuteParams
			Hold  bool `json:"hold"`
			Now   bool `json:"now"`
			Twice bool `json:"twice"`
		}
		var armed atomic.Pointer[fakeExec]
		sendExec := func(x fakeExec, selection uint64) {
			if x.Selection == 0 {
				x.Selection = selection
			}
			if x.Hold && !fakeWaitRelease(hostCtx, fakeReleasePath(os.Getppid(), "exec")) {
				_ = s.ShowMessage(ext.MessageError, "exec: not released")
				return
			}
			n := 1
			if x.Twice {
				n = 2
			}
			for range n {
				if err := s.Execute(hostCtx, x.EditorExecuteParams); err != nil {
					// 本体が出した理由を上書きしないように、断られたら出さない。
					fmt.Fprintf(os.Stderr, "exec err=%v\n", err)
					return
				}
				_ = s.ShowMessage(ext.MessageInfo, "exec ok")
			}
		}
		s.Command("fake.exec", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			var a struct{ Text string }
			_ = json.Unmarshal(p.Args, &a)
			var x fakeExec
			_ = json.Unmarshal([]byte(a.Text), &x)
			if x.Now {
				go sendExec(x, 0)
			} else {
				armed.Store(&x)
			}
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.list は、引数の一覧（tabs、commands、keys、registers、jumps）を editor/list で問い合わせ、結果の JSON を出す（FR-107）。
		s.Command("fake.list", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			var a struct{ Text string }
			_ = json.Unmarshal(p.Args, &a)
			go func() {
				r, err := s.ListEditor(hostCtx, ext.EditorListKind(a.Text))
				b, _ := json.Marshal(r)
				_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("list %s err=%v", b, err))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		s.OnView(func(_ context.Context, e *server.ViewEvent) {
			msg := fmt.Sprintf("view %d kind=%d item=%s key=%s", e.View, e.Kind, e.Item, e.Key)
			if e.Kind == server.ViewExpanded {
				msg += fmt.Sprintf(" expanded=%v", e.Expanded)
			}
			if e.Seq != 0 || e.Input != "" {
				msg += fmt.Sprintf(" seq=%d input=%s", e.Seq, e.Input)
			}
			if e.Child != "" {
				msg += " child=" + e.Child
			}
			_ = s.ShowMessage(ext.MessageInfo, msg)
			if e.Child != "" && fakeSwitcherEvent(s, e) {
				return
			}
			if e.Kind == server.ViewKey && e.Key == "fake.watch" {
				// ペインの中のキーから input/ask を送る（DAP のウォッチ式の追加。PR #411 の裁定要 1）。
				go func() {
					r, err := s.AskInput(hostCtx, ext.InputAskParams{Title: "ウォッチ式: "})
					_ = s.AppendOutput("input", fmt.Sprintf("value=%q cancelled=%v err=%v\n", r.Value, r.Cancelled, err))
				}()
			}
			switch {
			case e.Kind == server.ViewSelected && strings.HasPrefix(e.Item, "say "):
				// 書き足すテキストと入力の行の組（fakePopup の console）の入力を、出力へ書き足して返す（FR-274）。
				_ = s.AppendView(ext.ViewAppendParams{View: e.View, Text: strings.TrimPrefix(e.Item, "say ") + "\n"})
			case e.Kind == server.ViewSelected && strings.HasPrefix(e.Item, "flood "):
				// 指定の行数を、1 行ずつの view/append で書き足す。書き終えたら "flooded" と出す。
				n, _ := strconv.Atoi(strings.TrimPrefix(e.Item, "flood "))
				view := e.View
				go func() {
					for i := range n {
						_ = s.AppendView(ext.ViewAppendParams{View: view, Text: fmt.Sprintf("line %d\n", i)})
					}
					_ = s.ShowMessage(ext.MessageInfo, "flooded")
				}()
			case e.Kind == server.ViewInputChanged && strings.HasPrefix(e.Item, "f:"):
				// ファインダー（入力が f: で始まる）は、候補のうち入力の残りを含むものを、同じ seq で返す（FR-106）。
				q := strings.TrimPrefix(e.Item, "f:")
				var items []ext.ViewItem
				for _, it := range fakePopup("finder").Component.Items {
					if i := strings.Index(it.Label, q); i >= 0 {
						it.Highlights = []ext.ViewSpan{{Start: i, End: i + len(q), Style: "finder.match"}}
						items = append(items, it)
					}
				}
				_ = s.UpdateView(ext.ViewUpdateParams{View: e.View, Seq: e.Seq, Component: ext.ViewComponent{
					Kind: ext.ViewFinder, Title: fmt.Sprintf("finder %d", len(items)), Items: items,
				}})
			case e.Kind == server.ViewSelected && e.Item == "top":
				// 拡張機能から開閉を指定し直す（dir を閉じ、lazy を開いたまま子を付ける）。
				c := fakePopup("tree").Component
				closed := false
				c.Items[0].Expanded = &closed
				c.Items[1].Children = []ext.ViewItem{{ID: "lazy/x", Label: "lazy/x"}}
				_ = s.UpdateView(ext.ViewUpdateParams{View: e.View, Component: c})
			case e.Kind == server.ViewExpanded && e.Item == "lazy" && e.Expanded:
				// 子を後から読む項目の子を足して送り直す。dir の Expanded は false のままで、本体は利用者が開いた状態を保つ。
				c := fakePopup("tree").Component
				c.Items[1].Children = []ext.ViewItem{{ID: "lazy/x", Label: "lazy/x"}}
				_ = s.UpdateView(ext.ViewUpdateParams{View: e.View, Component: c})
			case e.Kind == server.ViewSelected && e.Item == "ask":
				// 選んだ項目の後に input/ask で続けて聞き、答えを出力の欄 seq に書き足す（fakePopup の asklist。#420）。
				go func() {
					r, err := s.AskInput(hostCtx, ext.InputAskParams{Title: "引数: "})
					_ = s.AppendOutput("seq", fmt.Sprintf("args value=%q cancelled=%v err=%v\n", r.Value, r.Cancelled, err))
				}()
			case e.Kind == server.ViewSelected && e.Item == "close":
				_ = s.CloseView(e.View)
			case e.Kind == server.ViewSelected && e.Item == "update":
				_ = s.UpdateView(ext.ViewUpdateParams{View: e.View, Component: ext.ViewComponent{
					Kind: ext.ViewList, Items: []ext.ViewItem{{ID: "new", Label: "updated"}},
				}})
			case (e.Kind == server.ViewSelected || e.Kind == server.ViewKey) && (e.Item == "open" || e.Item == "slow" || e.Item == "late"):
				// ファイラと同じく、選んだファイルを editor/open で開く（#244 の PR #248 のレビューの必須）。フックの中では応答を待てない。
				// 開くのは fakeOpenedPath のファイルで、開いたら "picked <項目>" を出す。
				// slow と late は、テストが fakeReleasePath のファイルを作るまで開かない（PR #248 の再レビューの必須）。
				// テストは、その間に別の client でキーを打つ（slow）か、選んだ時刻をフォーカスを戻す上限より前にずらす（late）。
				// 時間で遅らせると、負荷でテストの操作と開く順が入れ替わるためである（#313）。
				// popupKeys のキー（view/key）でも同じく開く（view/key の後はフォーカスを戻さないことを確かめるため）。
				go func() {
					if e.Item != "open" && !fakeWaitRelease(hostCtx, fakeReleasePath(os.Getppid(), e.Item)) {
						_ = s.ShowMessage(ext.MessageError, "open: not released: "+e.Item)
						return
					}
					if _, err := s.OpenEditor(hostCtx, ext.EditorOpenParams{Path: fakeOpenedPath(os.Getppid()), Focus: true}); err != nil {
						_ = s.ShowMessage(ext.MessageError, "open: "+err.Error())
						return
					}
					_ = s.ShowMessage(ext.MessageInfo, "picked "+e.Item)
				}()
			}
			// fake.exec で覚えた実行は、閉じる view/close を送った後に送る（ファインダーと同じ順）。
			if e.Kind == server.ViewSelected {
				if x := armed.Swap(nil); x != nil {
					go sendExec(*x, e.Selection)
				}
			}
		})
		s.Command("fake.jump", func(_ context.Context, b *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			id := b.ID()
			to := ext.Position{Line: min(p.Cursor.Line+1, b.LineCount()-1)}
			tx := ext.Transaction{Edits: []ext.Edit{{Range: ext.Range{Start: p.Cursor, End: p.Cursor}, Text: "*"}}}
			return ext.CommandExecuteResult{OK: true, Transaction: &tx, Open: &ext.EditorOpenParams{Buffer: &id, Position: &to}}, nil
		})
		s.Command("fake.later", func(_ context.Context, b *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			id, v, end := b.ID(), p.Version, ext.Position{Line: 0, Byte: len(b.Line(0))}
			go func() {
				_, _ = s.Edit(hostCtx, ext.BufferEditParams{Buffer: id, Transaction: ext.Transaction{
					BaseVersion: v, Edits: []ext.Edit{{Range: ext.Range{Start: end, End: end}, Text: "!"}},
				}})
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.empty、fake.lines、fake.endofline は、buffer/edit の noLines と endOfLine を試す。
		// fake.empty は全文を消して行の無いバッファにし、残りの 2 つは編集を持たずに状態だけを変える。
		lineState := func(id string, noLines, endOfLine *bool, erase bool) {
			s.Command(id, func(_ context.Context, b *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
				tx := ext.Transaction{BaseVersion: p.Version}
				if erase {
					last := b.LineCount() - 1
					tx.Edits = []ext.Edit{{Range: ext.Range{End: ext.Position{Line: last, Byte: len(b.Line(last))}}}}
				}
				go func() {
					r, err := s.Edit(hostCtx, ext.BufferEditParams{Buffer: b.ID(), Transaction: tx, NoLines: noLines, EndOfLine: endOfLine})
					_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("%s applied=%v v%d err=%v", id, r.Applied, r.Version, err))
				}()
				return ext.CommandExecuteResult{OK: true}, nil
			})
		}
		yes, no := true, false
		lineState("fake.empty", &yes, nil, true)
		lineState("fake.lines", &no, nil, false)
		lineState("fake.endofline", nil, &yes, false)
		s.Command("fake.dump", func(_ context.Context, b *server.Buffer, _ *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			if b == nil {
				// 本体がバッファを送っていない（p.Withheld。基本設計 5.3）。
				return ext.CommandExecuteResult{OK: false}, nil
			}
			_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("v%d:%q", b.Version(), b.Text()))
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.save は、カーソルのあるバッファを buffer/save（skipHooks）で保存させ、結果を表示する。
		s.Command("fake.save", func(_ context.Context, b *server.Buffer, _ *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			id := b.ID()
			go func() {
				r, err := s.SaveBuffer(hostCtx, ext.BufferSaveParams{Buffer: id, SkipHooks: true})
				_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("saved=%v err=%v", r.Saved, err))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.write は、引数の空白で区切ったパスを順に、ACP の fs/write_text_file と同じ手順（editor/open（focus が false）、
		// buffer/edit、skipHooks の buffer/save）で "written <名前>" にし、終わったら出力の欄 write に結果を書く（FR-109）。
		s.Command("fake.write", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			var args struct{ Text string }
			if err := json.Unmarshal(p.Args, &args); err != nil {
				return ext.CommandExecuteResult{}, err
			}
			go func() {
				var errs []string
				for _, path := range strings.Fields(args.Text) {
					if err := fakeWrite(hostCtx, s, path, "written "+filepath.Base(path)+"\n"); err != nil {
						errs = append(errs, err.Error())
					}
				}
				_ = s.AppendOutput("write", fmt.Sprintf("done errs=%q\n", errs))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.touch は、引数のパスを editor/open（focus が false）で開き、編集せずに buffer/save を送り、結果を出力の欄 write に書く。
		// 名前の変更を取り消した LSP の拡張機能と同じく、変えていない画面に出さないバッファを閉じさせる（#237）。
		s.Command("fake.touch", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			var args struct{ Text string }
			if err := json.Unmarshal(p.Args, &args); err != nil {
				return ext.CommandExecuteResult{}, err
			}
			go func() {
				res, err := s.OpenEditor(hostCtx, ext.EditorOpenParams{Path: args.Text, Focus: false})
				if err == nil {
					var sr ext.BufferSaveResult
					sr, err = s.SaveBuffer(hostCtx, ext.BufferSaveParams{Buffer: res.Buffer, SkipHooks: true})
					if err == nil && !sr.Saved {
						err = errors.New("not saved")
					}
				}
				_ = s.AppendOutput("write", fmt.Sprintf("done err=%v\n", err))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.echo は、受け取った command/execute の args をそのまま表示する。
		s.Command("fake.openlater", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			// 引数のパスを、応答の 300ms 後に editor/open（focus）で開き、開いたことを message/show で知らせる（#215 の client の振り分け）。
			var args struct{ Text string }
			if err := json.Unmarshal(p.Args, &args); err != nil {
				return ext.CommandExecuteResult{}, err
			}
			go func() {
				time.Sleep(300 * time.Millisecond)
				if _, err := s.OpenEditor(hostCtx, ext.EditorOpenParams{Path: args.Text, Focus: true}); err == nil {
					_ = s.ShowMessage(ext.MessageInfo, "opened later")
				}
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		s.Command("fake.echo", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("args=%s", p.Args))
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.eol は、複製の FinalNewline（buffer/open の finalNewline）を表示する。
		s.Command("fake.eol", func(_ context.Context, b *server.Buffer, _ *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("b%d finalNewline=%v", b.ID(), b.FinalNewline()))
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.ft は、複製のパスの名前とファイルの種類（buffer/open の path と fileType）を表示する。
		s.Command("fake.ft", func(_ context.Context, b *server.Buffer, _ *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("b%d %s ft=%s", b.ID(), filepath.Base(b.Path()), b.FileType()))
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.env は、本体から受け取った環境変数 ext.EnvMCPSocket の値を表示する。
		s.Command("fake.env", func(context.Context, *server.Buffer, *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			v, ok := os.LookupEnv(ext.EnvMCPSocket)
			_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("mcp=%q set=%v", v, ok))
			return ext.CommandExecuteResult{OK: true}, nil
		})
		s.Command("fake.ask", func(_ context.Context, _ *server.Buffer, _ *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			go func() {
				r, err := s.AskChoice(hostCtx, ext.ChoiceAskParams{Title: "どれ?", Choices: []ext.Choice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}})
				_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("selected=%s cancelled=%v err=%v", r.Selected, r.Cancelled, err))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.ask3 は、choice/ask の問いを 3 つ続けて送り、それぞれの結果を出力の欄 ask に書き足す（#151）。
		// 3 つ目は 1 つ目と 2 つ目より後に届くように、少し待ってから送る。
		s.Command("fake.ask3", func(context.Context, *server.Buffer, *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			for i := range 3 {
				go func() {
					time.Sleep(time.Duration(i) * 30 * time.Millisecond)
					r, err := s.AskChoice(hostCtx, ext.ChoiceAskParams{Title: "どれ?", Choices: []ext.Choice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}})
					_ = s.AppendOutput("ask", fmt.Sprintf("selected=%s cancelled=%v err=%v\n", r.Selected, r.Cancelled, err))
				}()
			}
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.askseq は、choice/ask の問いを 3 つ、前の問いの答えを受け取ってから次を送る（opencode 1.19.4 の権限の問いと同じ順。#151）。
		s.Command("fake.askseq", func(context.Context, *server.Buffer, *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			go func() {
				for range 3 {
					r, err := s.AskChoice(hostCtx, ext.ChoiceAskParams{Title: "どれ?", Choices: []ext.Choice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}})
					_ = s.AppendOutput("ask", fmt.Sprintf("selected=%s cancelled=%v err=%v\n", r.Selected, r.Cancelled, err))
				}
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.input は、input/ask で「名前: 」を聞き、結果を出力の欄 input に書き足す（FR-275）。
		// コマンドラインから引数を付けて呼ぶと（:fake.input 既定）、引数を初めの中身にする。
		s.Command("fake.input", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			var a struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(p.Args, &a)
			go func() {
				r, err := s.AskInput(hostCtx, ext.InputAskParams{Title: "名前: ", Default: a.Text})
				_ = s.AppendOutput("input", fmt.Sprintf("value=%q cancelled=%v err=%v\n", r.Value, r.Cancelled, err))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.mixed は、choice/ask、input/ask（初めの中身 x）、input/ask の 3 つの問いを、この順に届くように間を置いて続けて送り、
		// それぞれの結果を出力の欄 ask に書き足す（DAP の構成の選択の後に inputs の値を聞く順。FR-275）。
		s.Command("fake.mixed", func(context.Context, *server.Buffer, *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			go func() {
				r, err := s.AskChoice(hostCtx, ext.ChoiceAskParams{Title: "どれ?", Choices: []ext.Choice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}})
				_ = s.AppendOutput("ask", fmt.Sprintf("choice selected=%s cancelled=%v err=%v\n", r.Selected, r.Cancelled, err))
			}()
			for i, title := range []string{"式: ", "値: "} {
				go func() {
					time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
					def := ""
					if i == 0 {
						def = "x"
					}
					r, err := s.AskInput(hostCtx, ext.InputAskParams{Title: title, Default: def})
					_ = s.AppendOutput("ask", fmt.Sprintf("input%d value=%q cancelled=%v err=%v\n", i+1, r.Value, r.Cancelled, err))
				}()
			}
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.inputseq は、input/ask の「モジュール: 」に答えを受け取ってから 50 ms 待ち、続けて「引数: 」を聞き、
		// それぞれの結果を出力の欄 seq に書き足す（DAP の拡張機能の inputs が、モジュールと引数を続けて聞く順。#420）。
		// 引数が choice なら、2 つ目を choice/ask で聞く。50 ms は、答えから次の問いが届くまでの往復の間を、テストで確実に作るための待ちである。
		s.Command("fake.inputseq", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			var a struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(p.Args, &a)
			go func() {
				r, err := s.AskInput(hostCtx, ext.InputAskParams{Title: "モジュール: "})
				_ = s.AppendOutput("seq", fmt.Sprintf("module value=%q cancelled=%v err=%v\n", r.Value, r.Cancelled, err))
				if err != nil || r.Cancelled {
					return
				}
				time.Sleep(50 * time.Millisecond)
				if a.Text == "choice" {
					c, cerr := s.AskChoice(hostCtx, ext.ChoiceAskParams{Title: "どれ?", Choices: []ext.Choice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}})
					_ = s.AppendOutput("seq", fmt.Sprintf("choice selected=%s cancelled=%v err=%v\n", c.Selected, c.Cancelled, cerr))
					return
				}
				r, err = s.AskInput(hostCtx, ext.InputAskParams{Title: "引数: "})
				_ = s.AppendOutput("seq", fmt.Sprintf("args value=%q cancelled=%v err=%v\n", r.Value, r.Cancelled, err))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.inputcancel は、input/ask（引数が choice なら choice/ask）を送り、答えを待たずに 400 ms で取り消す（拡張機能の側からの取り下げ）。
		s.Command("fake.inputcancel", func(_ context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			var a struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(p.Args, &a)
			go func() {
				ctx, cancel := context.WithTimeout(hostCtx, 400*time.Millisecond)
				defer cancel()
				if a.Text == "choice" {
					_, _ = s.AskChoice(ctx, ext.ChoiceAskParams{Title: "どれ?", Choices: []ext.Choice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}})
					return
				}
				_, _ = s.AskInput(ctx, ext.InputAskParams{Title: "式: "})
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.inputexit は、input/ask を送り、答えを待つ間に拡張機能のプロセスを終わらせる（FR-275 の取り下げ）。
		s.Command("fake.inputexit", func(context.Context, *server.Buffer, *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			go func() {
				go func() { _, _ = s.AskInput(hostCtx, ext.InputAskParams{Title: "終わる: ", Default: "d"}) }()
				// テストが問いに 1 キーを打ち終えるまで待つ（負荷で遅れても打ち終えるように、長めにした）。
				time.Sleep(1500 * time.Millisecond)
				os.Exit(3)
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		s.Command("fake.state", func(_ context.Context, _ *server.Buffer, _ *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			go func() {
				st, err := s.EditorState(hostCtx)
				_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("mode=%s cursor=%d:%d buffers=%d err=%v", st.Mode, st.Cursor.Line, st.Cursor.Byte, len(st.Buffers), err))
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.focus は、少し待ってから、カーソルのない方のバッファを editor/open（focus）で開かせる。
		s.Command("fake.focus", func(context.Context, *server.Buffer, *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			go func() {
				time.Sleep(100 * time.Millisecond)
				st, err := s.EditorState(hostCtx)
				if err != nil {
					return
				}
				for _, b := range st.Buffers {
					if b.Buffer != st.Buffer {
						id := b.Buffer
						_, _ = s.OpenEditor(hostCtx, ext.EditorOpenParams{Buffer: &id, Focus: true})
						return
					}
				}
			}()
			return ext.CommandExecuteResult{OK: true}, nil
		})
		// fake.goto は、別のバッファ（複製を持つもののうち番号の最も小さいもの）の 2 行目へ移る open を、結果で返す。
		s.Command("fake.goto", func(_ context.Context, b *server.Buffer, _ *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			mu.Lock()
			defer mu.Unlock()
			var other ext.BufferID
			for id := range opened {
				if id != b.ID() && (other == 0 || id < other) {
					other = id
				}
			}
			if other == 0 {
				return ext.CommandExecuteResult{OK: false}, nil
			}
			return ext.CommandExecuteResult{OK: true, Open: &ext.EditorOpenParams{Buffer: &other, Position: &ext.Position{Line: 1}, Focus: true}}, nil
		})
		// fake.bufs は、届いた buffer/open と buffer/close の記録を、届いた順に表示する。
		s.Command("fake.bufs", func(context.Context, *server.Buffer, *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
			mu.Lock()
			defer mu.Unlock()
			_ = s.ShowMessage(ext.MessageInfo, "bufs: "+strings.Join(bufLog, ", "))
			return ext.CommandExecuteResult{OK: true}, nil
		})
		s.OnBufferOpen(func(_ context.Context, ev *server.BufferOpenEvent) {
			mu.Lock()
			defer mu.Unlock()
			opened[ev.Buffer.ID()] = true
			bufLog = append(bufLog, fmt.Sprintf("open %d %s", ev.Buffer.ID(), filepath.Base(ev.Buffer.Path())))
		})
		s.OnBufferClose(func(_ context.Context, ev *server.BufferCloseEvent) {
			mu.Lock()
			defer mu.Unlock()
			delete(opened, ev.Buffer)
			bufLog = append(bufLog, fmt.Sprintf("close %d", ev.Buffer))
		})
		s.BufferWillSave(func(_ context.Context, b *server.Buffer, _ *ext.BufferWillSaveParams) (ext.BufferWillSaveResult, error) {
			if b.LineCount() > 0 && strings.Contains(b.Line(0), "willSaveFail") {
				// 保存の前の処理が失敗したときのメッセージを確かめるテスト（TestUndoPersistWriteFailsWithExtError）のため。
				return ext.BufferWillSaveResult{}, errors.New("willSave failed")
			}
			if b.LineCount() > 0 && strings.Contains(b.Line(0), "willSaveProgress") {
				// 保存の前の整形の待ちの間に進み具合が出ることを確かめるテスト（TestProgressDuringWillSave）のため。
				// begin を送り、ファイルの隣の release ができるまで待ってから end を送る。
				_ = s.ReportProgress(ext.ProgressReportParams{Token: "fmt", Kind: ext.ProgressBegin, Title: "fake", Message: "formatting"})
				release := filepath.Join(filepath.Dir(b.Path()), "release")
				for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
					if _, err := os.Stat(release); err == nil {
						break
					}
				}
				_ = s.ReportProgress(ext.ProgressReportParams{Token: "fmt", Kind: ext.ProgressEnd, Message: "formatted"})
				return ext.BufferWillSaveResult{}, nil
			}
			for i := range b.LineCount() {
				if j := strings.Index(b.Line(i), "fmt"); j >= 0 {
					p := ext.Position{Line: i, Byte: j}
					return ext.BufferWillSaveResult{Transaction: &ext.Transaction{Edits: []ext.Edit{{
						Range: ext.Range{Start: p, End: ext.Position{Line: i, Byte: j + 3}}, Text: "FMT",
					}}}}, nil
				}
			}
			return ext.BufferWillSaveResult{}, nil
		})
	case fakeProvider:
		registerFakeProvider(s)
	case fakeChatty:
		s.OnBufferOpen(func(context.Context, *server.BufferOpenEvent) {
			for i := range 3 {
				_ = s.ShowMessage(ext.MessageInfo, fmt.Sprintf("chatty: loading %d", i+1))
			}
		})
	case fakeWindow, fakeSlowWindow:
		// 最初の行が見えていなければ、最初の行を帯にする（nvim-treesitter-context の、関数の中を見ているときの見出しの代役）。
		// fakeSlowWindow は、知らせを受けるたびにループの上で slowWindowDelay だけ待つ（本体の間引きの間隔より遅い拡張機能）。
		s.OnWindowChange(func(_ context.Context, e *server.WindowChangeEvent) {
			p := e.Params
			if p.Closed || e.Buffer == nil {
				return
			}
			if mode == fakeSlowWindow {
				time.Sleep(slowWindowDelay)
			}
			band := ext.BandSetParams{Window: p.Window, Buffer: p.Buffer, Version: p.Version, Seq: p.Seq}
			if p.Top > 0 {
				band.Lines = []ext.BandLine{{Text: e.Buffer.Line(0), Line: ext.SourceLine(0), Highlights: []ext.BandHighlight{{Start: 0, End: 4, Style: "keyword"}}}}
			}
			_ = s.SetBand(band)
		})
	case fakeSlowInit:
		s.OnInitialize(func(context.Context, *server.InitializeEvent) error {
			time.Sleep(slowInitDelay)
			return nil
		})
	}
	if err := s.Run(hostCtx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// fakeOpenedPath は、fake.pane files の項目を選んだときに偽の拡張機能が開くファイルのパスである。pid はテストのプロセスの ID である
// （偽の拡張機能はテストのプロセスの子なので、os.Getppid で同じパスを作れる）。
func fakeOpenedPath(pid int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("vi-imitation-fake-%d-opened.txt", pid))
}

// fakeReleasePath は、テストの実行ファイル（プロセス ID が pid）が、差し止めた送り出しを進めさせる印のファイルのパスである。
// item は、fake.pane の項目（slow か late）か、fake.exec の hold（exec）である。
func fakeReleasePath(pid int, item string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("vi-imitation-fake-%d-release-%s", pid, item))
}

// fakeWaitRelease は、印のファイル path ができるまで待ち、できたら消して true を返す。ctx が終わるか 30 秒で false を返す。
func fakeWaitRelease(ctx context.Context, path string) bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if err := os.Remove(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// fakePopup は、fake.popup の引数 kind の部品のポップアップの params を返す。
func fakePopup(kind string) ext.PopupOpenParams {
	switch kind {
	case "tree":
		return ext.PopupOpenParams{Placement: ext.PopupCenter, Component: ext.ViewComponent{Kind: ext.ViewTree, Title: "tree", Items: []ext.ViewItem{
			{ID: "dir", Label: "dir", Children: []ext.ViewItem{{ID: "dir/a", Label: "a"}, {ID: "dir/b", Label: "b"}}},
			{ID: "lazy", Label: "lazy", HasChildren: true},
			{ID: "top", Label: "top"},
		}}}
	case "input":
		return ext.PopupOpenParams{Placement: ext.PopupCenter, Component: ext.ViewComponent{Kind: ext.ViewInput, Title: "name", Text: "old"}}
	case "files":
		return ext.PopupOpenParams{Placement: ext.PopupCenter, Component: ext.ViewComponent{Kind: ext.ViewList, Title: "files", Items: []ext.ViewItem{
			{ID: "open", Label: "opened.txt"}, {ID: "slow", Label: "slow"}, {ID: "late", Label: "late"},
		}}}
	case "asklist":
		// 選ぶと input/ask で続けて聞く一覧（DAP の構成の一覧の後の inputs。#420）。
		return ext.PopupOpenParams{Placement: ext.PopupCenter, Component: ext.ViewComponent{Kind: ext.ViewList, Title: "asklist", Items: []ext.ViewItem{
			{ID: "ask", Label: "ask"},
		}}}
	case "finder":
		// placement を書かなくても、本体はファインダーを中央に出す。
		return ext.PopupOpenParams{Component: ext.ViewComponent{Kind: ext.ViewFinder, Title: "finder", Text: "f:", Items: []ext.ViewItem{
			{ID: "a", Label: "apple"}, {ID: "b", Label: "banana"}, {ID: "c", Label: "cherry"},
		}}}
	case "text":
		return ext.PopupOpenParams{Component: ext.ViewComponent{
			Kind: ext.ViewText, Text: "alpha beta\ngamma",
			Highlights: []ext.Decoration{{Range: ext.Range{End: ext.Position{Byte: 5}}, Style: "keyword"}},
		}}
	case "switcher", "switcher1":
		// 切り替えの部品（FR-269）。木（キー T）、一覧（キー L）、テキスト（キー無し）、組（キー R）の 4 つの子を持つ。switcher1 は木の子だけを持つ。
		// 組の子で say <文字列> を送ると、偽の拡張機能はその文字列の行を見えていないテキストの子 out へ書き足す（fakeSwitcherAppend）。
		children := []ext.ViewChild{
			{ID: "tree", Title: "Tree", Key: "T", Component: fakePopup("tree").Component},
			{ID: "list", Title: "List", Key: "L", Component: fakePopup("list").Component},
			{ID: "out", Title: "Out", Component: ext.ViewComponent{Kind: ext.ViewText, Text: "start\n", Tail: true}},
			{ID: "repl", Title: "Repl", Key: "R", Component: ext.ViewComponent{Kind: ext.ViewConsole, Text: "repl\n", Prompt: "> "}},
		}
		if kind == "switcher1" {
			children = children[:1]
		}
		return ext.PopupOpenParams{Placement: ext.PopupCenter, Component: ext.ViewComponent{Kind: ext.ViewSwitcher, Title: "dap", Children: children}}
	case "console":
		// 書き足すテキストと入力の行の組（FR-274）。placement を書かなくても、本体は中央に出す。
		// 入力の行で say <文字列> を送ると、偽の拡張機能はその文字列の行を出力へ書き足し、flood <行数> を送ると、その行数を書き足す。
		return ext.PopupOpenParams{Component: ext.ViewComponent{
			Kind: ext.ViewConsole, Title: "console", Text: "welcome\n", Prompt: "> ", History: []string{"h1"},
		}}
	}
	return ext.PopupOpenParams{Placement: ext.PopupCenter, Component: ext.ViewComponent{Kind: ext.ViewList, Title: "list", Items: []ext.ViewItem{
		{ID: "one", Label: "first", Highlights: []ext.ViewSpan{{Start: 0, End: 5, Style: "keyword"}}},
		{ID: "close", Label: "close me"},
		{ID: "update", Label: "update me"},
	}}}
}

// fakeSwitcherEvent は、切り替えの部品（fakePopup の switcher）の子の知らせ e に応え、応えたら true を返す（#416）。
//
//   - 組の子 repl で say <文字列> を送ると、その文字列の行を見えていないテキストの子 out へ書き足す。flood <行数> なら、その行数を
//     1 行ずつ out へ書き足し、書き終えたら "flooded" と出す
//   - 一覧の子 list で update を選ぶと、list の項目を置き換え、見えていない out の中身を "replaced" にする（子を指す view/update）
//   - 一覧の子 list で zs を打つと、部品の全体を置き換える（木の子を捨て、out を先頭に並べ直して中身を替え、out を選ぶ）
func fakeSwitcherEvent(s *server.Server, e *server.ViewEvent) bool {
	view := e.View
	switch {
	case e.Kind == server.ViewSelected && e.Child == "repl" && strings.HasPrefix(e.Item, "say "):
		_ = s.AppendView(ext.ViewAppendParams{View: view, Child: "out", Text: strings.TrimPrefix(e.Item, "say ") + "\n"})
	case e.Kind == server.ViewSelected && e.Child == "repl" && strings.HasPrefix(e.Item, "flood "):
		n, _ := strconv.Atoi(strings.TrimPrefix(e.Item, "flood "))
		go func() {
			for i := range n {
				_ = s.AppendView(ext.ViewAppendParams{View: view, Child: "out", Text: fmt.Sprintf("line %d\n", i)})
			}
			_ = s.ShowMessage(ext.MessageInfo, "flooded")
		}()
	case e.Kind == server.ViewSelected && e.Child == "list" && e.Item == "update":
		_ = s.UpdateView(ext.ViewUpdateParams{View: view, Child: "list", Component: ext.ViewComponent{
			Kind: ext.ViewList, Items: []ext.ViewItem{{ID: "new", Label: "updated"}},
		}})
		_ = s.UpdateView(ext.ViewUpdateParams{View: view, Child: "out", Component: ext.ViewComponent{Kind: ext.ViewText, Text: "replaced\n"}})
	case e.Kind == server.ViewKey && e.Key == "fake.swap":
		c := fakePopup("switcher").Component
		out := c.Children[2]
		out.Component.Text = "swapped\n"
		c.Children = []ext.ViewChild{out, c.Children[1], c.Children[3]}
		c.Selected = "out"
		_ = s.UpdateView(ext.ViewUpdateParams{View: view, Component: c})
	default:
		return false
	}
	return true
}

// fakeSwitcherChildren は、fakePopup の switcher の子の ID である。
var fakeSwitcherChildren = []string{"tree", "list", "out", "repl"}

// fakeSpecs は、テスト用の拡張機能の定義を返す。
func fakeSpecs(t *testing.T) []exthost.Spec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contrib := func(id, keys string) ext.Contribution { return ext.Contribution{ID: id, Keys: keys} }
	act := func(kind ext.ActivationKind, id string) ext.Activation { return ext.Activation{On: kind, ID: id} }
	ms := []ext.Manifest{
		{
			ID: "fake", Command: exe, Args: []string{fakeExtFlag, fakeMain}, SaveHook: true,
			Activation: []ext.Activation{
				act(ext.ActivationTextObject, "fake.line"), act(ext.ActivationTextObject, "fake.slow"),
				act(ext.ActivationTextObject, "fake.hang"), act(ext.ActivationCommand, "fake.upper"),
				act(ext.ActivationCommand, "fake.echo"), act(ext.ActivationCommand, "fake.openlater"),
				act(ext.ActivationCommand, "fake.write"), act(ext.ActivationCommand, "fake.touch"),
				act(ext.ActivationCommand, "fake.popup"), act(ext.ActivationCommand, "fake.headdiff"),
				act(ext.ActivationCommand, "fake.pane"), act(ext.ActivationCommand, "fake.list"), act(ext.ActivationCommand, "fake.exec"),
				act(ext.ActivationCommand, "fake.input"), act(ext.ActivationCommand, "fake.inputcancel"),
				act(ext.ActivationCommand, "fake.inputseq"),
			},
			Contributes: ext.Contributes{
				TextObjects: []ext.Contribution{contrib("fake.line", "il"), contrib("fake.slow", "is"), contrib("fake.hang", "ih")},
				Commands: []ext.Contribution{
					contrib("fake.upper", `\u`), contrib("fake.jump", `\j`), contrib("fake.later", `\l`),
					contrib("fake.dump", `\d`), contrib("fake.ask", `\a`), contrib("fake.state", `\s`),
					contrib("fake.focus", `\f`), contrib("fake.goto", `\g`), contrib("fake.eol", `\e`),
					contrib("fake.env", `\v`), contrib("fake.empty", `\z`), contrib("fake.lines", `\n`),
					contrib("fake.endofline", `\o`), contrib("fake.echo", ""), contrib("fake.openlater", ""), contrib("fake.save", `\w`),
					contrib("fake.bufs", `\b`), contrib("fake.ask3", `\3`), contrib("fake.askseq", `\4`),
					contrib("fake.write", ""), contrib("fake.touch", ""), contrib("fake.ft", `\y`),
					contrib("fake.popup", ""), contrib("fake.headdiff", ""), contrib("fake.pane", ""), contrib("fake.list", ""),
					contrib("fake.exec", ""), contrib("fake.input", `\i`), contrib("fake.mixed", `\m`), contrib("fake.inputexit", `\x`),
					contrib("fake.inputcancel", ""), contrib("fake.inputseq", ""),
				},
				// ポップアップの中だけで効くキー（FR-69）。x は本体では文字を消すキーで、ポップアップの中ではこちらが先に効く。
				// gx は g で始まるので、gg はキーの列が当たらずに本体の gg として動く。
				// <Tab> と <S-Tab> は、切り替えの部品（fakePopup の switcher）の子の中でだけ、子を切り替える（DAP の拡張機能の形。#416）。
				// d と zs は、一覧の子 list の中でだけ効く（FR-271）。
				PopupKeys: []ext.Contribution{
					contrib("fake.pick", "x"), contrib("fake.two", "zq"), contrib("fake.gx", "gx"), contrib("fake.watch", "zw"),
					{ID: "fake.next", Keys: "<Tab>", Action: ext.PopupKeyChildNext, Children: fakeSwitcherChildren},
					{ID: "fake.prev", Keys: "<S-Tab>", Action: ext.PopupKeyChildPrev, Children: fakeSwitcherChildren},
					{ID: "fake.del", Keys: "d", Children: []string{"list"}},
					{ID: "fake.swap", Keys: "zs", Children: []string{"list"}},
				},
			},
		},
		{
			ID: "ftx", Command: exe, Args: []string{fakeExtFlag, fakeFileType},
			Activation: []ext.Activation{{On: ext.ActivationFileType, Pattern: "fakeft"}},
		},
		{
			ID: "slow", Command: exe, Args: []string{fakeExtFlag, fakeSlowInit},
			Activation: []ext.Activation{{On: ext.ActivationFileType, Pattern: "slowft"}},
		},
	}
	if _, err := ext.ValidateManifests(ms); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	specs := make([]exthost.Spec, len(ms))
	for i, m := range ms {
		specs[i] = exthost.Spec{Manifest: m, Dir: dir}
	}
	return specs
}

// fakeTerm は、テスト用の偽の端末である。キーを送り、最後に描いた画面を持つ。
type fakeTerm struct {
	in chan TermEvent
	// closeIn は、in を 1 回だけ閉じるためのものである（closeInput）。
	closeIn sync.Once

	mu   sync.Mutex
	last ui.Screen
	// clips は、SetClipboard で送られたテキストを、送られた順に並べたものである。
	clips []string
	// w と h は、Size が返す大きさである。w が 0 なら 120 列と 6 行である。
	w, h int
	// onDraw は、nil でなければ、Draw のたびに（描いた画面を持った後、mu を持たずに）呼ぶ。Run の前に入れる。
	onDraw func()
}

// closeInput は、端末の入力を閉じる（端末が失われた）。何度呼んでもよい。
func (f *fakeTerm) closeInput() { f.closeIn.Do(func() { close(f.in) }) }

func (f *fakeTerm) SetClipboard(text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clips = append(f.clips, text)
	return nil
}

// clipboard は、SetClipboard で送られたテキストの複製を返す。
func (f *fakeTerm) clipboard() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.clips)
}

func newFakeTerm() *fakeTerm { return &fakeTerm{in: make(chan TermEvent, 1024)} }

func (f *fakeTerm) Input() <-chan TermEvent { return f.in }

func (f *fakeTerm) Size() (int, int) {
	if f.w > 0 {
		return f.w, f.h
	}
	return 120, 6
}

func (f *fakeTerm) Draw(s ui.Screen) error {
	f.mu.Lock()
	f.last = s
	f.mu.Unlock()
	if f.onDraw != nil {
		f.onDraw()
	}
	return nil
}

func (f *fakeTerm) screen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last.Grid == nil {
		return ""
	}
	return f.last.Grid.String()
}

// harness は、偽の端末で動かしたエディタである。
type harness struct {
	t      *testing.T
	e      *Editor
	term   *fakeTerm
	done   chan error
	sent   int64
	cancel context.CancelFunc
	// peers は、Attach で足した client の偽の端末である。後始末で入力を閉じる。
	peers []*fakeTerm
}

// start は、cfg でエディタを作り、files を開いて、別の goroutine で Run する。テストの終わりに止めて Close する。
func start(t *testing.T, cfg Config, files ...string) *harness {
	t.Helper()
	return startTerm(t, newFakeTerm(), cfg, files...)
}

// startTerm は、start の、偽の端末 term（大きさを変えたもの）を使う形である。
func startTerm(t *testing.T, term *fakeTerm, cfg Config, files ...string) *harness {
	t.Helper()
	return startTermWith(t, term, cfg, func(e *Editor) error {
		for _, f := range files {
			if err := e.Open(f); err != nil {
				return err
			}
		}
		return nil
	})
}

// startWith は、start と同じく cfg でエディタを作り、Run の前に open を呼んでから、別の goroutine で Run する。
func startWith(t *testing.T, cfg Config, open func(e *Editor) error) *harness {
	t.Helper()
	return startTermWith(t, newFakeTerm(), cfg, open)
}

// startTermWith は、startWith の、偽の端末 term を使う形である。
func startTermWith(t *testing.T, term *fakeTerm, cfg Config, open func(e *Editor) error) *harness {
	t.Helper()
	if cfg.WorkDir == "" {
		cfg.WorkDir = t.TempDir()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	e, err := New(term, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := open(e); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, e: e, term: term, done: make(chan error, 1), cancel: cancel}
	go func() { h.done <- e.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-h.done:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrTerminalLost) {
				t.Errorf("Run = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run が終わらない")
		}
		// 入力を読む goroutine（forwardInput）を残さないよう、偽の端末の入力を閉じる（PR #223 のレビューの並行処理の節）。
		term.closeInput()
		for _, p := range h.peers {
			p.closeInput()
		}
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := e.Close(closeCtx); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	return h
}

// keys は、Vim の表記のキーの列を端末から送る。
func (h *harness) keys(s string) {
	h.t.Helper()
	for _, k := range editcmd.MustParseKeys(s) {
		h.term.in <- TermEvent{Key: k}
		h.sent++
	}
}

// eventually は、cond がイベントループの goroutine で true を返すまで待つ。
func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var ok bool
		h.e.do(func() { ok = cond() })
		if ok {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("待ちきれない: %s（text=%q, message=%q）", what, h.text(), h.message())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settle は、端末から送ったキーを、イベントループがすべて処理し終えるまで待つ。
// 再生中の拡張機能の結果を待っている（replayWait）間は、キーの処理が終わっていないものとして待つ。
func (h *harness) settle() {
	h.t.Helper()
	h.eventually("キーの処理", func() bool {
		return h.e.forwarded.Load() == h.sent && h.e.keys.empty() && !h.e.replayPending()
	})
}

func (h *harness) text() string {
	var s string
	h.e.do(func() { s = h.e.cur.buf.Text() })
	return s
}

func (h *harness) message() string {
	var s string
	h.e.do(func() { s = h.e.message.Text })
	return s
}

// waitPopupOpened は、fake.popup が popup/open の応答の後に出す "opened <view> err=<nil>" を、イベントループが処理するまで待つ。
// fake.popup はこの知らせを popup/open の応答を待つ goroutine から出すので、負荷で遅れると、利用者の操作の知らせ（OnView）の後に届き、
// そのメッセージを上書きする。waitMessage はいまのメッセージだけを見るので、上書きされると待ちきれなくなる（#313）。
// ポップアップの操作を waitMessage で確かめるテストは、開いた後にこれを呼んでから操作する。
func (h *harness) waitPopupOpened(view uint64) {
	h.t.Helper()
	h.waitShown(fmt.Sprintf("opened %d err=<nil>", view))
}

// waitPaneOpened は、fake.pane が pane/open の応答の後に出す "pane <view> err=<nil>" を、イベントループが処理するまで待つ。
// waitPopupOpened と同じ理由で、ペインの操作を waitMessage で確かめるテストは、開いた後にこれを呼んでから操作する（#313）。
func (h *harness) waitPaneOpened(view uint64) {
	h.t.Helper()
	h.waitShown(fmt.Sprintf("pane %d err=<nil>", view))
}

// waitShown は、text と同じメッセージがメッセージの履歴に入るまで待つ。
func (h *harness) waitShown(text string) {
	h.t.Helper()
	h.eventually(text, func() bool {
		return slices.ContainsFunc(h.e.msgHistory, func(m ui.Message) bool { return m.Text == text })
	})
}

func (h *harness) state(id string) exthost.State {
	st, _ := h.e.host.State(id)
	return st
}

func durationPtr(d time.Duration) *time.Duration { return &d }
