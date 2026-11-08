package server

// Public discovery is deliberately separate from connected GitHub accounts and the
// runtime clients. It reads public metadata only; it never forwards tokens, executes
// repositories, installs packages, or connects to discovered MCP servers.
// Upstream contracts:
// https://docs.github.com/en/rest/git/trees
// https://docs.github.com/en/rest/repos/contents
// https://github.com/modelcontextprotocol/registry/blob/main/docs/reference/api/official-registry-api.md
import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/singleflight"
)

const (
	discoveryTTL           = 10 * time.Minute
	discoveryArchiveLimit  = 12 << 20
	discoveryUnpackedLimit = 64 << 20
	discoveryDocumentLimit = 64 << 10
	discoveryCacheLimit    = 32 << 20
	discoveryPageSize      = 20
)

var discoveryHosts = map[string]bool{"api.github.com": true, "raw.githubusercontent.com": true, "codeload.github.com": true, "registry.modelcontextprotocol.io": true}
var discoveryRepoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var discoveryRefRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
var discoverySHA = regexp.MustCompile(`^[a-f0-9]{40}$`)
var discoverySources = []map[string]any{
	{"name": "Anthropic", "repository": "anthropics/skills"},
	{"name": "OpenAI Plugins", "repository": "openai/plugins"},
	{"name": "OpenAI Skills (legacy)", "repository": "openai/skills", "deprecated": true},
}

type discoveryError struct {
	status        int
	code, message string
	retry         int
}

func (e *discoveryError) Error() string { return e.code }
func discoveryErr(status int, code, message string) error {
	return &discoveryError{status: status, code: code, message: message}
}
func discoveryReplyError(w http.ResponseWriter, err error) {
	var e *discoveryError
	if !errors.As(err, &e) {
		e = &discoveryError{status: 502, code: "upstream_error", message: "公开来源暂时无法访问，请稍后重试"}
	}
	body := map[string]any{"error": e.message, "code": e.code}
	if e.retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.retry))
		body["retry_after"] = e.retry
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, e.status, body)
}

type discoveryCacheItem struct {
	body []byte
	at   time.Time
}
type discoveryService struct {
	client     *http.Client
	mu         sync.Mutex
	cache      map[string]discoveryCacheItem
	cacheBytes int
	calls      map[string][]time.Time
	cooldown   map[string]time.Time
	flights    singleflight.Group
	slots      chan struct{}
}

func newDiscoveryService() *discoveryService {
	return &discoveryService{client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, cache: map[string]discoveryCacheItem{}, calls: map[string][]time.Time{}, cooldown: map[string]time.Time{}, slots: make(chan struct{}, 4)}
}
func (d *discoveryService) get(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !discoveryHosts[u.Host] || u.Fragment != "" {
		return nil, discoveryErr(400, "invalid_source", "不支持这个公开来源地址")
	}
	key := endpoint + "#limit=" + strconv.FormatInt(limit, 10)
	d.mu.Lock()
	cached, ok := d.cache[key]
	d.mu.Unlock()
	if ok && time.Since(cached.at) < discoveryTTL {
		return cached.body, nil
	}
	result := d.flights.DoChan(key, func() (any, error) {
		d.mu.Lock()
		cached, ok := d.cache[key]
		d.mu.Unlock()
		if ok && time.Since(cached.at) < discoveryTTL {
			return cached.body, nil
		}
		select {
		case d.slots <- struct{}{}:
			defer func() { <-d.slots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		now := time.Now()
		d.mu.Lock()
		until := d.cooldown[u.Host]
		d.mu.Unlock()
		if now.Before(until) {
			return nil, &discoveryError{429, "rate_limited", "公开来源暂时限流，请稍后重试", int(time.Until(until).Seconds()) + 1}
		}
		window := time.Minute
		maximum := 60
		if u.Host == "api.github.com" {
			window = time.Hour
			maximum = 50
		}
		if u.Host == "codeload.github.com" {
			maximum = 6
		}
		d.mu.Lock()
		recent := d.calls[u.Host][:0]
		for _, at := range d.calls[u.Host] {
			if now.Sub(at) < window {
				recent = append(recent, at)
			}
		}
		if len(recent) >= maximum {
			d.calls[u.Host] = recent
			retry := int(window.Seconds())
			d.mu.Unlock()
			return nil, &discoveryError{429, "rate_limited", "公开来源请求较多，请稍后重试", retry}
		}
		d.calls[u.Host] = append(recent, now)
		d.mu.Unlock()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if u.Host != "api.github.com" {
			req.Header.Set("Accept", "application/json, text/plain, application/zip")
		}
		req.Header.Set("User-Agent", "Coding-Agent-Public-Discovery/1.0")
		if u.Host == "api.github.com" {
			req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		}
		res, err := d.client.Do(req)
		if err != nil {
			return nil, discoveryErr(502, "upstream_error", "公开来源暂时无法访问，请稍后重试")
		}
		defer res.Body.Close()
		switch {
		case res.StatusCode == 404:
			return nil, discoveryErr(404, "not_found", "没有找到公开仓库、版本或文档")
		case res.StatusCode == 429 || res.StatusCode == 403:
			retry, _ := strconv.Atoi(res.Header.Get("Retry-After"))
			if retry < 1 || retry > 3600 {
				retry = 60
			}
			d.mu.Lock()
			d.cooldown[u.Host] = time.Now().Add(time.Duration(retry) * time.Second)
			d.mu.Unlock()
			return nil, &discoveryError{429, "rate_limited", "公开来源暂时限流，请稍后重试", retry}
		case res.StatusCode != 200:
			return nil, discoveryErr(502, "upstream_error", "公开来源返回异常，请稍后重试")
		}
		if res.ContentLength > limit {
			return nil, discoveryErr(422, "archive_too_large", "公开来源内容超过读取上限，请打开原仓库查看")
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(body)) > limit {
			return nil, discoveryErr(422, "archive_too_large", "公开来源内容超过读取上限，请打开原仓库查看")
		}
		d.mu.Lock()
		for k, c := range d.cache {
			if time.Since(c.at) >= discoveryTTL {
				delete(d.cache, k)
				d.cacheBytes -= len(c.body)
			}
		}
		for d.cacheBytes+len(body) > discoveryCacheLimit || len(d.cache) >= 128 {
			var oldest string
			var at time.Time
			for k, c := range d.cache {
				if oldest == "" || c.at.Before(at) {
					oldest = k
					at = c.at
				}
			}
			if oldest == "" {
				break
			}
			d.cacheBytes -= len(d.cache[oldest].body)
			delete(d.cache, oldest)
		}
		if previous, ok := d.cache[key]; ok {
			d.cacheBytes -= len(previous.body)
		}
		d.cache[key] = discoveryCacheItem{body, now}
		d.cacheBytes += len(body)
		d.mu.Unlock()
		return body, nil
	})
	select {
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.([]byte), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func discoveryRepo(input string) (string, error) {
	repo := strings.TrimSpace(input)
	if repo == "" {
		repo = "anthropics/skills"
	}
	if strings.HasPrefix(repo, "https://") {
		u, e := url.Parse(repo)
		if e != nil || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", discoveryErr(400, "invalid_request", "请输入公开 GitHub 仓库的 owner/repo 或仓库链接")
		}
		repo = strings.Trim(u.Path, "/")
	}
	repo = strings.TrimSuffix(repo, ".git")
	if !discoveryRepoRE.MatchString(repo) || strings.Contains(repo, "..") {
		return "", discoveryErr(400, "invalid_request", "请输入公开 GitHub 仓库的 owner/repo 或仓库链接")
	}
	return repo, nil
}
func discoveryRef(input string) (string, error) {
	if input == "" {
		input = "HEAD"
	}
	if !discoveryRefRE.MatchString(input) || strings.Contains(input, "..") || strings.Contains(input, "//") || strings.HasSuffix(input, "/") {
		return "", discoveryErr(400, "invalid_request", "仓库版本格式不正确")
	}
	return input, nil
}
func discoveryPath(p string) bool {
	if p == "" || len(p) > 1024 || strings.ContainsAny(p, "\\\x00\r\n") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." || part == "" {
			return false
		}
	}
	return true
}

type discoveryEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}
type discoverySnapshot struct {
	entries               []discoveryEntry
	files                 map[string]*zip.File
	ref, revision, source string
}

func (d *discoveryService) snapshot(ctx context.Context, repo, ref string) (*discoverySnapshot, error) {
	raw, err := d.get(ctx, "https://api.github.com/repos/"+repo+"/commits/"+url.PathEscape(ref), 1<<20)
	if err == nil {
		var commit struct {
			SHA string `json:"sha"`
		}
		if json.Unmarshal(raw, &commit) != nil || !discoverySHA.MatchString(commit.SHA) {
			return nil, discoveryErr(502, "invalid_metadata", "公开仓库返回了无效版本信息")
		}
		raw, err = d.get(ctx, "https://api.github.com/repos/"+repo+"/git/trees/"+commit.SHA+"?recursive=1", 8<<20)
		if err == nil {
			var tree struct {
				Tree      []discoveryEntry `json:"tree"`
				Truncated bool             `json:"truncated"`
			}
			if json.Unmarshal(raw, &tree) != nil || tree.Tree == nil {
				return nil, discoveryErr(502, "invalid_metadata", "公开仓库目录信息无法读取")
			}
			if !tree.Truncated && len(tree.Tree) <= 10000 {
				return &discoverySnapshot{entries: tree.Tree, ref: commit.SHA, revision: "commit:" + commit.SHA, source: "github"}, nil
			}
			err = discoveryErr(502, "tree_truncated", "公开仓库目录超过完整读取上限")
		}
	}
	var upstream *discoveryError
	if !errors.As(err, &upstream) || upstream.status == 404 || upstream.status == 400 {
		return nil, err
	}
	// GitHub's unauthenticated API is commonly rate-limited. Archive bytes are
	// bounded and inspected in memory; no entry is extracted or executed.
	raw, err = d.get(ctx, "https://codeload.github.com/"+repo+"/zip/"+url.PathEscape(ref), discoveryArchiveLimit)
	if err != nil {
		return nil, err
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, discoveryErr(502, "invalid_archive", "公开仓库归档无法读取")
	}
	if len(z.File) > 10000 {
		return nil, discoveryErr(422, "archive_too_large", "仓库文件数量超过读取上限，请打开原仓库查看")
	}
	snap := &discoverySnapshot{ref: ref, source: "git-archive", files: map[string]*zip.File{}}
	hash := sha256.Sum256(raw)
	snap.revision = "archive-sha256:" + hex.EncodeToString(hash[:])
	var total uint64
	var root string
	seen := map[string]bool{}
	for _, f := range z.File {
		total += f.UncompressedSize64
		if total > discoveryUnpackedLimit {
			return nil, discoveryErr(422, "archive_too_large", "仓库展开大小超过读取上限，请打开原仓库查看")
		}
		name := strings.TrimSuffix(f.Name, "/")
		if !discoveryPath(name) {
			return nil, discoveryErr(502, "invalid_archive", "仓库包含不支持的文件路径")
		}
		parts := strings.SplitN(name, "/", 2)
		if root == "" {
			root = parts[0]
		}
		if parts[0] != root {
			return nil, discoveryErr(502, "invalid_archive", "仓库归档根目录不一致")
		}
		if len(parts) < 2 || f.FileInfo().IsDir() {
			continue
		}
		p := parts[1]
		if seen[strings.ToLower(p)] {
			return nil, discoveryErr(502, "invalid_archive", "仓库包含重复文件路径")
		}
		seen[strings.ToLower(p)] = true
		mode := "100644"
		if !f.Mode().IsRegular() {
			mode = "120000"
		}
		snap.entries = append(snap.entries, discoveryEntry{Path: p, Type: "blob", Mode: mode, Size: int64(f.UncompressedSize64)})
		if mode == "100644" && path.Base(p) == "SKILL.md" {
			snap.files[p] = f
		}
	}
	return snap, nil
}
func (d *discoveryService) skillContent(ctx context.Context, repo string, snap *discoverySnapshot, p string) (string, error) {
	for _, entry := range snap.entries {
		if entry.Path == p {
			if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") || entry.Size > discoveryDocumentLimit {
				return "", discoveryErr(422, "unsupported_document", "Skill 文档过大或不是普通文件")
			}
			if snap.files != nil {
				f := snap.files[p]
				if f == nil {
					return "", discoveryErr(422, "unsupported_document", "Skill 文档不是普通文件")
				}
				r, e := f.Open()
				if e != nil {
					return "", e
				}
				defer r.Close()
				b, e := io.ReadAll(io.LimitReader(r, discoveryDocumentLimit+1))
				if e != nil {
					return "", e
				}
				if len(b) > discoveryDocumentLimit {
					return "", discoveryErr(422, "unsupported_document", "Skill 文档超过读取上限")
				}
				if !utf8.Valid(b) {
					return "", discoveryErr(422, "unsupported_document", "Skill 文档不是 UTF-8 文本")
				}
				return string(b), nil
			}
			raw, e := d.get(ctx, "https://raw.githubusercontent.com/"+repo+"/"+snap.ref+"/"+escapeDiscoveryPath(p), discoveryDocumentLimit)
			if e != nil {
				return "", e
			}
			if !utf8.Valid(raw) {
				return "", discoveryErr(422, "unsupported_document", "Skill 文档不是 UTF-8 文本")
			}
			return string(raw), nil
		}
	}
	return "", discoveryErr(404, "not_found", "没有找到这个 SKILL.md 文档")
}
func escapeDiscoveryPath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// Frontmatter values are plain data. This bounded scalar parser accepts quoted
// strings and YAML block descriptions, but never interprets tags or aliases.
func skillFrontmatter(raw string) (string, string, error) {
	raw = strings.TrimPrefix(strings.ReplaceAll(raw, "\r\n", "\n"), "\ufeff")
	lines := strings.Split(raw, "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return "", "", discoveryErr(422, "invalid_metadata", "Skill 缺少 name / description 文档头")
	}
	values := map[string]string{}
	end := false
	for i := 1; i < len(lines) && i < 200; i++ {
		line := lines[i]
		if line == "---" {
			end = true
			break
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, v, ok := strings.Cut(line, ":")
		if !ok || (key != "name" && key != "description") {
			continue
		}
		if _, ok := values[key]; ok {
			return "", "", discoveryErr(422, "invalid_metadata", "Skill 文档头包含重复字段")
		}
		v = strings.TrimSpace(v)
		if v == "|" || v == ">" || v == "|-" || v == ">-" {
			var parts []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t") || lines[i+1] == "") {
				i++
				parts = append(parts, strings.TrimSpace(lines[i]))
			}
			sep := "\n"
			if strings.HasPrefix(v, ">") {
				sep = " "
			}
			v = strings.Join(parts, sep)
		} else if strings.HasPrefix(v, "\"") {
			parsed, e := strconv.Unquote(v)
			if e != nil {
				return "", "", discoveryErr(422, "invalid_metadata", "Skill 文档头格式暂不支持")
			}
			v = parsed
		} else if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
			v = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		} else if strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{") || strings.HasPrefix(v, "&") || strings.HasPrefix(v, "*") || strings.HasPrefix(v, "!") {
			return "", "", discoveryErr(422, "invalid_metadata", "Skill 文档头需要文本名称与说明")
		}
		values[key] = strings.TrimSpace(v)
	}
	if !end || values["name"] == "" || values["description"] == "" {
		return "", "", discoveryErr(422, "invalid_metadata", "Skill 缺少 name / description 文档头")
	}
	return values["name"], values["description"], nil
}

type discoverySkill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Repository  string `json:"repository"`
	URL         string `json:"url"`
	Path        string `json:"path"`
	Ref         string `json:"ref"`
	Revision    string `json:"revision"`
	Source      string `json:"source"`
}

func (d *discoveryService) skill(ctx context.Context, repo string, snap *discoverySnapshot, p string) (discoverySkill, string, error) {
	raw, err := d.skillContent(ctx, repo, snap, p)
	if err != nil {
		return discoverySkill{}, "", err
	}
	name, description, err := skillFrontmatter(raw)
	if err != nil {
		return discoverySkill{}, "", err
	}
	return discoverySkill{repo + ":" + p, name, description, repo, "https://github.com/" + repo + "/blob/" + url.PathEscape(snap.ref) + "/" + escapeDiscoveryPath(p), p, snap.ref, snap.revision, snap.source}, raw, nil
}
func discoveryArgs(r *http.Request) (string, string, error) {
	repo, e := discoveryRepo(r.URL.Query().Get("repo"))
	if e != nil {
		return "", "", e
	}
	ref, e := discoveryRef(r.URL.Query().Get("ref"))
	return repo, ref, e
}
func (s *Server) handleExploreSkills(w http.ResponseWriter, r *http.Request, _ identity) {
	repo, ref, err := discoveryArgs(r)
	if err != nil {
		discoveryReplyError(w, err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		query = strings.TrimSpace(r.URL.Query().Get("q"))
	}
	if len(query) > 200 {
		discoveryReplyError(w, discoveryErr(400, "invalid_request", "搜索文字过长"))
		return
	}
	offset := 0
	if v := r.URL.Query().Get("cursor"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 || offset > 10000 {
			discoveryReplyError(w, discoveryErr(400, "invalid_request", "分页位置无效"))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	snap, err := s.discovery.snapshot(ctx, repo, ref)
	if err != nil {
		discoveryReplyError(w, err)
		return
	}
	paths := []string{}
	for _, e := range snap.entries {
		if e.Type == "blob" && path.Base(e.Path) == "SKILL.md" && discoveryPath(e.Path) && strings.Contains(strings.ToLower(e.Path), strings.ToLower(query)) {
			paths = append(paths, e.Path)
		}
	}
	sort.Strings(paths)
	if offset > len(paths) {
		offset = len(paths)
	}
	end := min(offset+discoveryPageSize, len(paths))
	items := []discoverySkill{}
	warnings := []string{}
	for _, p := range paths[offset:end] {
		item, _, e := s.discovery.skill(ctx, repo, snap, p)
		if e != nil {
			var typed *discoveryError
			if errors.As(e, &typed) && typed.status == 422 {
				warnings = append(warnings, p+": "+typed.message)
				continue
			}
			discoveryReplyError(w, e)
			return
		}
		items = append(items, item)
	}
	cursor := ""
	if end < len(paths) {
		cursor = strconv.Itoa(end)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": cursor, "sources": discoverySources, "repository": repo, "ref": snap.ref, "revision": snap.revision, "source": snap.source, "total": len(paths), "warnings": warnings, "search_scope": "path"})
}
func (s *Server) handleExploreSkillDetail(w http.ResponseWriter, r *http.Request, _ identity) {
	repo, ref, err := discoveryArgs(r)
	if err != nil {
		discoveryReplyError(w, err)
		return
	}
	p := r.URL.Query().Get("path")
	if !discoveryPath(p) || path.Base(p) != "SKILL.md" {
		discoveryReplyError(w, discoveryErr(400, "invalid_request", "请选择仓库里的 SKILL.md 文档"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	snap, err := s.discovery.snapshot(ctx, repo, ref)
	if err != nil {
		discoveryReplyError(w, err)
		return
	}
	item, raw, err := s.discovery.skill(ctx, repo, snap, p)
	if err != nil {
		discoveryReplyError(w, err)
		return
	}
	refs := []string{}
	dir := path.Dir(p)
	prefix := dir + "/"
	if dir == "." {
		prefix = ""
	}
	for _, e := range snap.entries {
		if e.Path != p && e.Type != "tree" && strings.HasPrefix(e.Path, prefix) {
			refs = append(refs, strings.TrimPrefix(e.Path, prefix))
		}
	}
	for _, match := range regexp.MustCompile(`\]\(([^)[:space:]]+)`).FindAllStringSubmatch(raw, -1) {
		target := strings.Trim(match[1], "<>")
		if strings.HasPrefix(target, "#") || strings.Contains(target, "://") {
			continue
		}
		if strings.HasPrefix(target, "mailto:") {
			continue
		}
		target = strings.Split(target, "#")[0]
		if target != "" && target != "SKILL.md" {
			found := false
			for _, v := range refs {
				if v == target {
					found = true
				}
			}
			if !found {
				refs = append(refs, target)
			}
		}
	}
	sort.Strings(refs)
	importable := len(refs) == 0 && skillName.MatchString(item.Name) && utf8.RuneCountInString(item.Description) <= 1024
	reason := ""
	if len(refs) > 0 {
		reason = "这个 Skill 包含附属文件，当前仅支持完整的单文件 Skill；请在原仓库查看依赖"
	} else if !importable {
		reason = "名称或说明不符合当前 Skill 格式，请先手动调整"
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, struct {
		discoverySkill
		Instructions   string   `json:"instructions"`
		ReferenceFiles []string `json:"reference_files"`
		Supporting     bool     `json:"has_supporting_files"`
		Importable     bool     `json:"importable"`
		Reason         string   `json:"import_reason,omitempty"`
	}{item, raw, refs, len(refs) > 0, importable, reason})
}

type registryHeader struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"isRequired"`
	Secret      bool   `json:"isSecret"`
}
type registryRemote struct {
	URL     string           `json:"url"`
	Type    string           `json:"type"`
	Headers []registryHeader `json:"headers"`
}
type registryArgument struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
	Required    bool   `json:"isRequired"`
	Secret      bool   `json:"isSecret"`
}
type registryPackage struct {
	RuntimeHint          string             `json:"runtimeHint"`
	RuntimeArguments     []registryArgument `json:"runtimeArguments"`
	PackageArguments     []registryArgument `json:"packageArguments"`
	EnvironmentVariables []registryHeader   `json:"environmentVariables"`
	Registry             string             `json:"registryType"`
	Identifier           string             `json:"identifier"`
	Version              string             `json:"version"`
	Transport            struct {
		Type string `json:"type"`
	} `json:"transport"`
}
type registryServer struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Website     string `json:"websiteUrl"`
	Repository  struct {
		URL string `json:"url"`
	} `json:"repository"`
	Remotes  []registryRemote  `json:"remotes"`
	Packages []registryPackage `json:"packages"`
}

func safeRegistryArguments(args []registryArgument) []map[string]any {
	out := []map[string]any{}
	for _, a := range args {
		value := a.Value
		if a.Secret {
			value = ""
		}
		out = append(out, map[string]any{"type": a.Type, "name": a.Name, "value": value, "description": a.Description, "required": a.Required, "secret": a.Secret})
	}
	return out
}
func safeRegistryEnvironment(vars []registryHeader) []map[string]any {
	out := []map[string]any{}
	for _, v := range vars {
		out = append(out, map[string]any{"name": v.Name, "description": v.Description, "required": v.Required, "secret": v.Secret})
	}
	return out
}
func publicMCPURL(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(raw, "{}\r\n") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return false
	}
	return true
}
func (s *Server) handleExploreMCP(w http.ResponseWriter, r *http.Request, _ identity) {
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		query = strings.TrimSpace(r.URL.Query().Get("q"))
	}
	if (query != "" && utf8.RuneCountInString(query) < 3) || len(query) > 200 {
		discoveryReplyError(w, discoveryErr(400, "search_too_short", "请输入至少 3 个字符搜索 MCP 名称"))
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 1024 || strings.ContainsAny(cursor, "\r\n\x00") {
		discoveryReplyError(w, discoveryErr(400, "invalid_request", "分页位置无效"))
		return
	}
	params := url.Values{"version": {"latest"}, "limit": {"20"}}
	if query != "" {
		params.Set("search", query)
	}
	if cursor != "" {
		params.Set("cursor", cursor)
	}
	raw, err := s.discovery.get(r.Context(), "https://registry.modelcontextprotocol.io/v0.1/servers?"+params.Encode(), 2<<20)
	if err != nil {
		discoveryReplyError(w, err)
		return
	}
	var list struct {
		Servers []struct {
			Server registryServer `json:"server"`
			Meta   map[string]struct {
				Status string `json:"status"`
			} `json:"_meta"`
		} `json:"servers"`
		Metadata struct {
			NextCursor string `json:"nextCursor"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Servers == nil {
		discoveryReplyError(w, discoveryErr(502, "invalid_metadata", "MCP 官方目录暂时无法读取"))
		return
	}
	items := []map[string]any{}
	for _, record := range list.Servers {
		if state := record.Meta["io.modelcontextprotocol.registry/official"].Status; state != "" && state != "active" {
			continue
		}
		entry := record.Server
		remotes := []map[string]any{}
		packages := []map[string]any{}
		connectable := false
		for _, remote := range entry.Remotes {
			required := false
			headers := []map[string]any{}
			for _, h := range remote.Headers {
				if h.Required || h.Secret {
					required = true
				}
				headers = append(headers, map[string]any{"name": h.Name, "description": h.Description, "required": h.Required, "secret": h.Secret})
			}
			can := remote.Type == "streamable-http" && publicMCPURL(remote.URL)
			connectable = connectable || can
			remotes = append(remotes, map[string]any{"url": remote.URL, "transport": remote.Type, "auth_required": required, "auth_unknown": !required, "headers": headers, "connectable": can})
		}
		for _, p := range entry.Packages {
			packages = append(packages, map[string]any{"registry_type": p.Registry, "identifier": p.Identifier, "version": p.Version, "transport": p.Transport.Type, "runtime_hint": p.RuntimeHint, "runtime_arguments": safeRegistryArguments(p.RuntimeArguments), "package_arguments": safeRegistryArguments(p.PackageArguments), "environment_variables": safeRegistryEnvironment(p.EnvironmentVariables)})
		}
		link := entry.Website
		if !publicMCPURL(link) {
			link = entry.Repository.URL
		}
		if !publicMCPURL(link) {
			link = ""
		}
		repository := entry.Repository.URL
		if !publicMCPURL(repository) {
			repository = ""
		}
		name := entry.Title
		if name == "" {
			name = entry.Name
		}
		items = append(items, map[string]any{"id": entry.Name, "name": name, "description": entry.Description, "version": entry.Version, "repository": repository, "url": link, "remotes": remotes, "packages": packages, "connectable": connectable, "source": "official-mcp-registry"})
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": list.Metadata.NextCursor, "source": "official-mcp-registry", "search_scope": "name"})
}
