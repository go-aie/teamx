package room

// Older snapshots may carry a digest written during format conversion. It was
// not independent evidence and is no longer part of the association model.
// Accept it only at the snapshot boundary; business and API types stay plain.
func readFollowupEventSnapshot(path string) (followupEvent, error) {
	var stored struct {
		followupEvent
		RetiredProof string `json:"reclaimed_source_proof"`
	}
	err := readSnapshot(path, &stored, followupBytes)
	return stored.followupEvent, err
}
