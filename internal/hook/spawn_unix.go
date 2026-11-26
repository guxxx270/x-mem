//go:build !windows

package hook

import (
	"os/exec"
	"syscall"
)

// spawnDetached starts a process that outlives the hook and does not hold
// Claude Code's pipes open.
func spawnDetached(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		cmd.Process.Release()
	}
}
