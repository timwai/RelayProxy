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
- [x] Go CI: gofmt, go vet, go mod tidy, Go tests, core race tests and benchmark smoke ([successful run](https://github.com/timwai/RelayProxy/actions/runs/38066614073)).
- [x] Cross-platform Go regression matrix: Ubuntu, macOS, Windows; Linux includes targeted P2P and direct path race tests ([successful run](https://github.com/timwai/RelayProxy/actions/runs/38066614068)).
- [x] Android gomobile AAR and Gradle debug APK ([successful run](https://github.com/timwai/RelayProxy/actions/runs/38066614074)).

- [x] Automated deterministic UDP packet loss smoke: 0%, 1%, 5% with BBR and Brutal, full payload integrity and QUIC diagnostics; **not** a real-link performance measurement.
- [x] Old/new version *linked protocol binaries* handshakes across QUIC and TLS, including same-version reference; the packaged Agent/Server acceptance remains open.
- [x] Hysteria2/BBR/Brutal repeatable download/upload CSV sampler supplied; operator-side A/B measurements not yet collected.
- [ ] In controlled network tests measure 0%, 1%, 5% loss: target throughput, p50/p95 RTT, goodput, retransmissions, CPU, and coexistence with another BBR flow.
- [ ] A/B Hysteria2 comparison on the same client, server, UDP path, RTT, bandwidth, packet-loss and hardware, repeated >= 5 times per case.
- [ ] Verify old-client/new-server and new-client/old-server pairings with both QUIC and TLS. The new cross-built gateway/protocol probe in `scripts/ci-brutal-mixed-compat.sh` covers a subset of this requirement, but full packaged Agent/Server pairings remain separate.
- [ ] Review CI artifacts and real-device results before opening/merging the replacement main PR.

## Binary protocol compatibility smoke

`scripts/ci-brutal-mixed-compat.sh` uses a checkout of the pre-Brutal main merge base and the feature branch. It builds identical standalone handshake probe sources against **both compiled versions of RelayProxy's production Gateway, tunnel, and v5 identity protocol packages**, then tests baseline-server/new-client, new-server/baseline-client and new/new over QUIC and TLS. All server capacity settings default to test-only loopback addresses. No administrator credentials, real proxy traffic, production GUI, or relay/exits are involved.

Run with `bash scripts/ci-brutal-mixed-compat.sh` on Linux after `git fetch origin main`. These are real old/new **linked protocol binaries**, not a full packaged Agent/Server release test. A pass narrows regression risk without fully checking actual platform binaries.

## Physical performance acceptance (pending)

The automated CI gates above passed on source commit `54ed7361625f7230120e701393fe8329907a8198`. They do **not** replace WAN tests or establish performance equivalence to Hysteria2.

Use a dedicated controllable test link and comparable SOCKS5 exit configurations. Record complete endpoints, CPUs, MTU, transport mode, configured Brutal upstream/downstream rates, actual bottleneck speeds, RTT, queue discipline, and server maximums before running. Ensure QUIC is selected on both sides; a fallback TLS result is not a Brutal measurement.

| Scenario | Loss | Controllers to compare | Direction | Repeats |
|---|---:|---|---|---:|
| Clean path | 0% | RelayProxy BBR, RelayProxy Brutal, Hysteria2 Brutal | both | >=5 |
| Light loss | 1% | RelayProxy BBR, RelayProxy Brutal, Hysteria2 Brutal | both | >=5 |
| Heavy loss | 5% | RelayProxy BBR, RelayProxy Brutal, Hysteria2 Brutal | both | >=5 |
| Coexistence | 0%, 1%, 5% | Brutal + separate BBR flow sharing the same bottleneck | both | >=5 |

For each trial capture useful-payload throughput (goodput), configured and observed send rate, transfer duration, p50/p95 end-to-end RTT during load, baseline idle RTT, QUIC lost bytes/packets, process CPU and RSS, and the competing BBR flow's goodput. Alternate controller order to avoid caching/warmup bias and compare median plus spread, not a single peak. Measure startup, prolonged steady load, reconnection and fallback-to-BBR behavior. Store raw CSV, QUIC diagnostics and scripts/environment parameters as linked Issue #198 artifacts.

**Release blockers:** hardware evidence above; mixed old/new Agent/Server pairings with QUIC and TLS; review of fairness under congestion. Do not silently enable non-zero rates and do not create/merge a mainline PR while these are unchecked.

## Reproducible Hysteria2 A/B sampler (operator-run)

The script `scripts/bench_brutal_acceptance.py` samples download or upload goodput through **three separately running SOCKS5/HTTP endpoints**, writes raw CSV, shuffles variant order every round, validates a known download hash when supplied, and preserves transfer errors as failed records. It **does not inject packet loss, measure actual QUIC RTT, sample CPU, or prove fairness**.

For each controlled 0%, 1%, and 5% packet loss condition, apply impairment **outside** the script on the same physical bottleneck; then run (replace example endpoints and a controlled static 64+ MiB file):

```sh
python3 scripts/bench_brutal_acceptance.py \
  --variant bbr=socks5h://127.0.0.1:1080 \
  --variant brutal=socks5h://127.0.0.1:1081 \
  --variant hysteria2=socks5h://127.0.0.1:1082 \
  --url https://test-origin.example/64MiB.bin \
  --expected-sha256 YOUR_KNOWN_FIXTURE_SHA256 \
  --scenario site-a-udp-rtt50ms --loss-label 1 \
  --runs 5 --direction download --output out/issue198.csv
```

For upload, use `--direction upload --upload-file /path/to/static.bin` and a separate server that accepts HTTP PUT; verify the uploaded hash at the destination. Run the matrix in both directions. A CSV row labeled "5%" does not prove that network loss was configured. Check actual packet counters (`tc -s qdisc`) and archive packet captures if available.

Collect idle and loaded p50/p95 RTT, transport QUIC loss counters, competitor BBR-flow goodput and CPU/RSS separately; curl's connect/first-byte times are **not** QUIC RTT or fairness. Keep old/new binary interoperability as a separate manual gate. Without a real network testbed and deployed Hysteria2 reference instance, this script provides tooling, **not** passing acceptance evidence.

## Reproduction

```sh
go test ./internal/congestion/... ./internal/tunnel ./internal/protocol ./agent/direct ./agent/p2p ./server/gateway ./server/p2p -count=1
go test -race ./internal/congestion/... ./internal/tunnel ./agent/direct ./agent/p2p ./server/gateway ./server/p2p -count=1
```

Reference: [#198](https://github.com/timwai/RelayProxy/issues/198), historical-only [#179](https://github.com/timwai/RelayProxy/pull/179).
