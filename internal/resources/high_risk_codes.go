package resources

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

const highRiskCodeBytes = 16

var (
	errHighRiskCodeExpired = errors.New("authorization code expired")
	errHighRiskCodeInvalid = errors.New("authorization code invalid")
)

type highRiskCodeBinding struct {
	OrgID, SubjectID, ActionID, PlanHash string
}

// highRiskCodeRecord contains only the values needed to persist a challenge.
// The owning service manages authorization, claim eligibility, and consumption.
type highRiskCodeRecord struct {
	ID         string
	Digest     string
	Ciphertext []byte
	ExpiresAt  time.Time
}

func issueHighRiskCode(vault *metadata.Vault, binding highRiskCodeBinding, now, expiresAt time.Time) (highRiskCodeRecord, error) {
	if vault == nil {
		return highRiskCodeRecord{}, ErrUnavailable
	}
	// PostgreSQL stores timestamps at microsecond precision. Bind that exact
	// deadline so encryption and verification survive a persistence round trip.
	record := highRiskCodeRecord{ExpiresAt: expiresAt.UTC().Truncate(time.Microsecond)}
	id, err := uuid.NewRandom()
	if err != nil {
		return highRiskCodeRecord{}, ErrUnavailable
	}
	record.ID = id.String()
	aad, err := record.associatedData(binding, now)
	if err != nil {
		return highRiskCodeRecord{}, err
	}

	var random [highRiskCodeBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return highRiskCodeRecord{}, ErrUnavailable
	}
	defer clear(random[:])
	code := make([]byte, base64.RawURLEncoding.EncodedLen(len(random)))
	defer clear(code)
	base64.RawURLEncoding.Encode(code, random[:])
	record.Digest = highRiskCodeDigest(vault, aad, code)
	record.Ciphertext = vault.Seal(code, aad)
	return record, nil
}

// claimHighRiskCode recovers the code for the service's authenticated claim
// response. The caller must verify identity and claim eligibility first.
func claimHighRiskCode(vault *metadata.Vault, binding highRiskCodeBinding, record highRiskCodeRecord, now time.Time) (string, error) {
	if vault == nil {
		return "", ErrUnavailable
	}
	aad, err := record.associatedData(binding, now)
	if err != nil {
		return "", err
	}
	code, err := vault.Open(record.Ciphertext, aad)
	if err != nil {
		return "", errHighRiskCodeInvalid
	}
	defer clear(code)
	if !hmac.Equal([]byte(record.Digest), []byte(highRiskCodeDigest(vault, aad, code))) {
		return "", errHighRiskCodeInvalid
	}
	return string(code), nil
}

// verifyHighRiskCode checks the submitted code without decrypting persisted
// data. Verification itself never changes the challenge state.
func verifyHighRiskCode(vault *metadata.Vault, binding highRiskCodeBinding, record highRiskCodeRecord, now time.Time, candidate string) error {
	if vault == nil {
		return ErrUnavailable
	}
	aad, err := record.associatedData(binding, now)
	if err != nil {
		return err
	}
	if len(candidate) != base64.RawURLEncoding.EncodedLen(highRiskCodeBytes) ||
		!hmac.Equal([]byte(record.Digest), []byte(highRiskCodeDigest(vault, aad, []byte(candidate)))) {
		return errHighRiskCodeInvalid
	}
	return nil
}

func (record highRiskCodeRecord) associatedData(binding highRiskCodeBinding, now time.Time) (string, error) {
	if binding.OrgID == "" || binding.SubjectID == "" || binding.ActionID == "" || binding.PlanHash == "" ||
		record.ID == "" || record.ExpiresAt.IsZero() || now.IsZero() {
		return "", errHighRiskCodeInvalid
	}
	if !now.Before(record.ExpiresAt) {
		return "", errHighRiskCodeExpired
	}
	aad, _ := json.Marshal([7]string{
		"mcpguard/high-risk-code/v1", binding.OrgID, binding.SubjectID, binding.ActionID,
		binding.PlanHash, record.ID, record.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
	return string(aad), nil
}

func highRiskCodeDigest(vault *metadata.Vault, aad string, code []byte) string {
	data := make([]byte, 0, len(aad)+1+len(code))
	data = append(data, aad...)
	data = append(data, 0)
	data = append(data, code...)
	defer clear(data)
	return vault.Hash(data)
}
