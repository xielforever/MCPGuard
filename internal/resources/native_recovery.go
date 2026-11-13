package resources

import (
	"bytes"
	"context"
	"encoding/json"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
)

// NativeReceipt never contains an endpoint, password or new CREATE authority.
type NativeReceipt struct {
	Status    string  `json:"status"`
	TargetID  string  `json:"targetId"`
	AccountID string  `json:"accountId"`
	PlanHash  string  `json:"planHash"`
	RoleOID   *string `json:"roleOid,omitempty"`
}
type NativeRecoveryProvider interface {
	Reconcile(context.Context, v02.ProvisioningPlan, string) (NativeReceipt, error)
	Cleanup(context.Context, v02.ProvisioningPlan, string) (NativeReceipt, error)
}

func (p *PGProvisioningProvider) Reconcile(ctx context.Context, plan v02.ProvisioningPlan, hash string) (NativeReceipt, error) {
	return p.nativeOutcome(ctx, plan, hash, false)
}
func (p *PGProvisioningProvider) Cleanup(ctx context.Context, plan v02.ProvisioningPlan, hash string) (NativeReceipt, error) {
	return p.nativeOutcome(ctx, plan, hash, true)
}
func (p *PGProvisioningProvider) nativeOutcome(ctx context.Context, plan v02.ProvisioningPlan, hash string, cleanup bool) (NativeReceipt, error) {
	var out NativeReceipt
	ep, ok := p.Private.Endpoints[plan.EndpointConfigRef]
	if !ok || !safeProviderEndpoint(ep) || ep.Fingerprint() != plan.TargetFingerprint || plan.ManagementMode != "PROVISIONED" {
		return out, ErrDenied
	}
	c, e := p.manager(ctx, plan.EndpointConfigRef)
	if e != nil {
		return out, e
	}
	defer c.Close(context.Background())
	expiry, _, e := normalizedTime(plan.ExpiresAt)
	if e != nil {
		return out, e
	}
	statement := "SELECT mcpguard_provision.reconcile_account($1,$2,$3,$4)"
	if cleanup {
		statement = "SELECT mcpguard_provision.cleanup_account($1,$2,$3,$4)"
	}
	var raw []byte
	if e = c.QueryRow(ctx, statement, plan.TargetID, plan.AccountID, hash, expiry).Scan(&raw); e != nil {
		return out, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || out.TargetID != plan.TargetID || out.AccountID != plan.AccountID || out.PlanHash != hash {
		return NativeReceipt{}, ErrDenied
	}
	switch out.Status {
	case "OWNED", "DISABLED", "ABSENT_SEALED", "CONFLICT", "CLEANED":
	default:
		return NativeReceipt{}, ErrDenied
	}
	if out.Status == "OWNED" && (out.RoleOID == nil || number(*out.RoleOID) <= 0) {
		return NativeReceipt{}, ErrDenied
	}
	return out, nil
}
