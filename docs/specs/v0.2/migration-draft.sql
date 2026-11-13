-- v0.2 RA-01 migration DRAFT for RA-04..RA-15.
-- OFFLINE DESIGN ARTIFACT ONLY. Not embedded in metadata/resources migrations.
-- First apply the explicitly versioned RA catalog migration, then implement and
-- review lifecycle triggers/privilege grants before promoting this draft.
CREATE SCHEMA mcpguard_ra_draft;
REVOKE ALL ON SCHEMA mcpguard_ra_draft FROM PUBLIC;
CREATE TABLE mcpguard_ra_draft.access_requests (
 org_id uuid NOT NULL,request_id uuid NOT NULL,subject_id uuid NOT NULL,
 resource_id uuid NOT NULL,resource_version bigint NOT NULL,profile_id uuid NOT NULL,profile_version bigint NOT NULL,
 request_hash text NOT NULL,request_revision bigint NOT NULL CHECK(request_revision>0),
 requested_expiry timestamptz NOT NULL,reason_ref text NOT NULL,
 state text NOT NULL CHECK(state IN ('SUBMITTED','PENDING_APPROVAL','APPROVED','REJECTED','CANCELLED','EXPIRED','STALE')),
 PRIMARY KEY(org_id,request_id),UNIQUE(org_id,request_id,request_hash,request_revision),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions,
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions
);
CREATE TABLE mcpguard_ra_draft.access_decisions (
 org_id uuid NOT NULL,decision_id uuid NOT NULL,request_id uuid NOT NULL,request_hash text NOT NULL,request_revision bigint NOT NULL,
 subject_id uuid NOT NULL,decider_id uuid NOT NULL,source text NOT NULL CHECK(source IN ('HUMAN','POLICY')),
 decision text NOT NULL CHECK(decision IN ('APPROVE','REJECT')),approved_expiry timestamptz,policy_ref text,
 PRIMARY KEY(org_id,decision_id),UNIQUE(org_id,request_id),
 FOREIGN KEY(org_id,request_id,request_hash,request_revision) REFERENCES mcpguard_ra_draft.access_requests(org_id,request_id,request_hash,request_revision),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,decider_id) REFERENCES mcpguard.principals(org_id,id),
 CHECK((source='HUMAN' AND decider_id<>subject_id) OR (source='POLICY' AND policy_ref IS NOT NULL))
);
CREATE TABLE mcpguard_ra_draft.native_accounts (
 org_id uuid NOT NULL,account_id uuid NOT NULL,subject_id uuid NOT NULL,resource_id uuid NOT NULL,
 profile_id uuid NOT NULL,profile_version bigint NOT NULL,native_identity text NOT NULL,
 management_mode text NOT NULL CHECK(management_mode IN ('PROVISIONED','PREEXISTING')),
 receipt_ref text,PRIMARY KEY(org_id,account_id),
 UNIQUE(org_id,account_id,subject_id,resource_id),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions
);
CREATE TABLE mcpguard_ra_draft.grant_versions (
 org_id uuid NOT NULL,grant_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),
 request_id uuid NOT NULL,subject_id uuid NOT NULL,resource_id uuid NOT NULL,resource_version bigint NOT NULL,
 profile_id uuid NOT NULL,profile_version bigint NOT NULL,account_id uuid NOT NULL,binding_id uuid NOT NULL,binding_version bigint NOT NULL,
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(org_id,grant_id,version),
 FOREIGN KEY(org_id,request_id) REFERENCES mcpguard_ra_draft.access_requests,
 FOREIGN KEY(org_id,resource_id,resource_version) REFERENCES mcpguard_ra.resource_versions,
 FOREIGN KEY(org_id,resource_id,profile_id,profile_version) REFERENCES mcpguard_ra.profile_versions,
 FOREIGN KEY(org_id,account_id,subject_id,resource_id) REFERENCES mcpguard_ra_draft.native_accounts(org_id,account_id,subject_id,resource_id),
 FOREIGN KEY(org_id,binding_id,binding_version) REFERENCES mcpguard_ra.identity_bindings(org_id,binding_id,binding_version)
);
CREATE TABLE mcpguard_ra_draft.grant_heads (
 org_id uuid NOT NULL,grant_id uuid NOT NULL,version bigint NOT NULL,
 state text NOT NULL CHECK(state IN ('PROVISIONING','ACTIVE','PROVISIONING_UNKNOWN','PROVISIONING_FAILED','REVOKED','EXPIRED')),
 auth_epoch bigint NOT NULL CHECK(auth_epoch>0),PRIMARY KEY(org_id,grant_id),
 FOREIGN KEY(org_id,grant_id,version) REFERENCES mcpguard_ra_draft.grant_versions
);
CREATE TABLE mcpguard_ra_draft.provisioning_jobs (
 org_id uuid NOT NULL,job_id uuid NOT NULL,grant_id uuid NOT NULL,grant_version bigint NOT NULL,
 account_plan_hash text NOT NULL,native_name text NOT NULL,receipt_ref text,
 state text NOT NULL CHECK(state IN ('READY','EXECUTING','VERIFIED','OUTCOME_UNKNOWN','FAILED','CLEANUP_PENDING')),
 PRIMARY KEY(org_id,job_id),UNIQUE(org_id,grant_id,grant_version),
 FOREIGN KEY(org_id,grant_id,grant_version) REFERENCES mcpguard_ra_draft.grant_versions
);
CREATE TABLE mcpguard_ra_draft.credential_leases (
 org_id uuid NOT NULL,lease_id uuid NOT NULL,credential_id uuid NOT NULL,grant_id uuid NOT NULL,grant_version bigint NOT NULL,
 expires_at timestamptz NOT NULL,revision bigint NOT NULL CHECK(revision>0),
 state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED','EXPIRED')),
 PRIMARY KEY(org_id,lease_id),UNIQUE(org_id,credential_id),
 FOREIGN KEY(org_id,grant_id,grant_version) REFERENCES mcpguard_ra_draft.grant_versions
);
CREATE TABLE mcpguard_ra_draft.credential_packages (
 org_id uuid NOT NULL,credential_id uuid NOT NULL,lease_id uuid NOT NULL,
 subject_id uuid NOT NULL,resource_id uuid NOT NULL,issuer_epoch uuid NOT NULL,key_id text NOT NULL,blob_ref text NOT NULL,
 format_version integer NOT NULL CHECK(format_version=1),digest text NOT NULL,
 state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED','EXPIRED')),
 PRIMARY KEY(org_id,credential_id),UNIQUE(org_id,lease_id),
 FOREIGN KEY(org_id,subject_id) REFERENCES mcpguard.principals(org_id,id),
 FOREIGN KEY(org_id,resource_id) REFERENCES mcpguard_ra.resource_heads,
 FOREIGN KEY(org_id,lease_id) REFERENCES mcpguard_ra_draft.credential_leases
);
CREATE TABLE mcpguard_ra_draft.download_tickets (
 org_id uuid NOT NULL,download_id uuid NOT NULL,credential_id uuid NOT NULL,owner_id uuid NOT NULL,client_instance_id uuid,
 expires_at timestamptz NOT NULL,state text NOT NULL CHECK(state IN ('READY','CLAIMED','EXPIRED','CANCELLED')),
 PRIMARY KEY(org_id,download_id),
 FOREIGN KEY(org_id,credential_id) REFERENCES mcpguard_ra_draft.credential_packages,
 FOREIGN KEY(org_id,owner_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE TABLE mcpguard_ra_draft.credential_contexts (
 org_id uuid NOT NULL,context_id uuid NOT NULL,owner_id uuid NOT NULL,client_instance_id uuid NOT NULL,verifier_hash bytea NOT NULL,
 expires_at timestamptz NOT NULL,absolute_expiry timestamptz NOT NULL,
 state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED','EXPIRED')),
 PRIMARY KEY(org_id,context_id),FOREIGN KEY(org_id,owner_id) REFERENCES mcpguard.principals(org_id,id),
 CHECK(expires_at<=absolute_expiry)
);
CREATE TABLE mcpguard_ra_draft.context_activations (
 org_id uuid NOT NULL,context_id uuid NOT NULL,credential_id uuid NOT NULL,resource_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED','EXPIRED')),
 PRIMARY KEY(org_id,context_id,credential_id),
 FOREIGN KEY(org_id,context_id) REFERENCES mcpguard_ra_draft.credential_contexts,
 FOREIGN KEY(org_id,credential_id) REFERENCES mcpguard_ra_draft.credential_packages,
 FOREIGN KEY(org_id,resource_id) REFERENCES mcpguard_ra.resource_heads
);
CREATE TABLE mcpguard_ra_draft.revocations (
 org_id uuid NOT NULL,revocation_id uuid NOT NULL,target_id uuid NOT NULL,
 target_type text NOT NULL CHECK(target_type IN ('CREDENTIAL','GRANT','PRINCIPAL','RESOURCE')),actor_id uuid NOT NULL,
 auth_epoch bigint NOT NULL,committed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 gateway_access text NOT NULL CHECK(gateway_access='REVOKED'),
 in_flight_stop text NOT NULL CHECK(in_flight_stop IN ('NONE','PENDING','CONFIRMED','FAILED','UNKNOWN')),
 native_cleanup text NOT NULL CHECK(native_cleanup IN ('NOT_REQUIRED','PENDING','CONFIRMED','FAILED')),
 PRIMARY KEY(org_id,revocation_id),FOREIGN KEY(org_id,actor_id) REFERENCES mcpguard.principals(org_id,id)
);
CREATE TABLE mcpguard_ra_draft.command_keys (
 org_id uuid NOT NULL,actor_id uuid NOT NULL,command text NOT NULL,target text NOT NULL,key text NOT NULL,payload_hash text NOT NULL,
 safe_response_ref text NOT NULL,PRIMARY KEY(org_id,actor_id,command,target,key),
 FOREIGN KEY(org_id,actor_id) REFERENCES mcpguard.principals(org_id,id)
);
-- Promotion gates: immutable version/decision/plan triggers; current grant and
-- package locking against the shared authorization head; claim CAS; owner/resource
-- composite FKs across leases/contexts/activations; expiration ordering; audit
-- transactions; explicit application and provisioning privileges. This DRAFT
-- does not constitute an executable production migration.
REVOKE ALL ON ALL TABLES IN SCHEMA mcpguard_ra_draft FROM PUBLIC;

