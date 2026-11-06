package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Workspaces 是工作区（Runtime 的 workspace）上 coding-web 要做的全部操作：建（克隆、复制、示例文件）、删、文件树、读文件、
// 提交记录、改动、提交、丢弃改动、推送、与远端同步。两种实现：
//
//   - runtime（默认）：经 Runtime 的 Workspace API（task-service 上的 WorkspaceService；Runtime 仓库的
//     docs/plan/workspace-layer.md）。工作区在 Runtime 那边（挂卷或存档在 ObjectStore），coding-web 不碰它的文件。
//   - local：在本机的工作区目录（<CODING_WORKSPACE_ROOT>/<租户>/<workspace>）里自己跑 git，要与 Worker 共用这块盘。
//     Workspace API 之前的做法，留给单元测试与没有 Workspace API 的 Runtime。
//
// 租户由 Runtime 令牌确定（runtime 里令牌原样转给 task-service；local 里目录按令牌里的租户拼）。
type Workspaces interface {
	Create(ctx context.Context, id identity, ws string, spec CreateSpec) (WorkspaceInfo, error)
	// Delete 删掉工作区；本来就没有时不报错。
	Delete(ctx context.Context, id identity, ws string) error
	List(ctx context.Context, id identity, ws, path string) ([]FileEntry, error)
	Read(ctx context.Context, id identity, ws, path string) (FileContent, error)
	// Log 是最近的提交（新的在前）；给了 base 时只给 HEAD 有、base 没有的。还没有提交时为空。
	Log(ctx context.Context, id identity, ws string, limit int, base string) ([]CommitInfo, error)
	// Changes 是相对 HEAD 的全部改动（含未跟踪的新文件），检测改名。
	Changes(ctx context.Context, id identity, ws string) (Changes, error)
	// Commit 把全部改动提交；没有改动时 committed 为 false。
	Commit(ctx context.Context, id identity, ws, message, name, email string) (sha string, committed bool, err error)
	Discard(ctx context.Context, id identity, ws string) error
	// Push 把 HEAD 推到远端的 branch（不强推），返回推上去的提交。
	Push(ctx context.Context, id identity, ws, branch string, auth GitAuth) (string, error)
	// Sync 取远端 branch 的最新提交并切过去（要求没有未提交的改动），返回新的 HEAD。
	Sync(ctx context.Context, id identity, ws, branch string, auth GitAuth) (string, error)
}

// GitAuth 是对远端做 git 操作的认证（各家平台的用户名与令牌见 Provider.gitAuth）；都为空时按公开仓库处理。
// Host（https://<平台>/）只有 local 要：它把 http.<Host>.extraheader 交给本机的 git。
type GitAuth struct {
	Host, User, Pass string
}

func (a GitAuth) empty() bool { return a.User == "" && a.Pass == "" }

// CreateSpec 是建工作区的来源：RepoURL（克隆）、From（复制同租户的另一个工作区）、Files（空仓库加初始文件与一次提交）三选一。
type CreateSpec struct {
	RepoURL, Ref            string
	Layout                  string
	Auth                    GitAuth
	From                    string
	Files                   []InitialFile
	AuthorName, AuthorEmail string
}

type InitialFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type WorkspaceInfo struct {
	Head, Branch string
	Layout       string
	Repos        []WorkspaceRepo
}

type FileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

type FileContent struct {
	Size     int64
	TooLarge bool
	Binary   bool
	Content  string
}

type CommitInfo struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

type DiffStat struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary"`
}

type Changes struct {
	Head, Branch string
	Files        []DiffStat
	Diff         string
	Truncated    bool
}

// workspaceError 是工作区操作的错误与给网页的 HTTP 状态（local 用；runtime 的错误是 Workspace API 的 *APIError）。
type workspaceError struct {
	Status int
	Err    error
}

func (e *workspaceError) Error() string { return e.Err.Error() }
func (e *workspaceError) Unwrap() error { return e.Err }

func wsErr(status int, err error) error { return &workspaceError{Status: status, Err: err} }

// statusOf 是一个工作区错误该回的 HTTP 状态；不认识的错误回 fallback。
func statusOf(err error, fallback int) int {
	var we *workspaceError
	if errors.As(err, &we) {
		return we.Status
	}
	var ae *APIError
	if errors.As(err, &ae) && ae.Status >= 400 && ae.Status <= 599 {
		return ae.Status
	}
	return fallback
}

// writeWorkspaceError 写出工作区操作的错误：Workspace API 的说明原样转出（同 Task API），local 的按请求的语言。
func writeWorkspaceError(w http.ResponseWriter, r *http.Request, err error, fallback int) {
	var ae *APIError
	if errors.As(err, &ae) {
		writeAPIError(w, r, err)
		return
	}
	writeErr(w, r, statusOf(err, fallback), err)
}

// isNotFound：工作区或里面的东西不存在（Workspace API 的 404，local 的 workspaceError 404）。
func isNotFound(err error) bool { return statusOf(err, 0) == http.StatusNotFound }

// --- runtime：经 Workspace API ---

type runtimeWorkspaces struct{ api *TaskAPI }

// int64JSON 读 protojson 的 int64（写成字符串，也收数字）。
type int64JSON int64

func (n *int64JSON) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(strings.Trim(string(b), `"`), 10, 64)
	*n = int64JSON(v)
	return err
}

func credential(a GitAuth) map[string]string {
	if a.empty() {
		return nil
	}
	return map[string]string{"username": a.User, "password": a.Pass}
}

func (w runtimeWorkspaces) Create(ctx context.Context, id identity, ws string, spec CreateSpec) (WorkspaceInfo, error) {
	req := map[string]any{
		"workspaceId": ws, "repoUrl": spec.RepoURL, "ref": spec.Ref, "fromWorkspaceId": spec.From, "files": spec.Files,
		"authorName": spec.AuthorName, "authorEmail": spec.AuthorEmail,
	}
	if spec.Layout == layoutMulti {
		req = map[string]any{"workspaceId": ws, "layout": layoutMulti, "fromWorkspaceId": spec.From}
	}
	if c := credential(spec.Auth); c != nil {
		req["credential"] = c
	}
	var resp struct {
		Workspace struct {
			Head   string          `json:"head"`
			Branch string          `json:"branch"`
			Layout string          `json:"layout"`
			Repos  []WorkspaceRepo `json:"repos"`
		} `json:"workspace"`
	}
	if err := w.api.workspaceOperation(ctx, id.token, "CreateWorkspace", req, &resp); err != nil {
		return WorkspaceInfo{}, err
	}
	return WorkspaceInfo{Head: resp.Workspace.Head, Branch: resp.Workspace.Branch, Layout: resp.Workspace.Layout, Repos: resp.Workspace.Repos}, nil
}

func (w runtimeWorkspaces) Delete(ctx context.Context, id identity, ws string) error {
	err := w.api.workspaceOperation(ctx, id.token, "DeleteWorkspace", map[string]any{"workspaceId": ws}, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (w runtimeWorkspaces) List(ctx context.Context, id identity, ws, path string) ([]FileEntry, error) {
	var resp struct {
		Entries []struct {
			Name string    `json:"name"`
			Type string    `json:"type"`
			Size int64JSON `json:"size"`
		} `json:"entries"`
	}
	if err := w.api.CallWorkspace(ctx, id.token, "ListFiles", map[string]any{"workspaceId": ws, "path": path}, &resp); err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		out = append(out, FileEntry{Name: e.Name, Dir: e.Type == "FILE_TYPE_DIRECTORY", Size: int64(e.Size)})
	}
	return out, nil
}

func (w runtimeWorkspaces) Read(ctx context.Context, id identity, ws, path string) (FileContent, error) {
	var resp struct {
		Size     int64JSON `json:"size"`
		TooLarge bool      `json:"tooLarge"`
		Binary   bool      `json:"binary"`
		Content  string    `json:"content"`
	}
	if err := w.api.CallWorkspace(ctx, id.token, "ReadFile", map[string]any{"workspaceId": ws, "path": path}, &resp); err != nil {
		return FileContent{}, err
	}
	return FileContent{Size: int64(resp.Size), TooLarge: resp.TooLarge, Binary: resp.Binary, Content: resp.Content}, nil
}

func (w runtimeWorkspaces) Log(ctx context.Context, id identity, ws string, limit int, base string) ([]CommitInfo, error) {
	var resp struct {
		Commits []struct {
			Sha        string `json:"sha"`
			AuthorName string `json:"authorName"`
			AuthoredAt string `json:"authoredAt"`
			Subject    string `json:"subject"`
		} `json:"commits"`
	}
	if err := w.api.CallWorkspace(ctx, id.token, "GetLog", map[string]any{"workspaceId": ws, "repo": id.repo, "limit": limit, "base": base}, &resp); err != nil {
		return nil, err
	}
	out := make([]CommitInfo, 0, len(resp.Commits))
	for _, c := range resp.Commits {
		out = append(out, CommitInfo{Hash: c.Sha, Author: c.AuthorName, Date: c.AuthoredAt, Subject: c.Subject})
	}
	return out, nil
}

func (w runtimeWorkspaces) Changes(ctx context.Context, id identity, ws string) (Changes, error) {
	var resp struct {
		Diff      string     `json:"diff"`
		Truncated bool       `json:"truncated"`
		Head      string     `json:"head"`
		Branch    string     `json:"branch"`
		Stats     []DiffStat `json:"stats"`
	}
	if err := w.api.CallWorkspace(ctx, id.token, "GetDiff", map[string]any{"workspaceId": ws, "repo": id.repo, "findRenames": true}, &resp); err != nil {
		return Changes{}, err
	}
	if resp.Stats == nil {
		resp.Stats = []DiffStat{}
	}
	return Changes{Head: resp.Head, Branch: resp.Branch, Files: resp.Stats, Diff: resp.Diff, Truncated: resp.Truncated}, nil
}

func (w runtimeWorkspaces) Commit(ctx context.Context, id identity, ws, message, name, email string) (string, bool, error) {
	var resp struct {
		Sha       string `json:"sha"`
		Committed bool   `json:"committed"`
	}
	req := map[string]any{"workspaceId": ws, "repo": id.repo, "message": message, "authorName": name, "authorEmail": email}
	if err := w.api.workspaceOperation(ctx, id.token, "CommitWorkspace", req, &resp); err != nil {
		return "", false, err
	}
	return resp.Sha, resp.Committed, nil
}

func (w runtimeWorkspaces) Discard(ctx context.Context, id identity, ws string) error {
	return w.api.workspaceOperation(ctx, id.token, "DiscardChanges", map[string]any{"workspaceId": ws, "repo": id.repo}, nil)
}

func (w runtimeWorkspaces) Push(ctx context.Context, id identity, ws, branch string, auth GitAuth) (string, error) {
	req := map[string]any{"workspaceId": ws, "repo": id.repo, "branch": branch}
	if c := credential(auth); c != nil {
		req["credential"] = c
	}
	var resp struct {
		Sha string `json:"sha"`
	}
	if err := w.api.workspaceOperation(ctx, id.token, "PushWorkspace", req, &resp); err != nil {
		return "", err
	}
	return resp.Sha, nil
}

func (w runtimeWorkspaces) Sync(ctx context.Context, id identity, ws, branch string, auth GitAuth) (string, error) {
	req := map[string]any{"workspaceId": ws, "repo": id.repo, "branch": branch}
	if c := credential(auth); c != nil {
		req["credential"] = c
	}
	var resp struct {
		Head string `json:"head"`
	}
	if err := w.api.workspaceOperation(ctx, id.token, "SyncWorkspace", req, &resp); err != nil {
		return "", err
	}
	return resp.Head, nil
}
