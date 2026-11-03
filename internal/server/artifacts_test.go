package server

// Runtime 的 docs/plan/n18-file-delivery-f3.md 第 2 节第 4 条与第 5 节第 6 条（F3，网页一侧）：交付区的文件。
//
//   - GET <会话>/tasks/{task}/artifacts：转调 Task API 的 ListTaskArtifacts（带用户的令牌），给出文件列表；下载地址换成 coding-web 自己的，
//     浏览器拿不到 Task API 与对象存储的地址；过期的标出来、不给地址。
//   - GET <会话>/tasks/{task}/artifacts/{artifact}：转发 Task API 的下载，原样带回内容与 Content-Type、Content-Disposition，
//     另外 nosniff、CSP sandbox、no-store（不用网页的 CSP）；Task API 的 404、410 原样，5xx 换成 502，错误里不带上游的正文。
//   - 任务要属于这个会话、会话要属于这个租户，否则 404，而且不去问 Task API；文件编号格式不对也一样。
//
// 实现之前（路由还没有，列文件是 404）这些测试跳过。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type artifactFile struct {
	mediaType, disposition, body string
	status                       int
}

// artifactTaskAPI 在 fakeTaskAPI 外面加上 ListTaskArtifacts 与下载路由。
type artifactTaskAPI struct {
	fake      *fakeTaskAPI
	mu        sync.Mutex
	artifacts map[string][]map[string]any
	files     map[string]artifactFile
	listCalls []string
	downloads []string
}

func (a *artifactTaskAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.URL.Path == "/agentruntime.taskapi.v1.TaskService/ListTaskArtifacts":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		task, _ := body["taskId"].(string)
		a.listCalls = append(a.listCalls, task+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"artifacts": a.artifacts[task]})
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.Contains(r.URL.Path, "/artifacts/"):
		a.downloads = append(a.downloads, r.URL.Path+" "+r.Header.Get("Authorization"))
		key := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
		f, ok := a.files[key]
		if !ok {
			http.Error(w, "upstream secret detail: obj://t/deliveries/x", http.StatusNotFound)
			return
		}
		if f.status != 0 {
			http.Error(w, "upstream secret detail: obj://t/deliveries/x", f.status)
			return
		}
		w.Header().Set("Content-Type", f.mediaType)
		w.Header().Set("Content-Disposition", f.disposition)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, f.body)
	default:
		a.fake.ServeHTTP(w, r)
	}
}

const (
	artReady   = "art_01J00000000000000000000000"
	artExpired = "art_01J00000000000000000000001"
)

// newArtifactSession 登录 acme/alice、建工作区与会话、提交一个任务（tsk_1），返回客户端、会话的接口前缀与上游。
func newArtifactSession(t *testing.T) (*httptest.Server, *client, string, *artifactTaskAPI) {
	t.Helper()
	up := &artifactTaskAPI{
		fake: &fakeTaskAPI{},
		artifacts: map[string][]map[string]any{"tsk_1": {
			{"artifactId": artReady, "name": "季度报告.csv", "path": "out/report.csv", "mediaType": "text/csv", "sizeBytes": "14", "sha256": "ab12",
				"createdAt": "2026-11-03T10:00:00Z", "expiresAt": "2026-11-10T10:00:00Z", "state": "TASK_ARTIFACT_STATE_READY", "downloadPath": "/v1/tasks/tsk_1/artifacts/" + artReady},
			{"artifactId": artExpired, "name": "old.txt", "path": "old.txt", "mediaType": "text/plain", "sizeBytes": "3", "sha256": "cd34",
				"createdAt": "2026-10-01T10:00:00Z", "expiresAt": "2026-10-08T10:00:00Z", "state": "TASK_ARTIFACT_STATE_EXPIRED", "downloadPath": "/v1/tasks/tsk_1/artifacts/" + artExpired},
		}},
		files: map[string]artifactFile{
			"tsk_1/artifacts/" + artReady:   {mediaType: "text/csv", disposition: "attachment; filename*=UTF-8''%E5%AD%A3%E5%BA%A6%E6%8A%A5%E5%91%8A.csv", body: "q,rate\n3,0.12\n"},
			"tsk_1/artifacts/" + artExpired: {status: http.StatusGone},
		},
	}
	upstream := httptest.NewServer(up)
	t.Cleanup(upstream.Close)
	ts, _, cfg, _ := newServerWith(t, func(c *Config) { c.TaskAPI = upstream.URL })
	up.fake.policies = cfg.PoliciesFile
	c := newClient(t, ts.URL)
	pid := sampleProject(t, c)
	api, _ := newSession(t, c, cfg, "acme", pid)
	if code, task, raw := c.do("POST", api+"/tasks", map[string]string{"message": "做一份季度报告"}); code != http.StatusCreated || task["id"] != "tsk_1" {
		t.Fatalf("提交任务失败：%d %s", code, raw)
	}
	// 实现之前没有这个路由：任务明明在，列文件却是 404。实现之后去掉这段跳过。
	res, err := c.http.Get(ts.URL + api + "/tasks/tsk_1/artifacts")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		t.Skip("交付区的接口还没写：先审验收测试（Runtime 的 docs/plan/n18-file-delivery-f3.md）")
	}
	return ts, c, api, up
}

func (c *client) get(path string) (*http.Response, string) {
	c.t.Helper()
	res, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

func TestTaskArtifactsAreListedWithLocalDownloadLinks(t *testing.T) {
	_, c, api, up := newArtifactSession(t)
	code, out, raw := c.do("GET", api+"/tasks/tsk_1/artifacts", nil)
	if code != http.StatusOK {
		t.Fatalf("列文件失败：%d %s", code, raw)
	}
	list, _ := out["artifacts"].([]any)
	if len(list) != 2 {
		t.Fatalf("应有两个文件：%s", raw)
	}
	ready, expired := list[0].(map[string]any), list[1].(map[string]any)
	if ready["id"] != artReady || ready["name"] != "季度报告.csv" || ready["media_type"] != "text/csv" || ready["size_bytes"] != float64(14) || ready["expired"] != false {
		t.Fatalf("文件信息不对：%v", ready)
	}
	if ready["url"] != api+"/tasks/tsk_1/artifacts/"+artReady {
		t.Fatalf("下载地址应是 coding-web 自己的：%v", ready["url"])
	}
	if expired["expired"] != true || expired["url"] != "" {
		t.Fatalf("过期的文件要标出来、不给下载地址：%v", expired)
	}
	if strings.Contains(raw, "/v1/tasks/") || strings.Contains(raw, "obj://") || strings.Contains(raw, "downloadPath") {
		t.Fatalf("不把 Task API 的地址给浏览器：%s", raw)
	}
	if len(up.listCalls) != 1 || !strings.HasPrefix(up.listCalls[0], "tsk_1 Bearer ") {
		t.Fatalf("要带用户的令牌转调 ListTaskArtifacts：%v", up.listCalls)
	}
}

func TestTaskArtifactDownloadIsForwardedWithSafeHeaders(t *testing.T) {
	_, c, api, up := newArtifactSession(t)
	res, body := c.get(api + "/tasks/tsk_1/artifacts/" + artReady)
	if res.StatusCode != http.StatusOK || body != "q,rate\n3,0.12\n" {
		t.Fatalf("下载失败：%d %q", res.StatusCode, body)
	}
	want := map[string]string{
		"Content-Type":            "text/csv",
		"Content-Disposition":     "attachment; filename*=UTF-8''%E5%AD%A3%E5%BA%A6%E6%8A%A5%E5%91%8A.csv",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "sandbox",
		"Cache-Control":           "no-store",
	}
	for k, v := range want {
		if got := res.Header.Get(k); got != v {
			t.Fatalf("%s 应是 %q，得到 %q", k, v, got)
		}
	}
	if len(up.downloads) != 1 || !strings.HasPrefix(up.downloads[0], "/v1/tasks/tsk_1/artifacts/"+artReady+" Bearer ") {
		t.Fatalf("要带用户的令牌转发下载：%v", up.downloads)
	}
}

func TestTaskArtifactDownloadErrorsKeepTheirMeaning(t *testing.T) {
	_, c, api, up := newArtifactSession(t)
	up.files["tsk_1/artifacts/art_01J00000000000000000000002"] = artifactFile{status: http.StatusInternalServerError}
	for _, tc := range []struct {
		id     string
		status int
	}{
		{artExpired, http.StatusGone},
		{"art_01J00000000000000000000009", http.StatusNotFound},
		{"art_01J00000000000000000000002", http.StatusBadGateway},
	} {
		res, body := c.get(api + "/tasks/tsk_1/artifacts/" + tc.id)
		if res.StatusCode != tc.status {
			t.Fatalf("%s：应返回 %d，得到 %d", tc.id, tc.status, res.StatusCode)
		}
		if strings.Contains(body, "obj://") || strings.Contains(body, "upstream secret") {
			t.Fatalf("%s：错误里不带上游的正文：%s", tc.id, body)
		}
	}
}

func TestArtifactsOutsideTheSessionAreNotReachable(t *testing.T) {
	ts, c, api, up := newArtifactSession(t)
	calls := len(up.listCalls) + len(up.downloads)

	other := newClient(t, ts.URL)
	other.do("POST", "/api/login", map[string]string{"tenant": "globex", "user": "bob"})
	if res, _ := other.get(api + "/tasks/tsk_1/artifacts/" + artReady); res.StatusCode != http.StatusNotFound {
		t.Fatalf("别的租户应 404，得到 %d", res.StatusCode)
	}
	if code, _, _ := other.do("GET", api+"/tasks/tsk_1/artifacts", nil); code != http.StatusNotFound {
		t.Fatalf("别的租户列文件应 404，得到 %d", code)
	}
	if res, _ := c.get(api + "/tasks/tsk_99/artifacts/" + artReady); res.StatusCode != http.StatusNotFound {
		t.Fatalf("不属于这个会话的任务应 404，得到 %d", res.StatusCode)
	}
	if res, _ := c.get(api + "/tasks/tsk_1/artifacts/..%2F..%2Fsecrets"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("格式不对的文件编号应 404，得到 %d", res.StatusCode)
	}
	if got := len(up.listCalls) + len(up.downloads); got != calls {
		t.Fatalf("越权与格式不对的请求不该去问 Task API：多了 %d 次", got-calls)
	}
}
