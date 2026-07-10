//go:build windows

package config

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockGlobalConfig opens or creates a .lock file alongside path and
// acquires an exclusive, blocking lock via LockFileEx (the Windows
// equivalent of flock).  The caller must call unlockGlobalConfig when
// done, even on error paths.
func lockGlobalConfig(path string) (*os.File, error) {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	if err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK, // exclusive, blocking (no FAIL_IMMEDIATELY)
		0,                                // reserved
		1, 0,                             // lock 1 byte at offset 0
		&windows.Overlapped{},
	); err != nil {
		_ = f.Close()
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("lock %s: %w", lockPath, err)
	}
	return f, nil
}

// unlockGlobalConfig releases the lock and cleans up the lock file.
func unlockGlobalConfig(f *os.File) {
	if f == nil {
		return
	}
	path := f.Name()
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
	_ = f.Close()
	_ = os.Remove(path)
}
