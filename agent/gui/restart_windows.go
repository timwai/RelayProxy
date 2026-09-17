//go:build windows

package gui

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// restartParentEnv is consumed before the singleton mutex is acquired. The
// replacement process waits for the old process to release all listeners and
// the named mutex, so a GUI restart cannot race its own shutdown.
const restartParentEnv = "RELAYPROXY_RESTART_PARENT_PID"

const restartParentWaitMilliseconds = 30 * 1000

// launchRestartProcess starts the same executable with the exact arguments
// used for the current launch. The child waits for this process in
// WaitForRestartParent before trying to acquire the singleton mutex.
func launchRestartProcess() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取当前客户端路径失败: %w", err)
	}
	arguments := append([]string(nil), os.Args[1:]...)
	command := exec.Command(executable, arguments...)
	command.Env = restartEnvironment(os.Environ(), os.Getpid())
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := command.Start(); err != nil {
		return fmt.Errorf("启动新的客户端进程失败: %w", err)
	}
	// The parent deliberately does not wait for the replacement. Release the
	// process handle so a long-running GUI does not retain a child handle until
	// shutdown.
	_ = command.Process.Release()
	return nil
}

func restartEnvironment(environment []string, parentPID int) []string {
	prefix := restartParentEnv + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+strconv.Itoa(parentPID))
}

// WaitForRestartParent blocks only when this process was launched by the GUI's
// restart action. An exited or inaccessible parent is safe to ignore because
// the singleton mutex acquisition below remains the final authority.
func WaitForRestartParent() {
	raw := strings.TrimSpace(os.Getenv(restartParentEnv))
	if raw == "" {
		return
	}
	_ = os.Unsetenv(restartParentEnv)
	parentPID, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || parentPID == 0 || parentPID == uint64(os.Getpid()) {
		return
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(parentPID))
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)
	_, _ = windows.WaitForSingleObject(handle, restartParentWaitMilliseconds)
}
