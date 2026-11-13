CREATE TABLE mcpguard.control_groups (
 org_id uuid NOT NULL REFERENCES mcpguard.control_heads, id uuid NOT NULL,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 100), enabled boolean NOT NULL DEFAULT false,
 PRIMARY KEY(org_id,id), UNIQUE(org_id,name)
);
CREATE TABLE mcpguard.group_members (
 org_id uuid NOT NULL, group_id uuid NOT NULL, principal_id uuid NOT NULL,
 PRIMARY KEY(org_id,group_id,principal_id),
 FOREIGN KEY(org_id,group_id) REFERENCES mcpguard.control_groups(org_id,id),
 FOREIGN KEY(org_id,principal_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE TABLE mcpguard.group_tool_grants (
 org_id uuid NOT NULL, group_id uuid NOT NULL, tool_name text NOT NULL, allowed boolean NOT NULL DEFAULT false,
 PRIMARY KEY(org_id,group_id,tool_name), FOREIGN KEY(org_id,group_id) REFERENCES mcpguard.control_groups(org_id,id)
);
CREATE TABLE mcpguard.group_reviewer_grants (
 org_id uuid NOT NULL, group_id uuid NOT NULL, resource_name text NOT NULL, allowed boolean NOT NULL DEFAULT false,
 PRIMARY KEY(org_id,group_id,resource_name), FOREIGN KEY(org_id,group_id) REFERENCES mcpguard.control_groups(org_id,id),
 FOREIGN KEY(org_id,resource_name) REFERENCES mcpguard.control_resources(org_id,name)
);
CREATE TRIGGER control_groups_revision BEFORE INSERT OR UPDATE OR DELETE ON mcpguard.control_groups FOR EACH ROW EXECUTE FUNCTION mcpguard.bump_control_revision();
CREATE TRIGGER group_members_revision BEFORE INSERT OR UPDATE OR DELETE ON mcpguard.group_members FOR EACH ROW EXECUTE FUNCTION mcpguard.bump_control_revision();
CREATE TRIGGER group_tool_grants_revision BEFORE INSERT OR UPDATE OR DELETE ON mcpguard.group_tool_grants FOR EACH ROW EXECUTE FUNCTION mcpguard.bump_control_revision();
CREATE TRIGGER group_reviewer_grants_revision BEFORE INSERT OR UPDATE OR DELETE ON mcpguard.group_reviewer_grants FOR EACH ROW EXECUTE FUNCTION mcpguard.bump_control_revision();
-- One authorization function is used by list, request, approval and resume.
-- An explicit principal false overrides grants inherited from groups.
CREATE FUNCTION mcpguard.has_tool_grant(target_org uuid,target_principal uuid,target_tool text) RETURNS boolean
LANGUAGE sql STABLE SET search_path=pg_catalog AS $$
 SELECT coalesce((SELECT allowed FROM mcpguard.tool_grants WHERE org_id=target_org AND principal_id=target_principal AND tool_name=target_tool),
  EXISTS(SELECT 1 FROM mcpguard.group_tool_grants g JOIN mcpguard.control_groups c ON (c.org_id,c.id)=(g.org_id,g.group_id)
  JOIN mcpguard.group_members m ON (m.org_id,m.group_id)=(g.org_id,g.group_id)
  WHERE g.org_id=target_org AND m.principal_id=target_principal AND g.tool_name=target_tool AND g.allowed AND c.enabled))
$$;
CREATE FUNCTION mcpguard.has_reviewer_grant(target_org uuid,target_principal uuid,target_resource text) RETURNS boolean
LANGUAGE sql STABLE SET search_path=pg_catalog AS $$
 SELECT coalesce((SELECT allowed FROM mcpguard.reviewer_grants WHERE org_id=target_org AND principal_id=target_principal AND resource_name=target_resource),
  EXISTS(SELECT 1 FROM mcpguard.group_reviewer_grants g JOIN mcpguard.control_groups c ON (c.org_id,c.id)=(g.org_id,g.group_id)
  JOIN mcpguard.group_members m ON (m.org_id,m.group_id)=(g.org_id,g.group_id)
  WHERE g.org_id=target_org AND m.principal_id=target_principal AND g.resource_name=target_resource AND g.allowed AND c.enabled))
$$;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA mcpguard FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA mcpguard FROM PUBLIC;
