package resources

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

type CredentialService struct {
	// Fence is optional; when configured it blocks previews/issuance while the
	// native recovery generation is ahead of the metadata recovery point.
	Access *AccessService
	Keys   PackageKeyProvider
	nonce  func() ([]byte, error)
	Fence  *RestoreFence
}

func NewCredentialService(c *Catalog, keys PackageKeyProvider) (*CredentialService, error) {
	if c == nil || c.SchemaVersion < 5 || keys == nil {
		return nil, ErrUnavailable
	}
	a, e := NewAccessService(c)
	if e != nil {
		return nil, e
	}
	return &CredentialService{a, keys, func() ([]byte, error) {
		nonce := make([]byte, v02.CredentialNonceBytes)
		_, e := rand.Read(nonce)
		return nonce, e
	}, nil}, nil
}

type credentialSelection struct {
	grant    v02.AccessGrant
	snapshot Snapshot
	issuer   v02.CredentialIssuer
	policy   v02.CredentialLeasePolicy
}
type credentialRow struct {
	ref                                                      v02.CredentialReference
	state                                                    string
	revision                                                 int64
	issued, expiry                                           time.Time
	issuerVersion, policyVersion, keyID, fingerprint, digest string
	nonce                                                    []byte
}

func credentialAudit(ctx context.Context, tx pgx.Tx, p metadata.Principal, cid, event string) error {
	_, e := tx.Exec(ctx, "INSERT INTO mcpguard_ra.credential_audit(org_id,subject_id,credential_id,reservation_id,event_type) VALUES($1,$2,$3,$3,$4)", p.OrgID, p.ID, cid, event)
	return e
}
func (s *CredentialService) issuer(ctx context.Context, tx pgx.Tx, org string) (v02.CredentialIssuer, error) {
	var i v02.CredentialIssuer
	var raw []byte
	e := tx.QueryRow(ctx, "SELECT definition FROM mcpguard_ra.credential_issuers WHERE org_id=$1", org).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return i, ErrDenied
	}
	if e != nil {
		return i, ErrUnavailable
	}
	if v02.Decode("CredentialIssuer", raw, &i) != nil || !i.Enabled || i.OrgID != org {
		return i, ErrDenied
	}
	return i, nil
}

// profileOperation reports the single operation a granted profile allows.
func (s *CredentialService) profileOperation(ctx context.Context, tx pgx.Tx, org, resourceID, profileID, profileVersion string) (string, error) {
	var raw []byte
	if e := tx.QueryRow(ctx, `SELECT definition FROM mcpguard_ra.profile_versions WHERE org_id=$1 AND resource_id=$2 AND profile_id=$3 AND version=$4`, org, resourceID, profileID, profileVersion).Scan(&raw); e != nil {
		return "", ErrUnavailable
	}
	var profile v02.PermissionProfile
	if v02.Decode("PermissionProfile", raw, &profile) != nil || len(profile.AllowedOperations) != 1 {
		return "", ErrDenied
	}
	return profile.AllowedOperations[0], nil
}

func (s *CredentialService) selectGrant(ctx context.Context, tx pgx.Tx, p metadata.Principal, grantID string) (credentialSelection, error) {
	var out credentialSelection
	g, e := s.Access.readGrant(ctx, tx, p.OrgID, grantID)
	if e != nil {
		return out, e
	}
	if g.SubjectID != p.ID || g.State != "ACTIVE" || g.AccountID == nil || g.BindingID == nil {
		return out, ErrDenied
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return out, ErrUnavailable
	}
	expiry, _, _ := normalizedTime(g.ExpiresAt)
	if !expiry.After(now) {
		return out, ErrDenied
	}
	var epoch int64
	if e = tx.QueryRow(ctx, "SELECT auth_epoch FROM mcpguard.authorization_heads WHERE org_id=$1", p.OrgID).Scan(&epoch); e != nil {
		return out, ErrUnavailable
	}
	operation, e := s.profileOperation(ctx, tx, p.OrgID, g.ResourceID, g.ProfileID, g.ProfileVersion)
	if e != nil {
		return out, e
	}
	toolName := FixtureToolName
	if operation == WriteOperation {
		toolName = WriteToolName
	}
	snap, e := s.Access.Catalog.snapshotBound(ctx, tx, p, g.ResourceID, g.ProfileID, g.ProfileVersion, operation, toolName, epoch)
	if e != nil {
		return out, e
	}
	if s.Fence != nil {
		if e := s.Fence.Check(ctx, p.OrgID, snap.Resource.EndpointConfigRef); e != nil {
			return out, e
		}
	}
	if snap.Binding.AccountID == nil || *snap.Binding.AccountID != *g.AccountID || snap.Binding.BindingID != *g.BindingID || snap.Resource.Version != g.ResourceVersion || snap.Binding.ProfileID != g.ProfileID || snap.Binding.ProfileVersion != g.ProfileVersion {
		return out, ErrDenied
	}
	i, e := s.issuer(ctx, tx, p.OrgID)
	if e != nil {
		return out, e
	}
	var raw []byte
	var policy v02.CredentialLeasePolicy
	e = tx.QueryRow(ctx, "SELECT definition FROM mcpguard_ra.credential_policies WHERE org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5", p.OrgID, g.ResourceID, g.ResourceVersion, g.ProfileID, g.ProfileVersion).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	if e != nil {
		return out, ErrUnavailable
	}
	if v02.Decode("CredentialLeasePolicy", raw, &policy) != nil || !policy.Enabled {
		return out, ErrDenied
	}
	return credentialSelection{*g, snap, i, policy}, nil
}
func statusOf(r *credentialRow) v02.CredentialStatus {
	return v02.CredentialStatus{CredentialID: r.ref.CredentialID, LeaseID: r.ref.LeaseID, ExpiresAt: r.expiry.UTC().Format(time.RFC3339Nano), State: r.state, Revision: strconv.FormatInt(r.revision, 10)}
}
func (s *CredentialService) row(ctx context.Context, tx pgx.Tx, p metadata.Principal, cid string) (*credentialRow, error) {
	r := &credentialRow{}
	var raw []byte
	e := tx.QueryRow(ctx, `SELECT p.reference,l.state,l.revision,l.issued_at,l.expires_at,l.issuer_version::text,l.policy_version::text,p.key_id,p.key_fingerprint,p.digest,p.nonce FROM mcpguard_ra.credential_packages p JOIN mcpguard_ra.credential_leases l ON (l.org_id,l.lease_id,l.credential_id)=(p.org_id,p.lease_id,p.credential_id) WHERE p.org_id=$1 AND l.subject_id=$2 AND p.credential_id=$3 FOR UPDATE OF l`, p.OrgID, p.ID, cid).Scan(&raw, &r.state, &r.revision, &r.issued, &r.expiry, &r.issuerVersion, &r.policyVersion, &r.keyID, &r.fingerprint, &r.digest, &r.nonce)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	if v02.Decode("CredentialReference", raw, &r.ref) != nil || r.ref.OrgID != p.OrgID || r.ref.SubjectID != p.ID || r.ref.CredentialID != cid {
		return nil, ErrDenied
	}
	issued, _, _ := normalizedTime(r.ref.IssuedAt)
	expiry, _, _ := normalizedTime(r.ref.ExpiresAt)
	if !issued.Equal(r.issued) || !expiry.Equal(r.expiry) {
		return nil, ErrDenied
	}
	return r, nil
}
func selectionMatches(r *credentialRow, c credentialSelection) bool {
	ref := r.ref
	return c.grant.GrantID == ref.GrantID && c.grant.GrantVersion == ref.GrantVersion && c.grant.ResourceID == ref.ResourceID && c.grant.ResourceVersion == ref.ResourceVersion && c.grant.ProfileID == ref.ProfileID && c.grant.ProfileVersion == ref.ProfileVersion && c.snapshot.Binding.BindingID == ref.BindingID && c.snapshot.Binding.BindingVersion == ref.BindingVersion && c.issuer.Issuer == ref.Issuer && c.issuer.IssuerEpoch == ref.IssuerEpoch && c.issuer.KeyID == r.keyID && c.issuer.KeyFingerprint == r.fingerprint && c.issuer.Version == r.issuerVersion && c.policy.Version == r.policyVersion
}
func (s *CredentialService) transition(ctx context.Context, tx pgx.Tx, p metadata.Principal, r *credentialRow, state, reason string) error {
	if r.state != "ACTIVE" {
		return nil
	}
	var cipher []byte
	if reason != "" {
		cipher = s.Access.Catalog.Store.Vault.Seal([]byte(reason), "credential-reason/"+p.OrgID+"/"+r.ref.CredentialID)
	}
	_, e := tx.Exec(ctx, "UPDATE mcpguard_ra.credential_leases SET state=$3,revision=revision+1,reason_ciphertext=$4 WHERE org_id=$1 AND credential_id=$2", p.OrgID, r.ref.CredentialID, state, cipher)
	if e != nil {
		return ErrUnavailable
	}
	if _, e = tx.Exec(ctx, "SELECT mcpguard_ra.advance_access_epoch($1)", p.OrgID); e != nil {
		return ErrUnavailable
	}
	if e = credentialAudit(ctx, tx, p, r.ref.CredentialID, "LEASE_"+state); e != nil {
		return ErrUnavailable
	}
	r.state = state
	r.revision++
	return nil
}
func (s *CredentialService) refresh(ctx context.Context, tx pgx.Tx, p metadata.Principal, r *credentialRow) error {
	if r.state != "ACTIVE" {
		return nil
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return ErrUnavailable
	}
	if !r.expiry.After(now) {
		return s.transition(ctx, tx, p, r, "EXPIRED", "")
	}
	current, e := s.selectGrant(ctx, tx, p, r.ref.GrantID)
	if errors.Is(e, errRecoveryPointUnverified) {
		// The restored lease cannot be used until reconciliation. Preserve
		// its recorded state so a verified, unaffected grant can recover.
		return e
	}
	if errors.Is(e, ErrDenied) || errors.Is(e, ErrConflict) || e == nil && !selectionMatches(r, current) {
		return s.transition(ctx, tx, p, r, "REVOKED", "")
	}
	return e
}
func (s *CredentialService) Status(ctx context.Context, p metadata.Principal, in v02.CredentialIDInput) (v02.CredentialStatus, error) {
	var out v02.CredentialStatus
	if !valid("CredentialIDInput", in) {
		return out, ErrInput
	}
	tx, _, e := s.Access.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	row, e := s.row(ctx, tx, p, in.CredentialID)
	if e != nil {
		return out, e
	}
	if e = s.refresh(ctx, tx, p, row); e != nil {
		return out, e
	}
	return statusOf(row), commit(ctx, tx)
}
func (s *CredentialService) command(ctx context.Context, tx pgx.Tx, p metadata.Principal, operation, key string, payload []byte) (string, error) {
	var cid, hash string
	e := tx.QueryRow(ctx, "SELECT credential_id::text,payload_hash FROM mcpguard_ra.credential_commands WHERE org_id=$1 AND subject_id=$2 AND command=$3 AND target='' AND request_key=$4", p.OrgID, p.ID, operation, key).Scan(&cid, &hash)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", nil
	}
	if e != nil {
		return "", ErrUnavailable
	}
	if !hmac.Equal([]byte(hash), []byte(s.Access.Catalog.Store.Vault.Hash(payload))) {
		return "", ErrIdempotency
	}
	return cid, nil
}
func (s *CredentialService) recordCommand(ctx context.Context, tx pgx.Tx, p metadata.Principal, operation, key, cid string, payload []byte) error {
	_, e := tx.Exec(ctx, "INSERT INTO mcpguard_ra.credential_commands VALUES($1,$2,$3,'',$4,$5,$6)", p.OrgID, p.ID, operation, key, s.Access.Catalog.Store.Vault.Hash(payload), cid)
	return e
}

type credentialPreparation struct {
	ref       v02.CredentialReference
	selection credentialSelection
	nonce     []byte
}

func (s *CredentialService) prepareIssue(ctx context.Context, p metadata.Principal, in v02.IssueCredentialInput) (*credentialPreparation, *v02.CredentialStatus, error) {
	tx, _, e := s.Access.begin(ctx, p)
	if e != nil {
		return nil, nil, e
	}
	defer rollback(tx)
	current, e := s.selectGrant(ctx, tx, p, in.GrantID)
	if e != nil {
		return nil, nil, e
	}
	payload, _ := json.Marshal(in)
	prior, e := s.command(ctx, tx, p, "ISSUE", in.RequestKey, payload)
	if e != nil {
		return nil, nil, e
	}
	if prior != "" {
		r, e := s.row(ctx, tx, p, prior)
		if e != nil {
			return nil, nil, e
		}
		if e = s.refresh(ctx, tx, p, r); e != nil {
			return nil, nil, e
		}
		out := statusOf(r)
		return nil, &out, commit(ctx, tx)
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return nil, nil, ErrUnavailable
	}
	now = now.UTC().Truncate(time.Microsecond)
	expiry := now.Add(time.Duration(current.policy.MaxTtlSeconds) * time.Second)
	grantExpiry, _, _ := normalizedTime(current.grant.ExpiresAt)
	if expiry.After(grantExpiry) {
		expiry = grantExpiry
	}
	if !expiry.After(now) {
		return nil, nil, ErrDenied
	}
	snap := current.snapshot
	ref := v02.CredentialReference{FormatVersion: "1", CredentialID: uuid.NewString(), LeaseID: uuid.NewString(), OrgID: p.OrgID, SubjectID: p.ID, ResourceID: current.grant.ResourceID, ResourceVersion: current.grant.ResourceVersion, GrantID: in.GrantID, GrantVersion: current.grant.GrantVersion, BindingID: snap.Binding.BindingID, BindingVersion: snap.Binding.BindingVersion, ProfileID: current.grant.ProfileID, ProfileVersion: current.grant.ProfileVersion, Issuer: current.issuer.Issuer, IssuerEpoch: current.issuer.IssuerEpoch, Audience: "mcpguard-execution", IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: expiry.Format(time.RFC3339Nano)}
	tag, e := tx.Exec(ctx, "UPDATE mcpguard_ra.credential_key_usage SET use_count=use_count+1 WHERE fingerprint=$1 AND use_count<$2", current.issuer.KeyFingerprint, int64(v02.MaxPackageKeyUses))
	if e != nil || tag.RowsAffected() != 1 {
		return nil, nil, ErrUnavailable
	}
	var nonce []byte
	reserved := false
	for attempt := 0; attempt < 3; attempt++ {
		nonce, e = s.nonce()
		if e != nil || len(nonce) != v02.CredentialNonceBytes {
			return nil, nil, ErrUnavailable
		}
		tag, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.credential_nonce_reservations(fingerprint,nonce,org_id,credential_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", current.issuer.KeyFingerprint, nonce, p.OrgID, ref.CredentialID)
		if e != nil {
			return nil, nil, ErrUnavailable
		}
		if tag.RowsAffected() == 1 {
			reserved = true
			break
		}
	}
	if !reserved {
		return nil, nil, ErrUnavailable
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.credential_audit(org_id,subject_id,reservation_id,event_type) VALUES($1,$2,$3,'NONCE_RESERVED')", p.OrgID, p.ID, ref.CredentialID); e != nil {
		return nil, nil, ErrUnavailable
	}
	if e = commit(ctx, tx); e != nil {
		return nil, nil, e
	}
	return &credentialPreparation{ref, current, nonce}, nil, nil
}

// Issue returns only a non-secret summary. Bytes remain in server-only blob
// storage; RA-08 will add the separately authenticated delivery protocol.
func (s *CredentialService) Issue(ctx context.Context, p metadata.Principal, in v02.IssueCredentialInput) (v02.CredentialStatus, error) {
	var out v02.CredentialStatus
	if !valid("IssueCredentialInput", in) {
		return out, ErrInput
	}
	prep, prior, e := s.prepareIssue(ctx, p, in)
	if e != nil {
		return out, e
	}
	if prior != nil {
		return *prior, nil
	}
	blob, e := s.encryptPreparation(ctx, p, prep)
	if e != nil {
		return out, e
	}
	return s.finishIssue(ctx, p, in, prep, blob)
}
func (s *CredentialService) encryptPreparation(ctx context.Context, p metadata.Principal, prep *credentialPreparation) ([]byte, error) {
	key, e := s.Keys.Resolve(ctx, p.OrgID, prep.ref.Issuer, prep.selection.issuer.KeyID)
	if e != nil || key == nil || key.Fingerprint() != prep.selection.issuer.KeyFingerprint {
		return nil, ErrUnavailable
	}
	blob, e := encodePackage(ctx, key, prep.ref, prep.selection.issuer.KeyID, prep.nonce)
	if e != nil {
		return nil, ErrUnavailable
	}
	issued, _, e := normalizedTime(prep.ref.IssuedAt)
	if e != nil {
		return nil, ErrUnavailable
	}
	// A short wall-clock rollback can put the prepared issue time in the
	// future. Wait outside the authorization transaction so revocation can
	// proceed and the final transaction still validates current authority.
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if e = waitForDatabaseTime(wait, s.Access.Catalog.Store.Pool, issued); e != nil {
		return nil, ErrUnavailable
	}
	return blob, nil
}
func (s *CredentialService) finishIssue(ctx context.Context, p metadata.Principal, in v02.IssueCredentialInput, prep *credentialPreparation, blob []byte) (v02.CredentialStatus, error) {
	var out v02.CredentialStatus
	tx, _, e := s.Access.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	out, e = s.issueWithin(ctx, tx, p, in, prep, blob)
	if e != nil {
		return out, e
	}
	return out, commit(ctx, tx)
}

// issueWithin lets RA-08 commit package, lease, ticket and critical audit in
// one transaction. Nonce reservation remains a preceding durable transaction.
func (s *CredentialService) issueWithin(ctx context.Context, tx pgx.Tx, p metadata.Principal, in v02.IssueCredentialInput, prep *credentialPreparation, blob []byte) (v02.CredentialStatus, error) {
	var out v02.CredentialStatus
	current, e := s.selectGrant(ctx, tx, p, in.GrantID)
	if e != nil {
		return out, e
	}
	payload, _ := json.Marshal(in)
	prior, e := s.command(ctx, tx, p, "ISSUE", in.RequestKey, payload)
	if e != nil {
		return out, e
	}
	if prior != "" {
		r, e := s.row(ctx, tx, p, prior)
		if e != nil {
			return out, e
		}
		if e = s.refresh(ctx, tx, p, r); e != nil {
			return out, e
		}
		return statusOf(r), nil
	}
	ref := prep.ref
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return out, ErrUnavailable
	}
	expiry, _, _ := normalizedTime(ref.ExpiresAt)
	issued, _, _ := normalizedTime(ref.IssuedAt)
	check := &credentialRow{ref: ref, keyID: prep.selection.issuer.KeyID, fingerprint: prep.selection.issuer.KeyFingerprint, issuerVersion: prep.selection.issuer.Version, policyVersion: prep.selection.policy.Version}
	if !selectionMatches(check, current) || !expiry.After(now) {
		return out, ErrDenied
	}
	if issued.After(now) {
		return out, ErrUnavailable
	}
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.credential_leases(org_id,lease_id,credential_id,subject_id,grant_id,grant_version,resource_id,resource_version,profile_id,profile_version,binding_id,binding_version,issuer_version,policy_version,issued_at,expires_at,state,revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'ACTIVE',1)`, p.OrgID, ref.LeaseID, ref.CredentialID, p.ID, ref.GrantID, ref.GrantVersion, ref.ResourceID, ref.ResourceVersion, ref.ProfileID, ref.ProfileVersion, ref.BindingID, ref.BindingVersion, current.issuer.Version, current.policy.Version, issued, expiry)
	if e != nil {
		return out, ErrUnavailable
	}
	rawRef, _ := json.Marshal(ref)
	sum := sha256.Sum256(blob)
	_, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.credential_packages VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", p.OrgID, ref.CredentialID, ref.LeaseID, ref.Issuer, ref.IssuerEpoch, current.issuer.KeyID, current.issuer.KeyFingerprint, prep.nonce, rawRef, hex.EncodeToString(sum[:]))
	if e != nil {
		return out, ErrUnavailable
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.credential_blobs VALUES($1,$2,$3)", p.OrgID, ref.CredentialID, blob); e != nil {
		return out, ErrUnavailable
	}
	if e = s.recordCommand(ctx, tx, p, "ISSUE", in.RequestKey, ref.CredentialID, payload); e != nil {
		return out, ErrUnavailable
	}
	if e = credentialAudit(ctx, tx, p, ref.CredentialID, "PACKAGE_ISSUED"); e != nil {
		return out, ErrUnavailable
	}
	out = v02.CredentialStatus{CredentialID: ref.CredentialID, LeaseID: ref.LeaseID, ExpiresAt: ref.ExpiresAt, State: "ACTIVE", Revision: "1"}
	return out, nil
}

// Validate is an online check at this call's transaction boundary, not an
// execution ticket. It never exposes plaintext reference metadata or secrets.
func (s *CredentialService) Validate(ctx context.Context, p metadata.Principal, resourceID, credentialID string, blob []byte) (v02.CredentialStatus, error) {
	var out v02.CredentialStatus
	if !valid("CredentialIDInput", v02.CredentialIDInput{CredentialID: credentialID}) || !valid("GetResourceInput", v02.GetResourceInput{ResourceID: resourceID}) {
		return out, ErrCredential
	}
	tx, _, e := s.Access.begin(ctx, p)
	if e != nil {
		if errors.Is(e, ErrDenied) {
			e = ErrCredential
		}
		return out, e
	}
	defer rollback(tx)
	row, e := s.row(ctx, tx, p, credentialID)
	if e != nil {
		if errors.Is(e, ErrDenied) {
			e = ErrCredential
		}
		return out, e
	}
	if e = s.refresh(ctx, tx, p, row); e != nil {
		return out, e
	}
	if row.state != "ACTIVE" || row.ref.ResourceID != resourceID {
		if e = commit(ctx, tx); e != nil {
			return out, e
		}
		return out, ErrCredential
	}
	if e = commit(ctx, tx); e != nil {
		return out, e
	}
	env, nonce, encrypted, e := parsePackage(blob)
	if e != nil {
		return out, ErrCredential
	}
	if env.Issuer != row.ref.Issuer || env.KeyID != row.keyID || !bytes.Equal(nonce, row.nonce) {
		return out, ErrCredential
	}
	key, e := s.Keys.Resolve(ctx, p.OrgID, env.Issuer, env.KeyID)
	if e != nil {
		if errors.Is(e, ErrCredential) {
			return out, ErrCredential
		}
		return out, ErrUnavailable
	}
	if key == nil || key.Fingerprint() != row.fingerprint {
		return out, ErrUnavailable
	}
	ref, e := openPackage(ctx, key, env, nonce, encrypted)
	if e != nil {
		return out, e
	}
	sum := sha256.Sum256(blob)
	if ref != row.ref || !hmac.Equal([]byte(hex.EncodeToString(sum[:])), []byte(row.digest)) {
		return out, ErrCredential
	}
	final, _, e := s.Access.begin(ctx, p)
	if e != nil {
		if errors.Is(e, ErrDenied) {
			e = ErrCredential
		}
		return out, e
	}
	defer rollback(final)
	current, e := s.row(ctx, final, p, credentialID)
	if e != nil {
		return out, e
	}
	if e = s.refresh(ctx, final, p, current); e != nil {
		return out, e
	}
	if current.state != "ACTIVE" || current.ref != ref || current.digest != row.digest {
		if e = commit(ctx, final); e != nil {
			return out, e
		}
		return out, ErrCredential
	}
	if e = credentialAudit(ctx, final, p, credentialID, "PACKAGE_VALIDATED"); e != nil {
		return out, ErrUnavailable
	}
	return statusOf(current), commit(ctx, final)
}
func (s *CredentialService) Revoke(ctx context.Context, p metadata.Principal, in v02.RevokeCredentialInput) (v02.CredentialStatus, error) {
	var out v02.CredentialStatus
	if !valid("RevokeCredentialInput", in) {
		return out, ErrInput
	}
	tx, _, e := s.Access.begin(ctx, p)
	if e != nil {
		return out, e
	}
	defer rollback(tx)
	row, e := s.row(ctx, tx, p, in.CredentialID)
	if e != nil {
		return out, e
	}
	raw, _ := json.Marshal(in)
	prior, e := s.command(ctx, tx, p, "REVOKE", in.RequestKey, raw)
	if e != nil {
		return out, e
	}
	if prior != "" {
		return statusOf(row), commit(ctx, tx)
	}
	if strconv.FormatInt(row.revision, 10) != in.ExpectedRevision {
		return out, ErrConflict
	}
	if row.state == "ACTIVE" {
		if e = s.transition(ctx, tx, p, row, "REVOKED", in.Reason); e != nil {
			return out, e
		}
	}
	if e = s.recordCommand(ctx, tx, p, "REVOKE", in.RequestKey, in.CredentialID, raw); e != nil {
		return out, ErrUnavailable
	}
	return statusOf(row), commit(ctx, tx)
}
