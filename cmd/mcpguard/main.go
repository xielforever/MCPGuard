package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xielforever/MCPGuard/internal/controlled"
	"github.com/xielforever/MCPGuard/internal/gateway"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/nativepg"
	"github.com/xielforever/MCPGuard/internal/oauthfixture"
	"github.com/xielforever/MCPGuard/internal/resources"
	"github.com/xielforever/MCPGuard/internal/runtimeconfig"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error(e.Error())
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) >= 2 && os.Args[1] == "migrate" {
		dryRun := len(os.Args) == 3 && os.Args[2] == "--dry-run"
		if len(os.Args) != 2 && !dryRun {
			return errors.New("usage: mcpguard migrate [--dry-run]")
		}
		dsn := os.Getenv("MCPGUARD_MIGRATION_DSN")
		if dsn == "" {
			return errors.New("MCPGUARD_MIGRATION_DSN is required")
		}
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if dryRun {
			plan, err := metadata.PlanMigration(c, dsn)
			if err != nil {
				return errors.New("metadata migration preflight failed; check access and migration history")
			}
			return json.NewEncoder(os.Stdout).Encode(plan)
		}
		if e := metadata.Migrate(c, dsn); e != nil {
			return errors.New("metadata migration failed; inspect the database with the migration role")
		}
		slog.Info("metadata migrations 1-4 applied/verified")
		return nil
	}
	if len(os.Args) != 1 {
		return errors.New("usage: mcpguard [migrate [--dry-run]]; configuration uses MCPGUARD_* environment variables")
	}
	cfg, e := runtimeconfig.Load()
	if e != nil {
		return e
	}
	v, e := metadata.NewVault(cfg.EncryptionKey, cfg.PlanKey)
	if e != nil {
		return e
	}
	boot, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s, e := metadata.Open(boot, cfg.MetadataDSN, v)
	if e != nil {
		return errors.New("metadata startup check failed; migrate first and use the runtime role")
	}
	defer s.Close()
	var r *nativepg.Reader
	if cfg.LegacyDemo {
		r, e = nativepg.Open(boot, cfg.BusinessDSN)
		if e != nil {
			return errors.New("legacy PG startup check failed; use an isolated demo database and SELECT-only role, or disable legacy demo")
		}
		defer r.Close()
	}
	g := gateway.New(ctx, s, r)
	defer g.Close()
	if cfg.ResourceCatalog {
		catalog, err := resources.NewCatalog(boot, s)
		if err != nil {
			return errors.New("optional resource catalog is not ready; migrate it explicitly first")
		}
		g.AttachResources(catalog)
	}
	native, e := controlled.OpenAdapters(boot, cfg.NativeConfig)
	if e != nil {
		return e
	}
	defer native.Close()
	control, e := controlled.New(s, native, "http://"+cfg.Listen)
	if e != nil {
		return e
	}
	console, e := controlled.OpenConsole(control, cfg.ConsoleConfig)
	if e != nil {
		return e
	}
	notifier, e := controlled.OpenNotifier(control, cfg.NotificationConfig)
	if e != nil {
		return e
	}
	console.Notifications = notifier
	g.AttachControlled(control, console, notifier)
	var fence *resources.RestoreFence
	if cfg.ResourceAccess {
		var err error
		fence, err = restoreFence(boot)
		if err != nil {
			return err
		}
	}
	if cfg.ResourceAccess {
		access, err := resources.NewAccessService(g.Resources)
		if err != nil {
			return errors.New("resource access workflow is not ready; migrate and configure explicitly")
		}
		g.AttachAccess(access, console)
	}
	if cfg.ResourceCredentials {
		keys, err := resources.LoadPackageKeys(cfg.CredentialKeys, cfg.EncryptionKey, cfg.PlanKey)
		if err != nil {
			return errors.New("credential key provider is not ready")
		}
		credentials, err := resources.NewCredentialService(g.Resources, keys)
		if err != nil {
			return errors.New("credential lifecycle is not ready; migrate and configure explicitly")
		}
		credentials.Fence = fence
		g.AttachCredentials(credentials)
		if cfg.ResourceDownloads {
			deliveryStore, err := metadata.Open(boot, cfg.DeliveryDSN, v)
			if err != nil {
				return errors.New("private delivery runtime is unavailable")
			}
			defer deliveryStore.Close()
			catalog, err := resources.NewCatalog(boot, deliveryStore)
			if err != nil {
				return errors.New("private delivery catalog is unavailable")
			}
			issuer, err := resources.NewCredentialService(catalog, keys)
			if err != nil {
				return errors.New("private delivery issuer is unavailable")
			}
			issuer.Fence = fence
			downloads, err := resources.NewDownloadService(issuer, cfg.DownloadTTL, cfg.DownloadMaxReady)
			if err != nil {
				return errors.New("private delivery policy is unavailable")
			}
			g.AttachDownloads(downloads)
			if cfg.ResourceContexts {
				contextStore, err := metadata.Open(boot, cfg.ContextDSN, v)
				if err != nil {
					return errors.New("context runtime is unavailable")
				}
				defer contextStore.Close()
				catalog, err := resources.NewCatalog(boot, contextStore)
				if err != nil {
					return errors.New("context catalog is unavailable")
				}
				credentials, err := resources.NewCredentialService(catalog, keys)
				if err != nil {
					return errors.New("context issuer is unavailable")
				}
				credentials.Fence = fence
				downloads, err := resources.NewDownloadService(credentials, cfg.DownloadTTL, cfg.DownloadMaxReady)
				if err != nil {
					return errors.New("context download runtime is unavailable")
				}
				contexts, err := resources.NewContextService(downloads, cfg.ContextTTL, cfg.ContextLifetime, cfg.ContextMaxActivations, cfg.SyncPageLimit)
				if err != nil {
					return errors.New("context policy is unavailable")
				}
				g.AttachContexts(contexts)
				if cfg.ResourceReads {
					provider, err := resources.LoadFileSecrets(cfg.NativeSecrets)
					if err != nil {
						return errors.New("native secret provider is not ready for planned reads")
					}
					manager := resources.NewRuntimeReadManager(catalog, provider)
					defer manager.Close()
					reads := resources.NewReadService(catalog, credentials, manager)
					reads.Fence = fence
					g.AttachReads(reads)
					if cfg.ResourceWrites {
						writes := resources.NewWriteService(catalog, credentials, manager)
						writes.Fence = fence
						if cfg.HighRiskWrites {
							notifier, err := resources.OpenHighRiskNotifier(contextStore, cfg.HighRiskWeComURL, cfg.HighRiskFeishuURL)
							if err != nil || console == nil || writes.EnableFixtureHighRisk(resources.HighRiskPolicy{
								ResourceID: cfg.HighRiskResourceID, ReviewerID: cfg.HighRiskReviewerID,
								Channel: cfg.HighRiskChannel, CodeTTL: cfg.HighRiskCodeTTL, Notifier: notifier,
							}) != nil {
								return errors.New("high-risk fixture writes require migrated storage, a reviewer, and both local notice receivers")
							}
						}
						g.AttachWrites(writes)
						g.AttachSelfService(console)
					}
				}
			}
		}
	}
	handler := g.Handler()
	if cfg.AuthMode == "oauth-fixture" {
		oauth, err := oauthfixture.Load(cfg.OAuthFixtureConfig, s.OAuthPrincipal)
		if err != nil {
			return err
		}
		handler = g.HandlerWithOptions(gateway.HTTPOptions{AuthMode: cfg.AuthMode, PublicOrigin: oauth.Config.PublicOrigin, ResourceMetadataURL: oauth.MetadataURL(), Verifier: oauth.Verify, Register: oauth.Register})
	}
	ln, e := net.Listen("tcp", cfg.Listen)
	if e != nil {
		return fmt.Errorf("listen failed: %w", e)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	recoveryDone := make(chan struct{})
	go func() { defer close(recoveryDone); g.RunRecovery(ctx) }()
	slog.Info("local test gateway ready", "listen", cfg.Listen, "profile", gateway.Profile, "auth", cfg.AuthMode)
	select {
	case e = <-serveErr:
		stop()
	case <-ctx.Done():
	}
	stop()
	g.Close()
	shutdown, scancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer scancel()
	if se := srv.Shutdown(shutdown); se != nil {
		_ = srv.Close()
		return errors.New("HTTP graceful shutdown timed out")
	}
	<-recoveryDone
	if e != nil && !errors.Is(e, http.ErrServerClosed) {
		return errors.New("HTTP server stopped unexpectedly")
	}
	return nil
}

// restoreFence wires the cross-store recovery fence when the operator enabled
// it. Without the DSN the pre-RA-15 behaviour is preserved; with the DSN set a
// missing provider configuration is a startup error instead of a silent bypass.
func restoreFence(ctx context.Context) (*resources.RestoreFence, error) {
	dsn := os.Getenv("MCPGUARD_RECOVERY_FENCE_DSN")
	if dsn == "" {
		return nil, nil
	}
	path := os.Getenv("MCPGUARD_PROVIDER_CONFIG")
	if path == "" {
		return nil, errors.New("MCPGUARD_RECOVERY_FENCE_DSN requires MCPGUARD_PROVIDER_CONFIG")
	}
	private, err := resources.LoadProviderPrivate(path)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return resources.NewRestoreFence(pool, &resources.PGProvisioningProvider{Private: private}), nil
}
