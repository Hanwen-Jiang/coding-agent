package server

// AUTH-03 里不用人定的两件（Runtime 仓库的 docs/plan/auth-tokens-2026-11-03.md 第 2 节）：
//
//  1. OAuth 加 PKCE（S256）：支持的平台（GitHub、GitLab、Gitea、Google）的授权地址带 code_challenge，换令牌时带
//     code_verifier，两者对得上；code_verifier 只在只对 /api/auth/<平台> 有效的 HttpOnly Cookie 里，不进授权地址。
//     不确定支持的平台（Gitee、Bitbucket）照旧不带。
//  2. JWT 模式下发布 JWKS（/.well-known/jwks.json）：只有公钥（OKP Ed25519，kid、alg、use），Runtime 或网关可以按它登记签发方；
//     没有 JWT 私钥时 404。

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestOAuthUsesPKCEWhereTheProviderSupportsIt(t *testing.T) {
	cases := []struct {
		id   string
		pkce bool
		user map[string]any
	}{
		{"gitlab", true, map[string]any{"id": 77, "username": "octo", "name": "Octo GL"}},
		{"gitee", false, map[string]any{"id": 9, "login": "li-lei", "name": "李雷"}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			forge := newFakeForge(t, tc.id, tc.user)
			var mu sync.Mutex
			var verifiers []string
			rec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					_ = r.ParseForm()
					mu.Lock()
					verifiers = append(verifiers, r.Form.Get("code_verifier"))
					mu.Unlock()
				}
				forge.serve(w, r)
			}))
			t.Cleanup(rec.Close)
			ts, _, srv := newOAuthServer(t, tc.id)
			pointProvider(srv, tc.id, rec.URL)
			c := newClient(t, ts.URL)
			c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			res, err := c.http.Get(ts.URL + "/api/auth/" + tc.id + "/login")
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			loc, _ := url.Parse(res.Header.Get("Location"))
			challenge, method := loc.Query().Get("code_challenge"), loc.Query().Get("code_challenge_method")
			if tc.pkce != (challenge != "" && method == "S256") || (!tc.pkce && (challenge != "" || method != "")) {
				t.Fatalf("PKCE=%v：授权地址 %s", tc.pkce, loc)
			}
			if strings.Contains(loc.String(), "code_verifier") {
				t.Fatal("code_verifier 不进授权地址")
			}
			res, err = c.http.Get(ts.URL + "/api/auth/" + tc.id + "/callback?code=good-code&state=" + loc.Query().Get("state"))
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if got := res.Header.Get("Location"); got != "/" {
				t.Fatalf("登录应成功：%s", got)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(verifiers) == 0 {
				t.Fatal("没有换令牌")
			}
			if !tc.pkce {
				if verifiers[0] != "" {
					t.Fatalf("不支持 PKCE 的平台不带 code_verifier：%q", verifiers[0])
				}
				return
			}
			sum := sha256.Sum256([]byte(verifiers[0]))
			if len(verifiers[0]) < 43 || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				t.Fatalf("code_verifier 与 code_challenge 对得上（S256）：%q / %q", verifiers[0], challenge)
			}
		})
	}
}

func TestJWKSPublishesOnlyThePublicKey(t *testing.T) {
	key := newEd25519(t)
	ts, _, _, _ := newServerWith(t, func(c *Config) {
		c.SigningKey, c.JWTKey, c.JWTKeyID, c.TokenKey = nil, key, "k2026", tokenKey32(4)
	})
	res, err := http.Get(ts.URL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var doc struct {
		Keys []map[string]string `json:"keys"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&doc) != nil || len(doc.Keys) != 1 {
		t.Fatalf("JWKS：%d %+v", res.StatusCode, doc)
	}
	k := doc.Keys[0]
	if k["kty"] != "OKP" || k["crv"] != "Ed25519" || k["kid"] != "k2026" || k["alg"] != "EdDSA" || k["use"] != "sig" {
		t.Fatalf("JWK 的字段：%v", k)
	}
	if k["d"] != "" {
		t.Fatal("JWKS 里不能有私钥（d）")
	}
	x, err := base64.RawURLEncoding.DecodeString(k["x"])
	if err != nil || !ed25519.PublicKey(x).Equal(key.Public()) {
		t.Fatalf("x 是公钥：%v", err)
	}

	plain, _, _, _ := newServerWith(t, nil)
	res, err = http.Get(plain.URL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("没有 JWT 私钥时没有 JWKS：%d", res.StatusCode)
	}
}
