package browsersync

import "testing"

func TestBrowserWSSPresenceInvalidationOnlyForCurrentSession(t *testing.T) {
	h := NewHandler(nil, nil)
	count := 0
	h.SetPresenceNotifier(func() { count++ })
	a := &browserConnection{}
	b := &browserConnection{}
	h.attach("device", a)
	if count != 1 {
		t.Fatalf("expected attach invalidation, got %d", count)
	}
	h.detach("device", a)
	if count != 2 {
		t.Fatalf("expected detach invalidation, got %d", count)
	}
	h.attach("device", b)
	if count != 3 {
		t.Fatalf("expected second attach invalidation, got %d", count)
	}
	h.detach("device", a)
	if count != 3 {
		t.Fatalf("stale socket detach invalidated current generation: %d", count)
	}
	if h.destination("device") != b {
		t.Fatal("stale detach removed active browser")
	}
	h.detach("device", b)
	if count != 4 || h.destination("device") != nil {
		t.Fatalf("last detach did not notify admin: %d", count)
	}
}
