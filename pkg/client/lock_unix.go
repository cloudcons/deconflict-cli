//go:build !windows

package client

import (
	"fmt"
	"os"
	"syscall"
)

// lockAppend takes an exclusive lock on an open log file and returns the
// function that releases it.
func lockAppend(fh *os.File) (func(), error) {
	if err := syscall.Flock(int(fh.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock %s: %w", fh.Name(), err)
	}
	return func() { syscall.Flock(int(fh.Fd()), syscall.LOCK_UN) }, nil
}
