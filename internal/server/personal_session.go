package server

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// A session is tenant-shared, but optional personal tool references belong to
// the submitting user. They never live in the shared sessions.json projection.
func (s *Store) personalSessionFile(tenant, user, workspace string) string {
	return filepath.Join(s.dir, tenant, "personal-sessions", workspace, workspaceHash(user)+".json")
}
func (s *Store) PersonalSession(tenant, user, workspace string) (*personalSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSONFile[*personalSelection](s.personalSessionFile(tenant, user, workspace))
}
func (s *Store) PutPersonalSession(tenant, user, workspace string, selection *personalSelection) error {
	if err := selection.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFileAtomic(s.personalSessionFile(tenant, user, workspace), selection)
}
func (s *Server) personalSession(id identity, sess Session) (Session, error) {
	var err error
	sess.PersonalConfig, err = s.store.PersonalSession(id.TenantID, id.UserID, sess.WorkspaceID)
	return sess, err
}

// Read older unreleased records without exposing their ambiguous owner. Those
// records did not identify who last selected the tools, so migrating them to
// CreatedBy would potentially transfer another user's private references.
func (s *Store) readSessions(tenant string) ([]Session, error) {
	sessions, err := readJSONFile[[]Session](s.sessionsFile(tenant))
	if err != nil {
		return nil, err
	}
	dirty := false
	for i := range sessions {
		if sessions[i].PersonalConfig != nil {
			sessions[i].PersonalConfig = nil
			dirty = true
		}
	}
	if dirty {
		err = writeFileAtomic(s.sessionsFile(tenant), sessions)
	}
	return sessions, err
}
func (s *Store) removePersonalSessions(tenant, workspace string) error {
	return os.RemoveAll(filepath.Join(s.dir, tenant, "personal-sessions", workspace))
}

// Private projections are response-only; even an update callback must not put
// them back into tenant-shared storage.
func sessionWithoutPersonal(sess Session) Session { sess.PersonalConfig = nil; return sess }

// Clone before caching the private response in the owner's HTTP operation.
func clonePersonalSelection(selection *personalSelection) *personalSelection {
	if selection == nil {
		return nil
	}
	raw, _ := json.Marshal(selection)
	var out personalSelection
	_ = json.Unmarshal(raw, &out)
	return &out
}
