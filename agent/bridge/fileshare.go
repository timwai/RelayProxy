package bridge

import (
	"context"
	"log"
	"time"

	"relayproxy/agent/app"
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
	start := time.Now()
	shares, err := b.agent.RemoteFileShares(ctx, deviceID)
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond || err != nil {
		log.Printf("[FileBrowse] shares total_ms=%d count=%d failed=%t", elapsed.Milliseconds(), len(shares), err != nil)
	}
	return shares, err
}

func (b *UIBridge) GetRemoteFileEntries(deviceID, shareID, relativePath string) ([]protocol.FileShareEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	entries, err := b.agent.RemoteFileList(ctx, deviceID, shareID, relativePath)
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond || err != nil {
		log.Printf("[FileBrowse] list total_ms=%d count=%d failed=%t", elapsed.Milliseconds(), len(entries), err != nil)
	}
	return entries, err
}

func (b *UIBridge) MutateRemoteFile(deviceID, shareID, action, relativePath, destination string) error {
 ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
 defer cancel()
 return b.agent.MutateRemoteFile(ctx, deviceID, shareID, action, relativePath, destination)
}

func (b *UIBridge) GetFileShareCandidates() ([]protocol.FileShareCandidate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return b.agent.FileShareCandidates(ctx)
}

func (b *UIBridge) GetFileDirectStatus() app.FileDirectStatus {
	if b == nil || b.agent == nil {
		return app.FileDirectStatus{MappingState: "disabled"}
	}
	return b.agent.FileDirectStatus()
}
