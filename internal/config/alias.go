package config

import (
	"math"

	"github.com/tailscale/hujson"
)

// CommandAlias は、設定の commandAliases の 1 件で、Ex コマンドの名前を付け替えるだけの別名である（基本設計 7.3。FR-150）。
type CommandAlias struct {
	// Name は、別名の名前（commandAliases のキー）である。
	Name string
	// Min は、省略できる最短の長さ（min の値）である。min を書かなければ Name の長さ（省略できない）にする。
	Min int
	// Command は、付け替える先のコマンドの名前（command の値）である。本体の Ex コマンドの名前か、拡張機能のコマンドの ID を書く。
	Command string
}

// commandAliases は、commandAliases の値を読む（FR-150）。
// 値はオブジェクトで、別名の名前から { "command": "<付け替える先>", "min": <省略できる最短の長さ> } への対応である。
// command は空でない文字列で、書かなければ誤りにする。min は 1 以上の整数で、省けば名前の長さにする。
// 形の誤り（知らない項目、型の違い）のある別名は使わない。
// 名前の文字、min と名前の長さ、付け替える先が使えるか、ほかのコマンドと重ならないかは、ここでは確かめず、
// CheckCommandAliases で Ex コマンドの別名の表に足すときに確かめる。並びは書いた順である。
//
// req: FR-150
func (s *source) commandAliases(v *hujson.Value, cfg *Config) []CommandAlias {
	const path = "commandAliases"
	obj, ok := s.object(v, path)
	if !ok {
		return nil
	}
	out := []CommandAlias{}
	for _, e := range s.members(obj, path) {
		apath := join(path, e.name)
		aobj, ok := s.object(e.value, apath)
		if !ok {
			continue
		}
		a := CommandAlias{Name: e.name, Min: len(e.name)}
		found, bad := false, false
		for _, m := range s.members(aobj, apath) {
			switch m.name {
			case "command":
				str, ok := s.str(m.value, join(apath, m.name))
				switch {
				case !ok:
					bad = true
				case str == "":
					s.report(m.value.StartOffset, "%s: 付け替える先のコマンドの名前が空である", join(apath, m.name))
					bad = true
				default:
					a.Command, found = str, true
				}
			case "min":
				n, ok := s.posInt(m.value, join(apath, m.name), math.MaxInt32)
				if !ok {
					bad = true
					continue
				}
				a.Min = int(n)
			default:
				s.report(m.nameOff, "%s: 未知の項目 %s がある。この別名を使わない", apath, m.name)
				bad = true
			}
		}
		if !found && !bad {
			s.report(e.value.StartOffset, "%s: 付け替える先のコマンドの名前（command）が無い", apath)
		}
		if !found || bad {
			continue
		}
		out = append(out, a)
		cfg.aliasPos = append(cfg.aliasPos, s.at(e.nameOff, ""))
	}
	return out
}

// CheckCommandAliases は、CommandAliases の各別名を、書いた順に add で Ex コマンドの別名の表に足し、
// 足せなかった別名を除いて、位置と内容を誤りとして返す（FR-150）。
// 別名の表は internal/command/ex が持ち、付け替える先にできる拡張機能のコマンドは internal/editor が知る。
// このパッケージはどちらも import しないので、呼び出し側（cmd/vi-imitation）が Parse と LoadExtensions の後に、
// 足す関数（editor.CommandAliases）を渡して 1 回呼ぶ。
//
// req: FR-150
func (c *Config) CheckCommandAliases(add func(CommandAlias) error) []error {
	var errs []error
	var kept []CommandAlias
	var keptPos []*Error
	for i, a := range c.CommandAliases {
		var pos *Error // Parse を通さずに作った Config では、位置が無い
		if i < len(c.aliasPos) {
			pos = c.aliasPos[i]
		}
		if err := add(a); err != nil {
			if c.disabledOwner(a.Command) {
				// extensions.disabled で止めた拡張機能のコマンドへの別名は、知らせずに使わない（CheckKeymapCommands と同じ）。
				continue
			}
			errs = append(errs, pos.with("%s: 別名を使えない: %v", join("commandAliases", a.Name), err))
			continue
		}
		kept = append(kept, a)
		keptPos = append(keptPos, pos)
	}
	c.CommandAliases, c.aliasPos = kept, keptPos
	SortErrors(errs)
	return errs
}
