// Server-side credential operations. Never configure this administrative
// binary as a coding-client connector: it requires private server keys.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/metadata"
	"github.com/xielforever/MCPGuard/internal/privatefile"
	"github.com/xielforever/MCPGuard/internal/resources"
)

func run() error {
	if len(os.Args) < 3 {
		return resources.ErrInput
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	storage, e := base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_ENCRYPTION_KEY"))
	if e != nil {
		return resources.ErrInput
	}
	plan, e := base64.StdEncoding.DecodeString(os.Getenv("MCPGUARD_PLAN_KEY"))
	if e != nil {
		return resources.ErrInput
	}
	vault, e := metadata.NewVault(storage, plan)
	if e != nil {
		return resources.ErrInput
	}
	store, e := metadata.Open(ctx, os.Getenv("MCPGUARD_METADATA_DSN"), vault)
	if e != nil {
		return resources.ErrUnavailable
	}
	defer store.Close()
	p, e := store.VerifyToken(ctx, os.Getenv("MCPGUARD_TOKEN"))
	if e != nil {
		return resources.ErrDenied
	}
	catalog, e := resources.NewCatalog(ctx, store)
	if e != nil {
		return e
	}
	keys, e := resources.LoadPackageKeys(os.Getenv("MCPGUARD_CREDENTIAL_KEYS"), storage, plan)
	if e != nil {
		return resources.ErrUnavailable
	}
	service, e := resources.NewCredentialService(catalog, keys)
	if e != nil {
		return e
	}
	var out v02.CredentialStatus
	switch os.Args[1] {
	case "enroll-client":
		if len(os.Args) != 4 {
			return resources.ErrInput
		}
		ttl, err := strconv.Atoi(os.Args[2])
		if err != nil {
			return resources.ErrInput
		}
		dir, name := filepath.Dir(os.Args[3]), filepath.Base(os.Args[3])
		if privatefile.CheckNew(dir, name) != nil {
			return resources.ErrInput
		}
		downloads, err := resources.NewDownloadService(service, 1, 1)
		if err != nil {
			return err
		}
		contexts, err := resources.NewContextService(downloads, 1, 1, 1, 1)
		if err != nil {
			return err
		}
		client, info, token, err := contexts.EnrollClient(ctx, p, ttl)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(struct {
			SchemaVersion string                        `json:"schemaVersion"`
			Authorization v02.DeliveryAuthorizationInfo `json:"authorization"`
			Token         string                        `json:"deliveryToken"`
			Client        v02.ClientRegistration        `json:"client"`
		}{"0.2", info, token, client})
		if err != nil {
			return resources.ErrUnavailable
		}
		if privatefile.WriteNew(dir, name, raw) != nil {
			return resources.ErrUnavailable
		}
		return json.NewEncoder(os.Stdout).Encode(client)
	case "enroll-delivery":
		if len(os.Args) != 4 {
			return resources.ErrInput
		}
		ttl, err := strconv.Atoi(os.Args[2])
		if err != nil {
			return resources.ErrInput
		}
		dir, name := filepath.Dir(os.Args[3]), filepath.Base(os.Args[3])
		if privatefile.CheckNew(dir, name) != nil {
			return resources.ErrInput
		}
		downloads, err := resources.NewDownloadService(service, 1, 1)
		if err != nil {
			return err
		}
		info, token, err := downloads.EnrollDelivery(ctx, p, ttl)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(struct {
			SchemaVersion string                        `json:"schemaVersion"`
			Authorization v02.DeliveryAuthorizationInfo `json:"authorization"`
			Token         string                        `json:"deliveryToken"`
		}{"0.2", info, token})
		if err != nil {
			return resources.ErrUnavailable
		}
		if privatefile.WriteNew(dir, name, raw) != nil {
			return resources.ErrUnavailable
		}
		return json.NewEncoder(os.Stdout).Encode(info)
	case "issue":
		if len(os.Args) != 4 {
			return resources.ErrInput
		}
		out, e = service.Issue(ctx, p, v02.IssueCredentialInput{GrantID: os.Args[2], RequestKey: os.Args[3]})
	case "status":
		if len(os.Args) != 3 {
			return resources.ErrInput
		}
		out, e = service.Status(ctx, p, v02.CredentialIDInput{CredentialID: os.Args[2]})
	case "check":
		if len(os.Args) != 5 {
			return resources.ErrInput
		}
		info, err := os.Lstat(os.Args[4])
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > v02.MaxPackageBytes {
			return resources.ErrCredential
		}
		raw, err := os.ReadFile(os.Args[4])
		if err != nil {
			return resources.ErrCredential
		}
		out, e = service.Validate(ctx, p, os.Args[2], os.Args[3], raw)
	case "revoke":
		if len(os.Args) != 6 {
			return resources.ErrInput
		}
		out, e = service.Revoke(ctx, p, v02.RevokeCredentialInput{CredentialID: os.Args[2], ExpectedRevision: os.Args[3], RequestKey: os.Args[4], Reason: os.Args[5]})
	default:
		return resources.ErrInput
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "Credential operation could not be completed. No plaintext reference, key or native secret is returned.")
		os.Exit(1)
	}
}
