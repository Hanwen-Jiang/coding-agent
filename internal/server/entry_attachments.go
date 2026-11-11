package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/agentruntime/coding-agent/internal/entry"
	entryemail "github.com/agentruntime/coding-agent/internal/entry/email"
)

// uploadEntryAttachments keeps automated email uploads on exactly the same
// actor-scoped Runtime File API as browser uploads. The deterministic name plus
// bytes let Runtime's owner/workspace/name/hash deduplication recover a lost
// response without creating another source; no local file registry is authority.
func (s *Server) uploadEntryAttachments(ctx context.Context, id identity, c entry.Connection, t entry.Trigger, workspaceID string) ([]string, error) {
	if t.Kind != "email" {
		return nil, nil
	}
	if id.TenantID != c.Tenant || id.UserID != c.User || t.Tenant != c.Tenant || t.User != c.User || t.ConnectionID != c.ID || t.ProjectID != c.ProjectID || workspaceID == "" || workspaceID != t.WorkspaceID {
		return nil, errors.New("entry attachment scope mismatch")
	}
	e, err := entryemail.DecodeEnvelope(t.Payload)
	if err != nil {
		return nil, err
	}
	if len(e.Attachments) == 0 {
		return nil, nil
	}
	release, err := acquireSourceTransfer(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	refs := make([]string, 0, len(e.Attachments))
	key := sha256.Sum256([]byte(t.ID))
	for i, a := range e.Attachments {
		name := sourceName(a.Name)
		if name == "" {
			return nil, errors.New("invalid entry attachment name")
		}
		prefix := fmt.Sprintf("mail-%x-%d-", key[:8], i+1)
		for len(prefix)+len(name) > 255 {
			_, size := utf8.DecodeLastRuneInString(name)
			name = name[:len(name)-size]
		}
		name = prefix + name
		digest := sha256.Sum256(a.Data)
		size := int64(len(a.Data))
		media := http.DetectContentType(a.Data)
		source, err := s.api.uploadSource(ctx, id.token, workspaceID, name, media, size, bytes.NewReader(a.Data))
		if err != nil {
			return nil, err
		}
		if !sourceIDPattern.MatchString(source.ID) || source.FileID != source.ID || !sourceVersionPattern.MatchString(source.VersionID) || source.Name != name || source.SizeBytes != size || source.SHA256 != hex.EncodeToString(digest[:]) {
			return nil, errors.New("entry attachment integrity mismatch")
		}
		expectedURI := "obj://" + id.TenantID + "/uploads/" + source.ID + "/" + name
		if source.ObjectURI != expectedURI && !strings.HasPrefix(source.ObjectURI, "obj://"+id.TenantID+"/file-derived/fjob_") {
			return nil, errors.New("entry attachment identity mismatch")
		}
		// Pin the immutable version and recheck the returned file's workspace and
		// actor access. We never turn an unverified URI from the webhook into a ref.
		resolved, err := s.resolveSources(ctx, id, workspaceID, []string{source.ID}, map[string]string{source.ID: source.VersionID})
		if err != nil {
			return nil, err
		}
		if len(resolved) != 1 || resolved[0].SHA256 != source.SHA256 || resolved[0].SizeBytes != size || resolved[0].Name != name {
			return nil, errors.New("entry attachment version mismatch")
		}
		refs = append(refs, resolved[0].ObjectURI)
	}
	return refs, nil
}
