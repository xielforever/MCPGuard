// Package privatefile implements private cache files, atomic writes and local
// process locks on Linux and Windows. It does not isolate a hostile same-user process.
package privatefile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrPrivate = errors.New("private file operation failed")

func CheckDir(dir string) error {
	absolute, e := filepath.Abs(dir)
	if e != nil {
		return ErrPrivate
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || reparse(p) {
			return ErrPrivate
		}
		if _, e = os.Lstat(filepath.Join(p, ".git")); e == nil {
			return ErrPrivate
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	info, e := os.Stat(absolute)
	if e != nil || privateSecurity(absolute, info) != nil {
		return ErrPrivate
	}
	return nil
}
func Read(path string, max int64) ([]byte, error) {
	if CheckDir(filepath.Dir(path)) != nil {
		return nil, ErrPrivate
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || reparse(path) || privateSecurity(path, info) != nil || info.Size() > max {
		return nil, ErrPrivate
	}
	raw, e := os.ReadFile(path)
	if e != nil || int64(len(raw)) > max {
		return nil, ErrPrivate
	}
	return raw, nil
}
func CheckNew(dir, name string) error {
	if CheckDir(dir) != nil || filepath.Base(name) != name || name == "." || name == ".." {
		return ErrPrivate
	}
	if _, e := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(e) {
		return ErrPrivate
	}
	return nil
}
func WriteNew(dir, name string, data []byte) error {
	if CheckNew(dir, name) != nil {
		return ErrPrivate
	}
	f, e := os.CreateTemp(dir, ".mcpguard-incoming-*")
	if e != nil {
		return ErrPrivate
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	if secureCreated(temp) != nil {
		return ErrPrivate
	}
	if _, e = f.Write(data); e != nil {
		return ErrPrivate
	}
	if e = f.Sync(); e != nil {
		return ErrPrivate
	}
	if e = f.Close(); e != nil {
		return ErrPrivate
	}
	if e = os.Link(temp, filepath.Join(dir, name)); e != nil {
		return ErrPrivate
	}
	return syncDir(dir)
}
func EnsureDir(dir string) error {
	if _, e := os.Lstat(dir); e == nil {
		return CheckDir(dir)
	} else if !os.IsNotExist(e) {
		return ErrPrivate
	}
	parent := filepath.Dir(dir)
	absolute, e := filepath.Abs(parent)
	if e != nil {
		return ErrPrivate
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || reparse(p) || info.Mode()&os.ModeSymlink != 0 {
			return ErrPrivate
		}
		if _, e = os.Lstat(filepath.Join(p, ".git")); e == nil {
			return ErrPrivate
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if mkdirPrivate(dir) != nil {
		return ErrPrivate
	}
	return CheckDir(dir)
}
func Replace(dir, name string, data []byte) error {
	if CheckDir(dir) != nil || filepath.Base(name) != name || name == "." || name == ".." {
		return ErrPrivate
	}
	path := filepath.Join(dir, name)
	if info, e := os.Lstat(path); e == nil {
		if !info.Mode().IsRegular() || reparse(path) || privateSecurity(path, info) != nil {
			return ErrPrivate
		}
	} else if !os.IsNotExist(e) {
		return ErrPrivate
	}
	f, e := os.CreateTemp(dir, ".mcpguard-state-*")
	if e != nil {
		return ErrPrivate
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	if secureCreated(temp) != nil {
		return ErrPrivate
	}
	if _, e = f.Write(data); e != nil {
		return ErrPrivate
	}
	if f.Sync() != nil || f.Close() != nil {
		return ErrPrivate
	}
	if replaceFile(temp, path) != nil {
		return ErrPrivate
	}
	return syncDir(dir)
}
func Lock(dir string) (func(), error) {
	if CheckDir(dir) != nil {
		return nil, ErrPrivate
	}
	path := filepath.Join(dir, ".mcpguard-cache.lock")
	if info, e := os.Lstat(path); e == nil {
		if !info.Mode().IsRegular() || reparse(path) || privateSecurity(path, info) != nil {
			return nil, ErrPrivate
		}
	} else if !os.IsNotExist(e) {
		return nil, ErrPrivate
	}
	f, e := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		return nil, ErrPrivate
	}
	if secureCreated(path) != nil || lockFile(f) != nil {
		f.Close()
		return nil, ErrPrivate
	}
	return func() { _ = unlockFile(f); _ = f.Close() }, nil
}

// CleanupTemps is used while holding the profile lock. A crashed writer may
// leave an unpublished private temp file; links and unexpected names fail closed.
func CleanupTemps(dir string, max int) error {
	if CheckDir(dir) != nil || max < 1 {
		return ErrPrivate
	}
	f, e := os.Open(dir)
	if e != nil {
		return ErrPrivate
	}
	defer f.Close()
	items, e := f.ReadDir(max + 1)
	if errors.Is(e, io.EOF) {
		e = nil
	}
	if e != nil || len(items) > max {
		return ErrPrivate
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Name(), ".mcpguard-incoming-") && !strings.HasPrefix(item.Name(), ".mcpguard-state-") {
			continue
		}
		path := filepath.Join(dir, item.Name())
		if _, e := Read(path, 1<<20); e != nil {
			return ErrPrivate
		}
		if os.Remove(path) != nil {
			return ErrPrivate
		}
	}
	return syncDir(dir)
}
