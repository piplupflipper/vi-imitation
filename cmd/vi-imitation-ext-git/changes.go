package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// 比べる元との差分があるファイルの一覧（git.changes と git.changesHead。FR-250〜FR-252。基本設計 6.9）。
//
// 比べる元は 1 つのコミットで、一覧はそのコミットの木と作業ツリーの差分である（git diff <コミット>）。
// 作業ツリーの側を開くファイルにするので、見比べの右の側はコミットでなく作業ツリーのファイルになる
// （diffview.nvim の --imply-local に当たる。オーナーの nvim の gitlab.nvim の設定が imply_local = true で開く）。
// 索引（インデックス）に足しただけの変更も、作業ツリーにあるので一覧に入る。

// defaultBases は、git.changes を引数なしで打ったときに、HEAD との merge-base を取る相手の候補である。前から順に、在るものを使う。
// origin/HEAD は、clone したときの既定のブランチ（origin/main など）を指す。
var defaultBases = []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"}

// comparison は、比べる元と、一覧を作るリポジトリである。
type comparison struct {
	// root は、作業ツリーの根の絶対パスである（git rev-parse --show-toplevel。worktree ではその worktree の根）。
	root string
	// rev は、比べる元のコミットのオブジェクト名（40 桁か 64 桁の 16 進）である。
	rev string
	// label は、比べる元の表示の名前である。一覧の見出しと、比べる元の版のバッファの名前（<label>:<パス>）に使う。
	label string
}

// resolveBase は、ディレクトリ dir を含む作業ツリーで、比べる元の指定 spec を 1 つのコミットに解く。
// 誤りの文言は、画面のメッセージの行に出せる日本語である。
//
// spec の書き方は次のとおりである（基本設計 6.9 の表）。
//
//   - 空: defaultBases の最初に在るものと HEAD の merge-base（origin/main...HEAD に当たる）
//   - <rev>...HEAD と <rev>...: <rev> と HEAD の merge-base
//   - それ以外: <rev> のコミット（HEAD、ブランチ、タグ、コミットのオブジェクト名）
//
// 右の側は作業ツリーのファイルなので、<rev>...<HEAD でないもの> と、2 点の範囲（<rev>..<rev>）は誤りにする。
//
// req: FR-250, FR-252
func resolveBase(ctx context.Context, dir, spec string) (comparison, error) {
	if err := lookGit(); err != nil {
		return comparison{}, err
	}
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return comparison{}, &notWorkTreeError{dir: dir, err: err}
	}
	root := strings.TrimSpace(string(top))
	if _, err = revParse(ctx, root, "HEAD"); err != nil {
		return comparison{}, errors.New("HEAD のコミットがありません（まだ 1 つもコミットしていないリポジトリです）")
	}
	cmp := comparison{root: root}
	spec = strings.TrimSpace(spec)
	switch {
	case strings.ContainsAny(spec, " \t"):
		return comparison{}, fmt.Errorf("比べる元は 1 つだけ書きます: %s", spec)
	case strings.HasPrefix(spec, "-"):
		return comparison{}, fmt.Errorf("比べる元 %s は - で始められません", spec)
	case spec == "":
		left, label, ok := defaultBase(ctx, root)
		if !ok {
			return comparison{}, fmt.Errorf("比べる元のブランチ（%s）が見つかりません。:git.changes <比べる元> で指定してください", strings.Join(defaultBases, "、"))
		}
		return mergeBase(ctx, cmp, left, label)
	case strings.Contains(spec, "..."):
		left, right, _ := strings.Cut(spec, "...")
		if right != "" && right != "HEAD" {
			return comparison{}, fmt.Errorf("%s の右の側は HEAD だけにできます（見比べの右の側は作業ツリーのファイルです）", spec)
		}
		if left == "" {
			return comparison{}, fmt.Errorf("%s の左の側がありません", spec)
		}
		return mergeBase(ctx, cmp, left, left)
	case strings.Contains(spec, ".."):
		return comparison{}, fmt.Errorf("範囲 %s は使えません（比べる元を 1 つのコミットか <ブランチ>...HEAD で書きます）", spec)
	}
	rev, err := revParse(ctx, root, spec)
	if err != nil {
		return comparison{}, fmt.Errorf("比べる元 %s のコミットが見つかりません", spec)
	}
	cmp.rev, cmp.label = rev, spec
	return cmp, nil
}

// notWorkTreeError は、ディレクトリが git の作業ツリーの中でないこと（リポジトリの外、.git の中、bare のリポジトリ）を表す。
// 呼び出し側は、バッファのディレクトリでこの誤りになったら、作業ディレクトリで試し直す。
type notWorkTreeError struct {
	dir string
	err error
}

func (e *notWorkTreeError) Error() string {
	return fmt.Sprintf("%s は git の作業ツリーの中ではありません（%v）", e.dir, e.err)
}

func (e *notWorkTreeError) Unwrap() error { return e.err }

// defaultBase は、defaultBases の最初に在るものと、その表示の名前を返す。
// origin/HEAD は、指しているブランチの名前（origin/main など）で表示する。
func defaultBase(ctx context.Context, root string) (rev, label string, ok bool) {
	for _, c := range defaultBases {
		if _, err := revParse(ctx, root, c); err != nil {
			continue
		}
		label = c
		if c == "origin/HEAD" {
			if out, err := git(ctx, root, "rev-parse", "--abbrev-ref", c); err == nil {
				if name := strings.TrimSpace(string(out)); name != "" && name != c {
					label = name
				}
			}
		}
		return c, label, true
	}
	return "", "", false
}

// mergeBase は、left と HEAD の merge-base を cmp の比べる元にする。表示の名前は <label>...HEAD である。
func mergeBase(ctx context.Context, cmp comparison, left, label string) (comparison, error) {
	if _, err := revParse(ctx, cmp.root, left); err != nil {
		return comparison{}, fmt.Errorf("比べる元 %s のコミットが見つかりません", left)
	}
	out, err := git(ctx, cmp.root, "merge-base", left, "HEAD")
	if err != nil {
		// 浅いクローンでは、履歴を切った所より前の共通の祖先が見えない。原因が分かるように案内する（PR #382 のレビューの推奨 1）。
		if sh, serr := git(ctx, cmp.root, "rev-parse", "--is-shallow-repository"); serr == nil && strings.TrimSpace(string(sh)) == "true" {
			return comparison{}, fmt.Errorf("%s と HEAD の共通の祖先が見つかりません（浅いクローンなので、git fetch --unshallow で履歴を取ってから打ち直してください）", label)
		}
		return comparison{}, fmt.Errorf("%s と HEAD の共通の祖先がありません", label)
	}
	cmp.rev, cmp.label = strings.TrimSpace(string(out)), label+"...HEAD"
	return cmp, nil
}

// revParse は、rev をコミットのオブジェクト名に解く。コミットでなければ（無いもの、木やブロブ）誤りを返す。
func revParse(ctx context.Context, root, rev string) (string, error) {
	out, err := git(ctx, root, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// changeKind は、ファイルの変更の種類である。一覧の行の先頭に出す 1 文字で、git diff --name-status の文字に合わせる。
type changeKind byte

// 変更の種類。
const (
	kindAdded     changeKind = 'A'
	kindModified  changeKind = 'M'
	kindDeleted   changeKind = 'D'
	kindRenamed   changeKind = 'R'
	kindType      changeKind = 'T'
	kindUntracked changeKind = '?'
)

// gitlinkMode と symlinkMode は、git diff --raw のモードの、サブモジュールとシンボリックリンクである。
const (
	gitlinkMode = "160000"
	symlinkMode = "120000"
)

// change は、比べる元と作業ツリーで違うファイル 1 つである。
type change struct {
	kind changeKind
	// path は、作業ツリーの側の、作業ツリーの根からの相対パスである。消したファイルでは、比べる元の側のパスである。
	path string
	// old は、比べる元の側の、作業ツリーの根からの相対パスである。比べる元に無いファイル（足した、未追跡）では空である。
	old string
	// submodule は、どちらかの側がサブモジュールであることを表す。
	submodule bool
	// symlink は、どちらかの側がシンボリックリンクであることを表す。
	symlink bool
	// nested は、追跡していない入れ子のリポジトリ（git ls-files --others が末尾に / を付けて出すディレクトリ）であることを表す。
	nested bool
	// oldMode と newMode は、両側にあるファイルのモードが違うとき（chmod +x など）の、比べる元と作業ツリーのモードである。同じなら空である。
	oldMode, newMode string
}

// label は、一覧の行の表示の文字列である。先頭の 3 バイトは変更の種類と空白で、その後が絞り込みに使うパスである（labelPrefix）。
// モードが変わったファイルは、末尾に「（モード 100644 -> 100755）」を足す。中身が同じでも、見比べて違いが無い理由が分かるようにする
// （PR #382 のレビューの推奨 3）。
func (c change) label() string {
	p := c.path
	if c.kind == kindRenamed {
		p = c.old + " -> " + c.path
	}
	if c.oldMode != "" {
		p += "（モード " + c.oldMode + " -> " + c.newMode + "）"
	}
	return string(c.kind) + "  " + p
}

// labelPrefix は、label の先頭の、変更の種類と空白のバイト数である。
const labelPrefix = 3

// listChanges は、cmp の比べる元のコミットと作業ツリーの間で違うファイルを、パスの順に返す。
// 追跡していないファイル（.gitignore などで無視したものを除く）も、比べる元に無いファイルとして入れる。
//
// git diff は、利用者の git の設定（diff.relative、diff.external、color.diff）の影響を受けないよう、--no-relative、--no-ext-diff、
// --no-color を付ける。名前の変更は -M で見つける（git の既定の 50% の似ている度合い）。
//
// req: FR-250
func listChanges(ctx context.Context, cmp comparison) ([]change, error) {
	raw, err := git(ctx, cmp.root, "diff", "--raw", "-z", "-M", "--no-relative", "--no-ext-diff", "--no-color", cmp.rev, "--")
	if err != nil {
		return nil, fmt.Errorf("%s との差分を読めません（%w）", cmp.label, err)
	}
	out, err := parseRaw(raw)
	if err != nil {
		return nil, fmt.Errorf("%s との差分を読めません（%w）", cmp.label, err)
	}
	others, err := git(ctx, cmp.root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("追跡していないファイルを読めません（%w）", err)
	}
	for p := range bytes.SplitSeq(others, []byte{0}) {
		if len(p) > 0 {
			// 入れ子のリポジトリ（サブモジュールとして登録していないもの）は、末尾に / を付けたディレクトリとして出る。
			out = append(out, change{kind: kindUntracked, path: string(p), nested: bytes.HasSuffix(p, []byte("/"))})
		}
	}
	slices.SortStableFunc(out, func(a, b change) int { return strings.Compare(a.path, b.path) })
	return out, nil
}

// parseRaw は、git diff --raw -z の出力を読む。1 つの変更は、「:<元のモード> <先のモード> <元の名> <先の名> <状態>」と、
// パス（名前の変更とコピーでは元と先の 2 つ）を、どれも NUL で区切って並べたものである（git-diff(1) の Raw output format）。
func parseRaw(raw []byte) ([]change, error) {
	// 出力は NUL で終わるので、最後の NUL の後の空を除いてから区切る。
	fields := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	var out []change
	for i := 0; i < len(fields); {
		head := string(fields[i])
		i++
		if head == "" {
			continue
		}
		meta := strings.Fields(strings.TrimPrefix(head, ":"))
		if len(meta) != 5 || !strings.HasPrefix(head, ":") {
			return nil, fmt.Errorf("git diff --raw の行を読めない: %q", head)
		}
		status := meta[4]
		two := status[0] == 'R' || status[0] == 'C'
		need := 1
		if two {
			need = 2
		}
		if i+need > len(fields) {
			return nil, fmt.Errorf("git diff --raw のパスが足りない: %q", head)
		}
		c := change{
			submodule: meta[0] == gitlinkMode || meta[1] == gitlinkMode,
			symlink:   meta[0] == symlinkMode || meta[1] == symlinkMode,
		}
		switch status[0] {
		case 'A':
			c.kind, c.path = kindAdded, string(fields[i])
		case 'D':
			c.kind, c.path, c.old = kindDeleted, string(fields[i]), string(fields[i])
		case 'R':
			c.kind, c.old, c.path = kindRenamed, string(fields[i]), string(fields[i+1])
		case 'C':
			// -C を付けないので出ないはずだが、出たら（diff.renames = copies）、元のファイルと見比べる足したファイルとして扱う。
			c.kind, c.old, c.path = kindAdded, string(fields[i]), string(fields[i+1])
		case 'T':
			c.kind, c.path, c.old = kindType, string(fields[i]), string(fields[i])
		default:
			// M と、索引の衝突（U）、不明（X）は、両側にあるファイルの変更として扱う。
			c.kind, c.path, c.old = kindModified, string(fields[i]), string(fields[i])
		}
		if c.kind == kindModified || c.kind == kindRenamed {
			if meta[0] != meta[1] && meta[0] != "000000" && meta[1] != "000000" {
				c.oldMode, c.newMode = meta[0], meta[1]
			}
		}
		i += need
		out = append(out, c)
	}
	return out, nil
}

// readBlob は、cmp の比べる元のコミットの、作業ツリーの根からの相対パス name のファイルの中身を、末尾の改行を 1 つ除いて返す。
// readHead と同じく、作業ツリーへ取り出すときと同じ変換（--filters）を通す。誤りの文言は画面に出せる日本語である。
func readBlob(ctx context.Context, cmp comparison, name string) (string, error) {
	out, err := git(ctx, cmp.root, "cat-file", "--filters", cmp.rev+":"+name)
	if err != nil {
		return "", fmt.Errorf("%s の %s を読めません（%w）", cmp.label, name, err)
	}
	if !utf8.Valid(out) {
		return "", fmt.Errorf("%s の %s は不正な UTF-8 を含むので開けません", cmp.label, name)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// absPath は、作業ツリーの根からの相対パス name の絶対パスである。
func (cmp comparison) absPath(name string) string {
	return filepath.Join(cmp.root, filepath.FromSlash(name))
}
