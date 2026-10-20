//go:build windows

package singleton

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type replacementShellExecuteInfo struct {
	Size       uint32
	Mask       uint32
	Window     uintptr
	Verb       *uint16
	File       *uint16
	Parameters *uint16
	Directory  *uint16
	Show       int32
	Instance   uintptr
	IDList     uintptr
	Class      *uint16
	ClassKey   windows.Handle
	HotKey     uint32
	Icon       windows.Handle
	Process    windows.Handle
}

var (
	replacementShellExecute   = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")
	replacementOLE32          = windows.NewLazySystemDLL("ole32.dll")
	replacementCoInitialize   = replacementOLE32.NewProc("CoInitializeEx")
	replacementCoUninitialize = replacementOLE32.NewProc("CoUninitialize")
)

// RunReplacementHelper only terminates the owner of the requested Agent mutex.
// The helper never starts a second desktop, network service or elevated Agent.
func RunReplacementHelper(raw string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("旧客户端替换助手未获得管理员权限")
	}
	request, err := parseReplacementRequest(raw)
	if err != nil {
		return err
	}
	requester, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, request.Requester)
	if err != nil {
		return fmt.Errorf("读取客户端启动进程失败: %w", err)
	}
	defer windows.CloseHandle(requester)
	created, err := processCreationTime(requester)
	if err != nil || created != request.Created {
		return errors.New("客户端启动进程已退出或被替换")
	}
	name, _ := windows.UTF16PtrFromString("Global\\" + request.Name)
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE|1, false, name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil // The predecessor exited while the UAC prompt was open.
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	pid, err := mutantOwnerPID(handle)
	if err != nil {
		return err
	}
	if request.PID != 0 && pid != request.PID {
		return nil // A competing launch already replaced the requested process.
	}
	if pid == 0 {
		return nil
	}
	// Only kill and wait for the process. The ordinary startup process, not
	// this elevated helper, must acquire and retain the instance mutex.
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	ownerCreated, err := processCreationTime(process)
	if err != nil {
		return err
	}
	if !request.canReplace(pid, ownerCreated) {
		return nil // A newer launch won the lock while UAC was awaiting consent.
	}
	current, err := mutantOwnerPID(handle)
	if err != nil || current != pid {
		return err
	}
	if state, err := windows.WaitForSingleObject(requester, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		return errors.New("客户端启动进程已退出，取消替换旧客户端")
	}
	if err := windows.TerminateProcess(process, 0); err != nil {
		if state, _ := windows.WaitForSingleObject(process, 0); state != windows.WAIT_OBJECT_0 {
			return err
		}
	}
	return waitForReplacementObject(process, time.Now().Add(10*time.Second))
}

func runElevatedReplacement(name string, pid uint32) error {
	if name != AgentInstanceName {
		return errors.New("仅支持为 RelayProxy 客户端请求替换权限")
	}
	// acquire runs on a pinned OS thread. ShellExecuteEx may invoke COM shell
	// extensions, so initialize that thread's apartment for the UAC request.
	status, _, _ := replacementCoInitialize.Call(0, 0x2|0x4) // apartment-threaded, disable OLE1 DDE
	if int32(status) < 0 {
		return fmt.Errorf("初始化客户端提权环境失败: HRESULT 0x%08x", uint32(status))
	}
	defer replacementCoUninitialize.Call()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	created, err := processCreationTime(windows.CurrentProcess())
	if err != nil {
		return err
	}
	request, err := json.Marshal(replacementRequest{Name: name, PID: pid, Requester: uint32(os.Getpid()), Created: created})
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return err
	}
	parameters, err := windows.UTF16PtrFromString(windows.EscapeArg("--" + ReplacementHelperFlagName() + "=" + string(request)))
	if err != nil {
		return err
	}
	directory, err := windows.UTF16PtrFromString(filepath.Dir(executable))
	if err != nil {
		return err
	}
	info := replacementShellExecuteInfo{
		Size: uint32(unsafe.Sizeof(replacementShellExecuteInfo{})),
		Mask: 0x40 | 0x100, // SEE_MASK_NOCLOSEPROCESS | SEE_MASK_NOASYNC
		Verb: verb, File: file, Parameters: parameters, Directory: directory,
	}
	ok, _, callErr := replacementShellExecute.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return errors.New("已取消管理员授权，旧客户端继续运行，新客户端未启动")
		}
		return fmt.Errorf("请求结束旧客户端的管理员权限失败: %v", callErr)
	}
	if info.Process == 0 {
		return errors.New("客户端替换助手未返回进程句柄")
	}
	defer windows.CloseHandle(info.Process)
	if err := waitForReplacementObject(info.Process, time.Now().Add(30*time.Second)); err != nil {
		return fmt.Errorf("等待客户端替换助手失败: %w", err)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(info.Process, &exitCode); err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("客户端替换助手失败（退出码 %d），旧客户端未能退出", exitCode)
	}
	return nil
}

func processCreationTime(process windows.Handle) (uint64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}
