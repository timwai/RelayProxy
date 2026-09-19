//go:build windows

package main

import "relayproxy/agent/divert"

func installWFPDriverCLI() error {
	return divert.InstallWFPDriver()
}
