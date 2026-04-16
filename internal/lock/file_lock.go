// Package lock provides an advisory-locked file per environment so that two
// c2quay processes cannot deploy to the same environment simultaneously.
//
// v0 uses flock(2) and is therefore local to the host. Distributed coordination
// (redis, consul) is a future extension.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrAlreadyHeld is returned by Acquire when another process holds the lock.
var ErrAlreadyHeld = errors.New("lock is already held")

// FileLock holds an exclusive flock(2) for its lifetime.
type FileLock struct {
	path string
	file *os.File
}

// Acquire takes an exclusive non-blocking flock on path. It returns
// ErrAlreadyHeld if another process currently holds the lock.
func Acquire(path string) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyHeld, path)
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return &FileLock{path: path, file: f}, nil
}

// Release drops the flock and closes the underlying file descriptor.
func (l *FileLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err1 := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err2 := l.file.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// Path returns the on-disk location of the lock file.
func (l *FileLock) Path() string { return l.path }
