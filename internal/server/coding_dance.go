package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/agentruntime/coding-agent/internal/entry"
	"github.com/agentruntime/coding-agent/internal/entry/codingdance"
)

const consentTokenTTL = 30 * time.Minute

type codingDanceCaller struct {
	scope      string
	tenantID   string
	userID     string
	token      string
	connection *entry.Connection
}

func (s *Server) authenticateCodingDance(c *gin.Context) (codingDanceCaller, bool) {
	// 1. Existing coding-web browser session (+ X-Coding-Web CSRF guard on POST)
	if cookie, err := c.Request.Cookie(cookieName); err == nil && cookie.Value != "" {
		if c.GetHeader(csrfHeader) != "" {
			if sess, ok, err := s.sessions.lookup(cookie.Value); err == nil && ok {
				id := s.sessionIdentity(cookie.Value, sess)
				return codingDanceCaller{scope: id.TenantID + ":" + id.UserID, tenantID: id.TenantID, userID: id.UserID, token: id.token}, true
			}
		}
	}

	// 2. Bearer API token (platform CODING_DANCE_API_TOKEN or per-connection webhook secret)
	header := c.GetHeader("Authorization")
	bearer, hasBearer := strings.CutPrefix(header, "Bearer ")
	bearer = strings.TrimSpace(bearer)
	if hasBearer && bearer != "" {
		if expected := strings.TrimSpace(s.cfg.CodingDanceToken); expected != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(expected)) == 1 {
			caller := codingDanceCaller{scope: "api-token"}
			if tenant := strings.TrimSpace(c.GetHeader("X-Coding-Dance-Tenant")); tenant != "" && slug.MatchString(tenant) {
				user := strings.TrimSpace(c.GetHeader("X-Coding-Dance-User"))
				if !slug.MatchString(user) {
					user = "companion"
				}
				caller.tenantID = tenant
				caller.userID = user
				caller.token = s.ingress.IssueToken(tenant, user, 5*time.Minute)
			}
			return caller, true
		}
		if connID := strings.TrimSpace(c.GetHeader("X-Coding-Dance-Connection")); connID != "" && s.cfg.EntryStore != nil {
			conn, err := s.cfg.EntryStore.LookupConnection(c.Request.Context(), connID)
			if err == nil && conn.Kind == "coding_dance" && conn.Enabled && conn.Config["deleted"] != true && entry.VerifySecret(conn.SecretHash, bearer) {
				tok := s.ingress.IssueToken(conn.Tenant, conn.User, 5*time.Minute)
				return codingDanceCaller{
					scope:      conn.ID,
					tenantID:   conn.Tenant,
					userID:     conn.User,
					token:      tok,
					connection: &conn,
				}, true
			}
		}
	}

	writeMsg(c, http.StatusUnauthorized, "Coding Dance 接口未认证或连接已停用")
	return codingDanceCaller{}, false
}

func (s *Server) consentSecret() []byte {
	if len(s.cfg.TokenKey) > 0 {
		return s.cfg.TokenKey
	}
	if len(s.cfg.SigningKey) > 0 {
		return s.cfg.SigningKey
	}
	if strings.TrimSpace(s.cfg.CodingDanceToken) != "" {
		return []byte(s.cfg.CodingDanceToken)
	}
	return []byte("coding-dance-ephemeral-hmac-key")
}

func (s *Server) signConsentToken(scope, problemID string, ts int64) string {
	mac := hmac.New(sha256.New, s.consentSecret())
	_, _ = fmt.Fprintf(mac, "%s\x00%s\x00%d", scope, problemID, ts)
	return fmt.Sprintf("%d.%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func (s *Server) verifyConsentToken(scope, problemID, tok string, now time.Time) bool {
	tsStr, _, ok := strings.Cut(strings.TrimSpace(tok), ".")
	if !ok {
		return false
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return false
	}
	issued := time.Unix(ts, 0).UTC()
	if now.Sub(issued) > consentTokenTTL || issued.Sub(now) > 2*time.Minute {
		return false
	}
	want := s.signConsentToken(scope, problemID, ts)
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(tok)), []byte(want)) == 1
}

func resolveConsentMode(caller codingDanceCaller, reqMode string) (string, bool) {
	mode := strings.TrimSpace(reqMode)
	if caller.connection != nil {
		mode = codingdance.EffectiveConsentMode(caller.connection.Config, codingdance.Event{ConsentMode: mode})
	} else if mode == "" {
		mode = "proactive_consent"
	}
	return mode, slices.Contains(codingdance.ConsentModes, mode)
}

func companionLang(reqLang string, caller codingDanceCaller) string {
	if reqLang == langEN {
		return langEN
	}
	if reqLang == langZH {
		return langZH
	}
	if caller.connection != nil {
		if l, _ := caller.connection.Config["language"].(string); l == langEN {
			return langEN
		}
	}
	return langZH
}

// handleCodingDanceHintCheck evaluates whether a stuck/failing signal warrants asking user consent.
// Under proactive_consent, it NEVER reveals solution code, fixes, or algorithm spoilers.
func (s *Server) handleCodingDanceHintCheck(c *gin.Context) {
	caller, ok := s.authenticateCodingDance(c)
	if !ok {
		return
	}
	if s.adminTelemetry != nil {
		s.adminTelemetry.Companion.HintChecks.Add(1)
	}
	var req struct {
		ConsentMode  string `json:"consent_mode"`
		ProblemID    string `json:"problem_id"`
		ProblemTitle string `json:"problem_title"`
		Language     string `json:"language"`
		Locale       string `json:"locale"`
		StuckSeconds int    `json:"stuck_seconds"`
		AttemptCount int    `json:"attempt_count"`
		ErrorOutput  string `json:"error_output"`
		FailedTests  int    `json:"failed_tests"`
	}
	if !readJSON(c, &req) {
		return
	}
	mode, valid := resolveConsentMode(caller, req.ConsentMode)
	if !valid {
		writeMsg(c, http.StatusBadRequest, "请求参数不合法")
		return
	}
	lang := companionLang(req.Locale, caller)

	if mode == "off" {
		if s.adminTelemetry != nil {
			s.adminTelemetry.Companion.ConsentBlocked.Add(1)
		}
		writeJSON(c, http.StatusOK, map[string]any{
			"consent_mode":     "off",
			"action":           "silent",
			"should_prompt":    false,
			"spoiler_free":     true,
			"available_levels": []string{},
		})
		return
	}
	if mode == "on_demand" {
		writeJSON(c, http.StatusOK, map[string]any{
			"consent_mode":     "on_demand",
			"action":           "standby",
			"should_prompt":    false,
			"spoiler_free":     true,
			"available_levels": codingdance.HintLevels,
		})
		return
	}

	stuck := req.StuckSeconds >= 120 || req.AttemptCount >= 3 || req.FailedTests > 0 || strings.TrimSpace(req.ErrorOutput) != ""
	if !stuck {
		writeJSON(c, http.StatusOK, map[string]any{
			"consent_mode":  "proactive_consent",
			"action":        "none",
			"should_prompt": false,
			"spoiler_free":  true,
		})
		return
	}

	if s.adminTelemetry != nil {
		s.adminTelemetry.Companion.ConsentPrompts.Add(1)
	}
	promptText := "发现你可能在当前题目遇到一点卡点，需要我给你一点不剧透的小提醒吗？"
	if lang == langEN {
		promptText = "Looks like you hit a tricky spot — would you like a small, spoiler-free nudge?"
	}
	token := s.signConsentToken(caller.scope, strings.TrimSpace(req.ProblemID), s.now().UTC().Unix())
	writeJSON(c, http.StatusOK, map[string]any{
		"consent_mode":      "proactive_consent",
		"action":            "ask_consent",
		"should_prompt":     true,
		"prompt_text":       promptText,
		"consent_token":     token,
		"recommended_level": "L1",
		"available_levels":  codingdance.HintLevels,
		"spoiler_free":      true,
	})
}

// handleCodingDanceHintReveal discloses an L1..L4 coaching hint ONLY after explicit user consent.
func (s *Server) handleCodingDanceHintReveal(c *gin.Context) {
	caller, ok := s.authenticateCodingDance(c)
	if !ok {
		return
	}
	var req struct {
		ConsentMode  string `json:"consent_mode"`
		UserConsent  bool   `json:"user_consented"`
		ConsentToken string `json:"consent_token"`
		HintLevel    string `json:"hint_level"`
		ProblemID    string `json:"problem_id"`
		ProblemTitle string `json:"problem_title"`
		Language     string `json:"language"`
		Locale       string `json:"locale"`
		Code         string `json:"code"`
		ErrorOutput  string `json:"error_output"`
		Mode         string `json:"mode"` // "fast" (default synchronous analyzer) | "runtime"
		ProjectID    string `json:"project_id"`
	}
	if !readJSON(c, &req) {
		return
	}
	mode, valid := resolveConsentMode(caller, req.ConsentMode)
	if !valid {
		writeMsg(c, http.StatusBadRequest, "请求参数不合法")
		return
	}
	if mode == "off" {
		if s.adminTelemetry != nil {
			s.adminTelemetry.Companion.ConsentBlocked.Add(1)
		}
		writeMsg(c, http.StatusForbidden, "当前处于隐私静默模式（off），已阻止生成提示或分析")
		return
	}
	if !req.UserConsent {
		if s.adminTelemetry != nil {
			s.adminTelemetry.Companion.ConsentBlocked.Add(1)
		}
		writeMsg(c, http.StatusForbidden, "需要用户明确点击同意后才可显示提示或分析")
		return
	}
	if mode == "proactive_consent" && strings.TrimSpace(req.ConsentToken) != "" {
		if !s.verifyConsentToken(caller.scope, strings.TrimSpace(req.ProblemID), req.ConsentToken, s.now().UTC()) {
			writeMsg(c, http.StatusForbidden, "提示令牌无效或已过期，请重新触发检查")
			return
		}
	}
	level := strings.TrimSpace(req.HintLevel)
	if level == "" && caller.connection != nil {
		level, _ = caller.connection.Config["default_hint_level"].(string)
	}
	if level == "" {
		level = "L1"
	}
	if !slices.Contains(codingdance.HintLevels, level) {
		writeMsg(c, http.StatusBadRequest, "提示等级只能是 L1、L2、L3 或 L4")
		return
	}
	lang := companionLang(req.Locale, caller)

	var runtimeTaskID string
	source := "companion_fast"
	if req.Mode == "runtime" && caller.token != "" {
		projID := strings.TrimSpace(req.ProjectID)
		if projID == "" && caller.connection != nil {
			projID = caller.connection.ProjectID
		}
		if projID != "" {
			if tid, err := s.dispatchCompanionTask(c, caller, projID, level, lang, req.ProblemTitle, req.ErrorOutput, req.Code); err == nil {
				runtimeTaskID = tid
				source = "runtime_async"
			}
		}
	}

	if s.adminTelemetry != nil {
		s.adminTelemetry.Companion.HintsRevealed.Add(1)
	}
	title, body, nextSteps := synthesizeLevelHint(level, lang, req.ProblemTitle, req.Language, req.ErrorOutput, req.Code)
	resp := map[string]any{
		"hint_level":       level,
		"source":           source,
		"title":            title,
		"hint":             body,
		"next_steps":       nextSteps,
		"available_levels": codingdance.HintLevels,
	}
	if runtimeTaskID != "" {
		resp["runtime_task_id"] = runtimeTaskID
	}
	writeJSON(c, http.StatusOK, resp)
}

func (s *Server) dispatchCompanionTask(c *gin.Context, caller codingDanceCaller, projectID, level, lang, problemTitle, errorOutput, code string) (string, error) {
	ctx, cancel := ctxTimeout(c.Request, 10*time.Second)
	defer cancel()
	rule := codingdance.LevelPromptRule(level, lang)
	msgText := fmt.Sprintf("%s\nProblem: %s\nError:\n%s\nCode snippet:\n%s", rule, problemTitle, truncateRunes(errorOutput, 1500), truncateRunes(code, 3000))
	var out struct {
		TaskID string `json:"taskId"`
	}
	err := s.api.Call(ctx, caller.token, "CreateTask", map[string]any{
		"tenant":        map[string]any{"tenantId": caller.tenantID, "userId": caller.userID, "workspaceId": projectID},
		"message":       msgText,
		"effectCeiling": "EFFECT_READ",
	}, &out)
	return out.TaskID, err
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

func synthesizeLevelHint(level, lang, title, progLang, errOut, code string) (string, string, []string) {
	errLow := strings.ToLower(errOut)
	hasIndexErr := strings.Contains(errLow, "index") || strings.Contains(errLow, "out of bounds") || strings.Contains(errLow, "out of range")
	hasTLE := strings.Contains(errLow, "timeout") || strings.Contains(errLow, "time limit") || strings.Contains(errLow, "tle")
	hasNull := strings.Contains(errLow, "none") || strings.Contains(errLow, "null") || strings.Contains(errLow, "nil") || strings.Contains(errLow, "undefined")

	if lang == langEN {
		switch level {
		case "L1":
			focus := "check loop invariants and boundary conditions when input length is 0, 1, or maximal."
			if hasTLE {
				focus = "check whether a nested loop can be replaced with prefix sums, two pointers, or a hash index."
			} else if hasIndexErr {
				focus = "trace the highest/lowest index accessed on the final loop iteration."
			}
			return "L1 · Direction Question",
				fmt.Sprintf("For %s (%s): ask yourself — if input size drops to 0/1 or spikes to max, %s", emptyFallback(title, "this problem"), emptyFallback(progLang, "code"), focus),
				[]string{"Trace a 2-element failing input by hand", "Request L2 if you want invariant clues"}
		case "L2":
			clue := "Separate state initialization from transition updates and verify edge bounds explicitly."
			if hasIndexErr {
				clue = "Boundary clue: guard `i + 1` / `len - 1` transitions and empty-container checks before indexing."
			} else if hasNull {
				clue = "Null-safety clue: verify base-case returns before dereferencing child/next nodes."
			} else if hasTLE {
				clue = "Complexity clue: eliminate repeated O(N) scans inside outer loops via precomputation or monotonic state."
			}
			return "L2 · Invariant & Boundary Clues", clue,
				[]string{"Verify failing boundary input", "Escalate to L3 for pseudocode outline"}
		case "L3":
			return "L3 · Structured Skeleton",
				"1. Guard empty / single-element base cases upfront.\n2. Maintain one clear state invariant across loop/recursion steps.\n3. Precompute lookup structure before main pass to stay within linear/log-linear bounds.\n4. Re-check termination condition and return boundary.",
				[]string{"Fill in steps 2-3 in your own style", "Run boundary tests", "Use L4 for full diff walkthrough"}
		default:
			diag := "Review invariant preservation, edge guards, and asymptotic time/space complexity."
			if hasIndexErr {
				diag = "Likely off-by-one slice/index bound on boundary iteration; ensure guard precedes indexing."
			} else if hasTLE {
				diag = "Detected potential super-linear hotspot; refactor inner scan to O(1) lookup or sliding window."
			}
			return "L4 · Full Diagnostic & Optimization Checklist",
				fmt.Sprintf("Root cause hint: %s\n\nCode lines inspected: %d.\nOptimization checklist: normalize edge guards, keep pure helper functions short, and assert n=0 / n=1 / duplicate-value tests.", diag, len(strings.Split(code, "\n"))),
				[]string{"Add unit test reproducing original error", "Apply minimal fix and re-run suite"}
		}
	}

	switch level {
	case "L1":
		focus := "检查当输入为空、长度为 1 或极端边界时，状态初始化与循环条件是否依然成立。"
		if hasTLE {
			focus = "检查外层循环内是否存在可被哈希表、双指针或前缀和降维的重复扫描。"
		} else if hasIndexErr {
			focus = "重点推演最后一次迭代以及为空容器时，下标访问是否可能越界。"
		}
		return "L1 · 方向引导问题",
			fmt.Sprintf("针对「%s」（%s）：先不要看答案，试着自问——%s", emptyFallback(title, "当前题目"), emptyFallback(progLang, "代码"), focus),
			[]string{"手工走一遍长度 0/1/2 的样例", "仍卡住可点选 L2 看边界线索"}
	case "L2":
		clue := "建议把边界检查和主状态转移拆开核对，明确每一步循环不变量。"
		if hasIndexErr {
			clue = "边界线索：在访问 `i+1` / `len-1` 或出栈前，先确认长度校验与非空保护是否覆盖所有分支。"
		} else if hasNull {
			clue = "空值线索：检查递归基/链表尾节点判空是否漏掉前置解引用。"
		} else if hasTLE {
			clue = "复杂度线索：将内层 O(N) 查找或切片拷贝改为预索引结构或原地双指针，避免超时。"
		}
		return "L2 · 关键约束与坑点线索", clue,
			[]string{"对照上述线索检查关键判断行", "需要结构框架可升级至 L3"}
	case "L3":
		return "L3 · 算法步骤骨架",
			"1. 入口处显式拦截空输入 / 极小规模基例；\n2. 定义清晰的状态变量（或预存哈希/单调结构）并保持单调更新；\n3. 循环体只负责状态转移，确保边界下标与终止条件闭合；\n4. 收尾统一返回并核对时间/空间复杂度目标。",
			[]string{"按上述 4 步骨架自行补全代码", "跑通最小与极端测试"}
	default:
		diag := "核对核心不变量维护、边界防卫与渐进复杂度控制。"
		if hasIndexErr {
			diag = "结合报错更像边界下标差 1 或空序列访问越界；把边界条件前移到访问之前可根治。"
		} else if hasTLE {
			diag = "结合超时信号需消解嵌套 O(N) 扫描，改为一次预处理 + O(1) 状态查询。"
		}
		return "L4 · 完整诊断与优化清单",
			fmt.Sprintf("诊断结论：%s\n\n已扫描提交片段行数：%d。\n优化核对清单：\n- 抽取纯边界判断与早期返回，降低嵌套深度；\n- 覆盖空输入 / 极小值 / 重复元素回归用例；\n- 复核时间与空间复杂度后重新提交。", diag, len(strings.Split(code, "\n"))),
			[]string{"先补 1 条复现报错的测试用例", "做最小修复并验证全量用例通过"}
	}
}

func emptyFallback(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

type historySubmission struct {
	ProblemID string   `json:"problem_id"`
	Title     string   `json:"title,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Status    string   `json:"status"` // accepted | wrong_answer | tle | runtime_error | compile_error
	ErrorType string   `json:"error_type,omitempty"`
	RuntimeMS int      `json:"runtime_ms,omitempty"`
	CodeLines int      `json:"code_lines,omitempty"`
}

// handleCodingDanceHistoryAnalyze analyzes past submissions (and optional workspace git log) to
// recommend refactoring patterns, habit insights, and personalized follow-up drills.
func (s *Server) handleCodingDanceHistoryAnalyze(c *gin.Context) {
	caller, ok := s.authenticateCodingDance(c)
	if !ok {
		return
	}
	var req struct {
		ConsentMode string              `json:"consent_mode"`
		UserConsent bool                `json:"user_consented"`
		Locale      string              `json:"locale"`
		Submissions []historySubmission `json:"submissions"`
		ProjectID   string              `json:"project_id"`
		SessionID   string              `json:"session_id"`
	}
	if !readJSON(c, &req) {
		return
	}
	mode, valid := resolveConsentMode(caller, req.ConsentMode)
	if !valid {
		writeMsg(c, http.StatusBadRequest, "请求参数不合法")
		return
	}
	if mode == "off" {
		if s.adminTelemetry != nil {
			s.adminTelemetry.Companion.ConsentBlocked.Add(1)
		}
		writeMsg(c, http.StatusForbidden, "当前处于隐私静默模式（off），已阻止生成提示或分析")
		return
	}
	if !req.UserConsent {
		if s.adminTelemetry != nil {
			s.adminTelemetry.Companion.ConsentBlocked.Add(1)
		}
		writeMsg(c, http.StatusForbidden, "需要用户明确点击同意后才可显示提示或分析")
		return
	}
	lang := companionLang(req.Locale, caller)
	if s.adminTelemetry != nil {
		s.adminTelemetry.Companion.HistoryAnalyses.Add(1)
	}

	total := len(req.Submissions)
	accepted := 0
	runtimeSum := 0
	runtimeCount := 0
	errBreakdown := map[string]int{}
	tagFailures := map[string]int{}
	for _, sub := range req.Submissions {
		if strings.EqualFold(sub.Status, "accepted") {
			accepted++
		} else {
			key := strings.TrimSpace(sub.ErrorType)
			if key == "" {
				key = strings.TrimSpace(sub.Status)
			}
			if key == "" {
				key = "unknown"
			}
			errBreakdown[key]++
			for _, tag := range sub.Tags {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					tagFailures[tag]++
				}
			}
		}
		if sub.RuntimeMS > 0 {
			runtimeSum += sub.RuntimeMS
			runtimeCount++
		}
	}
	passRate := 0.0
	if total > 0 {
		passRate = float64(accepted) / float64(total)
	}
	avgRuntime := 0
	if runtimeCount > 0 {
		avgRuntime = runtimeSum / runtimeCount
	}

	type tagCount struct {
		Tag   string `json:"tag"`
		Count int    `json:"failures"`
	}
	weakTags := make([]tagCount, 0, len(tagFailures))
	for k, v := range tagFailures {
		weakTags = append(weakTags, tagCount{Tag: k, Count: v})
	}
	slices.SortFunc(weakTags, func(a, b tagCount) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Tag, b.Tag)
	})

	// Optional workspace Git history check when bound to caller's own workspace session.
	var recentCommits []CommitInfo
	if caller.tenantID != "" && strings.TrimSpace(req.ProjectID) != "" && strings.TrimSpace(req.SessionID) != "" {
		wsID := req.ProjectID + "--" + req.SessionID
		id := identity{token: caller.token}
		id.TenantID = caller.tenantID
		id.UserID = caller.userID
		ctx, cancel := ctxTimeout(c.Request, 5*time.Second)
		logs, err := s.ws.Log(ctx, id, wsID, 10, "")
		cancel()
		if err == nil {
			if len(logs) > 10 {
				logs = logs[:10]
			}
			recentCommits = logs
		}
	}

	recommendations := make([]string, 0, 3)
	drills := make([]map[string]any, 0, 3)
	if len(weakTags) > 0 {
		topTag := weakTags[0].Tag
		if lang == langEN {
			recommendations = append(recommendations, fmt.Sprintf("Prioritize focused edge-case drills on `%s` (%d recent failures).", topTag, weakTags[0].Count))
			drills = append(drills, map[string]any{"focus_tag": topTag, "goal": "Write 3 boundary test cases before coding solution", "recommended_level": "L1"})
		} else {
			recommendations = append(recommendations, fmt.Sprintf("建议优先针对「%s」（近期失误 %d 次）做小规模边界先行训练。", topTag, weakTags[0].Count))
			drills = append(drills, map[string]any{"focus_tag": topTag, "goal": "编码前先列 3 组边界与极端输入并自测", "recommended_level": "L1"})
		}
	}
	if errBreakdown["tle"] > 0 {
		if lang == langEN {
			recommendations = append(recommendations, "Analyze loop complexity early before implementing nested lookups.")
			drills = append(drills, map[string]any{"focus_tag": "complexity", "goal": "Convert an O(N^2) solution to O(N log N) or O(N)", "recommended_level": "L2"})
		} else {
			recommendations = append(recommendations, "写嵌套循环前先估算 N 的量级，避免 TLE 返工。")
			drills = append(drills, map[string]any{"focus_tag": "complexity", "goal": "完成 1 题将 O(N^2) 重构为双指针或前缀和的练习", "recommended_level": "L2"})
		}
	}
	if len(recommendations) == 0 {
		if lang == langEN {
			recommendations = append(recommendations, "Maintain clean modular helper decomposition and post-edit regression checks.")
		} else {
			recommendations = append(recommendations, "保持提交前小步验证与清晰边界拆分习惯。")
		}
	}

	memoryStatus := "disabled"
	memoryInsights := []MemoryInsight{}
	if s.companionMemory != nil && caller.tenantID != "" && caller.token != "" {
		tagList := make([]string, 0, len(weakTags))
		for _, item := range weakTags {
			tagList = append(tagList, item.Tag)
		}
		memCtx, memCancel := ctxTimeout(c.Request, 4*time.Second)
		recalled, recErr := s.companionMemory.RecallWeakTags(memCtx, caller.tenantID, emptyFallback(caller.userID, "companion"), caller.token, tagList)
		if recErr == nil && recalled != nil {
			memoryInsights = recalled
		}
		saveErr := s.companionMemory.RecordWeakTags(memCtx, caller.tenantID, emptyFallback(caller.userID, "companion"), caller.token, tagFailures, errBreakdown)
		memCancel()
		if recErr != nil && saveErr != nil {
			memoryStatus = "unavailable"
		} else {
			memoryStatus = "synced"
		}
	}

	writeJSON(c, http.StatusOK, map[string]any{
		"summary": map[string]any{
			"total_submissions": total,
			"accepted":          accepted,
			"pass_rate":         passRate,
			"avg_runtime_ms":    avgRuntime,
			"error_breakdown":   errBreakdown,
		},
		"weak_tags":            weakTags,
		"persistent_weak_tags": tagFailures,
		"memory_status":        memoryStatus,
		"memory_insights":      memoryInsights,
		"recent_commits":       recentCommits,
		"recommendations":      recommendations,
		"suggested_drills":     drills,
	})
}
