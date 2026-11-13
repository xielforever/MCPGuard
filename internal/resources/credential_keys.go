package resources

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
)

var ErrCredential = errors.New("credential is not usable")

// PackageKey is a server-side cryptographic handle, never a client credential.
// The nonce must have been reserved durably before Seal is called.
type PackageKey interface {
	Fingerprint() string
	Seal(context.Context, []byte, []byte, []byte) ([]byte, error)
	Open(context.Context, []byte, []byte, []byte) ([]byte, error)
}
type PackageKeyProvider interface {
	Resolve(context.Context, string, string, string) (PackageKey, error)
}
type filePackageKeys struct{ keys map[string]PackageKey }
type localPackageKey struct {
	aead        cipher.AEAD
	fingerprint string
}

func (k *localPackageKey) Fingerprint() string { return k.fingerprint }
func (k *localPackageKey) Seal(ctx context.Context, nonce, data, aad []byte) ([]byte, error) {
	if ctx.Err() != nil || len(nonce) != v02.CredentialNonceBytes {
		return nil, ErrUnavailable
	}
	return k.aead.Seal(nil, nonce, data, aad), nil
}
func (k *localPackageKey) Open(ctx context.Context, nonce, data, aad []byte) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	if len(nonce) != v02.CredentialNonceBytes {
		return nil, ErrCredential
	}
	plain, e := k.aead.Open(nil, nonce, data, aad)
	if e != nil {
		return nil, ErrCredential
	}
	return plain, nil
}
func (f *filePackageKeys) Resolve(ctx context.Context, org, issuer, keyID string) (PackageKey, error) {
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	key, ok := f.keys[org+"/"+issuer+"/"+keyID]
	if !ok {
		return nil, ErrCredential
	}
	return key, nil
}
func packageKey(raw []byte) (PackageKey, error) {
	if len(raw) != 32 {
		return nil, ErrInput
	}
	block, e := aes.NewCipher(raw)
	if e != nil {
		return nil, ErrInput
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, ErrUnavailable
	}
	fp := sha256.Sum256(raw)
	return &localPackageKey{aead, hex.EncodeToString(fp[:])}, nil
}

// LoadPackageKeys is the isolated local implementation. Production KMS
// integration remains separate; unknown URLs/algorithms are never accepted.
func LoadPackageKeys(path string, forbidden ...[]byte) (PackageKeyProvider, error) {
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > v02.MaxInputBytes {
		return nil, ErrUnavailable
	}
	if len(forbidden) < 2 {
		return nil, ErrInput
	}
	raw, e := os.ReadFile(path)
	if e != nil || v02.CheckJSON(raw) != nil {
		return nil, ErrInput
	}
	var cfg struct {
		SchemaVersion string `json:"schemaVersion"`
		Keys          []struct {
			OrgID  string `json:"orgId"`
			Issuer string `json:"issuer"`
			KeyID  string `json:"keyId"`
			Key    string `json:"key"`
		} `json:"keys"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || cfg.SchemaVersion != "0.2" || len(cfg.Keys) == 0 || len(cfg.Keys) > 100 {
		return nil, ErrInput
	}
	f := &filePackageKeys{map[string]PackageKey{}}
	seen := map[string]bool{}
	for _, item := range cfg.Keys {
		decoded, e := base64.StdEncoding.Strict().DecodeString(item.Key)
		if e != nil || len(decoded) != 32 || base64.StdEncoding.EncodeToString(decoded) != item.Key {
			return nil, ErrInput
		}
		for _, excluded := range forbidden {
			if len(excluded) != 32 {
				return nil, ErrInput
			}
			if hmac.Equal(decoded, excluded) {
				return nil, ErrDenied
			}
		}
		key, e := packageKey(decoded)
		if e != nil {
			return nil, e
		}
		test := v02.CredentialIssuer{OrgID: item.OrgID, Issuer: item.Issuer, IssuerEpoch: "10000000-0000-4000-8000-000000000001", KeyID: item.KeyID, KeyFingerprint: key.Fingerprint(), Version: "1", Enabled: true}
		if !valid("CredentialIssuer", test) {
			return nil, ErrInput
		}
		id := item.OrgID + "/" + item.Issuer + "/" + item.KeyID
		if f.keys[id] != nil || seen[key.Fingerprint()] {
			return nil, ErrConflict
		}
		f.keys[id] = key
		seen[key.Fingerprint()] = true
	}
	return f, nil
}

func credentialAAD(issuer, keyID string) []byte {
	raw, _ := json.Marshal([]string{"mcpguard-reference", "1", issuer, keyID, "mcpguard-execution"})
	return raw
}
func encodePackage(ctx context.Context, key PackageKey, ref v02.CredentialReference, keyID string, nonce []byte) ([]byte, error) {
	if !valid("CredentialReference", ref) || len(nonce) != v02.CredentialNonceBytes {
		return nil, ErrInput
	}
	plain, _ := json.Marshal(ref)
	if len(plain) > v02.MaxCredentialReferenceBytes {
		return nil, ErrInput
	}
	encrypted, e := key.Seal(ctx, nonce, plain, credentialAAD(ref.Issuer, keyID))
	if e != nil {
		return nil, e
	}
	if len(encrypted) < v02.CredentialTagBytes {
		return nil, ErrUnavailable
	}
	cut := len(encrypted) - v02.CredentialTagBytes
	env := v02.CredentialEnvelope{FormatVersion: "1", Issuer: ref.Issuer, KeyID: keyID, Nonce: base64.RawURLEncoding.EncodeToString(nonce), Ciphertext: base64.RawURLEncoding.EncodeToString(encrypted[:cut]), AuthenticationTag: base64.RawURLEncoding.EncodeToString(encrypted[cut:])}
	raw, _ := json.Marshal(env)
	if len(raw) > v02.MaxPackageBytes || !valid("CredentialEnvelope", env) {
		return nil, ErrUnavailable
	}
	return raw, nil
}
func parsePackage(raw []byte) (v02.CredentialEnvelope, []byte, []byte, error) {
	var env v02.CredentialEnvelope
	if len(raw) > v02.MaxPackageBytes || v02.Decode("CredentialEnvelope", raw, &env) != nil {
		return env, nil, nil, ErrCredential
	}
	canonical, _ := json.Marshal(env)
	if !bytes.Equal(canonical, raw) {
		return env, nil, nil, ErrCredential
	}
	nonce, e := base64.RawURLEncoding.Strict().DecodeString(env.Nonce)
	if e != nil || len(nonce) != v02.CredentialNonceBytes {
		return env, nil, nil, ErrCredential
	}
	ciphertext, e := base64.RawURLEncoding.Strict().DecodeString(env.Ciphertext)
	if e != nil {
		return env, nil, nil, ErrCredential
	}
	tag, e := base64.RawURLEncoding.Strict().DecodeString(env.AuthenticationTag)
	if e != nil || len(tag) != v02.CredentialTagBytes {
		return env, nil, nil, ErrCredential
	}
	return env, nonce, append(ciphertext, tag...), nil
}
func openPackage(ctx context.Context, key PackageKey, env v02.CredentialEnvelope, nonce, encrypted []byte) (v02.CredentialReference, error) {
	var ref v02.CredentialReference
	raw, e := key.Open(ctx, nonce, encrypted, credentialAAD(env.Issuer, env.KeyID))
	if e != nil {
		return ref, e
	}
	if len(raw) > v02.MaxCredentialReferenceBytes || v02.Decode("CredentialReference", raw, &ref) != nil || ref.Issuer != env.Issuer {
		return v02.CredentialReference{}, ErrCredential
	}
	canonical, _ := json.Marshal(ref)
	if !bytes.Equal(canonical, raw) {
		return v02.CredentialReference{}, ErrCredential
	}
	return ref, nil
}
