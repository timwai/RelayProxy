FROM alpine:3.24

# iptables serves local transparent proxying; the LAN gateway uses nft and
# the full iproute2 implementation (BusyBox ip does not support policy rules).
RUN apk add --no-cache ca-certificates iptables nftables iproute2

WORKDIR /data
