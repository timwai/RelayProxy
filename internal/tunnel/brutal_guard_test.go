package tunnel

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestBrutalRTTGuardRequiresContinuousLoadedInflation(t *testing.T) {
	start := time.Unix(100, 0)
	const baseline = 10 * time.Millisecond
	var guard brutalRTTGuard
	for second := 0; second <= 9; second++ {
		// One loaded RTT spike at 5 seconds is not a sustained reason
		// to downgrade, and the guard must reset after its recovery.
		rtt := 20 * time.Millisecond
		if second == 5 || second == 7 || second == 8 {
			rtt = 150 * time.Millisecond
		}
		if guard.sample(start.Add(time.Duration(second)*time.Second),
			baseline, rtt, uint64(second)*128*1024) {
			t.Fatalf("guard fired after discontinuous RTT spikes at second %d", second)
		}
	}
	for second := 10; second <= 12; second++ {
		if fired := guard.sample(start.Add(time.Duration(second)*time.Second),
			baseline, 150*time.Millisecond, uint64(second)*128*1024); fired != (second == 12) {
			t.Fatalf("unexpected circuit-breaker state after %d consecutive loaded samples: %v", second-9, fired)
		}
	}
}

func TestBrutalRTTGuardDoesNotTriggerOnIdleOrTransientLoss(t *testing.T) {
	start := time.Unix(100, 0)
	var guard brutalRTTGuard
	for second := 0; second < 30; second++ {
		bytes := uint64(second) * 128 * 1024
		rtt := 50 * time.Millisecond
		if second > 8 {
			bytes = 8 * 128 * 1024       // No sender traffic after 8 s.
			rtt = 500 * time.Millisecond // Late RTT spike on idle connection.
		}
		if guard.sample(start.Add(time.Duration(second)*time.Second),
			40*time.Millisecond, rtt, bytes) {
			t.Fatalf("idle session should never trip at second %d", second)
		}
	}
	var stable brutalRTTGuard
	for second := 0; second < 15; second++ {
		// High absolute RTT is legitimate on a long path if it has not
		// inflated relative to the minimum observed RTT.
		if stable.sample(start.Add(time.Duration(second)*time.Second),
			200*time.Millisecond, 250*time.Millisecond, uint64(second)*128*1024) {
			t.Fatal("high baseline RTT should not trip guard")
		}
	}
}

func TestBrutalLossGuardTripsOnlyOnSustainedSevereLoss(t *testing.T) {
	start := time.Unix(200, 0)
	var guard brutalLossGuard
	var sent, lost uint64
	for second := 0; second <= 8; second++ {
		sent += 200
		lost += 10 // Nominal 5% loss must not trip the guard.
		if guard.sample(start.Add(time.Duration(second)*time.Second), sent, lost) {
			t.Fatalf("routine 5%% loss triggered fallback at second %d", second)
		}
	}
	for second := 9; second <= 10; second++ {
		sent += 200
		lost += 60 // 30% newly lost packets, but only two samples.
		if guard.sample(start.Add(time.Duration(second)*time.Second), sent, lost) {
			t.Fatalf("short packet-loss spike triggered fallback at second %d", second)
		}
	}
	// Return to stable traffic resets the consecutive-loss window.
	sent += 200
	lost += 10
	if guard.sample(start.Add(11*time.Second), sent, lost) {
		t.Fatal("guard tripped on recovery sample")
	}
	for second := 12; second <= 14; second++ {
		sent += 200
		lost += 60
		if got, want := guard.sample(start.Add(time.Duration(second)*time.Second), sent, lost), second == 14; got != want {
			t.Fatalf("second=%d unexpected severe-loss trip=%v, want=%v", second, got, want)
		}
	}
}

func TestBrutalLossGuardIgnoresIdleAndSpuriousLossCorrection(t *testing.T) {
	start := time.Unix(300, 0)
	var guard brutalLossGuard
	if guard.sample(start, 1000, 300) {
		t.Fatal("initial cumulative losses must not trigger")
	}
	// Loss counter may go backwards after QUIC marks a loss spurious.
	if guard.sample(start.Add(6*time.Second), 1200, 200) {
		t.Fatal("decreasing loss counter triggered fallback")
	}
	for second := 7; second < 20; second++ {
		if guard.sample(start.Add(time.Duration(second)*time.Second),
			1200, 200+uint64(second*20)) {
			t.Fatalf("idle packet-loss updates triggered guard at second %d", second)
		}
	}
}

func TestBrutalRTTGuardRevertsRealQUICToBBROnce(t *testing.T) {
	client, server := sessionPair(t, "quic")
	if !UseBrutal(server, 2_000_000, false) {
		t.Fatal("cannot enable Brutal on live QUIC session")
	}
	if !server.(*QUICSession).fallbackFromBrutal("unit-test sustained queue inflation") {
		t.Fatal("first safety fallback failed")
	}
	if server.(*QUICSession).fallbackFromBrutal("repeated") {
		t.Fatal("fallback was not idempotent")
	}
	if UseBrutal(server, 3_000_000, false) {
		t.Fatal("tripped session re-enabled Brutal after fallback")
	}
	diag := DiagnoseSession(server)
	if diag == nil || diag.QUIC == nil ||
		diag.QUIC.CongestionController != "bbr-aggressive" ||
		diag.QUIC.CongestionTargetBPS != 0 ||
		diag.QUIC.BrutalFallbackReason != "unit-test sustained queue inflation" {
		t.Fatalf("fallback diagnostics were not retained: %+v", diag)
	}
	clientStream, serverStream := streamPair(t, client, server)
	defer clientStream.Close()
	defer serverStream.Close()
	payload := bytes.Repeat([]byte("live-bbr-after-brutal"), 100)
	if err := serverStream.SetWriteDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := clientStream.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := serverStream.Write(payload)
		done <- err
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(clientStream, got); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("data integrity failed after switching QUIC sender back to BBR")
	}
}
