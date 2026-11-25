// Admin Observability & Self-Evolution Console mounted cleanly on #/admin without modifying app.js.
import { lang, setLang, t } from "./i18n.js";

const CONSOLE_ID = "admin-console";
let state = {
  loading: false,
  session: null,
  overview: null,
  statusFilter: "",
  error: "",
  busyID: "",
};

function isAdminRoute() {
  const hash = (location.hash || "").toLowerCase();
  const path = (location.pathname || "").toLowerCase();
  return hash.startsWith("#/admin") || path === "/admin" || path.startsWith("/admin/");
}

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k.startsWith("on") && typeof v === "function") {
      node.addEventListener(k.slice(2).toLowerCase(), v);
    } else if (k === "class") {
      node.className = v;
    } else if (typeof v === "boolean") {
      node.setAttribute(k, v ? "" : "false");
      if (k in node) node[k] = v;
    } else {
      node.setAttribute(k, String(v));
    }
  }
  for (const ch of children.flat()) {
    if (ch === null || ch === undefined || ch === false) continue;
    node.append(ch instanceof Node ? ch : document.createTextNode(String(ch)));
  }
  return node;
}

async function requestJSON(method, url, body) {
  const headers = { "X-Coding-Web": "1" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(url, {
    method,
    headers,
    credentials: "same-origin",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = { error: text };
  }
  if (!res.ok) {
    throw new Error(data.error || `HTTP ${res.status}`);
  }
  return data;
}

function badgeClass(status) {
  if (["promoted", "ok", "true", "pending_approval"].includes(status)) {
    return status === "pending_approval" ? "admin-badge warn" : "admin-badge ok";
  }
  if (["rejected_by_gate", "err", "false", "rolled_back"].includes(status)) {
    return status === "rolled_back" ? "admin-badge warn" : "admin-badge err";
  }
  return "admin-badge";
}

function sparklineSVG(points) {
  const svgNS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(svgNS, "svg");
  svg.setAttribute("class", "admin-spark");
  svg.setAttribute("viewBox", "0 0 200 44");
  svg.setAttribute("preserveAspectRatio", "none");
  if (!points || points.length < 2) {
    const line = document.createElementNS(svgNS, "line");
    line.setAttribute("x1", "0");
    line.setAttribute("y1", "36");
    line.setAttribute("x2", "200");
    line.setAttribute("y2", "36");
    line.setAttribute("stroke", "#cbd5e1");
    line.setAttribute("stroke-width", "1.5");
    svg.appendChild(line);
    return svg;
  }
  const vals = points.map((p) => Number(p.value || 0));
  const min = Math.min(...vals);
  const max = Math.max(...vals);
  const span = max - min || 1;
  const coords = vals
    .map((v, i) => {
      const x = (i / (vals.length - 1)) * 196 + 2;
      const y = 38 - ((v - min) / span) * 32;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  const poly = document.createElementNS(svgNS, "polyline");
  poly.setAttribute("fill", "none");
  poly.setAttribute("stroke", "#2563eb");
  poly.setAttribute("stroke-width", "2");
  poly.setAttribute("points", coords);
  svg.appendChild(poly);
  return svg;
}

function diffView(diffText) {
  const pre = el("pre", { class: "admin-diff" });
  for (const line of String(diffText || "").split("\n")) {
    let cls = "admin-diff-line";
    if (line.startsWith("+") && !line.startsWith("+++")) cls += " add";
    else if (line.startsWith("-") && !line.startsWith("---")) cls += " del";
    else if (line.startsWith("@@")) cls += " hunk";
    pre.appendChild(el("div", { class: cls }, line || " "));
  }
  return pre;
}

async function refreshData() {
  state.loading = true;
  state.error = "";
  render();
  try {
    state.session = await requestJSON("GET", "/api/admin/v1/session");
    if (state.session.authenticated) {
      state.overview = await requestJSON("GET", "/api/admin/v1/overview");
    } else {
      state.overview = null;
    }
  } catch (err) {
    state.error = err.message || String(err);
  } finally {
    state.loading = false;
    render();
  }
}

function renderLogin() {
  let emailInput, passInput, formErr;
  const form = el(
    "form",
    {
      class: "admin-login-card",
      onSubmit: async (e) => {
        e.preventDefault();
        formErr.textContent = "";
        formErr.hidden = true;
        try {
          await requestJSON("POST", "/api/admin/v1/login", {
            email: emailInput.value.trim(),
            password: passInput.value,
          });
          await refreshData();
        } catch (err) {
          formErr.textContent = err.message || String(err);
          formErr.hidden = false;
        }
      },
    },
    el("h2", {}, t("admin.loginTitle")),
    el("p", { class: "admin-muted" }, t("admin.loginLead")),
    (formErr = el("div", { class: "admin-alert", hidden: true })),
    el(
      "div",
      { class: "admin-field" },
      el("label", { for: "admin-email-input" }, t("admin.email")),
      (emailInput = el("input", { id: "admin-email-input", type: "email", required: true, autocomplete: "username" }))
    ),
    el(
      "div",
      { class: "admin-field" },
      el("label", { for: "admin-password-input" }, t("admin.password")),
      (passInput = el("input", { id: "admin-password-input", type: "password", required: true, autocomplete: "current-password" }))
    ),
    el("button", { class: "admin-btn primary", type: "submit" }, t("admin.signIn"))
  );
  return form;
}

function renderOverview() {
  const ov = state.overview || {};
  const tel = ov.telemetry || {};
  const vm = tel.victoria_metrics || {};
  const gf = tel.grafana || {};
  const api = tel.task_api || {};
  const comp = tel.companion_metrics || {};
  const reg = ov.registry || {};
  const allCandidates = ov.candidates || [];
  const filtered = state.statusFilter ? allCandidates.filter((c) => c.status === state.statusFilter) : allCandidates;

  const metricCards = el(
    "div",
    { class: "admin-grid" },
    el(
      "div",
      { class: "admin-card" },
      el("h3", {}, t("admin.taskApi")),
      el("div", { class: "admin-stat-value" }, api.healthy ? "OK" : t("admin.offline")),
      el("span", { class: badgeClass(String(!!api.healthy)) }, api.latency_ms !== undefined ? `${api.latency_ms} ms` : "n/a")
    ),
    el(
      "div",
      { class: "admin-card" },
      el("h3", {}, "VictoriaMetrics"),
      el("div", { class: "admin-stat-value" }, vm.targets_up !== undefined ? String(vm.targets_up) : vm.available ? "UP" : t("admin.offline")),
      el("p", { class: "admin-muted" }, `${t("admin.targetsUp")} · OTel ${Number(vm.otel_spans_per_sec || 0).toFixed(2)} spans/s`)
    ),
    el(
      "div",
      { class: "admin-card" },
      el("h3", {}, "Grafana"),
      el("div", { class: "admin-stat-value" }, gf.available ? `v${gf.version || "ready"}` : t("admin.offline")),
      el("span", { class: badgeClass(String(!!gf.available)) }, gf.database || "observability")
    ),
    el(
      "div",
      { class: "admin-card" },
      el("h3", {}, t("admin.codingDanceMetrics")),
      el("div", { class: "admin-stat-value" }, String(comp.hint_checks || 0)),
      el(
        "p",
        { class: "admin-muted" },
        `${t("admin.prompts")}: ${comp.consent_prompts || 0} · ${t("admin.revealed")}: ${comp.hints_revealed || 0} · ${t("admin.blocked")}: ${comp.consent_blocked || 0}`
      )
    )
  );

  const seriesSection =
    (tel.series || []).length > 0
      ? el(
          "div",
          { class: "admin-grid" },
          ...(tel.series || []).map((s) =>
            el(
              "div",
              { class: "admin-card" },
              el("h3", {}, s.label || s.name),
              el("div", { class: "admin-stat-value" }, s.latest !== undefined && s.latest !== null ? `${Number(s.latest).toFixed(2)} ${s.unit || ""}` : "--"),
              sparklineSVG(s.points || [])
            )
          )
        )
      : null;

  const regSection = el(
    "div",
    { class: "admin-card" },
    el("h3", {}, t("admin.registryTitle")),
    el(
      "p",
      { class: "admin-muted" },
      `${t("admin.isolatedTenant")}: sys-evolution (${reg.sys_evolution_policy?.version || "unseeded"}) · ${t("admin.platformSkills")}: ${(reg.platform_skills || []).length}`
    ),
    el(
      "div",
      { class: "admin-pill-row" },
      ...(reg.platform_skills || []).map((sk) => el("span", { class: "admin-badge ok" }, `${sk.name} · ${sk.version}`)),
      ...(reg.sys_evolution_skills || []).map((sk) => el("span", { class: "admin-badge warn" }, `${sk.name} (${sk.version})`))
    )
  );

  const statuses = ["", "pending_approval", "promoted", "rolled_back", "rejected_by_gate"];
  const filterRow = el(
    "div",
    { class: "admin-toolbar" },
    el("h3", {}, `${t("admin.evolutionQueue")} (${filtered.length})`),
    el(
      "div",
      { class: "admin-filter-group" },
      ...statuses.map((st) =>
        el(
          "button",
          {
            type: "button",
            class: `admin-btn${state.statusFilter === st ? " active" : ""}`,
            onClick: () => {
              state.statusFilter = st;
              render();
            },
          },
          st || t("admin.all")
        )
      )
    )
  );

  const candidateCards = filtered.map((cand) => {
    const gate = (cand.matrix_report || {}).gate || {};
    const busy = state.busyID === cand.candidate_id;
    const canPromote = cand.status === "pending_approval" && gate.passed === true;
    const canRollback = cand.status === "promoted";
    return el(
      "article",
      { class: "admin-card" },
      el(
        "div",
        { class: "admin-cand-head" },
        el(
          "div",
          { class: "admin-title-group" },
          el("strong", {}, `${cand.target_name} (${cand.baseline_version || "baseline"})`),
          el("span", { class: badgeClass(cand.status) }, cand.status),
          cand.staged_skill_name ? el("span", { class: "admin-badge" }, cand.staged_skill_name) : null
        ),
        el(
          "div",
          { class: "admin-actions" },
          canPromote
            ? el(
                "button",
                {
                  type: "button",
                  class: "admin-btn primary",
                  disabled: busy,
                  onClick: async () => {
                    state.busyID = cand.candidate_id;
                    render();
                    try {
                      await requestJSON("POST", `/api/admin/v1/evolution/candidates/${encodeURIComponent(cand.candidate_id)}/promote`, {});
                      await refreshData();
                    } catch (err) {
                      state.error = err.message || String(err);
                    } finally {
                      state.busyID = "";
                      render();
                    }
                  },
                },
                t("admin.promote")
              )
            : null,
          canRollback
            ? el(
                "button",
                {
                  type: "button",
                  class: "admin-btn danger",
                  disabled: busy,
                  onClick: async () => {
                    state.busyID = cand.candidate_id;
                    render();
                    try {
                      await requestJSON("POST", `/api/admin/v1/evolution/candidates/${encodeURIComponent(cand.candidate_id)}/rollback`, {
                        reason: "admin console rollback",
                      });
                      await refreshData();
                    } catch (err) {
                      state.error = err.message || String(err);
                    } finally {
                      state.busyID = "";
                      render();
                    }
                  },
                },
                t("admin.rollback")
              )
            : null
        )
      ),
      el(
        "div",
        { class: "admin-cand-metrics" },
        el("span", {}, `ID: ${cand.candidate_id}`),
        el("span", {}, `Gate: ${gate.passed ? "PASS" : "FAIL"}`),
        gate.baseline_pass_rate !== undefined
          ? el("span", {}, `Pass: ${Number(gate.baseline_pass_rate * 100).toFixed(0)}% -> ${Number(gate.candidate_pass_rate * 100).toFixed(0)}%`)
          : null,
        gate.baseline_blind_read_ratio !== undefined
          ? el("span", {}, `Blind Read: ${Number(gate.baseline_blind_read_ratio * 100).toFixed(0)}% -> ${Number(gate.candidate_blind_read_ratio * 100).toFixed(0)}%`)
          : null,
        cand.approved_by ? el("span", {}, `${t("admin.approvedBy")}: ${cand.approved_by} (${cand.promoted_version || ""})`) : null
      ),
      diffView(cand.unified_diff)
    );
  });

  const evolutionCard = el(
    "div",
    { class: "admin-card" },
    filterRow,
    candidateCards.length > 0 ? el("div", { class: "admin-main" }, ...candidateCards) : el("p", { class: "admin-muted" }, t("admin.noCandidates"))
  );

  return el("div", { class: "admin-main" }, metricCards, seriesSection, regSection, evolutionCard);
}

function render() {
  let host = document.getElementById(CONSOLE_ID);
  if (!isAdminRoute()) {
    document.body.classList.remove("admin-active");
    if (host) host.remove();
    return;
  }
  document.body.classList.add("admin-active");
  if (!host) {
    host = el("section", { id: CONSOLE_ID, class: "admin-shell" });
    document.body.appendChild(host);
  }

  const sess = state.session || {};
  const header = el(
    "header",
    { class: "admin-header" },
    el(
      "div",
      { class: "admin-title-group" },
      el("h1", { class: "admin-title" }, t("admin.title")),
      sess.authenticated ? el("span", { class: "admin-badge ok" }, sess.email || "admin") : null
    ),
    el(
      "div",
      { class: "admin-actions" },
      el("button", { type: "button", class: "admin-btn", onClick: () => setLang(lang === "zh" ? "en" : "zh") }, lang === "zh" ? "English" : "中文"),
      el("a", { class: "admin-btn", href: "#/" }, t("admin.backWorkspace")),
      sess.authenticated ? el("button", { type: "button", class: "admin-btn", disabled: state.loading, onClick: refreshData }, t("admin.refresh")) : null,
      sess.authenticated
        ? el(
            "button",
            {
              type: "button",
              class: "admin-btn",
              onClick: async () => {
                await requestJSON("POST", "/api/admin/v1/logout", {});
                await refreshData();
              },
            },
            t("admin.signOut")
          )
        : null
    )
  );

  const errBanner = state.error ? el("div", { class: "admin-alert" }, state.error) : null;
  const body = state.loading && !state.session
    ? el("p", { class: "admin-muted" }, t("common.loading"))
    : sess.authenticated
      ? renderOverview()
      : renderLogin();

  host.replaceChildren(header, errBanner, body);
}

window.addEventListener("hashchange", () => {
  if (isAdminRoute()) refreshData();
  else render();
});

if (isAdminRoute()) {
  refreshData();
}
