package bridge

import "relayproxy/internal/traffic"

func (b *UIBridge) GetConnections() traffic.Snapshot { return b.agent.Connections() }

func (b *UIBridge) ClearConnections() { b.agent.ClearConnections() }
