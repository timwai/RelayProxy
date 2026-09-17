//go:build !windows

package main

// detachConsole is a no-op off Windows; the concept of an attached console
// window does not exist on the supported Unix targets.
func detachConsole() {}
