package metadata

import (
	"context"
	"errors"
)

// CheckReady uses the runtime role without writing ledger or identity records.
// Privilege checks cover the normal claim/completion path; LIMIT 0 verifies
// required tables and columns without returning any tenant's stored payload.
func (s *Store) CheckReady(ctx context.Context) error {
	var ready bool
	err := s.Pool.QueryRow(ctx, `SELECT
		coalesce((SELECT max(version) IN (4,5,6) FROM mcpguard.schema_versions),false)
		AND current_setting('transaction_read_only')='off'
		AND NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole)
		AND NOT has_schema_privilege(current_user,'mcpguard','CREATE')
		AND NOT has_table_privilege(current_user,'mcpguard.audit_events','UPDATE,DELETE,TRUNCATE')
		AND has_function_privilege(current_user,'mcpguard.lock_auth_epoch(uuid)','EXECUTE')
		AND has_function_privilege(current_user,'mcpguard.lock_auth_epoch_exclusive(uuid)','EXECUTE')
		AND has_function_privilege(current_user,'mcpguard.has_tool_grant(uuid,uuid,text)','EXECUTE')
		AND has_function_privilege(current_user,'mcpguard.has_reviewer_grant(uuid,uuid,text)','EXECUTE')
		AND NOT has_table_privilege(current_user,'mcpguard.managed_audit','UPDATE,DELETE,TRUNCATE')
		AND (SELECT bool_and(has_table_privilege(current_user,tbl,priv)) FROM (VALUES
			('mcpguard.authorization_heads','SELECT'),('mcpguard.principals','SELECT'),
			('mcpguard.actions','SELECT'),('mcpguard.actions','INSERT'),
			('mcpguard.attempts','SELECT'),('mcpguard.attempts','INSERT'),
			('mcpguard.executions','SELECT'),('mcpguard.executions','INSERT'),
			('mcpguard.audit_events','SELECT'),('mcpguard.audit_events','INSERT'),
			('mcpguard.managed_actions','SELECT'),('mcpguard.managed_actions','INSERT'),
			('mcpguard.managed_executions','SELECT'),('mcpguard.managed_executions','INSERT'),
			('mcpguard.managed_decisions','SELECT'),('mcpguard.managed_decisions','INSERT'),
			('mcpguard.managed_audit','SELECT'),('mcpguard.managed_audit','INSERT'),
			('mcpguard.control_resources','SELECT'),('mcpguard.control_rules','SELECT'),
			('mcpguard.control_heads','SELECT'),('mcpguard.control_members','SELECT'),
			('mcpguard.tool_grants','SELECT'),('mcpguard.reviewer_grants','SELECT'),
			('mcpguard.control_operations','SELECT'),('mcpguard.control_operations','INSERT'),('mcpguard.notification_outbox','SELECT')
			,('mcpguard.control_groups','SELECT'),('mcpguard.group_members','SELECT'),
			('mcpguard.group_tool_grants','SELECT'),('mcpguard.group_reviewer_grants','SELECT')
		) AS required(tbl,priv))
		AND has_column_privilege(current_user,'mcpguard.actions','state','UPDATE')
		AND has_column_privilege(current_user,'mcpguard.attempts','state','UPDATE')
		AND has_column_privilege(current_user,'mcpguard.executions','dispatch_state','UPDATE')
		AND has_column_privilege(current_user,'mcpguard.executions','request_status','UPDATE')
		AND has_column_privilege(current_user,'mcpguard.executions','result_ciphertext','UPDATE')
		AND has_sequence_privilege(current_user,pg_get_serial_sequence('mcpguard.audit_events','seq'),'USAGE')
		FROM pg_roles r WHERE r.rolname=current_user`).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("metadata schema or runtime privileges are not ready")
	}
	_, err = s.Pool.Exec(ctx, `SELECT p.org_id,p.id,p.enabled,p.token_hash,p.expires_at,p.can_read,p.scope_id,
		h.auth_epoch,a.id,a.plan_hash,a.plan_ciphertext,a.state,t.id,t.state,
		e.id,e.request_status,e.dispatch_state,e.result_ciphertext,v.seq,v.event_type,v.created_at
		FROM mcpguard.principals p,mcpguard.authorization_heads h,mcpguard.actions a,
		mcpguard.attempts t,mcpguard.executions e,mcpguard.audit_events v LIMIT 0`)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `SELECT a.plan_hash,a.plan_ciphertext,a.config_revision,a.binding_hash,a.cancel_requested,e.hard_deadline,e.side_effect_status,e.result_ciphertext,d.reviewer_id,o.attempts,o.lease_until FROM mcpguard.managed_actions a,mcpguard.managed_executions e,mcpguard.managed_decisions d,mcpguard.notification_outbox o LIMIT 0`)
	return err
}
