package ui

import (
	"testing"

	"github.com/piplupflipper/vi-imitation/internal/buffer"
)

// 1 行の入力の問い（Frame.Input。FR-275）は、最後の行に問いの文と中身を続けて描き、カーソル（縦線）を中身の末尾に置く。
// 幅に収まらなければ、全角の文字の境目で先頭の側を隠して末尾を見せる。知らせは入力の行の上から上へ、何件目かは問いの文の前に出す。
// コマンドラインモードとメッセージより先に出す。CommandLinePopup なら画面の上の枠に描き、題を commandLine.title.input にし、
// 枠を置けないほど低ければ最後の行に描く。
//
// req: FR-275
func TestGoldenInput(t *testing.T) {
	b := buffer.New("x\n")
	r := &Renderer{Width: 40, Height: 8, Theme: testTheme()}
	golden(t, "input", renderSteps(r, []step{
		{"問いの文と初めの中身", Frame{Text: b, Input: &Input{Prompt: "式: ", Text: "a+b"}}},
		{"空の中身", Frame{Text: b, Input: &Input{Prompt: "名前: "}}},
		{"幅に収まらない全角の中身", Frame{Text: b, Input: &Input{Prompt: "値: ", Text: "あいうえおかきくけこさしすせそたちつてとなにぬねの"}}},
		{"続けて届いた問いと知らせ", Frame{Text: b, Input: &Input{Prompt: "式: ", Text: "x", Index: 2, Note: "問い 1 に「B」と答えました\nこの後に 1 件の問いが待っています"}}},
		{"コマンドラインモードとメッセージより先に出す", Frame{Text: b, Mode: ModeCommandLine, CommandLine: "w", Message: Message{Text: "m"}, Input: &Input{Prompt: "q: ", Text: "t"}}},
		{"ポップアップの枠", Frame{Text: b, CommandLinePopup: true, Input: &Input{Prompt: "式: ", Text: "全角", Note: "知らせ"}}},
	}))
	low := &Renderer{Width: 40, Height: 3, Theme: testTheme()}
	golden(t, "input-low", renderSteps(low, []step{
		{"枠を置けなければ最後の行", Frame{Text: b, CommandLinePopup: true, Input: &Input{Prompt: "式: ", Text: "y"}}},
	}))
}
