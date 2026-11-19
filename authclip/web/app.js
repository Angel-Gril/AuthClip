// AuthClip 前端：长期链接（#a= / #b=）+ 回调链接自动解析
//
// 说明：浏览器不允许读取其它标签页的地址栏，也不允许读 ERR_CONNECTION_REFUSED
// 页面的 DOM。因此「自动抓取 localhost 回调」的可行路径只有两条：
//   1) B 复制地址栏 → 粘进输入框（本文件自动切出 code / state）
//   2) 点「读剪贴板」→ navigator.clipboard.readText()（一次手势，Chrome 允许）
// 两条都走同一个 parseCallback()，不依赖任何服务端出站请求。

const appEl = document.querySelector("#app");
const connEl = document.querySelector("#conn");
const roleEl = document.querySelector("#role");

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
  drafts: new Map(),
  reveal: new Set(),
  stopStream: null,
};

// ---------------------------------------------------------------- DOM

function el(tag, props = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v === false || v == null) continue;
    if (k === "class") n.className = v;
    else if (k === "text") n.textContent = v;
    else if (k.startsWith("on") && typeof v === "function") n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    n.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return n;
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
  const res = await fetch(path, {
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
        const res = await fetch("/api/stream", {
          headers: { "X-Token": state.token },
          cache: "no-store",
          signal: ctl.signal,
        });
        if (!res.ok) throw new Error(String(res.status));
        connEl.className = "conn on";
        connEl.textContent = "实时已连接";
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
        connEl.textContent = "断开，重试中…";
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

async function refresh() {
  try {
    if (state.role === "admin") {
      state.board = await api("/api/board");
    } else {
      state.mine = await api("/api/mine");
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
  const select = el("select", { id: "member" },
    el("option", { value: "", text: members.length ? "选择接收人" : "先添加接收人" }),
    members.map((m) => el("option", { value: m.id, text: `${m.label} · ${m.id.slice(0, 4)}` })),
  );
  const link = el("textarea", { id: "new-link", class: "tall", spellcheck: "false", placeholder: "粘贴 Claude 授权链接（原样保存，不会尝试访问）" });
  const label = el("input", { id: "new-label", maxlength: 60, placeholder: "备注，例如 家宽-日本" });

  const submit = async (event) => {
    event.preventDefault();
    if (state.busy) return;
    state.busy = true;
    try {
      await api("/api/slots", {
        body: { member: select.value, link: link.value, label: label.value },
      });
      flash("已下发；对方打开自己的长期链接就能看到");
      await refresh();
    } catch (e) {
      flash(e.message, "err");
    } finally {
      state.busy = false;
    }
  };

  return el("form", { class: "card stack", onsubmit: submit },
    el("h2", { text: "下发授权链接" }),
    el("div", { class: "row" },
      el("div", { class: "grow" }, el("label", {}, "接收人", select)),
      el("div", { class: "grow" }, el("label", {}, "备注", label)),
    ),
    el("label", {}, "授权链接", link),
    feedbackLine(),
    el("div", { class: "row" }, el("button", { class: "primary", type: "submit", text: "下发", disabled: state.busy })),
    el("p", { class: "hint", text: "服务端只保存字符串：不预取、不校验可达性、不发往任何第三方。" }),
  );
}

// 长期链接默认打码：A 经常截屏/投屏，token 不该顺手泄露
function maskLink(link) {
  return String(link).replace(/([#?][ab]=)(.{4})[^&#]*/, (_, p, head) => `${p}${head}••••••••••••`);
}

function memberPanel() {
  const input = el("input", { id: "new-member", maxlength: 40, placeholder: "新接收人名字" });
  const add = async (event) => {
    event.preventDefault();
    if (state.busy) return;
    state.busy = true;
    try {
      const m = await api("/api/members", { body: { label: input.value } });
      flash(`已创建「${m.label}」，长期链接已可复制`);
      await refresh();
    } catch (e) {
      flash(e.message, "err");
    } finally {
      state.busy = false;
    }
  };
  const list = (state.board.members || []).map((m) => {
    // 长期链接不再随列表下发（防批量泄露）：要发就重新签发一条，只显示一次
    return el("div", { class: "member-row" },
      el("span", { class: "name", text: m.label }),
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
  const create = async (event) => {
    event.preventDefault();
    if (state.busy) return;
    state.busy = true;
    try {
      const r = await api("/api/users", {
        body: { role: roleSel.value, scope: scopeSel.value, label: labelInput.value },
      });
      await copy(r.link, "已创建，长期链接已复制（只显示这一次）");
      await refresh();
    } catch (e) {
      flash(e.message, "err");
    } finally {
      state.busy = false;
    }
  };

  return el("div", { class: "card stack" },
    el("h2", { text: "账号与权限" }),
    el("div", { class: "members" }, rows.length ? rows : el("p", { class: "hint", text: "只有当前账号。" })),
    el("form", { class: "row", onsubmit: create },
      el("div", { class: "grow" }, el("label", {}, "角色", roleSel)),
      scopeWrap,
      el("div", { class: "grow" }, el("label", {}, "名字", labelInput)),
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


function adminSlotCard(slot) {
  const member = (state.board.members || []).find((m) => m.id === slot.member);
  const filled = Boolean(slot.code || slot.email);
  const stateOfLinkValue = stateOfLink(slot.link);
  const stateMismatch = slot.state_param && stateOfLinkValue && slot.state_param !== stateOfLinkValue;

  const edit = el("div", { class: "stack", hidden: true },
    (() => {
      const ta = el("textarea", { class: "tall", spellcheck: "false" });
      ta.value = slot.link;
      return el("label", {}, "新的授权链接", ta, el("div", { class: "row", style: null },
        el("button", {
          type: "button", text: "保存",
          onclick: async () => {
            try {
              await api(`/api/slots/${slot.id}`, { method: "PATCH", body: { link: ta.value } });
              flash("链接已更新，对方页面会实时刷新");
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

  const copyAll = [slot.code, slot.email, slot.reg_region, slot.reg_ip, slot.node_ip]
    .filter(Boolean).join("\t");

  const facts = el("dl", { class: "facts" },
    fact("注册邮箱", slot.email),
    fact("凭证 code", slot.code, true),
    fact("注册地区", slot.reg_region),
    fact("注册 IP", slot.reg_ip),
    fact("观测 IP", slot.seen_ip),
    fact("节点 IP", slot.node_ip),
    fact("回传时间", timeLabel(slot.submitted)),
  );

  return el("article", { class: "slot" },
    el("div", { class: "slot-head" },
      el("div", { class: "row" },
        el("span", { class: `badge ${filled ? "filled" : ""}`, text: filled ? "已回传" : "待回传" }),
        el("span", { class: "slot-label", text: slot.label || "未命名" }),
        el("span", { class: "muted", text: `→ ${member ? member.label : "未知接收人"}` }),
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
        facts,
        slot.callback ? el("div", {}, el("h3", { text: "原始回调链接" }), el("div", { class: "cred", text: slot.callback })) : null,
        slot.code ? el("div", { class: "cred", text: slot.code }) : null,
        el("div", { class: "row" },
          el("button", { type: "button", class: "primary", text: "复制凭证", disabled: !slot.code, onclick: () => copy(slot.code, "凭证已复制") }),
          el("button", { type: "button", text: "复制整行", disabled: !copyAll, onclick: () => copy(copyAll, "已复制：code / 邮箱 / 地区 / IP / 节点") }),
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
async function downloadExport(format, secrets = false) {
  try {
    const qs = secrets ? `format=${format}&secrets=1&confirm=EXPORT` : `format=${format}`;
    const res = await fetch(`/api/export?${qs}`, {
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
      flash(secrets ? "已导出完整凭据（含已归档）——这份文件请勿外传" : "已导出（默认脱敏，含已归档任务）");
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
    ),
    all ? el("div", { class: "row" },
      el("button", { type: "button", text: "导出 CSV", onclick: () => downloadExport("csv") }),
      el("button", { type: "button", text: "导出 JSON", onclick: () => downloadExport("json") }),
      el("button", {
        type: "button", class: "danger", text: "导出完整凭据",
        onclick: () => {
          if (!window.confirm("将导出明文凭证、邮箱与完整 IP，文件泄露等于凭证泄露。\n确认继续？")) return;
          void downloadExport("csv", true);
        },
      }),
      el("span", { class: "hint", text: "默认脱敏（凭证/邮箱/IP 打码）；完整导出会二次确认并留审计。" }),
    ) : null,
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
        el("span", { class: "hint", text: "：粘进下面的框，系统自动切出 code 并核对 state；从弹窗切回来时会先自动试一次剪贴板。" })),
    ),
  );
  wrap.querySelector(".guide").innerHTML = `
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
  return wrap;
}

// 弹窗打开授权链接：noopener 让授权页拿不到 window.opener（防反向跳转劫持），
// 代价是拿不到 handle，所以「用户是否回来了」靠 visibilitychange 判断。
function openAuth(slot) {
  state.pendingSlot = slot.id;
  const w = window.open(slot.link, "authclip", "popup=yes,width=560,height=800,noopener");
  // noopener 时返回 null 是正常的，不能用它判断是否被拦。
  // 真弹窗会抢焦点，所以用「500ms 后本页是否还持有焦点」做启发式。
  setTimeout(() => {
    if (document.hasFocus()) flash("没看到弹窗？用「复制链接」在浏览器里打开也一样。");
  }, 500);
  void w;
}

// 用户从授权页切回来时，自动尝试读剪贴板并填入（没权限就静默跳过，不打扰）
async function tryAutoPaste() {
  const id = state.pendingSlot;
  if (!id || state.role !== "member") return;
  try {
    const text = await navigator.clipboard.readText();
    const p = parseCallback(text);
    if (!p?.code) return;
    const cur = state.drafts.get(id) || {};
    state.drafts.set(id, { ...cur, callback: text });
    state.pendingSlot = "";
    flash("已从剪贴板自动填入 code，核对后提交");
  } catch { /* 没有剪贴板权限：留给按钮 */ }
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

  const draft = state.drafts.get(slot.id) || {
    email: slot.email || "",
    callback: slot.callback || "",
    regIp: slot.reg_ip || state.mine.seen?.ip || "",
    regRegion: slot.reg_region || state.mine.seen?.region || "",
    nodeIp: slot.node_ip || "",
  };
  // 每次输入都合并进当前草稿：不能直接用闭包里那份 draft，
  // 否则表单重渲染后旧闭包会把它覆盖回去（丢字段）
  const set = (patch) => {
    const cur = state.drafts.get(slot.id) || draft;
    state.drafts.set(slot.id, { ...cur, ...patch });
  };

  const parsed = parseCallback(draft.callback);
  const linkState = stateOfLink(slot.link);
  const match = parsed && parsed.state && linkState ? (parsed.state === linkState) : null;

  const email = el("input", { type: "email", autocomplete: "off", maxlength: 254, placeholder: "注册这个账号用的邮箱" });
  email.value = draft.email;
  email.addEventListener("input", () => set({ email: email.value }));

  const callback = el("textarea", {
    class: "tall", spellcheck: "false",
    placeholder: "把地址栏那条 http://localhost:54545/callback?code=…&state=… 整条粘进来",
  });
  callback.value = draft.callback;
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
    try {
      const text = await navigator.clipboard.readText();
      callback.value = text;
      set({ callback: text });
      const p = parseCallback(text);
      flash(p?.code ? "已从剪贴板取到 code" : "剪贴板里没有 code=", p?.code ? "ok" : "err");
      render();
    } catch {
      flash("浏览器没给剪贴板权限，请手动粘贴", "err");
    }
  };

  const regIp = el("input", { placeholder: "注册区域 IP（默认填服务端观测值）" });
  regIp.value = draft.regIp;
  regIp.addEventListener("input", () => set({ regIp: regIp.value }));

  const regRegion = el("input", { maxlength: 2, placeholder: "US" });
  regRegion.value = draft.regRegion;
  regRegion.addEventListener("input", () => set({ regRegion: regRegion.value.toUpperCase() }));

  const nodeIp = el("input", { placeholder: "静态家宽节点 IP（选填）" });
  nodeIp.value = draft.nodeIp;
  nodeIp.addEventListener("input", () => set({ nodeIp: nodeIp.value }));

  const submit = async (event) => {
    event.preventDefault();
    if (state.busy) return;
    state.busy = true;
    try {
      await api(`/api/slots/${slot.id}/report`, {
        body: {
          email: email.value,
          callback: callback.value,
          code: "",
          reg_ip: regIp.value,
          reg_region: regRegion.value,
          node_ip: nodeIp.value,
        },
      });
      state.drafts.delete(slot.id);
      flash("已回传，对方会立刻看到；再提交一次可覆盖");
      await refresh();
    } catch (e) {
      flash(e.message, "err");
    } finally {
      state.busy = false;
    }
  };

  return el("article", { class: "slot" },
    el("div", { class: "slot-head" },
      el("div", { class: "row" },
        el("span", { class: `badge ${slot.code ? "filled" : ""}`, text: slot.code ? "已回传" : "待处理" }),
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
    el("p", { class: "hint", text: "开在小窗里授权；完成后切回本页会自动尝试从剪贴板填入 code。小窗留在 localhost:54545 打不开是正常的，可以关掉。" }),
    el("form", { class: "stack", onsubmit: submit },
      el("label", {}, "回调链接（粘贴整条，自动提取 code）", callback),
      el("div", { class: "row" },
        el("button", { type: "button", text: "读剪贴板并解析", onclick: readClip }),
        el("span", { class: "grow" }),
      ),
      parsed?.code
        ? el("div", { class: "picked" },
            el("span", { class: "k", text: "已提取 code" }),
            el("span", { class: "v", text: parsed.code }),
            match === true ? el("span", { class: "k", text: "state 与本条授权链接一致 ✓" }) : null,
            match === false ? el("span", { class: "k", text: "state 与本条链接不一致，确认是不是拿错了链接" }) : null,
          )
        : null,
      parsed?.auth ? el("p", { class: "feedback err", text: "这是授权链接本身（含 code_challenge），不是回调链接。请先点开完成授权。" }) : null,
      el("div", { class: "grid-2" },
        el("label", {}, "注册邮箱", email),
        el("label", {}, "注册地区（两位国家码）", regRegion),
      ),
      el("div", { class: "grid-2" },
        el("label", {}, "注册区域 IP", regIp),
        el("label", {}, "静态家宽节点 IP（选填）", nodeIp),
      ),
      el("div", { class: "row" },
        el("button", { class: "primary", type: "submit", text: slot.code ? "更新回传" : "提交回传", disabled: state.busy }),
        el("button", { type: "button", text: "用观测值填 IP/地区", onclick: () => {
          const seen = state.mine.seen || {};
          set({ regIp: seen.ip || draft.regIp, regRegion: seen.region || draft.regRegion });
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
  return el("div", { class: "stack" },
    el("div", {},
      el("h1", { text: "我的授权" }),
      el("p", { class: "muted", text: "点开对方给的授权链接，完成授权后把地址栏的回调链接粘回来。" }),
    ),
    el("div", { class: "card row" },
      el("span", { class: "hint", text: "本次连接的出口" }),
      mono(seen.ip || "服务端取不到（内网/本地）", "mono ink"),
      seen.region ? el("span", { class: "chip", text: `地区 ${seen.region}` }) : null,
      el("button", { type: "button", text: "复制 IP", disabled: !seen.ip, onclick: () => copy(seen.ip, "IP 已复制") }),
      el("span", { class: "hint", text: "提交时会自动写入「注册区域 IP / 地区」，也可以自己改。" }),
    ),
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

function render() {
  roleEl.hidden = !state.role;
  connEl.hidden = !state.role;
  if (state.role) {
    roleEl.className = `chip ${state.role === "admin" ? "admin" : ""}`;
    roleEl.textContent = state.role === "admin" ? "管理员" : "接收人";
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
  // 从授权小窗切回来：先给剪贴板一点写入时间，再尝试自动填入
  if (state.pendingSlot) setTimeout(() => void tryAutoPaste(), 300);
});
window.addEventListener("focus", () => {
  if (state.pendingSlot) void tryAutoPaste();
});

void start();



