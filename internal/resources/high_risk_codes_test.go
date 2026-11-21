package resources

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/xielforever/MCPGuard/internal/metadata"
)

func TestHighRiskCodeBindingAndDeadline(t *testing.T) {
	vault, err := metadata.NewVault(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 21, 9, 0, 0, 234567891, time.UTC)
	expiry := now.Add(2 * time.Minute)
	binding := highRiskCodeBinding{OrgID: "org", SubjectID: "requester", ActionID: "action", PlanHash: "plan"}
	record, err := issueHighRiskCode(vault, binding, now, expiry)
	if err != nil {
		t.Fatal(err)
	}
	if !record.ExpiresAt.Equal(expiry.Truncate(time.Microsecond)) {
		t.Fatalf("deadline did not use PostgreSQL precision: %v", record.ExpiresAt)
	}
	// Simulate separate requests after storing and loading a PostgreSQL instant.
	record.ExpiresAt = record.ExpiresAt.In(time.FixedZone("UTC+8", 28800))
	code, err := claimHighRiskCode(vault, binding, record, now)
	if err != nil || code == "" {
		t.Fatal("claim failed", err)
	}
	if err := verifyHighRiskCode(vault, binding, record, record.ExpiresAt.Add(-time.Microsecond), code); err != nil {
		t.Fatal("valid code was rejected", err)
	}
	if err := verifyHighRiskCode(vault, binding, record, record.ExpiresAt, code); !errors.Is(err, errHighRiskCodeExpired) {
		t.Fatalf("verification at expiry = %v", err)
	}
	if _, err := claimHighRiskCode(vault, binding, record, record.ExpiresAt); !errors.Is(err, errHighRiskCodeExpired) {
		t.Fatalf("claim at expiry = %v", err)
	}
	if _, err := issueHighRiskCode(vault, binding, now, now); !errors.Is(err, errHighRiskCodeExpired) {
		t.Fatalf("issued code with elapsed deadline: %v", err)
	}

	for _, test := range []struct {
		name    string
		binding highRiskCodeBinding
	}{
		{"organization", highRiskCodeBinding{OrgID: "other", SubjectID: "requester", ActionID: "action", PlanHash: "plan"}},
		{"requester", highRiskCodeBinding{OrgID: "org", SubjectID: "other", ActionID: "action", PlanHash: "plan"}},
		{"action", highRiskCodeBinding{OrgID: "org", SubjectID: "requester", ActionID: "other", PlanHash: "plan"}},
		{"plan", highRiskCodeBinding{OrgID: "org", SubjectID: "requester", ActionID: "action", PlanHash: "other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := claimHighRiskCode(vault, test.binding, record, now); !errors.Is(err, errHighRiskCodeInvalid) {
				t.Fatalf("claim with wrong %s = %v", test.name, err)
			}
			if err := verifyHighRiskCode(vault, test.binding, record, now, code); !errors.Is(err, errHighRiskCodeInvalid) {
				t.Fatalf("verification with wrong %s = %v", test.name, err)
			}
		})
	}
	modified := record
	modified.ExpiresAt = record.ExpiresAt.Add(time.Hour)
	if err := verifyHighRiskCode(vault, binding, modified, now, code); !errors.Is(err, errHighRiskCodeInvalid) {
		t.Fatalf("verification with modified deadline = %v", err)
	}
	modified = record
	modified.Ciphertext = bytes.Clone(record.Ciphertext)
	modified.Ciphertext[len(modified.Ciphertext)-1] ^= 1
	if _, err := claimHighRiskCode(vault, binding, modified, now); !errors.Is(err, errHighRiskCodeInvalid) {
		t.Fatalf("claim with modified ciphertext = %v", err)
	}
	independent, err := issueHighRiskCode(vault, binding, now, expiry)
	if err != nil {
		t.Fatal(err)
	}
	wrongCode, err := claimHighRiskCode(vault, binding, independent, now)
	if err != nil || independent.ID == record.ID || wrongCode == code {
		t.Fatal("a new challenge was not independent", err)
	}
	if err := verifyHighRiskCode(vault, binding, record, now, wrongCode); !errors.Is(err, errHighRiskCodeInvalid) {
		t.Fatalf("verification of another challenge's code = %v", err)
	}
}
