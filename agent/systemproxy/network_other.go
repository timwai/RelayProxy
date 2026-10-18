//go:build !darwin
package systemproxy

import "fmt"

type unsupportedBackend struct{}
func newBackend() backend { return unsupportedBackend{} }
func (unsupportedBackend) CurrentService() (string, error) { return "", fmt.Errorf("macOS-only backend") }
func (unsupportedBackend) Read(service, kind string) (ProxyState, error) { return ProxyState{}, fmt.Errorf("macOS-only backend") }
func (unsupportedBackend) Write(service, kind string, state ProxyState) error { return fmt.Errorf("macOS-only backend") }
func (unsupportedBackend) Validate(service string) error { return fmt.Errorf("macOS-only backend") }
