// headers writes secrets only to a native host's private header-hook channel.
// worker and capabilities never print headers, tokens or package bytes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/xielforever/MCPGuard/internal/clientadapter"
	"github.com/xielforever/MCPGuard/internal/deliveryclient"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run() error {
	if len(os.Args) < 2 {
		return clientadapter.ErrAdapter
	}
	flags := flag.NewFlagSet("http-private-helper", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("gateway", "", "Gateway /mcp endpoint")
	auth := flags.String("auth-file", "", "Enrolled private client file")
	dir := flags.String("directory", "", "Private profile outside Git")
	allow := flags.Bool("allow-loopback-http", false, "Isolated fixture only")
	once := flags.Bool("once", false, "One bounded worker pass")
	period := flags.Int("interval-ms", 1000, "Bounded worker interval")
	lifetime := flags.Int("max-runtime-seconds", 3600, "Worker lifetime bound")
	entry := flags.String("entry", "NATIVE_HTTP_HELPER", "Safe capability entry")
	limits := deliveryclient.DefaultCacheLimits()
	flags.IntVar(&limits.PageSize, "page-size", limits.PageSize, "New profile page limit")
	flags.IntVar(&limits.Pages, "max-pages", limits.Pages, "New profile sync page count")
	if flags.Parse(os.Args[2:]) != nil || flags.NArg() != 0 {
		return clientadapter.ErrAdapter
	}
	if os.Args[1] == "capabilities" {
		if *entry != "NATIVE_HTTP_HELPER" && *entry != "STDIO_PRIVATE" && *entry != "URL_ONLY" {
			return clientadapter.ErrAdapter
		}
		return json.NewEncoder(os.Stdout).Encode(clientadapter.Capabilities(*entry))
	}
	controller, e := clientadapter.New(clientadapter.Config{Endpoint: *endpoint, Token: os.Getenv("MCPGUARD_TOKEN"), AuthorizationFile: *auth, Directory: *dir, AllowLoopbackHTTP: *allow, Limits: limits})
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch os.Args[1] {
	case "headers":
		if _, e = controller.Step(ctx, true); e != nil {
			return e
		}
		return controller.WritePrivateHost(os.Stdout)
	case "worker":
		return controller.Watch(ctx, time.Duration(*period)*time.Millisecond, time.Duration(*lifetime)*time.Second, *once)
	default:
		return clientadapter.ErrAdapter
	}
}
func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "Private helper unavailable. Check current identity or reconnect after Context expiry; business requests are not replayed.")
		os.Exit(1)
	}
}
