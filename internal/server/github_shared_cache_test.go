package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/dev/memcache"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/cache"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
	"github.com/agentruntime/agent-runtime/libs/go-core/testkit"
	"github.com/agentruntime/coding-agent/internal/github"
)

type permissionCacheFixture struct {
	cache cache.Cache
	clock *testkit.FakeClock
	prov  *Provider
	calls atomic.Int64
	push  atomic.Bool
	// Optional barrier holds the first upstream response across invalidation.
	entered chan struct{}
	release chan struct{}
}

func newPermissionCacheFixture(t *testing.T) *permissionCacheFixture {
	t.Helper()
	f := &permissionCacheFixture{clock: testkit.NewFakeClock()}
	p := memcache.New(memcache.Options{Clock: f.clock})
	c, err := p.BindPlatform("coding-web")
	if err != nil {
		t.Fatal(err)
	}
	f.cache = c
	f.push.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.calls.Add(1)
		push := f.push.Load()
		if n == 1 && f.entered != nil {
			close(f.entered)
			<-f.release
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 555, "full_name": strings.TrimPrefix(r.URL.Path, "/repos/"),
			"private": true, "permissions": map[string]bool{"pull": true, "push": push}})
	}))
	t.Cleanup(upstream.Close)
	f.prov = &Provider{Name: "GitHub", API: upstream.URL, client: upstream.Client()}
	return f
}

func (f *permissionCacheFixture) server(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	return &Server{cfg: Config{SharedCache: f.cache, Now: f.clock.Now, DataDir: dir,
		GitHubApp: &GitHubApp{AppID: "123", ClientID: "test-client"}}, providers: map[string]*Provider{"github": f.prov}, store: &Store{dir: dir}}
}

func permissionTestIdentity(tenant, user string) identity {
	return identity{Identity: ingress.Identity{TenantID: tenant, UserID: user}}
}

func (f *permissionCacheFixture) read(t *testing.T, s *Server, id identity, token, repo string) permEntry {
	t.Helper()
	e, err := s.repoPermission(context.Background(), f.prov, id, token, &Project{Repo: repo, InstallationID: 100})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestGitHubSharedPermissionsCrossServerAndExpiry(t *testing.T) {
	f := newPermissionCacheFixture(t)
	a, b := f.server(t), f.server(t)
	id := permissionTestIdentity("acme", "alice")
	if !f.read(t, a, id, "private-token", "owner/repo").push || !f.read(t, b, id, "private-token", "owner/repo").push {
		t.Fatal("permission missing")
	}
	if f.calls.Load() != 1 {
		t.Fatalf("shared result queried upstream %d times", f.calls.Load())
	}
	f.push.Store(false)
	f.clock.Advance(permTTL)
	if f.read(t, b, id, "private-token", "owner/repo").push || f.calls.Load() != 2 {
		t.Fatal("expired permission reused")
	}
}

func TestGitHubSharedPermissionsIsolateIdentityCredentialAndApp(t *testing.T) {
	f := newPermissionCacheFixture(t)
	a := f.server(t)
	for _, q := range []struct{ tenant, user, token, repo string }{
		{"a", "alice", "secret-one", "owner/repo"}, {"b", "alice", "secret-one", "owner/repo"},
		{"a", "bob", "secret-one", "owner/repo"}, {"a", "alice", "secret-two", "owner/repo"},
		{"a", "alice", "secret-one", "owner/another"},
	} {
		f.read(t, a, permissionTestIdentity(q.tenant, q.user), q.token, q.repo)
	}
	b := f.server(t)
	b.cfg.GitHubApp.AppID = "other-app"
	f.read(t, b, permissionTestIdentity("a", "alice"), "secret-one", "owner/repo")
	if f.calls.Load() != 6 {
		t.Fatalf("authorization cache crossed an identity boundary: %d upstream calls", f.calls.Load())
	}
}

func TestGitHubSharedPermissionsInvalidationAndEpochEviction(t *testing.T) {
	f := newPermissionCacheFixture(t)
	a, b := f.server(t), f.server(t)
	id := permissionTestIdentity("acme", "alice")
	f.read(t, a, id, "token", "owner/repo")
	old, err := a.sharedPermissionEpoch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.invalidatePermissions(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.push.Store(false)
	if f.read(t, b, id, "token", "owner/repo").push || f.calls.Load() != 2 {
		t.Fatal("cross-server invalidation did not take effect")
	}
	if _, err := f.cache.Delete(context.Background(), a.permissionEpochKey()); err != nil {
		t.Fatal(err)
	}
	if f.read(t, b, id, "token", "owner/repo").push || f.calls.Load() != 3 {
		t.Fatal("epoch eviction resurrected an earlier authorization")
	}
	fresh, err := a.sharedPermissionEpoch(context.Background())
	if err != nil || fresh == old {
		t.Fatal("missing epoch reused an old generation")
	}
}

func TestGitHubSharedPermissionsInflightWriteCannotUndoInvalidation(t *testing.T) {
	f := newPermissionCacheFixture(t)
	f.entered, f.release = make(chan struct{}), make(chan struct{})
	a, b := f.server(t), f.server(t)
	id := permissionTestIdentity("acme", "alice")
	done := make(chan error, 1)
	go func() {
		_, err := a.repoPermission(context.Background(), f.prov, id, "token", &Project{Repo: "owner/repo", InstallationID: 100})
		done <- err
	}()
	<-f.entered
	err := b.invalidatePermissions(context.Background())
	f.push.Store(false)
	close(f.release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.read(t, b, id, "token", "owner/repo").push || f.calls.Load() != 2 {
		t.Fatal("old in-flight write became current authorization")
	}
	if f.read(t, a, id, "token", "owner/repo").push || f.calls.Load() != 2 {
		t.Fatal("new authorization not shared")
	}
}

type unavailablePermissionCache struct {
	cache.Cache
	wait bool
}

func (c unavailablePermissionCache) Get(ctx context.Context, _ string) ([]byte, bool, error) {
	if c.wait {
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	return nil, false, errors.New("private-cache-credential")
}

func (c unavailablePermissionCache) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("private-cache-credential")
}

func TestGitHubSharedPermissionsFailureUsesFreshGitHub(t *testing.T) {
	f := newPermissionCacheFixture(t)
	s := f.server(t)
	id := permissionTestIdentity("acme", "alice")
	// Seed both the shared cache and the development cache with now-stale grants.
	f.read(t, s, id, "token", "owner/repo")
	s.cfg.SharedCache = nil
	f.read(t, s, id, "token", "owner/repo")
	s.cfg.SharedCache = unavailablePermissionCache{Cache: f.cache, wait: true}
	f.push.Store(false)
	before := time.Now()
	if f.read(t, s, id, "token", "owner/repo").push {
		t.Fatal("cache outage reused stale authorization")
	}
	if elapsed := time.Since(before); elapsed > time.Second {
		t.Fatalf("cache fallback blocked too long: %s", elapsed)
	}
	if f.calls.Load() != 3 {
		t.Fatal("cache failure did not reauthorize against GitHub")
	}
}

func TestGitHubPermissionWebhookInvalidationFailuresAreRetryable(t *testing.T) {
	f := newPermissionCacheFixture(t)
	s := f.server(t)
	s.cfg.SharedCache = unavailablePermissionCache{Cache: f.cache}
	for event, payload := range map[string]string{
		"installation":              `{"action":"suspend","installation":{"id":100}}`,
		"installation_repositories": `{"action":"removed","installation":{"id":100},"repositories_removed":[{"id":555}]}`,
		"repository":                `{"action":"transferred","repository":{"id":555}}`,
		"github_app_authorization":  `{"action":"revoked","sender":{"id":42}}`,
	} {
		err := s.GitHubHandlers()[event](context.Background(), github.Delivery{Payload: []byte(payload)})
		if err == nil || strings.Contains(err.Error(), "private-cache-credential") {
			t.Fatalf("%s must retry with a safe error: %v", event, err)
		}
	}
	for event, payload := range map[string]string{
		"installation":              `{"action":"suspend"}`,
		"installation_repositories": `{"action":"removed"}`,
		"repository":                `{"action":"transferred"}`,
		"github_app_authorization":  `{"action":"revoked"}`,
	} {
		if err := s.GitHubHandlers()[event](context.Background(), github.Delivery{Payload: []byte(payload)}); err != nil {
			t.Fatalf("invalid %s event should not invalidate cache: %v", event, err)
		}
	}
}

func TestGitHubPermissionCacheOutageDoesNotBlockRevocation(t *testing.T) {
	for _, tc := range []struct{ event, payload, state string }{
		{"installation", `{"action":"suspend","installation":{"id":100}}`, "app_suspended"},
		{"installation", `{"action":"deleted","installation":{"id":100}}`, "app_removed"},
		{"installation_repositories", `{"action":"removed","installation":{"id":100},"repositories_removed":[{"id":555}]}`, "repo_removed"},
		{"repository", `{"action":"deleted","repository":{"id":555}}`, "remote_deleted"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			f := newEventFixture(t)
			f.srv.cfg.SharedCache = unavailablePermissionCache{}
			err := f.srv.GitHubHandlers()[tc.event](context.Background(), github.Delivery{Payload: []byte(tc.payload)})
			if err == nil || strings.Contains(err.Error(), "private-cache-credential") {
				t.Fatalf("cache failure must remain retryable without leaking credentials: %v", err)
			}
			if p := f.projectNow(); p.RemoteState != tc.state {
				t.Fatalf("revocation was blocked by cache outage: %s", p.RemoteState)
			}
			if tc.event == "installation" && len(f.tasks.cancels) == 0 {
				t.Fatal("cache outage prevented cancellation of the affected task")
			}
		})
	}
	t.Run("user_authorization", func(t *testing.T) {
		f := newEventFixture(t)
		f.srv.cfg.SharedCache = unavailablePermissionCache{}
		ids := map[string]identityRef{"github:42": {Tenant: f.id.TenantID, User: f.id.UserID}}
		if err := writeFileAtomic(f.srv.identitiesFile(), ids); err != nil {
			t.Fatal(err)
		}
		err := f.srv.onAppAuthorization(context.Background(), github.Delivery{Payload: []byte(`{"action":"revoked","sender":{"id":42}}`)})
		if err == nil || strings.Contains(err.Error(), "private-cache-credential") {
			t.Fatalf("cache failure must remain retryable without leaking credentials: %v", err)
		}
		a, err := f.srv.readAccount(f.id.TenantID, f.id.UserID)
		if err != nil {
			t.Fatal(err)
		}
		conn := a.Connections["github"]
		if conn.Token != "" || conn.Refresh != "" || !conn.ExpiresAt.IsZero() {
			t.Fatal("cache outage prevented removal of revoked credentials")
		}
	})
}
