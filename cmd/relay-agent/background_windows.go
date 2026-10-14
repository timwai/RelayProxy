//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

func configureBackgroundProcess(cmd *exec.Cmd) {
	// DETACHED_PROCESS prevents inheriting the launching console. The pipes
	// used for the readiness message and the log are still explicitly passed.
	const detachedProcess = 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}
