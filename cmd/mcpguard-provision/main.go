// A separate, explicitly configured native-account management process. Its
// credentials are never loaded into MCP or browser approval handlers.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/resources"
	"os"
	"time"
)

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if len(os.Args) >= 2 && os.Args[1] == "install-provider" {
		if len(os.Args) != 3 {
			return resources.ErrInput
		}
		raw, e := os.ReadFile(os.Args[2])
		if e != nil {
			return resources.ErrInput
		}
		var config v02.ProvisioningConfiguration
		if v02.Decode("ProvisioningConfiguration", raw, &config) != nil {
			return resources.ErrInput
		}
		return resources.InstallPGProvider(ctx, os.Getenv("MCPGUARD_PROVIDER_ADMIN_DSN"), os.Getenv("MCPGUARD_PROVIDER_MANAGER_ROLE"), config.Targets)
	}
	if len(os.Args) != 2 && len(os.Args) != 4 {
		return resources.ErrInput
	}
	key, e := base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_ENCRYPTION_KEY"))
	if e != nil {
		return resources.ErrInput
	}
	plan, e := base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_PLAN_KEY"))
	if e != nil {
		return resources.ErrInput
	}
	vault, e := metadata.NewVault(key, plan)
	if e != nil {
		return resources.ErrInput
	}
	store, e := metadata.Open(ctx, os.Getenv("MCPGUARD_METADATA_DSN"), vault)
	if e != nil {
		return resources.ErrUnavailable
	}
	defer store.Close()
	catalog, e := resources.NewCatalog(ctx, store)
	if e != nil {
		return e
	}
	private, e := resources.LoadProviderPrivate(os.Getenv("MCPGUARD_PROVIDER_CONFIG"))
	if e != nil {
		return e
	}
	worker, e := resources.NewProvisioner(catalog, private, &resources.PGProvisioningProvider{Private: private})
	if e != nil {
		return e
	}
	switch os.Args[1] {
	case "recover":
		if len(os.Args) != 4 {
			return resources.ErrInput
		}
		job, e := worker.Recover(ctx, os.Args[2], os.Args[3])
		if e != nil {
			return e
		}
		_ = json.NewEncoder(os.Stdout).Encode(job)
		if job.State != "VERIFIED" && job.State != "CLEANED" && job.State != "FAILED" {
			return resources.ErrUnavailable
		}
		return nil
	case "recover-once":
		if len(os.Args) != 2 {
			return resources.ErrInput
		}
		jobs, e := worker.RecoverOnce(ctx)
		if e != nil {
			return e
		}
		_ = json.NewEncoder(os.Stdout).Encode(jobs)
		for _, job := range jobs {
			if job.State != "VERIFIED" && job.State != "CLEANED" && job.State != "FAILED" {
				return resources.ErrUnavailable
			}
		}
		return nil
	case "run":
		if len(os.Args) != 4 {
			return resources.ErrInput
		}
		job, e := worker.Run(ctx, os.Args[2], os.Args[3])
		if e != nil {
			return e
		}
		_ = json.NewEncoder(os.Stdout).Encode(job)
		if job.State != "VERIFIED" {
			return resources.ErrUnavailable
		}
		return nil
	case "run-once":
		if len(os.Args) != 2 {
			return resources.ErrInput
		}
		jobs, e := worker.RunOnce(ctx)
		if e != nil {
			return e
		}
		_ = json.NewEncoder(os.Stdout).Encode(jobs)
		for _, j := range jobs {
			if j.State != "VERIFIED" {
				return resources.ErrUnavailable
			}
		}
		return nil
	}
	return resources.ErrInput
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "Provisioning or recovery still requires attention. Inspect the private job state; do not replay unknown management effects.")
		os.Exit(1)
	}
}
