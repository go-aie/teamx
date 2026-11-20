package room

import (
	"testing"
	"time"
)

// Bound process and durable I/O pressure even on machines with many CPUs.
// Call only for fixtures with private stores, no shared environment keys and
// no working-directory changes.
var fixtureTestSlots = make(chan struct{}, 4)

func parallelFixtureTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	waiting := time.Now()
	fixtureTestSlots <- struct{}{}
	acquired := time.Now()
	t.Cleanup(func() {
		<-fixtureTestSlots
		// Includes fixture cleanup; top-level Elapsed also includes the slot wait.
		t.Logf("fixture slot: wait=%s held=%s", acquired.Sub(waiting), time.Since(acquired))
	})
}
