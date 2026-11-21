// mcpguard-catalog performs explicit, administrative catalog migrations/imports.
// It never publishes business tools or prints native credentials.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/resources"
	"os"
	"regexp"
	"strconv"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mcpguard-catalog migrate [--dry-run] | import <catalog.json> | import-access <access.json> | import-provisioning <provisioning.json> | import-credentials <credentials.json> | grant-runtime <role> | grant-access-runtime <role> | grant-provision-runtime <role> | grant-credential-runtime <role> | grant-credential-status-runtime <role>")
		os.Exit(2)
	}
	dsn := os.Getenv("MCPGUARD_MIGRATION_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "MCPGUARD_MIGRATION_DSN is required")
		os.Exit(2)
	}
	var err error
	output := ""
	switch os.Args[1] {
	case "revoke":
		if len(os.Args) < 6 || len(os.Args) > 7 {
			err = resources.ErrInput
			break
		}
		kind, target, org, actorID := os.Args[2], os.Args[3], os.Args[4], os.Args[5]
		if kind != "CREDENTIAL" && kind != "GRANT" && kind != "PRINCIPAL" && kind != "RESOURCE" {
			err = resources.ErrInput
			break
		}
		if !regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`).MatchString(target) || !regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`).MatchString(org) || !regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`).MatchString(actorID) {
			err = resources.ErrInput
			break
		}
		reason := ""
		if len(os.Args) == 7 {
			reason = os.Args[6]
		}
		encKey, e := base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_ENCRYPTION_KEY"))
		if e != nil {
			err = resources.ErrInput
			break
		}
		planKey, e := base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_PLAN_KEY"))
		if e != nil {
			err = resources.ErrInput
			break
		}
		vault, e := metadata.NewVault(encKey, planKey)
		if e != nil {
			err = resources.ErrInput
			break
		}
		storeDSN := os.Getenv("MCPGUARD_METADATA_DSN")
		if storeDSN == "" {
			storeDSN = dsn
		}
		pool, e := pgxpool.New(ctx, storeDSN)
		if e != nil {
			err = resources.ErrUnavailable
			break
		}
		defer pool.Close()
		var result resources.RevocationResult
		result, err = resources.NewRevocationService(pool, vault).Revoke(ctx, metadata.Principal{OrgID: org, ID: actorID}, kind, target, reason)
		if err != nil {
			break
		}
		raw, _ := json.Marshal(result)
		output = string(raw)
	case "restore-check", "restore-accept":
		accept := os.Args[1] == "restore-accept"
		if (accept && len(os.Args) != 4) || (!accept && len(os.Args) != 3) {
			err = resources.ErrInput
			break
		}
		idPattern := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
		org := os.Args[2]
		if !idPattern.MatchString(org) || (accept && !idPattern.MatchString(os.Args[3])) {
			err = resources.ErrInput
			break
		}
		private, e := resources.LoadProviderPrivate(os.Getenv("MCPGUARD_PROVIDER_CONFIG"))
		if e != nil {
			err = resources.ErrInput
			break
		}
		pool, e := pgxpool.New(ctx, dsn)
		if e != nil {
			err = resources.ErrUnavailable
			break
		}
		defer pool.Close()
		fence := resources.NewRestoreFence(pool, &resources.PGProvisioningProvider{Private: private})
		native, e := fence.NativeMax(ctx, org)
		if e != nil {
			err = e
			break
		}
		expected, e := fence.Expected(ctx, org)
		if e != nil {
			err = e
			break
		}
		if accept {
			if e := fence.Record(ctx, org, native, os.Args[3]); e != nil {
				err = e
				break
			}
			expected = native
		}
		raw, _ := json.Marshal(map[string]any{"orgId": org, "native": native, "expected": expected, "suspect": native != expected})
		output = string(raw)
		if native != expected {
			err = resources.ErrConflict
		}
	case "revocation-cleanup":
		if len(os.Args) < 4 {
			err = resources.ErrInput
			break
		}
		mode, org := os.Args[2], os.Args[3]
		if !regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`).MatchString(org) {
			err = resources.ErrInput
			break
		}
		encKey, e := base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_ENCRYPTION_KEY"))
		if e != nil {
			err = resources.ErrInput
			break
		}
		planKey, e := base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_PLAN_KEY"))
		if e != nil {
			err = resources.ErrInput
			break
		}
		vault, e := metadata.NewVault(encKey, planKey)
		if e != nil {
			err = resources.ErrInput
			break
		}
		storeDSN := os.Getenv("MCPGUARD_METADATA_DSN")
		if storeDSN == "" {
			storeDSN = dsn
		}
		pool, e := pgxpool.New(ctx, storeDSN)
		if e != nil {
			err = resources.ErrUnavailable
			break
		}
		defer pool.Close()
		service := resources.NewRevocationService(pool, vault)
		if providerPath := os.Getenv("MCPGUARD_PROVIDER_CONFIG"); providerPath != "" {
			private, e := resources.LoadProviderPrivate(providerPath)
			if e != nil {
				err = resources.ErrInput
				break
			}
			service.Fence = resources.NewRestoreFence(pool, &resources.PGProvisioningProvider{Private: private})
		}
		switch mode {
		case "status":
			raw, e := revocationCleanupStatus(ctx, pool, org)
			if e != nil {
				err = e
				break
			}
			output = raw
		case "retry":
			if len(os.Args) < 5 || len(os.Args) > 6 {
				err = resources.ErrInput
				break
			}
			limit := 20
			if len(os.Args) == 6 {
				limit, e = strconv.Atoi(os.Args[5])
				if e != nil || limit <= 0 || limit > 100 {
					err = resources.ErrInput
					break
				}
			}
			var private *resources.ProviderPrivate
			private, err = resources.LoadProviderPrivate(os.Args[4])
			if err != nil {
				break
			}
			cleaned, failed, e := service.Cleanup(ctx, org, service.ProviderCleaner(&resources.PGProvisioningProvider{Private: private}), limit)
			if e != nil {
				err = e
				break
			}
			raw, _ := json.Marshal(map[string]any{"orgId": org, "cleaned": cleaned, "failed": failed, "reported": "attempted"})
			output = string(raw)
		default:
			err = resources.ErrInput
		}
	case "import-credentials":
		if len(os.Args) != 3 {
			err = resources.ErrInput
			break
		}
		var raw []byte
		raw, err = os.ReadFile(os.Args[2])
		if err == nil {
			err = resources.ImportCredentials(ctx, dsn, raw)
		}
	case "grant-context-enrollment":
		if len(os.Args) != 3 || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(os.Args[2]) {
			err = resources.ErrInput
			break
		}
		var c *pgx.Conn
		c, err = pgx.Connect(ctx, dsn)
		if err != nil {
			break
		}
		defer c.Close(ctx)
		role := pgx.Identifier{os.Args[2]}.Sanitize()
		_, err = c.Exec(ctx, "GRANT SELECT ON mcpguard_ra.delivery_authorizations TO "+role+";GRANT INSERT ON mcpguard_ra.credential_clients,mcpguard_ra.context_audit TO "+role)
	case "grant-delivery-enrollment":
		if len(os.Args) != 3 || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(os.Args[2]) {
			err = resources.ErrInput
			break
		}
		var c *pgx.Conn
		c, err = pgx.Connect(ctx, dsn)
		if err != nil {
			break
		}
		defer c.Close(ctx)
		role := pgx.Identifier{os.Args[2]}.Sanitize()
		_, err = c.Exec(ctx, "GRANT INSERT ON mcpguard_ra.delivery_authorizations,mcpguard_ra.download_audit TO "+role)
	case "grant-credential-runtime", "grant-credential-status-runtime", "grant-delivery-runtime", "grant-context-runtime", "grant-read-runtime", "grant-write-runtime":
		if len(os.Args) != 3 || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(os.Args[2]) {
			err = resources.ErrInput
			break
		}
		var c *pgx.Conn
		c, err = pgx.Connect(ctx, dsn)
		if err != nil {
			break
		}
		defer c.Close(ctx)
		role := pgx.Identifier{os.Args[2]}.Sanitize()
		_, err = c.Exec(ctx, "GRANT USAGE ON SCHEMA mcpguard,mcpguard_ra TO "+role+";GRANT SELECT ON mcpguard.schema_versions,mcpguard.principals,mcpguard.authorization_heads TO "+role+";GRANT SELECT ON mcpguard_ra.schema_versions,mcpguard_ra.resource_versions,mcpguard_ra.resource_heads,mcpguard_ra.profile_versions,mcpguard_ra.tool_bindings,mcpguard_ra.resource_access,mcpguard_ra.identity_bindings,mcpguard_ra.subject_revisions,mcpguard_ra.access_policy_versions,mcpguard_ra.access_policy_heads,mcpguard_ra.access_requests,mcpguard_ra.access_grant_versions,mcpguard_ra.access_grant_heads,mcpguard_ra.provision_targets,mcpguard_ra.existing_account_registrations,mcpguard_ra.credential_issuers,mcpguard_ra.credential_policies,mcpguard_ra.credential_leases,mcpguard_ra.credential_packages TO "+role+";GRANT UPDATE(state,revision,reason_ciphertext) ON mcpguard_ra.credential_leases TO "+role+";GRANT INSERT ON mcpguard_ra.credential_audit TO "+role+";GRANT USAGE ON SEQUENCE mcpguard.audit_events_seq_seq TO "+role+";GRANT EXECUTE ON FUNCTION mcpguard.lock_auth_epoch_exclusive(uuid),mcpguard.lock_auth_epoch(uuid),mcpguard_ra.advance_access_epoch(uuid) TO "+role)
		if err == nil && os.Args[1] != "grant-credential-status-runtime" {
			_, err = c.Exec(ctx, "GRANT SELECT ON mcpguard_ra.credential_commands,mcpguard_ra.credential_key_usage,mcpguard_ra.credential_nonce_reservations,mcpguard_ra.credential_blobs TO "+role+";GRANT INSERT ON mcpguard_ra.credential_leases,mcpguard_ra.credential_packages,mcpguard_ra.credential_blobs,mcpguard_ra.credential_commands,mcpguard_ra.credential_nonce_reservations TO "+role+";GRANT UPDATE(use_count) ON mcpguard_ra.credential_key_usage TO "+role)
		}
		if err == nil && os.Args[1] == "grant-credential-status-runtime" {
			_, err = c.Exec(ctx, "REVOKE SELECT ON mcpguard_ra.credential_blobs,mcpguard_ra.credential_commands,mcpguard_ra.credential_key_usage,mcpguard_ra.credential_nonce_reservations,mcpguard_ra.credential_key_registry FROM "+role)
		}
		if err == nil && (os.Args[1] == "grant-delivery-runtime" || os.Args[1] == "grant-context-runtime" || os.Args[1] == "grant-read-runtime" || os.Args[1] == "grant-write-runtime") {
			_, err = c.Exec(ctx, "GRANT SELECT ON mcpguard_ra.delivery_authorizations,mcpguard_ra.download_tickets,mcpguard_ra.download_commands,mcpguard_ra.download_audit TO "+role+";GRANT INSERT ON mcpguard_ra.download_tickets,mcpguard_ra.download_commands,mcpguard_ra.download_audit TO "+role+";GRANT UPDATE(state,revision,claimed_authorization_id,claimed_at) ON mcpguard_ra.download_tickets TO "+role)
		}
		if err == nil && (os.Args[1] == "grant-delivery-runtime" || os.Args[1] == "grant-context-runtime" || os.Args[1] == "grant-read-runtime" || os.Args[1] == "grant-write-runtime") {
			var ready bool
			err = c.QueryRow(ctx, "SELECT to_regclass('mcpguard_ra.credential_contexts') IS NOT NULL").Scan(&ready)
			if err == nil && ready {
				_, err = c.Exec(ctx, "GRANT USAGE ON SEQUENCE mcpguard_ra.download_tickets_queue_seq_seq TO "+role)
			}
		}
		if err == nil && (os.Args[1] == "grant-context-runtime" || os.Args[1] == "grant-read-runtime" || os.Args[1] == "grant-write-runtime") {
			_, err = c.Exec(ctx, "GRANT SELECT ON mcpguard_ra.credential_clients,mcpguard_ra.credential_contexts,mcpguard_ra.context_commands,mcpguard_ra.context_activation_versions,mcpguard_ra.context_activation_heads,mcpguard_ra.download_task_receipts,mcpguard_ra.credential_renewals,mcpguard_ra.context_audit TO "+role+";GRANT INSERT ON mcpguard_ra.credential_contexts,mcpguard_ra.context_commands,mcpguard_ra.context_activation_versions,mcpguard_ra.context_activation_heads,mcpguard_ra.download_task_receipts,mcpguard_ra.credential_renewals,mcpguard_ra.context_audit TO "+role+";GRANT UPDATE(state,expires_at,revision) ON mcpguard_ra.credential_contexts TO "+role+";GRANT UPDATE(revision) ON mcpguard_ra.context_activation_heads TO "+role+";GRANT UPDATE(claimed_context_id) ON mcpguard_ra.download_tickets TO "+role)
		}
		if err == nil && (os.Args[1] == "grant-read-runtime" || os.Args[1] == "grant-write-runtime") {
			_, err = c.Exec(ctx, "GRANT SELECT ON mcpguard_ra.native_secrets,mcpguard_ra.account_claims,mcpguard_ra.native_observations,mcpguard_ra.native_accounts TO "+role+";GRANT SELECT ON mcpguard_ra.access_reviewers TO "+role+";GRANT SELECT,INSERT ON mcpguard_ra.read_plans,mcpguard_ra.read_audit TO "+role+";GRANT UPDATE(state,result_ciphertext) ON mcpguard_ra.read_plans TO "+role)
		}
		if err == nil && os.Args[1] == "grant-write-runtime" {
			_, err = c.Exec(ctx, "GRANT SELECT,INSERT ON mcpguard_ra.write_requests,mcpguard_ra.write_audit TO "+role+";GRANT UPDATE(state,decided_by,decided_at,decision_reason,result_ciphertext) ON mcpguard_ra.write_requests TO "+role)
			var hasHighRisk bool
			if err == nil {
				err = c.QueryRow(ctx, "SELECT to_regclass('mcpguard_ra.high_risk_writes') IS NOT NULL").Scan(&hasHighRisk)
			}
			if err == nil && hasHighRisk {
				_, err = c.Exec(ctx, `
 GRANT SELECT,INSERT ON mcpguard_ra.high_risk_writes,mcpguard_ra.high_risk_audit,
  mcpguard_ra.high_risk_notifications,mcpguard_ra.high_risk_notification_attempts TO `+role+`;
 GRANT UPDATE(code_id,code_digest,code_ciphertext,expires_at,consumed_at,dispatch_at) ON mcpguard_ra.high_risk_writes TO `+role+`;
 GRANT UPDATE(state,attempts,next_at,lease_until,acknowledged_at,last_failure) ON mcpguard_ra.high_risk_notifications TO `+role)
			}
		}
	case "migrate":
		switch {
		case len(os.Args) == 2:
			err = resources.Migrate(ctx, dsn)
		case len(os.Args) == 3 && os.Args[2] == "--dry-run":
			plan, planErr := resources.PlanMigration(ctx, dsn)
			if planErr == nil {
				planErr = json.NewEncoder(os.Stdout).Encode(plan)
				if planErr == nil {
					return
				}
			}
			err = planErr
		default:
			err = resources.ErrInput
		}
	case "import":
		if len(os.Args) != 3 {
			err = resources.ErrInput
			break
		}
		var raw []byte
		raw, err = os.ReadFile(os.Args[2])
		if err == nil {
			err = resources.Import(ctx, dsn, raw)
		}
	case "import-access":
		if len(os.Args) != 3 {
			err = resources.ErrInput
			break
		}
		var raw []byte
		raw, err = os.ReadFile(os.Args[2])
		if err == nil {
			err = resources.ImportAccess(ctx, dsn, raw)
		}
	case "import-provisioning":
		if len(os.Args) != 3 {
			err = resources.ErrInput
			break
		}
		var raw []byte
		raw, err = os.ReadFile(os.Args[2])
		if err == nil {
			err = resources.ImportProvisioning(ctx, dsn, raw)
		}
	case "grant-provision-runtime":
		if len(os.Args) != 3 || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(os.Args[2]) {
			err = resources.ErrInput
			break
		}
		var c *pgx.Conn
		c, err = pgx.Connect(ctx, dsn)
		if err != nil {
			break
		}
		defer c.Close(ctx)
		role := pgx.Identifier{os.Args[2]}.Sanitize()
		_, err = c.Exec(ctx, "GRANT USAGE ON SCHEMA mcpguard,mcpguard_ra TO "+role+";GRANT SELECT ON mcpguard.schema_versions,mcpguard.principals,mcpguard.authorization_heads TO "+role+";GRANT SELECT ON ALL TABLES IN SCHEMA mcpguard_ra TO "+role+";GRANT INSERT ON mcpguard_ra.account_claims,mcpguard_ra.provisioning_jobs,mcpguard_ra.native_secrets,mcpguard_ra.native_observations,mcpguard_ra.native_accounts,mcpguard_ra.provision_audit,mcpguard_ra.identity_bindings,mcpguard_ra.access_grant_versions TO "+role+";GRANT UPDATE(state,failure_code) ON mcpguard_ra.provisioning_jobs TO "+role+";GRANT UPDATE(version,state,revision,auth_epoch) ON mcpguard_ra.access_grant_heads TO "+role+";GRANT UPDATE(auth_epoch) ON mcpguard.authorization_heads TO "+role+";GRANT USAGE,SELECT ON SEQUENCE mcpguard.audit_events_seq_seq TO "+role+";GRANT EXECUTE ON FUNCTION mcpguard.lock_auth_epoch(uuid),mcpguard.lock_auth_epoch_exclusive(uuid),mcpguard_ra.advance_access_epoch(uuid) TO "+role)
		if err == nil {
			var ready bool
			err = c.QueryRow(ctx, "SELECT to_regclass('mcpguard_ra.provision_recovery') IS NOT NULL").Scan(&ready)
			if err == nil && ready {
				_, err = c.Exec(ctx, "GRANT INSERT,UPDATE ON mcpguard_ra.provision_recovery TO "+role+";GRANT INSERT ON mcpguard_ra.provision_attempt_events TO "+role+";GRANT EXECUTE ON FUNCTION mcpguard_ra.assert_provision_lease(uuid,uuid) TO "+role)
			}
		}
	case "grant-access-runtime":
		if len(os.Args) != 3 || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(os.Args[2]) {
			err = resources.ErrInput
			break
		}
		var c *pgx.Conn
		c, err = pgx.Connect(ctx, dsn)
		if err != nil {
			break
		}
		defer c.Close(ctx)
		role := pgx.Identifier{os.Args[2]}.Sanitize()
		_, err = c.Exec(ctx, "GRANT SELECT ON ALL TABLES IN SCHEMA mcpguard_ra TO "+role+";GRANT INSERT ON mcpguard_ra.access_requests,mcpguard_ra.access_decisions,mcpguard_ra.access_grant_versions,mcpguard_ra.access_grant_heads,mcpguard_ra.access_commands,mcpguard_ra.access_audit TO "+role+";GRANT UPDATE(state,revision) ON mcpguard_ra.access_requests TO "+role+";GRANT UPDATE(state,revision,auth_epoch) ON mcpguard_ra.access_grant_heads TO "+role+";GRANT EXECUTE ON FUNCTION mcpguard_ra.advance_access_epoch(uuid),mcpguard.lock_auth_epoch_exclusive(uuid) TO "+role)
		if err == nil {
			var ready bool
			err = c.QueryRow(ctx, "SELECT to_regclass('mcpguard_ra.access_grant_ids') IS NOT NULL").Scan(&ready)
			if err == nil && ready {
				_, err = c.Exec(ctx, "GRANT INSERT ON mcpguard_ra.access_grant_ids TO "+role)
			}
		}
	case "grant-runtime":
		if len(os.Args) != 3 || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(os.Args[2]) {
			err = resources.ErrInput
			break
		}
		var c *pgx.Conn
		c, err = pgx.Connect(ctx, dsn)
		if err != nil {
			break
		}
		defer c.Close(ctx)
		role := pgx.Identifier{os.Args[2]}.Sanitize()
		_, err = c.Exec(ctx, "GRANT USAGE ON SCHEMA mcpguard_ra TO "+role+";GRANT SELECT ON ALL TABLES IN SCHEMA mcpguard_ra TO "+role+";GRANT INSERT ON mcpguard_ra.fixture_reads,mcpguard_ra.fixture_audit TO "+role+";GRANT UPDATE(state,result_ciphertext) ON mcpguard_ra.fixture_reads TO "+role)
	default:
		err = resources.ErrInput
	}
	// Existing grant commands retain their pre-RA-07 behavior, without gaining
	// access to newly added server-only crypto storage on a later re-run.
	if err == nil && (os.Args[1] == "grant-runtime" || os.Args[1] == "grant-access-runtime" || os.Args[1] == "grant-provision-runtime") {
		var c *pgx.Conn
		c, err = pgx.Connect(ctx, dsn)
		if err == nil {
			defer c.Close(ctx)
			var ready bool
			err = c.QueryRow(ctx, "SELECT to_regclass('mcpguard_ra.credential_blobs') IS NOT NULL").Scan(&ready)
			if err == nil && ready {
				_, err = c.Exec(ctx, "REVOKE SELECT ON mcpguard_ra.credential_blobs,mcpguard_ra.credential_commands,mcpguard_ra.credential_key_usage,mcpguard_ra.credential_nonce_reservations,mcpguard_ra.credential_key_registry FROM "+pgx.Identifier{os.Args[2]}.Sanitize())
			}
			if err == nil {
				err = c.QueryRow(ctx, "SELECT to_regclass('mcpguard_ra.delivery_authorizations') IS NOT NULL").Scan(&ready)
				if err == nil && ready {
					_, err = c.Exec(ctx, "REVOKE SELECT ON mcpguard_ra.delivery_authorizations,mcpguard_ra.download_commands FROM "+pgx.Identifier{os.Args[2]}.Sanitize())
				}
			}
			if err == nil {
				err = c.QueryRow(ctx, "SELECT to_regclass('mcpguard_ra.credential_contexts') IS NOT NULL").Scan(&ready)
				if err == nil && ready {
					_, err = c.Exec(ctx, "REVOKE SELECT ON mcpguard_ra.credential_clients,mcpguard_ra.credential_contexts,mcpguard_ra.context_commands,mcpguard_ra.context_activation_versions,mcpguard_ra.context_activation_heads,mcpguard_ra.download_task_receipts,mcpguard_ra.credential_renewals,mcpguard_ra.context_audit FROM "+pgx.Identifier{os.Args[2]}.Sanitize())
				}
			}
		}
	}
	if err != nil {
		if output != "" {
			fmt.Println(output)
		}
		fmt.Fprintln(os.Stderr, "Catalog command failed; inspect the private administrative environment. No sensitive details printed.")
		os.Exit(1)
	}
	if output != "" {
		fmt.Println(output)
		return
	}
	fmt.Println("Catalog administrative operation completed.")
}

func revocationCleanupStatus(ctx context.Context, pool *pgxpool.Pool, org string) (string, error) {
	rows, e := pool.Query(ctx, `SELECT account_id::text,state,attempts,coalesce(last_error,''),native_receipt FROM mcpguard_ra.revocation_cleanup WHERE org_id=$1 ORDER BY updated_at`, org)
	if e != nil {
		return "", resources.ErrUnavailable
	}
	defer rows.Close()
	type row struct {
		AccountID     string          `json:"accountId"`
		State         string          `json:"state"`
		Attempts      int             `json:"attempts"`
		LastError     string          `json:"lastError,omitempty"`
		NativeReceipt json.RawMessage `json:"nativeReceipt,omitempty"`
	}
	out := struct {
		OrgID   string `json:"orgId"`
		Pending int    `json:"pending"`
		Failed  int    `json:"failed"`
		Cleaned int    `json:"cleaned"`
		Rows    []row  `json:"rows"`
	}{OrgID: org, Rows: []row{}}
	for rows.Next() {
		var r row
		if rows.Scan(&r.AccountID, &r.State, &r.Attempts, &r.LastError, &r.NativeReceipt) != nil {
			return "", resources.ErrUnavailable
		}
		switch r.State {
		case "PENDING":
			out.Pending++
		case "FAILED":
			out.Failed++
		case "CLEANED":
			out.Cleaned++
		}
		out.Rows = append(out.Rows, r)
	}
	if rows.Err() != nil {
		return "", resources.ErrUnavailable
	}
	raw, _ := json.Marshal(out)
	return string(raw), nil
}
