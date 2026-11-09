package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/dev/memcache"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/cache"
	"github.com/agentruntime/agent-runtime/libs/go-core/testkit"
)

func discoveryTestShared(t *testing.T) (cache.Cache, *testkit.FakeClock) {
	t.Helper()
	clk := testkit.NewFakeClock()
	p := memcache.New(memcache.Options{Clock: clk})
	t.Cleanup(func() { _ = p.Close() })
	c, err := p.BindPlatform("coding-web")
	if err != nil {
		t.Fatal(err)
	}
	return c, clk
}

func TestPublicDiscoverySharedCacheAcrossInstancesAndTTL(t *testing.T) {
	c, clk := discoveryTestShared(t)
	var calls atomic.Int32
	upstream := func(*http.Request) *http.Response {
		return discoveryResponse(200, fmt.Appendf(nil, "response-%d", calls.Add(1)))
	}
	a, b := mockDiscovery(t, upstream), mockDiscovery(t, upstream)
	a.shared, b.shared = c, c
	endpoint := "https://registry.modelcontextprotocol.io/v0.1/servers"
	first, err := a.get(context.Background(), endpoint, 100)
	if err != nil {
		t.Fatal(err)
	}
	// A configured shared cache is authoritative even if old local entries exist.
	b.cache[endpoint+"#limit=100"] = discoveryCacheItem{body: []byte("stale-local"), at: time.Now()}
	second, err := b.get(context.Background(), endpoint, 100)
	if err != nil || string(first) != string(second) || calls.Load() != 1 {
		t.Fatalf("second instance did not reuse shared data: %q, calls=%d, err=%v", second, calls.Load(), err)
	}
	if len(a.cache) != 0 || a.cacheBytes != 0 {
		t.Fatal("shared mode also populated the local result cache")
	}
	clk.Advance(discoveryTTL + time.Second)
	third, err := b.get(context.Background(), endpoint, 100)
	if err != nil || string(third) != "response-2" || calls.Load() != 2 {
		t.Fatalf("expired shared data reused: %q, calls=%d, err=%v", third, calls.Load(), err)
	}
}

func TestPublicDiscoverySharedKeysIncludeResponseLimit(t *testing.T) {
	c, _ := discoveryTestShared(t)
	var calls atomic.Int32
	d := mockDiscovery(t, func(*http.Request) *http.Response {
		calls.Add(1)
		return discoveryResponse(200, []byte("0123456789"))
	})
	d.shared = c
	endpoint := "https://api.github.com/repos/example/repo"
	if _, err := d.get(context.Background(), endpoint, 100); err != nil {
		t.Fatal(err)
	}
	_, err := d.get(context.Background(), endpoint, 4)
	var de *discoveryError
	if !errors.As(err, &de) || de.code != "archive_too_large" || calls.Load() != 2 {
		t.Fatalf("small read limit bypassed by cached response: calls=%d, err=%v", calls.Load(), err)
	}
}

type discoveryUnavailableCache struct {
	cache.Cache
	t     *testing.T
	reads atomic.Int32
}

func (c *discoveryUnavailableCache) fail(ctx context.Context) error {
	c.t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > discoveryCacheTimeout+time.Millisecond {
		c.t.Error("shared cache operation is not bounded by its timeout")
	}
	return errors.New("private-cache-address-and-password")
}

func (c *discoveryUnavailableCache) Get(ctx context.Context, _ string) ([]byte, bool, error) {
	c.reads.Add(1)
	return nil, false, c.fail(ctx)
}

func (c *discoveryUnavailableCache) Set(ctx context.Context, _ string, _ []byte, _ time.Duration) error {
	return c.fail(ctx)
}

func (c *discoveryUnavailableCache) RateLimit(ctx context.Context, _ string, _ int64, _ time.Duration, _ int64) (cache.RateDecision, error) {
	return cache.RateDecision{}, c.fail(ctx)
}

func TestPublicDiscoverySharedCacheOutageFetchesUpstream(t *testing.T) {
	var calls atomic.Int32
	d := mockDiscovery(t, func(*http.Request) *http.Response {
		calls.Add(1)
		return discoveryResponse(200, []byte("public metadata"))
	})
	broken := &discoveryUnavailableCache{t: t}
	d.shared = broken
	for range 2 {
		body, err := d.get(context.Background(), "https://api.github.com/repos/example/repo", 100)
		if err != nil || string(body) != "public metadata" {
			t.Fatalf("cache failure leaked to caller: %q %v", body, err)
		}
	}
	if calls.Load() != 2 || broken.reads.Load() == 0 || len(d.cache) != 0 {
		t.Fatalf("outage fallback did not fetch directly: upstream=%d reads=%d local=%d", calls.Load(), broken.reads.Load(), len(d.cache))
	}
}

func TestPublicDiscoverySharedCacheStillCoalescesLocalRequests(t *testing.T) {
	c, _ := discoveryTestShared(t)
	var calls atomic.Int32
	d := mockDiscovery(t, func(*http.Request) *http.Response {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return discoveryResponse(200, []byte("metadata"))
	})
	d.shared = c
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.get(context.Background(), "https://api.github.com/repos/example/repo", 100); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("same resource fetched %d times", calls.Load())
	}
}

func TestPublicDiscoverySharedUpstreamLimitsAcrossInstances(t *testing.T) {
	c, clk := discoveryTestShared(t)
	var calls atomic.Int32
	upstream := func(*http.Request) *http.Response {
		calls.Add(1)
		return discoveryResponse(200, []byte("archive"))
	}
	a, b := mockDiscovery(t, upstream), mockDiscovery(t, upstream)
	a.shared, b.shared = c, c
	for i := range 6 {
		d := []*discoveryService{a, b}[i%2]
		if _, err := d.get(context.Background(), fmt.Sprintf("https://codeload.github.com/example/repo/zip/%d", i), 100); err != nil {
			t.Fatal(err)
		}
	}
	_, err := b.get(context.Background(), "https://codeload.github.com/example/repo/zip/7", 100)
	var de *discoveryError
	if !errors.As(err, &de) || de.status != 429 || calls.Load() != 6 {
		t.Fatalf("replicas did not share rate limit: upstream=%d err=%v", calls.Load(), err)
	}
	clk.Advance(time.Minute + time.Second)
	if _, err := b.get(context.Background(), "https://codeload.github.com/example/repo/zip/7", 100); err != nil || calls.Load() != 7 {
		t.Fatalf("shared rate limit did not expire: upstream=%d err=%v", calls.Load(), err)
	}
}

func TestPublicDiscoverySharesUpstreamCooldown(t *testing.T) {
	c, clk := discoveryTestShared(t)
	var calls atomic.Int32
	a := mockDiscovery(t, func(*http.Request) *http.Response {
		calls.Add(1)
		res := discoveryResponse(429, nil)
		res.Header.Set("Retry-After", "5")
		return res
	})
	b := mockDiscovery(t, func(*http.Request) *http.Response {
		calls.Add(1)
		return discoveryResponse(200, []byte("ok"))
	})
	a.shared, b.shared = c, c
	_, _ = a.get(context.Background(), "https://api.github.com/repos/example/first", 100)
	_, err := b.get(context.Background(), "https://api.github.com/repos/example/second", 100)
	var de *discoveryError
	if !errors.As(err, &de) || de.status != 429 || calls.Load() != 1 {
		t.Fatalf("upstream cooldown not shared: upstream=%d err=%v", calls.Load(), err)
	}
	clk.Advance(6 * time.Second)
	if _, err := b.get(context.Background(), "https://api.github.com/repos/example/second", 100); err != nil || calls.Load() != 2 {
		t.Fatalf("shared cooldown did not expire: upstream=%d err=%v", calls.Load(), err)
	}
}

func TestPublicDiscoveryDoesNotShareOversizedEntries(t *testing.T) {
	c, _ := discoveryTestShared(t)
	var calls atomic.Int32
	d := mockDiscovery(t, func(*http.Request) *http.Response {
		calls.Add(1)
		return discoveryResponse(200, []byte(strings.Repeat("x", discoverySharedEntryLimit+1)))
	})
	d.shared = c
	for range 2 {
		body, err := d.get(context.Background(), "https://codeload.github.com/example/repo/zip/main", discoveryArchiveLimit)
		if err != nil || len(body) != discoverySharedEntryLimit+1 {
			t.Fatalf("large source was rejected: bytes=%d err=%v", len(body), err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("oversized response entered shared cache: calls=%d", calls.Load())
	}
}
