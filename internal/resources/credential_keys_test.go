package resources

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
)

func exampleReference() v02.CredentialReference {
	return v02.CredentialReference{FormatVersion: "1", CredentialID: "80000000-0000-4000-8000-000000000001", LeaseID: "81000000-0000-4000-8000-000000000001", OrgID: "10000000-0000-4000-8000-000000000001", SubjectID: "20000000-0000-4000-8000-000000000001", ResourceID: "40000000-0000-4000-8000-000000000001", ResourceVersion: "1", GrantID: "71000000-0000-4000-8000-000000000001", GrantVersion: "2", BindingID: "60000000-0000-4000-8000-000000000001", BindingVersion: "1", ProfileID: "50000000-0000-4000-8000-000000000001", ProfileVersion: "1", Issuer: "mcpguard.test", IssuerEpoch: "82000000-0000-4000-8000-000000000001", Audience: "mcpguard-execution", IssuedAt: "2026-10-26T00:00:00Z", ExpiresAt: "2026-10-26T00:05:00Z"}
}
func TestReferenceEnvelopeCryptography(t *testing.T) {
	ctx := context.Background()
	key, _ := packageKey(bytes.Repeat([]byte{3}, 32))
	ref := exampleReference()
	nonce := bytes.Repeat([]byte{7}, 12)
	raw, e := encodePackage(ctx, key, ref, "test.v1", nonce)
	if e != nil {
		t.Fatal(e)
	}
	env, n, encrypted, e := parsePackage(raw)
	if e != nil {
		t.Fatal(e)
	}
	opened, e := openPackage(ctx, key, env, n, encrypted)
	if e != nil || opened != ref {
		t.Fatal("authenticated reference roundtrip failed")
	}
	expected := `["mcpguard-reference","1","mcpguard.test","test.v1","mcpguard-execution"]`
	if string(credentialAAD(env.Issuer, env.KeyID)) != expected {
		t.Fatal("AAD encoding drifted")
	}
	for _, field := range []string{"issuer", "keyId", "nonce", "ciphertext", "authenticationTag", "formatVersion"} {
		t.Run("tampered_"+field, func(t *testing.T) {
			var value map[string]any
			_ = json.Unmarshal(raw, &value)
			if field == "formatVersion" {
				value[field] = "2"
			} else if field == "issuer" {
				value[field] = "other.issuer"
			} else if field == "keyId" {
				value[field] = "other.key"
			} else {
				v := value[field].(string)
				next := "A"
				if v[:1] == next {
					next = "B"
				}
				value[field] = next + v[1:]
			}
			// Re-encode in the declared envelope order, so the test reaches GCM where
			// the altered field still has an otherwise valid wire shape.
			data, _ := json.Marshal(value)
			var changed v02.CredentialEnvelope
			_ = json.Unmarshal(data, &changed)
			data, _ = json.Marshal(changed)
			header, n, c, e := parsePackage(data)
			if e == nil {
				_, e = openPackage(ctx, key, header, n, c)
			}
			if !errors.Is(e, ErrCredential) {
				t.Fatal("tampering was not uniformly rejected")
			}
		})
	}
	t.Run("wrong_key", func(t *testing.T) {
		wrong, _ := packageKey(bytes.Repeat([]byte{4}, 32))
		if _, e := openPackage(ctx, wrong, env, n, encrypted); !errors.Is(e, ErrCredential) {
			t.Fatal("wrong key accepted")
		}
	})
	t.Run("ambiguous_and_noncanonical_encoding", func(t *testing.T) {
		for _, data := range [][]byte{append(append([]byte{}, raw...), byte('\n')), append(append([]byte{}, raw...), []byte(" {}")...), bytes.Replace(raw, []byte(`"formatVersion":"1"`), []byte(`"formatVersion":"1","formatVersion":"1"`), 1), bytes.Replace(raw, []byte(`"keyId":"test.v1"`), []byte(`"keyId":"test.v1","algorithm":"AES-CBC"`), 1), bytes.Replace(raw, []byte(env.AuthenticationTag), []byte(env.AuthenticationTag+"="), 1)} {
			if _, _, _, e := parsePackage(data); !errors.Is(e, ErrCredential) {
				t.Fatal("ambiguous envelope accepted")
			}
		}
		alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
		index := strings.IndexByte(alphabet, env.AuthenticationTag[len(env.AuthenticationTag)-1])
		changed := env
		changed.AuthenticationTag = changed.AuthenticationTag[:21] + string(alphabet[index|1])
		data, _ := json.Marshal(changed)
		if _, _, _, e := parsePackage(data); !errors.Is(e, ErrCredential) {
			t.Fatal("noncanonical base64 trailing bits accepted")
		}
	})
	t.Run("forbidden_plaintext_fields", func(t *testing.T) {
		for _, field := range []string{"password", "dsn", "secretRef", "nativeIdentity", "script"} {
			plain, _ := json.Marshal(ref)
			var payload map[string]any
			_ = json.Unmarshal(plain, &payload)
			payload[field] = "not allowed"
			plain, _ = json.Marshal(payload)
			c, _ := key.Seal(ctx, nonce, plain, credentialAAD(ref.Issuer, "test.v1"))
			if _, e := openPackage(ctx, key, env, nonce, c); !errors.Is(e, ErrCredential) {
				t.Fatal("native or executable payload field accepted")
			}
		}
	})
}
func TestPackageKeyProviderBoundaries(t *testing.T) {
	forbiddenA := bytes.Repeat([]byte{1}, 32)
	forbiddenB := bytes.Repeat([]byte{2}, 32)
	path := filepath.Join(t.TempDir(), "keys.json")
	write := func(key []byte) {
		data, _ := json.Marshal(map[string]any{"schemaVersion": "0.2", "keys": []map[string]string{{"orgId": exampleReference().OrgID, "issuer": "mcpguard.test", "keyId": "test.v1", "key": base64.StdEncoding.EncodeToString(key)}}})
		if os.WriteFile(path, data, 0600) != nil {
			t.Fatal("private key setup failed")
		}
	}
	write(bytes.Repeat([]byte{3}, 32))
	if _, e := LoadPackageKeys(path, forbiddenA, forbiddenB); e != nil {
		t.Fatal(e)
	}
	for _, key := range [][]byte{forbiddenA, forbiddenB} {
		write(key)
		if _, e := LoadPackageKeys(path, forbiddenA, forbiddenB); !errors.Is(e, ErrDenied) {
			t.Fatal("package key reused storage/plan key")
		}
	}
	write(bytes.Repeat([]byte{3}, 32))
	_ = os.Chmod(path, 0644)
	if _, e := LoadPackageKeys(path, forbiddenA, forbiddenB); e == nil {
		t.Fatal("world-readable keys accepted")
	}
	_ = os.Chmod(path, 0600)
	link := filepath.Join(filepath.Dir(path), "link.json")
	if os.Symlink(path, link) != nil {
		t.Fatal("symlink fixture failed")
	}
	if _, e := LoadPackageKeys(link, forbiddenA, forbiddenB); e == nil {
		t.Fatal("symlink key file accepted")
	}
	raw, _ := os.ReadFile(path)
	raw = bytes.Replace(raw, []byte(`"schemaVersion":"0.2"`), []byte(`"schemaVersion":"0.2","keyUrl":"https://keys.invalid"`), 1)
	_ = os.WriteFile(path, raw, 0600)
	if _, e := LoadPackageKeys(path, forbiddenA, forbiddenB); e == nil {
		t.Fatal("external key selector accepted")
	}
}
