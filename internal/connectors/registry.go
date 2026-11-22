package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xielforever/MCPGuard/internal/metadata"
	connector "github.com/xielforever/MCPGuard/pkg/connectors"
)

const (
	maxConnectors = 128
	maxGrantees   = 64
	maxConfigSize = 65536
	summaryFields = "c.id::text,c.name,c.kind,c.revision,c.enabled,c.test_status,c.tested_at"
)

type Registry struct {
	store          *metadata.Store
	mysqlAvailable bool
}

func NewRegistry(s *metadata.Store) *Registry { return &Registry{store: s} }

// MySQLAvailable reports the capability established by the startup check.
func (r *Registry) MySQLAvailable() bool { return r.mysqlAvailable }

func (r *Registry) Check(ctx context.Context) error {
	if r == nil || r.store == nil || r.store.Pool == nil || r.store.Vault == nil {
		return ErrUnavailable
	}
	var version int
	err := r.store.Pool.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM mcpguard.schema_versions`).Scan(&version)
	if err != nil || (version != 6 && version != 7) {
		return ErrUnavailable
	}
	var ready bool
	err = r.store.Pool.QueryRow(ctx, `SELECT
		current_setting('transaction_read_only')='off'
		AND has_table_privilege('mcpguard.connectors','SELECT')
		AND has_table_privilege('mcpguard.connectors','INSERT')
		AND has_column_privilege('mcpguard.connectors','revision','UPDATE')
		AND has_column_privilege('mcpguard.connectors','enabled','UPDATE')
		AND has_column_privilege('mcpguard.connectors','test_status','UPDATE')
		AND has_column_privilege('mcpguard.connectors','tested_at','UPDATE')
		AND NOT has_column_privilege('mcpguard.connectors','config_ciphertext','UPDATE')
		AND NOT has_table_privilege('mcpguard.connectors','DELETE,TRUNCATE')
		AND has_table_privilege('mcpguard.connector_grants','SELECT')
		AND has_table_privilege('mcpguard.connector_grants','INSERT')
		AND has_table_privilege('mcpguard.connector_audit','SELECT')
		AND has_table_privilege('mcpguard.connector_audit','INSERT')
		AND NOT has_table_privilege('mcpguard.connector_audit','UPDATE,DELETE,TRUNCATE')
		AND has_sequence_privilege(pg_get_serial_sequence('mcpguard.audit_events','seq'),'USAGE')`).Scan(&ready)
	if err != nil || !ready {
		return ErrUnavailable
	}
	r.mysqlAvailable = version == 7
	return nil
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func registryError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var failure *pgconn.PgError
	if errors.As(err, &failure) && (failure.Code == "23505" || failure.Code == "40001" || failure.Code == "55P03") {
		return ErrConflict
	}
	return ErrUnavailable
}

// Access is checked under the authorization epoch lock. Mutations take its
// exclusive form before the control head, matching other control-plane writers.
func (r *Registry) begin(ctx context.Context, p metadata.Principal, admin, write bool) (tx pgx.Tx, canRead bool, err error) {
	if uuid.Validate(p.OrgID) != nil || uuid.Validate(p.ID) != nil {
		return nil, false, ErrDenied
	}
	tx, err = r.store.Pool.Begin(ctx)
	if err != nil {
		return nil, false, ErrUnavailable
	}
	defer func() {
		if err != nil {
			rollback(tx)
		}
	}()
	lock := "SELECT mcpguard.lock_auth_epoch($1)"
	if write {
		lock = "SELECT mcpguard.lock_auth_epoch_exclusive($1)"
	}
	if _, e := tx.Exec(ctx, lock, p.OrgID); e != nil {
		return tx, false, ErrUnavailable
	}
	var isAdmin bool
	e := tx.QueryRow(ctx, `SELECT p.can_read,coalesce(m.is_admin,false)
		FROM mcpguard.principals p LEFT JOIN mcpguard.control_members m
		ON m.org_id=p.org_id AND m.principal_id=p.id
		WHERE p.org_id=$1 AND p.id=$2 AND p.token_hash=$3 AND p.enabled AND p.expires_at>clock_timestamp()`,
		p.OrgID, p.ID, p.TokenHash).Scan(&canRead, &isAdmin)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && admin && !isAdmin {
		return tx, false, ErrDenied
	}
	if e != nil {
		return tx, false, ErrUnavailable
	}
	if write {
		var head int64
		if e = tx.QueryRow(ctx, `SELECT revision FROM mcpguard.control_heads WHERE org_id=$1 FOR UPDATE`, p.OrgID).Scan(&head); e != nil {
			return tx, false, registryError(e)
		}
	}
	return tx, canRead, nil
}

func scanSummary(row pgx.Row, ciphertext *[]byte) (Summary, error) {
	var s Summary
	var kind string
	dest := []any{&s.ID, &s.Name, &kind, &s.Revision, &s.Enabled, &s.TestStatus, &s.TestedAt}
	if ciphertext != nil {
		dest = append(dest, ciphertext)
	}
	err := row.Scan(dest...)
	s.Type = connector.Kind(kind)
	return s, err
}

func (r *Registry) ListAdmin(ctx context.Context, p metadata.Principal) ([]Summary, error) {
	return r.list(ctx, p, true)
}

func (r *Registry) IsAdmin(ctx context.Context, p metadata.Principal) (bool, error) {
	tx, _, err := r.begin(ctx, p, true, false)
	if errors.Is(err, ErrDenied) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rollback(tx)
	return true, nil
}

func (r *Registry) ListUsable(ctx context.Context, p metadata.Principal) ([]Summary, error) {
	return r.list(ctx, p, false)
}

func (r *Registry) list(ctx context.Context, p metadata.Principal, admin bool) ([]Summary, error) {
	tx, canRead, err := r.begin(ctx, p, admin, false)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	out := make([]Summary, 0)
	if !admin && !canRead {
		return out, nil
	}
	query := "SELECT " + summaryFields + " FROM mcpguard.connectors c WHERE c.org_id=$1"
	args := []any{p.OrgID}
	if !admin {
		query += ` AND c.enabled AND EXISTS (
			SELECT 1 FROM mcpguard.connector_grants g WHERE g.org_id=c.org_id AND g.connector_id=c.id AND g.principal_id=$2)`
		args = append(args, p.ID)
	}
	rows, err := tx.Query(ctx, query+" ORDER BY c.name,c.id", args...)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		summary, err := scanSummary(rows, nil)
		if err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, summary)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}

type protectedConfig struct {
	Config      connector.Config      `json:"config"`
	Credentials connector.Credentials `json:"credentials"`
}

func binding(orgID, id string, kind connector.Kind) string {
	// The protected definition has no edit path. The operational revision
	// changes when the immutable definition is tested or enabled.
	return orgID + "/" + id + "/" + string(kind) + "/connector-config-v1"
}

func (r *Registry) definition(orgID string, summary Summary, ciphertext []byte) (connector.Definition, error) {
	account, err := r.store.Vault.Open(ciphertext, binding(orgID, summary.ID, summary.Type))
	if err != nil {
		return connector.Definition{}, ErrUnavailable
	}
	defer clear(account)
	var plain protectedConfig
	if err := json.Unmarshal(account, &plain); err != nil {
		return connector.Definition{}, ErrUnavailable
	}
	return connector.Definition{
		ID: summary.ID, OrgID: orgID, Type: summary.Type, Revision: summary.Revision,
		Config: plain.Config, Credentials: plain.Credentials,
	}, nil
}

func (r *Registry) GetUsable(ctx context.Context, p metadata.Principal, id string) (connector.Definition, error) {
	if uuid.Validate(id) != nil {
		return connector.Definition{}, ErrInput
	}
	tx, canRead, err := r.begin(ctx, p, false, false)
	if err != nil {
		return connector.Definition{}, err
	}
	defer rollback(tx)
	p.CanRead = canRead
	return r.GetUsableInTx(ctx, tx, p, id)
}

// GetUsableInTx shares the authorization snapshot of an action creation or
// execution claim. The caller must hold the organization's auth epoch lock
// and pass a principal whose identity and read privilege were verified there.
func (r *Registry) GetUsableInTx(ctx context.Context, tx pgx.Tx, p metadata.Principal, id string) (connector.Definition, error) {
	if uuid.Validate(id) != nil {
		return connector.Definition{}, ErrInput
	}
	if !p.CanRead {
		return connector.Definition{}, ErrNotFound
	}
	var ciphertext []byte
	summary, err := scanSummary(tx.QueryRow(ctx, "SELECT "+summaryFields+`,c.config_ciphertext FROM mcpguard.connectors c
		JOIN mcpguard.connector_grants g ON (g.org_id,g.connector_id)=(c.org_id,c.id)
		WHERE c.org_id=$1 AND c.id=$2 AND g.principal_id=$3 AND c.enabled`, p.OrgID, id, p.ID), &ciphertext)
	if err != nil {
		return connector.Definition{}, registryError(err)
	}
	return r.definition(p.OrgID, summary, ciphertext)
}

func normalizeGrantees(ids []string) ([]string, error) {
	if len(ids) == 0 || len(ids) > maxGrantees {
		return nil, ErrInput
	}
	out := make([]string, len(ids))
	for i, raw := range ids {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, ErrInput
		}
		out[i] = id.String()
	}
	slices.Sort(out)
	if len(slices.Compact(out)) != len(out) {
		return nil, ErrInput
	}
	return out, nil
}

func validKey(key string) bool { return len(key) >= 16 && len(key) <= 128 }

func (r *Registry) replay(ctx context.Context, tx pgx.Tx, p metadata.Principal, route, key, hash string) (Summary, bool, error) {
	var digest string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT payload_hash,response FROM mcpguard.control_operations
		WHERE org_id=$1 AND actor_id=$2 AND route=$3 AND key=$4`, p.OrgID, p.ID, route, key).Scan(&digest, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Summary{}, false, nil
	}
	if err != nil {
		return Summary{}, false, ErrUnavailable
	}
	if digest != hash {
		return Summary{}, false, ErrConflict
	}
	var result Summary
	if json.Unmarshal(raw, &result) != nil {
		return Summary{}, false, ErrUnavailable
	}
	return result, true, nil
}

func save(ctx context.Context, tx pgx.Tx, p metadata.Principal, route, key, hash string, summary Summary) error {
	b, err := json.Marshal(summary)
	if err != nil {
		return ErrUnavailable
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mcpguard.control_operations(org_id,actor_id,route,key,payload_hash,response)
		VALUES($1,$2,$3,$4,$5,$6)`, p.OrgID, p.ID, route, key, hash, b); err != nil {
		return registryError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ErrUnavailable
	}
	return nil
}

func audit(ctx context.Context, tx pgx.Tx, p metadata.Principal, summary Summary, event string) error {
	_, err := tx.Exec(ctx, `INSERT INTO mcpguard.connector_audit(org_id,connector_id,principal_id,revision,event_type)
		VALUES($1,$2,$3,$4,$5)`, p.OrgID, summary.ID, p.ID, summary.Revision, event)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func (r *Registry) Create(ctx context.Context, p metadata.Principal, input CreateInput, key string) (Summary, error) {
	const route = "connectors/create"
	if !validKey(key) || input.Name == "" || utf8.RuneCountInString(input.Name) > 100 {
		return Summary{}, ErrInput
	}
	switch input.Type {
	case connector.PostgreSQL, connector.MySQL, connector.Redis, connector.Jenkins, connector.Kubernetes:
	default:
		return Summary{}, ErrInput
	}
	grantees, err := normalizeGrantees(input.PrincipalIDs)
	if err != nil {
		return Summary{}, err
	}
	input.PrincipalIDs = grantees
	payload, err := json.Marshal(input)
	if err != nil || len(payload) > maxConfigSize {
		return Summary{}, ErrInput
	}
	hash := r.store.Vault.Hash(payload)
	clear(payload)

	tx, _, err := r.begin(ctx, p, true, true)
	if err != nil {
		return Summary{}, err
	}
	defer rollback(tx)
	if result, found, err := r.replay(ctx, tx, p, route, key, hash); err != nil || found {
		return result, err
	}
	var count, granteeCount int
	if err = tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM mcpguard.connectors WHERE org_id=$1),
		(SELECT count(*) FROM mcpguard.principals WHERE org_id=$1 AND id::text=ANY($2::text[]))`,
		p.OrgID, grantees).Scan(&count, &granteeCount); err != nil {
		return Summary{}, ErrUnavailable
	}
	if count >= maxConnectors {
		return Summary{}, ErrConflict
	}
	if granteeCount != len(grantees) {
		return Summary{}, ErrInput
	}
	plain, err := json.Marshal(protectedConfig{Config: input.Config, Credentials: input.Credentials})
	if err != nil {
		return Summary{}, ErrInput
	}
	id := uuid.NewString()
	ciphertext := r.store.Vault.Seal(plain, binding(p.OrgID, id, input.Type))
	clear(plain)
	summary, err := scanSummary(tx.QueryRow(ctx, `INSERT INTO mcpguard.connectors AS c(org_id,id,name,kind,config_ciphertext)
		VALUES($1,$2,$3,$4,$5) RETURNING `+summaryFields, p.OrgID, id, input.Name, input.Type, ciphertext), nil)
	if err != nil {
		return Summary{}, registryError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO mcpguard.connector_grants(org_id,connector_id,principal_id)
		SELECT org_id,$2,id FROM mcpguard.principals WHERE org_id=$1 AND id::text=ANY($3::text[])`,
		p.OrgID, id, grantees); err != nil {
		return Summary{}, registryError(err)
	}
	if err = audit(ctx, tx, p, summary, "CREATED"); err != nil {
		return Summary{}, err
	}
	return summary, save(ctx, tx, p, route, key, hash, summary)
}

func (r *Registry) Test(ctx context.Context, p metadata.Principal, id string, revision int64, key string, probe func(context.Context, connector.Definition) error) (Summary, error) {
	if uuid.Validate(id) != nil || revision < 1 || !validKey(key) || probe == nil {
		return Summary{}, ErrInput
	}
	const route = "connectors/test"
	hash := r.operationHash(id, revision, false)
	summary, def, err := r.testInput(ctx, p, id, revision, route, key, hash)
	if err != nil || def.ID == "" {
		return summary, err
	}

	// Provider I/O does not hold a database transaction or lock. A stale
	// probe cannot mark a connector enabled or overwrite a newer status.
	status := "PASSED"
	event := "TEST_PASSED"
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	if probe(probeCtx, def) != nil {
		status, event = "FAILED", "TEST_FAILED"
	}
	cancel()

	tx, _, err := r.begin(ctx, p, true, true)
	if err != nil {
		return Summary{}, err
	}
	defer rollback(tx)
	if result, found, err := r.replay(ctx, tx, p, route, key, hash); err != nil || found {
		return result, err
	}
	summary, err = scanSummary(tx.QueryRow(ctx, `UPDATE mcpguard.connectors AS c
		SET test_status=$4,tested_at=clock_timestamp(),revision=c.revision+1
		WHERE c.org_id=$1 AND c.id=$2 AND c.revision=$3 AND NOT c.enabled RETURNING `+summaryFields,
		p.OrgID, id, revision, status), nil)
	if errors.Is(err, pgx.ErrNoRows) {
		return Summary{}, ErrConflict
	}
	if err != nil {
		return Summary{}, registryError(err)
	}
	if err = audit(ctx, tx, p, summary, event); err != nil {
		return Summary{}, err
	}
	return summary, save(ctx, tx, p, route, key, hash, summary)
}

func (r *Registry) testInput(ctx context.Context, p metadata.Principal, id string, revision int64, route, key, hash string) (Summary, connector.Definition, error) {
	tx, _, err := r.begin(ctx, p, true, false)
	if err != nil {
		return Summary{}, connector.Definition{}, err
	}
	defer rollback(tx)
	if result, found, err := r.replay(ctx, tx, p, route, key, hash); err != nil || found {
		return result, connector.Definition{}, err
	}
	var ciphertext []byte
	summary, err := scanSummary(tx.QueryRow(ctx, "SELECT "+summaryFields+`,c.config_ciphertext FROM mcpguard.connectors c
		WHERE c.org_id=$1 AND c.id=$2`, p.OrgID, id), &ciphertext)
	if err != nil {
		return Summary{}, connector.Definition{}, registryError(err)
	}
	if summary.Revision != revision || summary.Enabled {
		return Summary{}, connector.Definition{}, ErrConflict
	}
	def, err := r.definition(p.OrgID, summary, ciphertext)
	return summary, def, err
}

func (r *Registry) operationHash(id string, revision int64, enabled bool) string {
	payload, _ := json.Marshal(struct {
		ID       string
		Revision int64
		Enabled  bool
	}{ID: id, Revision: revision, Enabled: enabled})
	return r.store.Vault.Hash(payload)
}

func (r *Registry) SetEnabled(ctx context.Context, p metadata.Principal, id string, revision int64, key string, enabled bool) (Summary, error) {
	if uuid.Validate(id) != nil || revision < 1 || !validKey(key) {
		return Summary{}, ErrInput
	}
	const route = "connectors/set-enabled"
	hash := r.operationHash(id, revision, enabled)
	tx, _, err := r.begin(ctx, p, true, true)
	if err != nil {
		return Summary{}, err
	}
	defer rollback(tx)
	if result, found, err := r.replay(ctx, tx, p, route, key, hash); err != nil || found {
		return result, err
	}
	summary, err := scanSummary(tx.QueryRow(ctx, "SELECT "+summaryFields+` FROM mcpguard.connectors c
		WHERE c.org_id=$1 AND c.id=$2`, p.OrgID, id), nil)
	if err != nil {
		return Summary{}, registryError(err)
	}
	if summary.Revision != revision || enabled && summary.TestStatus != "PASSED" {
		return Summary{}, ErrConflict
	}
	if summary.Enabled == enabled {
		return summary, save(ctx, tx, p, route, key, hash, summary)
	}
	summary, err = scanSummary(tx.QueryRow(ctx, `UPDATE mcpguard.connectors AS c SET enabled=$3,revision=c.revision+1
		WHERE c.org_id=$1 AND c.id=$2 RETURNING `+summaryFields, p.OrgID, id, enabled), nil)
	if err != nil {
		return Summary{}, registryError(err)
	}
	event := "DISABLED"
	if enabled {
		event = "ENABLED"
	}
	if err = audit(ctx, tx, p, summary, event); err != nil {
		return Summary{}, err
	}
	return summary, save(ctx, tx, p, route, key, hash, summary)
}

func (r *Registry) Events(ctx context.Context, p metadata.Principal, id string) ([]Event, error) {
	if uuid.Validate(id) != nil {
		return nil, ErrInput
	}
	tx, _, err := r.begin(ctx, p, true, false)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcpguard.connectors WHERE org_id=$1 AND id=$2)`,
		p.OrgID, id).Scan(&exists); err != nil {
		return nil, ErrUnavailable
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT event_type,revision,principal_id::text,created_at FROM mcpguard.connector_audit
		WHERE org_id=$1 AND connector_id=$2 ORDER BY seq DESC LIMIT 100`, p.OrgID, id)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := make([]Event, 0)
	for rows.Next() {
		var event Event
		if rows.Scan(&event.Type, &event.Revision, &event.ActorID, &event.At) != nil {
			return nil, ErrUnavailable
		}
		out = append(out, event)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}
