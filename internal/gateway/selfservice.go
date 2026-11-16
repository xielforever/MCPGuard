package gateway

import (
	"context"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xielforever/MCPGuard/internal/controlled"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/resources"
)

// AttachSelfService publishes the v0.2 self-service console API (resources,
// credentials, revocations and write approvals) behind the existing browser
// session and CSRF checks. Revocation needs an explicitly configured privileged
// DSN; without it the page reports the service as unavailable instead of
// silently degrading.
func (g *Gateway) AttachSelfService(console *controlled.Console) {
	if console == nil || g.Resources == nil {
		return
	}
	service := &resources.SelfServiceConsole{Catalog: g.Resources, Access: g.Access, Writes: g.Writes}
	if dsn := os.Getenv("MCPGUARD_CONSOLE_REVOCATION_DSN"); dsn != "" {
		if privatePath := os.Getenv("MCPGUARD_PROVIDER_CONFIG"); privatePath != "" {
			if private, err := resources.LoadProviderPrivate(privatePath); err == nil {
				if pool, err := pgxpool.New(context.Background(), dsn); err == nil {
					revoker := resources.NewRevocationService(pool, g.Resources.Store.Vault)
					revoker.Fence = resources.NewRestoreFence(pool, &resources.PGProvisioningProvider{Private: private})
					service.Revoker = revoker
				}
			}
		}
	}
	previous := console.APIExtension
	console.APIExtension = func(w http.ResponseWriter, r *http.Request, p metadata.Principal) bool {
		if service.ServeHTTP(w, r, p) {
			return true
		}
		if previous != nil {
			return previous(w, r, p)
		}
		return false
	}
}
