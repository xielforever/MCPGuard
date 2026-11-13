//go:build windows

package privatefile

import (
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBroadDACLAndReparseRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if EnsureDir(dir) != nil {
		t.Fatal("protected DACL directory")
	}
	if WriteNew(dir, "secret", []byte("x")) != nil {
		t.Fatal("protected DACL file")
	}
	sid, e := currentSID()
	if e != nil {
		t.Fatal("user SID")
	}
	sd, e := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid.String() + ")(A;;GR;;;WD)")
	if e != nil {
		t.Fatal("test descriptor")
	}
	acl, _, _ := sd.DACL()
	if windows.SetNamedSecurityInfo(filepath.Join(dir, "secret"), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil) != nil {
		t.Fatal("test ACL")
	}
	if _, e := Read(filepath.Join(dir, "secret"), 10); e == nil {
		t.Fatal("Everyone read ACE accepted")
	}
	if Replace(dir, "secret", []byte("x")) == nil {
		t.Fatal("broad file hardened implicitly")
	}
	base := t.TempDir()
	link := filepath.Join(base, "junction")
	// The exact verified temporary paths are test-owned and only create a junction.
	c := exec.Command("cmd.exe", "/c", "mklink", "/J", link, dir)
	if c.Run() != nil {
		t.Fatal("junction test setup")
	}
	defer os.Remove(link)
	if CheckDir(link) == nil || EnsureDir(filepath.Join(link, "escape")) == nil {
		t.Fatal("reparse ancestor accepted")
	}
}
