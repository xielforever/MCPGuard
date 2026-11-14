package resources

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"time"
)

//go:embed native_provider/001_owner_select.sql
var providerDDL string

//go:embed native_provider/002_recovery_fences.sql
var providerRecoveryDDL string

//go:embed native_provider/003_owner_write.sql
var providerWriteDDL string

func strconvI(n int) string { return strconv.Itoa(n) }

type ProviderPrivate struct {
	Endpoints       map[string]Endpoint     `json:"endpoints"`
	Managers        map[string]string       `json:"managers"`
	ExistingSecrets map[string]NativeSecret `json:"existingSecrets"`
}

func LoadProviderPrivate(path string) (*ProviderPrivate, error) {
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > v02.MaxInputBytes {
		return nil, ErrUnavailable
	}
	raw, e := os.ReadFile(path)
	if e != nil || v02.CheckJSON(raw) != nil {
		return nil, ErrInput
	}
	var p ProviderPrivate
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || p.Endpoints == nil || p.Managers == nil {
		return nil, ErrInput
	}
	for _, ep := range p.Endpoints {
		if !safeProviderEndpoint(ep) {
			return nil, ErrDenied
		}
	}
	for ref, dsn := range p.Managers {
		ep, ok := p.Endpoints[ref]
		cfg, e := pgx.ParseConfig(dsn)
		if !ok || e != nil || cfg.Host != ep.Host || int(cfg.Port) != ep.Port || cfg.Database != ep.Database || cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
			return nil, ErrDenied
		}
	}
	for _, secret := range p.ExistingSecrets {
		if len(secret.Password) < 32 || secret.Version == "" || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(secret.Username) {
			return nil, ErrInput
		}
	}
	return &p, nil
}
func safeProviderEndpoint(ep Endpoint) bool {
	ip := net.ParseIP(ep.Host)
	return ip != nil && ip.IsLoopback() && ep.Port > 0 && ep.Port <= 65535 && ep.Database == "mcpguard_business" && ep.SSLMode == "disable"
}
func nativeConfig(ep Endpoint, secret NativeSecret) (*pgx.ConnConfig, error) {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(secret.Username, secret.Password), Host: net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)), Path: "/" + ep.Database, RawQuery: "sslmode=disable"}
	cfg, e := pgx.ParseConfig(u.String())
	if e != nil {
		return nil, ErrUnavailable
	}
	cfg.ConnectTimeout = 2 * time.Second
	cfg.RuntimeParams = map[string]string{"application_name": "mcpguard-native-verification", "row_security": "on", "search_path": "pg_catalog", "default_transaction_read_only": "on", "statement_timeout": "3000"}
	return cfg, nil
}

type NativeProvisioningProvider interface {
	Create(context.Context, v02.ProvisioningTarget, v02.ProvisioningPlan, string, NativeSecret) error
	Verify(context.Context, v02.ProvisioningPlan, NativeSecret) (v02.NativeObservation, error)
	Disable(context.Context, v02.ProvisioningPlan, string) error
}
type PGProvisioningProvider struct{ Private *ProviderPrivate }

func (p *PGProvisioningProvider) manager(ctx context.Context, ref string) (*pgx.Conn, error) {
	dsn, ok := p.Private.Managers[ref]
	if !ok {
		return nil, ErrUnavailable
	}
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		return nil, ErrUnavailable
	}
	cfg.ConnectTimeout = 2 * time.Second
	cfg.RuntimeParams = map[string]string{"application_name": "mcpguard-restricted-provisioner", "search_path": "pg_catalog", "statement_timeout": "3000"}
	c, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return nil, ErrUnavailable
	}
	var safe bool
	e = c.QueryRow(ctx, `SELECT NOT(rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls) AND NOT EXISTS(SELECT FROM pg_auth_members a WHERE a.member=r.oid) AND NOT has_schema_privilege(current_user,'mcpguard_provision','CREATE') FROM pg_roles r WHERE rolname=current_user`).Scan(&safe)
	if e != nil || !safe {
		c.Close(ctx)
		return nil, ErrDenied
	}
	return c, nil
}
func (p *PGProvisioningProvider) Create(ctx context.Context, target v02.ProvisioningTarget, plan v02.ProvisioningPlan, hash string, secret NativeSecret) error {
	ep, ok := p.Private.Endpoints[plan.EndpointConfigRef]
	if !ok || ep.Fingerprint() != plan.TargetFingerprint {
		return ErrDenied
	}
	c, e := p.manager(ctx, plan.EndpointConfigRef)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	raw, _ := json.Marshal(target)
	var matches bool
	if e = c.QueryRow(ctx, "SELECT mcpguard_provision.target_matches($1,$2::jsonb)", target.TargetID, raw).Scan(&matches); e != nil {
		return ErrUnavailable
	}
	if !matches {
		return ErrDenied
	}
	expiry, _, e := normalizedTime(plan.ExpiresAt)
	if e != nil {
		return e
	}
	create := "mcpguard_provision.create_account"
	if target.Template == "PG_OWNER_UPDATE_V1" {
		create = "mcpguard_provision.create_write_account"
	}
	var oid uint32
	if e = c.QueryRow(ctx, "SELECT "+create+"($1,$2,$3,$4,$5)", target.TargetID, plan.AccountID, hash, secret.Password, expiry).Scan(&oid); e != nil {
		return ErrUnavailable
	}
	return nil
}

const extendedRoleCheck = `SELECT session_user::text,current_user::text,current_database(),r.oid::text,
 NOT(r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls) AND r.rolcanlogin
 AND NOT EXISTS(SELECT FROM pg_auth_members a WHERE a.member=r.oid)
 AND (r.rolvaliduntil IS NULL OR r.rolvaliduntil>=$1::timestamptz)
 AND NOT EXISTS(SELECT FROM pg_namespace n WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND has_schema_privilege(current_user,n.oid,'CREATE'))
 AND has_table_privilege(current_user,'resource_demo.items','SELECT')
 AND NOT has_table_privilege(current_user,'resource_demo.items','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 AND NOT has_any_column_privilege(current_user,'resource_demo.items','INSERT,UPDATE,REFERENCES')
 AND NOT EXISTS(SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND c.relkind IN ('r','v','m','f','p') AND c.oid<>'resource_demo.items'::regclass AND (has_table_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') OR has_any_column_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))
 AND NOT EXISTS(SELECT FROM pg_proc f JOIN pg_namespace n ON n.oid=f.pronamespace WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND f.prosecdef AND has_function_privilege(current_user,f.oid,'EXECUTE'))
 AND (SELECT c.relkind='r' AND c.relrowsecurity AND c.relowner<>r.oid FROM pg_class c WHERE c.oid='resource_demo.items'::regclass)
 AND (SELECT count(*)=1 AND bool_and(p.polcmd='r' AND p.polpermissive AND p.polroles=ARRAY[0::oid] AND pg_get_expr(p.polqual,p.polrelid)='(owner_name = CURRENT_USER)' AND p.polwithcheck IS NULL) FROM pg_policy p WHERE p.polrelid='resource_demo.items'::regclass AND p.polcmd='r')
 AND (SELECT count(*)<=1 AND coalesce(bool_and(p.polpermissive AND p.polroles=ARRAY[0::oid] AND pg_get_expr(p.polqual,p.polrelid)='(owner_name = CURRENT_USER)' AND pg_get_expr(p.polwithcheck,p.polrelid)='(owner_name = CURRENT_USER)'),true) FROM pg_policy p WHERE p.polrelid='resource_demo.items'::regclass AND p.polcmd='w')
 FROM pg_roles r WHERE r.rolname=current_user`

// writeRoleCheck is the write identity counterpart: it may UPDATE only the
// own-row policy target and keeps every other read guarantee.
const writeRoleCheck = `SELECT session_user::text,current_user::text,current_database(),r.oid::text,
 NOT(r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls) AND r.rolcanlogin
 AND NOT EXISTS(SELECT FROM pg_auth_members a WHERE a.member=r.oid)
 AND (r.rolvaliduntil IS NULL OR r.rolvaliduntil>=$1::timestamptz)
 AND NOT EXISTS(SELECT FROM pg_namespace n WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND has_schema_privilege(current_user,n.oid,'CREATE'))
 AND has_table_privilege(current_user,'resource_demo.items','SELECT')
 AND has_table_privilege(current_user,'resource_demo.items','UPDATE')
 AND NOT has_table_privilege(current_user,'resource_demo.items','INSERT,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 AND NOT EXISTS(SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND c.relkind IN ('r','v','m','f','p') AND c.oid<>'resource_demo.items'::regclass AND (has_table_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') OR has_any_column_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))
 AND NOT EXISTS(SELECT FROM pg_proc f JOIN pg_namespace n ON n.oid=f.pronamespace WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND f.prosecdef AND has_function_privilege(current_user,f.oid,'EXECUTE'))
 AND (SELECT c.relkind='r' AND c.relrowsecurity AND c.relowner<>r.oid FROM pg_class c WHERE c.oid='resource_demo.items'::regclass)
 AND (SELECT count(*)=1 AND bool_and(p.polcmd='r' AND p.polpermissive AND p.polroles=ARRAY[0::oid] AND pg_get_expr(p.polqual,p.polrelid)='(owner_name = CURRENT_USER)' AND p.polwithcheck IS NULL) FROM pg_policy p WHERE p.polrelid='resource_demo.items'::regclass AND p.polcmd='r')
 AND (SELECT count(*)=1 AND bool_and(p.polpermissive AND p.polroles=ARRAY[0::oid] AND pg_get_expr(p.polqual,p.polrelid)='(owner_name = CURRENT_USER)' AND pg_get_expr(p.polwithcheck,p.polrelid)='(owner_name = CURRENT_USER)') FROM pg_policy p WHERE p.polrelid='resource_demo.items'::regclass AND p.polcmd='w')
 FROM pg_roles r WHERE r.rolname=current_user`

func (p *PGProvisioningProvider) Verify(ctx context.Context, plan v02.ProvisioningPlan, secret NativeSecret) (v02.NativeObservation, error) {
	var out v02.NativeObservation
	check, templateName, e := p.roleCheck(ctx, plan)
	if e != nil {
		return out, e
	}
	ep, ok := p.Private.Endpoints[plan.EndpointConfigRef]
	if !ok || !safeProviderEndpoint(ep) || ep.Fingerprint() != plan.TargetFingerprint || secret.Username != plan.NativeIdentity || secret.Version != plan.SecretVersion {
		return out, ErrDenied
	}
	cfg, e := nativeConfig(ep, secret)
	if e != nil {
		return out, e
	}
	c, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return out, ErrUnavailable
	}
	defer c.Close(context.Background())
	tx, e := c.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, ErrUnavailable
	}
	defer rollback(tx)
	expiry, _, _ := normalizedTime(plan.ExpiresAt)
	var login, effective, database, oid string
	var safe bool
	if e = tx.QueryRow(ctx, check, expiry).Scan(&login, &effective, &database, &oid, &safe); e != nil {
		return out, ErrUnavailable
	}
	if !safe || login != plan.NativeIdentity || effective != login || database != ep.Database {
		return out, ErrDenied
	}
	var one int
	if e = tx.QueryRow(ctx, "SELECT count(*)::integer FROM (SELECT 1 FROM resource_demo.items LIMIT 1) x").Scan(&one); e != nil {
		return out, ErrDenied
	}
	out = v02.NativeObservation{AccountID: plan.AccountID, NativeIdentity: login, RoleOid: oid, Database: database, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	raw, _ := json.Marshal(struct{ Identity, OID, Database, Fingerprint, Template string }{login, oid, database, ep.Fingerprint(), templateName})
	sum := sha256.Sum256(raw)
	out.PermissionHash = hex.EncodeToString(sum[:])
	return out, nil
}
func (p *PGProvisioningProvider) Disable(ctx context.Context, plan v02.ProvisioningPlan, hash string) error {
	if plan.ManagementMode != "PROVISIONED" {
		return ErrDenied
	}
	c, e := p.manager(ctx, plan.EndpointConfigRef)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	var done bool
	if c.QueryRow(ctx, "SELECT mcpguard_provision.disable_account($1,$2)", plan.AccountID, hash).Scan(&done) != nil || !done {
		return ErrUnavailable
	}
	return nil
}

// roleCheck selects the pinned verification query for the target template.
func (p *PGProvisioningProvider) roleCheck(ctx context.Context, plan v02.ProvisioningPlan) (string, string, error) {
	c, e := p.manager(ctx, plan.EndpointConfigRef)
	if e != nil {
		return "", "", e
	}
	defer c.Close(context.Background())
	var template string
	if e = c.QueryRow(ctx, "SELECT definition->>'template' FROM mcpguard_provision.targets WHERE target_id=$1", plan.TargetID).Scan(&template); e != nil {
		return "", "", ErrDenied
	}
	switch template {
	case "PG_OWNER_SELECT_V1":
		return extendedRoleCheck, template, nil
	case "PG_OWNER_UPDATE_V1":
		return writeRoleCheck, template, nil
	default:
		return "", "", ErrDenied
	}
}
// InstallPGProvider is explicitly administrative and is never called at startup.
func InstallPGProvider(ctx context.Context, dsn, manager string, targets []v02.ProvisioningTarget) error {
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(manager) {
		return ErrInput
	}
	config, e := pgx.ParseConfig(dsn)
	if e != nil {
		return ErrInput
	}
	ep := Endpoint{Host: config.Host, Port: int(config.Port), Database: config.Database, SSLMode: "disable"}
	if !safeProviderEndpoint(ep) || config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		return ErrDenied
	}
	for _, target := range targets {
		if target.TargetFingerprint != ep.Fingerprint() {
			return ErrDenied
		}
	}
	c, e := pgx.Connect(ctx, dsn)
	if e != nil {
		return ErrUnavailable
	}
	defer c.Close(ctx)
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(529401)"); e != nil {
		return e
	}
	var present bool
	if e = tx.QueryRow(ctx, "SELECT to_regclass('mcpguard_provision.schema_versions') IS NOT NULL").Scan(&present); e != nil {
		return e
	}
	sum := sha256.Sum256([]byte(providerDDL))
	checksum := hex.EncodeToString(sum[:])
	if present {
		var saved string
		if tx.QueryRow(ctx, "SELECT checksum FROM mcpguard_provision.schema_versions WHERE version=1").Scan(&saved) != nil || saved != checksum {
			return ErrConflict
		}
	} else {
		if _, e = tx.Exec(ctx, providerDDL); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_provision.schema_versions VALUES(1,$1)", checksum); e != nil {
			return e
		}
	}
	var latest int
	if e = tx.QueryRow(ctx, "SELECT max(version) FROM mcpguard_provision.schema_versions").Scan(&latest); e != nil || latest > 3 {
		return ErrConflict
	}
	recoverySum := sha256.Sum256([]byte(providerRecoveryDDL))
	recoveryChecksum := hex.EncodeToString(recoverySum[:])
	if latest == 1 {
		if _, e = tx.Exec(ctx, providerRecoveryDDL); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_provision.schema_versions VALUES(2,$1)", recoveryChecksum); e != nil {
			return e
		}
	} else {
		var saved string
		if tx.QueryRow(ctx, "SELECT checksum FROM mcpguard_provision.schema_versions WHERE version=2").Scan(&saved) != nil || saved != recoveryChecksum {
			return ErrConflict
		}
	}
	writeSum := sha256.Sum256([]byte(providerWriteDDL))
	writeChecksum := hex.EncodeToString(writeSum[:])
	if latest <= 2 {
		if _, e = tx.Exec(ctx, providerWriteDDL); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_provision.schema_versions VALUES(3,$1)", writeChecksum); e != nil {
			return e
		}
	} else {
		var saved string
		if tx.QueryRow(ctx, "SELECT checksum FROM mcpguard_provision.schema_versions WHERE version=3").Scan(&saved) != nil || saved != writeChecksum {
			return ErrConflict
		}
	}
	for _, target := range targets {
		if !valid("ProvisioningTarget", target) {
			return ErrInput
		}
		raw, _ := json.Marshal(target)
		if _, e = tx.Exec(ctx, "INSERT INTO mcpguard_provision.targets VALUES($1,$2) ON CONFLICT DO NOTHING", target.TargetID, raw); e != nil {
			return e
		}
		var same bool
		if tx.QueryRow(ctx, "SELECT definition=$2::jsonb FROM mcpguard_provision.targets WHERE target_id=$1", target.TargetID, raw).Scan(&same) != nil || !same {
			return ErrConflict
		}
	}
	role := pgx.Identifier{manager}.Sanitize()
	_, e = tx.Exec(ctx, "GRANT USAGE ON SCHEMA mcpguard_provision TO "+role+";GRANT SELECT ON mcpguard_provision.targets TO "+role+";GRANT EXECUTE ON FUNCTION mcpguard_provision.create_account(uuid,uuid,text,text,timestamptz),mcpguard_provision.disable_account(uuid,text),mcpguard_provision.target_matches(uuid,jsonb),mcpguard_provision.create_write_account(uuid,uuid,text,text,timestamptz) TO "+role)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "GRANT EXECUTE ON FUNCTION mcpguard_provision.reconcile_account(uuid,uuid,text,timestamptz),mcpguard_provision.cleanup_account(uuid,uuid,text,timestamptz) TO "+role); e != nil {
		return e
	}
	return tx.Commit(ctx)
}


