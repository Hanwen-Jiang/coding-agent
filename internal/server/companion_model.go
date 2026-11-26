package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	commonv1 "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/common/v1"
	modelgatewayv1 "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/modelgateway/v1"
	"github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/modelgateway/v1/modelgatewayv1connect"
	"github.com/agentruntime/coding-agent/internal/entry/codingdance"
)

const defaultCompanionModelTier = "mid@low"

// CompanionChatTurn represents a single prior conversation turn passed from coding-dance.
type CompanionChatTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompanionProblemHint represents a single editorial hint item from problem context.
type CompanionProblemHint struct {
	T    string `json:"t"`
	Body string `json:"body"`
}

// CompanionProblemContext holds structured problem metadata forwarded by coding-dance.
type CompanionProblemContext struct {
	Topic  string                 `json:"topic,omitempty"`
	Level  string                 `json:"level,omitempty"`
	Lede   string                 `json:"lede,omitempty"`
	Hints  []CompanionProblemHint `json:"hints,omitempty"`
	Points []CompanionProblemHint `json:"points,omitempty"`
	AcmIO  string                 `json:"acm_io,omitempty"`
}

// CompanionFailedCase holds the failing test case details forwarded by coding-dance.
type CompanionFailedCase struct {
	Label    string `json:"label,omitempty"`
	Input    string `json:"input,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Error    string `json:"error,omitempty"`
}

// CompanionCoachInput carries coding-dance context for synchronous LLM companion coaching.
type CompanionCoachInput struct {
	TenantID       string                   `json:"-"`
	UserID         string                   `json:"-"`
	Locale         string                   `json:"-"`
	Intent         string                   `json:"intent,omitempty"`
	HintLevel      string                   `json:"hint_level,omitempty"`
	PracticeMode   string                   `json:"practice_mode,omitempty"`
	ProblemID      string                   `json:"problem_id,omitempty"`
	ProblemTitle   string                   `json:"problem_title,omitempty"`
	Language       string                   `json:"language,omitempty"`
	Code           string                   `json:"code,omitempty"`
	ErrorOutput    string                   `json:"error_output,omitempty"`
	Question       string                   `json:"question,omitempty"`
	History        []CompanionChatTurn      `json:"history,omitempty"`
	ProblemContext *CompanionProblemContext `json:"problem_context,omitempty"`
	FailedCase     *CompanionFailedCase     `json:"failed_case,omitempty"`
}

// CompanionCoachOutput matches the response schema expected by coding-dance companion UI.
type CompanionCoachOutput struct {
	HintLevel       string   `json:"hint_level"`
	Source          string   `json:"source"`
	Engine          string   `json:"engine"`
	Model           string   `json:"model,omitempty"`
	Title           string   `json:"title"`
	Hint            string   `json:"hint"`
	NextSteps       []string `json:"next_steps"`
	QuickReplies    []string `json:"quick_replies,omitempty"`
	AvailableLevels []string `json:"available_levels"`
}

// CompanionModel invokes agent-runtime ModelGatewayService for live companion coaching.
type CompanionModel interface {
	Coach(ctx context.Context, in CompanionCoachInput) (CompanionCoachOutput, error)
}

type grpcCompanionModel struct {
	client modelgatewayv1connect.ModelGatewayServiceClient
	tier   string
}

func newCompanionModel(rawAddr, tier string) CompanionModel {
	addr := strings.TrimSpace(rawAddr)
	if addr == "" {
		return nil
	}
	if strings.HasPrefix(addr, "grpc://") {
		addr = "http://" + strings.TrimPrefix(addr, "grpc://")
	} else if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	addr = strings.TrimRight(addr, "/")
	transport := &http2.Transport{}
	if strings.HasPrefix(addr, "http://") {
		transport.AllowHTTP = true
		transport.DialTLSContext = func(ctx context.Context, network, target string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, target)
		}
	}
	chosenTier := strings.TrimSpace(tier)
	if chosenTier == "" {
		chosenTier = defaultCompanionModelTier
	}
	return &grpcCompanionModel{
		client: modelgatewayv1connect.NewModelGatewayServiceClient(
			&http.Client{Transport: transport, Timeout: 30 * time.Second},
			addr,
			connect.WithGRPC(),
		),
		tier: chosenTier,
	}
}

func (m *grpcCompanionModel) Coach(ctx context.Context, in CompanionCoachInput) (CompanionCoachOutput, error) {
	tenant := strings.TrimSpace(in.TenantID)
	if tenant == "" {
		tenant = "coding-dance-guest"
	}
	user := strings.TrimSpace(in.UserID)
	if user == "" {
		user = "companion"
	}
	level := strings.TrimSpace(in.HintLevel)
	if level == "" {
		level = "L1"
	}
	intent := strings.TrimSpace(in.Intent)
	if intent == "" {
		intent = "hint"
	}

	messages := make([]*modelgatewayv1.ChatMessage, 0, 8)
	messages = append(messages, &modelgatewayv1.ChatMessage{
		Role: modelgatewayv1.ChatRole_CHAT_ROLE_SYSTEM,
		Text: buildCompanionSystemPrompt(in, level),
	})

	history := in.History
	if len(history) > 6 {
		history = history[len(history)-6:]
	}
	for _, turn := range history {
		content := strings.TrimSpace(turn.Content)
		if content == "" {
			continue
		}
		role := modelgatewayv1.ChatRole_CHAT_ROLE_USER
		if strings.EqualFold(strings.TrimSpace(turn.Role), "assistant") {
			role = modelgatewayv1.ChatRole_CHAT_ROLE_ASSISTANT
		}
		messages = append(messages, &modelgatewayv1.ChatMessage{
			Role: role,
			Text: truncateRunes(content, 1200),
		})
	}

	messages = append(messages, &modelgatewayv1.ChatMessage{
		Role: modelgatewayv1.ChatRole_CHAT_ROLE_USER,
		Text: buildCompanionUserPrompt(in, level, intent),
	})

	req := connect.NewRequest(&modelgatewayv1.GenerateRequest{
		Tenant: &commonv1.TenantContext{
			TenantId: tenant,
			UserId:   user,
		},
		Call: &modelgatewayv1.CallContext{
			TaskId:           "cd-" + companionHex(8),
			NodeId:           "companion-" + intent,
			Role:             commonv1.Role_ROLE_DELIVER,
			TaskFamily:       "coding_dance_companion",
			Tier:             m.tier,
			Reasoning:        commonv1.Reasoning_REASONING_DIRECT,
			MaxCostUsdMicros: 50_000,
		},
		Generation: &modelgatewayv1.GenerateParams{
			Messages:        messages,
			MaxOutputTokens: 900,
		},
	})

	resp, err := m.client.Generate(ctx, req)
	if err != nil {
		return CompanionCoachOutput{}, err
	}

	rawText := strings.TrimSpace(resp.Msg.GetText())
	if rawText == "" {
		return CompanionCoachOutput{}, fmt.Errorf("model gateway returned empty response")
	}
	modelID := strings.TrimSpace(resp.Msg.GetModel().GetModelProfileId())
	return parseCompanionModelOutput(rawText, level, in.Locale, modelID), nil
}

func buildCompanionSystemPrompt(in CompanionCoachInput, level string) string {
	practiceMode := "LeetCode 核心代码模式（用户只需编写核心函数/类方法）"
	if strings.EqualFold(strings.TrimSpace(in.PracticeMode), "acm") {
		practiceMode = "ACM 完整 I/O 模式（用户需要自行从标准输入 stdin 读取数据，并按严格格式输出到 stdout）"
	}
	levelRule := companionLevelDirective(level, in.Locale)
	if in.Locale == langEN {
		return strings.Join([]string{
			"You are XiaoWu (小舞), a lively, sharp, encouraging algorithm tutor on Coding Dance.",
			"Current mode: " + practiceMode + ". Language: " + emptyFallback(in.Language, "python") + ". Target coaching level: " + level + ".",
			"Strict rules: " + levelRule,
			"Never repeat generic boilerplate; refer directly to the user's code variables, loop bounds, failing case, or ACM I/O format.",
			"Respond ONLY with a valid JSON object (no surrounding markdown prose): {\"title\": string, \"hint\": string, \"next_steps\": string[], \"quick_replies\": string[]}.",
			"Keep `hint` concise (<= 180 words), clear, and energetic. Provide 1-2 short `next_steps` and 2-3 short follow-up `quick_replies`.",
		}, "\n")
	}
	return strings.Join([]string{
		"你是算法刷题平台 Coding Dance 的专属 AI 伴学助教「小舞」💃✨，性格活泼、敏锐、一针见血。",
		"当前练习模式：" + practiceMode + "，编程语言：" + emptyFallback(in.Language, "python") + "，提示档位：" + level + "。",
		"严格档位要求：" + levelRule,
		"回答准则：",
		"1. 直接结合用户的代码变量名、循环边界、当前报错、失败用例与题面要求给出具体排查或引导，拒绝放之四海而皆准的空洞套话；",
		"2. 若用户在问 ACM 模式读写格式或运行报错，结合本题输入输出说明具体点拨；",
		"3. 语气活泼亲切但不啰嗦，hint 正文控制在 220 字以内，善用加粗和行内代码；",
		"4. 只输出一个合法 JSON 对象（不要额外解释）：{\"title\": string, \"hint\": string, \"next_steps\": string[], \"quick_replies\": string[]}（next_steps 给 1~2 条下一步动作，quick_replies 给 2~3 条简短追问建议）。",
	}, "\n")
}

func companionLevelDirective(level, locale string) string {
	if locale == langEN {
		switch level {
		case "L1":
			return "Ask 1-2 targeted guiding questions pointing at direction/intuition ONLY. Never spoil the algorithm name, state transition, or code."
		case "L2":
			return "Reveal the key invariant, data structure clue, or boundary trap without giving pseudocode or complete code."
		case "L3":
			return "Provide a clear 3-4 step algorithmic skeleton and edge-case checklist without dumping the complete copy-pasteable solution."
		default:
			return "Perform direct root-cause diagnosis referencing lines/variables in the user's code and explain how to fix bugs and optimize complexity."
		}
	}
	switch level {
	case "L1":
		return "只提 1~2 个精准的引导性问题或直觉方向，绝对不剧透具体数据结构名称、状态转移公式或代码实现。"
	case "L2":
		return "点出关键切入线索、循环不变量或易错边界陷阱，不给伪代码与完整实现代码。"
	case "L3":
		return "梳理清晰的 3~4 步解题骨架与边界清单，保留最后动手的思考空间，不直接贴出完整 AC 代码。"
	default:
		return "结合用户代码的具体行/变量、报错与失败用例直接做根因诊断，说明修复思路与时空复杂度优化建议。"
	}
}

func buildCompanionUserPrompt(in CompanionCoachInput, level, intent string) string {
	parts := make([]string, 0, 8)
	title := emptyFallback(in.ProblemTitle, emptyFallback(in.ProblemID, "当前算法题"))
	header := fmt.Sprintf("题目：%s", title)
	if in.ProblemContext != nil && (strings.TrimSpace(in.ProblemContext.Topic) != "" || strings.TrimSpace(in.ProblemContext.Level) != "") {
		header += fmt.Sprintf("（题型：%s / 难度：%s）", strings.TrimSpace(in.ProblemContext.Topic), strings.TrimSpace(in.ProblemContext.Level))
	}
	parts = append(parts, header)

	if in.ProblemContext != nil {
		if lede := strings.TrimSpace(in.ProblemContext.Lede); lede != "" {
			parts = append(parts, "核心思路背景："+truncateRunes(lede, 500))
		}
		if len(in.ProblemContext.Hints) > 0 {
			hintLines := make([]string, 0, len(in.ProblemContext.Hints))
			for i, h := range in.ProblemContext.Hints {
				if i >= 3 {
					break
				}
				hintLines = append(hintLines, strings.TrimSpace(h.T)+"："+truncateRunes(strings.TrimSpace(h.Body), 240))
			}
			parts = append(parts, "题解分段线索：\n"+strings.Join(hintLines, "\n"))
		}
		if len(in.ProblemContext.Points) > 0 {
			pts := make([]string, 0, len(in.ProblemContext.Points))
			for i, p := range in.ProblemContext.Points {
				if i >= 3 {
					break
				}
				pts = append(pts, strings.TrimSpace(p.T)+"："+truncateRunes(strings.TrimSpace(p.Body), 180))
			}
			parts = append(parts, "常见注意事项：\n"+strings.Join(pts, "\n"))
		}
		if acmIO := strings.TrimSpace(in.ProblemContext.AcmIO); acmIO != "" {
			parts = append(parts, "ACM 输入输出格式说明："+truncateRunes(acmIO, 400))
		}
	}

	if code := strings.TrimSpace(in.Code); code != "" {
		parts = append(parts, fmt.Sprintf("用户当前代码（%s）：\n```%s\n%s\n```", emptyFallback(in.Language, "code"), in.Language, truncateRunes(code, 3000)))
	}
	if errOut := strings.TrimSpace(in.ErrorOutput); errOut != "" {
		parts = append(parts, "运行报错：\n"+truncateRunes(errOut, 1200))
	}
	if in.FailedCase != nil && (strings.TrimSpace(in.FailedCase.Input) != "" || strings.TrimSpace(in.FailedCase.Error) != "") {
		parts = append(parts, fmt.Sprintf(
			"失败用例（%s）：输入=%s | 预期=%s | 实际=%s | 错误=%s",
			emptyFallback(in.FailedCase.Label, "用例"),
			truncateRunes(in.FailedCase.Input, 300),
			truncateRunes(in.FailedCase.Expected, 300),
			truncateRunes(in.FailedCase.Actual, 300),
			truncateRunes(in.FailedCase.Error, 300),
		))
	}
	if q := strings.TrimSpace(in.Question); q != "" {
		parts = append(parts, "用户提问："+truncateRunes(q, 1000))
	} else {
		parts = append(parts, fmt.Sprintf("用户请求 %s 提示（意图：%s）。", level, intent))
	}
	return strings.Join(parts, "\n\n")
}

func parseCompanionModelOutput(rawText, level, locale, modelID string) CompanionCoachOutput {
	defaultTitle := level + " · 小舞点拨"
	if locale == langEN {
		defaultTitle = level + " · XiaoWu Hint"
	}
	defaultSteps := []string{"对照提示自检一次边界输入与返回值", "需要进一步排查可继续追问小舞"}
	if locale == langEN {
		defaultSteps = []string{"Check edge cases and loop bounds", "Ask a follow-up question if still stuck"}
	}

	cleaned := strings.TrimSpace(rawText)
	if start := strings.IndexByte(cleaned, '{'); start >= 0 {
		if end := strings.LastIndexByte(cleaned, '}'); end > start {
			candidate := cleaned[start : end+1]
			var parsed struct {
				Title        string   `json:"title"`
				Hint         string   `json:"hint"`
				NextSteps    []string `json:"next_steps"`
				QuickReplies []string `json:"quick_replies"`
			}
			if json.Unmarshal([]byte(candidate), &parsed) == nil && strings.TrimSpace(parsed.Hint) != "" {
				steps := cleanStringList(parsed.NextSteps, 3)
				if len(steps) == 0 {
					steps = defaultSteps
				}
				return CompanionCoachOutput{
					HintLevel:       level,
					Source:          "agent_runtime_gateway",
					Engine:          "llm",
					Model:           modelID,
					Title:           emptyFallback(strings.TrimSpace(parsed.Title), defaultTitle),
					Hint:            strings.TrimSpace(parsed.Hint),
					NextSteps:       steps,
					QuickReplies:    cleanStringList(parsed.QuickReplies, 3),
					AvailableLevels: codingdance.HintLevels,
				}
			}
		}
	}

	return CompanionCoachOutput{
		HintLevel:       level,
		Source:          "agent_runtime_gateway",
		Engine:          "llm",
		Model:           modelID,
		Title:           defaultTitle,
		Hint:            cleaned,
		NextSteps:       defaultSteps,
		AvailableLevels: codingdance.HintLevels,
	}
}

func cleanStringList(items []string, limit int) []string {
	out := make([]string, 0, min(len(items), limit))
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func companionHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
