package ui

import (
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// endInput は、カーソルを中身の末尾に置いた入力の問いを返す。
func endInput(prompt, text string) *Input {
	return &Input{Prompt: prompt, Text: text, Cursor: len(text)}
}

// 1 行の入力の問い（Frame.Input。FR-275）は、最後の行に問いの文と中身を続けて描き、カーソル（縦線）を Input.Cursor の位置に置く。
// 幅に収まらなければ、問いの文を残して、全角の文字の境目で中身の側を横に送る。問いの文が欄の半分を超えれば、問いの文の末尾を切る。
// 知らせは入力の行の上から上へ、何件目かは問いの文の前に出す。コマンドラインモードとメッセージより先に出す。
// CommandLinePopup なら画面の上の枠に描き、題を commandLine.title.input にし、枠を置けないほど低ければ最後の行に描く。
//
// req: FR-275
func TestGoldenInput(t *testing.T) {
	b := buffer.New("x\n")
	r := &Renderer{Width: 40, Height: 8, Theme: testTheme()}
	long := "あいうえおかきくけこさしすせそたちつてとなにぬねの"
	golden(t, "input", renderSteps(r, []step{
		{"問いの文と初めの中身", Frame{Text: b, Input: endInput("式: ", "a+b")}},
		{"空の中身", Frame{Text: b, Input: endInput("名前: ", "")}},
		{"幅に収まらない全角の中身は問いの文を残す", Frame{Text: b, Input: endInput("値: ", long)}},
		{"中身の途中のカーソル", Frame{Text: b, Input: &Input{Prompt: "式: ", Text: "--port 5678 -v", Cursor: len("--port ")}}},
		{"先頭のカーソルと長い中身", Frame{Text: b, Input: &Input{Prompt: "値: ", Text: long}}},
		{"欄の半分を超える問いの文", Frame{Text: b, Input: endInput(strings.Repeat("長い問い", 6)+": ", "abc")}},
		{"続けて届いた問いと知らせ", Frame{Text: b, Input: &Input{Prompt: "式: ", Text: "x", Cursor: 1, Index: 2, Note: "問い 1 に「B」と答えました\nこの後に 1 件の問いが待っています"}}},
		{"コマンドラインモードとメッセージより先に出す", Frame{Text: b, Mode: ModeCommandLine, CommandLine: "w", Message: Message{Text: "m"}, Input: endInput("q: ", "t")}},
		{"ポップアップの枠", Frame{Text: b, CommandLinePopup: true, Input: &Input{Prompt: "式: ", Text: "全角", Cursor: len("全"), Note: "知らせ"}}},
		{"ポップアップの枠の長い中身", Frame{Text: b, CommandLinePopup: true, Input: endInput("問い: ", long)}},
	}))
	low := &Renderer{Width: 40, Height: 3, Theme: testTheme()}
	golden(t, "input-low", renderSteps(low, []step{
		{"枠を置けなければ最後の行", Frame{Text: b, CommandLinePopup: true, Input: endInput("式: ", "y")}},
	}))
}
