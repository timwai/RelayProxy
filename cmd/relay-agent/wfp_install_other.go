//go:build !windows

package main

import "errors"

func installWFPDriverCLI() error {
	return errors.New("WFP driver installation is only available on Windows")
}
