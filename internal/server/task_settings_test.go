package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestSessionTaskSettingsPersistPerUserAndSessionAndMintEachSubmission(t *testing.T) {
	ts, tasks, _, app := newServerWith(t, nil)
	alice, bob, other := newClient(t, ts.URL), newClient(t, ts.URL), newClient(t, ts.URL)
	for _, v := range []struct {
		c            *client
		tenant, user string
	}{{alice, "acme", "alice"}, {bob, "acme", "bob"}, {other, "other", "alice"}} {
		v.c.do("POST", "/api/login", map[string]string{"tenant": v.tenant, "user": v.user})
	}
	_, p, _ := alice.do("POST", "/api/projects", map[string]string{"name": "Session preferences"})
	base := "/api/projects/" + p["id"].(string) + "/sessions"
	settings := map[string]any{"sandbox": "read", "network_request": map[string]any{"scope": "public", "minutes": 0, "unlimited": true, "reason": "Look up docs"}}
	code, a, raw := alice.do("POST", base, map[string]any{"title": "A", "task_settings": settings})
	if code != 201 {
		t.Fatal(code, raw)
	}
	_, b, _ := alice.do("POST", base, map[string]any{"title": "B"})
	aURL, bURL := base+"/"+a["id"].(string), base+"/"+b["id"].(string)
	if len(tasks.submits) != 0 {
		t.Fatal("saving settings started a Task")
	}
	for _, v := range []struct {
		c            *client
		url, sandbox string
		net          bool
	}{{alice, aURL, "read", true}, {alice, bURL, defaultSandbox, false}, {bob, aURL, defaultSandbox, false}} {
		code, got, raw := v.c.do("GET", v.url+"/settings", nil)
		if code != 200 || got["sandbox"] != v.sandbox || (got["network_request"] != nil) != v.net {
			t.Fatal(code, raw)
		}
	}
	if code, _, _ := other.do("GET", aURL+"/settings", nil); code != 404 {
		t.Fatalf("cross tenant %d", code)
	}
	// A second browser/device and a reopened Store get the persisted owner-specific value.
	device := newClient(t, ts.URL)
	device.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	_, got, _ := device.do("GET", aURL+"/settings", nil)
	if got["sandbox"] != "read" || got["network_request"] == nil {
		t.Fatal(got)
	}
	reopened := NewStore(app.store.dir)
	stored, err := reopened.TaskSettings("acme", "alice", a["workspace_id"].(string))
	if err != nil || stored.Sandbox != "read" || stored.Network == nil || !stored.Network.Unlimited {
		t.Fatal(stored, err)
	}
	for i := 0; i < 2; i++ {
		code, _, raw := alice.do("POST", aURL+"/tasks", map[string]any{"message": "Check docs"})
		if code != 201 {
			t.Fatal(code, raw)
		}
		req := tasks.submits[len(tasks.submits)-1]
		if req["effectCeiling"] != "EFFECT_READ" || req["networkRequest"].(map[string]any)["unlimited"] != true {
			t.Fatal(req)
		}
	}
	if tasks.submits[0]["clientRequestId"] == tasks.submits[1]["clientRequestId"] {
		t.Fatal("reused Task grant identity")
	}
	// Copying a session copies only the caller's own saved preferences.
	for _, owner := range []*client{alice, bob} {
		code, copied, raw := owner.do("POST", aURL+"/duplicate", map[string]any{"title": "Copy"})
		if code != 201 {
			t.Fatal(code, raw)
		}
		pref := copied["task_settings"].(map[string]any)
		if (pref["network_request"] != nil) != (owner == alice) {
			t.Fatal("copied another user settings", pref)
		}
	}
	// Explicit objects replace saved fields; an empty object cannot inherit public access.
	beforeEmpty := len(tasks.submits)
	if code, _, raw := alice.do("POST", aURL+"/tasks", map[string]any{"message": "invalid override", "network_request": map[string]any{}}); code != 400 || len(tasks.submits) != beforeEmpty {
		t.Fatal(code, raw)
	}
	// Explicit request null must never fall back to the saved public choice.
	code, _, raw = alice.do("POST", aURL+"/tasks", map[string]any{"message": "Offline request", "network_request": nil})
	if code != 201 {
		t.Fatal(code, raw)
	}
	if _, ok := tasks.submits[len(tasks.submits)-1]["networkRequest"]; ok {
		t.Fatal("explicit offline lost")
	}
	// Persisting off clears the setting for subsequent omitted request fields too.
	code, _, raw = alice.do("PUT", aURL+"/settings", map[string]any{"sandbox": "write_reversible", "network_request": nil})
	if code != 200 {
		t.Fatal(code, raw)
	}
	alice.do("POST", aURL+"/tasks", map[string]any{"message": "Still offline"})
	if _, ok := tasks.submits[len(tasks.submits)-1]["networkRequest"]; ok {
		t.Fatal("saved offline fell back")
	}
	data, err := os.ReadFile(app.store.sessionsFile("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "task_settings") || strings.Contains(string(data), "Look up docs") {
		t.Fatal("private settings leaked into shared session file")
	}
	// Invalid writes preserve the prior value and never reach Runtime.
	before := len(tasks.submits)
	for _, bad := range []map[string]any{{"sandbox": "write_irreversible"}, {"sandbox": "read", "network_request": map[string]any{"scope": "public", "minutes": 0}}} {
		if code, _, raw := alice.do("PUT", aURL+"/settings", bad); code != http.StatusBadRequest {
			t.Fatal(code, raw)
		}
	}
	if len(tasks.submits) != before {
		t.Fatal("settings validation called Runtime")
	}
	_, got, _ = alice.do("GET", aURL+"/settings", nil)
	if got["sandbox"] != defaultSandbox || got["network_request"] != nil {
		t.Fatal(got)
	}
	// List responses used on refresh hydrate only the logged-in user's values.
	_, _, raw = alice.do("GET", "/api/sessions", nil)
	var list []Session
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) < 2 {
		t.Fatal(list)
	}
}
