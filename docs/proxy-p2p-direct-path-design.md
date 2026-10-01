# RelayProxy Proxy P2P Direct Path Design

> Status: Implementation in progress
>
> Target branch: `main`
>
> Development branch: `feat/proxy-p2p-direct-path`
>
> Scope: RelayProxy proxy traffic between Client Agent and Exit Agent
>
> Goal: Use P2P QUIC when possible while preserving Relay Server as the reliable fallback path.

---

## 1. Background

RelayProxy currently uses the Relay Server as the data path between a Client Agent and an Exit Agent.

Current proxy path:

```text
Client Agent
    |
    | QUIC / TCP+TLS
    v
Relay Server
    |
    | QUIC / TCP+TLS
    v
Exit Agent
    |
    v
Target Network / Internet
```

Routing already decides:

```text
DIRECT / PROXY / REJECT
```

For `PROXY`, the effective Exit selection is:

```text
Rule Exit
    |
    | if empty
    v
Default Exit
    |
    | if empty
    v
Unique authorized online Exit
```

The purpose of this design is to optimize only the transport between the Client and the selected Exit.

P2P must not change which Exit is selected.

---

## 2. Design Principles

The design follows four core rules:

1. Routing decides **where traffic goes**.
2. Transport decides **how traffic reaches that Exit**.
3. P2P failure must not break normal proxy traffic.
4. The Relay Server remains the control plane and fallback data plane.

The logical model is:

```text
Routing Engine
      |
      +-- DIRECT -> local network
      |
      +-- REJECT -> blocked
      |
      +-- PROXY
             |
             v
         Select Exit
             |
             v
      Exit Path Selector
         |         |
         |         |
        P2P      Relay
```

---

## 3. Terminology

### 3.1 DIRECT

`DIRECT` keeps its existing meaning:

```text
Application
    |
    v
Local network stack
    |
    v
Destination
```

No Exit Agent is involved.

### 3.2 PROXY

`PROXY` means traffic is sent through an Exit Agent.

The transport path may be either:

```text
P2P QUIC
```

or:

```text
Relay QUIC / Relay TLS
```

### 3.3 P2P

P2P means the Client Agent and the selected Exit Agent communicate directly.

It does **not** mean routing action `DIRECT`.

Recommended terminology:

```text
Routing Action:
- DIRECT
- PROXY
- REJECT

Proxy Path:
- P2P_QUIC
- RELAY_QUIC
- RELAY_TLS
```

---

## 4. Target Architecture

```text
                         Relay Server
                    +--------------------+
                    | Auth / Approval    |
                    | Exit Discovery     |
                    | P2P Coordinator    |
                    | Rendezvous         |
                    | Lease / Revoke     |
                    | Relay Fallback     |
                    +--------+-----------+
                             |
                control      |      control
                             |
                    +--------+--------+
                    |                 |
               Client Agent      Exit Agent
                    |                 |
                    | UDP Punching    |
                    +---------------->|
                    |<----------------+
                    |                 |
                    +=================+
                    |    P2P QUIC     |
                    +=================+
                    |                 |
                    |                 +--> Exit ACL
                    |                 +--> Upstream Proxy
                    |                 +--> Internet
```

When P2P is unavailable:

```text
Client -> Relay Server -> Exit
```

---

## 5. Exit Selection

P2P must always use the Exit already selected by the routing layer.

Priority:

```text
1. Rule Exit
2. Default Exit
3. Unique authorized online Exit
```

Example:

```text
Default Exit: Guangzhou

github.com      -> Hong Kong Exit
*.company.com   -> Shanghai Exit
everything else -> Default Exit
```

Result:

```text
github.com
-> Hong Kong
-> try Client <-> Hong Kong P2P
-> fallback Relay -> Hong Kong

api.company.com
-> Shanghai
-> try Client <-> Shanghai P2P
-> fallback Relay -> Shanghai

google.com
-> Guangzhou
-> try Client <-> Guangzhou P2P
-> fallback Relay -> Guangzhou
```

P2P must never replace a rule-selected Exit with the default Exit.

---

## 6. Recommended Transport

The first implementation should use:

```text
UDP Hole Punching
        |
        v
QUIC
```

TCP hole punching is not required for the first version.

Why QUIC:

- RelayProxy already uses QUIC.
- NAT traversal works naturally over UDP.
- Multiple TCP flows can share one QUIC session.
- UDP proxy traffic can use QUIC DATAGRAM.
- TLS 1.3 security is already built into QUIC.
- Keepalive and idle management are implemented; established TCP fallback uses the resumable logical-stream overlay instead of relying on QUIC connection migration.

One Exit should normally have one long-lived P2P QUIC session:

```text
Client <==== P2P QUIC ====> Exit
             |
             +-- TCP Flow 1 -> QUIC Stream
             +-- TCP Flow 2 -> QUIC Stream
             +-- TCP Flow 3 -> QUIC Stream
             +-- UDP Flow 1 -> QUIC Datagram
             +-- UDP Flow 2 -> QUIC Datagram
```

Do not perform a new hole punch for every TCP connection.

---

## 7. P2P Session Model

The Client keeps a P2P session cache keyed by Exit ID.

Suggested structure:

```go
map[string]*P2PSession
```

Example:

```text
dev_hongkong  -> READY
dev_shanghai  -> READY
dev_guangzhou -> COOLDOWN
```

Suggested states:

```text
DISCONNECTED
     |
     v
DISCOVERING
     |
     v
RENDEZVOUS
     |
     v
PUNCHING
     |
     v
QUIC_HANDSHAKE
     |
     +----------+
     |          |
     v          |
   READY        |
     |          |
     v          |
 DEGRADED       |
     |          |
     v          |
 COOLDOWN ------+
```

State meanings:

| State | Description |
| --- | --- |
| DISCONNECTED | No P2P session exists |
| DISCOVERING | Collecting local candidates |
| RENDEZVOUS | Exchanging candidates through Server |
| PUNCHING | UDP hole punching |
| QUIC_HANDSHAKE | Establishing authenticated P2P QUIC |
| READY | P2P path usable |
| DEGRADED | Direct path recently failed |
| COOLDOWN | Temporarily suppress P2P retries |

---

## 8. Candidate Discovery

Candidate types:

```text
LAN
IPv6
Reflexive
```

Examples:

```text
LAN:
192.168.1.20:53124

Reflexive:
120.1.2.3:48192

IPv6:
[240e:xxxx::1]:53124
```

Recommended preference:

```text
same LAN
   |
public IPv6
   |
UDP reflexive
   |
Relay
```

### 8.1 Reuse Existing RDP P2P Infrastructure

RelayProxy already contains:

```text
internal/rdp/candidate
internal/rdp/punch
internal/rdp/secure
server/rdp/rendezvous
agent/rdp/p2p
```

These should be generalized rather than duplicated.

Target structure:

```text
internal/p2p/
├── candidate/
├── punch/
├── secure/
├── protocol.go
└── session.go
```

Then both Proxy P2P and RDP P2P can reuse the same primitives.

---

## 9. Rendezvous

Recommended Server configuration:

```yaml
server:
  p2p:
    enabled: true
    rendezvous_listen: ":3478"
    rendezvous_advertise: "relay.example.com:3478"
    lease_sec: 60
```

The port is configurable; `3478` is only an example.

The Agent must probe using the same UDP socket that will later be used for hole punching.

Do not assume the existing Agent-to-Server QUIC socket has a NAT mapping usable for Client-to-Exit P2P.

Correct flow:

```text
P2P Manager
   |
   +-- create UDP socket
   |
   +-- probe rendezvous
   |
   +-- discover reflexive address
   |
   +-- punch peer
   |
   +-- establish QUIC on the same path
```

This is important for symmetric NAT behavior.

---

## 10. UDP Hole Punching

The Server exchanges validated candidates between Client and Exit.

Example:

```text
Client reflexive:
120.1.1.1:41001

Exit reflexive:
58.1.1.1:51001
```

Both peers send authenticated punch packets:

```text
Client                         Exit
   |                            |
   | Punch -------------------> |
   | <------------------- Punch |
   |                            |
   | <------ PunchAck --------> |
```

The existing RDP implementation already includes:

- candidate racing
- short retry interval
- HMAC authenticated punch packets
- nonce validation
- UDP keepalive support
- replay protection for data packets

These primitives should be moved into the generic P2P layer.

---

## 11. QUIC Role Selection

To avoid role negotiation complexity:

```text
Client Agent = QUIC Client
Exit Agent   = QUIC Server
```

After successful hole punching:

```text
Client Agent
    |
    | QUIC / TLS 1.3
    v
Exit Agent
```

Only one QUIC session should normally exist per Client/Exit pair.

---

## 12. Security Model

P2P must preserve the same authorization guarantees as Relay mode.

### 12.1 Server Authorization

A P2P session is allowed only when:

```text
Client is approved
Exit is approved
same owner / authorized relationship
Client has proxy client capability
Exit has proxy exit capability
both advertise proxy_p2p_v1
```

### 12.2 Session Token

The Server creates:

```text
SessionID
SessionToken
ExpiresAt
```

Recommended token size:

```text
256-bit random
```

The token remains memory-only.

Punch packets use:

```text
HMAC(SessionToken, SessionID + Nonce + MessageType)
```

### 12.3 QUIC Identity Binding

Recommended approach:

1. Client and Exit each generate an ephemeral TLS identity.
2. Fingerprints are exchanged through the already authenticated Relay control channel.
3. The QUIC handshake verifies the expected peer fingerprint.

This prevents a third party from taking over a discovered UDP mapping.

### 12.4 No Persistent Secret Storage

Do not persist:

```text
P2P session token
ephemeral certificate private key
reflexive candidate
NAT port mapping
```

to SQLite.

---

## 13. Lease and Revocation

A P2P path must still depend on the Server control plane.

Recommended values:

```text
Lease: 60s
Renew: 20s
```

Flow:

```text
Server authorizes session for 60s
        |
Client <------ P2P ------> Exit
        |
renew every 20s
```

If the Server cannot renew the lease:

```text
lease expires
    |
    v
close P2P session
```

When an administrator:

- revokes a device
- revokes `proxy_exit`
- changes ownership
- removes approval

the Server sends:

```text
P2P_REVOKE
```

Both peers must immediately close the direct session.

---

## 14. Protocol Capability

Add a new capability:

```text
proxy_p2p_v1
```

P2P is available only if both peers advertise it.

Compatibility:

```text
new Server + old Client + new Exit -> Relay
new Server + new Client + old Exit -> Relay
new Server + new Client + new Exit -> P2P eligible
```

This allows rolling upgrades.

---

## 15. Control Messages

Recommended control messages:

```text
P2P_CONNECT_REQUEST
P2P_CONNECT_OFFER
P2P_CONNECT_ANSWER
P2P_CANDIDATE_UPDATE
P2P_LEASE_RENEW
P2P_LEASE_ACK
P2P_CLOSE
P2P_REVOKE
P2P_PATH_REPORT
```

### 15.1 Connect Request

Client -> Server:

```json
{
  "type": "connect_request",
  "exitDeviceId": "dev_hongkong",
  "protocol": "quic",
  "candidates": [
    {
      "type": "lan",
      "protocol": "udp",
      "address": "192.168.1.20:53221"
    },
    {
      "type": "reflexive",
      "protocol": "udp",
      "address": "120.1.2.3:48221"
    }
  ],
  "certFingerprint": "..."
}
```

### 15.2 Exit Offer

Server -> Exit:

```json
{
  "sessionId": 12345,
  "clientDeviceId": "dev_client",
  "sessionToken": "...",
  "candidates": [],
  "certFingerprint": "...",
  "expiresAt": 123456789
}
```

### 15.3 Exit Answer

Exit -> Server -> Client:

```json
{
  "sessionId": 12345,
  "exitDeviceId": "dev_exit",
  "candidates": [],
  "certFingerprint": "..."
}
```

---

## 16. TCP Proxy Data Path

The existing proxy protocol should be reused.

Current logical flow:

```text
Client
  |
Open Stream
  |
StreamHeader(OpenTCP)
  |
OpenTCPRequest
  |
Relay
  |
Exit
```

P2P path:

```text
Client
  |
P2P QUIC OpenStream()
  |
StreamHeader(OpenTCP)
  |
OpenTCPRequest
  |
Exit
```

The data protocol should remain the same.

Only the transport changes.

---

## 17. Exit Handler Refactor

The Exit side should have a transport-independent handler.

Suggested abstraction:

```text
ExitStreamHandler
├── HandleOpenTCP
└── HandleOpenUDP
```

Inputs may come from:

```text
Relay TunnelSession
P2P TunnelSession
```

The following behavior must remain identical for Relay and P2P:

- Exit ACL
- private-network access rules
- loopback rules
- Internet access rules
- upstream proxy selection
- DNS behavior
- logging
- traffic accounting

P2P must never bypass Exit policy checks.

---

## 18. UDP Proxy Data Path

When P2P QUIC supports native datagrams:

```text
Client
  |
QUIC DATAGRAM
  |
Exit
  |
UDP
  |
Target
```

Fallback:

```text
UDP-over-stream
```

Existing `datagram_required` behavior must remain unchanged.

If:

```text
datagram_required = true
```

and no native datagram path is available:

```text
return an error
```

Do not silently downgrade to reliable stream transport.

---

## 19. Adaptive Exit Dialer

Current structure:

```text
RoutingDialer
     |
TunnelDialer
     |
Relay
```

Target structure:

```text
RoutingDialer
     |
AdaptiveExitDialer
     |
     +-- P2PExitDialer
     |
     +-- RelayTunnelDialer
```

Suggested interface:

```go
type ExitDialer interface {
    proxy.TunnelDialer

    GetDefaultExitID() string
    SetDefaultExitID(string)
}
```

The routing layer should not know whether the selected transport is:

```text
P2P
Relay QUIC
Relay TLS
```

It only outputs:

```text
Action
ExitID
```

---

## 20. P2P Selection Policy

Default mode:

```text
auto
```

Recommended behavior:

```text
if P2P session READY:
    use P2P
else:
    use Relay immediately
    start/continue P2P establishment in background
```

The first user connection must not wait for hole punching.

Example:

```text
Connection #1 -> Relay immediately
background     -> establish P2P
Connection #2 -> P2P
Connection #3 -> P2P
```

This prevents P2P from making first-use latency worse.

---

## 21. Default Exit Prewarming

After the Client is authenticated and knows the default Exit:

```text
if P2P enabled:
    establish P2P to default Exit in background
```

Rule-specific Exits are initialized on first use.

This makes the common path ready before application traffic arrives.

---

## 22. Multiple Exit Sessions

Suggested default:

```text
max active P2P Exit sessions = 4
```

Use an LRU policy.

Example:

```text
Hong Kong  READY
Shanghai   READY
Guangzhou  READY
Tokyo      IDLE
```

Idle sessions may be closed after:

```text
120-300 seconds
```

This is especially important for Android Exit devices to reduce battery and NAT keepalive cost.

---

## 23. Keepalive

Suggested defaults:

```text
P2P keepalive: 10-15s
idle timeout: 120s
```

Keepalive responsibilities:

- preserve NAT mappings
- detect dead paths
- detect network rebinding

Battery-aware behavior:

- normal mode keeps the configured P2P keepalive and idle timeout
- power-constrained mode suppresses default-Exit prewarming
- power-constrained mode retains at most one cached Client -> Exit P2P session
- newly established power-constrained QUIC paths disable periodic keepalive and
  use a shorter idle timeout so Android does not keep waking the radio only to
  preserve an unused NAT mapping
- the idle reaper never closes a session with active QUIC streams
- reactive P2P attempts are still allowed while power-constrained, so active
  application traffic can obtain a direct path instead of being permanently
  forced through Relay

The generic Agent exposes `SetP2PPowerConstrained(bool)` for platform lifecycle
integration. The Android exit client is now integrated into this branch and
exposes the same policy through gomobile. Its foreground service enables the
low-power profile when Android Power Saver or device-idle mode is active, when
the screen is not interactive, or when the selected exit network is cellular.
The profile is relaxed automatically when those conditions clear.

---

## 24. Failure Cache and Cooldown

Do not retry failed P2P establishment on every application connection.

Suggested backoff:

```text
1st failure -> 5s
2nd failure -> 30s
3rd failure -> 2m
repeated    -> 10m
```

During cooldown:

```text
use Relay immediately
```

Network changes should reset cooldown.

Examples:

- Wi-Fi -> cellular
- Wi-Fi -> hotspot
- VPN connected/disconnected
- interface address changed

On network change:

```text
close old P2P
clear candidate cache
clear cooldown
rediscover
repunch
```

---

## 25. Existing TCP Connections During P2P Failure

RelayProxy now performs **safe pre-stream failover** before returning a new
connection to the application. If a READY P2P session fails while opening the
QUIC stream, writing the OpenTCP/OpenUDP request, or reading its response, the
Client:

```text
1. records the P2P -> Relay fallback
2. quarantines/removes the broken READY P2P session
3. enters the existing P2P cooldown
4. retries the new connection through Relay
```

ACL/business errors are not treated as transport failures and are never retried
through Relay to bypass Exit policy.

Established TCP streams now have a capability-gated recovery path. When both
authenticated peers advertise `proxy_stream_resume_v1`, a direct P2P TCP stream
uses a logical stream ID, byte-offset ACKs and bounded replay. If the P2P QUIC
transport dies after application bytes have been exchanged:

```text
negotiated resumable TCP stream -> rebind through Relay
legacy / unsupported TCP stream -> fail/reset
new connections                 -> Relay
```

The Exit retains the original target socket during a bounded recovery grace
period, so a successful rebind does not redial the destination and does not
change the remote TCP session. Replay is de-duplicated by byte offset before
bytes are exposed to the application.

This Version 1 migration is deliberately one-way: `P2P -> Relay` on direct-path
loss. Relay-originated streams remain the legacy raw stream format and are not
migrated back to P2P mid-flow. This keeps normal Relay traffic free of resume
framing/replay overhead and preserves mixed-version compatibility.

The production gate is stricter than merely advertising the capability:

- the Server derives peer capabilities from authenticated DeviceSession state;
- the capability is attached to the concrete P2P QUIC session;
- the Client enables resume only in automatic mode with Relay fallback enabled;
- the Exit keeps resumable target sessions in a bounded registry;
- resumable frame ACKs use a coalesced writer so simultaneous full-duplex DATA
  cannot deadlock both peers on control writes;
- a detached Exit session accepts an authenticated idempotent retry of its
  current generation, covering a lost rebind response without permitting an
  active or older generation to take ownership;
- normal Close sends a logical reset, while half-close uses replayable FIN/ACK;
- logical read/write deadlines continue to follow `net.Conn` semantics.

---

## 26. Server Availability

P2P payload may bypass the Server, but the Server remains authoritative.

If the Server connection disappears:

```text
existing P2P may continue only until lease expiry
```

After lease expiry:

```text
close direct session
reconnect control plane
```

This preserves revocation and authorization guarantees.

---

## 27. Configuration

### 27.1 Agent

Recommended:

```yaml
p2p:
  enabled: true

  # auto | relay_only | p2p_only
  mode: auto

  punch_timeout_ms: 1200
  keepalive_sec: 10
  idle_timeout_sec: 120

  max_exit_sessions: 4
  fallback: true
```

Normal GUI users should only need:

```text
P2P Direct Path
[Automatic]
```

Advanced settings may expose the remaining options.

### 27.2 Server

```yaml
server:
  p2p:
    enabled: true
    rendezvous_listen: ":3478"
    rendezvous_advertise: "relay.example.com:3478"

    lease_sec: 60
    max_sessions_per_device: 8
```

---

## 28. Optional Per-Rule Path Policy

The first version does not need a routing-rule P2P field.

Default:

```text
PROXY -> P2P when ready -> Relay fallback
```

A later advanced option can add:

```yaml
path: auto
```

Values:

```text
auto
relay_only
p2p_only
```

Example:

```yaml
- name: company-network
  action: proxy
  exit_id: company-exit
  path: relay_only
```

---

## 29. Unique Exit Resolution

Today the Server may auto-select an Exit if exactly one authorized Exit is online.

For P2P, the Client must know the concrete Exit ID before rendezvous begins.

Therefore the Server should synchronize the authorized Exit list to the Client.

Example:

```json
[
  {
    "deviceId": "dev_a",
    "online": true,
    "p2p": true
  }
]
```

The Client can then locally resolve:

```text
no rule Exit
no default Exit
exactly one authorized Exit
-> select that Exit
```

Relay and P2P then use identical Exit selection semantics.

---

## 30. GUI Design

### 30.1 Overview

Add:

```text
Current Exit
Hong Kong

Path
P2P QUIC

Direct RTT
24 ms
```

Fallback example:

```text
Path
Relay QUIC

P2P
Punch failed · Relay fallback active
```

### 30.2 Network Page

Suggested section:

```text
P2P Direct Path
--------------------------------

Mode
Automatic

Current Exit
Hong Kong

Status
P2P Connected

Local Candidate
192.168.1.20:52133

Reflexive Candidate
120.x.x.x:48221

Peer Candidate
58.x.x.x:32811

RTT
24 ms
```

Session tokens and private key material must never be displayed.

### 30.3 Live Connections

Add a `Path` field:

```text
Action: PROXY
Rule: GitHub
Exit: Hong Kong
Path: P2P QUIC
```

Possible values:

```text
DIRECT
P2P QUIC
Relay QUIC
Relay TLS
```

This is important for testing and diagnostics.

---

## 31. Server Web

Device detail may display:

```text
P2P Supported: yes
Candidates: LAN + Reflexive
Active P2P Sessions: 2
```

Server overview metrics:

```text
Active P2P sessions
P2P bytes
Relay bytes
P2P fallback count
Punch success rate
```

---

## 32. Traffic Accounting

P2P payload no longer passes through the Relay Server.

Therefore the Server cannot infer P2P byte counts directly.

Client and Exit should periodically report aggregated telemetry over the control channel.

Suggested interval:

```text
5-10 seconds
```

Example:

```json
{
  "exitId": "dev_exit",
  "path": "p2p_quic",
  "activeStreams": 12,
  "bytesUp": 123456,
  "bytesDown": 654321
}
```

Telemetry is for monitoring only and must not participate in the forwarding path.

---

## 33. Logging

Suggested Agent logs:

```text
[P2P] exit=dev_xxx state=discovering
[P2P] exit=dev_xxx reflexive=120.x.x.x:48123
[P2P] exit=dev_xxx state=punching candidates=3
[P2P] exit=dev_xxx state=ready rtt=24ms
[P2P] exit=dev_xxx path=p2p_quic
[P2P] exit=dev_xxx failed="punch timeout" fallback=relay
```

Never log:

- SessionToken
- private keys
- full authentication secrets

---

## 34. Expected NAT Behavior

| Network | Expected Result |
| --- | --- |
| Same LAN | Very likely P2P |
| Public IPv6 | Very likely direct |
| Typical home NAT | High P2P success |
| Port restricted NAT | Usually possible |
| Symmetric NAT | May fail |
| CGNAT + symmetric NAT | Often fails |
| UDP blocked | P2P unavailable |
| Enterprise firewall | Depends on policy |

Failure is acceptable because Relay remains the fallback.

---

## 35. TURN

A separate TURN service is not required for the first implementation.

RelayProxy already provides the fallback data path:

```text
P2P success -> Client <-> Exit
P2P failure -> Client -> Relay Server -> Exit
```

Functionally, the Relay Server already provides the fallback role required when direct traversal fails.

---

## 36. Proposed Code Layout

```text
internal/p2p/
├── candidate/
├── punch/
├── secure/
├── protocol.go
└── session.go

agent/p2p/
├── manager.go
├── session.go
└── dialer.go

agent/client/
├── dialer.go
└── adaptive_dialer.go

server/p2p/
├── coordinator.go
├── rendezvous.go
└── session.go
```

Existing RDP P2P code should gradually depend on `internal/p2p` rather than maintaining duplicate candidate/punch/security implementations.

---

## 37. Development Plan

### Phase 1 - Generalize Existing P2P Infrastructure

Move reusable RDP primitives:

```text
internal/rdp/candidate -> internal/p2p/candidate
internal/rdp/punch     -> internal/p2p/punch
internal/rdp/secure    -> internal/p2p/secure
```

Keep RDP behavior unchanged.

Acceptance:

- all existing RDP P2P tests pass
- no protocol behavior changes

### Phase 2 - Server P2P Coordinator

Implement:

- capability negotiation
- P2P Session ID/token
- candidate exchange
- lease
- revoke
- rendezvous

No proxy data path yet.

Acceptance:

- two test Agents can receive valid offers/answers
- authorization/revocation tests pass

### Phase 3 - Client/Exit P2P QUIC

Implement:

- P2P UDP socket
- candidate discovery
- reflexive probe
- UDP punch
- QUIC handshake
- peer fingerprint validation
- keepalive
- cooldown

Acceptance:

- LAN direct integration test
- reflexive candidate integration test
- punch timeout cleanly falls back

### Phase 4 - TCP Proxy Direct Path

Enable:

```text
SOCKS5 TCP
HTTP CONNECT
transparent proxy TCP
```

to use P2P.

Acceptance:

- same `OpenTCP` protocol works over Relay and P2P
- Exit ACL behavior is identical
- upstream proxy behavior is identical

### Phase 5 - UDP Proxy Direct Path

Add:

- QUIC DATAGRAM
- UDP associations
- `datagram_required` semantics

Acceptance:

- DNS and arbitrary UDP work
- required-datagram policy is preserved

### Phase 6 - Adaptive Exit Dialer

Implement:

```text
P2P READY -> P2P
otherwise -> Relay immediately
```

Add default Exit prewarm.

Acceptance:

- first connection never waits for P2P
- later connections switch to direct path automatically

### Phase 7 - GUI and Server Web

Add:

- P2P state
- path
- RTT
- candidate summary
- fallback reason
- P2P byte counts
- fallback counts

### Phase 8 - Optimization

Progress as of 2026-10-01:

- [x] IPv6 candidate discovery and dual-stack UDP punching
- [x] OS network-change detection and automatic P2P invalidation/retry
- [x] exponential cooldown after repeated direct-path failures
- [x] LRU session management for ready Client/Exit P2P sessions
- [x] battery-aware P2P core profile
- [x] Android lifecycle/power-saver/cellular-network wiring
- [x] candidate path scoring with bounded RTT preference
- [x] pre-stream transport failover + broken-path quarantine
- [x] live established TCP stream migration from P2P to Relay

The IPv6 implementation keeps Relay as the fallback. Mixed IPv4/IPv6 candidate
sets continue racing even when the local socket cannot use one address family,
so an unsupported family does not abort an otherwise usable direct path.

Path scoring follows the preferred order of reachable private/LAN candidates,
native IPv6 and reflexive UDP, while using observed punch latency to choose
between candidates in the same class. A short selection window avoids delaying
Relay fallback or direct-path establishment for a slow preferred candidate.
Punch request/ack state is tracked per remote address so two different
candidates cannot be combined into a false successful path.

---

## 38. Test Plan

### 38.1 Unit Tests

Required areas:

- candidate validation
- reflexive probing
- punch HMAC verification
- replay protection
- lease expiration
- capability negotiation
- Exit selection
- cooldown state machine
- route Exit precedence

### 38.2 Integration Tests

Scenarios:

1. Client and Exit on same LAN -> P2P
2. P2P disabled -> Relay
3. Client supports P2P, Exit does not -> Relay
4. Exit supports P2P, Client does not -> Relay
5. Punch timeout -> Relay
6. P2P session dies -> new connections use Relay
7. default Exit uses P2P
8. rule Exit overrides default Exit and uses that Exit's P2P session
9. ACL reject behaves the same over Relay and P2P
10. Exit upstream SOCKS5/HTTP behaves the same over P2P
11. revoke Exit grant -> P2P closes immediately
12. Server disconnected -> P2P closes after lease expiry
13. `datagram_required=true` never silently downgrades

### 38.3 Performance Tests

Measure:

- P2P RTT vs Relay RTT
- throughput
- CPU
- memory
- UDP loss
- QUIC datagram performance
- Server bandwidth reduction
- time to establish direct path
- failure fallback latency

---

## 39. Observability

Recommended counters:

```text
p2p_sessions_active
p2p_connect_attempts
p2p_connect_success
p2p_connect_failure
p2p_fallback_total
p2p_bytes_up
p2p_bytes_down
p2p_rtt_ms
p2p_punch_duration_ms
```

Recommended reason codes:

```text
unsupported
disabled
exit_offline
unauthorized
no_candidate
punch_timeout
quic_handshake_failed
lease_expired
revoked
network_changed
cooldown
```

---

## 40. Compatibility

The feature must be backward compatible.

Default behavior after Server upgrade:

```text
old Agents -> existing Relay path
new Agents without P2P enabled -> existing Relay path
new Client + new Exit + P2P enabled -> direct path eligible
```

Existing routing configuration must not require migration.

---

## 41. Non-Goals for Version 1

The first version explicitly does not include:

- arbitrary bidirectional live migration between Relay and P2P mid-flow
- TCP NAT hole punching
- independent operation without Relay Server control plane
- persistent P2P credentials
- mandatory P2P-only mode for normal users
- full ICE/STUN/TURN compatibility
- path bonding or multipath aggregation

---

## 42. Recommended Default Behavior

The normal user experience should remain simple.

GUI:

```text
P2P Direct Path
Automatic
```

Internal behavior:

```text
route connection
      |
      v
select Exit
      |
      v
P2P READY?
   |       |
  yes      no
   |       |
   v       +--> use Relay immediately
 use P2P        |
               +--> establish P2P in background
```

This gives RelayProxy a direct path without making the existing reliable Relay architecture less reliable.

---

## 43. Final Architecture Summary

```text
Application
    |
    v
Routing Engine
    |
    +-------------------+------------------+
    |                   |                  |
  DIRECT              PROXY              REJECT
    |                   |
    v                   v
Local Network       Select Exit
                        |
                        v
                AdaptiveExitDialer
                  |            |
                  |            |
              P2P QUIC       Relay
                  |            |
                  +------v-----+
                         |
                       Exit
                         |
                   Exit ACL / Policy
                         |
                   Upstream / Direct
                         |
                      Internet
```

The key rule remains:

> Routing chooses the Exit. P2P only changes the path used to reach that Exit.
