package resources

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

// The RA-12 fixture provisions separate disposable metadata and business
// databases. Create the same scoped account and order schema as the Wave 2
// runner while exercising the real WriteService and ContextService.
type highRiskFaultFixture struct {
	*contextFixture
	writes      *WriteService
	applicant   metadata.Principal
	reviewer    metadata.Principal
	nativeAdmin *pgx.Conn
	snapshot    Snapshot
	input       HighRiskPlanInput
}

func newHighRiskFaultFixture(t *testing.T) *highRiskFaultFixture {
	t.Helper()
	f, writes := newWriteFixture(t)
	applicant := f.people["alice"]
	request := f.input()
	request.ProfileID = writeProfileID(t, f)
	reviewer := writeApprover(t, f, writes, request.ResourceID)
	submitted, err := f.s.Request(f.ctx, applicant, request)
	if err != nil {
		t.Fatal("prepare write entitlement: ", err)
	}
	decision, err := f.s.Decide(f.ctx, reviewer, submitted.RequestID, approveInput(submitted))
	if err != nil {
		t.Fatal("approve write entitlement: ", err)
	}
	f.run(t, decision, "VERIFIED")
	ticket, err := f.contexts.Prepare(f.ctx, applicant, f.proofs["alice"], v02.PrepareDownloadInput{
		GrantID: decision.Grant.GrantID, RequestKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatal("prepare activated credential: ", err)
	}
	f.activate(t, "alice", ticket, f.take(t, "alice", ticket))
	tx, epoch, err := writes.Catalog.begin(f.ctx, applicant)
	if err != nil {
		t.Fatal("open snapshot transaction: ", err)
	}
	credential, err := writes.Credentials.row(f.ctx, tx, applicant, ticket.CredentialID)
	if err != nil {
		rollback(tx)
		t.Fatal("resolve credential binding: ", err)
	}
	snapshot, err := writes.Catalog.snapshotBound(f.ctx, tx, applicant, request.ResourceID, request.ProfileID,
		credential.ref.ProfileVersion, WriteOperation, WriteToolName, epoch)
	rollback(tx)
	if err != nil {
		t.Fatal("resolve write snapshot: ", err)
	}
	result := &highRiskFaultFixture{
		contextFixture: f, writes: writes, applicant: applicant, reviewer: reviewer, snapshot: snapshot,
		input: HighRiskPlanInput{ResourceID: request.ResourceID, CredentialID: ticket.CredentialID,
			Operation: HighRiskUpdateOrder, Target: HighRiskUpdateOrderID, ExpectedVersion: 1},
	}
	for index, resource := range f.bundle.Resources {
		if resource.ResourceID == request.ResourceID {
			result.nativeAdmin = f.native[index]
			break
		}
	}
	if result.nativeAdmin == nil {
		t.Fatal("no isolated business database for the write profile")
	}
	result.installNative(t)
	notices := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var notice struct {
			NotificationID string `json:"notificationId"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&notice) != nil || notice.NotificationID == "" {
			http.Error(w, "invalid fixture notice", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(notice)
	}))
	t.Cleanup(notices.Close)
	notifier, err := OpenHighRiskNotifier(writes.Catalog.Store, notices.URL+"/wecom", notices.URL+"/feishu")
	if err != nil {
		t.Fatal("configure local receipt fixture: ", err)
	}
	if err := writes.EnableFixtureHighRisk(HighRiskPolicy{ResourceID: request.ResourceID, ReviewerID: reviewer.ID,
		Channel: HighRiskWeCom, CodeTTL: time.Minute, Notifier: notifier}); err != nil {
		t.Fatal("enable fixture high-risk operation: ", err)
	}
	return result
}

func (f *highRiskFaultFixture) installNative(t *testing.T) {
	t.Helper()
	sql, err := os.ReadFile("../../scripts/fixtures/w2_high_risk_orders.sql")
	if err != nil {
		t.Fatal("load disposable orders fixture: ", err)
	}
	if _, err := f.nativeAdmin.Exec(f.ctx, string(sql)); err != nil {
		t.Fatal("install disposable orders fixture: ", err)
	}
	t.Cleanup(func() {
		if _, err := f.nativeAdmin.Exec(f.ctx, "DROP SCHEMA mcpguard_w2_fixture CASCADE; DROP ROLE mcpguard_w2_fixture_owner"); err != nil {
			t.Error("remove test-owned orders fixture: ", err)
		}
	})
	role := pgx.Identifier{f.snapshot.Binding.NativeIdentity}.Sanitize()
	if _, err := f.nativeAdmin.Exec(f.ctx, `GRANT USAGE ON SCHEMA mcpguard_w2_fixture TO `+role+`;
 GRANT EXECUTE ON FUNCTION mcpguard_w2_fixture.update_order_amount(text,bigint),
    mcpguard_w2_fixture.create_orders_index(text), mcpguard_w2_fixture.delete_cancelled_order(text,bigint) TO `+role); err != nil {
		t.Fatal("grant only the fixed fixture operations: ", err)
	}
}

func (f *highRiskFaultFixture) approve(t *testing.T) (id, code string) {
	t.Helper()
	planned, err := f.writes.PlanHighRisk(f.ctx, f.applicant, f.input, uuid.NewString())
	if err != nil || planned.State != "DEFERRED" {
		t.Fatalf("defer amount update: state=%s err=%v", planned.State, err)
	}
	notifier := f.writes.HighRisk.Notifier
	if err := notifier.Deliver(f.ctx); err != nil {
		t.Fatal("deliver approval-required notification: ", err)
	}
	approved, err := f.writes.Approve(f.ctx, f.reviewer, planned.RequestID, true, "approve isolated fault acceptance")
	if err != nil || approved.State != "APPROVED" {
		t.Fatalf("approve amount update: state=%s err=%v", approved.State, err)
	}
	if err := notifier.Deliver(f.ctx); err != nil {
		t.Fatal("deliver approval notice: ", err)
	}
	code, _, err = f.writes.ClaimHighRiskCode(f.ctx, f.applicant, approved.RequestID)
	if err != nil {
		t.Fatal("claim fresh test authorization code: ", err)
	}
	return approved.RequestID, code
}

func (f *highRiskFaultFixture) resume(id, code string) (WriteStatus, error) {
	return f.writes.ResumeHighRisk(f.ctx, f.applicant, id, code, f.contexts, f.proofs["alice"])
}

func (f *highRiskFaultFixture) orderSnapshot(t *testing.T) string {
	t.Helper()
	var snapshot string
	if err := f.nativeAdmin.QueryRow(f.ctx, `SELECT jsonb_agg(o ORDER BY tenant_id,order_id)::text
 FROM mcpguard_w2_fixture.orders o`).Scan(&snapshot); err != nil {
		t.Fatal("snapshot native fixture orders: ", err)
	}
	return snapshot
}

func TestHighRiskAuditWriteFailure(t *testing.T) {
	f := newHighRiskFaultFixture(t)
	id, code := f.approve(t)
	before := f.orderSnapshot(t)
	sequence := "audit_fault_count_" + uuid.New().String()[:8]
	function := "reject_dispatch_" + uuid.New().String()[:8]
	trigger := pgx.Identifier{function}.Sanitize()
	qualifiedFunction := pgx.Identifier{"mcpguard_ra", function}.Sanitize()
	qualifiedSequence := pgx.Identifier{"mcpguard_ra", sequence}.Sanitize()
	// The sequence marks exactly the rejected INSERT, independently of rollback.
	if _, err := f.admin.Exec(f.ctx, `CREATE SEQUENCE `+qualifiedSequence+`;
 GRANT USAGE ON SEQUENCE `+qualifiedSequence+` TO mcpguard_contexts;
 CREATE FUNCTION `+qualifiedFunction+`() RETURNS trigger LANGUAGE plpgsql AS $fault$
 BEGIN PERFORM nextval('mcpguard_ra.`+sequence+`'); RAISE EXCEPTION 'isolated audit storage failure'; END $fault$;
 CREATE TRIGGER `+trigger+` BEFORE INSERT ON mcpguard_ra.high_risk_audit FOR EACH ROW
 WHEN (NEW.org_id='`+f.applicant.OrgID+`' AND NEW.action_id='`+id+`' AND NEW.event_kind='DISPATCH_INTENT')
 EXECUTE FUNCTION `+qualifiedFunction+`()`); err != nil {
		t.Fatal("install scoped test-only audit failure: ", err)
	}
	t.Cleanup(func() {
		_, err := f.admin.Exec(f.ctx, `DROP TRIGGER IF EXISTS `+trigger+` ON mcpguard_ra.high_risk_audit;
 DROP FUNCTION `+qualifiedFunction+`(); DROP SEQUENCE `+qualifiedSequence)
		if err != nil {
			t.Error("remove test-only audit failure: ", err)
		}
	})
	if _, err := f.resume(id, code); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("the critical audit failure was not returned safely: %v", err)
	}
	var attempts int
	var seen bool
	if err := f.admin.QueryRow(f.ctx, "SELECT last_value, is_called FROM "+qualifiedSequence).Scan(&attempts, &seen); err != nil || !seen || attempts != 1 {
		t.Fatalf("expected exactly one intentional audit insert failure: attempts=%d seen=%t err=%v", attempts, seen, err)
	}
	var state string
	var unconsumed bool
	var critical, ledgerIntent int
	if err := f.admin.QueryRow(f.ctx, `SELECT w.state, h.consumed_at IS NULL AND h.dispatch_at IS NULL
   AND h.code_digest IS NOT NULL AND h.code_ciphertext IS NOT NULL,
   (SELECT count(*) FROM mcpguard_ra.high_risk_audit a WHERE a.org_id=h.org_id AND a.action_id=h.action_id
      AND a.event_kind IN ('CODE_CONSUMED','DISPATCH_INTENT')),
   (SELECT count(*) FROM mcpguard_ra.write_audit a WHERE a.org_id=h.org_id AND a.request_id=h.action_id
      AND a.event_type='DISPATCH_INTENT')
 FROM mcpguard_ra.high_risk_writes h JOIN mcpguard_ra.write_requests w
 ON w.org_id=h.org_id AND w.request_id=h.action_id WHERE h.org_id=$1 AND h.action_id=$2`,
		f.applicant.OrgID, id).Scan(&state, &unconsumed, &critical, &ledgerIntent); err != nil {
		t.Fatal("read back audit and authorization after fault: ", err)
	}
	if state != "APPROVED" || !unconsumed || critical != 0 || ledgerIntent != 0 {
		t.Fatalf("failed intent did not roll the claim back: state=%s unconsumed=%t critical=%d ledgerIntent=%d",
			state, unconsumed, critical, ledgerIntent)
	}
	if pools := f.writes.Manager.PoolCount(); pools != 0 || f.orderSnapshot(t) != before {
		t.Fatalf("failed critical audit reached native dispatch: nativePools=%d", pools)
	}
	if _, err := f.admin.Exec(f.ctx, "DROP TRIGGER "+trigger+" ON mcpguard_ra.high_risk_audit"); err != nil {
		t.Fatal("restore audit storage before an explicit resubmission: ", err)
	}
	if err := f.writes.RecoverHighRisk(f.ctx); err != nil {
		t.Fatal("check recovery after audit storage returns: ", err)
	}
	if f.writes.Manager.PoolCount() != 0 || f.orderSnapshot(t) != before {
		t.Fatal("restoring audit storage led to automatic dispatch")
	}
	result, err := f.resume(id, code)
	if err != nil || result.State != "SUCCEEDED" || result.Result == nil || result.Result.Native == nil ||
		result.Result.Native.AffectedRows != 1 || result.Result.Native.NewVersion != 2 {
		t.Fatalf("explicit submission after zero dispatch should update once: state=%s err=%v", result.State, err)
	}
	var amount, version int
	if err := f.nativeAdmin.QueryRow(f.ctx, `SELECT amount, version FROM mcpguard_w2_fixture.orders
 WHERE tenant_id='tenant_a' AND order_id='order_recalc_1'`).Scan(&amount, &version); err != nil || amount != 120 || version != 2 {
		t.Fatalf("explicit resubmission native result: amount=%d version=%d err=%v", amount, version, err)
	}
	if err := f.admin.QueryRow(f.ctx, `SELECT count(*) FROM mcpguard_ra.high_risk_audit
 WHERE org_id=$1 AND action_id=$2 AND event_kind='DISPATCH_INTENT'`, f.applicant.OrgID, id).Scan(&ledgerIntent); err != nil || ledgerIntent != 1 {
		t.Fatalf("explicit resubmission dispatch intent count: %d err=%v", ledgerIntent, err)
	}
	t.Logf("actionId=%s faultAttempts=1 faultState=APPROVED codeUnconsumed=true preRecoveryDispatchIntents=0 preRecoveryNativePools=0 nativeRowsBeforeRetryUnchanged=true explicitRetryDispatchIntents=1 explicitRetryAffectedRows=1 nativeAmount=120 nativeVersion=2", id)
}
