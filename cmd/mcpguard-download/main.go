// One-shot private manual delivery. No server keys or MCP tool registration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/xielforever/MCPGuard/internal/deliveryclient"
	"os"
	"time"
)

func run() error {
	if len(os.Args) < 2 {
		return deliveryclient.ErrDelivery
	}
	flags := flag.NewFlagSet("private-download", flag.ContinueOnError)
	flags.SetOutput(new(discard))
	origin := flags.String("gateway", "", "Gateway origin")
	id := flags.String("download-id", "", "Safe download ID")
	authFile := flags.String("auth-file", "", "Private delivery authorization file")
	dir := flags.String("directory", "", "Existing private destination directory")
	loopback := flags.Bool("allow-loopback-http", false, "Isolated local HTTP fixture only")
	if flags.Parse(os.Args[2:]) != nil || flags.NArg() != 0 {
		return deliveryclient.ErrDelivery
	}
	client, e := deliveryclient.New(*origin, os.Getenv("MCPGUARD_TOKEN"), *authFile, *loopback)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out any
	switch os.Args[1] {
	case "status":
		out, e = client.Status(ctx, *id)
	case "claim":
		out, e = client.Claim(ctx, *id, *dir)
	default:
		e = errors.New("unsupported operation")
	}
	if e != nil {
		return deliveryclient.ErrDelivery
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

type discard struct{}

func (*discard) Write(p []byte) (int, error) { return len(p), nil }
func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "Private download did not complete. Query safe ticket status before retrying; an interrupted response may already have consumed the ticket.")
		os.Exit(1)
	}
}
