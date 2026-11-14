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
