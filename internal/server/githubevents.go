package server

// GitHub App 的事件到动作（G-04，docs/plan/github-app.md 第 4 节）。处理函数注册进 G-02 的收件箱，按 delivery 去重，
// 但租约到期时同一条可能处理两次，所以每个处理函数都按“设成某个状态”来写，执行两次与执行一次结果相同。
// 事件只当提示：PR 与 CI 的状态按 API 取当前值，迟到的旧事件不覆盖新状态。日志不带负载。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/agentruntime/coding-agent/internal/github"
)

// GitHubHandlers 返回各事件的处理函数（main.go 注册进收件箱）。
func (s *Server) GitHubHandlers() map[string]github.Handler {
	return map[string]github.Handler{
		"installation":              s.onInstallation,
		"installation_repositories": s.onInstallationRepositories,
		"repository":                s.onRepository,
		"push":                      s.onPush,
		"pull_request":              s.onPullRequest,
		"workflow_run":              s.onWorkflowRun,
		"pull_request_review":       s.onPullRequestReview,
		"github_app_authorization":  s.onAppAuthorization,
	}
}

type ghRepo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type ghEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository ghRepo `json:"repository"`
	Ref        string `json:"ref"`
	Number     int    `json:"number"`
	Sender     struct {
		ID int64 `json:"id"`
	} `json:"sender"`
	ReposAdded   []ghRepo `json:"repositories_added"`
	ReposRemoved []ghRepo `json:"repositories_removed"`
	PullRequest  struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	WorkflowRun struct {
		ID         int64     `json:"id"`
		HeadBranch string    `json:"head_branch"`
		Status     string    `json:"status"`
		Conclusion string    `json:"conclusion"`
		UpdatedAt  time.Time `json:"updated_at"`
	} `json:"workflow_run"`
	Review struct {
		ID          int64     `json:"id"`
		State       string    `json:"state"`
		SubmittedAt time.Time `json:"submitted_at"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"review"`
}

func parseEvent(d github.Delivery) (ghEvent, error) {
	var e ghEvent
	if err := json.Unmarshal(d.Payload, &e); err != nil {
		return e, fmt.Errorf("投递 %s 的负载不是 JSON", d.ID)
	}
	return e, nil
}

// tenants 是数据目录下的各租户（按目录名）。
func (s *Server) tenants() []string {
	entries, err := os.ReadDir(s.cfg.DataDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && slug.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// updateGitHubProjects 改所有租户里满足 match 的 GitHub 项目，返回改到的（租户，项目）。
func (s *Server) updateGitHubProjects(match func(Project) bool, fn func(*Project)) (map[string][]Project, error) {
	out := map[string][]Project{}
	for _, tenant := range s.tenants() {
		err := s.store.UpdateProjects(tenant, func(ps []Project) ([]Project, error) {
			for i := range ps {
				if ps[i].Provider == "github" && match(ps[i]) {
					fn(&ps[i])
					out[tenant] = append(out[tenant], ps[i])
				}
			}
			return ps, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Server) onInstallation(ctx context.Context, d github.Delivery) error {
	e, err := parseEvent(d)
	if err != nil {
		return err
	}
	state := ""
	switch e.Action {
	case "suspend":
		state = "app_suspended"
	case "deleted":
		state = "app_removed"
	case "unsuspend", "created", "new_permissions_accepted":
	default:
		return nil
	}
	s.github.mu.Lock()
	s.github.perms = nil // 权限可能变了：下次重新查
	s.github.mu.Unlock()
	changed, err := s.updateGitHubProjects(func(p Project) bool { return p.InstallationID == e.Installation.ID }, func(p *Project) {
		if state != "" || p.RemoteState == "app_suspended" || p.RemoteState == "app_removed" {
			p.RemoteState = state
		}
	})
	if err != nil || state == "" {
		return err
	}
	if state == "app_removed" {
		// 卸载：关联到账号的这个安装立即去掉（G-05 第 4 条）。
		if err := s.unlinkInstallation(e.Installation.ID); err != nil {
			return err
		}
	}
	for tenant, ps := range changed {
		for _, p := range ps {
			s.cancelProjectTasks(ctx, tenant, p, msg("GitHub App 在这个仓库上的安装已暂停或卸载").In(langEN))
		}
	}
	return nil
}

// cancelProjectTasks 取消这个项目各会话里还在跑的 Task（以提交者的身份；Runtime 那边重复取消没有副作用）。
func (s *Server) cancelProjectTasks(ctx context.Context, tenant string, p Project, reason string) {
	sessions, err := s.store.Sessions(tenant)
	if err != nil {
		return
	}
	for _, sess := range sessions {
		if sess.ProjectID != p.ID {
			continue
		}
		tasks, _ := s.store.Tasks(tenant, sess.WorkspaceID)
		for _, t := range tasks {
			if terminal(t.Status) || t.CreatedBy == "" {
				continue
			}
			token := s.ingress.IssueToken(tenant, t.CreatedBy, defaultRuntimeTokenTTL)
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			var resp map[string]any
			if err := s.api.Call(cctx, token, "CancelTask", map[string]string{"taskId": t.ID, "reason": reason}, &resp); err != nil {
				log.Printf("取消 Task %s 失败：%v", t.ID, err)
			}
			cancel()
		}
	}
}

func (s *Server) onInstallationRepositories(_ context.Context, d github.Delivery) error {
	e, err := parseEvent(d)
	if err != nil {
		return err
	}
	set := func(repos []ghRepo, from, to string) error {
		ids := map[int64]bool{}
		for _, r := range repos {
			ids[r.ID] = true
		}
		_, err := s.updateGitHubProjects(func(p Project) bool { return ids[p.RepoID] && p.RemoteState == from }, func(p *Project) { p.RemoteState = to })
		return err
	}
	if err := set(e.ReposRemoved, "", "repo_removed"); err != nil {
		return err
	}
	return set(e.ReposAdded, "repo_removed", "")
}

func (s *Server) onRepository(_ context.Context, d github.Delivery) error {
	e, err := parseEvent(d)
	if err != nil || e.Repository.ID == 0 {
		return err
	}
	match := func(p Project) bool { return p.RepoID == e.Repository.ID }
	switch e.Action {
	case "renamed", "transferred":
		_, err = s.updateGitHubProjects(match, func(p *Project) { p.Repo = e.Repository.FullName })
	case "deleted":
		_, err = s.updateGitHubProjects(match, func(p *Project) { p.RemoteState = "remote_deleted" })
	}
	return err
}

func (s *Server) onPush(_ context.Context, d github.Delivery) error {
	e, err := parseEvent(d)
	if err != nil || e.Repository.ID == 0 {
		return err
	}
	_, err = s.updateGitHubProjects(func(p Project) bool {
		return p.RepoID == e.Repository.ID && p.BaseBranch != "" && e.Ref == "refs/heads/"+p.BaseBranch
	}, func(p *Project) { p.RemoteUpdated = true })
	return err
}

// updateRepoSessions 改所有租户里属于这个仓库、满足 match 的会话。
func (s *Server) updateRepoSessions(repoID int64, match func(Session) bool, fn func(*Session)) error {
	for _, tenant := range s.tenants() {
		ps, err := s.store.Projects(tenant)
		if err != nil {
			return err
		}
		projects := map[string]bool{}
		for _, p := range ps {
			if p.Provider == "github" && p.RepoID == repoID {
				projects[p.ID] = true
			}
		}
		if len(projects) == 0 {
			continue
		}
		if err := s.store.UpdateSessions(tenant, func(all []Session) ([]Session, error) {
			for i := range all {
				if projects[all[i].ProjectID] && match(all[i]) {
					fn(&all[i])
				}
			}
			return all, nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// installationGet 用一个只读、只含这个仓库的安装令牌调 GET path（PR、CI 的当前状态），用完吊销。
func (s *Server) installationGet(ctx context.Context, installation, repoID int64, perms map[string]string, path string, out any) error {
	prov := s.providers["github"]
	if s.cfg.GitHubApp == nil || prov == nil || installation == 0 {
		return fmt.Errorf("没有配置 GitHub App")
	}
	jwt, err := s.appJWT()
	if err != nil {
		return err
	}
	var issued struct {
		Token string `json:"token"`
	}
	body := map[string]any{"repository_ids": []int64{repoID}, "permissions": perms}
	if err := prov.api(ctx, jwt, http.MethodPost, "/app/installations/"+strconv.FormatInt(installation, 10)+"/access_tokens", body, &issued); err != nil {
		return err
	}
	defer func() {
		_ = prov.api(context.Background(), issued.Token, http.MethodDelete, "/installation/token", nil, nil)
	}()
	return prov.api(ctx, issued.Token, http.MethodGet, path, nil, out)
}

func (s *Server) onPullRequest(ctx context.Context, d github.Delivery) error {
	e, err := parseEvent(d)
	if err != nil || e.Repository.ID == 0 {
		return err
	}
	number := e.PullRequest.Number
	if number == 0 {
		number = e.Number
	}
	var pr struct {
		State    string  `json:"state"`
		MergedAt *string `json:"merged_at"`
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d", e.Repository.FullName, number)
	if err := s.installationGet(ctx, e.Installation.ID, e.Repository.ID, map[string]string{"pull_requests": "read"}, path, &pr); err != nil {
		return err // 取不到当前状态：重试（不用负载里的状态覆盖）
	}
	state := pr.State
	if pr.MergedAt != nil && *pr.MergedAt != "" {
		state = "merged"
	}
	return s.updateRepoSessions(e.Repository.ID, func(x Session) bool { return x.PRNumber == number }, func(x *Session) { x.PRState = state })
}

func (s *Server) onWorkflowRun(ctx context.Context, d github.Delivery) error {
	e, err := parseEvent(d)
	if err != nil || e.Repository.ID == 0 || e.WorkflowRun.HeadBranch == "" {
		return err
	}
	run := CIStatus{RunID: e.WorkflowRun.ID, Status: e.WorkflowRun.Status, Conclusion: e.WorkflowRun.Conclusion, UpdatedAt: e.WorkflowRun.UpdatedAt}
	var cur struct {
		Status     string    `json:"status"`
		Conclusion string    `json:"conclusion"`
		UpdatedAt  time.Time `json:"updated_at"`
	}
	// App 现在没有 Actions 权限（第 6 节）：读得到就按 API 的当前状态，读不到用事件里的，靠 updated_at 挡住迟到的旧事件。
	path := fmt.Sprintf("/repos/%s/actions/runs/%d", e.Repository.FullName, e.WorkflowRun.ID)
	if err := s.installationGet(ctx, e.Installation.ID, e.Repository.ID, map[string]string{"actions": "read"}, path, &cur); err == nil && !cur.UpdatedAt.IsZero() {
		run.Status, run.Conclusion, run.UpdatedAt = cur.Status, cur.Conclusion, cur.UpdatedAt
	}
	branch := e.WorkflowRun.HeadBranch
	return s.updateRepoSessions(e.Repository.ID, func(x Session) bool { return x.Branch == branch }, func(x *Session) {
		if x.CI == nil || !run.UpdatedAt.Before(x.CI.UpdatedAt) {
			c := run
			x.CI = &c
		}
	})
}

func (s *Server) onPullRequestReview(_ context.Context, d github.Delivery) error {
	e, err := parseEvent(d)
	if err != nil || e.Repository.ID == 0 || e.Review.ID == 0 {
		return err
	}
	review := PRReview{ID: e.Review.ID, State: e.Review.State, User: e.Review.User.Login, SubmittedAt: e.Review.SubmittedAt}
	return s.updateRepoSessions(e.Repository.ID, func(x Session) bool { return x.PRNumber == e.PullRequest.Number }, func(x *Session) {
		for i, r := range x.Reviews {
			if r.ID == review.ID {
				x.Reviews[i] = review
				return
			}
		}
		x.Reviews = append(x.Reviews, review)
		if len(x.Reviews) > 50 {
			x.Reviews = x.Reviews[len(x.Reviews)-50:]
		}
	})
}

func (s *Server) onAppAuthorization(_ context.Context, d github.Delivery) error {
	e, err := parseEvent(d)
	if err != nil || e.Action != "revoked" || e.Sender.ID == 0 {
		return err
	}
	ref, ok := s.lookupIdentity(identityKey("github", strconv.FormatInt(e.Sender.ID, 10)))
	if !ok {
		return nil
	}
	_, err = s.updateAccount(ref.Tenant, ref.User, func(a *Account) error {
		if c, ok := a.Connections["github"]; ok {
			c.Token, c.Refresh, c.ExpiresAt = "", "", time.Time{}
			a.Connections["github"] = c
		}
		return nil
	})
	log.Printf("GitHub 用户 %d 撤销了对 App 的授权：删了 %s/%s 的令牌", e.Sender.ID, ref.Tenant, ref.User)
	return err
}
