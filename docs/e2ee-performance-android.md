# E2EE Performance Optimization and Android Support

## Status

- Shared E2EE record-path allocation and write-call optimizations are implemented.
- Android Client and Exit integration use the shared Go E2EE implementation through gomobile.
- Benchmarks are included for 64 B, 1 KiB, 16 KiB and 64 KiB records. Run them on target devices before publishing performance claims.

## Record-path optimizations

The secure stream keeps the existing ChaCha20-Poly1305 algorithm, 16 KiB maximum plaintext record, sequence numbers and authenticated length header.

- Reuse the receive buffer and decrypt records in place after validating the record length.
- Reuse the transmit buffer instead of allocating a new ciphertext slice for every record.
- Keep send and receive nonces in fixed-size per-stream arrays; update only the sequence number for each record.
- Send the four-byte authenticated record header and ciphertext in one underlying write.
- Do not aggregate independent `Write` calls: preserving prompt delivery is important for interactive RDP-like traffic and small request/response flows.

The buffer sizes grow only to the largest record observed by that stream. This trades a bounded amount of retained memory per active E2EE stream for fewer allocations and less garbage collection.

## Benchmarking

Run:

```sh
go test ./internal/e2ee -run '^$' -bench 'BenchmarkSecureStream' -benchmem -count=3
```

The Go CI benchmark-smoke job runs the same benchmarks with a short benchtime. Compare the 64 B and 1 KiB cases for per-record overhead, and 16 KiB / 64 KiB cases for bulk throughput. Do not treat CI-host results as Android-device results.

## Android integration

Android uses `mobile/androidcore`, which is compiled into the app's gomobile AAR. The implementation therefore reuses the same handshake and secure stream as desktop Agent rather than introducing a second cryptographic protocol.

- The existing app-private `device-identity.json` Ed25519 identity is used for both device authentication and E2EE.
- TOFU Exit public-key pins are stored in app-private `peer-key-pins.json`, scoped by authenticated Server Instance ID and Exit Device ID.
- Android advertises E2EE v1 capability, uses authenticated Exit inventory keys for outbound Client streams, and verifies/pins the peer key only after successful key confirmation.
- Android Exit configures its persistent Ed25519 identity in the shared Exit handler and accepts SecureRelay streams from authenticated Relay and Server-coordinated P2P sessions.
- The Proxy settings page exposes `off`, `opportunistic` and `required`. The default is `opportunistic`. Required mode rejects unverified TLS, missing E2EE peer keys/pins and unsupported data paths rather than silently downgrading.
- Disabling outbound E2EE does not disable the Android device's ability to serve E2EE connections when acting as an Exit.

## Coverage boundaries

This integration covers remote Exit proxy TCP, reliable stream-UDP and Exit system DNS A/AAAA on supported Relay/P2P paths. It does not add application-level E2EE to Public Direct, RDP, file sharing or Browser Network Proxy. Relay-routed native datagram-required UDP remains fail-closed; native datagram-required UDP can use the authenticated P2P QUIC TLS path.

## Validation still required on physical devices

- Compare Android arm64 and arm32 throughput and CPU for E2EE on/off.
- Measure sustained screen-on and screen-off power use under realistic proxy load.
- Verify Client-to-Android-Exit and Android-Client-to-desktop-Exit interoperability over Relay and P2P.
- Exercise first-use pin persistence, app restart, Server instance changes and peer key changes.
