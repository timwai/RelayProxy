package punch

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"relayproxy/internal/p2p/secure"
)

func TestSendPunchRoundTracksEachAuthenticatedNonce(t *testing.T) {
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	address := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(receiver.LocalAddr().(*net.UDPAddr).Port))
	key := []byte("0123456789abcdef0123456789abcdef")
	pending := map[uint64]time.Time{}
	nextNonce := uint64(100)
	if err := sendPunchRound(sender, 999, key, []netip.AddrPort{address}, &nextNonce, pending); err != nil {
		t.Fatal(err)
	}
	if nextNonce != 101 || pending[101].IsZero() {
		t.Fatalf("first round did not track transmitted nonce: next=%d pending=%v", nextNonce, pending)
	}
	buffer := make([]byte, 256)
	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := receiver.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := secure.DecodePunchPacket(buffer[:n], key)
	if err != nil || decoded.Type != secure.PunchRequest || decoded.Nonce != 101 || decoded.SessionID != 999 {
		t.Fatalf("sent packet nonce mismatch: %+v error=%v", decoded, err)
	}
	if err := sendPunchRound(sender, 999, key, []netip.AddrPort{address}, &nextNonce, pending); err != nil {
		t.Fatal(err)
	}
	if nextNonce != 102 || pending[102].Before(pending[101]) {
		t.Fatalf("nonce/send timestamps not fresh per round: %d %+v", nextNonce, pending)
	}
}
