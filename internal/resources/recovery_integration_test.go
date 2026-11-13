package resources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

func newRecoveryFixture(t *testing.T) *provisionFixture {
	t.Helper()
	path := os.Getenv("MCPGUARD_RA_CONFIG")
	if path == "" {
		t.Skip("explicit RA-06 fixture required")
	}
	raw, e := os.ReadFile(path)
	var flags struct {
		Recovery bool `json:"raRecoveryPhase"`
	}
	if e != nil || json.Unmarshal(raw, &flags) != nil {
		t.Fatal("invalid recovery fixture")
	}
	if !flags.Recovery {
		t.Skip("explicit RA-06 fixture required")
	}
	return newProvisionFixture(t)
}
func recoveryDue(t *testing.T, f *provisionFixture, out v02.AccessStatusResult) {
	t.Helper()
	tx, e := f.admin.Begin(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollback(tx)
	// Fault injection uses only the isolated fixture administrator. Workers do
	// not have DDL rights or an API for shortening leases/backoff.
	if _, e = tx.Exec(f.ctx, "ALTER TABLE mcpguard_ra.provision_recovery DISABLE TRIGGER provision_recovery_guard"); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(f.ctx, `UPDATE mcpguard_ra.provision_recovery l SET lease_until=clock_timestamp()-interval '1 second',next_attempt_at=clock_timestamp()-interval '1 second' FROM mcpguard_ra.provisioning_jobs j WHERE (j.org_id,j.job_id)=(l.org_id,l.job_id) AND j.org_id=$1 AND j.grant_id=$2`, out.Request.OrgID, out.Grant.GrantID); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(f.ctx, "ALTER TABLE mcpguard_ra.provision_recovery ENABLE TRIGGER provision_recovery_guard"); e != nil {
		t.Fatal(e)
	}
	if tx.Commit(f.ctx) != nil {
		t.Fatal("fault injection commit failed")
	}
}
func frozenWork(t *testing.T, f *provisionFixture, out v02.AccessStatusResult) *provisionWork {
	t.Helper()
	tx, e := f.worker.begin(f.ctx, out.Request.OrgID)
	if e != nil {
		t.Fatal(e)
	}
	defer rollback(tx)
	work := &provisionWork{Plan: v02.ProvisioningPlan{OrgID: out.Request.OrgID, GrantID: out.Grant.GrantID}}
	var cipher []byte
	if e = tx.QueryRow(f.ctx, "SELECT job_id::text,account_id::text,plan_hash,plan_ciphertext FROM mcpguard_ra.provisioning_jobs WHERE org_id=$1 AND grant_id=$2", out.Request.OrgID, out.Grant.GrantID).Scan(&work.Plan.JobID, &work.Plan.AccountID, &work.Hash, &cipher); e != nil {
		t.Fatal(e)
	}
	if e = f.worker.loadFrozenWork(f.ctx, tx, work, cipher); e != nil {
		t.Fatal(e)
	}
	return work
}
func recovered(t *testing.T, f *provisionFixture, out v02.AccessStatusResult, state string) v02.ProvisioningJob {
	t.Helper()
	job, e := f.worker.Recover(f.ctx, out.Request.OrgID, out.Grant.GrantID)
	if e != nil {
		t.Fatal("recovery failed: ", e)
	}
	if job.State != state {
		t.Fatalf("recovery state %s, expected %s", job.State, state)
	}
	return job
}
func recoveryStatus(t *testing.T, f *provisionFixture, out v02.AccessStatusResult) v02.AccessStatusResult {
	t.Helper()
	p := f.people["alice"]
	if out.Grant.SubjectID != p.ID {
		p = f.people["bob"]
	}
	status, e := f.s.Status(f.ctx, p, out.Request.RequestID)
	if e != nil {
		t.Fatal(e)
	}
	return status
}
func assertOwnedCleaned(t *testing.T, f *provisionFixture, work *provisionWork, index int) {
	t.Helper()
	var safe bool
	if e := f.native[index].QueryRow(f.ctx, `SELECT NOT rolcanlogin AND NOT has_table_privilege(rolname,'resource_demo.items','SELECT') AND NOT has_schema_privilege(rolname,'resource_demo','USAGE') FROM pg_roles WHERE rolname=$1`, work.Plan.NativeIdentity).Scan(&safe); e != nil || !safe {
		t.Fatal("owned account cleanup lacks native proof")
	}
}

type recoveryFaultProvider struct {
	NativeProvisioningProvider
	NativeRecoveryProvider
	creates, inspects, cleanups, disables atomic.Int32
	failCleanup                           bool
	loseCleanupReceipt                    bool
	failInspect                           bool
	afterInspect                          func()
	rejectVerify                          bool
}

func (p *recoveryFaultProvider) Create(ctx context.Context, t v02.ProvisioningTarget, plan v02.ProvisioningPlan, h string, s NativeSecret) error {
	p.creates.Add(1)
	return p.NativeProvisioningProvider.Create(ctx, t, plan, h, s)
}
func (p *recoveryFaultProvider) Disable(ctx context.Context, plan v02.ProvisioningPlan, h string) error {
	p.disables.Add(1)
	return p.NativeProvisioningProvider.Disable(ctx, plan, h)
}
func (p *recoveryFaultProvider) Verify(ctx context.Context, plan v02.ProvisioningPlan, s NativeSecret) (v02.NativeObservation, error) {
	if p.rejectVerify {
		return v02.NativeObservation{}, ErrDenied
	}
	return p.NativeProvisioningProvider.Verify(ctx, plan, s)
}
func (p *recoveryFaultProvider) Reconcile(ctx context.Context, plan v02.ProvisioningPlan, h string) (NativeReceipt, error) {
	p.inspects.Add(1)
	if p.failInspect {
		return NativeReceipt{}, ErrUnavailable
	}
	r, e := p.NativeRecoveryProvider.Reconcile(ctx, plan, h)
	if e == nil && p.afterInspect != nil {
		p.afterInspect()
	}
	return r, e
}
func (p *recoveryFaultProvider) Cleanup(ctx context.Context, plan v02.ProvisioningPlan, h string) (NativeReceipt, error) {
	p.cleanups.Add(1)
	if p.failCleanup {
		return NativeReceipt{}, ErrUnavailable
	}
	r, e := p.NativeRecoveryProvider.Cleanup(ctx, plan, h)
	if e == nil && p.loseCleanupReceipt {
		return NativeReceipt{}, ErrUnavailable
	}
	return r, e
}
func faults(f *provisionFixture) *recoveryFaultProvider {
	p := &PGProvisioningProvider{f.private}
	return &recoveryFaultProvider{NativeProvisioningProvider: p, NativeRecoveryProvider: p}
}

// This entry point exists only in test binaries. No production crash hooks,
// caller-selected management operation or marker path are exposed by the CLI.
func TestRecoveryCrashProcessHelper(t *testing.T) {
	mode := os.Getenv("MCPGUARD_RECOVERY_HELPER")
	if mode == "" {
		return
	}
	key, e := base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_ENCRYPTION_KEY"))
	if e != nil {
		t.Fatal("private helper key unavailable")
	}
	planKey, e := base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_PLAN_KEY"))
	if e != nil {
		t.Fatal("private helper key unavailable")
	}
	vault, e := metadata.NewVault(key, planKey)
	if e != nil {
		t.Fatal("private helper vault unavailable")
	}
	ctx := context.Background()
	store, e := metadata.Open(ctx, os.Getenv("MCPGUARD_TEST_PROVISIONER_DSN"), vault)
	if e != nil {
		t.Fatal("helper metadata unavailable")
	}
	defer store.Close()
	c, e := NewCatalog(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	private, e := LoadProviderPrivate(os.Getenv("MCPGUARD_RA_PROVISION_CONFIG"))
	if e != nil {
		t.Fatal(e)
	}
	w, e := NewProvisioner(c, private, &PGProvisioningProvider{private})
	if e != nil {
		t.Fatal(e)
	}
	org, grant := os.Getenv("MCPGUARD_RECOVERY_ORG"), os.Getenv("MCPGUARD_RECOVERY_GRANT")
	var work *provisionWork
	if mode == "after_cleanup" {
		claim, e := w.claimRecovery(ctx, org, grant)
		if e != nil || claim == nil || claim.operation != "CLEANUP" {
			t.Fatal("helper cleanup claim failed")
		}
		work = claim.work
		if _, e = w.Provider.(NativeRecoveryProvider).Cleanup(ctx, work.Plan, work.Hash); e != nil {
			t.Fatal(e)
		}
	} else {
		work, _, e = w.prepare(ctx, org, grant)
		if e != nil || work == nil {
			t.Fatal("helper dispatch claim failed")
		}
		if mode != "before_dispatch" {
			if e = w.Provider.Create(ctx, work.Target, work.Plan, work.Hash, work.Secret); e != nil {
				t.Fatal(e)
			}
		}
		if mode == "after_verify" {
			if _, e = w.Provider.Verify(ctx, work.Plan, work.Secret); e != nil {
				t.Fatal(e)
			}
		}
	}
	raw, _ := json.Marshal(map[string]string{"jobId": work.Plan.JobID, "accountId": work.Plan.AccountID, "planHash": work.Hash})
	if os.WriteFile(os.Getenv("MCPGUARD_RECOVERY_MARKER"), raw, 0600) != nil {
		t.Fatal("helper checkpoint unavailable")
	}
	<-time.NewTimer(10 * time.Minute).C
}
func crashHelper(t *testing.T, f *provisionFixture, out v02.AccessStatusResult, mode string) {
	t.Helper()
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(t.TempDir(), "checkpoint.json")
	cmd := exec.Command(binary, "-test.run=^TestRecoveryCrashProcessHelper$")
	cmd.Env = append(os.Environ(), "MCPGUARD_RECOVERY_HELPER="+mode, "MCPGUARD_RECOVERY_ORG="+out.Request.OrgID, "MCPGUARD_RECOVERY_GRANT="+out.Grant.GrantID, "MCPGUARD_RECOVERY_MARKER="+marker)
	log, e := os.Create(filepath.Join(t.TempDir(), "crashed-process.log"))
	if e != nil {
		t.Fatal(e)
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	if cmd.Start() != nil {
		t.Fatal("helper start failed")
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(12 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(marker)
		var checkpoint map[string]string
		if json.Unmarshal(raw, &checkpoint) == nil && checkpoint["jobId"] != "" {
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("crash checkpoint did not become ready")
	}
	if cmd.Process.Kill() != nil {
		t.Fatal("process kill failed")
	}
	if cmd.Wait() == nil {
		t.Fatal("helper exited without forced process termination")
	}
}

func TestProvisionRecoveryIntegration(t *testing.T) {
	for _, mode := range []string{"before_dispatch", "after_create", "after_verify"} {
		t.Run("process_killed_"+mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			out := f.approved(t, "alice", 0)
			crashHelper(t, f, out, mode)
			work := frozenWork(t, f, out)
			pending, e := f.worker.Recover(f.ctx, out.Request.OrgID, out.Grant.GrantID)
			if e != nil || pending.State != "EXECUTING" {
				t.Fatal("live lease was stolen")
			}
			recoveryDue(t, f, out)
			if mode == "before_dispatch" {
				job := recovered(t, f, out, "FAILED")
				if job.Recovery.LastOutcome == nil || *job.Recovery.LastOutcome != "NATIVE_ABSENT_SEALED" {
					t.Fatal("absence not sealed")
				}
				if e = f.worker.Provider.Create(f.ctx, work.Target, work.Plan, work.Hash, work.Secret); e == nil {
					t.Fatal("late create escaped native tombstone")
				}
			} else {
				job := recovered(t, f, out, "VERIFIED")
				if job.AccountPlanHash != work.Hash {
					t.Fatal("recovery changed frozen plan")
				}
				status := recoveryStatus(t, f, out)
				if status.Grant.State != "ACTIVE" || status.Grant.AccountID == nil || *status.Grant.AccountID != work.Plan.AccountID {
					t.Fatal("recovery did not activate original identity")
				}
				replay := recovered(t, f, out, "VERIFIED")
				if replay.Recovery.Generation != job.Recovery.Generation {
					t.Fatal("terminal recovery created a new lease")
				}
			}
		})
	}
	t.Run("lost_create_receipt_recovers_same_account", func(t *testing.T) {
		f := newRecoveryFixture(t)
		out := f.approved(t, "alice", 1)
		wrapped := &observedProvider{NativeProvisioningProvider: f.worker.Provider, loseReceipt: true}
		f.worker.Provider = wrapped
		f.run(t, out, "OUTCOME_UNKNOWN")
		work := frozenWork(t, f, out)
		f.worker.Provider = &PGProvisioningProvider{f.private}
		recoveryDue(t, f, out)
		recovered(t, f, out, "VERIFIED")
		var n int
		if f.native[1].QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_provision.accounts WHERE account_id=$1", work.Plan.AccountID).Scan(&n) != nil || n != 1 || wrapped.creates.Load() != 1 {
			t.Fatal("unknown outcome created another native identity")
		}
	})
	t.Run("stale_dispatch_cannot_disable_recovered_active_account", func(t *testing.T) {
		f := newRecoveryFixture(t)
		out := f.approved(t, "alice", 0)
		actual := &PGProvisioningProvider{f.private}
		var recoveredID string
		wrapper := &observedProvider{NativeProvisioningProvider: actual, afterVerify: func() {
			recoveryDue(t, f, out)
			other := *f.worker
			other.Provider = actual
			job, e := other.Recover(f.ctx, out.Request.OrgID, out.Grant.GrantID)
			if e != nil || job.State != "VERIFIED" {
				t.Fatal("concurrent recovery failed")
			}
			recoveredID = job.JobID
		}}
		f.worker.Provider = wrapper
		if _, e := f.worker.Run(f.ctx, out.Request.OrgID, out.Grant.GrantID); e == nil {
			t.Fatal("stale dispatch committed a result")
		}
		status := recoveryStatus(t, f, out)
		var login bool
		if recoveredID == "" || status.Grant.State != "ACTIVE" || f.native[0].QueryRow(f.ctx, "SELECT rolcanlogin FROM pg_roles WHERE rolname=$1", wrapper.plan.NativeIdentity).Scan(&login) != nil || !login {
			t.Fatal("stale compensation disabled an ACTIVE identity")
		}
	})
	t.Run("concurrent_recovery_claims_one_generation", func(t *testing.T) {
		f := newRecoveryFixture(t)
		out := f.approved(t, "alice", 0)
		work, _, e := f.worker.prepare(f.ctx, out.Request.OrgID, out.Grant.GrantID)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.worker.Provider.Create(f.ctx, work.Target, work.Plan, work.Hash, work.Secret); e != nil {
			t.Fatal(e)
		}
		recoveryDue(t, f, out)
		wrapped := faults(f)
		f.worker.Provider = wrapped
		var wg sync.WaitGroup
		fail := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := f.worker.Recover(f.ctx, out.Request.OrgID, out.Grant.GrantID)
				if e != nil {
					fail <- e
				}
			}()
		}
		wg.Wait()
		close(fail)
		for e := range fail {
			t.Fatal(e)
		}
		job := recovered(t, f, out, "VERIFIED")
		if wrapped.inspects.Load() != 1 || wrapped.creates.Load() != 0 || job.Recovery.Attempts != 1 {
			t.Fatal("concurrent recovery duplicated native work")
		}
		if _, e = f.worker.Access.Catalog.Store.Pool.Exec(f.ctx, "UPDATE mcpguard_ra.provisioning_jobs SET failure_code='STALE' WHERE org_id=$1 AND job_id=$2", out.Request.OrgID, work.Plan.JobID); e == nil {
			t.Fatal("unleased result mutation succeeded")
		}
	})
	for _, mode := range []string{"cleanup_unavailable", "cleanup_receipt_lost", "crash_after_cleanup"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			out := f.approved(t, "alice", 0)
			wrapped := faults(f)
			wrapped.rejectVerify = true
			f.worker.Provider = wrapped
			f.run(t, out, "CLEANUP_PENDING")
			work := frozenWork(t, f, out)
			wrapped.rejectVerify = false
			recoveryDue(t, f, out)
			if mode == "crash_after_cleanup" {
				crashHelper(t, f, out, "after_cleanup")
				recoveryDue(t, f, out)
			} else {
				wrapped.failCleanup = mode == "cleanup_unavailable"
				wrapped.loseCleanupReceipt = mode == "cleanup_receipt_lost"
				waiting := recovered(t, f, out, "CLEANUP_PENDING")
				calls := wrapped.cleanups.Load()
				recovered(t, f, out, "CLEANUP_PENDING")
				if wrapped.cleanups.Load() != calls || waiting.Recovery.NextAttemptAt == nil {
					t.Fatal("cleanup backoff was bypassed")
				}
				wrapped.failCleanup = false
				wrapped.loseCleanupReceipt = false
				recoveryDue(t, f, out)
			}
			done := recovered(t, f, out, "CLEANED")
			if done.Recovery.CleanupState != "SUCCEEDED" {
				t.Fatal("cleanup state not linked")
			}
			assertOwnedCleaned(t, f, work, 0)
			if recoveryStatus(t, f, out).Grant.State == "ACTIVE" {
				t.Fatal("cleanup revived grant")
			}
			if e := f.worker.Provider.Create(f.ctx, work.Target, work.Plan, work.Hash, work.Secret); e == nil {
				t.Fatal("cleaned plan recreated an account")
			}
		})
	}
	for i, mode := range []string{"access_removed", "target_changed", "subject_disabled", "expired"} {
		t.Run("late_"+mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			index := i % 2
			out := f.approved(t, "alice", index)
			if mode == "expired" {
				input := f.input()
				input.ResourceID = f.bundle.Resources[index].ResourceID
				input.ProfileID = f.bundle.Profiles[index].ProfileID
				input.RequestedExpiry = time.Now().UTC().Add(2 * time.Second).Format(time.RFC3339Nano)
				r, e := f.s.Request(f.ctx, f.people["alice"], input)
				if e != nil {
					t.Fatal(e)
				}
				out, e = f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r))
				if e != nil {
					t.Fatal(e)
				}
			}
			work, _, e := f.worker.prepare(f.ctx, out.Request.OrgID, out.Grant.GrantID)
			if e != nil {
				t.Fatal(e)
			}
			if e = f.worker.Provider.Create(f.ctx, work.Target, work.Plan, work.Hash, work.Secret); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "access_removed":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", out.Request.OrgID, out.Grant.SubjectID, out.Grant.ResourceID)
			case "target_changed":
				f.config.Targets[index].Version = "2"
				f.apply(t)
			case "subject_disabled":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET enabled=false WHERE org_id=$1 AND id=$2", out.Request.OrgID, out.Grant.SubjectID)
			case "expired":
				time.Sleep(2100 * time.Millisecond)
			}
			if e != nil {
				t.Fatal(e)
			}
			recoveryDue(t, f, out)
			recovered(t, f, out, "CLEANUP_PENDING")
			recovered(t, f, out, "CLEANED")
			assertOwnedCleaned(t, f, work, index)
			var state string
			if f.admin.QueryRow(f.ctx, "SELECT state FROM mcpguard_ra.access_grant_heads WHERE org_id=$1 AND grant_id=$2", out.Request.OrgID, out.Grant.GrantID).Scan(&state) != nil || (state != "REVOKED" && state != "EXPIRED") {
				t.Fatal("late account escaped authorization termination")
			}
		})
	}
	t.Run("revocation_between_native_inspection_and_finish", func(t *testing.T) {
		f := newRecoveryFixture(t)
		out := f.approved(t, "alice", 0)
		work, _, e := f.worker.prepare(f.ctx, out.Request.OrgID, out.Grant.GrantID)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.worker.Provider.Create(f.ctx, work.Target, work.Plan, work.Hash, work.Secret); e != nil {
			t.Fatal(e)
		}
		wrapped := faults(f)
		wrapped.afterInspect = func() {
			if _, e := f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", out.Request.OrgID, out.Grant.SubjectID, out.Grant.ResourceID); e != nil {
				t.Fatal(e)
			}
		}
		f.worker.Provider = wrapped
		recoveryDue(t, f, out)
		recovered(t, f, out, "CLEANUP_PENDING")
		wrapped.afterInspect = nil
		recovered(t, f, out, "CLEANED")
		assertOwnedCleaned(t, f, work, 0)
	})
	for _, revoked := range []bool{false, true} {
		t.Run("preexisting_account_untouched_"+strconvBool(revoked), func(t *testing.T) {
			f := newRecoveryFixture(t)
			secret := f.existing(t, 0, "alice", "")
			out := f.approved(t, "alice", 0)
			if _, _, e := f.worker.prepare(f.ctx, out.Request.OrgID, out.Grant.GrantID); e != nil {
				t.Fatal(e)
			}
			if revoked {
				if _, e := f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", out.Request.OrgID, out.Grant.SubjectID, out.Grant.ResourceID); e != nil {
					t.Fatal(e)
				}
			}
			recoveryDue(t, f, out)
			want := "VERIFIED"
			if revoked {
				want = "FAILED"
			}
			recovered(t, f, out, want)
			var login, selectAllowed bool
			if f.native[0].QueryRow(f.ctx, "SELECT rolcanlogin,has_table_privilege(rolname,'resource_demo.items','SELECT') FROM pg_roles WHERE rolname=$1", secret.Username).Scan(&login, &selectAllowed) != nil || !login || !selectAllowed {
				t.Fatal("recovery changed preexisting account")
			}
		})
	}
	for _, mode := range []string{"cleanup", "inspection"} {
		t.Run("bounded_"+mode+"_retries", func(t *testing.T) {
			f := newRecoveryFixture(t)
			out := f.approved(t, "alice", 0)
			wrapped := faults(f)
			f.worker.Provider = wrapped
			if mode == "cleanup" {
				wrapped.rejectVerify = true
				f.run(t, out, "CLEANUP_PENDING")
				wrapped.rejectVerify = false
				wrapped.failCleanup = true
			} else {
				if _, _, e := f.worker.prepare(f.ctx, out.Request.OrgID, out.Grant.GrantID); e != nil {
					t.Fatal(e)
				}
				wrapped.failInspect = true
			}
			for n := 1; n <= 6; n++ {
				recoveryDue(t, f, out)
				want := "OUTCOME_UNKNOWN"
				if mode == "cleanup" {
					want = "CLEANUP_PENDING"
				}
				if n == 6 {
					want = "MANUAL_REQUIRED"
				}
				job := recovered(t, f, out, want)
				if n == 6 && job.Recovery.CleanupState != "MANUAL_REQUIRED" {
					t.Fatal("retry exhaustion was hidden")
				}
			}
			recovered(t, f, out, "MANUAL_REQUIRED")
			if mode == "cleanup" && wrapped.cleanups.Load() != 6 || mode == "inspection" && wrapped.inspects.Load() != 6 {
				t.Fatal("retry bound not enforced")
			}
		})
	}
	t.Run("unmanaged_identity_collision_is_never_disabled", func(t *testing.T) {
		f := newRecoveryFixture(t)
		out := f.approved(t, "alice", 0)
		work, _, e := f.worker.prepare(f.ctx, out.Request.OrgID, out.Grant.GrantID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.native[0].Exec(f.ctx, "CREATE ROLE "+pgx.Identifier{work.Plan.NativeIdentity}.Sanitize()+" LOGIN PASSWORD '"+work.Secret.Password+"'"); e != nil {
			t.Fatal("collision setup failed")
		}
		recoveryDue(t, f, out)
		recovered(t, f, out, "MANUAL_REQUIRED")
		var login bool
		_ = f.native[0].QueryRow(f.ctx, "SELECT rolcanlogin FROM pg_roles WHERE rolname=$1", work.Plan.NativeIdentity).Scan(&login)
		if !login {
			t.Fatal("unmanaged collision was disabled")
		}
	})
	t.Run("same_name_different_oid_is_never_adopted", func(t *testing.T) {
		f := newRecoveryFixture(t)
		out := f.approved(t, "alice", 0)
		work, _, e := f.worker.prepare(f.ctx, out.Request.OrgID, out.Grant.GrantID)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.worker.Provider.Create(f.ctx, work.Target, work.Plan, work.Hash, work.Secret); e != nil {
			t.Fatal(e)
		}
		role := pgx.Identifier{work.Plan.NativeIdentity}.Sanitize()
		renamed := pgx.Identifier{"retained_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
		if _, e = f.native[0].Exec(f.ctx, "ALTER ROLE "+role+" RENAME TO "+renamed+";CREATE ROLE "+role+" LOGIN PASSWORD '"+work.Secret.Password+"';"); e != nil {
			t.Fatal("OID replacement setup failed")
		}
		recoveryDue(t, f, out)
		recovered(t, f, out, "MANUAL_REQUIRED")
		var n int
		_ = f.native[0].QueryRow(f.ctx, "SELECT count(*) FROM pg_roles WHERE rolname=$1 AND rolcanlogin", work.Plan.NativeIdentity).Scan(&n)
		if n != 1 {
			t.Fatal("replacement identity was modified")
		}
	})
	t.Run("critical_recovery_audit_failure_rolls_back_result", func(t *testing.T) {
		f := newRecoveryFixture(t)
		out := f.approved(t, "alice", 0)
		wrapped := faults(f)
		wrapped.rejectVerify = true
		f.worker.Provider = wrapped
		f.run(t, out, "CLEANUP_PENDING")
		wrapped.rejectVerify = false
		work := frozenWork(t, f, out)
		suffix := strings.ReplaceAll(out.Request.OrgID, "-", "")
		sql := `CREATE FUNCTION mcpguard_ra.fail_recovery_` + suffix + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.org_id='` + out.Request.OrgID + `' AND NEW.event_type='OWNED_TEMPLATE_PRIVILEGES_REMOVED' THEN RAISE EXCEPTION 'injected recovery audit failure';END IF;RETURN NEW;END $$;CREATE TRIGGER fail_recovery_` + suffix + ` BEFORE INSERT ON mcpguard_ra.provision_attempt_events FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.fail_recovery_` + suffix + `();`
		if _, e := f.admin.Exec(f.ctx, sql); e != nil {
			t.Fatal("recovery fault setup failed")
		}
		recoveryDue(t, f, out)
		if _, e := f.worker.Recover(f.ctx, out.Request.OrgID, out.Grant.GrantID); e == nil {
			t.Fatal("critical recovery audit failure ignored")
		}
		assertOwnedCleaned(t, f, work, 0)
		var state string
		_ = f.admin.QueryRow(f.ctx, "SELECT state FROM mcpguard_ra.provisioning_jobs WHERE org_id=$1 AND job_id=$2", out.Request.OrgID, work.Plan.JobID).Scan(&state)
		if state != "CLEANUP_PENDING" {
			t.Fatal("unaudited cleanup result committed")
		}
		if _, e := f.admin.Exec(f.ctx, "DROP TRIGGER fail_recovery_"+suffix+" ON mcpguard_ra.provision_attempt_events"); e != nil {
			t.Fatal(e)
		}
		recoveryDue(t, f, out)
		recovered(t, f, out, "CLEANED")
	})
}
func strconvBool(value bool) string {
	if value {
		return "revoked"
	}
	return "valid"
}
