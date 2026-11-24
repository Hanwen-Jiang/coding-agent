//go:build windows

package server

import (
	"golang.org/x/sys/windows"
	"os"
)

// Synchronous LockFileEx waits for byte zero. The file is opened without
// FILE_FLAG_OVERLAPPED and is never deleted during normal operation.
func lockMetadataFile(file *os.File) error {
	var offset windows.Overlapped
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &offset)
}

func tryLockMetadataFile(file *os.File) (bool, error) {
	var offset windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &offset)
	if err == windows.ERROR_LOCK_VIOLATION {
		return false, nil
	}
	return err == nil, err
}

func unlockMetadataFile(file *os.File) {
	var offset windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &offset)
}
