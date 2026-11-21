// Package config は、設定ファイルと、拡張機能の宣言ファイルを読む（基本設計 2 章、7 章、5.1）。
//
// どちらのファイルも JSONC（コメントと末尾のカンマを許す JSON）で書く（FR-40）。
// JSONC は github.com/tailscale/hujson で標準の JSON に直してから encoding/json で読む（#9 の裁定）。
//
// このパッケージは読むだけで、読んだ値の適用（キーマップの展開、拡張機能の起動）は internal/editor と internal/exthost が行う。
// 基本設計 2 章の層の規則で、同じ層の internal/exthost も、上の層の internal/editor も import しない。
// 読んだ値は、editor.Config と exthost.Spec の項目と同じ型で返すので、呼び出し側（cmd/vi-imitation）が項目を写す。
//
// # 誤りの扱い
//
// 設定ファイルの誤りのある項目は使わず（既定値のままにし）、位置（行と列）と内容を Error で返す（基本設計 8 章）。
// 誤りがあっても、他の項目は読む。構文の誤りで全体を読めないときは、すべての項目が既定値になる。
//
// # 置き場所（仮）
//
// 設定ファイルの置き場所と、本体が拡張機能を探す規則は、仮に次のとおりにする（PR #109。基本設計 7 章、要件定義 6 章）。
//
//   - 設定ファイル: $XDG_CONFIG_HOME/vi-imitation/config.jsonc。XDG_CONFIG_HOME が空か相対パスなら ~/.config を使う（DefaultPath）
//   - 拡張機能: $XDG_DATA_HOME/vi-imitation/extensions/<名前>/（利用者）と、本体の実行ファイルの隣の extensions/<名前>/（同梱）。
//     XDG_DATA_HOME が空か相対パスなら ~/.local/share を使う（ExtensionDirs）。各ディレクトリの manifest.json を宣言ファイルとして読み、
//     同じ ID なら利用者のものを使う
//   - 配色: 設定ファイルと同じディレクトリの colors/<名前>.jsonc（利用者）と、本体に埋め込んだ colors/<名前>.jsonc（同梱）。
//     同じ名前なら利用者のものを使う（LoadColorScheme。#253）
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"time"

	"github.com/tailscale/hujson"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/editcmd"
	"github.com/piplupflipper/vi-imitation/internal/option"
)

// Config は、設定ファイルから読んだ値である（基本設計 7 章。項目の名前は仮）。
// ゼロ値は「設定ファイルが無い」状態で、すべての項目が既定値になる。
//
// 設定ファイルの形は次のとおりである。
//
//	{
//	  "keymap": { "normal": { "<Leader>w": ":w<CR>" }, "insert": { "<C-a>": { "action": "insert.line-start" } } },
//	  "leader": "<Space>",
//	  "fixendofline": true,
//	  "autoread": true,
//	  "checkIntervalMs": 1000,
//	  "fileTypes": { ".tmpl": "html", "Justfile": "make" },
//	  "readonlyPaths": ["~/go/pkg/mod", "/usr/include", "node_modules"],
//	  "extensions": {
//	    "syncTimeoutMs": 50,
//	    "startSyncTimeoutMs": 1000,
//	    "willSaveTimeoutMs": 1000,
//	    "memoryLimitMb": 0,
//	    "settings": { "lsp": { "servers": [] } },
//	    "activation": { "lsp": [{ "on": "filetype", "pattern": "python" }] },
//	    "disabled": ["memusage"]
//	  },
//	  "registers": { "historySize": 100, "maxEntryBytes": 1048576, "deleteToHistory": true, "clipboard": "osc52" },
//	  "colorscheme": "iceberg",
//	  "colors": { "diagnostic.error": { "sp": "#e27878", "undercurl": true }, "statusline": { "fg": 7, "bg": 0 } },
//	  "symbols": { "sign.breakpoint": "●", "popup.border": "┌─┐│┘─└│" },
//	  "icons": { "show": true, "fileTypes": { "go": { "text": "G", "fg": "#00add8" } } },
//	  "statusline": { "left": ["mode", "name", "modified", "readonly"], "right": ["filetype", "ruler", "position"] },
//	  "tabline": { "show": "always", "left": ["tabs"], "right": ["mrreview.status"] },
//	  "elevateCommand": ["sudo"],
//	  "commandAliases": { "tabedit": { "command": "tab.new", "min": 4 }, "tabnew": { "command": "tab.new" } },
//	  "indent": { "useSpaces": true, "width": 4 },
//	  "fileTypeOptions": { "go": { "indent": { "useSpaces": false } } }
//	}
//
// 設定の表（internal/option）の項目は、点で区切った名前を入れ子のキーにして書き、Options に入る
// （lineNumber.show なら "lineNumber": { "show": true }。基本設計 7.3）。
// 範囲がバッファごとの項目は、fileTypeOptions の下にファイルの種類ごとにも書け、FileTypeOptions に入る（FR-163）。
type Config struct {
	// Keymap は keymap の値で、editor.Config.Keymap にそのまま渡す。
	// モードの名前（"normal"、"visual"、"insert"、"commandLine"）から、キーの列の表記から展開するキーの列の表記への対応である。
	// 未知のモードと、editcmd.ParseKeysLeader で読めない表記の項目は除いてある。<Leader> は置き換えずに残す。
	// 右辺がオブジェクト（{ "action": "<ID>" }、{ "command": "<名前>" }）の項目は、ここではなく KeymapActions か KeymapCommands に入る。
	Keymap map[string]map[string]string
	// KeymapActions は、keymap の項目のうち右辺がオブジェクト（{ "action": "<ID>" }）のもので、editor.Config.KeymapActions に渡す（FR-126）。
	// モードの名前（"normal"、"visual"、"insert"）から、キーの列の表記から操作の ID への対応である。
	// 形の誤り（知らない項目、action が無い、commandLine に書いた）のある項目は除いてある。操作の ID とモードと左辺が使えるかは、
	// CheckKeymapActions で確かめる。項目が無ければ nil である。
	KeymapActions map[string]map[string]string
	// KeymapCommands は、keymap の項目のうち右辺が Ex コマンドのオブジェクト（{ "command": "<正規の名前>", "args": "<引数>" }）のもので、
	// editor.Config.KeymapCommands に渡す（FR-154）。モードの名前（"normal"、"visual"、"insert"）から、キーの列の表記から Ex コマンドへの対応である。
	// 形の誤り（知らない項目、action と command の両方がある、args だけがある、commandLine に書いた）のある項目は除いてある。
	// コマンドの名前とモードと左辺が使えるかは、CheckKeymapCommands で確かめる。項目が無ければ nil である。
	KeymapCommands map[string]map[string]KeymapCommand
	// Leader は leader の値で、キーの表記の <Leader> に当たるキーの列の表記である（基本設計 7.2。FR-97）。
	// editor.Config.Leader にそのまま渡す。項目が無いか誤りなら空で、editor の既定（editcmd.DefaultLeader）を使う。
	Leader string
	// SyncTimeout は extensions.syncTimeoutMs の値で、editor.Config.SyncTimeout にそのまま渡す。
	// 項目が無いか誤りなら nil（editor の既定値）。0 は「待たない」（FR-18）。
	SyncTimeout *time.Duration
	// StartSyncTimeout は extensions.startSyncTimeoutMs の値で、editor.Config.StartSyncTimeout にそのまま渡す。
	// 起動を含む問い合わせ（拡張機能が未起動か起動中のときの問い合わせ）の期限である（基本設計 5.2、5.4）。
	// 項目が無いか誤りか 0 なら 0 で、editor の既定値（editor.DefaultStartSyncTimeout。仮に 1 秒）を使う。
	StartSyncTimeout time.Duration
	// WillSaveTimeout は extensions.willSaveTimeoutMs の値で、保存の流れで拡張機能ごとに buffer/willSave の応答を待つ期限である
	// （基本設計 6.7 の保存の流れ。FR-177）。呼び出し側（cmd/vi-imitation）が exthost.Config.WillSaveTimeout にそのまま写す。
	// 項目が無いか誤りか 0 なら 0 で、exthost の既定値（exthost.DefaultWillSaveTimeout。仮に 1 秒）を使う。
	WillSaveTimeout time.Duration
	// FixEndOfLine は fixendofline の値で、書き出すときに末尾の改行を補うかどうかである（基本設計 4.1。Vim の 'fixendofline'）。
	// 項目が無いか誤りなら nil で、既定の有効（true）として扱う。
	// editor.Config にはまだ対応する項目が無いので、呼び出し側が buffer.Buffer.SetFixEndOfLine へ写す。
	FixEndOfLine *bool
	// AutoRead は autoread の値で、未変更のバッファのファイルが外で変わったときに問わずに読み直すかどうかである
	// （基本設計 6.7 の外での変更の確認。Vim の 'autoread'）。項目が無いか誤りなら nil で、editor の既定（有効）を使う。
	AutoRead *bool
	// CheckInterval は checkIntervalMs の値で、開いているバッファのファイルが外で変わっていないかを確かめる間隔である（基本設計 6.7）。
	// 項目が無いか誤りなら nil で、editor の既定（editor.DefaultCheckInterval）を使う。0 は「定期的に確かめない」である。
	CheckInterval *time.Duration
	// MemoryLimitMB は extensions.memoryLimitMb の値（MiB）で、0 は「上限なし」である（FR-20）。
	// 基本設計 7 章のとおり、拡張機能の子プロセスの RLIMIT_AS として設定する値で、
	// 呼び出し側（cmd/vi-imitation）が exthost.Config.MemoryLimitMB にそのまま写す。
	MemoryLimitMB int64
	// FileTypes は fileTypes の値で、editor.Config.FileTypes にそのまま渡す（基本設計 6.7 のファイルの種類の対応表）。
	// キーは拡張子（".md"）かファイル名（"Makefile"）、値はファイルの種類の名前である。
	FileTypes map[string]string
	// ExtensionSettings は extensions.settings の値で、拡張機能の ID から、その拡張機能に向けた設定への対応である。
	// LoadExtensions が Extension.Settings に入れ、本体は initialize の settings でそのまま渡す。
	ExtensionSettings map[string]json.RawMessage
	// ExtensionActivation は extensions.activation の値で、拡張機能の ID から、宣言ファイルの activation に足す契機への対応である
	// （基本設計 6.7 の「拡張機能ごとの activation の追加」）。足せるのは filetype の契機だけである。
	ExtensionActivation map[string][]ext.Activation
	// ExtensionDisabled は extensions.disabled の値で、使わない拡張機能の ID の集合である（基本設計 5.2。FR-226）。
	// LoadExtensions は、この ID の宣言ファイルを一覧に入れないので、本体はその拡張機能を起動せず、キーもコマンドも部品も足さない。
	// 起動の契機 startup の拡張機能（本体の起動で起動する）を止めるのに使う。項目が無ければ nil である。
	ExtensionDisabled map[string]bool
	// ExtensionInstall は extensions.install の値で、取得する拡張機能の列である（基本設計 5.6）。
	// 誤りのある項目は除いてある。呼び出し側が extmgr.Spec に写して extmgr.Sync に渡す。
	ExtensionInstall []ExtensionInstall
	// ExtensionInstallValid は、extensions.install を誤りなく読めたこと（項目が無いことと、設定ファイルが無いことを含む）である。
	// 設定ファイルを読めないとき、構文の誤りのとき、extensions か extensions.install に誤りがあるときは false で、
	// 呼び出し側は extmgr.Options.KeepUnlisted を true にして、書き損じで拡張機能を削除しない（FR-122）。
	ExtensionInstallValid bool
	// RegisterHistorySize は registers.historySize の値で、レジスターの履歴の件数である（FR-53）。
	// 項目が無いか誤り（1 未満を含む）なら 0 で、command の既定値（command.DefaultHistorySize）を使う。
	RegisterHistorySize int
	// RegisterMaxEntryBytes は registers.maxEntryBytes の値で、履歴に積む 1 件の最大のバイト数である（FR-53）。
	// 項目が無いか誤り（1 未満を含む）なら 0 で、command の既定値（command.DefaultMaxEntryBytes）を使う。
	RegisterMaxEntryBytes int
	// DeleteToHistory は registers.deleteToHistory の値で、名前を省略した削除と変更を履歴に積むかである（FR-52）。
	// 項目が無いか誤りなら nil で、既定の有効（true）として扱う。
	DeleteToHistory *bool
	// RegisterClipboard は registers.clipboard の値で、レジスターの履歴の先頭をホストのクリップボードへ送る手段である（FR-56）。
	// ClipboardOSC52 か ClipboardOff で、項目が無いか誤りなら空で、既定の ClipboardOSC52 として扱う。
	RegisterClipboard string
	// ReadOnlyPaths は readonlyPaths の値で、開いた時点で編集を禁止にするファイルの置き場所のディレクトリである（基本設計 4.6。FR-120）。
	// 各項目は、絶対パス（"~" はホームのディレクトリに展開してある）か、"/" を含まないディレクトリの名前（"node_modules"）である。
	// 誤りのある項目は除いてある。
	// editor.Config.ReadOnlyPaths にそのまま渡す。
	ReadOnlyPaths []string
	// Colors は colors の値で、色の名前から属性への対応である（基本設計 7.1。FR-91）。本体の既定の対応を名前ごとに置き換える。
	// 誤りのある項目は除いてある。名前が本体の知るものかは CheckAppearance で確かめる。
	Colors map[string]Style
	// ColorScheme は colorscheme の値で、選んだ配色の名前である（基本設計 7.1。FR-125）。
	// 項目が無いか誤り（使えない文字を含む名前を含む）なら空で、配色を選ばない（本体の既定の対応だけを使う）。
	ColorScheme string
	// SchemeColors は、ColorScheme の配色のファイルから LoadColorScheme が読んだ、色の名前から属性への対応である（FR-125）。
	// 本体の既定の対応を名前ごとに置き換え、Colors はその上にさらに名前ごとに置き換える。
	// 誤りのある項目と、本体の知らない名前は除いてある。LoadColorScheme を呼ぶまでは nil である。
	SchemeColors map[string]Style
	// Symbols は symbols の値で、記号の名前から描く文字列への対応である（基本設計 7.1。FR-93）。
	// 名前と文字列が使えるかは CheckAppearance で確かめる。
	Symbols map[string]string
	// Icons は icons の値のうちアイコンの表の項目で、アイコンの名前（icons の下の位置。"directory"、"fileTypes.go"）から
	// アイコンへの対応である（基本設計 7.1。FR-152）。本体の既定の表を名前ごとに置き換える。項目が無ければ nil である。
	// 誤りのあるアイコンは除いてある。文字列の表示幅は CheckAppearance で確かめる。icons.show は Options に入る。
	Icons map[string]Icon
	// StatusLine は statusline の値で、ステータス行に並べる部品の ID の列である（基本設計 7.1。FR-92）。
	// 項目が無いか誤りなら nil で、本体の既定の並びを使う。部品の ID は CheckAppearance で確かめる。
	StatusLine *StatusLine
	// TabLineShow は tabline.show の値で、タブの行を出す場合である（基本設計 7.1。FR-62）。
	// TabLineAlways か TabLineMultiple で、項目が無いか誤りなら空で、既定の TabLineAlways として扱う。
	TabLineShow string
	// TabLineParts は tabline の left と right の値で、タブの行に並べる部品の ID の列である（基本設計 7.1。FR-92）。
	// left も right も書かなければ nil で、本体の既定の並び（タブの見出しの並びだけ）を使う。部品の ID は CheckAppearance で確かめる。
	TabLineParts *StatusLine
	// ElevateCommand は elevateCommand の値で、権限の不足で書けないファイルを書くときに tee <パス> の前に付けるコマンドと引数である
	// （基本設計 4.7。FR-79。項目の名前は仮）。項目が無いか誤りなら nil で、editor の既定（["sudo"]）を使う。
	// editor.Config.ElevateCommand にそのまま渡す。
	ElevateCommand []string
	// Options は、設定の表（option の表。基本設計 7.3。FR-131）の項目の、最上位に書いた値である。
	// 正規の名前から値への対応で、書かなかった項目と誤りのある項目は入らない（表の既定値を使う）。項目が無ければ nil である。
	// 範囲がエディタ全体の項目はエディタの初めの値、範囲がバッファごとの項目はバッファを開くときの初めの値（FR-163）になる。
	// editor.Config.Options にそのまま渡す。
	Options map[string]option.Value
	// FileTypeOptions は fileTypeOptions の値で、ファイルの種類の名前から、そのファイルの種類のバッファを開くときに当てる、
	// 範囲がバッファごとの項目の値への対応である（FR-163。基本設計 7.3）。誤りのある項目は入らない。項目が無ければ nil である。
	// editor.Config.FileTypeOptions にそのまま渡す。
	FileTypeOptions map[string]map[string]option.Value
	// CommandAliases は commandAliases の値で、Ex コマンドの名前を付け替えるだけの別名を、書いた順に並べたものである（基本設計 7.3。FR-150）。
	// 形の誤りのある別名は除いてある。表に足せるか（名前、省略の長さ、付け替える先、重なり）は CheckCommandAliases で確かめる。
	// 項目が無ければ nil である。
	CommandAliases []CommandAlias

	// pos は、見た目の設定の項目を書いた位置である（CheckAppearance）。
	pos appearancePos

	// keymapPos は、KeymapActions と KeymapCommands の各項目の右辺を書いた位置である（CheckKeymapActions、CheckKeymapCommands）。
	// 1 つのモードの 1 つの左辺は、どちらか一方にだけ入る。
	keymapPos map[string]map[string]*Error

	// aliasPos は、CommandAliases の各別名の名前を書いた位置で、CommandAliases と同じ順に並べる（CheckCommandAliases）。
	aliasPos []*Error

	// extPos は、extensions.settings と extensions.activation に書いた拡張機能の ID の位置である。
	// 見つからない拡張機能の ID を LoadExtensions が知らせるのに使う。
	extPos map[string]*Error
}

// FileName は、設定ファイルの名前である（仮）。
const FileName = "config.jsonc"

// appName は、XDG のディレクトリの下に置くディレクトリの名前である。
const appName = "vi-imitation"

// mapModes は、keymap のモードの名前である。internal/editor の mapModeNames と同じにする。
var mapModes = map[string]bool{"normal": true, "visual": true, "insert": true, "commandLine": true}

// maxSyncTimeoutMs は、syncTimeoutMs の上限である。time.Duration に直しても溢れない値にする。
const maxSyncTimeoutMs = math.MaxInt64 / int64(time.Millisecond)

// Load は、path の設定ファイルを読む。
// ファイルが無ければ、ゼロ値の Config と nil を返す（設定ファイルは無くてよい）。
// ファイルを読めないときは、ゼロ値の Config と、位置の無い Error を返す。
// それ以外は Parse と同じである。
func Load(path string) (Config, []error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{ExtensionInstallValid: true}, nil
	}
	if err != nil {
		return Config{}, []error{&Error{Path: path, Msg: "設定ファイルを読めない: " + err.Error()}}
	}
	return Parse(path, data)
}

// topLevelNames は、設定ファイルの最上位の項目の名前のうち、設定の表（option）の項目でないもの（Parse の switch の case）である。
// 並びは Parse の switch と同じにする（TestTopLevelNames が照合する）。
var topLevelNames = []string{
	"keymap", "leader", "fixendofline", "autoread", "checkIntervalMs", "fileTypes", "extensions", "registers", "readonlyPaths",
	"colors", "colorscheme", "symbols", "icons", "statusline", "tabline", "elevateCommand", "commandAliases", "fileTypeOptions",
}

// TopLevelNames は、設定ファイルの最上位に書ける項目の名前のうち、設定の表（option.Defs）の項目でないものを返す。
// 本体のヘルプの語の一覧（:finder.help。FR-148）が、設定の名前として使う。返す値は写しである。
func TopLevelNames() []string {
	return append([]string(nil), topLevelNames...)
}

// Parse は、data を JSONC の設定ファイルとして読む。path は誤りの文言に出すパスである。
//
// 誤りのある項目（型の違い、範囲外の値、読めないキーの表記、未知の項目）は Config に入れず、
// 位置と内容を *Error で返す。誤りのある項目があっても、他の項目は読む。
// 未知の項目は、綴りの誤りに気づけるように誤りとして知らせ、値は無視する。
// 空のファイルと、空白とコメントだけのファイル（設定を全部コメントにしたもの）は、{} と同じに扱う。
// 先頭の UTF-8 の BOM は読み飛ばし、位置は BOM の後から数える。
//
// req: FR-40
func Parse(path string, data []byte) (Config, []error) {
	var cfg Config
	s := newSource(path, data)
	if s.onlyExtra() {
		cfg.ExtensionInstallValid = true
		return cfg, nil
	}
	root, err := s.parseJSONC()
	if err != nil {
		return cfg, []error{err}
	}
	obj, ok := root.Value.(*hujson.Object)
	if !ok {
		s.report(root.StartOffset, "設定ファイルの最上位はオブジェクトにする")
		return cfg, s.problems
	}
	cfg.ExtensionInstallValid = true
	for _, m := range s.members(obj, "") {
		switch m.name {
		case "keymap":
			s.keymap(m.value, &cfg)
		case "leader":
			cfg.Leader = s.leader(m.value)
		case "fixendofline":
			if b, ok := s.boolean(m.value, "fixendofline"); ok {
				cfg.FixEndOfLine = &b
			}
		case "autoread":
			if b, ok := s.boolean(m.value, "autoread"); ok {
				cfg.AutoRead = &b
			}
		case "checkIntervalMs":
			if n, ok := s.nonNegInt(m.value, "checkIntervalMs", maxSyncTimeoutMs); ok {
				d := time.Duration(n) * time.Millisecond
				cfg.CheckInterval = &d
			}
		case "fileTypes":
			cfg.FileTypes = s.fileTypes(m.value)
		case "extensions":
			s.extensions(m.value, &cfg)
		case "registers":
			s.registers(m.value, &cfg)
		case "readonlyPaths":
			cfg.ReadOnlyPaths = s.readOnlyPaths(m.value)
		case "colors":
			cfg.Colors = s.colors(m.value, &cfg)
		case "colorscheme":
			cfg.ColorScheme = s.colorScheme(m.value, &cfg)
		case "symbols":
			cfg.Symbols = s.symbols(m.value, &cfg)
		case "icons":
			s.icons(m, &cfg)
		case "statusline":
			cfg.StatusLine = s.statusLine(m.value, &cfg)
		case "tabline":
			cfg.TabLineShow, cfg.TabLineParts = s.tabLine(m.value, &cfg)
		case "elevateCommand":
			cfg.ElevateCommand = s.elevateCommand(m.value)
		case "commandAliases":
			cfg.CommandAliases = s.commandAliases(m.value, &cfg)
		case "fileTypeOptions":
			cfg.FileTypeOptions = s.fileTypeOptions(m.value, option.Defs())
		default:
			// 上の項目でなければ、設定の表（option）の項目として読む。表の項目の名前の最上位の段は、上の項目と重ねない
			// （icons だけは、表の項目 icons.show を icons が読む）。
			if !s.option(m, "", option.Defs(), &cfg) {
				s.report(m.nameOff, "未知の項目 %s を無視する", join("", m.name))
			}
		}
	}
	return cfg, s.problems
}

// keymap は、keymap の値を読む。右辺が文字列の項目を Keymap に、オブジェクトの項目（{ "action": "<ID>" }。FR-126）を
// KeymapActions に、Ex コマンドのオブジェクトの項目（{ "command": "<名前>" }。FR-154）を KeymapCommands に入れる（keymapAction）。
// 操作の ID とコマンドの名前は、ここでは空でないことだけを確かめ、使えるかは CheckKeymapActions と CheckKeymapCommands で確かめる。
//
// req: FR-126
func (s *source) keymap(v *hujson.Value, cfg *Config) {
	obj, ok := s.object(v, "keymap")
	if !ok {
		return
	}
	cfg.Keymap = map[string]map[string]string{}
	for _, mode := range s.members(obj, "keymap") {
		path := join("keymap", mode.name)
		if !mapModes[mode.name] {
			s.report(mode.nameOff, "未知のモード %s を無視する", path)
			continue
		}
		mobj, ok := s.object(mode.value, path)
		if !ok {
			continue
		}
		maps := map[string]string{}
		for _, e := range s.members(mobj, path) {
			// <Leader> の置き換え先は leader の項目で決まるが、どの leader でも空でない列になるので、既定の leader で確かめる。
			if keys, err := editcmd.ParseKeysLeader(e.name, nil); err != nil || len(keys) == 0 {
				s.report(e.nameOff, "%s: キーの列 %q を読めない%s", path, e.name, reason(err))
				continue
			}
			if _, isObj := e.value.Value.(*hujson.Object); isObj {
				s.keymapAction(cfg, mode.name, e)
				continue
			}
			rhs, ok := s.str(e.value, join(path, e.name))
			if !ok {
				continue
			}
			if _, err := editcmd.ParseKeysLeader(rhs, nil); err != nil {
				s.report(e.value.StartOffset, "%s: 展開する列 %q を読めない: %v", join(path, e.name), rhs, err)
				continue
			}
			maps[e.name] = rhs
		}
		cfg.Keymap[mode.name] = maps
	}
}

// KeymapCommand は、keymap の右辺に書いた Ex コマンド（{ "command": "<正規の名前>", "args": "<引数>" }）の 1 件である（FR-154）。
type KeymapCommand struct {
	// Name は command の値で、空でない。正規の名前かは CheckKeymapCommands で確かめる。
	Name string
	// Args は args の値で、書かなければ空である。
	Args string
}

// keymapAction は、keymap のモード mode の、右辺がオブジェクトの項目 e を読み、KeymapActions か KeymapCommands に入れる。
// オブジェクトは、本体の操作（{ "action": "<ID>" }。FR-126）か、Ex コマンド（{ "command": "<名前>", "args": "<引数>" }。FR-154）である。
// action と command はどちらか一方を書き、args は command と一緒にだけ書ける。値はどれも文字列で、action と command は空にできない。
// 知らない項目、action と command の両方、どちらも無いオブジェクト、command の無い args は誤りにして、項目ごと使わない。
// commandLine には書けない（コマンドラインの入力は操作の表を通らない）。
//
// req: FR-126, FR-154
func (s *source) keymapAction(cfg *Config, mode string, e member) {
	path := join(join("keymap", mode), e.name)
	if mode == "commandLine" {
		s.report(e.value.StartOffset, "%s: commandLine には操作もコマンドも書けない（キーの列の文字列を書く）", path)
		return
	}
	obj, _ := e.value.Value.(*hujson.Object)
	vals := map[string]string{}
	bad := false
	for _, m := range s.members(obj, path) {
		switch m.name {
		case "action", "command", "args":
		default:
			s.report(m.nameOff, "%s: 未知の項目 %s がある。この割り当てを使わない", path, m.name)
			bad = true
			continue
		}
		v, ok := s.str(m.value, join(path, m.name))
		if !ok {
			bad = true
			continue
		}
		if v == "" && m.name != "args" {
			msg := map[string]string{"action": "操作の ID が空である", "command": "コマンドの名前が空である"}[m.name]
			s.report(m.value.StartOffset, "%s: %s", join(path, m.name), msg)
			bad = true
			continue
		}
		vals[m.name] = v
	}
	_, hasAction := vals["action"]
	_, hasCommand := vals["command"]
	_, hasArgs := vals["args"]
	switch {
	case bad:
		return
	case hasAction && hasCommand:
		s.report(e.value.StartOffset, "%s: action と command は一方だけを書く。この割り当てを使わない", path)
		return
	case hasArgs && !hasCommand:
		s.report(e.value.StartOffset, "%s: args は command と一緒に書く。この割り当てを使わない", path)
		return
	case !hasAction && !hasCommand:
		s.report(e.value.StartOffset, "%s: 操作の ID（action）か Ex コマンド（command）が無い", path)
		return
	}
	if cfg.keymapPos == nil {
		cfg.keymapPos = map[string]map[string]*Error{}
	}
	if cfg.keymapPos[mode] == nil {
		cfg.keymapPos[mode] = map[string]*Error{}
	}
	cfg.keymapPos[mode][e.name] = s.at(e.value.StartOffset, "")
	if hasCommand {
		if cfg.KeymapCommands == nil {
			cfg.KeymapCommands = map[string]map[string]KeymapCommand{}
		}
		if cfg.KeymapCommands[mode] == nil {
			cfg.KeymapCommands[mode] = map[string]KeymapCommand{}
		}
		cfg.KeymapCommands[mode][e.name] = KeymapCommand{Name: vals["command"], Args: vals["args"]}
		return
	}
	if cfg.KeymapActions == nil {
		cfg.KeymapActions = map[string]map[string]string{}
	}
	if cfg.KeymapActions[mode] == nil {
		cfg.KeymapActions[mode] = map[string]string{}
	}
	cfg.KeymapActions[mode][e.name] = vals["action"]
}

// CheckKeymapActions は、KeymapActions の各項目（モードの名前、左辺の表記、操作の ID）を check で確かめ、使えない項目を除いて、
// 位置と内容を誤りとして返す（FR-126）。操作の表は internal/input が持ち、このパッケージは同じ層の internal/input を import しないので、
// 呼び出し側（cmd/vi-imitation）が Parse の後に、確かめる関数（editor.KeymapActionCheck）を渡して 1 回呼ぶ。
//
// req: FR-126
func (c *Config) CheckKeymapActions(check func(mode, lhs, id string) error) []error {
	var errs []error
	for mode, maps := range c.KeymapActions {
		for lhs, id := range maps {
			if err := check(mode, lhs, id); err != nil {
				errs = append(errs, c.keymapPos[mode][lhs].with("%s: 操作 %s を使えない: %v", join(join("keymap", mode), lhs), id, err))
				delete(maps, lhs)
			}
		}
	}
	SortErrors(errs)
	return errs
}

// CheckKeymapCommands は、KeymapCommands の各項目（モードの名前、左辺の表記、Ex コマンド）を check で確かめ、使えない項目を除いて、
// 位置と内容を誤りとして返す（FR-154）。書ける Ex コマンドの名前は、拡張機能の宣言ファイルと Ex コマンドの別名の表（設定の commandAliases を
// 足したもの）で決まり、このパッケージは同じ層の internal/command を import しないので、呼び出し側（cmd/vi-imitation）が宣言ファイルを読み、
// 別名を足した後に、確かめる関数（editor.KeymapCommandCheck）を渡して 1 回呼ぶ（CheckCommandAliases と同じ形）。
//
// req: FR-154
func (c *Config) CheckKeymapCommands(check func(mode, lhs string, cmd KeymapCommand) error) []error {
	var errs []error
	for mode, maps := range c.KeymapCommands {
		for lhs, cmd := range maps {
			if err := check(mode, lhs, cmd); err != nil {
				errs = append(errs, c.keymapPos[mode][lhs].with("%s: コマンド %s を使えない: %v", join(join("keymap", mode), lhs), cmd.Name, err))
				delete(maps, lhs)
			}
		}
	}
	SortErrors(errs)
	return errs
}

// leader は、leader の値を読む（基本設計 7.2。FR-97）。
// 値は ParseKeys の表記の文字列で、空の列と <Leader> を含む表記は誤りとして既定値（空）を返す。
//
// req: FR-97
func (s *source) leader(v *hujson.Value) string {
	str, ok := s.str(v, "leader")
	if !ok {
		return ""
	}
	if _, err := editcmd.ParseLeader(str); err != nil {
		s.report(v.StartOffset, "leader %q を読めない: %v", str, err)
		return ""
	}
	return str
}

// reason は、err があれば ": <err>" を返す。
func reason(err error) string {
	if err == nil {
		return "（空の列）"
	}
	return ": " + err.Error()
}

// fileTypes は、fileTypes の値を読む。
func (s *source) fileTypes(v *hujson.Value) map[string]string {
	obj, ok := s.object(v, "fileTypes")
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, e := range s.members(obj, "fileTypes") {
		path := join("fileTypes", e.name)
		if e.name == "" {
			s.report(e.nameOff, "fileTypes の拡張子かファイル名が空")
			continue
		}
		name, ok := s.str(e.value, path)
		if !ok {
			continue
		}
		if name == "" {
			s.report(e.value.StartOffset, "%s のファイルの種類の名前が空", path)
			continue
		}
		out[e.name] = name
	}
	return out
}

// extensions は、extensions の値を読んで cfg に入れる。
func (s *source) extensions(v *hujson.Value, cfg *Config) {
	obj, ok := s.object(v, "extensions")
	if !ok {
		cfg.ExtensionInstallValid = false
		return
	}
	for _, m := range s.members(obj, "extensions") {
		path := join("extensions", m.name)
		switch m.name {
		case "syncTimeoutMs":
			if n, ok := s.nonNegInt(m.value, path, maxSyncTimeoutMs); ok {
				d := time.Duration(n) * time.Millisecond
				cfg.SyncTimeout = &d
			}
		case "startSyncTimeoutMs":
			if n, ok := s.nonNegInt(m.value, path, maxSyncTimeoutMs); ok {
				cfg.StartSyncTimeout = time.Duration(n) * time.Millisecond
			}
		case "willSaveTimeoutMs":
			// req: FR-177
			if n, ok := s.nonNegInt(m.value, path, maxSyncTimeoutMs); ok {
				cfg.WillSaveTimeout = time.Duration(n) * time.Millisecond
			}
		case "memoryLimitMb":
			if n, ok := s.nonNegInt(m.value, path, math.MaxInt64>>20); ok {
				cfg.MemoryLimitMB = n
			}
		case "settings":
			cfg.ExtensionSettings = s.extensionSettings(m.value, cfg)
		case "activation":
			cfg.ExtensionActivation = s.extensionActivation(m.value, cfg)
		case "install":
			cfg.ExtensionInstall, cfg.ExtensionInstallValid = s.extensionInstall(m.value)
		case "disabled":
			cfg.ExtensionDisabled = s.extensionDisabled(m.value, cfg)
		default:
			s.report(m.nameOff, "未知の項目 %s を無視する", path)
		}
	}
}

// maxHistorySize は、historySize の上限である（command.MaxHistorySize と同じ値。基本設計 7 章）。
const maxHistorySize = 10000

// maxEntryBytesSetting は、maxEntryBytes の上限である。32 ビットの int でも溢れない値にする。
const maxEntryBytesSetting = math.MaxInt32

// registers.clipboard の値である（基本設計 7 章。FR-56）。
const (
	// ClipboardOSC52 は、履歴の先頭が変わるたびに OSC 52 の制御シーケンスで端末へ送ることを表す（既定）。
	ClipboardOSC52 = "osc52"
	// ClipboardOff は、ホストのクリップボードへ送らないことを表す。
	ClipboardOff = "off"
)

// registers は、registers の値を読んで cfg に入れる（基本設計 7 章。FR-52、FR-53、FR-56）。
// historySize は 1 以上 10000 以下、maxEntryBytes は 1 以上の整数で、範囲の外の値は誤りとして既定値を使う。
// clipboard は "osc52" か "off" で、それ以外の値は誤りとして既定値（"osc52"）を使う。
//
// req: FR-52, FR-53, FR-56
func (s *source) registers(v *hujson.Value, cfg *Config) {
	obj, ok := s.object(v, "registers")
	if !ok {
		return
	}
	for _, m := range s.members(obj, "registers") {
		path := join("registers", m.name)
		switch m.name {
		case "historySize":
			if n, ok := s.posInt(m.value, path, maxHistorySize); ok {
				cfg.RegisterHistorySize = int(n)
			}
		case "maxEntryBytes":
			if n, ok := s.posInt(m.value, path, maxEntryBytesSetting); ok {
				cfg.RegisterMaxEntryBytes = int(n)
			}
		case "deleteToHistory":
			if b, ok := s.boolean(m.value, path); ok {
				cfg.DeleteToHistory = &b
			}
		case "clipboard":
			if str, ok := s.str(m.value, path); ok {
				switch str {
				case ClipboardOSC52, ClipboardOff:
					cfg.RegisterClipboard = str
				default:
					s.report(m.value.StartOffset, "%s は %q か %q にする", path, ClipboardOSC52, ClipboardOff)
				}
			}
		default:
			s.report(m.nameOff, "未知の項目 %s を無視する", path)
		}
	}
}

// notePos は、拡張機能の ID を書いた位置を cfg に記録する。
func (s *source) notePos(cfg *Config, id string, off int, path string) {
	if cfg.extPos == nil {
		cfg.extPos = map[string]*Error{}
	}
	if _, ok := cfg.extPos[id]; !ok {
		cfg.extPos[id] = s.at(off, "%s の拡張機能 %s が見つからない", path, id)
	}
}

// extensionSettings は、extensions.settings の値を読む。値の中身は拡張機能が解釈するので、形を問わない。
func (s *source) extensionSettings(v *hujson.Value, cfg *Config) map[string]json.RawMessage {
	obj, ok := s.object(v, "extensions.settings")
	if !ok {
		return nil
	}
	out := map[string]json.RawMessage{}
	for _, e := range s.members(obj, "extensions.settings") {
		var buf bytes.Buffer
		if err := json.Compact(&buf, s.raw(e.value)); err != nil {
			// 構文木から切り出した標準の JSON なので、ここには来ない。
			s.report(e.value.StartOffset, "%s を読めない: %v", join("extensions.settings", e.name), err)
			continue
		}
		out[e.name] = buf.Bytes()
		s.notePos(cfg, e.name, e.nameOff, "extensions.settings")
	}
	return out
}

// extensionDisabled は、extensions.disabled の値（拡張機能の ID の文字列の配列）を読む（FR-226）。
// 文字列でない要素と空の文字列は誤りとして知らせて除く。同じ ID を 2 回書いても誤りにしない。
func (s *source) extensionDisabled(v *hujson.Value, cfg *Config) map[string]bool {
	const path = "extensions.disabled"
	arr, ok := s.array(v, path)
	if !ok {
		return nil
	}
	out := map[string]bool{}
	for i := range arr.Elements {
		el := &arr.Elements[i]
		id, ok := s.str(el, path+" の要素")
		if !ok {
			continue
		}
		if id == "" {
			s.report(el.StartOffset, "%s に空の ID は書けない", path)
			continue
		}
		out[id] = true
		s.notePos(cfg, id, el.StartOffset, path)
	}
	return out
}

// extensionActivation は、extensions.activation の値を読む。
func (s *source) extensionActivation(v *hujson.Value, cfg *Config) map[string][]ext.Activation {
	obj, ok := s.object(v, "extensions.activation")
	if !ok {
		return nil
	}
	out := map[string][]ext.Activation{}
	for _, e := range s.members(obj, "extensions.activation") {
		path := join("extensions.activation", e.name)
		arr, ok := s.array(e.value, path)
		if !ok {
			continue
		}
		var acts []ext.Activation
		for i := range arr.Elements {
			el := &arr.Elements[i]
			var a ext.Activation
			dec := json.NewDecoder(bytes.NewReader(s.raw(el)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&a); err != nil {
				s.report(el.StartOffset, "%s の契機を読めない: %v", path, err)
				continue
			}
			if a.On != ext.ActivationFileType || a.Pattern == "" || a.ID != "" {
				s.report(el.StartOffset, `%s に足せるのは {"on": "filetype", "pattern": "<ファイルの種類>"} の契機だけ`, path)
				continue
			}
			acts = append(acts, a)
		}
		out[e.name] = acts
		s.notePos(cfg, e.name, e.nameOff, "extensions.activation")
	}
	return out
}
