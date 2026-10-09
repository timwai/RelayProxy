#!/usr/bin/env bash
# Independent, opt-in DNS egress guard. This is deliberately NOT tied to the
# Agent process: nftables remains active if RelayProxy/its NFQUEUE exits.
set -euo pipefail

TABLE="relayproxy_dns_guard"
UNIT="relayproxy-dns-killswitch.service"
RULES="/etc/relayproxy/dns-guard.nft"
SERVICE="/etc/systemd/system/$UNIT"
ACTION="${1:-status}"

if [[ "$ACTION" != "status" && "${EUID}" -ne 0 ]]; then
    echo "Run enable/disable as root." >&2
    exit 1
fi
for bin in nft systemctl; do
    command -v "$bin" >/dev/null || { echo "Missing $bin" >&2; exit 1; }
done

case "$ACTION" in
    enable)
        install -d -m 0700 /etc/relayproxy
        cat >"$RULES" <<'NFT'
table inet relayproxy_dns_guard {
    chain output {
        type filter hook output priority 0; policy accept;
        # Keep local stub resolvers usable, but deny their own remote DNS
        # egress. This hook runs AFTER iptables mangle/NFQUEUE (priority -150).
        oifname "lo" accept
        meta l4proto { tcp, udp } th dport { 53, 853, 784, 8853 } drop
    }
}
NFT
        chmod 0600 "$RULES"
        nft --check --file "$RULES"
        cat >"$SERVICE" <<EOF
[Unit]
Description=RelayProxy persistent DNS egress guard (independent of Agent)
DefaultDependencies=no
After=local-fs.target
Before=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c '/usr/sbin/nft list table inet $TABLE >/dev/null 2>&1 || /usr/sbin/nft --file $RULES'
RemainAfterExit=yes

[Install]
WantedBy=network-pre.target multi-user.target
EOF
        chmod 0644 "$SERVICE"
        systemctl daemon-reload
        systemctl enable --now "$UNIT"
        echo "Enabled persistent DNS guard. Confirm with: sudo $0 status"
        ;;
    disable)
        systemctl disable --now "$UNIT" 2>/dev/null || true
        nft delete table inet "$TABLE" 2>/dev/null || true
        rm -f "$SERVICE" "$RULES"
        systemctl daemon-reload
        echo "Disabled RelayProxy independent DNS guard."
        ;;
    status)
        nft list table inet "$TABLE" || {
            echo "Persistent DNS guard NOT ACTIVE" >&2
            exit 1
        }
        systemctl is-enabled "$UNIT" || {
            echo "Rules active now, but NOT enabled for restart" >&2
            exit 1
        }
        ;;
    *)
        echo "Usage: $0 {enable|disable|status}" >&2
        exit 2
        ;;
esac
