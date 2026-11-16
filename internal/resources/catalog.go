// Package resources implements the opt-in v0.2 catalog and RA-03 native read
// foundation. It does not publish the future grant/credential-protected tools.
package resources

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

var (
	ErrDenied      = errors.New("resource access denied")
	ErrInput       = errors.New("invalid resource arguments")
	ErrConflict    = errors.New("resource version conflict")
	ErrUnavailable = errors.New("resource dependency unavailable")
)

const FixtureToolName = "fixture.pg_read_item"
const ReadOperation = "pg.item.read.v1"
const WriteOperation = "pg.item.update.v1"

// WriteToolName is the fixed public tool bound to the write profile.
const WriteToolName = "pg.update_note"

type Catalog struct {
	Store         *metadata.Store
	SchemaVersion int
	Access        *AccessService
}
type Snapshot struct {
	Resource v02.ResourceVersion
	Binding  v02.CredentialBinding
	Profile  v02.PermissionProfile
	Epoch    int64
}

func NewCatalog(ctx context.Context, s *metadata.Store) (*Catalog, error) {
	if s == nil || v02.Initialize() != nil {
		return nil, ErrUnavailable
	}
	version, err := schemaVersion(ctx, s.Pool)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Catalog{Store: s, SchemaVersion: version}, nil
}
func (c *Catalog) begin(ctx context.Context, p metadata.Principal) (pgx.Tx, int64, error) {
	tx, err := c.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("begin tx: %w", err)
	}
	var epoch int64
	if err = tx.QueryRow(ctx, "SELECT mcpguard.lock_auth_epoch($1)", p.OrgID).Scan(&epoch); err != nil {
		rollback(tx)
		return nil, 0, fmt.Errorf("begin lock: %w", err)
	}
	var current bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM mcpguard.principals WHERE org_id=$1 AND id=$2 AND token_hash=$3 AND enabled AND expires_at>clock_timestamp())", p.OrgID, p.ID, p.TokenHash).Scan(&current)
	if err != nil {
		rollback(tx)
		return nil, 0, fmt.Errorf("begin principal: %w", err)
	}
	if !current {
		rollback(tx)
		return nil, 0, ErrDenied
	}
	return tx, epoch, nil
}
func rollback(tx pgx.Tx) {
	ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
	defer done()
	_ = tx.Rollback(ctx)
}
func valid(name string, value any) bool {
	b, e := json.Marshal(value)
	return e == nil && v02.Decode(name, b, nil) == nil
}

type catalogCursor struct {
	Org, Principal, Kind, Last string
	Epoch                      int64
}

func (c *Catalog) cursor(v catalogCursor) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b) + "." + c.Store.Vault.Hash(b)
}
func (c *Catalog) parseCursor(raw string, want catalogCursor) (string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return "", ErrInput
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil || !hmac.Equal([]byte(parts[1]), []byte(c.Store.Vault.Hash(b))) {
		return "", ErrInput
	}
	var v catalogCursor
	if json.Unmarshal(b, &v) != nil || v.Org != want.Org || v.Principal != want.Principal || v.Kind != want.Kind || v.Epoch != want.Epoch {
		return "", ErrInput
	}
	if _, e = uuid.Parse(v.Last); e != nil {
		return "", ErrInput
	}
	return v.Last, nil
}

const resourceSelect = `SELECT v.definition,h.revision::text,h.enabled,a.can_request
 FROM mcpguard_ra.resource_heads h
 JOIN mcpguard_ra.resource_versions v ON (v.org_id,v.resource_id,v.version)=(h.org_id,h.resource_id,h.version)
 JOIN mcpguard_ra.resource_access a ON (a.org_id,a.resource_id)=(h.org_id,h.resource_id)
 WHERE h.org_id=$1 AND a.principal_id=$2 AND a.can_discover`

func resourceView(raw []byte, revision string, enabled, request bool) (v02.ResourceView, error) {
	var r v02.ResourceVersion
	if v02.Decode("ResourceVersion", raw, &r) != nil {
		return v02.ResourceView{}, ErrUnavailable
	}
	return v02.ResourceView{ResourceID: r.ResourceID, Version: r.Version, Revision: revision, Kind: r.Kind, Alias: r.Alias, DisplayName: r.DisplayName, Enabled: enabled, CanRequest: request && enabled, AccessState: "NOT_REQUESTED"}, nil
}
func (c *Catalog) List(ctx context.Context, p metadata.Principal, in v02.ListResourcesInput) (v02.ListResourcesResult, error) {
	out := v02.ListResourcesResult{SchemaVersion: "0.2", Resources: []v02.ResourceView{}}
	if !valid("ListResourcesInput", in) {
		return out, ErrInput
	}
	limit := 100
	kind := ""
	if in.Limit != nil {
		limit = *in.Limit
	}
	if in.Kind != nil {
		kind = *in.Kind
	}
	tx, epoch, e := c.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	cursor := catalogCursor{Org: p.OrgID, Principal: p.ID, Kind: kind, Epoch: epoch}
	var after any
	if in.After != nil && *in.After != "" {
		last, err := c.parseCursor(*in.After, cursor)
		if err != nil {
			return out, err
		}
		after = last
	}
	rows, e := tx.Query(ctx, resourceSelect+` AND ($3::uuid IS NULL OR h.resource_id>$3) AND ($4='' OR v.definition->>'kind'=$4) ORDER BY h.resource_id LIMIT $5`, p.OrgID, p.ID, after, kind, limit+1)
	if e != nil {
		return out, ErrUnavailable
	}
	for rows.Next() {
		var raw []byte
		var revision string
		var enabled, request bool
		if rows.Scan(&raw, &revision, &enabled, &request) != nil {
			rows.Close()
			return out, ErrUnavailable
		}
		item, e := resourceView(raw, revision, enabled, request)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Resources = append(out.Resources, item)
	}
	err := rows.Err()
	rows.Close()
	if err != nil {
		return out, ErrUnavailable
	}
	if len(out.Resources) > limit {
		out.Resources = out.Resources[:limit]
		cursor.Last = out.Resources[limit-1].ResourceID
		out.NextAfter = c.cursor(cursor)
	}
	if tx.Commit(ctx) != nil {
		return out, ErrUnavailable
	}
	if c.Access != nil {
		epoch, e := c.Access.Decorate(ctx, p, out.Resources)
		if e != nil {
			return out, e
		}
		if out.NextAfter != "" {
			cursor.Epoch = epoch
			out.NextAfter = c.cursor(cursor)
		}
	}
	return out, nil
}
func (c *Catalog) Get(ctx context.Context, p metadata.Principal, in v02.GetResourceInput) (v02.GetResourceResult, error) {
	out := v02.GetResourceResult{SchemaVersion: "0.2", Profiles: []v02.ProfileView{}}
	if !valid("GetResourceInput", in) {
		return out, ErrInput
	}
	tx, _, err := c.begin(ctx, p)
	if err != nil {
		return out, err
	}
	defer rollback(tx)
	var raw []byte
	var revision string
	var enabled, request bool
	err = tx.QueryRow(ctx, resourceSelect+" AND h.resource_id=$3", p.OrgID, p.ID, in.ResourceID).Scan(&raw, &revision, &enabled, &request)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	if err != nil {
		return out, ErrUnavailable
	}
	out.Resource, err = resourceView(raw, revision, enabled, request)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT p.definition FROM mcpguard_ra.profile_versions p JOIN mcpguard_ra.tool_bindings b
 ON (b.org_id,b.resource_id,b.profile_id,b.profile_version)=(p.org_id,p.resource_id,p.profile_id,p.version)
 WHERE b.org_id=$1 AND b.resource_id=$2 AND b.resource_version=$3 ORDER BY p.definition`, p.OrgID, in.ResourceID, out.Resource.Version)
	if err != nil {
		return out, ErrUnavailable
	}
	for rows.Next() {
		var data []byte
		var profile v02.PermissionProfile
		if rows.Scan(&data) != nil || v02.Decode("PermissionProfile", data, &profile) != nil {
			rows.Close()
			return out, ErrUnavailable
		}
		out.Profiles = append(out.Profiles, v02.ProfileView{ProfileID: profile.ProfileID, Version: profile.Version, Name: profile.Name})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, ErrUnavailable
	}
	if tx.Commit(ctx) != nil {
		return out, ErrUnavailable
	}
	if c.Access != nil {
		views := []v02.ResourceView{out.Resource}
		if _, e := c.Access.Decorate(ctx, p, views); e != nil {
			return out, e
		}
		out.Resource = views[0]
		if e := c.Access.ProfileLimits(ctx, p, out.Resource, out.Profiles); e != nil {
			return out, e
		}
	}
	return out, nil
}
func (c *Catalog) snapshot(ctx context.Context, tx pgx.Tx, p metadata.Principal, resourceID string, epoch int64) (Snapshot, error) {
	return c.snapshotBound(ctx, tx, p, resourceID, "", "", ReadOperation, FixtureToolName, epoch)
}

// snapshotBound selects the binding of one profile. A person may hold both a
// read and a write identity for the same resource; credential issuance and
// resource reads must use the binding that belongs to their profile.
func (c *Catalog) snapshotBound(ctx context.Context, tx pgx.Tx, p metadata.Principal, resourceID, profileID, profileVersion, operation, toolName string, epoch int64) (Snapshot, error) {
	var snap Snapshot
	var raw []byte
	var revision string
	var enabled, request bool
	snap.Epoch = epoch
	e := tx.QueryRow(ctx, resourceSelect+" AND h.resource_id=$3", p.OrgID, p.ID, resourceID).Scan(&raw, &revision, &enabled, &request)
	if errors.Is(e, pgx.ErrNoRows) {
		return snap, ErrDenied
	}
	if e != nil {
		return snap, ErrUnavailable
	}
	if !enabled {
		return snap, ErrDenied
	}
	if v02.Decode("ResourceVersion", raw, &snap.Resource) != nil || snap.Resource.Kind != "POSTGRES" {
		return snap, ErrDenied
	}
	// Look up the highest version BEFORE testing enabled. Never fall back to an
	// older enabled binding after a newer version has been disabled.
	if profileID == "" {
		e = tx.QueryRow(ctx, `SELECT definition,enabled FROM mcpguard_ra.identity_bindings WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3 ORDER BY binding_version DESC LIMIT 1`, p.OrgID, p.ID, resourceID).Scan(&raw, &enabled)
	} else {
		e = tx.QueryRow(ctx, `SELECT definition,enabled FROM mcpguard_ra.identity_bindings WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3 AND profile_id=$4 AND profile_version=$5 ORDER BY binding_version DESC LIMIT 1`, p.OrgID, p.ID, resourceID, profileID, profileVersion).Scan(&raw, &enabled)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return snap, ErrDenied
	}
	if e != nil {
		return snap, ErrUnavailable
	}
	if !enabled {
		return snap, ErrDenied
	}
	if v02.Decode("CredentialBinding", raw, &snap.Binding) != nil {
		return snap, ErrUnavailable
	}
	if snap.Binding.OrgID != p.OrgID || snap.Binding.PrincipalID != p.ID || snap.Binding.ResourceID != resourceID || snap.Binding.ResourceVersion != snap.Resource.Version {
		return snap, ErrDenied
	}
	e = tx.QueryRow(ctx, `SELECT p.definition FROM mcpguard_ra.profile_versions p JOIN mcpguard_ra.tool_bindings t
 ON (t.org_id,t.resource_id,t.profile_id,t.profile_version)=(p.org_id,p.resource_id,p.profile_id,p.version)
 WHERE t.org_id=$1 AND t.resource_id=$2 AND t.resource_version=$3 AND t.profile_id=$4 AND t.profile_version=$5 AND t.tool_name=$6 AND t.operation_id=$7 AND t.tool_version=1`, p.OrgID, resourceID, snap.Resource.Version, snap.Binding.ProfileID, snap.Binding.ProfileVersion, toolName, operation).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return snap, ErrDenied
	}
	if e != nil {
		return snap, ErrUnavailable
	}
	if v02.Decode("PermissionProfile", raw, &snap.Profile) != nil || snap.Profile.NativeEnforcement != "PG_OWNER_RLS_V1" {
		return snap, ErrDenied
	}
	found := false
	for _, op := range snap.Profile.AllowedOperations {
		found = found || op == operation
	}
	if !found {
		return snap, ErrDenied
	}
	if snap.Binding.AccountID != nil {
		if c.SchemaVersion < 3 {
			return snap, ErrDenied
		}
		var requestID string
		e = tx.QueryRow(ctx, `SELECT v.request_id::text FROM mcpguard_ra.access_grant_versions v JOIN mcpguard_ra.access_grant_heads h ON (h.org_id,h.grant_id,h.version)=(v.org_id,v.grant_id,v.version) WHERE v.org_id=$1 AND v.subject_id=$2 AND v.resource_id=$3 AND v.account_id=$4 AND v.binding_id=$5 AND v.binding_version=$6 AND h.state='ACTIVE' AND v.expires_at>clock_timestamp()`, p.OrgID, p.ID, resourceID, *snap.Binding.AccountID, snap.Binding.BindingID, snap.Binding.BindingVersion).Scan(&requestID)
		if errors.Is(e, pgx.ErrNoRows) {
			return snap, ErrDenied
		}
		if e != nil {
			return snap, ErrUnavailable
		}
		access := &AccessService{Catalog: c}
		r, e := access.request(ctx, tx, p.OrgID, requestID)
		if e != nil {
			return snap, e
		}
		yes, e := access.qualified(ctx, tx, r)
		if e != nil {
			return snap, e
		}
		if !yes {
			return snap, ErrDenied
		}
	}
	return snap, nil
}
func (c *Catalog) Snapshot(ctx context.Context, p metadata.Principal, resourceID string) (Snapshot, error) {
	tx, epoch, e := c.begin(ctx, p)
	if e != nil {
		return Snapshot{}, e
	}
	defer rollback(tx)
	snap, e := c.snapshot(ctx, tx, p, resourceID, epoch)
	if e != nil {
		return snap, e
	}
	if tx.Commit(ctx) != nil {
		return snap, ErrUnavailable
	}
	return snap, nil
}
func (c *Catalog) ClaimRead(ctx context.Context, p metadata.Principal, in v02.FixtureReadInput) (Snapshot, string, error) {
	if !valid("FixtureReadInput", in) {
		return Snapshot{}, "", ErrInput
	}
	tx, epoch, e := c.begin(ctx, p)
	if e != nil {
		return Snapshot{}, "", e
	}
	defer rollback(tx)
	snap, e := c.snapshot(ctx, tx, p, in.ResourceID, epoch)
	if e != nil {
		return snap, "", e
	}
	id := uuid.NewString()
	plan, _ := json.Marshal(struct {
		Principal string
		Snapshot  Snapshot
		Input     v02.FixtureReadInput
	}{p.ID, snap, in})
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.fixture_reads(org_id,principal_id,id,resource_id,resource_version,binding_id,binding_version,native_identity,secret_version,auth_epoch,plan_hash,state)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'RUNNING')`, p.OrgID, p.ID, id, in.ResourceID, snap.Resource.Version, snap.Binding.BindingID, snap.Binding.BindingVersion, snap.Binding.NativeIdentity, snap.Binding.SecretVersion, epoch, c.Store.Vault.Hash(plan))
	if e != nil {
		return snap, "", ErrUnavailable
	}
	_, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.fixture_audit(org_id,principal_id,execution_id,event_type) VALUES($1,$2,$3,'READ_INTENT')", p.OrgID, p.ID, id)
	if e != nil || tx.Commit(ctx) != nil {
		return snap, "", ErrUnavailable
	}
	return snap, id, nil
}
func (c *Catalog) FinishRead(ctx context.Context, p metadata.Principal, id, state string, result []byte) error {
	if state != "SUCCEEDED" && state != "FAILED" && state != "OUTCOME_UNKNOWN" {
		return ErrInput
	}
	tx, e := c.Store.Pool.Begin(ctx)
	if e != nil {
		return ErrUnavailable
	}
	defer rollback(tx)
	cipher := c.Store.Vault.Seal(result, "ra-read/"+p.OrgID+"/"+p.ID+"/"+id)
	tag, e := tx.Exec(ctx, "UPDATE mcpguard_ra.fixture_reads SET state=$4,result_ciphertext=$5 WHERE org_id=$1 AND principal_id=$2 AND id=$3 AND state='RUNNING'", p.OrgID, p.ID, id, state, cipher)
	if e != nil || tag.RowsAffected() != 1 {
		return ErrUnavailable
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.fixture_audit(org_id,principal_id,execution_id,event_type) VALUES($1,$2,$3,$4)", p.OrgID, p.ID, id, state); e != nil {
		return ErrUnavailable
	}
	if tx.Commit(ctx) != nil {
		return ErrUnavailable
	}
	return nil
}
func number(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
