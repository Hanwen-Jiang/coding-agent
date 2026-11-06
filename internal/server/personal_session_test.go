package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPersonalSessionReferencesArePrivateAndCopiedOnlyForTheirOwner(t *testing.T) {
	ts, tasks, _, s := newServerWith(t, nil)
	alice, bob := newClient(t, ts.URL), newClient(t, ts.URL)
	alice.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	bob.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "bob"})
	code, p, raw := alice.do("POST", "/api/projects", map[string]string{"name": "private refs"})
	if code != 201 {
		t.Fatalf("project: %d %s", code, raw)
	}
	base := "/api/projects/" + p["id"].(string) + "/sessions"
	code, sess, raw := alice.do("POST", base, map[string]any{"title": "test", "personal_config": map[string]any{"skill_names": []string{}, "mcp_connection_ids": []string{"alice-mcp"}}})
	if code != 201 {
		t.Fatalf("session: %d %s", code, raw)
	}
	id := sess["id"].(string)
	personal := func(c *client) map[string]any {
		_, _, raw := c.do("GET", "/api/sessions", nil)
		var sessions []map[string]any
		if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
			t.Fatal(err)
		}
		for _, item := range sessions {
			if item["id"] == id {
				selection, _ := item["personal_config"].(map[string]any)
				return selection
			}
		}
		t.Fatal("missing session")
		return nil
	}
	if selection := personal(alice); selection == nil || len(selection["skill_names"].([]any)) != 0 {
		t.Fatalf("explicit empty lost: %v", selection)
	}
	if selection := personal(bob); selection != nil {
		t.Fatalf("private references leaked: %v", selection)
	}
	data, err := os.ReadFile(s.store.sessionsFile("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "alice-mcp") || strings.Contains(string(data), "personal_config") {
		t.Fatal("personal references stored in shared sessions")
	}
	code, _, raw = bob.do("POST", base+"/"+id+"/tasks", map[string]string{"message": "use my defaults"})
	if code != 201 && code != 200 {
		t.Fatalf("task: %d %s", code, raw)
	}
	if _, present := tasks.submits[len(tasks.submits)-1]["inputs"].(map[string]any)["personal_config"]; present {
		t.Fatal("Bob inherited Alice's override")
	}
	for _, c := range []*client{alice, bob} {
		code, copy, raw := c.do("POST", base+"/"+id+"/duplicate", map[string]string{"title": "copy"})
		if code != 201 {
			t.Fatalf("copy: %d %s", code, raw)
		}
		_, has := copy["personal_config"]
		if has != (c == alice) {
			t.Fatalf("copied wrong user's references: %v", copy)
		}
	}
}

func TestWorkspaceHTTPRecoveryPreservesPersonalSelectionAndShortWorkspaceID(t *testing.T) {
	remote := &workspaceRemote{started: map[string]map[string]any{}, interrupt: true}
	dir := t.TempDir()
	first := newWorkspaceHTTPServer(t, dir, remote)
	owner := workspaceOwner()
	project := Project{ID: strings.Repeat("p", 60), Name: "long", CreatedBy: owner.UserID}
	if err := first.store.UpdateProjects(owner.TenantID, func([]Project) ([]Project, error) { return []Project{project}, nil }); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/sessions", strings.NewReader(`{"title":"test","personal_config":{"skill_names":[],"mcp_connection_ids":[]}}`))
	request.SetPathValue("project", project.ID)
	request.Header.Set("X-Coding-Async", "1")
	request.Header.Set("Idempotency-Key", "personal-recovery-1")
	out := httptest.NewRecorder()
	first.workspaceHTTP("create_session")(out, request, owner)
	if out.Code != 202 {
		t.Fatal(out.Body)
	}
	var admission map[string]string
	_ = json.Unmarshal(out.Body.Bytes(), &admission)
	first.background.Wait()
	remote.mu.Lock()
	remote.ready = true
	remote.mu.Unlock()
	second := newWorkspaceHTTPServer(t, dir, remote)
	result := waitWorkspaceStatus(t, second, owner, admission["operation_id"], "COMPLETED")
	second.background.Wait()
	data, _ := json.Marshal(result["result"])
	var created Session
	_ = json.Unmarshal(data, &created)
	if !slug.MatchString(created.WorkspaceID) || len(created.WorkspaceID) > 63 {
		t.Fatalf("invalid workspace id: %+v", created)
	}
	if created.PersonalConfig == nil || created.PersonalConfig.Skills == nil || len(*created.PersonalConfig.Skills) != 0 {
		t.Fatalf("explicit empty lost: %s", data)
	}
	saved, err := second.store.PersonalSession(owner.TenantID, owner.UserID, created.WorkspaceID)
	if err != nil || saved == nil || saved.MCP == nil || len(*saved.MCP) != 0 {
		t.Fatalf("private selection missing: %+v %v", saved, err)
	}
	path := second.store.workspaceOperationFile(owner.TenantID, admission["operation_id"])
	rec, _ := readJSONFile[*workspaceHTTPRecord](path)
	rec.Status = 0
	rec.Response = nil
	if err := writeFileAtomic(path, rec); err != nil {
		t.Fatal(err)
	}
	third := newWorkspaceHTTPServer(t, dir, remote)
	waitWorkspaceStatus(t, third, owner, rec.ID, "COMPLETED")
	third.background.Wait()
	sessions, _ := third.store.Sessions(owner.TenantID)
	if len(sessions) != 1 || sessions[0].PersonalConfig != nil {
		t.Fatalf("duplicate/shared selection: %v", sessions)
	}
}

func TestWorkspaceHTTPStalePollCannotOverwriteTerminalState(t *testing.T) {
	remote := &workspaceRemote{started: map[string]map[string]any{}, ready: true}
	s := newWorkspaceHTTPServer(t, t.TempDir(), remote)
	id := workspaceOwner()
	record := &workspaceHTTPRecord{ID: "who-" + strings.Repeat("a", 64), User: id.UserID, Kind: "create_project", Method: "POST", URL: "/api/projects", Body: json.RawMessage(`{"name":"repeat"}`), Facts: map[string]json.RawMessage{}}
	stale := *record
	record.Status = 201
	record.Response = json.RawMessage(`{"id":"already-created"}`)
	if err := writeFileAtomic(s.store.workspaceOperationFile(id.TenantID, record.ID), record); err != nil {
		t.Fatal(err)
	}
	s.resumeWorkspaceHTTP(&stale, id)
	s.background.Wait()
	loaded, _ := readJSONFile[*workspaceHTTPRecord](s.store.workspaceOperationFile(id.TenantID, record.ID))
	var response map[string]string
	_ = json.Unmarshal(loaded.Response, &response)
	if loaded.Status != 201 || response["id"] != "already-created" || remote.starts != 0 {
		t.Fatalf("stale state replayed: %+v starts=%d", loaded, remote.starts)
	}
}

func TestWorkspaceHTTPBackgroundDoesNotExtendBrowserIdleExpiry(t *testing.T) {
	now := time.Now()
	sessions := newSessions(Config{DataDir: t.TempDir(), SessionIdle: time.Minute, Now: func() time.Time { return now }})
	id, err := sessions.create("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Second)
	if _, ok, err := sessions.lookupWithoutActivity(id); err != nil || !ok {
		t.Fatal(ok, err)
	}
	now = now.Add(20 * time.Second)
	if _, ok, err := sessions.lookupWithoutActivity(id); err != nil || ok {
		t.Fatal("background refreshed browser activity", ok, err)
	}
}
