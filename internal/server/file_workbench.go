package server

// File intelligence shares Runtime authority. The UI proxy supplies the session
// workspace and authenticated bearer; it never accepts an upstream URL or scope.
import (
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var fileWorkbenchPath = regexp.MustCompile(`^(?:trash|src_[a-f0-9]{32}/(?:details|actions|publish|versions/ver_[a-f0-9]{32}/content))$`)
var fileJobPath = regexp.MustCompile(`^fjob_[a-f0-9]{48}(?:/cancel|/artifacts/art_[a-f0-9]{32})?$`)

func (s *Server) handleFileWorkbench(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	tail := strings.TrimPrefix(c.Param("file_tail"), "/") // Gin 的 *file_tail 带开头的斜杠
	knowledge := c.Param("knowledge_action")
	if knowledge != "" {
		s.proxyKnowledge(c, id, sess.WorkspaceID, knowledge)
		return
	}
	job := strings.Contains(c.Request.URL.Path, "/file-jobs")
	target := "/v1/files/" + tail
	if job {
		target = "/v1/file-jobs"
		if tail != "" {
			target += "/" + tail
		}
		if tail != "" && !fileJobPath.MatchString(tail) {
			http.NotFound(c.Writer, c.Request)
			return
		}
	} else if !fileWorkbenchPath.MatchString(tail) {
		http.NotFound(c.Writer, c.Request)
		return
	}
	if c.Request.Method == "POST" && !((job && (tail == "" || strings.HasSuffix(tail, "/cancel"))) || (!job && (strings.HasSuffix(tail, "/actions") || strings.HasSuffix(tail, "/publish")))) {
		http.NotFound(c.Writer, c.Request)
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 45*time.Second)
	defer cancel()
	upstream, err := http.NewRequestWithContext(ctx, c.Request.Method, s.api.base+target+"?"+url.Values{"workspace_id": {sess.WorkspaceID}}.Encode(), http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10))
	if err != nil {
		writeErr(c, 500, err)
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+id.token)
	if c.Request.Method == "POST" {
		upstream.Header.Set("Content-Type", "application/json")
	}
	response, err := s.api.client.Do(upstream)
	if err != nil {
		writeMsg(c, 502, "文件服务暂时不可用")
		return
	}
	defer response.Body.Close()
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	for _, header := range []string{"Content-Type", "Content-Disposition", "Content-Security-Policy", "Retry-After"} {
		if value := response.Header.Get(header); value != "" {
			c.Header(header, value)
		}
	}
	c.Status(response.StatusCode)
	limit := int64(16 << 20)
	if strings.Contains(tail, "/artifacts/") || strings.HasSuffix(tail, "/content") {
		limit = maxSourceBytes
	}
	if response.StatusCode >= 400 {
		limit = 64 << 10
	}
	_, _ = io.Copy(c.Writer, io.LimitReader(response.Body, limit))
}

func (s *Server) proxyKnowledge(c *gin.Context, id identity, workspace, action string) {
	if !((c.Request.Method == "GET" && action == "collection") || (c.Request.Method == "POST" && (action == "index" || action == "search"))) {
		http.NotFound(c.Writer, c.Request)
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, c.Request.Method, s.api.base+"/v1/knowledge/"+action+"?"+filesQuery(workspace).Encode(), http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10))
	if err != nil {
		writeErr(c, 500, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+id.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.api.slow.Do(req)
	if err != nil {
		writeMsg(c, 502, "检索服务暂时不可用")
		return
	}
	defer resp.Body.Close()
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "application/json")
	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, io.LimitReader(resp.Body, 16<<20))
}
