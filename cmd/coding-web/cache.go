package main

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/adapters/valkey"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/cache"
	"github.com/agentruntime/agent-runtime/libs/go-core/secret"
)

var cachePrefixPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,96}:$`)

// newSharedCache reuses the Cache adapter, not task-service's runtime namespace.
// Configured failures never silently select an in-process cache. Errors below
// deliberately omit the URL, credentials, and raw network/server error text.
func newSharedCache(ctx context.Context, getenv func(string) string) (cache.Cache, func(), error) {
	noop := func() {}
	profile := strings.TrimSpace(getenv("CODING_PROFILE"))
	if profile != "" && profile != "development" && profile != "production" {
		return nil, noop, errors.New("CODING_PROFILE must be development or production")
	}
	address := strings.TrimSpace(getenv("CODING_VALKEY_URL"))
	if address == "" {
		if profile == "production" {
			return nil, noop, errors.New("CODING_VALKEY_URL is required in production")
		}
		return nil, noop, nil
	}
	prefix := strings.TrimSpace(getenv("CODING_CACHE_PREFIX"))
	if prefix == "" {
		prefix = "coding-web:"
	}
	if !cachePrefixPattern.MatchString(prefix) || prefix == "ar:" {
		return nil, noop, errors.New("CODING_CACHE_PREFIX must be an application-specific prefix ending in ':'")
	}
	maxConns := 16
	if raw := strings.TrimSpace(getenv("CODING_VALKEY_MAX_CONNS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 256 {
			return nil, noop, errors.New("CODING_VALKEY_MAX_CONNS must be between 1 and 256")
		}
		maxConns = n
	}
	p, err := valkey.New(valkey.Options{
		URL: secret.New(address), Prefix: prefix, MaxConns: maxConns,
		DialTimeout: 2 * time.Second, OpTimeout: 250 * time.Millisecond,
	})
	if err != nil {
		return nil, noop, errors.New("CODING_VALKEY_URL is invalid")
	}
	closeCache := func() { _ = p.Close() }
	check, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := p.Ping(check); err != nil {
		closeCache()
		return nil, noop, errors.New("configured Valkey is unavailable; check connectivity and credentials")
	}
	shared, err := p.BindPlatform("coding-web")
	if err != nil {
		closeCache()
		return nil, noop, errors.New("cannot bind coding-web cache")
	}
	return shared, closeCache, nil
}
