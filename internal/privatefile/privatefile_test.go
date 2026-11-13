package privatefile

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrivateAtomicFilesAndLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if EnsureDir(dir) != nil {
		t.Fatal("private directory")
	}
	if WriteNew(dir, "credential", []byte("complete")) != nil {
		t.Fatal("new file")
	}
	if WriteNew(dir, "credential", []byte("overwrite")) == nil {
		t.Fatal("overwrote file")
	}
	if raw, e := Read(filepath.Join(dir, "credential"), 8); e != nil || !bytes.Equal(raw, []byte("complete")) {
		t.Fatal("published wrong contents")
	}
	if _, e := Read(filepath.Join(dir, "credential"), 3); e == nil {
		t.Fatal("read exceeded bound")
	}
	if Replace(dir, "state", []byte("first")) != nil || Replace(dir, "state", []byte("second")) != nil {
		t.Fatal("atomic state replacement")
	}
	if raw, e := Read(filepath.Join(dir, "state"), 20); e != nil || string(raw) != "second" {
		t.Fatal("state replacement not visible")
	}
	unlock, e := Lock(dir)
	if e != nil {
		t.Fatal("lock")
	}
	if duplicate, e := Lock(dir); e == nil {
		duplicate()
		t.Fatal("concurrent lock accepted")
	}
	unlock()
	if unlock, e = Lock(dir); e != nil {
		t.Fatal("lock not released")
	}
	unlock()
	if WriteNew(dir, ".mcpguard-incoming-crash", []byte("unpublished")) != nil || CleanupTemps(dir, 20) != nil {
		t.Fatal("private crash cleanup")
	}
	if _, e := os.Lstat(filepath.Join(dir, ".mcpguard-incoming-crash")); !os.IsNotExist(e) {
		t.Fatal("unpublished temp retained")
	}
	if WriteNew(dir, "../escape", []byte("x")) == nil {
		t.Fatal("escaped private directory")
	}
}
func TestLockReleasedByProcessExit(t *testing.T) {
	if dir := os.Getenv("MCPGUARD_TEST_LOCK_CHILD"); dir != "" {
		if _, e := Lock(dir); e != nil {
			os.Exit(12)
		}
		os.Exit(0)
	}
	dir := filepath.Join(t.TempDir(), "crash")
	if EnsureDir(dir) != nil {
		t.Fatal("directory")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestLockReleasedByProcessExit$")
	child.Env = append(os.Environ(), "MCPGUARD_TEST_LOCK_CHILD="+dir)
	if child.Run() != nil {
		t.Fatal("child could not acquire lock")
	}
	unlock, e := Lock(dir)
	if e != nil {
		t.Fatal("dead process retained lock")
	}
	unlock()
}
func TestWorkspaceAndLinkRefusal(t *testing.T) {
	base := t.TempDir()
	if os.WriteFile(filepath.Join(base, ".git"), []byte("worktree marker"), 0600) != nil {
		t.Fatal("marker")
	}
	if EnsureDir(filepath.Join(base, "cache")) == nil {
		t.Fatal("cache created inside Git worktree")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "real")
	if EnsureDir(dir) != nil {
		t.Fatal("directory")
	}
	if e := os.Symlink(dir, filepath.Join(parent, "linked")); e == nil {
		if CheckDir(filepath.Join(parent, "linked")) == nil {
			t.Fatal("symlink directory accepted")
		}
		if WriteNew(dir, "secret", []byte("x")) != nil {
			t.Fatal("file")
		}
		if os.Symlink(filepath.Join(dir, "secret"), filepath.Join(dir, "link")) == nil {
			if _, e := Read(filepath.Join(dir, "link"), 10); e == nil {
				t.Fatal("symlink file accepted")
			}
		}
	} // Windows without symlink privilege is tested with a junction separately.
}
