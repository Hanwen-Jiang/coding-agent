package server

// WEB-SEC（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md；environment-config.md 第 4.4 节）：网络密钥页的服务端，转调
// Runtime 的 UserSecretService（SEC-API）。
//
//   - 默认关着（D-23 第二阶段之前不算已支持）：没有 CODING_NETWORK_SECRETS=1 时三个接口都 404，不去问 Runtime；/api/config 不提它。
//   - GET /api/secrets 列出自己的密钥（名字与时间）；PUT /api/secrets/{name}（{"value"}）写入；DELETE /api/secrets/{name} 删除。
//     带这个用户现签的令牌调 Runtime，租户取自登录的身份；请求里没有用户字段，只能管自己的。
//   - 值只进不出：响应、错误信息、日志与 coding-web 的数据目录里都没有它（coding-web 不存值）。
//   - 名字与值先按契约核对（名字：字母、数字、.、_、-，最多 128；值：不能为空，UTF-8 最多 8 KiB，不能有控制字符（水平制表符除外）），
//     不合规时 400，不去问 Runtime，错误信息不带值。Runtime 的错误（例如每人 100 个的上限）原样带回。

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const secretValue = "ghp_TOP-SECRET-VALUE-0123456789"

// secretsTaskAPI 在 fakeTaskAPI 外面加上 UserSecretService：记下请求（方法、令牌、请求体），按名字存元数据（不存值）。
type secretsTaskAPI struct {
	fake  *fakeTaskAPI
	mu    sync.Mutex
	calls []secretCall
	names map[string]bool
	limit bool // PutUserSecret 回 ResourceExhausted user_secret_limit
}

type secretCall struct {
	Method, Auth string
	Body         map[string]any
}

func (a *secretsTaskAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/agentruntime.workspace.v1.UserSecretService/"
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
	if a.names == nil {
		a.names = map[string]bool{}
	}
	w.Header().Set("Content-Type", "application/json")
	name, _ := body["name"].(string)
	switch method {
	case "PutUserSecret":
		if a.limit {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"code":"resource_exhausted","message":"每个用户最多 100 个网络密钥"}`)
			return
		}
		created := !a.names[name]
		a.names[name] = true
		_ = json.NewEncoder(w).Encode(map[string]any{"secret": map[string]any{"name": name, "createdAt": "2026-11-04T08:00:00Z", "updatedAt": "2026-11-04T09:00:00Z"}, "created": created})
	case "ListUserSecrets":
		var out []map[string]any
		for n := range a.names {
			out = append(out, map[string]any{"name": n, "createdAt": "2026-11-04T08:00:00Z", "updatedAt": "2026-11-04T09:00:00Z"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": out})
	case "DeleteUserSecret":
		deleted := a.names[name]
		delete(a.names, name)
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted": deleted})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *secretsTaskAPI) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func newSecretsServer(t *testing.T, enabled bool) (*client, *secretsTaskAPI, Config) {
	t.Helper()
	up := &secretsTaskAPI{fake: &fakeTaskAPI{}}
	upstream := httptest.NewServer(up)
	t.Cleanup(upstream.Close)
	ts, _, cfg, _ := newServerWith(t, func(c *Config) {
		c.TaskAPI = upstream.URL
		c.NetworkSecrets = enabled
	})
	up.fake.policies = cfg.PoliciesFile
	c := newClient(t, ts.URL)
	login(t, c, "acme", "alice")
	return c, up, cfg
}

func TestNetworkSecretsAreOffByDefault(t *testing.T) {
	c, up, _ := newSecretsServer(t, false)
	for _, call := range []struct{ method, path string }{{"GET", "/api/secrets"}, {"PUT", "/api/secrets/token"}, {"DELETE", "/api/secrets/token"}} {
		if code, _, raw := c.do(call.method, call.path, map[string]string{"value": secretValue}); code != http.StatusNotFound {
			t.Fatalf("%s %s 关着时 404：%d %s", call.method, call.path, code, raw)
		}
	}
	if up.callCount() != 0 {
		t.Fatal("关着时不去问 Runtime")
	}
	if _, cfg, _ := c.do("GET", "/api/config", nil); cfg["network_secrets"] != nil {
		t.Fatalf("/api/config 不提它：%v", cfg)
	}
}

func TestNetworkSecretsPassThroughWithoutEchoingValues(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	c, up, cfg := newSecretsServer(t, true)
	if _, conf, _ := c.do("GET", "/api/config", nil); conf["network_secrets"] != true {
		t.Fatalf("打开时 /api/config 告诉页面：%v", conf)
	}
	code, out, raw := c.do("PUT", "/api/secrets/github-token", map[string]string{"value": secretValue})
	if code != http.StatusOK || out["created"] != true || strings.Contains(raw, secretValue) {
		t.Fatalf("写入：%d %s", code, raw)
	}
	if secret, _ := out["secret"].(map[string]any); secret["name"] != "github-token" || secret["updated_at"] == nil {
		t.Fatalf("回元数据：%s", raw)
	}
	up.mu.Lock()
	put := up.calls[len(up.calls)-1]
	up.mu.Unlock()
	if put.Method != "PutUserSecret" || put.Body["tenantId"] != "acme" || put.Body["name"] != "github-token" || put.Body["value"] != secretValue {
		t.Fatalf("转调 PutUserSecret：%+v", put)
	}
	if !strings.HasPrefix(put.Auth, "Bearer ") || len(put.Auth) < 20 {
		t.Fatalf("带这个用户现签的令牌：%q", put.Auth)
	}
	if _, ok := put.Body["user"]; ok {
		t.Fatal("请求里没有用户字段（Runtime 按令牌认人）")
	}
	if code, out, raw := c.do("PUT", "/api/secrets/github-token", map[string]string{"value": secretValue + "-2"}); code != http.StatusOK || out["created"] != false {
		t.Fatalf("同名的是替换：%d %s", code, raw)
	}
	code, out, raw = c.do("GET", "/api/secrets", nil)
	list, _ := out["secrets"].([]any)
	if code != http.StatusOK || len(list) != 1 || strings.Contains(raw, secretValue) {
		t.Fatalf("列出：%d %s", code, raw)
	}
	if first, _ := list[0].(map[string]any); first["name"] != "github-token" || first["created_at"] == nil {
		t.Fatalf("列出的是名字与时间：%s", raw)
	}
	if code, out, raw := c.do("DELETE", "/api/secrets/github-token", nil); code != http.StatusOK || out["deleted"] != true {
		t.Fatalf("删除：%d %s", code, raw)
	}
	if code, out, _ := c.do("DELETE", "/api/secrets/github-token", nil); code != http.StatusOK || out["deleted"] != false {
		t.Fatalf("删除是幂等的：%d %v", code, out)
	}
	if strings.Contains(logs.String(), secretValue) {
		t.Fatal("日志里没有值")
	}
	_ = filepath.WalkDir(cfg.DataDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(path); bytes.Contains(data, []byte(secretValue)) {
				t.Errorf("coding-web 不存值：%s", path)
			}
		}
		return nil
	})
}

func TestNetworkSecretNamesAndValuesAreChecked(t *testing.T) {
	c, up, _ := newSecretsServer(t, true)
	bad := []struct{ name, value string }{
		{"has space", secretValue},
		{strings.Repeat("a", 129), secretValue},
		{"ok", ""},
		{"ok", "line1\nline2-" + secretValue},
		{"ok", strings.Repeat("x", 8193)},
	}
	for _, b := range bad {
		code, _, raw := c.do("PUT", "/api/secrets/"+strings.ReplaceAll(b.name, " ", "%20"), map[string]string{"value": b.value})
		if code != http.StatusBadRequest || strings.Contains(raw, secretValue) {
			t.Fatalf("%q：不合规时 400，错误信息不带值：%d %s", b.name, code, raw)
		}
	}
	if code, _, _ := c.do("PUT", "/api/secrets/ok", map[string]string{"value": "tab\tis-fine"}); code != http.StatusOK {
		t.Fatalf("水平制表符可以：%d", code)
	}
	if code, _, _ := c.do("DELETE", "/api/secrets/has%20space", nil); code != http.StatusBadRequest {
		t.Fatalf("删除也核对名字：%d", code)
	}
	if n := up.callCount(); n != 1 {
		t.Fatalf("不合规的不去问 Runtime：%d 次", n)
	}
	up.mu.Lock()
	up.limit = true
	up.mu.Unlock()
	if code, _, raw := c.do("PUT", "/api/secrets/one-more", map[string]string{"value": secretValue}); code < 400 || !strings.Contains(raw, "100") || strings.Contains(raw, secretValue) {
		t.Fatalf("Runtime 的错误原样带回：%d %s", code, raw)
	}
}
