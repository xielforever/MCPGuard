// A deliberately local, scoped demonstration backend, not a production inventory
// connector. Its reservations persist in the isolated business PostgreSQL DB.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xielforever/MCPGuard/internal/controlled"
	"github.com/xielforever/MCPGuard/internal/nativepg"
)

type config struct{ Listen, Token, DSN string }

func main() {
	if e := run(); e != nil {
		slog.Error("local demo backend failed")
		os.Exit(1)
	}
}
func run() error {
	b, e := os.ReadFile(os.Getenv("MCPGUARD_DEMO_HTTP_CONFIG"))
	if e != nil {
		return e
	}
	var cfg config
	if e = json.Unmarshal(b, &cfg); e != nil {
		return e
	}
	host, _, e := net.SplitHostPort(cfg.Listen)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || len(cfg.Token) < 32 {
		return errors.New("invalid local backend config")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, e := pgxpool.New(ctx, cfg.DSN)
	if e != nil {
		return e
	}
	defer pool.Close()
	catalog, e := controlled.Catalog()
	if e != nil {
		return e
	}
	var reservation controlled.Tool
	for _, t := range catalog {
		if t.Name == "inventory.reserve_demo" {
			reservation = t
		}
	}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		c, d := context.WithTimeout(r.Context(), time.Second)
		defer d()
		if e := nativepg.CheckDemoDatabase(c, pool); e != nil {
			http.Error(w, "not ready", 503)
			return
		}
		reply(w, map[string]bool{"ready": true})
	})
	mux.HandleFunc("GET /demo/offices/office-demo", func(w http.ResponseWriter, r *http.Request) {
		var display string
		e := pool.QueryRow(r.Context(), `SELECT display_name FROM demo.offices WHERE scope_id=$1 AND office_id='office-demo' AND `+nativepg.DemoDatabasePredicate, r.Header.Get("X-MCPGuard-Scope")).Scan(&display)
		if e != nil {
			http.Error(w, "unavailable", 404)
			return
		}
		reply(w, map[string]any{"officeId": "office-demo", "displayName": display, "timeZone": "Asia/Shanghai"})
	})
	mux.HandleFunc("POST /demo/reservations", func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if e != nil {
			http.Error(w, "invalid", 400)
			return
		}
		args, e := reservation.Arguments(raw)
		if e != nil {
			http.Error(w, "invalid", 400)
			return
		}
		scope := r.Header.Get("X-MCPGuard-Scope")
		tx, e := pool.Begin(r.Context())
		if e != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		defer tx.Rollback(context.Background())
		qty := int(args["quantity"].(float64))
		tag, e := tx.Exec(r.Context(), `UPDATE demo.stock SET available=available-$3 WHERE scope_id=$1 AND item_id=$2 AND available>=$3 AND `+nativepg.DemoDatabasePredicate, scope, args["itemId"], qty)
		if e != nil || tag.RowsAffected() != 1 {
			http.Error(w, "precondition failed", 409)
			return
		}
		id := "reservation_demo_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		_, e = tx.Exec(r.Context(), `INSERT INTO demo.reservations VALUES($1,$2,$3,$4)`, scope, id, args["itemId"], qty)
		if e != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "outcome unavailable", 503)
			return
		}
		reply(w, map[string]any{"reservationId": id, "itemId": args["itemId"], "quantity": qty, "state": "RESERVED"})
	})
	scope := regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+cfg.Token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.URL.Path != "/healthz" && !scope.MatchString(r.Header.Get("X-MCPGuard-Scope")) {
			http.Error(w, "invalid scope", 403)
			return
		}
		mux.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case e = <-done:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
