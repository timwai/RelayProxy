#!/usr/bin/env bash
# Safely test the actual nftables DNS guard in an isolated Linux network
# namespace, without installing global firewall or systemd rules.
set -euo pipefail

for tool in ip nft tcpdump python3; do
    command -v "$tool" >/dev/null || { echo "missing $tool" >&2; exit 1; }
done
[[ "$(id -u)" == "0" ]] || { echo "run as root" >&2; exit 1; }

namespace="relayproxy-dns-ci-$$"
host_if="rpdh$$"
ns_if="rpdn$$"
work="$(mktemp -d)"
capture_pid=""
cleanup() {
    if [[ -n "$capture_pid" ]]; then
        kill "$capture_pid" 2>/dev/null || true
        wait "$capture_pid" 2>/dev/null || true
    fi
    ip link del "$host_if" 2>/dev/null || true
    ip netns del "$namespace" 2>/dev/null || true
    rm -rf "$work"
}
trap cleanup EXIT

ip netns add "$namespace"
ip link add "$host_if" type veth peer name "$ns_if"
ip link set "$ns_if" netns "$namespace"
ip addr add 10.210.71.1/30 dev "$host_if"
ip link set "$host_if" up
ip netns exec "$namespace" ip addr add 10.210.71.2/30 dev "$ns_if"
ip netns exec "$namespace" ip link set lo up
ip netns exec "$namespace" ip link set "$ns_if" up

# Exercise IPv6 as well when the CI kernel permits IPv6 on fresh veth links.
# Do not silently claim IPv6 coverage if the runner has disabled IPv6.
ipv6_enabled=0
if ip -6 addr add fd77:210:71::1/64 dev "$host_if" &&
   ip netns exec "$namespace" ip -6 addr add fd77:210:71::2/64 dev "$ns_if"; then
    ipv6_enabled=1
    sleep 2
else
    echo "::warning::Host kernel does not allow IPv6 veth addresses; IPv6 on-wire acceptance is unverified"
fi

# Reuse the exact nft rules emitted by the shipped opt-in kill-switch
# script. Never call its enable action on the CI host.
sed -n '/^table inet relayproxy_dns_guard {/,/^}$/p' scripts/dns-killswitch-linux.sh > "$work/guard.nft"
grep -q 'meta l4proto' "$work/guard.nft"

probe() {
    ip netns exec "$namespace" python3 - "$ipv6_enabled" <<'PY'
import socket
import sys
targets = [(socket.AF_INET, "10.210.71.1")]
if sys.argv[1] == "1":
    targets.append((socket.AF_INET6, "fd77:210:71::1"))
for family, target in targets:
    for port in (53, 853, 784, 8853):
        with socket.socket(family, socket.SOCK_DGRAM) as sock:
            sock.sendto(b"relayproxy-dns-guard-probe", (target, port))
    for port in (53, 853):
        with socket.socket(family, socket.SOCK_STREAM) as sock:
            sock.settimeout(0.2)
            try:
                sock.connect((target, port))
            except OSError:
                pass
PY
}

capture() {
    local pcap="$1"
    tcpdump -n -i "$host_if" -U -w "$pcap" '((tcp or udp) and (dst port 53 or dst port 853 or dst port 784 or dst port 8853))' > "$work/tcpdump.log" 2>&1 &
    capture_pid=$!
    sleep 1
    probe
    sleep 1
    kill "$capture_pid" 2>/dev/null || true
    wait "$capture_pid" 2>/dev/null || true
    capture_pid=""
    tcpdump -n -r "$pcap" 2>/dev/null | wc -l
}

baseline="$(capture "$work/baseline.pcap")"
if [[ "$baseline" -lt 4 ]]; then
    echo "Baseline veth capture did not observe expected unguarded packets ($baseline)" >&2
    exit 1
fi
echo "Observed $baseline actual DNS-port packets in the unguarded isolated namespace."
if [[ "$ipv6_enabled" -eq 1 ]]; then
    baseline_v6="$(tcpdump -n -r "$work/baseline.pcap" 'ip6' 2>/dev/null | wc -l)"
    if [[ "$baseline_v6" -lt 4 ]]; then
        echo "IPv6 veth baseline could not observe probes ($baseline_v6)" >&2
        exit 1
    fi
    echo "Observed $baseline_v6 real IPv6 DNS-port packets before installing the guard."
fi

ip netns exec "$namespace" nft -f "$work/guard.nft"
ip netns exec "$namespace" nft list table inet relayproxy_dns_guard
blocked="$(capture "$work/guarded.pcap")"
if [[ "$blocked" -ne 0 ]]; then
    echo "DNS guard allowed $blocked on-wire packets to escape isolated namespace" >&2
    exit 1
fi
echo "PASS: nftables guard stopped every tested UDP 53/853/784/8853 and TCP 53/853 packet before the veth; baseline capture proved observability."
if [[ "$ipv6_enabled" -eq 1 ]]; then
    echo "PASS: IPv6 tested with fd77:210:71::/64 in addition to IPv4."
fi
