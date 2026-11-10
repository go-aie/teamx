package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

const larkUpgradeJournal = "lark-notifications-upgrade.json"

func checkLarkNotificationUpgrade(root string) error {
	var j platformUpgrade
	err := readLarkUpgradeJournal(filepath.Join(root, larkUpgradeJournal), &j)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.Version != 1 || !j.Complete {
		return fmt.Errorf("%w: Lark notification upgrade incomplete; stop service and run teamx notifications upgrade-lark --rooms-dir ROOT", ErrVersion)
	}
	return nil
}

func (m *roomManager) upgradeLarkNotificationsLocked() (int, error) {
	journal := filepath.Join(m.store.dir, larkUpgradeJournal)
	var j platformUpgrade
	err := readLarkUpgradeJournal(journal, &j)
	if err == nil {
		if j.Version != 1 {
			return 0, ErrVersion
		}
		if !j.Complete {
			return len(j.Files), commitLarkNotificationUpgrade(m.store, j)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	j = platformUpgrade{Version: 1}
	active := map[string]bool{}
	for _, room := range m.index.Rooms {
		if room.Status != "ready" {
			continue
		}
		dir := filepath.Join(m.roomDir(room.RoomID), "notifications")
		info, e := os.Lstat(dir)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return 0, e
		}
		if !info.IsDir() {
			return 0, ErrInvalid
		}
		if err := validateLarkUpgradeDirectories(m.store.dir, room.RoomID); err != nil {
			return 0, err
		}
		entries, e := os.ReadDir(dir)
		if e != nil {
			return 0, e
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "lark.messages.") || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			before, e := readPrivateLarkSnapshot(path)
			if e != nil {
				return 0, e
			}
			if int64(len(before)) > notification.DefaultLimits().Bytes {
				return 0, ErrCapacity
			}
			var wire struct {
				notification.SourceID
				Schema int             `json:"schema"`
				State  json.RawMessage `json:"state"`
			}
			if decodeNotificationJSON(before, &wire) != nil || wire.SourceID != lark.MessagesSourceID {
				return 0, ErrInvalid
			}
			if wire.Schema == (&lark.Source{}).Schema() {
				state, e := decodeLarkUpgradeState(wire.State, room.RoomIdentity)
				if e != nil {
					return 0, e
				}
				if !state.Metadata().Retired {
					key := room.RoomInstance + "/" + state.Metadata().PlatformID
					if active[key] {
						return 0, ErrConflict
					}
					active[key] = true
				}
				if entry.Name() != "lark.messages."+state.Metadata().Key.SubscriptionID+".json" {
					return 0, ErrInvalid
				}
				continue
			}
			if wire.Schema != 1 {
				return 0, ErrVersion
			}
			wire.State, e = lark.UpgradeState(wire.State, room.RoomIdentity)
			if e != nil {
				return 0, fmt.Errorf("Room %s, %s: %w", room.RoomID, entry.Name(), e)
			}
			state, e := decodeLarkUpgradeState(wire.State, room.RoomIdentity)
			if e != nil || entry.Name() != "lark.messages."+state.Metadata().Key.SubscriptionID+".json" {
				return 0, ErrInvalid
			}
			if !state.Metadata().Retired {
				key := room.RoomInstance + "/" + state.Metadata().PlatformID
				if active[key] {
					return 0, ErrConflict
				}
				active[key] = true
			}
			wire.Schema = (&lark.Source{}).Schema()
			after, e := json.Marshal(wire)
			if e != nil {
				return 0, e
			}
			if int64(len(after)) > notification.DefaultLimits().Bytes {
				return 0, ErrCapacity
			}
			if backup, e := readPrivateLarkSnapshot(path + ".v1.bak"); e == nil {
				if !equalPlatformJSON(backup, before) {
					return 0, ErrConflict
				}
			} else if !errors.Is(e, os.ErrNotExist) {
				return 0, e
			}
			rel, e := filepath.Rel(m.store.dir, path)
			if e != nil {
				return 0, e
			}
			j.Files = append(j.Files, platformFileChange{Path: rel, Before: before, After: after})
		}
	}
	if len(j.Files) == 0 {
		return 0, nil
	}
	// A completed journal is immutable recovery evidence. New legacy files must
	// be handled from a consistent full backup, not overwrite that evidence.
	if _, e := os.Lstat(journal); e == nil {
		return 0, ErrConflict
	} else if !errors.Is(e, os.ErrNotExist) {
		return 0, e
	}
	if err := m.store.save(journal, j, platformUpgradeLimit); err != nil {
		return 0, err
	}
	return len(j.Files), commitLarkNotificationUpgrade(m.store, j)
}
func readLarkUpgradeJournal(path string, j *platformUpgrade) error {
	raw, err := readPrivateLarkSnapshot(path)
	if err != nil {
		return err
	}
	return decodeNotificationJSON(raw, j)
}
func readPrivateLarkSnapshot(path string) (json.RawMessage, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > platformUpgradeLimit {
		return nil, ErrInvalid
	}
	var b json.RawMessage
	err = readSnapshot(path, &b, platformUpgradeLimit)
	return b, err
}
func commitLarkNotificationUpgrade(s *Store, j platformUpgrade) error {
	// Preflight every path and preimage before writing any backup or candidate.
	if j.Version != 1 || j.Complete || len(j.Files) == 0 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, f := range j.Files {
		parts := strings.Split(filepath.ToSlash(f.Path), "/")
		if len(parts) != 4 || parts[0] != "rooms" || !safeMembershipID(parts[1]) || parts[2] != "notifications" || !strings.HasPrefix(parts[3], "lark.messages.") || !strings.HasSuffix(parts[3], ".json") || seen[f.Path] {
			return ErrInvalid
		}
		// Recovery evidence must describe an actual, deterministic schema-1
		// conversion, rather than arbitrary replacement JSON.
		var before, after struct {
			notification.SourceID
			Schema int             `json:"schema"`
			State  json.RawMessage `json:"state"`
		}
		if decodeNotificationJSON(f.Before, &before) != nil || decodeNotificationJSON(f.After, &after) != nil || before.SourceID != lark.MessagesSourceID || after.SourceID != lark.MessagesSourceID || before.Schema != 1 || after.Schema != 2 {
			return ErrInvalid
		}
		var owner RoomIdentity
		if json.Unmarshal(after.State, &owner) != nil || owner.RoomID != parts[1] {
			return ErrInvalid
		}
		state, err := decodeLarkUpgradeState(after.State, owner)
		if err != nil || parts[3] != "lark.messages."+state.Metadata().Key.SubscriptionID+".json" {
			return ErrInvalid
		}
		candidate, err := lark.UpgradeState(before.State, owner)
		if err != nil || !equalPlatformJSON(candidate, after.State) {
			return ErrInvalid
		}
		seen[f.Path] = true
		path := filepath.Join(s.dir, f.Path)
		if err := validateLarkUpgradeDirectories(s.dir, parts[1]); err != nil {
			return err
		}
		b, e := readPrivateLarkSnapshot(path)
		if e != nil {
			return e
		}
		if !equalPlatformJSON(b, f.Before) && !equalPlatformJSON(b, f.After) {
			return ErrConflict
		}
		if b, e := readPrivateLarkSnapshot(path + ".v1.bak"); e == nil {
			if !equalPlatformJSON(b, f.Before) {
				return ErrConflict
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	for _, f := range j.Files {
		path := filepath.Join(s.dir, f.Path)
		if _, e := os.Lstat(path + ".v1.bak"); errors.Is(e, os.ErrNotExist) {
			if e = s.save(path+".v1.bak", f.Before, platformUpgradeLimit); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
	}
	for _, f := range j.Files {
		if err := s.save(filepath.Join(s.dir, f.Path), f.After, platformUpgradeLimit); err != nil {
			return err
		}
	}
	j.Complete = true
	return s.save(filepath.Join(s.dir, larkUpgradeJournal), j, platformUpgradeLimit)
}

func validateLarkUpgradeDirectories(root, roomID string) error {
	room := filepath.Join(root, "rooms", roomID)
	for _, dir := range []string{filepath.Join(root, "rooms"), room, filepath.Join(room, "notifications")} {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return ErrInvalid
		}
	}
	return nil
}

func decodeLarkUpgradeState(raw []byte, owner RoomIdentity) (notification.State, error) {
	state, err := (&lark.Source{}).Decode(raw, owner)
	if err != nil {
		return nil, err
	}
	limits := notification.DefaultLimits()
	meta := state.Metadata()
	if int64(len(raw)) > limits.Bytes || meta.Observed > limits.Observed || meta.Pending > limits.Inbox {
		return nil, ErrCapacity
	}
	return state, nil
}
