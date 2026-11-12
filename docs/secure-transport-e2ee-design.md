# RelayProxy Secure Transport / E2EE Design

> Status: P0 transport hardening in implementation; P1 E2EE design for staged implementation.
> Target branch: `feat/secure-transport-e2ee-20261112`

## 1. Goals and boundaries

Separate three properties:

1. **Transport encryption**: TLS 1.3 protects each Agent-to-Relay or direct QUIC/TCP connection.
2. **End-to-end encryption**: Client Agent and selected Exit Agent encrypt proxy payload so the Relay can route and account for it without reading the target request or response.
3. **Metadata protection**: E2EE hides inner Host, Port, DNS payload, file names and application bytes. It does not hide Client/Exit device IDs, timing, byte counts, packet sizes, selected path or the destination IP contacted by the Exit.

The Exit is an endpoint and necessarily sees the destination and plaintext it must process. When Relay Server itself is selected as Exit, the Server is the endpoint and cannot be blind to that traffic. This design does not promise resistance to traffic analysis or DPI classification.

## 2. Current code facts

- Agent installations already have a persistent Ed25519 identity in `device-identity.json`.
- `DeviceHello.PublicKey` is sent during enrollment and persisted in `device_identities.public_key`.
- The online Server session stores the verified fingerprint but not the public key as a session field.
- `ProxyExit` inventory does not yet carry the peer's Ed25519 public key.
- Relay TCP/QUIC uses TLS 1.3 by default, but explicit plaintext TCP and insecure certificate-verification switches existed.
- P2P and Public Direct already use TLS 1.3 with ephemeral certificates and out-of-band fingerprint pinning. That transport authentication is independent of persistent device identity.

## 3. Threat model

E2EE protects payload confidentiality/integrity from passive network observers and a Relay process that can inspect forwarded stream bytes. It does not protect against compromised endpoints, process-memory/private-key theft, destination services, or traffic analysis.

The Server remains authoritative for device admission, capability grants, Client-to-Exit authorization, quotas and relay admission. The Exit enforces its local destination ACL and performs the actual dial.

## 4. Peer identity and key trust

Reuse the existing Ed25519 installation key as a long-term signing identity. Do not convert Ed25519 keys into X25519 keys or reuse signing keys as encryption keys.

For each Client-to-Exit stream:

1. Both endpoints generate fresh ephemeral X25519 key pairs and 32-byte nonces.
2. Both sign a domain-separated, length-prefixed transcript binding protocol version, Client Device ID, Exit Device ID, both ephemeral public keys and both nonces.
3. Client verifies the Exit signature against the authorized Exit public key from inventory. Exit verifies the Client signature and checks its signed Device ID against the outer admission header.
4. Both derive a shared secret with X25519, then use HKDF-SHA256 and the transcript hash to derive independent directional traffic keys.
5. Both exchange key-confirmation MACs before business data is accepted.

Carry verified online public keys in `DeviceSession` and authorized `ProxyExit` inventory. Persist a local TOFU pin keyed by (Server Instance ID, peer Device ID). First use records the peer key; later key changes fail closed and require explicit re-enrollment. TOFU detects later substitution but cannot independently authenticate first use. A later phase can replace TOFU with identity-authority-signed device certificates.

## 5. Secure stream architecture

Keep existing business request/response structures. Add a generic wrapper implementing `tunnel.TunnelStream`.

Relay path:

```text
Client Agent
  | outer StreamHeader: SecureRelay, ClientDeviceID, ExitDeviceID
  | authenticated E2EE handshake
  | encrypted inner StreamHeader + existing business request/data
  v
Relay Server
  | validates Client session, capability, Exit authorization, quotas and limits
  | forwards outer header and opaque encrypted bytes without parsing inner requests
  v
Exit Agent
  | authenticates Client key and completes E2EE handshake
  | decrypts inner StreamHeader and dispatches existing handler
  v
Existing TCP / UDP / DNS / RDP / file handler
```

P2P QUIC and Public Direct QUIC use the same E2EE wrapper and handshake. Their existing TLS 1.3 fingerprint-pinned transport remains enabled as defense in depth. A path switch must not change payload confidentiality.

### Record framing

Use ChaCha20-Poly1305 with separate directional keys. Each record contains a 4-byte big-endian ciphertext length and one AEAD ciphertext (maximum 16 KiB plaintext). A 64-bit monotonically increasing sequence is encoded into the 96-bit nonce; the length header is authenticated as associated data. Reject malformed, oversized, out-of-order or unauthenticated records. An authenticated empty record represents half-close; raw transport EOF without a valid close record is truncation. Preserve deadlines, cancellation, half-close and full-close semantics.

Handshake messages are length-prefixed and bounded to 4 KiB. No application data is accepted until both key confirmations succeed.

## 6. Relay admission, ACL and audit

The Relay continues enforcing authenticated Client session, current approval, `proxy.client`, Exit online/approved state, Client-to-Exit grants, stream limits, bandwidth quotas and connection accounting.

The Relay can no longer evaluate target-host rules from encrypted `OpenTCPRequest`. The Exit evaluates the decrypted host, port, DNS answers and final IP, including rebinding checks. For Relay-routed streams, the Server may attach its authoritative Relay ACL policy as trusted outer metadata on the Server-to-Exit leg; Exit intersects it with local policy. Client input must never override Relay policy.

Audit split:
- Relay: Client ID, Exit ID, protocol class, time, bytes, path and result. Target host/port/IP are unavailable.
- Exit-local: target host/port, resolved IP, ACL and dial result.
- Do not send Exit target details back to Relay if target metadata must remain private from Server.

## 7. Protocol rollout and downgrade

Target policy values:
- `off`: development/compatibility only.
- `opportunistic`: attempt E2EE when peer supports it and explicitly record non-E2EE fallback.
- `required`: refuse Client-to-Exit data streams unless E2EE completes.

Production default becomes `required` only after supported Agents, Server and Exit handlers ship. Missing peer key, unsupported version, failed signature/key confirmation or changed TOFU pin must fail closed in required mode. Never retry the same request over plaintext after an E2EE handshake failure. Capability negotiation is not authorization.

## 8. Staged implementation

### P0 — Transport hardening
- [x] TLS 1.3 remains the default tunnel transport.
- [x] Disabling Server tunnel TLS requires explicit development-only configuration.
- [x] Skipping Agent certificate verification requires explicit development-only configuration.
- [x] Programmatic Agent construction is guarded as well as YAML loading.
- [ ] Verify Server settings UI preserves/surfaces security warnings.
- [ ] Run Go tests, race tests, vet and cross-builds on CI.

### P1 — E2EE primitives and peer identity
- [ ] Bounded authenticated handshake (Ed25519 + ephemeral X25519 + HKDF).
- [ ] ChaCha20-Poly1305 record framing and `TunnelStream` wrapper.
- [ ] Replay, truncation, ordering, half-close and tamper tests.
- [ ] Carry verified public keys in online session and authorized Exit inventory.
- [ ] Local TOFU pin storage and explicit pin-change handling.

### P2 — Relay-blind proxy TCP/UDP
- [ ] Add `FrameTypeSecureRelay` and opaque Relay forwarding.
- [ ] Wrap Client TCP/UDP streams on Relay, P2P and Public Direct paths.
- [ ] Decrypt at Exit and dispatch existing TCP/UDP handlers.
- [ ] Move target-host ACL to Exit; preserve authoritative Relay ACL metadata.
- [ ] Redact target fields from Server connection audit.
- [ ] Document Server-local Exit as a non-blind endpoint.

### P3 — Remaining services
- [ ] DNS/system DNS, RDP, file share/listing/transfer and Browser Network Proxy.
- [ ] Multi-hop Exit chain and upstream proxy behavior.

### P4 — Outer metadata and traffic characteristics
- [ ] Validate ECH support in current Go and quic-go; do not claim ECH from a config flag alone.
- [ ] Evaluate real HTTP/2 CONNECT and HTTP/3 CONNECT-UDP/MASQUE interoperability.
- [ ] Measure padding/batching overhead on desktop and Android.
- [ ] Capture real ClientHello, ALPN, DNS, SNI and packet-size traces.

## 9. Acceptance tests

- Relay-side capture cannot recover inner Host, Port, DNS query, file names or payload.
- Exit receives original requests and returns byte-identical application data.
- Modified, truncated, reordered and replayed records are rejected.
- Wrong peer signature, wrong Device ID/Exit ID, wrong key-confirmation MAC and changed TOFU pin fail closed.
- No handshake failure falls back to plaintext in `required` mode.
- Outer TLS certificate validation remains active.
- Relay/P2P/Public Direct switching preserves E2EE identity and never leaks plaintext.
- Authorization revocation, ACL, stream limits, cancellation, half-close and audit accounting remain correct.
