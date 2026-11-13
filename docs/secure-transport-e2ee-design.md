# RelayProxy Secure Transport / E2EE Design

> Status: transport hardening and Relay-routed TCP/stream-UDP E2EE are implemented in the feature branch; cross-platform CI and end-to-end acceptance remain in progress.
> Target branch: `feat/secure-transport-e2ee-20261112`

## 1. Goals and boundaries

Separate three properties:

1. **Transport encryption**: TLS 1.3 protects each Agent-to-Relay or direct QUIC/TCP connection.
2. **End-to-end encryption**: for a remote device Exit reached through the Relay, the Client Agent and Exit Agent encrypt the inner proxy stream. The Relay routes and accounts for opaque bytes without parsing target requests or responses.
3. **Metadata protection**: the Relay can still see Client/Exit Device IDs, selected Exit, stream timing, byte counts and packet sizes. The Exit and destination network necessarily know the target. Traffic analysis and DPI classification are not hidden.

The Server itself cannot be blind when the Server-local Exit is selected: it is the egress endpoint. Remote device Exits use the application-level E2EE wrapper over Relay-routed streams and authenticated P2P QUIC. Public Direct continues to use TLS 1.3 with pinned ephemeral transport fingerprints and does not yet add the application-level E2EE wrapper.

## 2. Current code facts

- Agent installations have a persistent Ed25519 identity in `device-identity.json`.
- `DeviceHello.PublicKey` is sent during enrollment, verified by the Server, and copied into the online `DeviceSession`.
- Authorized online Exit inventory carries the Exit public key and an E2EE capability bit.
- The Client stores a TOFU pin in `peer-key-pins.json`, scoped by authenticated Server Instance ID and Exit Device ID. A changed key fails closed.
- The production CLI passes the pin-store path to the Agent. Embedders that construct `app.AgentConfig` without `E2EEPeerPinPath` do not enable Relay E2EE.
- Relay TCP, reliable stream-UDP and remote Exit system DNS A/AAAA requests can be E2EE-wrapped when Client, Server and remote Exit advertise support. Native datagram-required UDP currently returns a clear error rather than silently downgrading.
- Server-local Exit is intentionally excluded from E2EE because the Server is the endpoint.
- Relay TCP/QUIC and direct QUIC/TCP retain TLS 1.3. Plaintext TCP and disabled certificate verification require explicit development-only opt-ins.

## 3. Threat model and limitations

E2EE protects payload confidentiality/integrity from passive network observers and a Relay process that follows the forwarding protocol and inspects forwarded stream bytes. It does not protect against compromised endpoints, process-memory/private-key theft, destination services, traffic analysis, or a network operator that can observe the Exit's egress.

The Server remains authoritative for device admission, capability grants, Client-to-Exit authorization, quotas and Relay admission. The Exit enforces its local destination ACL and performs the actual dial. The Relay forwards the Server-owned ACL policy to the Exit; Client-supplied policy is never authoritative.

**First-use trust limitation:** the Client discovers an Exit key from the authenticated Server inventory. TOFU detects subsequent key changes, but it cannot authenticate the first key against an actively malicious Server that can substitute the inventory key on first use. Resisting a malicious Server requires a separately trusted identity authority or out-of-band verification of the first fingerprint. This implementation must not be described as protecting against an active Server MITM on first use.

## 4. Peer identity and key trust

Reuse the existing Ed25519 installation key as a long-term signing identity. Do not convert Ed25519 keys into X25519 keys or reuse signing keys as encryption keys.

For each Client-to-Exit stream:

1. Both endpoints generate fresh ephemeral X25519 key pairs and 32-byte nonces.
2. Both sign a domain-separated, length-prefixed transcript binding protocol version, Client Device ID, Exit Device ID, both ephemeral public keys and both nonces.
3. Client verifies the Exit signature against the authorized Exit public key from inventory. Exit verifies the Client signature against the public key forwarded by the Relay from the authenticated online Client session.
4. Both derive a shared secret with X25519, then use HKDF-SHA256 and the transcript hash to derive independent directional traffic keys and separate key-confirmation keys.
5. Both exchange key-confirmation MACs before business data is accepted.

The Client pins the Exit key only after successful handshake/key confirmation. Pins are keyed by (Server Instance ID, Exit Device ID). Later key changes fail closed with the old and presented short fingerprints in the error. An operator must verify the new key out of band and explicitly remove the pin file before trusting a rotation.

## 5. Secure stream architecture

Keep existing business request/response structures. Add a generic wrapper implementing `tunnel.TunnelStream`.

Relay-routed remote Exit path:

```text
Client Agent
  | outer StreamHeader: SecureRelay, ClientDeviceID, ExitDeviceID
  | clear metadata: Client public key
  | authenticated E2EE handshake
  | encrypted inner StreamHeader + existing TCP/UDP stream request and data
  v
Relay Server
  | validates Client capability/session, Exit authorization and Exit capability
  | verifies Client key against the authenticated online session
  | replaces ACL metadata with its own authoritative policy
  | forwards opaque encrypted bytes without parsing the inner request
  v
Exit Agent
  | authenticates Client key and completes E2EE handshake
  | decrypts inner StreamHeader and dispatches existing TCP/UDP handler
  v
Existing Exit ACL / resolver / dialer
```

P2P QUIC uses the application-level E2EE wrapper when both peers advertise support. Public Direct QUIC still relies on its TLS 1.3 fingerprint-pinned transport because it does not yet expose the authenticated SecureRelay stream handler.

### Record framing

Use ChaCha20-Poly1305 with separate directional keys. Each record contains a 4-byte big-endian ciphertext length and one AEAD ciphertext (maximum 16 KiB plaintext). A 64-bit monotonically increasing sequence is encoded into the 96-bit nonce; the length header is authenticated as associated data. Reject malformed, oversized, out-of-order or unauthenticated records. An authenticated empty record represents half-close; raw transport EOF without a valid close record is truncation. Preserve deadlines, cancellation, half-close and full-close semantics.

Handshake messages are length-prefixed and bounded to 4 KiB. No application data is accepted until both key confirmations succeed.

## 6. Relay admission, ACL and audit

The Relay enforces authenticated Client session, current approval, `proxy.client`, E2EE capability, Exit online/approved state, Client-to-Exit grants and stream admission. It checks that the clear Client public key matches the key in the authenticated online session. The Exit public key is selected from the authenticated authorized Exit inventory and then pinned by the Client.

The Relay cannot evaluate target-host rules from encrypted `OpenTCPRequest`. It forwards its authoritative ACL policy to the Exit; the Exit evaluates decrypted host, port, DNS answers and final IP, including rebinding checks, and intersects Relay policy with local policy. Client input cannot override Relay policy.

Audit split:
- Relay: Client ID, Exit ID, protocol class, time, bytes, path and result. Target host/port/IP are not available from the encrypted inner request.
- Exit-local: target host/port, resolved IP, ACL and dial result.
- Do not send Exit target details back to Relay if target metadata must remain private from Server.

## 7. Protocol rollout and downgrade

The Client supports `proxy.e2ee_mode` in `relay-agent.yaml`, exposed as “代理端到端加密（E2EE）” in the Agent desktop/Web settings:

- `off`: use the existing TLS-protected legacy proxy stream, without application-layer E2EE (explicit compatibility/development choice).
- `opportunistic` (default when unset): encrypt Relay-routed TCP and reliable stream-UDP when the authenticated Server and selected remote device Exit advertise support. Legacy endpoints may use the old TLS-protected proxy stream. Once E2EE negotiation starts, handshake/key-confirmation/TOFU failure closes that stream; no plaintext retry.
- `required`: fail closed for covered proxy TCP, stream-UDP and remote Exit system DNS A/AAAA queries unless they use authenticated E2EE. An explicit remote Exit device ID is required; Server-local Exit is rejected. Authenticated P2P QUIC can carry the same E2EE wrapper without Relay data-plane involvement. Public Direct does not yet expose the SecureRelay handler: auto mode falls back to Relay, while a direct-only route fails closed if it selects Public Direct. `p2p_only` is supported when the peer is E2EE-capable. Missing key/Server capability or suppressed E2EE advertising fails closed. Verified TLS remains mandatory.

Required mode protects against **capability-suppression downgrade** on these specific proxy flows, but does not authenticate an Exit's **first** TOFU key independently of the Server. Out-of-band fingerprint verification / identity-authority-signed Exit certificates remain follow-up security work. The mode now protects remote Exit system DNS A/AAAA questions on Relay and authenticated P2P QUIC paths. It does not yet protect other DNS resolver modes, RDP, file sharing, Browser Network Proxy, native UDP datagrams or other independent service streams.

## 8. Staged implementation

### P0 — Transport hardening
- [x] TLS 1.3 remains the default tunnel transport.
- [x] Disabling Server tunnel TLS requires explicit development-only configuration.
- [x] Skipping Agent certificate verification requires explicit development-only configuration.
- [x] Programmatic Agent construction is guarded as well as YAML loading.
- [x] Agent startup warns when development-only plaintext/insecure transport is enabled.
- [x] Server settings UI exposes explicit plaintext TCP opt-in and warnings; Agent UI requires confirmation before saving unsafe transport changes.
- [ ] Run Go tests, race tests, vet and cross-builds on CI.

### P1 — E2EE primitives and peer identity
- [x] Bounded authenticated handshake (Ed25519 + ephemeral X25519 + HKDF).
- [x] ChaCha20-Poly1305 record framing and `TunnelStream` wrapper with independent directional and key-confirmation keys.
- [x] Handshake, peer-key mismatch, tamper, oversized/truncated record and authenticated half-close tests.
- [x] Carry authenticated public keys in online sessions and authorized online Exit inventory.
- [x] Persistent TOFU pin store, Server/Exit scoping, key-change rejection and pin-store tests.
- [ ] Add explicit operator workflow/UI for verified Exit key rotation.
- [x] Add replay/ordering rejection tests and a bounded handshake-parser fuzz target (extended fuzz campaigns remain part of release testing).

### P2 — Relay-blind proxy TCP/UDP
- [x] Add `FrameTypeSecureRelay` and opaque Relay forwarding.
- [x] Wrap Relay-routed Client TCP, reliable stream-UDP and Exit system DNS A/AAAA requests when both endpoints support E2EE.
- [x] Wrap authenticated P2P QUIC Client TCP, reliable stream-UDP and Exit system DNS with the same E2EE identity handshake; bind the direct stream to the Server-coordinated Client/Exit device pair.
- [x] Decrypt at Exit and dispatch existing TCP/UDP handlers.
- [x] Forward authoritative Relay ACL metadata to Exit.
- [x] Keep target host/port/IP out of Relay secure-stream audit.
- [x] Add in-process Server router + real Exit handler integration acceptance proving the Relay cannot recover TCP target metadata/payload or Exit system DNS questions.
- [x] Add explicit TCP/stream-UDP/Exit-system-DNS E2EE-required policy, path-fail-closed and downgrade tests.
- [ ] Add the application-level E2EE wrapper to Public Direct paths after they expose an authenticated Exit stream handler.
- [ ] Add native datagram E2EE or a documented product-level stream fallback for UDP datagram-required callers.

### P3 — Remaining services
- [ ] RDP, file share/listing/transfer and Browser Network Proxy.
- [ ] Multi-hop Exit chain and upstream proxy behavior.

### P4 — Outer metadata and traffic characteristics
- [ ] Validate ECH support in current Go and quic-go; do not claim ECH from a config flag alone.
- [ ] Evaluate real HTTP/2 CONNECT and HTTP/3 CONNECT-UDP/MASQUE interoperability.
- [ ] Measure padding/batching overhead on desktop and Android.
- [ ] Capture real ClientHello, ALPN, DNS, SNI and packet-size traces.

## 9. Acceptance tests

- Relay-side capture cannot recover inner Host, Port, covered Exit-system DNS questions, file names or application payload.
- Exit receives original requests and returns byte-identical application data.
- Modified, truncated, reordered and replayed records are rejected.
- Wrong peer signature, wrong Device ID/Exit ID, wrong key-confirmation MAC and changed TOFU pin fail closed.
- No handshake failure falls back to plaintext after E2EE is selected.
- Outer TLS certificate validation remains active.
- Authorization revocation, ACL, stream limits, cancellation, half-close and audit accounting remain correct.
- First-use TOFU trust limitations remain documented even when required mode prevents capability suppression downgrade on the covered proxy flows.
