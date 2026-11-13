package controlled

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type NativeConfig struct {
	WriterDSN     string `json:"writerDsn"`
	RedisAddress  string `json:"redisAddress"`
	RedisUsername string `json:"redisUsername"`
	RedisPassword string `json:"redisPassword"`
	HTTPBase      string `json:"httpBase"`
	HTTPToken     string `json:"httpToken"`
}
type Adapters struct {
	cfg     NativeConfig
	writer  *pgxpool.Pool
	client  *http.Client
	binding string
}
type Outcome struct {
	Data                       any
	Dispatch, SideEffect, Code string
}

func loopbackAddress(addr string) bool {
	h, _, e := net.SplitHostPort(addr)
	return e == nil && net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback()
}
func OpenAdapters(ctx context.Context, path string) (*Adapters, error) {
	a := &Adapters{}
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return nil, errors.New("native configuration unavailable")
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if e = d.Decode(&a.cfg); e != nil {
			return nil, errors.New("invalid native configuration")
		}
	}
	b, _ := json.Marshal(a.cfg)
	b = append(b, catalogYAML...)
	h := sha256.Sum256(b)
	a.binding = hex.EncodeToString(h[:])
	if a.cfg.RedisAddress != "" && (!loopbackAddress(a.cfg.RedisAddress) || a.cfg.RedisUsername == "" || a.cfg.RedisPassword == "") {
		return nil, errors.New("local Redis requires loopback and ACL credentials")
	}
	if a.cfg.HTTPBase != "" {
		u, e := url.Parse(a.cfg.HTTPBase)
		if e != nil || u.Scheme != "http" || !loopbackAddress(u.Host) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || a.cfg.HTTPToken == "" {
			return nil, errors.New("local HTTP resource requires a fixed loopback origin and token")
		}
	}
	a.client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 8192, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	if a.cfg.WriterDSN != "" {
		c, e := pgxpool.ParseConfig(a.cfg.WriterDSN)
		if e != nil {
			return nil, errors.New("invalid writer configuration")
		}
		if net.ParseIP(c.ConnConfig.Host) == nil || !net.ParseIP(c.ConnConfig.Host).IsLoopback() {
			return nil, errors.New("local writer must use loopback")
		}
		c.MaxConns = 4
		c.ConnConfig.ConnectTimeout = 2 * time.Second
		c.ConnConfig.RuntimeParams["statement_timeout"] = "3000"
		c.ConnConfig.RuntimeParams["application_name"] = "mcpguard-controlled-writer"
		a.writer, e = pgxpool.NewWithConfig(ctx, c)
		if e != nil {
			return nil, e
		}
		if e = a.checkWriter(ctx); e != nil {
			a.Close()
			return nil, errors.New("writer role must have only SELECT and note/version UPDATE")
		}
	}
	return a, nil
}
func (a *Adapters) Close() {
	if a.writer != nil {
		a.writer.Close()
	}
	if a.client != nil {
		a.client.CloseIdleConnections()
	}
}
func (a *Adapters) Binding() string { return a.binding }
func (a *Adapters) Available(resource string) bool {
	switch resource {
	case "demo.pg":
		return a.writer != nil
	case "demo.redis":
		return a.cfg.RedisAddress != ""
	case "demo.directory", "demo.inventory":
		return a.cfg.HTTPBase != ""
	}
	return false
}
func (a *Adapters) checkWriter(ctx context.Context) error {
	var ok bool
	e := a.writer.QueryRow(ctx, `SELECT NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR has_schema_privilege(current_user,'demo','CREATE')) AND NOT has_table_privilege(current_user,'demo.items','INSERT,DELETE,TRUNCATE,REFERENCES,TRIGGER') AND has_table_privilege(current_user,'demo.items','SELECT') AND has_column_privilege(current_user,'demo.items','note','UPDATE') AND has_column_privilege(current_user,'demo.items','version','UPDATE') AND NOT has_column_privilege(current_user,'demo.items','scope_id','UPDATE') AND NOT has_column_privilege(current_user,'demo.items','item_id','UPDATE') AND current_setting('transaction_read_only')='off' FROM pg_roles r WHERE r.rolname=current_user`).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("writer privileges unavailable")
	}
	return nil
}
func (a *Adapters) Probe(ctx context.Context, resource string) error {
	switch resource {
	case "demo.pg":
		if a.writer != nil {
			return a.checkWriter(ctx)
		}
	case "demo.redis":
		_, _, e := a.redis(ctx, []string{"PING"}, false)
		return e
	case "demo.directory", "demo.inventory":
		_, e := a.http(ctx, "GET", "/healthz", nil, "")
		return e
	}
	return errors.New("dependency not configured")
}

var scopePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func (a *Adapters) Execute(ctx context.Context, t Tool, scope string, args map[string]any) Outcome {
	out := Outcome{Dispatch: "NOT_DISPATCHED", SideEffect: "NONE", Code: "DEPENDENCY_UNAVAILABLE"}
	if !scopePattern.MatchString(scope) || !a.Available(t.Resource) || ctx.Err() != nil {
		return out
	}
	switch t.Name {
	case "pg.update_demo_note":
		conn, e := a.writer.Acquire(ctx)
		if e != nil {
			return out
		}
		defer conn.Release()
		var version string
		out.Dispatch = "DISPATCH_UNKNOWN"
		out.SideEffect = "UNKNOWN"
		e = conn.QueryRow(ctx, `UPDATE demo.items SET note=$3,version=version+1 WHERE scope_id=$1 AND item_id=$2 AND version=$4::bigint AND version<999999999999999999 RETURNING version::text`, scope, args["itemId"], args["note"], args["expectedVersion"]).Scan(&version)
		var pe *pgconn.PgError
		if errors.Is(e, pgx.ErrNoRows) {
			return Outcome{Dispatch: "DISPATCHED", SideEffect: "NONE", Code: "PRECONDITION_FAILED"}
		}
		if errors.As(e, &pe) {
			return Outcome{Dispatch: "DISPATCHED", SideEffect: "NONE", Code: "NATIVE_OPERATION_FAILED"}
		}
		if e != nil {
			return out
		}
		out = Outcome{Data: map[string]any{"itemId": args["itemId"], "newVersion": version, "affectedRows": 1}, Dispatch: "DISPATCHED", SideEffect: "APPLIED"}
	case "redis.get_demo_note", "redis.set_demo_note":
		key := "demo:" + scope + ":notes:" + args["noteId"].(string)
		cmd := []string{"GET", key}
		write := t.Effect == "WRITE"
		if write {
			cmd = []string{"SET", key, args["value"].(string), "EX", strconv.Itoa(int(args["ttlSeconds"].(float64)))}
		}
		value, sent, e := a.redis(ctx, cmd, write)
		if sent {
			out.Dispatch = "DISPATCH_UNKNOWN"
			if write {
				out.SideEffect = "UNKNOWN"
			}
		}
		if e != nil {
			return out
		}
		out.Dispatch = "DISPATCHED"
		out.Code = ""
		out.SideEffect = "NONE"
		if write {
			if value == nil || *value != "OK" {
				out.Code = "INVALID_NATIVE_RESPONSE"
				out.SideEffect = "UNKNOWN"
			} else {
				out.SideEffect = "APPLIED"
				out.Data = map[string]any{"noteId": args["noteId"], "valueStored": true, "ttlSeconds": args["ttlSeconds"]}
			}
		} else {
			out.Data = map[string]any{"found": value != nil, "value": value, "encoding": "utf-8"}
		}
	case "directory.get_demo_office", "inventory.reserve_demo":
		method, path := "GET", "/demo/offices/office-demo"
		var body []byte
		if t.Effect == "WRITE" {
			method, path = "POST", "/demo/reservations"
			body, _ = json.Marshal(map[string]any{"itemId": args["itemId"], "quantity": args["quantity"]})
		}
		data, e := a.http(ctx, method, path, body, scope)
		out.Dispatch = "DISPATCH_UNKNOWN"
		if t.Effect == "WRITE" {
			out.SideEffect = "UNKNOWN"
		}
		if e != nil {
			return out
		}
		if e = json.Unmarshal(data, &out.Data); e != nil {
			out.Code = "INVALID_NATIVE_RESPONSE"
			return out
		}
		out.Dispatch = "DISPATCHED"
		out.Code = ""
		if t.Effect == "WRITE" {
			out.SideEffect = "APPLIED"
		}
	}
	if out.Code == "" {
		if e := t.ValidateData(out.Data); e != nil {
			out.Data = nil
			out.Code = "INVALID_NATIVE_RESPONSE"
			if t.Effect == "WRITE" {
				out.SideEffect = "UNKNOWN"
			}
		}
	}
	return out
}
func (a *Adapters) http(ctx context.Context, method, path string, body []byte, scope string) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, method, a.cfg.HTTPBase+path, bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.HTTPToken)
	req.Header.Set("X-MCPGuard-Scope", scope)
	req.Header.Set("Content-Type", "application/json")
	resp, e := a.client.Do(req)
	if e != nil {
		return nil, errors.New("native HTTP unavailable")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if e != nil || len(b) > 16384 || resp.StatusCode != 200 {
		return nil, errors.New("native HTTP response rejected")
	}
	return b, nil
}
func respRead(r *bufio.Reader) (*string, error) {
	lineBytes, e := r.ReadSlice('\n')
	line := string(lineBytes)
	if e != nil || len(line) > 128 || !strings.HasSuffix(line, "\r\n") {
		return nil, errors.New("invalid RESP line")
	}
	switch line[0] {
	case '+':
		v := strings.TrimSuffix(line[1:], "\r\n")
		return &v, nil
	case '$':
		n, e := strconv.Atoi(strings.TrimSuffix(line[1:], "\r\n"))
		if e != nil || n < -1 || n > 4096 {
			return nil, errors.New("invalid bulk size")
		}
		if n == -1 {
			return nil, nil
		}
		b := make([]byte, n+2)
		if _, e = io.ReadFull(r, b); e != nil {
			return nil, e
		}
		if string(b[n:]) != "\r\n" || !utf8.Valid(b[:n]) || utf8.RuneCount(b[:n]) > 1024 {
			return nil, errors.New("invalid bulk value")
		}
		v := string(b[:n])
		return &v, nil
	}
	return nil, errors.New("Redis response rejected")
}
func respWrite(w io.Writer, args []string) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	_, e := io.Copy(w, &b)
	return e
}
func (a *Adapters) redis(ctx context.Context, cmd []string, write bool) (*string, bool, error) {
	conn, e := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", a.cfg.RedisAddress)
	if e != nil {
		return nil, false, e
	}
	defer conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	r := bufio.NewReaderSize(conn, 8192)
	if e = respWrite(conn, []string{"AUTH", a.cfg.RedisUsername, a.cfg.RedisPassword}); e != nil {
		return nil, false, e
	}
	ack, e := respRead(r)
	if e != nil || ack == nil || *ack != "OK" {
		return nil, false, errors.New("Redis authentication unavailable")
	}
	if e = respWrite(conn, cmd); e != nil {
		return nil, true, e
	}
	v, e := respRead(r)
	return v, true, e
}
