package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// recordingHandoff 记下交给 Runtime 的输入（ENV-02 的接缝）。
type recordingHandoff struct {
	mu     sync.Mutex
	inputs []EnvironmentHandoffInput
}

func (h *recordingHandoff) Bind(_ context.Context, _ identity, _ Session, in EnvironmentHandoffInput) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inputs = append(h.inputs, in)
	return handoffPending, nil
}

func (h *recordingHandoff) last(t *testing.T) EnvironmentHandoffInput {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.inputs) == 0 {
		t.Fatal("接缝没有被调用")
	}
	return h.inputs[len(h.inputs)-1]
}

// 2026-11-03 人定：会话开出时记下环境配置的快照（不含密钥），之后只用快照；改配置不影响已开的会话；
// 删除不要求先改绑，删除时密钥立即删掉；交给 Runtime 的是快照，配置还在时再加上密钥。
func TestSessionsKeepASnapshotOfTheirEnvironment(t *testing.T) {
	ts, _, cfg, srv := newServerWith(t, nil)
	handoff := &recordingHandoff{}
	srv.handoff = handoff
	c := newClient(t, ts.URL)
	pid := sampleProject(t, c)

	code, env, raw := c.do("POST", "/api/environments", map[string]any{
		"name": "Python", "setup_script": "pip install -r requirements.txt", "start_command": "python -m app", "env": "MODE=dev",
		"network": map[string]any{"mode": "allowlist", "allowed_hosts": []string{"pypi.org"}},
		"secrets": []map[string]string{{"name": "OPENAI_API_KEY", "value": "sk-very-secret-1"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("建环境：%d %s", code, raw)
	}
	envID := env["id"].(string)
	// 密钥只写不读：响应里只有名字。
	if strings.Contains(raw, "sk-very-secret") || !strings.Contains(raw, `"OPENAI_API_KEY"`) || env["version"].(float64) != 1 {
		t.Fatalf("响应不能带密钥的值：%s", raw)
	}
	data, _ := os.ReadFile(filepath.Join(cfg.DataDir, "acme", "environments.json"))
	if strings.Contains(string(data), "sk-very-secret") {
		t.Fatal("密钥要加密存放")
	}

	code, sess, raw := c.do("POST", "/api/projects/"+pid+"/sessions", map[string]string{"title": "one", "environment_id": envID})
	if code != http.StatusCreated || strings.Contains(raw, "pip install") {
		t.Fatalf("开会话（会话的 JSON 不带快照的内容，同租户别人也能列出会话）：%d %s", code, raw)
	}
	in := handoff.last(t)
	if in.Snapshot.EnvironmentID != envID || in.Snapshot.Version != 1 || in.Snapshot.SetupScript != "pip install -r requirements.txt" ||
		in.Snapshot.StartCommand != "python -m app" || in.Snapshot.Env != "MODE=dev" || in.Snapshot.Network.Mode != networkAllowlist ||
		strings.Join(in.Snapshot.SecretNames, ",") != "OPENAI_API_KEY" || in.Secrets["OPENAI_API_KEY"] != "sk-very-secret-1" {
		t.Fatalf("交出去的是快照加上（配置还在时的）密钥：%#v", in)
	}
	snap, _ := json.Marshal(in.Snapshot)
	if strings.Contains(string(snap), "sk-very-secret") {
		t.Fatal("快照不含密钥")
	}

	// 改配置：已开的会话仍用快照；轮换后的密钥按名字取新的值。
	code, edited, raw := c.do("PUT", "/api/environments/"+envID, map[string]any{
		"name": "Python", "setup_script": "uv sync", "env": "MODE=prod",
		"secrets": []map[string]string{{"name": "OPENAI_API_KEY", "value": "sk-very-secret-2"}, {"name": "EXTRA_TOKEN", "value": "x-added"}},
	})
	if code != http.StatusOK || edited["version"].(float64) != 2 || strings.Contains(raw, "sk-very-secret") {
		t.Fatalf("改配置：%d %s", code, raw)
	}
	got, ok, err := srv.environmentHandoffInput("acme", Session{ProjectID: pid, ID: sess["id"].(string), WorkspaceID: sess["workspace_id"].(string), CreatedBy: "alice", EnvironmentID: envID})
	if err != nil || !ok || got.Snapshot.SetupScript != "pip install -r requirements.txt" || got.Snapshot.Env != "MODE=dev" || got.Snapshot.Version != 1 ||
		got.Secrets["OPENAI_API_KEY"] != "sk-very-secret-2" || got.Secrets["EXTRA_TOKEN"] != "" {
		t.Fatalf("改了配置之后会话仍用快照（密钥只取快照里有的名字）：%#v %v %v", got, ok, err)
	}
	// 只改名字不给值：保留原来的值；去掉的名字删掉。
	if code, _, raw := c.do("PUT", "/api/environments/"+envID, map[string]any{"name": "Python", "secrets": []map[string]string{{"name": "OPENAI_API_KEY"}}}); code != http.StatusOK {
		t.Fatalf("保留密钥：%d %s", code, raw)
	}
	if got, _, _ := srv.environmentHandoffInput("acme", Session{ProjectID: pid, ID: sess["id"].(string), WorkspaceID: sess["workspace_id"].(string), CreatedBy: "alice", EnvironmentID: envID}); got.Secrets["OPENAI_API_KEY"] != "sk-very-secret-2" {
		t.Fatalf("没给值的密钥保留原来的值：%#v", got.Secrets)
	}
	// 不带 secrets 的旧客户端：密钥不动。
	if code, _, raw := c.do("PUT", "/api/environments/"+envID, map[string]any{"name": "Python"}); code != http.StatusOK {
		t.Fatalf("不带 secrets：%d %s", code, raw)
	}
	if e, _, _ := srv.store.Environment("acme", envID); len(e.Secrets) != 1 {
		t.Fatalf("不带 secrets 的请求不动密钥：%#v", e.Secrets)
	}

	// 删除：不要求先改绑；配置从列表里消失，密钥立即删掉；会话保留快照，从此没有密钥。
	if code, _, raw := c.do("DELETE", "/api/environments/"+envID, nil); code != http.StatusOK {
		t.Fatalf("删除：%d %s", code, raw)
	}
	tomb, found, _ := srv.store.Environment("acme", envID)
	if !found || tomb.DeletedAt == nil || len(tomb.Secrets) != 0 {
		t.Fatalf("墓碑不再有密钥：%#v", tomb)
	}
	if _, list, _ := c.doList("GET", "/api/environments", nil); len(list) != 0 {
		t.Fatalf("删掉的不在列表里：%v", list)
	}
	got, ok, err = srv.environmentHandoffInput("acme", Session{ProjectID: pid, ID: sess["id"].(string), WorkspaceID: sess["workspace_id"].(string), CreatedBy: "alice", EnvironmentID: envID})
	if err != nil || !ok || got.Snapshot.SetupScript != "pip install -r requirements.txt" || len(got.Secrets) != 0 || !got.ConfigDeleted {
		t.Fatalf("删除之后：快照照用、没有密钥：%#v %v %v", got, ok, err)
	}
	_, envView, raw := c.do("GET", "/api/projects/"+pid+"/sessions/"+sess["id"].(string)+"/env", nil)
	config, _ := envView["environment"].(map[string]any)
	if config["name"] != "Python" || config["deleted"] != true || config["snapshot"] != true || strings.Contains(raw, "sk-very-secret") {
		t.Fatalf("右栏：删掉的环境，会话用创建时的设置：%s", raw)
	}
	// 删掉的环境不能再选给新会话。
	if code, _, _ := c.do("POST", "/api/projects/"+pid+"/sessions", map[string]string{"title": "two", "environment_id": envID}); code != http.StatusBadRequest {
		t.Fatalf("删掉的环境不能再选：%d", code)
	}
	// 复制会话沿用快照（不是活的配置），配置删了就没有密钥。
	code, dup, raw := c.do("POST", "/api/projects/"+pid+"/sessions/"+sess["id"].(string)+"/duplicate", map[string]string{"title": "copy"})
	if code != http.StatusCreated || dup["environment_id"] != envID {
		t.Fatalf("复制会话：%d %s", code, raw)
	}
	if in := handoff.last(t); in.Snapshot.SetupScript != "pip install -r requirements.txt" || len(in.Secrets) != 0 {
		t.Fatalf("副本交出去的是原会话的快照、没有密钥：%#v", in)
	}
	// 删会话连同它的快照。
	if code, _, raw := c.do("DELETE", "/api/projects/"+pid+"/sessions/"+dup["id"].(string), nil); code != http.StatusOK {
		t.Fatalf("删会话：%d %s", code, raw)
	}
	if _, err := os.Stat(srv.store.snapshotFile("acme", dup["workspace_id"].(string))); !os.IsNotExist(err) {
		t.Fatalf("删会话时快照也删：%v", err)
	}
}

func TestEnvironmentSecretValidation(t *testing.T) {
	ts, _, _, _ := newServerWith(t, nil)
	c := newClient(t, ts.URL)
	c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	for _, tc := range []struct {
		name    string
		secrets []map[string]string
		env     string
	}{
		{"bad name", []map[string]string{{"name": "A-B", "value": "x"}}, ""},
		{"duplicate", []map[string]string{{"name": "A", "value": "x"}, {"name": "A", "value": "y"}}, ""},
		{"same as .env", []map[string]string{{"name": "MODE", "value": "x"}}, "MODE=dev"},
		{"new without value", []map[string]string{{"name": "NEW_KEY"}}, ""},
		{"too long", []map[string]string{{"name": "BIG", "value": strings.Repeat("x", maxEnvironmentSecretBytes+1)}}, ""},
		{"nul", []map[string]string{{"name": "Z", "value": "a\x00b"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, raw := c.do("POST", "/api/environments", map[string]any{"name": "x-" + tc.name, "env": tc.env, "secrets": tc.secrets})
			if code != http.StatusBadRequest || strings.Contains(raw, "a\x00b") {
				t.Fatalf("%d %s", code, raw)
			}
		})
	}
	// 普通 .env 里像机密的名字：提示放到“密钥”里。
	code, _, raw := c.do("POST", "/api/environments", map[string]any{"name": "y", "env": "API_TOKEN=abc"})
	if code != http.StatusBadRequest || !strings.Contains(raw, "密钥") || strings.Contains(raw, "abc") {
		t.Fatalf(".env 里的机密：%d %s", code, raw)
	}
}
