// Private local component. It is intentionally not registered as an MCP tool.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/xielforever/MCPGuard/internal/deliveryclient"
	"os"
	"time"
)

type discard struct{}

func (*discard) Write(p []byte) (int, error) { return len(p), nil }
func run() error {
	if len(os.Args) < 2 {
		return deliveryclient.ErrSession
	}
	flags := flag.NewFlagSet("private-session", flag.ContinueOnError)
	flags.SetOutput(new(discard))
	origin := flags.String("gateway", "", "Gateway origin")
	auth := flags.String("auth-file", "", "Enrolled private client bootstrap")
	dir := flags.String("directory", "", "Private cache profile directory")
	loopback := flags.Bool("allow-loopback-http", false, "Isolated local HTTP fixture only")
	replace := flags.Bool("replace-context", false, "Explicitly create a new Context")
	id := flags.String("credential-id", "", "Safe credential ID")
	grant := flags.String("grant-id", "", "Safe grant ID")
	limits := deliveryclient.DefaultCacheLimits()
	flags.IntVar(&limits.Files, "max-files", limits.Files, "New profile file bound")
	flags.Int64Var(&limits.Bytes, "max-bytes", limits.Bytes, "New profile byte bound")
	flags.IntVar(&limits.PageSize, "page-size", limits.PageSize, "New profile queue page bound")
	flags.IntVar(&limits.Pages, "max-pages", limits.Pages, "New profile per-sync page bound")
	flags.IntVar(&limits.RenewBeforeSeconds, "renew-before-seconds", limits.RenewBeforeSeconds, "New profile active Context renewal threshold")
	if flags.Parse(os.Args[2:]) != nil || flags.NArg() != 0 {
		return deliveryclient.ErrSession
	}
	c, e := deliveryclient.New(*origin, os.Getenv("MCPGUARD_TOKEN"), *auth, *loopback)
	if e != nil {
		return e
	}
	s, e := deliveryclient.OpenSession(c, *dir, os.Args[1] == "init", limits)
	if e != nil {
		return e
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	var out any
	switch os.Args[1] {
	case "init":
		out, e = s.Init(ctx, *replace)
	case "status":
		out, e = s.Status(ctx)
	case "sync":
		out, e = s.Sync(ctx)
	case "renew-context":
		out, e = s.RenewContext(ctx, false)
	case "renew-credential":
		out, e = s.RenewCredential(ctx, *id)
	case "close":
		out, e = s.RenewContext(ctx, true)
	case "prepare":
		out, e = s.Prepare(ctx, *grant)
	default:
		return deliveryclient.ErrSession
	}
	if e != nil {
		return deliveryclient.ErrSession
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "Private session operation did not complete. Check current identity and Context; use an explicit Context replacement after expiry. No private contents are printed.")
		os.Exit(1)
	}
}
