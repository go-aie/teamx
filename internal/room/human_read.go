package room

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const humanReadVersion = 1

var errHumanReadUnavailable = errors.New("阅读状态暂未同步")

type humanReadFile struct {
	Version int `json:"version"`
	RoomIdentity
	OperatorID        string   `json:"operator_id"`
	Revision          uint64   `json:"revision"`
	ObservedLatestSeq uint64   `json:"observed_latest_seq"`
	SeenSeqs          []uint64 `json:"seen_seqs"`
}

type humanReadState struct {
	RoomIdentity
	OperatorID     string   `json:"operator_id"`
	Revision       uint64   `json:"revision"`
	SnapshotSeq    uint64   `json:"snapshot_seq"`
	ClearedThrough uint64   `json:"cleared_through"`
	SeenSeqs       []uint64 `json:"seen_seqs"`
	UnreadCount    int      `json:"unread_count"`
	MentionCount   int      `json:"mention_count"`
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
	h.file = humanReadFile{Version: humanReadVersion, RoomIdentity: room.RoomIdentity, OperatorID: operator, SeenSeqs: []uint64{}}
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
	if v.Version != humanReadVersion || v.RoomIdentity != room.RoomIdentity ||
		v.OperatorID != operator || !validText(v.OperatorID) || v.Revision == 0 ||
		v.ObservedLatestSeq > room.LatestSeq || v.SeenSeqs == nil || len(v.SeenSeqs) > maxMessages {
		return ErrInvalid
	}
	var previous uint64
	for _, seq := range v.SeenSeqs {
		if seq <= previous || seq > v.ObservedLatestSeq {
			return ErrInvalid
		}
		previous = seq
	}
	return nil
}

// state and confirm read the committed public snapshot inside the merge lock,
// so a later request cannot validate against an older history than its peer.
// snapshot is nil for a query; no query creates or compacts the file.
func (h *humanReadStore) update(store *Store, snapshot *uint64, seen []uint64, displayName string) (humanReadState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.unavailable != nil {
		return humanReadState{}, errHumanReadUnavailable
	}
	room, err := store.LoadRoom()
	if err != nil {
		return humanReadState{}, errHumanReadUnavailable
	}
	if room.RoomIdentity != h.file.RoomIdentity {
		return humanReadState{}, errHumanReadUnavailable
	}
	if snapshot != nil {
		if len(seen) > 100 || *snapshot > room.LatestSeq {
			return humanReadState{}, ErrRequest
		}
		for _, seq := range seen {
			if seq == 0 || seq > *snapshot {
				return humanReadState{}, ErrRequest
			}
		}
	}
	if h.uncertain {
		// One attempt per ambiguous replacement. Failure latches only this
		// service's reading state; reopening the service may try loading again.
		h.uncertain = false
		if err := h.recover(room); err != nil {
			h.unavailable = err
			return humanReadState{}, errHumanReadUnavailable
		}
	}
	set := make(map[uint64]bool, len(h.file.SeenSeqs)+len(seen))
	for _, seq := range h.file.SeenSeqs {
		if readableHumanSeq(room, h.file.OperatorID, seq) {
			set[seq] = true
		}
	}
	changed := false
	for _, seq := range seen {
		if readableHumanSeq(room, h.file.OperatorID, seq) && !set[seq] {
			set[seq], changed = true, true
		}
	}
	seqs := make([]uint64, 0, len(set))
	for seq := range set {
		seqs = append(seqs, seq)
	}
	slices.Sort(seqs)
	if changed {
		if h.file.Revision == math.MaxUint64 {
			return humanReadState{}, errHumanReadUnavailable
		}
		candidate := h.file
		candidate.Revision++
		candidate.ObservedLatestSeq = max(candidate.ObservedLatestSeq, *snapshot)
		candidate.SeenSeqs = seqs
		if err := h.save(candidate); err != nil {
			return humanReadState{}, errHumanReadUnavailable
		}
		h.file, h.hadFile = candidate, true
	}
	result := humanReadState{RoomIdentity: room.RoomIdentity, OperatorID: h.file.OperatorID,
		Revision: h.file.Revision, SnapshotSeq: room.LatestSeq, ClearedThrough: room.ClearedThrough, SeenSeqs: seqs}
	matcher := newHumanMentionMatcher(h.file.OperatorID, displayName)
	for _, message := range room.Messages {
		if message.Author == (Author{Kind: "human", ID: h.file.OperatorID}) || set[message.Seq] {
			continue
		}
		result.UnreadCount++
		if matcher.matches(message.Text) {
			result.MentionCount++
		}
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
