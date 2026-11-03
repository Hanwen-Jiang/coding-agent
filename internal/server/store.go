package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Project 是一个工作区：租户下的一个基线 Git 仓库（<工作区根>/<租户>/<ID>）。它下面可以开很多会话（Session），
// 每个会话从基线仓库复制一份自己的工作副本，互不干扰，可以同时跑。
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	// 从 Git 平台克隆的工作区：平台（github、gitee、gitlab、gitea、bitbucket）与仓库路径（owner/repo，GitLab 可有多级群组）；
	// 只有这样的工作区能推送、开 PR/MR。
	Provider string `json:"git_provider,omitempty"`
	Repo     string `json:"repo,omitempty"`
	// 多平台之前只有 GitHub：读到时转成 Provider、Repo。
	GitHubRepo string `json:"github_repo,omitempty"`
	// 克隆时的分支（PR 的目标分支）。
	BaseBranch string `json:"base_branch,omitempty"`

	// 以下是会话出现之前的字段（一个项目一段对话、直接在基线仓库里改）。读到时迁移成这个工作区的 main 会话，之后不再写。
	SessionID  string `json:"session_id,omitempty"`
	Branch     string `json:"branch,omitempty"`
	PRURL      string `json:"pr_url,omitempty"`
	PRNumber   int    `json:"pr_number,omitempty"`
	PushedHead string `json:"pushed_head,omitempty"`
}

// Session 是工作区里的一个会话：一段对话（Runtime 的一个 Session）加一份独立的工作副本。
type Session struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	// UpdatedAt 是最近一次提交任务的时间（侧栏按它排序）。
	UpdatedAt time.Time `json:"updated_at"`
	// WorkspaceID 是 Runtime 的 workspace id，也是工作副本的目录名（<工作区根>/<租户>/<WorkspaceID>）；
	// 新会话是 <项目>--<会话>，迁移来的 main 会话就是项目自己的目录。
	WorkspaceID      string `json:"workspace_id"`
	RuntimeSessionID string `json:"runtime_session_id,omitempty"`
	// EnvironmentID 是独立于项目（基线仓库）的环境配置；运行时状态不存于 coding-web。
	EnvironmentID string `json:"environment_id,omitempty"`
	// EnvironmentHandoff 是这份配置交给 Runtime 了没有（见 envhandoff.go）：pending 表示只记在这里、Runtime 还不知道。
	EnvironmentHandoff string `json:"environment_handoff,omitempty"`
	// 复制时基线仓库的提交。
	BaseCommit string `json:"base_commit,omitempty"`
	// Agent 的改动推到 Branch（agent/…），PR 记在 PRURL、PRNumber；PushedHead 是上次推送的提交。
	Branch     string `json:"branch,omitempty"`
	PRURL      string `json:"pr_url,omitempty"`
	PRNumber   int    `json:"pr_number,omitempty"`
	PushedHead string `json:"pushed_head,omitempty"`
}

// Environment 是一个可由会话选择的环境配置。它故意只保存普通配置：
// setup/start 脚本与 .env 变量会随配置返回给创建者；网络密钥、令牌等机密由 Runtime
// 的 SecretStore/代理管理，不进入这里的持久化记录。
//
// DeletedAt 是配置删除的墓碑时间。清理环境及其运行时资源由 Runtime 负责，coding-web
// 不在这里伪造 ready、reused 或 expiry 状态。
type Environment struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Privacy      string `json:"privacy"`
	SetupScript  string `json:"setup_script,omitempty"`
	StartCommand string `json:"start_command,omitempty"`
	Env          string `json:"env,omitempty"`
	// Network 是联网需求（禁止、受限列表、完全开放）；真正放行与否由 Runtime 按租户策略核对，只能收窄。
	Network EnvironmentNetwork `json:"network"`
	// Secrets 是 .env 的密钥：只写不读，值按（租户、用户、环境、名字）加密存放，不回给浏览器；删除环境时立即删掉。
	Secrets []EnvironmentSecret `json:"secrets,omitempty"`
	// Version 每次修改加一；会话的快照记下它开出时的版本。
	Version   int        `json:"version"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	CreatedBy string     `json:"created_by"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// EnvironmentSecret 是一个密钥：名字与加密后的值（AES-256-GCM，见 Server.sealSecret）。
type EnvironmentSecret struct {
	Name   string `json:"name"`
	Sealed string `json:"sealed"`
}

// EnvironmentSnapshot 是会话开出时环境配置的快照（2026-11-03 人定）：只有不是机密的设置（安装脚本、启动命令、普通变量、联网需求）
// 与密钥的名字，没有密钥的值。会话从此只用它：之后改配置不影响已开的会话；删掉配置时会话照用快照，只是从此没有密钥。
// 存在会话自己的文件里（env-snapshot-<workspace>.json），不进会话的 JSON：同租户的别人也能列出会话，但环境配置只有本人看得到。
type EnvironmentSnapshot struct {
	EnvironmentID string             `json:"environment_id"`
	Name          string             `json:"name"`
	Version       int                `json:"version"`
	UpdatedAt     time.Time          `json:"updated_at"`
	SetupScript   string             `json:"setup_script,omitempty"`
	StartCommand  string             `json:"start_command,omitempty"`
	Env           string             `json:"env,omitempty"`
	Network       EnvironmentNetwork `json:"network"`
	SecretNames   []string           `json:"secret_names,omitempty"`
	TakenAt       time.Time          `json:"taken_at"`
}

// EnvironmentNetwork 是环境配置里的联网需求（人工验收 Q29、Q30）：mode 是 deny、allowlist 或 open；allowlist 时
// AllowedHosts 是允许的域名，*.example.com 匹配子域名（与 Runtime 的 host_pattern 写法一致，不收裸的 *）。
// 只是配置：不是授权，也没有状态；授权属于 Task，由 Runtime 发放与收回。
type EnvironmentNetwork struct {
	Mode         string   `json:"mode"`
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
}

// TaskRecord 是在项目里下过的一个任务（状态以 Runtime 为准，这里只缓存最后一次看到的）。
type TaskRecord struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	// 修改排队中的消息时先记在这里，Runtime 确认改好了才换掉 Message。
	PendingMessage string    `json:"pending_message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	CreatedBy      string    `json:"created_by"`
	BaseCommit     string    `json:"base_commit,omitempty"`
	Status         string    `json:"status"`
	// 提交时选的沙箱等级（effect 上限：read 或 write_reversible）与模型倾向（economy、balanced、best）。
	Sandbox string `json:"sandbox,omitempty"`
	Model   string `json:"model,omitempty"`
	// 从 Task State 读到的花费（各 Round 的 cost_usd 之和）与轮数；Usage 是 QueryTask 的 usage（Runtime 填了才有）。
	CostUSD float64         `json:"cost_usd,omitempty"`
	Rounds  int             `json:"rounds,omitempty"`
	Usage   json.RawMessage `json:"usage,omitempty"`
	// Settled 表示任务已结束、上面的计量已经取过，之后不再查 Runtime。
	Settled bool `json:"settled,omitempty"`
}

func terminal(status string) bool {
	switch status {
	case "TASK_STATUS_DONE", "TASK_STATUS_ABORTED", "TASK_STATUS_CANCELLED", "TASK_STATUS_FAILED":
		return true
	}
	return false
}

// Store 把元数据存成 JSON 文件：<dir>/<租户>/projects.json、sessions.json、environments.json
// 与 tasks-<会话的 WorkspaceID>.json。MVP 用，单进程。
type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) projectsFile(tenant string) string {
	return filepath.Join(s.dir, tenant, "projects.json")
}

func (s *Store) sessionsFile(tenant string) string {
	return filepath.Join(s.dir, tenant, "sessions.json")
}

func (s *Store) environmentsFile(tenant string) string {
	return filepath.Join(s.dir, tenant, "environments.json")
}

func (s *Store) tasksFile(tenant, workspaceID string) string {
	return filepath.Join(s.dir, tenant, "tasks-"+workspaceID+".json")
}

func (s *Store) snapshotFile(tenant, workspaceID string) string {
	return filepath.Join(s.dir, tenant, "env-snapshot-"+workspaceID+".json")
}

// sessionFiles 是一个工作副本自己的文件（任务记录、环境配置的快照）：删会话、删工作区时一起删。
func (s *Store) sessionFiles(tenant, workspaceID string) []string {
	return []string{s.tasksFile(tenant, workspaceID), s.snapshotFile(tenant, workspaceID)}
}

// Snapshot 是会话（按工作副本）的环境配置快照；没有时 ok 为 false。
func (s *Store) Snapshot(tenant, workspaceID string) (EnvironmentSnapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, err := readJSONFile[*EnvironmentSnapshot](s.snapshotFile(tenant, workspaceID))
	if err != nil || snap == nil {
		return EnvironmentSnapshot{}, false, err
	}
	return *snap, true, nil
}

// PutSnapshot 记下会话的环境配置快照（只在开会话时写一次）。
func (s *Store) PutSnapshot(tenant, workspaceID string, snap EnvironmentSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFileAtomic(s.snapshotFile(tenant, workspaceID), snap)
}

func (s *Store) Sessions(tenant string) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSONFile[[]Session](s.sessionsFile(tenant))
}

// UpdateSessions 在锁里读出、修改、写回会话列表。
func (s *Store) UpdateSessions(tenant string, fn func([]Session) ([]Session, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := readJSONFile[[]Session](s.sessionsFile(tenant))
	if err != nil {
		return err
	}
	sessions, err = fn(sessions)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.sessionsFile(tenant), sessions)
}

func (s *Store) Environments(tenant string) ([]Environment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSONFile[[]Environment](s.environmentsFile(tenant))
}

func (s *Store) Environment(tenant, id string) (Environment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	envs, err := readJSONFile[[]Environment](s.environmentsFile(tenant))
	if err != nil {
		return Environment{}, false, err
	}
	for _, env := range envs {
		if env.ID == id {
			return env, true, nil
		}
	}
	return Environment{}, false, nil
}

// UpdateEnvironments 在锁里读出、修改、写回环境配置列表。
func (s *Store) UpdateEnvironments(tenant string, fn func([]Environment) ([]Environment, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	envs, err := readJSONFile[[]Environment](s.environmentsFile(tenant))
	if err != nil {
		return err
	}
	envs, err = fn(envs)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.environmentsFile(tenant), envs)
}

// DeleteEnvironment 写入删除墓碑，保留记录供 Runtime 做异步资源清理和审计。
func (s *Store) DeleteEnvironment(tenant, id string, at time.Time) error {
	return s.UpdateEnvironments(tenant, func(envs []Environment) ([]Environment, error) {
		for i := range envs {
			if envs[i].ID == id && envs[i].DeletedAt == nil {
				t := at
				envs[i].DeletedAt = &t
				// 密钥立即删掉（常常是因为密钥泄露才删环境）：墓碑里不留加密后的值，用它的会话从此拿不到密钥。
				envs[i].Secrets = nil
				break
			}
		}
		return envs, nil
	})
}

// HasTasks 判断某个工作副本有没有任务记录（迁移旧项目时用）。
func (s *Store) HasTasks(tenant, workspaceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := os.Stat(s.tasksFile(tenant, workspaceID))
	return err == nil
}

// DeleteSession 删掉一个会话的元数据与任务记录（工作副本由调用方删）。
func (s *Store) DeleteSession(tenant string, sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := readJSONFile[[]Session](s.sessionsFile(tenant))
	if err != nil {
		return err
	}
	sessions = slices.DeleteFunc(sessions, func(x Session) bool { return x.ProjectID == sess.ProjectID && x.ID == sess.ID })
	if err := writeFileAtomic(s.sessionsFile(tenant), sessions); err != nil {
		return err
	}
	for _, f := range s.sessionFiles(tenant, sess.WorkspaceID) {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func readJSONFile[T any](path string) (T, error) {
	var v T
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(data, &v)
}

func writeFileAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	// Windows 上别的进程正读着目标文件时 rename 会失败，重试几次。
	for attempt := 0; ; attempt++ {
		err = os.Rename(tmp.Name(), path)
		if err == nil || attempt >= 20 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// readProjects 读项目列表，把多平台之前的 github_repo 转成 Provider、Repo。
func (s *Store) readProjects(tenant string) ([]Project, error) {
	projects, err := readJSONFile[[]Project](s.projectsFile(tenant))
	for i := range projects {
		if p := &projects[i]; p.GitHubRepo != "" && p.Repo == "" {
			p.Provider, p.Repo, p.GitHubRepo = "github", p.GitHubRepo, ""
		}
	}
	return projects, err
}

func (s *Store) Projects(tenant string) ([]Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readProjects(tenant)
}

func (s *Store) Project(tenant, id string) (Project, bool, error) {
	projects, err := s.Projects(tenant)
	if err != nil {
		return Project{}, false, err
	}
	for _, p := range projects {
		if p.ID == id {
			return p, true, nil
		}
	}
	return Project{}, false, nil
}

// UpdateProjects 在锁里读出、修改、写回项目列表。
func (s *Store) UpdateProjects(tenant string, fn func([]Project) ([]Project, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	projects, err := s.readProjects(tenant)
	if err != nil {
		return err
	}
	projects, err = fn(projects)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.projectsFile(tenant), projects)
}

// Tasks 是一个会话（按它的 WorkspaceID）的任务列表。
func (s *Store) Tasks(tenant, workspaceID string) ([]TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSONFile[[]TaskRecord](s.tasksFile(tenant, workspaceID))
}

// UpdateTasks 在锁里读出、修改、写回一个会话的任务列表。
func (s *Store) UpdateTasks(tenant, workspaceID string, fn func([]TaskRecord) []TaskRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := readJSONFile[[]TaskRecord](s.tasksFile(tenant, workspaceID))
	if err != nil {
		return err
	}
	return writeFileAtomic(s.tasksFile(tenant, workspaceID), fn(tasks))
}

// DeleteProject 删掉工作区与它所有会话的元数据、任务记录（目录由调用方删）。
func (s *Store) DeleteProject(tenant, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	projects, err := readJSONFile[[]Project](s.projectsFile(tenant))
	if err != nil {
		return err
	}
	projects = slices.DeleteFunc(projects, func(p Project) bool { return p.ID == id })
	if err := writeFileAtomic(s.projectsFile(tenant), projects); err != nil {
		return err
	}
	sessions, err := readJSONFile[[]Session](s.sessionsFile(tenant))
	if err != nil {
		return err
	}
	files := s.sessionFiles(tenant, id)
	for _, x := range sessions {
		if x.ProjectID == id {
			files = append(files, s.sessionFiles(tenant, x.WorkspaceID)...)
		}
	}
	if err := writeFileAtomic(s.sessionsFile(tenant), slices.DeleteFunc(sessions, func(x Session) bool { return x.ProjectID == id })); err != nil {
		return err
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
