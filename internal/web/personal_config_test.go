package web

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

func TestPersonalConfigurationTranslations(t *testing.T) {
	zh, en := dictionaries(t)
	for _, key := range []string{"title", "tools", "note", "instructions", "path", "content", "add", "remove", "defaultNote", "environment", "noEnvironment", "missing", "empty", "unavailable", "sessionNote", "use", "saved", "retire", "retireConfirm"} {
		if _, ok := zh["personal."+key]; !ok {
			t.Errorf("missing zh personal.%s", key)
		}
		if _, ok := en["personal."+key]; !ok {
			t.Errorf("missing en personal.%s", key)
		}
	}
}

const personalConfigProbe = `
const out = {errors: []};
addEventListener("error", e => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", e => out.errors.push(String(e.reason)));
const sleep = ms => new Promise(r => setTimeout(r, ms));
const wait = async fn => { for (let i=0;i<250;i++) { const v=fn(); if(v)return v; await sleep(40); } throw Error("timeout"); };
try {
 const open = await wait(() => document.querySelector("dialog.account .personal-open")); open.click();
 const instructions = await wait(() => document.querySelector("dialog.personal-config .personal-instructions"));
 out.instructions = instructions.value;
 out.version = document.querySelector(".personal-version").textContent;
 out.selected = [...document.querySelectorAll(".personal-skills [data-reference]")].filter(x => x.checked).map(x => x.dataset.reference);
 out.environment = document.querySelector(".personal-environment").value;
 instructions.value = "Use pytest";
 document.querySelector(".personal-add").click();
 const rows = document.querySelectorAll(".personal-file");
 rows[rows.length-1].querySelector("input").value = ".config/ruff.toml";
 rows[rows.length-1].querySelector("textarea").value = "line-length=100";
 document.querySelector(".personal-save").click();
 await wait(() => !document.querySelector("dialog.personal-config"));
 out.saved = true;
} catch (e) { out.errors.push(String(e.stack || e)); }
fetch("/__result", {method:"POST", body:JSON.stringify(out)});
`

func TestPersonalConfigurationBrowser(t *testing.T) {
	for _, language := range []string{"zh", "en"} {
		t.Run(language, func(t *testing.T) {
			var mu sync.Mutex
			var saved params
			extra := map[string]any{
				"GET /api/personal-config": params{"config": params{"version": "user/v1", "digest": "digest1", "document": params{"instructions": "Be concise", "files": []any{}, "defaults": params{"skillNames": []string{"review"}, "environmentId": "env1"}}}},
				"/api/skills":              params{"available": true, "skills": []params{{"name": "review", "description": "Review changes"}, {"name": "other", "description": "Other"}}},
				"/api/mcp":                 params{"available": true, "connections": []any{}},
				"/api/environments":        []params{{"id": "env1", "name": "Python"}},
				"PUT /api/personal-config": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					_ = json.NewDecoder(r.Body).Decode(&saved)
					_ = json.NewEncoder(w).Encode(params{"config": params{"version": "user/v2", "digest": "digest2"}})
				}),
			}
			// runAccountProbe accepts method-qualified handler routes, plain keys for data.
			extra["/api/personal-config"] = extra["GET /api/personal-config"]
			delete(extra, "GET /api/personal-config")
			var result struct {
				Errors       []string `json:"errors"`
				Instructions string   `json:"instructions"`
				Version      string   `json:"version"`
				Selected     []string `json:"selected"`
				Environment  string   `json:"environment"`
				Saved        bool     `json:"saved"`
			}
			if err := json.Unmarshal(runAccountProbe(t, language, personalConfigProbe, params{}, extra), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Errors) > 0 || !result.Saved || result.Instructions != "Be concise" || result.Version != "user/v1 · digest1" || len(result.Selected) != 1 || result.Selected[0] != "review" || result.Environment != "env1" {
				t.Fatalf("UI: %+v", result)
			}
			mu.Lock()
			defer mu.Unlock()
			if saved["expected_version"] != "user/v1" {
				t.Fatalf("version guard: %v", saved)
			}
			doc := saved["document"].(map[string]any)
			if doc["instructions"] != "Use pytest" || len(doc["files"].([]any)) != 1 {
				t.Fatalf("save: %v", doc)
			}
		})
	}
}
