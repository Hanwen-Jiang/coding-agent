package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const workspaceDoneCacheMax = 4096

// workspaceHotState accelerates high-concurrency async workspace operations:
// - activeRunners avoids redundant open/flock syscalls when this replica already owns an op;
// - done caches immutable terminal records (Status != 0) to skip disk I/O and mutex contention;
// - readFlights coalesces concurrent status-poll disk reads for the same in-flight op.
type workspaceHotState struct {
	activeRunners sync.Map // key: tenant+"\x00"+opID -> struct{}
	remoteOwned   sync.Map // key: tenant+"\x00"+opID -> int64 unix nano backoff deadline
	admitFlights  singleflight.Group
	readFlights   singleflight.Group
	doneMu        sync.RWMutex
	done          map[string]*workspaceHTTPRecord
}

const workspaceRemoteLockBackoff = 50 * time.Millisecond

func workspaceHotKey(tenant, opID string) string { return tenant + "\x00" + opID }

func (h *workspaceHotState) getDone(key string) (*workspaceHTTPRecord, bool) {
	h.doneMu.RLock()
	rec, ok := h.done[key]
	h.doneMu.RUnlock()
	return rec, ok
}

func (h *workspaceHotState) putDone(key string, rec *workspaceHTTPRecord) {
	if rec == nil || rec.Status == 0 {
		return
	}
	cp := *rec
	cp.Response = append(json.RawMessage(nil), rec.Response...)
	h.doneMu.Lock()
	if h.done == nil {
		h.done = make(map[string]*workspaceHTTPRecord, 64)
	}
	if len(h.done) >= workspaceDoneCacheMax {
		// Simple bounded reset prevents unbounded memory growth under churn.
		h.done = make(map[string]*workspaceHTTPRecord, 64)
	}
	h.done[key] = &cp
	h.doneMu.Unlock()
}

// The HTTP journal retains application projection and response state only. Each
// sandbox mutation remains an existing durable Runtime workspace operation.
// A stopped web process resumes only after its owner authenticates a poll.
type workspaceHTTPRecord struct {
	ID       string                     `json:"id"`
	User     string                     `json:"user"`
	Kind     string                     `json:"kind"`
	Digest   string                     `json:"digest"`
	Method   string                     `json:"method"`
	Language string                     `json:"language"`
	URL      string                     `json:"url"`
	Body     json.RawMessage            `json:"body"`
	Paths    map[string]string          `json:"paths"`
	Created  time.Time                  `json:"created"`
	Status   int                        `json:"status,omitempty"`
	Response json.RawMessage            `json:"response,omitempty"`
	Facts    map[string]json.RawMessage `json:"facts"`
}

type workspaceHTTPJournal struct {
	server   *Server
	tenant   string
	identity identity // memory only: never marshalled into the record
	record   *workspaceHTTPRecord
}
type workspaceHTTPKey struct{}
type workspaceHTTPAbort struct{ err error }

func workspaceJournal(ctx context.Context) *workspaceHTTPJournal {
	j, _ := ctx.Value(workspaceHTTPKey{}).(*workspaceHTTPJournal)
	return j
}
func workspaceHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Store) workspaceOperationFile(tenant, id string) string {
	return filepath.Join(s.dir, tenant, "workspace-operations", id+".json")
}
func (j *workspaceHTTPJournal) save() error {
	j.server.store.mu.Lock()
	path := j.server.store.workspaceOperationFile(j.tenant, j.record.ID)
	err := writeWorkspaceHTTPRecord(path, j.record)
	j.server.store.mu.Unlock()
	if err == nil && j.record.Status != 0 {
		j.server.wsHot.putDone(workspaceHotKey(j.tenant, j.record.ID), j.record)
	}
	return err
}

func writeWorkspaceHTTPRecord(path string, record *workspaceHTTPRecord) error {
	if err := writeFileAtomic(path, record); err != nil {
		return err
	}
	// Admission and step evidence must survive a host restart, not only a process exit.
	// Open for writing: Windows' FlushFileBuffers rejects a read-only handle with
	// "Access is denied"; on Linux fsync works on either.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	_ = f.Close()
	if err != nil {
		return err
	}
	// Persist the rename's directory entry (a no-op on Windows, see syncDir).
	return syncDir(filepath.Dir(path))
}
func (j *workspaceHTTPJournal) put(key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	j.record.Facts[key] = b
	return j.save()
}
func (j *workspaceHTTPJournal) get(key string, value any) bool {
	raw, ok := j.record.Facts[key]
	if !ok {
		return false
	}
	if err := json.Unmarshal(raw, value); err != nil {
		panic(workspaceHTTPAbort{err})
	}
	return true
}
func workspaceFact(ctx context.Context, key string, value any) bool {
	j := workspaceJournal(ctx)
	return j != nil && j.get(key, value)
}
func saveWorkspaceFact(ctx context.Context, key string, value any) {
	if j := workspaceJournal(ctx); j != nil {
		if err := j.put(key, value); err != nil {
			panic(workspaceHTTPAbort{err})
		}
	}
}
func workspaceResourceID(ctx context.Context, kind, fallback string) string {
	if j := workspaceJournal(ctx); j != nil {
		return "w" + workspaceHash(j.record.ID, kind)[:24]
	}
	return fallback
}
func workspaceCreated(ctx context.Context) time.Time {
	if j := workspaceJournal(ctx); j != nil {
		return j.record.Created
	}
	return time.Now().UTC()
}

func (s *Server) workspaceHTTPHandler(kind string) authedHandler {
	switch kind {
	case "create_project":
		return s.handleCreateProject
	case "delete_project":
		return s.handleDeleteProject
	case "create_session":
		return s.handleCreateSession
	case "duplicate_session":
		return s.handleDuplicateSession
	case "delete_session":
		return s.handleDeleteSession
	case "attach":
		return s.handleAttachRepo
	case "detach":
		return s.handleDetachRepo
	case "commit":
		return s.handleCommit
	case "discard":
		return s.handleDiscard
	case "publish":
		return s.handlePublish
	case "sync":
		return s.handleSync
	case "reset":
		return s.handleResetBranch
	}
	return nil
}

// Reject unknown fields before persistence. Git credentials, secret environment
// values, cookies and Authorization never enter this journal.
func workspaceHTTPBody(kind string, body []byte) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	allowed := map[string]string{
		"create_project": "name git_url branch sample layout", "create_session": "title environment_id personal_config task_settings",
		"duplicate_session": "title", "attach": "repo", "detach": "discard_changes", "commit": "message", "publish": "title body",
	}[kind]
	for key := range fields {
		if !strings.Contains(" "+allowed+" ", " "+key+" ") {
			return nil, fmt.Errorf("unsupported workspace field %q", key)
		}
		if key == "repo" || key == "personal_config" || key == "task_settings" {
			continue
		}
		if key == "discard_changes" {
			var value bool
			if err := json.Unmarshal(fields[key], &value); err != nil {
				return nil, fmt.Errorf("invalid %s", key)
			}
		} else {
			var value string
			if err := json.Unmarshal(fields[key], &value); err != nil {
				return nil, fmt.Errorf("invalid %s", key)
			}
			if key == "git_url" && !workspaceURLWithoutCredential(value) {
				return nil, errors.New("Git URL must not contain credentials")
			}
		}
	}
	if raw := fields["repo"]; raw != nil {
		var repo map[string]json.RawMessage
		if err := json.Unmarshal(raw, &repo); err != nil {
			return nil, err
		}
		for key := range repo {
			if !strings.Contains(" name url ref path access primary ", " "+key+" ") {
				return nil, fmt.Errorf("unsupported repository field %q", key)
			}
			if key == "primary" {
				var value bool
				if err := json.Unmarshal(repo[key], &value); err != nil {
					return nil, errors.New("invalid primary")
				}
			} else {
				var value string
				if err := json.Unmarshal(repo[key], &value); err != nil {
					return nil, fmt.Errorf("invalid repository %s", key)
				}
				if key == "url" && !workspaceURLWithoutCredential(value) {
					return nil, errors.New("Git URL must not contain credentials")
				}
			}
		}
	}
	// personal_config only overrides optional references; never package contents.
	if raw := fields["personal_config"]; raw != nil {
		var ref map[string]json.RawMessage
		if err := json.Unmarshal(raw, &ref); err != nil {
			return nil, err
		}
		for key := range ref {
			if !strings.Contains(" skill_names mcp_connection_ids ", " "+key+" ") {
				return nil, fmt.Errorf("unsupported personal config reference field %q", key)
			}
			var names []string
			if err := json.Unmarshal(ref[key], &names); err != nil {
				return nil, fmt.Errorf("invalid personal config %s", key)
			}
		}
	}
	// Keep the async admission contract aligned with handleCreateSession. Decode
	// strictly before journaling so unknown nested fields cannot persist secrets.
	if raw := fields["task_settings"]; raw != nil {
		var settings *sessionTaskSettings
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&settings); err != nil {
			return nil, fmt.Errorf("invalid task_settings: %w", err)
		}
		if err := settings.validate(); err != nil {
			return nil, err
		}
		canonical, err := json.Marshal(settings)
		if err != nil {
			return nil, err
		}
		fields["task_settings"] = canonical
	}
	return json.Marshal(fields)
}

func workspaceURLWithoutCredential(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

var workspaceRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
var workspaceOperationID = regexp.MustCompile(`^who-[a-f0-9]{64}$`)

func (s *Server) workspaceHTTP(kind string) authedHandler {
	return func(c *gin.Context, id identity) {
		handler := s.workspaceHTTPHandler(kind)
		// Older clients keep the synchronous response shape. The browser opts in.
		if c.GetHeader("X-Coding-Async") != "1" || s.cfg.Workspaces == "local" {
			handler(c, id)
			return
		}
		key := c.GetHeader("Idempotency-Key")
		if !workspaceRequestID.MatchString(key) {
			writeJSON(c, 400, map[string]string{"error": "invalid Idempotency-Key"})
			return
		}
		raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodySize+1))
		if err != nil || len(raw) > maxBodySize {
			writeJSON(c, 400, map[string]string{"error": "invalid operation body"})
			return
		}
		body, err := workspaceHTTPBody(kind, raw)
		if err != nil {
			writeJSON(c, 400, map[string]string{"error": err.Error()})
			return
		}
		opID := "who-" + workspaceHash(id.TenantID, id.UserID, key)
		digest := workspaceHash(c.Request.Method, c.Request.URL.RequestURI(), string(body))
		input := &workspaceHTTPRecord{ID: opID, User: id.UserID, Kind: kind, Digest: digest, Method: c.Request.Method, URL: c.Request.URL.RequestURI(), Body: body, Language: requestLang(c.Request), Created: time.Now().UTC(), Facts: map[string]json.RawMessage{}, Paths: map[string]string{}}
		for _, p := range []string{"project", "session", "repo"} {
			input.Paths[p] = c.Param(p)
		}
		hotKey := workspaceHotKey(id.TenantID, opID)
		var rec *workspaceHTTPRecord
		if cached, ok := s.wsHot.getDone(hotKey); ok {
			rec, err = cached, nil
		} else {
			flightKey := hotKey + "\x00" + digest
			val, flightErr, _ := s.wsHot.admitFlights.Do(flightKey, func() (any, error) {
				r, e := s.store.admitWorkspaceHTTP(id.TenantID, input)
				if e == nil && r != nil && r.Status != 0 {
					s.wsHot.putDone(hotKey, r)
				}
				return r, e
			})
			err = flightErr
			if val != nil {
				rec = val.(*workspaceHTTPRecord)
			}
		}
		if err != nil {
			writeErr(c, 500, err)
			return
		}
		if rec.User != id.UserID || rec.Digest != digest {
			writeJSON(c, 409, map[string]string{"error": "Idempotency-Key already used with different parameters"})
			return
		}
		if rec.Status == 0 {
			if err := s.resumeWorkspaceHTTP(rec, id); err != nil {
				writeErr(c, 500, err)
				return
			}
		}
		writeJSON(c, http.StatusAccepted, map[string]string{"operation_id": opID, "status_url": "/api/workspace-operations/" + opID})
	}
}

func (s *Store) admitWorkspaceHTTP(tenant string, input *workspaceHTTPRecord) (*workspaceHTTPRecord, error) {
	path := s.workspaceOperationFile(tenant, input.ID)
	// Fast path: duplicate retries for an already-admitted operation only need
	// a shared read lock and avoid contending on the cross-process metadata lock.
	s.mu.RLock()
	saved, err := readJSONFile[*workspaceHTTPRecord](path)
	s.mu.RUnlock()
	if err != nil || saved != nil {
		return saved, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockMetadata()
	if err != nil {
		return nil, err
	}
	defer unlock()
	saved, err = readJSONFile[*workspaceHTTPRecord](path)
	if err != nil || saved != nil {
		return saved, err
	}
	return input, writeWorkspaceHTTPRecord(path, input)
}

func (s *Server) readWorkspaceHTTPRecord(tenant, opID string) (*workspaceHTTPRecord, error) {
	hotKey := workspaceHotKey(tenant, opID)
	if cached, ok := s.wsHot.getDone(hotKey); ok {
		return cached, nil
	}
	val, err, _ := s.wsHot.readFlights.Do(hotKey, func() (any, error) {
		if cached, ok := s.wsHot.getDone(hotKey); ok {
			return cached, nil
		}
		s.store.mu.RLock()
		rec, err := readJSONFile[*workspaceHTTPRecord](s.store.workspaceOperationFile(tenant, opID))
		s.store.mu.RUnlock()
		if err != nil {
			return nil, err
		}
		if rec != nil && rec.Status != 0 {
			s.wsHot.putDone(hotKey, rec)
		}
		return rec, nil
	})
	if err != nil || val == nil {
		return nil, err
	}
	return val.(*workspaceHTTPRecord), nil
}

func (s *Server) resumeWorkspaceHTTP(rec *workspaceHTTPRecord, id identity) error {
	if rec.Status != 0 {
		return nil
	}
	hotKey := workspaceHotKey(id.TenantID, rec.ID)
	// Fast path: this replica already holds the cross-process OS runner lock,
	// or recently observed another replica holding it (50ms backoff preserves fast crash takeover).
	if _, owned := s.wsHot.activeRunners.Load(hotKey); owned {
		return nil
	}
	if until, ok := s.wsHot.remoteOwned.Load(hotKey); ok && time.Now().UnixNano() < until.(int64) {
		return nil
	}
	path := s.store.workspaceOperationFile(id.TenantID, rec.ID)
	// The journal is replaced by rename. A separate stable inode owns the
	// runner across replicas and releases ownership if the process exits.
	claim, err := os.OpenFile(path+".runner.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	locked, err := tryLockMetadataFile(claim)
	if err != nil || !locked {
		_ = claim.Close()
		if err == nil && !locked {
			s.wsHot.remoteOwned.Store(hotKey, time.Now().Add(workspaceRemoteLockBackoff).UnixNano())
		}
		return err
	}
	s.wsHot.remoteOwned.Delete(hotKey)
	release := func() {
		s.wsHot.activeRunners.Delete(hotKey)
		unlockMetadataFile(claim)
		_ = claim.Close()
	}
	s.wsHot.activeRunners.Store(hotKey, struct{}{})
	// The caller may have read RUNNING just before the preceding runner saved
	// COMPLETED and released its slot. Never overwrite that newer result or its
	// recorded steps with the caller's older snapshot.
	s.store.mu.RLock()
	latest, err := readJSONFile[*workspaceHTTPRecord](path)
	s.store.mu.RUnlock()
	if err != nil || latest == nil || latest.User != id.UserID || latest.Status != 0 {
		if err == nil && latest != nil && latest.Status != 0 {
			s.wsHot.putDone(hotKey, latest)
		}
		release()
		return err
	}
	rec = latest
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		defer release()
		j := &workspaceHTTPJournal{server: s, tenant: id.TenantID, identity: id, record: rec}
		defer func() {
			if recovered := recover(); recovered != nil {
				// A storage/auth interruption leaves the journal pending, so a fresh
				// authenticated poll can resume its existing Runtime operations.
				if _, ok := recovered.(workspaceHTTPAbort); !ok {
					rec.Status = 500
					rec.Response = json.RawMessage(`{"error":"workspace operation interrupted"}`)
					_ = j.save()
				}
			}
		}()
		if err := j.save(); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), workspaceHTTPKey{}, j), 30*time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, rec.Method, rec.URL, bytes.NewReader(rec.Body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Language", rec.Language)
		// 处理函数对着一个合成的请求重放：没有连接，响应写进缓冲。路径参数取记录里存的（磁盘上旧的记录也有）。
		out := &workspaceHTTPResponse{header: make(http.Header)}
		c, _ := gin.CreateTestContext(out)
		c.Request = req
		for p, v := range rec.Paths {
			c.Params = append(c.Params, gin.Param{Key: p, Value: v})
		}
		s.workspaceHTTPHandler(rec.Kind)(c, id)
		c.Writer.WriteHeaderNow() // 只设了状态码、没写响应体时，Gin 还没把它写进 out
		// The web runner's lifetime is not the Runtime operation's deadline.
		// Leave its record resumable when this runner expires mid-poll.
		if ctx.Err() != nil {
			return
		}
		rec.Status = out.status
		if rec.Status == 0 {
			rec.Status = http.StatusOK
		}
		rec.Response = append(json.RawMessage(nil), out.body.Bytes()...)
		_ = j.save()
	}()
	return nil
}

func (s *Server) handleWorkspaceHTTPStatus(c *gin.Context, id identity) {
	opID := c.Param("operation")
	if !workspaceOperationID.MatchString(opID) {
		writeJSON(c, 404, map[string]string{"error": "operation not found"})
		return
	}
	rec, err := s.readWorkspaceHTTPRecord(id.TenantID, opID)
	if err != nil {
		writeErr(c, 500, err)
		return
	}
	if rec == nil || rec.User != id.UserID {
		writeJSON(c, 404, map[string]string{"error": "operation not found"})
		return
	}
	c.Header("Cache-Control", "no-store")
	if rec.Status == 0 {
		if err := s.resumeWorkspaceHTTP(rec, id); err != nil {
			writeErr(c, 500, err)
			return
		}
		writeJSON(c, 200, map[string]any{"operation_id": rec.ID, "status": "RUNNING"})
		return
	}
	status := "COMPLETED"
	if rec.Status >= 400 {
		status = "FAILED"
	}
	writeJSON(c, 200, map[string]any{"operation_id": rec.ID, "status": status, "http_status": rec.Status, "result": rec.Response})
}

func (s *Server) handleWorkspaceHTTPList(c *gin.Context, id identity) {
	files, err := filepath.Glob(filepath.Join(s.store.dir, id.TenantID, "workspace-operations", "who-*.json"))
	if err != nil {
		writeErr(c, 500, err)
		return
	}
	result := []map[string]any{}
	for _, path := range files {
		s.store.mu.Lock()
		rec, err := readJSONFile[*workspaceHTTPRecord](path)
		s.store.mu.Unlock()
		if err != nil {
			writeErr(c, 500, err)
			return
		}
		if rec != nil && rec.User == id.UserID && (rec.Status == 0 || (rec.Status >= 400 && time.Since(rec.Created) < 24*time.Hour)) {
			result = append(result, map[string]any{"operation_id": rec.ID, "status_url": "/api/workspace-operations/" + rec.ID})
		}
	}
	writeJSON(c, 200, result)
}

func (j *workspaceHTTPJournal) runtimeToken() string {
	// Only a currently valid browser session may refresh its five-minute Runtime
	// JWT. No saved JWT or expired session silently reauthorizes an operation.
	if j.identity.session == "" || j.server.sessions == nil {
		return j.identity.token
	}
	session, ok, err := j.server.sessions.lookupWithoutActivity(j.identity.session)
	if err != nil || !ok || session.Tenant != j.tenant || session.User != j.record.User {
		panic(workspaceHTTPAbort{errors.New("authentication required")})
	}
	return j.server.sessions.runtimeToken(j.identity.session, session, j.server.ingress.IssueToken)
}

// The existing handlers write their final HTTP response into this buffer while
// the browser independently polls the operation resource.
type workspaceHTTPResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *workspaceHTTPResponse) Header() http.Header { return r.header }
func (r *workspaceHTTPResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *workspaceHTTPResponse) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}
