package server

// G-02 的接线：Webhook 挂在 App 设置里登记的 /api/webhooks/github，不要登录、不要 X-Coding-Web 头（GitHub 带签名，由收件箱验）；
// 配了 App 但还没有收件箱（CODING_GITHUB_DSN）时回 503，GitHub 记为失败，配上库之后对账会补投。

import (
	"net/http"
	"strings"
	"testing"
)

func TestWebhookRouteReachesTheInboxWithoutLogin(t *testing.T) {
	var got int
	ts, _, _, _ := newServerWith(t, func(c *Config) {
		c.Webhook = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got++
			w.WriteHeader(http.StatusAccepted)
		})
	})
	res, err := http.Post(ts.URL+"/api/webhooks/github", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted || got != 1 {
		t.Fatalf("交给收件箱：%d %d", res.StatusCode, got)
	}
}

func TestWebhookRouteIs503WhileTheInboxIsNotConfigured(t *testing.T) {
	keyFile, _ := writeAppKey(t)
	app, err := GitHubAppFromEnv(envFunc(appEnv(keyFile)))
	if err != nil {
		t.Fatal(err)
	}
	ts, _, _, _ := newServerWith(t, func(c *Config) { c.GitHubApp = app })
	res, err := http.Post(ts.URL+"/api/webhooks/github", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("没有收件箱时 503：%d", res.StatusCode)
	}
}
