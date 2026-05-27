package main

import (
	"testing"
	"time"
)

// TestScoreboardRun exercises the scoreboard example end-to-end in-process.
// It runs `run()` in a goroutine with a hard 5s ceiling and asserts no error.
// The example self-cancels in about 1s, so 5s is comfortable headroom.
func TestScoreboardRun(t *testing.T) {
	done := make(chan error, 1)
	go func() { done <- run() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("scoreboard run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scoreboard run did not finish within 5s")
	}
}
