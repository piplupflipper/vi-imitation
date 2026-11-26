// vi-imitation の herdr app の mod（#440）。
//
// herdr app の中で動く vi-imitation は、herdr app の命令（pane.split など）を直接呼べない。
// vi-imitation は、ペインを割る依頼を、元の端末の hc のラベルとして書く（internal/mux の HerdrApp）。
// この mod は、依頼のある端末を 500 ミリ秒ごとに探し、herdr app の命令でペインを割り、結果を同じ端末のラベルに書き返す。
//
// ラベル（値は JSON。internal/mux/herdrapp.go と合わせる）:
//   vi-imitation.pending=1        依頼のある端末の印。hc ls --label で探す（hc ls はラベルを返さない）
//   vi-imitation.req.<番号>       依頼 {id, op, direction, argv, focus, terminal}
//   vi-imitation.res.<番号>       結果 {id, error, terminal, focus_error, focused}
//   vi-imitation.claim.<番号>     この mod の写しが依頼を引き受けた印（同じ依頼を 2 つの写しが処理しないため）
//
// 依頼の op:
//   split    元の端末のペインを direction（right、down、left）に割り、新しいペインで argv を起動する。
//            left は右に割ってから pane.swap で元のペインと入れ替える。focus が false なら元のペインへフォーカスを戻す
//   focus    terminal の端末のペインへフォーカスを移す
//   focused  terminal の端末のペインにフォーカスがあるかを答える
//
// herdr app が別のマシンの端末を扱うときは、hc をそのマシンで動かし（exec の on）、app の命令には machine/t7 の形の番地を渡す。
// ラベルに書く番地は、そのマシンの番地（machine/ の前置きなし）にする。

import { View } from "gpui-kit";
import { h_flex } from "gpui-base";
import { checking, exec, focused, machines, run, theme } from "herdr";

const PENDING = "vi-imitation.pending";
const REQ = "vi-imitation.req.";
const RES = "vi-imitation.res.";
const CLAIM = "vi-imitation.claim.";
const POLL_MS = 500;
const HC_TIMEOUT_MS = 10000;

export default class ViImitation extends View {
  init(_props, cx) {
    this.busy = false;
    this.served = 0;
    this.status = "待機中";
    this.handled = new Set();
    this.nonce = Math.random().toString(36).slice(2);
    // herdr-app check の間は、hc を呼ばない。
    if (checking()) return;
    cx.timer.every(POLL_MS, (cx) => this.poll(cx));
  }

  // poll は、前の探索が終わっていれば、すべてのマシンの依頼を探して処理する。表示が変わったときだけ描き直す。
  poll(cx) {
    if (this.busy) return;
    this.busy = true;
    cx.spawn(async (cx) => {
      const before = this.status;
      for (const on of machineNames()) {
        try {
          await this.serveMachine(on);
        } catch (e) {
          // このマシンに hc が無い、繋がらないなど。ほかのマシンは続ける。
          if (on !== null) this.status = `${on}: ${errorText(e)}`;
        }
      }
      this.busy = false;
      if (this.status !== before) cx.notify();
    });
  }

  // serveMachine は、マシン on（null はこのコンピューター）の、依頼の印のある端末を処理する。
  async serveMachine(on) {
    const out = await hcRun(["ls", "--json", "--label", `${PENDING}=1`], on);
    const terminals = JSON.parse(out).terminals ?? [];
    for (const t of terminals) {
      await this.serveTerminal(on, t.address);
    }
  }

  // serveTerminal は、端末 address の依頼をすべて処理し、残りが無ければ印を消す。
  async serveTerminal(on, address) {
    const labels = await hcLabels(address, on);
    for (const key of Object.keys(labels)) {
      if (!key.startsWith(REQ)) continue;
      const id = key.slice(REQ.length);
      const mark = `${on ?? ""}/${address}/${id}`;
      if (this.handled.has(mark)) continue;
      // 引き受けの印を書いて読み直し、自分の印が残っていれば処理する（別の画面の写しと取り合わない）。
      await hcRun(["label", address, `${CLAIM}${id}=${this.nonce}`], on);
      const again = await hcLabels(address, on);
      if (again[CLAIM + id] !== this.nonce || !(key in again)) continue;
      if (this.handled.size > 1000) this.handled.clear();
      this.handled.add(mark);
      let reply;
      try {
        reply = await this.handle(on, address, JSON.parse(labels[key]));
      } catch (e) {
        reply = { error: errorText(e) };
      }
      reply.id = id;
      await hcRun(["label", address, `${RES}${id}=${JSON.stringify(reply)}`, `${key}=`, `${CLAIM}${id}=`], on);
      this.served++;
      this.status = reply.error ? `${this.served} 件目 ${address}: ${reply.error}` : `${this.served} 件処理`;
    }
    const rest = await hcLabels(address, on);
    if (!Object.keys(rest).some((k) => k.startsWith(REQ))) {
      await hcRun(["label", address, `${PENDING}=`], on);
    }
  }

  // handle は、端末 address からの依頼 req を、herdr app の命令で行い、結果を返す。
  async handle(on, address, req) {
    const qual = (a) => (on ? `${on}/${a}` : a);
    const local = (a) => (on && typeof a === "string" && a.startsWith(`${on}/`) ? a.slice(on.length + 1) : a);
    switch (req.op) {
      case "split": {
        // 元の端末のペインにフォーカスを移してから、そのペインを割る。
        await command("pane.focus", { terminal: qual(address) });
        const direction = req.direction === "down" ? "down" : "right";
        const made = await command("pane.split", { direction, argv: req.argv });
        if (!made || !made.pane || !made.terminal) return { error: "pane.split returned no pane" };
        const reply = { terminal: local(made.terminal) };
        if (req.direction === "left") {
          try {
            await command("pane.swap", { pane: made.pane, direction: "left" });
          } catch (e) {
            // 向きの違うペインを残さない。
            await command("pane.close", { pane: made.pane }).catch(() => {});
            return { error: errorText(e) };
          }
        }
        try {
          if (req.focus) await command("pane.focus", { pane: made.pane });
          else await command("pane.focus", { terminal: qual(address) });
        } catch (e) {
          reply.focus_error = errorText(e);
        }
        return reply;
      }
      case "focus":
        await command("pane.focus", { terminal: qual(req.terminal) });
        return {};
      case "focused": {
        const f = focused();
        const t = f?.terminal;
        return { focused: !!f?.pane && (t === qual(req.terminal) || t === req.terminal) };
      }
      default:
        return { error: `unknown op ${req.op}` };
    }
  }

  render(_cx) {
    const t = theme();
    return h_flex()
      .size_full()
      .items_center()
      .px(8)
      .bg(t.chrome)
      .text_size(11)
      .text_color(t.muted)
      .child(`vi-imitation: ${this.status}`);
  }
}

// machineNames は、依頼を探すマシンの名前を返す。null はこのコンピューターである。
// 使えないマシンと、エージェントの一時的なマシンは除く。
function machineNames() {
  const names = [null];
  let list = [];
  try {
    list = machines() ?? [];
  } catch {
    list = [];
  }
  for (const m of list) {
    if (!m || m.name == null || m.temporary || m.layout === "unavailable") continue;
    names.push(m.name);
  }
  return names;
}

// hcRun は、マシン on で hc を args で動かし、標準出力を返す。0 でない終了コードなら誤りにする。
async function hcRun(args, on) {
  const opts = { timeout: HC_TIMEOUT_MS };
  if (on) opts.on = on;
  const r = await exec("hc", args, opts);
  const code = r.code ?? r.status ?? r.exit_code ?? 0;
  if (code !== 0) {
    const msg = String(r.stderr ?? "").trim();
    throw new Error(`hc ${args[0]}: ${msg || `exit ${code}`}`);
  }
  return String(r.stdout ?? "");
}

// hcLabels は、マシン on の端末 address のラベルを返す。
async function hcLabels(address, on) {
  const info = JSON.parse(await hcRun(["get", address, "--json"], on));
  return info.labels ?? {};
}

// command は、herdr app の命令を動かす。{ error } が返れば誤りにする。
async function command(name, args) {
  const r = await run(name, args);
  if (r && typeof r === "object" && r.error) throw new Error(`${name}: ${r.error}`);
  return r;
}

function errorText(e) {
  return e && e.message ? e.message : String(e);
}
