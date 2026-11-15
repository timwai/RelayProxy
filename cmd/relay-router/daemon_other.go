//go:build !linux

package main

import "fmt"

func maybeDaemonize(_ []string, _ string) (bool, error) {
	return false, fmt.Errorf("--daemon is currently supported on Linux only")
}
