package divert

import (
	"net/netip"
	"time"
)

// Shard-local and short-lived: never retain a PID/path for an entire UDP
// association. Revalidate idle/reused endpoints, and do not cache lookup misses.
const udpProcessCacheTTL = 250 * time.Millisecond
const udpProcessCacheEntries = 256

type udpProcessEntry struct {
	process packetProcess
	expires time.Time
}

type udpProcessCache struct {
	resolve processLookup
	entries map[FlowKey]udpProcessEntry
}

func newUDPProcessCache(resolve processLookup) *udpProcessCache {
	return &udpProcessCache{resolve: resolve, entries: make(map[FlowKey]udpProcessEntry)}
}

func (c *udpProcessCache) lookup(source, destination netip.AddrPort, now time.Time) packetProcess {
	key := FlowKey{Protocol: ProtoUDP, Source: source, Destination: destination}
	if entry, ok := c.entries[key]; ok && now.Before(entry.expires) {
		return entry.process
	}
	delete(c.entries, key)
	process, err := c.resolve(ProtoUDP, source, destination)
	if err != nil {
		return packetProcess{}
	}
	if process.pid != 0 {
		if len(c.entries) >= udpProcessCacheEntries {
			for old := range c.entries {
				delete(c.entries, old)
				break
			}
		}
		c.entries[key] = udpProcessEntry{process: process, expires: now.Add(udpProcessCacheTTL)}
	}
	return process
}

// Classification has its own queues. A forwarding worker may be waiting for a
// QUIC dial whose own UDP packets also need process attribution and self-bypass;
// sharing a worker would make that dial depend on itself.
func (i *packetInterceptor) classifyDatagrams(queue <-chan interceptedUDP) {
	defer i.wg.Done()
	owners := newUDPProcessCache(i.lookup)
	for {
		select {
		case <-i.ctx.Done():
			return
		case job := <-queue:
			if i.ctx.Err() != nil {
				return
			}
			packet := *job.packet
			process := owners.lookup(packet.Source, packet.Destination, time.Now())
			if i.ctx.Err() != nil {
				return
			}
			route, err := i.classifyUDP(packet, job.meta, process)
			if route != nil && err == nil {
				err = i.queueUDP(route.Key(), interceptedUDP{route: route, payload: packet.Payload, meta: job.meta})
			}
			if i.disableOnInjectionError(err) {
				i.report(err)
				return
			}
			i.report(err)
		}
	}
}
