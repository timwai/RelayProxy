//go:build windows

package gui

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"relayproxy/agent/divert"
)

const (
	shellExecuteMaskNoCloseProcess = 0x00000040
	waitInfinite                  = 0xffffffff
)

var (
	modShell32WFP        = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExWFP = modShell32WFP.NewProc("ShellExecuteExW")
)

type shellExecuteInfoWFP struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

func installWFPDriverFromGUI() (divert.WFPDriverStatus, error) {
	status := divert.WindowsWFPDriverStatus()
	if !status.PackageAvailable {
		if status.PackageError != "" {
			return status, errors.New(status.PackageError)
		}
		return status, errors.New("当前客户端没有内嵌可安装的 WFP 驱动包")
	}
	if status.Ready && status.Current {
		return status, nil
	}
	if status.Elevated {
		if err := divert.InstallWFPDriver(); err != nil {
			return divert.WindowsWFPDriverStatus(), err
		}
		status = divert.WindowsWFPDriverStatus()
		if !status.Ready {
			return status, fmt.Errorf("WFP 驱动安装完成，但仍未就绪: %s", status.Reason)
		}
		return status, nil
	}

	executable, err := os.Executable()
	if err != nil {
		return status, fmt.Errorf("获取当前客户端路径失败: %w", err)
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return status, err
	}
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return status, err
	}
	parameters, err := windows.UTF16PtrFromString(syscall.EscapeArg("--relayproxy-install-wfp-driver"))
	if err != nil {
		return status, err
	}

	info := shellExecuteInfoWFP{
		fMask:        shellExecuteMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: parameters,
		nShow:        0,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	ok, _, callErr := procShellExecuteExWFP.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return status, fmt.Errorf("请求管理员权限失败: %w", callErr)
	}
	if info.hProcess == 0 {
		return status, errors.New("管理员安装进程未返回有效句柄")
	}
	defer windows.CloseHandle(info.hProcess)

	if _, err := windows.WaitForSingleObject(info.hProcess, waitInfinite); err != nil {
		return status, fmt.Errorf("等待驱动安装进程失败: %w", err)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &exitCode); err != nil {
		return status, fmt.Errorf("读取驱动安装结果失败: %w", err)
	}

	status = divert.WindowsWFPDriverStatus()
	if exitCode != 0 {
		if status.Reason != "" {
			return status, fmt.Errorf("WFP 驱动安装失败 (exit=%d): %s", exitCode, status.Reason)
		}
		return status, fmt.Errorf("WFP 驱动安装失败 (exit=%d)", exitCode)
	}
	if !status.Ready {
		return status, fmt.Errorf("WFP 驱动安装进程已完成，但驱动仍未就绪: %s", status.Reason)
	}
	return status, nil
}
