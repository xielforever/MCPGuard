package resources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

type Endpoint struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	SSLMode  string `json:"sslMode"`
}

func (e Endpoint) Fingerprint() string {
	b, _ := json.Marshal(map[string]any{"host": e.Host, "port": e.Port, "database": e.Database, "sslMode": e.SSLMode})
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

type NativeSecret struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Version  string `json:"version"`
}
type SecretProvider interface {
	Resolve(context.Context, string, string, string) (Endpoint, NativeSecret, error)
}
type FileSecrets struct {
	endpoints map[string]Endpoint
	secrets   map[string]NativeSecret
}

func LoadFileSecrets(path string) (*FileSecrets, error) {
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return nil, ErrUnavailable
	}
	data, e := os.ReadFile(path)
	if e != nil || v02.CheckJSON(data) != nil {
		return nil, ErrUnavailable
	}
	var private struct {
		Endpoints map[string]Endpoint     `json:"endpoints"`
		Secrets   map[string]NativeSecret `json:"secrets"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&private) != nil || private.Endpoints == nil || private.Secrets == nil {
		return nil, ErrUnavailable
	}
	// Private references are immutable within this provider instance. Rotation
	// creates a new secret version; it never modifies bytes under a live version.
	for _, endpoint := range private.Endpoints {
		ip := net.ParseIP(endpoint.Host)
		if ip == nil || !ip.IsLoopback() || endpoint.Port < 1 || endpoint.Port > 65535 || endpoint.SSLMode != "disable" ||
			!regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(endpoint.Database) || endpoint.Database == "mcpguard_meta" {
			return nil, ErrUnavailable
		}
	}
	for key, secret := range private.Secrets {
		if len(secret.Password) < 32 || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(secret.Username) || !regexp.MustCompile(`^[1-9][0-9]{0,17}$`).MatchString(secret.Version) || key == "" {
			return nil, ErrUnavailable
		}
	}
	return &FileSecrets{private.Endpoints, private.Secrets}, nil
}
func (f *FileSecrets) Resolve(ctx context.Context, endpointRef, secretRef, version string) (Endpoint, NativeSecret, error) {
	if ctx.Err() != nil {
		return Endpoint{}, NativeSecret{}, ErrUnavailable
	}
	endpoint, ok := f.endpoints[endpointRef]
	secret, found := f.secrets[secretRef+"@"+version]
	if !ok || !found || secret.Version != version {
		return Endpoint{}, NativeSecret{}, ErrUnavailable
	}
	return endpoint, secret, nil
}

// ReadManager is the RA-03 foundation. No production MCP read route invokes it
// before the grant and encrypted-package stages are implemented.
type ReadManager struct {
	Catalog  *Catalog
	provider SecretProvider
	mu       sync.Mutex
	pools    map[string]*pgxpool.Pool
	closed   bool
}

func NewReadManager(c *Catalog, p SecretProvider) *ReadManager {
	return &ReadManager{Catalog: c, provider: p, pools: map[string]*pgxpool.Pool{}}
}
func (m *ReadManager) PoolCount() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.pools) }
func (m *ReadManager) Close() {
	m.mu.Lock()
	m.closed = true
	pools := m.pools
	m.pools = map[string]*pgxpool.Pool{}
	m.mu.Unlock()
	for _, p := range pools {
		p.Close()
	}
}
func (m *ReadManager) pool(ctx context.Context, p metadata.Principal, s Snapshot) (*pgxpool.Pool, error) {
	return m.poolMode(ctx, p, s, "ro")
}

// writePool serves the single fixed write function. The account still has no
// table-level write privilege; only the definer function may modify its row.
func (m *ReadManager) writePool(ctx context.Context, p metadata.Principal, s Snapshot) (*pgxpool.Pool, error) {
	return m.poolMode(ctx, p, s, "rw")
}

func (m *ReadManager) poolMode(ctx context.Context, p metadata.Principal, s Snapshot, mode string) (*pgxpool.Pool, error) {
	endpoint, secret, e := m.provider.Resolve(ctx, s.Resource.EndpointConfigRef, s.Binding.SecretRef, s.Binding.SecretVersion)
	if e != nil {
		return nil, fmt.Errorf("pool resolve: %w", e)
	}
	if endpoint.Fingerprint() != s.Resource.TargetFingerprint || secret.Username != s.Binding.NativeIdentity || secret.Version != s.Binding.SecretVersion {
		return nil, fmt.Errorf("pool binding mismatch: %w", ErrUnavailable)
	}
	ip := net.ParseIP(endpoint.Host)
	if ip == nil || !ip.IsLoopback() || endpoint.SSLMode != "disable" || endpoint.Database == "mcpguard_meta" {
		return nil, fmt.Errorf("pool endpoint (host=%s db=%s): %w", endpoint.Host, endpoint.Database, ErrUnavailable)
	}
	key := fmt.Sprintf("%s/%s/%s/%s/%s/%d/%s/%s/%s/%s/%s/%s", mode, p.OrgID, p.ID, s.Resource.ResourceID, s.Resource.Version, s.Epoch, s.Binding.BindingID, s.Binding.BindingVersion, s.Binding.NativeIdentity, s.Binding.SecretVersion, s.Profile.Version, endpoint.Fingerprint())
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrUnavailable
	}
	prefix := mode + "/" + p.OrgID + "/" + p.ID + "/" + s.Resource.ResourceID + "/"
	for previous, old := range m.pools {
		if previous != key && strings.HasPrefix(previous, prefix) {
			delete(m.pools, previous)
			go old.Close()
		}
	}
	if pool := m.pools[key]; pool != nil {
		return pool, nil
	}
	if len(m.pools) >= v02.MaxFixturePools {
		return nil, fmt.Errorf("pool limit %d: %w", v02.MaxFixturePools, ErrUnavailable)
	}
	target := url.URL{Scheme: "postgres", User: url.UserPassword(secret.Username, secret.Password), Host: net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)), Path: "/" + endpoint.Database, RawQuery: "sslmode=disable"}
	config, e := pgxpool.ParseConfig(target.String())
	if e != nil {
		return nil, ErrUnavailable
	}
	config.MaxConns = v02.FixturePoolMaxConns
	config.MinConns = 0
	config.MaxConnIdleTime = time.Minute
	config.ConnConfig.ConnectTimeout = 2 * time.Second
	readOnly, appName := "on", "mcpguard-ra03-reader"
	if mode == "rw" {
		readOnly, appName = "off", "mcpguard-ra12-writer"
	}
	config.ConnConfig.RuntimeParams = map[string]string{"application_name": appName, "default_transaction_read_only": readOnly, "row_security": "on", "statement_timeout": "3000", "idle_in_transaction_session_timeout": "5000", "search_path": "pg_catalog"}
	config.ConnConfig.BuildContextWatcherHandler = func(conn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: conn, CancelRequestDelay: 0, DeadlineDelay: time.Second}
	}
	config.AfterRelease = func(conn *pgx.Conn) bool {
		cleanup, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		if conn.DeallocateAll(cleanup) != nil {
			return false
		}
		_, err := conn.Exec(cleanup, "DISCARD ALL")
		return err == nil
	}
	pool, e := pgxpool.NewWithConfig(ctx, config)
	if e != nil {
		return nil, ErrUnavailable
	}
	m.pools[key] = pool
	return pool, nil
}

const roleCheck = `SELECT session_user::text,current_user::text,current_database(),
 NOT (r.rolsuper OR r.rolbypassrls OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication)
 AND NOT EXISTS(SELECT FROM pg_auth_members a WHERE a.member=r.oid)
 AND NOT has_schema_privilege(current_user,'resource_demo','CREATE')
 AND has_table_privilege(current_user,'resource_demo.items','SELECT')
 AND NOT has_table_privilege(current_user,'resource_demo.items','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 AND (SELECT c.relkind='r' AND c.relrowsecurity AND c.relowner<>r.oid FROM pg_class c WHERE c.oid='resource_demo.items'::regclass)
 AND (SELECT count(*)=1 AND bool_and(p.polcmd='r' AND p.polpermissive AND p.polroles=ARRAY[0::oid]
  AND pg_get_expr(p.polqual,p.polrelid)='(owner_name = CURRENT_USER)' AND p.polwithcheck IS NULL)
  FROM pg_policy p WHERE p.polrelid='resource_demo.items'::regclass AND p.polcmd='r')
 AND (SELECT count(*)<=1 AND coalesce(bool_and(p.polpermissive AND p.polroles=ARRAY[0::oid]
  AND pg_get_expr(p.polqual,p.polrelid)='(owner_name = CURRENT_USER)'
  AND pg_get_expr(p.polwithcheck,p.polrelid)='(owner_name = CURRENT_USER)'),true)
  FROM pg_policy p WHERE p.polrelid='resource_demo.items'::regclass AND p.polcmd='w')
 FROM pg_roles r WHERE r.rolname=current_user`
const ReadItemSQL = "SELECT item_id,note,version::text FROM resource_demo.items WHERE item_id=$1 LIMIT 1"

func (m *ReadManager) nativeRead(ctx context.Context, p metadata.Principal, s Snapshot, itemID string) ([]v02.ReadItem, error) {
	pool, e := m.pool(ctx, p, s)
	if e != nil {
		return nil, e
	}
	tx, e := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, ErrUnavailable
	}
	defer rollback(tx)
	if _, e = tx.Exec(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL row_security=on"); e != nil {
		return nil, ErrUnavailable
	}
	var login, effective, database string
	var safe bool
	if tx.QueryRow(ctx, roleCheck).Scan(&login, &effective, &database, &safe) != nil {
		return nil, ErrUnavailable
	}
	endpoint, _, e := m.provider.Resolve(ctx, s.Resource.EndpointConfigRef, s.Binding.SecretRef, s.Binding.SecretVersion)
	if e != nil || !safe || login != s.Binding.NativeIdentity || effective != login || database != endpoint.Database {
		return nil, ErrDenied
	}
	if s.Binding.AccountID != nil {
		// Account-backed bindings retain the stricter RA-05 permission proof on
		// each connection use, including detection of a dropped/recreated role.
		var oid, expectedOID string
		if tx.QueryRow(ctx, extendedRoleCheck, time.Now().UTC()).Scan(&login, &effective, &database, &oid, &safe) != nil || !safe {
			return nil, ErrDenied
		}
		if m.Catalog.Store.Pool.QueryRow(ctx, "SELECT role_oid::text FROM mcpguard_ra.native_observations WHERE org_id=$1 AND account_id=$2 ORDER BY observed_at LIMIT 1", p.OrgID, *s.Binding.AccountID).Scan(&expectedOID) != nil || oid != expectedOID {
			return nil, ErrDenied
		}
	}
	item := v02.ReadItem{}
	e = tx.QueryRow(ctx, ReadItemSQL, itemID).Scan(&item.ItemID, &item.Note, &item.Version)
	if errors.Is(e, pgx.ErrNoRows) {
		return []v02.ReadItem{}, nil
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	if !utf8.ValidString(item.Note) || utf8.RuneCountInString(item.Note) > 500 || !valid("ReadItem", item) {
		return nil, ErrUnavailable
	}
	return []v02.ReadItem{item}, nil
}
func (m *ReadManager) ReadFixture(ctx context.Context, p metadata.Principal, in v02.FixtureReadInput) (v02.FixtureReadResult, error) {
	out := v02.FixtureReadResult{SchemaVersion: "0.2", Items: []v02.ReadItem{}}
	s, id, e := m.Catalog.ClaimRead(ctx, p, in)
	if e != nil {
		return out, e
	}
	timeout := s.Profile.TimeoutMs
	if timeout > v02.DefaultFixtureTimeoutMs {
		timeout = v02.DefaultFixtureTimeoutMs
	}
	call, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	items, e := m.nativeRead(call, p, s, in.ItemID)
	if e == nil {
		current, check := m.Catalog.Snapshot(call, p, in.ResourceID)
		if check != nil || current.Epoch != s.Epoch {
			e = ErrDenied
		}
	}
	if e == nil {
		out.ExecutionID = id
		out.ResourceID = s.Resource.ResourceID
		out.ResourceVersion = s.Resource.Version
		out.BindingID = s.Binding.BindingID
		out.NativeIdentity = s.Binding.NativeIdentity
		out.Found = len(items) > 0
		out.Items = items
		raw, _ := json.Marshal(out)
		if len(raw) > s.Profile.MaxResultBytes {
			e = ErrUnavailable
		}
	}
	state := "SUCCEEDED"
	var payload []byte
	if e == nil {
		payload, _ = json.Marshal(out)
	} else {
		state = "FAILED"
		payload, _ = json.Marshal(v02.Error{Code: "ACCESS_DENIED", Message: "The resource read could not be completed."})
	}
	finish, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	if err := m.Catalog.FinishRead(finish, p, id, state, payload); err != nil {
		return v02.FixtureReadResult{}, ErrUnavailable
	}
	if e != nil {
		return v02.FixtureReadResult{}, e
	}
	return out, nil
}
