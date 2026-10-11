# Issue #198 — Hysteria Brutal optional QUIC controller

Status: ported to `feat/hysteria-brutal-issue-198`, **not approved for merge** until CI and on-wire performance tests pass.

## Compatibility and limits

Default `0` values preserve **BBR Aggressive** on every QUIC direction. TLS relay is never switched. Clients must complete device authorization before their QUIC sender can be changed. Server independently caps Agent -> Server against `tunnel.bandwidth.down_mbps` and Server -> Agent against `tunnel.bandwidth.up_mbps`. The maximum protocol request is 1,000,000 Mbps even when the Server cap is 0; configure realistic per-client limits in production.

An Agent config requests `transport.bandwidth.up_mbps` and `down_mbps`. Server `tunnel.bandwidth.ignore_client_bandwidth` forces BBR, regardless of client requests; `disable_loss_compensation` controls the Server sender only. Zero requests and unsupported older peers remain on BBR. P2P and Public Direct use Server-authorized directional values, not raw QUIC hints. A legacy Public Direct authenticator without rate authorization stays on BBR.

**Brutal is intentionally opt-in.** Rates above the physical path capacity amplify queueing, packet loss, bufferbloat, and cross-tenant unfairness. Start below the measured sustained available bandwidth. Individual caps remain per session; a *new optional* aggregate reservation pool also exists, but it is **not a global bandwidth scheduler, wire-rate limiter, or fairness guarantee**.

## Runtime bufferbloat safety fuse (implemented, release validation pending)

A **per-sending-QUIC-session** watchdog now checks sender-side QUIC `MinRTT`, `SmoothedRTT`, `BytesSent`, `PacketsSent` and `PacketsLost` once per second while Brutal is active. After at least **five seconds** of controller lifetime, it falls back to the existing **BBR Aggressive** controller only when at least **three consecutive busy samples** show all of:

- meaningful sending activity (≥64 KiB new QUIC-sent bytes at each sampling tick);
- smoothed RTT ≥3× the observed session minimum; and
- smoothed RTT at least **100 ms above** the observed session minimum.

A **second, independent severe-loss trigger** checks incremental QUIC packet counters. After the same five-second warmup, it requires **at least 100 newly sent packets per interval, at least 20% newly reported losses, and three consecutive intervals**. Packet-loss counters that go backwards after spurious recovery do not count as new loss; idle, ordinary 1%/5% loss and one-off spikes do not trigger. This is a conservative backstop for extreme loss, not proof of zero bufferbloat.

**Aggregate quota reclamation:** A Server sender that falls back to BBR also immediately refunds its **Server → Agent** compensated pacing reservation, without cancelling the independent **Agent → Server** admission. Disconnect/handshake cleanup is idempotent with an in-flight fallback. The Gateway logs `Released Server Brutal pacing reservation after BBR fallback` when an aggregate server-upload budget was configured. This avoids stranded pacing reservations on still-connected BBR sessions; it does **not** prevent an unrelated BBR stream from using real shared capacity. The Agent can show `brutal_fallback_reason` during the desktop speed test on its local sending QUIC connection. Automatic return to Brutal after a fuse event remains disabled for the lifetime of that connection.

The fallback is **one-way for that connection** (no BBR↔Brutal oscillation). Diagnostics report `brutal_fallback_reason`; the sender logs `[BrutalGuard]`. A temporary RTT spike, idle path, or naturally high-RTT steady link alone must not trigger it. Unit tests cover all three and an actual QUIC session switching back to BBR while preserving stream payload correctness.

This is only a **last-resort congestion safety fuse**: it does not guarantee queue-free transit, recognize every competing tenant, estimate path capacity, implement fair queuing, or prevent a short-lived transfer from temporarily monopolizing a link before the guard triggers. Aggregate admission budgets remain necessary and independently configured. The guard's real-world false-positive and reduction-in-bufferbloat behavior still require controlled WAN evidence before default-on.

### Five-round desktop BBR rate estimation (Agent GUI; operator-only)

**Agent → 出口双向测速 →「5 轮 BBR 保守校准」** now repeats the existing authorized speed-test RPC five times, validating each *before* the next. A result is rejected if any direction isn't `relay_quic`, does not report the local QUIC sender as `bbr-aggressive`, contains a protection fallback reason, has invalid throughput or sub-second duration, changes exit identity, or fails to complete five rounds. The accepted 5-round *local application goodput* p10 is multiplied by **0.60** and floored to integer Mbps for a provisional per-direction candidate. No timeout exception, direct/P2P path, TLS fallback, or existing Brutal mode is treated as calibration success.

**Critical limitation:** The Agent-side QUIC diagnostics only prove the **Agent sender's** congestion-controller state; they do *not* independently establish the **Server sender's** active controller for download samples. The GUI therefore states this explicitly and does **not** certify the candidate as a Server BBR baseline, real link-capacity limit, shared tenant headroom, loaded RTT or same-path Hysteria2 performance. It never writes `transport.bandwidth.up_mbps/down_mbps`; this tool remains an **operator-only preliminary estimate**, not an automatic default selection or gate-clearing WAN artifact. The separately collected physical-WAN three-way A/B evidence remains mandatory.

### Measured-rate proposal from existing BBR baseline (operator-only)

The project already has a bidirectional endpoint speed-test transport in `agent/client/speedtest.go` and `internal/speedtest/speedtest.go`. That is an important starting point, but a single application speed test **does not prove physical available capacity or multi-tenant fairness**. A first fail-closed evidence reducer is now available:

```sh
python3 scripts/recommend_brutal_target.py \
  --input evidence/ab.csv \
  --scenario wan-site-a \
  --headroom 0.60 \
  --output evidence/brutal-rate-candidate.json
```

It requires all three BBR/Brutal/Hysteria2 controller variants, both directions, all 0/1/5% conditions and at least five runs per condition. For each direction, it takes the **minimum BBR p10 application goodput across those loss conditions**, multiplies by 0.60 and rounds **down** to a whole Mbps. For example, if this worst-case p10 is 12 Mbps, the candidate is 7 Mbps; if it is 8 Mbps, the candidate is 4 Mbps. Zero or sub-1 Mbps candidates keep BBR. The fraction can only be set in (0, 0.75].

Output is always `auto_apply_allowed: false` and `brutal_default_eligible: false`; the tool **never edits Agent configuration**. This number is only a conservative, provisional upper bound for a *measured* site and must still be checked against true capacity, active competing traffic, shared Relay budget, loaded end-to-end latency, renegotiation/reconnect behavior, and the release policy. It must not be used as a production-wide default merely because the script succeeds.

## Actual full distribution archive audit (new CI gate)

The newly added `full-release-archive-check` job in `.github/workflows/brutal-compat.yml` runs **the project's actual `scripts/build.sh`**, rather than only compiling bare Go entrypoints, on the Linux release builder. `scripts/verify_brutal_release_packages.py` checks:

- Archive SHA256 manifest against every shipped artifact; full Linux amd64/arm64, macOS amd64/arm64, and Windows amd64/arm64 distribution `tar.gz` files, plus both Windows full ZIPs and the independent x64 Agent/WinDivert ZIP.
- Real nonempty Agent, Server, desktop executable/App bundle layout, both config files and bundled brand assets, with x64 WinDivert DLL/driver/license/version. Corrupted ZIP data, absent members, duplicate member names and archive-path escapes are errors.
- Each shipped Agent's `configs/relay-agent.yaml` must preserve `up_mbps: 0` and `down_mbps: 0`, so an accidental release configuration edit **cannot silently enable Brutal globally** before Issue #198's default-on gates pass.

The job publishes `package-audit.json` and build logs as CI evidence. It explicitly reports `signatures_and_installed_upgrade_verified: false` and `real_wan_acceptance_verified: false` even if all archive checks pass. **The audit does not install WinDivert, test macOS NetworkExtension signing, execute target-platform GUI binaries, check notarization, or simulate old-package→new-package application-data migrations.** Those remain release blockers needing proper physical OS and signing infrastructure.

## Conditions for a default-on Brutal proposal (NOT SATISFIED)

Hysteria Brutal requires a **numeric sending-rate target**. RelayProxy cannot safely invent that rate from only a server address or from the existence of a QUIC connection; a guessed high target can starve other traffic, and a guessed low target can perform worse than BBR. Therefore an unconfigured link **must continue to initialize on BBR** even after this watchdog exists.

Before changing shipped Agent defaults, require all of:

1. A reproducible **path-capacity discovery / authorization mechanism** (or explicit independently measured link budget), with per-direction limits and other traffic headroom. It must handle heterogeneous client uplinks, mobile network changes, metered networks, and shared exit contention without assuming a single global Mbps value.
2. The real-WAN, same-endpoint BBR/Brutal/Hysteria2 0/1/5% × upload/download × >=5-round matrix, sustained competing-flow/loaded RTT evidence, CPU and on-wire packet-loss traces, and an approved fairness/latency policy. Current CI `netem` tests are not a substitute.
3. Guard false-positive/false-negative evaluation, reconnect/fallback telemetry, repeated long-duration multi-tenant tests and staged opt-in field rollout with rollback. The default must not make an unproven production-wide change.
4. Installable/signed package and GUI/platform network interception upgrade QA, in addition to the passing 24 baseline/new executable authentication and SOCKS5 application transfer paths.
5. Explicit release-owner approval that states **which measured network population, bandwidth discovery policy and selected target rate** the default-on behavior applies to, and clearly reverts to BBR whenever path capacity is unknown.

Until those are complete, **do not change `transport.bandwidth.up_mbps/down_mbps: 0`, do not merge this branch to main, and do not label Issue #198 default-enabled**.

## Aggregate Relay QUIC Brutal budget (implemented, opt-in)

To preserve capacity for BBR and other applications, Server admins can optionally specify total directional **pacing reservations** alongside existing per-device caps:

```yaml
tunnel:
  bandwidth:
    up_mbps: 20
    down_mbps: 20
    total_up_mbps: 40
    total_down_mbps: 40
    ignore_client_bandwidth: false
```

Here `up_mbps` and `down_mbps` are still per-device. `total_up_mbps` and `total_down_mbps` separately cap the **sum of reserved Brutal pacing targets for authenticated Relay QUIC connections**. Each assigned target is conservatively charged **125%** toward the pool because Brutal's loss-compensation floor can make the actual QUIC pacer send at up to 1.25× the requested target. For example, a 40 Mbps total pool will fund at most 32 Mbps of total nominal Brutal targets (before UDP/IP overhead), not 40 Mbps. If the remaining compensated allowance can fund at least 1 Mbps, the handshake grants a reduced rate; otherwise that direction falls back to BBR. QUIC session closure and failed handshakes release reservations, with reconnects reclaiming capacity. TLS never reserves QUIC allowances or receives non-zero negotiated Brutal rates.

**Defaults:** Both total limits are `0` (unlimited; legacy behavior) and the transport rate hints still default to `0` (BBR). No new Brutal capacity is implicitly enabled. A Server administrator must explicitly configure total limits; they should be **below measured sustainable directional network throughput**, accounting for protocol overhead, shared exits, other tenants and BBR traffic. Limit values are validated as 0–1,000,000 Mbps.

**Web management:** The same six Brutal options (per-device upstream/downstream caps, total upstream/downstream quotas, ignore-client switch, and Server loss-compensation switch) are exposed under **Server Web → 服务配置 → 隧道连接**, and take effect after Server restart. When pool limits reduce a handshake's target, the Server logs a `Relay Brutal aggregate budget clamped` event with the requested and authorized directional rates.

**Scope and limitations:** This guards only authenticated **Relay QUIC** negotiations. It does not reserve or throttle Public Direct, peer-to-peer paths, Hysteria2 running on the same host, TLS/BBR flows, individual destination sockets, or physical NIC traffic. Server target-pool control is a concurrency-safe *admission/target budget*, not actual traffic shaping or dynamic congestion-aware fair queuing. Even with the pool enabled, real-link loaded RTT and BBR coexistence performance must be measured before release.



## Verification gate

- [x] Port controller, rate conversion/capping, loss-compensation tests, config and directional negotiation.
- [x] Add optional authenticated Relay QUIC aggregate directional budgets with conservative 125% loss-compensation headroom, safe BBR fallback and per-session release; configuration persistence, simultaneous-handshake and TLS fallback regressions included.
- [x] Preserve new main-line UPnP, Android settings, proxy routing, Public Direct authorization and P2P lifecycle.
- [x] Reject rate hints on legacy Public Direct authentication paths.
- [x] Go CI: gofmt, go vet, go mod tidy, Go tests, core race tests and benchmark smoke ([successful preceding run](https://github.com/timwai/RelayProxy/actions/runs/38100402492)); source-change follow-up requires the latest green Go CI.
- [x] Cross-platform Go regression matrix: Ubuntu, macOS, Windows; Linux includes targeted P2P/direct race tests ([successful preceding run](https://github.com/timwai/RelayProxy/actions/runs/38100402507)); source-change follow-up requires the latest green matrix.
- [x] Android gomobile AAR and Gradle debug APK ([successful run](https://github.com/timwai/RelayProxy/actions/runs/38066614074)).

- [x] Automated deterministic UDP packet loss smoke: 0%, 1%, 5% with BBR and Brutal, full payload integrity and QUIC diagnostics; **not** a real-link performance measurement.
- [x] Linux kernel `tc netem` shaped UDP tests (12 Mbps, 20 ms, 0/1/5% *configured random loss*) including concurrent BBR/Brutal sessions at 8 and 16 Mbps targets, qdisc counters and raw logs ([passing netem jobs](https://github.com/timwai/RelayProxy/actions/runs/38100605774)). **Laboratory simulation only, not external WAN/fairness acceptance**.
- [x] Old/new version *linked protocol binaries* handshakes across QUIC and TLS, including same-version reference; the packaged Agent/Server acceptance remains open.
- [x] Hysteria2/BBR/Brutal repeatable download/upload CSV sampler supplied; operator-side A/B measurements not yet collected.
- [x] Operator-run loaded ICMP latency sampler, ≥60s concurrent BBR/Brutal fairness monitor, complete release evidence-matrix validator, and real Linux old/new binary CI provided; real-WAN evidence remains uncollected.
- [ ] In controlled network tests measure 0%, 1%, 5% loss: target throughput, p50/p95 RTT, goodput, retransmissions, CPU, and coexistence with another BBR flow. **The CI co-active sample exposed significant BBR starvation when Brutal is overcommitted; a real-link fairness acceptance limit must be defined and verified before release.**
- [ ] A/B Hysteria2 comparison on the same client, server, UDP path, RTT, bandwidth, packet-loss and hardware, repeated >= 5 times per case.
- [x] Verify old/new **actual Linux CLI Agent+Server** pairings with both QUIC and TLS: eight combinations authenticated with real identity approval and private-key signatures ([successful production entrypoint job](https://github.com/timwai/RelayProxy/actions/runs/38106440949)).
- [x] Verify eight old/new **native executable authentication pairings** each on Linux, Windows and macOS (24 tests). This verifies CLI processes, not signed distribution packages.
- [x] Verify old/new **real Linux, Windows and macOS CLI executable application traffic**: 24 cross-version signed-auth QUIC/TLS paths through SOCKS5 and identity-granted Server Exit to a fixture origin, all payloads checked.
- [ ] Verify **released installers/archives** and desktop GUI/network interception upgrades on Windows/macOS, long-running real-WAN traffic, reconnect and platform private-key recovery. Lab-native executable tests do not fulfill this final requirement.
- [ ] Review CI artifacts and real-device results before opening/merging the replacement main PR.

## Binary protocol compatibility smoke

`scripts/ci-brutal-mixed-compat.sh` uses a checkout of the pre-Brutal main merge base and the feature branch. It builds identical standalone handshake probe sources against **both compiled versions of RelayProxy's production Gateway, tunnel, and v5 identity protocol packages**, then tests baseline-server/new-client, new-server/baseline-client and new/new over QUIC and TLS. All server capacity settings default to test-only loopback addresses. No administrator credentials, real proxy traffic, production GUI, or relay/exits are involved.

Run with `bash scripts/ci-brutal-mixed-compat.sh` on Linux after `git fetch origin main`. These are real old/new **linked protocol binaries**, not a full packaged Agent/Server release test. A pass narrows regression risk without fully checking actual platform binaries.


## Time-synchronized laboratory coexistence metrics

The `TestBrutalKernelNetemCoexistence` runner additionally records receiver progress and sender-side **QUIC smoothed RTT every 250 ms** while two flows share the same kernel queue. Each log point has an absolute Unix timestamp to distinguish actual overlap from finishing-time averages. `scripts/analyze_brutal_netem.py` intersects adjacent timestamp windows and writes `evidence/netem-overlap.csv` with BBR and Brutal overlap-only goodput, observed overlap seconds, a descriptive Jain index based on the two **estimated co-active rates**, and p50/p95 of sampled QUIC **smoothed** RTT. This time overlap analysis is substantially more useful than comparing independently completed downloads.

These laboratory snapshots are diagnostic, **not** true p50/p95 latency for individual application packets, time-weighted WAN measurements, or a release/fairness threshold. The script fails when either flow lacks sufficient (>=750 ms) co-active sampling, so a successful job cannot hide a nonoverlapping transfer. Observe kernel qdisc counters, sender diagnostics and separately measured loaded end-to-end RTT before making any WAN fairness conclusion.

### Simultaneous-flow measurements (new)

The dedicated [Linux kernel-netem CI analysis](https://github.com/timwai/RelayProxy/actions/runs/38103283560) sampled receiving bytes every 250 ms and intersected **only windows where BBR and Brutal were concurrently transmitting**. The table gives *one laboratory run* per condition, at a 12 Mbps qdisc bottleneck and 20 ms configured delay. Values are effective traffic rates across overlapping time windows, not independently measured completion-time rates.

| Random loss configured | Brutal target | Concurrent BBR goodput | Concurrent Brutal goodput | Jain index |
|---|---|---:|---:|---:|
| 0% | 16 Mbps (over bottleneck) | 0.87 Mbps | 9.65 Mbps | 0.590 |
| 1% | 16 Mbps (over bottleneck) | 0.51 Mbps | 7.03 Mbps | 0.572 |
| 5% | 16 Mbps (over bottleneck) | 0.84 Mbps | 7.07 Mbps | 0.618 |
| 0% | 8 Mbps (below bottleneck) | 3.13 Mbps | 7.97 Mbps | 0.840 |
| 1% | 8 Mbps (below bottleneck) | 3.47 Mbps | 7.59 Mbps | 0.878 |
| 5% | 8 Mbps (below bottleneck) | 3.48 Mbps | 7.39 Mbps | 0.885 |
| 0% | 4 Mbps (headroom reserved) | 7.19 Mbps | 3.92 Mbps | 0.920 |
| 1% | 4 Mbps (headroom reserved) | 7.07 Mbps | 3.92 Mbps | 0.924 |
| 5% | 4 Mbps (headroom reserved) | 7.30 Mbps | 3.99 Mbps | 0.921 |

**New conservative headroom evidence:** With the Brutal target at 4 Mbps under the 12 Mbps shared queue, BBR received approximately 7.1–7.3 Mbps simultaneously, versus Brutal roughly 3.9–4.0 Mbps (single short trial for each 0/1/5% case; [passing job](https://github.com/timwai/RelayProxy/actions/runs/38104588392)). This supports testing lower Brutal targets, not a universal rule or proof of WAN fairness.

**Release-impacting finding:** An overcommitted Brutal sender severely starved the competing BBR sender during co-active intervals, substantially more than the original completion-time ratios suggested. Even an 8 Mbps target did not yield equal sharing. The laboratory Jain index is a descriptive diagnostic, **not a certification threshold**. There is no automatic globally shared capacity reservation, so per-session rate caps alone cannot ensure tenant fairness.

Release acceptance now additionally requires defining a documented shared-bottleneck fairness policy (for example, limits on BBR competitor slowdown and loaded RTT) and validating it under repeated *real* link conditions before any default-on or production enablement. Keep Brutal opt-in and default rates at zero; do not present these traces as proof of fairness or Hysteria2 equivalence. Evidence CSV is available in CI artifacts named \`brutal-linux-netem-loss-*\` as \`netem-overlap.csv\`.

## Kernel traffic-shaping findings (not WAN acceptance)

A dedicated CI runner uses `tc netem rate 12mbit delay 20ms` on its loopback queue, with configured random loss 0/1/5%. Both independent 4 MiB QUIC transfers are run concurrently; BBR and Brutal both complete and pass integrity checks. Each row is **one short run**; rates are *per-flow bytes divided by wall-clock completion time*, not simultaneous-link fairness measurements:

| Configured random loss | Brutal target | BBR completion goodput | Brutal completion goodput |
|---|---|---:|---:|
| 0% | 16 Mbps (over bottleneck) | 5.36 Mbps | 9.11 Mbps |
| 1% | 16 Mbps (over bottleneck) | 4.19 Mbps | 9.03 Mbps |
| 5% | 16 Mbps (over bottleneck) | 5.17 Mbps | 8.53 Mbps |
| 0% | 8 Mbps (under bottleneck) | 5.19 Mbps | 7.29 Mbps |
| 1% | 8 Mbps (under bottleneck) | 5.13 Mbps | 7.16 Mbps |
| 5% | 8 Mbps (under bottleneck) | 5.04 Mbps | 7.08 Mbps |

The 0% configured random-loss trial still recorded **720 kernel queue drops** across all transfers: the shaper also drops packets when the queue fills, so the label is not a measurement of zero actual loss. Kernel qdisc counters are recorded before/after in uploaded `brutal-linux-netem-loss-*` CI artifacts. Very large QUIC smoothed RTT values were observed on some overcommitted Brutal runs; this is evidence of queueing pressure, **not** a reliable loaded p95 RTT statistic.

Therefore avoid setting Brutal target above sustainable available capacity, including overhead and competing tenants. An individual Server session cap cannot guarantee fairness across several sessions. Neither aggregate completion rates nor one example justify marking p50/p95 RTT, prolonged flow fairness, or Hysteria2 A/B requirements as passed. For these retain independent, time-synchronized performance sampling on real devices.

## WAN test kit: Hysteria2 same-link A/B and sustained BBR fairness

The test kit now offers **three distinct, operator-run stages** rather than pretending GitHub CI loopback is the real WAN. All three proxies must be real running processes; **the scripts do not download Hysteria2 or configure remote machines**. Hysteria2 client configuration follows [official Hysteria2 client documentation](https://v2.hysteria.network/zh/docs/getting-started/Client/) with \`bandwidth.up\` and \`bandwidth.down\` matching RelayProxy's configured directions, and a local SOCKS5 \`listen\`. Hysteria2 BBR's default preset is *standard*, while RelayProxy's existing controller is *BBR Aggressive*; explicitly record the distinction rather than falsely claiming identical BBR implementations.

**Preparation (physical WAN required):**

- Two independently controlled physical endpoints (Client A and Server B) separated by the real target WAN path. Run Hysteria2 and RelayProxy against equivalent endpoints and the **same test-origin content and comparable UDP path**, with P2P and Public Direct disabled for the RelayProxy baseline. Use three dedicated local SOCKS5 ports, e.g. \`1080\` = RelayProxy BBR, \`1081\` = RelayProxy Brutal and \`1082\` = Hysteria2 Brutal.
- Set Hysteria2 and RelayProxy Brutal to the **same up/down Mbps**. Match total/per-client server caps, client/server NICs, transport mode, path RTT and effective MTU; avoid different egress paths or proxies. Record \`relay-agent --version\`, Hysteria2 version, SHA256 of each executable and the immutable large HTTP origin fixture. Verify QUIC selected on both RelayProxy cases and preserve raw diagnostics.
- Externally configure and measure 0%, 1% and 5% loss **on the same WAN bottleneck**, with enough downstream/upstream capacity and spare CPU. Examples include \`tc netem\` at the WAN router; save \`tc -s qdisc show\` before/after and original operator console logs per condition. **Declaring \`--loss-label 5\` does not actually impose 5% packet loss**. Do not apply impairment to production routers.
- For ICMP RTT choose the *same-path server host IP* responding to \`ping\`; ping independently tests ICMP latency through the shared bottleneck, not precise QUIC stream/ACK RTT. Include unloaded baseline pings and external CPU/RSS/QUIC packet-loss captures in the evidence directory. Use a fixture large enough to remain transferring for the full load interval; avoid caches, HTTP 3xx, compression and variable file contents.

**A. Hysteria2, RelayProxy BBR, and RelayProxy Brutal same-link application throughput + loaded RTT.** For each loss condition and each direction, run at least five independent rounds. The A/B tool captures per-transfer p50/p95 ICMP samples when \`--ping-host\` is supplied, and now rejects trials without at least five responses:

\`\`\`sh
python3 scripts/bench_brutal_acceptance.py \
  --variant bbr=socks5h://127.0.0.1:1080 \
  --variant brutal=socks5h://127.0.0.1:1081 \
  --variant hysteria2=socks5h://127.0.0.1:1082 \
  --url https://YOUR-STATIC-ORIGIN.example/1GiB.bin \
  --expected-sha256 FIXTURE_SHA256 \
  --ping-host YOUR_SERVER_WAN_IP \
  --scenario wan-site-a --loss-label 1 \
  --direction download --runs 5 --output evidence/ab.csv

# Separate upload pass: same URL must accept HTTP PUT; use the same payload
# size for every proxy and omit --expected-sha256 for upload.
python3 scripts/bench_brutal_acceptance.py \
  --variant bbr=socks5h://127.0.0.1:1080 \
  --variant brutal=socks5h://127.0.0.1:1081 \
  --variant hysteria2=socks5h://127.0.0.1:1082 \
  --url https://YOUR-UPLOAD-ORIGIN.example/put \
  --upload-file evidence/upload-1GiB.bin \
  --ping-host YOUR_SERVER_WAN_IP \
  --scenario wan-site-a --loss-label 1 \
  --direction upload --runs 5 --output evidence/ab.csv

python3 scripts/summarize_brutal_acceptance.py \
  --input evidence/ab.csv --output evidence/ab-summary.csv
\`\`\`

Repeat A at **loss=0 and loss=5**, preserving the same \`--scenario\`, exact endpoint identities and comparable fixture lengths. The summary rejects mismatched URLs, inconsistent per-variant proxy endpoints, missing rounds, failed transfers and different payload sizes. Upload origin success indicates a completed PUT; independently verify receipt on the origin when assessing real upload correctness.

**B. Sustained *simultaneous* BBR + Brutal competition, five or more ≥60s rounds per loss condition.** This is a distinct fairness experiment, not another three-way serial A/B. It writes per-interval throughput for both flows, Jain's two-flow index, unloaded/loaded ICMP p50/p95, and rejects early connection completion or inadequate samples:

\`\`\`sh
python3 scripts/bench_brutal_coexistence.py \
  --bbr-proxy socks5h://127.0.0.1:1080 \
  --brutal-proxy socks5h://127.0.0.1:1081 \
  --url https://YOUR-STATIC-ORIGIN.example/20GiB.bin \
  --ping-host YOUR_SERVER_WAN_IP \
  --scenario wan-site-a --loss-label 1 \
  --rounds 5 --duration 60 --interval 1 \
  --output-dir evidence/coexistence-1
\`\`\`

Repeat for 0% and 5%. Result files: \`coexistence-summary.csv\`, \`coexistence-timeseries.csv\`, \`coexistence-metadata.json\`. These are *client-side HTTP goodput* and ICMP observations, not synchronized QUIC sender window samples or CPU/RSS/packet-loss measurements. Keep the independent platform diagnostics; these metrics **do not** establish a WAN release claim on their own.

Example files are provided at `docs/hysteria-brutal-wan-manifest.example.json` and `docs/hysteria-brutal-wan-policy.example.json`. **They are deliberately placeholders / illustrative policy values; do not submit them as collected data.** The validator rejects non-SHA256 values and manifest placeholders.

**C. Machine-check the complete evidence matrix.** Prepare two local JSON files: a manifest containing independent software hashes, physical host hardware/regions/network-path description, origin fixture hash, RTT target, and \`loss_evidence\` mapping for each loss \`0\`, \`1\`, \`5\` to a real on-disk \`impairment_log\`; and an explicitly signed-off policy with numeric \`min_jain\`, \`min_bbr_retention\`, \`max_loaded_icmp_p95_ms\`, \`max_loaded_over_idle_p50_ratio\`, and \`min_duration_s\`. The policy must be defined by the maintainer/operator based on actual link requirements rather than silently imposed by CI.

\`\`\`sh
python3 scripts/verify_brutal_release_evidence.py \
  --ab-csv evidence/ab.csv \
  --coexistence 0=evidence/coexistence-0/coexistence-summary.csv \
  --coexistence 1=evidence/coexistence-1/coexistence-summary.csv \
  --coexistence 5=evidence/coexistence-5/coexistence-summary.csv \
  --manifest evidence/environment.json \
  --policy evidence/acceptance-policy.json \
  --output evidence/machine-checks.json
\`\`\`

The validator requires the full **3 losses × 2 directions × 3 controllers × ≥5 runs** matrix plus five sustained coexistence rounds per loss, comparing BBR coactive throughput to its same-condition serial baseline. A green machine verdict explicitly retains \`human_real_wan_provenance_and_release_signoff_required: true\`. **No genuine measured WAN data or policy approval is available in this repository at present**; keep WAN and final release boxes unchecked. Capture process CPU/RSS, real QUIC loss/retransmissions and loaded end-to-end probe RTT with external tooling before human approval.

## Genuine production binary interoperability (Linux, Windows, macOS lab verified)

\`scripts/ci-brutal-full-binary-compat.sh\` now **builds actual \`cmd/relay-agent\` and \`cmd/relay-server\` binaries from both main's pre-feature merge base and the feature branch**, rather than only reusing the small linked-protocol probe. It creates an independent temporary SQLite database per pairing, a separate identity login, an Ed25519 installation key with real signed challenge, administrator-approved \`proxy.client\`, TLS loopback certificate, and a real headless Agent process. It requires that each Agent process log its normal successful device approval and that both executables remain alive. Pairings: baseline/baseline, baseline/new, new/baseline and new/new Server/Agent, each forced to \`quic_only\` and \`tcp_only\` (eight cases). [Successful CI execution](https://github.com/timwai/RelayProxy/actions/runs/38106440949) logged all eight PASS lines. This materially advances the release gate, but **does not exercise Windows/macOS distribution packages, signed installers, actual proxy throughput or direct-path/P2P interop**; these remain pending. All data is ephemeral, passwords are never active valid logins, and the certificate bypass is isolated to loopback test configuration. The native `scripts/ci-brutal-native-binary-compat.py` repeats the **same eight authenticated pairings on Windows and macOS runners** using actual production executable entrypoints and isolated certificates. [Windows/macOS successful run](https://github.com/timwai/RelayProxy/actions/runs/38106757479), together with Linux, confirms 24 authenticated process pairings. **Confirmed end-to-end payload tests:** [Linux binary/data-plane matrix](https://github.com/timwai/RelayProxy/actions/runs/38106923785) and [Windows/macOS native matrix](https://github.com/timwai/RelayProxy/actions/runs/38106940718) each passed eight old/new pairings. Every combination performed SOCKS5 -> authenticated QUIC/TLS Relay -> explicitly identity-authorized Server Exit -> HTTP origin on isolated loopback, and compared actual returned payload bytes, not just a login message. Across three operating systems this is **24 production CLI executable login and real application traffic pairings**. These are disposable loopback integration packages, **not** signed installer, desktop GUI, Windows transparent driver/macOS Network Extension, external WAN, or DNS/P2P compatibility tests.

## Physical performance acceptance (pending)

Initial CI gates passed on `54ed7361625f7230120e701393fe8329907a8198`, and the subsequent feature-branch CI expanded coverage to packet-loss, kernel netem and mixed-version binary probes. The latest run links above are the authoritative verification record; they do **not** replace WAN tests or establish performance equivalence to Hysteria2.

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

The script `scripts/bench_brutal_acceptance.py` (with no automatic HTTP redirect following and no embedded proxy URL credentials) samples download or upload goodput through **three separately running SOCKS5/HTTP endpoints**, writes raw CSV, shuffles variant order every round, validates a known download hash when supplied, and preserves transfer errors as failed records. It **does not inject packet loss, measure actual QUIC RTT, sample CPU, or prove fairness**.

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

After each run, validate sample completeness (same rounds for all three variants, ≥5 successful rounds, no duplicate rounds, no failed transfers) and write summary statistics:

```sh
python3 scripts/summarize_brutal_acceptance.py --input out/issue198.csv --output out/issue198-summary.csv
```

The summary rejects inconsistent origin URLs, changed proxy endpoints, unequal payload lengths, mixed checksum-verification status, noncontiguous or incomplete rounds, or failed transfers before reporting median and p10/p90 goodput and relative median difference to Hysteria2 **for each scenario/loss/direction**. It returns nonzero for missing samples, failed transfers or malformed numbers; it does **not** claim to verify actual induced loss, loaded RTT or fairness. Archive both raw and summarized CSV with exact build versions and hardware details.

For upload, use `--direction upload --upload-file /path/to/static.bin` and a separate server that accepts HTTP PUT; verify the uploaded hash at the destination. Run the matrix in both directions. A CSV row labeled "5%" does not prove that network loss was configured. Check actual packet counters (`tc -s qdisc`) and archive packet captures if available.

Collect idle and loaded p50/p95 RTT, transport QUIC loss counters, competitor BBR-flow goodput and CPU/RSS separately; curl's connect/first-byte times are **not** QUIC RTT or fairness. Keep old/new binary interoperability as a separate manual gate. Without a real network testbed and deployed Hysteria2 reference instance, this script provides tooling, **not** passing acceptance evidence.

## Reproduction

```sh
go test ./internal/congestion/... ./internal/tunnel ./internal/protocol ./agent/direct ./agent/p2p ./server/gateway ./server/p2p -count=1
go test -race ./internal/congestion/... ./internal/tunnel ./agent/direct ./agent/p2p ./server/gateway ./server/p2p -count=1
```

Reference: [#198](https://github.com/timwai/RelayProxy/issues/198), historical-only [#179](https://github.com/timwai/RelayProxy/pull/179).
