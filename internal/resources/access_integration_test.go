package resources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/runtimeconfig"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type accessFixture struct {
	ctx    context.Context
	s      *AccessService
	admin  *pgx.Conn
	dsn    string
	people map[string]metadata.Principal
	config v02.AccessConfiguration
	bundle v02.CatalogBundle
}

func (f *accessFixture) now(t *testing.T) time.Time {
	t.Helper()
	now, err := databaseTime(f.ctx, f.admin)
	if err != nil {
		t.Fatal("read fixture database time: ", err)
	}
	return now
}

func (f *accessFixture) awaitDeadline(t *testing.T, timestamp string) {
	t.Helper()
	deadline, _, err := normalizedTime(timestamp)
	if err != nil {
		t.Fatal("parse fixture deadline: ", err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	defer cancel()
	if err := waitForDatabaseTime(ctx, f.admin, deadline); err != nil {
		t.Fatal("database did not reach fixture deadline: ", err)
	}
}

func newAccessFixture(t *testing.T, reviewSeconds int) *accessFixture {
	t.Helper()
	path := os.Getenv("MCPGUARD_RA_CONFIG")
	if path == "" {
		t.Skip("explicit RA-04 fixture required")
	}
	raw, e := os.ReadFile(path)
	var scene struct {
		Access bool `json:"raAccessPhase"`
	}
	if e != nil || json.Unmarshal(raw, &scene) != nil {
		t.Fatal("invalid private RA fixture")
	}
	if !scene.Access {
		t.Skip("explicit RA-04 fixture required")
	}
	ctx := context.Background()
	dsn := os.Getenv("MCPGUARD_TEST_METADATA_ADMIN_DSN")
	admin, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal("metadata unavailable")
	}
	t.Cleanup(func() { admin.Close(ctx) })
	cfg, e := runtimeconfig.Load()
	if e != nil {
		t.Fatal(e)
	}
	vault, _ := metadata.NewVault(cfg.EncryptionKey, cfg.PlanKey)
	store, e := metadata.Open(ctx, cfg.MetadataDSN, vault)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(store.Close)
	catalog, e := NewCatalog(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	service, e := NewAccessService(catalog)
	if e != nil {
		t.Fatal(e)
	}
	org := uuid.NewString()
	if _, e = admin.Exec(ctx, "INSERT INTO mcpguard.authorization_heads(org_id) VALUES($1)", org); e != nil {
		t.Fatal(e)
	}
	people := map[string]metadata.Principal{}
	for _, name := range []string{"alice", "bob", "reviewer", "rogue"} {
		token := uuid.NewString() + uuid.NewString()
		hash := sha256.Sum256([]byte(token))
		id := uuid.NewString()
		if _, e = admin.Exec(ctx, `INSERT INTO mcpguard.principals(org_id,id,display_name,token_hash,expires_at,scope_id) VALUES($1,$2,$3,$4,clock_timestamp()+interval '1 day','ra_access')`, org, id, name, hash[:]); e != nil {
			t.Fatal(e)
		}
		person, e := store.VerifyToken(ctx, token)
		if e != nil {
			t.Fatal(e)
		}
		people[name] = person
	}
	raw, e = os.ReadFile(filepath.Join(filepath.Dir(path), "catalog.json"))
	var bundle v02.CatalogBundle
	if e != nil || v02.Decode("CatalogBundle", raw, &bundle) != nil {
		t.Fatal("invalid base catalog")
	}
	bundle.Bindings = []v02.CredentialBinding{}
	bundle.Access = []v02.ResourceAccess{}
	for i := range bundle.Resources {
		bundle.Resources[i].OrgID = org
	}
	for i := range bundle.Heads {
		bundle.Heads[i].OrgID = org
	}
	for i := range bundle.Profiles {
		bundle.Profiles[i].OrgID = org
	}
	for i := range bundle.Tools {
		bundle.Tools[i].OrgID = org
	}
	for _, r := range bundle.Resources {
		for _, p := range people {
			bundle.Access = append(bundle.Access, v02.ResourceAccess{OrgID: org, PrincipalID: p.ID, ResourceID: r.ResourceID, CanDiscover: true, CanRequest: true})
		}
	}
	raw, _ = json.Marshal(bundle)
	if e = Import(ctx, dsn, raw); e != nil {
		t.Fatal(e)
	}
	conf := v02.AccessConfiguration{SchemaVersion: "0.2", Policies: []v02.AccessPolicy{}, Heads: []v02.AccessPolicyHead{}, Reviewers: []v02.AccessReviewer{}}
	for i, p := range bundle.Profiles {
		conf.Policies = append(conf.Policies, v02.AccessPolicy{OrgID: org, ResourceID: p.ResourceID, ResourceVersion: "1", ProfileID: p.ProfileID, ProfileVersion: p.Version, Version: "1", ApprovalPolicyRef: p.ApprovalPolicyRef, MaxGrantTtlSeconds: 86400, ReviewTtlSeconds: reviewSeconds})
		conf.Heads = append(conf.Heads, v02.AccessPolicyHead{OrgID: org, ResourceID: p.ResourceID, ResourceVersion: "1", ProfileID: p.ProfileID, ProfileVersion: p.Version, Version: "1", Revision: "1", Enabled: true})
		if i == 0 {
			for _, name := range []string{"alice", "reviewer"} {
				conf.Reviewers = append(conf.Reviewers, v02.AccessReviewer{OrgID: org, ResourceID: p.ResourceID, PrincipalID: people[name].ID, CanApprove: true})
			}
		}
	}
	raw, _ = json.Marshal(conf)
	if e = ImportAccess(ctx, dsn, raw); e != nil {
		t.Fatal(e)
	}
	return &accessFixture{ctx, service, admin, dsn, people, conf, bundle}
}
func (f *accessFixture) input() v02.AccessRequestInput {
	return v02.AccessRequestInput{ResourceID: f.bundle.Resources[0].ResourceID, ProfileID: f.bundle.Profiles[0].ProfileID, RequestedExpiry: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), Reason: "scoped test request", RequestKey: uuid.NewString()}
}
func (f *accessFixture) request(t *testing.T, name string) v02.AccessRequest {
	t.Helper()
	r, e := f.s.Request(f.ctx, f.people[name], f.input())
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func approveInput(r v02.AccessRequest) v02.DecisionInput {
	expiry := r.RequestedExpiry
	return v02.DecisionInput{RequestHash: r.RequestHash, ExpectedRevision: r.Revision, Decision: "APPROVE", ApprovedExpiry: &expiry, Reason: "bounded human approval", RequestKey: uuid.NewString()}
}
func (f *accessFixture) count(t *testing.T, table string, id string) int {
	t.Helper()
	var n int
	e := f.admin.QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_ra."+table+" WHERE org_id=$1 AND request_id=$2", f.people["alice"].OrgID, id).Scan(&n)
	if e != nil {
		t.Fatal(e)
	}
	return n
}

func TestAccessWorkflowIntegration(t *testing.T) {
	t.Run("approve_is_atomic_and_never_active", func(t *testing.T) {
		f := newAccessFixture(t, 3600)
		r := f.request(t, "alice")
		out, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r))
		if e != nil {
			t.Fatal(e)
		}
		if out.Request.State != "APPROVED" || out.Decision == nil || out.Grant == nil || out.Grant.State != "PROVISIONING" || out.Grant.AccountID != nil || out.Grant.BindingID != nil || out.Grant.SubjectID != f.people["alice"].ID {
			t.Fatal("approval fabricated activation or lost ownership")
		}
		b, _ := json.Marshal(out)
		if v02.Decode("AccessStatusResult", b, nil) != nil {
			t.Fatal("status contract drift")
		}
		status, e := f.s.Status(f.ctx, f.people["alice"], r.RequestID)
		if e != nil || status.Grant.GrantID != out.Grant.GrantID {
			t.Fatal("owner status lost grant")
		}
		if _, e = f.s.Status(f.ctx, f.people["bob"], r.RequestID); !errors.Is(e, ErrDenied) {
			t.Fatal("request crossed owner")
		}
		if _, e = f.s.Grant(f.ctx, f.people["bob"], out.Grant.GrantID); !errors.Is(e, ErrDenied) {
			t.Fatal("grant crossed owner")
		}
		a, e := f.s.Grants(f.ctx, f.people["alice"], v02.ListGrantsInput{})
		if e != nil || len(a.Grants) != 1 {
			t.Fatal("own grant missing")
		}
		bobs, e := f.s.Grants(f.ctx, f.people["bob"], v02.ListGrantsInput{})
		if e != nil || len(bobs.Grants) != 0 {
			t.Fatal("grant list crossed owner")
		}
		detail, e := f.s.Catalog.Get(f.ctx, f.people["alice"], v02.GetResourceInput{ResourceID: r.ResourceID})
		if e != nil || detail.Resource.AccessState != "PROVISIONING" {
			t.Fatal("directory not linked to workflow")
		}
		if len(detail.Profiles) != 1 || detail.Profiles[0].MaxGrantTtlSeconds == nil || *detail.Profiles[0].MaxGrantTtlSeconds != 86400 {
			t.Fatal("request policy bounds missing from visible profile")
		}
		if _, e = f.s.Catalog.Snapshot(f.ctx, f.people["alice"], r.ResourceID); !errors.Is(e, ErrDenied) {
			t.Fatal("approval manufactured native binding")
		}
		if _, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.access_grant_heads SET state='ACTIVE',revision=revision+1 WHERE org_id=$1 AND grant_id=$2", r.OrgID, out.Grant.GrantID); e == nil {
			t.Fatal("unverified grant activated")
		}
		if _, e = f.s.Cancel(f.ctx, f.people["alice"], v02.CancelAccessInput{RequestID: r.RequestID, ExpectedRevision: out.Request.Revision, RequestKey: uuid.NewString()}); !errors.Is(e, ErrConflict) {
			t.Fatal("cancel rewrote approved request")
		}
		if f.count(t, "access_decisions", r.RequestID) != 1 || f.count(t, "access_grant_versions", r.RequestID) != 1 {
			t.Fatal("decision/grant not unique")
		}
		var ciphertext []byte
		if f.admin.QueryRow(f.ctx, "SELECT request_ciphertext FROM mcpguard_ra.access_requests WHERE org_id=$1 AND request_id=$2", r.OrgID, r.RequestID).Scan(&ciphertext) != nil || bytes.Contains(ciphertext, []byte(r.Reason)) {
			t.Fatal("request reason was stored as plaintext")
		}
	})
	t.Run("self_other_resource_scope_hash_and_expiry_denied", func(t *testing.T) {
		f := newAccessFixture(t, 3600)
		r := f.request(t, "alice")
		in := approveInput(r)
		for _, name := range []string{"alice", "rogue"} {
			if _, e := f.s.Decide(f.ctx, f.people[name], r.RequestID, in); !errors.Is(e, ErrDenied) {
				t.Fatal("unauthorized decision accepted")
			}
		}
		wrong := in
		wrong.RequestHash = "wrong"
		if _, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, wrong); !errors.Is(e, ErrConflict) {
			t.Fatal("modified hash accepted")
		}
		wrong = in
		wrong.ExpectedRevision = "2"
		if _, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, wrong); !errors.Is(e, ErrConflict) {
			t.Fatal("wrong revision accepted")
		}
		wrong = in
		later := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339Nano)
		wrong.ApprovedExpiry = &later
		if _, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, wrong); !errors.Is(e, ErrInput) {
			t.Fatal("approval expanded requested expiry")
		}
		input := f.input()
		input.ResourceID = f.bundle.Resources[1].ResourceID
		input.ProfileID = f.bundle.Profiles[1].ProfileID
		r2, e := f.s.Request(f.ctx, f.people["alice"], input)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.Decide(f.ctx, f.people["reviewer"], r2.RequestID, approveInput(r2)); !errors.Is(e, ErrDenied) {
			t.Fatal("reviewer crossed resource")
		}
		input = f.input()
		input.ProfileID = uuid.NewString()
		if _, e = f.s.Request(f.ctx, f.people["alice"], input); !errors.Is(e, ErrDenied) {
			t.Fatal("arbitrary profile accepted")
		}
		input = f.input()
		input.RequestedExpiry = time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339Nano)
		if _, e = f.s.Request(f.ctx, f.people["alice"], input); !errors.Is(e, ErrInput) {
			t.Fatal("policy TTL exceeded")
		}
	})
	t.Run("request_idempotency_concurrency_and_current_authorization", func(t *testing.T) {
		f := newAccessFixture(t, 3600)
		input := f.input()
		var wg sync.WaitGroup
		results := make(chan v02.AccessRequest, 12)
		errs := make(chan error, 12)
		for range 12 {
			wg.Add(1)
			go func() { defer wg.Done(); v, e := f.s.Request(f.ctx, f.people["alice"], input); results <- v; errs <- e }()
		}
		wg.Wait()
		close(results)
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		id := ""
		for r := range results {
			if id != "" && r.RequestID != id {
				t.Fatal("duplicate request created")
			}
			id = r.RequestID
		}
		if f.count(t, "access_requests", id) != 1 || f.count(t, "access_audit", id) != 1 {
			t.Fatal("idempotent replay repeated state or audit")
		}
		changed := input
		changed.Reason = "different"
		if _, e := f.s.Request(f.ctx, f.people["alice"], changed); !errors.Is(e, ErrIdempotency) {
			t.Fatal("same key accepted different payload")
		}
		other, e := f.s.Request(f.ctx, f.people["bob"], input)
		if e != nil || other.RequestID == id {
			t.Fatal("idempotency crossed caller")
		}
		if _, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", f.people["alice"].OrgID, f.people["alice"].ID, input.ResourceID); e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.Request(f.ctx, f.people["alice"], input); !errors.Is(e, ErrDenied) {
			t.Fatal("replay bypassed current request permission")
		}
	})
	t.Run("decision_concurrency_has_one_winner", func(t *testing.T) {
		f := newAccessFixture(t, 3600)
		r := f.request(t, "bob")
		var wg sync.WaitGroup
		errs := make(chan error, 10)
		for i := range 10 {
			actor := "reviewer"
			if i%2 == 0 {
				actor = "alice"
			}
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				_, e := f.s.Decide(f.ctx, f.people[name], r.RequestID, approveInput(r))
				errs <- e
			}(actor)
		}
		wg.Wait()
		close(errs)
		won := 0
		for e := range errs {
			if e == nil {
				won++
			} else if !errors.Is(e, ErrConflict) {
				t.Fatal(e)
			}
		}
		if won != 1 || f.count(t, "access_decisions", r.RequestID) != 1 || f.count(t, "access_grant_versions", r.RequestID) != 1 {
			t.Fatal("concurrent decisions consumed more than one qualification")
		}
	})
	t.Run("decision_replay_and_reviewer_removal", func(t *testing.T) {
		f := newAccessFixture(t, 3600)
		r := f.request(t, "alice")
		in := approveInput(r)
		first, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, in)
		if e != nil {
			t.Fatal(e)
		}
		again, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, in)
		if e != nil || again.Grant.GrantID != first.Grant.GrantID {
			t.Fatal("decision replay created new grant")
		}
		in.Reason = "changed"
		if _, e = f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, in); !errors.Is(e, ErrIdempotency) {
			t.Fatal("decision key payload changed")
		}
		in.Reason = "bounded human approval"
		if _, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.access_reviewers SET can_approve=false WHERE org_id=$1 AND resource_id=$2 AND principal_id=$3", r.OrgID, r.ResourceID, f.people["reviewer"].ID); e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, in); !errors.Is(e, ErrDenied) {
			t.Fatal("removed reviewer replayed decision")
		}
	})
	t.Run("rejection_and_cancel_are_terminal", func(t *testing.T) {
		f := newAccessFixture(t, 3600)
		r := f.request(t, "alice")
		in := approveInput(r)
		in.Decision = "REJECT"
		in.ApprovedExpiry = nil
		out, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, in)
		if e != nil || out.Request.State != "REJECTED" || out.Grant != nil {
			t.Fatal("rejection created grant")
		}
		r = f.request(t, "alice")
		cancel := v02.CancelAccessInput{RequestID: r.RequestID, ExpectedRevision: r.Revision, RequestKey: uuid.NewString()}
		if _, e = f.s.Cancel(f.ctx, f.people["bob"], cancel); !errors.Is(e, ErrDenied) {
			t.Fatal("other user cancelled request")
		}
		a, e := f.s.Cancel(f.ctx, f.people["alice"], cancel)
		if e != nil || a.State != "CANCELLED" {
			t.Fatal(e)
		}
		b, e := f.s.Cancel(f.ctx, f.people["alice"], cancel)
		if e != nil || b.Revision != a.Revision {
			t.Fatal("cancel replay changed revision")
		}
		if _, e = f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r)); !errors.Is(e, ErrConflict) {
			t.Fatal("cancelled request approved")
		}
	})
	for _, change := range []string{"resource", "policy", "request-right", "principal"} {
		t.Run("frozen_qualification_"+change, func(t *testing.T) {
			f := newAccessFixture(t, 3600)
			r := f.request(t, "alice")
			var e error
			switch change {
			case "resource":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_heads SET revision=revision+1 WHERE org_id=$1 AND resource_id=$2", r.OrgID, r.ResourceID)
			case "policy":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.access_policy_heads SET revision=revision+1 WHERE org_id=$1 AND resource_id=$2", r.OrgID, r.ResourceID)
			case "request-right":
				for _, enabled := range []bool{false, true} {
					_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_access SET can_request=$4 WHERE org_id=$1 AND resource_id=$2 AND principal_id=$3", r.OrgID, r.ResourceID, r.OwnerID, enabled)
					if e != nil {
						break
					}
				}
			case "principal":
				for _, enabled := range []bool{false, true} {
					_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET enabled=$3 WHERE org_id=$1 AND id=$2", r.OrgID, r.OwnerID, enabled)
					if e != nil {
						break
					}
				}
			}
			if e != nil {
				t.Fatal(e)
			}
			out, e := f.s.Status(f.ctx, f.people["alice"], r.RequestID)
			if e != nil || out.Request.State != "STALE" {
				t.Fatal("old request revived after qualification changed")
			}
			if _, e = f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r)); !errors.Is(e, ErrConflict) {
				t.Fatal("stale request approved")
			}
		})
	}
	t.Run("review_deadline_and_grant_expiry_persist", func(t *testing.T) {
		f := newAccessFixture(t, 1)
		r := f.request(t, "alice")
		var deadline time.Time
		if err := f.admin.QueryRow(f.ctx, "SELECT request_deadline FROM mcpguard_ra.access_requests WHERE org_id=$1 AND request_id=$2", r.OrgID, r.RequestID).Scan(&deadline); err != nil {
			t.Fatal("read persisted review deadline: ", err)
		}
		f.awaitDeadline(t, deadline.Format(time.RFC3339Nano))
		out, e := f.s.Status(f.ctx, f.people["alice"], r.RequestID)
		if e != nil || out.Request.State != "EXPIRED" {
			t.Fatal("review deadline not terminal")
		}
		f = newAccessFixture(t, 3600)
		r = f.request(t, "alice")
		in := approveInput(r)
		soon := f.now(t).UTC().Add(1500 * time.Millisecond).Format(time.RFC3339Nano)
		in.ApprovedExpiry = &soon
		approved, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, in)
		if e != nil {
			t.Fatal(e)
		}
		f.awaitDeadline(t, approved.Grant.ExpiresAt)
		g, e := f.s.Grant(f.ctx, f.people["alice"], approved.Grant.GrantID)
		if e != nil || g.State != "EXPIRED" || g.Revision != "2" {
			t.Fatal("provisioning grant expiry not terminal")
		}
		out, e = f.s.Status(f.ctx, f.people["alice"], r.RequestID)
		if e != nil || out.Request.State != "APPROVED" || out.Grant.State != "EXPIRED" {
			t.Fatal("expiry rewrote approval history")
		}
		if _, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.access_grant_heads SET state='PROVISIONING',revision=revision+1,auth_epoch=auth_epoch+1 WHERE org_id=$1 AND grant_id=$2", r.OrgID, g.GrantID); e == nil {
			t.Fatal("expired grant resurrected")
		}
	})
	t.Run("grant_invalidates_without_rewriting_decision", func(t *testing.T) {
		f := newAccessFixture(t, 3600)
		r := f.request(t, "alice")
		out, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_heads SET enabled=false,revision=revision+1 WHERE org_id=$1 AND resource_id=$2", r.OrgID, r.ResourceID); e != nil {
			t.Fatal(e)
		}
		g, e := f.s.Grant(f.ctx, f.people["alice"], out.Grant.GrantID)
		if e != nil || g.State != "REVOKED" {
			t.Fatal("invalid grant remained provisioning")
		}
		if _, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_heads SET enabled=true,revision=revision+1 WHERE org_id=$1 AND resource_id=$2", r.OrgID, r.ResourceID); e != nil {
			t.Fatal(e)
		}
		g, e = f.s.Grant(f.ctx, f.people["alice"], out.Grant.GrantID)
		if e != nil || g.State != "REVOKED" {
			t.Fatal("resource reenabling resurrected old grant")
		}
	})
	t.Run("review_queue_and_grant_cursor_scope", func(t *testing.T) {
		f := newAccessFixture(t, 3600)
		for range 3 {
			r := f.request(t, "bob")
			if _, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r)); e != nil {
				t.Fatal(e)
			}
		}
		one := 1
		page, e := f.s.Grants(f.ctx, f.people["bob"], v02.ListGrantsInput{Limit: &one})
		if e != nil || page.NextAfter == "" {
			t.Fatal("missing grant cursor")
		}
		next, e := f.s.Grants(f.ctx, f.people["bob"], v02.ListGrantsInput{After: &page.NextAfter})
		if e != nil || len(next.Grants) != 2 {
			t.Fatal("grant cursor failed")
		}
		if _, e = f.s.Grants(f.ctx, f.people["alice"], v02.ListGrantsInput{After: &page.NextAfter}); !errors.Is(e, ErrInput) {
			t.Fatal("grant cursor crossed user")
		}
		r := f.request(t, "bob")
		queue, e := f.s.ReviewQueue(f.ctx, f.people["reviewer"], v02.ListAccessRequestsInput{})
		if e != nil || len(queue.Requests) != 1 || queue.Requests[0].RequestID != r.RequestID {
			t.Fatal("review queue not resource scoped")
		}
		queue, e = f.s.ReviewQueue(f.ctx, f.people["bob"], v02.ListAccessRequestsInput{})
		if e != nil || len(queue.Requests) != 0 {
			t.Fatal("requester self-review queue")
		}
	})
	t.Run("audit_failure_rolls_back_decision_grant_and_command", func(t *testing.T) {
		f := newAccessFixture(t, 3600)
		r := f.request(t, "alice")
		suffix := uuid.NewString()[:8]
		fn := "ra_audit_" + suffix
		sql := `CREATE FUNCTION mcpguard_ra.` + fn + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.request_id='` + r.RequestID + `'::uuid AND NEW.event_type='DECISION_APPROVE' THEN RAISE EXCEPTION 'injected audit failure';END IF;RETURN NEW;END $$;CREATE TRIGGER ` + fn + ` BEFORE INSERT ON mcpguard_ra.access_audit FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.` + fn + `();`
		if _, e := f.admin.Exec(f.ctx, sql); e != nil {
			t.Fatal(e)
		}
		if _, e := f.s.Decide(f.ctx, f.people["reviewer"], r.RequestID, approveInput(r)); e == nil {
			t.Fatal("audit failure was ignored")
		}
		if f.count(t, "access_decisions", r.RequestID) != 0 || f.count(t, "access_grant_versions", r.RequestID) != 0 {
			t.Fatal("failed approval left durable grant")
		}
		out, e := f.s.Status(f.ctx, f.people["alice"], r.RequestID)
		if e != nil || out.Request.State != "PENDING_APPROVAL" {
			t.Fatal("audit failure left approved request")
		}
		if _, e = f.admin.Exec(f.ctx, "DROP TRIGGER "+fn+" ON mcpguard_ra.access_audit;DROP FUNCTION mcpguard_ra."+fn+"()"); e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.Catalog.Store.Pool.Exec(f.ctx, "DELETE FROM mcpguard_ra.access_audit WHERE org_id=$1", r.OrgID); e == nil {
			t.Fatal("runtime can delete audit")
		}
		if _, e = f.s.Catalog.Store.Pool.Exec(f.ctx, "UPDATE mcpguard_ra.access_policy_heads SET enabled=false WHERE org_id=$1", r.OrgID); e == nil {
			t.Fatal("runtime can change approval policy")
		}
	})
}
