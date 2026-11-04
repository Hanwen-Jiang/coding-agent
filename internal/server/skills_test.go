package server

// WEB-SKILLS（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md；N-14，docs/plan/n14-skill-contract.md）：“我的 Skill”页的服务端，
// 转调 Runtime 的 SkillService。
//
//   - GET /api/skills 列出看得到的 Skill（平台、团队、我的；不带正文）；GET /api/skills/{name}（可带 ?version=）读一个（带说明、
//     做法与文件）；PUT /api/skills/{name}（{"description","instructions","files"}）发布我的；DELETE /api/skills/{name} 收回我的。
//     带这个用户现签的令牌调 Runtime，租户取自登录的身份；请求里没有用户字段，只能管自己的。
//   - 名字先按契约核对（小写字母开头，小写字母、数字、_、-，最多 64 个字符），说明与做法不能为空；不合规时 400，不去问 Runtime。
//   - Runtime 没有 SkillService（Unimplemented 或 skills_unavailable）时列表回 {"skills": [], "available": false}，页上写明；
//     别的 Runtime 错误（name_taken、skill_limit 等）翻成用户看得懂的话。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// skillsTaskAPI 在 fakeTaskAPI 外面加上 SkillService：记下请求（方法、令牌、请求体），按名字存用户层的 Skill。
type skillsTaskAPI struct {
	fake        *fakeTaskAPI
	mu          sync.Mutex
	calls       []secretCall
	docs        map[string]map[string]any
	unavailable bool   // 回 Unimplemented（skills_unavailable）
	reason      string // PutSkill 回这个错误码
}

func (a *skillsTaskAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/agentruntime.skill.v1.SkillService/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		a.fake.ServeHTTP(w, r)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	method := strings.TrimPrefix(r.URL.Path, prefix)
	a.calls = append(a.calls, secretCall{method, r.Header.Get("Authorization"), body})
	if a.docs == nil {
		a.docs = map[string]map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	if a.unavailable {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = io.WriteString(w, `{"code":"unimplemented","message":"这个部署没有接 Registry","details":[{"type":"google.rpc.ErrorInfo","debug":{"reason":"skills_unavailable","domain":"agentruntime"}}]}`)
		return
	}
	name, _ := body["name"].(string)
	skill := func(name string, withDoc bool) map[string]any {
		doc := a.docs[name]
		out := map[string]any{"name": name, "version": "user/v1", "state": "RELEASE_STATE_DEFAULT", "scope": "SKILL_SCOPE_USER", "description": doc["description"], "owner": "alice", "publishedAt": "2026-11-04T09:00:00Z"}
		if withDoc {
			out["document"] = map[string]any{"value": doc}
		}
		return out
	}
	switch method {
	case "PutSkill":
		if a.reason != "" {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"already_exists","message":"平台已有这个名字","details":[{"type":"google.rpc.ErrorInfo","debug":{"reason":"`+a.reason+`","domain":"agentruntime"}}]}`)
			return
		}
		payload, _ := body["document"].(map[string]any)
		doc, _ := payload["value"].(map[string]any)
		created := a.docs[name] == nil
		a.docs[name] = doc
		_ = json.NewEncoder(w).Encode(map[string]any{"skill": skill(name, false), "created": created, "newVersion": true})
	case "ListSkills":
		out := []map[string]any{{"name": "deploy_guide", "version": "platform/v3", "scope": "SKILL_SCOPE_PLATFORM", "description": "平台的部署指南"}}
		for n := range a.docs {
			out = append(out, skill(n, false))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"skills": out})
	case "GetSkill":
		if a.docs[name] == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"not_found","message":"没有这个 Skill","details":[{"type":"google.rpc.ErrorInfo","debug":{"reason":"skill_not_found","domain":"agentruntime"}}]}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"skill": skill(name, true)})
	case "RetireSkill":
		retired := a.docs[name] != nil
		delete(a.docs, name)
		version := ""
		if retired {
			version = "user/v1"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"retired": retired, "version": version})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *skillsTaskAPI) callsOf(method string) []secretCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []secretCall
	for _, c := range a.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func newSkillsServer(t *testing.T) (*client, *skillsTaskAPI) {
	t.Helper()
	up := &skillsTaskAPI{fake: &fakeTaskAPI{}}
	upstream := httptest.NewServer(up)
	t.Cleanup(upstream.Close)
	ts, _, cfg, _ := newServerWith(t, func(c *Config) { c.TaskAPI = upstream.URL })
	up.fake.policies = cfg.PoliciesFile
	c := newClient(t, ts.URL)
	login(t, c, "acme", "alice")
	return c, up
}

const skillBody = "1. 列出本周合并的改动。\n2. 按模块归类。"

func TestSkillsArePublishedListedReadAndRetiredThroughTheRuntime(t *testing.T) {
	c, up := newSkillsServer(t)
	files := []map[string]string{{"path": "templates/report.md", "media_type": "text/markdown", "content": "## 本周"}}
	code, out, raw := c.do("PUT", "/api/skills/weekly-report", map[string]any{"description": "写周报", "instructions": skillBody, "files": files})
	if code != http.StatusOK || out["created"] != true || out["new_version"] != true {
		t.Fatalf("发布：%d %s", code, raw)
	}
	put := up.callsOf("PutSkill")
	if len(put) != 1 || put[0].Body["tenantId"] != "acme" || put[0].Body["name"] != "weekly-report" || !strings.HasPrefix(put[0].Auth, "Bearer ") {
		t.Fatalf("转调 PutSkill（租户取自登录、带用户的令牌）：%+v", put)
	}
	payload, _ := put[0].Body["document"].(map[string]any)
	doc, _ := payload["value"].(map[string]any)
	if doc["description"] != "写周报" || doc["instructions"] != skillBody || len(doc["files"].([]any)) != 1 {
		t.Fatalf("正文原样转给 Runtime：%v", doc)
	}
	if _, ok := put[0].Body["userId"]; ok {
		t.Error("请求里没有用户字段")
	}

	code, out, raw = c.do("GET", "/api/skills", nil)
	if code != http.StatusOK || out["available"] != true {
		t.Fatalf("列出：%d %s", code, raw)
	}
	listed, _ := out["skills"].([]any)
	scopes := map[string]string{}
	for _, item := range listed {
		s := item.(map[string]any)
		scopes[s["name"].(string)] = s["scope"].(string)
		if _, ok := s["instructions"]; ok {
			t.Errorf("列表不带正文：%v", s)
		}
	}
	if scopes["weekly-report"] != "user" || scopes["deploy_guide"] != "platform" {
		t.Fatalf("层写成 platform、tenant、user：%v", scopes)
	}

	code, out, raw = c.do("GET", "/api/skills/weekly-report?version=user/v1", nil)
	skill, _ := out["skill"].(map[string]any)
	if code != http.StatusOK || skill["instructions"] != skillBody || skill["description"] != "写周报" || len(skill["files"].([]any)) != 1 {
		t.Fatalf("读一个（带做法与文件）：%d %s", code, raw)
	}
	if got := up.callsOf("GetSkill"); len(got) != 1 || got[0].Body["version"] != "user/v1" {
		t.Errorf("版本原样转给 Runtime：%+v", got)
	}

	code, out, raw = c.do("DELETE", "/api/skills/weekly-report", nil)
	if code != http.StatusOK || out["retired"] != true || out["version"] != "user/v1" {
		t.Fatalf("收回：%d %s", code, raw)
	}
}

func TestBadSkillRequestsAreRefusedBeforeTheRuntime(t *testing.T) {
	c, up := newSkillsServer(t)
	for _, tc := range []struct {
		path string
		body map[string]any
	}{
		{"/api/skills/Weekly", map[string]any{"description": "x", "instructions": "y"}},
		{"/api/skills/" + strings.Repeat("a", 65), map[string]any{"description": "x", "instructions": "y"}},
		{"/api/skills/ok-name", map[string]any{"description": "", "instructions": "y"}},
		{"/api/skills/ok-name", map[string]any{"description": "x", "instructions": "  "}},
	} {
		if code, _, raw := c.do("PUT", tc.path, tc.body); code != http.StatusBadRequest {
			t.Errorf("%s %v：应 400：%d %s", tc.path, tc.body, code, raw)
		}
	}
	if code, _, _ := c.do("DELETE", "/api/skills/Bad_Name", nil); code != http.StatusBadRequest {
		t.Errorf("收回时名字也核对：%d", code)
	}
	if n := len(up.callsOf("PutSkill")) + len(up.callsOf("RetireSkill")); n != 0 {
		t.Errorf("不合规的不去问 Runtime：%d 次", n)
	}
}

func TestSkillsWithoutTheRuntimeServiceSayItIsUnavailable(t *testing.T) {
	c, up := newSkillsServer(t)
	up.unavailable = true
	code, out, raw := c.do("GET", "/api/skills", nil)
	if code != http.StatusOK || out["available"] != false || len(out["skills"].([]any)) != 0 {
		t.Fatalf("Runtime 没有 SkillService 时列表回 available false：%d %s", code, raw)
	}
	if code, out, _ := c.do("PUT", "/api/skills/ok-name", map[string]any{"description": "x", "instructions": "y"}); code == http.StatusOK || !strings.Contains(out["error"].(string), "Skill") {
		t.Errorf("发布时写明不可用：%d %v", code, out)
	}
}

func TestRuntimeSkillErrorsAreTranslated(t *testing.T) {
	c, up := newSkillsServer(t)
	up.reason = "name_taken"
	_, out, _ := c.do("PUT", "/api/skills/deploy_guide", map[string]any{"description": "x", "instructions": "y"})
	if msg, _ := out["error"].(string); !strings.Contains(msg, "已经有") {
		t.Errorf("name_taken 翻成中文：%v", out)
	}
	if runtimeMsg("skill_limit") == nil || runtimeMsg("skill_too_large") == nil || runtimeMsg("duplicate_path") == nil || runtimeMsg("skill_not_found") == nil {
		t.Error("Skill 的错误码都有说法")
	}
}
