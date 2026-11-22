package ext

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// decoration/set の JSON の形（基本設計 6.1）。
//
// 範囲の一覧は、範囲ごとのオブジェクトの配列ではなく、整数の列 data と色の種類の名前の表 styles で表す。
// LSP の semantic tokens と同じく、範囲の開始を直前の範囲の開始からの差で書き、1 つの範囲に 5 つの整数を使う。
// オブジェクトの配列では 1 つの範囲が 90 バイトほどになり、約 6 MB の JSON の一覧が 74 MB になって本体の受信の上限（64 MiB）を超えた（#47 のレビュー）。
//
//	{"buffer":1,"version":3,"styles":["keyword","string"],"data":[0,0,0,7,0, 1,4,0,5,1]}
//
// data の i 番目の範囲（0 から数える）は data[5i:5i+5] の次の 5 つである。直前の範囲の開始は、i が 0 なら行 0 のバイト 0 とする。
//
//	deltaLine  開始の行 - 直前の範囲の開始の行
//	deltaStart deltaLine が 0 なら 開始のバイト - 直前の範囲の開始のバイト、そうでなければ 開始のバイト
//	lineSpan   終わりの行 - 開始の行
//	end        lineSpan が 0 なら 終わりのバイト - 開始のバイト（範囲の長さ）、そうでなければ 終わりのバイト
//	style      styles の添字
//
// 整数は符号付きで、範囲の順序と値に制約は無い（並んでいなくても、行をまたいでも、終わりが開始より前でも表せる）。
// 送る側の一覧（DecorationSetParams.Decorations）は、順序を含めてそのまま読み戻せる。
// 本体は、重なった範囲を一覧の後ろのものほど上に描くので、順序には意味がある。
//
// styles の 1 つの項目は、色の種類の名前だけなら文字列で、直に付ける色（Decoration.Attr。FR-230）があれば
// 名前と色のオブジェクトである（名前は省いてよい）。0.18.0 までの本体は文字列でない項目を読めず、通知の全体を誤りとして捨てる。
//
//	{"buffer":1,"version":3,"styles":["keyword",{"style":"comment","attr":{"fg":"#8b80a8"}},{"attr":{"bg":"#ff0000"}}],"data":[...]}
const decorationFields = 5

// decorationSetWire は、decoration/set の params の JSON の形である。
type decorationSetWire struct {
	Buffer  BufferID        `json:"buffer"`
	Version Version         `json:"version"`
	Styles  []styleEntry    `json:"styles"`
	Data    json.RawMessage `json:"data"`
	// Old は、API のバージョン 0.1.0 までの形（範囲ごとのオブジェクトの配列）の項目である。読むときに誤りにするためだけに置く。
	Old json.RawMessage `json:"decorations,omitempty"`
}

// TextAttr は、範囲（Decoration）と仮想テキスト（VirtualTextChunk）に直に付ける色と飾りである（基本設計 6.1。FR-230）。
// 色の種類の名前（Style）と一緒に付けたら、本体は名前の色の上にこの色を重ねる（空の色は名前の色のまま、飾りはどちらかにあれば付ける）。
// 名前の色は利用者が設定の colors で変えられるが、この色は変えられない。インレイヒントの薄い色や、色コード（#ff0000 など）の着色のように、
// 拡張機能が値を決める色に使う。
type TextAttr struct {
	// Fg、Bg、Sp は、文字の色、背景の色、下線と波線の色である。"#rrggbb"（24 ビットの色。16 進の大文字と小文字のどちらでもよい）か、
	// 空（付けない）にする。
	Fg string `json:"fg,omitempty"`
	Bg string `json:"bg,omitempty"`
	Sp string `json:"sp,omitempty"`
	// Bold は太字、Italic は斜体、Underline は下線、Undercurl は波線、Reverse は文字と背景の色の入れ替えである。
	Bold      bool `json:"bold,omitempty"`
	Italic    bool `json:"italic,omitempty"`
	Underline bool `json:"underline,omitempty"`
	Undercurl bool `json:"undercurl,omitempty"`
	Reverse   bool `json:"reverse,omitempty"`
}

// Validate は、色が "#rrggbb" か空であることを確かめる。
func (a *TextAttr) Validate() error {
	for _, c := range [...]struct{ name, v string }{{"fg", a.Fg}, {"bg", a.Bg}, {"sp", a.Sp}} {
		if c.v != "" && !IsHexColor(c.v) {
			return fmt.Errorf("attr.%s %q is not #rrggbb", c.name, c.v)
		}
	}
	return nil
}

// IsHexColor は、s が "#rrggbb"（16 進の大文字と小文字のどちらでもよい）かを返す。
func IsHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// styleEntry は、decoration/set の styles の 1 つの項目（色の種類の名前と、直に付ける色）である。
// Attr が nil なら JSON では名前の文字列にし、そうでなければ名前と色のオブジェクトにする。
type styleEntry struct {
	Style string
	Attr  *TextAttr
}

// styleKey は、styleEntry を表の添字に引くための、比べられる形である。
type styleKey struct {
	style   string
	attr    TextAttr
	hasAttr bool
}

func (e styleEntry) key() styleKey {
	k := styleKey{style: e.Style}
	if e.Attr != nil {
		k.attr, k.hasAttr = *e.Attr, true
	}
	return k
}

// styleEntryObject は、styleEntry のオブジェクトの形である。
type styleEntryObject struct {
	Style string    `json:"style,omitempty"`
	Attr  *TextAttr `json:"attr,omitempty"`
}

// MarshalJSON は、e を名前の文字列か、名前と色のオブジェクトにする。
func (e styleEntry) MarshalJSON() ([]byte, error) {
	if e.Attr == nil {
		return json.Marshal(e.Style)
	}
	return json.Marshal(styleEntryObject(e))
}

// UnmarshalJSON は、名前の文字列か、名前と色のオブジェクトを読む。色が "#rrggbb" でなければ誤りにする。
func (e *styleEntry) UnmarshalJSON(b []byte) error {
	if i := skipSpace(b, 0); i < len(b) && b[i] == '"' {
		*e = styleEntry{}
		return json.Unmarshal(b, &e.Style)
	}
	var o styleEntryObject
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	if o.Attr != nil {
		if err := o.Attr.Validate(); err != nil {
			return err
		}
	}
	*e = styleEntry(o)
	return nil
}

// JSONSize は、p を MarshalJSON で JSON にしたときのバイト数を、符号化せずに返す。
// 送る前に大きさの上限を確かめるのに使う（一覧が大きいと符号化に数十 ms かかる）。
func (p DecorationSetParams) JSONSize() int {
	index, styles := styleTable(p.Decorations)
	n := len(`{"buffer":,"version":,"styles":,"data":[]}`) + uintDigits(uint64(p.Buffer)) + uintDigits(uint64(p.Version))
	n += len(mustMarshal(styles))
	var prev Position
	for _, d := range p.Decorations {
		for _, v := range encodeDecoration(prev, d, index.of(d)) {
			n += intDigits(v) + 1 // 区切りの ","
		}
		prev = d.Range.Start
	}
	if len(p.Decorations) > 0 {
		n-- // 最後の整数の後ろには "," が無い
	}
	return n
}

// styleEntry は、d の色の種類の名前と直に付ける色の組を返す。
func (d Decoration) styleEntry() styleEntry { return styleEntry{Style: d.Style, Attr: d.Attr} }

// styleIndex は、色の種類の名前と直に付ける色の組から、styles の表の添字を引く対応である（styleTable）。
// 直に付ける色の無い範囲（構文の色の範囲のほとんど）は、名前の文字列だけで引く。数十万の範囲を符号化するときに、
// 色の項目まで含めた組で引くと遅くなるためである。
type styleIndex struct {
	names map[string]int
	attrs map[styleKey]int
}

// of は、d の組の添字を返す。
func (x styleIndex) of(d Decoration) int {
	if d.Attr == nil {
		return x.names[d.Style]
	}
	return x.attrs[d.styleEntry().key()]
}

// styleTable は、decs に現れる色の種類の名前と直に付ける色の組を、初めて現れた順に並べた表と、組から添字への対応を返す。
func styleTable(decs []Decoration) (styleIndex, []styleEntry) {
	index := styleIndex{names: map[string]int{}, attrs: map[styleKey]int{}}
	styles := []styleEntry{}
	for _, d := range decs {
		if d.Attr == nil {
			if _, ok := index.names[d.Style]; !ok {
				index.names[d.Style] = len(styles)
				styles = append(styles, d.styleEntry())
			}
			continue
		}
		k := d.styleEntry().key()
		if _, ok := index.attrs[k]; !ok {
			index.attrs[k] = len(styles)
			styles = append(styles, d.styleEntry())
		}
	}
	return index, styles
}

// mustMarshal は、styles の表を JSON にする。名前の文字列と色のオブジェクトの符号化は失敗しない。
func mustMarshal(styles []styleEntry) []byte {
	b, err := json.Marshal(styles)
	if err != nil {
		panic(err)
	}
	return b
}

// uintDigits は、u の 10 進の桁数を返す。
func uintDigits(u uint64) int {
	n := 1
	for ; u >= 10; u /= 10 {
		n++
	}
	return n
}

// intDigits は、i の 10 進の表記の文字数（負なら "-" を含む）を返す。
func intDigits(i int) int {
	n := 1
	if i < 0 {
		n++ // "-"。MinInt の符号を反転すると桁あふれするので、負のまま割る
		for ; i <= -10; i /= 10 {
			n++
		}
		return n
	}
	for ; i >= 10; i /= 10 {
		n++
	}
	return n
}

// MarshalJSON は、p を decoration/set の JSON の形（範囲を整数の列にしたもの）にする。
// 色の種類の名前の表 styles は、一覧に初めて現れた順に並べる。
func (p DecorationSetParams) MarshalJSON() ([]byte, error) {
	index, styles := styleTable(p.Decorations)
	stylesJSON := mustMarshal(styles)
	// 1 つの範囲はたいてい 12 バイトに収まる。
	b := make([]byte, 0, 64+len(stylesJSON)+12*len(p.Decorations))
	b = append(b, `{"buffer":`...)
	b = strconv.AppendUint(b, uint64(p.Buffer), 10)
	b = append(b, `,"version":`...)
	b = strconv.AppendUint(b, uint64(p.Version), 10)
	b = append(b, `,"styles":`...)
	b = append(b, stylesJSON...)
	b = append(b, `,"data":[`...)
	var prev Position
	for i, d := range p.Decorations {
		if i > 0 {
			b = append(b, ',')
		}
		for j, v := range encodeDecoration(prev, d, index.of(d)) {
			if j > 0 {
				b = append(b, ',')
			}
			b = strconv.AppendInt(b, int64(v), 10)
		}
		prev = d.Range.Start
	}
	b = append(b, "]}"...)
	return b, nil
}

// encodeDecoration は、直前の範囲の開始が prev のときの、d の 5 つの整数を返す。
// 差は int の範囲で桁あふれしてもよい（decodeDecoration が同じく桁あふれして元の値に戻る）。
func encodeDecoration(prev Position, d Decoration, style int) [decorationFields]int {
	s, e := d.Range.Start, d.Range.End
	v := [decorationFields]int{s.Line - prev.Line, s.Byte, e.Line - s.Line, e.Byte, style}
	if v[0] == 0 {
		v[1] -= prev.Byte
	}
	if v[2] == 0 {
		v[3] -= s.Byte
	}
	return v
}

// decodeDecoration は、直前の範囲の開始が prev のときの 5 つの整数 v から、範囲を求める。encodeDecoration の逆である。
func decodeDecoration(prev Position, v [decorationFields]int) Range {
	var r Range
	r.Start.Line = prev.Line + v[0]
	r.Start.Byte = v[1]
	if v[0] == 0 {
		r.Start.Byte += prev.Byte
	}
	r.End.Line = r.Start.Line + v[2]
	r.End.Byte = v[3]
	if v[2] == 0 {
		r.End.Byte += r.Start.Byte
	}
	return r
}

// UnmarshalJSON は、decoration/set の JSON の形を読む。
// data の長さが 5 の倍数でないか、style が styles の外を指すか、styles の色が "#rrggbb" でなければ誤りを返す。
// data の項目が無いか null の通知と、前の形の項目 decorations を持つ通知も誤りにする。
// 前の形の拡張機能の通知を空の一覧として受け取ると、本体はその拡張機能の色を黙って消すからである。空の一覧は "data":[] で送る。
// 一覧が空なら、Decorations は nil ではなく長さ 0 のスライスにする。
func (p *DecorationSetParams) UnmarshalJSON(b []byte) error {
	var w decorationSetWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	if w.Old != nil {
		return errors.New("decoration/set: the decorations field is the format before API 0.2.0; send styles and data")
	}
	if w.Data == nil || string(w.Data) == "null" {
		return errors.New("decoration/set: data is missing")
	}
	data, err := parseInts(w.Data)
	if err != nil {
		return fmt.Errorf("decoration/set data: %w", err)
	}
	if len(data)%decorationFields != 0 {
		return fmt.Errorf("decoration/set data: length %d is not a multiple of %d", len(data), decorationFields)
	}
	decs := make([]Decoration, len(data)/decorationFields)
	var prev Position
	for i := range decs {
		v := [decorationFields]int(data[i*decorationFields : (i+1)*decorationFields])
		if v[4] < 0 || v[4] >= len(w.Styles) {
			return fmt.Errorf("decoration/set data: style index %d out of range [0, %d)", v[4], len(w.Styles))
		}
		r := decodeDecoration(prev, v)
		st := w.Styles[v[4]]
		decs[i] = Decoration{Range: r, Style: st.Style, Attr: st.Attr}
		prev = r.Start
	}
	*p = DecorationSetParams{Buffer: w.Buffer, Version: w.Version, Decorations: decs}
	return nil
}

var errNotIntArray = errors.New("not an array of integers")

// parseInts は、JSON の整数の配列を読む。
// encoding/json で []int に読むより速い（一覧は数十万の範囲、数百万の整数になる）。
// 呼ぶ前に encoding/json が JSON として正しいことを確かめているので、ここでは整数の配列であることだけを確かめる。
func parseInts(b []byte) ([]int, error) {
	i := skipSpace(b, 0)
	if i == len(b) || b[i] != '[' {
		return nil, errNotIntArray
	}
	// 整数の数は、区切りの数から見積もる。
	n := 0
	for _, c := range b {
		if c == ',' {
			n++
		}
	}
	out := make([]int, 0, n+1)
	i = skipSpace(b, i+1)
	if i < len(b) && b[i] == ']' {
		return out, nil
	}
	for {
		j := i
		if j < len(b) && b[j] == '-' {
			j++
		}
		for j < len(b) && b[j] >= '0' && b[j] <= '9' {
			j++
		}
		v, err := strconv.Atoi(string(b[i:j]))
		if err != nil {
			return nil, errNotIntArray
		}
		out = append(out, v)
		i = skipSpace(b, j)
		if i == len(b) {
			return nil, errNotIntArray
		}
		switch b[i] {
		case ',':
			i = skipSpace(b, i+1)
		case ']':
			return out, nil
		default:
			return nil, errNotIntArray
		}
	}
}

// skipSpace は、b[i:] の先頭の JSON の空白を飛ばした位置を返す。
func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}
