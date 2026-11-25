package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"

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
}

// readLaunchJSON は、folder の .vscode/launch.json を JSONC として読む（hujson で JSON に直してから UseNumber で読む）。
// version は読まない。compounds は候補にしない。presentation.hidden が真の構成は候補から外す。
//
// req: FR-281
func readLaunchJSON(folder string) (*launchFile, error) {
	path := filepath.Join(folder, launchJSONPath)
	data, err := os.ReadFile(path) //nolint:gosec // 利用者のワークスペースの launch.json を読む
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
	if c, ok := top["compounds"]; ok {
		if arr, ok := c.([]any); !ok || len(arr) > 0 {
			lf.hasCompounds = true
		}
	}
	raw, ok := top["configurations"]
	if !ok || raw == nil {
		return lf, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: configurations is not an array", path)
	}
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
	}
	return lf, nil
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
