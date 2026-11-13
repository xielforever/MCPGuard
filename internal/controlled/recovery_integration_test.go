package controlled_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xielforever/MCPGuard/internal/controlled"
)

type blocked struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	cancel  bool
}

func (*blocked) Available(string) bool { return true }
func (*blocked) Binding() string       { return "blocked-profile" }
func (n *blocked) Execute(ctx context.Context, _ controlled.Tool, _ string, _ map[string]any) controlled.Outcome {
	n.calls.Add(1)
	close(n.started)
	if n.cancel {
		<-ctx.Done()
	} else {
		<-n.release
	}
	return controlled.Outcome{Dispatch: "DISPATCH_UNKNOWN", SideEffect: "UNKNOWN", Code: "OUTCOME_UNKNOWN"}
}
func TestControlledCrashRecovery(t *testing.T) {
	f := setup(t)
	native := &blocked{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-native.release:
		default:
			close(native.release)
		}
	})
	var e error
	f.s, e = controlled.New(f.store, native, f.s.Origin)
	if e != nil {
		t.Fatal(e)
	}
	r := create(t, f, "redis.set_demo_note", map[string]any{"noteId": "help", "value": "crash-test", "ttlSeconds": 60})
	id := r["actionId"].(string)
	approve(t, f, id)
	done := make(chan error, 1)
	go func() { _, e := f.s.Control(f.ctx, f.p, id, "RESUME"); done <- e }()
	select {
	case <-native.started:
	case <-time.After(3 * time.Second):
		t.Fatal("did not claim")
	}
	tx, e := f.admin.Begin(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(f.ctx)
	if _, e = tx.Exec(f.ctx, `ALTER TABLE mcpguard.managed_executions DISABLE TRIGGER managed_execution_guard`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(f.ctx, `UPDATE mcpguard.managed_executions SET hard_deadline=clock_timestamp()-interval '30 seconds' WHERE org_id=$1 AND action_id=$2`, f.p.OrgID, id); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(f.ctx, `ALTER TABLE mcpguard.managed_executions ENABLE TRIGGER managed_execution_guard`); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(f.ctx); e != nil {
		t.Fatal(e)
	}
	// A new service instance terminalizes stale intent; it never invokes native.
	recovered, e := controlled.New(f.store, native, f.s.Origin)
	if e != nil {
		t.Fatal(e)
	}
	if e = recovered.Recover(f.ctx); e != nil {
		t.Fatal(e)
	}
	v, e := recovered.Control(f.ctx, f.p, id, "RESUME")
	if e != nil || v["action"].(map[string]any)["state"] != "OUTCOME_UNKNOWN" {
		t.Fatal(v, e)
	}
	close(native.release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if native.calls.Load() != 1 {
		t.Fatal("recovery replayed write")
	}
	if _, e = f.store.Pool.Exec(f.ctx, `DELETE FROM mcpguard.managed_executions WHERE org_id=$1 AND action_id=$2`, f.p.OrgID, id); e == nil {
		t.Fatal("permanent qualification deleted")
	}
}
func TestControlledCancellation(t *testing.T) {
	f := setup(t)
	native := &blocked{started: make(chan struct{}), cancel: true}
	var e error
	f.s, e = controlled.New(f.store, native, f.s.Origin)
	if e != nil {
		t.Fatal(e)
	}
	r := create(t, f, "redis.set_demo_note", map[string]any{"noteId": "help", "value": "cancel-test", "ttlSeconds": 60})
	id := r["actionId"].(string)
	approve(t, f, id)
	done := make(chan error, 1)
	go func() { _, e := f.s.Control(f.ctx, f.p, id, "RESUME"); done <- e }()
	<-native.started
	cancelled, e := f.s.Control(f.ctx, f.p, id, "CANCEL")
	if e != nil || cancelled["outcome"] != "CANCELLATION_REQUESTED" {
		t.Fatal(cancelled, e)
	}
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("durable cancellation did not reach executor")
	}
	if result(t, f, id)["requestStatus"] != "OUTCOME_UNKNOWN" {
		t.Fatal("cancel fabricated safe rollback")
	}
}
func TestGroupAuthorization(t *testing.T) {
	f := setup(t)
	gid := uuid.NewString()
	save := func(payload map[string]any) {
		t.Helper()
		snap, e := f.s.Snapshot(f.ctx, f.reviewer)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.Configure(f.ctx, f.reviewer, payload, fmt.Sprintf(`"%v"`, snap["revision"]), uuid.NewString()); e != nil {
			t.Fatal(e)
		}
	}
	save(map[string]any{"kind": "group", "id": gid, "name": "Local readers", "enabled": false})
	save(map[string]any{"kind": "groupMember", "groupId": gid, "principalId": f.p.ID, "allowed": true})
	save(map[string]any{"kind": "groupGrant", "groupId": gid, "toolName": "redis.get_demo_note", "allowed": true})
	if _, e := f.admin.Exec(f.ctx, `DELETE FROM mcpguard.tool_grants WHERE org_id=$1 AND principal_id=$2 AND tool_name='redis.get_demo_note'`, f.p.OrgID, f.p.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Create(f.ctx, f.p, "redis.get_demo_note", []byte(`{"noteId":"welcome"}`)); !errors.Is(e, controlled.ErrDenied) {
		t.Fatal("disabled group granted access", e)
	}
	save(map[string]any{"kind": "group", "id": gid, "name": "Local readers", "enabled": true})
	r := create(t, f, "redis.get_demo_note", map[string]any{"noteId": "welcome"})
	if r["requestStatus"] != "SUCCEEDED" {
		t.Fatal(r)
	}
	save(map[string]any{"kind": "grant", "principalId": f.p.ID, "toolName": "redis.get_demo_note", "allowed": false})
	if _, e := f.s.Create(f.ctx, f.p, "redis.get_demo_note", []byte(`{"noteId":"welcome"}`)); !errors.Is(e, controlled.ErrDenied) {
		t.Fatal("explicit principal denial lost to group", e)
	}
}
