package notification

import (
	"testing"
	"time"
)

func TestBackfillChoicesAndValidation(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, seconds := range []int{0, 3600, 86400} {
		start, err := BackfillBaseline(seconds, time.Time{}, now)
		if err != nil || !start.Equal(now.Add(-time.Duration(seconds)*time.Second)) {
			t.Fatal(seconds, start, err)
		}
	}
	local := now.Add(-time.Hour).In(time.FixedZone("local", 8*3600)).Add(123 * time.Millisecond)
	start, err := BackfillBaseline(0, local, now)
	if err != nil || start.Location() != time.UTC || !start.Equal(now.Add(-time.Hour)) {
		t.Fatal(start, err)
	}
	for _, choice := range []struct {
		seconds int
		start   time.Time
	}{
		{-1, time.Time{}}, {86401, time.Time{}}, {3600, now}, {0, time.Unix(0, 0)}, {0, now.Add(time.Second)},
	} {
		if _, err := BackfillBaseline(choice.seconds, choice.start, now); err == nil {
			t.Fatal("invalid reception start accepted", choice)
		}
	}
}
