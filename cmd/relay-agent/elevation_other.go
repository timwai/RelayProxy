//go:build !windows

package main

func waitForElevationParent() {}

func ensureTransparentProxyElevation(string) (bool, error) {
	return false, nil
}
