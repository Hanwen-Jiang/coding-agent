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
func unlockMetadataFile(file *os.File) {
	var offset windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &offset)
}
