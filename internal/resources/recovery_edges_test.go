package resources

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

func TestRecoveryNativeAndPlanFencesIntegration(t *testing.T) {
	for _, mode := range []string{"inspection", "cleanup"} {
		t.Run("last_attempt_crash_"+mode, func(t *testing.T) {
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
			for n := 0; n < 5; n++ {
				recoveryDue(t, f, out)
				state := "OUTCOME_UNKNOWN"
				if mode == "cleanup" {
					state = "CLEANUP_PENDING"
				}
				recovered(t, f, out, state)
			}
			recoveryDue(t, f, out)
			claim, e := f.worker.claimRecovery(f.ctx, out.Request.OrgID, out.Grant.GrantID)
			if e != nil || claim == nil || claim.attempts != 6 {
				t.Fatal("last attempt not claimed")
			}
			// Simulate a crash after the final claim. Recovery may close metadata,
			// but must never make a seventh native call.
			recoveryDue(t, f, out)
			job := recovered(t, f, out, "MANUAL_REQUIRED")
			if job.Recovery.Operation != "FINALIZE" || mode == "cleanup" && wrapped.cleanups.Load() != 5 || mode == "inspection" && wrapped.inspects.Load() != 5 {
				t.Fatal("last-claim crash bypassed retry fence")
			}
		})
	}
	t.Run("native_creation_commit_wins_before_inspection", func(t *testing.T) {
		f := newRecoveryFixture(t)
		out := f.approved(t, "alice", 0)
		work, _, e := f.worker.prepare(f.ctx, out.Request.OrgID, out.Grant.GrantID)
		if e != nil {
			t.Fatal(e)
		}
		recoveryDue(t, f, out)
		tx, e := f.native[0].Begin(f.ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer rollback(tx)
		if _, e = tx.Exec(f.ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,529401))", work.Plan.AccountID); e != nil {
			t.Fatal(e)
		}
		type result struct {
			job v02.ProvisioningJob
			err error
		}
		done := make(chan result, 1)
		observer, e := pgx.Connect(f.ctx, f.admins[0])
		if e != nil {
			t.Fatal("native observer unavailable")
		}
		defer observer.Close(f.ctx)
		go func() {
			job, e := f.worker.Recover(f.ctx, out.Request.OrgID, out.Grant.GrantID)
			done <- result{job, e}
		}()
		deadline := time.Now().Add(2 * time.Second)
		blocked := false
		for time.Now().Before(deadline) {
			var count int
			if observer.QueryRow(f.ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name='mcpguard-restricted-provisioner' AND wait_event_type='Lock' AND query LIKE '%reconcile_account%'`).Scan(&count) == nil && count > 0 {
				blocked = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !blocked {
			t.Fatal("native inspection did not serialize with creation")
		}
		expiry, _, _ := normalizedTime(work.Plan.ExpiresAt)
		var oid uint32
		if e = tx.QueryRow(f.ctx, "SELECT mcpguard_provision.create_account($1,$2,$3,$4,$5)", work.Plan.TargetID, work.Plan.AccountID, work.Hash, work.Secret.Password, expiry).Scan(&oid); e != nil {
			t.Fatal("in-flight creation failed")
		}
		if tx.Commit(f.ctx) != nil {
			t.Fatal("native commit failed")
		}
		r := <-done
		if r.err != nil || r.job.State != "VERIFIED" {
			t.Fatal("committed native result was not reconciled: ", r.err)
		}
	})
	for _, mode := range []string{"corrupted_plan", "changed_private_endpoint"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			out := f.approved(t, "alice", 0)
			work, _, e := f.worker.prepare(f.ctx, out.Request.OrgID, out.Grant.GrantID)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "corrupted_plan" {
				tx, e := f.admin.Begin(f.ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer rollback(tx)
				if _, e = tx.Exec(f.ctx, "ALTER TABLE mcpguard_ra.provisioning_jobs DISABLE TRIGGER provision_job_guard"); e != nil {
					t.Fatal(e)
				}
				if _, e = tx.Exec(f.ctx, "UPDATE mcpguard_ra.provisioning_jobs SET plan_ciphertext=decode('0001','hex') WHERE org_id=$1 AND job_id=$2", out.Request.OrgID, work.Plan.JobID); e != nil {
					t.Fatal(e)
				}
				if _, e = tx.Exec(f.ctx, "ALTER TABLE mcpguard_ra.provisioning_jobs ENABLE TRIGGER provision_job_guard"); e != nil {
					t.Fatal(e)
				}
				if tx.Commit(f.ctx) != nil {
					t.Fatal("corruption fault failed")
				}
			} else {
				ep := f.private.Endpoints[work.Plan.EndpointConfigRef]
				ep.Port++
				f.private.Endpoints[work.Plan.EndpointConfigRef] = ep
			}
			wrapped := faults(f)
			f.worker.Provider = wrapped
			recoveryDue(t, f, out)
			recovered(t, f, out, "MANUAL_REQUIRED")
			if wrapped.inspects.Load() != 0 || wrapped.creates.Load() != 0 || wrapped.cleanups.Load() != 0 {
				t.Fatal("invalid plan reached a native target")
			}
		})
	}
	t.Run("due_batch_is_bounded_and_terminal_work_is_not_claimed", func(t *testing.T) {
		f := newRecoveryFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		before := recoveryStatus(t, f, out).Provisioning.Recovery.Generation
		jobs, e := f.worker.RecoverOnce(f.ctx)
		if e != nil {
			t.Fatal(e)
		}
		if len(jobs) > v02.MaxRecoveryBatch {
			t.Fatal("unbounded recovery batch")
		}
		if recoveryStatus(t, f, out).Provisioning.Recovery.Generation != before {
			t.Fatal("batch reclaimed an ACTIVE grant")
		}
	})
}

func TestRecoveryV3UnknownUpgradeIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	out := f.approved(t, "alice", 0)
	wrapped := &observedProvider{NativeProvisioningProvider: f.worker.Provider, loseReceipt: true}
	f.worker.Provider = wrapped
	f.run(t, out, "OUTCOME_UNKNOWN")
	database := "ra_upgrade_v3_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, e := f.admin.Exec(f.ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	address, e := url.Parse(f.dsn)
	if e != nil {
		t.Fatal("upgrade config invalid")
	}
	address.Path = "/" + database
	dsn := address.String()
	if e = metadata.Migrate(f.ctx, dsn); e != nil {
		t.Fatal(e)
	}
	target, e := pgx.Connect(f.ctx, dsn)
	if e != nil {
		t.Fatal("upgrade target unavailable")
	}
	defer target.Close(f.ctx)
	for version := 1; version <= 3; version++ {
		if _, e = target.Exec(f.ctx, string(migrationBytes(version))); e != nil {
			t.Fatal(e)
		}
		if _, e = target.Exec(f.ctx, "INSERT INTO mcpguard_ra.schema_versions VALUES($1,$2)", version, migrationChecksum(version)); e != nil {
			t.Fatal(e)
		}
	}
	// Clone only this new test organization into a genuine v3 schema. Temporarily
	// disable restore-time triggers, as for an administrative data restore; every
	// copied row comes from the actual approved/created/unknown source workflow.
	tables := []string{"mcpguard.authorization_heads", "mcpguard.principals", "mcpguard_ra.resource_versions", "mcpguard_ra.resource_heads", "mcpguard_ra.profile_versions", "mcpguard_ra.tool_bindings", "mcpguard_ra.resource_access", "mcpguard_ra.subject_revisions", "mcpguard_ra.access_policy_versions", "mcpguard_ra.access_policy_heads", "mcpguard_ra.access_reviewers", "mcpguard_ra.access_requests", "mcpguard_ra.access_decisions", "mcpguard_ra.access_grant_ids", "mcpguard_ra.access_grant_versions", "mcpguard_ra.access_grant_heads", "mcpguard_ra.access_commands", "mcpguard_ra.access_audit", "mcpguard_ra.provision_targets", "mcpguard_ra.existing_account_registrations", "mcpguard_ra.provision_config_history", "mcpguard_ra.account_claims", "mcpguard_ra.provisioning_jobs", "mcpguard_ra.native_secrets", "mcpguard_ra.native_observations", "mcpguard_ra.native_accounts", "mcpguard_ra.provision_audit"}
	tx, e := target.Begin(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollback(tx)
	if _, e = tx.Exec(f.ctx, "SET LOCAL session_replication_role=replica"); e != nil {
		t.Fatal(e)
	}
	for _, table := range tables {
		var raw []byte
		if e = f.admin.QueryRow(f.ctx, "SELECT coalesce(jsonb_agg(to_jsonb(t)),'[]'::jsonb) FROM "+table+" t WHERE org_id=$1", out.Request.OrgID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(f.ctx, "INSERT INTO "+table+" SELECT * FROM jsonb_populate_recordset(NULL::"+table+",$1::jsonb)", raw); e != nil {
			t.Fatal(e)
		}
	}
	if tx.Commit(f.ctx) != nil {
		t.Fatal("v3 source restore failed")
	}
	if _, e = target.Exec(f.ctx, "SELECT setval('mcpguard.audit_events_seq_seq',1000000)"); e != nil {
		t.Fatal(e)
	}
	var before string
	if e = target.QueryRow(f.ctx, "SELECT to_jsonb(j)::text FROM mcpguard_ra.provisioning_jobs j WHERE org_id=$1 AND grant_id=$2", out.Request.OrgID, out.Grant.GrantID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = Migrate(f.ctx, dsn); e != nil {
		t.Fatal(e)
	}
	if e = Migrate(f.ctx, dsn); e != nil {
		t.Fatal(e)
	}
	var after string
	var generation int64
	if e = target.QueryRow(f.ctx, "SELECT to_jsonb(j)::text FROM mcpguard_ra.provisioning_jobs j WHERE org_id=$1 AND grant_id=$2", out.Request.OrgID, out.Grant.GrantID).Scan(&after); e != nil || before != after {
		t.Fatal("v3 job plan or original outcome overwritten")
	}
	if e = target.QueryRow(f.ctx, "SELECT generation FROM mcpguard_ra.provision_recovery l JOIN mcpguard_ra.provisioning_jobs j USING(org_id,job_id) WHERE org_id=$1 AND grant_id=$2", out.Request.OrgID, out.Grant.GrantID).Scan(&generation); e != nil || generation != 0 {
		t.Fatal("upgrade fabricated an earlier recovery attempt")
	}
	binary := os.Getenv("MCPGUARD_TEST_CATALOG_BINARY")
	if binary == "" {
		binary = filepath.Join("/work/.dev/ra06-build", "mcpguard-catalog")
	}
	cmd := exec.Command(binary, "grant-provision-runtime", "mcpguard_provisioner")
	cmd.Env = append(os.Environ(), "MCPGUARD_MIGRATION_DSN="+dsn)
	if result, e := cmd.CombinedOutput(); e != nil {
		t.Fatal("upgrade worker grant failed; output bytes: ", len(result))
	}
	workerURL, e := url.Parse(os.Getenv("MCPGUARD_TEST_PROVISIONER_DSN"))
	if e != nil {
		t.Fatal("upgrade worker config invalid")
	}
	workerURL.Path = "/" + database
	store, e := metadata.Open(f.ctx, workerURL.String(), f.s.Catalog.Store.Vault)
	if e != nil {
		t.Fatal("upgrade worker unavailable")
	}
	defer store.Close()
	catalog, e := NewCatalog(f.ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	worker, e := NewProvisioner(catalog, f.private, &PGProvisioningProvider{f.private})
	if e != nil {
		t.Fatal(e)
	}
	job, e := worker.Recover(f.ctx, out.Request.OrgID, out.Grant.GrantID)
	if e != nil || job.State != "VERIFIED" || job.Recovery.Generation != "1" || job.Recovery.Attempts != 1 {
		t.Fatal("v3 unknown job did not recover with its first real lease: ", e)
	}
	var originalRows int
	if target.QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_ra.access_grant_versions WHERE org_id=$1 AND grant_id=$2", out.Request.OrgID, out.Grant.GrantID).Scan(&originalRows) != nil || originalRows != 2 {
		t.Fatal("upgraded activation history missing")
	}
}
