package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cacheEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestCacheConfigFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"production missing endpoint", map[string]string{"CODING_PROFILE": "production"}},
		{"unknown profile", map[string]string{"CODING_PROFILE": "prodction"}},
		{"invalid URL", map[string]string{"CODING_VALKEY_URL": "broken://user:do-not-print@localhost"}},
		{"runtime namespace", map[string]string{"CODING_VALKEY_URL": "redis://:do-not-print@localhost", "CODING_CACHE_PREFIX": "ar:"}},
		{"invalid pool", map[string]string{"CODING_VALKEY_URL": "redis://:do-not-print@localhost", "CODING_VALKEY_MAX_CONNS": "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, closeCache, err := newSharedCache(context.Background(), cacheEnv(tc.env))
			defer closeCache()
			if err == nil || c != nil {
				t.Fatal("bad configuration silently accepted")
			}
			if strings.Contains(err.Error(), "do-not-print") {
				t.Fatal("cache credential leaked in error")
			}
		})
	}
	c, closeCache, err := newSharedCache(context.Background(), cacheEnv(nil))
	defer closeCache()
	if err != nil || c != nil {
		t.Fatal("development without Valkey must retain local behavior")
	}
}

// Run against an isolated, authenticated local server. No production data or
// credentials are used, and the temporary server never persists its contents.
func localCacheServer(t *testing.T) string {
	t.Helper()
	var binary string
	for _, name := range []string{"valkey-server", "redis-server"} {
		if path, err := exec.LookPath(name); err == nil {
			binary = path
			break
		}
	}
	if binary == "" {
		t.Skip("install valkey-server or redis-server for the isolated cache integration test")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(random[:])
	dir := t.TempDir()
	config := fmt.Sprintf("bind 127.0.0.1\nport %d\nprotected-mode yes\nrequirepass %s\nsave \"\"\nappendonly no\nlogfile %q\ndir %q\n", port, password, filepath.Join(dir, "server.log"), dir)
	cmd := exec.Command(binary, "-")
	cmd.Stdin = strings.NewReader(config)
	cmd.Stdout, cmd.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		t.Fatal("cannot start isolated cache server")
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	address := fmt.Sprintf("redis://:%s@127.0.0.1:%d/0", password, port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, closeCache, err := newSharedCache(context.Background(), cacheEnv(map[string]string{"CODING_VALKEY_URL": address}))
		closeCache()
		if err == nil {
			return address
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("isolated cache server did not become ready")
	return ""
}

func TestSharedCacheAcrossClientsAndNamespaces(t *testing.T) {
	address := localCacheServer(t)
	ctx := context.Background()
	config := map[string]string{"CODING_PROFILE": "production", "CODING_VALKEY_URL": address}
	a, closeA, err := newSharedCache(ctx, cacheEnv(config))
	if err != nil {
		t.Fatal(err)
	}
	defer closeA()
	b, closeB, err := newSharedCache(ctx, cacheEnv(config))
	if err != nil {
		t.Fatal(err)
	}
	defer closeB()
	if err := a.Set(ctx, "shared", []byte("public metadata"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if value, found, err := b.Get(ctx, "shared"); err != nil || !found || string(value) != "public metadata" {
		t.Fatal("independent clients did not share cache data")
	}
	config["CODING_CACHE_PREFIX"] = "coding-web-test-other:"
	c, closeC, err := newSharedCache(ctx, cacheEnv(config))
	if err != nil {
		t.Fatal(err)
	}
	defer closeC()
	if _, found, err := c.Get(ctx, "shared"); err != nil || found {
		t.Fatal("cache namespaces overlap")
	}
	if _, err := a.Delete(ctx, "shared"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := b.Get(ctx, "shared"); err != nil || found {
		t.Fatal("deletion did not cross client boundary")
	}
	if err := a.Set(ctx, "short", []byte("expires"), 40*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, found, err := b.Get(ctx, "short"); err != nil || found {
		t.Fatal("cache TTL was not enforced")
	}
	bad := strings.Replace(address, "redis://:", "redis://:wrong-", 1)
	if _, closeBad, err := newSharedCache(ctx, cacheEnv(map[string]string{"CODING_VALKEY_URL": bad})); err == nil {
		closeBad()
		t.Fatal("wrong authentication silently accepted")
	}
}
