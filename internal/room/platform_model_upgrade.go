package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const platformUpgradeLimit int64 = 256 << 20
const platformUpgradeJournal = "platform-model-upgrade.json"

type platformFileChange struct {
	Path   string          `json:"path"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}
type platformUpgrade struct {
	Version  int                  `json:"version"`
	Complete bool                 `json:"complete"`
	Files    []platformFileChange `json:"files"`
}

func checkPlatformUpgrade(root string) error {
	var j platformUpgrade
	err := readSnapshot(filepath.Join(root, platformUpgradeJournal), &j, platformUpgradeLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.Version != 1 || !j.Complete {
		return fmt.Errorf("%w: platform model upgrade incomplete; stop service and run teamx platforms upgrade --rooms-dir ROOT", ErrVersion)
	}
	return nil
}

// Only the offline boundary understands historical identity keys. Raw values
// and numbers stay exact; message bodies, tool input and environment are opaque.
func convertPlatformJSON(data json.RawMessage) (json.RawMessage, bool, error) {
	var object map[string]json.RawMessage
	if len(data) == 0 || data[0] != '{' && data[0] != '[' {
		return data, false, nil
	}
	if data[0] == '[' {
		var values []json.RawMessage
		if json.Unmarshal(data, &values) != nil {
			return nil, false, ErrInvalid
		}
		changed := false
		for i, v := range values {
			n, c, e := convertPlatformJSON(bytes.TrimSpace(v))
			if e != nil {
				return nil, false, e
			}
			values[i] = n
			changed = changed || c
		}
		if !changed {
			return data, false, nil
		}
		b, e := json.Marshal(values)
		return b, true, e
	}
	if json.Unmarshal(data, &object) != nil {
		return nil, false, ErrInvalid
	}
	changed := false
	for old, next := range map[string]string{"connection_id": "platform_id", "connection_generation": "platform_generation", "connection_version": "platform_version"} {
		if value, ok := object[old]; ok {
			if _, exists := object[next]; exists {
				return nil, false, ErrConflict
			}
			object[next] = value
			delete(object, old)
			changed = true
		}
	}
	for key, v := range object {
		switch key {
		case "input", "body", "text", "content", "tool_env", "resources":
			continue
		}
		n, c, e := convertPlatformJSON(bytes.TrimSpace(v))
		if e != nil {
			return nil, false, e
		}
		object[key] = n
		changed = changed || c
	}
	if !changed {
		return data, false, nil
	}
	b, e := json.Marshal(object)
	return b, true, e
}

// Whitelist private application snapshots; never traverse workspaces, resources
// or DSH backend/session directories, nor reinterpret arbitrary repository JSON.
func platformSnapshotPath(rel string) bool {
	p := strings.Split(filepath.ToSlash(rel), "/")
	if rel == "rooms.json" {
		return true
	}
	if len(p) < 3 || p[0] != "rooms" {
		return false
	}
	if len(p) == 3 {
		switch p[2] {
		case "room-config.json", "room.json", "tool-bindings.json", "gitlab-notifications.json":
			return true
		}
	}
	if len(p) > 3 && p[2] == "notifications" {
		return strings.HasSuffix(rel, ".json")
	}
	if len(p) > 5 && p[2] == "agents" && (p[4] == "followups" || p[4] == "actions") {
		return strings.HasSuffix(rel, ".json")
	}
	return false
}

func platformSnapshotDirectory(rel string) bool {
	p := strings.Split(filepath.ToSlash(rel), "/")
	if rel == "." || rel == "rooms" {
		return true
	}
	if len(p) < 2 || p[0] != "rooms" {
		return false
	}
	if len(p) == 2 {
		return true
	}
	switch p[2] {
	case "notifications", "inboxes":
		return true
	case "agents":
		return len(p) <= 4 || p[4] == "followups" || p[4] == "actions"
	}
	return false
}
func platformWalkEntry(root, path string, d fs.DirEntry) error {
	rel, _ := filepath.Rel(root, path)
	if d.Type()&os.ModeSymlink != 0 && (platformSnapshotDirectory(rel) || platformSnapshotPath(rel)) {
		return ErrInvalid
	}
	if d.IsDir() && !platformSnapshotDirectory(rel) {
		return filepath.SkipDir
	}
	return nil
}

func upgradePlatformModel(s *Store, candidates map[string]json.RawMessage) error {
	j := platformUpgrade{Version: 1}
	var index struct {
		Rooms []struct {
			RoomID string `json:"room_id"`
		} `json:"rooms"`
	}
	raw, readErr := os.ReadFile(filepath.Join(s.dir, "rooms.json"))
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if readErr == nil && json.Unmarshal(raw, &index) != nil {
		return ErrInvalid
	}
	rooms := map[string]bool{}
	for _, r := range index.Rooms {
		rooms[r.RoomID] = true
	}
	err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e := platformWalkEntry(s.dir, path, d); e != nil {
			return e
		}
		rel, _ := filepath.Rel(s.dir, path)
		if !platformSnapshotPath(rel) {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) > 1 && parts[0] == "rooms" && !rooms[parts[1]] {
			return fmt.Errorf("%w: unindexed Room snapshot", ErrInvalid)
		}
		info, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > platformUpgradeLimit {
			return ErrInvalid
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if !json.Valid(b) {
			return ErrInvalid
		}
		n, changed, e := convertPlatformJSON(bytes.TrimSpace(b))
		if e != nil {
			return fmt.Errorf("%s: %w", rel, e)
		}
		if changed {
			candidates[path] = n
		}
		return nil
	})
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(candidates))
	for path := range candidates {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		rel, e := filepath.Rel(s.dir, path)
		if e != nil || !fs.ValidPath(filepath.ToSlash(rel)) {
			return ErrInvalid
		}
		before, e := os.ReadFile(path)
		if errors.Is(e, os.ErrNotExist) {
			before = []byte("null")
		} else if e != nil {
			return e
		}
		if bytes.Equal(bytes.TrimSpace(before), bytes.TrimSpace(candidates[path])) {
			continue
		}
		j.Files = append(j.Files, platformFileChange{rel, before, candidates[path]})
	}
	if len(j.Files) == 0 {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(s.dir, platformUpgradeJournal)); err == nil {
		return ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Validate all typed candidates in a private mirror before touching originals.
	if err := validatePlatformCandidates(s, j); err != nil {
		return err
	}
	if err := s.save(filepath.Join(s.dir, platformUpgradeJournal), j, platformUpgradeLimit); err != nil {
		return err
	}
	return commitPlatformUpgrade(s, j)
}

func resumePlatformUpgrade(s *Store) (bool, error) {
	var j platformUpgrade
	err := readSnapshot(filepath.Join(s.dir, platformUpgradeJournal), &j, platformUpgradeLimit)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if j.Version != 1 {
		return true, ErrVersion
	}
	if j.Complete {
		return false, nil
	}
	return true, commitPlatformUpgrade(s, j)
}
func commitPlatformUpgrade(s *Store, j platformUpgrade) error {
	// Check the entire retry set before any write. Each file must be its exact
	// preimage or committed candidate; a third value is an external conflict.
	for _, c := range j.Files {
		if !fs.ValidPath(filepath.ToSlash(c.Path)) {
			return ErrInvalid
		}
		path := filepath.Join(s.dir, c.Path)
		info, e := os.Lstat(path)
		var b []byte
		if errors.Is(e, os.ErrNotExist) {
			b = []byte("null")
		} else if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return ErrInvalid
		} else {
			b, e = os.ReadFile(path)
			if e != nil {
				return e
			}
		}
		if !equalPlatformJSON(b, c.Before) && !equalPlatformJSON(b, c.After) {
			return ErrConflict
		}
	}
	for _, c := range j.Files {
		if err := s.save(filepath.Join(s.dir, c.Path), c.After, platformUpgradeLimit); err != nil {
			return err
		}
	}
	j.Complete = true
	return s.save(filepath.Join(s.dir, platformUpgradeJournal), j, platformUpgradeLimit)
}
func equalPlatformJSON(a, b []byte) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}

func validatePlatformCandidates(s *Store, j platformUpgrade) error {
	dir, err := os.MkdirTemp("", "teamx-platform-candidate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Copy only owned snapshots, including unchanged evidence used by validators.
	err = filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e := platformWalkEntry(s.dir, path, d); e != nil {
			return e
		}
		rel, _ := filepath.Rel(s.dir, path)
		if !platformSnapshotPath(rel) && rel != "platform-connections.json" && !(strings.Contains(filepath.ToSlash(rel), "/inboxes/") && strings.HasSuffix(rel, ".json")) {
			return nil
		}
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() {
			return ErrInvalid
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		dst := filepath.Join(dir, rel)
		if e = os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
			return e
		}
		return os.WriteFile(dst, b, 0600)
	})
	if err != nil {
		return err
	}
	for _, c := range j.Files {
		dst := filepath.Join(dir, c.Path)
		if err = os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return err
		}
		if err = os.WriteFile(dst, c.After, 0600); err != nil {
			return err
		}
	}
	lock, err := lockDirectory(dir)
	if err != nil {
		return err
	}
	mirror := &Store{dir: dir, lock: lock, ops: defaultSnapshotOps()}
	defer mirror.Close()
	m := &roomManager{store: mirror}
	m.initPlatform()
	if _, err = m.platforms.Load(); err != nil {
		return err
	}
	err = readSnapshot(m.indexPath(), &m.index, maxRoomsFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = m.validateIndex(); err != nil {
		return err
	}
	for _, entry := range m.index.Rooms {
		if entry.Status != "ready" {
			continue
		}
		if _, err = m.readToolBindingsLocked(entry.RoomIdentity); err != nil {
			return err
		}
		rs := &Store{dir: m.roomDir(entry.RoomID), roomID: entry.RoomID, instance: entry.RoomInstance, lock: lock, ops: defaultSnapshotOps()}
		state, e := rs.loadRoom()
		if e != nil {
			return e
		}
		if e = rs.checkReferenceRoom(state); e != nil {
			return e
		}
		if e = validatePlatformFollowups(rs); e != nil {
			return e
		}
	}
	return m.loadPlatformNotificationsLocked()
}

func validatePlatformFollowups(s *Store) error {
	return filepath.WalkDir(filepath.Join(s.dir, "agents"), func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel, _ := filepath.Rel(s.dir, path)
		p := strings.Split(filepath.ToSlash(rel), "/")
		if len(p) < 5 || p[2] != "followups" {
			return nil
		}
		switch p[3] {
		case "events":
			var event followupEvent
			if err := readSnapshot(path, &event, followupBytes); err != nil {
				return err
			}
			var inbox InboxEntry
			if err := readSnapshot(filepath.Join(s.dir, "inboxes", event.MembershipID, inboxEntryName(event.Seq)), &inbox, DefaultInboxLimits().EntryBytes); err != nil {
				return err
			}
			_, err := s.loadFollowupEventLocked(inbox)
			return err
		case "objects":
			if len(p) == 7 && p[5] == "units" {
				var u followupUnit
				if err := readSnapshot(path, &u, followupBytes); err != nil {
					return err
				}
				_, err := s.loadUnitLocked(u.MemberIdentity, p[4], u.Ref)
				return err
			}
		}
		return nil
	})
}
