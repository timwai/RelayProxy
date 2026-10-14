//go:build windows

package gui

import "relayproxy/agent/fileshare"

// Native Wails callers use exactly the same bridge validation as local Web.
func (s *WailsService) GetFileShares() (fileshare.State, error) {
	return s.owner.bridge.GetFileShares()
}
func (s *WailsService) SaveFileShares(state fileshare.State) (fileshare.State, error) {
	return s.owner.bridge.SaveFileShares(state)
}
