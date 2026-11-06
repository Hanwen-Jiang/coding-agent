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
	start := map[string]any{"requestId": requestID, operation: req}
	var started struct {
		OperationID string `json:"operationId"`
	}
	var err error
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
	if started.OperationID == "" {
		return fmt.Errorf("workspace operation returned no operation ID")
	}
	for {
		var result struct {
			Status string                                   `json:"status"`
			Result json.RawMessage                          `json:"result"`
			Error  struct{ Category, Code, Message string } `json:"error"`
		}
		err = a.call(ctx, a.client, "/agentruntime.workspace.v1.WorkspaceService/GetWorkspaceOperation", token, map[string]any{"workspaceId": req["workspaceId"], "operationId": started.OperationID}, &result)
		if err != nil {
			if !retryWorkspace(err) || ctx.Err() != nil {
				return err
			}
		} else {
			switch result.Status {
			case "WORKSPACE_OPERATION_STATUS_COMPLETED":
				if out == nil {
					return nil
				}
				return json.Unmarshal(result.Result, out)
			case "WORKSPACE_OPERATION_STATUS_FAILED":
				status := map[string]int{"ERROR_CATEGORY_INVALID": 400, "ERROR_CATEGORY_DENIED": 403, "ERROR_CATEGORY_NOT_FOUND": 404, "ERROR_CATEGORY_CONFLICT": 409, "ERROR_CATEGORY_TRANSIENT": 503}[result.Error.Category]
				if status == 0 {
					status = http.StatusBadGateway
				}
				if result.Error.Code == "repo_dirty" {
					status = http.StatusConflict
				}
				return &APIError{Status: status, Code: result.Error.Category, Reason: result.Error.Code, Message: result.Error.Message}
			case "WORKSPACE_OPERATION_STATUS_RUNNING":
			default:
				return fmt.Errorf("unknown workspace operation status %q", result.Status)
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
