import PocketBase from "./vendor/pocketbase.es.mjs";
import { chooseLanguage, languages, messages, regions } from "./locale.mjs";

const pb = new PocketBase(location.origin);
const users = "portal_users";
const records = pb.collection("portal_tasks");
const preferred = navigator.languages?.length ? navigator.languages : [navigator.language];
const content = document.querySelector("#content");
const language = document.querySelector("#language");
const logout = document.querySelector("#logout");
const flows = {
  team: {
    steps: ["teamFirst", "teamSecond"],
    entries: {
      "team-site": ["openOfficial", "https://claude.ai/"],
      "team-guide": ["teamGuide", "https://support.claude.com/en/articles/13133750-manage-members-on-team-and-enterprise-plans"],
    },
  },
  code: {
    steps: ["codeFirst", "codeSecond"],
    entries: {
      "code-auth": ["openDocs", "https://code.claude.com/docs/en/authentication"],
      "code-setup": ["codeSetup", "https://code.claude.com/docs/en/setup"],
    },
  },
};
const issueTypes = ["invitation", "signin", "access", "other"];
const historyLifetime = 7 * 24 * 60 * 60 * 1000;
const drafts = new Map();
let assignDraft = { assignee: "", flow: "team", email: "" };
let country = "";
let manual = localStorage.getItem("portal-language") ?? "";
let locale = chooseLanguage(manual, country, preferred);
let account;
let tasks = new Map();
let members = [];
let waiting = true;
let busy = false;
let feedback = "";
let loadError = false;
let generation = 0;
let taskChanges = 0;

const t = (key) => messages[locale][key];
const esc = (value) => String(value ?? "").replace(/[&<>"']/g, (char) =>
  ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char],
);
const selected = (value, current) => value === current ? " selected" : "";
const options = (values, current) => values.map(([value, label]) =>
  `<option value="${esc(value)}"${selected(value, current)}>${esc(label)}</option>`,
).join("");
const taskURL = (id) => `/#task=${encodeURIComponent(id)}`;
const fragment = (key) => new URLSearchParams(location.hash.slice(1)).get(key) ?? "";
const activeId = () => fragment("task");
const admin = () => account?.role === "admin";
const memberName = (id) => members.find((member) => member.id === id)?.email ?? "";
const disabled = () => busy ? " disabled" : "";
const expiredLabel = (task) => task.flow === "team" ? "expired" : "codeExpired";
const pill = (task) => `<span class="pill ${esc(task.state)}">${esc(t(task.state === "expired" ? expiredLabel(task) : task.state))}</span>`;
const notice = () => `<p role="status" class="feedback${["copied", "dataCopied"].includes(feedback) ? " success" : ""}">${esc(t(feedback))}</p>`;

function regionName(code) {
  return code && code !== "ZZ" ? new Intl.DisplayNames(locale, { type: "region" }).of(code) : t("unknown");
}

function regionOptions() {
  const names = new Intl.DisplayNames(locale, { type: "region" });
  const compare = new Intl.Collator(locale).compare;
  return [["ZZ", t("unknown")], ...regions.map((code) => [code, names.of(code)])
    .sort((left, right) => compare(left[1], right[1]))];
}

function dateLabel(value) {
  const date = value && new Date(value.replace(" ", "T"));
  return date ? new Intl.DateTimeFormat(locale, { dateStyle: "medium" }).format(date) : "";
}

function activityTime(value) {
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function emailField(label, name, value, required, autocomplete = "off") {
  return `<label>${esc(t(label))}<input id="${name}" name="${name}" type="email" dir="ltr"
    autocomplete="${autocomplete}" maxlength="254" value="${esc(value)}"${required ? " required" : ""}></label>`;
}

function loginPage(email) {
  return `<section class="login">
    <div><h1>${esc(t("login"))}</h1><p class="muted">${esc(t("loginHelp"))}</p></div>
    <form id="login-form" class="form" novalidate>
      ${emailField("email", "login-email", email, true, "username")}
      <label>${esc(t("password"))}<input id="password" name="password" dir="ltr" type="password" autocomplete="current-password" required></label>
      ${notice()}
      <button class="primary"${disabled()}>${esc(t(busy ? "loading" : "login"))}</button>
    </form></section>`;
}

function assignmentForm() {
  const available = members.map((member) => [member.id, member.email]);
  return `<aside class="panel assign">
    <h2>${esc(t("newTask"))}</h2>
    <form id="assign-form" class="form" novalidate>
      <label>${esc(t("selectMember"))}<select id="assignee" name="assignee" required>${options([["", "—"], ...available], assignDraft.assignee)}</select></label>
      <label>${esc(t("flow"))}<select id="flow" name="flow">${options(Object.keys(flows).map((key) => [key, t(key)]), assignDraft.flow)}</select></label>
      ${emailField("taskEmail", "prefill-email", assignDraft.email, false)}
      <div class="form-actions">
        <button class="primary"${disabled()}>${esc(t("newTask"))}</button>
        <button type="button" id="copy-inbox" class="quiet"${assignDraft.assignee ? "" : " disabled"}>${esc(t("copyInbox"))}</button>
      </div>
    </form>
  </aside>`;
}

function taskGroup(title, list) {
  return `<section class="task-group"><h2>${esc(t(title))}</h2>${list.length
    ? `<ul class="task-list">${list.map((task) => `<li class="task-row">
        <div><a href="${taskURL(task.id)}">${esc(t(task.flow))}</a><div class="meta muted">
          ${admin() ? `<bdi>${esc(memberName(task.assignee))}</bdi>` : ""}
          <time datetime="${esc(task.updated)}">${esc(dateLabel(task.updated))}</time>
          ${["renewed", "restarted", "entryUpdated"].includes(task.timeline?.at(-1)?.kind)
            ? `<span>${esc(t(task.timeline.at(-1).kind))}</span>` : ""}
        </div></div>${pill(task)}
      </li>`).join("")}</ul>` : `<p class="muted empty">${esc(t("empty"))}</p>`}</section>`;
}

function taskList() {
  const list = [...tasks.values()].sort((a, b) => b.updated.localeCompare(a.updated));
  return `<section>
    <div class="workspace-title">
      <p class="eyebrow">${esc(t(account.role))}<bdi>${esc(account.email)}</bdi></p>
      <h1>${esc(t(admin() ? "assignedTasks" : "myTasks"))}</h1>
    </div>
    ${notice()}
    <div class="${admin() ? "columns" : ""}">
      <div>${taskGroup("active", list.filter((task) => task.state !== "confirmed"))}
      ${taskGroup("history", list.filter((task) => task.state === "confirmed"))}</div>
      ${admin() ? assignmentForm() : ""}
    </div></section>`;
}

function reportForm(task) {
  const draft = drafts.get(task.id) ?? {
    email: task.email || account.email,
    ip: task.ip || "",
    region: task.region || (regions.includes(country) ? country : "ZZ"),
    outcome: task.state === "issue" ? "issue" : "submitted",
    issue: task.issue || "signin",
  };
  const showIssue = draft.outcome === "issue";
  const outcomes = [["submitted", t("done")], ["issue", t("issue")]];
  return `<form id="report-form" class="form" novalidate>
    ${emailField("taskEmail", "report-email", draft.email, true)}
    <div class="network">
      <label>${esc(t("ip"))}<input id="report-ip" name="ip" dir="ltr" maxlength="45"
        value="${esc(draft.ip)}" aria-describedby="ip-help" autocomplete="off"></label>
      <p class="muted" id="ip-help">${esc(t("ipHelp"))}</p>
      <button type="button" id="detect-ip" class="quiet"${disabled()}>${esc(t("detectIp"))}</button>
    </div>
    <label>${esc(t("region"))}<select id="region" name="region">${options(regionOptions(), draft.region)}</select></label>
    <label>${esc(t("result"))}<select id="outcome" name="outcome">${options(outcomes, draft.outcome)}</select></label>
    <label id="issue-field"${showIssue ? "" : " hidden"}>${esc(t("issueType"))}<select id="issue-type" name="issue"
      ${showIssue ? "required" : "disabled"}>${options(issueTypes.filter((key) => key !== "invitation" || task.flow === "team")
        .map((key) => [key, t(key)]), draft.issue)}</select></label>
    <button class="primary"${disabled()}>${esc(t("submit"))}</button>
  </form>`;
}

function facts(task) {
  const fields = [
    ...(admin() ? [["assignedTo", memberName(task.assignee)]] : []),
    ["created", dateLabel(task.created)], ["taskEmail", task.email],
    ["ip", task.ip], ["region", task.region && regionName(task.region)], ["issueType", t(task.issue)],
    ["keptUntil", task.confirmed_at && activityTime(new Date(task.confirmed_at.replace(" ", "T")).getTime() + historyLifetime)],
  ];
  return `<dl class="facts">${fields.filter(([, value]) => value).map(([label, value]) =>
    `<div><dt>${esc(t(label))}</dt><dd><bdi>${esc(value)}</bdi></dd></div>`,
  ).join("")}</dl>`;
}

function unavailablePage() {
  return `<a class="back" href="/#">${esc(t("allTasks"))}</a><h1>${esc(t("unavailable"))}</h1>`;
}

function entryKey(task) {
  const entries = flows[task.flow].entries;
  return Object.hasOwn(entries, task.entry) ? task.entry : Object.keys(entries)[0];
}

function detail(task) {
  const back = `<a class="back" href="/#">${esc(t("allTasks"))}</a>`;
  if (!task) return unavailablePage();
  const { steps, entries } = flows[task.flow];
  const entry = entryKey(task);
  const [link, url] = entries[entry];
  const isTeamAdmin = admin() && task.flow === "team";
  const entryForm = admin() && task.state !== "confirmed" ? `<form id="entry-form" class="form entry-form">
    <label>${esc(t("updateEntry"))}<select id="official-entry" name="entry">${options(
      Object.entries(entries).map(([key, [label]]) => [key, t(label)]), entry,
    )}</select></label>
    <button class="quiet"${disabled()}>${esc(t("updateEntry"))}</button></form>` : "";
  const timeline = [...(task.timeline ?? [])].reverse().map((event) =>
    `<li><span>${esc(t(event.kind))}</span> <time datetime="${esc(event.at)}">${esc(activityTime(event.at))}</time></li>`,
  ).join("");
  return `${back}
    <div class="task-heading"><h1>${esc(t(task.flow))}</h1>${pill(task)}</div>
    ${notice()}
    <div class="columns">
      <section class="guide">
        <h2>${esc(t("flow"))}</h2>
        <ol>${steps.map((step) => `<li>${esc(t(step))}</li>`).join("")}</ol>
        <a href="${esc(url)}" target="_blank" rel="noopener noreferrer">${esc(t(link))}</a>
        ${isTeamAdmin ? `<p><a href="https://claude.ai/admin-settings/members" target="_blank" rel="noopener noreferrer">${esc(t("manageTeam"))}</a></p>` : ""}
        <div class="actions">
          <button type="button" id="copy-link" class="quiet">${esc(t("copy"))}</button>
          <button type="button" id="copy-official" class="quiet">${esc(t("copyOfficial"))}</button>
        </div>
        ${entryForm}
        ${timeline ? `<div class="activity"><h2>${esc(t("timeline"))}</h2>
          <ol aria-live="polite">${timeline}</ol></div>` : ""}
      </section>
      <section class="panel">
        <h2>${esc(t("result"))}</h2>
        ${!admin() && (task.state === "pending" || task.state === "issue") ? reportForm(task) : facts(task)}
        ${admin() && task.region ? `<div class="actions">
          ${task.state === "submitted" ? `<button type="button" id="confirm" class="primary"${disabled()}>${esc(t("confirm"))}</button>` : ""}
          <button type="button" id="copy-data" class="quiet">${esc(t("copyData"))}</button>
        </div>` : ""}
      </section>
    </div>`;
}

function render() {
  const focus = document.activeElement;
  const loginEmail = document.querySelector("#login-email")?.value ?? "";
  const password = document.querySelector("#password")?.value;
  document.documentElement.lang = locale;
  document.documentElement.dir = locale === "ar" ? "rtl" : "ltr";
  document.title = t("brand");
  document.querySelector("#brand").textContent = t("brand");
  document.querySelector("#language-label").textContent = t("language");
  language.innerHTML = options([["", t("automatic")], ...Object.entries(languages)], manual);
  logout.textContent = t("logout");
  logout.hidden = !account;
  content.setAttribute("aria-busy", String(waiting));
  if (waiting) content.innerHTML = `<p role="status" class="muted">${esc(t("loading"))}</p>`;
  else if (!account) content.innerHTML = loginPage(loginEmail);
  else if (loadError) content.innerHTML = `${notice()}<button type="button" id="retry" class="quiet">${esc(t("retry"))}</button>`;
  else if (fragment("inbox") && !admin() && fragment("inbox") !== account.id) content.innerHTML = unavailablePage();
  else content.innerHTML = activeId() ? detail(tasks.get(activeId())) : taskList();
  const passwordField = document.querySelector("#password");
  if (passwordField && password !== undefined) passwordField.value = password;
  if (focus?.id && !focus.isConnected) document.getElementById(focus.id)?.focus({ preventScroll: true });
}

function errorMessage(error, fallback = "error") {
  if (error.status === 429) return "rateLimited";
  if (error.status === 409) return "changed";
  if (error.status === 404) return "unavailable";
  if (error.status === 400) return fallback === "loginError" ? fallback : "badInput";
  return fallback;
}

async function refresh(current) {
  const changesAtStart = taskChanges;
  try {
    const [list, people] = await Promise.all([
      records.getFullList({ sort: "-updated", requestKey: null }),
      admin() ? pb.send("/api/portal/members", { requestKey: null }) : [],
    ]);
    if (current !== generation) return;
    members = people;
    if (taskChanges === changesAtStart) {
      tasks = new Map(list.map((task) => [task.id, task]));
    } else {
      for (const task of list) {
        if (tasks.has(task.id)) setTask(task);
      }
      if (tasks.size !== list.length) void refresh(current);
    }
    loadError = false;
  } catch (error) {
    if (current !== generation) return;
    if (error.status === 401) {
      pb.authStore.clear();
      return;
    }
    if (waiting || loadError) {
      feedback = errorMessage(error);
      loadError = true;
    }
  }
  if (current === generation) {
    waiting = false;
    render();
  }
}

function setTask(task) {
  const current = tasks.get(task.id);
  if (!current || current.updated < task.updated) {
    tasks.set(task.id, task);
    taskChanges++;
  }
}

async function openWorkspace(record) {
  const current = ++generation;
  account = record;
  tasks = new Map();
  drafts.clear();
  assignDraft = { assignee: "", flow: "team", email: "" };
  members = [];
  loadError = false;
  feedback = "";
  waiting = Boolean(account);
  render();
  await pb.realtime.unsubscribe().catch(() => {});
  if (current !== generation || !record) return;
  const update = ({ action, record: task }) => {
    if (current !== generation) return;
    if (action === "delete") {
      tasks.delete(task.id);
      taskChanges++;
    }
    else setTask(task);
    if (!activeId() || activeId() === task.id) render();
  };
  void Promise.all([
    records.subscribe("*", update),
    pb.realtime.subscribe("PB_CONNECT", () => void refresh(current)),
  ]).then(() => {
    if (current === generation) return refresh(current);
  }).catch(() => {});
  await refresh(current);
}

function send(path, body) {
  return pb.send(`/api/portal/${path}`, { method: "POST", body });
}

async function copyText(value, label, copied = "copied") {
  try {
    await navigator.clipboard.writeText(value);
    feedback = copied;
    render();
  } catch {
    window.prompt(t(label), value);
  }
}

async function submitAction(action, fallback = "error") {
  if (busy) return;
  busy = true;
  feedback = "";
  render();
  try {
    await action();
  } catch (error) {
    feedback = errorMessage(error, fallback);
    if (error.status === 409) await refresh(generation);
  } finally {
    busy = false;
    render();
  }
}

language.addEventListener("change", () => {
  manual = language.value;
  localStorage.setItem("portal-language", manual);
  locale = chooseLanguage(manual, country, preferred);
  render();
});
logout.addEventListener("click", () => pb.authStore.clear());
document.querySelector("#home").addEventListener("click", (event) => {
  event.preventDefault();
  if (location.hash) location.hash = "";
});
window.addEventListener("hashchange", () => {
  feedback = "";
  render();
  window.scrollTo(0, 0);
});
document.addEventListener("visibilitychange", () => {
  if (account && document.visibilityState === "visible") void refresh(generation);
});

content.addEventListener("input", (event) => {
  const form = event.target.form;
  if (form?.id === "assign-form") {
    const data = new FormData(form);
    assignDraft = { assignee: data.get("assignee"), flow: data.get("flow"), email: data.get("prefill-email") };
    form.querySelector("#copy-inbox").disabled = !assignDraft.assignee;
  }
  if (form?.id === "report-form") {
    const issueField = form.querySelector("#issue-field");
    const outcome = form.elements.outcome.value;
    issueField.hidden = outcome !== "issue";
    form.elements.issue.disabled = issueField.hidden;
    form.elements.issue.required = !issueField.hidden;
    drafts.set(activeId(), {
      email: form.elements["report-email"].value,
      ip: form.elements.ip.value,
      region: form.elements.region.value,
      outcome,
      issue: form.elements.issue.value,
    });
  }
});

content.addEventListener("submit", (event) => {
  event.preventDefault();
  const form = event.target;
  if (!form.checkValidity()) {
    const invalid = form.querySelector(":invalid")?.id;
    feedback = "badInput";
    render();
    if (invalid) document.getElementById(invalid)?.focus();
    return;
  }
  const data = new FormData(form);
  if (form.id === "login-form") {
    void submitAction(
      () => pb.collection(users).authWithPassword(data.get("login-email").trim(), data.get("password")),
      "loginError",
    );
  }
  if (form.id === "assign-form") {
    void submitAction(async () => {
      const task = await send("tasks", {
        assignee: data.get("assignee"), flow: data.get("flow"), email: data.get("prefill-email"),
      });
      setTask(task);
      assignDraft = { assignee: "", flow: "team", email: "" };
      location.hash = `task=${task.id}`;
    });
  }
  if (form.id === "report-form") {
    const id = activeId();
    const task = tasks.get(id);
    void submitAction(async () => {
      setTask(await send(`tasks/${encodeURIComponent(id)}/report`, {
        updated: task.updated, email: data.get("report-email"), region: data.get("region"), ip: data.get("ip"),
        outcome: data.get("outcome"), issue: data.get("issue") ?? "",
      }));
      drafts.delete(id);
    });
  }
  if (form.id === "entry-form") {
    const task = tasks.get(activeId());
    void submitAction(async () => {
      setTask(await send(`tasks/${encodeURIComponent(task.id)}/renew`, { updated: task.updated, entry: data.get("entry") }));
    });
  }
});

content.addEventListener("click", async (event) => {
  if (event.target.id === "retry") void refresh(generation);
  if (event.target.id === "copy-link" || event.target.id === "copy-inbox") {
    const inbox = event.target.id === "copy-inbox";
    const path = inbox ? `/#inbox=${encodeURIComponent(assignDraft.assignee)}` : taskURL(activeId());
    const url = new URL(path, location.origin).href;
    await copyText(url, inbox ? "copyInbox" : "copy");
  }
  if (event.target.id === "copy-official") {
    const task = tasks.get(activeId());
    const [, url] = flows[task.flow].entries[entryKey(task)];
    await copyText(url, "copyOfficial");
  }
  if (event.target.id === "confirm") {
    const task = tasks.get(activeId());
    void submitAction(async () => {
      setTask(await send(`tasks/${encodeURIComponent(task.id)}/${event.target.id}`, { updated: task.updated }));
    });
  }
  if (event.target.id === "detect-ip") {
    const id = activeId();
    try {
      const result = await pb.send("/api/portal/network");
      const field = document.querySelector("#report-ip");
      if (activeId() !== id || !field) return;
      if (!result.ip) {
        feedback = "ipUnavailable";
        render();
        return;
      }
      field.value = result.ip;
      if (regions.includes(result.country)) document.querySelector("#region").value = result.country;
      field.dispatchEvent(new Event("input", { bubbles: true }));
      if (feedback === "ipUnavailable") {
        feedback = "";
        render();
      }
    } catch (error) {
      if (activeId() !== id) return;
      feedback = errorMessage(error);
      render();
    }
  }
  if (event.target.id === "copy-data") {
    const task = tasks.get(activeId());
    const data = [task.email, task.ip, regionName(task.region)].filter(Boolean).join("\n");
    await copyText(data, "copyData", "dataCopied");
  }
});

render();
void pb.send("/api/portal/context").then((result) => {
  country = result.country;
  locale = chooseLanguage(manual, country, preferred);
  render();
}).catch(() => {});

if (pb.authStore.isValid) {
  try {
    await pb.collection(users).authRefresh();
  } catch {
    pb.authStore.clear();
  }
} else {
  pb.authStore.clear();
}
pb.authStore.onChange((_, record) => {
  if (record?.id !== account?.id) void openWorkspace(record);
});
await openWorkspace(pb.authStore.record);
