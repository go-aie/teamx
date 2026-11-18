package inputstream

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSegmentPublishedPrefixAndRecordBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "segment.jsonl")
	body := []byte("{\"seq\":1}\n{\"seq\":2}\n")
	if err := os.WriteFile(path, append(append([]byte{}, body...), []byte("complete-but-unpublished\npartial")...), 0600); err != nil {
		t.Fatal(err)
	}
	bounds := SegmentRange{1, 2, int64(len(body)), Digest(body)}
	data, err := ReadSegment(path, bounds, 1024)
	if err != nil || string(data) != string(body) {
		t.Fatal(string(data), err)
	}
	var offsets []int
	if err := WalkSegment(data, 10, func(_ []byte, at int) error { offsets = append(offsets, at); return nil }); err != nil || len(offsets) != 2 || offsets[1] != 10 {
		t.Fatal(offsets, err)
	}
	if err := WalkSegment(data, 9, func([]byte, int) error { return nil }); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	if err := WalkSegment([]byte("unterminated"), 100, func([]byte, int) error { return nil }); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	if err := TruncateSegment(path, bounds.Bytes); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != bounds.Bytes {
		t.Fatal(info, err)
	}
	if err := os.WriteFile(path, []byte("short\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSegment(path, bounds, 1024); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"seq\":9}\n{\"seq\":2}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSegment(path, bounds, 1024); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

func TestPublicationSequenceRules(t *testing.T) {
	for _, tc := range []struct {
		name              string
		floor, latest     uint64
		ranges            []SegmentRange
		contiguous, valid bool
	}{
		{"empty", 4, 4, nil, true, true},
		{"missing", 4, 5, nil, true, false},
		{"reversed floor", 5, 4, nil, false, false},
		{"contiguous", 4, 6, []SegmentRange{{5, 6, 20, "sum"}}, true, true},
		{"room gap", 4, 6, []SegmentRange{{6, 6, 20, "sum"}}, true, false},
		{"import gaps", 4, 8, []SegmentRange{{6, 6, 20, "sum"}}, false, true},
		{"overlap", 4, 8, []SegmentRange{{5, 6, 20, "sum"}, {6, 8, 20, "sum"}}, false, false},
		{"beyond root", 4, 5, []SegmentRange{{5, 6, 20, "sum"}}, false, false},
		{"size", 0, 1, []SegmentRange{{1, 1, MaxBytes + 1, "sum"}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatePublication(tc.floor, tc.latest, tc.ranges, tc.contiguous)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

type failingSegmentWriter struct {
	short             bool
	syncErr, closeErr error
	synced, closed    bool
}

func (f *failingSegmentWriter) Write(b []byte) (int, error) {
	if f.short {
		return len(b) - 1, nil
	}
	return len(b), nil
}
func (f *failingSegmentWriter) Sync() error  { f.synced = true; return f.syncErr }
func (f *failingSegmentWriter) Close() error { f.closed = true; return f.closeErr }
func TestSegmentWriteFailureNeverSkipsClose(t *testing.T) {
	injected := errors.New("injected")
	for _, f := range []*failingSegmentWriter{{short: true}, {syncErr: injected}, {closeErr: injected}, {}} {
		err := WriteSegment(f, []byte("body\n"))
		if !f.closed || f.short && (!errors.Is(err, io.ErrShortWrite) || f.synced) || f.syncErr != nil && !errors.Is(err, injected) || f.closeErr != nil && !errors.Is(err, injected) {
			t.Fatal(f, err)
		}
	}
}

func TestConfirmationRejectsMalformedPhysicalRanges(t *testing.T) {
	for _, entries := range [][]Location{{{"p", 2}, {"p", 1}}, {{"p", 1}, {"p", 1}}, {{"p", 3}}, {{"other", 1}}} {
		if err := ValidateReadRange("p", 0, 2, 2, entries); err == nil {
			t.Fatal(entries)
		}
	}
	if ValidateReadRange("p", 3, 2, 4, nil) == nil || ValidateReadRange("p", 0, 3, 2, nil) == nil {
		t.Fatal("invalid range accepted")
	}
	if ValidateCheckpoint(0, 3, 4) != nil || ValidateCheckpoint(5, 4, 4) == nil {
		t.Fatal("compacted checkpoint comparison")
	}
}
