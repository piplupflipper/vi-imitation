<!-- AI エージェント向けの文書（vi-imitation --docs script）。印の間は make docs が生成する。印の間を手で編集しない。 -->
# スクリプトの拡張機能

Go のソースファイル 1 つを、配布物の `vi-imitation-ext-script` が yaegi で実行する拡張機能である。
宣言ファイルとスクリプトの例は `vi-imitation --docs usage スクリプトの拡張機能` にある。
書く前に、Go と違う点を `vi-imitation --docs usage 'yaegi の制限'` で読む。
メッセージの型（`ext`）は `vi-imitation --docs extensions` と `vi-imitation --docs extension-types <型の名前>` にある。

```go
package main

import (
	"context"
	"log"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

func main() {
	s := server.New()
	// 使わない引数にも _ と名前を付ける（yaegi の制限）
	s.Command("hello.say", func(ctx context.Context, _ *server.Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error) {
		if err := s.ShowMessage(ext.MessageInfo, "hello"); err != nil {
			return ext.CommandExecuteResult{}, err
		}
		return ext.CommandExecuteResult{OK: true}, nil
	})
	if err := s.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

## import できるパッケージ

次の標準ライブラリと、`github.com/piplupflipper/vi-imitation/ext`、`github.com/piplupflipper/vi-imitation/ext/server` だけを import できる。
パッケージごとの使える記号は `vi-imitation --docs script-symbols <import のパス>` で出す。
一覧に無い記号（型の引数を取る関数の `slices.Sort` など）は使えない。

<!-- generated:script-packages:begin -->
`archive/tar`、`archive/zip`、`bufio`、`bytes`、`cmp`、`compress/bzip2`、`compress/flate`、`compress/gzip`、`compress/lzw`、`compress/zlib`、`container/heap`、`container/list`、`container/ring`、`context`、`crypto/aes`、`crypto/cipher`、`crypto/des`、`crypto/dsa`、`crypto/ecdh`、`crypto/ecdsa`、`crypto/ed25519`、`crypto/elliptic`、`crypto/hmac`、`crypto/md5`、`crypto/rand`、`crypto/rc4`、`crypto/rsa`、`crypto/sha1`、`crypto/sha256`、`crypto/sha512`、`crypto/subtle`、`crypto/tls`、`crypto/x509/pkix`、`crypto/x509`、`crypto`、`database/sql/driver`、`database/sql`、`debug/buildinfo`、`debug/dwarf`、`debug/elf`、`debug/gosym`、`debug/macho`、`debug/pe`、`debug/plan9obj`、`encoding/ascii85`、`encoding/asn1`、`encoding/base32`、`encoding/base64`、`encoding/binary`、`encoding/csv`、`encoding/gob`、`encoding/hex`、`encoding/json/v2`、`encoding/json`、`encoding/pem`、`encoding/xml`、`encoding`、`errors`、`expvar`、`flag`、`fmt`、`go/ast`、`go/build/constraint`、`go/build`、`go/constant`、`go/doc/comment`、`go/doc`、`go/format`、`go/importer`、`go/parser`、`go/printer`、`go/scanner`、`go/token`、`go/types`、`go/version`、`hash/adler32`、`hash/crc32`、`hash/crc64`、`hash/fnv`、`hash/maphash`、`hash`、`html/template`、`html`、`image/color/palette`、`image/color`、`image/draw`、`image/gif`、`image/jpeg`、`image/png`、`image`、`index/suffixarray`、`io/fs`、`io/ioutil`、`io`、`log/slog`、`log/syslog`、`log`、`maps`、`math/big`、`math/bits`、`math/cmplx`、`math/rand/v2`、`math/rand`、`math`、`mime/multipart`、`mime/quotedprintable`、`mime`、`net/http/cgi`、`net/http/cookiejar`、`net/http/fcgi`、`net/http/httptest`、`net/http/httptrace`、`net/http/httputil`、`net/http/pprof`、`net/http`、`net/mail`、`net/netip`、`net/rpc/jsonrpc`、`net/rpc`、`net/smtp`、`net/textproto`、`net/url`、`net`、`os/exec`、`os/signal`、`os/user`、`os`、`path/filepath`、`path`、`reflect`、`regexp/syntax`、`regexp`、`runtime/debug`、`runtime/metrics`、`runtime/pprof`、`runtime/trace`、`runtime`、`slices`、`sort`、`strconv`、`strings`、`sync/atomic`、`sync`、`syscall`、`testing/fstest`、`testing/iotest`、`testing/quick`、`testing/slogtest`、`testing`、`text/scanner`、`text/tabwriter`、`text/template/parse`、`text/template`、`time`、`unicode/utf16`、`unicode/utf8`、`unicode`
<!-- generated:script-packages:end -->

## ext/server の API

`ext` と `ext/server` の公開の記号は、すべてスクリプトから使える。

<!-- generated:server-api:begin -->
#### 関数と変数

- `func InheritedSocket() (ln net.Listener, conns []net.Conn, err error)`: 本体が起動の契機 socket（ext.ActivationSocket（socket））で引き継いだ、待ち受けているソケットと、
  本体が受け付けて渡した接続を返す。
- `func IsReadOnly(err error) bool`: err が、編集を禁止したバッファへの buffer/edit を本体が断った誤りの応答（code が ext.ErrorCodeReadOnly（-32001））かを返す。
  Edit が返した誤りに使う。
- `ErrBufferClosed`: WaitBuffer で待っている間か、待ち始める前に、そのバッファの buffer/close が届いた（その後に buffer/open が届いていない）ことを表す。
  本体は、不正な UTF-8 を含むバッファを拡張機能へ送らず、editor/open で開いたときは buffer/open の代わりに buffer/close を送る（ext.BufferCloseParams）。
- `ErrChildUnsupported`: 切り替えの部品の子（空でない child）へ Rebind したことを表す。
  子だけを置き換える view/updateは、切り替えの部品を足すときに足す。
- `ErrEditInHandler`: Server が呼んだハンドラーかフックの中から、その ctx で、本体の応答を待つ関数を呼んだことを表す。
  対象は Edit、EditorState、OpenEditor、SaveBuffer、ListDiagnostics、AskChoice、WaitBuffer である。
  待つと詰まるので、これらはリクエストを送らずにこの誤りを返す。
- `ErrInvalidPosition`: 位置がバッファの外にあるか、UTF-8 の文字の途中を指すことを表す。
- `ErrInvalidRange`: 範囲の終わりが始まりより前にあることを表す。
- `ErrMessageTooLarge`: 本体の受信の上限を超えるので送らなかったことを表す（SetVirtualTexts）。
  上限を超えたメッセージを本体は読まずに接続を閉じるので、送る前に断る。接続は切れず、ほかの通知とリクエストはそのまま使える。
- `ErrNoBufferSync`: 本体がこの拡張機能へバッファの同期を送らない（宣言ファイルの noBufferSync。ext.InitializeParams の NoBufferSync）ので、
  WaitBuffer がバッファの複製を待たなかったことを表す。待っても buffer/open は届かず、ctx の期限まで戻らなくなるためである。
- `ErrNotRunning`: Serve の前か後に、本体へメッセージを送ろうとしたことを表す。
- `ErrOpening`: TextLog が開き直している最中（Open の open を呼んでいる間）に、Open か Rebind を呼んだことを表す。

#### `server.AsyncBufferWillSaveHandler`

buffer/willSave に後から答えるハンドラーである。
言語サーバーへ整形を問い合わせるときに使う。守ることは他の非同期のハンドラーと同じである。

`type AsyncBufferWillSaveHandler func(ctx context.Context, b *Buffer, p *ext.BufferWillSaveParams, reply func(ext.BufferWillSaveResult, error))`


#### `server.AsyncCommandHandler`

command/execute に後から答えるハンドラーである。
b が nil になる場合は CommandHandler と同じである。

`type AsyncCommandHandler func(ctx context.Context, b *Buffer, p *ext.CommandExecuteParams, reply func(ext.CommandExecuteResult, error))`


#### `server.AsyncMotionHandler`

motion/resolve に後から答えるハンドラーである。

`type AsyncMotionHandler func(ctx context.Context, b *Buffer, p *ext.MotionResolveParams, reply func(ext.MotionResolveResult, error))`


#### `server.AsyncOperatorHandler`

operator/apply に後から答えるハンドラーである。

`type AsyncOperatorHandler func(ctx context.Context, b *Buffer, p *ext.OperatorApplyParams, reply func(ext.OperatorApplyResult, error))`


#### `server.AsyncProviderHandler`

provider/invoke に後から答えるハンドラーである。

`type AsyncProviderHandler func(ctx context.Context, b *Buffer, p *ext.ProviderInvokeParams, reply func(ext.ProviderInvokeResult, error))`


#### `server.AsyncTextObjectHandler`

textObject/resolve に後から答えるハンドラーである。

`type AsyncTextObjectHandler func(ctx context.Context, b *Buffer, p *ext.TextObjectResolveParams, reply func(ext.TextObjectResolveResult, error))`


#### `server.Buffer`

本体のバッファの複製である。
buffer/open で受け取った全文に、buffer/change の差分を順に適用して、本体と同じ内容を保つ。

- `func (b *Buffer) Clone() *Buffer`: 内容とバージョン番号が同じ、独立した複製を返す。
  返した Buffer は、元の Buffer の更新の影響を受けないので、別の goroutine に渡してよい。
- `func (b *Buffer) FileType() string`: ファイルの種類を返す。決まらなければ空である。
- `func (b *Buffer) FinalNewline() bool`: 本体がこのバッファをファイルへ書き出したときに、最後の行の後に改行が付くかどうかを返す。
  Text は末尾の改行を含まないので、ファイルとしての内容は、これが true なら Text に "\n" を足したものになる。
  最後に受け取った buffer/open の finalNewline の値で、省かれていれば true である（ext.BufferOpenParams）。
  buffer/change では変わらない。
- `func (b *Buffer) ID() ext.BufferID`: バッファの番号を返す。
- `func (b *Buffer) Line(i int) string`: i 番目（0 始まり）の行を、改行を含まずに返す。
  i が 0 以上 LineCount() 未満でなければ panic する。
- `func (b *Buffer) LineCount() int`: 行の数を返す。空のバッファでも 1 以上である。
- `func (b *Buffer) Path() string`: バッファに対応するファイルのパスを返す。ファイルに対応しないバッファでは空である。
- `func (b *Buffer) Slice(r ext.Range) (string, error)`: 範囲 r のテキストを返す。
  範囲がバッファの外にあるか、文字の途中を指す場合は ErrInvalidPosition を、
  終わりが始まりより前にある場合は ErrInvalidRange を包んだ誤りを返す。
- `func (b *Buffer) Text() string`: 行を "\n" でつないだ全文を返す。
- `func (b *Buffer) Version() ext.Version`: 複製の内容に対応するバージョン番号を返す。

#### `server.BufferChangeEvent`

buffer/change の差分を複製に適用したことを表す。
差分が欠けて buffer/resync を送った場合は呼ばれず、次の buffer/open で BufferOpenEvent が届く。

- `Buffer *Buffer`: 差分を適用した後の複製である。フックが戻るまでの間だけ内容を保つ。
- `FromVersion ext.Version`: Edits を適用する前のバージョン番号である。
- `ToVersion ext.Version`: Edits を適用した後のバージョン番号で、Buffer.Version() と等しい。
- `Edits []ext.Edit`: 適用した編集の列で、受け取った逐次の数え方のままである（ext.BufferChangeParams）。
  構文の解析や LSP の増分の同期に使う。

#### `server.BufferCloseEvent`

buffer/close を受け取り、複製を捨てたことを表す。
開いていないバッファの buffer/close では呼ばれない。

- `Buffer ext.BufferID`: 閉じたバッファの番号である。

#### `server.BufferDidSaveEvent`

buffer/didSave を受け取ったことを表す。

- `Buffer *Buffer`: 書き出したバッファの複製である。複製を持っていないか、送り直しを待っている場合は nil。
  フックが戻るまでの間だけ内容を保つ。
- `Params ext.BufferDidSaveParams`: 受け取った params である。Params.Version は書き出した時点のバージョン番号である。

#### `server.BufferOpenEvent`

buffer/open で全文を受け取り、複製を作り直したことを表す。

- `Buffer *Buffer`: 作った複製である。フックが戻るまでの間だけ内容を保つ。
- `Reopen bool`: 既に持っていたバッファの全文を受け取ったかどうかである。
  buffer/resync の応答として本体が送り直したときは true になる。
  LSP の拡張機能なら、false で textDocument/didOpen を、true で全文の textDocument/didChange を送る、といった振り分けに使う。

#### `server.BufferWillSaveHandler`

buffer/willSave に答えるハンドラーである。
b は p.Buffer の複製で、そのバージョン番号は p.Version と一致する。
返した result に Transaction があれば、その BaseVersion は、Server が p.Version で上書きする。
編集しないなら、Transaction を省いた result を返す。

`type BufferWillSaveHandler func(ctx context.Context, b *Buffer, p *ext.BufferWillSaveParams) (ext.BufferWillSaveResult, error)`


#### `server.CommandHandler`

command/execute に答えるハンドラーである。
b は p.Buffer の複製で、そのバージョン番号は p.Version と一致する。
ただし p.Withheld が true（本体が不正な UTF-8 のためにバッファを送っていない）なら b は nil である。
バッファを使うコマンドは、そのとき OK を false で返す。
宣言ファイルに noBufferSync を書いた拡張機能では、本体がどのバッファも送らず、p.Withheld をいつも立てるので、b はいつも nil である
（ext.Manifest.NoBufferSync）。そうした拡張機能では、バッファの複製を待つ WaitBuffer も、待たずに ErrNoBufferSync を返す。
返した result に Transaction があれば、その BaseVersion は、Server が p.Version で上書きする。

`type CommandHandler func(ctx context.Context, b *Buffer, p *ext.CommandExecuteParams) (ext.CommandExecuteResult, error)`


#### `server.InitializeEvent`

initialize を受け取ったことを表す。

- `APIVersion string`: 本体が実装している拡張機能 API のバージョンである。
- `Settings json.RawMessage`: 利用者の設定ファイルのうち、この拡張機能に向けた部分である。無ければ nil。
- `NoBufferSync bool`: 本体がこの拡張機能へバッファの同期を送らない（宣言ファイルの noBufferSync）ことを表す。
  true なら、ハンドラーの b はいつも nil で、WaitBuffer は ErrNoBufferSync を返す。

#### `server.MotionHandler`

motion/resolve に答えるハンドラーである。
b は p.Buffer の複製で、そのバージョン番号は p.Version と一致する。
返した result の Version は、Server が p.Version で上書きする。

`type MotionHandler func(ctx context.Context, b *Buffer, p *ext.MotionResolveParams) (ext.MotionResolveResult, error)`


#### `server.OperatorHandler`

operator/apply に答えるハンドラーである。
b は p.Buffer の複製で、そのバージョン番号は p.Version と一致する。
返した result の Transaction.BaseVersion は、Server が p.Version で上書きする。

`type OperatorHandler func(ctx context.Context, b *Buffer, p *ext.OperatorApplyParams) (ext.OperatorApplyResult, error)`


#### `server.ProviderHandler`

provider/invoke に答えるハンドラーである。
b が nil になる場合と、結果の Transaction の BaseVersion の扱いは CommandHandler と同じである。

`type ProviderHandler func(ctx context.Context, b *Buffer, p *ext.ProviderInvokeParams) (ext.ProviderInvokeResult, error)`


#### `server.Server`

拡張機能の側のメッセージループである。
本体との JSON-RPC の接続、バッファの複製の管理、登録したハンドラーの呼び出しを行う。

- `ErrorLog *log.Logger`: panic と内部の誤りを書くロガーである。nil なら標準エラー出力へ書く。
- `func New() *Server`: ハンドラーを登録していない Server を作る。
- `func (s *Server) AppendOutput(channel, text string) error`: 本体へ output/append を送り、名前が channel の出力の欄へ text を書き足させる。
  応答は待たない。どこから呼んでもよい。
- `func (s *Server) AppendView(p ext.ViewAppendParams) error`: 本体へ view/append を送り、開いたテキストの部品の末尾へ書き足させる。応答は待たない。どこから呼んでもよい。
  p が ext.ViewAppendParams.Validate を通らなければ、送らずにその誤りを返す。
  閉じた後に開き直したときに中身を戻したいときは、TextLog を使う。
- `func (s *Server) AskChoice(ctx context.Context, p ext.ChoiceAskParams) (ext.ChoiceAskResult, error)`: 本体へ choice/ask を送り、利用者に選択肢から 1 つ選ばせる。
  本体は利用者の応答を待つので、応答までの時間に上限は無い。打ち切るときは ctx を取り消す。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) BufferWillSave(h BufferWillSaveHandler)`: 保存の直前の buffer/willSave に答えるハンドラーを登録する。
  本体が buffer/willSave を送るのは、宣言ファイルの saveHook が true の拡張機能だけである。
  2 回登録するか（BufferWillSaveAsync との重複を含む）、Serve の後に呼ぶと panic する。
- `func (s *Server) BufferWillSaveAsync(h AsyncBufferWillSaveHandler)`: BufferWillSave の非同期版である。
- `func (s *Server) CloseView(id ext.ViewID) error`: 本体へ view/close を送り、開いたポップアップを閉じさせる。応答は待たない。どこから呼んでもよい。
  本体は、この知らせで閉じたポップアップについて view/closed を送らない。
- `func (s *Server) Command(id string, h CommandHandler)`: 宣言ファイルの contributes.commands に書いた id のコマンドのハンドラーを登録する。
  同じ id を 2 回登録するか（CommandAsync との重複を含む）、Serve の後に呼ぶと panic する。
- `func (s *Server) CommandAsync(id string, h AsyncCommandHandler)`: Command の非同期版である。
- `func (s *Server) CreateBuffer(ctx context.Context, p ext.BufferCreateParams) (ext.BufferCreateResult, error)`: 本体へ buffer/create を送り、p のテキストでファイルでないバッファを作らせる。応答は作ったバッファの番号である。
  p が ext.BufferCreateParams.Validate を通らなければ、送らずにその誤りを返す。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) DisposeBuffer(ctx context.Context, id ext.BufferID) error`: 本体へ buffer/dispose を送り、CreateBuffer で作ったバッファを閉じさせる。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) Edit(ctx context.Context, p ext.BufferEditParams) (ext.BufferEditResult, error)`: 本体へ buffer/edit を送り、応答を待つ。
  補完の確定や整形のように、拡張機能の側から始める編集に使う。
  編集を禁止したバッファへの要求は、本体が誤りの応答で断るので、IsReadOnly が true を返す誤りになる。作り直しても通らない。
  適用されると、本体は buffer/change を送り、複製はループがそれを処理した時点で更新される（Edit が戻った時点ではまだ古いことがある）。
- `func (s *Server) EditorState(ctx context.Context) (ext.EditorStateResult, error)`: 本体へ editor/state を送り、編集の状態（カーソルのあるバッファ、カーソル位置、モード、選択範囲、
  開いているバッファの一覧）を問い合わせる。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) Execute(ctx context.Context, p ext.EditorExecuteParams) error`: 本体へ editor/execute を送り、キーの一覧のキーかコマンドを、利用者が打ったのと同じく実行させる。
  本体は実行の前に応答する。p が ext.EditorExecuteParams.Validate を通らなければ、送らずにその誤りを返す。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) GetOptions(ctx context.Context, p ext.OptionGetParams) (ext.OptionGetResult, error)`: 本体へ option/get を送り、設定の項目の今の値を読む。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) ListDiagnostics(ctx context.Context, p ext.DiagnosticListParams) (ext.DiagnosticListResult, error)`: 本体へ diagnostic/list を送り、本体が受け取り済みの診断を問い合わせる。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) ListEditor(ctx context.Context, list ext.EditorListKind) (ext.EditorListResult, error)`: 本体へ editor/list を送り、本体が持つ一覧（タブ、コマンド、キーの一覧、レジスターの履歴、ジャンプリスト）の 1 つを
  問い合わせる。応答は、問い合わせた list の欄だけを埋めたものである。
  list が知らない種類なら、送らずにその誤りを返す。Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) Motion(id string, h MotionHandler)`: 宣言ファイルの contributes.motions に書いた id のモーションのハンドラーを登録する。
  同じ id を 2 回登録するか（MotionAsync との重複を含む）、Serve の後に呼ぶと panic する。
- `func (s *Server) MotionAsync(id string, h AsyncMotionHandler)`: Motion の非同期版である。
- `func (s *Server) OnBufferChange(f func(ctx context.Context, e *BufferChangeEvent))`: buffer/change の差分を複製に適用したときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
- `func (s *Server) OnBufferClose(f func(ctx context.Context, e *BufferCloseEvent))`: buffer/close を受け取って複製を捨てたときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
- `func (s *Server) OnBufferDidSave(f func(ctx context.Context, e *BufferDidSaveEvent))`: buffer/didSave を受け取ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
  LSP の拡張機能が textDocument/didSave を、DAP の拡張機能がブレークポイントの送り直しを行うのに使う。
- `func (s *Server) OnBufferOpen(f func(ctx context.Context, e *BufferOpenEvent))`: buffer/open で複製を作ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
- `func (s *Server) OnInitialize(f func(ctx context.Context, e *InitializeEvent) error)`: initialize を受け取ったときに呼ぶ関数を登録する。
  f が誤りを返すと、initialize に失敗を応答する。本体はそれを起動の失敗として扱う。
  Serve の後に呼ぶと panic する。
- `func (s *Server) OnShutdown(f func(ctx context.Context, e *ShutdownEvent))`: shutdown を受け取ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
- `func (s *Server) OnView(f func(ctx context.Context, e *ViewEvent))`: ポップアップの利用者の操作の知らせを受け取ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
  他のフックと同じくループの上で呼ばれるので、f の中で本体の応答を待つ関数（OpenPopup など）をこの ctx で呼ぶと ErrEditInHandler になる。
  応答を待たない UpdateView と CloseView は呼んでよい。
- `func (s *Server) OnWindowChange(f func(ctx context.Context, e *WindowChangeEvent))`: window/change を受け取ったときに呼ぶ関数を登録する。Serve の後に呼ぶと panic する。
  本体が送るのは、宣言ファイルの windowEvents が true の拡張機能だけである。
- `func (s *Server) OpenEditor(ctx context.Context, p ext.EditorOpenParams) (ext.EditorOpenResult, error)`: 本体へ editor/open を送り、ファイルかバッファを開かせる。
  p が ext.EditorOpenParams.Validate を通らなければ、送らずにその誤りを返す。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) OpenPane(ctx context.Context, p ext.PaneOpenParams) (ext.PaneOpenResult, error)`: 本体へ pane/open を送り、表示の部品を中身にしたペインを端末多重化ソフトで開かせる。応答はペインの ID である。
  端末多重化ソフトの外では、本体はペインに代えて中央のポップアップに出す。どちらでも、以後は同じ ID で
  UpdateView と CloseView を送れ、利用者の操作は OnView で届く。
  p が ext.PaneOpenParams.Validate を通らなければ、送らずにその誤りを返す。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) OpenPopup(ctx context.Context, p ext.PopupOpenParams) (ext.PopupOpenResult, error)`: 本体へ popup/open を送り、表示の部品を中身にしたポップアップを開かせる。応答はポップアップの ID である。
  p が ext.PopupOpenParams.Validate を通らなければ、送らずにその誤りを返す。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) Operator(id string, h OperatorHandler)`: 宣言ファイルの contributes.operators に書いた id のオペレーターのハンドラーを登録する。
  同じ id を 2 回登録するか（OperatorAsync との重複を含む）、Serve の後に呼ぶと panic する。
- `func (s *Server) OperatorAsync(id string, h AsyncOperatorHandler)`: Operator の非同期版である。
- `func (s *Server) Provider(op ext.Operation, h ProviderHandler)`: 宣言ファイルの provides に書いた本体の操作 op のハンドラーを登録する。
  同じ op を 2 回登録するか（ProviderAsync との重複を含む）、Serve の後に呼ぶと panic する。
  登録していない操作の provider/invoke には、JSON-RPC のエラー（InvalidParams）を応答する。
- `func (s *Server) ProviderAsync(op ext.Operation, h AsyncProviderHandler)`: Provider の非同期版である。
- `func (s *Server) ReportProgress(p ext.ProgressReportParams) error`: 本体へ progress/report を送り、時間のかかる処理の進み具合を知らせる。
  p.Kind が ext.ProgressBegin（begin） なら始め、ext.ProgressReport（report） なら本文か百分率を変え、ext.ProgressEnd（end） なら終える。
  識別子 p.Token は拡張機能の中で一意にする。応答は待たない。どこから呼んでもよい。
- `func (s *Server) Run(ctx context.Context) error`: 標準入力と標準出力で本体と通信し、Serve と同じく動く。
  拡張機能の main から呼ぶ。
- `func (s *Server) SaveBuffer(ctx context.Context, p ext.BufferSaveParams) (ext.BufferSaveResult, error)`: 本体へ buffer/save を送り、バッファをファイルへ書き出させる。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error`: r から本体のメッセージを読み、w へ書き、接続が終わるまでメッセージを処理する。
  r と w が io.Closer を実装していれば、終わるときに閉じる。
- `func (s *Server) SetBand(p ext.BandSetParams) error`: 本体へ band/set を送り、窓の上端に行を重ねて描く帯を置く。
  p.Lines が空なら帯を消す。p.Seq には、帯を作る元にした知らせの WindowChangeEvent.Params.Seq を書く。
  応答は待たない。どこから呼んでもよい。
  p.Validate を通らない帯は、送らずに誤りを返す（本体も捨てる）。
- `func (s *Server) SetDecorations(p ext.DecorationSetParams) error`: 本体へ decoration/set を送る。応答は待たない。どこから呼んでもよい。
- `func (s *Server) SetDiagnostics(p ext.DiagnosticSetParams) error`: 本体へ diagnostic/set を送る。応答は待たない。どこから呼んでもよい。
- `func (s *Server) SetOptions(ctx context.Context, p ext.OptionSetParams) error`: 本体へ option/set を送り、本体の設定の項目の値を変える。
  p.Buffer を書けばそのバッファの範囲がバッファごとの項目を、省けば範囲がエディタ全体の項目を変える。
  値は利用者が :set で変えた値には勝たない。表に無い名前や範囲の外の値があれば、本体はどの値も入れずに誤りで応答する。
  p が ext.OptionSetParams.Validate を通らなければ、送らずにその誤りを返す。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す（buffer/open のフックからは goroutine を起こして呼ぶ）。
- `func (s *Server) SetSigns(p ext.SignSetParams) error`: 本体へ sign/set を送る。応答は待たない。どこから呼んでもよい。
- `func (s *Server) SetStatusLine(part, text, style string) error`: 本体へ statusline/set を送り、宣言ファイルの contributes.statusline で足した部品 part の中身を text にする
  。style は部品の色の名前で、空なら利用者の設定の statusline.ext の色で描く。text が空なら部品を出さない。
  応答は待たない。どこから呼んでもよい。
- `func (s *Server) SetVirtualTexts(p ext.VirtualTextSetParams) error`: 本体へ virtualText/set を送り、バッファの位置に本文に無い文字列（仮想テキスト）を出させる
  。一覧はバッファ全体の分で、同じ拡張機能とバッファの前の一覧を置き換える。
  空の一覧で、その組の仮想テキストを消す。応答は待たない。どこから呼んでもよい。
- `func (s *Server) ShowMessage(level ext.MessageLevel, text string) error`: 本体へ message/show を送り、利用者へメッセージを表示させる。応答は待たない。どこから呼んでもよい。
- `func (s *Server) ShowPopup(p ext.PopupShowParams) error`: 本体へ popup/show を送る。応答は待たない。どこから呼んでもよい。
- `func (s *Server) StartDiff(ctx context.Context, p ext.DiffStartParams) (ext.DiffStartResult, error)`: 本体へ diff/start を送り、2 つのバッファの見比べを始めさせる。
  p が ext.DiffStartParams.Validate を通らなければ、送らずにその誤りを返す。
  見比べを始められなければ（端末多重化ソフトの外、同じ client の 2 つのタブなど）、本体は誤りで応答する。誤りの文言は画面に出せる日本語である。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) TextObject(id string, h TextObjectHandler)`: 宣言ファイルの contributes.textObjects に書いた id のテキストオブジェクトのハンドラーを登録する。
  同じ id を 2 回登録するか（TextObjectAsync との重複を含む）、Serve の後に呼ぶと panic する。
- `func (s *Server) TextObjectAsync(id string, h AsyncTextObjectHandler)`: TextObject の非同期版である。
- `func (s *Server) UpdateBuffer(ctx context.Context, p ext.BufferUpdateParams) (ext.BufferUpdateResult, error)`: 本体へ buffer/update を送り、CreateBuffer で作ったバッファの中身を置き換えさせる。応答は置き換えた後のバージョン番号である。
  Edit と同じく、ループの上から呼ぶと ErrEditInHandler を返す。
- `func (s *Server) UpdateView(p ext.ViewUpdateParams) error`: 本体へ view/update を送り、開いたポップアップの部品の中身を置き換えさせる。応答は待たない。どこから呼んでもよい。
  p.Component が ext.ViewComponent.Validate を通らなければ、送らずにその誤りを返す。
- `func (s *Server) WaitBuffer(ctx context.Context, id ext.BufferID, version ext.Version) (*Buffer, error)`: バッファ id の複製が、バージョン番号 version 以降になるまで待ち、その時点の複製の Clone を返す。
  既にそうなっていれば待たずに返す。
  editor/open（OpenEditor）の応答を受け取った後に、本体が先に送った buffer/open の処理を待つのに使う。
  本体が buffer/edit を適用した後の buffer/change を待つのにも使える。

#### `server.ShutdownEvent`

本体から shutdown を受け取ったことを表す。
中身は無いが、後から項目を足せるように構造体にしている。


#### `server.TextLog`

テキストの部品へ書き足す中身の写しである。

- `func NewTextLog(s *Server) *TextLog`: s の上の空の写しを作る。行数の上限は本体と同じ値（ext.ViewTextMaxLines（10000））である。
- `func (t *TextLog) Append(text, style string) error`: text を写しに足す。style は 7.1 の色の名前で、空でなければ text の全体にその色を付ける。
  結びついていれば、錠を持ったまま view/append（結びついた部品と子）を送る。開き直している最中（Open）なら、送らずに
  「開いている間の書き足し」に積み、開き終えたときに送る。送る順は呼んだ順である。
  text が不正な UTF-8 を含めば、何もせずに誤りを返す。誤りは、送れなかったとき（接続が無いとき）にも返すが、写しには足してある。
- `func (t *TextLog) Attached() (view ext.ViewID, child string, ok bool)`: 結びついた部品と子を返す。結びついていなければ ok が false である。
  部品が閉じた（view/closed を受けた）後と、別の写しが同じ部品に付け替えた後は、結びついていない。
- `func (t *TextLog) Detach()`: 結びつきを外す。その後の Append は写しにだけ足す。
  開き直している最中（Open が open を呼んでいる間）に呼ぶと、Open は open が戻っても結びつけず、その間の書き足しも送らない
  （開いた部品は、拡張機能の側で閉じる）。
- `func (t *TextLog) Open(open func(ext.ViewComponent) (ext.ViewID, error), child string) error`: 写しの中身を部品にして開き直し、開いた部品に結びつける。
- `func (t *TextLog) Rebind(view ext.ViewID, child string) error`: 開いている部品 view に結びつけ直し、写しの中身で部品の中身を置き換える。
  錠の中で、前の結びつきと、同じ部品に結びついていた別の写しを外し、写しの中身を部品にした view/update を送り、結びつける。
  view/update と view/append を同じ錠の中で送るので、付け替えの前の書き足しは view/update の中身に入り、後の書き足しは
  view/update の後の view/append になる。部品が既に閉じていれば、何も送らずに結びつけない。

#### `server.TextObjectHandler`

textObject/resolve に答えるハンドラーである。
b は p.Buffer の複製で、そのバージョン番号は p.Version と一致する。
返した result の Version は、Server が p.Version で上書きする。

`type TextObjectHandler func(ctx context.Context, b *Buffer, p *ext.TextObjectResolveParams) (ext.TextObjectResolveResult, error)`


#### `server.ViewEvent`

本体からポップアップの利用者の操作の知らせ（view/closed、view/select、input/change、view/key、view/expand、
view/tabChange）を受け取ったことを表す。

- `Kind ViewEventKind`
- `View ext.ViewID`: ポップアップの ID である。
- `Item string`: 選んだ項目の ID か、入力の行の中身である。ViewClosed では空である。
- `Key string`: ViewKey のときの contributes.popupKeys の項目の ID である。
- `Expanded bool`: ViewExpanded のときの、項目を開いたかどうかである。
- `Seq uint64`: ViewInputChanged のときの input/change の番号である（ext.InputChangeParams.Seq）。
  ファインダー（ext.ViewFinder（finder））の一覧を返す view/update の Seq に入れる。
- `Input string`: ファインダーの ViewSelected と ViewKey のときの、入力の行の中身である。
- `Selection uint64`: ViewSelected のときの選択の番号である（ext.ViewSelectParams.Selection）。Execute に渡す。
- `Buffer ext.BufferID`: ViewTabChanged のときの、替わった後のタブのバッファの番号である。

#### `server.ViewEventKind`

ViewEvent の種類である。

`type ViewEventKind int`

- `ViewClosed`: view/closed である。利用者か本体がポップアップを閉じた。
- `ViewSelected`: view/select である。Item に項目の ID（入力の行では中身）が入る。
- `ViewInputChanged`: input/change である。Item に入力の行の中身が入る。
- `ViewKey`: view/key である。Key に contributes.popupKeys の項目の ID が、Item にカーソルの行の項目の ID が入る。
- `ViewExpanded`: view/expand である。Item に開閉した木の項目の ID が、Expanded に開いたかどうかが入る。
- `ViewTabChanged`: view/tabChange である（API 0.10.0 で足した）。分割したペインを開いた元の client のカーソルのあるタブが替わった。
  Buffer にそのタブのバッファの番号が、Item にそのファイルの絶対パス（ファイルでなければ空）が入る（ext.ViewTabChangeParams）。

#### `server.WindowChangeEvent`

本体から window/change を受け取ったことを表す。

- `Params ext.WindowChangeParams`: 受け取った知らせである。Params.Closed が true なら、窓が無くなった。
  帯を送るときは、Params.Seq を ext.BandSetParams.Seq に書く（本体が、古い知らせから作った帯を捨てる）。
- `Buffer *Buffer`: 窓のバッファの複製である。持っていない（拡張機能へ送っていないバッファ、窓が無くなった）なら nil である。
  フックが戻るまでの間だけ内容を保つ。
  本体は window/change より前にそれまでの buffer/change を送るので、複製のバージョン番号はふつう Params.Version と同じである。
  差分が欠けて buffer/open の送り直しを待っている間は、複製が古いことがある（Buffer.Version で確かめる）。
<!-- generated:server-api:end -->
