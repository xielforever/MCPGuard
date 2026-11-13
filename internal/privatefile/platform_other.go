//go:build !linux && !windows

package privatefile

import "os"

func reparse(string) bool                       { return true }
func privateSecurity(string, os.FileInfo) error { return ErrPrivate }
func secureCreated(string) error                { return ErrPrivate }
func mkdirPrivate(string) error                 { return ErrPrivate }
func replaceFile(string, string) error          { return ErrPrivate }
func syncDir(string) error                      { return ErrPrivate }
func lockFile(*os.File) error                   { return ErrPrivate }
func unlockFile(*os.File) error                 { return ErrPrivate }
