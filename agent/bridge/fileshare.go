package bridge

import "relayproxy/agent/fileshare"

// File sharing state is persisted separately from the Agent YAML; the Web and
// native UI both call this bridge, so neither may bypass local validation.
func (b *UIBridge) GetFileShares() (fileshare.State, error) {
	store, err := fileshare.New(b.rawConfigPath())
	if err != nil {
		return fileshare.State{}, err
	}
	return store.Load()
}

func (b *UIBridge) SaveFileShares(next fileshare.State) (fileshare.State, error) {
	store, err := fileshare.New(b.rawConfigPath())
	if err != nil {
		return fileshare.State{}, err
	}
	return store.Save(next)
}
