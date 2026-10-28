package server

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// localWorkspaces 在本机的工作区目录里自己跑 git（见 Workspaces 的说明）：目录是 <root>/<租户>/<workspace>，租户来自令牌。
type localWorkspaces struct {
	root string
	// 与 Server 共用一个（测试会改它的 AllowFileProtocol）。
	git *Git
}

func (w localWorkspaces) dir(id identity, ws string) string {
	return filepath.Join(w.root, id.TenantID, ws)
}

// existing 是已有的工作区目录；没有时 404。
func (w localWorkspaces) existing(id identity, ws string) (string, error) {
	dir := w.dir(id, ws)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", wsErr(http.StatusNotFound, msg("工作区不存在"))
	}
	return dir, nil
}

func (w localWorkspaces) runAuth(ctx context.Context, dir string, auth GitAuth, args ...string) (string, error) {
	if auth.empty() {
		return w.git.Run(ctx, dir, args...)
	}
	return w.git.RunWithAuth(ctx, dir, auth.Host, auth.User, auth.Pass, args...)
}

func (w localWorkspaces) Create(ctx context.Context, id identity, ws string, spec CreateSpec) (WorkspaceInfo, error) {
	dir := w.dir(id, ws)
	if _, err := os.Stat(dir); err == nil {
		return WorkspaceInfo{}, wsErr(http.StatusConflict, msg("工作区已经存在"))
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return WorkspaceInfo{}, err
	}
	err := func() error {
		switch {
		case spec.RepoURL != "":
			args := []string{"clone", "--depth", "50", "--no-tags"}
			if spec.Ref != "" {
				args = append(args, "--branch", spec.Ref)
			}
			_, err := w.runAuth(ctx, filepath.Dir(dir), spec.Auth, append(args, "--", spec.RepoURL, dir)...)
			return err
		case spec.From != "":
			src, err := w.existing(id, spec.From)
			if err != nil {
				return err
			}
			if _, err := w.git.CloneLocal(ctx, src, dir, w.git.Branch(ctx, src)); err != nil {
				return err
			}
			// 远端沿用源工作区的（本地克隆之后 origin 是源的目录）。
			if origin, err := w.git.Run(ctx, src, "remote", "get-url", "origin"); err == nil && strings.HasPrefix(strings.TrimSpace(origin), "https://") {
				_, err = w.git.Run(ctx, dir, "remote", "set-url", "origin", strings.TrimSpace(origin))
				return err
			}
			return nil
		default:
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			for _, f := range spec.Files {
				target := filepath.Join(dir, filepath.FromSlash(f.Path))
				if f.Path == "" || strings.ContainsAny(f.Path, "\\\x00") || filepath.IsAbs(f.Path) || slices.ContainsFunc(strings.Split(f.Path, "/"), func(p string) bool {
					return p == "" || p == "." || p == ".." || p == ".git"
				}) {
					return msg("路径不合法")
				}
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(target, []byte(f.Content), 0o644); err != nil {
					return err
				}
			}
			commands := [][]string{{"init", "-q", "-b", "main"}}
			if len(spec.Files) > 0 {
				commands = append(commands, []string{"add", "-A"}, []string{"commit", "-q", "-m", "Initial commit"})
			}
			for _, args := range commands {
				if _, err := w.git.RunAs(ctx, dir, spec.AuthorName, spec.AuthorEmail, args...); err != nil {
					return err
				}
			}
			return nil
		}
	}()
	if err != nil {
		_ = os.RemoveAll(dir)
		if statusOf(err, 0) != 0 {
			return WorkspaceInfo{}, err
		}
		return WorkspaceInfo{}, wsErr(http.StatusBadRequest, err)
	}
	return WorkspaceInfo{Head: w.git.Head(ctx, dir), Branch: w.git.Branch(ctx, dir)}, nil
}

func (w localWorkspaces) Delete(_ context.Context, id identity, ws string) error {
	return os.RemoveAll(w.dir(id, ws))
}

func (w localWorkspaces) List(_ context.Context, id identity, ws, path string) ([]FileEntry, error) {
	root, err := w.existing(id, ws)
	if err != nil {
		return nil, err
	}
	dir, err := safePath(root, path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, wsErr(http.StatusNotFound, msg("目录不存在"))
	}
	if err != nil {
		return nil, wsErr(http.StatusBadRequest, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, wsErr(http.StatusNotFound, msg("目录不存在"))
	}
	out := []FileEntry{}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileEntry{Name: e.Name(), Dir: e.IsDir(), Size: info.Size()})
	}
	return out, nil
}

func (w localWorkspaces) Read(_ context.Context, id identity, ws, path string) (FileContent, error) {
	root, err := w.existing(id, ws)
	if err != nil {
		return FileContent{}, err
	}
	file, err := safePath(root, path)
	if errors.Is(err, fs.ErrNotExist) {
		return FileContent{}, wsErr(http.StatusNotFound, msg("文件不存在"))
	}
	if err != nil {
		return FileContent{}, wsErr(http.StatusBadRequest, err)
	}
	info, err := os.Stat(file)
	if err != nil || info.IsDir() {
		return FileContent{}, wsErr(http.StatusNotFound, msg("文件不存在"))
	}
	if info.Size() > maxFileBytes {
		return FileContent{Size: info.Size(), TooLarge: true}, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return FileContent{}, err
	}
	if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
		return FileContent{Size: info.Size(), Binary: true}, nil
	}
	return FileContent{Size: info.Size(), Content: string(data)}, nil
}

func (w localWorkspaces) Log(ctx context.Context, id identity, ws string, limit int, base string) ([]CommitInfo, error) {
	dir, err := w.existing(id, ws)
	if err != nil {
		return nil, err
	}
	commits := []CommitInfo{}
	if w.git.Head(ctx, dir) == "" {
		return commits, nil
	}
	rev := "HEAD"
	if base != "" {
		rev = base + "..HEAD"
	}
	out, err := w.git.Run(ctx, dir, "log", "-n", strconv.Itoa(limit), "--date=iso-strict", "--pretty=format:%H%x1f%an%x1f%ad%x1f%s", rev, "--")
	if err != nil {
		return nil, wsErr(http.StatusNotFound, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Split(line, "\x1f"); len(f) == 4 {
			commits = append(commits, CommitInfo{Hash: f[0], Author: f[1], Date: f[2], Subject: f[3]})
		}
	}
	return commits, nil
}

func (w localWorkspaces) Changes(ctx context.Context, id identity, ws string) (Changes, error) {
	dir, err := w.existing(id, ws)
	if err != nil {
		return Changes{}, err
	}
	if _, err = w.git.Run(ctx, dir, "add", "--intent-to-add", "--", "."); err != nil {
		return Changes{}, err
	}
	numstat, err := w.git.Run(ctx, dir, "diff", "HEAD", "--numstat", "--no-color", "--no-ext-diff", "--find-renames")
	if err != nil {
		return Changes{}, err
	}
	files := []DiffStat{}
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		added, _ := strconv.Atoi(f[0])
		deleted, _ := strconv.Atoi(f[1])
		files = append(files, DiffStat{Path: f[2], Added: added, Deleted: deleted, Binary: f[0] == "-"})
	}
	diff, err := w.git.Run(ctx, dir, "diff", "HEAD", "--no-color", "--no-ext-diff", "--find-renames")
	if err != nil {
		return Changes{}, err
	}
	truncated := false
	if len(diff) > maxDiffBytes {
		diff, truncated = diff[:maxDiffBytes], true
	}
	return Changes{Head: w.git.Head(ctx, dir), Branch: w.git.Branch(ctx, dir), Files: files, Diff: diff, Truncated: truncated}, nil
}

func (w localWorkspaces) Commit(ctx context.Context, id identity, ws, message, name, email string) (string, bool, error) {
	dir, err := w.existing(id, ws)
	if err != nil {
		return "", false, err
	}
	if _, err := w.git.Run(ctx, dir, "add", "-A"); err != nil {
		return "", false, err
	}
	status, err := w.git.Run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(status) == "" {
		return w.git.Head(ctx, dir), false, nil
	}
	if _, err := w.git.RunAs(ctx, dir, name, email, "commit", "-q", "-m", message); err != nil {
		return "", false, err
	}
	return w.git.Head(ctx, dir), true, nil
}

func (w localWorkspaces) Discard(ctx context.Context, id identity, ws string) error {
	dir, err := w.existing(id, ws)
	if err != nil {
		return err
	}
	for _, args := range [][]string{{"reset", "-q", "--hard", "HEAD"}, {"clean", "-q", "-fd"}} {
		if _, err := w.git.Run(ctx, dir, args...); err != nil {
			return err
		}
	}
	return nil
}

func (w localWorkspaces) Push(ctx context.Context, id identity, ws, branch string, auth GitAuth) (string, error) {
	dir, err := w.existing(id, ws)
	if err != nil {
		return "", err
	}
	if _, err := w.runAuth(ctx, dir, auth, "push", "-q", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return "", wsErr(http.StatusBadGateway, err)
	}
	return w.git.Head(ctx, dir), nil
}

func (w localWorkspaces) Sync(ctx context.Context, id identity, ws, branch string, auth GitAuth) (string, error) {
	dir, err := w.existing(id, ws)
	if err != nil {
		return "", err
	}
	if status, _ := w.git.Run(ctx, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		return "", wsErr(http.StatusConflict, msg("还有未提交的改动：先推送、提交或丢弃"))
	}
	if _, err := w.runAuth(ctx, dir, auth, "fetch", "-q", "--depth", "50", "origin", branch); err != nil {
		return "", wsErr(http.StatusBadGateway, msg("拉取失败：%v", err))
	}
	target := branch
	if out, err := w.git.Run(ctx, dir, "rev-parse", "--verify", "-q", "origin/"+branch); err == nil && strings.TrimSpace(out) != "" {
		target = "origin/" + branch
	}
	if _, err := w.git.Run(ctx, dir, "checkout", "-q", "-B", branch, target); err != nil {
		return "", err
	}
	return w.git.Head(ctx, dir), nil
}
