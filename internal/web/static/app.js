// Coding Agent 网页（原生 JS，无构建步骤）。版式参照 Claude：纸面底色、衬线体的回答、卡片式输入框（docs/plan/coding-agent-mvp.md 第 7、8 节）。
// 工作区（基线仓库）下有很多会话；每个会话一段对话加一份自己的工作副本，可以同时跑。
// 左栏：上面是新建会话、搜索、工作区、进行中，下面按工作区分组列出会话。中间：会话（每个任务一轮：你的消息、工作过程、回答，
// 输入框随阶段变化）、工作区一览或进行中的任务。右栏是当前会话的环境、上下文、输出、改动与后台任务；diff、文件与提交历史在宽抽屉里打开。
// DOM 一律用 textContent 构造，不拼 HTML。界面文字一律经 i18n.js 的 t() 取（中文与英文）。

import { abortMarkdown, doneWhenText, errorText, exitWhenText, goalText, questionText, reasonText, retryDetail, retryTitle } from "./codes.js";
import { LANGS, lang, locale, setLang, t } from "./i18n.js";
import { installScrollbars } from "./scrollbars.js";

const EVENT_TYPES = [
  "task_accepted", "round_started", "stage_started", "node_started", "node_finished",
  "thinking_delta", "tool_status", "token_delta", "human_input_required", "verdict", "decision_retry", "task_finished",
  "guidance_updated", "task_message_edited", "context_usage", "plan_updated", "resnapshot_required",
];
const STATUS = {
  TASK_STATUS_QUEUED: [t("status.queued"), "run"],
  TASK_STATUS_RUNNING: [t("status.running"), "run"],
  TASK_STATUS_DONE: [t("status.done"), "ok"],
  TASK_STATUS_ABORTED: [t("status.aborted"), "err"],
  TASK_STATUS_CANCELLED: [t("status.cancelled"), "warn"],
  TASK_STATUS_FAILED: [t("status.failed"), "err"],
};
const TERMINAL = new Set(["TASK_STATUS_DONE", "TASK_STATUS_ABORTED", "TASK_STATUS_CANCELLED", "TASK_STATUS_FAILED"]);
const QUIET = new Set(["heartbeat", "token_delta", "thinking_delta", "guidance_updated", "task_message_edited", "context_usage", "plan_updated"]);
const REPLAY_LIMIT = 12;

// 沙箱等级就是 Task 的 effect 上限；模型倾向交给 Planner 选 Tier（服务端见 runsettings.go）。
const SANDBOX = [
  { id: "read", icon: "eye", label: t("sandbox.read"), desc: t("sandbox.readDesc") },
  { id: "write_reversible", icon: "edit", label: t("sandbox.write"), desc: t("sandbox.writeDesc") },
  { id: "full", icon: "globe", label: t("sandbox.full"), desc: t("sandbox.fullDesc"), disabled: true },
];
const MODELS = [
  { id: "economy", icon: "leaf", label: t("model.economy"), desc: t("model.economyDesc") },
  { id: "balanced", icon: "gauge", label: t("model.balanced"), desc: t("model.balancedDesc") },
  { id: "best", icon: "zap", label: t("model.best"), desc: t("model.bestDesc") },
];
const VERDICTS = Object.fromEntries(Object.entries({
  done: "ok", continue: "info", repair: "warn", replan: "warn", rollback: "warn", reroute: "warn", escalate: "warn", abort: "err",
}).map(([k, kind]) => [k, [t("verdict." + k), kind]]));
const SECTIONS = ["env", "context", "output", "changes", "background"];

const ls = {
  get(k) { try { return localStorage.getItem(k); } catch { return null; } },
  set(k, v) { try { localStorage.setItem(k, v); } catch { /* 无痕窗口等：只在本次有效 */ } },
};

function setting(key, allowed, fallback) {
  const v = ls.get("cw_" + key);
  return allowed.includes(v) ? v : fallback;
}

const state = {
  config: { dev_login: false, github_login: false, sandbox_control: false },
  me: null,
  personalConfig: null,
  personalChoices: new Map(),
  projects: [], // 工作区
  sessions: [], // 全部会话，最近活动的在前
  samples: [],
  environments: null, // 自己的环境配置（与仓库无关，开会话时选用）；null 是还没读到
  draftEnvironment: "", // 草稿选的环境配置；空是默认环境
  project: null, // 当前工作区
  session: null, // 当前会话；为空时是草稿（第一条消息发出时才开会话）
  view: "session", // session、workspaces、activity 或 environments
  groupsClosed: new Set((ls.get("cw_groups_closed") || "").split(",").filter(Boolean)),
  groupsAll: new Set(), // 展开了全部会话的工作区
  tasks: [], // 旧 → 新
  runs: new Map(), // taskId → { events, done, es, promise }
  detail: new Map(), // taskId → { state, usage, cost, roundSeq }
  artifacts: new Map(), // taskId → { list, failed }：任务交付的文件（N-18），结束后取一次
  open: new Set(), // 展开的工作过程
  closed: new Set(), // 用户手动收起的
  replied: new Set(),
  guidance: new Map(), // taskId → 这个任务的纠偏（发出的顺序）：{ id, text, mode, state, reason, editing, draft }
  queuedEdit: null, // 正在修改的排队消息：{ taskId, draft }
  ctxToolsOpen: false, // 右栏：展开了每个工具占多少
  envOlderOpen: false, // 右栏：展开了更早的沙箱环境记录
  detailsOpen: false, // 右栏：展开了任务详情
  rawOpen: new Set(), // 展开了“原始事件”的步骤（taskId:eventId）：重画时保持展开
  shownSteps: new Map(), // taskId → 已经画过的步骤（eventId）：只给新来的步骤进场动画
  shownNow: new Map(), // taskId → “此刻”那一行对应的事件：换了才给它进场动画
  pending: null, // 正在提交的任务：{ message, phase: "session" | "submit", started, shown }
  changes: null,
  repos: [],
  repoName: "",
  env: null,
  active: [],
  filesPath: "",
  sending: false,
  drawer: null,
  settings: {
    sandbox: setting("sandbox", ["read", "write_reversible"], "write_reversible"),
    model: setting("model", ["economy", "balanced", "best"], "balanced"),
  },
  statusOpen: ls.get("cw_status") ? ls.get("cw_status") === "1" : window.innerWidth > 1180,
  sideCollapsed: ls.get("cw_side") === "0",
  sideOpen: false,
  sections: new Set((ls.get("cw_sections") ?? SECTIONS.join(",")).split(",").filter((s) => SECTIONS.includes(s))),
};

const ui = {}; // 常驻的 DOM 节点（重画时只换内容）

// 当前会话的接口前缀；草稿（还没开出会话）时为 null。
function sbase() {
  return state.project && state.session ? `/api/projects/${state.project.id}/sessions/${state.session.id}` : null;
}

function inSession() {
  return state.view === "session" && !!state.session;
}

function sessionURL(projectId, sessionId) {
  return `/api/projects/${projectId}/sessions/${sessionId}`;
}

// ---------------------------------------------------------------- DOM

function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v === null || v === undefined) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v);
    else if (k === "value") el.value = v;
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  append(el, children);
  return el;
}

// 整块重画（replaceChildren）时保住滚动位置：先清空再放新内容，区域会一下子变空，滚动位置被夹回顶上，看起来就是“自己滚了一下”。
// 区域本身与里面滚过的子区域（按在树里的位置对上，标签与 class 都一样才算同一个）都还原。
function keepScroll(root, render) {
  const top = root.scrollTop;
  const left = root.scrollLeft;
  const inner = [];
  for (const el of root.querySelectorAll("*")) {
    if (!el.scrollTop && !el.scrollLeft) continue;
    const path = [];
    for (let n = el; n && n !== root; n = n.parentElement) path.unshift(Array.prototype.indexOf.call(n.parentElement.children, n));
    inner.push({ path, tag: el.tagName, cls: el.getAttribute("class"), top: el.scrollTop, left: el.scrollLeft });
  }
  render();
  root.scrollTop = top;
  root.scrollLeft = left;
  for (const s of inner) {
    let el = root;
    for (const i of s.path) el = el && el.children[i];
    if (el && el.tagName === s.tag && el.getAttribute("class") === s.cls) { el.scrollTop = s.top; el.scrollLeft = s.left; }
  }
}

function append(el, children) {
  for (const c of [children].flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

const ICONS = {
  plus: "M12 5v14M5 12h14",
  send: "M12 19V5M5 12l7-7 7 7",
  folder: "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z",
  branch: "M6 3v12M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM18 9a9 9 0 0 1-9 9",
  check: "M20 6 9 17l-5-5",
  x: "M18 6 6 18M6 6l12 12",
  alert: "M12 9v4M12 17h.01M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z",
  chevron: "M9 18l6-6-6-6",
  chevDown: "M6 9l6 6 6-6",
  panelRight: "M3 5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2zM15 3v18",
  panelLeft: "M3 5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2zM9 3v18",
  file: "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8zM14 2v6h6",
  fileText: "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8zM14 2v6h6M8 13h8M8 17h5",
  trash: "M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6",
  download: "M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3",
  commit: "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM3 12h6M15 12h6",
  external: "M15 3h6v6M10 14 21 3M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6",
  sparkles: "M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9zM19 15l.8 2.2L22 18l-2.2.8L19 21l-.8-2.2L16 18l2.2-.8z",
  layers: "M12 2 2 7l10 5 10-5zM2 17l10 5 10-5M2 12l10 5 10-5",
  play: "M6 4l13 8-13 8z",
  shield: "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z",
  terminal: "M4 17l6-6-6-6M12 19h8",
  refresh: "M3 12a9 9 0 0 1 15-6.7L21 8M21 3v5h-5M21 12a9 9 0 0 1-15 6.7L3 16M3 21v-5h5",
  logout: "M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9",
  user: "M20 21a8 8 0 0 0-16 0M12 13a5 5 0 1 0 0-10 5 5 0 0 0 0 10z",
  link: "M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7",
  menu: "M3 6h18M3 12h18M3 18h18",
  stop: "M7 7h10v10H7z",
  clock: "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 6v6l4 2",
  code: "M16 18l6-6-6-6M8 6l-6 6 6 6",
  box: "M21 8 12 3 3 8v8l9 5 9-5zM3 8l9 5 9-5M12 13v8",
  copy: "M9 9h11v11H9zM5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1",
  history: "M3 12a9 9 0 1 0 3-6.7L3 8M3 3v5h5M12 7v5l3 2",
  diff: "M12 3v14M5 10h14M5 21h14",
  eye: "M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z",
  edit: "M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z",
  globe: "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM2 12h20M12 2a15 15 0 0 1 0 20M12 2a15 15 0 0 0 0 20",
  gauge: "M12 14l4-4M3.3 17a9 9 0 1 1 17.4 0",
  leaf: "M11 20A7 7 0 0 1 4 13c0-6 7-9 16-9 0 9-3 16-9 16zM4 20l8-8",
  zap: "M13 2 3 14h9l-1 8 10-12h-9z",
  server: "M3 4h18v6H3zM3 14h18v6H3zM7 7h.01M7 17h.01",
  activity: "M22 12h-4l-3 9L9 3l-3 9H2",
  sun: "M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4",
  moon: "M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z",
  monitor: "M3 4h18v12H3zM8 20h8M12 16v4",
  more: "M5 12h.01M12 12h.01M19 12h.01",
  queue: "M3 6h13M3 12h13M3 18h8M18 15v6M15 18h6",
  list: "M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01",
  search: "M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16zM21 21l-4.3-4.3",
  compose: "M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7M18.5 2.5a2.1 2.1 0 0 1 3 3L12 15l-4 1 1-4z",
  grid: "M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z",
  chat: "M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z",
};
const GITHUB_PATH = "M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z";

function icon(name, cls = "") {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  const github = name === "github";
  svg.setAttribute("viewBox", github ? "0 0 16 16" : "0 0 24 24");
  svg.setAttribute("class", `icon ${github ? "filled" : ""} ${cls}`);
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS(ns, "path");
  path.setAttribute("d", github ? GITHUB_PATH : ICONS[name] || ICONS.box);
  svg.append(path);
  return svg;
}

// 标志：缺口朝右的环（Task Loop：规划、执行、复核）加一根光标，读作“C|”。Agent 工作时光标闪烁。
function logo(cls = "") {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("class", `icon logo bold ${cls}`);
  svg.setAttribute("aria-hidden", "true");
  for (const [d, part] of [["M16.6 6.9A8 8 0 1 0 16.6 17.1", "ring"], ["M20 9v6", "cursor"]]) {
    const path = document.createElementNS(ns, "path");
    path.setAttribute("d", d);
    path.setAttribute("class", part);
    svg.append(path);
  }
  return svg;
}

function mount(node) {
  const app = document.getElementById("app");
  app.className = "";
  app.replaceChildren(node);
}

function toast(text, isError = false) {
  const el = h("div", { class: "toast" + (isError ? " err" : "") }, text);
  document.body.append(el);
  setTimeout(() => el.remove(), isError ? 6000 : 2600);
}

function copy(text) {
  navigator.clipboard.writeText(text).then(() => toast(t("common.copied")), () => toast(t("common.copyFailed"), true));
}

function fmtTime(iso) {
  const d = new Date(iso);
  return isNaN(d) ? "" : d.toLocaleString(locale, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function fmtClock(iso) {
  const d = new Date(iso);
  return isNaN(d) ? "" : d.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function fmtDuration(ms) {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return t("dur.s", { s });
  const m = Math.floor(s / 60);
  return s % 60 ? t("dur.ms", { m, s: s % 60 }) : t("dur.m", { m });
}

// fmtMs 是一次工具调用、沙箱启动这类短时间：不到一分钟时带一位小数（0.1 秒、8.1 秒）。
function fmtMs(ms) {
  if (ms >= 60000) return fmtDuration(ms);
  return t("dur.s", { s: (Math.max(0, ms) / 1000).toFixed(1).replace(/\.0$/, "") });
}

function fmtTimer(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

function fmtAgo(iso) {
  const ms = Date.now() - new Date(iso);
  if (isNaN(ms)) return "";
  if (ms < 60e3) return t("ago.now");
  if (ms < 3600e3) return t("ago.min", { n: Math.floor(ms / 60e3) });
  if (ms < 86400e3) return t("ago.hour", { n: Math.floor(ms / 3600e3) });
  return fmtTime(iso);
}

function fmtSize(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

function fmtUSD(x) {
  if (!x) return "$0";
  return x < 0.01 ? "<$0.01" : `$${x.toFixed(2)}`;
}

function fmtTokens(n) {
  if (n < 1000) return String(n);
  if (n < 1e6) return `${(n / 1000).toFixed(n < 1e4 ? 1 : 0)}k`;
  return `${(n / 1e6).toFixed(1)}M`;
}

function num(v) {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}

function statusChip(status) {
  const [label, kind] = STATUS[status] || [status || t("status.unknown"), ""];
  return h("span", { class: `chip ${kind}` }, kind === "run" ? h("span", { class: "spinner" }) : null, label);
}

function verdictChip(verdict) {
  const key = String(verdict || "").replace(/^VERDICT_/, "").toLowerCase();
  const [label, kind] = VERDICTS[key] || [key || "—", ""];
  return h("span", { class: `chip ${kind}`, title: key }, label);
}

function initials(name) {
  return (name || "?").slice(0, 2).toUpperCase();
}

function pick(obj, ...keys) {
  for (const k of keys) if (obj && obj[k] !== undefined && obj[k] !== null && obj[k] !== "") return obj[k];
  return undefined;
}

function isTyping(el) {
  return el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable);
}

// ---------------------------------------------------------------- Markdown（只造 DOM）

function md(text) {
  const frag = document.createDocumentFragment();
  const lines = String(text || "").replace(/\r\n/g, "\n").split("\n");
  let i = 0;
  const para = [];
  const flush = () => {
    if (!para.length) return;
    const p = h("p");
    para.forEach((l, k) => { if (k) p.append(h("br")); inline(p, l); });
    frag.append(p);
    para.length = 0;
  };
  const isRow = (l) => /^\s*\|.*\|\s*$/.test(l);
  const isRule = (l) => /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(l);
  while (i < lines.length) {
    const line = lines[i];
    const fence = line.match(/^\s*```\s*([\w+#.-]*)\s*$/);
    if (fence) {
      flush();
      const body = [];
      i++;
      while (i < lines.length && !/^\s*```\s*$/.test(lines[i])) body.push(lines[i++]);
      i++;
      frag.append(codeBlock(body.join("\n"), fence[1]));
      continue;
    }
    if (isRow(line) && i + 1 < lines.length && isRule(lines[i + 1])) {
      flush();
      const head = cells(line);
      i += 2;
      const rows = [];
      while (i < lines.length && isRow(lines[i])) rows.push(cells(lines[i++]));
      frag.append(h("div", { class: "md-table" }, h("table", {},
        h("thead", {}, h("tr", {}, head.map((c) => inline(h("th"), c)))),
        h("tbody", {}, rows.map((r) => h("tr", {}, head.map((_, k) => inline(h("td"), r[k] || ""))))),
      )));
      continue;
    }
    if (/^\s*(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      flush();
      frag.append(h("hr"));
      i++;
      continue;
    }
    const heading = line.match(/^(#{1,4})\s+(.*)$/);
    if (heading) {
      flush();
      frag.append(inline(h(`h${heading[1].length}`), heading[2]));
      i++;
      continue;
    }
    if (/^\s*([-*•]|\d+[.)])\s+/.test(line)) {
      flush();
      const first = line.match(/^\s*(\d+)[.)]\s+/);
      const list = h(first ? "ol" : "ul", first && first[1] !== "1" ? { start: first[1] } : {});
      while (i < lines.length && /^\s*([-*•]|\d+[.)])\s+/.test(lines[i])) {
        list.append(inline(h("li"), lines[i].replace(/^\s*([-*•]|\d+[.)])\s+/, "")));
        i++;
      }
      frag.append(list);
      continue;
    }
    if (/^>\s?/.test(line)) {
      flush();
      const quote = h("blockquote");
      while (i < lines.length && /^>\s?/.test(lines[i])) {
        if (quote.childNodes.length) quote.append(h("br"));
        inline(quote, lines[i].replace(/^>\s?/, ""));
        i++;
      }
      frag.append(quote);
      continue;
    }
    if (!line.trim()) flush();
    else para.push(line);
    i++;
  }
  flush();
  return frag;
}

function cells(line) {
  return line.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => c.trim());
}

function codeBlock(code, lang) {
  return h("div", { class: "codeblock" },
    h("div", { class: "cb-head" }, h("span", { class: "grow" }, lang || t("code.lang")),
      h("button", { class: "btn ghost xs", onclick: () => copy(code) }, icon("copy", "xs"), t("common.copy"))),
    h("pre", {}, h("code", {}, code)),
  );
}

function inline(el, text) {
  const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\[[^\]]+\]\((https?:\/\/[^)\s]+)\))/g;
  let last = 0;
  let m;
  while ((m = re.exec(text))) {
    if (m.index > last) el.append(text.slice(last, m.index));
    if (m[1]) el.append(h("code", {}, m[1].slice(1, -1)));
    else if (m[2]) el.append(h("strong", {}, m[2].slice(2, -2)));
    else if (m[3]) el.append(h("a", { href: m[4], target: "_blank", rel: "noopener noreferrer" }, m[3].slice(1, m[3].indexOf("]"))));
    last = re.lastIndex;
  }
  if (last < text.length) el.append(text.slice(last));
  return el;
}

// ---------------------------------------------------------------- API

async function api(method, path, body) {
  const opts = { method, headers: { "X-Coding-Web": "1" }, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const workspace = workspaceMutation(method, path);
  if (workspace) {
    opts.headers["X-Coding-Async"] = "1";
    opts.headers["Idempotency-Key"] = crypto.randomUUID();
  }
  let res;
  for (let attempt = 0; ; attempt++) {
    try {
      res = await fetch(path, workspace ? { ...opts, signal: AbortSignal.timeout(20000) } : opts);
      if (!workspace || ![502, 503, 504, 524].includes(res.status) || attempt >= 2) break;
    } catch (ex) { if (!workspace || attempt >= 2) throw ex; }
    await new Promise(resolve => setTimeout(resolve, 500 * (attempt + 1)));
  }
  if (res.status === 401 && path !== "/api/me") {
    state.me = null;
    renderLogin(t("api.expired"));
    throw new Error("unauthorized");
  }
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (res.status === 202 && data && data.status_url) return pollWorkspaceOperation(data.status_url);
  if (!res.ok) { const ex = new Error((data && data.error) || t("api.failed", { status: res.status })); ex.code = data && data.code; throw ex; }
  return data;
}

// Polling survives connection loss; reload discovers pending server journals.
function workspaceMutation(method, path) {
  if (!["POST", "DELETE"].includes(method)) return false;
  path = path.split("?")[0];
  return /^\/api\/projects(?:\/[^/]+)?$/.test(path) ||
    /^\/api\/projects\/[^/]+\/(?:repos(?:\/[^/]+\/detach)?|sessions(?:\/[^/]+(?:\/(?:duplicate|repos(?:\/[^/]+\/detach)?|commit|discard|publish|reset-branch|sync))?)?)$/.test(path);
}

const workspacePolls = new Map();
function workspaceSeen(path, remember = false) {
  const key = "coding-workspace-results";
  try {
    const seen = JSON.parse(sessionStorage.getItem(key) || "[]");
    if (remember && !seen.includes(path)) sessionStorage.setItem(key, JSON.stringify([...seen.slice(-99), path]));
    return seen.includes(path);
  } catch { return false; }
}
function pollWorkspaceOperation(path) {
  if (workspacePolls.has(path)) return workspacePolls.get(path);
  const task = (async () => {
    for (;;) {
      let res, data;
      try {
        res = await fetch(path, { credentials: "same-origin", headers: { "X-Coding-Web": "1" }, signal: AbortSignal.timeout(20000) });
        data = await res.json();
      } catch { await new Promise(resolve => setTimeout(resolve, 1000)); continue; }
      if (res.status === 401) {
        state.me = null; renderLogin(t("api.expired")); throw new Error("unauthorized");
      }
      if (res.status >= 500) { await new Promise(resolve => setTimeout(resolve, 1000)); continue; }
      if (!res.ok) throw new Error(data.error || t("api.failed", { status: res.status }));
      if (data.status === "COMPLETED") { workspaceSeen(path, true); return data.result; }
      if (data.status === "FAILED") {
        workspaceSeen(path, true);
        const ex = new Error(data.result?.error || t("api.failed", { status: data.http_status }));
        ex.code = data.result?.code; throw ex;
      }
      if (data.status !== "RUNNING") throw new Error(t("api.failed", { status: data.status }));
      await new Promise(resolve => setTimeout(resolve, 500));
    }
  })().finally(() => workspacePolls.delete(path));
  workspacePolls.set(path, task);
  return task;
}

async function resumeWorkspaceOperations() {
  const pending = await api("GET", "/api/workspace-operations");
  for (const operation of pending || []) {
    if (workspacePolls.has(operation.status_url) || workspaceSeen(operation.status_url)) continue;
    pollWorkspaceOperation(operation.status_url).then(async () => {
      [state.projects, state.sessions] = await Promise.all([api("GET", "/api/projects"), api("GET", "/api/sessions")]);
      toast(t("workspace.operationRecovered"));
      await route();
    }).catch(report);
  }
}

function report(ex) {
  if (ex && ex.message !== "unauthorized") toast(ex.message, true);
}

// ---------------------------------------------------------------- 登录方式与 Git 平台

function providerList() {
  return state.config.providers || [];
}

function providerInfo(id) {
  return providerList().find((p) => p.id === id) || { id, name: id || "Git", url: "", git: true };
}

function connection(id) {
  return ((state.me && state.me.connections) || []).find((c) => c.provider === id) || null;
}

function gitConnections() {
  return ((state.me && state.me.connections) || []).filter((c) => c.git);
}

// 各家的标志：GitHub、Gitee、GitLab、Gitea、Codeberg、Forgejo、Bitbucket 取自 Simple Icons（CC0，simple-icons@13），
// 用品牌色；Google 用它规定的四色 G。用在登录按钮、“账号与平台”、仓库与工作区旁边。
const BRANDS = {
  gitee: { color: "#C71D23", d: "M11.984 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12 12 12 0 0 0 12-12A12 12 0 0 0 12 0a12 12 0 0 0-.016 0zm6.09 5.333c.328 0 .593.266.592.593v1.482a.594.594 0 0 1-.593.592H9.777c-.982 0-1.778.796-1.778 1.778v5.63c0 .327.266.592.593.592h5.63c.982 0 1.778-.796 1.778-1.778v-.296a.593.593 0 0 0-.592-.593h-4.15a.592.592 0 0 1-.592-.592v-1.482a.593.593 0 0 1 .593-.592h6.815c.327 0 .593.265.593.592v3.408a4 4 0 0 1-4 4H5.926a.593.593 0 0 1-.593-.593V9.778a4.444 4.444 0 0 1 4.445-4.444h8.296Z" },
  gitlab: { color: "#FC6D26", d: "m23.6004 9.5927-.0337-.0862L20.3.9814a.851.851 0 0 0-.3362-.405.8748.8748 0 0 0-.9997.0539.8748.8748 0 0 0-.29.4399l-2.2055 6.748H7.5375l-2.2057-6.748a.8573.8573 0 0 0-.29-.4412.8748.8748 0 0 0-.9997-.0537.8585.8585 0 0 0-.3362.4049L.4332 9.5015l-.0325.0862a6.0657 6.0657 0 0 0 2.0119 7.0105l.0113.0087.03.0213 4.976 3.7264 2.462 1.8633 1.4995 1.1321a1.0085 1.0085 0 0 0 1.2197 0l1.4995-1.1321 2.4619-1.8633 5.006-3.7489.0125-.01a6.0682 6.0682 0 0 0 2.0094-7.003z" },
  gitea: { color: "#609926", d: "M4.209 4.603c-.247 0-.525.02-.84.088-.333.07-1.28.283-2.054 1.027C-.403 7.25.035 9.685.089 10.052c.065.446.263 1.687 1.21 2.768 1.749 2.141 5.513 2.092 5.513 2.092s.462 1.103 1.168 2.119c.955 1.263 1.936 2.248 2.89 2.367 2.406 0 7.212-.004 7.212-.004s.458.004 1.08-.394c.535-.324 1.013-.893 1.013-.893s.492-.527 1.18-1.73c.21-.37.385-.729.538-1.068 0 0 2.107-4.471 2.107-8.823-.042-1.318-.367-1.55-.443-1.627-.156-.156-.366-.153-.366-.153s-4.475.252-6.792.306c-.508.011-1.012.023-1.512.027v4.474l-.634-.301c0-1.39-.004-4.17-.004-4.17-1.107.016-3.405-.084-3.405-.084s-5.399-.27-5.987-.324c-.187-.011-.401-.032-.648-.032zm.354 1.832h.111s.271 2.269.6 3.597C5.549 11.147 6.22 13 6.22 13s-.996-.119-1.641-.348c-.99-.324-1.409-.714-1.409-.714s-.73-.511-1.096-1.52C1.444 8.73 2.021 7.7 2.021 7.7s.32-.859 1.47-1.145c.395-.106.863-.12 1.072-.12zm8.33 2.554c.26.003.509.127.509.127l.868.422-.529 1.075a.686.686 0 0 0-.614.359.685.685 0 0 0 .072.756l-.939 1.924a.69.69 0 0 0-.66.527.687.687 0 0 0 .347.763.686.686 0 0 0 .867-.206.688.688 0 0 0-.069-.882l.916-1.874a.667.667 0 0 0 .237-.02.657.657 0 0 0 .271-.137 8.826 8.826 0 0 1 1.016.512.761.761 0 0 1 .286.282c.073.21-.073.569-.073.569-.087.29-.702 1.55-.702 1.55a.692.692 0 0 0-.676.477.681.681 0 1 0 1.157-.252c.073-.141.141-.282.214-.431.19-.397.515-1.16.515-1.16.035-.066.218-.394.103-.814-.095-.435-.48-.638-.48-.638-.467-.301-1.116-.58-1.116-.58s0-.156-.042-.27a.688.688 0 0 0-.148-.241l.516-1.062 2.89 1.401s.48.218.583.619c.073.282-.019.534-.069.657-.24.587-2.1 4.317-2.1 4.317s-.232.554-.748.588a1.065 1.065 0 0 1-.393-.045l-.202-.08-4.31-2.1s-.417-.218-.49-.596c-.083-.31.104-.691.104-.691l2.073-4.272s.183-.37.466-.497a.855.855 0 0 1 .35-.077z" },
  codeberg: { color: "#2185D0", d: "M11.955.49A12 12 0 0 0 0 12.49a12 12 0 0 0 1.832 6.373L11.838 5.928a.187.14 0 0 1 .324 0l10.006 12.935A12 12 0 0 0 24 12.49a12 12 0 0 0-12-12 12 12 0 0 0-.045 0zm.375 6.467l4.416 16.553a12 12 0 0 0 5.137-4.213z" },
  forgejo: { color: "#FB923C", d: "M16.7773 0c1.6018 0 2.9004 1.2986 2.9004 2.9005s-1.2986 2.9004-2.9004 2.9004c-1.0854 0-2.0315-.596-2.5288-1.4787H12.91c-2.3322 0-4.2272 1.8718-4.2649 4.195l-.0007 2.1175a7.0759 7.0759 0 0 1 4.148-1.4205l.1176-.001 1.3385.0002c.4973-.8827 1.4434-1.4788 2.5288-1.4788 1.6018 0 2.9004 1.2986 2.9004 2.9005s-1.2986 2.9004-2.9004 2.9004c-1.0854 0-2.0315-.596-2.5288-1.4787H12.91c-2.3322 0-4.2272 1.8718-4.2649 4.195l-.0007 2.319c.8827.4973 1.4788 1.4434 1.4788 2.5287 0 1.602-1.2986 2.9005-2.9005 2.9005-1.6018 0-2.9004-1.2986-2.9004-2.9005 0-1.0853.596-2.0314 1.4788-2.5287l-.0002-9.9831c0-3.887 3.1195-7.0453 6.9915-7.108l.1176-.001h1.3385C14.7458.5962 15.692 0 16.7773 0ZM7.2227 19.9052c-.6596 0-1.1943.5347-1.1943 1.1943s.5347 1.1943 1.1943 1.1943 1.1944-.5347 1.1944-1.1943-.5348-1.1943-1.1944-1.1943Zm9.5546-10.4644c-.6596 0-1.1944.5347-1.1944 1.1943s.5348 1.1943 1.1944 1.1943c.6596 0 1.1943-.5347 1.1943-1.1943s-.5347-1.1943-1.1943-1.1943Zm0-7.7346c-.6596 0-1.1944.5347-1.1944 1.1943s.5348 1.1943 1.1944 1.1943c.6596 0 1.1943-.5347 1.1943-1.1943s-.5347-1.1943-1.1943-1.1943Z" },
  bitbucket: { color: "#2684FF", d: "M.778 1.213a.768.768 0 00-.768.892l3.263 19.81c.084.5.515.868 1.022.873H19.95a.772.772 0 00.77-.646l3.27-20.03a.768.768 0 00-.768-.891zM14.52 15.53H9.522L8.17 8.466h7.561z" },
};
const GOOGLE_G = [
  ["#EA4335", "M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"],
  ["#4285F4", "M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"],
  ["#FBBC05", "M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"],
  ["#34A853", "M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"],
];

function brandSVG(viewBox, paths, size, title) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", viewBox);
  svg.setAttribute("class", `icon brand ${size}`);
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", title);
  for (const [fill, d] of paths) {
    const path = document.createElementNS(ns, "path");
    path.setAttribute("fill", fill);
    path.setAttribute("d", d);
    svg.append(path);
  }
  return svg;
}

function badge(id, size = "sm") {
  if (id === "github") return icon("github", size);
  const info = providerInfo(id);
  if (id === "google") return brandSVG("0 0 48 48", GOOGLE_G, size, info.name);
  // Gitea 这一家指向 Codeberg 或 Forgejo 时用它们自己的标志。
  const key = id === "gitea" && /codeberg/i.test(info.name) ? "codeberg" : id === "gitea" && /forgejo/i.test(info.name) ? "forgejo" : id;
  const b = BRANDS[key];
  if (b) return brandSVG("0 0 24 24", [[b.color, b.d]], size, info.name);
  const el = h("span", { class: `pv-badge ${size}`, title: info.name }, String(id || "?").slice(0, 2));
  el.style.background = "#76736b";
  return el;
}

// 工作区的标记：Git 平台上的仓库用平台标记，示例用盒子，其余用文件夹。
function projectMark(p, size = "sm") {
  if (p.git_provider) return badge(p.git_provider, size);
  return icon(p.source && p.source.startsWith("sample:") ? "box" : "folder", size);
}

function repoURL(p) {
  const base = (providerInfo(p.git_provider).url || "").replace(/\/$/, "");
  return base && p.repo ? `${base}/${p.repo}` : "";
}

function requestName(p) {
  return providerInfo(p.git_provider).request_name || "PR";
}

// 地址里 #…?linked=gitlab 或 ?error=… 是 OAuth 回调带回来的结果。
function hashParams() {
  return new URLSearchParams(location.hash.split("?")[1] || "");
}

// openAccount 是“账号与平台”：已连接的登录方式、连接别家、断开。
async function openAccount() {
  closePop();
  if (document.querySelector("dialog.account")) return;
  const body = h("div", { class: "d-body" }, h("div", { class: "row muted" }, h("span", { class: "spinner" }), t("common.loading")));
  const dialog = h("dialog", { class: "account" },
    h("div", { class: "d-head" }, h("h2", {}, t("account.title")), h("div", { class: "muted small" }, t("account.who", { tenant: state.me.tenant, user: state.me.user }))),
    body,
    h("div", { class: "d-foot" }, h("button", { class: "btn ghost", onclick: () => dialog.close() }, t("common.close"))),
  );
  // 配了 GitHub App 时连接列表下面另有一段（G-05）：只取一次，断开重画时原样留着。
  const github = state.config.github_app ? h("div", { class: "github-app-slot" }) : null;
  // 服务端打开了网络密钥（WEB-SEC）时的入口；D-23 第二阶段之前默认关着。
  const secrets = state.config.network_secrets
    ? h("div", { class: "secrets-entry" }, h("button", { class: "btn sm secrets-open", onclick: () => openSecrets() }, t("secrets.open")))
    : null;
  // 我的 Skill（WEB-SKILLS）：总有入口；部署没有 Skill 时对话框里写明。
  const skills = h("div", { class: "skills-entry" }, h("button", { class: "btn sm skills-open", onclick: () => openSkills() }, t("skills.open")));
  const personal = h("div", {}, h("button", { class: "btn sm personal-open", onclick: () => openPersonalConfig() }, t("personal.title")));
  // 我的 MCP（Runtime 的 N-15）：总有入口；部署没有或账号的策略不允许时对话框里写明。
  const mcp = h("div", { class: "mcp-entry" }, h("button", { class: "btn sm mcp-open", onclick: () => openMcp() }, t("mcp.open")));
  const draw = (acct) => {
    const conns = acct.connections || [];
    const rows = providerList().filter((p) => p.configured || state.config.dev_login || conns.some((c) => c.provider === p.id)).map((p) => {
      const c = conns.find((x) => x.provider === p.id);
      const who = c && c.name && c.name !== c.login ? t("account.nameParen", { login: c.login, name: c.name }) : c && c.login;
      const status = c ? t("account.connected", { who }) : p.configured ? t("account.notConnected") : t("account.notConfigured");
      return h("div", { class: "conn-row" + (c ? " on" : "") },
        badge(p.id, "md"),
        h("div", { class: "grow" },
          h("div", { class: "t" }, p.name, p.git ? null : h("span", { class: "chip" }, t("account.loginOnly"))),
          h("div", { class: "m" }, status)),
        c ? h("button", { class: "btn sm", onclick: () => disconnect(p, draw) }, t("account.disconnect"))
          : p.configured ? h("a", { class: "btn sm primary", href: `/api/auth/${p.id}/login?link=1` }, t("account.connect")) : null);
    });
    body.replaceChildren(...[h("div", { class: "conn-list" }, rows), github, secrets, skills, mcp, personal].filter(Boolean));
  };
  dialog.addEventListener("close", () => dialog.remove());
  document.body.append(dialog);
  dialog.showModal();
  try { draw(await api("GET", "/api/account")); } catch (ex) { report(ex); }
  if (github) drawGitHubApp(github);
}

function personalChoiceKey() {
  return `${state.me?.tenant}/${state.me?.user}/${state.project?.id}/${state.session?.id || "new"}`;
}

function personalSelection() {
  return state.personalChoices.get(personalChoiceKey()) || state.session?.personal_config || undefined;
}

function personalTool() {
  return h("button", { class: "tool personal-tools", onclick: () => openPersonalConfig(true), title: t("personal.tools") }, icon("box", "sm"), t("personal.tools"));
}

async function openPersonalConfig(sessionOnly = false) {
  if (document.querySelector("dialog.personal-config")) return;
  const choiceKey = personalChoiceKey();
  const body = h("div", { class: "d-body" }, t("common.loading"));
  const dialog = h("dialog", { class: "personal-config" },
    h("div", { class: "d-head" }, h("h2", {}, t(sessionOnly ? "personal.tools" : "personal.title"))), body,
    h("div", { class: "d-foot" }, h("button", { class: "btn ghost", onclick: () => dialog.close() }, t("common.close"))));
  dialog.addEventListener("close", () => dialog.remove());
  document.body.append(dialog); dialog.showModal();
  try {
    const [response, skills, mcp, environments] = await Promise.all([
      api("GET", "/api/personal-config"), api("GET", "/api/skills"), api("GET", "/api/mcp"), api("GET", "/api/environments"),
    ]);
    if (response.available === false) { body.replaceChildren(h("p", {}, t("personal.unavailable"))); return; }
    let config = response.config || {};
    const doc = config.document || {}, defaults = doc.defaults || {};
    const chosen = sessionOnly ? personalSelection() : null;
    const instructions = h("textarea", { class: "personal-instructions", rows: 5, value: doc.instructions || "", "aria-label": t("personal.instructions") });
    const fileList = h("div", { class: "personal-files" });
    const fileRows = [];
    const addFile = (file = {}) => {
      if (fileRows.length >= 10) return;
      const path = h("input", { class: "personal-path", value: file.path || "", placeholder: ".config/ruff.toml", "aria-label": t("personal.path") });
      const content = h("textarea", { class: "personal-content", value: file.content || "", rows: 3, "aria-label": t("personal.content") });
      const item = { path, content };
      const row = h("div", { class: "personal-file" }, path, content, h("button", { class: "btn sm", onclick: () => { fileRows.splice(fileRows.indexOf(item), 1); row.remove(); } }, t("personal.remove")));
      fileRows.push(item); fileList.append(row);
    };
    (doc.files || []).forEach(addFile);
    const choices = (items, selected, key, label) => {
      const present = new Set(items.map((x) => x.id));
      for (const id of selected || []) if (!present.has(id)) items.push({ id, name: `${id} · ${t("personal.missing")}` });
      const checked = new Set(selected || items.map((x) => x.id));
      const inputs = items.map((item) => ({ id: item.id, name: item.name, input: h("input", { type: "checkbox", checked: checked.has(item.id), "data-reference": item.id }) }));
      return { el: h("fieldset", { class: key }, h("legend", {}, label), inputs.length ? inputs.map((x) => h("label", { class: "row" }, x.input, x.name)) : h("p", { class: "muted" }, t("personal.empty"))), values: () => inputs.filter((x) => x.input.checked).map((x) => x.id) };
    };
    const skillDefault = defaults.skillNames?.length ? defaults.skillNames : undefined;
    const mcpDefault = defaults.mcpConnectionIds?.length ? defaults.mcpConnectionIds : undefined;
    const skillChoices = choices((skills.skills || []).filter((x) => !x.shadowed).map((x) => ({ id: x.name, name: `${x.name} · ${x.description || ""}` })), chosen?.skill_names ?? (sessionOnly ? skillDefault : defaults.skillNames || []), "personal-skills", t("skills.open"));
    const mcpChoices = choices((mcp.connections || []).filter((x) => x.tools?.some((tool) => tool.enabled)).map((x) => ({ id: x.connection_id, name: x.name })), chosen?.mcp_connection_ids ?? (sessionOnly ? mcpDefault : defaults.mcpConnectionIds || []), "personal-mcp", t("mcp.open"));
    const environment = h("select", { class: "personal-environment", "aria-label": t("personal.environment") }, h("option", { value: "" }, t("personal.noEnvironment")), ...environments.map((x) => h("option", { value: x.id }, x.name)));
    if (defaults.environmentId && !environments.some((x) => x.id === defaults.environmentId)) environment.append(h("option", { value: defaults.environmentId }, `${defaults.environmentId} · ${t("personal.missing")}`));
    environment.value = defaults.environmentId || "";
    const save = h("button", { class: "btn primary personal-save", onclick: async () => {
      save.disabled = true;
      try {
        if (sessionOnly) {
          state.personalChoices.set(choiceKey, { skill_names: skillChoices.values(), mcp_connection_ids: mcpChoices.values() });
          dialog.close(); updateComposer(true); return;
        }
        const document = { instructions: instructions.value, files: fileRows.map((x) => ({ path: x.path.value, content: x.content.value })), defaults: { skillNames: skillChoices.values(), mcpConnectionIds: mcpChoices.values(), environmentId: environment.value } };
        const updated = await api("PUT", "/api/personal-config", { document, expected_version: config.version || "" });
        config = updated.config; state.personalConfig = config;
        if (!state.session) setDraftEnvironment(environment.value);
        toast(t("personal.saved")); dialog.close();
      } catch (ex) { report(ex); } finally { save.disabled = false; }
    } }, t(sessionOnly ? "personal.use" : "skills.save"));
    body.replaceChildren(h("p", { class: "muted" }, t(sessionOnly ? "personal.sessionNote" : "personal.note")),
      ...(!sessionOnly ? [h("label", { class: "lbl" }, t("personal.instructions"), instructions), fileList,
        h("button", { class: "btn sm personal-add", onclick: () => addFile() }, t("personal.add")),
        h("p", { class: "muted" }, t("personal.defaultNote"))] : []),
      skillChoices.el, mcpChoices.el,
      ...(!sessionOnly ? [h("label", { class: "lbl" }, t("personal.environment"), environment),
        h("p", { class: "muted small personal-version" }, config.version ? `${config.version} · ${config.digest}` : "")] : []), save,
      ...(!sessionOnly && config.version ? [h("button", { class: "btn ghost personal-retire", onclick: async () => {
        if (!confirm(t("personal.retireConfirm"))) return;
        try { await api("DELETE", "/api/personal-config"); state.personalConfig = null; dialog.close(); } catch (ex) { report(ex); }
      } }, t("personal.retire"))] : []));
  } catch (ex) { body.replaceChildren(h("p", { class: "error" }, ex.message)); }
}

// openSecrets 是“网络密钥”（WEB-SEC，/api/secrets；值存在 Runtime 的 SecretStore 里）：只列名字与时间，值只进不出——
// 编辑不回显旧值，保存之后清空输入框，关掉对话框时也清空。环境现在还不能引用它们（D-23 第二阶段），页上写明。
async function openSecrets() {
  if (document.querySelector("dialog.secrets")) return;
  const list = h("div", { class: "secret-list" }, h("div", { class: "row muted" }, h("span", { class: "spinner" }), t("common.loading")));
  const name = h("input", { class: "secret-name", placeholder: t("secrets.name"), maxlength: 128, autocomplete: "off", spellcheck: "false" });
  const value = h("input", { class: "secret-value", type: "password", placeholder: t("secrets.value"), autocomplete: "new-password", spellcheck: "false" });
  const save = h("button", { class: "btn sm primary secret-save", onclick: () => saveSecret() }, t("secrets.save"));
  const dialog = h("dialog", { class: "secrets" },
    h("div", { class: "d-head" }, h("h2", {}, t("secrets.title"))),
    h("div", { class: "d-body" },
      h("div", { class: "secrets-note" }, t("secrets.note")),
      list,
      h("div", { class: "secret-form" }, name, value, save)),
    h("div", { class: "d-foot" }, h("button", { class: "btn ghost", onclick: () => dialog.close() }, t("common.close"))),
  );
  const draw = (items) => {
    list.replaceChildren(items.length
      ? h("div", { class: "conn-list" }, items.map((s) => h("div", { class: "conn-row secret-row", "data-name": s.name },
        h("div", { class: "grow" }, h("div", { class: "t" }, s.name), h("div", { class: "m" }, t("secrets.updated", { time: fmtTime(s.updated_at || s.created_at) }))),
        h("button", { class: "btn sm secret-delete", onclick: () => removeSecret(s.name) }, t("secrets.delete")))))
      : h("div", { class: "muted" }, t("secrets.empty")));
  };
  const reload = async () => {
    try { draw((await api("GET", "/api/secrets")).secrets || []); } catch (ex) { report(ex); }
  };
  async function saveSecret() {
    const n = name.value.trim();
    if (!n || !value.value) return;
    save.disabled = true;
    try {
      await api("PUT", `/api/secrets/${encodeURIComponent(n)}`, { value: value.value });
      value.value = "";
      name.value = "";
      toast(t("secrets.saved", { name: n }));
      await reload();
    } catch (ex) { report(ex); } finally { save.disabled = false; }
  }
  async function removeSecret(n) {
    if (!confirm(t("secrets.deleteConfirm", { name: n }))) return;
    try {
      await api("DELETE", `/api/secrets/${encodeURIComponent(n)}`);
      toast(t("secrets.deleted", { name: n }));
      await reload();
    } catch (ex) { report(ex); }
  }
  dialog.addEventListener("close", () => { value.value = ""; dialog.remove(); });
  document.body.append(dialog);
  dialog.showModal();
  await reload();
}

// openSkills 是“我的 Skill”（WEB-SKILLS，/api/skills；Runtime 的 N-14）：列出看得到的 Skill（平台、团队的只读，我的可以编辑与收回，
// 被遮住的写明不会被用到），新建与编辑我的（名字、说明、做法；编辑时名字不能改，原有的文件照旧带上）。
async function openSkills() {
  if (document.querySelector("dialog.skills")) return;
  const list = h("div", { class: "skill-list" }, h("div", { class: "row muted" }, h("span", { class: "spinner" }), t("common.loading")));
  const name = h("input", { class: "skill-name", placeholder: t("skills.name"), maxlength: 64, autocomplete: "off", spellcheck: "false" });
  const description = h("input", { class: "skill-description", placeholder: t("skills.description"), maxlength: 1024 });
  const instructions = h("textarea", { class: "skill-instructions", placeholder: t("skills.instructions"), rows: 8, spellcheck: "false" });
  const filesNote = h("div", { class: "muted small skill-files" });
  const save = h("button", { class: "btn sm primary skill-save", onclick: () => saveSkill() }, t("skills.save"));
  const cancel = h("button", { class: "btn sm ghost skill-cancel", onclick: () => resetForm() }, t("skills.cancel"));
  const form = h("div", { class: "skill-form" }, name, description, instructions, filesNote, h("div", { class: "row" }, save, cancel));
  const dialog = h("dialog", { class: "skills" },
    h("div", { class: "d-head" }, h("h2", {}, t("skills.title"))),
    h("div", { class: "d-body" }, h("div", { class: "skills-note" }, t("skills.note")), list, form),
    h("div", { class: "d-foot" }, h("button", { class: "btn ghost", onclick: () => dialog.close() }, t("common.close"))),
  );
  // 正在编辑的 Skill 原有的文件：页上不改它们，保存时照旧带上。
  let files = null;
  const resetForm = () => {
    name.value = ""; name.readOnly = false; description.value = ""; instructions.value = "";
    files = null; filesNote.textContent = ""; cancel.hidden = true;
  };
  const row = (s) => h("div", { class: "conn-row skill-row", "data-name": s.name, "data-scope": s.scope },
    h("div", { class: "grow" },
      h("div", { class: "t" }, s.name, h("span", { class: "chip" }, t(`skills.scope.${s.scope}`))),
      h("div", { class: "m" }, s.description || ""),
      s.shadowed ? h("div", { class: "m skill-shadowed" }, t("skills.shadowed")) : null),
    s.scope === "user" ? h("button", { class: "btn sm skill-edit", onclick: () => editSkill(s.name) }, t("skills.edit")) : null,
    s.scope === "user" ? h("button", { class: "btn sm skill-retire", onclick: () => retireSkill(s.name) }, t("skills.retire")) : null);
  const draw = (data) => {
    if (data.available === false) {
      list.replaceChildren(h("div", { class: "muted skills-unavailable" }, t("skills.unavailable")));
      form.remove();
      return;
    }
    const items = data.skills || [];
    list.replaceChildren(items.length ? h("div", { class: "conn-list" }, items.map(row)) : h("div", { class: "muted" }, t("skills.empty")));
  };
  const reload = async () => {
    try { draw(await api("GET", "/api/skills")); } catch (ex) { report(ex); }
  };
  async function editSkill(n) {
    try {
      const { skill } = await api("GET", `/api/skills/${encodeURIComponent(n)}`);
      name.value = skill.name; name.readOnly = true;
      description.value = skill.description || "";
      instructions.value = skill.instructions || "";
      files = skill.files && skill.files.length ? skill.files : null;
      filesNote.textContent = files ? t("skills.files", { n: files.length }) : "";
      cancel.hidden = false;
      instructions.focus();
    } catch (ex) { report(ex); }
  }
  async function saveSkill() {
    const n = name.value.trim();
    if (!n || !description.value.trim() || !instructions.value.trim()) return;
    const body = { description: description.value, instructions: instructions.value };
    if (files) body.files = files;
    save.disabled = true;
    try {
      await api("PUT", `/api/skills/${encodeURIComponent(n)}`, body);
      toast(t("skills.saved", { name: n }));
      resetForm();
      await reload();
    } catch (ex) { report(ex); } finally { save.disabled = false; }
  }
  async function retireSkill(n) {
    if (!confirm(t("skills.retireConfirm", { name: n }))) return;
    try {
      await api("DELETE", `/api/skills/${encodeURIComponent(n)}`);
      toast(t("skills.retired", { name: n }));
      if (name.readOnly && name.value === n) resetForm();
      await reload();
    } catch (ex) { report(ex); }
  }
  resetForm();
  dialog.addEventListener("close", () => dialog.remove());
  document.body.append(dialog);
  dialog.showModal();
  await reload();
}

// openMcp 是“我的 MCP”（/api/mcp；Runtime 的 N-15）：登记自己的远程 MCP Server（https 地址，鉴权头引用“网络密钥”页里的
// 一个名字，值不经这里），看它的工具，逐个打开并定权限：只读（远端自己说只读的才能选）或不可逆写（每次调用前要你批准）。
// 新出现与参数变了的工具先关着；删掉连接后，正在跑的任务也不能再调它。任务开始时用的是当时打开的工具。
async function openMcp() {
  if (document.querySelector("dialog.mcp")) return;
  const list = h("div", { class: "mcp-list" }, h("div", { class: "row muted" }, h("span", { class: "spinner" }), t("common.loading")));
  const name = h("input", { class: "mcp-name", placeholder: t("mcp.name"), maxlength: 32, autocomplete: "off", spellcheck: "false" });
  const url = h("input", { class: "mcp-url", placeholder: "https://example.com/mcp", autocomplete: "off", spellcheck: "false" });
  const secret = h("select", { class: "mcp-secret" }, h("option", { value: "" }, t("mcp.noSecret")));
  const header = h("input", { class: "mcp-header", placeholder: t("mcp.header"), maxlength: 64, autocomplete: "off", spellcheck: "false" });
  const create = h("button", { class: "btn sm primary mcp-create", onclick: () => createConn() }, t("mcp.create"));
  const form = h("div", { class: "mcp-form" }, h("div", { class: "t" }, t("mcp.add")), name, url, h("div", { class: "row" }, secret, header), create);
  const dialog = h("dialog", { class: "mcp" },
    h("div", { class: "d-head" }, h("h2", {}, t("mcp.title"))),
    h("div", { class: "d-body" }, h("div", { class: "mcp-note" }, t("mcp.note")), list, form),
    h("div", { class: "d-foot" }, h("button", { class: "btn ghost", onclick: () => dialog.close() }, t("common.close"))),
  );
  const open = new Set();
  const stateText = (c) => c.state === "ready" ? t("mcp.ready", { n: c.tools.filter((x) => x.enabled).length, total: c.tools.length })
    : t("mcp.error", { why: t(`mcp.err.${c.error_code}`) === `mcp.err.${c.error_code}` ? c.error_code : t(`mcp.err.${c.error_code}`) });
  const toolRow = (c, tool, edits) => {
    const readOnly = tool.read_only_hint === true;
    const choice = h("select", { class: "mcp-effect", disabled: !!tool.unavailable_reason },
      h("option", { value: "" }, t("mcp.off")),
      h("option", { value: "read", disabled: !readOnly }, readOnly ? t("mcp.read") : t("mcp.readNotOffered")),
      h("option", { value: "write_irreversible" }, t("mcp.write")));
    choice.value = tool.enabled ? tool.effect : "";
    const timeout = h("input", { class: "mcp-timeout", type: "number", min: 1, max: 300, value: tool.timeout_s || 30, title: t("mcp.timeout") });
    const mark = () => edits.set(tool.tool, { tool: tool.tool, enabled: !!choice.value, effect: choice.value, timeout_s: Number(timeout.value) || 30, schema_digest: tool.schema_digest });
    choice.addEventListener("change", mark);
    timeout.addEventListener("change", mark);
    return h("div", { class: "mcp-tool", "data-tool": tool.tool },
      h("div", { class: "grow" },
        h("div", { class: "t" }, tool.tool, h("span", { class: "chip mono" }, tool.name), readOnly ? h("span", { class: "chip" }, t("mcp.readOnlyHint")) : null),
        h("div", { class: "m mcp-desc" }, tool.description || ""),
        tool.unavailable_reason ? h("div", { class: "m mcp-unavailable" }, t(`mcp.err.${tool.unavailable_reason}`)) : null),
      choice, timeout);
  };
  const connRow = (c) => {
    const edits = new Map();
    const tools = open.has(c.connection_id)
      ? h("div", { class: "mcp-tools" },
        c.tools.length ? c.tools.map((x) => toolRow(c, x, edits)) : h("div", { class: "muted" }, t("mcp.noTools")),
        c.tools.length ? h("div", { class: "row" }, h("button", { class: "btn sm primary mcp-save", onclick: () => saveTools(c, edits) }, t("mcp.save"))) : null)
      : null;
    return h("div", { class: "conn-row mcp-row" + (c.state === "ready" ? " on" : ""), "data-name": c.name },
      h("div", { class: "grow" },
        h("div", { class: "t" }, c.name, c.server_name ? h("span", { class: "chip" }, c.server_name) : null, c.secret_name ? h("span", { class: "chip" }, t("mcp.usesSecret", { name: c.secret_name })) : null),
        h("div", { class: "m mono" }, c.url),
        h("div", { class: "m mcp-state" }, stateText(c)),
        tools),
      h("div", { class: "col" },
        h("button", { class: "btn sm mcp-tools-toggle", onclick: () => toggle(c) }, open.has(c.connection_id) ? t("mcp.hideTools") : t("mcp.showTools")),
        h("button", { class: "btn sm mcp-refresh", onclick: () => refresh(c) }, t("mcp.refresh")),
        h("button", { class: "btn sm mcp-delete", onclick: () => removeConn(c) }, t("mcp.delete"))));
  };
  let items = [];
  const draw = () => {
    list.replaceChildren(items.length ? h("div", { class: "conn-list" }, items.map(connRow)) : h("div", { class: "muted" }, t("mcp.empty")));
  };
  const reload = async () => {
    try {
      const data = await api("GET", "/api/mcp");
      if (data.available === false) {
        list.replaceChildren(h("div", { class: "muted mcp-unavailable" }, t("mcp.unavailable")));
        form.hidden = true;
        return;
      }
      // 列表不带参数 schema；展开的连接另取一次全的。
      items = await Promise.all((data.connections || []).map(async (c) => open.has(c.connection_id) ? (await api("GET", `/api/mcp/${encodeURIComponent(c.connection_id)}`)).connection : c));
      draw();
    } catch (ex) { report(ex); }
  };
  const loadSecrets = async () => {
    if (!state.config.network_secrets) return;
    try {
      for (const s of (await api("GET", "/api/secrets")).secrets || []) secret.append(h("option", { value: s.name }, s.name));
    } catch { /* 网络密钥不可用时只能建不带鉴权的连接 */ }
  };
  async function toggle(c) {
    if (open.has(c.connection_id)) open.delete(c.connection_id); else open.add(c.connection_id);
    await reload();
  }
  async function createConn() {
    const n = name.value.trim(), u = url.value.trim();
    if (!n || !u) return;
    create.disabled = true;
    try {
      const { connection, created } = await api("POST", "/api/mcp", { name: n, url: u, secret_name: secret.value, header: header.value.trim() });
      toast(created ? t("mcp.created", { name: n }) : t("mcp.exists", { name: n }));
      if (connection.state !== "ready") toast(stateText(connection), true);
      name.value = ""; url.value = ""; header.value = ""; secret.value = "";
      open.add(connection.connection_id);
      await reload();
    } catch (ex) { report(ex); } finally { create.disabled = false; }
  }
  async function refresh(c) {
    try {
      const r = await api("POST", `/api/mcp/${encodeURIComponent(c.connection_id)}/refresh`);
      toast(r.connection.state === "ready" ? t("mcp.refreshed", { added: r.added.length, disabled: r.disabled.length, removed: r.removed.length }) : stateText(r.connection), r.connection.state !== "ready");
      await reload();
    } catch (ex) { report(ex); }
  }
  async function saveTools(c, edits) {
    if (!edits.size) return;
    const changes = [...edits.values()];
    if (changes.some((x) => x.enabled && x.effect === "write_irreversible") && !confirm(t("mcp.writeConfirm"))) return;
    try {
      await api("PUT", `/api/mcp/${encodeURIComponent(c.connection_id)}/tools`, { tools: changes });
      toast(t("mcp.saved", { name: c.name }));
      await reload();
    } catch (ex) { report(ex); }
  }
  async function removeConn(c) {
    if (!confirm(t("mcp.deleteConfirm", { name: c.name }))) return;
    try {
      await api("DELETE", `/api/mcp/${encodeURIComponent(c.connection_id)}`);
      toast(t("mcp.deleted", { name: c.name }));
      open.delete(c.connection_id);
      await reload();
    } catch (ex) { report(ex); }
  }
  dialog.addEventListener("close", () => dialog.remove());
  document.body.append(dialog);
  dialog.showModal();
  await Promise.all([reload(), loadSecrets()]);
}

// drawGitHubApp 是账号页里的 GitHub App 一段（G-05，/api/github/status）：旧 OAuth 连接的提示、安装或调整的入口、
// 各仓库的远端状况（暂停、卸载、移除、删除时写明怎么办）。
async function drawGitHubApp(slot) {
  let st;
  try { st = await api("GET", "/api/github/status"); } catch (ex) { report(ex); return; }
  const external = { target: "_blank", rel: "noopener" };
  const installs = st.installations || [];
  const lines = [];
  if (!st.connected) lines.push(h("div", { class: "m" }, t("github.notConnected")));
  else if (st.legacy) lines.push(h("div", { class: "gh-note" }, h("span", {}, t("github.legacy")), h("a", { class: "btn sm gh-reconnect", href: "/api/auth/github/login?link=1" }, t("github.reconnect"))));
  lines.push(installs.length
    ? h("div", { class: "gh-line" }, h("span", { class: "m" }, t("github.installed", { n: installs.length })), h("a", { class: "btn sm gh-adjust", href: st.install_url, ...external }, t("github.adjust")))
    : h("div", { class: "gh-line" }, h("span", { class: "m" }, t("github.notInstalled")), h("a", { class: "btn sm primary gh-install", href: st.install_url, ...external }, t("github.install"))));
  const repos = (st.projects || []).map((p) => {
    const s = p.remote_state || "ok";
    return h("div", { class: "gh-repo" + (p.remote_updated ? " gh-updated" : ""), "data-repo": p.repo, "data-state": s },
      h("span", { class: "t" }, p.repo),
      h("span", { class: "m" }, t("github.state." + s), p.remote_updated ? " · " + t("github.remoteUpdated") : null));
  });
  slot.replaceChildren(h("div", { class: "github-app" },
    h("div", { class: "gh-head" }, t("github.title")),
    lines,
    h("div", { class: "gh-sub" }, t("github.repos")),
    repos.length ? h("div", { class: "gh-repos" }, repos) : h("div", { class: "m" }, t("github.noRepos"))));
}

async function disconnect(p, draw) {
  if (!confirm(t("account.disconnectConfirm", { name: p.name }))) return;
  try {
    const r = await api("POST", `/api/account/connections/${p.id}/disconnect`);
    state.me = await api("GET", "/api/me");
    draw(r);
    renderSide();
    toast(t("account.disconnected", { name: p.name }));
  } catch (ex) { report(ex); }
}

// ---------------------------------------------------------------- 外观

function applyTheme() {
  const t = ls.get("cw_theme");
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
}

// ---------------------------------------------------------------- 弹出菜单

let pop = null;

function openPop(anchor, build, opts = {}) {
  if (pop && pop.anchor === anchor) return closePop();
  closePop();
  const el = h("div", { class: "pop", role: "menu" });
  append(el, build(closePop));
  const r = anchor.getBoundingClientRect();
  if (opts.matchWidth) el.style.width = `${Math.max(r.width, 220)}px`;
  document.body.append(el);
  const left = Math.min(Math.max(8, r.left), window.innerWidth - el.offsetWidth - 8);
  let top = r.top - el.offsetHeight - 6;
  if (top < 8) top = Math.min(r.bottom + 6, window.innerHeight - el.offsetHeight - 8);
  el.style.left = `${left}px`;
  el.style.top = `${top}px`;
  anchor.classList.add("open");
  pop = { el, anchor };
}

function closePop() {
  if (!pop) return;
  pop.el.remove();
  pop.anchor.classList.remove("open");
  pop = null;
  if (ui.barDirty) updateComposer(true);
}

function popItem(ic, title, desc, selected, onclick, disabled = false) {
  return h("button", { class: "pop-item", disabled, onclick },
    icon(ic, "sm"), h("div", { class: "grow" }, h("div", { class: "t" }, title), desc ? h("div", { class: "d" }, desc) : null),
    selected ? icon("check", "sm check") : null);
}

// ---------------------------------------------------------------- 登录

function renderLogin(message = "") {
  closeStreams();
  closeDrawer();
  closePop();
  const hashError = new URLSearchParams(location.hash.split("?")[1] || "").get("error");
  const err = h("div", { class: "err-text" }, message || hashError || "");
  const tenant = h("input", { class: "field", placeholder: t("login.tenantPh"), autocomplete: "organization", value: ls.get("cw_tenant") || "" });
  const user = h("input", { class: "field", placeholder: t("login.userPh"), autocomplete: "username", value: ls.get("cw_user") || "" });
  const submit = h("button", { class: "btn primary block", type: "submit" }, t("login.devSubmit"));
  const form = h("form", { class: "col", onsubmit: async (e) => {
    e.preventDefault();
    submit.disabled = true;
    err.textContent = "";
    try {
      const me = await api("POST", "/api/login", { tenant: tenant.value, user: user.value });
      ls.set("cw_tenant", me.tenant);
      ls.set("cw_user", me.user);
      state.me = await api("GET", "/api/me");
      await enterApp();
    } catch (ex) {
      if (ex.message !== "unauthorized") err.textContent = ex.message;
    } finally {
      submit.disabled = false;
    }
  } },
    h("div", {}, h("label", { class: "lbl" }, t("login.tenant")), tenant),
    h("div", {}, h("label", { class: "lbl" }, t("login.user")), user),
    submit,
  );
  // 第三方登录：GitHub 是主按钮，其余 Git 平台收在它下面的“其他 Git 平台”里，Google 单独一个。
  // 开发环境（开着开发登录）里没配置的也列出来、不可点，说明还能接哪些。
  const usable = (p) => p && (p.configured || state.config.dev_login);
  const oauthButton = (p, cls, label) => p.configured
    ? h("a", { class: `btn block ${cls}`, href: `/api/auth/${p.id}/login` }, badge(p.id), h("span", {}, label))
    : h("button", { class: `btn block ${cls}`, type: "button", disabled: true, title: t("login.notConfiguredTitle", { name: p.name, env: `CODING_${p.id.toUpperCase()}` }) },
      badge(p.id), h("span", {}, label), h("span", { class: "off" }, t("login.notConfigured")));
  const all = providerList();
  const gh = all.find((p) => p.id === "github");
  const google = all.find((p) => p.id === "google");
  const others = all.filter((p) => p.git && p.id !== "github" && usable(p));
  const otherList = h("div", { class: "other-providers" + (others.some((p) => p.configured) && !usable(gh) ? "" : " hidden") },
    others.map((p) => oauthButton(p, "provider-btn", p.name)));
  const otherToggle = others.length ? h("button", {
    class: "other-toggle", type: "button",
    onclick: () => { otherList.classList.toggle("hidden"); otherToggle.classList.toggle("open"); },
  }, h("span", {}, t("login.otherGit")), h("span", { class: "names" }, others.map((p) => p.name).join(" · ")), icon("chevDown", "xs caret")) : null;
  const thirdParty = [
    usable(gh) ? oauthButton(gh, "github-btn", t("login.with", { name: gh.name })) : null,
    otherToggle, otherList,
    usable(google) ? oauthButton(google, "google-btn", t("login.with", { name: google.name })) : null,
  ].filter(Boolean);
  const hasThirdParty = usable(gh) || others.length || usable(google);
  mount(h("div", { class: "login-page" },
    h("div", { class: "login-card" },
      h("div", { class: "hero" },
        h("div", { class: "mark big" }, logo("lg")),
        h("h1", {}, "Coding Agent"),
        h("p", {}, t("login.tagline")),
      ),
      h("div", { class: "login-box" },
        thirdParty,
        hasThirdParty && state.config.dev_login ? h("div", { class: "sep" }, t("login.or")) : null,
        state.config.dev_login ? form : null,
        !hasThirdParty && !state.config.dev_login ? h("div", { class: "note" }, t("login.none")) : null,
        err,
      ),
      langSwitch(),
    ),
  ));
}

// langSwitch 是登录页底部的语言切换：每种语言用它自己的名字。
function langSwitch() {
  return h("div", { class: "lang-switch", role: "group", "aria-label": t("menu.language") },
    icon("globe", "xs"),
    LANGS.map((l) => h("button", { class: l.id === lang ? "active" : "", lang: l.tag, "aria-pressed": String(l.id === lang), onclick: () => setLang(l.id) }, l.label)));
}

async function logout() {
  await api("POST", "/api/logout").catch(() => {});
  state.me = null;
  state.project = null;
  state.session = null;
  state.personalConfig = null;
  state.personalChoices.clear();
  state.draftEnvironment = "";
  state.environments = null;
  renderLogin();
}

// ---------------------------------------------------------------- 主界面与路由

async function enterApp() {
  state.personalChoices.clear();
  state.personalConfig = null;
  state.draftEnvironment = "";
  state.environments = null;
  try { state.personalConfig = (await api("GET", "/api/personal-config")).config || null; } catch { /* Older deployments have no personal packages. */ }
  const personalEnv = state.personalConfig?.document?.defaults?.environmentId;
  const rememberedEnvironment = ls.get(draftEnvironmentKey());
  state.draftEnvironment = rememberedEnvironment ?? personalEnv ?? "";
  [state.projects, state.samples, state.sessions] = await Promise.all([api("GET", "/api/projects"), api("GET", "/api/samples"), api("GET", "/api/sessions")]);
  loadActive();
  await loadEnvironments();
  await route();
  resumeWorkspaceOperations().catch(report);
}

function findProject(id) {
  return state.projects.find((p) => p.id === id) || null;
}

function findSession(pid, sid) {
  return state.sessions.find((s) => s.project_id === pid && s.id === sid) || null;
}

// 地址：#/w/<工作区>/s/<会话>、#/w/<工作区>/new（新会话草稿）、#/workspaces、#/activity、#/environments。
async function route() {
  const hash = location.hash;
  let m;
  if ((m = hash.match(/^#\/w\/([^/]+)\/s\/([^/?]+)/)) && findSession(m[1], m[2])) return openSession(m[1], m[2]);
  if ((m = hash.match(/^#\/w\/([^/?]+)\/new/)) && findProject(m[1])) return openDraft(m[1]);
  if (hash.startsWith("#/workspaces")) return go("workspaces");
  if (hash.startsWith("#/activity")) return go("activity");
  if (hash.startsWith("#/environments")) return go("environments");
  return landing();
}

// landing：最近的会话；没有会话时开最近工作区的草稿；什么都没有时是欢迎页。
function landing() {
  const latest = state.sessions[0];
  if (latest && findProject(latest.project_id)) return openSession(latest.project_id, latest.id);
  if (state.projects[0]) return openDraft(state.projects[0].id);
  resetSessionState();
  state.project = null;
  state.session = null;
  state.view = "session";
  setHash("");
  renderApp();
}

let routedHash = "";
function setHash(hash) {
  routedHash = hash;
  if (location.hash !== hash) location.hash = hash;
}

function resetSessionState() {
  closeStreams();
  closeDrawer();
  state.tasks = [];
  state.runs = new Map();
  state.detail = new Map();
  state.artifacts = new Map();
  state.guidance = new Map();
  state.queuedEdit = null;
  state.changes = null;
  state.repos = [];
  state.repoName = "";
  state.env = null;
  state.filesPath = "";
}

async function openSession(pid, sid) {
  resetSessionState();
  state.view = "session";
  state.project = findProject(pid);
  state.session = findSession(pid, sid);
  state.sideOpen = false;
  setHash(`#/w/${pid}/s/${sid}`);
  if (state.session) {
    const base = sbase();
    try {
      const tasks = await api("GET", `${base}/tasks`);
      if (sbase() !== base) return;
      state.tasks = tasks.slice().reverse();
    } catch (ex) { report(ex); }
  }
  renderApp();
  if (state.session) {
    await loadWorkspaceRepos();
    loadChanges();
    loadEnv();
    const focus = focusTask();
    if (focus) loadDetail(focus);
    replayTasks();
  }
}

// openDraft 打开工作区里的新会话草稿：第一条消息发出时才真正开会话（复制工作副本）。
function openDraft(pid) {
  resetSessionState();
  state.view = "session";
  state.project = findProject(pid);
  state.session = null;
  state.sideOpen = false;
  setHash(`#/w/${pid}/new`);
  renderApp();
  if (ui.input) ui.input.focus();
  loadWorkspaceRepos();
}

function go(view) {
  resetSessionState();
  state.view = view;
  state.session = null;
  state.sideOpen = false;
  setHash(`#/${view}`);
  renderApp();
}

// newSession 是左栏的“新建会话”：在当前工作区（没有时取最近活动的一个）开草稿；还没有工作区就先建一个。
function newSession() {
  const p = state.project || findProject((state.sessions[0] || {}).project_id) || state.projects[0];
  if (p) openDraft(p.id);
  else openNewProject();
}

function appClass() {
  return ["app",
    state.sideCollapsed && "side-collapsed",
    state.sideOpen && "side-open",
    !(state.statusOpen && inSession()) && "status-closed",
  ].filter(Boolean).join(" ");
}

function applyLayout() {
  if (ui.root) ui.root.className = appClass();
}

function renderApp() {
  closePop();
  ui.side = h("aside", { class: "side" });
  ui.topbar = h("header", { class: "topbar" });
  ui.scroll = h("div", { class: "scroll", id: "thread" });
  const composing = state.view === "session" && !!state.project;
  ui.dock = composing ? dock() : null;
  if (!composing) { ui.composer = null; ui.input = null; }
  ui.statusBody = h("div", { class: "status-body" });
  ui.root = h("div", { class: appClass() },
    ui.side,
    h("main", { class: "main" }, ui.topbar, ui.scroll, ui.dock),
    h("aside", { class: "status" },
      h("div", { class: "status-head" }, h("span", { class: "h grow" }, t("status.panel")),
        h("button", { class: "btn ghost sm icon-only", title: t("status.hide"), onclick: toggleStatus }, icon("x", "sm"))),
      ui.statusBody),
  );
  mount(ui.root);
  renderSide();
  renderTopbar();
  if (state.view === "workspaces") renderWorkspaces();
  else if (state.view === "activity") renderActivity();
  else if (state.view === "environments") renderEnvironments();
  else renderThread(true);
  if (composing) updateComposer(true);
  renderStatus();
  updateTitle();
}

function toggleStatus() {
  state.statusOpen = !state.statusOpen;
  ls.set("cw_status", state.statusOpen ? "1" : "0");
  applyLayout();
  renderTopbar();
  if (state.statusOpen) renderStatus();
}

function updateTitle() {
  const live = inSession() && currentTask();
  const name = state.view === "workspaces" ? t("nav.workspaces")
    : state.view === "activity" ? t("nav.activity")
      : state.view === "environments" ? t("nav.environments")
      : state.session ? state.session.title || t("session.untitled")
        : state.project ? t("session.newIn", { name: state.project.name }) : "";
  document.title = `${live ? "● " : ""}${name ? name + " · " : ""}Coding Agent`;
}

// 事件来得密：同一帧里只重画一次输入框、右栏、左栏与进行中的列表。
let chromePending = false;
function scheduleChrome() {
  if (chromePending) return;
  chromePending = true;
  requestAnimationFrame(() => {
    chromePending = false;
    if (!state.me) return;
    renderSide();
    if (state.view === "session" && state.project) updateComposer();
    renderStatus();
    if (state.view === "activity") renderActivity();
    updateTitle();
  });
}

// ---------------------------------------------------------------- 左栏

function sourceLabel(p) {
  if (p.layout === "WORKSPACE_LAYOUT_MULTI") return t("repos.multi");
  if (p.repo) return `${providerInfo(p.git_provider).name} · ${p.repo}`;
  if (!p.source || p.source === "empty") return t("source.empty");
  if (p.source.startsWith("sample:")) return t("source.sample");
  return p.source.replace(/^https:\/\//, "");
}

function fmtShortAgo(iso) {
  const ms = Date.now() - new Date(iso);
  if (isNaN(ms)) return "";
  if (ms < 60e3) return t("ago.now");
  if (ms < 3600e3) return t("short.min", { n: Math.floor(ms / 60e3) });
  if (ms < 86400e3) return t("short.hour", { n: Math.floor(ms / 3600e3) });
  if (ms < 7 * 86400e3) return t("short.day", { n: Math.floor(ms / 86400e3) });
  const d = new Date(iso);
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

// 有任务在跑的会话（<工作区>/<会话>）：来自“进行中”，再加上当前会话。
function liveSessions() {
  const live = new Set(state.active.map((a) => `${a.project_id}/${a.session_id}`));
  if (inSession() && currentTask()) live.add(`${state.project.id}/${state.session.id}`);
  return live;
}

// 工作区的顺序：按其中最近活动的会话，没有会话的按创建时间。
function orderedProjects() {
  const last = new Map();
  for (const s of state.sessions) if (!last.has(s.project_id)) last.set(s.project_id, s.updated_at);
  const at = (p) => new Date(last.get(p.id) || p.created_at).getTime() || 0;
  return state.projects.slice().sort((a, b) => at(b) - at(a));
}

function renderSide() {
  if (!ui.side || !state.me) return;
  const me = state.me;
  const name = me.login || me.user;
  const live = liveSessions();
  const nav = (ic, label, onclick, opts = {}) => h("button", { class: "nav-item" + (opts.active ? " active" : ""), title: opts.title || label, onclick },
    icon(ic, "sm"), h("span", { class: "grow" }, label),
    opts.live ? h("span", { class: "spinner" }) : null,
    opts.count ? h("span", { class: "count" }, opts.count) : null,
    opts.kbd ? h("span", { class: "kbd" }, opts.kbd) : null);
  ui.side.replaceChildren(
    h("div", { class: "side-head" },
      h("div", { class: "brand grow" }, h("span", { class: "mark" }, logo()), "Coding Agent"),
      h("button", { class: "btn ghost sm icon-only only-wide", title: t("side.collapse"), onclick: () => { state.sideCollapsed = true; ls.set("cw_side", "0"); applyLayout(); renderTopbar(); } }, icon("panelLeft", "sm")),
      h("button", { class: "btn ghost sm icon-only only-narrow", title: t("common.close"), onclick: () => { state.sideOpen = false; applyLayout(); } }, icon("x", "sm"))),
    h("nav", { class: "side-nav" },
      nav("compose", t("nav.newSession"), newSession, { title: t("nav.newSessionTitle") }),
      nav("search", t("nav.search"), openSearch, { kbd: "Ctrl K" }),
      nav("grid", t("nav.workspaces"), () => go("workspaces"), { active: state.view === "workspaces", count: state.projects.length || null }),
      nav("activity", t("nav.activity"), () => go("activity"), { active: state.view === "activity", count: state.active.length || null, live: state.active.length > 0 }),
      nav("server", t("nav.environments"), () => go("environments"), { active: state.view === "environments", count: (state.environments || []).length || null }),
    ),
    h("div", { class: "side-label" }, t("side.sessions")),
    h("div", { class: "side-list" },
      state.projects.length ? orderedProjects().map((p) => workspaceGroup(p, live)) : h("div", { class: "side-empty" }, t("side.noWorkspaces"))),
    // 整个个人信息块都是按钮：点哪里都打开账号、外观与语言的菜单，菜单与这一块一样宽。
    h("div", { class: "side-foot" },
      h("button", { class: "profile", title: t("menu.title"), "aria-haspopup": "menu", onclick: (e) => openPop(e.currentTarget, userMenu, { matchWidth: true }) },
        h("span", { class: "avatar" }, me.avatar_url ? h("img", { src: me.avatar_url, alt: "" }) : initials(name)),
        h("span", { class: "grow who" }, h("span", { class: "n ellipsis" }, name), h("span", { class: "m ellipsis" }, t("side.tenant", { tenant: me.tenant }))),
        icon("more", "sm more"))),
  );
}

const GROUP_MAX = 6;

function workspaceGroup(p, live) {
  const closed = state.groupsClosed.has(p.id);
  const sessions = state.sessions.filter((s) => s.project_id === p.id);
  const drafting = state.view === "session" && !state.session && state.project && state.project.id === p.id;
  const all = state.groupsAll.has(p.id);
  const shown = all ? sessions : sessions.slice(0, GROUP_MAX);
  const toggle = () => {
    if (closed) state.groupsClosed.delete(p.id); else state.groupsClosed.add(p.id);
    ls.set("cw_groups_closed", [...state.groupsClosed].join(","));
    renderSide();
  };
  const anyLive = sessions.some((s) => live.has(`${p.id}/${s.id}`));
  return h("div", { class: "ws-group" + (closed ? " closed" : "") },
    h("div", { class: "ws-head" },
      h("button", { class: "ws-toggle", title: `${p.name} · ${sourceLabel(p)}`, onclick: toggle },
        icon("chevron", "xs chev"), projectMark(p, "sm"), h("span", { class: "t ellipsis" }, p.name),
        closed && anyLive ? h("span", { class: "spinner" }) : null),
      h("button", { class: "btn ghost xs icon-only ws-add", title: t("ws.newIn", { name: p.name }), onclick: () => openDraft(p.id) }, icon("plus", "xs"))),
    closed ? null : h("div", { class: "ws-sessions" },
      drafting ? h("div", { class: "side-item session active draft" }, icon("compose", "xs"), h("span", { class: "t grow ellipsis" }, t("session.new"))) : null,
      shown.map((s) => sessionItem(p, s, live.has(`${p.id}/${s.id}`))),
      sessions.length > GROUP_MAX ? h("button", { class: "more-link", onclick: () => { if (all) state.groupsAll.delete(p.id); else state.groupsAll.add(p.id); renderSide(); } },
        all ? t("ws.less") : t("ws.showAll", { n: sessions.length })) : null,
      !sessions.length && !drafting ? h("button", { class: "more-link", onclick: () => openDraft(p.id) }, t("ws.noSessions")) : null,
    ),
  );
}

function sessionItem(p, s, isLive) {
  const active = inSession() && state.project.id === p.id && state.session.id === s.id;
  const open = () => { if (!active) openSession(p.id, s.id); else { state.sideOpen = false; applyLayout(); } };
  return h("div", {
    class: "side-item session" + (active ? " active" : ""),
    role: "button", tabindex: 0, title: s.title || t("session.untitled"),
    onclick: open,
    onkeydown: (e) => { if (e.key === "Enter") open(); },
  },
    isLive ? h("span", { class: "spinner", title: t("session.live") }) : s.pr_number ? badge(p.git_provider, "xs") : icon("chat", "xs"),
    h("span", { class: "t grow ellipsis" }, s.title || t("session.untitled")),
    h("span", { class: "when" }, fmtShortAgo(s.updated_at)),
    h("button", { class: "btn ghost xs icon-only item-more", title: t("session.menuTitle"), onclick: (e) => { e.stopPropagation(); openPop(e.currentTarget, (close) => sessionMenu(p, s, close)); } }, icon("more", "xs")),
  );
}

function sessionMenu(p, s, close) {
  return [
    popItem("edit", t("session.rename"), null, false, () => { close(); renameSession(p, s); }),
    popItem("copy", t("session.duplicate"), t("session.duplicateDesc"), false, () => { close(); duplicateSession(p, s); }),
    popItem("trash", t("session.delete"), s.workspace_id !== p.id ? t("session.deleteCopy") : t("session.deleteRecord"), false, () => { close(); deleteSession(p, s); }),
  ];
}

function setSession(s) {
  state.sessions = state.sessions.map((x) => (x.project_id === s.project_id && x.id === s.id ? s : x));
  if (state.session && state.session.project_id === s.project_id && state.session.id === s.id) state.session = s;
}

async function renameSession(p, s) {
  const title = prompt(t("session.titlePrompt"), s.title || "");
  if (!title || !title.trim()) return;
  try {
    setSession(await api("POST", `${sessionURL(p.id, s.id)}/rename`, { title }));
    renderSide();
    renderTopbar();
    updateTitle();
  } catch (ex) { report(ex); }
}

// duplicateSession 复制会话：新会话的工作副本从这个会话复制（Workspace API 的 copy），环境配置沿用，对话不带过去。
async function duplicateSession(p, s) {
  toast(t("session.duplicating"));
  try {
    const dup = await api("POST", `${sessionURL(p.id, s.id)}/duplicate`, { title: t("session.copyTitle", { title: s.title || t("session.untitled") }) });
    state.sessions.unshift(dup);
    toast(t("session.duplicated"));
    openSession(p.id, dup.id);
  } catch (ex) { report(ex); }
}

async function deleteSession(p, s) {
  const title = s.title || t("session.untitled");
  if (!confirm(t(s.workspace_id !== p.id ? "session.deleteConfirmCopy" : "session.deleteConfirm", { title }))) return;
  try {
    await api("DELETE", sessionURL(p.id, s.id));
    state.sessions = state.sessions.filter((x) => !(x.project_id === p.id && x.id === s.id));
    toast(t("session.deleted"));
    if (state.session && state.session.project_id === p.id && state.session.id === s.id) {
      const next = state.sessions.find((x) => x.project_id === p.id);
      if (next) openSession(p.id, next.id); else openDraft(p.id);
    } else renderSide();
  } catch (ex) { report(ex); }
}

function userMenu(close) {
  const theme = ls.get("cw_theme") || "system";
  const set = (value) => { ls.set("cw_theme", value); applyTheme(); close(); };
  const conns = state.me.connections || [];
  const names = conns.map((c) => providerInfo(c.provider).name).join(t("list.sep"));
  return [
    popItem("link", t("account.title"), conns.length ? t("account.connectedList", { n: conns.length, names }) : t("account.connectHint"), false, () => { close(); openAccount(); }),
    h("div", { class: "pop-sep" }),
    h("div", { class: "pop-title" }, t("menu.appearance")),
    popItem("monitor", t("theme.system"), null, theme === "system", () => set("system")),
    popItem("sun", t("theme.light"), null, theme === "light", () => set("light")),
    popItem("moon", t("theme.dark"), null, theme === "dark", () => set("dark")),
    h("div", { class: "pop-sep" }),
    h("div", { class: "pop-title" }, t("menu.language")),
    ...LANGS.map((l) => popItem("globe", l.label, null, l.id === lang, () => { close(); setLang(l.id); })),
    h("div", { class: "pop-sep" }),
    popItem("logout", t("menu.logout"), null, false, () => { close(); logout(); }),
  ];
}

// ---------------------------------------------------------------- 顶栏

function renderTopbar() {
  if (!ui.topbar) return;
  const p = state.project;
  const s = state.session;
  const parts = [
    h("button", { class: "btn ghost sm icon-only only-narrow", title: t("side.open"), onclick: () => { state.sideOpen = true; applyLayout(); } }, icon("menu", "sm")),
    state.sideCollapsed ? h("button", { class: "btn ghost sm icon-only only-wide", title: t("side.expand"), onclick: () => { state.sideCollapsed = false; ls.set("cw_side", "1"); applyLayout(); renderTopbar(); } }, icon("panelLeft", "sm")) : null,
  ];
  if (state.view === "workspaces") parts.push(h("span", { class: "title" }, t("nav.workspaces")));
  else if (state.view === "activity") parts.push(h("span", { class: "title" }, t("nav.activity")));
  else if (state.view === "environments") parts.push(h("span", { class: "title" }, t("nav.environments")));
  else if (p) {
    parts.push(h("button", { class: "crumb ellipsis", title: t("ws.overview"), onclick: () => go("workspaces") }, p.name));
    parts.push(h("span", { class: "crumb-sep" }, "/"));
    parts.push(h("span", { class: "title ellipsis" }, s ? s.title || t("session.untitled") : t("session.new")));
    parts.push(h("div", { class: "chips" },
      p.repo ? h("a", { class: "chip", href: repoURL(p), target: "_blank", rel: "noopener" }, badge(p.git_provider, "xs"), p.repo) : null,
      s && s.branch ? h("span", { class: "chip run mono" }, icon("branch", "xs"), s.branch) : p.base_branch ? h("span", { class: "chip mono" }, icon("branch", "xs"), p.base_branch) : null,
      s && s.pr_url ? h("a", { class: "chip ok", href: s.pr_url, target: "_blank", rel: "noopener" }, `PR #${s.pr_number}`, icon("external", "xs")) : null,
    ));
  }
  if (p && p.layout === "WORKSPACE_LAYOUT_MULTI" && state.view === "session") {
    if (state.session && state.repos.length) {
      const picker = h("select", { class: "field", "aria-label": t("repos.select"), "data-repo-picker": "" },
        state.repos.map((repo) => h("option", { value: repo.name, selected: repo.name === state.repoName }, `${repo.name}${repo.access === "REPO_ACCESS_WRITE" ? "" : " · " + t("repos.readOnly")}`)));
      picker.addEventListener("change", () => { state.repoName = picker.value; state.changes = null; renderTopbar(); renderStatus(); loadChanges(); if (state.drawer) renderDrawer(); });
      parts.push(picker);
    }
    parts.push(h("button", { class: "btn sm", onclick: () => openWorkspaceRepos(p, state.session), "data-repo-manage": "" }, icon("folder", "sm"), t("repos.manage")));
  }
  parts.push(h("span", { class: "grow" }));
  if (inSession()) {
    parts.push(h("button", { class: "btn ghost sm icon-only", title: t("drawer.title"), onclick: () => openDrawer("changes") }, icon("diff", "sm")));
    parts.push(h("button", { class: "btn ghost sm icon-only", title: t("session.menuTitle"), onclick: (e) => openPop(e.currentTarget, (close) => sessionMenu(p, s, close)) }, icon("more", "sm")));
    parts.push(h("button", { class: "btn ghost sm icon-only", title: state.statusOpen ? t("status.hide") : t("status.show"), onclick: toggleStatus }, icon("panelRight", "sm")));
  }
  ui.topbar.replaceChildren(...parts.filter(Boolean));
}

// ---------------------------------------------------------------- 工作区一览、进行中、搜索

function renderWorkspaces() {
  if (!ui.scroll) return;
  const live = liveSessions();
  const cards = orderedProjects().map((p) => {
    const sessions = state.sessions.filter((s) => s.project_id === p.id);
    const running = sessions.filter((s) => live.has(`${p.id}/${s.id}`)).length;
    return h("div", { class: "ws-card" },
      h("div", { class: "row" },
        h("span", { class: "ws-icon" }, projectMark(p, "sm")),
        h("div", { class: "grow" }, h("div", { class: "name ellipsis" }, p.name), h("div", { class: "src ellipsis" }, sourceLabel(p)))),
      h("div", { class: "stats" },
        h("span", {}, t("ws.sessions", { n: sessions.length })),
        running ? h("span", { class: "chip run" }, h("span", { class: "spinner" }), t("ws.running", { n: running })) : null,
        sessions[0] ? h("span", { class: "muted" }, t("ws.recent", { ago: fmtAgo(sessions[0].updated_at) })) : null),
      sessions.length ? h("div", { class: "ws-recent" }, sessions.slice(0, 3).map((s) => h("button", { class: "link-row", onclick: () => openSession(p.id, s.id) },
        live.has(`${p.id}/${s.id}`) ? h("span", { class: "spinner" }) : icon("chat", "xs"), h("span", { class: "grow ellipsis" }, s.title || t("session.untitled")), h("span", { class: "when" }, fmtShortAgo(s.updated_at))))) : null,
      h("div", { class: "actions" },
        h("button", { class: "btn primary sm", onclick: () => openDraft(p.id) }, icon("plus", "sm"), t("nav.newSession")),
        p.layout === "WORKSPACE_LAYOUT_MULTI" ? h("button", { class: "btn sm", onclick: () => openWorkspaceRepos(p, null) }, t("repos.manage")) : null,
        h("span", { class: "grow" }),
        h("button", { class: "btn ghost sm danger", onclick: () => deleteProject(p) }, icon("trash", "sm"), t("ws.delete"))),
    );
  });
  ui.scroll.replaceChildren(h("div", { class: "page" },
    h("div", { class: "page-head" },
      h("div", { class: "grow" }, h("h1", {}, t("nav.workspaces"))),
      h("button", { class: "btn primary", onclick: () => openNewProject() }, icon("plus", "sm"), t("ws.create"))),
    cards.length ? h("div", { class: "ws-grid" }, cards) : welcome(),
  ));
}

function renderActivity() {
  if (!ui.scroll) return;
  const rows = state.active.map((a) => h("div", { class: "act-row", role: "button", tabindex: 0, onclick: () => jumpTo(a.project_id, a.session_id, a.task.id) },
    a.task.status === "TASK_STATUS_RUNNING" ? h("span", { class: "spinner" }) : icon("clock", "sm"),
    h("div", { class: "grow" },
      h("div", { class: "t" }, a.task.message),
      h("div", { class: "m" }, `${a.project_name} / ${a.session_title || t("session.untitled")} · ${fmtAgo(a.task.created_at)}`)),
    statusChip(a.task.status),
    h("button", { class: "btn sm", onclick: (e) => { e.stopPropagation(); cancelTask(a.task.id, sessionURL(a.project_id, a.session_id)); } }, icon("stop", "xs"), t("common.stop")),
  ));
  ui.scroll.replaceChildren(h("div", { class: "page" },
    h("div", { class: "page-head" },
      h("div", { class: "grow" }, h("h1", {}, t("nav.activity")))),
    rows.length ? h("div", { class: "act-list" }, rows)
      : h("div", { class: "empty-page" }, icon("check", "lg"), h("div", {}, t("activity.empty")),
        h("button", { class: "btn sm", onclick: newSession }, icon("compose", "sm"), t("nav.newSession"))),
  ));
}

// ---------------------------------------------------------------- 环境配置

// 环境配置与仓库无关（人工验收 Q16–37）：名称、描述、安装脚本、启动命令、.env 变量与联网需求，只有自己看得到；开新会话时选用。
// 这里只编辑配置；就绪、复用、到期这类运行状态只来自 Runtime 的记录（右栏“环境”），这里不显示也不推断。
// 字节上限与服务端一致（UTF-8 字节，不是字数）：先在本地挡住，免得几十 KB 的脚本白传一趟；其余（.env 的格式、域名）由服务端说。
const ENV_LIMITS = { name: 80, description: 4096, setup_script: 65536, start_command: 65536, env: 16384 };
const ENV_LIMIT_ERRORS = { name: "envcfg.err.name", description: "envcfg.err.description", setup_script: "envcfg.err.setup", start_command: "envcfg.err.start", env: "envcfg.err.env" };
const NETWORK_MODES = [
  { id: "deny", icon: "shield" },
  { id: "allowlist", icon: "list" },
  { id: "open", icon: "globe" },
];
const MAX_HOSTS = 100;

function utf8Bytes(text) {
  return new TextEncoder().encode(text).length;
}

async function loadEnvironments() {
  try {
    state.environments = await api("GET", "/api/environments");
    if (state.view === "environments") renderEnvironments();
    renderSide();
    scheduleChrome();
  } catch { /* 下次再取 */ }
  return state.environments || [];
}

function findEnvironment(id) {
  return (state.environments || []).find((e) => e.id === id) || null;
}

// draftEnvironment 是草稿要用的环境配置：记住的那份已经删掉（列表里没有）时回到默认环境。
function draftEnvironment() {
  const id = state.draftEnvironment;
  if (!id || (state.environments && !findEnvironment(id))) return "";
  return id;
}

function draftEnvironmentKey() {
  return `cw_environment:${state.me?.tenant || ""}/${state.me?.user || ""}`;
}

function setDraftEnvironment(id) {
  state.draftEnvironment = id;
  ls.set(draftEnvironmentKey(), id);
  updateComposer(true);
}

function envVarCount(env) {
  return String(env.env || "").split("\n").filter((l) => l.trim() && !l.trim().startsWith("#")).length;
}

function networkOf(env) {
  const n = env.network || {};
  return { mode: NETWORK_MODES.some((m) => m.id === n.mode) ? n.mode : "deny", hosts: n.allowed_hosts || [] };
}

function networkLabel(env) {
  const n = networkOf(env);
  return n.mode === "allowlist" ? t("envcfg.net.hosts", { n: n.hosts.length }) : t("envcfg.net." + n.mode);
}

function environmentSessions(env) {
  return state.sessions.filter((s) => s.environment_id === env.id);
}

function renderEnvironments() {
  if (!ui.scroll || state.view !== "environments") return;
  const list = state.environments;
  const cards = (list || []).map((env) => {
    const n = networkOf(env);
    const used = environmentSessions(env).length;
    return h("div", { class: "ws-card env-card", "data-environment-id": env.id },
      h("div", { class: "row" },
        h("span", { class: "ws-icon" }, icon("server", "sm")),
        h("div", { class: "grow" }, h("div", { class: "name ellipsis" }, env.name), env.description ? h("div", { class: "env-desc" }, env.description) : null)),
      h("div", { class: "chip-row" },
        h("span", { class: "chip", title: t("envcfg.privacy") }, icon("user", "xs"), t("envcfg.onlyMe")),
        h("span", { class: "chip" + (n.mode === "open" ? " warn" : "") }, icon((NETWORK_MODES.find((m) => m.id === n.mode) || NETWORK_MODES[0]).icon, "xs"), networkLabel(env)),
        env.setup_script ? h("span", { class: "chip" }, icon("terminal", "xs"), t("envcfg.setupChip")) : null,
        env.start_command ? h("span", { class: "chip" }, icon("play", "xs"), t("envcfg.startChip")) : null,
        envVarCount(env) ? h("span", { class: "chip mono" }, t("envcfg.vars", { n: envVarCount(env) })) : null,
        (env.secrets || []).length ? h("span", { class: "chip" }, icon("shield", "xs"), t("envcfg.secretsCount", { n: env.secrets.length })) : null),
      h("div", { class: "stats" }, h("span", { class: "muted" }, used ? t("envcfg.used", { n: used }) : t("envcfg.unused"))),
      h("div", { class: "actions" },
        h("button", { class: "btn sm", "data-environment-edit": "", onclick: () => openEnvironmentEditor(env) }, icon("edit", "sm"), t("envcfg.edit")),
        h("span", { class: "grow" }),
        h("button", { class: "btn ghost sm danger", "data-environment-delete": "", onclick: () => deleteEnvironment(env) }, icon("trash", "sm"), t("envcfg.delete"))),
    );
  });
  ui.scroll.replaceChildren(h("div", { class: "page" },
    h("div", { class: "page-head" },
      h("div", { class: "grow" }, h("h1", {}, t("nav.environments")), h("p", {}, t("envcfg.lead"))),
      h("button", { class: "btn primary", "data-environment-create": "", onclick: () => openEnvironmentEditor(null) }, icon("plus", "sm"), t("envcfg.create"))),
    list === null ? h("div", { class: "row muted" }, h("span", { class: "spinner" }), t("common.loading"))
      : cards.length ? h("div", { class: "ws-grid" }, cards)
        : h("div", { class: "empty-page" }, icon("server", "lg"), h("div", {}, t("envcfg.empty"))),
  ));
}

// openEnvironmentEditor 新建（env 为空）或编辑一份环境配置。保存失败时对话框不关、填的内容都留着。
function openEnvironmentEditor(env) {
  closePop();
  for (const old of document.querySelectorAll("dialog.environment-editor")) { old.close(); old.remove(); }
  const net = env ? networkOf(env) : { mode: "deny", hosts: [] };
  let mode = net.mode;
  const counters = {};
  const field = (name, tag, value, attrs = {}) => {
    const el = h(tag, { class: "field" + (tag === "textarea" ? " mono" : ""), name, value: value || "", ...attrs });
    if (ENV_LIMITS[name]) {
      counters[name] = h("span", { class: "env-bytes" });
      el.addEventListener("input", () => countBytes(name, el));
    }
    return el;
  };
  const fields = {
    name: field("name", "input", env && env.name, { placeholder: t("envcfg.namePh"), autocomplete: "off" }),
    description: field("description", "textarea", env && env.description, { rows: 2, placeholder: t("envcfg.descriptionPh") }),
    setup_script: field("setup_script", "textarea", env && env.setup_script, { rows: 5, spellcheck: "false", placeholder: "pip install -r requirements.txt" }),
    start_command: field("start_command", "textarea", env && env.start_command, { rows: 2, spellcheck: "false", placeholder: "python -m http.server 8000" }),
    env: field("env", "textarea", env && env.env, { rows: 4, spellcheck: "false", placeholder: "MODE=dev\nPORT=8080" }),
    allowed_hosts: field("allowed_hosts", "textarea", net.hosts.join("\n"), { rows: 3, spellcheck: "false", placeholder: t("envcfg.hostsPh") }),
  };
  fields.description.classList.remove("mono");
  const countBytes = (name, el) => {
    const max = ENV_LIMITS[name];
    const n = utf8Bytes(name === "name" || name === "description" ? el.value.trim() : el.value);
    const near = n > max * 0.8;
    counters[name].textContent = near ? t("envcfg.bytes", { n, max }) : "";
    counters[name].classList.toggle("over", n > max);
  };
  for (const name of Object.keys(counters)) countBytes(name, fields[name]);
  const err = h("div", { class: "err-text", role: "alert" });
  const seg = h("div", { class: "seg", role: "radiogroup", "aria-label": t("envcfg.network") });
  const netHint = h("div", { class: "muted small" });
  const hostsBox = h("div", {}, h("label", { class: "lbl" }, t("envcfg.hosts")), fields.allowed_hosts);
  const drawNetwork = () => {
    seg.replaceChildren(...NETWORK_MODES.map((m) => h("button", {
      type: "button", class: mode === m.id ? "active" : "", role: "radio", "aria-checked": String(mode === m.id), "data-network-mode": m.id,
      onclick: () => { mode = m.id; drawNetwork(); },
    }, icon(m.icon, "sm"), t("envcfg.net." + m.id))));
    netHint.textContent = t("envcfg.netHint." + mode);
    hostsBox.classList.toggle("hidden", mode !== "allowlist");
  };
  drawNetwork();
  const block = (label, hint, el, name) => h("div", {},
    h("div", { class: "env-label" }, h("label", { class: "lbl grow" }, label), name ? counters[name] : null),
    el, hint ? h("div", { class: "muted small env-hint" }, hint) : null);
  const save = h("button", { class: "btn primary", type: "submit" }, t("envcfg.save"));
  const cancel = h("button", { class: "btn ghost", type: "button", "data-editor-cancel": "" }, t("common.cancel"));
  // 密钥：只写。已有的只显示名字（值留空就保留原来的），新加的填名字与值；保存后值不再出现在页面上。
  const secretList = h("div", { class: "env-secrets" });
  const secretRows = [];
  const addSecret = (name = "", existing = false) => {
    const nameEl = h("input", { class: "field mono", "data-secret-name": "", value: name, placeholder: "API_KEY", autocomplete: "off", spellcheck: "false", readonly: existing || null });
    const valueEl = h("input", { class: "field mono", type: "password", "data-secret-value": "", autocomplete: "new-password",
      placeholder: existing ? t("envcfg.secretKeep") : t("envcfg.secretValuePh") });
    const item = { nameEl, valueEl };
    item.row = h("div", { class: "env-secret-row", "data-secret-row": "" }, nameEl, valueEl,
      h("button", { type: "button", class: "btn ghost xs icon-only", title: t("envcfg.secretRemove"), onclick: () => { item.row.remove(); secretRows.splice(secretRows.indexOf(item), 1); } }, icon("x", "xs")));
    secretRows.push(item);
    secretList.append(item.row);
    return item;
  };
  for (const sec of (env && env.secrets) || []) addSecret(sec.name, true);
  const secretsBlock = h("div", {},
    h("div", { class: "env-label" }, h("label", { class: "lbl grow" }, t("envcfg.secrets")),
      h("button", { type: "button", class: "btn ghost xs", "data-secret-add": "", onclick: () => addSecret().nameEl.focus() }, icon("plus", "xs"), t("envcfg.secretAdd"))),
    secretList,
    h("div", { class: "muted small env-hint" }, t("envcfg.secretsHint")));
  const form = h("form", { novalidate: true },
    h("div", { class: "d-head" }, h("h2", {}, env ? t("envcfg.editTitle") : t("envcfg.newTitle")),
      h("div", { class: "muted small" }, t("envcfg.editorNote"))),
    h("div", { class: "d-body" },
      h("div", { class: "row env-row" },
        h("div", { class: "grow" }, block(t("envcfg.name"), null, fields.name, "name")),
        h("div", {}, h("label", { class: "lbl" }, t("envcfg.privacy")), h("span", { class: "chip" }, icon("user", "xs"), t("envcfg.onlyMe")))),
      block(t("envcfg.description"), null, fields.description, "description"),
      block(t("envcfg.setup"), t("envcfg.setupHint"), fields.setup_script, "setup_script"),
      block(t("envcfg.start"), t("envcfg.startHint"), fields.start_command, "start_command"),
      block(t("envcfg.env"), t("envcfg.envHint"), fields.env, "env"),
      secretsBlock,
      h("div", { class: "col env-network" }, h("label", { class: "lbl" }, t("envcfg.network")), seg, netHint, hostsBox)),
    // 错误放在按钮旁：表单长，放在最下面时常常看不到。
    h("div", { class: "d-foot" }, h("div", { class: "grow env-err" }, err), cancel, save),
  );
  const dialog = h("dialog", { class: "environment-editor" }, form);
  // 本地先挡字节上限（与服务端同一个数）；挡下时指到那一栏。
  const tooLarge = () => {
    for (const [name, max] of Object.entries(ENV_LIMITS)) {
      const value = name === "name" || name === "description" ? fields[name].value.trim() : fields[name].value;
      const n = utf8Bytes(value);
      if (n > max || (name === "name" && !n)) return { name, text: t(ENV_LIMIT_ERRORS[name], { max, n }) };
    }
    return null;
  };
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    err.textContent = "";
    const bad = tooLarge();
    if (bad) {
      err.textContent = bad.text;
      fields[bad.name].focus();
      return;
    }
    const hosts = mode === "allowlist" ? fields.allowed_hosts.value.split(/[\s,]+/).filter(Boolean) : [];
    if (hosts.length > MAX_HOSTS) {
      err.textContent = t("envcfg.err.hosts", { max: MAX_HOSTS });
      return;
    }
    const body = {
      name: fields.name.value, description: fields.description.value, privacy: "only_me",
      setup_script: fields.setup_script.value, start_command: fields.start_command.value, env: fields.env.value,
      network: { mode, allowed_hosts: hosts },
      // 空着的新行不算；已有的值留空表示保留。
      secrets: secretRows.map((r) => ({ name: r.nameEl.value.trim(), value: r.valueEl.value })).filter((x) => x.name || x.value),
    };
    save.disabled = true;
    save.textContent = t("envcfg.saving");
    try {
      const saved = env ? await api("PUT", `/api/environments/${env.id}`, body) : await api("POST", "/api/environments", body);
      const list = (state.environments || []).filter((x) => x.id !== saved.id);
      state.environments = env ? (state.environments || []).map((x) => (x.id === saved.id ? saved : x)) : [saved, ...list];
      dialog.close();
      dialog.remove(); // close 事件要等下一轮才到：先拿掉，列表画出来时对话框已经不在了
      toast(t("envcfg.saved", { name: saved.name }));
      renderEnvironments();
      renderSide();
      scheduleChrome();
    } catch (ex) {
      if (ex.message !== "unauthorized") err.textContent = ex.message;
    } finally {
      save.disabled = false;
      save.textContent = t("envcfg.save");
    }
  });
  cancel.addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => dialog.remove());
  document.body.append(dialog);
  dialog.showModal();
  fields.name.focus();
}

async function deleteEnvironment(env) {
  const n = environmentSessions(env).length;
  // 还有会话在用时说清楚：它们保留创建时的设置，但密钥立即删除（2026-11-03 人定）。
  if (!confirm(t(n ? "envcfg.deleteConfirmUsed" : "envcfg.deleteConfirm", { name: env.name, n }))) return;
  try {
    await api("DELETE", `/api/environments/${env.id}`);
    state.environments = (state.environments || []).filter((x) => x.id !== env.id);
    toast(t("envcfg.deleted"));
    renderEnvironments();
    renderSide();
    scheduleChrome();
  } catch (ex) { report(ex); }
}

// environmentTool 是输入框下的“环境”：草稿时可以选（默认环境或自己的一份配置），开出会话后固定为当时选的。
function environmentTool() {
  const fixed = !!state.session;
  const id = fixed ? state.session.environment_id || "" : draftEnvironment();
  const env = id ? findEnvironment(id) : null;
  const bound = fixed && state.env && state.env.environment;
  const label = !id ? t("envcfg.default") : env ? env.name : bound && bound.name ? bound.name : t("envcfg.missing");
  const open = async (anchor) => {
    if (state.environments === null) await loadEnvironments();
    openPop(anchor, (close) => {
      const option = (envId, ic, title, desc) => {
        const el = popItem(ic, title, desc, draftEnvironment() === envId, () => { close(); setDraftEnvironment(envId); });
        el.dataset.environmentOption = envId;
        return el;
      };
      return [
        h("div", { class: "pop-title" }, t("envcfg.heading")),
        option("", "box", t("envcfg.default"), t("envcfg.defaultDesc")),
        ...(state.environments || []).map((e) => option(e.id, "server", e.name, e.description || networkLabel(e))),
        h("div", { class: "pop-sep" }),
        popItem("edit", t("envcfg.manage"), null, false, () => { close(); go("environments"); }),
        h("div", { class: "pop-note" }, t("envcfg.pendingNote")),
      ];
    });
  };
  return h("button", {
    class: "tool", "data-environment-select": "", disabled: fixed,
    title: fixed ? t("envcfg.fixedTitle") : t("envcfg.selectTitle"),
    onclick: (e) => open(e.currentTarget),
  }, icon("server", "sm"), h("span", { class: "v" }, label), fixed ? null : icon("chevDown", "caret"));
}

function environmentKey() {
  const id = state.session ? state.session.environment_id || "" : draftEnvironment();
  const bound = state.env && state.env.environment ? state.env.environment.name : "";
  return [id, bound, (state.environments || []).map((e) => `${e.id}:${e.name}`).join(",")].join("/");
}

// openSearch 是搜索面板（Ctrl/⌘ K）：按标题与工作区名找会话，按名字与来源找工作区。
function openSearch() {
  closePop();
  if (document.querySelector("dialog.palette")) return;
  const input = h("input", { class: "pal-input", placeholder: t("search.placeholder"), "aria-label": t("nav.search") });
  const list = h("div", { class: "pal-list" });
  const dialog = h("dialog", { class: "palette" }, h("div", { class: "pal-head" }, icon("search", "sm"), input, h("span", { class: "kbd" }, "Esc")), list);
  let items = [];
  let sel = 0;
  const pick = (i) => { const it = items[i]; if (!it) return; dialog.close(); it.run(); };
  const draw = () => {
    const q = input.value.trim().toLowerCase();
    const has = (text) => String(text || "").toLowerCase().includes(q);
    const sessions = state.sessions.filter((s) => !q || has(s.title) || has((findProject(s.project_id) || {}).name)).slice(0, 30).map((s) => ({
      group: t("side.sessions"), mark: () => icon("chat", "sm"), title: s.title || t("session.untitled"), sub: `${(findProject(s.project_id) || {}).name || ""} · ${fmtAgo(s.updated_at)}`, run: () => openSession(s.project_id, s.id),
    }));
    const projects = state.projects.filter((p) => !q || has(p.name) || has(sourceLabel(p))).slice(0, 10).map((p) => ({
      group: t("nav.workspaces"), mark: () => projectMark(p, "sm"), title: p.name, sub: t("search.newIn", { source: sourceLabel(p) }), run: () => openDraft(p.id),
    }));
    items = [...sessions, ...projects];
    sel = Math.min(sel, Math.max(0, items.length - 1));
    const out = [];
    items.forEach((it, i) => {
      if (i === 0 || items[i - 1].group !== it.group) out.push(h("div", { class: "pal-group" }, it.group));
      out.push(h("button", { class: "pal-item" + (i === sel ? " sel" : ""), onclick: () => pick(i), onmousemove: () => { if (sel !== i) { sel = i; draw(); } } },
        it.mark(), h("span", { class: "t ellipsis" }, it.title), h("span", { class: "m ellipsis" }, it.sub)));
    });
    list.replaceChildren(...(out.length ? out : [h("div", { class: "pal-empty" }, t("search.none"))]));
    const cur = list.querySelector(".pal-item.sel");
    if (cur) cur.scrollIntoView({ block: "nearest" });
  };
  input.addEventListener("input", () => { sel = 0; draw(); });
  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); sel = Math.min(sel + 1, items.length - 1); draw(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); sel = Math.max(sel - 1, 0); draw(); }
    else if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); pick(sel); }
  });
  dialog.addEventListener("close", () => dialog.remove());
  dialog.addEventListener("click", (e) => { if (e.target === dialog) dialog.close(); });
  document.body.append(dialog);
  dialog.showModal();
  draw();
  input.focus();
}

// ---------------------------------------------------------------- 任务的派生状态

function statusOf(t) {
  const run = state.runs.get(t.id);
  const fin = run && run.events.find((e) => e.type === "task_finished");
  return (fin && fin.value.status) || t.status;
}

function liveTasks() {
  return state.tasks.filter((t) => !TERMINAL.has(statusOf(t)));
}

// 同一项目（Session）的任务串行：最早的那个没结束的就是正在跑的。
function currentTask() {
  return liveTasks()[0] || null;
}

function focusTask() {
  return currentTask() || state.tasks[state.tasks.length - 1] || null;
}

function lastFinished() {
  for (let i = state.tasks.length - 1; i >= 0; i--) {
    const run = state.runs.get(state.tasks[i].id);
    const fin = run && run.events.find((e) => e.type === "task_finished");
    if (fin) return { task: state.tasks[i], result: fin.value };
  }
  return null;
}

function pendingAsk(task) {
  const run = state.runs.get(task.id);
  if (!run) return null;
  const events = run.events;
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i];
    if (ev.type !== "human_input_required") continue;
    const v = ev.value || {};
    const qid = pick(v, "question_id", "questionId");
    if (state.replied.has(qid) || events.slice(i + 1).some((e) => !QUIET.has(e.type) || interruptApplied(e))) return null;
    const raw = pick(v, "options", "choices") || ["approve", "reject"];
    const options = raw.map((o) => (typeof o === "string" ? { id: o, label: o } : { id: pick(o, "id", "value", "option"), label: pick(o, "label", "text", "id", "value") }));
    return { qid, question: questionText(v) || pick(v, "question", "prompt", "summary", "message") || t("ask.default"), options };
  }
  return null;
}

// 立刻打断生效了：问题已经撤回。
function interruptApplied(e) {
  return e.type === "guidance_updated" && e.value.state === "applied" && e.value.mode === "interrupt";
}

function phase() {
  if (!state.project) return { mode: "none" };
  const live = liveTasks();
  if (live.length) {
    const current = live[0];
    const ask = pendingAsk(current);
    return { mode: ask ? "waiting" : "running", current, queued: live.slice(1), ask };
  }
  const last = state.tasks[state.tasks.length - 1];
  if (!last) return { mode: "empty" };
  return { mode: statusOf(last) === "TASK_STATUS_DONE" ? "done" : "failed", last };
}

function liveInfo(task) {
  const run = state.runs.get(task.id);
  const events = run ? run.events : [];
  let round = 0;
  for (const e of events) if (e.type === "round_started") round = num(pick(e.value, "round_seq")) || round;
  const steps = workSteps(events);
  const now = steps.length ? phaseNow(task, events).text : task.status === "TASK_STATUS_QUEUED" ? t("status.queued") : t("run.connecting");
  const elapsed = events.length ? Date.now() - new Date(events[0].time) : 0;
  return { round, now, elapsed, steps: steps.length };
}

// ---------------------------------------------------------------- 对话

function renderThread(stick = false) {
  const thread = ui.scroll;
  if (!thread) return;
  const nearBottom = stick || thread.scrollHeight - thread.scrollTop - thread.clientHeight < 120;
  let content;
  if (!state.project) content = welcome();
  else if (!state.tasks.length && !pendingTurnShown()) content = projectEmpty();
  else content = [state.tasks.map(turn), pendingTurn()];
  // 在底部附近就跟到底；往上翻着看的时候保住位置，不被新事件拽走。
  keepScroll(thread, () => thread.replaceChildren(h("div", { class: "thread" }, content)));
  if (nearBottom) thread.scrollTop = thread.scrollHeight;
}

function renderTurn(taskId) {
  const old = document.getElementById("turn-" + taskId);
  const t = state.tasks.find((x) => x.id === taskId);
  if (!old || !t) return renderThread();
  const thread = ui.scroll;
  const nearBottom = thread.scrollHeight - thread.scrollTop - thread.clientHeight < 120;
  old.replaceWith(turn(t));
  if (nearBottom) thread.scrollTop = thread.scrollHeight;
}

function scrollToTurn(id) {
  const el = document.getElementById("turn-" + id);
  if (el) el.scrollIntoView({ behavior: "smooth", block: "start" });
}

function welcome() {
  const sample = state.samples[0];
  const conns = gitConnections();
  return h("div", { class: "hero-empty" },
    h("div", { class: "mark big" }, logo("lg")),
    h("h2", {}, t("welcome.title")),
    h("div", { class: "suggestions" },
      sample ? suggestion("box", t("welcome.sample"), sample.description, () => quickSample(sample)) : null,
      suggestion("branch", conns.length ? t("welcome.myRepos") : t("welcome.clone"),
        conns.length ? t("welcome.myReposDesc", { names: conns.map((c) => providerInfo(c.provider).name).join(t("list.sep")) }) : t("welcome.cloneDesc"),
        () => openNewProject("git")),
      suggestion("folder", t("source.empty"), t("welcome.emptyDesc"), () => openNewProject("empty")),
    ),
  );
}

function suggestion(ic, title, desc, onclick) {
  return h("button", { class: "suggestion", onclick }, icon(ic), h("div", {}, h("div", { class: "t" }, title), h("div", { class: "d" }, desc)));
}

async function quickSample(sample) {
  try {
    const p = await api("POST", "/api/projects", { name: sample.name, sample: sample.id });
    state.projects.unshift(p);
    openDraft(p.id);
    setComposer(sample.prompt);
  } catch (ex) { report(ex); }
}

// projectEmpty 是新会话（草稿或还没有任务的会话）的开场：给几条起手的任务。
function projectEmpty() {
  const sample = state.samples.find((s) => state.project.source === "sample:" + s.id);
  const ideas = [
    sample ? ["sparkles", t("idea.fixSample"), sample.prompt] : null,
    ["eye", t("idea.explain"), t("idea.explainPrompt")],
    ["check", t("idea.fix"), t("idea.fixPrompt")],
    ["terminal", t("idea.tests"), t("idea.testsPrompt")],
  ].filter(Boolean);
  return h("div", { class: "hero-empty" },
    h("div", { class: "mark big" }, logo("lg")),
    h("h2", {}, t("empty.title", { name: state.project.name })),
    h("div", { class: "suggestions" }, ideas.map(([ic, title, prompt]) => suggestion(ic, title, prompt.length > 60 ? prompt.slice(0, 58) + "…" : prompt, () => setComposer(prompt)))),
  );
}

function modelLabel(id) {
  return (MODELS.find((m) => m.id === id) || {}).label || id;
}

function turn(task) {
  const run = state.runs.get(task.id);
  const events = run ? run.events : [];
  const finished = events.find((e) => e.type === "task_finished");
  const status = statusOf(task);
  const running = !TERMINAL.has(status);
  let tail = null;
  if (finished) tail = result(task, finished.value);
  else if (running) tail = draftView(run) || waitingLine(task);
  else if (!run) tail = h("div", { class: "result-foot" }, statusChip(status), h("button", { class: "btn ghost sm", onclick: () => loadRun(task, true) }, t("turn.load")));
  // 每来一个事件整轮重画：这里不带进场动画（只有刚发出的那一轮在“提交中”时升上来一次）。
  const queued = status === "TASK_STATUS_QUEUED";
  const editing = state.queuedEdit && state.queuedEdit.taskId === task.id;
  return h("section", { class: "turn", id: "turn-" + task.id },
    h("div", { class: "msg-user" + (queued ? " queued-item" : ""), "data-task-id": queued ? task.id : null },
      editing ? queuedEditor(task) : h("div", { class: "bubble" }, task.pending_message || task.message),
      h("div", { class: "meta" }, fmtTime(task.created_at),
        task.sandbox === "read" ? h("span", { class: "chip info" }, icon("eye", "xs"), t("sandbox.read")) : null,
        task.model && task.model !== "balanced" ? h("span", { class: "chip" }, modelLabel(task.model)) : null,
        queued && !editing ? h("button", { class: "btn ghost xs queued-edit", onclick: () => startQueuedEdit(task) }, icon("edit", "xs"), t("queued.edit")) : null),
    ),
    h("div", { class: "msg-agent" },
      h("div", { class: "agent-mark" + (running ? " live" : ""), title: running ? t("turn.working") : null }, logo()),
      h("div", { class: "agent-body" }, planArea(events), subAgentArea(events), worklog(task, events, running, finished), asks(events), guidanceList(task), tail),
    ),
  );
}

// 计划区（N-04）：Runtime 在规划后、每轮结束与收尾时推 plan_updated。只看最新的一份，整份替换（不按步骤合并）；
// 没有计划的任务（fast）不画。current_step 那一步另加 current。
const PLAN_STATUSES = new Set(["pending", "active", "done", "blocked"]);

function planArea(events) {
  let plan = null;
  for (const e of events) if (e.type === "plan_updated" && Array.isArray(e.value.steps)) plan = e.value;
  if (!plan || !plan.steps.length) return null;
  return h("div", { class: "plan" },
    h("div", { class: "plan-title" }, t("plan.title")),
    h("ol", { class: "plan-steps" }, plan.steps.map((s) => {
      const status = PLAN_STATUSES.has(s.status) ? s.status : "pending";
      const current = s.id && s.id === plan.current_step;
      return h("li", { class: "plan-step" + (current ? " current" : ""), "data-status": status, title: t("plan.status." + status) },
        h("span", { class: "plan-mark" }), h("span", { class: "plan-text" }, s.title || s.id));
    })));
}

// 并行的子 Agent（N-16）：node_started 的 mode 为 sub_agent 的 Goal Node 各一行，按开始的先后；之后 node_id 是它的 tool_status
// 是它的进度（几个子 Agent 的事件交错到达）。调用次数按 step 算；node_finished 之后按 status 定成功或失败。同一个 id 在不同轮里各算各的。
function subAgentArea(events) {
  const agents = new Map();
  for (const e of events) {
    const v = e.value || {};
    const key = v.round_seq + ":" + v.node_id;
    if (e.type === "node_started" && v.mode === "sub_agent" && !agents.has(key)) {
      agents.set(key, { node: v.node_id, steps: new Set(), last: null, state: "running" });
    } else if (agents.has(key)) {
      const a = agents.get(key);
      if (e.type === "tool_status") {
        a.steps.add(v.step);
        a.last = v.tool + (v.target ? " · " + v.target : "");
      } else if (e.type === "node_finished") {
        a.state = v.status === "succeeded" ? "succeeded" : "failed";
      }
    }
  }
  if (!agents.size) return null;
  return h("div", { class: "subagents" },
    h("div", { class: "subagents-title" }, t("subagent.title")),
    [...agents.values()].map((a) => h("div", { class: "subagent", "data-node": a.node, "data-state": a.state, title: t("subagent.state." + a.state) },
      h("span", { class: "subagent-mark" }),
      h("span", { class: "subagent-name" }, a.node),
      h("span", { class: "subagent-steps muted" }, t("subagent.steps", { n: a.steps.size })),
      a.last ? h("span", { class: "subagent-last" }, a.last) : null)));
}

function waitingLine(task) {
  const live = liveTasks();
  const ahead = live.indexOf(task);
  if (ahead > 0) {
    return h("div", { class: "waiting-line" }, icon("clock", "sm"), t("queue.ahead", { n: ahead }),
      h("button", { class: "btn ghost xs", onclick: () => cancelTask(task.id) }, t("queue.cancel")));
  }
  const run = state.runs.get(task.id);
  if (run && run.events.length) return null; // 进度在工作过程与输入框上方
  return h("div", { class: "waiting-line" }, h("span", { class: "spinner" }), run ? t("run.starting") : t("run.connecting"),
    h("span", { class: "since", "data-since": task.created_at }, fmtSince(task.created_at)));
}

// draftDelta 把一条逐字增量（DELTA：node_id、offset、text）拼进这个节点的草稿。offset 是 Runtime 按字符（码点）数的位置：
// 接着末尾就追加；落在已有范围里（activity 重试从 0 重来）就从那里重写；超出末尾（连上时前面已经推过）就从这里另起，前面标省略。
function draftDelta(run, type, v) {
  const piece = String(v.text || "");
  if (!piece) return;
  const node = String(v.node_id || "");
  const offset = Math.max(0, Number(v.offset) || 0);
  run.drafts = run.drafts || new Map();
  const d = run.drafts.get(node) || { text: { base: 0, chars: [] }, thinking: { base: 0, chars: [] } };
  const part = type === "token_delta" ? d.text : d.thinking;
  const end = part.base + part.chars.length;
  if (offset >= part.base && offset <= end) part.chars.splice(offset - part.base, Infinity, ...piece);
  else { part.base = offset; part.chars = [...piece]; }
  // 最近更新的节点排在最后，草稿画它。
  run.drafts.delete(node);
  run.drafts.set(node, d);
}

// draftView 是还在生成的回答：有正文画正文（前面没收全时带省略号），只有思考时画思考的末尾。
function draftView(run) {
  if (!run || !run.drafts || !run.drafts.size) return null;
  const d = [...run.drafts.values()].pop();
  if (d.text.chars.length) return h("div", { class: "answer draft" }, md((d.text.base ? "…" : "") + d.text.chars.join("")));
  const thinking = d.thinking.chars;
  if (!thinking.length) return null;
  return h("div", { class: "draft-thinking" }, (thinking.length > 400 || d.thinking.base ? "…" : "") + thinking.slice(-400).join(""));
}

// pendingTurn 是正在提交的那一轮：消息先出现，下面说明在做什么、等了多久（开会话要复制工作副本，借沙箱要几秒到十几秒）。
function pendingTurn() {
  const p = pendingTurnShown();
  if (!p) return null;
  const enter = p.shown ? "" : " enter";
  p.shown = true;
  return h("section", { class: "turn pending" + enter },
    h("div", { class: "msg-user" }, h("div", { class: "bubble" }, p.message)),
    h("div", { class: "msg-agent" },
      h("div", { class: "agent-mark live" }, logo()),
      h("div", { class: "agent-body" },
        h("div", { class: "waiting-line" }, h("span", { class: "spinner" }), h("span", {}, t(p.phase === "session" ? "pending.session" : "pending.submit")),
          h("span", { class: "since", "data-since": p.started }, fmtSince(p.started))),
        p.phase === "session" ? h("div", { class: "indeterminate" }) : null)),
  );
}

// pendingTurnShown 是这里该显示的“提交中”：只在发出它的那个工作区（与会话）里，提交途中切到别处不跟过去。
function pendingTurnShown() {
  const p = state.pending;
  if (!p || !state.project || p.project !== state.project.id) return null;
  if (p.session !== (state.session ? state.session.id : null)) return null;
  return p;
}

// fmtSince 是“已经等了多久”（不到 2 秒不显示，免得一闪而过）。
function fmtSince(since) {
  const ms = Date.now() - new Date(since).getTime();
  return ms >= 2000 ? fmtDuration(ms) : "";
}

function asks(events) {
  return events.filter((e) => e.type === "human_input_required").map((ev) => {
    const v = ev.value || {};
    const qid = pick(v, "question_id", "questionId");
    const q = questionText(v) || pick(v, "question", "prompt", "summary", "message") || t("ask.short");
    const answered = state.replied.has(qid) || events.some((e) => e.event_id > ev.event_id && !QUIET.has(e.type));
    return answered
      ? h("div", { class: "ask-inline done" }, icon("check", "sm"), h("span", {}, t("ask.replied", { q })))
      : h("div", { class: "ask-inline" }, icon("alert", "sm"), h("span", {}, h("strong", {}, t("ask.needs")), q));
  });
}

function worklog(task, events, running, finished) {
  const steps = workSteps(events);
  if (!steps.length) return null;
  const isOpen = state.open.has(task.id) || (running && !state.closed.has(task.id));
  const first = events[0];
  const elapsed = first ? (finished ? new Date(finished.time) : Date.now()) - new Date(first.time) : 0;
  const head = running
    ? [h("span", { class: "spinner" }), h("span", { class: "grow", id: "wl-" + task.id }, workingText(steps.length, elapsed))]
    : [icon(finished && finished.value.status === "TASK_STATUS_DONE" ? "check" : "clock", "sm lead"), h("span", { class: "grow" }, t("work.done", { dur: fmtDuration(elapsed), n: steps.length }))];
  // 只有这次新来的步骤带进场动画：第一次画（打开会话、载入历史）时一个都不动。
  const shown = state.shownSteps.get(task.id);
  state.shownSteps.set(task.id, new Set(steps.map((e) => e.event_id)));
  const live = running ? phaseNow(task, events) : null;
  // “此刻”那一行换了内容（新的阶段、新的一次调用）才带进场动画。
  const nowBefore = state.shownNow.get(task.id);
  state.shownNow.set(task.id, live ? live.lastId : null);
  const nowEnter = !!shown && !!live && live.lastId !== nowBefore;
  // 点开、收起的那一次重画带一次性的过渡；之后来事件重画时不重放。
  const toggled = state.toggledLog && state.toggledLog.id === task.id ? (isOpen ? " opening" : " closing") : "";
  if (toggled) state.toggledLog = null;
  return h("div", { class: "worklog" + (isOpen ? " open" : "") + toggled },
    h("button", { class: "worklog-head", onclick: () => {
      if (isOpen) { state.open.delete(task.id); state.closed.add(task.id); } else { state.open.add(task.id); state.closed.delete(task.id); }
      state.toggledLog = { id: task.id };
      renderTurn(task.id);
    } }, head, icon("chevron", "sm chev")),
    h("div", { class: "worklog-steps" }, steps.map((ev) => step(ev, task.id, !!shown && !shown.has(ev.event_id))),
      live ? h("div", { class: "step now" + (nowEnter ? " enter" : "") },
        h("div", { class: "step-icon" }, h("span", { class: "spinner" })),
        h("div", { class: "step-main" }, h("div", { class: "step-title" }, live.text)),
        h("span", { class: "step-time since", "data-since": live.since }, fmtSince(live.since))) : null),
  );
}

// phaseNow 是进行中的任务此刻在做什么、从什么时候开始：按最后一个事件推（规划、执行、审查、下一轮）。
function phaseNow(task, events) {
  const steps = timeline(events);
  const last = steps[steps.length - 1];
  if (!last) return { text: t("run.starting"), since: task.created_at, lastId: null };
  let text = stepInfo(last).now;
  if (last.type === "tool_status" && last.value.state === "running" && last.value.tool === "shell_run"
    && !steps.some((e) => e.type === "tool_status" && e.value.tool === "shell_run" && e.value.state !== "running")) {
    // 这个任务里第一次跑命令：可能要先借一个沙箱环境（冷启动几秒到十几秒），先说一声。
    text += " · " + t("tool.sandboxMaybe");
  }
  if (last.type === "node_finished") {
    // 这一轮的节点都结束了：Reviewer 在审查。
    const stage = [...steps].reverse().find((e) => e.type === "stage_started");
    const nodes = stage && Array.isArray(stage.value.nodes) ? stage.value.nodes.map((n) => (typeof n === "string" ? n : pick(n, "id"))) : [];
    const done = new Set(steps.filter((e) => e.type === "node_finished" && e.event_id > (stage ? stage.event_id : 0)).map((e) => pick(e.value, "node_id")));
    if (nodes.length && nodes.every((n) => done.has(n))) text = t("step.reviewNow");
  } else if (last.type === "verdict") {
    text = t("step.nextRoundNow");
  }
  return { text, since: last.time, lastId: last.event_id };
}

function workingText(steps, elapsed) {
  return t("work.live", { n: steps }) + (elapsed ? " · " + fmtDuration(elapsed) : "");
}

// timeline 是进度事件（不含心跳、问题与结局）；workSteps 是画成步骤的那些：一次工具调用有两个 tool_status（running，然后
// succeeded 或 failed），步骤里只有有了结果的那个，正在跑的那次显示在“此刻”那一行（phaseNow）。
function timeline(events) {
  return events.filter((e) => !QUIET.has(e.type) && e.type !== "task_finished" && e.type !== "human_input_required");
}

function workSteps(events) {
  return timeline(events).filter((e) => !(e.type === "tool_status" && e.value && e.value.state === "running"));
}

// toolText 是一次工具调用给人看的说法：常用的工具各有说法（正在读、读了……），其余的写工具名。
const KNOWN_TOOLS = new Set(["repo_read", "repo_list", "repo_grep", "repo_edit", "shell_run", "web_fetch"]);
function toolText(v, done) {
  const tool = String(v.tool || "");
  const target = String(v.target || "");
  const text = KNOWN_TOOLS.has(tool)
    ? t(`tool.${done ? "done" : "run"}.${tool}`, { target })
    : t(done ? "tool.done" : "tool.run", { tool, target });
  return text.trim();
}

function stepInfo(ev) {
  const v = ev.value || {};
  let ic = "box", kind = "", title = ev.type, detail = "", chips = null, now = "";
  switch (ev.type) {
    case "task_accepted": ic = "check"; kind = "ok"; title = t("step.accepted"); now = t("step.acceptedNow"); break;
    case "round_started":
      ic = "play"; kind = "run"; title = t("step.round", { n: pick(v, "round_seq") ?? "" }); now = t("step.roundNow");
      detail = v.rerun ? t("verdict.replan") : "";
      break;
    case "stage_started": {
      ic = "layers"; kind = "run";
      const goal = goalText(v) || t("step.execute");
      title = t("step.plan", { goal });
      now = t("step.planNow", { goal });
      const exitWhen = exitWhenText(v);
      detail = exitWhen ? t("step.exitWhen", { x: exitWhen }) : "";
      if (v.triage_level) detail = (detail ? detail + "\n" : "") + "Triage: " + v.triage_level;
      const nodes = Array.isArray(v.nodes) ? v.nodes : [];
      chips = nodes.length ? h("div", { class: "step-chips" }, nodes.map((n) => h("span", { class: "chip mono" }, typeof n === "string" ? n : pick(n, "id") || t("step.node")))) : null;
      break;
    }
    case "node_started": {
      const id = pick(v, "node_id") || t("step.node");
      ic = "terminal"; title = t("step.nodeStart", { id }); now = t("step.nodeRunning", { id });
      break;
    }
    case "node_finished": {
      const st = String(pick(v, "status") || "");
      const failed = /fail|denied|error/i.test(st);
      const skipped = st === "skipped"; // 上游没成功而跳过（upstream_failed）：既不是失败，也不算完成
      const id = pick(v, "node_id") || t("step.node");
      ic = failed ? "x" : skipped ? "alert" : "check"; kind = failed ? "err" : skipped ? "warn" : "ok";
      title = t(failed ? "step.nodeFailed" : skipped ? "step.nodeSkipped" : "step.nodeDone", { id });
      now = skipped ? title : t(failed ? "step.nodeFailedNow" : "step.nodeDoneNow", { id });
      detail = [[pick(v, "role"), st && st !== "succeeded" ? st : ""].filter(Boolean).join(" · "), errorText(v)].filter(Boolean).join("\n");
      break;
    }
    case "tool_status": {
      const state = String(v.state || "");
      ic = v.tool === "shell_run" ? "terminal" : v.tool === "repo_edit" ? "compose" : "eye";
      if (state === "running") {
        kind = "run";
        title = toolText(v, false);
        now = title;
      } else {
        kind = state === "failed" ? "err" : "ok";
        title = state === "failed" ? t("tool.failed", { what: toolText(v, false) }) : toolText(v, true);
        const parts = [];
        if (v.duration_ms != null) parts.push(t("tool.took", { dur: fmtMs(num(v.duration_ms)) }));
        // 本机后端几乎不花时间：不到 0.3 秒的不提，免得满屏“启动沙箱用了 0 秒”。
        if (num(v.sandbox_startup_ms) >= 300) parts.push(t("tool.sandboxStarted", { dur: fmtMs(num(v.sandbox_startup_ms)) }));
        detail = [parts.join(" · "), state === "failed" ? errorText(v) : ""].filter(Boolean).join("\n");
        now = t("step.thinkingNow");
      }
      break;
    }
    case "verdict": {
      const key = String(pick(v, "verdict") || "").replace(/^VERDICT_/, "").toLowerCase();
      const [label, k] = VERDICTS[key] || [key || "—", ""];
      ic = "shield"; kind = k === "info" ? "run" : k;
      title = t("step.verdict", { label, key });
      now = t("step.verdictNow", { label });
      detail = reasonText(v);
      break;
    }
    case "decision_retry":
      ic = "refresh"; kind = "warn";
      title = retryTitle(v);
      detail = retryDetail(v);
      break;
  }
  return { ic, kind, title, detail, chips, now: now || title };
}

function step(ev, taskId, enter) {
  const s = stepInfo(ev);
  // 展开了的“原始事件”记在 state 里：鼠标移开、来了新事件整轮重画，都保持展开。
  const key = taskId + ":" + ev.event_id;
  return h("div", { class: `step ${s.kind}` + (enter ? " enter" : "") },
    h("div", { class: "step-icon" }, icon(s.ic, "xs")),
    h("div", { class: "step-main" },
      h("div", { class: "step-title" }, s.title),
      s.detail ? h("div", { class: "step-detail" }, s.detail) : null,
      s.chips,
      h("details", {
        class: "raw", open: state.rawOpen.has(key),
        ontoggle: (e) => {
          const el = e.currentTarget;
          if (el.open) state.rawOpen.add(key); else state.rawOpen.delete(key);
          el.classList.toggle("just-opened", el.open); // 展开那一下的淡入；重画出的新元素不带它，不重放
        },
      }, h("summary", {}, t("step.raw")), h("pre", {}, JSON.stringify(ev.value || {}, null, 2))),
    ),
    h("span", { class: "step-time" }, fmtClock(ev.time)),
  );
}

function deliverableText(result) {
  const d = result.deliverable && result.deliverable.value !== undefined ? result.deliverable.value : result.deliverable;
  if (typeof d === "string") return d;
  if (!d || typeof d !== "object") return "";
  if (result.status === "TASK_STATUS_ABORTED" && Array.isArray(d.not_completed)) return abortText(d);
  const t = pick(d, "answer", "summary", "text", "content", "result", "report", "message");
  if (typeof t === "string") {
    const trimmed = t.trim();
    if (trimmed.startsWith("{")) {
      try {
        const inner = JSON.parse(trimmed);
        const s = pick(inner, "answer", "summary", "text", "content", "result");
        if (typeof s === "string") return s;
      } catch { /* 不是 JSON，原样用 */ }
    }
    return t;
  }
  return objectMarkdown(d);
}

// abort 的交付：部分结果（最近一个成功的交付节点的结果）放在说明后面。
function abortText(d) {
  const partial = d.partial === undefined ? "" : deliverableText({ deliverable: d.partial });
  return abortMarkdown(d) + (partial ? `\n\n### ${t("abort.partial")}\n${partial}` : "");
}

// 结构化的交付（没有 text、answer 这类字段）按字段排成小节，比原样的 JSON 好读。
function objectMarkdown(d) {
  const inlineValue = (v) => (v !== null && typeof v === "object" ? "`" + JSON.stringify(v) + "`" : String(v));
  const out = [];
  for (const [k, v] of Object.entries(d)) {
    out.push(`### ${k.replace(/_/g, " ")}`);
    if (Array.isArray(v)) out.push(v.map((x) => `- ${inlineValue(x)}`).join("\n"));
    else if (v !== null && typeof v === "object") out.push(Object.entries(v).map(([kk, vv]) => `- **${kk}**: ${inlineValue(vv)}`).join("\n"));
    else out.push(String(v));
    out.push("");
  }
  return out.join("\n");
}

function taskCost(t) {
  const d = state.detail.get(t.id);
  return d && d.cost !== undefined ? d.cost : t.cost_usd || 0;
}

// 任务交付的文件（N-18）：结束后取一次（同一个任务不重复取），取到了再重画；过期的标出来、不给链接。
function loadArtifacts(task) {
  const base = sbase();
  if (!base || state.artifacts.has(task.id)) return;
  state.artifacts.set(task.id, { list: null });
  api("GET", base + "/tasks/" + task.id + "/artifacts")
    .then((d) => state.artifacts.set(task.id, { list: (d && d.artifacts) || [] }))
    .catch(() => state.artifacts.set(task.id, { failed: true }))
    .finally(() => renderThread());
}

function artifactItems(task) {
  const a = state.artifacts.get(task.id);
  if (!a) {
    loadArtifacts(task);
    return null;
  }
  if (a.failed) return [h("div", { class: "muted" }, t("artifacts.loadFailed"))];
  if (!a.list || !a.list.length) return null;
  return [
    h("div", { class: "sp-sub" }, t("artifacts.title")),
    a.list.map((f) => h("div", { class: "deliverable" },
      f.url
        ? h("a", { href: f.url, download: f.name, title: t("artifacts.download") }, f.name)
        : h("span", { class: "deliverable-expired", title: t("artifacts.expired") }, f.name),
      h("span", { class: "muted" }, f.url ? fmtSize(f.size_bytes || 0) : t("artifacts.expired")),
    )),
  ];
}

function result(task, r) {
  const text = deliverableText(r);
  const ok = r.status === "TASK_STATUS_DONE";
  const cost = taskCost(task);
  const rounds = r.rounds || task.rounds;
  const n = state.changes ? state.changes.files.length : 0;
  // 交付区先列文件，再给文字（N-18）。
  const files = artifactItems(task);
  return h("div", { class: "col" },
    files ? h("div", { class: "deliverables" }, files) : null,
    text ? h("div", { class: "answer" }, md(text)) : null,
    r.error ? h("div", { class: "error-box" }, typeof r.error === "string" ? r.error : pick(r.error, "message") || JSON.stringify(r.error)) : null,
    h("div", { class: "result-foot" },
      statusChip(r.status),
      rounds ? h("span", { class: "chip" }, t("rounds", { n: rounds })) : null,
      cost ? h("span", { class: "chip" }, fmtUSD(cost)) : null,
      h("span", { class: "grow" }),
      text ? h("button", { class: "btn ghost xs", onclick: () => copy(text) }, icon("copy", "xs"), t("common.copy")) : null,
      ok && n && task === state.tasks[state.tasks.length - 1] ? h("button", { class: "btn ghost xs", onclick: () => openDrawer("changes") }, icon("diff", "xs"), t("changes.review")) : null,
    ),
  );
}

// ---------------------------------------------------------------- 事件流

function closeStreams() {
  for (const run of state.runs.values()) if (run.es) run.es.close();
}

// toEvent 把事件流的一条 data（或会话快照里的一条事件，格式相同）变成 run.events 里的一项。
function toEvent(data, msgType = "message", lastEventId = 0) {
  const id = Number(data.event_id || lastEventId || 0);
  const type = msgType === "message" ? String(data.type || "").replace(/^TASK_EVENT_TYPE_/, "").toLowerCase() : msgType;
  return { event_id: id, type, time: data.emitted_at, value: (data.payload && data.payload.value) || {} };
}

// loadRun 接上一个任务的事件。seed 是会话快照里这个任务的一份（WEB-SNAP）：{ events, cursor }。快照里的事件先画出来；
// 已经结束的不再开事件流，没结束的从 cursor 续传（事件流地址带 last_event_id）。没有 seed 时从头回放。
function loadRun(t, force = false, seed = null) {
  if (state.runs.has(t.id) && !force) return state.runs.get(t.id).promise;
  const base = sbase();
  const run = { events: [], done: false, es: null };
  state.runs.set(t.id, run);
  const seen = new Set();
  for (const data of (seed && seed.events) || []) {
    const ev = toEvent(data);
    if (ev.event_id && seen.has(ev.event_id)) continue;
    if (ev.event_id) seen.add(ev.event_id);
    run.events.push(ev);
    if (ev.type === "task_finished") {
      t.status = ev.value.status || t.status;
      if (!state.closed.has(t.id)) state.open.delete(t.id);
      guidanceTaskFinished(t);
      run.done = true;
    } else if (ev.type === "guidance_updated") {
      guidanceEvent(t, ev.value);
    }
  }
  if (seed) {
    scheduleTurn(t.id);
    scheduleChrome();
  }
  if (run.done) {
    run.promise = Promise.resolve();
    return run.promise;
  }
  run.promise = new Promise((resolve) => {
    const cursor = seed && seed.cursor ? `?last_event_id=${seed.cursor}` : "";
    historyTraffic.requests++;
    const es = new EventSource(`${base}/tasks/${t.id}/events${cursor}`);
    run.es = es;
    let timer = null;
    const finish = () => { es.close(); run.done = true; clearTimeout(timer); resolve(); };
    const onEvent = (msg) => {
      if (sbase() !== base) return finish();
      if (msg.type === "resnapshot_required") {
        // 游标过期（N-19）：中间的事件已经过了保留期，重新取快照。
        es.close();
        clearTimeout(timer);
        resolve();
        resnapshot(base);
        return;
      }
      let data;
      try { data = JSON.parse(msg.data); } catch { return; }
      if (msg.type === "token_delta" || msg.type === "thinking_delta") {
        // 逐字增量（DELTA）没有 id：lastEventId 还是上一条持久事件的，不能拿来去重；不进 run.events，只拼草稿。
        if (!run.done) draftDelta(run, msg.type, (data.payload && data.payload.value) || {});
        scheduleTurn(t.id);
        return;
      }
      const ev = toEvent(data, msg.type, msg.lastEventId);
      const id = ev.event_id;
      if (id && seen.has(id)) return;
      if (id) seen.add(id);
      const type = ev.type;
      run.events.push(ev);
      const replaying = TERMINAL.has(t.status) && !run.live;
      if (type === "task_finished") {
        t.status = ev.value.status || t.status;
        if (!state.closed.has(t.id)) state.open.delete(t.id);
        guidanceTaskFinished(t);
        run.drafts = null;
        finish();
        if (!replaying) afterFinish(t);
      } else if (type === "node_finished" && !replaying) {
        throttledChanges();
      } else if (type === "guidance_updated") {
        guidanceEvent(t, ev.value);
      } else if (type === "task_message_edited" && !replaying) {
        queuedEdited(t, ev.value);
      } else if ((type === "verdict" || type === "round_started") && !replaying) {
        // 各轮的重要输出在 Task State 里（右栏“输出”）。
        loadDetail(t);
      }
      scheduleTurn(t.id);
      scheduleChrome();
    };
    run.live = !TERMINAL.has(t.status);
    for (const name of EVENT_TYPES) es.addEventListener(name, onEvent);
    es.onmessage = onEvent;
    es.onerror = () => {
      // 结束了的任务流关掉后浏览器会重连；已经拿到 task_finished 就不再连。
      if (run.done) es.close();
    };
    if (TERMINAL.has(t.status)) timer = setTimeout(finish, 15000);
  });
  return run.promise;
}

const pendingTurns = new Set();
function scheduleTurn(id) {
  if (pendingTurns.has(id)) return;
  pendingTurns.add(id);
  requestAnimationFrame(() => { pendingTurns.delete(id); renderTurn(id); });
}

// paintFromSnapshot 用会话快照（WEB-SNAP，Runtime 的 N-19）画首屏：快照里有的任务带着它的事件与游标接上，没有的进行中任务从头接；
// 取不到快照（老的 Runtime 没有这个端点）时返回 false，由调用方照旧逐个回放。
async function paintFromSnapshot(base) {
  let snap;
  historyTraffic.requests++;
  try { snap = await api("GET", base + "/snapshot"); } catch { return false; }
  if (sbase() !== base || !snap || !Array.isArray(snap.tasks)) return false;
  const byId = new Map(snap.tasks.map((s) => [s.task_id, s]));
  for (const t of state.tasks.slice(-REPLAY_LIMIT)) {
    const s = byId.get(t.id);
    if (s) {
      const events = (s.events || []).slice();
      if (s.plan && !events.some((e) => e.event_id === s.plan.event_id)) events.unshift(s.plan);
      events.sort((a, b) => Number(a.event_id || 0) - Number(b.event_id || 0));
      loadRun(t, true, { events, cursor: Number(s.last_event_id) || 0 });
    } else if (!TERMINAL.has(t.status)) {
      loadRun(t, true);
    }
  }
  return true;
}

let resnapshotting = false;
async function resnapshot(base) {
  if (resnapshotting || sbase() !== base) return;
  resnapshotting = true;
  try {
    closeStreams();
    if (!(await paintFromSnapshot(base))) for (const t of state.tasks.slice(-REPLAY_LIMIT)) loadRun(t, true);
  } finally {
    resnapshotting = false;
  }
}

// PERF-00（Runtime 的 workspace-read 文档第 2 节）：打开会话时取历史的请求数（取快照、开事件流）。replayTasks 量这一段的耗时
// 与请求数，画完报给 coding-web 记成指标；只是计量，不改加载的方式。
const historyTraffic = { requests: 0 };

async function replayTasks() {
  const base = sbase();
  const started = performance.now();
  const before = historyTraffic.requests;
  if (await paintFromSnapshot(base)) return reportHistoryLoad(base, "snapshot", started, before);
  if (sbase() !== base) return;
  const recent = state.tasks.slice(-REPLAY_LIMIT);
  // 取不到快照时先批量取事件（HIST-01）；Runtime 给不了的任务（或整个端点用不了时全部）才逐个回放。
  const left = await paintFromEvents(base, recent);
  if (sbase() !== base) return;
  const replay = left || recent;
  // 先接上进行中的（实时），再从最新的往前一个一个重放已结束的（浏览器对同一主机的连接数有限）：打开会话先看到最后的对话，
  // 更早的在上面陆续补上；停在底部时 renderTurn 一直跟到底。
  for (const t of replay) if (!TERMINAL.has(t.status)) loadRun(t);
  for (const t of replay.slice().reverse()) {
    if (sbase() !== base) return;
    if (TERMINAL.has(t.status)) await loadRun(t);
  }
  reportHistoryLoad(base, left ? "events" : "replay", started, before);
}

// paintFromEvents 用批量取的事件（HIST-01，Runtime 的 ListSessionEvents）画历史：每个任务从头取，有 more 的拿 next_after_event_id
// 接着取；取全了的任务带着事件与游标接上（结束的不再开事件流，没结束的从游标续传）。返回 Runtime 给不了、要逐个回放的任务
// （不存在、events_unavailable、没取完）；第一次就取不到（老的 Runtime 或 coding-web）时返回 null，由调用方全部逐个回放。
async function paintFromEvents(base, recent) {
  if (!recent.length) return [];
  const cursors = new Map(recent.map((t) => [t.id, 0]));
  const events = new Map(recent.map((t) => [t.id, []]));
  const done = new Map();
  for (let round = 0; cursors.size && round < 50; round++) {
    let page = null;
    historyTraffic.requests++;
    try {
      page = await api("POST", base + "/events", { cursors: [...cursors].map(([task_id, after_event_id]) => ({ task_id, after_event_id })) });
    } catch { /* 下面按取不到处理 */ }
    if (sbase() !== base) return [];
    if (!page || !Array.isArray(page.tasks)) {
      if (!round) return null;
      break; // 翻页中途取不到：没取完的逐个回放
    }
    const byId = new Map(page.tasks.map((x) => [x.task_id, x]));
    for (const id of [...cursors.keys()]) {
      const x = byId.get(id);
      if (!x || !x.found || x.events_unavailable) {
        cursors.delete(id);
        continue;
      }
      events.get(id).push(...(x.events || []));
      const next = Number(x.next_after_event_id) || 0;
      if (x.more && next > cursors.get(id)) cursors.set(id, next);
      else {
        cursors.delete(id);
        done.set(id, next);
      }
    }
  }
  const left = [];
  for (const t of recent) {
    if (done.has(t.id)) loadRun(t, true, { events: events.get(t.id), cursor: done.get(t.id) });
    else left.push(t);
  }
  return left;
}

// reportHistoryLoad 报一次历史加载的耗时与请求数；中途换了会话不报，报不上不影响页面。
function reportHistoryLoad(base, mode, started, before) {
  if (sbase() !== base) return;
  const report = { mode, duration_ms: Math.round(performance.now() - started), requests: historyTraffic.requests - before };
  api("POST", "/api/metrics/history-load", report).catch(() => { /* 只是计量 */ });
}

function afterFinish(t) {
  loadChanges();
  loadEnv();
  loadDetail(t);
  loadActive();
  refreshTasks();
  const next = currentTask();
  if (next) loadDetail(next);
}

// cancelTask 停止一个任务；base 是它所在会话的接口前缀，默认当前会话。
async function cancelTask(taskId, base = sbase()) {
  if (!base || !confirm(t("task.stopConfirm"))) return;
  try {
    await api("POST", `${base}/tasks/${taskId}/cancel`);
    toast(t("task.stopRequested"));
    loadActive();
  } catch (ex) { report(ex); }
}

// ---------------------------------------------------------------- 数据

async function loadChanges() {
  const base = sbase();
  if (!base) return;
  try {
    const selected = state.repoName;
    if (isMulti() && !selected) { state.changes = { files: [], diff: "" }; scheduleChrome(); return; }
    const c = await api("GET", gitURL("changes"));
    if (sbase() === base && state.repoName === selected) { state.changes = c; scheduleChrome(); }
  } catch { /* 下次再取 */ }
}

// 任务运行中按节点完成刷新改动，最多每 15 秒一次（读改动会动 git 的 index，别和 Agent 的命令抢）。
let lastChanges = 0;
function throttledChanges() {
  if (Date.now() - lastChanges < 15000) return;
  lastChanges = Date.now();
  loadChanges();
}

async function loadEnv() {
  const base = sbase();
  if (!base) return;
  try {
    const e = await api("GET", `${base}/env`);
    if (sbase() === base) { state.env = e; scheduleChrome(); }
  } catch { /* 下次再取 */ }
}

async function loadActive() {
  try {
    state.active = await api("GET", "/api/tasks/active");
    renderSide();
    scheduleChrome();
  } catch { /* 下次再取 */ }
}

async function loadDetail(t) {
  const base = sbase();
  if (!base) return;
  try {
    const data = await api("GET", `${base}/tasks/${t.id}`);
    if (sbase() !== base) return;
    const rt = data.runtime || {};
    const ts = rt.taskState && rt.taskState.value ? rt.taskState.value : null;
    const rounds = ts && Array.isArray(ts.rounds) ? ts.rounds : [];
    state.detail.set(t.id, {
      state: ts,
      usage: rt.usage || null,
      roundSeq: rt.roundSeq || 0,
      cost: rounds.length ? rounds.reduce((s, r) => s + num(r.cost_usd), 0) : undefined,
    });
    // 刷新页面后的纠偏：事件里没有文字，文字在 Task State。
    for (const g of (ts && Array.isArray(ts.guidance) ? ts.guidance : [])) {
      const list = guidanceOf(t.id);
      const item = list.find((x) => x.id === g.guidance_id);
      if (item) {
        if (!item.text) item.text = g.text;
      } else {
        list.push({ id: g.guidance_id, text: g.text, mode: g.mode, state: g.state });
      }
    }
    const rec = state.tasks.find((x) => x.id === t.id);
    if (rec && data.task) Object.assign(rec, data.task, { status: TERMINAL.has(rec.status) ? rec.status : data.task.status });
    scheduleChrome();
  } catch { /* 下次再取 */ }
}

async function refreshTasks() {
  const base = sbase();
  if (!base) return;
  try {
    const list = await api("GET", `${base}/tasks`);
    if (sbase() !== base) return;
    const byId = new Map(list.map((t) => [t.id, t]));
    for (const t of state.tasks) {
      const n = byId.get(t.id);
      if (n) Object.assign(t, n, { status: TERMINAL.has(t.status) && !TERMINAL.has(n.status) ? t.status : n.status });
    }
    scheduleChrome();
  } catch { /* 下次再取 */ }
}

// ---------------------------------------------------------------- 输入框

// 输入框是一张卡片：上沿是运行状态或待确认的问题，中间是输入与发送；沙箱、模型倾向与用量放在卡片外的下方。
function dock() {
  ui.next = h("div", { class: "next-steps" });
  ui.cStatus = h("div", { class: "hidden" });
  ui.cAsk = h("div", { class: "hidden" });
  ui.cRight = h("div", { class: "c-right" });
  ui.cOpts = h("div", { class: "hidden" });
  ui.input = h("textarea", { class: "c-input", rows: 1, id: "composer", "aria-label": t("composer.aria") });
  ui.input.addEventListener("input", () => { autosize(); refreshSend(); });
  ui.input.addEventListener("keydown", onComposerKey);
  ui.composer = h("div", { class: "composer" }, ui.cStatus, ui.cAsk, h("div", { class: "c-row" }, ui.input, ui.cRight), ui.cOpts);
  ui.tools = h("div", { class: "c-tools" });
  ui.barKey = "";
  return h("div", { class: "dock" }, h("div", { class: "dock-inner" }, ui.next, ui.composer, ui.tools));
}

function autosize() {
  if (!ui.input) return;
  ui.input.style.height = "auto";
  ui.input.style.height = Math.min(ui.input.scrollHeight, 260) + "px";
}

function refreshSend() {
  if (ui.send) ui.send.disabled = !ui.input.value.trim() || state.sending;
}

function onComposerKey(e) {
  if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
  e.preventDefault();
  const ph = phase();
  const urgent = e.ctrlKey || e.metaKey;
  if (ph.mode === "waiting" && !urgent) {
    // 等确认时 Enter 在有补充说明、且有“带说明重新规划”这样的选项时是回答它；否则是纠偏（继续等，回复后带上）。
    const guided = ph.ask.options.find((o) => /guidance|replan/i.test(o.id));
    if (guided && ui.input.value.trim()) reply(ph.current, ph.ask, guided.id);
    else steer(ph.current, "next_step");
    return;
  }
  if (ph.mode === "running" || ph.mode === "waiting") {
    // 运行中：Enter 纠偏（下一个检查点生效），Ctrl+Enter 立刻打断；排队（下一个任务）在发送按钮旁的菜单里。
    steer(ph.current, urgent ? "interrupt" : "next_step");
    return;
  }
  submit();
}

// ---------------------------------------------------------------- 纠偏、立刻打断、撤回与修改

function guidanceId() {
  const bytes = crypto.getRandomValues(new Uint8Array(12));
  return "g" + [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function guidanceOf(taskId) {
  if (!state.guidance.has(taskId)) state.guidance.set(taskId, []);
  return state.guidance.get(taskId);
}

function redrawGuidance(taskId) {
  scheduleTurn(taskId);
  scheduleChrome();
}

async function steer(task, mode) {
  const text = ui.input.value.trim();
  if (!text) return;
  const item = { id: guidanceId(), text, mode, state: "sending" };
  guidanceOf(task.id).push(item);
  ui.input.value = "";
  autosize();
  refreshSend();
  redrawGuidance(task.id);
  try {
    await api("POST", `${sbase()}/tasks/${task.id}/guidance`, { guidance_id: item.id, text, mode });
  } catch (ex) {
    // 任务刚好结束或还没开始：这条没有发出去，提示可以作为新任务发送。
    item.state = "not_applied";
    redrawGuidance(task.id);
    report(ex);
  }
}

// 事件里没有文字：刷新页面后的纠偏，文字从 Task State 补（loadDetail）。
function guidanceEvent(task, v) {
  const id = pick(v, "guidance_id");
  const list = guidanceOf(task.id);
  let item = list.find((g) => g.id === id);
  if (!item) {
    if (v.state === "rejected" || v.state === "retracted") return;
    item = { id, text: "", mode: v.mode || "next_step", state: v.state };
    list.push(item);
  }
  switch (v.state) {
    case "pending":
      if (item.state !== "applied") item.state = "pending";
      item.reason = v.reason_code || "";
      break;
    case "applied":
      item.state = "applied";
      item.reason = "";
      item.editing = false;
      break;
    case "retracted":
      list.splice(list.indexOf(item), 1);
      break;
    case "rejected":
      guidanceRejected(item, list, v.reason_code || "");
      break;
  }
  redrawGuidance(task.id);
}

function guidanceRejected(item, list, reason) {
  if (reason === "already_applied") {
    item.state = "applied";
    item.editing = false;
    toast(t("guidance.rejected.already_applied"), true);
  } else if (reason === "not_found") {
    list.splice(list.indexOf(item), 1);
    toast(t("guidance.rejected.not_found"), true);
  } else if (reason === "task_finished") {
    item.state = "not_applied";
  } else {
    item.state = "not_applied";
    toast(t("guidance.rejected.too_many_pending"), true);
  }
}

// 任务结束时还没生效的：提示可以作为新任务发送。
function guidanceTaskFinished(task) {
  for (const g of guidanceOf(task.id)) {
    if (g.state === "sending" || g.state === "pending") {
      g.state = "not_applied";
      g.editing = false;
    }
  }
}

async function retractGuidance(task, item) {
  try {
    await api("POST", `${sbase()}/tasks/${task.id}/guidance/${item.id}/retract`);
  } catch (ex) { report(ex); }
}

async function saveGuidanceEdit(task, item, text) {
  if (!text || text === item.text) {
    item.editing = false;
    return redrawGuidance(task.id);
  }
  item.editing = false;
  item.text = text;
  redrawGuidance(task.id);
  try {
    await api("POST", `${sbase()}/tasks/${task.id}/guidance/${item.id}/edit`, { text });
  } catch (ex) { report(ex); }
}

function inlineEditor(wrapClass, value, onInput, onSave, onCancel) {
  const area = h("textarea", { class: "c-input", rows: 2, value });
  area.addEventListener("input", () => onInput(area.value));
  area.addEventListener("keydown", (e) => {
    if (e.isComposing) return;
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      onSave(area.value.trim());
    } else if (e.key === "Escape") {
      e.stopPropagation();
      onCancel();
    }
  });
  requestAnimationFrame(() => { if (area.isConnected) area.focus(); });
  return h("div", { class: wrapClass }, area);
}

function guidanceList(task) {
  const items = (state.guidance.get(task.id) || []).filter((g) => g.text);
  if (!items.length) return null;
  return h("div", { class: "guidance-list" }, items.map((g) => guidanceItem(task, g)));
}

function guidanceItem(task, g) {
  const open = g.state === "sending" || g.state === "pending";
  const changeable = open && g.mode === "next_step";
  const label = g.mode === "interrupt" ? t("composer.interrupt") : t("composer.steer");
  return h("div", { class: "guidance-item" + (g.state === "applied" ? " applied" : "") + (g.state === "not_applied" ? " missed" : ""), "data-guidance-id": g.id, "data-mode": g.mode },
    h("div", { class: "guidance-head" },
      h("span", { class: "chip" + (g.mode === "interrupt" ? " warn" : "") }, label),
      g.state === "applied" ? h("span", { class: "guidance-state" }, icon("check", "xs"), t("guidance.applied")) : null,
      open ? h("span", { class: "guidance-state muted" }, g.reason === "irreversible_in_flight" ? t("guidance.waitIrreversible") : t("guidance.pending")) : null,
      g.state === "not_applied" ? h("span", { class: "guidance-state muted" }, t("guidance.notApplied")) : null,
      h("span", { class: "grow" }),
      changeable && !g.editing ? h("button", { class: "btn ghost xs guidance-edit", onclick: () => { g.editing = true; g.draft = g.text; redrawGuidance(task.id); } }, icon("edit", "xs"), t("guidance.edit")) : null,
      changeable ? h("button", { class: "btn ghost xs guidance-retract", onclick: () => retractGuidance(task, g) }, icon("x", "xs"), t("guidance.retract")) : null,
      g.state === "not_applied" ? h("button", { class: "btn xs guidance-send-as-task", onclick: () => {
        const list = guidanceOf(task.id);
        list.splice(list.indexOf(g), 1);
        setComposer(g.text);
        redrawGuidance(task.id);
      } }, icon("queue", "xs"), t("guidance.sendAsTask")) : null,
    ),
    g.editing
      ? inlineEditor("guidance-editor", g.draft ?? g.text, (v) => { g.draft = v; }, (v) => saveGuidanceEdit(task, g, v), () => { g.editing = false; redrawGuidance(task.id); })
      : h("div", { class: "guidance-text" }, g.text),
  );
}

// 修改排队中的消息：改不改由 Runtime 定（任务可能刚好开始了），结果靠事件 task_message_edited。

function startQueuedEdit(task) {
  state.queuedEdit = { taskId: task.id, draft: task.pending_message || task.message };
  scheduleTurn(task.id);
}

function queuedEditor(task) {
  return inlineEditor("queued-editor", state.queuedEdit.draft, (v) => { state.queuedEdit.draft = v; }, (v) => saveQueuedEdit(task, v), () => { state.queuedEdit = null; scheduleTurn(task.id); });
}

async function saveQueuedEdit(task, message) {
  state.queuedEdit = null;
  scheduleTurn(task.id);
  if (!message || message === task.message) return;
  const before = task.pending_message;
  task.pending_message = message;
  scheduleTurn(task.id);
  try {
    await api("POST", `${sbase()}/tasks/${task.id}/message`, { message });
  } catch (ex) {
    task.pending_message = before;
    scheduleTurn(task.id);
    report(ex);
  }
}

function queuedEdited(task, v) {
  const applied = v.state === "applied";
  if (applied && task.pending_message) task.message = task.pending_message;
  task.pending_message = "";
  if (!applied) toast(t("queued.editTooLate"), true);
  scheduleTurn(task.id);
  api("POST", `${sbase()}/tasks/${task.id}/message/result`, { applied }).catch(() => { /* 只影响这里记的消息，下次再说 */ });
}

function updateComposer(force = false) {
  if (!ui.composer || !state.project) return;
  const ph = phase();
  ui.composer.className = "composer" + (ph.mode === "waiting" ? " waiting" : "");

  if (ph.mode === "running" || ph.mode === "waiting") {
    const info = liveInfo(ph.current);
    ui.cStatus.className = "c-status";
    ui.cStatus.replaceChildren(...[
      ph.mode === "running" ? h("span", { class: "spinner" }) : icon("alert", "sm"),
      h("span", { class: "grow ellipsis" }, info.round ? t("composer.round", { n: info.round }) : "", h("span", { class: "what" }, ph.mode === "waiting" ? t("composer.waiting") : info.now)),
      ph.queued.length ? h("span", { class: "chip" }, t("composer.queued", { n: ph.queued.length })) : null,
      h("span", { class: "when", id: "c-elapsed" }, info.elapsed ? fmtTimer(info.elapsed) : ""),
      h("button", { class: "btn xs", title: t("composer.stopTitle"), onclick: () => cancelTask(ph.current.id) }, icon("stop", "xs"), t("common.stop")),
    ].filter(Boolean));
  } else {
    ui.cStatus.className = "hidden";
    ui.cStatus.replaceChildren();
  }

  if (ph.mode === "waiting") {
    ui.cAsk.className = "c-ask";
    ui.cAsk.replaceChildren(h("div", { class: "h" }, icon("alert", "xs"), t("ask.title")), h("div", { class: "q" }, ph.ask.question));
  } else {
    ui.cAsk.className = "hidden";
    ui.cAsk.replaceChildren();
  }

  ui.input.placeholder = ["empty", "done", "failed", "running", "waiting"].includes(ph.mode) ? t("composer.ph." + ph.mode) : "";

  // 工具条与下一步只在内容变了时重画，免得打断打开着的菜单。
  const m = meter();
  const key = [ph.mode, ph.ask && ph.ask.qid, state.settings.sandbox, state.settings.model, JSON.stringify(state.networkRequest), meterText(m),
    state.changes ? state.changes.files.length : -1, ph.mode === "failed" || ph.mode === "done" ? ph.last.id : "",
    state.env && state.env.sandbox ? state.env.sandbox.mode : "", environmentKey()].join("|");
  if (!force && key !== ui.barKey && pop && ui.tools.contains(pop.anchor)) {
    ui.barDirty = true; // 菜单开着：等它关上再重画
  } else if (force || key !== ui.barKey) {
    ui.barKey = key;
    ui.barDirty = false;
    if (ph.mode === "waiting") {
      // 等确认时，发送按钮换成问题给的选项。
      ui.cRight.replaceChildren();
      ui.cOpts.className = "c-opts";
      ui.cOpts.replaceChildren(...ph.ask.options.map((o) => h("button", {
        class: "btn sm" + (/approve|yes|continue/i.test(o.id) ? " primary" : ""),
        onclick: () => reply(ph.current, ph.ask, o.id),
      }, optionLabel(o.label))));
    } else {
      ui.cRight.replaceChildren(sendButton(ph));
      ui.cOpts.className = "hidden";
      ui.cOpts.replaceChildren();
    }
    ui.tools.replaceChildren(environmentTool(), personalTool(), sandboxTool(), networkTool(), modelTool(), meterTool(m), h("span", { class: "grow" }), h("div", { class: "c-hint" }, hint(ph)));
    ui.next.replaceChildren(...nextSteps(ph));
  }
  refreshSend();
}

function sendButton(ph) {
  if (ph.mode !== "running") {
    ui.send = h("button", { class: "send", title: t("composer.sendTitle"), onclick: () => submit() }, icon("send", "sm"));
    return ui.send;
  }
  // 运行中：发送是纠偏，别的选择（立刻打断、下一个任务）在旁边的菜单里。
  ui.send = h("button", { class: "send", title: t("hint.steer"), onclick: () => steer(ph.current, "next_step") }, icon("send", "sm"));
  const toggle = h("button", { class: "btn ghost xs send-menu-toggle", title: t("composer.queueTitle"), onclick: () => openPop(toggle, (close) => sendMenu(ph, close)) }, icon("chevron", "xs"));
  return h("div", { class: "send-group" }, toggle, ui.send);
}

function sendMenu(ph, close) {
  const item = (action, ic, title, desc, run) => {
    const el = popItem(ic, title, desc, false, () => { close(); run(); });
    el.dataset.action = action;
    return el;
  };
  return h("div", { class: "send-menu" },
    item("steer", "send", t("composer.steer"), t("hint.steer"), () => steer(ph.current, "next_step")),
    item("interrupt", "stop", t("composer.interrupt"), t("hint.interrupt"), () => steer(ph.current, "interrupt")),
    item("queue", "queue", t("composer.queueNext"), t("composer.queueTitle"), () => submit()));
}

function hint(ph) {
  const k = (key, text) => h("span", {}, h("span", { class: "kbd" }, key), " ", text);
  if (ph.mode === "running") return [k("Enter", t("composer.steer")), k("Ctrl+Enter", t("composer.interrupt")), k("Esc", t("hint.stop")), k("Shift+Enter", t("hint.newline"))];
  if (ph.mode === "waiting") return [];
  return [k("Enter", t("hint.send")), k("Shift+Enter", t("hint.newline"))];
}

const OPTION_LABELS = {
  approve: "opt.approve", reject: "opt.reject", continue_as_is: "opt.continue", replan_with_guidance: "opt.replanGuided", stop_and_wrap_up: "opt.wrapUp",
  continue: "opt.continue", replan: "opt.replan", stop: "opt.wrapUp",
};

function optionLabel(label) {
  return OPTION_LABELS[label] ? t(OPTION_LABELS[label]) : label;
}

function nextSteps(ph) {
  const chip = (ic, label, onclick, primary) => h("button", { class: "next-step" + (primary ? " primary" : ""), onclick }, icon(ic, "xs"), label);
  if (ph.mode === "done") {
    const n = state.changes ? state.changes.files.length : 0;
    const p = state.project;
    const s = state.session || {};
    const out = [];
    if (n) out.push(chip("diff", t("next.review", { n }), () => openDrawer("changes"), true));
    if (n && p.repo && connection(p.git_provider)) {
      const rn = requestName(p);
      out.push(h("button", { class: "next-step", onclick: () => openDrawer("changes", { focus: "publish" }) }, badge(p.git_provider, "xs"),
        s.pr_number ? t("publish.pushTo", { rn, n: s.pr_number }) : t("publish.pushDraft", { rn })));
    }
    else if (n) out.push(chip("commit", t("next.commit"), () => openDrawer("changes", { focus: "commit" })));
    out.push(chip("edit", t("next.refine"), () => setComposer(t("next.refinePrompt"))));
    out.push(chip("fileText", t("next.explain"), () => setComposer(t("next.explainPrompt"))));
    return out;
  }
  if (ph.mode === "failed") {
    const last = ph.last;
    return [
      chip("refresh", t("next.retry"), () => submit(last.message), true),
      chip("edit", t("next.edit"), () => setComposer(last.message)),
      chip("list", t("next.where"), () => { state.open.add(last.id); state.closed.delete(last.id); renderTurn(last.id); scrollToTurn(last.id); }),
    ];
  }
  return [];
}

function setSetting(key, value) {
  state.settings[key] = value;
  ls.set("cw_" + key, value);
  updateComposer(true);
  renderStatus();
}

function sandboxDesc(s) {
  if (s.id !== "write_reversible") return s.desc;
  const mode = state.env && state.env.sandbox && state.env.sandbox.mode;
  return mode === "host" ? t("sandbox.hostDesc") : s.desc;
}

function sandboxTool() {
  const cur = SANDBOX.find((s) => s.id === state.settings.sandbox) || SANDBOX[1];
  const can = state.config.sandbox_control;
  return h("button", {
    class: "tool" + (cur.id === "read" ? " read-only" : ""),
    disabled: !can,
    title: can ? t("sandbox.title") : t("sandbox.fixed"),
    onclick: (e) => openPop(e.currentTarget, (close) => [
      h("div", { class: "pop-title" }, t("sandbox.heading")),
      ...SANDBOX.map((s) => popItem(s.icon, s.label, sandboxDesc(s), state.settings.sandbox === s.id, () => { setSetting("sandbox", s.id); close(); }, s.disabled)),
    ]),
  }, icon(cur.icon, "sm"), h("span", { class: "v" }, cur.label), icon("chevDown", "caret"));
}

// A network choice authorizes this Task only; environments never grant access.
function networkTool() {
  const label = state.networkRequest ? (state.networkRequest.scope === "public" ? (t("network.public")) : (t("network.hosts"))) : (t("network.off"));
  return h("button", { class: "tool", title: t("network.next"), onclick: async (e) => {
    const anchor = e.currentTarget;
    try {
      const policy = await api("GET", "/api/network-policy");
      openPop(anchor, (close) => {
        const choose = (request) => { state.networkRequest = request; state.networkRequestTenant = state.me?.tenant; close(); updateComposer(true); };
        const out = [h("div", { class: "pop-title" }, t("network.title")),
          popItem("lock", t("network.off"), "", !state.networkRequest, () => choose(null))];
        if (!policy.allowed) { out.push(h("p", { class: "muted" }, t("network.disabled"))); return out; }
        const modes = policy.modes || ["deny", "allowlist"];
        const duration = h("select", { "aria-label": t("network.duration") }, ...[15, 30, 60].filter((m) => m <= policy.max_grant_minutes).map((m) => h("option", { value: m }, t("network.minutes", { minutes: m }))));
        out.push(h("label", { class: "lbl" }, t("network.duration"), duration));
        const request = (scope, hosts) => ({ scope, hosts, minutes: Number(duration.value), reason: t("network.reason") });
        if (modes.includes("allowlist")) {
          const hosts = h("input", { type: "text", value: (policy.hosts || []).join(", "), "aria-label": t("network.hostInput"), placeholder: "api.example.com" });
          out.push(h("label", { class: "lbl" }, t("network.hostsLabel"), hosts),
            popItem("globe", t("network.hosts"), "", state.networkRequest?.scope === "hosts", () => choose(request("hosts", hosts.value.split(/[,，\s]+/).filter(Boolean)))));
        }
        if (modes.includes("open")) out.push(popItem("globe", t("network.public"), t("network.publicHint"), state.networkRequest?.scope === "public", () => choose(request("public", []))));
        return out;
      });
    } catch (ex) { toast(ex.message, true); }
  } }, icon("globe", "sm"), h("span", { class: "v" }, label), icon("chevDown", "caret"));
}

function modelTool() {
  const cur = MODELS.find((m) => m.id === state.settings.model) || MODELS[1];
  return h("button", {
    class: "tool",
    title: t("model.title"),
    onclick: (e) => openPop(e.currentTarget, (close) => [
      h("div", { class: "pop-title" }, t("model.heading")),
      ...MODELS.map((m) => popItem(m.icon, m.label, m.desc, state.settings.model === m.id, () => { setSetting("model", m.id); close(); })),
    ]),
  }, icon(cur.icon, "sm"), h("span", { class: "v" }, cur.label), icon("chevDown", "caret"));
}

function meter() {
  let cost = 0, input = 0, output = 0, cacheRead = 0, hasUsage = false;
  for (const t of state.tasks) {
    cost += taskCost(t);
    const d = state.detail.get(t.id);
    const u = (d && d.usage) || t.usage;
    if (u && typeof u === "object") {
      hasUsage = true;
      input += num(pick(u, "inputTokens", "input_tokens"));
      output += num(pick(u, "outputTokens", "output_tokens"));
      cacheRead += num(pick(u, "cacheReadTokens", "cache_read_tokens"));
    }
  }
  const cur = currentTask();
  return { cost, input, output, cacheRead, hasUsage, count: state.tasks.length, current: cur ? taskCost(cur) : null };
}

function meterText(m) {
  return m.hasUsage ? t("meter.text", { tokens: fmtTokens(m.input + m.output), cost: fmtUSD(m.cost) }) : fmtUSD(m.cost);
}

function meterTool(m) {
  return h("button", {
    class: "tool",
    title: t("meter.title"),
    onclick: (e) => openPop(e.currentTarget, () => {
      const mm = meter();
      const row = (k, v) => [h("span", { class: "k" }, k), h("span", { class: "v" }, v)];
      return [
        h("div", { class: "pop-title" }, t("meter.title")),
        h("div", { class: "meter-grid" },
          row(t("meter.tasks"), String(mm.count)),
          row(t("meter.cost"), fmtUSD(mm.cost)),
          mm.current !== null ? row(t("meter.current"), fmtUSD(mm.current)) : null,
          mm.hasUsage ? [row(t("meter.input"), fmtTokens(mm.input)), row(t("meter.output"), fmtTokens(mm.output)), row(t("meter.cache"), fmtTokens(mm.cacheRead))] : null),
      ];
    }),
  }, icon("gauge", "sm"), h("span", { class: "v keep" }, meterText(m)));
}

// submit 发出一条任务。草稿时先开会话（复制工作副本，标题取这条消息的第一行），再提交。
async function submit(text) {
  const message = (text ?? (ui.input ? ui.input.value : "")).trim();
  if (!message || state.sending || !state.project) return;
  state.sending = true;
  refreshSend();
  // 消息先出现在对话里，下面说明在做什么：草稿要先开会话（复制工作副本，借沙箱），再提交。界面不等请求回来。
  state.pending = { message, phase: state.session ? "submit" : "session", started: new Date().toISOString(), project: state.project.id, session: state.session ? state.session.id : null };
  if (text === undefined && ui.input) { ui.input.value = ""; autosize(); }
  renderThread(true);
  try {
    if (!state.session) {
      const p = state.project;
      const sess = await api("POST", `/api/projects/${p.id}/sessions`, { title: message, environment_id: draftEnvironment() || undefined, personal_config: personalSelection() });
      state.sessions.unshift(sess);
      state.session = sess;
      state.pending = { ...state.pending, phase: "submit", session: sess.id };
      setHash(`#/w/${p.id}/s/${sess.id}`);
      await loadWorkspaceRepos();
      renderApp();
      loadEnv();
    }
    const base = sbase();
    const selection = personalSelection();
    const t = await api("POST", `${base}/tasks`, { message, sandbox: state.settings.sandbox, model: state.settings.model, personal_config: selection, network_request: state.networkRequestTenant === state.me?.tenant ? state.networkRequest || undefined : undefined });
    if (selection) state.session.personal_config = selection;
    state.networkRequest = null;
    if (sbase() !== base) return;
    state.pending = null;
    state.tasks.push(t);
    // 这个会话成了最近活动的：排到最前。
    const touched = { ...state.session, updated_at: t.created_at };
    state.sessions = [touched, ...state.sessions.filter((x) => !(x.project_id === touched.project_id && x.id === touched.id))];
    state.session = touched;
    renderThread(true);
    renderTopbar();
    loadRun(t);
    loadActive();
    scheduleChrome();
  } catch (ex) {
    state.pending = null;
    renderThread();
    report(ex);
    if (text === undefined && ui.input && !ui.input.value) setComposer(message);
  } finally {
    state.pending = null;
    state.sending = false;
    refreshSend();
    if (ui.input) ui.input.focus();
  }
}

async function reply(task, ask, option) {
  try {
    await api("POST", `${sbase()}/tasks/${task.id}/reply`, { question_id: ask.qid, option, guidance: ui.input.value.trim() });
    state.replied.add(ask.qid);
    ui.input.value = "";
    autosize();
    renderTurn(task.id);
    updateComposer(true);
    toast(t("ask.sent"));
  } catch (ex) { report(ex); }
}

function setComposer(text) {
  if (!ui.input) return;
  ui.input.value = text;
  autosize();
  refreshSend();
  ui.input.focus();
  ui.input.setSelectionRange(text.length, text.length);
}

// ---------------------------------------------------------------- 右栏

function renderStatus() {
  if (!ui.statusBody || !inSession() || !state.statusOpen) return;
  // 右栏随事件整块重画：保住滚动位置，不再每来一个事件就跳回顶上。
  keepScroll(ui.statusBody, () => ui.statusBody.replaceChildren(
    section("env", "server", t("sp.env"), envSummary(), envBody),
    section("context", "layers", t("sp.context"), contextSummary(), contextBody),
    section("output", "fileText", t("sp.output"), outputSummary(), outputBody),
    section("changes", "diff", t("sp.changes"), changesSummary(), changesBody),
    section("background", "activity", t("sp.background"), backgroundSummary(), backgroundBody),
  ));
}

function section(key, ic, title, summary, body) {
  const open = state.sections.has(key);
  return h("section", { class: "sp" + (open ? " open" : "") },
    h("button", { class: "sp-head", onclick: () => {
      if (state.sections.has(key)) state.sections.delete(key); else state.sections.add(key);
      ls.set("cw_sections", [...state.sections].join(","));
      renderStatus();
    } }, icon(ic, "sm"), h("span", { class: "t" }, title), h("span", { class: "sum" }, summary), icon("chevron", "xs chev")),
    open ? h("div", { class: "sp-body" }, body()) : null,
  );
}

function kv(rows) {
  return h("div", { class: "kv" }, rows.filter(Boolean).map(([k, v]) => [h("div", { class: "k" }, k), h("div", { class: "v" }, v)]));
}

function envSummary() {
  const e = state.env;
  if (!e) return h("span", { class: "spinner" });
  return e.runtime && e.runtime.ok ? [h("span", { class: "dot ok" }), t("env.connected")] : [h("span", { class: "dot err" }), t("env.down")];
}

function envBody() {
  const e = state.env;
  if (!e) return h("div", { class: "empty-line" }, t("common.loading"));
  const p = gitProject();
  const s = gitSessionState();
  const sb = e.sandbox || {};
  // 分支、HEAD 与改动取自“改动”（/env 不调 Workspace API：那要借沙箱环境）。
  const c = state.changes || {};
  const dirty = c.files ? c.files.length : null;
  const se = e.session || {};
  const level = SANDBOX.find((x) => x.id === state.settings.sandbox) || SANDBOX[1];
  const sandboxText = sb.mode === "docker" ? t("env.docker", { image: sb.image || t("env.defaultImage") }) : sb.mode === "host" ? t("env.host") : t("env.unknown");
  const rsid = se.runtime_session_id || "";
  return [
    kv([
      ["Runtime", [h("span", { class: "dot " + (e.runtime && e.runtime.ok ? "ok" : "err") }), e.runtime && e.runtime.ok ? t("env.taskOk") : t("env.taskDown")]],
      [t("env.config"), envConfigValue(e.environment)],
      [t("env.sandbox"), sandboxText],
      [t("env.permission"), [h("span", { class: "chip" + (level.id === "read" ? " info" : "") }, icon(level.icon, "xs"), level.label), h("span", { class: "muted small" }, t("env.nextTask"))]],
      [t("env.copy"), se.own_copy ? [h("span", {}, t("env.ownCopy")), h("span", { class: "mono small muted", title: se.workspace_id }, se.workspace_id)] : t("env.sharedCopy")],
      [t("env.branch"), c.branch ? [h("span", { class: "chip mono" }, icon("branch", "xs"), c.branch), c.head ? h("span", { class: "mono small muted" }, c.head.slice(0, 7)) : null] : "—"],
      [t("sp.changes"), dirty === null ? "—" : dirty ? t("env.dirty", { n: dirty }) : t("env.clean")],
      [t("env.workspace"), [h("span", {}, p.name), p.repo ? null : h("span", { class: "muted small" }, sourceLabel(p))]],
      p.repo ? [t("env.repo"), [badge(p.git_provider, "xs"), h("a", { href: repoURL(p), target: "_blank", rel: "noopener" }, p.repo),
        connection(p.git_provider) ? null : h("span", { class: "muted small" }, t("env.notConnected", { name: providerInfo(p.git_provider).name }))]] : null,
      rsid ? ["Session", h("span", { class: "mono small", title: rsid }, rsid.slice(0, 16) + "…")] : null,
    ]),
    runtimeEnvBlock(e.runtime_environments),
    h("div", { class: "sp-actions" },
      h("button", { class: "btn xs", onclick: () => openDrawer("files") }, icon("folder", "xs"), t("env.browse")),
      s.branch ? h("button", { class: "btn xs", title: t("env.resetTitle", { base: p.base_branch }), onclick: resetBranch }, icon("refresh", "xs"), t("env.reset", { base: p.base_branch })) : null,
      canSync(p, s) ? h("button", { class: "btn xs", "data-action": "sync", title: t("sync.title", { base: p.base_branch }), onclick: syncSession }, icon("download", "xs"), t("sync.btn", { base: p.base_branch })) : null,
      h("button", { class: "btn ghost xs", title: t("common.reload"), onclick: loadEnv }, icon("refresh", "xs"))),
  ];
}

// 拉取基线分支的最新提交（Workspace API 的 sync）：只给有远端、还没推送到自己分支的会话；推送过的用“回到基线分支”。
function canSync(p, s) {
  return !!(p && s && !s.branch && p.base_branch && String(p.source || "").startsWith("https://"));
}

async function syncSession() {
  const p = gitProject();
  const base = sbase();
  toast(t("sync.running", { base: p.base_branch }));
  try {
    const r = await api("POST", gitURL("sync"));
    setSession(r.session);
    if (sbase() !== base) return;
    toast(t("sync.done", { base: p.base_branch, head: String(r.head || "").slice(0, 7) }));
    loadChanges();
    loadEnv();
    if (state.drawer) renderDrawer();
  } catch (ex) { report(ex); }
}

// envConfigValue 是会话选定的环境配置：名字（删掉的标出来）与还没交给 Runtime 的提示。
function envConfigValue(cfg) {
  if (!cfg) return h("span", { class: "muted" }, t("env.configDefault"));
  const env = findEnvironment(cfg.id);
  const name = (env && env.name) || cfg.name || t("envcfg.missing");
  return [
    env ? h("button", { class: "link-btn", onclick: () => openEnvironmentEditor(env) }, name) : h("span", {}, name),
    cfg.deleted ? h("div", { class: "muted small env-note", "data-env-config-deleted": "" }, t("env.configDeletedNote")) : null,
    !cfg.deleted && cfg.changed_since ? h("div", { class: "muted small env-note" }, t("env.configChanged")) : null,
    cfg.handoff === "pending" ? h("span", { class: "chip warn", title: t("envcfg.pendingNote") }, t("env.handoffPending")) : null,
  ];
}

// envStateChip：就绪只看 Runtime 给的 ready（ledger 为 READY、健康已确认、两个期限都没到）；READY 而 ready 为假是“未确认就绪”。
function envStateChip(r) {
  if (r.ready) return h("span", { class: "chip ok" }, h("span", { class: "dot ok" }), t("env.state.ready"));
  if (r.state === "ready") return h("span", { class: "chip warn", title: t("env.unconfirmedTitle") }, t("env.state.unconfirmed"));
  const kind = { creating: "run", destroying: "", destroyed: "", failed: "err", lost: "err" }[r.state];
  const key = ["creating", "destroying", "destroyed", "failed", "lost"].includes(r.state) ? r.state : "unknown";
  return h("span", { class: "chip" + (kind ? " " + kind : "") }, t("env.state." + key));
}

// runtimeEnvBlock 是 Runtime 记下的沙箱环境（EnvironmentService 的只读投影）：最新的一条详细列出，更早的收起来。
// 没有的时间写“未知”，不按最近活动或别的时间去推。
function runtimeEnvBlock(re) {
  const head = h("div", { class: "sp-sub" }, t("env.runtimeEnv"));
  if (!re) return [head, h("div", { class: "empty-line" }, t("env.statusUnknown", { reason: "—" }))];
  const reasonText = (code) => { const k = "env.reason." + code; const v = t(k); return v === k ? code : v; };
  if (re.status === "blocked_external") {
    return [head, h("div", { class: "env-status", "data-env-status": re.status },
      h("span", { class: "chip err mono" }, "blocked_external"), h("span", { class: "muted small" }, t("env.statusBlocked"), " · ", reasonText(re.reason || "unreachable")))];
  }
  if (re.status !== "ok") {
    return [head, h("div", { class: "env-status", "data-env-status": re.status },
      h("span", { class: "chip mono" }, "unknown"),
      h("span", { class: "muted small" }, re.reason === "no_runtime_session" ? t("env.noRuntimeSession") : t("env.statusUnknown", { reason: reasonText(re.reason || "—") })))];
  }
  const records = re.records || [];
  if (!records.length) return [head, h("div", { class: "empty-line", "data-env-status": "ok" }, t("env.noRecords"))];
  const [cur, ...older] = records;
  const when = (iso) => (iso ? fmtTime(iso) : h("span", { class: "muted" }, t("env.unknownValue")));
  const backend = [cur.backend, cur.isolation, cur.runtime].filter(Boolean).join(" · ");
  return [
    head,
    h("div", { class: "env-record", "data-env-status": "ok", "data-env-ref": cur.env_ref },
      kv([
        [t("env.state"), [envStateChip(cur), h("span", { class: "mono small muted", title: cur.env_ref }, cur.env_ref)]],
        [t("env.reused"), cur.reused ? t("env.reusedYes", { at: fmtTime(cur.reused_at) || "—" }) : t("env.reusedNo")],
        [t("env.healthAt"), when(cur.health_confirmed_at)],
        [t("env.expires"), when(cur.expires_at)],
        [t("env.maxUntil"), when(cur.max_until)],
        cur.destroyed_at ? [t("env.destroyedAt"), fmtTime(cur.destroyed_at)] : null,
        backend ? [t("env.backend"), h("span", { class: "small" }, backend)] : null,
      ])),
    older.length ? toggled("env-older", state.envOlderOpen, (v) => { state.envOlderOpen = v; }, t("env.earlier", { n: older.length }),
      h("div", { class: "env-older" }, older.map((r) => h("div", { class: "env-older-row" }, envStateChip(r),
        h("span", { class: "mono small muted ellipsis grow", title: r.env_ref }, r.env_ref), h("span", { class: "muted small" }, fmtTime(r.created_at)))))) : null,
    re.truncated ? h("div", { class: "muted small" }, t("env.truncated")) : null,
  ];
}

function contextSummary() {
  const task = focusTask();
  if (!task) return "—";
  const d = state.detail.get(task.id);
  const rounds = d && d.state && Array.isArray(d.state.rounds) ? d.state.rounds.length : task.rounds || 0;
  const cost = taskCost(task);
  const usage = latestUsage(task);
  return [usage ? ctxPercent(num(usage.total_tokens), ctxUsable(usage)) : null, rounds ? t("rounds", { n: rounds }) : null, cost ? fmtUSD(cost) : null].filter(Boolean).join(" · ") || "—";
}

// 上下文占比（Runtime 的 docs/plan/running-task-interaction.md 第 3 节，参考 Claude Code 的 /context）：这次模型调用的上下文里各类内容占多少。
const CTX_CATEGORIES = ["system_prompt", "system_tools", "mcp_tools", "memory", "task_brief", "messages"];

// 这个任务最近一次模型调用的占比；它还没调用过模型（排队中、刚开始）时看上一个任务的。
function latestUsage(task) {
  const at = state.tasks.indexOf(task);
  for (let k = at; k >= 0; k--) {
    const events = state.runs.get(state.tasks[k].id)?.events || [];
    for (let i = events.length - 1; i >= 0; i--) if (events[i].type === "context_usage") return events[i].value;
  }
  return null;
}

function ctxTokens(n) {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n);
}

function ctxPercent(tokens, total) {
  return `${(total > 0 ? (tokens / total) * 100 : 0).toFixed(1)}%`;
}

// 可用长度：超过它 Runtime 就开始压缩（压缩线），不超过模型的窗口。条形图以它为满格（Runtime 的 docs/plan/feedback-2026-11-01.md N-17）：
// 模型窗口常有 100 万 token，按窗口画的话几万 token 的上下文只占一条细线，看不出多少。总数是这次实际计费的输入 token，各类按估算的比例分摊。
function ctxUsable(usage) {
  const windowTokens = num(usage.window_tokens);
  const line = num(usage.effective_tokens);
  if (windowTokens > 0 && line > 0) return Math.min(windowTokens, line);
  return windowTokens || line;
}

function ctxUsage(usage) {
  const windowTokens = num(usage.window_tokens);
  const usable = ctxUsable(usage);
  const total = num(usage.total_tokens);
  const full = Math.max(usable, total);
  const byName = new Map((usage.categories || []).map((c) => [c.name, num(c.tokens)]));
  const rows = [...CTX_CATEGORIES.map((name) => [name, byName.get(name) || 0, t(`ctx.cat.${name}`)]), ["free", Math.max(0, usable - total), t("ctx.free")]];
  const tools = usage.tools || [];
  const segment = (name, tokens) => {
    const seg = h("span", { class: "seg", "data-cat": name, title: `${t(name === "free" ? "ctx.free" : `ctx.cat.${name}`)} ${ctxTokens(tokens)}` });
    seg.style.width = ctxPercent(tokens, full);
    // 很小的一类也看得见（至少 2px），占比的数字照实写在下面。
    if (tokens > 0 && name !== "free") seg.style.minWidth = "2px";
    return seg;
  };
  return h("div", { class: "ctx-usage" },
    h("div", { class: "ctx-head" },
      h("span", { class: "ctx-used" }, t("ctx.used", { used: ctxTokens(total), usable: ctxTokens(usable) })),
      h("span", { class: "ctx-pct-total" }, ctxPercent(total, usable))),
    h("div", { class: "ctx-bar", role: "img", "aria-label": t("ctx.used", { used: ctxTokens(total), usable: ctxTokens(usable) }) },
      rows.map(([name, tokens]) => segment(name, tokens)),
      total > usable ? (() => {
        const mark = h("span", { class: "ctx-mark" });
        mark.style.left = ctxPercent(usable, full);
        return mark;
      })() : null),
    h("div", { class: "ctx-rows" }, rows.map(([name, tokens, label]) => h("div", { class: "ctx-row", "data-cat": name },
      h("span", { class: "ctx-dot", "data-cat": name }),
      h("span", { class: "ctx-label" }, label),
      h("span", { class: "ctx-num" }, ctxTokens(tokens)),
      h("span", { class: "ctx-pct" }, ctxPercent(tokens, usable))))),
    h("div", { class: "ctx-line muted small" },
      windowTokens > usable ? t("ctx.window", { window: ctxTokens(windowTokens), usable: ctxTokens(usable) }) : t("ctx.compactionLine", { n: ctxTokens(usable) }),
      (usage.applied || []).length ? ` · ${t("ctx.applied", { steps: usage.applied.join(t("list.sep")) })}` : ""),
    tools.length ? toggled("ctx-tools", state.ctxToolsOpen, (v) => { state.ctxToolsOpen = v; }, t("ctx.tools"),
      tools.map((x) => h("div", { class: "ctx-tool" }, h("span", { class: "mono small" }, x.name), x.mcp ? h("span", { class: "chip" }, "MCP") : null, h("span", { class: "grow" }), h("span", { class: "ctx-num" }, ctxTokens(num(x.tokens)))))) : null,
  );
}

// details 重画时保持展开。
function toggled(cls, open, remember, title, body) {
  const el = h("details", { class: cls, open: open || null }, h("summary", {}, title), body);
  el.addEventListener("toggle", () => remember(el.open));
  return el;
}

function contextBody() {
  const task = focusTask();
  if (!task) return h("div", { class: "empty-line" }, t("ctx.empty"));
  const usage = latestUsage(task);
  return [
    usage ? ctxUsage(usage) : h("div", { class: "empty-line" }, t("ctx.noUsage")),
    toggled("task-details", state.detailsOpen, (v) => { state.detailsOpen = v; }, t("ctx.details"), taskDetails(task)),
  ];
}

function taskDetails(task) {
  const d = state.detail.get(task.id);
  const s = d && d.state;
  if (!s) return [h("div", { class: "goal" }, task.message), h("div", { class: "empty-line" }, t("ctx.loading"))];
  const c = s.constraints || {};
  const chips = [];
  if (c.effect_ceiling) {
    const lv = SANDBOX.find((x) => x.id === c.effect_ceiling);
    chips.push(h("span", { class: "chip" + (c.effect_ceiling === "read" ? " info" : "") }, lv ? lv.label : c.effect_ceiling));
  }
  if (c.budget_usd) chips.push(h("span", { class: "chip" }, t("ctx.budget", { cost: fmtUSD(num(c.budget_usd)) })));
  if (c.model_preference) chips.push(h("span", { class: "chip" }, t("ctx.model", { label: modelLabel(c.model_preference) })));
  for (const [k, v] of Object.entries(c)) {
    if (!["effect_ceiling", "budget_usd", "model_preference"].includes(k)) chips.push(h("span", { class: "chip mono" }, `${k}: ${typeof v === "object" ? JSON.stringify(v) : v}`));
  }
  const list = (title, items, max = 6) => {
    if (!Array.isArray(items) || !items.length) return null;
    const shown = items.slice(-max);
    return [h("div", { class: "sp-sub" }, `${title} · ${items.length}`), h("ul", { class: "sp-list" }, shown.map((x) => h("li", {}, String(x)))),
      items.length > max ? h("div", { class: "empty-line" }, t("ctx.older", { n: items.length - max })) : null];
  };
  const rounds = Array.isArray(s.rounds) ? s.rounds : [];
  // stage_goal 是 Runtime 写的文字；翻译要用的代码在同一轮的 stage_started 事件里。
  const stages = (state.runs.get(task.id)?.events || []).filter((e) => e.type === "stage_started");
  const roundGoal = (r) => {
    const stage = stages.find((e) => pick(e.value, "round_seq") === r.seq);
    return (stage && goalText(stage.value)) || r.stage_goal || "—";
  };
  return [
    h("div", { class: "goal" }, s.goal || task.message),
    h("div", { class: "sp-sub" }, t("ctx.doneWhen")),
    h("div", { class: "muted done-when" }, doneWhenText(s) || "—"),
    chips.length ? [h("div", { class: "sp-sub" }, t("ctx.constraints")), h("div", { class: "chip-row" }, chips)] : null,
    list(t("ctx.milestones"), s.milestones),
    list(t("ctx.facts"), s.facts),
    list(t("ctx.assumptions"), s.assumptions),
    list(t("ctx.questions"), s.open_questions),
    rounds.length ? [h("div", { class: "sp-sub" }, t("ctx.rounds")), h("div", { class: "rounds" }, rounds.map((r) => h("div", { class: "round-row" },
      h("span", { class: "n" }, r.seq), h("span", { class: "g ellipsis", title: roundGoal(r) }, roundGoal(r)),
      r.verdict ? verdictChip(r.verdict) : r.status && r.status !== "completed" ? h("span", { class: "spinner" }) : null,
      r.cost_usd ? h("span", { class: "c" }, fmtUSD(num(r.cost_usd))) : null)))] : null,
  ];
}

// 各轮的重要输出（Runtime 的 docs/plan/running-task-interaction.md 第 4 节）：按任务、Round 分组的重要文件与重要答案；全文在对话里。
function outputRounds() {
  const out = [];
  for (const task of state.tasks) {
    const s = state.detail.get(task.id)?.state;
    for (const r of (s && Array.isArray(s.rounds) ? s.rounds : [])) {
      const hl = r.highlights;
      if (hl && ((hl.files || []).length || (hl.answers || []).length)) out.push({ task, seq: r.seq, files: hl.files || [], answers: hl.answers || [] });
    }
  }
  return out;
}

// 已结束的任务的 Task State 平时不取；右栏要汇总时才取。
const detailAsked = new Set();
function askDetails() {
  for (const task of state.tasks) {
    const key = `${sbase()}/${task.id}`;
    if (!state.detail.has(task.id) && !detailAsked.has(key)) {
      detailAsked.add(key);
      loadDetail(task);
    }
  }
}

function outputSummary() {
  const n = outputRounds().length;
  return n ? t("rounds", { n }) : "—";
}

function outputBody() {
  askDetails();
  const rounds = outputRounds();
  if (!rounds.length) return h("div", { class: "empty-line" }, t("output.empty"));
  return rounds.map((r) => h("div", { class: "out-round", "data-task": r.task.id, "data-round": r.seq },
    h("div", { class: "out-round-h" },
      h("span", { class: "grow ellipsis", title: r.task.message }, t("output.round", { n: r.seq }), " · ", r.task.message),
      h("button", { class: "btn ghost xs out-goto", title: t("output.inThread"), onclick: () => scrollToTurn(r.task.id) }, icon("chevron", "xs"))),
    r.files.length ? [h("div", { class: "sp-sub" }, t("output.files")), r.files.map((f) => h("div", { class: "out-file" },
      h("button", { class: "mono small out-path", title: t("env.browse"), onclick: () => { state.filesPath = f.path.includes("/") ? f.path.slice(0, f.path.lastIndexOf("/")) : ""; openDrawer("files"); } }, f.path),
      f.why ? h("div", { class: "out-why muted small" }, f.why) : null))] : null,
    r.answers.length ? [h("div", { class: "sp-sub" }, t("output.answers")), r.answers.map((a) => h("div", { class: "out-answer" }, h("div", { class: "out-text" }, a)))] : null,
  ));
}

function changeStats() {
  const files = state.changes ? state.changes.files : [];
  return { files, added: files.reduce((n, f) => n + f.added, 0), deleted: files.reduce((n, f) => n + f.deleted, 0) };
}

function changesSummary() {
  if (!state.changes) return h("span", { class: "spinner" });
  const { files, added, deleted } = changeStats();
  if (!files.length) return t("env.clean");
  return [h("span", { class: "stat-add" }, `+${added}`), h("span", { class: "stat-del" }, `−${deleted}`), t("files.count", { n: files.length })];
}

function changesBody() {
  if (!state.changes) return h("div", { class: "empty-line" }, t("common.loading"));
  const { files } = changeStats();
  const p = gitProject();
  const s = gitSessionState();
  const max = 12;
  return [
    files.length
      ? h("div", {}, files.slice(0, max).map((f) => h("button", { class: "file-row", title: f.path, onclick: () => openDrawer("changes", { focus: f.path }) },
        icon("file", "xs"), h("span", { class: "p ellipsis" }, f.path),
        f.binary ? h("span", { class: "muted small" }, t("changes.binary")) : [h("span", { class: "stat-add" }, `+${f.added}`), h("span", { class: "stat-del" }, `−${f.deleted}`)])),
      files.length > max ? h("div", { class: "empty-line" }, t("changes.more", { n: files.length - max })) : null)
      : h("div", { class: "empty-line" }, t("changes.none")),
    h("div", { class: "sp-actions" },
      files.length ? h("button", { class: "btn primary xs", onclick: () => openDrawer("changes") }, icon("diff", "xs"), t("changes.reviewCommit")) : null,
      files.length && canWriteRepo() && p.repo && connection(p.git_provider) ? h("button", { class: "btn xs", onclick: () => openDrawer("changes", { focus: "publish" }) },
        badge(p.git_provider, "xs"), s.pr_number ? t("publish.pushTo", { rn: requestName(p), n: s.pr_number }) : t("publish.pushOpen", { rn: requestName(p) })) : null,
      files.length ? h("a", { class: "btn xs", href: gitURL("patch"), download: `${s.workspace_id}.patch` }, icon("download", "xs"), t("changes.patch")) : null,
      h("button", { class: "btn ghost xs", onclick: () => openDrawer("history") }, icon("history", "xs"), t("drawer.history"))),
  ];
}

function backgroundSummary() {
  const n = state.active.length;
  return n ? [h("span", { class: "spinner" }), t("bg.count", { n })] : t("bg.idle");
}

function backgroundBody() {
  if (!state.active.length) return h("div", { class: "empty-line" }, t("activity.empty"));
  return h("div", {}, state.active.map((a) => h("div", { class: "bg-row", title: t("bg.open"), onclick: () => jumpTo(a.project_id, a.session_id, a.task.id) },
    a.task.status === "TASK_STATUS_RUNNING" ? h("span", { class: "spinner" }) : icon("clock", "sm"),
    h("div", { class: "grow" }, h("div", { class: "t" }, a.task.message),
      h("div", { class: "m" }, `${a.project_name} / ${a.session_title || t("session.untitled")} · ${(STATUS[a.task.status] || [a.task.status])[0]} · ${fmtAgo(a.task.created_at)}`)),
    h("button", { class: "btn ghost xs icon-only", title: t("common.stop"), onclick: (e) => { e.stopPropagation(); cancelTask(a.task.id, sessionURL(a.project_id, a.session_id)); } }, icon("x", "xs")),
  )));
}

async function jumpTo(projectId, sessionId, taskId) {
  if (!inSession() || state.project.id !== projectId || state.session.id !== sessionId) await openSession(projectId, sessionId);
  requestAnimationFrame(() => scrollToTurn(taskId));
}

// ---------------------------------------------------------------- 详情抽屉

function openDrawer(tab, opts = {}) {
  if (!sbase()) return;
  closeDrawer();
  closePop();
  state.drawer = { tab, focus: opts.focus || null };
  const scrim = h("div", { class: "scrim", onclick: closeDrawer });
  const el = h("div", { class: "drawer", role: "dialog", "aria-label": t("drawer.title") });
  ui.drawer = { scrim, el };
  document.body.append(scrim, el);
  renderDrawer();
}

function closeDrawer() {
  if (!ui.drawer) return;
  ui.drawer.scrim.remove();
  ui.drawer.el.remove();
  ui.drawer = null;
  state.drawer = null;
}

function renderDrawer() {
  const d = state.drawer;
  if (!d || !ui.drawer) return;
  const n = state.changes ? state.changes.files.length : 0;
  const tab = (key, ic, label, extra) => h("button", { class: "dtab" + (d.tab === key ? " active" : ""), onclick: () => { d.tab = key; d.focus = null; renderDrawer(); } }, icon(ic, "sm"), label, extra);
  const body = h("div", { class: "drawer-body" });
  ui.drawer.el.replaceChildren(
    h("div", { class: "drawer-head" },
      h("span", { class: "h ellipsis", title: state.project.name }, state.session.title || t("session.untitled")),
      tab("changes", "diff", t("sp.changes"), n ? h("span", { class: "n" }, n) : null),
      tab("files", "folder", t("drawer.files")),
      tab("history", "history", t("drawer.history")),
      h("span", { class: "grow" }),
      h("button", { class: "btn ghost sm icon-only", title: t("drawer.close"), onclick: closeDrawer }, icon("x", "sm"))),
    body,
  );
  if (d.tab === "changes") drawChanges(body, d.focus);
  else if (d.tab === "files") loadFiles(body, state.filesPath);
  else loadHistory(body);
}

async function drawChanges(body, focus) {
  body.replaceChildren(h("div", { class: "row muted" }, h("span", { class: "spinner" }), t("changes.loading")));
  let data;
  const requestURL = gitURL("changes");
  try {
    if (isMulti() && !selectedRepo()) { body.replaceChildren(h("div", { class: "muted" }, t("repos.empty"))); return; }
    data = await api("GET", requestURL);
    if (requestURL !== gitURL("changes")) return;
    state.changes = data;
    scheduleChrome();
  } catch (ex) {
    body.replaceChildren(h("div", { class: "error-box" }, ex.message));
    return;
  }
  const busy = !!currentTask();
  const parts = [];
  if (!canWriteRepo()) parts.push(h("div", { class: "chip", "data-repo-readonly": "" }, t("repos.readOnly")));
  let commitInput = null;
  if (!data.files.length) {
    parts.push(h("div", { class: "card" },
      h("div", { class: "card-title" }, icon("check", "sm"), t("changes.cleanTitle")),
      h("div", { class: "muted" }, t("changes.branch"), h("span", { class: "mono" }, data.branch || "—"), " · HEAD ", h("span", { class: "mono" }, (data.head || "").slice(0, 10)))));
  } else {
    const added = data.files.reduce((n, f) => n + f.added, 0);
    const deleted = data.files.reduce((n, f) => n + f.deleted, 0);
    commitInput = h("input", { class: "field", placeholder: t("commit.ph"), value: lastMessage() });
    const commit = h("button", { class: "btn primary sm", "data-git-commit": "", disabled: busy || !canWriteRepo(), onclick: async () => {
      commit.disabled = true;
      try {
        const r = await api("POST", gitURL("commit"), { message: commitInput.value });
        toast(t("commit.done", { hash: r.head.slice(0, 8) }));
        await loadChanges();
        loadEnv();
        renderDrawer();
      } catch (ex) { commit.disabled = false; report(ex); }
    } }, icon("commit", "sm"), t("commit.btn"));
    parts.push(h("div", { class: "card" },
      h("div", { class: "card-title" }, t("changes.title", { n: data.files.length }), h("span", { class: "stat-add" }, `+${added}`), h("span", { class: "stat-del" }, `−${deleted}`),
        busy ? h("span", { class: "chip run" }, h("span", { class: "spinner" }), t("changes.busy")) : null),
      h("div", { class: "actions" }, h("div", { class: "grow" }, commitInput), commit,
        h("a", { class: "btn sm", href: gitURL("patch"), download: `${state.session.workspace_id}.patch` }, icon("download", "sm"), t("changes.patch")),
        h("button", { class: "btn sm danger", disabled: busy, onclick: discard }, icon("trash", "sm"), t("changes.discard"))),
    ));
  }
  const pub = publishCard(busy, data.files.length > 0);
  if (pub) parts.push(pub.card);
  parts.push(...renderDiff(data.diff));
  if (data.truncated) parts.push(h("div", { class: "muted" }, t("changes.truncated")));
  body.replaceChildren(...parts);
  if (focus === "commit" && commitInput) commitInput.focus();
  else if (focus === "publish" && pub) pub.title.focus();
  else if (focus) {
    const el = [...body.querySelectorAll(".diff-file")].find((x) => x.getAttribute("data-path") === focus);
    if (el) { el.scrollIntoView({ block: "start" }); el.classList.add("flash"); setTimeout(() => el.classList.remove("flash"), 1600); }
  }
}

async function discard() {
  if (!confirm(t("changes.discardConfirm"))) return;
  try {
    await api("POST", gitURL("discard"));
    toast(t("changes.discarded"));
    await loadChanges();
    loadEnv();
    renderDrawer();
  } catch (ex) { report(ex); }
}

function lastMessage() {
  // 默认提交说明：最近一条像样的任务描述（跳过“-”这类过短的）。
  const task = [...state.tasks].reverse().find((x) => x.message.trim().length > 3);
  return task ? task.message.trim().split("\n")[0].slice(0, 72) : "";
}

// publishCard 是“推送并开草稿 PR/MR”：一个会话一个 agent/* 分支、一个 PR（GitLab 叫 Merge Request）。
function publishCard(busy, dirty) {
  const p = gitProject();
  const s = gitSessionState();
  if (!p.repo || !canWriteRepo()) return null;
  const info = providerInfo(p.git_provider);
  const rn = requestName(p);
  if (!connection(p.git_provider)) {
    return {
      card: h("div", { class: "card" },
        h("div", { class: "card-title" }, badge(p.git_provider, "sm"), h("span", { class: "mono grow ellipsis" }, p.repo)),
        h("div", { class: "muted" }, t("publish.connectFirst", { name: info.name, rn })),
        info.configured ? h("div", { class: "actions" }, h("a", { class: "btn sm primary", href: `/api/auth/${p.git_provider}/login?link=1` }, t("account.connectTo", { name: info.name }))) : null),
      title: null,
    };
  }
  const title = h("input", { class: "field", placeholder: t("publish.titlePh", { rn }), value: s.title || lastMessage() });
  const out = h("div", {});
  const label = s.pr_number ? t("publish.pushTo", { rn, n: s.pr_number }) : t("publish.pushDraft", { rn });
  const push = h("button", { class: "btn primary sm", disabled: busy, onclick: async () => {
    push.disabled = true;
    push.lastChild.textContent = t("publish.pushing");
    try {
      const r = await api("POST", gitURL("publish"), { title: title.value });
      setSession(r.session);
      toast(r.created ? t("publish.opened", { rn, n: r.pr_number }) : t("publish.pushed", { branch: r.branch }));
      renderTopbar();
      renderSide();
      await loadChanges();
      loadEnv();
      renderDrawer();
    } catch (ex) {
      push.disabled = false;
      push.lastChild.textContent = label;
      if (ex.message !== "unauthorized") out.replaceChildren(h("div", { class: "error-box" }, ex.message));
    }
  } }, badge(p.git_provider, "sm"), h("span", {}, label));
  const card = h("div", { class: "card" },
    h("div", { class: "card-title" }, badge(p.git_provider, "sm"), h("a", { class: "mono grow ellipsis", href: repoURL(p), target: "_blank", rel: "noopener" }, p.repo),
      s.pr_url ? h("a", { class: "chip ok", href: s.pr_url, target: "_blank", rel: "noopener" }, `${rn} #${s.pr_number}`, icon("external", "xs")) : null),
    h("div", { class: "muted" }, s.branch
      ? t(dirty ? "publish.updateDirty" : "publish.update", { branch: s.branch, rn })
      : t("publish.new", { base: p.base_branch || t("publish.baseBranch"), rn })),
    h("div", { class: "actions" }, h("div", { class: "grow" }, title), push),
    out,
  );
  return { card, title };
}

function renderDiff(text) {
  const files = [];
  let cur = null;
  let oldNo = 0, newNo = 0;
  for (const line of String(text || "").split("\n")) {
    if (line.startsWith("diff --git ")) {
      const m = line.match(/ b\/(.*)$/);
      cur = { path: m ? m[1] : line, rows: [], added: 0, deleted: 0 };
      files.push(cur);
      continue;
    }
    if (!cur) continue;
    if (/^(index |--- |\+\+\+ |new file mode|deleted file mode|similarity index|rename from|rename to|old mode|new mode|Binary files)/.test(line)) continue;
    const hunk = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
    if (hunk) {
      oldNo = Number(hunk[1]); newNo = Number(hunk[2]);
      cur.rows.push(h("tr", { class: "hunk" }, h("td", { class: "ln" }), h("td", { class: "ln" }), h("td", { class: "code" }, line)));
    } else if (line.startsWith("+")) {
      cur.added++;
      cur.rows.push(h("tr", { class: "add" }, h("td", { class: "ln" }), h("td", { class: "ln" }, newNo++), h("td", { class: "code" }, line)));
    } else if (line.startsWith("-")) {
      cur.deleted++;
      cur.rows.push(h("tr", { class: "del" }, h("td", { class: "ln" }, oldNo++), h("td", { class: "ln" }), h("td", { class: "code" }, line)));
    } else if (line.startsWith(" ") || line === "") {
      if (line === "" && !cur.rows.length) continue;
      cur.rows.push(h("tr", {}, h("td", { class: "ln" }, oldNo++), h("td", { class: "ln" }, newNo++), h("td", { class: "code" }, line || " ")));
    }
  }
  return files.map((f) => {
    const box = h("div", { class: "diff-file", "data-path": f.path });
    box.append(
      h("button", { class: "diff-head", onclick: () => box.classList.toggle("collapsed") }, icon("file", "sm"), h("span", { class: "grow ellipsis" }, f.path),
        h("span", { class: "stat-add" }, `+${f.added}`), h("span", { class: "stat-del" }, `−${f.deleted}`)),
      h("div", { class: "diff-body" }, h("table", { class: "diff-table" }, h("tbody", {}, f.rows))),
    );
    return box;
  });
}

// changedPaths 是“改动”里列出的文件（文件树上标出来；改名的按新路径）。
function changedPaths() {
  const prefix = isMulti() ? selectedRepo()?.path || "" : "";
  return new Set((state.changes ? state.changes.files : []).map((f) => join(prefix, String(f.path).replace(/^.* => /, ""))));
}

async function loadFiles(body, path) {
  state.filesPath = path;
  let entries;
  try {
    entries = await api("GET", `${sbase()}/tree?path=${encodeURIComponent(path)}`);
  } catch (ex) {
    body.replaceChildren(h("div", { class: "error-box" }, ex.message));
    return;
  }
  const parts = path ? path.split("/") : [];
  const viewer = h("div", {});
  const changes = changedPaths();
  body.replaceChildren(
    h("div", { class: "card" },
      h("div", { class: "crumbs" }, h("button", { onclick: () => loadFiles(body, "") }, state.session.workspace_id),
        parts.map((p, i) => [h("span", {}, "/"), h("button", { onclick: () => loadFiles(body, parts.slice(0, i + 1).join("/")) }, p)])),
      entries.length ? h("div", {}, entries.map((e) => {
        const full = join(path, e.name);
        const changed = e.dir ? [...changes].some((c) => c.startsWith(full + "/")) : changes.has(full);
        return h("button", {
          class: "tree-item" + (changed ? " changed" : ""), "data-path": full,
          onclick: () => (e.dir ? loadFiles(body, full) : showFile(viewer, full)),
        }, icon(e.dir ? "folder" : "file", "sm"), e.name,
        changed ? h("span", { class: "chip run tree-mark", title: t("files.changedTitle") }, e.dir ? "•" : t("files.changed")) : null,
        e.dir ? null : h("span", { class: "size" }, fmtSize(e.size)));
      })) : h("div", { class: "muted" }, t("files.empty")),
    ),
    viewer,
  );
}

function join(a, b) {
  return a ? `${a}/${b}` : b;
}

async function showFile(viewer, path) {
  viewer.replaceChildren(h("div", { class: "row muted" }, h("span", { class: "spinner" }), t("common.loading")));
  try {
    const f = await api("GET", `${sbase()}/file?path=${encodeURIComponent(path)}`);
    const head = h("div", { class: "row" }, icon("file", "sm"), h("strong", { class: "mono grow ellipsis" }, path),
      changedPaths().has(path) ? h("button", { class: "btn xs", "data-action": "file-diff", onclick: () => openDrawer("changes", { focus: isMulti() ? path.slice((selectedRepo()?.path.length || 0) + 1) : path }) }, icon("diff", "xs"), t("files.viewDiff")) : null,
      h("span", { class: "muted" }, fmtSize(f.size || 0)));
    const content = f.binary ? h("div", { class: "muted" }, t("files.binary"))
      : f.too_large ? h("div", { class: "muted" }, t("files.tooLarge"))
        : h("pre", { class: "code-view" }, f.content.replace(/\n$/, "").split("\n").map((l) => h("span", {}, l || " ")));
    viewer.replaceChildren(h("div", { class: "col" }, head, content));
  } catch (ex) {
    viewer.replaceChildren(h("div", { class: "error-box" }, ex.message));
  }
}

// 提交记录：最近的 limit 个（Workspace API 的 GetLog 最多 200 个，没有游标），“显示更早的”时多取。会话开始时的提交标为“会话起点”，
// 在它之后的是这个会话自己的提交（起点不在这一页里时不标，免得猜错）。
const HISTORY_STEP = 30;
const HISTORY_MAX = 200;

async function loadHistory(body, limit = HISTORY_STEP) {
  try {
    const commits = await api("GET", gitURL("log", { limit }));
    const start = gitSessionState().base_commit || "";
    const at = start ? commits.findIndex((c) => c.hash === start) : -1;
    const rows = commits.map((c, i) => h("div", { class: "commit-row", "data-hash": c.hash },
      h("span", { class: "hash" }, c.hash.slice(0, 7)),
      h("div", { class: "grow" }, h("div", {}, c.subject), h("div", { class: "muted small" }, `${c.author} · ${fmtTime(c.date)}`)),
      i < at ? h("span", { class: "chip run", "data-own": "" }, t("history.own")) : i === at ? h("span", { class: "chip", "data-start": "" }, t("history.start")) : null,
    ));
    const more = commits.length >= limit && limit < HISTORY_MAX
      ? h("button", { class: "btn ghost sm", "data-action": "history-more", onclick: () => loadHistory(body, Math.min(limit + HISTORY_STEP, HISTORY_MAX)) }, t("history.more"))
      : commits.length >= HISTORY_MAX ? h("div", { class: "muted small" }, t("history.capped", { n: HISTORY_MAX })) : null;
    body.replaceChildren(h("div", { class: "card" }, commits.length ? rows : h("div", { class: "muted" }, t("history.none")), more));
  } catch (ex) {
    body.replaceChildren(h("div", { class: "error-box" }, ex.message));
  }
}

async function resetBranch() {
  const p = gitProject();
  const s = gitSessionState();
  if (!confirm(t("env.resetConfirm", { base: p.base_branch, branch: s.branch }))) return;
  try {
    const r = await api("POST", gitURL("reset-branch"));
    setSession(r.session);
    toast(t("env.resetDone", { base: p.base_branch }));
    renderTopbar();
    loadChanges();
    loadEnv();
  } catch (ex) { report(ex); }
}

// A selection scopes Git operations only. File browsing and agent tasks see the whole workspace.
function isMulti() { return state.project?.layout === "WORKSPACE_LAYOUT_MULTI"; }
function selectedRepo() { return state.repos.find((repo) => repo.name === state.repoName); }
function canWriteRepo() { return !isMulti() || selectedRepo()?.access === "REPO_ACCESS_WRITE"; }
function gitURL(endpoint, params = {}) {
  const query = new URLSearchParams(params);
  if (isMulti()) query.set("repo", state.repoName);
  return `${sbase()}/${endpoint}${query.size ? "?" + query : ""}`;
}
function gitProject() {
  if (!isMulti()) return state.project;
  const repo = selectedRepo();
  return { ...state.project, source: repo?.url || "", repo: repo?.remote_repo || "", git_provider: repo?.git_provider || "", base_branch: repo?.state?.base_branch || "" };
}
function gitSessionState() {
  if (!isMulti()) return state.session;
  return { ...state.session, branch: "", pr_url: "", pr_number: 0, base_commit: "", ...selectedRepo()?.state, ...state.session?.repo_states?.[state.repoName] };
}
function workspaceRepoBase(p = state.project, session = state.session) {
  return session ? sessionURL(p.id, session.id) : `/api/projects/${p.id}`;
}
async function loadWorkspaceRepos() {
  if (!isMulti()) return;
  const base = workspaceRepoBase();
  try {
    const data = await api("GET", `${base}/repos`);
    if (!state.project || workspaceRepoBase() !== base) return;
    state.repos = data.repos || [];
    if (!state.repos.some((r) => r.name === state.repoName)) state.repoName = (state.repos.find((r) => r.primary) || state.repos[0])?.name || "";
    renderTopbar();
  } catch (ex) { report(ex); }
}

function openWorkspaceRepos(project, session) {
  const base = workspaceRepoBase(project, session);
  const list = h("div", { class: "col", "data-workspace-repos": "" });
  const error = h("div", { class: "err-text", role: "alert" });
  const name = h("input", { class: "field", "aria-label": t("repos.name"), maxlength: 64 });
  const path = h("input", { class: "field", "aria-label": t("repos.path"), maxlength: 64 });
  const url = h("input", { class: "field", "aria-label": t("new.url"), placeholder: "https://github.com/owner/repo.git" });
  const ref = h("input", { class: "field", "aria-label": t("env.branch"), placeholder: t("new.branchPh") });
  const access = h("select", { class: "field", "aria-label": t("repos.access") },
    h("option", { value: "REPO_ACCESS_READ" }, t("repos.readOnly")), h("option", { value: "REPO_ACCESS_WRITE" }, t("repos.writable")));
  const primary = h("input", { type: "checkbox" });
  const attach = h("button", { class: "btn primary", "data-repo-attach": "" }, t("repos.attach"));
  const close = h("button", { class: "btn ghost" }, t("common.close"));
  let running = false;
  const dialog = h("dialog", {},
    h("div", { class: "d-head" }, h("h2", {}, t("repos.manage"))),
    h("div", { class: "d-body col" }, h("div", { class: "muted" }, t(session ? "repos.sessionHint" : "repos.baselineHint")), list,
      h("label", { class: "lbl" }, t("new.url")), url,
      h("div", { class: "row" }, h("label", { class: "grow lbl" }, t("repos.name"), name), h("label", { class: "grow lbl" }, t("repos.path"), path)),
      h("label", { class: "lbl" }, t("env.branch")), ref,
      h("label", { class: "lbl" }, t("repos.access")), access,
      h("label", { class: "row" }, primary, t("repos.primary")), error),
    h("div", { class: "d-foot" }, close, attach));
  close.onclick = () => dialog.close();
  dialog.addEventListener("close", () => dialog.remove());
  url.addEventListener("change", () => {
    const guess = url.value.trim().split("/").pop().replace(/\.git$/, "").replace(/[^A-Za-z0-9._-]/g, "-").slice(0, 64);
    if (!name.value) name.value = guess;
    if (!path.value) path.value = guess;
  });
  const busy = () => running || (session && !!state.tasks.find((task) => !TERMINAL.has(statusOf(task)) && state.session?.id === session.id));
  async function refresh() {
    const data = await api("GET", `${base}/repos`);
    list.replaceChildren(...(data.repos.length ? data.repos.map((repo) => h("div", { class: "card row", "data-repo-name": repo.name },
      h("div", { class: "grow" }, h("strong", {}, repo.name), " ", h("span", { class: "mono muted" }, repo.path),
        h("div", { class: "small muted" }, repo.url),
        h("span", { class: "chip" }, t(repo.access === "REPO_ACCESS_WRITE" ? "repos.writable" : "repos.readOnly")),
        repo.primary ? h("span", { class: "chip" }, t("repos.primary")) : null),
      h("button", { class: "btn danger sm", disabled: busy(), onclick: () => detach(repo) }, t("repos.detach")))) : [h("div", { class: "muted" }, t("repos.empty"))]));
    if (state.project?.id === project.id && workspaceRepoBase() === base) {
      await loadWorkspaceRepos(); loadChanges(); if (state.drawer) renderDrawer();
    }
    attach.disabled = busy();
  }
  async function detach(repo) {
    if (busy() || !confirm(t("repos.detachConfirm", { name: repo.name }))) return;
    running = true; attach.disabled = true; error.textContent = "";
    try {
      let result;
      try { result = await api("POST", `${base}/repos/${encodeURIComponent(repo.name)}/detach`, { discard_changes: false }); }
      catch (ex) {
        if (ex.code !== "repo_dirty" || !confirm(t("repos.discardConfirm", { name: repo.name }))) throw ex;
        result = await api("POST", `${base}/repos/${encodeURIComponent(repo.name)}/detach`, { discard_changes: true });
      }
      toast(t("repos.detached", { files: result.discardedFiles || 0, commits: result.discardedCommits || 0 }));
    } catch (ex) { error.textContent = ex.message; }
    finally { running = false; await refresh().catch((ex) => { error.textContent = ex.message; }); }
  }
  attach.onclick = async () => {
    if (busy()) return;
    running = true; attach.disabled = true; attach.textContent = t("repos.attaching"); error.textContent = "";
    try {
      await api("POST", `${base}/repos`, { repo: { name: name.value.trim(), url: url.value.trim(), ref: ref.value.trim(), path: path.value.trim(), access: access.value, primary: primary.checked } });
      name.value = path.value = url.value = ref.value = ""; primary.checked = false;
    } catch (ex) { error.textContent = ex.message; }
    finally { running = false; attach.textContent = t("repos.attach"); await refresh().catch((ex) => { error.textContent = ex.message; }); }
  };
  document.body.append(dialog); dialog.showModal();
  refresh().catch((ex) => { error.textContent = ex.message; });
}

// ---------------------------------------------------------------- 项目

function openNewProject(initial) {
  const conns = gitConnections();
  let mode = initial || (conns.length ? "git" : state.samples.length ? "sample" : "git");
  let sample = state.samples[0] ? state.samples[0].id : "";
  let provider = conns[0] ? conns[0].provider : "";
  const repoCache = {}; // 平台 → 仓库列表（"loading" 表示在读）
  let filter = "";
  const name = h("input", { class: "field", placeholder: t("new.name") });
  const gitUrl = h("input", { class: "field", placeholder: "https://github.com/owner/repo.git" });
  const branch = h("input", { class: "field", placeholder: t("new.branchPh") });
  const err = h("div", { class: "err-text" });
  const seg = h("div", { class: "seg" });
  const body = h("div", { class: "col" });
  const create = h("button", { class: "btn primary" }, t("new.create"));
  const cancel = h("button", { class: "btn ghost" }, t("common.cancel"));
  const dialog = h("dialog", {},
    h("div", { class: "d-head" }, h("h2", {}, t("new.title"))),
    h("div", { class: "d-body" }, seg, body, err),
    h("div", { class: "d-foot" }, cancel, create),
  );
  const repoList = h("div", { class: "pick-list" });
  const drawRepos = () => {
    const p = provider;
    if (!p) return repoList.replaceChildren();
    if (repoCache[p] === undefined) {
      repoCache[p] = "loading";
      api("GET", `/api/git/${p}/repos`).then((r) => { repoCache[p] = r; if (provider === p) drawRepos(); })
        .catch((ex) => { repoCache[p] = []; err.textContent = ex.message; if (provider === p) drawRepos(); });
    }
    const repos = repoCache[p];
    if (repos === "loading") return repoList.replaceChildren(h("div", { class: "row muted" }, h("span", { class: "spinner" }), t("new.loadingRepos", { name: providerInfo(p).name })));
    const q = filter.trim().toLowerCase();
    const shown = repos.filter((r) => !q || r.full_name.toLowerCase().includes(q)).slice(0, 100);
    // 服务端已把自己名下的排在前面：两组都有时各加一个小标题（别人的多是协作者或私有 fork 继承来的权限）。
    const grouped = shown.some((r) => r.mine) && shown.some((r) => !r.mine);
    const rows = [];
    shown.forEach((r, i) => {
      if (grouped && (i === 0 || shown[i - 1].mine !== r.mine)) rows.push(h("div", { class: "pick-group" }, t(r.mine ? "new.yourRepos" : "new.sharedRepos")));
      rows.push(h("button", {
        class: "pick" + (gitUrl.value === r.clone_url ? " active" : ""),
        onclick: () => { gitUrl.value = r.clone_url; branch.value = r.default_branch || ""; name.value = r.full_name.split("/").pop(); drawRepos(); },
      }, badge(r.provider, "sm"), h("span", { class: "mono grow ellipsis" }, r.full_name),
        r.private ? h("span", { class: "chip" }, t("new.private")) : null,
        r.can_push ? null : h("span", { class: "chip warn" }, t("new.readOnly"))));
    });
    repoList.replaceChildren(...(rows.length ? rows : [h("div", { class: "muted" }, t("new.noRepos"))]));
  };
  const draw = () => {
    seg.replaceChildren(...[["git", "branch", conns.length ? t("new.myRepos") : t("new.gitUrl")], ["sample", "box", t("new.sample")], ["empty", "folder", t("source.empty")], ["multi", "layers", t("repos.multi")]].map(([k, ic, l]) =>
      h("button", { class: mode === k ? "active" : "", onclick: () => { mode = k; draw(); } }, icon(ic, "sm"), l)));
    const parts = [];
    if (mode === "git") {
      if (conns.length) {
        if (conns.length > 1) {
          parts.push(h("div", { class: "pv-tabs" }, conns.map((c) => h("button", {
            class: "pv-tab" + (c.provider === provider ? " active" : ""),
            onclick: () => { provider = c.provider; filter = ""; draw(); },
          }, badge(c.provider, "xs"), providerInfo(c.provider).name))));
        }
        const search = h("input", { class: "field", placeholder: t("new.searchPh", { name: providerInfo(provider).name }), value: filter });
        search.addEventListener("input", () => { filter = search.value; drawRepos(); });
        parts.push(h("div", { class: "col" }, search, repoList));
        drawRepos();
      }
      const connectable = providerList().filter((p) => p.git && p.configured && !connection(p.id));
      if (connectable.length) {
        parts.push(h("div", { class: "connect-row" }, h("span", { class: "muted small" }, conns.length ? t("new.alsoConnect") : t("new.connect")),
          connectable.map((p) => h("a", { class: "btn xs", href: `/api/auth/${p.id}/login?link=1` }, badge(p.id, "xs"), p.name))));
      }
      parts.push(h("div", {}, h("label", { class: "lbl" }, conns.length ? t("new.orUrl") : t("new.url")), gitUrl));
      parts.push(h("div", { class: "row" }, h("div", { class: "grow" }, h("label", { class: "lbl" }, t("new.name")), name), h("div", { class: "grow" }, h("label", { class: "lbl" }, t("env.branch")), branch)));
    } else if (mode === "sample") {
      parts.push(...state.samples.map((s) => h("div", {
        class: "sample-card" + (sample === s.id ? " active" : ""),
        onclick: () => { sample = s.id; name.value = s.name; draw(); },
      }, h("div", { class: "row" }, icon("box", "sm"), h("strong", {}, s.name)), h("div", { class: "muted" }, s.description))));
      if (!name.value && state.samples[0]) name.value = (state.samples.find((s) => s.id === sample) || state.samples[0]).name;
      parts.push(h("div", {}, h("label", { class: "lbl" }, t("new.name")), name));
    } else {
      parts.push(h("div", { class: "muted" }, t(mode === "multi" ? "repos.createDesc" : "new.emptyDesc")), h("div", {}, h("label", { class: "lbl" }, t("new.name")), name));
    }
    body.replaceChildren(...parts);
  };
  draw();
  cancel.addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => dialog.remove());
  create.addEventListener("click", async () => {
    err.textContent = "";
    create.disabled = true;
    create.textContent = mode === "git" ? t("new.cloning") : t("new.creating");
    const req = { name: name.value || (mode === "git" ? (gitUrl.value.split("/").pop() || "").replace(/\.git$/, "") : "") };
    if (mode === "git") Object.assign(req, { git_url: gitUrl.value, branch: branch.value });
    if (mode === "sample") req.sample = sample;
    if (mode === "multi") req.layout = "WORKSPACE_LAYOUT_MULTI";
    try {
      const p = await api("POST", "/api/projects", req);
      state.projects.unshift(p);
      dialog.close();
      openDraft(p.id);
      if (mode === "multi") openWorkspaceRepos(p, null);
      const s = mode === "sample" ? state.samples.find((x) => x.id === sample) : null;
      if (s) setComposer(s.prompt);
    } catch (ex) {
      if (ex.message !== "unauthorized") err.textContent = ex.message;
      create.disabled = false;
      create.textContent = t("new.create");
    }
  });
  document.body.append(dialog);
  dialog.showModal();
}

async function deleteProject(p) {
  const n = state.sessions.filter((s) => s.project_id === p.id).length;
  if (!confirm(t(n ? "ws.deleteConfirmSessions" : "ws.deleteConfirm", { name: p.name, n }))) return;
  try {
    await api("DELETE", `/api/projects/${p.id}`);
    state.projects = state.projects.filter((x) => x.id !== p.id);
    state.sessions = state.sessions.filter((s) => s.project_id !== p.id);
    toast(t("ws.deleted"));
    if (state.project && state.project.id === p.id) state.project = null;
    if (state.view === "workspaces") renderApp();
    else landing();
  } catch (ex) { report(ex); }
}

// ---------------------------------------------------------------- 计时、轮询与快捷键

let tick = 0;
setInterval(() => {
  tick++;
  // 进行中的任务：只改用时那段文字，不重画整轮（不打断正在输入的内容）。
  for (const t of state.tasks) {
    const run = state.runs.get(t.id);
    const el = document.getElementById("wl-" + t.id);
    if (!run || !el || !run.events.length) continue;
    el.textContent = workingText(workSteps(run.events).length, Date.now() - new Date(run.events[0].time));
  }
  // “已经等了多久”（提交中、正在规划、Reviewer 在审查……）：只改文字。
  for (const el of document.querySelectorAll("[data-since]")) el.textContent = fmtSince(el.dataset.since);
  const cur = inSession() && currentTask();
  const timer = document.getElementById("c-elapsed");
  if (timer && cur) {
    const run = state.runs.get(cur.id);
    if (run && run.events.length) timer.textContent = fmtTimer(Date.now() - new Date(run.events[0].time));
  }
  if (!state.me) return;
  if (cur && tick % 4 === 0) loadDetail(cur);
  if (tick % (state.active.length ? 5 : 30) === 0) loadActive();
}, 1000);

document.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "k" && state.me) {
    e.preventDefault();
    openSearch();
    return;
  }
  if (e.key === "Escape") {
    if (pop) { closePop(); return; }
    if (ui.drawer) { closeDrawer(); return; }
    if (document.querySelector("dialog[open]") || !state.me || !inSession()) return;
    const cur = currentTask();
    if (cur) { e.preventDefault(); cancelTask(cur.id); }
    return;
  }
  if (e.key === "/" && !isTyping(e.target) && ui.input && !ui.drawer && !document.querySelector("dialog[open]")) {
    e.preventDefault();
    ui.input.focus();
  }
});

document.addEventListener("mousedown", (e) => {
  if (pop && !pop.el.contains(e.target) && !pop.anchor.contains(e.target)) closePop();
});
window.addEventListener("resize", closePop);

// 浏览器的前进、后退：地址变了而且不是自己刚设的，就按地址重新打开。
window.addEventListener("hashchange", () => {
  if (!state.me || location.hash === routedHash) return;
  routedHash = location.hash;
  route();
});

async function boot() {
  document.documentElement.lang = locale;
  applyTheme();
  installScrollbars();
  // OAuth 回调带回来的结果：连接成功（?linked=<平台>）或失败（?error=…）。进主界面会改地址，先记下来。
  const params = hashParams();
  const linked = params.get("linked");
  const failed = params.get("error");
  try { state.config = await api("GET", "/api/config"); } catch { /* 用默认 */ }
  try { state.me = await api("GET", "/api/me"); } catch { state.me = null; }
  if (state.me) {
    await enterApp().catch(report);
    if (linked) { toast(t("account.linked", { name: providerInfo(linked).name })); openAccount(); }
    if (failed) toast(failed, true);
  } else renderLogin();
}

boot();
