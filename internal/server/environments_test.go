package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNormalizeEnvironmentInput(t *testing.T) {
	in, err := normalizeEnvironmentInput(environmentInput{
		Name: " Python ", Description: "local tools", SetupScript: "apt-get update",
		StartCommand: "python -m http.server", Env: "MODE=dev\n# comment\nPORT=8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "Python" || in.Privacy != onlyMePrivacy {
		t.Fatalf("normalized config = %#v", in)
	}
	for _, tc := range []struct {
		name string
		in   environmentInput
	}{
		{"missing name", environmentInput{Env: "MODE=dev"}},
		{"secret key", environmentInput{Name: "x", Env: "API_TOKEN=do-not-store"}},
		{"duplicate key", environmentInput{Name: "x", Env: "A=1\nA=2"}},
		{"bad key", environmentInput{Name: "x", Env: "A-B=1"}},
		{"bad privacy", environmentInput{Name: "x", Privacy: "tenant"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeEnvironmentInput(tc.in); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	tooLong := environmentInput{Name: "x", Description: string(make([]byte, maxEnvironmentDescriptionBytes+1))}
	if _, err := normalizeEnvironmentInput(tooLong); err == nil {
		t.Fatal("expected description length validation")
	}
}

// .env 的上限与 Runtime 的 SetWorkspaceEnvironment 一致（16 KiB、64 个变量；task-service 的 workspaceapi.checkDotEnv）：
// 这边放过、Runtime 拒绝（env_too_large）的配置在开会话时才报错，所以保存时就挡住。
func TestEnvironmentDotEnvLimitsMatchTheRuntime(t *testing.T) {
	vars := func(n int) string {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = fmt.Sprintf("V%d=1", i)
		}
		return strings.Join(lines, "\n")
	}
	if _, err := normalizeEnvironmentInput(environmentInput{Name: "x", Env: vars(64)}); err != nil {
		t.Fatalf("64 个变量要收：%v", err)
	}
	if _, err := normalizeEnvironmentInput(environmentInput{Name: "x", Env: "A=" + strings.Repeat("x", 16<<10-2)}); err != nil {
		t.Fatalf("正好 16 KiB 要收：%v", err)
	}
	for _, tc := range []struct {
		env, want string
	}{
		{vars(65), "most 64 variables"},
		{"A=" + strings.Repeat("x", 16<<10-1), "16384 bytes"},
	} {
		_, err := normalizeEnvironmentInput(environmentInput{Name: "x", Env: tc.env})
		if err == nil || !strings.Contains(msg("环境配置不合法：%v", err).In(langEN), tc.want) {
			t.Errorf("got %v, want %q", err, tc.want)
		}
	}
}

// 联网需求（人工验收 Q29、Q30）：三档模式，受限列表的域名可以用 *.example.com 匹配子域名；写法不对的整份拒绝，不静默丢掉。
func TestEnvironmentNetworkIntent(t *testing.T) {
	in, err := normalizeEnvironmentInput(environmentInput{Name: "x"})
	if err != nil || in.Network.Mode != networkDeny || len(in.Network.AllowedHosts) != 0 {
		t.Fatalf("默认禁止联网：%#v %v", in.Network, err)
	}
	in, err = normalizeEnvironmentInput(environmentInput{Name: "x", Network: EnvironmentNetwork{
		Mode: "Allowlist", AllowedHosts: []string{" PyPI.org. ", "*.githubusercontent.com", "", "pypi.org"},
	}})
	if err != nil || in.Network.Mode != networkAllowlist || strings.Join(in.Network.AllowedHosts, ",") != "pypi.org,*.githubusercontent.com" {
		t.Fatalf("受限列表：%#v %v", in.Network, err)
	}
	if in, err = normalizeEnvironmentInput(environmentInput{Name: "x", Network: EnvironmentNetwork{Mode: "open"}}); err != nil || in.Network.Mode != networkOpen {
		t.Fatalf("完全开放只是需求，能不能用由 Runtime 按租户策略定：%#v %v", in.Network, err)
	}
	for _, tc := range []struct {
		name    string
		network EnvironmentNetwork
		want    string
	}{
		{"unknown mode", EnvironmentNetwork{Mode: "proxy"}, "deny, allowlist or open"},
		{"empty allowlist", EnvironmentNetwork{Mode: "allowlist"}, "at least one domain"},
		{"hosts without allowlist", EnvironmentNetwork{Mode: "deny", AllowedHosts: []string{"pypi.org"}}, "only apply to the allowlist"},
		{"bare wildcard", EnvironmentNetwork{Mode: "allowlist", AllowedHosts: []string{"*"}}, "without scheme, port, path or wildcard"},
		{"url", EnvironmentNetwork{Mode: "allowlist", AllowedHosts: []string{"https://pypi.org/simple"}}, "without scheme"},
		{"port", EnvironmentNetwork{Mode: "allowlist", AllowedHosts: []string{"pypi.org:443"}}, "without scheme"},
		{"ip", EnvironmentNetwork{Mode: "allowlist", AllowedHosts: []string{"169.254.169.254"}}, "IP address"},
		{"single label", EnvironmentNetwork{Mode: "allowlist", AllowedHosts: []string{"localhost"}}, "isn't a public domain"},
		{"numeric tld", EnvironmentNetwork{Mode: "allowlist", AllowedHosts: []string{"10.0.0"}}, "isn't a public domain"},
		{"bad label", EnvironmentNetwork{Mode: "allowlist", AllowedHosts: []string{"-bad.example.com"}}, "isn't a public domain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeEnvironmentInput(environmentInput{Name: "x", Network: tc.network})
			if err == nil || !strings.Contains(msg("环境配置不合法：%v", err).In(langEN), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	many := make([]string, maxEnvironmentHosts+1)
	for i := range many {
		many[i] = fmt.Sprintf("h%d.example.com", i)
	}
	if _, err := normalizeEnvironmentInput(environmentInput{Name: "x", Network: EnvironmentNetwork{Mode: "allowlist", AllowedHosts: many}}); err == nil {
		t.Fatal("域名太多要拒绝")
	}
}

func TestEnvironmentValidationMessagesInEnglish(t *testing.T) {
	for _, tc := range []struct {
		in   environmentInput
		want string
	}{
		{environmentInput{Name: "x", Env: "BAD-NAME=value"}, "line 1 must be KEY=value"},
		{environmentInput{Name: "x", Env: "API_TOKEN=private"}, "may contain a secret"},
		{environmentInput{Name: "x", SetupScript: strings.Repeat("中", 21846)}, "65536 bytes"},
	} {
		_, err := normalizeEnvironmentInput(tc.in)
		if err == nil || !strings.Contains(msg("环境配置不合法：%v", err).In(langEN), tc.want) {
			t.Errorf("English validation: got %v, want %q", err, tc.want)
		}
	}
}

func TestEnvironmentStoreTombstone(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	env := Environment{ID: "env-python-abcd", Name: "Python", Privacy: onlyMePrivacy, CreatedAt: now, CreatedBy: "alice"}
	if err := store.UpdateEnvironments("acme", func(all []Environment) ([]Environment, error) {
		return append(all, env), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteEnvironment("acme", env.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Environment("acme", env.ID)
	if err != nil || !ok || got.DeletedAt == nil {
		t.Fatalf("tombstone = %#v, ok=%v, err=%v", got, ok, err)
	}
}

func TestEnvironmentCRUDAndSessionBinding(t *testing.T) {
	ts, _, _ := newServer(t)
	c := newClient(t, ts.URL)
	if code, _, raw := c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"}); code != http.StatusOK {
		t.Fatalf("login: %d %s", code, raw)
	}
	code, env, raw := c.do("POST", "/api/environments", map[string]string{
		"name": "Python", "description": "tooling", "setup_script": "python -m pip install -r requirements.txt",
		"start_command": "python -m app", "env": "MODE=dev",
	})
	if code != http.StatusCreated {
		t.Fatalf("create environment: %d %s", code, raw)
	}
	envID := env["id"].(string)
	if env["privacy"] != onlyMePrivacy || env["name"] != "Python" {
		t.Fatalf("created environment: %#v", env)
	}
	res, err := c.http.Get(c.base + "/api/environments")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var list []Environment
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&list) != nil || len(list) != 1 {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("list environments: %d %s", res.StatusCode, raw)
	}
	if code, _, raw := c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "bob"}); code != http.StatusOK {
		t.Fatalf("switch user: %d %s", code, raw)
	}
	if code, _, _ := c.do("GET", "/api/environments/"+envID, nil); code != http.StatusNotFound {
		t.Fatalf("only_me environment should be hidden from another user, got %d", code)
	}
	if code, _, raw := c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"}); code != http.StatusOK {
		t.Fatalf("switch back: %d %s", code, raw)
	}
	code, project, raw := c.do("POST", "/api/projects", map[string]string{"name": "Demo", "sample": "ci_rounding"})
	if code != http.StatusCreated {
		t.Fatalf("create project: %d %s", code, raw)
	}
	pid := project["id"].(string)
	code, sess, raw := c.do("POST", "/api/projects/"+pid+"/sessions", map[string]string{"title": "run", "environment_id": envID})
	if code != http.StatusCreated || sess["environment_id"] != envID {
		t.Fatalf("create session with environment: %d %s %#v", code, raw, sess)
	}
	if env["network"].(map[string]any)["mode"] != networkDeny {
		t.Fatalf("network defaults to deny: %#v", env["network"])
	}
	code, updated, raw := c.do("PUT", "/api/environments/"+envID, map[string]any{"name": "Python 3", "env": "MODE=prod",
		"network": map[string]any{"mode": "allowlist", "allowed_hosts": []string{"pypi.org", "*.pythonhosted.org"}}})
	if code != http.StatusOK {
		t.Fatalf("update environment: %d %s", code, raw)
	}
	if n := updated["network"].(map[string]any); n["mode"] != networkAllowlist || len(n["allowed_hosts"].([]any)) != 2 {
		t.Fatalf("network intent round-trip: %s", raw)
	}
	if code, _, raw := c.do("DELETE", "/api/environments/"+envID, nil); code != http.StatusOK {
		t.Fatalf("delete environment: %d %s", code, raw)
	}
	if code, _, _ := c.do("GET", "/api/environments/"+envID, nil); code != http.StatusNotFound {
		t.Fatalf("deleted environment should be hidden, got %d", code)
	}
}
