package inputstream

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRetainedMetadataRebuildSkipsAndMetadataOnlyDiscovery(t *testing.T) {
	dir := t.TempDir()
	r := openTestRepo(t, dir)
	entries := []ImportEntry{}
	for _, row := range []struct {
		key, group string
		seq        uint64
		consumed   bool
	}{{"a", "A", 2, true}, {"b", "A", 4, false}, {"c", "B", 6, true}} {
		q := request(row.key, row.group, "node", "saved body")
		q.Seq, q.DeliveryDigest = row.seq, row.key
		q.ReceivedAt = storedTime.Add(time.Duration(row.seq) * time.Hour)
		entries = append(entries, ImportEntry{AppendRequest: q, Consumed: row.consumed})
	}
	if err := r.ImportPartition("approval", 0, 6, entries); err != nil {
		t.Fatal(err)
	}
	before, err := r.RetainedStreams()
	if err != nil || len(before) != 2 {
		t.Fatal(before, err)
	}
	if err := r.ExcludeLocal(Location{Partition: before[0].Snapshot.Partition, Seq: 4}, "verified skip"); err != nil {
		t.Fatal(err)
	}
	after, err := r.RetainedStreams()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("consumption changed retained metadata", after, err)
	}
	reopened := openTestRepo(t, dir)
	actual, err := reopened.RetainedStreams()
	if err != nil || !reflect.DeepEqual(before, actual) {
		t.Fatal("restart changed retained stream identities", actual, err)
	}
	// Metadata discovery does not touch the published bodies. Actual reads still
	// fail closed when bytes are missing instead of interpreting them as empty.
	for _, seg := range r.parts["approval"].state.Segments {
		if err := os.Remove(filepath.Join(r.partDir("approval"), "records", seg.Name)); err != nil {
			t.Fatal(err)
		}
	}
	if metadata, err := r.RetainedStreams(); err != nil || !reflect.DeepEqual(before, metadata) {
		t.Fatal(metadata, err)
	}
	l := before[0].Locations[0]
	if _, err := r.Read(before[0].Snapshot, l); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}
