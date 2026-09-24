package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xielforever/MCPGuard/internal/api"
	"github.com/xielforever/MCPGuard/internal/auth"
	"github.com/xielforever/MCPGuard/internal/config"
	"github.com/xielforever/MCPGuard/internal/proxy"
	"github.com/xielforever/MCPGuard/internal/rules"
	"github.com/xielforever/MCPGuard/internal/store"
)

func main() {
	configPath := flag.String("config", "mcpguard.yaml", "config file path")
	port := flag.Int("port", 8080, "web UI port")
	flag.Parse()

	// Load Config
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	cfg.Server.Port = *port

	// Init DB
	dbStore, err := store.NewStore(cfg.Database.Driver, cfg.Database.DSN)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer dbStore.Close()

	if err := dbStore.InitSchema(); err != nil {
		log.Fatalf("Failed to initialize schema: %v", err)
	}

	// Init Rules Engine
	ruleEngine := rules.NewEngine()
	builtinRules := rules.BuiltinRules()
	for _, r := range builtinRules {
		ruleEngine.AddRule(r)
	}
	// TODO: Load rules from DB and add to engine

	// Init Auth Service
	authService := auth.NewConfirmService(cfg.Auth)

	// Init API Router
	router := api.SetupRouter(dbStore, ruleEngine, authService)

	// Start Web Server
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		log.Printf("Starting web server on %s\n", addr)
		if err := router.Run(addr); err != nil {
			log.Printf("Web server error: %v", err)
		}
	}()

	// Start Proxy
	if cfg.Proxy.Mode == "stdio" {
		stdioProxy := proxy.NewStdioProxy(cfg.Proxy, ruleEngine, authService, dbStore)
		go func() {
			log.Println("Starting STDIO proxy...")
			if err := stdioProxy.Start(); err != nil {
				log.Fatalf("Proxy error: %v", err)
			}
		}()
		defer stdioProxy.Stop()
	}

	// Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	<-ctx.Done()
	log.Println("Shutdown complete")
}
