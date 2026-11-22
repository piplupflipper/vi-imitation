package ext

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MethodWindowChange は、本体から拡張機能への通知である。params は WindowChangeParams。
// 窓の見えている行の範囲、カーソルの位置、モード、表示領域の大きさが変わったことを知らせる
// （基本設計 5.3、6.7 の「窓の変化の知らせ」。FR-235、FR-236。Neovim の WinScrolled と CursorMoved に当たる）。
//
// 本体が送るのは、宣言ファイルの windowEvents が true で、稼働中の拡張機能だけである。
// 起動の契機にはならない（未起動の拡張機能は起動しない）。稼働中になった拡張機能には、その時点のすべての窓の状態を送る。
//
// キーを打つたびには送らない。本体は、画面を描いた後に、窓ごとに前に送った中身と比べ、違う窓だけを送る。
// 前に送ってから WindowNotifyIntervalMs のミリ秒が経っていなければ、その間は送らずに、時間が来た時点の最後の中身を 1 回だけ送る
// （nvim-treesitter-context の throttle_by_id と同じく、続けて変わっても、その時間のうちに 2 回までにする）。
// このため、拡張機能が受け取る中身は、途中の中身を飛ばすことがある。
// ext/server は、さらに、フックが前の知らせを処理している間に同じ窓の知らせが続けて届いたら、最後の 1 件だけをフックへ渡す
// （拡張機能の処理が間隔より遅くても、知らせが溜まって ping の応答が遅れないようにする）。
//
// 窓が無くなった（client が終わった）ときは、Closed を true にした知らせを 1 回送る。その WindowID は使い回さない。
//
// 古い本体（0.16.0 まで）は送らない。
const MethodWindowChange = "window/change"

// WindowNotifyIntervalMs は、本体が window/change を同じ拡張機能へ続けて送るときの、送る間隔の下限のミリ秒である（仮）。
// nvim-treesitter-context（オーナーの nvim に入っているコミット 8decd73460d26ef0a0befb745d022dd1fc2c38b8）の
// lua/treesitter-context.lua の throttle_by_id の 150 ミリ秒に合わせた。
const WindowNotifyIntervalMs = 150

// WindowID は、本体が窓ごとに決める識別子である。拡張機能は値の形に頼らず、window/change で受け取った値をそのまま band/set に書く。
//
// 今の本体の窓は、編集の client の画面のバッファの表示領域（client ごとに 1 つ）で、識別子は "client/<client の番号>" である。
// 端末多重化ソフトで分割したペインの client と、見比べ（diff/start）のために分割したペインの client は別の client なので、窓も別である。
// ポップアップの中のバッファ（ホバー、ポップアップで見る、ファインダー）と部品の client の画面は、今は窓として知らせない。
// 後で知らせるときは、"client/" で始まらない識別子にする。
type WindowID string

// WindowChangeParams は、window/change の params である。
type WindowChangeParams struct {
	// Window は、窓の識別子である。
	Window WindowID `json:"window"`
	// Closed は、窓が無くなった（client が終わった）ことを表す。true なら、ほかの項目は読まない。
	Closed bool `json:"closed,omitempty"`
	// Buffer は、窓に出しているバッファ（その client のカーソルのあるタブ。フォーカスのあるポップアップを開いていれば、
	// 開いたときのタブ）の番号である。拡張機能へ送っていないバッファ（不正な UTF-8 を含むもの）のこともある。
	Buffer BufferID `json:"buffer,omitempty"`
	// Version は、送った時点のバッファのバージョン番号である。band/set の Version にそのまま書くと、
	// 本体は、帯を押したときに、帯を作ったときから本文が変わっていないかを確かめられる（BandSetParams.Version）。
	Version Version `json:"version,omitempty"`
	// Top は、表示領域の最初の行（0 から数える。Vim の line('w0') - 1）である。
	// 行を折り返して描くとき、最初の行を途中から見せていることがある。
	Top int `json:"top"`
	// Bottom は、直前の描画で表示領域に最後まで描いた行の次の行である（Vim の w_botline。0 から数える）。
	// 見えている行は Top から Bottom の前の行までで、最後に途中まで描いた行があればそれも見えている。
	Bottom int `json:"bottom"`
	// Rows と Columns は、表示領域の行数と、本文の列数（印の列と行番号の列を除く）である。
	Rows    int `json:"rows"`
	Columns int `json:"columns"`
	// Left は、行を折り返さない窓の、表示領域の左の端に当たる表示の列（横のスクロールの位置。0 から数える。Vim の leftcol）である。
	// 行を折り返す窓では 0 である。窓の列で位置を決める拡張機能（ブロックの罫線など）が、行の表示の列から引いて窓の列にする。
	// 横のスクロール（ホイールの横の送り、カーソルが左右の端から出たときの送り）だけで変わったときも知らせる。
	Left int `json:"left"`
	// Cursor は、窓のバッファのカーソル位置である。
	Cursor Position `json:"cursor"`
	// Mode は、窓の client のモードである（ポップアップにフォーカスがあれば、ポップアップのバッファのモード）。
	Mode Mode `json:"mode,omitempty"`
	// Diff は、窓のバッファが見比べ（diff/start、:diffsplit）の片側であることを表す。
	Diff bool `json:"diff,omitempty"`
	// Seq は、知らせの通し番号である。本体が window/change を送るたびに 1 から増やす（窓と拡張機能をまたいで 1 つの数え方）。
	// 拡張機能は、この知らせを元に作った帯の BandSetParams.Seq に書く。
	Seq uint64 `json:"seq"`
}

// MethodBandSet は、拡張機能から本体への通知である。params は BandSetParams。
// 窓の表示領域の上の端に、何行かを重ねて描く帯を置く（基本設計 6.7 の「窓の上端の帯」。FR-237〜FR-239。
// nvim-treesitter-context の窓に当たる）。
//
// 本体は、拡張機能と窓の組ごとに最後に届いた帯を持ち、窓のバッファが Buffer のときだけ描く。
// 行の無い帯（Lines が空）は、その組の帯を消す。拡張機能が異常で終わるか止まったら、その拡張機能の帯をすべて消す。
// 1 つの窓に複数の拡張機能の帯があれば、拡張機能の ID の順に上から並べる。
//
// 帯はカーソルの行を隠さない。本体は、帯の行数がカーソルの表示領域の中の行（上から数えた行数）を超えるとき、
// 帯の上の行から削って、カーソルの行より上に収める（nvim-treesitter-context の calc_max_lines と trim_scope の既定の outer と同じ）。
// 帯の下に隠れた本文の行のために、スクロールの余白は足さない（nvim-treesitter-context と同じ）。
//
// 古い本体（0.16.0 まで）は、知らない通知として捨てる（ログに残すだけ）。
const MethodBandSet = "band/set"

// MaxBandLines は、band/set の 1 つの帯に書ける行の数の上限である（仮）。超えた帯は、本体がログに残して捨てる。
const MaxBandLines = 64

// BandSetParams は、band/set の params である。
type BandSetParams struct {
	// Window は、帯を描く窓（window/change の Window）である。
	Window WindowID `json:"window"`
	// Buffer は、帯を作ったときの窓のバッファである。窓のバッファが替わったら、本体は描かない。
	Buffer BufferID `json:"buffer"`
	// Version は、帯を作ったときのバッファのバージョン番号である。本体は、今のバージョン番号がこれと違う間も帯を描くが、
	// 帯を押してもその行へ移らない（下の本文を押したことにする。nvim-treesitter-context の get_source_line と同じ）。
	Version Version `json:"version"`
	// Seq は、帯を作る元にした window/change の Seq である。本体は、同じ拡張機能と窓の組の今の帯の Seq より小さい帯を、
	// 古い知らせから作ったものとして捨てる（別の goroutine で作った帯が、作り終える順で入れ替わって届いても、新しい帯を上書きしない）。
	// 0（省く）なら比べずに置き換える。
	Seq uint64 `json:"seq,omitempty"`
	// Lines は、帯の行を上から並べたものである。空なら帯を消す。MaxBandLines を超えられない。
	Lines []BandLine `json:"lines"`
}

// BandLine は、帯の 1 行である。
type BandLine struct {
	// Text は、行の中身の文字列である。改行を含められない。本体は折り返さず、窓の横のスクロールの位置に合わせて、
	// 本文と同じ列から描く（行を折り返して描く窓では、左の端から描く）。タブ文字は窓のバッファのタブ幅（tab.width）で広げる。
	Text string `json:"text"`
	// Line は、行に当たるバッファの行（0 から数える）である。本体は、行番号の列にこの行の番号を描き、
	// 帯のこの行を押したらこの行へ移る（FR-238）。省くと（nil）、行番号を描かず、押しても下の本文を押したことにする。
	Line *int `json:"line,omitempty"`
	// Highlights は、行の中の範囲ごとの色である。後の範囲ほど上に重ねる。
	Highlights []BandHighlight `json:"highlights,omitempty"`
}

// BandHighlight は、帯の行の中の、色を付ける範囲である。
type BandHighlight struct {
	// Start と End は、Text の中のバイトの範囲 [Start, End) である。
	Start int `json:"start"`
	End   int `json:"end"`
	// Style は、色の名前（本体の色の名前のどれか。decoration/set の style と同じ。基本設計 7.1）である。
	// 本体は、帯の色（band）の上に、この名前の色を重ねる。本体の知らない名前なら、色を重ねない。
	Style string `json:"style"`
}

// SourceLine は、BandLine.Line に入れる値を作る。
func SourceLine(n int) *int {
	return &n
}

// Validate は、band/set の params を本体が受け付けるかを確かめる。
// 窓の識別子が空のもの、行が多すぎるもの、改行か不正な UTF-8 を含む行、負の行の番号、行の外を指す範囲を断る。
// 本体は、断ったものをログに残して捨てる。
func (p BandSetParams) Validate() error {
	if p.Window == "" {
		return errors.New("band/set: empty window")
	}
	if len(p.Lines) > MaxBandLines {
		return fmt.Errorf("band/set: %d lines exceed the limit %d", len(p.Lines), MaxBandLines)
	}
	for i, l := range p.Lines {
		if strings.ContainsAny(l.Text, "\r\n") {
			return fmt.Errorf("band/set: line %d contains a newline", i)
		}
		if !utf8.ValidString(l.Text) {
			return fmt.Errorf("band/set: line %d is not valid UTF-8", i)
		}
		if l.Line != nil && *l.Line < 0 {
			return fmt.Errorf("band/set: line %d has a negative source line %d", i, *l.Line)
		}
		for j, h := range l.Highlights {
			if h.Start < 0 || h.Start > h.End || h.End > len(l.Text) {
				return fmt.Errorf("band/set: line %d highlight %d [%d, %d) is out of the text [0, %d)", i, j, h.Start, h.End, len(l.Text))
			}
		}
	}
	return nil
}
