package direct

import (
	"errors"
	"strings"
	"sync"

	"relayproxy/internal/protocol"
)

type AuthorizationState struct {
	PolicyRevision        int64
	AuthorizationRevision int64
}

type AuthorizationStore struct {
	mu             sync.RWMutex
	exitDeviceID   string
	policyRevision int64
	clients        map[string]AuthorizationState
}

func NewAuthorizationStore(exitDeviceID string, policyRevision int64) *AuthorizationStore {
	return &AuthorizationStore{
		exitDeviceID:   strings.TrimSpace(exitDeviceID),
		policyRevision: policyRevision,
		clients:        make(map[string]AuthorizationState),
	}
}

func (s *AuthorizationStore) Update(update protocol.PublicDirectAuthorizationUpdate) error {
	if s == nil {
		return errors.New("public direct authorization store is unavailable")
	}
	clientDeviceID := strings.TrimSpace(update.ClientDeviceID)
	exitDeviceID := strings.TrimSpace(update.ExitDeviceID)
	if clientDeviceID == "" || exitDeviceID == "" || clientDeviceID == exitDeviceID {
		return errors.New("invalid public direct authorization update")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if exitDeviceID != s.exitDeviceID || update.PolicyRevision != s.policyRevision {
		return errors.New("public direct authorization update is stale")
	}
	if !update.Authorized {
		delete(s.clients, clientDeviceID)
		return nil
	}
	if update.AuthorizationRevision < 0 {
		return errors.New("public direct authorization revision is invalid")
	}
	s.clients[clientDeviceID] = AuthorizationState{
		PolicyRevision:        update.PolicyRevision,
		AuthorizationRevision: update.AuthorizationRevision,
	}
	return nil
}

func (s *AuthorizationStore) Resolve(clientDeviceID string) (int64, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.RLock()
	state, ok := s.clients[strings.TrimSpace(clientDeviceID)]
	s.mu.RUnlock()
	if !ok {
		return 0, false
	}
	return state.AuthorizationRevision, true
}

func (s *AuthorizationStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.clients = make(map[string]AuthorizationState)
	s.mu.Unlock()
}
