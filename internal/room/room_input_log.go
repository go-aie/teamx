package room

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/go-aie/teamx/internal/inputstream"
)

// room.json is the only publication authority. Segments have no separate root,
// cursor, transaction journal or replayed mutable state. Actor candidates keep
// materialized history in memory; state-only saves reuse every committed byte.
type roomLogRoot struct {
	Version  int              `json:"version"`
	Segments []roomLogSegment `json:"segments"`
	Records  int              `json:"records"`
	Bytes    int              `json:"bytes"`
}
type roomLogSegment struct {
	Name   string `json:"name"`
	First  uint64 `json:"first"`
	Last   uint64 `json:"last"`
	Bytes  int    `json:"bytes"`
	Digest string `json:"digest"`
}

type roomLogCache struct {
	Key      string
	Messages []RoomMessage
	Files    []os.FileInfo
}

func cloneRoomLogMessages(messages []RoomMessage) []RoomMessage {
	out := slices.Clone(messages)
	if out == nil {
		out = []RoomMessage{}
	}
	for i := range out {
		m := &out[i]
		m.Mentions = slices.Clone(m.Mentions)
		if m.Source != nil {
			source := *m.Source
			m.Source = &source
		}
		if m.Event != nil {
			event := *m.Event
			event.Targets = slices.Clone(event.Targets)
			m.Event = &event
		}
	}
	return out
}

const roomSegmentBytes = 1 << 20

func (s *Store) roomLogDir() string { return filepath.Join(s.dir, "room-input", "records") }
func (s *Store) readRoomSegment(seg roomLogSegment) ([]byte, error) {
	if !safeMembershipID(seg.Name) || seg.First == 0 || seg.Last < seg.First || seg.Bytes <= 0 || seg.Bytes > roomSegmentBytes+inputstream.MaxRecordBytes {
		return nil, ErrInvalid
	}
	f, err := os.Open(filepath.Join(s.roomLogDir(), seg.Name+".jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < int64(seg.Bytes) {
		return nil, ErrInvalid
	}
	b := make([]byte, seg.Bytes)
	if _, err := io.ReadFull(f, b); err != nil {
		return nil, err
	}
	if inputstream.Digest(b) != seg.Digest || b[len(b)-1] != '\n' {
		return nil, ErrInvalid
	}
	return b, nil
}
func (s *Store) loadRoomLog(v *RoomSnapshot) error {
	if err := s.readRoomLog(v); err != nil {
		return err
	}
	v.Messages = cloneRoomLogMessages(v.Messages)
	return nil
}

// readRoomLog can share immutable cached history with the commit validator.
// LoadRoom callers receive a separate, owned copy.
func (s *Store) readRoomLog(v *RoomSnapshot) error {
	root := v.InputLog
	if root.Version != 1 || root.Records != int(v.LatestSeq-v.ClearedThrough) || root.Bytes < 0 || root.Bytes > inputstream.MaxBytes || root.Records > inputstream.MaxRecords {
		return ErrInvalid
	}
	key := inputstream.Ref("room_log_", v.RoomIdentity, v.ClearedThrough, v.LatestSeq, root)
	if cache := s.roomInputCache; cache != nil && cache.Key == key && len(cache.Files) == len(root.Segments) {
		valid := true
		for i, seg := range root.Segments {
			info, err := os.Stat(filepath.Join(s.roomLogDir(), seg.Name+".jsonl"))
			if err != nil || info.Size() != cache.Files[i].Size() || info.ModTime() != cache.Files[i].ModTime() || !os.SameFile(info, cache.Files[i]) {
				valid = false
				break
			}
		}
		if valid {
			v.Messages = cache.Messages
			return nil
		}
	}
	v.Messages = []RoomMessage{}
	prev, total := v.ClearedThrough, 0
	seen := map[string]bool{}
	for _, seg := range root.Segments {
		if seg.First != prev+1 || seen[seg.Name] {
			return ErrInvalid
		}
		seen[seg.Name] = true
		b, err := s.readRoomSegment(seg)
		if err != nil {
			return fmt.Errorf("Room log: %w", err)
		}
		total += len(b)
		scan := bufio.NewScanner(bytes.NewReader(b))
		scan.Buffer(make([]byte, 4096), inputstream.MaxRecordBytes)
		for scan.Scan() {
			var m RoomMessage
			if err := inputstream.StrictDecode(scan.Bytes(), &m); err != nil {
				return err
			}
			prev++
			if m.Seq != prev {
				return ErrInvalid
			}
			v.Messages = append(v.Messages, m)
		}
		if err := scan.Err(); err != nil {
			return err
		}
		if prev != seg.Last {
			return ErrInvalid
		}
	}
	if prev != v.LatestSeq || total != root.Bytes || len(v.Messages) != root.Records {
		return ErrInvalid
	}
	files := []os.FileInfo{}
	for _, seg := range root.Segments {
		info, err := os.Stat(filepath.Join(s.roomLogDir(), seg.Name+".jsonl"))
		if err != nil {
			return err
		}
		files = append(files, info)
	}
	s.roomInputCache = &roomLogCache{Key: key, Messages: v.Messages, Files: files}
	return nil
}
func (s *Store) writeRoomSegment(data []byte, first, last uint64) (roomLogSegment, error) {
	name, err := newOpaqueID()
	if err != nil {
		return roomLogSegment{}, err
	}
	dir := s.roomLogDir()
	if err := s.ensureSnapshotDirectory(dir); err != nil {
		return roomLogSegment{}, err
	}
	// Unique names make pre-publication failures unambiguously orphaned. A
	// partially written segment can never be mistaken for a committed append.
	f, err := s.ops.createTemp(dir, ".room-log-*")
	if err != nil {
		return roomLogSegment{}, err
	}
	defer os.Remove(f.Name())
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return roomLogSegment{}, err
	}
	path := filepath.Join(dir, name+".jsonl")
	if err := s.ops.rename(f.Name(), path); err != nil {
		return roomLogSegment{}, err
	}
	if err := s.ops.syncDir(dir); err != nil {
		return roomLogSegment{}, err
	}
	return roomLogSegment{Name: name, First: first, Last: last, Bytes: len(data), Digest: inputstream.Digest(data)}, nil
}
func (s *Store) prepareRoomLog(v RoomSnapshot) (*roomLogRoot, error) {
	root := &roomLogRoot{Version: 1, Segments: []roomLogSegment{}, Records: len(v.Messages)}
	var old RoomSnapshot
	err := readSnapshot(filepath.Join(s.dir, "room.json"), &old, maxRoomFileBytes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil && old.InputLog == nil {
		return nil, fmt.Errorf("%w: Room requires explicit data upgrade", ErrVersion)
	}
	if err == nil {
		if err := s.readRoomLog(&old); err != nil {
			return nil, err
		}
		if err := s.cleanRoomLog(*old.InputLog); err != nil {
			return nil, err
		}
		if old.RoomIdentity != v.RoomIdentity || v.LatestSeq < old.LatestSeq || v.ClearedThrough < old.ClearedThrough {
			return nil, ErrIdentity
		}
		for i := range old.Messages {
			m := &old.Messages[i]
			if m.Seq > v.ClearedThrough && !reflect.DeepEqual(m, &v.Messages[m.Seq-v.ClearedThrough-1]) {
				return nil, ErrConflict
			}
		}
	}
	// Reuse whole immutable segments. A clear rewrites only its boundary;
	// an append rewrites only a short final segment. Native state updates write
	// no history bytes at all.
	appendRange := func(from, to uint64) error {
		if from > to {
			return nil
		}
		var pending bytes.Buffer
		first := from
		flush := func(last uint64) error {
			if pending.Len() == 0 {
				return nil
			}
			seg, err := s.writeRoomSegment(pending.Bytes(), first, last)
			if err != nil {
				return err
			}
			root.Segments = append(root.Segments, seg)
			root.Bytes += seg.Bytes
			pending.Reset()
			first = last + 1
			return nil
		}
		for _, m := range v.Messages[from-v.ClearedThrough-1 : to-v.ClearedThrough] {
			line, err := json.Marshal(m)
			if err != nil {
				return err
			}
			line = append(line, '\n')
			if len(line) > inputstream.MaxRecordBytes {
				return ErrCapacity
			}
			if root.Bytes+pending.Len()+len(line) > inputstream.MaxBytes {
				return ErrCapacity
			}
			if pending.Len() > 0 && pending.Len()+len(line) > roomSegmentBytes {
				if err := flush(m.Seq - 1); err != nil {
					return err
				}
			}
			pending.Write(line)
		}
		return flush(to)
	}
	start := v.ClearedThrough + 1
	if old.InputLog != nil {
		for i, seg := range old.InputLog.Segments {
			if seg.Last < start {
				continue
			}
			if i == len(old.InputLog.Segments)-1 && v.LatestSeq > old.LatestSeq && seg.Bytes < roomSegmentBytes {
				break
			}
			if seg.First < start {
				if err := appendRange(start, seg.Last); err != nil {
					return nil, err
				}
			} else {
				if seg.First != start {
					return nil, ErrInvalid
				}
				root.Segments = append(root.Segments, seg)
				root.Bytes += seg.Bytes
			}
			start = seg.Last + 1
		}
	}
	if err := appendRange(start, v.LatestSeq); err != nil {
		return nil, err
	}
	if root.Bytes > inputstream.MaxBytes {
		return nil, ErrCapacity
	}
	return root, nil
}
func (s *Store) cleanRoomLog(root roomLogRoot) error {
	keep := map[string]int{}
	for _, seg := range root.Segments {
		keep[seg.Name+".jsonl"] = seg.Bytes
	}
	files, err := os.ReadDir(s.roomLogDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, f := range files {
		path := filepath.Join(s.roomLogDir(), f.Name())
		if size, ok := keep[f.Name()]; ok {
			info, err := f.Info()
			if err != nil {
				return err
			}
			if info.Size() > int64(size) {
				file, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					return err
				}
				err = file.Truncate(int64(size))
				if err == nil {
					err = file.Sync()
				}
				err = errors.Join(err, file.Close())
				if err != nil {
					return err
				}
			}
		} else if !f.IsDir() {
			if err := s.ops.remove(path); err != nil {
				return err
			}
		}
	}
	return s.ops.syncDir(s.roomLogDir())
}
