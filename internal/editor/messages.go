package editor

import (
	"fmt"
	"strings"

	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// メッセージの履歴と、拡張機能のメッセージで本体の誤りを消さない規則（#211。基本設計 8 章）。
//
// 本体の誤り（showError。設定の誤り、操作の失敗、拡張機能の異常終了）と拡張機能の message/show は、履歴に残し、:messages で見られる。
// 拡張機能の message/show は、メッセージの行に本体の誤りが出ている間は、それを上書きしない（showExtMessage）。
// 本体の誤りは、利用者が次のキーを打つと消える（handleKey）ので、上書きしないのはそれまでである。
// 起動したときの設定の誤りが、Go のファイルを開いた直後から届く言語サーバーの知らせで見えなくなる（#211）のと、
// 再生を止めた誤りが、待つ間に届いた拡張機能の知らせで見えなくなる（#224）のを避けるためである。

// maxMessageHistory は、メッセージの履歴に残す件数の上限である（仮）。超えたら古いものから捨てる。
const maxMessageHistory = 200

// messagesExtension は、コマンドラインの表（exCommands）で :messages に入れる拡張機能の ID の代わりの値である（extManagerExtension と同じ形）。
const messagesExtension = " messages"

// messagesCommandNames は、:messages として受けるコマンドの名前である（Vim と同じく :mes まで縮められる）。
var messagesCommandNames = []string{"mes", "mess", "messa", "messag", "message", "messages"}

// addMessagesCommand は、コマンドラインの表に :messages を足す。拡張機能が同じ ID のコマンドを宣言していれば、本体の :messages を優先し、誤りを返す。
func addMessagesCommand(cmds map[string]string) []error {
	var errs []error
	for _, name := range messagesCommandNames {
		if prev, ok := cmds[name]; ok {
			errs = append(errs, fmt.Errorf("拡張機能 %s のコマンド %s は本体の :messages と重なるので、コマンドラインからは呼べない", prev, name))
		}
		cmds[name] = messagesExtension
	}
	return errs
}

// noteMessage は、m をメッセージの履歴に足す。本文が空なら足さない。
func (e *Editor) noteMessage(m ui.Message) {
	if m.Text == "" {
		return
	}
	e.msgHistory = append(e.msgHistory, m)
	if n := len(e.msgHistory) - maxMessageHistory; n > 0 {
		e.msgHistory = append(e.msgHistory[:0:0], e.msgHistory[n:]...)
	}
}

// showExtMessage は、拡張機能の message/show の m を履歴に残し、メッセージの行に出す。
// メッセージの行に本体の誤り（拡張機能のメッセージでない誤り）が出ていれば、上書きしない（このファイルの冒頭の規則）。
// hit-enter の問いで待っている本体の複数行のメッセージ（:ls、:messages など）も、上書きしない（keepsHitEnter）。
func (e *Editor) showExtMessage(m ui.Message) {
	e.noteMessage(m)
	if e.message.Level == ui.MessageError && e.message != e.extMessage {
		return
	}
	if e.keepsHitEnter() {
		return
	}
	e.message = m
	e.extMessage = m
}

// showLaterMessage は、利用者のキーによらずに後から起きたこと（別の client の操作、拡張機能の diff/start で分割したペインの client が
// 繋いだこと）の本体の知らせ m を、読み込んでいる client のメッセージの行に出す。
// その client が hit-enter の問いで本体の複数行のメッセージを出して待っていれば、上書きせずに履歴に残すだけにする（keepsHitEnter）。
func (e *Editor) showLaterMessage(m ui.Message) {
	if e.keepsHitEnter() {
		e.noteMessage(m)
		return
	}
	e.message = m
}

// keepsHitEnter は、読み込んでいる client が、hit-enter の問いで本体の複数行のメッセージ（拡張機能のメッセージでないもの）を出して
// 待っているかを返す。そのメッセージは、利用者が次のキーを打つまで、後から届いた知らせで上書きしない。
// 利用者が打った :ls の一覧が、続けて届いた知らせ（diff/start の「見比べます」）で、読む前に消えたため
// （#381 の続き。main の CI の TestChangesLastClientInTmux が揺れた。Vim の hit-enter の問いも、キーを打つまで一覧を残す）。
func (e *Editor) keepsHitEnter() bool {
	return e.hitEnter() && e.message != e.extMessage
}

// showMessages は、:messages で、メッセージの履歴を古い順に 1 件 1 行で出す（Vim の :messages）。
// 2 件以上なら複数の行なので hit-enter の問いになる（render の hitEnter）。履歴が空なら何も出さない。
// 1 件の中の改行は空白にし、1 件を 1 行に収める。
// メッセージの行は画面の半分までしか広がらない（ui の drawMessageLines）ので、収まらなければ新しい側を残し、
// 古い側を「（前の N 件は略）」の 1 行にする。誤りは新しい側に溜まるので、見せたいものほど新しい（#229 のレビューの推奨 3）。
func (e *Editor) showMessages() {
	lines := make([]string, len(e.msgHistory))
	for i, m := range e.msgHistory {
		lines[i] = strings.ReplaceAll(m.Text, "\n", " ")
	}
	if limit := max(e.renderer.Height/2, 2); len(lines) > limit {
		skip := len(lines) - (limit - 1)
		lines = append([]string{fmt.Sprintf("（前の %d 件は略）", skip)}, lines[skip:]...)
	}
	e.message = infoMessage(strings.Join(lines, "\n"))
}
