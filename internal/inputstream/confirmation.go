package inputstream

// ValidateReadRange checks an opaque physical page. Adapters separately check
// execution authority, visibility and the exact expected records before commit.
func ValidateReadRange(partition string, from, through, snapshot uint64, entries []Location) error {
	if partition == "" || from > through || through > snapshot {
		return ErrConflict
	}
	previous := from
	for _, l := range entries {
		if l.Partition != partition {
			return ErrIdentity
		}
		if l.Seq <= previous || l.Seq > through {
			return ErrConflict
		}
		previous = l.Seq
	}
	return nil
}

// ValidateCheckpoint compares positions above a domain's published floor.
// Private partitions compact retired checkpoints; Room requires from >= floor
// before invoking this primitive and commits its sole cursor with its proof.
func ValidateCheckpoint(current, from, floor uint64) error {
	if max(current, floor) != max(from, floor) {
		return ErrConflict
	}
	return nil
}
