package resources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The fixture writer sends its normal COMMIT to PostgreSQL. The wrapper reads
// and verifies the server's successful response, then closes before pgx sees
// it. Both the independently observed commit and the client's loss are needed.
type commitReplyFault struct {
	armed          atomic.Bool
	awaitingReply  atomic.Bool
	serverAck      atomic.Bool
	nativeAttempts atomic.Int32
	commitAttempts atomic.Int32
}

func (f *commitReplyFault) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	switch {
	case query.SQL == updateOrderAmountSQL:
		f.nativeAttempts.Add(1)
	case strings.EqualFold(query.SQL, "commit"):
		f.commitAttempts.Add(1)
		if f.armed.Swap(false) {
			f.awaitingReply.Store(true)
		}
	}
	return ctx
}

func (*commitReplyFault) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type commitReplyConn struct {
	net.Conn
	fault *commitReplyFault
}

func (c *commitReplyConn) Read(buf []byte) (int, error) {
	if !c.fault.awaitingReply.Swap(false) {
		return c.Conn.Read(buf)
	}
	frontend := pgproto3.NewFrontend(c.Conn, io.Discard)
	response, err := frontend.Receive()
	if err != nil {
		_ = c.Conn.Close()
		return 0, err
	}
	tag, ok := response.(*pgproto3.CommandComplete)
	if !ok || string(tag.CommandTag) != "COMMIT" {
		_ = c.Conn.Close()
		return 0, errors.New("expected PostgreSQL to acknowledge COMMIT")
	}
	response, err = frontend.Receive()
	_ = c.Conn.Close()
	if err != nil {
		return 0, err
	}
	ready, ok := response.(*pgproto3.ReadyForQuery)
	if !ok || ready.TxStatus != 'I' {
		return 0, errors.New("expected an idle PostgreSQL transaction after COMMIT")
	}
	c.fault.serverAck.Store(true)
	return 0, io.EOF
}

func useCommitReplyFault(t *testing.T, fixture *highRiskFaultFixture, manager *ReadManager, fault *commitReplyFault) {
	t.Helper()
	pool, err := manager.writePool(fixture.ctx, fixture.applicant, fixture.snapshot)
	if err != nil {
		t.Fatal("resolve isolated native pool: ", err)
	}
	config := pool.Config()
	config.MaxConns = 1
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.ConnConfig.Tracer = fault
	dial := config.ConnConfig.DialFunc
	config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &commitReplyConn{Conn: conn, fault: fault}, nil
	}
	replacement, err := pgxpool.NewWithConfig(fixture.ctx, config)
	if err != nil {
		t.Fatal("instrument isolated native pool: ", err)
	}
	t.Cleanup(replacement.Close)
	manager.mu.Lock()
	for key, value := range manager.pools {
		if value == pool {
			manager.pools[key] = replacement
			manager.mu.Unlock()
			pool.Close()
			return
		}
	}
	manager.mu.Unlock()
	t.Fatal("isolated native pool was not registered")
}

func TestHighRiskFaultCommitOutcomeUnknownNeverRetries(t *testing.T) {
	f := newHighRiskFaultFixture(t)
	id, code := f.approve(t)
	fault := &commitReplyFault{}
	useCommitReplyFault(t, f, f.writes.Manager, fault)
	fault.armed.Store(true)

	assertUnknown := func(status WriteStatus, err error) {
		t.Helper()
		if err != nil || status.State != "OUTCOME_UNKNOWN" || !status.DispatchIntent || status.HasResult || status.Result != nil {
			t.Fatalf("expected OUTCOME_UNKNOWN with a dispatch intent and no result: state=%s intent=%t hasResult=%t err=%v",
				status.State, status.DispatchIntent, status.HasResult, err)
		}
	}
	assertNative := func(amount, version int) {
		t.Helper()
		var targetAmount, targetVersion, otherAmount, otherVersion int
		err := f.nativeAdmin.QueryRow(f.ctx, `
SELECT a.amount,a.version,b.amount,b.version
FROM mcpguard_w2_fixture.orders a CROSS JOIN mcpguard_w2_fixture.orders b
WHERE a.tenant_id='tenant_a' AND a.order_id=$1 AND b.tenant_id='tenant_b' AND b.order_id=$1`, HighRiskUpdateOrderID).
			Scan(&targetAmount, &targetVersion, &otherAmount, &otherVersion)
		if err != nil || targetAmount != amount || targetVersion != version || otherAmount != 100 || otherVersion != 1 {
			t.Fatalf("native readback differs: target=%d/v%d want=%d/v%d comparison=%d/v%d err=%v",
				targetAmount, targetVersion, amount, version, otherAmount, otherVersion, err)
		}
	}
	assertOneEffect := func() {
		t.Helper()
		assertNative(120, 2)
		if !fault.serverAck.Load() || fault.nativeAttempts.Load() != 1 || fault.commitAttempts.Load() != 1 {
			t.Fatalf("expected one native execution and verified COMMIT response: executions=%d commits=%d acknowledged=%t",
				fault.nativeAttempts.Load(), fault.commitAttempts.Load(), fault.serverAck.Load())
		}
	}
	assertNative(100, 1)
	assertUnknown(f.resume(id, code))
	assertOneEffect()

	// Reconstruct the gateway service and native pools from persisted metadata.
	previous := f.writes.Manager
	previous.Close()
	manager := NewReadManager(f.writes.Catalog, previous.provider)
	manager.accountSecrets = previous.accountSecrets
	t.Cleanup(manager.Close)
	useCommitReplyFault(t, f, manager, fault)
	restarted := NewWriteService(f.writes.Catalog, f.writes.Credentials, manager)
	restarted.Fence = f.writes.Fence
	if err := restarted.EnableFixtureHighRisk(*f.writes.HighRisk); err != nil {
		t.Fatal("enable high-risk recovery on the recreated service: ", err)
	}
	if err := restarted.RecoverHighRisk(f.ctx); err != nil {
		t.Fatal("recover persisted writes: ", err)
	}
	assertUnknown(restarted.Status(f.ctx, f.applicant, id))
	if _, err := restarted.ResumeHighRisk(f.ctx, f.applicant, id, code, f.contexts, f.proofs["alice"]); !errors.Is(err, ErrDenied) {
		t.Fatalf("explicit repeat after unknown outcome should be denied: %v", err)
	}
	if err := restarted.RecoverHighRisk(f.ctx); err != nil {
		t.Fatal("run another recovery pass: ", err)
	}
	assertUnknown(restarted.Status(f.ctx, f.applicant, id))
	assertOneEffect()

	evidence, err := restarted.HighRiskEvidence(f.ctx, f.applicant, id)
	if err != nil || evidence.State != "OUTCOME_UNKNOWN" {
		t.Fatalf("read correlated evidence: state=%s err=%v", evidence.State, err)
	}
	events := map[string]int{}
	for _, event := range evidence.Events {
		events[event.Kind]++
		if event.Kind == "OUTCOME_UNKNOWN" || event.Kind == "CODE_DENIED" {
			var detail struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(event.Detail, &detail); err != nil {
				t.Fatal("decode audit reason: ", err)
			}
			want := "NATIVE_COMMIT_UNCONFIRMED"
			if event.Kind == "CODE_DENIED" {
				want = "ALREADY_CONSUMED"
			}
			if detail.Reason != want {
				t.Fatalf("audit %s reason=%q, want %q", event.Kind, detail.Reason, want)
			}
		}
	}
	for _, once := range []string{"CODE_CONSUMED", "DISPATCH_INTENT", "OUTCOME_UNKNOWN", "CODE_DENIED"} {
		if events[once] != 1 {
			t.Fatalf("expected one %s record, got %d", once, events[once])
		}
	}
	if events["SUCCEEDED"] != 0 {
		t.Fatal("the gateway recorded a successful result despite the lost COMMIT response")
	}
	t.Logf("actionId=%s serverCommitAcknowledged=true gatewayState=OUTCOME_UNKNOWN nativeExecutions=1 nativeCommits=1 "+
		"dispatchIntents=1 codeConsumptions=1 target=120/v2 comparison=100/v1 recoveryPasses=2 replay=ALREADY_CONSUMED", id)
}
