//go:build !windows

package agent

import (
	"log"
	"os"
	"syscall"
)

// restartSelf replaces the process with a fresh copy of itself (same flags),
// so it works with or without a service supervisor.
func restartSelf() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("cannot restart: %v", err)
		return
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		log.Printf("restart failed: %v (exiting so a supervisor can restart the agent)", err)
		os.Exit(3)
	}
}
