FROM alpine:3.24

# iptables serves local transparent proxying; the LAN gateway uses nft and
# the full iproute2 implementation (BusyBox ip does not support policy rules).
RUN apk add --no-cache ca-certificates iptables nftables iproute2

WORKDIR /data

# The host supplies the statically linked Linux binary and the complete
# writable configuration directory, including device-identity.json.
ENV RELAY_AGENT_CONFIG_DIR=/data
VOLUME ["/data"]
ENTRYPOINT ["/opt/relayproxy/bin/relay-agent", "--no-gui"]
CMD []
