//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detach puts the update in its own session, so it outlives the hook that
// started it and is not killed with the agent's process group.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
