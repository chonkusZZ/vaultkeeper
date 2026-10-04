//go:build windows

package agent

import (
	"log"
	"os"
	"os/exec"
)

func restartSelf() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("cannot restart: %v", err)
		return
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("restart failed: %v", err)
		return
	}
	os.Exit(0)
}
