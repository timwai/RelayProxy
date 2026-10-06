// Package androidcore exposes the RelayProxy exit-node data plane through
// gomobile. Android UI code should use NewClient, Start, Stop and StatusJSON.
//
// The Android VPN reuses the shared agent/client dialer. In particular,
// ordinary UDP/443 is carried by the reliable framed UDP stream so nested
// HTTP/3 handshakes don't depend on RelayProxy native datagram fragmentation;
// routes that explicitly require native datagrams keep that mode.
package androidcore
