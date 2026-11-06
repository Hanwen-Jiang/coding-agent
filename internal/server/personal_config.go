package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/personalconfig"
	pb "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/personalconfig/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

const personalConfigService = "/agentruntime.personalconfig.v1.PersonalConfigService/"

// Nil slices inherit the package defaults; explicit empty slices disable the
// corresponding optional tools for this session. Runtime still checks all ACLs.
type personalSelection struct {
	Skills *[]string `json:"skill_names,omitempty"`
	MCP    *[]string `json:"mcp_connection_ids,omitempty"`
}

func (p *personalSelection) validate() error {
	if p == nil {
		return nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var selection map[string]any
	if err = json.Unmarshal(raw, &selection); err != nil {
		return err
	}
	input, err := structpb.NewStruct(map[string]any{"personal_config": selection})
	if err != nil {
		return err
	}
	_, err = personalconfig.Select(nil, input)
	return err
}

func (s *Server) callPersonalConfig(r *http.Request, id identity, method string, req, resp any) error {
	ctx, cancel := ctxTimeout(r, 15*time.Second)
	defer cancel()
	return s.api.call(ctx, s.api.client, personalConfigService+method, id.token, req, resp)
}

func (s *Server) handleGetPersonalConfig(w http.ResponseWriter, r *http.Request, id identity) {
	var response json.RawMessage
	err := s.callPersonalConfig(r, id, "GetPersonalConfig", map[string]any{"tenantId": id.TenantID}, &response)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Reason == "personal_config_not_found" || apiErr.Code == "not_found" {
			writeJSON(w, http.StatusOK, map[string]any{"config": nil, "available": true})
			return
		}
		if apiErr.Code == "unimplemented" {
			writeJSON(w, http.StatusOK, map[string]any{"config": nil, "available": false})
			return
		}
	}
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handlePutPersonalConfig(w http.ResponseWriter, r *http.Request, id identity) {
	var req struct {
		Document        json.RawMessage `json:"document"`
		ExpectedVersion string          `json:"expected_version"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	doc := &pb.PersonalConfigDocument{}
	if err := protojson.Unmarshal(req.Document, doc); err != nil {
		writeMsg(w, r, http.StatusBadRequest, "个人配置格式不合法")
		return
	}
	canonical, err := personalconfig.Canonical(doc)
	if err != nil {
		writeErr(w, r, http.StatusBadRequest, err)
		return
	}
	if envID := doc.GetDefaults().GetEnvironmentId(); envID != "" {
		_, ok, err := s.environmentForUser(id.TenantID, id.UserID, envID)
		if err != nil {
			writeErr(w, r, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeMsg(w, r, http.StatusBadRequest, "环境不存在或不属于当前用户")
			return
		}
	}
	var response json.RawMessage
	err = s.callPersonalConfig(r, id, "PutPersonalConfig", map[string]any{"tenantId": id.TenantID, "document": json.RawMessage(canonical), "expectedVersion": req.ExpectedVersion}, &response)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleRetirePersonalConfig(w http.ResponseWriter, r *http.Request, id identity) {
	var response json.RawMessage
	if err := s.callPersonalConfig(r, id, "RetirePersonalConfig", map[string]any{"tenantId": id.TenantID}, &response); err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
