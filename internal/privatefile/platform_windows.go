package privatefile

import (
	"golang.org/x/sys/windows"
	"os"
	"strings"
	"unsafe"
)

func currentSID() (*windows.SID, error) {
	user, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return nil, e
	}
	return user.User.Sid, nil
}
func privateSD() (*windows.SECURITY_DESCRIPTOR, error) {
	sid, e := currentSID()
	if e != nil {
		return nil, e
	}
	return windows.SecurityDescriptorFromString("O:" + sid.String() + "D:P(A;OICI;FA;;;" + sid.String() + ")(A;OICI;FA;;;SY)")
}
func reparse(path string) bool {
	if strings.HasPrefix(path, `\\`) {
		return true
	}
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return true
	}
	attributes, e := windows.GetFileAttributes(p)
	return e != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
func privateSecurity(path string, _ os.FileInfo) error {
	sid, e := currentSID()
	if e != nil {
		return ErrPrivate
	}
	sd, e := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil {
		return ErrPrivate
	}
	owner, _, e := sd.Owner()
	if e != nil || !owner.Equals(sid) {
		return ErrPrivate
	}
	control, _, e := sd.Control()
	if e != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return ErrPrivate
	}
	acl, _, e := sd.DACL()
	if e != nil || acl == nil || acl.AceCount < 1 {
		return ErrPrivate
	}
	ownerAllowed := false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return ErrPrivate
		}
		allowed := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !allowed.Equals(sid) && allowed.String() != "S-1-5-18" {
			return ErrPrivate
		}
		if uint32(ace.Mask)&0x1f01ff != 0x1f01ff {
			return ErrPrivate
		}
		if allowed.Equals(sid) {
			ownerAllowed = true
		}
	}
	if !ownerAllowed {
		return ErrPrivate
	}
	return nil
}
func secureCreated(path string) error {
	sd, e := privateSD()
	if e != nil {
		return e
	}
	acl, _, e := sd.DACL()
	if e != nil {
		return e
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
func mkdirPrivate(path string) error {
	sd, e := privateSD()
	if e != nil {
		return e
	}
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return e
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	return windows.CreateDirectory(p, &sa)
}
func replaceFile(from, to string) error {
	a, e := windows.UTF16PtrFromString(from)
	if e != nil {
		return e
	}
	b, e := windows.UTF16PtrFromString(to)
	if e != nil {
		return e
	}
	return windows.MoveFileEx(a, b, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func syncDir(string) error { return nil } // File Sync + write-through rename are used on Windows.
func lockFile(f *os.File) error {
	var overlap windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap)
}
func unlockFile(f *os.File) error {
	var overlap windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlap)
}
