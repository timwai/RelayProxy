package bridge

import (
	"context"
	"time"

	"relayproxy/agent/fileshare"
	"relayproxy/internal/protocol"
)

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
	saved, err := store.Save(next)
	if err == nil && b.agent != nil {
		b.agent.RefreshFileDirectUPnP()
	}
	return saved, err
}

func (b *UIBridge) GetFileSharePeers() ([]protocol.FileSharePeer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return b.agent.FileSharePeers(ctx)
}

func (b *UIBridge) GetRemoteFileShares(deviceID string) ([]protocol.FileShareDescriptor, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return b.agent.RemoteFileShares(ctx, deviceID)
}

func (b *UIBridge) GetRemoteFileEntries(deviceID, shareID, relativePath string) ([]protocol.FileShareEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return b.agent.RemoteFileList(ctx, deviceID, shareID, relativePath)
}

func (b *UIBridge) GetFileShareCandidates() ([]protocol.FileShareCandidate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return b.agent.FileShareCandidates(ctx)
}
