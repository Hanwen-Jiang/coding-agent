package server

// File intelligence shares Runtime authority. The UI proxy supplies the session
// workspace and authenticated bearer; it never accepts an upstream URL or scope.
import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var fileWorkbenchPath = regexp.MustCompile(`^(?:trash|src_[a-f0-9]{32}/(?:details|actions|publish|versions/ver_[a-f0-9]{32}/content))$`)
var fileJobPath = regexp.MustCompile(`^fjob_[a-f0-9]{48}(?:/cancel|/artifacts/art_[a-f0-9]{32})?$`)

func (s *Server) handleFileWorkbench(w http.ResponseWriter, r *http.Request, id identity) {
	_, sess, ok := s.session(w, r, id)
	if !ok {
		return
	}
	tail := r.PathValue("file_tail")
	knowledge := r.PathValue("knowledge_action")
	if knowledge != "" {
		s.proxyKnowledge(w, r, id, sess.WorkspaceID, knowledge)
		return
	}
	job := strings.Contains(r.URL.Path, "/file-jobs")
	target := "/v1/files/" + tail
	if job {
		target = "/v1/file-jobs"
		if tail != "" {
			target += "/" + tail
		}
		if tail != "" && !fileJobPath.MatchString(tail) {
			http.NotFound(w, r)
			return
		}
	} else if !fileWorkbenchPath.MatchString(tail) {
		http.NotFound(w, r)
		return
	}
	if r.Method == "POST" && !((job && (tail == "" || strings.HasSuffix(tail, "/cancel"))) || (!job && (strings.HasSuffix(tail, "/actions") || strings.HasSuffix(tail, "/publish")))) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := ctxTimeout(r, 45*time.Second)
	defer cancel()
	upstream, err := http.NewRequestWithContext(ctx, r.Method, s.api.base+target+"?"+url.Values{"workspace_id": {sess.WorkspaceID}}.Encode(), http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		writeErr(w, r, 500, err)
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+id.token)
	if r.Method == "POST" {
		upstream.Header.Set("Content-Type", "application/json")
	}
	response, err := s.api.client.Do(upstream)
	if err != nil {
		writeMsg(w, r, 502, "文件服务暂时不可用")
		return
	}
	defer response.Body.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	for _, header := range []string{"Content-Type", "Content-Disposition", "Content-Security-Policy", "Retry-After"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	limit := int64(16 << 20)
	if strings.Contains(tail, "/artifacts/") || strings.HasSuffix(tail, "/content") {
		limit = maxSourceBytes
	}
	if response.StatusCode >= 400 {
		limit = 64 << 10
	}
	_, _ = io.Copy(w, io.LimitReader(response.Body, limit))
}

func (s *Server) proxyKnowledge(w http.ResponseWriter, r *http.Request, id identity, workspace, action string) {
	if !((r.Method == "GET" && action == "collection") || (r.Method == "POST" && (action == "index" || action == "search"))) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := ctxTimeout(r, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, s.api.base+"/v1/knowledge/"+action+"?"+filesQuery(workspace).Encode(), http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		writeErr(w, r, 500, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+id.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.api.slow.Do(req)
	if err != nil {
		writeMsg(w, r, 502, "检索服务暂时不可用")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 16<<20))
}
