//go:build linux || darwin

package singleton

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestAcquirePreventsDuplicateAgent(t *testing.T) {
	name := fmt.Sprintf("RelayProxyAgent_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	first, err := Acquire(name)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Acquire(name); err == nil {
		second.Release()
		first.Release()
		t.Fatal("duplicate Agent lock unexpectedly succeeded")
	}
	first.Release()

	again, err := Acquire(name)
	if err != nil {
		t.Fatalf("lock not released on shutdown: %v", err)
	}
	again.Release()
}
