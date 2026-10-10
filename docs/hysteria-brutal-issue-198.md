# Issue #198 — Hysteria Brutal optional QUIC controller

Status: ported to `feat/hysteria-brutal-issue-198`, **not approved for merge** until CI and on-wire performance tests pass.

## Compatibility and limits

Default `0` values preserve **BBR Aggressive** on every QUIC direction. TLS relay is never switched. Clients must complete device authorization before their QUIC sender can be changed. Server independently caps Agent -> Server against `tunnel.bandwidth.down_mbps` and Server -> Agent against `tunnel.bandwidth.up_mbps`. The maximum protocol request is 1,000,000 Mbps even when the Server cap is 0; configure realistic per-client limits in production.

An Agent config requests `transport.bandwidth.up_mbps` and `down_mbps`. Server `tunnel.bandwidth.ignore_client_bandwidth` forces BBR, regardless of client requests; `disable_loss_compensation` controls the Server sender only. Zero requests and unsupported older peers remain on BBR. P2P and Public Direct use Server-authorized directional values, not raw QUIC hints. A legacy Public Direct authenticator without rate authorization stays on BBR.

**Brutal is intentionally opt-in.** Rates above the physical path capacity amplify queueing, packet loss, bufferbloat, and cross-tenant unfairness. Start below the measured sustained available bandwidth. Server caps are per connected session, not a global shared-bandwidth scheduler.

## Verification gate

- [x] Port controller, rate conversion/capping, loss-compensation tests, config and directional negotiation.
- [x] Preserve new main-line UPnP, Android settings, proxy routing, Public Direct authorization and P2P lifecycle.
- [x] Reject rate hints on legacy Public Direct authentication paths.
- [ ] Go CI: gofmt, go vet, go mod tidy, Go tests, core race tests and benchmark smoke must pass on this branch.
- [ ] Cross-platform Go regression matrix: Ubuntu, macOS, Windows.
- [ ] Android gomobile AAR and Gradle debug APK must build.
- [ ] In controlled network tests measure 0%, 1%, 5% loss: target throughput, p50/p95 RTT, goodput, retransmissions, CPU, and coexistence with another BBR flow.
- [ ] A/B Hysteria2 comparison on the same client, server, UDP path, RTT, bandwidth, packet-loss and hardware, repeated >= 5 times per case.
- [ ] Verify old-client/new-server and new-client/old-server pairings with both QUIC and TLS.
- [ ] Review CI artifacts and real-device results before opening/merging the replacement main PR.

## Reproduction

```sh
go test ./internal/congestion/... ./internal/tunnel ./internal/protocol ./agent/direct ./agent/p2p ./server/gateway ./server/p2p -count=1
go test -race ./internal/congestion/... ./internal/tunnel ./agent/direct ./agent/p2p ./server/gateway -count=1
```

Reference: [#198](https://github.com/timwai/RelayProxy/issues/198), historical-only [#179](https://github.com/timwai/RelayProxy/pull/179).
