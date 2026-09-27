//go:build windows

package startup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

const (
	startupHelperFlagName = "relayproxy-startup-helper"
	startupHelperInstall  = "install"
	startupHelperRemove   = "remove"

	shellExecuteMaskNoCloseProcess = 0x00000040
	shellExecuteMaskNoAsync        = 0x00000100
	shellShowHidden                = 0
)

var startupMu sync.Mutex

type shellExecuteInfo struct {
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
	startupShell32      = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = startupShell32.NewProc("ShellExecuteExW")
)

type autoStartState struct {
	command string
	taskXML string
}

type autoStartStore interface {
	read() (autoStartState, error)
	writeCommand(string) error
	writeTask(string) error
	deleteTask() error
}

// SetAutoStart uses a per-user, interactive, highest-privilege logon task for
// transparent interception. Ordinary proxy mode keeps the unprivileged Run key.
// Creating/removing an elevated task requires one administrator-launched session.
func SetAutoStart(appName, exePath, configPath string, enable, requireAdmin bool) error {
	startupMu.Lock()
	defer startupMu.Unlock()
	store, err := newWindowsAutoStartStore(appName)
	if err != nil {
		return err
	}
	before, err := store.read()
	if err != nil {
		return err
	}
	desired, err := desiredAutoStart(exePath, configPath, store.userSID, enable, requireAdmin)
	if err != nil {
		return err
	}
	elevated := windows.GetCurrentProcessToken().IsElevated()
	if !elevated && !sameAutoStart(before, desired) {
		switch {
		case desired.taskXML != "":
			return runElevatedAutoStartHelper(configPath, startupHelperInstall)
		case before.taskXML != "":
			if err := runElevatedAutoStartHelper(configPath, startupHelperRemove); err != nil {
				return err
			}
			if desired.command != "" {
				return store.writeCommand(desired.command)
			}
			return nil
		}
	}
	_, err = applyAutoStart(store, before, desired, elevated)
	return err
}

// SyncAutoStart migrates an already-enabled login entry when the saved network
// mode changes. It never enables autostart on its own. The rollback restores the
// exact previous registration if the caller cannot save the new configuration.
func SyncAutoStart(appName, exePath, configPath string, requireAdmin bool) (func() error, error) {
	startupMu.Lock()
	defer startupMu.Unlock()
	store, err := newWindowsAutoStartStore(appName)
	if err != nil {
		return nil, err
	}
	before, err := store.read()
	if err != nil {
		return nil, err
	}
	if autoStartCommand(before) == "" {
		return nil, nil
	}
	desired, err := desiredAutoStart(exePath, configPath, store.userSID, true, requireAdmin)
	if err != nil {
		return nil, err
	}
	elevated := windows.GetCurrentProcessToken().IsElevated()
	if !elevated && before.taskXML == "" && desired.taskXML != "" {
		if err := runElevatedAutoStartHelper(configPath, startupHelperInstall); err != nil {
			return nil, err
		}
		beforeCommand := before.command
		return func() error {
			startupMu.Lock()
			defer startupMu.Unlock()
			removeErr := runElevatedAutoStartHelper(configPath, startupHelperRemove)
			restoreErr := store.writeCommand(beforeCommand)
			return errors.Join(removeErr, restoreErr)
		}, nil
	}
	if !elevated && before.taskXML != "" && desired.taskXML == "" {
		if err := runElevatedAutoStartHelper(configPath, startupHelperRemove); err != nil {
			return nil, err
		}
		if err := store.writeCommand(desired.command); err != nil {
			// Best-effort restore of the managed elevated task if publishing the
			// ordinary Run entry fails.
			restoreErr := runElevatedAutoStartHelper(configPath, startupHelperInstall)
			return nil, errors.Join(err, restoreErr)
		}
		return func() error {
			startupMu.Lock()
			defer startupMu.Unlock()
			removeErr := store.writeCommand("")
			restoreErr := runElevatedAutoStartHelper(configPath, startupHelperInstall)
			return errors.Join(removeErr, restoreErr)
		}, nil
	}
	rollback, err := applyAutoStart(store, before, desired, elevated)
	if rollback == nil || err != nil {
		return nil, err
	}
	return func() error {
		startupMu.Lock()
		defer startupMu.Unlock()
		return rollback()
	}, nil
}

func desiredAutoStart(exePath, configPath, userSID string, enable, requireAdmin bool) (autoStartState, error) {
	if !enable {
		return autoStartState{}, nil
	}
	executable, arguments, err := autoStartAction(exePath, configPath)
	if err != nil {
		return autoStartState{}, err
	}
	if requireAdmin {
		definition, err := elevatedTaskXML(executable, arguments, userSID)
		return autoStartState{taskXML: definition}, err
	}
	return autoStartState{command: syscall.EscapeArg(executable) + " " + arguments}, nil
}

func autoStartAction(exePath, configPath string) (string, string, error) {
	if strings.TrimSpace(exePath) == "" || strings.ContainsRune(exePath, 0) || strings.ContainsRune(configPath, 0) {
		return "", "", errors.New("无效的自启动程序或配置路径")
	}
	executable, err := filepath.Abs(exePath)
	if err != nil {
		return "", "", err
	}
	// --minimized is a GUI startup mode, but the executable itself may have a
	// product-specific name (for example RelayProxy.exe). Keep --gui explicit so
	// login startup does not fall back to headless mode merely because the binary
	// name does not contain "gui".
	arguments := "--gui --minimized"
	if configPath != "" {
		absoluteConfig, err := filepath.Abs(configPath)
		if err != nil {
			return "", "", err
		}
		arguments += " --config " + syscall.EscapeArg(absoluteConfig)
	}
	return executable, arguments, nil
}

func sameAutoStart(left, right autoStartState) bool {
	if left.command != right.command {
		return false
	}
	if left.taskXML == "" || right.taskXML == "" {
		return left.taskXML == right.taskXML
	}
	return sameTaskDefinition(left.taskXML, right.taskXML)
}

func restoreAutoStart(store autoStartStore, state autoStartState) error {
	var err error
	if state.taskXML != "" {
		err = store.writeTask(state.taskXML)
	} else {
		err = store.deleteTask()
	}
	return errors.Join(err, store.writeCommand(state.command))
}

func applyAutoStart(store autoStartStore, before, desired autoStartState, elevated bool) (func() error, error) {
	if sameAutoStart(before, desired) {
		return nil, nil
	}
	if (before.taskXML != "" || desired.taskXML != "") && !elevated {
		return nil, errors.New("透明代理的开机自启需要管理员权限，请以管理员身份启动客户端后设置一次")
	}
	rollback := func() error { return restoreAutoStart(store, before) }
	if desired.taskXML != "" {
		if err := store.writeTask(desired.taskXML); err != nil {
			return nil, fmt.Errorf("设置管理员自启动任务失败: %w", err)
		}
		if err := store.writeCommand(""); err != nil {
			return nil, errors.Join(fmt.Errorf("移除普通自启动项失败: %w", err), rollback())
		}
	} else {
		if err := store.writeCommand(desired.command); err != nil {
			return nil, err
		}
		if before.taskXML != "" {
			if err := store.deleteTask(); err != nil {
				return nil, errors.Join(fmt.Errorf("移除管理员自启动任务失败: %w", err), store.writeCommand(before.command))
			}
		}
	}
	return rollback, nil
}

func autoStartCommand(state autoStartState) string {
	if task, err := parseTaskDefinition(state.taskXML); err == nil && task.Settings.Enabled && len(task.Actions.Exec) == 1 {
		for _, trigger := range task.Triggers.Logon {
			if trigger.Enabled {
				return syscall.EscapeArg(task.Actions.Exec[0].Command) + " " + task.Actions.Exec[0].Arguments
			}
		}
	}
	return strings.TrimSpace(state.command)
}

// AutoStartCommand reports the active managed task or the ordinary Run entry.
func AutoStartCommand(appName string) string {
	store, err := newWindowsAutoStartStore(appName)
	if err != nil {
		return ""
	}
	state, _ := store.read()
	return autoStartCommand(state)
}

func IsAutoStartEnabled(appName string) bool { return AutoStartCommand(appName) != "" }

type windowsAutoStartStore struct {
	appName, taskName, userSID string
}

func newWindowsAutoStartStore(appName string) (*windowsAutoStartStore, error) {
	if strings.TrimSpace(appName) == "" || strings.ContainsAny(appName, "\\/:*?\"<>|\x00") {
		return nil, errors.New("invalid autostart application name")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid := user.User.Sid.String()
	return &windowsAutoStartStore{appName: appName, taskName: appName + "-" + sid, userSID: sid}, nil
}

func (s *windowsAutoStartStore) read() (autoStartState, error) {
	var state autoStartState
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err == nil {
		state.command, _, err = key.GetStringValue(s.appName)
		_ = key.Close()
	}
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return state, err
	}
	state.taskXML, err = readLogonTask(s.taskName)
	return state, err
}

func (s *windowsAutoStartStore) writeCommand(command string) error {
	if command == "" {
		key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer key.Close()
		err = key.DeleteValue(s.appName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(s.appName, command)
}

func (s *windowsAutoStartStore) writeTask(definition string) error {
	return writeLogonTask(s.taskName, s.userSID, definition)
}

func (s *windowsAutoStartStore) deleteTask() error { return deleteLogonTask(s.taskName) }

func ExePath() (string, error) { return os.Executable() }

func HelperFlagName() string { return startupHelperFlagName }

func RunElevatedHelper(action, appName, configPath string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("管理员自启动辅助进程未获得提升权限")
	}
	if action != startupHelperInstall && action != startupHelperRemove {
		return fmt.Errorf("未知的自启动辅助操作 %q", action)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	enable := action == startupHelperInstall
	return SetAutoStart(appName, executable, configPath, enable, enable)
}

func runElevatedAutoStartHelper(configPath, action string) error {
	if action != startupHelperInstall && action != startupHelperRemove {
		return fmt.Errorf("未知的自启动辅助操作 %q", action)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	if strings.TrimSpace(configPath) != "" {
		configPath, err = filepath.Abs(configPath)
		if err != nil {
			return err
		}
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return err
	}
	parameters := "--" + startupHelperFlagName + "=" + action
	if configPath != "" {
		parameters += " --config " + syscall.EscapeArg(configPath)
	}
	params, err := windows.UTF16PtrFromString(parameters)
	if err != nil {
		return err
	}
	directory, err := windows.UTF16PtrFromString(filepath.Dir(executable))
	if err != nil {
		return err
	}
	info := shellExecuteInfo{
		Size:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		Mask:       shellExecuteMaskNoCloseProcess | shellExecuteMaskNoAsync,
		Verb:       verb,
		File:       file,
		Parameters: params,
		Directory:  directory,
		Show:       shellShowHidden,
	}
	ok, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return errors.New("已取消管理员授权，透明代理自启动未设置")
		}
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return fmt.Errorf("请求管理员权限失败: %w", callErr)
		}
		return errors.New("请求管理员权限失败")
	}
	if info.Process == 0 {
		return errors.New("管理员自启动辅助进程未返回进程句柄")
	}
	defer windows.CloseHandle(info.Process)
	if _, err := windows.WaitForSingleObject(info.Process, windows.INFINITE); err != nil {
		return fmt.Errorf("等待管理员自启动辅助进程失败: %w", err)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(info.Process, &exitCode); err != nil {
		return fmt.Errorf("读取管理员自启动辅助进程结果失败: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("管理员自启动辅助进程失败，退出码 %d", exitCode)
	}
	return nil
}
