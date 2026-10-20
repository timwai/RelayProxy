package singleton

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestStartupPreparationFailureReleasesNewLock(t *testing.T) {
	name := fmt.Sprintf("RelayProxyAgent_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	cancelled := errors.New("startup preparation cancelled")
	called := false
	mutex, err := AcquireForStartup(name, func(replacing bool) error {
		called = true
		if replacing {
			t.Error("first launch unexpectedly detected a predecessor")
		}
		return cancelled
	})
	if mutex != nil {
		mutex.Release()
		t.Fatal("cancelled preparation acquired an instance lock")
	}
	if !called || !errors.Is(err, cancelled) {
		t.Fatalf("preparation result: called=%t, err=%v", called, err)
	}
	// A failed first launch must release its newly created lock so retrying
	// startup cannot mistake it for an existing client or require elevation.
	mutex, err = AcquireForStartup(name, nil)
	if err != nil {
		t.Fatalf("could not retry after cancelled preparation: %v", err)
	}
	mutex.Release()
}
