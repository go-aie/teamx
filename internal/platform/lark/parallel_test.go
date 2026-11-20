package lark

import (
	"testing"
	"time"
)

var clientTestSlots = make(chan struct{}, 4)

// Each caller owns a factory, CLI directory and account queues. Authentication
// tests that change process-wide environment remain serial.
func parallelClientTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	waiting := time.Now()
	clientTestSlots <- struct{}{}
	acquired := time.Now()
	t.Cleanup(func() {
		<-clientTestSlots
		// Includes fixture cleanup, but not the Go parallel scheduler pause.
		t.Logf("client slot: wait=%s held=%s", acquired.Sub(waiting), time.Since(acquired))
	})
}
