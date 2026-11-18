package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

// ReadResult is the data payload of one planned production read. It carries no
// credentials, package bytes, native secret or endpoint information.
type ReadResult struct {
	SchemaVersion   string         `json:"schemaVersion"`
	PlanID          string         `json:"planId"`
	ResourceID      string         `json:"resourceId"`
	ResourceVersion v02.Version    `json:"resourceVersion"`
	BindingID       string         `json:"bindingId"`
	CredentialID    string         `json:"credentialId"`
	NativeIdentity  string         `json:"nativeIdentity"`
	Found           bool           `json:"found"`
	Items           []v02.ReadItem `json:"items"`
	Attempted       bool           `json:"-"`
}

// ReadService is the RA-11 entry point. A read is bound to an ACTIVE credential
// package for the current principal and resource before dispatch, and the
// binding is re-checked after the native call before the result is recorded.
type ReadService struct {
	Catalog     *Catalog
	Credentials *CredentialService
	Manager     *ReadManager
	Fence       *RestoreFence
}

func NewReadService(c *Catalog, credentials *CredentialService, m *ReadManager) *ReadService {
	return &ReadService{Catalog: c, Credentials: credentials, Manager: m}
}

// AuthorizeRead reports whether the credential is ACTIVE, unexpired and bound
// to the same resource for the current principal. It never returns package
// bytes or native secrets.
func (r *ReadService) AuthorizeRead(ctx context.Context, p metadata.Principal, credentialID, resourceID string) (int64, error) {
	tx, _, e := r.Catalog.begin(ctx, p)
	if e != nil {
		return 0, e
	}
	defer rollback(tx)
	row, e := r.Credentials.row(ctx, tx, p, credentialID)
	if e != nil {
		if errors.Is(e, ErrDenied) {
			return 0, ErrDenied
		}
		return 0, e
	}
	if e = r.Credentials.refresh(ctx, tx, p, row); e != nil {
		return 0, e
	}
	if row.state != "ACTIVE" || row.ref.ResourceID != resourceID {
		_ = commit(ctx, tx)
		return 0, ErrDenied
	}
	if e = commit(ctx, tx); e != nil {
		return 0, e
	}
	return row.revision, nil
}

func (r *ReadService) ReadItem(ctx context.Context, p metadata.Principal, in v02.ReadItemInput) (ReadResult, error) {
	return r.readItem(ctx, p, in, nil, ContextProof{})
}

// ReadItemInContext is the public MCP path. The host proof and activation are
// checked again before dispatch and before releasing a native result.
func (r *ReadService) ReadItemInContext(ctx context.Context, p metadata.Principal, in v02.ReadItemInput, contexts *ContextService, proof ContextProof) (ReadResult, error) {
	if contexts == nil {
		return ReadResult{}, ErrDenied
	}
	return r.readItem(ctx, p, in, contexts, proof)
}

func (r *ReadService) readItem(ctx context.Context, p metadata.Principal, in v02.ReadItemInput, contexts *ContextService, proof ContextProof) (ReadResult, error) {
	out := ReadResult{SchemaVersion: "0.2", Items: []v02.ReadItem{}}
	if !valid("ReadItemInput", in) {
		return out, ErrInput
	}
	if contexts != nil {
		if _, e := contexts.CheckActivation(ctx, p, proof, in.ResourceID, in.CredentialID); e != nil {
			return out, e
		}
	}
	snap, planID, e := r.claim(ctx, p, in)
	if e != nil {
		return out, e
	}
	out.PlanID = planID
	out.ResourceID = snap.Resource.ResourceID
	out.ResourceVersion = snap.Resource.Version
	out.BindingID = snap.Binding.BindingID
	out.CredentialID = in.CredentialID
	out.NativeIdentity = snap.Binding.NativeIdentity

	timeout := snap.Profile.TimeoutMs
	if timeout > v02.DefaultFixtureTimeoutMs {
		timeout = v02.DefaultFixtureTimeoutMs
	}
	call, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()

	// Dispatch re-check: the plan is only usable while the frozen snapshot and
	// the credential revision are still current.
	if e = r.current(call, p, snap, in); e != nil {
		r.finish(p, planID, "FAILED", nil)
		return out, e
	}
	if contexts != nil {
		if _, e = contexts.CheckActivation(call, p, proof, in.ResourceID, in.CredentialID); e != nil {
			r.finish(p, planID, "FAILED", nil)
			return out, e
		}
	}
	native, stop := watchDispatch(call, r.Catalog.Store.Pool, p.OrgID, planID, false)
	out.Attempted = true
	items, e := r.Manager.nativeRead(native, p, snap, in.ItemID)
	stop()
	if e != nil {
		state := "FAILED"
		if errors.Is(e, ErrUnavailable) {
			state = "OUTCOME_UNKNOWN"
		}
		r.finish(p, planID, state, nil)
		return out, e
	}
	// Completion re-check: a revocation or version change that commits while the
	// native read runs still fails the caller and keeps the audit trail.
	if e = r.current(ctx, p, snap, in); e != nil {
		r.finish(p, planID, "FAILED", nil)
		return out, e
	}
	if contexts != nil {
		if _, e = contexts.CheckActivation(ctx, p, proof, in.ResourceID, in.CredentialID); e != nil {
			r.finish(p, planID, "FAILED", nil)
			return out, e
		}
	}
	out.Found = len(items) > 0
	out.Items = items
	raw, _ := json.Marshal(out)
	if len(raw) > snap.Profile.MaxResultBytes {
		r.finish(p, planID, "FAILED", nil)
		return out, ErrUnavailable
	}
	if e = r.finish(p, planID, "SUCCEEDED", raw); e != nil {
		return ReadResult{PlanID: planID, Attempted: true}, ErrUnavailable
	}
	return out, nil
}

// claim freezes the immutable plan and records the intent before any dispatch.
func (r *ReadService) claim(ctx context.Context, p metadata.Principal, in v02.ReadItemInput) (Snapshot, string, error) {
	tx, epoch, e := r.Catalog.begin(ctx, p)
	if e != nil {
		return Snapshot{}, "", fmt.Errorf("begin: %w", e)
	}
	defer rollback(tx)
	row, e := r.Credentials.row(ctx, tx, p, in.CredentialID)
	if e != nil {
		if errors.Is(e, ErrDenied) {
			return Snapshot{}, "", ErrDenied
		}
		return Snapshot{}, "", e
	}
	if e = r.Credentials.refresh(ctx, tx, p, row); e != nil {
		return Snapshot{}, "", fmt.Errorf("refresh: %w", e)
	}
	if row.state != "ACTIVE" || row.ref.ResourceID != in.ResourceID {
		return Snapshot{}, "", ErrDenied
	}
	snap, e := r.Catalog.snapshotBound(ctx, tx, p, in.ResourceID, row.ref.ProfileID, row.ref.ProfileVersion, ReadOperation, FixtureToolName, epoch)
	if e != nil {
		return Snapshot{}, "", fmt.Errorf("snapshot: %w", e)
	}
	if r.Fence != nil {
		if e := r.Fence.Check(ctx, p.OrgID, snap.Resource.EndpointConfigRef); e != nil {
			return Snapshot{}, "", e
		}
	}
	id := uuid.NewString()
	plan, _ := json.Marshal(struct {
		Principal string
		Snapshot  Snapshot
		Input     v02.ReadItemInput
		Revision  int64
	}{p.ID, snap, in, row.revision})
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.read_plans(org_id,principal_id,plan_id,resource_id,resource_version,binding_id,binding_version,native_identity,secret_version,credential_id,credential_revision,item_id,auth_epoch,plan_hash,state)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'RUNNING')`,
		p.OrgID, p.ID, id, in.ResourceID, snap.Resource.Version, snap.Binding.BindingID, snap.Binding.BindingVersion, snap.Binding.NativeIdentity, snap.Binding.SecretVersion, in.CredentialID, row.revision, in.ItemID, epoch, r.Catalog.Store.Vault.Hash(plan))
	if e != nil {
		return Snapshot{}, "", fmt.Errorf("plan insert: %w", e)
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.read_audit(org_id,principal_id,plan_id,event_type) VALUES($1,$2,$3,'READ_INTENT')", p.OrgID, p.ID, id); e != nil {
		return Snapshot{}, "", fmt.Errorf("audit insert: %w", e)
	}
	if e = commit(ctx, tx); e != nil {
		return Snapshot{}, "", fmt.Errorf("commit: %w", e)
	}
	return snap, id, nil
}

// current re-reads the resource snapshot and the credential state, and reports
// ErrDenied when either moved after the plan was frozen.
func (r *ReadService) current(ctx context.Context, p metadata.Principal, snap Snapshot, in v02.ReadItemInput) error {
	tx, epoch, e := r.Catalog.begin(ctx, p)
	if e != nil {
		return e
	}
	defer rollback(tx)
	row, e := r.Credentials.row(ctx, tx, p, in.CredentialID)
	if e != nil {
		return ErrDenied
	}
	if e = r.Credentials.refresh(ctx, tx, p, row); e != nil {
		return e
	}
	if row.state != "ACTIVE" || row.ref.ResourceID != in.ResourceID {
		return ErrDenied
	}
	now, e := r.Catalog.snapshotBound(ctx, tx, p, in.ResourceID, row.ref.ProfileID, row.ref.ProfileVersion, ReadOperation, FixtureToolName, epoch)
	if e != nil {
		return e
	}
	if now.Epoch != snap.Epoch || now.Binding.BindingID != snap.Binding.BindingID || now.Binding.BindingVersion != snap.Binding.BindingVersion {
		return ErrDenied
	}
	return nil
}

func (r *ReadService) finish(p metadata.Principal, planID, state string, result []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, e := r.Catalog.Store.Pool.Begin(ctx)
	if e != nil {
		return ErrUnavailable
	}
	defer rollback(tx)
	var cipher []byte
	if result != nil {
		cipher = r.Catalog.Store.Vault.Seal(result, "ra-read/"+p.OrgID+"/"+p.ID+"/"+planID)
	}
	tag, e := tx.Exec(ctx, "UPDATE mcpguard_ra.read_plans SET state=$3,result_ciphertext=$4 WHERE org_id=$1 AND plan_id=$2 AND state='RUNNING'", p.OrgID, planID, state, cipher)
	if e != nil || tag.RowsAffected() != 1 {
		return ErrUnavailable
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.read_audit(org_id,principal_id,plan_id,event_type) VALUES($1,$2,$3,$4)", p.OrgID, p.ID, planID, state); e != nil {
		return ErrUnavailable
	}
	if e = commit(ctx, tx); e != nil {
		return ErrUnavailable
	}
	return nil
}
