package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type discoveryRoundTrip func(*http.Request) (*http.Response, error)

func (f discoveryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func discoveryResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
}
func mockDiscovery(t *testing.T, handle func(*http.Request) *http.Response) *discoveryService {
	t.Helper()
	d := newDiscoveryService()
	d.client.Transport = discoveryRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("public discovery leaked credentials")
		}
		if !discoveryHosts[r.URL.Host] || r.URL.Scheme != "https" {
			t.Errorf("unexpected source %s", r.URL)
		}
		return handle(r), nil
	})
	return d
}
func skillZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, content := range files {
		f, e := w.Create("repo-HEAD/" + name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write([]byte(content)); e != nil {
			t.Fatal(e)
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func discoveryJSON(t *testing.T, h authedHandler, target string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest("GET", target, nil)
	r.Header.Set("Authorization", "Bearer private-token")
	r.Header.Set("Cookie", "private-session=secret")
	w := httptest.NewRecorder()
	h(w, r, identity{})
	var data map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatal(w.Body.String())
	}
	return w.Code, data
}
func TestPublicDiscoveryRoutesRequireLogin(t *testing.T) {
	ts, _, _, srv := newServerWith(t, nil)
	var calls atomic.Int32
	srv.discovery = mockDiscovery(t, func(_ *http.Request) *http.Response { calls.Add(1); return discoveryResponse(500, nil) })
	c := newClient(t, ts.URL)
	for _, p := range []string{"/api/discovery/skills", "/api/discovery/skills/detail?repo=a/b&path=SKILL.md", "/api/discovery/mcp"} {
		status, _, _ := c.do("GET", p, nil)
		if status != 401 {
			t.Fatalf("%s: %d", p, status)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthenticated discovery reached upstream")
	}
	c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	status, _, _ := c.do("GET", "/api/discovery/skills?repo=http://127.0.0.1/secret", nil)
	if status != 400 {
		t.Fatalf("bad repository accepted: %d", status)
	}
}
func TestPublicDiscoverySkillArchiveFallbackAndImportBoundary(t *testing.T) {
	archive := skillZIP(t, map[string]string{"skills/pure/SKILL.md": "---\nname: pure\ndescription: >\n  A real public skill\n  with folded metadata.\n---\n# Instructions\nRead this text.", "skills/complex/SKILL.md": "---\nname: complex\ndescription: 'With resources'\n---\nRead references/guide.md", "skills/complex/references/guide.md": "Supporting text", "skills/external/SKILL.md": "---\nname: external\ndescription: Has an external relative document\n---\nRead [guide](../../shared.md)."})
	var githubCalls atomic.Int32
	d := mockDiscovery(t, func(r *http.Request) *http.Response {
		if r.URL.Host == "api.github.com" {
			githubCalls.Add(1)
			return discoveryResponse(403, []byte(`{"message":"rate limit"}`))
		}
		if r.URL.Host == "codeload.github.com" {
			return discoveryResponse(200, archive)
		}
		t.Fatalf("unexpected %s", r.URL)
		return nil
	})
	s := &Server{discovery: d}
	code, list := discoveryJSON(t, s.handleExploreSkills, "/api/discovery/skills?repo=example/repo")
	if code != 200 || list["source"] != "git-archive" || len(list["items"].([]any)) != 3 {
		t.Fatalf("%d %#v", code, list)
	}
	code, detail := discoveryJSON(t, s.handleExploreSkillDetail, "/api/discovery/skills/detail?repo=example/repo&path=skills/pure/SKILL.md")
	if code != 200 || detail["name"] != "pure" || detail["description"] != "A real public skill with folded metadata." || detail["importable"] != true || !strings.Contains(detail["instructions"].(string), "# Instructions") {
		t.Fatalf("%d %#v", code, detail)
	}
	if !strings.HasPrefix(detail["revision"].(string), "archive-sha256:") || detail["ref"] != "HEAD" {
		t.Fatalf("archive pretends to be a commit: %#v", detail)
	}
	for _, p := range []string{"skills/complex/SKILL.md", "skills/external/SKILL.md"} {
		_, detail = discoveryJSON(t, s.handleExploreSkillDetail, "/api/discovery/skills/detail?repo=example/repo&path="+p)
		if detail["importable"] != false || detail["has_supporting_files"] != true {
			t.Fatalf("partial skill allowed: %#v", detail)
		}
	}
	if githubCalls.Load() != 1 {
		t.Fatalf("rate-limited source repeatedly queried: %d", githubCalls.Load())
	}
}
func TestPublicDiscoveryGitTreePinsRawDocumentsToCommit(t *testing.T) {
	sha := strings.Repeat("a", 40)
	d := mockDiscovery(t, func(r *http.Request) *http.Response {
		switch r.URL.Host + r.URL.Path {
		case "api.github.com/repos/owner/repo/commits/HEAD":
			return discoveryResponse(200, []byte(`{"sha":"`+sha+`"}`))
		case "api.github.com/repos/owner/repo/git/trees/" + sha:
			if r.URL.Query().Get("recursive") != "1" {
				t.Fatal("tree must be complete")
			}
			return discoveryResponse(200, []byte(`{"tree":[{"path":"skills/sample/SKILL.md","type":"blob","mode":"100644","size":70}],"truncated":false}`))
		case "raw.githubusercontent.com/owner/repo/" + sha + "/skills/sample/SKILL.md":
			return discoveryResponse(200, []byte("---\nname: sample\ndescription: Correct snapshot\n---\nDo the task."))
		default:
			t.Fatalf("unexpected URL %s", r.URL)
			return nil
		}
	})
	s := &Server{discovery: d}
	code, result := discoveryJSON(t, s.handleExploreSkillDetail, "/api/discovery/skills/detail?repo=owner/repo&path=skills/sample/SKILL.md")
	if code != 200 || result["ref"] != sha || result["revision"] != "commit:"+sha {
		t.Fatalf("%d %#v", code, result)
	}
}
func TestPublicDiscoveryRejectsUntrustedPathsAndSourcesBeforeNetwork(t *testing.T) {
	d := mockDiscovery(t, func(_ *http.Request) *http.Response { t.Fatal("invalid request reached network"); return nil })
	s := &Server{discovery: d}
	for _, repo := range []string{"http://localhost/secret", "https://github.com.evil/a/b", "https://user:password@github.com/a/b", "../secret", "a/b/c", "https://github.com/a/b?token=x"} {
		code, _ := discoveryJSON(t, s.handleExploreSkills, "/api/discovery/skills?repo="+url.QueryEscape(repo))
		if code != 400 {
			t.Fatalf("repo %q: %d", repo, code)
		}
	}
	for _, p := range []string{"../SKILL.md", "/SKILL.md", "a/../SKILL.md", "a\\SKILL.md", "README.md"} {
		code, _ := discoveryJSON(t, s.handleExploreSkillDetail, "/api/discovery/skills/detail?repo=a/b&path="+url.QueryEscape(p))
		if code != 400 {
			t.Fatalf("path %q: %d", p, code)
		}
	}
	for _, raw := range []string{"http://api.github.com/a", "https://api.github.com:444/a", "https://evil.test/a", "https://token@api.github.com/a"} {
		if _, err := d.get(context.Background(), raw, 100); err == nil {
			t.Fatal("untrusted source accepted")
		}
	}
}
func TestPublicDiscoveryCacheCoalescesAndDoesNotFollowRedirect(t *testing.T) {
	var calls atomic.Int32
	d := mockDiscovery(t, func(r *http.Request) *http.Response {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		if r.URL.Path == "/redirect" {
			res := discoveryResponse(302, nil)
			res.Header.Set("Location", "https://internal.example/secret")
			return res
		}
		return discoveryResponse(200, []byte(`{}`))
	})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.get(context.Background(), "https://api.github.com/public", 100)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("cache miss requested %d times", calls.Load())
	}
	if _, err := d.get(context.Background(), "https://api.github.com/redirect", 100); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("redirect followed")
	}
}
func TestPublicDiscoveryArchiveLimitsAndMalformedMetadata(t *testing.T) {
	for name, archive := range map[string][]byte{"traversal": skillZIP(t, map[string]string{"../SKILL.md": "bad"}), "oversized-document": skillZIP(t, map[string]string{"SKILL.md": strings.Repeat("x", discoveryDocumentLimit+1)}), "invalid-frontmatter": skillZIP(t, map[string]string{"SKILL.md": "---\nname: a\ndescription: !Execute bad\n---\nx"})} {
		t.Run(name, func(t *testing.T) {
			d := mockDiscovery(t, func(r *http.Request) *http.Response {
				if r.URL.Host == "api.github.com" {
					return discoveryResponse(403, nil)
				}
				return discoveryResponse(200, archive)
			})
			s := &Server{discovery: d}
			code, _ := discoveryJSON(t, s.handleExploreSkillDetail, "/api/discovery/skills/detail?repo=a/b&path=SKILL.md")
			if code < 400 {
				t.Fatal("unsafe document accepted")
			}
		})
	}
	d := mockDiscovery(t, func(_ *http.Request) *http.Response { return discoveryResponse(200, []byte(strings.Repeat("x", 101))) })
	if _, err := d.get(context.Background(), "https://raw.githubusercontent.com/a/b/main/SKILL.md", 100); err == nil {
		t.Fatal("oversized response accepted")
	}
}
func TestPublicDiscoveryMCPRegistryFiltersAndSafeCapabilities(t *testing.T) {
	d := mockDiscovery(t, func(r *http.Request) *http.Response {
		if r.URL.Query().Get("version") != "latest" || r.URL.Query().Get("search") != "github" || r.URL.Query().Get("cursor") != "publisher/server:1" {
			t.Fatalf("filters lost: %s", r.URL)
		}
		return discoveryResponse(200, []byte(`{"servers":[{"server":{"name":"io.example/remote","title":"Remote","description":"Public server","version":"1.0","repository":{"url":"https://github.com/example/server"},"remotes":[{"type":"streamable-http","url":"https://example.com/mcp","headers":[{"name":"Authorization","isRequired":true,"isSecret":true,"value":"Bearer never-copy-me"}]}]}},{"server":{"name":"io.example/local","version":"2","packages":[{"registryType":"npm","identifier":"@example/mcp","version":"2","runtimeHint":"npx","transport":{"type":"stdio"},"environmentVariables":[{"name":"API_KEY","isRequired":true,"isSecret":true,"value":"never-copy-me"}]}]}},{"server":{"name":"io.example/unsafe","remotes":[{"type":"streamable-http","url":"https://127.0.0.1/mcp"}]}}],"metadata":{"nextCursor":"next:2"}}`))
	})
	s := &Server{discovery: d}
	code, result := discoveryJSON(t, s.handleExploreMCP, "/api/discovery/mcp?query=github&cursor=publisher/server:1")
	if code != 200 || result["next_cursor"] != "next:2" {
		t.Fatalf("%d %#v", code, result)
	}
	items := result["items"].([]any)
	first := items[0].(map[string]any)
	remote := first["remotes"].([]any)[0].(map[string]any)
	if first["connectable"] != true || remote["transport"] != "streamable-http" || remote["auth_required"] != true {
		t.Fatal(first)
	}
	if items[1].(map[string]any)["connectable"] != false || items[2].(map[string]any)["connectable"] != false {
		t.Fatal("unsupported remote marked connectable")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "never-copy-me") {
		t.Fatal("registry secret template leaked into connector form")
	}
}
func TestPublicDiscoveryMCPShortQueryDoesNotBrowseEverything(t *testing.T) {
	s := &Server{discovery: mockDiscovery(t, func(_ *http.Request) *http.Response { t.Fatal("short search was sent upstream"); return nil })}
	code, result := discoveryJSON(t, s.handleExploreMCP, "/api/discovery/mcp?query=ab")
	if code != 400 || result["code"] != "search_too_short" {
		t.Fatalf("%d %#v", code, result)
	}
}
func TestPublicDiscoveryLiveSources(t *testing.T) {
	if os.Getenv("CODING_DISCOVERY_LIVE") != "1" {
		t.Skip("explicit read-only public source integration")
	}
	s := &Server{discovery: newDiscoveryService()}
	code, list := discoveryJSON(t, s.handleExploreSkills, "/api/discovery/skills?repo=anthropics/skills")
	if code != 200 {
		t.Fatalf("skills source: %d %#v", code, list)
	}
	items := list["items"].([]any)
	if len(items) == 0 {
		t.Fatal("empty live skill catalog")
	}
	entry := items[0].(map[string]any)
	target := "/api/discovery/skills/detail?repo=anthropics/skills&ref=" + url.QueryEscape(entry["ref"].(string)) + "&path=" + url.QueryEscape(entry["path"].(string))
	code, detail := discoveryJSON(t, s.handleExploreSkillDetail, target)
	if code != 200 || len(detail["instructions"].(string)) < 20 {
		t.Fatalf("skill preview: %d %#v", code, detail)
	}
	code, mcp := discoveryJSON(t, s.handleExploreMCP, "/api/discovery/mcp?query=github")
	if code != 200 || len(mcp["items"].([]any)) == 0 {
		t.Fatalf("MCP registry: %d %#v", code, mcp)
	}
	t.Logf("live sources: %d/%v skill entries via %s; preview %s; %d registry servers; warnings=%v", len(items), list["total"], list["source"], detail["name"], len(mcp["items"].([]any)), list["warnings"])
}
