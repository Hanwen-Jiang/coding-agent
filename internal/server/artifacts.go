package server

// 交付区的文件（Runtime 的 docs/plan/n18-file-delivery-f3.md 第 2 节第 4 条，F3；验收测试在 artifacts_test.go）。
//
//   - GET <会话>/tasks/{task}/artifacts：带用户的令牌转调 Task API 的 ListTaskArtifacts；下载地址换成这里的，浏览器拿不到 Task API
//     与对象存储的地址；过期的（与不认识的状态）标成 expired，不给地址。
//   - GET <会话>/tasks/{task}/artifacts/{artifact}：转发 Task API 的下载（它每次重新核对租户、Task 与有效期），带回内容与
//     Content-Type、Content-Disposition，另加 nosniff、CSP sandbox、no-store；404、410 原样，其余失败是 502，不带上游的正文。
//   - 任务要属于这个会话（s.task），文件编号格式不对的不去问 Task API。

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var artifactIDPattern = regexp.MustCompile(`^art_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

// taskArtifact 是 ListTaskArtifacts 返回的一项（protojson：int64 写成字符串）。
type taskArtifact struct {
	ArtifactID string      `json:"artifactId"`
	Name       string      `json:"name"`
	Path       string      `json:"path"`
	MediaType  string      `json:"mediaType"`
	SizeBytes  json.Number `json:"sizeBytes"`
	SHA256     string      `json:"sha256"`
	CreatedAt  string      `json:"createdAt"`
	ExpiresAt  string      `json:"expiresAt"`
	State      string      `json:"state"`
}

func (s *Server) handleListArtifacts(c *gin.Context, id identity) {
	_, t, ok := s.task(c, id)
	if !ok {
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 15*time.Second)
	defer cancel()
	var out struct {
		Artifacts []taskArtifact `json:"artifacts"`
	}
	if err := s.api.Call(ctx, id.token, "ListTaskArtifacts", map[string]string{"taskId": t.ID}, &out); err != nil {
		writeAPIError(c, err)
		return
	}
	base := strings.TrimSuffix(c.Request.URL.Path, "/artifacts")
	list := make([]map[string]any, 0, len(out.Artifacts))
	for _, a := range out.Artifacts {
		size, _ := a.SizeBytes.Int64()
		expired := a.State != "TASK_ARTIFACT_STATE_READY"
		url := ""
		if !expired && artifactIDPattern.MatchString(a.ArtifactID) {
			url = base + "/artifacts/" + a.ArtifactID
		}
		list = append(list, map[string]any{
			"id": a.ArtifactID, "name": a.Name, "path": a.Path, "media_type": a.MediaType, "size_bytes": size, "sha256": a.SHA256,
			"created_at": a.CreatedAt, "expires_at": a.ExpiresAt, "expired": expired, "url": url,
		})
	}
	writeJSON(c, http.StatusOK, map[string]any{"artifacts": list})
}

func (s *Server) handleArtifactDownload(c *gin.Context, id identity) {
	_, t, ok := s.task(c, id)
	if !ok {
		return
	}
	artifact := c.Param("artifact")
	if !artifactIDPattern.MatchString(artifact) {
		writeMsg(c, http.StatusNotFound, "文件不存在")
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 2*time.Minute)
	defer cancel()
	res, err := s.api.Artifact(ctx, id.token, t.ID, artifact)
	if err != nil {
		writeMsg(c, http.StatusBadGateway, "连不上 task-service")
		return
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		writeMsg(c, http.StatusNotFound, "文件不存在")
		return
	case http.StatusGone:
		writeMsg(c, http.StatusGone, "文件已过期")
		return
	default:
		writeMsg(c, http.StatusBadGateway, "暂时下载不了这个文件")
		return
	}
	h := c.Writer.Header()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Disposition"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, res.Body)
}
