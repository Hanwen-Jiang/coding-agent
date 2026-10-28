package web

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func getFrom(t *testing.T, h http.Handler, path string) (*http.Response, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

func get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	return getFrom(t, Handler(), path)
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
	for _, link := range []string{`href="/favicon.ico"`, `href="/assets/favicon.svg?v=`, `rel="apple-touch-icon" href="/apple-touch-icon.png"`} {
		if !bytes.Contains(page, []byte(link)) {
			t.Errorf("index.html 少了 %s", link)
		}
	}
	if res, _ := get(t, "/some/route"); !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Errorf("别的路径仍回单页应用：%s", res.Header.Get("Content-Type"))
	}
}

var assetRef = regexp.MustCompile(`"/assets/([a-z0-9._-]+)(\?v=([0-9a-f]+))?"`)

// 页面引用的 /assets/ 都带这一版的版本号，JS 模块之间的 import 也带；带版本号的长期缓存，不带的每次都问。
// Cloudflare 把 /assets/ 的 no-cache 改成 4 小时：没有版本号时重新部署后浏览器会拿旧的 JS 配新的页面。
func TestAssetURLsCarryTheBuildVersion(t *testing.T) {
	_, page := get(t, "/")
	refs := assetRef.FindAllSubmatch(page, -1)
	if len(refs) < 3 {
		t.Fatalf("页面里的 /assets/ 引用太少：%s", page)
	}
	version := string(refs[0][3])
	for _, ref := range refs {
		if string(ref[3]) == "" || string(ref[3]) != version {
			t.Errorf("引用 %s 没带同一个版本号（%s）", ref[0], version)
		}
	}
	res, app := get(t, "/assets/app.js?v="+version)
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("带版本号的 app.js：%d %s", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	if !bytes.Contains(app, []byte(`from "./i18n.js?v=`+version+`"`)) {
		t.Error("app.js 引 i18n.js 也要带版本号（模块按 URL 缓存）")
	}
	for _, p := range []string{"/assets/app.js", "/assets/app.js?v=old"} {
		if res, _ := get(t, p); res.Header.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s 不是这一版：应每次都问，实际 %s", p, res.Header.Get("Cache-Control"))
		}
	}
	for _, p := range []string{"/assets/nope.js", "/assets/index.html"} {
		if res, _ := get(t, p); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s 应 404，实际 %d", p, res.StatusCode)
		}
	}
}

func TestAnyChangedFileChangesTheVersion(t *testing.T) {
	site := func(js string) fs.FS {
		return fstest.MapFS{
			"index.html": {Data: []byte(`<script type="module" src="/assets/app.js"></script>`)},
			"app.js":     {Data: []byte(`import { t } from "./i18n.js";`)},
			"i18n.js":    {Data: []byte(js)},
		}
	}
	a, b := load(site("export const t = 1;")), load(site("export const t = 2;"))
	if a.version == b.version {
		t.Fatal("只改了 i18n.js，版本号也要变")
	}
	if want := `"./i18n.js?v=` + b.version + `"`; !bytes.Contains(b.files["app.js"], []byte(want)) {
		t.Fatalf("app.js 的 import 没改写：%s", b.files["app.js"])
	}
	if a2 := load(site("export const t = 1;")); a2.version != a.version {
		t.Fatal("同样的文件版本号要相同（多副本时页面与资源对得上）")
	}
}
