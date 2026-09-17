//go:build windows

package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"relayproxy/agent/app"
	"relayproxy/agent/bridge"
)

// Opt-in integration check: a separate desktop keeps the test's native windows
// off the user's screen. It starts no agent listeners, tunnel or driver.
func TestConnectionsNativeWindowLifecycle(t *testing.T) {
	if os.Getenv("RELAYPROXY_GUI_SMOKE") != "1" {
		t.Skip("set RELAYPROXY_GUI_SMOKE=1 for isolated native WebView2 validation")
	}
	profile, err := filepath.Abs("../../.smoke/native-monitor-profile")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", profile)
	if os.Getenv("RELAYPROXY_GUI_SMOKE_CHILD") != "1" {
		runConnectionsSmokeOnDesktop(t)
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	agent, err := app.NewAgent(app.AgentConfig{Mode: "CLIENT"})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	a := &appWindow{bridge: bridge.NewUIBridge(agent, ""), stopCh: make(chan struct{})}
	m := &connectionWindow{owner: a, dark: true, done: make(chan struct{})}
	a.monitor = m
	go m.run()
	defer a.closeConnections()
	waitFor := func(description string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout: %s", description)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("native monitor creation", func() bool { return m.hwnd.Load() != 0 })
	hwnd := win.HWND(m.hwnd.Load())
	if !win.IsWindowVisible(hwnd) {
		t.Fatal("monitor did not open")
	}
	win.PostMessage(hwnd, win.WM_CLOSE, 0, 0)
	waitFor("close hides monitor", func() bool { return !win.IsWindowVisible(hwnd) })
	select {
	case <-m.done:
		t.Fatal("closing monitor terminated its reusable view")
	default:
	}
	a.openConnections()
	waitFor("reopen existing monitor", func() bool { return win.IsWindowVisible(hwnd) })
	a.monitorMu.Lock()
	same := a.monitor == m
	a.monitorMu.Unlock()
	if !same {
		t.Fatal("reopen allocated another monitor")
	}
	a.stopStatusLoop()
	a.closeConnections()
	select {
	case <-m.done:
	default:
		t.Fatal("monitor did not end with its owner")
	}
	if exists, _, _ := user32.NewProc("IsWindow").Call(uintptr(hwnd)); exists != 0 {
		t.Fatal("native monitor window leaked after shutdown")
	}
}

func runConnectionsSmokeOnDesktop(t *testing.T) {
	t.Helper()
	// WebView2 starts browser subprocesses. The whole test process must start
	// on this desktop so those subprocesses inherit the same desktop as the
	// controller, rather than only moving the controller's current thread.
	user32 := windows.NewLazySystemDLL("user32.dll")
	name := fmt.Sprintf("RelayProxyMonitorTest%d", time.Now().UnixNano())
	desktop, _, err := user32.NewProc("CreateDesktopW").Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(name))), 0, 0, 0, 0x01ff, 0)
	if desktop == 0 {
		t.Fatalf("create isolated desktop: %v", err)
	}
	defer user32.NewProc("CloseDesktop").Call(desktop)
	logPath := filepath.Join(t.TempDir(), "native-monitor.log")
	output, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	handle := windows.Handle(output.Fd())
	if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAYPROXY_GUI_SMOKE_CHILD", "1")
	command := windows.StringToUTF16Ptr(windows.EscapeArg(exe) + " -test.run=^TestConnectionsNativeWindowLifecycle$ -test.v -test.timeout=45s")
	startup := windows.StartupInfo{Desktop: windows.StringToUTF16Ptr(name), Flags: windows.STARTF_USESTDHANDLES, StdOutput: handle, StdErr: handle}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	var child windows.ProcessInformation
	if err := windows.CreateProcess(nil, command, nil, nil, true, windows.CREATE_NO_WINDOW, nil, nil, &startup, &child); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child.Process)
	defer windows.CloseHandle(child.Thread)
	status, err := windows.WaitForSingleObject(child.Process, 50_000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		_ = windows.TerminateProcess(child.Process, 1)
		t.Fatalf("native smoke process did not finish: status=%d error=%v", status, err)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(child.Process, &exitCode); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if exitCode != 0 {
		t.Fatalf("native monitor smoke process exited with code %d", exitCode)
	}
}
