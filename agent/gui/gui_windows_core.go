//go:build windows && !wailslegacy

package gui

import "relayproxy/agent/bridge"

// The default Windows build is the headless Go Core used by the WinUI 3
// relay-agent-gui.exe shell. The legacy Wails desktop implementation is only
// compiled when the wailslegacy build tag is explicitly requested.
func Run(_ *bridge.UIBridge, _ Options) error { return ErrUnsupported }

func ShowStartupError(_ error, _ string) {}
func ActivateExistingWindow()                {}
func RequestQuit()                           {}
func RequestRestart() error                  { return ErrUnsupported }
func WaitForRestartParent()                  {}
