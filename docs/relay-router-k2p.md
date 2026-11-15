# RelayProxy Router — K2P / Padavan

## Scope

This is a headless, exit-only RelayProxy runtime for low-resource Linux routers.
The first hardware target is the Phicomm K2P (MediaTek MT7621, MIPS32 little-endian).

The runtime reuses the existing authenticated TLS Relay session and Router-specific
Exit handler. It also supports an optional server-coordinated P2P path using UDP
hole punching and QUIC streams. It does not start a local SOCKS5/HTTP proxy, GUI,
Web UI, Gateway/TPROXY, file sharing, RDP, public-direct service, native QUIC
datagrams, or application-layer E2EE.

## K2P build target

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
  go build -trimpath -buildvcs=false -ldflags="-s -w" \
  -o relay-router-mipsle ./cmd/relay-router
```

Or run `scripts/build-relay-router-mipsle.sh`. The dedicated GitHub Actions workflow
also publishes a 14-day build artifact.

## CLI

Required:
- `--server`: Relay Server host/IP with optional port. Default port is 443.
- `--identity-id`: identity ID issued by the Relay Server.
- `--name`: device display name.

Optional:
- `--server-name`: TLS SNI and certificate name. Defaults to the server hostname.
  It is mandatory when `--server` is an IP address.
- `--data-dir`: persistent identity directory (default `/etc/storage/relayproxy`).
- `--ca-file`: additional PEM CA bundle; system roots are retained.
- `--max-connections`: local cap (default 64, maximum 256).
- `--p2p-upnp`: explicitly allow a temporary UPnP UDP mapping when the Server also enables it. Off by default.

Example for K2P behind the same NAT as the Relay Server:

```sh
/tmp/relay-router \
  --server 192.168.31.14:21000 \
  --server-name www.tingjusting.fun \
  --identity-id YOUR_SERVER_ISSUED_ID \
  --name K2P-Router \
  --data-dir /etc/storage/relayproxy
```

The target address and TLS server name are independent: TCP connects to
`192.168.31.14:21000`, while TLS verifies the certificate for
`www.tingjusting.fun`. Certificate verification is never disabled.

The Router requests only `proxy.exit`. The Server must approve that capability.
The Server-side `relay_acl` must also permit private-network targets if the Router
is expected to reach LAN devices; the Router's local policy allows Internet and
private networks but denies loopback.

### P2P direct transport

When the Server returns both `proxy_p2p_v1` and
`proxy_p2p_stream_only_v1` in the authenticated transport profile, the Router
starts the existing P2P manager as an Exit:
- Server authorization, rendezvous, leases, peer fingerprints and ACL policy stay on the authenticated TLS Relay control plane.
- Peers punch UDP candidates and establish a fingerprint-pinned QUIC TLS 1.3 session for data.
- TCP, stream-framed UDP and exit system DNS can use the direct QUIC stream path.
- Native QUIC datagrams, resumable TCP, SecureRelay/E2EE, RDP and file sharing remain unsupported.
- The ordinary TLS Relay listener stays active. If punching or QUIC setup fails, Clients continue through Relay without changing the selected Exit.
- Legacy Clients that do not advertise the new P2P datagram-negotiation capability are not offered the stream-only P2P path; they continue to work through Relay.

UPnP is opt-in on both sides: the Server must allow it and the Router must be
started with `--p2p-upnp`. On Padavan, set `RELAY_P2P_UPNP=1` before starting the
supervisor. Otherwise the Router uses its configured UDP port range and ordinary
NAT traversal only. Permit outbound UDP and the configured P2P UDP range in the
Router firewall; difficult NAT types may still require Relay fallback.

UDP on the Relay path and on direct P2P remains stream-framed. This Router profile
does not advertise native QUIC datagrams.

## Padavan runtime

Copy the binary to `/tmp/relay-router` and copy
`scripts/relay-router-padavan.sh` to
`/etc/storage/relayproxy/relay-router.sh`. Set these environment variables before
calling the script (or export them from the existing Padavan startup script):

- `RELAY_SERVER`
- `RELAY_SERVER_NAME` (required for an IP endpoint)
- `RELAY_IDENTITY_ID`
- `RELAY_DEVICE_NAME`
- `RELAY_P2P_UPNP=1` (optional; enables local UPnP only when Server policy allows it)

Then run:

```sh
RELAY_SERVER=192.168.31.14:21000 \
RELAY_SERVER_NAME=www.tingjusting.fun \
RELAY_IDENTITY_ID=YOUR_SERVER_ISSUED_ID \
RELAY_DEVICE_NAME=K2P-Router \
/etc/storage/relayproxy/relay-router.sh start
```

The launcher stores runtime PID/log files under `/tmp`. Device identity is stored
under `/etc/storage/relayproxy/identity.json`. On Padavan, `/etc/storage` is a
RAM-backed view of the Storage partition; after first identity creation and after
changing startup scripts, persist the storage using the Padavan-supported storage
save procedure. Do not write the binary to MTD.

A startup hook can be added without replacing the existing script. For example,
append the following block to the current `/etc/storage/started_script.sh`, edit
the values, and then persist the Storage settings:

```sh
# RelayProxy Router — keep this block in addition to existing startup commands.
(
    sleep 15
    export RELAY_SERVER="192.168.31.14:21000"
    export RELAY_SERVER_NAME="www.tingjusting.fun"
    export RELAY_IDENTITY_ID="YOUR_SERVER_ISSUED_ID"
    export RELAY_DEVICE_NAME="K2P-Router"
    /etc/storage/relayproxy/relay-router.sh start
) &
```

The 15-second delay is only a conservative startup default; increase it if WAN
connectivity is not ready. Keep the existing startup script backup so the change
can be rolled back.

To start automatically, append a small invocation to the existing
`/etc/storage/started_script.sh` without replacing its contents. Delay startup
until WAN/DNS/network initialization has completed. Preserve a backup of the
startup script before editing it.

## K2P field validation — 2026-11-15

The first MIPSLE soft-float binary has been installed on the Phicomm K2P.

User-reported results:
- Router connected to the Relay Server successfully.
- Device approval completed successfully.
- Network exit tests completed without reported problems.

The following hardware measurements are still outstanding and must be recorded
before calling the K2P profile production-ready:
- Idle process RSS, virtual memory, and thread count.
- CPU usage while idle and during sustained TCP/UDP forwarding.
- Peak memory during concurrent streams.
- Reconnect behavior after WAN loss and router restart.
- Actual binary size on the router filesystem and available /tmp headroom.

The current CI run passed Go CI, cross-platform regression, and the dedicated
MIPSLE build workflow for the tested branch head.

## K2P idle resource baseline — 2026-11-15

User-provided measurements from the running K2P (MT7621, 128 MB class RAM):

| Metric | Observation |
|---|---:|
| Process RSS | 22,280 kB (about 21.8 MiB) |
| VmSize / VmPeak | 580,464 kB (about 566.9 MiB) |
| VmData | 565,368 kB (about 552.1 MiB) |
| Threads | 10 |
| CPU in idle sample | 0.0% for relay-router |
| System CPU idle | 98.3% in second top sample; first sample 100% |
| Load average | 0.01 / 0.03 / 0.00 |

Interpretation: VmSize and VmData are virtual-memory accounting and must not be
treated as resident physical RAM usage. RSS is the relevant initial resident
footprint. The supplied top output is an idle sample; it does not establish
CPU cost or peak memory under sustained forwarding.

Next acceptance run: sustained TCP transfer and concurrent UDP traffic, then
record RSS, CPU, softirq, free/cache memory and transfer throughput during load.
Also test WAN disconnect/reconnect and router restart before production sign-off.

## K2P forwarding-load sample — 2026-11-15

User supplied samples 0–6 from a planned 12-sample, 10-second interval run.
The pasted output ends at sample 6, so this is a partial load observation, not
a completed two-minute soak test.

| Sample | VmRSS | VmHWM | Router CPU | System CPU idle |
|---:|---:|---:|---:|---:|
| 0 | 19,900 kB | 22,364 kB | 0.0% | 100.0% |
| 1 | 19,900 kB | 22,364 kB | 0.0% | 100.0% |
| 2 | 19,660 kB | 22,364 kB | 0.0% | 100.0% |
| 3 | 20,088 kB | 22,364 kB | 29.1% | 66.6% |
| 4 | 20,088 kB | 22,364 kB | 23.2% | 75.0% |
| 5 | 20,088 kB | 22,364 kB | 23.2% | 75.0% |
| 6 | 20,088 kB | 22,364 kB | 25.1% | 72.9% |

Threads remained at 10 in every supplied sample. RSS stayed within 19.2–19.6 MiB
and the high-water mark remained 22,364 kB. The Router process CPU rose to
23.2–29.1% in samples 3–6 while forwarding load was apparently present.

Interpretation: no material RSS growth is visible in these samples. CPU use is
measurable but does not by itself establish saturation. BusyBox top's SMP CPU
percentage must be interpreted against the four logical CPUs reported by this
MT7621 system; throughput, per-core utilization and transfer duration are not
included in the supplied excerpt.

Next: obtain samples 7–11 and record concurrent TCP/UDP transfer throughput,
per-core CPU if available, and softirq. Repeat at several concurrent stream
counts before setting a production concurrency limit.

## K2P completed 12-sample forwarding observation — 2026-11-15

User supplied all 12 samples at roughly 10-second intervals (about 112 seconds).

| Sample | VmRSS (kB) | Router CPU | System idle | Load average (1m) |
|---:|---:|---:|---:|---:|
| 0 | 20,024 | 0.0% | 100.0% | 0.13 |
| 1 | 20,024 | 0.0% | 100.0% | 0.11 |
| 2 | 19,612 | 25.2% | 72.9% | 0.33 |
| 3 | 19,568 | 25.4% | 70.8% | 0.58 |
| 4 | 19,568 | 29.3% | 66.6% | 0.87 |
| 5 | 19,568 | 0.0% | 100.0% | 0.74 |
| 6 | 19,568 | 1.1% | 97.7% | 0.63 |
| 7 | 20,076 | 27.5% | 68.7% | 0.92 |
| 8 | 20,076 | 25.2% | 70.8% | 1.08 |
| 9 | 20,076 | 25.2% | 72.9% | 1.45 |
| 10 | 20,076 | 24.9% | 68.7% | 1.69 |
| 11 | 20,076 | 31.2% | 64.5% | 1.73 |

VmHWM stayed at 22,364 kB throughout; thread count stayed at 10. RSS ranged
from 19,568 to 20,076 kB during the later samples (overall samples: 19,568–
20,076 kB, with a 19,612 kB observation also reported); no monotonic growth
was observed. CPU fell to 0–1.1% in samples 5–6, then rose to 24.9–31.2% in
samples 7–11. System idle remained 64.5–72.9% during those active samples;
softirq peaked at 6.2% in sample 11.

Interpretation: the process has a stable resident-memory footprint in this
short observation and CPU utilization is measurable but the system was not
fully CPU-saturated in the shown samples. Load average reached 1.73, which
should be treated as a trend to watch, not a direct CPU-saturation metric.
The test does not include throughput, exact active connection count, traffic
direction, or a confirmed workload profile, so it is not a throughput or
64-concurrent-stream acceptance result.

Next acceptance: record measured TCP throughput (single and parallel), UDP
throughput/latency, exact concurrent stream count, and per-core CPU under a
repeatable load. Then test WAN loss/recovery and a router reboot.


## Router process supervision and Padavan boot deployment

The Router binary also supports native Linux daemonization independently of the Padavan launcher:

```sh
/tmp/relay-router --server 192.168.31.14:21000 \
  --server-name www.tingjusting.fun \
  --identity-id YOUR_SERVER_ISSUED_ID \
  --name K2P-Router \
  --data-dir /etc/storage/relayproxy \
  --daemon

# Optional custom log location
/tmp/relay-router ... --daemon --log-file /tmp/relay-router-custom.log
```

`--daemon` starts a detached Linux session, redirects stdin from `/dev/null`,
and sends stdout/stderr (including Go logs) to `/tmp/relay-router.log` by default.
The parent command prints the child PID and exits. Send SIGTERM to that PID for
graceful shutdown. Native daemon mode does not create a PID file or automatically
restart after a crash.

Use either native `--daemon` or the Padavan shell supervisor below, not both:
the supervisor expects to own and wait for the Router child process, whereas
`--daemon` immediately detaches it.

The Padavan launcher now runs a lightweight shell supervisor:
- `start` starts the supervisor, which launches the Router child.
- Unexpected child exit causes restart with exponential backoff (2, 4, 8, ...,
  capped at 60 seconds by default).
- `status` reports supervisor and child PIDs separately.
- `stop` signals the supervisor, which gracefully terminates its child.
- The log rotates at approximately 1 MiB, retaining one previous log.
- The supervisor does not restart after an explicit `stop`.

Example control commands:

```sh
/etc/storage/relayproxy/relay-router-padavan.sh start
/etc/storage/relayproxy/relay-router-padavan.sh status
/etc/storage/relayproxy/relay-router-padavan.sh restart
/etc/storage/relayproxy/relay-router-padavan.sh stop
```

Important K2P deployment constraint: the observed Padavan Storage MTD partition
is only 512 KiB. The latest P2P-enabled MIPSLE Router binary is 11.44 MiB; do not
try to store the executable in that partition or in the small `/etc/storage` overlay.
The default executable path `/tmp/relay-router` is RAM-backed and is lost on
reboot. Keep the binary on a persistent USB/storage mount if available, or
arrange a separately verified boot-time provisioning step that copies the
binary into `/tmp` before invoking the supervisor. The identity directory
remains `/etc/storage/relayproxy` and must be persisted using Padavan's storage
save procedure. Never write the executable to MTD.

Example addition to `started_script.sh` (replace the source path with the
actual persistent mount path; do not replace the existing script):

```sh
(
    sleep 15
    cp /path/on/persistent-storage/relay-router /tmp/relay-router
    chmod 0755 /tmp/relay-router
    export RELAY_SERVER="192.168.31.14:21000"
    export RELAY_SERVER_NAME="www.tingjusting.fun"
    export RELAY_IDENTITY_ID="YOUR_SERVER_ISSUED_ID"
    export RELAY_DEVICE_NAME="K2P-Router"
    /etc/storage/relayproxy/relay-router-padavan.sh start
) &
```

The launcher supervises process exit; the Go Agent runtime separately owns
Relay session reconnect. These are complementary recovery layers. This does
not yet verify WAN recovery or reboot recovery on the physical K2P.


## Local binary upgrade and rollback

The Padavan launcher supports checksum-verified local upgrades without replacing
the device identity:

```sh
# Verify the SHA-256 from a trusted release channel before running this command.
/etc/storage/relayproxy/relay-router-padavan.sh upgrade /tmp/relay-router.new YOUR_64_CHAR_SHA256
/etc/storage/relayproxy/relay-router-padavan.sh status

# Roll back to the previously installed executable if needed.
/etc/storage/relayproxy/relay-router-padavan.sh rollback
```


Upgrade behavior:
- Verify the supplied SHA-256 before staging and again after copying.
- Verify the ELF magic, 32-bit class, and little-endian encoding expected by the
  K2P linux/mipsle target.
- Stage beside the active binary and atomically replace it.
- Preserve the prior executable as `<binary>.previous`.
- If the Router was running, stop and restart the supervisor around the upgrade.
- `rollback` restores the previous executable while keeping the same identity.

The upgrade command accepts a local file only. It deliberately does not download
or execute an arbitrary URL; artifact transport and release authenticity must be
handled by a trusted distribution channel. If `BIN` is under `/tmp`, the new
binary and rollback copy are also volatile and will be lost at reboot.

## Current limitations / next gates

1. Repeat the physical K2P idle/load measurements with the P2P-enabled binary; the existing RSS/CPU samples are from the earlier 7.19 MiB stream-only build.
2. Validate real WAN UDP hole punching across LAN, full-cone/restricted and symmetric NATs; record direct-path success rate and Relay fallback behavior.
3. Measure P2P-enabled RSS, CPU, UDP socket count and concurrent QUIC streams on MT7621 before raising the default connection limit.
4. Complete TCP/UDP throughput, latency, WAN-loss recovery and router-reboot acceptance.
5. Native QUIC datagrams, E2EE, RDP, file sharing and public-direct remain outside the Router profile.

## Dedicated minimal Router runtime and binary reduction

The Router executable no longer imports or constructs `agent/app.NewAgent`.
The command now uses a dedicated `agent/router` runtime that owns only:
- Device identity challenge/response authentication and approval.
- TLS 1.3 with the existing `relayproxy-tcp` ALPN and yamux multiplexing.
- Authenticated control heartbeat and reconnect with bounded exponential backoff.
- Relay-origin TCP forwarding and stream-framed UDP forwarding.
- Exit system DNS requests and the existing ACL enforcement.

The Router requests only `proxy.exit` and advertises TCP, TLS, QUIC, stream-framed
UDP, target ACL, exit system DNS, `proxy_p2p_v1` and the explicit
`proxy_p2p_stream_only_v1` profile. It does not advertise or initialize GUI,
SOCKS/HTTP client listeners, transparent interception, local DNS capture, routing
subscriptions, file sharing, RDP, browser sync, public-direct, native QUIC datagrams,
speed-test or E2EE. TCP resume is deliberately not advertised and is rejected if
requested. UDP remains stream-framed over TLS/yamux on Relay and over QUIC streams
on the direct P2P path.

The exit data plane has Router-specific TCP/UDP dispatch methods. They reuse the
existing target ACL, DNS cache, destination dialing and stream-pipe implementation,
but bypass E2EE, resumable TCP, and native datagram dispatch paths. The Server-side
device approval and stream wire protocol remain unchanged.

The MIPSLE build workflow now tests the dedicated runtime and exit handlers, builds
with stripped symbols/debug tables and reports the exact artifact size. The first
successful dedicated-runtime build (2026-11-15) produced **7.19 MiB (7,536,795 bytes)**
for linux/mipsle softfloat. The previous Router binary was approximately 15 MB, so the
new artifact is roughly half the prior size. This is a genuine runtime/import-graph
reduction rather than a claim based only on disabling features at runtime.


## P2P-enabled MIPSLE build — 2026-11-15

The dedicated MIPSLE workflow passed the Router package tests and cross-compiled
the P2P-enabled binary as a statically linked, stripped ELF32 little-endian
MIPS32 soft-float executable.

| Build profile | Binary size |
|---|---:|
| Earlier stream-only Router | 7.19 MiB (7,536,795 bytes) |
| P2P + QUIC stream Router | 11.44 MiB (11,993,243 bytes) |
| Increase | 4.25 MiB (about 59%) |

The P2P build adds the existing UDP candidate discovery, punching, fingerprint
verification and QUIC transport stack. The artifact is still too large for the
512 KiB Padavan Storage MTD partition; keep it on persistent external storage
and copy it to `/tmp` at boot as described above. The earlier physical K2P
RSS/CPU measurements do not validate this larger runtime and must be repeated.

The dedicated workflow now also runs the P2P manager and Server coordinator
tests, in addition to the Router/Exit tests, before producing the MIPSLE artifact.
