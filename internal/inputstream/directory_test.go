package inputstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestReadableDirectoryAdmissionSnapshotAndFairExposure(t *testing.T) {
	r := openTestRepo(t, t.TempDir(), approvalCodec{provider: "a"}, approvalCodec{provider: "b"}, approvalCodec{provider: "c"})
	for _, provider := range []string{"a", "b", "c"} {
		for i := 0; i < 12; i++ {
			consumeRecord(t, r, provider, fmt.Sprintf("%s-%d", provider, i), fmt.Sprintf("flow-%d", i), uint64(i+1))
		}
	}
	snap, _ := r.Snapshot("a")
	o := Observation{Blocked: map[Location]string{{snap.Partition, 1}: "private admission reason"}}
	c, err := r.Catalog(1, o)
	if err != nil {
		t.Fatal(err)
	}
	first, exposure, err := c.Directory()
	if err != nil || first.Pending != 35 || len(first.Streams) > 5 || len(exposure.Sources) < 2 {
		t.Fatal(first, exposure, err)
	}
	b, _ := json.Marshal(first)
	for _, word := range []string{"blocked", "private admission", "scope_ref", "guidance", "source_version", "BACKGROUND", "saved evidence"} {
		if strings.Contains(string(b), word) {
			t.Fatal("directory leak", word)
		}
	}
	if len(b) > DirectoryBytes {
		t.Fatal(len(b))
	}
	second, err := c.ReadableList(ListRequest{Cursor: first.Next})
	if err != nil {
		t.Fatal(err)
	}
	preference := Preferences{Attention: map[string]string{exposure.LastSource: "high"}, LastExposed: map[string]uint64{}}
	hot, err := r.Catalog(1, Observation{Blocked: o.Blocked, Preferences: preference})
	if err != nil {
		t.Fatal(err)
	}
	updated, _, err := hot.Directory()
	if err != nil || len(updated.Streams) == 0 || updated.Streams[0].Attention != "high" || updated.Pending != first.Pending {
		t.Fatal("a new observation must apply the saved priority to the same available inputs", updated, err)
	}
	unchanged, err := c.ReadableList(ListRequest{Cursor: first.Next})
	if err != nil || second.Pending != unchanged.Pending || second.Stop != unchanged.Stop ||
		(second.Next == "") != (unchanged.Next == "") || !reflect.DeepEqual(second.Streams, unchanged.Streams) {
		t.Fatal("a hot priority change altered an existing cursor", err)
	}
	allExposed := map[string]bool{}
	for turn := uint64(2); turn <= 4; turn++ {
		current, err := r.Catalog(turn, Observation{Blocked: o.Blocked, Preferences: preference})
		if err != nil {
			t.Fatal(err)
		}
		_, shown, err := current.Directory()
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range shown.Sources {
			allExposed[source] = true
			preference.LastExposed[source] = turn
		}
		preference.LastSource = shown.LastSource
	}
	if len(allExposed) != 3 {
		t.Fatal("backlogged normal sources lost their exposure opportunity", len(allExposed))
	}
	seen := map[string]bool{}
	for _, s := range first.Streams {
		seen[s.Ref] = true
	}
	consumeRecord(t, r, "a", "after", "later", 13)
	cursor := first.Next
	for cursor != "" {
		page, err := c.ReadableList(ListRequest{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if page.Pending != 35 {
			t.Fatal("snapshot changed")
		}
		b, _ := json.Marshal(page)
		if len(b) > PageBytes {
			t.Fatal(len(b))
		}
		for _, s := range page.Streams {
			if seen[s.Ref] {
				t.Fatal("repeated stream")
			}
			seen[s.Ref] = true
		}
		cursor = page.Next
	}
	if len(seen) != 35 {
		t.Fatal("tail hidden", len(seen))
	}
	again, againExposure, err := c.Directory()
	if err != nil || !reflect.DeepEqual(first, again) || !reflect.DeepEqual(exposure, againExposure) {
		t.Fatal("directory changed")
	}
	refreshed, err := r.Catalog(1, o)
	if err != nil {
		t.Fatal(err)
	}
	d, _, _ := refreshed.Directory()
	if d.Pending != 36 {
		t.Fatal("fresh observation missed append")
	}
	if _, err := c.ReadableList(ListRequest{Cursor: "expired"}); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if _, err := c.ReadableList(ListRequest{Cursor: first.Next, Limit: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
func TestSimpleLimitsRejectWhitespaceReferences(t *testing.T) {
	for _, data := range []string{`{"stream_ref":" "}`, `{"stream_ref":"x","limit":null}`, `{"stream_ref":"x","limit":0}`, `{"stream_ref":"x","limit":1.5}`} {
		if _, err := DecodeStreamRead([]byte(data)); err == nil {
			t.Fatal(data)
		}
	}
}

type validationOnlyCodec struct{ approvalCodec }

func (validationOnlyCodec) ValidateStoredRecord(rec StoredRecord) error {
	if rec.Channel != "changes" {
		return ErrIdentity
	}
	return nil
}
func TestStorageReopensWithOnlyDirectoryProjection(t *testing.T) {
	codec := validationOnlyCodec{approvalCodec{provider: "approval"}}
	dir := t.TempDir()
	r := openTestRepo(t, dir, codec)
	appendTest(t, r, "approval", request("valid", "A", "node", "saved evidence"))
	r = openTestRepo(t, dir, codec)
	for _, part := range r.parts {
		for _, record := range part.records {
			if !strings.Contains(string(record.Payload), "saved evidence") {
				t.Fatal("immutable body lost", record.Location)
			}
		}
	}
	c, err := r.Catalog(1, Observation{})
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := c.Directory()
	if err != nil || d.Pending != 1 {
		t.Fatal(d, err)
	}
	q := request("wrong", "B", "node", "wrong envelope")
	q.Channel = "decisions"
	if _, err := r.AppendOnce("approval", q); !errors.Is(err, ErrIdentity) {
		t.Fatal("adapter envelope validation lost", err)
	}
	usage, _ := r.InspectUsage()
	if usage.Records != 1 {
		t.Fatal("invalid record published", usage)
	}
}
