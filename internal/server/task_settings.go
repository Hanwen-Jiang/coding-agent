package server

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"path/filepath"
)

// Saved choices are per user and session, never a reusable Runtime grant.
// Each submitted Task still receives a fresh NetworkRequest for Runtime validation.
type sessionTaskSettings struct {
	Sandbox string              `json:"sandbox"`
	Network *taskNetworkRequest `json:"network_request"`
}

func defaultTaskSettings() *sessionTaskSettings { return &sessionTaskSettings{Sandbox: defaultSandbox} }
func (v *sessionTaskSettings) validate() error {
	if v == nil {
		return nil
	}
	sandbox, ok := pickOption(v.Sandbox, sandboxLevels, defaultSandbox)
	if !ok {
		return msg("沙箱等级只能是 read 或 write_reversible")
	}
	v.Sandbox = sandbox
	if v.Network != nil {
		return v.Network.validate()
	}
	return nil
}
func cloneTaskSettings(v *sessionTaskSettings) *sessionTaskSettings {
	if v == nil {
		return defaultTaskSettings()
	}
	raw, _ := json.Marshal(v)
	var copy sessionTaskSettings
	_ = json.Unmarshal(raw, &copy)
	return &copy
}
func (s *Store) taskSettingsFile(tenant, user, workspace string) string {
	return filepath.Join(s.dir, tenant, "personal-sessions", workspace, workspaceHash(user)+".settings.json")
}
func (s *Store) TaskSettings(tenant, user, workspace string) (*sessionTaskSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := readJSONFile[*sessionTaskSettings](s.taskSettingsFile(tenant, user, workspace))
	if err != nil {
		return nil, err
	}
	if value == nil {
		return defaultTaskSettings(), nil
	}
	if err = value.validate(); err != nil {
		return nil, err
	}
	return value, nil
}
func (s *Store) PutTaskSettings(tenant, user, workspace string, value *sessionTaskSettings) error {
	value = cloneTaskSettings(value)
	if err := value.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFileAtomic(s.taskSettingsFile(tenant, user, workspace), value)
}
func (s *Server) handleSessionTaskSettings(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	if c.Request.Method == http.MethodGet {
		writeJSON(c, 200, cloneTaskSettings(sess.TaskSettings))
		return
	}
	var settings sessionTaskSettings
	if !readJSON(c, &settings) {
		return
	}
	if err := settings.validate(); err != nil {
		writeErr(c, 400, err)
		return
	}
	if err := s.store.PutTaskSettings(id.TenantID, id.UserID, sess.WorkspaceID, &settings); err != nil {
		writeErr(c, 500, err)
		return
	}
	writeJSON(c, 200, &settings)
}
