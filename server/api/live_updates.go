package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"relayproxy/server/repository"
)

// Live updates carry revision invalidations ONLY. Subscribers must use the
// existing authenticated, identity-filtered APIs to load current state; no
// device inventory, credentials or private application data travel in SSE.
type liveUpdates struct {
	mu            sync.Mutex
	revision      uint64
	scopeRevision map[string]uint64
	subscribers   map[chan uint64]liveSubscriber
}
type liveSubscriber struct {
	identityID string
	admin      bool
}

const maxLiveSubscribers = 128

func newLiveUpdates() *liveUpdates {
	return &liveUpdates{
		scopeRevision: make(map[string]uint64),
		subscribers: make(map[chan uint64]liveSubscriber),
	}
}

func (h *liveUpdates) subscribe(identityID string, admin bool) (<-chan uint64, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subscribers) >= maxLiveSubscribers {
		return nil, nil, false
	}
	ch := make(chan uint64, 1)
	h.subscribers[ch] = liveSubscriber{identityID: identityID, admin: admin}
	return ch, func() {
		h.mu.Lock()
		delete(h.subscribers, ch)
		h.mu.Unlock()
	}, true
}

// Empty scope broadcasts to administrators only. An identity-scoped event
// reaches that identity and administrators; a different identity receives
// neither the revision nor a change in its SSE cadence.
func (h *liveUpdates) publish(identityID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.revision++
	if identityID != "" {
		h.scopeRevision[identityID]++
	}
	for ch, subscriber := range h.subscribers {
		if !subscriber.admin && (identityID == "" || subscriber.identityID != identityID) {
			continue
		}
		// Identity subscribers see only their OWN revision sequence, never
		// gaps revealing how often another identity changed its state.
		revision := h.revision
		if !subscriber.admin {
			revision = h.scopeRevision[identityID]
		}
		select {
		case ch <- revision:
		default:
			// Keep the MOST RECENT revision when a slow subscriber misses
			// intermediate invalidations. The buffer remains bounded and a
			// canonical read always supersedes every earlier revision.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- revision:
			default:
			}
		}
	}
}

func (r *Router) handleLiveEvents(w http.ResponseWriter, req *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	user, ok := req.Context().Value(principalContextKey).(*repository.User)
	if !ok || user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ch, unsubscribe, ok := r.live.subscribe(user.IdentityID, user.Role == "admin")
	if !ok {
		http.Error(w, "too many live subscribers", http.StatusServiceUnavailable)
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	pulse := time.NewTicker(25 * time.Second)
	defer pulse.Stop()
	for {
		select {
		case <-req.Context().Done():
			return
		case revision := <-ch:
			payload, _ := json.Marshal(map[string]uint64{"revision": revision})
			if _, err := fmt.Fprintf(w, "event: invalidate\ndata: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-pulse.C:
			// A disabled user or revoked login must not hold an authorized
			// long-lived subscription until the browser happens to reconnect.
			cookie, err := requestSessionCookie(req)
			if err != nil {
				return
			}
			id, valid := r.authService.Authenticate(cookie.Value)
			if !valid || id != user.ID {
				return
			}
			current, err := r.db.GetUserByID(id)
			if err != nil || current.Status != "active" || current.Role != user.Role ||
				current.IdentityID != user.IdentityID {
				return
			}
			if _, err = fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

type mutationStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *mutationStatusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *mutationStatusWriter) Write(content []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(content)
}
func (w *mutationStatusWriter) succeeded() bool {
	return w.status == 0 || w.status >= 200 && w.status < 300
}
