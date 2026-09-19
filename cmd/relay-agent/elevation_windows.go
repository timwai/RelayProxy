//go:build windows

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const elevationParentArgument = "--relayproxy-elevation-parent"
const elevationParentWaitMilliseconds = 30 * 1000

var (
	modShell32         = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW  = modShell32.NewProc("ShellExecuteW")
)

// waitForElevationParent consumes the private parent marker before flag.Parse.
// The elevated replacement waits for the old process to release the singleton
// mutex and listeners, preventing the UAC handoff from racing itself.
func waitForElevationParent() {
	parentPID, filtered := consumeElevationParentArgument(os.Args)
	os.Args = filtered
	if parentPID == 0 || parentPID == uint32(os.Getpid()) {
		return
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, parentPID)
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)
	_, _ = windows.WaitForSingleObject(handle, elevationParentWaitMilliseconds)
}

func consumeElevationParentArgument(args []string) (uint32, []string) {
	if len(args) == 0 {
		return 0, args
	}
	filtered := make([]string, 0, len(args))
	filtered = append(filtered, args[0])
	var parentPID uint32
	prefix := elevationParentArgument + "="
	for _, argument := range args[1:] {
		if !strings.HasPrefix(argument, prefix) {
			filtered = append(filtered, argument)
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(argument, prefix))
		value, err := strconv.ParseUint(raw, 10, 32)
		if err == nil && value != 0 {
			parentPID = uint32(value)
		}
	}
	return parentPID, filtered
}

// ensureTransparentProxyElevation relaunches the current executable through UAC
// only when system transparent proxy mode is enabled. Ordinary SOCKS/HTTP use
// stays asInvoker and never prompts solely because WFP support is compiled in.
func ensureTransparentProxyElevation(networkMode string) (bool, error) {
	if strings.ToLower(strings.TrimSpace(networkMode)) != "divert" ||
		windows.GetCurrentProcessToken().IsElevated() {
		return false, nil
	}

	executable, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("获取当前客户端路径失败: %w", err)
	}
	arguments := append([]string(nil), os.Args[1:]...)
	arguments = append(arguments, fmt.Sprintf("%s=%d", elevationParentArgument, os.Getpid()))
	escaped := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		escaped = append(escaped, syscall.EscapeArg(argument))
	}

	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return false, err
	}
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return false, err
	}
	parameters, err := windows.UTF16PtrFromString(strings.Join(escaped, " "))
	if err != nil {
		return false, err
	}

	result, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(parameters)),
		0,
		1, // SW_SHOWNORMAL
	)
	if result <= 32 {
		return false, fmt.Errorf("请求管理员权限失败 (ShellExecuteW=%d): %v", result, callErr)
	}
	return true, nil
}
