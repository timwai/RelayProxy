package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const backgroundReadyMessage = "RELAYPROXY_AGENT_BACKGROUND_READY"

func backgroundChildArgs(args []string, logFile string) []string {
	childArgs := append([]string(nil), args...)
	// Keep all user-supplied overrides and the working directory. Appending
	// flags also makes --no-gui win over any default desktop launch.
	return append(childArgs, "--no-gui", "--log-file="+logFile, "--_background-child")
}

func resolveBackgroundLogFile(requested string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home directory for background log: %w", err)
		}
		requested = filepath.Join(home, ".relayproxy", "logs", "relay-agent.log")
	}
	path, err := filepath.Abs(requested)
	if err != nil {
		return "", fmt.Errorf("resolve background log path: %w", err)
	}
	return path, nil
}

func launchBackground(args []string, requestedLog string) error {
	logPath, err := resolveBackgroundLogFile(requestedLog)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate agent executable: %w", err)
	}
	pid, err := startBackgroundCommand(exec.Command(executable, backgroundChildArgs(args, logPath)...), logPath)
	if err != nil {
		return err
	}
	fmt.Printf("RelayProxy Agent started in background (PID %d). Log: %s\n", pid, logPath)
	return nil
}

// startBackgroundCommand waits until the child explicitly reports that both
// the Agent and the optional web UI have started. A bare successful fork is not
// proof that a config, identity, listener, or instance lock is usable.
func startBackgroundCommand(cmd *exec.Cmd, logPath string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return 0, fmt.Errorf("create background log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return 0, fmt.Errorf("open background log: %w", err)
	}
	defer logFile.Close()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("create background startup pipe: %w", err)
	}
	cmd.Stdin = nil
	cmd.Stderr = logFile
	configureBackgroundProcess(cmd)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start background agent: %w", err)
	}

	ready := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(stdout)
		line, readErr := reader.ReadString('\n')
		switch {
		case readErr == nil && strings.TrimSpace(line) == backgroundReadyMessage:
			ready <- nil
		case readErr == nil:
			ready <- fmt.Errorf("unexpected background startup response: %q", strings.TrimSpace(line))
		case errors.Is(readErr, io.EOF):
			ready <- errors.New("agent exited before reporting startup readiness")
		default:
			ready <- fmt.Errorf("read background startup response: %w", readErr)
		}
	}()

	select {
	case err = <-ready:
	case <-time.After(60 * time.Second):
		err = errors.New("timed out waiting for background agent startup")
	}
	_ = stdout.Close()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 0, fmt.Errorf("%w (see %s)", err, logPath)
	}
	pid := cmd.Process.Pid
	// The child is intentionally independent of this short-lived CLI process.
	// Release the parent-side OS handle without waiting for Agent shutdown.
	if err := cmd.Process.Release(); err != nil {
		return 0, fmt.Errorf("release background process handle: %w", err)
	}
	return pid, nil
}

func notifyBackgroundReady() {
	_, _ = fmt.Fprintln(os.Stdout, backgroundReadyMessage)
	// The parent closes its readiness pipe once it sees the message. Never
	// write application output into that pipe afterwards (SIGPIPE on Unix).
	previous := os.Stdout
	os.Stdout = os.Stderr
	_ = previous.Close()
}
