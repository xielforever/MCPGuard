package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/xielforever/MCPGuard/internal/clientadapter"
	"github.com/xielforever/MCPGuard/internal/connect"
	"github.com/xielforever/MCPGuard/internal/deliveryclient"
	"github.com/xielforever/MCPGuard/internal/wireids"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	flag.CommandLine.SetOutput(io.Discard)
	endpoint := flag.String("gateway", "", "gateway HTTPS /mcp endpoint")
	allow := flag.Bool("allow-loopback-http", false, "allow explicit loopback HTTP for local testing")
	auth := flag.String("private-auth-file", "", "Enrolled private client bootstrap")
	dir := flag.String("private-directory", "", "Private profile outside Git")
	caps := flag.Bool("capabilities", false, "Print safe private adapter capabilities and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected connector arguments")
		os.Exit(1)
	}
	if *caps {
		_ = json.NewEncoder(os.Stdout).Encode(clientadapter.Capabilities("STDIO_PRIVATE"))
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var e error
	if *auth != "" || *dir != "" {
		if *auth == "" || *dir == "" {
			e = clientadapter.ErrAdapter
		} else {
			e = connect.RunPrivate(ctx, clientadapter.Config{Endpoint: *endpoint, Token: os.Getenv("MCPGUARD_TOKEN"), AuthorizationFile: *auth, Directory: *dir, AllowLoopbackHTTP: *allow, Limits: deliveryclient.DefaultCacheLimits()}, wireids.Stdio())
		}
	} else {
		e = connect.Run(ctx, *endpoint, os.Getenv("MCPGUARD_TOKEN"), *allow, wireids.Stdio())
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "connector stopped:", e)
		os.Exit(1)
	}
}
