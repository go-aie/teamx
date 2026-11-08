package lark

import "testing"

var clientTestSlots = make(chan struct{}, 4)

// Each caller owns a factory, CLI directory and account queues. Authentication
// tests that change process-wide environment remain serial.
func parallelClientTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	clientTestSlots <- struct{}{}
	t.Cleanup(func() { <-clientTestSlots })
}
