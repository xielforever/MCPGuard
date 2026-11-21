ALTER TABLE mcpguard.control_resources DROP CONSTRAINT control_resources_name_check;
ALTER TABLE mcpguard.control_resources ADD CONSTRAINT control_resources_name_check
 CHECK (name IN ('demo.pg','demo.redis','demo.directory','demo.inventory','jenkins'));
