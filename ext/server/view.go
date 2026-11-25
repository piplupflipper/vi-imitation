package server

import (
	"context"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/rpc"
)

// この節は、ポップアップと表示の部品（ext の view.go。基本設計 1.4、5.3 の段階 3 の表）を扱う。

// OpenPopup は、本体へ popup/open を送り、表示の部品を中身にしたポップアップを開かせる。応答はポップアップの ID である。
// p が ext.PopupOpenParams.Validate を通らなければ、送らずにその誤りを返す。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
func (s *Server) OpenPopup(ctx context.Context, p ext.PopupOpenParams) (ext.PopupOpenResult, error) {
	if err := p.Validate(); err != nil {
		return ext.PopupOpenResult{}, err
	}
	return call[ext.PopupOpenResult](ctx, s, ext.MethodPopupOpen, p)
}

// OpenPane は、本体へ pane/open を送り、表示の部品を中身にしたペインを端末多重化ソフトで開かせる（FR-115）。応答はペインの ID である。
// 端末多重化ソフトの外では、本体はペインに代えて中央のポップアップに出す（FR-123）。どちらでも、以後は同じ ID で
// UpdateView と CloseView を送れ、利用者の操作は OnView で届く。
// p が ext.PaneOpenParams.Validate を通らなければ、送らずにその誤りを返す。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
func (s *Server) OpenPane(ctx context.Context, p ext.PaneOpenParams) (ext.PaneOpenResult, error) {
	if err := p.Validate(); err != nil {
		return ext.PaneOpenResult{}, err
	}
	return call[ext.PaneOpenResult](ctx, s, ext.MethodPaneOpen, p)
}

// UpdateView は、本体へ view/update を送り、開いたポップアップの部品の中身を置き換えさせる。応答は待たない。どこから呼んでもよい。
// p が ext.ViewUpdateParams.Validate を通らなければ、送らずにその誤りを返す。
// 切り替えの部品（ext.ViewSwitcher）の 1 つの子だけを置き換えるときは、p.Child にその子の ID を入れる（API 0.23.0 から）。
func (s *Server) UpdateView(p ext.ViewUpdateParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return s.notify(ext.MethodViewUpdate, p)
}

// CloseView は、本体へ view/close を送り、開いたポップアップを閉じさせる。応答は待たない。どこから呼んでもよい。
// 本体は、この知らせで閉じたポップアップについて view/closed を送らない。
func (s *Server) CloseView(id ext.ViewID) error {
	return s.notify(ext.MethodViewClose, ext.ViewCloseParams{View: id})
}

// ViewEventKind は、ViewEvent の種類である。
type ViewEventKind int

// ViewEvent の種類。
const (
	// ViewClosed は、view/closed である。利用者か本体がポップアップを閉じた。
	ViewClosed ViewEventKind = iota
	// ViewSelected は、view/select である。Item に項目の ID（入力の行では中身）が入る。
	ViewSelected
	// ViewInputChanged は、input/change である。Item に入力の行の中身が入る。
	ViewInputChanged
	// ViewKey は、view/key である。Key に contributes.popupKeys の項目の ID が、Item にカーソルの行の項目の ID が入る。
	ViewKey
	// ViewExpanded は、view/expand である。Item に開閉した木の項目の ID が、Expanded に開いたかどうかが入る。
	ViewExpanded
	// ViewTabChanged は、view/tabChange である（API 0.10.0 で足した）。分割したペインを開いた元の client のカーソルのあるタブが替わった。
	// Buffer にそのタブのバッファの番号が、Item にそのファイルの絶対パス（ファイルでなければ空）が入る（ext.ViewTabChangeParams）。
	ViewTabChanged
	// ViewChildSelected は、view/childSelect である（API 0.23.0 で足した）。利用者が切り替えの部品の子を切り替えた。
	// Child に選んだ子の ID が入る（ext.ViewChildSelectParams）。
	ViewChildSelected
)

// ViewEvent は、本体からポップアップの利用者の操作の知らせ（view/closed、view/select、input/change、view/key、view/expand、
// view/tabChange）を受け取ったことを表す。
type ViewEvent struct {
	Kind ViewEventKind
	// View は、ポップアップの ID である。
	View ext.ViewID
	// Item は、選んだ項目の ID か、入力の行の中身である。ViewClosed では空である。
	Item string
	// Key は、ViewKey のときの contributes.popupKeys の項目の ID である。
	Key string
	// Expanded は、ViewExpanded のときの、項目を開いたかどうかである。
	Expanded bool
	// Seq は、ViewInputChanged のときの input/change の番号である（ext.InputChangeParams.Seq）。
	// ファインダー（ext.ViewFinder）の一覧を返す view/update の Seq に入れる。
	Seq uint64
	// Input は、ファインダーの ViewSelected と ViewKey のときの、入力の行の中身である。
	Input string
	// Selection は、ViewSelected のときの選択の番号である（ext.ViewSelectParams.Selection）。Execute に渡す。
	Selection uint64
	// Buffer は、ViewTabChanged のときの、替わった後のタブのバッファの番号である。
	Buffer ext.BufferID
	// Child は、切り替えの部品（ext.ViewSwitcher）の中の知らせ（ViewSelected、ViewInputChanged、ViewKey、ViewExpanded）なら、
	// その子の ID である。ViewChildSelected では選んだ子の ID である。ほかでは空である（API 0.23.0 で足した）。
	Child string
}

// ListEditor は、本体へ editor/list を送り、本体が持つ一覧（タブ、コマンド、キーの一覧、レジスターの履歴、ジャンプリスト）の 1 つを
// 問い合わせる（FR-107）。応答は、問い合わせた list の欄だけを埋めたものである。
// list が知らない種類なら、送らずにその誤りを返す。Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
func (s *Server) ListEditor(ctx context.Context, list ext.EditorListKind) (ext.EditorListResult, error) {
	p := ext.EditorListParams{List: list}
	if err := p.Validate(); err != nil {
		return ext.EditorListResult{}, err
	}
	return call[ext.EditorListResult](ctx, s, ext.MethodEditorList, p)
}

// Execute は、本体へ editor/execute を送り、キーの一覧のキーかコマンドを、利用者が打ったのと同じく実行させる。
// 本体は実行の前に応答する。p が ext.EditorExecuteParams.Validate を通らなければ、送らずにその誤りを返す。
// Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
func (s *Server) Execute(ctx context.Context, p ext.EditorExecuteParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	_, err := call[ext.EditorExecuteResult](ctx, s, ext.MethodEditorExecute, p)
	return err
}

// OnView は、ポップアップの利用者の操作の知らせを受け取ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
// 他のフックと同じくループの上で呼ばれるので、f の中で本体の応答を待つ関数（OpenPopup など）をこの ctx で呼ぶと ErrEditInHandler になる。
// 応答を待たない UpdateView と CloseView は呼んでよい。
func (s *Server) OnView(f func(ctx context.Context, e *ViewEvent)) {
	s.mustNotStarted()
	s.onView = f
}

// viewNotification は、ポップアップの知らせ n を ViewEvent にして OnView のフックへ渡す。知らせでなければ何もしない。
func (s *Server) viewNotification(ctx context.Context, n *rpc.Notification) {
	var e ViewEvent
	var err error
	switch n.Method {
	case ext.MethodViewClosed:
		var p ext.ViewClosedParams
		err = decodeParams(n, &p)
		e = ViewEvent{Kind: ViewClosed, View: p.View}
	case ext.MethodViewSelect:
		var p ext.ViewSelectParams
		err = decodeParams(n, &p)
		e = ViewEvent{Kind: ViewSelected, View: p.View, Item: p.Item, Input: p.Input, Selection: p.Selection, Child: p.Child}
	case ext.MethodInputChange:
		var p ext.InputChangeParams
		err = decodeParams(n, &p)
		e = ViewEvent{Kind: ViewInputChanged, View: p.View, Item: p.Text, Seq: p.Seq, Child: p.Child}
	case ext.MethodViewKey:
		var p ext.ViewKeyParams
		err = decodeParams(n, &p)
		e = ViewEvent{Kind: ViewKey, View: p.View, Item: p.Item, Key: p.ID, Input: p.Input, Child: p.Child}
	case ext.MethodViewExpand:
		var p ext.ViewExpandParams
		err = decodeParams(n, &p)
		e = ViewEvent{Kind: ViewExpanded, View: p.View, Item: p.Item, Expanded: p.Expanded, Child: p.Child}
	case ext.MethodViewChildSelect:
		var p ext.ViewChildSelectParams
		err = decodeParams(n, &p)
		e = ViewEvent{Kind: ViewChildSelected, View: p.View, Child: p.Child}
	case ext.MethodViewTabChange:
		var p ext.ViewTabChangeParams
		err = decodeParams(n, &p)
		e = ViewEvent{Kind: ViewTabChanged, View: p.View, Item: p.Path, Buffer: p.Buffer}
	default:
		return
	}
	if err != nil {
		s.logf("%v", err)
		return
	}
	if e.Kind == ViewClosed {
		// 拡張機能のフックより先に、閉じた部品に結びついた写しを外す（TextLog。FR-273）。
		s.viewClosed(e.View)
	}
	if s.onView != nil {
		s.hook("view hook", func() { s.onView(ctx, &e) })
	}
}
