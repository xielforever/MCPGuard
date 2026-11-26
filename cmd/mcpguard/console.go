package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xielforever/MCPGuard/internal/controlled"
)

// runConsole serves the split console frontend: public static assets plus a
// reverse proxy for the gateway-owned login, API, MCP and health paths. The
// gateway keeps its loopback bind and host/Origin checks; this process only
// makes that loopback service reachable from a separate Deployment.
func runConsole(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("console", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "frontend TCP listen address")
	upstream := fs.String("upstream", "", "gateway HTTP base URL")
	allowNonLoopback := fs.Bool("allow-non-loopback", false, "allow a non-loopback listen address for a cluster-only frontend")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateConsoleListen(*listen, *allowNonLoopback); err != nil {
		return err
	}
	target, err := parseConsoleUpstream(*upstream)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return errors.New("console frontend could not listen")
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           newConsoleHandler(target),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("console frontend ready", "listen", *listen, "upstream", target.String())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("console frontend stopped")
	}
	return nil
}

func validateConsoleListen(address string, allowNonLoopback bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return errors.New("console listen address must be a host and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !allowNonLoopback) {
		return errors.New("console listen address must be loopback unless --allow-non-loopback is explicit")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return errors.New("console listen address must use a valid TCP port")
	}
	return nil
}

func parseConsoleUpstream(raw string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" ||
		target.Fragment != "" || target.Opaque != "" ||
		(target.Scheme != "http" && target.Scheme != "https") {
		return nil, errors.New("console upstream must be an HTTP(S) base URL without credentials, query or fragment")
	}
	if target.Path != "" && target.Path != "/" {
		return nil, errors.New("console upstream must not contain a path")
	}
	target.Path = ""
	return target, nil
}

func newConsoleHandler(upstream *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Director = nil
	proxy.Rewrite = func(request *httputil.ProxyRequest) {
		request.SetURL(upstream)
		// The gateway's browser console checks Host and Origin against its own
		// loopback origin. Preserve both so the public frontend stays same-origin.
		request.Out.Host = request.In.Host
		request.SetXForwarded()
	}
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		slog.Warn("console proxy failed", "error", err)
		http.Error(w, "gateway unavailable", http.StatusBadGateway)
	}

	static := controlled.StaticHandler()
	mux := http.NewServeMux()
	mux.Handle("GET /console/{$}", static)
	mux.Handle("GET /web/", static)
	mux.HandleFunc("GET /console", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/console/", http.StatusTemporaryRedirect)
	})
	mux.Handle("/console/login", proxy)
	mux.Handle("/console/api/", proxy)
	mux.Handle("/mcp", proxy)
	mux.Handle("/healthz", proxy)
	mux.Handle("/readyz", proxy)
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/console/", http.StatusTemporaryRedirect)
	})
	return mux
}