# RelayProxy Router — K2P / Padavan

## Scope

This is a headless, exit-only RelayProxy runtime for low-resource Linux routers.
The first hardware target is the Phicomm K2P (MediaTek MT7621, MIPS32 little-endian).

The first implementation deliberately uses the existing Relay Agent session and Exit
handler through a constrained Router profile. It does not start a local SOCKS5/HTTP
proxy, GUI, Web UI, Gateway/TPROXY, file sharing, RDP, P2P, QUIC, or E2EE.

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

UDP is carried over the existing reliable stream transport in this first phase.
This is not native QUIC datagram transport and does not provide P2P.

## Padavan runtime

Copy the binary to `/tmp/relay-router` and copy
`scripts/relay-router-padavan.sh` to
`/etc/storage/relayproxy/relay-router.sh`. Set these environment variables before
calling the script (or export them from the existing Padavan startup script):

- `RELAY_SERVER`
- `RELAY_SERVER_NAME` (required for an IP endpoint)
- `RELAY_IDENTITY_ID`
- `RELAY_DEVICE_NAME`

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

## Current limitations / next gates

1. Run the MIPSLE cross-compile workflow and inspect the binary size.
2. Install on the actual K2P and verify identity approval plus TLS/yamux session.
3. Verify TCP and UDP exit traffic, including access to a LAN target.
4. Measure idle and sustained memory/CPU use on MT7621.
5. Only then evaluate QUIC/P2P as a separate phase.

The initial build reuses the existing Agent application package. Router mode removes
desktop features from the executable entry point and disables unrelated runtime
capabilities, but it is not yet a fully extracted minimal Go dependency graph. Binary
size and memory measurements determine whether a later Exit-only package extraction
is necessary.
