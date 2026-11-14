//go:build !windows

package server

import "os"

// syncDir fsyncs a directory so a rename into it survives a host crash, not only
// a process exit.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
