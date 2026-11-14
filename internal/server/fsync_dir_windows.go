//go:build windows

package server

// syncDir is a no-op on Windows: a directory cannot be flushed there (os.Open
// gives a read-only handle and FlushFileBuffers fails with "Access is denied"),
// and NTFS journals the rename's metadata itself. Linux keeps the real fsync.
func syncDir(string) error { return nil }
