package resources

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

type HighRiskOperation string

const (
	HighRiskUpdateOrder   HighRiskOperation = "UPDATE_ORDER_AMOUNT"
	HighRiskCreateIndex   HighRiskOperation = "CREATE_ORDERS_INDEX"
	HighRiskDeleteOrder   HighRiskOperation = "DELETE_CANCELLED_ORDER"
	HighRiskUpdateOrderID                   = "order_recalc_1"
	HighRiskDeleteOrderID                   = "order_cancelled_1"
	HighRiskIndexName                       = "orders_shipping_lookup_idx"

	highRiskSchema          = "mcpguard_w2_fixture"
	highRiskTable           = "orders"
	highRiskIndexColumn     = "shipping_code"
	highRiskIndexDefinition = "CREATE INDEX " + HighRiskIndexName + " ON " + highRiskSchema + "." + highRiskTable + " USING btree (" + highRiskIndexColumn + ")"
)

// HighRiskPlanPreview describes the exact approved fixture effect.
type HighRiskPlanPreview struct {
	Schema          string `json:"schema"`
	Table           string `json:"table"`
	TenantID        string `json:"tenantId,omitempty"`
	Target          string `json:"target"`
	ExpectedVersion int64  `json:"expectedVersion,omitempty"`
	ExpectedStatus  string `json:"expectedStatus,omitempty"`
	OldAmount       int64  `json:"oldAmount,omitempty"`
	NewAmount       int64  `json:"newAmount,omitempty"`
	AffectedRows    int    `json:"affectedRows"`
	IndexColumn     string `json:"indexColumn,omitempty"`
	IndexDefinition string `json:"indexDefinition,omitempty"`
}

func HighRiskPreview(op HighRiskOperation, target string, expectedVersion int64) (HighRiskPlanPreview, bool) {
	preview := HighRiskPlanPreview{Schema: highRiskSchema, Table: highRiskTable, Target: target, ExpectedVersion: expectedVersion}
	switch {
	case op == HighRiskUpdateOrder && target == HighRiskUpdateOrderID && expectedVersion == 1:
		preview.TenantID = "tenant_a"
		preview.ExpectedStatus = "READY_TO_SHIP"
		preview.OldAmount, preview.NewAmount, preview.AffectedRows = 100, 120, 1
	case op == HighRiskCreateIndex && target == HighRiskIndexName && expectedVersion == 0:
		preview.IndexColumn, preview.IndexDefinition = highRiskIndexColumn, highRiskIndexDefinition
	case op == HighRiskDeleteOrder && target == HighRiskDeleteOrderID && expectedVersion == 1:
		preview.TenantID = "tenant_a"
		preview.ExpectedStatus, preview.AffectedRows = "CANCELLED", 1
	default:
		return HighRiskPlanPreview{}, false
	}
	return preview, true
}

func ValidHighRiskTarget(op HighRiskOperation, target string, expectedVersion int64) bool {
	_, valid := HighRiskPreview(op, target, expectedVersion)
	return valid
}

type HighRiskNativeResult struct {
	Outcome         string `json:"outcome"`
	AffectedRows    int    `json:"affectedRows"`
	NewVersion      int64  `json:"newVersion,omitempty"`
	IndexDefinition string `json:"indexDefinition,omitempty"`
	IndexValid      bool   `json:"indexValid,omitempty"`
}

// These queries invoke only the functions installed by the disposable orders fixture.
const (
	updateOrderAmountSQL    = `SELECT outcome,affected_rows,new_version FROM mcpguard_w2_fixture.update_order_amount($1,$2)`
	createOrdersIndexSQL    = `SELECT mcpguard_w2_fixture.create_orders_index($1)`
	deleteCancelledOrderSQL = `SELECT outcome,affected_rows FROM mcpguard_w2_fixture.delete_cancelled_order($1,$2)`
	verifyOrdersIndexSQL    = `SELECT pg_get_indexdef(i.indexrelid), i.indisvalid AND i.indisready AND i.indislive AND NOT i.indisunique
 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
 WHERE i.indexrelid=to_regclass('mcpguard_w2_fixture.orders_shipping_lookup_idx')
 AND i.indrelid=to_regclass('mcpguard_w2_fixture.orders') AND c.relkind='i'`
)

// The verified RA write account keeps its existing own-row permissions on
// resource_demo.items. It can reach fixture orders only through these three
// functions, owned by the fixture's unprivileged NOLOGIN role.
const highRiskRoleCheck = `WITH fixture_owner AS (
 SELECT oid FROM pg_roles owner_role WHERE rolname='mcpguard_w2_fixture_owner'
 AND NOT (rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)
 AND NOT EXISTS(SELECT FROM pg_auth_members WHERE member=owner_role.oid)
), fixed_functions AS (
 SELECT f.oid FROM pg_proc f JOIN fixture_owner o ON f.proowner=o.oid
 WHERE f.prosecdef AND f.oid IN (
  to_regprocedure('mcpguard_w2_fixture.update_order_amount(text,bigint)'),
  to_regprocedure('mcpguard_w2_fixture.create_orders_index(text)'),
  to_regprocedure('mcpguard_w2_fixture.delete_cancelled_order(text,bigint)'))
 AND f.proconfig=ARRAY['search_path=pg_catalog, pg_temp']
)
SELECT session_user::text,current_user::text,current_database(),r.oid::text,
 r.rolcanlogin AND NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
 AND (r.rolvaliduntil IS NULL OR r.rolvaliduntil>=$1::timestamptz)
 AND NOT EXISTS(SELECT FROM pg_auth_members WHERE member=r.oid)
 AND NOT has_database_privilege(current_user,current_database(),'CREATE')
 AND NOT EXISTS(SELECT FROM pg_namespace n WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema'
  AND (n.nspowner=r.oid OR has_schema_privilege(current_user,n.oid,'CREATE')))
 AND NOT has_table_privilege(current_user,to_regclass('resource_demo.items'),'INSERT,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 AND NOT has_any_column_privilege(current_user,to_regclass('resource_demo.items'),'INSERT,REFERENCES')
 AND (SELECT c.relkind='r' AND c.relrowsecurity AND c.relowner<>r.oid FROM pg_class c WHERE c.oid=to_regclass('resource_demo.items'))
 AND (SELECT count(*)=2 AND bool_and(p.polroles=ARRAY[0::oid] AND p.polpermissive
  AND pg_get_expr(p.polqual,p.polrelid)='(owner_name = CURRENT_USER)'
  AND ((p.polcmd='r' AND p.polwithcheck IS NULL) OR
       (p.polcmd='w' AND pg_get_expr(p.polwithcheck,p.polrelid)='(owner_name = CURRENT_USER)')))
  FROM pg_policy p WHERE p.polrelid=to_regclass('resource_demo.items'))
 AND EXISTS(SELECT FROM pg_class c JOIN fixture_owner o ON c.relowner=o.oid
  WHERE c.oid=to_regclass('mcpguard_w2_fixture.orders') AND c.relkind='r')
 AND NOT EXISTS(SELECT FROM pg_class c JOIN pg_namespace n ON c.relnamespace=n.oid
  WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND c.relkind IN ('r','p','v','m','f')
  AND (c.relowner=r.oid OR (c.oid<>to_regclass('resource_demo.items')::oid AND
   (has_table_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
    OR has_any_column_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))))
 AND has_schema_privilege(current_user,'mcpguard_w2_fixture','USAGE')
 AND (SELECT count(*)=3 AND bool_and(has_function_privilege(current_user,oid,'EXECUTE')) FROM fixed_functions)
 AND NOT EXISTS(SELECT FROM pg_proc f JOIN pg_namespace n ON f.pronamespace=n.oid
  WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND f.prosecdef
  AND has_function_privilege(current_user,f.oid,'EXECUTE') AND f.oid NOT IN (SELECT oid FROM fixed_functions))
 FROM pg_roles r WHERE r.rolname=current_user`

func (s *WriteService) nativeHighRisk(ctx context.Context, p metadata.Principal, snap Snapshot, op HighRiskOperation, target string, expectedVersion int64) (HighRiskNativeResult, error) {
	var result HighRiskNativeResult
	if !ValidHighRiskTarget(op, target, expectedVersion) {
		return result, ErrInput
	}
	call, cancel := context.WithTimeout(ctx, time.Duration(snap.Profile.TimeoutMs)*time.Millisecond)
	defer cancel()
	endpoint, _, err := s.Manager.resolve(call, snap)
	if err != nil || !safeProviderEndpoint(endpoint) || snap.Binding.AccountID == nil {
		return result, ErrDenied
	}
	pool, err := s.Manager.writePool(call, p, snap)
	if err != nil {
		return result, err
	}
	tx, err := pool.Begin(call)
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	var login, effective, database, oid, expectedOID string
	var safe bool
	if tx.QueryRow(call, highRiskRoleCheck, time.Now().UTC()).Scan(&login, &effective, &database, &oid, &safe) != nil ||
		!safe || login != snap.Binding.NativeIdentity || login != effective || database != endpoint.Database ||
		s.Catalog.Store.Pool.QueryRow(call, `SELECT role_oid::text FROM mcpguard_ra.native_observations WHERE org_id=$1 AND account_id=$2 ORDER BY observed_at LIMIT 1`, p.OrgID, *snap.Binding.AccountID).Scan(&expectedOID) != nil ||
		oid != expectedOID {
		return result, ErrDenied
	}
	if err = executeHighRiskFixture(call, tx, op, target, expectedVersion, &result); err != nil {
		return HighRiskNativeResult{}, fmt.Errorf("native fixture operation: %w", err)
	}
	if err = tx.Commit(call); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return HighRiskNativeResult{}, err
		}
		return HighRiskNativeResult{}, errCommitOutcomeUnknown
	}
	return result, nil
}

func executeHighRiskFixture(ctx context.Context, tx pgx.Tx, op HighRiskOperation, target string, expectedVersion int64, result *HighRiskNativeResult) error {
	switch op {
	case HighRiskUpdateOrder:
		if err := tx.QueryRow(ctx, updateOrderAmountSQL, target, expectedVersion).Scan(&result.Outcome, &result.AffectedRows, &result.NewVersion); err != nil {
			return err
		}
		if result.Outcome == "UPDATED" && (result.AffectedRows != 1 || result.NewVersion != expectedVersion+1) {
			return ErrUnavailable
		}
		return checkHighRiskRowResult(*result, "UPDATED")
	case HighRiskCreateIndex:
		if err := tx.QueryRow(ctx, createOrdersIndexSQL, target).Scan(&result.Outcome); err != nil {
			return err
		}
		if result.Outcome == "INDEX_EXISTS" {
			return nil
		}
		if result.Outcome != "INDEX_CREATED" {
			return ErrUnavailable
		}
		if err := tx.QueryRow(ctx, verifyOrdersIndexSQL).Scan(&result.IndexDefinition, &result.IndexValid); err != nil {
			return err
		}
		if !result.IndexValid || result.IndexDefinition != highRiskIndexDefinition {
			return ErrUnavailable
		}
		return nil
	case HighRiskDeleteOrder:
		if err := tx.QueryRow(ctx, deleteCancelledOrderSQL, target, expectedVersion).Scan(&result.Outcome, &result.AffectedRows); err != nil {
			return err
		}
		return checkHighRiskRowResult(*result, "DELETED")
	default:
		return ErrInput
	}
}

func checkHighRiskRowResult(result HighRiskNativeResult, successfulOutcome string) error {
	if result.Outcome == successfulOutcome && result.AffectedRows == 1 {
		return nil
	}
	switch result.Outcome {
	case "VERSION_CONFLICT", "NOT_FOUND", "PRECONDITION_FAILED":
		if result.AffectedRows == 0 && result.NewVersion == 0 {
			return nil
		}
	}
	return ErrUnavailable
}
