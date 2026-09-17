package divert

import "time"

func (i *packetInterceptor) flowMetadata(p ipPacket, process packetProcess) Flow {
	flow := packetFlow(p, process)
	flow.Host = i.dns.lookup(p.Destination.Addr())
	if flow.Host != "" {
		flow.DomainSource = "dns"
	}
	return flow
}

func (i *packetInterceptor) inboundPacket(data []byte, meta packetMetadata) error {
	p, err := parseIPPacket(data)
	if err != nil {
		// Normal inbound fragments and unsupported packets still belong to the
		// OS. Widening capture for telemetry must not blackhole them.
		if accepter, ok := i.device.(packetAccepter); ok {
			return accepter.Accept(meta)
		}
		return i.device.Send(data, meta)
	}
	if p.Protocol == ProtoTCP {
		if port := i.ports[p.Destination.Addr().Is6()]; port != 0 && p.Destination.Port() == port {
			return nil // Only this handle's own reflection injections may enter.
		}
	}
	if err := i.sendPacket(p, meta); err != nil {
		return err
	}
	key := FlowKey{Protocol: p.Protocol, Source: p.Destination, Destination: p.Source}
	if p.Protocol == ProtoTCP {
		i.mu.Lock()
		flow := i.tcp[key]
		i.mu.Unlock()
		if flow != nil && flow.route.Decision().Action == ActionDirect {
			i.trackDirectTCP(flow, p, false)
		}
		return nil
	}
	i.dns.response(p.Source, p.Destination, p.Payload)
	i.server.mu.Lock()
	association := i.server.udp[key]
	if association != nil && association.route.Decision().Action == ActionDirect {
		association.touch(time.Now())
		association.route.traffic.Activate()
		association.route.traffic.AddDownload(len(p.Payload))
	}
	i.server.mu.Unlock()
	return nil
}

func (i *packetInterceptor) trackDirectTCP(flow *tcpRedirect, p ipPacket, outbound bool) {
	if outbound {
		flow.route.traffic.AddUpload(len(p.Payload))
	} else {
		flow.route.traffic.AddDownload(len(p.Payload))
	}
	if p.TCPFlags&0x10 != 0 {
		flow.route.traffic.Activate()
	}
	i.mu.Lock()
	flow.lastSeen = time.Now()
	if p.TCPFlags&0x01 != 0 {
		if outbound {
			flow.sentFIN = true
		} else {
			flow.receivedFIN = true
		}
	}
	finished := p.TCPFlags&0x04 != 0 || (flow.sentFIN && flow.receivedFIN)
	if finished {
		flow.finished = time.Now()
	}
	i.mu.Unlock()
	if finished {
		flow.route.traffic.Finish("closed", nil)
	}
}

// Recheck idle DIRECT sockets against the OS instead of expiring an established
// but quiet connection. Owner lookup happens outside the packet-map lock.
func (i *packetInterceptor) sweepDirectTCP(now time.Time) {
	type candidate struct {
		key  FlowKey
		flow *tcpRedirect
		seen time.Time
	}
	var candidates []candidate
	i.mu.Lock()
	for key, flow := range i.tcp {
		if flow.route.Decision().Action == ActionDirect && flow.finished.IsZero() && now.Sub(flow.lastSeen) > 2*time.Minute {
			candidates = append(candidates, candidate{key, flow, flow.lastSeen})
		}
	}
	i.mu.Unlock()
	for _, candidate := range candidates {
		if i.ctx.Err() != nil {
			return
		}
		process, err := i.lookup(ProtoTCP, candidate.key.Source, candidate.key.Destination)
		metadata := candidate.flow.route.Metadata()
		if err == nil && process.pid == metadata.ProcessID && process.path == metadata.Process {
			continue
		}
		i.mu.Lock()
		if i.tcp[candidate.key] == candidate.flow && candidate.flow.lastSeen.Equal(candidate.seen) {
			delete(i.tcp, candidate.key)
			candidate.flow.route.traffic.Finish("closed", nil)
		}
		i.mu.Unlock()
	}
}
