# RelayProxy Public Direct Path Design

> Status: Planning
>
> Target branch: `main`
>
> Scope: RelayProxy proxy traffic between Client Agent and Exit Agent
>
> Related design: `docs/proxy-p2p-direct-path-design.md`
>
> Goal: When an Exit Agent has a verified publicly reachable endpoint, allow Client Agents to connect to it directly without P2P hole punching, while preserving P2P and Relay as fallback paths.

---

## 1. Background

RelayProxy currently has two proxy data paths between a Client Agent and an Exit Agent:

```text
1. P2P QUIC

Client Agent -----------------------> Exit Agent
                direct QUIC
                after P2P signaling,
                candidate exchange,
                UDP punching

2. Relay

Client Agent ---> Relay Server ---> Exit Agent
```

The existing P2P implementation is appropriate for NAT traversal, but it introduces work that is unnecessary when an Exit Agent is already publicly reachable.

For example, a cloud VM, a host with a public IPv4 address, or a host with globally reachable IPv6 does not need:

```text
connect_request
connect_offer
connect_answer
candidate exchange
UDP hole punching
```

before a direct QUIC connection can be attempted.

The new capability therefore introduces a distinct proxy transport path:

```text
PUBLIC_DIRECT_QUIC
```

This is not a new routing action. Routing remains unchanged.

---

## 2. Design Principles

The design follows these rules:

1. Routing continues to decide **which Exit is selected**.
2. The path layer decides **how the Client reaches that Exit**.
3. Public direct is independent of P2P NAT traversal.
4. The Relay Server remains the authenticated control plane.
5. A publicly reachable Exit must never become an unauthenticated open proxy.
6. Direct-path failure must not break ordinary proxy traffic.
7. Existing TCP and UDP proxy request protocols should be reused unchanged.
8. Existing Exit handlers should not care whether traffic arrived through Public Direct, P2P, or Relay.
9. Public direct availability must be verified by the Server instead of being trusted solely from Agent self-reporting.
10. Public Direct and P2P failures must have independent health and cooldown state.

---

## 3. Terminology

### 3.1 Routing DIRECT

The existing routing action `DIRECT` keeps its current meaning:

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

### 3.2 Proxy direct path

A proxy direct path means the selected Exit Agent is reached without forwarding the data through the Relay Server.

Proxy paths become:

```text
PUBLIC_DIRECT_QUIC
P2P_QUIC
RELAY_QUIC
RELAY_TLS
```

Recommended protocol strings:

```text
public_direct_quic
p2p_quic
relay_quic
relay_tls
```

### 3.3 Public Direct

Public Direct means:

- the Exit Agent owns a direct QUIC listener;
- at least one advertised endpoint is publicly reachable;
- the Server has verified that endpoint;
- the Client is authorized for the Exit;
- the Client presents a Server-issued short-lived authorization ticket;
- no P2P candidate exchange or UDP hole punching is required.

### 3.4 P2P

P2P remains the NAT traversal path described in `proxy-p2p-direct-path-design.md`.

It may use:

- LAN candidates;
- reflexive candidates;
- UPnP-created mappings;
- rendezvous discovery;
- UDP punching.

UPnP remains part of P2P and is not reclassified as Public Direct.

---

## 4. Target Architecture

```text
                              Relay Server
                    +-----------------------------+
                    | Auth / Approval             |
                    | Exit Discovery              |
                    | Authorization               |
                    | Public Endpoint Registry    |
                    | Reachability Verification   |
                    | Direct Access Ticket Issue  |
                    | P2P Coordinator             |
                    | Relay Fallback              |
                    +--------------+--------------+
                                   |
                         authenticated control
                        /                          \
                       /                            \
              Client Agent                      Exit Agent
                   |                                |
                   |                                | Public QUIC Listener
                   |                                | UDP :port
                   |                                |
                   +================================>
                   |      PUBLIC_DIRECT_QUIC        |
                   |                                |
                   |                                +--> Exit ACL
                   |                                +--> Upstream Proxy
                   |                                +--> Internet
                   |
                   | if unavailable / failed
                   v
              P2P QUIC attempt
                   |
                   | if unavailable / failed
                   v
              Relay QUIC / TLS
```

Default automatic priority:

```text
PUBLIC_DIRECT_QUIC
        |
        v
P2P_QUIC
        |
        v
RELAY_QUIC / RELAY_TLS
```

---

## 5. Why Public Direct Must Not Be a P2P Candidate

The current P2P endpoint owns one UDP socket that is reused across:

```text
reflexive discovery
UDP punching
QUIC handshake
```

This socket lifetime is important for NAT mappings.

That model is correct for P2P, but a public Exit has a different lifecycle:

```text
Exit starts
    |
    v
bind stable public-direct listener
    |
    v
register endpoint with Server
    |
    v
Server verifies endpoint
    |
    v
many authorized Clients may connect
```

A public listener is therefore not a session-scoped P2P candidate.

It is a long-lived Exit capability.

The implementation should not extend `P2PCandidate` to represent this listener. Public endpoints should have their own protocol model.

---

## 6. Direct Path Abstraction

### 6.1 Current limitation

The current Client dialer effectively models the selected path as:

```go
(session, direct bool)
```

and a direct session is treated as P2P.

Once Public Direct exists, `direct == true` is no longer sufficient because it may mean:

```text
PUBLIC_DIRECT_QUIC
P2P_QUIC
```

### 6.2 Proposed path type

Introduce an explicit path type.

Conceptually:

```go
type ProxyPath string

const (
    ProxyPathPublicDirectQUIC ProxyPath = "public_direct_quic"
    ProxyPathP2PQUIC          ProxyPath = "p2p_quic"
    ProxyPathRelayQUIC        ProxyPath = "relay_quic"
    ProxyPathRelayTLS         ProxyPath = "relay_tls"
)
```

The selected transport should carry both the session and the path:

```go
type SelectedSession struct {
    Session tunnel.TunnelSession
    Path    ProxyPath
}
```

This removes path inference from a boolean and makes telemetry, fallback, diagnostics, and future transports explicit.

### 6.3 DirectPathManager

Introduce an Agent-side direct path selector.

Conceptually:

```text
TunnelDialer
    |
    v
DirectPathManager
    |
    +-- PublicDirectProvider
    |
    +-- P2PProvider
    |
    +-- Relay fallback
```

Possible provider interface:

```go
type DirectPathProvider interface {
    SessionForExit(exitID string) (tunnel.TunnelSession, bool)
    Ensure(exitID string)
    Path() ProxyPath
}
```

The exact Go API may differ, but P2P should no longer be hard-coded as the only direct provider inside `TunnelDialer`.

---

## 7. Public Direct Listener

### 7.1 Listener ownership

An Exit Agent with Public Direct enabled starts a long-lived QUIC listener.

Example:

```text
0.0.0.0:35820/udp
[::]:35820/udp
```

The listener accepts authenticated RelayProxy direct sessions.

It is independent from P2P per-session sockets.

### 7.2 Suggested package layout

```text
agent/direct/
    manager.go
    public_client.go
    public_listener.go
    auth.go
```

Do not place the public listener under:

```text
agent/p2p/
```

because Public Direct does not require peer-to-peer NAT traversal.

### 7.3 Reusing Exit Handler

After authentication succeeds, the public direct QUIC connection should expose the same:

```text
tunnel.TunnelSession
tunnel.TunnelStream
```

abstractions used elsewhere.

The existing Exit stream handling can then remain:

```text
FrameTypeOpenTCP
FrameTypeOpenUDP
FrameTypeSpeedTest
...
```

The Exit business layer should not need separate Public Direct implementations of TCP and UDP proxying.

---

## 8. QUIC Refactoring

The current direct QUIC implementation lives in:

```text
internal/p2p/quic.go
```

Its transport primitives are useful outside P2P, but the current API assumes:

- a punched UDP socket;
- an ephemeral P2P identity;
- peer certificate fingerprint pinning.

The implementation should separate:

```text
generic direct QUIC transport
```

from:

```text
P2P socket/punch/authentication lifecycle
```

Suggested direction:

```text
internal/direct/
    quic.go
    ticket.go
```

or:

```text
internal/tunnel/
    direct_quic.go
```

Then both:

```text
Public Direct
P2P
```

can build `tunnel.TunnelSession` objects using shared QUIC transport primitives.

The existing P2P security properties must not be weakened during this refactor.

---

## 9. Public Endpoint Model

Public Direct endpoints require a dedicated protocol type.

Conceptually:

```go
type PublicDirectEndpoint struct {
    Protocol string
    Address  string
    Source   string
    Verified bool
}
```

Example values:

```json
{
  "protocol": "udp",
  "address": "203.0.113.20:35820",
  "source": "observed",
  "verified": true
}
```

Endpoint sources may include:

```text
observed
ipv6
manual
```

Only verified endpoints are published to Clients as usable Public Direct endpoints.

---

## 10. Endpoint Discovery

The initial implementation should support three endpoint sources.

### 10.1 Server-observed public IPv4

The Server already sees the source address of the authenticated Agent connection.

For an Agent listening on a known public-direct UDP port, the Server can use:

```text
observed public IP + registered listener port
```

as a verification candidate.

This address is not trusted until verification succeeds.

### 10.2 Global IPv6

The Agent may report globally routable IPv6 addresses associated with the listener.

The Server verifies them before publication.

Link-local, unspecified, multicast, loopback, and private-only addresses must not be treated as public endpoints.

### 10.3 Manual advertise address

For cloud hosts, port forwarding, multiple interfaces, or DNS-based setups:

```yaml
direct:
  public:
    advertise: "exit.example.com:35820"
```

A manually configured endpoint must still pass Server verification unless verification is explicitly disabled by a future advanced setting.

---

## 11. Server Reachability Verification

### 11.1 Why verification is mandatory

The Server must not trust a statement such as:

```text
"I am publicly reachable at 203.0.113.20:35820"
```

without testing it.

Otherwise the inventory could publish:

- stale public addresses;
- CGNAT endpoints;
- blocked firewall ports;
- expired IPv6 addresses;
- incorrect manual addresses;
- private addresses mistakenly reported as public.

### 11.2 Verification flow

```text
Exit Agent                   Relay Server
    |                             |
    | register listener           |
    | address candidates          |
    +---------------------------->|
    |                             |
    |                       create challenge
    |                             |
    |<========= QUIC/UDP =========|
    |  verification challenge     |
    |                             |
    | verification response       |
    |============================>|
    |                             |
    |                        endpoint VERIFIED
```

The probe must prove that:

1. the endpoint is reachable;
2. the responder is the same authenticated Exit Agent;
3. the listener is speaking the expected RelayProxy protocol.

### 11.3 Verification state

Suggested states:

```text
unknown
verifying
verified
failed
expired
```

Verification results should have a lifetime.

A previously verified endpoint should not remain valid forever after the Agent network changes.

---

## 12. Direct Access Authentication

### 12.1 Threat model

A public direct listener is reachable from the Internet.

Without direct authorization it could accidentally become:

```text
an open Internet proxy
```

Therefore possession of:

```text
IP + port
```

must not grant proxy access.

### 12.2 Server-issued Direct Access Ticket

The preferred design is a short-lived Server-signed access ticket.

Conceptually the ticket contains:

```text
version
clientDeviceID
exitDeviceID
issuedAt
expiresAt
policyRevision
authorizationRevision
nonce
allowed capabilities
signature
```

The Exit validates the ticket using a Server verification key.

### 12.3 Connection flow

```text
Client Agent               Relay Server                 Exit Agent
    |                           |                           |
    | request/refresh exits     |                           |
    |-------------------------->|                           |
    |                           | check authorization       |
    |<--------------------------|                           |
    | endpoint + ticket         |                           |
    |                                                       |
    |======================= QUIC ==========================>|
    |                                                       |
    | DirectAccessTicket                                    |
    |------------------------------------------------------>|
    |                                                       |
    |                                   verify signature    |
    |                                   verify expiry       |
    |                                   verify client ID    |
    |                                   verify exit ID      |
    |                                   verify policy rev   |
    |                                                       |
    |<================ proxy session =======================>|
```

No P2P:

```text
connect_request
connect_offer
connect_answer
candidate exchange
punching
```

is required.

### 12.4 Ticket lifetime

Tickets should be short-lived.

Initial recommendation:

```text
5 minutes
```

An already authenticated QUIC session does not need to disconnect merely because the ticket later expires.

New connections require a current ticket.

---

## 13. Authorization and ACL

Public Direct must preserve the same authorization boundary as Relay and P2P.

The effective connection must still enforce:

```text
Client is approved
Exit is approved
Client has proxy-client grant
Exit has proxy-exit grant
Client is authorized to use this Exit
Relay policy is current
Exit local ACL is applied
target host/IP protocol checks are applied
```

Public Direct must never mean:

```text
endpoint known => access allowed
```

### 13.1 Policy binding

The ticket should bind at least:

```text
clientDeviceID
exitDeviceID
policyRevision
authorizationRevision
expiresAt
```

The initial version may use the existing authenticated Server control connection to keep Exit authorization and policy state current.

A later design may allow longer control-plane outages by embedding a fully signed policy snapshot, but that is not required for V1.

---

## 14. Exit Inventory Extension

The Agent already receives Exit inventory through:

```text
DeviceAccepted.ProxyExits
Pong.ProxyExits
```

Public Direct metadata should extend that inventory.

Conceptual wire shape:

```json
{
  "deviceId": "exit-1",
  "name": "US Exit",
  "online": true,
  "direct": {
    "public": {
      "available": true,
      "transport": "quic",
      "endpoints": [
        "203.0.113.20:35820",
        "[2001:db8::20]:35820"
      ]
    },
    "p2p": {
      "available": true
    }
  }
}
```

The actual protocol structure should avoid duplicating sensitive authorization material into general telemetry.

If tickets are included with inventory, they must be scoped to the authenticated Client and short-lived.

Alternatively, the Client may fetch a fresh ticket only when it needs to establish a Public Direct session.

---

## 15. Path Selection

### 15.1 Automatic mode

Recommended selection algorithm:

```text
Selected Exit
    |
    +-- READY Public Direct session?
    |       |
    |       +-- yes -> PUBLIC_DIRECT_QUIC
    |
    +-- verified Public Direct endpoint available?
    |       |
    |       +-- yes -> start fast Public Direct attempt
    |
    +-- READY P2P session?
    |       |
    |       +-- yes -> P2P_QUIC
    |
    +-- start/continue P2P negotiation
    |
    +-- use Relay
```

### 15.2 Do not block ordinary proxy traffic

The first application request must not wait through long direct-path setup.

Recommended behavior:

```text
Public Direct fast attempt:
200-500 ms budget
```

If no ready Public Direct session is obtained inside the small budget:

```text
fall back to current usable path
```

while P2P establishment may continue asynchronously for subsequent flows.

The exact timeout should be tuned with real measurements.

### 15.3 Existing ready paths

Once a Public Direct or P2P session is READY, new flows can use it immediately.

Existing flows should not be migrated blindly between paths unless they use an explicit resumable transport mechanism.

---

## 16. Failure and Cooldown

Public Direct and P2P must maintain independent failure state.

Example:

```text
Public Direct
  timeout
  -> cooldown 30s

P2P
  punching failed
  -> cooldown 60s
```

A Public Direct failure must not suppress P2P.

A P2P failure must not suppress Public Direct.

Suggested per-path state:

```text
failure count
last error
cooldown until
last success
RTT
bytes up/down
```

---

## 17. Network Change Handling

Public reachability is not permanent.

Examples:

```text
Wi-Fi -> mobile hotspot
DHCP/public IPv4 change
IPv6 prefix rotation
ISP reconnect
firewall change
port-forwarding change
VPN interface change
```

On network signature change:

```text
Agent network change
        |
        v
invalidate local public endpoint state
        |
        v
re-register candidates
        |
        v
Server re-verifies
        |
        v
old endpoint revoked
        |
        v
ProxyExits refreshed
```

The existing `CurrentNetworkSignature()` logic currently located in the P2P endpoint code should be moved to a reusable network utility instead of being duplicated.

---

## 18. Configuration

Public Direct should not be configured under `p2p:`.

### 18.1 Server

Suggested configuration:

```yaml
direct:
  enabled: true

  public:
    enabled: true
    port_start: 35000
    port_end: 35999
    verify: true

p2p:
  enabled: true
  rendezvous_listen: ""
  rendezvous_advertise: ""
```

The exact ownership of the port range needs implementation review.

If the listener port is chosen entirely by the Agent, the Server may not need a global range. If Server policy is intended to constrain allowed Agent listener ports, the range belongs in Server configuration.

### 18.2 Agent

Suggested configuration:

```yaml
direct:
  public:
    enabled: true
    listen: ":0"
    advertise: ""
```

Normal operation should require only:

```yaml
enabled: true
```

A manual `advertise` field is for advanced setups.

Examples:

```yaml
advertise: "203.0.113.20:35820"
```

or:

```yaml
advertise: "exit.example.com:35820"
```

---

## 19. Transport Modes

The current P2P-specific mode names become incomplete after Public Direct is added.

Existing modes:

```text
auto
relay_only
p2p_only
```

Recommended evolution:

```text
auto
direct_only
p2p_only
relay_only
```

Semantics:

```text
auto:
    public -> p2p -> relay

direct_only:
    public -> p2p -> fail

p2p_only:
    p2p -> fail

relay_only:
    relay
```

`p2p_only` should remain for backward compatibility.

A future `public_only` mode may be useful for diagnostics but is not required for the initial implementation.

---

## 20. Observability

Current P2P-oriented fields such as:

```text
P2PState
P2PPath
P2PRTTMs
P2PFallbackCount
```

should gradually evolve into path-neutral direct transport metrics.

Recommended model:

```text
DirectState
DirectPath
DirectRTTMs
DirectFallbackCount
DirectBytesUp
DirectBytesDown
```

Path values identify the concrete transport:

```text
public_direct_quic
p2p_quic
relay_quic
relay_tls
```

Public Direct should additionally expose:

```text
listener state
verified endpoint count
last verification time
last verification failure
selected endpoint
address family
```

Sensitive tickets, tokens, fingerprints, and private keys must never be logged.

---

## 21. Server Web UI

The Server Web should expose Public Direct separately from P2P.

Example Exit status:

```text
US-01
Online

Public Direct    Available
P2P              Available
Relay            Available

Current path
Public Direct QUIC

Verified endpoints
203.0.113.20:35820
[2001:db8::20]:35820

RTT
42 ms
```

Suggested configuration controls:

```text
Direct
  [x] Enable Public Direct
  UDP port range
  [x] Verify public reachability

P2P
  [x] Enable P2P
  rendezvous settings
  UPnP
```

Public Direct availability and P2P availability must not be shown as the same status.

---

## 22. Security Requirements

Public Direct implementation must satisfy all of the following before release:

1. Public listener rejects unauthenticated clients.
2. Direct tickets are signed by the Server.
3. Tickets bind Client and Exit device IDs.
4. Tickets have short expiration.
5. Tickets cannot be replayed indefinitely.
6. Exit authorization revocation is respected.
7. Policy revisions are checked.
8. Endpoint registration is authenticated.
9. Server verification proves endpoint ownership.
10. Private and invalid addresses are not published as public endpoints.
11. Tickets and sensitive key material are never logged.
12. Listener rate limits prevent cheap unauthenticated resource exhaustion.
13. QUIC handshakes use TLS 1.3.
14. A failed Public Direct authentication must not fall through to proxy stream handling.
15. Public Direct must not bypass Exit target ACL checks.

---

## 23. Suggested Code Boundaries

Target structure:

```text
agent/
├── client/
│   └── dialer.go
├── direct/
│   ├── manager.go
│   ├── public_client.go
│   ├── public_listener.go
│   └── auth.go
├── p2p/
│   ├── endpoint.go
│   └── manager.go
└── exit/
    └── handler.go

internal/
├── direct/
│   ├── quic.go
│   └── ticket.go
├── protocol/
│   ├── direct.go
│   └── p2p.go
└── tunnel/

server/
├── direct/
│   ├── registry.go
│   ├── verifier.go
│   └── ticket.go
└── p2p/
    └── coordinator.go
```

Important ownership rule:

```text
agent/direct   = public direct lifecycle
agent/p2p      = NAT traversal lifecycle
agent/exit     = proxy target handling
server/direct  = endpoint verification + direct authorization
server/p2p     = P2P session coordination
```

---

## 24. Implementation Phases

### Phase 1: Path abstraction

Goal: remove the assumption that every direct session is P2P.

Tasks:

- introduce explicit proxy path type;
- replace `direct bool` path identification;
- retain existing P2P and Relay behavior;
- update TCP path reporting;
- update tests.

Success criteria:

- no behavior change for current users;
- all existing P2P and Relay tests pass;
- code can represent `public_direct_quic` without special casing.

### Phase 2: Public Direct listener and client

Goal: establish a Client-to-Exit QUIC session without P2P punching.

Tasks:

- add long-lived Exit public QUIC listener;
- add Public Direct Client dialer;
- reuse `tunnel.TunnelSession`;
- feed accepted streams into existing Exit handler;
- add basic local integration tests.

At this phase, development-only authorization may be used only in tests. Production exposure waits for Phase 4 security completion.

### Phase 3: Endpoint registry and verification

Goal: allow Server to know which Exit endpoints are truly reachable.

Tasks:

- add endpoint registration protocol;
- add server registry;
- add challenge-based verification;
- support observed IPv4;
- support global IPv6;
- support manual advertise address;
- publish only verified endpoints;
- invalidate endpoints when device/session/network changes.

### Phase 4: Direct Access Ticket

Goal: make the public listener safe for Internet exposure.

Tasks:

- define ticket wire format;
- add Server signing key usage;
- issue scoped short-lived tickets;
- verify tickets on Exit;
- bind ticket to authorization/policy revision;
- add replay and expiry protections;
- add abuse/rate-limit handling.

This phase is mandatory before enabling Public Direct by default.

### Phase 5: Client path selection

Goal: integrate Public Direct into normal proxy traffic.

Tasks:

- implement Public Direct provider;
- path priority: public -> P2P -> relay;
- independent cooldowns;
- bounded fast attempt;
- preserve Relay immediately when direct setup is unavailable;
- update stream-resume behavior where appropriate.

### Phase 6: Web configuration and status

Goal: make the feature operable without editing YAML manually.

Tasks:

- Server Web Public Direct enable/disable;
- port policy;
- verification state;
- endpoint list;
- selected path;
- RTT and failure reason;
- Agent status integration;
- diagnostics.

### Phase 7: Hardening

Tasks:

- IPv4/IPv6 racing;
- multiple endpoints;
- endpoint preference;
- listener DoS limits;
- network-change tests;
- authorization revocation tests;
- stale-ticket tests;
- Server reconnect tests;
- performance benchmarks;
- long-running stability tests.

---

## 25. V1 Scope

The recommended first production scope is:

```text
Transport:
    QUIC only

Address families:
    IPv4
    IPv6

Discovery:
    observed public IPv4
    global IPv6
    manual advertise address

Security:
    Server-verified endpoint
    Server-signed Direct Access Ticket
    existing Client/Exit authorization
    existing Exit ACL

Path order:
    Public Direct
    P2P
    Relay

Fallback:
    enabled by default
```

Not included in V1:

```text
TCP-based Public Direct
UPnP reclassification
automatic router port mapping for Public Direct
full offline authorization without Server control state
connection migration between arbitrary path types
public_only mode
```

---

## 26. Acceptance Criteria

The feature is complete when all of the following are true.

### Functional

- A verified public Exit can be reached by a Client without any P2P signaling.
- TCP proxy traffic works over Public Direct.
- UDP proxy traffic works over Public Direct.
- Public Direct uses the normal Exit ACL and upstream behavior.
- Public Direct failure falls back to P2P or Relay according to policy.
- P2P continues to work for NATed Agents.
- Relay continues to work when no direct path exists.

### Security

- An unauthenticated Internet host cannot use the Exit as a proxy.
- A Client cannot use an unauthorized Exit.
- Revoked authorization stops new Public Direct sessions.
- Expired or invalid tickets are rejected.
- Public endpoint registration cannot spoof another authenticated Exit.
- Sensitive credentials are not logged.

### Operations

- Server Web shows Public Direct availability independently from P2P.
- Operators can see verification failures.
- Operators can identify the active path.
- Network changes invalidate stale endpoint state.
- Public Direct and P2P cooldowns do not interfere with each other.

### Compatibility

- Old Agents that know only P2P and Relay continue to operate.
- New Clients can use old Exits through P2P/Relay.
- New Exits can serve old Clients through P2P/Relay.
- Existing routing rules and Exit selection semantics do not change.

---

## 27. Final Path Model

After this work, RelayProxy proxy transport should be understood as:

```text
Routing
    |
    +-- DIRECT --------------------------> destination
    |
    +-- REJECT --------------------------> blocked
    |
    +-- PROXY
           |
           v
       Select Exit
           |
           v
       Path Selector
           |
           +-- PUBLIC_DIRECT_QUIC -------> Exit
           |
           +-- P2P_QUIC ----------------> Exit
           |
           +-- RELAY_QUIC/TLS -> Server -> Exit
```

The most important architectural rule is:

> Public Direct and P2P are different mechanisms that may produce the same `tunnel.TunnelSession` abstraction. The Client path selector chooses between them; the proxy protocol and Exit business logic stay shared.
