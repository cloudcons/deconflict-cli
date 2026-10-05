//go:build windows

package cli

import "os"

// processAlive reports whether a process exists. On Windows FindProcess opens
// a handle, which fails for a process that has gone.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
