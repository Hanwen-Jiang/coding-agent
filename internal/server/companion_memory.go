package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/types/known/structpb"

	commonv1 "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/common/v1"
	memoryv1 "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/memory/v1"
	"github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/memory/v1/memoryv1connect"
)

const defaultCodingDanceEmbedVer = "a6api/text-embedding-3-large@2026-10"

// MemoryInsight summarizes a recalled long-term memory entry for companion guidance.
type MemoryInsight struct {
	ID         string  `json:"id,omitempty"`
	Title      string  `json:"title"`
	Guidance   string  `json:"guidance"`
	TaskFamily string  `json:"task_family,omitempty"`
	Score      float64 `json:"score,omitempty"`
}

// CompanionMemory persists and recalls cross-problem weak knowledge tags per tenant/user.
type CompanionMemory interface {
	RecallWeakTags(ctx context.Context, tenantID, userID, bearerToken string, tags []string) ([]MemoryInsight, error)
	RecordWeakTags(ctx context.Context, tenantID, userID, bearerToken string, weakTags map[string]int, errBreakdown map[string]int) error
}

type grpcCompanionMemory struct {
	client memoryv1connect.MemoryServiceClient
}

func newCompanionMemory(rawAddr string) CompanionMemory {
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
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, target)
		}
	}
	return &grpcCompanionMemory{
		client: memoryv1connect.NewMemoryServiceClient(&http.Client{Transport: transport, Timeout: 4 * time.Second}, addr, connect.WithGRPC()),
	}
}

func (m *grpcCompanionMemory) RecallWeakTags(ctx context.Context, tenantID, userID, bearerToken string, tags []string) ([]MemoryInsight, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(bearerToken) == "" {
		return nil, nil
	}
	query := "coding-dance weak tags"
	if len(tags) > 0 {
		query += ": " + strings.Join(tags, ", ")
	}
	req := connect.NewRequest(&memoryv1.SearchRequest{
		Tenant:     &commonv1.TenantContext{TenantId: tenantID, UserId: userID},
		Trigger:    memoryv1.SearchTrigger_SEARCH_TRIGGER_PLANNING,
		Query:      query,
		TaskFamily: "coding_dance",
		TopK:       5,
	})
	req.Header().Set("Authorization", "Bearer "+bearerToken)
	resp, err := m.client.Search(ctx, req)
	if err != nil {
		return nil, err
	}
	insights := make([]MemoryInsight, 0, len(resp.Msg.GetItems()))
	for _, item := range resp.Msg.GetItems() {
		raw, _ := item.GetMemoryItem().GetValue().AsInterface().(map[string]any)
		if raw == nil {
			continue
		}
		content, _ := raw["content"].(map[string]any)
		title, _ := content["title"].(string)
		guidance, _ := content["guidance"].(string)
		if title == "" && guidance == "" {
			continue
		}
		id, _ := raw["id"].(string)
		insights = append(insights, MemoryInsight{
			ID:       id,
			Title:    title,
			Guidance: guidance,
			Score:    item.GetScore(),
		})
	}
	return insights, nil
}

func (m *grpcCompanionMemory) RecordWeakTags(ctx context.Context, tenantID, userID, bearerToken string, weakTags map[string]int, errBreakdown map[string]int) error {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(bearerToken) == "" || len(weakTags) == 0 {
		return nil
	}
	tags := make([]string, 0, len(weakTags))
	for k := range weakTags {
		tags = append(tags, k)
	}
	slices.Sort(tags)
	taskID := crockfordID("tsk_")
	candidates := make([]*memoryv1.MemoryCandidate, 0, min(len(tags), 5))
	today := time.Now().UTC().Format("2006-01-02")
	for _, tag := range tags {
		if len(candidates) >= 5 {
			break
		}
		count := weakTags[tag]
		guidance := fmt.Sprintf("Practice targeted boundary checks for `%s` (%d historical failures).", tag, count)
		if len(errBreakdown) > 0 {
			parts := make([]string, 0, len(errBreakdown))
			for k, v := range errBreakdown {
				parts = append(parts, k+":"+strconv.Itoa(v))
			}
			slices.Sort(parts)
			guidance += " Error signals: " + strings.Join(parts, ", ")
		}
		itemDoc := map[string]any{
			"id":    crockfordID("mem_"),
			"scope": map[string]any{"tenant": tenantID, "user": emptyFallback(userID, "companion")},
			"kind":  "failure",
			"applies_when": map[string]any{
				"task_family": []any{"coding_dance"},
				"tools":       []any{sanitizeToolTag(tag)},
			},
			"content": map[string]any{
				"title":     fmt.Sprintf("coding-dance weak topic: %s", tag),
				"guidance":  guidance,
				"rationale": "Tracked automatically via coding-dance history-analyze companion.",
			},
			"provenance":        map[string]any{"source": "agent_reflection"},
			"trust":             0.75,
			"validity":          map[string]any{"valid_from": today},
			"sensitivity":       "none",
			"embedding_version": defaultCodingDanceEmbedVer,
			"version":           1,
		}
		val, err := structpb.NewValue(itemDoc)
		if err != nil {
			continue
		}
		candidates = append(candidates, &memoryv1.MemoryCandidate{
			MemoryItem:   &commonv1.JsonPayload{SchemaId: "memory_item_v1", Value: val},
			SourceTaskId: taskID,
		})
	}
	if len(candidates) == 0 {
		return nil
	}
	req := connect.NewRequest(&memoryv1.SubmitCandidatesRequest{
		Tenant:     &commonv1.TenantContext{TenantId: tenantID, UserId: userID},
		Candidates: candidates,
	})
	req.Header().Set("Authorization", "Bearer "+bearerToken)
	_, err := m.client.SubmitCandidates(ctx, req)
	return err
}

func crockfordID(prefix string) string {
	const alpha = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	b := make([]byte, 25)
	_, _ = rand.Read(b)
	out := make([]byte, 26)
	out[0] = '0'
	for i := 0; i < 25; i++ {
		out[i+1] = alpha[int(b[i])&31]
	}
	return prefix + string(out)
}

func sanitizeToolTag(tag string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(tag)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else if r == '-' || r == ' ' {
			b.WriteRune('_')
		}
	}
	s := b.String()
	if s == "" {
		return "coding_dance"
	}
	if s[0] < 'a' || s[0] > 'z' {
		s = "tag_" + s
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
