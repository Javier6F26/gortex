//go:build !windows

package config

import (
	"fmt"
	"os"
	"syscall"
)

// lockGlobalConfig opens or creates a .lock file alongside path and
// acquires an exclusive, blocking flock.  The caller must call
// unlockGlobalConfig when done, even on error paths.
//
// A blocking flock is used so concurrent processes wait their turn
// instead of busy-looping or failing.
func lockGlobalConfig(path string) (*os.File, error) {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("flock %s: %w", lockPath, err)
	}
	return f, nil
}

// unlockGlobalConfig releases the flock and cleans up the lock file.
func unlockGlobalConfig(f *os.File) {
	if f == nil {
		return
	}
	path := f.Name()
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
	_ = os.Remove(path)
}
