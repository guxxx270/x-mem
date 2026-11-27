//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const detachedProcess = 0x00000008

func spawnDetached(name string, args ...string) int {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess}
	if cmd.Start() != nil {
		return 0
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	return pid
}

func processAlive(pid int) bool {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	syscall.CloseHandle(h)
	return true
}
