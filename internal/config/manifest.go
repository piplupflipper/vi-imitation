package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/piplupflipper/vi-imitation/ext"
)

// ManifestFileName は、拡張機能のディレクトリに置く宣言ファイルの名前である（基本設計 5.1。make dist の配置と同じ）。
// 拡張子は .json だが、中身は JSONC として読む。
const ManifestFileName = "manifest.json"

// Extension は、読み込んで検査した 1 つの拡張機能である。
// 項目の名前と型と並びは exthost.Spec と同じにしてあり、呼び出し側は exthost.Spec(e) で変換できる。
type Extension struct {
	// Manifest は、検査済み（ext.ValidateManifests が誤りを返さなかった）の宣言ファイルの内容である。
	// 設定の extensions.activation の契機は、Activation の後ろに足してある。
	Manifest ext.Manifest
	// Dir は、宣言ファイルのあるディレクトリの絶対パスである。
	Dir string
	// Settings は、設定の extensions.settings のうち、この拡張機能に向けた部分である。無ければ nil。
	Settings json.RawMessage
}

// DefaultPath は、設定ファイルの置き場所（仮）を返す。
// $XDG_CONFIG_HOME/vi-imitation/config.jsonc で、XDG_CONFIG_HOME が空か相対パスなら（XDG Base Directory の決まり）、
// $HOME/.config を使う。どちらも決まらなければ誤りを返す。
func DefaultPath() (string, error) {
	base, err := xdgDir("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName, FileName), nil
}

// StateDir は、状態のファイル（アンドゥの履歴、カーソルの位置。FR-165、FR-166）の置き場所を返す（基本設計 4.10）。
// $XDG_STATE_HOME/vi-imitation で、XDG_STATE_HOME が空か相対パスなら（XDG Base Directory の決まり）、$HOME/.local/state を使う。
// どちらも決まらなければ誤りを返す。Neovim が undo と shada を置く stdpath('state') と同じ規則である。
// ディレクトリは作らない（使う側が、状態を残すときに初めて作る）。
//
// この先、ほかの状態（コマンドラインと検索の履歴、レジスターなど）を残す機能も、この下に置く。
func StateDir() (string, error) {
	base, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
}

// ExtensionDirs は、拡張機能を探すディレクトリ（仮）を、優先する順に返す。
// executable は本体の実行ファイルのパス（os.Executable の結果）で、空なら 2 つ目を省く。
//
//  1. $XDG_DATA_HOME/vi-imitation/extensions（利用者の拡張機能）。XDG_DATA_HOME が空か相対パスなら $HOME/.local/share を使い、
//     どちらも決まらなければ省く
//  2. executable と同じディレクトリの extensions（同梱の拡張機能。make dist の配置）
//
// 同じ ID の拡張機能が複数のディレクトリにあれば、LoadExtensions は先のディレクトリのものを使う。
// 利用者のディレクトリを先にするのは、同梱の拡張機能を利用者が同じ ID のもので置き換えられるようにするためである（Vim の runtimepath と同じ考え方）。
func ExtensionDirs(executable string) []string {
	var dirs []string
	if base, err := xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share")); err == nil {
		dirs = append(dirs, filepath.Join(base, appName, "extensions"))
	}
	if executable != "" {
		dirs = append(dirs, filepath.Join(filepath.Dir(executable), "extensions"))
	}
	return dirs
}

// xdgDir は、環境変数 env が絶対パスならそれを、そうでなければ $HOME/fallback を返す。
func xdgDir(env, fallback string) (string, error) {
	if d := os.Getenv(env); filepath.IsAbs(d) {
		return d, nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("%s と HOME がどちらも設定されていない", env)
	}
	return filepath.Join(home, fallback), nil
}

// manifestKeys は、宣言ファイルの最上位の項目の名前である（ext.Manifest の json のタグ）。
var manifestKeys = map[string]bool{
	"id": true, "command": true, "args": true, "saveHook": true, "noBufferSync": true, "activation": true, "contributes": true, "provides": true,
}

// LoadManifest は、path の宣言ファイルを JSONC として読み、ext.Manifest.Validate で検査する。
//
// 読めないとき（構文の誤り、項目の型の違い）と、Validate が誤りを返したときは err を返し、その宣言ファイルは使わない。
// 最上位の未知の項目は、後の版の API で足した項目かもしれないので誤りにせず、warnings に位置付きで返す。
// Validate の警告（未知の契機）も warnings に返す。
// 位置の分かる誤りと警告は *Error である。
//
// req: FR-40
func LoadManifest(path string) (m ext.Manifest, warnings []error, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return m, nil, &Error{Path: path, Msg: "宣言ファイルを読めない: " + err.Error()}
	}
	s := newSource(path, data)
	root, err := s.parseJSONC()
	if err != nil {
		return m, nil, err
	}
	obj, ok := root.Value.(*hujson.Object)
	if !ok {
		return m, nil, s.at(root.StartOffset, "宣言ファイルの最上位はオブジェクトにする")
	}
	for _, mb := range s.members(obj, "") {
		if !manifestKeys[mb.name] {
			s.report(mb.nameOff, "未知の項目 %s を無視する", join("", mb.name))
		}
	}
	warnings = s.problems
	if err := json.Unmarshal(s.std, &m); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return ext.Manifest{}, warnings, s.at(typeErrorOffset(&root, te), "宣言ファイルの %s の型が違う（%s に %s は入らない）", te.Field, te.Type, te.Value)
		}
		return ext.Manifest{}, warnings, &Error{Path: path, Msg: "宣言ファイルを読めない: " + err.Error()}
	}
	vw, verr := m.Validate()
	for _, w := range vw {
		warnings = append(warnings, &Error{Path: path, Msg: w.Error()})
	}
	if verr != nil {
		return ext.Manifest{}, warnings, &Error{Path: path, Msg: "宣言ファイルの誤り: " + verr.Error()}
	}
	return m, warnings, nil
}

// typeErrorOffset は、型の違う値の先頭の位置を返す。
// encoding/json の Offset は値を読み終えた位置なので、te.Field（"activation.0.on" の形）を JSON Pointer に直して構文木から引く。
// 引けなければ Offset を使う。
func typeErrorOffset(root *hujson.Value, te *json.UnmarshalTypeError) int {
	if te.Field != "" {
		ptr := ""
		for f := range strings.SplitSeq(te.Field, ".") {
			ptr += "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(f)
		}
		if v := root.Find(ptr); v != nil {
			return v.StartOffset
		}
	}
	return int(te.Offset)
}

// LoadExtensions は、dirs の各ディレクトリの直下の <名前>/manifest.json を宣言ファイルとして読み、
// 設定 cfg の extensions.settings と extensions.activation を合わせた拡張機能の一覧を返す（基本設計 5.1、6.7）。
// 拡張機能のプロセスは起動しない（FR-14）。
//
// 宣言ファイルの ext.ActivationSettingsFileType の契機の直後に、その拡張機能に向けた extensions.settings から作った
// ファイルの種類の契機（ext.ActivationFileType）を足す（expandSettingsActivation）。extensions.activation の契機はその後ろに並ぶ。
//
// extensions.disabled に ID を書いた拡張機能は、どのディレクトリのものも一覧に入れない（FR-226）。
//
// 一覧の順は、dirs の順、ディレクトリの中では名前の順である。
// 存在しないディレクトリと、manifest.json の無いディレクトリは飛ばす。
// 読めない宣言ファイルと、先に読んだものと ID が重なる宣言ファイルは使わず、problems に返す。
// 宣言ファイルの警告と、設定に書いたが見つからない拡張機能の ID も problems に返す。
// 返す一覧は ext.ValidateManifests を通っている。
func LoadExtensions(dirs []string, cfg Config) (exts []Extension, problems []error) {
	var paths []string
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			problems = append(problems, &Error{Path: d, Msg: "拡張機能のディレクトリを読めない: " + err.Error()})
		}
		// ReadDir は誤りがあっても読めた分を返すので、それは使う。
		for _, e := range entries {
			p := filepath.Join(d, e.Name(), ManifestFileName)
			if _, err := os.Stat(p); err == nil {
				paths = append(paths, p)
			}
		}
	}

	from := map[string]string{}
	for _, p := range paths {
		m, warns, err := LoadManifest(p)
		problems = append(problems, warns...)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if prev, ok := from[m.ID]; ok {
			problems = append(problems, &Error{Path: p, Msg: fmt.Sprintf("拡張機能の ID %s が %s と重なるので、こちらを使わない", m.ID, prev)})
			continue
		}
		from[m.ID] = p
		if cfg.ExtensionDisabled[m.ID] {
			// 利用者が使わないと決めた拡張機能は、一覧に入れない。
			// from には入れたので、同じ ID の後のディレクトリの宣言ファイル（同梱のもの）も使わない。
			// req: FR-226
			continue
		}
		dir, err := filepath.Abs(filepath.Dir(p))
		if err != nil {
			problems = append(problems, &Error{Path: p, Msg: "宣言ファイルのディレクトリを絶対パスにできない: " + err.Error()})
			continue
		}
		settings := cfg.ExtensionSettings[m.ID]
		acts, missing := expandSettingsActivation(m.Activation, settings)
		for _, name := range missing {
			// 位置は、extensions.settings に書いた拡張機能の ID の位置を使う（値の中の位置は、RawMessage にした後は分からない）。
			e := &Error{Path: p}
			if pos, ok := cfg.extPos[m.ID]; ok {
				e = &Error{Path: pos.Path, Line: pos.Line, Column: pos.Column}
			}
			e.Msg = fmt.Sprintf("extensions.settings.%s.%s が無いか、文字列か文字列を含む配列でないので、拡張機能 %s の起動の契機を作らない", m.ID, name, m.ID)
			problems = append(problems, e)
		}
		acts = append(acts, cfg.ExtensionActivation[m.ID]...)
		m.Activation = acts
		exts = append(exts, Extension{Manifest: m, Dir: dir, Settings: settings})
	}

	ids := make([]string, 0, len(cfg.extPos))
	for id := range cfg.extPos {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if _, ok := from[id]; !ok {
			problems = append(problems, cfg.extPos[id])
		}
	}

	ms := make([]ext.Manifest, len(exts))
	for i := range exts {
		ms[i] = exts[i].Manifest
	}
	// 各宣言ファイルは LoadManifest で検査し、ID の重なりは除き、足した契機は filetype だけなので、ここで誤りは出ない。
	// 警告は LoadManifest で返したので捨てる。
	if _, err := ext.ValidateManifests(ms); err != nil {
		return nil, append(problems, err)
	}
	return exts, problems
}

// expandSettingsActivation は、acts の ext.ActivationSettingsFileType の契機のそれぞれの直後に、
// settings の中の Pattern（JSON Pointer）が指すファイルの種類の名前から作った ext.ActivationFileType の契機を足した列を返す。
//
// 元の契機は残す。本体（internal/exthost）は ext.ActivationSettingsFileType の契機で起動しないので、残しても働きは変わらず、
// 足す契機が 1 つも無いときに activation が空になって宣言ファイルの検査に落ちることを避けられる。
// 同じ名前は 1 回だけ足し、空の名前は足さない。
// settings が無いか JSON として読めないとき、Pattern の指す値が無いか文字列とその配列でないときは、何も足さない。
// 設定の中身の誤りを知らせるのは、設定を解釈する拡張機能である。
// ただし、Pattern の "*" が当たった項目の下に、その先の値が無いか、文字列か文字列を含む配列でないとき（[] や [3] を含む）は、
// その場所の名前（点で区切ったもの）を missing に返す。項目の名前を誤ると（fileTypes を filetypes と書くなど）、
// 契機が作られず拡張機能が起動しないので、拡張機能からは知らせられないためである（PR #282 のレビュー）。
func expandSettingsActivation(acts []ext.Activation, settings json.RawMessage) (out []ext.Activation, missing []string) {
	if !slices.ContainsFunc(acts, func(a ext.Activation) bool { return a.On == ext.ActivationSettingsFileType }) {
		return acts, nil
	}
	var root any
	if len(settings) > 0 {
		if err := json.Unmarshal(settings, &root); err != nil {
			root = nil
		}
	}
	out = make([]ext.Activation, 0, len(acts))
	seen := map[string]bool{}
	for _, a := range acts {
		out = append(out, a)
		if a.On != ext.ActivationSettingsFileType || root == nil {
			continue
		}
		var w pointerWalk
		w.walk(root, a.Pattern, "", false)
		missing = append(missing, w.missing...)
		for _, ft := range w.found {
			if ft == "" || seen[ft] {
				continue
			}
			seen[ft] = true
			out = append(out, ext.Activation{On: ext.ActivationFileType, Pattern: ft})
		}
	}
	return out, missing
}

// pointerStrings は、v の中で JSON Pointer ptr が指す値の文字列を返す（ext.ActivationSettingsFileType の doc）。
// 参照トークン "*" は、オブジェクトのすべての項目（名前の順）か配列のすべての要素に当たる。
// 指した値が文字列ならその 1 つ、配列ならその中の文字列のすべてを返し、それ以外の値は除く。
func pointerStrings(v any, ptr string) []string {
	var w pointerWalk
	w.walk(v, ptr, "", false)
	return w.found
}

// pointerWalk は、JSON Pointer で値を辿った結果である。
type pointerWalk struct {
	// found は、指した値の文字列である。
	found []string
	// missing は、"*" が当たった項目の下で、その先の値が無いか、文字列か文字列を含む配列でない場所の、点で区切った名前である。
	// 利用者が設定の項目の名前を誤ったとき（fileTypes を filetypes と書いたなど）に知らせるのに使う。
	missing []string
}

// walk は、v（at の位置の値）から ptr を辿る。wild は、ここまでに "*" が当たったかどうかである。
func (w *pointerWalk) walk(v any, ptr, at string, wild bool) {
	rest, ok := strings.CutPrefix(ptr, "/")
	if !ok {
		if ptr != "" {
			return
		}
		switch x := v.(type) {
		case string:
			w.found = append(w.found, x)
			return
		case []any:
			n := 0
			for _, e := range x {
				if s, ok := e.(string); ok {
					w.found = append(w.found, s)
					n++
				}
			}
			// 文字列を 1 つも含まない配列（[] や [3]）も、契機を 1 つも作らないので知らせる。
			if n == 0 && wild {
				w.missing = append(w.missing, at)
			}
			return
		}
		if wild {
			w.missing = append(w.missing, at)
		}
		return
	}
	tok, next, found := strings.Cut(rest, "/")
	if found {
		next = "/" + next
	}
	tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
	type child struct {
		name string
		v    any
	}
	var children []child
	switch x := v.(type) {
	case map[string]any:
		if tok == "*" {
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				children = append(children, child{k, x[k]})
			}
		} else if c, ok := x[tok]; ok {
			children = append(children, child{tok, c})
		}
	case []any:
		if tok == "*" {
			for i, c := range x {
				children = append(children, child{strconv.Itoa(i), c})
			}
		} else if i, err := strconv.Atoi(tok); err == nil && i >= 0 && i < len(x) && tok == strconv.Itoa(i) {
			children = append(children, child{tok, x[i]})
		}
	}
	if len(children) == 0 && tok != "*" && wild {
		w.missing = append(w.missing, joinName(at, tok))
	}
	for _, c := range children {
		w.walk(c.v, next, joinName(at, c.name), wild || tok == "*")
	}
}

// joinName は、点で区切った名前 at の後ろに name を足す。
func joinName(at, name string) string {
	if at == "" {
		return name
	}
	return at + "." + name
}
