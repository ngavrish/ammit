package main

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// Run 6d42457b: the writer was held for ~400s, every record waited behind it,
// and the pulse read the moment the wait ended was our own deafness. A record
// that waited past deafAfter for the writer moves the absence clock; one that
// did not leaves it where it was.
func TestStoreWaitRearmsAbsence(t *testing.T) {
	if err := openDB(filepath.Join(t.TempDir(), "ammit.db")); err != nil {
		t.Fatal(err)
	}
	was := _deafAfter.Load()
	defer _deafAfter.Store(was)
	_deafAfter.Store(math.Float64bits(0.05))

	old := nowWall() - 1000
	_upSinceBits.Store(math.Float64bits(old))
	store(event{"kind": "note", "run": "absence-test", "text": "no wait"})
	if got := upSince(); got != old {
		t.Fatalf("a record that did not wait moved the absence clock: %v -> %v", old, got)
	}

	mu.Lock()
	go func() {
		time.Sleep(200 * time.Millisecond)
		mu.Unlock()
	}()
	store(event{"kind": "heartbeat", "run": "absence-test"})
	if up := nowWall() - upSince(); up > 5 {
		t.Fatalf("a record that waited for the writer left the absence clock %.0fs old", up)
	}
}
