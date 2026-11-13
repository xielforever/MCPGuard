package resources

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestContextSecurityEdgesIntegration(t *testing.T) {
	t.Run("deactivation_replay_cannot_close_new_activation", func(t *testing.T) {
		f := newContextFixture(t)
		ticket, _ := f.prepareContext(t, "alice", 0)
		blob := f.take(t, "alice", ticket)
		f.activate(t, "alice", ticket, blob)
		in := v02.DeactivateInput{ResourceID: *ticket.ResourceID, RequestKey: uuid.NewString()}
		if a, e := f.contexts.Deactivate(f.ctx, f.people["alice"], f.proofs["alice"], in); e != nil || a.State != "REVOKED" {
			t.Fatal("close activation")
		}
		f.activate(t, "alice", ticket, blob)
		if a, e := f.contexts.Deactivate(f.ctx, f.people["alice"], f.proofs["alice"], in); e != nil || a.State != "ACTIVE" {
			t.Fatal("old close command changed new activation")
		}
		if a, e := f.contexts.CheckActivation(f.ctx, f.people["alice"], f.proofs["alice"], *ticket.ResourceID, ticket.CredentialID); e != nil || a.Revision != "3" {
			t.Fatal("activation history not monotonic")
		}
	})
	t.Run("concurrent_activation_appends_once_and_completes_task", func(t *testing.T) {
		f := newContextFixture(t)
		ticket, _ := f.prepareContext(t, "alice", 0)
		blob := f.take(t, "alice", ticket)
		in := activationInput(t, ticket, blob)
		var wg sync.WaitGroup
		var failed atomic.Int32
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, e := f.contexts.Activate(f.ctx, f.people["alice"], f.proofs["alice"], in); e != nil {
					failed.Add(1)
				}
			}()
		}
		wg.Wait()
		if failed.Load() != 0 || f.count(t, "context_activation_versions") != 1 || f.count(t, "download_task_receipts") != 1 {
			t.Fatal("activation race not idempotent")
		}
	})
	t.Run("activation_audit_failure_rolls_back_activation_receipt_and_command", func(t *testing.T) {
		f := newContextFixture(t)
		ticket, _ := f.prepareContext(t, "alice", 0)
		blob := f.take(t, "alice", ticket)
		org := f.people["alice"].OrgID
		suffix := strings.ReplaceAll(org, "-", "")
		_, e := f.admin.Exec(f.ctx, `CREATE FUNCTION mcpguard_ra.context_fault_`+suffix+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.org_id='`+org+`' AND NEW.event_type='ACTIVATED' THEN RAISE EXCEPTION 'injected context audit fault';END IF;RETURN NEW;END $$;CREATE TRIGGER context_fault_`+suffix+` BEFORE INSERT ON mcpguard_ra.context_audit FOR EACH ROW EXECUTE FUNCTION mcpguard_ra.context_fault_`+suffix+`();`)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.contexts.Activate(f.ctx, f.people["alice"], f.proofs["alice"], activationInput(t, ticket, blob)); !errors.Is(e, ErrUnavailable) {
			t.Fatal("audit outage not fail closed")
		}
		if f.count(t, "context_activation_versions") != 0 || f.count(t, "download_task_receipts") != 0 {
			t.Fatal("audit failure left partial activation")
		}
	})
	t.Run("policy_changes_deny_renewal_and_copied_file", func(t *testing.T) {
		f := newContextFixture(t)
		ticket, _ := f.prepareContext(t, "alice", 0)
		blob := f.take(t, "alice", ticket)
		f.activate(t, "alice", ticket, blob)
		f.config.Policies[0].Version = "2"
		f.config.Policies[0].Enabled = false
		f.apply(t)
		if _, e := f.contexts.Activate(f.ctx, f.people["alice"], f.proofs["alice"], activationInput(t, ticket, blob)); e == nil {
			t.Fatal("policy revocation bypassed")
		}
		if _, e := f.contexts.RenewCredential(f.ctx, f.people["alice"], f.proofs["alice"], v02.RenewCredentialInput{CredentialID: ticket.CredentialID, RequestKey: uuid.NewString()}); e == nil {
			t.Fatal("revoked policy renewed package")
		}
		f.config.Policies[0].Version = "3"
		f.config.Policies[0].Enabled = true
		f.apply(t)
		if _, e := f.contexts.CheckActivation(f.ctx, f.people["alice"], f.proofs["alice"], *ticket.ResourceID, ticket.CredentialID); !errors.Is(e, ErrDenied) {
			t.Fatal("new policy resurrected old lease")
		}
	})
	t.Run("commands_audit_store_verifiers_and_typed_metadata_only", func(t *testing.T) {
		f := newContextFixture(t)
		ticket, _ := f.prepareContext(t, "alice", 0)
		blob := f.take(t, "alice", ticket)
		f.activate(t, "alice", ticket, blob)
		var env v02.CredentialEnvelope
		if json.Unmarshal(blob, &env) != nil {
			t.Fatal("packet")
		}
		for _, table := range []string{"credential_contexts", "context_commands", "context_audit", "context_activation_versions", "download_task_receipts"} {
			var data string
			if f.admin.QueryRow(f.ctx, "SELECT coalesce(jsonb_agg(to_jsonb(t)),'[]'::jsonb)::text FROM mcpguard_ra."+table+" t WHERE org_id=$1", f.people["alice"].OrgID).Scan(&data) != nil {
				t.Fatal("audit query")
			}
			for _, secret := range []string{f.clientTokens["alice"], f.proofs["alice"].secret, env.Ciphertext, env.AuthenticationTag, env.Nonce} {
				if strings.Contains(data, secret) {
					t.Fatal("private value in ", table)
				}
			}
		}
	})
}
