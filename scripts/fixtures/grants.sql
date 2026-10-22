REVOKE ALL ON SCHEMA public,mcpguard FROM PUBLIC;
GRANT USAGE ON SCHEMA mcpguard TO mcpguard_app;
GRANT EXECUTE ON FUNCTION mcpguard.lock_auth_epoch(uuid) TO mcpguard_app;
GRANT SELECT ON ALL TABLES IN SCHEMA mcpguard TO mcpguard_app;
GRANT INSERT ON mcpguard.actions,mcpguard.attempts,mcpguard.executions,mcpguard.audit_events TO mcpguard_app;
GRANT UPDATE(state) ON mcpguard.actions,mcpguard.attempts TO mcpguard_app;
GRANT UPDATE(dispatch_state,request_status,result_ciphertext) ON mcpguard.executions TO mcpguard_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA mcpguard TO mcpguard_app;
