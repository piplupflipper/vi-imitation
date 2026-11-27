// vi-imitation の herdr app の mod（#440）。
//
// herdr app の中で動く vi-imitation は、herdr app の命令（pane.split など）を直接呼べない。
// vi-imitation は、ペインを割る依頼を、元の端末の hc のラベルとして書く（internal/mux の HerdrApp）。
// この mod は、依頼のある端末を探し（依頼のあった後の 30 秒は 0.5 秒ごと、ほかは 2 秒ごと）、herdr app の命令でペインを割り、結果を同じ端末のラベルに書き返す。
//
// ラベル（値は JSON。internal/mux/herdrapp.go と合わせる）:
//   vi-imitation.pending=1        依頼のある端末の印。hc ls --label で探す（hc ls はラベルを返さない）
//   vi-imitation.req.<番号>       依頼 {id, op, direction, argv, cwd, width, height, focus, terminal}
//   vi-imitation.res.<番号>       結果 {id, error, terminal, focus_error, focused, busy, ops}
//   vi-imitation.claim.<番号>     この mod の写しが依頼を引き受けた印（同じ依頼を 2 つの写しが処理しないため）
//   vi-imitation.split=<番号>     split で開いた新しい端末に、その中で動く本体の包んだスクリプトが付ける印。
//                                 run("pane.split") が番地を返さないときに、新しい端末を探すのに使う（#444）
//   vi-imitation.popup=<番号>     popup で起動した窓の端末に付ける印。この印の端末が無くなったら（中のコマンドが終わった）窓を閉じる（#452）
//
// 依頼の op:
//   split        元の端末のペインを direction（right、down、left）に割り、新しいペインで argv を起動する。
//                left は右に割ってから pane.swap で元のペインと入れ替える。focus が false なら元のペインへフォーカスを戻す
//   focus        terminal の端末のペインへフォーカスを移す
//   focused      terminal の端末のペインにフォーカスがあるかを答える
//   popup        argv を、herdr.* のラベルの無い端末として hc run で cwd に起動し、その端末を画面の中央の窓（openOverlay の kind window）に出して、
//                キーボードをその端末へ移す。窓の大きさは、width と height の百分率を画面の大きさ（window()）から決める。
//                この herdr app に窓が既に開いていれば、開かずに busy を返す（#452）。窓の上には、この mod の写し（props の popup）を
//                帯として置く。帯はドラッグの取っ手と閉じるボタンで、窓が mod の外で消えたら（レイアウトを替えたなど）close で端末を終わらせる
//   popup_close  依頼を書いた端末から開いた窓の端末を終わらせ、窓を閉じる
//   hello        この mod の知っている op を ops で答える。本体は、popup を知っているかをこれで確かめ、知らなければ（古い mod）
//                ポップアップを vi-imitation の画面の中に描く
//
// herdr app が別のマシンの端末を扱うときは、hc をそのマシンで動かし（exec の on）、app の命令には machine/t7 の形の番地を渡す。
// ラベルに書く番地は、そのマシンの番地（machine/ の前置きなし）にする。

import { View, div } from "gpui-kit";
import { h_flex } from "gpui-base";
import {
  checking,
  closeOverlay,
  exec,
  focused,
  machines,
  openOverlay,
  panes,
  props,
  run,
  theme,
  window as appWindow,
} from "herdr";

const PENDING = "vi-imitation.pending";
const REQ = "vi-imitation.req.";
const RES = "vi-imitation.res.";
const CLAIM = "vi-imitation.claim.";
// SPLIT は、新しい端末で動く本体の包んだスクリプトが自分に付けるラベルの名前である（値は依頼の番号。internal/mux の HerdrAppSplitLabel）。
const SPLIT = "vi-imitation.split";
// pane.split の後に、SPLIT のラベルの付いた端末とそのペインを探す上限。探しても見つからなかった依頼の端末は、ORPHAN_MS の間、
// 現れたら hc kill で閉じる。
const FIND_MS = 5000;
const ORPHAN_MS = 60000;
// 探す間隔。依頼があってから IDLE_AFTER_MS の間は POLL_MS ごとに、それより後は IDLE_POLL_MS ごとに探す。
// 本体は依頼を書いてから 10 秒（internal/mux の HerdrAppWait）待つので、静かな間の 2 秒は待ちに収まる。
// 別のマシンへの exec は SSH を通るので、依頼の無い間は探す回数を減らす（AGENTS.md の「poll gently」）。
const POLL_MS = 500;
const IDLE_POLL_MS = 2000;
const IDLE_AFTER_MS = 30000;
// hc を動かせなかったマシン（hc が無い、繋がらない）は、FAIL_BACKOFF_MS の間は探さない。
const FAIL_BACKOFF_MS = 30000;
const HC_TIMEOUT_MS = 10000;

// POPUP は、popup で起動した窓の端末に付けるラベルの名前である（値は依頼の番号。internal/mux の HerdrAppPopupLabel）。
const POPUP = "vi-imitation.popup";
// OPS は、hello で答える、この mod の知っている op である。
const OPS = ["split", "focus", "focused", "popup", "popup_close", "hello"];
// 窓の大きさの百分率の既定（依頼に無いとき。本体の既定と同じ 80%）と、画面の大きさを読めないときの窓の大きさ（論理ピクセル。仮）。
const POPUP_PERCENT = 80;
const POPUP_FALLBACK = { width: 960, height: 600 };
// MOD は、この mod のフォルダーの名前である（使い方の mods/vi-imitation/）。窓の上の帯に、この mod の写しを props で置く。
const MOD = "vi-imitation";
// TITLE_PX は、窓の上の帯（ドラッグと閉じるボタン）の高さ（論理ピクセル）である。
const TITLE_PX = 26;

// popups は、この herdr app に開いている（開いている途中を含む）窓である。{ on, from, id, terminal, overlay, tab } を持つ。
// 写しの間で共有し、1 つの herdr app に窓を 1 つだけ開く（どのタブの vi-imitation からの依頼でも同じ。使い方に書いた）。
const popups = new Set();
// watching は、ある写しが窓の端末の終わりを確かめている途中であることを表す（写しが重ねて閉じない）。
let watching = false;

// inFlight は、この herdr app の中で処理中の依頼である。mod の写しが複数あっても、同じ herdr app の中では 1 つの写しだけが処理する。
// モジュールのコードは写しの間で 1 つなので、写しの間で共有する（JavaScript は 1 本のスレッドで動くので、調べて足す間に割り込まれない）。
const inFlight = new Set();

export default class ViImitation extends View {
  init(_props, cx) {
    // 窓の上の帯の写し（props の popup に依頼の番号）は、依頼を探さず、ドラッグの取っ手と閉じるボタンだけを出す。
    // 窓が mod の外で消えたとき（レイアウトを替えた、herdr app を閉じた）は close が呼ばれるので、そこで窓の端末を終わらせる。
    const p = popupProps();
    if (p) {
      this.popup = String(p.popup);
      this.title = typeof p.title === "string" ? p.title : "vi-imitation";
      return;
    }
    this.busy = false;
    this.served = 0;
    this.status = "待機中";
    this.handled = new Set();
    this.lastPoll = 0;
    this.lastActive = 0;
    this.retryAt = new Map();
    this.orphans = [];
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
      // 窓が開いている間は、中のコマンドの終わりにすぐ気づけるように、短い間隔で探す。
      if (popups.size > 0) this.lastActive = Date.now();
      await watchPopups();
      await this.reapOrphans();
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

  // reapOrphans は、新しい端末を見つけられずに誤りを返した依頼の端末が後から現れていたら、hc kill で閉じる。
  // 期限（ORPHAN_MS）を過ぎた依頼は忘れる。
  async reapOrphans() {
    const now = Date.now();
    this.orphans = this.orphans.filter((o) => o.until > now);
    for (const o of [...this.orphans]) {
      try {
        const out = await hcRun(["ls", "--json", "--label", `${SPLIT}=${o.id}`], o.on);
        const terms = JSON.parse(out).terminals ?? [];
        for (const t of terms) await hcRun(["kill", t.address], o.on);
        if (terms.length > 0) this.orphans = this.orphans.filter((x) => x !== o);
      } catch {
        // 次の探索で読み直す。
      }
    }
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
        // run("pane.split") は、作ったペインと端末を返さないことがある（2026-11-26 にオーナーの herdr app で確かめた。#444）。
        // 返さなければ、新しい端末で動く本体の包んだスクリプトが自分に付けるラベル（SPLIT=<依頼の番号>）で端末を探し、そのペインを引く。
        let newPane = made && typeof made === "object" ? made.pane : null;
        let newTerm = made && typeof made === "object" && made.terminal ? local(made.terminal) : null;
        if (!newPane || !newTerm) {
          const found = await findSplit(on, req.id);
          if (!found.terminal) {
            // 後から端末が現れたら閉じる（待つ人のいないペインを残さない）。
            this.orphans.push({ on, id: req.id, until: Date.now() + ORPHAN_MS });
            return { error: `new terminal not found (label ${SPLIT}=${req.id})` };
          }
          newTerm = found.terminal;
          newPane = found.pane;
          if (!newPane) {
            await hcRun(["kill", newTerm], on).catch(() => {});
            return { error: `no pane shows the new terminal ${qual(newTerm)}` };
          }
        }
        const reply = { terminal: newTerm };
        if (req.direction === "left") {
          try {
            await command("pane.swap", { pane: newPane, target: from });
          } catch (e) {
            // 向きの違うペインを残さない。
            await command("pane.close", { pane: newPane }).catch(() => {});
            return { error: errorText(e) };
          }
        }
        try {
          await command("pane.focus", { pane: req.focus ? newPane : from });
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
      case "popup":
        return await openPopup(on, address, req);
      case "popup_close":
        await closePopups((p) => p.on === on && p.from === address, true);
        return {};
      case "hello":
        return { ops: OPS };
      default:
        // 本体（internal/mux の herdrAppUnknownOp）は、この前置きで古い mod を見分ける。
        return { error: `unknown op ${req.op}` };
    }
  }

  // close は、この写しのブロックが消えるときに 1 回呼ばれる（AGENTS.md の Cleanup）。窓の上の帯の写しなら、窓が mod の外で消えた
  // （レイアウトを替えた、閉じるボタン、herdr app を閉じた）ので、窓の端末を hc kill で終わらせて席を空ける。
  // mod が窓を閉じたとき（中のコマンドが終わった、popup_close）は、席は既に空いていて何もしない。
  async close(_cx) {
    if (this.popup) await endPopup(this.popup);
  }

  render(_cx) {
    const t = theme();
    if (this.popup) {
      // 帯を押すと窓をドラッグできる（帯は押下を止めない）。閉じるボタンだけは押下を止め、窓の端末を終わらせて窓を閉じる。
      return h_flex()
        .size_full()
        .items_center()
        .justify_between()
        .px(8)
        .bg(t.chrome)
        .text_size(11)
        .text_color(t.muted)
        .child(this.title)
        .child(
          div()
            .id("vi-imitation-popup-close")
            .px(6)
            .text_color(t.foreground)
            .hover((s) => s.bg(t.hover))
            .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
            .on_click(() => {
              endPopup(this.popup, true);
            })
            .child("×"),
        );
    }
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

// openPopup は、マシン on の端末 address からの popup の依頼 req を処理する（#452）。
// 窓が既に開いていれば busy を返す。そうでなければ、先に席を取ってから（hc run を待つ間に別の依頼が割り込まない）、
// req.argv を hc run --bg で起動する。--no-parent は、起動した端末を元の端末の子（sub-agent）として扱わせず、元の端末の
// プログラムが終わっても道連れにしないため、--no-notify は元の端末へ知らせの文字を打ち込ませないため、--rm は終わった端末を
// hc ls から消して終わりに気づくためである。herdr.* のラベルを付けないので、端末はタブに取り込まれない。
// 起動した端末を窓の中のブロックに出し、pane.focus {terminal} でキーボードをその端末へ移す（移せなくても窓は開いたままにする）。
async function openPopup(on, address, req) {
  if (popups.size > 0) return { busy: true, error: "a vi-imitation popup is already open" };
  if (!Array.isArray(req.argv) || req.argv.length === 0) return { error: "popup: empty argv" };
  let tab = null;
  try {
    tab = focused()?.tab ?? null;
  } catch {
    tab = null;
  }
  const seat = { on, from: address, id: req.id, terminal: null, overlay: null, tab };
  popups.add(seat);
  try {
    const args = ["run", "--bg", "--json", "--no-parent", "--no-notify", "--rm", "--label", `${POPUP}=${req.id}`];
    if (req.cwd) args.push("--cwd", req.cwd);
    args.push("--", ...req.argv);
    const started = JSON.parse(await hcRun(args, on));
    let term = started?.address;
    if (!term) throw new Error("hc run: no terminal address");
    if (on && term.startsWith(`${on}/`)) term = term.slice(on.length + 1);
    seat.terminal = term;
    const qualTerm = on ? `${on}/${term}` : term;
    // 上に帯（この mod の写し。ドラッグの取っ手と閉じるボタン。窓が消えたら close で端末を終わらせる）、下に端末を置く（AGENTS.md の Windows の例）。
    seat.overlay = await openOverlay({
      kind: "window",
      ...popupSize(req.width, req.height),
      layout: {
        type: "column",
        children: [
          { type: "block", size: { px: TITLE_PX }, content: { mod: MOD, props: { popup: req.id, title: "vi-imitation" } } },
          { type: "block", content: { terminal: qualTerm } },
        ],
      },
    });
    await command("pane.focus", { terminal: qualTerm }).catch(() => {});
    return { terminal: term };
  } catch (e) {
    popups.delete(seat);
    if (seat.terminal) await hcRun(["kill", seat.terminal], on).catch(() => {});
    if (seat.overlay != null) closeOverlayQuietly(seat.overlay);
    return { error: errorText(e) };
  }
}

// popupSize は、窓の幅と高さ（論理ピクセル）を、百分率 wp と hp（0 なら POPUP_PERCENT）と画面の大きさ（window()）から決める。
// 画面の大きさを読めなければ POPUP_FALLBACK にする。
function popupSize(wp, hp) {
  let win = null;
  try {
    win = appWindow();
  } catch {
    win = null;
  }
  const w = Number(win?.width ?? win?.size?.width);
  const h = Number(win?.height ?? win?.size?.height);
  const pct = (p) => (p > 0 && p <= 100 ? p : POPUP_PERCENT) / 100;
  return {
    width: w > 0 ? Math.round(w * pct(wp)) : POPUP_FALLBACK.width,
    height: h > 0 ? Math.round(h * pct(hp)) : POPUP_FALLBACK.height,
  };
}

// watchPopups は、開いている窓の端末（ラベル POPUP=<番号>）が hc ls から消えていれば（中のコマンドが終わった）、窓を閉じて、
// 窓を開いた元の端末へキーボードを戻す。端末を読めなければ、次の探索で読み直す。
async function watchPopups() {
  if (watching || popups.size === 0) return;
  watching = true;
  try {
    for (const p of [...popups]) {
      if (p.overlay == null) continue; // 開いている途中
      let alive = true;
      try {
        const out = await hcRun(["ls", "--json", "--label", `${POPUP}=${p.id}`], p.on);
        alive = (JSON.parse(out).terminals ?? []).length > 0;
      } catch {
        continue;
      }
      if (alive) continue;
      // 窓を閉じる前に、利用者がまだ窓（かペインの無いところ）にいて、開いたときと同じタブにいるかを見る。
      // 窓の外を押して別のペインやタブへ移った利用者を、元の端末へ引き戻さない（#248 の部品のペインと同じ考え方）。
      const back = stillInPopup(p);
      await closePopups((x) => x === p, false);
      if (back) await command("pane.focus", { terminal: p.on ? `${p.on}/${p.from}` : p.from }).catch(() => {});
    }
  } finally {
    watching = false;
  }
}

// stillInPopup は、今のフォーカスが窓 p の端末か、ペインの無いところ（窓の中のブロックなど）で、窓を開いたときと同じタブにあるかを返す。
// 読めなければ戻さない。
function stillInPopup(p) {
  let f = null;
  try {
    f = focused();
  } catch {
    return false;
  }
  if (!f) return false;
  if (p.tab != null && f.tab != null && f.tab !== p.tab) return false;
  const term = p.on ? `${p.on}/${p.terminal}` : p.terminal;
  return f.terminal === term || f.terminal === p.terminal || !f.pane;
}

// endPopup は、依頼の番号 id の窓の席を空け、窓の端末を hc kill で終わらせる。close なら窓も閉じる（閉じるボタン）。
// 席が無ければ（mod が既に閉じた）何もしない。
async function endPopup(id, close = false) {
  for (const p of [...popups]) {
    if (p.id !== id) continue;
    popups.delete(p);
    if (close && p.overlay != null) closeOverlayQuietly(p.overlay);
    if (p.terminal) await hcRun(["kill", p.terminal], p.on).catch(() => {});
  }
}

// popupProps は、この写しが窓の上の帯なら、その props（{ popup, title }）を返す。そうでなければ null を返す。
function popupProps() {
  try {
    const p = props();
    return p && p.popup != null ? p : null;
  } catch {
    return null;
  }
}

// closePopups は、match に合う開いた窓を閉じる。kill なら、先に窓の端末を hc kill で終わらせる。
async function closePopups(match, kill) {
  for (const p of [...popups]) {
    if (!match(p) || p.overlay == null) continue;
    popups.delete(p);
    if (kill && p.terminal) await hcRun(["kill", p.terminal], p.on).catch(() => {});
    closeOverlayQuietly(p.overlay);
  }
}

// closeOverlayQuietly は、窓 id を閉じる。既に閉じていて誤りになっても無視する。
function closeOverlayQuietly(id) {
  try {
    const r = closeOverlay(id);
    if (r && typeof r.catch === "function") r.catch(() => {});
  } catch {
    // 既に閉じている。
  }
}

// findSplit は、マシン on で、ラベル SPLIT=<id> の付いた端末と、その端末を出しているペインを、FIND_MS まで探す。
// 新しい端末の中のスクリプトがラベルを付けるまでと、herdr app の panes() に新しいペインが出るまでを待つ。
// 返すのは { terminal, pane } で、見つからなければそれぞれ null である。
// mod は setTimeout を使えないので、別のマシンでは sleep を挟み、このコンピューターでは読み直す回数を限る。
async function findSplit(on, id) {
  const deadline = Date.now() + FIND_MS;
  let terminal = null;
  for (let i = 0; i < 50 && Date.now() < deadline; i++) {
    if (!terminal) {
      const out = await hcRun(["ls", "--json", "--label", `${SPLIT}=${id}`], on).catch(() => "{}");
      let terms = [];
      try {
        terms = JSON.parse(out).terminals ?? [];
      } catch {
        terms = [];
      }
      terminal = terms[0]?.address ?? null;
    }
    if (terminal) {
      const pane = await paneOf(on, terminal).catch(() => null);
      if (pane) {
        await hcRun(["label", terminal, `${SPLIT}=`], on).catch(() => {});
        return { terminal, pane };
      }
    }
    if (on) await exec("sleep", ["0.1"], { on, timeout: HC_TIMEOUT_MS }).catch(() => {});
  }
  return { terminal, pane: null };
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
