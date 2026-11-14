package web

// WEB-SKILLS（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md；N-14）：“我的 Skill”页。
//
//   - 账号页总有一个“我的 Skill”按钮（.skills-open），打开对话框 dialog.skills。
//   - 列出看得到的 Skill：每个一行 .skill-row（data-name、data-scope），写着说明；平台与团队的只读，我的有“编辑”（.skill-edit）与
//     “收回”（.skill-retire）；被遮住的我的（shadowed）写明不会被用到。
//   - 新建：名字 .skill-name、说明 .skill-description、做法 .skill-instructions（HeroUI 的文本框宿主，读写 value 同原生），保存 .skill-save 调
//     PUT /api/skills/<名字>，之后重新列出。编辑：先 GET /api/skills/<名字> 填进表单（名字不能改），保存时原有的文件照旧带上。
//   - /api/skills 回 available false 时只写明这个部署还没有 Skill（.skills-unavailable），没有表单。
//   - 新的文字两种语言都有。
// 静态检查总是跑；真正执行 JS 的检查用浏览器（找不到时跳过）。

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

var skillKeys = []string{
	"skills.open", "skills.title", "skills.note", "skills.empty", "skills.unavailable", "skills.scope.platform", "skills.scope.tenant",
	"skills.scope.user", "skills.shadowed", "skills.name", "skills.description", "skills.instructions", "skills.save", "skills.saved",
	"skills.edit", "skills.cancel", "skills.retire", "skills.retireConfirm", "skills.retired", "skills.files",
}

func TestTheSkillsTextIsInBothLanguages(t *testing.T) {
	zh, en := dictionaries(t)
	for _, key := range skillKeys {
		if _, ok := zh[key]; !ok {
			t.Errorf("中文字典缺 %s", key)
		}
		if _, ok := en[key]; !ok {
			t.Errorf("英文字典缺 %s", key)
		}
	}
	_, app := getFrom(t, Handler(), "/assets/app.js")
	if !strings.Contains(string(app), "/api/skills") {
		t.Error("app.js 要读 /api/skills")
	}
}

const skillsProbe = `
const out = { errors: [], button: false, first: null, editing: null, after: null, unavailable: false };
addEventListener("error", (e) => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", (e) => out.errors.push(String(e.reason && e.reason.stack || e.reason)));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wait = async (fn, ms) => { const end = Date.now() + ms; for (;;) { const v = fn(); if (v) return v; if (Date.now() > end) return null; await sleep(30); } };
const read = (d) => ({
  rows: [...d.querySelectorAll(".skill-row")].map((r) => ({
    name: r.dataset.name, scope: r.dataset.scope, edit: !!r.querySelector(".skill-edit"), retire: !!r.querySelector(".skill-retire"),
    shadowed: !!r.querySelector(".skill-shadowed"), text: r.textContent,
  })),
  form: !!d.querySelector(".skill-instructions"),
  text: d.textContent,
});
try {
  await wait(() => document.querySelector("dialog.account .conn-list"), 8000);
  const open = await wait(() => document.querySelector("dialog.account .skills-open"), 2000);
  out.button = !!open;
  if (open) {
    open.click();
    const d = await wait(() => document.querySelector("dialog.skills") && (document.querySelector("dialog.skills .skill-row") || document.querySelector("dialog.skills .skills-unavailable")) && document.querySelector("dialog.skills"), 4000);
    if (d && d.querySelector(".skills-unavailable")) {
      out.unavailable = true;
      out.first = read(d);
    } else if (d) {
      out.first = read(d);
      const mine = d.querySelector('.skill-row[data-name="weekly-report"] .skill-edit');
      if (mine) {
        mine.click();
        await wait(() => d.querySelector(".skill-instructions").value.includes("按模块"), 3000);
        out.editing = { name: d.querySelector(".skill-name").value, readOnly: d.querySelector(".skill-name").readOnly,
          instructions: d.querySelector(".skill-instructions").value, description: d.querySelector(".skill-description").value };
        d.querySelector(".skill-instructions").value = "改过的做法";
        d.querySelector(".skill-save").click();
        await wait(() => d.querySelector(".skill-save") && !d.querySelector(".skill-name").readOnly, 3000);
      }
      const name = d.querySelector(".skill-name");
      name.value = "new-skill"; name.dispatchEvent(new Event("input", { bubbles: true }));
      d.querySelector(".skill-description").value = "新的说明";
      d.querySelector(".skill-instructions").value = "新的做法";
      d.querySelector(".skill-save").click();
      await wait(() => d.querySelector('.skill-row[data-name="new-skill"]'), 4000);
      out.after = read(d);
    }
  }
} catch (ex) {
  out.errors.push(String(ex && ex.stack || ex));
}
await fetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

type skillRowView struct {
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Edit     bool   `json:"edit"`
	Retire   bool   `json:"retire"`
	Shadowed bool   `json:"shadowed"`
	Text     string `json:"text"`
}

type skillsView struct {
	Rows []skillRowView `json:"rows"`
	Form bool           `json:"form"`
	Text string         `json:"text"`
}

type skillsResult struct {
	Errors  []string    `json:"errors"`
	Button  bool        `json:"button"`
	First   *skillsView `json:"first"`
	Editing *struct {
		Name         string `json:"name"`
		ReadOnly     bool   `json:"readOnly"`
		Instructions string `json:"instructions"`
		Description  string `json:"description"`
	} `json:"editing"`
	After       *skillsView `json:"after"`
	Unavailable bool        `json:"unavailable"`
}

func TestTheSkillsPageListsEditsAndPublishes(t *testing.T) {
	var mu sync.Mutex
	var puts []params
	listed := func() []params {
		rows := []params{
			{"name": "deploy_guide", "version": "platform/v3", "scope": "platform", "description": "平台的部署指南"},
			{"name": "team_style", "version": "tenant/v1", "scope": "tenant", "description": "团队的代码风格"},
			{"name": "lint_rules", "version": "user/v1", "scope": "user", "description": "我的旧规则", "shadowed": true},
			{"name": "weekly-report", "version": "user/v2", "scope": "user", "description": "写周报"},
		}
		for _, p := range puts {
			if p["name"] == "new-skill" {
				rows = append(rows, params{"name": "new-skill", "version": "user/v1", "scope": "user", "description": "新的说明"})
			}
		}
		return rows
	}
	extra := map[string]any{
		"GET /api/skills": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(params{"skills": listed(), "available": true})
		}),
		"GET /api/skills/{name}": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(params{"skill": params{"name": r.PathValue("name"), "version": "user/v2", "scope": "user", "description": "写周报",
				"instructions": "按模块归类", "files": []params{{"path": "templates/report.md", "media_type": "text/markdown", "content": "## 本周"}}}})
		}),
		"PUT /api/skills/{name}": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body params
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["name"] = r.PathValue("name")
			mu.Lock()
			puts = append(puts, body)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(params{"skill": params{"name": r.PathValue("name"), "version": "user/v1", "scope": "user"}, "created": true, "new_version": true})
		}),
	}
	for _, lang := range []string{"zh", "en"} {
		mu.Lock()
		puts = nil
		mu.Unlock()
		var res skillsResult
		raw := runAccountProbe(t, lang, skillsProbe, params{}, extra)
		if err := json.Unmarshal(raw, &res); err != nil || len(res.Errors) != 0 || !res.Button || res.First == nil {
			t.Fatalf("%s：账号页有入口，对话框列出 Skill：%v %s", lang, err, raw)
		}
		byName := map[string]skillRowView{}
		for _, row := range res.First.Rows {
			byName[row.Name] = row
		}
		if p := byName["deploy_guide"]; p.Scope != "platform" || p.Edit || p.Retire || !strings.Contains(p.Text, "平台的部署指南") {
			t.Errorf("%s：平台的只读、写着说明：%+v", lang, p)
		}
		if m := byName["weekly-report"]; !m.Edit || !m.Retire {
			t.Errorf("%s：我的可以编辑与收回：%+v", lang, m)
		}
		if s := byName["lint_rules"]; !s.Shadowed {
			t.Errorf("%s：被遮住的我的写明不会被用到：%+v", lang, s)
		}
		if res.Editing == nil || res.Editing.Name != "weekly-report" || !res.Editing.ReadOnly || res.Editing.Instructions != "按模块归类" || res.Editing.Description != "写周报" {
			t.Errorf("%s：编辑先读出原文，名字不能改：%+v", lang, res.Editing)
		}
		mu.Lock()
		got := append([]params(nil), puts...)
		mu.Unlock()
		if len(got) != 2 {
			t.Fatalf("%s：编辑与新建各一次 PUT：%v", lang, got)
		}
		if got[0]["name"] != "weekly-report" || got[0]["instructions"] != "改过的做法" || got[0]["files"] == nil {
			t.Errorf("%s：编辑时原有的文件照旧带上：%v", lang, got[0])
		}
		if got[1]["name"] != "new-skill" || got[1]["description"] != "新的说明" || got[1]["instructions"] != "新的做法" {
			t.Errorf("%s：新建：%v", lang, got[1])
		}
		if res.After == nil || len(res.After.Rows) != 5 {
			t.Errorf("%s：保存之后重新列出：%+v", lang, res.After)
		}
		if strings.Contains(res.First.Text, "skills.") {
			t.Errorf("%s：有没翻译的键名：%q", lang, res.First.Text)
		}
	}
}

func TestTheSkillsPageSaysWhenTheDeploymentHasNone(t *testing.T) {
	extra := map[string]any{"/api/skills": params{"skills": []any{}, "available": false}}
	var res skillsResult
	raw := runAccountProbe(t, "zh", skillsProbe, params{}, extra)
	if err := json.Unmarshal(raw, &res); err != nil || len(res.Errors) != 0 {
		t.Fatalf("页面：%v %s", err, raw)
	}
	if !res.Unavailable || res.First == nil || res.First.Form {
		t.Errorf("Runtime 没有 Skill 时写明，不给表单：%s", raw)
	}
}
