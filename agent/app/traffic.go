package app

import "relayproxy/internal/traffic"

func (a *Agent) Connections() traffic.Snapshot { return a.traffic.Snapshot() }
