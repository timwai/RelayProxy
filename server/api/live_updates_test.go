package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relayproxy/server/repository"
)

func TestLiveEventsSurviveServerWriteTimeout(t *testing.T) {
	r := &Router{live: newLiveUpdates()}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user := &repository.User{IdentityID: "identity", Role: "admin"}
		req = req.WithContext(context.WithValue(req.Context(), principalContextKey, user))
		r.handleLiveEvents(w, req)
	}))
	srv.Config.WriteTimeout = 50 * time.Millisecond
	srv.Start()
	defer srv.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != ": connected\n" {
		t.Fatalf("initial SSE event: %q, %v", line, err)
	}
	// Keep the HTTP response open beyond the server-wide absolute deadline.
	time.Sleep(150 * time.Millisecond)
	r.live.publish("")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("SSE ended at the global WriteTimeout: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			if !strings.Contains(line, `"revision":1`) {
				t.Fatalf("invalid event: %q", line)
			}
			break
		}
	}
}

func TestLiveUpdatesScopeIsolationAndAdminDelivery(t *testing.T) {
	h := newLiveUpdates()
	admin, cancelAdmin, ok := h.subscribe("", true)
	if !ok {
		t.Fatal("admin subscribe denied")
	}
	defer cancelAdmin()
	a, cancelA, ok := h.subscribe("identity_a", false)
	if !ok {
		t.Fatal("identity A subscribe denied")
	}
	defer cancelA()
	b, cancelB, ok := h.subscribe("identity_b", false)
	if !ok {
		t.Fatal("identity B subscribe denied")
	}
	defer cancelB()

	h.publish("identity_a")
	select {
	case <-a:
	default:
		t.Fatal("identity A missed own notification")
	}
	select {
	case <-admin:
	default:
		t.Fatal("admin missed identity notification")
	}
	select {
	case <-b:
		t.Fatal("cross-identity activity leaked to B")
	default:
	}
	h.publish("")
	select {
	case <-admin:
	default:
		t.Fatal("admin missed global mutation")
	}
	select {
	case <-a:
		t.Fatal("identity subscriber received admin-only activity")
	default:
	}
	select {
	case <-b:
		t.Fatal("unrelated subscriber received admin-only activity")
	default:
	}
}

func TestLiveUpdatesSlowConsumersCannotBlockAuthorizationMutations(t *testing.T) {
	h := newLiveUpdates()
	ch, done, ok := h.subscribe("id1", false)
	if !ok {
		t.Fatal("subscribe failed")
	}
	// The subscriber stops reading, but publishing only queues a single
	// revalidation hint, never unbounded logs or cross-identity data.
	for i := 0; i < 1000; i++ {
		h.publish("id1")
	}
	select {
	case value := <-ch:
		if value != 1000 {
			t.Fatalf("slow consumer observed stale revision %d, expected 1000", value)
		}
	default:
		t.Fatal("no bounded invalidation was queued")
	}
	done()
	h.publish("id1") // unsubscribed event cannot enqueue or panic
	if len(h.subscribers) != 0 {
		t.Fatal("subscriber leaked after unsubscribe")
	}
}

func TestLiveUpdatesGlobalConnectionBudget(t *testing.T) {
	h := newLiveUpdates()
	releases := make([]func(), 0, maxLiveSubscribers)
	for i := 0; i < maxLiveSubscribers; i++ {
		_, done, ok := h.subscribe("same_identity", false)
		if !ok {
			t.Fatalf("unexpected limit at %d", i)
		}
		releases = append(releases, done)
	}
	if _, _, ok := h.subscribe("", true); ok {
		t.Fatal("unbounded SSE subscribers allowed")
	}
	for _, done := range releases {
		done()
	}
	if _, done, ok := h.subscribe("", true); !ok {
		t.Fatal("subscriber budget was not released")
	} else {
		done()
	}
}

func TestLiveUpdatesScopedRevisionsHideCrossIdentityActivity(t *testing.T) {
	h := newLiveUpdates()
	a, release, ok := h.subscribe("identity_a", false)
	if !ok {
		t.Fatal("subscription denied")
	}
	defer release()
	h.publish("identity_a")
	if got := <-a; got != 1 {
		t.Fatalf("first scoped revision = %d", got)
	}
	for i := 0; i < 17; i++ {
		h.publish("identity_b")
	}
	h.publish("") // admin-only events are also private
	h.publish("identity_a")
	if got := <-a; got != 2 {
		t.Fatalf("cross-identity event count leaked through revision gap: %d", got)
	}
}
