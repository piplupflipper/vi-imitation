package ui

// 窓の重なり（1 つの画面の中で、後に描くものが上に重なる順）。render が上から順に描く。
// 部品を足すときは、この表に行を足して、上下の部品との順をここで決め、render の同じ位置で描く。
// バッファの表示領域に重ねる部品は、表示領域のグリッドの中の位置（textArea）を引数で受け取り、0 行目から始まることや、
// 右の端がグリッドの右の端であることを前提にしない（表示領域の上に帯を足したときに、部品を直さずに済むようにする。PR #345 のレビューの推奨 3）。
//
//	順   部品                                                         描く所
//	1    バッファの行（本文、印の列、行番号の列、見比べの詰め物、"~" の行）  render の行の繰り返し（drawGutter、drawLine、drawFiller）
//	2    行を折り返すときの表示領域の端の印（"<<<"、"@@@"）               drawer.drawMarks
//	2.5  起動の案内（Frame.Intro。FR-205、FR-207）                         drawIntro
//	2.7  表示領域の上の端の帯（Frame.Band。FR-237、FR-239）                 drawBand
//	3    右端のスクロールバーと印（Frame.Scrollbar。FR-186、FR-187）       drawScrollbar
//	4    拡張機能の進み具合（Frame.Progress。FR-178）                      drawProgress
//	5    浮かべる窓（Frame.List、Frame.Float とそのプレビュー）             drawList、drawFloat
//	6    ステータス行                                                   drawStatus
//	7    出力の欄                                                       drawOutput
//	8    コマンドラインの行（メッセージ、上へ広げた複数の行のメッセージ、選択肢の問い） drawCommandLine
//	8.1  補完の候補の一覧（Frame.Completion。FR-197。コマンドラインの行の真上）   drawCommandLine の中の drawCompletion
//	8.2  画面の上のポップアップのコマンドラインの枠（Frame.CommandLinePopup。FR-215、FR-216） drawCommandLine の中の drawCommandLinePopup
//	8.3  その下の補完の候補の一覧の枠（FR-217）                                 drawCommandLinePopup の中の drawPaletteMenu
//	9    タブの行（画面の一番上の 1 行。ほかの部品を 1 行下へずらす）        renderWithTabLine
//
// 画面の上のポップアップのコマンドライン（8.2、8.3）は、最後の行の代わりに描くので、8.1 と同じ画面には出ない。
// どの窓よりも上に重ねるのは、noice.nvim の cmdline_popup と cmdline_popupmenu の zindex（200）が、浮かべた窓の既定（50）と
// fidget.nvim（45）と nvim-scrollview（40）より上にあるのと同じ重なりにするためである。ステータス行と出力の欄の上にも重なる。
//
// 行を足すときは、今の行の番号を振り直さず、間に小数の番号（2.5、8.1）で入れる。
// ほかの文書とコメントは、この表の行を番号でなく部品の名前と描く関数（drawCompletion など）で指す（PR #358 のレビューの推奨 6）。
//
// 起動の案内をバッファの行のすぐ上に置くのは、Neovim 0.12.5 が案内を窓の格子（"~" の行）に直に描き（update_screen の intro_message）、
// 浮かべた窓（nvim-scrollview、fidget.nvim、ホバー）とメッセージはその上に重なるのと同じにするためである。
// 案内は表示領域の中に描くので、ステータス行とコマンドラインの行は覆わない（Neovim も 'laststatus' が 2 ならステータス行を覆わない）。
// 進み具合を浮かべる窓より下に置くのは、fidget.nvim の窓の zindex の既定（45）が、Neovim の浮かべた窓の既定（50）より下にあるのと
// 同じ重なりにするためである。複数の行に広がったメッセージ（コマンドラインの行）も、進み具合の上に出る。
// スクロールバーを進み具合より下に置くのも、nvim-scrollview の窓の zindex の既定（40）が fidget.nvim の 45 より下にあるのと同じ重なりにするためである。
// 進み具合は右の端の 1 列を空けて描く（progressPadding）ので、スクロールバーの列とは重ならない。
// 折り返しの "@@@"（2）は最後の行の右の 3 列に描くので、スクロールバーがその右の 1 列に重なる（nvim-scrollview も、浮かべた窓として本文の上に描く）。
//
// 上の端の帯（drawBand）は、nvim-treesitter-context（オーナーの nvim のコミット 8decd73460d26ef0a0befb745d022dd1fc2c38b8）の窓の
// zindex の既定（20）に合わせ、本文と起動の案内の上、スクロールバー（nvim-scrollview の 40）、進み具合（fidget.nvim の 45）、
// 浮かべた窓（50）、画面の上のポップアップのコマンドライン（noice.nvim の 200）の下に置く。
// 起動の案内は Neovim が窓の格子に直に描くので、浮かべた窓である帯はその上に重なる。
// スクロールバーは帯の行の右の端の列に重なり、帯の上でもつまみと印を押せる（HitTest がスクロールバーのセルを帯より先に引く）。
// 進み具合は表示領域の最後の行から上へ積むので、表示領域の低い画面でだけ帯と重なり、そのときは進み具合が上に出る。
// 画面の上のポップアップのコマンドラインの枠（8.2）は画面の上から 3 行目から下に置くので、帯の行と重なれば枠が上に出る。

// textArea は、バッファの表示領域の、グリッドの中の位置である（窓の重なりの表の doc）。
type textArea struct {
	// top は、表示領域の最初の行のグリッドの行の番号である。
	top int
	// rows は、表示領域の行数である。
	rows int
	// width は、表示領域の右の端の次の列（グリッドの左の端から数えた列数）である。
	width int
}

// バッファの行の文字に重ねる色の順（span.order）。大きい方を上に重ねる（sweep.attr）。
// 色の種類を足すときは、この表に行を足し、上下の種類との順をここで決める。
//
//	順                          色の種類                                    入れる所
//	diffLineOrder（-2）         見比べの行の色（diff.add など）               drawer.diffSpans
//	diffTextOrder（-1）         見比べの違う文字の色（diff.text）              drawer.diffSpans
//	0 以上 glyphOrder 未満       構文の色（decoration/set の一覧での位置）       drawer.prepare（Decorations）
//	glyphOrder                  文字の表記の色（specialkey、whitespace、行末の記号の nontext） drawer.look と drawer.eolCell（attrAt の mark）
//	diagnosticOrder + i         診断（i は Frame.Diagnostics での位置）         drawer.collectSpans
//	searchOrder                 検索の一致（search。FR-137）                  drawer.searchSpans
//	highlightOrder + i          範囲の色（i は Frame.Highlights での位置。FR-140、FR-153） drawer.collectSpans
//	（span の外）               選択（visual）                               drawer.attrAt が span の後に重ねる
//
// 見比べの色は構文の色より下に置き、構文の文字の色が差分の背景の色の上に出るようにする。
// 文字の表記の色（^A などの表記の specialkey、空白の記号の whitespace、行末の記号の nontext。FR-170）は構文の色の上、診断の下に置く。
// Vim 9.1 と Neovim 0.12.5 は、コメントと文字列の中の空白の記号と ^A を、構文の色でなく SpecialKey と Whitespace の色で描く
// （2026-11-21 に、Go のファイルの行末の空白、生の文字列とブロックコメントの中の行頭の空白とタブ文字、C のコメントの中の ^A を
// tmux で確かめた。PR #334 のレビューの必須 1）。表記の色は文字ごとに決まるので span にせず、attrAt が mark として受け取り、この順に重ねる。
// 一致の色は診断の上、選択の下に置く（#275 の本文）。
// 範囲の色（ヤンクした範囲の一時的な色）は一致の色の上、選択の下に置く（#283 の本文）。ヤンクした直後に、一致の色の中でもヤンクした範囲が見えるようにする。
// 打つたびの検索の今の一致（#296）は、検索のためだけの順を作らず、internal/editor が範囲の色の一覧の最後に足す。
// このため一致の色（search）とヤンクした範囲の色の上、選択の下になる。nvim 0.12.5 も、打っている間の今の一致には、ほかの一致の Search の上に IncSearch を出す。
// 選択だけは span にせず、カーソルの文字を除いて attrAt で最後に重ねる。
// カーソルの文字には、選択の色を重ねない（端末のカーソルが反転で描かれても文字が読めるように。Vim と同じ）。
const (
	diffLineOrder   = -2
	diffTextOrder   = -1
	glyphOrder      = diagnosticOrder - 1
	diagnosticOrder = 1 << 40
	searchOrder     = 1 << 41
	highlightOrder  = 1 << 42
)
