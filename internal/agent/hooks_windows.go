//go:build windows

package agent

import (
	"os/exec"
	"time"
)

func isMountPoint(path string) bool { return true } // rely on the listing / marker check

func shellArgs(script string) (string, []string) {
	return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
}

func configureProcGroup(cmd *exec.Cmd) { cmd.WaitDelay = 5 * time.Second }

func pingArgs(host string) []string { return []string{"-n", "1", "-w", "2000", host} }
