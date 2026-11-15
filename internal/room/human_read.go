package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const humanReadVersion = 2

var errHumanReadUnavailable = errors.New("阅读状态暂未同步")

type humanReadFile struct {
	Version int `json:"version"`
	RoomIdentity
	OperatorID        string   `json:"operator_id"`
	Revision          uint64   `json:"revision"`
	ObservedLatestSeq uint64   `json:"observed_latest_seq"`
	OpenedThrough     uint64   `json:"opened_through"`
	MentionSeenSeqs   []uint64 `json:"mention_seen_seqs"`
	// Read only for the one-time projection of validated v1 files.
	SeenSeqs []uint64 `json:"-"`
}

type humanReadLegacyFile struct {
	Version int `json:"version"`
	RoomIdentity
	OperatorID        string   `json:"operator_id"`
	Revision          uint64   `json:"revision"`
	ObservedLatestSeq uint64   `json:"observed_latest_seq"`
	SeenSeqs          []uint64 `json:"seen_seqs"`
}

// Decode each on-disk version against its own strict schema. The legacy
// schema is only an input to projection; all subsequent saves use v2.
func (v *humanReadFile) UnmarshalJSON(data []byte) error {
	var header struct{ Version int }
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if header.Version == 1 {
		var old humanReadLegacyFile
		if err := dec.Decode(&old); err != nil {
			return err
		}
		*v = humanReadFile{Version: 1, RoomIdentity: old.RoomIdentity, OperatorID: old.OperatorID,
			Revision: old.Revision, ObservedLatestSeq: old.ObservedLatestSeq, SeenSeqs: old.SeenSeqs}
		return nil
	}
	type currentFile humanReadFile
	var current currentFile
	if err := dec.Decode(&current); err != nil {
		return err
	}
	*v = humanReadFile(current)
	return nil
}

type humanReadState struct {
	Version int `json:"version"`
	RoomIdentity
	OperatorID        string   `json:"operator_id"`
	Revision          uint64   `json:"revision"`
	SnapshotSeq       uint64   `json:"snapshot_seq"`
	ClearedThrough    uint64   `json:"cleared_through"`
	OpenedThrough     uint64   `json:"opened_through"`
	MentionSeenSeqs   []uint64 `json:"mention_seen_seqs"`
	UnseenMentionSeqs []uint64 `json:"unseen_mention_seqs"`
	// A pointer preserves an explicitly requested empty array.
	NewMessageSeqs *[]uint64 `json:"new_message_seqs,omitempty"`
	UnreadCount    int       `json:"unread_count"`
	MentionCount   int       `json:"mention_count"`
}

type humanReadDelta struct {
	SnapshotSeq        *uint64
	OpenedThrough      *uint64
	MentionSeenSeqs    []uint64
	IncludeNewMessages bool
}

// The service owns the Room directory lock. This sidecar has its own commit
// and failure state: a read receipt must never freeze business Store writes.
// Lock order is humanReadStore.mu then Store.mu (only to load public history).
type humanReadStore struct {
	mu          sync.Mutex
	dir         string
	operator    string // Immutable, also used by HTTP identity checks.
	file        humanReadFile
	unavailable error
	uncertain   bool
	hadFile     bool
	ops         snapshotOps
}

func newHumanReadStore(dir string, room RoomSnapshot, operator string) *humanReadStore {
	h := &humanReadStore{dir: dir, operator: operator, ops: defaultSnapshotOps()}
	h.file = humanReadFile{Version: humanReadVersion, RoomIdentity: room.RoomIdentity, OperatorID: operator, OpenedThrough: room.ClearedThrough, MentionSeenSeqs: []uint64{}}
	v, err := h.load(room)
	if errors.Is(err, os.ErrNotExist) {
		return h
	}
	if err != nil {
		h.unavailable = err
	} else {
		h.file, h.hadFile = v, true
	}
	return h
}

func (h *humanReadStore) path() string { return filepath.Join(h.dir, "human-read.json") }

func (h *humanReadStore) load(room RoomSnapshot) (humanReadFile, error) {
	var v humanReadFile
	if err := readSnapshot(h.path(), &v, maxPrivateFileBytes); err != nil {
		return v, err
	}
	return v, validateHumanReadFile(v, room, h.operator)
}

func validateHumanReadFile(v humanReadFile, room RoomSnapshot, operator string) error {
	if (v.Version != 1 && v.Version != humanReadVersion) || v.RoomIdentity != room.RoomIdentity ||
		v.OperatorID != operator || !validText(v.OperatorID) || v.Revision == 0 ||
		v.ObservedLatestSeq > room.LatestSeq {
		return ErrInvalid
	}
	seqs := v.MentionSeenSeqs
	if v.Version == 1 {
		if v.OpenedThrough != 0 || v.MentionSeenSeqs != nil {
			return ErrInvalid
		}
		seqs = v.SeenSeqs
	} else if v.SeenSeqs != nil || v.OpenedThrough > v.ObservedLatestSeq {
		return ErrInvalid
	}
	if seqs == nil || len(seqs) > maxMessages {
		return ErrInvalid
	}
	var previous uint64
	for _, seq := range seqs {
		if seq <= previous || seq > v.ObservedLatestSeq {
			return ErrInvalid
		}
		previous = seq
	}
	return nil
}

// A single projection supplies state and catalog counts from the same public
// snapshot. Queries (including v1 projection and clearing) never write a file.
func projectHumanRead(room RoomSnapshot, file humanReadFile, displayName string, includeNew bool) humanReadState {
	opened, seqs := file.OpenedThrough, file.MentionSeenSeqs
	if file.Version == 1 {
		opened, seqs = room.ClearedThrough, file.SeenSeqs
	}
	result := humanReadState{Version: humanReadVersion, RoomIdentity: room.RoomIdentity, OperatorID: file.OperatorID,
		Revision: file.Revision, SnapshotSeq: room.LatestSeq, ClearedThrough: room.ClearedThrough,
		OpenedThrough: max(opened, room.ClearedThrough), MentionSeenSeqs: []uint64{}, UnseenMentionSeqs: []uint64{}}
	seen := make(map[uint64]bool, len(seqs))
	for _, seq := range seqs {
		seen[seq] = true
	}
	if includeNew {
		seqs := []uint64{}
		result.NewMessageSeqs = &seqs
	}
	matcher := newHumanMentionMatcher(file.OperatorID, displayName)
	for _, message := range room.Messages {
		if message.Author == (Author{Kind: "human", ID: file.OperatorID}) {
			continue
		}
		mentioned := matcher.matches(message.Text)
		if mentioned && seen[message.Seq] {
			result.MentionSeenSeqs = append(result.MentionSeenSeqs, message.Seq)
		}
		unseenMention := mentioned && !seen[message.Seq]
		if unseenMention {
			result.UnseenMentionSeqs = append(result.UnseenMentionSeqs, message.Seq)
		}
		newMessage := message.Seq > result.OpenedThrough
		if newMessage && includeNew {
			*result.NewMessageSeqs = append(*result.NewMessageSeqs, message.Seq)
		}
		if newMessage || unseenMention {
			result.UnreadCount++
		}
	}
	result.MentionCount = len(result.UnseenMentionSeqs)
	return result
}

// Read the committed public snapshot inside the merge lock. Two independent
// increments merge by max/opened and union/mentions, never by replacement.
func (h *humanReadStore) update(store *Store, delta humanReadDelta, displayName string) (humanReadState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.unavailable != nil {
		return humanReadState{}, errHumanReadUnavailable
	}
	room, err := store.LoadRoom()
	if err != nil || room.RoomIdentity != h.file.RoomIdentity {
		return humanReadState{}, errHumanReadUnavailable
	}
	if delta.SnapshotSeq != nil {
		if len(delta.MentionSeenSeqs) > 100 || *delta.SnapshotSeq > room.LatestSeq ||
			(delta.OpenedThrough != nil && *delta.OpenedThrough > *delta.SnapshotSeq) {
			return humanReadState{}, ErrRequest
		}
		for _, seq := range delta.MentionSeenSeqs {
			if seq == 0 || seq > *delta.SnapshotSeq {
				return humanReadState{}, ErrRequest
			}
		}
	} else if delta.OpenedThrough != nil || delta.MentionSeenSeqs != nil {
		return humanReadState{}, ErrRequest
	}
	if h.uncertain {
		// Attempt recovery once per ambiguous replacement; business writes remain independent.
		h.uncertain = false
		if err := h.recover(room); err != nil {
			h.unavailable = err
			return humanReadState{}, errHumanReadUnavailable
		}
	}
	result := projectHumanRead(room, h.file, displayName, delta.IncludeNewMessages)
	if delta.SnapshotSeq == nil {
		return result, nil
	}
	opened := result.OpenedThrough
	if delta.OpenedThrough != nil {
		opened = max(opened, *delta.OpenedThrough)
	}
	set := make(map[uint64]bool, len(result.MentionSeenSeqs)+len(delta.MentionSeenSeqs))
	for _, seq := range result.MentionSeenSeqs {
		set[seq] = true
	}
	changed := opened > result.OpenedThrough || h.file.Version == 1
	matcher := newHumanMentionMatcher(h.file.OperatorID, displayName)
	for _, seq := range delta.MentionSeenSeqs {
		if readableHumanSeq(room, h.file.OperatorID, seq) && matcher.matches(room.Messages[seq-room.ClearedThrough-1].Text) && !set[seq] {
			set[seq], changed = true, true
		}
	}
	if changed {
		if h.file.Revision == math.MaxUint64 {
			return humanReadState{}, errHumanReadUnavailable
		}
		seqs := make([]uint64, 0, len(set))
		for seq := range set {
			seqs = append(seqs, seq)
		}
		slices.Sort(seqs)
		candidate := humanReadFile{Version: humanReadVersion, RoomIdentity: room.RoomIdentity, OperatorID: h.file.OperatorID,
			Revision: h.file.Revision + 1, ObservedLatestSeq: max(h.file.ObservedLatestSeq, *delta.SnapshotSeq, room.ClearedThrough),
			OpenedThrough: opened, MentionSeenSeqs: seqs}
		if err := h.save(candidate); err != nil {
			return humanReadState{}, errHumanReadUnavailable
		}
		h.file, h.hadFile = candidate, true
		result = projectHumanRead(room, candidate, displayName, delta.IncludeNewMessages)
	}
	return result, nil
}

func readableHumanSeq(room RoomSnapshot, operator string, seq uint64) bool {
	return seq > room.ClearedThrough && seq <= room.LatestSeq &&
		room.Messages[seq-room.ClearedThrough-1].Author != (Author{Kind: "human", ID: operator})
}

// No mkdir: the existing service lifecycle and directory ownership must hold.
func (h *humanReadStore) save(v humanReadFile) error {
	f, err := h.ops.createTemp(h.dir, ".human-read-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()
	enc := json.NewEncoder(&boundedWriter{w: f, remaining: maxPrivateFileBytes})
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	closed = true
	if err := f.Close(); err != nil {
		return err
	}
	if err := h.ops.rename(f.Name(), h.path()); err != nil {
		h.uncertain = true
		return err
	}
	if err := h.ops.syncDir(h.dir); err != nil {
		h.uncertain = true
		return err
	}
	return nil
}

func (h *humanReadStore) recover(room RoomSnapshot) error {
	v, err := h.load(room)
	if errors.Is(err, os.ErrNotExist) && !h.hadFile {
		// A failed first rename may have left no receipt at all. Persist that
		// absence before allowing another increment.
		return h.ops.syncDir(h.dir)
	}
	if err != nil {
		return err
	}
	f, err := os.OpenFile(h.path(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := h.ops.syncDir(h.dir); err != nil {
		return err
	}
	h.file, h.hadFile = v, true
	return nil
}

// Match the browser's mentionRanges ASCII boundaries with literal names.
// Go's regexp has no lookaround; checking adjacent bytes also preserves its
// intentional behavior for non-ASCII names and punctuation.
type humanMentionMatcher struct{ needles []string }

func newHumanMentionMatcher(names ...string) humanMentionMatcher {
	m := humanMentionMatcher{}
	for _, name := range names {
		if name != "" && !slices.Contains(m.needles, "@"+name) {
			m.needles = append(m.needles, "@"+name)
		}
	}
	return m
}

func humanMentionWord(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_'
}

func (m humanMentionMatcher) matches(text string) bool {
	for _, needle := range m.needles {
		for offset := 0; offset < len(text); {
			rel := strings.Index(text[offset:], needle)
			if rel < 0 {
				break
			}
			start := offset + rel
			end := start + len(needle)
			beforeOK := start == 0 || !(humanMentionWord(text[start-1]) || strings.ContainsRune("@.+-", rune(text[start-1])))
			afterOK := end == len(text) || !(humanMentionWord(text[end]) || strings.ContainsRune("@+-", rune(text[end])) ||
				text[end] == '.' && end+1 < len(text) && humanMentionWord(text[end+1]))
			if beforeOK && afterOK {
				return true
			}
			offset = start + 1
		}
	}
	return false
}
