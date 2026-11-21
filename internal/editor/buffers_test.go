package editor

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/internal/buffer"
	"github.com/piplupflipper/vi-imitation/internal/command"
	"github.com/piplupflipper/vi-imitation/internal/ui"
)

// numberedLines は、"<prefix> 1 foo" から "<prefix> n foo" までの n 行を返す。
func numberedLines(prefix string, n int, suffix string) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "%s %d%s\n", prefix, i, suffix)
	}
	return sb.String()
}

// jumpList は、Vim 9.1 の getjumplist() と同じく、整理（cleanupJumps）した後のジャンプリストを "名前:行.列" の列で返す。
// 行は 1 始まり、列は 0 始まりのバイト位置で、Vim の getjumplist() の lnum と col に当たる。
// カーソルの位置も同じ形で返す。イベントループの goroutine で呼ぶ。
func (e *Editor) jumpList() (list string, idx int, cur string) {
	e.cleanupJumps()
	pos := func(bs *bufferState, p buffer.Position) string {
		return fmt.Sprintf("%s:%d.%d", e.bufferName(bs), p.Line+1, p.Byte)
	}
	items := make([]string, 0, len(e.jumps))
	for _, j := range e.jumps {
		items = append(items, pos(e.findBuffer(j.buf), j.pos))
	}
	return strings.Join(items, " "), e.jumpIdx, pos(e.cur, e.cur.state.Cursor())
}

// 1 つのバッファの中のジャンプリスト。期待値は、手元の Vim 9.1 で、30 行のファイルの 1 行目の先頭から
// :normal でキーを打ち、getjumplist() とカーソルを読んで得た（vim -Nu NONE -i NONE -n --not-a-term。2026-11-16）。
func TestJumpListVim(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": numberedLines("line", 30, " foo")})
	a := func(lines ...string) string {
		for i, l := range lines {
			lines[i] = "a.txt:" + l
		}
		return strings.Join(lines, " ")
	}
	tests := []struct {
		keys string
		list string
		idx  int
		cur  string
	}{
		{"G", a("1.0"), 1, "30.0"},
		{"gg", a("1.0"), 1, "1.0"},
		{"5G", a("1.0"), 1, "5.0"},
		{"jjjgg", a("4.0"), 1, "1.0"},
		{"Ggg", a("1.0", "30.0"), 2, "1.0"},
		{"GggG", a("30.0", "1.0"), 2, "30.0"},
		{"/line 7<CR>", a("1.0"), 1, "7.0"},
		{"/line 7<CR>n", a("1.0", "7.0"), 2, "7.0"},
		// * はカーソルを動かさず、ジャンプリストにも記録しない（FR-139。Vim と違う）。次の一致へ移る n は、Vim の n と同じく記録する。
		{"*", "", 0, "1.0"},
		{"*n", a("1.0"), 1, "2.0"},
		{"%", "", 0, "1.0"},
		{"}", a("1.0"), 1, "30.10"},
		{":5<CR>", "", 0, "5.0"},
		{":5<CR>:s/foo/bar/<CR>", a("5.0"), 1, "5.0"},
		{":10s/foo/bar/<CR>", a("1.0"), 1, "10.0"},
		{":g/line 2/s/foo/x/<CR>", a("1.0"), 1, "29.0"},
		{":10d<CR>", a("1.0"), 1, "10.0"},
		{"5GdG", a("1.0", "5.0"), 2, "4.0"},
		{"d/line 7<CR>", a("1.0"), 1, "1.0"},
		{"yG", a("1.0"), 1, "1.0"},
		{"vG<Esc>", a("1.0"), 1, "30.0"},
		{"G<C-o>", a("1.0", "30.0"), 0, "1.0"},
		{"G<C-o><C-i>", a("1.0", "30.0"), 1, "30.0"},
		{"G<C-o><C-i><C-i>", a("1.0", "30.0"), 1, "30.0"},
		{"5G10G<C-o><C-o>", a("1.0", "5.0", "10.0"), 0, "1.0"},
		{"5G10G<C-o><C-o><C-i>", a("1.0", "5.0", "10.0"), 1, "5.0"},
		{"5G10G2<C-o>", a("1.0", "5.0", "10.0"), 0, "1.0"},
		{"5G10G3<C-o>", a("1.0", "5.0"), 2, "10.0"},
		{"5G10G15G<C-o><C-o>20G", a("1.0", "10.0", "15.0", "5.0"), 4, "20.0"},
		{"5G10G<C-o>jj<C-o>", a("1.0", "5.0", "10.0"), 0, "1.0"},
		{"5G10G5G", a("1.0", "5.0", "10.0"), 3, "5.0"},
		{"5G10G5G<C-o>", a("1.0", "10.0", "5.0"), 1, "10.0"},
		{"lllG<C-o>", a("1.3", "30.0"), 0, "1.3"},
		{"<C-o>", "", 0, "1.0"},
		{"G<C-i>", a("1.0"), 1, "30.0"},
		{"5G<C-o><C-o>", a("1.0", "5.0"), 0, "1.0"},
		// 行の削除と挿入に合わせて行をずらし、消した行の中の項目は消した範囲の最初の行へ寄せる。
		{"10G20G1G3dd", a("1.0", "7.0", "17.0"), 3, "1.0"},
		{"10G20G9G3dd", a("1.0", "9.0", "17.0"), 3, "9.0"},
		{"10G20G1GOnew<Esc>", a("2.0", "11.0", "21.0"), 3, "1.2"},
		// 3 行目の 3 文字目（jjll）から。
		{"jjll:g/zzz/d<CR>", "", 0, "3.2"},
		{"jjll:g/line 2/d<CR>", a("2.2"), 1, "19.0"},
		{"jjll:v/line/d<CR>", "", 0, "3.2"},
		{"jjll:5,6s/zzz/x/e<CR>", "", 0, "3.2"},
		{"jjll:d<CR>", a("3.2"), 1, "3.0"},
		{"jjll:50d<CR>", "", 0, "3.2"},
		{"jjll/zzz<CR>", "", 0, "3.2"},
		{"jjll{", a("3.2"), 1, "1.0"},
		{"jjll20%", a("3.2"), 1, "6.0"},
		{"jjlldgg", a("1.2"), 1, "1.0"},
		{"jjlld}", a("3.2"), 1, "3.1"},
	}
	for _, tt := range tests {
		t.Run(tt.keys, func(t *testing.T) {
			h := start(t, Config{WorkDir: dir}, "a.txt")
			h.keys(tt.keys)
			h.settle()
			h.e.do(func() {
				list, idx, cur := h.e.jumpList()
				if list != tt.list || idx != tt.idx || cur != "a.txt:"+tt.cur {
					t.Errorf("jumps = [%s] idx=%d cur=%s, want [%s] idx=%d cur=a.txt:%s", list, idx, cur, tt.list, tt.idx, tt.cur)
				}
			})
		})
	}
}

// バッファの切り替え（:e、:b、:bn、:bp、<C-^>）と、バッファをまたぐジャンプリスト。
// 期待値は、手元の Vim 9.1 で同じ順にキーを打って得た（2026-11-16）。:ls の印は、バッファを閉じずに持つ（隠れたバッファの h）点だけが違う。
// 未保存の変更の後の手順は、段階 3 のタブ（基本設計 4.4）に合わせて書き直した（#215）。
//
// req: FR-08, FR-64
func TestBufferSwitchVim(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a.txt": numberedLines("line", 30, " foo"),
		"b.txt": numberedLines("bline", 10, ""),
		"c.txt": numberedLines("cline", 10, ""),
	})
	h := start(t, Config{WorkDir: dir}, "a.txt")
	steps := []struct {
		keys string
		list string // 空なら確かめない
		cur  string
		alt  string
		msg  string // メッセージに含まれる文字列。空なら確かめない
	}{
		{keys: "5G:e b.txt<CR>", list: "a.txt:1.0 a.txt:5.0", cur: "b.txt:1.0", alt: "a.txt"},
		{keys: "3G:e c.txt<CR>", list: "a.txt:1.0 a.txt:5.0 b.txt:1.0 b.txt:3.0", cur: "c.txt:1.0", alt: "b.txt"},
		{keys: "jj:b1<CR>", list: "a.txt:1.0 a.txt:5.0 b.txt:1.0 b.txt:3.0 c.txt:3.0", cur: "a.txt:5.0", alt: "c.txt"},
		{keys: ":bn<CR>", list: "a.txt:1.0 b.txt:1.0 b.txt:3.0 c.txt:3.0 a.txt:5.0", cur: "b.txt:3.0", alt: "a.txt"},
		{keys: ":bn<CR>", list: "a.txt:1.0 b.txt:1.0 c.txt:3.0 a.txt:5.0 b.txt:3.0", cur: "c.txt:3.0", alt: "b.txt"},
		{keys: ":bn<CR>", list: "a.txt:1.0 b.txt:1.0 a.txt:5.0 b.txt:3.0 c.txt:3.0", cur: "a.txt:5.0", alt: "c.txt"},
		{keys: ":bp<CR>", list: "a.txt:1.0 b.txt:1.0 b.txt:3.0 c.txt:3.0 a.txt:5.0", cur: "c.txt:3.0", alt: "a.txt"},
		{keys: ":bn 2<CR>", list: "a.txt:1.0 b.txt:1.0 b.txt:3.0 a.txt:5.0 c.txt:3.0", cur: "b.txt:3.0", alt: "c.txt"},
		{keys: ":bN<CR>", list: "a.txt:1.0 b.txt:1.0 a.txt:5.0 c.txt:3.0 b.txt:3.0", cur: "a.txt:5.0", alt: "b.txt"},
		{keys: "<C-^>", list: "a.txt:1.0 b.txt:1.0 c.txt:3.0 b.txt:3.0 a.txt:5.0", cur: "b.txt:3.0", alt: "a.txt"},
		{keys: "<C-^>", list: "a.txt:1.0 b.txt:1.0 c.txt:3.0 a.txt:5.0 b.txt:3.0", cur: "a.txt:5.0", alt: "b.txt"},
		{keys: "3<C-^>", list: "a.txt:1.0 b.txt:1.0 c.txt:3.0 b.txt:3.0 a.txt:5.0", cur: "c.txt:3.0", alt: "a.txt"},
		// 同じバッファへの切り替えは何もしない。
		{keys: "3<C-^>:b3<CR>:b<CR>", list: "a.txt:1.0 b.txt:1.0 c.txt:3.0 b.txt:3.0 a.txt:5.0", cur: "c.txt:3.0", alt: "a.txt"},
		// :e の読み直しもジャンプとして記録する。
		{keys: ":e<CR>", list: "a.txt:1.0 b.txt:1.0 b.txt:3.0 a.txt:5.0 c.txt:3.0", cur: "c.txt:3.0", alt: "a.txt"},
		{keys: ":ls<CR>", cur: "c.txt:3.0", alt: "a.txt", msg: strings.Join([]string{
			`  1 #h   "a.txt"                        line 5`,
			`  2  h   "b.txt"                        line 3`,
			`  3 %a   "c.txt"                        line 3`,
		}, "\n")},
		{keys: "<CR>:b 9<CR>", cur: "c.txt:3.0", alt: "a.txt", msg: "E86"},
		{keys: "9<C-^>", cur: "c.txt:3.0", alt: "a.txt", msg: "E92"},
		{keys: ":b b.t<CR>", cur: "b.txt:3.0", alt: "c.txt"},
		{keys: ":b txt<CR>", cur: "b.txt:3.0", alt: "c.txt", msg: "E93"},
		{keys: ":b zz<CR>", cur: "b.txt:3.0", alt: "c.txt", msg: "E94"},
		{keys: ":b a.txt<CR>", list: "a.txt:1.0 b.txt:1.0 a.txt:5.0 c.txt:3.0 b.txt:3.0", cur: "a.txt:5.0", alt: "b.txt"},
		{keys: ":b 1 2<CR>", cur: "a.txt:5.0", alt: "b.txt", msg: "E488"},
		{keys: ":3b<CR>", cur: "a.txt:5.0", alt: "b.txt", msg: "E481"},
		// 段階 3 では隠れたバッファが無い（離れたタブもタブの行に残る）ので、未保存の変更があっても E37 で断らずに移る
		// （基本設計 4.4 の段階 3 のタブとバッファ。Vim 9.1 の :bn! と同じ結果）。
		{keys: "ix<Esc>:bn<CR>", list: "a.txt:1.0 b.txt:1.0 c.txt:3.0 b.txt:3.0 a.txt:5.0", cur: "b.txt:3.0", alt: "a.txt"},
		{keys: ":ls<CR>", cur: "b.txt:3.0", alt: "a.txt", msg: strings.Join([]string{
			`  1 #h + "a.txt"                        line 5`,
			`  2 %a   "b.txt"                        line 3`,
			`  3  h   "c.txt"                        line 3`,
		}, "\n")},
		// :q はカーソルのあるタブとそのバッファを閉じ、裏のタブへ移る（FR-64）。
		{keys: "<CR>:q<CR>", list: "a.txt:1.0 c.txt:3.0 a.txt:5.0", cur: "a.txt:5.0", alt: ""},
		// :qa は、未保存のタブがあれば、Vim 9.1 と同じくその名前を出してそのタブへ移る。
		{keys: ":b3<CR>:qa<CR>", cur: "a.txt:5.0", alt: "c.txt", msg: `E162: バッファ "a.txt" の変更は保存されていません`},
		{keys: ":qa<CR>", cur: "a.txt:5.0", alt: "c.txt", msg: "E37"},
	}
	for _, st := range steps {
		h.keys(st.keys)
		h.settle()
		h.e.do(func() {
			list, _, cur := h.e.jumpList()
			if st.list != "" && list != st.list {
				t.Errorf("%s: jumps = [%s], want [%s]", st.keys, list, st.list)
			}
			if cur != st.cur {
				t.Errorf("%s: cur = %s, want %s", st.keys, cur, st.cur)
			}
			alt := ""
			if h.e.alt != nil {
				alt = h.e.bufferName(h.e.alt)
			}
			if alt != st.alt {
				t.Errorf("%s: alt = %s, want %s", st.keys, alt, st.alt)
			}
			if st.msg != "" && !strings.Contains(h.e.message.Text, st.msg) {
				t.Errorf("%s: message = %q, want it to contain %q", st.keys, h.e.message.Text, st.msg)
			}
		})
	}
}

// :b と :bd の引数の "\ " は名前の空白で、末尾の "\ " も除かない（#354）。"\ " の後に続けて打った空白は除く。
// 期待値は、同じ名前のバッファを置いた Vim 9.1 と Neovim 0.12.5 で同じコマンドを打って得た（2026-11-22。両者は同じ結果だった）。
// 補った名前（"\" で逃がした形。FR-195）を <CR> で開くと、名前の末尾が空白でもそのバッファへ移る（Vim と Neovim も同じ）。
//
// req: FR-08, FR-195
func TestBufferArgEscapedSpace(t *testing.T) {
	names := []string{"sp a.txt", "spx.txt", "a|b.txt", "tail ", "other.txt"}
	files := map[string]string{}
	for _, n := range names {
		files[n] = n + "\n"
	}
	h := start(t, Config{WorkDir: writeFiles(t, files)}, names...)
	listed := func() string {
		var s []string
		h.e.do(func() {
			for _, bs := range h.e.bufs {
				s = append(s, "["+h.e.bufferName(bs)+"]")
			}
		})
		return strings.Join(s, ",")
	}
	all := "[sp a.txt],[spx.txt],[a|b.txt],[tail ],[other.txt]"
	for _, st := range []struct {
		keys   string
		cur    string // 移った先のバッファの中身（名前と同じ）
		msg    string // メッセージに含まれる文字列。空ならメッセージが無いこと
		listed string // 空なら all
	}{
		{keys: `:b sp\ <CR>`, cur: "sp a.txt"},
		{keys: `:b   sp\ <CR>`, cur: "sp a.txt"},
		{keys: `:b sp\  <CR>`, cur: "sp a.txt"},
		{keys: `:b sp\ a.txt   <CR>`, cur: "sp a.txt"},
		{keys: `:b sp\ a<CR>`, cur: "sp a.txt"},
		{keys: `:b sp a<CR>`, cur: "sp a.txt"},
		{keys: `:b! sp\ <CR>`, cur: "sp a.txt"},
		{keys: `:b sp\ |ls<CR>`, cur: "sp a.txt", msg: `"tail "`},
		{keys: `:b sp\ \ <CR>`, cur: "other.txt", msg: "E94"},
		{keys: `:b sp\\ <CR>`, cur: "other.txt", msg: "E94"},
		{keys: `:b a\|b<CR>`, cur: "a|b.txt"},
		{keys: `:b tail\ <CR>`, cur: "tail "},
		{keys: `:b tail\  <CR>`, cur: "tail "},
		{keys: `:b tail <CR>`, cur: "tail "},
		// 補った候補を入れる形と、:b の読み方が往復する。候補が 2 つなので、1 つ目の <CR> は選んだ候補で決める（#363）。
		{keys: `:b sp<Tab><CR><CR>`, cur: "sp a.txt"},
		{keys: `:b tai<Tab><CR>`, cur: "tail "},
		{keys: `:bd sp\ |ls<CR>`, cur: "other.txt", msg: `"spx.txt"`, listed: "[spx.txt],[a|b.txt],[tail ],[other.txt]"},
		{keys: `:bd! tail\  <CR>`, cur: "other.txt", listed: "[spx.txt],[a|b.txt],[other.txt]"},
		{keys: `:bd a\|b<CR>`, cur: "other.txt", listed: "[spx.txt],[other.txt]"},
	} {
		h.keys(`<Esc>:b other<CR>`)
		h.settle()
		h.e.do(func() { h.e.message = infoMessage("") })
		h.keys(st.keys)
		h.settle()
		if got := h.text(); got != st.cur {
			t.Errorf("%s: バッファ = %q, want %q", st.keys, got, st.cur)
		}
		if got := h.message(); (st.msg == "" && got != "") || !strings.Contains(got, st.msg) {
			t.Errorf("%s: メッセージ = %q, want %q", st.keys, got, st.msg)
		}
		if got, want := listed(), cmp.Or(st.listed, all); got != want {
			t.Errorf("%s: バッファの一覧 = %s, want %s", st.keys, got, want)
		}
	}
}

// :e と :w のファイルの名前の末尾の "\ " も名前の空白で、除かない（#354）。"\ " の後に続けて打った空白は除く。
// 補ったファイルの名前（末尾が空白の "tail " は "tail\ " と入る）を <CR> で開くと、そのファイルを開く。
// :w は打った名前のファイルだけを作り、"\" で終わる名前のファイルを作らない。
// 期待値は、同じファイルを置いた Vim 9.1 と Neovim 0.12.5 で同じコマンドを打って得た（2026-11-22。両者は同じ結果だった）。
//
// req: FR-195, FR-200
func TestFileArgEscapedSpace(t *testing.T) {
	tail := "tail " // 名前の末尾が空白のファイル
	h := startCompletion(t, map[string]string{tail: "tail\n", "other.txt": "other\n"}, nil)
	var dir string
	h.e.do(func() { dir = h.e.workDir() })
	files := func() string {
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, ent := range ents {
			data, _ := os.ReadFile(filepath.Join(dir, ent.Name()))
			s = append(s, fmt.Sprintf("[%s]=%q", ent.Name(), data))
		}
		return strings.Join(s, ",")
	}
	for _, st := range []struct {
		keys  string
		cur   string // 開いているバッファの中身
		files string // 空なら、最初の 2 つのファイルのまま
	}{
		{keys: `:e tail\ <CR>`, cur: "tail"},
		{keys: `:e   tail\  <CR>`, cur: "tail"},
		{keys: `:e tai<Tab><CR>`, cur: "tail"},
		{keys: `:w new\ <CR>`, cur: "other", files: `[new ]="other\n",[other.txt]="other\n",[tail ]="tail\n"`},
		{keys: `:w! new\  <CR>`, cur: "other", files: `[new ]="other\n",[other.txt]="other\n",[tail ]="tail\n"`},
	} {
		h.keys(`<Esc>:e other.txt<CR>`)
		h.settle()
		h.e.do(func() { h.e.message = infoMessage("") })
		h.keys(st.keys)
		h.settle()
		if got := h.text(); got != st.cur {
			t.Errorf("%s: バッファ = %q, want %q", st.keys, got, st.cur)
		}
		var msg ui.Message
		h.e.do(func() { msg = h.e.message })
		if msg.Level == ui.MessageError {
			t.Errorf("%s: 誤りのメッセージ %q", st.keys, msg.Text)
		}
		want := cmp.Or(st.files, `[other.txt]="other\n",[tail ]="tail\n"`)
		if got := files(); got != want {
			t.Errorf("%s: ファイル = %s, want %s", st.keys, got, want)
		}
	}
}

// <C-o> と <C-i> は、バッファをまたいで戻り、進む。<Tab> はタブの移動なので（FR-98）、<C-i> で進む。
func TestJumpAcrossBuffers(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": numberedLines("line", 30, " foo"), "b.txt": numberedLines("bline", 10, "")})
	h := start(t, Config{WorkDir: dir}, "a.txt")
	steps := []struct {
		keys, list string
		idx        int
		cur, msg   string
	}{
		{"5G:e b.txt<CR>3G", "a.txt:1.0 a.txt:5.0 b.txt:1.0", 3, "b.txt:3.0", ""},
		{"<C-o>", "a.txt:1.0 a.txt:5.0 b.txt:1.0 b.txt:3.0", 2, "b.txt:1.0", ""},
		{"<C-o>", "a.txt:1.0 a.txt:5.0 b.txt:1.0 b.txt:3.0", 1, "a.txt:5.0", ""},
		{"<C-i>", "a.txt:1.0 a.txt:5.0 b.txt:1.0 b.txt:3.0", 2, "b.txt:1.0", ""},
		{"<C-i>", "a.txt:1.0 a.txt:5.0 b.txt:1.0 b.txt:3.0", 3, "b.txt:3.0", ""},
		// 段階 3 では、未保存の変更があっても別のタブへ移る（基本設計 4.4 の段階 3 のタブとバッファ）。
		{"ix<Esc><C-o><C-o>", "a.txt:1.0 a.txt:5.0 b.txt:1.0 b.txt:3.0", 1, "a.txt:5.0", ""},
	}
	for _, st := range steps {
		h.keys(st.keys)
		h.settle()
		h.e.do(func() {
			list, idx, cur := h.e.jumpList()
			if list != st.list || idx != st.idx || cur != st.cur {
				t.Errorf("%s: jumps = [%s] idx=%d cur=%s, want [%s] idx=%d cur=%s", st.keys, list, idx, cur, st.list, st.idx, st.cur)
			}
			if st.msg != "" && !strings.Contains(h.e.message.Text, st.msg) {
				t.Errorf("%s: message = %q, want it to contain %q", st.keys, h.e.message.Text, st.msg)
			}
		})
	}
}

// 別のバッファへ移ると、画面の最初の行を、移った先のカーソルの行を表示領域の中ほど（上から (h-1)/2 行目）に置く位置にし、
// 先頭より前と、最終行の後ろの "~" の行が出る位置には詰める（Vim 9.1 の enter_buffer。#155）。
// 期待値は、tmux の Vim 9.1 で、別のバッファの 300 行目から <C-^> で戻ったときの line("w0") を、
// 表示領域が 33 行と 32 行の画面で測った式（400 行のファイルの 50 行目は 34 と 35、384 行目以降は 368 と 369、22 行のファイルは常に 1）を、
// fake の端末の 4 行の表示領域に当てはめたもの。
func TestEnterBufferView(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a.txt": numberedLines("a", 3, ""), "b.txt": numberedLines("b", 30, ""), "c.txt": numberedLines("c", 30, ""),
	})
	// Vim の測り方に合わせて、タブの行を出さない（出すと表示領域が 1 行減る）。
	h := start(t, Config{WorkDir: dir, TabLine: ui.TabLineNever}, "a.txt")
	steps := []struct {
		keys string
		top  int // 0 から数えた最初の行
	}{
		// 画面に収まる短いファイルへ戻ると、1 行目から出す（変更前は前のバッファの位置から中ほどへ動かし、1 行目が隠れた）。
		// 同じバッファの中で大きく下へ動く 20G と <C-o> は、Neovim 0.12.5 の update_topline と同じく、中ほど（上から (h-1)/2 行目）に置く
		// （2026-11-21 に、30 行のファイルを 4 行の表示領域の nvim で 20G と 2G<C-o> を打って、line("w0") が 19 になることを確かめた。
		// #337 より前は 17 だった）。
		{"3G:e b.txt<CR>20G", 18},
		{":b1<CR>", 0},
		{"<C-^>", 18},
		{":e c.txt<CR>30G", 26},
		{"<C-^>", 18},
		{"<C-^>", 26},
		// 同じバッファの中の <C-o> は、移る前の位置から画面に収める（b.txt の 20 行目へ）。別のバッファへの <C-o> は中ほどに置く（c.txt の 30 行目へ）。
		{"<C-^>2G", 0},
		{"<C-o>", 18},
		{"<C-o>", 26},
	}
	for _, st := range steps {
		h.keys(st.keys)
		h.settle()
		h.e.do(func() {
			if got := h.e.renderer.Top(); got != st.top {
				_, _, cur := h.e.jumpList()
				t.Errorf("%s: top = %d, want %d (cur %s)", st.keys, got, st.top, cur)
			}
		})
	}
}

// マクロの中の <C-o> は、その場で動く。別のバッファへ移ったら、マクロの残りは移った先のバッファで続く。
//
// req: FR-05
func TestJumpInMacro(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "a1\na2\na3\n", "b.txt": "b1\nb2\nb3\n"})
	h := start(t, Config{WorkDir: dir}, "a.txt")
	// レジスターの "\x0f" は <C-o> である（Vim と同じ）。
	h.e.do(func() { h.e.cur.state.Registers().Set('q', command.Register{Text: "Gx\x0fx"}) })
	h.keys("@q")
	h.settle()
	if got := h.text(); got != "1\na2\n3" {
		t.Errorf("Gx<C-o>x: text = %q", got)
	}
	h.keys(":w<CR>:e b.txt<CR>")
	h.e.do(func() { h.e.cur.state.Registers().Set('q', command.Register{Text: "\x0fx"}) })
	h.keys("@q")
	h.settle()
	h.e.do(func() {
		for _, bs := range h.e.bufs {
			// <C-o> で a.txt の 1 行目（"1"）へ戻り、x はそこで消す。
			want := map[string]string{"a.txt": "\na2\n3", "b.txt": "b1\nb2\nb3"}[filepath.Base(bs.path)]
			if got := bs.buf.Text(); got != want {
				t.Errorf("%s: text = %q, want %q", filepath.Base(bs.path), got, want)
			}
		}
	})
}

// マクロの中の Ex コマンドのバッファの切り替え（:e、:b、:bn、:bp）は、その場で動く。
// 切り替えの後のキーは、移った先のバッファで続く（#172）。
// 期待値は、Vim 9.1 で同じ 3 つのファイルを開き、レジスター c に同じキーを入れて再生し、P を打って得た（2026-11-18）。
// 段階 3 では、未保存の変更があってもタブを移る（基本設計 4.4。E37 で止まる場合は無くなった）。
//
// req: FR-05
func TestExBufferSwitchInMacro(t *testing.T) {
	tests := []struct {
		name  string
		macro string            // レジスター c に入れる中身（空なら入れない）
		keys  string            // a.txt で打つキー
		want  map[string]string // ファイルの名前ごとのバッファのテキスト
		cur   string
		err   string // メッセージの行に出る誤り（空なら確かめない）
	}{
		// #172 の入力。記録した後、a.txt で再生した yy は b.txt の B1 を取る。
		{"記録した :e", "", "qc:e b.txt<CR>yyq:e a.txt<CR>@cP", map[string]string{"a.txt": "ab", "b.txt": "B1\nB1\nB2"}, "b.txt", ""},
		{":b", ":b2\ryy", "@cP", map[string]string{"a.txt": "ab", "b.txt": "B1\nB1\nB2"}, "b.txt", ""},
		{":bn", ":bn\ryy", "@cP", map[string]string{"a.txt": "ab", "b.txt": "B1\nB1\nB2"}, "b.txt", ""},
		// a.txt は最初のバッファなので、:bp は最後の c.txt へ回る。
		{":bp", ":bp\ryy", "@cP", map[string]string{"a.txt": "ab", "c.txt": "C1\nC1\nC2"}, "c.txt", ""},
		// 2 回の切り替え。x は c.txt の C を消し、P はそれを貼る。
		{"2 回の :e", ":e b.txt\ryy:e c.txt\rx", "@cP", map[string]string{"a.txt": "ab", "b.txt": "B1\nB2", "c.txt": "C1\nC2"}, "c.txt", ""},
		// 同じ行の :w の動作を先に行ってから移る。
		{":w|e", ":w|e b.txt\ryy", "@cP", map[string]string{"a.txt": "ab", "b.txt": "B1\nB1\nB2"}, "b.txt", ""},
		// 回数付き。段階 3 では未保存の変更があってもタブを移るので（基本設計 4.4）、2 回目の :bn も c.txt へ移る（Vim 9.1 の :bn! と同じ）。
		{"2@c", ":bn\rx", "2@c", map[string]string{"a.txt": "ab", "b.txt": "1\nB2", "c.txt": "1\nC2"}, "c.txt", ""},
		// x で a.txt が変わっても :bn は移り、後ろの x は b.txt で動く。
		{"変更の後の :bn", "x:bn\rx", "@c", map[string]string{"a.txt": "b", "b.txt": "1\nB2", "c.txt": "C1\nC2"}, "b.txt", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeFiles(t, map[string]string{"a.txt": "ab\n", "b.txt": "B1\nB2\n", "c.txt": "C1\nC2\n"})
			h := start(t, Config{WorkDir: dir}, "a.txt")
			h.keys(":e b.txt<CR>:e c.txt<CR>:b1<CR>")
			h.settle()
			if tt.macro != "" {
				h.e.do(func() { h.e.cur.state.Registers().Set('c', command.Register{Text: tt.macro}) })
			}
			h.keys(tt.keys)
			h.settle()
			h.e.do(func() {
				for _, bs := range h.e.bufs {
					name := filepath.Base(bs.path)
					want, ok := tt.want[name]
					if !ok {
						continue
					}
					if got := bs.buf.Text(); got != want {
						t.Errorf("%s: text = %q, want %q", name, got, want)
					}
				}
				if got := filepath.Base(h.e.cur.path); got != tt.cur {
					t.Errorf("cur = %s, want %s", got, tt.cur)
				}
			})
			if got := h.message(); tt.err != "" && !strings.HasPrefix(got, tt.err) {
				t.Errorf("message = %q, want %s", got, tt.err)
			}
		})
	}
}

// 起動したときの名前の無い空のバッファから、マクロの再生の中で :e すると、bufferState は同じまま State を作り直す（replaceBuffer）。
// 残りのキーは作り直した State で動き、拡張機能へ送った版（sent）はバッファの版と揃う（PR #173 のレビューの必須 1）。
// 期待値の xB1 は、Vim 9.1 で同じキーを打って得た（レビュー、2026-11-18）。
//
// req: FR-05, FR-16
func TestExEditFromEmptyBufferInMacro(t *testing.T) {
	dir := writeFiles(t, map[string]string{"b.txt": "B1\nB2\n"})
	check := func(h *harness, step, want string) {
		t.Helper()
		h.settle()
		h.e.do(func() {
			bs := h.e.cur
			if got := filepath.Base(bs.path); got != "b.txt" {
				t.Errorf("%s: cur = %q", step, got)
			}
			if got := bs.buf.Text(); got != want {
				t.Errorf("%s: text = %q, want %q", step, got, want)
			}
			if bs.sent != bs.buf.Version() {
				t.Errorf("%s: sent = %d, version = %d", step, bs.sent, bs.buf.Version())
			}
		})
	}
	// 段階 3 では、最後のタブを :bd! で閉じると client が終わるので（FR-114）、名前の無い空のタブへ戻れない。
	// 段階 2 までの「記録してから :bd! で空のバッファへ戻る」手順に代えて、記録と同じ中身をレジスターに入れ、起動したときの空のタブで再生する。
	h := start(t, Config{WorkDir: dir})
	h.e.do(func() { h.e.cur.state.Registers().Set('c', command.Register{Text: ":e b.txt\rix\x1b"}) })
	h.keys("@c")
	check(h, "@c", "xB1\nB2")
	h.keys("x")
	check(h, "x", "B1\nB2")
	// 再生の中の yy も、作り直した State のレジスターに入る。
	h2 := start(t, Config{WorkDir: dir})
	h2.e.do(func() { h2.e.cur.state.Registers().Set('c', command.Register{Text: ":e b.txt\ryy"}) })
	h2.keys("@cP")
	check(h2, "yy", "B1\nB1\nB2")
}

// :jumps の一覧は、Vim 9.1 と同じ形で出す。期待値は、tmux の上の Vim 9.1 で同じキーを打って得た（2026-11-16）。
//
// req: FR-08
func TestJumpsCommandVim(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": numberedLines("line", 30, " foo"), "b.txt": numberedLines("bline", 10, "")})
	h := start(t, Config{WorkDir: dir}, "a.txt")
	for _, st := range []struct{ keys, want string }{
		{"lll5G:e b.txt<CR>3G:jumps<CR>", " jump line  col file/text\n   3     1    3 a.txt\n   2     5    0 a.txt\n   1     1    0 bline 1\n>"},
		{"<CR><C-o><C-o>:jumps<CR>", " jump line  col file/text\n   1     1    3 line 1 foo\n>  0     5    0 line 5 foo\n   1     1    0 b.txt\n   2     3    0 b.txt"},
	} {
		h.keys(st.keys)
		h.settle()
		if got := h.message(); got != st.want {
			t.Errorf("%s: :jumps = %q, want %q", st.keys, got, st.want)
		}
	}
}

// マクロの記録、ドットリピート、;、@@ は、Vim 9.1 と同じくバッファの切り替えをまたいで引き継ぐ（PR #150 のレビューの必須 1）。
//
// req: FR-05, FR-06
func TestEditorWideStateAcrossBuffers(t *testing.T) {
	files := map[string]string{"a.txt": "ah1\na2\na3\n", "b.txt": "one\ntwo\nthree\n"}
	type check func(t *testing.T, e *Editor)
	cur := func(want string) check {
		return func(t *testing.T, e *Editor) {
			if _, _, got := e.jumpList(); got != want {
				t.Errorf("cur = %s, want %s", got, want)
			}
		}
	}
	tests := []struct {
		keys  string
		check check
	}{
		// :bn の前後のキーをまとめて a に記録し、記録は 1 つだけ生きる。
		{"qa:bn<CR>jq", func(t *testing.T, e *Editor) {
			if r, _ := e.cur.state.Registers().Get('a'); r.Text != ":bn\rj" {
				got := r.Text
				t.Errorf(`"a = %q, want ":bn\rj"`, got)
			}
			for _, bs := range e.bufs {
				if r := bs.state.Recording(); r != 0 {
					t.Errorf("%s: recording %c", e.bufferName(bs), r)
				}
			}
		}},
		{"dd:w<CR>:bn<CR>.", func(t *testing.T, e *Editor) {
			if got := e.cur.buf.Text(); got != "two\nthree" {
				t.Errorf("b.txt = %q", got)
			}
		}},
		{"fh:bn<CR>3G;", cur("b.txt:3.1")},
		// マクロの再生を繰り返す . も引き継ぐ（FR-57）。
		{"qaxq:w<CR>:bn<CR>.", func(t *testing.T, e *Editor) {
			if got := e.cur.buf.Text(); got != "ne\ntwo\nthree" {
				t.Errorf("b.txt = %q", got)
			}
		}},
		{"qbjq@b:bn<CR>@@", cur("b.txt:2.0")},
	}
	for _, tt := range tests {
		t.Run(tt.keys, func(t *testing.T) {
			h := start(t, Config{WorkDir: writeFiles(t, files)}, "a.txt", "b.txt", "a.txt")
			h.keys(tt.keys)
			h.settle()
			h.e.do(func() { tt.check(t, h.e) })
		})
	}
}

// :wa、:qa、:wqa、:xa、:bd。
//
// req: FR-08, FR-114
func TestWriteAllQuitAllDelete(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "a1\n", "b.txt": "b1\n", "c.txt": "c1\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt", "b.txt", "c.txt", "a.txt")
	h.keys(`\b`)
	h.eventually("拡張機能の起動", func() bool { return strings.HasPrefix(h.e.message.Text, "bufs:") })
	steps := []struct{ keys, cur, msg string }{
		{"x:bn!<CR>x:bn!<CR>", "c.txt:1.0", ""},
		{":qa<CR>", "a.txt:1.0", `E162: バッファ "a.txt"`},
		// :bd は未保存の変更があれば E89 で閉じず、! で捨てて閉じる。閉じた後は裏のタブ（c.txt）へ移る。
		{":bd<CR>", "a.txt:1.0", "E89"},
		{":bd!<CR>", "c.txt:1.0", ""},
		{":bd 9<CR>", "c.txt:1.0", "E86"},
		{":wa<CR>", "c.txt:1.0", ""},
	}
	for _, st := range steps {
		h.keys(st.keys)
		h.settle()
		h.e.do(func() {
			if _, _, cur := h.e.jumpList(); cur != st.cur {
				t.Errorf("%s: cur = %s, want %s", st.keys, cur, st.cur)
			}
			if !strings.Contains(h.e.message.Text, st.msg) {
				t.Errorf("%s: message = %q, want it to contain %q", st.keys, h.e.message.Text, st.msg)
			}
		})
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if a, b := read("a.txt"), read("b.txt"); a != "a1\n" || b != "1\n" {
		t.Errorf(":bd! と :wa の後: a.txt = %q（変更を捨てる）, b.txt = %q（書き出す）", a, b)
	}
	h.e.do(func() {
		if got := h.e.bufferList(); strings.Contains(got, "a.txt") {
			t.Errorf(":bd の後の :ls = %q", got)
		}
	})
	h.keys(`\b`)
	h.eventually("buffer/close", func() bool { return strings.Contains(h.e.message.Text, "close 1") })
	// 段階 3 では、最後のタブを閉じると client が終わり、最後の client なので Run が戻る（FR-114、FR-61）。
	h.keys(":bd<CR>:bd<CR>")
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("最後のタブを :bd で閉じた後の Run = %v", err)
		}
		h.done <- err
	case <-time.After(5 * time.Second):
		t.Error("最後のタブを :bd で閉じても終わらない")
	}

	h2 := start(t, Config{WorkDir: dir})
	h2.keys("ix<Esc>:wqa<CR>")
	h2.settle()
	if got := h2.message(); !strings.Contains(got, "E141") {
		t.Errorf(":wqa の名前の無いバッファ: message = %q", got)
	}
	h2.keys(":qa!<CR>")
	select {
	case err := <-h2.done:
		h2.done <- err
	case <-time.After(5 * time.Second):
		t.Error(":qa! で終わらない")
	}
}

// 最後の行より後ろを指すジャンプの項目へは、Vim 9.1 と同じく E19 で移らない。
func TestJumpInvalidLine(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": numberedLines("line", 30, " foo")})
	h := start(t, Config{WorkDir: dir}, "a.txt")
	h.keys("25GdG<C-o>")
	h.settle()
	h.e.do(func() {
		if !strings.Contains(h.e.message.Text, "E19") {
			t.Errorf("message = %q", h.e.message.Text)
		}
		if list, idx, cur := h.e.jumpList(); list != "a.txt:1.0 a.txt:25.0 a.txt:24.0" || idx != 1 || cur != "a.txt:24.0" {
			t.Errorf("jumps = [%s] idx=%d cur=%s", list, idx, cur)
		}
	})
}

// 拡張機能の editor/open は、起動したときの名前の無い空のバッファから開くとき、:e と同じくその番号を使い直す。
func TestExtensionOpenReusesNoName(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "a1\n"})
	h := start(t, Config{WorkDir: dir})
	h.e.do(func() {
		if _, _, err := h.e.editorOpen(ext.EditorOpenParams{Path: filepath.Join(dir, "a.txt"), Focus: true}); err != nil {
			t.Fatal(err)
		}
		if got := h.e.bufferList(); got != `  1 %a   "a.txt"                        line 1` {
			t.Errorf(":ls = %q", got)
		}
	})
}

// 拡張機能の editor/open による移動（定義へ移動）は、ジャンプとして記録し、<C-o> で戻れる。
// 別のバッファへ移ったときは、移る前の位置だけを記録する。
func TestJumpFromExtensionOpen(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "a1\na2\na3\n", "b.txt": "b1\nb2\nb3\nb4\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)}, "a.txt", "b.txt")
	// b.txt の 3 行目から、fake.goto で a.txt の 2 行目へ移る。
	h.keys(`jj\g`)
	h.eventually("a.txt へ移る", func() bool { return h.e.cur.path == filepath.Join(dir, "a.txt") })
	h.e.do(func() {
		if list, idx, cur := h.e.jumpList(); list != "b.txt:3.0" || idx != 1 || cur != "a.txt:2.0" {
			t.Errorf("fake.goto の後: jumps = [%s] idx=%d cur=%s", list, idx, cur)
		}
	})
	h.keys("<C-o>")
	h.settle()
	h.e.do(func() {
		if list, idx, cur := h.e.jumpList(); list != "b.txt:3.0 a.txt:2.0" || idx != 0 || cur != "b.txt:3.0" {
			t.Errorf("<C-o> の後: jumps = [%s] idx=%d cur=%s", list, idx, cur)
		}
	})
	// 同じバッファの中の移動（fake.jump は * を入れて次の行へ移る）。
	h.keys(`k\j`)
	h.eventually("fake.jump の移動", func() bool { return h.e.cur.state.Cursor().Line == 2 && strings.HasPrefix(h.e.cur.buf.Line(1), "*") })
	h.keys("<C-o>")
	h.settle()
	h.e.do(func() {
		if _, _, cur := h.e.jumpList(); cur != "b.txt:2.0" {
			t.Errorf("fake.jump の後の <C-o>: cur = %s, want b.txt:2.0", cur)
		}
	})
}

// バッファの切り替えでは、拡張機能へ buffer/open も buffer/close も送り直さない。
// 起動したときの空の名前の無いバッファで :e すると、同じ番号で buffer/close と buffer/open を送り、そのファイルのバッファにする。
//
// req: FR-16
func TestBufferSwitchNotifications(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "a1\n", "b.txt": "b1\n"})
	h := start(t, Config{WorkDir: dir, Extensions: fakeSpecs(t)})
	h.keys(`\b`)
	h.eventually("拡張機能の起動", func() bool { return h.e.message.Text == "bufs: open 1 ." })
	h.keys(`:e a.txt<CR>:e b.txt<CR>:b1<CR><C-^>:bn<CR>:bp<CR>G<C-o>\b`)
	want := "bufs: open 1 ., close 1, open 1 a.txt, open 2 b.txt"
	h.eventually("切り替えの後の記録", func() bool { return h.e.message.Text == want })
	h.e.do(func() {
		if got := h.e.bufferList(); !strings.Contains(got, `  1 %a   "a.txt"`) || !strings.Contains(got, `  2 #h   "b.txt"`) || strings.Contains(got, "No Name") {
			t.Errorf(":ls = %q", got)
		}
	})
}
