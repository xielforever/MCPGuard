package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/xielforever/MCPGuard/internal/connect"
	"github.com/xielforever/MCPGuard/internal/wireids"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	endpoint := flag.String("gateway", "", "gateway HTTPS /mcp endpoint")
	allow := flag.Bool("allow-loopback-http", false, "allow explicit loopback HTTP for local testing")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected connector arguments")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := connect.Run(ctx, *endpoint, os.Getenv("MCPGUARD_TOKEN"), *allow, wireids.Stdio()); e != nil {
		fmt.Fprintln(os.Stderr, "connector stopped:", e)
		os.Exit(1)
	}
}
