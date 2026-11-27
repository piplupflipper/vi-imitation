package ui

import (
	"maps"
	"slices"
	"strings"
	"unicode"
)

// 色の種類の名前（論理名）。名前から属性への対応は Theme が持つ。
//
// 構文の色の名前は、基本設計 6.1 の decoration/set の style の表（仮）と同じである。
// それ以外は、画面の部品と診断に付ける、このパッケージが定めた名前（仮）である。
const (
	StyleComment     = "comment"
	StyleKeyword     = "keyword"
	StyleString      = "string"
	StyleNumber      = "number"
	StyleConstant    = "constant"
	StyleFunction    = "function"
	StyleType        = "type"
	StyleVariable    = "variable"
	StyleOperator    = "operator"
	StylePunctuation = "punctuation"
	// StyleTag は、マークアップ（JSX など）のタグの名前である（tree-sitter のキャプチャ名 @tag。FR-330）。
	StyleTag = "tag"
	// StyleAttribute は、マークアップ（JSX など）のタグの属性の名前である（tree-sitter のキャプチャ名 @attribute。FR-330）。
	StyleAttribute = "attribute"

	// StyleVisual は、ビジュアルモードの選択の範囲である。
	StyleVisual = "visual"
	// StyleStatusLine は、ステータス行である。
	StyleStatusLine = "statusline"
	// StyleNonText は、バッファの最終行より後の行の "~" と、横のスクロールで左の端が欠けた全角文字の "<" である。
	StyleNonText = "nontext"
	// StyleSpecialKey は、^A や <80> の形で出した文字である。
	StyleSpecialKey = "specialkey"
	// StyleWhitespace は、空白の記号（FR-170）のうち、タブ文字、空白、ノーブレークスペースの記号である（Neovim 0.12.5 の Whitespace）。
	// 行末の記号は StyleNonText で描く（Neovim と同じ）。Vim 9.1 は空白の記号に SpecialKey を使うが、^A などの表記と分けて色を付けられるよう、
	// Neovim に合わせて名前を分けた。^A などの表記と同じく下地の色にし、構文、診断、検索の一致、範囲の色、選択の色をこの上に重ねる（layers.go）。
	StyleWhitespace = "whitespace"
	// StyleLineNr は、行番号の列（Frame.LineNumbers。FR-135）である。数字と、右の空けた 1 列を含む列の全体に付ける（Vim の LineNr）。
	StyleLineNr = "linenr"
	// StyleSearch は、直前の検索のパターンに一致する範囲（Frame.Search。FR-137）である（Vim の Search）。
	// 構文の色、見比べの色、診断の上に重ね、選択の色（StyleVisual）の下に置く。
	StyleSearch = "search"
	// StyleIncSearch は、/ と ? でパターンを打っている間に、打つたびの検索で出す今の一致（Frame.Highlights の 1 つ。FR-153）である
	// （Vim と Neovim の IncSearch）。検索の一致の色（StyleSearch）とヤンクした範囲の色（StyleYank）の上に重ね、選択の色の下に置く。
	StyleIncSearch = "incsearch"
	// StyleYank は、ヤンクした範囲に一時的に付ける色（Frame.Highlights。FR-140）である。
	// nvim の vim.hl.on_yank が既定で使う IncSearch に当たる。検索の一致の色（StyleSearch）の上に重ね、選択の色の下に置く。
	// 選択と重なったところで選択の色が上に見えるよう、既定と同梱の配色では反転でなく背景の色で付ける。
	StyleYank = "yank"
	// StyleMessageInfo、StyleMessageWarning、StyleMessageError は、コマンドラインの行に出すメッセージである。
	StyleMessageInfo    = "message.info"
	StyleMessageWarning = "message.warning"
	StyleMessageError   = "message.error"
	// StyleDiagnosticError などは、診断の範囲である。名前は "diagnostic." と、診断の重要度（基本設計 6.7）をつないだもの。
	StyleDiagnosticError   = "diagnostic.error"
	StyleDiagnosticWarning = "diagnostic.warning"
	StyleDiagnosticInfo    = "diagnostic.info"
	StyleDiagnosticHint    = "diagnostic.hint"
	// StylePopup は、カーソルの近くに浮かべる popup/show の本文と、補完の候補の一覧（Frame.Completion）の項目である（Vim の Pmenu に当たる）。
	StylePopup = "popup"
	// StylePopupSelected は、浮かべる一覧（Frame.List）と補完の候補の一覧（Frame.Completion）の選んでいる項目である（Vim の PmenuSel に当たる）。
	StylePopupSelected = "popup.selected"
	// StylePopupMatch は、補完の候補の一覧の項目の、打った語に当たる先頭の文字である（Neovim 0.12.5 の PmenuMatch に当たる）。
	// StylePopup か StylePopupSelected の上に重ねる（Attr.Over）ので、飾りだけを変えることもできる。
	StylePopupMatch = "popup.match"
	// StylePopupScrollbar と StylePopupThumb は、補完の候補の一覧のスクロールバーの溝とつまみである（Vim の PmenuSbar と PmenuThumb）。
	StylePopupScrollbar = "popup.scrollbar"
	StylePopupThumb     = "popup.thumb"
	// StyleCommandLinePopup は、コマンドラインと検索の入力を画面の上のポップアップに出すとき（Frame.CommandLinePopup。FR-215）の、
	// 入力の枠の中と、その下の補完の候補の一覧の項目である（noice.nvim の NoiceCmdlinePopup。既定のリンク先の Normal と同じく、
	// 既定の対応には無い）。一覧の選んでいる項目と打った語に当たる文字は、下の端の一覧と同じ StylePopupSelected と StylePopupMatch で描く。
	StyleCommandLinePopup = "commandLine.popup"
	// StyleCommandLinePopupBorder は、コマンドライン（:）の入力の枠と、その下の補完の候補の一覧の枠の、枠線と題である（FR-216。
	// noice.nvim の NoiceCmdlinePopupBorder と NoiceCmdlinePopupTitle。既定のリンク先の DiagnosticSignInfo に合わせ、
	// sign.diagnostic.info と同じ色にした）。
	StyleCommandLinePopupBorder = "commandLine.popup.border"
	// StyleCommandLinePopupBorderSearch は、検索（/ と ?）の入力の枠の、枠線と題である（FR-216。noice.nvim の
	// NoiceCmdlinePopupBorderSearch。既定のリンク先の DiagnosticSignWarn に合わせ、sign.diagnostic.warning と同じ色にした）。
	StyleCommandLinePopupBorderSearch = "commandLine.popup.border.search"
	// StyleStatusLineNC は、出力の欄の下の行（カーソルの無い窓のステータス行。Vim の StatusLineNC）である。
	StyleStatusLineNC = "statuslinenc"
	// StyleSign... は、印の列の印である。名前は "sign." と印の種類をつないだもの。診断の印は "sign.diagnostic." と重要度をつなぐ。
	StyleSignBreakpoint           = "sign.breakpoint"
	StyleSignBreakpointUnverified = "sign.breakpointUnverified"
	StyleSignExecutionPoint       = "sign.executionPoint"
	StyleSignDiagnosticError      = "sign.diagnostic.error"
	StyleSignDiagnosticWarning    = "sign.diagnostic.warning"
	StyleSignDiagnosticInfo       = "sign.diagnostic.info"
	StyleSignDiagnosticHint       = "sign.diagnostic.hint"
	// StylePopupBorder は、浮かべる窓（popup/show の本文と Frame.List）の枠線である（記号 SymbolPopupBorder を設定したときだけ描く）。
	// 表に無ければ StylePopup で描く。
	StylePopupBorder = "popup.border"
	// StyleFinderMatch は、ファインダー（FR-106）の一覧の、入力に一致した文字である（fzf の hl に当たる）。
	// 拡張機能が一覧の項目の highlights でこの名前を指す。
	StyleFinderMatch = "finder.match"
	// StylePreviewLine は、項目のプレビュー（Float.Preview。FR-149）の、項目の指す行（grep の一致した行など）である。
	// 行の全体に付け、構文の色はその上に重ねる（fzf-lua のプレビューの FzfLuaCursorLine に当たる）。
	StylePreviewLine = "preview.line"
	// StyleProgress は、画面の右下に重ねる進み具合（Frame.Progress。FR-178）の本文と百分率である。
	// fidget.nvim は本文にハイライトのグループを付けない（Normal で出る）ので、既定の対応には無い（端末の文字の色で出る）。
	StyleProgress = "progress"
	// StyleProgressTitle は、続いている進み具合の表題と記号（progress.running）である（fidget.nvim の progress_style の既定の WarningMsg）。
	StyleProgressTitle = "progress.title"
	// StyleProgressDone は、終わった進み具合の表題と記号（progress.done）である（fidget.nvim の done_style の既定の Constant）。
	StyleProgressDone = "progress.done"
	// StyleProgressFailed は、失敗して終わった進み具合（progress/report の end の failed）の表題と記号（progress.failed）である。
	// fidget.nvim の end には成否の区別が無いので、対応するグループは無い。
	StyleProgressFailed = "progress.failed"
	// StyleScrollbar は、バッファの表示領域の右端のスクロールバー（Frame.Scrollbar。FR-186）のつまみである（nvim-scrollview の ScrollView）。
	// つまみのセルの文字と色の上に重ねるので、背景の色だけを持たせると下の文字が残る。
	StyleScrollbar = "scrollbar"
	// StyleScrollbarDiagnosticError などは、スクロールバーの印（FR-187）である。名前は "scrollbar." と印の種類をつないだもの。
	// 診断の印は "scrollbar.diagnostic." と重要度をつなぐ（nvim-scrollview の ScrollViewDiagnosticsError など）。
	// StyleScrollbarSearch は、検索の一致の印である（nvim-scrollview の ScrollViewSearch）。つまみの上の印は、StyleScrollbar の上に重ねる。
	StyleScrollbarDiagnosticError   = "scrollbar.diagnostic.error"
	StyleScrollbarDiagnosticWarning = "scrollbar.diagnostic.warning"
	StyleScrollbarDiagnosticInfo    = "scrollbar.diagnostic.info"
	StyleScrollbarDiagnosticHint    = "scrollbar.diagnostic.hint"
	StyleScrollbarSearch            = "scrollbar.search"
	// StyleBand は、表示領域の上の端の帯（Frame.Band。FR-237）の本文の列である（nvim-treesitter-context の TreesitterContext）。
	// 帯の行の範囲の色（拡張機能が送った色の名前）は、この上に重ねる。既定の対応には無く、書いていなければ浮かべた窓の
	// StylePopup の色を引き継ぐ（styleLinks。TreesitterContext は浮かべた窓の NormalFloat へのリンク）。
	StyleBand = "band"
	// StyleBandLineNr は、帯の行の、印の列と行番号の列である（nvim-treesitter-context の TreesitterContextLineNumber）。
	// 既定の対応には無く、書いていなければ StyleLineNr の色を引き継ぐ（styleLinks。TreesitterContextLineNumber は LineNr へのリンク）。
	StyleBandLineNr = "band.linenr"
	// StyleBandBottom は、帯の最後の行に、列の全体に重ねる色である（nvim-treesitter-context の TreesitterContextBottom）。
	// 帯と本文の境目に下線を引くときに使う。既定の対応には無い（TreesitterContextBottom の既定のリンク先は NONE）。
	StyleBandBottom = "band.bottom"
	// StyleInlayHint は、LSP の拡張機能が仮想テキスト（virtualText/set）で出すインレイヒントの文字列である（FR-258。Neovim の LspInlayHint）。
	// 既定の対応には無く、書いていなければ StyleNonText の色を引き継ぐ（styleLinks。Neovim 0.12.5 の LspInlayHint は NonText へのリンク）。
	StyleInlayHint = "inlayHint"
	// StyleChunk は、tree-sitter の拡張機能が、カーソルを含む構文のまとまりの範囲を示す罫線（FR-265。仮想テキストの style）の色である
	// （hlchunk.nvim の HLChunk1）。StyleChunkError は、構文の誤りを含むまとまりの罫線の色である（hlchunk.nvim の HLChunk2）。
	StyleChunk      = "chunk"
	StyleChunkError = "chunk.error"
	// StyleStatusLineMode などは、ステータス行の部品ごとの色である。名前は "statusline." と部品の ID（StatusPart...）をつないだもの。
	// 部品は StyleStatusLine の上に重ねて描く（Attr.Over）ので、文字の色だけを変えることもできる。既定の対応には無い。
	StyleStatusLineMode      = "statusline.mode"
	StyleStatusLineName      = "statusline.name"
	StyleStatusLineModified  = "statusline.modified"
	StyleStatusLineReadOnly  = "statusline.readonly"
	StyleStatusLineFileType  = "statusline.filetype"
	StyleStatusLineRecording = "statusline.recording"
	StyleStatusLineRuler     = "statusline.ruler"
	StyleStatusLinePosition  = "statusline.position"
	// StyleStatusLineExt は、拡張機能が足した部品（Frame.ExtParts）の色である。部品の style（ExtPart.Style）は、この上に重ねる。
	StyleStatusLineExt = "statusline.ext"
	// StyleStatusLineModeNormal などは、モードごとのモードの部品の色である。StyleStatusLineMode の上に重ねる。
	// ビジュアルモードは文字単位と行単位で同じ名前を使う。
	StyleStatusLineModeNormal      = "statusline.mode.normal"
	StyleStatusLineModeInsert      = "statusline.mode.insert"
	StyleStatusLineModeVisual      = "statusline.mode.visual"
	StyleStatusLineModeCommandLine = "statusline.mode.commandline"
	// StyleTabLine は、タブの行の、カーソルの無いタブの見出しである（FR-62。Vim の TabLine）。
	StyleTabLine = "tabline"
	// StyleTabLineSelected は、タブの行の、カーソルのあるタブの見出しである（Vim の TabLineSel）。
	StyleTabLineSelected = "tabline.selected"
	// StyleTabLineFill は、タブの行の、見出しの右の空いた列である（Vim の TabLineFill）。
	StyleTabLineFill = "tabline.fill"
	// StyleViewChild は、切り替えの部品（FR-269）の見出しの行の、選んでいない子の名前である（nvim-dap-view の Tab）。
	// 表に無ければ StyleTabLine を引き継ぐ。
	StyleViewChild = "view.child"
	// StyleViewChildSelected は、切り替えの部品の見出しの行の、選んでいる子の名前である（nvim-dap-view の TabSelected）。
	// 表に無ければ StyleTabLineSelected を引き継ぐ。
	StyleViewChildSelected = "view.child.selected"
)

// styleNames は、本体が色を付ける対象の名前の全部である（StyleNames）。
var styleNames = []string{
	StyleComment, StyleKeyword, StyleString, StyleNumber, StyleConstant, StyleFunction, StyleType, StyleVariable,
	StyleOperator, StylePunctuation, StyleTag, StyleAttribute,
	StyleVisual, StyleStatusLine, StyleStatusLineNC, StyleNonText, StyleSpecialKey, StyleWhitespace, StyleLineNr, StyleSearch, StyleIncSearch, StyleYank,
	StyleMessageInfo, StyleMessageWarning, StyleMessageError,
	StyleDiagnosticError, StyleDiagnosticWarning, StyleDiagnosticInfo, StyleDiagnosticHint,
	StylePopup, StylePopupSelected, StylePopupBorder, StylePopupMatch, StylePopupScrollbar, StylePopupThumb, StyleFinderMatch, StylePreviewLine,
	StyleCommandLinePopup, StyleCommandLinePopupBorder, StyleCommandLinePopupBorderSearch,
	StyleProgress, StyleProgressTitle, StyleProgressDone, StyleProgressFailed,
	StyleScrollbar, StyleScrollbarDiagnosticError, StyleScrollbarDiagnosticWarning, StyleScrollbarDiagnosticInfo, StyleScrollbarDiagnosticHint,
	StyleScrollbarSearch,
	StyleBand, StyleBandLineNr, StyleBandBottom,
	StyleInlayHint,
	StyleChunk, StyleChunkError,
	StyleSignBreakpoint, StyleSignBreakpointUnverified, StyleSignExecutionPoint,
	StyleSignDiagnosticError, StyleSignDiagnosticWarning, StyleSignDiagnosticInfo, StyleSignDiagnosticHint,
	StyleStatusLineMode, StyleStatusLineName, StyleStatusLineModified, StyleStatusLineReadOnly, StyleStatusLineFileType,
	StyleStatusLineRecording, StyleStatusLineRuler, StyleStatusLinePosition, StyleStatusLineExt,
	StyleStatusLineModeNormal, StyleStatusLineModeInsert, StyleStatusLineModeVisual, StyleStatusLineModeCommandLine,
	StyleTabLine, StyleTabLineSelected, StyleTabLineFill,
	StyleViewChild, StyleViewChildSelected,
	StyleDiffAdd, StyleDiffChange, StyleDiffDelete, StyleDiffText,
}

// StyleNames は、本体が色を付ける対象の名前（設定の colors に書ける名前。FR-91）を、定義の順に返す。
// 拡張機能の decoration/set の style も、この名前から選ぶ（基本設計 6.1、7.1）。返す列は呼び出しごとに作り直す。
func StyleNames() []string {
	return slices.Clone(styleNames)
}

// IsStyleName は、name が設定の colors に書ける色の名前かを返す。本体の色を付ける対象の名前（StyleNames）と、
// スクロールバーの印の種類の名前（IsScrollbarMarkName。後に拡張機能が足す種類の色を、拡張機能が起動する前の設定に書けるように、
// 決まった表で確かめない）である。
//
// req: FR-187
func IsStyleName(name string) bool {
	return slices.Contains(styleNames, name) || IsScrollbarMarkName(name)
}

// scrollbarMarkPrefix は、スクロールバーの印の種類の色と記号の名前の頭である（StyleScrollbarSearch など）。
const scrollbarMarkPrefix = StyleScrollbar + "."

// IsScrollbarMarkName は、name が、スクロールバーの印の種類の色と記号の名前の形（"scrollbar." の後に、空でなく、
// コンマと空白を含まない名前が続く）かを返す（FR-187）。後の名前は、設定の scrollbar.marks に書ける種類の名前と同じ規則である
// （コンマで区切り、前後の空白を除いた空でない名前）。後に拡張機能が足す種類（git など）を、名前の表に頼らずに受け付ける。
//
// ただし、"scrollbar." の後の最初の点までの部分が、本体の種類の名前（本体の色の名前の表の "scrollbar.<種類>" と
// "scrollbar.<種類>.<…>" の <種類>。diagnostic と search）と同じか、編集距離が scrollbarTypoDistance 以下で、本体の表に無い名前は
// 受け付けない（scrollbar.diagnostics.error、scrollbar.serch などの書き間違いを、設定の誤りとして知らせるため。PR #351 の再レビューの推奨 3）。
//
// req: FR-187
func IsScrollbarMarkName(name string) bool {
	rest, ok := strings.CutPrefix(name, scrollbarMarkPrefix)
	if !ok || rest == "" || strings.ContainsFunc(rest, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		return false
	}
	if slices.Contains(styleNames, name) {
		return true
	}
	kind, _, _ := strings.Cut(rest, ".")
	for _, b := range builtinScrollbarKinds() {
		if editDistance(kind, b) <= scrollbarTypoDistance {
			return false
		}
	}
	return true
}

// scrollbarTypoDistance は、IsScrollbarMarkName が本体の種類の名前の書き間違いとみなす編集距離の上限である。
// 本体の種類の名前に 1 文字か 2 文字を足した、消した、替えた名前（diagnostics は 1、隣の 2 文字を入れ替えた名前は 2）を書き間違いとし、
// git、todo、mark などの短い名前（search との距離は 4 以上）は書き間違いにしない。
const scrollbarTypoDistance = 2

// builtinScrollbarKinds は、本体の色の名前の表（styleNames）にある、スクロールバーの印の種類の名前（"scrollbar." の後の最初の点まで）を返す。
func builtinScrollbarKinds() []string {
	var out []string
	for _, n := range styleNames {
		if rest, ok := strings.CutPrefix(n, scrollbarMarkPrefix); ok {
			kind, _, _ := strings.Cut(rest, ".")
			if !slices.Contains(out, kind) {
				out = append(out, kind)
			}
		}
	}
	return out
}

// editDistance は、a と b の文字の編集距離（Levenshtein 距離）を返す。
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// Theme は、色の種類の名前から属性への対応である。
// 表に無い名前の範囲には色を付けない（基本設計 6.1）。
type Theme map[string]Attr

// WithColors は、t を写したものに、colors の名前ごとの属性を上書きした対応を返す（設定の colors。FR-91）。
// 上書きは名前ごとの置き換えで、既定の属性と混ぜない（文字の色だけを書いた名前は、既定の背景の色と飾りを持たない）。
// t は変更しない。
//
// req: FR-91
func (t Theme) WithColors(colors map[string]Attr) Theme {
	out := make(Theme, len(t)+len(colors))
	maps.Copy(out, t)
	maps.Copy(out, colors)
	return out
}

// DefaultTheme は、既定の対応（仮）を返す。色は端末の 16 色の番号で、Vim の暗い背景の既定の色に近いものを選んだ。
// 設定の colorscheme で選んだ配色（FR-125）と設定の colors（FR-91）は、この順に WithColors で名前ごとに上書きする。
func DefaultTheme() Theme {
	return Theme{
		StyleComment:  {Fg: Indexed(14)},
		StyleKeyword:  {Fg: Indexed(11)},
		StyleString:   {Fg: Indexed(13)},
		StyleNumber:   {Fg: Indexed(13)},
		StyleConstant: {Fg: Indexed(13)},
		StyleFunction: {Fg: Indexed(14)},
		StyleType:     {Fg: Indexed(10)},
		StyleOperator: {Fg: Indexed(11)},

		StyleVisual:     {Reverse: true},
		StyleStatusLine: {Reverse: true, Bold: true},
		StyleNonText:    {Fg: Indexed(12)},
		StyleSpecialKey: {Fg: Indexed(12)},
		// 空白の記号は、Neovim 0.12.5 の既定（Whitespace は NonText へのリンク）と同じく nontext と同じ色にした。
		StyleWhitespace: {Fg: Indexed(12)},
		// 行番号は、Vim 9.1 の暗い背景の既定（2026-11-20 に TERM=xterm-256color、background=dark の :hi LineNr で確かめた ctermfg=11）と同じ番号にした。
		StyleLineNr: {Fg: Indexed(11)},
		// 検索の一致は、Vim 9.1 の暗い背景の既定（2026-11-20 に同じ起動の :hi Search で確かめた ctermfg=0 ctermbg=11）と同じ番号にした。
		StyleSearch: {Fg: Indexed(0), Bg: Indexed(11)},
		// 打つたびの検索の今の一致は、Vim 9.1 の暗い背景の既定（2026-11-20 に同じ起動の :hi IncSearch で確かめた cterm=reverse）と同じ反転にした。
		StyleIncSearch: {Reverse: true},
		// ヤンクした範囲は、背景の色だけを持つ。nvim の vim.hl.on_yank の既定の IncSearch（Vim 9.1 の暗い背景の既定は cterm=reverse）に
		// すると、既定の選択（StyleVisual）も反転なので、重なったところと選択だけのところを見分けられない（PR #287 のレビューの推奨 1）。
		// 番号は、Vim 9.1 の暗い背景の MatchParen の既定（2026-11-20 に同じ起動の :hi MatchParen で確かめた ctermbg=6）を借りた。
		// 選択と重なったところは、反転でこの背景の色が文字の色になるので、選択だけのところとも、ヤンクの色だけのところとも違って見える。
		StyleYank:           {Bg: Indexed(6)},
		StyleMessageWarning: {Fg: Indexed(11)},
		StyleMessageError:   {Fg: Indexed(15), Bg: Indexed(1)},

		StyleDiagnosticError:   {Undercurl: true, Sp: Indexed(9)},
		StyleDiagnosticWarning: {Undercurl: true, Sp: Indexed(11)},
		StyleDiagnosticInfo:    {Underline: true, Sp: Indexed(12)},
		StyleDiagnosticHint:    {Underline: true, Sp: Indexed(8)},

		StyleStatusLineNC:  {Reverse: true},
		StylePopup:         {Fg: Indexed(15), Bg: Indexed(8)},
		StylePopupSelected: {Fg: Indexed(0), Bg: Indexed(14)},
		// 補完の候補の一覧の、打った語に当たる文字は、Neovim 0.12.5 の既定（PmenuMatch は cterm=bold）と同じく太字にする。
		// スクロールバーの溝は、Neovim の既定（PmenuSbar は Pmenu へのリンク）と同じく popup と同じ背景の色にし、
		// つまみは溝と見分けられる明るい灰色（Vim 9.1 の暗い背景の PmenuSbar の ctermbg=248 に近い 7 番）にした。
		StylePopupMatch:     {Bold: true},
		StylePopupScrollbar: {Bg: Indexed(8)},
		StylePopupThumb:     {Bg: Indexed(7)},
		StyleFinderMatch:    {Bold: true, Underline: true},
		// プレビューの項目の指す行は、窓の色（StylePopup の背景の 8 番）の上で見分けられ、構文の色（文字の色）を消さないよう、
		// 背景の色だけを持つ。
		StylePreviewLine: {Bg: Indexed(0)},
		// 画面の上のポップアップのコマンドラインの枠線と題は、noice.nvim の既定のリンク先（コマンドラインは DiagnosticSignInfo、
		// 検索は DiagnosticSignWarn）に当たる、本体の sign.diagnostic.info と sign.diagnostic.warning と同じ番号にした（FR-216）。
		StyleCommandLinePopupBorder:       {Fg: Indexed(12)},
		StyleCommandLinePopupBorderSearch: {Fg: Indexed(11)},
		// 進み具合の表題は、fidget.nvim の既定（続いている間は WarningMsg、終わったら Constant）に合わせ、本体の既定の
		// message.warning と constant と同じ番号にした。
		StyleProgressTitle: {Fg: Indexed(11)},
		StyleProgressDone:  {Fg: Indexed(13)},
		// 失敗して終わった進み具合は、印の列の誤りの診断の印（sign.diagnostic.error）と同じ赤にした。fidget.nvim に対応するものは無い。
		StyleProgressFailed: {Fg: Indexed(9)},
		// スクロールバーのつまみは、反転だけを持つ（下の文字を残し、その文字と背景の色を入れ替える）。nvim-scrollview の既定の ScrollView は
		// Visual へのリンクで、本体の既定の visual も反転なので、それに合わせた。端末の背景が暗くても明るくても、反転した文字は端末の文字の色と
		// 背景の色の組で読めるので、背景の明暗を前提にしない（PR #351 の再レビューの推奨 2）。winblend の透かしは端末のセルでは出せないので、
		// 主張を抑えた色にしたいときは、配色か colors で背景の色を書く（同梱の iceberg は Visual の背景）。
		// #351 の途中では背景の色を固定していたが、16 色の 8 番（xterm の既定の色で #7f7f7f）は 9 番の赤との比が 1.00、256 色の 236 番（#303030）は
		// 明るい背景の端末で下の黒の文字との比が 1.59 で、どちらかの端末で読めなかった（WCAG の相対輝度で計算した）。
		StyleScrollbar: {Reverse: true},
		// スクロールバーの診断の印は、印の列の診断の印（sign.diagnostic.*）と同じ番号にした（nvim-scrollview の既定も、印の列の
		// DiagnosticSign* の色へのリンク）。つまみの上の印は、反転で、印の色の地に端末の背景の色の記号として出るので、印と端末の背景の色の比は
		// つまみの外と同じで、印の列の印とも同じである。
		// 検索の一致の印は、nvim-scrollview の既定の ScrollViewSearch（NonText へのリンク）の nontext の 12 番だと情報の診断の印と同じ色になるので、
		// 診断の印と重ならない 14 番（シアン）にした。
		StyleScrollbarDiagnosticError:   {Fg: Indexed(9)},
		StyleScrollbarDiagnosticWarning: {Fg: Indexed(11)},
		StyleScrollbarDiagnosticInfo:    {Fg: Indexed(12)},
		StyleScrollbarDiagnosticHint:    {Fg: Indexed(8)},
		StyleScrollbarSearch:            {Fg: Indexed(14)},
		StyleSignBreakpoint:             {Fg: Indexed(9), Bold: true},
		StyleSignBreakpointUnverified:   {Fg: Indexed(8)},
		StyleSignExecutionPoint:         {Fg: Indexed(11), Bold: true},
		StyleSignDiagnosticError:        {Fg: Indexed(9)},
		StyleSignDiagnosticWarning:      {Fg: Indexed(11)},
		StyleSignDiagnosticInfo:         {Fg: Indexed(12)},
		StyleSignDiagnosticHint:         {Fg: Indexed(8)},
		// タブの行は Vim 9.1 の暗い背景の既定（TabLine は下線、TabLineSel は太字、TabLineFill は反転）に近づけた。
		StyleTabLine:         {Underline: true, Fg: Indexed(15), Bg: Indexed(8)},
		StyleTabLineSelected: {Bold: true},
		StyleTabLineFill:     {Reverse: true},
		// 差分の色は、Vim 9.1 の暗い背景の既定と同じ番号にした（2026-11-19 に TERM=xterm-256color、background=dark の :hi で確かめた。
		// DiffAdd は ctermbg=4、DiffChange は ctermbg=5、DiffDelete は ctermfg=12 ctermbg=6、DiffText は cterm=bold ctermbg=9）。
		StyleDiffAdd:    {Bg: Indexed(4)},
		StyleDiffChange: {Bg: Indexed(5)},
		StyleDiffDelete: {Fg: Indexed(12), Bg: Indexed(6)},
		StyleDiffText:   {Bg: Indexed(9), Bold: true},

		// ブロックの範囲の罫線は、hlchunk.nvim（コミット 4ddc056e4ca45f1c7dc6aa21935df710b263b1aa）の chunk の style の既定
		// （{ fg = "#806d9c" }、{ fg = "#c21f30" }）と同じ 24 ビットの色にした。hlchunk.nvim は配色によらずこの色で描く。
		StyleChunk:      {Fg: RGB(0x80, 0x6d, 0x9c)},
		StyleChunkError: {Fg: RGB(0xc2, 0x1f, 0x30)},
	}
}

// styleLinks は、表に無ければ別の名前の属性を引き継ぐ名前と、その引き継ぐ先の名前である（Neovim の :hi default link に当たる）。
// 既定の対応、配色（FR-125）、設定の colors（FR-91）のどれにも書いていなければ、引き継ぐ先の今の属性を使う。
// 配色が引き継ぐ先の色だけを変えても、引き継ぐ名前の色が付いてくる。
//
//   - StyleBand は StylePopup（nvim-treesitter-context の TreesitterContext は浮かべた窓の NormalFloat へのリンクで、
//     本体の浮かべた窓（ホバーなど）の色の名前は StylePopup である）
//   - StyleBandLineNr は StyleLineNr（TreesitterContextLineNumber は LineNr へのリンク）
//   - StyleInlayHint は StyleNonText（Neovim 0.12.5 の LspInlayHint は NonText へのリンク。2026-11-22 に nvim --clean の :hi LspInlayHint で確かめた）
//   - StyleViewChild は StyleTabLine、StyleViewChildSelected は StyleTabLineSelected（切り替えの部品の見出しの行は、
//     タブの行の見出しの色を引き継ぐ。基本設計 1.4。nvim-dap-view の見出しの行も Tab と TabSelected を使う）
//   - StyleTag は StyleKeyword、StyleAttribute は StyleType（Vim 9.1 の syntax/html.vim のリンク。htmlTagName は htmlStatement を経て
//     Statement、htmlArg は Type。FR-331）。#457 より前に書いた利用者の配色は tag と attribute を持たないので、
//     既定の対応に固定の色を置くと、その配色の中でこの 2 つだけが配色の外の色になる（PR #458 のレビューの推奨 1）
//
// req: FR-258, FR-331
var styleLinks = map[string]string{
	StyleTag:               StyleKeyword,
	StyleAttribute:         StyleType,
	StyleBand:              StylePopup,
	StyleBandLineNr:        StyleLineNr,
	StyleInlayHint:         StyleNonText,
	StyleViewChild:         StyleTabLine,
	StyleViewChildSelected: StyleTabLineSelected,
}

// attr は、名前 name の属性を返す。表に無い名前なら、引き継ぐ先（styleLinks）があればその属性を、無ければゼロ値（飾りなし）を返す。
func (t Theme) attr(name string) Attr {
	if a, ok := t[name]; ok {
		return a
	}
	if to, ok := styleLinks[name]; ok {
		return t[to]
	}
	return Attr{}
}
