//go:build linux

package privatefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBroadPermissionsRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if EnsureDir(dir) != nil {
		t.Fatal("directory")
	}
	if WriteNew(dir, "secret", []byte("x")) != nil {
		t.Fatal("file")
	}
	if os.Chmod(filepath.Join(dir, "secret"), 0644) != nil {
		t.Fatal("chmod")
	}
	if _, e := Read(filepath.Join(dir, "secret"), 10); e == nil {
		t.Fatal("broad file accepted")
	}
	if Replace(dir, "secret", []byte("x")) == nil {
		t.Fatal("broad file hardened implicitly")
	}
	if os.Chmod(dir, 0755) != nil {
		t.Fatal("chmod")
	}
	if CheckDir(dir) == nil {
		t.Fatal("broad directory accepted")
	}
}
