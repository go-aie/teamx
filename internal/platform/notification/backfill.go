package notification

import "time"

// NormalizeBackfill is shared by notification providers. Relative lookback and
// an absolute start are mutually exclusive; absolute times use whole UTC seconds.
func NormalizeBackfill(seconds int, start time.Time) (time.Time, error) {
	if seconds < 0 || seconds > 86400 || !start.IsZero() && (seconds != 0 || start.Unix() <= 0) {
		return start, ErrRequest
	}
	if !start.IsZero() {
		start = start.UTC().Truncate(time.Second)
	}
	return start, nil
}

// BackfillBaseline resolves a choice once, when reception first starts.
func BackfillBaseline(seconds int, start, now time.Time) (time.Time, error) {
	start, err := NormalizeBackfill(seconds, start)
	if err != nil || start.After(now) {
		return time.Time{}, ErrRequest
	}
	if start.IsZero() {
		start = now.Add(-time.Duration(seconds) * time.Second)
	}
	return start, nil
}
