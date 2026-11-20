// AuthClip 前端：长期链接（#a= / #b=）+ 回调链接自动解析
import { LANGUAGES, getLocale, pickLocale, setLocale, t } from "./i18n.mjs";
//
// 说明：浏览器不允许读取其它标签页的地址栏，也不允许读 ERR_CONNECTION_REFUSED
// 页面的 DOM。因此「自动抓取 localhost 回调」的可行路径只有两条：
//   1) B 复制地址栏 → 粘进输入框（本文件自动切出 code / state）
//   2) 点「读剪贴板」→ navigator.clipboard.readText()（一次手势，Chrome 允许）
// 两条都走同一个 parseCallback()，不依赖任何服务端出站请求。

// 部署在子路径（如 /clip/）时，页面内的请求必须相对当前目录解析，
// 否则 /api/... 会打到根站（另一个应用）上。
const PAGE_ROOT = new URL("./", document.baseURI).href;
const apiURL = (p) => new URL(String(p).replace(/^\/+/, ""), PAGE_ROOT).href;

const appEl = document.querySelector("#app");
const connEl = document.querySelector("#conn");
const roleEl = document.querySelector("#role");
const langSel = document.querySelector("#lang");
const whoEl = document.querySelector("#who");

// 语言：手动选择优先，其次浏览器语言；默认简体
let manualLocale = localStorage.getItem("ac-locale") || "auto";
function applyLocale() {
  setLocale(pickLocale(manualLocale, navigator.languages));
  document.documentElement.lang = getLocale();
}

const state = {
  role: "",
  token: "",
  me: null,
  board: { members: [], slots: [] },
  mine: { slots: [], seen: { ip: "", region: "" } },
  geoUrl: "",
  feedback: "",
  feedbackKind: "",
  busy: false,
  // 每个任务只有一份「当前轮次」状态：gen 一变整份作废。
  // 表单和手动读取剪贴板共用它。
  rounds: new Map(),
  reveal: new Set(),
  resubmit: new Set(), // B 端点「重新提交」后临时展开表单
  ipRisk: null,        // B 端出口 IP 画像（提交前预览）
  me: null,            // 当前身份 {label, username}
  stopStream: null,
};

// ---------------------------------------------------------------- DOM

function el(tag, props = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v === false || v == null) continue;
    if (k === "class") n.className = v;
    else if (k === "text") n.textContent = t(v);
    else if (k === "placeholder" || k === "title" || k === "aria-label") n.setAttribute(k, t(String(v)));
    else if (k.startsWith("on") && typeof v === "function") n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    n.append(c instanceof Node ? c : document.createTextNode(t(String(c))));
  }
  return n;
}

// 内联 HTML（如图示 SVG）里的文案也要翻译：直接遍历文本节点
function translateTree(root) {
  for (const n of root.querySelectorAll("*")) {
    for (const child of [...n.childNodes]) {
      if (child.nodeType === Node.TEXT_NODE) {
        const out = t(child.textContent);
        if (out !== child.textContent) child.textContent = out;
      }
    }
    for (const attr of ["placeholder", "title", "aria-label"]) {
      const v = n.getAttribute?.(attr);
      if (v) n.setAttribute(attr, t(v));
    }
  }
}

// ---- IP 画像的展示口径（与导出列保持一致）----
function riskScore(r) {
  if (!r) return "";
  return String(r.trust_score ?? "");
}
function riskKind(r) {
  if (!r) return "";
  if (r.is_residential) return "住宅";
  if (r.is_datacenter) return "机房";
  if (r.kind === "mobile") return "移动";
  return r.kind || "未知";
}
function riskFlags(r) {
  if (!r) return "";
  const f = [];
  if (r.is_vpn) f.push("VPN");
  if (r.is_proxy) f.push("代理");
  if (r.is_tor) f.push("Tor");
  if (r.is_crawler) f.push("爬虫");
  if (r.is_abuser) f.push("滥用");
  if (r.is_datacenter) f.push("数据中心");
  return f.length ? f.join("/") : "无";
}
function riskOrg(r) {
  if (!r) return "";
  const org = r.org || "";
  return r.asn ? `${org} (AS${r.asn})` : org;
}
function riskPlace(r) {
  if (!r) return "";
  const place = r.city ? `${r.country} / ${r.city}` : (r.country || "");
  return r.rdns ? `${place} · ${r.rdns}` : place;
}

const mono = (t, cls = "mono") => el("span", { class: cls, text: t ?? "" });

function feedbackLine() {
  if (!state.feedback) return null;
  return el("p", { class: `feedback ${state.feedbackKind}`, text: state.feedback });
}

function flash(text, kind = "ok") {
  state.feedback = text;
  state.feedbackKind = kind;
  render();
}

// 错误 → 可读文案（之前这个函数根本不存在，服务端报错会抛 ReferenceError）
function errorMessage(err, fallback = "操作失败，请重试") {
  const msg = err && err.message ? String(err.message) : "";
  if (err?.status === 429) return "尝试过于频繁，请稍后重试";
  if (err?.status === 401) return "链接无效或已被更换，请向发链接的人索取新的长期链接。";
  if (msg && /[\u4e00-\u9fa5]/.test(msg)) return msg; // 服务端已给中文
  return msg || fallback;
}

// 统一的「忙」执行器：先复位 busy 再渲染，避免错误后按钮被画成灰的却再也不恢复。
// fn 内部只改 state.feedback，不直接 render（否则还是 busy=true 的那一帧）。
async function busyRun(fn, { fallback = "操作失败，请重试", focus = "" } = {}) {
  if (state.busy) return false;
  state.busy = true;
  state.feedback = "";
  render();
  let err = null;
  try {
    await fn();
  } catch (e) {
    err = e;
  }
  state.busy = false;
  if (err) {
    state.feedback = errorMessage(err, fallback);
    state.feedbackKind = "err";
    render();
    if (focus) document.getElementById(focus)?.focus();
    return false;
  }
  render();
  return true;
}

async function copy(text, label = "已复制") {
  if (!text) return flash("没有可复制的内容", "err");
  try {
    await navigator.clipboard.writeText(text);
    flash(label);
  } catch {
    window.prompt("手动复制", text);
  }
}

function timeLabel(v) {
  if (!v) return "";
  const d = new Date(v);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit",
  }).format(d);
}

// ---------------------------------------------------------------- 路由与鉴权

function readToken() {
  const h = location.hash.slice(1);
  const m = /^([ab])=(.+)$/.exec(h);
  if (!m) return null;
  return { role: m[1] === "a" ? "admin" : "member", token: decodeURIComponent(m[2]) };
}

async function api(path, opts = {}) {
  const res = await fetch(apiURL(path), {
    cache: "no-store",
    method: opts.method ?? (opts.body ? "POST" : "GET"),
    headers: {
      "X-Token": state.token,
      ...(opts.body ? { "Content-Type": "application/json" } : {}),
    },
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = { error: text }; }
  if (!res.ok) {
    const err = new Error(data?.error || `HTTP ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return data;
}

// SSE over fetch：token 走请求头，绝不进 URL / 反代日志
async function startStream() {
  stopStream();
  let closed = false;
  const ctl = new AbortController();
  state.stopStream = () => { closed = true; ctl.abort(); };

  const run = async () => {
    while (!closed) {
      try {
        const res = await fetch(apiURL("/api/stream"), {
          headers: { "X-Token": state.token },
          cache: "no-store",
          signal: ctl.signal,
        });
        if (!res.ok) throw new Error(String(res.status));
        connEl.className = "conn on";
        connEl.textContent = t("实时已连接");
        const reader = res.body.getReader();
        const dec = new TextDecoder();
        let buf = "";
        while (!closed) {
          const { value, done } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true });
          let i;
          while ((i = buf.indexOf("\n\n")) >= 0) {
            const chunk = buf.slice(0, i);
            buf = buf.slice(i + 2);
            if (chunk.includes("event: change")) void refresh();
          }
        }
      } catch {
        if (closed) return;
        connEl.className = "conn off";
        connEl.textContent = t("断开，重试中…");
        await new Promise((r) => setTimeout(r, 2000));
      }
    }
  };
  void run();
}

function stopStream() {
  if (state.stopStream) state.stopStream();
  state.stopStream = null;
}

// 「当前授权轮次」的唯一载体：按任务 id 存一份，带 link_gen。
// 换链接后，丢弃上一轮的表单草稿。
function roundFor(slot) {
  const gen = slot.link_gen ?? 0;
  const cur = state.rounds.get(slot.id);
  if (cur && cur.gen === gen) return cur;
  const fresh = {
    gen,
    callback: slot.callback || "",
    email: "",        // 服务端不下发已提交内容，B 端每次都重新填
    password: "",
    regIp: slot.reg_ip || state.mine.seen?.ip || "",
    regRegion: slot.reg_region || state.mine.seen?.region || "",
    nodeIp: slot.node_ip || "",
  };
  state.rounds.set(slot.id, fresh);
  return fresh;
}

// expectGen：写入方在「读」之前看到的代次。异步路径必须传：
// 否则 await 期间换链、round 被删又重建之后，旧数据会写进新轮次。
function patchRound(id, patch, expectGen) {
  // 强制约定：写入必须声明「我看到的是第几代」。
  // - 异步路径（剪贴板、await 之后）：传读之前拿到的 gen
  // - 同步路径（用户正在输入）：传 null，表示"就是当前这一轮"
  // 不传 = 编程错误，直接抛，避免又出现一个"只按 id 写"的入口。
  if (expectGen === undefined) {
    throw new Error("patchRound: expectGen is required (pass null for synchronous writes)");
  }
  const cur = state.rounds.get(id);
  if (!cur) return false;
  if (expectGen !== null && cur.gen !== expectGen) return false;
  state.rounds.set(id, { ...cur, ...patch });
  return true;
}

function dropStaleRounds() {
  const gen = new Map((state.mine.slots || []).map((s) => [s.id, s.link_gen ?? 0]));
  let dropped = 0;
  for (const [id, r] of state.rounds) {
    if (gen.get(id) !== r.gen) {
      state.rounds.delete(id);
      dropped++;
    }
  }
  if (dropped > 0) {
    state.feedback = "授权链接已更新，之前的草稿已清空，请重新粘贴回调链接。";
    state.feedbackKind = "err";
  }
}

async function refresh() {
  try {
    if (state.role === "admin") {
      state.board = await api("/api/board");
      state.me = (state.board.users || []).find((x) => x.self) || null;
    } else {
      state.mine = await api("/api/mine");
  if (state.role === "member") {
    try { state.ipRisk = (await api("/api/ipcheck")).risk || null; } catch { state.ipRisk = null; }
  }
      state.me = { label: state.mine.label, username: state.mine.username };
      dropStaleRounds();
    }
  } catch (e) {
    if (e.status === 401) {
      stopStream();
      state.token = "";
      state.role = "";
      state.feedback = "链接无效或已被更换，请向发链接的人索取新的长期链接。";
      state.feedbackKind = "err";
    } else {
      state.feedback = e.message;
      state.feedbackKind = "err";
    }
  }
  render();
}

// ---------------------------------------------------------------- 回调解析

// 只做字符串解析，不发任何请求
function parseCallback(text) {
  const s = (text || "").trim();
  if (!s) return null;
  const notCode = (c) => !c || ["true", "false", "1", "0"].includes(c.toLowerCase());
  try {
    const u = new URL(s);
    // 授权链接本身带 code=true / code_challenge / response_type，必须先排除
    if (u.searchParams.get("code_challenge") || u.searchParams.get("response_type") ||
        u.searchParams.get("redirect_uri") || u.searchParams.get("scope")) return { auth: true };
    const code = u.searchParams.get("code");
    if (code && !notCode(code)) return { code, state: u.searchParams.get("state") || "", raw: s };
  } catch { /* 不是完整 URL */ }
  if (/[?&]?code=/.test(s)) {
    try {
      const q = new URLSearchParams(s.replace(/^[^?]*\?/, ""));
      const code = q.get("code");
      if (code && !notCode(code)) return { code, state: q.get("state") || "", raw: s };
    } catch { /* ignore */ }
  }
  // 只拿到授权码（没有整条回调链接）：放行，但标记来源未校验
  if (!/^https?:/i.test(s) && !/\s/.test(s) && s.length >= 16) {
    return { code: s, state: "", raw: s, bare: true };
  }
  return null;
}

// 从 A 的授权链接里取 state，用来校验 B 交回来的 state 是不是同一条流程
function stateOfLink(link) {
  try {
    return new URL(link).searchParams.get("state") || "";
  } catch {
    return "";
  }
}

// ---------------------------------------------------------------- 管理员视图

function newSlotForm() {
  const members = state.board.members || [];
  // 按用户名选：输入框 + datalist，输入用户名或名字都能匹配；服务端按用户名解析
  const pick = el("input", {
    id: "member", list: "member-list", autocomplete: "off", spellcheck: "false",
    placeholder: members.length ? "输入用户名，如 b01" : "先添加接收人",
  });
  const list = el("datalist", { id: "member-list" },
    members.filter((m) => !m.revoked).map((m) =>
      el("option", { value: m.username, label: `${m.label}（${m.username}）` })),
  );
  const resolved = el("p", { class: "hint" });
  const sync = () => {
    const v = pick.value.trim().toLowerCase();
    const hit = members.find((m) => m.username === v || m.id === v ||
      String(m.label).toLowerCase() === v);
    resolved.textContent = !v ? "" : hit
      ? (hit.revoked ? `⚠「${hit.label}」已吊销，不能下发` : `→ ${hit.label}（${hit.username}）`)
      : "⚠ 没有匹配的接收人";
    resolved.className = `hint ${v && !hit ? "warn-text" : ""}`;
  };
  pick.addEventListener("input", sync);

  const link = el("textarea", { id: "new-link", class: "tall", spellcheck: "false", placeholder: "粘贴 Claude 授权链接（原样保存，不会尝试访问）" });
  const label = el("input", { id: "new-label", maxlength: 60, placeholder: "备注，例如 家宽-日本" });

  const submit = async (event) => {
    event.preventDefault();
    if (state.busy) return;
    const v = pick.value.trim();
    const hit = members.find((m) => m.username === v.toLowerCase() || m.id === v ||
      String(m.label).toLowerCase() === v.toLowerCase());
    if (!hit) return flash("请填写接收人用户名", "err");
    if (hit.revoked) return flash("该接收人已吊销", "err");
    if (!link.value.trim()) {
      state.feedback = "请先粘贴授权链接";
      state.feedbackKind = "err";
      render();
      link.focus();
      return;
    }
    await busyRun(async () => {
      await api("/api/slots", {
        body: { member: hit.id, link: link.value, label: label.value },
      });
      state.feedback = "已下发；对方打开自己的长期链接就能看到";
      state.feedbackKind = "ok";
      await refresh();
    });
  };

  return el("form", { class: "card stack", onsubmit: submit },
    el("h2", { text: "下发授权链接" }),
    el("div", { class: "row" },
      el("div", { class: "grow" }, el("label", {}, "接收人（用户名）", pick, list, resolved)),
      el("div", { class: "grow" }, el("label", {}, "备注", label)),
    ),
    el("label", {}, "授权链接", link),
    feedbackLine(),
    el("div", { class: "row" }, el("button", { class: "primary", type: "submit", text: "下发", disabled: state.busy })),
    el("p", { class: "hint", text: "用户名叫不准时可直接输名字，下方会显示匹配到谁。服务端只保存链接字符串：不预取、不校验可达性。" }),
  );
}

// 长期链接默认打码：A 经常截屏/投屏，token 不该顺手泄露
function maskLink(link) {
  return String(link).replace(/([#?][ab]=)(.{4})[^&#]*/, (_, p, head) => `${p}${head}••••••••••••`);
}

function memberPanel() {
  const input = el("input", { id: "new-member", maxlength: 40, placeholder: "新接收人名字" });
  const uname = el("input", { id: "new-member-username", maxlength: 32, autocomplete: "off", placeholder: "用户名（选填，中英文均可，如 b03 / 张三）" });
  const add = async (event) => {
    event.preventDefault();
    if (state.busy) return;
    await busyRun(async () => {
      const m = await api("/api/members", { body: { label: input.value, username: uname.value } });
      state.feedback = `已创建「${m.label}」，长期链接已可复制`;
      state.feedbackKind = "ok";
      await refresh();
    });
  };
  const list = (state.board.members || []).map((m) => {
    // 长期链接不再随列表下发（防批量泄露）：要发就重新签发一条，只显示一次
    return el("div", { class: "member-row" },
      el("span", { class: "name", text: m.label }),
      mono(m.username || ""),
      m.revoked ? el("span", { class: "badge warn", text: "已吊销" }) : null,
      mono("链接不在列表中显示"),
      el("span", { class: "grow" }),
      el("button", {
        type: "button", text: "查看链接", disabled: m.revoked,
        onclick: async () => {
          try {
            const r = await api(`/api/users/${m.id}/link`);
            await copy(r.link, "已复制（此次查看已记审计）");
          } catch (e) { flash(e.message, "err"); }
        },
      }),
      el("button", {
        type: "button", text: "重新生成链接", disabled: m.revoked,
        onclick: async () => {
          try {
            const r = await api(`/api/users/${m.id}/rotate`, { method: "POST", body: {} });
            await copy(r.link, "新链接已生成并复制（旧链接立即失效）");
            await refresh();
          } catch (e) { flash(e.message, "err"); }
        },
      }),
    );
  });

  return el("div", { class: "card stack" },
    el("h2", { text: "接收人（长期链接）" }),
    el("div", { class: "members" }, list.length ? list : el("p", { class: "hint", text: "还没有接收人。" })),
    el("form", { class: "row", onsubmit: add },
      el("div", { class: "grow" }, el("label", {}, "新增接收人", input)),
      el("div", { class: "grow" }, el("label", {}, "用户名", uname)),
      el("button", { class: "primary", type: "submit", text: "创建并生成链接", disabled: state.busy }),
    ),
    el("p", { class: "hint", text: "这条链接长期有效、无需登录：转发即等于授权，务必只给对应的人。" }),
  );
}

// ---------------------------------------------------------------- 账号与权限

async function userAction(path, opts, okText) {
  try {
    await api(path, opts);
    flash(okText);
    await refresh();
  } catch (e) {
    flash(e.message, "err");
  }
}

function usersPanel() {
  if (!state.board.users) return null; // 只有 all 范围管理员能拿到

  const rows = state.board.users.map((u) => {
    const role = u.role === "admin" ? (u.scope === "own" ? "管理员·限自己" : "管理员·全量") : "接收人";
    return el("div", { class: "member-row" },
      el("span", { class: "name", text: u.label || "未命名" }),
      mono(u.username || ""),
      el("span", { class: "chip", text: role }),
      u.revoked ? el("span", { class: "badge warn", text: "已吊销" }) : el("span", { class: "badge filled", text: "有效" }),
      u.self ? el("span", { class: "badge", text: "当前账号" }) : null,
      el("span", { class: "grow" }),
      el("button", {
        type: "button", text: "轮换链接", disabled: u.self,
        onclick: async () => {
          try {
            const r = await api(`/api/users/${u.id}/rotate`, { method: "POST", body: {} });
            await copy(r.link, "新链接已生成并复制（旧链接立即失效）");
            await refresh();
          } catch (e) { flash(e.message, "err"); }
        },
      }),
      el("button", {
        type: "button", text: u.revoked ? "恢复" : "吊销", disabled: u.self,
        onclick: () => userAction(`/api/users/${u.id}`, { method: "PATCH", body: { revoke: !u.revoked } },
          u.revoked ? "已恢复访问" : "已吊销，链接立即失效"),
      }),
      u.role === "admin" ? el("button", {
        type: "button", text: u.scope === "own" ? "提为全量" : "降为限自己", disabled: u.self,
        onclick: () => userAction(`/api/users/${u.id}`, { method: "PATCH", body: { scope: u.scope === "own" ? "all" : "own" } },
          "权限范围已更新"),
      }) : null,
      el("button", {
        type: "button", class: "danger", text: "删除", disabled: u.self,
        onclick: () => userAction(`/api/users/${u.id}`, { method: "DELETE" }, "已删除"),
      }),
    );
  });

  const roleSel = el("select", {},
    el("option", { value: "member", text: "接收人" }),
    el("option", { value: "admin", text: "管理员" }),
  );
  const scopeSel = el("select", {},
    el("option", { value: "own", text: "只能管自己创建的任务" }),
    el("option", { value: "all", text: "全量（含账号与审计）" }),
  );
  const scopeWrap = el("div", { class: "grow" }, el("label", {}, "管理员范围", scopeSel));
  const toggleScope = () => { scopeWrap.hidden = roleSel.value !== "admin"; };
  roleSel.addEventListener("change", toggleScope);
  toggleScope();

  const labelInput = el("input", { maxlength: 40, placeholder: "名字" });
  const unameInput = el("input", { maxlength: 32, autocomplete: "off", placeholder: "用户名（选填）" });
  const create = async (event) => {
    event.preventDefault();
    if (state.busy) return;
    const ok = await busyRun(async () => {
      const r = await api("/api/users", {
        body: {
          role: roleSel.value, scope: scopeSel.value,
          label: labelInput.value, username: unameInput.value,
        },
      });
      await copy(r.link, "已创建，长期链接已复制（只显示这一次）");
      await refresh();
    });
    void ok;
  };

  return el("div", { class: "card stack" },
    el("h2", { text: "账号与权限" }),
    el("div", { class: "members" }, rows.length ? rows : el("p", { class: "hint", text: "只有当前账号。" })),
    el("form", { class: "row", onsubmit: create },
      el("div", { class: "grow" }, el("label", {}, "角色", roleSel)),
      scopeWrap,
      el("div", { class: "grow" }, el("label", {}, "名字", labelInput)),
      el("div", { class: "grow" }, el("label", {}, "用户名", unameInput)),
      el("button", { class: "primary", type: "submit", text: "创建", disabled: state.busy }),
    ),
    el("p", { class: "hint", text: "「限自己」的管理员只能看/管自己下发的任务，也拿不到接收人的长期链接；轮换与吊销会立即让对方当前链接失效。" }),
  );
}

function auditPanel() {
  if (!state.board.audit) return null;
  const rows = state.board.audit.slice(0, 12).map((a) =>
    el("div", { class: "member-row" },
      el("span", { class: "hint", text: timeLabel(a.at) }),
      el("span", { class: "chip", text: a.action }),
      el("span", { class: "name", text: a.label || a.actor.slice(0, 6) }),
      a.slot ? mono(`#${a.slot}`) : null,
      a.target ? mono(a.target) : null,
    ),
  );
  return el("div", { class: "card stack" },
    el("h2", { text: "操作审计（最近 12 条）" }),
    el("div", { class: "members" }, rows.length ? rows : el("p", { class: "hint", text: "暂无操作。" })),
    el("p", { class: "hint", text: "只记录「谁对哪条任务做了什么」，不含凭证内容。" }),
  );
}


// Go 的零值 time.Time 会序列化成 "0001-01-01T00:00:00Z"，在 JS 里是 truthy，
// 直接 if (slot.submitted) 会把「未提交」也当成「已回传」——必须显式判断。
function isSubmitted(slot) {
  const t = slot && slot.submitted;
  if (!t) return false;
  const s = String(t);
  return !s.startsWith("0001-01-01") && !s.startsWith("0001-01-01T00:00:00Z");
}

function slotStale(slot) {
  const ls = stateOfLink(slot.link);
  return Boolean(slot.state_param) && (!ls || slot.state_param !== ls);
}

function adminSlotCard(slot) {
  const member = (state.board.members || []).find((m) => m.id === slot.member);
  const filled = Boolean(slot.code || slot.email);
  const stateOfLinkValue = stateOfLink(slot.link);
  // 回传的 state 与当前链接对不上，或链接本身没有 state 无法核对 → 视为"可能不是本轮"
  const stale = slotStale(slot);
  const stateMismatch = stale;

  const edit = el("div", { class: "stack", hidden: true },
    (() => {
      const ta = el("textarea", { class: "tall", spellcheck: "false" });
      ta.value = slot.link;
      const keep = el("input", { type: "checkbox" });
      const hadResult = Boolean(slot.code || slot.email);
      return el("label", {}, "新的授权链接", ta,
        el("p", { class: "hint", text: hadResult
          ? "⚠ 这条任务已有回传：保存新链接会清空旧凭证/邮箱/IP（视为新一轮），除非勾选下面的保留。"
          : "保存后对方页面会实时刷新。" }),
        el("label", { class: "row" }, keep, el("span", { class: "hint", text: "保留已有回传（仅当链接写错但结果仍属本轮；会留痕）" })),
        el("div", { class: "row" },
          el("button", {
            type: "button", text: "保存",
            onclick: async () => {
              try {
                await api(`/api/slots/${slot.id}`, {
                  method: "PATCH",
                  body: { link: ta.value, keep_result: keep.checked },
                });
                flash(keep.checked ? "链接已更新，旧回传已保留（已留痕）" : "链接已更新，旧回传已清空");
                await refresh();
              } catch (e) { flash(e.message, "err"); }
            },
          }),
        ));
    })(),
  );

  const nodeInput = el("input", { placeholder: "静态家宽节点 IP（可留空）" });
  nodeInput.value = slot.node_ip || "";
  const regionInput = el("input", { placeholder: "地区，如 JP", maxlength: 2 });
  regionInput.value = slot.reg_region || "";

  const copyAll = [slot.code, slot.email, slot.password, slot.reg_region, slot.reg_ip, slot.node_ip]
    .filter(Boolean).join("\t");

  const facts = el("dl", { class: "facts" },
    fact("注册邮箱", slot.email),
    fact("登录密码", slot.password, true),
    fact("凭证 code", slot.code, true),
    fact("注册地区", slot.reg_region),
    fact("注册 IP", slot.reg_ip),
    fact("观测 IP", slot.seen_ip),
    fact("节点 IP", slot.node_ip),
    fact("信任分", riskScore(slot.risk)),
    fact("IP 类型", riskKind(slot.risk)),
    fact("风控标记", riskFlags(slot.risk)),
    fact("ASN / 组织", riskOrg(slot.risk)),
    fact("城市 / rDNS", riskPlace(slot.risk)),
    fact("时区 / 语言", [slot.tz, slot.lang].filter(Boolean).join(" · ")),
    fact("回传时间", timeLabel(slot.submitted)),
  );

  return el("article", { class: "slot" },
    el("div", { class: "slot-head" },
      el("div", { class: "row" },
        el("span", { class: `badge ${filled ? "filled" : ""}`, text: filled ? "已回传" : "待回传" }),
        el("span", { class: "slot-label", text: slot.label || "未命名" }),
        el("span", { class: "muted", text: `→ ${member ? member.label : "未知接收人"}` }),
        slot.unverified ? el("span", { class: "badge warn", text: "来源未校验" }) : null,
        stateMismatch ? el("span", { class: "badge warn", text: "state 不一致" }) : null,
        el("span", { class: "slot-id", text: `#${slot.id}` }),
      ),
      el("span", { class: "muted", text: timeLabel(slot.updated) }),
    ),
    el("div", { class: "split" },
      el("section", { class: "stack" },
        el("h3", { text: "授权链接" }),
        el("div", { class: "linkbox" },
          el("div", { class: "row" },
            el("a", { class: "btn primary", href: slot.link, target: "_blank", rel: "noopener noreferrer", text: "打开授权链接" }),
            el("button", { type: "button", text: "复制链接", onclick: () => copy(slot.link) }),
          ),
          mono(slot.link),
        ),
        el("div", { class: "row" },
          el("button", { type: "button", text: "改链接", onclick: () => { edit.hidden = !edit.hidden; } }),
          el("button", {
            type: "button", class: "danger", text: "清空回传", disabled: !filled,
            onclick: async () => {
              try {
                await api(`/api/slots/${slot.id}/clear`, { method: "POST", body: {} });
                flash("已清空");
                await refresh();
              } catch (e) { flash(e.message, "err"); }
            },
          }),
          el("button", {
            type: "button", text: "归档",
            onclick: async () => {
              try {
                await api(`/api/slots/${slot.id}`, { method: "DELETE" });
                flash("已归档：对方的「历史」里仍可见，看板不再显示");
                await refresh();
              } catch (e) { flash(e.message, "err"); }
            },
          }),
          state.board.scope === "all" ? el("button", {
            type: "button", class: "danger", text: "彻底删除",
            onclick: async () => {
              try {
                await api(`/api/slots/${slot.id}?purge=1`, { method: "DELETE" });
                flash("已彻底删除（对方历史也一并清除）");
                await refresh();
              } catch (e) { flash(e.message, "err"); }
            },
          }) : null,
        ),
        edit,
        el("div", { class: "grid-2" },
          el("label", {}, "节点 IP（管理员补充）", nodeInput),
          el("label", {}, "地区", regionInput),
        ),
        el("div", { class: "row" },
          el("button", {
            type: "button", text: "保存补充字段",
            onclick: async () => {
              try {
                await api(`/api/slots/${slot.id}`, {
                  method: "PATCH",
                  body: { node_ip: nodeInput.value, region: regionInput.value },
                });
                flash("已保存");
                await refresh();
              } catch (e) { flash(e.message, "err"); }
            },
          }),
        ),
      ),
      el("section", { class: "stack" },
        el("h3", { text: "B 回传" }),
        // 日常核对就放在这里：不靠导出，一眼看到"这条可能不是本轮"
        stale ? el("div", { class: "feedback err" },
          el("b", { text: "⚠ 这条回传与当前链接对不上。" }),
          el("span", { text: " " + (stateOfLinkValue
            ? "回调的 state 与当前授权链接不同，可能是换链接前的旧结果。"
            : "当前链接没有 state，无法核对来源，建议重新下发链接。") }),
          el("div", { class: "row" },
            el("button", {
              type: "button", class: "danger", text: "清空这条回传",
              onclick: async () => {
                try {
                  await api(`/api/slots/${slot.id}/clear`, { method: "POST", body: {} });
                  flash("已清空；请让接收人重新提交本轮结果");
                  await refresh();
                } catch (e) { flash(e.message, "err"); }
              },
            }),
          ),
        ) : null,
        facts,
        slot.callback ? el("div", {}, el("h3", { text: "原始回调链接" }), el("div", { class: "cred", text: slot.callback })) : null,
        slot.code ? el("div", { class: "cred", text: slot.code }) : null,
        el("div", { class: "row" },
          el("button", {
            type: "button", class: "primary", text: "复制凭证", disabled: !slot.code,
            onclick: () => {
              if (stale && !window.confirm("这条回传与当前链接不一致（可能来自上一轮）。\n仍要复制吗？")) return;
              void copy(slot.code, "凭证已复制");
            },
          }),
          el("button", {
            type: "button", text: "导出 IP 卡片", disabled: !slot.risk,
            onclick: () => { void downloadCard(slot); },
          }),
          el("button", {
            type: "button", text: "复制整行", disabled: !copyAll,
            onclick: () => {
              if (stale && !window.confirm("这条回传与当前链接不一致（可能来自上一轮）。\n仍要复制吗？")) return;
              void copy(copyAll, "已复制：code / 邮箱 / 密码 / 地区 / IP / 节点");
            },
          }),
        ),
      ),
    ),
  );
}

function fact(label, value, wide = false) {
  return el("div", {},
    el("dt", { text: label }),
    value ? el("dd", { class: wide ? "mono ink" : "", text: value }) : el("dd", { class: "empty", text: "—" }),
  );
}

// 导出：token 走请求头，用 fetch 拿 blob 再触发下载（<a href> 带不了头）
async function downloadExport(format, masked = false) {
  try {
    const qs = masked ? `format=${format}&masked=1` : `format=${format}`;
    const res = await fetch(apiURL(`/api/export?${qs}`), {
      headers: { "X-Token": state.token }, cache: "no-store",
    });
    if (!res.ok) {
      let msg = `HTTP ${res.status}`;
      try { msg = (await res.json()).error || msg; } catch { /* 非 JSON */ }
      throw new Error(msg);
    }
    const blob = await res.blob();
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = /filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") || "")?.[1] || `authclip.${format}`;
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
      flash(masked ? "已导出脱敏版（凭证/邮箱/IP 打码）" : "已导出（含明文账号密码，请勿外传）");
  } catch (e) {
    flash(e.message, "err");
  }
}

// 导出该任务的 IP 卡片（自建 SVG，数据来自已存画像）
async function downloadCard(slot) {
  try {
    const res = await fetch(apiURL(`/api/slots/${slot.id}/card.svg`), {
      headers: { "X-Token": state.token }, cache: "no-store",
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const blob = await res.blob();
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `authclip-${slot.id}-ip.svg`;
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
    flash("已导出 IP 卡片（SVG）");
  } catch (e) {
    flash(e.message, "err");
  }
}

function adminView() {
  const slots = state.board.slots || [];
  const all = state.board.scope === "all";
  return el("div", { class: "stack" },
    el("div", {},
      el("h1", { text: "授权中继" }),
      el("p", { class: "muted", text: "下发授权链接 → 对方打开并回传凭证 → 这里复制。" }),
      whoLine(),
    ),
    all ? el("div", { class: "row" },
      el("button", { type: "button", text: "导出 CSV", onclick: () => downloadExport("csv") }),
      el("button", { type: "button", text: "导出 JSON", onclick: () => downloadExport("json") }),
      el("button", { type: "button", text: "导出脱敏 CSV", onclick: () => { void downloadExport("csv", true); } }),
      el("span", { class: "hint", text: "CSV/JSON 默认含明文的邮箱、密码与凭证（方便直接复制）；脱敏版会把这几列打码。两种导出都会留审计。" }),
    ) : null,
    (() => {
      const n = slots.filter(slotStale).length;
      if (!n) return null;
      return el("div", { class: "feedback err" },
        el("b", { text: `⚠ 有 ${n} 条任务的回传与当前链接对不上` }),
        el("span", { text: "（多为换链接前的旧结果）。点开对应卡片可一键清空，或让接收人重新提交。" }),
      );
    })(),
    feedbackLine(),
    newSlotForm(),
    memberPanel(),
    usersPanel(),
    slots.length ? el("div", { class: "stack" }, slots.map(adminSlotCard))
      : el("div", { class: "card" }, el("p", { class: "muted", text: "还没有下发授权链接。" })),
    auditPanel(),
  );
}

// ---------------------------------------------------------------- 粘贴指引图

// 内联 SVG：不引外部图片、不用 inline style，CSP 的 style-src 'self' 也不会拦。
// 只教「从地址栏整条复制」，不碰出错页面的任何交互。
// 文字说明放在 SVG 外面（手机上 SVG 会缩，说明仍可读）。
function copyGuide() {
  const wrap = el("div", { class: "card stack" },
    el("div", { class: "guide" }),
    el("ol", { class: "steps" },
      el("li", {}, el("b", { text: "授权后跳转是正常的" }),
        el("span", { class: "hint", text: "：浏览器会停在 localhost:54545 并显示「无法访问」，因为本机没有监听这个端口。地址栏里那条就是回调链接。" })),
      el("li", {}, el("b", { text: "整条复制地址栏" }),
        el("span", { class: "hint", text: "：点一下地址栏让整条变蓝 → Ctrl+A → Ctrl+C；手机长按地址栏 → 全选 → 复制。" })),
      el("li", {}, el("b", { text: "回到本页粘贴" }),
        el("span", { class: "hint", text: "：在对应任务中粘贴，或点该任务的「读剪贴板并解析」。" })),
    ),
  );
  const guide = wrap.querySelector(".guide");
  guide.innerHTML = `
<svg viewBox="0 0 800 140" role="img" aria-label="从地址栏复制回调链接的示意图" xmlns="http://www.w3.org/2000/svg">
  <rect x="1" y="1" width="798" height="138" rx="14" fill="#fbfcfb" stroke="#e4ebe6"/>

  <rect x="18" y="24" width="252" height="92" rx="11" fill="#ffffff" stroke="#dfe6e1"/>
  <circle cx="36" cy="42" r="4" fill="#d6ded8"/>
  <circle cx="48" cy="42" r="4" fill="#d6ded8"/>
  <circle cx="60" cy="42" r="4" fill="#d6ded8"/>
  <rect x="28" y="54" width="232" height="30" rx="8" fill="#eef5f1" stroke="#146b4a" stroke-width="1.8"/>
  <text x="38" y="74" font-family="ui-monospace, Consolas, monospace" font-size="12" fill="#16211c">localhost:54545/callback?code=…</text>
  <rect x="28" y="90" width="232" height="18" rx="6" fill="#f6f8f6" stroke="#eef1ef" stroke-dasharray="4 4"/>

  <path d="M280 70 h28" stroke="#c8d4cd" stroke-width="2"/>
  <path d="M308 70 l-8 -5 v10 z" fill="#c8d4cd"/>

  <rect x="316" y="24" width="180" height="92" rx="11" fill="#ffffff" stroke="#dfe6e1"/>
  <rect x="332" y="40" width="54" height="30" rx="7" fill="#fbfcfb" stroke="#c8d4cd"/>
  <text x="359" y="60" text-anchor="middle" font-family="system-ui, sans-serif" font-size="12" fill="#16211c">Ctrl</text>
  <rect x="392" y="40" width="30" height="30" rx="7" fill="#fbfcfb" stroke="#c8d4cd"/>
  <text x="407" y="60" text-anchor="middle" font-family="system-ui, sans-serif" font-size="12" fill="#16211c">A</text>
  <rect x="332" y="78" width="54" height="30" rx="7" fill="#fbfcfb" stroke="#c8d4cd"/>
  <text x="359" y="98" text-anchor="middle" font-family="system-ui, sans-serif" font-size="12" fill="#16211c">Ctrl</text>
  <rect x="392" y="78" width="30" height="30" rx="7" fill="#fbfcfb" stroke="#c8d4cd"/>
  <text x="407" y="98" text-anchor="middle" font-family="system-ui, sans-serif" font-size="12" fill="#16211c">C</text>

  <path d="M504 70 h28" stroke="#c8d4cd" stroke-width="2"/>
  <path d="M532 70 l-8 -5 v10 z" fill="#c8d4cd"/>

  <rect x="540" y="24" width="244" height="92" rx="11" fill="#ffffff" stroke="#dfe6e1"/>
  <rect x="556" y="40" width="212" height="38" rx="8" fill="#ffffff" stroke="#146b4a" stroke-width="1.8" stroke-dasharray="6 5"/>
  <text x="568" y="64" font-family="system-ui, sans-serif" font-size="11.5" fill="#8b988f">粘贴到这里</text>
  <rect x="556" y="86" width="212" height="22" rx="6" fill="#e6f3ed" stroke="#bfe0d0"/>
  <text x="566" y="102" font-family="ui-monospace, Consolas, monospace" font-size="10.5" fill="#146b4a">code = rd5sfX1UAH…</text>
</svg>`;
  translateTree(wrap.querySelector(".guide"));
  return wrap;
}

// 弹窗打开授权链接：noopener 让授权页拿不到 window.opener（防反向跳转劫持），
function openAuth(slot) {
  window.open(slot.link, "authclip", "popup=yes,width=560,height=800,noopener");
  // noopener 时返回 null 是正常的，不能用它判断是否被拦。
  // 真弹窗会抢焦点，所以用「500ms 后本页是否还持有焦点」做启发式。
  setTimeout(() => {
    if (document.hasFocus()) flash("没看到弹窗？用「复制链接」在浏览器里打开也一样。");
  }, 500);
}

// ---------------------------------------------------------------- 接收人视图

function memberCard(slot) {
  // 已归档：只读历史，不再显示表单（链接也可能已失效）
  if (slot.deleted) {
    return el("article", { class: "slot" },
      el("div", { class: "slot-head" },
        el("div", { class: "row" },
          el("span", { class: "badge", text: "已结束" }),
          el("span", { class: "slot-label", text: slot.label || "授权任务" }),
          el("span", { class: "slot-id", text: `#${slot.id}` }),
        ),
        el("span", { class: "muted", text: timeLabel(slot.deleted_at || slot.updated) }),
      ),
      el("dl", { class: "facts" },
        fact("授权链接", slot.link, true),
        fact("注册邮箱", slot.email),
        fact("登录密码", slot.password, true),
    fact("凭证 code", slot.code, true),
        fact("注册地区", slot.reg_region),
        fact("注册 IP", slot.reg_ip),
        fact("节点 IP", slot.node_ip),
        fact("回传时间", timeLabel(slot.submitted)),
      ),
      slot.code ? el("div", { class: "row" },
        el("button", { type: "button", text: "复制凭证", onclick: () => copy(slot.code, "凭证已复制") }),
      ) : null,
    );
  }

  // 表单草稿属于当前任务和轮次。
  const round = roundFor(slot);
  // 输入是同步的：写的就是当前这一轮，显式传 round.gen（轮次已变则写入被拒）
  const set = (patch) => {
    if (!patchRound(slot.id, patch, round.gen)) void refresh();
  };

  const parsed = parseCallback(round.callback);
  const linkState = stateOfLink(slot.link);
  const match = parsed && parsed.state && linkState ? (parsed.state === linkState) : null;

  const email = el("input", { type: "email", autocomplete: "off", maxlength: 254, placeholder: "注册这个账号用的邮箱" });
  // 有些账号要密码登录；默认 type=password，可一键显示
  const passwordInput = el("input", { type: "password", autocomplete: "new-password", maxlength: 200, placeholder: "该账号的登录密码（不需要可留空）" });
  email.value = round.email;
  email.addEventListener("input", () => set({ email: email.value }));
  passwordInput.value = round.password;
  passwordInput.addEventListener("input", () => set({ password: passwordInput.value }));

  const callback = el("textarea", {
    class: "tall", spellcheck: "false",
    placeholder: "把地址栏那条 http://localhost:54545/callback?code=…&state=… 整条粘进来",
  });
  callback.value = round.callback;
  const handlePaste = () => {
    const text = callback.value;
    set({ callback: text });
    const p = parseCallback(text);
    if (p?.code) flash("已识别 code，可提交", "ok");
    else if (p?.auth) flash("这是授权链接本身，不是回调链接", "err");
    else if (text.trim()) flash("没识别到 code=，请确认粘的是回调链接", "err");
    else render();
    if (p?.code) render();
  };
  callback.addEventListener("input", handlePaste);
  callback.addEventListener("paste", () => setTimeout(handlePaste, 0));

  const readClip = async () => {
    const expectGen = round.gen; // 点击那一刻的代次
    try {
      const text = await navigator.clipboard.readText();
      const p = parseCallback(text);
      // await 期间可能换链：代次不符就不写，避免旧剪贴板进新轮次
      if (!patchRound(slot.id, { callback: text }, expectGen)) {
        flash("授权链接已更新，剪贴板内容未填入，请重新打开授权链接。", "err");
        render();
        return;
      }
      callback.value = text;
      flash(p?.code ? "已从剪贴板取到 code" : "剪贴板里没有 code=", p?.code ? "ok" : "err");
      render();
    } catch {
      flash("浏览器没给剪贴板权限，请手动粘贴", "err");
    }
  };

  const regIp = el("input", { placeholder: "注册区域 IP（默认填服务端观测值）" });
  regIp.value = round.regIp;
  regIp.addEventListener("input", () => set({ regIp: regIp.value }));

  const regRegion = el("input", { maxlength: 2, placeholder: "US" });
  regRegion.value = round.regRegion;
  regRegion.addEventListener("input", () => set({ regRegion: regRegion.value.toUpperCase() }));

  const nodeIp = el("input", { placeholder: "静态家宽节点 IP（选填）" });
  nodeIp.value = round.nodeIp;
  nodeIp.addEventListener("input", () => set({ nodeIp: nodeIp.value }));

  const submit = async (event) => {
    event.preventDefault();
    if (state.busy) return;
    // 本地先挡一道：空邮箱/空回调不必往返服务端，也不会把按钮卡住
    if (!email.value.trim()) {
      state.feedback = "请先填写注册邮箱";
      state.feedbackKind = "err";
      render();
      email.focus();
      return;
    }
    if (!callback.value.trim()) {
      state.feedback = "请先粘贴回调链接，或单独粘贴授权码";
      state.feedbackKind = "err";
      render();
      callback.focus();
      return;
    }
    const ok = await busyRun(async () => {
      await api(`/api/slots/${slot.id}/report`, {
        body: {
          email: email.value,
          password: passwordInput.value,
          callback: callback.value,
          // 浏览器环境信号：自动带上，不打扰用户
          tz: (() => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch { return ""; } })(),
          lang: navigator.language || "",
          code: "",
          reg_ip: regIp.value,
          reg_region: regRegion.value,
          node_ip: nodeIp.value,
          link_gen: roundFor(slot).gen,
        },
      });
      state.rounds.delete(slot.id);
      state.resubmit.delete(slot.id); // 收起表单，改为只看摘要
      state.feedback = "已回传，内容已从本页清空；需要修改请点「重新提交」";
      state.feedbackKind = "ok";
      await refresh();
    }, { focus: "report-email" });
    void ok;
  };

  // 提交成功后 B 端只看摘要：凭证不再回显，也不再通过接口下发
  const submittedSummary = () => el("div", { class: "stack" },
    el("p", { class: "hint", text: "已提交，本页不再显示内容（避免凭证留在浏览器里）。需要修改请点「重新提交」。" }),
    el("dl", { class: "facts" },
      fact("提交时间", timeLabel(slot.submitted)),
      fact("注册邮箱", slot.email),
      fact("注册地区", slot.reg_region),
    ),
    el("div", { class: "row" },
      el("button", { type: "button", class: "primary", text: "重新提交", onclick: () => { state.resubmit.add(slot.id); render(); } }),
    ),
  );

  return el("article", { class: "slot" },
    el("div", { class: "slot-head" },
      el("div", { class: "row" },
        el("span", { class: `badge ${isSubmitted(slot) ? "filled" : ""}`, text: isSubmitted(slot) ? "已回传" : "待处理" }),
        el("span", { class: "slot-label", text: slot.label || "授权任务" }),
        el("span", { class: "slot-id", text: `#${slot.id}` }),
      ),
      el("span", { class: "muted", text: timeLabel(slot.updated) }),
    ),
    el("div", { class: "row" },
      el("a", {
        class: "btn primary", href: slot.link, target: "_blank", rel: "noopener noreferrer",
        text: "打开授权链接",
        onclick: (e) => {
          // 允许 Ctrl/⌘ 点击走浏览器原生新标签；普通点击走小窗
          if (e.metaKey || e.ctrlKey || e.shiftKey) return;
          e.preventDefault();
          openAuth(slot);
        },
      }),
      el("button", { type: "button", text: "复制链接", onclick: () => copy(slot.link) }),
    ),
    isSubmitted(slot) ? null : el("p", { class: "hint", text: "完成后回到对应任务，手动粘贴或点「读剪贴板并解析」。" }),
    (isSubmitted(slot) && !state.resubmit.has(slot.id)) ? submittedSummary() : el("form", { class: "stack", onsubmit: submit },
      el("label", {}, "回调链接（粘贴整条，自动提取 code）", callback),
      el("div", { class: "row" },
        el("button", { type: "button", text: "读剪贴板并解析", onclick: readClip }),
        el("span", { class: "grow" }),
      ),
      parsed?.code
        ? el("div", { class: "picked" },
            el("span", { class: "k", text: "已提取 code" }),
            el("span", { class: "v", text: parsed.code }),
            parsed.bare ? el("span", { class: "k warn", text: "未带 state，来源未校验" }) : null,
            match === true ? el("span", { class: "k", text: "state 与本条授权链接一致 ✓" }) : null,
            match === false ? el("span", { class: "k", text: "state 与本条链接不一致，确认是不是拿错了链接" }) : null,
          )
        : null,
      parsed?.auth ? el("p", { class: "feedback err", text: "这是授权链接本身（含 code_challenge），不是回调链接。请先点开完成授权。" }) : null,
      el("div", { class: "grid-2" },
        el("label", {}, "注册邮箱", email),
        el("label", {}, "登录密码（如该账号需要）", passwordInput),
      ),
      el("div", { class: "grid-2" },
        el("label", {}, "注册地区（两位国家码）", regRegion),
        el("span", {}),
      ),
      el("div", { class: "grid-2" },
        el("label", {}, "注册区域 IP", regIp),
        el("label", {}, "静态家宽节点 IP（选填）", nodeIp),
      ),
      el("div", { class: "row" },
        el("button", { class: "primary", type: "submit", text: isSubmitted(slot) ? "重新提交" : "提交回传", disabled: state.busy }),
        el("button", { type: "button", text: "用观测值填 IP/地区", onclick: () => {
          const seen = state.mine.seen || {};
          set({ regIp: seen.ip || round.regIp, regRegion: seen.region || round.regRegion });
          render();
        } }),
      ),
    ),
  );
}

function memberView() {
  const all = state.mine.slots || [];
  const active = all.filter((s) => !s.deleted);
  const history = all.filter((s) => s.deleted);
  const seen = state.mine.seen || {};
  // 画像拿不到时（上游不可用）退化为只显示出口 IP，保证「复制 IP」始终可用
  const prof = state.ipRisk || (seen.ip ? { ip: seen.ip, degraded: true } : null);
  return el("div", { class: "stack" },
    el("div", {},
      el("h1", { text: "我的授权" }),
      el("p", { class: "muted", text: "点开对方给的授权链接，完成授权后把地址栏的回调链接粘回来。" }),
      whoLine(),
    ),
    prof ? el("div", { class: "card stack" },
      el("h3", { text: "出口 IP 画像" }),
      el("dl", { class: "facts" },
        fact("IP", prof.ip),
        fact("信任分", prof.degraded ? "—" : riskScore(prof)),
        fact("IP 类型", prof.degraded ? "—" : riskKind(prof)),
        fact("风控标记", prof.degraded ? "—" : riskFlags(prof)),
        fact("ASN / 组织", prof.degraded ? "—" : riskOrg(prof)),
        fact("地区", seen.region || "—"),
      ),
      el("div", { class: "row" },
        el("button", { type: "button", text: "复制 IP", disabled: !prof.ip, onclick: () => copy(prof.ip, "IP 已复制") }),
        el("span", { class: "grow" }),
      ),
      el("p", { class: "hint", text: "提交时会自动记录这份画像，也会写入「注册区域 IP / 地区」，都可以自己改。" }),
    ) : null,
    feedbackLine(),
    active.length ? copyGuide() : null,
    active.length ? el("div", { class: "stack" }, active.map(memberCard)) : null,
    !active.length && !history.length
      ? el("div", { class: "card" }, el("p", { class: "muted", text: "还没有收到授权任务，等对方下发。" }))
      : null,
    history.length ? el("div", { class: "stack" },
      el("h2", { text: "历史（只读）" }),
      el("p", { class: "hint", text: "对方结束或归档的任务留在这里，方便你回看自己提交过什么。" }),
      history.map(memberCard),
    ) : null,
  );
}

// ---------------------------------------------------------------- 落地页

function landing() {
  const input = el("input", { placeholder: "把发给你的长期链接整条粘进来" });
  const go = () => {
    const v = input.value.trim();
    const m = /#([ab])=(.*)$/.exec(v);
    if (!m) return flash("链接里没有 #a= 或 #b=，请检查", "err");
    location.hash = `${m[1]}=${m[2]}`;
    location.reload();
  };
  return el("div", { class: "card stack" },
    el("h1", { text: "请用你的长期链接打开" }),
    el("p", { class: "muted", text: "链接形如 https://…/#a=…（管理员）或 https://…/#b=…（接收人）。不需要登录。" }),
    feedbackLine(),
    el("label", {}, "长期链接", input),
    el("div", { class: "row" }, el("button", { class: "primary", type: "button", text: "进入", onclick: go })),
  );
}

// ---------------------------------------------------------------- 渲染与启动

// 「我是谁」：名字 + 用户名，A/B 两端一眼可辨
function whoText() {
  const me = state.me || {};
  const name = me.label || "";
  const user = me.username ? "@" + me.username : "";
  return [name, user].filter(Boolean).join(" ");
}

// 页面正文里的身份行（服务端角色 + 名字 + 用户名）
function whoLine() {
  if (!state.me) return null;
  const role = state.role === "admin"
    ? (state.me.scope === "own" ? "管理员·限自己" : "管理员·全量")
    : "接收人";
  return el("p", { class: "muted who-line", text: `${t("当前身份")}：${whoText()}（${t(role)}）` });
}

function render() {
  // 身份芯片已经写明「名字 @用户名（角色在正文那行）」，角色徽标就不再重复占位
  roleEl.hidden = !state.role || Boolean(state.me);
  connEl.hidden = !state.role;
  if (state.me) {
    whoEl.hidden = false;
    whoEl.textContent = whoText();
  } else {
    whoEl.hidden = true;
  }
  if (state.role) {
    roleEl.className = `chip ${state.role === "admin" ? "admin" : ""}`;
    roleEl.textContent = t(state.role === "admin" ? "管理员" : "接收人");
    // 连接状态文案由 SSE 直接写 textContent，切换语言时要跟着重刷
    connEl.textContent = t(connEl.classList.contains("on") ? "实时已连接"
      : connEl.classList.contains("off") ? "断开，重试中…" : "连接中…");
  }
  const view = !state.token ? landing() : state.role === "admin" ? adminView() : memberView();
  appEl.setAttribute("aria-busy", "false");
  appEl.replaceChildren(view);
}

// 可选：由浏览器去查地域端点（配置了 -geo-url 才启用）。
// 服务端不出站，避免把 B 的 IP 交给别人的同时也避免服务端暴露。
async function resolveGeo() {
  if (!state.geoUrl || state.mine.seen?.region) return;
  try {
    const res = await fetch(state.geoUrl, { cache: "no-store" });
    const text = (await res.text()).slice(0, 4000);
    let region = "";
    try {
      const j = JSON.parse(text);
      const pick = j.country_code ?? j.countryCode ?? j.country ?? j.region ?? j.cc ?? "";
      region = typeof pick === "string" ? pick : (pick?.iso_code ?? "");
    } catch {
      const m = text.match(/\b([A-Z]{2})\b/);
      region = m ? m[1] : "";
    }
    region = String(region).toUpperCase().slice(0, 2);
    if (/^[A-Z]{2}$/.test(region) && state.mine.seen) {
      state.mine.seen.region = region;
      render();
    }
  } catch { /* 端点不可用就当没配 */ }
}

async function start() {
  const t = readToken();
  if (!t) return render();
  state.role = t.role;
  state.token = t.token;
  try {
    state.geoUrl = (await api("/api/config")).geo_url || "";
  } catch { /* 无配置也能用 */ }
  await refresh();
  if (state.token) startStream();
  if (state.role === "member") void resolveGeo();
}

document.querySelector("#home").addEventListener("click", (e) => {
  if (!state.token) return;
  e.preventDefault();
  location.hash = "";
  stopStream();
  state.token = "";
  state.role = "";
  render();
});

window.addEventListener("hashchange", () => { stopStream(); void start(); });
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState !== "visible" || !state.token) return;
  void refresh();
});

// 语言选择器
applyLocale();
for (const [value, label] of LANGUAGES) {
  const opt = el("option", { value, text: label });
  if (value === manualLocale) opt.selected = true;
  langSel.append(opt);
}
langSel.addEventListener("change", () => {
  manualLocale = langSel.value;
  localStorage.setItem("ac-locale", manualLocale);
  applyLocale();
  render();
});

void start();

























