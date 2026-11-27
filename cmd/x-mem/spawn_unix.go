//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// spawnDetached starts a process that outlives this one and the terminal.
func spawnDetached(name string, args ...string) int {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() != nil {
		return 0
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	return pid
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
