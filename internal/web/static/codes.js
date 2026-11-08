// Runtime 进度事件里的代码翻成界面的语言：verdict 的 reason_code、问题的 question_code、node_finished 与 tool_status 的 error_code、
// stage_started 的 goal_code、exit_when_code、Task State 的 done_when_code、abort 交付的 codes、decision_retry。代码表见 Runtime 的 docs/plan/eval-observability-and-runtime-fixes.md 第 11.5、11.8 节。
// 不认识的代码、少了参数时一律用事件自己的文字（Runtime 新增代码不必先改这里）。文字都在 i18n.js；这个模块不碰 DOM。

import { t, tCode } from "./i18n.js";

// 参数里内部的取值换成界面的叫法。
const LABELS = { step: "ladder", next: "ladder", budget: "budget", risk: "effect", ceiling: "effect" };

function usd(x) {
  return x === undefined ? x : `$${Number(x).toFixed(2)}`;
}

function messageParams(params) {
  const raw = params || {};
  const out = {};
  for (const [k, v] of Object.entries(raw)) out[k] = Array.isArray(v) ? v.join(t("list.sep")) : v;
  for (const k of ["spent_ratio", "completion"]) if (typeof raw[k] === "number") out[k] = `${Math.round(raw[k] * 100)}%`;
  for (const [k, prefix] of Object.entries(LABELS)) if (raw[k] !== undefined) out[k] = tCode(`${prefix}.${raw[k]}`) ?? raw[k];
  // 哪项预算用完了：说法按预算的种类不同；不认识的种类没有 spent，整句回落到事件的文字。
  if (raw.budget !== undefined) {
    const money = raw.budget === "usd";
    out.spent = tCode(`budget.spent.${raw.budget}`, { limit: money ? usd(raw.limit) : raw.limit, used: money ? usd(raw.used) : raw.used });
  }
  return out;
}

export function reasonText(v) {
  const params = messageParams(v.params);
  // 规则判定时说明为什么没有模型评审（Runtime 的 docs/plan/feedback-2026-11-01.md N-02）；不认识的原因用通用的说法。
  const cause = v.reason_code === "rule_fallback" && v.params && v.params.cause;
  const why = cause ? tCode(`reason.rule_fallback.${cause}`, params) : undefined;
  return why ?? tCode(`reason.${v.reason_code}`, params) ?? v.reason ?? "";
}

// Only recognized, complete typed requests get the structured network approval UI.
// The original HumanChannel question remains the fallback for older/malformed events.
const NETWORK_QUESTIONS = new Set(["network_permission_hosts", "network_permission_public", "network_permission_hosts_unlimited", "network_permission_public_unlimited"]);
export function networkPermission(v) {
  if (!NETWORK_QUESTIONS.has(v.question_code)) return null;
  const p = v.params;
  if (!p || !Array.isArray(p.hosts) || !Array.isArray(p.ports) || !p.ports.length ||
      !p.hosts.every((host) => typeof host === "string" && host.trim()) ||
      !p.ports.every((port) => typeof port === "string" && port.trim()) ||
      !Number.isInteger(p.minutes) || p.minutes < 0 || typeof p.reason !== "string" || !p.reason.trim()) return null;
  if (v.question_code.endsWith("_unlimited") && p.minutes !== 0) return null;
  const scope = v.question_code.startsWith("network_permission_public") ? "public" : "hosts";
  if (scope === "hosts" && !p.hosts.length) return null;
  return { scope, hosts:p.hosts, ports:p.ports, minutes:p.minutes, reason:p.reason };
}

// questionText 规则生成的问题用 Verdict 的代码：理由加上固定的三选一。翻不了时是事件的 text。
export function questionText(v) {
  if (NETWORK_QUESTIONS.has(v.question_code) && !networkPermission(v)) return v.text || "";
  const params = messageParams(v.params);
  const code = networkPermission(v)?.minutes === 0 ? `${v.question_code.replace(/_unlimited$/, "")}_unlimited` : v.question_code;
  const own = tCode(`question.${code}`, params);
  if (own) return own;
  const why = tCode(`reason.${v.question_code}`, params);
  return why ? t("question.fromReason", { why }) : v.text || "";
}

export function errorText(v) {
  return tCode(`error.${v.error_code}`, messageParams(v.params)) ?? v.error ?? "";
}

export function goalText(v) {
  return tCode(`goal.${v.goal_code}`, messageParams(v.params)) ?? v.goal ?? "";
}

export function exitWhenText(v) {
  return tCode(`exitWhen.${v.exit_when_code}`, messageParams(v.params)) ?? v.exit_when ?? "";
}

export function doneWhenText(v) {
  return tCode(`doneWhen.${v.done_when_code}`) ?? v.done_when ?? "";
}

// abortMarkdown 是 abort 的交付（Task 的结局，不是进度事件）：为什么停下、完成了什么、没完成什么。
// 理由是代码给的时候 Runtime 带 codes，理由与各 Node 按代码翻译；模型自己写的没有 codes，用它的原文。
export function abortMarkdown(d) {
  const codes = d.codes || {};
  const nodes = Array.isArray(codes.nodes) ? codes.nodes : null;
  const completed = nodes ? nodes.filter((n) => n.status === "succeeded").map((n) => n.node_id) : d.completed || [];
  let missing = d.not_completed || [];
  if (nodes) {
    missing = nodes.filter((n) => n.status !== "succeeded").map((n) => t("abort.nodeError", { id: n.node_id, why: errorText(n) || n.status }));
    if (!missing.length) missing = [t("abort.noDelivery")];
  }
  const section = (title, items) => `### ${title}\n${items.map((x) => `- ${x}`).join("\n")}`;
  return [
    `### ${t("abort.why")}\n${reasonText({ ...codes, reason: d.reason })}`,
    completed.length ? section(t("abort.completed"), completed) : "",
    missing.length ? section(t("abort.notCompleted"), missing) : "",
  ].filter(Boolean).join("\n\n");
}

// 上次失败的代码归成三种说法；其余的都算“没有成功”，详情里给事件的文字。
const RETRY_KINDS = new Map([
  ["activity_timeout", "timeout"], ["activity_deadline", "timeout"], ["grpc_deadline_exceeded", "timeout"],
  ["models_exhausted", "unavailable"], ["grpc_unavailable", "unavailable"],
]);

export function retryTitle(v) {
  const what = tCode(`retry.step.${v.step}`, { node: v.node_id }) ?? t("retry.call");
  return t(`retry.${RETRY_KINDS.get(v.error_code) || "failed"}`, { what, attempt: v.attempt, max: v.max_attempts });
}

export function retryDetail(v) {
  return RETRY_KINDS.has(v.error_code) ? "" : v.error || v.error_code || "";
}
