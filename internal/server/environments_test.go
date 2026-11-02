package server

import (
	"encoding/json"
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
	if code, _, raw := c.do("PUT", "/api/environments/"+envID, map[string]string{"name": "Python 3", "env": "MODE=prod"}); code != http.StatusOK {
		t.Fatalf("update environment: %d %s", code, raw)
	}
	if code, _, raw := c.do("DELETE", "/api/environments/"+envID, nil); code != http.StatusOK {
		t.Fatalf("delete environment: %d %s", code, raw)
	}
	if code, _, _ := c.do("GET", "/api/environments/"+envID, nil); code != http.StatusNotFound {
		t.Fatalf("deleted environment should be hidden, got %d", code)
	}
}
