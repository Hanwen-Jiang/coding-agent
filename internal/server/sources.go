package server

// Runtime owns source identity, versions and authorization. Local source JSON
// is read only as migration input; migration receipts are disposable projections.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxSourceBytes = 50 << 20
const maxTaskSourceBytes = 200 << 20
const maxTaskSources = 10

var sourceIDPattern = regexp.MustCompile(`^src_[a-f0-9]{32}$`)
var sourceVersionPattern = regexp.MustCompile(`^ver_[a-f0-9]{32}$`)
var sourceDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var sourcePreviewTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true}

// Bounded process-wide spool slots: no request allocates a full source in RAM.
var sourceTransferSlots = make(chan struct{}, 2)
var errSourceTooLarge = errors.New("source exceeds limit")

func acquireSourceTransfer(ctx context.Context) (func(), error) {
	select {
	case sourceTransferSlots <- struct{}{}:
		return func() { <-sourceTransferSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type SourceRecord struct {
	Versioned bool      `json:"-"`
	ID        string    `json:"id"`
	AliasIDs  []string  `json:"alias_ids,omitempty"`
	FileID    string    `json:"file_id,omitempty"`
	VersionID string    `json:"version_id,omitempty"`
	Name      string    `json:"name"`
	MIMEType  string    `json:"mime_type"`
	SizeBytes int64     `json:"size_bytes"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	ObjectURI string    `json:"object_uri"`
}

func (s *Store) sourcesFile(tenant, workspace string) string {
	return filepath.Join(s.dir, tenant, "sources-"+workspace+".json")
}
func (s *Store) sourcesMigrationFile(tenant, workspace, user string) string {
	return filepath.Join(s.dir, tenant, "source-migrations", workspace, workspaceHash(user)+".json")
}
func (s *Store) Sources(tenant, workspace string) ([]SourceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSONFile[[]SourceRecord](s.sourcesFile(tenant, workspace))
}

// PutSources is retained solely for legacy import fixtures. New uploads never call it.
func (s *Store) PutSources(tenant, workspace string, sources []SourceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFileAtomic(s.sourcesFile(tenant, workspace), sources)
}
func sourceName(raw string) string {
	name := path.Base(strings.ReplaceAll(raw, `\`, "/"))
	if name == "." || name == ".." || name == "/" || len(name) > 255 || !utf8.ValidString(name) {
		return ""
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return ""
		}
	}
	return name
}
func sourceView(source SourceRecord, base string, tasks []TaskRecord) map[string]any {
	out := map[string]any{"id": source.ID, "file_id": source.FileID, "alias_ids": source.AliasIDs, "version_id": source.VersionID, "name": source.Name, "mime_type": source.MIMEType, "size_bytes": source.SizeBytes, "sha256": source.SHA256, "created_at": source.CreatedAt, "url": base + "/sources/" + source.ID + "/content"}
	if sourcePreviewTypes[source.MIMEType] {
		out["preview_url"] = base + "/sources/" + source.ID + "/preview"
	}
	taskIDs := []string{}
	for _, task := range tasks {
		for _, id := range task.SourceIDs {
			if id == source.ID || id == source.FileID || slices.Contains(source.AliasIDs, id) {
				taskIDs = append(taskIDs, task.ID)
				break
			}
		}
	}
	out["task_ids"] = taskIDs
	return out
}
func sourceBase(c *gin.Context) string {
	return "/api/projects/" + url.PathEscape(c.Param("project")) + "/sessions/" + url.PathEscape(c.Param("session"))
}

type runtimeFileRecord struct {
	SourceURI string `json:"source_uri"`
	File      struct {
		ID            string    `json:"id"`
		WorkspaceID   string    `json:"workspace_id"`
		Name          string    `json:"name"`
		CreatedBy     string    `json:"created_by"`
		CreatedAt     time.Time `json:"created_at"`
		HeadVersionID string    `json:"head_version_id"`
		Lifecycle     string    `json:"lifecycle"`
	} `json:"file"`
	Version struct {
		ID        string    `json:"id"`
		FileID    string    `json:"file_id"`
		MIMEType  string    `json:"mime_type"`
		SizeBytes int64     `json:"size_bytes"`
		SHA256    string    `json:"sha256"`
		CreatedAt time.Time `json:"created_at"`
		CreatedBy string    `json:"created_by"`
	} `json:"version"`
}
type runtimeFilePage struct {
	Files      []runtimeFileRecord `json:"files"`
	NextCursor string              `json:"next_cursor"`
}

func (record runtimeFileRecord) source(id identity, workspace, alias string, requestedVersions ...string) (SourceRecord, error) {
	expectedVersion := record.File.HeadVersionID
	if len(requestedVersions) > 0 && requestedVersions[0] != "" {
		expectedVersion = requestedVersions[0]
	}
	f, v := record.File, record.Version
	_, _, err := mime.ParseMediaType(v.MIMEType)
	if !sourceIDPattern.MatchString(f.ID) || !sourceVersionPattern.MatchString(v.ID) || v.FileID != f.ID || expectedVersion != v.ID || f.WorkspaceID != workspace || f.Lifecycle != "active" || sourceName(f.Name) != f.Name || f.Name == "" || v.SizeBytes < 0 || v.SizeBytes > maxSourceBytes || !sourceDigestPattern.MatchString(v.SHA256) || err != nil {
		return SourceRecord{}, msg("附件不存在或不属于当前会话")
	}
	sourceID := f.ID
	if alias != "" {
		if !sourceIDPattern.MatchString(alias) {
			return SourceRecord{}, msg("附件不存在或不属于当前会话")
		}
		sourceID = alias
	}
	sourceURI := record.SourceURI
	if sourceURI == "" {
		sourceURI = "obj://" + id.TenantID + "/uploads/" + sourceID + "/" + f.Name
	}
	if !strings.HasPrefix(sourceURI, "obj://"+id.TenantID+"/uploads/") && !strings.HasPrefix(sourceURI, "obj://"+id.TenantID+"/file-derived/") {
		return SourceRecord{}, msg("附件来源无效")
	}
	return SourceRecord{Versioned: record.SourceURI != "", ID: sourceID, FileID: f.ID, VersionID: v.ID, Name: f.Name, MIMEType: v.MIMEType, SizeBytes: v.SizeBytes, SHA256: v.SHA256, CreatedAt: v.CreatedAt, CreatedBy: f.CreatedBy, ObjectURI: sourceURI}, nil
}
func (a *TaskAPI) fileRequest(ctx context.Context, token, method, target string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+target, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &APIError{Status: res.StatusCode, Code: "file_api_unavailable", Message: "File API request failed"}
	}
	return json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out)
}
func filesQuery(workspace string) url.Values { return url.Values{"workspace_id": {workspace}} }

// Migration is per owner, and the receipt fingerprints only that owner's old
// rows. Missing/failed Runtime never silently restores local-authority writes.
func (s *Server) migrateSources(ctx context.Context, id identity, workspace string) error {
	lock := s.workspaceLock("source-migration:" + id.TenantID + ":" + workspace + ":" + id.UserID)
	lock.Lock()
	defer lock.Unlock()
	old, err := s.store.Sources(id.TenantID, workspace)
	if err != nil {
		return err
	}
	selected := []SourceRecord{}
	for _, source := range old {
		if source.CreatedBy == id.UserID {
			selected = append(selected, source)
		}
	}
	if len(selected) == 0 {
		return nil
	}
	raw, _ := json.Marshal(selected)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	markerPath := s.store.sourcesMigrationFile(id.TenantID, workspace, id.UserID)
	var receipt struct {
		Digest string `json:"digest"`
	}
	s.store.mu.Lock()
	receipt, err = readJSONFile[struct {
		Digest string `json:"digest"`
	}](markerPath)
	s.store.mu.Unlock()
	if err != nil {
		return err
	}
	if receipt.Digest == digest {
		return nil
	}
	refs := make([]string, 0, len(selected))
	for _, source := range selected {
		if !sourceIDPattern.MatchString(source.ID) || sourceName(source.Name) != source.Name || source.ObjectURI != "obj://"+id.TenantID+"/uploads/"+source.ID+"/"+source.Name {
			return msg("附件不存在或不属于当前会话")
		}
		refs = append(refs, source.ObjectURI)
	}
	// A page is atomic at Runtime. Receipts are written only after all pages succeed;
	// crash recovery safely re-adopts already registered aliases idempotently.
	for start := 0; start < len(refs); start += 500 {
		end := min(start+500, len(refs))
		var page runtimeFilePage
		if err = s.api.fileRequest(ctx, id.token, http.MethodPost, "/v1/files/adopt?"+filesQuery(workspace).Encode(), map[string]any{"object_uris": refs[start:end]}, &page); err != nil {
			return err
		}
		if len(page.Files) != end-start {
			return errors.New("incomplete source migration")
		}
		for i, record := range page.Files {
			source, err := record.source(id, workspace, selected[start+i].ID)
			if err != nil {
				return err
			}
			if source.ObjectURI != refs[start+i] {
				return errors.New("source migration identity mismatch")
			}
		}
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	return writeFileAtomic(markerPath, map[string]string{"digest": digest})
}
func (s *Server) runtimeSources(ctx context.Context, id identity, workspace string) ([]SourceRecord, error) {
	if err := s.migrateSources(ctx, id, workspace); err != nil {
		return nil, err
	}
	result := []SourceRecord{}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 100; pages++ {
		query := filesQuery(workspace)
		query.Set("limit", "100")
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var page runtimeFilePage
		if err := s.api.fileRequest(ctx, id.token, http.MethodGet, "/v1/files?"+query.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for _, record := range page.Files {
			source, err := record.source(id, workspace, "")
			if err != nil {
				return nil, err
			}
			if seen[source.ID] {
				return nil, errors.New("duplicate source in catalog")
			}
			seen[source.ID] = true
			result = append(result, source)
		}
		if page.NextCursor == "" {
			return result, nil
		}
		if page.NextCursor == cursor {
			return nil, errors.New("file pagination did not advance")
		}
		cursor = page.NextCursor
	}
	return nil, errors.New("file pagination limit reached")
}
func (s *Server) handleListSources(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 30*time.Second)
	defer cancel()
	sources, err := s.runtimeSources(ctx, id, sess.WorkspaceID)
	if err != nil {
		writeMsg(c, 502, "暂时无法读取文件来源")
		return
	}
	tasks, err := s.store.Tasks(id.TenantID, sess.WorkspaceID)
	if err != nil {
		writeErr(c, 500, err)
		return
	}
	byID := map[string]int{}
	seen := map[string]bool{}
	for i, source := range sources {
		byID[source.ID] = i
		seen[source.ID] = true
	}
	// Retain pre-dedup task references as aliases of the canonical catalog file.
	for _, task := range tasks {
		for _, alias := range task.SourceIDs {
			if seen[alias] {
				continue
			}
			seen[alias] = true
			resolved, err := s.resolveSources(ctx, id, sess.WorkspaceID, []string{alias})
			if err != nil {
				continue
			}
			source := resolved[0]
			if index, ok := byID[source.FileID]; ok {
				sources[index].AliasIDs = append(sources[index].AliasIDs, alias)
			}
		}
	}
	out := []map[string]any{}
	for _, source := range sources {
		out = append(out, sourceView(source, sourceBase(c), tasks))
	}
	c.Header("Cache-Control", "no-store")
	writeJSON(c, 200, map[string]any{"sources": out})
}

// spoolSource bounds bytes on disk and hashes incrementally. It never uses the
// user's filename as a local path; callers always close and remove the temp file.
func spoolSource(reader io.Reader, limit int64) (*os.File, int64, string, string, error) {
	file, err := os.CreateTemp("", "coding-source-*")
	if err != nil {
		return nil, 0, "", "", err
	}
	cleanup := func() { file.Close(); os.Remove(file.Name()) }
	hasher := sha256.New()
	count, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(reader, limit+1))
	if err != nil || count > limit {
		cleanup()
		if err == nil {
			err = errSourceTooLarge
		}
		return nil, count, "", "", err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, count, "", "", err
	}
	header := make([]byte, 512)
	n, err := file.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		cleanup()
		return nil, count, "", "", err
	}
	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		cleanup()
		return nil, count, "", "", err
	}
	return file, count, hex.EncodeToString(hasher.Sum(nil)), http.DetectContentType(header[:n]), nil
}
func (s *Server) handleUploadSource(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	release, err := acquireSourceTransfer(c.Request.Context())
	if err != nil {
		return
	}
	defer release()
	ctx, cancel := ctxTimeout(c.Request, 2*time.Minute)
	defer cancel()
	if err = s.migrateSources(ctx, id, sess.WorkspaceID); err != nil {
		writeMsg(c, 502, "暂时无法读取文件来源")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSourceBytes+(64<<10))
	reader, err := c.Request.MultipartReader()
	if err != nil {
		writeMsg(c, 400, "请选择一个有效文件")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" {
		writeMsg(c, 400, "请选择一个有效文件")
		return
	}
	name := sourceName(part.FileName())
	if name == "" {
		writeMsg(c, 400, "文件名不合法")
		return
	}
	file, size, digest, media, err := spoolSource(part, maxSourceBytes)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.Is(err, errSourceTooLarge) || errors.As(err, &tooLarge) {
			writeMsg(c, 413, "单个文件不能超过 50 MiB")
		} else {
			writeMsg(c, 502, "暂时上传不了这个文件")
		}
		return
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	_ = part.Close()
	if _, err = reader.NextPart(); !errors.Is(err, io.EOF) {
		writeMsg(c, 400, "每次只能上传一个文件")
		return
	}
	source, err := s.api.uploadSource(ctx, id.token, sess.WorkspaceID, name, media, size, file)
	if err != nil {
		if statusOf(err, 502) == 413 {
			writeMsg(c, 413, "文件来源存储配额已满")
		} else {
			writeMsg(c, 502, "暂时上传不了这个文件")
		}
		return
	}
	expectedURI := "obj://" + id.TenantID + "/uploads/" + source.ID + "/" + name
	if !sourceIDPattern.MatchString(source.ID) || source.FileID != source.ID || !sourceVersionPattern.MatchString(source.VersionID) || (source.ObjectURI != expectedURI && !strings.HasPrefix(source.ObjectURI, "obj://"+id.TenantID+"/file-derived/fjob_")) || source.Name != name || source.SizeBytes != size || source.SHA256 != digest {
		writeMsg(c, 502, "暂时上传不了这个文件")
		return
	}
	source.MIMEType = media
	source.CreatedBy = id.UserID
	writeJSON(c, 201, map[string]any{"source": sourceView(source, sourceBase(c), nil)})
}
func (a *TaskAPI) uploadSource(ctx context.Context, token, workspace, name, media string, size int64, data io.Reader) (SourceRecord, error) {
	query := filesQuery(workspace)
	query.Set("filename", name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/v1/uploads?"+query.Encode(), data)
	if err != nil {
		return SourceRecord{}, err
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", media)
	res, err := a.slow.Do(req)
	if err != nil {
		return SourceRecord{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != 201 {
		return SourceRecord{}, &APIError{Status: res.StatusCode, Code: "source_upload_failed", Message: "Source upload failed"}
	}
	var wire struct {
		SourceRecord
		URI string `json:"uri"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&wire)
	wire.ObjectURI = wire.URI
	return wire.SourceRecord, err
}
func (a *TaskAPI) sourceContent(ctx context.Context, token, workspace string, source SourceRecord) (*http.Response, error) {
	target := a.base + "/v1/uploads/" + source.ID + "/" + url.PathEscape(source.Name) + "?" + filesQuery(workspace).Encode()
	if source.Versioned && source.FileID != "" && source.VersionID != "" {
		target = a.base + "/v1/files/" + source.FileID + "/versions/" + source.VersionID + "/content?" + filesQuery(workspace).Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return a.slow.Do(req)
}
func (s *Server) resolveSources(ctx context.Context, id identity, workspace string, ids []string, choices ...map[string]string) ([]SourceRecord, error) {
	selectedVersions := map[string]string{}
	if len(choices) > 0 && choices[0] != nil {
		selectedVersions = choices[0]
	}
	for id, version := range selectedVersions {
		if !slices.Contains(ids, id) || !sourceVersionPattern.MatchString(version) {
			return nil, msg("附件版本无效")
		}
	}
	if len(ids) > maxTaskSources {
		return nil, msg("每个任务最多使用 10 个附件")
	}
	if len(ids) == 0 {
		return []SourceRecord{}, nil
	}
	if err := s.migrateSources(ctx, id, workspace); err != nil {
		return nil, err
	}
	result := []SourceRecord{}
	seen := map[string]bool{}
	canonicalSeen := map[string]bool{}
	total := int64(0)
	for _, ref := range ids {
		if !sourceIDPattern.MatchString(ref) || seen[ref] {
			return nil, msg("附件不存在或不属于当前会话")
		}
		seen[ref] = true
		var record runtimeFileRecord
		target := "/v1/files/" + ref
		requested := selectedVersions[ref]
		if requested != "" {
			target += "/versions/" + requested
		}
		if err := s.api.fileRequest(ctx, id.token, http.MethodGet, target+"?"+filesQuery(workspace).Encode(), nil, &record); err != nil {
			return nil, err
		}
		source, err := record.source(id, workspace, ref, requested)
		if err != nil {
			return nil, err
		}
		if canonicalSeen[source.FileID] {
			return nil, msg("附件不存在或不属于当前会话")
		}
		canonicalSeen[source.FileID] = true
		total += source.SizeBytes
		if total > maxTaskSourceBytes {
			return nil, msg("任务附件总计不能超过 200 MiB")
		}
		result = append(result, source)
	}
	return result, nil
}
func (s *Server) handleSourceContent(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 2*time.Minute)
	defer cancel()
	sources, err := s.resolveSources(ctx, id, sess.WorkspaceID, []string{c.Param("source")})
	if err != nil {
		writeMsg(c, 404, "文件不存在")
		return
	}
	source := sources[0]
	preview := strings.HasSuffix(c.Request.URL.Path, "/preview")
	if preview && !sourcePreviewTypes[source.MIMEType] {
		writeMsg(c, 404, "文件不存在")
		return
	}
	release, err := acquireSourceTransfer(ctx)
	if err != nil {
		return
	}
	defer release()
	res, err := s.api.sourceContent(ctx, id.token, sess.WorkspaceID, source)
	if err != nil {
		writeMsg(c, 502, "暂时下载不了这个文件")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		status := 502
		if res.StatusCode == 404 || res.StatusCode == 410 {
			status = res.StatusCode
		}
		writeMsg(c, status, "暂时下载不了这个文件")
		return
	}
	file, size, digest, media, err := spoolSource(res.Body, maxSourceBytes)
	if err != nil {
		writeMsg(c, 502, "暂时下载不了这个文件")
		return
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	if size != source.SizeBytes || digest != source.SHA256 || media != source.MIMEType {
		writeMsg(c, 502, "暂时下载不了这个文件")
		return
	}
	disposition := "attachment"
	if preview {
		disposition = "inline"
	}
	h := c.Writer.Header()
	h.Set("Content-Type", media)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": source.Name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "no-store")
	_, _ = io.Copy(c.Writer, file)
}

func (s *Server) handleSourceCapabilities(c *gin.Context, id identity) {
	var workspace string
	if c.Param("session") == "" {
		project, ok := s.project(c, id)
		if !ok {
			return
		}
		workspace = project.ID
	} else {
		_, session, ok := s.session(c, id)
		if !ok {
			return
		}
		workspace = session.WorkspaceID
	}
	ctx, cancel := ctxTimeout(c.Request, 15*time.Second)
	defer cancel()
	var capability map[string]any
	if err := s.api.fileRequest(ctx, id.token, http.MethodGet, "/v1/files/capabilities?"+filesQuery(workspace).Encode(), nil, &capability); err != nil {
		writeMsg(c, 502, "暂时无法读取文件来源")
		return
	}
	c.Header("Cache-Control", "no-store")
	writeJSON(c, 200, capability)
}
