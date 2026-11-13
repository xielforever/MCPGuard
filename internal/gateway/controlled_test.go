package gateway

import (
	"encoding/json"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"testing"
	"time"
)

func validateManaged(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	var schema jsonschema.Schema
	if e := json.Unmarshal(raw, &schema); e != nil {
		t.Fatal(e)
	}
	resolved, e := schema.Resolve(nil)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(v)
	var value any
	_ = json.Unmarshal(b, &value)
	if e = resolved.Validate(value); e != nil {
		t.Fatalf("managed schema rejected %s: %v", b, e)
	}
}
func TestControlledOutputContracts(t *testing.T) {
	id, attempt, execution, rid := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	deferred := map[string]any{"kind": "DEFERRED", "actionId": id, "attemptId": attempt, "state": "PENDING_APPROVAL", "executionId": nil, "dispatchState": "NOT_DISPATCHED", "sideEffectStatus": "NONE", "approvalUrl": "http://127.0.0.1:18080/web/#action=" + id, "nextActions": []string{"STATUS", "CANCEL"}, "expiresAt": now.Add(time.Minute)}
	validateManaged(t, controlledResultSchema, deferred)
	for _, state := range []string{"PENDING_APPROVAL", "READY", "EXECUTING", "SUCCEEDED", "FAILED", "DENIED", "EXPIRED", "CANCELLED", "OUTCOME_UNKNOWN"} {
		t.Run(state, func(t *testing.T) {
			var eid, status, source any
			dispatch, side := "NOT_DISPATCHED", "NONE"
			if state == "READY" {
				source = "HUMAN"
			}
			if state == "EXECUTING" || state == "SUCCEEDED" || state == "FAILED" || state == "OUTCOME_UNKNOWN" {
				eid = execution
				status = state
				source = "HUMAN"
				dispatch = "DISPATCHED"
			}
			if state == "EXECUTING" {
				status = "RUNNING"
				dispatch = "INTENT_RECORDED"
			}
			if state == "SUCCEEDED" {
				side = "APPLIED"
			}
			if state == "OUTCOME_UNKNOWN" {
				dispatch = "DISPATCH_UNKNOWN"
				side = "UNKNOWN"
			}
			if state == "DENIED" || state == "EXPIRED" {
				status = "FAILED"
			}
			if state == "CANCELLED" {
				status = "CANCELLED"
			}
			action := map[string]any{"actionId": id, "state": state, "authorizationSource": source, "interactionMode": "CONTROL_FLOW", "revision": 1, "planHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "toolName": "redis.set_demo_note", "resourceId": rid, "createdAt": now, "expiresAt": now.Add(time.Minute), "executionId": eid, "dispatchState": dispatch, "requestStatus": status, "sideEffectStatus": side, "availableActions": []string{"STATUS"}}
			control := map[string]any{"kind": "ACTION_CONTROL", "operation": "STATUS", "requestId": uuid.NewString(), "outcome": "STATE_RETURNED", "action": action, "result": nil, "error": nil}
			validateManaged(t, controlledActionSchema, control)
		})
	}
}
