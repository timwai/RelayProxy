package p2p

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"relayproxy/internal/protocol"
)

func TestLeaseRenewalRecoversWithinGrantedLifetime(t *testing.T) {
	for _, failure := range []string{"network", "authorization lookup"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				m := NewManager(context.Background(), func(_ context.Context, message protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
					if calls.Add(1) == 1 {
						if failure == "network" {
							return protocol.RDPControlMessage{}, errors.New("temporary signaling failure")
						}
						return protocol.RDPControlMessage{Type: protocol.RDPControlError, ErrorCode: "AUTH_UNAVAILABLE"}, nil
					}
					return protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck, SessionID: message.SessionID, LeaseExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, nil
				}, "", time.Minute, "")
				defer m.Close()
				s := m.newSession(42, "controller", "target", []byte("0123456789abcdef"), nil, 0)
				originalExpiry := s.ExpiresAt.Load()
				time.Sleep(20 * time.Second)
				synctest.Wait()
				select {
				case <-s.closed:
					t.Fatal("one transient renewal failure closed an authorized session")
				default:
				}
				if calls.Load() != 1 || s.ExpiresAt.Load() != originalExpiry {
					t.Fatalf("failed renewal changed lifetime: calls=%d expires=%d", calls.Load(), s.ExpiresAt.Load())
				}
				time.Sleep(time.Second)
				synctest.Wait()
				if calls.Load() != 2 || s.ExpiresAt.Load() <= originalExpiry {
					t.Fatalf("renewal did not recover promptly: calls=%d expires=%d", calls.Load(), s.ExpiresAt.Load())
				}
			})
		})
	}
}

func TestLeaseRenewalFailureCannotOutliveAuthorization(t *testing.T) {
	for _, failure := range []string{"network", "blocked", "wrong session", "expired ack", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := NewManager(context.Background(), func(ctx context.Context, message protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
					switch failure {
					case "blocked":
						<-ctx.Done()
						return protocol.RDPControlMessage{}, ctx.Err()
					case "wrong session":
						return protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck, SessionID: message.SessionID + 1, LeaseExpiresAt: time.Now().Add(time.Hour).UnixMilli()}, nil
					case "expired ack":
						return protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck, SessionID: message.SessionID, LeaseExpiresAt: time.Now().UnixMilli()}, nil
					case "malformed":
						return protocol.RDPControlMessage{}, nil
					default:
						return protocol.RDPControlMessage{}, errors.New("unreachable")
					}
				}, "", 15*time.Second, "")
				defer m.Close()
				s := m.newSession(42, "controller", "target", []byte("0123456789abcdef"), nil, 0)
				time.Sleep(14 * time.Second)
				synctest.Wait()
				select {
				case <-s.closed:
					t.Fatal("session was closed before its granted lease expired")
				default:
				}
				time.Sleep(time.Second)
				synctest.Wait()
				select {
				case <-s.closed:
				default:
					t.Fatal("failed renewal kept an expired authorization alive")
				}
				m.mu.Lock()
				remaining := len(m.sessions)
				m.mu.Unlock()
				if remaining != 0 {
					t.Fatal("expired session remained registered")
				}
			})
		})
	}
}

func TestLeaseRenewalRejectionClosesImmediately(t *testing.T) {
	for _, code := range []string{protocol.ErrCodeAccessDenied, "SESSION_TOKEN_INVALID", "SESSION_NOT_FOUND"} {
		t.Run(code, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := NewManager(context.Background(), func(context.Context, protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
					return protocol.RDPControlMessage{Type: protocol.RDPControlError, ErrorCode: code}, nil
				}, "", time.Minute, "")
				defer m.Close()
				s := m.newSession(42, "controller", "target", []byte("0123456789abcdef"), nil, 0)
				var closed atomic.Bool
				s.SetOnClose(func() { closed.Store(true) })
				time.Sleep(20 * time.Second)
				synctest.Wait()
				m.mu.Lock()
				remaining := len(m.sessions)
				m.mu.Unlock()
				if !closed.Load() || remaining != 0 {
					t.Fatal("explicit rejection did not immediately close the RDP session")
				}
			})
		})
	}
}
