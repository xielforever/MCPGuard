package resources

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"strconv"
	"strings"
	"time"
)

type Provisioner struct {
	Access   *AccessService
	Private  *ProviderPrivate
	Provider NativeProvisioningProvider
}
type provisionWork struct {
	Plan       v02.ProvisioningPlan
	Target     v02.ProvisioningTarget
	Secret     NativeSecret
	Hash       string
	LeaseOwner string
	Generation int64
	Recovered  bool
	Receipt    *NativeReceipt
}

func NewProvisioner(c *Catalog, private *ProviderPrivate, provider NativeProvisioningProvider) (*Provisioner, error) {
	if c == nil || c.SchemaVersion < 3 || private == nil || provider == nil {
		return nil, ErrUnavailable
	}
	var allowed bool
	e := c.Store.Pool.QueryRow(context.Background(), `SELECT NOT(rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls) AND has_table_privilege(current_user,'mcpguard_ra.provisioning_jobs','INSERT') AND NOT has_table_privilege(current_user,'mcpguard.managed_actions','INSERT') FROM pg_roles WHERE rolname=current_user`).Scan(&allowed)
	if e != nil || !allowed {
		return nil, ErrDenied
	}
	a, e := NewAccessService(c)
	if e != nil {
		return nil, e
	}
	return &Provisioner{a, private, provider}, nil
}
func (w *Provisioner) begin(ctx context.Context, org string) (pgx.Tx, error) {
	tx, e := w.Access.Catalog.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, ErrUnavailable
	}
	var epoch int64
	if e = tx.QueryRow(ctx, "SELECT mcpguard.lock_auth_epoch_exclusive($1)", org).Scan(&epoch); e != nil {
		rollback(tx)
		return nil, ErrUnavailable
	}
	return tx, nil
}
func provisionAudit(ctx context.Context, tx pgx.Tx, org, job, event string) error {
	_, e := tx.Exec(ctx, "INSERT INTO mcpguard_ra.provision_audit(org_id,job_id,event_type) VALUES($1,$2,$3)", org, job, event)
	return e
}
func safeJob(ctx context.Context, tx pgx.Tx, org, grant string) (*v02.ProvisioningJob, error) {
	out := &v02.ProvisioningJob{GrantID: grant}
	e := tx.QueryRow(ctx, "SELECT job_id::text,plan_hash,state FROM mcpguard_ra.provisioning_jobs WHERE org_id=$1 AND grant_id=$2", org, grant).Scan(&out.JobID, &out.AccountPlanHash, &out.State)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	if e = publicRecovery(ctx, tx, org, out); e != nil {
		return nil, e
	}
	return out, nil
}
func (w *Provisioner) prepare(ctx context.Context, org, grant string) (*provisionWork, *v02.ProvisioningJob, error) {
	if _, e := uuid.Parse(org); e != nil {
		return nil, nil, ErrInput
	}
	if !valid("GrantIDInput", v02.GrantIDInput{GrantID: grant}) {
		return nil, nil, ErrInput
	}
	tx, e := w.begin(ctx, org)
	if e != nil {
		return nil, nil, e
	}
	defer rollback(tx)
	g, e := w.Access.readGrant(ctx, tx, org, grant)
	if e != nil {
		return nil, nil, e
	}
	old, e := safeJob(ctx, tx, org, grant)
	if e != nil {
		return nil, nil, e
	}
	if old != nil {
		return nil, old, commit(ctx, tx)
	}
	if g.State != "PROVISIONING" || g.GrantVersion != "1" {
		return nil, nil, ErrConflict
	}
	r, e := w.Access.request(ctx, tx, org, g.RequestID)
	if e != nil {
		return nil, nil, e
	}
	yes, e := w.Access.qualified(ctx, tx, r)
	if e != nil {
		return nil, nil, e
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return nil, nil, e
	}
	expiry, _, e := normalizedTime(g.ExpiresAt)
	if e != nil || !expiry.After(now) || !yes {
		return nil, nil, ErrDenied
	}
	var raw []byte
	var enabled bool
	var target v02.ProvisioningTarget
	e = tx.QueryRow(ctx, `SELECT definition,enabled FROM mcpguard_ra.provision_targets WHERE org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5`, org, g.ResourceID, g.ResourceVersion, g.ProfileID, g.ProfileVersion).Scan(&raw, &enabled)
	if e != nil || !enabled || v02.Decode("ProvisioningTarget", raw, &target) != nil {
		return nil, nil, ErrDenied
	}
	ep, ok := w.Private.Endpoints[target.EndpointConfigRef]
	if !ok || ep.Fingerprint() != target.TargetFingerprint {
		return nil, nil, ErrDenied
	}
	account := uuid.NewString()
	plan := v02.ProvisioningPlan{OrgID: org, ResourceID: g.ResourceID, ResourceVersion: g.ResourceVersion, ProfileID: g.ProfileID, ProfileVersion: g.ProfileVersion, JobID: uuid.NewString(), GrantID: grant, GrantVersion: "1", SubjectID: g.SubjectID, AccountID: account, BindingID: uuid.NewString(), TargetID: target.TargetID, TargetVersion: target.Version, EndpointConfigRef: target.EndpointConfigRef, TargetFingerprint: target.TargetFingerprint, ManagementMode: "PROVISIONED", NativeIdentity: "mg_" + strings.ReplaceAll(account, "-", ""), SecretRef: "native." + strings.ReplaceAll(account, "-", ""), SecretVersion: "1", ExpiresAt: g.ExpiresAt}
	var registration v02.ExistingAccountRegistration
	e = tx.QueryRow(ctx, `SELECT definition,enabled FROM mcpguard_ra.existing_account_registrations WHERE org_id=$1 AND subject_id=$2 AND resource_id=$3 AND resource_version=$4 AND profile_id=$5 AND profile_version=$6`, org, g.SubjectID, g.ResourceID, g.ResourceVersion, g.ProfileID, g.ProfileVersion).Scan(&raw, &enabled)
	var secret NativeSecret
	if e == nil {
		if !enabled || v02.Decode("ExistingAccountRegistration", raw, &registration) != nil {
			return nil, nil, ErrDenied
		}
		plan.ManagementMode = "PREEXISTING"
		plan.AccountID = registration.AccountID
		plan.NativeIdentity = registration.NativeIdentity
		plan.SecretRef = registration.SecretRef
		plan.SecretVersion = registration.SecretVersion
		v := registration.Version
		plan.RegistrationVersion = &v
		secret, ok = w.Private.ExistingSecrets[plan.SecretRef+"@"+plan.SecretVersion]
		if !ok || secret.Username != plan.NativeIdentity || secret.Version != plan.SecretVersion {
			return nil, nil, ErrDenied
		}
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, nil, ErrUnavailable
	} else {
		if _, ok = w.Private.Managers[plan.EndpointConfigRef]; !ok {
			return nil, nil, ErrDenied
		}
		random := make([]byte, 32)
		if _, e = rand.Read(random); e != nil {
			return nil, nil, ErrUnavailable
		}
		secret = NativeSecret{Username: plan.NativeIdentity, Password: base64.RawURLEncoding.EncodeToString(random), Version: "1"}
	}
	var latest int64
	if e = tx.QueryRow(ctx, "SELECT coalesce(max(binding_version),0) FROM mcpguard_ra.identity_bindings WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", org, g.SubjectID, g.ResourceID).Scan(&latest); e != nil {
		return nil, nil, e
	}
	plan.BindingVersion = strconv.FormatInt(latest+1, 10)
	if !valid("ProvisioningPlan", plan) {
		return nil, nil, ErrInput
	}
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.account_claims VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, org, plan.AccountID, g.SubjectID, g.ResourceID, g.ResourceVersion, g.ProfileID, g.ProfileVersion, plan.TargetFingerprint, plan.NativeIdentity, plan.ManagementMode)
	if e != nil {
		return nil, nil, ErrConflict
	}
	var match bool
	e = tx.QueryRow(ctx, `SELECT subject_id=$3 AND resource_id=$4 AND resource_version=$5 AND profile_id=$6 AND profile_version=$7 AND target_fingerprint=$8 AND native_identity=$9 AND management_mode=$10 FROM mcpguard_ra.account_claims WHERE org_id=$1 AND account_id=$2`, org, plan.AccountID, g.SubjectID, g.ResourceID, g.ResourceVersion, g.ProfileID, g.ProfileVersion, plan.TargetFingerprint, plan.NativeIdentity, plan.ManagementMode).Scan(&match)
	if e != nil || !match {
		return nil, nil, ErrConflict
	}
	data, _ := json.Marshal(secret)
	vault := w.Access.Catalog.Store.Vault
	cipher := vault.Seal(data, "native-secret/"+org+"/"+plan.AccountID+"/"+plan.SecretVersion)
	_, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.native_secrets VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", org, plan.AccountID, plan.SecretRef, plan.SecretVersion, cipher)
	if e != nil {
		return nil, nil, e
	}
	var saved []byte
	var ref string
	if e = tx.QueryRow(ctx, "SELECT ciphertext,secret_ref FROM mcpguard_ra.native_secrets WHERE org_id=$1 AND account_id=$2 AND version=$3", org, plan.AccountID, plan.SecretVersion).Scan(&saved, &ref); e != nil {
		return nil, nil, e
	}
	plain, e := vault.Open(saved, "native-secret/"+org+"/"+plan.AccountID+"/"+plan.SecretVersion)
	if e != nil || string(plain) != string(data) || ref != plan.SecretRef {
		return nil, nil, ErrConflict
	}
	raw, _ = json.Marshal(plan)
	hash := vault.Hash(raw)
	encrypted := vault.Seal(raw, "provision-plan/"+org+"/"+plan.JobID)
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.provisioning_jobs(org_id,job_id,grant_id,grant_version,account_id,plan_hash,plan_ciphertext,state) VALUES($1,$2,$3,1,$4,$5,$6,'EXECUTING')`, org, plan.JobID, grant, plan.AccountID, hash, encrypted)
	if e != nil {
		return nil, nil, ErrConflict
	}
	work := &provisionWork{Plan: plan, Target: target, Secret: secret, Hash: hash}
	if w.Access.Catalog.SchemaVersion >= 4 {
		if e = w.initialLease(ctx, tx, work); e != nil {
			return nil, nil, e
		}
	}
	if e = provisionAudit(ctx, tx, org, plan.JobID, "PROVISIONING_INTENT"); e != nil {
		return nil, nil, e
	}
	if e = commit(ctx, tx); e != nil {
		return nil, nil, e
	}
	return work, nil, nil
}
func (w *Provisioner) finish(ctx context.Context, work *provisionWork, observation v02.NativeObservation) error {
	plan := work.Plan
	tx, e := w.begin(ctx, plan.OrgID)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if e = w.leaseContext(ctx, tx, work); e != nil {
		return e
	}
	g, e := w.Access.readGrant(ctx, tx, plan.OrgID, plan.GrantID)
	if e != nil {
		return e
	}
	r, e := w.Access.request(ctx, tx, plan.OrgID, g.RequestID)
	if e != nil {
		return e
	}
	yes, e := w.Access.qualified(ctx, tx, r)
	if e != nil {
		return e
	}
	now, e := databaseTime(ctx, tx)
	if e != nil {
		return e
	}
	expiry, _, _ := normalizedTime(g.ExpiresAt)
	if !yes || !expiry.After(now) || (g.State != "PROVISIONING" && !(work.Recovered && g.State == "PROVISIONING_UNKNOWN")) || g.GrantVersion != "1" {
		return ErrDenied
	}
	var targetVersion string
	var enabled bool
	e = tx.QueryRow(ctx, "SELECT version::text,enabled FROM mcpguard_ra.provision_targets WHERE org_id=$1 AND resource_id=$2 AND resource_version=$3 AND profile_id=$4 AND profile_version=$5", plan.OrgID, plan.ResourceID, plan.ResourceVersion, plan.ProfileID, plan.ProfileVersion).Scan(&targetVersion, &enabled)
	if e != nil || !enabled || targetVersion != plan.TargetVersion {
		return ErrDenied
	}
	var latest int64
	if e = tx.QueryRow(ctx, "SELECT coalesce(max(binding_version),0) FROM mcpguard_ra.identity_bindings WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", plan.OrgID, plan.SubjectID, plan.ResourceID).Scan(&latest); e != nil {
		return e
	}
	if number(plan.BindingVersion) != latest+1 {
		return ErrConflict
	}
	observation.ObservationID = uuid.NewString()
	if observation.AccountID != plan.AccountID || observation.NativeIdentity != plan.NativeIdentity || !valid("NativeObservation", observation) {
		return ErrDenied
	}
	if work.Recovered {
		if work.Receipt == nil || work.Receipt.Status != "OWNED" || work.Receipt.PlanHash != work.Hash || work.Receipt.RoleOID == nil || *work.Receipt.RoleOID != observation.RoleOid {
			return ErrDenied
		}
		if e = w.saveReceipt(ctx, tx, work); e != nil {
			return e
		}
	}
	var priorOID string
	e = tx.QueryRow(ctx, "SELECT role_oid::text FROM mcpguard_ra.native_observations WHERE org_id=$1 AND account_id=$2 ORDER BY observed_at LIMIT 1", plan.OrgID, plan.AccountID).Scan(&priorOID)
	if e == nil && priorOID != observation.RoleOid {
		return ErrDenied
	}
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	raw, _ := json.Marshal(observation)
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.native_observations(org_id,observation_id,account_id,job_id,role_oid,permission_hash,definition) VALUES($1,$2,$3,$4,$5,$6,$7)", plan.OrgID, observation.ObservationID, plan.AccountID, plan.JobID, observation.RoleOid, observation.PermissionHash, raw); e != nil {
		return e
	}
	account := v02.NativeAccount{AccountID: plan.AccountID, OrgID: plan.OrgID, ResourceID: plan.ResourceID, ResourceVersion: plan.ResourceVersion, SubjectID: plan.SubjectID, ProfileID: plan.ProfileID, ProfileVersion: plan.ProfileVersion, NativeIdentity: plan.NativeIdentity, ManagementMode: plan.ManagementMode, ObservedPermissionRef: observation.ObservationID}
	raw, _ = json.Marshal(account)
	if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_ra.native_accounts VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING", plan.OrgID, plan.AccountID, plan.SubjectID, plan.ResourceID, plan.ResourceVersion, plan.ProfileID, plan.ProfileVersion, plan.NativeIdentity, plan.ManagementMode, raw); e != nil {
		return e
	}
	accountID := plan.AccountID
	binding := v02.CredentialBinding{BindingID: plan.BindingID, OrgID: plan.OrgID, PrincipalID: plan.SubjectID, ResourceID: plan.ResourceID, ResourceVersion: plan.ResourceVersion, ProfileID: plan.ProfileID, ProfileVersion: plan.ProfileVersion, BindingVersion: plan.BindingVersion, NativeIdentity: plan.NativeIdentity, SecretRef: plan.SecretRef, SecretVersion: plan.SecretVersion, Enabled: true, AccountID: &accountID}
	raw, _ = json.Marshal(binding)
	if _, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.identity_bindings VALUES($1,$2,$3,$4,$5,$6,$7,$8,true,$9)`, plan.OrgID, plan.SubjectID, plan.ResourceID, plan.BindingID, plan.BindingVersion, plan.ResourceVersion, plan.ProfileID, plan.ProfileVersion, raw); e != nil {
		return e
	}
	verified, e := tx.Exec(ctx, "UPDATE mcpguard_ra.provisioning_jobs SET state='VERIFIED' WHERE org_id=$1 AND job_id=$2 AND state IN ('EXECUTING','OUTCOME_UNKNOWN')", plan.OrgID, plan.JobID)
	if e != nil {
		return e
	}
	if verified.RowsAffected() != 1 {
		return ErrConflict
	}
	_, e = tx.Exec(ctx, `INSERT INTO mcpguard_ra.access_grant_versions(org_id,grant_id,version,request_id,decision_id,subject_id,resource_id,resource_version,profile_id,profile_version,expires_at,account_id,binding_id,binding_version,observation_id,activation_job_id) VALUES($1,$2,2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, plan.OrgID, plan.GrantID, g.RequestID, g.DecisionID, plan.SubjectID, plan.ResourceID, plan.ResourceVersion, plan.ProfileID, plan.ProfileVersion, expiry, plan.AccountID, plan.BindingID, plan.BindingVersion, observation.ObservationID, plan.JobID)
	if e != nil {
		return e
	}
	var epoch int64
	if e = tx.QueryRow(ctx, "SELECT mcpguard_ra.advance_access_epoch($1)", plan.OrgID).Scan(&epoch); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, "UPDATE mcpguard_ra.access_grant_heads SET version=2,state='ACTIVE',revision=revision+1,auth_epoch=$3 WHERE org_id=$1 AND grant_id=$2 AND version=1 AND state=$4", plan.OrgID, plan.GrantID, epoch, g.State)
	if e != nil || tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if e = provisionAudit(ctx, tx, plan.OrgID, plan.JobID, "NATIVE_VERIFIED_GRANT_ACTIVE"); e != nil {
		return e
	}
	if e = w.releaseLease(ctx, tx, work, "ACTIVATED", "NOT_REQUIRED", 0); e != nil {
		return e
	}
	return commit(ctx, tx)
}
func (w *Provisioner) failed(ctx context.Context, work *provisionWork, jobState, grantState, code string) error {
	plan := work.Plan
	tx, e := w.begin(ctx, plan.OrgID)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if e = w.leaseContext(ctx, tx, work); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, "UPDATE mcpguard_ra.provisioning_jobs SET state=$3,failure_code=$4 WHERE org_id=$1 AND job_id=$2 AND state='EXECUTING'", plan.OrgID, plan.JobID, jobState, code)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return commit(ctx, tx)
	}
	var epoch int64
	if e = tx.QueryRow(ctx, "SELECT mcpguard_ra.advance_access_epoch($1)", plan.OrgID).Scan(&epoch); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE mcpguard_ra.access_grant_heads SET state=$3,revision=revision+1,auth_epoch=$4 WHERE org_id=$1 AND grant_id=$2 AND state='PROVISIONING'", plan.OrgID, plan.GrantID, grantState, epoch); e != nil {
		return e
	}
	if e = provisionAudit(ctx, tx, plan.OrgID, plan.JobID, code); e != nil {
		return e
	}
	cleanup := "NOT_REQUIRED"
	if jobState == "CLEANUP_PENDING" {
		cleanup = "PENDING"
	}
	if e = w.releaseLease(ctx, tx, work, code, cleanup, time.Duration(v02.RecoveryBackoffSeconds)*time.Second); e != nil {
		return e
	}
	return commit(ctx, tx)
}
func (w *Provisioner) Run(ctx context.Context, org, grant string) (v02.ProvisioningJob, error) {
	work, prior, e := w.prepare(ctx, org, grant)
	if e != nil {
		return v02.ProvisioningJob{}, e
	}
	if prior != nil {
		return *prior, nil
	}
	plan := work.Plan
	call, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	created := false
	if plan.ManagementMode == "PROVISIONED" {
		e = w.Provider.Create(call, work.Target, plan, work.Hash, work.Secret)
		created = e == nil
	}
	var observation v02.NativeObservation
	if e == nil {
		observation, e = w.Provider.Verify(call, plan, work.Secret)
	}
	if e == nil {
		e = w.finish(call, work, observation)
	}
	if e != nil {
		finish, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		jobState, grantState, code := "OUTCOME_UNKNOWN", "PROVISIONING_UNKNOWN", "PROVISIONING_OUTCOME_UNKNOWN"
		if plan.ManagementMode == "PREEXISTING" {
			jobState, grantState, code = "FAILED", "PROVISIONING_FAILED", "EXISTING_ACCOUNT_VERIFICATION_FAILED"
		}
		if created {
			jobState, grantState, code = "CLEANUP_PENDING", "PROVISIONING_FAILED", "NATIVE_VERIFICATION_OR_ACTIVATION_FAILED"
			if w.Access.Catalog.SchemaVersion < 4 {
				_ = w.Provider.Disable(finish, plan, work.Hash)
			}
		}
		if errors.Is(e, ErrDenied) && created {
			grantState = "REVOKED"
		}
		if failed := w.failed(finish, work, jobState, grantState, code); failed != nil {
			return v02.ProvisioningJob{}, ErrUnavailable
		}
		// Only a committed terminal/cleanup decision authorizes compensation.
		// A stale dispatch must never disable an account recovered as ACTIVE.
		if created && w.Access.Catalog.SchemaVersion >= 4 {
			_ = w.Provider.Disable(finish, plan, work.Hash)
		}
	}
	readCtx, readDone := context.WithTimeout(context.Background(), 5*time.Second)
	defer readDone()
	tx, err := w.begin(readCtx, org)
	if err != nil {
		return v02.ProvisioningJob{}, ErrUnavailable
	}
	defer rollback(tx)
	out, err := safeJob(readCtx, tx, org, grant)
	if err != nil || out == nil {
		return v02.ProvisioningJob{}, ErrUnavailable
	}
	return *out, commit(readCtx, tx)
}

// RunOnce is bounded and explicit. It never executes a business operation.
func (w *Provisioner) RunOnce(ctx context.Context) ([]v02.ProvisioningJob, error) {
	rows, e := w.Access.Catalog.Store.Pool.Query(ctx, `SELECT h.org_id::text,h.grant_id::text FROM mcpguard_ra.access_grant_heads h JOIN mcpguard_ra.access_grant_versions v ON (v.org_id,v.grant_id,v.version)=(h.org_id,h.grant_id,h.version) JOIN mcpguard_ra.provision_targets t ON (t.org_id,t.resource_id,t.resource_version,t.profile_id,t.profile_version)=(v.org_id,v.resource_id,v.resource_version,v.profile_id,v.profile_version) WHERE h.state='PROVISIONING' AND t.enabled AND NOT EXISTS(SELECT FROM mcpguard_ra.provisioning_jobs j WHERE (j.org_id,j.grant_id)=(h.org_id,h.grant_id)) ORDER BY h.org_id,h.grant_id LIMIT $1`, v02.MaxProvisioningBatch)
	if e != nil {
		return nil, ErrUnavailable
	}
	type target struct{ org, grant string }
	targets := []target{}
	for rows.Next() {
		var t target
		if rows.Scan(&t.org, &t.grant) != nil {
			rows.Close()
			return nil, ErrUnavailable
		}
		targets = append(targets, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []v02.ProvisioningJob{}
	for _, t := range targets {
		job, e := w.Run(ctx, t.org, t.grant)
		if errors.Is(e, ErrDenied) || errors.Is(e, ErrConflict) {
			continue
		}
		if e != nil {
			return out, e
		}
		out = append(out, job)
	}
	return out, nil
}

type VaultSecretProvider struct {
	Catalog   *Catalog
	Endpoints map[string]Endpoint
}

func (v *VaultSecretProvider) Resolve(ctx context.Context, endpointRef, secretRef, version string) (Endpoint, NativeSecret, error) {
	ep, ok := v.Endpoints[endpointRef]
	if !ok {
		return Endpoint{}, NativeSecret{}, ErrDenied
	}
	var org, subject, resource, account, fp string
	var ciphertext []byte
	e := v.Catalog.Store.Pool.QueryRow(ctx, `SELECT s.org_id::text,c.subject_id::text,c.resource_id::text,c.account_id::text,c.target_fingerprint,s.ciphertext FROM mcpguard_ra.native_secrets s JOIN mcpguard_ra.account_claims c ON (c.org_id,c.account_id)=(s.org_id,s.account_id) WHERE s.secret_ref=$1 AND s.version=$2`, secretRef, version).Scan(&org, &subject, &resource, &account, &fp, &ciphertext)
	if e != nil || fp != ep.Fingerprint() {
		return Endpoint{}, NativeSecret{}, ErrDenied
	}
	p, e := v.Catalog.Store.OAuthPrincipal(ctx, org, subject)
	if e != nil {
		return Endpoint{}, NativeSecret{}, ErrDenied
	}
	snap, e := v.Catalog.Snapshot(ctx, p, resource)
	if e != nil || snap.Binding.AccountID == nil || *snap.Binding.AccountID != account || snap.Binding.SecretRef != secretRef || snap.Binding.SecretVersion != version {
		return Endpoint{}, NativeSecret{}, ErrDenied
	}
	plain, e := v.Catalog.Store.Vault.Open(ciphertext, "native-secret/"+org+"/"+account+"/"+version)
	var secret NativeSecret
	if e != nil || json.Unmarshal(plain, &secret) != nil || secret.Username != snap.Binding.NativeIdentity || secret.Version != version {
		return Endpoint{}, NativeSecret{}, ErrUnavailable
	}
	return ep, secret, nil
}

var _ SecretProvider = (*VaultSecretProvider)(nil)
