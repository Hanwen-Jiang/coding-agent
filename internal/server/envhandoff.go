package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// 环境配置与 Runtime 之间有两条线（人工验收 Q16–37、Q99–105）：
//
//   - 交出去：会话选定的环境配置（setup、start、.env、联网需求）交给 Runtime，由它在代码执行前跑 setup、注入变量、按租户策略发联网授权。
//     契约（ENV-02：工作区绑定哪些字段、走哪个 RPC）还在 Runtime 那边人审（S-01），所以这里只留接缝 environmentHandoff：
//     现在的实现什么都不发，会话上记 pending，/env 照实报出来。契约定下后只换这一个实现。
//   - 读回来：Runtime 的 EnvironmentService（task-service 上与 Task API 同一个地址，只读）。就绪、复用、到期只取它的 ledger 记录，
//     读不到时报 unknown 或 blocked_external，不从日志、Pod 或命令成败去猜。

// 交接状态（Session.EnvironmentHandoff）。
const (
	// handoffPending：选择记在会话上，还没有交给 Runtime（ENV-02 的契约未定）。
	handoffPending = "pending"
)

// EnvironmentHandoffInput 是交给 Runtime 的东西（2026-11-03 人定）：会话开出时的快照（不是活的配置），配置还在时再加上密钥的值；
// 配置删掉之后没有密钥。密钥只在这里解开，不写日志、不回给浏览器。
type EnvironmentHandoffInput struct {
	Snapshot EnvironmentSnapshot
	// Secrets 是快照里列着名字、配置里现在还有的密钥（轮换过的取新值）；配置删掉时为空。
	Secrets map[string]string
	// ConfigDeleted：配置已经删掉（会话照用快照，没有密钥）。
	ConfigDeleted bool
}

// environmentHandoff 是把会话的环境配置交给 Runtime 的接缝。在会话的工作副本建好之后、第一个任务之前调用；
// 返回交接状态（记在 Session.EnvironmentHandoff）。返回错误时会话照开，状态记 pending，下次可以再交。
// 输入用 environmentHandoffInput 现算：以后每次起沙箱前再交一次时，密钥按那时的配置取（删掉了就没有）。
type environmentHandoff interface {
	Bind(ctx context.Context, id identity, sess Session, in EnvironmentHandoffInput) (string, error)
}

// pendingHandoff 是 ENV-02 契约定下之前的实现：不调 Runtime。
// TODO(ENV-02)：换成调 Runtime 的工作区绑定（Runtime 仓库 docs/plan/environment-config.md 第 4、5 节）。
type pendingHandoff struct{}

func (pendingHandoff) Bind(context.Context, identity, Session, EnvironmentHandoffInput) (string, error) {
	return handoffPending, nil
}

// environmentHandoffInput 按会话的快照算出交给 Runtime 的输入。没有快照（没选环境，或快照之前开的会话）时 ok 为 false。
// 密钥只取快照里列着名字的，而且只在配置还在、仍是开会话的人的时候取；解不开的密钥报错（不拿空值顶替）。
func (s *Server) environmentHandoffInput(tenant string, sess Session) (EnvironmentHandoffInput, bool, error) {
	snap, ok, err := s.store.Snapshot(tenant, sess.WorkspaceID)
	if err != nil || !ok {
		return EnvironmentHandoffInput{}, false, err
	}
	in := EnvironmentHandoffInput{Snapshot: snap, Secrets: map[string]string{}}
	env, found, err := s.store.Environment(tenant, snap.EnvironmentID)
	if err != nil {
		return EnvironmentHandoffInput{}, false, err
	}
	if !found || env.DeletedAt != nil || env.CreatedBy != sess.CreatedBy {
		in.ConfigDeleted = true
		return in, true, nil
	}
	for _, sec := range env.Secrets {
		if !slices.Contains(snap.SecretNames, sec.Name) {
			continue
		}
		value, err := s.openSecret(tenant, env.CreatedBy, env.ID, sec.Name, sec.Sealed)
		if err != nil {
			return EnvironmentHandoffInput{}, false, err
		}
		in.Secrets[sec.Name] = value
	}
	return in, true, nil
}

// snapshotOf 是开会话那一刻环境配置的快照：不是机密的设置与密钥的名字。
func snapshotOf(env Environment, now time.Time) EnvironmentSnapshot {
	snap := EnvironmentSnapshot{
		EnvironmentID: env.ID, Name: env.Name, Version: env.Version, UpdatedAt: env.UpdatedAt,
		SetupScript: env.SetupScript, StartCommand: env.StartCommand, Env: env.Env, Network: env.Network, TakenAt: now,
	}
	if snap.Network.Mode == "" {
		snap.Network.Mode = networkDeny
	}
	for _, sec := range env.Secrets {
		snap.SecretNames = append(snap.SecretNames, sec.Name)
	}
	return snap
}

// --- 读 Runtime 的 EnvironmentService ---

// 投影的状态：ok 是读到了（记录可以为空）；unknown 是没法问或 Runtime 不肯答；blocked_external 是 Runtime 的环境状态服务
// 连不上、没有这个服务或说暂时读不到。
const (
	envStatusOK              = "ok"
	envStatusUnknown         = "unknown"
	envStatusBlockedExternal = "blocked_external"
)

// runtimeEnvironments 是 /env 里 Runtime 环境记录的投影。
type runtimeEnvironments struct {
	Status string `json:"status"`
	// Reason 是 unknown、blocked_external 的原因：no_runtime_session（这个会话还没跑过任务，Runtime 没有它的 Session）、
	// unreachable、unimplemented，或 Runtime 给的稳定代码（例如 environment_status_unavailable）。
	Reason    string          `json:"reason,omitempty"`
	Records   []envRecordView `json:"records"`
	Truncated bool            `json:"truncated,omitempty"`
}

// envRecordView 是 EnvRecord 给网页看的部分：状态、ready、reused 与各个时间都原样取自 Runtime 的 ledger；没有的时间就是空，
// 不拿最近活动时间或 Kubernetes 的 TTL 去补。
type envRecordView struct {
	EnvRef            string `json:"env_ref"`
	State             string `json:"state"`
	Ready             bool   `json:"ready"`
	Reused            bool   `json:"reused"`
	Backend           string `json:"backend,omitempty"`
	Kind              string `json:"kind,omitempty"`
	Isolation         string `json:"isolation,omitempty"`
	Runtime           string `json:"runtime,omitempty"`
	Image             string `json:"image,omitempty"`
	Writable          bool   `json:"writable"`
	TaskID            string `json:"task_id,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	ReadyAt           string `json:"ready_at,omitempty"`
	HealthConfirmedAt string `json:"health_confirmed_at,omitempty"`
	ReusedAt          string `json:"reused_at,omitempty"`
	LastUsedAt        string `json:"last_used_at,omitempty"`
	ExpiresAt         string `json:"expires_at,omitempty"`
	MaxUntil          string `json:"max_until,omitempty"`
	DestroyedAt       string `json:"destroyed_at,omitempty"`
}

// envState 读 protojson 的枚举：认识的名字去掉前缀写成小写；不认识的名字或数字（Runtime 新加的状态）一律 unknown，不当作成功。
type envState string

func (s *envState) UnmarshalJSON(b []byte) error {
	var name string
	if json.Unmarshal(b, &name) != nil {
		*s = "unknown"
		return nil
	}
	switch v := strings.TrimPrefix(name, "ENVIRONMENT_STATE_"); v {
	case "CREATING", "READY", "DESTROYING", "DESTROYED", "FAILED", "LOST":
		*s = envState(strings.ToLower(v))
	default:
		*s = "unknown"
	}
	return nil
}

type envRecordWire struct {
	EnvRef            string   `json:"envRef"`
	State             envState `json:"state"`
	Ready             bool     `json:"ready"`
	Reused            bool     `json:"reused"`
	Backend           string   `json:"backend"`
	Kind              string   `json:"kind"`
	Isolation         string   `json:"isolation"`
	Runtime           string   `json:"runtime"`
	Image             string   `json:"image"`
	Writable          bool     `json:"writable"`
	TaskID            string   `json:"taskId"`
	CreatedAt         string   `json:"createdAt"`
	ReadyAt           string   `json:"readyAt"`
	HealthConfirmedAt string   `json:"healthConfirmedAt"`
	ReusedAt          string   `json:"reusedAt"`
	LastUsedAt        string   `json:"lastUsedAt"`
	ExpiresAt         string   `json:"expiresAt"`
	MaxUntil          string   `json:"maxUntil"`
	DestroyedAt       string   `json:"destroyedAt"`
}

// envQueryTimeout：/env 不为环境状态等太久；超时按 blocked_external 报。
const envQueryTimeout = 5 * time.Second

// runtimeEnvironmentsOf 问 Runtime 这个会话（工作副本 + Runtime Session）的环境记录。只读：不建环境、不续期、不探测。
func (s *Server) runtimeEnvironmentsOf(ctx context.Context, id identity, sess Session) runtimeEnvironments {
	out := runtimeEnvironments{Records: []envRecordView{}}
	if sess.RuntimeSessionID == "" {
		out.Status, out.Reason = envStatusUnknown, "no_runtime_session"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, envQueryTimeout)
	defer cancel()
	var resp struct {
		Environments []envRecordWire `json:"environments"`
		Truncated    bool            `json:"truncated"`
	}
	req := map[string]any{"tenantId": id.TenantID, "workspaceId": sess.WorkspaceID, "sessionId": sess.RuntimeSessionID}
	if err := s.api.CallEnvironment(ctx, id.token, "ListEnvironments", req, &resp); err != nil {
		out.Status, out.Reason = envQueryFailure(err)
		return out
	}
	out.Status, out.Truncated = envStatusOK, resp.Truncated
	for _, e := range resp.Environments {
		out.Records = append(out.Records, envRecordView{
			EnvRef: e.EnvRef, State: string(e.State), Ready: e.Ready && e.State == "ready", Reused: e.Reused,
			Backend: e.Backend, Kind: e.Kind, Isolation: e.Isolation, Runtime: e.Runtime, Image: e.Image, Writable: e.Writable, TaskID: e.TaskID,
			CreatedAt: e.CreatedAt, ReadyAt: e.ReadyAt, HealthConfirmedAt: e.HealthConfirmedAt, ReusedAt: e.ReusedAt,
			LastUsedAt: e.LastUsedAt, ExpiresAt: e.ExpiresAt, MaxUntil: e.MaxUntil, DestroyedAt: e.DestroyedAt,
		})
	}
	return out
}

// envQueryFailure 把查询失败分成 blocked_external（服务连不上、没有、暂时读不到）与 unknown（别的拒绝）。
func envQueryFailure(err error) (string, string) {
	var ae *APIError
	if !errors.As(err, &ae) {
		return envStatusBlockedExternal, "unreachable"
	}
	switch {
	case ae.Note != nil || ae.Status == http.StatusBadGateway:
		return envStatusBlockedExternal, "unreachable"
	case ae.Reason != "":
		if ae.Code == "unavailable" || ae.Code == "deadline_exceeded" || ae.Status >= 500 {
			return envStatusBlockedExternal, ae.Reason
		}
		return envStatusUnknown, ae.Reason
	case ae.Code == "unimplemented" || ae.Status == http.StatusNotImplemented || ae.Status == http.StatusNotFound && ae.Code == "":
		// 没有这个服务（更早的 task-service）：路由不存在时是不带 connect 代码的 404。
		return envStatusBlockedExternal, "unimplemented"
	case ae.Code == "unavailable" || ae.Code == "deadline_exceeded" || ae.Status >= 500:
		return envStatusBlockedExternal, "unavailable"
	}
	if ae.Code != "" {
		return envStatusUnknown, ae.Code
	}
	return envStatusUnknown, "http_" + strconv.Itoa(ae.Status)
}
