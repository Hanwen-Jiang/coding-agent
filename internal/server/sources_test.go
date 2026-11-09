package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSourceNamesPreserveUnicodeAndRejectControlCharacters(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"需求 文档 #1?.txt", "需求 文档 #1?.txt"},
		{"参考📎.png", "参考📎.png"},
		{`C:\fakepath\报告.txt`, "报告.txt"},
		{"../../报告.txt", "报告.txt"},
		{"literal%2Fname.txt", "literal%2Fname.txt"},
		{".", ""}, {"..", ""}, {"", ""},
		{"nul\x00.txt", ""}, {"line\n.txt", ""}, {"tab\t.txt", ""},
		{"control\u0085.txt", ""}, {"invalid\xff.txt", ""},
	} {
		if got := sourceName(test.input); got != test.want {
			t.Errorf("sourceName(%q)=%q, want %q", test.input, got, test.want)
		}
	}
}

func uploadTestSource(t *testing.T, c *client, base, name string, data []byte) (int, map[string]any, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	_ = writer.Close()
	req, _ := http.NewRequest("POST", c.base+base+"/sources", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(csrfHeader, "1")
	res, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out, string(raw)
}

func TestSessionSourcesUploadReferenceIsolationAndDownload(t *testing.T) {
	var mu sync.Mutex
	files := map[string][]byte{}
	records := map[string]runtimeFileRecord{}
	aliases := map[string]string{}
	pagedReads := 0
	var app *Server
	adoptCalls := 0
	fileAPIUnavailable := false
	uploads := 0
	downloads := 0
	fake := &fakeTaskAPI{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		identity, authErr := app.ingress.Authenticate(r.Context(), ingress.Credentials{BearerToken: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
		if authErr != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		ws := r.URL.Query().Get("workspace_id")
		matches := func(record runtimeFileRecord) bool {
			return identity.TenantID == "acme" && record.File.WorkspaceID == ws && record.File.CreatedBy == identity.UserID
		}
		if fileAPIUnavailable && strings.HasPrefix(r.URL.Path, "/v1/files") {
			http.Error(w, "catalog unavailable", 503)
			return
		}
		switch {
		case r.URL.Path == "/v1/files/capabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{"max_file_bytes": maxSourceBytes, "max_task_bytes": maxTaskSourceBytes, "max_task_files": maxTaskSources})
		case r.URL.Path == "/v1/files/adopt":
			adoptCalls++
			var body struct {
				ObjectURIs []string `json:"object_uris"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			page := runtimeFilePage{Files: []runtimeFileRecord{}}
			for _, uri := range body.ObjectURIs {
				parts := strings.SplitN(strings.TrimPrefix(uri, "obj://acme/uploads/"), "/", 2)
				if len(parts) != 2 {
					http.Error(w, "bad uri", 400)
					return
				}
				lookup := parts[0]
				if canonical := aliases[lookup]; canonical != "" {
					lookup = canonical
				}
				record, ok := records[lookup]
				if !ok || !matches(record) || record.File.Name != parts[1] {
					http.NotFound(w, r)
					return
				}
				page.Files = append(page.Files, record)
			}
			_ = json.NewEncoder(w).Encode(page)
		case r.URL.Path == "/v1/files":
			page := runtimeFilePage{Files: []runtimeFileRecord{}}
			for _, record := range records {
				if matches(record) {
					page.Files = append(page.Files, record)
				}
			}
			slices.SortFunc(page.Files, func(a, b runtimeFileRecord) int { return strings.Compare(a.File.ID, b.File.ID) })
			offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
			if offset > 0 {
				pagedReads++
			}
			end := min(offset+2, len(page.Files))
			if end < len(page.Files) {
				page.NextCursor = strconv.Itoa(end)
			}
			page.Files = page.Files[offset:end]
			_ = json.NewEncoder(w).Encode(page)
		case strings.HasPrefix(r.URL.Path, "/v1/files/"):
			lookup := strings.TrimPrefix(r.URL.Path, "/v1/files/")
			if canonical := aliases[lookup]; canonical != "" {
				lookup = canonical
			}
			record, ok := records[lookup]
			if !ok || !matches(record) {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(record)
		case r.URL.Path == "/v1/uploads":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Error("upload did not forward authenticated token")
			}
			data, _ := io.ReadAll(r.Body)
			name := r.URL.Query().Get("filename")
			ws := r.URL.Query().Get("workspace_id")
			if ws == "" {
				t.Error("upload omitted workspace")
			}
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			var record runtimeFileRecord
			for _, candidate := range records {
				if matches(candidate) && candidate.File.Name == name && candidate.Version.SHA256 == digest {
					record = candidate
					break
				}
			}
			if record.File.ID == "" {
				uploads++
				id := fmt.Sprintf("src_%032x", uploads)
				files["/v1/uploads/"+id+"/"+name] = data
				record.File.ID = id
				record.File.WorkspaceID = ws
				record.File.Name = name
				record.File.CreatedBy = identity.UserID
				record.File.CreatedAt = time.Now().UTC()
				record.File.HeadVersionID = fmt.Sprintf("ver_%032x", uploads)
				record.File.Lifecycle = "active"
				record.Version.ID = record.File.HeadVersionID
				record.Version.FileID = id
				record.Version.MIMEType = http.DetectContentType(data)
				record.Version.SizeBytes = int64(len(data))
				record.Version.SHA256 = digest
				record.Version.CreatedAt = record.File.CreatedAt
				record.Version.CreatedBy = identity.UserID
				records[id] = record
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": record.File.ID, "file_id": record.File.ID, "version_id": record.Version.ID, "name": name, "uri": "obj://acme/uploads/" + record.File.ID + "/" + name, "mime_type": record.Version.MIMEType, "size_bytes": len(data), "sha256": digest, "created_at": record.Version.CreatedAt})
		case strings.HasPrefix(r.URL.Path, "/v1/uploads/"):
			downloads++
			data, ok := files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(data)
		default:
			fake.ServeHTTP(w, r)
		}
	}))
	defer upstream.Close()
	server, _, cfg, serverApp := newServerWith(t, func(c *Config) { c.TaskAPI = upstream.URL })
	app = serverApp
	fake.policies = cfg.PoliciesFile
	alice := newClient(t, server.URL)
	pid := sampleProject(t, alice)
	base, _ := newSession(t, alice, cfg, "acme", pid)
	code, out, raw := uploadTestSource(t, alice, base, "../../报告.txt", []byte("source material\n"))
	if code != 201 {
		t.Fatal(code, raw)
	}
	source := out["source"].(map[string]any)
	sourceID := source["id"].(string)
	if code, repeated, raw := uploadTestSource(t, alice, base, "报告.txt", []byte("source material\n")); code != 201 || repeated["source"].(map[string]any)["id"] != sourceID {
		t.Fatal("upload retry changed source identity", code, raw)
	}
	mu.Lock()
	if uploads != 1 {
		t.Error("upload retry wrote another Runtime object")
	}
	mu.Unlock()
	if source["name"] != "报告.txt" || strings.Contains(raw, "obj://") || strings.Contains(raw, "object_uri") || source["preview_url"] != nil {
		t.Fatal("unsafe source projection", raw)
	}
	code, task, raw := alice.do("POST", base+"/tasks", map[string]any{"message": "Read my attachment", "source_ids": []string{sourceID}})
	if code != 201 {
		t.Fatal(code, raw)
	}
	if task["source_ids"].([]any)[0] != sourceID {
		t.Fatal("task lost references", task)
	}
	mu.Lock()
	submission := fake.submits[len(fake.submits)-1]
	refs := submission["attachments"].([]any)
	mu.Unlock()
	if len(refs) != 1 || !strings.Contains(refs[0].(string), "/uploads/"+sourceID+"/") || submission["message"] != "Read my attachment" {
		t.Fatal("upstream attachment contract", submission)
	}
	_, listed, raw := alice.do("GET", base+"/sources", nil)
	entries := listed["sources"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["task_ids"].([]any)[0] != task["id"] {
		t.Fatal("source/task relation", raw)
	}
	// Metadata and task references survive reopening the application store.
	allSessions, _ := app.store.Sessions("acme")
	var workspace string
	for _, session := range allSessions {
		if base == "/api/projects/"+pid+"/sessions/"+session.ID {
			workspace = session.WorkspaceID
		}
	}
	restored, err := NewStore(app.store.dir).Sources("acme", workspace)
	if err != nil || len(restored) != 0 {
		t.Fatal("new source metadata was written to the old authority", restored, err)
	}
	// Stale local metadata cannot override Runtime, and is adopted once per owner.
	legacy := SourceRecord{ID: sourceID, Name: "报告.txt", CreatedBy: "alice", ObjectURI: refs[0].(string)}
	if err := app.store.PutSources("acme", workspace, []SourceRecord{legacy}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if code, _, raw := alice.do("GET", base+"/sources", nil); code != 200 {
			t.Fatal("legacy migration", code, raw)
		}
	}
	mu.Lock()
	if adoptCalls != 1 {
		t.Error("migration did not retain its owner receipt", adoptCalls)
	}
	mu.Unlock()
	if _, err := os.Stat(app.store.sourcesMigrationFile("acme", workspace, "alice")); err != nil {
		t.Fatal("missing migration receipt", err)
	}
	mu.Lock()
	fileAPIUnavailable = true
	mu.Unlock()
	if code, _, _ := alice.do("GET", base+"/sources", nil); code != 502 {
		t.Fatal("local metadata served during catalog outage", code)
	}
	if code, _, _ := alice.do("POST", base+"/tasks", map[string]any{"message": "Do not bypass catalog", "source_ids": []string{sourceID}}); code == 201 {
		t.Fatal("task bypassed catalog outage")
	}
	mu.Lock()
	fileAPIUnavailable = false
	mu.Unlock()
	if code, capability, raw := alice.do("GET", base+"/sources/capabilities", nil); code != 200 || capability["max_file_bytes"] != float64(maxSourceBytes) {
		t.Fatal("capabilities", code, raw)
	}
	if code, capability, raw := alice.do("GET", "/api/projects/"+pid+"/sources/capabilities", nil); code != 200 || capability["max_file_bytes"] != float64(maxSourceBytes) {
		t.Fatal("draft capabilities", code, raw)
	}
	request := func(c *client, target string) (int, http.Header, []byte) {
		t.Helper()
		res, err := c.http.Get(c.base + target)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, data
	}
	status, headers, data := request(alice, source["url"].(string))
	if status != 200 || string(data) != "source material\n" || !strings.HasPrefix(headers.Get("Content-Disposition"), "attachment") || headers.Get("Content-Security-Policy") != "sandbox" || headers.Get("X-Content-Type-Options") != "nosniff" || headers.Get("Cache-Control") != "no-store" {
		t.Fatal(status, headers, string(data))
	}

	// Runtime canonicalizes old duplicate aliases; histories keep their old URL,
	// while the file list counts the canonical source only once.
	alias := "src_ffffffffffffffffffffffffffffffff"
	mu.Lock()
	aliases[alias] = sourceID
	files["/v1/uploads/"+alias+"/报告.txt"] = []byte("source material\n")
	mu.Unlock()
	aliasLegacy := SourceRecord{ID: alias, Name: "报告.txt", CreatedBy: "alice", ObjectURI: "obj://acme/uploads/" + alias + "/报告.txt"}
	if err := app.store.PutSources("acme", workspace, []SourceRecord{legacy, aliasLegacy}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.UpdateTasks("acme", workspace, func(tasks []TaskRecord) []TaskRecord {
		return append(tasks, TaskRecord{ID: "legacy-task", SourceIDs: []string{alias}, Status: "TASK_STATUS_DONE"})
	}); err != nil {
		t.Fatal(err)
	}
	code, listed, raw = alice.do("GET", base+"/sources", nil)
	if code != 200 {
		t.Fatal(code, raw)
	}
	entries = listed["sources"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["alias_ids"].([]any)[0] != alias {
		t.Fatal("alias duplicated or disappeared", raw)
	}
	status, _, data = request(alice, base+"/sources/"+alias+"/content")
	if status != 200 || string(data) != "source material\n" {
		t.Fatal("legacy alias download", status, string(data))
	}
	// A tampered legacy row cannot be adopted or receive a successful receipt.
	badLegacy := aliasLegacy
	badLegacy.ObjectURI = "obj://acme/private/secret.txt"
	if err := app.store.PutSources("acme", workspace, []SourceRecord{badLegacy}); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := alice.do("GET", base+"/sources", nil); code != 502 {
		t.Fatal("tampered migration accepted", code)
	}
	if err := app.store.PutSources("acme", workspace, []SourceRecord{legacy, aliasLegacy}); err != nil {
		t.Fatal(err)
	}
	// Cross-session and cross-user sources are rejected before any Runtime call.
	otherBase, _ := newSession(t, alice, cfg, "acme", pid)
	if code, _, _ := alice.do("POST", otherBase+"/tasks", map[string]any{"message": "wrong session", "source_ids": []string{sourceID}}); code != 400 {
		t.Fatal("cross-session reference accepted", code)
	}
	bob := newClient(t, server.URL)
	bob.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "bob"})
	if code, _, _ := bob.do("POST", base+"/tasks", map[string]any{"message": "wrong owner", "source_ids": []string{sourceID}}); code != 400 {
		t.Fatal("foreign owner reference accepted", code)
	}
	foreign := newClient(t, server.URL)
	foreign.do("POST", "/api/login", map[string]string{"tenant": "other", "user": "alice"})
	mu.Lock()
	beforeDownloads := downloads
	mu.Unlock()
	for _, attempt := range []struct {
		c   *client
		url string
	}{{alice, otherBase + "/sources/" + sourceID + "/content"}, {bob, source["url"].(string)}, {foreign, source["url"].(string)}, {newClient(t, server.URL), source["url"].(string)}} {
		if code, _, _ := request(attempt.c, attempt.url); code != 404 && code != 401 {
			t.Fatal("foreign read accepted", code)
		}
	}
	mu.Lock()
	if downloads != beforeDownloads {
		t.Error("unauthorized read reached Runtime")
	}
	mu.Unlock()
	// Raster previews are authenticated same-origin resources; HTML has no inline route.
	png := append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, bytes.Repeat([]byte{0}, 20)...)
	code, out, raw = uploadTestSource(t, alice, base, "image.png", png)
	if code != 201 {
		t.Fatal(raw)
	}
	image := out["source"].(map[string]any)
	status, headers, data = request(alice, image["preview_url"].(string))
	if status != 200 || headers.Get("Content-Type") != "image/png" || !strings.HasPrefix(headers.Get("Content-Disposition"), "inline") || !bytes.Equal(png, data) {
		t.Fatal(status, headers)
	}
	code, out, raw = uploadTestSource(t, alice, base, "page.html", []byte("<html><script>alert(1)</script></html>"))
	if code != 201 {
		t.Fatal(raw)
	}
	html := out["source"].(map[string]any)
	if html["preview_url"] != nil {
		t.Fatal("HTML offered preview")
	}
	if code, _, _ := request(alice, base+"/sources/"+html["id"].(string)+"/preview"); code != 404 {
		t.Fatal("HTML preview allowed")
	}
	if code, _, _ := uploadTestSource(t, alice, base, "big.bin", make([]byte, maxSourceBytes+1)); code != 413 {
		t.Fatal("oversized upload accepted", code)
	}
	if code, _, _ := alice.do("POST", base+"/tasks", map[string]any{"message": "duplicate", "source_ids": []string{sourceID, sourceID}}); code != 400 {
		t.Fatal("duplicate source accepted")
	}
	tooMany := make([]string, maxTaskSources+1)
	if code, _, _ := alice.do("POST", base+"/tasks", map[string]any{"message": "many", "source_ids": tooMany}); code != 400 {
		t.Fatal("source count limit ignored")
	}
	// Filename punctuation stays an opaque object key. URL escaping must neither
	// turn ?/# into request query/fragment nor decode a literal percent sequence.
	for _, name := range []string{"需求 文档 #1? 100%.txt", "literal%2Fname.txt", "参考📎.txt"} {
		code, out, raw := uploadTestSource(t, alice, base, name, []byte(name))
		if code != 201 {
			t.Fatal("Unicode upload", code, raw)
		}
		source := out["source"].(map[string]any)
		status, headers, data := request(alice, source["url"].(string))
		if status != 200 || string(data) != name || source["name"] != name || headers.Get("Content-Disposition") == "" {
			t.Fatal("filename URL round trip", status, headers, string(data))
		}
		if code, _, raw := alice.do("POST", base+"/tasks", map[string]any{"message": "Read the named attachment", "source_ids": []string{source["id"].(string)}}); code != 201 {
			t.Fatal("filename task reference", code, raw)
		}
	}
	code, listed, raw = alice.do("GET", base+"/sources", nil)
	if code != 200 || len(listed["sources"].([]any)) < 6 {
		t.Fatal("paginated source list truncated", code, raw)
	}
	mu.Lock()
	if pagedReads == 0 {
		t.Error("catalog cursor was not followed")
	}
	files["/v1/uploads/"+sourceID+"/报告.txt"] = []byte("modified upstream")
	mu.Unlock()
	if code, _, data := request(alice, source["url"].(string)); code != 502 || strings.Contains(string(data), "modified upstream") {
		t.Fatal("corrupt download escaped hash validation", code, string(data))
	}

	// The proxy streams a full 50 MiB file, with a 200 MiB task boundary.
	large := bytes.Repeat([]byte("x"), maxSourceBytes)
	code, uploaded, raw := uploadTestSource(t, alice, base, "50MiB.bin", large)
	if code != 201 {
		t.Fatal("50 MiB upload boundary", code, raw)
	}
	largeSource := uploaded["source"].(map[string]any)
	status, _, data = request(alice, largeSource["url"].(string))
	if status != 200 || len(data) != maxSourceBytes || !bytes.Equal(data, large) {
		t.Fatal("50 MiB download", status, len(data))
	}
	largeIDs := []string{largeSource["id"].(string)}
	mu.Lock()
	largeRecord := records[largeIDs[0]]
	for i := 1; i <= 4; i++ {
		copy := largeRecord
		copy.File.ID = fmt.Sprintf("src_%032x", 1000+i)
		copy.File.HeadVersionID = fmt.Sprintf("ver_%032x", 1000+i)
		copy.File.Name = fmt.Sprintf("large-%d.bin", i)
		copy.Version.ID = copy.File.HeadVersionID
		copy.Version.FileID = copy.File.ID
		records[copy.File.ID] = copy
		largeIDs = append(largeIDs, copy.File.ID)
	}
	mu.Unlock()
	if code, _, raw := alice.do("POST", base+"/tasks", map[string]any{"message": "Exactly 200 MiB", "source_ids": largeIDs[:4]}); code != 201 {
		t.Fatal("200 MiB boundary", code, raw)
	}
	if code, _, _ := alice.do("POST", base+"/tasks", map[string]any{"message": "Above 200 MiB", "source_ids": largeIDs}); code != 400 {
		t.Fatal("task byte limit not enforced", code)
	}

}
