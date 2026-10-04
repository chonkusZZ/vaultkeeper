//go:build !windows

package agent

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// isMountPoint reports whether path sits on a different device than its parent.
func isMountPoint(path string) bool {
	a, err1 := os.Stat(path)
	b, err2 := os.Stat(path + "/..")
	if err1 != nil || err2 != nil {
		return false
	}
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	return ok1 && ok2 && sa.Dev != sb.Dev
}

func shellArgs(script string) (string, []string) { return "/bin/sh", []string{"-c", script} }

// killTree makes the script and everything it started die together on timeout.
func configureProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
}

func pingArgs(host string) []string {
	if isDarwin {
		return []string{"-c", "1", "-W", "2000", host}
	}
	return []string{"-c", "1", "-W", "2", host}
}
