package inputstream

import (
	"bytes"
	"errors"
	"io"
	"os"
)

// SegmentRange describes only published bytes. Owners retain their native root
// encoding, file names, sequence rules and atomic publication authority.
type SegmentRange struct {
	First, Last uint64
	Bytes       int64
	Digest      string
}

// ValidatePublication checks the physical ranges before any body is decoded.
// Room requires contiguous sequences; imported platform partitions allow gaps.
func ValidatePublication(reclaimed, latest uint64, segments []SegmentRange, contiguous bool) (int64, error) {
	if reclaimed > latest || len(segments) > MaxRecords {
		return 0, ErrCorrupt
	}
	previous, total := reclaimed, int64(0)
	for _, s := range segments {
		if s.First <= previous || s.Last < s.First || s.Last > latest || s.Bytes <= 0 || s.Bytes > MaxBytes || s.Digest == "" || contiguous && s.First != previous+1 {
			return 0, ErrCorrupt
		}
		total += s.Bytes
		if total > MaxBytes {
			return 0, ErrCorrupt
		}
		previous = s.Last
	}
	if contiguous && previous != latest {
		return 0, ErrCorrupt
	}
	return total, nil
}

// ReadSegment never interprets or validates an unpublished tail. Recovery may
// truncate it only after the owning domain has validated its published root.
func ReadSegment(path string, s SegmentRange, maxBytes int64) ([]byte, error) {
	if s.First == 0 || s.Last < s.First || s.Bytes <= 0 || s.Bytes > maxBytes || maxBytes > MaxBytes {
		return nil, ErrCorrupt
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < s.Bytes {
		return nil, ErrCorrupt
	}
	b := make([]byte, int(s.Bytes))
	if _, err := io.ReadFull(f, b); err != nil {
		return nil, err
	}
	if b[len(b)-1] != '\n' || Digest(b) != s.Digest {
		return nil, ErrCorrupt
	}
	return b, nil
}

// WalkSegment enforces identical record byte boundaries for every adapter.
// Lines exclude the newline; offsets include all preceding newline bytes.
func WalkSegment(data []byte, maxRecordBytes int, visit func([]byte, int) error) error {
	for at := 0; at < len(data); {
		end := bytes.IndexByte(data[at:], '\n')
		if end < 0 || end+1 > maxRecordBytes {
			return ErrCorrupt
		}
		if err := visit(data[at:at+end], at); err != nil {
			return err
		}
		at += end + 1
	}
	return nil
}

type SegmentWriter interface {
	io.Writer
	Sync() error
	Close() error
}

// WriteSegment owns the handle. Callers position/truncate an append handle or
// supply a new file, then publish only after file and directory sync succeed.
func WriteSegment(f SegmentWriter, data []byte) error {
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// TruncateSegment discards unpublished bytes without rewriting retained data.
func TruncateSegment(path string, committed int64) error {
	if committed < 0 {
		return ErrCorrupt
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	info, statErr := f.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() < committed {
		return errors.Join(ErrCorrupt, statErr, f.Close())
	}
	err = f.Truncate(committed)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
