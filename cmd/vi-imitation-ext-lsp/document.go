package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// maxHistory は、document が覚えておく過去の版の数（現在の版を含む）である。仮の値である。
// 言語サーバーは、利用者が編集を進める間に計算した診断を、古い版の番号で送ってくる。
// その位置を本体の位置へ変換するには、その版の行が要る。
const maxHistory = 16

// errNoSnapshot は、診断などが指す版の行を、もう覚えていないことを表す。
var errNoSnapshot = errors.New("snapshot of the version is not kept")

// snapshot は、ある版の文書の行である。lines は書き換えない（版ごとに別の配列を持つ）。
type snapshot struct {
	version ext.Version
	lines   lines
}

// document は、言語サーバーと同期する 1 つのバッファの、拡張機能の側の写しである。
//
// 言語サーバーへ送る文書の版の番号には、本体のバージョン番号をそのまま使う（基本設計 6.3）。
//
// 言語サーバーへ送る全文は、ファイルへ書き出したときの内容と同じにする。
// 本体は末尾の改行を含めずに全文を送り、書き出すときに改行が付くかどうかを finalNewline で知らせる（ext.BufferOpenParams、基本設計 4.1）。
// finalNewline が true なら、本体の全文の末尾に "\n" を 1 つ足して送る。
// 足さずに送ると、gofmt のように末尾に改行を置く整形は、毎回、末尾に改行を足す編集を返し、本体のバッファに空の行が 1 つ増える。
// 足した改行の後ろの空の行（仮想の行。lines の doc）を指す位置は、本体の最後の行の行末に変換する。
// finalNewline が false なら、本体の全文をそのまま送る。このとき仮想の行は無い。
// 整形が末尾に改行を足す編集を返せば、本体のバッファに空の行が 1 つ増え、書き出した内容は整形の結果と同じになる。
type document struct {
	buffer     ext.BufferID
	path       string
	uri        string
	fileType   string
	languageID string
	// finalNewline は、本体がファイルへ書き出したときに最後の行の後に改行が付くかどうか（server.Buffer の FinalNewline）である。
	finalNewline bool
	// history は、覚えている版の行で、古い順に並ぶ。最後が現在の版である。長さは 1 以上 maxHistory 以下である。
	history []snapshot
	// inlay は、インレイヒントの問い合わせの状態である（inlay.go）。
	inlay inlayState
}

// newDocument は、本体のバッファの複製 b から document を作る。
func newDocument(b *server.Buffer, languageID string) *document {
	d := &document{
		buffer:     b.ID(),
		path:       b.Path(),
		uri:        pathToURI(b.Path()),
		fileType:   b.FileType(),
		languageID: languageID,
	}
	d.reset(b)
	return d
}

// reset は、履歴を捨て、b の全文と末尾の改行の扱いを現在の版にする。
func (d *document) reset(b *server.Buffer) {
	d.finalNewline = b.FinalNewline()
	ls := make(lines, b.LineCount())
	for i := range ls {
		ls[i] = b.Line(i)
	}
	d.history = []snapshot{{version: b.Version(), lines: ls}}
}

// current は、現在の版を返す。
func (d *document) current() snapshot { return d.history[len(d.history)-1] }

// at は、版 v の行を返す。覚えていなければ errNoSnapshot を返す。
func (d *document) at(v ext.Version) (lines, error) {
	for i := len(d.history) - 1; i >= 0; i-- {
		if d.history[i].version == v {
			return d.history[i].lines, nil
		}
	}
	return nil, fmt.Errorf("version %d: %w", v, errNoSnapshot)
}

// lspText は、現在の版を、言語サーバーへ送る全文（finalNewline が true なら末尾に "\n" を足したもの）にする。
func (d *document) lspText() string {
	text := strings.Join(d.current().lines, "\n")
	if d.finalNewline {
		text += "\n"
	}
	return text
}

// apply は、buffer/change の編集の列を、逐次の数え方で現在の版に順に適用し、版 to を現在の版にする。
//
// want が true なら、各編集を、適用する直前の行で enc の位置に変換した TextDocumentContentChangeEvent の列も返す。
// 本体の編集と LSP の textDocument/didChange の contentChanges は、どちらも逐次の数え方なので（ext.Transaction、LSP 3.17 の仕様）、
// 編集を 1 つずつ、その直前の行で変換すれば、順序を変えずに送れる。
// 本体の位置はどれも本体の行の中を指すので、仮想の行は変換に関わらない。
//
// 編集の位置が行の外を指すと誤りを返し、現在の版を変えない。
// ext/server は同じ検査を通った編集だけをフックへ渡すので、誤りは起きない前提である。
//
// req: FR-30
func (d *document) apply(edits []ext.Edit, to ext.Version, enc encoding, want bool) ([]contentChange, error) {
	ls := slices.Clone(d.current().lines)
	var changes []contentChange
	for i, e := range edits {
		s, en := e.Range.Start, e.Range.End
		if s.Line < 0 || en.Line >= len(ls) || s.Line > en.Line ||
			s.Byte < 0 || s.Byte > len(ls[s.Line]) || en.Byte < 0 || en.Byte > len(ls[en.Line]) {
			return nil, fmt.Errorf("edit %d: range %+v is out of the document", i, e.Range)
		}
		if want {
			r := lspRange{Start: ls.toLSP(s, enc), End: ls.toLSP(en, enc)}
			changes = append(changes, contentChange{Range: &r, Text: e.Text})
		}
		joined := ls[s.Line][:s.Byte] + e.Text + ls[en.Line][en.Byte:]
		ls = slices.Replace(ls, s.Line, en.Line+1, strings.Split(joined, "\n")...)
	}
	d.history = append(d.history, snapshot{version: to, lines: ls})
	if n := len(d.history) - maxHistory; n > 0 {
		d.history = slices.Delete(d.history, 0, n)
	}
	return changes, nil
}

// formattingTransaction は、textDocument/formatting の結果の TextEdit の列を、版 v の行 ls をもとにしたトランザクションにする。
//
// LSP の TextEdit の配列の範囲は、どれも計算のもとにした文書の位置を指し、互いに重ならない。
// 同じ開始の位置を持つ編集は認められていて、同じ位置の挿入は配列の順に結果に並ぶ（LSP 3.17 の仕様）。
// トランザクションは逐次の数え方なので、範囲の開始の位置の降順に並べ、開始の位置が同じものは配列の添字の降順に並べる（基本設計 6.3）。
// 後ろの編集から適用すれば、前の編集の位置はずれない。
//
// finalNewline は、ls を言語サーバーへ送ったときに末尾に "\n" を足したかどうか（document の doc）である。
// false なら仮想の行は無く、ls の外を指す位置は最後の行の行末にするだけで、編集は書き換えない。
//
// 仮想の行（言語サーバーに送った全文の末尾の改行の後ろ）に触れる編集は、本体の位置で表せるように書き換える。
// 本体の全文を H とすると、言語サーバーの全文は H + "\n" で、仮想の行の先頭はその末尾である。
// 終わりが仮想の行にある編集 [s, 末尾) を T で置き換えた結果は H[:s] + T なので、これが H' + "\n" の形になる H' を作る。
//
//   - 始まりも仮想の行にある編集（末尾の改行の後ろへの挿入）は、最後の行の行末への "\n" と、T から末尾の "\n" を 1 つ除いたものの挿入にする。
//     T が空なら何もしない
//   - T が "\n" で終われば、終わりを最後の行の行末にし、T の末尾の "\n" を末尾の改行の代わりとして除く
//   - T が空で、s が 2 行目以降の行の先頭なら（H[:s] は "\n" で終わる）、始まりを前の行の行末に、終わりを最後の行の行末にした削除にする。
//     gopls（v0.24.0）は、末尾の空の行を消す整形をこの形（行の先頭から仮想の行の先頭までの削除）で返す
//   - それ以外は、整形の結果が末尾の改行を持たない場合で、本体の位置では表せない。終わりを最後の行の行末にし、末尾の改行は残す
//
// 編集が無ければ nil を返す。
//
// req: FR-30
func formattingTransaction(ls lines, finalNewline bool, v ext.Version, edits []textEdit, enc encoding) *ext.Transaction {
	if len(edits) == 0 {
		return nil
	}
	type item struct {
		index int
		start lspPosition
		edit  ext.Edit
	}
	items := make([]item, 0, len(edits))
	for i, te := range edits {
		s, sv := ls.fromLSP(te.Range.Start, enc)
		e, ev := ls.fromLSP(te.Range.End, enc)
		if e.Line < s.Line || (e.Line == s.Line && e.Byte < s.Byte) {
			e = s
		}
		text := te.NewText
		if ev && finalNewline {
			switch {
			case sv:
				if text == "" {
					continue
				}
				text = "\n" + strings.TrimSuffix(text, "\n")
			case strings.HasSuffix(text, "\n"):
				text = strings.TrimSuffix(text, "\n")
			case text == "" && s.Byte == 0 && s.Line > 0:
				s = ext.Position{Line: s.Line - 1, Byte: len(ls[s.Line-1])}
			}
		}
		items = append(items, item{index: i, start: te.Range.Start, edit: ext.Edit{Range: ext.Range{Start: s, End: e}, Text: text}})
	}
	slices.SortStableFunc(items, func(a, b item) int {
		if a.start.Line != b.start.Line {
			return b.start.Line - a.start.Line
		}
		if a.start.Character != b.start.Character {
			return b.start.Character - a.start.Character
		}
		return b.index - a.index
	})
	t := &ext.Transaction{BaseVersion: v, Edits: make([]ext.Edit, 0, len(items))}
	for _, it := range items {
		if it.edit.Range.Start == it.edit.Range.End && it.edit.Text == "" {
			continue
		}
		t.Edits = append(t.Edits, it.edit)
	}
	if len(t.Edits) == 0 {
		return nil
	}
	return t
}
