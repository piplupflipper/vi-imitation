// Package mux は、端末多重化ソフト（tmux と herdr）を操作する層である（基本設計 1.3。FR-66、FR-67）。
//
// server だけがこの層を使う。拡張機能はこの層を直接使わず、本体へのリクエスト（pane/open、popup/open）で頼む（FR-115）。
// このパッケージは、どのペインに何を出すかを知らない。呼び出し側が、起動するコマンドと環境変数を渡す。
//
// # 実装
//
// Detect が環境変数から、tmux 用、herdr 用、どちらでもないときの 3 つの実装のどれかを選ぶ。
// 判定の順は、server のソケットのパスの規則（基本設計 1.2）と同じく、tmux、herdr、どちらでもない、の順である。
//
//   - tmux: tmux のコマンドを、環境変数 TMUX の先頭のソケットのパスを -S に渡して起動する（tmux.go）
//   - herdr: herdr の socket API（protocol 22）へ、改行で区切った JSON のリクエストを送る（herdr.go）
//   - どちらでもない（FR-60）: どの操作も ErrUnsupported を返す
//
// # 並行性
//
// メソッドは複数の goroutine から同時に呼んでよい（tmux の実装が持つ、開いたポップアップの表は mutex で守る）。
// どの操作も外部のコマンドかソケットを使うので時間がかかる。
// イベントループ（基本設計 2.1）から同期で呼ばず、別の goroutine で呼んで結果をチャネルで受ける前提である。
package mux

import (
	"context"
	"errors"
	"maps"
	"strings"
)

// Kind は、端末多重化ソフトの種類である。
type Kind int

// 端末多重化ソフトの種類。
const (
	// KindNone は、端末多重化ソフトの外である（FR-60）。
	KindNone Kind = iota
	// KindTmux は、tmux の中である。
	KindTmux
	// KindHerdr は、herdr の中である。
	KindHerdr
)

// String は、種類の名前を返す。
func (k Kind) String() string {
	switch k {
	case KindNone:
		return "none"
	case KindTmux:
		return "tmux"
	case KindHerdr:
		return "herdr"
	}
	return "unknown"
}

// ErrUnsupported は、端末多重化ソフトの外（FR-60）で、操作ができないことを表す。
// 呼び出し側は、ポップアップを編集の client の画面の中央に描くなど、代わりの手段を取る（基本設計 1.2、1.4）。
var ErrUnsupported = errors.New("mux: no terminal multiplexer")

// ErrPopupBusy は、ポップアップが既に開いていて、新しいポップアップを開けなかったことを表す。
// tmux も herdr も、1 つの画面（tmux の client、herdr の端末）にポップアップを 1 つしか開かない。
var ErrPopupBusy = errors.New("mux: a popup is already open")

// ErrNoTarget は、元の client のペイン（SplitOptions.Target、PopupOptions.Target）が空であることを表す。
var ErrNoTarget = errors.New("mux: empty target pane")

// Direction は、ペインを分割する向きである。
type Direction int

// 分割の向き。新しいペインは、元のペインの右、下、左のどれかに開く。
const (
	// Right は、元のペインの右に開く（tmux の split-window -h、herdr の right）。
	Right Direction = iota
	// Down は、元のペインの下に開く（tmux の split-window -v、herdr の down）。
	Down
	// Left は、元のペインの左に開く（tmux の split-window -h -b）。
	// herdr 0.9.0 の plugin.pane.open は分割の向きに right と down しか受け付けないので、herdr では右に開いてから
	// pane.swap で元のペインと入れ替え、Focus のとおりにフォーカスを移し直す（Herdr.SplitPane。#437）。
	Left
)

// Command は、ペインかポップアップの中で起動するコマンドである。
type Command struct {
	// Args は、起動するプログラムとその引数である。空にしない。
	// 端末多重化ソフトにはシェルのコマンドの文字列として渡すので、各要素はシェルの引用で囲んで渡す。
	Args []string
	// Env は、足す環境変数である（server のソケットのパス VI_IMITATION_SOCKET など。基本設計 1.3）。
	Env map[string]string
	// Dir は、起動するディレクトリである。空なら端末多重化ソフトの既定に任せる。
	Dir string
}

// SplitOptions は、ペインを分割して開くときの指定である。
type SplitOptions struct {
	// Target は、分割する元のペインの識別子である（元の client のペイン）。空にしない（ErrNoTarget）。
	// server は、server を起動した編集の client より長く動き（FR-61）、そのペインは閉じたり別の window へ動いたり（FR-112）するので、
	// Mux.CurrentPane（server の環境変数のペイン）を既定にせず、常に元の client のペインを渡す。
	Target string
	// Direction は、分割の向きである。
	Direction Direction
	// Size は、新しいペインの大きさを、分割する向きの元のペインの大きさに対する百分率（1〜100）で表す。
	// 0 なら端末多重化ソフトの既定に任せる。
	// herdr 0.9.0 は、プラグインのペインを分割で開くときの大きさを受け付けないので、herdr では無視する。
	Size int
	// Command は、新しいペインで起動するコマンドである。
	Command Command
	// Focus は、新しいペインへフォーカスを移すかである。
	Focus bool
}

// PopupOptions は、画面の中央のポップアップを開くときの指定である（FR-67）。
type PopupOptions struct {
	// Target は、ポップアップを開いた元のペイン（元の client のペイン）の識別子である。空にしない（ErrNoTarget。SplitOptions.Target と同じ理由）。
	// tmux では、このペインの window を表示している tmux の client の上に開く。
	// herdr 0.9.0 は、ポップアップをアクティブなペインの上にしか開けないので、herdr では使わない。
	// このため herdr では、キーを受けてから開くまでの間に利用者が別のタブへ移ると、移った先のタブの上に開く。
	Target string
	// Width と Height は、端末全体に対する百分率（1〜100）である。0 なら端末多重化ソフトの既定に任せる。
	Width, Height int
	// Command は、ポップアップの中で起動するコマンドである。コマンドが終わるとポップアップは閉じる。
	Command Command
	// KeepOnFailure は、コマンドが 0 でない終了コードで終わったときに、ポップアップを閉じずに残すことを表す（FR-168）。
	// 残したポップアップには、コマンドの最後の出力と、終了コードと閉じ方の案内が見えたままになり、利用者が何かキーを打つと閉じる。
	// tmux も herdr も、コマンドを同じ sh のスクリプトで包む（keepOnFailure）。
	// false なら、終了コードに依らずコマンドが終わると閉じる（tmux の display-popup -E。部品の client）。
	KeepOnFailure bool
}

// Mux は、端末多重化ソフトを操作する口である（基本設計 1.3）。
//
// ペインの識別子は、tmux では %1 のようなペインの ID、herdr では wW:p7 のようなペインの ID である。
type Mux interface {
	// Kind は、端末多重化ソフトの種類を返す。
	Kind() Kind

	// CurrentPane は、Detect に渡した環境変数のペインの識別子を返す。
	// 編集の client が起動したときに、server へ自分のペインを知らせる（基本設計 1.2）のに使う。
	// server の中で呼ぶと、server を起動した編集の client のペインが返り、今の元の client のペインとは限らない。
	CurrentPane(ctx context.Context) (string, error)

	// Unit は、Detect に渡した環境変数のペインの、server の単位（tmux の window、herdr のタブ）を、
	// ほかの単位と区別できる文字列で返す（基本設計 1.2）。
	// 編集の client が起動したときに、この文字列から server のソケットのパスを作る（FR-59）のに使う。
	// 端末多重化ソフトのサーバー（tmux のソケット、herdr のソケット）が異なれば、識別子が同じでも文字列は異なる。
	Unit(ctx context.Context) (string, error)

	// SplitPane は、ペインを分割して開き、中でコマンドを起動し、新しいペインの識別子を返す。
	// コマンドが終わるとペインは閉じる。
	SplitPane(ctx context.Context, opts SplitOptions) (string, error)

	// OpenPopup は、画面の中央のポップアップを開き、中でコマンドを起動する（FR-67、FR-168）。
	// コマンドが終わるとポップアップは閉じる（PopupOptions.KeepOnFailure なら、0 で終わったときだけ閉じる）。
	// 中のコマンドが起動したら、終わりを待たずに戻る。
	// ポップアップが既に開いていて開けないときは ErrPopupBusy を返す。開いているのが誰のポップアップでも同じである。
	OpenPopup(ctx context.Context, opts PopupOptions) error

	// ClosePopup は、開いているポップアップを外から閉じる。target は、OpenPopup の PopupOptions.Target と同じものを渡す。
	//
	// tmux では、この層が target に開いたポップアップがまだ開いているときだけ閉じ、そうでなければ何もせずに nil を返す。
	// herdr では、開いているポップアップを、誰が開いたものかを問わず閉じる（popup.close は引数を取らず、
	// 開いたポップアップの ID も返らないため。herdr 0.9.0）。利用者が入れた他のプラグイン（starkeep など）のポップアップも閉じうる。
	// このため server が自分のポップアップを閉じるときは、ポップアップの部品の client へ server のソケットで終わりを伝える形を基本にし、
	// ClosePopup は部品の client が応答しないときの最後の手段にする。
	ClosePopup(ctx context.Context, target string) error

	// FocusPane は、from のペインから pane のペインへフォーカスを移す（FR-63）。
	// ペインが別の window（タブ）にあれば、その window（タブ）へ移る。移せないときは誤りを返す。
	// from は、操作を受けた元の client のペインで、空にしない（ErrNoTarget）。
	// tmux では、from のペインの window を表示している tmux の client を切り替える。herdr では使わない。
	FocusPane(ctx context.Context, from, pane string) error

	// ClosePane は、ペインを閉じる。
	ClosePane(ctx context.Context, pane string) error

	// PaneFocused は、ペインが利用者の見ているアクティブなペインかを返す（#244）。
	// tmux では、そのペインが window のアクティブなペインで、その window をどれかの tmux の client が表示していることである。
	// herdr では、pane.get の focused である。ペインが無ければ誤りを返す。
	// ペインの部品で選んだ後に元の client のペインへフォーカスを戻す前に、利用者がまだそのペインにいるかを確かめるのに使う。
	PaneFocused(ctx context.Context, pane string) (bool, error)
}

// ScreenWidther は、ポップアップを出す端末の幅を返せる端末多重化ソフトの層である（tmux。herdr は持たない）。
// PopupOptions.Width の百分率は端末の幅に対するものなので、開くポップアップの幅を前もって知るのに使う（FR-149）。
type ScreenWidther interface {
	// ScreenWidth は、OpenPopup で target のペインの上にポップアップを出す先の端末の列数を返す。
	ScreenWidth(ctx context.Context, target string) (int, error)
}

// Detect は、環境変数から端末多重化ソフトを判定し、その実装を返す。
// getenv には os.Getenv を渡す。テストでは偽の環境を渡す。
//
//   - TMUX と TMUX_PANE があれば tmux（tmux のソケットのパスは TMUX の最初のカンマの前）
//   - HERDR_ENV が 1 で、HERDR_PANE_ID と HERDR_SOCKET_PATH があれば herdr
//   - どれでもなければ、どの操作も ErrUnsupported を返す実装
func Detect(getenv func(string) string) Mux {
	if tmux, pane := getenv("TMUX"), getenv("TMUX_PANE"); tmux != "" && pane != "" {
		socket, _, _ := strings.Cut(tmux, ",")
		return &Tmux{Bin: "tmux", Socket: socket, Pane: pane}
	}
	if getenv("HERDR_ENV") == "1" {
		if pane, socket := getenv("HERDR_PANE_ID"), getenv("HERDR_SOCKET_PATH"); pane != "" && socket != "" {
			return &Herdr{Socket: socket, Pane: pane}
		}
	}
	return None{}
}

// None は、端末多重化ソフトの外（FR-60）の実装である。Kind を除くどの操作も ErrUnsupported を返す。
type None struct{}

var _ Mux = None{}

// Kind は KindNone を返す。
func (None) Kind() Kind { return KindNone }

// CurrentPane は ErrUnsupported を返す。
func (None) CurrentPane(context.Context) (string, error) { return "", ErrUnsupported }

// Unit は ErrUnsupported を返す。
func (None) Unit(context.Context) (string, error) { return "", ErrUnsupported }

// SplitPane は ErrUnsupported を返す。
func (None) SplitPane(context.Context, SplitOptions) (string, error) { return "", ErrUnsupported }

// OpenPopup は ErrUnsupported を返す。
func (None) OpenPopup(context.Context, PopupOptions) error { return ErrUnsupported }

// ClosePopup は ErrUnsupported を返す。
func (None) ClosePopup(context.Context, string) error { return ErrUnsupported }

// FocusPane は ErrUnsupported を返す。
func (None) FocusPane(context.Context, string, string) error { return ErrUnsupported }

// ClosePane は ErrUnsupported を返す。
func (None) ClosePane(context.Context, string) error { return ErrUnsupported }

// PaneFocused は ErrUnsupported を返す。
func (None) PaneFocused(context.Context, string) (bool, error) { return false, ErrUnsupported }

// keepScript は、PopupOptions.KeepOnFailure のときにポップアップのコマンドを包む sh のスクリプトである（FR-168）。
// 引数のコマンドを動かし、0 で終わればそのまま終わる（ポップアップが閉じる）。
// 0 でなければ、終了コードと閉じ方を出し、端末を 1 文字ずつ読む形にして、何かキーを 1 つ読んでから同じ終了コードで終わる。
// キーを待つ前に、コマンドの実行中に打って端末に溜まっていた入力を、待たずに読む形（min 0 time 0）で読み捨てる。
// 溜まった入力で、出力を読む前に閉じないためである（PR #323 の再レビューの推奨。実行中の j 1 つで閉じた）。
// 1 回の読み捨ては 4096 バイトまでで、それより多く溜まっていれば残りで閉じうる。
// :! の後の問い（internal/client の handover.go）と同じく終了コードを出し、q で閉じる癖のある TUI の利用者が q でも閉じられるように、
// Enter に限らずどのキーでも閉じる。端末でなければ（テスト）stty は失敗し、標準入力の 1 バイトを読む。
//
// tmux の display-popup -EE（0 で終わったときだけ閉じる）は、残したポップアップに閉じ方を出さず、Escape と C-c でしか閉じない
// （2026-11-21 に tmux 3.6 で確かめた。q、Enter、Space では閉じなかった。PR #323 のレビュー）。herdr 0.9.0 のプラグインのポップアップには
// 同じ指定が無い。このため、どちらもこのスクリプトで包み、tmux は display-popup -E で開く。
const keepScript = `"$@"; s=$?; [ "$s" -eq 0 ] && exit 0; ` +
	`printf '\nシェルが %d を返しました\n閉じるには何かキーを押してください' "$s"; ` +
	`stty -icanon -echo min 0 time 0 2>/dev/null && dd bs=4096 count=1 >/dev/null 2>&1; ` +
	`stty min 1 2>/dev/null; dd bs=1 count=1 >/dev/null 2>&1; exit "$s"`

// herdrPaneEnv は、herdr のポップアップの中へ下のペインの識別子を渡す環境変数の名前である（InPane）。
const herdrPaneEnv = "VI_IMITATION_MUX_PANE"

// herdrPaneScript は、herdrPaneEnv の値を HERDR_PANE_ID に入れてから、引数のコマンドを exec する sh のスクリプトである（InPane）。
const herdrPaneScript = `HERDR_PANE_ID=$` + herdrPaneEnv + `; export HERDR_PANE_ID; unset ` + herdrPaneEnv + `; exec "$@"`

// InPane は、ペイン pane の上に開くポップアップの中で c を起動したときに、c とその子のプロセスが、pane で起動したプロセスと同じく
// 端末多重化ソフトと自分のペインを判定できる（Detect が同じ実装を選び、Unit が pane の window かタブを返す）ように、
// c の環境変数を足すか、c を包んだコマンドを返す（FR-168）。端末多重化ソフトの外（None）なら c をそのまま返す。
// ポップアップの中で起動した vi-imitation が、pane で起動したときと同じ server（同じ window かタブの server。FR-59）に繋ぐためである。
//
// tmux 3.6 の display-popup は、ポップアップの中に TMUX は入れるが、TMUX_PANE は入れない（中の TMUX_PANE は tmux のグローバルな環境の値で、
// ふつうは無い。2026-11-21 に確かめた。PR #323 の再レビュー）。このため -e で TMUX_PANE に pane を入れる。
// herdr 0.9.0 のプラグインのポップアップには、HERDR_ENV と HERDR_SOCKET_PATH は入るが HERDR_PANE_ID は入らず、plugin.pane.open の env で
// HERDR_PANE_ID を渡しても中のプロセスに届かなかった（2026-11-21 に確かめた。ほかの名前の環境変数は届いた）。
// このため別の名前（herdrPaneEnv）で渡し、中で HERDR_PANE_ID に入れ直してから c を exec する sh のスクリプト（herdrPaneScript）で包む。
func InPane(m Mux, pane string, c Command) Command {
	switch m.(type) {
	case *Tmux:
		c.Env = withEnv(c.Env, "TMUX_PANE", pane)
	case *Herdr:
		c.Env = withEnv(c.Env, herdrPaneEnv, pane)
		c.Args = append([]string{"sh", "-c", herdrPaneScript, "sh"}, c.Args...)
	}
	return c
}

// withEnv は、env の写しに name=value を足したものを返す。
func withEnv(env map[string]string, name, value string) map[string]string {
	out := make(map[string]string, len(env)+1)
	maps.Copy(out, env)
	out[name] = value
	return out
}

// keepOnFailure は、c のコマンドを keepScript で包んだコマンドを返す。
func keepOnFailure(c Command) Command {
	c.Args = append([]string{"sh", "-c", keepScript, "sh"}, c.Args...)
	return c
}

// shellJoin は、args の各要素を POSIX のシェルの単一引用で囲み、空白でつないだ 1 つのコマンドの文字列にする。
// 引用の要らない文字だけの要素は囲まない。
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuote は、s を POSIX のシェルで 1 つの語として読まれる形にする。
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !isShellSafe(r) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isShellSafe は、r がシェルの引用なしで書いてよい文字かを返す。
// = は含めない。先頭の語の a=b は、シェルが環境変数の代入として読むためである。
func isShellSafe(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return true
	}
	return strings.ContainsRune("-_./:@%+,", r)
}

// checkCommand は、元のペインと起動するコマンドが空でないことを確かめる。
func checkCommand(target string, c Command) error {
	if target == "" {
		return ErrNoTarget
	}
	if len(c.Args) == 0 {
		return errors.New("mux: empty command")
	}
	return nil
}

// checkPercent は、百分率の指定が 0〜100 であることを確かめる。
func checkPercent(name string, v int) error {
	if v < 0 || v > 100 {
		return errors.New("mux: " + name + " must be between 0 and 100")
	}
	return nil
}
