//go:build windows

package hook

import "os/exec"

func spawnDetached(name string, args ...string) {
	cmd := exec.Command(name, args...)
	if cmd.Start() == nil {
		cmd.Process.Release()
	}
}
