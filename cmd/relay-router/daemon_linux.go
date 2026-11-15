//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const daemonChildEnv = "RELAY_ROUTER_DAEMON_CHILD"

func maybeDaemonize(args []string, logFile string) (bool, error) {
	if os.Getenv(daemonChildEnv) == "1" {
		return false, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("resolve Router executable: %w", err)
	}
	if logFile == "" {
		logFile = "/tmp/relay-router.log"
	}
	pid, err := launchDaemon(executable, args, logFile)
	if err != nil {
		return false, err
	}
	fmt.Printf("RelayProxy Router started in background (pid %d); log: %s\n", pid, logFile)
	return true, nil
}

func launchDaemon(executable string, args []string, logFile string) (int, error) {
	logFile = strings.TrimSpace(logFile)
	if logFile == "" {
		logFile = "/tmp/relay-router.log"
	}
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		return 0, fmt.Errorf("create daemon log directory: %w", err)
	}
	log, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 0, fmt.Errorf("open daemon log %q: %w", logFile, err)
	}
	defer log.Close()

	null, err := os.Open(os.DevNull)
	if err != nil {
		return 0, fmt.Errorf("open null device: %w", err)
	}
	defer null.Close()

	cmd := exec.Command(executable, args...)
	cmd.Env = daemonChildEnvironment(os.Environ())
	cmd.Stdin = null
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start background Router: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		_ = cmd.Process.Kill()
		return 0, fmt.Errorf("release background Router process: %w", err)
	}
	return pid, nil
}

func daemonChildEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, value := range environment {
		if !strings.HasPrefix(value, daemonChildEnv+"=") {
			result = append(result, value)
		}
	}
	return append(result, daemonChildEnv+"=1")
}
