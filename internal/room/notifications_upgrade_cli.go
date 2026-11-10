package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// These explicit offline commands convert local notification settings/scopes.
// They never start collectors, query a provider, or change progress and receipts.
func runNotificationsUpgrade(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "upgrade" && args[0] != "upgrade-lark") {
		return fmt.Errorf("usage: teamx notifications upgrade|upgrade-lark --rooms-dir ROOT (service must be stopped)")
	}
	flags := flag.NewFlagSet("notifications upgrade", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("rooms-dir", "", "existing service root")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" || flags.NArg() != 0 {
		return ErrRequest
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	lock, err := lockDirectory(abs)
	if err != nil {
		return err
	}
	store := &Store{dir: abs, lock: lock, ops: defaultSnapshotOps()}
	defer store.Close()
	m := &roomManager{store: store}
	m.initPlatform()
	if err := readSnapshot(m.indexPath(), &m.index, maxRoomsFileBytes); err != nil {
		return err
	}
	if err := m.validateIndex(); err != nil {
		return err
	}
	if _, err := m.platforms.Load(); err != nil {
		return err
	}
	if args[0] == "upgrade-lark" {
		count, err := m.upgradeLarkNotificationsLocked()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "Lark notification schema 2; upgraded %d snapshots. Private .v1.bak backups and lark-notifications-upgrade.json preserve original state and recovery progress.\n", count)
		return err
	}
	count, err := m.upgradeGitLabNotificationsLocked()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "GitLab notification schema %d; upgraded %d Room settings. Platform choices, source snapshots, subscription IDs, progress, pending items and receipts are preserved. Private .vN.bak backups are beside settings; .v0.bak containing null marks previously absent settings.\n", gitlabNotificationSchema, count)
	return err
}

type gitlabNotificationUpgrade struct {
	path, backup string
	original     json.RawMessage
	next         gitlabNotificationFile
}

func readPrivateNotificationJSON(path string) (json.RawMessage, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxGitLabConfigBytes {
		return nil, ErrInvalid
	}
	var data json.RawMessage
	if err := readSnapshot(path, &data, maxGitLabConfigBytes); err != nil {
		return nil, err
	}
	return data, nil
}

func decodeNotificationJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func (m *roomManager) upgradeGitLabNotificationsLocked() (int, error) {
	// Validate the complete conversion set and every backup before any mutation.
	var changes []gitlabNotificationUpgrade
	for _, entry := range m.index.Rooms {
		if entry.Status != "ready" {
			continue
		}
		owner := entry.RoomIdentity
		if err := m.notifications.LoadLocked(owner); err != nil {
			return 0, err
		}
		path := m.gitlabNotificationPath(owner)
		original, err := readPrivateNotificationJSON(path)
		absent := errors.Is(err, os.ErrNotExist)
		if err != nil && !absent {
			return 0, err
		}
		var schema struct {
			Schema int `json:"schema"`
		}
		if !absent && json.Unmarshal(original, &schema) != nil {
			return 0, ErrInvalid
		}
		if schema.Schema == gitlabNotificationSchema {
			if err := m.loadGitLabNotificationsLocked(owner); err != nil {
				return 0, err
			}
			continue
		}
		next := gitlabNotificationFile{RoomIdentity: owner, Schema: gitlabNotificationSchema, Platforms: map[string]gitlabNotificationRecord{}}
		var legacy *gitlabNotificationRecord
		if !absent {
			if schema.Schema != 2 {
				return 0, ErrVersion
			}
			var saved gitlabNotificationRecord
			if err := decodeNotificationJSON(original, &saved); err != nil {
				return 0, err
			}
			// Schema 2 has no master switch. Its category flags are the exact opt-in.
			saved.Config.Enabled = true
			cfg, err := gitlab.NormalizeNotificationConfig(saved.Config)
			if err != nil || saved.RoomIdentity != owner || saved.Version == 0 || !reflect.DeepEqual(cfg, saved.Config) {
				return 0, ErrInvalid
			}
			saved.Schema = gitlabNotificationSchema
			next.Platforms[cfg.PlatformID] = saved
			legacy = &saved
		}
		for _, id := range m.gitlabPlatformIDsLocked(owner) {
			if _, exists := next.Platforms[id]; exists {
				continue
			}
			// A half-applied legacy account switch cannot be reinterpreted as an
			// independent opt-in without an explicit operator choice.
			if legacy != nil {
				return 0, fmt.Errorf("%w: Room %s has a pending cross-platform GitLab configuration; resolve it with the previous version before upgrade", ErrConflict, owner.RoomID)
			}
			view, err := m.gitlabNotificationViewLocked(owner, id)
			if err != nil {
				return 0, err
			}
			if view.Errors["config"] != "" {
				return 0, fmt.Errorf("%w: Room %s platform %s has incompatible channel settings; source data is unchanged", ErrConflict, owner.RoomID, id)
			}
			next.Platforms[id] = gitlabNotificationRecord{RoomIdentity: owner, Schema: gitlabNotificationSchema, Version: 1, Config: view.Config, BackfillLocked: view.BackfillLocked}
		}
		if len(next.Platforms) == 0 {
			continue
		}
		if err := validateGitLabNotificationFile(owner, next); err != nil {
			return 0, err
		}
		if absent {
			original = json.RawMessage("null")
		}
		backup := fmt.Sprintf("%s.v%d.bak", path, schema.Schema)
		prior, err := readPrivateNotificationJSON(backup)
		if err == nil {
			var a, b bytes.Buffer
			if json.Compact(&a, prior) != nil || json.Compact(&b, original) != nil || !bytes.Equal(a.Bytes(), b.Bytes()) {
				return 0, fmt.Errorf("%w: notification backup differs", ErrConflict)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		changes = append(changes, gitlabNotificationUpgrade{path, backup, original, next})
	}
	for _, change := range changes {
		if _, err := os.Lstat(change.backup); errors.Is(err, os.ErrNotExist) {
			if err := m.store.save(change.backup, change.original, maxGitLabConfigBytes); err != nil {
				return 0, err
			}
		} else if err != nil {
			return 0, err
		}
	}
	count := 0
	for _, change := range changes {
		if err := m.store.save(change.path, change.next, maxGitLabConfigBytes); err != nil {
			return count, err
		}
		m.gitlabNotifications[change.next.RoomIdentity] = change.next.Platforms
		count++
	}
	return count, nil
}
