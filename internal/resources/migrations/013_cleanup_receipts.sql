-- Keep the most recent verified provider receipt alongside the retry ledger.
-- Historical cleanups have NULL evidence and can be rechecked explicitly.
ALTER TABLE mcpguard_ra.revocation_cleanup ADD COLUMN native_receipt jsonb;
