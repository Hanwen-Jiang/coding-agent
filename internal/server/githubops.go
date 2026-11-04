package server

// GitHub App 的运维（G-05，docs/plan/github-app.md 第 4 节）服务端的部分：续期与签发失败的计数、按 GitHub 的安装列表对账
// （覆盖超过 3 天的停机：收件箱的对账只能补 3 天内的投递）、给界面用的状态接口、卸载时把关联的安装去掉。界面本身等网页那条线
// 放开 app.js、i18n.js 之后再做。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/agentruntime/coding-agent/internal/github"
)

type noMetrics struct{}

func (noMetrics) Count(string, int64, map[string]string)     {}
func (noMetrics) Observe(string, float64, map[string]string) {}

func (s *Server) metrics() github.Metrics {
	if s.cfg.Metrics != nil {
		return s.cfg.Metrics
	}
	return noMetrics{}
}

// ReconcileGitHubInstallations 按 GET /app/installations 与各安装（有项目用到的）的仓库列表纠正项目的远端状况与安装 ID；
// 执行两次结果相同。coding-web 每小时跑一次。
func (s *Server) ReconcileGitHubInstallations(ctx context.Context) error {
	prov := s.providers["github"]
	if s.cfg.GitHubApp == nil || prov == nil {
		return nil
	}
	jwt, err := s.appJWT()
	if err != nil {
		return err
	}
	installs := map[int64]bool{} // 安装 ID → 是否暂停
	for page := 1; page <= 20; page++ {
		var list []struct {
			ID          int64   `json:"id"`
			SuspendedAt *string `json:"suspended_at"`
		}
		if err := prov.api(ctx, jwt, http.MethodGet, fmt.Sprintf("/app/installations?per_page=100&page=%d", page), nil, &list); err != nil {
			return err
		}
		for _, x := range list {
			installs[x.ID] = x.SuspendedAt != nil && *x.SuspendedAt != ""
		}
		if len(list) < 100 {
			break
		}
	}
	// 用到的安装各列一次仓库（只读元数据的安装令牌，用完吊销）。
	repos := map[int64]map[int64]bool{}
	reachable := func(inst, repoID int64) (bool, error) {
		suspended, ok := installs[inst]
		if !ok || suspended || repoID == 0 {
			return ok, nil
		}
		if _, done := repos[inst]; !done {
			ids, err := s.installationRepos(ctx, prov, jwt, inst)
			if err != nil {
				return false, err
			}
			repos[inst] = ids
		}
		return repos[inst][repoID], nil
	}
	// 记着的安装没了、或仓库不在里面：卸载后重新安装、仓库转到别的安装之后安装 ID 会变，按仓库查一次它现在的安装。
	moved := map[string]int64{} // 仓库全名 → 现在的安装（查不到时为 0）
	for _, tenant := range s.tenants() {
		ps, _ := s.store.Projects(tenant)
		for _, p := range ps {
			if p.Provider != "github" || p.InstallationID == 0 || p.RemoteState == "remote_deleted" {
				continue
			}
			if ok, err := reachable(p.InstallationID, p.RepoID); err != nil || ok {
				if err != nil {
					return err
				}
				continue
			}
			if _, done := moved[p.Repo]; done {
				continue
			}
			var cur struct {
				ID int64 `json:"id"`
			}
			var ge *GitError
			switch err := prov.api(ctx, jwt, http.MethodGet, "/repos/"+p.Repo+"/installation", nil, &cur); {
			case errors.As(err, &ge) && ge.Status == http.StatusNotFound:
			case err != nil:
				return err
			}
			if _, ok := installs[cur.ID]; !ok {
				cur.ID = 0
			} else if _, err := reachable(cur.ID, p.RepoID); err != nil {
				return err
			}
			moved[p.Repo] = cur.ID
		}
	}
	_, err = s.updateGitHubProjects(func(p Project) bool { return p.InstallationID != 0 }, func(p *Project) {
		if inst := moved[p.Repo]; inst != 0 {
			p.InstallationID = inst
		}
		suspended, ok := installs[p.InstallationID]
		switch {
		case !ok:
			p.RemoteState = "app_removed"
		case suspended:
			p.RemoteState = "app_suspended"
		case p.RepoID != 0 && !repos[p.InstallationID][p.RepoID]:
			if p.RemoteState != "remote_deleted" {
				p.RemoteState = "repo_removed"
			}
		case p.RemoteState != "remote_deleted":
			p.RemoteState = ""
		}
	})
	return err
}

func (s *Server) installationRepos(ctx context.Context, prov *Provider, jwt string, inst int64) (map[int64]bool, error) {
	var issued struct {
		Token string `json:"token"`
	}
	body := map[string]any{"permissions": map[string]string{"metadata": "read"}}
	if err := prov.api(ctx, jwt, http.MethodPost, "/app/installations/"+strconv.FormatInt(inst, 10)+"/access_tokens", body, &issued); err != nil {
		s.metrics().Count("coding.github.issue_failures", 1, map[string]string{"use": "reconcile"})
		return nil, err
	}
	defer func() {
		_ = prov.api(context.Background(), issued.Token, http.MethodDelete, "/installation/token", nil, nil)
	}()
	out := map[int64]bool{}
	for page := 1; page <= 50; page++ {
		var res struct {
			Repositories []struct {
				ID int64 `json:"id"`
			} `json:"repositories"`
		}
		if err := prov.api(ctx, issued.Token, http.MethodGet, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), nil, &res); err != nil {
			return nil, err
		}
		for _, r := range res.Repositories {
			out[r.ID] = true
		}
		if len(res.Repositories) < 100 {
			break
		}
	}
	return out, nil
}

// handleGitHubStatus 给“账号与平台”页：连接状态、是不是旧 OAuth 的连接、关联的安装、安装入口、各项目的远端状况。
func (s *Server) handleGitHubStatus(w http.ResponseWriter, r *http.Request, id identity) {
	app := s.cfg.GitHubApp
	if app == nil {
		writeMsg(w, r, http.StatusNotFound, "没有配置 GitHub App")
		return
	}
	a, err := s.readAccount(id.TenantID, id.UserID)
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	c, connected := a.Connections["github"]
	connected = connected && c.Token != ""
	type projectView struct {
		ID            string `json:"id"`
		Repo          string `json:"repo"`
		RemoteState   string `json:"remote_state,omitempty"`
		RemoteUpdated bool   `json:"remote_updated,omitempty"`
	}
	projects := []projectView{}
	ps, _ := s.store.Projects(id.TenantID)
	for _, p := range ps {
		if p.Provider == "github" {
			projects = append(projects, projectView{ID: p.ID, Repo: p.Repo, RemoteState: p.RemoteState, RemoteUpdated: p.RemoteUpdated})
		}
	}
	installations := a.GitHubInstallations
	if installations == nil {
		installations = []int64{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"slug": app.Slug, "install_url": "https://github.com/apps/" + app.Slug + "/installations/new",
		"connected": connected, "legacy": connected && !c.App, "installations": installations, "projects": projects,
	})
}

// unlinkInstallation 把卸载的安装从所有账号的关联里去掉（G-05 第 4 条：卸载后立即清掉）。
func (s *Server) unlinkInstallation(inst int64) error {
	for _, tenant := range s.tenants() {
		users, err := os.ReadDir(filepath.Join(s.cfg.DataDir, tenant, "users"))
		if err != nil {
			continue
		}
		for _, u := range users {
			user, ok := strings.CutSuffix(u.Name(), ".json")
			if !ok || u.IsDir() || !slug.MatchString(user) {
				continue
			}
			if _, err := s.updateAccount(tenant, user, func(a *Account) error {
				a.GitHubInstallations = slices.DeleteFunc(a.GitHubInstallations, func(x int64) bool { return x == inst })
				return nil
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
