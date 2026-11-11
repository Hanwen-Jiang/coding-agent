//go:build unix

package server

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

// flock is attached to this open file description, not a PID. Independent
// handles in one process contend as well as handles from separate processes.
func lockMetadataFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
func unlockMetadataFile(file *os.File) {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		if !errors.Is(err, unix.EINTR) {
			return
		}
	}
}
