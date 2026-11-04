package notification

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
)

type subscription struct {
	state    State
	source   Source
	revision uint64
}

func (m *Manager) directory(path string, create bool) error {
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) && create {
		if err = m.host.EnsureDirectory(dir); err != nil {
			return err
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return ErrInvalid
	}
	return nil
}

// Every subscription has one source-tagged snapshot in the owner's directory.
// The common envelope selects the source codec; source progress remains opaque.
type snapshot struct {
	SourceID
	Schema int             `json:"schema"`
	State  json.RawMessage `json:"state"`
}

func validComponent(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (m *Manager) StatePathLocked(key Key) (string, error) {
	if !validComponent(key.Source.Provider) || !validComponent(key.Source.Channel) || !validComponent(key.SubscriptionID) || m.sources[key.Source] == nil {
		return "", ErrInvalid
	}
	directory, err := m.host.Directory(key.Owner)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, key.Source.Provider+"."+key.Source.Channel+"."+key.SubscriptionID+".json"), nil
}

// LoadLocked loads all explicit opt-ins. A missing directory never enables one.
func (m *Manager) LoadLocked(owner platform.RoomIdentity) error {
	if err := m.host.Check(false); err != nil {
		return err
	}
	directory, err := m.host.Directory(owner)
	if err != nil {
		return err
	}
	if err = m.directory(filepath.Join(directory, "state.json"), false); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err = m.loadPathLocked(owner, filepath.Join(directory, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) loadPathLocked(owner platform.RoomIdentity, path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > m.Limits.Bytes {
		return ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, m.Limits.Bytes+1))
	if err != nil || int64(len(data)) > m.Limits.Bytes {
		return ErrInvalid
	}
	var saved snapshot
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&saved) != nil || dec.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	source := m.sources[saved.SourceID]
	if source == nil || saved.Schema != source.Schema() {
		return ErrInvalid
	}
	state, err := source.Decode(saved.State, owner)
	if err != nil || state == nil || state.Validate(owner) != nil || state.Metadata().Key.Source != source.ID() {
		return fmt.Errorf("%w: notification source schema", ErrInvalid)
	}
	key := state.Metadata().Key
	expected, err := m.StatePathLocked(key)
	if err != nil || path != expected || state.Metadata().Observed > m.Limits.Observed || state.Metadata().Pending > m.Limits.Inbox {
		return ErrInvalid
	}
	if _, exists := m.states[key]; exists {
		return ErrInvalid
	}
	m.states[key] = subscription{state: state.CloneState(), source: source, revision: 1}
	return nil
}

// SaveLocked uses only the injected atomic writer, including its service-wide
// commit-unknown latch. Runtime state becomes visible strictly after success.
func (m *Manager) SaveLocked(state State) error {
	if err := m.host.Check(true); err != nil {
		return err
	}
	meta := state.Metadata()
	source := m.sources[meta.Key.Source]
	if source == nil || source.Schema() <= 0 {
		return ErrInvalid
	}
	if meta.Observed > m.Limits.Observed || meta.Pending > m.Limits.Inbox {
		return ErrCapacity
	}
	if err := state.Validate(meta.Key.Owner); err != nil {
		return err
	}
	progress, err := json.Marshal(state)
	if err != nil {
		return ErrInvalid
	}
	saved := snapshot{SourceID: meta.Key.Source, Schema: source.Schema(), State: progress}
	data, err := json.Marshal(saved)
	if err != nil {
		return ErrInvalid
	}
	if int64(len(data))+1 > m.Limits.Bytes {
		return ErrCapacity
	}
	path, err := m.StatePathLocked(meta.Key)
	if err != nil {
		return err
	}
	if err = m.directory(path, true); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) {
		return ErrInvalid
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = m.host.Save(path, saved, m.Limits.Bytes); err != nil {
		return err
	}
	previous := m.states[meta.Key]
	m.states[meta.Key] = subscription{state: state.CloneState(), source: source, revision: previous.revision + 1}
	return nil
}

func (m *Manager) StateLocked(key Key) (State, bool) {
	s, ok := m.states[key]
	if !ok {
		return nil, false
	}
	return s.state.CloneState(), true
}

// StatesLocked returns explicit subscription identities in a stable order.
func (m *Manager) StatesLocked(owner platform.RoomIdentity) []State {
	states := []State{}
	for key, saved := range m.states {
		if key.Owner == owner {
			states = append(states, saved.state.CloneState())
		}
	}
	sort.Slice(states, func(i, j int) bool {
		a, b := states[i].Metadata().Key, states[j].Metadata().Key
		if a.Source.Provider != b.Source.Provider {
			return a.Source.Provider < b.Source.Provider
		}
		if a.Source.Channel != b.Source.Channel {
			return a.Source.Channel < b.Source.Channel
		}
		return a.SubscriptionID < b.SubscriptionID
	})
	return states
}
func (m *Manager) ForgetRoomLocked(owner platform.RoomIdentity) {
	for key := range m.states {
		if key.Owner == owner {
			delete(m.states, key)
		}
	}
}
func (m *Manager) ReferencesLocked(id string) []platform.RoomIdentity {
	out := []platform.RoomIdentity{}
	seen := map[platform.RoomIdentity]bool{}
	for key, s := range m.states {
		if s.state.Metadata().ConnectionID == id && !seen[key.Owner] {
			out = append(out, key.Owner)
			seen[key.Owner] = true
		}
	}
	return out
}
