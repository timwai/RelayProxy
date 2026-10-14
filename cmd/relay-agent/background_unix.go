//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func configureBackgroundProcess(cmd *exec.Cmd) {
	// Start a new session so closing the shell/terminal cannot hang up Agent.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
