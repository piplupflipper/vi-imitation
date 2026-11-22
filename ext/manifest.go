package ext

import (
	"errors"
	"fmt"
	"strings"
)

// Manifest は、拡張機能の宣言ファイルの内容である（基本設計 5.1）。
// 本体は、拡張機能を起動せずに宣言ファイルだけを読み、追加する操作と起動の契機を知る。
// 項目の名前は仮である。
//
// 宣言ファイルは JSONC で書く。
// JSONC は internal/config が github.com/tailscale/hujson で標準の JSON に直してから読む（#9 の裁定）。
// このパッケージは、読み込んだ後の値の型と検査（Validate）だけを持つ。
//
// req: FR-13
type Manifest struct {
	// ID は、拡張機能の ID である。ドットリピートの記録と設定で使う。
	ID string `json:"id"`
	// Command は、拡張機能の実行ファイルのパスである。
	// 相対パスは、宣言ファイルのあるディレクトリから解決する。
	Command string `json:"command"`
	// Args は、Command に渡す引数の列である（基本設計 6.7 の宣言ファイルに足す項目）。
	// スクリプトの拡張機能が、共通の実行ファイルにスクリプトのパスを渡すのに使う。無ければ省く。
	// 本体は解決せずにそのまま渡す。相対パスの引数は、拡張機能が EnvExtensionDir のディレクトリから解決する（基本設計 5.1）。
	Args []string `json:"args,omitempty"`
	// SaveHook は、true なら、稼働中のときに保存の直前の buffer/willSave を受け取る（基本設計 6.7 の保存の流れ）。
	// 保存の前に整形する LSP の拡張機能が使う。
	SaveHook bool `json:"saveHook,omitempty"`
	// NoBufferSync は、true なら、本体がこの拡張機能へバッファの同期（buffer/open、buffer/change、buffer/close、buffer/didSave）を
	// 送らないことを表す（API 0.16.0 で足した。基本設計 5.3。#367）。
	// バッファのテキストを使わずに常に動く拡張機能（メモリの使用量など）が、開いたすべてのバッファの複製を持たないためのものである。
	// 本体は、この拡張機能への command/execute と provider/invoke の params の Withheld を立てるので、ext/server はハンドラーへ
	// バッファの代わりに nil を渡す。バッファの複製を要る操作（operators、motions、textObjects）と SaveHook とは併せて書けない（Validate）。
	// 古い本体（0.15.0 まで）は未知の項目として無視し、バッファを送る。
	NoBufferSync bool `json:"noBufferSync,omitempty"`
	// Activation は、起動の契機の一覧である。どれか 1 つが成り立った時点で起動する。
	Activation []Activation `json:"activation"`
	// Contributes は、拡張機能が追加する操作である。
	Contributes Contributes `json:"contributes"`
	// Provides は、拡張機能が引き受ける本体の操作（提供者として名乗る操作）の列である（基本設計 5.1、5.5。FR-94）。
	// 本体は、操作が呼ばれたら、カーソルのあるバッファのファイルの種類で提供者を選び、provider/invoke で問い合わせる。
	//
	// 選び方は次のとおりである（仮。基本設計 5.5）。
	//
	//   - その操作を名乗った拡張機能のうち、ファイルの種類の契機（ActivationFileType。設定で足したものを含む）と
	//     設定のファイルの種類の契機（ActivationSettingsFileType）のどちらも持たないか、
	//     ファイルの種類の契機の Pattern のどれかがバッファのファイルの種類と一致するものを候補にする
	//   - 候補のうち、未起動か起動中か稼働中のものを、本体が宣言ファイルを読んだ順に探し、最初のものを使う。
	//     同じ操作を複数の拡張機能が名乗ったときの選び方は未決（基本設計 10 章）で、それまでは先に読んだものを使う
	//   - 未起動なら、問い合わせで起動する。provides だけを持つ拡張機能は、Activation を空にしてよい
	//
	// この版の API の知らない操作の名前は、Validate が警告にし、本体は問い合わせない。
	Provides []Operation `json:"provides,omitempty"`
}

// EnvExtensionDir は、本体が拡張機能の子プロセスに渡す環境変数の名前である（名前は仮。基本設計 5.1）。
// 値は、宣言ファイルのあるディレクトリの絶対パスである。
// 子プロセスの作業ディレクトリは本体のものを引き継ぐので、宣言ファイルの隣のファイルを探すときはこの値を使う。
// スクリプトの拡張機能の実行ファイルは、args の相対パスのスクリプトをここから解決する（基本設計 6.6）。
const EnvExtensionDir = "VI_IMITATION_EXTENSION_DIR"

// EnvMCPSocket は、本体がすべての拡張機能の子プロセスに渡す環境変数の名前である（名前は仮。基本設計 6.2）。
// 値は、MCP の中継コマンドが接続する Unix ドメインソケットの絶対パスで、本体が起動ごとに一意に決める。
// 本体は、起動の契機 ActivationSocket を宣言した拡張機能のために、このパスで起動時から待ち受ける（基本設計 6.2）。
// ACP の拡張機能はこのパスを中継コマンドの引数にしてエージェントへ渡す（基本設計 6.5）。
const EnvMCPSocket = "VI_IMITATION_MCP_SOCKET"

// EnvSocketFDs は、本体が ActivationSocket の拡張機能へ引き継いだファイル記述子の数を渡す環境変数の名前である（名前は仮。基本設計 6.2）。
// 値が n（1 以上）なら、ファイル記述子 SocketFDStart が待ち受けているソケットで、
// SocketFDStart+1 から SocketFDStart+n-1 までが、本体が受け付けて拡張機能へ渡す接続である。
// 値が空か変数が無ければ、何も引き継いでいない。
// 本体は、すべての拡張機能の子プロセスでこの変数を上書きするので、本体の環境の古い値は引き継がない。
// 拡張機能は ext/server の InheritedSocket で受け取る。
const EnvSocketFDs = "VI_IMITATION_SOCKET_FDS"

// SocketFDStart は、EnvSocketFDs で引き継ぐ最初のファイル記述子の番号である。
// 0〜2 は標準入出力で、本体との通信に使う。
const SocketFDStart = 3

// ActivationKind は、起動の契機の種類である。
type ActivationKind string

// 起動の契機の種類。
//
// ActivationOperator、ActivationMotion、ActivationTextObject、ActivationCommand は、
// 同じ拡張機能が追加した、その種類の操作が呼ばれたときに成り立つ。
// ActivationFileType は、Pattern と一致する種類のファイルを開いたときに成り立つ。
// ActivationSocket は、本体が EnvMCPSocket のパスで待ち受けるソケットへ、最初の接続が来たときに成り立つ（基本設計 6.2）。
// ID と Pattern は使わない。本体は、待ち受けているソケットと受け付けた接続を、子プロセスへ引き継ぐ（EnvSocketFDs）。
// 1 つの本体で待ち受けるソケットは 1 つなので、この契機を宣言した拡張機能が複数あれば、本体は最初の 1 つだけに使う。
//
// ActivationSettingsFileType は、利用者の設定のうちこの拡張機能に向けた部分（extensions.settings.<ID>）から、
// ファイルの種類の契機（ActivationFileType）を作ることを表す（基本設計 6.7。#277）。
// Pattern は、設定の中の値を指す JSON Pointer（RFC 6901）で、参照トークン "*" はオブジェクトのすべての項目か配列のすべての要素に当たる。
// 指した値が文字列ならその 1 つを、配列ならその中の文字列のすべてを、ファイルの種類の名前とする。それ以外の値は無視する。
// 本体（internal/config の LoadExtensions）は、宣言ファイルと設定を読んだときに、この契機を ActivationFileType の契機に置き換える。
// 利用者が設定に書いたファイルの種類を、設定の extensions.activation にもう一度書かずに済ませるためのものである。
// "*" が当たった項目の下にその先の値が無いか、文字列か文字列を含む配列でなければ、本体はその項目を設定の誤りとして知らせる（項目の名前の綴りの誤りのため）。
// 例: tree-sitter の拡張機能の {"on": "settingsFileType", "pattern": "/parsers/*/fileTypes"}。
//
// ActivationStartup は、本体が起動して最初の画面を描き終えた後に成り立つ（API 0.16.0 で足した。基本設計 5.2。#367）。
// ID と Pattern は使わない。利用者の操作もファイルを開くことも待たずに、起動している間は常に動かしておく拡張機能
// （開いたバッファすべてに色を付けるものなど）が使う。最初の画面を描く前には起動しないので、本体の起動の速さ（NFR-05）を損なわない。
// 利用者は、設定の extensions.disabled にその拡張機能の ID を書けば、起動させない（宣言ファイルを読まない）。
// 古い本体（0.15.0 まで）は知らない契機として警告にして無視するので、この契機だけを持つ拡張機能は起動しない。
const (
	ActivationOperator         ActivationKind = "operator"
	ActivationMotion           ActivationKind = "motion"
	ActivationTextObject       ActivationKind = "textObject"
	ActivationCommand          ActivationKind = "command"
	ActivationFileType         ActivationKind = "filetype"
	ActivationSocket           ActivationKind = "socket"
	ActivationSettingsFileType ActivationKind = "settingsFileType"
	ActivationStartup          ActivationKind = "startup"
)

// Known は、k がこの版の API の知っている契機の種類かどうかを返す。
func (k ActivationKind) Known() bool {
	switch k {
	case ActivationOperator, ActivationMotion, ActivationTextObject, ActivationCommand, ActivationFileType, ActivationSocket,
		ActivationSettingsFileType, ActivationStartup:
		return true
	}
	return false
}

// Activation は、1 つの起動の契機である。
type Activation struct {
	// On は、契機の種類である。
	On ActivationKind `json:"on"`
	// ID は、操作が呼ばれたときの契機で、その操作の ID である。それ以外の契機では空。
	ID string `json:"id,omitempty"`
	// Pattern は、ActivationFileType の契機で、ファイルの種類の名前である。
	// ActivationSettingsFileType の契機では、設定の中の値を指す JSON Pointer である。それ以外の契機では空。
	Pattern string `json:"pattern,omitempty"`
}

// Contributes は、拡張機能が追加する操作の一覧である。
type Contributes struct {
	Operators   []Contribution `json:"operators,omitempty"`
	Motions     []Contribution `json:"motions,omitempty"`
	TextObjects []Contribution `json:"textObjects,omitempty"`
	Commands    []Contribution `json:"commands,omitempty"`
	// StatusLine は、ステータス行とタブ行に足す部品の一覧である（基本設計 5.1、7.1。FR-92）。
	// 部品の中身は statusline/set で送る（MethodStatusLineSet）。足したのは API 0.5.0 である。
	StatusLine []StatusLinePart `json:"statusline,omitempty"`
	// PopupKeys は、この拡張機能が popup/open で開いたポップアップの中だけで効くキーである（FR-69。基本設計 5.1）。
	// 各項目の Keys は空にできない。フォーカスを入れたポップアップのノーマルモードで打つと、本体は view/key で ID を知らせる
	// （ViewKeyParams）。本体の既定のキーより先に引く。ID は popupKeys の中で一意にし、他の操作の ID とは別に数える。
	// 操作ではないので、起動の契機（Activation）の対象にはならない。
	PopupKeys []Contribution `json:"popupKeys,omitempty"`
}

// Contribution は、拡張機能が追加する 1 つの操作である。
type Contribution struct {
	// ID は、操作の ID である。拡張機能の中で一意にする。
	// 本体はドットリピートで、拡張機能の ID とこの ID を記録する（FR-12）。
	ID string `json:"id"`
	// Keys は、既定のキーの列（"ys" など）である。利用者の設定で上書きできる。
	// 既定のキーを持たない操作では空。
	Keys string `json:"keys,omitempty"`
	// Description は、操作の説明文である（基本設計 5.1）。本体はキーの一覧（FR-90）とヘルプ（:help）に出す。無ければ省く。
	// 足したのは API 0.4.0 の後だが、版は上げていない（APIVersion の doc）。
	Description string `json:"description,omitempty"`
	// Action は、Contributes.PopupKeys の項目で、キーを打ったときに view/key を送る代わりに本体がする操作である（API 0.10.0 で足した）。
	// 省くと view/key を送る。ほかの種類の操作では読まない。
	Action PopupKeyAction `json:"action,omitempty"`
}

// PopupKeyAction は、contributes.popupKeys のキーで、view/key の代わりに本体がする操作の名前である。
type PopupKeyAction string

// popupKeys のキーの操作。
const (
	// PopupKeySelect は、一覧と木の <CR> と同じく、カーソルの行の項目を選ぶ（view/select）。木の子を持つ項目なら開閉する（view/expand）。
	// ファイラの l（ディレクトリを開閉し、ファイルを開く。nvim の neo-tree の open。FR-105）に使う。
	// <CR> と同じ経路なので、選んだら閉じる中央のポップアップ（PaneOpenParams.CloseOnSelect）を閉じ、
	// 分割したペインで選んだ後の editor/open は端末多重化ソフトのフォーカスを元の client へ戻す（基本設計 1.2）。
	PopupKeySelect PopupKeyAction = "select"
)

// Known は、a がこの版の API の知っている popupKeys の操作の名前か、空（view/key を送る）かを返す。
func (a PopupKeyAction) Known() bool {
	return a == "" || a == PopupKeySelect
}

// KnownActivation は、Activation のうち種類が既知の契機だけを返す。
// 本体は、未知の契機を無視し、この一覧だけで起動を判断する。
func (m *Manifest) KnownActivation() []Activation {
	var known []Activation
	for _, a := range m.Activation {
		if a.On.Known() {
			known = append(known, a)
		}
	}
	return known
}

// ProvidesOp は、拡張機能が provides で op を名乗ったかを返す。op はこの版の API の知っている操作に限る。
func (m *Manifest) ProvidesOp(op Operation) bool {
	if !op.Known() {
		return false
	}
	for _, p := range m.Provides {
		if p == op {
			return true
		}
	}
	return false
}

// HandlesFileType は、拡張機能が、ファイルの種類 ft のバッファの操作の提供者の候補になるかを返す（Provides の doc）。
// ファイルの種類の契機（ActivationFileType）も、設定のファイルの種類の契機（ActivationSettingsFileType）も持たない拡張機能は、
// どのファイルの種類でも候補になる。
//
// 設定のファイルの種類の契機を持つ拡張機能は、それを展開したファイルの種類の契機（internal/config の LoadExtensions が足す）の
// どれかと一致するファイルの種類でだけ候補になる。設定に何も書いていなければ、どのファイルの種類でも候補にならない。
// 整形ツールの拡張機能（基本設計 6.10）が、設定に書いていないファイルの種類（Go など）で、
// LSP の拡張機能より先に format の提供者に選ばれないためである（#339）。
//
// req: FR-176
func (m *Manifest) HandlesFileType(ft string) bool {
	has := false
	for _, a := range m.Activation {
		if a.On != ActivationFileType && a.On != ActivationSettingsFileType {
			continue
		}
		has = true
		if a.On == ActivationFileType && a.Pattern == ft {
			return true
		}
	}
	return !has
}

// Validate は、宣言ファイルを読み込んだ後の値を検査する。
// 見つけた誤りをすべて errors.Join でまとめて err に返し、誤りが無ければ err は nil である。
// err が nil でない宣言ファイルは、拡張機能として使わない。
//
// 検査するのは次の項目である。
//
//   - ID と Command が空でないこと。ID が空白を含まないこと
//   - Activation、Provides、Contributes.StatusLine のどれかが 1 つ以上あること。
//     部品を足す拡張機能は、利用者が部品を並べていれば本体の起動時に起動する（基本設計 7.1）ので、Activation を空にしてよい
//   - Provides に同じ操作が 2 回現れないこと
//   - 操作の契機は、同じ種類の操作として Contributes にある ID を指すこと
//   - ファイルの種類の契機は、Pattern が空でないこと
//   - 設定からファイルの種類を作る契機（ActivationSettingsFileType）は、Pattern が "/" で始まること
//   - Contributes の各操作の ID が空でなく、種類をまたいで重複しないこと
//   - Contributes.StatusLine の各部品の ID が、拡張機能の ID と "." で始まり、その後が空でなく "." を含まず、空白を含まず、重複しないこと
//   - Contributes.PopupKeys の各項目の ID とキーが空でなく、ID が PopupKeys の中で重複しないこと
//   - NoBufferSync が true なら、SaveHook と、Contributes の operators、motions、textObjects が無いこと
//
// 未知の種類の契機と、未知の操作の名前の provides と、Contributes.PopupKeys の未知の Action は、誤りにせず、warnings に 1 件ずつ返す。
// API の後の版で契機の種類を足しても、古い本体が新しい拡張機能の宣言ファイルを丸ごと拒まないようにするためである。
// 本体は未知の契機だけを無視する（KnownActivation）。
// 契機がすべて未知なら、その拡張機能は起動しないが、誤りにはしない。
//
// Command のファイルが存在するかどうかは検査しない。
//
// req: FR-13
func (m *Manifest) Validate() (warnings []error, err error) {
	var errs []error
	if m.ID == "" {
		errs = append(errs, errors.New("id が空である"))
	} else if strings.ContainsFunc(m.ID, isSpace) {
		errs = append(errs, fmt.Errorf("id %q が空白を含む", m.ID))
	}
	if m.Command == "" {
		errs = append(errs, errors.New("command が空である"))
	}

	// 種類ごとの操作の ID の集合を作りながら、ID の空と重複を検査する。
	ids := map[ActivationKind]map[string]bool{}
	seen := map[string]string{}
	for _, group := range []struct {
		kind  ActivationKind
		field string
		items []Contribution
	}{
		{ActivationOperator, "operators", m.Contributes.Operators},
		{ActivationMotion, "motions", m.Contributes.Motions},
		{ActivationTextObject, "textObjects", m.Contributes.TextObjects},
		{ActivationCommand, "commands", m.Contributes.Commands},
	} {
		ids[group.kind] = map[string]bool{}
		for i, c := range group.items {
			path := fmt.Sprintf("contributes.%s[%d]", group.field, i)
			if c.ID == "" {
				errs = append(errs, fmt.Errorf("%s.id が空である", path))
				continue
			}
			if prev, ok := seen[c.ID]; ok {
				errs = append(errs, fmt.Errorf("%s.id %q が %s と重複する", path, c.ID, prev))
				continue
			}
			seen[c.ID] = path
			ids[group.kind][c.ID] = true
		}
	}

	errs = append(errs, m.validateStatusLine()...)

	popupIDs := map[string]bool{}
	for i, c := range m.Contributes.PopupKeys {
		path := fmt.Sprintf("contributes.popupKeys[%d]", i)
		switch {
		case c.ID == "":
			errs = append(errs, fmt.Errorf("%s.id が空である", path))
		case popupIDs[c.ID]:
			errs = append(errs, fmt.Errorf("%s.id %q が重複する", path, c.ID))
		case c.Keys == "":
			errs = append(errs, fmt.Errorf("%s.keys が空である", path))
		case !c.Action.Known():
			warnings = append(warnings, fmt.Errorf("%s.action %q は未知の操作なので無視する（view/key を送る）", path, c.Action))
		}
		popupIDs[c.ID] = true
	}

	if m.NoBufferSync {
		for _, c := range []struct {
			name string
			set  bool
		}{
			{"saveHook", m.SaveHook},
			{"contributes.operators", len(m.Contributes.Operators) > 0},
			{"contributes.motions", len(m.Contributes.Motions) > 0},
			{"contributes.textObjects", len(m.Contributes.TextObjects) > 0},
		} {
			if c.set {
				errs = append(errs, fmt.Errorf("noBufferSync とバッファの複製が要る %s は併せて書けない", c.name))
			}
		}
	}

	if len(m.Activation) == 0 && len(m.Provides) == 0 && len(m.Contributes.StatusLine) == 0 {
		errs = append(errs, errors.New("activation と provides と contributes.statusline がどれも空である"))
	}
	seenOps := map[Operation]bool{}
	for i, op := range m.Provides {
		path := fmt.Sprintf("provides[%d]", i)
		switch {
		case seenOps[op]:
			errs = append(errs, fmt.Errorf("%s %q が重複する", path, op))
		case !op.Known():
			warnings = append(warnings, fmt.Errorf("%s %q は未知の操作なので無視する", path, op))
		}
		seenOps[op] = true
	}
	for i, a := range m.Activation {
		path := fmt.Sprintf("activation[%d]", i)
		switch a.On {
		case ActivationOperator, ActivationMotion, ActivationTextObject, ActivationCommand:
			if a.ID == "" {
				errs = append(errs, fmt.Errorf("%s.id が空である", path))
			} else if !ids[a.On][a.ID] {
				errs = append(errs, fmt.Errorf("%s.id %q が contributes の %s に無い", path, a.ID, a.On))
			}
		case ActivationFileType:
			if a.Pattern == "" {
				errs = append(errs, fmt.Errorf("%s.pattern が空である", path))
			}
		case ActivationSettingsFileType:
			if !strings.HasPrefix(a.Pattern, "/") {
				errs = append(errs, fmt.Errorf("%s.pattern %q が / で始まる JSON Pointer でない", path, a.Pattern))
			}
		case ActivationSocket, ActivationStartup:
		default:
			warnings = append(warnings, fmt.Errorf("%s.on %q は未知の契機なので無視する", path, a.On))
		}
	}
	return warnings, errors.Join(errs...)
}

// validateStatusLine は、contributes.statusline の部品の ID を検査する（StatusLinePart.ID の doc の規則）。
func (m *Manifest) validateStatusLine() []error {
	var errs []error
	seen := map[string]int{}
	for i, p := range m.Contributes.StatusLine {
		path := fmt.Sprintf("contributes.statusline[%d]", i)
		local, ok := strings.CutPrefix(p.ID, m.ID+".")
		switch {
		case p.ID == "":
			errs = append(errs, fmt.Errorf("%s.id が空である", path))
			continue
		case m.ID == "" || !ok || local == "":
			errs = append(errs, fmt.Errorf("%s.id %q が拡張機能の ID %q と \".\" で始まらないか、その後が空である", path, p.ID, m.ID))
			continue
		case strings.Contains(local, "."):
			// 拡張機能の ID が "." を含むと、ID "a" の拡張機能の "a.b.x" と、ID "a.b" の拡張機能の "a.b.x" が重なるので、
			// 拡張機能の ID の後には "." を置かせない（PR #239 のレビューの推奨 2）。
			errs = append(errs, fmt.Errorf("%s.id %q の拡張機能の ID %q と \".\" の後が \".\" を含む", path, p.ID, m.ID))
			continue
		case strings.ContainsFunc(p.ID, isSpace):
			errs = append(errs, fmt.Errorf("%s.id %q が空白を含む", path, p.ID))
			continue
		}
		if prev, dup := seen[p.ID]; dup {
			errs = append(errs, fmt.Errorf("%s.id %q が contributes.statusline[%d] と重複する", path, p.ID, prev))
			continue
		}
		seen[p.ID] = i
	}
	return errs
}

// ValidateManifests は、複数の宣言ファイルを検査する。
// 各宣言ファイルの Validate に加えて、拡張機能の ID が重複しないことを検査する。
// 誤りと警告には、どの宣言ファイルのものかを前に付ける。
// 誤りはすべて errors.Join でまとめて err に返し、誤りが無ければ err は nil である。
//
// req: FR-13
func ValidateManifests(ms []Manifest) (warnings []error, err error) {
	var errs []error
	seen := map[string]int{}
	for i := range ms {
		m := &ms[i]
		ws, verr := m.Validate()
		for _, w := range ws {
			warnings = append(warnings, fmt.Errorf("宣言ファイル[%d]（id %q）: %w", i, m.ID, w))
		}
		if verr != nil {
			errs = append(errs, fmt.Errorf("宣言ファイル[%d]（id %q）: %w", i, m.ID, verr))
		}
		if m.ID == "" {
			continue
		}
		if prev, ok := seen[m.ID]; ok {
			errs = append(errs, fmt.Errorf("宣言ファイル[%d] の id %q が宣言ファイル[%d] と重複する", i, m.ID, prev))
			continue
		}
		seen[m.ID] = i
	}
	return warnings, errors.Join(errs...)
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
}
