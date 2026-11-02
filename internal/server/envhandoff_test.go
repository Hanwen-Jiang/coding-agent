package server

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// /env 的 Runtime 环境记录（人工验收 Q99–105）：只取 EnvironmentService 的 ledger 记录，ready、reused、到期原样投影；
// 不认识的状态是 unknown；读不到时是 blocked_external 或 unknown，不猜。
func TestEnvProjectsRuntimeEnvironmentRecords(t *testing.T) {
	ts, fake, _ := newServer(t)
	c := newClient(t, ts.URL)
	pid := sampleProject(t, c)
	code, env, raw := c.do("POST", "/api/environments", map[string]any{"name": "Python", "setup_script": "pip install -r requirements.txt"})
	if code != http.StatusCreated {
		t.Fatalf("建环境：%d %s", code, raw)
	}
	code, sess, raw := c.do("POST", "/api/projects/"+pid+"/sessions", map[string]string{"title": "跑一跑", "environment_id": env["id"].(string)})
	if code != http.StatusCreated || sess["environment_handoff"] != handoffPending {
		t.Fatalf("开会话：%d %s", code, raw)
	}
	api := "/api/projects/" + pid + "/sessions/" + sess["id"].(string)
	envOf := func() (map[string]any, map[string]any) {
		t.Helper()
		code, out, raw := c.do("GET", api+"/env", nil)
		if code != http.StatusOK {
			t.Fatalf("env：%d %s", code, raw)
		}
		renv, _ := out["runtime_environments"].(map[string]any)
		return out, renv
	}

	// 还没跑过任务：Runtime 没有这个会话的 Session，没法问，也就不问。
	out, renv := envOf()
	if renv["status"] != envStatusUnknown || renv["reason"] != "no_runtime_session" || len(fake.envQueries) != 0 {
		t.Fatalf("没有 Runtime Session 时应为 unknown：%v（问了 %d 次）", renv, len(fake.envQueries))
	}
	config, _ := out["environment"].(map[string]any)
	if config["name"] != "Python" || config["handoff"] != handoffPending || config["deleted"] != false {
		t.Fatalf("会话选定的环境配置：%v", out["environment"])
	}

	if code, _, raw := c.do("POST", api+"/tasks", map[string]string{"message": "跑测试"}); code != http.StatusCreated {
		t.Fatalf("下任务：%d %s", code, raw)
	}
	fake.envReply = map[string]any{"truncated": true, "environments": []any{
		map[string]any{
			"envRef": "sbe-new", "tenantId": "acme", "state": "ENVIRONMENT_STATE_READY", "ready": true, "reused": true, "backend": "k8s", "kind": "k8s",
			"isolation": "container", "writable": true, "runtime": "python", "createdAt": "2026-11-03T01:00:00Z", "healthConfirmedAt": "2026-11-03T01:00:05Z",
			"reusedAt": "2026-11-03T01:10:00Z", "lastUsedAt": "2026-11-03T01:11:00Z", "expiresAt": "2026-11-03T01:41:00Z", "maxUntil": "2026-11-03T05:00:00Z",
			"backendEnvId": "pod-123",
		},
		// ledger 是 READY，但健康没确认：不算就绪。
		map[string]any{"envRef": "sbe-unconfirmed", "state": "ENVIRONMENT_STATE_READY", "createdAt": "2026-11-03T00:50:00Z"},
		// 自相矛盾的记录（ready 却不是 READY）也不当就绪。
		map[string]any{"envRef": "sbe-odd", "state": "ENVIRONMENT_STATE_DESTROYED", "ready": true},
		// Runtime 新加的状态与数字的枚举：unknown，不当成功。
		map[string]any{"envRef": "sbe-future", "state": "ENVIRONMENT_STATE_HIBERNATING"},
		map[string]any{"envRef": "sbe-number", "state": 9},
	}}
	out, renv = envOf()
	if len(fake.envQueries) != 1 {
		t.Fatalf("应问一次 ListEnvironments，实际 %d", len(fake.envQueries))
	}
	q := fake.envQueries[0]
	if q["tenantId"] != "acme" || q["sessionId"] != "ses_1" || !strings.HasSuffix(q["workspaceId"].(string), "--"+sess["id"].(string)) {
		t.Fatalf("查询要限定在这个会话的工作副本与 Runtime Session：%v", q)
	}
	if got := fake.tokens[len(fake.tokens)-1]; !strings.HasPrefix(got, "Bearer ") {
		t.Fatalf("查询要带登录得到的 Runtime 令牌：%q", got)
	}
	records, _ := renv["records"].([]any)
	if renv["status"] != envStatusOK || renv["truncated"] != true || len(records) != 5 {
		t.Fatalf("投影：%v", renv)
	}
	byRef := map[string]map[string]any{}
	for _, r := range records {
		rec := r.(map[string]any)
		byRef[rec["env_ref"].(string)] = rec
	}
	first := byRef["sbe-new"]
	if first["state"] != "ready" || first["ready"] != true || first["reused"] != true || first["expires_at"] != "2026-11-03T01:41:00Z" ||
		first["max_until"] != "2026-11-03T05:00:00Z" || first["health_confirmed_at"] != "2026-11-03T01:00:05Z" || first["backend_env_id"] != nil {
		t.Fatalf("就绪、复用与到期原样取自 ledger（后端的实例名不给网页）：%v", first)
	}
	unconfirmed := byRef["sbe-unconfirmed"]
	if unconfirmed["state"] != "ready" || unconfirmed["ready"] != false || unconfirmed["reused"] != false || unconfirmed["expires_at"] != nil {
		t.Fatalf("没确认健康的 READY 不算就绪，没有的到期时间不补：%v", unconfirmed)
	}
	if byRef["sbe-odd"]["ready"] != false {
		t.Fatalf("不是 READY 的记录不当就绪：%v", byRef["sbe-odd"])
	}
	if byRef["sbe-future"]["state"] != "unknown" || byRef["sbe-number"]["state"] != "unknown" {
		t.Fatalf("不认识的状态是 unknown：%v %v", byRef["sbe-future"], byRef["sbe-number"])
	}

	// Runtime 说环境状态暂时读不到：blocked_external，带它的稳定代码。
	fake.envStatus = http.StatusServiceUnavailable
	fake.envBody = map[string]any{"code": "unavailable", "message": "Runtime 环境状态暂时不可读",
		"details": []any{map[string]any{"type": "google.rpc.ErrorInfo", "debug": map[string]any{"reason": "environment_status_unavailable", "domain": errorInfoDomain}}}}
	if _, renv = envOf(); renv["status"] != envStatusBlockedExternal || renv["reason"] != "environment_status_unavailable" || len(renv["records"].([]any)) != 0 {
		t.Fatalf("读不到时不能沿用旧的记录：%v", renv)
	}
	// 没有 EnvironmentService 的 task-service（路由不存在的 404）：blocked_external。
	fake.envStatus, fake.envBody = http.StatusNotFound, nil
	if _, renv = envOf(); renv["status"] != envStatusBlockedExternal || renv["reason"] != "unimplemented" {
		t.Fatalf("没有这个服务：%v", renv)
	}
	// 别的拒绝：unknown。
	fake.envStatus = http.StatusForbidden
	fake.envBody = map[string]any{"code": "permission_denied", "message": "不行",
		"details": []any{map[string]any{"type": "google.rpc.ErrorInfo", "debug": map[string]any{"reason": "tenant_mismatch", "domain": errorInfoDomain}}}}
	if _, renv = envOf(); renv["status"] != envStatusUnknown || renv["reason"] != "tenant_mismatch" {
		t.Fatalf("被拒绝：%v", renv)
	}

	// 删掉的环境配置：会话还显示它选的是哪一份。
	if code, _, raw := c.do("DELETE", "/api/environments/"+env["id"].(string), nil); code != http.StatusOK {
		t.Fatalf("删环境：%d %s", code, raw)
	}
	out, _ = envOf()
	if config, _ := out["environment"].(map[string]any); config["name"] != "Python" || config["deleted"] != true {
		t.Fatalf("删掉的配置：%v", out["environment"])
	}
}

func TestEnvQueryFailures(t *testing.T) {
	for _, tc := range []struct {
		err          error
		status, want string
	}{
		{errors.New("dial tcp: refused"), envStatusBlockedExternal, "unreachable"},
		{&APIError{Status: http.StatusBadGateway, Code: "unavailable", Note: msg("连不上 task-service")}, envStatusBlockedExternal, "unreachable"},
		{&APIError{Status: http.StatusNotImplemented, Code: "unimplemented"}, envStatusBlockedExternal, "unimplemented"},
		{&APIError{Status: http.StatusGatewayTimeout, Code: "deadline_exceeded"}, envStatusBlockedExternal, "unavailable"},
		{&APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Reason: "environment_status_unavailable"}, envStatusBlockedExternal, "environment_status_unavailable"},
		{&APIError{Status: http.StatusTooManyRequests, Code: "resource_exhausted", Reason: "rate_limit"}, envStatusUnknown, "rate_limit"},
		{&APIError{Status: http.StatusBadRequest, Code: "invalid_argument"}, envStatusUnknown, "invalid_argument"},
		{&APIError{Status: http.StatusNotFound, Code: "not_found"}, envStatusUnknown, "not_found"},
	} {
		if status, reason := envQueryFailure(tc.err); status != tc.status || reason != tc.want {
			t.Errorf("%v：%s %s，应为 %s %s", tc.err, status, reason, tc.status, tc.want)
		}
	}
}
