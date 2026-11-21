package connectors

import (
	"context"
	"errors"
	"testing"
)

func TestEgressRejectsUnapprovedAndLinkLocalDestinations(t *testing.T) {
	policy, err := NewEgressPolicy("127.0.0.0/8, 10.4.0.0/16, 169.254.0.0/16, ::1/128, fe80::/10")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, host := range []string{"127.0.0.1", "::ffff:127.0.0.1", "10.4.20.3", "::1"} {
		ip, err := policy.Resolve(ctx, host)
		if err != nil || len(ip) != 1 || ip[0].Is4In6() {
			t.Fatalf("allowed destination %q: %v (%v)", host, ip, err)
		}
	}
	for _, host := range []string{"10.5.20.3", "169.254.169.254", "::", "fe80::1", "fe80::1%eth0"} {
		if _, err := policy.Resolve(ctx, host); !errors.Is(err, ErrDenied) {
			t.Fatalf("destination %q must be denied, got %v", host, err)
		}
	}
	for _, cidrs := range []string{"", "0.0.0.0/0", "127.0.0.1", "127.0.0.0/8,"} {
		if _, err := NewEgressPolicy(cidrs); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid egress policy %q: %v", cidrs, err)
		}
	}
}
