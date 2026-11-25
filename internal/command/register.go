package command

import (
	"fmt"
	"slices"
	"strings"

	"github.com/piplupflipper/vi-imitation/internal/editcmd"
)

// Register は、レジスター 1 つの内容である（FR-04）。
//
// Linewise が true なら行単位のテキストで、Text は行を "\n" でつないだもの（最後の行の後の改行は含めない）である。
// Vim の getreg() が返す行単位のテキストは最後に改行が付くが、ここでは付けない。
// false なら文字単位のテキストで、Text の "\n" は行の区切りである。
//
// Vim と同じく、「空のテキストが入っている」と「何も入っていない」を区別する。
// 空の行の C（c$）や yl は、空の文字単位のテキストを書き込む（Vim の getregtype() は v を返す）。
// 何も入っていないのはゼロ値だけで、Registers が書き込んだ値（Set で書いたものを含む）は、テキストが空でも入っているものとして扱う。
type Register struct {
	Text     string
	Linewise bool

	// keys は、q で記録したキーの列である。Text からキーへ戻すと失われるキー（矢印キーなど）を保つ。
	// 記録したもの以外では nil である。
	keys []editcmd.Key
	// filled は、Registers が書き込んだ値であることを表す。空のテキストが入っているレジスターを、空のレジスターと区別する。
	filled bool
}

// empty は、何も入っていない（一度も書き込んでいない）かを返す。空のテキストが入っていれば false である。
func (r Register) empty() bool { return !r.filled && r.Text == "" && !r.Linewise && len(r.keys) == 0 }

// appendTo は、r の後に add を追記した内容を返す（"A のような大文字の名前への書き込み）。
// どちらかが行単位なら、結果は行単位になり、間に改行を入れる（Vim と同じ）。
func (r Register) appendTo(add Register) Register {
	if r.empty() {
		return add
	}
	out := Register{Linewise: r.Linewise || add.Linewise, filled: true}
	sep := ""
	if out.Linewise {
		sep = "\n"
	}
	out.Text = r.Text + sep + add.Text
	if r.keys != nil && add.keys != nil {
		out.keys = append(slices.Clone(r.keys), add.keys...)
	}
	return out
}

// 履歴の設定の既定値である（基本設計 7 章の registers）。
const (
	// DefaultHistorySize は、履歴の件数の既定値である（FR-53）。
	DefaultHistorySize = 100
	// DefaultMaxEntryBytes は、履歴に積む 1 件の最大のバイト数の既定値（1 MiB）である（FR-53）。
	DefaultMaxEntryBytes = 1 << 20
	// MaxHistorySize は、履歴の件数の上限に設定できる最大の値である（基本設計 7 章）。
	MaxHistorySize = 10000
)

// RegisterConfig は、レジスターの履歴の設定である（基本設計 7 章の registers。FR-52、FR-53）。
// ゼロ値は、すべての項目が既定値の設定である。
type RegisterConfig struct {
	// HistorySize は、履歴の件数の上限（historySize）である。0 以下なら DefaultHistorySize、MaxHistorySize を超えたら MaxHistorySize を使う。
	HistorySize int
	// MaxEntryBytes は、履歴に積む 1 件の最大のバイト数（maxEntryBytes）である。0 以下なら DefaultMaxEntryBytes を使う。
	MaxEntryBytes int
	// DeleteToHistory は、名前を省略した削除と変更を履歴に積むか（deleteToHistory）である。nil なら既定の true である。
	DeleteToHistory *bool
}

// historySize は、既定値を補った履歴の件数の上限を返す。
func (c RegisterConfig) historySize() int {
	if c.HistorySize <= 0 {
		return DefaultHistorySize
	}
	return min(c.HistorySize, MaxHistorySize)
}

// maxEntryBytes は、既定値を補った 1 件の最大のバイト数を返す。
func (c RegisterConfig) maxEntryBytes() int {
	if c.MaxEntryBytes <= 0 {
		return DefaultMaxEntryBytes
	}
	return c.MaxEntryBytes
}

// deleteToHistory は、既定値を補った deleteToHistory を返す。
func (c RegisterConfig) deleteToHistory() bool {
	return c.DeleteToHistory == nil || *c.DeleteToHistory
}

// Registers は、レジスターの集合である（FR-04、FR-50、FR-51）。ゼロ値は、全て空で、設定が既定値のレジスターである。
//
// 名前は次の 3 種類だけで、それ以外の名前（Vim の 0〜9、-、"、.、: など）は読み書きできない（FR-50）。
//
//	a〜z   名前付きのレジスター。A〜Z で書き込むと追記する
//	_      書き込んでも何も残らず、読むと空である
//	0      名前の省略（無名のレジスター）。別の置き場を持たず、履歴の先頭を読む
//
// 履歴は 1 本で、新しいものを先頭に置く（FR-51）。どの操作が何を変えるかは基本設計 4.5 の表のとおりである。
// 件数の上限を超えたら最も古いものから捨てる。MaxEntryBytes を超えたテキストは履歴に積まず、
// 「上限を超えた無名のレジスター」として 1 つだけ持つ。その間は、無名のレジスターはそれを読む。次に履歴に積むと捨てる（FR-53）。
//
// 履歴のスライスは古い順に並べ、積むときは末尾に足し、上限を超えたら先頭を捨てる（積むたびに全体を写さない）。
// 積むとき、置き換えるとき、Promote はその場で配列を書き換えるので、TakeOver は履歴の配列を複製して State の間で共有しない。
// 切り替えの前の State にも、保留した拡張機能の結果（ApplyDeferred）が書き込みうるためである。
// Registers を値で写すときは、同じ理由で history を複製する。
//
// req: FR-04
type Registers struct {
	named [26]Register
	// history は、履歴を古い順に並べたものである（最後が先頭、つまり最も新しい）。
	history []Register
	// oversize は、上限を超えた無名のレジスターである。無ければ空（empty）である。
	oversize Register
	cfg      RegisterConfig
	// pushes は、履歴の先頭が変わった回数である。履歴の先頭が変わったかを、前に判定したときの値との比較で判定する。
	pushes uint64
}

// SetConfig は、履歴の設定を c にする。件数の上限が今の件数より少なければ、古いものを捨てる。
func (r *Registers) SetConfig(c RegisterConfig) {
	r.cfg = c
	if n := c.historySize(); len(r.history) > n {
		r.history = slices.Clone(r.history[len(r.history)-n:])
	}
}

// Config は、履歴の設定を返す。
func (r *Registers) Config() RegisterConfig { return r.cfg }

// validRegister は、name がレジスターの名前（a〜z、A〜Z、_、名前の省略の 0）であるかを返す（FR-50）。
func validRegister(name rune) bool {
	return name == 0 || name == '_' || name >= 'a' && name <= 'z' || name >= 'A' && name <= 'Z'
}

// InvalidRegisterError は、レジスターの名前に使えない文字を指定した操作の失敗である（FR-50）。
// ErrFailed を包まないので、呼び出し側（internal/editor）はメッセージの行に出す。
type InvalidRegisterError struct {
	// Name は、指定した名前である。
	Name rune
}

func (e *InvalidRegisterError) Error() string {
	return fmt.Sprintf("E354: レジスターの名前に使えない文字です: '%c'", e.Name)
}

// checkRegister は、name がレジスターの名前でなければ *InvalidRegisterError を返す（FR-50）。
//
// req: FR-50
func checkRegister(name rune) error {
	if validRegister(name) {
		return nil
	}
	return &InvalidRegisterError{Name: name}
}

// CheckRegister は、name がレジスターの名前（a〜z、A〜Z、_、名前の省略の 0）でなければ *InvalidRegisterError（E354）を返す（FR-50）。
// 拡張機能のポップアップのキーに付けた名前（"ac。FR-315）を、view/key で知らせる前に確かめるのに使う（internal/editor）。
func CheckRegister(name rune) error { return checkRegister(name) }

// Get は、名前が name のレジスターの内容を返す。name が 0 なら無名のレジスター（上限を超えた無名のレジスターがあればそれ、
// 無ければ履歴の先頭）である。_ は空を返す。名前に使えない文字なら false を返す（FR-50）。
//
// req: FR-50, FR-51
func (r *Registers) Get(name rune) (Register, bool) {
	switch {
	case name == 0:
		if !r.oversize.empty() {
			return r.oversize, true
		}
		if n := len(r.history); n > 0 {
			return r.history[n-1], true
		}
		return Register{}, true
	case name >= 'a' && name <= 'z':
		return r.named[name-'a'], true
	case name >= 'A' && name <= 'Z':
		return r.named[name-'A'], true
	case name == '_':
		return Register{}, true
	}
	return Register{}, false
}

// History は、履歴を新しい順に並べた複製を返す（FR-51）。上限を超えた無名のレジスターは含めない。
func (r *Registers) History() []Register {
	out := slices.Clone(r.history)
	slices.Reverse(out)
	return out
}

// fromHistory は、名前 name の貼り付けが履歴の先頭を貼るかを返す。名前を省略し、上限を超えた無名のレジスターが無く、履歴が空でないときである。
func (r *Registers) fromHistory(name rune) bool {
	return name == 0 && r.oversize.empty() && len(r.history) > 0
}

// RegisterEntry は、レジスターの一覧（:registers）の 1 項目である（FR-54）。
type RegisterEntry struct {
	// Name は、名前付きのレジスターの名前（a〜z）である。履歴の項目なら 0 である。
	Name rune
	// Index は、履歴の項目の、新しいほうからの添字（0 が先頭）である。Name が 0 のときだけ意味を持つ。
	Index int
	// Register は、項目の内容である。
	Register Register
}

// List は、レジスターの一覧に出す項目を返す（FR-54。基本設計 4.5）。
// 履歴の項目を新しい順に並べ、続けて、中身のある名前付きのレジスターを a から順に並べる。
// 上限を超えた無名のレジスターは、履歴の項目でないので含めない。
//
// req: FR-54
func (r *Registers) List() []RegisterEntry {
	out := make([]RegisterEntry, 0, len(r.history))
	for i, reg := range r.History() {
		out = append(out, RegisterEntry{Index: i, Register: reg})
	}
	for i, reg := range r.named {
		if !reg.empty() {
			out = append(out, RegisterEntry{Name: rune('a' + i), Register: reg})
		}
	}
	return out
}

// Promote は、履歴の新しいほうから i 番目（0 が先頭）の項目を、履歴の先頭へ移す（基本設計 4.5 の、一覧から選んで貼った項目）。
// 項目は移すだけで、同じ項目を 2 件にしない。上限を超えた無名のレジスターは捨てる（無名のレジスターは移した項目を読む）。
// i が範囲の外なら何もせず false を返す。i が 1 以上なら履歴の先頭が変わるので、Pushes を 1 増やす。
//
// req: FR-51
func (r *Registers) Promote(i int) bool {
	n := len(r.history)
	if i < 0 || i >= n {
		return false
	}
	r.oversize = Register{}
	if i == 0 {
		return true
	}
	j := n - 1 - i
	reg := r.history[j]
	copy(r.history[j:], r.history[j+1:])
	r.history[n-1] = reg
	r.pushes++
	return true
}

// Pushes は、履歴の先頭が変わった回数（積んだ、置き換えた、Promote で移した回数）を返す。
// 呼び出し側（internal/editor）は、結果を扱い終えるたびに、前に判定したときの値と比べて、履歴の先頭が変わったかを判定する（基本設計 4.5 の FR-56）。
// 値は TakeOver で引き継ぐので、バッファの切り替えをまたいでも単調に増える。
func (r *Registers) Pushes() uint64 { return r.pushes }

// Head は、履歴の先頭（最も新しい項目）を返す。履歴が空なら false を返す。
// Get(0) と違い、上限を超えた無名のレジスターは読まない（FR-56 の送り出しは、上限を超えたテキストを送らない）。
func (r *Registers) Head() (Register, bool) {
	if n := len(r.history); n > 0 {
		return r.history[n-1], true
	}
	return Register{}, false
}

// Set は、名前を指定したヤンクと同じく、名前が name のレジスターに reg を書き込む（基本設計 4.5 の表）。
// 大文字の名前は追記する。name が 0 なら履歴に積むだけである。_ には何も書かない。
// 名前に使えない文字なら何もせず false を返す。
// 書き込んだテキストが MaxEntryBytes を超えて履歴に積まなかったことは知らせない（write の notice を捨てる）。
func (r *Registers) Set(name rune, reg Register) bool {
	if !validRegister(name) {
		return false
	}
	r.write(name, reg, false, false)
	return true
}

// Yank は、名前を指定した本体の y と同じく、名前が name のレジスターに reg を書き込む（拡張機能の register/set。FR-315）。
// 大文字の名前は追記し、name が 0 なら履歴に積むだけで、_ には何も書かない（Set と同じ。基本設計 4.5 の表）。
// 名前に使えない文字なら何も書かずに *InvalidRegisterError（E354）を返す（FR-50）。
// Set と違い、書き込んだテキストが MaxEntryBytes を超えて履歴に積まなかったら、y と同じ知らせの文言を notice に返す（FR-53）。
//
// req: FR-315
func (r *Registers) Yank(name rune, reg Register) (notice string, err error) {
	if err := checkRegister(name); err != nil {
		return "", err
	}
	_, notice = r.write(name, reg, false, false)
	return notice, nil
}

// slot は、名前付きのレジスターを指すポインターを返す。a〜z、A〜Z 以外の名前では nil を返す。
// マクロの記録（stopRecording）は、これで履歴を変えずに書き込む。
func (r *Registers) slot(name rune) *Register {
	switch {
	case name >= 'a' && name <= 'z':
		return &r.named[name-'a']
	case name >= 'A' && name <= 'Z':
		return &r.named[name-'A']
	}
	return nil
}

// write は、ヤンク（del が false）か、削除と変更（del が true）で、名前が name のレジスターに reg を書く（基本設計 4.5 の表）。
//
//   - 名前の省略: ヤンクは履歴の先頭に積む。削除と変更は、deleteToHistory が真なら積み、偽なら何もしない（FR-52）
//   - a〜z: そのレジスターに書き、書いた内容を履歴の先頭に積む（deleteToHistory によらない）
//   - A〜Z: そのレジスターに追記し、追記した後の内容全体を履歴の先頭に積む
//   - _: 何もしない
//
// 履歴に積むテキストが MaxEntryBytes を超えたら、積まずに上限を超えた無名のレジスターに置き、
// 積まなかったことを知らせる文言を notice に返す（FR-53）。名前付きのレジスターには大きさによらず書く。
// 名前は呼び出し側が checkRegister で確かめておく。名前に使えない文字なら何もしない。
//
// replace が真なら、積む代わりに履歴の先頭を置き換える（1 回の Ex コマンドの行の中の 2 回目以降の削除。applyExResult）。
// pushed は、履歴に積んだか先頭を置き換えたかである。
//
// req: FR-51, FR-52, FR-53
func (r *Registers) write(name rune, reg Register, del, replace bool) (pushed bool, notice string) {
	reg.filled = true
	switch {
	case name == 0:
		if del && !r.cfg.deleteToHistory() {
			return false, ""
		}
	case name >= 'a' && name <= 'z':
		r.named[name-'a'] = reg
	case name >= 'A' && name <= 'Z':
		p := &r.named[name-'A']
		*p = p.appendTo(reg)
		reg = *p
	default:
		return false, ""
	}
	return r.push(reg, replace)
}

// push は、reg を履歴の先頭に積む（replace が真で履歴が空でなければ、先頭を置き換える）。件数の上限を超えたら最も古いものを捨てる。
// MaxEntryBytes を超えたら積まずに上限を超えた無名のレジスターに置き、知らせる文言を返す（FR-53）。
//
// 末尾に足して先頭を切り詰めるので、全体を写すのは配列の容量が尽きたときだけである。
//
// req: FR-51, FR-53
func (r *Registers) push(reg Register, replace bool) (bool, string) {
	if limit := r.cfg.maxEntryBytes(); len(reg.Text) > limit {
		r.oversize = reg
		return false, fmt.Sprintf("%d バイトのテキストは上限の %d バイトを超えるので、レジスターの履歴に積みませんでした", len(reg.Text), limit)
	}
	r.oversize = Register{}
	if n := len(r.history); replace && n > 0 {
		r.history[n-1] = reg
	} else {
		r.history = append(r.history, reg)
		if over := len(r.history) - r.cfg.historySize(); over > 0 {
			r.history = r.history[over:]
		}
	}
	r.pushes++
	return true, ""
}

// writeRegister は、操作の書き込みを Registers.write で行い、積まなかったことの知らせ（FR-53）を Result.Message に入れる。
// replace と pushed は Registers.write と同じである。
func (s *State) writeRegister(name rune, reg Register, del, replace bool) (pushed bool) {
	pushed, notice := s.registers.write(name, reg, del, replace)
	if notice != "" {
		s.res.Message = Message{Text: notice, Warning: true}
	}
	return pushed
}

// rawText は、マクロとして記録したキーの列を、Vim のレジスターと同じ生のテキストにする。
// <Esc> は "\x1b"、<CR> は "\r"、<Tab> は "\t"、<BS> は "\x08"、Ctrl つきの英字は制御文字にする。
// 生の文字にできないキー（矢印キーなど）は、Vim の表記（<Up> など）で書く。
func rawText(keys []editcmd.Key) string {
	var sb strings.Builder
	for _, k := range keys {
		switch {
		case k.Code == editcmd.CodeRune && k.Mod == 0:
			sb.WriteRune(k.Rune)
		case k.Code == editcmd.CodeRune && k.Mod == editcmd.ModCtrl && k.Rune >= 'a' && k.Rune <= 'z':
			sb.WriteRune(k.Rune - 'a' + 1)
		case k.Mod == 0 && k.Code == editcmd.CodeEsc:
			sb.WriteByte(0x1b)
		case k.Mod == 0 && k.Code == editcmd.CodeEnter:
			sb.WriteByte('\r')
		case k.Mod == 0 && k.Code == editcmd.CodeTab:
			sb.WriteByte('\t')
		case k.Mod == 0 && k.Code == editcmd.CodeBackspace:
			sb.WriteByte(0x08)
		default:
			sb.WriteString(k.String())
		}
	}
	return sb.String()
}

// keysFromText は、レジスターのテキストをキーの列にする（rawText の逆）。
// "\n" は <CR> として読む（行単位のレジスターを @ で再生するとき、行の区切りは <CR> に当たる）。
func keysFromText(text string) []editcmd.Key {
	keys := make([]editcmd.Key, 0, len(text))
	for _, r := range text {
		switch {
		case r == 0x1b:
			keys = append(keys, editcmd.Special(editcmd.CodeEsc))
		case r == '\r' || r == '\n':
			keys = append(keys, editcmd.Special(editcmd.CodeEnter))
		case r == '\t':
			keys = append(keys, editcmd.Special(editcmd.CodeTab))
		case r == 0x08 || r == 0x7f:
			keys = append(keys, editcmd.Special(editcmd.CodeBackspace))
		case r >= 1 && r <= 26:
			keys = append(keys, editcmd.Key{Code: editcmd.CodeRune, Rune: 'a' + r - 1, Mod: editcmd.ModCtrl}.Normalize())
		default:
			keys = append(keys, editcmd.Rune(r))
		}
	}
	return keys
}

// macroKeys は、レジスターの内容を、再生するキーの列にする。
func (r Register) macroKeys() []editcmd.Key {
	if r.keys != nil {
		return r.keys
	}
	text := r.Text
	if r.Linewise {
		text += "\n"
	}
	return keysFromText(text)
}
