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

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

var startupMu sync.Mutex

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
	_, err = applyAutoStart(store, before, desired, windows.GetCurrentProcessToken().IsElevated())
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
	rollback, err := applyAutoStart(store, before, desired, windows.GetCurrentProcessToken().IsElevated())
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
