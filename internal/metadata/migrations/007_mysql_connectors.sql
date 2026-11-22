ALTER TABLE mcpguard.connectors DROP CONSTRAINT connectors_kind_check;
ALTER TABLE mcpguard.connectors ADD CONSTRAINT connectors_kind_check
  CHECK (kind IN ('postgresql','redis','jenkins','kubernetes','mysql'));
