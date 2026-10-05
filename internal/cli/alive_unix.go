//go:build !windows

package cli

import "syscall"

// processAlive reports whether a process exists, by signal 0.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
