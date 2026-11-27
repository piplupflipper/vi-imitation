package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/piplupflipper/vi-imitation/ext"
)

// 言語サーバーの進み具合（LSP 3.17 の WorkDoneProgress）を、本体の progress/report に写す（基本設計 6.3。FR-179）。
//
// initialize で window.workDoneProgress を申し出る（clientCapabilities）。サーバーが window/workDoneProgress/create で作る識別子は
// 受け付けるだけで覚えず（serverRequest）、$/progress の begin で覚え、end で忘れる。
// 本体へ送る識別子は、言語サーバーの名前と、言語サーバーのプロセスの番号（proc.gen）と、サーバーの識別子の JSON をつないだものにする。
// 同じ拡張機能の中で、言語サーバーと根の組ごとのプロセスと、再起動の前後の言語サーバーの識別子が重ならないようにするためである
// （プロセスの番号は、根の違うプロセスでも重ならない）。
// 表題は、言語サーバーの名前と、サーバーの表題をつないだもの（"gopls: Setting up workspace"）にする。
// 言語サーバーのプロセスが終わったら、終わっていない進み具合をすべて end で終える（client.watch）。
//
// mise に言語サーバーを入れさせる間の進み具合（FR-143）も、同じ通知で知らせる（mise.go の miseInstall）。

// procGen は、言語サーバーのプロセスに振る番号（proc.gen）の、最後に振った値である。
var procGen atomic.Uint64

// reportProgress は、本体へ progress/report を送る。送れなければログに書く。
func (x *extension) reportProgress(p ext.ProgressReportParams) {
	if err := x.srv.ReportProgress(p); err != nil {
		x.logf("progress/report: %v", err)
	}
}

// progress は、言語サーバー p の $/progress を本体の progress/report に写す。接続の読み取りの goroutine から呼ばれるので、待たない。
// 稼働中の言語サーバーでないもの（再起動の前のもの）と、kind の無い value（部分の結果）は捨てる。
// begin の無い識別子の report と end も捨てる（本体も捨てるが、送らずに済ませる）。
//
// req: FR-179
func (c *client) progress(p *proc, params progressParams) {
	var v workDoneProgress
	if err := json.Unmarshal(params.Value, &v); err != nil || len(params.Token) == 0 {
		return
	}
	out := ext.ProgressReportParams{Message: v.Message}
	switch v.Kind {
	case "begin":
		out.Kind = ext.ProgressBegin
		out.Title = c.name()
		if v.Title != "" {
			out.Title += ": " + v.Title
		}
	case "report":
		out.Kind = ext.ProgressReport
	case "end":
		out.Kind = ext.ProgressEnd
	default:
		return
	}
	if v.Percentage != nil && out.Kind != ext.ProgressEnd {
		out.Percentage = ext.Percent(int(math.Round(*v.Percentage)))
	}
	out.Token = fmt.Sprintf("lsp/%s/%d/%s", c.server, p.gen, params.Token)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc != p {
		return
	}
	switch out.Kind {
	case ext.ProgressBegin:
		p.progress[out.Token] = true
	case ext.ProgressReport:
		if !p.progress[out.Token] {
			return
		}
	case ext.ProgressEnd:
		if !p.progress[out.Token] {
			return
		}
		delete(p.progress, out.Token)
	}
	// 本体への通知は列に積むだけで待たないので、mu を持ったまま送る（client の doc）。begin と end の順が入れ替わらない。
	c.x.reportProgress(out)
}

// endProgress は、c.mu を持った状態で、言語サーバー p の終わっていない進み具合をすべて、失敗の end（failed）で終える。
// 本文は言語サーバーが終わったことにする（読み込み中の本文のまま終わった印が付かないように。PR #345 のレビューの推奨 2）。
// 言語サーバーが終わった（異常終了、止めた）ときに呼ぶ（client.watch）。
//
// req: FR-179
func (c *client) endProgress(p *proc) {
	for token := range p.progress {
		c.x.reportProgress(ext.ProgressReportParams{
			Token: token, Kind: ext.ProgressEnd, Failed: true, Message: "言語サーバー " + c.name() + " が終了した",
		})
	}
	clear(p.progress)
}
