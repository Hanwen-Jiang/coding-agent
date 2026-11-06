package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Each long operation is started once with a stable request ID and polled through
// short requests. Retrying a lost start response reuses the same ID and inputs.
func (a *TaskAPI) workspaceOperation(ctx context.Context, token, method string, req map[string]any, out any) error {
	operation := map[string]string{"CreateWorkspace": "create", "DeleteWorkspace": "delete", "CommitWorkspace": "commit", "PushWorkspace": "push", "SyncWorkspace": "sync", "DiscardChanges": "discard", "AttachRepo": "attachRepo", "DetachRepo": "detachRepo"}[method]
	if operation == "" {
		return fmt.Errorf("unsupported workspace operation %q", method)
	}
	key := randomHex(16)
	requestID := key[:8] + "-" + key[8:12] + "-4" + key[13:16] + "-a" + key[17:20] + "-" + key[20:]
	j := workspaceJournal(ctx)
	if j != nil {
		// Credentials may be freshly issued after web restart; identity and
		// semantic input, rather than a short-lived password, define this step.
		semantic := make(map[string]any, len(req))
		for k, v := range req {
			if k != "credential" {
				semantic[k] = v
			}
		}
		raw, e := json.Marshal(semantic)
		if e != nil {
			return e
		}
		requestID = "cw-" + workspaceHash(j.record.ID, method, string(raw))
	}
	start := map[string]any{"requestId": requestID, operation: req}
	var started struct {
		OperationID string `json:"operationId"`
	}
	var err error
	if j != nil {
		started.OperationID = "wso-" + workspaceHash(j.tenant, j.record.User, requestID)
		step := map[string]any{"request_id": requestID, "operation_id": started.OperationID, "workspace_id": req["workspaceId"], "method": method}
		if err := j.put("runtime:"+requestID, step); err != nil {
			panic(workspaceHTTPAbort{err})
		}
		// Query before Start: a response can be lost after durable acceptance.
		// This also avoids conflicting with credentials renewed after a restart.
		var existing workspaceOperationResult
		for {
			err = a.call(ctx, a.client, "/agentruntime.workspace.v1.WorkspaceService/GetWorkspaceOperation", token, map[string]any{"workspaceId": req["workspaceId"], "operationId": started.OperationID}, &existing)
			if !retryWorkspace(err) || ctx.Err() != nil {
				break
			}
			if e := waitWorkspace(ctx, 500*time.Millisecond); e != nil {
				return e
			}
		}
		if err == nil {
			if done, e := existing.decode(out); done {
				return e
			}
		} else {
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.Status != http.StatusNotFound {
				return err
			}
			started.OperationID = ""
		}
	}
	if started.OperationID == "" {
		for attempt := 0; attempt < 3; attempt++ {
			err = a.call(ctx, a.client, "/agentruntime.workspace.v1.WorkspaceService/StartWorkspaceOperation", token, start, &started)
			if err == nil {
				break
			}
			if !retryWorkspace(err) || ctx.Err() != nil {
				return err
			}
			if err := waitWorkspace(ctx, time.Duration(attempt+1)*200*time.Millisecond); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
	if started.OperationID == "" {
		return fmt.Errorf("workspace operation returned no operation ID")
	}
	for {
		var result workspaceOperationResult
		err = a.call(ctx, a.client, "/agentruntime.workspace.v1.WorkspaceService/GetWorkspaceOperation", token, map[string]any{"workspaceId": req["workspaceId"], "operationId": started.OperationID}, &result)
		if err != nil {
			if !retryWorkspace(err) || ctx.Err() != nil {
				return err
			}
		} else {
			if done, e := result.decode(out); done {
				return e
			}
		}
		if err := waitWorkspace(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}
func retryWorkspace(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == 429 || ae.Status >= 500)
}
func waitWorkspace(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Kept in one decoder for resumed queries and ordinary polling.
type workspaceOperationResult struct {
	Status string                                   `json:"status"`
	Result json.RawMessage                          `json:"result"`
	Error  struct{ Category, Code, Message string } `json:"error"`
}

func (r workspaceOperationResult) decode(out any) (bool, error) {
	switch r.Status {
	case "WORKSPACE_OPERATION_STATUS_COMPLETED":
		if out == nil {
			return true, nil
		}
		return true, json.Unmarshal(r.Result, out)
	case "WORKSPACE_OPERATION_STATUS_FAILED":
		status := map[string]int{"ERROR_CATEGORY_INVALID": 400, "ERROR_CATEGORY_DENIED": 403, "ERROR_CATEGORY_NOT_FOUND": 404, "ERROR_CATEGORY_CONFLICT": 409, "ERROR_CATEGORY_TRANSIENT": 503}[r.Error.Category]
		if status == 0 {
			status = http.StatusBadGateway
		}
		if r.Error.Code == "repo_dirty" {
			status = http.StatusConflict
		}
		return true, &APIError{Status: status, Code: r.Error.Category, Reason: r.Error.Code, Message: r.Error.Message}
	case "WORKSPACE_OPERATION_STATUS_RUNNING":
		return false, nil
	default:
		return true, fmt.Errorf("unknown workspace operation status %q", r.Status)
	}
}
