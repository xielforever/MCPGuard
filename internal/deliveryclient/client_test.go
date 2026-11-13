package deliveryclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/privatefile"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testDownloadID = "83000000-0000-4000-8000-000000000001"

func clientFixture(t *testing.T, mode string) (*Client, string, *atomic.Int32, []byte) {
	t.Helper()
	dir := t.TempDir()
	authDir := t.TempDir()
	if os.Chmod(dir, 0700) != nil || os.Chmod(authDir, 0700) != nil {
		t.Fatal("private directory setup failed")
	}
	proof := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	expiry := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	auth := map[string]any{"schemaVersion": "0.2", "authorization": v02.DeliveryAuthorizationInfo{AuthorizationID: "84000000-0000-4000-8000-000000000001", OwnerID: "20000000-0000-4000-8000-000000000001", ExpiresAt: expiry, Audience: "mcpguard-delivery", Scope: "credential:claim"}, "deliveryToken": proof}
	encoded, _ := json.Marshal(auth)
	path := filepath.Join(authDir, "authorization.json")
	if privatefile.WriteNew(authDir, "authorization.json", encoded) != nil {
		t.Fatal("private authorization setup failed")
	}
	env := v02.CredentialEnvelope{FormatVersion: "1", Issuer: "mcpguard.test", KeyID: "reference.v1", Nonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 12)), Ciphertext: base64.RawURLEncoding.EncodeToString([]byte("opaque encrypted bytes")), AuthenticationTag: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 16))}
	blob, _ := json.Marshal(env)
	ticket := v02.DownloadTicket{DownloadID: testDownloadID, CredentialID: "80000000-0000-4000-8000-000000000001", LeaseID: "81000000-0000-4000-8000-000000000001", OwnerID: "20000000-0000-4000-8000-000000000001", ExpiresAt: expiry, CredentialExpiresAt: expiry, State: "READY", Revision: "1", DeliveryMode: "MANUAL_OWNER", DeliveryOutcome: "NOT_STARTED"}
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-MCPGuard-Delivery-Token") != proof || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("c", 32) {
			t.Error("private authorization missing")
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ticket)
			return
		}
		calls.Add(1)
		name := "mcpguard-" + ticket.CredentialID + ".mgcred"
		data := append([]byte{}, blob...)
		if mode == "redirect" {
			w.Header().Set("Location", "http://127.0.0.1:1/private")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		if mode == "unknown_field" {
			data = append(data[:len(data)-1], []byte(`,"command":"arbitrary"}`)...)
		}
		if mode == "oversized" {
			data = bytes.Repeat([]byte{'x'}, v02.MaxPackageBytes+1)
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		if mode == "bad_digest" {
			digest = strings.Repeat("0", 64)
		}
		if mode == "unexpected_filename" {
			name = "../../outside.mgcred"
		}
		headers := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nCache-Control: no-store\r\nContent-Disposition: attachment; filename=\"%s\"\r\nContent-Length: %d\r\nX-MCPGuard-Package-SHA256: %s\r\n\r\n", name, len(data), digest)
		if mode == "truncated" {
			conn, buf, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			_, _ = io.WriteString(buf, headers)
			_, _ = buf.Write(data[:len(data)/2])
			_ = buf.Flush()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Header().Set("X-MCPGuard-Package-SHA256", digest)
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	client, e := New(server.URL, strings.Repeat("c", 32), path, true)
	if e != nil {
		t.Fatal(e)
	}
	return client, dir, calls, blob
}
func TestPrivateDeliveryClient(t *testing.T) {
	t.Run("save_complete_response_with_private_atomic_file", func(t *testing.T) {
		client, dir, calls, blob := clientFixture(t, "")
		receipt, e := client.Claim(context.Background(), testDownloadID, dir)
		if e != nil || receipt.State != "SAVED" || calls.Load() != 1 {
			t.Fatal("valid download failed")
		}
		raw, e := privatefile.Read(filepath.Join(dir, receipt.File), v02.MaxPackageBytes)
		if e != nil || !bytes.Equal(raw, blob) {
			t.Fatal("saved file mismatch")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatal("temporary file was exposed")
		}
		public, _ := json.Marshal(receipt)
		if bytes.Contains(public, blob) || bytes.Contains(public, []byte(client.proof)) {
			t.Fatal("receipt leaked private content")
		}
	})
	for _, mode := range []string{"truncated", "oversized", "bad_digest", "unexpected_filename", "redirect", "unknown_field"} {
		t.Run(mode, func(t *testing.T) {
			client, dir, calls, _ := clientFixture(t, mode)
			if _, e := client.Claim(context.Background(), testDownloadID, dir); e == nil {
				t.Fatal("unsafe response accepted")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 || calls.Load() != 1 {
				t.Fatal("partial file published or claim auto-retried")
			}
		})
	}
	t.Run("existing_private_file_not_overwritten_or_claimed", func(t *testing.T) {
		client, dir, calls, _ := clientFixture(t, "")
		name := "mcpguard-80000000-0000-4000-8000-000000000001.mgcred"
		if privatefile.WriteNew(dir, name, []byte("existing")) != nil {
			t.Fatal("setup failed")
		}
		if _, e := client.Claim(context.Background(), testDownloadID, dir); e == nil || calls.Load() != 0 {
			t.Fatal("existing file was claimed or replaced")
		}
		raw, _ := os.ReadFile(filepath.Join(dir, name))
		if string(raw) != "existing" {
			t.Fatal("existing file changed")
		}
	})
	t.Run("nonprivate_destination_rejected_before_claim", func(t *testing.T) {
		client, dir, calls, _ := clientFixture(t, "")
		_ = os.Chmod(dir, 0755)
		if _, e := client.Claim(context.Background(), testDownloadID, dir); e == nil || calls.Load() != 0 {
			t.Fatal("public destination accepted")
		}
	})
	t.Run("symlink_destination_rejected", func(t *testing.T) {
		client, dir, calls, _ := clientFixture(t, "")
		link := filepath.Join(t.TempDir(), "link")
		if os.Symlink(dir, link) != nil {
			t.Fatal("setup failed")
		}
		if _, e := client.Claim(context.Background(), testDownloadID, link); e == nil || calls.Load() != 0 {
			t.Fatal("symlink destination used")
		}
	})
	t.Run("git_working_tree_destination_rejected", func(t *testing.T) {
		client, dir, calls, _ := clientFixture(t, "")
		if os.Mkdir(filepath.Join(dir, ".git"), 0700) != nil {
			t.Fatal("setup failed")
		}
		if _, e := client.Claim(context.Background(), testDownloadID, dir); e == nil || calls.Load() != 0 {
			t.Fatal("repository credential storage accepted")
		}
	})
}

func TestDeliveryClientConfiguration(t *testing.T) {
	client, _, _, _ := clientFixture(t, "")
	_ = client
	dir := t.TempDir()
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("private directory setup failed")
	}
	path := filepath.Join(dir, "auth.json")
	raw := []byte(`{"schemaVersion":"0.2","deliveryToken":"private"}`)
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("setup failed")
	}
	for _, origin := range []string{"http://127.0.0.1:8080", "http://example.invalid", "https://example.invalid?token=secret", "https://user:secret@example.invalid", "https://example.invalid/path"} {
		if _, e := New(origin, strings.Repeat("c", 32), path, false); e == nil {
			t.Fatal("invalid origin accepted")
		}
	}
	_ = os.Chmod(path, 0644)
	if _, e := New("https://example.invalid", strings.Repeat("c", 32), path, false); e == nil {
		t.Fatal("public authorization file accepted")
	}
	_ = os.Chmod(path, 0600)
	link := filepath.Join(dir, "auth-link.json")
	if os.Symlink(path, link) != nil {
		t.Fatal("setup failed")
	}
	if _, e := New("https://example.invalid", strings.Repeat("c", 32), link, false); e == nil {
		t.Fatal("authorization symlink accepted")
	}
}
