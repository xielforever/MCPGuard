package resources

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type downloadFixture struct {
	*credentialFixture
	download *DownloadService
	tokens   map[string]string
}

func newDownloadFixture(t *testing.T) *downloadFixture {
	t.Helper()
	path := os.Getenv("MCPGUARD_RA_CONFIG")
	if path == "" {
		t.Skip("explicit RA-08 fixture required")
	}
	raw, e := os.ReadFile(path)
	var scene struct {
		Download bool `json:"raDownloadPhase"`
	}
	if e != nil || json.Unmarshal(raw, &scene) != nil {
		t.Fatal("invalid private fixture")
	}
	if !scene.Download {
		t.Skip("explicit RA-08 fixture required")
	}
	f := newCredentialFixture(t)
	store, e := metadata.Open(f.ctx, os.Getenv("MCPGUARD_TEST_DELIVERY_DSN"), f.s.Catalog.Store.Vault)
	if e != nil {
		t.Fatal("download runtime unavailable")
	}
	t.Cleanup(store.Close)
	catalog, e := NewCatalog(f.ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	credentials, e := NewCredentialService(catalog, f.keys)
	if e != nil {
		t.Fatal(e)
	}
	service, e := NewDownloadService(credentials, 60, 10)
	if e != nil {
		t.Fatal(e)
	}
	enrollment, e := NewDownloadService(f.service, 60, 10)
	if e != nil {
		t.Fatal(e)
	}
	tokens := map[string]string{}
	for _, name := range []string{"alice", "bob"} {
		_, token, e := enrollment.EnrollDelivery(f.ctx, f.people[name], 600)
		if e != nil {
			t.Fatal("private enrollment failed: ", e)
		}
		tokens[name] = token
	}
	return &downloadFixture{f, service, tokens}
}
func (f *downloadFixture) prepare(t *testing.T, name string, index int) (v02.PrepareDownloadResult, v02.AccessStatusResult) {
	t.Helper()
	out := f.approved(t, name, index)
	f.run(t, out, "VERIFIED")
	ticket, e := f.download.Prepare(f.ctx, f.people[name], v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()})
	if e != nil {
		t.Fatal(e)
	}
	return ticket, out
}
func (f *downloadFixture) status(t *testing.T, name, id string) v02.DownloadTicket {
	t.Helper()
	out, e := f.download.Status(f.ctx, f.people[name], f.tokens[name], v02.DownloadIDInput{DownloadID: id})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *downloadFixture) deliver(name, id string, send func(v02.DownloadTicket, string, []byte) error) error {
	return f.download.Deliver(f.ctx, f.people[name], f.tokens[name], v02.DownloadIDInput{DownloadID: id}, send)
}
func discardDownload(v02.DownloadTicket, string, []byte) error { return nil }
func (f *downloadFixture) faultAudit(t *testing.T, event string) {
	t.Helper()
	org := f.people["alice"].OrgID
	suffix := strings.ReplaceAll(org, "-", "")
	sql := `CREATE FUNCTION mcpguard_ra.download_fault_` + suffix + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.org_id='` + org + `' AND NEW.event_type='` + event + `' THEN RAISE EXCEPTION 'injected private delivery fault';END IF;RETURN NEW;END $$;CREATE TRIGGER download_fault_` + suffix + ` BEFORE INSERT ON mcpguard_ra.download_audit FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.download_fault_` + suffix + `();`
	if _, e := f.admin.Exec(f.ctx, sql); e != nil {
		t.Fatal(e)
	}
}

func TestPrivateDownloadsIntegration(t *testing.T) {
	t.Run("idempotency_payload_conflict_and_command_redaction", func(t *testing.T) {
		f := newDownloadFixture(t)
		one := f.approved(t, "alice", 0)
		two := f.approved(t, "alice", 1)
		f.run(t, one, "VERIFIED")
		f.run(t, two, "VERIFIED")
		in := v02.PrepareDownloadInput{GrantID: one.Grant.GrantID, RequestKey: uuid.NewString()}
		if _, e := f.download.Prepare(f.ctx, f.people["alice"], in); e != nil {
			t.Fatal(e)
		}
		var stored string
		if f.admin.QueryRow(f.ctx, "SELECT request_key FROM mcpguard_ra.download_commands WHERE org_id=$1", one.Request.OrgID).Scan(&stored) != nil || stored == in.RequestKey {
			t.Fatal("raw caller request key persisted")
		}
		in.GrantID = two.Grant.GrantID
		if _, e := f.download.Prepare(f.ctx, f.people["alice"], in); !errors.Is(e, ErrIdempotency) || f.count(t, "download_tickets") != 1 {
			t.Fatal("same request key accepted different grant")
		}
	})
	t.Run("copied_ticket_cannot_cross_organization", func(t *testing.T) {
		f := newDownloadFixture(t)
		ticket, _ := f.prepare(t, "alice", 0)
		other := newDownloadFixture(t)
		called := false
		e := other.download.Deliver(other.ctx, other.people["alice"], other.tokens["alice"], v02.DownloadIDInput{DownloadID: ticket.DownloadID}, func(v02.DownloadTicket, string, []byte) error { called = true; return nil })
		if !errors.Is(e, ErrDenied) || called {
			t.Fatal("ticket crossed organization")
		}
	})
	t.Run("two_resources_two_people_private_claim", func(t *testing.T) {
		f := newDownloadFixture(t)
		for index := 0; index < 2; index++ {
			for _, name := range []string{"alice", "bob"} {
				ticket, out := f.prepare(t, name, index)
				var blob []byte
				e := f.deliver(name, ticket.DownloadID, func(meta v02.DownloadTicket, digest string, raw []byte) error {
					if meta.CredentialID != ticket.CredentialID || meta.State != "CLAIMED" || meta.DeliveryOutcome != "UNKNOWN" || len(digest) != 64 {
						t.Fatal("private metadata mismatch")
					}
					blob = append([]byte{}, raw...)
					return nil
				})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.service.Validate(f.ctx, f.people[name], out.Grant.ResourceID, ticket.CredentialID, blob); e != nil {
					t.Fatal("downloaded encrypted reference failed validation")
				}
				status := f.status(t, name, ticket.DownloadID)
				if status.State != "CLAIMED" || status.DeliveryOutcome != "WRITE_COMPLETED" || status.Revision != "2" {
					t.Fatal("claim status missing")
				}
			}
		}
		if f.count(t, "download_tickets") != 4 || f.count(t, "credential_packages") != 4 || f.count(t, "native_accounts") != 4 {
			t.Fatal("resource/user identities crossed")
		}
	})
	t.Run("prepare_replay_then_new_ticket_same_package_no_account", func(t *testing.T) {
		f := newDownloadFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		input := v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}
		one, e := f.download.Prepare(f.ctx, f.people["alice"], input)
		if e != nil {
			t.Fatal(e)
		}
		replay, e := f.download.Prepare(f.ctx, f.people["alice"], input)
		if e != nil || replay != one {
			t.Fatal("prepare replay created ticket")
		}
		var nativeBefore int
		if f.native[0].QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_provision.accounts").Scan(&nativeBefore) != nil {
			t.Fatal("native inventory unavailable")
		}
		if e = f.deliver("alice", one.DownloadID, discardDownload); e != nil {
			t.Fatal(e)
		}
		replay, e = f.download.Prepare(f.ctx, f.people["alice"], input)
		if e != nil || replay.DownloadID != one.DownloadID || replay.State != "CLAIMED" {
			t.Fatal("claimed idempotency replay revived ticket")
		}
		input.RequestKey = uuid.NewString()
		two, e := f.download.Prepare(f.ctx, f.people["alice"], input)
		if e != nil || two.DownloadID == one.DownloadID || two.CredentialID != one.CredentialID || two.LeaseID != one.LeaseID || two.CredentialExpiresAt != one.CredentialExpiresAt {
			t.Fatal("new ticket changed package or extended lease")
		}
		var nativeAfter int
		if f.native[0].QueryRow(f.ctx, "SELECT count(*) FROM mcpguard_provision.accounts").Scan(&nativeAfter) != nil || nativeAfter != nativeBefore || f.count(t, "native_accounts") != 1 || f.count(t, "provisioning_jobs") != 1 || f.count(t, "credential_packages") != 1 || f.count(t, "credential_leases") != 1 {
			t.Fatal("new ticket created account/grant/package")
		}
	})
	t.Run("concurrent_prepare_one_package_and_one_ticket", func(t *testing.T) {
		f := newDownloadFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		input := v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}
		var wg sync.WaitGroup
		var failures atomic.Int32
		results := make(chan v02.PrepareDownloadResult, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, e := f.download.Prepare(f.ctx, f.people["alice"], input)
				if e != nil {
					failures.Add(1)
				} else {
					results <- out
				}
			}()
		}
		wg.Wait()
		close(results)
		var first v02.PrepareDownloadResult
		for out := range results {
			if first.DownloadID == "" {
				first = out
			} else if first != out {
				t.Fatal("concurrent prepare lost idempotency")
			}
		}
		if failures.Load() != 0 || f.count(t, "download_tickets") != 1 || f.count(t, "credential_packages") != 1 {
			t.Fatal("concurrent prepare duplicated delivery")
		}
	})
	t.Run("concurrent_claim_one_delivery_across_service_instances", func(t *testing.T) {
		f := newDownloadFixture(t)
		ticket, _ := f.prepare(t, "alice", 0)
		other, e := NewDownloadService(f.download.Credentials, 60, 10)
		if e != nil {
			t.Fatal(e)
		}
		var wg sync.WaitGroup
		var writes, successes, unexpected atomic.Int32
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				s := f.download
				if i%2 == 1 {
					s = other
				}
				e := s.Deliver(f.ctx, f.people["alice"], f.tokens["alice"], v02.DownloadIDInput{DownloadID: ticket.DownloadID}, func(v02.DownloadTicket, string, []byte) error { writes.Add(1); return nil })
				if e == nil {
					successes.Add(1)
				} else if !errors.Is(e, ErrConflict) {
					unexpected.Add(1)
				}
			}(i)
		}
		wg.Wait()
		if writes.Load() != 1 || successes.Load() != 1 || unexpected.Load() != 0 {
			t.Fatal("ticket delivered more than once or failed unexpectedly")
		}
	})
	t.Run("independent_private_authorization_required", func(t *testing.T) {
		f := newDownloadFixture(t)
		ticket, _ := f.prepare(t, "alice", 0)
		called := false
		for _, attempt := range []struct{ name, token string }{{"alice", ""}, {"alice", f.tokens["bob"]}, {"bob", f.tokens["alice"]}, {"alice", strings.Repeat("A", 43)}} {
			e := f.download.Deliver(f.ctx, f.people[attempt.name], attempt.token, v02.DownloadIDInput{DownloadID: ticket.DownloadID}, func(v02.DownloadTicket, string, []byte) error { called = true; return nil })
			if !errors.Is(e, ErrDenied) {
				t.Fatal("invalid private authority accepted")
			}
		}
		if called || f.status(t, "alice", ticket.DownloadID).State != "READY" {
			t.Fatal("denied claim consumed or disclosed file")
		}
	})
	t.Run("expired_authorization_does_not_consume_ticket", func(t *testing.T) {
		f := newDownloadFixture(t)
		ticket, _ := f.prepare(t, "alice", 0)
		enrollment, _ := NewDownloadService(f.service, 60, 10)
		authorization, token, e := enrollment.EnrollDelivery(f.ctx, f.people["alice"], 1)
		if e != nil {
			t.Fatal(e)
		}
		f.awaitDeadline(t, authorization.ExpiresAt)
		if e = f.download.Deliver(f.ctx, f.people["alice"], token, v02.DownloadIDInput{DownloadID: ticket.DownloadID}, discardDownload); !errors.Is(e, ErrDenied) {
			t.Fatal("expired private authorization used")
		}
		if f.status(t, "alice", ticket.DownloadID).State != "READY" {
			t.Fatal("expired private token consumed ticket")
		}
	})
	t.Run("expired_ticket_can_only_be_replaced_with_new_id", func(t *testing.T) {
		f := newDownloadFixture(t)
		f.download.TicketTTL = 1
		ticket, out := f.prepare(t, "alice", 0)
		f.awaitDeadline(t, ticket.ExpiresAt)
		if e := f.deliver("alice", ticket.DownloadID, discardDownload); !errors.Is(e, ErrConflict) {
			t.Fatal("expired ticket used")
		}
		if f.status(t, "alice", ticket.DownloadID).State != "EXPIRED" {
			t.Fatal("ticket expiry missing")
		}
		next, e := f.download.Prepare(f.ctx, f.people["alice"], v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()})
		if e != nil || next.CredentialID != ticket.CredentialID || next.DownloadID == ticket.DownloadID {
			t.Fatal("expired ticket replacement changed package")
		}
	})
	t.Run("ready_ticket_bound_and_subject_cap", func(t *testing.T) {
		f := newDownloadFixture(t)
		f.download.MaxReady = 2
		first, out := f.prepare(t, "alice", 0)
		expiry, _, _ := normalizedTime(first.ExpiresAt)
		leaseExpiry, _, _ := normalizedTime(first.CredentialExpiresAt)
		if expiry.After(leaseExpiry) {
			t.Fatal("ticket exceeds lease")
		}
		in := v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}
		if _, e := f.download.Prepare(f.ctx, f.people["alice"], in); e != nil {
			t.Fatal(e)
		}
		in.RequestKey = uuid.NewString()
		if _, e := f.download.Prepare(f.ctx, f.people["alice"], in); !errors.Is(e, ErrConflict) {
			t.Fatal("unbounded pending tickets")
		}
		if f.count(t, "download_tickets") != 2 || f.count(t, "credential_packages") != 1 {
			t.Fatal("ticket cap left partial state")
		}
	})
	for _, mode := range []string{"lease_revoked", "resource_disabled", "subject_disabled", "subject_expired", "token_replaced", "delivery_authorization_revoked"} {
		t.Run(mode, func(t *testing.T) {
			f := newDownloadFixture(t)
			ticket, out := f.prepare(t, "alice", 0)
			p := f.people["alice"]
			var e error
			switch mode {
			case "lease_revoked":
				_, e = f.service.Revoke(f.ctx, p, v02.RevokeCredentialInput{CredentialID: ticket.CredentialID, ExpectedRevision: "1", RequestKey: uuid.NewString(), Reason: "server revoked before delivery"})
			case "resource_disabled":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_heads SET enabled=false,revision=revision+1 WHERE org_id=$1 AND resource_id=$2", p.OrgID, out.Grant.ResourceID)
			case "subject_disabled":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET enabled=false WHERE org_id=$1 AND id=$2", p.OrgID, p.ID)
			case "subject_expired":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET expires_at=clock_timestamp()-interval '1 second' WHERE org_id=$1 AND id=$2", p.OrgID, p.ID)
			case "token_replaced":
				replacement := make([]byte, 32)
				if _, e = rand.Read(replacement); e == nil {
					_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard.principals SET token_hash=$3 WHERE org_id=$1 AND id=$2", p.OrgID, p.ID, replacement)
				}
			case "delivery_authorization_revoked":
				_, e = f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.delivery_authorizations SET state='REVOKED',revision=revision+1 WHERE org_id=$1 AND subject_id=$2", p.OrgID, p.ID)
			}
			if e != nil {
				t.Fatal(e)
			}
			var calls int
			e = f.deliver("alice", ticket.DownloadID, func(v02.DownloadTicket, string, []byte) error { calls++; return nil })
			if e == nil || calls != 0 {
				t.Fatal("closed authorization delivered bytes")
			}
		})
	}
	t.Run("runtime_has_no_native_management_or_enrollment_rights", func(t *testing.T) {
		f := newDownloadFixture(t)
		var safe bool
		e := f.download.Credentials.Access.Catalog.Store.Pool.QueryRow(f.ctx, `SELECT NOT has_table_privilege(current_user,'mcpguard_ra.native_secrets','SELECT') AND NOT has_table_privilege(current_user,'mcpguard_ra.provisioning_jobs','INSERT') AND NOT has_table_privilege(current_user,'mcpguard_ra.delivery_authorizations','INSERT') AND NOT has_table_privilege(current_user,'mcpguard_ra.download_tickets','DELETE')`).Scan(&safe)
		if e != nil || !safe {
			t.Fatal("delivery runtime overprivileged")
		}
	})
}

func TestPrivateDownloadFailureIntegration(t *testing.T) {
	t.Run("prepare_audit_failure_rolls_back_package_lease_ticket", func(t *testing.T) {
		f := newDownloadFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		f.faultAudit(t, "TICKET_READY")
		_, e := f.download.Prepare(f.ctx, f.people["alice"], v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()})
		if !errors.Is(e, ErrUnavailable) || f.count(t, "credential_packages") != 0 || f.count(t, "credential_leases") != 0 || f.count(t, "download_tickets") != 0 || f.count(t, "download_commands") != 0 || f.count(t, "credential_nonce_reservations") != 1 {
			t.Fatal("failed prepare audit partially committed")
		}
	})
	t.Run("claim_audit_failure_sends_nothing_and_does_not_consume", func(t *testing.T) {
		f := newDownloadFixture(t)
		ticket, _ := f.prepare(t, "alice", 0)
		f.faultAudit(t, "CLAIM_COMMITTED")
		var writes int
		e := f.deliver("alice", ticket.DownloadID, func(v02.DownloadTicket, string, []byte) error { writes++; return nil })
		if !errors.Is(e, ErrUnavailable) || writes != 0 || f.status(t, "alice", ticket.DownloadID).State != "READY" {
			t.Fatal("unaudited claim disclosed bytes")
		}
	})
	t.Run("partial_write_is_terminal_and_audit_drops_error_text", func(t *testing.T) {
		f := newDownloadFixture(t)
		ticket, _ := f.prepare(t, "alice", 0)
		e := f.deliver("alice", ticket.DownloadID, func(v02.DownloadTicket, string, []byte) error {
			return errors.New("private-header=" + f.tokens["alice"] + " /secret/path")
		})
		if !errors.Is(e, ErrUnavailable) {
			t.Fatal("interrupted send reported success")
		}
		status := f.status(t, "alice", ticket.DownloadID)
		if status.State != "CLAIMED" || status.DeliveryOutcome != "WRITE_INTERRUPTED" {
			t.Fatal("interruption revived claim")
		}
		if e = f.deliver("alice", ticket.DownloadID, discardDownload); !errors.Is(e, ErrConflict) {
			t.Fatal("interrupted claim replayed")
		}
		var audit string
		if f.admin.QueryRow(f.ctx, "SELECT jsonb_agg(to_jsonb(a))::text FROM mcpguard_ra.download_audit a WHERE org_id=$1", f.people["alice"].OrgID).Scan(&audit) != nil || strings.Contains(audit, f.tokens["alice"]) || strings.Contains(audit, "/secret/path") {
			t.Fatal("private error leaked to audit")
		}
	})
	t.Run("request_cancel_after_commit_keeps_claim_and_records_outcome", func(t *testing.T) {
		f := newDownloadFixture(t)
		ticket, _ := f.prepare(t, "alice", 0)
		ctx, cancel := context.WithCancel(f.ctx)
		defer cancel()
		e := f.download.Deliver(ctx, f.people["alice"], f.tokens["alice"], v02.DownloadIDInput{DownloadID: ticket.DownloadID}, func(v02.DownloadTicket, string, []byte) error { cancel(); return io.ErrUnexpectedEOF })
		if !errors.Is(e, ErrUnavailable) || f.status(t, "alice", ticket.DownloadID).DeliveryOutcome != "WRITE_INTERRUPTED" {
			t.Fatal("cancelled request lost durable outcome")
		}
	})
	t.Run("post_write_audit_failure_is_unknown_not_replayable", func(t *testing.T) {
		f := newDownloadFixture(t)
		ticket, _ := f.prepare(t, "alice", 0)
		f.faultAudit(t, "DELIVERY_WRITE_COMPLETED")
		var calls int
		e := f.deliver("alice", ticket.DownloadID, func(v02.DownloadTicket, string, []byte) error { calls++; return nil })
		if !errors.Is(e, ErrUnavailable) || calls != 1 {
			t.Fatal("post-write audit fault not surfaced")
		}
		status := f.status(t, "alice", ticket.DownloadID)
		if status.State != "CLAIMED" || status.DeliveryOutcome != "UNKNOWN" {
			t.Fatal("missing outcome guessed success or revived ticket")
		}
		if e = f.deliver("alice", ticket.DownloadID, discardDownload); !errors.Is(e, ErrConflict) {
			t.Fatal("uncertain delivery replayed")
		}
	})
	t.Run("qualification_changes_during_crypto_no_late_ticket", func(t *testing.T) {
		f := newDownloadFixture(t)
		out := f.approved(t, "alice", 0)
		f.run(t, out, "VERIFIED")
		f.download.Credentials.Keys = &faultPackageKeys{PackageKeyProvider: f.keys, beforeResolve: func() {
			_, e := f.admin.Exec(f.ctx, "UPDATE mcpguard_ra.resource_access SET can_request=false WHERE org_id=$1 AND principal_id=$2 AND resource_id=$3", out.Request.OrgID, out.Grant.SubjectID, out.Grant.ResourceID)
			if e != nil {
				t.Fatal(e)
			}
		}}
		if _, e := f.download.Prepare(f.ctx, f.people["alice"], v02.PrepareDownloadInput{GrantID: out.Grant.GrantID, RequestKey: uuid.NewString()}); !errors.Is(e, ErrDenied) || f.count(t, "download_tickets") != 0 || f.count(t, "credential_packages") != 0 {
			t.Fatal("late crypto committed revoked delivery")
		}
	})
}
