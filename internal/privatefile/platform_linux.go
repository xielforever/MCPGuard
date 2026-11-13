package privatefile

import (
	"os"
	"syscall"
)

func reparse(string) bool { return false }
func privateSecurity(_ string, info os.FileInfo) error {
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
		return ErrPrivate
	}
	return nil
}
func secureCreated(path string) error   { return os.Chmod(path, 0600) }
func mkdirPrivate(path string) error    { return os.Mkdir(path, 0700) }
func replaceFile(from, to string) error { return os.Rename(from, to) }
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return ErrPrivate
	}
	defer f.Close()
	if f.Sync() != nil {
		return ErrPrivate
	}
	return nil
}
func lockFile(f *os.File) error   { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
