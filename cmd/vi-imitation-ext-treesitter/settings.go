package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/piplupflipper/vi-imitation/internal/extmgr"
)

// parserSpec は、利用者の設定に並べた 1 つのパーサである（FR-142。設定の extensions.settings.treesitter.parsers の 1 項目）。
//
// 設定の形は次のとおりである（項目の名前は仮）。
//
//	"extensions": {
//	  "settings": {
//	    "treesitter": {
//	      // パーサの C のソースをビルドする C コンパイラ。既定は PATH の cc
//	      "compiler": "cc",
//	      "parsers": {
//	        // 名前は、取得したソースとビルドしたものを置くディレクトリの名前と、ロックファイルの記録の名前になる
//	        "python": {
//	          "repo": "https://github.com/tree-sitter/tree-sitter-python",
//	          "version": "v0.25.0",
//	          "fileTypes": ["python"],
//	          // 省略できる。src/ のあるディレクトリ（リポジトリの中の相対パス）。既定はリポジトリの最上位
//	          "path": "",
//	          // 省略できる。ハイライトのクエリのファイル（リポジトリの中の相対パス）。複数あれば順につなぐ
//	          "queries": ["queries/highlights.scm"]
//	        },
//	        // ほかのパーサのリポジトリのクエリを継ぐ例（FR-328）。queries の項目に {"parser", "file"} を書く
//	        "typescript": {
//	          "repo": "https://github.com/tree-sitter/tree-sitter-typescript",
//	          "version": "v0.23.2",
//	          "fileTypes": ["typescript"],
//	          "path": "typescript",
//	          "queries": ["queries/highlights.scm", {"parser": "javascript", "file": "queries/highlights.scm"}]
//	        }
//	      },
//	      // 見出しの帯の設定（contextSettings）
//	      "context": { "maxLines": 4, "multilineThreshold": 1 },
//	      // ブロックの範囲の罫線の設定（chunkSettings）
//	      "chunk": { "enable": true }
//	    }
//	  }
//	}
type parserSpec struct {
	// name は、parsers の項目の名前である。
	name string
	// repo、version は、取得元の git のリポジトリと版（タグ、ブランチ、コミットのハッシュ）である（extmgr.Checkout に渡す）。
	repo, version string
	// fileTypes は、このパーサを使うファイルの種類である。利用者の設定の中で重なった種類は除いてある。
	fileTypes []string
	// path は、src/parser.c のある src/ の親のディレクトリの、リポジトリの中の相対パスである。空ならリポジトリの最上位である。
	// 1 つのリポジトリに複数の文法を置くもの（typescript/ と tsx/ を持つ tree-sitter-typescript など）に使う。
	path string
	// queries は、ハイライトのクエリのファイルの列である。並べた順につなぐ（先に書いたものの色が優先される。decorations の doc）。
	// 空なら、path の下の queries/highlights.scm、無ければリポジトリの最上位の queries/highlights.scm を使う。
	queries []querySource
}

// querySource は、ハイライトのクエリの 1 つのファイルである。
// 設定では、このパーサのリポジトリのファイルは文字列で、ほかのパーサのリポジトリのファイルは {"parser", "file"} で書く（FR-328）。
type querySource struct {
	// parser は、ファイルを持つパーサの名前である。空なら、このパーサ自身のリポジトリである。
	// 空でなければ、同じ設定の parsers に誤り無く書いたパーサの名前で、自身の名前ではない（parseSettings が確かめる）。
	parser string
	// file は、そのパーサのリポジトリの中の相対パスである。
	file string
}

// String は、誤りの文言とビルドの鍵に使う表記（"file" か "parser:file"）を返す。
func (q querySource) String() string {
	if q.parser == "" {
		return q.file
	}
	return q.parser + ":" + q.file
}

// inherits は、spec のクエリが継ぐほかのパーサの名前を、初めて出てきた順に重ねずに返す。
func (spec parserSpec) inherits() []string {
	var names []string
	for _, q := range spec.queries {
		if q.parser != "" && !slices.Contains(names, q.parser) {
			names = append(names, q.parser)
		}
	}
	return names
}

// treesitterSettings は、設定の extensions.settings.treesitter の値である（parserSpec の doc の形）。
type treesitterSettings struct {
	// compiler は、パーサの C のソースをビルドする C コンパイラの名前かパスである。空なら "cc" である。
	compiler string
	// parsers は、名前の順に並べた、使えるパーサの一覧である。誤りのある項目は含めない。
	parsers []parserSpec
	// listed は、parsers の項目の名前の集合で、誤りのある項目の名前も含む。
	// ロックファイルから、設定に並べなくなったパーサの記録を消すのに使う（誤りのある項目の記録は残す）。
	listed map[string]bool
	// context は、見出しの帯（sticky.go）の設定である。
	context contextSettings
	// chunk は、ブロックの範囲の罫線（chunk.go）の設定である。
	chunk chunkSettings
}

// chunkSettings は、設定の extensions.settings.treesitter.chunk の値である（FR-267。項目の名前は仮）。
// 既定は、hlchunk.nvim（コミット 4ddc056e4ca45f1c7dc6aa21935df710b263b1aa）の lua/hlchunk/mods/chunk/chunk_conf.lua の既定と、
// lua/hlchunk/mods/base_mod/base_conf.lua の enable の既定（false）に合わせた。
//
//	"chunk": {
//	  // 罫線を出すか。既定は出さない（hlchunk.nvim は setup で enable を書かないと出さない）
//	  "enable": true,
//	  // カーソルが止まってから描き直すまでのミリ秒（hlchunk.nvim の delay。既定は 300）
//	  "delay": 100,
//	  // 罫線を描く動きのミリ秒（hlchunk.nvim の duration。既定は 200）。0 なら一度に描く
//	  "duration": 100,
//	  // 構文の誤りを含むまとまりを chunk.error の色で描くか（hlchunk.nvim の error_sign。既定は描く）
//	  "errorSign": true,
//	  // この大きさ（バイト）以上のバッファには描かない（hlchunk.nvim の max_file_size。既定は 1 MiB）。0 なら上限なし
//	  "maxFileSize": 1048576,
//	  // 罫線の記号（hlchunk.nvim の chars）。どれも表示幅が 1 の文字 1 つ
//	  "symbols": { "horizontalLine": "═", "verticalLine": "║", "leftTop": "╔", "leftBottom": "╚", "leftArrow": "─", "rightArrow": "▶" }
//	}
type chunkSettings struct {
	enable bool
	// delay と duration は、ミリ秒である。
	delay, duration int
	errorSign       bool
	// maxFileSize は、罫線を描かないバッファの全文のバイト数の下限である。0 なら上限なし。
	maxFileSize int
	symbols     chunkSymbols
}

// chunkSymbols は、罫線の記号である（hlchunk.nvim の chars）。どれも表示幅が 1 の文字 1 つである。
type chunkSymbols struct {
	// horizontalLine は、始まりと終わりの行の横線、verticalLine は間の行の縦線である。
	horizontalLine, verticalLine string
	// leftTop と leftBottom は、始まりと終わりの行の角である。
	leftTop, leftBottom string
	// leftArrow は始まりの行の、rightArrow は終わりの行の、本文の側の端の記号である。
	leftArrow, rightArrow string
}

// defaultChunkSettings は、設定に chunk が無いときの値である（chunkSettings の doc）。
var defaultChunkSettings = chunkSettings{
	enable:      false,
	delay:       300,
	duration:    200,
	errorSign:   true,
	maxFileSize: 1024 * 1024,
	symbols: chunkSymbols{
		horizontalLine: "─",
		verticalLine:   "│",
		leftTop:        "╭",
		leftBottom:     "╰",
		leftArrow:      "─",
		rightArrow:     ">",
	},
}

// chunkJSON は、chunk の JSON の形である。省いた項目は nil で、既定の値を使う。
type chunkJSON struct {
	Enable      *bool             `json:"enable"`
	Delay       *int              `json:"delay"`
	Duration    *int              `json:"duration"`
	ErrorSign   *bool             `json:"errorSign"`
	MaxFileSize *int              `json:"maxFileSize"`
	Symbols     map[string]string `json:"symbols"`
}

// maxChunkMs は、chunk の delay と duration の上限のミリ秒である（仮。誤って大きな値を書いて、罫線が出ないように見えるのを防ぐ）。
const maxChunkMs = 10000

// parseChunk は、chunk の値を読む。誤りのある項目は既定の値のままにし、誤りを返す。
//
// req: FR-267
func parseChunk(raw json.RawMessage) (chunkSettings, []string) {
	out := defaultChunkSettings
	var j chunkJSON
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return out, []string{fmt.Sprintf("設定の extensions.settings.treesitter.chunk を読めない: %v", err)}
	}
	var problems []string
	if j.Enable != nil {
		out.enable = *j.Enable
	}
	if j.ErrorSign != nil {
		out.errorSign = *j.ErrorSign
	}
	if j.MaxFileSize != nil {
		if *j.MaxFileSize < 0 {
			problems = append(problems, "設定の extensions.settings.treesitter.chunk.maxFileSize は 0 以上にする")
		} else {
			out.maxFileSize = *j.MaxFileSize
		}
	}
	for _, f := range []struct {
		name string
		v    *int
		to   *int
	}{{"delay", j.Delay, &out.delay}, {"duration", j.Duration, &out.duration}} {
		if f.v == nil {
			continue
		}
		if *f.v < 0 || *f.v > maxChunkMs {
			problems = append(problems, fmt.Sprintf("設定の extensions.settings.treesitter.chunk.%s は 0 から %d の整数にする", f.name, maxChunkMs))
			continue
		}
		*f.to = *f.v
	}
	fields := map[string]*string{
		"horizontalLine": &out.symbols.horizontalLine,
		"verticalLine":   &out.symbols.verticalLine,
		"leftTop":        &out.symbols.leftTop,
		"leftBottom":     &out.symbols.leftBottom,
		"leftArrow":      &out.symbols.leftArrow,
		"rightArrow":     &out.symbols.rightArrow,
	}
	for _, name := range sortedKeys(j.Symbols) {
		to, ok := fields[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("設定の extensions.settings.treesitter.chunk.symbols に知らない項目 %s がある", name))
			continue
		}
		if !isChunkSymbol(j.Symbols[name]) {
			problems = append(problems, fmt.Sprintf("設定の extensions.settings.treesitter.chunk.symbols.%s は表示幅が 1 の文字 1 つにする", name))
			continue
		}
		*to = j.Symbols[name]
	}
	return out, problems
}

// isChunkSymbol は、s を罫線の記号に使えるか（文字 1 つで、タブ、制御文字、空白、結合文字でないか）を返す。
// 本体の表示幅の表を持たないので、全角の文字は断れない（書いた全角の文字は、本体が 2 列で描くので罫線がずれる）。
func isChunkSymbol(s string) bool {
	r, size := utf8.DecodeRuneInString(s)
	if s == "" || size != len(s) || r == utf8.RuneError {
		return false
	}
	return unicode.IsGraphic(r) && !unicode.IsSpace(r) && !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Me, r)
}

// contextSettings は、設定の extensions.settings.treesitter.context の値である（FR-247。項目の名前は仮）。
//
//	"context": {
//	  // 見出しの帯を出すか。既定は出す（nvim-treesitter-context の enable の既定）
//	  "enable": true,
//	  // 帯の行数の上限。0 は上限なし（nvim-treesitter-context の max_lines の既定）
//	  "maxLines": 0,
//	  // 1 つの見出しに使う行数の上限（nvim-treesitter-context の multiline_threshold。既定は 20）
//	  "multilineThreshold": 20
//	}
type contextSettings struct {
	enable             bool
	maxLines           int
	multilineThreshold int
}

// defaultContextSettings は、設定に context が無いときの値である。nvim-treesitter-context（コミット 8decd73460d26ef0a0befb745d022dd1fc2c38b8）の
// lua/treesitter-context/config.lua の既定（enable = true、max_lines = 0、multiline_threshold = 20）に合わせた。
var defaultContextSettings = contextSettings{enable: true, maxLines: 0, multilineThreshold: 20}

// contextJSON は、context の JSON の形である。省いた項目は nil で、既定の値を使う。
type contextJSON struct {
	Enable             *bool `json:"enable"`
	MaxLines           *int  `json:"maxLines"`
	MultilineThreshold *int  `json:"multilineThreshold"`
}

// parseContext は、context の値を読む。誤りのある項目は既定の値のままにし、誤りを返す。
//
// req: FR-247
func parseContext(raw json.RawMessage) (contextSettings, []string) {
	out := defaultContextSettings
	var j contextJSON
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return out, []string{fmt.Sprintf("設定の extensions.settings.treesitter.context を読めない: %v", err)}
	}
	var problems []string
	if j.Enable != nil {
		out.enable = *j.Enable
	}
	if j.MaxLines != nil {
		if *j.MaxLines < 0 {
			problems = append(problems, "設定の extensions.settings.treesitter.context.maxLines は 0 以上にする")
		} else {
			out.maxLines = *j.MaxLines
		}
	}
	if j.MultilineThreshold != nil {
		if *j.MultilineThreshold < 1 {
			problems = append(problems, "設定の extensions.settings.treesitter.context.multilineThreshold は 1 以上にする")
		} else {
			out.multilineThreshold = *j.MultilineThreshold
		}
	}
	return out, problems
}

// defaultCompiler は、設定に compiler が無いときの C コンパイラである。
const defaultCompiler = "cc"

// parserName は、パーサの名前に使える文字列である（extmgr の拡張機能の名前と同じ規則）。ディレクトリの名前になる。
var parserName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// parseSettings は、extensions.settings.treesitter の値を読む（FR-142）。
//
// 誤りは problems に 1 件ずつ日本語で返し、誤りのある項目だけを使わない。
//
//   - 値が JSON のオブジェクトでなければ、すべてを使わない。parsers がオブジェクトでないときも、パーサを 1 つも使わない
//   - 知らない項目（最上位と各パーサ）は誤りにする。最上位の知らない項目は無視し、パーサの知らない項目はそのパーサを使わない
//   - repo が空か "-" で始まるか相対パス、version が空か "-" で始まる、fileTypes が空か空の名前を含む、
//     path か queries がリポジトリの外を指す（絶対パスや ".." を含む）パーサは使わない
//   - queries で継ぐパーサ（FR-328）が、誤り無く読めたパーサに無ければ、継ぐ側のパーサを使わない（dropBrokenInherits）
//   - 2 つ以上のパーサが同じファイルの種類を持てば、その種類にはどのパーサも使わない（ほかの種類には使う）
//
// settings が nil（設定が無い）なら、誤りの無い空の設定を返す。
//
// req: FR-142, FR-328
func parseSettings(settings json.RawMessage) (treesitterSettings, []string) {
	out := treesitterSettings{compiler: defaultCompiler, listed: map[string]bool{}, context: defaultContextSettings, chunk: defaultChunkSettings}
	if len(bytes.TrimSpace(settings)) == 0 || bytes.Equal(bytes.TrimSpace(settings), []byte("null")) {
		return out, nil
	}
	var problems []string
	var top map[string]json.RawMessage
	if err := json.Unmarshal(settings, &top); err != nil {
		return out, []string{fmt.Sprintf("設定の extensions.settings.treesitter を読めない: %v", err)}
	}
	keys := sortedKeys(top)
	for _, k := range keys {
		switch k {
		case "compiler":
			var c string
			if err := json.Unmarshal(top[k], &c); err != nil || c == "" {
				problems = append(problems, "設定の extensions.settings.treesitter.compiler は空でない文字列にする")
				continue
			}
			out.compiler = c
		case "context":
			c, ps := parseContext(top[k])
			out.context = c
			problems = append(problems, ps...)
		case "chunk":
			c, ps := parseChunk(top[k])
			out.chunk = c
			problems = append(problems, ps...)
		case "parsers":
		default:
			problems = append(problems, fmt.Sprintf("設定の extensions.settings.treesitter に知らない項目 %s がある", k))
		}
	}
	raw, ok := top["parsers"]
	if !ok {
		return out, problems
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return out, append(problems, fmt.Sprintf("設定の extensions.settings.treesitter.parsers を読めない: %v", err))
	}
	for _, name := range sortedKeys(entries) {
		out.listed[name] = true
		spec, err := parseParser(name, entries[name])
		if err != nil {
			problems = append(problems, fmt.Sprintf("パーサ %s を使わない: %v", name, err))
			continue
		}
		out.parsers = append(out.parsers, spec)
	}
	out.parsers, problems = dropBrokenInherits(out.parsers, problems)

	// 利用者の設定の中で重なったファイルの種類は、どのパーサにも使わない。
	owners := map[string][]string{}
	for _, p := range out.parsers {
		for _, ft := range p.fileTypes {
			if !slices.Contains(owners[ft], p.name) {
				owners[ft] = append(owners[ft], p.name)
			}
		}
	}
	for _, ft := range sortedKeys(owners) {
		if names := owners[ft]; len(names) > 1 {
			problems = append(problems, fmt.Sprintf("ファイルの種類 %s を複数のパーサ（%s）が持つので、%s にはどれも使わない", ft, strings.Join(names, "、"), ft))
		}
	}
	for i := range out.parsers {
		p := &out.parsers[i]
		p.fileTypes = slices.DeleteFunc(p.fileTypes, func(ft string) bool { return len(owners[ft]) > 1 })
	}
	return out, problems
}

// dropBrokenInherits は、クエリを継ぐパーサ（querySource.parser）が、誤り無く読めたパーサ（parsers）に無いパーサを除き、
// 除いた理由を problems に足して返す。除いたパーサを継ぐパーサも、続けて除く。
//
// req: FR-328
func dropBrokenInherits(parsers []parserSpec, problems []string) ([]parserSpec, []string) {
	for {
		valid := map[string]bool{}
		for _, p := range parsers {
			valid[p.name] = true
		}
		var kept []parserSpec
		for _, p := range parsers {
			missing := ""
			for _, name := range p.inherits() {
				if !valid[name] {
					missing = name
					break
				}
			}
			if missing != "" {
				problems = append(problems, fmt.Sprintf("パーサ %s を使わない: queries の継ぐパーサ %s が、parsers に誤り無く書かれていない", p.name, missing))
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) == len(parsers) {
			return kept, problems
		}
		parsers = kept
	}
}

// parserJSON は、parsers の 1 項目の JSON の形である。
type parserJSON struct {
	Repo      string   `json:"repo"`
	Version   string   `json:"version"`
	FileTypes []string `json:"fileTypes"`
	Path      string   `json:"path"`
	// Queries の項目は、このパーサのリポジトリの中の相対パスの文字列か、queryJSON のオブジェクトである。
	Queries []json.RawMessage `json:"queries"`
}

// queryJSON は、queries の項目のうち、ほかのパーサのリポジトリのファイルを指すものの JSON の形である（FR-328）。
type queryJSON struct {
	// Parser は、同じ設定の parsers に書いたパーサの名前である。
	Parser string `json:"parser"`
	// File は、そのパーサのリポジトリの中の相対パスである。
	File string `json:"file"`
}

// parseQuery は、queries の 1 項目を読んで検査する。self は、そのパーサ自身の名前である。
//
// req: FR-328
func parseQuery(self string, raw json.RawMessage) (querySource, error) {
	var file string
	if err := json.Unmarshal(raw, &file); err == nil {
		if !filepath.IsLocal(file) {
			return querySource{}, fmt.Errorf("queries の %q はリポジトリの中の相対パスにする", file)
		}
		return querySource{file: file}, nil
	}
	var j queryJSON
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return querySource{}, fmt.Errorf("queries の項目 %s は、文字列か {\"parser\": 名前, \"file\": パス} にする", raw)
	}
	switch {
	case j.Parser == "":
		return querySource{}, errors.New("queries の項目の parser が空である")
	case j.Parser == self:
		return querySource{}, fmt.Errorf("queries の項目の parser に自身の名前 %s は書かない（自身のファイルは文字列で書く）", self)
	case !filepath.IsLocal(j.File):
		return querySource{}, fmt.Errorf("queries の項目の file %q はパーサ %s のリポジトリの中の相対パスにする", j.File, j.Parser)
	}
	return querySource{parser: j.Parser, file: j.File}, nil
}

// parseParser は、parsers の 1 項目を読んで検査する。
func parseParser(name string, raw json.RawMessage) (parserSpec, error) {
	if !parserName.MatchString(name) {
		return parserSpec{}, fmt.Errorf("名前は英数字で始め、英数字と . _ - だけにする")
	}
	var j parserJSON
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return parserSpec{}, fmt.Errorf("読めない: %w", err)
	}
	switch {
	case j.Repo == "" || strings.HasPrefix(j.Repo, "-"):
		return parserSpec{}, fmt.Errorf("repo %q は空にせず、- で始めない", j.Repo)
	case extmgr.IsRelativeRepo(j.Repo):
		return parserSpec{}, fmt.Errorf("repo %q は相対パスにしない（URL か絶対パスにする）", j.Repo)
	case j.Version == "" || strings.HasPrefix(j.Version, "-"):
		return parserSpec{}, fmt.Errorf("version %q は空にせず、- で始めない", j.Version)
	case len(j.FileTypes) == 0:
		return parserSpec{}, fmt.Errorf("fileTypes が空である")
	case slices.Contains(j.FileTypes, ""):
		return parserSpec{}, fmt.Errorf("fileTypes に空の名前がある")
	case j.Path != "" && !filepath.IsLocal(j.Path):
		return parserSpec{}, fmt.Errorf("path %q はリポジトリの中の相対パスにする", j.Path)
	}
	var queries []querySource
	for _, raw := range j.Queries {
		q, err := parseQuery(name, raw)
		if err != nil {
			return parserSpec{}, err
		}
		queries = append(queries, q)
	}
	fileTypes := slices.Clone(j.FileTypes)
	slices.Sort(fileTypes)
	return parserSpec{
		name:      name,
		repo:      j.Repo,
		version:   j.Version,
		fileTypes: slices.Compact(fileTypes),
		path:      filepath.Clean(j.Path),
		queries:   queries,
	}, nil
}

// sortedKeys は、m のキーを名前の順に返す。
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
