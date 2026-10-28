package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

// 不看页面、直接到站点根上找图标的客户端（Safari、书签、iOS 主屏幕）拿到的是图标，不是单页应用的 index.html。
func TestRootIconsAreImagesNotTheApp(t *testing.T) {
	for path, want := range map[string]struct {
		contentType string
		magic       []byte
	}{
		"/favicon.ico":                      {"image/x-icon", []byte{0, 0, 1, 0}},
		"/apple-touch-icon.png":             {"image/png", []byte("\x89PNG")},
		"/apple-touch-icon-precomposed.png": {"image/png", []byte("\x89PNG")},
		"/assets/favicon.svg":               {"image/svg+xml", []byte("<svg")},
	} {
		res, body := get(t, path)
		if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), want.contentType) || !bytes.HasPrefix(body, want.magic) {
			t.Errorf("%s：%d %s %q", path, res.StatusCode, res.Header.Get("Content-Type"), body[:min(len(body), 8)])
		}
	}
	_, page := get(t, "/")
	for _, link := range []string{`href="/favicon.ico"`, `href="/assets/favicon.svg"`, `rel="apple-touch-icon" href="/apple-touch-icon.png"`} {
		if !bytes.Contains(page, []byte(link)) {
			t.Errorf("index.html 少了 %s", link)
		}
	}
	if res, _ := get(t, "/some/route"); !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Errorf("别的路径仍回单页应用：%s", res.Header.Get("Content-Type"))
	}
}
