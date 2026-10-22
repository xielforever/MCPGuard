CREATE SCHEMA IF NOT EXISTS demo;
CREATE TABLE IF NOT EXISTS demo.items (
  scope_id text NOT NULL CHECK(scope_id ~ '^[a-z][a-z0-9_]{0,31}$'),
  item_id text NOT NULL CHECK(item_id ~ '^item_demo_[a-z0-9_]{1,32}$'),
  note text NOT NULL CHECK(length(note)<=500),
  version bigint NOT NULL CHECK(version>0 AND version<1000000000000000000),
  PRIMARY KEY(scope_id,item_id)
);
INSERT INTO demo.items VALUES
 ('scope_a','item_demo_1','Alice scope A',1),
 ('scope_b','item_demo_1','Bob scope B',9007199254740993),
 ('scope_b','item_demo_b_only','Only visible in scope B',1)
ON CONFLICT(scope_id,item_id) DO UPDATE SET note=EXCLUDED.note,version=EXCLUDED.version;
REVOKE ALL ON SCHEMA demo,public FROM PUBLIC;
REVOKE ALL ON demo.items FROM PUBLIC,mcpguard_reader;
GRANT USAGE ON SCHEMA demo TO mcpguard_reader;
GRANT SELECT ON demo.items TO mcpguard_reader;
ALTER ROLE mcpguard_reader SET default_transaction_read_only=on;
