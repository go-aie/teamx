package room

import "testing"

// Bound process and durable I/O pressure even on machines with many CPUs.
// Call only for fixtures with private stores and no process-wide env changes.
var fixtureTestSlots = make(chan struct{}, 4)

func parallelFixtureTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	fixtureTestSlots <- struct{}{}
	t.Cleanup(func() { <-fixtureTestSlots })
}
