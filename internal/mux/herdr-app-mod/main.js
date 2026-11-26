// vi-imitation の herdr app の mod（#440）。
//
// herdr app の中で動く vi-imitation は、herdr app の命令（pane.split など）を直接呼べない。
// vi-imitation は、ペインを割る依頼を、元の端末の hc のラベルとして書く（internal/mux の HerdrApp）。
// この mod は、依頼のある端末を探し（依頼のあった後の 30 秒は 0.5 秒ごと、ほかは 2 秒ごと）、herdr app の命令でペインを割り、結果を同じ端末のラベルに書き返す。
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
import { checking, exec, focused, machines, panes, run, theme } from "herdr";

const PENDING = "vi-imitation.pending";
const REQ = "vi-imitation.req.";
const RES = "vi-imitation.res.";
const CLAIM = "vi-imitation.claim.";
// 探す間隔。依頼があってから IDLE_AFTER_MS の間は POLL_MS ごとに、それより後は IDLE_POLL_MS ごとに探す。
// 本体は依頼を書いてから 10 秒（internal/mux の HerdrAppWait）待つので、静かな間の 2 秒は待ちに収まる。
// 別のマシンへの exec は SSH を通るので、依頼の無い間は探す回数を減らす（AGENTS.md の「poll gently」）。
const POLL_MS = 500;
const IDLE_POLL_MS = 2000;
const IDLE_AFTER_MS = 30000;
// hc を動かせなかったマシン（hc が無い、繋がらない）は、FAIL_BACKOFF_MS の間は探さない。
const FAIL_BACKOFF_MS = 30000;
const HC_TIMEOUT_MS = 10000;

// inFlight は、この herdr app の中で処理中の依頼である。mod の写しが複数あっても、同じ herdr app の中では 1 つの写しだけが処理する。
// モジュールのコードは写しの間で 1 つなので、写しの間で共有する（JavaScript は 1 本のスレッドで動くので、調べて足す間に割り込まれない）。
const inFlight = new Set();

export default class ViImitation extends View {
  init(_props, cx) {
    this.busy = false;
    this.served = 0;
    this.status = "待機中";
    this.handled = new Set();
    this.lastPoll = 0;
    this.lastActive = 0;
    this.retryAt = new Map();
    this.nonce = Math.random().toString(36).slice(2);
    // herdr-app check の間は、hc を呼ばない。
    if (checking()) return;
    cx.timer.every(POLL_MS, (cx) => this.poll(cx));
  }

  // poll は、前の探索が終わっていて、次に探す時刻を過ぎていれば、すべてのマシンの依頼を探して処理する。表示が変わったときだけ描き直す。
  poll(cx) {
    const now = Date.now();
    const interval = now - this.lastActive < IDLE_AFTER_MS ? POLL_MS : IDLE_POLL_MS;
    if (this.busy || now - this.lastPoll < interval - 50) return;
    this.busy = true;
    this.lastPoll = now;
    cx.spawn(async (cx) => {
      const before = this.status;
      for (const on of machineNames()) {
        if ((this.retryAt.get(on) ?? 0) > Date.now()) continue;
        try {
          await this.serveMachine(on);
          this.retryAt.delete(on);
        } catch (e) {
          // このマシンで hc を動かせない（hc が無い、PATH に無い、繋がらない）。しばらく探さず、ほかのマシンは続ける。
          // このコンピューター（null）は、vi-imitation の動かないことが多い（Windows など）ので、帯に出さない。
          this.retryAt.set(on, Date.now() + FAIL_BACKOFF_MS);
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
    if (terminals.length > 0) this.lastActive = Date.now();
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
      if (this.handled.has(mark) || inFlight.has(mark)) continue;
      // ほかの mod の引き受けの印が付いている依頼には手を出さない（退く。PR #441 の再レビューの推奨 B）。
      const owner = labels[CLAIM + id];
      if (owner && owner !== this.nonce) continue;
      inFlight.add(mark);
      try {
        // 別の herdr app（別の PC からつないだ画面）の mod と取り合わないように、引き受けの印を書いて読み直し、自分の印が残っていれば処理する。
        // hc label は比べてから書く操作を持たないので、2 つの mod がほぼ同時に書いて読むと両方が処理しうる（使い方に、mod は 1 つの画面に入れると書いた）。
        // 読み直しで依頼のラベルが消えていれば（本体が待ちを打ち切った）、ペインを割らない。
        await hcRun(["label", address, `${CLAIM}${id}=${this.nonce}`], on);
        const again = await hcLabels(address, on);
        if (again[CLAIM + id] !== this.nonce || !(key in again)) {
          // 引き受けに負けた（ほかの mod の印に書き替わった）か、本体が取り下げた。この依頼は二度と引き受けない。
          this.handled.add(mark);
          continue;
        }
        if (this.handled.size > 1000) this.handled.clear();
        this.handled.add(mark);
        let reply;
        try {
          reply = await this.handle(on, address, JSON.parse(again[key]));
        } catch (e) {
          reply = { error: errorText(e) };
        }
        reply.id = id;
        await hcRun(["label", address, `${RES}${id}=${JSON.stringify(reply)}`, `${key}=`, `${CLAIM}${id}=`], on);
        this.served++;
        this.status = reply.error ? `${this.served} 件目 ${address}: ${reply.error}` : `${this.served} 件処理`;
      } finally {
        inFlight.delete(mark);
      }
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
        // 元の端末のペインを引き、割る、入れ替える、フォーカスを戻す先に明示する。
        // 見つからなければ割らない（利用者の別のペインを割らない。PR #441 のレビューの推奨 1）。
        const from = await paneOf(on, address);
        if (!from) return { error: `no pane shows terminal ${qual(address)}` };
        const direction = req.direction === "down" ? "down" : "right";
        const made = await command("pane.split", { direction, pane: from, argv: req.argv });
        if (!made || !made.pane || !made.terminal) return { error: "pane.split returned no pane" };
        const reply = { terminal: local(made.terminal) };
        if (req.direction === "left") {
          try {
            await command("pane.swap", { pane: made.pane, target: from });
          } catch (e) {
            // 向きの違うペインを残さない。
            await command("pane.close", { pane: made.pane }).catch(() => {});
            return { error: errorText(e) };
          }
        }
        try {
          await command("pane.focus", { pane: req.focus ? made.pane : from });
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

// paneOf は、マシン on の端末 address を出しているペインの ID を返す。無ければ null を返す。
// まず panes() から端末の番地で引く。引けなければ、pane.focus {terminal} でその端末へフォーカスを移し、
// focused() の端末がその番地と合うときだけ、そのペインを返す（AGENTS.md の pane.focus は、レイアウトの端末のブロックを先に選ぶことがある。
// そのときは focused().pane が null で、割らない）。
async function paneOf(on, address) {
  const names = on ? [`${on}/${address}`, address] : [address];
  let list = [];
  try {
    list = panes() ?? [];
  } catch {
    list = [];
  }
  const p = list.find((x) => x && x.id && names.includes(x.terminal));
  if (p) return p.id;
  await command("pane.focus", { terminal: names[0] });
  const f = focused();
  return f?.pane && names.includes(f.terminal) ? f.pane : null;
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
