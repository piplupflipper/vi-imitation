package ext

import "errors"

// ファイルでないバッファの作成と、拡張機能から始める見比べ（基本設計 5.3 の段階 3 の表、6.8、6.9。FR-88、FR-99、FR-104）。
//
// git 連携の拡張機能は、バッファのファイルの HEAD の版を buffer/create でファイルでないバッファにし、
// diff/start でそのファイルのバッファと見比べさせる。
//
// メソッドと向き（名前は仮）:
//
//   - buffer/create（拡張機能 → 本体、リクエスト）: BufferCreateParams のテキストで、ファイルでないバッファを作る。
//     応答は BufferCreateResult。作ったバッファはどのタブにもしない。本体は応答より先に、稼働中の拡張機能へ buffer/open を送る
//   - buffer/update（拡張機能 → 本体、リクエスト）: buffer/create で作ったバッファの中身を BufferUpdateParams のテキストに置き換える。
//     編集を禁止したバッファでも置き換える。応答は BufferUpdateResult
//   - buffer/dispose（拡張機能 → 本体、リクエスト）: buffer/create で作ったバッファを閉じる（BufferDisposeParams）。タブならタブも閉じる
//   - diff/start（拡張機能 → 本体、リクエスト）: DiffStartParams の 2 つのバッファの見比べを始める。応答は DiffStartResult
//
// buffer/update と buffer/dispose を送れるのは、そのバッファを buffer/create で作った拡張機能だけである。
// 他の拡張機能のバッファと、利用者が開いたバッファには、本体は JSON-RPC の誤りで応答する。
//
// 本体は隠れたバッファを持たない（FR-64）。拡張機能は、作ったバッファを、タブにする（editor/open の buffer と focus）か、
// diff/start に渡すか、buffer/dispose で閉じる。diff/start で見比べが始まらなかったとき（誤りで応答したとき、
// 分割したペインの client が繋がなかったとき）は、本体が、まだタブでない作ったバッファを閉じる。
const (
	// MethodBufferCreate は、拡張機能から本体へのリクエストである。params は BufferCreateParams、result は BufferCreateResult。
	MethodBufferCreate = "buffer/create"
	// MethodBufferUpdate は、拡張機能から本体へのリクエストである。params は BufferUpdateParams、result は BufferUpdateResult。
	MethodBufferUpdate = "buffer/update"
	// MethodBufferDispose は、拡張機能から本体へのリクエストである。params は BufferDisposeParams、result は BufferDisposeResult。
	MethodBufferDispose = "buffer/dispose"
	// MethodDiffStart は、拡張機能から本体へのリクエストである。params は DiffStartParams、result は DiffStartResult。
	MethodDiffStart = "diff/start"
)

// BufferUpdateParams は、buffer/update の params である。
type BufferUpdateParams struct {
	// Buffer は、buffer/create で作ったバッファの番号である。
	Buffer BufferID `json:"buffer"`
	// Text は、新しい中身である。末尾の改行を含めない。
	Text string `json:"text"`
}

// BufferUpdateResult は、buffer/update の result である。
type BufferUpdateResult struct {
	// Version は、置き換えた後のバージョン番号である。中身が同じなら変わらない。
	Version Version `json:"version"`
}

// BufferDisposeParams は、buffer/dispose の params である。
type BufferDisposeParams struct {
	// Buffer は、buffer/create で作ったバッファの番号である。
	Buffer BufferID `json:"buffer"`
}

// BufferDisposeResult は、buffer/dispose の result である。項目は無い。
type BufferDisposeResult struct{}

// BufferCreateParams は、buffer/create の params である。
//
// 作るバッファは、ファイルを持たない（保存の先が無い）。
// FR-88 のとおり、既定では編集を禁止した状態で始める。Writable を true にしたときだけ編集できる状態で始める。
type BufferCreateParams struct {
	// Title は、バッファの名前である。タブの行、ステータス行、:ls にパスの代わりに出す。空にはできない。
	Title string `json:"title"`
	// Text は、バッファの中身である。位置を数えるもとのテキスト（基本設計 4.1）で、末尾の改行を含めない。
	Text string `json:"text"`
	// FileType は、ファイルの種類である（構文の色付けなどに使う）。省けば種類の無いバッファにする。
	FileType string `json:"fileType,omitempty"`
	// Writable は、true なら編集できる状態で始めることを表す。省けば編集を禁止した状態で始める（FR-88）。
	Writable bool `json:"writable,omitempty"`
}

// Validate は、Title が空でないことを検査する。本体は、誤りのある params のリクエストに JSON-RPC のエラーで応答する。
func (p *BufferCreateParams) Validate() error {
	if p.Title == "" {
		return errors.New("title が空")
	}
	return nil
}

// BufferCreateResult は、buffer/create の result である。
type BufferCreateResult struct {
	// Buffer は、作ったバッファの番号である。
	Buffer BufferID `json:"buffer"`
	// Version は、作った時点のバージョン番号である。
	Version Version `json:"version"`
}

// DiffStartParams は、diff/start の params である。
//
// 本体は、A と B の 2 つのバッファを、A を左（diff の A の側）にして見比べる（基本設計 6.8）。
// 2 つのバッファは別々の編集の client のタブでなければならない。
//
//   - どちらも別々の client のタブなら、その場で見比べを始める
//   - どちらも同じ client のタブなら、誤りにする（同じ client の 2 つのタブは同時に画面に出ない）
//   - 片方だけがタブなら、そのタブの client のペインを端末多重化ソフトの層で分割し、新しい編集の client を起動して、
//     もう片方のバッファをそのタブにしてから見比べを始める（:diffsplit と同じ）。応答は分割を頼んだ時点で返す。
//     新しいペインは、タブでないバッファが A なら元のペインの左に、B なら右に開き、ペインの左右を見比べの左右と揃える
//     （API 0.26.0 から。#437。0.25.0 までは、どちらでも右に開き、フォーカスを新しいペインへ移した）。
//     フォーカスは右のペイン（B の側）に置く。右に開いたら新しいペインへ移し、左に開いたら元のペインに残す。
//     比べる元の版を A（buffer/create で作ったタブでないバッファ）、作業ツリーのファイルを B（タブ）にすると、版が左、ファイルが右に出る
//     （git 連携の拡張機能の並び。FR-104、FR-251）。herdr 0.9.0 は左への分割を持たないので、右に開いてから元のペインと入れ替える
//   - どちらもタブでなければ、誤りにする
//
// 端末多重化ソフトの外（FR-60）では、分割できないので誤りにする。
// 拡張機能は、editor/state の CanSplit で、分割できるかを buffer/create の前に確かめられる。
// 分割したペインの client を待っている間に届いた、分割の要る diff/start も誤りにする。
type DiffStartParams struct {
	// A は、見比べの左の側のバッファの番号である。
	A BufferID `json:"a"`
	// B は、見比べの右の側のバッファの番号である。
	B BufferID `json:"b"`
}

// Validate は、A と B が違うバッファであることを検査する。
func (p *DiffStartParams) Validate() error {
	if p.A == p.B {
		return errors.New("a と b が同じバッファ")
	}
	return nil
}

// DiffStartResult は、diff/start の result である。
type DiffStartResult struct {
	// Split は、ペインを分割して新しい編集の client を起動したことを表す。
	// true のとき、見比べは新しい client が繋いだ後に始まる（繋がなければ始まらない）。false なら応答の時点で始まっている。
	Split bool `json:"split,omitempty"`
}
