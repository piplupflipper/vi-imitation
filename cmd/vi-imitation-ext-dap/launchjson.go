package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/piplupflipper/vi-imitation/ext/expand"
	"github.com/piplupflipper/vi-imitation/ext/project"
)

// この節は、.vscode/launch.json の読み方である（基本設計 6.4 の「.vscode/launch.json の読み方」。FR-281）。
// VS Code の文書「Debug configuration」（https://code.visualstudio.com/docs/debugtest/debugging-configuration）に合わせる。

// launchJSONPath は、ワークスペースの根から見た launch.json のパスである。
var launchJSONPath = filepath.Join(".vscode", "launch.json")

// findLaunchJSON は、start（バッファのファイルか、ファイルが無ければ作業ディレクトリ）から親へたどり、
// .vscode/launch.json を持つ最初のディレクトリ（ワークスペースの根）を返す（ext/project の Root）。
// start が Git の作業ツリーの中なら、Git の根より上へはたどらない（ホームディレクトリの .vscode などを拾わないため。仮）。
// 見つからなければ空を返す。
//
// req: FR-281
func findLaunchJSON(start string) (string, error) {
	if start == "" {
		return "", nil
	}
	root, err := project.Root(start, []string{launchJSONPath})
	if errors.Is(err, project.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if git, gerr := project.GitRoot(start); gerr == nil && !within(root, git) {
		return "", nil
	}
	return root, nil
}

// within は、dir が base かその下にあるかを返す。
func within(dir, base string) bool {
	rel, err := filepath.Rel(base, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// launchFile は、読んだ launch.json である。
type launchFile struct {
	// configurations は、候補にする構成（presentation.hidden が真のものを除く）を、書いた順に並べたものである。
	configurations []map[string]any
	// hasCompounds は、compounds を書いていることを表す。
	hasCompounds bool
	// inputs は、${input:ID} の問いの表である（FR-298）。
	inputs inputTable
	// written は、configurations と同じ並びの、構成ごとの利用者に聞く変数の書かれた順である（writtenVars）。
	written [][]expand.Var
}

// readLaunchJSON は、folder の .vscode/launch.json を JSONC として読む（hujson で JSON に直してから UseNumber で読む）。
// version は読まない。compounds は候補にしない。presentation.hidden が真の構成は候補から外す。inputs は ${input:ID} の問いの表にする。
//
// req: FR-281, FR-298
func readLaunchJSON(folder string) (*launchFile, error) {
	path := filepath.Join(folder, launchJSONPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	std, err := hujson.Standardize(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	v, err := decodeJSON(std)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: not an object", path)
	}
	lf := &launchFile{}
	if c, has := top["compounds"]; has {
		if arr, isArr := c.([]any); !isArr || len(arr) > 0 {
			lf.hasCompounds = true
		}
	}
	if lf.inputs, err = parseInputs(top["inputs"], false); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	raw, ok := top["configurations"]
	if !ok || raw == nil {
		return lf, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: configurations is not an array", path)
	}
	rawConfigs := configsRaw(std)
	for i, c := range arr {
		m, ok := c.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: configurations[%d] is not an object", path, i)
		}
		if p, ok := m["presentation"].(map[string]any); ok {
			if hidden, _ := p["hidden"].(bool); hidden {
				continue
			}
		}
		lf.configurations = append(lf.configurations, m)
		var w []expand.Var
		if i < len(rawConfigs) {
			w = writtenVars(rawConfigs[i])
		}
		lf.written = append(lf.written, w)
	}
	return lf, nil
}

// configsRaw は、JSON の data の最上位の configurations の配列の要素を、読む前の文字列のまま返す（writtenVars のため）。
// 読めなければ nil を返す（聞く順は展開の口の順になる）。
func configsRaw(data []byte) []json.RawMessage {
	var top struct {
		Configurations []json.RawMessage `json:"configurations"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil
	}
	return top.Configurations
}

// applyPlatform は、構成の linux の中のキーで、構成の同じキーを置き換える（文書の「Platform-specific properties」）。
// windows と osx は読み捨てる。type は OS ごとに変えられない（同じ節）ので当てず、扱えない項目として返す（ignorableKeys）。
// linux、windows、osx のキーは構成から除く。
//
// req: FR-281
func applyPlatform(cfg map[string]any) (linuxType bool) {
	if l, ok := cfg["linux"].(map[string]any); ok {
		for k, v := range l {
			if k == "type" {
				linuxType = true
				continue
			}
			cfg[k] = v
		}
	}
	delete(cfg, "linux")
	delete(cfg, "windows")
	delete(cfg, "osx")
	return linuxType
}
