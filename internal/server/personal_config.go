package server

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"regexp"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/personalconfig"
	pb "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/personalconfig/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

const personalConfigService = "/agentruntime.personalconfig.v1.PersonalConfigService/"

var personalVersion = regexp.MustCompile(`^user/v[1-9][0-9]*$`)

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

func (s *Server) handleGetPersonalConfig(c *gin.Context, id identity) {
	c.Header("Cache-Control", "no-store")
	version := c.Query("version")
	if version != "" && !personalVersion.MatchString(version) {
		writeMsg(c, http.StatusBadRequest, "个人配置格式不合法")
		return
	}
	var response json.RawMessage
	err := s.callPersonalConfig(c.Request, id, "GetPersonalConfig", map[string]any{"tenantId": id.TenantID, "version": version}, &response)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if version == "" && apiErr.Reason == "personal_config_not_found" {
			writeJSON(c, http.StatusOK, map[string]any{"config": nil, "available": true})
			return
		}
		if apiErr.Code == "unimplemented" || (apiErr.Status == http.StatusNotFound && apiErr.Reason == "") {
			writeJSON(c, http.StatusOK, map[string]any{"config": nil, "available": false})
			return
		}
	}
	if err != nil {
		writeAPIError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, response)
}

func (s *Server) handlePutPersonalConfig(c *gin.Context, id identity) {
	c.Header("Cache-Control", "no-store")
	var req struct {
		Document        json.RawMessage `json:"document"`
		ExpectedVersion string          `json:"expected_version"`
	}
	if !readJSON(c, &req) {
		return
	}
	if req.ExpectedVersion != "" && !personalVersion.MatchString(req.ExpectedVersion) {
		writeMsg(c, http.StatusBadRequest, "个人配置格式不合法")
		return
	}
	doc := &pb.PersonalConfigDocument{}
	if err := protojson.Unmarshal(req.Document, doc); err != nil {
		writeMsg(c, http.StatusBadRequest, "个人配置格式不合法")
		return
	}
	canonical, err := personalconfig.Canonical(doc)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	if envID := doc.GetDefaults().GetEnvironmentId(); envID != "" {
		_, ok, err := s.environmentForUser(id.TenantID, id.UserID, envID)
		if err != nil {
			writeErr(c, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeMsg(c, http.StatusBadRequest, "环境不存在或不属于当前用户")
			return
		}
	}
	var response json.RawMessage
	err = s.callPersonalConfig(c.Request, id, "PutPersonalConfig", map[string]any{"tenantId": id.TenantID, "document": json.RawMessage(canonical), "expectedVersion": req.ExpectedVersion}, &response)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, response)
}

func (s *Server) handleRetirePersonalConfig(c *gin.Context, id identity) {
	c.Header("Cache-Control", "no-store")
	var response json.RawMessage
	if err := s.callPersonalConfig(c.Request, id, "RetirePersonalConfig", map[string]any{"tenantId": id.TenantID}, &response); err != nil {
		writeAPIError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, response)
}
